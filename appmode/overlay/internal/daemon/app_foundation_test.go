package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentdc/internal/ipc"
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

func TestAppRoutesAreRegisteredAndCookieReachable(t *testing.T) {
	a := &api{}
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)

	tests := []struct {
		pattern string
		path    string
	}{
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
		{"GET /llm/route", "/llm/route"},
		{"PUT /llm/route", "/llm/route"},
		{"GET /llm/status", "/llm/status"},
	}

	for _, tt := range tests {
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
		})
	}
}
