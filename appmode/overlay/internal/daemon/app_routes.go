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
