package daemon

import (
	"net/http"
)

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
	"GET /llm/route",
	"PUT /llm/route",
	"GET /llm/status",
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
	mux.Handle("GET /llm/route", a.auth(a.handleLLMRouteGet))
	mux.Handle("PUT /llm/route", a.auth(a.handleLLMRoutePut))
	mux.Handle("GET /llm/status", a.auth(a.handleLLMStatus))
}
