package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os/exec"
)

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
	promptFlag     string   // cờ đứng trước prompt trong argv: "-p" gemini; rỗng = positional (codex)
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
