package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentdc/internal/store"
)

func TestClaudeDescriptorIsNPM(t *testing.T) {
	d := cliDescriptors["claude-code"]
	if d.npmPackage != "@anthropic-ai/claude-code" {
		t.Errorf("npmPackage = %q", d.npmPackage)
	}
	if d.npmBin == "" {
		t.Error("npmBin rỗng; want đường bin\\claude.exe")
	}
	// nativeBin đã bị xoá khỏi cliDescriptor (claude-code là descriptor cuối dùng nó) — không còn
	// trường để assert; việc file compile đã chứng minh không còn nơi nào đọc nativeBin.
}

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
		{"codex", "gpt-5.6-terra",
			[]string{"exec", "-s", "read-only", "--ignore-user-config",
				"model_reasoning_effort=low", "features.plugins=false", "features.skill_search=false",
				"-m", "gpt-5.6-terra"},
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

// TestParseCLIAnswer ghim parse codex theo output THẬT đã capture (testdata/codex-out.txt — chạy
// `codex exec` trên máy này, 2026-08-07, câu trả lời nhiều dòng UTF-8). KHÔNG bịa: fixture là stdout
// thật và khớp byte-for-byte với file `-o/--output-last-message`.
func TestParseCLIAnswer(t *testing.T) {
	codexOut, err := os.ReadFile(filepath.Join("testdata", "codex-out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := parseCLIAnswer(cliDescriptors["codex"], codexOut)
	if !strings.Contains(got, "tiêu hóa") {
		t.Errorf("parse codex = %q; muốn chứa câu trả lời thật", got)
	}
	// "chỉ trim, không đổi nội dung": kết quả đúng bằng fixture đã trim — bắt được nếu parser thêm/bớt
	// hay cắt xén gì (khác hẳn `got == TrimSpace(got)`, luôn đúng vì parser vốn đã trim → vô nghĩa).
	if want := strings.TrimSpace(string(codexOut)); got != want {
		t.Errorf("parse codex = %q; want %q (chỉ trim)", got, want)
	}
}

// TestParseGeminiAnswerExtractsResponseField kiểm LOGIC trích .response, KHÔNG phải tính tương thích
// với gemini THẬT: Google khai tử đăng nhập cá nhân của gemini-cli (2026-08 → Antigravity) nên KHÔNG
// capture live được, và Gemini đã bỏ khỏi onboarding (dormant). Input dưới đây là SHAPE theo tài liệu
// `-o json`, CỐ Ý dán nhãn tổng hợp — KHÔNG giả làm output đã đo. Khi Gemini hồi sinh (CLI Antigravity
// đăng nhập được): capture `-o json` thật, xác nhận key, rồi ghim lại.
func TestParseGeminiAnswerExtractsResponseField(t *testing.T) {
	// JSON có .response → trích đúng, đã trim.
	if got := parseGeminiAnswer([]byte(`{"response":"  4  ","stats":{}}`)); got != "4" {
		t.Errorf("parseGeminiAnswer(json .response) = %q; want %q", got, "4")
	}
	// fallback: không phải JSON → trả raw đã trim, KHÔNG nuốt câu trả lời.
	if got := parseGeminiAnswer([]byte("  4 khong-json  ")); got != "4 khong-json" {
		t.Errorf("parseGeminiAnswer(non-json) = %q; want raw đã trim", got)
	}
	// JSON hợp lệ nhưng .response rỗng ({}) → trả rỗng (để textOrUpstream báo upstream-empty), KHÔNG
	// phun khối JSON ra làm "câu trả lời".
	if got := parseGeminiAnswer([]byte("{}")); got != "" {
		t.Errorf("parseGeminiAnswer({}) = %q; want rỗng", got)
	}
}

// TestGeminiAuthProbe chốt ngữ nghĩa đã hoà giải giữa test và skeleton của plan: thiếu hẳn file →
// loggedOut (biết chắc chưa đăng nhập, đi login được — hành động đúng và thật); file có mặt nhưng
// KHÔNG đọc được nội dung (rỗng / khoá) → unknown, KHÔNG dám kết luận "chưa đăng nhập" khi ta chỉ là
// không đọc nổi; file có nội dung → loggedIn. Đoán "chưa đăng nhập" sai đẩy người dùng vào vòng login
// lại vô ích.
func TestGeminiAuthProbe(t *testing.T) {
	// Thiếu hẳn file (os.IsNotExist) → biết chắc chưa đăng nhập.
	missing := filepath.Join(t.TempDir(), "khong-ton-tai", "oauth_creds.json")
	if st := probeGeminiAuth(missing); st != authLoggedOut {
		t.Errorf("probeGeminiAuth(thiếu file) = %v; want authLoggedOut", st)
	}
	// File rỗng: có mặt nhưng không mang credential → unknown (KHÔNG phải loggedOut). Đây là nhánh
	// "có-nhưng-không-đọc-được" test được sạch trên Windows, thay cho việc ép quyền/khoá file.
	empty := filepath.Join(t.TempDir(), "oauth_creds.json")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if st := probeGeminiAuth(empty); st != authUnknown {
		t.Errorf("probeGeminiAuth(file rỗng) = %v; want authUnknown", st)
	}
	// File có nội dung → coi như đã đăng nhập.
	present := filepath.Join(t.TempDir(), "oauth_creds.json")
	if err := os.WriteFile(present, []byte(`{"access_token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := probeGeminiAuth(present); st != authLoggedIn {
		t.Errorf("probeGeminiAuth(file có) = %v; want authLoggedIn", st)
	}
}

// TestCodexAuthFromExit: exit 0 = đã đăng nhập, khác 0 = chưa. Test LOGIC không spawn (spawn codex
// qua node+codex.js được chốt ở task sau) — đúng theo kế hoạch: ưu tiên kiểm ánh xạ, không dựng CLI.
func TestCodexAuthFromExit(t *testing.T) {
	if got := codexAuthFromExit(0); got != authLoggedIn {
		t.Errorf("codexAuthFromExit(0) = %v; want authLoggedIn", got)
	}
	for _, code := range []int{1, 2, 127} {
		if got := codexAuthFromExit(code); got != authLoggedOut {
			t.Errorf("codexAuthFromExit(%d) = %v; want authLoggedOut", code, got)
		}
	}
}

// TestClaudeAuthFromJSON chốt shape thật của `claude auth status --json`. loggedIn vắng / output hỏng
// → unknown (không đoán loggedOut khi ta không chắc).
func TestClaudeAuthFromJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want authState
	}{
		{"đã đăng nhập", `{"loggedIn":true,"subscriptionType":"team"}`, authLoggedIn},
		{"chưa đăng nhập", `{"loggedIn":false}`, authLoggedOut},
		{"thiếu trường", `{"email":"x"}`, authUnknown},
		{"rác", `not json`, authUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeAuthFromJSON([]byte(tc.in)); got != tc.want {
				t.Errorf("claudeAuthFromJSON(%q) = %v; want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestCheckCLIAuthDispatch: dispatcher chọn đúng nhánh theo authMethod. Dùng nhánh gemini-file (dò
// file, không spawn) để kiểm việc phân nhánh mà không phụ thuộc CLI cài sẵn; codex-exit chưa wire
// spawn nên phải trả unknown (an toàn), KHÔNG loggedOut.
func TestCheckCLIAuthDispatch(t *testing.T) {
	present := filepath.Join(t.TempDir(), "oauth_creds.json")
	if err := os.WriteFile(present, []byte(`{"access_token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	discard := slog.New(slog.DiscardHandler)
	if got := checkCLIAuth(t.Context(), cliDescriptors["gemini-cli"], present, discard); got != authLoggedIn {
		t.Errorf("checkCLIAuth(gemini, file có) = %v; want authLoggedIn", got)
	}
	if got := checkCLIAuth(t.Context(), cliDescriptors["codex"], "", discard); got != authUnknown {
		t.Errorf("checkCLIAuth(codex) = %v; want authUnknown (chưa wire spawn)", got)
	}
}

// TestClaudeAuthLive chạy `claude auth status --json` THẬT qua checkCLIAuth. Skip sạch nếu claude
// vắng. Máy này claude ĐÃ đăng nhập → loggedIn; nếu máy khác đã logout thì skip thay vì fail giả.
func TestClaudeAuthLive(t *testing.T) {
	if _, _, err := resolveCLIProgram(cliDescriptors["claude-code"]); err != nil {
		t.Skip("claude chưa cài qua npm (@anthropic-ai/claude-code vắng ở npm root)")
	}
	got := checkCLIAuth(t.Context(), cliDescriptors["claude-code"], "", slog.New(slog.DiscardHandler))
	switch got {
	case authLoggedIn: // đúng kỳ vọng máy này
	case authLoggedOut:
		t.Skip("claude cài nhưng đã logout — bỏ qua (test này cần trạng thái đăng nhập)")
	default:
		t.Fatalf("checkCLIAuth(claude) = %v; muốn một trạng thái xác định (spawn/parse hỏng?)", got)
	}
}

// TestCodexAuthLive chạy `codex login status` THẬT rồi ánh xạ exit qua codexAuthFromExit. Resolve
// codex.js là việc nặng nên để trong test (skip nếu vắng); production checkCLIAuth CHƯA wire nhánh
// này. Máy này codex ĐÃ logout → exit khác 0 → loggedOut.
func TestCodexAuthLive(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node không có trên PATH")
	}
	// node global root trên Windows là <nodedir>\node_modules; binJS là đường dẫn tương đối trong đó.
	codexJS := filepath.Join(filepath.Dir(node), "node_modules", cliDescriptors["codex"].binJS)
	if _, err := os.Stat(codexJS); err != nil {
		t.Skipf("không thấy codex.js ở %s — bỏ qua", codexJS)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	code := 0
	if err := exec.CommandContext(ctx, node, codexJS, "login", "status").Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Skipf("chạy codex login status lỗi ngoài exit code: %v", err)
		}
		code = ee.ExitCode()
	}
	if got := codexAuthFromExit(code); got != authLoggedOut {
		t.Skipf("codex login status exit=%d → %v (máy này kỳ vọng logged-out; có thể đã đăng nhập)", code, got)
	}
}

// --- adapter: run seam tiêm, KHÔNG spawn CLI thật ---

// TestCLIAdapterGenerateBuildsArgvAndReturnsTrimmedText: Generate dựng argv của descriptor, chạy
// qua seam a.run, và trả câu trả lời codex = stdout đã trim (đã ghim theo capture thật, Task 8).
// codex đọc prompt qua argv.
func TestCLIAdapterGenerateBuildsArgvAndReturnsTrimmedText(t *testing.T) {
	var gotArgv []string
	var gotStdin []byte
	a := &cliAdapter{
		d: cliDescriptors["codex"], providerID: "codex-1", logger: slog.New(slog.DiscardHandler),
		run: func(_ context.Context, argv []string, stdin []byte) ([]byte, error) {
			gotArgv, gotStdin = argv, stdin
			return []byte("  codex trả lời  "), nil
		},
	}
	resp, err := a.Generate(t.Context(), llmRequest{Model: "gpt-5.4", Prompt: "khách hỏi giá"}, []byte("cred"))
	if err != nil {
		t.Fatalf("Generate() = _, %v; want nil", err)
	}
	if resp.Text != "codex trả lời" {
		t.Errorf("Generate().Text = %q; want %q (parse tạm trim stdout)", resp.Text, "codex trả lời")
	}
	for _, w := range []string{"exec", "-s", "read-only", "-m", "gpt-5.4"} {
		if !slices.Contains(gotArgv, w) {
			t.Errorf("argv %v thiếu %q", gotArgv, w)
		}
	}
	if last := gotArgv[len(gotArgv)-1]; last != "khách hỏi giá" {
		t.Errorf("argv cuối = %q; want prompt của khách (sau `--`)", last)
	}
	// codex đọc prompt qua argv, KHÔNG qua stdin.
	if gotStdin != nil {
		t.Errorf("codex stdin = %q; want nil", gotStdin)
	}
}

// TestCLIAdapterGenerateFeedsClaudePromptViaStdin: claude bật promptViaStdin, nên prompt đi qua
// stdin chứ không nằm trong argv.
func TestCLIAdapterGenerateFeedsClaudePromptViaStdin(t *testing.T) {
	var gotStdin []byte
	a := &cliAdapter{
		d: cliDescriptors["claude-code"], providerID: "claude-code", logger: slog.New(slog.DiscardHandler),
		run: func(_ context.Context, _ []string, stdin []byte) ([]byte, error) {
			gotStdin = stdin
			return []byte("claude trả lời"), nil
		},
	}
	if _, err := a.Generate(t.Context(), llmRequest{Model: "sonnet", Prompt: "câu hỏi"}, nil); err != nil {
		t.Fatalf("Generate() = _, %v; want nil", err)
	}
	if string(gotStdin) != "câu hỏi" {
		t.Errorf("claude stdin = %q; want prompt của khách", gotStdin)
	}
}

func TestCLIAdapterGeneratePreservesCancellationDuringProgramDiscovery(t *testing.T) {
	a := newCLIAdapter(
		cliDescriptors["claude-code"], "claude-code", slog.New(slog.DiscardHandler),
	)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := a.Generate(ctx, llmRequest{Model: "sonnet", Prompt: "câu hỏi"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() discovery error = %v; want context.Canceled", err)
	}
}

// TestCLIAdapterGenerateClassifiesExitWithoutLeakingStderr: một *cliExit được phân loại theo
// stderr, nhưng thân stderr KHÔNG BAO GIỜ vào thông báo lỗi (hợp đồng che).
func TestCLIAdapterGenerateClassifiesExitWithoutLeakingStderr(t *testing.T) {
	const stderr = "You've hit your usage limit. sk-canary-leak"
	a := &cliAdapter{
		d: cliDescriptors["codex"], providerID: "codex-1", logger: slog.New(slog.DiscardHandler),
		run: func(context.Context, []string, []byte) ([]byte, error) {
			return nil, &cliExit{err: errors.New("exit status 1"), stderr: stderr}
		},
	}
	_, err := a.Generate(t.Context(), llmRequest{Model: "gpt-5.4", Prompt: "x"}, nil)
	if err == nil {
		t.Fatal("Generate() với cliExit = _, nil; want lỗi")
	}
	var le *llmError
	if !errors.As(err, &le) {
		t.Fatalf("Generate() error = %T; want *llmError", err)
	}
	// "usage limit" → rate_limit (đi tiếp), KHÔNG credential (dừng chuỗi).
	if le.Kind != llmErrorRateLimit {
		t.Errorf("Generate() error kind = %q; want rate_limit", le.Kind)
	}
	if strings.Contains(le.Error(), "usage limit") || strings.Contains(le.Error(), "sk-canary-leak") {
		t.Errorf("Generate() error mang thân stderr: %q", le.Error())
	}
}

// TestCLIAdapterTestReturnsCredentialErrorWhenAuthUnknown: codex-exit chưa wire spawn nên
// checkCLIAuth trả authUnknown; Test ánh xạ mọi trạng thái khác loggedIn thành lỗi credential.
func TestCLIAdapterTestReturnsCredentialErrorWhenAuthUnknown(t *testing.T) {
	a := newCLIAdapter(cliDescriptors["codex"], "codex-1", slog.New(slog.DiscardHandler))
	err := a.Test(t.Context(), "gpt-5.4", nil)
	if err == nil {
		t.Fatal("Test(codex, auth unknown) = nil; want lỗi credential")
	}
	var le *llmError
	if !errors.As(err, &le) || le.Kind != llmErrorCredential {
		t.Errorf("Test() error = %v; want *llmError kind=credential", err)
	}
}

// TestCLIAdapterDiscoverReturnsDescriptorSeeds: Discover TĨNH — trả đúng modelSeeds gắn providerID.
func TestCLIAdapterDiscoverReturnsDescriptorSeeds(t *testing.T) {
	a := newCLIAdapter(cliDescriptors["gemini-cli"], "gemini-cli-1", slog.New(slog.DiscardHandler))
	models, err := a.Discover(t.Context(), nil)
	if err != nil {
		t.Fatalf("Discover() = _, %v; want nil", err)
	}
	seeds := cliDescriptors["gemini-cli"].modelSeeds
	if len(models) != len(seeds) {
		t.Fatalf("Discover() trả %d model; want %d (bằng modelSeeds)", len(models), len(seeds))
	}
	for i, m := range models {
		if m.ProviderID != "gemini-cli-1" {
			t.Errorf("model[%d].ProviderID = %q; want gemini-cli-1", i, m.ProviderID)
		}
		if m.ModelID != seeds[i].id || m.Name != seeds[i].name {
			t.Errorf("model[%d] = %s/%s; want %s/%s", i, m.ModelID, m.Name, seeds[i].id, seeds[i].name)
		}
		if m.Source != store.LLMModelDiscovered || !m.Available {
			t.Errorf("model[%d] source/available = %s/%v; want discovered/true", i, m.Source, m.Available)
		}
	}
}

// TestDescriptorModelsClaudeCode: helper gieo model TĨNH cho claude-code (không adapter) trả đúng
// 4 seed haiku/sonnet/opus/fable, gắn providerID, nguồn discovered/available.
func TestDescriptorModelsClaudeCode(t *testing.T) {
	models := descriptorModels("claude-code", "claude-code")
	seeds := cliDescriptors["claude-code"].modelSeeds
	if len(models) != len(seeds) || len(models) != 4 {
		t.Fatalf("descriptorModels(claude-code) trả %d model; want 4 (= modelSeeds)", len(models))
	}
	for i, m := range models {
		if m.ProviderID != "claude-code" {
			t.Errorf("model[%d].ProviderID = %q; want claude-code", i, m.ProviderID)
		}
		if m.ModelID != seeds[i].id || m.Name != seeds[i].name {
			t.Errorf("model[%d] = %s/%s; want %s/%s", i, m.ModelID, m.Name, seeds[i].id, seeds[i].name)
		}
		if m.Source != store.LLMModelDiscovered || !m.Available {
			t.Errorf("model[%d] source/available = %s/%v; want discovered/true", i, m.Source, m.Available)
		}
	}
}

// TestDescriptorModelsUnknownKind: kind không có descriptor → nil (không có model để gieo).
func TestDescriptorModelsUnknownKind(t *testing.T) {
	if m := descriptorModels("openai", "x"); m != nil {
		t.Errorf("descriptorModels(openai) = %v; want nil (không có descriptor)", m)
	}
}

// TestEnsureCLIProviderModelsPopulatesClaudeCode: sau ensureCLIProviderModels, store có đủ model
// tĩnh của claude-code (đường mà connect + startup gọi để detail/combo hiện model).
func TestEnsureCLIProviderModelsPopulatesClaudeCode(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf(`store.Open(":memory:") = %v; want nil`, err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ensureCLIProviderModels(st, slog.New(slog.DiscardHandler), "claude-code", "claude-code")

	models, err := st.LLMModels("claude-code")
	if err != nil {
		t.Fatalf("LLMModels(claude-code) = %v; want nil", err)
	}
	if len(models) != 4 {
		t.Fatalf("LLMModels(claude-code) trả %d model; want 4", len(models))
	}
}

// codexCacheFixture ghi một models_cache.json giả (đúng shape thật của codex) vào configDir và trả
// đường dẫn thư mục — dùng chung cho các test đọc cache live.
func codexCacheFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const cache = `{
	  "fetched_at": "2026-08-08T00:00:00Z",
	  "models": [
	    {"slug": "gpt-5.6-sol", "display_name": "GPT-5.6-Sol", "visibility": "list", "supported_in_api": true},
	    {"slug": "gpt-5.6-sol-wm", "display_name": "GPT-5.6-Sol-WM", "visibility": "hide", "supported_in_api": false},
	    {"slug": "gpt-5.6-terra", "display_name": "GPT-5.6-Terra", "visibility": "list", "supported_in_api": true},
	    {"slug": "codex-auto-review", "display_name": "Codex Auto Review", "visibility": "hide", "supported_in_api": true},
	    {"slug": "no-name", "display_name": "", "visibility": "list", "supported_in_api": true}
	  ]
	}`
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatalf("ghi fixture cache = %v; want nil", err)
	}
	return dir
}

// TestCodexCachedModelsFiltersVisibility: chỉ visibility=="list" đi qua (bỏ *-wm, auto-review ẩn);
// display_name rỗng thì rơi về slug làm tên. Đây là hợp đồng đọc cache LIVE của codex.
func TestCodexCachedModelsFiltersVisibility(t *testing.T) {
	got := codexCachedModels(codexCacheFixture(t))
	want := []cliModel{
		{"gpt-5.6-sol", "GPT-5.6-Sol"},
		{"gpt-5.6-terra", "GPT-5.6-Terra"},
		{"no-name", "no-name"}, // display_name rỗng → slug
	}
	if !slices.Equal(got, want) {
		t.Fatalf("codexCachedModels = %v; want %v", got, want)
	}
	// Thiếu file / dir rỗng → nil (caller fallback seed).
	if m := codexCachedModels(t.TempDir()); m != nil {
		t.Errorf("codexCachedModels(empty dir) = %v; want nil", m)
	}
	if m := codexCachedModels(""); m != nil {
		t.Errorf("codexCachedModels(\"\") = %v; want nil", m)
	}
}

// TestCliProviderModelsPrefersLiveCache: có account codex với cache đọc được → cliProviderModels trả
// list LIVE (gpt-5.6-sol — KHÔNG có trong seed tĩnh), chứng minh đường xanh thay seed bằng cache thật.
func TestCliProviderModelsPrefersLiveCache(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf(`store.Open(":memory:") = %v; want nil`, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_ = st.CreateLLMProvider(store.LLMProvider{ID: "codex", Name: "Codex", Kind: "codex", Enabled: true})
	_ = st.CreateLLMAccount(store.LLMAccount{ID: "a1", ProviderID: "codex", Label: "x",
		ConfigDir: codexCacheFixture(t), Enabled: true})

	got := cliProviderModels(st, "codex", "codex")
	ids := make([]string, len(got))
	for i, m := range got {
		ids[i] = m.ModelID
	}
	if !slices.Equal(ids, []string{"gpt-5.6-sol", "gpt-5.6-terra", "no-name"}) {
		t.Fatalf("cliProviderModels(live) ids = %v; want live cache list", ids)
	}
	if !slices.Contains(ids, "gpt-5.6-sol") {
		t.Errorf("list live thiếu gpt-5.6-sol (flagship không có trong seed tĩnh)")
	}
}

// TestCliProviderModelsFallsBackToSeeds: không account nào có cache → cliProviderModels trả seed TĨNH
// của descriptor (đường mới connect, codex chưa chạy lượt nào để ghi cache).
func TestCliProviderModelsFallsBackToSeeds(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf(`store.Open(":memory:") = %v; want nil`, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_ = st.CreateLLMProvider(store.LLMProvider{ID: "codex", Name: "Codex", Kind: "codex", Enabled: true})
	_ = st.CreateLLMAccount(store.LLMAccount{ID: "a1", ProviderID: "codex", Label: "x",
		ConfigDir: t.TempDir(), Enabled: true}) // dir rỗng, không cache

	got := cliProviderModels(st, "codex", "codex")
	want := descriptorModels("codex", "codex")
	if len(got) != len(want) || len(got) == 0 {
		t.Fatalf("cliProviderModels(fallback) trả %d model; want %d (= seed)", len(got), len(want))
	}
	for i := range want {
		if got[i].ModelID != want[i].ModelID {
			t.Errorf("model[%d] = %s; want seed %s", i, got[i].ModelID, want[i].ModelID)
		}
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

// stillAlive poll xem pid còn ĐANG CHẠY không. Chỉ OpenProcess thành công là chưa đủ: Windows giữ
// process object đã terminate khi vẫn còn handle tham chiếu tới nó. Helper theo nền tảng dùng
// WaitForSingleObject trên Windows để phân biệt running với terminated-but-not-yet-reaped.
func stillAlive(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !cliTestProcessRunning(pid) {
			return false
		}
		if time.Now().After(deadline) {
			return true // vẫn mở được sau timeout → coi như còn sống
		}
		time.Sleep(50 * time.Millisecond)
	}
}

type hangingNPMDiscovery struct {
	markerPath  string
	releasePath string
}

func newHangingNPMDiscovery(t *testing.T) *hangingNPMDiscovery {
	t.Helper()
	npmRootCache.mu.Lock()
	originalCachedRoot := npmRootCache.root
	npmRootCache.root = ""
	npmRootCache.mu.Unlock()
	t.Cleanup(func() {
		npmRootCache.mu.Lock()
		npmRootCache.root = originalCachedRoot
		npmRootCache.mu.Unlock()
	})

	binDir := t.TempDir()
	fixture := &hangingNPMDiscovery{
		markerPath:  filepath.Join(t.TempDir(), "first-started"),
		releasePath: filepath.Join(t.TempDir(), "release-first"),
	}
	root := filepath.Join(t.TempDir(), "npm-root")
	for _, d := range []cliDescriptor{cliDescriptors["claude-code"], cliDescriptors["gemini-cli"]} {
		binPath := d.npmBin
		if binPath == "" {
			binPath = d.binJS
		}
		path := filepath.Join(root, binPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		stub := []byte("not an executable")
		if runtime.GOOS != "windows" {
			stub = []byte("#!/bin/sh\nexit 1\n")
		}
		if err := os.WriteFile(path, stub, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	npmName := "npm"
	npmScript := "#!/bin/sh\n" +
		"if [ ! -f \"$NPM_TEST_MARKER\" ]; then\n" +
		"  : > \"$NPM_TEST_MARKER\"\n" +
		"  while [ ! -f \"$NPM_TEST_RELEASE\" ]; do sleep 0.02; done\n" +
		"fi\n" +
		"printf '%s\\n' \"$NPM_TEST_ROOT\"\n"
	if runtime.GOOS == "windows" {
		npmName = "npm.cmd"
		npmScript = "@echo off\r\n" +
			"if exist \"%NPM_TEST_MARKER%\" goto ready\r\n" +
			"type nul > \"%NPM_TEST_MARKER%\"\r\n" +
			":wait\r\n" +
			"if exist \"%NPM_TEST_RELEASE%\" goto ready\r\n" +
			"\"%SystemRoot%\\System32\\ping.exe\" -n 1 -w 20 127.0.0.1 >nul\r\n" +
			"goto wait\r\n" +
			":ready\r\n" +
			"echo %NPM_TEST_ROOT%\r\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, npmName), []byte(npmScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NPM_TEST_MARKER", fixture.markerPath)
	t.Setenv("NPM_TEST_RELEASE", fixture.releasePath)
	t.Setenv("NPM_TEST_ROOT", root)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(fixture.release)
	return fixture
}

func (f *hangingNPMDiscovery) waitStarted(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(f.markerPath); err == nil {
			return
		}
		if time.Now().After(deadline) {
			f.release()
			t.Fatal("npm discovery did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *hangingNPMDiscovery) release() {
	_ = os.WriteFile(f.releasePath, []byte("release"), 0o600)
}

func TestNPMRootDiscoveryHonorsCancellationAndDoesNotBlockLaterCallers(t *testing.T) {
	fixture := newHangingNPMDiscovery(t)

	firstCtx, cancelFirst := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelFirst()
	firstDone := make(chan authState, 1)
	go func() {
		firstDone <- probeClaudeAuth(firstCtx, cliDescriptors["claude-code"], slog.New(slog.DiscardHandler))
	}()
	fixture.waitStarted(t)

	secondCtx, cancelSecond := context.WithTimeout(t.Context(), time.Second)
	defer cancelSecond()
	secondDone := make(chan authState, 1)
	go func() {
		secondDone <- probeClaudeAuth(secondCtx, cliDescriptors["claude-code"], slog.New(slog.DiscardHandler))
	}()

	failure := ""
	select {
	case <-secondDone:
	case <-time.After(1500 * time.Millisecond):
		failure = "later npm discovery was blocked by the hung first call"
		fixture.release()
	}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		if failure == "" {
			failure = "first npm discovery ignored its canceled context"
		}
		fixture.release()
	}
	if failure != "" {
		t.Fatal(failure)
	}
}
