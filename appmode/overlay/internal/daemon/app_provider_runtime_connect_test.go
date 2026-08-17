package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentdc/internal/config"
	"agentdc/internal/store"
)

const runtimeConnectFutureKind = "future-cli"

type runtimeConnectEventLog struct {
	mu     sync.Mutex
	events []string
}

func (log *runtimeConnectEventLog) add(event string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.events = append(log.events, event)
}

func (log *runtimeConnectEventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.events...)
}

type runtimeConnectDriverProbe struct {
	events        *runtimeConnectEventLog
	installed     bool
	auth          authState
	detectEntered chan struct{}
	releaseDetect chan struct{}
	detectOnce    sync.Once

	mu        sync.Mutex
	configDir string
}

type runtimeConnectNilWaitDriver struct{}

func (runtimeConnectNilWaitDriver) Detect() (bool, error) { return true, nil }
func (runtimeConnectNilWaitDriver) Install(context.Context, func(string)) error {
	return nil
}
func (runtimeConnectNilWaitDriver) Login(
	context.Context,
	string,
) (string, string, func() error, error) {
	return "https://future.invalid/login", "FUTURE-CODE", nil, nil
}
func (runtimeConnectNilWaitDriver) PollAuth(string) authState  { return authLoggedOut }
func (runtimeConnectNilWaitDriver) AccountLabel(string) string { return "" }

func (driver *runtimeConnectDriverProbe) Detect() (bool, error) {
	driver.events.add("detect")
	if driver.detectEntered != nil {
		driver.detectOnce.Do(func() { close(driver.detectEntered) })
	}
	if driver.releaseDetect != nil {
		<-driver.releaseDetect
	}
	return driver.installed, nil
}

func (driver *runtimeConnectDriverProbe) Install(
	_ context.Context,
	onLine func(string),
) error {
	driver.events.add("install")
	if onLine != nil {
		onLine("installing future runtime")
	}
	return nil
}

func (driver *runtimeConnectDriverProbe) Login(
	ctx context.Context,
	configDir string,
) (string, string, func() error, error) {
	driver.events.add("login")
	driver.mu.Lock()
	driver.configDir = configDir
	driver.mu.Unlock()
	return "https://future.invalid/login", "FUTURE-CODE", func() error {
		<-ctx.Done()
		return ctx.Err()
	}, nil
}

func (driver *runtimeConnectDriverProbe) PollAuth(configDir string) authState {
	driver.events.add("poll")
	driver.mu.Lock()
	driver.configDir = configDir
	driver.mu.Unlock()
	return driver.auth
}

func (driver *runtimeConnectDriverProbe) AccountLabel(configDir string) string {
	driver.events.add("label")
	driver.mu.Lock()
	driver.configDir = configDir
	driver.mu.Unlock()
	return "Future account"
}

func (driver *runtimeConnectDriverProbe) seenConfigDir() string {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.configDir
}

type runtimeConnectHarness struct {
	a        *api
	ctx      appRuntimeContext
	mux      *http.ServeMux
	manager  *connectManager
	root     string
	idCalls  atomic.Int32
	registry appProviderRuntimeRegistry
}

func newRuntimeConnectHarness(
	t *testing.T,
	registry appProviderRuntimeRegistry,
	accountID string,
) *runtimeConnectHarness {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "portal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &api{
		cfg: config.Config{Dir: root}, st: st, logger: logger, portalOpen: true,
	}
	harness := &runtimeConnectHarness{a: a, root: root, registry: registry}
	// Legacy fields stay populated so the pre-Task-5 implementation fails by
	// observable dispatch/rejection rather than panicking in its background job.
	harness.manager = &connectManager{
		runner:           &fakeRunner{installed: true, auth: authLoggedOut},
		ensure:           func(string) error { return nil },
		ensureOnboarding: func(string) error { return nil },
		ensureModels:     func(string) error { return nil },
		createAccount:    func(store.LLMAccount) error { return nil },
		bindOnboarding: func(int64, string, store.LLMAccount) (store.OnboardingSnapshot, error) {
			return store.OnboardingSnapshot{}, nil
		},
		dataDir: root,
		newID: func() string {
			harness.idCalls.Add(1)
			return accountID
		},
		logger:       logger,
		loginTimeout: 300 * time.Millisecond,
		pollInterval: 2 * time.Millisecond,
	}
	harness.ctx = appRuntimeContext{api: a, registry: registry, connect: harness.manager}
	harness.mux = http.NewServeMux()
	previous := connectMgr
	registerAppRoutesWithContext(harness.mux, harness.ctx)
	t.Cleanup(func() {
		harness.manager.cancel(runtimeConnectFutureKind)
		harness.manager.cancel("codex")
		connectMgr = previous
	})
	return harness
}

func runtimeConnectRegistry(
	t *testing.T,
	mutate func(*appProviderRuntimeRegistration),
) appProviderRuntimeRegistry {
	t.Helper()
	registrations := appRuntimeSyntheticRegistrations()
	index := appRuntimeRegistrationIndex(t, registrations, runtimeConnectFutureKind)
	if mutate != nil {
		mutate(&registrations[index])
	}
	registry, err := newAppProviderRuntimeRegistry(appRuntimeSyntheticOptions(), registrations)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func runtimeConnectPrepareOnboarding(
	t *testing.T,
	ctx appRuntimeContext,
	kind string,
) store.OnboardingSnapshot {
	t.Helper()
	onboarding := ctx.onboardingStore()
	initial, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	selected, err := onboarding.ReplaceOnboardingProviderSelection(
		initial.State.Revision,
		[]string{kind},
	)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := onboarding.BeginOnboardingProvider(selected.State.Revision, kind)
	if err != nil {
		t.Fatal(err)
	}
	return connected
}

func runtimeConnectRequest(
	t *testing.T,
	harness *runtimeConnectHarness,
	method, kind, body string,
	wantStatus int,
) []byte {
	t.Helper()
	return serveAppPortalRoute(
		t,
		harness.mux,
		method,
		"/llm/providers/"+kind+"/connect",
		body,
		wantStatus,
	)
}

func runtimeConnectStoreDigest(t *testing.T, harness *runtimeConnectHarness) string {
	t.Helper()
	providers, err := harness.a.st.LLMProviders()
	if err != nil {
		t.Fatal(err)
	}
	type providerRows struct {
		Provider store.LLMProvider
		Accounts []store.LLMAccount
		Models   []store.LLMModel
	}
	rows := make([]providerRows, 0, len(providers))
	for _, provider := range providers {
		accounts, err := harness.a.st.LLMAccounts(provider.ID)
		if err != nil {
			t.Fatal(err)
		}
		models, err := harness.a.st.LLMModels(provider.ID)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, providerRows{Provider: provider, Accounts: accounts, Models: models})
	}
	snapshot, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(struct {
		Rows     []providerRows
		Snapshot store.OnboardingSnapshot
	}{Rows: rows, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func runtimeConnectProvider(
	t *testing.T,
	harness *runtimeConnectHarness,
	kind string,
) (store.LLMProvider, bool) {
	t.Helper()
	providers, err := harness.a.st.LLMProviders()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range providers {
		if provider.ID == kind {
			return provider, true
		}
	}
	return store.LLMProvider{}, false
}

func TestRuntimeConnectRejectsMissingDriverBeforeFilesystemMutation(t *testing.T) {
	tests := []struct {
		name             string
		kind             string
		body             string
		corrupt          func(*appProviderRuntimeRegistration)
		assertFactory    bool
		wantFactoryCalls int32
	}{
		{name: "unknown kind", kind: "unknown-runtime", assertFactory: true},
		{name: "OpenCode is denied", kind: "opencode", assertFactory: true},
		{
			name: "factory returns nil driver", kind: runtimeConnectFutureKind,
			corrupt: func(registration *appProviderRuntimeRegistration) {
				registration.Connect = func(*slog.Logger) appConnectDriver { return nil }
			},
			assertFactory: true, wantFactoryCalls: 1,
		},
		{
			name: "missing account strategy", kind: runtimeConnectFutureKind,
			corrupt: func(registration *appProviderRuntimeRegistration) {
				registration.EnsureAccountProvider = nil
			},
		},
		{
			name: "missing model seeder", kind: runtimeConnectFutureKind,
			corrupt: func(registration *appProviderRuntimeRegistration) {
				registration.SeedConnectedModels = nil
			},
		},
		{
			name:          "driver resolves before onboarding preflight",
			kind:          runtimeConnectFutureKind,
			body:          `{"onboarding_revision":999}`,
			assertFactory: true, wantFactoryCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var factoryCalls atomic.Int32
			events := &runtimeConnectEventLog{}
			driver := &runtimeConnectDriverProbe{
				events: events, installed: true, auth: authLoggedIn,
			}
			registry := runtimeConnectRegistry(t, func(registration *appProviderRuntimeRegistration) {
				registration.Connect = func(*slog.Logger) appConnectDriver {
					factoryCalls.Add(1)
					return driver
				}
			})
			if test.corrupt != nil {
				registration := registry.byKind[runtimeConnectFutureKind]
				test.corrupt(&registration)
				if test.name == "factory returns nil driver" {
					originalFactory := registration.Connect
					registration.Connect = func(logger *slog.Logger) appConnectDriver {
						factoryCalls.Add(1)
						return originalFactory(logger)
					}
				}
				registry.byKind[runtimeConnectFutureKind] = registration
			}
			harness := newRuntimeConnectHarness(t, registry, "must-not-be-minted")
			before := runtimeConnectStoreDigest(t, harness)

			wantStatus := http.StatusBadRequest
			if test.body != "" {
				wantStatus = http.StatusConflict
			}
			runtimeConnectRequest(t, harness, http.MethodPost, test.kind, test.body, wantStatus)

			if got := harness.idCalls.Load(); got != 0 {
				t.Fatalf("newID calls = %d; want 0 before admission", got)
			}
			if got := factoryCalls.Load(); test.assertFactory && got != test.wantFactoryCalls {
				t.Fatalf("driver factory calls = %d; want %d", got, test.wantFactoryCalls)
			}
			if got := events.snapshot(); len(got) != 0 {
				t.Fatalf("rejected Connect entered driver: %v", got)
			}
			if after := runtimeConnectStoreDigest(t, harness); after != before {
				t.Fatalf("rejected Connect mutated Store:\nbefore=%s\nafter=%s", before, after)
			}
			if _, err := os.Stat(filepath.Join(harness.root, "accounts")); !os.IsNotExist(err) {
				t.Fatalf("rejected Connect created account filesystem: %v", err)
			}
		})
	}
}

func TestRuntimeConnectSeedsModelsBeforeBindingOnboarding(t *testing.T) {
	events := &runtimeConnectEventLog{}
	driver := &runtimeConnectDriverProbe{events: events, installed: false, auth: authLoggedIn}
	seedEntered := make(chan struct{})
	seedRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseSeed := func() { releaseOnce.Do(func() { close(seedRelease) }) }
	t.Cleanup(releaseSeed)
	var harness *runtimeConnectHarness
	var startSnapshot store.OnboardingSnapshot
	registry := runtimeConnectRegistry(t, func(registration *appProviderRuntimeRegistration) {
		registration.Connect = func(*slog.Logger) appConnectDriver {
			events.add("factory")
			return driver
		}
		registration.EnsureAccountProvider = func(*store.Store) error {
			return errors.New("normal account strategy must not run during onboarding")
		}
		registration.SeedConnectedModels = func(st *store.Store, providerID string) error {
			events.add("seed")
			if providerID != runtimeConnectFutureKind {
				return errors.New("seeder did not receive the Account ProviderID")
			}
			providers, err := st.LLMProviders()
			if err != nil {
				return err
			}
			provider := store.LLMProvider{}
			providerExists := false
			for _, candidate := range providers {
				if candidate.ID == runtimeConnectFutureKind {
					provider = candidate
					providerExists = true
					break
				}
			}
			if !providerExists || provider.Enabled {
				return errors.New("onboarding Provider was not ensured disabled before seed")
			}
			accounts, err := st.LLMAccounts(providerID)
			if err != nil || len(accounts) != 0 {
				return errors.New("Account was persisted before model seed")
			}
			snapshot, err := harness.ctx.onboardingStore().OnboardingSnapshot()
			if err != nil || snapshot.State != startSnapshot.State ||
				!reflect.DeepEqual(snapshot.Stages, startSnapshot.Stages) {
				return errors.New("onboarding advanced before model seed")
			}
			close(seedEntered)
			<-seedRelease
			return st.ReplaceLLMModels(providerID, store.LLMModelDiscovered, []store.LLMModel{{
				ProviderID: providerID, ModelID: "future-model", Name: "Future Model", Available: true,
			}})
		}
	})
	harness = newRuntimeConnectHarness(t, registry, "future-onboarding-account")
	startSnapshot = runtimeConnectPrepareOnboarding(t, harness.ctx, runtimeConnectFutureKind)
	body := `{"onboarding_revision":` + strconv.FormatInt(startSnapshot.State.Revision, 10) + `}`
	runtimeConnectRequest(t, harness, http.MethodPost, runtimeConnectFutureKind, body, http.StatusOK)

	select {
	case <-seedEntered:
	case <-time.After(time.Second):
		t.Fatal("Connect did not reach model seed")
	}
	if state, ok := harness.manager.status(runtimeConnectFutureKind); !ok ||
		state.Phase != phasePolling || state.ProviderID != "" || state.AccountID != "" {
		t.Fatalf("state while seed is blocked = %+v, ok=%v", state, ok)
	}
	releaseSeed()
	terminal := waitPhase(t, harness.manager, runtimeConnectFutureKind, phaseConnected)
	if terminal.ProviderID != runtimeConnectFutureKind || terminal.AccountID != "future-onboarding-account" {
		t.Fatalf("terminal state = %+v", terminal)
	}

	final, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if final.State.Phase != store.OnboardingPhaseSetup ||
		final.State.Revision != startSnapshot.State.Revision+1 ||
		final.State.ProviderID != runtimeConnectFutureKind ||
		final.State.AccountID != "future-onboarding-account" {
		t.Fatalf("bound onboarding snapshot = %+v", final)
	}
	accounts, err := harness.a.st.LLMAccounts(runtimeConnectFutureKind)
	if err != nil || len(accounts) != 1 || accounts[0].Enabled {
		t.Fatalf("staged Accounts = %+v, err=%v", accounts, err)
	}
	models, err := harness.a.st.LLMModels(runtimeConnectFutureKind)
	if err != nil || len(models) != 1 || models[0].ModelID != "future-model" {
		t.Fatalf("seeded models = %+v, err=%v", models, err)
	}
	wantEvents := []string{"factory", "detect", "install", "login", "poll", "label", "seed"}
	if got := events.snapshot(); !slices.Equal(got, wantEvents) {
		t.Fatalf("Connect order = %v; want %v", got, wantEvents)
	}
}

func TestRuntimeConnectSeedFailureCleansAccountWithoutAdvancing(t *testing.T) {
	events := &runtimeConnectEventLog{}
	driver := &runtimeConnectDriverProbe{events: events, installed: true, auth: authLoggedIn}
	var harness *runtimeConnectHarness
	registry := runtimeConnectRegistry(t, func(registration *appProviderRuntimeRegistration) {
		registration.Connect = func(*slog.Logger) appConnectDriver { return driver }
		registration.EnsureAccountProvider = func(*store.Store) error {
			return errors.New("normal account strategy must not run during onboarding")
		}
		registration.SeedConnectedModels = func(st *store.Store, providerID string) error {
			if providerID != runtimeConnectFutureKind {
				return errors.New("wrong ProviderID")
			}
			accounts, err := st.LLMAccounts(providerID)
			if err != nil || len(accounts) != 0 {
				return errors.New("Account existed before failed seed")
			}
			return errors.New("synthetic model seed failed at a private path")
		}
	})
	harness = newRuntimeConnectHarness(t, registry, "owned-leaf")
	start := runtimeConnectPrepareOnboarding(t, harness.ctx, runtimeConnectFutureKind)
	kindRoot := filepath.Join(harness.root, "accounts", runtimeConnectFutureKind)
	sibling := filepath.Join(kindRoot, "sibling", "keep.txt")
	parentMarker := filepath.Join(kindRoot, "keep-parent.txt")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, []byte("keep sibling"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parentMarker, []byte("keep parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"onboarding_revision":` + strconv.FormatInt(start.State.Revision, 10) + `}`
	runtimeConnectRequest(t, harness, http.MethodPost, runtimeConnectFutureKind, body, http.StatusOK)
	terminal := waitPhase(t, harness.manager, runtimeConnectFutureKind, phaseError)
	waitConnectJobDone(t, harness.manager)
	if terminal.ProviderID != "" || terminal.AccountID != "" ||
		terminal.Error != "không lưu được kết nối" {
		t.Fatalf("seed failure terminal = %+v", terminal)
	}

	after, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.State != start.State || !reflect.DeepEqual(after.Stages, start.Stages) {
		t.Fatalf("seed failure advanced onboarding:\nbefore=%+v\nafter=%+v", start, after)
	}
	accounts, err := harness.a.st.LLMAccounts(runtimeConnectFutureKind)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("seed failure Accounts = %+v, err=%v", accounts, err)
	}
	models, err := harness.a.st.LLMModels(runtimeConnectFutureKind)
	if err != nil || len(models) != 0 {
		t.Fatalf("seed failure models = %+v, err=%v", models, err)
	}
	provider, exists := runtimeConnectProvider(t, harness, runtimeConnectFutureKind)
	if !exists || provider.Enabled {
		t.Fatalf("ensured staging Provider = %+v, exists=%v; want retained disabled singleton", provider, exists)
	}
	if _, err := os.Stat(filepath.Join(kindRoot, "owned-leaf")); !os.IsNotExist(err) {
		t.Fatalf("failed seed retained owned account leaf: %v", err)
	}
	for _, retained := range []string{sibling, parentMarker} {
		if _, err := os.Stat(retained); err != nil {
			t.Fatalf("failed seed removed unowned path %q: %v", retained, err)
		}
	}
}

func TestRuntimeConnectSyntheticDriverUsesOnlyOwnedRoot(t *testing.T) {
	events := &runtimeConnectEventLog{}
	detectEntered := make(chan struct{})
	detectRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseDetect := func() { releaseOnce.Do(func() { close(detectRelease) }) }
	t.Cleanup(releaseDetect)
	driver := &runtimeConnectDriverProbe{
		events: events, installed: true, auth: authLoggedIn,
		detectEntered: detectEntered, releaseDetect: detectRelease,
	}
	var originalEnsureCalls atomic.Int32
	var originalSeedCalls atomic.Int32
	var evilCalls atomic.Int32
	registry := runtimeConnectRegistry(t, func(registration *appProviderRuntimeRegistration) {
		registration.Connect = func(*slog.Logger) appConnectDriver {
			events.add("factory")
			return driver
		}
		registration.EnsureAccountProvider = func(st *store.Store) error {
			originalEnsureCalls.Add(1)
			return st.EnsureAccountRuntimeProvider(runtimeConnectFutureKind, "Future CLI")
		}
		registration.SeedConnectedModels = func(st *store.Store, providerID string) error {
			originalSeedCalls.Add(1)
			return st.ReplaceLLMModels(providerID, store.LLMModelDiscovered, []store.LLMModel{{
				ProviderID: providerID, ModelID: "future-model", Name: "Future Model", Available: true,
			}})
		}
	})
	harness := newRuntimeConnectHarness(t, registry, "owned-account")
	sibling := filepath.Join(harness.root, "accounts", runtimeConnectFutureKind, "sibling", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A context route must stay bound to its manager even when a legacy test
	// swaps the package singleton after registration.
	decoyEvents := &runtimeConnectEventLog{}
	decoy := newTestManager(
		t,
		&runtimeConnectDriverAsLegacy{driver: &runtimeConnectDriverProbe{
			events: decoyEvents, installed: true, auth: authLoggedOut,
		}},
		func(string) error { return nil },
		func(store.LLMAccount) error { return nil },
	)
	connectMgr = decoy
	runtimeConnectRequest(t, harness, http.MethodPost, runtimeConnectFutureKind, `{}`, http.StatusOK)
	select {
	case <-detectEntered:
	case <-time.After(time.Second):
		t.Fatal("context-bound synthetic driver did not start")
	}

	// One manager owns one slot across kinds. Resolving Codex is harmless, but
	// it must not create another directory or start a production process.
	runtimeConnectRequest(t, harness, http.MethodPost, "codex", `{}`, http.StatusConflict)
	if got := harness.idCalls.Load(); got != 1 {
		t.Fatalf("newID calls while second kind is busy = %d; want 1", got)
	}

	// Mutate the registry map only after Detect proves external I/O has begun.
	// The in-flight job must retain the already-bound driver and callbacks.
	mutated := harness.registry.byKind[runtimeConnectFutureKind]
	mutated.Connect = func(*slog.Logger) appConnectDriver {
		evilCalls.Add(1)
		return nil
	}
	mutated.EnsureAccountProvider = func(*store.Store) error {
		evilCalls.Add(1)
		return errors.New("late registry ensure")
	}
	mutated.SeedConnectedModels = func(*store.Store, string) error {
		evilCalls.Add(1)
		return errors.New("late registry seed")
	}
	harness.registry.byKind[runtimeConnectFutureKind] = mutated
	releaseDetect()
	terminal := waitPhase(t, harness.manager, runtimeConnectFutureKind, phaseConnected)
	if terminal.ProviderID != runtimeConnectFutureKind || terminal.AccountID != "owned-account" {
		t.Fatalf("terminal state = %+v", terminal)
	}
	if got := evilCalls.Load(); got != 0 {
		t.Fatalf("in-flight job reread mutated registry %d times", got)
	}
	if originalEnsureCalls.Load() != 1 || originalSeedCalls.Load() != 1 {
		t.Fatalf("captured callbacks ensure=%d seed=%d; want 1/1",
			originalEnsureCalls.Load(), originalSeedCalls.Load())
	}
	if got := decoyEvents.snapshot(); len(got) != 0 {
		t.Fatalf("context routes fell back to global manager: %v", got)
	}
	expectedDir := filepath.Join(harness.root, "accounts", runtimeConnectFutureKind, "owned-account")
	if got := driver.seenConfigDir(); got != expectedDir {
		t.Fatalf("driver config dir = %q; want owned leaf %q", got, expectedDir)
	}
	accounts, err := harness.a.st.LLMAccounts(runtimeConnectFutureKind)
	if err != nil || len(accounts) != 1 || !accounts[0].Enabled || accounts[0].ConfigDir != expectedDir {
		t.Fatalf("normal Connect Accounts = %+v, err=%v", accounts, err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("successful Connect touched sibling path: %v", err)
	}
}

// runtimeConnectDriverAsLegacy is test-fixture glue only: old connectManager
// tests continue injecting connectRunner while context routes use appConnectDriver.
type runtimeConnectDriverAsLegacy struct {
	driver appConnectDriver
}

func (adapter *runtimeConnectDriverAsLegacy) detect(string) (bool, error) {
	return adapter.driver.Detect()
}

func (adapter *runtimeConnectDriverAsLegacy) install(
	ctx context.Context,
	_ string,
	onLine func(string),
) error {
	return adapter.driver.Install(ctx, onLine)
}

func (adapter *runtimeConnectDriverAsLegacy) login(
	ctx context.Context,
	_ string,
	configDir string,
) (string, string, func() error, error) {
	return adapter.driver.Login(ctx, configDir)
}

func (adapter *runtimeConnectDriverAsLegacy) pollAuth(_ string, configDir string) authState {
	return adapter.driver.PollAuth(configDir)
}

func (adapter *runtimeConnectDriverAsLegacy) accountLabel(_ string, configDir string) string {
	return adapter.driver.AccountLabel(configDir)
}

func TestRuntimeConnectProductionCodexClaudeCompatibility(t *testing.T) {
	registry := productionAppProviderRuntimeRegistry()
	for _, kind := range []string{"codex", "claude-code"} {
		t.Run(kind, func(t *testing.T) {
			factory, ok := registry.connectDriverFactory(kind)
			if !ok {
				t.Fatal("production Connect factory is missing")
			}
			driver := factory(slog.New(slog.DiscardHandler))
			bound, ok := driver.(appBoundConnectDriver)
			if !ok || bound.kind != kind {
				t.Fatalf("production driver = %#v; want kind-bound compatibility wrapper", driver)
			}
			if _, ok := bound.runner.(*defaultConnectRunner); !ok {
				t.Fatalf("production runner = %T; want *defaultConnectRunner", bound.runner)
			}
			if _, ok := registry.accountProviderStrategy(kind); !ok {
				t.Fatal("production account strategy is missing")
			}
			if _, ok := registry.connectedModelSeeder(kind); !ok {
				t.Fatal("production model seeder is missing")
			}
		})
	}
}

func TestRuntimeConnectNilLoginWaitFailsClosed(t *testing.T) {
	const helperEnv = "GO_WANT_RUNTIME_CONNECT_NIL_WAIT_HELPER"
	if os.Getenv(helperEnv) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "-test.run=^TestRuntimeConnectNilLoginWaitFailsClosed$")
		cmd.Env = append(os.Environ(), helperEnv+"=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			if !strings.Contains(string(output), "panic: runtime error") {
				t.Fatalf("isolated nil-wait scenario failed for an unexpected reason: %v\n%s", err, output)
			}
			t.Fatalf("nil Login wait callback crashed the Connect worker: %v\n%s", err, output)
		}
		return
	}

	var accountStrategyCalls atomic.Int32
	var seedCalls atomic.Int32
	registry := runtimeConnectRegistry(t, func(registration *appProviderRuntimeRegistration) {
		registration.Connect = func(*slog.Logger) appConnectDriver {
			return runtimeConnectNilWaitDriver{}
		}
		registration.EnsureAccountProvider = func(st *store.Store) error {
			accountStrategyCalls.Add(1)
			return st.EnsureAccountRuntimeProvider(runtimeConnectFutureKind, "Future CLI")
		}
		registration.SeedConnectedModels = func(*store.Store, string) error {
			seedCalls.Add(1)
			return nil
		}
	})
	harness := newRuntimeConnectHarness(t, registry, "nil-wait-account")
	start := runtimeConnectPrepareOnboarding(t, harness.ctx, runtimeConnectFutureKind)
	kindRoot := filepath.Join(harness.root, "accounts", runtimeConnectFutureKind)
	sibling := filepath.Join(kindRoot, "sibling", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(sibling), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sibling, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"onboarding_revision":` + strconv.FormatInt(start.State.Revision, 10) + `}`
	runtimeConnectRequest(t, harness, http.MethodPost, runtimeConnectFutureKind, body, http.StatusOK)
	terminal := waitPhase(t, harness.manager, runtimeConnectFutureKind, phaseError)
	waitConnectJobDone(t, harness.manager)
	if terminal.Phase != phaseError || terminal.Error != "không mở được đăng nhập" ||
		terminal.ProviderID != "" || terminal.AccountID != "" {
		t.Fatalf("nil-wait terminal state = %+v", terminal)
	}
	encoded, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{harness.root, "nil Login wait callback"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("nil-wait response leaked %q: %s", private, encoded)
		}
	}
	after, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.State != start.State || !reflect.DeepEqual(after.Stages, start.Stages) {
		t.Fatalf("nil-wait failure advanced onboarding:\nbefore=%+v\nafter=%+v", start, after)
	}
	if accountStrategyCalls.Load() != 0 || seedCalls.Load() != 0 {
		t.Fatalf("nil-wait failure reached persistence callbacks: account=%d seed=%d",
			accountStrategyCalls.Load(), seedCalls.Load())
	}
	if _, exists := runtimeConnectProvider(t, harness, runtimeConnectFutureKind); exists {
		t.Fatal("nil-wait failure created a Provider")
	}
	accounts, err := harness.a.st.LLMAccounts(runtimeConnectFutureKind)
	if err != nil || len(accounts) != 0 {
		t.Fatalf("nil-wait Accounts = %+v, err=%v", accounts, err)
	}
	models, err := harness.a.st.LLMModels(runtimeConnectFutureKind)
	if err != nil || len(models) != 0 {
		t.Fatalf("nil-wait models = %+v, err=%v", models, err)
	}
	if _, err := os.Stat(filepath.Join(kindRoot, "nil-wait-account")); !os.IsNotExist(err) {
		t.Fatalf("nil-wait failure retained owned account leaf: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Fatalf("nil-wait failure removed sibling path: %v", err)
	}
}
