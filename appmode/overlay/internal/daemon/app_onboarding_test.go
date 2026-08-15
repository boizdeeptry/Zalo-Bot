package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
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
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/store"
)

func TestAppOnboardingTestChatPersistsHashOnlyReceipt(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	state, persona := seedAppOnboardingTestChat(t, env, "codex", 41)
	route, err := env.a.st.OnboardingTestRoute(context.Background(), state.Revision)
	if err != nil {
		t.Fatal(err)
	}
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
		appOnboardingRandomFromReader(bytes.NewReader(randomBytes)),
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
		!sameOnboardingPath(gotAccount.ConfigDir, accountConfigDir(env.dataDir, "codex", "staged-account")) {
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
	wantHash, err := store.OnboardingTestReceiptHash(hex.EncodeToString(wantHashBytes[:]), route.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
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

func TestAppOnboardingTestChatRealClaudeAdapterIsIsolatedAndStateless(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	state, _ := seedAppOnboardingTestChat(t, env, "claude-code", 45)
	stagedConfig := accountConfigDir(env.dataDir, "claude-code", "staged-account")
	safeKB := t.TempDir()
	safeWork := t.TempDir()
	liveFiles := filepath.Join(t.TempDir(), "customer-files")
	liveRoster := filepath.Join(t.TempDir(), "roster.md")
	liveOverlay := filepath.Join(t.TempDir(), "overlays")
	env.a.zalo.cfg.KBRoots = []string{safeKB}
	env.a.zalo.cfg.WorkDir = safeWork
	env.a.zalo.cfg.FilesDir = liveFiles
	env.a.zalo.cfg.RosterPath = liveRoster
	env.a.zalo.cfg.OverlayDir = liveOverlay
	env.a.zalo.cfg.overlayFor = filepath.Join(liveOverlay, "customer-thread.md")
	env.a.zalo.cfg.OwnerUID = "live-owner-uid"
	fakeClaudeExe(t)

	var gotArgs []string
	var gotEnv []string
	var gotDir string
	var gotPrompt string
	oldProcess := appOnboardingRunCLIProcess
	appOnboardingRunCLIProcess = func(_ context.Context, cmd *exec.Cmd, stdin []byte, _ chan<- int, _ *slog.Logger) ([]byte, error) {
		gotArgs = slices.Clone(cmd.Args)
		gotEnv = slices.Clone(cmd.Env)
		gotDir = cmd.Dir
		gotPrompt = string(stdin)
		return []byte(`{"type":"system","subtype":"init"}` + "\n" +
			`{"type":"result","result":"Xin chào, tôi là Bé Mi."}` + "\n"), nil
	}
	t.Cleanup(func() { appOnboardingRunCLIProcess = oldProcess })
	withAppOnboardingTestSeams(t,
		defaultAppOnboardingTestExecute,
		func() time.Time { return time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC) },
		appOnboardingRandomFromReader(bytes.NewReader(bytes.Repeat([]byte{0x44}, 32))),
		time.Second,
	)
	beforeSideEffects := appOnboardingTestSideEffectDigest(t, env)
	beforeFiles, err := os.ReadDir(stagedConfig)
	if err != nil {
		t.Fatal(err)
	}

	rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":45,"message":"xin chào"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := decodeOnboardingResponse[appOnboardingTestChatResponse](t, rr)
	if response.Answer != "Xin chào, tôi là Bé Mi." || response.Revision != state.Revision+1 {
		t.Fatalf("structured adapter response = %+v", response)
	}
	if slices.Contains(gotArgs, "--session-id") || !slices.Contains(gotArgs, "--no-session-persistence") {
		t.Fatalf("Claude argv is stateful: %q", gotArgs)
	}
	joinedArgs := strings.Join(gotArgs, "\x00")
	for _, forbidden := range []string{liveFiles, liveRoster, liveOverlay, "customer-thread", "live-owner-uid"} {
		if strings.Contains(joinedArgs, forbidden) || strings.Contains(gotPrompt, forbidden) {
			t.Fatalf("real adapter leaked live/customer config %q: argv=%q prompt=%q", forbidden, gotArgs, gotPrompt)
		}
	}
	if !strings.Contains(joinedArgs, safeKB) || gotDir != safeWork {
		t.Fatalf("real adapter safe roots/workdir = argv %q dir %q; want KB %q workdir %q", gotArgs, gotDir, safeKB, safeWork)
	}
	var stagedEnv []string
	for _, item := range gotEnv {
		if strings.HasPrefix(strings.ToUpper(item), "CLAUDE_CONFIG_DIR=") {
			stagedEnv = append(stagedEnv, item[len("CLAUDE_CONFIG_DIR="):])
		}
	}
	if !slices.Equal(stagedEnv, []string{stagedConfig}) {
		t.Fatalf("CLAUDE_CONFIG_DIR values = %v; want [%q]", stagedEnv, stagedConfig)
	}
	afterFiles, err := os.ReadDir(stagedConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterFiles) != len(beforeFiles) {
		t.Fatalf("staged config gained session/transcript files: before=%v after=%v", beforeFiles, afterFiles)
	}
	if afterSideEffects := appOnboardingTestSideEffectDigest(t, env); afterSideEffects != beforeSideEffects {
		t.Fatalf("real adapter changed production attempts/sessions/memory/live tables:\nbefore=%q\nafter=%q",
			beforeSideEffects, afterSideEffects)
	}
}

func TestAppOnboardingTestChatClaudeInvalidStreamsPersistNothingAndLeakNothing(t *testing.T) {
	tests := []struct {
		name, output, secret string
	}{
		{name: "progress only", output: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"path":"PROGRESS-SECRET"}}]}}` + "\n", secret: "PROGRESS-SECRET"},
		{name: "malformed JSON", output: `{"type":"result","result":"MALFORMED-SECRET"` + "\n", secret: "MALFORMED-SECRET"},
		{name: "session metadata only", output: `{"type":"system","session_id":"SESSION-SECRET"}` + "\n", secret: "SESSION-SECRET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingTestChat(t, env, "claude-code", 46)
			env.a.zalo.cfg.KBRoots = []string{t.TempDir()}
			env.a.zalo.cfg.WorkDir = t.TempDir()
			fakeClaudeExe(t)
			oldProcess := appOnboardingRunCLIProcess
			appOnboardingRunCLIProcess = func(context.Context, *exec.Cmd, []byte, chan<- int, *slog.Logger) ([]byte, error) {
				return []byte(tt.output), nil
			}
			t.Cleanup(func() { appOnboardingRunCLIProcess = oldProcess })
			withAppOnboardingTestSeams(t,
				defaultAppOnboardingTestExecute,
				time.Now,
				appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
				time.Second,
			)

			rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":46,"message":"xin chào"}`)
			requireOnboardingCode(t, rr, http.StatusBadGateway, "ONBOARDING_TEST_FAILED")
			if strings.Contains(rr.Body.String(), tt.secret) {
				t.Fatalf("raw Claude event leaked to response: %s", rr.Body.String())
			}
			if state := env.state(t); state.TestNonceHash != "" || state.TestExpiresAt != "" || state.Revision != 46 {
				t.Fatalf("invalid Claude stream changed receipt state: %+v", state)
			}
		})
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
				appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
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
				appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
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

func TestAppOnboardingTestChatRejectsNonOwnedConfigPathsBeforeRunner(t *testing.T) {
	tests := []struct {
		name       string
		accountID  string
		configPath func(*onboardingRouteTestEnv, string) string
	}{
		{
			name:      "live account path",
			accountID: "staged-account",
			configPath: func(env *onboardingRouteTestEnv, _ string) string {
				return accountConfigDir(env.dataDir, "codex", "live-account")
			},
		},
		{
			name:      "outside path",
			accountID: "staged-account",
			configPath: func(_ *onboardingRouteTestEnv, _ string) string {
				return t.TempDir()
			},
		},
		{
			name:      "traversal alias",
			accountID: "staged-account",
			configPath: func(_ *onboardingRouteTestEnv, expected string) string {
				return expected + string(filepath.Separator) + ".." + string(filepath.Separator) + "staged-account"
			},
		},
		{
			name:      "invalid account id",
			accountID: "..",
			configPath: func(_ *onboardingRouteTestEnv, expected string) string {
				return expected
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingTestChat(t, env, "codex", 41)
			expected := accountConfigDir(env.dataDir, "codex", "staged-account")
			if tt.accountID != "staged-account" {
				if _, err := env.db.Exec(`UPDATE llm_accounts SET id = ?, config_dir = ? WHERE id = 'staged-account'`,
					tt.accountID, accountConfigDir(env.dataDir, "codex", tt.accountID)); err != nil {
					t.Fatal(err)
				}
				state := env.state(t)
				state.AccountID = tt.accountID
				env.setState(t, state)
				expected = accountConfigDir(env.dataDir, "codex", tt.accountID)
			}
			badPath := tt.configPath(env, expected)
			if err := os.MkdirAll(filepath.Clean(badPath), 0o700); err != nil {
				t.Fatalf("MkdirAll corrupt config path: %v", err)
			}
			if _, err := env.db.Exec(`UPDATE llm_accounts SET config_dir = ? WHERE id = ?`, badPath, tt.accountID); err != nil {
				t.Fatal(err)
			}
			calls := 0
			withAppOnboardingTestSeams(t,
				func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
					calls++
					return "unexpected", nil
				},
				time.Now,
				appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
				time.Second,
			)

			rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":41,"message":"xin chào"}`)
			requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_STAGING_INVALID")
			if calls != 0 {
				t.Fatalf("unsafe config path called runner %d times", calls)
			}
			if state := env.state(t); state.TestNonceHash != "" || state.TestExpiresAt != "" {
				t.Fatalf("unsafe config path persisted receipt: %+v", state)
			}
		})
	}
}

func TestAppOnboardingTestChatRejectsLinkedConfigPathBeforeRunner(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingTestChat(t, env, "codex", 41)
	configDir := accountConfigDir(env.dataDir, "codex", "staged-account")
	if err := os.Remove(configDir); err != nil {
		t.Fatalf("remove empty config dir: %v", err)
	}
	victim := t.TempDir()
	if runtime.GOOS == "windows" {
		makeForcedOnboardingJunction(t, victim, configDir)
	} else if err := os.Symlink(victim, configDir); err != nil {
		t.Fatalf("create config symlink: %v", err)
	}
	calls := 0
	withAppOnboardingTestSeams(t,
		func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
			calls++
			return "unexpected", nil
		},
		time.Now,
		appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
		time.Second,
	)

	rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":41,"message":"xin chào"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_STAGING_INVALID")
	if calls != 0 {
		t.Fatalf("linked config path called runner %d times", calls)
	}
}

func TestAppOnboardingTestChatRejectsEmptyOrMissingNameAnswer(t *testing.T) {
	tests := []struct {
		name, answer string
		wantCode     string
	}{
		{name: "empty", answer: " \n\t ", wantCode: "ONBOARDING_TEST_ANSWER_INVALID"},
		{name: "missing name", answer: "Xin chào, tôi là trợ lý của bạn.", wantCode: "ONBOARDING_PERSONA_NOT_APPLIED"},
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
				appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
				120*time.Second,
			)
			rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":9,"message":"xin chào"}`)
			requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, tt.wantCode)
			if after := env.state(t); after != before {
				t.Fatalf("invalid answer changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestNormalizedOnboardingAnswerContainsNameUsesUnicodeSimpleFoldAndWhitespace(t *testing.T) {
	tests := []struct {
		name, answer, displayName string
		want                      bool
	}{
		{name: "Greek sigma cycle", answer: "Είμαι ΟΣ", displayName: "ος", want: true},
		{name: "Kelvin fold cycle", answer: "Tôi là K Bot", displayName: "k bot", want: true},
		{name: "long s fold cycle", answer: "Đây là ſora", displayName: "SORA", want: true},
		{name: "Unicode whitespace", answer: "Xin chào, Bé\u2003\tMi", displayName: "bé mi", want: true},
		{name: "different name", answer: "Xin chào An Nhiên", displayName: "Bé Mi", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedOnboardingAnswerContainsName(tt.answer, tt.displayName); got != tt.want {
				t.Fatalf("normalizedOnboardingAnswerContainsName(%q, %q) = %t; want %t",
					tt.answer, tt.displayName, got, tt.want)
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
		appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
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
				appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
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
		appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
		10*time.Millisecond,
	)

	rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":16,"message":"xin chào"}`)
	requireOnboardingCode(t, rr, http.StatusGatewayTimeout, "ONBOARDING_TEST_TIMEOUT")
	if after := env.state(t); after != before {
		t.Fatalf("late success changed state: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingTestChatPostflightRejectsMutationsWhileRunnerIsBlocked(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*testing.T, *onboardingRouteTestEnv)
		wantCode string
	}{
		{
			name: "persona bytes",
			mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
				t.Helper()
				if err := os.WriteFile(env.a.zalo.cfg.PersonaPath, []byte("persona changed directly"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "ONBOARDING_PERSONA_CHANGED",
		},
		{
			name: "display name",
			mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
				t.Helper()
				if err := env.a.st.SetAgentDisplayName("Tên Khác"); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "ONBOARDING_PERSONA_CHANGED",
		},
		{
			name: "account config",
			mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
				t.Helper()
				if _, err := env.db.Exec(`UPDATE llm_accounts SET config_dir = ? WHERE id = 'staged-account'`, t.TempDir()); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "ONBOARDING_STAGING_INVALID",
		},
		{
			name: "account enabled",
			mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
				t.Helper()
				if _, err := env.db.Exec(`UPDATE llm_accounts SET enabled = 1 WHERE id = 'staged-account'`); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "ONBOARDING_STAGING_INVALID",
		},
		{
			name: "model availability",
			mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
				t.Helper()
				if _, err := env.db.Exec(`UPDATE llm_models SET available = 0 WHERE provider_id = 'codex' AND model_id = 'gpt-5.6-terra'`); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "ONBOARDING_NO_MODEL",
		},
		{
			name: "state staging combo",
			mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
				t.Helper()
				if _, err := env.db.Exec(`UPDATE app_onboarding_state SET staged_combo_id = 'changed-combo' WHERE id = 1`); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "ONBOARDING_STAGING_INVALID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingTestChat(t, env, "codex", 31)
			entered := make(chan struct{})
			release := make(chan struct{})
			withAppOnboardingTestSeams(t,
				func(ctx context.Context, _ *api, _ store.OnboardingState, _ store.OnboardingStagingAccount, _ string) (string, error) {
					close(entered)
					select {
					case <-release:
						return "Xin chào, tôi là Bé Mi.", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				},
				time.Now,
				appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
				time.Second,
			)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":31,"message":"xin chào"}`)
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("runner was not entered")
			}

			tt.mutate(t, env)
			close(release)
			var rr *httptest.ResponseRecorder
			select {
			case rr = <-done:
			case <-time.After(time.Second):
				t.Fatal("test chat did not finish after runner release")
			}
			wantStatus := http.StatusConflict
			if tt.wantCode == "ONBOARDING_NO_MODEL" {
				wantStatus = http.StatusUnprocessableEntity
			}
			requireOnboardingCode(t, rr, wantStatus, tt.wantCode)
			if state := env.state(t); state.TestNonceHash != "" || state.TestExpiresAt != "" {
				t.Fatalf("postflight mutation persisted receipt: %+v", state)
			}
		})
	}
}

func TestAppOnboardingProviderStaleRevisionDoesNotCancelRunningTest(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingTestChat(t, env, "codex", 33)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRunner := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRunner()
	withAppOnboardingTestSeams(t,
		func(ctx context.Context, _ *api, _ store.OnboardingState, _ store.OnboardingStagingAccount, _ string) (string, error) {
			close(entered)
			select {
			case <-ctx.Done():
				close(canceled)
				return "", ctx.Err()
			case <-release:
				return "Xin chào, tôi là Bé Mi.", nil
			}
		},
		time.Now,
		appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
		120*time.Second,
	)
	testDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		testDone <- env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":33,"message":"xin chào"}`)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("runner was not entered")
	}

	provider := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":32,"kind":"claude-code"}`)
	requireOnboardingCode(t, provider, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
	select {
	case <-canceled:
		t.Fatal("stale provider request canceled the valid running test")
	default:
	}

	releaseRunner()
	select {
	case rr := <-testDone:
		if rr.Code != http.StatusOK {
			t.Fatalf("test chat status=%d body=%s", rr.Code, rr.Body.String())
		}
		response := decodeOnboardingResponse[appOnboardingTestChatResponse](t, rr)
		if response.Revision != 34 || response.TestToken == "" {
			t.Fatalf("test chat receipt = %+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("test chat did not finish after runner release")
	}
	state := env.state(t)
	if state.Revision != 34 || state.ProviderKind != "" || state.TestNonceHash == "" || state.TestExpiresAt == "" {
		t.Fatalf("stale provider request changed or suppressed valid receipt: %+v", state)
	}
}

// appOnboardingBeginRejectsTestPhaseWithoutCancellation captures the V8
// contract that replaced the old Provider-transition fence: begin is a small
// phase-gated Store mutation and never owns Test Chat cancellation.
func appOnboardingBeginRejectsTestPhaseWithoutCancellation(t *testing.T) {
	t.Helper()
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseTest, StagedComboID: "staged-combo",
		PersonaFingerprint: "fingerprint", Revision: 37,
	})
	before := env.state(t)

	cancelCalls := 0
	onboardingTestMu.Lock()
	oldActive := onboardingTestActive
	oldDone := onboardingTestDone
	oldCancel := onboardingTestCancel
	oldCancelRequested := onboardingTestCancelRequested
	onboardingTestActive = true
	onboardingTestDone = make(chan struct{})
	onboardingTestCancel = func() { cancelCalls++ }
	onboardingTestCancelRequested = false
	onboardingTestMu.Unlock()
	t.Cleanup(func() {
		onboardingTestMu.Lock()
		onboardingTestActive = oldActive
		onboardingTestDone = oldDone
		onboardingTestCancel = oldCancel
		onboardingTestCancelRequested = oldCancelRequested
		onboardingTestMu.Unlock()
	})

	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":37,"kind":"codex"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if cancelCalls != 0 {
		t.Fatalf("rejected begin requested Test Chat cancellation %d times", cancelCalls)
	}
	if after := env.state(t); after != before {
		t.Fatalf("rejected begin mutated Test state: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingProviderRejectsTestPhaseWithoutCancellation(t *testing.T) {

	appOnboardingBeginRejectsTestPhaseWithoutCancellation(t)
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
		appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
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
		random    appOnboardingTestRandomFunc
		failStore bool
		wantCode  string
	}{
		{name: "random", random: appOnboardingRandomFromReader(errorReader{err: errors.New("RANDOM_SECRET")}), wantCode: "ONBOARDING_TEST_TOKEN_FAILED"},
		{name: "store", random: appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))), failStore: true, wantCode: "ONBOARDING_STATE_UNAVAILABLE"},
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

func TestAppOnboardingTestChatTimeoutWhileRandomnessBlocksDoesNotPersist(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingTestChat(t, env, "codex", 27)
	before := env.state(t)
	randomEntered := make(chan struct{})
	randomCanceled := make(chan error, 1)
	withAppOnboardingTestSeams(t,
		func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
			return "Xin chào, tôi là Bé Mi.", nil
		},
		time.Now,
		func(ctx context.Context, _ []byte) error {
			close(randomEntered)
			<-ctx.Done()
			randomCanceled <- ctx.Err()
			return ctx.Err()
		},
		10*time.Millisecond,
	)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":27,"message":"xin chào"}`)
	}()
	select {
	case <-randomEntered:
	case <-time.After(time.Second):
		t.Fatal("randomness seam was not entered")
	}
	var rr *httptest.ResponseRecorder
	select {
	case rr = <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return after timeout while randomness was blocked")
	}
	requireOnboardingCode(t, rr, http.StatusGatewayTimeout, "ONBOARDING_TEST_TIMEOUT")
	select {
	case err := <-randomCanceled:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("randomness context error = %v; want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("randomness seam did not observe timeout")
	}
	if after := env.state(t); after != before {
		t.Fatalf("randomness timeout changed state: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingTestChatCancellationBeforeStoreDoesNotPersist(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingTestChat(t, env, "codex", 28)
	before := env.state(t)
	ctx, cancel := context.WithCancel(context.Background())
	withAppOnboardingTestSeams(t,
		func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
			return "Xin chào, tôi là Bé Mi.", nil
		},
		time.Now,
		func(_ context.Context, dst []byte) error {
			copy(dst, bytes.Repeat([]byte{0x7a}, len(dst)))
			cancel()
			return nil
		},
		time.Second,
	)
	req := httptest.NewRequest(http.MethodPost, "/onboarding/test-chat", strings.NewReader(`{"revision":28,"message":"xin chào"}`)).WithContext(ctx)

	rr := env.serveRequest(req)
	requireOnboardingCode(t, rr, http.StatusRequestTimeout, "ONBOARDING_REQUEST_CANCELED")
	if after := env.state(t); after != before {
		t.Fatalf("cancellation before Store changed state: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingTestChatCancellationAfterCommitCompensatesReceiptAndAllowsRetry(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	state, _ := seedAppOnboardingTestChat(t, env, "codex", 29)
	ctx, cancel := context.WithCancel(context.Background())
	withAppOnboardingTestSeams(t,
		func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error) {
			return "Xin chào, tôi là Bé Mi.", nil
		},
		func() time.Time { return time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC) },
		appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
		time.Second,
	)
	appOnboardingTestAfterCommit = cancel
	req := httptest.NewRequest(http.MethodPost, "/onboarding/test-chat", strings.NewReader(`{"revision":29,"message":"xin chào"}`)).WithContext(ctx)

	rr := env.serveRequest(req)
	if rr.Body.Len() != 0 || len(rr.Header()) != 0 {
		t.Fatalf("canceled client received post-commit response: headers=%v body=%s", rr.Header(), rr.Body.String())
	}
	after := env.state(t)
	if after.Revision != state.Revision+2 || after.TestNonceHash != "" || after.TestExpiresAt != "" {
		t.Fatalf("post-commit cancellation left an orphan receipt: before=%+v after=%+v", state, after)
	}
	status := env.serve(http.MethodGet, "/onboarding/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("status after compensation=%d body=%s", status.Code, status.Body.String())
	}
	current := decodeOnboardingResponse[appOnboardingStatusResponse](t, status)
	if current.Revision != after.Revision {
		t.Fatalf("status revision = %d; want compensated revision %d", current.Revision, after.Revision)
	}
	appOnboardingTestAfterCommit = func() {}
	appOnboardingTestRandom = appOnboardingRandomFromReader(bytes.NewReader(bytes.Repeat([]byte{0x33}, 32)))
	retry := env.serve(http.MethodPost, "/onboarding/test-chat",
		fmt.Sprintf(`{"revision":%d,"message":"xin chào lần nữa"}`, current.Revision))
	if retry.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	retried := env.state(t)
	if retried.Revision != current.Revision+1 || retried.TestNonceHash == "" || retried.TestExpiresAt == "" {
		t.Fatalf("retry did not persist a fresh receipt: %+v", retried)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func appOnboardingRandomFromReader(r io.Reader) appOnboardingTestRandomFunc {
	return func(ctx context.Context, dst []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.ReadFull(r, dst); err != nil {
			return err
		}
		return ctx.Err()
	}
}

func withAppOnboardingTestSeams(
	t *testing.T,
	execute any,
	now func() time.Time,
	random appOnboardingTestRandomFunc,
	timeout time.Duration,
) {
	t.Helper()
	oldExecute := appOnboardingTestExecute
	oldNow := appOnboardingTestNow
	oldRandom := appOnboardingTestRandom
	oldTimeout := appOnboardingTestTimeout
	oldAfterCommit := appOnboardingTestAfterCommit
	switch execute := execute.(type) {
	case appOnboardingTestExecutor:
		appOnboardingTestExecute = execute
	case func(context.Context, *api, store.OnboardingTestRoute, string) (appOnboardingTestResult, error):
		appOnboardingTestExecute = appOnboardingTestExecutor(execute)
	case func(context.Context, *api, store.OnboardingState, store.OnboardingStagingAccount, string) (string, error):
		appOnboardingTestExecute = func(
			ctx context.Context,
			a *api,
			route store.OnboardingTestRoute,
			prompt string,
		) (appOnboardingTestResult, error) {
			if len(route.Entries) == 0 {
				return appOnboardingTestResult{}, store.ErrOnboardingInvalidStagingOwnership
			}
			entry := route.Entries[0]
			staged := store.OnboardingStagingAccount{
				AccountID: entry.AccountID, ProviderID: entry.ProviderID,
				ProviderKind: entry.Kind, ConfigDir: entry.ConfigDir,
			}
			answer, err := execute(ctx, a, route.State, staged, prompt)
			return appOnboardingTestResult{
				Answer: answer, ProviderID: entry.ProviderID,
				ModelID: entry.ModelID, Position: entry.Position,
			}, err
		}
	default:
		t.Fatalf("unsupported onboarding Test seam %T", execute)
	}
	appOnboardingTestNow = now
	appOnboardingTestRandom = random
	appOnboardingTestTimeout = timeout
	appOnboardingTestAfterCommit = func() {}
	t.Cleanup(func() {
		appOnboardingTestExecute = oldExecute
		appOnboardingTestNow = oldNow
		appOnboardingTestRandom = oldRandom
		appOnboardingTestTimeout = oldTimeout
		appOnboardingTestAfterCommit = oldAfterCommit
	})
}

func withAppOnboardingProviderAfterTestWait(t *testing.T, hook func()) {
	t.Helper()
	oldHook := appOnboardingProviderAfterTestWait
	appOnboardingProviderAfterTestWait = hook
	t.Cleanup(func() { appOnboardingProviderAfterTestWait = oldHook })
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
	modelID := map[string]string{"codex": "gpt-5.6-terra", "claude-code": "sonnet"}[kind]
	env.replaceProviderStages(t, appOnboardingProviderWire{
		Kind: kind, Status: "ready", ProviderID: kind,
		AccountID: "staged-account", ModelID: modelID, Position: 0,
	})
	const persona = "PERSONA-SECRET: nói ngắn gọn, chân thành."
	configureAgentPersonaForOnboardingTest(t, env, persona)
	if err := env.a.st.SetAgentDisplayName("Bé Mi"); err != nil {
		t.Fatal(err)
	}
	state := env.state(t)
	state.Phase = store.OnboardingPhaseTest
	state.ProviderKind = ""
	state.ProviderID = ""
	state.AccountID = ""
	state.ModelID = ""
	state.StagedComboID = appOnboardingCompletionComboIDForTest
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

func TestAgentCompleteFromTestEditsPersonaAndInvalidatesReceipt(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	state, _ := seedAppOnboardingTestChat(t, env, "codex", 20)
	original := []byte("Tên {{TEN_BOT}}.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Old"); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x6a}, 32))
	digest := sha256.Sum256([]byte(token))
	state.PersonaFingerprint = strings.Repeat("b", sha256.Size*2)
	state.TestNonceHash = fmt.Sprintf("%x", digest[:])
	state.TestExpiresAt = "2099-08-12T08:00:00Z"
	env.setState(t, state)

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{"TEN_BOT":"New"},"display_name":"New","require_complete":true,"onboarding_revision":20}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := decodeOnboardingResponse[struct {
		Ready              bool   `json:"ready"`
		DisplayName        string `json:"display_name"`
		OnboardingPhase    string `json:"onboarding_phase"`
		OnboardingRevision int64  `json:"onboarding_revision"`
	}](t, rr)
	if !response.Ready || response.DisplayName != "New" ||
		response.OnboardingPhase != store.OnboardingPhaseTest || response.OnboardingRevision != 21 {
		t.Fatalf("completion response = %+v", response)
	}
	wantPersona := []byte("Tên New.\n")
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, wantPersona) {
		t.Fatalf("persona = %q, %v; want %q", got, err, wantPersona)
	}
	state = env.state(t)
	if state.Phase != store.OnboardingPhaseTest || state.Revision != 21 ||
		state.PersonaFingerprint != framedAgentFingerprintForTest(wantPersona, "New") ||
		state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Fatalf("saved Test-phase persona state = %+v", state)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "New" {
		t.Fatalf("AgentDisplayName() = %q, %v", name, err)
	}

	oldReceipt := env.serve(http.MethodPost, "/onboarding/complete", fmt.Sprintf(
		`{"revision":21,"test_token":%q}`,
		token,
	))
	requireOnboardingCode(t, oldReceipt, http.StatusConflict, "ONBOARDING_TEST_REQUIRED")
}

func TestAgentCompleteFromTestUnchangedPersonaStillInvalidatesReceipt(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	state, _ := seedAppOnboardingTestChat(t, env, "codex", 30)
	original := []byte("Persona hoàn chỉnh.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Bot"); err != nil {
		t.Fatal(err)
	}
	fingerprint := framedAgentFingerprintForTest(original, "Bot")
	state.PersonaFingerprint = fingerprint
	state.TestNonceHash = strings.Repeat("ab", sha256.Size)
	state.TestExpiresAt = "2099-08-12T08:00:00Z"
	env.setState(t, state)

	rr := env.serve(http.MethodPut, "/agent",
		`{"values":{},"display_name":"Bot","require_complete":true,"onboarding_revision":30}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	state = env.state(t)
	if state.Phase != store.OnboardingPhaseTest || state.Revision != 31 ||
		state.PersonaFingerprint != fingerprint || state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Fatalf("unchanged Test-phase save = %+v", state)
	}
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("unchanged persona = %q, %v", got, err)
	}
	if _, err := os.Lstat(persona + ".agentdc-recovery"); !os.IsNotExist(err) {
		t.Fatalf("metadata-only save left recovery journal: %v", err)
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
		{name: "stale from test", phase: store.OnboardingPhaseTest, revision: 4, request: 3, status: http.StatusConflict, code: "ONBOARDING_REVISION_CONFLICT"},
		{name: "provider phase", phase: store.OnboardingPhaseProvider, revision: 4, request: 4, status: http.StatusConflict, code: "ONBOARDING_PHASE_INVALID"},
		{name: "setup phase", phase: store.OnboardingPhaseSetup, revision: 4, request: 4, status: http.StatusConflict, code: "ONBOARDING_PHASE_INVALID"},
		{name: "connect phase", phase: store.OnboardingPhaseConnect, revision: 4, request: 4, status: http.StatusConflict, code: "ONBOARDING_PHASE_INVALID"},
		{name: "completed phase", phase: store.OnboardingPhaseCompleted, revision: 4, request: 4, status: http.StatusConflict, code: "ONBOARDING_PHASE_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			persona := configureAgentPersonaForOnboardingTest(t, env, "Persona hoàn chỉnh.\n")
			if err := env.a.st.SetAgentDisplayName("Original"); err != nil {
				t.Fatal(err)
			}
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
			if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Original" {
				t.Fatalf("rejected completion changed display name to %q (%v)", name, err)
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

func TestAgentCompleteFromTestStoreFailureRestoresPersonaAndReceipt(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	original := []byte("Tên {{TEN_BOT}}.\n")
	persona := configureAgentPersonaForOnboardingTest(t, env, string(original))
	if err := env.a.st.SetAgentDisplayName("Original"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "old-fingerprint", TestNonceHash: strings.Repeat("ab", sha256.Size),
		TestExpiresAt: "2099-08-12T08:00:00Z", Revision: 12,
	})
	before := env.state(t)
	if _, err := env.db.Exec(`CREATE TRIGGER fail_agent_test_persona_cas
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'injected Test persona CAS failure'); END`); err != nil {
		t.Fatal(err)
	}

	rr := env.serve(http.MethodPut, "/agent", `{
"values":{"TEN_BOT":"Changed"},"display_name":"Changed","require_complete":true,"onboarding_revision":12}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s; want 500", rr.Code, rr.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("Store failure left persona %q (%v)", got, err)
	}
	if name, err := env.a.st.AgentDisplayName(); err != nil || name != "Original" {
		t.Fatalf("Store failure changed display name to %q (%v)", name, err)
	}
	if after := env.state(t); after != before {
		t.Fatalf("Store failure changed prior receipt: before=%+v after=%+v", before, after)
	}
	if _, err := os.Lstat(persona + ".agentdc-recovery"); !os.IsNotExist(err) {
		t.Fatalf("successful rollback left recovery journal: %v", err)
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

	rr := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":7,"kind":"codex","account_id":"staged-account"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingStatusWire](t, rr)
	if got.Revision != 8 || got.ProviderKind != "codex" || got.ProviderID != "codex" ||
		got.AccountID != "staged-account" || got.ModelID != "gpt-5.6-terra" ||
		got.Phase != store.OnboardingPhasePersona || len(got.Providers) != 1 ||
		got.Providers[0].Status != "ready" {
		t.Fatalf("setup response = %+v", got)
	}
	state := env.state(t)
	if state.Phase != store.OnboardingPhasePersona || state.Revision != 8 ||
		state.ProviderKind != "" || state.ProviderID != "" || state.AccountID != "" ||
		state.ModelID != "" || state.StagedComboID == "" {
		t.Fatalf("staged state = %+v; response = %+v", state, got)
	}
	if afterLive := appOnboardingLiveRoutingBytes(t, env); afterLive != beforeLive {
		t.Fatalf("setup changed live routing rows:\nbefore=%q\nafter=%q", beforeLive, afterLive)
	}
}

func TestAppOnboardingSetupUsesClaudePreferredModel(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingSetup(t, env, "claude-code", "claude-account", false, 3, nil)

	rr := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":3,"kind":"claude-code","account_id":"claude-account"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[map[string]any](t, rr)
	if got["model_id"] != "sonnet" || got["phase"] != store.OnboardingPhasePersona {
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

	rr := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":5,"kind":"codex","account_id":"fallback-account"}`)
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

	rr := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":4,"kind":"codex","account_id":"no-model-account"}`)
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
				fmt.Sprintf(`{"revision":6,"kind":"codex","account_id":%q}`, tt.requestAccount))
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
		{name: "stale", phase: store.OnboardingPhaseSetup, body: `{"revision":8,"kind":"codex","account_id":"staged-account"}`, code: "ONBOARDING_REVISION_CONFLICT"},
		{name: "wrong phase", phase: store.OnboardingPhaseConnect, body: `{"revision":9,"kind":"codex","account_id":"staged-account"}`, code: "ONBOARDING_PHASE_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingSetup(t, env, "codex", "staged-account", false, 9, []store.LLMModel{
				{ModelID: "gpt-5.6-terra", Available: true},
			})
			state := env.state(t)
			state.Phase = tt.phase
			if tt.phase == store.OnboardingPhaseConnect {
				state.ProviderID = ""
				state.AccountID = ""
			}
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
	body := `{"revision":12,"kind":"codex","account_id":"staged-account"}`
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

	mismatch := env.serve(http.MethodPost, "/onboarding/setup", `{"revision":12,"kind":"codex","account_id":"other-account"}`)
	requireOnboardingCode(t, mismatch, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
	if stateAfterMismatch := env.state(t); stateAfterMismatch != stateAfterFirst {
		t.Fatalf("mismatched retry changed state: first=%+v mismatch=%+v", stateAfterFirst, stateAfterMismatch)
	}
}

func TestAppOnboardingSetupStrictJSON(t *testing.T) {
	tests := []string{
		`{"revision":1,"kind":"codex","account_id":"account","extra":true}`,
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
	if _, err := env.db.Exec(`UPDATE llm_providers SET enabled = 0 WHERE id = ?`, kind); err != nil {
		t.Fatalf("disable staged Provider %q: %v", kind, err)
	}
	env.replaceProviderStages(t, appOnboardingProviderWire{
		Kind: kind, Status: "pending", Position: 0,
	})
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseConnect, ProviderKind: kind, Revision: revision - 1,
	})
	account := store.LLMAccount{
		ID: accountID, ProviderID: kind, Label: accountID,
		ConfigDir: accountConfigDir(env.dataDir, kind, accountID), Enabled: false,
	}
	if _, err := env.a.st.BindOnboardingAccount(revision-1, kind, account); err != nil {
		t.Fatalf("BindOnboardingAccount(%q) = %v", accountID, err)
	}
	if accountEnabled {
		if _, err := env.db.Exec(`UPDATE llm_accounts SET enabled = 1 WHERE id = ?`, accountID); err != nil {
			t.Fatalf("enable staged Account %q: %v", accountID, err)
		}
	}
	if err := os.MkdirAll(accountConfigDir(env.dataDir, kind, accountID), 0o700); err != nil {
		t.Fatalf("MkdirAll account config %q: %v", accountID, err)
	}
	for _, model := range models {
		model.ProviderID = kind
		model.Name = model.ModelID
		model.Source = store.LLMModelManual
		if err := env.a.st.AddLLMModel(model); err != nil {
			t.Fatalf("AddLLMModel(%q) = %v", model.ModelID, err)
		}
	}
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

const appOnboardingCompletionComboIDForTest = "5f9967c7-93cf-4ac8-9da8-f6a7c1af8801"

func TestAppOnboardingCompleteActivatesVerifiedReceiptWithExactPrivateResponse(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedOnboardingRoute(t, env, "live-provider", "openai", true)
	if _, err := env.db.Exec(`INSERT INTO llm_route_entries(position, provider_id, model_id, enabled)
VALUES (0, 'live-provider', 'model', 1)`); err != nil {
		t.Fatal(err)
	}
	token, now := seedAppOnboardingCompletion(t, env, 101, false)
	hash := sha256.Sum256([]byte(token))
	stateBefore, err := env.a.st.OnboardingState()
	if err != nil {
		t.Fatal(err)
	}
	wantBoundHash, err := hex.DecodeString(stateBefore.TestNonceHash)
	if err != nil {
		t.Fatal(err)
	}
	var comparedLeft, comparedRight []byte
	oldCompare := appOnboardingCompleteConstantTimeCompare
	appOnboardingCompleteConstantTimeCompare = func(left, right []byte) int {
		comparedLeft = slices.Clone(left)
		comparedRight = slices.Clone(right)
		return subtle.ConstantTimeCompare(left, right)
	}
	oldNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now }
	t.Cleanup(func() {
		appOnboardingCompleteConstantTimeCompare = oldCompare
		appOnboardingCompleteNow = oldNow
	})
	var logs syncLogBuffer
	env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := env.serve(http.MethodPost, "/onboarding/complete", fmt.Sprintf(
		`{"revision":101,"test_token":%q}`, token,
	))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	wantBody := `{"completed":true,"onboarding_version":1,"combo_id":"` + appOnboardingCompletionComboIDForTest + `"}`
	if strings.TrimSpace(rr.Body.String()) != wantBody {
		t.Fatalf("response = %q; want exact %q", strings.TrimSpace(rr.Body.String()), wantBody)
	}
	if !bytes.Equal(comparedLeft, wantBoundHash) || !bytes.Equal(comparedRight, wantBoundHash) ||
		len(comparedLeft) != sha256.Size || len(comparedRight) != sha256.Size {
		t.Fatalf("constant-time compare inputs = %x / %x; want route-bound SHA-256", comparedLeft, comparedRight)
	}
	state := env.state(t)
	if state.Phase != store.OnboardingPhaseCompleted || state.CompletedVersion != store.CurrentOnboardingVersion ||
		state.Revision != 102 || state.RestartInProgress || state.TestNonceHash != "" ||
		state.TestExpiresAt != "" || state.PersonaFingerprint != "" || state.StagedComboID != "" {
		t.Fatalf("completed state = %+v", state)
	}
	status := env.serve(http.MethodGet, "/onboarding/status", "")
	projected := decodeOnboardingResponse[appOnboardingStatusResponse](t, status)
	if projected.Required || projected.ProviderID != "codex" || projected.AccountID != "staged-account" ||
		projected.ModelID != "gpt-5.6-terra" {
		t.Fatalf("completed status projection = %+v", projected)
	}
	for _, forbidden := range []string{token, fmt.Sprintf("%x", hash[:]), "persona-fingerprint", accountConfigDir(env.dataDir, "codex", "staged-account")} {
		if strings.Contains(rr.Body.String(), forbidden) || strings.Contains(logs.String(), forbidden) {
			t.Fatal("completion leaked a private receipt/config value in response or logs")
		}
	}
	combos, err := env.a.st.LLMCombos()
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) != 1 || combos[0].ID != appOnboardingCompletionComboIDForTest ||
		!combos[0].Active || len(combos[0].Members) != 1 {
		t.Fatalf("completed live combo = %+v", combos)
	}
}

func TestAppOnboardingCompleteRejectsReceiptAndConfigurationDriftWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name     string
		body     func(string) string
		mutate   func(*onboardingRouteTestEnv, time.Time)
		wantCode string
	}{
		{name: "stale revision", wantCode: "ONBOARDING_REVISION_CONFLICT", body: func(token string) string { return fmt.Sprintf(`{"revision":100,"test_token":%q}`, token) }},
		{name: "missing receipt", wantCode: "ONBOARDING_TEST_REQUIRED", mutate: func(env *onboardingRouteTestEnv, _ time.Time) {
			_, _ = env.db.Exec(`UPDATE app_onboarding_state SET test_nonce_hash = ''`)
		}},
		{name: "expired receipt", wantCode: "ONBOARDING_TEST_EXPIRED", mutate: func(env *onboardingRouteTestEnv, now time.Time) {
			_, _ = env.db.Exec(`UPDATE app_onboarding_state SET test_expires_at = ?`, now.Format(time.RFC3339Nano))
		}},
		{name: "wrong token", wantCode: "ONBOARDING_TEST_REQUIRED", body: func(string) string {
			return `{"revision":101,"test_token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
		}},
		{name: "malformed token", wantCode: "ONBOARDING_TEST_REQUIRED", body: func(string) string { return `{"revision":101,"test_token":"not_base64=="}` }},
		{name: "wrong phase", wantCode: "ONBOARDING_TEST_REQUIRED", mutate: func(env *onboardingRouteTestEnv, _ time.Time) {
			_, _ = env.db.Exec(`UPDATE app_onboarding_state SET phase = 'persona'`)
		}},
		{name: "persona direct mutation", wantCode: "ONBOARDING_CONFIGURATION_CHANGED", mutate: func(env *onboardingRouteTestEnv, _ time.Time) {
			_ = os.WriteFile(env.a.zalo.cfg.PersonaPath, []byte("changed persona"), 0o600)
		}},
		{name: "name direct mutation", wantCode: "ONBOARDING_CONFIGURATION_CHANGED", mutate: func(env *onboardingRouteTestEnv, _ time.Time) { _ = env.a.st.SetAgentDisplayName("Tên khác") }},
		{name: "config direct mutation", wantCode: "ONBOARDING_CONFIGURATION_CHANGED", mutate: func(env *onboardingRouteTestEnv, _ time.Time) {
			_, _ = env.db.Exec(`UPDATE llm_accounts SET config_dir = ? WHERE id = 'staged-account'`, t.TempDir())
		}},
		{name: "account enabled drift", wantCode: "ONBOARDING_CONFIGURATION_CHANGED", mutate: func(env *onboardingRouteTestEnv, _ time.Time) {
			_, _ = env.db.Exec(`UPDATE llm_accounts SET enabled = 1 WHERE id = 'staged-account'`)
		}},
		{name: "model drift", wantCode: "ONBOARDING_CONFIGURATION_CHANGED", mutate: func(env *onboardingRouteTestEnv, _ time.Time) {
			_, _ = env.db.Exec(`UPDATE llm_models SET available = 0 WHERE provider_id = 'codex'`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			token, now := seedAppOnboardingCompletion(t, env, 101, false)
			if tt.mutate != nil {
				tt.mutate(env, now)
			}
			beforeState := env.state(t)
			beforeSideEffects := appOnboardingTestSideEffectDigest(t, env)
			oldNow := appOnboardingCompleteNow
			appOnboardingCompleteNow = func() time.Time { return now }
			t.Cleanup(func() { appOnboardingCompleteNow = oldNow })
			body := fmt.Sprintf(`{"revision":101,"test_token":%q}`, token)
			if tt.body != nil {
				body = tt.body(token)
			}
			rr := env.serve(http.MethodPost, "/onboarding/complete", body)
			requireOnboardingCode(t, rr, http.StatusConflict, tt.wantCode)
			if after := env.state(t); after != beforeState {
				t.Fatalf("rejected completion changed state: before=%+v after=%+v", beforeState, after)
			}
			if after := appOnboardingTestSideEffectDigest(t, env); after != beforeSideEffects {
				t.Fatalf("rejected completion changed live data:\nbefore=%s\nafter=%s", beforeSideEffects, after)
			}
		})
	}
}

func TestAppOnboardingCompleteStrictJSONAndPositiveRevision(t *testing.T) {
	tests := []struct {
		name, body, code string
		status           int
	}{
		{name: "unknown field", body: `{"revision":1,"test_token":"x","extra":true}`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
		{name: "trailing JSON", body: `{"revision":1,"test_token":"x"}{}`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
		{name: "zero revision", body: `{"revision":0,"test_token":"x"}`, status: http.StatusUnprocessableEntity, code: "ONBOARDING_REVISION_INVALID"},
		{name: "negative revision", body: `{"revision":-1,"test_token":"x"}`, status: http.StatusUnprocessableEntity, code: "ONBOARDING_REVISION_INVALID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			before := env.state(t)
			rr := env.serve(http.MethodPost, "/onboarding/complete", tt.body)
			requireOnboardingCode(t, rr, tt.status, tt.code)
			if after := env.state(t); after != before {
				t.Fatalf("invalid request changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestAppOnboardingCompleteCommitFailureRollsBackAndIsPrivate(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	token, now := seedAppOnboardingCompletion(t, env, 111, true)
	beforeState := env.state(t)
	beforeSideEffects := appOnboardingTestSideEffectDigest(t, env)
	if _, err := env.db.Exec(`CREATE TRIGGER fail_onboarding_complete_combo
BEFORE INSERT ON llm_combos BEGIN SELECT RAISE(ABORT, 'COMMIT_SECRET'); END`); err != nil {
		t.Fatal(err)
	}
	var logs syncLogBuffer
	env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	oldNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now }
	t.Cleanup(func() { appOnboardingCompleteNow = oldNow })

	rr := env.serve(http.MethodPost, "/onboarding/complete", fmt.Sprintf(`{"revision":111,"test_token":%q}`, token))
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_COMMIT_FAILED")
	if strings.Contains(rr.Body.String(), "COMMIT_SECRET") || strings.Contains(rr.Body.String(), token) {
		t.Fatalf("commit error leaked internals: %s", rr.Body.String())
	}
	if after := env.state(t); after != beforeState {
		t.Fatalf("failed commit changed state: before=%+v after=%+v", beforeState, after)
	}
	if after := appOnboardingTestSideEffectDigest(t, env); after != beforeSideEffects {
		t.Fatalf("failed commit changed live data:\nbefore=%s\nafter=%s", beforeSideEffects, after)
	}
}

func TestAppOnboardingCompleteRouteDatabaseFailureIsPrivateServerError(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	token, now := seedAppOnboardingCompletion(t, env, 116, false)
	before := env.state(t)
	if _, err := env.db.Exec(`DROP TABLE llm_models`); err != nil {
		t.Fatal(err)
	}
	oldNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now }
	t.Cleanup(func() { appOnboardingCompleteNow = oldNow })

	rr := env.serve(http.MethodPost, "/onboarding/complete",
		fmt.Sprintf(`{"revision":116,"test_token":%q}`, token))
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_COMMIT_FAILED")
	for _, private := range []string{
		token,
		"no such table",
		"llm_models",
		"staged-account",
		accountConfigDir(env.dataDir, "codex", "staged-account"),
	} {
		if strings.Contains(rr.Body.String(), private) {
			t.Fatalf("route DB failure response leaked %q: %s", private, rr.Body.String())
		}
	}
	if after := env.state(t); after != before {
		t.Fatalf("route DB read failure changed onboarding state: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingCompleteAllowsOneConcurrentWinner(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	token, now := seedAppOnboardingCompletion(t, env, 121, false)
	oldNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now }
	t.Cleanup(func() { appOnboardingCompleteNow = oldNow })
	body := fmt.Sprintf(`{"revision":121,"test_token":%q}`, token)
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			<-start
			responses <- env.serve(http.MethodPost, "/onboarding/complete", body)
		}()
	}
	close(start)
	first, second := <-responses, <-responses
	codes := []int{first.Code, second.Code}
	slices.Sort(codes)
	if !slices.Equal(codes, []int{http.StatusOK, http.StatusConflict}) {
		t.Fatalf("concurrent completion statuses = %v; bodies=%s / %s", codes, first.Body.String(), second.Body.String())
	}
}

func TestAppOnboardingCompleteFencesActiveAndNewTestChat(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	token, now := seedAppOnboardingCompletion(t, env, 131, false)
	oldNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now }
	t.Cleanup(func() { appOnboardingCompleteNow = oldNow })
	entered := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	withAppOnboardingTestSeams(t,
		func(ctx context.Context, _ *api, _ store.OnboardingState, _ store.OnboardingStagingAccount, _ string) (string, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return "", ctx.Err()
		},
		func() time.Time { return now },
		appOnboardingRandomFromReader(bytes.NewReader(make([]byte, 32))),
		time.Second,
	)
	testDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		testDone <- env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":131,"message":"xin chào"}`)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("active Test Chat did not enter runner")
	}
	completeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completeDone <- env.serve(http.MethodPost, "/onboarding/complete", fmt.Sprintf(
			`{"revision":131,"test_token":%q}`, token,
		))
	}()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Complete did not cancel active Test Chat")
	}
	blocked := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":131,"message":"xin chào"}`)
	requireOnboardingCode(t, blocked, http.StatusConflict, "ONBOARDING_TEST_BUSY")
	close(release)
	select {
	case rr := <-testDone:
		requireOnboardingCode(t, rr, http.StatusRequestTimeout, "ONBOARDING_REQUEST_CANCELED")
	case <-time.After(time.Second):
		t.Fatal("canceled Test Chat did not finish")
	}
	select {
	case rr := <-completeDone:
		if rr.Code != http.StatusOK {
			t.Fatalf("completion status=%d body=%s", rr.Code, rr.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("completion did not finish after Test Chat cleanup")
	}
}

func seedAppOnboardingCompletion(
	t *testing.T,
	env *onboardingRouteTestEnv,
	revision int64,
	restart bool,
) (string, time.Time) {
	t.Helper()
	state, _ := seedAppOnboardingTestChat(t, env, "codex", revision)
	token := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	digest := sha256.Sum256([]byte(token))
	now := time.Date(2026, 8, 11, 12, 0, 0, 123, time.UTC)
	state.StagedComboID = appOnboardingCompletionComboIDForTest
	state.TestNonceHash = fmt.Sprintf("%x", digest[:])
	state.TestExpiresAt = now.Add(time.Minute).Format(time.RFC3339Nano)
	state.RestartInProgress = restart
	if restart {
		state.CompletedVersion = store.CurrentOnboardingVersion
	}
	env.setState(t, state)
	route, err := env.a.st.OnboardingTestRoute(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	boundHash, err := store.OnboardingTestReceiptHash(fmt.Sprintf("%x", digest[:]), route.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	state.TestNonceHash = boundHash
	env.setState(t, state)
	return token, now
}
