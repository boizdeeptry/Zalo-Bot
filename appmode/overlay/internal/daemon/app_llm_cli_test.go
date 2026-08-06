package daemon

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCLIRegistryHasThreeVendors(t *testing.T) {
	got := make([]string, 0, len(cliDescriptors))
	for kind := range cliDescriptors {
		got = append(got, kind)
	}
	slices.Sort(got)
	want := []string{"claude-code", "codex", "gemini-cli"}
	if !slices.Equal(got, want) {
		t.Fatalf("cliDescriptors kinds = %v; want %v", got, want)
	}
	for kind, d := range cliDescriptors {
		if d.kind != kind {
			t.Errorf("map key %q != d.kind %q", kind, d.kind)
		}
		if d.readOnlyArgs == nil {
			t.Errorf("%s: readOnlyArgs nil — mọi vendor phải có cờ chỉ-đọc", kind)
		}
		if len(d.modelSeeds) == 0 {
			t.Errorf("%s: modelSeeds rỗng — Discover trả danh sách tĩnh", kind)
		}
	}
}

// TestCLIArgvIsReadOnlyAndNeverBypassesSandbox chốt tính chất an ninh: tin nhắn khách chảy vào
// req.Prompt là input không tin được, mà argv dựng ra không bao giờ được mang một cờ bỏ sandbox.
func TestCLIArgvIsReadOnlyAndNeverBypassesSandbox(t *testing.T) {
	cases := []struct {
		kind, model              string
		wantContains, wantAbsent []string
	}{
		{"codex", "gpt-5.4",
			[]string{"exec", "-s", "read-only", "-m", "gpt-5.4"},
			[]string{"--dangerously-bypass-approvals-and-sandbox", "workspace-write", "danger-full-access", "-y", "--yolo"}},
		{"gemini-cli", "gemini-2.5-pro",
			[]string{"-p", "--approval-mode", "plan", "-m", "gemini-2.5-pro"},
			[]string{"-y", "--yolo", "auto_edit"}},
		{"claude-code", "sonnet",
			[]string{"--allowed-tools", "Read", "Grep", "Glob", "WebFetch", "--model", "sonnet"},
			[]string{"--dangerously-skip-permissions", "--permission-mode", "bypassPermissions"}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			d := cliDescriptors[tc.kind]
			argv := buildCLIArgv(d, llmRequest{Model: tc.model, Prompt: "khách hỏi giá"})
			joined := strings.Join(argv, " ")
			for _, w := range tc.wantContains {
				if !slices.Contains(argv, w) {
					t.Errorf("argv %v thiếu %q", argv, w)
				}
			}
			// Ghim những cờ xấu đã biết theo plan.
			for _, b := range tc.wantAbsent {
				if strings.Contains(joined, b) {
					t.Errorf("argv %q chứa cờ bỏ sandbox %q — cấm tuyệt đối", joined, b)
				}
			}
			// Mạnh hơn danh sách chép tay: KHÔNG cờ bỏ sandbox nào của chính vendor được lọt,
			// nên bảo đảm bám theo registry, thêm cờ cấm mới là tự động được canh.
			for _, b := range d.bannedArgs {
				if strings.Contains(joined, b) {
					t.Errorf("argv %q chứa bannedArgs[%q] của vendor — cấm tuyệt đối", joined, b)
				}
			}
		})
	}
}

// TestCodexPromptStartingWithDashIsShieldedFromClap chốt lỗ tiêm cờ qua positional trần của codex:
// một tin nhắn khách bắt đầu bằng "--" phải nằm sau "--" để clap coi nó là dữ liệu, không phải cờ.
func TestCodexPromptStartingWithDashIsShieldedFromClap(t *testing.T) {
	argv := buildCLIArgv(cliDescriptors["codex"], llmRequest{Model: "gpt-5.4",
		Prompt: "--dangerously-bypass-approvals-and-sandbox"})
	// Prompt độc phải nằm SAU một phần tử "--", để clap coi nó là dữ liệu chứ không phải cờ.
	sep := slices.Index(argv, "--")
	if sep < 0 || sep != len(argv)-2 {
		t.Fatalf("argv %v: prompt bare-positional phải có `--` ngay trước nó", argv)
	}
	if argv[len(argv)-1] != "--dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("argv %v: prompt phải là phần tử cuối, sau `--`", argv)
	}
}

// TestCLIRunKillsTheWholeTreeOnCancel chứng minh huỷ ctx giết CẢ CÂY node→cháu, không để mồ côi.
// Nếu chỉ giết con trực tiếp (exec.CommandContext / cmd.Process.Kill) thì node chết nhưng CHÁU sống
// tiếp — đúng bug Task 5. Test bắt pid CHÁU (không phải node) rồi khẳng định pid đó chết sau huỷ.
func TestCLIRunKillsTheWholeTreeOnCancel(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node không có trên PATH")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "tree.js")
	pidFile := filepath.Join(dir, "child.pid")
	// node đẻ một tiến trình CHÁU ngủ vô hạn, DETACHED + unref → cháu SỐNG SÓT khi node chết (mồ côi
	// thật, đúng bug node→codex→[con] Task 5). Không detached thì Windows kéo cháu chết theo node và
	// test thành vô nghĩa: cả giết-cây lẫn giết-con-trực-tiếp đều trông như nhau. Pid cháu ghi ra FILE
	// vì runCLIProcess chiếm stdout/stderr và trên đường huỷ không trả chúng ra.
	js := `const {spawn}=require("child_process");const fs=require("fs");` +
		`const c=spawn(process.execPath,["-e","setInterval(()=>{},1e9)"],{stdio:"ignore",detached:true});` +
		`c.unref();` +
		`fs.writeFileSync(process.argv[2],String(c.pid));` +
		`setInterval(()=>{},1e9);`
	if err := os.WriteFile(script, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	// Goroutine thoát khi runCLIProcess trả về (sau cancel → killPidTree → cmd.Wait trả về).
	go func() {
		defer close(done)
		_, _ = runCLIProcess(ctx, exec.Command(node, script, pidFile), nil, nil, slog.New(slog.DiscardHandler))
	}()

	grandchild := readGrandchildPID(t, pidFile)
	// Dọn rác dù test có fail (ví dụ khi CHÁU mồ côi còn sống): killPidTree trên pid đã chết trả nil.
	t.Cleanup(func() { _ = killPidTree(grandchild, "test-cleanup", slog.New(slog.DiscardHandler)) })

	cancel()
	if stillAlive(grandchild, 3*time.Second) {
		t.Fatalf("tiến trình cháu %d còn sống sau huỷ — cây bị mồ côi", grandchild)
	}
	<-done // đảm bảo runCLIProcess (và cmd.Wait bên trong) đã kết thúc trước khi test rời đi
}

// TestClassifyCLIError chốt việc ánh xạ output CLI về taxonomy: hết hạn mức → rate_limit (đi tiếp),
// chưa cài / chưa đăng nhập → credential (dừng chuỗi), và mơ hồ mặc định rate_limit — không bao giờ
// rơi vào một loại chặn-chuỗi.
func TestClassifyCLIError(t *testing.T) {
	cases := []struct {
		name, stderr string
		notInstalled bool
		want         llmErrorKind
	}{
		{"chưa cài", "", true, llmErrorCredential},
		{"chưa đăng nhập codex", "Not logged in", false, llmErrorCredential},
		{"hết hạn mức", "You've hit your usage limit. Try again later.", false, llmErrorRateLimit},
		{"mơ hồ mặc định rate_limit", "some unrecognized failure", false, llmErrorRateLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyCLIError(tc.stderr, tc.notInstalled)
			if got != tc.want {
				t.Errorf("classifyCLIError(%q, installed=%v) = %q; want %q", tc.stderr, !tc.notInstalled, got, tc.want)
			}
		})
	}
}

// readGrandchildPID poll file cho tới khi node ghi xong pid cháu rồi parse. Poll ngắn có giới hạn:
// đang chờ một tiến trình NGOẠI khởi động, không có channel để đợi.
func readGrandchildPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("node không ghi pid cháu vào %s trong 5s", pidFile)
	return 0
}

// stillAlive poll xem pid còn là tiến trình sống trên Windows không. Tái dùng processStart
// (procid_windows.go): OpenProcess thất bại (ok=false) nghĩa là pid đã biến mất. Poll ngắn hợp lệ
// vì đây là tiến trình NGOẠI (cháu do node đẻ, không phải con của test) — không channel nào để đợi
// cái chết của nó, khác hẳn time.Sleep dùng để đồng bộ.
func stillAlive(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, ok := processStart(pid); !ok {
			return false // tiến trình đã biến mất
		}
		if time.Now().After(deadline) {
			return true // vẫn mở được sau timeout → coi như còn sống
		}
		time.Sleep(50 * time.Millisecond)
	}
}
