# OpenCode synthetic safety spike — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` after the production
> multi-provider plan. Steps use `- [ ]` checkboxes.

**Goal:** Chứng minh một adapter OpenCode cô lập có thể discover model, chạy prompt synthetic bằng
stdin/NDJSON và xoá exact session, trong khi tuyệt đối không xuất hiện trong catalog production hoặc
nhận dữ liệu Zalo thật khi chưa có OS containment.

**TDD mode:** yes

**Architecture:** Một package-local driver dựng policy/env/workdir/XDG roots app-owned, parser NDJSON
fail-closed và chạy process qua lifecycle manager hiện có. Registry giữ descriptor OpenCode
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
- `permission:{"*":"deny"}`, `share:"disabled"`, `autoupdate:false`, `--pure`; argv cấm auto/yolo,
  attachment, session resume, server attach và prompt text.
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
      spec, err := newOpenCodeSpikeSpec(root, binary, "opencode/deepseek-v4-flash-free")
      require.NoError(t, err)
      require.NotContains(t, strings.Join(spec.Args, "\x00"), syntheticOpenCodePrompt)
      require.Equal(t, syntheticOpenCodePrompt, spec.Stdin)
      require.Contains(t, spec.Args, "--pure")
      require.Contains(t, spec.Args, "json")
  }

  func TestParseOpenCodeSpikeNDJSONReturnsExactSessionAndText(t *testing.T) {
      got, err := parseOpenCodeSpikeNDJSON(strings.NewReader(
          `{"type":"step_start","sessionID":"ses_1"}`+"\n"+
          `{"type":"text","sessionID":"ses_1","part":{"text":"OK"}}`+"\n"+
          `{"type":"step_finish","sessionID":"ses_1"}`+"\n"))
      require.NoError(t, err)
      require.Equal(t, openCodeSpikeResult{SessionID: "ses_1", Text: "OK"}, got)
  }

  func TestParseOpenCodeFreeModelsFiltersAndSorts(t *testing.T) {
      got, err := parseOpenCodeFreeModels(strings.NewReader(
          "opencode/mimo-v2.5-free\nother/unsafe-free\nopencode/deepseek-v4-flash-free\n"))
      require.NoError(t, err)
      require.Equal(t, []string{
          "opencode/deepseek-v4-flash-free", "opencode/mimo-v2.5-free",
      }, got)
  }
  ```

  Add cases for invalid UTF-8, >1 MiB line/total, unknown/malformed/mixed session, missing terminal,
  empty/oversized text, error/reasoning/tool events, empty/duplicate/whitespace/control/oversized model
  lines, banned namespace/suffix and every banned arg. Discovery argv is exactly
  `models opencode --pure` and never uses `--refresh`.

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

  Use `json.Decoder` per bounded line into exact typed envelopes; reject tool-related event types even
  if a later text/finish exists. Parse discovery as bounded UTF-8 lines, keep exact
  `opencode/<nonempty>-free`, dedupe and sort; prefer `opencode/deepseek-v4-flash-free` when present,
  otherwise the first sorted safe model. Inline config is canonical JSON with
  deny-all/share/autoupdate policy.

- [ ] **Step 4: Run GREEN**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(OpenCodeSpikeSpec|ParseOpenCodeSpike)'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_opencode_spike.go appmode/overlay/internal/daemon/app_opencode_spike_test.go
  git commit -m "feat: add safe OpenCode spike protocol"
  ```

### Task 2: Isolated process lifecycle và exact session compensation

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_opencode_spike.go`
- Modify: `appmode/overlay/internal/daemon/app_opencode_spike_test.go`
- Modify: `appmode/overlay/internal/daemon/app_cli_job_windows.go`
- Modify: `appmode/overlay/internal/daemon/app_cli_job_windows_test.go`

**Public behavior to verify:** Synthetic runner dùng toàn bộ app-owned roots, được cancel/reap và luôn
attempt xoá đúng session đã tạo trước khi trả kết quả.

- [ ] **Step 1: Write failing process-seam tests**

  ```go
  func TestRunOpenCodeSyntheticSpikeDeletesExactSession(t *testing.T) {
      fake := newOpenCodeFakeProcess(t, validOpenCodeEvents("ses_owned", "OK"))
      got, err := runOpenCodeSyntheticSpike(context.Background(), fake.capability(), fake.path())
      require.NoError(t, err)
      require.Equal(t, "OK", got)
      require.Equal(t, []string{"session", "delete", "ses_owned"}, fake.deleteArgs())
      require.True(t, fake.allPathsUnder(fake.root()))
  }
  ```

  Cover cancel before first event, cancel after session event, child process lifetime, run error,
  malformed stream, delete error, delete timeout, foreign session canary, symlink/reparse root and no
  output/prompt/session ID in logs. For cancel-before-event, fake `session list --format json` returns
  zero, one exact nonce title, two duplicates and a foreign title; only the single exact match may be
  passed to delete.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestRunOpenCodeSyntheticSpike|TestOpenCodeSpike.*Cancel'
  ```

- [ ] **Step 3: Implement managed runner**

  Create four exact XDG child roots plus empty workdir under a fresh app-owned account root. Generate a
  cryptographic 128-bit nonce and pass only `--title agentdc-spike-<base64url-nonce>` to `run`; the title
  contains no prompt/customer data. Delegate discovery, run, list and delete process creation/reaping to
  the existing managed-process seam. After run stops, use the parsed event session ID when present;
  otherwise execute `session list --pure --format json` in the same isolated roots and accept only one
  entry whose title exactly equals the nonce title. Run `session delete <exact-id>` when an owned ID
  exists. Malformed/duplicate/foreign-only listing fails closed; never delete a guessed ID. Finally
  remove the whole fresh app-owned root after all processes reap, including the no-session case.

- [ ] **Step 4: Run GREEN and lifecycle regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(RunOpenCodeSyntheticSpike|OpenCodeSpike|RunCLIProcess|CLIJob)'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_opencode_spike.go appmode/overlay/internal/daemon/app_opencode_spike_test.go appmode/overlay/internal/daemon/app_cli_job_windows.go appmode/overlay/internal/daemon/app_cli_job_windows_test.go
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
      got := advertisedOnboardingProviderOptions()
      require.NotContains(t, providerKinds(got), "opencode")
      _, err := appCanonicalOnboardingKinds([]string{"opencode"})
      require.ErrorIs(t, err, ErrOnboardingProviderUnsupported)
  }

  func TestOpenCodeLocalSmoke(t *testing.T) {
      binary := os.Getenv("AGENTDC_OPENCODE_SPIKE_BINARY")
      if binary == "" { t.Skip("explicit synthetic spike binary not supplied") }
      got, err := runVerifiedLocalOpenCodeSmoke(t.Context(), binary)
      require.NoError(t, err)
      require.Equal(t, "OK", got)
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

  Keep the descriptor in the internal registry with `Advertised:false`, `SyntheticOnly:true` and no
  production `Runner`/`AuthDriver`. Accept the smoke path only inside the test helper; verify exact
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

  The optional smoke must create/delete only its own temp roots and session. If signature policy does
  not accept the npm binary, report the gate as safely closed; do not weaken it or use another app's
  binary.

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
- no production route, account, model, session or Zalo prompt can reach the spike;
- promotion requires a new reviewed design implementing real OS containment and credential/retention
  policy.
