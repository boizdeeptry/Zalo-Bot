# CLI subscription providers — engine — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Make the operator's Claude / ChatGPT / Google AI subscriptions usable as entries in the existing fallback chain, by spawning the vendor's own already-authenticated CLI, so a turn costs subscription quota instead of per-token API billing.

**TDD mode:** yes

**Architecture:** A new `local_cli` provider family implements the existing `providerAdapter{Generate, Test, Discover}` interface by spawning a process instead of making an HTTP call. Three vendors (`codex`, `gemini-cli`, `claude-code`) are declarative descriptors — binary/argv/auth-method/model-seeds as data, one adapter body. The router is unchanged; it already knows only `providerAdapter`. The Claude Code path, today a hardcoded special case, folds into this family last. The empty-route invariant (Claude-Code-must-be-last) is lifted so a chain can be all-subscription. This plan is the ENGINE; the zero-terminal install+login UX is a follow-on plan (see "Scope" below).

**Tech stack:** Go + `os/exec` (spawn), the base-app `killPidTree`/`runTaskkillTree` (`AgentDC/internal/daemon/headless.go`, `taskkill /T`) for tree cancellation; SQLite through the existing store; browser-native ES modules for the Models page change. CLIs verified on this machine: `claude` 2.1.223, `codex` 0.146.1, `gemini` 0.54.0. Invoke npm-installed CLIs as `node.exe <package>/<bin>.js` — never the `.ps1`/`.cmd` shim (Go cannot run `.ps1`; `.cmd` carries CVE-2024-24576). `claude` is a native exe, spawned directly.

**Spec:** `.planning/specs/2026-08-06-cli-subscription-providers-design.md`

**Research:** `.planning/research/cli-subscription-providers-RESEARCH.md` (21 decisions, measured on-machine)

**Scope — what this plan does NOT cover (follow-on "onboarding" plan):** bundling npm into the package (`app\node` ships only `node.exe`), install-on-demand when a CLI is absent, in-Portal login driving with device-auth, and the disconnected→connected Providers-page state machine. This plan delivers a working engine: with the CLIs installed and logged in (manually, during development), subscriptions route. The Providers page here only *detects and displays* auth status read-only. Onboarding automates the rest.

---

## File structure

### New — the local_cli adapter family (overlay)

- Create `appmode/overlay/internal/daemon/app_llm_cli.go` — descriptor registry, argv construction, spawn+parse+classify, auth detection. Parallels `app_llm_http.go` but spawns a process.
- Create `appmode/overlay/internal/daemon/app_llm_cli_test.go` — table tests for argv, error classification, auth detection, and the kill-tree cancellation.

### Modified — lift the Claude-Code-last invariant (§6)

- Modify `appmode/overlay/internal/store/app_llm.go` — `validateLLMRoute` (~line 450) and `BootstrapClaudeRoute` (~417).
- Modify `appmode/overlay/internal/store/app_llm_test.go` — replace the tests that pin the old invariant.
- Modify `appmode/overlay/internal/daemon/app_llm_router.go` — drop the `entries[len-1] == claude-code` tail assumption (~line 114); register `local_cli` kinds in `newLLMAdapter` (~290); route attachments through the first eligible `local_cli` (§7, `threadHasAttachments`/`HasAttachments`).
- Modify `appmode/overlay/internal/daemon/app_llm_router_test.go` — router tests for the lifted invariant, attachment-through-cli, and the updated canary.
- Modify `appmode/overlay/internal/webui/static/pages/models.js` — remove the Claude-Code control-lock privilege (`isLocked`/`tailIndex`, `CLAUDE_ID`).
- Modify `appmode/tests/models.test.mjs` — replace the "Claude Code is locked last" tests.

### Modified — §5.2 Claude Code merge (last, riskiest)

- Modify `scripts/BuildApp.psm1` — the `answerZalo` seam and any Claude-specific staging.
- Modify base-app references via the overlay: fold `runClaude`/`execZaloRunner` usage into the `claude-code` descriptor. (The base-app source in `AgentDC/internal/daemon/` is read-only; the merge happens through the overlay + seam, never by editing the source repo.)

---

### Task 1: Declarative descriptor registry for the three CLI vendors

**Files:**
- Create: `appmode/overlay/internal/daemon/app_llm_cli.go`
- Test: `appmode/overlay/internal/daemon/app_llm_cli_test.go`

**Public behavior to verify:** The registry exposes exactly three CLI vendor descriptors (`codex`, `gemini-cli`, `claude-code`), each carrying the data the adapter needs, and nothing is hardcoded in the adapter body.

- [ ] **Step 1: Write a failing behavior test**

  ```go
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
          if d.readOnlyArgs == nil {
              t.Errorf("%s: readOnlyArgs nil — mọi vendor phải có cờ chỉ-đọc", kind)
          }
          if len(d.modelSeeds) == 0 {
              t.Errorf("%s: modelSeeds rỗng — Discover trả danh sách tĩnh", kind)
          }
      }
  }
  ```

- [ ] **Step 2: Run and confirm RED**

  Run: `$env:ZALOBOT_REPO = [Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User'); pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run TestCLIRegistry`
  Expected: FAIL — `undefined: cliDescriptors`.

- [ ] **Step 3: Implement the registry**

  ```go
  // cliDescriptor là DỮ LIỆU cho một vendor CLI, không phải code. Adapter thân duy nhất đọc nó.
  // Học từ 9Router: provider là descriptor, không phải một nhánh switch.
  type cliDescriptor struct {
      kind        string   // khớp llm_providers.kind
      display     string   // tên hiện ở Portal
      npmPackage  string   // gói cài; rỗng nếu là native exe (claude)
      binJS       string   // đường dẫn tương đối tới entry .js trong node_modules; rỗng nếu native
      nativeBin   string   // tên exe khi là native (claude); rỗng nếu chạy qua node
      subArgs     []string // lệnh con không tương tác: {"exec"} codex, nil cho -p
      promptViaStdin bool  // true: prompt qua stdin; false: positional arg
      modelFlag   string   // "-m"
      readOnlyArgs []string // cờ giới hạn chỉ-đọc — BẮT BUỘC có
      bannedArgs  []string // cờ bỏ sandbox — test chặn adapter dựng chúng
      authMethod  string   // "claude-json" | "codex-exit" | "gemini-file"
      modelSeeds  []cliModel // Discover tĩnh
      claudeBudget bool     // true = dùng ctx gốc, không đặt dưới 25s (chỉ claude-code)
  }

  type cliModel struct{ id, name string }

  // Verified trên --help bản đang cài (2026-08-06). KHÔNG bê nguyên list proxy của 9Router.
  var cliDescriptors = map[string]cliDescriptor{
      "codex": {
          kind: "codex", display: "OpenAI Codex (ChatGPT)",
          npmPackage: "@openai/codex", binJS: `@openai\codex\bin\codex.js`,
          subArgs: []string{"exec"}, promptViaStdin: false, modelFlag: "-m",
          readOnlyArgs: []string{"-s", "read-only", "--skip-git-repo-check", "--ephemeral", "--color", "never"},
          bannedArgs:  []string{"--dangerously-bypass-approvals-and-sandbox", "workspace-write", "danger-full-access"},
          authMethod: "codex-exit",
          modelSeeds: []cliModel{{"gpt-5.5", "GPT-5.5"}, {"gpt-5.4", "GPT-5.4"}, {"gpt-5.4-mini", "GPT-5.4 mini"}},
      },
      "gemini-cli": {
          kind: "gemini-cli", display: "Gemini CLI (Google AI)",
          npmPackage: "@google/gemini-cli", binJS: `@google\gemini-cli\bundle\gemini.js`,
          subArgs: nil, promptViaStdin: false, modelFlag: "-m",
          readOnlyArgs: []string{"--approval-mode", "plan", "--skip-trust", "-o", "json"},
          bannedArgs:  []string{"-y", "--yolo", "auto_edit", "--raw-output"},
          authMethod: "gemini-file",
          modelSeeds: []cliModel{{"gemini-2.5-pro", "Gemini 2.5 Pro"}, {"gemini-2.5-flash", "Gemini 2.5 Flash"}, {"gemini-3-pro-preview", "Gemini 3 Pro Preview"}},
      },
      "claude-code": {
          kind: "claude-code", display: "Claude Code",
          nativeBin: "claude",
          subArgs: nil, promptViaStdin: true, modelFlag: "--model",
          readOnlyArgs: []string{"--allowed-tools", "Read", "Grep", "Glob", "WebFetch"},
          bannedArgs:  []string{"--dangerously-skip-permissions", "bypassPermissions", "--permission-mode"},
          authMethod: "claude-json", claudeBudget: true,
          modelSeeds: []cliModel{{"sonnet", "Claude Sonnet"}, {"opus", "Claude Opus"}, {"haiku", "Claude Haiku"}},
      },
  }
  ```

- [ ] **Step 4: Run and confirm GREEN** — `go-check.ps1 -Run TestCLIRegistry`, PASS.

- [ ] **Step 5: Refactor** — none needed; registry is data.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_llm_cli_test.go
  git commit -m "feat: declarative registry for CLI subscription vendors"
  ```

---

### Task 2: Build the exact argv, and never a sandbox-bypass flag

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_cli.go`
- Test: `appmode/overlay/internal/daemon/app_llm_cli_test.go`

**Public behavior to verify:** For each vendor, `Generate` builds the precise argv the CLI expects, in read-only mode, with the chosen model — and no reachable input can make it emit a sandbox-bypass flag.

- [ ] **Step 1: Write a failing behavior test**

  ```go
  func TestCLIArgvIsReadOnlyAndNeverBypassesSandbox(t *testing.T) {
      cases := []struct{ kind, model string; wantContains, wantAbsent []string }{
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
              argv := buildCLIArgv(cliDescriptors[tc.kind], llmRequest{Model: tc.model, Prompt: "khách hỏi giá"})
              joined := strings.Join(argv, " ")
              for _, w := range tc.wantContains {
                  if !slices.Contains(argv, w) {
                      t.Errorf("argv %v thiếu %q", argv, w)
                  }
              }
              for _, b := range tc.wantAbsent {
                  if strings.Contains(joined, b) {
                      t.Errorf("argv %q chứa cờ bỏ sandbox %q — cấm tuyệt đối", joined, b)
                  }
              }
          })
      }
  }
  ```

- [ ] **Step 2: Run and confirm RED** — `undefined: buildCLIArgv`.

- [ ] **Step 3: Implement `buildCLIArgv`**

  ```go
  // buildCLIArgv dựng argv cho phần SAU tên chương trình. Không bao giờ chèn cờ ngoài descriptor,
  // nên một tin nhắn khách không thể thành một cờ bỏ sandbox.
  func buildCLIArgv(d cliDescriptor, req llmRequest) []string {
      argv := make([]string, 0, 16)
      argv = append(argv, d.subArgs...)          // codex: "exec"; khác: rỗng
      argv = append(argv, d.readOnlyArgs...)      // luôn có, luôn trước
      if d.modelFlag != "" && req.Model != "" {
          argv = append(argv, d.modelFlag, req.Model)
      }
      if !d.promptViaStdin {                       // codex/gemini: prompt positional (codex qua -o file ở Task 3)
          if d.kind == "gemini-cli" {
              argv = append(argv, "-p", req.Prompt)
          } else {
              argv = append(argv, req.Prompt)
          }
      }
      return argv
  }
  ```

  Prompt của claude đi qua stdin (Task 3), nên không nằm trong argv. Codex đọc câu trả lời qua file `-o` (Task 3); prompt của codex là positional cuối.

- [ ] **Step 4: Run and confirm GREEN.**

- [ ] **Step 5: Refactor** — nếu nhánh `gemini-cli`/`codex` trong `!promptViaStdin` rối, tách một trường `promptFlag` vào descriptor (`-p` cho gemini, rỗng cho codex) thay vì `if kind ==`.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_llm_cli_test.go
  git commit -m "feat: read-only argv construction for CLI vendors"
  ```

---

### Task 3: Spawn the process and kill the whole tree on cancel

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_cli.go`
- Test: `appmode/overlay/internal/daemon/app_llm_cli_test.go`

**Public behavior to verify:** A cancelled turn terminates the entire `node → cli → child` process tree, leaving no orphan — the exact failure Task 5 of provider-routing fixed.

- [ ] **Step 1: Write a failing behavior test**

  ```go
  // Spawn một cây thật: node chạy một script đẻ ra một tiến trình con ngủ dài. Huỷ ctx, rồi
  // khẳng định cả cây chết. exec.CommandContext chỉ giết node, để con mồ côi — test này bắt đúng đó.
  func TestCLIRunKillsTheWholeTreeOnCancel(t *testing.T) {
      dir := t.TempDir()
      script := filepath.Join(dir, "tree.js")
      // node cha đẻ con `node -e setInterval`, in pid con ra stderr rồi tự treo.
      os.WriteFile(script, []byte(`
        const {spawn} = require("child_process");
        const c = spawn(process.execPath, ["-e", "setInterval(()=>{},1e9)"], {stdio:"ignore"});
        process.stderr.write("CHILD "+c.pid+"\n");
        setInterval(()=>{}, 1e9);
      `), 0o600)

      ctx, cancel := context.WithCancel(t.Context())
      childPID := make(chan int, 1)
      go func() {
          _, _ = runCLIProcess(ctx, exec.Command(nodeExe(), script), nil, childPID)
      }()
      pid := <-childPID
      cancel()
      // Đợi có giới hạn rồi kiểm tiến trình con còn sống không (OpenProcess/Signal(0) tương đương).
      if stillAlive(pid, 3*time.Second) {
          t.Fatalf("tiến trình con %d còn sống sau huỷ — cây bị mồ côi", pid)
      }
  }
  ```

  (`nodeExe`, `stillAlive` là helper của test/adapter; `nodeExe` trỏ tới `node` đang chạy daemon.)

- [ ] **Step 2: Run and confirm RED** — `undefined: runCLIProcess`.

- [ ] **Step 3: Implement `runCLIProcess`**

  ```go
  // runCLIProcess chạy một CLI đã dựng sẵn, ghi prompt vào stdin nếu có, đọc stdout, và khi ctx bị
  // huỷ thì giết CẢ CÂY bằng killPidTree (taskkill /T) — KHÔNG dựa exec.CommandContext, vì nó chỉ
  // giết con trực tiếp, để lại node→cli→[con] mồ côi (bug Task 5, headless.go:109 nêu đúng hazard).
  func runCLIProcess(ctx context.Context, cmd *exec.Cmd, stdin []byte, childPID chan<- int) ([]byte, error) {
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
      done := make(chan error, 1)
      go func() { done <- cmd.Wait() }()
      select {
      case <-ctx.Done():
          _ = killPidTree(cmd.Process.Pid, "llm-cli", nil) // taskkill /T
          <-done
          return nil, ctx.Err()
      case err := <-done:
          if err != nil {
              return out.Bytes(), &cliExit{err: err, stderr: errBuf.String()}
          }
          return out.Bytes(), nil
      }
  }
  ```

  `killPidTree` sống ở base-app `AgentDC/internal/daemon/headless.go:258`; nó cùng package `daemon`, gọi thẳng được. `cliExit` mang stderr cho Task 4 phân loại.

- [ ] **Step 4: Run and confirm GREEN.**

- [ ] **Step 5: Refactor** — đảm bảo `<-done` sau kill không kẹt (killPidTree đã ép thoát); nếu cần, bọc `Wait` bằng timeout ngắn.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_llm_cli_test.go
  git commit -m "feat: spawn CLI and kill the process tree on cancel"
  ```

---

### Task 4: Classify CLI failures into the existing taxonomy

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_cli.go`
- Test: `appmode/overlay/internal/daemon/app_llm_cli_test.go`

**Public behavior to verify:** A CLI failure maps to the right `llmErrorKind` — quota→`rate_limit` (fallback), not-logged-in / not-installed→`credential` (stop) — and an ambiguous failure defaults to `rate_limit`, never to a chain-stopping kind.

- [ ] **Step 1: Write a failing behavior test**

  ```go
  func TestClassifyCLIError(t *testing.T) {
      cases := []struct{ name, stderr string; notInstalled bool; want llmErrorKind }{
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
  ```

  > **Awaiting (đóng ở Task 8):** chuỗi stderr thật của codex/gemini khi hết-hạn-mức và khi chưa-đăng-nhập chưa xác minh được (chưa login). Bảng trên dùng chuỗi tổng hợp cho khung logic; Task 8 chạy CLI thật đã login rồi ghim chuỗi thật vào bảng này.

- [ ] **Step 2: Run and confirm RED** — `undefined: classifyCLIError`.

- [ ] **Step 3: Implement `classifyCLIError`**

  ```go
  // classifyCLIError ánh xạ output CLI về taxonomy CHUNG. Mặc định rate_limit khi mơ hồ: đoán sai
  // hướng đó chỉ tốn một lượt thử Provider sau; đoán sai thành credential thì chết cả chuỗi.
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
  ```

  KHÔNG thêm `llmErrorKind` mới. `isFallbackEligible` giữ nguyên bốn loại.

- [ ] **Step 4: Run and confirm GREEN.**

- [ ] **Step 5: Refactor** — `containsAny` là helper nhỏ; nếu đã có trong package thì dùng lại, đừng tạo bản thứ hai.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_llm_cli_test.go
  git commit -m "feat: classify CLI failures into the fallback taxonomy"
  ```

---

### Task 5: Detect login status without burning a turn

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_cli.go`
- Test: `appmode/overlay/internal/daemon/app_llm_cli_test.go`

**Public behavior to verify:** `Test` reports each vendor's login state cheaply — claude via JSON, codex via exit code, gemini via credential-file probe — and a gemini probe that cannot read returns "unknown", never "not logged in".

- [ ] **Step 1: Write a failing behavior test**

  ```go
  func TestGeminiAuthProbeReturnsUnknownWhenUnreadable(t *testing.T) {
      // Thiếu file → unknown, KHÔNG phải "chưa đăng nhập". Đoán "chưa đăng nhập" đẩy người dùng
      // vào vòng login lại vô ích khi thực ra chỉ là ta không đọc được.
      missing := filepath.Join(t.TempDir(), "khong-ton-tai", "oauth_creds.json")
      st := probeGeminiAuth(missing)
      if st != authUnknown {
          t.Fatalf("probeGeminiAuth(thiếu file) = %v; want authUnknown", st)
      }
      // File có mặt → coi như đã đăng nhập.
      present := filepath.Join(t.TempDir(), "oauth_creds.json")
      os.WriteFile(present, []byte(`{"access_token":"x"}`), 0o600)
      if probeGeminiAuth(present) != authLoggedIn {
          t.Fatalf("probeGeminiAuth(file có) != authLoggedIn")
      }
  }
  ```

- [ ] **Step 2: Run and confirm RED** — `undefined: probeGeminiAuth`.

- [ ] **Step 3: Implement auth detection**

  ```go
  type authState int
  const (
      authUnknown authState = iota // không xác định được — KHÁC với chưa đăng nhập
      authLoggedIn
      authLoggedOut
  )

  // probeGeminiAuth: Gemini KHÔNG có lệnh status. Dò sự tồn tại file credential. Đây là chi tiết
  // NỘI BỘ của hãng, có thể vỡ khi họ đổi — cô lập sau hàm này, và thiếu/không-đọc-được → unknown.
  func probeGeminiAuth(credPath string) authState {
      info, err := os.Stat(credPath)
      if err != nil {
          if os.IsNotExist(err) {
              return authLoggedOut
          }
          return authUnknown // quyền/khoá file → không kết luận chưa-đăng-nhập
      }
      if info.Size() == 0 {
          return authUnknown
      }
      return authLoggedIn
  }
  ```

  > **Awaiting (đóng ở Task 8):** tên file credential thật của Gemini (`~/.gemini/oauth_creds.json` là suy đoán confidence THẤP). Task 8 đăng nhập gemini rồi xác nhận tên file, chỉnh hằng đường dẫn nếu sai. claude (`auth status --json`) và codex (`login status` exit code) test được live ngay: thêm hai test gọi thật, `t.Skip` nếu CLI vắng.

- [ ] **Step 4: Run and confirm GREEN.**

- [ ] **Step 5: Refactor** — gom ba nhánh auth vào một `checkCLIAuth(descriptor) authState` chọn theo `authMethod`.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_llm_cli_test.go
  git commit -m "feat: cheap per-vendor CLI login detection"
  ```

---

### Task 6: Lift the Claude-Code-last invariant so a chain can be all-subscription

**Files:**
- Modify: `appmode/overlay/internal/store/app_llm.go` (around `validateLLMRoute` ~450, `BootstrapClaudeRoute` ~417)
- Test: `appmode/overlay/internal/store/app_llm_test.go`

**Public behavior to verify:** An empty route is valid, and a route whose enabled final entry is NOT `claude-code` is valid — while a route whose last entry is disabled is still rejected.

- [ ] **Step 1: Write a failing behavior test**

  ```go
  func TestRouteNoLongerRequiresClaudeCodeLast(t *testing.T) {
      st := openTestStore(t)
      // Chuỗi chỉ có codex, không có claude-code — trước đây bị từ chối, giờ hợp lệ.
      mustCreateProvider(t, st, LLMProvider{ID: "codex", Kind: "codex", Enabled: true})
      err := st.ReplaceLLMRoute(1, []LLMRouteEntry{
          {Position: 0, ProviderID: "codex", ModelID: "gpt-5.4", Enabled: true},
      })
      if err != nil {
          t.Fatalf("chuỗi codex-only bị từ chối: %v; giờ phải hợp lệ", err)
      }
      // Chuỗi rỗng: hợp lệ (onboarding máy mới).
      if err := st.ReplaceLLMRoute(2, nil); err != nil {
          t.Fatalf("chuỗi rỗng bị từ chối: %v; rỗng-là-hợp-lệ", err)
      }
      // Mắt xích cuối đang TẮT: vẫn bị từ chối.
      err = st.ReplaceLLMRoute(3, []LLMRouteEntry{
          {Position: 0, ProviderID: "codex", ModelID: "gpt-5.4", Enabled: false},
      })
      if err == nil {
          t.Fatal("chuỗi kết thúc bằng mắt xích TẮT phải bị từ chối")
      }
  }
  ```

- [ ] **Step 2: Run and confirm RED** — the codex-only and empty cases fail on the current `claude-code`-last check.

- [ ] **Step 3: Change the validation**

  Trong `validateLLMRoute`: bỏ nhánh đòi `entries[len-1].ProviderID == "claude-code"`. Giữ: nếu `len(entries) > 0` thì `entries[len-1].Enabled` phải true. Cho phép `len(entries) == 0`. Trong `BootstrapClaudeRoute`: không force-seed `claude-code` trên máy mới — rỗng-là-hợp-lệ; chỉ seed model từ `data/model.txt` NẾU đã có một route claude-code (giữ tương thích ngược, không tạo mới).

  ```go
  // validateLLMRoute — thay khối cũ:
  //   if last.ProviderID != "claude-code" { return ErrRouteMustEndWithClaude }
  // bằng:
  if n := len(entries); n > 0 && !entries[n-1].Enabled {
      return fmt.Errorf("mắt xích cuối phải đang bật")
  }
  // rỗng là hợp lệ: onboarding (§6). Portal chặn ở màn hình đầu, bot báo chưa sẵn sàng.
  ```

- [ ] **Step 4: Run and confirm GREEN**, và sửa/xoá test cũ ghim `claude-code`-last (chúng giờ mô tả hành vi đã bỏ).

- [ ] **Step 5: Refactor** — nếu có hằng `ErrRouteMustEndWithClaude` giờ vô dụng, xoá; grep caller trước.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/store/app_llm.go appmode/overlay/internal/store/app_llm_test.go
  git commit -m "feat: allow empty and non-Claude-tail routes"
  ```

---

### Task 7: Drop the Claude-Code tail privilege in router and Models UI

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go` (~line 114; `newLLMAdapter` ~290)
- Modify: `appmode/overlay/internal/webui/static/pages/models.js`
- Test: `appmode/overlay/internal/daemon/app_llm_router_test.go`, `appmode/tests/models.test.mjs`

**Public behavior to verify:** The router treats the final entry generically (no `entries[len-1] == claude-code` assumption) and registers `local_cli` kinds; the Models page no longer locks Claude Code as an immovable last row.

- [ ] **Step 1: Write failing tests**

  Go — router builds a `local_cli` adapter for a `codex` provider and runs a codex-only chain to completion (with a fake CLI runner injected, no real process):

  ```go
  func TestRouterRunsACodexOnlyChain(t *testing.T) {
      r := newTestRouter(t, withFakeCLIRunner(func(kind string, req llmRequest) (llmResponse, error) {
          return llmResponse{Text: "trả lời từ " + kind}, nil
      }))
      r.saveRoute(t, []LLMRouteEntry{{ProviderID: "codex", ModelID: "gpt-5.4", Enabled: true}})
      got, err := r.Run(t.Context(), "khách hỏi", nil)
      if err != nil || !strings.Contains(got, "codex") {
          t.Fatalf("Run codex-only = %q, %v; muốn trả lời từ codex, không đòi claude-code cuối", got, err)
      }
  }
  ```

  JS (`models.test.mjs`) — replace the "Claude Code row is locked/last" assertions with: a chain ending in `codex` renders its Up/Down/remove controls ENABLED, and saving it issues `PUT /llm/route`.

- [ ] **Step 2: Run and confirm RED** — router asserts claude-code last; `newLLMAdapter` has no `codex`/`gemini-cli`/`claude-code` local_cli case; models.js locks the last row.

- [ ] **Step 3: Implement**

  - `app_llm_router.go`: in `newLLMAdapter`, add cases for `codex`, `gemini-cli`, `claude-code` → construct the `local_cli` adapter (from Tasks 1–5) bound to that descriptor. Remove the `entries[len-1] == "claude-code"` special-case in the run loop; the final entry is just the last enabled entry.
  - `models.js`: delete `isLocked`/`tailIndex` special-casing of `CLAUDE_ID`; every row gets the same controls. (Claude Code, when present, is now an ordinary entry.)

- [ ] **Step 4: Run and confirm GREEN** — `go-check.ps1` and `npm --prefix appmode test`.

- [ ] **Step 5: Refactor** — keep the mutation functions pure (they already are); update any comment that still says "Claude Code is pinned last".

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_router_test.go appmode/overlay/internal/webui/static/pages/models.js appmode/tests/models.test.mjs
  git commit -m "feat: treat every route entry generically, register local_cli kinds"
  ```

---

### Task 8: Capture real codex + gemini output, then pin the parsers (needs login)

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_cli.go` (answer-parse per vendor)
- Test: `appmode/overlay/internal/daemon/app_llm_cli_test.go` (pin against captured fixtures)

**Public behavior to verify:** `Generate` extracts the assistant's final answer from each vendor's real output, and the error-classification strings in Task 4 / auth path in Task 5 match what the CLIs actually emit.

> **This task requires the operator to be logged in.** At execute time, the runner pauses and asks the user to log into codex and gemini in a terminal (this is the one manual login; the follow-on onboarding plan automates it). It is a **capture-first** task: run the real CLI, record the output, THEN write the test against the recorded bytes. Writing a parser test against a guessed format is the exact failure the spec forbids.

- [ ] **Step 1: Capture real output (no test yet)**

  With the user logged in, run and save each to a fixture file under `appmode/overlay/internal/daemon/testdata/`:

  ```powershell
  node "$env:APPDATA\npm\node_modules\@openai\codex\bin\codex.js" exec -s read-only --skip-git-repo-check --ephemeral --color never -m gpt-5.4 -o codex-out.txt "2+2"
  node "$env:APPDATA\npm\node_modules\@google\gemini-cli\bundle\gemini.js" -p "2+2" -m gemini-2.5-pro --approval-mode plan -o json > gemini-out.json
  ```

  Record: the success answer location (codex writes to the `-o` file; gemini `-o json` shape), and if you can trigger it, a quota-exhaustion stderr. Confirm the Gemini credential filename (Task 5 Awaiting): check `~/.gemini/` after login.

- [ ] **Step 2: Write the parse test against the captured fixtures**

  ```go
  func TestParseCLIAnswer(t *testing.T) {
      codexOut := readFixture(t, "codex-out.txt")
      if got := parseCLIAnswer(cliDescriptors["codex"], codexOut); !strings.Contains(got, "4") {
          t.Errorf("parse codex = %q; muốn chứa câu trả lời", got)
      }
      geminiOut := readFixture(t, "gemini-out.json")
      if got := parseCLIAnswer(cliDescriptors["gemini-cli"], geminiOut); !strings.Contains(got, "4") {
          t.Errorf("parse gemini = %q; muốn chứa câu trả lời", got)
      }
  }
  ```

- [ ] **Step 3: Confirm RED, then implement `parseCLIAnswer`** per the captured shapes (codex: read the `-o` output file; gemini: `.response` from `-o json`; claude: the `result` event from `stream-json`). Update Task 4's `classifyCLIError` strings and Task 5's gemini credential path to the captured reality.

- [ ] **Step 4: Confirm GREEN.**

- [ ] **Step 5: Refactor** — keep each vendor's parse in a small function selected by descriptor, not one big switch in `Generate`.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_llm_cli_test.go appmode/overlay/internal/daemon/testdata/
  git commit -m "feat: pin CLI answer parsing to real captured output"
  ```

---

### Task 9: Route attachment turns through the first eligible local_cli

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go` (`threadHasAttachments`/`HasAttachments` handling)
- Test: `appmode/overlay/internal/daemon/app_llm_router_test.go`

**Public behavior to verify:** A turn carrying a file is served by the first enabled `local_cli` in the chain, still bypasses all four HTTP API providers, and no HTTP adapter ever receives the file path or bytes.

- [ ] **Step 1: Write a failing behavior test** (extend the existing attachment canary)

  ```go
  func TestAttachmentTurnUsesLocalCLINotHTTP(t *testing.T) {
      const canaryPath = `C:\Users\Admin\.agentdc\zalo-files\CANARY.pdf`
      const canaryBytes = "CANARY-BYTES-noi-dung-khach"
      httpCalls := recordingHTTPAdapters(t) // 4 fake HTTP adapters that must NOT be called
      r := newTestRouter(t, withFakeCLIRunner(func(kind string, req llmRequest) (llmResponse, error) {
          return llmResponse{Text: "cli đọc được tệp"}, nil
      }), withHTTP(httpCalls))
      r.saveRoute(t, []LLMRouteEntry{
          {ProviderID: "openai", ModelID: "gpt-5", Enabled: true},   // HTTP — phải bị bỏ qua
          {ProviderID: "codex", ModelID: "gpt-5.4", Enabled: true},  // local_cli — phải phục vụ
      })
      _, err := r.RunWithAttachment(t.Context(), "xem ảnh này", canaryPath, []byte(canaryBytes))
      if err != nil {
          t.Fatalf("lượt có tệp lỗi: %v", err)
      }
      if httpCalls.count() != 0 {
          t.Errorf("Provider HTTP được gọi %d lần trong lượt có tệp; muốn 0", httpCalls.count())
      }
      blob := httpCalls.serializedInput()
      for _, c := range []string{jsonForm(t, canaryPath), canaryBytes} {
          if strings.Contains(blob, c) {
              t.Errorf("đầu vào adapter HTTP chứa canary %q", c)
          }
      }
  }
  ```

- [ ] **Step 2: Run and confirm RED** — today attachments hard-route to `claude-code` only, so a codex-served attachment path doesn't exist.

- [ ] **Step 3: Implement** — in the attachment branch, instead of selecting `claude-code`, select the first enabled entry whose provider kind is a `local_cli` (`codex`/`gemini-cli`/`claude-code`); skip HTTP kinds. If none, the turn cannot be served with a file → report unavailable (do not silently drop, per §6).

- [ ] **Step 4: Run and confirm GREEN.** Confirm the existing four-HTTP-provider canary test still passes unchanged.

- [ ] **Step 5: Refactor** — extract `firstEligibleCLIEntry(snapshot)` as a small pure function so it is unit-testable and the router loop stays readable.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_router_test.go
  git commit -m "feat: attachment turns go through any local_cli, HTTP still bypassed"
  ```

---

### Task 10: Discover returns the static per-vendor model list

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_cli.go`
- Test: `appmode/overlay/internal/daemon/app_llm_cli_test.go`

**Public behavior to verify:** `Discover` for a CLI vendor returns its seeded model list as `store.LLMModel` values with `Source = discovered`, without any network call or process spawn.

- [ ] **Step 1: Write a failing behavior test**

  ```go
  func TestCLIDiscoverReturnsSeededModels(t *testing.T) {
      a := newCLIAdapter(cliDescriptors["gemini-cli"], nil)
      got, err := a.Discover(t.Context(), nil)
      if err != nil {
          t.Fatal(err)
      }
      if len(got) == 0 || got[0].Source != store.LLMModelDiscovered {
          t.Fatalf("Discover gemini-cli = %+v; muốn danh sách model discovered", got)
      }
      ids := make([]string, len(got))
      for i, m := range got { ids[i] = m.ModelID }
      if !slices.Contains(ids, "gemini-2.5-pro") {
          t.Errorf("Discover thiếu gemini-2.5-pro; có %v", ids)
      }
  }
  ```

- [ ] **Step 2: Run and confirm RED** — `Discover` not implemented for the CLI adapter.

- [ ] **Step 3: Implement** — `Discover` maps `descriptor.modelSeeds` to `[]store.LLMModel{ProviderID, ModelID, Name, Source: store.LLMModelDiscovered, Available: true}`. No I/O.

- [ ] **Step 4: Run and confirm GREEN.**

- [ ] **Step 5: Refactor** — none; it's a map.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_llm_cli_test.go
  git commit -m "feat: static model discovery for CLI vendors"
  ```

---

### Task 11: Merge Claude Code into the local_cli family (riskiest — last)

**Files:**
- Modify: `scripts/BuildApp.psm1` (the `answerZalo` seam; any Claude-specific staging)
- Modify: `appmode/overlay/internal/store/app_llm.go` (migration of the `claude-code` provider row)
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go` (route the `claude-code` entry through the local_cli adapter, honoring the >25s budget)
- Test: `appmode/overlay/internal/daemon/app_llm_router_test.go`, `scripts` build fixtures, and the full build

**Public behavior to verify:** Claude Code answers through the same `local_cli` adapter as codex/gemini (not the old `runClaude`/`execZaloRunner` special path), keeps its long (>25s) budget, and every existing test — the attachment canary, the `duty.go` seam fixtures, the KB-consult behavior — stays green.

- [ ] **Step 1: Write a failing behavior test**

  ```go
  func TestClaudeCodeRunsThroughTheCLIAdapterWithLongBudget(t *testing.T) {
      var gotTimeout time.Duration
      r := newTestRouter(t, withFakeCLIRunner(func(kind string, req llmRequest) (llmResponse, error) {
          gotTimeout = deadlineFromCtx(t) // ctx phải mang ngân sách gốc, không phải 25s
          return llmResponse{Text: "claude trả lời"}, nil
      }))
      r.saveRoute(t, []LLMRouteEntry{{ProviderID: "claude-code", ModelID: "sonnet", Enabled: true}})
      got, err := r.Run(t.Context(), "tư vấn dài", nil)
      if err != nil || !strings.Contains(got, "claude") {
          t.Fatalf("claude-code qua adapter = %q, %v", got, err)
      }
      if gotTimeout <= 25*time.Second {
          t.Errorf("ngân sách claude-code = %v; phải > 25s (KB-consult đo 42–114s)", gotTimeout)
      }
  }
  ```

- [ ] **Step 2: Run and confirm RED** — claude-code still routes through the hardcoded `runClaude` path with the default 25s provider budget.

- [ ] **Step 3: Implement the merge**

  - Route the `claude-code` entry through the `local_cli` adapter (descriptor `claudeBudget: true`), running under the parent ctx / long budget exactly as `runClaude` does today, not the 25s per-provider child.
  - Migration (`store/app_llm.go`): the seeded `claude-code` provider row keeps `kind = "claude-code"`, which is now a `local_cli` kind — no data migration of routes needed; existing routes stay valid.
  - `BuildApp.psm1`: keep the `answerZalo` seam patching correctly; if `execZaloRunner` wiring changes, update the seam and its fixture. **The source repo must stay byte-identical — verify `git -C $env:ZALOBOT_REPO status --porcelain` empty before and after the build.**

- [ ] **Step 4: Run the full verification** — `go-check.ps1`, `npm --prefix appmode test`, the build fixtures (`pwsh -NoProfile -File .\tests\build-app.Tests.ps1`), and a full `build-app.ps1`. The attachment canary and duty.go seam fixtures MUST stay green. **If the merge destabilizes the seam or the canary, per the spec's §5.2 hedge, prefer keeping Claude Code on its old path and leave it un-merged — codex/gemini as local_cli still deliver the feature.**

- [ ] **Step 5: Refactor** — if `runClaude`/`execZaloRunner` are now dead, remove them; grep callers first. If the merge was abandoned per the hedge, document why in a comment where the two paths diverge.

- [ ] **Step 6: Commit**

  ```bash
  git add scripts/BuildApp.psm1 appmode/overlay/internal/store/app_llm.go appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_router_test.go
  git commit -m "feat: fold Claude Code into the local_cli family"
  ```

---

### Task 12: Full-engine verification and regression gate

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_cli_test.go` (cross-vendor acceptance)
- Modify: `tests/build-app.Tests.ps1` (if a new package assertion is needed)

**Public behavior to verify:** A build with the engine passes every existing and new test, the credential/canary/source-cleanliness gates stay green, and a mixed chain (HTTP + local_cli) falls through correctly.

- [ ] **Step 1: Add a cross-vendor acceptance test**

  ```go
  // Chuỗi: HTTP trả 429 → codex (local_cli, fake runner) trả lời. Telemetry ghi đúng một lần chuyển,
  // provider phục vụ là codex. Không lượt nào chạy CLI ngoài read-only (Task 2 đã chặn argv).
  func TestChainFallsFromHTTPToCLI(t *testing.T) {
      r := newTestRouter(t,
          withHTTPStatus("openai", 429),
          withFakeCLIRunner(func(kind string, _ llmRequest) (llmResponse, error) {
              return llmResponse{Text: "codex phục vụ"}, nil
          }))
      r.saveRoute(t, []LLMRouteEntry{
          {ProviderID: "openai", ModelID: "gpt-5", Enabled: true},
          {ProviderID: "codex", ModelID: "gpt-5.4", Enabled: true},
      })
      got, err := r.Run(t.Context(), "khách hỏi", nil)
      if err != nil || !strings.Contains(got, "codex") {
          t.Fatalf("fallback HTTP→CLI = %q, %v", got, err)
      }
      st := r.status(t)
      if st.Fallbacks != 1 || st.ActiveProviderID != "codex" {
          t.Errorf("status = %+v; muốn 1 fallback, active codex", st)
      }
  }
  ```

- [ ] **Step 2: Run and confirm RED/GREEN** as the acceptance wiring lands.

- [ ] **Step 3: Run the complete matrix**

  ```powershell
  $env:ZALOBOT_REPO = [Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User')
  $env:ZALOBOT_PERSONA = [Environment]::GetEnvironmentVariable('ZALOBOT_PERSONA','User')
  pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO
  npm --prefix appmode test
  pwsh -NoProfile -File .\tests\build-app.Tests.ps1
  pwsh -NoProfile -File .\build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out (Join-Path $env:TEMP ('cli-final-' + [guid]::NewGuid().ToString('N').Substring(0,8)))
  git -C $env:ZALOBOT_REPO status --porcelain
  ```

  Expected: all green; `Assert-NoProviderCredential` still passes (CLIs put no credential in the package); source repo empty before and after; the package builds. Delete the output dir.

- [ ] **Step 4: Confirm GREEN across the matrix.**

- [ ] **Step 5: Refactor** — none; this is the gate.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli_test.go tests/build-app.Tests.ps1
  git commit -m "test: verify the CLI subscription engine end to end"
  ```

---

## Follow-on plan (not in this plan)

The zero-terminal onboarding UX is its own plan, same topic:

- Bundle npm into `app\node` (ships only `node.exe` today) + update `Assert-AppPackage`.
- Install-on-demand: Enable an absent CLI → `node npm-cli.js install -g <pkg> --prefix app\node`, with progress in the Portal.
- Drive login from the Portal: spawn the CLI's login command, show the vendor OAuth popup (device-auth for the cross-machine case), poll auth status, flip to Connected showing email + plan.
- Providers-page subscription group: disconnected default, the state machine, Disconnect (logout).

Then the two deferred topics from the design dialogue, each its own `/discuss`: **multi-account**, then **combos** (Capacity auto-switch, Round Robin, Fusion).
