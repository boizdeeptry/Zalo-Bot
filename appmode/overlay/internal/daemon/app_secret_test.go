package daemon

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// requireCredentialProtection bỏ qua test khi bản build hiện tại không có kho khoá nào.
//
// Chỉ hai test round-trip cần nó: phải mã hoá được thì mới có gì để giải mã. Hợp đồng của
// nhánh còn lại do TestProtectProviderSecretFailsClosed giữ, và test đó chạy ở mọi GOOS —
// nên bỏ qua ở đây không để lại lỗ nào.
func requireCredentialProtection(t *testing.T) {
	t.Helper()
	if _, err := protectProviderSecret([]byte("probe")); errors.Is(err, ErrCredentialUnsupported) {
		t.Skipf("bản build này không bảo vệ được credential: %v", err)
	}
}

func TestProviderSecretRoundTripAndRedaction(t *testing.T) {
	requireCredentialProtection(t)

	key := []byte("sk-provider-test-secret")
	encrypted, err := protectProviderSecret(key)
	if err != nil {
		t.Fatalf("protectProviderSecret(%q) error = %v; want nil", key, err)
	}
	if bytes.Contains(encrypted, key) {
		t.Fatalf("protectProviderSecret(%q) trả ciphertext chứa nguyên bản rõ", key)
	}
	plain, err := unprotectProviderSecret(encrypted)
	if err != nil {
		t.Fatalf("unprotectProviderSecret(ciphertext của %q) error = %v; want nil", key, err)
	}
	if !bytes.Equal(plain, key) {
		t.Fatalf("round trip %q = %q; want %q", key, plain, key)
	}
}

func TestUnprotectProviderSecretRejectsUnreadableCiphertext(t *testing.T) {
	requireCredentialProtection(t)

	valid, err := protectProviderSecret([]byte("sk-corruption-probe"))
	if err != nil {
		t.Fatalf("protectProviderSecret() error = %v; want nil", err)
	}
	corrupt := bytes.Clone(valid)
	corrupt[len(corrupt)/2] ^= 0xFF

	tests := []struct {
		name   string
		cipher []byte
	}{
		{"rỗng", nil},
		{"không phải blob DPAPI", []byte("not a dpapi blob at all")},
		{"blob thật bị lật một byte", corrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain, err := unprotectProviderSecret(tt.cipher)
			if !errors.Is(err, ErrCredentialUnreadable) {
				t.Fatalf("unprotectProviderSecret(%s) error = %v; want ErrCredentialUnreadable", tt.name, err)
			}
			if len(plain) != 0 {
				t.Fatalf("unprotectProviderSecret(%s) trả %d byte bản rõ; want 0", tt.name, len(plain))
			}
		})
	}
}

// TestProtectProviderSecretFailsClosed khoá cửa "cất bản rõ khi không mã hoá được".
//
// Viết theo cách chạy được trên MỌI hệ điều hành, vì đó là cách duy nhất một test ở đây nói
// được điều gì về nhánh non-Windows: một tệp test gắn thẻ !windows không bao giờ được biên dịch
// trong bản build này và chứng minh đúng con số không. Ở đây nhánh nào compile vào thì nhánh đó
// phải thoả cùng một hợp đồng — hoặc ciphertext không chứa bản rõ, hoặc lỗi và không có gì cả.
func TestProtectProviderSecretFailsClosed(t *testing.T) {
	key := []byte("sk-fail-closed-probe")
	cipher, err := protectProviderSecret(key)
	if err != nil {
		if !errors.Is(err, ErrCredentialUnsupported) {
			t.Fatalf("protectProviderSecret() error = %v; want nil hoặc ErrCredentialUnsupported", err)
		}
		if len(cipher) != 0 {
			t.Fatalf("protectProviderSecret() trả %d byte kèm lỗi; want 0", len(cipher))
		}
		return
	}
	if bytes.Contains(cipher, key) {
		t.Fatalf("protectProviderSecret() trả ciphertext chứa nguyên bản rõ")
	}
}

func TestSanitizeProviderErrorRedactsCredentialShapes(t *testing.T) {
	// Hai canary khác nhau là cố ý: plainCanary không mang tiền tố nhà cung cấp nào, nên các ca
	// header/JSON dưới đây chỉ xanh khi chính luật bám nhãn của chúng chạy — luật tiền tố không
	// đỡ hộ được. prefixCanary dành cho ca duy nhất không có nhãn nào để bám.
	const (
		plainCanary  = "Ab3xQ9zK7mP2wR5t"
		prefixCanary = "sk-canary-Ab3xQ9zK7mP2wR5t"
	)
	// So khớp ĐÚNG BẰNG chứ không chỉ "không còn canary": một hàm trả thẳng "REDACTED" cũng qua
	// được phép thử vắng mặt, và nhóm 1 — phần giữ lại cái nhãn — sẽ không có ai canh. Cụ thể,
	// lỗi "[redacted]" tự ăn lại đầu ra của chính mình đã lọt qua đúng vì kiểu assert đó.
	tests := []struct {
		name, in, want string
	}{
		{"header Authorization Bearer", "Authorization: Bearer " + plainCanary, "Authorization: REDACTED"},
		{"header viết thường", "authorization: bearer " + plainCanary, "authorization: REDACTED"},
		{"Bearer rụng mất tên header", "401 unauthorized (Bearer " + plainCanary + ")", "401 unauthorized (Bearer REDACTED)"},
		{"header x-api-key", "x-api-key: " + plainCanary, "x-api-key: REDACTED"},
		{"header api-key", "api-key: " + plainCanary, "api-key: REDACTED"},
		{
			"giữ lại phần chẩn đoán sau giá trị",
			"x-api-key: " + plainCanary + " (request-id req_011CabcXYZ) status=401",
			"x-api-key: REDACTED (request-id req_011CabcXYZ) status=401",
		},
		{
			"nhiều dòng header",
			"POST /v1/messages\r\nx-api-key: " + plainCanary + "\r\nContent-Type: application/json",
			"POST /v1/messages\r\nx-api-key: REDACTED\r\nContent-Type: application/json",
		},
		{"json api_key", `{"api_key":"` + plainCanary + `"}`, `{"api_key":REDACTED}`},
		{"json access_token", `{"access_token":"` + plainCanary + `"}`, `{"access_token":REDACTED}`},
		{"json client_secret", `{"client_secret":"` + plainCanary + `"}`, `{"client_secret":REDACTED}`},
		{"json key", `{"key":"` + plainCanary + `"}`, `{"key":REDACTED}`},
		{
			"query string kiểu Gemini",
			`Post "https://generativelanguage.googleapis.com/v1beta/models:generateContent?key=` + plainCanary + `": 400`,
			`Post "https://generativelanguage.googleapis.com/v1beta/models:generateContent?key=REDACTED": 400`,
		},
		{
			"khoá lọt vào câu văn lỗi của Provider",
			`{"error":{"message":"Incorrect API key provided: ` + prefixCanary + `. Check your key.","code":"invalid_api_key"}}`,
			`{"error":{"message":"Incorrect API key provided: sk-REDACTED. Check your key.","code":"invalid_api_key"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeProviderError(tt.in)
			if got != tt.want {
				t.Fatalf("sanitizeProviderError(%q) = %q; want %q", tt.in, got, tt.want)
			}
			for _, canary := range []string{plainCanary, prefixCanary} {
				if strings.Contains(got, canary) {
					t.Fatalf("sanitizeProviderError(%q) = %q; credential %q vẫn còn", tt.in, got, canary)
				}
			}
			// Che hai lần phải ra đúng chuỗi cũ: router bọc lỗi rồi tầng HTTP che lần nữa là
			// đường đi bình thường, không phải trường hợp hiếm.
			if again := sanitizeProviderError(got); again != got {
				t.Fatalf("sanitizeProviderError(sanitizeProviderError(%q)) = %q; want %q", tt.in, again, got)
			}
		})
	}
}

// sanitizeProviderError phải giữ lại phần chẩn đoán được. Một hàm che sạch mọi thứ thì an toàn
// nhưng vô dụng, và người vận hành sẽ vòng qua nó bằng cách log chuỗi gốc ở một chỗ khác.
func TestSanitizeProviderErrorKeepsDiagnosticText(t *testing.T) {
	const in = "429 rate limit: model claude-sonnet-4-5-20250929, retry after 30s, tokens_used 1200"
	if got := sanitizeProviderError(in); got != in {
		t.Fatalf("sanitizeProviderError(%q) = %q; want giữ nguyên", in, got)
	}
}
