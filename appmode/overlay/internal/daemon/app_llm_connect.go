package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"agentdc/internal/store"

	"github.com/google/uuid"
)

type connectPhase string

const (
	phaseDetecting     connectPhase = "detecting"
	phaseInstalling    connectPhase = "installing"
	phaseAwaitingLogin connectPhase = "awaiting_login"
	phasePolling       connectPhase = "polling"
	phaseConnected     connectPhase = "connected"
	phaseError         connectPhase = "error"
	phaseCanceled      connectPhase = "canceled"
)

// connectState is the Portal-facing snapshot. SECURITY INVARIANT: it must NOT contain the
// account's config dir (an internal filesystem path). Only phase/message/loginURL/error.
type connectState struct {
	Kind     string       `json:"kind"`
	Phase    connectPhase `json:"phase"`
	Message  string       `json:"message,omitempty"`
	LoginURL string       `json:"loginUrl,omitempty"`
	Code     string       `json:"code,omitempty"` // device-auth one-time code (NOT a secret credential; expires ~15m)
	Error    string       `json:"error,omitempty"`
}

// connectRunner isolates the three side-effecting steps so the state machine is testable
// without spawning a CLI. login and pollAuth receive configDir so the default runner can run
// the CLI with <envVarFor(kind)>=<configDir> (session lands in the isolated dir). detect and
// install are machine-level (no configDir).
type connectRunner interface {
	detect(kind string) (installed bool, err error)
	install(ctx context.Context, kind string, onLine func(string)) error
	login(ctx context.Context, kind, configDir string) (loginURL, code string, wait func() error, err error)
	pollAuth(kind, configDir string) authState
}

// subscriptionKinds: only these kinds may connect. claude-code joins when its kind is unified
// (seeded claude_code → claude-code) and it routes through cliAdapter.
var subscriptionKinds = map[string]bool{"codex": true}

var errConnectBusy = errors.New("connect: đang có phiên kết nối khác")

const (
	connectLoginTimeout = 5 * time.Minute
	connectPollInterval = 2 * time.Second
)

// connectJob holds one in-flight connect attempt. configDir/accountID/label are internal (not
// in connectState). All state access goes through set/snapshot under the mutex.
type connectJob struct {
	mu        sync.Mutex
	state     connectState
	accountID string
	configDir string
	label     string
	cancel    context.CancelFunc
}

func (j *connectJob) set(mut func(*connectState)) { j.mu.Lock(); defer j.mu.Unlock(); mut(&j.state) }
func (j *connectJob) snapshot() connectState      { j.mu.Lock(); defer j.mu.Unlock(); return j.state }

// connectManager runs ONE connect job at a time. Dependencies are injected (not the store) so
// the state machine tests without a DB: ensure creates the provider row, createAccount records
// the account, dataDir anchors the per-account config dir, newID mints the account id.
type connectManager struct {
	mu            sync.Mutex
	runner        connectRunner
	ensure        func(kind string) error
	createAccount func(store.LLMAccount) error
	dataDir       string
	newID         func() string
	logger        *slog.Logger
	job           *connectJob
	jobKind       string
	loginTimeout  time.Duration // 0 → connectLoginTimeout (5m default); test seam
	pollInterval  time.Duration // 0 → connectPollInterval (2s default); test seam
}

func (m *connectManager) start(kind, label string) (connectState, error) {
	m.mu.Lock()
	if m.job != nil {
		cur := m.job.snapshot()
		terminal := cur.Phase == phaseConnected || cur.Phase == phaseError || cur.Phase == phaseCanceled
		if m.jobKind == kind && !terminal {
			m.mu.Unlock()
			return cur, nil // same kind already running — return current, not an error
		}
		if m.jobKind != kind && !terminal {
			m.mu.Unlock()
			return connectState{}, errConnectBusy
		}
	}
	id := m.newID()
	dir := accountConfigDir(m.dataDir, kind, id)
	// ponytail: MkdirAll runs under the lock; start() is user-driven and rare, so the brief
	// I/O under lock is fine. Keeping it here avoids creating a stray dir on a busy rejection.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.mu.Unlock()
		return connectState{}, fmt.Errorf("create account config dir %s: %w", dir, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &connectJob{
		state:     connectState{Kind: kind, Phase: phaseDetecting},
		accountID: id, configDir: dir, label: label, cancel: cancel,
	}
	m.job = job
	m.jobKind = kind
	m.mu.Unlock()
	go m.run(ctx, kind, job)
	return job.snapshot(), nil
}

func (m *connectManager) status(kind string) (connectState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job == nil || m.jobKind != kind {
		return connectState{}, false
	}
	return m.job.snapshot(), true
}

func (m *connectManager) cancel(kind string) bool {
	m.mu.Lock()
	job := m.job
	k := m.jobKind
	m.mu.Unlock()
	if job == nil || k != kind {
		return false
	}
	job.cancel()
	job.set(func(s *connectState) {
		if s.Phase != phaseConnected {
			s.Phase = phaseCanceled
		}
	})
	return true
}

func (m *connectManager) fail(job *connectJob, kind, userMsg string, cause error) {
	m.logger.Error("connect failed", "kind", kind, "phase", job.snapshot().Phase, "err", cause)
	job.set(func(s *connectState) { s.Phase = phaseError; s.Error = userMsg })
}

func (m *connectManager) run(ctx context.Context, kind string, job *connectJob) {
	defer job.cancel() // cancel ctx on EVERY return path — unblocks the wait() goroutine below
	installed, err := m.runner.detect(kind)
	if err != nil {
		m.fail(job, kind, "không kiểm được CLI: "+err.Error(), err)
		return
	}
	if !installed {
		job.set(func(s *connectState) { s.Phase = phaseInstalling; s.Message = "Đang cài…" })
		if err := m.runner.install(ctx, kind, func(line string) {
			job.set(func(s *connectState) { s.Message = line })
		}); err != nil {
			m.fail(job, kind, "cài thất bại", err)
			return
		}
	}
	job.set(func(s *connectState) { s.Phase = phaseAwaitingLogin; s.Message = "" })
	loginURL, code, wait, err := m.runner.login(ctx, kind, job.configDir)
	if err != nil {
		m.fail(job, kind, "không mở được đăng nhập", err)
		return
	}
	if loginURL != "" {
		job.set(func(s *connectState) { s.LoginURL = loginURL; s.Code = code })
	}
	timeout := m.loginTimeout
	if timeout == 0 {
		timeout = connectLoginTimeout
	}
	poll := m.pollInterval
	if poll == 0 {
		poll = connectPollInterval
	}
	loginCtx, stop := context.WithTimeout(ctx, timeout)
	defer stop()
	go func() { _ = wait() }() // exits when loginCtx/ctx done (default runner kills the tree via ctx)
	job.set(func(s *connectState) { s.Phase = phasePolling })
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		if m.runner.pollAuth(kind, job.configDir) == authLoggedIn {
			if err := m.ensure(kind); err != nil {
				m.fail(job, kind, "lưu provider lỗi: "+err.Error(), err)
				return
			}
			now := time.Now()
			acct := store.LLMAccount{
				ID: job.accountID, ProviderID: kind, Label: job.label,
				ConfigDir: job.configDir, Enabled: true, AddedAt: &now,
			}
			if err := m.createAccount(acct); err != nil {
				m.fail(job, kind, "lưu account lỗi: "+err.Error(), err)
				return
			}
			job.set(func(s *connectState) { s.Phase = phaseConnected; s.Message = "" })
			return
		}
		select {
		case <-loginCtx.Done():
			if ctx.Err() != nil {
				job.set(func(s *connectState) { s.Phase = phaseCanceled })
				return
			}
			m.fail(job, kind, "hết giờ đăng nhập", nil)
			return
		case <-tick.C:
		}
	}
}

// connectMgr is the process-level connect manager (one connect job at a time across the daemon).
// ponytail: package singleton for the same reason as accountSel — `api` is declared in the base
// repo (build asserts git-clean, can't add a field) and handlers are methods on *api. Initialized
// once by registerAppRoutes from the serving api; one daemon = one api instance. Tests assign it
// directly (and reset to nil via t.Cleanup).
var connectMgr *connectManager

func (a *api) newConnectManager() *connectManager {
	return &connectManager{
		runner:        newDefaultConnectRunner(a.logger),
		ensure:        a.st.EnsureProviderForKind,
		createAccount: a.st.CreateLLMAccount,
		dataDir:       a.cfg.Dir,
		newID:         newAccountID,
		logger:        a.logger,
	}
}

// newAccountID mints a filesystem-safe unique id for an account config dir. uuid.NewString is
// already a dependency (registry.go uses it for PTY session ids) — reuse it instead of rolling a
// crypto/rand+hex generator; the string is hex+hyphens, safe as a path segment.
func newAccountID() string { return uuid.NewString() }

// defaultConnectRunner is the real runner, pinned against `codex` CLI output captured live
// (codex-cli 0.147.0) at the NEEDS-LOGIN checkpoint. install shells npm; login/pollAuth spawn
// `node <codex.js>` with CODEX_HOME=configDir so each account's session stays isolated.
type defaultConnectRunner struct{ logger *slog.Logger }

func newDefaultConnectRunner(l *slog.Logger) *defaultConnectRunner {
	return &defaultConnectRunner{logger: l}
}

func (d *defaultConnectRunner) detect(kind string) (bool, error) {
	desc, ok := cliDescriptors[kind]
	if !ok {
		return false, fmt.Errorf("connect detect: kind lạ %q", kind)
	}
	if _, _, err := resolveCLIProgram(desc); err == nil {
		return true, nil
	}
	// resolveCLIProgram fails for both "not installed" and other resolution errors; treat as
	// not-installed so the install step can try (and report a clear error if it truly can't).
	return false, nil
}

// install runs npm to completion, then replays the buffered output to onLine — it does NOT
// stream live. The "installing" phase message the user sees live comes from run()'s Phase/Message
// set beforehand; per-line npm progress only appears after npm exits. Kept buffer-then-Wait
// (not a live pipe) because it's race-free as-is: see login()'s cmd.Wait() note for why writing
// combined output straight into a bytes.Buffer while still reading it would race.
//
// ponytail: shells the ambient `npm`. The packaged app bundles node+npm (build task) so this
// resolves offline on a fresh machine; on a dev box it uses PATH npm.
func (d *defaultConnectRunner) install(ctx context.Context, kind string, onLine func(string)) error {
	pkg := cliDescriptors[kind].npmPackage
	if pkg == "" {
		return fmt.Errorf("connect install: kind %q không cài qua npm", kind)
	}
	cmd := exec.CommandContext(ctx, "npm", "install", "-g", pkg)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out // npm writes progress to stderr; fold into the same stream
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("connect install: start npm: %w", err)
	}
	err := cmd.Wait()
	if onLine != nil {
		sc := bufio.NewScanner(bytes.NewReader(out.Bytes()))
		for sc.Scan() {
			onLine(sc.Text())
		}
	}
	if err != nil {
		return fmt.Errorf("connect install: npm exit: %w", err)
	}
	return nil
}

// connectURLRe/connectCodeRe parse `node <codex.js> login --device-auth` stdout — captured live
// (codex-cli 0.147.0): a "https://..." URL line, then later a one-time code like "EQ0J-QKCPZ" on
// its own line. connectCodeRe is only tried on lines that did NOT match the URL, so it can never
// grab a fragment out of the URL itself.
var connectURLRe = regexp.MustCompile(`https://\S+`)
var connectCodeRe = regexp.MustCompile(`\b[A-Z0-9]{3,6}-[A-Z0-9]{3,6}\b`)

// connectANSIRe strips SGR color escapes. codex wraps its output in ANSI (e.g. the code prints as
// "\x1b[94mEU2D-4GQCP\x1b[0m"), and it does so even with NO_COLOR set — verified live via the
// packaged daemon's E2E. Without stripping, connectCodeRe's leading \b never matches (the char
// before the code is the 'm' ending the escape, a word char → no boundary) and connectURLRe would
// capture a trailing "\x1b[0m" into the URL. Strip before matching, per line.
var connectANSIRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// scanDeviceAuth reads `login --device-auth` stdout and pulls the login URL + one-time code.
// Pure (no process, no I/O beyond the given reader) so it's testable against the captured
// codex 0.147.0 sample directly — see TestScanDeviceAuth.
func scanDeviceAuth(r io.Reader) (url, code string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := connectANSIRe.ReplaceAllString(sc.Text(), "")
		if url == "" {
			if m := connectURLRe.FindString(line); m != "" {
				url = m
				continue // don't also code-match the URL line
			}
		}
		if code == "" {
			if m := connectCodeRe.FindString(line); m != "" {
				code = m
			}
		}
		if url != "" && code != "" {
			break
		}
	}
	return url, code
}

// login runs `node <codex.js> login --device-auth` with <envVarFor(kind)>=configDir, scans stdout
// for the login URL + one-time code, and returns a wait() that blocks on process exit (success
// writes auth.json into configDir and the process exits 0). Uses exec.Command (not
// CommandContext): we kill the whole tree via killPidTree on ctx cancel, because CommandContext
// only kills the direct node child and would orphan its grandchildren — same hazard runCLIProcess
// documents.
func (d *defaultConnectRunner) login(ctx context.Context, kind, configDir string) (string, string, func() error, error) {
	desc, ok := cliDescriptors[kind]
	if !ok {
		return "", "", nil, fmt.Errorf("connect login: kind lạ %q", kind)
	}
	varName, ok := envVarFor(kind)
	if !ok {
		return "", "", nil, fmt.Errorf("connect login: kind không phải subscription: %q", kind)
	}
	program, prefixArgs, err := resolveCLIProgram(desc)
	if err != nil {
		return "", "", nil, fmt.Errorf("connect login: resolve %s: %w", kind, err)
	}
	argv := append(append([]string{}, prefixArgs...), "login", "--device-auth")
	cmd := exec.Command(program, argv...)
	cmd.Env = append(os.Environ(), varName+"="+configDir)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", nil, err
	}
	if err := cmd.Start(); err != nil {
		return "", "", nil, err
	}
	pid := cmd.Process.Pid
	// Kill the whole tree when ctx is cancelled — covers cancel during the URL-wait below AND
	// during the post-return polling phase (the connect state machine cancels ctx on every
	// terminal exit via job.cancel()). Bounded: this goroutine exits as soon as ctx is done.
	go func() {
		<-ctx.Done()
		_ = killPidTree(pid, "connect-login", d.logger)
	}()
	type parsed struct{ url, code string }
	resCh := make(chan parsed, 1)
	// Bounded: exits when the pipe closes (process exit) or the scan otherwise ends.
	go func() {
		u, c := scanDeviceAuth(stdout)
		resCh <- parsed{u, c}
	}()
	select {
	case r := <-resCh:
		if r.url == "" {
			_ = killPidTree(pid, "connect-login", d.logger)
			// cmd.Stderr is a bytes.Buffer, not *os.File — exec spawns an internal copy goroutine
			// into it that only settles once Wait returns. Reap BEFORE reading errBuf.String(),
			// mirroring runCLIProcess's <-done after killPidTree: otherwise the read races the
			// still-running copy AND the process is left unreaped.
			_ = cmd.Wait()
			if d.logger != nil {
				d.logger.Error("connect login: không đọc được URL", "kind", kind, "stderr", errBuf.String())
			}
			return "", "", nil, errors.New("không đọc được URL đăng nhập từ codex")
		}
		wait := func() error { return cmd.Wait() }
		return r.url, r.code, wait, nil
	case <-time.After(45 * time.Second):
		_ = killPidTree(pid, "connect-login", d.logger)
		_ = cmd.Wait() // reap — see the url-empty branch above for why
		return "", "", nil, errors.New("codex không in URL đăng nhập trong 45s")
	case <-ctx.Done():
		// the kill goroutine above handles the tree kill; still need to reap here too.
		_ = cmd.Wait()
		return "", "", nil, ctx.Err()
	}
}

// pollAuth runs `node <codex.js> login status` with <envVarFor(kind)>=configDir — cheap, no
// subscription turn spent. Captured live (codex-cli 0.147.0): exit 0 + "Logged in using ChatGPT"
// when signed in, exit 1 + "Not logged in" otherwise. codex prints no email, so the account label
// stays as the user gave it.
//
// Uses CommandContext (unlike login's killPidTree tree-kill): this is a short, read-only status
// probe with no node grandchildren to orphan — same reasoning as probeClaudeAuth in app_llm_cli.go.
func (d *defaultConnectRunner) pollAuth(kind, configDir string) authState {
	desc, ok := cliDescriptors[kind]
	if !ok {
		return authUnknown
	}
	varName, ok := envVarFor(kind)
	if !ok {
		return authUnknown
	}
	program, prefixArgs, err := resolveCLIProgram(desc)
	if err != nil {
		return authUnknown
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	argv := append(append([]string{}, prefixArgs...), "login", "status")
	cmd := exec.CommandContext(ctx, program, argv...)
	cmd.Env = append(os.Environ(), varName+"="+configDir)
	err = cmd.Run()
	if ctx.Err() != nil {
		return authUnknown // timed out probing → don't conclude logged-out
	}
	if err == nil {
		return codexAuthFromExit(0) // authLoggedIn
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return codexAuthFromExit(exit.ExitCode()) // exit 1 → authLoggedOut
	}
	return authUnknown // couldn't even spawn → unknown, not logged-out
}

// --- HTTP handlers ---

func (a *api) handleLLMConnectStart(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if !subscriptionKinds[kind] {
		a.writeLLMErr(w, http.StatusBadRequest, "CONNECT_KIND_UNSUPPORTED",
			"chỉ OpenAI Codex mới kết nối được ở bản này", map[string]string{"kind": kind})
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if !a.decodeLLMBody(w, r, &body) {
		return // decodeLLMBody already wrote the error
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "Tài khoản"
	}
	st, err := connectMgr.start(kind, label)
	if errors.Is(err, errConnectBusy) {
		// Unreachable while codex is the only subscription kind (a second POST of the same kind
		// returns the current state, and any other kind is rejected 400 above). Kept for when
		// claude-code becomes a second connectable kind.
		a.writeLLMErr(w, http.StatusConflict, "CONNECT_BUSY", "đang có phiên kết nối khác", nil)
		return
	}
	if err != nil {
		a.writeLLMInternal(w, "không khởi động được kết nối", err)
		return
	}
	a.writeJSON(w, http.StatusOK, st)
}

func (a *api) handleLLMConnectStatus(w http.ResponseWriter, r *http.Request) {
	st, ok := connectMgr.status(r.PathValue("kind"))
	if !ok {
		a.writeJSON(w, http.StatusOK, map[string]any{"phase": "idle"})
		return
	}
	a.writeJSON(w, http.StatusOK, st)
}

func (a *api) handleLLMConnectCancel(w http.ResponseWriter, r *http.Request) {
	connectMgr.cancel(r.PathValue("kind"))
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
