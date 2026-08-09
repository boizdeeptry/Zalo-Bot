package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/config"
	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

type appZaloHookRunner struct {
	mu          sync.Mutex
	legacyCalls int
	inputs      []appZaloSessionRunInput
	results     []appZaloRunResult
	errors      []error
	before      func(int, appZaloSessionRunInput)
	entered     chan struct{}
	release     chan struct{}
}

func (r *appZaloHookRunner) Run(_ context.Context, _ string, _ func(string)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.legacyCalls++
	return "legacy", nil
}

func (r *appZaloHookRunner) appRunZaloSession(
	_ context.Context,
	in appZaloSessionRunInput,
	_ func(string),
) (appZaloRunResult, error) {
	r.mu.Lock()
	call := len(r.inputs)
	r.inputs = append(r.inputs, in)
	var result appZaloRunResult
	if call < len(r.results) {
		result = r.results[call]
	}
	var err error
	if call < len(r.errors) {
		err = r.errors[call]
	}
	before := r.before
	entered := r.entered
	release := r.release
	r.mu.Unlock()

	if before != nil {
		before(call, in)
	}
	if entered != nil {
		entered <- struct{}{}
	}
	if release != nil {
		<-release
	}
	if result.SessionID == "" {
		result.SessionID = in.SessionID
	}
	return result, err
}

func (r *appZaloHookRunner) snapshot() ([]appZaloSessionRunInput, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.inputs), r.legacyCalls
}

type appZaloCommandHookRunner struct {
	runner appZaloSessionRunner
	legacy int
}

type appZaloDeadlineHookRunner struct {
	ctxErr chan error
}

func (r *appZaloDeadlineHookRunner) Run(_ context.Context, _ string, _ func(string)) (string, error) {
	return "legacy", nil
}

func (r *appZaloDeadlineHookRunner) appRunZaloSession(
	ctx context.Context,
	in appZaloSessionRunInput,
	_ func(string),
) (appZaloRunResult, error) {
	r.ctxErr <- ctx.Err()
	return appZaloRunResult{Answer: `{"answers":["second"]}`, SessionID: in.SessionID}, nil
}

func (r *appZaloCommandHookRunner) Run(_ context.Context, _ string, _ func(string)) (string, error) {
	r.legacy++
	return "legacy", nil
}

func (r *appZaloCommandHookRunner) appRunZaloSession(
	ctx context.Context,
	in appZaloSessionRunInput,
	step func(string),
) (appZaloRunResult, error) {
	return r.runner.RunSession(ctx, in, step)
}

func TestAppZaloCurrentMsgIDParsesOnlyTopLevelString(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: "valid", raw: json.RawMessage(`{"msgId":"m-123","cliMsgId":"c-1"}`), want: "m-123"},
		{name: "empty", raw: json.RawMessage(`{"msgId":""}`)},
		{name: "number", raw: json.RawMessage(`{"msgId":123}`)},
		{name: "nested", raw: json.RawMessage(`{"message":{"msgId":"nested"}}`)},
		{name: "wrong case", raw: json.RawMessage(`{"msgID":"wrong"}`)},
		{name: "invalid", raw: json.RawMessage(`{"msgId":`)},
		{name: "missing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := appZaloCurrentMsgID(tc.raw); got != tc.want {
				t.Errorf("appZaloCurrentMsgID(%s) = %q; want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestAppRunZaloLeavesStatelessRunnerUnchanged(t *testing.T) {
	a, _, zc := appZaloHookFixture(t, "stateless")
	run := &fakeZaloRunner{out: `{"answers":["ok"]}`}

	got, err := a.appRunZalo(
		context.Background(), run, zc, "stateless", "question", "msg-1", nil, nil, nil, func(string) {},
	)
	if err != nil {
		t.Fatalf("appRunZalo() error = %v", err)
	}
	if got != run.out || run.calls != 1 || !strings.Contains(run.gotPrompt, "You answer a customer's question") {
		t.Fatalf("stateless runner = output %q, calls %d, prompt %q", got, run.calls, run.gotPrompt)
	}
	if _, err := a.st.ZaloCLISession("stateless"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stateless runner created session mapping: %v", err)
	}
}

func TestAppAnswerZaloCreatesResumesAndSurvivesAPIRecreation(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread-a")
	if err := appZaloAddMessage(st, "thread-a", ipc.ZaloIn, "khách", "first question", "msg-1"); err != nil {
		t.Fatal(err)
	}
	command := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
		{stdout: appZaloHookResult(`{"answers":["first answer"]}`, 100)},
		{stdout: appZaloHookResult(`{"answers":["second answer"]}`, 200)},
		{stdout: appZaloHookResult(`{"answers":["third answer"]}`, 300)},
	}}
	run := &appZaloCommandHookRunner{runner: appZaloSessionRunner{cfg: zc, command: command.run}}
	deps := &zaloDeps{cfg: zc, run: run}
	if err := a.appAnswerZalo(deps, "thread-a", "first question", appZaloReply("msg-1"), nil); err != nil {
		t.Fatalf("first appAnswerZalo() error = %v", err)
	}
	first, err := st.ZaloCLISession("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.TurnCount != 1 || first.MessageCursor == 0 {
		t.Fatalf("first session = %+v; want completed turn and cursor", first)
	}
	if err := appZaloAddMessage(st, "thread-a", ipc.ZaloOut, ipc.ZaloAuthorOperator, "operator correction", "operator-1"); err != nil {
		t.Fatal(err)
	}
	if err := appZaloAddMessage(st, "thread-a", ipc.ZaloIn, "khách", "second question", "msg-2"); err != nil {
		t.Fatal(err)
	}
	if err := a.appAnswerZalo(deps, "thread-a", "second question", appZaloReply("msg-2"), nil); err != nil {
		t.Fatalf("second appAnswerZalo() error = %v", err)
	}

	recreated := appZaloAPIWithStore(a.cfg, st)
	if err := appZaloAddMessage(st, "thread-a", ipc.ZaloIn, "khách", "third question", "msg-3"); err != nil {
		t.Fatal(err)
	}
	thirdHighWater, err := st.LatestZaloMessageID("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := recreated.appAnswerZalo(deps, "thread-a", "third question", appZaloReply("msg-3"), nil); err != nil {
		t.Fatalf("restart appAnswerZalo() error = %v", err)
	}

	if len(command.calls) != 3 {
		t.Fatalf("Claude calls = %d; want 3", len(command.calls))
	}
	if !appZaloHasArgPair(command.calls[0].argv, "--session-id", first.ClaudeSessionID) {
		t.Errorf("first argv = %q; want --session-id %s", command.calls[0].argv, first.ClaudeSessionID)
	}
	for i := 1; i < 3; i++ {
		if !appZaloHasArgPair(command.calls[i].argv, "--resume", first.ClaudeSessionID) {
			t.Errorf("call %d argv = %q; want --resume %s", i+1, command.calls[i].argv, first.ClaudeSessionID)
		}
	}
	if !strings.Contains(command.calls[1].prompt, "operator correction") ||
		strings.Contains(command.calls[1].prompt, "You answer a customer's question") {
		t.Errorf("resume prompt did not contain only delta material: %q", command.calls[1].prompt)
	}
	if got := strings.Count(command.calls[1].prompt, "second question"); got != 1 {
		t.Errorf("current question occurrences = %d; want 1 from durable msgId match", got)
	}
	final, err := st.ZaloCLISession("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if final.ClaudeSessionID != first.ClaudeSessionID || final.TurnCount != 3 || final.MessageCursor != thirdHighWater {
		t.Fatalf("durable resumed session = %+v; consumed high-water = %d", final, thirdHighWater)
	}
	if run.legacy != 0 {
		t.Fatalf("production structured runner used legacy Run %d times", run.legacy)
	}
}

func TestAppAnswerZaloUsesDifferentSessionForSecondThread(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread-a", "thread-b")
	command := &appZaloRecordingCommand{responses: []appZaloCommandResponse{
		{stdout: appZaloHookResult(`{"answers":["a"]}`, 10)},
		{stdout: appZaloHookResult(`{"answers":["b"]}`, 10)},
	}}
	run := &appZaloCommandHookRunner{runner: appZaloSessionRunner{cfg: zc, command: command.run}}
	deps := &zaloDeps{cfg: zc, run: run}
	for _, threadID := range []string{"thread-a", "thread-b"} {
		if err := appZaloAddMessage(st, threadID, ipc.ZaloIn, "khách", threadID+" question", threadID+"-msg"); err != nil {
			t.Fatal(err)
		}
		if err := a.appAnswerZalo(deps, threadID, threadID+" question", appZaloReply(threadID+"-msg"), nil); err != nil {
			t.Fatal(err)
		}
	}
	one, _ := st.ZaloCLISession("thread-a")
	two, _ := st.ZaloCLISession("thread-b")
	if one.ClaudeSessionID == "" || two.ClaudeSessionID == "" || one.ClaudeSessionID == two.ClaudeSessionID {
		t.Fatalf("thread sessions = %q and %q; want distinct UUIDs", one.ClaudeSessionID, two.ClaudeSessionID)
	}
	if len(command.calls) != 2 {
		t.Fatalf("Claude calls = %d; want exactly one per thread", len(command.calls))
	}
}

func TestAppRunZaloRotatesAtConfiguredBoundariesWithoutExtraCall(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*store.ZaloCLISession, *zaloConfig)
		wantResume bool
	}{
		{name: "one token below context", mutate: func(s *store.ZaloCLISession, _ *zaloConfig) { s.ContextTokens = appZaloContextRotateTokens - 1 }, wantResume: true},
		{name: "context boundary", mutate: func(s *store.ZaloCLISession, _ *zaloConfig) { s.ContextTokens = appZaloContextRotateTokens }},
		{name: "turn boundary", mutate: func(s *store.ZaloCLISession, _ *zaloConfig) { s.TurnCount = appZaloMaxSessionTurns }},
		{name: "model changed", mutate: func(s *store.ZaloCLISession, zc *zaloConfig) { s.Model = "old-model"; zc.Model = "new-model" }},
		{name: "fingerprint changed", mutate: func(s *store.ZaloCLISession, _ *zaloConfig) { s.PromptFingerprint = "old-fingerprint" }},
		{name: "explicit flag", mutate: func(s *store.ZaloCLISession, _ *zaloConfig) { s.RotateBeforeNext = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, st, zc := appZaloHookFixture(t, "thread")
			session := store.ZaloCLISession{
				ThreadID:          "thread",
				ClaudeSessionID:   appZaloTestSessionID,
				Model:             zc.Model,
				PromptFingerprint: appZaloPromptFingerprint(zc, "thread"),
				MessageCursor:     7,
			}
			tc.mutate(&session, &zc)
			if _, err := st.CreateZaloCLISession(session); err != nil {
				t.Fatal(err)
			}
			run := &appZaloHookRunner{results: []appZaloRunResult{{Answer: "answer"}}}
			if _, err := a.appRunZalo(context.Background(), run, zc, "thread", "question", "", nil, nil, nil, func(string) {}); err != nil {
				t.Fatal(err)
			}
			inputs, legacy := run.snapshot()
			if len(inputs) != 1 || legacy != 0 {
				t.Fatalf("calls = %d structured, %d legacy; want 1, 0", len(inputs), legacy)
			}
			if inputs[0].Resume != tc.wantResume {
				t.Errorf("Resume = %v; want %v", inputs[0].Resume, tc.wantResume)
			}
			got, err := st.ZaloCLISession("thread")
			if err != nil {
				t.Fatal(err)
			}
			wantGeneration := int64(2)
			if tc.wantResume {
				wantGeneration = 1
			}
			if got.Generation != wantGeneration {
				t.Errorf("generation = %d; want %d", got.Generation, wantGeneration)
			}
			if !tc.wantResume && got.ClaudeSessionID == appZaloTestSessionID {
				t.Error("rotation reused the old Claude session ID")
			}
		})
	}
}

func TestAppAnswerZaloCommitsMeasuredAndEstimatedContextAfterPipeline(t *testing.T) {
	tests := []struct {
		name     string
		result   appZaloRunResult
		wantFunc func(appZaloSessionRunInput) int64
	}{
		{
			name:     "measured replaces estimate",
			result:   appZaloRunResult{Answer: `{"answers":["measured"]}`, ContextTokens: 4321, UsageMeasured: true, OutputBytes: 99999},
			wantFunc: func(appZaloSessionRunInput) int64 { return 4321 },
		},
		{
			name:   "missing usage estimates prompt and stream bytes",
			result: appZaloRunResult{Answer: `{"answers":["estimated"]}`, OutputBytes: 31},
			wantFunc: func(in appZaloSessionRunInput) int64 {
				return (int64(len(in.Prompt)) + 31 + 2) / 3
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, st, zc := appZaloHookFixture(t, "thread")
			if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "question", "msg-1"); err != nil {
				t.Fatal(err)
			}
			consumed, err := st.LatestZaloMessageID("thread")
			if err != nil {
				t.Fatal(err)
			}
			run := &appZaloHookRunner{results: []appZaloRunResult{tc.result}}
			if err := a.appAnswerZalo(&zaloDeps{cfg: zc, run: run}, "thread", "question", appZaloReply("msg-1"), nil); err != nil {
				t.Fatal(err)
			}
			inputs, _ := run.snapshot()
			got, err := st.ZaloCLISession("thread")
			if err != nil {
				t.Fatal(err)
			}
			if got.ContextTokens != tc.wantFunc(inputs[0]) || got.TurnCount != 1 || got.MessageCursor != consumed {
				t.Fatalf("completed session = %+v; consumed high-water = %d", got, consumed)
			}
		})
	}
}

func TestAppAnswerZaloRecoveryEstimatesTheSuccessfulBootstrapAttempt(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "short delta", "msg-1"); err != nil {
		t.Fatal(err)
	}
	appZaloCreateHookSession(t, st, zc, "thread", appZaloTestSessionID)
	run := &appZaloHookRunner{results: []appZaloRunResult{{
		Answer:            `{"answers":["recovered"]}`,
		SessionID:         appZaloTestRecoverySessionID,
		Recovered:         true,
		RecoveryAttempted: true,
		RecoveryCode:      appZaloErrorResumeNotFound,
		OutputBytes:       37,
	}}}
	if err := a.appAnswerZalo(
		&zaloDeps{cfg: zc, run: run}, "thread", "short delta", appZaloReply("msg-1"), nil,
	); err != nil {
		t.Fatal(err)
	}
	inputs, _ := run.snapshot()
	if len(inputs) != 1 || len(inputs[0].BootstrapPrompt) <= len(inputs[0].Prompt) {
		t.Fatalf("test requires bootstrap larger than delta: %+v", inputs)
	}
	got, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	want := (int64(len(inputs[0].BootstrapPrompt)) + 37 + 2) / 3
	if got.ContextTokens != want {
		t.Fatalf("recovered ContextTokens = %d; want bootstrap estimate %d (delta estimate %d)",
			got.ContextTokens, want, (int64(len(inputs[0].Prompt))+37+2)/3)
	}
}

func TestAppRunZaloRecoveryReplacesGenerationOnlyAfterVerifiedSuccess(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	appZaloCreateHookSession(t, st, zc, "thread", appZaloTestSessionID)
	run := &appZaloHookRunner{results: []appZaloRunResult{{
		Answer:            "recovered answer",
		SessionID:         appZaloTestRecoverySessionID,
		Recovered:         true,
		RecoveryAttempted: true,
		RecoveryCode:      appZaloErrorResumeNotFound,
	}}}

	if _, err := a.appRunZalo(context.Background(), run, zc, "thread", "question", "", nil, nil, nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 2 || got.ClaudeSessionID != appZaloTestRecoverySessionID || got.TurnCount != 0 || got.MessageCursor != 0 {
		t.Fatalf("recovered session = %+v", got)
	}
}

func TestAppRunZaloRecoveryPersistenceFailureMarksOldMappingForRotation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recovery.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertZaloThread("thread", "thread"); err != nil {
		t.Fatal(err)
	}
	a := appZaloAPIWithStore(config.Config{}, st)
	zc := appZaloHookConfig(t)
	appZaloCreateHookSession(t, st, zc, "thread", appZaloTestSessionID)

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TRIGGER app_test_fail_session_replace
BEFORE UPDATE OF claude_session_id ON app_zalo_cli_sessions
WHEN NEW.claude_session_id <> OLD.claude_session_id
BEGIN
  SELECT RAISE(ABORT, 'injected replacement failure');
END`); err != nil {
		t.Fatal(err)
	}

	run := &appZaloHookRunner{results: []appZaloRunResult{{
		Answer:            "recovered answer",
		SessionID:         appZaloTestRecoverySessionID,
		Recovered:         true,
		RecoveryAttempted: true,
		RecoveryCode:      appZaloErrorResumeNotFound,
	}}}
	if _, err := a.appRunZalo(
		context.Background(), run, zc, "thread", "question", "", nil, nil, nil, func(string) {},
	); err != nil {
		t.Fatal(err)
	}
	got, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 1 || got.ClaudeSessionID != appZaloTestSessionID ||
		!got.RotateBeforeNext || got.LastError != appZaloErrorStateUpdate {
		t.Fatalf("recovery persistence failure left unsafe mapping: %+v", got)
	}
}

func TestAppRunZaloInvalidatesFailedRecoveryAndResumeUnusableForNextTurn(t *testing.T) {
	tests := []struct {
		name   string
		result appZaloRunResult
		code   string
	}{
		{name: "failed missing recovery", result: appZaloRunResult{RecoveryAttempted: true, RecoveryCode: appZaloErrorResumeNotFound}, code: appZaloErrorResumeNotFound},
		{name: "resume unusable", result: appZaloRunResult{RecoveryCode: appZaloErrorResumeUnusable}, code: appZaloErrorResumeUnusable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, st, zc := appZaloHookFixture(t, "thread")
			appZaloCreateHookSession(t, st, zc, "thread", appZaloTestSessionID)
			run := &appZaloHookRunner{
				results: []appZaloRunResult{tc.result},
				errors:  []error{appZaloNewRunError(tc.code, nil)},
			}
			if _, err := a.appRunZalo(context.Background(), run, zc, "thread", "question", "", nil, nil, nil, func(string) {}); appZaloRunErrorCode(err) != tc.code {
				t.Fatalf("appRunZalo() error = %v; want %s", err, tc.code)
			}
			failed, _ := st.ZaloCLISession("thread")
			if !failed.RotateBeforeNext || failed.LastError != tc.code || failed.Generation != 1 {
				t.Fatalf("failed mapping = %+v", failed)
			}
			if inputs, _ := run.snapshot(); len(inputs) != 1 {
				t.Fatalf("same-turn calls = %d; want 1", len(inputs))
			}

			next := &appZaloHookRunner{results: []appZaloRunResult{{Answer: "next answer"}}}
			if _, err := a.appRunZalo(context.Background(), next, zc, "thread", "next", "", nil, nil, nil, func(string) {}); err != nil {
				t.Fatal(err)
			}
			inputs, _ := next.snapshot()
			if len(inputs) != 1 || inputs[0].Resume || inputs[0].SessionID == appZaloTestSessionID {
				t.Fatalf("next-turn input = %+v; want fresh bootstrap generation", inputs)
			}
			rotated, _ := st.ZaloCLISession("thread")
			if rotated.Generation != 2 || rotated.ClaudeSessionID != inputs[0].SessionID {
				t.Fatalf("rotated mapping = %+v, input = %+v", rotated, inputs[0])
			}
		})
	}
}

func TestAppAnswerZaloRunnerFailureEscalatesWithoutAdvancingCursor(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "question", "msg-1"); err != nil {
		t.Fatal(err)
	}
	run := &appZaloHookRunner{errors: []error{appZaloNewRunError(appZaloErrorNetwork, nil)}}
	if err := a.appAnswerZalo(&zaloDeps{cfg: zc, run: run}, "thread", "question", appZaloReply("msg-1"), nil); err != nil {
		t.Fatalf("normal escalation returned error: %v", err)
	}
	got, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := st.LatestZaloMessageID("thread")
	if latest <= 1 {
		t.Fatalf("runner failure did not take existing handoff pipeline; latest = %d", latest)
	}
	if got.TurnCount != 0 || got.MessageCursor != 0 {
		t.Fatalf("runner failure advanced session = %+v", got)
	}
}

func TestAppAnswerZaloSuccessfulRunnerCompletesNormalValidationEscalation(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "question", "msg-1"); err != nil {
		t.Fatal(err)
	}
	consumed, err := st.LatestZaloMessageID("thread")
	if err != nil {
		t.Fatal(err)
	}
	run := &appZaloHookRunner{results: []appZaloRunResult{{
		Answer: "not valid JSON", ContextTokens: 88, UsageMeasured: true,
	}}}
	if err := a.appAnswerZalo(&zaloDeps{cfg: zc, run: run}, "thread", "question", appZaloReply("msg-1"), nil); err != nil {
		t.Fatalf("normal validation escalation returned error: %v", err)
	}
	got, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	if got.TurnCount != 1 || got.ContextTokens != 88 || got.MessageCursor != consumed {
		t.Fatalf("successful Claude turn used wrong consumed high-water: session %+v, want %d", got, consumed)
	}
}

func TestAppAnswerZaloDoesNotConsumeInboundInsertedWhileClaudeRuns(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "first question", "msg-1"); err != nil {
		t.Fatal(err)
	}
	beforeRun, err := st.LatestZaloMessageID("thread")
	if err != nil {
		t.Fatal(err)
	}
	run := &appZaloHookRunner{
		results: []appZaloRunResult{
			{Answer: `{"answers":["first answer"]}`},
			{Answer: `{"answers":["second answer"]}`},
		},
		before: func(call int, _ appZaloSessionRunInput) {
			if call != 0 {
				return
			}
			if addErr := appZaloAddMessage(
				st, "thread", ipc.ZaloIn, "khách", "arrived while Claude was running", "msg-2",
			); addErr != nil {
				t.Fatalf("insert concurrent inbound: %v", addErr)
			}
		},
	}
	deps := &zaloDeps{cfg: zc, run: run}
	if err := a.appAnswerZalo(deps, "thread", "first question", appZaloReply("msg-1"), nil); err != nil {
		t.Fatal(err)
	}
	first, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	if first.MessageCursor != beforeRun {
		t.Fatalf("cursor after interleaved inbound = %d; want pre-run high-water %d", first.MessageCursor, beforeRun)
	}

	if err := a.appAnswerZalo(deps, "thread", "follow up", ipc.ZaloOutboxDraft{}, nil); err != nil {
		t.Fatal(err)
	}
	inputs, _ := run.snapshot()
	if len(inputs) != 2 || !strings.Contains(inputs[1].Prompt, "arrived while Claude was running") {
		t.Fatalf("next delta did not retain interleaved inbound: %+v", inputs)
	}
}

func TestAppRunZaloBootstrapCursorComesFromSuppliedHistoryNotNewerStoreState(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "included in bootstrap", "msg-1"); err != nil {
		t.Fatal(err)
	}
	history, err := st.ZaloMessages("thread", zaloHistoryTurns)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID == 0 {
		t.Fatalf("bootstrap history = %+v", history)
	}
	if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "not in bootstrap", "msg-2"); err != nil {
		t.Fatal(err)
	}
	carrier := &appZaloCompletionCarrier{}
	ctx := context.WithValue(context.Background(), appZaloCompletionContextKey{}, carrier)
	run := &appZaloHookRunner{results: []appZaloRunResult{{Answer: "answer"}}}
	if _, err := a.appRunZalo(
		ctx, run, zc, "thread", "included in bootstrap", "msg-1", history, nil, nil, func(string) {},
	); err != nil {
		t.Fatal(err)
	}
	pending, ok := carrier.take()
	if !ok {
		t.Fatal("successful structured run did not publish pending completion")
	}
	if pending.messageCursor != history[0].ID {
		t.Fatalf("bootstrap completion cursor = %d; want supplied history high-water %d",
			pending.messageCursor, history[0].ID)
	}
}

func TestAppAnswerZaloResumeLeavesRowsBeyondFirstDeltaPagePending(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	for i := 1; i <= 105; i++ {
		body := fmt.Sprintf("message-%03d", i)
		if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", body, body); err != nil {
			t.Fatal(err)
		}
	}
	appZaloCreateHookSession(t, st, zc, "thread", appZaloTestSessionID)
	page, err := st.ZaloMessagesAfter("thread", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 100 {
		t.Fatalf("first delta page rows = %d; want 100", len(page))
	}
	run := &appZaloHookRunner{results: []appZaloRunResult{
		{Answer: `{"answers":["first"]}`},
		{Answer: `{"answers":["second"]}`},
	}}
	deps := &zaloDeps{cfg: zc, run: run}
	if err := a.appAnswerZalo(deps, "thread", "message-105", appZaloReply("message-105"), nil); err != nil {
		t.Fatal(err)
	}
	first, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	if first.MessageCursor != page[len(page)-1].ID {
		t.Fatalf("first resume cursor = %d; want fetched page tail %d",
			first.MessageCursor, page[len(page)-1].ID)
	}
	if err := a.appAnswerZalo(deps, "thread", "follow up", ipc.ZaloOutboxDraft{}, nil); err != nil {
		t.Fatal(err)
	}
	inputs, _ := run.snapshot()
	if len(inputs) != 2 || !strings.Contains(inputs[1].Prompt, "message-105") {
		t.Fatalf("second delta lost rows beyond first page: %+v", inputs)
	}
}

func TestAppAnswerZaloStoreFailureDoesNotAdvancePendingCompletion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "hook.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertZaloThread("thread", "thread"); err != nil {
		t.Fatal(err)
	}
	if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "question", "msg-1"); err != nil {
		t.Fatal(err)
	}
	a := appZaloAPIWithStore(config.Config{}, st)
	zc := appZaloHookConfig(t)
	run := &appZaloHookRunner{
		results: []appZaloRunResult{{Answer: `{"answers":["answer"]}`, ContextTokens: 99, UsageMeasured: true}},
		before: func(_ int, _ appZaloSessionRunInput) {
			if closeErr := st.Close(); closeErr != nil {
				t.Errorf("close store: %v", closeErr)
			}
		},
	}
	if err := a.appAnswerZalo(&zaloDeps{cfg: zc, run: run}, "thread", "question", appZaloReply("msg-1"), nil); err == nil {
		t.Fatal("appAnswerZalo() error = nil after store was closed")
	}

	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	if got.TurnCount != 0 || got.MessageCursor != 0 || got.ContextTokens != 0 {
		t.Fatalf("failed answer/store pipeline advanced session = %+v", got)
	}
}

func TestAppAnswerZaloGateSerializesSameStoreAndThreadAcrossAPIs(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	otherAPI := appZaloAPIWithStore(a.cfg, st)
	run := &appZaloHookRunner{
		results: []appZaloRunResult{
			{Answer: `{"answers":["first"]}`},
			{Answer: `{"answers":["second"]}`},
		},
		entered: make(chan struct{}, 2),
		release: make(chan struct{}, 2),
	}
	deps := &zaloDeps{cfg: zc, run: run}
	errs := make(chan error, 2)
	go func() { errs <- a.appAnswerZalo(deps, "thread", "first", ipc.ZaloOutboxDraft{}, nil) }()
	appZaloAwaitHookEntry(t, run.entered)
	go func() { errs <- otherAPI.appAnswerZalo(deps, "thread", "second", ipc.ZaloOutboxDraft{}, nil) }()
	select {
	case <-run.entered:
		t.Fatal("same store/thread entered the structured runner concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	run.release <- struct{}{}
	appZaloAwaitHookEntry(t, run.entered)
	run.release <- struct{}{}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	inputs, _ := run.snapshot()
	if len(inputs) != 2 || inputs[0].Resume || !inputs[1].Resume || inputs[0].SessionID != inputs[1].SessionID {
		t.Fatalf("serialized inputs = %+v", inputs)
	}
}

func TestAppAnswerZaloGateAllowsDifferentThreadsConcurrently(t *testing.T) {
	a, _, zc := appZaloHookFixture(t, "thread-a", "thread-b")
	run := &appZaloHookRunner{
		results: []appZaloRunResult{
			{Answer: `{"answers":["a"]}`},
			{Answer: `{"answers":["b"]}`},
		},
		entered: make(chan struct{}, 2),
		release: make(chan struct{}, 2),
	}
	deps := &zaloDeps{cfg: zc, run: run}
	errs := make(chan error, 2)
	go func() { errs <- a.appAnswerZalo(deps, "thread-a", "a", ipc.ZaloOutboxDraft{}, nil) }()
	go func() { errs <- a.appAnswerZalo(deps, "thread-b", "b", ipc.ZaloOutboxDraft{}, nil) }()
	appZaloAwaitHookEntry(t, run.entered)
	appZaloAwaitHookEntry(t, run.entered)
	run.release <- struct{}{}
	run.release <- struct{}{}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppAnswerZaloStartsTurnTimeoutAfterGateAcquisition(t *testing.T) {
	a, st, firstConfig := appZaloHookFixture(t, "thread")
	otherAPI := appZaloAPIWithStore(a.cfg, st)
	first := &appZaloHookRunner{
		results: []appZaloRunResult{{Answer: `{"answers":["first"]}`}},
		entered: make(chan struct{}, 1),
		release: make(chan struct{}, 1),
	}
	firstConfig.Timeout = time.Second
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- a.appAnswerZalo(
			&zaloDeps{cfg: firstConfig, run: first}, "thread", "first", ipc.ZaloOutboxDraft{}, nil,
		)
	}()
	appZaloAwaitHookEntry(t, first.entered)

	secondConfig := firstConfig
	secondConfig.Timeout = 10 * time.Millisecond
	second := &appZaloDeadlineHookRunner{ctxErr: make(chan error, 1)}
	secondErr := make(chan error, 1)
	go func() {
		secondErr <- otherAPI.appAnswerZalo(
			&zaloDeps{cfg: secondConfig, run: second}, "thread", "second", ipc.ZaloOutboxDraft{}, nil,
		)
	}()
	select {
	case err := <-second.ctxErr:
		t.Fatalf("second turn entered while the first still held the gate (ctx error %v)", err)
	case <-time.After(30 * time.Millisecond):
	}
	first.release <- struct{}{}
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-second.ctxErr:
		if err != nil {
			t.Fatalf("second turn context was already expired on gate entry: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second turn did not enter after gate release")
	}
	if err := <-secondErr; err != nil {
		t.Fatal(err)
	}
}

func TestAppRunZaloCASConflictReloadsWithoutOverwritingWinner(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	old := appZaloCreateHookSession(t, st, zc, "thread", appZaloTestSessionID)
	winnerID := "33333333-3333-4333-8333-333333333333"
	run := &appZaloHookRunner{
		results: []appZaloRunResult{{
			Answer:            "recovered answer",
			SessionID:         appZaloTestRecoverySessionID,
			Recovered:         true,
			RecoveryAttempted: true,
			RecoveryCode:      appZaloErrorResumeNotFound,
		}},
		before: func(_ int, _ appZaloSessionRunInput) {
			next := old
			next.ClaudeSessionID = winnerID
			if _, err := st.ReplaceZaloCLISession(old.Generation, next); err != nil {
				t.Fatalf("install concurrent winner: %v", err)
			}
		},
	}
	if _, err := a.appRunZalo(context.Background(), run, zc, "thread", "question", "", nil, nil, nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 2 || got.ClaudeSessionID != winnerID {
		t.Fatalf("CAS conflict overwrote winner: %+v", got)
	}
}

func TestAppReloadZaloSessionAfterConflictRejectsKnownStaleWinner(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*store.ZaloCLISession)
		wantError bool
	}{
		{name: "valid winner resumes"},
		{
			name: "winner still requests rotation",
			mutate: func(session *store.ZaloCLISession) {
				session.RotateBeforeNext = true
			},
			wantError: true,
		},
		{
			name: "winner has stale model",
			mutate: func(session *store.ZaloCLISession) {
				session.Model = "old-model"
			},
			wantError: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, st, zc := appZaloHookFixture(t, "thread")
			session := store.ZaloCLISession{
				ThreadID: "thread", ClaudeSessionID: appZaloTestSessionID, Model: zc.Model,
				PromptFingerprint: appZaloPromptFingerprint(zc, "thread"),
			}
			if tc.mutate != nil {
				tc.mutate(&session)
			}
			if _, err := st.CreateZaloCLISession(session); err != nil {
				t.Fatal(err)
			}
			got, err := a.appReloadZaloSessionAfterConflict(zc, "thread", session.PromptFingerprint)
			if tc.wantError {
				if err == nil || err.Error() != appZaloErrorStateUpdate {
					t.Fatalf("reload stale winner error = %v; want safe %q", err, appZaloErrorStateUpdate)
				}
				return
			}
			if err != nil || got.ClaudeSessionID != appZaloTestSessionID {
				t.Fatalf("reload valid winner = %+v, %v", got, err)
			}
		})
	}
}

func TestAppAnswerZaloCompletionCASConflictDoesNotAdvanceAnotherGeneration(t *testing.T) {
	a, st, zc := appZaloHookFixture(t, "thread")
	if err := appZaloAddMessage(st, "thread", ipc.ZaloIn, "khách", "question", "msg-1"); err != nil {
		t.Fatal(err)
	}
	old := appZaloCreateHookSession(t, st, zc, "thread", appZaloTestSessionID)
	winnerID := "44444444-4444-4444-8444-444444444444"
	run := &appZaloHookRunner{
		results: []appZaloRunResult{{Answer: `{"answers":["answer"]}`, ContextTokens: 55, UsageMeasured: true}},
		before: func(_ int, _ appZaloSessionRunInput) {
			next := old
			next.ClaudeSessionID = winnerID
			next.ContextTokens = 0
			next.TurnCount = 0
			next.MessageCursor = 0
			if _, err := st.ReplaceZaloCLISession(old.Generation, next); err != nil {
				t.Fatalf("install completion winner: %v", err)
			}
		},
	}
	if err := a.appAnswerZalo(&zaloDeps{cfg: zc, run: run}, "thread", "question", appZaloReply("msg-1"), nil); err != nil {
		t.Fatal(err)
	}
	got, err := st.ZaloCLISession("thread")
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != 2 || got.ClaudeSessionID != winnerID ||
		got.TurnCount != 0 || got.MessageCursor != 0 || got.ContextTokens != 0 {
		t.Fatalf("completion CAS conflict mutated another generation: %+v", got)
	}
}

func appZaloHookFixture(t *testing.T, threadIDs ...string) (*api, *store.Store, zaloConfig) {
	t.Helper()
	reg, st, cfg := newTestRegistry(t)
	for _, threadID := range threadIDs {
		if err := st.UpsertZaloThread(threadID, threadID); err != nil {
			t.Fatal(err)
		}
	}
	a := &api{
		cfg: cfg, st: st, reg: reg,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		portal: newPortalStore(),
	}
	return a, st, appZaloHookConfig(t)
}

func appZaloHookConfig(t *testing.T) zaloConfig {
	t.Helper()
	return zaloConfig{
		KBRoots: []string{t.TempDir()}, WorkDir: t.TempDir(), Timeout: 2 * time.Second,
		Model: "sonnet", CiteMode: zaloCiteSoft,
	}
}

func appZaloAPIWithStore(cfg config.Config, st *store.Store) *api {
	return &api{
		cfg: cfg, st: st,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		portal: newPortalStore(),
	}
}

func appZaloAddMessage(st *store.Store, threadID, direction, author, body, msgID string) error {
	return st.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: threadID, Direction: direction, Author: author, Body: body,
		ZaloMsgID: msgID, CreatedAt: time.Now(),
	})
}

func appZaloReply(msgID string) ipc.ZaloOutboxDraft {
	return ipc.ZaloOutboxDraft{ReplyQuote: json.RawMessage(`{"msgId":"` + msgID + `","cliMsgId":"cli"}`)}
}

func appZaloHookResult(answer string, contextTokens int64) string {
	event := struct {
		Type   string           `json:"type"`
		Result string           `json:"result"`
		Usage  map[string]int64 `json:"usage,omitempty"`
	}{Type: "result", Result: answer}
	if contextTokens > 0 {
		event.Usage = map[string]int64{"input_tokens": contextTokens}
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		panic(err)
	}
	return string(encoded) + "\n"
}

func appZaloHasArgPair(argv []string, key, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == key && argv[i+1] == value {
			return true
		}
	}
	return false
}

func appZaloCreateHookSession(
	t *testing.T,
	st *store.Store,
	zc zaloConfig,
	threadID, sessionID string,
) store.ZaloCLISession {
	t.Helper()
	session, err := st.CreateZaloCLISession(store.ZaloCLISession{
		ThreadID: threadID, ClaudeSessionID: sessionID, Model: zc.Model,
		PromptFingerprint: appZaloPromptFingerprint(zc, threadID),
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func appZaloAwaitHookEntry(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("structured runner did not enter")
	}
}
