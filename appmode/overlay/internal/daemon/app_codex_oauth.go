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
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	codexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	// scope có offline_access để nhận refresh_token; hai scope connectors đi kèm luồng codex thật.
	codexOAuthScope = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	// đường callback loopback — trùng codex CLI (login\src\server.rs).
	codexRedirectPath = "/auth/callback"
)

// codexLoopbackPorts: cổng codex ĐÃ ĐĂNG KÝ với OpenAI (ưu tiên 1455, dự phòng 1457) — Hydra chỉ nhận
// redirect_uri đúng cổng này, không phải cổng ngẫu nhiên. Trùng preferred/fallback của codex CLI.
var codexLoopbackPorts = []int{1455, 1457}

// bindCodexLoopback mở listener loopback ở một trong các cổng đã đăng ký. Bind 127.0.0.1 (localhost
// phân giải về đây trên Windows); redirect_uri quảng bá "localhost". Cả hai cổng bận → lỗi rõ ràng.
func bindCodexLoopback() (net.Listener, error) {
	for _, port := range codexLoopbackPorts {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			return ln, nil
		}
	}
	return nil, fmt.Errorf("cổng đăng nhập codex %v đang bận — đóng phiên đăng nhập codex khác rồi thử lại", codexLoopbackPorts)
}

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

// openBrowser mở URL bằng trình duyệt mặc định (Windows). Là biến để test thay bằng no-op — không thì
// mỗi lần chạy test lại bật một tab thật. Best-effort: thất bại thì user tự bấm URL hiện ở Portal.
var openBrowser = func(rawURL string, logger *slog.Logger) {
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start(); err != nil && logger != nil {
		logger.Debug("không mở được trình duyệt cho OAuth codex", "err", err)
	}
}

// startCodexOAuthLogin khởi động luồng đăng nhập OAuth trình duyệt cho một account codex: mở cổng
// loopback, dựng URL authorize, mở trình duyệt tới trang đăng nhập ChatGPT, và trả (authorizeURL,
// wait). wait() CHẶN tới khi callback về (hoặc ctx huỷ), đổi code → token → ghi auth.json. Không
// spawn process nào (khác device-auth) nên không cần killPidTree — chỉ đóng server ở cuối.
//
// Trùng hình dạng loginClaude để state machine dùng y hệt: trả URL (không có device code), wait()
// chặn tới khi xong; pollAuth đọc auth.json để biết đã đăng nhập.
func startCodexOAuthLogin(ctx context.Context, client *http.Client, configDir string, logger *slog.Logger) (string, func() error, error) {
	verifier, challenge := codexPKCE()
	state := codexOAuthState()
	ln, err := bindCodexLoopback()
	if err != nil {
		return "", nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	// PHẢI là host "localhost" (KHÔNG phải 127.0.0.1) + cổng đã đăng ký (1455/1457): OpenAI Hydra khớp
	// redirect_uri của client codex chính xác theo host+port đó, sai host trả authorize_hydra_invalid_request.
	redirectURI := fmt.Sprintf("http://localhost:%d%s", port, codexRedirectPath)
	authorizeURL := codexBuildAuthorizeURL(redirectURI, challenge, state)

	codeCh := make(chan string, 1)
	oauthErrCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(codexRedirectPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state { // chống CSRF: state phải khớp cái đã gửi
			http.Error(w, "state mismatch", http.StatusBadRequest)
			trySend(oauthErrCh, errors.New("state không khớp (nghi CSRF)"))
			return
		}
		if e := q.Get("error"); e != "" {
			writeHTML(w, "<p>Đăng nhập bị từ chối. Có thể đóng tab này.</p>")
			trySend(oauthErrCh, fmt.Errorf("authorize bị từ chối: %s", e))
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "thiếu code", http.StatusBadRequest)
			return
		}
		writeHTML(w, "<h3>Đã kết nối Codex.</h3><p>Có thể đóng tab này và quay lại phần mềm.</p>")
		trySend(codeCh, code)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }() // thoát khi srv.Shutdown

	openBrowser(authorizeURL, logger)

	wait := func() error {
		defer func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
		}()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-oauthErrCh:
			return err
		case code := <-codeCh:
			tok, err := codexExchangeCode(ctx, client, code, verifier, redirectURI)
			if err != nil {
				return fmt.Errorf("đổi code lấy token: %w", err)
			}
			if err := createCodexAuthFile(configDir, tok); err != nil {
				return fmt.Errorf("ghi auth.json: %w", err)
			}
			return nil
		}
	}
	return authorizeURL, wait, nil
}

func trySend[T any](ch chan<- T, v T) {
	select {
	case ch <- v:
	default:
	}
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}
