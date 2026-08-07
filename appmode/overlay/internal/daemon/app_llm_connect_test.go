package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/store"
)

type fakeRunner struct {
	installed  bool
	loginURL   string
	auth       authState
	installErr error
	loginErr   error
	installCnt int
	lastCfgDir string // configDir seen by login/pollAuth — proves isolation is threaded through
}

func (f *fakeRunner) detect(string) (bool, error) { return f.installed, nil }
func (f *fakeRunner) install(ctx context.Context, _ string, onLine func(string)) error {
	f.installCnt++
	if onLine != nil {
		onLine("cài…")
	}
	return f.installErr
}
func (f *fakeRunner) login(ctx context.Context, _, configDir string) (string, func() error, error) {
	f.lastCfgDir = configDir
	if f.loginErr != nil {
		return "", nil, f.loginErr
	}
	return f.loginURL, func() error { <-ctx.Done(); return ctx.Err() }, nil
}
func (f *fakeRunner) pollAuth(_, configDir string) authState { f.lastCfgDir = configDir; return f.auth }

func newTestManager(t *testing.T, r connectRunner, ensure func(string) error, create func(store.LLMAccount) error) *connectManager {
	t.Helper()
	return &connectManager{
		runner: r, ensure: ensure, createAccount: create,
		dataDir: t.TempDir(), newID: func() string { return "acc1" },
		logger:       slog.New(slog.DiscardHandler),
		loginTimeout: 200 * time.Millisecond,
		pollInterval: 5 * time.Millisecond,
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
	if strings.Contains(string(b), "acc1") || strings.Contains(string(b), "accounts") {
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

// TestConnectRoutesRegisterWithoutConflict proves the {kind} routes don't collide with the
// existing {id} routes (Go 1.22 ServeMux panics on conflicting patterns at registration).
func TestConnectRoutesRegisterWithoutConflict(t *testing.T) {
	a := newAppRouteAPI(t)
	t.Cleanup(func() { connectMgr = nil })
	mux := http.NewServeMux()
	a.registerAppRoutes(mux) // must not panic
}
