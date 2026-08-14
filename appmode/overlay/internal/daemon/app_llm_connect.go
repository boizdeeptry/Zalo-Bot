package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
// account's config dir or credentials. Persisted IDs appear only in the connected terminal.
type connectState struct {
	Kind       string       `json:"kind"`
	Phase      connectPhase `json:"phase"`
	Message    string       `json:"message,omitempty"`
	LoginURL   string       `json:"loginUrl,omitempty"`
	Code       string       `json:"code,omitempty"` // device-auth one-time code (NOT a secret credential; expires ~15m)
	Error      string       `json:"error,omitempty"`
	ProviderID string       `json:"providerId,omitempty"`
	AccountID  string       `json:"accountId,omitempty"`
}

type connectStartRequest struct {
	Label              string `json:"label"`
	OnboardingRevision *int64 `json:"onboarding_revision,omitempty"`
}

type connectOnboardingContext struct {
	Revision int64
	Kind     string
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
	// accountLabel returns a vendor-supplied display label for the connected account, or "" to
	// keep the user-given label. codex prints no email → ""; claude-code returns the signed-in
	// email from `claude auth status --json`.
	accountLabel(kind, configDir string) string
}

var (
	errConnectBusy           = errors.New("connect: đang có phiên kết nối khác")
	errConnectCleanupPending = errors.New("connect: cleanup is still pending")
)

const (
	connectLoginTimeout         = 5 * time.Minute
	connectPollInterval         = 2 * time.Second
	connectCleanupFailedMessage = "không dọn được dữ liệu kết nối; hãy thử lại"
)

type connectPollTicker struct {
	ticks <-chan time.Time
	stop  func()
}

func startConnectPollTicker(interval time.Duration) connectPollTicker {
	ticker := time.NewTicker(interval)
	return connectPollTicker{ticks: ticker.C, stop: ticker.Stop}
}

// connectJob holds one in-flight connect attempt. configDir/accountID/label are internal (not
// in connectState). All state access goes through guarded helpers/snapshot under the mutex.
type connectJob struct {
	mu             sync.Mutex
	state          connectState
	accountID      string
	configDir      string
	label          string
	onboarding     *connectOnboardingContext
	cancel         context.CancelFunc
	done           chan struct{}
	claimed        bool // success owns persistence; protected by mu and never exposed to the Portal
	cleanupPending bool
}

func (j *connectJob) snapshot() connectState { j.mu.Lock(); defer j.mu.Unlock(); return j.state }

func terminalConnectPhase(phase connectPhase) bool {
	return phase == phaseConnected || phase == phaseError || phase == phaseCanceled
}

// transitionActive advances one expected non-terminal phase atomically. The context check and
// state check happen under the same mutex used by requestCancel, so a blocked runner cannot revive
// a canceled job when it returns. Passing the same phase updates fields without weakening the guard.
func (j *connectJob) transitionActive(
	ctx context.Context,
	expected, next connectPhase,
	mut func(*connectState),
) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if ctx.Err() != nil || j.claimed || terminalConnectPhase(j.state.Phase) || j.state.Phase != expected {
		return false
	}
	j.state.Phase = next
	if mut != nil {
		mut(&j.state)
	}
	return true
}

// requestCancel and claimSuccess share j.mu as their linearization point. If cancel wins, the
// success path observes phaseCanceled and performs no persistence. If claim wins, cancellation
// reports false and leaves the claimed persistence sequence to finish.
func (j *connectJob) requestCancel() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.claimed || terminalConnectPhase(j.state.Phase) {
		return false
	}
	j.cancel()
	j.state.Phase = phaseCanceled
	j.state.Error = ""
	return true
}

func (j *connectJob) claimSuccess() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.claimed || j.state.Phase != phasePolling {
		return false
	}
	j.claimed = true
	return true
}

func (j *connectJob) finishSuccess(providerID, accountID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.claimed {
		return
	}
	j.state.ProviderID = providerID
	j.state.AccountID = accountID
	j.state.Phase = phaseConnected
	j.state.Message = ""
}

func (j *connectJob) failIfActive(userMsg string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if terminalConnectPhase(j.state.Phase) {
		return false
	}
	j.state.Phase = phaseError
	j.state.Error = userMsg
	return true
}

func (j *connectJob) markCleanupFailed() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cleanupPending = true
	j.state.Phase = phaseError
	j.state.Error = connectCleanupFailedMessage
	j.state.ProviderID = ""
	j.state.AccountID = ""
}

func (j *connectJob) cleanupIsPending() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cleanupPending
}

func (j *connectJob) markCleanupComplete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cleanupPending = false
}

// connectManager runs ONE connect job at a time. Dependencies are injected (not the store) so
// the state machine tests without a DB: ensure creates the provider row, createAccount records
// the account, dataDir anchors the per-account config dir, newID mints the account id.
type connectManager struct {
	mu               sync.Mutex
	runner           connectRunner
	ensure           func(kind string) error
	ensureOnboarding func(kind string) error
	ensureModels     func(kind string) error
	createAccount    func(store.LLMAccount) error
	bindOnboarding   func(expectedRevision int64, kind string, account store.LLMAccount) (store.OnboardingSnapshot, error)
	dataDir          string
	newID            func() string
	logger           *slog.Logger
	job              *connectJob
	jobKind          string
	loginTimeout     time.Duration // 0 → connectLoginTimeout (5m default); test seam
	pollInterval     time.Duration // 0 → connectPollInterval (2s default); test seam
	newPollTicker    func(time.Duration) connectPollTicker
	cleanupConfigDir func(path string) error
}

func (m *connectManager) start(
	kind string,
	label string,
	onboardingArgs ...*connectOnboardingContext,
) (connectState, error) {
	var onboarding *connectOnboardingContext
	if len(onboardingArgs) > 0 && onboardingArgs[0] != nil {
		copied := *onboardingArgs[0]
		onboarding = &copied
	}
	m.mu.Lock()
	if m.job != nil {
		cur := m.job.snapshot()
		terminal := terminalConnectPhase(cur.Phase)
		finished := false
		select {
		case <-m.job.done:
			finished = true
		default:
		}
		if !finished {
			if m.jobKind == kind && !terminal &&
				equalConnectOnboardingContext(m.job.onboarding, onboarding) {
				m.mu.Unlock()
				return cur, nil
			}
			m.mu.Unlock()
			return connectState{}, errConnectBusy
		}
		if m.job.cleanupIsPending() {
			if err := m.cleanupJobConfig(m.job, m.jobKind); err != nil {
				cur = m.job.snapshot()
				m.mu.Unlock()
				return cur, err
			}
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
		state: connectState{Kind: kind, Phase: phaseDetecting}, accountID: id,
		configDir: dir, label: label, onboarding: onboarding, cancel: cancel, done: make(chan struct{}),
	}
	m.job = job
	m.jobKind = kind
	m.mu.Unlock()
	go m.run(ctx, kind, job)
	return job.snapshot(), nil
}

func equalConnectOnboardingContext(a, b *connectOnboardingContext) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Revision == b.Revision && a.Kind == b.Kind
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
	return job.requestCancel()
}

func (m *connectManager) fail(job *connectJob, kind, userMsg string, cause error) {
	if m.logger != nil {
		m.logger.Error(
			"connect failed",
			"kind", kind,
			"phase", job.snapshot().Phase,
			"errorType", fmt.Sprintf("%T", cause),
		)
	}
	job.failIfActive(userMsg)
}

// cleanupJobConfig performs one bounded cleanup attempt. A failed attempt remains owned by the
// completed job, and start/provider-mutation retries it before admitting any replacement work.
func (m *connectManager) cleanupJobConfig(job *connectJob, kind string) error {
	remove := m.cleanupConfigDir
	if remove == nil {
		remove = os.RemoveAll
	}
	if err := remove(job.configDir); err != nil {
		job.markCleanupFailed()
		if m.logger != nil {
			m.logger.Error(
				"connect cleanup failed",
				"kind", kind,
				"errorType", fmt.Sprintf("%T", err),
			)
		}
		return errConnectCleanupPending
	}
	job.markCleanupComplete()
	return nil
}

func (m *connectManager) run(ctx context.Context, kind string, job *connectJob) {
	// done closes LAST, after process cancellation and config-dir cleanup. A canceled Portal phase is
	// visible immediately, but start() must keep the process-global slot until this goroutine owns no
	// remaining process, pipe, or filesystem cleanup.
	defer close(job.done)
	// start() created job.configDir before OAuth ran. Only the success path (connected=true) writes
	// auth.json into it and records an account row. Deferred FIRST so cleanup runs after process
	// cancellation/join. Failed removal remains owned by this job and is retried before replacement.
	connected := false
	defer func() {
		if connected {
			return
		}
		_ = m.cleanupJobConfig(job, kind)
	}()
	var loginWaitDone <-chan error
	defer func() {
		// Cancel on EVERY return path, then join the login callback before config cleanup and done.
		// The callback owns the login process (or OAuth server); fire-and-forget would release the
		// process-global connect slot while that resource was still shutting down.
		job.cancel()
		if loginWaitDone != nil {
			<-loginWaitDone
		}
	}()
	installed, err := m.runner.detect(kind)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		m.fail(job, kind, "không kiểm được CLI", err)
		return
	}
	if !installed {
		if !job.transitionActive(ctx, phaseDetecting, phaseInstalling, func(s *connectState) {
			s.Message = "Đang cài…"
		}) {
			return
		}
		if err := m.runner.install(ctx, kind, func(line string) {
			job.transitionActive(ctx, phaseInstalling, phaseInstalling, func(s *connectState) {
				s.Message = line
			})
		}); err != nil {
			if ctx.Err() != nil {
				return
			}
			m.fail(job, kind, "cài thất bại", err)
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !job.transitionActive(ctx, phaseInstalling, phaseAwaitingLogin, func(s *connectState) {
			s.Message = ""
		}) {
			return
		}
	} else if !job.transitionActive(ctx, phaseDetecting, phaseAwaitingLogin, func(s *connectState) {
		s.Message = ""
	}) {
		return
	}
	loginURL, code, wait, err := m.runner.login(ctx, kind, job.configDir)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		m.fail(job, kind, "không mở được đăng nhập", err)
		return
	}
	// Start and retain the wait callback before checking cancellation. login may have launched a
	// process/OAuth server even if cancel won while login itself was blocked; the deferred join must
	// still reap that resource before cleanup and global-slot release.
	waitResult := make(chan error, 1)
	loginWaitDone = waitResult
	go func() { waitResult <- wait() }()
	if ctx.Err() != nil {
		return
	}
	if !job.transitionActive(ctx, phaseAwaitingLogin, phasePolling, func(s *connectState) {
		if loginURL != "" {
			s.LoginURL = loginURL
			s.Code = code
		}
	}) {
		return
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
	newPollTicker := m.newPollTicker
	if newPollTicker == nil {
		newPollTicker = startConnectPollTicker
	}
	ticker := newPollTicker(poll)
	defer ticker.stop()
	waitCompleted := false
	for {
		auth := m.runner.pollAuth(kind, job.configDir)
		if waitCompleted {
			switch auth {
			case authLoggedIn:
				// Continue into the success path below.
			case authLoggedOut:
				m.fail(job, kind, "đăng nhập kết thúc nhưng chưa xác thực",
					errors.New("login callback completed without authentication"))
				return
			case authUnknown:
				// Inconclusive: wait for the next bounded poll or loginCtx timeout.
			}
		}
		if auth == authLoggedIn {
			// Everything above the claim is read-only preparation. requestCancel races with this
			// claim under job.mu; all provider/account persistence stays strictly after it.
			label := m.runner.accountLabel(kind, job.configDir)
			if label == "" {
				label = job.label
			}
			now := time.Now()
			acct := store.LLMAccount{
				ID: job.accountID, ProviderID: kind, Label: label,
				ConfigDir: job.configDir, Enabled: job.onboarding == nil, AddedAt: &now,
			}
			if !job.claimSuccess() {
				return
			}
			if job.onboarding == nil {
				if m.ensure == nil {
					m.fail(job, kind, "không lưu được kết nối", errors.New("normal Provider ensure is unavailable"))
					return
				}
				if err := m.ensure(kind); err != nil {
					m.fail(job, kind, "không lưu được kết nối", err)
					return
				}
			} else {
				if m.ensureOnboarding == nil {
					m.fail(job, kind, "không lưu được kết nối", errors.New("onboarding Provider ensure is unavailable"))
					return
				}
				if err := m.ensureOnboarding(kind); err != nil {
					m.fail(job, kind, "không lưu được kết nối", err)
					return
				}
			}
			if m.ensureModels != nil {
				if err := m.ensureModels(kind); err != nil {
					m.fail(job, kind, "không lưu được kết nối", err)
					return
				}
			}
			if job.onboarding == nil {
				if m.createAccount == nil {
					m.fail(job, kind, "không lưu được kết nối", errors.New("connect Account persistence is unavailable"))
					return
				}
				if err := m.createAccount(acct); err != nil {
					m.fail(job, kind, "không lưu được kết nối", err)
					return
				}
			} else {
				if m.bindOnboarding == nil {
					m.fail(job, kind, "không lưu được kết nối", errors.New("onboarding Account bind is unavailable"))
					return
				}
				if _, err := m.bindOnboarding(job.onboarding.Revision, kind, acct); err != nil {
					m.fail(job, kind, "không lưu được kết nối", err)
					return
				}
			}
			connected = true
			job.finishSuccess(kind, acct.ID)
			return
		}
		select {
		case waitErr := <-waitResult:
			// This receive joins wait(); keep the deferred join only for paths that leave while the
			// callback is still running (cancel, timeout, or auth observed first).
			loginWaitDone = nil
			waitResult = nil
			if ctx.Err() != nil {
				return
			}
			if waitErr != nil {
				m.fail(job, kind, "đăng nhập thất bại", waitErr)
				return
			}
			// Disable the consumed channel permanently. Every probe from the next loop onward uses
			// the completed-wait switch above: loggedIn succeeds, loggedOut fails explicitly, and
			// authUnknown remains bounded by ticker/loginCtx.
			waitCompleted = true
			continue
		case <-loginCtx.Done():
			if ctx.Err() != nil {
				return
			}
			m.fail(job, kind, "hết giờ đăng nhập", nil)
			return
		case <-ticker.ticks:
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
		runner:           newDefaultConnectRunner(a.logger),
		ensure:           a.st.EnsureProviderForKind,
		ensureOnboarding: a.st.EnsureOnboardingProviderForKind,
		ensureModels: func(k string) error {
			models := cliProviderModels(a.st, k, k)
			return a.st.ReplaceLLMModels(k, store.LLMModelDiscovered, models)
		},
		createAccount:  a.st.CreateLLMAccount,
		bindOnboarding: a.st.BindOnboardingAccount,
		dataDir:        a.cfg.Dir,
		newID:          newAccountID,
		logger:         a.logger,
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

// installNPMCommand builds the `npm install -g <pkg>` command. It's a package var purely as a test
// seam: real npm isn't assumed present in the test env, so TestConnectInstallStreamsLive swaps in a
// helper-process command to exercise install()'s live pipe-scan streaming portably. Production never
// reassigns it.
var installNPMCommand = func(_ context.Context, pkg string) *exec.Cmd {
	return exec.Command("npm", "install", "-g", pkg)
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

// install runs npm and streams its combined output to onLine LIVE — each line fires as npm emits
// it, not after exit — so run()'s onLine → connectState.Message updates during the (long) download
// and the Portal poll shows progress instead of a frozen panel. An OS pipe carries stdout+stderr
// directly (no exec copy goroutine): one goroutine Waits for the root, then closes the managed
// lifetime so inherited descendant writers die and the main goroutine can scan through EOF. This
// separates root completion from pipe EOF without double-Wait or a shared-buffer race.
//
// NOTE npm suppresses its TTY progress bar in a non-TTY pipe, so live lines are sparse during the
// download — that's expected; the frontend's crawling bar + elapsed timer carry the "it's working"
// signal, and whatever npm does emit ("added N packages", notices) now shows live.
//
// ponytail: shells the ambient `npm` (inherits the daemon env — no cmd.Env set). In the packaged
// app, run.bat puts bundled node+npm on PATH and sets npm_config_prefix=<data>\cli, so this installs
// offline into a writable app dir on a zero-Node machine; `npm root -g` (resolveCLIProgram) resolves
// to the same prefix, so codex.js is found where npm just put it. On a dev box it uses PATH npm and
// the machine's global prefix, unchanged.
func (d *defaultConnectRunner) install(ctx context.Context, kind string, onLine func(string)) error {
	pkg := cliDescriptors[kind].npmPackage
	if pkg == "" {
		return fmt.Errorf("connect install: kind %q không cài qua npm", kind)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("connect install: npm canceled: %w", err)
	}
	cmd := installNPMCommand(ctx, pkg)
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("connect install: output pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw // npm writes progress to stderr; fold into the same stream
	process, err := startManagedCLIProcess(cmd, d.logger)
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return fmt.Errorf("connect install: start npm: %w", err)
	}
	rootWait := make(chan error, 1)
	go func() {
		rootWait <- cmd.Wait()
	}()
	if err := pw.Close(); err != nil {
		_ = process.terminate()
		<-rootWait
		_ = process.close()
		_ = pr.Close()
		return fmt.Errorf("connect install: close parent output pipe: %w", err)
	}
	lifetimeDone := make(chan error, 1)
	go func() {
		var waitErr error
		select {
		case <-ctx.Done():
			if err := process.terminate(); err != nil && d.logger != nil {
				d.logger.Warn("connect install: terminate npm process tree", "err", err)
			}
			waitErr = <-rootWait
		case waitErr = <-rootWait:
		}
		if closeErr := process.close(); closeErr != nil && d.logger != nil {
			d.logger.Warn("connect install: close npm process lifetime", "err", closeErr)
		}
		lifetimeDone <- waitErr
	}()
	defer pr.Close()
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		if onLine != nil {
			onLine(sc.Text())
		}
	}
	// A scan error (e.g. a >64KB line with no newline → bufio.ErrTooLong) stops consuming the OS
	// pipe while the root or a descendant may still be blocked writing. Drain until the lifetime
	// watcher closes the remaining writers; otherwise output backpressure could keep the root from
	// exiting. npm -g lines are short, but the old bounded-buffer code merely truncated them.
	if sc.Err() != nil {
		_, _ = io.Copy(io.Discard, pr)
	}
	err = <-lifetimeDone
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("connect install: npm canceled: %w", ctxErr)
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

// stripANSI removes SGR color escapes from a line before matching — shared by scanDeviceAuth and
// scanClaudeLoginURL so both scanners agree on how ANSI is handled (see connectANSIRe).
func stripANSI(s string) string { return connectANSIRe.ReplaceAllString(s, "") }

// claudeVisitRe pulls the login URL out of claude's "…visit: https://…" line. claude login is
// browser-OAuth: it prints a URL, NO device code — so the code capture in scanDeviceAuth has no
// analog here.
var claudeVisitRe = regexp.MustCompile(`visit:\s*(https://\S+)`)

// scanClaudeLoginURL reads `claude auth login --claudeai` stdout and returns the browser-OAuth URL
// (empty if none seen). Pure (reader-only) so it's testable against the captured sample directly —
// see TestScanClaudeLoginURL.
func scanClaudeLoginURL(r io.Reader) string {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if m := claudeVisitRe.FindStringSubmatch(stripANSI(sc.Text())); m != nil {
			return m[1]
		}
	}
	return ""
}

// scanDeviceAuth reads `login --device-auth` stdout and pulls the login URL + one-time code.
// Pure (no process, no I/O beyond the given reader) so it's testable against the captured
// codex 0.147.0 sample directly — see TestScanDeviceAuth.
func scanDeviceAuth(r io.Reader) (url, code string) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := stripANSI(sc.Text())
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
	if kind == "claude-code" {
		return d.loginClaude(ctx, configDir)
	}
	if kind == "codex" {
		// codex connect qua OAuth trình duyệt (thay device-auth): tự chạy authorization_code + PKCE,
		// KHÔNG spawn codex CLI, KHÔNG cần bật device-auth. Trả URL authorize (không device code);
		// wait() đổi code → ghi auth.json. Client riêng cho lượt đổi token (hạn 30s).
		url, wait, err := startCodexOAuthLogin(ctx, &http.Client{Timeout: 30 * time.Second}, configDir, d.logger)
		return url, "", wait, err
	}
	desc, ok := cliDescriptors[kind]
	if !ok {
		return "", "", nil, fmt.Errorf("connect login: kind lạ %q", kind)
	}
	varName, ok := envVarFor(kind)
	if !ok {
		return "", "", nil, fmt.Errorf("connect login: kind không phải subscription: %q", kind)
	}
	program, prefixArgs, err := resolveCLIProgramContext(ctx, desc)
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

// loginClaude runs `claude auth login --claudeai` with CLAUDE_CONFIG_DIR=configDir, scans stdout
// for the browser-OAuth URL (NO device code — claude prints only a URL), and returns a wait() that
// blocks on process exit (authorize → "Login successful." → exit 0 writes creds into configDir).
// Mirrors login()'s codex discipline EXACTLY: exec.Command + killPidTree on ctx cancel (not
// CommandContext, which would orphan claude's grandchildren), reap-before-read-stderr, and a
// timeout that kills+reaps if the URL never appears.
func (d *defaultConnectRunner) loginClaude(ctx context.Context, configDir string) (string, string, func() error, error) {
	program, prefixArgs, err := resolveCLIProgramContext(ctx, cliDescriptors["claude-code"])
	if err != nil {
		return "", "", nil, fmt.Errorf("connect login: resolve claude-code: %w", err)
	}
	argv := append(append([]string{}, prefixArgs...), "auth", "login", "--claudeai")
	cmd := exec.Command(program, argv...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
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
	// Kill the whole tree when ctx is cancelled — covers cancel during the URL-wait below AND the
	// post-return polling phase (run() cancels ctx on every terminal exit). Bounded: exits on ctx done.
	go func() {
		<-ctx.Done()
		_ = killPidTree(pid, "connect-login", d.logger)
	}()
	urlCh := make(chan string, 1)
	// Bounded: exits when the visit line is seen or the pipe closes (process exit).
	go func() { urlCh <- scanClaudeLoginURL(stdout) }()
	select {
	case u := <-urlCh:
		if u == "" {
			_ = killPidTree(pid, "connect-login", d.logger)
			// Reap BEFORE reading errBuf — cmd.Stderr is a bytes.Buffer fed by an internal copy
			// goroutine that only settles once Wait returns (same as login()'s codex branch).
			_ = cmd.Wait()
			if d.logger != nil {
				d.logger.Error("connect login: không đọc được URL", "kind", "claude-code", "stderr", errBuf.String())
			}
			return "", "", nil, errors.New("không đọc được URL đăng nhập từ claude")
		}
		wait := func() error { return cmd.Wait() }
		return u, "", wait, nil
	case <-time.After(45 * time.Second):
		_ = killPidTree(pid, "connect-login", d.logger)
		_ = cmd.Wait() // reap — see the url-empty branch above for why
		return "", "", nil, errors.New("claude không in URL đăng nhập trong 45s")
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
	if kind == "claude-code" {
		return d.pollAuthClaude(configDir)
	}
	if kind == "codex" {
		// codex connect qua OAuth: login wait() ghi thẳng auth.json, không còn hỏi `codex login status`.
		// Có token đọc được → đã đăng nhập; chưa có → chưa (state machine tiếp tục poll tới khi có).
		if _, err := readCodexTokens(configDir); err == nil {
			return authLoggedIn
		}
		return authLoggedOut
	}
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

// pollAuthClaude runs `claude auth status --json` with CLAUDE_CONFIG_DIR=configDir and reads
// loggedIn — a backup signal to login's wait-exit-0. Mirrors probeClaudeAuth's discipline:
// CommandContext is enough (claude is a single process, no node grandchildren to orphan), and the
// body is read even on non-zero exit (claude prints valid {"loggedIn":false} while exiting ≠0).
func (d *defaultConnectRunner) pollAuthClaude(configDir string) authState {
	program, prefixArgs, err := resolveCLIProgram(cliDescriptors["claude-code"])
	if err != nil {
		return authUnknown
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	argv := append(append([]string{}, prefixArgs...), "auth", "status", "--json")
	cmd := exec.CommandContext(ctx, program, argv...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	out, _ := cmd.Output() // .Output() returns stdout even on a non-zero exit
	if ctx.Err() != nil {
		return authUnknown // timed out probing → don't conclude logged-out
	}
	return claudeAuthFromJSON(out)
}

// accountLabel returns a display label for a freshly connected account. codex prints no email → ""
// (the state machine keeps the user-given label). claude-code reads the signed-in email from
// `claude auth status --json` so the account shows WHO it is; email read is best-effort — a missing
// value returns "" and the account falls back to the user-given label.
func (d *defaultConnectRunner) accountLabel(kind, configDir string) string {
	if kind != "claude-code" {
		return ""
	}
	program, prefixArgs, err := resolveCLIProgram(cliDescriptors["claude-code"])
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	argv := append(append([]string{}, prefixArgs...), "auth", "status", "--json")
	cmd := exec.CommandContext(ctx, program, argv...)
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)
	out, _ := cmd.Output() // best-effort; body is present even on non-zero exit
	return claudeEmailFromJSON(out)
}

// claudeEmailFromJSON reads the "email" field from `claude auth status --json`. Absent / bad JSON →
// "" (account keeps the user-given label). Sibling of claudeAuthFromJSON (which reads only
// loggedIn) — kept separate so that parser's callers stay untouched.
func claudeEmailFromJSON(out []byte) string {
	var v struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return ""
	}
	return v.Email
}

// --- HTTP handlers ---

func (a *api) handleLLMConnectStart(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if !subscriptionKinds[kind] {
		a.writeLLMErr(w, http.StatusBadRequest, "CONNECT_KIND_UNSUPPORTED",
			"loại tài khoản này chưa kết nối được ở bản này", map[string]string{"kind": kind})
		return
	}
	var body connectStartRequest
	if !a.decodeLLMBody(w, r, &body) {
		return // decodeLLMBody already wrote the error
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "Tài khoản"
	}
	// Admission shares the onboarding mutation gate. The lock order is always onboarding first,
	// then connectManager.mu inside start/cancel, so a new Connect cannot appear between onboarding
	// cancel/cleanup and its committed state transition.
	onboardingMutationMu.Lock()
	if r.Context().Err() != nil {
		onboardingMutationMu.Unlock()
		a.writeLLMErr(w, http.StatusRequestTimeout, "CONNECT_REQUEST_CANCELED",
			"yêu cầu kết nối đã bị huỷ", nil)
		return
	}
	var onboarding *connectOnboardingContext
	if body.OnboardingRevision != nil {
		if !a.requireOnboardingRevision(w, *body.OnboardingRevision) {
			onboardingMutationMu.Unlock()
			return
		}
		snapshot, err := a.st.OnboardingSnapshot()
		if err != nil {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStateUnavailable(w, err)
			return
		}
		if err := validateOnboardingSnapshot(snapshot); err != nil {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStateUnavailable(w, err)
			return
		}
		state := snapshot.State
		if state.Revision != *body.OnboardingRevision {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStoreError(w, store.ErrOnboardingConflict)
			return
		}
		if !onboardingRequired(state) || state.Phase != store.OnboardingPhaseConnect {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidPhase)
			return
		}
		if state.ProviderKind != kind {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStoreError(w, store.ErrOnboardingProviderUnsupported)
			return
		}
		if !connectOnboardingStateIsClean(state) {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidStagingOwnership)
			return
		}
		stage, selected := onboardingStageForKind(snapshot.Stages, kind)
		if !selected || stage.Status != "pending" || stage.ProviderID != "" ||
			stage.AccountID != "" || stage.ModelID != "" {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidStagingOwnership)
			return
		}
		onboarding = &connectOnboardingContext{Revision: state.Revision, Kind: kind}
	}
	st, err := connectMgr.start(kind, label, onboarding)
	onboardingMutationMu.Unlock()
	if errors.Is(err, errConnectBusy) {
		// Reachable now that codex and claude-code are both subscription kinds: starting one while
		// the other is mid-connect returns errConnectBusy (a second POST of the SAME kind returns
		// the current state instead; unsupported kinds are rejected 400 above).
		a.writeLLMErr(w, http.StatusConflict, "CONNECT_BUSY", "đang có phiên kết nối khác", nil)
		return
	}
	if errors.Is(err, errConnectCleanupPending) {
		a.writeLLMErr(w, http.StatusInternalServerError, "CONNECT_CLEANUP_FAILED",
			connectCleanupFailedMessage, nil)
		return
	}
	if err != nil {
		a.writeLLMInternal(w, "không khởi động được kết nối", err)
		return
	}
	a.writeJSON(w, http.StatusOK, st)
}

func onboardingRequired(state store.OnboardingState) bool {
	return state.CompletedVersion < store.CurrentOnboardingVersion || state.RestartInProgress
}

func connectOnboardingStateIsClean(state store.OnboardingState) bool {
	return state.ProviderID == "" && state.AccountID == "" && state.ModelID == "" &&
		state.StagedComboID == "" && state.TestNonceHash == "" && state.TestExpiresAt == ""
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
	ok := connectMgr.cancel(r.PathValue("kind"))
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}
