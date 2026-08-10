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

func TestMemoryFoundationRoutesAreRegisteredAndCookieReachable(t *testing.T) {
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
		{"GET /memory", "/memory"},
		{"GET /memory/threads/{tid}", "/memory/threads/thread-a"},
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
