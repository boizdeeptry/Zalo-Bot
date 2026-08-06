package daemon

import (
	"net/http"
	"os"
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
	a.bootstrapLLMRoute()
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

// bootstrapLLMRoute gieo chuỗi fallback mặc định lúc daemon lên, mang theo lựa chọn mô hình cũ.
//
// Ở đây vì registerAppRoutes là hàm duy nhất của lớp Portal chạy lúc khởi động. Gieo là no-op khi
// route đã có (xem BootstrapClaudeRoute), nên máy đang dùng không bị đặt lại mỗi lần mở.
//
// Lỗi chỉ được LOG: một chuỗi chưa gieo được làm trang Mô hình trống, còn từ chối đăng ký route
// thì làm sập cả Portal — kể cả những trang không liên quan tới LLM.
func (a *api) bootstrapLLMRoute() {
	// Test bảng định tuyến dựng api rỗng để chỉ hỏi mux khớp pattern nào; nó không có store, và
	// không có cửa này thì đăng ký route trở thành một lượt nil-deref.
	if a.st == nil {
		return
	}
	if err := a.st.BootstrapClaudeRoute(a.legacyZaloModel()); err != nil {
		a.logger.Error("llm route: không gieo được chuỗi mặc định", "err", err)
	}
}

// legacyZaloModel đọc lựa chọn mô hình cũ trong data\model.txt.
//
// Cùng danh sách CHO PHÉP mà PUT /kb/model dùng: giá trị này thành ModelID của mắt xích Claude
// Code, và mắt xích đó dựng ra tham số --model của một tiến trình thật. Một tệp bị sửa tay không
// được đi thẳng vào dòng lệnh đó.
//
// Tệp KHÔNG bị xoá hay ghi lại: Chay.bat vẫn đọc nó, nên nó là đường lùi nếu lớp Portal bị gỡ ra.
func (a *api) legacyZaloModel() string {
	// Mặc định là mô hình vòng trực ĐANG chạy, để chuỗi vừa gieo trả lời giống hệt hôm qua.
	// defaultZaloModel khi vòng trực tắt: chuỗi vẫn phải hợp lệ để trang Mô hình có gì để hiện.
	model := defaultZaloModel
	if a.zalo != nil {
		model = a.zalo.cfg.Model
	}
	saved, err := os.ReadFile(modelFile(a.cfg.Dir))
	if err != nil {
		return model
	}
	if choice, ok := normalizeModelChoice(string(saved)); ok {
		return choice
	}
	return model
}
