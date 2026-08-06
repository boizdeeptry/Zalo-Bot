package daemon

import (
	"errors"
	"regexp"
)

// ErrCredentialUnreadable nói rằng ciphertext vẫn còn đó nhưng không mở ra được nữa.
//
// Là lỗi riêng vì nó KHÔNG phải một hỏng hóc tạm thời: DPAPI cố ý từ chối khi người dùng
// Windows đã khác, khi máy đã cài lại, hoặc khi hàng trong database bị sửa tay. Cả ba đều có
// đúng một lối ra là nhập lại API key, nên tầng HTTP phải phân biệt được nó với lỗi mạng —
// hiện "thử lại sau" cho một khoá không bao giờ mở được nữa là bắt người dùng chờ vô hạn.
var ErrCredentialUnreadable = errors.New("provider credential unreadable")

// ErrCredentialUnsupported chặn đường ghi khoá trên hệ điều hành không có DPAPI.
//
// Fail-closed chứ không rơi về lưu thô: bản build non-Windows vẫn phải compile (xem
// app_secret_other.go), và cách hỏng êm nhất ở đó là nhận khoá rồi cất nguyên văn xuống SQLite
// mà không ai nhận ra. Từ chối ồn ào thì người mang sang nền tảng khác biết ngay còn thiếu gì.
var ErrCredentialUnsupported = errors.New("provider credential protection unsupported")

// providerSecretMask là thứ thay chỗ credential. Cố định để log grep được, và không mang theo
// độ dài hay vài ký tự đầu của khoá — "sk-abc…" đủ để thu hẹp không gian tìm kiếm.
const providerSecretMask = "[redacted]"

// providerSecretRedactions là các hình dạng credential bị xoá khỏi mọi thông báo lỗi Provider.
//
// Mỗi mẫu giữ NHÓM 1 làm nhãn và nuốt phần giá trị, nên chuỗi sau khi che vẫn nói được là đã
// che cái gì. Thứ tự có ý nghĩa: mẫu bám nhãn chạy trước, mẫu đoán theo tiền tố chạy cuối để
// chỉ dọn phần các mẫu trên không với tới.
var providerSecretRedactions = []*regexp.Regexp{
	// Authorization/Proxy-Authorization, cả dạng header "Tên: giá trị" lẫn dạng JSON
	// "tên":"giá trị". Nuốt tới hết dòng vì scheme nào cũng có thể đứng sau (Bearer, Basic,
	// một token trần), và liệt kê scheme là cách bỏ sót cái chưa gặp.
	regexp.MustCompile(`(?i)\b((?:proxy-)?authorization"?\s*[:=]\s*)[^\r\n;,]*`),
	// Bearer đứng một mình, sau khi tên header đã rụng trên đường đi qua log và các lớp bọc lỗi.
	regexp.MustCompile(`(?i)\b(bearer\s+)[^\s"',;]+`),
	// x-api-key / api-key / api_key / apiKey — Anthropic và phần lớn gateway tương thích OpenAI.
	regexp.MustCompile(`(?i)\b((?:x-)?api[-_]?key"?\s*[:=]\s*)[^\r\n;,]*`),
	// key= trong query string: Gemini nhận khoá qua URL, nên MỌI thông báo lỗi có kèm URL đều
	// mang khoá theo — kể cả câu lỗi do net/http tự sinh, nơi không có header nào để bám vào.
	regexp.MustCompile(`(?i)([?&](?:api[-_]?)?key=)[^&\s"'#]*`),
	// Trường JSON có tên tận cùng bằng key/token/secret: access_token, client_secret, api_key…
	// Lớp [^"\r\n] hai bên giữ cho mẫu không nhảy qua ranh giới trường và nuốt cả object.
	regexp.MustCompile(`(?i)("[^"\r\n]*(?:key|token|secret)"\s*:\s*)("[^"\r\n]*"|[^\s,}\]]+)`),
	// Khoá lộ nguyên văn giữa câu văn. Provider hay chép lại chính khoá sai vào thông báo
	// ("Incorrect API key provided: sk-…"), và ở đó không còn nhãn nào để bám — chỉ còn tiền tố
	// của từng nhà cung cấp. Giữ lại tiền tố để câu lỗi vẫn cho biết đó là khoá của ai.
	regexp.MustCompile(`(?i)\b(sk-ant-|sk-|xai-|gsk_|AIza)[A-Za-z0-9_-]{8,}`),
}

// sanitizeProviderError xoá credential khỏi một thông báo lỗi trước khi nó được log hay hiện ra.
//
// Là cửa DUY NHẤT cho việc đó: mọi lỗi gọi Provider đi qua đây, vì một đường thứ hai là một
// đường sẽ có ngày quên che. Người gọi KHÔNG được đưa body trả về của Provider vào đây — body
// là câu chữ tự do, không luật nào phủ hết được, nên nó phải dừng lại ở tầng gọi API chứ không
// phải được hy vọng là hàm này dọn hộ.
func sanitizeProviderError(msg string) string {
	for _, r := range providerSecretRedactions {
		msg = r.ReplaceAllString(msg, "${1}"+providerSecretMask)
	}
	return msg
}
