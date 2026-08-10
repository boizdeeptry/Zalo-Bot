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
	if strings.Contains(string(b), "acc1") || strings.Contains(string(b), "accounts") || strings.Contains(string(b), "config_dir") {
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
