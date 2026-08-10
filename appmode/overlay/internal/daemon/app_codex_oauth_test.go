package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCodexPKCEChallengeMatchesVerifier(t *testing.T) {
	verifier, challenge := codexPKCE()
	if len(verifier) < 43 {
		t.Fatalf("verifier len = %d; want >= 43 (RFC 7636)", len(verifier))
	}
	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != want {
		t.Fatalf("challenge = %q; want base64url(sha256(verifier)) = %q", challenge, want)
	}
	// Hai lượt gọi phải khác nhau (ngẫu nhiên).
	v2, _ := codexPKCE()
	if verifier == v2 {
		t.Errorf("codexPKCE lặp verifier — phải ngẫu nhiên")
	}
}

func TestCodexBuildAuthorizeURLCarriesCodexFlags(t *testing.T) {
	raw := codexBuildAuthorizeURL("http://127.0.0.1:20128/auth/callback", "chal-1", "state-1")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize url = %v", err)
	}
	if u.Scheme+"://"+u.Host+u.Path != codexAuthorizeURL {
		t.Fatalf("authorize base = %s; want %s", u.Scheme+"://"+u.Host+u.Path, codexAuthorizeURL)
	}
	q := u.Query()
	checks := map[string]string{
		"response_type":              "code",
		"client_id":                  codexClientID,
		"redirect_uri":               "http://127.0.0.1:20128/auth/callback",
		"scope":                      codexOAuthScope,
		"code_challenge":             "chal-1",
		"code_challenge_method":      "S256",
		"state":                      "state-1",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
	}
	for k, want := range checks {
		if got := q.Get(k); got != want {
			t.Errorf("authorize %s = %q; want %q", k, got, want)
		}
	}
	if !strings.Contains(q.Get("scope"), "offline_access") {
		t.Errorf("scope thiếu offline_access (không có refresh_token)")
	}
}

// fakeJWT dựng một JWT giả (header.payload.sig) với payload là claims — chỉ để test decode, không ký.
func fakeJWT(t *testing.T, claims any) string {
	t.Helper()
	payload, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

func TestCodexAccountIDFromIDToken(t *testing.T) {
	idToken := fakeJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "acct-xyz"},
		"email":                       "x@y.com",
	})
	if got := codexAccountIDFromIDToken(idToken); got != "acct-xyz" {
		t.Fatalf("account_id = %q; want acct-xyz", got)
	}
	// id_token hỏng / thiếu → rỗng, không panic.
	for _, bad := range []string{"", "not-a-jwt", "a.b", fakeJWT(t, map[string]string{"email": "x"})} {
		if got := codexAccountIDFromIDToken(bad); got != "" {
			t.Errorf("account_id(%q) = %q; want rỗng", bad, got)
		}
	}
}

func TestCodexExchangeCode(t *testing.T) {
	idToken := fakeJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "acct-1"},
	})
	client := stubClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != codexTokenURL {
			t.Fatalf("URL = %s; want %s", req.URL, codexTokenURL)
		}
		var m map[string]string
		_ = json.NewDecoder(req.Body).Decode(&m)
		if m["grant_type"] != "authorization_code" || m["code"] != "the-code" ||
			m["code_verifier"] != "the-verifier" || m["client_id"] != codexClientID {
			t.Errorf("exchange body = %v; want authorization_code + code + verifier + client_id", m)
		}
		return httpResp(200, `{"access_token":"at","refresh_token":"rt","id_token":"`+idToken+`"}`), nil
	})
	tok, err := codexExchangeCode(context.Background(), client, "the-code", "the-verifier", "http://127.0.0.1:1/auth/callback")
	if err != nil {
		t.Fatalf("codexExchangeCode = %v; want nil", err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" || tok.AccountID != "acct-1" {
		t.Fatalf("tok = %+v; want at/rt/acct-1 (account_id từ id_token)", tok)
	}
}

// TestStartCodexOAuthLoginRoundTrip ghim toàn luồng loopback: authorize URL đúng → callback với code
// + state → đổi token (stub) → ghi auth.json đọc lại được. openBrowser stub no-op để không bật tab thật.
func TestStartCodexOAuthLoginRoundTrip(t *testing.T) {
	prev := openBrowser
	openBrowser = func(string, *slog.Logger) {}
	t.Cleanup(func() { openBrowser = prev })

	dir := t.TempDir()
	idToken := fakeJWT(t, map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "acct-1"}})
	client := stubClient(func(*http.Request) (*http.Response, error) {
		return httpResp(200, `{"access_token":"at","refresh_token":"rt","id_token":"`+idToken+`"}`), nil
	})
	authURL, wait, err := startCodexOAuthLogin(context.Background(), client, dir, nil)
	if err != nil {
		t.Fatalf("startCodexOAuthLogin = %v", err)
	}
	u, _ := url.Parse(authURL)
	state := u.Query().Get("state")
	redirect := u.Query().Get("redirect_uri")
	if state == "" || redirect == "" {
		t.Fatalf("authorize thiếu state/redirect_uri: %s", authURL)
	}
	// Giả trình duyệt gọi callback với code + state đúng. Server bind 127.0.0.1 nên gọi thẳng địa chỉ
	// đó (redirect quảng bá "localhost" — tránh phân giải ::1 làm test flaky).
	cb := strings.Replace(redirect, "localhost", "127.0.0.1", 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		if resp, err := http.Get(cb + "?state=" + url.QueryEscape(state) + "&code=the-code"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	if err := wait(); err != nil {
		t.Fatalf("wait = %v; want nil (callback → exchange → write auth.json)", err)
	}
	tok, err := readCodexTokens(dir)
	if err != nil || tok.AccessToken != "at" || tok.AccountID != "acct-1" {
		t.Fatalf("auth.json sau connect = %+v err=%v; want at/acct-1", tok, err)
	}
}

func TestCreateCodexAuthFileReadableBack(t *testing.T) {
	dir := t.TempDir()
	err := createCodexAuthFile(dir, codexTokens{AccessToken: "at-new", RefreshToken: "rt-new", IDToken: "id", AccountID: "acct-9"})
	if err != nil {
		t.Fatalf("createCodexAuthFile = %v", err)
	}
	tok, err := readCodexTokens(dir)
	if err != nil {
		t.Fatalf("readCodexTokens after create = %v", err)
	}
	if tok.AccessToken != "at-new" || tok.AccountID != "acct-9" {
		t.Fatalf("read back = %+v; want at-new/acct-9", tok)
	}
}
