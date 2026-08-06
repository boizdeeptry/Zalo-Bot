package daemon

import (
	"slices"
	"strings"
	"testing"
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
