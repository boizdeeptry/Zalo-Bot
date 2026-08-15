package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentdc/internal/store"

	"github.com/google/uuid"
)

func TestAppOnboardingReadyRunnerUsesOrderedFallbackAndActualWinner(t *testing.T) {
	t.Run("first success skips second", func(t *testing.T) {
		var calls []string
		runner := newAppOnboardingReadyRunner("Bé Mi", []appOnboardingReadyMember{
			appOnboardingReadyMemberForTest(0, "codex", "codex-model", func(context.Context, string, func(string)) (string, error) {
				calls = append(calls, "codex")
				return "Xin chào, tôi là Bé Mi.", nil
			}),
			appOnboardingReadyMemberForTest(1, "claude-code", "claude-model", func(context.Context, string, func(string)) (string, error) {
				calls = append(calls, "claude-code")
				return "Xin chào, tôi là Bé Mi từ Provider thứ hai.", nil
			}),
		})

		got, err := runner.Run(context.Background(), "prompt", func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if got.Answer != "Xin chào, tôi là Bé Mi." || got.ProviderID != "codex" ||
			got.ModelID != "codex-model" || got.Position != 0 {
			t.Fatalf("result = %+v", got)
		}
		if !slices.Equal(calls, []string{"codex"}) {
			t.Fatalf("calls = %v; second member must not run", calls)
		}
	})

	t.Run("first failure falls through", func(t *testing.T) {
		var calls []string
		runner := newAppOnboardingReadyRunner("Bé Mi", []appOnboardingReadyMember{
			appOnboardingReadyMemberForTest(0, "codex", "codex-model", func(context.Context, string, func(string)) (string, error) {
				calls = append(calls, "codex")
				return "", errors.New("FIRST_MEMBER_PRIVATE_FAILURE")
			}),
			appOnboardingReadyMemberForTest(1, "claude-code", "claude-model", func(context.Context, string, func(string)) (string, error) {
				calls = append(calls, "claude-code")
				return "Xin chào, tôi là Bé Mi từ fallback.", nil
			}),
		})

		got, err := runner.Run(context.Background(), "prompt", func(string) {})
		if err != nil {
			t.Fatal(err)
		}
		if got.Answer != "Xin chào, tôi là Bé Mi từ fallback." || got.ProviderID != "claude-code" ||
			got.ModelID != "claude-model" || got.Position != 1 {
			t.Fatalf("fallback result = %+v", got)
		}
		if !slices.Equal(calls, []string{"codex", "claude-code"}) {
			t.Fatalf("calls = %v; want strict position order", calls)
		}
	})

	t.Run("all fail is sanitized", func(t *testing.T) {
		runner := newAppOnboardingReadyRunner("Bé Mi", []appOnboardingReadyMember{
			appOnboardingReadyMemberForTest(0, "codex", "codex-model", func(context.Context, string, func(string)) (string, error) {
				return "", errors.New("CODEX_PRIVATE_FAILURE")
			}),
			appOnboardingReadyMemberForTest(1, "claude-code", "claude-model", func(context.Context, string, func(string)) (string, error) {
				return "", errors.New("CLAUDE_PRIVATE_FAILURE")
			}),
		})
		_, err := runner.Run(context.Background(), "prompt", func(string) {})
		if err == nil {
			t.Fatal("all-failed runner unexpectedly succeeded")
		}
		if strings.Contains(err.Error(), "PRIVATE_FAILURE") {
			t.Fatalf("all-failed error leaked member details: %v", err)
		}
	})
}

func TestAppOnboardingReadyRunnerFallsThroughSemanticInvalidAnswers(t *testing.T) {
	for _, test := range []struct {
		name, firstAnswer string
	}{
		{name: "empty answer", firstAnswer: "  "},
		{name: "persona name missing", firstAnswer: "Xin chào bạn."},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			runner := newAppOnboardingReadyRunner("Bé Mi", []appOnboardingReadyMember{
				appOnboardingReadyMemberForTest(0, "codex", "codex-model", func(context.Context, string, func(string)) (string, error) {
					calls = append(calls, "codex")
					return test.firstAnswer, nil
				}),
				appOnboardingReadyMemberForTest(1, "claude-code", "claude-model", func(context.Context, string, func(string)) (string, error) {
					calls = append(calls, "claude-code")
					return "Xin chào, tôi là Bé Mi.", nil
				}),
			})

			got, err := runner.Run(context.Background(), "prompt", func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			if got.Answer != "Xin chào, tôi là Bé Mi." || got.ProviderID != "claude-code" ||
				got.ModelID != "claude-model" || got.Position != 1 {
				t.Fatalf("semantic fallback result = %+v", got)
			}
			if !slices.Equal(calls, []string{"codex", "claude-code"}) {
				t.Fatalf("semantic fallback calls = %v", calls)
			}
		})
	}
}

func TestAppOnboardingReadyRunnerReturnsCapturedSemanticInvalidAfterExhaustion(t *testing.T) {
	var calls []string
	runner := newAppOnboardingReadyRunner("Bé Mi", []appOnboardingReadyMember{
		appOnboardingReadyMemberForTest(0, "codex", "codex-model", func(context.Context, string, func(string)) (string, error) {
			calls = append(calls, "codex")
			return "  ", nil
		}),
		appOnboardingReadyMemberForTest(1, "claude-code", "claude-model", func(context.Context, string, func(string)) (string, error) {
			calls = append(calls, "claude-code")
			return "Xin chào nhưng không có tên bot.", nil
		}),
	})

	got, err := runner.Run(context.Background(), "prompt", func(string) {})
	if err != nil {
		t.Fatalf("semantic-invalid exhaustion error = %v; want captured result", err)
	}
	if got.Answer != "" || got.ProviderID != "codex" || got.ModelID != "codex-model" || got.Position != 0 {
		t.Fatalf("captured semantic-invalid result = %+v", got)
	}
	if !slices.Equal(calls, []string{"codex", "claude-code"}) {
		t.Fatalf("semantic-invalid exhaustion calls = %v; want every member", calls)
	}
}

func TestAppOnboardingReadyRunnerStopsImmediatelyOnParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var secondCalls atomic.Int32
	runner := newAppOnboardingReadyRunner("Bé Mi", []appOnboardingReadyMember{
		appOnboardingReadyMemberForTest(0, "codex", "codex-model", func(context.Context, string, func(string)) (string, error) {
			cancel()
			return "", context.Canceled
		}),
		appOnboardingReadyMemberForTest(1, "claude-code", "claude-model", func(context.Context, string, func(string)) (string, error) {
			secondCalls.Add(1)
			return "Xin chào, tôi là Bé Mi.", nil
		}),
	})

	if _, err := runner.Run(ctx, "prompt", func(string) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v; want context canceled", err)
	}
	if secondCalls.Load() != 0 {
		t.Fatalf("parent cancellation called second member %d times", secondCalls.Load())
	}
}

func TestAppOnboardingTestChatMultiReturnsActualFallbackIdentityAndBindsReceipt(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingMultiTestChatForTask6(t, env, 81)
	fixedNow := time.Date(2026, 8, 15, 11, 0, 0, 0, time.UTC)
	randomBytes := bytes.Repeat([]byte{0x5c}, sha256.Size)
	var captured store.OnboardingTestRoute
	withAppOnboardingTestResultSeams(t,
		func(_ context.Context, _ *api, route store.OnboardingTestRoute, prompt string) (appOnboardingTestResult, error) {
			captured = route
			if !strings.Contains(prompt, "xin chào") {
				t.Fatalf("prompt = %q; want request", prompt)
			}
			return appOnboardingTestResult{
				Answer: "Xin chào, tôi là Bé Mi.", ProviderID: "claude-code",
				ModelID: "claude-model", Position: 1,
			}, nil
		},
		func() time.Time { return fixedNow },
		appOnboardingRandomFromReader(bytes.NewReader(randomBytes)),
		time.Second,
	)
	beforeSideEffects := appOnboardingTestSideEffectDigest(t, env)

	rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":81,"message":"xin chào"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingTestChatResponse](t, rr)
	if got.ProviderID != "claude-code" || got.ModelID != "claude-model" || got.Position != 1 ||
		got.Answer != "Xin chào, tôi là Bé Mi." || got.Revision != 82 {
		t.Fatalf("response = %+v", got)
	}
	if len(captured.Entries) != 2 || captured.Entries[0].AccountID != "codex-account" ||
		captured.Entries[1].AccountID != "claude-account" {
		t.Fatalf("captured route = %+v", captured)
	}
	tokenHash := sha256.Sum256([]byte(got.TestToken))
	boundHash, err := store.OnboardingTestReceiptHash(hex.EncodeToString(tokenHash[:]), captured.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	state := env.state(t)
	if state.TestNonceHash != boundHash || state.Revision != 82 {
		t.Fatalf("persisted receipt = %+v; want bound hash %q", state, boundHash)
	}
	if afterSideEffects := appOnboardingTestSideEffectDigest(t, env); afterSideEffects != beforeSideEffects {
		t.Fatalf("multi Test Chat changed live/session/Memory/attempt state:\nbefore=%s\nafter=%s",
			beforeSideEffects, afterSideEffects)
	}
	for _, private := range []string{captured.Entries[0].ConfigDir, captured.Entries[1].ConfigDir, captured.Fingerprint} {
		if strings.Contains(rr.Body.String(), private) {
			t.Fatalf("response leaked private route metadata %q: %s", private, rr.Body.String())
		}
	}
}

func TestAppOnboardingTestChatMultiRefreshRetryReplacesReceipt(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingMultiTestChatForTask6(t, env, 86)
	fixedNow := time.Date(2026, 8, 15, 11, 30, 0, 0, time.UTC)
	randomBytes := append(bytes.Repeat([]byte{0x31}, sha256.Size), bytes.Repeat([]byte{0x42}, sha256.Size)...)
	var calls atomic.Int32
	withAppOnboardingTestResultSeams(t,
		func(_ context.Context, _ *api, _ store.OnboardingTestRoute, _ string) (appOnboardingTestResult, error) {
			calls.Add(1)
			return appOnboardingTestResult{
				Answer: "Xin chào, tôi là Bé Mi.", ProviderID: "codex",
				ModelID: "codex-model", Position: 0,
			}, nil
		},
		func() time.Time { return fixedNow },
		appOnboardingRandomFromReader(bytes.NewReader(randomBytes)),
		time.Second,
	)

	first := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":86,"message":"xin chào"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first Test Chat status=%d body=%s", first.Code, first.Body.String())
	}
	firstResponse := decodeOnboardingResponse[appOnboardingTestChatResponse](t, first)
	status := env.serve(http.MethodGet, "/onboarding/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("refreshed status=%d body=%s", status.Code, status.Body.String())
	}
	refreshed := decodeOnboardingResponse[appOnboardingStatusResponse](t, status)
	if refreshed.Revision != firstResponse.Revision {
		t.Fatalf("refreshed revision=%d want=%d", refreshed.Revision, firstResponse.Revision)
	}

	second := env.serve(http.MethodPost, "/onboarding/test-chat",
		fmt.Sprintf(`{"revision":%d,"message":"xin chào lần nữa"}`, refreshed.Revision))
	if second.Code != http.StatusOK {
		t.Fatalf("retry Test Chat status=%d body=%s", second.Code, second.Body.String())
	}
	secondResponse := decodeOnboardingResponse[appOnboardingTestChatResponse](t, second)
	if secondResponse.Revision != firstResponse.Revision+1 || secondResponse.TestToken == firstResponse.TestToken {
		t.Fatalf("retry response=%+v first=%+v", secondResponse, firstResponse)
	}
	if calls.Load() != 2 {
		t.Fatalf("runner calls=%d want=2", calls.Load())
	}
	secondTokenHash := sha256.Sum256([]byte(secondResponse.TestToken))
	route, err := env.a.st.OnboardingTestRoute(context.Background(), secondResponse.Revision)
	if err != nil {
		t.Fatal(err)
	}
	wantBound, err := store.OnboardingTestReceiptHash(hex.EncodeToString(secondTokenHash[:]), route.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if state := env.state(t); state.TestNonceHash != wantBound {
		t.Fatalf("persisted retry receipt=%+v want hash=%s", state, wantBound)
	}
}

func TestAppOnboardingTestChatMultiAllowsEnabledLiveProvider(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingMultiTestChatForTask6(t, env, 88)
	if _, err := env.db.Exec(`UPDATE llm_providers SET enabled=1 WHERE id='codex'`); err != nil {
		t.Fatal(err)
	}
	withAppOnboardingTestResultSeams(t,
		func(_ context.Context, _ *api, _ store.OnboardingTestRoute, _ string) (appOnboardingTestResult, error) {
			return appOnboardingTestResult{
				Answer: "Xin chào, tôi là Bé Mi.", ProviderID: "codex",
				ModelID: "codex-model", Position: 0,
			}, nil
		},
		time.Now,
		appOnboardingRandomFromReader(bytes.NewReader(bytes.Repeat([]byte{0x55}, sha256.Size))),
		time.Second,
	)

	rr := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":88,"message":"xin chào"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("enabled live Provider Test Chat status=%d body=%s", rr.Code, rr.Body.String())
	}
	if state := env.state(t); state.Revision != 89 || state.TestNonceHash == "" {
		t.Fatalf("enabled live Provider did not persist Test receipt: %+v", state)
	}
}

func TestAppOnboardingTestChatMultiCancellationCompensationDoesNotEraseNewerRetry(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingMultiTestChatForTask6(t, env, 89)
	fixedNow := time.Date(2026, 8, 15, 11, 45, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	withAppOnboardingTestResultSeams(t,
		func(_ context.Context, _ *api, _ store.OnboardingTestRoute, _ string) (appOnboardingTestResult, error) {
			return appOnboardingTestResult{
				Answer: "Xin chào, tôi là Bé Mi.", ProviderID: "codex",
				ModelID: "codex-model", Position: 0,
			}, nil
		},
		func() time.Time { return fixedNow },
		appOnboardingRandomFromReader(bytes.NewReader(bytes.Repeat([]byte{0x66}, sha256.Size))),
		time.Second,
	)

	var replacement store.OnboardingState
	var replacementErr error
	appOnboardingTestAfterCommit = func() {
		current := env.state(t)
		route, err := env.a.st.OnboardingTestRoute(context.Background(), current.Revision)
		if err != nil {
			replacementErr = err
			cancel()
			return
		}
		replacement, replacementErr = env.a.st.SaveOnboardingTestReceipt(
			context.Background(),
			route,
			strings.Repeat("77", sha256.Size),
			store.NewOnboardingTestReceiptPolicy(fixedNow.Add(time.Second)),
		)
		cancel()
	}
	req := httptest.NewRequest(http.MethodPost, "/onboarding/test-chat",
		strings.NewReader(`{"revision":89,"message":"xin chào"}`)).WithContext(ctx)

	rr := env.serveRequest(req)
	if replacementErr != nil {
		t.Fatalf("concurrent exact replacement failed: %v", replacementErr)
	}
	if rr.Body.Len() != 0 || len(rr.Header()) != 0 {
		t.Fatalf("canceled client received response: headers=%v body=%s", rr.Header(), rr.Body.String())
	}
	if current := env.state(t); current != replacement {
		t.Fatalf("cancellation compensation erased newer replacement: got=%+v want=%+v", current, replacement)
	}
}

func TestAppOnboardingTestChatMultiRejectsRouteDriftAfterRunner(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *onboardingRouteTestEnv)
	}{
		{name: "stage reorder", mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
			if _, err := env.db.Exec(`UPDATE app_onboarding_provider_stages SET position=position+8`); err != nil {
				t.Fatal(err)
			}
			if _, err := env.db.Exec(`UPDATE app_onboarding_provider_stages SET position=CASE kind WHEN 'codex' THEN 1 ELSE 0 END`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stage missing", mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
			if _, err := env.db.Exec(`DELETE FROM app_onboarding_provider_stages WHERE kind='claude-code'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stage model changed", mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
			if err := env.a.st.AddLLMModel(store.LLMModel{
				ProviderID: "codex", ModelID: "codex-model-new", Name: "codex-model-new",
				Source: store.LLMModelManual, Available: true,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := env.db.Exec(`UPDATE app_onboarding_provider_stages SET model_id='codex-model-new' WHERE kind='codex'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Provider name blank", mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
			if _, err := env.db.Exec(`UPDATE llm_providers SET name='' WHERE id='codex'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Account enabled", mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
			if _, err := env.db.Exec(`UPDATE llm_accounts SET enabled=1 WHERE id='codex-account'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "model unavailable", mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
			if _, err := env.db.Exec(`UPDATE llm_models SET available=0 WHERE provider_id='codex' AND model_id='codex-model'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "config changed", mutate: func(t *testing.T, env *onboardingRouteTestEnv) {
			if _, err := env.db.Exec(`UPDATE llm_accounts SET config_dir=? WHERE id='codex-account'`, t.TempDir()); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedAppOnboardingMultiTestChatForTask6(t, env, 91)
			entered := make(chan struct{})
			release := make(chan struct{})
			withAppOnboardingTestResultSeams(t,
				func(ctx context.Context, _ *api, _ store.OnboardingTestRoute, _ string) (appOnboardingTestResult, error) {
					close(entered)
					select {
					case <-release:
						return appOnboardingTestResult{
							Answer: "Xin chào, tôi là Bé Mi.", ProviderID: "codex",
							ModelID: "codex-model", Position: 0,
						}, nil
					case <-ctx.Done():
						return appOnboardingTestResult{}, ctx.Err()
					}
				},
				time.Now,
				appOnboardingRandomFromReader(bytes.NewReader(make([]byte, sha256.Size))),
				time.Second,
			)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":91,"message":"xin chào"}`)
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("runner did not start")
			}
			test.mutate(t, env)
			close(release)
			var rr *httptest.ResponseRecorder
			select {
			case rr = <-done:
			case <-time.After(time.Second):
				t.Fatal("handler did not return after route drift")
			}
			if rr.Code == http.StatusOK {
				t.Fatalf("route drift unexpectedly succeeded: %s", rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "codex-account") || strings.Contains(rr.Body.String(), "codex-model-new") {
				t.Fatalf("route drift error leaked private identity: %s", rr.Body.String())
			}
			var revision int64
			var nonceHash, expiresAt string
			if err := env.db.QueryRow(`
SELECT revision, test_nonce_hash, test_expires_at
FROM app_onboarding_state WHERE id=1`).Scan(&revision, &nonceHash, &expiresAt); err != nil {
				t.Fatal(err)
			}
			if nonceHash != "" || expiresAt != "" || revision != 91 {
				t.Fatalf(
					"route drift persisted receipt: revision=%d nonce=%q expires=%q",
					revision,
					nonceHash,
					expiresAt,
				)
			}
		})
	}
}

func appOnboardingReadyMemberForTest(
	position int,
	providerID string,
	modelID string,
	run func(context.Context, string, func(string)) (string, error),
) appOnboardingReadyMember {
	return appOnboardingReadyMember{
		Entry: store.OnboardingTestRouteEntry{
			Position: position, Kind: providerID, ProviderID: providerID,
			AccountID: providerID + "-account", ModelID: modelID,
		},
		Run: run,
	}
}

func withAppOnboardingTestResultSeams(
	t *testing.T,
	execute appOnboardingTestExecutor,
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
	appOnboardingTestExecute = execute
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

func seedAppOnboardingMultiTestChatForTask6(t *testing.T, env *onboardingRouteTestEnv, revision int64) {
	t.Helper()
	entries := []store.OnboardingTestRouteEntry{
		{Position: 0, Kind: "codex", ProviderID: "codex", AccountID: "codex-account", ModelID: "codex-model"},
		{Position: 1, Kind: "claude-code", ProviderID: "claude-code", AccountID: "claude-account", ModelID: "claude-model"},
	}
	stages := make([]appOnboardingProviderWire, 0, len(entries))
	for index := range entries {
		entry := &entries[index]
		if err := env.a.st.EnsureOnboardingProviderForKind(entry.Kind); err != nil {
			t.Fatal(err)
		}
		if _, err := env.db.Exec(`UPDATE llm_providers SET enabled=0 WHERE id=?`, entry.ProviderID); err != nil {
			t.Fatal(err)
		}
		entry.ConfigDir = accountConfigDir(env.dataDir, entry.Kind, entry.AccountID)
		if err := os.MkdirAll(entry.ConfigDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := env.a.st.CreateLLMAccount(store.LLMAccount{
			ID: entry.AccountID, ProviderID: entry.ProviderID, Label: entry.AccountID,
			ConfigDir: entry.ConfigDir, Enabled: false,
		}); err != nil {
			t.Fatal(err)
		}
		if err := env.a.st.AddLLMModel(store.LLMModel{
			ProviderID: entry.ProviderID, ModelID: entry.ModelID, Name: entry.ModelID,
			Source: store.LLMModelManual, Available: true,
		}); err != nil {
			t.Fatal(err)
		}
		stages = append(stages, appOnboardingProviderWire{
			Kind: entry.Kind, Status: "ready", ProviderID: entry.ProviderID,
			AccountID: entry.AccountID, ModelID: entry.ModelID, Position: entry.Position,
		})
	}
	env.replaceProviderStages(t, stages...)
	const persona = "PERSONA-SECRET: nói ngắn gọn, chân thành."
	configureAgentPersonaForOnboardingTest(t, env, persona)
	if err := env.a.st.SetAgentDisplayName("Bé Mi"); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseTest, StagedComboID: uuid.NewString(),
		PersonaFingerprint: agentPersonaFingerprint([]byte(persona), "Bé Mi"), Revision: revision,
	})
}

func TestAppLLMRouterPinnedFallbackRemainsIndependentFromOnboardingRunner(t *testing.T) {
	// Task 6 introduces a dedicated onboarding fallback runner. This compile-time
	// story test guards the live router's public constructor from being replaced
	// or taught to read staged Accounts.
	route := store.LLMRouteSnapshot{Type: "fallback", Entries: []store.LLMRouteEntry{{
		Position: 0, ProviderID: "live-provider", ModelID: "live-model", Enabled: true,
	}}}
	runner := newAppLLMRunner(appLLMRunnerConfig{
		Route: route, Store: appOnboardingNoopAttemptStore{},
		Adapters:   map[string]providerAdapter{"live-provider": okAdapter("live")},
		Credential: func(string) ([]byte, error) { return nil, nil },
		Claude:     func(string) zaloRunner { return silentZaloRunner{} },
	})
	answer, err := runner.Run(context.Background(), "prompt", func(string) {})
	if err != nil || answer != "live" {
		t.Fatalf("live router = %q, %v", answer, err)
	}
	if fmt.Sprintf("%T", runner) != "*daemon.appLLMRunner" {
		t.Fatalf("live runner type changed: %T", runner)
	}
}
