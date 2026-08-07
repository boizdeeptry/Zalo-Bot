package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"agentdc/internal/store"
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.mu.Unlock()
		return connectState{}, err
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

func (m *connectManager) fail(job *connectJob, msg string) {
	job.set(func(s *connectState) { s.Phase = phaseError; s.Error = msg })
}

func (m *connectManager) run(ctx context.Context, kind string, job *connectJob) {
	installed, err := m.runner.detect(kind)
	if err != nil {
		m.fail(job, "không kiểm được CLI: "+err.Error())
		return
	}
	if !installed {
		job.set(func(s *connectState) { s.Phase = phaseInstalling; s.Message = "Đang cài…" })
		if err := m.runner.install(ctx, kind, func(line string) {
			job.set(func(s *connectState) { s.Message = line })
		}); err != nil {
			m.fail(job, "cài thất bại")
			return
		}
	}
	job.set(func(s *connectState) { s.Phase = phaseAwaitingLogin; s.Message = "" })
	loginURL, wait, err := m.runner.login(ctx, kind, job.configDir)
	if err != nil {
		m.fail(job, "không mở được đăng nhập")
		return
	}
	if loginURL != "" {
		job.set(func(s *connectState) { s.LoginURL = loginURL })
	}
	loginCtx, stop := context.WithTimeout(ctx, connectLoginTimeout)
	defer stop()
	go func() { _ = wait() }() // exits when loginCtx/ctx done (default runner kills the tree via ctx)
	job.set(func(s *connectState) { s.Phase = phasePolling })
	tick := time.NewTicker(connectPollInterval)
	defer tick.Stop()
	for {
		if m.runner.pollAuth(kind, job.configDir) == authLoggedIn {
			if err := m.ensure(kind); err != nil {
				m.fail(job, "lưu provider lỗi: "+err.Error())
				return
			}
			now := time.Now()
			acct := store.LLMAccount{
				ID: job.accountID, ProviderID: kind, Label: job.label,
				ConfigDir: job.configDir, Enabled: true, AddedAt: &now,
			}
			if err := m.createAccount(acct); err != nil {
				m.fail(job, "lưu account lỗi: "+err.Error())
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
			m.fail(job, "hết giờ đăng nhập")
			return
		case <-tick.C:
		}
	}
}
