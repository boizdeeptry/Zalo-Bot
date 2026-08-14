package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentdc/internal/config"
	"agentdc/internal/store"
)

type onboardingRouteTestEnv struct {
	a       *api
	mux     *http.ServeMux
	db      *sql.DB
	dataDir string
}

func newOnboardingRouteTestEnv(t *testing.T) *onboardingRouteTestEnv {
	t.Helper()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "portal.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open(%s) = %v", dbPath, err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		_ = st.Close()
		t.Fatalf("sql.Open(%s) = %v", dbPath, err)
	}
	raw.SetMaxOpenConns(1)
	a := &api{
		cfg:        config.Config{Dir: dataDir},
		st:         st,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		portalOpen: true,
	}
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)
	t.Cleanup(func() {
		connectMgr = nil
		_ = raw.Close()
		_ = st.Close()
	})
	return &onboardingRouteTestEnv{a: a, mux: mux, db: raw, dataDir: dataDir}
}

func (e *onboardingRouteTestEnv) serve(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set(portalHeader, "1")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	return rr
}

func (e *onboardingRouteTestEnv) serveRequest(req *http.Request) *httptest.ResponseRecorder {
	if req.Method != http.MethodGet {
		req.Header.Set(portalHeader, "1")
	}
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, req)
	return rr
}

func (e *onboardingRouteTestEnv) setState(t *testing.T, state store.OnboardingState) {
	t.Helper()
	_, err := e.db.Exec(`UPDATE app_onboarding_state SET
completed_version = ?, phase = ?, provider_kind = ?, provider_id = ?, account_id = ?,
model_id = ?, staged_combo_id = ?, persona_fingerprint = ?, test_nonce_hash = ?,
test_expires_at = ?, restart_in_progress = ?, revision = ?, updated_at = ?
WHERE id = 1`,
		state.CompletedVersion, state.Phase, state.ProviderKind, state.ProviderID,
		state.AccountID, state.ModelID, state.StagedComboID, state.PersonaFingerprint,
		state.TestNonceHash, state.TestExpiresAt, state.RestartInProgress,
		state.Revision, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("set onboarding state: %v", err)
	}
}

func (e *onboardingRouteTestEnv) setCorruptState(t *testing.T, state store.OnboardingState) {
	t.Helper()
	conn, err := e.db.Conn(t.Context())
	if err != nil {
		t.Fatalf("open raw database connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("disable test-only check constraints: %v", err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = OFF`)
	}()
	_, err = conn.ExecContext(t.Context(), `UPDATE app_onboarding_state SET
completed_version = ?, phase = ?, provider_kind = ?, provider_id = ?, account_id = ?,
model_id = ?, staged_combo_id = ?, persona_fingerprint = ?, test_nonce_hash = ?,
test_expires_at = ?, restart_in_progress = ?, revision = ?, updated_at = ?
WHERE id = 1`,
		state.CompletedVersion, state.Phase, state.ProviderKind, state.ProviderID,
		state.AccountID, state.ModelID, state.StagedComboID, state.PersonaFingerprint,
		state.TestNonceHash, state.TestExpiresAt, state.RestartInProgress,
		state.Revision, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("set corrupt onboarding state: %v", err)
	}
}

func (e *onboardingRouteTestEnv) state(t *testing.T) store.OnboardingState {
	t.Helper()
	state, err := e.a.st.OnboardingState()
	if err != nil {
		t.Fatalf("OnboardingState() = %v", err)
	}
	return state
}

func decodeOnboardingResponse[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	return decodeAppRouteJSON[T](t, rr.Body.Bytes())
}

func onboardingErrorCode(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	got := decodeOnboardingResponse[struct {
		Error llmErrorBody `json:"error"`
	}](t, rr)
	return got.Error.Code
}

func requireOnboardingCode(t *testing.T, rr *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status=%d body=%s; want %d", rr.Code, rr.Body.String(), status)
	}
	if got := onboardingErrorCode(t, rr); got != code {
		t.Fatalf("error code=%q body=%s; want %q", got, rr.Body.String(), code)
	}
}

func TestAppOnboardingStatusRequiresWizardForV1(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	rr := env.serve(http.MethodGet, "/onboarding/status", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
	if !got.Required || got.CurrentVersion != 1 || got.CompletedVersion != 0 ||
		got.Phase != store.OnboardingPhaseProvider || got.Revision != 1 {
		t.Fatalf("status=%+v", got)
	}
}

func TestAppOnboardingStatusRequiredTracksCompletionAndRestart(t *testing.T) {
	tests := []struct {
		name     string
		state    store.OnboardingState
		required bool
	}{
		{"completed", store.OnboardingState{CompletedVersion: 1, Phase: store.OnboardingPhaseCompleted, Revision: 7}, false},
		{"restart", store.OnboardingState{CompletedVersion: 1, Phase: store.OnboardingPhaseProvider, RestartInProgress: true, Revision: 8}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			env.setState(t, tt.state)
			rr := env.serve(http.MethodGet, "/onboarding/status", "")
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			got := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
			if got.Required != tt.required || got.CompletedVersion != tt.state.CompletedVersion ||
				got.RestartInProgress != tt.state.RestartInProgress || got.Revision != tt.state.Revision {
				t.Fatalf("status=%+v", got)
			}
		})
	}
}

func seedOnboardingRoute(t *testing.T, env *onboardingRouteTestEnv, providerID, kind string, enabled bool) {
	t.Helper()
	if err := env.a.st.CreateLLMProvider(store.LLMProvider{
		ID: providerID, Name: providerID, Kind: kind, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateLLMProvider(%s) = %v", providerID, err)
	}
	if err := env.a.st.AddLLMModel(store.LLMModel{
		ProviderID: providerID, ModelID: "model", Name: "model",
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		t.Fatalf("AddLLMModel(%s) = %v", providerID, err)
	}
	combo, err := env.a.st.CreateLLMCombo("active", "fallback")
	if err != nil {
		t.Fatalf("CreateLLMCombo() = %v", err)
	}
	if _, err := env.a.st.ReplaceLLMComboMembers(combo.ID, combo.Revision, "fallback", []store.LLMRouteEntry{{
		ProviderID: providerID, ModelID: "model", Enabled: enabled,
	}}); err != nil {
		t.Fatalf("ReplaceLLMComboMembers() = %v", err)
	}
	if err := env.a.st.SetActiveLLMCombo(combo.ID); err != nil {
		t.Fatalf("SetActiveLLMCombo() = %v", err)
	}
}

func onboardingRoutingDigest(t *testing.T, env *onboardingRouteTestEnv) string {
	t.Helper()
	combos, err := env.a.st.LLMCombos()
	if err != nil {
		t.Fatal(err)
	}
	route, err := env.a.st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(struct {
		Combos any `json:"combos"`
		Route  any `json:"route"`
	}{Combos: combos, Route: route})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestAppOnboardingStatusSuggestsOnlySupportedFirstEnabledRouteKind(t *testing.T) {
	tests := []struct {
		name       string
		providerID string
		kind       string
		enabled    bool
		want       string
	}{
		{"codex", "codex-route", "codex", true, "codex"},
		{"claude", "claude-route", "claude-code", true, "claude-code"},
		{"unsupported", "openai-route", "openai", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			seedOnboardingRoute(t, env, tt.providerID, tt.kind, tt.enabled)
			rr := env.serve(http.MethodGet, "/onboarding/status", "")
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if got := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr).SuggestedProviderKind; got != tt.want {
				t.Fatalf("suggested_provider_kind=%q; want %q", got, tt.want)
			}
		})
	}
	t.Run("empty route", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		rr := env.serve(http.MethodGet, "/onboarding/status", "")
		if got := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr).SuggestedProviderKind; got != "" {
			t.Fatalf("suggested_provider_kind=%q; want empty", got)
		}
	})
}

func TestAppOnboardingStatusNeverSerializesInternalOrSecretFields(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseTest, StagedComboID: "HIDDEN-COMBO",
		PersonaFingerprint: "HIDDEN-FINGERPRINT", TestNonceHash: "HIDDEN-NONCE-HASH",
		TestExpiresAt: "HIDDEN-EXPIRY", Revision: 4,
	})
	env.replaceProviderStages(t, appOnboardingProviderWire{
		Kind: "codex", Status: "ready", ProviderID: "provider-public",
		AccountID: "account-public", ModelID: "model-public", Position: 0,
	})
	rr := env.serve(http.MethodGet, "/onboarding/status", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	for key := range object {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{"config", "combo", "fingerprint", "nonce", "hash", "expiry", "expires", "credential", "secret", "token"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("response key %q contains forbidden fragment %q", key, forbidden)
			}
		}
	}
	body := rr.Body.String()
	for _, hidden := range []string{"HIDDEN-COMBO", "HIDDEN-FINGERPRINT", "HIDDEN-NONCE-HASH", "HIDDEN-EXPIRY"} {
		if strings.Contains(body, hidden) {
			t.Fatalf("response leaked %q: %s", hidden, body)
		}
	}
}

func TestAppOnboardingStatusRejectsSemanticallyCorruptState(t *testing.T) {
	tests := []struct {
		name  string
		state store.OnboardingState
	}{
		{"nonpositive revision", store.OnboardingState{Phase: store.OnboardingPhaseProvider, Revision: 0}},
		{"negative completed version", store.OnboardingState{CompletedVersion: -1, Phase: store.OnboardingPhaseProvider, Revision: 1}},
		{"unknown phase", store.OnboardingState{Phase: "broken-phase", Revision: 1}},
		{"unsupported selected provider", store.OnboardingState{Phase: store.OnboardingPhaseProvider, ProviderKind: "openai", Revision: 1}},
		{"connect without provider", store.OnboardingState{Phase: store.OnboardingPhaseConnect, Revision: 1}},
		{"setup without provider", store.OnboardingState{Phase: store.OnboardingPhaseSetup, Revision: 1}},
		{"persona without provider", store.OnboardingState{Phase: store.OnboardingPhasePersona, Revision: 1}},
		{"test without provider", store.OnboardingState{Phase: store.OnboardingPhaseTest, Revision: 1}},
		{"completed unsupported provider", store.OnboardingState{CompletedVersion: 1, Phase: store.OnboardingPhaseCompleted, ProviderKind: "private-provider", Revision: 1}},
		{"completed orphan identity", store.OnboardingState{CompletedVersion: 1, Phase: store.OnboardingPhaseCompleted, ProviderID: "HIDDEN-PROVIDER", Revision: 1}},
		{"completed phase restarting", store.OnboardingState{CompletedVersion: 1, Phase: store.OnboardingPhaseCompleted, RestartInProgress: true, Revision: 1}},
		{"completed version in unfinished phase", store.OnboardingState{CompletedVersion: 1, Phase: store.OnboardingPhaseProvider, Revision: 1}},
		{"restart before completed version", store.OnboardingState{CompletedVersion: 0, Phase: store.OnboardingPhaseProvider, RestartInProgress: true, Revision: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			env.setCorruptState(t, tt.state)
			rr := env.serve(http.MethodGet, "/onboarding/status", "")
			requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
			body := strings.ToLower(rr.Body.String())
			for _, forbidden := range []string{"sql", "database", "app_onboarding_state", env.dataDir, "broken-phase", "openai", "private-provider", "HIDDEN-PROVIDER"} {
				if strings.Contains(body, strings.ToLower(forbidden)) {
					t.Fatalf("response leaked %q: %s", forbidden, rr.Body.String())
				}
			}
		})
	}
}

func TestAppOnboardingStatusTreatsOlderCompletionAsProviderUpgrade(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{
		CompletedVersion: store.CurrentOnboardingVersion - 1,
		Phase:            store.OnboardingPhaseCompleted,
		ProviderKind:     "codex",
		ProviderID:       "live-provider",
		AccountID:        "live-account",
		ModelID:          "live-model",
		Revision:         6,
	})
	rr := env.serve(http.MethodGet, "/onboarding/status", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
	if !got.Required || got.CompletedVersion != store.CurrentOnboardingVersion-1 ||
		got.Phase != store.OnboardingPhaseProvider || got.ProviderKind != "" ||
		got.ProviderID != "" || got.AccountID != "" || got.ModelID != "" || got.Revision != 6 {
		t.Fatalf("upgrade status=%+v", got)
	}
}

func TestAppOnboardingUpgradeProviderPreservesLiveAccountDirectoryAndConnect(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	if err := env.a.st.EnsureProviderForKind("codex"); err != nil {
		t.Fatal(err)
	}
	dir := accountConfigDir(env.dataDir, "codex", "live-account")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: "live-account", ProviderID: "codex", Label: "live", ConfigDir: dir, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		CompletedVersion: store.CurrentOnboardingVersion - 1,
		Phase:            store.OnboardingPhaseCompleted,
		ProviderKind:     "codex",
		ProviderID:       "codex",
		AccountID:        "live-account",
		ModelID:          "live-model",
		Revision:         4,
	})
	ctx, cancel := context.WithCancel(context.Background())
	job := newManualConnectJob("codex", cancel)
	connectMgr = &connectManager{job: job, jobKind: "codex"}
	t.Cleanup(func() {
		cancel()
		select {
		case <-job.done:
		default:
			close(job.done)
		}
	})

	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":4,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if ctx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("upgrade selection canceled the prior completed flow's Connect job")
	}
	requireAccountPresent(t, env, "codex", "live-account", true)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("live config directory was removed: %v", err)
	}
}

func TestAppOnboardingProviderRejectsCorruptStateBeforeConnectCleanup(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setCorruptState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseConnect, ProviderKind: "openai", Revision: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	job := newManualConnectJob("openai", cancel)
	connectMgr = &connectManager{job: job, jobKind: "openai"}
	t.Cleanup(func() {
		cancel()
		close(job.done)
	})
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"codex"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if ctx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("handler canceled Connect before rejecting corrupt onboarding state")
	}
}

func TestAppOnboardingStatusFailsClosedWithoutStateAndHidesDatabaseError(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	if _, err := env.db.Exec(`DELETE FROM app_onboarding_state WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	rr := env.serve(http.MethodGet, "/onboarding/status", "")
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
	body := strings.ToLower(rr.Body.String())
	for _, forbidden := range []string{"sql", "database", "app_onboarding_state", env.dataDir} {
		if strings.Contains(body, strings.ToLower(forbidden)) {
			t.Fatalf("response leaked %q: %s", forbidden, rr.Body.String())
		}
	}
}
