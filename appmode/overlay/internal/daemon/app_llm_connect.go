package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	Error    string       `json:"error,omitempty"`
}

// connectRunner isolates the three side-effecting steps so the state machine is testable
// without spawning a CLI. login and pollAuth receive configDir so the default runner can run
// the CLI with <envVarFor(kind)>=<configDir> (session lands in the isolated dir). detect and
// install are machine-level (no configDir).
type connectRunner interface {
	detect(kind string) (installed bool, err error)
	install(ctx context.Context, kind string, onLine func(string)) error
	login(ctx context.Context, kind, configDir string) (loginURL string, wait func() error, err error)
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
	loginURL, wait, err := m.runner.login(ctx, kind, job.configDir)
	if err != nil {
		m.fail(job, kind, "không mở được đăng nhập", err)
		return
	}
	if loginURL != "" {
		job.set(func(s *connectState) { s.LoginURL = loginURL })
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

// defaultConnectRunner is the real runner. detect is real; install/login/pollAuth are pinned
// against captured CLI output at the NEEDS-LOGIN checkpoint (Task 5b) — until then they are honest
// "not yet pinned" stubs so we never ship guessed CLI commands / URL parsing / env-threading.
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

func (d *defaultConnectRunner) install(ctx context.Context, kind string, onLine func(string)) error {
	return errors.New("cài tự động chưa được ghim (capture-first, Task 5b)")
}

func (d *defaultConnectRunner) login(ctx context.Context, kind, configDir string) (string, func() error, error) {
	return "", nil, errors.New("đăng nhập trong Portal chưa được ghim (capture-first, Task 5b)")
}

func (d *defaultConnectRunner) pollAuth(kind, configDir string) authState {
	// codex-exit auth is not wired in checkCLIAuth yet (returns authUnknown), and threading
	// CODEX_HOME=configDir into the auth probe is pinned at Task 5b. Honest unknown until then.
	return authUnknown
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
