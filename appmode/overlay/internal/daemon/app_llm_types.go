package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"

	"agentdc/internal/store"
)

// llmRequest là một lượt gọi đã chuẩn hoá về dạng nhỏ nhất mà mọi Provider đều nói được.
//
// Chỉ prompt và model, KHÔNG phải một cuộc hội thoại: bốn API dưới đây có bốn cách biểu diễn
// lịch sử hội thoại khác nhau, và dịch xuôi ngược giữa chúng là lớp mà thiết kế cố ý không mang
// về (xem .planning/research/9router-RESEARCH.md §5). Router ghép lịch sử thành prompt trước.
type llmRequest struct {
	Model  string
	Prompt string
}

// llmResponse là phần duy nhất của phản hồi mà lượt trả lời cần: văn bản.
type llmResponse struct {
	Text string
}

// llmErrorKind phân loại một lần gọi hỏng theo VIỆC PHẢI LÀM, không theo mã lỗi.
//
// Đây là thứ quyết định chuỗi fallback đi tiếp hay dừng, nên nó chặt hơn nguồn tham khảo
// (9Router không công bố taxonomy nào) và không được nới ra cho giống: credential sai, model
// không tồn tại, request sai và nội dung bị từ chối đều là những thứ đổi Provider không chữa
// được — riêng cái cuối còn là hành vi ta chủ động không muốn có.
type llmErrorKind string

const (
	llmErrorNetwork    llmErrorKind = "network"
	llmErrorTimeout    llmErrorKind = "timeout"
	llmErrorRateLimit  llmErrorKind = "rate_limit"
	llmErrorUpstream   llmErrorKind = "upstream"
	llmErrorCredential llmErrorKind = "credential"
	llmErrorModel      llmErrorKind = "model"
	llmErrorRequest    llmErrorKind = "request"
	llmErrorPolicy     llmErrorKind = "policy"
	// llmErrorCanceled là loại thứ CHÍN, ngoài tám loại spec liệt kê.
	//
	// Thêm vào vì taxonomy cũ không có ngăn nào cho việc huỷ, và xếp nó vào "network" là sai
	// theo đúng định nghĩa ngay dưới đây: ctx chết rồi thì gửi cùng request đi đâu cũng chết.
	// Không có ngăn riêng thì một lượt bị huỷ (tắt daemon, hết hạn mức lượt) sẽ đi hết phần
	// còn lại của chuỗi rồi kết thúc bằng một lỗi đổ tội cho các Provider.
	llmErrorCanceled llmErrorKind = "canceled"
)

// isFallbackEligible nói một lỗi có đáng thử Provider kế tiếp hay không.
//
// Đúng bốn loại: bốn thứ mà cùng một request gửi tới một nơi khác có thể thành công. Mọi loại
// còn lại thử lại vẫn hỏng y hệt, nên đi tiếp chỉ nhân số lần chờ của khách lên.
func isFallbackEligible(kind llmErrorKind) bool {
	switch kind {
	case llmErrorNetwork, llmErrorTimeout, llmErrorRateLimit, llmErrorUpstream:
		return true
	default:
		return false
	}
}

// providerAdapter là những gì router cần ở một API Provider, và không hơn.
//
// credential là THAM SỐ của từng lượt gọi chứ không phải trường của struct: adapter sống suốt
// đời daemon còn khoá thì được giải mã ngay trước khi dùng và rơi đi ngay sau đó, nên cất nó
// vào struct là kéo dài thời gian bản rõ nằm trong bộ nhớ mà không ai còn nhớ tại sao.
type providerAdapter interface {
	Generate(ctx context.Context, req llmRequest, credential []byte) (llmResponse, error)
	Test(ctx context.Context, model string, credential []byte) error
	Discover(ctx context.Context, credential []byte) ([]store.LLMModel, error)
}

// llmError là một lần gọi Provider hỏng, kèm phân loại để router quyết định đi tiếp hay dừng.
//
// Thông báo được che NGAY LÚC DỰNG chứ không lúc in ra: một lỗi đi qua nhiều lớp bọc, và lớp
// nào quên gọi hàm che thì lớp đó là chỗ khoá rò ra.
//
// KHÔNG giữ lỗi gốc và KHÔNG có Unwrap: lỗi của net/http mang nguyên URL đã gọi, nên một
// Unwrap() là một đường vòng qua lượt che — người gọi lấy được bản chưa che mà không biết. Chữ
// nghĩa cần chẩn đoán đã nằm trong msg (đã che), còn câu hỏi duy nhất mà router hỏi tiếp là
// "loại gì", và Kind trả lời nó mà không cần chuỗi bọc.
type llmError struct {
	Kind llmErrorKind
	msg  string
}

func (e *llmError) Error() string { return e.msg }

func newLLMError(kind llmErrorKind, cause error, format string, args ...any) *llmError {
	msg := fmt.Sprintf(format, args...)
	if cause != nil {
		msg += ": " + cause.Error()
	}
	return &llmError{Kind: kind, msg: sanitizeProviderError(msg)}
}

// transportError phân loại lỗi trước khi có phản hồi: quá hạn là timeout, còn lại là network.
//
// Xét cả net.Error.Timeout() chứ không chỉ context.DeadlineExceeded vì hai nguồn hết giờ khác
// nhau — deadline của ctx và http.Client.Timeout — và chúng không cùng một lỗi gốc.
func transportError(op string, err error) *llmError {
	// Huỷ xét TRƯỚC, vì context.Canceled không phải DeadlineExceeded và cũng không phải net.Error
	// có Timeout() — nó sẽ rơi thẳng vào "network", loại ĐƯỢC fallback. Khi đó một lượt bị huỷ
	// đi hết chuỗi, gọi từng Provider còn lại bằng một ctx đã chết, rồi báo lỗi như thể họ hỏng.
	if errors.Is(err, context.Canceled) {
		return newLLMError(llmErrorCanceled, err, "%s: lượt gọi bị huỷ", op)
	}
	kind := llmErrorNetwork
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		kind = llmErrorTimeout
	}
	return newLLMError(kind, err, "%s: không gọi được Provider", op)
}
