package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentdc/internal/store"
)

// cliDescriptor là DỮ LIỆU cho một vendor CLI, không phải code. Adapter thân duy nhất đọc nó.
// Học từ 9Router: provider là descriptor, không phải một nhánh switch.
type cliDescriptor struct {
	kind           string // kind của họ provider local_cli. claude-code dùng gạch nối, thống nhất với hàng seeded (migration đã hợp nhất kind cũ claude_code → claude-code).
	display        string
	npmPackage     string   // gói cài; rỗng nếu là native exe (claude)
	binJS          string   // đường dẫn tương đối tới entry .js trong node_modules; rỗng nếu native
	nativeBin      string   // tên exe khi là native (claude); rỗng nếu chạy qua node
	subArgs        []string // lệnh con không tương tác: {"exec"} codex, nil cho -p
	promptViaStdin bool     // true: prompt qua stdin; false: positional arg
	promptFlag     string   // cờ đứng trước prompt trong argv: "-p" gemini; rỗng = positional (codex)
	modelFlag      string
	readOnlyArgs   []string   // cờ giới hạn chỉ-đọc — BẮT BUỘC có
	bannedArgs     []string   // cờ bỏ sandbox — test chặn adapter dựng chúng
	authMethod     string     // "claude-json" | "codex-exit" | "gemini-file"
	modelSeeds     []cliModel // Discover tĩnh
	claudeBudget   bool       // Task 11 đọc khi claude-code chạy qua cliAdapter; hiện Run ép ngân sách dài theo providerID. true = dùng ctx gốc, không đặt dưới 25s (chỉ claude-code)
}

type cliModel struct{ id, name string }

// Verified trên --help bản đang cài (2026-08-06). KHÔNG bê nguyên list proxy của 9Router.
var cliDescriptors = map[string]cliDescriptor{
	"codex": {
		kind: "codex", display: "OpenAI Codex (ChatGPT)",
		npmPackage: "@openai/codex", binJS: `@openai\codex\bin\codex.js`,
		subArgs: []string{"exec"}, promptViaStdin: false, modelFlag: "-m",
		// Cô lập lượt tư vấn khỏi config codex của MÁY KHÁCH — verify `codex exec` thật 2026-08-07:
		//   --ignore-user-config: bỏ ~/.codex/config.toml (model/effort/MCP/provider của khách); auth vẫn
		//     ở CODEX_HOME nên phiên ChatGPT còn. 24.342→12.392 tok, và hết treo 3 phút do config khách.
		//   -c model_reasoning_effort=low: ghim effort thấp, không để khách (xhigh, đắt) hay default quyết.
		//   -c features.{plugins,skill_search}=false: tắt plugin/skill nạp từ ~/.codex/{plugins,skills}
		//     (superpowers đọc SKILL.md mỗi lượt) — thứ --ignore-user-config KHÔNG tắt vì là feature mặc
		//     định bật, không nằm trong config.toml. Sau khi tắt: "2+2" còn 700 tok, sạch rò. DÙNG dạng
		//     `-c features.X=false` CHỨ KHÔNG `--disable X`: `--disable` tên lạ THOÁT 1 ("Unknown feature
		//     flag") → bản codex sau đổi tên feature là hỏng MỌI lượt; dạng `-c` bỏ qua lặng tên lạ (verify:
		//     exit 0). KHÔNG cờ nào bỏ sandbox — read-only vẫn nguyên.
		readOnlyArgs: []string{
			"-s", "read-only", "--skip-git-repo-check", "--ephemeral", "--color", "never",
			"--ignore-user-config",
			"-c", "model_reasoning_effort=low",
			"-c", "features.plugins=false",
			"-c", "features.skill_search=false",
		},
		bannedArgs: []string{"--dangerously-bypass-approvals-and-sandbox", "workspace-write", "danger-full-access"},
		authMethod: "codex-exit",
		// modelSeeds ghim theo /models THẬT (~/.codex/models_cache.json, fetch từ API 2026-08-07). Bỏ
		// codex-auto-review (model duyệt nội bộ, không phải model chat). gpt-5.4 CŨ đã không còn tồn tại.
		modelSeeds: []cliModel{
			{"gpt-5.6-terra", "GPT-5.6-Terra"}, // cân bằng, mặc định của codex
			{"gpt-5.6-luna", "GPT-5.6-Luna"},   // nhanh & rẻ
			{"gpt-5.5", "GPT-5.5"},             // frontier
			{"gpt-5.4-mini", "GPT-5.4-Mini"},   // nhỏ, rẻ nhất
		},
	},
	"gemini-cli": {
		kind: "gemini-cli", display: "Gemini CLI (Google AI)",
		npmPackage: "@google/gemini-cli", binJS: `@google\gemini-cli\bundle\gemini.js`,
		subArgs: nil, promptViaStdin: false, promptFlag: "-p", modelFlag: "-m",
		readOnlyArgs: []string{"--approval-mode", "plan", "--skip-trust", "-o", "json"},
		bannedArgs:   []string{"-y", "--yolo", "yolo", "auto_edit", "--raw-output"},
		authMethod:   "gemini-file",
		modelSeeds:   []cliModel{{"gemini-2.5-pro", "Gemini 2.5 Pro"}, {"gemini-2.5-flash", "Gemini 2.5 Flash"}, {"gemini-3-pro-preview", "Gemini 3 Pro Preview"}},
	},
	"claude-code": {
		kind: "claude-code", display: "Claude Code",
		nativeBin: "claude",
		subArgs:   nil, promptViaStdin: true, modelFlag: "--model",
		readOnlyArgs: []string{"--allowed-tools", "Read", "Grep", "Glob", "WebFetch"},
		bannedArgs:   []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "bypassPermissions", "--permission-mode"},
		authMethod:   "claude-json", claudeBudget: true,
		modelSeeds: []cliModel{{"sonnet", "Claude Sonnet"}, {"opus", "Claude Opus"}, {"fable", "Claude Fable"}},
	},
}

// buildCLIArgv dựng argv cho phần SAU tên chương trình. CỐ Ý chỉ nối những gì descriptor mang:
// một tin nhắn khách (input không tin được) không có đường nào thành một cờ bỏ sandbox.
func buildCLIArgv(d cliDescriptor, req llmRequest) []string {
	argv := make([]string, 0, 16)
	argv = append(argv, d.subArgs...)      // codex: "exec"; khác: rỗng
	argv = append(argv, d.readOnlyArgs...) // luôn có, luôn trước
	if d.modelFlag != "" && req.Model != "" {
		argv = append(argv, d.modelFlag, req.Model)
	}
	// claude đọc prompt qua stdin (Task 3) nên không nằm trong argv. Với vendor truyền qua argv:
	if !d.promptViaStdin {
		if d.promptFlag != "" {
			argv = append(argv, d.promptFlag, req.Prompt) // gemini: -p <prompt>, prompt là VALUE của cờ — an toàn
		} else {
			// `--` chặn clap/codex đọc một prompt bắt đầu bằng "--" thành cờ. Tin nhắn khách là ĐẦU VÀO
			// KHÔNG TIN CẬY, và "--dangerously-bypass-approvals-and-sandbox" là cờ thật của codex exec:
			// không có `--`, một positional trần "--..." bị hiểu thành chính cái cờ bỏ sandbox đó.
			argv = append(argv, "--", req.Prompt)
		}
	}
	return argv
}

// classifyCLIError ánh xạ output CLI về taxonomy CHUNG. Mặc định rate_limit khi mơ hồ: đoán sai
// hướng đó chỉ tốn một lượt thử Provider sau; đoán sai thành credential thì chết cả chuỗi.
//
// Danh sách chuỗi con cố ý để RỘNG. Chuỗi chưa-đăng-nhập của codex XÁC NHẬN THẬT (2026-08-07):
// `codex login status` in "Not logged in" (→ credential). Chuỗi HẾT-HẠN-MỨC vẫn CHƯA kiểm chứng
// (không ép được quota khi còn hạn mức) → giữ default rate_limit (đi tiếp). Đừng khớp chính xác.
func classifyCLIError(stderr string, notInstalled bool) llmErrorKind {
	if notInstalled {
		return llmErrorCredential // cần cài + đăng nhập; thử tiếp vô ích
	}
	s := strings.ToLower(stderr)
	switch {
	case containsAny(s, "not logged in", "please run", "authenticate", "login"):
		return llmErrorCredential
	case containsAny(s, "usage limit", "rate limit", "quota", "too many requests", "try again later"):
		return llmErrorRateLimit
	default:
		return llmErrorRateLimit // mơ hồ → cho chuỗi đi tiếp
	}
}

// containsAny trả true nếu s chứa bất kỳ chuỗi con nào trong subs.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// authState là trạng thái đăng nhập của một CLI vendor. authUnknown ("không xác định được") KHÁC hẳn
// authLoggedOut ("biết chắc chưa đăng nhập"): nhầm hai cái này đẩy người dùng vào vòng login lại vô
// ích khi thực ra ta chỉ là không dò/không đọc được.
type authState int

const (
	authUnknown   authState = iota // không kết luận được (chưa wire / không đọc được / output lạ)
	authLoggedIn                   // biết chắc đã đăng nhập
	authLoggedOut                  // biết chắc CHƯA đăng nhập — hành động được: đi login
)

// checkCLIAuth dò trạng thái đăng nhập theo authMethod của descriptor — RẺ, không tốn một lượt
// subscription. Mỗi vendor dò một kiểu, bất đối xứng này là THẬT (từ research): claude có lệnh
// `auth status --json`, codex chỉ có exit code, gemini KHÔNG có lệnh status nên phải dò file
// credential.
//
// credPath chỉ dùng cho nhánh gemini-file; các nhánh khác bỏ qua. Là THAM SỐ (không hardcode) để
// Task 8 ghim đúng tên file thật ở một nơi duy nhất.
func checkCLIAuth(ctx context.Context, d cliDescriptor, credPath string, logger *slog.Logger) authState {
	switch d.authMethod {
	case "gemini-file":
		return probeGeminiAuth(credPath)
	case "claude-json":
		return probeClaudeAuth(ctx, d, logger)
	case "codex-exit":
		// codex chạy qua `node <codex.js> login status`; việc RESOLVE codex.js (node global root)
		// được chốt ở task spawn sau, nên nhánh này chưa wire → unknown (an toàn, KHÔNG bịa "chưa
		// đăng nhập"). Ánh xạ exit→state đã sẵn trong codexAuthFromExit, cắm exit code vào là xong.
		return authUnknown
	default:
		return authUnknown
	}
}

// probeGeminiAuth: Gemini KHÔNG có lệnh status. Dò sự tồn tại file credential — đây là chi tiết NỘI
// BỘ của hãng, có thể vỡ khi Google đổi CLI, nên cô lập sau hàm này với test ghim. Tên file thật là
// LOW confidence (Task 8 xác nhận sau khi đăng nhập), vì thế credPath là tham số, không hardcode.
//
//   - thiếu file (os.IsNotExist)          → loggedOut: chưa đăng nhập thật, đi login được
//   - có file nhưng không đọc được / rỗng → unknown: KHÔNG kết luận "chưa đăng nhập" khi chỉ là quyền
//     /khoá file hay file 0 byte — nếu không sẽ loop người dùng login vô ích
//   - có file, khác rỗng                  → loggedIn
func probeGeminiAuth(credPath string) authState {
	if credPath == "" {
		return authUnknown // không dựng được đường dẫn (home-dir lỗi) → không kết luận chưa-đăng-nhập
	}
	info, err := os.Stat(credPath)
	if err != nil {
		if os.IsNotExist(err) {
			return authLoggedOut
		}
		return authUnknown // quyền/khoá file → không kết luận chưa-đăng-nhập
	}
	if info.Size() == 0 {
		return authUnknown // có mặt nhưng rỗng → không mang credential → không dám kết luận
	}
	return authLoggedIn
}

// probeClaudeAuth chạy `claude auth status --json` (native exe) rồi đọc trường loggedIn. Rẻ, chỉ đọc,
// KHÔNG sinh nội dung. Không dò được (chưa cài / spawn lỗi) → unknown, KHÔNG suy ra loggedOut.
func probeClaudeAuth(ctx context.Context, d cliDescriptor, logger *slog.Logger) authState {
	bin, err := exec.LookPath(d.nativeBin)
	if err != nil {
		return authUnknown // chưa cài → không biết trạng thái đăng nhập
	}
	// status là tức thì; chặn thời gian để một exe treo không giữ cả lời gọi Test. claude là tiến
	// trình đơn (không có cháu node) nên CommandContext giết-con-trực-tiếp là đủ, không cần killPidTree.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "auth", "status", "--json").Output()
	if err != nil {
		// `claude auth status --json` THOÁT khác 0 khi CHƯA đăng nhập, nhưng vẫn in JSON
		// {"loggedIn": false} hợp lệ (verify claude 2.1.223). Đọc thân TRƯỚC khi bỏ cuộc: một
		// exit≠0 KÈM JSON dứt khoát vẫn là câu trả lời (loggedOut), không phải "không dò được".
		// .Output() vẫn trả stdout đã bắt được dù lệnh thoát lỗi.
		if st := claudeAuthFromJSON(out); st != authUnknown {
			return st
		}
		if logger != nil {
			logger.Debug("claude auth status thất bại", "err", err)
		}
		return authUnknown
	}
	return claudeAuthFromJSON(out)
}

// claudeAuthFromJSON đọc {"loggedIn": bool} từ output `claude auth status --json`. loggedIn vắng /
// output hỏng → unknown (KHÔNG đoán loggedOut). Con trỏ *bool để phân biệt "trường vắng" với
// "trường = false". Tách hàm để test ghim đúng shape thật của lệnh.
func claudeAuthFromJSON(out []byte) authState {
	var v struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.LoggedIn == nil {
		return authUnknown
	}
	if *v.LoggedIn {
		return authLoggedIn
	}
	return authLoggedOut
}

// codexAuthFromExit ánh xạ exit code của `codex login status`: 0 = đã đăng nhập, khác 0 = chưa. codex
// KHÔNG có --json cho status, exit code là tín hiệu duy nhất. Hàm thuần để test được không cần spawn
// (spawn qua node+codex.js chốt ở task sau).
func codexAuthFromExit(code int) authState {
	if code == 0 {
		return authLoggedIn
	}
	return authLoggedOut
}

// cliExit mang stderr để Task 4 phân loại lỗi (hết hạn mức vs chưa đăng nhập).
type cliExit struct {
	err    error
	stderr string
}

func (e *cliExit) Error() string { return e.err.Error() }
func (e *cliExit) Unwrap() error { return e.err }

// runCLIProcess chạy một CLI đã dựng sẵn, ghi prompt vào stdin nếu có, đọc stdout, và khi ctx bị
// huỷ thì giết CẢ CÂY bằng killPidTree (taskkill /T) — KHÔNG dựa exec.CommandContext, vì nó chỉ
// giết con trực tiếp, để lại node→cli→[con] mồ côi (bug Task 5, headless.go nêu đúng hazard).
//
// childPID: nếu != nil PHẢI được đọc (buffered cap>=1, hoặc đọc ở goroutine khác) — send này CHẶN
// tới khi pid được nhận, cố ý để pid vào sổ orphan-reap trước khi chờ tiến trình.
func runCLIProcess(ctx context.Context, cmd *exec.Cmd, stdin []byte, childPID chan<- int, logger *slog.Logger) ([]byte, error) {
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	} else {
		cmd.Stdin = bytes.NewReader(nil) // codex exec đọc stdin mặc định → cấp rỗng, không để treo
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if childPID != nil {
		childPID <- cmd.Process.Pid
	}
	// Goroutine thoát khi cmd.Wait trả về; sau killPidTree (taskkill /F ép thoát cây, giết cả node
	// con trực tiếp) Wait trả về ngay nên <-done không kẹt.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = killPidTree(cmd.Process.Pid, "llm-cli", logger) // taskkill /T — giết cả cây
		<-done
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			return out.Bytes(), &cliExit{err: err, stderr: errBuf.String()}
		}
		return out.Bytes(), nil
	}
}

// --- adapter: một cliAdapter cho MỌI vendor CLI, dữ liệu ở descriptor ---

var _ providerAdapter = (*cliAdapter)(nil)

// cliAdapter chạy một vendor CLI như một Provider của router. Song song bốn adapter HTTP, nhưng
// vận chuyển là một tiến trình cục bộ chứ không phải một request.
type cliAdapter struct {
	d          cliDescriptor
	providerID string
	logger     *slog.Logger
	// run cho phép test tiêm một runner giả thay cho spawn thật — router test và adapter test không
	// được spawn CLI. Mặc định (nil) = spawn thật qua runCLIProcess.
	run func(ctx context.Context, argv []string, stdin []byte) ([]byte, error)
	// accountEnv chọn account cho lượt này (round-robin+cooldown) và trả env <VAR>=<configDir>
	// + penalize. nil = không multi-account (giữ hành vi cũ: env mặc định của máy). ok=false =
	// 0 account enabled → spawn trả credential (DỪNG chuỗi).
	accountEnv func() (env []string, penalize func(rateLimited bool), ok bool)
}

func newCLIAdapter(d cliDescriptor, providerID string, logger *slog.Logger) *cliAdapter {
	return &cliAdapter{d: d, providerID: providerID, logger: logger}
}

// Generate dựng argv từ descriptor, chạy CLI, rồi parse câu trả lời.
//
// Tin nhắn khách (input KHÔNG tin được) chỉ đi vào req.Prompt, và buildCLIArgv đã canh nó không
// thành một cờ bỏ sandbox — xem app_llm_cli.go. Lỗi trả về KHÔNG BAO GIỜ mang thân stderr: nó
// dựng từ tên CLI + loại lỗi, vì stderr của CLI hay chép lại khoá/đường dẫn (hợp đồng che của
// sanitizeProviderError).
//
// credential KHÔNG dùng: một CLI thuê bao mang phiên đăng nhập của riêng nó (ẩn ở ~/.codex,
// ~/.gemini, ~/.claude), không nhận khoá qua tham số. Router vẫn xoá nó (r.generate defer clear).
func (a *cliAdapter) Generate(ctx context.Context, req llmRequest, credential []byte) (llmResponse, error) {
	argv := buildCLIArgv(a.d, req)
	// claude đọc prompt qua stdin (Task 3); vendor khác đã có prompt trong argv.
	var stdin []byte
	if a.d.promptViaStdin {
		stdin = []byte(req.Prompt)
	}
	out, penalize, err := a.spawn(ctx, argv, stdin)
	if err != nil {
		var exit *cliExit
		if errors.As(err, &exit) {
			// classifyCLIError đọc stderr để PHÂN LOẠI, nhưng thân stderr KHÔNG đi vào thông báo:
			// chỉ tên CLI + loại lỗi ra ngoài.
			kind := classifyCLIError(exit.stderr, false)
			// Chỉ rate_limit mới phạt account (đặt cooldown); các loại khác penalize(false) = no-op.
			if penalize != nil {
				penalize(kind == llmErrorRateLimit)
			}
			return llmResponse{}, newLLMError(kind, err, "%s: gọi CLI hỏng", a.d.kind)
		}
		// Đã là llmError (chưa cài) hoặc lỗi ctx (huỷ/hết giờ) — trả nguyên để router phân loại.
		return llmResponse{}, err
	}
	return textOrUpstream(a.d.kind+" generate", parseCLIAnswer(a.d, out))
}

// spawn tách runner test khỏi spawn thật. Khi a.run != nil, seam thay TOÀN BỘ việc định vị +
// chạy tiến trình — adapter test không phụ thuộc CLI có cài trên máy hay không.
//
// Trả về thêm penalize (có thể nil) để Generate phạt account khi lỗi là rate_limit. accountEnv
// chạy TRƯỚC nhánh a.run: penalize phải có ở CẢ đường test (a.run) lẫn đường thật.
func (a *cliAdapter) spawn(ctx context.Context, argv []string, stdin []byte) ([]byte, func(bool), error) {
	var env []string
	var penalize func(bool)
	if a.accountEnv != nil {
		e, p, ok := a.accountEnv()
		if !ok {
			// 0 account enabled = chưa đăng nhập → credential (DỪNG chuỗi), y như not-installed.
			return nil, nil, newLLMError(llmErrorCredential, nil, "%s: chưa có tài khoản nào đăng nhập", a.d.kind)
		}
		env, penalize = e, p
	}
	if a.run != nil {
		out, err := a.run(ctx, argv, stdin)
		return out, penalize, err
	}
	program, prefixArgs, err := resolveCLIProgram(a.d)
	if err != nil {
		// Chưa cài → credential (cần cài + đăng nhập), loại DỪNG chuỗi: thử tiếp một CLI chưa cài
		// chỉ tốn thời gian của khách.
		return nil, penalize, newLLMError(classifyCLIError("", true), nil, "%s: CLI chưa cài", a.d.kind)
	}
	// exec.Command chứ không CommandContext: runCLIProcess tự xử lý huỷ ctx bằng killPidTree
	// (giết cả cây node→cli→cháu), còn CommandContext chỉ giết con trực tiếp và để cháu mồ côi.
	cmd := exec.Command(program, append(prefixArgs, argv...)...)
	if env != nil {
		cmd.Env = env
	}
	out, err := runCLIProcess(ctx, cmd, stdin, nil, a.logger)
	return out, penalize, err
}

// Test dò trạng thái đăng nhập — RẺ, không tốn một lượt subscription. loggedIn → nil; mọi trạng
// thái khác (chưa đăng nhập / không dò được) → lỗi credential để Portal nhắc đăng nhập lại.
func (a *cliAdapter) Test(ctx context.Context, _ string, _ []byte) error {
	switch checkCLIAuth(ctx, a.d, geminiCredPath(), a.logger) {
	case authLoggedIn:
		return nil
	default:
		return newLLMError(llmErrorCredential, nil, "%s: chưa đăng nhập hoặc không dò được trạng thái", a.d.kind)
	}
}

// Discover trả danh sách model TĨNH từ descriptor. Không I/O: vendor CLI không có endpoint liệt kê
// model, nên danh sách được ghim ở modelSeeds và Portal hiện đúng nó.
func (a *cliAdapter) Discover(_ context.Context, _ []byte) ([]store.LLMModel, error) {
	return descriptorModels(a.d.kind, a.providerID), nil
}

// descriptorModels dựng danh sách model TĨNH của một họ CLI, gắn providerID. Rỗng (nil) nếu kind
// không có descriptor hoặc descriptor không mang modelSeeds.
//
// Nguồn sự thật DUY NHẤT của phép ánh xạ modelSeeds → store.LLMModel: cliAdapter.Discover gọi nó cho
// codex/gemini-cli, còn claude-code — KHÔNG có adapter (định tuyến qua runClaude) — thì discover +
// gieo-lúc-khởi-động gọi thẳng hàm này. Mọi model đều nguồn discovered + available.
func descriptorModels(kind, providerID string) []store.LLMModel {
	d, ok := cliDescriptors[kind]
	if !ok || len(d.modelSeeds) == 0 {
		return nil
	}
	out := make([]store.LLMModel, 0, len(d.modelSeeds))
	for _, m := range d.modelSeeds {
		out = append(out, store.LLMModel{
			ProviderID: providerID, ModelID: m.id, Name: m.name,
			Source: store.LLMModelDiscovered, Available: true,
		})
	}
	return out
}

// ensureCLIProviderModels gieo model TĨNH của một CLI provider vào store. Idempotent:
// ReplaceLLMModels thay trọn nguồn discovered trong MỘT transaction, nên gọi lại không nhân đôi.
// No-op nếu kind không có seed.
//
// ponytail: nuốt lỗi ReplaceLLMModels — gieo best-effort, hỏng chỉ khiến model chưa hiện (không mất
// dữ liệu khách), và hai nơi gọi (startup, connect goroutine) đều không có đường trả lỗi hợp lý.
func ensureCLIProviderModels(st *store.Store, providerID, kind string) {
	if st == nil {
		return // không có store để gieo (registerAppRoutes gọi từ test route bằng api không DB)
	}
	if m := descriptorModels(kind, providerID); len(m) > 0 {
		_ = st.ReplaceLLMModels(providerID, store.LLMModelDiscovered, m)
	}
}

// parseCLIAnswer trích câu trả lời cuối từ output CLI, chọn cách parse theo vendor.
//
// codex: `codex exec` (KHÔNG --json) ghi ĐÚNG tin nhắn cuối ra stdout, còn toàn bộ event-log/
// reasoning ra stderr. Capture THẬT trên máy (2026-08-07): stdout khớp byte-for-byte với file
// `-o/--output-last-message`, cả với câu trả lời nhiều dòng UTF-8. Nên trim stdout là hợp đồng gọn
// nhất — không phải tạo file tạm mỗi lượt; `-o <file>` là dự phòng có tài liệu nếu một bản codex sau
// làm bẩn stdout.
// gemini-cli: `-o json` bọc câu trả lời trong {"response": ...} — trích .response (xem parseGeminiAnswer).
// claude-code: stream-json, event `result` — Task 11 gộp claude vào cliAdapter; CHƯA nối vào ở
// production nên chưa đi qua hàm này (Generate gọi parseCLIAnswer cho MỌI descriptor, nên khi nối sẽ
// phải thêm nhánh riêng cho stream-json, KHÔNG để rơi vào trim).
func parseCLIAnswer(d cliDescriptor, out []byte) string {
	if d.kind == "gemini-cli" {
		return parseGeminiAnswer(out)
	}
	// codex: stdout đã là câu trả lời sạch (log ở stderr); claude-code: Task 11.
	return strings.TrimSpace(string(out))
}

// parseGeminiAnswer trích .response từ output `-o json` của Gemini CLI; CHỈ fallback về raw đã trim
// khi output KHÔNG phải JSON (ví dụ output vendor khác lỡ đi nhầm vào đây).
//
// CHƯA kiểm chứng bằng output THẬT: Google khai tử đăng nhập cá nhân của gemini-cli (2026-08, "no
// longer supported for individuals" → Antigravity), nên không capture live được và Gemini đã bỏ khỏi
// onboarding (DORMANT). Key .response theo tài liệu `-o json` + quyết định research, KHÔNG phải giá
// trị đo thật. Khi có CLI Gemini đăng nhập lại được: capture `-o json` thật, xác nhận key, rồi ghim.
// ponytail: .response theo tài liệu, chưa verify live — Gemini dormant, verify lại khi hồi sinh.
func parseGeminiAnswer(out []byte) string {
	var v struct {
		Response string `json:"response"`
	}
	// Parse THÀNH CÔNG thì tin nó: .response rỗng ({} hay {"response":""}) → trả rỗng để textOrUpstream
	// báo "không có nội dung" (kích hoạt fallback), KHÔNG phun cả khối JSON ra làm "câu trả lời".
	if err := json.Unmarshal(out, &v); err == nil {
		return strings.TrimSpace(v.Response)
	}
	return strings.TrimSpace(string(out))
}

// resolveCLIProgram định vị chương trình chạy một vendor CLI.
//
//   - native (claude): exec.LookPath tên exe → (path, nil).
//   - npm (codex/gemini): chạy `node <binJS>`, với binJS nằm dưới npm global root (`npm root -g`).
//
// Định vị thật được Task 8 ghim (spawn thật + capture output); ở đây đủ để đường sản xuất dựng
// được lệnh. Test KHÔNG đi qua đây — a.run seam chặn trước.
func resolveCLIProgram(d cliDescriptor) (program string, prefixArgs []string, err error) {
	if d.nativeBin != "" {
		bin, err := exec.LookPath(d.nativeBin)
		if err != nil {
			return "", nil, err
		}
		return bin, nil, nil
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return "", nil, err
	}
	root, err := npmGlobalRoot()
	if err != nil {
		return "", nil, err
	}
	binJS := filepath.Join(root, d.binJS)
	if _, err := os.Stat(binJS); err != nil {
		return "", nil, err
	}
	return node, []string{binJS}, nil
}

// npmGlobalRoot đọc npm global root, nơi gói cài -g nằm.
//
// Cache MỘT lần cả đời daemon (OnceValues): root không đổi lúc chạy, còn `npm root -g` là một
// spawn KHÔNG có ctx nên ngân sách 25s mỗi Provider của router không huỷ được nó — không cache thì
// một npm cold-start chậm chạy trên MỌI lượt codex/gemini. Cache lại giới hạn cái treo đó về lần
// resolve đầu tiên.
var npmGlobalRoot = sync.OnceValues(func() (string, error) {
	out, err := exec.Command("npm", "root", "-g").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
})

// geminiCredPath là đường dò credential của Gemini CLI.
//
// Tên file XÁC NHẬN THẬT (2026-08-07): ~/.gemini/oauth_creds.json tồn tại trên máy từng đăng nhập
// (1822 byte). Cô lập ở MỘT hàm vì đây là chi tiết nội bộ của hãng, có thể vỡ khi Google đổi CLI.
// Chỉ nhánh gemini-file trong checkCLIAuth đọc tới; các vendor khác bỏ qua tham số này.
func geminiCredPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "oauth_creds.json")
}
