package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	waitPhase(t, m, "codex", phasePolling)
	if !m.cancel("codex") {
		t.Fatal("cancel returned false")
	}
	st, _ := m.status("codex")
	if st.Phase != phaseCanceled {
		t.Errorf("phase = %q; want canceled", st.Phase)
	}
}

func TestConnectSecondKindWhileBusyIsRejected(t *testing.T) {
	r := &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut}
	m := newTestManager(t, r, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	m.start("codex", "x")
	waitPhase(t, m, "codex", phasePolling)
	// start() does not validate kind (that's the HTTP handler's job in Task 4); any second
	// distinct kind string exercises the one-job-at-a-time guard.
	if _, err := m.start("other", "y"); !errors.Is(err, errConnectBusy) {
		t.Errorf("start(other) while busy = %v; want errConnectBusy", err)
	}
}
