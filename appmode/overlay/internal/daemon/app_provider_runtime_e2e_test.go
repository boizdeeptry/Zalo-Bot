package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentdc/internal/providercatalog"
	"agentdc/internal/store"
)

const (
	providerRuntimeE2EFutureKind  = "future-cli"
	providerRuntimeE2EFutureModel = "future-model"
	providerRuntimeE2ECodexKind   = "codex"
	providerRuntimeE2ECodexModel  = "codex-e2e-model"
	providerRuntimeE2ELiveAnswer  = "PRIVATE_FUTURE_LIVE_ANSWER_E2E"
)

type providerRuntimeE2EObservations struct {
	mu        sync.Mutex
	factories []store.OnboardingTestRouteEntry
	runs      []store.OnboardingTestRouteEntry
	prompts   []string
}

func (o *providerRuntimeE2EObservations) recordFactory(entry store.OnboardingTestRouteEntry) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.factories = append(o.factories, entry)
}

func (o *providerRuntimeE2EObservations) recordRun(
	entry store.OnboardingTestRouteEntry,
	prompt string,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.runs = append(o.runs, entry)
	o.prompts = append(o.prompts, prompt)
}

func (o *providerRuntimeE2EObservations) snapshot() (
	[]store.OnboardingTestRouteEntry,
	[]store.OnboardingTestRouteEntry,
	[]string,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.factories), slices.Clone(o.runs), slices.Clone(o.prompts)
}

type providerRuntimeE2EFixture struct {
	harness        *runtimeOnboardingHarness
	observations   *providerRuntimeE2EObservations
	drivers        map[string]*runtimeConnectDriverProbe
	connectLog     *runtimeConnectEventLog
	accountIDs     map[string]string
	models         map[string]string
	selectorCalls  map[string]*atomic.Int32
	codexLive      *fakeAdapter
	futureLive     *fakeAdapter
	personaPath    string
	responses      []string
	bootstrapPosts atomic.Int32
}

func newProviderRuntimeE2EFixture(
	t *testing.T,
	futureSucceeds bool,
	privateProviderError string,
) *providerRuntimeE2EFixture {
	t.Helper()
	observations := &providerRuntimeE2EObservations{}
	connectLog := &runtimeConnectEventLog{}
	drivers := map[string]*runtimeConnectDriverProbe{
		providerRuntimeE2ECodexKind: {
			events: connectLog, installed: true, auth: authLoggedIn,
		},
		providerRuntimeE2EFutureKind: {
			events: connectLog, installed: true, auth: authLoggedIn,
		},
	}
	accountIDs := map[string]string{
		providerRuntimeE2ECodexKind:  "codex-e2e-account",
		providerRuntimeE2EFutureKind: "future-e2e-account",
	}
	models := map[string]string{
		providerRuntimeE2ECodexKind:  providerRuntimeE2ECodexModel,
		providerRuntimeE2EFutureKind: providerRuntimeE2EFutureModel,
	}
	selectorCalls := map[string]*atomic.Int32{
		providerRuntimeE2ECodexKind:  {},
		providerRuntimeE2EFutureKind: {},
	}
	codexLive := failAdapter(llmErrorUpstream)
	futureLive := okAdapter(providerRuntimeE2ELiveAnswer)

	harness := newRuntimeOnboardingHarness(t, func(registrations []appProviderRuntimeRegistration) {
		for _, selectedKind := range []string{providerRuntimeE2ECodexKind, providerRuntimeE2EFutureKind} {
			kind := selectedKind
			registration := runtimeOnboardingRegistration(t, registrations, kind)
			driver := drivers[kind]
			modelID := models[kind]
			accountID := accountIDs[kind]
			displayName := registration.Metadata.DisplayName
			registration.Connect = func(*slog.Logger) appConnectDriver {
				connectLog.add("factory:" + kind)
				return driver
			}
			registration.EnsureAccountProvider = func(st *store.Store) error {
				return st.EnsureAccountRuntimeProvider(kind, displayName)
			}
			registration.SeedConnectedModels = func(st *store.Store, providerID string) error {
				if providerID != kind {
					return fmt.Errorf("seed identity changed from %q to %q", kind, providerID)
				}
				return st.ReplaceLLMModels(providerID, store.LLMModelDiscovered, []store.LLMModel{{
					ProviderID: providerID, ModelID: modelID, Name: modelID, Available: true,
				}})
			}
			registration.SelectOnboardingModel = func(st *store.Store, providerID string) (string, error) {
				selectorCalls[kind].Add(1)
				if providerID != kind {
					return "", store.ErrOnboardingInvalidStagingOwnership
				}
				available, err := st.LLMModels(providerID)
				if err != nil {
					return "", err
				}
				for _, model := range available {
					if model.ProviderID == providerID && model.ModelID == modelID && model.Available {
						return modelID, nil
					}
				}
				return "", store.ErrOnboardingModelUnavailable
			}
			registration.NewOnboardingMember = func(
				_ *api,
				entry store.OnboardingTestRouteEntry,
			) (appOnboardingMemberRun, error) {
				if entry.Kind != kind || entry.ProviderID != kind || entry.AccountID != accountID ||
					entry.ModelID != modelID {
					return nil, store.ErrOnboardingInvalidStagingOwnership
				}
				observations.recordFactory(entry)
				return func(_ context.Context, prompt string, _ func(string)) (string, error) {
					wins := kind == providerRuntimeE2EFutureKind && futureSucceeds
					observations.recordRun(entry, prompt)
					if !wins {
						return "", errors.New(privateProviderError)
					}
					return "Xin chào, tôi là " + packagedPersonaTestName + ".", nil
				}, nil
			}
			registration.NewAdapter = func(
				providerID string,
				_ *store.Store,
				_ *http.Client,
				_ *slog.Logger,
			) (providerAdapter, error) {
				if providerID != kind {
					return nil, fmt.Errorf("adapter identity changed from %q to %q", kind, providerID)
				}
				if kind == providerRuntimeE2ECodexKind {
					return codexLive, nil
				}
				return futureLive, nil
			}
		}
	})

	var nextID atomic.Int32
	harness.ctx.connect.newID = func() string {
		if nextID.Add(1) == 1 {
			return accountIDs[providerRuntimeE2ECodexKind]
		}
		return accountIDs[providerRuntimeE2EFutureKind]
	}
	harness.ctx.connect.loginTimeout = time.Second
	harness.ctx.connect.pollInterval = time.Millisecond
	personaPath := filepath.Join(harness.env.dataDir, "persona.md")
	harness.env.a.zalo = &zaloDeps{cfg: zaloConfig{PersonaPath: personaPath, Model: "unused"}}
	// Re-register after all pointer-backed fixture seams are installed. These
	// handlers close over the local registry and manager, never the legacy mux.
	fixture := &providerRuntimeE2EFixture{
		harness: harness, observations: observations, drivers: drivers, connectLog: connectLog,
		accountIDs: accountIDs, models: models, selectorCalls: selectorCalls,
		codexLive: codexLive, futureLive: futureLive, personaPath: personaPath,
	}
	boundMux := runtimeLiveHandler(t, harness.ctx)
	harness.env.mux = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/onboarding/bootstrap" {
			fixture.bootstrapPosts.Add(1)
		}
		boundMux.ServeHTTP(w, r)
	})
	return fixture
}

func (fixture *providerRuntimeE2EFixture) serve(
	method string,
	path string,
	body string,
) *httptest.ResponseRecorder {
	response := fixture.harness.env.serve(method, path, body)
	fixture.responses = append(fixture.responses, response.Body.String())
	return response
}

func (fixture *providerRuntimeE2EFixture) postBootstrap(
	t *testing.T,
	revision int64,
) *httptest.ResponseRecorder {
	t.Helper()
	response := fixture.serve(
		http.MethodPost,
		"/onboarding/bootstrap",
		fmt.Sprintf(`{"revision":%d}`, revision),
	)
	if posts := fixture.bootstrapPosts.Load(); posts != 1 {
		t.Fatalf("bootstrap POST count=%d; want exactly 1", posts)
	}
	return response
}

func (fixture *providerRuntimeE2EFixture) responseText() string {
	return strings.Join(fixture.responses, "\n")
}

func (fixture *providerRuntimeE2EFixture) driveToPersona(t *testing.T) int64 {
	t.Helper()
	status := fixture.serve(http.MethodGet, "/onboarding/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("initial status=%d body=%s", status.Code, status.Body.String())
	}
	initial := decodeOnboardingResponse[appOnboardingStatusWire](t, status)
	kinds := make([]string, len(initial.Options))
	ranks := make([]int, len(initial.Options))
	for index, option := range initial.Options {
		kinds[index], ranks[index] = option.Kind, option.RouteRank
	}
	if !slices.Equal(kinds, []string{"codex", "future-cli", "claude-code"}) ||
		!slices.Equal(ranks, []int{10, 50, 100}) {
		t.Fatalf("runtime status kinds/ranks=%q/%v", kinds, ranks)
	}

	selected := fixture.serve(
		http.MethodPut,
		"/onboarding/providers",
		fmt.Sprintf(`{"revision":%d,"selected_kinds":["future-cli","codex"]}`, initial.Revision),
	)
	if selected.Code != http.StatusOK {
		t.Fatalf("plural selection=%d body=%s", selected.Code, selected.Body.String())
	}
	selection := decodeOnboardingResponse[appOnboardingStatusWire](t, selected)
	if len(selection.Providers) != 2 || selection.Providers[0].Kind != providerRuntimeE2ECodexKind ||
		selection.Providers[1].Kind != providerRuntimeE2EFutureKind {
		t.Fatalf("canonical plural selection=%+v", selection)
	}

	current := selection
	for _, kind := range []string{providerRuntimeE2ECodexKind, providerRuntimeE2EFutureKind} {
		begun := fixture.serve(
			http.MethodPut,
			"/onboarding/provider",
			fmt.Sprintf(`{"revision":%d,"kind":%q}`, current.Revision, kind),
		)
		if begun.Code != http.StatusOK {
			t.Fatalf("begin %s=%d body=%s", kind, begun.Code, begun.Body.String())
		}
		connectState := decodeOnboardingResponse[appOnboardingStatusWire](t, begun)
		if connectState.Phase != store.OnboardingPhaseConnect || connectState.ProviderKind != kind {
			t.Fatalf("begin %s response=%+v", kind, connectState)
		}
		started := fixture.serve(
			http.MethodPost,
			"/llm/providers/"+kind+"/connect",
			fmt.Sprintf(`{"label":"E2E","onboarding_revision":%d}`, connectState.Revision),
		)
		if started.Code != http.StatusOK {
			t.Fatalf("Connect %s=%d body=%s", kind, started.Code, started.Body.String())
		}
		if strings.Contains(started.Body.String(), fixture.harness.env.dataDir) {
			t.Fatalf("Connect start leaked config root: %s", started.Body.String())
		}
		connected := providerRuntimeE2EWaitConnected(t, fixture, kind)
		waitConnectJobDone(t, fixture.harness.ctx.connect)
		if connected.ProviderID != kind || connected.AccountID != fixture.accountIDs[kind] {
			t.Fatalf("Connect %s terminal=%+v", kind, connected)
		}
		providerRuntimeE2EAssertStagedConnect(t, fixture, kind)

		afterConnect := fixture.serve(http.MethodGet, "/onboarding/status", "")
		if afterConnect.Code != http.StatusOK {
			t.Fatalf("status after Connect %s=%d body=%s", kind, afterConnect.Code, afterConnect.Body.String())
		}
		setupState := decodeOnboardingResponse[appOnboardingStatusWire](t, afterConnect)
		if setupState.Phase != store.OnboardingPhaseSetup || setupState.ProviderKind != kind ||
			setupState.AccountID != fixture.accountIDs[kind] {
			t.Fatalf("bound Setup state for %s=%+v", kind, setupState)
		}
		setup := fixture.serve(
			http.MethodPost,
			"/onboarding/setup",
			fmt.Sprintf(`{"revision":%d,"kind":%q,"account_id":%q}`,
				setupState.Revision, kind, fixture.accountIDs[kind]),
		)
		if setup.Code != http.StatusOK {
			t.Fatalf("Setup %s=%d body=%s", kind, setup.Code, setup.Body.String())
		}
		current = decodeOnboardingResponse[appOnboardingStatusWire](t, setup)
		if current.Revision != setupState.Revision+1 {
			t.Fatalf("Setup %s revision=%d; want authoritative successor %d",
				kind, current.Revision, setupState.Revision+1)
		}
		if calls := fixture.selectorCalls[kind].Load(); calls != 1 {
			t.Fatalf("Setup %s selector calls=%d; want exactly 1", kind, calls)
		}
	}
	if current.Phase != store.OnboardingPhasePersona || len(current.Providers) != 2 {
		t.Fatalf("final Setup did not enter Persona: %+v", current)
	}
	for index, kind := range []string{providerRuntimeE2ECodexKind, providerRuntimeE2EFutureKind} {
		stage := current.Providers[index]
		if stage.Kind != kind || stage.Status != "ready" || stage.Position != index ||
			stage.AccountID != fixture.accountIDs[kind] || stage.ModelID != fixture.models[kind] {
			t.Fatalf("ready stage %d=%+v", index, stage)
		}
	}
	var connectFactories []string
	for _, event := range fixture.connectLog.snapshot() {
		if strings.HasPrefix(event, "factory:") {
			connectFactories = append(connectFactories, strings.TrimPrefix(event, "factory:"))
		}
	}
	if !slices.Equal(connectFactories, []string{
		providerRuntimeE2ECodexKind, providerRuntimeE2EFutureKind,
	}) {
		t.Fatalf("context-bound Connect factory order=%q", connectFactories)
	}
	if _, err := os.Lstat(fixture.personaPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Persona was not missing before automatic bootstrap: %v", err)
	}
	return current.Revision
}

func providerRuntimeE2EWaitConnected(
	t *testing.T,
	fixture *providerRuntimeE2EFixture,
	kind string,
) connectState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response := fixture.serve(http.MethodGet, "/llm/providers/"+kind+"/connect", "")
		if response.Code != http.StatusOK {
			t.Fatalf("Connect status %s=%d body=%s", kind, response.Code, response.Body.String())
		}
		state := decodeOnboardingResponse[connectState](t, response)
		if state.Phase == phaseConnected {
			return state
		}
		if state.Phase == phaseError || state.Phase == phaseCanceled {
			t.Fatalf("Connect %s stopped at %+v", kind, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Connect %s did not complete", kind)
	return connectState{}
}

func providerRuntimeE2EAssertStagedConnect(
	t *testing.T,
	fixture *providerRuntimeE2EFixture,
	kind string,
) {
	t.Helper()
	wantDir := accountConfigDir(fixture.harness.env.dataDir, kind, fixture.accountIDs[kind])
	if got := fixture.drivers[kind].seenConfigDir(); got != wantDir {
		t.Fatalf("%s Connect config=%q want owned %q", kind, got, wantDir)
	}
	if info, err := os.Stat(wantDir); err != nil || !info.IsDir() {
		t.Fatalf("%s owned config missing: %v", kind, err)
	}
	children, err := os.ReadDir(filepath.Dir(wantDir))
	if err != nil || len(children) != 1 || children[0].Name() != fixture.accountIDs[kind] {
		t.Fatalf("%s config root children=%v err=%v", kind, children, err)
	}
	accounts, err := fixture.harness.env.a.st.LLMAccounts(kind)
	if err != nil || len(accounts) != 1 || accounts[0].ID != fixture.accountIDs[kind] ||
		accounts[0].ConfigDir != wantDir || accounts[0].Enabled {
		t.Fatalf("%s staged Accounts=%+v err=%v", kind, accounts, err)
	}
	models, err := fixture.harness.env.a.st.LLMModels(kind)
	if err != nil || len(models) != 1 || models[0].ModelID != fixture.models[kind] || !models[0].Available {
		t.Fatalf("%s seeded models=%+v err=%v", kind, models, err)
	}
	if calls := fixture.selectorCalls[kind].Load(); calls != 0 {
		t.Fatalf("%s selector ran before Setup: %d", kind, calls)
	}
	providers, err := fixture.harness.env.a.st.LLMProviders()
	if err != nil {
		t.Fatalf("%s Providers: %v", kind, err)
	}
	registration, ok := fixture.harness.registry.registration(kind)
	if !ok {
		t.Fatalf("%s registration disappeared before Setup", kind)
	}
	found := false
	for _, provider := range providers {
		if provider.ID != kind {
			continue
		}
		found = provider.Kind == kind && provider.Name == registration.Metadata.DisplayName
		break
	}
	if !found {
		t.Fatalf("%s exact account-runtime Provider was not ensured: %+v", kind, providers)
	}
}

func TestProviderRuntimeFutureEndToEnd(t *testing.T) {
	providerRuntimeE2EAssertPrivacyScannerPrecondition(t)
	productionBefore := productionAppProviderRuntimeRegistry()
	productionCatalogBefore := productionBefore.Catalog().Options()
	productionManagementBefore := productionBefore.managementOptions()
	defaultBefore := providercatalog.Default().Options()

	t.Run("single Persona bootstrap reaches live Future", func(t *testing.T) {
		randomBytes := bytes.Repeat([]byte{0x6a}, sha256.Size)
		rawToken := base64.RawURLEncoding.EncodeToString(randomBytes)
		privateProviderError := "PRIVATE_PROVIDER_FAILURE_" + rawToken
		fixture := newProviderRuntimeE2EFixture(t, true, privateProviderError)
		var logs syncLogBuffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		fixture.harness.env.a.logger = logger
		fixture.harness.ctx.connect.logger = logger
		personaRevision := fixture.driveToPersona(t)
		if fixture.harness.ctx.hasAnyConnectedProvider() {
			t.Fatal("disabled staged Accounts contributed readiness before Complete")
		}
		fixedNow := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
		receiptPolicy := store.NewOnboardingTestReceiptPolicy(fixedNow)
		if installed := installOnboardingBootstrapSeams(
			t, fixedNow, randomBytes, time.Second,
		); installed != rawToken {
			t.Fatalf("deterministic bootstrap token=%q want=%q", installed, rawToken)
		}
		type executionObservation struct {
			route  store.OnboardingTestRoute
			result appOnboardingTestResult
			err    error
		}
		var executionMu sync.Mutex
		var executions []executionObservation
		oldExecute := appOnboardingTestExecute
		appOnboardingTestExecute = func(
			ctx context.Context,
			runtimeContext appRuntimeContext,
			route store.OnboardingTestRoute,
			prompt string,
		) (appOnboardingTestResult, error) {
			result, err := defaultAppOnboardingTestExecute(ctx, runtimeContext, route, prompt)
			route.Entries = slices.Clone(route.Entries)
			executionMu.Lock()
			executions = append(executions, executionObservation{route: route, result: result, err: err})
			executionMu.Unlock()
			return result, err
		}
		t.Cleanup(func() { appOnboardingTestExecute = oldExecute })
		type receiptObservation struct {
			state        store.OnboardingState
			expectedHash string
			files        []byte
			err          error
		}
		receipt := make(chan receiptObservation, 1)
		oldAfterCommit := appOnboardingTestAfterCommit
		appOnboardingTestAfterCommit = func() {
			state, err := fixture.harness.ctx.onboardingStore().OnboardingState()
			expectedHash := ""
			if err == nil {
				route, routeErr := fixture.harness.ctx.onboardingStore().OnboardingTestRoute(
					context.Background(), state.Revision,
				)
				if routeErr != nil {
					err = routeErr
				} else {
					digest := sha256.Sum256([]byte(rawToken))
					nonceHash := hex.EncodeToString(digest[:])
					expectedHash, err = store.OnboardingTestReceiptHash(nonceHash, route.Fingerprint)
				}
			}
			files, filesErr := providerRuntimeE2EOwnedRegularBytes(fixture.harness.env.dataDir)
			if err == nil {
				err = filesErr
			}
			receipt <- receiptObservation{
				state: state, expectedHash: expectedHash, files: files, err: err,
			}
		}
		t.Cleanup(func() { appOnboardingTestAfterCommit = oldAfterCommit })

		response := fixture.postBootstrap(t, personaRevision)
		if response.Code != http.StatusOK {
			t.Fatalf("automatic bootstrap=%d body=%s", response.Code, response.Body.String())
		}
		executionMu.Lock()
		capturedExecutions := slices.Clone(executions)
		executionMu.Unlock()
		if len(capturedExecutions) != 1 {
			t.Fatalf("production executor result count=%d; want exactly 1", len(capturedExecutions))
		}
		execution := capturedExecutions[0]
		if execution.err != nil {
			t.Fatalf("production executor result error=%v", execution.err)
		}
		wantAnswer := "Xin chào, tôi là " + packagedPersonaTestName + "."
		if execution.result.Answer != wantAnswer ||
			execution.result.ProviderID != providerRuntimeE2EFutureKind ||
			execution.result.ModelID != providerRuntimeE2EFutureModel || execution.result.Position != 1 ||
			execution.route.DisplayName != packagedPersonaTestName {
			t.Fatalf("production executor result=%+v displayName=%q; want Future position 1 and packaged Persona",
				execution.result, execution.route.DisplayName)
		}
		if execution.result.Position >= len(execution.route.Entries) {
			t.Fatalf("production executor position=%d route entries=%d",
				execution.result.Position, len(execution.route.Entries))
		}
		selectedEntry := execution.route.Entries[execution.result.Position]
		if selectedEntry.Kind != providerRuntimeE2EFutureKind ||
			selectedEntry.ProviderID != providerRuntimeE2EFutureKind ||
			selectedEntry.AccountID != fixture.accountIDs[providerRuntimeE2EFutureKind] ||
			selectedEntry.ModelID != providerRuntimeE2EFutureModel || selectedEntry.Position != 1 {
			t.Fatalf("production executor selected route entry=%+v", selectedEntry)
		}
		completed := decodeOnboardingResponse[appOnboardingStatusResponse](t, response)
		if completed.Phase != store.OnboardingPhaseCompleted || completed.Revision != personaRevision+3 ||
			completed.Required || len(completed.Providers) != 0 {
			t.Fatalf("automatic completion=%+v; want Completed r+3", completed)
		}
		var observed receiptObservation
		select {
		case observed = <-receipt:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for the committed Test receipt observation")
		}
		if observed.err != nil || observed.state.Phase != store.OnboardingPhaseTest ||
			observed.state.Revision != personaRevision+2 || observed.expectedHash == "" ||
			observed.state.TestNonceHash != observed.expectedHash ||
			observed.state.TestExpiresAt != receiptPolicy.ExpiresAt.Format(time.RFC3339Nano) ||
			strings.Contains(observed.state.TestNonceHash, rawToken) {
			t.Fatalf("after-commit receipt=%+v expectedHash=%q expectedExpiry=%q err=%v",
				observed.state, observed.expectedHash,
				receiptPolicy.ExpiresAt.Format(time.RFC3339Nano), observed.err)
		}

		factories, runs, prompts := fixture.observations.snapshot()
		if got := providerRuntimeE2EEntryKinds(factories); !slices.Equal(got, []string{"codex", "future-cli"}) {
			t.Fatalf("member factory order=%q", got)
		}
		if got := providerRuntimeE2EEntryKinds(runs); !slices.Equal(got, []string{"codex", "future-cli"}) {
			t.Fatalf("fallback run order=%q", got)
		}
		for _, prompt := range prompts {
			if !strings.Contains(prompt, packagedPersonaTestText) ||
				!strings.Contains(prompt, `"display_name":"`+packagedPersonaTestName+`"`) {
				t.Fatalf("bootstrap did not run packaged Persona: %q", prompt)
			}
		}

		route, err := fixture.harness.env.a.st.LLMRoute()
		if err != nil || len(route.Entries) != 2 {
			t.Fatalf("completed route=%+v err=%v", route, err)
		}
		for index, kind := range []string{providerRuntimeE2ECodexKind, providerRuntimeE2EFutureKind} {
			entry := route.Entries[index]
			if entry.ProviderID != kind || entry.ModelID != fixture.models[kind] || !entry.Enabled {
				t.Fatalf("completed route entry %d=%+v", index, entry)
			}
			accounts, accountErr := fixture.harness.env.a.st.LLMAccounts(kind)
			if accountErr != nil || len(accounts) != 1 ||
				accounts[0].ID != fixture.accountIDs[kind] || !accounts[0].Enabled {
				t.Fatalf("completed %s Accounts=%+v err=%v", kind, accounts, accountErr)
			}
		}
		post, err := fixture.harness.ctx.onboardingStore().OnboardingSnapshot()
		providerRuntimeE2EAssertCompletedSnapshot(t, post, err, personaRevision+3)
		continuity := fixture.serve(http.MethodGet, "/onboarding/status", "")
		providerRuntimeE2EAssertCompletedStatus(t, continuity, personaRevision+3)
		if !fixture.harness.ctx.hasAnyConnectedProvider() {
			t.Fatal("Completed Accounts did not contribute readiness")
		}
		answer, err := fixture.harness.ctx.appZaloRunner(
			zaloConfig{}, silentZaloRunner{}, "future-e2e-thread", false,
		).Run(t.Context(), "live prompt", func(string) {})
		codexCalls := fixture.codexLive.seen()
		futureCalls := fixture.futureLive.seen()
		if err != nil || answer != providerRuntimeE2ELiveAnswer || len(codexCalls) != 1 ||
			len(futureCalls) != 1 {
			t.Fatalf("live Zalo answer=%q err=%v calls=%d/%d", answer, err,
				len(codexCalls), len(futureCalls))
		}
		if codexCalls[0].Model != providerRuntimeE2ECodexModel ||
			futureCalls[0].Model != providerRuntimeE2EFutureModel ||
			codexCalls[0].Prompt != "live prompt" || futureCalls[0].Prompt != "live prompt" ||
			len(codexCalls[0].Cred) != 0 || len(futureCalls[0].Cred) != 0 {
			t.Fatalf("live fallback calls codex=%+v future=%+v", codexCalls[0], futureCalls[0])
		}

		afterFiles, err := providerRuntimeE2EOwnedRegularBytes(fixture.harness.env.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		providerRuntimeE2EAssertPrivateHygiene(
			t,
			rawToken,
			fixture.responseText(),
			logs.String(),
			append(observed.files, afterFiles...),
			[]string{privateProviderError, providerRuntimeE2ELiveAnswer},
			privateProviderError,
			providerRuntimeE2ELiveAnswer,
			fixture.harness.env.dataDir,
			fixture.personaPath,
			packagedPersonaTestText,
			packagedPersonaTestName,
		)
		if strings.Contains(response.Body.String(), "test_token") ||
			strings.Contains(response.Body.String(), "test_nonce_hash") ||
			strings.Contains(response.Body.String(), "test_expires_at") {
			t.Fatalf("bootstrap response exposed receipt internals: %s", response.Body.String())
		}
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("failed bootstrap preserves old live route and Accounts", func(t *testing.T) {
		const privateFailure = "PRIVATE_E2E_BOOTSTRAP_FAILURE"
		fixture := newProviderRuntimeE2EFixture(t, false, privateFailure)
		var logs syncLogBuffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		fixture.harness.env.a.logger = logger
		fixture.harness.ctx.connect.logger = logger
		seedOnboardingRoute(t, fixture.harness.env, "old-provider", "openai", true)
		oldConfig := filepath.Join(fixture.harness.env.dataDir, "accounts", "openai", "old-account")
		if err := os.MkdirAll(oldConfig, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := fixture.harness.env.a.st.CreateLLMAccount(store.LLMAccount{
			ID: "old-account", ProviderID: "old-provider", Label: "Old", ConfigDir: oldConfig, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		personaRevision := fixture.driveToPersona(t)
		beforeRoute := appOnboardingLiveRoutingBytes(t, fixture.harness.env)
		beforeAccounts := providerRuntimeE2EAccountRows(t, fixture.harness.env)
		rawToken := installOnboardingBootstrapSeams(
			t, time.Date(2026, 8, 16, 14, 15, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x6b}, sha256.Size), time.Second,
		)

		response := fixture.postBootstrap(t, personaRevision)
		requireOnboardingCode(t, response, http.StatusBadGateway, "ONBOARDING_TEST_FAILED")
		failedSnapshot, err := fixture.harness.ctx.onboardingStore().OnboardingSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		failed := failedSnapshot.State
		if failed.Phase != store.OnboardingPhaseTest || failed.Revision != personaRevision+1 ||
			failed.TestNonceHash != "" || failed.TestExpiresAt != "" {
			t.Fatalf("failed bootstrap state=%+v", failed)
		}
		if len(failedSnapshot.Stages) != 2 || failedSnapshot.Stages[0].Status != "ready" ||
			failedSnapshot.Stages[1].Status != "ready" {
			t.Fatalf("failed bootstrap staging=%+v", failedSnapshot.Stages)
		}
		if after := appOnboardingLiveRoutingBytes(t, fixture.harness.env); after != beforeRoute {
			t.Fatalf("failed bootstrap changed old route:\nbefore=%s\nafter=%s", beforeRoute, after)
		}
		if after := providerRuntimeE2EAccountRows(t, fixture.harness.env); after != beforeAccounts {
			t.Fatalf("failed bootstrap changed Accounts:\nbefore=%s\nafter=%s", beforeAccounts, after)
		}
		files, err := providerRuntimeE2EOwnedRegularBytes(fixture.harness.env.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		providerRuntimeE2EAssertPrivateHygiene(
			t,
			rawToken,
			fixture.responseText(),
			logs.String(),
			files,
			[]string{privateFailure},
			privateFailure,
			oldConfig,
			fixture.harness.env.dataDir,
			fixture.personaPath,
			packagedPersonaTestText,
			packagedPersonaTestName,
		)
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("Test resume completes at r plus 2", func(t *testing.T) {
		const revision int64 = 301
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{providerRuntimeE2EFutureKind},
			map[string]string{providerRuntimeE2EFutureKind: providerRuntimeE2EFutureModel},
			revision,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, providerRuntimeE2EFutureKind)
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		harness.ctx.personaDefaults = &appPersonaDefaultsSource{root: ""}
		before, err := harness.ctx.onboardingStore().OnboardingSnapshot()
		if err != nil || before.State.Phase != store.OnboardingPhaseTest ||
			before.State.Revision != revision {
			t.Fatalf("Test resume precondition=%+v err=%v", before, err)
		}
		var logs syncLogBuffer
		harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
		rawToken := installOnboardingBootstrapSeams(
			t, time.Date(2026, 8, 16, 14, 30, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x6c}, sha256.Size), time.Second,
		)
		var posts atomic.Int32
		bound := onboardingBootstrapHandler(t, harness, nil)
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/onboarding/bootstrap" {
				posts.Add(1)
			}
			bound.ServeHTTP(w, r)
		})
		response := serveOnboardingBootstrap(
			handler, context.Background(), revision,
		)
		if posts.Load() != 1 {
			t.Fatalf("Test resume bootstrap POST count=%d; want exactly 1", posts.Load())
		}
		if response.Code != http.StatusOK {
			t.Fatalf("Test resume=%d body=%s", response.Code, response.Body.String())
		}
		providerRuntimeE2EAssertCompletedStatus(t, response, revision+2)
		after, err := harness.ctx.onboardingStore().OnboardingSnapshot()
		providerRuntimeE2EAssertCompletedSnapshot(t, after, err, revision+2)
		continuity := runtimeLiveServeHandler(t, handler, http.MethodGet, "/onboarding/status", "")
		providerRuntimeE2EAssertCompletedStatus(t, continuity, revision+2)
		files, err := providerRuntimeE2EOwnedRegularBytes(harness.env.dataDir)
		if err != nil {
			t.Fatal(err)
		}
		providerRuntimeE2EAssertPrivateHygiene(
			t, rawToken, response.Body.String()+"\n"+continuity.Body.String(), logs.String(), files, nil,
			harness.env.dataDir, onboardingBootstrapPersonaForTest,
		)
	})

	t.Run("registries remain local and OpenCode remains denied", func(t *testing.T) {
		providerRuntimeE2EAssertRegistryIsolation(t)
	})

	t.Run("synthetic literals stay out of production core", func(t *testing.T) {
		providerRuntimeE2EAssertLiteralHygiene(t)
	})

	productionAfter := productionAppProviderRuntimeRegistry()
	if !reflect.DeepEqual(productionAfter.Catalog().Options(), productionCatalogBefore) ||
		!reflect.DeepEqual(productionAfter.managementOptions(), productionManagementBefore) ||
		!reflect.DeepEqual(providercatalog.Default().Options(), defaultBefore) {
		t.Fatal("local acceptance registries changed production registry or default Catalog")
	}
	if productionAfter.supportsOnboarding(providerRuntimeE2EFutureKind) ||
		productionAfter.supportsConnect(providerRuntimeE2EFutureKind) {
		t.Fatal("synthetic Future runtime leaked into production")
	}
}

func providerRuntimeE2EEntryKinds(entries []store.OnboardingTestRouteEntry) []string {
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Kind
	}
	return result
}

func providerRuntimeE2EAssertCompletedSnapshot(
	t *testing.T,
	snapshot store.OnboardingSnapshot,
	err error,
	wantRevision int64,
) {
	t.Helper()
	state := snapshot.State
	if err != nil || state.Phase != store.OnboardingPhaseCompleted ||
		state.CompletedVersion != store.CurrentOnboardingVersion ||
		state.Revision != wantRevision || state.RestartInProgress ||
		state.ProviderKind != "" || state.ProviderID != "" || state.AccountID != "" ||
		state.ModelID != "" || state.StagedComboID != "" || state.PersonaFingerprint != "" ||
		state.TestNonceHash != "" || state.TestExpiresAt != "" || len(snapshot.Stages) != 0 {
		t.Fatalf("persisted Completed snapshot=%+v err=%v; want revision %d and cleared staging/receipt",
			snapshot, err, wantRevision)
	}
}

func providerRuntimeE2EAssertCompletedStatus(
	t *testing.T,
	response *httptest.ResponseRecorder,
	wantRevision int64,
) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("Completed status=%d body=%s", response.Code, response.Body.String())
	}
	status := decodeOnboardingResponse[appOnboardingStatusResponse](t, response)
	if status.Required || status.CurrentVersion != store.CurrentOnboardingVersion ||
		status.CompletedVersion != store.CurrentOnboardingVersion ||
		status.Phase != store.OnboardingPhaseCompleted || status.Revision != wantRevision ||
		status.RestartInProgress || status.ProviderKind != "" || status.ProviderID != "" ||
		status.AccountID != "" || status.ModelID != "" || len(status.Providers) != 0 {
		t.Fatalf("Completed status continuity=%+v; want revision %d", status, wantRevision)
	}
}

func providerRuntimeE2EAccountRows(t *testing.T, env *onboardingRouteTestEnv) string {
	t.Helper()
	rows, err := env.db.Query(`
SELECT id, provider_id, label, email, config_dir, enabled, added_at
FROM llm_accounts
ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var result strings.Builder
	for rows.Next() {
		var id, providerID, label, email, configDir, addedAt string
		var enabled int
		if err := rows.Scan(&id, &providerID, &label, &email, &configDir, &enabled, &addedAt); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&result, "%q|%q|%q|%q|%q|%d|%q;",
			id, providerID, label, email, configDir, enabled, addedAt)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result.String()
}

func providerRuntimeE2EOwnedRegularBytes(root string) ([]byte, error) {
	var result []byte
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result = append(result, content...)
		return nil
	})
	return result, err
}

func providerRuntimeE2EAssertPrivateHygiene(
	t *testing.T,
	rawToken string,
	response string,
	logs string,
	persistence []byte,
	persistencePrivate []string,
	privateValues ...string,
) {
	t.Helper()
	responseLeak, err := providerRuntimeE2EResponseContainsPrivate(response, rawToken)
	if err != nil {
		t.Fatalf("decode public response stream for privacy scan: %v", err)
	}
	if responseLeak || providerRuntimeE2ELogContainsPrivate(logs, rawToken) ||
		bytes.Contains(persistence, []byte(rawToken)) {
		t.Fatalf("raw Test token leaked: response=%s logs=%s", response, logs)
	}
	for _, private := range persistencePrivate {
		if private != "" && bytes.Contains(persistence, []byte(private)) {
			t.Fatalf("private canary %q persisted under the owned root", private)
		}
	}
	for _, private := range privateValues {
		responseLeak, err := providerRuntimeE2EResponseContainsPrivate(response, private)
		if err != nil {
			t.Fatalf("decode public response stream for privacy scan: %v", err)
		}
		if private != "" && (responseLeak || providerRuntimeE2ELogContainsPrivate(logs, private)) {
			t.Fatalf("private value %q leaked: response=%s logs=%s", private, response, logs)
		}
	}
}

func providerRuntimeE2EResponseContainsPrivate(response, private string) (bool, error) {
	if private == "" {
		return false, nil
	}
	if strings.Contains(response, private) {
		return true, nil
	}
	decoder := json.NewDecoder(strings.NewReader(response))
	for {
		var value any
		err := decoder.Decode(&value)
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if providerRuntimeE2EJSONContainsPrivate(value, private) {
			return true, nil
		}
	}
}

func providerRuntimeE2EJSONContainsPrivate(value any, private string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, private)
	case []any:
		return slices.ContainsFunc(typed, func(item any) bool {
			return providerRuntimeE2EJSONContainsPrivate(item, private)
		})
	case map[string]any:
		for key, item := range typed {
			if strings.Contains(key, private) || providerRuntimeE2EJSONContainsPrivate(item, private) {
				return true
			}
		}
	}
	return false
}

func providerRuntimeE2ELogContainsPrivate(logs, private string) bool {
	if private == "" || strings.Contains(logs, private) {
		return private != ""
	}
	quoted := strconv.Quote(private)
	return len(quoted) >= 2 && strings.Contains(logs, quoted[1:len(quoted)-1])
}

func providerRuntimeE2EAssertPrivacyScannerPrecondition(t *testing.T) {
	t.Helper()
	const windowsPath = `C:\Users\private\AppData\secret`
	encoded, err := json.Marshal(map[string]any{
		"nested": []any{map[string]any{"path": windowsPath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), windowsPath) {
		t.Fatalf("privacy scanner precondition did not JSON-escape Windows path: %s", encoded)
	}
	contains, err := providerRuntimeE2EResponseContainsPrivate(string(encoded), windowsPath)
	if err != nil || !contains {
		t.Fatalf("privacy scanner missed JSON-escaped Windows path: contains=%v err=%v body=%s",
			contains, err, encoded)
	}
	escapedLog := "path=" + strconv.Quote(windowsPath)
	if strings.Contains(escapedLog, windowsPath) {
		t.Fatalf("privacy scanner precondition did not log-escape Windows path: %s", escapedLog)
	}
	if !providerRuntimeE2ELogContainsPrivate(escapedLog, windowsPath) {
		t.Fatalf("privacy scanner missed log-escaped Windows path: %s", escapedLog)
	}
}

func providerRuntimeE2EAssertRegistryIsolation(t *testing.T) {
	t.Helper()
	var leftCalls, rightCalls atomic.Int32
	build := func(name string, calls *atomic.Int32) appProviderRuntimeRegistry {
		registrations := appRuntimeSyntheticRegistrations()
		future := runtimeOnboardingRegistration(t, registrations, providerRuntimeE2EFutureKind)
		future.SelectOnboardingModel = func(*store.Store, string) (string, error) {
			calls.Add(1)
			return name, nil
		}
		registry, err := newAppProviderRuntimeRegistry(appRuntimeSyntheticOptions(), registrations)
		if err != nil {
			t.Fatal(err)
		}
		return registry
	}
	left := build("left-model", &leftCalls)
	right := build("right-model", &rightCalls)
	for _, clean := range []struct {
		name     string
		registry appProviderRuntimeRegistry
	}{
		{name: "left", registry: left},
		{name: "right", registry: right},
		{name: "production", registry: productionAppProviderRuntimeRegistry()},
	} {
		if clean.registry.catalogContainsProductionDenied() {
			t.Fatalf("%s clean registry contains a production-denied raw Catalog option", clean.name)
		}
		providerRuntimeE2EAssertOpenCodeDenied(t, clean.registry)
	}

	errorsFound := make(chan error, 3)
	var wait sync.WaitGroup
	for _, test := range []struct {
		registry appProviderRuntimeRegistry
		want     string
	}{
		{registry: left, want: "left-model"},
		{registry: right, want: "right-model"},
	} {
		test := test
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				selector, ok := test.registry.onboardingModelSelector(providerRuntimeE2EFutureKind)
				if !ok {
					errorsFound <- errors.New("local Future selector missing")
					return
				}
				got, err := selector(nil, providerRuntimeE2EFutureKind)
				if err != nil || got != test.want {
					errorsFound <- fmt.Errorf("selector=%q err=%v want=%q", got, err, test.want)
					return
				}
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		production := productionAppProviderRuntimeRegistry()
		for iteration := 0; iteration < 100; iteration++ {
			if production.supportsOnboarding(providerRuntimeE2EFutureKind) ||
				production.supportsConnect(providerRuntimeE2EFutureKind) {
				errorsFound <- errors.New("production observed local Future runtime")
				return
			}
		}
	}()
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if leftCalls.Load() != 100 || rightCalls.Load() != 100 {
		t.Fatalf("independent selector calls=%d/%d", leftCalls.Load(), rightCalls.Load())
	}
	if _, err := providercatalog.Default().CanonicalSelectedKinds(
		[]string{providerRuntimeE2EFutureKind},
	); !errors.Is(err, providercatalog.ErrUnsupportedKind) {
		t.Fatalf("default Catalog accepted synthetic Future: %v", err)
	}

	const (
		controlKind        = "control-cli"
		openCodeKind       = "opencode"
		intermediateRank   = 75
		intermediateUIRank = 17
	)
	control := appRuntimeFutureRegistration()
	control.Metadata.Kind = controlKind
	control.Metadata.DisplayName = "OpenCode"
	control.Metadata.Description = "Synthetic denied runtime"
	control.Metadata.Prefix = "oc"
	control.Metadata.UIOrder = intermediateUIRank
	control.Metadata.CatalogRank = intermediateRank
	controlOption := providercatalog.Option{
		Kind: controlKind, DisplayName: control.Metadata.DisplayName,
		Description: control.Metadata.Description, Advertised: true, RouteRank: intermediateRank,
	}
	controlOptions := append(slices.Clone(appRuntimeSyntheticOptions()), controlOption)
	controlRegistrations := append(slices.Clone(appRuntimeSyntheticRegistrations()), control)
	controlRegistry, err := newAppProviderRuntimeRegistry(controlOptions, controlRegistrations)
	if err != nil {
		t.Fatalf("rank-75 non-denied whole-registry control was rejected: %v", err)
	}
	controlCatalogOption, catalogOK := controlRegistry.Catalog().Option(controlKind)
	controlManagement, managementOK := controlRegistry.managementOption(controlKind)
	if !catalogOK || controlCatalogOption != controlOption || !managementOK ||
		control.Metadata.CatalogRank != intermediateRank ||
		controlManagement != control.Metadata.managementOption() {
		t.Fatalf("rank-75 control projections drifted: catalog=%+v/%v management=%+v/%v",
			controlCatalogOption, catalogOK, controlManagement, managementOK)
	}
	controlCatalog := controlRegistry.Catalog().Options()
	controlRanks := make([]int, len(controlCatalog))
	for index, option := range controlCatalog {
		controlRanks[index] = option.RouteRank
	}
	if !slices.Equal(controlRanks, []int{10, 50, intermediateRank, 100}) {
		t.Fatalf("rank-75 control did not remain between Future and terminal Claude: %v", controlRanks)
	}

	openCode := control
	openCode.Metadata.Kind = openCodeKind
	openCodeOption := controlOption
	openCodeOption.Kind = openCodeKind
	controlMetadataTwin := openCode.Metadata
	controlMetadataTwin.Kind = controlKind
	controlOptionTwin := openCodeOption
	controlOptionTwin.Kind = controlKind
	if controlMetadataTwin != control.Metadata || controlOptionTwin != controlOption {
		t.Fatal("rank-75 non-denied control is not otherwise identical to the OpenCode candidate")
	}
	forgedOptions := append(slices.Clone(appRuntimeSyntheticOptions()), openCodeOption)
	forgedRegistrations := append(slices.Clone(appRuntimeSyntheticRegistrations()), openCode)
	if _, err := newAppProviderRuntimeRegistry(forgedOptions, forgedRegistrations); err == nil ||
		!errors.Is(err, errAppProviderRuntimeRegistry) || !strings.Contains(err.Error(), "production-denied") {
		t.Fatalf("otherwise-valid rank-75 OpenCode constructor rejection=%v", err)
	}

	poisonedCatalog, err := providercatalog.New(forgedOptions)
	if err != nil {
		t.Fatalf("build rank-75 OpenCode poison Catalog: %v", err)
	}
	httpPoisoned := controlRegistry
	httpPoisoned.catalog = poisonedCatalog
	httpPoisoned.byKind = make(map[string]appProviderRuntimeRegistration, len(controlRegistry.byKind))
	for kind, registration := range controlRegistry.byKind {
		if kind != controlKind {
			httpPoisoned.byKind[kind] = registration
		}
	}
	httpPoisoned.byKind[openCodeKind] = openCode
	httpPoisoned.management = slices.Clone(controlRegistry.management)
	replacedManagement := false
	for index, option := range httpPoisoned.management {
		if option.Kind == controlKind {
			httpPoisoned.management[index] = openCode.Metadata.managementOption()
			replacedManagement = true
			break
		}
	}
	if !replacedManagement {
		t.Fatal("rank-75 control management row was unavailable for exact OpenCode poison")
	}
	rawHTTP, exists := httpPoisoned.byKind["opencode"]
	if !exists {
		t.Fatal("poisoned registry raw by-kind state does not contain OpenCode")
	}
	rawCatalogOption, catalogOK := httpPoisoned.catalog.Option(openCodeKind)
	rawManagement := openCode.Metadata.managementOption()
	if !httpPoisoned.valid || !httpPoisoned.catalogContainsProductionDenied() ||
		!catalogOK || rawCatalogOption != openCodeOption || rawHTTP.Metadata.CatalogRank != intermediateRank ||
		!slices.Contains(httpPoisoned.management, rawManagement) {
		t.Fatalf("rank-75 HTTP poison is not the exact denied twin: catalog=%+v/%v management=%+v",
			rawCatalogOption, catalogOK, httpPoisoned.management)
	}
	if err := validateAppProviderMetadata(rawHTTP.Metadata); err != nil {
		t.Fatalf("HTTP OpenCode poison has invalid metadata independent of production denial: %v", err)
	}
	if err := validateAppProviderRuntimeDependencies(rawHTTP); err != nil {
		t.Fatalf("HTTP OpenCode poison is not an otherwise-valid standard runtime: %v", err)
	}
	if rawHTTP.NewAdapter == nil || rawHTTP.Terminal != nil || rawHTTP.NewStructuredSession != nil {
		t.Fatal("HTTP OpenCode poison is not an otherwise-valid standard adapter registration")
	}
	providerRuntimeE2EAssertOpenCodeDenied(t, httpPoisoned)
	env := newOnboardingRouteTestEnv(t)
	response := runtimeLiveServe(
		t, runtimeLiveContext(t, env, httpPoisoned), http.MethodGet, "/llm/providers", "",
	)
	runtimeProviderHTTPAssertGenericInternalError(
		t, response, "opencode", runtimeProviderHTTPPrivateRegistry, env.dataDir,
	)

	fullyPoisoned := httpPoisoned
	fullyPoisoned.byKind = make(
		map[string]appProviderRuntimeRegistration, len(httpPoisoned.byKind),
	)
	for kind, registration := range httpPoisoned.byKind {
		fullyPoisoned.byKind[kind] = registration
	}
	rawOpenCode := fullyPoisoned.byKind["opencode"]
	rawOpenCode.Metadata.AttachmentPolicy = appProviderAttachmentLocal
	rawOpenCode.NewLocalAttachmentRun = appRuntimeAttachmentFactory
	rawOpenCode.Terminal = appRuntimeTerminalCallback
	rawOpenCode.NewStructuredSession = appRuntimeStructuredFactory
	fullyPoisoned.byKind["opencode"] = rawOpenCode
	if !fullyPoisoned.catalogContainsProductionDenied() {
		t.Fatal("poisoned registry raw Catalog does not contain OpenCode")
	}
	if rawOpenCode.Metadata.AttachmentPolicy != appProviderAttachmentLocal {
		t.Fatalf("poisoned registry raw OpenCode attachment policy=%q; want local",
			rawOpenCode.Metadata.AttachmentPolicy)
	}
	if rawOpenCode.NewLocalAttachmentRun == nil {
		t.Fatal("poisoned registry raw OpenCode local attachment callback is nil")
	}
	if rawOpenCode.Terminal == nil {
		t.Fatal("poisoned registry raw OpenCode terminal callback is nil")
	}
	if rawOpenCode.NewStructuredSession == nil {
		t.Fatal("poisoned registry raw OpenCode structured session callback is nil")
	}
	if rawOpenCode.NewAdapter == nil || !registrationSupportsOnboarding(rawOpenCode) {
		t.Fatal("fully poisoned raw OpenCode registration lacks its standard/onboarding callbacks")
	}
	if !slices.ContainsFunc(fullyPoisoned.management, func(option appProviderManagementOption) bool {
		return option.Kind == "opencode"
	}) {
		t.Fatal("poisoned registry raw management state does not contain OpenCode")
	}
	providerRuntimeE2EAssertOpenCodeDenied(t, fullyPoisoned)

	openCodeEntry := store.OnboardingTestRouteEntry{
		Position: 0, Kind: openCodeKind, ProviderID: openCodeKind,
		AccountID: "opencode-account", ModelID: "opencode-model",
		ConfigDir: filepath.Join(env.dataDir, "accounts", openCodeKind, "opencode-account"),
	}
	controlEntry := openCodeEntry
	controlEntry.Kind = controlKind
	if err := controlRegistry.validateOnboardingLiveRoute([]store.OnboardingTestRouteEntry{controlEntry}); err != nil {
		t.Fatalf("structurally valid non-denied single-entry Onboarding route was rejected: %v", err)
	}
	if err := fullyPoisoned.validateOnboardingLiveRoute(
		[]store.OnboardingTestRouteEntry{openCodeEntry},
	); err == nil || err.Error() != "onboarding live runtime is unavailable" {
		t.Fatalf("fully poisoned registry accepted structurally valid OpenCode live route: %v", err)
	}
}

func providerRuntimeE2EAssertOpenCodeDenied(t *testing.T, registry appProviderRuntimeRegistry) {
	t.Helper()
	catalog := registry.Catalog()
	if registry.catalogContainsProductionDenied() && len(catalog.Options()) != 0 {
		t.Fatal("poisoned Catalog projection did not fail closed to empty")
	}
	if _, exposed := catalog.Option("opencode"); exposed {
		t.Fatal("OpenCode appeared through the Catalog accessor")
	}
	for _, option := range catalog.Options() {
		if option.Kind == "opencode" {
			t.Fatal("OpenCode appeared in Catalog projection")
		}
	}
	for _, option := range registry.onboardingOptions() {
		if option.Kind == "opencode" {
			t.Fatal("OpenCode appeared in onboarding projection")
		}
	}
	for _, option := range registry.managementOptions() {
		if option.Kind == "opencode" {
			t.Fatal("OpenCode appeared in management projection")
		}
	}
	validatedManagement, err := registry.validatedManagementOptions()
	if registry.catalogContainsProductionDenied() {
		if !errors.Is(err, errAppProviderRuntimeRegistry) || len(validatedManagement) != 0 {
			t.Fatalf("poisoned validated management projection=%+v err=%v; want empty registry error",
				validatedManagement, err)
		}
	} else {
		if err != nil {
			t.Fatalf("validated management projection: %v", err)
		}
		for _, option := range validatedManagement {
			if option.Kind == "opencode" {
				t.Fatal("OpenCode appeared in validated management projection")
			}
		}
	}
	if slices.Contains(registry.readinessAccountKinds(), "opencode") ||
		registry.supportsOnboarding("opencode") || registry.supportsConnect("opencode") ||
		registry.contributesReadiness("opencode") || registry.contributesCredentialReadiness("opencode") {
		t.Fatal("OpenCode appeared in a capability/readiness projection")
	}
	if _, err := registry.canonicalOnboardingKinds([]string{"opencode"}); !errors.Is(err, providercatalog.ErrUnsupportedKind) {
		t.Fatalf("OpenCode canonicalization=%v", err)
	}
	checks := map[string]bool{}
	_, checks["registration"] = registry.registration("opencode")
	_, checks["management"] = registry.managementOption("opencode")
	_, checks["Connect"] = registry.connectDriverFactory("opencode")
	_, checks["account"] = registry.accountProviderStrategy("opencode")
	_, checks["seeder"] = registry.connectedModelSeeder("opencode")
	_, checks["selector"] = registry.onboardingModelSelector("opencode")
	_, checks["member"] = registry.onboardingMemberFactory("opencode")
	_, checks["adapter"] = registry.providerAdapterFactory("opencode")
	_, checks["attachment"] = registry.localAttachmentRunnerFactory("opencode")
	_, checks["terminal"] = registry.terminalRouteRunner("opencode")
	_, checks["structured"] = registry.structuredSessionRunnerFactory("opencode")
	for name, exposed := range checks {
		if exposed {
			t.Errorf("OpenCode exposed through %s accessor", name)
		}
	}
}

func providerRuntimeE2EAssertLiteralHygiene(t *testing.T) {
	t.Helper()
	forbidden := []string{providerRuntimeE2EFutureKind, providerRuntimeE2EFutureModel}
	if err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr != nil {
				t.Errorf("unquote production Go literal in %s: %v", path, unquoteErr)
				return true
			}
			for _, synthetic := range forbidden {
				if strings.Contains(value, synthetic) {
					t.Errorf("production Go file %s contains synthetic string literal %q",
						path, synthetic)
				}
			}
			return true
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join("..", "webui", "static")
	if err := filepath.WalkDir(webRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, literal := range forbidden {
			if bytes.Contains(content, []byte(literal)) {
				t.Errorf("production JS file %s contains synthetic literal %q", path, literal)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
