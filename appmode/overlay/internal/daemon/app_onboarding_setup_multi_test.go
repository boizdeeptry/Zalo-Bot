package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"agentdc/internal/store"
)

func TestAppOnboardingSetupMultiReturnsStandardStatusAndRetriesWithoutRediscovery(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	selected, err := env.a.st.ReplaceOnboardingProviderSelection(1, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	codexBase, codexAccount := bindAppOnboardingMultiKindForTest(
		t, env, selected.State.Revision, "codex", "codex-account",
	)
	addAppOnboardingMultiModelForTest(t, env, "codex", "gpt-5.6-terra", true)

	oldSelect := appSelectOnboardingModel
	discoveryCalls := 0
	appSelectOnboardingModel = func(st *store.Store, kind, providerID string) (string, error) {
		discoveryCalls++
		return selectOnboardingModel(st, kind, providerID)
	}
	t.Cleanup(func() { appSelectOnboardingModel = oldSelect })
	body := fmt.Sprintf(`{"revision":%d,"kind":"codex","account_id":%q}`, codexBase, codexAccount.ID)
	first := env.serve(http.MethodPost, "/onboarding/setup", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first setup status=%d body=%s", first.Code, first.Body.String())
	}
	firstStatus := decodeOnboardingResponse[appOnboardingStatusWire](t, first)
	if firstStatus.Phase != store.OnboardingPhaseProvider || firstStatus.Revision != codexBase+1 ||
		firstStatus.ProviderKind != "" || firstStatus.ProviderID != "" ||
		firstStatus.AccountID != "" || firstStatus.ModelID != "" {
		t.Fatalf("first setup status = %+v", firstStatus)
	}
	assertOnboardingProviderWireKinds(t, firstStatus.Providers, []string{"codex", "claude-code"})
	assertAppOnboardingProviderWireForTest(t, firstStatus.Providers[0], "codex", "ready", codexAccount.ID, "gpt-5.6-terra")
	assertAppOnboardingProviderWireForTest(t, firstStatus.Providers[1], "claude-code", "pending", "", "")
	assertAppOnboardingSetupStandardAndPrivateForTest(t, first.Body.String(), codexAccount)
	if discoveryCalls != 1 {
		t.Fatalf("normal setup discovery calls = %d; want 1", discoveryCalls)
	}

	retry := env.serve(http.MethodPost, "/onboarding/setup", body)
	if retry.Code != http.StatusOK {
		t.Fatalf("setup retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	if retryStatus := decodeOnboardingResponse[appOnboardingStatusWire](t, retry); retryStatus.Revision != firstStatus.Revision ||
		!slices.Equal(retryStatus.Providers, firstStatus.Providers) {
		t.Fatalf("retry status = %+v; want %+v", retryStatus, firstStatus)
	}
	if discoveryCalls != 1 {
		t.Fatalf("lost-response retry rediscovered model: calls=%d; want 1", discoveryCalls)
	}

	claudeBase, claudeAccount := bindAppOnboardingMultiKindForTest(
		t, env, firstStatus.Revision, "claude-code", "claude-account",
	)
	addAppOnboardingMultiModelForTest(t, env, "claude-code", "sonnet", true)
	last := env.serve(http.MethodPost, "/onboarding/setup",
		fmt.Sprintf(`{"revision":%d,"kind":"claude-code","account_id":%q}`, claudeBase, claudeAccount.ID))
	if last.Code != http.StatusOK {
		t.Fatalf("last setup status=%d body=%s", last.Code, last.Body.String())
	}
	lastStatus := decodeOnboardingResponse[appOnboardingStatusWire](t, last)
	if lastStatus.Phase != store.OnboardingPhasePersona || lastStatus.Revision != claudeBase+1 {
		t.Fatalf("last setup status = %+v", lastStatus)
	}
	assertOnboardingProviderWireKinds(t, lastStatus.Providers, []string{"codex", "claude-code"})
	assertAppOnboardingProviderWireForTest(t, lastStatus.Providers[0], "codex", "ready", codexAccount.ID, "gpt-5.6-terra")
	assertAppOnboardingProviderWireForTest(t, lastStatus.Providers[1], "claude-code", "ready", claudeAccount.ID, "sonnet")
	assertAppOnboardingSetupStandardAndPrivateForTest(t, last.Body.String(), claudeAccount)
}

func TestAppOnboardingSetupMultiStrictRequestAndMismatchHaveNoSideEffects(t *testing.T) {
	tests := []struct {
		name      string
		body      func(int64, string) string
		status    int
		code      string
		withModel bool
	}{
		{
			name: "missing kind",
			body: func(revision int64, accountID string) string {
				return fmt.Sprintf(`{"revision":%d,"account_id":%q}`, revision, accountID)
			},
			status: http.StatusUnprocessableEntity, code: "ONBOARDING_PROVIDER_UNSUPPORTED", withModel: true,
		},
		{
			name: "unsupported kind",
			body: func(revision int64, accountID string) string {
				return fmt.Sprintf(`{"revision":%d,"kind":"future-runtime","account_id":%q}`, revision, accountID)
			},
			status: http.StatusUnprocessableEntity, code: "ONBOARDING_PROVIDER_UNSUPPORTED", withModel: true,
		},
		{
			name: "unknown field",
			body: func(revision int64, accountID string) string {
				return fmt.Sprintf(`{"revision":%d,"kind":"codex","account_id":%q,"extra":true}`, revision, accountID)
			},
			status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID", withModel: true,
		},
		{
			name: "duplicate kind",
			body: func(revision int64, accountID string) string {
				return fmt.Sprintf(`{"revision":%d,"kind":"codex","kind":"claude-code","account_id":%q}`, revision, accountID)
			},
			status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID", withModel: true,
		},
		{
			name: "active kind mismatch",
			body: func(revision int64, accountID string) string {
				return fmt.Sprintf(`{"revision":%d,"kind":"claude-code","account_id":%q}`, revision, accountID)
			},
			status: http.StatusConflict, code: "ONBOARDING_STAGING_INVALID", withModel: true,
		},
		{
			name: "no model",
			body: func(revision int64, accountID string) string {
				return fmt.Sprintf(`{"revision":%d,"kind":"codex","account_id":%q}`, revision, accountID)
			},
			status: http.StatusUnprocessableEntity, code: "ONBOARDING_NO_MODEL",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			selected, err := env.a.st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
			if err != nil {
				t.Fatal(err)
			}
			base, account := bindAppOnboardingMultiKindForTest(t, env, selected.State.Revision, "codex", "codex-account")
			if test.withModel {
				addAppOnboardingMultiModelForTest(t, env, "codex", "gpt-5.6-terra", true)
			}
			before, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			rr := env.serve(http.MethodPost, "/onboarding/setup", test.body(base, account.ID))
			requireOnboardingCode(t, rr, test.status, test.code)
			after, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if before.State != after.State || !slices.Equal(before.Stages, after.Stages) {
				t.Fatalf("rejected setup mutated snapshot: before=%+v after=%+v", before, after)
			}
			if strings.Contains(rr.Body.String(), "future-runtime") || strings.Contains(rr.Body.String(), account.ConfigDir) {
				t.Fatalf("setup error leaked request/private data: %s", rr.Body.String())
			}
		})
	}
}

func TestConnectOnboardingMultiAdmissionUsesSelectedSnapshotAndAllowsRetainedFingerprint(t *testing.T) {
	t.Run("retained fingerprint", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		selected, err := env.a.st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
		if err != nil {
			t.Fatal(err)
		}
		connected, err := env.a.st.BeginOnboardingProvider(selected.State.Revision, "codex")
		if err != nil {
			t.Fatal(err)
		}
		state := connected.State
		state.PersonaFingerprint = "saved-persona"
		env.setState(t, state)
		connectMgr = newTestManager(t, &fakeRunner{installed: true, auth: authUnknown},
			func(string) error { return nil }, func(store.LLMAccount) error { return nil })
		connectMgr.ensureModels = func(string) error { return nil }

		rr := env.serve(http.MethodPost, "/llm/providers/codex/connect",
			fmt.Sprintf(`{"onboarding_revision":%d}`, state.Revision))
		if rr.Code != http.StatusOK {
			t.Fatalf("connect status=%d body=%s", rr.Code, rr.Body.String())
		}
		connectMgr.cancel("codex")
		waitConnectJobDone(t, connectMgr)
	})

	t.Run("missing selected stage", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		selected, err := env.a.st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
		if err != nil {
			t.Fatal(err)
		}
		connected, err := env.a.st.BeginOnboardingProvider(selected.State.Revision, "codex")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.db.Exec(`DELETE FROM app_onboarding_provider_stages WHERE kind = 'codex'`); err != nil {
			t.Fatal(err)
		}
		connectMgr = newTestManager(t, &fakeRunner{installed: true, auth: authUnknown},
			func(string) error { return nil }, func(store.LLMAccount) error { return nil })

		rr := env.serve(http.MethodPost, "/llm/providers/codex/connect",
			fmt.Sprintf(`{"onboarding_revision":%d}`, connected.State.Revision))
		requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
		if connectMgr.job != nil {
			t.Fatal("Connect manager started without selected pending target stage")
		}
	})
}

func bindAppOnboardingMultiKindForTest(
	t *testing.T,
	env *onboardingRouteTestEnv,
	revision int64,
	kind string,
	accountID string,
) (int64, store.LLMAccount) {
	t.Helper()
	connected, err := env.a.st.BeginOnboardingProvider(revision, kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.EnsureOnboardingProviderForKind(kind); err != nil {
		t.Fatal(err)
	}
	addedAt := time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC)
	account := store.LLMAccount{
		ID: accountID, ProviderID: kind, Label: kind + " staging", Email: kind + "@example.test",
		ConfigDir: "D:/private/" + accountID, AddedAt: &addedAt,
	}
	bound, err := env.a.st.BindOnboardingAccount(connected.State.Revision, kind, account)
	if err != nil {
		t.Fatal(err)
	}
	return bound.State.Revision, account
}

func addAppOnboardingMultiModelForTest(
	t *testing.T,
	env *onboardingRouteTestEnv,
	kind string,
	modelID string,
	available bool,
) {
	t.Helper()
	if err := env.a.st.AddLLMModel(store.LLMModel{
		ProviderID: kind, ModelID: modelID, Name: modelID,
		Source: store.LLMModelDiscovered, Available: available,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertAppOnboardingProviderWireForTest(
	t *testing.T,
	provider appOnboardingProviderWire,
	kind string,
	status string,
	accountID string,
	modelID string,
) {
	t.Helper()
	providerID := ""
	if status == "ready" {
		providerID = kind
	}
	if provider.Kind != kind || provider.Status != status || provider.ProviderID != providerID ||
		provider.AccountID != accountID || provider.ModelID != modelID {
		t.Fatalf("provider = %+v; want kind=%q status=%q account=%q model=%q",
			provider, kind, status, accountID, modelID)
	}
}

func assertAppOnboardingSetupStandardAndPrivateForTest(
	t *testing.T,
	body string,
	account store.LLMAccount,
) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(body), &object); err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{"combo_name", "staged_combo_id", "persona_fingerprint", "test_nonce_hash"} {
		if _, exists := object[obsolete]; exists {
			t.Fatalf("standard setup response exposed %q: %s", obsolete, body)
		}
	}
	assertNoOnboardingPrivateKeys(t, object)
	for _, secret := range []string{account.ConfigDir, account.Email, onboardingProviderMutationReceiptKeyForDaemonTest} {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatalf("setup response leaked private value %q: %s", secret, body)
		}
	}
}

const onboardingProviderMutationReceiptKeyForDaemonTest = "onboarding_provider_mutation_receipt_v1"
