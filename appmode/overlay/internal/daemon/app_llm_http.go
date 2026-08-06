package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"agentdc/internal/store"
)

// Gốc endpoint sản xuất của bốn API chính thức. Là hằng số chứ không phải cấu hình: một Provider
// "OpenAI" trỏ tới máy chủ khác thì không còn là OpenAI, và một ô nhập URL trong Portal là đúng
// một đường để khoá thật đi tới một nơi không phải nhà cung cấp.
const (
	openAIBase     = "https://api.openai.com"
	anthropicBase  = "https://api.anthropic.com"
	geminiBase     = "https://generativelanguage.googleapis.com"
	openRouterBase = "https://openrouter.ai"
)

// llmResponseCap chặn trần thân phản hồi ở 2 MiB.
//
// Một lượt trả lời hợp lệ không tới vài chục KB, nên trần này chỉ chạm tới khi Provider hỏng
// hoặc phản hồi không phải thứ ta tưởng — và ở đúng hai trường hợp đó, đọc hết là cách một máy
// chủ ở xa lấy hết RAM của daemon.
const llmResponseCap = 2 << 20

// anthropicVersion là phiên bản API Anthropic yêu cầu trên MỌI request.
const anthropicVersion = "2023-06-01"

// anthropicMaxTokens: /v1/messages BẮT BUỘC có max_tokens, không có giá trị "không giới hạn".
// 4096 đủ cho một lượt trả lời khách và đủ nhỏ để một model đi lạc không đốt hết hạn mức.
const anthropicMaxTokens = 4096

// Header định danh ứng dụng mà tài liệu OpenRouter khuyến nghị (nghiên cứu 9Router §4). Thiếu
// chúng request vẫn chạy; có thì bảng xếp hạng của họ biết lưu lượng đến từ đâu.
//
// Tên miền dùng hậu tố .invalid dành riêng theo RFC 6761: ứng dụng này chưa có site công khai,
// và khai một domain không phải của mình là mạo nhận người khác.
const (
	openRouterReferer = "https://agentdc.invalid/"
	openRouterTitle   = "AgentDC Portal"
)

// llmHTTP là phần vận chuyển dùng chung của cả bốn adapter: gửi request có chặn trần, và biến
// mã trạng thái thành một llmErrorKind.
//
// Bốn Provider có bốn hình dạng payload khác nhau nên codec KHÔNG gộp được, nhưng phần "gọi đi,
// đọc về, phân loại lỗi" thì giống hệt — và đó mới là chỗ một bản sao thứ hai sẽ lệch đi rồi
// nằm im.
type llmHTTP struct {
	client *http.Client
	base   string
}

// post gửi payload JSON và trả thân phản hồi đã đọc có chặn trần.
func (h llmHTTP) post(ctx context.Context, op, path string, header http.Header, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, newLLMError(llmErrorRequest, err, "%s: mã hoá payload", op)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, newLLMError(llmErrorRequest, err, "%s: dựng request", op)
	}
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/json")
	return h.do(op, req)
}

func (h llmHTTP) get(ctx context.Context, op, path string, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+path, nil)
	if err != nil {
		return nil, newLLMError(llmErrorRequest, err, "%s: dựng request", op)
	}
	req.Header = header.Clone()
	return h.do(op, req)
}

func (h llmHTTP) do(op string, req *http.Request) ([]byte, error) {
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, transportError(op, err)
	}
	// Không rút cạn body trước khi đóng: thân dài hơn trần là thứ ta CỐ Ý bỏ dở, và đọc nốt để
	// tái dùng kết nối chính là việc mà trần vừa từ chối làm.
	defer resp.Body.Close() //nolint:errcheck // Đóng thân phản hồi đọc dở không nói thêm được gì.

	// Hỏng giữa lúc đọc thân vẫn là hỏng tầng vận chuyển — kể cả khi header đã về. Phân loại
	// bằng chính transportError để một lượt hết giờ giữa chừng không bị ghi nhầm thành upstream.
	body, err := io.ReadAll(io.LimitReader(resp.Body, llmResponseCap))
	if err != nil {
		return nil, transportError(op, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(op, req, resp.StatusCode)
	}
	return body, nil
}

// statusError dựng câu lỗi từ status, method và URL — KHÔNG từ body.
//
// Đây là ràng buộc cấu trúc chứ không phải sở thích: sanitizeProviderError chỉ bắt được những
// hình dạng credential đã biết, nên một khoá không mang tiền tố nào mà Provider chép vào câu văn
// của họ sẽ đi thẳng ra log. Không đọc body vào thông báo thì không có gì để rò.
func statusError(op string, req *http.Request, status int) *llmError {
	// Bỏ query khỏi URL: hiện tại không adapter nào để khoá ở đó, và đây là thứ giữ cho câu nói
	// trên vẫn đúng nếu mai có người thêm một adapter dùng ?key=.
	safe := *req.URL
	safe.RawQuery, safe.Fragment = "", ""
	return newLLMError(classifyStatus(status), nil, "%s: %s %s trả %d", op, req.Method, safe.String(), status)
}

// classifyStatus ánh xạ mã HTTP sang loại lỗi. Ranh giới duy nhất đáng nhớ: chỉ 429 và 5xx đi
// tiếp được sang Provider sau.
func classifyStatus(status int) llmErrorKind {
	switch {
	case status == http.StatusTooManyRequests:
		return llmErrorRateLimit
	case status >= 500:
		return llmErrorUpstream
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return llmErrorCredential
	// 404 trên bốn API này gần như luôn là "model không tồn tại": đường dẫn còn lại đều cố định
	// trong tệp này, nên chúng không gõ sai được lúc chạy.
	case status == http.StatusNotFound:
		return llmErrorModel
	case status >= 400:
		return llmErrorRequest
	default:
		// 2xx khác 200 và 3xx: không phải thứ hợp đồng nói tới, nên coi là Provider đang hỏng.
		return llmErrorUpstream
	}
}

// decodeLLMJSON đọc thân phản hồi thành v.
//
// Body hỏng là lỗi upstream — Provider trả về thứ giao thức không cho phép — nên nó ĐƯỢC đi
// tiếp sang Provider sau. Lỗi gốc của encoding/json bị bỏ đi vì nó trích một mẩu chính cái body
// đang bị nghi ngờ.
func decodeLLMJSON(op string, body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return newLLMError(llmErrorUpstream, nil, "%s: phản hồi không phải JSON hợp lệ", op)
	}
	return nil
}

// policyError là nội dung bị Provider từ chối. Dừng chuỗi, không đổi Provider.
func policyError(op string) error {
	return newLLMError(llmErrorPolicy, nil, "%s: Provider từ chối nội dung theo chính sách", op)
}

// textOrUpstream biến một phản hồi 200 nhưng rỗng thành lỗi upstream.
//
// "Thành công mà không có chữ nào" không phải một lượt trả lời — để nó lọt qua thì khách nhận
// được tin nhắn trống và không có gì trong log nói vì sao.
func textOrUpstream(op, text string) (llmResponse, error) {
	if strings.TrimSpace(text) == "" {
		return llmResponse{}, newLLMError(llmErrorUpstream, nil, "%s: phản hồi không có nội dung", op)
	}
	return llmResponse{Text: text}, nil
}

// discoveredModel chuẩn hoá một mục trong danh sách model; ok = false thì bỏ qua mục đó.
//
// Tên rỗng lấy id làm tên vì Portal hiển thị Name, và một ô trống ở đó không cho biết model nào.
func discoveredModel(providerID, modelID, name string) (store.LLMModel, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return store.LLMModel{}, false
	}
	if name = strings.TrimSpace(name); name == "" {
		name = modelID
	}
	return store.LLMModel{
		ProviderID: providerID, ModelID: modelID, Name: name,
		Source: store.LLMModelDiscovered, Available: true,
	}, true
}

// sortLLMModels sắp xếp theo model id để danh sách ổn định giữa hai lần đồng bộ.
//
// Thứ tự Provider trả về không có bảo đảm nào, và một danh sách nhảy chỗ mỗi lần làm mới khiến
// người dùng không tin nổi cái mình đang nhìn.
//
// "Ổn định" ở đây nghĩa là XÁC ĐỊNH giữa các lần đồng bộ, không phải nghĩa stable-sort: khoá
// sắp xếp là model id, mà (provider_id, model_id) là khoá chính của bảng llm_models nên không
// có hai mục cùng khoá để mà giữ thứ tự tương đối. SortStableFunc chỉ là bảo hiểm rẻ tiền cho
// trường hợp Provider trả trùng id — lúc đó upsert dưới database cũng gộp chúng lại.
func sortLLMModels(models []store.LLMModel) []store.LLMModel {
	slices.SortStableFunc(models, func(a, b store.LLMModel) int {
		return strings.Compare(a.ModelID, b.ModelID)
	})
	return models
}

// assertModelListed đối chiếu model đang cấu hình với danh sách Provider vừa trả về.
//
// Test dùng chính lượt liệt kê model làm phép thử vô hại: nó xác thực credential mà không sinh
// token nào. Đã cầm danh sách trong tay thì soát luôn model đang cấu hình, vì "khoá đúng nhưng
// model gõ sai" chỉ lộ ra vào lúc có tin nhắn khách — đúng lúc tệ nhất.
func assertModelListed(op, model string, models []store.LLMModel) error {
	if model == "" {
		return nil
	}
	if slices.ContainsFunc(models, func(m store.LLMModel) bool { return m.ModelID == model }) {
		return nil
	}
	return newLLMError(llmErrorModel, nil, "%s: Provider không liệt kê model %q", op, model)
}

// bearerHeader là header xác thực của OpenAI và OpenRouter — chỗ hai Provider này thật sự nói
// chung một giao thức.
func bearerHeader(credential []byte) http.Header {
	return http.Header{"Authorization": {"Bearer " + string(credential)}}
}

// openAIStyleModels là bao ngoài của GET models mà CẢ OpenAI lẫn OpenRouter dùng.
//
// Một bản parse duy nhất, theo đúng bài học của 9Router (§1): 150 Provider của họ không ứng với
// 150 codec, mà với một nhúm format dùng chung. Phần generate thì không gộp được — /v1/responses
// và /chat/completions là hai hình dạng khác hẳn nhau — nên chúng ở lại chỗ riêng bên dưới.
type openAIStyleModels struct {
	Data []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"data"`
}

func (m openAIStyleModels) normalize(providerID string) []store.LLMModel {
	out := make([]store.LLMModel, 0, len(m.Data))
	for _, item := range m.Data {
		if model, ok := discoveredModel(providerID, item.ID, item.Name); ok {
			out = append(out, model)
		}
	}
	return sortLLMModels(out)
}

var (
	_ providerAdapter = (*openAIAdapter)(nil)
	_ providerAdapter = (*anthropicAdapter)(nil)
	_ providerAdapter = (*geminiAdapter)(nil)
	_ providerAdapter = (*openRouterAdapter)(nil)
)

// --- OpenAI ---

// openAIAdapter nói giao thức Responses API.
//
// providerID nằm trong struct còn credential thì không: id là thứ công khai và cố định theo
// hàng cấu hình, nên Discover trả về model đã gắn đúng chủ mà không cần người gọi vá lại.
type openAIAdapter struct {
	llmHTTP
	providerID string
}

// newOpenAIAdapter dựng adapter dùng client được tiêm vào. Người gọi PHẢI đặt client.Timeout —
// ctx chặn được một lượt gọi, nhưng một client không hạn giờ vẫn treo được lúc bắt tay TLS.
func newOpenAIAdapter(providerID string, client *http.Client) *openAIAdapter {
	return &openAIAdapter{llmHTTP{client: client, base: openAIBase}, providerID}
}

// openAIResponses là phản hồi của POST /v1/responses.
type openAIResponses struct {
	Output []struct {
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Refusal string `json:"refusal"`
		} `json:"content"`
	} `json:"output"`
	IncompleteDetails struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func (a *openAIAdapter) Generate(ctx context.Context, req llmRequest, credential []byte) (llmResponse, error) {
	const op = "openai generate"
	body, err := a.post(ctx, op, "/v1/responses", bearerHeader(credential), map[string]any{
		"model": req.Model,
		"input": req.Prompt,
	})
	if err != nil {
		return llmResponse{}, err
	}
	var payload openAIResponses
	if err := decodeLLMJSON(op, body, &payload); err != nil {
		return llmResponse{}, err
	}
	if payload.IncompleteDetails.Reason == "content_filter" {
		return llmResponse{}, policyError(op)
	}

	var text strings.Builder
	refused := false
	for _, item := range payload.Output {
		for _, c := range item.Content {
			switch c.Type {
			case "output_text":
				text.WriteString(c.Text)
			case "refusal":
				refused = true
			}
		}
	}
	// Từ chối kèm chữ vẫn là một lượt trả lời được: model nói vì sao nó không làm. Chỉ khi không
	// còn gì để gửi cho khách thì đây mới là lỗi chính sách.
	if refused && text.Len() == 0 {
		return llmResponse{}, policyError(op)
	}
	return textOrUpstream(op, text.String())
}

func (a *openAIAdapter) Test(ctx context.Context, model string, credential []byte) error {
	models, err := a.Discover(ctx, credential)
	if err != nil {
		return err
	}
	return assertModelListed("openai test", model, models)
}

func (a *openAIAdapter) Discover(ctx context.Context, credential []byte) ([]store.LLMModel, error) {
	const op = "openai discover"
	body, err := a.get(ctx, op, "/v1/models", bearerHeader(credential))
	if err != nil {
		return nil, err
	}
	var payload openAIStyleModels
	if err := decodeLLMJSON(op, body, &payload); err != nil {
		return nil, err
	}
	return payload.normalize(a.providerID), nil
}

// --- Anthropic ---

type anthropicAdapter struct {
	llmHTTP
	providerID string
}

func newAnthropicAdapter(providerID string, client *http.Client) *anthropicAdapter {
	return &anthropicAdapter{llmHTTP{client: client, base: anthropicBase}, providerID}
}

func anthropicHeader(credential []byte) http.Header {
	return http.Header{
		"X-Api-Key":         {string(credential)},
		"Anthropic-Version": {anthropicVersion},
	}
}

// anthropicMessage là phản hồi của POST /v1/messages.
type anthropicMessage struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
}

// anthropicModels là phản hồi của GET /v1/models. Không dùng chung bao ngoài với OpenAI: tên
// hiển thị nằm ở display_name chứ không phải name, và gộp lại thì mọi model mất tên.
type anthropicModels struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

func (a *anthropicAdapter) Generate(ctx context.Context, req llmRequest, credential []byte) (llmResponse, error) {
	const op = "anthropic generate"
	body, err := a.post(ctx, op, "/v1/messages", anthropicHeader(credential), map[string]any{
		"model":      req.Model,
		"max_tokens": anthropicMaxTokens,
		"messages":   []map[string]string{{"role": "user", "content": req.Prompt}},
	})
	if err != nil {
		return llmResponse{}, err
	}
	var payload anthropicMessage
	if err := decodeLLMJSON(op, body, &payload); err != nil {
		return llmResponse{}, err
	}
	if payload.StopReason == "refusal" {
		return llmResponse{}, policyError(op)
	}
	var text strings.Builder
	for _, c := range payload.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return textOrUpstream(op, text.String())
}

func (a *anthropicAdapter) Test(ctx context.Context, model string, credential []byte) error {
	models, err := a.Discover(ctx, credential)
	if err != nil {
		return err
	}
	return assertModelListed("anthropic test", model, models)
}

func (a *anthropicAdapter) Discover(ctx context.Context, credential []byte) ([]store.LLMModel, error) {
	const op = "anthropic discover"
	// limit=1000 là trần của API. Mặc định chỉ 20 model một trang, và một danh sách cụt trông y
	// hệt một danh sách đủ — model thiếu chỉ lộ ra khi người dùng đi tìm cái không có ở đó.
	body, err := a.get(ctx, op, "/v1/models?limit=1000", anthropicHeader(credential))
	if err != nil {
		return nil, err
	}
	var payload anthropicModels
	if err := decodeLLMJSON(op, body, &payload); err != nil {
		return nil, err
	}
	out := make([]store.LLMModel, 0, len(payload.Data))
	for _, item := range payload.Data {
		if model, ok := discoveredModel(a.providerID, item.ID, item.DisplayName); ok {
			out = append(out, model)
		}
	}
	return sortLLMModels(out), nil
}

// --- Gemini ---

type geminiAdapter struct {
	llmHTTP
	providerID string
}

func newGeminiAdapter(providerID string, client *http.Client) *geminiAdapter {
	return &geminiAdapter{llmHTTP{client: client, base: geminiBase}, providerID}
}

// geminiHeader gửi khoá qua header chứ KHÔNG qua ?key= dù API nhận cả hai.
//
// URL đi vào câu lỗi net/http tự sinh, vào log truy cập của mọi proxy trên đường và vào lịch sử
// của bất cứ công cụ nào chạm tới — header thì không đi đâu cả.
func geminiHeader(credential []byte) http.Header {
	return http.Header{"X-Goog-Api-Key": {string(credential)}}
}

// geminiGenerate là phản hồi của POST :generateContent.
type geminiGenerate struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
}

// geminiBlockedFinish là các lý do dừng đồng nghĩa "bị chặn", đối lại "STOP"/"MAX_TOKENS" là
// dừng bình thường.
var geminiBlockedFinish = []string{
	"SAFETY", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "RECITATION", "IMAGE_SAFETY",
}

type geminiModels struct {
	Models []struct {
		Name                       string   `json:"name"`
		DisplayName                string   `json:"displayName"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	} `json:"models"`
}

func (a *geminiAdapter) Generate(ctx context.Context, req llmRequest, credential []byte) (llmResponse, error) {
	const op = "gemini generate"
	// Model nằm TRONG đường dẫn ở API này, nên nó phải được escape: một model id có "/" mà đi
	// thẳng vào URL sẽ gọi sang một endpoint khác hẳn.
	path := "/v1beta/models/" + url.PathEscape(req.Model) + ":generateContent"
	body, err := a.post(ctx, op, path, geminiHeader(credential), map[string]any{
		"contents": []map[string]any{{
			"role":  "user",
			"parts": []map[string]string{{"text": req.Prompt}},
		}},
	})
	if err != nil {
		return llmResponse{}, err
	}
	var payload geminiGenerate
	if err := decodeLLMJSON(op, body, &payload); err != nil {
		return llmResponse{}, err
	}
	// Hai chỗ báo bị chặn: promptFeedback khi chính prompt bị từ chối, finishReason khi câu trả
	// lời bị cắt giữa chừng. Bỏ sót chỗ nào thì lần đó rơi nhầm vào upstream và chuỗi đi tiếp.
	if payload.PromptFeedback.BlockReason != "" {
		return llmResponse{}, policyError(op)
	}
	var text strings.Builder
	for _, c := range payload.Candidates {
		if slices.Contains(geminiBlockedFinish, c.FinishReason) {
			return llmResponse{}, policyError(op)
		}
		for _, p := range c.Content.Parts {
			text.WriteString(p.Text)
		}
	}
	return textOrUpstream(op, text.String())
}

func (a *geminiAdapter) Test(ctx context.Context, model string, credential []byte) error {
	models, err := a.Discover(ctx, credential)
	if err != nil {
		return err
	}
	return assertModelListed("gemini test", model, models)
}

func (a *geminiAdapter) Discover(ctx context.Context, credential []byte) ([]store.LLMModel, error) {
	const op = "gemini discover"
	// pageSize=1000 là trần của API; mặc định 50 thì cắt mất phần đuôi danh sách.
	body, err := a.get(ctx, op, "/v1beta/models?pageSize=1000", geminiHeader(credential))
	if err != nil {
		return nil, err
	}
	var payload geminiModels
	if err := decodeLLMJSON(op, body, &payload); err != nil {
		return nil, err
	}
	out := make([]store.LLMModel, 0, len(payload.Models))
	for _, item := range payload.Models {
		// Danh sách này gồm cả model nhúng và model đếm token. Đưa chúng vào chuỗi fallback là
		// đưa vào một mắt xích chắc chắn hỏng, và nó chỉ hỏng lúc có tin nhắn khách.
		if !slices.Contains(item.SupportedGenerationMethods, "generateContent") {
			continue
		}
		// Tên trả về có dạng "models/gemini-2.5-flash" còn endpoint generate lại tự thêm tiền tố
		// đó, nên phải cắt — không cắt thì URL thành ".../models/models/gemini-2.5-flash".
		id := strings.TrimPrefix(item.Name, "models/")
		if model, ok := discoveredModel(a.providerID, id, item.DisplayName); ok {
			out = append(out, model)
		}
	}
	return sortLLMModels(out), nil
}

// --- OpenRouter ---

type openRouterAdapter struct {
	llmHTTP
	providerID string
}

func newOpenRouterAdapter(providerID string, client *http.Client) *openRouterAdapter {
	return &openRouterAdapter{llmHTTP{client: client, base: openRouterBase}, providerID}
}

func openRouterHeader(credential []byte) http.Header {
	h := bearerHeader(credential)
	h.Set("HTTP-Referer", openRouterReferer)
	h.Set("X-Title", openRouterTitle)
	return h
}

// openRouterChat là phản hồi của POST /api/v1/chat/completions.
type openRouterChat struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func (a *openRouterAdapter) Generate(ctx context.Context, req llmRequest, credential []byte) (llmResponse, error) {
	const op = "openrouter generate"
	body, err := a.post(ctx, op, "/api/v1/chat/completions", openRouterHeader(credential), map[string]any{
		"model":    req.Model,
		"messages": []map[string]string{{"role": "user", "content": req.Prompt}},
	})
	if err != nil {
		return llmResponse{}, err
	}
	var payload openRouterChat
	if err := decodeLLMJSON(op, body, &payload); err != nil {
		return llmResponse{}, err
	}
	var text strings.Builder
	for _, c := range payload.Choices {
		if c.FinishReason == "content_filter" {
			return llmResponse{}, policyError(op)
		}
		text.WriteString(c.Message.Content)
	}
	return textOrUpstream(op, text.String())
}

func (a *openRouterAdapter) Test(ctx context.Context, model string, credential []byte) error {
	models, err := a.Discover(ctx, credential)
	if err != nil {
		return err
	}
	return assertModelListed("openrouter test", model, models)
}

func (a *openRouterAdapter) Discover(ctx context.Context, credential []byte) ([]store.LLMModel, error) {
	const op = "openrouter discover"
	body, err := a.get(ctx, op, "/api/v1/models", openRouterHeader(credential))
	if err != nil {
		return nil, err
	}
	var payload openAIStyleModels
	if err := decodeLLMJSON(op, body, &payload); err != nil {
		return nil, err
	}
	return payload.normalize(a.providerID), nil
}
