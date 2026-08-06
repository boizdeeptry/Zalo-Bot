package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"agentdc/internal/config"
	"agentdc/internal/store"
)

// Canary của tầng Portal. Khác llmTestKey của app_llm_http_test.go có chủ đích: khi một trong hai
// chuỗi này xuất hiện ở chỗ không được phép, cái tên nói ngay nó rò từ tầng adapter hay tầng API.
//
// Cả hai MỌC RA từ llmPackageCanary thay vì là hai chuỗi rời: cửa chặn gói
// (tests/build-app.Tests.ps1) quét đúng một chuỗi con trong gói đã dựng, nên một khoá thử nghiệm
// không mang chuỗi con đó sẽ đi ra bản bán mà không phép quét nào thấy.
//
// Cả hai mang tiền tố "sk-" vì đó là hình dạng thật của khoá OpenAI, và mẫu che cuối cùng trong
// sanitizeProviderError bám vào chính tiền tố đó — canary không giống khoá thật thì test che sẽ
// xanh trên một mẫu không bao giờ chạy.
const (
	llmAPIKey     = llmPackageCanary + "-portal-4Hn8Qw2ZxL6vB9td"
	llmAPINextKey = llmPackageCanary + "-portal-7Rk1Mv5YcT3pE8ws"
	llmAPIToken   = "master-token-for-llm-api-tests"
)

// syncLogBuffer gom log của daemon để test soi. Có khoá vì handler chạy trên goroutine của
// httptest.Server còn phần kiểm thì chạy trên goroutine của test.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// stubLLMAdapter thay bốn adapter thật trong test.
//
// Giữ CẢ bản sao lẫn chính lát cắt mà handler truyền vào: bản sao trả lời "adapter có nhận đúng
// khoá không", còn lát cắt trả lời "handler có xoá bản rõ sau lượt gọi không". Một trong hai thôi
// thì một trong hai câu hỏi đó không kiểm được.
type stubLLMAdapter struct {
	mu            sync.Mutex
	testErr       error
	discovered    []store.LLMModel
	discoverErr   error
	seenSecret    string
	seenSlice     []byte
	seenModel     string
	testCalls     int
	discoverCalls int
}

func (s *stubLLMAdapter) observe(credential []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenSecret = string(credential)
	s.seenSlice = credential
}

func (s *stubLLMAdapter) Generate(context.Context, llmRequest, []byte) (llmResponse, error) {
	return llmResponse{}, errors.New("stub adapter không sinh nội dung")
}

func (s *stubLLMAdapter) Test(_ context.Context, model string, credential []byte) error {
	s.observe(credential)
	s.mu.Lock()
	s.seenModel = model
	s.testCalls++
	err := s.testErr
	s.mu.Unlock()
	return err
}

func (s *stubLLMAdapter) Discover(_ context.Context, credential []byte) ([]store.LLMModel, error) {
	s.observe(credential)
	s.mu.Lock()
	s.discoverCalls++
	models, err := s.discovered, s.discoverErr
	s.mu.Unlock()
	return models, err
}

func (s *stubLLMAdapter) secret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seenSecret
}

func (s *stubLLMAdapter) slice() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seenSlice
}

// --- harness ---

type llmAPIHarness struct {
	t    *testing.T
	api  *api
	srv  *httptest.Server
	st   *store.Store
	logs *syncLogBuffer
	stub *stubLLMAdapter
}

func newLLMAPIHarness(t *testing.T) *llmAPIHarness {
	t.Helper()
	// Gần như mọi test ở đây lưu rồi đọc lại một khoá, nên bản build không có kho khoá thì cả
	// tệp không nói được gì. Bỏ qua theo đúng lối app_secret_test.go dùng, thay vì đỏ hàng loạt
	// trên một nền tảng mà hợp đồng đã do TestProtectProviderSecretFailsClosed giữ.
	requireCredentialProtection(t)

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf(`store.Open(":memory:") = %v; want nil`, err)
	}
	t.Cleanup(func() { _ = st.Close() })

	logs := &syncLogBuffer{}
	a := &api{
		cfg:    config.Config{Dir: t.TempDir(), Token: llmAPIToken},
		st:     st,
		logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		portal: newPortalStore(),
	}
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Adapter giả đi qua chính newLLMAdapter để danh sách kind CHO PHÉP vẫn là thứ thật đang
	// chặn: nếu seam tự nhận mọi kind thì test "kind lạ bị từ chối" sẽ xanh vì lý do sai.
	stub := &stubLLMAdapter{}
	previous := llmAdapterFor
	llmAdapterFor = func(kind, providerID string, client *http.Client, logger *slog.Logger) (providerAdapter, bool) {
		if _, ok := previous(kind, providerID, client, logger); !ok {
			return nil, false
		}
		return stub, true
	}
	t.Cleanup(func() { llmAdapterFor = previous })

	return &llmAPIHarness{t: t, api: a, srv: srv, st: st, logs: logs, stub: stub}
}

type llmAPIResponse struct {
	status int
	raw    string
}

// do gửi một request đã xác thực bằng master token.
func (h *llmAPIHarness) do(method, path, body string) llmAPIResponse {
	h.t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, h.srv.URL+path, reader)
	if err != nil {
		h.t.Fatalf("NewRequestWithContext(%s %s) = %v; want nil", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+llmAPIToken)
	req.Header.Set(portalHeader, "1")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return h.send(req)
}

func (h *llmAPIHarness) send(req *http.Request) llmAPIResponse {
	h.t.Helper()
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatalf("Do(%s %s) = %v; want nil", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // thân đã đọc hết ngay dưới đây
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(resp.Body); err != nil {
		h.t.Fatalf("read body of %s %s = %v; want nil", req.Method, req.URL.Path, err)
	}
	return llmAPIResponse{status: resp.StatusCode, raw: raw.String()}
}

// object giải mã thân JSON thành map để test đọc trường mà không cần một struct cho mỗi endpoint.
func (r llmAPIResponse) object(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.raw), &out); err != nil {
		t.Fatalf("Unmarshal(%q) = %v; want nil", r.raw, err)
	}
	return out
}

func (r llmAPIResponse) errorCode(t *testing.T) string {
	t.Helper()
	envelope, ok := r.object(t)["error"].(map[string]any)
	if !ok {
		t.Fatalf("response body %q has no error object; want the error envelope", r.raw)
	}
	code, _ := envelope["code"].(string)
	return code
}

func (h *llmAPIHarness) mustStatus(got llmAPIResponse, want int, what string) llmAPIResponse {
	h.t.Helper()
	if got.status != want {
		h.t.Fatalf("%s status = %d, want %d; body = %s", what, got.status, want, got.raw)
	}
	return got
}

// createProvider dựng một Provider API kèm khoá và trả về id do máy chủ sinh.
func (h *llmAPIHarness) createProvider(kind, name, credential string) string {
	h.t.Helper()
	body := fmt.Sprintf(`{"kind":%q,"name":%q,"enabled":true,"credential":%q}`, kind, name, credential)
	resp := h.mustStatus(h.do(http.MethodPost, "/llm/providers", body), http.StatusCreated, "create provider")
	provider, ok := resp.object(h.t)["provider"].(map[string]any)
	if !ok {
		h.t.Fatalf("create provider body = %s; want a provider object", resp.raw)
	}
	id, _ := provider["id"].(string)
	if id == "" {
		h.t.Fatalf("create provider returned empty id; body = %s", resp.raw)
	}
	return id
}

// seedClaudeModel thêm một model cho Provider claude-code hệ thống.
//
// Bootstrap CŨ tự gieo claude-code/haiku lúc khởi động; §6 bỏ gieo (máy mới đi qua onboarding),
// nên test nào lưu một chuỗi qua claude-code phải tự thêm model trước — validateLLMRoute từ chối
// một mắt xích trỏ tới model không tồn tại.
func (h *llmAPIHarness) seedClaudeModel(model string) {
	h.t.Helper()
	if err := h.st.AddLLMModel(store.LLMModel{
		ProviderID: "claude-code", ModelID: model, Name: model,
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		h.t.Fatalf("AddLLMModel(claude-code/%s) = %v; want nil", model, err)
	}
}

// storedSecret mở khoá đang lưu của một Provider. Đây là cách DUY NHẤT test nhìn được bản rõ —
// không endpoint nào trả nó ra.
func (h *llmAPIHarness) storedSecret(providerID string) string {
	h.t.Helper()
	cipher, err := h.st.LLMCredentialCipher(providerID)
	if err != nil {
		h.t.Fatalf("LLMCredentialCipher(%q) = %v; want nil", providerID, err)
	}
	plain, err := unprotectProviderSecret(cipher)
	if err != nil {
		h.t.Fatalf("unprotectProviderSecret(%q) = %v; want nil", providerID, err)
	}
	defer clear(plain)
	return string(plain)
}

// assertNoSecret là khẳng định trung tâm của tệp này: không thân phản hồi và không dòng log nào
// được mang khoá, dù ở dạng bản rõ hay bản mã.
func (h *llmAPIHarness) assertNoSecret(what string, extra ...string) {
	h.t.Helper()
	haystacks := append([]string{"log", h.logs.String()}, extra...)
	for i := 0; i+1 < len(haystacks); i += 2 {
		for _, secret := range []string{llmAPIKey, llmAPINextKey} {
			if strings.Contains(haystacks[i+1], secret) {
				h.t.Errorf("%s: %s chứa credential %q; nội dung = %s",
					what, haystacks[i], secret, haystacks[i+1])
			}
		}
	}
}

// --- xác thực ---

func TestLLMAPIRequiresCookieSessionAndPortalHeader(t *testing.T) {
	h := newLLMAPIHarness(t)
	key, err := h.api.portal.mintKey()
	if err != nil {
		t.Fatalf("mintKey() = %v; want nil", err)
	}
	session, ok := h.api.portal.exchangeKey(key)
	if !ok {
		t.Fatal("exchangeKey() = _, false; want a session token")
	}

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		cookie     bool
		portal     bool
		wantStatus int
	}{
		{"đọc không có gì", http.MethodGet, "/llm/providers", "", false, false, http.StatusUnauthorized},
		{"đọc bằng cookie", http.MethodGet, "/llm/providers", "", true, false, http.StatusOK},
		{"ghi bằng cookie thiếu header", http.MethodPost, "/llm/providers",
			`{"kind":"openai","name":"OpenAI"}`, true, false, http.StatusUnauthorized},
		{"ghi bằng cookie đủ header", http.MethodPost, "/llm/providers",
			`{"kind":"openai","name":"OpenAI"}`, true, true, http.StatusCreated},
		{"ghi không có gì", http.MethodPost, "/llm/providers",
			`{"kind":"openai","name":"OpenAI"}`, false, true, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(
				context.Background(), tc.method, h.srv.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("NewRequestWithContext(%s %s) = %v; want nil", tc.method, tc.path, err)
			}
			if tc.cookie {
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			}
			if tc.portal {
				req.Header.Set(portalHeader, "1")
			}
			got := h.send(req)
			if got.status != tc.wantStatus {
				t.Fatalf("%s %s (cookie=%v, portal=%v) status = %d, want %d; body = %s",
					tc.method, tc.path, tc.cookie, tc.portal, got.status, tc.wantStatus, got.raw)
			}
		})
	}
}

// --- hình dạng request ---

func TestLLMAPIDecodesStrictlyWithinOneMiB(t *testing.T) {
	h := newLLMAPIHarness(t)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{"trường lạ", `{"kind":"openai","name":"OpenAI","base_url":"http://evil.invalid"}`,
			http.StatusBadRequest, "INVALID_REQUEST"},
		{"JSON hỏng", `{"kind":`, http.StatusBadRequest, "INVALID_REQUEST"},
		{"vượt trần 1 MiB",
			`{"kind":"openai","name":"` + strings.Repeat("a", llmRequestCap) + `"}`,
			http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := h.do(http.MethodPost, "/llm/providers", tc.body)
			if got.status != tc.wantStatus {
				t.Fatalf("POST /llm/providers (%s) status = %d, want %d; body = %s",
					tc.name, got.status, tc.wantStatus, got.raw)
			}
			if code := got.errorCode(t); code != tc.wantCode {
				t.Errorf("POST /llm/providers (%s) error code = %q, want %q", tc.name, code, tc.wantCode)
			}
		})
	}

	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list after rejects")
	providers, _ := list.object(t)["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("rejected requests changed the store: %d providers, want 1 (only claude-code)", len(providers))
	}
}

func TestLLMAPIRejectsProviderKindOutsideAllowlist(t *testing.T) {
	h := newLLMAPIHarness(t)
	got := h.do(http.MethodPost, "/llm/providers", `{"kind":"evil","name":"Nhà lạ"}`)
	if got.status != http.StatusUnprocessableEntity {
		t.Fatalf("create with kind=evil status = %d, want 422; body = %s", got.status, got.raw)
	}
	if code := got.errorCode(t); code != "PROVIDER_KIND_UNSUPPORTED" {
		t.Errorf("create with kind=evil error code = %q, want PROVIDER_KIND_UNSUPPORTED", code)
	}
	// claude_code là kind của Provider hệ thống: nó tồn tại nhưng không được tạo thêm bản thứ hai,
	// nếu không thì một chuỗi fallback có thể kết thúc bằng một "Claude Code" do người dùng dựng.
	got = h.do(http.MethodPost, "/llm/providers", `{"kind":"claude_code","name":"Bản sao"}`)
	if got.status != http.StatusUnprocessableEntity {
		t.Fatalf("create with kind=claude_code status = %d, want 422; body = %s", got.status, got.raw)
	}
}

func TestLLMAPIGeneratesStableIDsAndFixedEndpoints(t *testing.T) {
	h := newLLMAPIHarness(t)

	if first := h.createProvider("openai", "OpenAI chính", ""); first != "openai" {
		t.Errorf("first openai provider id = %q, want %q", first, "openai")
	}
	second := h.createProvider("openai", "OpenAI dự phòng", "")
	if second != "openai-2" {
		t.Errorf("second openai provider id = %q, want %q", second, "openai-2")
	}
	// Đổi tên KHÔNG đổi id: route trỏ tới id, nên một id chạy theo tên là một chuỗi fallback tự
	// đứt mỗi lần người dùng sửa nhãn hiển thị.
	h.mustStatus(h.do(http.MethodPut, "/llm/providers/"+second,
		`{"name":"Tên khác hẳn","enabled":true}`), http.StatusOK, "rename provider")
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")

	body := list.object(t)
	wantEndpoints := map[string]string{
		"openai":     openAIBase,
		"anthropic":  anthropicBase,
		"gemini":     geminiBase,
		"openrouter": openRouterBase,
	}
	kinds, _ := body["kinds"].([]any)
	if len(kinds) != len(wantEndpoints) {
		t.Fatalf("kinds count = %d, want %d; body = %s", len(kinds), len(wantEndpoints), list.raw)
	}
	for _, entry := range kinds {
		kind, _ := entry.(map[string]any)
		name, _ := kind["kind"].(string)
		endpoint, _ := kind["endpoint"].(string)
		if want := wantEndpoints[name]; endpoint != want {
			t.Errorf("kinds[%q].endpoint = %q, want %q", name, endpoint, want)
		}
	}
	renamed := llmAPIProvider(t, list, second)
	if got, _ := renamed["name"].(string); got != "Tên khác hẳn" {
		t.Errorf("provider %q name = %q, want %q", second, got, "Tên khác hẳn")
	}
	if got, _ := renamed["endpoint"].(string); got != openAIBase {
		t.Errorf("provider %q endpoint = %q, want %q", second, got, openAIBase)
	}
}

// llmAPIProvider lấy một Provider theo id trong thân GET /llm/providers.
func llmAPIProvider(t *testing.T, resp llmAPIResponse, id string) map[string]any {
	t.Helper()
	providers, _ := resp.object(t)["providers"].([]any)
	for _, entry := range providers {
		p, _ := entry.(map[string]any)
		if got, _ := p["id"].(string); got == id {
			return p
		}
	}
	t.Fatalf("provider %q not in list; body = %s", id, resp.raw)
	return nil
}

// --- credential ---

func TestLLMAPINeverReturnsCredentialInReads(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)

	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	provider := llmAPIProvider(t, list, id)
	if configured, _ := provider["credential_configured"].(bool); !configured {
		t.Errorf("provider %q credential_configured = false, want true", id)
	}
	if unreadable, _ := provider["credential_unreadable"].(bool); unreadable {
		t.Errorf("provider %q credential_unreadable = true, want false right after a successful write", id)
	}
	for _, field := range []string{"credential", "credential_cipher", "api_key", "secret"} {
		if _, present := provider[field]; present {
			t.Errorf("provider %q exposes field %q; body = %s", id, field, list.raw)
		}
	}
	// Bản mã cũng không được ra ngoài, kể cả dưới dạng base64 mà encoding/json tự sinh cho []byte.
	cipher, err := h.st.LLMCredentialCipher(id)
	if err != nil {
		t.Fatalf("LLMCredentialCipher(%q) = %v; want nil", id, err)
	}
	if strings.Contains(list.raw, string(cipher)) {
		t.Errorf("GET /llm/providers leaked the ciphertext; body = %s", list.raw)
	}
	h.assertNoSecret("GET /llm/providers", "response", list.raw)
}

func TestLLMAPIBlankCredentialOnUpdateKeepsTheStoredKey(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)

	h.mustStatus(h.do(http.MethodPut, "/llm/providers/"+id,
		`{"name":"OpenAI đã đổi tên","enabled":false,"credential":""}`), http.StatusOK, "update provider")

	if got := h.storedSecret(id); got != llmAPIKey {
		t.Fatalf("stored credential after blank update = %q, want the original key unchanged", got)
	}
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	provider := llmAPIProvider(t, list, id)
	if enabled, _ := provider["enabled"].(bool); enabled {
		t.Errorf("provider %q enabled = true, want false after the update", id)
	}
	if configured, _ := provider["credential_configured"].(bool); !configured {
		t.Errorf("provider %q credential_configured = false after a blank update; the key was dropped", id)
	}
}

func TestLLMAPICredentialReplaceAndClearAreExplicit(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)

	replace := h.do(http.MethodPut, "/llm/providers/"+id+"/credential",
		fmt.Sprintf(`{"credential":%q}`, llmAPINextKey))
	h.mustStatus(replace, http.StatusNoContent, "replace credential")
	if got := h.storedSecret(id); got != llmAPINextKey {
		t.Fatalf("stored credential after replace = %q, want the new key", got)
	}

	clearResp := h.do(http.MethodDelete, "/llm/providers/"+id+"/credential", "")
	h.mustStatus(clearResp, http.StatusNoContent, "clear credential")
	if _, err := h.st.LLMCredentialCipher(id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("LLMCredentialCipher after clear = %v, want store.ErrNotFound", err)
	}
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	if configured, _ := llmAPIProvider(t, list, id)["credential_configured"].(bool); configured {
		t.Errorf("provider %q credential_configured = true after an explicit clear", id)
	}

	blank := h.do(http.MethodPut, "/llm/providers/"+id+"/credential", `{"credential":"   "}`)
	if blank.status != http.StatusUnprocessableEntity {
		t.Fatalf("replace with a blank credential status = %d, want 422; body = %s", blank.status, blank.raw)
	}
	if code := blank.errorCode(t); code != "PROVIDER_CREDENTIAL_INVALID" {
		t.Errorf("blank credential error code = %q, want PROVIDER_CREDENTIAL_INVALID", code)
	}
	h.assertNoSecret("credential mutations", "replace", replace.raw, "clear", clearResp.raw, "blank", blank.raw)
}

// TestLLMAPIUnreadableCredentialAsksForReentry canh trạng thái DPAPI-không-mở-được: ciphertext
// còn nguyên nhưng database đã sang máy hoặc tài khoản Windows khác.
//
// Đây là trạng thái phải phân biệt được với "chưa nhập khoá" và với một lỗi tạm thời: metadata
// Provider sống sót, chỉ khoá là mất, và lối ra DUY NHẤT là nhập lại. Hiện "thử lại sau" cho nó
// là bắt người trực chờ một thứ không bao giờ tự khỏi.
func TestLLMAPIUnreadableCredentialAsksForReentry(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)
	// Ciphertext rác: đúng hình dạng "đã có khoá" nhưng unprotect từ chối — cùng thứ DPAPI trả
	// về khi blob được mã hoá bởi một tài khoản khác.
	if err := h.st.SetLLMCredentialCipher(id, []byte("rác không giải mã được")); err != nil {
		t.Fatalf("SetLLMCredentialCipher(%q, rác) = %v; want nil", id, err)
	}

	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	provider := llmAPIProvider(t, list, id)
	if configured, _ := provider["credential_configured"].(bool); !configured {
		t.Errorf("provider %q credential_configured = false; ciphertext còn đó nên nó phải là true", id)
	}
	if unreadable, _ := provider["credential_unreadable"].(bool); !unreadable {
		t.Errorf("provider %q credential_unreadable = false, want true", id)
	}

	got := h.do(http.MethodPost, "/llm/providers/"+id+"/test", `{"model":"gpt-4o"}`)
	if got.status != http.StatusUnprocessableEntity {
		t.Fatalf("test với khoá không mở được: status = %d, want 422; body = %s", got.status, got.raw)
	}
	if code := got.errorCode(t); code != "PROVIDER_CREDENTIAL_UNREADABLE" {
		t.Errorf("khoá không mở được: error code = %q, want PROVIDER_CREDENTIAL_UNREADABLE", code)
	}
	// Gợi ý phải gắn vào ô credential: đó là thứ biến 422 này thành một trạng thái nhập lại được
	// thay vì một câu báo lỗi cụt.
	envelope, _ := got.object(t)["error"].(map[string]any)
	fields, _ := envelope["fields"].(map[string]any)
	if hint, _ := fields["credential"].(string); hint == "" {
		t.Errorf("khoá không mở được: thiếu gợi ý ở trường credential; body = %s", got.raw)
	}
	if h.stub.testCalls != 0 {
		t.Errorf("adapter được gọi với một khoá không mở được: %d lượt", h.stub.testCalls)
	}
}

// TestLLMAPISavingACredentialZeroesThePlaintext canh lượt xoá bản rõ trên đường ĐI NHIỀU NHẤT:
// mỗi lần tạo, sửa hay thay khoá đều dựng một lát cắt bản rõ để đưa vào DPAPI.
//
// Chuỗi trong request thì không xoá được (chuỗi Go bất biến), nên lát cắt là phần duy nhất dọn
// được — và nó là phần sống lâu hơn, vì nó đi tiếp xuống lớp syscall.
func TestLLMAPISavingACredentialZeroesThePlaintext(t *testing.T) {
	h := newLLMAPIHarness(t)

	// Khoá vì hàm này chạy trên goroutine của httptest.Server còn phần kiểm chạy trên goroutine
	// của test — cùng lý do stubLLMAdapter có khoá.
	var mu sync.Mutex
	var seen [][]byte
	previous := protectSecret
	protectSecret = func(plain []byte) ([]byte, error) {
		mu.Lock()
		seen = append(seen, plain)
		mu.Unlock()
		return previous(plain)
	}
	t.Cleanup(func() { protectSecret = previous })

	id := h.createProvider("openai", "OpenAI", llmAPIKey)
	h.mustStatus(h.do(http.MethodPut, "/llm/providers/"+id+"/credential",
		fmt.Sprintf(`{"credential":%q}`, llmAPINextKey)), http.StatusNoContent, "replace credential")

	mu.Lock()
	defer mu.Unlock()
	// Hai lượt: một lúc tạo, một lúc thay. Đếm trước khi xét nội dung, vì isAllZero trên một lát
	// cắt rỗng là true — không có cửa này thì test xanh cả khi chẳng lượt nào chạy qua đây.
	if len(seen) != 2 {
		t.Fatalf("protectSecret nhận %d lượt, want 2 (tạo và thay khoá)", len(seen))
	}
	for i, plain := range seen {
		if len(plain) == 0 {
			t.Fatalf("lượt %d đưa vào một lát cắt rỗng; want bản rõ của khoá", i)
		}
		if !isAllZero(plain) {
			t.Errorf("lượt %d để lại bản rõ trong lát cắt: %q", i, plain)
		}
	}
}

// --- test và discover ---

func TestLLMAPIDraftTestWritesNothingAndZeroesThePlaintext(t *testing.T) {
	h := newLLMAPIHarness(t)

	got := h.do(http.MethodPost, "/llm/providers/test",
		fmt.Sprintf(`{"kind":"openai","credential":%q,"model":"gpt-4o"}`, llmAPIKey))
	h.mustStatus(got, http.StatusOK, "draft test")

	if h.stub.secret() != llmAPIKey {
		t.Fatalf("draft test passed credential %q to the adapter, want the drafted key", h.stub.secret())
	}
	if remaining := h.stub.slice(); !isAllZero(remaining) {
		t.Fatalf("draft test left plaintext in the credential slice: %q", remaining)
	}
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	providers, _ := list.object(t)["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("draft test created %d providers, want none beyond claude-code", len(providers)-1)
	}
	h.assertNoSecret("draft test", "response", got.raw, "list", list.raw)
}

func TestLLMAPISavedTestLoadsTheStoredCredential(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)

	got := h.mustStatus(h.do(http.MethodPost, "/llm/providers/"+id+"/test", `{"model":"gpt-4o"}`),
		http.StatusOK, "saved test")
	if h.stub.secret() != llmAPIKey {
		t.Fatalf("saved test passed credential %q to the adapter, want the stored key", h.stub.secret())
	}
	if remaining := h.stub.slice(); !isAllZero(remaining) {
		t.Fatalf("saved test left plaintext in the credential slice: %q", remaining)
	}
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	if status, _ := llmAPIProvider(t, list, id)["last_check_status"].(string); status != "ok" {
		t.Errorf("provider %q last_check_status = %q, want %q", id, status, "ok")
	}
	h.assertNoSecret("saved test", "response", got.raw, "list", list.raw)
}

func TestLLMAPIFailedTestRecordsASanitizedReason(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)
	// Lỗi trần, KHÔNG phải llmError: llmError tự che lúc dựng, nên dùng nó thì test này xanh nhờ
	// tầng adapter và không nói gì về việc handler có che hay không.
	h.stub.testErr = errors.New("openai test: Incorrect API key provided: " + llmAPIKey)

	got := h.do(http.MethodPost, "/llm/providers/"+id+"/test", `{"model":"gpt-4o"}`)
	if got.status != http.StatusBadGateway {
		t.Fatalf("failed test status = %d, want 502; body = %s", got.status, got.raw)
	}
	if code := got.errorCode(t); code != "PROVIDER_TEST_FAILED" {
		t.Errorf("failed test error code = %q, want PROVIDER_TEST_FAILED", code)
	}
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	provider := llmAPIProvider(t, list, id)
	if status, _ := provider["last_check_status"].(string); status != "error" {
		t.Errorf("provider %q last_check_status = %q, want %q", id, status, "error")
	}
	if reason, _ := provider["last_error"].(string); reason == "" || strings.Contains(reason, llmAPIKey) {
		t.Errorf("provider %q last_error = %q; want a non-empty reason with the key masked", id, reason)
	}
	h.assertNoSecret("failed test", "response", got.raw, "list", list.raw)
}

func TestLLMAPIDiscoveryReplacesDiscoveredAndKeepsManual(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)
	h.mustStatus(h.do(http.MethodPost, "/llm/providers/"+id+"/models",
		`{"model_id":"gpt-preview","name":"Bản xem trước"}`), http.StatusCreated, "add manual model")
	if err := h.st.ReplaceLLMModels(id, store.LLMModelDiscovered, []store.LLMModel{
		{ProviderID: id, ModelID: "gpt-cu", Name: "Model cũ", Available: true},
	}); err != nil {
		t.Fatalf("ReplaceLLMModels(%q, discovered) = %v; want nil", id, err)
	}
	h.stub.discovered = []store.LLMModel{
		{ProviderID: id, ModelID: "gpt-4o", Name: "GPT-4o", Source: store.LLMModelDiscovered, Available: true},
		{ProviderID: id, ModelID: "gpt-4o-mini", Name: "GPT-4o mini", Source: store.LLMModelDiscovered, Available: true},
	}

	got := h.mustStatus(h.do(http.MethodPost, "/llm/providers/"+id+"/discover", ""),
		http.StatusOK, "discover models")

	// Cùng hợp đồng bản rõ như hai đường kiểm thử: khoá đúng đi vào adapter, rồi lát cắt bị xoá.
	// Câu hỏi thứ nhất KHÔNG phải là thừa — isAllZero(nil) là true, nên thiếu nó thì lượt kiểm
	// thứ hai xanh cả khi handler chẳng truyền khoá nào cho adapter.
	if h.stub.secret() != llmAPIKey {
		t.Fatalf("discover passed credential %q to the adapter, want the stored key", h.stub.secret())
	}
	if remaining := h.stub.slice(); !isAllZero(remaining) {
		t.Fatalf("discover left plaintext in the credential slice: %q", remaining)
	}
	if want := []string{"gpt-4o", "gpt-4o-mini", "gpt-preview"}; !equalStrings(llmAPIModelIDs(t, got), want) {
		t.Fatalf("discover returned models %v, want %v", llmAPIModelIDs(t, got), want)
	}
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	stored := llmAPIProviderModelIDs(t, list, id)
	if want := []string{"gpt-4o", "gpt-4o-mini", "gpt-preview"}; !equalStrings(stored, want) {
		t.Fatalf("stored models after discovery = %v, want %v (manual kept, stale discovered gone)", stored, want)
	}
	h.assertNoSecret("discover", "response", got.raw, "list", list.raw)
}

func TestLLMAPIFailedDiscoveryKeepsCachedModels(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)
	if err := h.st.ReplaceLLMModels(id, store.LLMModelDiscovered, []store.LLMModel{
		{ProviderID: id, ModelID: "gpt-4o", Name: "GPT-4o", Available: true},
	}); err != nil {
		t.Fatalf("ReplaceLLMModels(%q, discovered) = %v; want nil", id, err)
	}
	h.stub.discoverErr = errors.New("openai discover: GET /v1/models với x-api-key: " + llmAPIKey + " trả 500")

	got := h.do(http.MethodPost, "/llm/providers/"+id+"/discover", "")
	if got.status != http.StatusBadGateway {
		t.Fatalf("failed discover status = %d, want 502; body = %s", got.status, got.raw)
	}
	if code := got.errorCode(t); code != "PROVIDER_DISCOVER_FAILED" {
		t.Errorf("failed discover error code = %q, want PROVIDER_DISCOVER_FAILED", code)
	}
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	stored := llmAPIProviderModelIDs(t, list, id)
	if want := []string{"gpt-4o"}; !equalStrings(stored, want) {
		t.Fatalf("models after a failed discovery = %v, want %v; the cache was emptied", stored, want)
	}
	h.assertNoSecret("failed discover", "response", got.raw, "list", list.raw)
}

// TestLLMAPIDiscoverTakesNoParameters giữ discover trong cùng luật giải mã như mọi mutation khác.
//
// Endpoint không có tham số nào, nên thân rỗng phải đi lọt còn một trường lạ phải bị chặn: im
// lặng bỏ qua nó là để người gọi tin rằng mình vừa truyền được một tuỳ chọn khám phá.
func TestLLMAPIDiscoverTakesNoParameters(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)

	got := h.do(http.MethodPost, "/llm/providers/"+id+"/discover", `{"model":"gpt-4o"}`)
	if got.status != http.StatusBadRequest {
		t.Fatalf("discover với trường lạ: status = %d, want 400; body = %s", got.status, got.raw)
	}
	if code := got.errorCode(t); code != "INVALID_REQUEST" {
		t.Errorf("discover với trường lạ: error code = %q, want INVALID_REQUEST", code)
	}
	if h.stub.discoverCalls != 0 {
		t.Errorf("adapter được gọi dù thân request bị từ chối: %d lượt", h.stub.discoverCalls)
	}
	// Thân rỗng vẫn là lượt gọi hợp lệ — đó là hình dạng bình thường của endpoint này.
	h.mustStatus(h.do(http.MethodPost, "/llm/providers/"+id+"/discover", ""),
		http.StatusOK, "discover với thân rỗng")
}

func TestLLMAPIRejectsCallsWithoutAStoredCredential(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", "")

	for _, path := range []string{"/llm/providers/" + id + "/test", "/llm/providers/" + id + "/discover"} {
		t.Run(path, func(t *testing.T) {
			got := h.do(http.MethodPost, path, "")
			if got.status != http.StatusUnprocessableEntity {
				t.Fatalf("POST %s without a key status = %d, want 422; body = %s", path, got.status, got.raw)
			}
			if code := got.errorCode(t); code != "PROVIDER_CREDENTIAL_MISSING" {
				t.Errorf("POST %s without a key error code = %q, want PROVIDER_CREDENTIAL_MISSING", path, code)
			}
		})
	}
	if h.stub.testCalls != 0 || h.stub.discoverCalls != 0 {
		t.Fatalf("adapter was called without a credential: test=%d discover=%d",
			h.stub.testCalls, h.stub.discoverCalls)
	}
}

// --- model ---

func TestLLMAPIModelsAddAndDelete(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)

	added := h.mustStatus(h.do(http.MethodPost, "/llm/providers/"+id+"/models",
		`{"model_id":" gpt-4o ","name":"GPT-4o"}`), http.StatusCreated, "add model")
	if want := []string{"gpt-4o"}; !equalStrings(llmAPIModelIDs(t, added), want) {
		t.Fatalf("models after add = %v, want %v", llmAPIModelIDs(t, added), want)
	}
	h.mustStatus(h.do(http.MethodDelete, "/llm/providers/"+id+"/models?model_id=gpt-4o", ""),
		http.StatusNoContent, "delete model")
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	if got := llmAPIProviderModelIDs(t, list, id); len(got) != 0 {
		t.Fatalf("models after delete = %v, want none", got)
	}

	empty := h.do(http.MethodPost, "/llm/providers/"+id+"/models", `{"model_id":"  "}`)
	if empty.status != http.StatusUnprocessableEntity {
		t.Fatalf("add blank model status = %d, want 422; body = %s", empty.status, empty.raw)
	}
}

// TestLLMAPISystemProviderModelsStayOnTheAllowlist canh cửa mà mắt xích cuối mở ra: model của
// claude-code trở thành tham số --model của một tiến trình thật, nên nó đi qua đúng danh sách CHO
// PHÉP mà PUT /kb/model dùng.
func TestLLMAPISystemProviderModelsStayOnTheAllowlist(t *testing.T) {
	h := newLLMAPIHarness(t)

	ok := h.do(http.MethodPost, "/llm/providers/claude-code/models", `{"model_id":"opus"}`)
	h.mustStatus(ok, http.StatusCreated, "add allowlisted claude model")

	bad := h.do(http.MethodPost, "/llm/providers/claude-code/models",
		`{"model_id":"sonnet --dangerously-skip-permissions"}`)
	if bad.status != http.StatusUnprocessableEntity {
		t.Fatalf("add off-allowlist claude model status = %d, want 422; body = %s", bad.status, bad.raw)
	}
	if code := bad.errorCode(t); code != "MODEL_INVALID" {
		t.Errorf("off-allowlist claude model error code = %q, want MODEL_INVALID", code)
	}
}

// --- Provider hệ thống ---

func TestLLMAPISystemProviderCannotBeEditedOrDeleted(t *testing.T) {
	h := newLLMAPIHarness(t)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"sửa", http.MethodPut, "/llm/providers/claude-code", `{"name":"Đổi tên","enabled":false}`},
		{"xoá", http.MethodDelete, "/llm/providers/claude-code", ""},
		{"đặt khoá", http.MethodPut, "/llm/providers/claude-code/credential",
			fmt.Sprintf(`{"credential":%q}`, llmAPIKey)},
		{"xoá khoá", http.MethodDelete, "/llm/providers/claude-code/credential", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := h.do(tc.method, tc.path, tc.body)
			if got.status != http.StatusUnprocessableEntity {
				t.Fatalf("%s %s status = %d, want 422; body = %s", tc.method, tc.path, got.status, got.raw)
			}
			if code := got.errorCode(t); code != "PROVIDER_SYSTEM_READONLY" {
				t.Errorf("%s %s error code = %q, want PROVIDER_SYSTEM_READONLY", tc.method, tc.path, code)
			}
		})
	}
	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	system := llmAPIProvider(t, list, "claude-code")
	if name, _ := system["name"].(string); name != "Claude Code" {
		t.Errorf("system provider name = %q, want %q; a refused edit still landed", name, "Claude Code")
	}
	if enabled, _ := system["enabled"].(bool); !enabled {
		t.Error("system provider enabled = false; a refused edit still landed")
	}
}

// TestLLMAPIDeleteRemovesAnUnroutedProviderWithItsKeyAndModels canh đường xoá THÀNH CÔNG.
//
// Không chỉ đếm số Provider còn lại: khoá và model phải đi theo. Một hàng credential mồ côi là
// ciphertext nằm lại trong database cho một Provider không còn tồn tại, còn model mồ côi thì hiện
// ra trở lại nếu ai đó tạo lại Provider trùng id.
func TestLLMAPIDeleteRemovesAnUnroutedProviderWithItsKeyAndModels(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)
	h.mustStatus(h.do(http.MethodPost, "/llm/providers/"+id+"/models", `{"model_id":"gpt-4o"}`),
		http.StatusCreated, "add model")

	h.mustStatus(h.do(http.MethodDelete, "/llm/providers/"+id, ""),
		http.StatusNoContent, "delete provider")

	list := h.mustStatus(h.do(http.MethodGet, "/llm/providers", ""), http.StatusOK, "list providers")
	providers, _ := list.object(t)["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("providers after delete = %d, want 1 (chỉ còn claude-code); body = %s",
			len(providers), list.raw)
	}
	if _, err := h.st.LLMCredentialCipher(id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("LLMCredentialCipher(%q) after delete = %v, want store.ErrNotFound", id, err)
	}
	models, err := h.st.LLMModels(id)
	if err != nil {
		t.Fatalf("LLMModels(%q) = %v; want nil", id, err)
	}
	if len(models) != 0 {
		t.Errorf("models of %q after delete = %v, want none", id, models)
	}
}

func TestLLMAPIDeleteRejectsAProviderTheRouteUses(t *testing.T) {
	h := newLLMAPIHarness(t)
	id := h.createProvider("openai", "OpenAI", llmAPIKey)
	h.mustStatus(h.do(http.MethodPost, "/llm/providers/"+id+"/models", `{"model_id":"gpt-4o"}`),
		http.StatusCreated, "add model")
	h.seedClaudeModel("haiku")
	revision := llmAPIRouteRevision(t, h.mustStatus(h.do(http.MethodGet, "/llm/route", ""),
		http.StatusOK, "read route"))
	h.mustStatus(h.do(http.MethodPut, "/llm/route", fmt.Sprintf(
		`{"revision":%d,"entries":[{"provider_id":%q,"model_id":"gpt-4o","enabled":true},`+
			`{"provider_id":"claude-code","model_id":"haiku","enabled":true}]}`, revision, id)),
		http.StatusOK, "save route")

	got := h.do(http.MethodDelete, "/llm/providers/"+id, "")
	if got.status != http.StatusConflict {
		t.Fatalf("delete a routed provider status = %d, want 409; body = %s", got.status, got.raw)
	}
	if code := got.errorCode(t); code != "PROVIDER_IN_USE" {
		t.Errorf("delete a routed provider error code = %q, want PROVIDER_IN_USE", code)
	}
}

func TestLLMAPIUnknownProviderIs404(t *testing.T) {
	h := newLLMAPIHarness(t)
	got := h.do(http.MethodPut, "/llm/providers/khong-co-that", `{"name":"X","enabled":true}`)
	if got.status != http.StatusNotFound {
		t.Fatalf("update an unknown provider status = %d, want 404; body = %s", got.status, got.raw)
	}
	if code := got.errorCode(t); code != "PROVIDER_NOT_FOUND" {
		t.Errorf("update an unknown provider error code = %q, want PROVIDER_NOT_FOUND", code)
	}
}

// --- route ---

func TestLLMAPIRouteConflictReturns409(t *testing.T) {
	h := newLLMAPIHarness(t)
	h.seedClaudeModel("haiku")
	current := h.mustStatus(h.do(http.MethodGet, "/llm/route", ""), http.StatusOK, "read route")
	revision := llmAPIRouteRevision(t, current)

	saved := h.mustStatus(h.do(http.MethodPut, "/llm/route", fmt.Sprintf(
		`{"revision":%d,"entries":[{"provider_id":"claude-code","model_id":"haiku","enabled":true}]}`,
		revision)), http.StatusOK, "save route")
	if next := llmAPIRouteRevision(t, saved); next != revision+1 {
		t.Fatalf("revision after save = %d, want %d", next, revision+1)
	}

	stale := h.do(http.MethodPut, "/llm/route", fmt.Sprintf(
		`{"revision":%d,"entries":[{"provider_id":"claude-code","model_id":"haiku","enabled":true}]}`,
		revision))
	if stale.status != http.StatusConflict {
		t.Fatalf("stale route save status = %d, want 409; body = %s", stale.status, stale.raw)
	}
	if code := stale.errorCode(t); code != "ROUTE_REVISION_CONFLICT" {
		t.Errorf("stale route save error code = %q, want ROUTE_REVISION_CONFLICT", code)
	}
}

func TestLLMAPIStatusReportsTelemetry(t *testing.T) {
	h := newLLMAPIHarness(t)
	for _, a := range []store.LLMAttempt{
		{
			ProviderID: "openai-1", ModelID: "gpt-5-mini", Outcome: store.LLMAttemptError,
			ErrorKind: "credential",
		},
		{ProviderID: "claude-code", ModelID: "haiku", Outcome: store.LLMAttemptOK},
	} {
		if err := h.st.RecordLLMAttempt(a); err != nil {
			t.Fatalf("RecordLLMAttempt(%q) = %v; want nil", a.ProviderID, err)
		}
	}

	got := h.mustStatus(h.do(http.MethodGet, "/llm/status", ""), http.StatusOK, "read status")
	body := got.object(t)
	if provider, _ := body["active_provider_id"].(string); provider != "claude-code" {
		t.Errorf("active_provider_id = %q, want %q", provider, "claude-code")
	}
	if attempts, _ := body["attempts"].(float64); attempts != 2 {
		t.Errorf("attempts = %v, want 2", body["attempts"])
	}
	if fallbacks, _ := body["fallbacks"].(float64); fallbacks != 0 {
		t.Errorf("fallbacks = %v, want 0", body["fallbacks"])
	}
	// Phân loại lỗi phải RA ĐƯỢC tới Portal: đây là thứ duy nhất phân biệt "khoá sai, sửa 30 giây"
	// với "Claude Code cũng hỏng", và trên đường trả lời khách hai thứ đó giống hệt nhau.
	if kind, _ := body["last_error_kind"].(string); kind != "credential" {
		t.Errorf("last_error_kind = %v, want %q", body["last_error_kind"], "credential")
	}
	if provider, _ := body["last_error_provider_id"].(string); provider != "openai-1" {
		t.Errorf("last_error_provider_id = %v, want %q", body["last_error_provider_id"], "openai-1")
	}
}

// --- tiện ích của test ---

func llmAPIModelIDs(t *testing.T, resp llmAPIResponse) []string {
	t.Helper()
	models, _ := resp.object(t)["models"].([]any)
	return llmAPICollectModelIDs(models)
}

func llmAPIProviderModelIDs(t *testing.T, list llmAPIResponse, id string) []string {
	t.Helper()
	models, _ := llmAPIProvider(t, list, id)["models"].([]any)
	return llmAPICollectModelIDs(models)
}

func llmAPICollectModelIDs(models []any) []string {
	out := make([]string, 0, len(models))
	for _, entry := range models {
		m, _ := entry.(map[string]any)
		id, _ := m["model_id"].(string)
		out = append(out, id)
	}
	return out
}

func llmAPIRouteRevision(t *testing.T, resp llmAPIResponse) int64 {
	t.Helper()
	revision, ok := resp.object(t)["revision"].(float64)
	if !ok {
		t.Fatalf("route body %q has no numeric revision", resp.raw)
	}
	return int64(revision)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func isAllZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
