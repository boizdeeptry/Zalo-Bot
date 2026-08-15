package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentdc/internal/store"

	"github.com/google/uuid"
)

func TestAppOnboardingBackToProvidersPersonaAndTestReturnAuthoritativePrivateStatus(t *testing.T) {
	for _, phase := range []string{store.OnboardingPhasePersona, store.OnboardingPhaseTest} {
		t.Run(phase, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			source, privateValues := seedAppOnboardingBackReadyForTest(t, env, phase, 21)

			rr := env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":21}`)
			if rr.Code != http.StatusOK {
				t.Fatalf("Back status=%d body=%s", rr.Code, rr.Body.String())
			}
			got := decodeOnboardingResponse[appOnboardingStatusWire](t, rr)
			if got.Phase != store.OnboardingPhaseProvider || got.Revision != 22 ||
				got.ProviderKind != "" || got.ProviderID != "" || got.AccountID != "" || got.ModelID != "" {
				t.Fatalf("Back response = %+v", got)
			}
			if got.Providers == nil || len(got.Providers) != len(source.Stages) {
				t.Fatalf("Back providers = %+v", got.Providers)
			}
			for index, stage := range source.Stages {
				wire := got.Providers[index]
				if wire.Kind != stage.Kind || wire.Status != "ready" ||
					wire.ProviderID != stage.ProviderID || wire.AccountID != stage.AccountID ||
					wire.ModelID != stage.ModelID || wire.Position != stage.Position {
					t.Fatalf("Back provider %d = %+v; want %+v", index, wire, stage)
				}
			}
			persisted, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if persisted.State.StagedComboID != "" || persisted.State.TestNonceHash != "" ||
				persisted.State.TestExpiresAt != "" ||
				persisted.State.PersonaFingerprint != source.State.PersonaFingerprint ||
				!slices.Equal(persisted.Stages, source.Stages) {
				t.Fatalf("persisted Back = %+v; source=%+v", persisted, source)
			}
			body := rr.Body.String()
			for _, private := range privateValues {
				if private != "" && strings.Contains(body, private) {
					t.Fatalf("Back response leaked private value %q: %s", private, body)
				}
			}
			for _, privateKey := range []string{"staged_combo_id", "persona_fingerprint", "test_nonce_hash", "test_expires_at"} {
				if strings.Contains(body, privateKey) {
					t.Fatalf("Back response exposed private key %q: %s", privateKey, body)
				}
			}
		})
	}
}

func TestAppOnboardingBackToProvidersStrictRequestAndInvalidStateDoNotCancelTest(t *testing.T) {
	t.Run("strict request", func(t *testing.T) {
		tests := []struct {
			name   string
			body   string
			status int
			code   string
		}{
			{name: "missing revision", body: `{}`, status: http.StatusUnprocessableEntity, code: "ONBOARDING_REVISION_INVALID"},
			{name: "unknown field", body: `{"revision":31,"extra":true}`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
			{name: "duplicate revision", body: `{"revision":31,"revision":32}`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
			{name: "null", body: `null`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				env := newOnboardingRouteTestEnv(t)
				seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhasePersona, 31)
				before, err := env.a.st.OnboardingSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				rr := env.serve(http.MethodPost, "/onboarding/back-to-providers", test.body)
				requireOnboardingCode(t, rr, test.status, test.code)
				after, err := env.a.st.OnboardingSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				if before.State != after.State || !slices.Equal(before.Stages, after.Stages) {
					t.Fatal("invalid Back request mutated state")
				}
			})
		}
	})

	for _, test := range []struct {
		name     string
		phase    string
		revision int64
		body     string
		code     string
	}{
		{name: "invalid phase", phase: store.OnboardingPhaseProvider, revision: 41, body: `{"revision":41}`, code: "ONBOARDING_PHASE_INVALID"},
		{name: "stale", phase: store.OnboardingPhasePersona, revision: 41, body: `{"revision":40}`, code: "ONBOARDING_REVISION_CONFLICT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			source, _ := seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhasePersona, test.revision)
			if test.phase == store.OnboardingPhaseProvider {
				state := source.State
				state.Phase = store.OnboardingPhaseProvider
				state.StagedComboID = ""
				env.setState(t, state)
			}
			cancelCalls, finish := installFakeActiveOnboardingTestForBack(t)
			defer finish()
			before, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			rr := env.serve(http.MethodPost, "/onboarding/back-to-providers", test.body)
			requireOnboardingCode(t, rr, http.StatusConflict, test.code)
			if cancelCalls.Load() != 0 {
				t.Fatalf("rejected Back canceled Test Chat %d times", cancelCalls.Load())
			}
			after, err := env.a.st.OnboardingSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if before.State != after.State || !slices.Equal(before.Stages, after.Stages) {
				t.Fatal("rejected Back mutated state")
			}
		})
	}
}

func TestAppOnboardingBackToProvidersCancelsWaitsAndFencesNewTestChat(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhaseTest, 51)
	cancelCalls, finish := installFakeActiveOnboardingTestForBack(t)
	defer finish()
	canceled := fakeActiveOnboardingTestCanceledForBack(t)

	oldAfterWait := appOnboardingProviderAfterTestWait
	afterWait := make(chan struct{}, 1)
	appOnboardingProviderAfterTestWait = func() { afterWait <- struct{}{} }
	t.Cleanup(func() { appOnboardingProviderAfterTestWait = oldAfterWait })

	backDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		backDone <- env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":51}`)
	}()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Back did not cancel active Test Chat")
	}
	blocked := env.serve(http.MethodPost, "/onboarding/test-chat", `{"revision":51,"message":"xin chào"}`)
	requireOnboardingCode(t, blocked, http.StatusConflict, "ONBOARDING_TEST_BUSY")
	if cancelCalls.Load() != 1 {
		t.Fatalf("Back cancel calls = %d; want 1", cancelCalls.Load())
	}

	finish()
	select {
	case <-afterWait:
	case <-time.After(time.Second):
		t.Fatal("Back did not finish waiting for Test Chat")
	}
	select {
	case rr := <-backDone:
		if rr.Code != http.StatusOK {
			t.Fatalf("Back status=%d body=%s", rr.Code, rr.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("Back did not complete after Test Chat ended")
	}
}

func TestAppOnboardingBackToProvidersCanceledWaiterDoesNotMutate(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhaseTest, 61)
	_, finish := installFakeActiveOnboardingTestForBack(t)
	defer finish()
	canceled := fakeActiveOnboardingTestCanceledForBack(t)
	before, err := env.a.st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/onboarding/back-to-providers", strings.NewReader(`{"revision":61}`)).WithContext(ctx)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- env.serveRequest(req) }()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("Back did not reach Test Chat wait")
	}
	cancel()
	select {
	case rr := <-done:
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_BACK_CANCELED")
	case <-time.After(time.Second):
		t.Fatal("canceled Back waiter did not return")
	}
	after, err := env.a.st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if before.State != after.State || !slices.Equal(before.Stages, after.Stages) {
		t.Fatal("canceled Back waiter mutated state")
	}
}

func TestAppOnboardingBackToProvidersExactRetryDoesNotCancelTest(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhaseTest, 71)
	first := env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":71}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first Back status=%d body=%s", first.Code, first.Body.String())
	}
	cancelCalls, finish := installFakeActiveOnboardingTestForBack(t)
	defer finish()
	retry := env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":71}`)
	if retry.Code != http.StatusOK {
		t.Fatalf("Back retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	if cancelCalls.Load() != 0 {
		t.Fatalf("exact Back retry canceled Test Chat %d times", cancelCalls.Load())
	}
	firstStatus := decodeOnboardingResponse[appOnboardingStatusWire](t, first)
	retryStatus := decodeOnboardingResponse[appOnboardingStatusWire](t, retry)
	if firstStatus.Revision != retryStatus.Revision || !slices.Equal(firstStatus.Providers, retryStatus.Providers) {
		t.Fatalf("retry status=%+v; want %+v", retryStatus, firstStatus)
	}
}

func TestAppOnboardingBackToProvidersRetainedFingerprintSelectionLifecycleStaysProjectable(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	source, _ := seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhaseTest, 111)
	if _, err := env.db.Exec(`DELETE FROM app_onboarding_provider_stages WHERE kind = 'claude-code'`); err != nil {
		t.Fatal(err)
	}
	back := env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":111}`)
	if back.Code != http.StatusOK {
		t.Fatalf("Back status=%d body=%s", back.Code, back.Body.String())
	}
	added := env.serve(http.MethodPut, "/onboarding/providers",
		`{"revision":112,"selected_kinds":["claude-code","codex"]}`)
	if added.Code != http.StatusOK {
		t.Fatalf("add pending status=%d body=%s", added.Code, added.Body.String())
	}
	removedBody := `{"revision":113,"selected_kinds":["codex"]}`
	persona := env.serve(http.MethodPut, "/onboarding/providers", removedBody)
	if persona.Code != http.StatusOK {
		t.Fatalf("remove pending status=%d body=%s", persona.Code, persona.Body.String())
	}
	personaStatus := decodeOnboardingResponse[appOnboardingStatusWire](t, persona)
	if personaStatus.Phase != store.OnboardingPhasePersona || personaStatus.Revision != 114 ||
		len(personaStatus.Providers) != 1 || personaStatus.Providers[0].Kind != "codex" {
		t.Fatalf("Persona status = %+v", personaStatus)
	}
	retry := env.serve(http.MethodPut, "/onboarding/providers", removedBody)
	if retry.Code != http.StatusOK {
		t.Fatalf("remove pending replay status=%d body=%s", retry.Code, retry.Body.String())
	}
	status := env.serve(http.MethodGet, "/onboarding/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", status.Code, status.Body.String())
	}
	persisted, err := env.a.st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State.PersonaFingerprint != source.State.PersonaFingerprint ||
		persisted.State.Phase != store.OnboardingPhasePersona {
		t.Fatalf("retained Persona state = %+v; source=%+v", persisted.State, source.State)
	}

	corrupt := persisted.State
	corrupt.PersonaFingerprint = "malformed-retained-fingerprint"
	env.setState(t, corrupt)
	malformed := env.serve(http.MethodGet, "/onboarding/status", "")
	requireOnboardingCode(t, malformed, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
}

func TestAppOnboardingStatusRejectsMalformedTestFingerprintPrivately(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	snapshot, _ := seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhaseTest, 121)
	malformedFingerprint := "malformed-test-persona-fingerprint"
	state := snapshot.State
	state.PersonaFingerprint = malformedFingerprint
	env.setState(t, state)

	rr := env.serve(http.MethodGet, "/onboarding/status", "")
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
	if strings.Contains(rr.Body.String(), malformedFingerprint) ||
		strings.Contains(strings.ToLower(rr.Body.String()), "fingerprint") {
		t.Fatalf("status exposed malformed Test fingerprint: %s", rr.Body.String())
	}
}

func TestAppOnboardingBackToProvidersRejectsMalformedTestFingerprintBeforeCancellation(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	snapshot, _ := seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhaseTest, 131)
	malformedFingerprint := "malformed-test-persona-fingerprint"
	state := snapshot.State
	state.PersonaFingerprint = malformedFingerprint
	env.setState(t, state)
	before, err := env.a.st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	cancelCalls, finish := installFakeActiveOnboardingTestForBack(t)
	defer finish()
	canceled := fakeActiveOnboardingTestCanceledForBack(t)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":131}`)
	}()
	var rr *httptest.ResponseRecorder
	select {
	case rr = <-done:
	case <-canceled:
		// Let a regressed handler that canceled the runner finish waiting so the
		// RED test terminates and reports the observable cancellation.
		finish()
		select {
		case rr = <-done:
		case <-time.After(time.Second):
			t.Fatal("Back did not return after the regressed cancellation was released")
		}
	case <-time.After(time.Second):
		t.Fatal("Back neither rejected malformed state nor canceled the Test runner")
	}
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE")
	if cancelCalls.Load() != 0 {
		t.Fatalf("malformed Test fingerprint canceled runner %d times", cancelCalls.Load())
	}
	select {
	case <-canceled:
		t.Fatal("malformed Test fingerprint canceled active Test runner")
	default:
	}
	onboardingTestMu.Lock()
	stillActive := onboardingTestActive && !onboardingTestCancelRequested
	onboardingTestMu.Unlock()
	if !stillActive {
		t.Fatal("malformed Test fingerprint changed active Test runner state")
	}
	after, err := env.a.st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if before.State != after.State || !slices.Equal(before.Stages, after.Stages) {
		t.Fatalf("malformed Test fingerprint mutated snapshot: before=%+v after=%+v", before, after)
	}
	if strings.Contains(rr.Body.String(), malformedFingerprint) ||
		strings.Contains(strings.ToLower(rr.Body.String()), "fingerprint") {
		t.Fatalf("Back exposed malformed Test fingerprint: %s", rr.Body.String())
	}
}

func TestAppOnboardingBackToProvidersConcurrentRequestHasOneTransitionAndSafeRetry(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedAppOnboardingBackReadyForTest(t, env, store.OnboardingPhasePersona, 81)
	oldAfterWait := appOnboardingProviderAfterTestWait
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	appOnboardingProviderAfterTestWait = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	t.Cleanup(func() { appOnboardingProviderAfterTestWait = oldAfterWait })

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":81}`)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first Back did not enter transition gap")
	}
	second := env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":81}`)
	requireOnboardingCode(t, second, http.StatusConflict, "ONBOARDING_TRANSITION_BUSY")
	close(release)
	first := <-firstDone
	if first.Code != http.StatusOK {
		t.Fatalf("first Back status=%d body=%s", first.Code, first.Body.String())
	}
	retry := env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":81}`)
	if retry.Code != http.StatusOK {
		t.Fatalf("concurrent loser retry status=%d body=%s", retry.Code, retry.Body.String())
	}
}

var fakeOnboardingTestCanceledForBack atomic.Pointer[chan struct{}]

func installFakeActiveOnboardingTestForBack(t *testing.T) (*atomic.Int32, func()) {
	t.Helper()
	onboardingTestMu.Lock()
	if onboardingTestActive || onboardingProviderTransition != nil {
		onboardingTestMu.Unlock()
		t.Fatal("onboarding Test/transition global is not clean")
	}
	done := make(chan struct{})
	canceled := make(chan struct{})
	var cancelOnce sync.Once
	var cancelCalls atomic.Int32
	onboardingTestActive = true
	onboardingTestDone = done
	onboardingTestCancel = func() {
		cancelCalls.Add(1)
		cancelOnce.Do(func() { close(canceled) })
	}
	onboardingTestCancelRequested = false
	onboardingTestMu.Unlock()
	fakeOnboardingTestCanceledForBack.Store(&canceled)

	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			onboardingTestMu.Lock()
			if onboardingTestDone == done {
				onboardingTestActive = false
				onboardingTestDone = nil
				onboardingTestCancel = nil
				onboardingTestCancelRequested = false
				close(done)
			}
			onboardingTestMu.Unlock()
		})
	}
	t.Cleanup(func() {
		finish()
		fakeOnboardingTestCanceledForBack.Store(nil)
	})
	return &cancelCalls, finish
}

func fakeActiveOnboardingTestCanceledForBack(t *testing.T) <-chan struct{} {
	t.Helper()
	pointer := fakeOnboardingTestCanceledForBack.Load()
	if pointer == nil {
		t.Fatal("fake active onboarding Test is not installed")
	}
	return *pointer
}

func seedAppOnboardingBackReadyForTest(
	t *testing.T,
	env *onboardingRouteTestEnv,
	phase string,
	revision int64,
) (store.OnboardingSnapshot, []string) {
	t.Helper()
	stages := []appOnboardingProviderWire{
		{Kind: "codex", Status: "ready", ProviderID: "codex", AccountID: "codex-back-account", ModelID: "codex-back-model", Position: 0},
		{Kind: "claude-code", Status: "ready", ProviderID: "claude-code", AccountID: "claude-back-account", ModelID: "claude-back-model", Position: 1},
	}
	privateValues := make([]string, 0, len(stages)*2+2)
	for _, stage := range stages {
		if err := env.a.st.EnsureOnboardingProviderForKind(stage.Kind); err != nil {
			t.Fatal(err)
		}
		configDir := filepath.Join(env.dataDir, "private-"+stage.AccountID)
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		email := stage.AccountID + "@private.test"
		if err := env.a.st.CreateLLMAccount(store.LLMAccount{
			ID: stage.AccountID, ProviderID: stage.ProviderID, Label: stage.AccountID,
			Email: email, ConfigDir: configDir,
		}); err != nil {
			t.Fatal(err)
		}
		if err := env.a.st.AddLLMModel(store.LLMModel{
			ProviderID: stage.ProviderID, ModelID: stage.ModelID, Name: stage.ModelID,
			Source: store.LLMModelDiscovered, Available: true,
		}); err != nil {
			t.Fatal(err)
		}
		privateValues = append(privateValues, configDir, email)
	}
	env.replaceProviderStages(t, stages...)
	state := store.OnboardingState{
		Phase: phase, StagedComboID: uuid.NewString(), Revision: revision,
	}
	if phase == store.OnboardingPhaseTest {
		state.PersonaFingerprint = strings.Repeat("d", 64)
		state.TestNonceHash = strings.Repeat("b", 64)
		state.TestExpiresAt = time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
		privateValues = append(privateValues, state.PersonaFingerprint, state.TestNonceHash)
	}
	env.setState(t, state)
	snapshot, err := env.a.st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, privateValues
}

func TestAppOnboardingBackToProvidersWrongMethod(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	req := httptest.NewRequest(http.MethodPut, "/onboarding/back-to-providers", strings.NewReader(`{"revision":1}`))
	req.Header.Set(portalHeader, "1")
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status=%d body=%s", rr.Code, rr.Body.String())
	}
}
