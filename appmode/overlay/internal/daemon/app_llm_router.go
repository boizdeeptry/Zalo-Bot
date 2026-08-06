package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"agentdc/internal/ipc"
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
	// Claude dựng mắt xích cuối cho ĐÚNG model mà snapshot của lượt này nêu tên.
	//
	// Là hàm chứ không phải một runner dựng sẵn vì model của mắt xích cuối nằm TRONG snapshot, và
	// snapshot chỉ đọc lúc Run. Một runner dựng trước lượt phải đọc route lần thứ hai để biết gọi
	// model nào, và giữa hai lần đọc đó route đổi được — khi ấy lượt chạy một model trong khi
	// telemetry ghi model kia.
	//
	// Luôn có mặt: validateLLMRoute không cho lưu chuỗi kết thúc bằng thứ khác Claude Code, nên
	// router không cần một nhánh "nếu không còn gì".
	Claude func(model string) zaloRunner
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
	text, err := r.cfg.Claude(e.ModelID).Run(ctx, prompt, step)
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

// --- dây nối bản chạy ---

// newLLMAdapter chọn giao thức theo kind của Provider.
//
// Kind lạ trả về false chứ không một adapter mặc định: đoán bừa giao thức nghĩa là gửi khoá thật
// tới một máy chủ theo hình dạng request của nhà cung cấp khác.
func newLLMAdapter(kind, providerID string, client *http.Client) (providerAdapter, bool) {
	switch kind {
	case "openai":
		return newOpenAIAdapter(providerID, client), true
	case "anthropic":
		return newAnthropicAdapter(providerID, client), true
	case "gemini":
		return newGeminiAdapter(providerID, client), true
	case "openrouter":
		return newOpenRouterAdapter(providerID, client), true
	}
	return nil, false
}

// appZaloRunner dựng runner cho ĐÚNG một lượt Zalo.
//
// Dựng mới mỗi lượt chứ không cất vào api: đó là toàn bộ lý do một lần lưu route có hiệu lực ngay
// mà không phải mở lại phần mềm. Danh sách Provider cũng đọc lại ở đây, nên một Provider vừa thêm
// gọi được từ tin nhắn kế tiếp.
//
// Không trả về lỗi vì chỗ gọi nó là một tham số của answerZalo. Đọc hỏng thì lượt này đi thẳng
// Claude Code — đúng hành vi trước khi có định tuyến, và trả lời được vẫn hơn im lặng.
//
// hasNewFiles là tệp của TIN NÀY. Tệp của cả luồng mới là thứ quyết định — xem HasAttachments.
func (a *api) appZaloRunner(zc zaloConfig, base zaloRunner, threadID string, hasNewFiles bool) zaloRunner {
	// Chuỗi rỗng nghĩa là chưa có định tuyến, KHÔNG phải "không còn gì để gọi": router coi rỗng
	// là lỗi và lượt sẽ chết, nên cửa này phải ở đây chứ không ở trong router. Xảy ra khi lần
	// gieo lúc khởi động hỏng, và ở đó im lặng bỏ tin của khách là cách hỏng tệ nhất.
	//
	// Đây là lần đọc route THỨ NHẤT; Run đọc lần nữa để chụp snapshot của lượt. Chúng không đá
	// nhau: một chuỗi đã có không thể trở lại rỗng (validateLLMRoute từ chối danh sách rỗng, và
	// DeleteLLMProvider không xoá nổi mắt xích cuối). Bất đối xứng còn lại là cố ý — đọc hỏng ở
	// đây rơi về base, đọc hỏng trong Run thì giết lượt, vì tới đó đã không còn base để rơi về.
	snapshot, err := a.st.LLMRoute()
	if err != nil {
		a.logger.Error("llm route: không đọc được chuỗi, lượt này đi thẳng Claude Code", "err", err)
		return base
	}
	if len(snapshot.Entries) == 0 {
		return base
	}
	adapters, err := a.appLLMAdapters()
	if err != nil {
		a.logger.Error("llm route: không dựng được adapter, lượt này đi thẳng Claude Code", "err", err)
		return base
	}
	return newAppLLMRunner(appLLMRunnerConfig{
		Store:      a.st,
		Adapters:   adapters,
		Claude:     func(model string) zaloRunner { return a.appClaudeRunner(zc, base, model) },
		Credential: a.appLLMCredential,
		// Tệp của CẢ LUỒNG, không phải của tin này. answerZalo gọi mergeZaloFiles để gộp tệp của
		// 10 tin gần nhất vào lượt, rồi buildConsultPrompt viết đường dẫn tuyệt đối của chúng vào
		// prompt kèm câu "đọc trước khi trả lời". Tính theo mỗi tin thì một câu hỏi tiếp nối chỉ
		// có chữ sẽ mang prompt đó ra API: Provider không có ổ đĩa nên nó trả lời mù — đúng hồi
		// quy 2026-08-04 mà mergeZaloFiles sinh ra để chữa — và đường dẫn kèm tên người dùng
		// Windows cùng tiêu đề khách đặt rời khỏi máy này.
		HasAttachments: hasNewFiles || a.threadHasAttachments(threadID),
		Logger:         a.logger,
	})
}

// threadHasAttachments nói luồng này có tệp mở được trong tầm lịch sử mà một lượt nhìn thấy.
//
// Cùng tầm (zaloHistoryTurns) và cùng bộ lọc (Path != "") với mergeZaloFiles, vì câu hỏi ở đây
// đúng là "lượt sắp tới có tệp trong prompt không". Sticker và vị trí không bao giờ có Path, nên
// một hội thoại lỡ có sticker không bị đẩy sang đường chậm tới hết đời luồng.
//
// Đọc hỏng thì trả về true: Claude Code chạy được mọi lượt, còn đoán "không có tệp" mà đoán sai
// là gửi đường dẫn của khách ra ngoài. Sai về phía chậm, không sai về phía rò.
func (a *api) threadHasAttachments(threadID string) bool {
	history, err := a.st.ZaloMessages(threadID, zaloHistoryTurns)
	if err != nil {
		a.logger.Warn("llm route: không đọc được lịch sử, lượt này đi thẳng Claude Code",
			"thread", threadID, "err", err)
		return true
	}
	return slices.ContainsFunc(history, func(m ipc.ZaloMessage) bool {
		return slices.ContainsFunc(m.Attachments, func(at ipc.ZaloAttachment) bool {
			return at.Path != ""
		})
	})
}

// appClaudeRunner là mắt xích cuối chạy đúng model mà route nêu tên.
//
// zc là BẢN SAO (tham số theo giá trị), nên đổi Model ở đây không chạm tới cấu hình của vòng trực
// — hai lượt song song trên hai model khác nhau vẫn đúng.
//
// Trùng model thì giữ nguyên base đã tiêm vào. Không phải để tiết kiệm: base là chỗ duy nhất
// duty_test.go tiêm được một runner giả, và dựng mới ở đây biến mọi test đó thành lượt gọi tiến
// trình claude thật.
//
// Model rỗng cũng giữ base. validateLLMRoute không cho lưu một mắt xích như thế, nên đây là hàng
// bị sửa tay trong database — và `claude --model ""` là một dòng lệnh hỏng, còn cấu hình đang chạy
// thì vẫn trả lời được.
func (a *api) appClaudeRunner(zc zaloConfig, base zaloRunner, model string) zaloRunner {
	if model == "" || model == zc.Model {
		return base
	}
	zc.Model = model
	return execZaloRunner{cfg: zc, logger: a.logger}
}

// appLLMAdapters dựng adapter cho mọi Provider API đang lưu, khoá theo id.
//
// Provider đang TẮT vẫn có adapter: cửa bật/tắt của một lượt là cờ Enabled trên mắt xích route, và
// validateLLMRoute đã chặn việc lưu một chuỗi đi qua Provider tắt. Bỏ ở đây nữa thì tắt một
// Provider sẽ làm chuỗi dừng giữa chừng thay vì bỏ qua nó.
func (a *api) appLLMAdapters() (map[string]providerAdapter, error) {
	providers, err := a.st.LLMProviders()
	if err != nil {
		return nil, fmt.Errorf("llm route: đọc danh sách Provider: %w", err)
	}
	// Hạn giờ trên client vì ctx chặn được một lượt gọi nhưng không chặn được lúc bắt tay TLS.
	// Cùng mức với hạn giờ mỗi Provider của router, nên không có hai con số phải giữ đồng bộ.
	//
	// Client mới mỗi lượt là RẺ: Transport để nil nghĩa là http.DefaultTransport, nên pool kết
	// nối vẫn dùng chung — chỉ mỗi trường Timeout là của riêng client này.
	client := &http.Client{Timeout: defaultLLMProviderTimeout}
	adapters := make(map[string]providerAdapter, len(providers))
	for _, p := range providers {
		if adapter, ok := newLLMAdapter(p.Kind, p.ID, client); ok {
			adapters[p.ID] = adapter
		}
	}
	return adapters, nil
}

// appLLMCredential mở khoá của một Provider, trả về BẢN RÕ MỚI mỗi lượt gọi.
//
// Mới mỗi lượt vì router xoá lát cắt nó nhận ngay sau lời gọi adapter: một hiện thực trả về lát
// cắt dùng chung sẽ phát ra toàn số 0 từ lượt thứ hai.
func (a *api) appLLMCredential(providerID string) ([]byte, error) {
	cipher, err := a.st.LLMCredentialCipher(providerID)
	if err != nil {
		return nil, err
	}
	return unprotectProviderSecret(cipher)
}
