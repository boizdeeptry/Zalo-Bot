package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/store"
)

type appOnboardingStatusWire struct {
	Phase        string                      `json:"phase"`
	ProviderKind string                      `json:"provider_kind"`
	ProviderID   string                      `json:"provider_id"`
	AccountID    string                      `json:"account_id"`
	ModelID      string                      `json:"model_id"`
	Revision     int64                       `json:"revision"`
	Providers    []appOnboardingProviderWire `json:"providers"`
	Options      []appProviderOptionWire     `json:"provider_options"`
}

type appOnboardingProviderWire struct {
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	ProviderID string `json:"provider_id"`
	AccountID  string `json:"account_id"`
	ModelID    string `json:"model_id"`
	Position   int    `json:"position"`
}

type appProviderOptionWire struct {
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
	Beta        bool   `json:"beta"`
	Advertised  bool   `json:"advertised"`
	RouteRank   int    `json:"route_rank"`
}

func (e *onboardingRouteTestEnv) replaceProviderStages(
	t *testing.T,
	stages ...appOnboardingProviderWire,
) {
	t.Helper()
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM app_onboarding_provider_stages`); err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		if _, err := tx.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, provider_id, account_id, model_id, position, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`, stage.Kind, stage.Status, stage.ProviderID,
			stage.AccountID, stage.ModelID, stage.Position, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestAppOnboardingStatusProjectsCatalogAndSelectedProviders(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)

	fresh := env.serve(http.MethodGet, "/onboarding/status", "")
	if fresh.Code != http.StatusOK {
		t.Fatalf("fresh status=%d body=%s", fresh.Code, fresh.Body.String())
	}
	freshStatus := decodeOnboardingResponse[appOnboardingStatusWire](t, fresh)
	if freshStatus.Providers == nil || len(freshStatus.Providers) != 0 {
		t.Fatalf("fresh providers = %#v; want non-nil empty array", freshStatus.Providers)
	}
	if len(freshStatus.Options) != 2 || freshStatus.Options[0].Kind != "codex" ||
		freshStatus.Options[1].Kind != "claude-code" {
		t.Fatalf("provider_options = %+v", freshStatus.Options)
	}
	for _, option := range freshStatus.Options {
		if !option.Advertised {
			t.Fatalf("provider option is not advertised: %+v", option)
		}
	}

	if _, err := env.a.st.ReplaceOnboardingProviderSelection(1, []string{"claude-code", "codex"}); err != nil {
		t.Fatal(err)
	}
	selected := env.serve(http.MethodGet, "/onboarding/status", "")
	if selected.Code != http.StatusOK {
		t.Fatalf("selected status=%d body=%s", selected.Code, selected.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingStatusWire](t, selected)
	assertOnboardingProviderWireKinds(t, got.Providers, []string{"codex", "claude-code"})
	for _, provider := range got.Providers {
		if provider.Status != "pending" || provider.ProviderID != "" ||
			provider.AccountID != "" || provider.ModelID != "" {
			t.Fatalf("pending provider leaked identity or wrong status: %+v", provider)
		}
	}
}

func TestAppOnboardingStatusProjectsCatalogV8PersonaAndTestIdentity(t *testing.T) {
	for _, phase := range []string{store.OnboardingPhasePersona, store.OnboardingPhaseTest} {
		t.Run(phase, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			state := store.OnboardingState{
				Phase: phase, StagedComboID: "00000000-0000-4000-8000-000000000001", Revision: 9,
			}
			if phase == store.OnboardingPhaseTest {
				state.PersonaFingerprint = strings.Repeat("a", 64)
			}
			env.setState(t, state)
			env.replaceProviderStages(t, appOnboardingProviderWire{
				Kind: "codex", Status: "ready", ProviderID: "codex-provider",
				AccountID: "codex-account", ModelID: "codex-model", Position: 0,
			})

			rr := env.serve(http.MethodGet, "/onboarding/status", "")
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			got := decodeOnboardingResponse[appOnboardingStatusWire](t, rr)
			if got.ProviderKind != "codex" || got.ProviderID != "codex-provider" ||
				got.AccountID != "codex-account" || got.ModelID != "codex-model" {
				t.Fatalf("legacy projection = %+v", got)
			}
			assertOnboardingProviderWireKinds(t, got.Providers, []string{"codex"})
		})
	}
}

func TestAppOnboardingStatusProjectsCatalogRejectsMalformedStageAndHidesNestedSecrets(t *testing.T) {
	t.Run("malformed", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		env.replaceProviderStages(t, appOnboardingProviderWire{
			Kind: "private-provider", Status: "pending", Position: 0,
		})
		rr := env.serve(http.MethodGet, "/onboarding/status", "")
		requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
		if strings.Contains(strings.ToLower(rr.Body.String()), "private-provider") {
			t.Fatalf("malformed kind leaked: %s", rr.Body.String())
		}
	})

	t.Run("recursive privacy", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		if _, err := env.a.st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"}); err != nil {
			t.Fatal(err)
		}
		rr := env.serve(http.MethodGet, "/onboarding/status", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		var value any
		if err := json.Unmarshal(rr.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		assertNoOnboardingPrivateKeys(t, value)
	})
}

func TestAppOnboardingProvidersPutPersistsBothCanonicalAndSupportsNoopLostResponse(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	rr := env.serve(http.MethodPut, "/onboarding/providers",
		`{"revision":1,"selected_kinds":["claude-code","codex"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingStatusWire](t, rr)
	if got.Revision != 2 || got.Phase != store.OnboardingPhaseProvider {
		t.Fatalf("status = %+v", got)
	}
	assertOnboardingProviderWireKinds(t, got.Providers, []string{"codex", "claude-code"})

	for name, body := range map[string]string{
		"no-op":         `{"revision":2,"selected_kinds":["codex","claude-code"]}`,
		"lost response": `{"revision":1,"selected_kinds":["claude-code","codex"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			retry := env.serve(http.MethodPut, "/onboarding/providers", body)
			if retry.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", retry.Code, retry.Body.String())
			}
			if status := decodeOnboardingResponse[appOnboardingStatusWire](t, retry); status.Revision != 2 {
				t.Fatalf("retry revision = %d; want 2", status.Revision)
			}
		})
	}

	before, err := env.a.st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	stale := env.serve(http.MethodPut, "/onboarding/providers", `{"revision":1,"selected_kinds":[]}`)
	requireOnboardingCode(t, stale, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
	after, err := env.a.st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before.Stages, after.Stages) || before.State != after.State {
		t.Fatalf("stale request mutated snapshot: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingProvidersPutCommittedSnapshotDoesNotReadSuggestion(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	oldSuggested := appOnboardingSuggestedProviderKind
	suggestionCalls := 0
	appOnboardingSuggestedProviderKind = func(*api) (string, error) {
		suggestionCalls++
		return "", errors.New("SUGGESTION-DB-SECRET")
	}
	t.Cleanup(func() { appOnboardingSuggestedProviderKind = oldSuggested })

	selected := env.serve(http.MethodPut, "/onboarding/providers",
		`{"revision":1,"selected_kinds":["claude-code","codex"]}`)
	if selected.Code != http.StatusOK {
		t.Fatalf("committed plural status=%d body=%s", selected.Code, selected.Body.String())
	}
	selection := decodeOnboardingResponse[appOnboardingStatusWire](t, selected)
	if selection.Revision != 2 || selection.Phase != store.OnboardingPhaseProvider {
		t.Fatalf("committed plural response = %+v", selection)
	}
	assertOnboardingProviderWireKinds(t, selection.Providers, []string{"codex", "claude-code"})
	if suggestionCalls != 0 {
		t.Fatalf("plural mutation performed %d post-commit suggestion reads", suggestionCalls)
	}

	status := env.serve(http.MethodGet, "/onboarding/status", "")
	requireOnboardingCode(t, status, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
	if suggestionCalls != 1 {
		t.Fatalf("GET suggestion seam calls = %d; want 1", suggestionCalls)
	}
	if strings.Contains(status.Body.String(), "SUGGESTION-DB-SECRET") {
		t.Fatalf("GET leaked suggestion error: %s", status.Body.String())
	}

	suggestionCalls = 0
	begun := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":2,"kind":"codex"}`)
	if begun.Code != http.StatusOK {
		t.Fatalf("committed singular status=%d body=%s", begun.Code, begun.Body.String())
	}
	begin := decodeOnboardingResponse[appOnboardingStatusWire](t, begun)
	if begin.Revision != 3 || begin.Phase != store.OnboardingPhaseConnect || begin.ProviderKind != "codex" {
		t.Fatalf("committed singular response = %+v", begin)
	}
	assertOnboardingProviderWireKinds(t, begin.Providers, []string{"codex", "claude-code"})
	if suggestionCalls != 0 {
		t.Fatalf("singular mutation performed %d post-commit suggestion reads", suggestionCalls)
	}
}

func TestAppOnboardingProvidersPutStrictSelectionValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{"missing", `{"revision":1}`, "ONBOARDING_PROVIDER_SELECTION_INVALID"},
		{"null", `{"revision":1,"selected_kinds":null}`, "ONBOARDING_PROVIDER_SELECTION_INVALID"},
		{"object", `{"revision":1,"selected_kinds":{}}`, "ONBOARDING_PROVIDER_SELECTION_INVALID"},
		{"non-string", `{"revision":1,"selected_kinds":[1]}`, "ONBOARDING_PROVIDER_SELECTION_INVALID"},
		{"blank", `{"revision":1,"selected_kinds":[""]}`, "ONBOARDING_PROVIDER_SELECTION_INVALID"},
		{"duplicate", `{"revision":1,"selected_kinds":["codex","codex"]}`, "ONBOARDING_PROVIDER_SELECTION_INVALID"},
		{"too many", `{"revision":1,"selected_kinds":["codex","codex","codex","codex","codex","codex","codex","codex","codex"]}`, "ONBOARDING_PROVIDER_SELECTION_INVALID"},
		{"unsupported", `{"revision":1,"selected_kinds":["gemini-cli"]}`, "ONBOARDING_PROVIDER_UNSUPPORTED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			rr := env.serve(http.MethodPut, "/onboarding/providers", tt.body)
			requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, tt.code)
			if state := env.state(t); state.Revision != 1 || state.Phase != store.OnboardingPhaseProvider {
				t.Fatalf("invalid request mutated state: %+v", state)
			}
		})
	}

	t.Run("empty array is valid", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		rr := env.serve(http.MethodPut, "/onboarding/providers", `{"revision":1,"selected_kinds":[]}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		got := decodeOnboardingResponse[appOnboardingStatusWire](t, rr)
		if got.Providers == nil || len(got.Providers) != 0 || got.Revision != 1 {
			t.Fatalf("empty selection response = %+v", got)
		}
	})
}

func TestAppOnboardingMutationsRejectDuplicateTopLevelKeysWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"providers revision", http.MethodPut, "/onboarding/providers", `{"revision":1,"revision":1,"selected_kinds":[]}`},
		{"providers escaped revision", http.MethodPut, "/onboarding/providers", `{"revision":1,"\u0072evision":1,"selected_kinds":[]}`},
		{"providers case-folded revision", http.MethodPut, "/onboarding/providers", `{"revision":1,"Revision":1,"selected_kinds":[]}`},
		{"providers Unicode long-s selected kinds", http.MethodPut, "/onboarding/providers", `{"revision":1,"selected_kinds":[],"\u017felected_kinds":["codex"]}`},
		{"providers selected kinds", http.MethodPut, "/onboarding/providers", `{"revision":1,"selected_kinds":[],"selected_kinds":[]}`},
		{"provider revision", http.MethodPut, "/onboarding/provider", `{"revision":1,"revision":1,"kind":"codex"}`},
		{"provider kind", http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"codex","kind":"codex"}`},
		{"provider Unicode Kelvin kind", http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"codex","\u212aind":"claude-code"}`},
		{"restart confirmed", http.MethodPost, "/onboarding/restart", `{"revision":1,"confirmed":true,"confirmed":true}`},
		{"setup account", http.MethodPost, "/onboarding/setup", `{"revision":1,"account_id":"account","account_id":"account"}`},
		{"test message", http.MethodPost, "/onboarding/test-chat", `{"revision":1,"message":"xin chào","message":"xin chào"}`},
		{"complete token", http.MethodPost, "/onboarding/complete", `{"revision":1,"test_token":"token","test_token":"token"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			before, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			beforeRouting := onboardingRoutingDigest(t, env)

			rr := env.serve(tt.method, tt.path, tt.body)
			requireOnboardingCode(t, rr, http.StatusBadRequest, "ONBOARDING_REQUEST_INVALID")

			after, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if before.State != after.State || !slices.Equal(before.Stages, after.Stages) {
				t.Fatalf("duplicate-key request mutated onboarding: before=%+v after=%+v", before, after)
			}
			if afterRouting := onboardingRoutingDigest(t, env); afterRouting != beforeRouting {
				t.Fatalf("duplicate-key request mutated routing: before=%s after=%s", beforeRouting, afterRouting)
			}
		})
	}
}

func TestAppOnboardingProvidersPutRejectsDirtySlotAndReadyOmission(t *testing.T) {
	t.Run("dirty Provider slot", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		env.setState(t, store.OnboardingState{
			Phase: store.OnboardingPhaseProvider, ProviderKind: "codex", Revision: 5,
		})
		rr := env.serve(http.MethodPut, "/onboarding/providers", `{"revision":5,"selected_kinds":["codex"]}`)
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_CONFIGURATION_CHANGED")
	})

	t.Run("ready omission", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		env.setState(t, store.OnboardingState{Phase: store.OnboardingPhaseProvider, Revision: 7})
		env.replaceProviderStages(t, appOnboardingProviderWire{
			Kind: "codex", Status: "ready", ProviderID: "codex",
			AccountID: "account", ModelID: "model", Position: 0,
		})
		rr := env.serve(http.MethodPut, "/onboarding/providers", `{"revision":7,"selected_kinds":[]}`)
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_CONFIGURATION_CHANGED")
	})
}

func TestAppOnboardingProvidersPutProjectsRetainedFingerprintAcrossProviderConnectAndSetup(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	fingerprint := agentPersonaFingerprint([]byte("Tên bot là Bé Mi."), "Bé Mi")
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseProvider, PersonaFingerprint: fingerprint, Revision: 11,
	})
	env.replaceProviderStages(t, appOnboardingProviderWire{
		Kind: "codex", Status: "ready", ProviderID: "codex",
		AccountID: "codex-account", ModelID: "codex-model", Position: 0,
	})

	assertProjectable := func(name string, rrBody string, statusCode int) appOnboardingStatusWire {
		t.Helper()
		if statusCode != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", name, statusCode, rrBody)
		}
		if strings.Contains(rrBody, fingerprint) || strings.Contains(strings.ToLower(rrBody), "fingerprint") {
			t.Fatalf("%s leaked retained fingerprint: %s", name, rrBody)
		}
		return decodeAppRouteJSON[appOnboardingStatusWire](t, []byte(rrBody))
	}

	status := env.serve(http.MethodGet, "/onboarding/status", "")
	projected := assertProjectable("Provider status", status.Body.String(), status.Code)
	if projected.Phase != store.OnboardingPhaseProvider || projected.Revision != 11 {
		t.Fatalf("Provider projection = %+v", projected)
	}

	noOp := env.serve(http.MethodPut, "/onboarding/providers",
		`{"revision":11,"selected_kinds":["codex"]}`)
	projected = assertProjectable("all-ready no-op", noOp.Body.String(), noOp.Code)
	if projected.Phase != store.OnboardingPhaseProvider || projected.Revision != 11 {
		t.Fatalf("all-ready no-op projection = %+v", projected)
	}

	added := env.serve(http.MethodPut, "/onboarding/providers",
		`{"revision":11,"selected_kinds":["claude-code","codex"]}`)
	projected = assertProjectable("add pending", added.Body.String(), added.Code)
	if projected.Phase != store.OnboardingPhaseProvider || projected.Revision != 12 {
		t.Fatalf("add-pending projection = %+v", projected)
	}
	assertOnboardingProviderWireKinds(t, projected.Providers, []string{"codex", "claude-code"})

	begun := env.serve(http.MethodPut, "/onboarding/provider",
		`{"revision":12,"kind":"claude-code"}`)
	projected = assertProjectable("begin pending", begun.Body.String(), begun.Code)
	if projected.Phase != store.OnboardingPhaseConnect || projected.ProviderKind != "claude-code" ||
		projected.Revision != 13 {
		t.Fatalf("begin projection = %+v", projected)
	}
	if got := env.state(t).PersonaFingerprint; got != fingerprint {
		t.Fatalf("begin changed retained fingerprint = %q; want %q", got, fingerprint)
	}

	setup := env.state(t)
	setup.Phase = store.OnboardingPhaseSetup
	setup.ProviderID = "claude-code"
	setup.AccountID = "claude-account"
	env.setState(t, setup)
	setupStatus := env.serve(http.MethodGet, "/onboarding/status", "")
	projected = assertProjectable("Setup status", setupStatus.Body.String(), setupStatus.Code)
	if projected.Phase != store.OnboardingPhaseSetup || projected.ProviderKind != "claude-code" ||
		projected.ProviderID != "claude-code" || projected.AccountID != "claude-account" {
		t.Fatalf("Setup projection = %+v", projected)
	}
}

func TestAppOnboardingProvidersPutConcurrentSameRevisionHasOneWinner(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	bodies := []string{
		`{"revision":1,"selected_kinds":["codex"]}`,
		`{"revision":1,"selected_kinds":["claude-code"]}`,
	}
	start := make(chan struct{})
	statuses := make(chan int, len(bodies))
	var wg sync.WaitGroup
	for _, body := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses <- env.serve(http.MethodPut, "/onboarding/providers", body).Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("status counts = %v; want one 200 and one 409", counts)
	}
}

func TestAppOnboardingProviderBeginsSelectedPendingAndPreservesOtherRows(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	selected := env.serve(http.MethodPut, "/onboarding/providers",
		`{"revision":1,"selected_kinds":["claude-code","codex"]}`)
	if selected.Code != http.StatusOK {
		t.Fatalf("select status=%d body=%s", selected.Code, selected.Body.String())
	}

	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":2,"kind":"claude-code"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("begin status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingStatusWire](t, rr)
	if got.Phase != store.OnboardingPhaseConnect || got.ProviderKind != "claude-code" || got.Revision != 3 {
		t.Fatalf("begin response = %+v", got)
	}
	assertOnboardingProviderWireKinds(t, got.Providers, []string{"codex", "claude-code"})

	retry := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":2,"kind":"claude-code"}`)
	if retry.Code != http.StatusOK {
		t.Fatalf("lost response status=%d body=%s", retry.Code, retry.Body.String())
	}
	mismatch := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":2,"kind":"codex"}`)
	requireOnboardingCode(t, mismatch, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
}

func TestAppOnboardingProviderBeginsSelectedRejectsUnselectedDirtyAndOtherPhaseWithoutCancel(t *testing.T) {
	t.Run("unselected", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"codex"}`)
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_STAGING_INVALID")
	})

	t.Run("dirty Provider slot", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		env.setState(t, store.OnboardingState{
			Phase: store.OnboardingPhaseProvider, ProviderKind: "codex", Revision: 4,
		})
		env.replaceProviderStages(t, appOnboardingProviderWire{Kind: "codex", Status: "pending", Position: 0})
		rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":4,"kind":"codex"}`)
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_CONFIGURATION_CHANGED")
	})

	t.Run("Connect phase does not cancel", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		env.setState(t, store.OnboardingState{
			Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 6,
		})
		env.replaceProviderStages(t, appOnboardingProviderWire{Kind: "codex", Status: "pending", Position: 0})
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
		go func() {
			<-ctx.Done()
			select {
			case <-job.done:
			default:
				close(job.done)
			}
		}()

		rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":6,"kind":"codex"}`)
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
		if ctx.Err() != nil || job.snapshot().Phase == phaseCanceled {
			t.Fatal("rejected begin canceled Connect")
		}
	})
}

func assertOnboardingProviderWireKinds(
	t *testing.T,
	providers []appOnboardingProviderWire,
	want []string,
) {
	t.Helper()
	if providers == nil || len(providers) != len(want) {
		t.Fatalf("providers = %#v; want kinds %q", providers, want)
	}
	for index, kind := range want {
		if providers[index].Kind != kind || providers[index].Position != index {
			t.Fatalf("provider %d = %+v; want kind %q at canonical position", index, providers[index], kind)
		}
	}
}

func assertNoOnboardingPrivateKeys(t *testing.T, value any) {
	t.Helper()
	forbidden := []string{
		"command", "config", "credential", "env", "path", "secret", "token",
		"receipt", "combo", "fingerprint", "nonce", "expiry", "expires",
	}
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				lower := strings.ToLower(key)
				for _, fragment := range forbidden {
					if strings.Contains(lower, fragment) {
						t.Fatalf("response key %q contains forbidden fragment %q", key, fragment)
					}
				}
				walk(nested)
			}
		case []any:
			for _, nested := range typed {
				walk(nested)
			}
		}
	}
	walk(value)
}
