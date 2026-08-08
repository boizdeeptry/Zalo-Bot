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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// codexBackendURL là endpoint responses của codex ở chế độ ChatGPT-subscription (capture từ codex.exe).
const codexBackendURL = "https://chatgpt.com/backend-api/codex/responses"

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
