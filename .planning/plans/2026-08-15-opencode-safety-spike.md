# OpenCode synthetic safety spike — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` after the production
> multi-provider plan. Steps use `- [ ]` checkboxes.

**Goal:** Chứng minh một adapter OpenCode cô lập có thể discover model và chạy prompt synthetic bằng
stdin/NDJSON với database in-memory không để lại session, trong khi tuyệt đối không xuất hiện trong
catalog production hoặc nhận dữ liệu Zalo thật khi chưa có OS containment.

**TDD mode:** yes

**Architecture:** Một package-local driver dựng policy/env allowlist/workdir/XDG/npm roots app-owned,
ghim `OPENCODE_DB=:memory:`, npm offline và log `ERROR`; parser NDJSON fail-closed và chạy process qua
lifecycle manager hiện có.
Registry giữ descriptor OpenCode
`advertised=false`; smoke thật chỉ chạy khi test operator truyền explicit environment flag và chỉ dùng
prompt synthetic cố định. Không thêm HTTP route, auth import hoặc production runner binding.

**Tech stack:** Go, OpenCode `v1.18.18`, native Windows process/Job Object lifecycle hiện có, JSONL,
official free `opencode/*-free` models.

**Spec:** `.planning/specs/2026-08-15-onboarding-multi-provider-design.md` mục 10

**Research:** `.planning/research/onboarding-multi-provider-opencode-RESEARCH.md`

---

## Safety rules

- Không đọc `~/.config/opencode`, `~/.local/share/opencode`, credential, AGS binary/data hoặc project
  config. Mọi XDG root và workdir phải nằm dưới một temp/app-owned root exact.
- Không download/cài binary trong automated test. Resolver nhận binary app-owned đã cấu hình; local
  smoke có thể nhận path explicit từ operator rồi verify version/signature policy.
- Prompt production/customer không đi vào spike. Smoke dùng hằng synthetic `Chỉ trả lời đúng một từ:
  OK` và model free allowlisted.
- `permission:{"*":"deny"}`, `share:"disabled"`, `autoupdate:false`, `OPENCODE_DB=:memory:`,
  `OPENCODE_LOG_LEVEL=ERROR`, `--pure --log-level ERROR`; argv cấm auto/yolo,
  attachment, session resume, server attach và prompt text.
- Process env là allowlist: app-owned XDG data/config/cache/state, HOME/USERPROFILE, TEMP/TMP và các
  biến OpenCode/npm policy explicit. Không kế thừa ambient `OPENCODE_*`, `XDG_*`, proxy/registry
  credential, npm config hoặc provider API keys.
- `--pure` không chặn config bootstrap. Không set `OPENCODE_CONFIG_DIR`; ghim
  `NPM_CONFIG_OFFLINE=true`, owned `NPM_CONFIG_CACHE`/`NPM_CONFIG_PREFIX`, và tắt audit/fund/update
  notifier. Bootstrap/log file chỉ được nằm trong owned root, được scan chống synthetic-data leakage rồi
  xoá sau khi toàn bộ process tree đã reap.
- Job Object/process group chỉ cleanup lifecycle, không được gọi là sandbox. Driver trả
  `ErrOpenCodeContainmentUnavailable` cho mọi attempt không mang explicit synthetic-test capability.

### Task 1: Pure policy builder và bounded NDJSON parser

**Files:**
- Create: `appmode/overlay/internal/daemon/app_opencode_spike.go`
- Create: `appmode/overlay/internal/daemon/app_opencode_spike_test.go`

**Public behavior to verify:** Driver discover và chọn deterministic model `opencode/*-free`, chỉ dựng
safe argv/env và chỉ nhận một session-consistent stream text hoàn tất không có tool event.

- [ ] **Step 1: Write failing pure behavior tests**

  ```go
  func TestOpenCodeSpikeSpecNeverPlacesPromptInArgv(t *testing.T) {
      root := t.TempDir()
      binary := filepath.Join(root, "opencode.exe")
      spec, err := newOpenCodeSpikeSpec(root, binary, "opencode/deepseek-v4-flash-free")
      if err != nil { t.Fatal(err) }
      if strings.Contains(strings.Join(spec.Args, "\x00"), syntheticOpenCodePrompt) {
          t.Fatal("prompt leaked into argv")
      }
      if spec.Stdin != syntheticOpenCodePrompt { t.Fatalf("stdin = %q", spec.Stdin) }
      wantArgs := []string{"--pure", "--log-level", "ERROR", "run", "--format", "json",
          "--model", "opencode/deepseek-v4-flash-free", "--agent", "agentdc-synthetic",
          "--dir", filepath.Join(root, "work")}
      if !slices.Equal(spec.Args, wantArgs) { t.Fatalf("args = %#v; want %#v", spec.Args, wantArgs) }
      for _, want := range []string{"OPENCODE_DB=:memory:", "OPENCODE_LOG_LEVEL=ERROR", "NPM_CONFIG_OFFLINE=true"} {
          if !slices.Contains(spec.Env, want) { t.Fatalf("env missing %q", want) }
      }
  }

  func TestParseOpenCodeSpikeNDJSONReturnsExactSessionAndText(t *testing.T) {
      got, err := parseOpenCodeSpikeNDJSON(strings.NewReader(
          `{"type":"step_start","timestamp":1,"sessionID":"ses_1","part":{"id":"prt_1","sessionID":"ses_1","messageID":"msg_1","type":"step-start"}}`+"\n"+
          `{"type":"text","timestamp":2,"sessionID":"ses_1","part":{"id":"prt_2","sessionID":"ses_1","messageID":"msg_1","type":"text","text":"OK","time":{"start":1,"end":2}}}`+"\n"+
          `{"type":"step_finish","timestamp":3,"sessionID":"ses_1","part":{"id":"prt_3","sessionID":"ses_1","messageID":"msg_1","type":"step-finish","reason":"stop","cost":0,"tokens":{"total":1,"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}}`+"\n"))
      if err != nil { t.Fatal(err) }
      want := openCodeSpikeResult{SessionID: "ses_1", Text: "OK"}
      if got != want { t.Fatalf("result = %#v; want %#v", got, want) }
  }

  func TestParseOpenCodeSpikeFreeModelsSortsAndSelects(t *testing.T) {
      got, err := parseOpenCodeFreeModels(strings.NewReader(
          "opencode/mimo-v2.5-free\nopencode/deepseek-v4-flash-free\n"))
      if err != nil { t.Fatal(err) }
      want := []string{"opencode/deepseek-v4-flash-free", "opencode/mimo-v2.5-free"}
      if !slices.Equal(got, want) { t.Fatalf("models = %#v; want %#v", got, want) }
      selected, err := selectOpenCodeSpikeModel(got)
      if err != nil { t.Fatal(err) }
      if selected != "opencode/deepseek-v4-flash-free" { t.Fatalf("selected = %q", selected) }
  }

  func TestOpenCodeSpikeModelDiscoverySpecIsExact(t *testing.T) {
      root := t.TempDir()
      binary := filepath.Join(root, "opencode.exe")
      spec, err := newOpenCodeModelDiscoverySpec(root, binary)
      if err != nil { t.Fatal(err) }
      want := []string{"--pure", "--log-level", "ERROR", "models", "opencode"}
      if !slices.Equal(spec.Args, want) { t.Fatalf("args = %#v; want %#v", spec.Args, want) }
      if spec.Stdin != "" { t.Fatal("discovery unexpectedly has stdin") }
  }
  ```

  Add cases for invalid UTF-8, >1 MiB line/total, unknown/malformed/mixed session, missing terminal,
  empty/oversized text, error/reasoning/tool events, empty/duplicate/whitespace/control/oversized model
  lines, banned namespace/suffix and every banned arg. Discovery is fail-closed: any nonempty line not
  matching the exact bounded `opencode/<safe>-free` grammar rejects the whole response; only exact valid
  duplicates are deduplicated. Require finite top-level timestamp; nested
  `id/sessionID/messageID/type`; nested/top-level session equality; completed text `time.end`; complete
  finish `reason/cost/tokens/cache`; and exact order start → one-or-more text → one finish. Run has no
  positional message and uses exact `--pure --log-level ERROR run --format json --model <id> --agent
  agentdc-synthetic --dir <empty-root>`. Discovery argv is exactly `--pure --log-level ERROR models
  opencode` and never uses `--refresh`.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(OpenCodeSpikeSpec|ParseOpenCodeSpike)'
  ```

- [ ] **Step 3: Implement pure builder/parser**

  ```go
  type openCodeSpikeSpec struct {
      Binary, WorkDir, Stdin string
      Args, Env []string
  }

  type openCodeSpikeResult struct { SessionID, Text string }

  var ErrOpenCodeContainmentUnavailable = errors.New("opencode OS containment unavailable")
  ```

  Use only the standard library (the repo does not depend on Testify). Before typed decoding, perform a
  bounded recursive token walk with exact case-sensitive allowed-key sets for the top object and nested
  `part/time/tokens/cache` objects. Reject duplicate decoded keys (including `\u` aliases), case-folded
  aliases such as `Type`, excess depth and any unknown protocol key; `json.Decoder` alone is insufficient
  because it accepts duplicate and case-insensitive struct keys. Documented optional `metadata` may contain
  bounded objects/arrays, but the recursive walker still rejects duplicate/case-folded keys and enforces
  max depth 16/max 4096 tokens inside it. Then use `json.Decoder` per bounded line
  into exact typed envelopes; reject tool-related event types even
  if a later text/finish exists. Parse discovery as bounded UTF-8 lines, keep exact
  `opencode/<safe>-free`, reject the whole stream on any invalid nonempty line, dedupe exact valid lines
  and sort. Add `newOpenCodeModelDiscoverySpec(root,binary)` for the separate exact no-stdin discovery
  argv/env and `selectOpenCodeSpikeModel`: prefer `opencode/deepseek-v4-flash-free` when present,
  otherwise the first sorted safe model; empty input returns a typed no-model error. Inline config is
  canonical JSON with a dedicated synthetic
  agent plus `enabled_providers:["opencode"]`, deny-all/share/snapshot/autoupdate policy. Env ghim
  `OPENCODE_DB=:memory:`, `OPENCODE_LOG_LEVEL=ERROR`, disable project config, external skills,
  Claude-code import, LSP download, autoupdate, model refresh, autocompact and auto-share. Npm ghim
  offline, owned cache/prefix, audit/fund/update notifier off; tuyệt đối không set `OPENCODE_CONFIG_DIR`.
  Exact allowlist gồm các app-owned `XDG_DATA_HOME/XDG_CONFIG_HOME/XDG_CACHE_HOME/XDG_STATE_HOME`,
  `HOME/USERPROFILE`, `TEMP/TMP`; `OPENCODE_CONFIG_CONTENT`, `OPENCODE_DB=:memory:`,
  `OPENCODE_PURE=1`, `OPENCODE_PERMISSION={"*":"deny"}`, `OPENCODE_LOG_LEVEL=ERROR`,
  `OPENCODE_DISABLE_PROJECT_CONFIG=1`, `OPENCODE_DISABLE_EXTERNAL_SKILLS=1`,
  `OPENCODE_DISABLE_CLAUDE_CODE=1`, `OPENCODE_DISABLE_LSP_DOWNLOAD=1`,
  `OPENCODE_DISABLE_AUTOUPDATE=1`, `OPENCODE_DISABLE_MODELS_FETCH=1`,
  `OPENCODE_DISABLE_AUTOCOMPACT=1`, `OPENCODE_AUTO_SHARE=0`; và npm offline/owned variables trên.
  Trên Windows, explicit pin `SYSTEMROOT` từ một absolute system root đã validate vì `os/exec` sẽ tự
  chèn ambient `SYSTEMROOT` nếu thiếu; test Task 2 phải kiểm `cmd.Environ()`, không chỉ `spec.Env`.
  Không forward ambient `PATH`, proxy, registry, telemetry, API-key hoặc credential variables; nếu native
  Windows runtime sau này chứng minh cần một biến hệ thống cụ thể, phải thêm bằng RED test và review riêng.
  Vì prompt smoke chỉ cần `OK`, cap line và tổng output giữ 1 MiB; không nới lên cap chung 8 MiB của
  Claude. NDJSON còn có cap 1024 events để nhiều event nhỏ không tạo CPU loop; IDs/message/reason/model
  đều ASCII bounded, một message ID xuyên stream và part IDs không được trùng. Chấp nhận đúng optional
  v1.18.18 `snapshot/metadata/synthetic/ignored/tokens.total`, không chấp nhận field tự bịa.

- [ ] **Step 4: Run GREEN**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(OpenCodeSpikeSpec|ParseOpenCodeSpike)'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_opencode_spike.go appmode/overlay/internal/daemon/app_opencode_spike_test.go
  git commit -m "feat: add safe OpenCode spike protocol"
  ```

### Task 2: Isolated process lifecycle và zero-retention cleanup

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_opencode_spike.go`
- Modify: `appmode/overlay/internal/daemon/app_opencode_spike_test.go`
- Create: `appmode/overlay/internal/daemon/app_opencode_process.go`
- Create: `appmode/overlay/internal/daemon/app_opencode_process_test.go`
- Modify: `appmode/overlay/internal/daemon/app_cli_job_windows.go`
- Modify: `appmode/overlay/internal/daemon/app_cli_job_windows_test.go`
- Modify: `appmode/overlay/internal/daemon/app_cli_job_unix.go`
- Modify: `appmode/overlay/internal/daemon/app_cli_job_unix_test.go`
- Modify: `appmode/overlay/internal/daemon/app_cli_process_group.go`
- Modify: `appmode/overlay/internal/daemon/app_cli_process_group_test.go`

**Public behavior to verify:** Synthetic runner dùng toàn bộ app-owned roots, được cancel/reap, dùng
SQLite in-memory và xoá exact temp root sau khi process dừng mà không tạo DB/WAL/session artifact.

- [ ] **Step 1: Write failing process-seam tests**

  ```go
  func TestRunOpenCodeSyntheticSpikeLeavesNoDurableSession(t *testing.T) {
      fake := newOpenCodeFakeProcess(t, validOpenCodeEvents("ses_owned", "OK"))
      got, err := runOpenCodeSyntheticSpike(context.Background(), fake.capability(), fake.path())
      if err != nil { t.Fatal(err) }
      if got != "OK" { t.Fatalf("answer = %q", got) }
      if calls := fake.sessionManagementArgs(); len(calls) != 0 { t.Fatalf("session calls = %#v", calls) }
      if fake.anyDatabaseArtifact() { t.Fatal("durable database artifact exists") }
      if !fake.allPathsUnder(fake.root()) { t.Fatal("child escaped owned root") }
  }
  ```

  Cover cancel before first event, cancel after session event, child process lifetime, run error,
  malformed stream, symlink/reparse root and no output/prompt/session ID in logs. Add canary files for
  `opencode.db`, `opencode.db-wal`, `opencode.db-shm`; permit bootstrap/log artifacts only below the
  exact owned root, scan every bounded regular file for full-prompt/per-run-title/raw-error/session leakage, then
  prove exact root removal. Assert no `session list/delete` or surviving background process is ever
  spawned, npm is offline and no registry/proxy credential is forwarded. Build the actual `exec.Cmd`
  and assert `cmd.Environ()` equals the reviewed allowlist (including the one explicit Windows
  `SYSTEMROOT`) so Go's critical-environment injection cannot bypass the test. Prove
  stdout, stderr, NDJSON line and aggregate caps at the process boundary. After a cap is crossed the
  runner must continue draining both pipes to EOF before reap, so a noisy child cannot deadlock on a full
  pipe; raw stderr/output never enters the returned error or log.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestRunOpenCodeSyntheticSpike|TestOpenCodeSpike.*Cancel'
  ```

- [ ] **Step 3: Implement managed runner**

  Create four exact XDG child roots plus isolated HOME, TEMP and empty workdir under a fresh app-owned
  root. Generate a cryptographic 128-bit nonce and pass only `--title agentdc-spike-<base64url-nonce>`
  to `run`; the title contains no prompt/customer data. A dedicated OpenCode runner uses
  `startManagedCLIProcess` for Job Object/process-group ownership, but drains bounded stdout/stderr itself
  instead of calling the existing unbounded `runCLIProcess`. Both processes receive
  `OPENCODE_DB=:memory:`, npm-offline/log-level policy and the exact allowlisted env. The parsed session ID is only a
  stream-consistency token. Finally remove the whole fresh app-owned root after all processes reap,
  including cancel/no-session paths. Before removal, scan owned artifacts without logging their contents;
  failure to prove no DB/WAL/SHM, no synthetic-data leakage, no background child or cleanup is a failed
  spike. A real smoke on a fresh root may succeed using already available packages or fail with a typed
  offline-bootstrap error; it must never retry with npm online.

  Extend the platform lifecycle with a package-private `managedCLIProcessQuiescer` contract used only by
  the synthetic runner. On Windows it calls `TerminateJobObject`, polls
  `JobObjectBasicAccountingInformation.ActiveProcesses` to zero under a bounded cleanup context, then
  closes the Job handle; on supported Unix it kills the process group and polls `kill(-pgid, 0)` until
  `ESRCH`. Unsupported platforms fail before spawn. Root `cmd.Wait` and quiescence are both required;
  timeout/query failure closes the lifetime boundary but forbids artifact scan/delete and returns a
  typed cleanup error. Existing `runCLIProcess` behavior remains unchanged.

  Artifact audit uses `os.OpenRoot` plus `Lstat`, max depth 16, max 4096 entries, max 1 MiB per regular
  file and max 16 MiB aggregate bytes. It rejects symlink/reparse/special entries and any overflow before
  reading content; traversal never follows an entry outside the root. After a quiescent process tree,
  success/error/parser-failure paths audit then remove the exact owned root. If audit fails, still attempt
  exact owned-root removal and return the joined error; if quiescence is unproven, do not race a scan or
  delete against live descendants.

- [ ] **Step 4: Run GREEN and lifecycle regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(RunOpenCodeSyntheticSpike|OpenCodeSpike|ManagedCLIProcessQuiesce|RunCLIProcess|CLIJob)'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_opencode_spike.go appmode/overlay/internal/daemon/app_opencode_spike_test.go appmode/overlay/internal/daemon/app_opencode_process.go appmode/overlay/internal/daemon/app_opencode_process_test.go appmode/overlay/internal/daemon/app_cli_job_windows.go appmode/overlay/internal/daemon/app_cli_job_windows_test.go appmode/overlay/internal/daemon/app_cli_job_unix.go appmode/overlay/internal/daemon/app_cli_job_unix_test.go appmode/overlay/internal/daemon/app_cli_process_group.go appmode/overlay/internal/daemon/app_cli_process_group_test.go
  git commit -m "feat: isolate OpenCode synthetic runs"
  ```

### Task 3: Production deny gate và opt-in local smoke

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_provider_registry.go`
- Modify: `appmode/overlay/internal/daemon/app_provider_registry_test.go`
- Modify: `appmode/overlay/internal/daemon/app_opencode_spike.go`
- Modify: `appmode/overlay/internal/daemon/app_opencode_spike_test.go`
- Create: `appmode/overlay/internal/daemon/app_opencode_signature_windows.go`
- Create: `appmode/overlay/internal/daemon/app_opencode_signature_other.go`
- Create: `appmode/overlay/internal/daemon/app_opencode_signature_windows_test.go`
- Create: `appmode/overlay/internal/daemon/app_opencode_smoke_test.go`

**Public behavior to verify:** OpenCode không xuất hiện trong production status/catalog, không thể được
chọn qua HTTP, và smoke thật chỉ chạy khi operator cấp explicit synthetic capability/path.

- [ ] **Step 1: Write failing deny/smoke tests**

  ```go
  func TestProviderRegistryNeverAdvertisesOpenCodeWithoutContainment(t *testing.T) {
      got := appProviderOptions()
      for _, option := range got {
          if option.Kind == "opencode" { t.Fatal("OpenCode was advertised") }
      }
      _, err := appCanonicalOnboardingKinds([]string{"opencode"})
      if !errors.Is(err, providercatalog.ErrUnsupportedKind) {
          t.Fatalf("error = %v; want unsupported kind", err)
      }
  }

  func TestOpenCodeLocalSmoke(t *testing.T) {
      binary := os.Getenv("AGENTDC_OPENCODE_SPIKE_BINARY")
      if binary == "" { t.Skip("explicit synthetic spike binary not supplied") }
      got, err := runVerifiedLocalOpenCodeSmoke(t.Context(), binary)
      if err != nil { t.Fatal(err) }
      if got != "OK" { t.Fatalf("answer = %q", got) }
  }
  ```

  Add a test proving environment flags alone cannot create a production catalog option or runner
  binding, and binary path/version/hash/signature mismatch stops before process start. Pin release
  `1.18.18`, Windows x64 SHA-256
  `B6EFA9EB3EE1D5C25438F3CBA03C471E5D8661A6121ABC47F209DB3B3F05F415`, valid WinVerifyTrust result,
  and signer subject containing `Anomaly Innovations, Inc`; non-Windows smoke remains unavailable
  until that platform has its own signed manifest.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestProviderRegistryNeverAdvertisesOpenCode|TestOpenCode.*Gate'
  ```

- [ ] **Step 3: Implement explicit gate**

  Do not add OpenCode to `providercatalog.Options`, `appProviderCapabilities`, `cliDescriptors`,
  `subscriptionKinds`, router or onboarding runner. Define a separate package-private
  `appSyntheticRuntimeDescriptor`/map in `app_opencode_spike.go` with `Advertised:false`,
  `SyntheticOnly:true` and no production `Runner`/`AuthDriver`; no production lookup consumes this map.
  Accept the smoke path only inside the test helper; verify exact
  `opencode --version == 1.18.18`, pinned SHA-256 and Authenticode trust/signer via native Windows APIs
  before run. Never invoke PowerShell/shell text to verify the binary.

- [ ] **Step 4: Run GREEN, optional real smoke and full daemon suite**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestProviderRegistryNeverAdvertisesOpenCode|TestOpenCode.*Gate'
  $env:AGENTDC_OPENCODE_SPIKE_BINARY='C:\nvm4w\nodejs\node_modules\opencode-ai\bin\opencode.exe'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestOpenCodeLocalSmoke'
  Remove-Item Env:AGENTDC_OPENCODE_SPIKE_BINARY
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
  ```

  The optional smoke must use only its own temp roots, in-memory database, npm-offline policy and fixed
  synthetic prompt. It must verify no SQLite/WAL/prompt-bearing artifact before deleting the owned root.
  If offline bootstrap cannot complete or signature policy does not accept the npm binary, report the
  gate as safely closed; do not enable network, weaken policy or use another app's binary.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_provider_registry.go appmode/overlay/internal/daemon/app_provider_registry_test.go appmode/overlay/internal/daemon/app_opencode_spike.go appmode/overlay/internal/daemon/app_opencode_spike_test.go appmode/overlay/internal/daemon/app_opencode_signature_windows.go appmode/overlay/internal/daemon/app_opencode_signature_other.go appmode/overlay/internal/daemon/app_opencode_signature_windows_test.go appmode/overlay/internal/daemon/app_opencode_smoke_test.go
  git commit -m "test: gate OpenCode synthetic integration"
  ```

## Final spike gate

Run full overlay and secret/copy scans. The expected product state remains:

- multi-provider Codex/Claude production onboarding works;
- OpenCode source/tests exist and synthetic smoke evidence is recorded;
- `GET /onboarding/status` does not advertise OpenCode;
- no production route, account, model, durable session or Zalo prompt can reach the spike;
- promotion requires a new reviewed design implementing real OS containment and credential/retention
  policy.
