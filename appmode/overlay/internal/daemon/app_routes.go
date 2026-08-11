package daemon

import "net/http"

var appPortalRoutePatterns = []string{
	"GET /onboarding/status",
	"PUT /onboarding/provider",
	"POST /onboarding/setup",
	"POST /onboarding/restart",
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
	// Trang Provider. Cùng hạng với /kb: cookie đọc và ghi được, mọi mutation vẫn phải mang
	// portalHeader. Không endpoint nào ở đây trả credential ra, nên một phiên trình duyệt đọc
	// được danh sách vẫn không lấy được khoá.
	"GET /llm/providers",
	"POST /llm/providers",
	"PUT /llm/providers/{id}",
	"DELETE /llm/providers/{id}",
	"PUT /llm/providers/{id}/credential",
	"DELETE /llm/providers/{id}/credential",
	"POST /llm/providers/test",
	"POST /llm/providers/{id}/test",
	"POST /llm/providers/{id}/discover",
	"POST /llm/providers/{id}/models",
	"DELETE /llm/providers/{id}/models",
	"DELETE /llm/providers/{id}/accounts/{accountId}",
	"POST /llm/providers/{kind}/connect",
	"GET /llm/providers/{kind}/connect",
	"DELETE /llm/providers/{kind}/connect",
	"GET /llm/route",
	"PUT /llm/route",
	// Combos: cùng hạng an toàn với /llm/route — cookie đọc/ghi được, mutation vẫn cần portalHeader,
	// và không bề mặt nào trả credential hay config dir (chỉ provider_id/model_id).
	"GET /llm/combos",
	"POST /llm/combos",
	"PUT /llm/combos/{id}",
	"POST /llm/combos/{id}/activate",
	"DELETE /llm/combos/{id}",
	"GET /llm/status",
	"GET /memory",
	"GET /memory/threads/{tid}",
	"POST /memory/threads/{tid}",
	"PUT /memory/threads/{tid}/{id}",
	"DELETE /memory/threads/{tid}/{id}",
	"POST /memory/threads/{tid}/{id}/approve",
	"POST /memory/threads/{tid}/{id}/reject",
	"POST /memory/threads/{tid}/{id}/restore",
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
	mux.Handle("GET /onboarding/status", a.auth(a.handleOnboardingStatus))
	mux.Handle("PUT /onboarding/provider", a.auth(a.handleOnboardingProvider))
	mux.Handle("POST /onboarding/setup", a.auth(a.handleOnboardingSetup))
	mux.Handle("POST /onboarding/restart", a.auth(a.handleOnboardingRestart))
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
	mux.Handle("GET /llm/providers", a.auth(a.handleLLMProviderList))
	mux.Handle("POST /llm/providers", a.auth(a.handleLLMProviderCreate))
	mux.Handle("PUT /llm/providers/{id}", a.auth(a.handleLLMProviderUpdate))
	mux.Handle("DELETE /llm/providers/{id}", a.auth(a.handleLLMProviderDelete))
	mux.Handle("PUT /llm/providers/{id}/credential", a.auth(a.handleLLMCredentialPut))
	mux.Handle("DELETE /llm/providers/{id}/credential", a.auth(a.handleLLMCredentialDelete))
	// Đứng TRƯỚC /llm/providers/{id}/test trong tệp này chỉ để đọc cho thuận; ServeMux chọn theo
	// độ cụ thể chứ không theo thứ tự, và hai mẫu này khác số đoạn nên không tranh nhau.
	mux.Handle("POST /llm/providers/test", a.auth(a.handleLLMProviderDraftTest))
	mux.Handle("POST /llm/providers/{id}/test", a.auth(a.handleLLMProviderTest))
	mux.Handle("POST /llm/providers/{id}/discover", a.auth(a.handleLLMProviderDiscover))
	mux.Handle("POST /llm/providers/{id}/models", a.auth(a.handleLLMModelAdd))
	mux.Handle("DELETE /llm/providers/{id}/models", a.auth(a.handleLLMModelDelete))
	mux.Handle("DELETE /llm/providers/{id}/accounts/{accountId}", a.auth(a.handleLLMAccountDelete))

	// connectMgr is refreshed on every registerAppRoutes call (not lazily nil-guarded) so it always
	// reflects the api that registered the routes. Production registers once; tests each get their
	// own valid manager and never inherit a prior test's closed store / nil logger.
	connectMgr = a.newConnectManager()
	// Gieo model tĩnh của claude-code ngay khi khởi động: nó là provider luôn có sẵn (seeded), chạy
	// qua runClaude nên KHÔNG có adapter để discover — không gieo ở đây thì detail + combo picker
	// trống model dù chưa ai connect. Idempotent (ReplaceLLMModels thay trọn nguồn discovered).
	ensureCLIProviderModels(a.st, a.logger, "claude-code", "claude-code")
	mux.Handle("POST /llm/providers/{kind}/connect", a.auth(a.handleLLMConnectStart))
	mux.Handle("GET /llm/providers/{kind}/connect", a.auth(a.handleLLMConnectStatus))
	mux.Handle("DELETE /llm/providers/{kind}/connect", a.auth(a.handleLLMConnectCancel))
	mux.Handle("GET /llm/route", a.auth(a.handleLLMRouteGet))
	mux.Handle("PUT /llm/route", a.auth(a.handleLLMRoutePut))
	mux.Handle("GET /llm/combos", a.auth(a.handleLLMComboList))
	mux.Handle("POST /llm/combos", a.auth(a.handleLLMComboCreate))
	mux.Handle("PUT /llm/combos/{id}", a.auth(a.handleLLMComboReplace))
	mux.Handle("POST /llm/combos/{id}/activate", a.auth(a.handleLLMComboActivate))
	mux.Handle("DELETE /llm/combos/{id}", a.auth(a.handleLLMComboDelete))
	mux.Handle("GET /llm/status", a.auth(a.handleLLMStatus))
	mux.Handle("GET /memory", a.auth(a.handleMemoryOverview))
	mux.Handle("GET /memory/threads/{tid}", a.auth(a.handleMemoryThreadGet))
	mux.Handle("POST /memory/threads/{tid}", a.auth(a.handleMemoryThreadPost))
	mux.Handle("PUT /memory/threads/{tid}/{id}", a.auth(a.handleMemoryThreadPut))
	mux.Handle("DELETE /memory/threads/{tid}/{id}", a.auth(a.handleMemoryThreadDelete))
	mux.Handle("POST /memory/threads/{tid}/{id}/approve", a.auth(a.handleMemoryThreadApprove))
	mux.Handle("POST /memory/threads/{tid}/{id}/reject", a.auth(a.handleMemoryThreadReject))
	mux.Handle("POST /memory/threads/{tid}/{id}/restore", a.auth(a.handleMemoryThreadRestore))
	mux.Handle("GET /memory/lessons", a.auth(a.handleMemoryLessonsGet))
	mux.Handle("POST /memory/lessons", a.auth(a.handleMemoryLessonPost))
	mux.Handle("PUT /memory/lessons/{id}", a.auth(a.handleMemoryLessonPut))
	mux.Handle("DELETE /memory/lessons/{id}", a.auth(a.handleMemoryLessonDelete))
}
