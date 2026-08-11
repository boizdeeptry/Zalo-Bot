package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/store"
)

func TestAppOnboardingTestChatPersistsHashOnlyReceipt(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	state, persona := seedAppOnboardingTestChat(t, env, "codex", 41)
	const message = "Hãy giới thiệu bạn với tôi"
	const answer = "Xin chào, tôi là BÉ\n\tMI và rất vui được gặp bạn."
	fixedNow := time.Date(2026, 8, 11, 8, 0, 0, 123456789, time.UTC)
	randomBytes := make([]byte, 32)
	for i := range randomBytes {
		randomBytes[i] = byte(i)
	}

	var gotState store.OnboardingState
	var gotAccount store.OnboardingStagingAccount
	var gotPrompt string
	var logs syncLogBuffer
	env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	withAppOnboardingTestSeams(t,
		func(ctx context.Context, _ *api, state store.OnboardingState, account store.OnboardingStagingAccount, prompt string) (string, error) {
			gotState, gotAccount, gotPrompt = state, account, prompt
			return answer, nil
		},
		func() time.Time { return fixedNow },
		bytes.NewReader(randomBytes),
		120*time.Second,
	)
	beforeSideEffects := appOnboardingTestSideEffectDigest(t, env)

	rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":41,"message":"  Hãy giới thiệu bạn với tôi  "}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingTestChatResponse](t, rr)
	wantToken := base64.RawURLEncoding.EncodeToString(randomBytes)
	wantExpiry := fixedNow.Add(10 * time.Minute).Format(time.RFC3339Nano)
	if got.Answer != answer || got.BotName != "Bé Mi" || got.ProviderID != "codex" ||
		got.ModelID != "gpt-5.6-terra" || got.TestToken != wantToken ||
		got.ExpiresAt != wantExpiry || got.Revision != 42 {
		t.Fatalf("test-chat response = %+v", got)
	}
	if len(got.TestToken) != 43 || strings.ContainsAny(got.TestToken, "+/=") {
		t.Fatalf("test token is not 256-bit Base64URL-no-padding: %q", got.TestToken)
	}
	if gotState != state || gotAccount.AccountID != "staged-account" ||
		gotAccount.ConfigDir != "D:/onboarding/staged-account" {
		t.Fatalf("executor staging = state %+v account %+v", gotState, gotAccount)
	}
	if !strings.Contains(gotPrompt, persona) || !strings.Contains(gotPrompt, `"display_name":"Bé Mi"`) ||
		!strings.Contains(gotPrompt, message) || !strings.Contains(strings.ToLower(gotPrompt), "tiếng việt") {
		t.Fatalf("prompt lacks full persona, structured identity, message, or Vietnamese request: %q", gotPrompt)
	}
	if strings.Contains(logs.String(), persona) || strings.Contains(logs.String(), message) || strings.Contains(logs.String(), answer) {
		t.Fatalf("test-chat content leaked to logs: %q", logs.String())
	}

	after := env.state(t)
	wantHashBytes := sha256.Sum256([]byte(wantToken))
	wantHash := hex.EncodeToString(wantHashBytes[:])
	if after.Revision != state.Revision+1 || after.TestNonceHash != wantHash ||
		after.TestExpiresAt != wantExpiry || after.PersonaFingerprint != state.PersonaFingerprint {
		t.Fatalf("persisted receipt = %+v; want hash %q", after, wantHash)
	}
	serializedState, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{wantToken, message, answer, "Bé Mi"} {
		if strings.Contains(string(serializedState), forbidden) {
			t.Fatalf("persisted state contains plaintext %q: %s", forbidden, serializedState)
		}
	}
	if afterSideEffects := appOnboardingTestSideEffectDigest(t, env); afterSideEffects != beforeSideEffects {
		t.Fatalf("test chat changed production side effects:\nbefore=%q\nafter=%q", beforeSideEffects, afterSideEffects)
	}
}

func TestAppOnboardingTestChatStrictMessageValidation(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty", body: []byte(`{"revision":2,"message":"  "}`)},
		{name: "too long unicode", body: []byte(`{"revision":2,"message":"` + strings.Repeat("ạ", 501) + `"}`)},
		{name: "control", body: []byte(`{"revision":2,"message":"xin\u0000chao"}`)},
		{name: "newline", body: []byte(`{"revision":2,"message":"xin\nchao"}`)},
		{name: "invalid utf8", body: append([]byte(`{"revision":2,"message":"`), append([]byte{0xff}, []byte(`"}`)...)...)},
		{name: "unknown field", body: []byte(`{"revision":2,"message":"xin chao","extra":true}`)},
		{name: "trailing json", body: []byte(`{"revision":2,"message":"xin chao"}{}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			before := env.state(t)
			calls := 0
			withAppOnboardingTestSeams(t,
				func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
					calls++
					return "unexpected", nil
				},
				time.Now,
				bytes.NewReader(make([]byte, 32)),
				120*time.Second,
			)
			req := httptest.NewRequest(http.MethodPost, "/onboarding/test-chat", bytes.NewReader(tt.body))
			rr := env.serveRequest(req)
			wantCode := "ONBOARDING_TEST_MESSAGE_INVALID"
			if tt.name == "unknown field" || tt.name == "trailing json" || tt.name == "invalid utf8" {
				wantCode = "ONBOARDING_REQUEST_INVALID"
			}
			requireOnboardingCode(t, rr, http.StatusBadRequest, wantCode)
			if calls != 0 {
				t.Fatalf("invalid request called runner %d times", calls)
			}
			if after := env.state(t); after != before {
				t.Fatalf("invalid request changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAppOnboardingTestChatRejectsInvalidStagingBeforeRunner(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*onboardingRouteTestEnv, *store.OnboardingState)
		body     string
		wantCode string
	}{
		{name: "stale revision", body: `{"revision":40,"message":"xin chào"}`, wantCode: "ONBOARDING_REVISION_CONFLICT"},
		{name: "wrong phase", body: `{"revision":41,"message":"xin chào"}`, wantCode: "ONBOARDING_PHASE_INVALID", mutate: func(_ *onboardingRouteTestEnv, state *store.OnboardingState) {
			state.Phase = store.OnboardingPhasePersona
		}},
		{name: "fingerprint changed", body: `{"revision":41,"message":"xin chào"}`, wantCode: "ONBOARDING_PERSONA_CHANGED", mutate: func(env *onboardingRouteTestEnv, _ *store.OnboardingState) {
			_ = os.WriteFile(env.a.zalo.cfg.PersonaPath, []byte("persona changed"), 0o600)
		}},
		{name: "wrong provider id", body: `{"revision":41,"message":"xin chào"}`, wantCode: "ONBOARDING_STAGING_INVALID", mutate: func(_ *onboardingRouteTestEnv, state *store.OnboardingState) { state.ProviderID = "other" }},
		{name: "enabled account", body: `{"revision":41,"message":"xin chào"}`, wantCode: "ONBOARDING_STAGING_INVALID", mutate: func(env *onboardingRouteTestEnv, _ *store.OnboardingState) {
			_, _ = env.db.Exec(`UPDATE llm_accounts SET enabled = 1 WHERE id = 'staged-account'`)
		}},
		{name: "unavailable model", body: `{"revision":41,"message":"xin chào"}`, wantCode: "ONBOARDING_NO_MODEL", mutate: func(env *onboardingRouteTestEnv, _ *store.OnboardingState) {
			_, _ = env.db.Exec(`UPDATE llm_models SET available = 0 WHERE provider_id = 'codex'`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			state, _ := seedAppOnboardingTestChat(t, env, "codex", 41)
			if tt.mutate != nil {
				tt.mutate(env, &state)
				env.setState(t, state)
			}
			before := env.state(t)
			calls := 0
			withAppOnboardingTestSeams(t,
				func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
					calls++
					return "unexpected", nil
				},
				time.Now,
				bytes.NewReader(make([]byte, 32)),
				120*time.Second,
			)
			rr := env.serve(http.MethodPost, "/onboarding/test-chat", tt.body)
			wantStatus := http.StatusConflict
			if tt.wantCode == "ONBOARDING_NO_MODEL" {
				wantStatus = http.StatusUnprocessableEntity
			}
			requireOnboardingCode(t, rr, wantStatus, tt.wantCode)
			if calls != 0 {
				t.Fatalf("invalid staging called runner %d times", calls)
			}
			if after := env.state(t); after != before {
				t.Fatalf("invalid staging changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAppOnboardingTestChatRejectsEmptyOrMissingNameAnswer(t *testing.T) {
	tests := []struct {
		name, answer string
	}{
		{name: "empty", answer: " \n\t "},
		{name: "missing name", answer: "Xin chào, tôi là trợ lý của bạn."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingTestChat(t, env, "codex", 9)
			before := env.state(t)
			withAppOnboardingTestSeams(t,
				func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
					return tt.answer, nil
				},
				time.Now,
				bytes.NewReader(make([]byte, 32)),
				120*time.Second,
			)
			rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":9,"message":"xin chào"}`)
			requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, "ONBOARDING_TEST_ANSWER_INVALID")
			if after := env.state(t); after != before {
				t.Fatalf("invalid answer changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAppOnboardingTestChatBusySkipsSecondRunner(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingTestChat(t, env, "codex", 12)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls int
	var callsMu sync.Mutex
	withAppOnboardingTestSeams(t,
		func(ctx context.Context, _ *api, _ store.OnboardingState, _ store.OnboardingStagingAccount, _ string) (string, error) {
			callsMu.Lock()
			calls++
			call := calls
			callsMu.Unlock()
			if call > 1 {
				return "unexpected second runner", nil
			}
			close(entered)
			select {
			case <-release:
				return "Xin chào, tôi là Bé Mi.", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		time.Now,
		bytes.NewReader(make([]byte, 32)),
		120*time.Second,
	)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":12,"message":"xin chào"}`)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first Test Chat did not enter runner")
	}
	second := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":12,"message":"xin chào lần hai"}`)
	requireOnboardingCode(t, second, http.StatusConflict, "ONBOARDING_TEST_BUSY")
	close(release)
	select {
	case first := <-firstDone:
		if first.Code != http.StatusOK {
			t.Fatalf("first Test Chat status=%d body=%s", first.Code, first.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("first Test Chat did not finish")
	}
	callsMu.Lock()
	defer callsMu.Unlock()
	if calls != 1 {
		t.Fatalf("runner calls = %d; want exactly 1", calls)
	}
}

func TestAppOnboardingTestChatCancellationAndTimeoutDoNotPersist(t *testing.T) {
	tests := []struct {
		name       string
		timeout    time.Duration
		cancel     bool
		wantStatus int
		wantCode   string
	}{
		{name: "client cancel", timeout: time.Second, cancel: true, wantStatus: http.StatusRequestTimeout, wantCode: "ONBOARDING_REQUEST_CANCELED"},
		{name: "hard timeout", timeout: 10 * time.Millisecond, wantStatus: http.StatusGatewayTimeout, wantCode: "ONBOARDING_TEST_TIMEOUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingTestChat(t, env, "codex", 15)
			before := env.state(t)
			childEntered := make(chan struct{})
			childCanceled := make(chan error, 1)
			withAppOnboardingTestSeams(t,
				func(ctx context.Context, _ *api, _ store.OnboardingState, _ store.OnboardingStagingAccount, _ string) (string, error) {
					close(childEntered)
					<-ctx.Done()
					childCanceled <- ctx.Err()
					return "", ctx.Err()
				},
				time.Now,
				bytes.NewReader(make([]byte, 32)),
				tt.timeout,
			)
			ctx := context.Background()
			var cancel context.CancelFunc
			if tt.cancel {
				ctx, cancel = context.WithCancel(ctx)
			}
			req := httptest.NewRequest(http.MethodPost, "/onboarding/test-chat", strings.NewReader(`{"revision":15,"message":"xin chào"}`)).WithContext(ctx)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- env.serveRequest(req) }()
			select {
			case <-childEntered:
			case <-time.After(time.Second):
				t.Fatal("runner child did not start")
			}
			if cancel != nil {
				cancel()
			}
			var rr *httptest.ResponseRecorder
			select {
			case rr = <-done:
			case <-time.After(time.Second):
				t.Fatal("canceled handler did not return")
			}
			requireOnboardingCode(t, rr, tt.wantStatus, tt.wantCode)
			select {
			case <-childCanceled:
			case <-time.After(time.Second):
				t.Fatal("runner child did not observe cancellation")
			}
			if after := env.state(t); after != before {
				t.Fatalf("canceled test changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAppOnboardingTestChatRejectsLateSuccessAfterHardTimeout(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingTestChat(t, env, "codex", 16)
	before := env.state(t)
	withAppOnboardingTestSeams(t,
		func(ctx context.Context, _ *api, _ store.OnboardingState, _ store.OnboardingStagingAccount, _ string) (string, error) {
			<-ctx.Done()
			return "Xin chào, tôi là Bé Mi.", nil
		},
		time.Now,
		bytes.NewReader(make([]byte, 32)),
		10*time.Millisecond,
	)

	rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":16,"message":"xin chào"}`)
	requireOnboardingCode(t, rr, http.StatusGatewayTimeout, "ONBOARDING_TEST_TIMEOUT")
	if after := env.state(t); after != before {
		t.Fatalf("late success changed state: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingTestChatRunnerFailureIsSafeAndDoesNotPersist(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingTestChat(t, env, "codex", 18)
	before := env.state(t)
	withAppOnboardingTestSeams(t,
		func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
			return "", errors.New("RUNNER_SECRET prompt and account path")
		},
		time.Now,
		bytes.NewReader(make([]byte, 32)),
		120*time.Second,
	)

	rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":18,"message":"xin chào"}`)
	requireOnboardingCode(t, rr, http.StatusBadGateway, "ONBOARDING_TEST_FAILED")
	if strings.Contains(rr.Body.String(), "RUNNER_SECRET") {
		t.Fatalf("runner failure leaked internal details: %s", rr.Body.String())
	}
	if after := env.state(t); after != before {
		t.Fatalf("runner failure changed state: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingTestChatRandomAndStoreFailuresDoNotPersist(t *testing.T) {
	tests := []struct {
		name      string
		random    io.Reader
		failStore bool
		wantCode  string
	}{
		{name: "random", random: errorReader{err: errors.New("RANDOM_SECRET")}, wantCode: "ONBOARDING_TEST_TOKEN_FAILED"},
		{name: "store", random: bytes.NewReader(make([]byte, 32)), failStore: true, wantCode: "ONBOARDING_STATE_UNAVAILABLE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingTestChat(t, env, "codex", 23)
			before := env.state(t)
			if tt.failStore {
				if _, err := env.db.Exec(`CREATE TRIGGER fail_test_chat_receipt
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'STORE_SECRET'); END`); err != nil {
					t.Fatal(err)
				}
			}
			withAppOnboardingTestSeams(t,
				func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
					return "Xin chào, tôi là Bé Mi.", nil
				},
				time.Now,
				tt.random,
				120*time.Second,
			)
			rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":23,"message":"xin chào"}`)
			requireOnboardingCode(t, rr, http.StatusInternalServerError, tt.wantCode)
			if strings.Contains(rr.Body.String(), "SECRET") {
				t.Fatalf("failure leaked internal details: %s", rr.Body.String())
			}
			if after := env.state(t); after != before {
				t.Fatalf("failed test changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func withAppOnboardingTestSeams(
	t *testing.T,
	execute appOnboardingTestExecutor,
	now func() time.Time,
	random io.Reader,
	timeout time.Duration,
) {
	t.Helper()
	oldExecute := appOnboardingTestExecute
	oldNow := appOnboardingTestNow
	oldRandom := appOnboardingTestRandom
	oldTimeout := appOnboardingTestTimeout
	appOnboardingTestExecute = execute
	appOnboardingTestNow = now
	appOnboardingTestRandom = random
	appOnboardingTestTimeout = timeout
	t.Cleanup(func() {
		appOnboardingTestExecute = oldExecute
		appOnboardingTestNow = oldNow
		appOnboardingTestRandom = oldRandom
		appOnboardingTestTimeout = oldTimeout
	})
}

func seedAppOnboardingTestChat(
	t *testing.T,
	env *onboardingRouteTestEnv,
	kind string,
	revision int64,
) (store.OnboardingState, string) {
	t.Helper()
	seedAppOnboardingSetup(t, env, kind, "staged-account", false, revision, []store.LLMModel{{
		ModelID:   map[string]string{"codex": "gpt-5.6-terra", "claude-code": "sonnet"}[kind],
		Available: true,
	}})
	const persona = "PERSONA-SECRET: nói ngắn gọn, chân thành."
	configureAgentPersonaForOnboardingTest(t, env, persona)
	if err := env.a.st.SetAgentDisplayName("Bé Mi"); err != nil {
		t.Fatal(err)
	}
	state := env.state(t)
	state.Phase = store.OnboardingPhaseTest
	state.ModelID = map[string]string{"codex": "gpt-5.6-terra", "claude-code": "sonnet"}[kind]
	state.StagedComboID = "staged-combo"
	state.PersonaFingerprint = agentPersonaFingerprint([]byte(persona), "Bé Mi")
	env.setState(t, state)
	return env.state(t), persona
}

func appOnboardingTestSideEffectDigest(t *testing.T, env *onboardingRouteTestEnv) string {
	t.Helper()
	queries := []string{
		`SELECT COUNT(*) FROM llm_attempts`,
		`SELECT COUNT(*) FROM app_zalo_cli_sessions`,
		`SELECT COUNT(*) FROM app_memory_revisions`,
		`SELECT COUNT(*) FROM app_memory_subject_revisions`,
		`SELECT COUNT(*) FROM zalo_threads`,
		`SELECT COUNT(*) FROM zalo_messages`,
		`SELECT COUNT(*) FROM zalo_memory`,
		`SELECT COUNT(*) FROM zalo_lessons`,
		`SELECT COALESCE(group_concat(id || ':' || enabled, ','), '') FROM llm_accounts`,
		`SELECT COALESCE(group_concat(id || ':' || enabled, ','), '') FROM llm_providers`,
		`SELECT COALESCE(group_concat(provider_id || ':' || model_id || ':' || available, ','), '') FROM llm_models`,
	}
	var digest strings.Builder
	digest.WriteString(appOnboardingLiveRoutingBytes(t, env))
	for _, query := range queries {
		var value string
		if err := env.db.QueryRow(query).Scan(&value); err != nil {
			t.Fatalf("side-effect digest query %q: %v", query, err)
		}
		fmt.Fprintf(&digest, "|%s=%s", query, value)
	}
	return digest.String()
}

func TestAgentCompletesOnboardingWithAuthoritativeNameAndFingerprint(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	persona := configureAgentPersonaForOnboardingTest(t, env, "Tên {{TEN_BOT}} ở {{đơn vị}}.\n")
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex",
		ProviderID: "codex", AccountID: "account", ModelID: "model",
		StagedComboID: "combo", Revision: 9,
	})

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{"TEN_BOT":"  An Nhiên  ","đơn vị":"Công ty Mở"},
"display_name":"An Nhiên","require_complete":true,"onboarding_revision":9}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[struct {
		Ready              bool   `json:"ready"`
		DisplayName        string `json:"display_name"`
		OnboardingPhase    string `json:"onboarding_phase"`
		OnboardingRevision int64  `json:"onboarding_revision"`
		LegacyRevision     *int64 `json:"revision"`
	}](t, rr)
	if !got.Ready || got.DisplayName != "An Nhiên" ||
		got.OnboardingPhase != store.OnboardingPhaseTest || got.OnboardingRevision != 10 ||
		got.LegacyRevision != nil {
		t.Fatalf("completion response = %+v", got)
	}
	finalBytes, err := os.ReadFile(persona)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := []byte("Tên An Nhiên ở Công ty Mở.\n")
	if !bytes.Equal(finalBytes, wantBytes) {
		t.Fatalf("final persona = %q; want %q", finalBytes, wantBytes)
	}
	wantFingerprint := framedAgentFingerprintForTest(wantBytes, "An Nhiên")
	state := env.state(t)
	if state.Phase != store.OnboardingPhaseTest || state.Revision != 10 ||
		state.PersonaFingerprint != wantFingerprint ||
		state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Fatalf("completion state = %+v; want fingerprint %q", state, wantFingerprint)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "An Nhiên" {
		t.Fatalf("AgentDisplayName() = %q, %v", name, err)
	}
	backup, err := os.ReadFile(persona + ".goc")
	if err != nil || string(backup) != "Tên {{TEN_BOT}} ở {{đơn vị}}.\n" {
		t.Fatalf("backup = %q, %v", backup, err)
	}
}

func TestAgentPersonaFingerprintFramesAmbiguousPairs(t *testing.T) {
	fingerprints := make([]string, 0, 2)
	for _, tt := range []struct {
		persona     string
		displayName string
	}{
		{persona: "ab", displayName: "c"},
		{persona: "a", displayName: "bc"},
	} {
		env := newOnboardingRouteTestEnv(t)
		configureAgentPersonaForOnboardingTest(t, env, tt.persona)
		env.setState(t, store.OnboardingState{
			Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 3,
		})
		rr := env.serve(http.MethodPut, "/agent", fmt.Sprintf(
			`{"values":{},"display_name":%q,"require_complete":true,"onboarding_revision":3}`,
			tt.displayName,
		))
		if rr.Code != http.StatusOK {
			t.Fatalf("complete (%q,%q) status=%d body=%s", tt.persona, tt.displayName, rr.Code, rr.Body.String())
		}
		fingerprints = append(fingerprints, env.state(t).PersonaFingerprint)
	}
	if fingerprints[0] == fingerprints[1] {
		t.Fatalf("ambiguous pairs share fingerprint %q", fingerprints[0])
	}
}

func TestAgentNormalPutInvalidatesActiveOnboardingReceiptAtomically(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	persona := configureAgentPersonaForOnboardingTest(t, env, "Tên {{TEN_BOT}}.\n")
	if err := env.a.st.SetAgentDisplayName("Old"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "old-fingerprint", TestNonceHash: "old-receipt",
		TestExpiresAt: "2026-08-11T08:00:00Z", Revision: 20,
	})

	rr := env.serve(http.MethodPut, "/agent", `{"values":{"TEN_BOT":"New"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("normal PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || string(got) != "Tên New.\n" {
		t.Fatalf("normal PUT persona = %q (%v)", got, err)
	}
	state := env.state(t)
	if state.Phase != store.OnboardingPhasePersona || state.Revision != 21 ||
		state.PersonaFingerprint != "" || state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Fatalf("normal PUT left stale onboarding receipt: %+v", state)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "New" {
		t.Fatalf("normal PUT display name = %q, %v", name, err)
	}
}

func TestAgentNormalPutPreservesCompletedOnboardingState(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	configureAgentPersonaForOnboardingTest(t, env, "Tên {{TEN_BOT}}.\n")
	env.setState(t, store.OnboardingState{
		CompletedVersion: store.CurrentOnboardingVersion,
		Phase:            store.OnboardingPhaseCompleted,
		ProviderKind:     "codex",
		Revision:         30,
	})

	rr := env.serve(http.MethodPut, "/agent", `{"values":{"TEN_BOT":"New"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("normal PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	state := env.state(t)
	if state.Phase != store.OnboardingPhaseCompleted || state.Revision != 30 ||
		state.CompletedVersion != store.CurrentOnboardingVersion {
		t.Fatalf("normal PUT changed completed onboarding: %+v", state)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "New" {
		t.Fatalf("normal PUT display name = %q, %v", name, err)
	}
}

func TestAgentNormalPutSerializesAgainstStaleCompletion(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	persona := configureAgentPersonaForOnboardingTest(t, env, "Tên {{TEN_BOT}}.\n")
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 9,
	})

	originalWriter := writeAgentFileAtomic
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls int
	var mu sync.Mutex
	var secondOnce sync.Once
	writeAgentFileAtomic = func(path string, data []byte, mode os.FileMode) error {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(firstEntered)
			<-releaseFirst
		} else {
			secondOnce.Do(func() { close(secondEntered) })
		}
		return writeAppFileAtomic(path, data, mode)
	}
	t.Cleanup(func() { writeAgentFileAtomic = originalWriter })

	normalDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		normalDone <- env.serve(http.MethodPut, "/agent", `{"values":{"TEN_BOT":"Normal"}}`)
	}()
	<-firstEntered
	completeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completeDone <- env.serve(http.MethodPut, "/agent", `{
"values":{"TEN_BOT":"Stale"},"require_complete":true,"onboarding_revision":9}`)
	}()

	secondBeforeRelease := false
	select {
	case <-secondEntered:
		secondBeforeRelease = true
	case <-time.After(250 * time.Millisecond):
	}
	close(releaseFirst)
	normal := <-normalDone
	complete := <-completeDone
	if secondBeforeRelease {
		t.Fatal("stale completion entered persona writer while normal PUT still owned it")
	}
	if normal.Code != http.StatusOK {
		t.Fatalf("normal status=%d body=%s", normal.Code, normal.Body.String())
	}
	requireOnboardingCode(t, complete, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
	if got, err := os.ReadFile(persona); err != nil || string(got) != "Tên Normal.\n" {
		t.Fatalf("serialized persona = %q (%v)", got, err)
	}
	state := env.state(t)
	if state.Phase != store.OnboardingPhasePersona || state.PersonaFingerprint != "" {
		t.Fatalf("stale completion attached receipt: %+v", state)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Normal" {
		t.Fatalf("serialized display name = %q, %v", name, err)
	}
}

func TestAgentCompleteRejectsMultilineMustacheWithoutWrites(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Tên {{foo\nbar}}.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Original"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 7,
	})
	before := env.state(t)

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{},"display_name":"New","require_complete":true,"onboarding_revision":7}`)
	requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, "AGENT_PLACEHOLDERS_REMAIN")
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("multiline validation changed persona to %q (%v)", got, err)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Original" {
		t.Fatalf("multiline validation changed name to %q (%v)", name, err)
	}
	if after := env.state(t); after != before {
		t.Fatalf("multiline validation changed state: before=%+v after=%+v", before, after)
	}
	if _, err := os.Stat(persona + ".goc"); !os.IsNotExist(err) {
		t.Fatalf("multiline validation created backup: %v", err)
	}
}

func framedAgentFingerprintForTest(persona []byte, displayName string) string {
	return framedHashForTest("agentdc/agent-persona-fingerprint/v1", persona, []byte(displayName))
}

func framedHashForTest(domain string, fields ...[]byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(domain + "\x00"))
	for _, field := range fields {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(field)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestAgentCompleteLegacyReadyPersonaStoresOnlyDisplayName(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Persona cũ đã hoàn chỉnh.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 5,
	})

	rr := env.serve(http.MethodPut, "/agent",
		`{"values":{},"display_name":"Bot Cũ","require_complete":true,"onboarding_revision":5}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("legacy persona changed to %q (%v)", got, err)
	}
	if _, err := os.Stat(persona + ".goc"); !os.IsNotExist(err) {
		t.Fatalf("legacy metadata-only completion created backup: %v", err)
	}
	get := env.serve(http.MethodGet, "/agent", "")
	got := decodeOnboardingResponse[struct {
		Ready       bool   `json:"ready"`
		DisplayName string `json:"display_name"`
	}](t, get)
	if !got.Ready || got.DisplayName != "Bot Cũ" {
		t.Fatalf("GET /agent = %+v", got)
	}
}

func TestAgentCompleteRejectsRevisionAndPhaseErrors(t *testing.T) {
	tests := []struct {
		name     string
		phase    string
		revision int64
		request  int64
		status   int
		code     string
	}{
		{name: "zero", phase: store.OnboardingPhasePersona, revision: 4, request: 0, status: http.StatusUnprocessableEntity, code: "ONBOARDING_REVISION_INVALID"},
		{name: "negative", phase: store.OnboardingPhasePersona, revision: 4, request: -1, status: http.StatusUnprocessableEntity, code: "ONBOARDING_REVISION_INVALID"},
		{name: "stale", phase: store.OnboardingPhasePersona, revision: 4, request: 3, status: http.StatusConflict, code: "ONBOARDING_REVISION_CONFLICT"},
		{name: "wrong phase", phase: store.OnboardingPhaseSetup, revision: 4, request: 4, status: http.StatusConflict, code: "ONBOARDING_PHASE_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			persona := configureAgentPersonaForOnboardingTest(t, env, "Persona hoàn chỉnh.\n")
			env.setState(t, store.OnboardingState{
				Phase: tt.phase, ProviderKind: "codex", Revision: tt.revision,
			})
			before := env.state(t)
			rr := env.serve(http.MethodPut, "/agent", fmt.Sprintf(
				`{"values":{},"display_name":"Bot","require_complete":true,"onboarding_revision":%d}`,
				tt.request,
			))
			requireOnboardingCode(t, rr, tt.status, tt.code)
			if after := env.state(t); after != before {
				t.Fatalf("rejected completion changed state: before=%+v after=%+v", before, after)
			}
			if got, err := os.ReadFile(persona); err != nil || string(got) != "Persona hoàn chỉnh.\n" {
				t.Fatalf("rejected completion changed persona to %q (%v)", got, err)
			}
		})
	}
}

func TestAgentCompletePlaceholdersRemainHasZeroWrites(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Tên {{TEN_BOT}}, vai trò {{vai-trò}}, hỏng {{")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Original"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 6,
	})
	before := env.state(t)

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{"TEN_BOT":"Bot"},"require_complete":true,"onboarding_revision":6}`)
	requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, "AGENT_PLACEHOLDERS_REMAIN")
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("validation changed persona to %q (%v)", got, err)
	}
	if _, err := os.Stat(persona + ".goc"); !os.IsNotExist(err) {
		t.Fatalf("validation created backup: %v", err)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Original" {
		t.Fatalf("validation changed display name to %q (%v)", name, err)
	}
	if after := env.state(t); after != before {
		t.Fatalf("validation changed state: before=%+v after=%+v", before, after)
	}
}

func TestAgentCompleteAtomicWriteFailurePreservesAllState(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Tên {{TEN_BOT}}.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Original"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 8,
	})
	before := env.state(t)
	originalWriter := writeAgentFileAtomic
	writeAgentFileAtomic = func(string, []byte, os.FileMode) error {
		return fmt.Errorf("injected atomic write failure")
	}
	t.Cleanup(func() { writeAgentFileAtomic = originalWriter })

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{"TEN_BOT":"Bot"},"require_complete":true,"onboarding_revision":8}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s; want 500", rr.Code, rr.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("failed write changed persona to %q (%v)", got, err)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Original" {
		t.Fatalf("failed write changed display name to %q (%v)", name, err)
	}
	if after := env.state(t); after != before {
		t.Fatalf("failed write changed state: before=%+v after=%+v", before, after)
	}
}

func TestAgentCompleteStoreFailureRestoresPersonaAndMetadata(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Tên {{TEN_BOT}}.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Original"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 12,
	})
	before := env.state(t)
	if _, err := env.db.Exec(`CREATE TRIGGER fail_agent_persona_cas
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'injected CAS failure'); END`); err != nil {
		t.Fatal(err)
	}

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{"TEN_BOT":"Changed"},"require_complete":true,"onboarding_revision":12}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s; want 500", rr.Code, rr.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("store failure left persona %q (%v)", got, err)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Original" {
		t.Fatalf("store failure changed display name to %q (%v)", name, err)
	}
	if after := env.state(t); after != before {
		t.Fatalf("store failure changed state: before=%+v after=%+v", before, after)
	}
}

func TestAgentNormalStoreFailureRestoresPersonaAndMetadata(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Tên {{TEN_BOT}}.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Original"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "fingerprint", TestNonceHash: "receipt", Revision: 12,
	})
	before := env.state(t)
	if _, err := env.db.Exec(`CREATE TRIGGER fail_normal_agent_persona_update
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'injected normal update failure'); END`); err != nil {
		t.Fatal(err)
	}

	rr := env.serve(http.MethodPut, "/agent", `{"values":{"TEN_BOT":"Changed"}}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s; want 500", rr.Code, rr.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("store failure left persona %q (%v)", got, err)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Original" {
		t.Fatalf("store failure changed display name to %q (%v)", name, err)
	}
	if after := env.state(t); after != before {
		t.Fatalf("store failure changed state: before=%+v after=%+v", before, after)
	}
}

func TestAgentRollbackFailureIsFailClosedAndRecoveredByNextWrite(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Tên {{TEN_BOT}}.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Original"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 12,
	})
	before := env.state(t)
	if _, err := env.db.Exec(`CREATE TRIGGER fail_agent_persona_with_recovery
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'injected Store failure'); END`); err != nil {
		t.Fatal(err)
	}
	originalRestore := restoreAgentFileAtomic
	restoreAgentFileAtomic = func(string, []byte, os.FileMode) error {
		return fmt.Errorf("injected restore failure")
	}
	t.Cleanup(func() { restoreAgentFileAtomic = originalRestore })

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{"TEN_BOT":"Changed"},"require_complete":true,"onboarding_revision":12}`)
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "AGENT_ROLLBACK_FAILED")
	if strings.Contains(rr.Body.String(), persona) || strings.Contains(rr.Body.String(), string(original)) {
		t.Fatalf("rollback response leaked path/content: %s", rr.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || string(got) != "Tên Changed.\n" {
		t.Fatalf("failed rollback persona = %q (%v)", got, err)
	}
	if _, err := os.Stat(persona + ".agentdc-recovery"); err != nil {
		t.Fatalf("recovery obligation missing: %v", err)
	}
	if after := env.state(t); after != before {
		t.Fatalf("failed Store transaction changed state: before=%+v after=%+v", before, after)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Original" {
		t.Fatalf("failed Store transaction changed name to %q (%v)", name, err)
	}
	get := env.serve(http.MethodGet, "/agent", "")
	getState := decodeOnboardingResponse[struct {
		Ready           bool   `json:"ready"`
		ValidationError string `json:"validation_error"`
	}](t, get)
	if getState.Ready || getState.ValidationError == "" {
		t.Fatalf("pending recovery was not visible in GET: %s", get.Body.String())
	}

	if _, err := env.db.Exec(`DROP TRIGGER fail_agent_persona_with_recovery`); err != nil {
		t.Fatal(err)
	}
	restoreAgentFileAtomic = originalRestore
	retry := env.serve(http.MethodPut, "/agent", `{"values":{"TEN_BOT":"Recovered"}}`)
	if retry.Code != http.StatusOK {
		t.Fatalf("recovery retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || string(got) != "Tên Recovered.\n" {
		t.Fatalf("recovered persona = %q (%v)", got, err)
	}
	if _, err := os.Stat(persona + ".agentdc-recovery"); !os.IsNotExist(err) {
		t.Fatalf("successful recovery left obligation: %v", err)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Recovered" {
		t.Fatalf("recovered display name = %q, %v", name, err)
	}
}

func TestAgentStaleCompletionReconcilesCommittedRecoveryBeforeConflict(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Tên Committed.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhasePersona, ProviderKind: "codex", Revision: 12,
	})
	token, err := env.a.prepareAgentPersonaRecovery(
		persona,
		[]byte("Tên {{TEN_BOT}}.\n"),
		original,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.a.st.AdvanceOnboardingPersonaWithRecovery(
		12,
		"committed-fingerprint",
		"Committed",
		token,
	); err != nil {
		t.Fatal(err)
	}

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{},"display_name":"Committed","require_complete":true,"onboarding_revision":12}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
	if _, err := os.Stat(persona + ".agentdc-recovery"); !os.IsNotExist(err) {
		t.Fatalf("stale retry left committed recovery obligation: %v", err)
	}
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("committed recovery changed persona to %q (%v)", got, err)
	}
}

func TestAgentLegacyRecoveryNeverOverwritesCurrentPersona(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	current := []byte("Tên Later.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(current))
	setAgentRecoveryTokenForTest(t, env, strings.Repeat("c", 32))
	record := agentPersonaRecoveryRecord{
		Version:  1,
		Token:    strings.Repeat("a", 32),
		Original: []byte("Tên {{TEN_BOT}}.\n"),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPersonaRecoveryPath(persona), encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	rr := env.serve(http.MethodPut, "/agent", `{"values":{"UNKNOWN":"Requested"}}`)
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "AGENT_ROLLBACK_FAILED")
	requireAgentRecoveryFileState(t, persona, current, true)
}

func TestAgentRecoveryRejectsUnownedJournalWithoutOverwrite(t *testing.T) {
	const (
		token         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		previousToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		otherToken    = "cccccccccccccccccccccccccccccccc"
	)
	original := []byte("Tên {{TEN_BOT}}.\n")
	replacement := []byte("Tên Intended.\n")
	third := []byte("Tên Later.\n")
	tests := []struct {
		name        string
		current     []byte
		storedToken string
		bindingPath func(string) string
		domain      string
	}{
		{
			name: "stale sidecar token", current: replacement, storedToken: otherToken,
		},
		{
			name: "cross persona copied journal", current: replacement, storedToken: previousToken,
			bindingPath: func(string) string {
				return filepath.Join(t.TempDir(), "source-persona.md")
			},
		},
		{
			name: "committed token current file mismatch", current: third, storedToken: token,
		},
		{
			name: "uncommitted token current file mismatch", current: third, storedToken: previousToken,
		},
		{
			name: "journal domain mismatch", current: replacement, storedToken: previousToken,
			domain: "agentdc/other-recovery/v2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			persona := configureAgentPersonaForOnboardingTest(t, env, string(tt.current))
			bindingPath := persona
			if tt.bindingPath != nil {
				bindingPath = tt.bindingPath(persona)
			}
			domain := tt.domain
			if domain == "" {
				domain = agentRecoveryDomainForTest
			}
			writeAgentRecoveryFixtureForTest(
				t,
				agentPersonaRecoveryPath(persona),
				bindingPath,
				original,
				replacement,
				token,
				previousToken,
				domain,
			)
			setAgentRecoveryTokenForTest(t, env, tt.storedToken)

			rr := env.serve(http.MethodPut, "/agent", `{"values":{"UNKNOWN":"Requested"}}`)
			requireOnboardingCode(t, rr, http.StatusInternalServerError, "AGENT_ROLLBACK_FAILED")
			requireAgentRecoveryFileState(t, persona, tt.current, true)
		})
	}
}

func TestAgentRecoveryFinalizesOnlyOwnedFileStates(t *testing.T) {
	const (
		token         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		previousToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	original := []byte("Tên Original.\n")
	replacement := []byte("Tên Intended.\n")
	tests := []struct {
		name        string
		current     []byte
		storedToken string
		want        []byte
	}{
		{
			name: "committed intended replacement", current: replacement,
			storedToken: token, want: replacement,
		},
		{
			name: "uncommitted intended replacement", current: replacement,
			storedToken: previousToken, want: original,
		},
		{
			name: "uncommitted already original", current: original,
			storedToken: previousToken, want: original,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			persona := configureAgentPersonaForOnboardingTest(t, env, string(tt.current))
			writeAgentRecoveryFixtureForTest(
				t,
				agentPersonaRecoveryPath(persona),
				persona,
				original,
				replacement,
				token,
				previousToken,
				agentRecoveryDomainForTest,
			)
			setAgentRecoveryTokenForTest(t, env, tt.storedToken)

			rr := env.serve(http.MethodPut, "/agent", `{"values":{"UNKNOWN":"Requested"}}`)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s; want post-recovery validation 400", rr.Code, rr.Body.String())
			}
			requireAgentRecoveryFileState(t, persona, tt.want, false)
		})
	}
}

func TestAgentRecoveryRejectsSymlinkAndNonRegularSidecars(t *testing.T) {
	const (
		token         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		previousToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	current := []byte("Tên Intended.\n")
	original := []byte("Tên Original.\n")
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			persona := configureAgentPersonaForOnboardingTest(t, env, string(current))
			sidecar := agentPersonaRecoveryPath(persona)
			switch kind {
			case "symlink":
				target := filepath.Join(t.TempDir(), "recovery.json")
				writeAgentRecoveryFixtureForTest(
					t, target, persona, original, current, token, previousToken, agentRecoveryDomainForTest,
				)
				if err := os.Symlink(target, sidecar); err != nil {
					t.Skipf("filesystem cannot create a test symlink: %v", err)
				}
			case "directory":
				if err := os.Mkdir(sidecar, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			setAgentRecoveryTokenForTest(t, env, previousToken)

			rr := env.serve(http.MethodPut, "/agent", `{"values":{"UNKNOWN":"Requested"}}`)
			requireOnboardingCode(t, rr, http.StatusInternalServerError, "AGENT_ROLLBACK_FAILED")
			requireAgentRecoveryFileState(t, persona, current, true)
		})
	}
}

const agentRecoveryDomainForTest = "agentdc/agent-persona-recovery/v2"

func writeAgentRecoveryFixtureForTest(
	t *testing.T,
	sidecarPath string,
	bindingPath string,
	original []byte,
	replacement []byte,
	token string,
	previousToken string,
	domain string,
) {
	t.Helper()
	absolute, err := filepath.Abs(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Clean(absolute)
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	record := struct {
		Version         int    `json:"version"`
		Domain          string `json:"domain"`
		Token           string `json:"token"`
		PreviousToken   string `json:"previous_token"`
		PersonaBinding  string `json:"persona_binding"`
		Original        []byte `json:"original"`
		OriginalHash    string `json:"original_sha256"`
		ReplacementHash string `json:"replacement_sha256"`
	}{
		Version:         2,
		Domain:          domain,
		Token:           token,
		PreviousToken:   previousToken,
		PersonaBinding:  framedHashForTest(domain+"/path", []byte(canonical)),
		Original:        append([]byte(nil), original...),
		OriginalHash:    framedHashForTest(domain+"/original", original),
		ReplacementHash: framedHashForTest(domain+"/replacement", replacement),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecarPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func setAgentRecoveryTokenForTest(t *testing.T, env *onboardingRouteTestEnv, token string) {
	t.Helper()
	if _, err := env.db.Exec(`INSERT INTO app_meta(key, value)
VALUES ('agent_persona_recovery_token', ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, token); err != nil {
		t.Fatal(err)
	}
}

func requireAgentRecoveryFileState(
	t *testing.T,
	persona string,
	wantPersona []byte,
	wantSidecar bool,
) {
	t.Helper()
	got, err := os.ReadFile(persona)
	if err != nil || !bytes.Equal(got, wantPersona) {
		t.Fatalf("persona = %q, %v; want %q", got, err, wantPersona)
	}
	_, err = os.Lstat(agentPersonaRecoveryPath(persona))
	if wantSidecar && err != nil {
		t.Fatalf("recovery obligation missing: %v", err)
	}
	if !wantSidecar && !os.IsNotExist(err) {
		t.Fatalf("recovery obligation was not removed: %v", err)
	}
}

func TestPersonaFullEditInvalidatesOnboardingReceipt(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	configureAgentPersonaForOnboardingTest(t, env, "Persona cũ.\n")
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "fingerprint", TestNonceHash: "receipt",
		TestExpiresAt: "2026-08-11T08:00:00Z", Revision: 20,
	})

	rr := env.serve(http.MethodPut, "/agent/persona/persona", `{"text":"Persona mới.\n"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	state := env.state(t)
	if state.Phase != store.OnboardingPhasePersona || state.Revision != 21 ||
		state.PersonaFingerprint != "" || state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Fatalf("full edit did not invalidate receipt: %+v", state)
	}
}

func configureAgentPersonaForOnboardingTest(t *testing.T, env *onboardingRouteTestEnv, text string) string {
	t.Helper()
	persona := filepath.Join(env.dataDir, "persona.md")
	if err := os.WriteFile(persona, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	env.a.zalo = &zaloDeps{cfg: zaloConfig{PersonaPath: persona, Model: "haiku"}}
	return persona
}

func TestAppOnboardingSetupStagesComboWithoutChangingLiveRoute(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedOnboardingRoute(t, env, "live-provider", "openai", true)
	if _, err := env.db.Exec(`INSERT INTO llm_route_entries(position, provider_id, model_id, enabled)
VALUES (0, 'live-provider', 'model', 1)`); err != nil {
		t.Fatal(err)
	}
	seedAppOnboardingSetup(t, env, "codex", "staged-account", false, 7, []store.LLMModel{
		{ModelID: "gpt-5.6-terra", Available: true},
	})
	beforeLive := appOnboardingLiveRoutingBytes(t, env)

	rr := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":7,"account_id":"staged-account"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[struct {
		Revision      int64  `json:"revision"`
		ProviderKind  string `json:"provider_kind"`
		ProviderID    string `json:"provider_id"`
		AccountID     string `json:"account_id"`
		ModelID       string `json:"model_id"`
		StagedComboID string `json:"staged_combo_id"`
		ComboName     string `json:"combo_name"`
	}](t, rr)
	if got.Revision != 8 || got.ProviderKind != "codex" || got.ProviderID != "codex" ||
		got.AccountID != "staged-account" || got.ModelID != "gpt-5.6-terra" ||
		got.StagedComboID == "" || got.ComboName != "Mặc định · Codex" {
		t.Fatalf("setup response = %+v", got)
	}
	state := env.state(t)
	if state.Phase != store.OnboardingPhasePersona || state.Revision != 8 ||
		state.ModelID != got.ModelID || state.StagedComboID != got.StagedComboID {
		t.Fatalf("staged state = %+v; response = %+v", state, got)
	}
	if afterLive := appOnboardingLiveRoutingBytes(t, env); afterLive != beforeLive {
		t.Fatalf("setup changed live routing rows:\nbefore=%q\nafter=%q", beforeLive, afterLive)
	}
}

func TestAppOnboardingSetupUsesClaudePreferredModel(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingSetup(t, env, "claude-code", "claude-account", false, 3, nil)

	rr := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":3,"account_id":"claude-account"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[map[string]any](t, rr)
	if got["model_id"] != "sonnet" || got["combo_name"] != "Mặc định · Claude" {
		t.Fatalf("setup response = %#v", got)
	}
}

func TestAppOnboardingSetupFallsBackInDescriptorSeedOrder(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingSetup(t, env, "codex", "fallback-account", false, 5, []store.LLMModel{
		{ModelID: "gpt-5.6-terra", Available: false},
		{ModelID: "gpt-5.5", Available: true},
		{ModelID: "gpt-5.6-luna", Available: true},
	})

	rr := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":5,"account_id":"fallback-account"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[map[string]any](t, rr)
	if got["model_id"] != "gpt-5.6-luna" {
		t.Fatalf("fallback model = %#v; want descriptor's first available seed gpt-5.6-luna", got["model_id"])
	}
}

func TestAppOnboardingSetupNoAvailableModelFailsWithoutMutation(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingSetup(t, env, "codex", "no-model-account", false, 4, []store.LLMModel{
		{ModelID: "gpt-5.6-terra", Available: false},
		{ModelID: "gpt-5.6-luna", Available: false},
	})
	before := env.state(t)
	beforeLive := appOnboardingLiveRoutingBytes(t, env)

	rr := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":4,"account_id":"no-model-account"}`)
	requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, "ONBOARDING_NO_MODEL")
	if after := env.state(t); after != before {
		t.Fatalf("no-model request changed state: before=%+v after=%+v", before, after)
	}
	if afterLive := appOnboardingLiveRoutingBytes(t, env); afterLive != beforeLive {
		t.Fatalf("no-model request changed live routing: before=%q after=%q", beforeLive, afterLive)
	}
}

func TestAppOnboardingSetupRejectsWrongOrEnabledAccount(t *testing.T) {
	tests := []struct {
		name, requestAccount string
		enabled              bool
	}{
		{name: "wrong account", requestAccount: "other-account"},
		{name: "enabled account", requestAccount: "staged-account", enabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingSetup(t, env, "codex", "staged-account", tt.enabled, 6, []store.LLMModel{
				{ModelID: "gpt-5.6-terra", Available: true},
			})
			before := env.state(t)
			rr := env.serve(http.MethodPost, "/onboarding/setup",
				fmt.Sprintf(`{"revision":6,"account_id":%q}`, tt.requestAccount))
			requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_STAGING_INVALID")
			if after := env.state(t); after != before {
				t.Fatalf("rejected setup changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAppOnboardingSetupRejectsStaleRevisionAndWrongPhase(t *testing.T) {
	tests := []struct {
		name, phase, body, code string
	}{
		{name: "stale", phase: store.OnboardingPhaseSetup, body: `{"revision":8,"account_id":"staged-account"}`, code: "ONBOARDING_REVISION_CONFLICT"},
		{name: "wrong phase", phase: store.OnboardingPhaseConnect, body: `{"revision":9,"account_id":"staged-account"}`, code: "ONBOARDING_PHASE_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingSetup(t, env, "codex", "staged-account", false, 9, []store.LLMModel{
				{ModelID: "gpt-5.6-terra", Available: true},
			})
			state := env.state(t)
			state.Phase = tt.phase
			env.setState(t, state)
			before := env.state(t)
			rr := env.serve(http.MethodPost, "/onboarding/setup", tt.body)
			requireOnboardingCode(t, rr, http.StatusConflict, tt.code)
			if after := env.state(t); after != before {
				t.Fatalf("rejected setup changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAppOnboardingSetupLostResponseRetryIsIdempotent(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingSetup(t, env, "codex", "staged-account", false, 12, []store.LLMModel{
		{ModelID: "gpt-5.6-terra", Available: true},
	})
	body := `{"revision":12,"account_id":"staged-account"}`
	first := env.serve(http.MethodPost, "/onboarding/setup", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	stateAfterFirst := env.state(t)
	retry := env.serve(http.MethodPost, "/onboarding/setup", body)
	if retry.Code != http.StatusOK || retry.Body.String() != first.Body.String() {
		t.Fatalf("retry status/body = %d/%q; want %d/%q", retry.Code, retry.Body.String(), first.Code, first.Body.String())
	}
	if stateAfterRetry := env.state(t); stateAfterRetry != stateAfterFirst {
		t.Fatalf("retry changed state: first=%+v retry=%+v", stateAfterFirst, stateAfterRetry)
	}

	mismatch := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":12,"account_id":"other-account"}`)
	requireOnboardingCode(t, mismatch, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
	if stateAfterMismatch := env.state(t); stateAfterMismatch != stateAfterFirst {
		t.Fatalf("mismatched retry changed state: first=%+v mismatch=%+v", stateAfterFirst, stateAfterMismatch)
	}
}

func TestAppOnboardingSetupStrictJSON(t *testing.T) {
	tests := []string{
		`{"revision":1,"account_id":"account","extra":true}`,
		`{"revision":1,`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			before := env.state(t)
			rr := env.serve(http.MethodPost, "/onboarding/setup", body)
			requireOnboardingCode(t, rr, http.StatusBadRequest, "ONBOARDING_REQUEST_INVALID")
			if after := env.state(t); after != before {
				t.Fatalf("invalid JSON changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func seedAppOnboardingSetup(
	t *testing.T,
	env *onboardingRouteTestEnv,
	kind, accountID string,
	accountEnabled bool,
	revision int64,
	models []store.LLMModel,
) {
	t.Helper()
	if err := env.a.st.EnsureOnboardingProviderForKind(kind); err != nil {
		t.Fatalf("EnsureOnboardingProviderForKind(%q) = %v", kind, err)
	}
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: accountID, ProviderID: kind, Label: accountID,
		ConfigDir: "D:/onboarding/" + accountID, Enabled: accountEnabled,
	}); err != nil {
		t.Fatalf("CreateLLMAccount(%q) = %v", accountID, err)
	}
	for _, model := range models {
		model.ProviderID = kind
		model.Name = model.ModelID
		model.Source = store.LLMModelManual
		if err := env.a.st.AddLLMModel(model); err != nil {
			t.Fatalf("AddLLMModel(%q) = %v", model.ModelID, err)
		}
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseSetup, ProviderKind: kind, ProviderID: kind,
		AccountID: accountID, Revision: revision,
	})
}

func appOnboardingLiveRoutingBytes(t *testing.T, env *onboardingRouteTestEnv) string {
	t.Helper()
	queries := []string{
		`SELECT id, name, type, active, revision FROM llm_combos ORDER BY id`,
		`SELECT combo_id, position, provider_id, model_id, enabled FROM llm_combo_members ORDER BY combo_id, position`,
		`SELECT position, provider_id, model_id, enabled FROM llm_route_entries ORDER BY position`,
	}
	var snapshot strings.Builder
	for _, query := range queries {
		rows, err := env.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		fmt.Fprintf(&snapshot, "%s|", query)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			fmt.Fprintf(&snapshot, "%#v;", values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot.String()
}
