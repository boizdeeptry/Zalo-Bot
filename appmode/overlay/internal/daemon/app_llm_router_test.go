package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/store"
)

// --- fakes ---

// fakeRouteStore là store của router thu nhỏ lại còn hai việc: phát snapshot và nhận telemetry.
//
// LLMRoute trả về ĐÚNG lát cắt nó đang giữ, không phải bản sao — ngược hẳn với store thật. Đó là
// chủ ý: chỉ khi nguồn chia sẻ mảng nền thì test mới hỏi được câu "router có tự sao chép không",
// và đó là câu duy nhất chứng minh một lượt đang chạy không đổi đường giữa chừng.
type fakeRouteStore struct {
	mu       sync.Mutex
	snapshot store.LLMRouteSnapshot
	attempts []store.LLMAttempt
	routeErr error
	recordFn func(store.LLMAttempt) error
}

func (s *fakeRouteStore) LLMRoute() (store.LLMRouteSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.routeErr != nil {
		return store.LLMRouteSnapshot{}, s.routeErr
	}
	return s.snapshot, nil
}

func (s *fakeRouteStore) RecordLLMAttempt(a store.LLMAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordFn != nil {
		if err := s.recordFn(a); err != nil {
			return err
		}
	}
	s.attempts = append(s.attempts, a)
	return nil
}

func (s *fakeRouteStore) recorded() []store.LLMAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.LLMAttempt(nil), s.attempts...)
}

// setEntry sửa một mục của snapshot TẠI CHỖ, mô phỏng một lần lưu route xảy ra giữa lượt.
func (s *fakeRouteStore) setEntry(i int, e store.LLMRouteEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.Entries[i] = e
}

// fakeCall là một lượt gọi adapter, ở dạng serialize được để soi canary.
//
// Cred giữ NGUYÊN lát cắt adapter nhận được, không phải bản sao: test xoá-bản-rõ dựa vào đúng
// việc chúng dùng chung mảng nền để thấy router đã xoá thật hay chưa.
type fakeCall struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Cred   []byte `json:"credential"`
}

type fakeAdapter struct {
	mu    sync.Mutex
	calls []fakeCall
	fn    func(ctx context.Context, req llmRequest) (llmResponse, error)
	// entered nhận một tín hiệu khi adapter vào lượt gọi, và chỉ hangAdapter dựng nó. Đệm 1 để
	// adapter không kẹt lại khi test không cần đọc; test nào cần chèn việc vào giữa một lượt
	// gọi thì chờ ở đây.
	entered chan struct{}
}

func (a *fakeAdapter) Generate(ctx context.Context, req llmRequest, credential []byte) (llmResponse, error) {
	a.mu.Lock()
	a.calls = append(a.calls, fakeCall{Model: req.Model, Prompt: req.Prompt, Cred: credential})
	a.mu.Unlock()
	return a.fn(ctx, req)
}

// Router chỉ sinh chữ. Test và Discover thuộc đường quản trị, nên gọi tới đây là một lỗi lập
// trình chứ không phải một nhánh cần mô phỏng.
func (a *fakeAdapter) Test(context.Context, string, []byte) error {
	panic("router không được gọi Test")
}

func (a *fakeAdapter) Discover(context.Context, []byte) ([]store.LLMModel, error) {
	panic("router không được gọi Discover")
}

func (a *fakeAdapter) seen() []fakeCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]fakeCall(nil), a.calls...)
}

// MarshalJSON để serialize() nhìn thấy các lượt gọi đã ghi thay vì một struct toàn trường không
// xuất khẩu — tức là để phép soi canary có gì để soi.
func (a *fakeAdapter) MarshalJSON() ([]byte, error) { return json.Marshal(a.seen()) }

// okAdapter trả lời ngay bằng text.
func okAdapter(text string) *fakeAdapter {
	return &fakeAdapter{fn: func(context.Context, llmRequest) (llmResponse, error) {
		return llmResponse{Text: text}, nil
	}}
}

// failAdapter hỏng ngay bằng đúng một loại lỗi.
func failAdapter(kind llmErrorKind) *fakeAdapter {
	return &fakeAdapter{fn: func(context.Context, llmRequest) (llmResponse, error) {
		return llmResponse{}, newLLMError(kind, nil, "fake %s", kind)
	}}
}

// hangAdapter treo tới khi ctx của chính lượt gọi chết, rồi phân loại y như adapter thật.
func hangAdapter() *fakeAdapter {
	a := &fakeAdapter{entered: make(chan struct{}, 1)}
	a.fn = func(ctx context.Context, _ llmRequest) (llmResponse, error) {
		a.entered <- struct{}{}
		<-ctx.Done()
		return llmResponse{}, transportError("fake", ctx.Err())
	}
	return a
}

type fakeClaude struct {
	mu      sync.Mutex
	prompts []string
	steps   []string
	fn      func(ctx context.Context, prompt string) (string, error)
}

func (c *fakeClaude) Run(ctx context.Context, prompt string, step func(string)) (string, error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, prompt)
	c.mu.Unlock()
	if step != nil {
		// Gọi step là cách DUY NHẤT chứng minh callback gốc đi tới được đây: hàm không so sánh
		// được với nhau, nên chỉ tác dụng phụ của nó mới quan sát được.
		step("claude nhận được step")
	}
	return c.fn(ctx, prompt)
}

func (c *fakeClaude) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.prompts...)
}

func okClaude(text string) *fakeClaude {
	return &fakeClaude{fn: func(context.Context, string) (string, error) { return text, nil }}
}

// fakeCredentials phát một bản rõ MỚI cho mỗi lượt gọi và giữ lại mọi lát cắt đã phát.
//
// Phát bản mới mỗi lần vì đường thật cũng vậy (DPAPI cấp phát bộ nhớ mới), và giữ lại để test
// soi được rằng router đã xoá đúng cái nó đã cầm.
type fakeCredentials struct {
	mu     sync.Mutex
	plain  map[string]string
	err    map[string]error
	handed [][]byte
}

func newFakeCredentials(plain map[string]string) *fakeCredentials {
	return &fakeCredentials{plain: plain, err: map[string]error{}}
}

func (c *fakeCredentials) open(providerID string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.err[providerID]; err != nil {
		return nil, err
	}
	out := []byte(c.plain[providerID])
	c.handed = append(c.handed, out)
	return out, nil
}

func (c *fakeCredentials) issued() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.handed...)
}

// --- helpers ---

func entry(providerID, modelID string, enabled bool) store.LLMRouteEntry {
	return store.LLMRouteEntry{ProviderID: providerID, ModelID: modelID, Enabled: enabled}
}

// newRoute gán position theo thứ tự, giống hệt cách store thật đọc chuỗi ra.
func newRoute(entries ...store.LLMRouteEntry) store.LLMRouteSnapshot {
	for i := range entries {
		entries[i].Position = i
	}
	return store.LLMRouteSnapshot{Revision: 7, Entries: entries}
}

// threeEntryRoute là chuỗi hai API rồi Claude Code — hình dạng mà phần lớn test dưới đây cần.
func threeEntryRoute() store.LLMRouteSnapshot {
	return newRoute(
		entry("openai-1", "gpt-5-mini", true),
		entry("gemini-1", "gemini-2.5-flash", true),
		entry("claude-code", "haiku", true),
	)
}

// routerFixture gom bốn thứ router cần lại để từng test chỉ nói phần nó quan tâm.
type routerFixture struct {
	store    *fakeRouteStore
	adapters map[string]providerAdapter
	claude   *fakeClaude
	creds    *fakeCredentials

	// step chạy trên goroutine của Claude Code, nên nó khoá y như các fake khác — mọi thứ
	// router chạm tới trong một lượt đều phải chịu được -race.
	mu    sync.Mutex
	steps []string
}

func newRouterFixture(snapshot store.LLMRouteSnapshot, claude *fakeClaude) *routerFixture {
	return &routerFixture{
		store:    &fakeRouteStore{snapshot: snapshot},
		adapters: map[string]providerAdapter{},
		claude:   claude,
		creds:    newFakeCredentials(map[string]string{}),
	}
}

func (f *routerFixture) with(providerID, credential string, a *fakeAdapter) *routerFixture {
	f.adapters[providerID] = a
	f.creds.plain[providerID] = credential
	return f
}

func (f *routerFixture) step(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, msg)
}

func (f *routerFixture) seenSteps() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.steps...)
}

func (f *routerFixture) runner(cfg appLLMRunnerConfig) *appLLMRunner {
	cfg.Store = f.store
	cfg.Adapters = f.adapters
	cfg.Claude = f.claude
	cfg.Credential = f.creds.open
	// Vứt log đi: một test cố ý làm hỏng telemetry sẽ in WARN vào slog mặc định, tức là vào
	// giữa đầu ra của những test không liên quan.
	cfg.Logger = slog.New(slog.DiscardHandler)
	return newAppLLMRunner(cfg)
}

// serialize đổ một giá trị ra JSON để soi chuỗi. Lỗi mã hoá là lỗi của test, không phải phát hiện.
func serialize(t *testing.T, v any) string {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal(%T) = %v; want nil", v, err)
	}
	return string(blob)
}

// jsonForm là canary SAU khi đi qua json.Marshal, tức đúng hình dạng nó sẽ mang trong chuỗi mà
// serialize() trả về.
//
// Cần thiết vì một đường dẫn Windows đầy dấu "\", và json.Marshal nhân đôi chúng: so chuỗi thô
// với blob đã mã hoá thì KHÔNG BAO GIỜ khớp, kể cả khi đường dẫn đã rò ra thật. Phép kiểm đó
// luôn xanh — tệ hơn không có, vì nó trông như đang canh cửa.
func jsonForm(t *testing.T, canary string) string {
	t.Helper()
	return strings.Trim(serialize(t, canary), `"`)
}

// kindOfErr đọc phân loại ra khỏi lỗi router trả về, để test khỏi so khớp câu chữ.
func kindOfErr(t *testing.T, err error) llmErrorKind {
	t.Helper()
	var le *llmError
	if !errors.As(err, &le) {
		t.Fatalf("errors.As(%v, *llmError) = false; want true", err)
	}
	return le.Kind
}

// --- tests ---

func TestAppLLMRunnerServesTheFirstEnabledEntry(t *testing.T) {
	first, second := okAdapter(`{"answer":"a"}`), okAdapter(`{"answer":"b"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)

	got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf(`Run(ctx, "câu hỏi", step) = _, %v; want nil`, err)
	}
	if got != `{"answer":"a"}` {
		t.Errorf(`Run(ctx, "câu hỏi", step) = %q; want %q`, got, `{"answer":"a"}`)
	}
	calls := first.seen()
	if len(calls) != 1 || calls[0].Model != "gpt-5-mini" || calls[0].Prompt != "câu hỏi" {
		t.Fatalf("openai-1 nhận %+v; want đúng 1 lượt {gpt-5-mini, câu hỏi}", calls)
	}
	if len(second.seen()) != 0 {
		t.Errorf("gemini-1 được gọi %d lần; want 0", len(second.seen()))
	}
	if len(claude.seen()) != 0 {
		t.Errorf("claude được gọi %d lần; want 0", len(claude.seen()))
	}
	attempts := f.store.recorded()
	if len(attempts) != 1 {
		t.Fatalf("RecordLLMAttempt gọi %d lần; want 1", len(attempts))
	}
	a := attempts[0]
	if a.ProviderID != "openai-1" || a.ModelID != "gpt-5-mini" {
		t.Errorf("attempt[0] provider/model = %s/%s; want openai-1/gpt-5-mini", a.ProviderID, a.ModelID)
	}
	if a.Outcome != store.LLMAttemptOK || a.ErrorKind != "" || a.FellBack {
		t.Errorf("attempt[0] outcome/kind/fellBack = %s/%s/%v; want ok//false", a.Outcome, a.ErrorKind, a.FellBack)
	}
	if a.StartedAt.IsZero() {
		t.Errorf("attempt[0].StartedAt là zero; want thời điểm bắt đầu lượt gọi")
	}
}

func TestAppLLMRunnerFallsBackOnDegradedErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind llmErrorKind
	}{
		{"rate limit 429", llmErrorRateLimit},
		{"network", llmErrorNetwork},
		{"upstream 5xx", llmErrorUpstream},
		{"timeout", llmErrorTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := failAdapter(tc.kind), okAdapter(`{"answer":"b"}`)
			claude := okClaude(`{"answer":"claude"}`)
			f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)

			got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
			if err != nil {
				t.Fatalf("Run() với lỗi %s = _, %v; want nil", tc.kind, err)
			}
			if got != `{"answer":"b"}` {
				t.Errorf("Run() với lỗi %s = %q; want %q", tc.kind, got, `{"answer":"b"}`)
			}
			if len(claude.seen()) != 0 {
				t.Errorf("claude được gọi %d lần; want 0", len(claude.seen()))
			}
			attempts := f.store.recorded()
			if len(attempts) != 2 {
				t.Fatalf("RecordLLMAttempt gọi %d lần; want 2 (một lượt hỏng, một lượt được)", len(attempts))
			}
			if attempts[0].Outcome != store.LLMAttemptError || attempts[0].ErrorKind != string(tc.kind) {
				t.Errorf("attempt[0] outcome/kind = %s/%s; want error/%s",
					attempts[0].Outcome, attempts[0].ErrorKind, tc.kind)
			}
			if !attempts[0].FellBack || attempts[0].NextProviderID != "gemini-1" {
				t.Errorf("attempt[0] fellBack/next = %v/%q; want true/gemini-1",
					attempts[0].FellBack, attempts[0].NextProviderID)
			}
			if attempts[1].ProviderID != "gemini-1" || attempts[1].Outcome != store.LLMAttemptOK {
				t.Errorf("attempt[1] provider/outcome = %s/%s; want gemini-1/ok",
					attempts[1].ProviderID, attempts[1].Outcome)
			}
		})
	}
}

func TestAppLLMRunnerStopsOnNonFallbackErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind llmErrorKind
	}{
		{"credential", llmErrorCredential},
		{"model", llmErrorModel},
		{"request", llmErrorRequest},
		{"policy", llmErrorPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, second := failAdapter(tc.kind), okAdapter(`{"answer":"b"}`)
			claude := okClaude(`{"answer":"claude"}`)
			f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)

			got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
			if err == nil {
				t.Fatalf("Run() với lỗi %s = %q, nil; want lỗi dừng chuỗi", tc.kind, got)
			}
			if k := kindOfErr(t, err); k != tc.kind {
				t.Errorf("Run() với lỗi %s trả về lỗi loại %s; want %s", tc.kind, k, tc.kind)
			}
			if len(second.seen()) != 0 {
				t.Errorf("gemini-1 được gọi %d lần sau lỗi %s; want 0", len(second.seen()), tc.kind)
			}
			if len(claude.seen()) != 0 {
				t.Errorf("claude được gọi %d lần sau lỗi %s; want 0", len(claude.seen()), tc.kind)
			}
			attempts := f.store.recorded()
			if len(attempts) != 1 {
				t.Fatalf("RecordLLMAttempt gọi %d lần; want 1", len(attempts))
			}
			if attempts[0].FellBack || attempts[0].NextProviderID != "" {
				t.Errorf("attempt[0] fellBack/next = %v/%q; want false/\"\"",
					attempts[0].FellBack, attempts[0].NextProviderID)
			}
		})
	}
}

func TestAppLLMRunnerSkipsDisabledEntriesAndCallsEachProviderOnce(t *testing.T) {
	first, off, third := failAdapter(llmErrorUpstream), okAdapter(`{"answer":"off"}`), okAdapter(`{"answer":"c"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(
		entry("openai-1", "gpt-5-mini", true),
		entry("gemini-1", "gemini-2.5-flash", false),
		entry("openrouter-1", "x-ai/grok-4", true),
		entry("claude-code", "haiku", true),
	), claude).
		with("openai-1", "sk-one", first).
		with("gemini-1", "sk-two", off).
		with("openrouter-1", "sk-three", third)

	got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf("Run() = _, %v; want nil", err)
	}
	if got != `{"answer":"c"}` {
		t.Errorf("Run() = %q; want %q", got, `{"answer":"c"}`)
	}
	if len(off.seen()) != 0 {
		t.Errorf("mục đang tắt được gọi %d lần; want 0", len(off.seen()))
	}
	if len(first.seen()) != 1 {
		t.Errorf("openai-1 được gọi %d lần; want đúng 1 (không thử lại cùng Provider)", len(first.seen()))
	}
	attempts := f.store.recorded()
	if len(attempts) != 2 {
		t.Fatalf("RecordLLMAttempt gọi %d lần; want 2 (mục tắt không sinh telemetry)", len(attempts))
	}
	if attempts[0].NextProviderID != "openrouter-1" {
		t.Errorf("attempt[0].NextProviderID = %q; want openrouter-1 (bỏ qua mục đang tắt)",
			attempts[0].NextProviderID)
	}
}

func TestAppLLMRunnerFallsBackToClaudeLast(t *testing.T) {
	first := failAdapter(llmErrorUpstream)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(
		entry("openai-1", "gpt-5-mini", true),
		entry("claude-code", "haiku", true),
	), claude).with("openai-1", "sk-one", first)

	runner := f.runner(appLLMRunnerConfig{})
	got, err := runner.Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf("Run() = _, %v; want nil", err)
	}
	if got != `{"answer":"claude"}` {
		t.Errorf("Run() = %q; want %q", got, `{"answer":"claude"}`)
	}
	if prompts := claude.seen(); len(prompts) != 1 || prompts[0] != "câu hỏi" {
		t.Fatalf("claude nhận %q; want đúng một lượt \"câu hỏi\"", prompts)
	}
	// step gốc CHỈ đi tới Claude Code — bốn adapter API không có chỗ nào nhận nó.
	if steps := f.seenSteps(); len(steps) != 1 || steps[0] != "claude nhận được step" {
		t.Errorf("step nhận %q; want đúng một dòng từ Claude Code", steps)
	}
	attempts := f.store.recorded()
	if len(attempts) != 2 {
		t.Fatalf("RecordLLMAttempt gọi %d lần; want 2", len(attempts))
	}
	if attempts[0].NextProviderID != "claude-code" {
		t.Errorf("attempt[0].NextProviderID = %q; want claude-code", attempts[0].NextProviderID)
	}
	last := attempts[1]
	if last.ProviderID != "claude-code" || last.ModelID != "haiku" || last.Outcome != store.LLMAttemptOK {
		t.Errorf("attempt[1] = %s/%s/%s; want claude-code/haiku/ok", last.ProviderID, last.ModelID, last.Outcome)
	}
}

func TestAppLLMRunnerReportsUnavailableWhenEveryEntryFails(t *testing.T) {
	first := failAdapter(llmErrorUpstream)
	claude := &fakeClaude{fn: func(context.Context, string) (string, error) {
		return "", errors.New("claude không chạy được")
	}}
	f := newRouterFixture(newRoute(
		entry("openai-1", "gpt-5-mini", true),
		entry("claude-code", "haiku", true),
	), claude).with("openai-1", "sk-one", first)

	got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
	if err == nil {
		t.Fatalf("Run() = %q, nil; want lỗi khi cả chuỗi hỏng", got)
	}
	if got != "" {
		t.Errorf("Run() = %q; want chuỗi rỗng khi hỏng", got)
	}
	attempts := f.store.recorded()
	if len(attempts) != 2 {
		t.Fatalf("RecordLLMAttempt gọi %d lần; want 2", len(attempts))
	}
	for i, a := range attempts {
		if a.Outcome != store.LLMAttemptError {
			t.Errorf("attempt[%d].Outcome = %s; want error", i, a.Outcome)
		}
	}
	if attempts[1].FellBack {
		t.Errorf("attempt[1].FellBack = true; want false (không còn gì để đổi sang)")
	}
}

func TestAppLLMRunnerTimesOutOneProviderAndMovesOn(t *testing.T) {
	first := hangAdapter()
	second := okAdapter(`{"answer":"b"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)

	runner := f.runner(appLLMRunnerConfig{
		PerProviderTimeout: 20 * time.Millisecond,
		APIChainTimeout:    10 * time.Second,
	})
	got, err := runner.Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf("Run() = _, %v; want nil", err)
	}
	if got != `{"answer":"b"}` {
		t.Errorf("Run() = %q; want %q", got, `{"answer":"b"}`)
	}
	attempts := f.store.recorded()
	if len(attempts) != 2 {
		t.Fatalf("RecordLLMAttempt gọi %d lần; want 2", len(attempts))
	}
	if attempts[0].ErrorKind != string(llmErrorTimeout) {
		t.Errorf("attempt[0].ErrorKind = %q; want %q", attempts[0].ErrorKind, llmErrorTimeout)
	}
	// Adapter này treo tới khi hết hạn giờ, nên hàng telemetry của nó PHẢI mang một khoảng thời
	// gian thật. Không có ràng buộc này thì một hằng 0 lọt vào cũng không ai thấy, và bảng chỉ
	// còn cho biết lượt nào hỏng chứ không cho biết Provider nào chậm.
	if attempts[0].Duration <= 0 {
		t.Errorf("attempt[0].Duration = %v; want > 0 sau một lượt gọi hết hạn giờ", attempts[0].Duration)
	}
}

func TestAppLLMRunnerStopsTheAPIChainAtItsBudget(t *testing.T) {
	first := hangAdapter()
	second := okAdapter(`{"answer":"b"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)

	// Hạn mức cả chuỗi ngắn hơn hạn mức một Provider: mục đầu nuốt hết ngân sách, nên mục thứ
	// hai không được gọi nữa và lượt đi thẳng tới Claude Code.
	runner := f.runner(appLLMRunnerConfig{
		PerProviderTimeout: 10 * time.Second,
		APIChainTimeout:    20 * time.Millisecond,
	})
	got, err := runner.Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf("Run() = _, %v; want nil", err)
	}
	if got != `{"answer":"claude"}` {
		t.Errorf("Run() = %q; want %q", got, `{"answer":"claude"}`)
	}
	if len(second.seen()) != 0 {
		t.Errorf("gemini-1 được gọi %d lần sau khi hết ngân sách chuỗi API; want 0", len(second.seen()))
	}
	attempts := f.store.recorded()
	if len(attempts) != 2 {
		t.Fatalf("RecordLLMAttempt gọi %d lần; want 2", len(attempts))
	}
	if attempts[0].NextProviderID != "claude-code" {
		t.Errorf("attempt[0].NextProviderID = %q; want claude-code", attempts[0].NextProviderID)
	}
}

// Hạn mức lượt hết là cách một lượt CHẾT THẬT ở bản chạy: triggerZaloDuty dựng ctx bằng
// context.WithTimeout(context.Background(), zaloTurnBudget) và tắt daemon KHÔNG huỷ nó — một
// lượt đang chạy chỉ bị bỏ lại. Nên đường này, chứ không phải context.Canceled, là đường mà
// việc "không đổ tội cho Provider" phải đúng.
func TestAppLLMRunnerAbortsWhenTheTurnBudgetExpires(t *testing.T) {
	first := hangAdapter()
	second := okAdapter(`{"answer":"b"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)

	// Hạn mức lượt ngắn hơn cả hai hạn mức của router, nên thứ chết trước là ctx cha.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	t.Cleanup(cancel)
	runner := f.runner(appLLMRunnerConfig{
		PerProviderTimeout: 10 * time.Second,
		APIChainTimeout:    10 * time.Second,
	})

	got, err := runner.Run(ctx, "câu hỏi", f.step)
	if err == nil {
		t.Fatalf("Run() sau khi hết hạn mức lượt = %q, nil; want lỗi", got)
	}
	if len(second.seen()) != 0 || len(claude.seen()) != 0 {
		t.Errorf("sau khi hết hạn mức lượt: gemini-1 %d lượt, claude %d lượt; want 0/0",
			len(second.seen()), len(claude.seen()))
	}
	// Cùng lý do như lượt bị huỷ: hạn mức lượt là quyết định của daemon, không phải lỗi của
	// Provider. Ghi nó vào sổ làm LLMStatus báo hỏng cho một Provider chỉ mắc tội chậm hơn
	// phần thời gian còn lại của lượt.
	if attempts := f.store.recorded(); len(attempts) != 0 {
		t.Errorf("RecordLLMAttempt gọi %d lần khi hết hạn mức lượt; want 0 (%+v)", len(attempts), attempts)
	}
}

func TestAppLLMRunnerAbortsWhenTheTurnIsCancelled(t *testing.T) {
	first := hangAdapter()
	second := okAdapter(`{"answer":"b"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	runner := f.runner(appLLMRunnerConfig{
		PerProviderTimeout: 10 * time.Second,
		APIChainTimeout:    10 * time.Second,
	})

	type result struct {
		text string
		err  error
	}
	// Goroutine kết thúc khi Run trả về, và Run trả về vì cancel() dưới đây giết ctx của nó.
	done := make(chan result, 1)
	go func() {
		text, err := runner.Run(ctx, "câu hỏi", f.step)
		done <- result{text, err}
	}()

	// Huỷ SAU khi adapter đã vào lượt gọi: huỷ sớm hơn thì Run chết trước vòng lặp và test chỉ
	// chứng minh được rằng một ctx chết thì không gọi ai, không phải điều đang hỏi.
	<-first.entered
	cancel()
	got := <-done

	if got.err == nil {
		t.Fatalf("Run() sau khi huỷ = %q, nil; want lỗi", got.text)
	}
	if !errors.Is(got.err, context.Canceled) && kindOfErr(t, got.err) != llmErrorCanceled {
		t.Errorf("Run() sau khi huỷ trả về %v; want lỗi huỷ", got.err)
	}
	if len(second.seen()) != 0 || len(claude.seen()) != 0 {
		t.Errorf("sau khi huỷ: gemini-1 %d lượt, claude %d lượt; want 0/0",
			len(second.seen()), len(claude.seen()))
	}
	// Một lượt bị huỷ KHÔNG phải lỗi của Provider: ghi nó vào telemetry là đổ tội nhầm và làm
	// LLMStatus báo hỏng cho một Provider chưa hề trả lời sai.
	if attempts := f.store.recorded(); len(attempts) != 0 {
		t.Errorf("RecordLLMAttempt gọi %d lần khi lượt bị huỷ; want 0 (%+v)", len(attempts), attempts)
	}
}

func TestAppLLMRunnerKeepsOneSnapshotForTheWholeTurn(t *testing.T) {
	blocked, entered := make(chan struct{}), make(chan struct{}, 1)
	first := &fakeAdapter{fn: func(context.Context, llmRequest) (llmResponse, error) {
		entered <- struct{}{}
		<-blocked
		return llmResponse{}, newLLMError(llmErrorUpstream, nil, "fake 503")
	}}
	original, swapped := okAdapter(`{"answer":"original"}`), okAdapter(`{"answer":"swapped"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).
		with("openai-1", "sk-one", first).
		with("gemini-1", "sk-two", original).
		with("openrouter-1", "sk-three", swapped)

	type result struct {
		text string
		err  error
	}
	// Goroutine kết thúc khi Run trả về; Run trả về sau khi close(blocked) mở khoá mục đầu.
	done := make(chan result, 1)
	go func() {
		text, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
		done <- result{text, err}
	}()

	// Đợi mục đầu vào lượt gọi trước đã: lưu sớm hơn thì router chưa kịp đọc snapshot nào, và
	// test chỉ chứng minh được rằng một route mới thì được dùng — chuyện hiển nhiên.
	<-entered
	// Người dùng lưu route mới ĐÚNG LÚC lượt đang chạy dở.
	f.store.setEntry(1, entry("openrouter-1", "x-ai/grok-4", true))
	close(blocked)
	got := <-done

	if got.err != nil {
		t.Fatalf("Run() = _, %v; want nil", got.err)
	}
	if got.text != `{"answer":"original"}` {
		t.Errorf("Run() = %q; want %q (lượt đang chạy phải giữ snapshot cũ)",
			got.text, `{"answer":"original"}`)
	}
	if len(swapped.seen()) != 0 {
		t.Errorf("Provider của route mới được gọi %d lần trong lượt cũ; want 0", len(swapped.seen()))
	}
}

func TestAppLLMRunnerSendsAttachmentTurnsOnlyToClaude(t *testing.T) {
	// Hai canary: đường dẫn tệp khách gửi (nằm trong prompt, đúng như buildConsultPrompt dựng)
	// và nội dung tệp đó. Không cái nào được rời khỏi máy này qua một API Provider.
	const canaryPath = `C:\Users\Admin\.agentdc\zalo-files\CANARY-PATH-9f3a.pdf`
	const canaryBytes = "CANARY-BYTES-4b71-noi-dung-rieng-tu-cua-khach"

	prompt := "Khách gửi tệp:\n  " + canaryPath + "\nNội dung đọc được: " + canaryBytes + "\nCâu hỏi: giá bao nhiêu?"
	first, second := okAdapter(`{"answer":"api"}`), okAdapter(`{"answer":"api2"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)

	got, err := f.runner(appLLMRunnerConfig{HasAttachments: true}).Run(t.Context(), prompt, f.step)
	if err != nil {
		t.Fatalf("Run() với tệp đính kèm = _, %v; want nil", err)
	}
	if got != `{"answer":"claude"}` {
		t.Errorf("Run() với tệp đính kèm = %q; want %q", got, `{"answer":"claude"}`)
	}
	// Fixture thật sự mang canary: nếu Claude không thấy nó thì mọi khẳng định "vắng mặt" dưới
	// đây đều rỗng nghĩa. Errorf chứ không Fatalf, để các khẳng định vắng mặt vẫn được chạy —
	// một thay đổi làm lượt đi sai đường phải hiện ra ở CẢ hai chỗ.
	prompts := claude.seen()
	if len(prompts) != 1 {
		t.Errorf("claude nhận %d prompt; want 1", len(prompts))
	} else if !strings.Contains(prompts[0], canaryPath) || !strings.Contains(prompts[0], canaryBytes) {
		t.Errorf("claude nhận %q; want prompt chứa cả hai canary", prompts[0])
	}
	for id, a := range f.adapters {
		if calls := a.(*fakeAdapter).seen(); len(calls) != 0 {
			t.Errorf("adapter %s được gọi %d lần trong lượt có tệp; want 0", id, len(calls))
		}
	}
	if issued := f.creds.issued(); len(issued) != 0 {
		t.Errorf("mở khoá %d lần trong lượt có tệp; want 0 (không gọi API thì không cần khoá)", len(issued))
	}
	for _, canary := range []string{jsonForm(t, canaryPath), jsonForm(t, canaryBytes)} {
		if blob := serialize(t, f.adapters); strings.Contains(blob, canary) {
			t.Errorf("đầu vào adapter chứa canary %q: %s", canary, blob)
		}
		if blob := serialize(t, f.store.recorded()); strings.Contains(blob, canary) {
			t.Errorf("telemetry chứa canary %q: %s", canary, blob)
		}
	}
}

func TestAppLLMRunnerZeroesTheCredentialAfterTheCall(t *testing.T) {
	first := failAdapter(llmErrorUpstream)
	second := okAdapter(`{"answer":"b"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-ant-canary-one", first).with("gemini-1", "sk-canary-two", second)

	if _, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step); err != nil {
		t.Fatalf("Run() = _, %v; want nil", err)
	}
	issued := f.creds.issued()
	if len(issued) != 2 {
		t.Fatalf("mở khoá %d lần; want 2 (một lần cho mỗi Provider được gọi)", len(issued))
	}
	for i, plain := range issued {
		for j, b := range plain {
			if b != 0 {
				t.Fatalf("bản rõ thứ %d còn byte khác 0 ở vị trí %d (%q); want đã xoá sạch",
					i, j, plain)
			}
		}
	}
	// Adapter giữ ĐÚNG lát cắt router truyền vào, nên đây là bằng chứng router xoá chính thứ
	// adapter đã cầm chứ không phải một bản sao nào khác.
	for _, call := range first.seen() {
		if len(call.Cred) == 0 {
			t.Fatalf("adapter nhận credential rỗng; fixture hỏng")
		}
		for _, b := range call.Cred {
			if b != 0 {
				t.Errorf("credential adapter cầm = %q; want đã xoá sạch sau lượt gọi", call.Cred)
				break
			}
		}
	}
}

func TestAppLLMRunnerStopsWhenTheCredentialCannotBeOpened(t *testing.T) {
	first, second := okAdapter(`{"answer":"a"}`), okAdapter(`{"answer":"b"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(threeEntryRoute(), claude).with("openai-1", "sk-one", first).with("gemini-1", "sk-two", second)
	f.creds.err["openai-1"] = fmt.Errorf("unprotect: %w", ErrCredentialUnreadable)

	got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
	if err == nil {
		t.Fatalf("Run() với khoá không mở được = %q, nil; want lỗi cấu hình", got)
	}
	if !errors.Is(err, ErrCredentialUnreadable) {
		t.Errorf("Run() = %v; want lỗi bọc ErrCredentialUnreadable", err)
	}
	if len(first.seen()) != 0 || len(second.seen()) != 0 || len(claude.seen()) != 0 {
		t.Errorf("khoá không mở được nhưng vẫn gọi: openai %d, gemini %d, claude %d; want 0/0/0",
			len(first.seen()), len(second.seen()), len(claude.seen()))
	}
	if attempts := f.store.recorded(); len(attempts) != 0 {
		t.Errorf("RecordLLMAttempt gọi %d lần khi chưa gọi Provider nào; want 0", len(attempts))
	}
}

func TestAppLLMRunnerFailsWhenTheRouteCannotBeRead(t *testing.T) {
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(entry("claude-code", "haiku", true)), claude)
	f.store.routeErr = errors.New("database đóng")

	if _, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step); err == nil {
		t.Fatalf("Run() khi không đọc được route = _, nil; want lỗi")
	}
	if len(claude.seen()) != 0 {
		t.Errorf("claude được gọi %d lần khi chưa có route; want 0", len(claude.seen()))
	}
}

func TestAppLLMRunnerKeepsAnsweringWhenTelemetryFails(t *testing.T) {
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(entry("claude-code", "haiku", true)), claude)
	f.store.recordFn = func(store.LLMAttempt) error { return errors.New("đĩa đầy") }

	// Telemetry là sổ vận hành, không phải câu trả lời: mất nó thì mất số liệu, không được mất
	// tin nhắn gửi cho khách.
	got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf("Run() khi telemetry hỏng = _, %v; want nil", err)
	}
	if got != `{"answer":"claude"}` {
		t.Errorf("Run() = %q; want %q", got, `{"answer":"claude"}`)
	}
}
