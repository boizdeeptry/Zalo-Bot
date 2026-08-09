package daemon

import "net/http"

var appPortalRoutePatterns = []string{
	"GET /agent",
	"PUT /agent",
	"GET /agent/persona/{name}",
	"PUT /agent/persona/{name}",
	"GET /kb",
	"POST /kb/upload",
	"POST /kb/ingest",
	"DELETE /kb/ingest",
	"GET /kb/model",
	"PUT /kb/model",
	"GET /memory",
	"GET /memory/threads/{tid}",
	"POST /memory/threads/{tid}",
	"PUT /memory/threads/{tid}/{id}",
	"DELETE /memory/threads/{tid}/{id}",
	"GET /memory/lessons",
	"POST /memory/lessons",
	"PUT /memory/lessons/{id}",
	"DELETE /memory/lessons/{id}",
}

func init() {
	for _, pattern := range appPortalRoutePatterns {
		cookieAllowedPaths[pattern] = true
	}
}

func (a *api) registerAppRoutes(mux *http.ServeMux) {
	mux.Handle("GET /agent", a.auth(a.handleAgentGet))
	mux.Handle("PUT /agent", a.auth(a.handleAgentPut))
	mux.Handle("GET /agent/persona/{name}", a.auth(a.handlePersonaGet))
	mux.Handle("PUT /agent/persona/{name}", a.auth(a.handlePersonaPut))
	mux.Handle("GET /kb", a.auth(a.handleKBList))
	mux.Handle("POST /kb/upload", a.auth(a.handleKBUpload))
	mux.Handle("POST /kb/ingest", a.auth(a.handleKBIngest))
	mux.Handle("DELETE /kb/ingest", a.auth(a.handleKBIngestStop))
	mux.Handle("GET /kb/model", a.auth(a.handleKBModelGet))
	mux.Handle("PUT /kb/model", a.auth(a.handleKBModelPut))
	mux.Handle("GET /memory", a.auth(a.handleMemoryOverview))
	mux.Handle("GET /memory/threads/{tid}", a.auth(a.handleMemoryThreadGet))
	mux.Handle("POST /memory/threads/{tid}", a.auth(a.handleMemoryThreadPost))
	mux.Handle("PUT /memory/threads/{tid}/{id}", a.auth(a.handleMemoryThreadPut))
	mux.Handle("DELETE /memory/threads/{tid}/{id}", a.auth(a.handleMemoryThreadDelete))
	mux.Handle("GET /memory/lessons", a.auth(a.handleMemoryLessonsGet))
	mux.Handle("POST /memory/lessons", a.auth(a.handleMemoryLessonPost))
	mux.Handle("PUT /memory/lessons/{id}", a.auth(a.handleMemoryLessonPut))
	mux.Handle("DELETE /memory/lessons/{id}", a.auth(a.handleMemoryLessonDelete))
}
