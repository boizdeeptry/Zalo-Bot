package daemon

// cliDescriptor là DỮ LIỆU cho một vendor CLI, không phải code. Adapter thân duy nhất đọc nó.
// Học từ 9Router: provider là descriptor, không phải một nhánh switch.
type cliDescriptor struct {
	kind           string // kind của họ provider local_cli MỚI. claude-code CỐ Ý dùng gạch nối, khác hàng seeded cũ kind=claude_code (gạch dưới) — Task 11 đổi tên claude_code → claude-code.
	display        string
	npmPackage     string   // gói cài; rỗng nếu là native exe (claude)
	binJS          string   // đường dẫn tương đối tới entry .js trong node_modules; rỗng nếu native
	nativeBin      string   // tên exe khi là native (claude); rỗng nếu chạy qua node
	subArgs        []string // lệnh con không tương tác: {"exec"} codex, nil cho -p
	promptViaStdin bool     // true: prompt qua stdin; false: positional arg
	modelFlag      string
	readOnlyArgs   []string   // cờ giới hạn chỉ-đọc — BẮT BUỘC có
	bannedArgs     []string   // cờ bỏ sandbox — test chặn adapter dựng chúng
	authMethod     string     // "claude-json" | "codex-exit" | "gemini-file"
	modelSeeds     []cliModel // Discover tĩnh
	claudeBudget   bool       // true = dùng ctx gốc, không đặt dưới 25s (chỉ claude-code)
}

type cliModel struct{ id, name string }

// Verified trên --help bản đang cài (2026-08-06). KHÔNG bê nguyên list proxy của 9Router.
var cliDescriptors = map[string]cliDescriptor{
	"codex": {
		kind: "codex", display: "OpenAI Codex (ChatGPT)",
		npmPackage: "@openai/codex", binJS: `@openai\codex\bin\codex.js`,
		subArgs: []string{"exec"}, promptViaStdin: false, modelFlag: "-m",
		readOnlyArgs: []string{"-s", "read-only", "--skip-git-repo-check", "--ephemeral", "--color", "never"},
		bannedArgs:   []string{"--dangerously-bypass-approvals-and-sandbox", "workspace-write", "danger-full-access"},
		authMethod:   "codex-exit",
		modelSeeds:   []cliModel{{"gpt-5.5", "GPT-5.5"}, {"gpt-5.4", "GPT-5.4"}, {"gpt-5.4-mini", "GPT-5.4 mini"}},
	},
	"gemini-cli": {
		kind: "gemini-cli", display: "Gemini CLI (Google AI)",
		npmPackage: "@google/gemini-cli", binJS: `@google\gemini-cli\bundle\gemini.js`,
		subArgs: nil, promptViaStdin: false, modelFlag: "-m",
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
