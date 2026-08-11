package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

func TestWorkflowHookDefaultsToContinueAgent(t *testing.T) {
	a := &api{}
	got, err := a.evaluateAppWorkflow(
		ipc.ZaloEventRequest{ThreadID: "t1"},
		ipc.ZaloMessage{ThreadID: "t1", Body: "xin chào"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != appWorkflowContinue {
		t.Fatalf("decision = %q; want %q", got, appWorkflowContinue)
	}
}

type appRouteTestCase struct {
	pattern string
	path    string
}

func appRouteTestCases() []appRouteTestCase {
	return []appRouteTestCase{
		{"GET /onboarding/status", "/onboarding/status"},
		{"PUT /onboarding/provider", "/onboarding/provider"},
		{"POST /onboarding/restart", "/onboarding/restart"},
		{"GET /agent", "/agent"},
		{"PUT /agent", "/agent"},
		{"GET /agent/persona/{name}", "/agent/persona/main"},
		{"PUT /agent/persona/{name}", "/agent/persona/main"},
		{"GET /kb", "/kb"},
		{"POST /kb/upload", "/kb/upload"},
		{"POST /kb/ingest", "/kb/ingest"},
		{"DELETE /kb/ingest", "/kb/ingest"},
		{"GET /kb/model", "/kb/model"},
		{"PUT /kb/model", "/kb/model"},
		{"GET /llm/providers", "/llm/providers"},
		{"POST /llm/providers", "/llm/providers"},
		{"PUT /llm/providers/{id}", "/llm/providers/openai"},
		{"DELETE /llm/providers/{id}", "/llm/providers/openai"},
		{"PUT /llm/providers/{id}/credential", "/llm/providers/openai/credential"},
		{"DELETE /llm/providers/{id}/credential", "/llm/providers/openai/credential"},
		{"POST /llm/providers/test", "/llm/providers/test"},
		{"POST /llm/providers/{id}/test", "/llm/providers/openai/test"},
		{"POST /llm/providers/{id}/discover", "/llm/providers/openai/discover"},
		{"POST /llm/providers/{id}/models", "/llm/providers/openai/models"},
		{"DELETE /llm/providers/{id}/models", "/llm/providers/openai/models"},
		{"DELETE /llm/providers/{id}/accounts/{accountId}", "/llm/providers/codex/accounts/account-a"},
		{"POST /llm/providers/{kind}/connect", "/llm/providers/codex/connect"},
		{"GET /llm/providers/{kind}/connect", "/llm/providers/codex/connect"},
		{"DELETE /llm/providers/{kind}/connect", "/llm/providers/codex/connect"},
		{"GET /llm/route", "/llm/route"},
		{"PUT /llm/route", "/llm/route"},
		{"GET /llm/combos", "/llm/combos"},
		{"POST /llm/combos", "/llm/combos"},
		{"PUT /llm/combos/{id}", "/llm/combos/combo-a"},
		{"POST /llm/combos/{id}/activate", "/llm/combos/combo-a/activate"},
		{"DELETE /llm/combos/{id}", "/llm/combos/combo-a"},
		{"GET /llm/status", "/llm/status"},
		{"GET /memory", "/memory"},
		{"GET /memory/threads/{tid}", "/memory/threads/thread-a"},
		{"GET /memory/threads/{tid}", "/memory/threads/thread-a?uid=member-a"},
		{"POST /memory/threads/{tid}", "/memory/threads/thread-a"},
		{"PUT /memory/threads/{tid}/{id}", "/memory/threads/thread-a/1"},
		{"DELETE /memory/threads/{tid}/{id}", "/memory/threads/thread-a/1"},
		{"POST /memory/threads/{tid}/{id}/approve", "/memory/threads/thread-a/1/approve"},
		{"POST /memory/threads/{tid}/{id}/reject", "/memory/threads/thread-a/1/reject"},
		{"POST /memory/threads/{tid}/{id}/restore", "/memory/threads/thread-a/1/restore"},
		{"GET /memory/lessons", "/memory/lessons"},
		{"POST /memory/lessons", "/memory/lessons"},
		{"PUT /memory/lessons/{id}", "/memory/lessons/1"},
		{"DELETE /memory/lessons/{id}", "/memory/lessons/1"},
	}
}

func TestAppRoutesAreRegisteredAndCookieReachable(t *testing.T) {
	a := &api{}
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)

	for _, tt := range appRouteTestCases() {
		t.Run(tt.pattern, func(t *testing.T) {
			method, _, _ := strings.Cut(tt.pattern, " ")
			req := httptest.NewRequest(method, tt.path, nil)
			_, gotPattern := mux.Handler(req)
			if gotPattern != tt.pattern {
				t.Fatalf("matched pattern = %q; want %q", gotPattern, tt.pattern)
			}
			if !cookieAllowedPaths[tt.pattern] {
				t.Fatalf("cookieAllowedPaths[%q] = false; Portal cannot reach route", tt.pattern)
			}

			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s without credentials returned %d; want %d", method, tt.path,
					recorder.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestAppRoutesRequirePortalHeaderForMutations(t *testing.T) {
	a := &api{portalOpen: true}
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)

	for _, tt := range appRouteTestCases() {
		method, _, _ := strings.Cut(tt.pattern, " ")
		if method == http.MethodGet {
			continue
		}
		t.Run(tt.pattern, func(t *testing.T) {
			req := httptest.NewRequest(method, tt.path, nil)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s without %s returned %d; want %d", method, tt.path,
					portalHeader, recorder.Code, http.StatusUnauthorized)
			}
		})
	}
}

func serveAppPortalRoute(
	t *testing.T,
	mux *http.ServeMux,
	method, path, body string,
	wantStatus int,
) []byte {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set(portalHeader, "1")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s = %d; want %d (body=%s)", method, path,
			recorder.Code, wantStatus, recorder.Body.String())
	}
	return recorder.Body.Bytes()
}

func decodeAppRouteJSON[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode route response %q: %v", body, err)
	}
	return value
}

func appComboByID(combos []comboBody, id string) (comboBody, bool) {
	for _, combo := range combos {
		if combo.ID == id {
			return combo, true
		}
	}
	return comboBody{}, false
}

// Provider and Memory already have authenticated real-mux functional suites. These two journeys
// close the remaining mapping gap for every Combo and Connect pattern instead of merely proving
// that some authenticated wrapper is registered at each path.
func TestAppRoutesDispatchEveryComboHandler(t *testing.T) {
	a := newAppRouteAPI(t)
	a.portalOpen = true
	t.Cleanup(func() { connectMgr = nil })
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)

	initial := decodeAppRouteJSON[struct {
		Combos []comboBody `json:"combos"`
	}](t, serveAppPortalRoute(t, mux, http.MethodGet, "/llm/combos", "", http.StatusOK))
	if len(initial.Combos) != 0 {
		t.Fatalf("initial GET /llm/combos returned %d combos; want empty list", len(initial.Combos))
	}

	created := decodeAppRouteJSON[comboBody](t, serveAppPortalRoute(
		t, mux, http.MethodPost, "/llm/combos", `{"name":"Mux route","type":"fallback"}`,
		http.StatusOK,
	))
	if created.ID == "" || created.Name != "Mux route" || created.Type != "fallback" ||
		created.Active || created.Revision != 1 {
		t.Fatalf("POST /llm/combos = %+v; want new inactive fallback combo at revision 1", created)
	}

	seedComboAPIProvider(t, a, "mux-openai", "mux-model")
	replaced := decodeAppRouteJSON[comboBody](t, serveAppPortalRoute(
		t, mux, http.MethodPut, "/llm/combos/"+created.ID,
		`{"revision":1,"type":"round_robin","entries":[{"provider_id":"mux-openai","model_id":"mux-model","enabled":true}]}`,
		http.StatusOK,
	))
	if replaced.ID != created.ID || replaced.Revision != 2 || replaced.Type != "round_robin" ||
		len(replaced.Entries) != 1 || replaced.Entries[0].ProviderID != "mux-openai" ||
		replaced.Entries[0].ModelID != "mux-model" || !replaced.Entries[0].Enabled {
		t.Fatalf("PUT /llm/combos/%s = %+v; want revised route member", created.ID, replaced)
	}

	activated := decodeAppRouteJSON[struct {
		OK bool `json:"ok"`
	}](t, serveAppPortalRoute(t, mux, http.MethodPost,
		"/llm/combos/"+created.ID+"/activate", "", http.StatusOK))
	if !activated.OK {
		t.Fatal("POST combo activate returned ok=false; want true")
	}
	stored, err := a.st.LLMCombos()
	if err != nil {
		t.Fatalf("LLMCombos after activate = _, %v", err)
	}
	var active store.LLMCombo
	foundActive := false
	for _, combo := range stored {
		if combo.ID == created.ID {
			active = combo
			foundActive = true
			break
		}
	}
	if !foundActive || !active.Active {
		t.Fatalf("combo %q after activate = %+v, present=%v; want active", created.ID, active, foundActive)
	}

	keeper, err := a.st.CreateLLMCombo("Keeper", "fallback")
	if err != nil {
		t.Fatalf("CreateLLMCombo(keeper) = _, %v", err)
	}
	if err := a.st.SetActiveLLMCombo(keeper.ID); err != nil {
		t.Fatalf("SetActiveLLMCombo(keeper) = %v", err)
	}
	serveAppPortalRoute(t, mux, http.MethodDelete, "/llm/combos/"+created.ID, "", http.StatusNoContent)

	remaining := decodeAppRouteJSON[struct {
		Combos []comboBody `json:"combos"`
	}](t, serveAppPortalRoute(t, mux, http.MethodGet, "/llm/combos", "", http.StatusOK))
	if _, found := appComboByID(remaining.Combos, created.ID); found {
		t.Fatalf("GET /llm/combos still contains deleted combo %q: %+v", created.ID, remaining.Combos)
	}
	if got, found := appComboByID(remaining.Combos, keeper.ID); !found || !got.Active {
		t.Fatalf("GET /llm/combos keeper = %+v, present=%v; want sole active keeper", got, found)
	}
}

type appRouteConnectRunner struct {
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (r *appRouteConnectRunner) detect(string) (bool, error) {
	close(r.entered)
	<-r.release
	close(r.returned)
	return false, context.Canceled
}

func (*appRouteConnectRunner) install(context.Context, string, func(string)) error {
	return context.Canceled
}

func (*appRouteConnectRunner) login(context.Context, string, string) (string, string, func() error, error) {
	return "", "", nil, context.Canceled
}

func (*appRouteConnectRunner) pollAuth(string, string) authState  { return authUnknown }
func (*appRouteConnectRunner) accountLabel(string, string) string { return "" }

func TestAppRoutesDispatchEveryConnectHandler(t *testing.T) {
	a := newAppRouteAPI(t)
	a.portalOpen = true
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)

	runner := &appRouteConnectRunner{
		entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{}),
	}
	manager := newTestManager(t, runner, func(string) error { return nil }, func(store.LLMAccount) error { return nil })
	connectMgr = manager
	t.Cleanup(func() {
		manager.cancel("codex")
		connectMgr = nil
	})
	released := false
	defer func() {
		if !released {
			close(runner.release)
		}
	}()

	started := decodeAppRouteJSON[connectState](t, serveAppPortalRoute(
		t, mux, http.MethodPost, "/llm/providers/codex/connect", `{"label":"Mux account"}`,
		http.StatusOK,
	))
	if started.Kind != "codex" || started.Phase != phaseDetecting {
		t.Fatalf("POST connect = %+v; want codex/detecting", started)
	}
	select {
	case <-runner.entered:
	case <-time.After(time.Second):
		t.Fatal("POST connect did not enter deterministic runner")
	}

	status := decodeAppRouteJSON[connectState](t, serveAppPortalRoute(
		t, mux, http.MethodGet, "/llm/providers/codex/connect", "", http.StatusOK,
	))
	if status.Kind != "codex" || status.Phase != phaseDetecting {
		t.Fatalf("GET connect status = %+v; want current codex/detecting state", status)
	}

	canceled := decodeAppRouteJSON[struct {
		OK bool `json:"ok"`
	}](t, serveAppPortalRoute(t, mux, http.MethodDelete,
		"/llm/providers/codex/connect", "", http.StatusOK))
	if !canceled.OK {
		t.Fatal("DELETE connect returned ok=false; want true")
	}
	manager.mu.Lock()
	canceledJob := manager.job
	manager.mu.Unlock()
	status = decodeAppRouteJSON[connectState](t, serveAppPortalRoute(
		t, mux, http.MethodGet, "/llm/providers/codex/connect", "", http.StatusOK,
	))
	if status.Kind != "codex" || status.Phase != phaseCanceled {
		t.Fatalf("GET connect status after DELETE = %+v; want codex/canceled", status)
	}

	close(runner.release)
	released = true
	select {
	case <-runner.returned:
	case <-time.After(time.Second):
		t.Fatal("deterministic connect runner did not stop")
	}
	select {
	case <-canceledJob.done:
	case <-time.After(time.Second):
		t.Fatal("canceled connect job did not finish cleanup")
	}
	// Cancellation is terminal even after cleanup: the runner's later error must not overwrite it.
	if stopped := canceledJob.snapshot(); stopped.Phase != phaseCanceled {
		t.Fatalf("released deterministic runner phase = %q; want canceled", stopped.Phase)
	}
}

func TestAppRouteConnectCancelReportsFalseAfterSuccessClaim(t *testing.T) {
	a := newAppRouteAPI(t)
	a.portalOpen = true
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)

	ensureEntered := make(chan struct{})
	ensureRelease := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(ensureRelease) }) }
	runner := &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedIn}
	manager := newTestManager(t, runner,
		func(string) error {
			close(ensureEntered)
			<-ensureRelease
			return nil
		},
		func(store.LLMAccount) error { return nil },
	)
	connectMgr = manager
	t.Cleanup(func() {
		release()
		manager.cancel("codex")
		connectMgr = nil
	})

	decodeAppRouteJSON[connectState](t, serveAppPortalRoute(
		t, mux, http.MethodPost, "/llm/providers/codex/connect", `{"label":"Claimed account"}`,
		http.StatusOK,
	))
	manager.mu.Lock()
	job := manager.job
	manager.mu.Unlock()
	select {
	case <-ensureEntered:
	case <-time.After(time.Second):
		t.Fatal("connect did not reach claimed persistence gate")
	}

	canceled := decodeAppRouteJSON[struct {
		OK bool `json:"ok"`
	}](t, serveAppPortalRoute(t, mux, http.MethodDelete,
		"/llm/providers/codex/connect", "", http.StatusOK))
	if canceled.OK {
		t.Fatal("DELETE connect returned ok=true after success claim; want false")
	}
	release()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("claimed connect job did not finish")
	}
	if st := job.snapshot(); st.Phase != phaseConnected {
		t.Fatalf("claimed connect phase = %q; want connected", st.Phase)
	}
}
