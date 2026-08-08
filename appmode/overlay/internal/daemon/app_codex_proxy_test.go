package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexSSEFixture tái hiện ĐÚNG shape SSE mà probe thật trả về (2026-08-08): các event responses,
// câu trả lời nằm ở các delta của response.output_text.delta.
const codexSSEFixture = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"id":"msg_1","type":"message","role":"assistant"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Xin ","item_id":"msg_1"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"chào","item_id":"msg_1"}

event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","text":"Xin chào"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}
`

func TestParseCodexSSEAccumulatesDeltas(t *testing.T) {
	got, err := parseCodexSSE(strings.NewReader(codexSSEFixture))
	if err != nil {
		t.Fatalf("parseCodexSSE = %v; want nil", err)
	}
	if got != "Xin chào" {
		t.Fatalf("parseCodexSSE = %q; want %q", got, "Xin chào")
	}
}

func TestParseCodexSSEFailedEventIsError(t *testing.T) {
	const failed = `event: response.failed
data: {"type":"response.failed","response":{"status":"failed","error":{"message":"usage limit"}}}
`
	_, err := parseCodexSSE(strings.NewReader(failed))
	if err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("parseCodexSSE(failed) err = %v; want chứa 'usage limit'", err)
	}
}

func TestParseCodexSSEEmptyIsError(t *testing.T) {
	const empty = `event: response.created
data: {"type":"response.created","response":{"status":"in_progress"}}
`
	if _, err := parseCodexSSE(strings.NewReader(empty)); err == nil {
		t.Fatalf("parseCodexSSE(no content) err = nil; want lỗi 'không có nội dung'")
	}
}

func TestCodexResponsesBodyShape(t *testing.T) {
	raw, err := codexResponsesBody("gpt-5.4-mini", "tin nhắn khách")
	if err != nil {
		t.Fatalf("codexResponsesBody = %v; want nil", err)
	}
	var body codexReqBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal body = %v; want nil", err)
	}
	if body.Model != "gpt-5.4-mini" || !body.Stream || body.Store {
		t.Errorf("body = %+v; want model=gpt-5.4-mini stream=true store=false", body)
	}
	if len(body.Input) != 1 || len(body.Input[0].Content) != 1 ||
		body.Input[0].Role != "user" || body.Input[0].Content[0].Type != "input_text" ||
		body.Input[0].Content[0].Text != "tin nhắn khách" {
		t.Errorf("input = %+v; want một message user input_text mang đúng prompt", body.Input)
	}
}

// codexAuthFixture ghi một auth.json giả (đúng shape thật: {auth_mode, tokens:{...}}) vào dir tạm.
func codexAuthFixture(t *testing.T, access string) string {
	t.Helper()
	dir := t.TempDir()
	f := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": "",
		"tokens": map[string]string{
			"access_token": access, "refresh_token": "rt-xyz", "account_id": "acct-1", "id_token": "id-jwt",
		},
		"last_refresh": "2026-08-08T00:00:00Z",
	}
	data, _ := json.Marshal(f)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
		t.Fatalf("ghi fixture auth.json = %v", err)
	}
	return dir
}

func TestReadCodexTokens(t *testing.T) {
	tok, err := readCodexTokens(codexAuthFixture(t, "at-123"))
	if err != nil {
		t.Fatalf("readCodexTokens = %v; want nil", err)
	}
	if tok.AccessToken != "at-123" || tok.RefreshToken != "rt-xyz" || tok.AccountID != "acct-1" {
		t.Fatalf("tokens = %+v; want at-123/rt-xyz/acct-1", tok)
	}
}

func TestReadCodexTokensErrors(t *testing.T) {
	if _, err := readCodexTokens(t.TempDir()); err == nil {
		t.Errorf("readCodexTokens(no file) = nil; want lỗi")
	}
	if _, err := readCodexTokens(codexAuthFixture(t, "")); err == nil {
		t.Errorf("readCodexTokens(empty access_token) = nil; want lỗi")
	}
	if _, err := readCodexTokens(""); err == nil {
		t.Errorf("readCodexTokens(\"\") = nil; want lỗi")
	}
}
