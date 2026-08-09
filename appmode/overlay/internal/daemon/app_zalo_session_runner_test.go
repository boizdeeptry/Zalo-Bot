package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	appZaloTestSessionID         = "11111111-1111-4111-8111-111111111111"
	appZaloTestRecoverySessionID = "22222222-2222-4222-8222-222222222222"
	appZaloPipeHelperEnv         = "AGENTDC_TEST_ZALO_RUNNER_PIPE_HELPER"
)

type appZaloCommandCall struct {
	prompt  string
	argv    []string
	workDir string
	env     []string
}

type appZaloCommandResponse struct {
	stdout   string
	stderr   string
	waitErr  error
	startErr error
}

type appZaloRecordingCommand struct {
	calls     []appZaloCommandCall
	responses []appZaloCommandResponse
}

func (f *appZaloRecordingCommand) run(
	_ context.Context,
	prompt string,
	argv []string,
	workDir string,
	env []string,
) (io.ReadCloser, io.ReadCloser, func() error, error) {
	f.calls = append(f.calls, appZaloCommandCall{
		prompt:  prompt,
		argv:    slices.Clone(argv),
		workDir: workDir,
		env:     slices.Clone(env),
	})
	response := appZaloCommandResponse{}
	if len(f.responses) > 0 {
		response = f.responses[0]
		f.responses = f.responses[1:]
	}
	if response.startErr != nil {
		return nil, nil, nil, response.startErr
	}
	return io.NopCloser(strings.NewReader(response.stdout)),
		io.NopCloser(strings.NewReader(response.stderr)),
		func() error { return response.waitErr }, nil
}

func appZaloRunnerFixture(command appZaloClaudeCommand) appZaloSessionRunner {
	return appZaloSessionRunner{
		cfg: zaloConfig{
			KBRoots:        []string{`C:\knowledge-a`, `C:\knowledge-b`},
			FilesDir:       `C:\customer-files`,
			WorkDir:        `C:\zalo-work`,
			Model:          "sonnet",
			ThinkingTokens: 2048,
		},
		command: command,
	}
}

func appZaloResultFixture(answer string) string {
	return `{"type":"result","result":"` + answer + `","duration_ms":10,"num_turns":1}` + "\n"
}

func appZaloErrorResultFixture(message string) string {
	event := struct {
		Type    string `json:"type"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}{Type: "result", Result: message, IsError: true}
	encoded, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return string(encoded) + "\n"
}

func TestAppZaloSessionRunnerCreatesNamedSessionWithReadOnlyProfile(t *testing.T) {
	t.Setenv("CLAUDECODE", "must-be-stripped")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "must-also-be-stripped")
	t.Setenv("CLAUDE_CONFIG_DIR", `C:\claude-config-must-survive`)
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{{
		stdout: appZaloResultFixture("answer"),
	}}}
	runner := appZaloRunnerFixture(fake.run)
	var steps []string

	result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID: appZaloTestSessionID,
		Prompt:    "bootstrap prompt",
	}, func(step string) { steps = append(steps, step) })
	if err != nil {
		t.Fatalf("RunSession() error = %v", err)
	}
	if result.Answer != "answer" || result.SessionID != appZaloTestSessionID || result.Recovered {
		t.Fatalf("RunSession() = %+v", result)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("command calls = %d, want 1", len(fake.calls))
	}
	call := fake.calls[0]
	if call.prompt != "bootstrap prompt" {
		t.Errorf("stdin = %q, want bootstrap prompt", call.prompt)
	}
	if call.workDir != `C:\zalo-work` {
		t.Errorf("work dir = %q", call.workDir)
	}
	if !slices.Equal(call.argv[:3], []string{"-p", "--session-id", appZaloTestSessionID}) {
		t.Errorf("argv prefix = %q", call.argv)
	}
	if slices.Contains(call.argv, "--resume") {
		t.Errorf("new-session argv contains --resume: %q", call.argv)
	}
	appZaloAssertArgPair(t, call.argv, "--model", "sonnet")
	appZaloAssertArgPair(t, call.argv, "--output-format", "stream-json")
	appZaloAssertArgSequence(t, call.argv, []string{"--allowed-tools", "Read", "Grep", "Glob", "WebFetch"})
	for _, dir := range []string{`C:\knowledge-a`, `C:\knowledge-b`, `C:\customer-files`} {
		appZaloAssertArgPair(t, call.argv, "--add-dir", dir)
	}
	if !slices.Contains(call.argv, "--verbose") {
		t.Errorf("argv lacks --verbose: %q", call.argv)
	}
	appZaloAssertEnv(t, call.env, "CLAUDECODE", false)
	appZaloAssertEnv(t, call.env, "CLAUDE_CODE_ENTRYPOINT", false)
	appZaloAssertEnv(t, call.env, "CLAUDE_CONFIG_DIR", true)
	appZaloAssertEnvValue(t, call.env, "MAX_THINKING_TOKENS", "2048")
	if !slices.Contains(steps, "agent bắt đầu đọc knowledge base") {
		t.Errorf("steps = %q, missing process-start step", steps)
	}
}

func TestAppZaloSessionRunnerResumesExactSession(t *testing.T) {
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{{
		stdout: appZaloResultFixture("resumed answer"),
	}}}
	runner := appZaloRunnerFixture(fake.run)

	result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID:         appZaloTestSessionID,
		Prompt:            "delta prompt",
		Resume:            true,
		RecoverySessionID: appZaloTestRecoverySessionID,
		BootstrapPrompt:   "bootstrap prompt",
	}, func(string) {})
	if err != nil {
		t.Fatalf("RunSession() error = %v", err)
	}
	if result.Answer != "resumed answer" || result.SessionID != appZaloTestSessionID || result.Recovered {
		t.Fatalf("RunSession() = %+v", result)
	}
	argv := fake.calls[0].argv
	if !slices.Equal(argv[:3], []string{"-p", "--resume", appZaloTestSessionID}) {
		t.Errorf("argv prefix = %q", argv)
	}
	if slices.Contains(argv, "--session-id") {
		t.Errorf("resume argv contains --session-id: %q", argv)
	}
}

func TestAppZaloSessionRunnerParsesOptionalUsageAndOutputBytes(t *testing.T) {
	withUsage := `{"type":"result","result":"measured","usage":{` +
		`"input_tokens":11,"cache_creation_input_tokens":22,` +
		`"cache_read_input_tokens":33,"output_tokens":44,"future_tokens":999}}` + "\n"
	withoutUsage := `{"type":"result","result":"unmeasured","usage":{` +
		`"input_tokens":"unknown-shape","future_tokens":{"nested":true}}}`
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
		{stdout: withUsage},
		{stdout: withoutUsage},
	}}
	runner := appZaloRunnerFixture(fake.run)

	measured, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID: appZaloTestSessionID,
		Prompt:    "bootstrap",
	}, func(string) {})
	if err != nil {
		t.Fatalf("measured RunSession() error = %v", err)
	}
	if measured.ContextTokens != 110 || !measured.UsageMeasured {
		t.Errorf("measured usage = %+v, want 110 measured tokens", measured)
	}
	if measured.OutputBytes != int64(len(withUsage)) {
		t.Errorf("OutputBytes = %d, want %d", measured.OutputBytes, len(withUsage))
	}

	unmeasured, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID: appZaloTestRecoverySessionID,
		Prompt:    "bootstrap",
	}, func(string) {})
	if err != nil {
		t.Fatalf("unknown usage must not fail: %v", err)
	}
	if unmeasured.Answer != "unmeasured" || unmeasured.ContextTokens != 0 || unmeasured.UsageMeasured {
		t.Errorf("unknown usage result = %+v", unmeasured)
	}
	if unmeasured.OutputBytes != int64(len(withoutUsage)) {
		t.Errorf("OutputBytes = %d, want %d", unmeasured.OutputBytes, len(withoutUsage))
	}
}

func TestAppZaloSessionRunnerKeepsExistingStreamStepsAndFallback(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"C:\\kb\\page.md"}}]}}` + "\n" +
		"plain fallback line\n"
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{{stdout: stream}}}
	runner := appZaloRunnerFixture(fake.run)
	var steps []string

	result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID: appZaloTestSessionID,
		Prompt:    "bootstrap",
	}, func(step string) { steps = append(steps, step) })
	if err != nil {
		t.Fatalf("RunSession() error = %v", err)
	}
	if result.Answer != stream {
		t.Errorf("fallback answer = %q, want exact scanned stream %q", result.Answer, stream)
	}
	if !slices.Contains(steps, "Read page.md") || !slices.Contains(steps, "không thấy event result, đọc cả stdout") {
		t.Errorf("steps = %q", steps)
	}
}

func TestAppZaloSessionRunnerRecoversVerifiedMissingResume(t *testing.T) {
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
		{stderr: "Error: No conversation found with session ID " + appZaloTestSessionID, waitErr: errors.New("exit status 1")},
		{stdout: appZaloResultFixture("recovered answer")},
	}}
	runner := appZaloRunnerFixture(fake.run)

	result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID:         appZaloTestSessionID,
		Prompt:            "delta prompt",
		Resume:            true,
		RecoverySessionID: appZaloTestRecoverySessionID,
		BootstrapPrompt:   "fresh bootstrap",
	}, func(string) {})
	if err != nil {
		t.Fatalf("RunSession() error = %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("command calls = %d, want exactly 2", len(fake.calls))
	}
	if fake.calls[0].prompt != "delta prompt" || fake.calls[1].prompt != "fresh bootstrap" {
		t.Errorf("prompts = %q, %q", fake.calls[0].prompt, fake.calls[1].prompt)
	}
	if !slices.Equal(fake.calls[0].argv[:3], []string{"-p", "--resume", appZaloTestSessionID}) {
		t.Errorf("resume argv = %q", fake.calls[0].argv)
	}
	if !slices.Equal(fake.calls[1].argv[:3], []string{"-p", "--session-id", appZaloTestRecoverySessionID}) {
		t.Errorf("recovery argv = %q", fake.calls[1].argv)
	}
	if result.Answer != "recovered answer" || result.SessionID != appZaloTestRecoverySessionID ||
		!result.Recovered || result.RecoveryCode != appZaloErrorResumeNotFound {
		t.Errorf("recovery result = %+v", result)
	}
}

func TestAppZaloSessionRunnerMarksOnlyExactRequestedResumeFailureUnusable(t *testing.T) {
	for _, tc := range []struct {
		name             string
		message          string
		want             string
		wantRecoveryCode string
	}{
		{name: "exact requested session", message: "Failed to resume session " + appZaloTestSessionID, want: "resume_unusable", wantRecoveryCode: "resume_unusable"},
		{name: "wrong session", message: "Failed to resume session " + appZaloTestRecoverySessionID, want: appZaloErrorProcess},
		{name: "missing session identity", message: "Failed to resume session", want: appZaloErrorProcess},
		{name: "print mode wording", message: "--print mode: Failed to resume session " + appZaloTestSessionID, want: appZaloErrorProcess},
		{name: "invented corruption pair", message: "--resume session load failed (" + appZaloTestSessionID + "): Unexpected end of JSON input", want: appZaloErrorProcess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
				{stdout: appZaloErrorResultFixture(tc.message)},
				{stdout: appZaloResultFixture("must not run")},
			}}
			runner := appZaloRunnerFixture(fake.run)
			result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
				SessionID:         appZaloTestSessionID,
				Prompt:            "delta with private content",
				Resume:            true,
				RecoverySessionID: appZaloTestRecoverySessionID,
				BootstrapPrompt:   "bootstrap with private content",
			}, func(string) {})
			if got := appZaloRunErrorCode(err); got != tc.want {
				t.Fatalf("error code = %q, want %q (err %v)", got, tc.want, err)
			}
			if err == nil || err.Error() != tc.want || strings.Contains(err.Error(), appZaloTestSessionID) {
				t.Errorf("unsafe error = %v", err)
			}
			if len(fake.calls) != 1 {
				t.Errorf("command calls = %d, want no same-turn retry", len(fake.calls))
			}
			if result.RecoveryAttempted || result.RecoveryCode != tc.wantRecoveryCode {
				t.Errorf("result metadata = %+v", result)
			}
		})
	}
}

func TestAppZaloSessionRunnerDoesNotRetryOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stderr  string
		waitErr error
		want    string
	}{
		{name: "network", stderr: "network error: connection reset by peer", waitErr: errors.New("exit status 1"), want: appZaloErrorNetwork},
		{name: "permission", stderr: "permission denied for this account", waitErr: errors.New("exit status 1"), want: appZaloErrorPermission},
		{name: "model", stderr: "model sonnet is not available", waitErr: errors.New("exit status 1"), want: appZaloErrorModel},
		{name: "generic", stderr: "customer-secret-canary: unexpected failure", waitErr: errors.New("exit status 1"), want: appZaloErrorProcess},
		{name: "session wording not allowlisted", stderr: "network session not found while connecting", waitErr: errors.New("exit status 1"), want: appZaloErrorNetwork},
		{name: "resume phrase after timeout", stderr: "request timed out: No conversation found with session ID " + appZaloTestSessionID, waitErr: errors.New("exit status 1"), want: appZaloErrorTimeout},
		{name: "resume phrase after network error", stderr: "network error: No conversation found with session ID " + appZaloTestSessionID, waitErr: errors.New("exit status 1"), want: appZaloErrorNetwork},
		{name: "resume phrase after permission error", stderr: "permission denied: No conversation found with session ID " + appZaloTestSessionID, waitErr: errors.New("exit status 1"), want: appZaloErrorPermission},
		{name: "resume phrase after invalid model", stderr: "invalid model sonnet: No conversation found with session ID " + appZaloTestSessionID, waitErr: errors.New("exit status 1"), want: appZaloErrorModel},
		{name: "resume phrase after unknown model", stderr: "unknown model sonnet: No conversation found with session ID " + appZaloTestSessionID, waitErr: errors.New("exit status 1"), want: appZaloErrorModel},
		{name: "generic resume failure is not corruption", stderr: "Failed to resume session: Unexpected end of JSON input", waitErr: errors.New("exit status 1"), want: appZaloErrorProcess},
		{name: "load failure permission wins", stderr: "--resume session load failed (" + appZaloTestSessionID + "): permission denied reading transcript", waitErr: errors.New("exit status 1"), want: appZaloErrorPermission},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
				{stderr: tc.stderr, waitErr: tc.waitErr},
				{stdout: appZaloResultFixture("must not run")},
			}}
			runner := appZaloRunnerFixture(fake.run)
			_, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
				SessionID:         appZaloTestSessionID,
				Prompt:            "delta prompt with private content",
				Resume:            true,
				RecoverySessionID: appZaloTestRecoverySessionID,
				BootstrapPrompt:   "bootstrap with private content",
			}, func(string) {})
			if got := appZaloRunErrorCode(err); got != tc.want {
				t.Fatalf("error code = %q, want %q (err %v)", got, tc.want, err)
			}
			if len(fake.calls) != 1 {
				t.Errorf("command calls = %d, want no retry", len(fake.calls))
			}
			if strings.Contains(err.Error(), "customer-secret-canary") || strings.Contains(err.Error(), "private content") {
				t.Errorf("error leaked raw content: %q", err)
			}
		})
	}
}

func TestAppZaloSessionRunnerClassifiesStdoutResultErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stdout     string
		stderr     string
		want       string
		wantCalls  int
		wantAnswer string
	}{
		{name: "stdout timeout", stdout: appZaloErrorResultFixture("API Error: request timed out"), want: appZaloErrorTimeout, wantCalls: 1},
		{name: "stdout network", stdout: appZaloErrorResultFixture("API network error: connection reset by peer"), want: appZaloErrorNetwork, wantCalls: 1},
		{name: "stdout model", stdout: appZaloErrorResultFixture("invalid model sonnet"), want: appZaloErrorModel, wantCalls: 1},
		{name: "stdout missing session", stdout: appZaloErrorResultFixture("No conversation found with session ID " + appZaloTestSessionID), wantCalls: 2, wantAnswer: "recovered"},
		{name: "stdout network beats stderr missing", stdout: appZaloErrorResultFixture("network error: connection refused"), stderr: "No conversation found with session ID " + appZaloTestSessionID, want: appZaloErrorNetwork, wantCalls: 1},
		{name: "stderr permission beats stdout missing", stdout: appZaloErrorResultFixture("No conversation found with session ID " + appZaloTestSessionID), stderr: "permission denied for transcript", want: appZaloErrorPermission, wantCalls: 1},
		{name: "stderr network beats unusable resume", stdout: appZaloErrorResultFixture("Failed to resume session " + appZaloTestSessionID), stderr: "network error: connection reset", want: appZaloErrorNetwork, wantCalls: 1},
		{name: "stderr permission beats unusable resume", stdout: appZaloErrorResultFixture("Failed to resume session " + appZaloTestSessionID), stderr: "permission denied reading transcript", want: appZaloErrorPermission, wantCalls: 1},
		{name: "stderr model beats unusable resume", stdout: appZaloErrorResultFixture("Failed to resume session " + appZaloTestSessionID), stderr: "model sonnet is not available", want: appZaloErrorModel, wantCalls: 1},
		{name: "split missing signature is not synthesized", stdout: appZaloErrorResultFixture("No conversation found"), stderr: "with session ID " + appZaloTestSessionID, want: appZaloErrorProcess, wantCalls: 1},
		{name: "split unusable signature is not synthesized", stdout: appZaloErrorResultFixture("Failed to resume"), stderr: "session " + appZaloTestSessionID, want: appZaloErrorProcess, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
				{stdout: tc.stdout, stderr: tc.stderr},
				{stdout: appZaloResultFixture("recovered")},
			}}
			runner := appZaloRunnerFixture(fake.run)
			result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
				SessionID:         appZaloTestSessionID,
				Prompt:            "delta with private content",
				Resume:            true,
				RecoverySessionID: appZaloTestRecoverySessionID,
				BootstrapPrompt:   "bootstrap with private content",
			}, func(string) {})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("RunSession() error = %v", err)
				}
				if result.Answer != tc.wantAnswer || !result.Recovered {
					t.Errorf("result = %+v, want recovered answer", result)
				}
			} else {
				if got := appZaloRunErrorCode(err); got != tc.want {
					t.Fatalf("error code = %q, want %q (err %v)", got, tc.want, err)
				}
				if err == nil || err.Error() != tc.want {
					t.Errorf("error = %v, want safe code only %q", err, tc.want)
				}
			}
			if len(fake.calls) != tc.wantCalls {
				t.Errorf("command calls = %d, want %d", len(fake.calls), tc.wantCalls)
			}
		})
	}
}

func TestAppZaloSessionRunnerIgnoresErrorWordsOutsideErrorResults(t *testing.T) {
	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"network error: no conversation found with session id"}]}}` + "\n" +
		appZaloResultFixture("answer")
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{{stdout: stream}}}
	runner := appZaloRunnerFixture(fake.run)

	result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID:         appZaloTestSessionID,
		Prompt:            "delta",
		Resume:            true,
		RecoverySessionID: appZaloTestRecoverySessionID,
		BootstrapPrompt:   "bootstrap",
	}, func(string) {})
	if err != nil {
		t.Fatalf("RunSession() error = %v", err)
	}
	if result.Answer != "answer" || len(fake.calls) != 1 {
		t.Errorf("result = %+v, calls = %d", result, len(fake.calls))
	}
}

func TestAppZaloSessionRunnerReturnsRecoveryMetadataWhenBootstrapFails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response appZaloCommandResponse
		want     string
	}{
		{name: "network", response: appZaloCommandResponse{stderr: "network error: connection reset by peer", waitErr: errors.New("exit status 1")}, want: appZaloErrorNetwork},
		{name: "timeout", response: appZaloCommandResponse{stderr: "private timeout details", waitErr: context.DeadlineExceeded}, want: appZaloErrorTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
				{stderr: "No conversation found with session ID " + appZaloTestSessionID, waitErr: errors.New("exit status 1")},
				tc.response,
			}}
			runner := appZaloRunnerFixture(fake.run)
			result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
				SessionID:         appZaloTestSessionID,
				Prompt:            "delta",
				Resume:            true,
				RecoverySessionID: appZaloTestRecoverySessionID,
				BootstrapPrompt:   "bootstrap",
			}, func(string) {})
			if got := appZaloRunErrorCode(err); got != tc.want {
				t.Fatalf("error code = %q, want %q", got, tc.want)
			}
			if !result.RecoveryAttempted {
				t.Errorf("result lacks true RecoveryAttempted metadata: %+v", result)
			}
			if result.RecoveryCode != appZaloErrorResumeNotFound || result.Recovered {
				t.Errorf("recovery metadata = %+v", result)
			}
			if len(fake.calls) != 2 {
				t.Errorf("command calls = %d, want exactly 2", len(fake.calls))
			}
			if err == nil || strings.Contains(err.Error(), "private") || err.Error() != tc.want {
				t.Errorf("unsafe error = %v", err)
			}
		})
	}
}

func TestAppZaloSessionRunnerPreservesCancellationAndTimeoutWithoutRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ctxErr error
		want   string
	}{
		{name: "canceled", ctxErr: context.Canceled, want: appZaloErrorCanceled},
		{name: "deadline", ctxErr: context.DeadlineExceeded, want: appZaloErrorTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(tc.ctxErr)
			fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
				{stdout: appZaloErrorResultFixture("Failed to resume session " + appZaloTestSessionID), waitErr: tc.ctxErr},
				{stdout: appZaloResultFixture("must not run")},
			}}
			runner := appZaloRunnerFixture(fake.run)
			_, err := runner.RunSession(ctx, appZaloSessionRunInput{
				SessionID:         appZaloTestSessionID,
				Prompt:            "delta",
				Resume:            true,
				RecoverySessionID: appZaloTestRecoverySessionID,
				BootstrapPrompt:   "bootstrap",
			}, func(string) {})
			if got := appZaloRunErrorCode(err); got != tc.want {
				t.Fatalf("error code = %q, want %q (err %v)", got, tc.want, err)
			}
			if len(fake.calls) != 1 {
				t.Errorf("command calls = %d, want no retry", len(fake.calls))
			}
		})
	}
}

func TestAppZaloSessionRunnerPreservesCancellationBeforeProcessStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{{
		startErr: context.Canceled,
	}}}
	runner := appZaloRunnerFixture(fake.run)

	_, err := runner.RunSession(ctx, appZaloSessionRunInput{
		SessionID:         appZaloTestSessionID,
		Prompt:            "delta",
		Resume:            true,
		RecoverySessionID: appZaloTestRecoverySessionID,
		BootstrapPrompt:   "bootstrap",
	}, func(string) {})
	if got := appZaloRunErrorCode(err); got != appZaloErrorCanceled {
		t.Fatalf("error code = %q, want %q (err %v)", got, appZaloErrorCanceled, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(%v, context.Canceled) = false", err)
	}
	if len(fake.calls) != 1 {
		t.Errorf("command calls = %d, want no retry", len(fake.calls))
	}
}

func TestAppZaloSessionRunnerRetriesResumeAtMostOnce(t *testing.T) {
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
		{stderr: "No conversation found with session ID " + appZaloTestSessionID, waitErr: errors.New("exit status 1")},
		{stderr: "No conversation found with session ID " + appZaloTestRecoverySessionID, waitErr: errors.New("exit status 1")},
		{stdout: appZaloResultFixture("must not run")},
	}}
	runner := appZaloRunnerFixture(fake.run)

	_, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID:         appZaloTestSessionID,
		Prompt:            "delta",
		Resume:            true,
		RecoverySessionID: appZaloTestRecoverySessionID,
		BootstrapPrompt:   "bootstrap",
	}, func(string) {})
	if got := appZaloRunErrorCode(err); got != appZaloErrorProcess {
		t.Fatalf("second-attempt error code = %q, want %q", got, appZaloErrorProcess)
	}
	if len(fake.calls) != 2 {
		t.Errorf("command calls = %d, want exactly 2", len(fake.calls))
	}
}

func TestAppZaloResumeArgvRequiresOneExactSessionPair(t *testing.T) {
	valid := []string{"-p", "--session-id", appZaloTestSessionID, "--verbose"}
	got, err := appZaloResumeArgv(valid, appZaloTestSessionID)
	if err != nil {
		t.Fatalf("appZaloResumeArgv() error = %v", err)
	}
	if !slices.Equal(got, []string{"-p", "--resume", appZaloTestSessionID, "--verbose"}) {
		t.Errorf("appZaloResumeArgv() = %q", got)
	}
	if !slices.Equal(valid, []string{"-p", "--session-id", appZaloTestSessionID, "--verbose"}) {
		t.Errorf("appZaloResumeArgv mutated input: %q", valid)
	}

	for _, argv := range [][]string{
		{"-p", "--verbose"},
		{"-p", "--session-id"},
		{"-p", "--session-id", appZaloTestSessionID, "--session-id", appZaloTestSessionID},
		{"-p", "--session-id", appZaloTestRecoverySessionID},
		{"-p", "--session-id", appZaloTestSessionID, "--resume", appZaloTestSessionID},
	} {
		if _, err := appZaloResumeArgv(argv, appZaloTestSessionID); err == nil {
			t.Errorf("appZaloResumeArgv(%q) error = nil", argv)
		}
	}
}

func TestAppZaloSessionRunnerRetainsEightMiBScannerLimit(t *testing.T) {
	tooLarge := strings.Repeat("x", (8<<20)+1)
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{{stdout: tooLarge}}}
	runner := appZaloRunnerFixture(fake.run)

	_, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID: appZaloTestSessionID,
		Prompt:    "bootstrap",
	}, func(string) {})
	if got := appZaloRunErrorCode(err); got != appZaloErrorOutput {
		t.Fatalf("error code = %q, want %q (err %v)", got, appZaloErrorOutput, err)
	}
}

func TestAppZaloSessionRunnerCapsAggregateFallbackWhileDrainingAllOutput(t *testing.T) {
	line := strings.Repeat("x", 1024) + "\n"
	stream := strings.Repeat(line, (appZaloMaxClaudeStreamLine/len(line))+2)
	fake := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
		{stdout: stream},
		{stdout: stream + appZaloResultFixture("bounded answer")},
	}}
	runner := appZaloRunnerFixture(fake.run)

	_, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID: appZaloTestSessionID,
		Prompt:    "bootstrap",
	}, func(string) {})
	if got := appZaloRunErrorCode(err); got != appZaloErrorOutput {
		t.Fatalf("fallback error code = %q, want %q", got, appZaloErrorOutput)
	}

	result, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID: appZaloTestRecoverySessionID,
		Prompt:    "bootstrap",
	}, func(string) {})
	if err != nil {
		t.Fatalf("valid final result after aggregate cap error = %v", err)
	}
	if result.Answer != "bounded answer" || result.OutputBytes != int64(len(stream)+len(appZaloResultFixture("bounded answer"))) {
		t.Errorf("result = %+v", result)
	}
}

func TestAppZaloRunnerPipeHelper(_ *testing.T) {
	if os.Getenv(appZaloPipeHelperEnv) != "overlong-line" {
		return
	}
	_, _ = io.WriteString(os.Stdout, strings.Repeat("x", appZaloMaxClaudeStreamLine+(64<<10)))
}

func TestAppZaloSessionRunnerDrainsOversizedOSPipeAndReapsChild(t *testing.T) {
	var waitCalls atomic.Int32
	command := func(
		ctx context.Context,
		_ string,
		_ []string,
		_ string,
		_ []string,
	) (io.ReadCloser, io.ReadCloser, func() error, error) {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppZaloRunnerPipeHelper$")
		cmd.Env = append(os.Environ(), appZaloPipeHelperEnv+"=overlong-line")
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
		return stdout, stderr, func() error {
			waitCalls.Add(1)
			return cmd.Wait()
		}, nil
	}
	runner := appZaloRunnerFixture(command)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()

	_, err := runner.RunSession(ctx, appZaloSessionRunInput{
		SessionID: appZaloTestSessionID,
		Prompt:    "bootstrap",
	}, func(string) {})
	elapsed := time.Since(started)
	if got := appZaloRunErrorCode(err); got != appZaloErrorOutput {
		t.Fatalf("error code = %q, want %q after %s (ctx err %v)", got, appZaloErrorOutput, elapsed, ctx.Err())
	}
	if ctx.Err() != nil || elapsed >= 2*time.Second {
		t.Fatalf("runner waited for cancellation instead of draining/reaping: elapsed %s, ctx err %v", elapsed, ctx.Err())
	}
	if got := waitCalls.Load(); got != 1 {
		t.Fatalf("wait calls = %d, want exactly 1", got)
	}
}

type appZaloReadErrorCloser struct {
	closed atomic.Bool
}

func (r *appZaloReadErrorCloser) Read([]byte) (int, error) {
	return 0, errors.New("injected stdout read failure")
}

func (r *appZaloReadErrorCloser) Close() error {
	r.closed.Store(true)
	return nil
}

func TestAppZaloSessionRunnerClosesUnreadableStdoutBeforeWait(t *testing.T) {
	stdout := &appZaloReadErrorCloser{}
	var closedAtWait atomic.Bool
	var waitCalls atomic.Int32
	command := func(
		context.Context,
		string,
		[]string,
		string,
		[]string,
	) (io.ReadCloser, io.ReadCloser, func() error, error) {
		return stdout, io.NopCloser(strings.NewReader("")), func() error {
			waitCalls.Add(1)
			closedAtWait.Store(stdout.closed.Load())
			return nil
		}, nil
	}
	runner := appZaloRunnerFixture(command)

	_, err := runner.RunSession(context.Background(), appZaloSessionRunInput{
		SessionID: appZaloTestSessionID,
		Prompt:    "bootstrap",
	}, func(string) {})
	if got := appZaloRunErrorCode(err); got != appZaloErrorOutput {
		t.Fatalf("error code = %q, want %q", got, appZaloErrorOutput)
	}
	if !closedAtWait.Load() {
		t.Error("stdout was not closed before Wait after drain failed")
	}
	if got := waitCalls.Load(); got != 1 {
		t.Fatalf("wait calls = %d, want exactly 1", got)
	}
}

func appZaloAssertArgPair(t *testing.T, argv []string, key, value string) {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == key && argv[i+1] == value {
			return
		}
	}
	t.Errorf("argv %q lacks adjacent %q %q", argv, key, value)
}

func appZaloAssertArgSequence(t *testing.T, argv, want []string) {
	t.Helper()
	for i := 0; i+len(want) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(want)], want) {
			return
		}
	}
	t.Errorf("argv %q lacks adjacent sequence %q", argv, want)
}

func appZaloAssertEnv(t *testing.T, env []string, key string, want bool) {
	t.Helper()
	found := false
	for _, item := range env {
		name, _, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(name, key) {
			found = true
			break
		}
	}
	if found != want {
		t.Errorf("environment contains %s = %v, want %v", key, found, want)
	}
}

func appZaloAssertEnvValue(t *testing.T, env []string, key, want string) {
	t.Helper()
	for _, item := range env {
		name, value, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(name, key) {
			if value != want {
				t.Errorf("%s = %q, want %q", key, value, want)
			}
			return
		}
	}
	t.Errorf("environment lacks %s", key)
}
