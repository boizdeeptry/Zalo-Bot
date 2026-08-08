package daemon

// Codex browser-OAuth connect (thay device-auth): tự chạy luồng authorization_code + PKCE tới
// auth.openai.com, giống 9Router — mở trang đăng nhập ChatGPT, bắt code qua localhost, đổi lấy token.
// KHÔNG cần device-auth, KHÔNG cần codex CLI login. Tham số ghim từ codex.exe (capture 2026-08-08).

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	codexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	// scope có offline_access để nhận refresh_token; hai scope connectors đi kèm luồng codex thật.
	codexOAuthScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	// đường callback loopback — trùng codex CLI (login\src\server.rs). Port động, chọn lúc chạy.
	codexRedirectPath = "/auth/callback"
)

// codexPKCE sinh cặp PKCE (RFC 7636): verifier ngẫu nhiên (crypto/rand, base64url 43 ký tự — trong
// khoảng 43..128 mà codex.exe assert) và challenge = base64url(sha256(verifier)), method S256.
func codexPKCE() (verifier, challenge string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	verifier = base64.RawURLEncoding.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// codexOAuthState sinh state chống CSRF cho lượt authorize.
func codexOAuthState() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// codexBuildAuthorizeURL dựng URL authorize. HAI cờ riêng của codex — id_token_add_organizations +
// codex_cli_simplified_flow — ghim từ binary; thiếu là luồng đăng nhập codex không đúng (trang lạ /
// thiếu account trong id_token). redirectURI là loopback http://127.0.0.1:<port>/auth/callback.
func codexBuildAuthorizeURL(redirectURI, challenge, state string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", codexClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", codexOAuthScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", "codex_cli_rs")
	return codexAuthorizeURL + "?" + q.Encode()
}

// codexExchangeCode đổi authorization code lấy token (grant_type=authorization_code + PKCE verifier).
// account_id lấy từ claim của id_token. redirectURI PHẢI trùng cái đã gửi lúc authorize (OpenAI khớp).
func codexExchangeCode(ctx context.Context, client *http.Client, code, verifier, redirectURI string) (codexTokens, error) {
	if code == "" || verifier == "" {
		return codexTokens{}, errors.New("thiếu code hoặc code_verifier")
	}
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"code_verifier": verifier,
		"client_id":     codexClientID,
		"redirect_uri":  redirectURI,
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
		return codexTokens{}, fmt.Errorf("oauth/token (authorization_code) trả %d", resp.StatusCode)
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
	return codexTokens{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		IDToken:      r.IDToken,
		AccountID:    codexAccountIDFromIDToken(r.IDToken),
	}, nil
}

// codexAccountIDFromIDToken đọc chatgpt_account_id từ claim "https://api.openai.com/auth" của id_token
// (JWT). CHỈ giải mã phần payload để lấy account_id — KHÔNG xác thực chữ ký và KHÔNG tin cậy bảo mật
// vào nó (bảo mật nằm ở access_token). Rỗng nếu id_token trống/hỏng/thiếu claim.
func codexAccountIDFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Auth.ChatGPTAccountID
}

// createCodexAuthFile ghi MỚI auth.json cho một account vừa đăng nhập OAuth (đúng shape codex:
// {auth_mode:"chatgpt", tokens:{...}, last_refresh}). Nguyên tử qua .tmp + rename. Dùng khi connect
// (chưa có file); refresh về sau đi qua writeCodexTokens (giữ trường cũ).
func createCodexAuthFile(configDir string, tok codexTokens) error {
	if configDir == "" {
		return errors.New("configDir rỗng")
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("tạo CODEX_HOME: %w", err)
	}
	file := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]string{
			"access_token":  tok.AccessToken,
			"refresh_token": tok.RefreshToken,
			"id_token":      tok.IDToken,
			"account_id":    tok.AccountID,
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	}
	out, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	path := codexAuthPath(configDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("ghi auth.json.tmp: %w", err)
	}
	return os.Rename(tmp, path)
}
