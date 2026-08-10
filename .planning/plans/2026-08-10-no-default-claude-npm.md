# No-default provider + Claude auto-install qua npm — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Máy khách mới không tự route đi đâu (chưa kết nối = bot im lặng + Portal cảnh báo); Claude tự cài qua npm bundled khi bấm Connect (cơ chế node+binJS như codex), giữ nguyên hành vi agentic (KB `--add-dir`, stream-json, per-account `CLAUDE_CONFIG_DIR`).

**TDD mode:** yes — router-silence, store no-seed, resolveCLIProgram/install, và base opt-in `Program` field đều unit/table-testable. Hai việc chỉ verify được lúc chạy thật (real `auth login`; stdio + orphan qua hop node→native) là **checkpoint capture-first** (T1 spike + T9 E2E), giống CC login / engine Task 8 — KHÔNG bịa output.

**Architecture:** Hai nửa độc lập trên cùng nhánh. **(1) Bỏ default:** thêm sentinel `ErrZaloSilent` (base, additive) + guard trong `answerZalo`; overlay trả "silent runner" (→ `ErrZaloSilent`) khi 0 provider connected / route rỗng / 0 claude account, thay vì `return base`; bỏ seed combo mặc định. **(2) Claude npm (cơ chế B):** descriptor `claude-code` `nativeBin`→`npmPackage`+`binJS=cli-wrapper.cjs`; `install()` cho phép claude; base `execZaloRunner` nhận opt-in `zaloConfig.Program`/`ProgramPrefixArgs` (rỗng = `LookPath` cũ → standalone KHÔNG đổi). Fallback A-native-direct-exe pre-approved nếu hop node→native lỗi.

**Tech stack:** Go (overlay `agentdc` + base AgentDC), Node 22 + npm 10 bundled, `@anthropic-ai/claude-code` (unpinned, như `@openai/codex`), Portal vanilla JS (`node --test`).

**Spec:** `.planning/specs/2026-08-10-no-default-claude-npm-design.md`

**Research:** `.planning/research/no-default-claude-npm-RESEARCH.md`

---

## Ràng buộc bất di bất dịch (từ research `## Decisions for plan`)

1. `npmPackage="@anthropic-ai/claude-code"` (không pin, như codex). `binJS=@anthropic-ai\claude-code\cli-wrapper.cjs` (KHÔNG `cli.js` — không tồn tại; KHÔNG `bin\claude.exe` — là PE, `node` không chạy được).
2. Install là **online-tại-Connect** (npm i -g tải wrapper + optional dep win32-x64 ~90MB từ registry; postinstall hardlink binary native). KHÔNG pre-bundle binary 287MB. KHÔNG `--omit=optional`.
3. Connect call sites (`loginClaude`/`pollAuthClaude`/`accountLabel`/`detect`) đã nối `prefixArgs` — KHÔNG sửa.
4. **Break site duy nhất spec sót:** `probeClaudeAuth` (`app_llm_cli.go:213`) đọc `d.nativeBin` trực tiếp → repoint `resolveCLIProgram`. **+ site thứ 3 (plan phát hiện):** `app_knowledge.go:456` `LookPath("claude")` (KB-ingest write-mode) → repoint `resolveCLIProgram`.
5. Cổng im = **base sentinel `ErrZaloSilent`** (KHÔNG overlay-skip — build seam chỉ thay đối số runner, `answerZalo` luôn chạy + escalate mọi lỗi runner). Additive/backward-compatible.
6. Base `execZaloRunner.Run`: opt-in `zaloConfig.Program`+`ProgramPrefixArgs`, set thì dùng, rỗng → `LookPath(prof.Binary)` (standalone byte-for-byte không đổi).
7. Router im: `appZaloRunner` route-error + empty-entries + **0-provider-connected**, và `appClaudeRunner` 0-account → silent runner, KHÔNG `base`.
8. Fallback nếu checkpoint T1 cắn: **A-native-direct-exe** (descriptor field `npmBin=@anthropic-ai\claude-code\bin\claude.exe` + nhánh resolveCLIProgram trả `(<root>\<npmBin>, nil)`; chạy PE thật, không hop, không .cmd). ~5 dòng, mọi call site đã chịu `prefixArgs=nil`.

## Fast loops

- Go (overlay against base): `$env:ZALOBOT_REPO=[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User'); pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO [-Run <regex>]`
- Portal: `npm --prefix appmode test`
- Full gate: `build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out <temp>` (7/7, canary "sach").
- Env vars KHÔNG truyền xuống shell con — load ở đầu mỗi shell.

## File structure

**Overlay (`Zalo-Bot/appmode/overlay/internal/daemon`):**
- `app_llm_cli.go` — descriptor `claude-code` npm-hoá; `probeClaudeAuth` repoint; (dead `nativeBin` resolve branch để lại vô hại).
- `app_llm_connect.go` — `install()` cho phép claude-code.
- `app_llm_router.go` — `silentZaloRunner`; `hasAnyConnectedProvider`; `appZaloRunner`/`appClaudeRunner` im thay vì base; `appClaudeRunner` set `Program`.
- `app_knowledge.go` — KB-ingest repoint sang `resolveCLIProgram` + account config dir.
- `app_llm_api.go` (+ `app_routes.go`) — trường status "chưa có provider".
- store (`internal/store`) — bỏ seed combo mặc định.
- `webui/static/pages/providers.js` + `portal.css` — banner cảnh báo.

**Base (`AgentDC/internal/daemon`):**
- `duty.go` — `ErrZaloSilent` + guard trong `answerZalo`; `zaloConfig.Program`/`ProgramPrefixArgs` + `execZaloRunner.Run` dùng chúng.

**Tests:** `app_llm_*_test.go` (overlay), `duty_test.go` (base), `appmode/tests/providers.test.mjs`.

---

### Task 1: SPIKE (capture-first, gate) — cài thật, chốt binJS + B-wrapper vs A-native

**Files:**
- Không sửa code sản phẩm. Ghi kết quả vào cuối `.planning/research/no-default-claude-npm-RESEARCH.md` mục `## Spike results (T1)`.

**Reason TDD skipped:** spike — đo hành vi runtime thật để chốt cơ chế trước khi đụng base repo. Không có gì để test-first.

**Public behavior to verify:** sau task, biết chắc `binJS` đúng đường, và B-wrapper (`node cli-wrapper.cjs`) có (a) cho stdio chảy qua hop node→native, (b) KHÔNG mồ côi `claude` khi giết `node` lúc timeout — hoặc chốt chuyển sang A-native-direct-exe.

- [ ] **Step 1: Cài vào prefix vứt đi (KHÔNG đụng máy)**

  ```powershell
  $spike = "$env:TEMP\claude-spike"; Remove-Item -Recurse -Force $spike -ErrorAction SilentlyContinue
  New-Item -ItemType Directory -Force $spike | Out-Null
  $env:npm_config_prefix = $spike; $env:npm_config_cache = "$spike\cache"
  npm install -g @anthropic-ai/claude-code 2>&1 | Select-Object -Last 20
  # Xác nhận layout:
  $root = npm root -g            # phải = $spike\node_modules (hoặc $spike\node_modules trên Windows)
  Test-Path "$root\@anthropic-ai\claude-code\cli-wrapper.cjs"   # => True (đây là binJS)
  Test-Path "$root\@anthropic-ai\claude-code\bin\claude.exe"    # => True (PE thật sau postinstall; đường fallback A)
  ```
  Expected: cả hai `True`. Ghi lại `npm root -g` thực tế + kích thước `claude.exe` (nếu ~280MB = binary thật đã hardlink; nếu ~500B = còn stub → postinstall lỗi, điều tra).

- [ ] **Step 2: stdio qua hop (KHÔNG cần login)**

  ```powershell
  $node = (Get-Command node).Source
  $wrap = "$root\@anthropic-ai\claude-code\cli-wrapper.cjs"
  & $node $wrap --version          # phải in version qua hop → stdout chảy được
  & $node $wrap auth status --json # in JSON (loggedOut) → stdout inherit OK qua spawnSync
  ```
  Expected: cả hai in ra stdout (không nuốt). Nếu stdout trống/nuốt → hop node→native chặn stdio trên Windows → nghiêng A-native.

- [ ] **Step 3: orphan-on-timeout (mô phỏng CommandContext giết node)**

  Chạy một lệnh treo lâu qua hop rồi giết ĐÚNG tiến trình `node` (như `exec.CommandContext` làm), xem `claude.exe` con còn sống không:
  ```powershell
  $p = Start-Process $node -ArgumentList "`"$wrap`"","-p","xin chao" -PassThru -NoNewWindow
  Start-Sleep -Seconds 2
  Stop-Process -Id $p.Id -Force               # giết chỉ node (không /T)
  Start-Sleep -Seconds 1
  Get-Process claude -ErrorAction SilentlyContinue | Select-Object Id,ProcessName  # còn claude = MỒ CÔI
  ```
  Expected: nếu KHÔNG còn `claude` → B-wrapper an toàn (native chết theo pipe vỡ). Nếu CÒN `claude` mồ côi → HAZARD xác nhận → chốt A-native-direct-exe (chạy PE thẳng, `CommandContext` giết đúng nó).

- [ ] **Step 4: Chốt quyết định + ghi**

  Ghi vào `## Spike results (T1)`: `npm root -g` thật, đường `binJS`, kết quả step 2/3, và **DECISION: B-wrapper** (mặc định) **hoặc A-native-direct-exe** (nếu step2 nuốt stdio hoặc step3 mồ côi). Mọi task sau đọc quyết định này. Dọn `$spike`.

- [ ] **Step 5: Commit**

  ```bash
  git add .planning/research/no-default-claude-npm-RESEARCH.md
  git commit -m "chore: T1 spike — claude npm layout + B-wrapper vs A-native decision"
  ```

---

### Task 2: BASE — sentinel `ErrZaloSilent` + guard im trong `answerZalo`

**Files:**
- Modify: `C:\Users\Admin\Desktop\AgentDC\internal\daemon\duty.go` (thêm sentinel; guard quanh `:584-587`)
- Test: `C:\Users\Admin\Desktop\AgentDC\internal\daemon\duty_test.go`

**Public behavior to verify:** một `zaloRunner` trả `("", ErrZaloSilent)` làm `answerZalo` KHÔNG gửi gì cho khách, KHÔNG flag thread, trả `nil` (im sạch) — khác hẳn escalate/handoff của lỗi runner thường.

- [ ] **Step 1: Viết test thất bại (base, same-package)**

  ```go
  func TestAnswerZaloSilentSentinelStaysQuiet(t *testing.T) {
      a, deps := newTestAPIWithThread(t, ipc.ZaloAuto) // helper sẵn có dựng api + thread auto
      silent := zaloRunnerFunc(func(context.Context, string, func(string)) (string, error) {
          return "", ErrZaloSilent
      })
      err := a.answerZalo(context.Background(), deps.cfg, silent, deps.threadID, "hỏi gì đó",
          nil, ipc.ZaloOutboxDraft{})
      if err != nil {
          t.Fatalf("answerZalo = %v; want nil (im sạch)", err)
      }
      // KHÔNG có tin ra, KHÔNG flag:
      if out, _ := a.lastZaloOutbound(deps.threadID); out != "" {
          t.Errorf("có tin gửi khách %q; want im", out)
      }
      if flagged, _ := a.st.ZaloThreadFlagged(deps.threadID); flagged {
          t.Errorf("thread bị flag; want không (im, không escalate)")
      }
  }
  ```
  (Nếu chưa có `zaloRunnerFunc`/`newTestAPIWithThread`/`ZaloThreadFlagged`, dùng helper tương đương đã có trong `duty_test.go`; audit tên thật lúc thực thi.)

- [ ] **Step 2: Chạy — FAIL**

  Run: `go test ./internal/daemon/ -run TestAnswerZaloSilentSentinel` (trong AgentDC)
  Expected: FAIL — `ErrZaloSilent` chưa định nghĩa (compile error) hoặc runner lỗi → escalate → thread flagged.

- [ ] **Step 3: Cài đặt**

  Thêm sentinel (gần đầu file cạnh các var lỗi khác):
  ```go
  // ErrZaloSilent: runner cố ý KHÔNG trả lời (chưa cấu hình provider). answerZalo nuốt im,
  // KHÔNG escalate/handoff. Chỉ overlay trả nó; base execZaloRunner không bao giờ → standalone không đổi.
  var ErrZaloSilent = errors.New("zalo: im lặng — chưa cấu hình provider")
  ```
  Trong `answerZalo`, NGAY sau `raw, err := run.Run(ctx, prompt, step)` và TRƯỚC nhánh `if err != nil { return escalate(...) }` (~`:584`):
  ```go
  if errors.Is(err, ErrZaloSilent) {
      a.logger.Info("zalo duty: chưa cấu hình provider, bot im", "thread", threadID)
      return nil
  }
  ```
  Đảm bảo `errors` đã import.

- [ ] **Step 4: Chạy — PASS**

  Run: `go test ./internal/daemon/ -run TestAnswerZaloSilentSentinel`
  Expected: PASS. Rồi `go test ./internal/daemon/` (toàn base daemon) vẫn xanh (standalone không đổi).

- [ ] **Step 5: Refactor** — không cần.

- [ ] **Step 6: Commit**

  ```bash
  git -C C:/Users/Admin/Desktop/AgentDC add internal/daemon/duty.go internal/daemon/duty_test.go
  git -C C:/Users/Admin/Desktop/AgentDC commit -m "feat: ErrZaloSilent — answerZalo stays quiet on unconfigured (opt-in)"
  ```
  (Base repo commit riêng — như CC4 `b666721`.)

---

### Task 3: BASE — opt-in `zaloConfig.Program` + `ProgramPrefixArgs` trong `execZaloRunner.Run`

**Files:**
- Modify: `AgentDC\internal\daemon\duty.go` (`type zaloConfig` ~`:37-116`; `execZaloRunner.Run` ~`:1735-1747`)
- Test: `AgentDC\internal\daemon\duty_test.go`

**Public behavior to verify:** khi `cfg.Program != ""`, `execZaloRunner.Run` chạy `Program` + `ProgramPrefixArgs` + args (không `LookPath`); khi rỗng → `LookPath(prof.Binary)` như cũ (standalone không đổi).

- [ ] **Step 1: Viết test thất bại**

  ```go
  func TestExecZaloRunnerUsesProgramWhenSet(t *testing.T) {
      // seam: execZaloRunner spawn qua exec.CommandContext; test chặn bằng cách trỏ Program tới
      // một script in argv rồi đọc lại. Dùng helper spawn sẵn có; nếu không, dùng "cmd /c echo".
      cfg := zaloConfig{
          Program:           `C:\Windows\System32\cmd.exe`,
          ProgramPrefixArgs: []string{"/c", "echo"},
          // Model/ConfigDir để mặc định
      }
      r := execZaloRunner{cfg: cfg, logger: testLogger(t)}
      out, err := r.Run(context.Background(), "PING", func(string) {})
      if err != nil { t.Fatalf("Run err: %v", err) }
      if !strings.Contains(out, "PING") == false { /* placeholder */ }
      _ = out
  }
  ```
  GHI CHÚ THỰC THI: `execZaloRunner.Run` viết prompt qua stdin + parse stream-json, nên test "echo" ở trên KHÔNG phản ánh đúng đường parse. Test THẬT nên là **table-test ở tầng dựng lệnh**: tách phần chọn `program/prefix` ra một hàm nhỏ thuần `resolveRunProgram(cfg, prof) (string, []string)` và test nó:
  ```go
  func TestResolveRunProgram(t *testing.T) {
      prof := agent.ConsultReadOnly()
      // Program set → dùng nó, bỏ LookPath
      p, pre := resolveRunProgram(zaloConfig{Program: `X:\node.exe`, ProgramPrefixArgs: []string{`w.cjs`}}, prof)
      if p != `X:\node.exe` || len(pre) != 1 || pre[0] != `w.cjs` {
          t.Fatalf("Program set: got %q %v", p, pre)
      }
      // Program rỗng → LookPath(prof.Binary); chỉ khẳng định KHÔNG panic + prefix rỗng
      _, pre2 := resolveRunProgram(zaloConfig{}, prof)
      if len(pre2) != 0 { t.Fatalf("empty Program: prefix phải rỗng, got %v", pre2) }
  }
  ```

- [ ] **Step 2: Chạy — FAIL**

  Run: `go test ./internal/daemon/ -run TestResolveRunProgram`
  Expected: FAIL — `resolveRunProgram` chưa tồn tại; field `Program` chưa có.

- [ ] **Step 3: Cài đặt**

  `type zaloConfig` — thêm (cạnh `ConfigDir`):
  ```go
  // Program/ProgramPrefixArgs: opt-in. Rỗng = LookPath(prof.Binary) như cũ (standalone không đổi).
  // Overlay set chúng để chạy claude npm qua `node <cli-wrapper.cjs>` (per-account). Xem appClaudeRunner.
  Program           string
  ProgramPrefixArgs []string
  ```
  Tách hàm thuần trong `duty.go`:
  ```go
  func resolveRunProgram(cfg zaloConfig, prof agent.Profile) (string, []string, error) {
      if cfg.Program != "" {
          return cfg.Program, cfg.ProgramPrefixArgs, nil
      }
      bin, err := exec.LookPath(prof.Binary)
      return bin, nil, err
  }
  ```
  `execZaloRunner.Run` (~`:1737`) đổi:
  ```go
  program, prefix, err := resolveRunProgram(e.cfg, prof)
  if err != nil { /* giữ nguyên xử lý lỗi LookPath cũ */ }
  ...
  cmd := exec.CommandContext(ctx, program, append(append([]string{}, prefix...), args...)...)
  ```
  (Giữ nguyên `cmd.Env = claudeEnv(...)`, stdin prompt, parse stream-json.)

- [ ] **Step 4: Chạy — PASS**

  Run: `go test ./internal/daemon/ -run TestResolveRunProgram` → PASS; rồi `go test ./internal/daemon/` toàn bộ xanh.

- [ ] **Step 5: Refactor** — không.

- [ ] **Step 6: Commit**

  ```bash
  git -C C:/Users/Admin/Desktop/AgentDC add internal/daemon/duty.go internal/daemon/duty_test.go
  git -C C:/Users/Admin/Desktop/AgentDC commit -m "feat: zaloConfig.Program opt-in for node+binJS claude (empty = LookPath, standalone unchanged)"
  ```

---

### Task 4: OVERLAY — descriptor `claude-code` → npm; sửa `probeClaudeAuth` + `app_knowledge` resolve

**Files:**
- Modify: `appmode\overlay\internal\daemon\app_llm_cli.go` (descriptor `:83-94`; `probeClaudeAuth` `:212-236`)
- Modify: `appmode\overlay\internal\daemon\app_knowledge.go` (`:455-473`)
- Test: `appmode\overlay\internal\daemon\app_llm_cli_test.go`

**Public behavior to verify:** `resolveCLIProgram(cliDescriptors["claude-code"])` trả `(node, [<root>\@anthropic-ai\claude-code\cli-wrapper.cjs])` (theo quyết định T1 = B-wrapper; nếu A-native → `(<root>\...\bin\claude.exe, nil)`); `probeClaudeAuth` và KB-ingest KHÔNG còn đọc `LookPath("claude")`/`d.nativeBin` trực tiếp.

- [ ] **Step 1: Viết test thất bại**

  ```go
  func TestClaudeDescriptorIsNPM(t *testing.T) {
      d := cliDescriptors["claude-code"]
      if d.npmPackage != "@anthropic-ai/claude-code" {
          t.Errorf("npmPackage = %q; want @anthropic-ai/claude-code", d.npmPackage)
      }
      if d.nativeBin != "" {
          t.Errorf("nativeBin = %q; want rỗng (đã npm-hoá)", d.nativeBin)
      }
      if d.binJS == "" {
          t.Errorf("binJS rỗng; want đường cli-wrapper.cjs")
      }
  }
  ```
  (Nếu T1 chốt A-native: test kiểm `d.npmBin` thay vì `binJS` — điều chỉnh theo quyết định T1.)

- [ ] **Step 2: Chạy — FAIL**

  Run: `pwsh -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run TestClaudeDescriptorIsNPM`
  Expected: FAIL — `nativeBin` vẫn `"claude"`, `npmPackage`/`binJS` rỗng.

- [ ] **Step 3: Cài đặt**

  Descriptor (`:83-94`) — thay dòng `nativeBin: "claude",` bằng:
  ```go
  npmPackage: "@anthropic-ai/claude-code", binJS: `@anthropic-ai\claude-code\cli-wrapper.cjs`,
  ```
  (giữ nguyên `readOnlyArgs`, `bannedArgs`, `authMethod:"claude-json"`, `modelSeeds`, `claudeBudget:true`.)

  `probeClaudeAuth` (`:212-236`) — thay `bin, err := exec.LookPath(d.nativeBin)` bằng resolve chuẩn (mirror `pollAuthClaude`):
  ```go
  program, prefixArgs, err := resolveCLIProgram(d)
  if err != nil {
      return authUnknown // chưa cài → unknown, đúng như trước khi có claude
  }
  argv := append(append([]string{}, prefixArgs...), "auth", "status", "--json")
  cmd := exec.CommandContext(ctx, program, argv...)
  ```
  (Điều chỉnh phần đọc/parse output giữ nguyên.)

  `app_knowledge.go` (`:456`) — thay `bin, err := exec.LookPath("claude")` bằng:
  ```go
  program, prefixArgs, err := resolveCLIProgram(cliDescriptors["claude-code"])
  if err != nil {
      ingest.step("chưa cài Claude Code")
      ingest.finish("chưa cài Claude Code, hoặc chưa đăng nhập. Bấm Connect Claude trong Portal trước. Xem DOC TRUOC.txt")
      return
  }
  ```
  Và `:473` dựng lệnh với prefix: `cmd := exec.CommandContext(ctx, program, append(append([]string{}, prefixArgs...), args...)...)`.
  KB-ingest dùng account claude-code nếu có (giữ cô lập): trước khi dựng cmd, nếu `accounts, _ := a.st.LLMAccounts(claudeCodeProviderID); len>0`, set `cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+accounts[0].ConfigDir)` (ingest là admin one-shot, account đầu đủ dùng — ponytail: account[0], round-robin không cần cho ingest thủ công).

- [ ] **Step 4: Chạy — PASS**

  Run: `pwsh -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run "TestClaudeDescriptor|TestResolveCLIProgram|ProbeClaudeAuth"` rồi go-check full.
  Expected: PASS. (Kiểm luôn không còn reader `d.nativeBin` nào ngoài nhánh resolve chết: grep `nativeBin`.)

- [ ] **Step 5: Refactor** — có thể xoá nhánh `if d.nativeBin != ""` trong `resolveCLIProgram` (giờ chết). Tùy chọn, vô hại nếu để.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_knowledge.go appmode/overlay/internal/daemon/app_llm_cli_test.go
  git commit -m "feat: claude-code descriptor → npm (cli-wrapper.cjs); repoint probeClaudeAuth + KB-ingest to resolveCLIProgram"
  ```

---

### Task 5: OVERLAY — `install()` cho phép claude-code (npm i -g)

**Files:**
- Modify: `appmode\overlay\internal\daemon\app_llm_connect.go` (`install()` `:326-354`)
- Test: `appmode\overlay\internal\daemon\app_llm_connect_test.go`

**Public behavior to verify:** `install(ctx,"claude-code",…)` KHÔNG còn trả lỗi từ chối; nó dựng `npm install -g @anthropic-ai/claude-code`.

- [ ] **Step 1: Viết test thất bại**

  Có seam? `install` gọi `exec.CommandContext("npm",…)` trực tiếp. Test qua một seam lệnh nếu có (`f.commands`), hoặc kiểm nhánh sớm: khẳng định claude-code KHÔNG rơi vào nhánh reject. Tối thiểu, test rằng `cliDescriptors["claude-code"].npmPackage != ""` (đã có ở T4) + một test dựng-argv nếu `install` tách được. Nếu không có seam, thêm test mỏng:
  ```go
  func TestInstallAcceptsClaude(t *testing.T) {
      // install dùng npmPackage; claude-code giờ non-rỗng nên KHÔNG vào nhánh reject.
      if cliDescriptors["claude-code"].npmPackage == "" {
          t.Fatal("claude-code.npmPackage rỗng → install sẽ vẫn từ chối")
      }
      // Nhánh reject cũ chỉ khớp kind=="claude-code" + trả lỗi 'chưa cài'; xác nhận đã gỡ bằng
      // cách gọi install với ctx đã-cancel và khẳng định lỗi KHÔNG phải câu 'cài tại claude.com'.
      ctx, cancel := context.WithCancel(context.Background()); cancel()
      err := (&defaultConnectRunner{}).install(ctx, "claude-code", func(string){})
      if err != nil && strings.Contains(err.Error(), "claude.com/claude-code") {
          t.Fatalf("vẫn nhánh reject cũ: %v", err)
      }
  }
  ```

- [ ] **Step 2: Chạy — FAIL**

  Run: `pwsh -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run TestInstallAcceptsClaude`
  Expected: FAIL — nhánh `if kind == "claude-code" { return fmt.Errorf("…claude.com…") }` còn đó.

- [ ] **Step 3: Cài đặt**

  Xoá 3 dòng reject (`:329-331`). Còn lại `pkg := cliDescriptors[kind].npmPackage; if pkg == "" {…}` sẽ tự đúng (claude-code npmPackage non-rỗng → `npm install -g @anthropic-ai/claude-code`).

- [ ] **Step 4: Chạy — PASS**

  Run: `pwsh -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run TestInstallAcceptsClaude` → PASS; go-check full xanh.

- [ ] **Step 5: Refactor** — không.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_connect.go appmode/overlay/internal/daemon/app_llm_connect_test.go
  git commit -m "feat: install() allows claude-code (npm i -g @anthropic-ai/claude-code)"
  ```

---

### Task 6: OVERLAY — silent runner + gate 0-provider + router im + wire Program

**Files:**
- Modify: `appmode\overlay\internal\daemon\app_llm_router.go` (`appZaloRunner` `:513-550`; `appClaudeRunner` `:592-607`; thêm `silentZaloRunner`, `hasAnyConnectedProvider`)
- Test: `appmode\overlay\internal\daemon\app_llm_router_test.go`

**Public behavior to verify:** (a) 0 provider connected → `appZaloRunner` trả runner im (`Run`→`ErrZaloSilent`); (b) route rỗng / đọc route hỏng → im, KHÔNG base; (c) `appClaudeRunner` 0 account → im, KHÔNG base; (d) có account claude → `execZaloRunner` với `zc.Program`/`ProgramPrefixArgs` set từ `resolveCLIProgram`.

- [ ] **Step 1: Viết test thất bại**

  ```go
  func TestAppZaloRunnerSilentWhenNoProvider(t *testing.T) {
      a := newTestAPI(t) // store rỗng: 0 provider, 0 account, 0 combo
      base := zaloRunnerFunc(func(context.Context, string, func(string)) (string, error) {
          t.Fatal("KHÔNG được gọi base khi chưa cấu hình"); return "", nil
      })
      r := a.appZaloRunner(zaloConfig{}, base, "thread-1", false)
      _, err := r.Run(context.Background(), "hỏi", func(string){})
      if !errors.Is(err, ErrZaloSilent) {
          t.Fatalf("Run err = %v; want ErrZaloSilent", err)
      }
  }

  func TestAppClaudeRunnerSilentWhenNoAccount(t *testing.T) {
      a := newTestAPI(t) // 0 claude account
      base := zaloRunnerFunc(func(context.Context, string, func(string)) (string, error) {
          t.Fatal("KHÔNG gọi base"); return "", nil
      })
      r := a.appClaudeRunner(zaloConfig{}, base, "opus")
      _, err := r.Run(context.Background(), "hỏi", func(string){})
      if !errors.Is(err, ErrZaloSilent) {
          t.Fatalf("Run err = %v; want ErrZaloSilent", err)
      }
  }
  ```
  (`ErrZaloSilent` là base export — import qua package base; overlay `daemon` đã dùng type base khác nên import path có sẵn.)

- [ ] **Step 2: Chạy — FAIL**

  Run: `pwsh -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run "AppZaloRunnerSilent|AppClaudeRunnerSilent"`
  Expected: FAIL — hiện trả `base` (test `t.Fatal` khi base bị gọi, hoặc Run trả nil).

- [ ] **Step 3: Cài đặt**

  Thêm silent runner:
  ```go
  // silentZaloRunner: chưa cấu hình provider → im. answerZalo nhận ErrZaloSilent và nuốt (không escalate).
  type silentZaloRunner struct{}
  func (silentZaloRunner) Run(context.Context, string, func(string)) (string, error) {
      return "", ErrZaloSilent
  }
  ```
  Helper connected (dùng chung với Portal T8):
  ```go
  // hasAnyConnectedProvider: buyer đã kết nối ÍT NHẤT một provider chưa. Thuê bao = có account enabled;
  // API provider = có credential. Rẻ, đọc store. 0 → bot im (không default).
  func (a *api) hasAnyConnectedProvider() bool {
      for _, kind := range subscriptionKinds { // {codex, claude-code}
          if accs, err := a.st.LLMAccounts(providerIDForKind(kind)); err == nil && len(accs) > 0 {
              for _, ac := range accs { if ac.Enabled { return true } }
          }
      }
      if provs, err := a.st.LLMProviders(); err == nil {
          for _, p := range provs { if p.CredentialConfigured { return true } } // API providers
      }
      return false
  }
  ```
  (Audit tên thật: `subscriptionKinds`, `providerIDForKind`/`claudeCodeProviderID`, `LLMProviders`/field credential — điều chỉnh khi thực thi.)

  `appZaloRunner` — đầu hàm, trước đọc route:
  ```go
  if !a.hasAnyConnectedProvider() {
      return silentZaloRunner{}
  }
  ```
  Đổi hai `return base` (`:525` đọc lỗi, `:528` rỗng) → `return silentZaloRunner{}`. Log giữ (đổi câu "đi thẳng Claude Code" → "chưa cấu hình, bot im").

  `appClaudeRunner` — nhánh có account: sau `zc.ConfigDir = acc.ConfigDir`, set program:
  ```go
  if program, prefixArgs, err := resolveCLIProgram(cliDescriptors["claude-code"]); err == nil {
      zc.Program, zc.ProgramPrefixArgs = program, prefixArgs
  }
  ```
  Nhánh 0-account (`:602-604` `return base`) → `return silentZaloRunner{}`. (Bỏ luôn nhánh `model != "" ...` dựng execZaloRunner mặc-định-login: 0 account = không có login nào để chạy → im.)

- [ ] **Step 4: Chạy — PASS**

  Run: `pwsh -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run "AppZaloRunner|AppClaudeRunner"` → PASS. Rồi go-check full — chú ý các test cũ khẳng định `appClaudeRunner` trả `base` (router_test `:1535-1544`): cập nhật kỳ vọng sang `silentZaloRunner` (hành vi mới, có chủ đích).

- [ ] **Step 5: Refactor** — `base` param của `appClaudeRunner` giờ có thể vô dụng; giữ chữ ký ổn định hoặc gỡ nếu không caller nào cần. Ưu tiên giữ để diff nhỏ.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_router_test.go
  git commit -m "feat: no default — silent runner when no provider connected; claude runner sets Program"
  ```

---

### Task 7: OVERLAY store — bỏ seed combo mặc định

**Files:**
- Modify: store migration/seed (grep `seed`/`default combo` trong `internal/store` — nơi CB1 gieo combo mặc định)
- Test: store test (`internal/store/*_test.go`)

**Public behavior to verify:** store mới toanh → 0 combo, không combo active → `LLMRoute()` trả entries rỗng → (với 0 provider) bot im.

- [ ] **Step 1: Viết test thất bại**

  ```go
  func TestFreshStoreHasNoDefaultCombo(t *testing.T) {
      st := openMemoryStore(t) // :memory:, migrate mới nhất
      combos, err := st.LLMCombos()
      if err != nil { t.Fatal(err) }
      if len(combos) != 0 {
          t.Fatalf("combo lúc mới = %d; want 0 (không default)", len(combos))
      }
      route, err := st.LLMRoute()
      if err != nil { t.Fatal(err) }
      if len(route.Entries) != 0 {
          t.Fatalf("route entries = %d; want 0", len(route.Entries))
      }
  }
  ```

- [ ] **Step 2: Chạy — FAIL**

  Run: `pwsh -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run TestFreshStoreHasNoDefaultCombo`
  Expected: FAIL — CB1 seed một combo mặc định.

- [ ] **Step 3: Cài đặt**

  Gỡ câu INSERT seed combo mặc định trong migration CB1 (giữ schema `llm_combos`/members). Vì nhánh chưa ship rộng và data dir mới ở phiên bản schema hiện tại, greenfield — không cần migration hạ cấp. Nếu seed nằm trong `EnsureDefaultCombo()` gọi lúc bootstrap → gỡ lời gọi đó.
  Chú ý delete-guard combos (`canDelete = !active && len>1`) và Combos UI "tạo combo đầu tiên" khi rỗng — kiểm frontend chịu được 0 combo (T8 kề).

- [ ] **Step 4: Chạy — PASS**

  Run: full go-check. Expected: PASS; các test combo cũ giả định "có 1 combo mặc định" phải cập nhật sang 0.

- [ ] **Step 5: Refactor** — không.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/store/
  git commit -m "feat: no seeded default combo — Combos ships empty (no default routing)"
  ```

---

### Task 8: OVERLAY — Portal banner "chưa có provider"

**Files:**
- Modify: `app_llm_api.go` (+ `app_routes.go` nếu cần) — trường `providersConnected`/`hasProvider` trong body providers/dashboard
- Modify: `webui\static\pages\providers.js` — banner khi 0 provider
- Modify: `webui\static\portal.css` — style banner
- Test: `appmode\tests\providers.test.mjs`

**Public behavior to verify:** khi 0 provider connected, Providers page hiện banner cảnh báo "Chưa kết nối provider — bot sẽ im lặng"; khi có ≥1 → ẩn.

- [ ] **Step 1: Viết test thất bại (Portal, node --test)**

  ```js
  test("banner cảnh báo hiện khi 0 provider connected", () => {
      const dom = mountProvidersPage({ providers: [], providersConnected: 0 });
      const banner = dom.querySelector("[data-no-provider-warning]");
      assert.ok(banner, "phải có banner khi 0 provider");
      assert.match(banner.textContent, /im lặng|chưa kết nối/i);
  });
  test("banner ẩn khi có provider connected", () => {
      const dom = mountProvidersPage({ providers: [/*…*/], providersConnected: 1 });
      assert.equal(dom.querySelector("[data-no-provider-warning]"), null);
  });
  ```
  (Dùng harness `mountProvidersPage` sẵn có trong providers.test.mjs; audit tên props thật.)

- [ ] **Step 2: Chạy — FAIL**

  Run: `npm --prefix appmode test`
  Expected: FAIL — chưa có `[data-no-provider-warning]`.

- [ ] **Step 3: Cài đặt**

  Backend: thêm `providersConnected int` (hoặc `hasProvider bool`) vào body providers dùng `a.hasAnyConnectedProvider()` (T6). Frontend `providers.js`: đầu trang, nếu `data.providersConnected === 0` → render:
  ```js
  element("div", {
      className: "pv-no-provider",
      attributes: { "data-no-provider-warning": "true", role: "status" },
  }, "Chưa kết nối provider nào — bot sẽ IM LẶNG với người nhắn. Hãy Connect một provider.")
  ```
  CSS `.pv-no-provider`: nền amber nhạt, chữ đậm, bo góc (dùng token màu sẵn có, theme-aware).

- [ ] **Step 4: Chạy — PASS**

  Run: `npm --prefix appmode test` → banner tests PASS + 100 cũ vẫn xanh.

- [ ] **Step 5: Refactor** — không.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/daemon/app_llm_api.go appmode/overlay/internal/webui/static/pages/providers.js appmode/overlay/internal/webui/static/portal.css appmode/tests/providers.test.mjs
  git commit -m "feat(portal): warn when no provider connected (bot stays silent)"
  ```

---

### Task 9: CHECKPOINT (NEEDS-LOGIN) — E2E qua gói + full build gate

**Files:**
- Không sửa code (trừ fix phát sinh). Chạy gói thật + login thật.

**Reason TDD skipped:** capture-first E2E — login OAuth là hành động của user; xác minh hop node→native + cô lập per-account + im-khi-chưa-cấu-hình chỉ đo được trên gói thật. Giống #43/#46.

**Public behavior to verify:** fresh gói (data mới) im lặng + Portal banner; Connect Claude (OAuth thật) → npm install chạy → account hiện → combo dùng Claude → consult trả lời qua hop; nếu T1 chốt A-native, xác nhận không mồ côi.

- [ ] **Step 1: Build gate đầy đủ**

  ```powershell
  $env:ZALOBOT_REPO=[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User')
  $env:ZALOBOT_PERSONA=[Environment]::GetEnvironmentVariable('ZALOBOT_PERSONA','User')
  pwsh -NoProfile -File .\scripts\build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out F:\dist\_verify-nodefault
  ```
  Expected: 7/7 phase, canary "sach: khong con dau khach hang nao".

- [ ] **Step 2: Fresh data → im + banner**

  Chạy gói với data dir MỚI (không account). Gửi một tin Zalo test (hoặc qua đường test nội bộ). Expected: KHÔNG có tin trả khách; log daemon "chưa cấu hình provider, bot im"; Portal Providers hiện banner amber. (Xác nhận KHÔNG escalate/handoff.)

- [ ] **Step 3: Connect Claude thật (USER làm OAuth)**

  Trong Portal: Connect OpenAI? không — Connect **Claude**. Quan sát: install phase chạy `npm i -g @anthropic-ai/claude-code` (tải ~90MB, "Đang cài…" im một lúc) → login in URL → **user mở URL + đăng nhập** → account "Tài khoản 1" (label email) hiện. Xác nhận `data\cli\node_modules\@anthropic-ai\claude-code\cli-wrapper.cjs` tồn tại + `data\accounts\claude-code\<uuid>\` có creds.

- [ ] **Step 4: Consult qua hop**

  Tạo combo có claude-code (model haiku), set active. Gửi tin Zalo. Expected: bot trả lời (qua `node cli-wrapper.cjs -p --input-format stream-json --add-dir <kb>` dưới `CLAUDE_CONFIG_DIR` của account). Kiểm log: prompt tới, stream-json về, không kẹt trust-dialog.

- [ ] **Step 5: Hazard checks (theo T1)**

  Nếu T1=B-wrapper: gây một lượt timeout (prompt nặng + ngân sách ngắn) → xác nhận không còn `claude.exe` mồ côi sau lượt (`Get-Process claude`). Nếu còn mồ côi → áp fallback A-native-direct-exe (sửa descriptor + resolveCLIProgram theo Decisions #8), rebuild, lặp lại.

- [ ] **Step 6: Swap vào live + xác nhận** (tùy chọn, như các lần trước)

  Dừng daemon, copy `app\` mới đè (giữ `data\`), khởi động qua `Start.vbs nobrowser`. F5 Portal.

- [ ] **Step 7: Commit (nếu có fix phát sinh)**

  ```bash
  git add -A && git commit -m "fix: <mô tả> phát hiện trong E2E no-default+claude-npm"
  ```

---

## Self-review

**Spec coverage:**
- Phần 1 (bỏ default, im): T2 (sentinel) + T6 (router im + gate 0-provider) + T7 (bỏ seed). ✅
- Phần 2 (Claude npm B): T1 (spike/gate) + T4 (descriptor + probe/knowledge repoint) + T5 (install) + T3 (base Program) + T6 (wire Program). ✅
- Phần 3 (Portal banner): T8. ✅
- Capture-first (login, stdio/orphan hop, seam): T1 + T9. ✅
- Break site probeClaudeAuth + site thứ 3 app_knowledge: T4. ✅
- Base opt-in/backward-compat: T2 + T3 (rỗng = đường cũ). ✅
- Fallback A-native: T1 quyết định + T9 step 5 áp dụng nếu cần. ✅

**Placeholder scan:** T3 step 1 có một `resolveRunProgram` test là chính (test "echo" bị đánh dấu là không phản ánh đúng — đã hướng dẫn tách hàm thuần + test hàm đó). Các "audit tên thật lúc thực thi" là chỉ dẫn xác minh danh định (helper/field có thể khác tên), KHÔNG phải TBD logic. Không có bước code thiếu code.

**Type/naming consistency:** `ErrZaloSilent` (base, dùng ở T2/T6/T9); `silentZaloRunner` (T6); `hasAnyConnectedProvider` (T6, dùng lại T8); `zaloConfig.Program`/`ProgramPrefixArgs` (T3, set ở T6); `binJS`/`npmPackage`/`nativeBin` (T4). Nhất quán. `providerIDForKind`/`claudeCodeProviderID`/`subscriptionKinds`/`LLMCombos`/`LLMProviders`/`CredentialConfigured` — đánh dấu audit tên thật (có thể lệch trong codebase).

## Execute order

T1 (gate) → T2, T3 (base, độc lập) → T4, T5 (claude npm overlay) → T6 (router im + wire, cần T2/T3/T4) → T7 (bỏ seed) → T8 (Portal, cần T6 helper) → T9 (E2E + gate). Base commits (T2,T3) ở lại history AgentDC như CC4.
