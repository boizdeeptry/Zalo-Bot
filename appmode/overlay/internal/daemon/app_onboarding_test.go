package daemon

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentdc/internal/store"
)

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
	fingerprintInput := append(append([]byte(nil), wantBytes...), []byte("An Nhiên")...)
	wantFingerprint := fmt.Sprintf("%x", sha256.Sum256(fingerprintInput))
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
