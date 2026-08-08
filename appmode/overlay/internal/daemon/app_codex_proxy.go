package daemon

// Codex proxy (thay đường CLI cho provider codex): gọi THẲNG backend ChatGPT của codex bằng token
// OAuth của account, thay vì spawn codex.exe. Endpoint + shape request/response ghim từ capture THẬT
// (probe 200 trên máy 2026-08-08): POST https://chatgpt.com/backend-api/codex/responses, body kiểu
// Responses API, trả SSE; câu trả lời gom từ event response.output_text.delta.
//
// LƯU Ý ĐỊNH VỊ: đây KHÔNG phải đường xanh "chính chủ" — nó proxy phiên OAuth y như 9Router, nên MANG
// rủi ro khoá tài khoản. Badge phải nói đúng sự thật (xem PX6). Chỉ dựng theo yêu cầu rõ của chủ sản phẩm.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentdc/internal/store"
)

// codexBackendURL là endpoint responses của codex ở chế độ ChatGPT-subscription (capture từ codex.exe).
const codexBackendURL = "https://chatgpt.com/backend-api/codex/responses"

// codexTokenURL + codexClientID: refresh token OAuth. client_id là ĐỊNH DANH CÔNG KHAI của codex CLI
// (public/native client, không phải bí mật) — ghim từ codex.exe. Refresh dùng grant_type=refresh_token.
const (
	codexTokenURL  = "https://auth.openai.com/oauth/token"
	codexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexUserAgent = "codex_cli_rs/0.0.0"
	codexScope     = "openid profile email"
)

// --- PX2: token store — đọc token OAuth từ <CODEX_HOME>/auth.json của một account ---

// codexTokens là bộ token proxy cần từ auth.json (auth_mode=chatgpt). Bí mật — KHÔNG log, KHÔNG đưa
// vào thân phản hồi Portal.
type codexTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	IDToken      string `json:"id_token"`
}

// codexAuthPath dựng đường tới auth.json trong CODEX_HOME của một account.
func codexAuthPath(configDir string) string {
	return filepath.Join(configDir, "auth.json")
}

// readCodexTokens đọc access/refresh/account_id từ <configDir>/auth.json. Lỗi khi thiếu file, JSON
// hỏng, hoặc không có access_token (chưa đăng nhập, hoặc account dùng API key chứ không phải ChatGPT).
func readCodexTokens(configDir string) (codexTokens, error) {
	if configDir == "" {
		return codexTokens{}, errors.New("configDir rỗng")
	}
	data, err := os.ReadFile(codexAuthPath(configDir))
	if err != nil {
		return codexTokens{}, fmt.Errorf("đọc auth.json: %w", err)
	}
	var f struct {
		Tokens *codexTokens `json:"tokens"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return codexTokens{}, fmt.Errorf("parse auth.json: %w", err)
	}
	if f.Tokens == nil || f.Tokens.AccessToken == "" {
		return codexTokens{}, errors.New("auth.json không có access_token (chưa đăng nhập ChatGPT)")
	}
	return *f.Tokens, nil
}

// --- PX3 (phần thuần): dựng body request + parse SSE — test được không cần mạng ---

// codexReqContent/Item/Body dựng thân Responses API tối thiểu mà probe đã xác nhận nhận (200).
// Tin nhắn khách (input KHÔNG tin được) chỉ nằm ở text của một input_text — không có đường nào nó
// thành cờ hay chỉ thị hệ thống.
type codexReqContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type codexReqItem struct {
	Type    string            `json:"type"`
	Role    string            `json:"role"`
	Content []codexReqContent `json:"content"`
}
type codexReqBody struct {
	Model        string         `json:"model"`
	Instructions string         `json:"instructions"`
	Input        []codexReqItem `json:"input"`
	Stream       bool           `json:"stream"`
	Store        bool           `json:"store"`
}

// codexResponsesBody dựng JSON body cho một lượt hỏi. prompt là toàn bộ ngữ cảnh (giống cách đường CLI
// truyền cả prompt cho `codex exec`). stream=true + store=false đúng như probe.
func codexResponsesBody(model, prompt string) ([]byte, error) {
	return json.Marshal(codexReqBody{
		Model:        model,
		Instructions: "",
		Input:        []codexReqItem{{Type: "message", Role: "user", Content: []codexReqContent{{Type: "input_text", Text: prompt}}}},
		Stream:       true,
		Store:        false,
	})
}

// parseCodexSSE gom câu trả lời từ luồng SSE của endpoint responses. Chỉ cộng dồn delta của event
// response.output_text.delta (đã verify trên probe: {"type":"response.output_text.delta","delta":"OK"}).
// response.failed / error → trả lỗi. Không thấy nội dung nào → lỗi "rỗng" để router fallback.
//
// Dòng data SSE có thể dài (một event mang cả object response) nên nới buffer scanner.
func parseCodexSSE(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out strings.Builder
	var failed string
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue // dòng data lạ (không JSON kỳ vọng) — bỏ qua, không làm hỏng cả lượt
		}
		switch ev.Type {
		case "response.output_text.delta":
			out.WriteString(ev.Delta)
		case "response.failed":
			if ev.Response.Error != nil {
				failed = ev.Response.Error.Message
			} else {
				failed = "response.failed"
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("đọc SSE: %w", err)
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		if failed != "" {
			return "", fmt.Errorf("codex responses lỗi: %s", failed)
		}
		return "", errors.New("codex responses không có nội dung")
	}
	return text, nil
}

// --- PX3 (phần mạng): gọi endpoint responses ---

// newCodexSessionID sinh một session_id kiểu UUIDv4 cho header. crypto/rand (không math/rand) — id
// gửi lên backend, không để trùng/đoán được.
func newCodexSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// codexDoResponses gửi MỘT lượt hỏi tới backend responses với đúng bộ header tối thiểu mà probe đã
// xác nhận nhận (200). Trả (text, statusCode, err). status != 200 KHÔNG phải err — caller phân loại
// (401 → refresh, 429 → rate limit…). Thân lỗi của backend KHÔNG đi vào err (có thể chép token/khoá).
func codexDoResponses(ctx context.Context, client *http.Client, tok codexTokens, model, prompt string) (string, int, error) {
	body, err := codexResponsesBody(model, prompt)
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexBackendURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("chatgpt-account-id", tok.AccountID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("User-Agent", codexUserAgent)
	req.Header.Set("session_id", newCodexSessionID())
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)) // drain để tái dùng kết nối; KHÔNG đưa vào lỗi
		return "", resp.StatusCode, nil
	}
	text, perr := parseCodexSSE(resp.Body)
	return text, resp.StatusCode, perr
}

// --- PX4: refresh token ---

// refreshCodexToken đổi refresh_token lấy access_token mới qua oauth/token. Trả token mới (access +
// có thể refresh/id mới). Không log secret. Lỗi khi refresh_token vắng, mạng, hoặc backend != 200.
func refreshCodexToken(ctx context.Context, client *http.Client, refreshToken string) (codexTokens, error) {
	if refreshToken == "" {
		return codexTokens{}, errors.New("thiếu refresh_token")
	}
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     codexClientID,
		"scope":         codexScope,
	})
	if err != nil {
		return codexTokens{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, bytes.NewReader(body))
	if err != nil {
		return codexTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", codexUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return codexTokens{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return codexTokens{}, fmt.Errorf("oauth/token trả %d", resp.StatusCode)
	}
	var r struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return codexTokens{}, fmt.Errorf("parse oauth/token: %w", err)
	}
	if r.AccessToken == "" {
		return codexTokens{}, errors.New("oauth/token không có access_token")
	}
	return codexTokens{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, IDToken: r.IDToken}, nil
}

// writeCodexTokens ghi token mới (sau refresh) vào auth.json, GIỮ NGUYÊN mọi trường khác của file (codex
// tự thêm field mới) — đọc vào map, chỉ thay access/refresh/id + last_refresh. Ghi qua .tmp rồi rename
// cho nguyên tử. refresh/id rỗng thì giữ giá trị cũ (một số refresh không trả refresh_token mới).
func writeCodexTokens(configDir string, tok codexTokens, lastRefresh string) error {
	path := codexAuthPath(configDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("đọc auth.json: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse auth.json: %w", err)
	}
	tokObj := map[string]json.RawMessage{}
	if raw, ok := root["tokens"]; ok {
		_ = json.Unmarshal(raw, &tokObj)
	}
	set := func(m map[string]json.RawMessage, k, v string) {
		if v == "" {
			return
		}
		b, _ := json.Marshal(v)
		m[k] = b
	}
	set(tokObj, "access_token", tok.AccessToken)
	set(tokObj, "refresh_token", tok.RefreshToken)
	set(tokObj, "id_token", tok.IDToken)
	set(tokObj, "account_id", tok.AccountID)
	tb, err := json.Marshal(tokObj)
	if err != nil {
		return err
	}
	root["tokens"] = tb
	set(root, "last_refresh", lastRefresh)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("ghi auth.json.tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

// codexProxyGenerate là một lượt hỏi HOÀN CHỈNH qua proxy: đọc token của account → gọi responses →
// nếu 401 thì refresh MỘT lần, ghi lại auth.json, rồi thử lại → parse SSE. Trả text hoặc lỗi kèm
// statusCode để router phân loại (429 rate limit, 401/403 credential…).
func codexProxyGenerate(ctx context.Context, client *http.Client, configDir, model, prompt string) (string, int, error) {
	tok, err := readCodexTokens(configDir)
	if err != nil {
		return "", 0, err
	}
	text, status, err := codexDoResponses(ctx, client, tok, model, prompt)
	if status == http.StatusUnauthorized {
		newTok, rerr := refreshCodexToken(ctx, client, tok.RefreshToken)
		if rerr != nil {
			return "", status, fmt.Errorf("refresh token: %w", rerr)
		}
		if newTok.RefreshToken == "" {
			newTok.RefreshToken = tok.RefreshToken // refresh không trả cái mới → giữ cái cũ
		}
		newTok.AccountID = tok.AccountID
		_ = writeCodexTokens(configDir, newTok, time.Now().UTC().Format(time.RFC3339)) // best-effort: lỗi ghi không chặn lượt
		text, status, err = codexDoResponses(ctx, client, newTok, model, prompt)
	}
	if err != nil {
		return "", status, err
	}
	if status != http.StatusOK {
		return "", status, fmt.Errorf("codex backend trả %d", status)
	}
	return text, status, nil
}

// --- PX5: adapter — proxy THAY cliAdapter cho provider codex ---

var _ providerAdapter = (*codexProxyAdapter)(nil)

// codexProxyAdapter chạy một lượt Codex bằng cách gọi thẳng backend qua token OAuth của account, thay
// vì spawn codex.exe. Router dùng nó y như một adapter thường. credential KHÔNG dùng — token nằm trong
// auth.json của account, không đi qua daemon.
type codexProxyAdapter struct {
	providerID string
	logger     *slog.Logger
	client     *http.Client
	// pickConfigDir chọn account cho lượt này (round-robin+cooldown, cùng selector với các CLI) và
	// trả CODEX_HOME của nó + penalize. nil = chưa wire account (đường Test bare) → trả credential.
	pickConfigDir func() (configDir string, penalize func(rateLimited bool), ok bool)
	// generate là seam test — nil = gọi codexProxyGenerate thật (mạng).
	generate func(ctx context.Context, client *http.Client, configDir, model, prompt string) (string, int, error)
}

// Generate gọi backend qua proxy rồi ánh xạ status/err về taxonomy router (fallback hay dừng chuỗi).
func (a *codexProxyAdapter) Generate(ctx context.Context, req llmRequest, _ []byte) (llmResponse, error) {
	if a.pickConfigDir == nil {
		return llmResponse{}, newLLMError(llmErrorCredential, nil, "codex: chưa chọn được tài khoản")
	}
	configDir, penalize, ok := a.pickConfigDir()
	if !ok {
		// 0 account đăng nhập = credential (DỪNG chuỗi), như cliAdapter khi accountEnv ok=false.
		return llmResponse{}, newLLMError(llmErrorCredential, nil, "codex: chưa có tài khoản nào đăng nhập")
	}
	gen := a.generate
	if gen == nil {
		gen = codexProxyGenerate
	}
	text, status, err := gen(ctx, a.client, configDir, req.Model, req.Prompt)
	if err == nil && status == http.StatusOK {
		return textOrUpstream("codex proxy", text)
	}
	if status == 0 { // chưa có phản hồi → lỗi transport/huỷ ctx
		return llmResponse{}, transportError("codex proxy", err)
	}
	var kind llmErrorKind
	if status == http.StatusOK {
		// 200 nhưng nội dung lỗi (response.failed / rỗng): phân loại theo thông báo, như đường CLI
		// đọc stderr — "usage limit" → rate_limit (đi tiếp), còn lại → rate_limit mặc định.
		kind = classifyCLIError(errString(err), false)
	} else {
		kind = classifyCodexStatus(status)
	}
	if penalize != nil {
		penalize(kind == llmErrorRateLimit)
	}
	// Thân lỗi backend KHÔNG đi vào thông báo (có thể chép token/khoá) — chỉ tên + loại.
	return llmResponse{}, newLLMError(kind, nil, "codex proxy: gọi hỏng")
}

// Test dò đăng nhập RẺ: chọn account, đọc token trong auth.json. Có token → nil; không → credential.
func (a *codexProxyAdapter) Test(_ context.Context, _ string, _ []byte) error {
	if a.pickConfigDir == nil {
		return newLLMError(llmErrorCredential, nil, "codex: chưa chọn được tài khoản")
	}
	configDir, _, ok := a.pickConfigDir()
	if !ok {
		return newLLMError(llmErrorCredential, nil, "codex: chưa có tài khoản nào đăng nhập")
	}
	if _, err := readCodexTokens(configDir); err != nil {
		return newLLMError(llmErrorCredential, nil, "codex: tài khoản chưa đăng nhập hoặc thiếu token")
	}
	return nil
}

// Discover: khám phá THẬT của codex đi qua handleLLMProviderDiscover (cliProviderModels — đọc cache
// live). Đây chỉ là fallback tĩnh để thoả interface, không phải đường được gọi trong luồng discover.
func (a *codexProxyAdapter) Discover(_ context.Context, _ []byte) ([]store.LLMModel, error) {
	return descriptorModels("codex", a.providerID), nil
}

// classifyCodexStatus ánh xạ HTTP status backend → taxonomy. 401/403 credential (refresh đã thử ở
// codexProxyGenerate), 429 rate_limit, 400/422 request, 5xx upstream; còn lại rate_limit (mơ hồ →
// cho chuỗi đi tiếp, đồng nhất với classifyCLIError).
func classifyCodexStatus(status int) llmErrorKind {
	switch {
	case status == http.StatusTooManyRequests:
		return llmErrorRateLimit
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return llmErrorCredential
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return llmErrorRequest
	case status >= 500:
		return llmErrorUpstream
	default:
		return llmErrorRateLimit
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
