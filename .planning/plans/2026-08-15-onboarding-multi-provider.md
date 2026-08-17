# Onboarding đa Provider, cài tuần tự — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or
> `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Cho phép nhiều Provider cùng được ON và lưu bền trước khi cài, cài/kết nối từng Provider
theo CTA riêng, kiểm thử fallback staging và atomically hoàn tất thành một Combo nhiều member.

**TDD mode:** yes

**Architecture:** Singleton onboarding tiếp tục giữ lifecycle/revision và đúng một active install
slot; bảng V8 mới giữ tập Provider đã chọn cùng trạng thái pending/ready và position canonical theo
catalog. Daemon cung cấp catalog, mutation set, orchestration tuần tự và route staging; Portal chỉ
render snapshot authoritative, không giữ selection shadow. OpenCode không nằm trong plan production
này; một plan spike riêng sẽ giữ nó `advertised=false` cho tới khi đạt safety gate.

**Tech stack:** Go, SQLite, vanilla JavaScript ES modules, Node `node:test`, PowerShell overlay test
harness, Provider Connect controller và Portal DOM harness hiện có.

**Spec:** `.planning/specs/2026-08-15-onboarding-multi-provider-design.md`

**Research:** `.planning/research/onboarding-multi-provider-opencode-RESEARCH.md`

**Workspace:** `D:/TuvanZalo/_build/.worktrees/portal-m1`, worktree riêng trên branch
`integration/main-memory-v2`.

---

## Execution rules

- Chỉ sửa worktree nêu trên; không dùng dữ liệu live trong `D:\TuvanZalo\data`, không broad-kill
  tiến trình, không đọc credential/token/log hội thoại.
- Mỗi task có một commit riêng. Với Go, helper overlay yêu cầu tree sạch: commit RED tests vào commit
  của task, chạy RED, thêm production code rồi `git commit --amend --no-edit`; không tạo commit rác.
- RED hợp lệ là assertion hành vi mới thất bại hoặc build thiếu đúng public symbol mới. Fixture/syntax
  hỏng, timeout không liên quan hoặc test flaky không phải RED hợp lệ.
- Mọi HTTP mutation dùng auth/mutation policy hiện có, strict JSON một document, bounded input và
  revision CAS. Không cancel job hợp lệ trước khi stale/phase/kind preflight thành công.
- Singleton + stage rows phải được đọc trong cùng SQLite read transaction. Mọi mutation stage tăng
  singleton revision đúng một lần.
- Không lưu config path, command, token, receipt, prompt, answer hoặc log riêng tư trong response.
- `onboarding.js`, mọi production/test JS mới và các file đã chạm giữ tối đa 800 dòng.
- UI giữ modal Tư Vấn Zalo trên nền lưới, sidebar ẩn, không đưa tên/branding/asset của sản phẩm tham
  chiếu vào runtime.

## File responsibility map

- `appmode/overlay/internal/store/app_schema.go` — schema V8 và orchestration migration.
- `appmode/overlay/internal/providercatalog/catalog.go` — metadata/rank/canonical selection dùng chung,
  không phụ thuộc Store hoặc daemon.
- `appmode/overlay/internal/store/app_onboarding_providers.go` — snapshot transaction, stage rows,
  selected-set CAS, begin/setup/back transitions.
- `appmode/overlay/internal/store/app_onboarding.go` — receipt/Complete dùng route staging nhiều row.
- `appmode/overlay/internal/store/app_llm.go` — guard Provider/Account/model đang được stage.
- `appmode/overlay/internal/daemon/app_provider_registry.go` — catalog/canonical rank/capabilities.
- `appmode/overlay/internal/daemon/app_onboarding.go` — HTTP contracts, projection và orchestration.
- `appmode/overlay/internal/daemon/app_llm_connect.go` — bind Connect vào active pending stage.
- `appmode/overlay/internal/daemon/app_onboarding_runner.go` — pinned fallback Test Chat.
- `appmode/overlay/internal/webui/static/pages/onboarding-contract.js` — strict catalog/stage/status
  normalization và service methods.
- `appmode/overlay/internal/webui/static/pages/onboarding-provider-flow.js` — pure set mutation/begin
  reconciliation.
- `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js` — generic rows/toggles/CTA.
- `appmode/overlay/internal/webui/static/pages/onboarding.js` — generation/abort orchestration only.
- `appmode/tests/helpers/onboarding-fixtures.mjs` — real-shape status/catalog/stage fixtures.
- `appmode/tests/onboarding-multi-provider.test.mjs` — dedicated public UI/service regressions.

## Test command conventions

Run from worktree root:

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run '<regex>'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '<regex>'
node --test appmode/tests/<file>.test.mjs
npm test --prefix appmode
```

---

### Task 1: Schema V8 và snapshot onboarding nguyên tử

**Files:**
- Modify: `appmode/overlay/internal/store/app_schema.go`
- Create: `appmode/overlay/internal/store/app_onboarding_providers.go`
- Modify: `appmode/overlay/internal/store/app_schema_test.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`

**Public behavior to verify:** Fresh/V7 database mở thành V8 với stage rows đúng phase; caller luôn đọc
singleton và ordered rows thuộc cùng một revision.

- [ ] **Step 1: Write failing Store behavior tests**

  Thêm test table cho `provider/connect/setup/persona/test/completed`, future version 9, và snapshot:

  ```go
  type OnboardingProviderStage struct {
      Kind, Status, ProviderID, AccountID, ModelID string
      Position int
  }

  type OnboardingSnapshot struct {
      State  OnboardingState
      Stages []OnboardingProviderStage
  }

  func TestMigrateAppV7OnboardingProviderStages(t *testing.T) {
      cases := []struct {
          phase string
          wantStatus string
          wantRows int
      }{
          {"provider", "", 0}, {"connect", "pending", 1},
          {"setup", "pending", 1}, {"persona", "ready", 1},
          {"test", "ready", 1}, {"completed", "", 0},
      }
      for _, tc := range cases {
          t.Run(tc.phase, func(t *testing.T) {
              st, oldRevision := openMigratedV7Fixture(t, tc.phase)
              got, err := st.OnboardingSnapshot()
              require.NoError(t, err)
              require.Len(t, got.Stages, tc.wantRows)
              if tc.wantRows == 1 {
                  require.Equal(t, tc.wantStatus, got.Stages[0].Status)
                  require.Equal(t, 0, got.Stages[0].Position)
                  require.Equal(t, oldRevision+1, got.State.Revision)
              }
          })
      }
  }

  func TestOnboardingSnapshotReadsStateAndStagesAtomically(t *testing.T) {
      st := openV8FixtureWithStages(t, OnboardingPhaseProvider, []OnboardingProviderStage{
          {Kind: "codex", Status: "ready", Position: 0,
              ProviderID: "codex", AccountID: "acct-c", ModelID: "gpt-5.6-terra"},
          {Kind: "claude-code", Status: "pending", Position: 1},
      })
      got, err := st.OnboardingSnapshot()
      require.NoError(t, err)
      require.Equal(t, OnboardingPhaseProvider, got.State.Phase)
      require.Equal(t, []string{"codex", "claude-code"}, stageKinds(got.Stages))
      require.Equal(t, []int{0, 1}, stagePositions(got.Stages))
  }
  ```

  `openMigratedV7Fixture`, `openV8FixtureWithStages`, `stageKinds` và `stagePositions` là test helpers
  được thêm trong cùng file; fixture ghi state + rows bằng một SQL transaction trước khi gọi public
  `OnboardingSnapshot`.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(MigrateAppV7OnboardingProviderStages|OnboardingSnapshotReadsStateAndStagesAtomically|MigrateAppRejectsFutureSchema)'
  ```

  Expected: build fails only because V8 stage/snapshot symbols are absent, or assertions show schema
  version/rows still follow V7.

- [ ] **Step 3: Implement schema, migration and read transaction**

  Use this exact row contract and a dedicated read transaction:

  ```sql
  CREATE TABLE app_onboarding_provider_stages (
    kind TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('pending','ready')),
    position INTEGER NOT NULL CHECK (position >= 0),
    provider_id TEXT NOT NULL DEFAULT '',
    account_id TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
  );
  CREATE UNIQUE INDEX app_onboarding_provider_position
    ON app_onboarding_provider_stages(position);
  ```

  Set `appSchemaVersion = 8`. Migrate V7 in one transaction; clear singleton provider fields only for
  persona/test after copying exact identity into the ready row. Keep `CurrentOnboardingVersion` at 1.

- [ ] **Step 4: Run GREEN and regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(MigrateApp|OnboardingSnapshot)'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_schema.go appmode/overlay/internal/store/app_schema_test.go appmode/overlay/internal/store/app_onboarding_providers.go appmode/overlay/internal/store/app_onboarding_test.go
  git commit -m "feat: add multi-provider onboarding stages"
  ```

### Task 2: Persist selected set và bắt đầu đúng một pending Provider

**Files:**
- Create: `appmode/overlay/internal/providercatalog/catalog.go`
- Create: `appmode/overlay/internal/providercatalog/catalog_test.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_providers.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`

**Public behavior to verify:** Cả Codex và Claude có thể được selected cùng lúc, pending row có thể
thêm/bỏ, ready row luôn được giữ, và CTA chỉ chuyển kind được chọn sang active Connect.

- [ ] **Step 1: Write failing Store tests through exported methods**

  ```go
  func TestReplaceOnboardingProviderSelectionPersistsTwoKinds(t *testing.T) {
      got, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
      require.NoError(t, err)
      require.Equal(t, int64(2), got.State.Revision)
      require.Equal(t, []string{"codex", "claude-code"}, stageKinds(got.Stages))
  }

  func TestBeginOnboardingProviderKeepsOtherSelectedRows(t *testing.T) {
      got, err := st.BeginOnboardingProvider(2, "claude-code")
      require.NoError(t, err)
      require.Equal(t, OnboardingPhaseConnect, got.State.Phase)
      require.Equal(t, "claude-code", got.State.ProviderKind)
      require.Len(t, got.Stages, 2)
  }
  ```

  Cover zero/duplicate/>8 kinds, noncanonical position, stale concurrency, exact one-revision lost
  response, remove pending, reject removal of ready, begin unselected/ready/other phase, and no partial
  mutation on failpoint/commit error.

  Add an explicit phase table proving selection accepts only `provider`; `connect/setup/persona/test/
  completed` return `ErrOnboardingInvalidPhase` with the full database digest unchanged.

  Thêm case bắt buộc: snapshot có một ready row và một pending row; bỏ pending cuối phải tạo
  `staged_combo_id`, chuyển `provider -> persona`, giữ ready row và tăng revision đúng một lần.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(ReplaceOnboardingProviderSelection|BeginOnboardingProvider)'
  ```

- [ ] **Step 3: Implement minimum transactional Store API**

  ```go
  func (s *Store) ReplaceOnboardingProviderSelection(
      expectedRevision int64, kinds []string,
  ) (OnboardingSnapshot, error)

  func (s *Store) BeginOnboardingProvider(
      expectedRevision int64, kind string,
  ) (OnboardingSnapshot, error)
  ```

  The shared `providercatalog.CanonicalSelectedKinds(kinds)` validates advertised kinds, duplicates and
  the limit of 8, then sorts by unique route rank. Store calls it itself, so direct Store callers cannot
  bypass order; daemon reuses the same function for request errors. Require phase `provider` before any
  row write. Compare the full exact successor for lost-response idempotence. Reindex positions inside
  the same transaction and increment singleton revision once. Nếu mutation làm mọi row còn lại đều
  ready, cấp combo UUID và chuyển sang Persona trong chính transaction đó; zero selected chỉ ở lại
  Provider.

- [ ] **Step 4: Run GREEN and full Store onboarding slice**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test.*Onboarding'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/providercatalog/catalog.go appmode/overlay/internal/providercatalog/catalog_test.go appmode/overlay/internal/store/app_onboarding_providers.go appmode/overlay/internal/store/app_onboarding_test.go
  git commit -m "feat: persist onboarding provider selection"
  ```

### Task 3: Server-driven catalog, status projection và selection HTTP API

**Files:**
- Create: `appmode/overlay/internal/daemon/app_provider_registry.go`
- Create: `appmode/overlay/internal/daemon/app_provider_registry_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_connect.go`

**Public behavior to verify:** Status trả catalog và ordered stages không bí mật; PUT selected set lưu cả
hai toggle; singular PUT chỉ bắt đầu một selected pending kind.

- [ ] **Step 1: Write failing HTTP/registry tests**

  ```go
  func TestOnboardingStatusProjectsCatalogAndTwoSelectedProviders(t *testing.T) {
      res := authenticatedGET(t, app, "/onboarding/status")
      require.Equal(t, []string{"codex", "claude-code"}, responseStageKinds(res))
      require.Equal(t, 10, option(res, "codex").RouteRank)
      require.Greater(t, option(res, "claude-code").RouteRank, option(res, "codex").RouteRank)
      require.NotContains(t, responseBody(res), "config_dir")
  }

  func TestOnboardingProvidersPutPersistsCanonicalSet(t *testing.T) {
      res := authenticatedJSON(t, app, http.MethodPut, "/onboarding/providers",
          `{"revision":1,"selected_kinds":["claude-code","codex"]}`)
      require.Equal(t, []string{"codex", "claude-code"}, responseStageKinds(res))
  }
  ```

  Cover auth/mutation policy, strict JSON, unsupported/duplicate/blank/>8, stale/no side effect, exact
  lost response, dirty active slot, advertised-only catalog and stable error codes.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppProviderRegistry|AppOnboarding(StatusProjectsCatalog|ProvidersPut|ProviderBeginsSelected))'
  ```

- [ ] **Step 3: Implement registry and routes**

  ```go
  type appProviderOption struct {
      Kind, DisplayName, Description string
      Recommended, Beta, Advertised bool
      RouteRank int
  }

  func appCanonicalOnboardingKinds(input []string) ([]string, error)
  ```

  Build daemon driver lookup around the shared provider catalog; Codex ranks before Claude Code. Replace
  hard-coded onboarding/subscription allowlists with lookup through the registry where behavior is
  equivalent. Route `PUT /onboarding/providers`; reinterpret
  existing `PUT /onboarding/provider` as begin-selected-pending. Status comes from one Store snapshot.

- [ ] **Step 4: Run GREEN and focused route regression**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppProviderRegistry|AppOnboarding|ConnectOnboarding)'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_provider_registry.go appmode/overlay/internal/daemon/app_provider_registry_test.go appmode/overlay/internal/daemon/app_onboarding.go appmode/overlay/internal/daemon/app_onboarding_test.go appmode/overlay/internal/daemon/app_routes.go appmode/overlay/internal/daemon/app_llm_connect.go
  git commit -m "feat: expose onboarding provider catalog"
  ```

### Task 4: Bind và Setup từng Provider, quay lại danh sách cho tới hàng cuối

**Files:**
- Modify: `appmode/overlay/internal/store/app_onboarding_providers.go`
- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_connect.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_connect_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`

**Public behavior to verify:** Provider đầu setup xong thành ready rồi trở về Provider list; Provider cuối
setup xong mới tạo combo draft và sang Persona.

- [ ] **Step 1: Write failing Store and daemon tests**

  ```go
  func TestStageOnboardingSetupReturnsProviderUntilAllSelectedReady(t *testing.T) {
      first := stageConnectedKind(t, st, "codex", "acct-codex", "gpt-5.6-terra")
      require.Equal(t, OnboardingPhaseProvider, first.State.Phase)
      require.Equal(t, "ready", stage(first, "codex").Status)
      require.Equal(t, "pending", stage(first, "claude-code").Status)
      require.Empty(t, first.State.StagedComboID)

      last := stageConnectedKind(t, st, "claude-code", "acct-claude", "claude-opus-4-1")
      require.Equal(t, OnboardingPhasePersona, last.State.Phase)
      require.NotEmpty(t, last.State.StagedComboID)
  }
  ```

  Cover exact active kind/revision/account, Connect bind only pending row, deterministic positions,
  both setup successor idempotence cases, no-model rollback, concurrent winner and post-connect drift.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(BindOnboardingAccount|StageOnboardingSetup).*Multi'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(ConnectOnboarding|AppOnboardingSetup).*Multi'
  ```

- [ ] **Step 3: Implement row-aware bind/setup and standard snapshot response**

  Setup request becomes exact:

  ```json
  {"revision":7,"kind":"codex","account_id":"acct-codex"}
  ```

  The response is the normal status projection, including `phase`, `providers`, `provider_options`,
  active IDs and lifecycle fields. On non-final success clear singleton active fields and return
  `provider`; on final success create one UUID, clear active fields and return `persona`.

- [ ] **Step 4: Run GREEN and connect/setup regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(BindOnboardingAccount|StageOnboardingSetup)'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(ConnectOnboarding|AppOnboardingSetup)'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_onboarding_providers.go appmode/overlay/internal/store/app_onboarding.go appmode/overlay/internal/store/app_onboarding_test.go appmode/overlay/internal/daemon/app_llm_connect.go appmode/overlay/internal/daemon/app_llm_connect_test.go appmode/overlay/internal/daemon/app_onboarding.go appmode/overlay/internal/daemon/app_onboarding_test.go
  git commit -m "feat: stage onboarding providers sequentially"
  ```

### Task 5: Back authoritative và bảo vệ mọi stage reference

**Files:**
- Modify: `appmode/overlay/internal/store/app_onboarding_providers.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/store/app_llm.go`
- Modify: `appmode/overlay/internal/store/app_llm_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`

**Public behavior to verify:** Back từ Persona/Test thực sự quay về Provider bằng server CAS; mutation
Provider/Account/model không thể làm stage row trở thành dangling reference.

- [ ] **Step 1: Write failing behavior tests**

  ```go
  func TestBackOnboardingToProvidersKeepsReadyStagesAndPersona(t *testing.T) {
      got, err := st.BackOnboardingToProviders(revision)
      require.NoError(t, err)
      require.Equal(t, OnboardingPhaseProvider, got.State.Phase)
      require.Empty(t, got.State.StagedComboID)
      require.NotEmpty(t, got.State.PersonaFingerprint)
      require.Len(t, got.Stages, 2)
  }
  ```

  Add Store public-mutation tests proving delete/replace of a staged provider, account or model returns a
  stable conflict and leaves the full database digest unchanged. Add HTTP tests for auth, persona/test
  only, test-run cancel-and-wait, stale request no cancellation, and exact status response.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(BackOnboardingToProviders|LLMMutationRejectsOnboardingStageReference)'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppOnboardingBackToProviders'
  ```

- [ ] **Step 3: Implement Store guard and route**

  ```go
  func (s *Store) BackOnboardingToProviders(expectedRevision int64) (OnboardingSnapshot, error)
  ```

  Add `POST /onboarding/back-to-providers` with `{revision}`. Use the existing Test Chat transition fence;
  wait without holding the mutation mutex, relock/revalidate, then mutate. Keep ready rows and persona
  fingerprint; clear draft combo and receipt.

- [ ] **Step 4: Run GREEN**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(BackOnboardingToProviders|LLMMutationRejectsOnboardingStageReference)'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppOnboardingBackToProviders'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_onboarding_providers.go appmode/overlay/internal/store/app_onboarding_test.go appmode/overlay/internal/store/app_llm.go appmode/overlay/internal/store/app_llm_test.go appmode/overlay/internal/daemon/app_onboarding.go appmode/overlay/internal/daemon/app_onboarding_test.go appmode/overlay/internal/daemon/app_routes.go
  git commit -m "fix: protect onboarding provider stages"
  ```

### Task 6: Test Chat chạy exact staged fallback nhiều member

**Files:**
- Create: `appmode/overlay/internal/daemon/app_onboarding_runner.go`
- Create: `appmode/overlay/internal/daemon/app_onboarding_runner_test.go`
- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router_test.go`

**Public behavior to verify:** Test Chat thử Codex rồi Claude theo position, pin đúng disabled account,
fallback khi member đầu lỗi và lưu receipt chỉ cho exact route/persona đã test.

- [ ] **Step 1: Write failing fallback and fingerprint tests**

  ```go
  func TestAppOnboardingTestChatFallsBackAcrossPinnedStages(t *testing.T) {
      runner := newAppOnboardingTestRunner([]appOnboardingReadyEntry{
          {Position: 0, Kind: "codex", AccountID: "acct-c", ModelID: "gpt-5.6-terra"},
          {Position: 1, Kind: "claude-code", AccountID: "acct-a", ModelID: "claude-opus-4-1"},
      })
      got := runWithFirstFailure(t, runner)
      require.Equal(t, "claude-code", got.ProviderID)
      require.Equal(t, 1, got.Position)
  }
  ```

  Cover first success/no second call, all fail, account/model/config drift before and after runner,
  route reorder fingerprint change, no live combo/session/Memory/attempt writes, busy/cancel/timeout and
  actual serving identity in response.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppOnboardingTestChat.*Multi|AppOnboardingReadyRunner|AppLLMRouter.*PinnedFallback)'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'TestOnboarding.*Fingerprint.*Stages'
  ```

- [ ] **Step 3: Implement ordered ready-route snapshot and runner**

  ```go
  type appOnboardingTestResult struct {
      Answer, ProviderID, ModelID string
      Position int
  }
  ```

  Frame every `position/kind/provider/account/model` with domain and uint64 lengths. Build one synthetic
  fallback route from the ready snapshot; carry pinned account per entry. Preserve Claude-last order.

- [ ] **Step 4: Run GREEN and full Test Chat slice**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppOnboardingTestChat|TestAppOnboardingReadyRunner|TestAppLLMRouter.*Pinned'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test.*Onboarding.*(Receipt|Fingerprint)'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_onboarding_runner.go appmode/overlay/internal/daemon/app_onboarding_runner_test.go appmode/overlay/internal/store/app_onboarding.go appmode/overlay/internal/store/app_onboarding_test.go appmode/overlay/internal/daemon/app_onboarding.go appmode/overlay/internal/daemon/app_onboarding_test.go appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_router_test.go
  git commit -m "feat: verify onboarding provider fallback"
  ```

### Task 7: Complete atomically thành Combo nhiều member

**Files:**
- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`

**Public behavior to verify:** Một valid receipt kích hoạt toàn bộ staged accounts và route N member
trong một commit; bất kỳ validation/failpoint/commit error nào giữ live route cũ nguyên vẹn.

- [ ] **Step 1: Write failing Complete tests**

  ```go
  func TestCompleteOnboardingActivatesOrderedProviderFallback(t *testing.T) {
      got, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
          Revision: revision, TestNonceHash: receiptHash,
          PersonaFingerprint: personaFingerprint, Now: now,
          ConfigBindings: []OnboardingConfigBinding{
              {Kind: "codex", AccountID: "acct-c", ConfigDir: codexDir},
              {Kind: "claude-code", AccountID: "acct-a", ConfigDir: claudeDir},
          },
      })
      require.NoError(t, err)
      require.Equal(t, OnboardingPhaseCompleted, got.Phase)
      require.Equal(t, []string{"codex", "claude-code"}, activeComboKinds(t, st))
      snapshot, err := st.OnboardingSnapshot()
      require.NoError(t, err)
      require.Empty(t, snapshot.Stages)
  }
  ```

  Cover exact binding set (missing/extra/duplicate/swapped), receipt one-time/expiry, every stage drift,
  enable all new accounts/providers, disable older same-provider accounts, N combo members, legacy route
  positions, route revision max+1/overflow, nine existing failpoint groups expanded to loop over both
  members, actual Commit failure and concurrent one-winner.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'TestCompleteOnboarding.*Multi'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppOnboardingComplete.*Multi'
  ```

- [ ] **Step 3: Implement one Store transaction**

  Revise the input without changing the return type:

  ```go
  type OnboardingConfigBinding struct {
      Kind, AccountID, ConfigDir string
  }

  type CompleteOnboardingInput struct {
      Revision int64
      TestNonceHash, PersonaFingerprint string
      ConfigBindings []OnboardingConfigBinding
      Now time.Time
  }

  func (s *Store) CompleteOnboarding(
      ctx context.Context, input CompleteOnboardingInput,
  ) (OnboardingState, error)
  ```

  Re-read all rows/accounts/models in Store, compare daemon config bindings as an exact set, insert combo
  members and legacy route entries by persisted position, complete state, clear receipt and delete stage
  rows before the same `Commit()`. Never accept route entries supplied by HTTP/daemon.

- [ ] **Step 4: Run GREEN and Complete regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'TestCompleteOnboarding'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppOnboardingComplete'
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_onboarding.go appmode/overlay/internal/store/app_onboarding_test.go appmode/overlay/internal/daemon/app_onboarding.go appmode/overlay/internal/daemon/app_onboarding_test.go
  git commit -m "feat: activate onboarding provider fallback"
  ```

### Task 8: Portal contract và pure reconciliation cho selected set

**Files:**
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding-contract.js`
- Create: `appmode/overlay/internal/webui/static/pages/onboarding-provider-flow.js`
- Modify: `appmode/overlay/internal/webui/static/app-main.js`
- Modify: `appmode/overlay/internal/webui/static/pages/settings.js`
- Modify: `appmode/tests/helpers/onboarding-fixtures.mjs`
- Create: `appmode/tests/onboarding-multi-provider.test.mjs`
- Modify: `appmode/tests/app-main.test.mjs`
- Modify: `appmode/tests/settings.test.mjs`

**Public behavior to verify:** Browser giữ và kiểm tra full catalog/stage snapshot, PUT cả selected set,
reconcile lost responses chính xác và không biến suggested provider thành ON giả.

- [ ] **Step 1: Write failing Node public-contract tests**

  ```js
  test("persists both selected providers in canonical catalog order", async () => {
    const result = await persistProviderSetWithReconciliation({
      state: status({ providers: [] }),
      selectedKinds: ["claude-code", "codex"],
      updateProviders: async () => status({
        revision: 2,
        providers: [pending("codex", 0), pending("claude-code", 1)],
      }),
    });
    assert.deepEqual(result.state.providers.map(({ kind }) => kind), ["codex", "claude-code"]);
  });
  ```

  Cover duplicate/unknown/malformed catalog, dirty pending/ready identities, noncontiguous positions,
  active-slot invariants, max safe revision, direct malformed 2xx no GET laundering, rejected request
  exact GET successor, stale mismatch, abort/dispose, setup phase provider/persona and Test actual member.

- [ ] **Step 2: Run RED**

  ```powershell
  node --test appmode/tests/onboarding-multi-provider.test.mjs appmode/tests/app-main.test.mjs appmode/tests/settings.test.mjs
  ```

- [ ] **Step 3: Implement strict normalizers, service and pure helper**

  ```js
  export function normalizeProviderStages(value, options) {
    if (!Array.isArray(value) || !Array.isArray(options)) throw new TypeError("invalid providers");
    const allowed = new Set(options.map(({ kind }) => kind));
    const stages = value.map((stage, position) => normalizeStage(stage, position, allowed));
    if (new Set(stages.map(({ kind }) => kind)).size !== stages.length) {
      throw new TypeError("invalid providers");
    }
    return Object.freeze(stages.map((stage) => Object.freeze({ ...stage })));
  }

  export async function persistProviderSetWithReconciliation(input) {
    try {
      return { kind: "updated", state: input.normalize(await input.updateProviders(
        input.state.revision, input.selectedKinds,
      )) };
    } catch (error) {
      if (input.isMalformedSuccess(error) || error?.name === "AbortError") throw error;
      const state = input.normalize(await input.loadStatus());
      if (isExactSelectionSuccessor(input.state, state, input.selectedKinds)) {
        return { kind: "updated", state };
      }
      return { kind: "moved", state };
    }
  }

  export async function beginProviderInstallWithReconciliation(input) {
    try {
      return { kind: "started", state: input.normalize(await input.beginProvider(
        input.state.revision, input.kind,
      )) };
    } catch (error) {
      if (input.isMalformedSuccess(error) || error?.name === "AbortError") throw error;
      const state = input.normalize(await input.loadStatus());
      return isExactBeginSuccessor(input.state, state, input.kind)
        ? { kind: "started", state }
        : { kind: "moved", state };
    }
  }
  ```

  `projectStatus` retains `providers` and `provider_options`; setup returns a normal status snapshot.
  Remove frontend static allowlist checks from app startup/settings and rely on normalized advertised
  catalog. Keep all values copied/frozen or treated immutably.

- [ ] **Step 4: Run GREEN and syntax/line gates**

  ```powershell
  node --test appmode/tests/onboarding-multi-provider.test.mjs appmode/tests/app-main.test.mjs appmode/tests/settings.test.mjs appmode/tests/onboarding-regressions.test.mjs
  node --check appmode/overlay/internal/webui/static/pages/onboarding-contract.js
  node --check appmode/overlay/internal/webui/static/pages/onboarding-provider-flow.js
  ```

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/onboarding-contract.js appmode/overlay/internal/webui/static/pages/onboarding-provider-flow.js appmode/overlay/internal/webui/static/app-main.js appmode/overlay/internal/webui/static/pages/settings.js appmode/tests/helpers/onboarding-fixtures.mjs appmode/tests/onboarding-multi-provider.test.mjs appmode/tests/app-main.test.mjs appmode/tests/settings.test.mjs appmode/tests/onboarding-regressions.test.mjs
  git commit -m "feat: normalize onboarding provider sets"
  ```

### Task 9: Portal render nhiều toggle và cài từng hàng

**Files:**
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding.js`
- Modify: `appmode/overlay/internal/webui/static/portal.css`
- Modify: `appmode/tests/onboarding-multi-provider.test.mjs`
- Modify: `appmode/tests/onboarding-provider-one-click.test.mjs`
- Modify: `appmode/tests/onboarding-early.test.mjs`
- Modify: `appmode/tests/onboarding.test.mjs`
- Modify: `appmode/tests/onboarding-recovery.test.mjs`
- Modify: `appmode/tests/onboarding-ags-setup.test.mjs`
- Modify: `appmode/tests/shell.test.mjs`

**Public behavior to verify:** Codex và Claude cùng ON; mỗi CTA cài đúng row; row kia vẫn ON/chờ;
provider đầu quay danh sách, provider cuối sang Persona; ready row khóa ON; refresh/back/cancel authoritative.

- [ ] **Step 1: Write failing public DOM/page tests**

  Trước khi thêm assertion mới, chuyển các case selection cũ cần sửa từ
  `onboarding-provider-one-click.test.mjs` và `onboarding-early.test.mjs` sang
  `onboarding-multi-provider.test.mjs`; chạy ba file một lần để chứng minh refactor vẫn GREEN và đưa
  hai file cũ xuống dưới 760 dòng. Không sửa `provider-connect.test.mjs` (1178 dòng) hoặc
  `providers.test.mjs` (849 dòng); chỉ chạy chúng làm regressions read-only.

  ```js
  test("keeps two providers on and installs only the clicked row", async () => {
    const page = await mountOnboarding({ initial: status({ providers: [] }) });
    click(page.toggle("codex"));
    await page.flush();
    click(page.toggle("claude-code"));
    await page.flush();
    assert.equal(page.toggle("codex").getAttribute("aria-pressed"), "true");
    assert.equal(page.toggle("claude-code").getAttribute("aria-pressed"), "true");

    click(page.install("claude-code"));
    await page.flush();
    assert.deepEqual(page.connectStarts, [{ kind: "claude-code", immediate: true }]);
  });
  ```

  Cover persist per toggle, OFF pending confirmation/focus/Escape, ready disabled copy, one-flight lock,
  other row `Đang chờ`, double click/two-tab, first/last Setup outcomes, exact priority labels, generic
  fallback icon, Connect retry/cancel, authoritative Persona Back, dispose/late response, reload and 390px
  no horizontal overflow. Keep Providers page prompt regression unchanged.

- [ ] **Step 2: Run RED**

  ```powershell
  node --test appmode/tests/onboarding-multi-provider.test.mjs appmode/tests/onboarding-provider-one-click.test.mjs appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs appmode/tests/onboarding.test.mjs appmode/tests/onboarding-recovery.test.mjs appmode/tests/provider-connect.test.mjs appmode/tests/providers.test.mjs appmode/tests/shell.test.mjs
  ```

- [ ] **Step 3: Implement generic row view and page orchestration**

  Derive selection only from `state.providers`; `suggested_provider_kind` is a badge. Toggle calls
  `PUT /onboarding/providers`; CTA calls begin endpoint then existing Provider Connect with
  `{label:"Onboarding", onboardingRevision, immediate:true}`. While active, show all selected rows and
  lock mutations. Route Setup snapshot by its real phase. Persona Back calls server and never assigns a
  fake local provider phase.

- [ ] **Step 4: Run GREEN, full Portal suite and static gates**

  ```powershell
  node --test appmode/tests/onboarding-multi-provider.test.mjs appmode/tests/onboarding-provider-one-click.test.mjs appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs appmode/tests/onboarding.test.mjs appmode/tests/onboarding-recovery.test.mjs appmode/tests/onboarding-regressions.test.mjs appmode/tests/provider-connect.test.mjs appmode/tests/providers.test.mjs appmode/tests/app-main.test.mjs appmode/tests/settings.test.mjs appmode/tests/shell.test.mjs
  npm test --prefix appmode
  node --check appmode/overlay/internal/webui/static/pages/onboarding-early-view.js
  node --check appmode/overlay/internal/webui/static/pages/onboarding.js
  ```

  Assert every new or modified JS file is at most 800 lines and `git diff --check` is clean. Hai file
  regression >800 không được sửa nên không nằm trong gate touched-file.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/onboarding-early-view.js appmode/overlay/internal/webui/static/pages/onboarding.js appmode/overlay/internal/webui/static/portal.css appmode/tests
  git commit -m "feat: install multiple onboarding providers"
  ```

## Final verification gates

After Task 9, run fresh from clean HEAD:

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all
npm test --prefix appmode
pwsh -NoProfile -File .\appmode\test-static.ps1
pwsh -NoProfile -File .\appmode\build.ps1 -OutputDir D:\TuvanZalo\_artifacts\tuvanzalo-multi-provider-preview
git diff --check
git status --short --branch
```

Launch only the packaged artifact with its own data directory. Verify with desktop and true 390x844 CDP
captures:

1. both Codex and Claude ON/pending;
2. one ready and the other ON/pending;
3. active Connect with the other row visible as waiting;
4. no sidebar, modal fully visible on grid, no intermediate connect button.

Run a final spec-compliance reviewer and then code-quality reviewer. Fix findings with RED→GREEN tests,
rerun all gates, commit fixes separately, then push `integration/main-memory-v2`.
