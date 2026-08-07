package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/config"
	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

// llmPackageCanary là khoá thử nghiệm gốc của MỌI tầng test: tệp này, app_llm_api_test.go,
// providers.test.mjs và models.test.mjs. Cửa chặn gói trong tests/build-app.Tests.ps1 tìm đúng
// chuỗi con này trong gói đã dựng và đòi 0 lần khớp.
//
// Một chuỗi gốc cho mọi khoá thử nghiệm, và đó là ĐIỀU KIỆN để phép quét kia nói được điều gì:
// quét một chuỗi mà không tệp nào trong repo mang thì luôn xanh, kể cả khi cửa chặn đã hỏng. Có
// mặt trong fixture nghĩa là chuỗi này thật sự có đường vào gói — một hằng test bị nhấc lên tệp
// sản xuất, hay một khoá dán nhầm vào trang Portal (assets nhúng thẳng vào agentdc.exe) — nên
// "0 lần khớp" là một khẳng định có thể sai.
const llmPackageCanary = "sk-package-must-never-contain-7f36d2"

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

// fakeCLI dựng một *cliAdapter THẬT với seam run giả. Router nhận diện một mắt xích local_cli bằng
// type *cliAdapter (xem firstEligibleCLIEntry), nên một lượt có tệp trên codex/gemini-cli phải đi
// qua đúng type đó — một fakeAdapter dán nhãn "codex" sẽ bị coi là HTTP và không đủ điều kiện.
//
// seam run trả về answer đã dựng sẵn và ghi lại phần đầu vào (argv+stdin) vào seen, để test soi
// được prompt của khách CÓ tới CLI cục bộ (được phép) mà KHÔNG tới một adapter HTTP.
func fakeCLI(kind, providerID, answer string, seen *[]string) *cliAdapter {
	return &cliAdapter{
		d:          cliDescriptors[kind],
		providerID: providerID,
		logger:     slog.New(slog.DiscardHandler),
		run: func(_ context.Context, argv []string, stdin []byte) ([]byte, error) {
			*seen = append(*seen, strings.Join(argv, " ")+string(stdin))
			return []byte(answer), nil
		},
	}
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
	// opened là id của những Provider đã bị HỎI khoá, ghi lại ngay lúc hỏi.
	//
	// Tách khỏi handed vì handed không trả lời được câu đó: router xoá lát cắt nó vừa dùng, nên
	// mọi phần tử trong handed đều là số 0 lúc test đọc tới — một phép so "có khoá của X không"
	// dựa vào chúng thì không bao giờ đỏ được, kể cả khi khoá đã bị mở thật.
	opened []string
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
	c.opened = append(c.opened, providerID)
	out := []byte(c.plain[providerID])
	c.handed = append(c.handed, out)
	return out, nil
}

func (c *fakeCredentials) issued() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.handed...)
}

func (c *fakeCredentials) unlocked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.opened...)
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

// withCLI đăng ký một mắt xích local_cli THẬT (*cliAdapter) thay vì một fakeAdapter, để router nhận
// diện đúng nó là local_cli chứ không phải một Provider HTTP.
func (f *routerFixture) withCLI(providerID, credential string, a *cliAdapter) *routerFixture {
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
	cfg.Claude = func(string) zaloRunner { return f.claude }
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

// Mắt xích BẬT trỏ vào một Provider đang TẮT là hình dạng mà validateLLMRoute không canh được:
// nó chạy lúc lưu chuỗi, còn việc tắt Provider xảy ra sau đó. Không có cửa này thì lượt kế tiếp
// vẫn mở khoá và gửi prompt của khách tới đúng nhà cung cấp vừa bị ngắt.
func TestAppLLMRunnerSkipsEntriesWhoseProviderIsDisabled(t *testing.T) {
	first, off, third := failAdapter(llmErrorUpstream), okAdapter(`{"answer":"off"}`), okAdapter(`{"answer":"c"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(
		entry("openai-1", "gpt-5-mini", true),
		entry("gemini-1", "gemini-2.5-flash", true),
		entry("openrouter-1", "x-ai/grok-4", true),
		entry("claude-code", "haiku", true),
	), claude).
		with("openai-1", "sk-one", first).
		with("gemini-1", "sk-two", off).
		with("openrouter-1", "sk-three", third)

	got, err := f.runner(appLLMRunnerConfig{Disabled: map[string]bool{"gemini-1": true}}).
		Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf("Run() = _, %v; want nil", err)
	}
	if got != `{"answer":"c"}` {
		t.Errorf("Run() = %q; want %q (chuỗi đi tiếp tới mắt xích sau)", got, `{"answer":"c"}`)
	}
	if len(off.seen()) != 0 {
		t.Errorf("adapter của Provider đang tắt được gọi %d lần; want 0", len(off.seen()))
	}
	// Khoá của Provider tắt không được MỞ, chứ không chỉ không được gửi đi: giải mã rồi vứt vẫn
	// là một lần bản rõ nằm trong bộ nhớ vì một Provider mà người trực đã ngắt.
	if got := f.creds.unlocked(); !slices.Equal(got, []string{"openai-1", "openrouter-1"}) {
		t.Errorf("các Provider bị mở khoá = %q; want [openai-1 openrouter-1]", got)
	}
	attempts := f.store.recorded()
	if len(attempts) != 2 {
		t.Fatalf("RecordLLMAttempt gọi %d lần; want 2 (Provider tắt không sinh telemetry)", len(attempts))
	}
	for _, a := range attempts {
		if a.ProviderID == "gemini-1" {
			t.Errorf("telemetry có hàng của Provider đang tắt; want không hàng nào")
		}
	}
	// Cột next_provider_id phải nói đúng nơi lượt ĐÃ đi, không phải mắt xích kế tiếp trên giấy.
	if attempts[0].NextProviderID != "openrouter-1" {
		t.Errorf("attempt[0].NextProviderID = %q; want openrouter-1 (bỏ qua Provider đang tắt)",
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

// TestAppLLMRunnerRefusesAttachmentTurnWithNoLocalCLI canh mặt còn lại của ranh giới an toàn: người
// trực dựng được một chuỗi TOÀN Provider HTTP, không có mắt xích local_cli nào. Một lượt có tệp trên
// chuỗi đó không có đích an toàn — local_cli là nơi DUY NHẤT tệp nằm im trên đĩa máy này — nên nó
// phải trả LỖI, KHÔNG được rơi xuống vòng lặp và gửi đường dẫn/nội dung tệp của khách sang một
// adapter HTTP. Canary claude-tail (TestAppLLMRunnerSendsAttachmentTurnsOnlyToClaude) và first-cli
// (TestAttachmentTurnUsesFirstLocalCLINotHTTP) KHÔNG đi qua nhánh này, nên một hồi quy rơi-xuống-vòng-lặp
// vẫn qua được chúng — chỉ test này canh đúng đường đó.
//
// (Lưu ý §9: một chuỗi codex-only KHÔNG còn thuộc nhánh này — codex là local_cli hợp lệ nên nó PHỤC
// VỤ lượt có tệp, xem TestAttachmentTurnUsesFirstLocalCLINotHTTP. "Không có đích" giờ nghĩa là
// toàn-HTTP.)
func TestAppLLMRunnerRefusesAttachmentTurnWithNoLocalCLI(t *testing.T) {
	const canaryPath = `C:\Users\Admin\.agentdc\zalo-files\CANARY-NOCLI-3c9e.pdf`
	const canaryBytes = "CANARY-BYTES-nocli-noi-dung-rieng-tu-cua-khach"

	prompt := "Khách gửi tệp:\n  " + canaryPath + "\nNội dung đọc được: " + canaryBytes + "\nCâu hỏi: giá bao nhiêu?"
	openai, gemini := okAdapter(`{"answer":"api"}`), okAdapter(`{"answer":"api2"}`)
	claude := okClaude(`{"answer":"claude"}`)
	// Hai Provider HTTP (fakeAdapter), KHÔNG cái nào là *cliAdapter: chuỗi toàn-HTTP, không đích an toàn.
	f := newRouterFixture(newRoute(
		entry("openai-1", "gpt-5-mini", true),
		entry("gemini-1", "gemini-2.5-flash", true),
	), claude).with("openai-1", "sk-one", openai).with("gemini-1", "sk-two", gemini)

	got, err := f.runner(appLLMRunnerConfig{HasAttachments: true}).Run(t.Context(), prompt, f.step)
	if err == nil {
		t.Fatalf("lượt có tệp trên route toàn-HTTP = %q, nil; want lỗi", got)
	}
	if got != "" {
		t.Errorf("Run() = %q; want chuỗi rỗng khi từ chối", got)
	}
	// Không mắt xích local_cli thì không có đích nào cả — cả Claude giả cũng không được gọi.
	if len(claude.seen()) != 0 {
		t.Errorf("claude được gọi %d lần; want 0", len(claude.seen()))
	}
	// Không adapter nào được gọi, không khoá nào được mở: canary không có đường vào một Provider.
	for id, a := range f.adapters {
		if calls := a.(*fakeAdapter).seen(); len(calls) != 0 {
			t.Errorf("adapter %s được gọi %d lần trong lượt có tệp bị từ chối; want 0", id, len(calls))
		}
	}
	if issued := f.creds.issued(); len(issued) != 0 {
		t.Errorf("mở khoá %d lần; want 0 (không gọi Provider thì không cần khoá)", len(issued))
	}
	for _, canary := range []string{jsonForm(t, canaryPath), jsonForm(t, canaryBytes)} {
		if blob := serialize(t, f.adapters); strings.Contains(blob, canary) {
			t.Errorf("đầu vào adapter chứa canary %q trong lượt bị từ chối: %s", canary, blob)
		}
		if blob := serialize(t, f.store.recorded()); strings.Contains(blob, canary) {
			t.Errorf("telemetry chứa canary %q: %s", canary, blob)
		}
	}
}

// TestAttachmentTurnUsesFirstLocalCLINotHTTP là hành vi cốt lõi của §9: một lượt có tệp đi tới mắt
// xích local_cli ĐẦU TIÊN đang bật (codex ở đây), KHÔNG tới Provider HTTP đứng trước nó trong chuỗi.
// Đường dẫn/nội dung tệp của khách ĐƯỢC tới CLI cục bộ (nó đọc trên đĩa máy này) nhưng KHÔNG BAO GIỜ
// tới một adapter HTTP — đó là ranh giới an toàn mà cả thiết kế bảo vệ.
func TestAttachmentTurnUsesFirstLocalCLINotHTTP(t *testing.T) {
	const canaryPath = `C:\Users\Admin\.agentdc\zalo-files\CANARY-FIRSTCLI-1a2b.pdf`
	const canaryBytes = "CANARY-BYTES-firstcli-noi-dung-rieng-tu-cua-khach"
	prompt := "Khách gửi tệp:\n  " + canaryPath + "\nNội dung đọc được: " + canaryBytes + "\nCâu hỏi: giá bao nhiêu?"

	openai := okAdapter(`{"answer":"api"}`)
	var codexSaw []string
	codex := fakeCLI("codex", "codex-1", `{"answer":"codex"}`, &codexSaw)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(
		entry("openai-1", "gpt-5-mini", true),
		entry("codex-1", "gpt-5.4", true),
	), claude).with("openai-1", "sk-one", openai).withCLI("codex-1", "sub-token", codex)

	got, err := f.runner(appLLMRunnerConfig{HasAttachments: true}).Run(t.Context(), prompt, f.step)
	if err != nil {
		t.Fatalf("Run() lượt có tệp trên [openai, codex] = _, %v; want nil", err)
	}
	if got != `{"answer":"codex"}` {
		t.Errorf("Run() = %q; want %q (local_cli đầu tiên phục vụ, không phải HTTP đứng trước)", got, `{"answer":"codex"}`)
	}
	// codex là CLI cục bộ — nó ĐƯỢC thấy tệp; đây là kiểm tra fixture thật sự mang canary tới đích,
	// nếu không thì mọi khẳng định "vắng mặt" dưới đây đều rỗng nghĩa.
	if len(codexSaw) != 1 || !strings.Contains(codexSaw[0], canaryPath) || !strings.Contains(codexSaw[0], canaryBytes) {
		t.Errorf("codex nhận %q; want đúng 1 lượt chứa cả hai canary", codexSaw)
	}
	if len(openai.seen()) != 0 {
		t.Errorf("openai-1 (HTTP) được gọi %d lần trong lượt có tệp; want 0", len(openai.seen()))
	}
	if len(claude.seen()) != 0 {
		t.Errorf("claude được gọi %d lần; want 0 (đích là codex)", len(claude.seen()))
	}
	// Không adapter HTTP nào được gọi thì không khoá nào được mở: đúng như lượt claude — một lượt có
	// tệp không đi qua đường HTTP nên không cần bản rõ nào.
	if issued := f.creds.issued(); len(issued) != 0 {
		t.Errorf("mở khoá %d lần trong lượt có tệp; want 0", len(issued))
	}
	// Ranh giới an toàn: canary không có đường vào đầu vào của adapter HTTP hay vào telemetry.
	for _, canary := range []string{jsonForm(t, canaryPath), jsonForm(t, canaryBytes)} {
		if blob := serialize(t, openai); strings.Contains(blob, canary) {
			t.Errorf("đầu vào adapter HTTP chứa canary %q: %s", canary, blob)
		}
		if blob := serialize(t, f.store.recorded()); strings.Contains(blob, canary) {
			t.Errorf("telemetry chứa canary %q: %s", canary, blob)
		}
	}
	attempts := f.store.recorded()
	if len(attempts) != 1 || attempts[0].ProviderID != "codex-1" || attempts[0].Outcome != store.LLMAttemptOK {
		t.Fatalf("telemetry = %+v; want đúng một hàng codex-1/ok", attempts)
	}
}

// TestAttachmentTurnPicksFirstEligibleLocalCLIInOrder chốt nghĩa của "đầu tiên": chuỗi
// [openai, gemini-cli, codex] cho một lượt có tệp phải chạy mắt xích local_cli ĐẦU TIÊN theo thứ tự
// chuỗi (gemini-cli), KHÔNG phải codex đứng sau — thứ tự quan trọng, không phải một local_cli bất kỳ.
func TestAttachmentTurnPicksFirstEligibleLocalCLIInOrder(t *testing.T) {
	const canaryPath = `C:\Users\Admin\.agentdc\zalo-files\CANARY-ORDER-7e8f.pdf`
	prompt := "Khách gửi tệp:\n  " + canaryPath + "\nCâu hỏi: giá bao nhiêu?"

	openai := okAdapter(`{"answer":"api"}`)
	var geminiSaw, codexSaw []string
	// gemini-cli xuất `-o json` → parseGeminiAnswer trích .response. Dùng ĐÚNG shape đó làm marker
	// (không phải {"answer":...} tuỳ tiện) để fake khớp với parser thật của vendor.
	gemini := fakeCLI("gemini-cli", "gemini-cli-1", `{"response":"gemini"}`, &geminiSaw)
	codex := fakeCLI("codex", "codex-1", `{"answer":"codex"}`, &codexSaw)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(
		entry("openai-1", "gpt-5-mini", true),
		entry("gemini-cli-1", "gemini-2.5-flash", true),
		entry("codex-1", "gpt-5.4", true),
	), claude).with("openai-1", "sk-one", openai).
		withCLI("gemini-cli-1", "sub-gemini", gemini).
		withCLI("codex-1", "sub-codex", codex)

	got, err := f.runner(appLLMRunnerConfig{HasAttachments: true}).Run(t.Context(), prompt, f.step)
	if err != nil {
		t.Fatalf("Run() = _, %v; want nil", err)
	}
	if got != "gemini" {
		t.Errorf("Run() = %q; want %q (local_cli ĐẦU TIÊN, không phải codex sau nó)", got, "gemini")
	}
	if len(geminiSaw) != 1 {
		t.Errorf("gemini-cli nhận %d lượt; want 1 (nó là local_cli đầu tiên)", len(geminiSaw))
	}
	if len(codexSaw) != 0 {
		t.Errorf("codex nhận %d lượt; want 0 (đứng sau gemini-cli nên không được gọi)", len(codexSaw))
	}
	if len(openai.seen()) != 0 {
		t.Errorf("openai-1 (HTTP) được gọi %d lần; want 0", len(openai.seen()))
	}
	attempts := f.store.recorded()
	if len(attempts) != 1 || attempts[0].ProviderID != "gemini-cli-1" || attempts[0].Outcome != store.LLMAttemptOK {
		t.Fatalf("telemetry = %+v; want đúng một hàng gemini-cli-1/ok", attempts)
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

// TestAppLLMRunnerServesACodexOnlyChain chốt tính position-agnostic: một chuỗi KHÔNG có Claude
// Code — toàn gói thuê bao — vẫn chạy qua adapter, không được rơi nhầm vào runClaude. Router cũ
// tách entries[len-1] làm Claude Code, nên với chuỗi này nó gọi fakeClaude thay vì adapter — đúng
// giả định vị trí mà task này gỡ.
func TestAppLLMRunnerServesACodexOnlyChain(t *testing.T) {
	codex := okAdapter(`{"answer":"codex"}`)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(entry("codex-1", "gpt-5.4", true)), claude).
		with("codex-1", "sub-token", codex)

	got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
	if err != nil {
		t.Fatalf("Run() chuỗi codex-only = _, %v; want nil", err)
	}
	if got != `{"answer":"codex"}` {
		t.Errorf("Run() chuỗi codex-only = %q; want %q (adapter, không phải Claude Code)", got, `{"answer":"codex"}`)
	}
	if len(claude.seen()) != 0 {
		t.Errorf("claude được gọi %d lần trong chuỗi không có Claude Code; want 0", len(claude.seen()))
	}
	if calls := codex.seen(); len(calls) != 1 || calls[0].Model != "gpt-5.4" {
		t.Fatalf("codex-1 nhận %+v; want đúng 1 lượt {gpt-5.4}", calls)
	}
	attempts := f.store.recorded()
	if len(attempts) != 1 || attempts[0].ProviderID != "codex-1" || attempts[0].Outcome != store.LLMAttemptOK {
		t.Fatalf("telemetry = %+v; want đúng một hàng codex-1/ok", attempts)
	}
}

// TestAppLLMRunnerCodexOnlyChainReportsUnavailableWhenItFails: một chuỗi codex-only mà mắt xích
// duy nhất hỏng KHÔNG được im lặng trả về rỗng — cả chuỗi cạn thì lượt phải nói ra "không khả dụng".
func TestAppLLMRunnerCodexOnlyChainReportsUnavailableWhenItFails(t *testing.T) {
	codex := failAdapter(llmErrorUpstream)
	claude := okClaude(`{"answer":"claude"}`)
	f := newRouterFixture(newRoute(entry("codex-1", "gpt-5.4", true)), claude).
		with("codex-1", "sub-token", codex)

	got, err := f.runner(appLLMRunnerConfig{}).Run(t.Context(), "câu hỏi", f.step)
	if err == nil {
		t.Fatalf("Run() chuỗi codex-only hỏng = %q, nil; want lỗi không khả dụng", got)
	}
	if got != "" {
		t.Errorf("Run() chuỗi codex-only hỏng = %q; want chuỗi rỗng", got)
	}
	if len(claude.seen()) != 0 {
		t.Errorf("claude được gọi %d lần; want 0 (chuỗi không có Claude Code)", len(claude.seen()))
	}
	attempts := f.store.recorded()
	if len(attempts) != 1 || attempts[0].Outcome != store.LLMAttemptError || attempts[0].FellBack {
		t.Fatalf("telemetry = %+v; want một hàng codex-1/error/fellBack=false", attempts)
	}
}

// --- dây nối thật: appZaloRunner, appClaudeRunner, bootstrap ---

// newAppRouteAPI dựng một api có store THẬT.
//
// Store thật chứ không fakeRouteStore: appZaloRunner là nơi DUY NHẤT appLLMRunnerConfig được ghép
// trong bản chạy, và newAppLLMRunner không canh nil ở Store/Adapters/Claude/Credential. Một test
// dựng config bằng tay sẽ xanh trong khi lượt đầu tiên trên máy người mua panic.
func newAppRouteAPI(t *testing.T) *api {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open(memory) = _, %v; want nil", err)
	}
	t.Cleanup(func() { st.Close() })
	return &api{
		cfg:    config.Config{Dir: t.TempDir()},
		st:     st,
		logger: slog.New(slog.DiscardHandler),
	}
}

// saveClaudeRoute lưu chuỗi một-mắt-xích claude-code/model qua đúng đường ghi Portal dùng.
func saveClaudeRoute(t *testing.T, a *api, model string) {
	t.Helper()
	if err := a.st.AddLLMModel(store.LLMModel{
		ProviderID: "claude-code", ModelID: model, Name: model,
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		t.Fatalf("AddLLMModel(claude-code, %q) = %v; want nil", model, err)
	}
	saveRoute(t, a, store.LLMRouteEntry{ProviderID: "claude-code", ModelID: model, Enabled: true})
}

// saveRoute lưu một chuỗi bất kỳ, giả định model của từng mắt xích đã có sẵn.
func saveRoute(t *testing.T, a *api, entries ...store.LLMRouteEntry) {
	t.Helper()
	snapshot, err := a.st.LLMRoute()
	if err != nil {
		t.Fatalf("LLMRoute() = _, %v; want nil", err)
	}
	if _, err := a.st.ReplaceLLMRoute(snapshot.Revision, entries); err != nil {
		t.Fatalf("ReplaceLLMRoute(%+v) = _, %v; want nil", entries, err)
	}
}

// awaitSignal chờ một TÍN HIỆU, không chờ một khoảng thời gian.
//
// Hạn giờ ở đây chỉ để lỗi đọc được: runner dựng nhầm một execZaloRunner thì nó đi gọi tiến trình
// claude thật, và không có nhánh này thì test treo tới hạn 10 phút của go test rồi đổ ra toàn bộ
// goroutine thay vì một dòng nói đúng chỗ hỏng.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("chờ %s quá 30s; runner không gọi tới base runner đã tiêm vào", what)
	}
}

// blockingClaude dừng giữa lượt để test chèn một lần lưu route vào đúng khoảng đó.
type blockingClaude struct {
	entered chan struct{}
	release chan struct{}
	answer  string
}

func (c *blockingClaude) Run(ctx context.Context, _ string, _ func(string)) (string, error) {
	c.entered <- struct{}{}
	select {
	case <-c.release:
		return c.answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestAppZaloRunnerFollowsTheNewestSavedRoute(t *testing.T) {
	a := newAppRouteAPI(t)
	saveClaudeRoute(t, a, "haiku")

	// zc.Model khớp mắt xích cuối của route A, nên runner GIỮ base này — xem appClaudeRunner. Đó
	// cũng là phép kiểm: chuyền sai model xuống thì base không được gọi và test dừng ở awaitSignal.
	inFlight := &blockingClaude{
		entered: make(chan struct{}, 1), release: make(chan struct{}), answer: "trả lời theo route A",
	}
	runner := a.appZaloRunner(zaloConfig{Model: "haiku"}, inFlight, "t1", false)

	done := make(chan error, 1)
	go func() {
		got, err := runner.Run(t.Context(), "câu hỏi 1", func(string) {})
		if err == nil && got != inFlight.answer {
			err = fmt.Errorf("Run() = %q; want %q", got, inFlight.answer)
		}
		done <- err
	}()
	awaitSignal(t, inFlight.entered, "lượt đang bay vào Claude Code")

	// Lưu ĐÈ giữa lượt: lượt đang bay đã chụp route A và không được đổi sang B.
	saveClaudeRoute(t, a, "opus")
	close(inFlight.release)
	if err := <-done; err != nil {
		t.Fatalf("lượt đang bay: %v", err)
	}
	status, err := a.st.LLMStatus()
	if err != nil {
		t.Fatalf("LLMStatus() = _, %v; want nil", err)
	}
	if status.ActiveProviderID != "claude-code" || status.ActiveModelID != "haiku" {
		t.Errorf("sau lượt đang bay LLMStatus() = %s/%s; want claude-code/haiku (route A)",
			status.ActiveProviderID, status.ActiveModelID)
	}

	// Tin tiếp theo đi theo route B, và daemon KHÔNG khởi động lại giữa hai lượt.
	next := okClaude("trả lời theo route B")
	got, err := a.appZaloRunner(zaloConfig{Model: "opus"}, next, "t1", false).
		Run(t.Context(), "câu hỏi 2", func(string) {})
	if err != nil {
		t.Fatalf("Run() lượt sau khi lưu route B = _, %v; want nil", err)
	}
	if got != "trả lời theo route B" {
		t.Errorf("Run() lượt sau = %q; want %q", got, "trả lời theo route B")
	}
	if status, err = a.st.LLMStatus(); err != nil {
		t.Fatalf("LLMStatus() = _, %v; want nil", err)
	}
	if status.ActiveModelID != "opus" {
		t.Errorf("sau lượt thứ hai LLMStatus().ActiveModelID = %q; want opus (route B)", status.ActiveModelID)
	}
}

// TestAppZaloRunnerKeepsThreadsWithEarlierFilesOffTheAPIChain ghim ranh giới an toàn của tệp.
//
// Tin của LƯỢT NÀY không có tệp, nhưng answerZalo gộp tệp của 10 tin gần nhất vào (mergeZaloFiles)
// và buildConsultPrompt viết đường dẫn TUYỆT ĐỐI cùng tiêu đề khách đặt vào prompt. Cửa chặn tính
// theo mỗi tin đến sẽ để prompt đó đi ra API: Provider không có ổ đĩa nên trả lời mù — đúng hồi
// quy mà mergeZaloFiles sinh ra để chữa — và đường dẫn kèm tên người dùng Windows rời khỏi máy.
func TestAppZaloRunnerKeepsThreadsWithEarlierFilesOffTheAPIChain(t *testing.T) {
	for _, tc := range []struct {
		name        string
		attachments []ipc.ZaloAttachment
		wantClaude  bool
	}{
		{"lịch sử có tệp mở được", []ipc.ZaloAttachment{
			{Kind: "chat.photo", Path: `C:\Users\Admin\.agentdc\zalo-files\anh.jpg`, Title: "đơn thuốc"},
		}, true},
		{"lịch sử không có tệp nào", nil, false},
		// Sticker và vị trí không bao giờ có Path, nên mergeZaloFiles bỏ chúng. Ép đường chậm ở
		// đây là bắt mọi hội thoại có một sticker phải đi Claude Code tới hết đời luồng.
		{"lịch sử chỉ có sticker", []ipc.ZaloAttachment{{Kind: "chat.sticker"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAppRouteAPI(t)
			// Provider API cố ý CHƯA có khoá: cửa chặn hỏng thì lượt dừng ở bước mở khoá, chứ
			// không gửi đường dẫn tệp của khách tới một máy chủ thật.
			if err := a.st.CreateLLMProvider(store.LLMProvider{
				ID: "openai-1", Name: "openai-1", Kind: "openai", Enabled: true,
			}); err != nil {
				t.Fatalf("CreateLLMProvider(openai-1) = %v; want nil", err)
			}
			for _, m := range []store.LLMModel{
				{ProviderID: "openai-1", ModelID: "gpt-5-mini", Name: "gpt-5-mini", Source: store.LLMModelManual, Available: true},
				{ProviderID: "claude-code", ModelID: "haiku", Name: "haiku", Source: store.LLMModelManual, Available: true},
			} {
				if err := a.st.AddLLMModel(m); err != nil {
					t.Fatalf("AddLLMModel(%s/%s) = %v; want nil", m.ProviderID, m.ModelID, err)
				}
			}
			saveRoute(t, a,
				store.LLMRouteEntry{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true},
				store.LLMRouteEntry{ProviderID: "claude-code", ModelID: "haiku", Enabled: true},
			)
			if err := a.st.UpsertZaloThread("t1", "Khách lẻ"); err != nil {
				t.Fatalf("UpsertZaloThread(t1) = %v; want nil", err)
			}
			if err := a.st.AddZaloMessage(ipc.ZaloMessage{
				ThreadID: "t1", Direction: ipc.ZaloIn, Body: "ảnh đây ạ",
				Attachments: tc.attachments, CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("AddZaloMessage(t1) = %v; want nil", err)
			}

			claude := okClaude("claude đọc được tệp")
			// hasNewFiles = false: tin của lượt này chỉ có chữ.
			got, err := a.appZaloRunner(zaloConfig{Model: "haiku"}, claude, "t1", false).
				Run(t.Context(), "trong hình là gì", func(string) {})

			if !tc.wantClaude {
				// Không có tệp thì chuỗi API PHẢI được đi vào — và nó dừng ở mắt xích đầu vì
				// Provider chưa có khoá. Đó là bằng chứng lượt không nhảy thẳng tới Claude Code.
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("Run() = %q, %v; want lỗi mở khoá openai-1 (chuỗi API được đi vào)", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run() = _, %v; want nil", err)
			}
			if got != "claude đọc được tệp" {
				t.Errorf("Run() = %q; want câu trả lời của Claude Code", got)
			}
			status, serr := a.st.LLMStatus()
			if serr != nil {
				t.Fatalf("LLMStatus() = _, %v; want nil", serr)
			}
			if status.ActiveProviderID != "claude-code" {
				t.Errorf("LLMStatus().ActiveProviderID = %q; want claude-code", status.ActiveProviderID)
			}
		})
	}
}

func TestAppZaloRunnerKeepsAnsweringWithoutASavedRoute(t *testing.T) {
	a := newAppRouteAPI(t)
	base := okClaude("trả lời như trước khi có định tuyến")

	// Chưa gieo chuỗi nào. Router coi chuỗi rỗng là lỗi, nên không có cửa chặn trong
	// appZaloRunner thì mọi tin của khách rơi vào im lặng cho tới khi có người mở Portal.
	got, err := a.appZaloRunner(zaloConfig{Model: "haiku"}, base, "t1", false).
		Run(t.Context(), "câu hỏi", func(string) {})
	if err != nil {
		t.Fatalf("Run() khi chưa có chuỗi = _, %v; want nil", err)
	}
	if got != "trả lời như trước khi có định tuyến" {
		t.Errorf("Run() khi chưa có chuỗi = %q; want câu trả lời của base runner", got)
	}
}

// Cửa nối giữa hai tầng: router biết bỏ qua Provider đang tắt, nhưng chỉ appZaloRunner mới biết
// Provider NÀO đang tắt. Kiểm riêng từng tầng thì cả hai xanh trong khi dây nối giữa chúng đứt.
//
// Test này KHÔNG chạm mạng, và đó chính là thứ khiến nó nói được điều gì đó: openai-1 chưa có
// khoá, nên nếu cửa tắt không được chuyền xuống, Run dừng ngay ở bước mở khoá và trả về lỗi. Xanh
// nghĩa là lượt đã bỏ qua mắt xích đó và rơi xuống lưới an toàn.
func TestAppZaloRunnerSkipsAProviderTurnedOffAfterTheRouteWasSaved(t *testing.T) {
	a := newAppRouteAPI(t)
	if err := a.st.CreateLLMProvider(store.LLMProvider{
		ID: "openai-1", Name: "OpenAI", Kind: "openai", Enabled: true,
	}); err != nil {
		t.Fatalf("CreateLLMProvider(openai-1) = %v; want nil", err)
	}
	for _, m := range []store.LLMModel{
		{ProviderID: "openai-1", ModelID: "gpt-5-mini", Name: "gpt-5-mini"},
		{ProviderID: "claude-code", ModelID: "haiku", Name: "haiku"},
	} {
		m.Source, m.Available = store.LLMModelManual, true
		if err := a.st.AddLLMModel(m); err != nil {
			t.Fatalf("AddLLMModel(%s/%s) = %v; want nil", m.ProviderID, m.ModelID, err)
		}
	}
	saveRoute(t, a,
		store.LLMRouteEntry{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true},
		store.LLMRouteEntry{ProviderID: "claude-code", ModelID: "haiku", Enabled: true},
	)

	// Đúng thao tác của người trực khi một khoá bị lộ: tắt Provider, KHÔNG đụng vào chuỗi. Mắt
	// xích openai-1 vẫn ở đó và vẫn mang Enabled = true.
	if err := a.st.UpdateLLMProvider(store.LLMProvider{
		ID: "openai-1", Name: "OpenAI", Enabled: false,
	}); err != nil {
		t.Fatalf("UpdateLLMProvider(openai-1, enabled=false) = %v; want nil", err)
	}

	const answer = "trả lời từ lưới an toàn"
	got, err := a.appZaloRunner(zaloConfig{Model: "haiku"}, okClaude(answer), "t1", false).
		Run(t.Context(), "câu hỏi", func(string) {})
	if err != nil {
		t.Fatalf("Run() sau khi tắt openai-1 = _, %v; want nil (bỏ qua mắt xích, đi tiếp)", err)
	}
	if got != answer {
		t.Errorf("Run() sau khi tắt openai-1 = %q; want %q", got, answer)
	}
	status, err := a.st.LLMStatus()
	if err != nil {
		t.Fatalf("LLMStatus() = _, %v; want nil", err)
	}
	if status.ActiveProviderID != "claude-code" {
		t.Errorf("LLMStatus().ActiveProviderID = %q; want claude-code", status.ActiveProviderID)
	}
	if status.Attempts != 1 {
		t.Errorf("LLMStatus().Attempts = %d; want 1 (Provider đã tắt không sinh lượt gọi nào)",
			status.Attempts)
	}
}

func TestAppClaudeRunnerCarriesTheRouteModel(t *testing.T) {
	a := newAppRouteAPI(t)
	base := okClaude("base")
	zc := zaloConfig{Model: "haiku", WorkDir: `C:\work`, ThinkingTokens: 512}

	for _, tc := range []struct {
		name     string
		model    string
		wantBase bool
	}{
		{"model của route trùng cấu hình đang chạy", "haiku", true},
		{"model của route khác cấu hình đang chạy", "opus", false},
		// Hàng bị sửa tay trong database: `claude --model ""` là dòng lệnh hỏng, còn cấu hình
		// đang chạy thì vẫn trả lời được.
		{"model rỗng giữ cấu hình đang chạy", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := a.appClaudeRunner(zc, base, tc.model)
			if tc.wantBase {
				// Giữ base là điều kiện để test upstream (duty_test.go) còn tiêm được runner giả:
				// dựng mới ở đây biến chúng thành lượt gọi tiến trình claude thật.
				if got != zaloRunner(base) {
					t.Fatalf("appClaudeRunner(zc, base, %q) = %T; want chính base đã tiêm vào", tc.model, got)
				}
				return
			}
			fresh, ok := got.(execZaloRunner)
			if !ok {
				t.Fatalf("appClaudeRunner(zc, base, %q) = %T; want execZaloRunner", tc.model, got)
			}
			if fresh.cfg.Model != tc.model {
				t.Errorf("appClaudeRunner(zc, base, %q).cfg.Model = %q; want %q",
					tc.model, fresh.cfg.Model, tc.model)
			}
			// Chỉ Model đổi: phần còn lại của cấu hình lượt dựng ra dòng lệnh claude, và mất nó
			// thì lượt chạy sai thư mục hoặc sai hạn suy nghĩ.
			if fresh.cfg.WorkDir != zc.WorkDir || fresh.cfg.ThinkingTokens != zc.ThinkingTokens {
				t.Errorf("appClaudeRunner(zc, base, %q).cfg workdir/thinking = %q/%d; want %q/%d",
					tc.model, fresh.cfg.WorkDir, fresh.cfg.ThinkingTokens, zc.WorkDir, zc.ThinkingTokens)
			}
		})
	}
	if zc.Model != "haiku" {
		t.Errorf("zaloConfig của người gọi bị sửa: Model = %q; want haiku", zc.Model)
	}
}

func TestAppLLMAdaptersComeFromTheStoredProviderKind(t *testing.T) {
	a := newAppRouteAPI(t)
	for id, kind := range map[string]string{
		"o-1": "openai", "a-1": "anthropic", "g-1": "gemini", "r-1": "openrouter", "x-1": "chưa hỗ trợ",
	} {
		if err := a.st.CreateLLMProvider(store.LLMProvider{
			ID: id, Name: id, Kind: kind, Enabled: id != "a-1",
		}); err != nil {
			t.Fatalf("CreateLLMProvider(%q, %q) = %v; want nil", id, kind, err)
		}
	}

	adapters, disabled, err := a.appLLMAdapters()
	if err != nil {
		t.Fatalf("appLLMAdapters() = _, _, %v; want nil", err)
	}
	// a-1 đang tắt nhưng VẪN có adapter: hai trạng thái này khác nhau ở chỗ router xử lý chúng
	// khác nhau — thiếu adapter là cấu hình hỏng và chuỗi dừng, còn đang tắt thì chuỗi đi tiếp.
	if !disabled["a-1"] {
		t.Errorf("appLLMAdapters() disabled[a-1] = false; want true (Provider đang tắt)")
	}
	for _, id := range []string{"o-1", "g-1", "r-1", "claude-code"} {
		if disabled[id] {
			t.Errorf("appLLMAdapters() disabled[%q] = true; want false", id)
		}
	}

	for _, tc := range []struct {
		id   string
		want providerAdapter
	}{
		{"o-1", (*openAIAdapter)(nil)},
		{"a-1", (*anthropicAdapter)(nil)},
		{"g-1", (*geminiAdapter)(nil)},
		{"r-1", (*openRouterAdapter)(nil)},
	} {
		got, ok := adapters[tc.id]
		if !ok {
			t.Errorf("appLLMAdapters()[%q] thiếu; want %T", tc.id, tc.want)
			continue
		}
		if fmt.Sprintf("%T", got) != fmt.Sprintf("%T", tc.want) {
			t.Errorf("appLLMAdapters()[%q] = %T; want %T", tc.id, got, tc.want)
		}
	}
	// Claude Code không đi qua HTTP, và một kind lạ là cấu hình sai: cả hai KHÔNG được có adapter,
	// để router dừng chuỗi và nói ra thay vì gọi nhầm giao thức.
	for _, id := range []string{"claude-code", "x-1"} {
		if _, ok := adapters[id]; ok {
			t.Errorf("appLLMAdapters()[%q] có adapter; want không có", id)
		}
	}
}

func TestAppLLMCredentialUnlocksExactlyOneProvider(t *testing.T) {
	requireCredentialProtection(t)
	a := newAppRouteAPI(t)
	for _, id := range []string{"o-1", "o-2"} {
		if err := a.st.CreateLLMProvider(store.LLMProvider{
			ID: id, Name: id, Kind: "openai", Enabled: true,
		}); err != nil {
			t.Fatalf("CreateLLMProvider(%q) = %v; want nil", id, err)
		}
	}
	key := []byte("sk-runtime-wiring")
	cipher, err := protectProviderSecret(key)
	if err != nil {
		t.Fatalf("protectProviderSecret() = _, %v; want nil", err)
	}
	if err := a.st.SetLLMCredentialCipher("o-1", cipher); err != nil {
		t.Fatalf("SetLLMCredentialCipher(o-1) = %v; want nil", err)
	}

	got, err := a.appLLMCredential("o-1")
	if err != nil {
		t.Fatalf("appLLMCredential(o-1) = _, %v; want nil", err)
	}
	if !bytes.Equal(got, key) {
		t.Errorf("appLLMCredential(o-1) = %q; want %q", got, key)
	}
	// Bản rõ phải MỚI mỗi lượt: router xoá lát cắt nó nhận, nên dùng chung mảng nền nghĩa là lượt
	// thứ hai phát ra toàn số 0 và Provider trả 401 mà không ai hiểu vì sao.
	again, err := a.appLLMCredential("o-1")
	if err != nil {
		t.Fatalf("appLLMCredential(o-1) lần hai = _, %v; want nil", err)
	}
	clear(again)
	if !bytes.Equal(got, key) {
		t.Errorf("xoá bản rõ lượt hai làm hỏng lượt một: %q; want %q", got, key)
	}
	// Chưa nhập khoá là một lỗi NÓI RA, không phải một lát cắt rỗng gửi đi làm Provider trả 401.
	if plain, err := a.appLLMCredential("o-2"); err == nil {
		t.Errorf("appLLMCredential(o-2) khi chưa có khoá = %q, nil; want lỗi", plain)
	}
}

// --- nghiệm thu xuyên tầng ---

// TestAppLLMRunnerFallsBackOverRealHTTP là lượt nghiệm thu xuyên tầng: store thật, adapter thật,
// HTTP thật, và trạng thái đọc lại từ chính database ngay sau khi Run trả về.
//
// Mọi test bên trên tiêm fakeAdapter, nên chúng bỏ qua đúng hai mắt xích mà một lượt fallback
// thật đi qua: 429 trên dây biến thành llmErrorRateLimit (classifyStatus), và thân JSON của
// Provider biến thành câu trả lời. Ghép sai một trong hai thì chuỗi vẫn xanh trong test đơn vị và
// vẫn im lặng trên máy người mua.
//
// Phân loại lỗi đi tới tận LLMStatus.LastErrorKind, nên test này chốt luôn ánh xạ 429 →
// rate_limit trên đường thật: một Portal chỉ đếm được "2 lượt / 1 lần né" thì không nói được cho
// người trực biết phải sửa gì. Bảng classifyStatus trong app_llm_http_test.go canh phần còn lại
// của ánh xạ; ở đây là mắt xích nối nó với bề mặt /llm/status.
func TestAppLLMRunnerFallsBackOverRealHTTP(t *testing.T) {
	// Hình dạng answerZalo đòi ở đầu ra của runner. Nhánh clarify là nhánh DUY NHẤT được ra ngoài
	// mà không cần trích dẫn, nên đây là câu trả lời hợp lệ ngắn nhất — test này nghiệm thu đường
	// đi của một lượt, không nghiệm thu phần soát trích dẫn của upstream.
	const answer = `{"clarify":"Dạ mình cần tư vấn phần nào ạ?"}`

	rateLimited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"rate limit"}}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(rateLimited.Close)

	answering := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Kiểm ngay trong handler chứ không cất lại rồi kiểm sau: handler chạy trên goroutine của
		// httptest còn phần kiểm chạy trên goroutine của test, và t.Errorf là thứ duy nhất trong
		// hai cách đó an toàn với -race mà không phải dựng thêm một cái khoá.
		if got, want := r.Header.Get("Authorization"), "Bearer "+llmPackageCanary; got != want {
			t.Errorf("Provider thứ hai nhận Authorization = %q; want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(w,
			`{"output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`,
			answer); err != nil {
			t.Errorf("ghi phản hồi giả = %v; want nil", err)
		}
	}))
	t.Cleanup(answering.Close)

	a := newAppRouteAPI(t)
	client := &http.Client{Timeout: 10 * time.Second}
	adapters := map[string]providerAdapter{}
	var route []store.LLMRouteEntry
	for _, p := range []struct{ id, model, base string }{
		{"openai-1", "gpt-5-mini", rateLimited.URL},
		{"openai-2", "gpt-5", answering.URL},
	} {
		if err := a.st.CreateLLMProvider(store.LLMProvider{
			ID: p.id, Name: p.id, Kind: "openai", Enabled: true,
		}); err != nil {
			t.Fatalf("CreateLLMProvider(%q) = %v; want nil", p.id, err)
		}
		if err := a.st.AddLLMModel(store.LLMModel{
			ProviderID: p.id, ModelID: p.model, Name: p.model,
			Source: store.LLMModelManual, Available: true,
		}); err != nil {
			t.Fatalf("AddLLMModel(%s/%s) = %v; want nil", p.id, p.model, err)
		}
		// Constructor sản xuất không nhận base — endpoint là hằng của Provider, không phải một ô
		// nhập — nên test trỏ nó vào máy chủ giả bằng cách đổi trường, y như app_llm_http_test.go.
		adapter := newOpenAIAdapter(p.id, client)
		adapter.base = p.base
		adapters[p.id] = adapter
		route = append(route, store.LLMRouteEntry{ProviderID: p.id, ModelID: p.model, Enabled: true})
	}
	if err := a.st.AddLLMModel(store.LLMModel{
		ProviderID: "claude-code", ModelID: "haiku", Name: "haiku",
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		t.Fatalf("AddLLMModel(claude-code/haiku) = %v; want nil", err)
	}
	route = append(route, store.LLMRouteEntry{ProviderID: "claude-code", ModelID: "haiku", Enabled: true})
	saveRoute(t, a, route...)

	claude := okClaude(`{"clarify":"Claude Code không được gọi trong lượt này"}`)
	runner := newAppLLMRunner(appLLMRunnerConfig{
		Store:    a.st,
		Adapters: adapters,
		Claude:   func(string) zaloRunner { return claude },
		// Bản rõ MỚI mỗi lượt: router xoá lát cắt nó vừa dùng, nên một lát cắt dùng chung sẽ phát
		// ra toàn số 0 từ Provider thứ hai và test đỏ vì một lý do không liên quan.
		Credential: func(string) ([]byte, error) { return []byte(llmPackageCanary), nil },
		Logger:     slog.New(slog.DiscardHandler),
	})

	got, err := runner.Run(t.Context(), "khách hỏi giá combo", func(string) {})
	if err != nil {
		t.Fatalf("Run() qua hai Provider HTTP = _, %v; want nil", err)
	}
	if got != answer {
		t.Errorf("Run() qua hai Provider HTTP = %q; want %q", got, answer)
	}
	if len(claude.seen()) != 0 {
		t.Errorf("claude được gọi %d lần khi Provider thứ hai trả lời được; want 0", len(claude.seen()))
	}

	// Đọc NGAY sau khi Run trả về. Portal lấy trạng thái bằng một request khác, và không có gì
	// đồng bộ hai đường đó — telemetry chưa nằm trong database lúc Run kết thúc thì trang Models
	// hiện một Provider đang phục vụ khác với Provider vừa trả lời.
	status, err := a.st.LLMStatus()
	if err != nil {
		t.Fatalf("LLMStatus() = _, %v; want nil", err)
	}
	if status.ActiveProviderID != "openai-2" || status.ActiveModelID != "gpt-5" {
		t.Errorf("LLMStatus() provider/model = %s/%s; want openai-2/gpt-5",
			status.ActiveProviderID, status.ActiveModelID)
	}
	if status.Attempts != 2 || status.Fallbacks != 1 {
		t.Errorf("LLMStatus() attempts/fallbacks = %d/%d; want 2/1 (một lượt 429 rồi một lượt được)",
			status.Attempts, status.Fallbacks)
	}
	if status.LastSuccessAt == nil {
		t.Errorf("LLMStatus().LastSuccessAt = nil; want thời điểm của lượt thành công")
	}
	// 429 trên dây phải đi hết đường tới bề mặt người trực đọc, không dừng lại ở cột database.
	if status.LastErrorKind != string(llmErrorRateLimit) || status.LastErrorProviderID != "openai-1" {
		t.Errorf("LLMStatus() lastError kind/provider = %q/%q; want rate_limit/openai-1",
			status.LastErrorKind, status.LastErrorProviderID)
	}
}
