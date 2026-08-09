package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"agentdc/internal/agent"
)

const (
	appZaloErrorResumeNotFound = "resume_not_found"
	appZaloErrorCanceled       = "canceled"
	appZaloErrorTimeout        = "timeout"
	appZaloErrorNetwork        = "network"
	appZaloErrorPermission     = "permission"
	appZaloErrorModel          = "model"
	appZaloErrorProcess        = "process_failed"
	appZaloErrorOutput         = "output_unreadable"
	appZaloErrorArgv           = "invalid_session_argv"
	appZaloErrorStart          = "process_start_failed"

	appZaloMaxClaudeStreamLine = 8 << 20
	appZaloMaxStderrBytes      = 4 << 10
)

// appZaloClaudeCommand is the process boundary used by the session runner.
// The string arguments are stdin and working directory respectively; the
// production adapter resolves the read-only Claude profile's binary internally.
type appZaloClaudeCommand func(
	context.Context,
	string,
	[]string,
	string,
	[]string,
) (io.ReadCloser, io.ReadCloser, func() error, error)

// appZaloSessionRunInput describes either a fresh bootstrap or a resume. The
// caller owns session identity so it can persist every generation atomically.
// RecoverySessionID is used only for the single allowed resume recovery.
type appZaloSessionRunInput struct {
	SessionID         string
	Prompt            string
	Resume            bool
	RecoverySessionID string
	BootstrapPrompt   string
}

// appZaloRunResult contains only the answer and bounded telemetry needed by the
// orchestration layer. SessionID is the effective session after recovery.
type appZaloRunResult struct {
	Answer        string
	ContextTokens int64
	UsageMeasured bool
	OutputBytes   int64
	SessionID     string
	Recovered     bool
	RecoveryCode  string
}

type appZaloSessionRunner struct {
	cfg     zaloConfig
	command appZaloClaudeCommand
}

type appZaloSafeRunError struct {
	code  string
	match error
}

func (e *appZaloSafeRunError) Error() string { return e.code }

func (e *appZaloSafeRunError) Is(target error) bool {
	return e.match != nil && target == e.match
}

func appZaloRunErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var safe *appZaloSafeRunError
	if errors.As(err, &safe) {
		return safe.code
	}
	return appZaloErrorProcess
}

func appZaloNewRunError(code string, match error) error {
	return &appZaloSafeRunError{code: code, match: match}
}

// RunSession executes one Claude turn. A resume is retried only when stderr
// matches the narrow missing/corrupt transcript allowlist, and then only with
// the caller-supplied recovery ID and bootstrap prompt.
func (r appZaloSessionRunner) RunSession(
	ctx context.Context,
	in appZaloSessionRunInput,
	step func(string),
) (appZaloRunResult, error) {
	result, err := r.runAttempt(ctx, in.SessionID, in.Prompt, in.Resume, step)
	if err == nil {
		result.SessionID = in.SessionID
		return result, nil
	}
	if !in.Resume || appZaloRunErrorCode(err) != appZaloErrorResumeNotFound {
		return appZaloRunResult{}, err
	}

	result, err = r.runAttempt(ctx, in.RecoverySessionID, in.BootstrapPrompt, false, step)
	if err != nil {
		return appZaloRunResult{}, err
	}
	result.SessionID = in.RecoverySessionID
	result.Recovered = true
	result.RecoveryCode = appZaloErrorResumeNotFound
	return result, nil
}

func (r appZaloSessionRunner) runAttempt(
	ctx context.Context,
	sessionID, prompt string,
	resume bool,
	step func(string),
) (appZaloRunResult, error) {
	argv, err := consultArgv(r.cfg, sessionID)
	if err != nil {
		return appZaloRunResult{}, appZaloNewRunError(appZaloErrorArgv, nil)
	}
	if resume {
		argv, err = appZaloResumeArgv(argv, sessionID)
		if err != nil {
			return appZaloRunResult{}, appZaloNewRunError(appZaloErrorArgv, nil)
		}
	}

	prof := agent.ConsultReadOnly()
	env := prof.Env(os.Environ())
	if r.cfg.ThinkingTokens > 0 {
		env = append(env, "MAX_THINKING_TOKENS="+strconv.Itoa(r.cfg.ThinkingTokens))
	}
	command := r.command
	if command == nil {
		command = appZaloStartClaude
	}
	stdout, stderr, wait, err := command(ctx, prompt, argv, r.cfg.WorkDir, env)
	if err != nil {
		appZaloClose(stdout)
		appZaloClose(stderr)
		code, match := appZaloClassifyClaudeFailure(ctx, err, "", resume)
		if code == appZaloErrorCanceled || code == appZaloErrorTimeout {
			return appZaloRunResult{}, appZaloNewRunError(code, match)
		}
		return appZaloRunResult{}, appZaloNewRunError(appZaloErrorStart, nil)
	}
	if stdout == nil || stderr == nil || wait == nil {
		appZaloClose(stdout)
		appZaloClose(stderr)
		return appZaloRunResult{}, appZaloNewRunError(appZaloErrorStart, nil)
	}
	defer stdout.Close()
	defer stderr.Close()
	if step == nil {
		step = func(string) {}
	}
	step("agent bắt đầu đọc knowledge base")

	stderrDone := make(chan string, 1)
	go func() {
		stderrDone <- appZaloReadClipped(stderr, appZaloMaxStderrBytes)
	}()
	parsed, scanErr := appZaloParseClaudeStream(stdout, step)
	stderrText := <-stderrDone
	waitErr := wait()
	if waitErr != nil {
		code, match := appZaloClassifyClaudeFailure(ctx, waitErr, stderrText, resume)
		return appZaloRunResult{}, appZaloNewRunError(code, match)
	}
	if scanErr != nil {
		return appZaloRunResult{}, appZaloNewRunError(appZaloErrorOutput, nil)
	}
	return parsed, nil
}

// appZaloResumeArgv replaces exactly one adjacent --session-id value generated
// by consultArgv. It fails closed if the profile ever changes shape unexpectedly.
func appZaloResumeArgv(argv []string, sessionID string) ([]string, error) {
	pairAt := -1
	for i, arg := range argv {
		switch arg {
		case "--resume":
			return nil, errors.New(appZaloErrorArgv)
		case "--session-id":
			if pairAt >= 0 || i+1 >= len(argv) || argv[i+1] != sessionID {
				return nil, errors.New(appZaloErrorArgv)
			}
			pairAt = i
		}
	}
	if pairAt < 0 {
		return nil, errors.New(appZaloErrorArgv)
	}
	out := append([]string(nil), argv...)
	out[pairAt] = "--resume"
	return out, nil
}

type appZaloCountingReader struct {
	r io.Reader
	n int64
}

func (r *appZaloCountingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

func appZaloParseClaudeStream(stdout io.Reader, step func(string)) (appZaloRunResult, error) {
	counted := &appZaloCountingReader{r: stdout}
	scanner := bufio.NewScanner(counted)
	scanner.Buffer(make([]byte, 0, 64<<10), appZaloMaxClaudeStreamLine)

	var result appZaloRunResult
	var body strings.Builder
	var sawResult bool
	for scanner.Scan() {
		line := scanner.Bytes()
		body.Write(line)
		body.WriteByte('\n')
		streamStep, answer, isResult := parseStreamLine(line)
		if streamStep != "" {
			step(streamStep)
		}
		if !isResult {
			continue
		}
		result.Answer = answer
		sawResult = true
		result.ContextTokens, result.UsageMeasured = appZaloParseClaudeUsage(line)
	}
	result.OutputBytes = counted.n
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !sawResult {
		step("không thấy event result, đọc cả stdout")
		result.Answer = body.String()
	}
	return result, nil
}

func appZaloParseClaudeUsage(line []byte) (int64, bool) {
	var event struct {
		Type  string          `json:"type"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(line, &event) != nil || event.Type != "result" || len(event.Usage) == 0 {
		return 0, false
	}
	var usage map[string]json.RawMessage
	if json.Unmarshal(event.Usage, &usage) != nil {
		return 0, false
	}
	keys := [...]string{
		"input_tokens",
		"cache_creation_input_tokens",
		"cache_read_input_tokens",
		"output_tokens",
	}
	var total int64
	var measured bool
	for _, key := range keys {
		raw, ok := usage[key]
		if !ok {
			continue
		}
		var number json.Number
		if json.Unmarshal(raw, &number) != nil {
			continue
		}
		value, err := number.Int64()
		if err != nil || value < 0 {
			continue
		}
		measured = true
		if value > math.MaxInt64-total {
			total = math.MaxInt64
			continue
		}
		total += value
	}
	return total, measured
}

func appZaloClassifyClaudeFailure(
	ctx context.Context,
	waitErr error,
	stderr string,
	resume bool,
) (string, error) {
	if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return appZaloErrorTimeout, context.DeadlineExceeded
	}
	if errors.Is(waitErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return appZaloErrorCanceled, context.Canceled
	}
	normalized := appZaloNormalizeClaudeError(stderr)
	if appZaloContainsAny(normalized,
		"request timed out", "operation timed out", "network timeout", "etimedout") {
		return appZaloErrorTimeout, nil
	}
	if appZaloContainsAny(normalized,
		"network error", "connection reset", "connection refused", "unable to connect",
		"failed to connect", "econnreset") || strings.HasPrefix(normalized, "network ") {
		return appZaloErrorNetwork, nil
	}
	if appZaloContainsAny(normalized,
		"permission denied", "unauthorized", "authentication failed", "not authorized", "forbidden") {
		return appZaloErrorPermission, nil
	}
	if strings.Contains(normalized, "invalid model") || strings.Contains(normalized, "unknown model") ||
		(strings.Contains(normalized, "model ") && appZaloContainsAny(normalized, "not available", "not found")) {
		return appZaloErrorModel, nil
	}
	if resume && appZaloIsRecoverableResumeError(normalized) {
		return appZaloErrorResumeNotFound, nil
	}
	return appZaloErrorProcess, nil
}

func appZaloNormalizeClaudeError(stderr string) string {
	clean := string(agent.StripANSI([]byte(stderr)))
	return strings.Join(strings.Fields(strings.ToLower(clean)), " ")
}

func appZaloIsRecoverableResumeError(normalized string) bool {
	return appZaloContainsAny(normalized,
		"no conversation found with session id",
		"no session found with id",
		"session transcript is corrupt",
		"session transcript is corrupted",
		"corrupt session transcript",
		"failed to resume session: session not found",
		"failed to resume session: transcript corrupt")
}

func appZaloContainsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

type appZaloClippedWriter struct {
	b         strings.Builder
	remaining int
}

func (w *appZaloClippedWriter) Write(p []byte) (int, error) {
	if w.remaining > 0 {
		keep := min(len(p), w.remaining)
		_, _ = w.b.Write(p[:keep])
		w.remaining -= keep
	}
	return len(p), nil
}

func appZaloReadClipped(r io.Reader, limit int) string {
	w := appZaloClippedWriter{remaining: limit}
	_, _ = io.Copy(&w, r)
	return w.b.String()
}

func appZaloClose(closer io.Closer) {
	if closer != nil {
		_ = closer.Close()
	}
}

func appZaloStartClaude(
	ctx context.Context,
	prompt string,
	argv []string,
	workDir string,
	env []string,
) (io.ReadCloser, io.ReadCloser, func() error, error) {
	prof := agent.ConsultReadOnly()
	binary, err := exec.LookPath(prof.Binary)
	if err != nil {
		return nil, nil, nil, err
	}
	cmd := exec.CommandContext(ctx, binary, argv...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(prompt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, nil, nil, err
	}
	return stdout, stderr, cmd.Wait, nil
}
