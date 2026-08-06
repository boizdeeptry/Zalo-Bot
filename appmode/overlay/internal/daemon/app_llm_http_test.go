package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentdc/internal/store"

	"github.com/google/go-cmp/cmp"
)

// Hai canary tách biệt: llmTestKey đi vào header xác thực, llmTestLeak chỉ tồn tại trong body
// Provider trả về. Tách ra thì test rò rỉ nói được rõ CÁI GÌ lọt — khoá, hay nguyên body.
const (
	llmTestKey    = "sk-canary-Ab3xQ9zK7mP2wR5t"
	llmTestLeak   = "leak-canary-Zz9Qm4Xr"
	llmTestPrompt = "khách hỏi giá combo"
	llmTestReply  = "chào bạn, combo đang là 390k"
)

// llmAdapterCase mô tả một Provider bằng đúng những gì hợp đồng của nó nói ra ngoài: đường dẫn
// cố định, header xác thực, hình dạng payload gửi đi và bốn dạng phản hồi cần phân biệt.
type llmAdapterCase struct {
	name  string
	model string
	// build dựng adapter thật rồi trỏ base vào fake server. Constructor sản xuất KHÔNG nhận base
	// — endpoint là hằng số của Provider, không phải tuỳ chọn — nên test đổi trường trực tiếp.
	build        func(client *http.Client, base string) providerAdapter
	generatePath string
	modelsPath   string
	// authHeaders phải có mặt ở CẢ generate lẫn models: một lượt liệt kê model không kèm khoá
	// trả về 401 trên máy thật nhưng vẫn xanh trên fake server dễ dãi.
	authHeaders map[string]string
	// modelsQuery là query string của lượt liệt kê model, so khớp đúng bằng: hai Provider phải
	// nâng trần phân trang, và một danh sách bị cắt trông y hệt một danh sách đủ.
	modelsQuery string
	// wantRequest là toàn bộ body gửi đi, so khớp đúng bằng: một trường thừa hay thiếu tên đều
	// là thứ chỉ lộ ra khi gọi API thật.
	wantRequest map[string]any
	generateOK  string
	// generatePolicy là MỌI tín hiệu từ chối của Provider, không phải một cái đại diện: bỏ sót
	// một tín hiệu thì lượt bị từ chối rơi vào upstream, và upstream thì được đi tiếp — tức là
	// chuỗi mang chính nội dung vừa bị từ chối sang Provider sau.
	generatePolicy []llmPolicyFixture
	modelsOK       string
	wantModels     []store.LLMModel
}

type llmPolicyFixture struct{ name, body string }

func llmAdapterCases() []llmAdapterCase {
	return []llmAdapterCase{
		{
			name:  "openai",
			model: "gpt-4o",
			build: func(c *http.Client, base string) providerAdapter {
				a := newOpenAIAdapter("prov-openai", c)
				a.base = base
				return a
			},
			generatePath: "/v1/responses",
			modelsPath:   "/v1/models",
			authHeaders:  map[string]string{"Authorization": "Bearer " + llmTestKey},
			wantRequest:  map[string]any{"model": "gpt-4o", "input": llmTestPrompt},
			generateOK: `{"output":[{"type":"message","content":[
				{"type":"output_text","text":"` + llmTestReply + `"}]}]}`,
			generatePolicy: []llmPolicyFixture{
				{"incomplete_details", `{"incomplete_details":{"reason":"content_filter"},"output":[]}`},
				{"khối refusal", `{"output":[{"type":"message","content":[
					{"type":"refusal","refusal":"Tôi không hỗ trợ việc này"}]}]}`},
			},
			modelsOK: `{"object":"list","data":[
				{"id":"gpt-4o-mini","object":"model"},{"id":"gpt-4o","object":"model"}]}`,
			wantModels: []store.LLMModel{
				{ProviderID: "prov-openai", ModelID: "gpt-4o", Name: "gpt-4o", Source: store.LLMModelDiscovered, Available: true},
				{ProviderID: "prov-openai", ModelID: "gpt-4o-mini", Name: "gpt-4o-mini", Source: store.LLMModelDiscovered, Available: true},
			},
		},
		{
			name:  "anthropic",
			model: "claude-sonnet-4-5",
			build: func(c *http.Client, base string) providerAdapter {
				a := newAnthropicAdapter("prov-anthropic", c)
				a.base = base
				return a
			},
			generatePath: "/v1/messages",
			modelsPath:   "/v1/models",
			authHeaders: map[string]string{
				"X-Api-Key":         llmTestKey,
				"Anthropic-Version": anthropicVersion,
			},
			wantRequest: map[string]any{
				"model":      "claude-sonnet-4-5",
				"max_tokens": float64(anthropicMaxTokens),
				"messages":   []any{map[string]any{"role": "user", "content": llmTestPrompt}},
			},
			generateOK: `{"content":[{"type":"text","text":"` + llmTestReply + `"}],"stop_reason":"end_turn"}`,
			generatePolicy: []llmPolicyFixture{
				{"stop_reason refusal", `{"content":[],"stop_reason":"refusal"}`},
			},
			modelsQuery: "limit=1000",
			modelsOK: `{"data":[
				{"type":"model","id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5"},
				{"type":"model","id":"claude-haiku-4-5","display_name":"Claude Haiku 4.5"}],"has_more":false}`,
			wantModels: []store.LLMModel{
				{ProviderID: "prov-anthropic", ModelID: "claude-haiku-4-5", Name: "Claude Haiku 4.5", Source: store.LLMModelDiscovered, Available: true},
				{ProviderID: "prov-anthropic", ModelID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", Source: store.LLMModelDiscovered, Available: true},
			},
		},
		{
			name:  "gemini",
			model: "gemini-2.5-flash",
			build: func(c *http.Client, base string) providerAdapter {
				a := newGeminiAdapter("prov-gemini", c)
				a.base = base
				return a
			},
			generatePath: "/v1beta/models/gemini-2.5-flash:generateContent",
			modelsPath:   "/v1beta/models",
			// Khoá đi qua header chứ KHÔNG qua ?key=: URL đi vào câu lỗi của net/http, vào log
			// truy cập và vào lịch sử proxy, còn header thì không.
			authHeaders: map[string]string{"X-Goog-Api-Key": llmTestKey},
			wantRequest: map[string]any{
				"contents": []any{map[string]any{
					"role":  "user",
					"parts": []any{map[string]any{"text": llmTestPrompt}},
				}},
			},
			generateOK: `{"candidates":[{"content":{"role":"model","parts":[
				{"text":"` + llmTestReply + `"}]},"finishReason":"STOP"}]}`,
			generatePolicy: []llmPolicyFixture{
				{"promptFeedback blockReason", `{"promptFeedback":{"blockReason":"SAFETY"},"candidates":[]}`},
				{"finishReason SAFETY", `{"candidates":[{"finishReason":"SAFETY","content":{"parts":[]}}]}`},
			},
			modelsQuery: "pageSize=1000",
			modelsOK: `{"models":[
				{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","supportedGenerationMethods":["generateContent"]},
				{"name":"models/text-embedding-004","displayName":"Embedding 004","supportedGenerationMethods":["embedContent"]},
				{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","supportedGenerationMethods":["countTokens","generateContent"]}]}`,
			wantModels: []store.LLMModel{
				{ProviderID: "prov-gemini", ModelID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash", Source: store.LLMModelDiscovered, Available: true},
				{ProviderID: "prov-gemini", ModelID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Source: store.LLMModelDiscovered, Available: true},
			},
		},
		{
			name:  "openrouter",
			model: "openai/gpt-4o",
			build: func(c *http.Client, base string) providerAdapter {
				a := newOpenRouterAdapter("prov-openrouter", c)
				a.base = base
				return a
			},
			generatePath: "/api/v1/chat/completions",
			modelsPath:   "/api/v1/models",
			authHeaders: map[string]string{
				"Authorization": "Bearer " + llmTestKey,
				"HTTP-Referer":  openRouterReferer,
				"X-Title":       openRouterTitle,
			},
			wantRequest: map[string]any{
				"model":    "openai/gpt-4o",
				"messages": []any{map[string]any{"role": "user", "content": llmTestPrompt}},
			},
			generateOK: `{"choices":[{"message":{"role":"assistant","content":"` + llmTestReply +
				`"},"finish_reason":"stop"}]}`,
			generatePolicy: []llmPolicyFixture{
				{"finish_reason content_filter",
					`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`},
			},
			modelsOK: `{"data":[
				{"id":"openai/gpt-4o","name":"OpenAI: GPT-4o"},
				{"id":"anthropic/claude-sonnet-4.5","name":"Anthropic: Claude Sonnet 4.5"}]}`,
			wantModels: []store.LLMModel{
				{ProviderID: "prov-openrouter", ModelID: "anthropic/claude-sonnet-4.5", Name: "Anthropic: Claude Sonnet 4.5", Source: store.LLMModelDiscovered, Available: true},
				{ProviderID: "prov-openrouter", ModelID: "openai/gpt-4o", Name: "OpenAI: GPT-4o", Source: store.LLMModelDiscovered, Available: true},
			},
		},
	}
}

// llmFakeServer ghi lại request cuối cùng và trả về một phản hồi cố định.
type llmFakeServer struct {
	*httptest.Server
	method string
	path   string
	query  string
	header http.Header
	body   []byte
}

func newLLMFakeServer(t *testing.T, status int, body string) *llmFakeServer {
	t.Helper()
	f := &llmFakeServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.method, f.path, f.header = r.Method, r.URL.Path, r.Header.Clone()
		f.query = r.URL.RawQuery
		f.body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

// httpClient là client của fake server kèm một hạn giờ rộng rãi — lưới chặn để test không treo
// mãi khi có gì đó hỏng, chứ không phải thứ khẳng định nào dưới đây trông vào.
func (f *llmFakeServer) httpClient() *http.Client {
	c := f.Client()
	c.Timeout = 5 * time.Second
	return c
}

func (f *llmFakeServer) requestJSON(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(f.body, &v); err != nil {
		t.Fatalf("body gửi đi = %q; không phải JSON: %v", f.body, err)
	}
	return v
}

func (f *llmFakeServer) assertHeaders(t *testing.T, want map[string]string) {
	t.Helper()
	for name, value := range want {
		if got := f.header.Get(name); got != value {
			t.Errorf("header %s = %q; want %q", name, got, value)
		}
	}
}

func llmKindOf(t *testing.T, err error) llmErrorKind {
	t.Helper()
	var le *llmError
	if !errors.As(err, &le) {
		t.Fatalf("error = %v (%T); want *llmError", err, err)
	}
	return le.Kind
}

func TestLLMAdapterGenerateSendsOfficialRequest(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newLLMFakeServer(t, http.StatusOK, tc.generateOK)
			a := tc.build(f.httpClient(), f.URL)

			got, err := a.Generate(context.Background(),
				llmRequest{Model: tc.model, Prompt: llmTestPrompt}, []byte(llmTestKey))
			if err != nil {
				t.Fatalf("Generate(%s) error = %v; want nil", tc.name, err)
			}
			if got.Text != llmTestReply {
				t.Errorf("Generate(%s).Text = %q; want %q", tc.name, got.Text, llmTestReply)
			}
			if f.method != http.MethodPost {
				t.Errorf("Generate(%s) method = %q; want %q", tc.name, f.method, http.MethodPost)
			}
			if f.path != tc.generatePath {
				t.Errorf("Generate(%s) path = %q; want %q", tc.name, f.path, tc.generatePath)
			}
			// Query rỗng là một khẳng định về BẢO MẬT, không phải về hình thức: không adapter nào
			// được để credential vào URL, vì URL đi vào log truy cập và lịch sử proxy.
			if f.query != "" {
				t.Errorf("Generate(%s) query = %q; want rỗng", tc.name, f.query)
			}
			if ct := f.header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Generate(%s) Content-Type = %q; want %q", tc.name, ct, "application/json")
			}
			f.assertHeaders(t, tc.authHeaders)
			if diff := cmp.Diff(tc.wantRequest, f.requestJSON(t)); diff != "" {
				t.Errorf("Generate(%s) payload mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestLLMAdapterDiscoverNormalizesAndSorts(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newLLMFakeServer(t, http.StatusOK, tc.modelsOK)
			a := tc.build(f.httpClient(), f.URL)

			got, err := a.Discover(context.Background(), []byte(llmTestKey))
			if err != nil {
				t.Fatalf("Discover(%s) error = %v; want nil", tc.name, err)
			}
			if f.method != http.MethodGet {
				t.Errorf("Discover(%s) method = %q; want %q", tc.name, f.method, http.MethodGet)
			}
			if f.path != tc.modelsPath {
				t.Errorf("Discover(%s) path = %q; want %q", tc.name, f.path, tc.modelsPath)
			}
			// Trần phân trang: Anthropic mặc định trả 20 model, Gemini 50. Bỏ tham số đi thì danh
			// sách cụt, và một danh sách cụt trông y hệt một danh sách đủ.
			if f.query != tc.modelsQuery {
				t.Errorf("Discover(%s) query = %q; want %q", tc.name, f.query, tc.modelsQuery)
			}
			f.assertHeaders(t, tc.authHeaders)
			if diff := cmp.Diff(tc.wantModels, got); diff != "" {
				t.Errorf("Discover(%s) mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

// Discover không được trả danh sách một nửa kèm lỗi: tầng dịch vụ giữ nguyên bản cache cũ khi
// adapter lỗi, và một lát cắt không rỗng ở đây sẽ lặng lẽ ghi đè nó bằng dữ liệu dở dang.
func TestLLMAdapterDiscoverReturnsNoPartialListOnError(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newLLMFakeServer(t, http.StatusInternalServerError, tc.modelsOK)
			a := tc.build(f.httpClient(), f.URL)

			got, err := a.Discover(context.Background(), []byte(llmTestKey))
			if err == nil {
				t.Fatalf("Discover(%s) error = nil; want lỗi", tc.name)
			}
			if len(got) != 0 {
				t.Fatalf("Discover(%s) trả %d model kèm lỗi; want 0", tc.name, len(got))
			}
		})
	}
}

// Test phải là phép thử VÔ HẠI: một lượt liệt kê model, không sinh token nào.
func TestLLMAdapterTestUsesModelList(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newLLMFakeServer(t, http.StatusOK, tc.modelsOK)
			a := tc.build(f.httpClient(), f.URL)

			if err := a.Test(context.Background(), tc.model, []byte(llmTestKey)); err != nil {
				t.Fatalf("Test(%s, %q) error = %v; want nil", tc.name, tc.model, err)
			}
			if f.method != http.MethodGet {
				t.Errorf("Test(%s) method = %q; want %q", tc.name, f.method, http.MethodGet)
			}
			if f.path != tc.modelsPath {
				t.Errorf("Test(%s) path = %q; want %q", tc.name, f.path, tc.modelsPath)
			}
			f.assertHeaders(t, tc.authHeaders)
		})
	}
}

func TestLLMAdapterTestRejectsUnlistedModel(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newLLMFakeServer(t, http.StatusOK, tc.modelsOK)
			a := tc.build(f.httpClient(), f.URL)

			err := a.Test(context.Background(), "model-khong-ton-tai", []byte(llmTestKey))
			if got := llmKindOf(t, err); got != llmErrorModel {
				t.Fatalf("Test(%s, model lạ) kind = %q; want %q", tc.name, got, llmErrorModel)
			}
		})
	}
}

func TestLLMAdapterMapsStatusToKind(t *testing.T) {
	statuses := []struct {
		status int
		want   llmErrorKind
	}{
		{http.StatusTooManyRequests, llmErrorRateLimit},
		{http.StatusInternalServerError, llmErrorUpstream},
		{http.StatusServiceUnavailable, llmErrorUpstream},
		{http.StatusUnauthorized, llmErrorCredential},
		{http.StatusForbidden, llmErrorCredential},
		{http.StatusNotFound, llmErrorModel},
		{http.StatusBadRequest, llmErrorRequest},
		{http.StatusUnprocessableEntity, llmErrorRequest},
		// 2xx khác 200: không nằm trong hợp đồng của bốn API này, nên Provider đang trả về thứ
		// ta không hiểu — coi như họ hỏng, và được thử Provider sau.
		{http.StatusCreated, llmErrorUpstream},
	}
	for _, tc := range llmAdapterCases() {
		for _, st := range statuses {
			t.Run(tc.name+"/"+http.StatusText(st.status), func(t *testing.T) {
				f := newLLMFakeServer(t, st.status, `{"error":{"message":"`+llmTestLeak+`"}}`)
				a := tc.build(f.httpClient(), f.URL)

				_, err := a.Generate(context.Background(),
					llmRequest{Model: tc.model, Prompt: llmTestPrompt}, []byte(llmTestKey))
				if got := llmKindOf(t, err); got != st.want {
					t.Fatalf("Generate(%s) với %d kind = %q; want %q", tc.name, st.status, got, st.want)
				}
			})
		}
	}
}

// Một phản hồi 200 nhưng không đọc được là hỏng hóc PHÍA PROVIDER, nên nó được đi tiếp sang
// Provider sau — khác hẳn credential sai hay model không tồn tại, những thứ thử lại vẫn hỏng.
func TestLLMAdapterMalformedSuccessIsFallbackEligibleUpstream(t *testing.T) {
	bodies := []struct{ name, body string }{
		{"rỗng", ""},
		{"không phải JSON", "<html>502 bad gateway</html>"},
		{"JSON đúng nhưng không có nội dung", `{}`},
	}
	for _, tc := range llmAdapterCases() {
		for _, b := range bodies {
			t.Run(tc.name+"/"+b.name, func(t *testing.T) {
				f := newLLMFakeServer(t, http.StatusOK, b.body)
				a := tc.build(f.httpClient(), f.URL)

				_, err := a.Generate(context.Background(),
					llmRequest{Model: tc.model, Prompt: llmTestPrompt}, []byte(llmTestKey))
				got := llmKindOf(t, err)
				if got != llmErrorUpstream {
					t.Fatalf("Generate(%s, body %s) kind = %q; want %q", tc.name, b.name, got, llmErrorUpstream)
				}
				if !isFallbackEligible(got) {
					t.Fatalf("isFallbackEligible(%q) = false; want true", got)
				}
			})
		}
	}
}

// Nội dung bị từ chối DỪNG chuỗi: đổi Provider để né chính sách là hành vi thiết kế không muốn có.
//
// Chạy hết MỌI tín hiệu từ chối của từng Provider, không phải một cái đại diện. Một tín hiệu
// không ai canh mà hỏng thì lượt đó rơi vào upstream — loại ĐƯỢC fallback — nên chuỗi sẽ mang
// đúng nội dung vừa bị từ chối sang Provider kế tiếp. Đó là lỗi tệ nhất tệp này có thể có.
func TestLLMAdapterPolicyRefusalStopsChain(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		for _, fixture := range tc.generatePolicy {
			t.Run(tc.name+"/"+fixture.name, func(t *testing.T) {
				f := newLLMFakeServer(t, http.StatusOK, fixture.body)
				a := tc.build(f.httpClient(), f.URL)

				_, err := a.Generate(context.Background(),
					llmRequest{Model: tc.model, Prompt: llmTestPrompt}, []byte(llmTestKey))
				got := llmKindOf(t, err)
				if got != llmErrorPolicy {
					t.Fatalf("Generate(%s, %s) kind = %q; want %q", tc.name, fixture.name, got, llmErrorPolicy)
				}
				if isFallbackEligible(got) {
					t.Fatalf("isFallbackEligible(%q) = true; want false", got)
				}
			})
		}
	}
}

func TestLLMAdapterMapsTransportFailureToNetwork(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newLLMFakeServer(t, http.StatusOK, tc.generateOK)
			client, base := f.httpClient(), f.URL
			f.Close() // cổng đóng trước khi gọi: không có gì để bắt tay.
			a := tc.build(client, base)

			_, err := a.Generate(context.Background(),
				llmRequest{Model: tc.model, Prompt: llmTestPrompt}, []byte(llmTestKey))
			if got := llmKindOf(t, err); got != llmErrorNetwork {
				t.Fatalf("Generate(%s, server đã đóng) kind = %q; want %q", tc.name, got, llmErrorNetwork)
			}
		})
	}
}

func TestLLMAdapterMapsDeadlineToTimeout(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		t.Run(tc.name, func(t *testing.T) {
			// Handler treo tới khi test thả ra, thay vì ngủ một khoảng đoán trước: deadline của
			// ctx là thứ duy nhất quyết định khi nào lượt gọi kết thúc.
			//
			// Thả bằng channel riêng chứ KHÔNG chờ r.Context(): net/http chỉ bật lượt đọc nền
			// phát hiện client rời đi sau khi thân request đã được đọc hết, mà handler này không
			// đọc thân — nên r.Context() ở đây không bao giờ Done và srv.Close() treo vĩnh viễn.
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				<-release
			}))
			// Cleanup chạy ngược thứ tự đăng ký: thả handler trước, đóng server sau.
			t.Cleanup(srv.Close)
			t.Cleanup(func() { close(release) })
			client := srv.Client()
			client.Timeout = 5 * time.Second
			a := tc.build(client, srv.URL)

			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			_, err := a.Generate(ctx, llmRequest{Model: tc.model, Prompt: llmTestPrompt}, []byte(llmTestKey))
			if got := llmKindOf(t, err); got != llmErrorTimeout {
				t.Fatalf("Generate(%s, quá hạn) kind = %q; want %q", tc.name, got, llmErrorTimeout)
			}
		})
	}
}

// Huỷ ctx KHÔNG được fallback. Đây là loại lỗi duy nhất mà nguyên nhân nằm ở PHÍA TA: lượt đã
// bị bỏ (tắt daemon, hết hạn mức lượt), nên gọi tiếp Provider sau chỉ là gọi bằng một ctx đã
// chết — hỏng ngay, hỏng hết chuỗi, rồi báo một lỗi đổ tội cho các Provider.
func TestLLMAdapterMapsCancellationToCanceled(t *testing.T) {
	for _, tc := range llmAdapterCases() {
		t.Run(tc.name, func(t *testing.T) {
			f := newLLMFakeServer(t, http.StatusOK, tc.generateOK)
			a := tc.build(f.httpClient(), f.URL)

			// Huỷ trước khi gọi: xác định hoàn toàn, không cần server phải treo.
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			_, err := a.Generate(ctx, llmRequest{Model: tc.model, Prompt: llmTestPrompt}, []byte(llmTestKey))
			got := llmKindOf(t, err)
			if got != llmErrorCanceled {
				t.Fatalf("Generate(%s, ctx đã huỷ) kind = %q; want %q", tc.name, got, llmErrorCanceled)
			}
			if isFallbackEligible(got) {
				t.Fatalf("isFallbackEligible(%q) = true; want false", got)
			}
		})
	}
}

// Câu lỗi phải dựng từ status, method và URL — KHÔNG từ body. sanitizeProviderError là lưới cuối
// chứ không phải cái cớ để chuyển tiếp nguyên body: một khoá không mang tiền tố nào lọt vào giữa
// câu văn của Provider thì không mẫu nào bắt được.
func TestLLMAdapterErrorsHideCredentialAndBody(t *testing.T) {
	leaky := `{"error":{"message":"Incorrect API key provided: ` + llmTestKey +
		`. Trace ` + llmTestLeak + `","type":"invalid_request_error"}}`
	responses := []struct {
		name   string
		status int
		body   string
	}{
		{"lỗi 401 kèm khoá trong body", http.StatusUnauthorized, leaky},
		{"200 nhưng body hỏng", http.StatusOK, leaky + "<<<"},
	}
	for _, tc := range llmAdapterCases() {
		for _, r := range responses {
			t.Run(tc.name+"/"+r.name, func(t *testing.T) {
				f := newLLMFakeServer(t, r.status, r.body)
				a := tc.build(f.httpClient(), f.URL)

				_, err := a.Generate(context.Background(),
					llmRequest{Model: tc.model, Prompt: llmTestPrompt}, []byte(llmTestKey))
				if err == nil {
					t.Fatalf("Generate(%s, %s) error = nil; want lỗi", tc.name, r.name)
				}
				for _, canary := range []string{llmTestKey, llmTestLeak} {
					if strings.Contains(err.Error(), canary) {
						t.Fatalf("Generate(%s, %s) error = %q; còn chứa %q", tc.name, r.name, err, canary)
					}
				}
			})
		}
	}
}

// Bốn endpoint sản xuất không có test nào khác chạm tới: mọi test ở trên ghi đè base để trỏ vào
// fake server. Không ghim ở đây thì một lỗi gõ trong hằng số đi thẳng ra bản phát hành, và thứ
// hỏng là một request mang khoá thật bay tới máy chủ của người khác.
//
// Đọc qua constructor chứ không đọc thẳng hằng số: cách này bắt được cả trường hợp constructor
// bị nối nhầm sang hằng số của Provider khác.
func TestLLMAdapterUsesPinnedProductionHosts(t *testing.T) {
	tests := []struct{ name, got, want string }{
		{"openai", newOpenAIAdapter("p", nil).base, "https://api.openai.com"},
		{"anthropic", newAnthropicAdapter("p", nil).base, "https://api.anthropic.com"},
		{"gemini", newGeminiAdapter("p", nil).base, "https://generativelanguage.googleapis.com"},
		{"openrouter", newOpenRouterAdapter("p", nil).base, "https://openrouter.ai"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Fatalf("new%sAdapter().base = %q; want %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestIsFallbackEligible(t *testing.T) {
	tests := []struct {
		kind llmErrorKind
		want bool
	}{
		{llmErrorNetwork, true},
		{llmErrorTimeout, true},
		{llmErrorRateLimit, true},
		{llmErrorUpstream, true},
		{llmErrorCredential, false},
		{llmErrorModel, false},
		{llmErrorRequest, false},
		{llmErrorPolicy, false},
		{llmErrorCanceled, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := isFallbackEligible(tt.kind); got != tt.want {
				t.Fatalf("isFallbackEligible(%q) = %v; want %v", tt.kind, got, tt.want)
			}
		})
	}
}

// Trần 2 MiB giữ cho một Provider hỏng (hoặc bị chiếm) không nuốt hết RAM của daemon.
//
// Phản hồi dưới đây là JSON HỢP LỆ và dài hơn trần. Đó là điều kiện để test này có nghĩa: bỏ
// io.LimitReader đi thì thân về nguyên vẹn, phân tích thành công và Generate trả về text — tức
// test đỏ. Một thân vốn đã hỏng sẵn thì xanh cả khi có trần lẫn khi không, và không canh gì cả.
func TestLLMAdapterCapsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output":[{"type":"message","content":[{"type":"output_text","text":"`)
		_, _ = io.Copy(w, io.LimitReader(endlessLetterA{}, llmResponseCap))
		_, _ = io.WriteString(w, `"}]}]}`)
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.Timeout = 30 * time.Second
	a := newOpenAIAdapter("prov-openai", client)
	a.base = srv.URL

	got, err := a.Generate(context.Background(),
		llmRequest{Model: "gpt-4o", Prompt: llmTestPrompt}, []byte(llmTestKey))
	if err == nil {
		t.Fatalf("Generate(thân dài hơn trần) = %d ký tự, error = nil; want lỗi vì thân bị cắt",
			len(got.Text))
	}
	// Cắt ngang thì JSON không đóng được ngoặc: hỏng phía Provider, nên là upstream.
	if kind := llmKindOf(t, err); kind != llmErrorUpstream {
		t.Fatalf("Generate(thân dài hơn trần) kind = %q; want %q", kind, llmErrorUpstream)
	}
}

type endlessLetterA struct{}

func (endlessLetterA) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}
