package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubClient trả một *http.Client mà mọi request đi qua fn — test HTTP không chạm mạng.
type stubRoundTripper struct {
	fn func(*http.Request) (*http.Response, error)
}

func (s stubRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return s.fn(req) }

func stubClient(fn func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: stubRoundTripper{fn}}
}

func httpResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

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

func TestCodexDoResponsesSendsHeadersAndParses(t *testing.T) {
	client := stubClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != codexBackendURL {
			t.Fatalf("URL = %s; want %s", req.URL, codexBackendURL)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer at-1" {
			t.Errorf("Authorization = %q; want Bearer at-1", got)
		}
		if got := req.Header.Get("chatgpt-account-id"); got != "acct-1" {
			t.Errorf("chatgpt-account-id = %q; want acct-1", got)
		}
		if got := req.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q; want text/event-stream", got)
		}
		return httpResp(200, codexSSEFixture), nil
	})
	text, status, err := codexDoResponses(context.Background(), client,
		codexTokens{AccessToken: "at-1", AccountID: "acct-1"}, "gpt-5.4-mini", "hi")
	if err != nil || status != 200 {
		t.Fatalf("codexDoResponses status=%d err=%v", status, err)
	}
	if text != "Xin chào" {
		t.Fatalf("text = %q; want %q", text, "Xin chào")
	}
}

func TestCodexDoResponsesNon200ReturnsStatusNotError(t *testing.T) {
	client := stubClient(func(*http.Request) (*http.Response, error) { return httpResp(429, "rate limited"), nil })
	text, status, err := codexDoResponses(context.Background(), client, codexTokens{AccessToken: "x"}, "m", "p")
	if err != nil || status != 429 || text != "" {
		t.Fatalf("got text=%q status=%d err=%v; want ''/429/nil (caller phân loại)", text, status, err)
	}
}

func TestRefreshCodexTokenSendsGrant(t *testing.T) {
	client := stubClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != codexTokenURL {
			t.Fatalf("URL = %s; want %s", req.URL, codexTokenURL)
		}
		body, _ := io.ReadAll(req.Body)
		var m map[string]string
		_ = json.Unmarshal(body, &m)
		if m["grant_type"] != "refresh_token" || m["refresh_token"] != "rt-old" || m["client_id"] != codexClientID {
			t.Errorf("refresh body = %v; want grant=refresh_token rt=rt-old client_id=%s", m, codexClientID)
		}
		return httpResp(200, `{"access_token":"at-new","refresh_token":"rt-new","id_token":"id-new"}`), nil
	})
	tok, err := refreshCodexToken(context.Background(), client, "rt-old")
	if err != nil {
		t.Fatalf("refreshCodexToken = %v; want nil", err)
	}
	if tok.AccessToken != "at-new" || tok.RefreshToken != "rt-new" || tok.IDToken != "id-new" {
		t.Fatalf("tok = %+v; want at-new/rt-new/id-new", tok)
	}
}

func TestWriteCodexTokensPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	orig := `{"auth_mode":"chatgpt","OPENAI_API_KEY":"","tokens":{"access_token":"old","refresh_token":"rt","account_id":"acct-1","id_token":"idt","extra":"keep"},"weird":"root-keep"}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCodexTokens(dir, codexTokens{AccessToken: "new", RefreshToken: "rt2"}, "2026-08-08"); err != nil {
		t.Fatalf("writeCodexTokens = %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "auth.json"))
	var root map[string]any
	_ = json.Unmarshal(data, &root)
	if root["weird"] != "root-keep" || root["last_refresh"] != "2026-08-08" {
		t.Errorf("root fields = %v; want weird kept + last_refresh set", root)
	}
	toks, _ := root["tokens"].(map[string]any)
	if toks["access_token"] != "new" || toks["refresh_token"] != "rt2" {
		t.Errorf("tokens not updated: %v", toks)
	}
	if toks["account_id"] != "acct-1" || toks["extra"] != "keep" {
		t.Errorf("tokens lost preserved fields: %v", toks)
	}
}

// TestCodexProxyGenerateRefreshesOn401 ghim đường lõi: access hết hạn (401) → refresh → ghi lại
// auth.json → thử lại thành công. Đúng ba lượt gọi: responses(401) → oauth/token → responses(200).
func TestCodexProxyGenerateRefreshesOn401(t *testing.T) {
	dir := codexAuthFixture(t, "at-expired")
	var calls []string
	client := stubClient(func(req *http.Request) (*http.Response, error) {
		u := req.URL.String()
		auth := req.Header.Get("Authorization")
		calls = append(calls, u)
		switch {
		case u == codexBackendURL && auth == "Bearer at-expired":
			return httpResp(401, `{"error":"expired"}`), nil
		case u == codexTokenURL:
			return httpResp(200, `{"access_token":"at-fresh","refresh_token":"rt-fresh"}`), nil
		case u == codexBackendURL && auth == "Bearer at-fresh":
			return httpResp(200, codexSSEFixture), nil
		default:
			t.Fatalf("unexpected call %s auth=%s", u, auth)
			return nil, nil
		}
	})
	text, status, err := codexProxyGenerate(context.Background(), client, dir, "gpt-5.4-mini", "hi")
	if err != nil || status != 200 {
		t.Fatalf("codexProxyGenerate status=%d err=%v; want 200/nil", status, err)
	}
	if text != "Xin chào" {
		t.Fatalf("text = %q; want %q", text, "Xin chào")
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %v; want 3 (responses→token→responses)", calls)
	}
	// auth.json phải đã được ghi token mới (lượt sau dùng at-fresh).
	rt, err := readCodexTokens(dir)
	if err != nil || rt.AccessToken != "at-fresh" {
		t.Fatalf("auth.json access = %q err=%v; want at-fresh (đã ghi lại sau refresh)", rt.AccessToken, err)
	}
}
