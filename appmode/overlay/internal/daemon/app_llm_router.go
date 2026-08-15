package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"agentdc/internal/agent"
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

// claudeCodeProviderID định danh mắt xích Claude Code — mắt xích DUY NHẤT router chạy qua runClaude
// (ngân sách lượt dài) thay vì qua một adapter với ngân sách 25s. Là hằng của một id Provider cố
// định (hàng seeded bởi migration), không phải kind. Task 11 gộp Claude Code thành một adapter
// local_cli bình thường và gỡ nhánh đặc biệt này.
const claudeCodeProviderID = "claude-code"

// llmRouteTelemetryStore là phần store mà một runner đã dựng còn được phép dùng.
// Route đã nằm trong cfg.Route; thu hẹp interface thành chỉ ghi telemetry khiến một lần đọc
// database thứ hai trong Run không thể vô tình quay lại mà không đổi contract này.
type llmRouteTelemetryStore interface {
	RecordLLMAttempt(store.LLMAttempt) error
}

var _ llmRouteTelemetryStore = (*store.Store)(nil)

// appLLMRunnerConfig là toàn bộ dây nối của một lượt định tuyến.
type appLLMRunnerConfig struct {
	// Route is the immutable route snapshot captured while constructing this
	// turn's runner. Store remains below only for attempt telemetry.
	Route    store.LLMRouteSnapshot
	Store    llmRouteTelemetryStore
	Adapters map[string]providerAdapter
	// Claude dựng mắt xích cuối cho ĐÚNG model mà snapshot của lượt này nêu tên.
	//
	// Là hàm chứ không phải một runner dựng sẵn vì model của mắt xích cuối nằm TRONG snapshot, và
	// snapshot đã được chụp trước khi dựng runner. Một lần đọc khác trong Run sẽ tạo khe đua: route
	// dùng để qua cửa cấu hình có thể khác route thật sự được gọi và ghi telemetry.
	//
	// Luôn có mặt: appZaloRunner luôn tiêm nó (validateLLMRoute chỉ đòi mắt xích cuối đang bật —
	// chuỗi không kết thúc bằng Claude Code vẫn hợp lệ), nên router không cần nhánh "nếu không còn gì".
	Claude func(model string) zaloRunner
	// ClaudeForSession resolves the terminal Claude runner before session
	// selection. The current binding is advisory: implementations reuse it only
	// while that account remains enabled, otherwise choosing a replacement.
	ClaudeForSession func(
		ctx context.Context,
		model string,
		current *store.ZaloCLISession,
	) (zaloRunner, appZaloClaudeBinding)
	// Disabled là id của những Provider API đang TẮT, và nó là công tắc ngắt SỐNG.
	//
	// Cờ Enabled trên mắt xích route không thay được nó: hợp lệ hoá chỉ chạy lúc LƯU chuỗi, nên
	// tắt một Provider sau đó để lại một mắt xích vẫn bật trỏ vào nó. Không có tập này thì lượt
	// kế tiếp vẫn giải mã khoá và gửi prompt của khách tới đúng nhà cung cấp mà người trực vừa
	// tắt — và lý do người ta tắt thường là khoá đã lộ.
	//
	// nil hợp lệ: đọc một map nil trả về false, nghĩa là "không Provider nào bị tắt".
	Disabled map[string]bool
	// Credential trả về BẢN RÕ MỚI cho mỗi lượt gọi. Router xoá nó ngay sau lượt gọi, nên một
	// hiện thực trả về lát cắt dùng chung sẽ tự bắn vào chân mình ở lượt sau.
	Credential func(providerID string) ([]byte, error)
	// HasAttachments là ranh giới an toàn, không phải một tuỳ chọn: lượt có tệp hoặc ảnh chỉ
	// được đi tới local CLI, nơi tệp nằm im trên đĩa máy này.
	HasAttachments bool
	// RequiresStructuredClaude is set only when the captured resumable session
	// delta contains an attachment. A stateless local CLI can answer, but cannot
	// consume that durable delta cursor, so selecting it would starve the backlog.
	RequiresStructuredClaude bool
	PerProviderTimeout       time.Duration
	APIChainTimeout          time.Duration
	Logger                   *slog.Logger
}

// appLLMRunner chạy một lượt trả lời qua chuỗi fallback đã lưu.
type appLLMRunner struct {
	cfg appLLMRunnerConfig
}

type appLLMRouteInput struct {
	statelessPrompt string
	structured      *appZaloSessionRunInput
}

type appZaloClaudeBindingState uint8

const (
	appZaloClaudeBindingUnresolved appZaloClaudeBindingState = iota
	appZaloClaudeBindingUnavailable
	appZaloClaudeBindingSelected
)

type appZaloClaudeBinding struct {
	State     appZaloClaudeBindingState
	AccountID string
	ConfigDir string
	Model     string
}

type appZaloSessionRouteResolver interface {
	appResolveZaloSessionRoute(
		ctx context.Context,
		current *store.ZaloCLISession,
	) (zaloRunner, appZaloClaudeBinding)
}

var (
	_ zaloRunner              = (*appLLMRunner)(nil)
	_ appZaloStructuredRunner = (*appLLMRunner)(nil)
)

func newAppLLMRunner(cfg appLLMRunnerConfig) *appLLMRunner {
	// Entries là slice do store sở hữu. Clone tại ranh giới constructor để mọi
	// thay đổi/lần lưu sau chỉ áp dụng cho runner của lượt kế tiếp.
	cfg.Route.Entries = slices.Clone(cfg.Route.Entries)
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

// appOnboardingNoopAttemptStore keeps Test Chat outside production telemetry.
// The router still follows its normal attempt-recording semantics, but this
// isolated sink deliberately commits nothing.
type appOnboardingNoopAttemptStore struct{}

func (appOnboardingNoopAttemptStore) RecordLLMAttempt(store.LLMAttempt) error { return nil }

type appOnboardingCLIProcess func(
	context.Context,
	*exec.Cmd,
	[]byte,
	chan<- int,
	*slog.Logger,
) ([]byte, error)

var appOnboardingRunCLIProcess appOnboardingCLIProcess = runCLIProcess

type appOnboardingClaudeRunner struct {
	cfg    zaloConfig
	logger *slog.Logger
}

// appOnboardingPinnedClaudeRunner receives the exact validated staging Account
// directly. There is no round-robin or live-session selector on this path.
// Unlike the legacy live runner, this stateless onboarding path delegates child
// ownership to runCLIProcess so cancellation tears down the managed process tree.
func appOnboardingPinnedClaudeRunner(
	base zaloConfig,
	staged store.OnboardingStagingAccount,
	model string,
	program string,
	prefixArgs []string,
	logger *slog.Logger,
) zaloRunner {
	isolated := zaloConfig{
		KBRoots:           slices.Clone(base.KBRoots),
		WorkDir:           base.WorkDir,
		ThinkingTokens:    base.ThinkingTokens,
		Model:             model,
		ConfigDir:         staged.ConfigDir,
		Program:           program,
		ProgramPrefixArgs: slices.Clone(prefixArgs),
	}
	return &appOnboardingClaudeRunner{cfg: isolated, logger: logger}
}

func (r *appOnboardingClaudeRunner) Run(
	ctx context.Context,
	prompt string,
	step func(string),
) (string, error) {
	profile := agent.ConsultReadOnly()
	args, err := appOnboardingClaudeArgv(profile, r.cfg)
	if err != nil {
		return "", err
	}
	args = append(slices.Clone(r.cfg.ProgramPrefixArgs), args...)
	for _, arg := range args {
		if appOnboardingClaudeArgIsStateful(arg) {
			return "", errors.New("staged Claude command contains session persistence")
		}
	}
	cmd := exec.Command(r.cfg.Program, args...)
	cmd.Dir = r.cfg.WorkDir
	cmd.Env = profile.Env(os.Environ())
	cmd.Env = appZaloSetEnv(cmd.Env, "CLAUDE_CONFIG_DIR", r.cfg.ConfigDir)
	if r.cfg.ThinkingTokens > 0 {
		cmd.Env = appZaloSetEnv(cmd.Env, "MAX_THINKING_TOKENS", fmt.Sprint(r.cfg.ThinkingTokens))
	}

	step("agent bắt đầu đọc knowledge base")
	out, err := appOnboardingRunCLIProcess(ctx, cmd, []byte(prompt), nil, r.logger)
	if err != nil {
		return "", fmt.Errorf("run staged Claude: %w", err)
	}

	var answer string
	var sawResult bool
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event struct {
			Type    string `json:"type"`
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
		}
		if err := json.Unmarshal(line, &event); err != nil || event.Type == "" {
			return "", errors.New("staged Claude returned an invalid structured event")
		}
		progress, result, isResult := parseStreamLine(line)
		if progress != "" {
			step(progress)
		}
		if isResult {
			if sawResult || event.IsError {
				return "", errors.New("staged Claude returned an invalid result event")
			}
			answer, sawResult = sanitizeOnboardingClaudeResult(result)
			if !sawResult {
				return "", errors.New("staged Claude returned an empty or unsafe result")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read staged Claude output: %w", err)
	}
	if !sawResult {
		return "", errors.New("staged Claude returned no result event")
	}
	return answer, nil
}

func appOnboardingClaudeArgv(profile agent.Profile, cfg zaloConfig) ([]string, error) {
	args := make([]string, 0, len(profile.HeadlessArgs)+2+len(cfg.KBRoots)*2)
	for index := 0; index < len(profile.HeadlessArgs); index++ {
		arg := profile.HeadlessArgs[index]
		if arg == "--session-id" {
			if index+1 >= len(profile.HeadlessArgs) || profile.HeadlessArgs[index+1] != "{SID}" {
				return nil, errors.New("read-only Claude profile has an unsafe session-id shape")
			}
			index++
			continue
		}
		if strings.Contains(arg, "{SID}") || appOnboardingClaudeArgIsStateful(arg) {
			return nil, errors.New("read-only Claude profile contains session persistence")
		}
		args = append(args, arg)
	}
	args = append(args, "--no-session-persistence")
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if len(cfg.KBRoots) == 0 {
		return nil, errors.New("no knowledge-base roots configured")
	}
	for _, root := range cfg.KBRoots {
		args = append(args, "--add-dir", root)
	}
	return args, nil
}

func appOnboardingClaudeArgIsStateful(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	switch name {
	case "--session-id", "--resume", "-r", "--continue", "-c", "--fork-session":
		return true
	default:
		return false
	}
}

func sanitizeOnboardingClaudeResult(result string) (string, bool) {
	if !utf8.ValidString(result) {
		return "", false
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return "", false
	}
	for _, value := range result {
		if unicode.IsControl(value) && value != '\n' && value != '\r' && value != '\t' {
			return "", false
		}
	}
	return result, true
}

// Run trả lời một lượt bằng snapshot đã chụp khi dựng runner.
//
// Một lần lưu route sau constructor vì thế không đổi đường đi của lượt đang chạy; runner/lượt kế
// tiếp sẽ chụp cấu hình mới.
func (r *appLLMRunner) Run(ctx context.Context, prompt string, step func(string)) (string, error) {
	result, err := r.run(ctx, appLLMRouteInput{statelessPrompt: prompt}, step)
	return result.Answer, err
}

func (r *appLLMRunner) appRunZaloSession(
	ctx context.Context,
	in appZaloSessionRunInput,
	step func(string),
) (appZaloRunResult, error) {
	statelessPrompt := in.StatelessPrompt
	if statelessPrompt == "" {
		statelessPrompt = in.Prompt
	}
	return r.run(ctx, appLLMRouteInput{
		statelessPrompt: statelessPrompt,
		structured:      &in,
	}, step)
}

func (r *appLLMRunner) appWithZaloAttachments(hasAttachments bool) zaloRunner {
	cloned := *r
	cloned.cfg.HasAttachments = hasAttachments
	return &cloned
}

func (r *appLLMRunner) appWithZaloStructuredClaudeRequirement(required bool) zaloRunner {
	cloned := *r
	cloned.cfg.RequiresStructuredClaude = required
	return &cloned
}

func (r *appLLMRunner) appResolveZaloSessionRoute(
	ctx context.Context,
	current *store.ZaloCLISession,
) (zaloRunner, appZaloClaudeBinding) {
	entry, ok := findClaudeEntry(r.cfg.Route.Entries)
	if !ok || r.cfg.ClaudeForSession == nil {
		return r, appZaloClaudeBinding{}
	}
	claude, binding := r.cfg.ClaudeForSession(ctx, entry.ModelID, current)
	if binding.Model == "" {
		binding.Model = entry.ModelID
	}
	cloned := *r
	cloned.cfg.Claude = func(string) zaloRunner { return claude }
	cloned.cfg.ClaudeForSession = nil
	return &cloned, binding
}

// run is the single route-policy loop used by both the legacy stateless seam
// and the structured Zalo session seam. Only the prompt/execution boundary
// differs: APIs and non-Claude CLIs receive statelessPrompt, while Claude gets
// the structured session input when one is present.
func (r *appLLMRunner) run(
	ctx context.Context,
	in appLLMRouteInput,
	step func(string),
) (appZaloRunResult, error) {
	snapshot := r.cfg.Route
	entries := slices.Clone(snapshot.Entries)
	if len(entries) == 0 {
		return appZaloRunResult{}, errors.New("llm route: chuỗi fallback rỗng, không có gì để gọi")
	}
	// A captured pending attachment belongs to structured session state. API and
	// non-Claude local CLI paths receive only the stateless bootstrap prompt and
	// cannot advance its cursor, so trying either would rediscover the same row on
	// every future turn. Select one enabled Claude entry directly or fail closed.
	if in.structured != nil && r.cfg.RequiresStructuredClaude {
		e, ok := findClaudeEntry(entries)
		if !ok {
			return appZaloRunResult{}, newLLMError(llmErrorRequest, nil,
				"llm route: pending session attachment requires an enabled Claude Code entry")
		}
		return r.runClaude(ctx, e, in, step)
	}
	// Lượt có tệp chỉ đi tới một mắt xích local_cli — nơi tệp nằm im trên đĩa máy này (codex,
	// gemini-cli đều chạy chỉ-đọc, và claude-code). Chọn cái ĐẦU TIÊN đủ điều kiện ở bất kỳ vị trí
	// nào; không có thì lượt này không phục vụ được — KHÔNG để đường dẫn/nội dung tệp của khách chạm
	// một adapter HTTP. Đó là ranh giới an toàn mà cả thiết kế bảo vệ.
	if r.cfg.HasAttachments {
		e, ok := r.firstEligibleCLIEntry(entries)
		if !ok {
			return appZaloRunResult{}, newLLMError(llmErrorRequest, nil,
				"llm route: lượt có tệp nhưng chuỗi không có mắt xích local_cli")
		}
		// claude-code giữ NGUYÊN đường runClaude (mang step gốc, ngân sách lượt dài) — Task 11 sở hữu
		// path đó. codex/gemini-cli chạy qua adapter, cũng dưới ctx CHA: một lượt CLI đọc tệp là một
		// lượt thật, KHÔNG phải một API treo 25s. Cả hai đều terminal — một mắt xích, không fallback.
		if e.ProviderID == claudeCodeProviderID {
			return r.runClaude(ctx, e, in, step)
		}
		return r.runCLIAttachment(ctx, r.cfg.Adapters[e.ProviderID], e, in.statelessPrompt)
	}

	// Round-robin: xoay để lượt này bắt đầu ở mắt xích đủ điều kiện kế con trỏ combo, rồi vẫn
	// fallthrough hết phần còn lại. CHỈ phần đủ điều kiện xoay — claude-code là lưới cuối, không phải
	// một primary định kỳ (xem rrRotate). Nhánh HasAttachments ở trên đã trả về, nên định tuyến tệp
	// giữ NGUYÊN thứ tự (firstEligibleCLIEntry là ranh giới ổn định, không round-robin).
	if snapshot.Type == "round_robin" {
		entries = rrRotate(entries, snapshot.ComboID)
	}

	apiCtx, cancel := context.WithTimeout(ctx, r.cfg.APIChainTimeout)
	defer cancel()
	// Không tách mắt xích cuối: chuỗi này position-agnostic. Claude Code là mắt xích ĐẦU CUỐI ở
	// bất kỳ vị trí nào; mọi mắt xích khác đi qua adapter với ngân sách 25s dưới apiCtx.
	for i, e := range entries {
		// Hai cửa tắt, không một: mắt xích tắt là lựa chọn của người dựng chuỗi, còn Provider tắt
		// là lệnh ngắt của người trực. Cái sau tới SAU lúc lưu chuỗi nên nó phải được xét ở đây,
		// tại lượt gọi, chứ không ở chỗ hợp lệ hoá.
		if !e.Enabled || r.cfg.Disabled[e.ProviderID] {
			continue
		}
		// Claude Code là mắt xích ĐẦU CUỐI: chạy dưới ctx CHA (ngân sách lượt dài — một lượt Claude
		// Code đo được 42–114s — không phải 25s của một API treo) và mang step gốc, rồi trả kết quả.
		// Mắt xích sau nó trong lượt này KHÔNG chạy; Task 11 cho Claude Code fallthrough như mọi cái.
		if e.ProviderID == claudeCodeProviderID {
			return r.runClaude(ctx, e, in, step)
		}
		// Ngân sách chuỗi API đã cạn: mọi mắt xích API còn lại chạy bằng một apiCtx đã chết chỉ sinh
		// thêm một hàng telemetry đổ tội cho Provider sau. Bỏ qua chúng — vòng lặp đi tiếp để tới
		// mắt xích claude-code, thứ DUY NHẤT còn chạy được (nó dùng ctx cha, không dùng apiCtx).
		if apiCtx.Err() != nil {
			continue
		}
		// Hai lỗi TRƯỚC lượt gọi, và cả hai đều dừng chuỗi mà không sinh telemetry: sổ vận hành
		// đếm những lần thật sự chạm tới Provider, nên một hàng "hỏng" cho một request chưa hề
		// rời khỏi máy này làm người đọc kết luận sai về nhà cung cấp.
		adapter, ok := r.cfg.Adapters[e.ProviderID]
		if !ok {
			// Không im lặng nhảy cóc: chuỗi này do người dùng dựng, và một mục không gọi được
			// mà bị bỏ qua sẽ nằm trong Portal như đang phục vụ trong khi nó chưa từng chạy.
			return appZaloRunResult{}, newLLMError(llmErrorRequest, nil,
				"llm route: không có adapter cho Provider %s", e.ProviderID)
		}
		credential, err := r.cfg.Credential(e.ProviderID)
		if err != nil {
			return appZaloRunResult{}, fmt.Errorf("llm route: mở khoá của %s: %w", e.ProviderID, err)
		}

		started := time.Now()
		resp, err := r.generate(apiCtx, adapter, e, in.statelessPrompt, credential)
		elapsed := time.Since(started)
		if err == nil {
			r.record(llmOKAttempt(e, started, elapsed))
			return appZaloRunResult{Answer: resp.Text}, nil
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
			return appZaloRunResult{}, err
		}
		if !isFallbackEligible(kind) {
			r.record(llmErrorAttempt(e, started, elapsed, kind, ""))
			return appZaloRunResult{}, err
		}
		// Nơi lượt THỰC SỰ đi tiếp, không phải mắt xích kế trên giấy: nếu ngân sách chuỗi API vừa
		// cạn thì mọi mắt xích API còn lại bị bỏ qua, nên đích thật là mắt xích claude-code kế —
		// nó chạy dưới ctx cha. Ghi đúng nơi đã đi để fell_back và next_provider_id không nói dối.
		next := r.nextProvider(entries, i+1)
		if apiCtx.Err() != nil {
			next = ""
			if claude, ok := findClaudeEntry(entries[i+1:]); ok {
				next = claude.ProviderID
			}
		}
		r.record(llmErrorAttempt(e, started, elapsed, kind, next))
	}
	// Vòng lặp hết mà không mắt xích nào phục vụ (mọi mắt xích API hỏng, không tới được claude-code):
	// "không khả dụng", KHÔNG im lặng trả rỗng — cả chuỗi đã cạn và không có câu trả lời để gửi.
	return appZaloRunResult{}, newLLMError(llmErrorUpstream, nil,
		"llm route: cạn chuỗi fallback, không mắt xích nào trả lời được")
}

// findClaudeEntry tìm mắt xích Claude Code ĐANG BẬT trong chuỗi, ở bất kỳ vị trí nào.
//
// Là hàm riêng vì hai chỗ cần nó: cửa chặn lượt có tệp (phải đi Claude Code) và telemetry lúc
// ngân sách chuỗi API cạn (đích thật là mắt xích claude-code kế). Chỉ xét Enabled — Claude Code
// không bao giờ nằm trong Disabled (tập đó chỉ chứa Provider gọi được qua HTTP, xem appLLMAdapters).
func findClaudeEntry(entries []store.LLMRouteEntry) (store.LLMRouteEntry, bool) {
	for _, e := range entries {
		if e.Enabled && e.ProviderID == claudeCodeProviderID {
			return e, true
		}
	}
	return store.LLMRouteEntry{}, false
}

// firstEligibleCLIEntry tìm mắt xích local_cli ĐẦU TIÊN đủ điều kiện phục vụ một lượt có tệp.
//
// "Đủ điều kiện" = đang bật, KHÔNG nằm trong Disabled, VÀ thuộc họ local_cli. Ba nơi tệp đọc được
// ngay trên đĩa máy này: codex, gemini-cli (đều chỉ-đọc) và claude-code. Bốn Provider HTTP thì
// KHÔNG — chúng không có ổ đĩa, và gửi đường dẫn/nội dung tệp của khách sang đó là phá ranh giới an
// toàn. Cùng HAI cửa tắt như vòng lặp trong Run: mắt xích tắt là lựa chọn người dựng chuỗi, Provider
// tắt là lệnh ngắt của người trực (tới SAU lúc lưu chuỗi).
//
// Nhận diện kind theo cấu trúc sẵn có, không cần một map kind riêng: claude-code CHƯA có adapter
// (Task 11) nên nhận bằng ProviderID; codex/gemini-cli nhận bằng type-assert *cliAdapter — đúng type
// mà newLLMAdapter dựng cho hai kind local_cli đó. Một adapter vắng mặt hoặc là adapter HTTP đều
// trả về ok=false ở phép assert này, nên bốn Provider HTTP tự động rớt. "Đầu tiên" = vị trí thấp
// nhất trong chuỗi, nên trả về ngay mục khớp đầu tiên.
func (r *appLLMRunner) firstEligibleCLIEntry(entries []store.LLMRouteEntry) (store.LLMRouteEntry, bool) {
	for _, e := range entries {
		if !e.Enabled || r.cfg.Disabled[e.ProviderID] {
			continue
		}
		if e.ProviderID == claudeCodeProviderID {
			return e, true
		}
		if _, ok := r.cfg.Adapters[e.ProviderID].(*cliAdapter); ok {
			return e, true
		}
	}
	return store.LLMRouteEntry{}, false
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
	in appLLMRouteInput, step func(string)) (appZaloRunResult, error) {
	if err := ctx.Err(); err != nil {
		return appZaloRunResult{}, fmt.Errorf("llm route: lượt bị huỷ trước khi tới %s: %w", e.ProviderID, err)
	}
	started := time.Now()
	claude := r.cfg.Claude(e.ModelID)
	var result appZaloRunResult
	var err error
	if in.structured == nil {
		result.Answer, err = claude.Run(ctx, in.statelessPrompt, step)
	} else if structured, ok := claude.(appZaloStructuredRunner); ok {
		result, err = structured.appRunZaloSession(ctx, *in.structured, step)
	} else if _, silent := claude.(silentZaloRunner); silent {
		err = ErrZaloSilent
	} else {
		err = errors.New("llm route: Claude runner không hỗ trợ session có cấu trúc")
	}
	elapsed := time.Since(started)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return appZaloRunResult{}, fmt.Errorf("llm route: lượt bị huỷ trong %s: %w", e.ProviderID, ctxErr)
		}
		// Trong chuỗi = bot ĐÃ cấu hình (đã qua cổng hasAnyConnectedProvider). Mắt xích Claude cuối
		// không chạy được (0 account / không resolve được claude.exe) trả ErrZaloSilent là lỗi
		// runtime → PHẢI leo thang (gọi người), KHÔNG nuốt im. ErrZaloSilent chỉ mang nghĩa "chưa
		// cấu hình" ở TẦNG VÀO (appZaloRunner), không xuyên qua answerZalo từ trong chuỗi. Dùng %v
		// để cắt chuỗi sentinel — cùng ghi telemetry "không khả dụng" như mọi cách cạn chuỗi khác.
		if errors.Is(err, ErrZaloSilent) {
			r.record(llmErrorAttempt(e, started, elapsed, llmErrorUpstream, ""))
			return appZaloRunResult{}, fmt.Errorf("llm route: mắt xích Claude cuối không khả dụng: %v", err)
		}
		// Không còn mắt xích nào sau Claude Code, nên đây là trạng thái "không khả dụng": cả
		// chuỗi đã cạn và lượt này không có câu trả lời nào để gửi.
		r.record(llmErrorAttempt(e, started, elapsed, llmErrorUpstream, ""))
		return result, fmt.Errorf("llm route: cạn chuỗi fallback, %s cũng hỏng: %w", e.ProviderID, err)
	}
	r.record(llmOKAttempt(e, started, elapsed))
	return result, nil
}

// runCLIAttachment giao một lượt có tệp cho một mắt xích codex/gemini-cli, bằng ctx GỐC.
//
// Cùng lẽ ngân sách với runClaude: một lượt CLI đọc tệp là một lượt thật (đo được hàng chục giây),
// không phải một lượt gọi API 25s — nên nó chạy dưới ctx cha, KHÔNG dưới apiCtx. Terminal như
// runClaude: một mắt xích local_cli duy nhất phục vụ rồi trả về, KHÔNG chạy vòng fallback.
//
// KHÔNG mở khoá: codex/gemini-cli là CLI thuê bao mang phiên đăng nhập riêng và bỏ qua credential
// (xem cliAdapter.Generate), y như claude-code — nên truyền nil, không có bản rõ nào để lộ hay xoá.
func (r *appLLMRunner) runCLIAttachment(ctx context.Context, adapter providerAdapter,
	e store.LLMRouteEntry, prompt string) (appZaloRunResult, error) {
	if err := ctx.Err(); err != nil {
		return appZaloRunResult{}, fmt.Errorf("llm route: lượt bị huỷ trước khi tới %s: %w", e.ProviderID, err)
	}
	started := time.Now()
	resp, err := adapter.Generate(ctx, llmRequest{Model: e.ModelID, Prompt: prompt}, nil)
	elapsed := time.Since(started)
	if err != nil {
		// Lượt chết KHÔNG phải lỗi Provider: ctx cha chết (tắt daemon / hết hạn mức lượt) thì không
		// ghi telemetry — cùng kỷ luật "không đổ tội" như vòng lặp API và runClaude.
		kind := llmErrorKindOf(err)
		if kind == llmErrorCanceled || ctx.Err() != nil {
			return appZaloRunResult{}, fmt.Errorf("llm route: lượt bị huỷ trong %s: %w", e.ProviderID, err)
		}
		// Không còn mắt xích nào sau nó (terminal): "không khả dụng", ghi hàng lỗi với next rỗng.
		r.record(llmErrorAttempt(e, started, elapsed, kind, ""))
		return appZaloRunResult{}, fmt.Errorf("llm route: cạn chuỗi fallback, %s cũng hỏng: %w", e.ProviderID, err)
	}
	r.record(llmOKAttempt(e, started, elapsed))
	return appZaloRunResult{Answer: resp.Text}, nil
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

// nextProvider là mục sẽ được thử tiếp, để telemetry nói đúng chuỗi đã đi.
//
// Cùng ĐÚNG hai cửa tắt như vòng lặp trong Run. Bỏ sót cửa Disabled ở đây thì sổ vận hành ghi
// "né sang X" về một Provider mà lượt đó không hề gọi tới — và fell_back tính từ cùng chỗ này,
// nên sai một chỗ là sai cả hai cột.
//
// Mắt xích cuối không nằm trong Disabled: tập đó chỉ chứa Provider gọi được qua HTTP, còn Claude
// Code thì không (xem appLLMAdapters). Nhờ vậy dòng telemetry cuối vẫn trỏ đúng lưới an toàn.
func (r *appLLMRunner) nextProvider(entries []store.LLMRouteEntry, from int) string {
	for _, e := range entries[from:] {
		if e.Enabled && !r.cfg.Disabled[e.ProviderID] {
			return e.ProviderID
		}
	}
	return ""
}

// rotate trả một lát cắt bắt đầu ở offset k rồi vòng về đầu: entries[k:] + entries[:k]. Thuần,
// không sửa đầu vào (Run đã Clone snapshot.Entries). k phải trong [0,len).
func rotate(entries []store.LLMRouteEntry, k int) []store.LLMRouteEntry {
	n := len(entries)
	if n == 0 || k%n == 0 {
		return entries
	}
	k %= n
	out := make([]store.LLMRouteEntry, 0, n)
	out = append(out, entries[k:]...)
	out = append(out, entries[:k]...)
	return out
}

// rrRotate xoay một combo round_robin cho ĐÚNG một lượt: chỉ những mắt xích ĐỦ ĐIỀU KIỆN (đang bật,
// KHÔNG phải claude-code) xoay vòng với nhau để mỗi lượt một cái khác dẫn đầu, còn mắt xích tắt và
// lưới an toàn claude-code giữ NGUYÊN thứ tự tương đối của chúng phía SAU.
//
// Vì sao chỉ xoay phần đủ điều kiện: claude-code là mắt xích ĐẦU CUỐI — nó trả lời ngay bằng ngân
// sách lượt dài rồi trả về. Xoay CẢ chuỗi thì cứ 1-trên-N lượt claude-code lại lọt lên đầu và lượt
// đó đi thẳng đường Claude đắt/chậm, bỏ qua các Provider API — đúng thứ round-robin sinh ra để
// tránh. Nó phải là chốt chặn CUỐI, chỉ chạm tới sau khi các mắt xích đủ điều kiện đã hỏng.
//
// Con trỏ đẩy theo SỐ mắt xích đủ điều kiện (len(lead)), không theo cả chuỗi, nên mỗi lượt đúng
// một mắt xích API khác nhau dẫn đầu. Không mắt xích đủ điều kiện nào (toàn tắt hoặc chỉ có
// claude-code) → không xoay, giữ nguyên chuỗi. Một mắt xích đủ điều kiện → rotate về identity.
func rrRotate(entries []store.LLMRouteEntry, comboID string) []store.LLMRouteEntry {
	// lead cấp sẵn đủ chỗ cho CẢ chuỗi: ở nhánh identity (rotate trả lại chính lead khi k%n==0),
	// append(lead, tail...) ghi tail vào phần đuôi backing array của lead — an toàn vì lead là mảng
	// mới toanh ở đây, không dùng lại sau lời gọi này, và cap đủ nên không cấp phát lại đè ai.
	lead := make([]store.LLMRouteEntry, 0, len(entries))
	tail := make([]store.LLMRouteEntry, 0, len(entries))
	for _, e := range entries {
		if e.Enabled && e.ProviderID != claudeCodeProviderID {
			lead = append(lead, e)
		} else {
			tail = append(tail, e)
		}
	}
	if len(lead) == 0 {
		return entries
	}
	return append(rotate(lead, comboRR.next(comboID, len(lead))), tail...)
}

// comboRRCursor là con trỏ round-robin in-memory theo combo id — đối xứng accountSel (app_llm_accounts.go):
// `api` khai ở base repo không thêm field được, và runner dựng mới mỗi lượt nên cursor không ở đó
// được. Guard mutex; restart reset (vô hại: cùng lắm lệch một lượt phân bổ).
type comboRRCursor struct {
	mu     sync.Mutex
	cursor map[string]int
}

func newComboRR() *comboRRCursor { return &comboRRCursor{cursor: map[string]int{}} }

// next trả offset hiện tại cho combo rồi tăng con trỏ (mod n). n<=0 → 0.
func (c *comboRRCursor) next(comboID string, n int) int {
	if n <= 0 {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.cursor[comboID] % n
	c.cursor[comboID] = (k + 1) % n
	return k
}

var comboRR = newComboRR()

// --- dây nối bản chạy ---

// newLLMAdapter chọn giao thức theo kind của Provider.
//
// Kind lạ trả về false chứ không một adapter mặc định: đoán bừa giao thức nghĩa là gửi khoá thật
// tới một máy chủ theo hình dạng request của nhà cung cấp khác.
//
// logger chỉ đi tới adapter CLI (nó spawn tiến trình cần log); bốn adapter HTTP bỏ qua nó. KIND
// "claude-code" CỐ Ý vắng: Claude Code vẫn chạy qua runClaude trong task này (Task 11 gộp nó vào
// đây), nên một Provider claude-code không có adapter, và router nhận ra nó bằng ProviderID.
func newLLMAdapter(kind, providerID string, client *http.Client, logger *slog.Logger) (providerAdapter, bool) {
	switch kind {
	case "openai":
		return newOpenAIAdapter(providerID, client), true
	case "anthropic":
		return newAnthropicAdapter(providerID, client), true
	case "gemini":
		return newGeminiAdapter(providerID, client), true
	case "openrouter":
		return newOpenRouterAdapter(providerID, client), true
	case "codex":
		// codex chạy qua PROXY (gọi thẳng backend bằng token OAuth), KHÔNG spawn CLI nữa — quyết định
		// của chủ sản phẩm (mang rủi ro khoá, badge nói đúng). pickConfigDir wire ở appLLMAdapters.
		return &codexProxyAdapter{providerID: providerID, logger: logger, client: client}, true
	case "gemini-cli":
		return newCLIAdapter(cliDescriptors[kind], providerID, logger), true
	}
	return nil, false
}

// silentZaloRunner: chưa cấu hình provider → im. answerZalo nhận ErrZaloSilent và nuốt (không escalate).
type silentZaloRunner struct{}

func (silentZaloRunner) Run(context.Context, string, func(string)) (string, error) {
	return "", ErrZaloSilent
}

// hasAnyConnectedProvider trả lời: người mua đã nối ít nhất một Provider chưa?
//
// "Nối" đúng theo cách Portal tính (providers.js): một kind subscription nối = có ít nhất một
// account đang BẬT; một Provider API nối = đã có credential (CredentialConfigured). Chưa có gì thì
// bot IM (silentZaloRunner) thay vì rơi về một Claude mặc định — đảo ngược cố ý của "thà trả lời
// còn hơn im".
//
// providerID của một kind subscription CHÍNH LÀ kind (EnsureProviderForKind gieo id = kind), nên
// LLMAccounts(kind) đọc đúng account của nó — không cần một map kind→providerID riêng.
//
// Đọc hỏng ở nhánh nào thì coi nhánh đó "không có": bot im là mặc định an toàn, và một lỗi đọc
// store không được biến thành "đã nối" để rồi gửi tin của khách đi khi chưa cấu hình gì.
func (a *api) hasAnyConnectedProvider() bool {
	for kind := range subscriptionKinds {
		if accs, err := a.st.LLMAccounts(kind); err == nil {
			for _, ac := range accs {
				if ac.Enabled {
					return true
				}
			}
		}
	}
	if provs, err := a.st.LLMProviders(); err == nil {
		for _, p := range provs {
			if p.CredentialConfigured {
				return true
			}
		}
	}
	return false
}

// appZaloRunner dựng runner cho ĐÚNG một lượt Zalo.
//
// Dựng mới mỗi lượt chứ không cất vào api: đó là toàn bộ lý do một lần lưu route có hiệu lực ngay
// mà không phải mở lại phần mềm. Danh sách Provider cũng đọc lại ở đây, nên một Provider vừa thêm
// gọi được từ tin nhắn kế tiếp.
//
// Không trả về lỗi vì chỗ gọi nó là một tham số của answerZalo. KHÔNG có Claude mặc định: chưa
// nối Provider nào, hay đọc route hỏng, thì lượt này IM (silentZaloRunner → ErrZaloSilent, mà
// answerZalo nuốt) chứ không rơi về base. Đảo ngược cố ý của "thà trả lời còn hơn im".
//
// hasNewFiles là tệp của TIN NÀY. Tệp của cả luồng mới là thứ quyết định — xem HasAttachments.
func (a *api) appZaloRunner(zc zaloConfig, base zaloRunner, threadID string, hasNewFiles bool) zaloRunner {
	// Chưa nối Provider nào → im, TRƯỚC cả khi đọc route: không có Claude mặc định để rơi về, nên
	// một bot chưa cấu hình phải lặng thay vì trả lời bằng một login sẵn nào đó của máy này.
	if !a.hasAnyConnectedProvider() {
		return silentZaloRunner{}
	}
	// Chuỗi rỗng nghĩa là chưa có định tuyến, KHÔNG phải "không còn gì để gọi": router coi rỗng
	// là lỗi và lượt sẽ chết, nên cửa này phải ở đây chứ không ở trong router. Xảy ra khi lần
	// gieo lúc khởi động hỏng, và ở đó vẫn im — có Provider nối nhưng chưa có chuỗi để đi.
	//
	// Đây là lần đọc route DUY NHẤT của lượt. Cùng snapshot vừa qua cửa cấu hình được chuyển vào
	// runner; một lần lưu xảy ra sau đây chỉ có hiệu lực với runner/lượt kế tiếp.
	snapshot, err := a.st.LLMRoute()
	if err != nil {
		a.logger.Error("llm route: không đọc được chuỗi, chưa cấu hình, bot im", "err", err)
		return silentZaloRunner{}
	}
	if len(snapshot.Entries) == 0 {
		return silentZaloRunner{}
	}
	adapters, disabled, err := a.appLLMAdapters()
	if err != nil {
		a.logger.Error("llm route: không dựng được adapter, lỗi, bot im", "err", err)
		return silentZaloRunner{}
	}
	return newAppLLMRunner(appLLMRunnerConfig{
		Route:    snapshot,
		Store:    a.st,
		Adapters: adapters,
		Disabled: disabled,
		Claude:   func(model string) zaloRunner { return a.appClaudeRunner(zc, base, model) },
		ClaudeForSession: func(
			ctx context.Context,
			model string,
			current *store.ZaloCLISession,
		) (zaloRunner, appZaloClaudeBinding) {
			return a.appClaudeRunnerForSession(ctx, zc, model, current)
		},
		Credential: a.appLLMCredential,
		// Tệp của tin hiện tại đã biết trước khi dựng runner. Tệp lịch sử được chụp đúng một lần
		// ngay sau đó trong appAnswerZalo và áp qua appWithZaloAttachments; cùng snapshot ấy dựng
		// prompt, nên không có cửa một tệp lọt vào prompt sau quyết định an toàn này.
		HasAttachments: hasNewFiles,
		Logger:         a.logger,
	})
}

// appClaudeRunner là mắt xích cuối chạy đúng model mà route nêu tên, dưới đăng nhập của một account.
//
// zc là BẢN SAO (tham số theo giá trị), nên đổi Model/ConfigDir/Program ở đây không chạm tới cấu
// hình của vòng trực — hai lượt song song trên hai model/account khác nhau vẫn đúng.
//
// Claude CHỈ chạy khi CÓ account claude-code nối VÀ định vị được exe: chọn một account theo
// round-robin (accountSel, cùng selector với codex), chạy lượt dưới CLAUDE_CONFIG_DIR của nó
// (zc.ConfigDir → claudeEnv) và dưới exe claude cài qua npm (resolveCLIProgram → zc.Program). Không
// có Claude mặc định nữa: 0 account, hoặc account có nhưng exe không định vị được, thì IM
// (silentZaloRunner) — KHÔNG rơi về base. base giữ lại trong chữ ký cho seam của appZaloRunner/test,
// dù thân hàm không còn dùng tới.
//
// ponytail: penalize-on-rate-limit CHƯA nối cho Claude — runClaude không có bộ phân loại rate-limit
// (khác cliAdapter của codex), nên đây chỉ round-robin. Cooldown là follow-up khi runClaude phân
// loại được lỗi 429 của Claude.
func (a *api) appClaudeRunner(zc zaloConfig, base zaloRunner, model string) zaloRunner {
	_ = base
	runner, _ := a.appClaudeRunnerForSession(context.Background(), zc, model, nil)
	return runner
}

func (a *api) appClaudeRunnerForSession(
	ctx context.Context,
	zc zaloConfig,
	model string,
	current *store.ZaloCLISession,
) (zaloRunner, appZaloClaudeBinding) {
	accounts, err := a.st.LLMAccounts(claudeCodeProviderID)
	if err != nil {
		return silentZaloRunner{}, appZaloClaudeBinding{}
	}
	if len(accounts) == 0 {
		return silentZaloRunner{}, appZaloClaudeBinding{State: appZaloClaudeBindingUnavailable}
	}
	acc, ok := appSelectClaudeAccountForSession(accountSel, accounts, current)
	if !ok {
		return silentZaloRunner{}, appZaloClaudeBinding{State: appZaloClaudeBindingUnavailable}
	}
	zc.ConfigDir = acc.ConfigDir
	if model != "" && model != zc.Model {
		zc.Model = model
	}
	binding := appZaloClaudeBinding{
		State:     appZaloClaudeBindingSelected,
		AccountID: acc.ID,
		ConfigDir: acc.ConfigDir,
		Model:     zc.Model,
	}
	program, prefixArgs, err := resolveCLIProgramContext(ctx, cliDescriptors["claude-code"])
	if err != nil {
		// Account có nhưng binary claude không định vị được → im, không đoán một exe khác.
		return silentZaloRunner{}, binding
	}
	zc.Program, zc.ProgramPrefixArgs = program, prefixArgs
	return execZaloRunner{cfg: zc, logger: a.logger}, binding
}

// appLLMAdapters dựng adapter cho mọi Provider API đang lưu, khoá theo id, kèm tập những id đang
// TẮT.
//
// Provider tắt vẫn có adapter, và đó là lý do phải trả thêm tập thứ hai: một mục route trỏ vào
// Provider không có adapter là cấu hình hỏng và router DỪNG chuỗi để nói ra, còn một Provider bị
// người trực tắt thì router phải BỎ QUA và đi tiếp. Hai cách xử lý khác nhau nên chúng không dùng
// chung được một phép thử "có adapter không".
//
// Cửa này không thừa so với validateLLMRoute: chỗ đó chỉ chạy lúc LƯU chuỗi, nên nó chặn được
// việc dựng một chuỗi qua Provider đang tắt, chứ không chặn được việc tắt một Provider mà chuỗi
// đang dùng. Đường thứ hai mới là đường người trực đi khi khoá bị lộ.
func (a *api) appLLMAdapters() (map[string]providerAdapter, map[string]bool, error) {
	providers, err := a.st.LLMProviders()
	if err != nil {
		return nil, nil, fmt.Errorf("llm route: đọc danh sách Provider: %w", err)
	}
	// Hạn giờ trên client vì ctx chặn được một lượt gọi nhưng không chặn được lúc bắt tay TLS.
	// Cùng mức với hạn giờ mỗi Provider của router, nên không có hai con số phải giữ đồng bộ.
	//
	// Client mới mỗi lượt là RẺ: Transport để nil nghĩa là http.DefaultTransport, nên pool kết
	// nối vẫn dùng chung — chỉ mỗi trường Timeout là của riêng client này.
	client := &http.Client{Timeout: defaultLLMProviderTimeout}
	adapters := make(map[string]providerAdapter, len(providers))
	disabled := map[string]bool{}
	for _, p := range providers {
		adapter, ok := newLLMAdapter(p.Kind, p.ID, client, a.logger)
		if !ok {
			continue
		}
		// Multi-account: chỉ kind subscription (envVarFor true) chạy qua cliAdapter mới cần chọn
		// account + set env config-dir mỗi lượt. Hôm nay chỉ codex khớp (claude-code còn qua
		// runClaude tới Task 11; gemini-cli là cliAdapter nhưng không subscription → bỏ qua).
		if ca, isCLI := adapter.(*cliAdapter); isCLI {
			if _, isSub := envVarFor(p.Kind); isSub {
				ca.accountEnv = makeAccountEnv(a.st, accountSel, p.ID, p.Kind)
			}
		}
		// codex chạy qua proxy: cần CODEX_HOME của account đã chọn để đọc token OAuth (auth.json).
		if pa, isProxy := adapter.(*codexProxyAdapter); isProxy {
			pa.pickConfigDir = makeAccountConfigDir(a.st, accountSel, p.ID, p.Kind)
		}
		adapters[p.ID] = adapter
		// Chỉ Provider GỌI ĐƯỢC mới vào tập tắt. Claude Code không có adapter nên nó không bao giờ
		// tới đây — đúng như phải thế: nó là lưới an toàn, và một hàng database sửa tay không được
		// biến nó thành mắt xích bị bỏ qua.
		if !p.Enabled {
			disabled[p.ID] = true
		}
	}
	return adapters, disabled, nil
}

// appLLMCredential mở khoá của một Provider, trả về BẢN RÕ MỚI mỗi lượt gọi.
//
// Mới mỗi lượt vì router xoá lát cắt nó nhận ngay sau lời gọi adapter: một hiện thực trả về lát
// cắt dùng chung sẽ phát ra toàn số 0 từ lượt thứ hai.
func (a *api) appLLMCredential(providerID string) ([]byte, error) {
	cipher, err := a.st.LLMCredentialCipher(providerID)
	if errors.Is(err, store.ErrNotFound) {
		// Provider không lưu credential (proxy/CLI thuê bao như codex/gemini-cli, hoặc API provider
		// chưa nhập khoá): trả nil, để adapter tự quyết. Adapter proxy/CLI bỏ qua tham số này; adapter
		// HTTP nhận nil sẽ tự trả lỗi credential (được ghi telemetry ở nhánh non-fallback của Run).
		return nil, nil
	}
	if err != nil {
		return nil, err // decrypt/unprotect hỏng = credential KHÔNG đọc được → vẫn dừng chuỗi
	}
	return unprotectProviderSecret(cipher)
}
