package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/store"
)

// TestScanDeviceAuth pins scanDeviceAuth against the REAL captured `codex login --device-auth`
// output (codex-cli 0.147.0, see the T5b task's ground truth). Proves: the uppercase code regex
// doesn't false-match the lowercase URL, and the `continue` after a URL match keeps that same line
// from also being tried as a code line.
func TestScanDeviceAuth(t *testing.T) {
	// codex ANSI-colorizes its output (verified live via the packaged daemon): the URL and code
	// print wrapped in SGR escapes like "\x1b[94m...\x1b[0m". The sample carries those so the test
	// fails if scanDeviceAuth ever stops stripping them (without stripping, url keeps a trailing
	// "\x1b[0m" and code's leading \b never matches — the real E2E bug this guards against).
	const sample = "\x1b[90mFollow these steps to sign in with ChatGPT using device code authorization:\x1b[0m\n" +
		"\n" +
		"1. Open this link in your browser and sign in to your account\n" +
		"   \x1b[94mhttps://auth.openai.com/codex/device\x1b[0m\n" +
		"\n" +
		"2. Enter this one-time code (expires in 15 minutes)\n" +
		"   \x1b[94mEQ0J-QKCPZ\x1b[0m\n" +
		"\n" +
		"\x1b[90mContinue only if you started this login in Codex.\x1b[0m\n"
	url, code := scanDeviceAuth(strings.NewReader(sample))
	if url != "https://auth.openai.com/codex/device" {
		t.Errorf("url = %q; want the captured device-auth URL", url)
	}
	if code != "EQ0J-QKCPZ" {
		t.Errorf("code = %q; want the captured one-time code", code)
	}
}

// TestScanClaudeLoginURL pins scanClaudeLoginURL against the REAL captured
// `claude auth login --claudeai` stdout: a "visit: https://…" line (browser OAuth, NO device
// code) followed by a "Paste code here if prompted >" prompt line with no trailing newline.
func TestScanClaudeLoginURL(t *testing.T) {
	sample := "Opening browser to sign in…\n" +
		"If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&client_id=abc&state=xyz\n" +
		"Paste code here if prompted > "
	url := scanClaudeLoginURL(strings.NewReader(sample))
	if url != "https://claude.com/cai/oauth/authorize?code=true&client_id=abc&state=xyz" {
		t.Errorf("scanClaudeLoginURL = %q", url)
	}
}

func TestConnectLoginCancelsHangingNPMDiscovery(t *testing.T) {
	fixture := newHangingNPMDiscovery(t)
	ctx, cancel := context.WithCancel(t.Context())
	configDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := (&defaultConnectRunner{}).login(ctx, "claude-code", configDir)
		done <- err
	}()
	fixture.waitStarted(t)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("login error = %v; want context.Canceled", err)
		}
	case <-time.After(time.Second):
		fixture.release()
		<-done
		t.Fatal("login did not cancel while npm discovery was hanging")
	}
}

func TestConnectLoginClaudePreservesDeadlineDuringHangingNPMDiscovery(t *testing.T) {
	fixture := newHangingNPMDiscovery(t)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	configDir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := (&defaultConnectRunner{}).loginClaude(ctx, configDir)
		done <- err
	}()
	fixture.waitStarted(t)

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("loginClaude error = %v; want context.DeadlineExceeded", err)
		}
	case <-time.After(1500 * time.Millisecond):
		fixture.release()
		<-done
		t.Fatal("loginClaude did not honor its deadline while npm discovery was hanging")
	}
}

// TestClaudeEmailFromJSON pins the parser that sets the account label: the real `claude auth status
// --json` shape yields the email, and malformed JSON yields "" (account falls back to job.label).
func TestClaudeEmailFromJSON(t *testing.T) {
	if got := claudeEmailFromJSON([]byte(`{"email":"a@b.com","loggedIn":true}`)); got != "a@b.com" {
		t.Errorf("claudeEmailFromJSON(valid) = %q; want a@b.com", got)
	}
	if got := claudeEmailFromJSON([]byte("garbage")); got != "" {
		t.Errorf("claudeEmailFromJSON(garbage) = %q; want empty", got)
	}
}

// TestConnectClaudeLabelsAccountWithEmail proves the claude-code branch: the account is labeled
// with the email accountLabel reads from `claude auth status --json` (overriding the user-given
// label), and the Portal snapshot still leaks no config dir.
func TestConnectClaudeLabelsAccountWithEmail(t *testing.T) {
	var mu sync.Mutex
	var createdAcct store.LLMAccount
	// claude login prints only a URL (no device code) → fake mirrors that with acctLabel set.
	r := &fakeRunner{installed: true, loginURL: "https://claude.com/cai/oauth/authorize?code=true", auth: authLoggedIn, acctLabel: "user@example.com"}
	m := newTestManager(t, r,
		func(string) error { return nil },
		func(a store.LLMAccount) error { mu.Lock(); createdAcct = a; mu.Unlock(); return nil },
	)
	if _, err := m.start("claude-code", "Tài khoản 1"); err != nil {
		t.Fatalf("start = %v; want nil", err)
	}
	st := waitPhase(t, m, "claude-code", phaseConnected)
	if st.Phase != phaseConnected {
		t.Fatalf("phase = %q; want connected (err=%q)", st.Phase, st.Error)
	}
	mu.Lock()
	defer mu.Unlock()
	if createdAcct.ProviderID != "claude-code" {
		t.Errorf("provider = %q; want claude-code", createdAcct.ProviderID)
	}
	if createdAcct.Label != "user@example.com" {
		t.Errorf("label = %q; want the email from accountLabel (not the user-given label)", createdAcct.Label)
	}
	// SECURITY: config dir must NOT be serialized to the Portal (mirror the codex canary)
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), filepath.Join(m.dataDir, "accounts")) ||
		strings.Contains(string(b), "configDir") || strings.Contains(string(b), "config_dir") {
		t.Errorf("connectState JSON leaks the internal config dir: %s", b)
	}
}

type fakeRunner struct {
	installed  bool
	loginURL   string
	auth       authState
	installErr error
	loginErr   error
	installCnt int
	lastCfgDir string // configDir seen by login/pollAuth — proves isolation is threaded through
	acctLabel  string // returned by accountLabel — "" (codex) keeps job.label; set = email path
}

func (f *fakeRunner) detect(string) (bool, error) { return f.installed, nil }
func (f *fakeRunner) install(ctx context.Context, _ string, onLine func(string)) error {
	f.installCnt++
	if onLine != nil {
		onLine("cài…")
	}
	return f.installErr
}
func (f *fakeRunner) login(ctx context.Context, _, configDir string) (string, string, func() error, error) {
	f.lastCfgDir = configDir
	if f.loginErr != nil {
		return "", "", nil, f.loginErr
	}
	return f.loginURL, "AAAA-BBBBB", func() error { <-ctx.Done(); return ctx.Err() }, nil
}
func (f *fakeRunner) pollAuth(_, configDir string) authState { f.lastCfgDir = configDir; return f.auth }
func (f *fakeRunner) accountLabel(_, _ string) string        { return f.acctLabel }

func newTestManager(t *testing.T, r connectRunner, ensure func(string) error, create func(store.LLMAccount) error) *connectManager {
	t.Helper()
	return &connectManager{
		runner: r, ensure: ensure, ensureOnboarding: ensure, createAccount: create,
		dataDir: t.TempDir(), newID: func() string { return "acc1" },
		logger:       slog.New(slog.DiscardHandler),
		loginTimeout: 200 * time.Millisecond,
		pollInterval: 5 * time.Millisecond,
	}
}

func TestConnectOnboardingSuccessStagesDisabledAccountAndPublishesIDsAfterBind(t *testing.T) {
	r := &fakeRunner{installed: true, loginURL: "https://auth.example/login", auth: authLoggedIn}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error {
		t.Error("onboarding Connect called createAccount")
		return nil
	})
	m.ensureModels = func(string) error { return nil }
	bindEntered := make(chan store.LLMAccount, 1)
	releaseBind := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseBind) }) })
	bindCalls := 0
	m.bindOnboarding = func(revision int64, kind string, account store.LLMAccount) (store.OnboardingSnapshot, error) {
		bindCalls++
		if revision != 7 || kind != "codex" {
			t.Errorf("bind context = revision:%d kind:%q", revision, kind)
		}
		bindEntered <- account
		<-releaseBind
		return store.OnboardingSnapshot{State: store.OnboardingState{
			Phase: store.OnboardingPhaseSetup, ProviderKind: kind,
			ProviderID: kind, AccountID: account.ID, Revision: revision + 1,
		}}, nil
	}

	if _, err := m.start("codex", "Onboarding", &connectOnboardingContext{Revision: 7, Kind: "codex"}); err != nil {
		t.Fatalf("start onboarding = %v", err)
	}
	var staged store.LLMAccount
	select {
	case staged = <-bindEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("onboarding bind was not reached")
	}
	if staged.Enabled {
		t.Fatalf("staged Account is enabled: %+v", staged)
	}
	if st, _ := m.status("codex"); st.Phase != phasePolling || st.ProviderID != "" || st.AccountID != "" {
		t.Fatalf("state exposed IDs before bind committed: %+v", st)
	}
	releaseOnce.Do(func() { close(releaseBind) })
	st := waitPhase(t, m, "codex", phaseConnected)
	if st.ProviderID != "codex" || st.AccountID != "acc1" || bindCalls != 1 {
		t.Fatalf("terminal=%+v bindCalls=%d", st, bindCalls)
	}
}

func TestConnectNormalSuccessCreatesEnabledAccountWithoutOnboardingBind(t *testing.T) {
	r := &fakeRunner{installed: true, auth: authLoggedIn}
	var created store.LLMAccount
	bindCalls := 0
	m := newTestManager(t, r, func(string) error { return nil }, func(account store.LLMAccount) error {
		created = account
		return nil
	})
	m.ensureModels = func(string) error { return nil }
	m.bindOnboarding = func(int64, string, store.LLMAccount) (store.OnboardingSnapshot, error) {
		bindCalls++
		return store.OnboardingSnapshot{}, nil
	}

	if _, err := m.start("codex", "Normal"); err != nil {
		t.Fatal(err)
	}
	st := waitPhase(t, m, "codex", phaseConnected)
	if !created.Enabled || created.ID != "acc1" || bindCalls != 0 {
		t.Fatalf("created=%+v bindCalls=%d", created, bindCalls)
	}
	if st.ProviderID != "codex" || st.AccountID != created.ID {
		t.Fatalf("terminal=%+v", st)
	}
}

func TestConnectModelPersistenceFailureBlocksAccountAndCleansDirectory(t *testing.T) {
	r := &fakeRunner{installed: true, auth: authLoggedIn}
	created := 0
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error {
		created++
		return nil
	})
	m.ensureModels = func(string) error { return errors.New("SQL model failure at D:/private/models") }

	if _, err := m.start("codex", "Normal"); err != nil {
		t.Fatal(err)
	}
	st := waitPhase(t, m, "codex", phaseError)
	if created != 0 || st.ProviderID != "" || st.AccountID != "" {
		t.Fatalf("error terminal=%+v created=%d", st, created)
	}
	if strings.Contains(strings.ToLower(st.Error), "sql") || strings.Contains(st.Error, "D:/private") {
		t.Fatalf("terminal error leaked internal detail: %q", st.Error)
	}
	waitConnectJobDone(t, m)
	dir := filepath.Join(m.dataDir, "accounts", "codex", "acc1")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("model failure left config dir %q: %v", dir, err)
	}
}

func TestConnectOnboardingPersistenceFailuresLeaveNoIDsOrConfigDirectory(t *testing.T) {
	tests := []struct {
		name       string
		ensureErr  error
		modelsErr  error
		bindErr    error
		wantModels int
		wantBind   int
	}{
		{name: "provider", ensureErr: errors.New("provider SQL private path"), wantModels: 0, wantBind: 0},
		{name: "models", modelsErr: errors.New("model SQL private path"), wantModels: 1, wantBind: 0},
		{name: "bind", bindErr: store.ErrOnboardingConflict, wantModels: 1, wantBind: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{installed: true, auth: authLoggedIn}
			createCalls, modelCalls, bindCalls := 0, 0, 0
			m := newTestManager(t, r, func(string) error { return tt.ensureErr }, func(store.LLMAccount) error {
				createCalls++
				return nil
			})
			m.ensureModels = func(string) error {
				modelCalls++
				return tt.modelsErr
			}
			m.bindOnboarding = func(int64, string, store.LLMAccount) (store.OnboardingSnapshot, error) {
				bindCalls++
				return store.OnboardingSnapshot{}, tt.bindErr
			}
			if _, err := m.start("codex", "Onboarding", &connectOnboardingContext{Revision: 3, Kind: "codex"}); err != nil {
				t.Fatal(err)
			}
			st := waitPhase(t, m, "codex", phaseError)
			if st.ProviderID != "" || st.AccountID != "" || createCalls != 0 ||
				modelCalls != tt.wantModels || bindCalls != tt.wantBind {
				t.Fatalf("terminal=%+v create=%d models=%d bind=%d", st, createCalls, modelCalls, bindCalls)
			}
			if strings.Contains(strings.ToLower(st.Error), "sql") || strings.Contains(strings.ToLower(st.Error), "private") {
				t.Fatalf("terminal error leaked internal detail: %q", st.Error)
			}
			waitConnectJobDone(t, m)
			if _, err := os.Stat(filepath.Join(m.dataDir, "accounts", "codex", "acc1")); !os.IsNotExist(err) {
				t.Fatalf("failed persistence left config dir: %v", err)
			}
		})
	}
}

func TestConnectOnboardingFailureCleanupRetriesWithoutLeakingPaths(t *testing.T) {
	tests := []struct {
		name     string
		modelErr error
		bindErr  error
		wantBind int
	}{
		{name: "model", modelErr: errors.New("MODEL_SECRET D:/private/model-cache"), wantBind: 0},
		{name: "bind", bindErr: errors.New("BIND_SECRET D:/private/account"), wantBind: 1},
		{name: "bind CAS", bindErr: store.ErrOnboardingConflict, wantBind: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &fakeRunner{installed: true, auth: authLoggedIn}
			var logs strings.Builder
			idCalls := 0
			bindCalls := 0
			cleanupCalls := 0
			cleanupFails := true
			m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error {
				t.Error("onboarding failure called normal createAccount")
				return nil
			})
			m.logger = slog.New(slog.NewTextHandler(&logs, nil))
			m.ensureOnboarding = func(string) error { return nil }
			m.ensureModels = func(string) error { return tt.modelErr }
			m.bindOnboarding = func(int64, string, store.LLMAccount) (store.OnboardingSnapshot, error) {
				bindCalls++
				return store.OnboardingSnapshot{}, tt.bindErr
			}
			m.newID = func() string {
				idCalls++
				return fmt.Sprintf("cleanup-account-%d", idCalls)
			}
			m.cleanupConfigDir = func(path string) error {
				cleanupCalls++
				if cleanupFails {
					return errors.New("REMOVE_SECRET " + path)
				}
				return os.RemoveAll(path)
			}
			ctx := &connectOnboardingContext{Revision: 6, Kind: "codex"}
			if _, err := m.start("codex", "cleanup", ctx); err != nil {
				t.Fatal(err)
			}
			waitConnectJobDone(t, m)
			st, _ := m.status("codex")
			if st.Phase != phaseError || st.Error != connectCleanupFailedMessage ||
				st.ProviderID != "" || st.AccountID != "" {
				t.Fatalf("cleanup-failed terminal=%+v", st)
			}
			if bindCalls != tt.wantBind || cleanupCalls != 1 || idCalls != 1 {
				t.Fatalf("bind=%d cleanup=%d ids=%d", bindCalls, cleanupCalls, idCalls)
			}
			firstDir := filepath.Join(m.dataDir, "accounts", "codex", "cleanup-account-1")
			if _, err := os.Stat(firstDir); err != nil {
				t.Fatalf("failed removal released directory ownership: %v", err)
			}
			serialized, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{firstDir, "D:/private", "MODEL_SECRET", "BIND_SECRET", "REMOVE_SECRET"} {
				if strings.Contains(string(serialized), forbidden) || strings.Contains(logs.String(), forbidden) {
					t.Fatalf("cleanup failure leaked %q: state=%s logs=%s", forbidden, serialized, logs.String())
				}
			}

			if _, err := m.start("codex", "blocked retry", ctx); !errors.Is(err, errConnectCleanupPending) {
				t.Fatalf("start while cleanup still fails = %v; want errConnectCleanupPending", err)
			}
			if cleanupCalls != 2 || idCalls != 1 {
				t.Fatalf("blocked retry cleanup=%d ids=%d; want 2/1", cleanupCalls, idCalls)
			}

			cleanupFails = false
			r.auth = authLoggedOut
			if _, err := m.start("codex", "successful cleanup retry", ctx); err != nil {
				t.Fatalf("start after cleanup became possible = %v", err)
			}
			if cleanupCalls != 3 || idCalls != 2 {
				t.Fatalf("successful retry cleanup=%d ids=%d; want 3/2", cleanupCalls, idCalls)
			}
			if _, err := os.Stat(firstDir); !os.IsNotExist(err) {
				t.Fatalf("successful retry left old owned directory: %v", err)
			}
			waitPhase(t, m, "codex", phasePolling)
			if !m.cancel("codex") {
				t.Fatal("cancel cleanup retry job returned false")
			}
			waitConnectJobDone(t, m)
		})
	}
}

func TestConnectContextReuseRequiresExactMatch(t *testing.T) {
	r := &fakeRunner{installed: true, auth: authLoggedOut}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	ctx := &connectOnboardingContext{Revision: 9, Kind: "codex"}
	if _, err := m.start("codex", "first", ctx); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, m, "codex", phasePolling)
	if _, err := m.start("codex", "identical", &connectOnboardingContext{Revision: 9, Kind: "codex"}); err != nil {
		t.Fatalf("identical context = %v; want current state", err)
	}
	for _, mismatch := range []*connectOnboardingContext{
		nil,
		{Revision: 10, Kind: "codex"},
		{Revision: 9, Kind: "claude-code"},
	} {
		if _, err := m.start("codex", "mismatch", mismatch); !errors.Is(err, errConnectBusy) {
			t.Errorf("mismatch %+v = %v; want errConnectBusy", mismatch, err)
		}
	}
	m.cancel("codex")
}

func TestConnectStateJSONPublishesIDsOnlyForSuccess(t *testing.T) {
	for _, phase := range []connectPhase{
		phaseDetecting, phaseInstalling, phaseAwaitingLogin, phasePolling, phaseError, phaseCanceled,
	} {
		state := connectState{Kind: "codex", Phase: phase, Error: "bounded"}
		body, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["providerId"]; ok {
			t.Errorf("phase %q exposed providerId: %s", phase, body)
		}
		if _, ok := fields["accountId"]; ok {
			t.Errorf("phase %q exposed accountId: %s", phase, body)
		}
		for _, forbidden := range []string{"configDir", "config_dir", "credential", "D:/private"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("phase %q exposed %q: %s", phase, forbidden, body)
			}
		}
	}

	job := &connectJob{state: connectState{Kind: "codex", Phase: phasePolling}, claimed: true}
	job.finishSuccess("codex", "account-1")
	body, err := json.Marshal(job.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"providerId":"codex"`) ||
		!strings.Contains(string(body), `"accountId":"account-1"`) {
		t.Fatalf("connected state omitted persisted IDs: %s", body)
	}
	for _, forbidden := range []string{"configDir", "config_dir", "credential", "secret"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("connected state exposed %q: %s", forbidden, body)
		}
	}
}

func waitConnectJobDone(t *testing.T, m *connectManager) {
	t.Helper()
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
		t.Fatal("connect job did not finish")
	}
}

// waitPhase polls status until it reaches want (or a terminal error) or times out.
func waitPhase(t *testing.T, m *connectManager, kind string, want connectPhase) connectState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := m.status(kind)
		if st.Phase == want || st.Phase == phaseError {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not reach phase %q within 3s", want)
	return connectState{}
}

func TestConnectHappyPathAlreadyInstalled(t *testing.T) {
	var mu sync.Mutex
	var ensured, created int
	var createdAcct store.LLMAccount
	r := &fakeRunner{installed: true, loginURL: "https://auth.example/login", auth: authLoggedIn}
	m := newTestManager(t, r,
		func(string) error { mu.Lock(); ensured++; mu.Unlock(); return nil },
		func(a store.LLMAccount) error { mu.Lock(); created++; createdAcct = a; mu.Unlock(); return nil },
	)
	first, err := m.start("codex", "Tài khoản 1")
	if err != nil {
		t.Fatalf("start = %v; want nil", err)
	}
	if first.Phase == "" {
		t.Fatalf("start returned empty phase")
	}
	st := waitPhase(t, m, "codex", phaseConnected)
	if st.Phase != phaseConnected {
		t.Fatalf("phase = %q; want connected (err=%q)", st.Phase, st.Error)
	}
	if st.LoginURL != "https://auth.example/login" {
		t.Errorf("loginUrl = %q; want the login URL", st.LoginURL)
	}
	if r.installCnt != 0 {
		t.Errorf("already installed but install called %d times", r.installCnt)
	}
	mu.Lock()
	defer mu.Unlock()
	if ensured != 1 {
		t.Errorf("ensure called %d times; want 1", ensured)
	}
	if created != 1 {
		t.Errorf("createAccount called %d times; want 1", created)
	}
	// account carries the generated id + isolated config dir
	wantDir := filepath.Join(m.dataDir, "accounts", "codex", "acc1")
	if createdAcct.ID != "acc1" || createdAcct.ProviderID != "codex" ||
		createdAcct.Label != "Tài khoản 1" || createdAcct.ConfigDir != wantDir || !createdAcct.Enabled {
		t.Errorf("created account = %+v; want id=acc1 provider=codex label='Tài khoản 1' dir=%q enabled", createdAcct, wantDir)
	}
	// isolated dir actually created on disk
	if fi, err := os.Stat(wantDir); err != nil || !fi.IsDir() {
		t.Errorf("config dir not created at %q (err=%v)", wantDir, err)
	}
	// runner saw the isolated dir (isolation threaded through)
	if r.lastCfgDir != wantDir {
		t.Errorf("runner saw configDir %q; want %q", r.lastCfgDir, wantDir)
	}
	// SECURITY: config dir must NOT be serialized to the Portal
	b, _ := json.Marshal(st)
	if strings.Contains(string(b), wantDir) || strings.Contains(string(b), "configDir") ||
		strings.Contains(string(b), "config_dir") {
		t.Errorf("connectState JSON leaks the internal config dir: %s", b)
	}
}

func TestConnectInstallsWhenMissing(t *testing.T) {
	r := &fakeRunner{installed: false, loginURL: "https://x", auth: authLoggedIn}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	if _, err := m.start("codex", "Tài khoản 1"); err != nil {
		t.Fatalf("start = %v; want nil", err)
	}
	st := waitPhase(t, m, "codex", phaseConnected)
	if st.Phase != phaseConnected {
		t.Fatalf("phase = %q; want connected (err=%q)", st.Phase, st.Error)
	}
	if r.installCnt != 1 {
		t.Errorf("install called %d times; want 1 (was missing)", r.installCnt)
	}
}

func TestConnectInstallFailureIsError(t *testing.T) {
	r := &fakeRunner{installed: false, installErr: errors.New("mạng hỏng")}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.start("codex", "x")
	st := waitPhase(t, m, "codex", phaseError)
	if st.Phase != phaseError {
		t.Fatalf("phase = %q; want error", st.Phase)
	}
	if !strings.Contains(st.Error, "cài thất bại") {
		t.Errorf("error = %q; want the install-failure message", st.Error)
	}
}

func TestConnectLoginTimeoutIsError(t *testing.T) {
	// installed, login returns a URL but auth never flips to loggedIn → poll loop must time out.
	r := &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.start("codex", "x")
	st := waitPhase(t, m, "codex", phaseError)
	if st.Phase != phaseError {
		t.Fatalf("phase = %q; want error (timeout)", st.Phase)
	}
	if !strings.Contains(st.Error, "giờ") {
		t.Errorf("error = %q; want the login-timeout message", st.Error)
	}
}

// TestConnectFailureLeavesNoConfigDir proves the orphan-dir fix: a failed connect (login rejected)
// removes the config dir start() created, since no account row references it. Contrast the happy
// path (TestConnectHappyPathAlreadyInstalled) which asserts the dir survives.
func TestConnectFailureLeavesNoConfigDir(t *testing.T) {
	r := &fakeRunner{installed: true, loginErr: errors.New("đăng nhập bị từ chối")}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	if _, err := m.start("codex", "x"); err != nil {
		t.Fatalf("start = %v; want nil", err)
	}
	st := waitPhase(t, m, "codex", phaseError)
	if st.Phase != phaseError {
		t.Fatalf("phase = %q; want error", st.Phase)
	}
	// Cleanup is run()'s deferred remove, which fires just after the terminal phase is set — poll for it.
	dir := filepath.Join(m.dataDir, "accounts", "codex", "acc1")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return // removed — pass
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("config dir %q still exists after failed connect; want removed", dir)
}

func TestConnectCancelStopsLoginAndPolls(t *testing.T) {
	// auth never loggedIn → machine sits in polling until cancel.
	r := &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.start("codex", "x")
	st := waitPhase(t, m, "codex", phasePolling)
	if st.Phase != phasePolling {
		t.Fatalf("phase = %q; want polling before cancel/second-start", st.Phase)
	}
	if !m.cancel("codex") {
		t.Fatal("cancel returned false")
	}
	st, _ = m.status("codex")
	if st.Phase != phaseCanceled {
		t.Errorf("phase = %q; want canceled", st.Phase)
	}
}

type cancelCleanupGateRunner struct {
	fakeRunner
	mu         sync.Mutex
	installRun int
	entered    chan struct{}
	cancelSeen chan struct{}
	release    chan struct{}
}

func (r *cancelCleanupGateRunner) install(ctx context.Context, _ string, onLine func(string)) error {
	r.mu.Lock()
	r.installRun++
	run := r.installRun
	r.mu.Unlock()
	if run != 1 {
		return nil
	}
	if onLine != nil {
		onLine("blocked cleanup")
	}
	close(r.entered)
	<-ctx.Done()
	close(r.cancelSeen)
	<-r.release
	return ctx.Err()
}

func TestConnectCanceledJobKeepsGlobalSlotUntilRunnerCleanupReturns(t *testing.T) {
	r := &cancelCleanupGateRunner{
		fakeRunner: fakeRunner{
			installed: false, loginURL: "https://x", auth: authLoggedIn,
		},
		entered: make(chan struct{}), cancelSeen: make(chan struct{}), release: make(chan struct{}),
	}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	nextID := 0
	m.newID = func() string {
		nextID++
		return fmt.Sprintf("acc%d", nextID)
	}
	if _, err := m.start("codex", "first"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	firstJob := m.job
	m.mu.Unlock()
	select {
	case <-r.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first install did not enter")
	}
	if _, err := m.start("codex", "same-kind duplicate"); err != nil {
		t.Fatalf("same-kind start while install is gated = %v; want current job", err)
	}
	r.mu.Lock()
	installRuns := r.installRun
	r.mu.Unlock()
	if installRuns != 1 {
		t.Fatalf("same-kind start launched %d installs; want exactly 1", installRuns)
	}
	if !m.cancel("codex") {
		t.Fatal("cancel returned false")
	}
	select {
	case <-r.cancelSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not observe cancellation")
	}
	if st, _ := m.status("codex"); st.Phase != phaseCanceled {
		t.Fatalf("visible phase while cleanup is blocked = %q; want canceled", st.Phase)
	}

	_, secondErr := m.start("codex", "second")
	close(r.release)
	if !errors.Is(secondErr, errConnectBusy) {
		t.Fatalf("start while canceled runner still owned slot = %v; want errConnectBusy", secondErr)
	}
	select {
	case <-firstJob.done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled install job did not finish after runner cleanup returned")
	}
	if st := firstJob.snapshot(); st.Phase != phaseCanceled {
		t.Fatalf("canceled install final phase = %q; want canceled", st.Phase)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := m.start("codex", "after cleanup")
		if err == nil {
			break
		}
		if !errors.Is(err, errConnectBusy) {
			t.Fatalf("start after runner cleanup = %v; want nil", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("global connect slot stayed busy after runner cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	firstDir := filepath.Join(m.dataDir, "accounts", "codex", "acc1")
	if _, err := os.Stat(firstDir); !os.IsNotExist(err) {
		t.Fatalf("first job cleanup did not remove %q before slot release: %v", firstDir, err)
	}
	if st := waitPhase(t, m, "codex", phaseConnected); st.Phase != phaseConnected {
		t.Fatalf("phase after slot release = %q; want connected", st.Phase)
	}
}

type loginWaitGateRunner struct {
	fakeRunner
	mu           sync.Mutex
	loginRuns    int
	waitEntered  chan struct{}
	cancelSeen   chan struct{}
	release      chan struct{}
	waitReturned chan struct{}
}

func (r *loginWaitGateRunner) login(ctx context.Context, kind, configDir string) (string, string, func() error, error) {
	r.mu.Lock()
	r.loginRuns++
	run := r.loginRuns
	r.mu.Unlock()
	if run != 1 {
		return r.fakeRunner.login(ctx, kind, configDir)
	}
	r.lastCfgDir = configDir
	return "https://auth.example/login", "AAAA-BBBBB", func() error {
		close(r.waitEntered)
		<-ctx.Done()
		close(r.cancelSeen)
		<-r.release
		close(r.waitReturned)
		return ctx.Err()
	}, nil
}

func TestConnectLoginWaitKeepsGlobalSlotUntilItReturns(t *testing.T) {
	r := &loginWaitGateRunner{
		fakeRunner:  fakeRunner{installed: true, auth: authLoggedOut},
		waitEntered: make(chan struct{}), cancelSeen: make(chan struct{}),
		release: make(chan struct{}), waitReturned: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	t.Cleanup(release)
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	nextID := 0
	m.newID = func() string {
		nextID++
		return fmt.Sprintf("acc%d", nextID)
	}
	if _, err := m.start("codex", "first"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	firstJob := m.job
	m.mu.Unlock()
	select {
	case <-r.waitEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("login wait callback did not start")
	}
	if !m.cancel("codex") {
		t.Fatal("cancel returned false")
	}
	select {
	case <-r.cancelSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("login wait callback did not observe cancellation")
	}
	if st, _ := m.status("codex"); st.Phase != phaseCanceled {
		t.Fatalf("visible phase while login wait cleanup is blocked = %q; want canceled", st.Phase)
	}
	select {
	case <-firstJob.done:
		t.Fatal("connect job released its slot before login wait callback returned")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := m.start("claude-code", "second"); !errors.Is(err, errConnectBusy) {
		t.Fatalf("cross-kind start while login wait cleanup is blocked = %v; want errConnectBusy", err)
	}
	if _, err := os.Stat(firstJob.configDir); err != nil {
		t.Fatalf("config dir was cleaned before login wait callback returned: %v", err)
	}

	release()
	select {
	case <-r.waitReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("login wait callback did not return after release")
	}
	select {
	case <-firstJob.done:
	case <-time.After(3 * time.Second):
		t.Fatal("connect job did not release its slot after login wait callback returned")
	}
	if _, err := os.Stat(firstJob.configDir); !os.IsNotExist(err) {
		t.Fatalf("config dir still exists after joined login cleanup: %v", err)
	}
	if _, err := m.start("claude-code", "second"); err != nil {
		t.Fatalf("cross-kind start after joined login cleanup = %v; want nil", err)
	}
	m.cancel("claude-code")
}

type earlyLoginWaitRunner struct {
	fakeRunner
	mu          sync.Mutex
	loginRuns   int
	waitErr     error
	waitStarted chan struct{}
	release     chan struct{}
	poll        func() authState
}

func (r *earlyLoginWaitRunner) login(ctx context.Context, kind, configDir string) (string, string, func() error, error) {
	r.mu.Lock()
	r.loginRuns++
	run := r.loginRuns
	r.mu.Unlock()
	if run != 1 {
		return r.fakeRunner.login(ctx, kind, configDir)
	}
	r.lastCfgDir = configDir
	return "https://auth.example/login", "AAAA-BBBBB", func() error {
		close(r.waitStarted)
		<-r.release
		return r.waitErr
	}, nil
}

func (r *earlyLoginWaitRunner) pollAuth(_, configDir string) authState {
	r.lastCfgDir = configDir
	if r.poll != nil {
		return r.poll()
	}
	return r.auth
}

func finishEarlyLoginWait(t *testing.T, waitErr error) connectState {
	t.Helper()
	r := &earlyLoginWaitRunner{
		fakeRunner:  fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut},
		waitErr:     waitErr,
		waitStarted: make(chan struct{}),
		release:     make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	t.Cleanup(release)
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.loginTimeout = 5 * time.Minute
	m.pollInterval = time.Hour
	nextID := 0
	m.newID = func() string {
		nextID++
		return fmt.Sprintf("acc%d", nextID)
	}
	if _, err := m.start("codex", "first"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-r.waitStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("login wait callback did not start")
	}
	if _, err := m.start("claude-code", "before wait cleanup"); !errors.Is(err, errConnectBusy) {
		t.Fatalf("cross-kind start before wait result = %v; want errConnectBusy", err)
	}
	if _, err := os.Stat(job.configDir); err != nil {
		t.Fatalf("config dir missing before wait result: %v", err)
	}
	release()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		m.cancel("codex")
		select {
		case <-job.done:
		case <-time.After(3 * time.Second):
		}
		t.Fatal("completed login wait did not finish job promptly; it was ignored until poll timeout")
	}
	if _, err := os.Stat(job.configDir); !os.IsNotExist(err) {
		t.Fatalf("config dir still exists when completed job released slot: %v", err)
	}
	st := job.snapshot()
	if _, err := m.start("claude-code", "after cleanup"); err != nil {
		t.Fatalf("cross-kind start after wait-result cleanup = %v; want nil", err)
	}
	m.mu.Lock()
	secondJob := m.job
	m.mu.Unlock()
	m.cancel("claude-code")
	select {
	case <-secondJob.done:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement connect job did not stop during test cleanup")
	}
	return st
}

func TestConnectLoginWaitErrorFailsPromptly(t *testing.T) {
	st := finishEarlyLoginWait(t, errors.New("oauth exchange failed"))
	if st.Phase != phaseError {
		t.Fatalf("phase after login wait error = %q; want error", st.Phase)
	}
	if st.Error != "đăng nhập thất bại" {
		t.Fatalf("error after login wait error = %q; want explicit login failure", st.Error)
	}
}

func TestConnectLoginWaitNilWithoutAuthFailsPromptly(t *testing.T) {
	st := finishEarlyLoginWait(t, nil)
	if st.Phase != phaseError {
		t.Fatalf("phase after login wait success without auth = %q; want error", st.Phase)
	}
	if st.Error != "đăng nhập kết thúc nhưng chưa xác thực" {
		t.Fatalf("error after login wait success without auth = %q; want explicit incomplete-login failure", st.Error)
	}
}

func TestConnectLoginWaitNilPreservesConcurrentAuthSuccess(t *testing.T) {
	polls := 0
	r := &earlyLoginWaitRunner{
		fakeRunner:  fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut},
		waitStarted: make(chan struct{}),
		release:     make(chan struct{}),
		poll: func() authState {
			polls++
			if polls == 1 {
				return authLoggedOut
			}
			return authLoggedIn
		},
	}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.loginTimeout = 5 * time.Minute
	m.pollInterval = time.Hour
	if _, err := m.start("codex", "auth completes with wait"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-r.waitStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("login wait callback did not start")
	}
	close(r.release)
	select {
	case <-job.done:
	case <-time.After(time.Second):
		m.cancel("codex")
		t.Fatal("wait completion concurrent with auth success was ignored")
	}
	if st := job.snapshot(); st.Phase != phaseConnected {
		t.Fatalf("phase when auth appears with successful wait = %q; want connected", st.Phase)
	}
}

func useManualConnectPollTicker(m *connectManager) (chan time.Time, <-chan struct{}) {
	ticks := make(chan time.Time, 1)
	stopped := make(chan struct{})
	m.newPollTicker = func(time.Duration) connectPollTicker {
		return connectPollTicker{
			ticks: ticks,
			stop:  func() { close(stopped) },
		}
	}
	return ticks, stopped
}

func TestConnectLoginWaitNilUnknownContinuesPollingToLoggedIn(t *testing.T) {
	polls := 0
	ensured, created := 0, 0
	immediateUnknown := make(chan struct{})
	laterUnknown := make(chan struct{})
	r := &earlyLoginWaitRunner{
		fakeRunner:  fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut},
		waitStarted: make(chan struct{}),
		release:     make(chan struct{}),
		poll: func() authState {
			polls++
			switch polls {
			case 1:
				return authLoggedOut
			case 2:
				close(immediateUnknown)
				return authUnknown
			case 3:
				close(laterUnknown)
				return authUnknown
			default:
				return authLoggedIn
			}
		},
	}
	m := newTestManager(t, r,
		func(string) error { ensured++; return nil },
		func(store.LLMAccount) error { created++; return nil },
	)
	m.loginTimeout = 5 * time.Minute
	m.pollInterval = time.Hour
	ticks, tickerStopped := useManualConnectPollTicker(m)
	if _, err := m.start("codex", "unknown then logged in"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-r.waitStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("login wait callback did not start")
	}
	close(r.release)
	select {
	case <-immediateUnknown:
	case <-time.After(time.Second):
		m.cancel("codex")
		t.Fatal("authUnknown was not probed immediately after the completed wait callback")
	}
	select {
	case <-job.done:
		t.Fatal("auth polling completed before the first manual tick")
	default:
	}
	ticks <- time.Now()
	select {
	case <-laterUnknown:
	case <-time.After(time.Second):
		m.cancel("codex")
		t.Fatal("authUnknown was not rechecked after the first manual tick")
	}
	select {
	case <-job.done:
		t.Fatal("auth polling completed before the second manual tick")
	default:
	}
	ticks <- time.Now()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		m.cancel("codex")
		t.Fatal("authUnknown after wait completion did not continue bounded polling")
	}
	if st := job.snapshot(); st.Phase != phaseConnected {
		t.Fatalf("phase after auth unknown then logged in = %q; want connected", st.Phase)
	}
	if ensured != 1 || created != 1 {
		t.Fatalf("persistence calls after auth unknown then logged in = ensure:%d account:%d; want 1/1", ensured, created)
	}
	if _, err := os.Stat(job.configDir); err != nil {
		t.Fatalf("connected config dir was removed after auth unknown: %v", err)
	}
	select {
	case <-tickerStopped:
	default:
		t.Fatal("manual poll ticker was not stopped when the connect job finished")
	}
}

func TestConnectLoginWaitNilUnknownThenLoggedOutFailsExplicitly(t *testing.T) {
	polls := 0
	ensured, created := 0, 0
	immediateUnknown := make(chan struct{})
	r := &earlyLoginWaitRunner{
		fakeRunner:  fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut},
		waitStarted: make(chan struct{}),
		release:     make(chan struct{}),
		poll: func() authState {
			polls++
			switch polls {
			case 1:
				return authLoggedOut
			case 2:
				close(immediateUnknown)
				return authUnknown
			default:
				return authLoggedOut
			}
		},
	}
	m := newTestManager(t, r,
		func(string) error { ensured++; return nil },
		func(store.LLMAccount) error { created++; return nil },
	)
	m.loginTimeout = 5 * time.Minute
	m.pollInterval = time.Hour
	ticks, tickerStopped := useManualConnectPollTicker(m)
	if _, err := m.start("codex", "unknown then logged out"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-r.waitStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("login wait callback did not start")
	}
	close(r.release)
	select {
	case <-immediateUnknown:
	case <-time.After(time.Second):
		m.cancel("codex")
		t.Fatal("authUnknown was not probed immediately after the completed wait callback")
	}
	select {
	case <-job.done:
		t.Fatal("auth polling completed before the manual logout tick")
	default:
	}
	ticks <- time.Now()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		m.cancel("codex")
		t.Fatal("known logout after authUnknown did not fail promptly")
	}
	st := job.snapshot()
	if st.Phase != phaseError {
		t.Fatalf("phase after auth unknown then logged out = %q; want error", st.Phase)
	}
	if st.Error != "đăng nhập kết thúc nhưng chưa xác thực" {
		t.Fatalf("error after auth unknown then logged out = %q; want explicit incomplete-login failure", st.Error)
	}
	if ensured != 0 || created != 0 {
		t.Fatalf("persistence ran after auth unknown then logged out: ensure:%d account:%d", ensured, created)
	}
	if _, err := os.Stat(job.configDir); !os.IsNotExist(err) {
		t.Fatalf("failed auth config dir still exists: %v", err)
	}
	select {
	case <-tickerStopped:
	default:
		t.Fatal("manual poll ticker was not stopped when the connect job finished")
	}
}

type blockingSuccessfulStageRunner struct {
	stage   connectPhase
	entered chan struct{}
	release chan struct{}
}

func (r *blockingSuccessfulStageRunner) detect(string) (bool, error) {
	if r.stage == phaseDetecting {
		close(r.entered)
		<-r.release
	}
	return r.stage != phaseInstalling, nil
}

func (r *blockingSuccessfulStageRunner) install(context.Context, string, func(string)) error {
	if r.stage == phaseInstalling {
		close(r.entered)
		<-r.release
	}
	return nil
}

func (r *blockingSuccessfulStageRunner) login(ctx context.Context, _, _ string) (string, string, func() error, error) {
	if r.stage == phaseAwaitingLogin {
		close(r.entered)
		<-r.release
	}
	return "https://x", "AAAA-BBBBB", func() error {
		<-ctx.Done()
		return ctx.Err()
	}, nil
}

func (*blockingSuccessfulStageRunner) pollAuth(string, string) authState  { return authLoggedIn }
func (*blockingSuccessfulStageRunner) accountLabel(string, string) string { return "" }

func TestConnectCancelWhileBlockingStageReturnsSuccessStaysCanceled(t *testing.T) {
	tests := []struct {
		name  string
		stage connectPhase
	}{
		{name: "detect", stage: phaseDetecting},
		{name: "install", stage: phaseInstalling},
		{name: "login", stage: phaseAwaitingLogin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &blockingSuccessfulStageRunner{
				stage: tt.stage, entered: make(chan struct{}), release: make(chan struct{}),
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(r.release) }) }
			t.Cleanup(release)
			ensured, created := 0, 0
			m := newTestManager(t, r,
				func(string) error { ensured++; return nil },
				func(store.LLMAccount) error { created++; return nil },
			)
			if _, err := m.start("codex", "cancel blocked "+tt.name); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			job := m.job
			m.mu.Unlock()
			select {
			case <-r.entered:
			case <-time.After(3 * time.Second):
				t.Fatalf("%s did not enter blocking gate", tt.name)
			}
			if !m.cancel("codex") {
				t.Fatalf("cancel did not win while %s was blocked", tt.name)
			}
			release()
			select {
			case <-job.done:
			case <-time.After(3 * time.Second):
				t.Fatalf("job did not finish after blocked %s returned success", tt.name)
			}
			if st := job.snapshot(); st.Phase != phaseCanceled || st.Error != "" {
				t.Fatalf("final state after canceled %s returned success = %+v; want canceled with empty error", tt.name, st)
			}
			if ensured != 0 || created != 0 {
				t.Fatalf("persistence ran after canceled %s returned success: ensure:%d account:%d", tt.name, ensured, created)
			}
			if _, err := os.Stat(job.configDir); !os.IsNotExist(err) {
				t.Fatalf("canceled %s config dir still exists: %v", tt.name, err)
			}
		})
	}
}

type gatedLoggedInPollRunner struct {
	fakeRunner
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (r *gatedLoggedInPollRunner) pollAuth(_, configDir string) authState {
	r.lastCfgDir = configDir
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return authLoggedIn
}

func TestConnectCancelWhilePollBlockedPreventsPersistence(t *testing.T) {
	r := &gatedLoggedInPollRunner{
		fakeRunner: fakeRunner{installed: true, loginURL: "https://x"},
		entered:    make(chan struct{}), release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	t.Cleanup(release)
	ensured, created := 0, 0
	m := newTestManager(t, r,
		func(string) error { ensured++; return nil },
		func(store.LLMAccount) error { created++; return nil },
	)
	if _, err := m.start("codex", "cancel blocked poll"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-r.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("pollAuth did not enter gate")
	}
	if !m.cancel("codex") {
		t.Fatal("cancel did not win while pollAuth was blocked")
	}
	release()
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled poll job did not finish")
	}
	st := job.snapshot()
	if st.Phase != phaseCanceled || st.Error != "" {
		t.Fatalf("final canceled poll state = %+v; want canceled with empty error", st)
	}
	if ensured != 0 || created != 0 {
		t.Fatalf("persistence ran after cancel won blocked poll: ensure:%d account:%d", ensured, created)
	}
	if _, err := os.Stat(job.configDir); !os.IsNotExist(err) {
		t.Fatalf("canceled poll config dir still exists: %v", err)
	}
}

type gatedAccountLabelRunner struct {
	fakeRunner
	entered chan struct{}
	release chan struct{}
}

func (r *gatedAccountLabelRunner) accountLabel(_, _ string) string {
	close(r.entered)
	<-r.release
	return "signed-in@example.com"
}

func TestConnectCancelImmediatelyBeforePersistencePreventsWrites(t *testing.T) {
	r := &gatedAccountLabelRunner{
		fakeRunner: fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedIn},
		entered:    make(chan struct{}), release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(r.release) }) }
	t.Cleanup(release)
	ensured, created := 0, 0
	m := newTestManager(t, r,
		func(string) error { ensured++; return nil },
		func(store.LLMAccount) error { created++; return nil },
	)
	if _, err := m.start("codex", "cancel before persistence"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-r.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("account label preparation did not enter gate")
	}
	if !m.cancel("codex") {
		t.Fatal("cancel did not win before persistence claim")
	}
	release()
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
		t.Fatal("pre-persistence canceled job did not finish")
	}
	if st := job.snapshot(); st.Phase != phaseCanceled || st.Error != "" {
		t.Fatalf("final pre-persistence cancel state = %+v; want canceled with empty error", st)
	}
	if ensured != 0 || created != 0 {
		t.Fatalf("persistence ran after pre-persistence cancel: ensure:%d account:%d", ensured, created)
	}
}

type gatedLogHandler struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (*gatedLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *gatedLogHandler) Handle(context.Context, slog.Record) error {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return nil
}
func (h *gatedLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *gatedLogHandler) WithGroup(string) slog.Handler      { return h }

func TestConnectCancelBetweenErrorCheckAndTransitionWins(t *testing.T) {
	r := &earlyLoginWaitRunner{
		fakeRunner:  fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut},
		waitErr:     errors.New("oauth exchange failed"),
		waitStarted: make(chan struct{}), release: make(chan struct{}),
	}
	h := &gatedLogHandler{entered: make(chan struct{}), release: make(chan struct{})}
	var waitReleaseOnce, logReleaseOnce sync.Once
	releaseWait := func() { waitReleaseOnce.Do(func() { close(r.release) }) }
	releaseLog := func() { logReleaseOnce.Do(func() { close(h.release) }) }
	t.Cleanup(releaseWait)
	t.Cleanup(releaseLog)
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.logger = slog.New(h)
	m.loginTimeout = 5 * time.Minute
	m.pollInterval = time.Hour
	if _, err := m.start("codex", "cancel error race"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-r.waitStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("login wait callback did not start")
	}
	releaseWait()
	select {
	case <-h.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("error transition did not reach logger gate")
	}
	if !m.cancel("codex") {
		t.Fatal("cancel did not win while error transition was gated")
	}
	releaseLog()
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
		t.Fatal("error-race job did not finish")
	}
	if st := job.snapshot(); st.Phase != phaseCanceled || st.Error != "" {
		t.Fatalf("final error-race state = %+v; want canceled with empty error", st)
	}
}

func TestConnectCancelAfterSuccessClaimReportsFalse(t *testing.T) {
	r := &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedIn}
	ensureEntered := make(chan struct{})
	ensureRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(ensureRelease) }) }
	t.Cleanup(release)
	ensured, created := 0, 0
	m := newTestManager(t, r,
		func(string) error {
			close(ensureEntered)
			<-ensureRelease
			ensured++
			return nil
		},
		func(store.LLMAccount) error { created++; return nil },
	)
	if _, err := m.start("codex", "claimed success"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-ensureEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("claimed success did not enter persistence gate")
	}
	canceled := m.cancel("codex")
	release()
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
		t.Fatal("claimed success job did not finish")
	}
	if canceled {
		t.Fatal("cancel reported success after persistence claim had won")
	}
	if st := job.snapshot(); st.Phase != phaseConnected {
		t.Fatalf("phase after success claim won = %q; want connected", st.Phase)
	}
	if ensured != 1 || created != 1 {
		t.Fatalf("persistence after success claim = ensure:%d account:%d; want 1/1", ensured, created)
	}
}

type cancelDuringLoginRunner struct {
	fakeRunner
	entered chan struct{}
}

func (r *cancelDuringLoginRunner) login(ctx context.Context, _, configDir string) (string, string, func() error, error) {
	r.lastCfgDir = configDir
	close(r.entered)
	<-ctx.Done()
	return "", "", nil, ctx.Err()
}

func TestConnectCancelDuringSynchronousLoginStaysCanceled(t *testing.T) {
	r := &cancelDuringLoginRunner{
		fakeRunner: fakeRunner{installed: true, auth: authLoggedOut},
		entered:    make(chan struct{}),
	}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	if _, err := m.start("codex", "cancel during login"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	job := m.job
	m.mu.Unlock()
	select {
	case <-r.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("synchronous login did not enter")
	}
	if !m.cancel("codex") {
		t.Fatal("cancel returned false")
	}
	select {
	case <-job.done:
	case <-time.After(3 * time.Second):
		t.Fatal("connect job did not finish after synchronous login observed cancellation")
	}
	st := job.snapshot()
	if st.Phase != phaseCanceled {
		t.Fatalf("final phase after synchronous login cancellation = %q; want canceled", st.Phase)
	}
	if st.Error != "" {
		t.Fatalf("final error after synchronous login cancellation = %q; want empty", st.Error)
	}
}

// TestConnectPollingCarriesLoginURLAndCode proves login's device-auth code (not just the URL)
// reaches connectState. The happy-path fake flips authLoggedIn immediately, so the state never
// sits in polling long enough to observe LoginURL/Code — this test holds auth at loggedOut so the
// machine parks in polling, then asserts both fields, then cancels to end the job cleanly.
func TestConnectPollingCarriesLoginURLAndCode(t *testing.T) {
	r := &fakeRunner{installed: true, loginURL: "https://auth.example/login", auth: authLoggedOut}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.start("codex", "x")
	st := waitPhase(t, m, "codex", phasePolling)
	if st.Phase != phasePolling {
		t.Fatalf("phase = %q; want polling", st.Phase)
	}
	if st.LoginURL != "https://auth.example/login" {
		t.Errorf("loginUrl = %q; want the fake login URL", st.LoginURL)
	}
	if st.Code != "AAAA-BBBBB" {
		t.Errorf("code = %q; want the fake device code", st.Code)
	}
	m.cancel("codex")
}

func TestConnectSecondKindWhileBusyIsRejected(t *testing.T) {
	r := &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.start("codex", "x")
	st := waitPhase(t, m, "codex", phasePolling)
	if st.Phase != phasePolling {
		t.Fatalf("phase = %q; want polling before cancel/second-start", st.Phase)
	}
	// start() does not validate kind (that's the HTTP handler's job in Task 4); any second
	// distinct kind string exercises the one-job-at-a-time guard.
	if _, err := m.start("other", "y"); !errors.Is(err, errConnectBusy) {
		t.Errorf("start(other) while busy = %v; want errConnectBusy", err)
	}
}

func TestConnectEndpoints(t *testing.T) {
	a := newAppRouteAPI(t)
	// inject a fake runner via the package singleton; reset after.
	connectMgr = &connectManager{
		runner:        &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedIn},
		ensure:        a.st.EnsureProviderForKind,
		createAccount: a.st.CreateLLMAccount,
		dataDir:       t.TempDir(),
		newID:         func() string { return "acc1" },
		logger:        slog.New(slog.DiscardHandler),
		loginTimeout:  200 * time.Millisecond,
		pollInterval:  5 * time.Millisecond,
	}
	t.Cleanup(func() { connectMgr = nil })

	// POST start (valid kind) → 200
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/llm/providers/codex/connect", strings.NewReader(`{"label":"Tài khoản 1"}`))
	req.SetPathValue("kind", "codex")
	a.handleLLMConnectStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST connect(codex) = %d; want 200 (body=%s)", rec.Code, rec.Body)
	}

	// POST with no body → empty label defaults; same kind returns current state → still 200
	recDup := httptest.NewRecorder()
	reqDup := httptest.NewRequest("POST", "/llm/providers/codex/connect", nil)
	reqDup.SetPathValue("kind", "codex")
	a.handleLLMConnectStart(recDup, reqDup)
	if recDup.Code != http.StatusOK {
		t.Errorf("POST connect(codex, no body) = %d; want 200", recDup.Code)
	}

	// unsupported kind → 400
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/llm/providers/openai/connect", nil)
	req2.SetPathValue("kind", "openai")
	a.handleLLMConnectStart(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("POST connect(openai) = %d; want 400", rec2.Code)
	}

	// GET status → 200 (some phase)
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/llm/providers/codex/connect", nil)
	req3.SetPathValue("kind", "codex")
	a.handleLLMConnectStatus(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("GET connect status = %d; want 200", rec3.Code)
	}

	// DELETE cancel → 200
	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest("DELETE", "/llm/providers/codex/connect", nil)
	req4.SetPathValue("kind", "codex")
	a.handleLLMConnectCancel(rec4, req4)
	if rec4.Code != http.StatusOK {
		t.Errorf("DELETE connect = %d; want 200", rec4.Code)
	}
}

func TestConnectCleanupPendingEndpointDoesNotLeakPathOrRemoveError(t *testing.T) {
	a := newAppRouteAPI(t)
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	a.logger = logger
	done := make(chan struct{})
	close(done)
	const privatePath = `D:\private\credentials\auth`
	job := &connectJob{
		state:     connectState{Kind: "codex", Phase: phaseError, Error: connectCleanupFailedMessage},
		configDir: privatePath, done: done, cleanupPending: true,
	}
	connectMgr = &connectManager{
		job: job, jobKind: "codex", logger: logger,
		cleanupConfigDir: func(string) error { return errors.New("REMOVE_SECRET " + privatePath) },
	}
	t.Cleanup(func() { connectMgr = nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/llm/providers/codex/connect", nil)
	req.SetPathValue("kind", "codex")
	a.handleLLMConnectStart(rec, req)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "CONNECT_CLEANUP_FAILED") {
		t.Fatalf("cleanup-pending response status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{privatePath, "REMOVE_SECRET"} {
		if strings.Contains(rec.Body.String(), forbidden) || strings.Contains(logs.String(), forbidden) {
			t.Fatalf("cleanup endpoint leaked %q: body=%s logs=%s", forbidden, rec.Body.String(), logs.String())
		}
	}
	if !job.cleanupIsPending() {
		t.Fatal("failed endpoint retry released cleanup ownership")
	}
}

// TestConnectRoutesRegisterWithoutConflict proves the {kind} routes don't collide with the
// existing {id} routes (Go 1.22 ServeMux panics on conflicting patterns at registration).
func TestConnectRoutesRegisterWithoutConflict(t *testing.T) {
	a := newAppRouteAPI(t)
	t.Cleanup(func() { connectMgr = nil })
	mux := http.NewServeMux()
	a.registerAppRoutes(mux) // must not panic
}

func TestInstallAcceptsClaude(t *testing.T) {
	// claude-code now has a non-empty npmPackage, so install must NOT take the old reject branch.
	if cliDescriptors["claude-code"].npmPackage == "" {
		t.Fatal("precondition: claude-code.npmPackage empty (T4 not applied?)")
	}
	// Call install with an already-cancelled ctx so npm can't actually run; assert the error is NOT
	// the old 'cài tại claude.com/claude-code' rejection (i.e. we got past the reject branch).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&defaultConnectRunner{}).install(ctx, "claude-code", func(string) {})
	if err != nil && strings.Contains(err.Error(), "claude.com/claude-code") {
		t.Fatalf("still hitting the old reject branch: %v", err)
	}
}

// TestConnectInstallHelper is the child-process body for TestConnectInstallStreamsLive. It's a no-op
// unless the parent set GO_WANT_INSTALL_HELPER — the standard os/exec test pattern. It prints one
// line, then BLOCKS reading stdin (the gate); the parent releases it by closing stdin. os.Exit(0)
// before the test framework prints its own summary so only these two lines reach the scanner.
func TestConnectInstallHelper(t *testing.T) {
	if os.Getenv("GO_WANT_INSTALL_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "downloading codex")
	_, _ = bufio.NewReader(os.Stdin).ReadByte() // block until the parent closes the gate
	fmt.Fprintln(os.Stdout, "added 1 package")
	os.Exit(0)
}

// TestConnectInstallStreamsLive proves install() streams npm output LIVE, not buffered-until-exit.
// The helper process prints a line then blocks on a stdin gate (so it has NOT exited), and the test
// asserts that line reaches onLine anyway. The old buffer-then-Wait code would deadlock here (it
// reads nothing until Wait, but Wait can't return while the child blocks) → the receive times out.
// The live pipe-scan code delivers the line immediately → pass. Then we release the gate and assert
// clean completion. Race-free: no sleeps for synchronization, only failure-timeout guards.
func TestConnectInstallStreamsLive(t *testing.T) {
	gateR, gateW := io.Pipe() // child blocks reading gateR until the test closes gateW
	orig := installNPMCommand
	installNPMCommand = func(ctx context.Context, _ string) *exec.Cmd {
		c := exec.CommandContext(ctx, os.Args[0], "-test.run=TestConnectInstallHelper")
		c.Env = append(os.Environ(), "GO_WANT_INSTALL_HELPER=1")
		c.Stdin = gateR
		return c
	}
	t.Cleanup(func() { installNPMCommand = orig })

	lines := make(chan string, 8)
	done := make(chan error, 1)
	go func() {
		done <- (&defaultConnectRunner{}).install(context.Background(), "codex", func(l string) { lines <- l })
	}()

	// The first real line must arrive while the child is still blocked on the gate (i.e. before it
	// exits) — the definition of live streaming. Drain any framework preamble until we see it.
	if !waitForLine(t, lines, "downloading codex", 3*time.Second) {
		t.Fatal("no live install line before the process exited — install is buffering, not streaming")
	}
	gateW.Close() // release the gate → child prints the second line and exits 0

	if !waitForLine(t, lines, "added 1 package", 3*time.Second) {
		t.Fatal("second install line never arrived")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("install err = %v; want nil (helper exits 0)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("install did not return after the process exited")
	}
}

func TestConnectInstallCancelKillsProcessTreeAndClosesPipe(t *testing.T) {
	switch runtime.GOOS {
	case "windows", "aix", "darwin", "dragonfly", "freebsd", "linux", "netbsd", "openbsd", "solaris":
	default:
		t.Skip("managed process containment has no descendant guarantee on this platform")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the install process-tree regression")
	}
	dir := t.TempDir()
	rootPIDFile := filepath.Join(dir, "root.pid")
	childPIDFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "install-tree.js")
	js := `const {spawn}=require("child_process");const fs=require("fs");` +
		`fs.writeFileSync(process.argv[2],String(process.pid));` +
		`const code='require("fs").writeFileSync(process.argv[1],String(process.pid));setInterval(()=>{},1e9)';` +
		`const detached=process.platform==="win32";` +
		`const child=spawn(process.execPath,["-e",code,process.argv[3]],{stdio:"inherit",detached});` +
		`if(detached)child.unref();console.log("install tree ready");setInterval(()=>{},1e9);`
	if err := os.WriteFile(script, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}

	originalCommand := installNPMCommand
	if runtime.GOOS == "windows" {
		binDir := t.TempDir()
		npmScript := "@echo off\r\n\"%CONNECT_TEST_NODE%\" \"%CONNECT_TEST_SCRIPT%\" \"%CONNECT_TEST_ROOT_PID%\" \"%CONNECT_TEST_CHILD_PID%\"\r\n"
		if err := os.WriteFile(filepath.Join(binDir, "npm.cmd"), []byte(npmScript), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CONNECT_TEST_NODE", node)
		t.Setenv("CONNECT_TEST_SCRIPT", script)
		t.Setenv("CONNECT_TEST_ROOT_PID", rootPIDFile)
		t.Setenv("CONNECT_TEST_CHILD_PID", childPIDFile)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		installNPMCommand = func(context.Context, string) *exec.Cmd {
			return exec.Command("npm", "install", "-g", "ignored")
		}
	} else {
		installNPMCommand = func(context.Context, string) *exec.Cmd {
			return exec.Command(node, script, rootPIDFile, childPIDFile)
		}
	}
	t.Cleanup(func() { installNPMCommand = originalCommand })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (&defaultConnectRunner{logger: slog.New(slog.DiscardHandler)}).install(
			ctx, "codex", func(string) {},
		)
	}()
	rootPID := readGrandchildPID(t, rootPIDFile)
	childPID := readGrandchildPID(t, childPIDFile)
	cleanup := func() {
		_ = killPidTree(rootPID, "connect-install-test-cleanup", slog.New(slog.DiscardHandler))
		_ = killPidTree(childPID, "connect-install-test-cleanup", slog.New(slog.DiscardHandler))
	}
	t.Cleanup(cleanup)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("install cancellation error = %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		_, rootAlive := processStart(rootPID)
		_, childAlive := processStart(childPID)
		cleanup()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		t.Fatalf("install did not return after cancellation; root alive=%v descendant alive=%v (pipe remained open)",
			rootAlive, childAlive)
	}
	if stillAlive(rootPID, 3*time.Second) {
		t.Errorf("install root %d survived cancellation", rootPID)
	}
	if stillAlive(childPID, 3*time.Second) {
		t.Errorf("install descendant %d survived cancellation", childPID)
	}
}

func TestConnectInstallRootExitClosesLifetimeAndInheritedPipe(t *testing.T) {
	switch runtime.GOOS {
	case "windows", "aix", "darwin", "dragonfly", "freebsd", "linux", "netbsd", "openbsd", "solaris":
	default:
		t.Skip("managed process containment has no descendant guarantee on this platform")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the install root-exit regression")
	}
	dir := t.TempDir()
	rootPIDFile := filepath.Join(dir, "root.pid")
	childPIDFile := filepath.Join(dir, "child.pid")
	childReadyFile := filepath.Join(dir, "child.ready")
	script := filepath.Join(dir, "install-root-exit.js")
	js := `const {spawn}=require("child_process");const fs=require("fs");` +
		`fs.writeFileSync(process.argv[2],String(process.pid));` +
		`const code='const fs=require("fs");fs.writeFileSync(process.argv[1],String(process.pid));fs.writeFileSync(process.argv[2],"ready");setInterval(()=>{},1e9)';` +
		`const detached=process.platform==="win32";` +
		`const child=spawn(process.execPath,["-e",code,process.argv[3],process.argv[4]],{stdio:"inherit",detached});` +
		`if(detached)child.unref();` +
		`const exitWhenChildReady=()=>{if(fs.existsSync(process.argv[4])){console.log("root exiting normally");process.exit(0)}setTimeout(exitWhenChildReady,10)};exitWhenChildReady();`
	if err := os.WriteFile(script, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}

	originalCommand := installNPMCommand
	if runtime.GOOS == "windows" {
		binDir := t.TempDir()
		npmScript := "@echo off\r\n\"%CONNECT_TEST_NODE%\" \"%CONNECT_TEST_SCRIPT%\" \"%CONNECT_TEST_ROOT_PID%\" \"%CONNECT_TEST_CHILD_PID%\" \"%CONNECT_TEST_CHILD_READY%\"\r\n"
		if err := os.WriteFile(filepath.Join(binDir, "npm.cmd"), []byte(npmScript), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CONNECT_TEST_NODE", node)
		t.Setenv("CONNECT_TEST_SCRIPT", script)
		t.Setenv("CONNECT_TEST_ROOT_PID", rootPIDFile)
		t.Setenv("CONNECT_TEST_CHILD_PID", childPIDFile)
		t.Setenv("CONNECT_TEST_CHILD_READY", childReadyFile)
		t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		installNPMCommand = func(context.Context, string) *exec.Cmd {
			return exec.Command("npm", "install", "-g", "ignored")
		}
	} else {
		installNPMCommand = func(context.Context, string) *exec.Cmd {
			return exec.Command(node, script, rootPIDFile, childPIDFile, childReadyFile)
		}
	}
	t.Cleanup(func() { installNPMCommand = originalCommand })

	done := make(chan error, 1)
	go func() {
		done <- (&defaultConnectRunner{logger: slog.New(slog.DiscardHandler)}).install(
			t.Context(), "codex", func(string) {},
		)
	}()
	rootPID := readGrandchildPID(t, rootPIDFile)
	childPID := readGrandchildPID(t, childPIDFile)
	cleanup := func() {
		_ = killPidTree(rootPID, "connect-install-root-exit-test-cleanup", slog.New(slog.DiscardHandler))
		_ = killPidTree(childPID, "connect-install-root-exit-test-cleanup", slog.New(slog.DiscardHandler))
	}
	t.Cleanup(cleanup)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("install after normal root exit = %v; want nil", err)
		}
	case <-time.After(2 * time.Second):
		_, rootAlive := processStart(rootPID)
		_, childAlive := processStart(childPID)
		cleanup()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
		t.Fatalf("install did not return after normal root exit; root alive=%v descendant alive=%v (pipe remained open)",
			rootAlive, childAlive)
	}
	if stillAlive(rootPID, 3*time.Second) {
		t.Errorf("install root %d survived normal completion", rootPID)
	}
	if stillAlive(childPID, 3*time.Second) {
		t.Errorf("install descendant %d survived normal root completion", childPID)
	}
}

// waitForLine reads onLine deliveries until want is seen or the timeout fires. Robust to any lines
// the child emits before want (drained, not asserted-against).
func waitForLine(t *testing.T, lines <-chan string, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case got := <-lines:
			if got == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// gatedInstallRunner drives the state machine with an install that emits a line, then blocks until
// the test has observed it as connectState.Message, then emits a second line and returns. Proves the
// run() → onLine → Message wiring surfaces per-line install output LIVE (before install returns), so
// the Portal poll shows progress instead of a frozen panel.
type gatedInstallRunner struct {
	fakeRunner
	gate chan struct{} // test closes it after observing the first line as Message
}

func (g *gatedInstallRunner) install(_ context.Context, _ string, onLine func(string)) error {
	onLine("step 1: tải gói")
	<-g.gate // block: install has NOT returned yet
	onLine("step 2: hoàn tất")
	return nil
}

// TestConnectInstallMessageStreamsProgressively asserts connectState.Message reflects an install
// line BEFORE install returns (the machine sits in phaseInstalling with the first line as Message
// while install is gated), then advances to the second line once released.
func TestConnectInstallMessageStreamsProgressively(t *testing.T) {
	g := &gatedInstallRunner{
		fakeRunner: fakeRunner{installed: false, loginURL: "https://x", auth: authLoggedIn},
		gate:       make(chan struct{}),
	}
	m := newTestManager(t, g, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	if _, err := m.start("codex", "x"); err != nil {
		t.Fatalf("start = %v; want nil", err)
	}
	// While install is gated, Message must already carry the first line (live, before install exits).
	if st := waitMessage(t, m, "codex", "step 1: tải gói"); st.Phase != phaseInstalling {
		t.Fatalf("phase = %q while first line showing; want installing", st.Phase)
	}
	close(g.gate) // release install → it emits the second line then returns
	st := waitPhase(t, m, "codex", phaseConnected)
	if st.Phase != phaseConnected {
		t.Fatalf("phase = %q; want connected (err=%q)", st.Phase, st.Error)
	}
}

// waitMessage polls status until Message == want (or a terminal error) or times out.
func waitMessage(t *testing.T, m *connectManager, kind, want string) connectState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, _ := m.status(kind)
		if st.Message == want || st.Phase == phaseError {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Message never became %q within 3s", want)
	return connectState{}
}
