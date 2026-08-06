package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"agentdc/internal/store"
)

// Hạn giờ mặc định của một lượt định tuyến.
//
// Hai mức chứ không một, vì chúng chặn hai kiểu hỏng khác nhau: một Provider treo (25s) và một
// chuỗi fallback dài lê thê cộng dồn thời gian chờ của khách (60s). Chỉ có mức thứ hai thì bốn
// mục hỏng nối nhau vẫn kịp đốt hết 60 giây trước khi Claude Code được nhắc tới; chỉ có mức thứ
// nhất thì cùng chuỗi đó thành 100 giây.
//
// Claude Code KHÔNG nằm trong ngân sách nào ở đây — nó giữ hạn mức lượt sẵn có (zaloTurnBudget).
const (
	defaultLLMProviderTimeout = 25 * time.Second
	defaultLLMChainTimeout    = 60 * time.Second
)

// llmRouteStore là phần store mà router dùng, và không hơn.
//
// Khai ở phía NGƯỜI DÙNG chứ không phải phía store: router chỉ đọc chuỗi và ghi telemetry, nên
// một seam hai hàm ở đây vừa đủ để test dựng được những tình huống database thật không dựng nổi
// — một snapshot đổi giữa lượt, một lần ghi telemetry hỏng.
type llmRouteStore interface {
	LLMRoute() (store.LLMRouteSnapshot, error)
	RecordLLMAttempt(store.LLMAttempt) error
}

var _ llmRouteStore = (*store.Store)(nil)

// appLLMRunnerConfig là toàn bộ dây nối của một lượt định tuyến.
type appLLMRunnerConfig struct {
	Store    llmRouteStore
	Adapters map[string]providerAdapter
	// Claude là mắt xích cuối, luôn có mặt: validateLLMRoute không cho lưu một chuỗi kết thúc
	// bằng thứ khác, nên router không cần một nhánh "nếu không còn gì".
	Claude zaloRunner
	// Credential trả về BẢN RÕ MỚI cho mỗi lượt gọi. Router xoá nó ngay sau lượt gọi, nên một
	// hiện thực trả về lát cắt dùng chung sẽ tự bắn vào chân mình ở lượt sau.
	Credential func(providerID string) ([]byte, error)
	// HasAttachments là ranh giới an toàn, không phải một tuỳ chọn: lượt có tệp hoặc ảnh chỉ
	// được đi tới Claude Code, nơi tệp nằm im trên đĩa máy này.
	HasAttachments     bool
	PerProviderTimeout time.Duration
	APIChainTimeout    time.Duration
	Logger             *slog.Logger
}

// appLLMRunner chạy một lượt trả lời qua chuỗi fallback đã lưu.
type appLLMRunner struct {
	cfg appLLMRunnerConfig
}

var _ zaloRunner = (*appLLMRunner)(nil)

func newAppLLMRunner(cfg appLLMRunnerConfig) *appLLMRunner {
	if cfg.PerProviderTimeout <= 0 {
		cfg.PerProviderTimeout = defaultLLMProviderTimeout
	}
	if cfg.APIChainTimeout <= 0 {
		cfg.APIChainTimeout = defaultLLMChainTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &appLLMRunner{cfg: cfg}
}

// Run trả lời một lượt: đọc chuỗi fallback đúng MỘT lần, rồi đi theo nó.
//
// Snapshot được sao ra ngay tại đây và không đọc lại. Một lần lưu route giữa chừng vì thế không
// đổi đường đi của lượt đang chạy — nếu đọc lại giữa chuỗi thì cùng một tin nhắn có thể gọi nửa
// đầu theo cấu hình cũ và nửa sau theo cấu hình mới, và không log nào giải thích được vì sao.
func (r *appLLMRunner) Run(ctx context.Context, prompt string, step func(string)) (string, error) {
	snapshot, err := r.cfg.Store.LLMRoute()
	if err != nil {
		return "", fmt.Errorf("llm route: đọc chuỗi fallback: %w", err)
	}
	// Clone vì Entries là lát cắt của người khác: store thật dựng mới mỗi lần gọi, nhưng đó là
	// lời hứa của store chứ không phải của router, và router là nơi tính bất biến phải đúng.
	entries := slices.Clone(snapshot.Entries)
	if len(entries) == 0 {
		return "", errors.New("llm route: chuỗi fallback rỗng, không có gì để gọi")
	}
	// Mắt xích cuối LUÔN là Claude Code đang bật (validateLLMRoute giữ điều đó), nên phần API
	// là mọi mục còn lại. Tách trước nhánh dưới đây để cửa chặn lượt có tệp là ĐÚNG MỘT câu
	// return đứng trước vòng lặp, thay vì một câu if phải nhớ lặp lại trong thân vòng lặp.
	claude, apiEntries := entries[len(entries)-1], entries[:len(entries)-1]
	if r.cfg.HasAttachments {
		return r.runClaude(ctx, claude, prompt, step)
	}

	apiCtx, cancel := context.WithTimeout(ctx, r.cfg.APIChainTimeout)
	defer cancel()
	for i, e := range apiEntries {
		if !e.Enabled {
			continue
		}
		// Hai lỗi TRƯỚC lượt gọi, và cả hai đều dừng chuỗi mà không sinh telemetry: sổ vận hành
		// đếm những lần thật sự chạm tới Provider, nên một hàng "hỏng" cho một request chưa hề
		// rời khỏi máy này làm người đọc kết luận sai về nhà cung cấp.
		adapter, ok := r.cfg.Adapters[e.ProviderID]
		if !ok {
			// Không im lặng nhảy cóc: chuỗi này do người dùng dựng, và một mục không gọi được
			// mà bị bỏ qua sẽ nằm trong Portal như đang phục vụ trong khi nó chưa từng chạy.
			return "", newLLMError(llmErrorRequest, nil,
				"llm route: không có adapter cho Provider %s", e.ProviderID)
		}
		credential, err := r.cfg.Credential(e.ProviderID)
		if err != nil {
			return "", fmt.Errorf("llm route: mở khoá của %s: %w", e.ProviderID, err)
		}

		started := time.Now()
		resp, err := r.generate(apiCtx, adapter, e, prompt, credential)
		elapsed := time.Since(started)
		if err == nil {
			r.record(llmOKAttempt(e, started, elapsed))
			return resp.Text, nil
		}
		kind := llmErrorKindOf(err)
		// Lượt chết KHÔNG phải lỗi của Provider, nên nó không vào sổ: một hàng "error" ở đây làm
		// LLMStatus báo hỏng cho một Provider chỉ mắc tội chậm hơn phần thời gian còn lại.
		//
		// Xét ctx cha chứ không chỉ xét kind, vì hai thứ đó KHÔNG trùng nhau. Ở bản chạy, lượt
		// dựng ctx bằng context.WithTimeout(context.Background(), zaloTurnBudget) và tắt daemon
		// không huỷ nó (xem triggerZaloDuty), nên cách một lượt chết thật là HẾT HẠN — tức
		// DeadlineExceeded, mà transportError xếp vào "timeout": được fallback, và được ghi.
		// Chỉ xét llmErrorCanceled thì nhánh này canh một cửa mà production không đi qua.
		if kind == llmErrorCanceled || ctx.Err() != nil {
			return "", err
		}
		if !isFallbackEligible(kind) {
			r.record(llmErrorAttempt(e, started, elapsed, kind, ""))
			return "", err
		}
		// Ngân sách cả chuỗi cạn thì phần API dừng tại đây và lượt đi thẳng tới Claude Code:
		// gọi tiếp bằng một ctx đã chết chỉ sinh thêm hàng telemetry đổ tội cho Provider sau.
		if apiCtx.Err() != nil {
			r.record(llmErrorAttempt(e, started, elapsed, kind, claude.ProviderID))
			break
		}
		r.record(llmErrorAttempt(e, started, elapsed, kind, nextEnabledProvider(entries, i+1)))
	}
	return r.runClaude(ctx, claude, prompt, step)
}

// generate giữ bản rõ sống đúng bằng một lượt gọi.
//
// defer clear đứng cạnh lời gọi duy nhất truyền bản rõ đi, nên không nhánh trả về nào lách qua
// được — kể cả panic từ trong adapter. Lát cắt này là chính lát cắt adapter nhận, nên xoá ở đây
// xoá luôn thứ adapter còn cầm; một adapter cất bản rõ vào struct của nó sẽ chỉ còn số 0.
func (r *appLLMRunner) generate(ctx context.Context, adapter providerAdapter,
	e store.LLMRouteEntry, prompt string, credential []byte) (llmResponse, error) {
	defer clear(credential)
	callCtx, cancel := context.WithTimeout(ctx, r.cfg.PerProviderTimeout)
	defer cancel()
	return adapter.Generate(callCtx, llmRequest{Model: e.ModelID, Prompt: prompt}, credential)
}

// runClaude giao lượt cho mắt xích cuối, bằng ctx GỐC chứ không phải ctx của chuỗi API.
//
// Vì đó là hai ngân sách khác nhau: 60 giây kia là trần cho việc thử các API, còn một lượt
// Claude Code đo được 42–114 giây và vẫn là một lượt bình thường. Dùng chung ctx thì mắt xích
// cuối chết ngay khi được gọi tới, đúng lúc nó là thứ duy nhất còn lại.
//
// step gốc chỉ đi tới ĐÂY: adapter API không có chỗ nhận nó, và terminal Runtime cần từng bước
// của chính lượt dài này.
func (r *appLLMRunner) runClaude(ctx context.Context, e store.LLMRouteEntry,
	prompt string, step func(string)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("llm route: lượt bị huỷ trước khi tới %s: %w", e.ProviderID, err)
	}
	started := time.Now()
	text, err := r.cfg.Claude.Run(ctx, prompt, step)
	elapsed := time.Since(started)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("llm route: lượt bị huỷ trong %s: %w", e.ProviderID, ctxErr)
		}
		// Không còn mắt xích nào sau Claude Code, nên đây là trạng thái "không khả dụng": cả
		// chuỗi đã cạn và lượt này không có câu trả lời nào để gửi.
		r.record(llmErrorAttempt(e, started, elapsed, llmErrorUpstream, ""))
		return "", fmt.Errorf("llm route: cạn chuỗi fallback, %s cũng hỏng: %w", e.ProviderID, err)
	}
	r.record(llmOKAttempt(e, started, elapsed))
	return text, nil
}

// record ghi một lần gọi vào sổ vận hành. Sổ hỏng KHÔNG được làm hỏng câu trả lời.
func (r *appLLMRunner) record(a store.LLMAttempt) {
	if err := r.cfg.Store.RecordLLMAttempt(a); err != nil {
		r.cfg.Logger.Warn("llm route: không ghi được telemetry",
			"provider", a.ProviderID, "model", a.ModelID, "err", err)
	}
}

// llmOKAttempt và llmErrorAttempt là hai hình dạng DUY NHẤT của một hàng telemetry.
//
// Không trường nào mang prompt, câu trả lời hay đường dẫn tệp — bảng llm_attempts đi thẳng ra
// Portal và vào log, nên nội dung của khách không có đường nào vào đây.
func llmOKAttempt(e store.LLMRouteEntry, started time.Time, elapsed time.Duration) store.LLMAttempt {
	return store.LLMAttempt{
		ProviderID: e.ProviderID, ModelID: e.ModelID,
		StartedAt: started, Duration: elapsed, Outcome: store.LLMAttemptOK,
	}
}

// llmErrorAttempt: next rỗng nghĩa là chuỗi dừng ở đây, và đó cũng là nghĩa của fell_back = 0.
// Hai trường đi cùng nhau nên chúng được tính từ cùng một chỗ, không phải hai chỗ.
func llmErrorAttempt(e store.LLMRouteEntry, started time.Time, elapsed time.Duration,
	kind llmErrorKind, next string) store.LLMAttempt {
	return store.LLMAttempt{
		ProviderID: e.ProviderID, ModelID: e.ModelID,
		StartedAt: started, Duration: elapsed, Outcome: store.LLMAttemptError,
		ErrorKind: string(kind), FellBack: next != "", NextProviderID: next,
	}
}

// llmErrorKindOf đọc phân loại của một lỗi gọi Provider.
//
// errors.As chứ không errors.Is: llmError cố ý không có Unwrap (xem app_llm_types.go), nên so
// sánh với một sentinel đã bọc sẽ luôn trượt.
//
// Lỗi lạ được coi là upstream — loại ĐƯỢC đi tiếp. Một adapter trả về thứ ngoài taxonomy là bug
// của adapter đó, và cách hỏng ít tệ nhất là để lượt rơi xuống Claude Code thay vì im lặng.
func llmErrorKindOf(err error) llmErrorKind {
	var le *llmError
	if errors.As(err, &le) {
		return le.Kind
	}
	if errors.Is(err, context.Canceled) {
		return llmErrorCanceled
	}
	return llmErrorUpstream
}

// nextEnabledProvider là mục sẽ được thử tiếp, để telemetry nói đúng chuỗi đã đi.
func nextEnabledProvider(entries []store.LLMRouteEntry, from int) string {
	for _, e := range entries[from:] {
		if e.Enabled {
			return e.ProviderID
		}
	}
	return ""
}
