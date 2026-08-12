# Onboarding Wizard bắt buộc với staged activation — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` or `:executing-plans` to run
> this plan. Steps use `- [ ]` checkboxes.

**Goal:** Thêm wizard lần đầu bắt buộc cho Codex/Claude Code, chứng minh Provider, model và Persona
hoạt động bằng một lượt chat thật trước khi thay cấu hình live, đồng thời giữ nguyên giao diện Portal
cũ và cho phép resume an toàn sau refresh/restart.

**TDD mode:** yes — confirmed by the user on 2026-08-11.

**Architecture:** SQLite V7 giữ một state machine onboarding singleton độc lập với schema version.
Connect tạo Account staging disabled; Setup chỉ ghi descriptor nháp; Test Chat chạy một route snapshot
cô lập và pin đúng Account staging; Complete mới bật Account đã test, disable Account cũ cùng
Provider, thay toàn bộ Combo trong một transaction và đánh dấu onboarding version. Frontend tải
status trước Portal router, dùng lại Connect và Persona component đã tách, rồi handoff sang Portal
sau Complete.

**Tech stack:** Go daemon overlay, SQLite, vanilla JavaScript ES modules, DOM API, Node built-in test
runner, PowerShell/Pester build gates và local Codex/Claude CLI adapters.

**Spec:** `.planning/specs/2026-08-11-onboarding-wizard-design.md`

**Research:** skipped — không có research artifact cho onboarding; đây là phần mở rộng cục bộ của
Connect, Agent, Combo, router và Portal hiện có đã được map trực tiếp từ codebase.

---

## Execution rules

- Work only in `D:\TuvanZalo\_build\.worktrees\portal-m1` on
  `integration/main-memory-v2`.
- The worktree is already isolated. Do not create a second worktree, modify `D:\TuvanZalo\data`,
  start the live daemon, or overwrite the packaged executable/static assets.
- Preserve user changes and keep each task in its own commit. Do not push, merge to `main`, create a
  PR, or deploy until the user explicitly asks after implementation review.
- For every behavior change: add the focused test, run it and record a meaningful RED, implement the
  minimum contract, run GREEN, then run the named regression set before committing.
- A compile error caused only by a missing production symbol is acceptable for the first RED. Syntax
  errors, fixture corruption and unrelated failures are not acceptable RED evidence.
- Never put credential bytes, CLI config paths, test messages, prompt text or answers into onboarding
  state, JSON logs, live LLM telemetry or persisted chat/Memory tables.
- Every mutation uses the existing Portal mutation header and an optimistic `revision`. A stale
  revision must return conflict without a partial write.
- Resolve Account cleanup paths with `filepath.Abs`, `filepath.Rel` and exact IDs. Reject `.` paths,
  parent traversal and any target outside `<dataDir>/accounts/<kind>/<accountId>`; never use a glob.
- UI work must preserve existing Portal layout and scope new selectors under `[data-onboarding]`,
  `.onboarding-shell` or `.settings-page`.

## File responsibility map

- `appmode/overlay/internal/store/app_schema.go` — V7 schema and singleton seed only.
- `appmode/overlay/internal/store/app_onboarding.go` — onboarding model, CAS transitions and Complete
  transaction; no filesystem or HTTP work.
- `appmode/overlay/internal/store/app_onboarding_test.go` — real SQLite state, migration and rollback
  contracts.
- `appmode/overlay/internal/daemon/app_onboarding.go` — HTTP handlers, cleanup policy, model selection,
  receipt lifecycle and isolated test execution.
- `appmode/overlay/internal/daemon/app_onboarding_test.go` — handler and no-side-effect tests.
- `appmode/overlay/internal/daemon/app_llm_connect.go` — one shared Connect manager with optional
  onboarding context; terminal IDs remain non-secret.
- `appmode/overlay/internal/daemon/app_llm_cli.go` — explicit onboarding model per CLI descriptor.
- `appmode/overlay/internal/daemon/app_agent.go` and `app_persona.go` — complete Persona validation,
  display-name metadata and receipt invalidation.
- `appmode/overlay/internal/daemon/app_zalo_session_prompt.go` and `app_zalo_session_hook.go` — display
  name in production identity prompt and prompt/session fingerprint.
- `appmode/overlay/internal/webui/static/components/provider-connect.js` — shared Connect lifecycle,
  timer, polling, cancel and panel rendering.
- `appmode/overlay/internal/webui/static/components/persona-fields.js` — shared field labels,
  validation, rendering and first-error focus.
- `appmode/overlay/internal/webui/static/pages/onboarding.js` — wizard controller only.
- `appmode/overlay/internal/webui/static/pages/settings.js` — minimal real Settings page with restart
  entry.
- `appmode/overlay/internal/webui/static/app-main.js` — fail-closed bootstrap gate and Portal handoff.

## Test command conventions

Run commands from the worktree root. The Go helper stages the overlay against the clean upstream
repository before testing:

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run '<regex>'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '<regex>'
node --test appmode/tests/<file>.test.mjs
```

Expected RED means the named new test fails for the intended missing behavior. Expected GREEN means
the command exits `0` with no skipped newly-added case.

### Task 1: Add schema V7 and a fail-closed onboarding status reader

**Files:**

- Modify: `appmode/overlay/internal/store/app_schema.go:11-295`
- Modify: `appmode/overlay/internal/store/app_schema_test.go:620-1528`
- Create: `appmode/overlay/internal/store/app_onboarding.go`
- Create: `appmode/overlay/internal/store/app_onboarding_test.go`

**Public behavior to verify:** Fresh and V1–V6 databases migrate to V7, preserve all existing rows,
seed exactly one onboarding row with `completed_version=0`, and never treat a missing singleton row
as completed.

- [ ] **Step 1: Write the failing migration and status tests**

  Add table-driven tests with these exact cases: fresh database, V6 database with Provider/Account/
  Combo/session/Memory fixtures, singleton cardinality, unknown phase rejection and a deliberately
  deleted singleton. The central assertions should be:

  ```go
  func TestMigrateAppV7SeedsRequiredOnboarding(t *testing.T) {
      db := openMigratedAppTestDB(t)
      if got := appSchemaVersionForTest(t, db); got != "7" {
          t.Fatalf("schema_version = %q; want 7", got)
      }
      st := &Store{db: db}
      got, err := st.OnboardingState()
      if err != nil {
          t.Fatal(err)
      }
      if got.CompletedVersion != 0 || got.Phase != OnboardingPhaseProvider || got.Revision != 1 {
          t.Fatalf("initial onboarding state = %+v", got)
      }
  }

  func TestOnboardingStateMissingRowFailsClosed(t *testing.T) {
      st := openAppStoreForTest(t)
      if _, err := st.db.Exec(`DELETE FROM app_onboarding_state WHERE id = 1`); err != nil {
          t.Fatal(err)
      }
      if _, err := st.OnboardingState(); !errors.Is(err, ErrOnboardingStateMissing) {
          t.Fatalf("error = %v; want ErrOnboardingStateMissing", err)
      }
  }
  ```

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(MigrateAppV7|OnboardingState)'
  ```

  Expected: compile failure for the missing onboarding types/method, or migration expectation `7`
  fails while the current implementation remains V6.

- [ ] **Step 3: Implement the minimum V7 schema and reader**

  Set `appSchemaVersion` to `7`, add the table to the migration transaction, seed with
  `INSERT OR IGNORE`, and define the state contract:

  ```go
  const CurrentOnboardingVersion int64 = 1

  const (
      OnboardingPhaseProvider  = "provider"
      OnboardingPhaseConnect   = "connect"
      OnboardingPhaseSetup     = "setup"
      OnboardingPhasePersona   = "persona"
      OnboardingPhaseTest      = "test"
      OnboardingPhaseCompleted = "completed"
  )

  var ErrOnboardingStateMissing = errors.New("onboarding state is missing")
  var ErrOnboardingConflict = errors.New("onboarding revision conflict")

  type OnboardingState struct {
      CompletedVersion   int64
      Phase              string
      ProviderKind       string
      ProviderID         string
      AccountID          string
      ModelID            string
      StagedComboID      string
      PersonaFingerprint string
      TestNonceHash      string
      TestExpiresAt      string
      RestartInProgress  bool
      Revision           int64
      UpdatedAt          string
  }

  func (s *Store) OnboardingState() (OnboardingState, error) {
      var state OnboardingState
      var restart int
      err := s.db.QueryRow(`SELECT completed_version, phase, provider_kind, provider_id,
          account_id, model_id, staged_combo_id, persona_fingerprint, test_nonce_hash,
          test_expires_at, restart_in_progress, revision, updated_at
          FROM app_onboarding_state WHERE id = 1`).Scan(
          &state.CompletedVersion, &state.Phase, &state.ProviderKind, &state.ProviderID,
          &state.AccountID, &state.ModelID, &state.StagedComboID, &state.PersonaFingerprint,
          &state.TestNonceHash, &state.TestExpiresAt, &restart, &state.Revision, &state.UpdatedAt)
      if errors.Is(err, sql.ErrNoRows) {
          return OnboardingState{}, ErrOnboardingStateMissing
      }
      if err != nil {
          return OnboardingState{}, fmt.Errorf("read onboarding state: %w", err)
      }
      state.RestartInProgress = restart == 1
      return state, nil
  }
  ```

  The SQL table must use `CHECK (id = 1)`, `CHECK (restart_in_progress IN (0,1))`, and a phase CHECK
  listing all six constants. Do not add credential/config-dir/message/answer columns.

- [ ] **Step 4: Run GREEN and migration regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(MigrateApp|OnboardingState|AppSchema)'
  ```

  Update post-migration version assertions from `"6"` to `"7"`; keep source fixtures that
  intentionally declare V1–V6 unchanged. In the schema-version validation table, make `7` the
  current version and `8` the future-version rejection; likewise move any fixture whose sole purpose
  is simulating a future schema from `7` to `8`. Expected: exit `0`, with existing data-preservation
  tests still green.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_schema.go `
    appmode/overlay/internal/store/app_schema_test.go `
    appmode/overlay/internal/store/app_onboarding.go `
    appmode/overlay/internal/store/app_onboarding_test.go
  git diff --cached --check
  git commit -m "feat: persist onboarding state"
  ```

### Task 2: Add optimistic provider/restart transitions and exact staging ownership

**Files:**

- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`

**Public behavior to verify:** Store can first inspect the exact disabled staging Account owned by an
expected revision without mutating state. After the daemon safely removes that directory, selecting a
supported Provider atomically deletes that exact Account row, moves any required pre-completion
phase to `connect`, increments revision and clears staging/receipt fields. Restart is allowed only after completion, preserves
`completed_version` and marks `restart_in_progress=1`. Stale revisions do nothing.

- [ ] **Step 1: Write failing state-transition tests**

  ```go
  func TestSelectOnboardingProviderUsesCASAndReturnsOwnedStaging(t *testing.T) {
      st := openAppStoreForTest(t)
      seedOwnedStagingAccount(t, st, "old-account", "claude-code")
      old := forceOnboardingState(t, st, OnboardingState{
          Phase: OnboardingPhaseProvider, ProviderKind: "claude-code",
          AccountID: "old-account", Revision: 4,
      })
      cleanup, err := st.OnboardingStagingAccount(old.Revision)
      if err != nil {
          t.Fatal(err)
      }
      got, err := st.SelectOnboardingProvider(old.Revision, "codex", cleanup.AccountID)
      if err != nil {
          t.Fatal(err)
      }
      if cleanup.AccountID != "old-account" || got.ProviderKind != "codex" ||
          got.Phase != OnboardingPhaseConnect || got.Revision != 5 {
          t.Fatalf("state=%+v cleanup=%+v", got, cleanup)
      }
      if _, err := st.SelectOnboardingProvider(4, "claude-code", "");
          !errors.Is(err, ErrOnboardingConflict) {
          t.Fatalf("stale error = %v", err)
      }
  }
  ```

  Add separate cases proving only `codex` and `claude-code` are accepted; the staging Account must be
  disabled and match the stored Account ID before cleanup ownership is returned; an enabled/live
  Account is never returned for deletion; selection is allowed from `provider`, `connect`, `setup`,
  `persona` and `test` but not `completed`; restart before completion is rejected; retry of the same
  accepted transition does not silently apply twice.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(SelectOnboardingProvider|RestartOnboarding)'
  ```

  Expected: compile failure for the transition methods.

- [ ] **Step 3: Implement CAS transitions inside Store transactions**

  Use a non-secret cleanup value and an explicit supported-kind validator:

  ```go
  type OnboardingStagingAccount struct {
      AccountID    string
      ProviderID   string
      ProviderKind string
      ConfigDir    string
  }

  func IsOnboardingProviderKind(kind string) bool {
      return kind == "codex" || kind == "claude-code"
  }

  func (s *Store) SelectOnboardingProvider(
      expectedRevision int64,
      kind string,
      cleanedAccountID string,
  ) (OnboardingState, error) {
      if !IsOnboardingProviderKind(kind) {
          return OnboardingState{}, ErrOnboardingProviderUnsupported
      }
      return s.transitionOnboardingProvider(expectedRevision, kind, cleanedAccountID, false)
  }

  func (s *Store) RestartOnboarding(
      expectedRevision int64,
      cleanedAccountID string,
  ) (OnboardingState, error) {
      return s.transitionOnboardingProvider(expectedRevision, "", cleanedAccountID, true)
  }
  ```

  `OnboardingStagingAccount(expectedRevision)` must fail on stale revision and return a descriptor
  only when the stored Account exists, is disabled and belongs to the selected Provider. The
  transition transaction must re-read that ownership, require `cleanedAccountID` to match it, delete
  only that Account row, update with `WHERE id=1 AND revision=?`, clear Provider/Account/model/combo/
  fingerprint/receipt fields, and return `ErrOnboardingConflict` when `RowsAffected()!=1`. Store does
  not remove directories; the daemon does that between inspection and transition while holding the
  shared onboarding mutation lock.

- [ ] **Step 4: Run GREEN**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(SelectOnboardingProvider|RestartOnboarding|OnboardingState)'
  ```

  Expected: exit `0`; stale transitions leave the row and Account unchanged.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_onboarding.go `
    appmode/overlay/internal/store/app_onboarding_test.go
  git diff --cached --check
  git commit -m "feat: add onboarding state transitions"
  ```

### Task 3: Expose status, Provider selection and restart with safe cleanup

**Files:**

- Create: `appmode/overlay/internal/daemon/app_onboarding.go`
- Create: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go:5-127`
- Modify: `appmode/overlay/internal/daemon/app_foundation_test.go`

**Public behavior to verify:** The browser can read a non-secret resumable status and perform
revision-guarded Provider selection/restart. Cleanup removes only the disabled Account staging owned
by the singleton and its exact validated directory; path escape or filesystem failure leaves the
Account row intact and returns an error.

- [ ] **Step 1: Write failing route and cleanup tests**

  ```go
  func TestAppOnboardingStatusRequiresWizardForV1(t *testing.T) {
      env := newAppRouteTestEnv(t)
      rr := env.serve("GET", "/onboarding/status", nil)
      if rr.Code != http.StatusOK {
          t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
      }
      var got struct {
          Required bool  `json:"required"`
          Version  int64 `json:"current_version"`
          Revision int64 `json:"revision"`
      }
      decodeAppRouteJSON(t, rr, &got)
      if !got.Required || got.Version != 1 || got.Revision != 1 {
          t.Fatalf("status = %+v", got)
      }
  }

  func TestOnboardingCleanupRejectsPathOutsideAccountsRoot(t *testing.T) {
      root := t.TempDir()
      target := filepath.Join(root, "..", "do-not-delete")
      err := removeOwnedOnboardingAccount(root, store.OnboardingStagingAccount{
          AccountID: "owned", ProviderKind: "codex", ConfigDir: target,
      })
      if !errors.Is(err, errOnboardingUnsafeAccountPath) {
          t.Fatalf("error = %v", err)
      }
  }
  ```

  Add route-table cases for all new methods, cookie auth, mutation header, malformed JSON, body-size
  limit, unsupported kind, stale revision, unconfirmed restart, cleanup of one exact safe directory,
  enabled Account preservation, active Connect cancellation before a Provider change, late Connect
  success not reviving the old flow, supported active-route suggestion and no secret/config-dir keys
  in status JSON.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppOnboardingStatus|OnboardingCleanup|AppPortalRoutes)'
  ```

  Expected: `/onboarding/status` is not registered and cleanup symbols are absent.

- [ ] **Step 3: Implement the three endpoints and cleanup boundary**

  Register exactly:

  ```go
  "GET /onboarding/status"
  "PUT /onboarding/provider"
  "POST /onboarding/restart"
  ```

  Use stable JSON request/response types:

  ```go
  type appOnboardingRevisionRequest struct {
      Revision int64 `json:"revision"`
  }

  type appOnboardingProviderRequest struct {
      Revision int64  `json:"revision"`
      Kind     string `json:"kind"`
  }

  type appOnboardingRestartRequest struct {
      Revision  int64 `json:"revision"`
      Confirmed bool  `json:"confirmed"`
  }

  type appOnboardingStatusResponse struct {
      Required              bool   `json:"required"`
      CurrentVersion        int64  `json:"current_version"`
      CompletedVersion      int64  `json:"completed_version"`
      Phase                 string `json:"phase"`
      ProviderKind          string `json:"provider_kind"`
      SuggestedProviderKind string `json:"suggested_provider_kind"`
      ProviderID            string `json:"provider_id"`
      AccountID             string `json:"account_id"`
      ModelID               string `json:"model_id"`
      RestartInProgress     bool   `json:"restart_in_progress"`
      Revision              int64  `json:"revision"`
  }
  ```

  Compute `Required` as `CompletedVersion < CurrentOnboardingVersion || RestartInProgress`. When no
  Provider is selected, derive `SuggestedProviderKind` from the active route only if it is `codex` or
  `claude-code`. For Provider change/restart, hold the shared onboarding mutation lock, authoritatively
  cancel and await any active Connect job for the old kind, inspect the owned staging descriptor at
  the expected revision, validate and remove its exact directory, then call the Store transition that
  deletes the matching Account row. If cancel or directory validation/removal fails, return a
  retryable error and preserve state plus Account row. A missing already-cleaned directory is safe for
  retry. Never serialize `ConfigDir`, receipt hash or expiry.

- [ ] **Step 4: Run GREEN and route regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppOnboardingStatus|OnboardingCleanup|AppPortalRoutes|AppMutation)'
  ```

  Expected: exit `0`; every mutation rejects missing Portal header and stale revision.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_onboarding.go `
    appmode/overlay/internal/daemon/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_routes.go `
    appmode/overlay/internal/daemon/app_foundation_test.go
  git diff --cached --check
  git commit -m "feat: expose onboarding bootstrap api"
  ```

### Task 4: Reuse Connect while persisting an Account as staging-disabled

**Files:**

- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_connect.go:39-980`
- Modify: `appmode/overlay/internal/daemon/app_llm_connect_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`

**Public behavior to verify:** Normal Providers Connect remains unchanged and creates an enabled
Account. Connect with `onboarding_revision` is accepted only in matching `connect` state, creates a
disabled Account, atomically binds it to the singleton, and exposes `providerId/accountId` only after
persistence. Cancel/error never exposes IDs or config paths.

- [ ] **Step 1: Write failing Connect contract tests**

  ```go
  func TestConnectOnboardingPersistsDisabledAccountBeforePublishingIDs(t *testing.T) {
      env := newConnectTestEnv(t)
      state := env.selectProvider("codex")
      env.connectSuccess("codex", map[string]any{"onboarding_revision": state.Revision})
      got := env.waitForConnectPhase("codex", connectConnected)
      if got.ProviderID == "" || got.AccountID == "" {
          t.Fatalf("terminal state lacks ids: %+v", got)
      }
      account := env.account(got.ProviderID, got.AccountID)
      if account.Enabled {
          t.Fatal("onboarding account must remain disabled")
      }
      onboarding := env.onboardingState()
      if onboarding.AccountID != got.AccountID || onboarding.Phase != store.OnboardingPhaseSetup {
          t.Fatalf("onboarding state = %+v", onboarding)
      }
  }
  ```

  Add cases for normal Connect enabled=true, wrong kind, wrong/stale revision, state changing while
  login is running, bind failure cleanup, late success after authoritative cancel, and JSON scans
  proving `configDir`, `credential` and vendor tokens are absent.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestConnect(Onboarding|Normal|Cancel)'
  ```

  Expected: the start body ignores onboarding revision and successful Account is enabled.

- [ ] **Step 3: Add an optional onboarding context to the existing manager**

  Extend only the shared start/job contract:

  ```go
  type connectStartRequest struct {
      Label              string `json:"label"`
      OnboardingRevision int64  `json:"onboarding_revision,omitempty"`
  }

  type connectState struct {
      Kind       string `json:"kind"`
      Phase      string `json:"phase"`
      Message    string `json:"message,omitempty"`
      LoginURL   string `json:"loginUrl,omitempty"`
      Code       string `json:"code,omitempty"`
      Error      string `json:"error,omitempty"`
      ProviderID string `json:"providerId,omitempty"`
      AccountID  string `json:"accountId,omitempty"`
  }

  type connectOnboardingContext struct {
      Revision int64
      Kind     string
  }
  ```

  Validate the context before starting, persist Account with `Enabled:false`, then call a Store CAS
  bind that checks revision/kind/phase and advances to `setup`. Publish terminal IDs only after both
  persistence steps succeed. Keep the current normal path with `Enabled:true`. On any bind failure,
  invoke the same managed cleanup path used by cancel and do not publish IDs.

- [ ] **Step 4: Run GREEN and the whole Connect suite**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(Connect|AppLLMConnect)'
  ```

  Expected: exit `0`; old Connect tests pass without modifying their request bodies.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_onboarding.go `
    appmode/overlay/internal/store/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_llm_connect.go `
    appmode/overlay/internal/daemon/app_llm_connect_test.go `
    appmode/overlay/internal/daemon/app_onboarding_test.go
  git diff --cached --check
  git commit -m "feat: stage onboarding provider connection"
  ```

### Task 5: Stage the deterministic Auto-combo without touching live routing

**Files:**

- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_cli.go:21-110`
- Modify: `appmode/overlay/internal/daemon/app_llm_cli_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Modify: `appmode/overlay/internal/daemon/app_foundation_test.go`

**Public behavior to verify:** `POST /onboarding/setup` selects Codex `gpt-5.6-terra` or Claude
`sonnet` when available, otherwise the first available descriptor seed. It stages IDs only, advances
to Persona, is idempotent for the same revision/account, and leaves all live Combo/member/route rows
byte-for-byte unchanged.

- [ ] **Step 1: Write failing model-policy and setup tests**

  ```go
  func TestAppOnboardingSetupStagesComboWithoutChangingLiveRoute(t *testing.T) {
      env := newOnboardingTestEnv(t)
      before := env.liveRoutingDigest()
      state := env.connectedState("codex", "acct-stage")
      rr := env.serveJSON("POST", "/onboarding/setup", map[string]any{
          "revision": state.Revision, "account_id": "acct-stage",
      })
      if rr.Code != http.StatusOK {
          t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
      }
      got := env.onboardingState()
      if got.ModelID != "gpt-5.6-terra" || got.StagedComboID == "" ||
          got.Phase != store.OnboardingPhasePersona {
          t.Fatalf("state = %+v", got)
      }
      if after := env.liveRoutingDigest(); after != before {
          t.Fatalf("live route changed: before=%s after=%s", before, after)
      }
  }
  ```

  Add Claude `sonnet`, preferred unavailable fallback, no available model, wrong Account, enabled
  live Account rejection, stale revision and identical retry cases. For idempotence, retry with the
  original request after a simulated lost response and assert the same staged combo/model/revision.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppOnboardingSetup|OnboardingModel)'
  ```

  Expected: missing route/handler and missing descriptor field.

- [ ] **Step 3: Implement explicit model policy and Store staging**

  ```go
  type cliDescriptor struct {
      kind            string
      providerName    string
      onboardingModel string
      modelSeeds      []store.LLMModel
  }

  type appOnboardingSetupRequest struct {
      Revision  int64  `json:"revision"`
      AccountID string `json:"account_id"`
  }

  type OnboardingSetup struct {
      Revision      int64
      ProviderKind  string
      ProviderID    string
      AccountID     string
      ModelID       string
      StagedComboID string
      ComboName     string
  }
  ```

  Set Codex `onboardingModel: "gpt-5.6-terra"` and Claude
  `onboardingModel: "sonnet"`. Select only available models belonging to the staged Provider. Store
  `staged_combo_id` as a UUID plus the selected model/account and move `setup → persona`; do not write
  `llm_combos`, `llm_combo_members` or legacy route projection. Retry must read and return the staged
  result when provider/account match, without increasing revision.

- [ ] **Step 4: Run GREEN and route preservation regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppOnboardingSetup|OnboardingModel|AppPortalRoutes)'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(StageOnboardingSetup|LLMRoute|LLMCombo)'
  ```

  Expected: both commands exit `0`; setup retry returns the same staged IDs and live routing digest.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_onboarding.go `
    appmode/overlay/internal/store/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_llm_cli.go `
    appmode/overlay/internal/daemon/app_llm_cli_test.go `
    appmode/overlay/internal/daemon/app_onboarding.go `
    appmode/overlay/internal/daemon/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_routes.go `
    appmode/overlay/internal/daemon/app_foundation_test.go
  git diff --cached --check
  git commit -m "feat: stage onboarding auto combo"
  ```

### Task 6: Make Persona completeness and display name authoritative

**Files:**

- Create: `appmode/overlay/internal/store/app_agent_meta.go`
- Create: `appmode/overlay/internal/store/app_agent_meta_test.go`
- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_agent.go:1-190`
- Modify: `appmode/overlay/internal/daemon/app_agent_test.go`
- Modify: `appmode/overlay/internal/daemon/app_persona.go:53-118`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`

**Public behavior to verify:** `GET /agent` returns `display_name` and detects every closed mustache
hole. `PUT /agent` accepts `display_name`, `require_complete` and `onboarding_revision`; complete mode
validates the fully-rendered document in memory, rejects any remaining `{{...}}` with 422 without
writing, supports legacy-ready persona with empty `values`, preserves the one-time backup, and moves
Persona to Test only when file plus display name are ready. A complete-mode success returns the new
`onboarding_phase` and `onboarding_revision` so the wizard never guesses the CAS revision.

- [ ] **Step 1: Write failing broad-scan, atomicity and legacy tests**

  ```go
  func TestAgentPutRequireCompleteRejectsBroadMustacheWithoutWriting(t *testing.T) {
      env := newAgentTestEnv(t, "Tên {{ten_bot}}; lạ {{ key-with-dash }}")
      before := env.readPersona()
      rr := env.putAgent(map[string]any{
          "display_name": "Bé Mi",
          "values": map[string]string{"ten_bot": "Bé Mi"},
          "require_complete": true,
          "onboarding_revision": env.personaRevision(),
      })
      if rr.Code != http.StatusUnprocessableEntity {
          t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
      }
      if got := env.readPersona(); got != before {
          t.Fatalf("persona changed: %q", got)
      }
  }

  func TestAgentPutLegacyReadyPersonaOnlyStoresDisplayName(t *testing.T) {
      env := newAgentTestEnv(t, "Bạn là trợ lý tư vấn nội thất.")
      state := env.stagePersonaPhase()
      rr := env.putAgent(map[string]any{
          "display_name": "Bé Mi", "values": map[string]string{},
          "require_complete": true, "onboarding_revision": state.Revision,
      })
      if rr.Code != http.StatusOK || env.readPersona() != "Bạn là trợ lý tư vấn nội thất." {
          t.Fatalf("status=%d persona=%q", rr.Code, env.readPersona())
      }
      if got, _ := env.store.AgentDisplayName(); got != "Bé Mi" {
          t.Fatalf("display name = %q", got)
      }
  }
  ```

  Add exact cases for lowercase/Unicode/space/dash mustaches, unclosed `{{` reported as validation
  error, empty/newline/mustache delimiters/more than 60 Unicode code points in values or display
  name, `TEN_BOT` copying to display name, a conflicting explicit display name rejected, stale
  revision, backup not overwritten, atomic file-write error, and successful fingerprint/phase/
  revision update. Add a full-persona PUT case proving any edit invalidates a stored test receipt.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAgent(PutRequireComplete|PutLegacy|GetDisplay|BroadMustache|PersonaReceipt)'
  ```

  Expected: lowercase/unknown holes are missed and the current request type rejects empty values.

- [ ] **Step 3: Implement shared validation, typed metadata and serialized advance**

  Use a broad scanner that does not limit keys to uppercase:

  ```go
  var appAgentMustachePattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

  type appAgentPutRequest struct {
      Values             map[string]string `json:"values"`
      DisplayName        string            `json:"display_name"`
      RequireComplete    bool              `json:"require_complete"`
      OnboardingRevision int64             `json:"onboarding_revision"`
  }

  type appAgentPutResponse struct {
      Ready              bool   `json:"ready"`
      DisplayName        string `json:"display_name"`
      OnboardingPhase    string `json:"onboarding_phase,omitempty"`
      OnboardingRevision int64  `json:"onboarding_revision,omitempty"`
  }

  func validateAppAgentValue(value string) error {
      value = strings.TrimSpace(value)
      if value == "" || strings.ContainsAny(value, "\r\n") ||
          strings.Contains(value, "{{") || strings.Contains(value, "}}") ||
          utf8.RuneCountInString(value) > 60 {
          return errAppAgentValueInvalid
      }
      return nil
  }
  ```

  Add `AgentDisplayName() (string,error)` and `SetAgentDisplayName(string) error` over the exact
  `app_meta` key `agent_display_name`. For onboarding complete mode, hold an API-level onboarding
  mutation lock, re-read revision/phase before file I/O, render and scan in memory, atomically replace
  the persona, then call one Store transaction that upserts display name and CASes phase
  `persona → test` with a SHA-256 fingerprint of the final persona bytes plus display name. If that
  Store transaction fails after the file write, restore the prior persona bytes using the same atomic
  replace helper before returning. When `TEN_BOT` is present, use its normalized value as the display
  name and reject a conflicting explicit `display_name`. Normal
  non-onboarding Agent updates retain current behavior. If onboarding is active, a successful
  full-document edit moves the flow back to `persona` and clears fingerprint/receipt so it must be
  validated and tested again.

- [ ] **Step 4: Run GREEN and Agent regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAgent|TestPersona'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(AgentDisplayName|AdvanceOnboardingPersona|InvalidateOnboardingPersona)'
  ```

  Expected: both commands exit `0`; validation failures leave persona, metadata and state unchanged.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_agent_meta.go `
    appmode/overlay/internal/store/app_agent_meta_test.go `
    appmode/overlay/internal/store/app_onboarding.go `
    appmode/overlay/internal/store/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_agent.go `
    appmode/overlay/internal/daemon/app_agent_test.go `
    appmode/overlay/internal/daemon/app_persona.go `
    appmode/overlay/internal/daemon/app_onboarding_test.go
  git diff --cached --check
  git commit -m "feat: require complete onboarding persona"
  ```

### Task 7: Put the structured display name into production prompt identity

**Files:**

- Modify: `appmode/overlay/internal/daemon/app_zalo_session_prompt.go:87-210`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_prompt_test.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_hook.go:138-700`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_hook_test.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_runner_test.go`

**Public behavior to verify:** Production bootstrap/stateless consult prompts contain an authoritative
display-name identity section. Changing only the structured display name changes the prompt
fingerprint and rotates the Claude CLI session; subsequent turns in the same unchanged session do
not resend the full persona/display-name block.

- [ ] **Step 1: Write failing prompt and session-rotation tests**

  ```go
  func TestAppZaloPromptFingerprintIncludesDisplayName(t *testing.T) {
      zc := appPromptTestConfig(t)
      first := appZaloPromptFingerprint(zc, "thread-1", "Bé Mi")
      second := appZaloPromptFingerprint(zc, "thread-1", "Bé Na")
      if first == second {
          t.Fatal("display-name change must rotate prompt fingerprint")
      }
  }

  func TestAppZaloBootstrapPromptNamesAgentOnce(t *testing.T) {
      prompt := buildAppZaloBootstrapPrompt(appPromptTestConfig(t), "thread-1", "Bé Mi", "Xin chào", nil)
      if !strings.Contains(prompt, "Tên hiển thị bắt buộc của bạn: Bé Mi") {
          t.Fatalf("bootstrap prompt lacks identity: %q", prompt)
      }
  }
  ```

  Add runner-hook cases proving the first turn receives identity/persona, resumed same-fingerprint
  turn receives only delta input, display-name change selects a fresh session UUID, and stateless
  Codex/fallback paths also receive identity. Assert prompt logs remain absent as before.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppZalo.*(DisplayName|NamesAgent|Fingerprint)'
  ```

  Expected: current fingerprint signature and prompt omit display name.

- [ ] **Step 3: Thread display name through one captured turn snapshot**

  Use a small authoritative helper:

  ```go
  func appAgentIdentityPrompt(displayName string) string {
      displayName = strings.TrimSpace(displayName)
      if displayName == "" {
          return ""
      }
      return "Tên hiển thị bắt buộc của bạn: " + displayName +
          ". Khi tự giới thiệu, phải dùng đúng tên này.\n"
  }

  func appZaloPromptFingerprint(zc zaloConfig, threadID, displayName string) string {
      h := sha256.New()
      appZaloWriteFingerprintField(h, "display_name", strings.TrimSpace(displayName))
      appZaloWriteFingerprintField(h, "persona", readPersona(zc.PersonaPath))
      appZaloWriteFingerprintField(h, "roster", readPersona(zc.RosterPath))
      appZaloWriteFingerprintField(h, "overlay", readPersona(zaloOverlayPath(zc.OverlayDir, threadID)))
      return hex.EncodeToString(h.Sum(nil))
  }
  ```

  Load display name once while capturing the immutable turn snapshot, pass it through bootstrap,
  resume selection and stateless consult paths, and prepend the identity helper immediately before
  persona content. Do not inject the full identity/persona block into a resumed unchanged Claude
  session delta.

- [ ] **Step 4: Run GREEN and session regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppZalo(Session|Prompt|Structured|Stateless|DisplayName)'
  ```

  Expected: exit `0`; existing per-thread session and Memory tests remain green.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_zalo_session_prompt.go `
    appmode/overlay/internal/daemon/app_zalo_session_prompt_test.go `
    appmode/overlay/internal/daemon/app_zalo_session_hook.go `
    appmode/overlay/internal/daemon/app_zalo_session_hook_test.go `
    appmode/overlay/internal/daemon/app_zalo_session_runner_test.go
  git diff --cached --check
  git commit -m "feat: apply agent display name to prompts"
  ```

### Task 8: Run isolated staged Test Chat and mint a hash-only receipt

**Files:**

- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_accounts.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_accounts_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go:45-180`
- Modify: `appmode/overlay/internal/daemon/app_llm_router_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Modify: `appmode/overlay/internal/daemon/app_foundation_test.go`

**Public behavior to verify:** `POST /onboarding/test-chat` accepts one trimmed message up to 500
characters, uses the real router/runner with one staged fallback entry pinned to the exact disabled
Account, applies Persona/display name, and produces no Zalo session, Memory, transcript or live
attempt telemetry. A nonempty answer containing the normalized display name mints a 256-bit opaque
one-time token; SQLite stores only its SHA-256 hash with a 10-minute expiry.

- [ ] **Step 1: Write failing executor, receipt and no-side-effect tests**

  ```go
  func TestAppOnboardingTestChatPinsStagingAndHasNoLiveSideEffects(t *testing.T) {
      env := newOnboardingTestEnv(t)
      state := env.readyForTest("codex", "acct-stage", "gpt-5.6-terra", "Bé Mi")
      before := env.sideEffectDigest()
      env.runner.answer = "Xin chào, mình là Bé Mi."
      rr := env.serveJSON("POST", "/onboarding/test-chat", map[string]any{
          "revision": state.Revision, "message": "Xin chào",
      })
      if rr.Code != http.StatusOK {
          t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
      }
      if env.runner.accountID != "acct-stage" || env.runner.modelID != "gpt-5.6-terra" {
          t.Fatalf("runner account/model = %q/%q", env.runner.accountID, env.runner.modelID)
      }
      if after := env.sideEffectDigest(); after != before {
          t.Fatalf("side effects changed: before=%s after=%s", before, after)
      }
      if strings.Contains(env.persistedOnboardingJSON(), "Xin chào") ||
          strings.Contains(env.persistedOnboardingJSON(), "Bé Mi.") {
          t.Fatal("message or answer persisted")
      }
  }
  ```

  Add cases for Claude pinned ConfigDir, no fallback to a live Account, empty answer, missing bot
  name with Unicode case-fold/space normalization, message empty/too long, wrong phase/fingerprint,
  stale revision, two concurrent calls (`ONBOARDING_TEST_BUSY`), client cancellation, hard 120-second
  timeout with child cancellation, random-source failure, and receipt hash/expiry/invalidation. Spy on
  `RecordLLMAttempt`, session writes and Memory hooks and assert zero calls.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppOnboardingTestChat|TestOnboardingPinnedAccount'
  ```

  Expected: route and isolated executor are missing.

- [ ] **Step 3: Implement a pinned runner factory and receipt boundary**

  Keep live runner unchanged; construct an onboarding runner with injected no-op telemetry and a
  credential/config resolver pinned to the validated Account:

  ```go
  type appOnboardingTestRequest struct {
      Revision int64  `json:"revision"`
      Message  string `json:"message"`
  }

  type appOnboardingTestResponse struct {
      Answer     string `json:"answer"`
      BotName    string `json:"bot_name"`
      ProviderID string `json:"provider_id"`
      ModelID    string `json:"model_id"`
      TestToken  string `json:"test_token"`
      ExpiresAt  string `json:"expires_at"`
      Revision   int64  `json:"revision"`
  }

  type appOnboardingAttemptSink struct{}

  func (appOnboardingAttemptSink) RecordLLMAttempt(store.LLMAttempt) error { return nil }

  func newOnboardingToken() (plain string, hash string, err error) {
      raw := make([]byte, 32)
      if _, err = io.ReadFull(rand.Reader, raw); err != nil {
          return "", "", err
      }
      plain = base64.RawURLEncoding.EncodeToString(raw)
      sum := sha256.Sum256([]byte(plain))
      return plain, hex.EncodeToString(sum[:]), nil
  }
  ```

  Build a route snapshot with exactly one enabled fallback entry. For Codex, supply a config-dir
  picker that returns only the staged Account directory; for Claude, call a helper that receives that
  Account directly instead of the round-robin selector. Run `appLLMRunner.Run` with a 120-second
  child context, the normal consult prompt plus authoritative short Vietnamese self-introduction,
  and no Zalo session wrapper. Normalize display name/answer with `strings.Fields` plus Unicode
  lowercasing before containment. Persist only hash, expiry, fingerprint and a bumped revision.

- [ ] **Step 4: Run GREEN and router safety regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppOnboardingTestChat|OnboardingPinnedAccount|AppLLMRunner|AppLLMAccounts)'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(SaveOnboardingTestReceipt|InvalidateOnboardingPersona)'
  ```

  Expected: both commands exit `0`; no-side-effect spies remain zero and persisted state contains no
  clear token/message/answer.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_onboarding.go `
    appmode/overlay/internal/store/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_llm_accounts.go `
    appmode/overlay/internal/daemon/app_llm_accounts_test.go `
    appmode/overlay/internal/daemon/app_llm_router.go `
    appmode/overlay/internal/daemon/app_llm_router_test.go `
    appmode/overlay/internal/daemon/app_onboarding.go `
    appmode/overlay/internal/daemon/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_routes.go `
    appmode/overlay/internal/daemon/app_foundation_test.go
  git diff --cached --check
  git commit -m "feat: verify onboarding with staged chat"
  ```

### Task 9: Complete onboarding in one rollback-safe routing transaction

**Files:**

- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Modify: `appmode/overlay/internal/daemon/app_foundation_test.go`

**Public behavior to verify:** `POST /onboarding/complete` accepts one valid unexpired token only after
manual confirmation. It constant-time checks the token hash, revalidates revision, fingerprint,
Provider/Account/model and receipt, then atomically enables the staged Account, disables other
Accounts of that Provider, deletes all old Combo/member/legacy route projection rows, creates exactly
one active fallback Combo, clears the receipt and sets onboarding V1 completed. Any injected SQL
failure rolls everything back.

- [ ] **Step 1: Write failing success, receipt and fault-injection tests**

  ```go
  func TestCompleteOnboardingAtomicallyReplacesRouting(t *testing.T) {
      st := openAppStoreForTest(t)
      fixture := seedReadyOnboardingFixture(t, st)
      got, err := st.CompleteOnboarding(store.CompleteOnboardingInput{
          Revision:           fixture.Revision,
          TestNonceHash:      fixture.TokenHash,
          PersonaFingerprint: fixture.PersonaFingerprint,
          Now:                fixture.Now,
      })
      if err != nil {
          t.Fatal(err)
      }
      combos, err := st.LLMCombos()
      if err != nil {
          t.Fatal(err)
      }
      if len(combos) != 1 || !combos[0].Active || combos[0].ID != fixture.StagedComboID ||
          combos[0].Type != "fallback" || len(combos[0].Members) != 1 {
          t.Fatalf("combos = %+v", combos)
      }
      if got.CompletedVersion != store.CurrentOnboardingVersion ||
          got.Phase != store.OnboardingPhaseCompleted {
          t.Fatalf("state = %+v", got)
      }
  }
  ```

  Build a Store test seam that fails after each statement group: account enable/disable, member
  delete, combo delete, combo insert, member insert and state update. For every injected point compare
  a digest of accounts/combos/members/legacy route/state before and after. Add Provider-other-kind
  preservation, same-kind old Account disabled-but-not-deleted, one-time receipt, expired token,
  wrong token, stale revision/fingerprint, unavailable model, enabled-state drift, two concurrent
  completes and hash-only API response cases.

- [ ] **Step 2: Run RED**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'TestCompleteOnboarding'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppOnboardingComplete'
  ```

  Expected: missing Store transaction and HTTP handler.

- [ ] **Step 3: Implement receipt verification and the single transaction**

  ```go
  type CompleteOnboardingInput struct {
      Revision          int64
      TestNonceHash     string
      PersonaFingerprint string
      Now               time.Time
  }

  type appOnboardingCompleteRequest struct {
      Revision  int64  `json:"revision"`
      TestToken string `json:"test_token"`
  }

  type appOnboardingCompleteResponse struct {
      Completed         bool   `json:"completed"`
      OnboardingVersion int64  `json:"onboarding_version"`
      ComboID           string `json:"combo_id"`
  }
  ```

  The daemon validates token shape, hashes it, compares decoded hash bytes with
  `subtle.ConstantTimeCompare`, recomputes the current persona/display-name fingerprint under the
  onboarding mutation lock, and passes only the token hash plus fingerprint to Store. The Store opens
  one SQL transaction, re-reads and validates the singleton plus Account/model/persona snapshot, updates
  Account enabled states, manually deletes all combo members and legacy route rows, deletes combos,
  inserts the staged fallback Combo and its one member, then updates the singleton through the final
  expected revision. Commit only after every statement succeeds. Map stable codes:
  `ONBOARDING_TEST_REQUIRED`, `ONBOARDING_TEST_EXPIRED`,
  `ONBOARDING_CONFIGURATION_CHANGED`, `ONBOARDING_REVISION_CONFLICT` and
  `ONBOARDING_COMMIT_FAILED`.

- [ ] **Step 4: Run GREEN and full routing regressions**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(CompleteOnboarding|LLMCombo|LLMRoute|LLMAccount)'
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppOnboardingComplete|AppPortalRoutes|AppLLMRoute|AppLLMCombo)'
  ```

  Expected: both commands exit `0`; every injected fault leaves the full before/after digest equal.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_onboarding.go `
    appmode/overlay/internal/store/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_onboarding.go `
    appmode/overlay/internal/daemon/app_onboarding_test.go `
    appmode/overlay/internal/daemon/app_routes.go `
    appmode/overlay/internal/daemon/app_foundation_test.go
  git diff --cached --check
  git commit -m "feat: activate verified onboarding atomically"
  ```

### Task 10: Extract the existing Provider Connect lifecycle into a shared component

**Files:**

- Create: `appmode/overlay/internal/webui/static/components/provider-connect.js`
- Create: `appmode/tests/provider-connect.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/pages/providers.js`
- Modify: `appmode/tests/providers.test.mjs`

**Public behavior to verify:** Providers retains its current detect/install/login/poll/cancel UI,
progress percentage, elapsed timer, sanitized terminal log, login link/code, retry and disposal
semantics, but the lifecycle exists in one reusable component that can accept an onboarding revision
and report terminal success once.

- [ ] **Step 1: Move behavior assertions to a failing component contract**

  ```javascript
  test("provider connect reports terminal success exactly once", async () => {
    const phases = [
      { phase: "detecting", message: "Đang kiểm tra" },
      { phase: "login", code: "ABCD-EFGH", message: "Đăng nhập" },
      { phase: "connected", providerId: "codex", accountId: "acct-1" },
    ];
    const terminal = [];
    const component = createProviderConnect({
      kind: "codex",
      service: scriptedConnectService(phases),
      onConnected: (state) => terminal.push(state),
      pollDelayMs: 0,
    });
    component.mount(testDocument.createElement("section"));
    await component.start({ label: "Máy chính", onboardingRevision: 9 });
    await flushAsyncWork();
    assert.equal(terminal.length, 1);
    assert.equal(terminal[0].accountId, "acct-1");
    component.dispose();
  });
  ```

  Port the current Providers test matrix to the new component: phase-to-progress mapping, timer,
  start body, login URL/code, cancel is authoritative, stale poll after retry, error rendering,
  terminal callback, back/cancel hook and dispose. Keep page-level tests asserting Provider detail
  still mounts the component and refreshes accounts after ordinary success.

- [ ] **Step 2: Run RED**

  ```powershell
  node --test appmode/tests/provider-connect.test.mjs
  ```

  Expected: module import fails because the component does not exist.

- [ ] **Step 3: Extract without changing the service contract**

  Export a small controller:

  ```javascript
  export function createProviderConnect({
    kind,
    service,
    onConnected = () => {},
    onBack = null,
    pollDelayMs = 700,
    now = () => Date.now(),
  }) {
    let disposed = false;
    let runToken = 0;
    let host = null;

    function mount(container) {
      host = container;
      renderIdle();
    }

    async function start({ label = "", onboardingRevision = 0 } = {}) {
      const token = ++runToken;
      await service.start(kind, { label, onboarding_revision: onboardingRevision || undefined });
      await pollUntilTerminal(token);
    }

    function dispose() {
      disposed = true;
      runToken += 1;
      stopProgressTimer();
    }

    return Object.freeze({ cancel, dispose, mount, start });
  }
  ```

  Implement `renderIdle`, polling, progress and timer with the exact existing DOM/text behavior moved
  from `providers.js`. Remove the old nested polling state machine from Providers and instantiate
  this component there. Ensure optional onboarding fields are omitted for normal Connect.

- [ ] **Step 4: Run GREEN and Providers regressions**

  ```powershell
  node --test appmode/tests/provider-connect.test.mjs appmode/tests/providers.test.mjs appmode/tests/providers-status.test.mjs
  ```

  Expected: exit `0`; ordinary Providers Connect still sends no `onboarding_revision`.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/components/provider-connect.js `
    appmode/overlay/internal/webui/static/pages/providers.js `
    appmode/tests/provider-connect.test.mjs `
    appmode/tests/providers.test.mjs
  git diff --cached --check
  git commit -m "refactor: share provider connect component"
  ```

### Task 11: Extract dynamic Persona fields without changing the Agent page

**Files:**

- Create: `appmode/overlay/internal/webui/static/components/persona-fields.js`
- Create: `appmode/tests/persona-fields.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/pages/agents.js`
- Modify: `appmode/tests/agents.test.mjs`

**Public behavior to verify:** Agent and onboarding can render the same dynamic placeholder fields,
friendly labels/samples, Unicode-length validation, remaining counter and first-error focus. Unknown
keys are humanized; legacy-ready Persona can request only a display-name field.

- [ ] **Step 1: Write the failing reusable field-model tests**

  ```javascript
  test("persona fields humanize unknown holes and block incomplete values", () => {
    const model = createPersonaFieldModel({
      holes: ["TEN_BOT", "linh-vuc-tu-van"],
      samples: { "linh-vuc-tu-van": "Ví dụ: nội thất văn phòng" },
      displayName: "",
    });
    assert.equal(model.fields[0].label, "Tên bot");
    assert.equal(model.fields[1].label, "Linh vuc tu van");
    assert.equal(model.remaining, 2);
    const result = model.validate({
      values: { TEN_BOT: "Bé Mi", "linh-vuc-tu-van": "" },
      displayName: "Bé Mi",
    });
    assert.equal(result.ok, false);
    assert.equal(result.firstErrorKey, "linh-vuc-tu-van");
  });
  ```

  Add cases for server-supplied sample, known labels, trim, newline, mustache delimiter, 60/61 Unicode
  code points, remaining counter, Enter-to-next-field, first-error focus, `TEN_BOT` supplying the
  display name without a duplicate input, and display-name-only mode when a legacy-ready persona has
  no `TEN_BOT`. Keep Agent page tests for GET/render/PUT/backup/error behavior.

- [ ] **Step 2: Run RED**

  ```powershell
  node --test appmode/tests/persona-fields.test.mjs
  ```

  Expected: module import fails because the shared component does not exist.

- [ ] **Step 3: Extract the model and DOM renderer**

  ```javascript
  export const MAX_PERSONA_VALUE_CODE_POINTS = 60;

  export function validatePersonaValue(value) {
    const normalized = String(value ?? "").trim();
    if (!normalized) return "Vui lòng điền mục này";
    if (/\r|\n/.test(normalized)) return "Chỉ nhập trên một dòng";
    if (normalized.includes("{{") || normalized.includes("}}")) {
      return "Không dùng dấu placeholder";
    }
    if (Array.from(normalized).length > MAX_PERSONA_VALUE_CODE_POINTS) {
      return "Tối đa 60 ký tự";
    }
    return "";
  }

  export function createPersonaFields({ agent, onChange = () => {} }) {
    let root = null;
    function mount(container) {
      root = container;
      render();
    }
    return Object.freeze({ focusFirstError, mount, read, render, validate });
  }
  ```

  Keep key values exactly as server sent; humanization affects labels only. When `TEN_BOT` exists,
  derive `display_name` from that value and do not render a second name input. Otherwise render the
  dedicated display-name field when metadata is empty. Refactor `agents.js` to use this renderer/model
  and preserve its public exports where current tests/importers depend on them, delegating to the
  shared functions instead of keeping duplicate validation.

- [ ] **Step 4: Run GREEN and Agent UI regressions**

  ```powershell
  node --test appmode/tests/persona-fields.test.mjs appmode/tests/agents.test.mjs
  ```

  Expected: exit `0`; existing Agent page DOM and request assertions remain green.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/components/persona-fields.js `
    appmode/overlay/internal/webui/static/pages/agents.js `
    appmode/tests/persona-fields.test.mjs `
    appmode/tests/agents.test.mjs
  git diff --cached --check
  git commit -m "refactor: share persona field component"
  ```

### Task 12: Build Welcome, Connect and hidden Setup wizard stages

**Files:**

- Create: `appmode/overlay/internal/webui/static/pages/onboarding.js`
- Create: `appmode/tests/onboarding.test.mjs`

**Public behavior to verify:** Required onboarding opens a full-screen legacy-styled wizard, offers
only Codex/Claude Code, preselects an eligible staged/active kind, warns that login is required and
live config changes only after verification, reuses the Connect component, and calls Setup exactly
once after terminal success before rendering Persona. Persisted phases resume correctly.

- [ ] **Step 1: Write failing service and early-stage controller tests**

  ```javascript
  test("connected onboarding runs setup once then renders persona", async () => {
    const calls = [];
    const service = createOnboardingService({
      requestJSON: async (path, options = {}) => {
        calls.push([path, options]);
        if (path === "/onboarding/setup") {
          return { phase: "persona", revision: 4, model_id: "gpt-5.6-terra" };
        }
        throw new Error(`unexpected path ${path}`);
      },
    });
    const page = createOnboardingPage({
      initialStatus: {
        required: true, phase: "connect", revision: 3,
        provider_kind: "codex", suggested_provider_kind: "codex",
      },
      service,
      connectFactory: ({ onConnected }) => fakeConnect({
        onConnected,
        terminal: { providerId: "codex", accountId: "acct-1" },
      }),
    });
    page.mount(testDocument.body);
    await clickByText(testDocument.body, "Kết nối");
    await flushAsyncWork();
    assert.equal(calls.filter(([path]) => path === "/onboarding/setup").length, 1);
    assert.match(testDocument.body.textContent, /Trợ lý của bạn là ai/);
  });
  ```

  Add Welcome disabled Continue, only two kinds, provider selection body/revision, preselection,
  warning copy, back cancel ordering, authoritative cancel after late terminal, Setup loading/success/
  retry, double terminal callback, stale async continuation, dispose and resume at `connect`, `setup`
  or `persona` cases.

- [ ] **Step 2: Run RED**

  ```powershell
  node --test --test-name-pattern='Welcome|Connect|Setup|resume' appmode/tests/onboarding.test.mjs
  ```

  Expected: onboarding module is missing.

- [ ] **Step 3: Implement a single-owner page controller and typed service**

  ```javascript
  export function createOnboardingService({ requestJSON: request = requestJSON } = {}) {
    return Object.freeze({
      status: ({ signal } = {}) => request("/onboarding/status", { signal }),
      selectProvider: (kind, revision, { signal } = {}) => request("/onboarding/provider", {
        method: "PUT", body: { kind, revision }, signal,
      }),
      setup: (accountId, revision, { signal } = {}) => request("/onboarding/setup", {
        method: "POST", body: { account_id: accountId, revision }, signal,
      }),
    });
  }

  export function createOnboardingPage({
    initialStatus,
    service = createOnboardingService(),
    connectFactory = createProviderConnect,
    onComplete = () => {},
  }) {
    let status = Object.freeze({ ...initialStatus });
    let disposed = false;
    let revisionToken = 0;
    let connect = null;
    let abortController = null;
    return Object.freeze({ dispose, mount });
  }
  ```

  Render the visible step rail as `Kết nối → Cá nhân hoá → Trò chuyện thử`; Setup is a status row
  `Đang chuẩn bị cấu hình… ✓`, not a fourth progress item. Every async continuation checks both
  `disposed` and its local revision token before rendering. Retain only one Connect instance and one
  setup promise per staged Account/revision.

- [ ] **Step 4: Run GREEN**

  ```powershell
  node --test --test-name-pattern='Welcome|Connect|Setup|resume' appmode/tests/onboarding.test.mjs
  ```

  Expected: exit `0`; no duplicate Setup call and stale Connect completion cannot advance the page.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/onboarding.js `
    appmode/tests/onboarding.test.mjs
  git diff --cached --check
  git commit -m "feat: add onboarding provider stages"
  ```

### Task 13: Add gated Persona, Test Chat, confirmation and completion UI

**Files:**

- Modify: `appmode/overlay/internal/webui/static/pages/onboarding.js`
- Modify: `appmode/tests/onboarding.test.mjs`

**Public behavior to verify:** Persona cannot advance while a field/display name is invalid. Test
Chat starts with `Xin chào`, permits one request at a time, renders the bot bubble under the configured
name, has no Skip, and exposes Complete only after a successful token. Refresh loses the token.
Failure supports Retry/Back. Completion says `{Tên bot} đã sẵn sàng!` and exposes Portal/Knowledge
handoff targets.

- [ ] **Step 1: Write failing late-stage gating tests**

  ```javascript
  test("test chat requires a successful receipt and manual confirmation", async () => {
    const requests = [];
    const page = readyForTestPage({
      requestJSON: async (path, options) => {
        requests.push([path, options.body]);
        if (path === "/onboarding/test-chat") {
          return {
            answer: "Xin chào, mình là Bé Mi.", bot_name: "Bé Mi",
            test_token: "opaque-test-token", expires_at: "2026-08-11T10:10:00Z", revision: 8,
          };
        }
        if (path === "/onboarding/complete") {
          return { completed: true, onboarding_version: 1, combo_id: "combo-new" };
        }
        throw new Error(`unexpected path ${path}`);
      },
    });
    page.mount(testDocument.body);
    assert.equal(findButton(testDocument.body, "Bỏ qua"), null);
    await clickByText(testDocument.body, "Gửi thử");
    assert.equal(requests.some(([path]) => path === "/onboarding/complete"), false);
    await clickByText(testDocument.body, "Ổn, dùng cấu hình này");
    assert.equal(requests.filter(([path]) => path === "/onboarding/complete").length, 1);
    assert.match(testDocument.body.textContent, /Bé Mi đã sẵn sàng!/);
  });
  ```

  Add Persona dynamic fields/counter/focus, legacy display-only submit, request revision,
  server 422 field errors, back invalidates volatile receipt, `Xin chào` prefill, 500/501 characters,
  one-in-flight, missing-name error, timeout/cancel, retry, complete stale/expired/failure, no Complete
  button before token, double confirmation, refresh/resume at `test` with no token, CTA Portal and
  CTA Knowledge cases.

- [ ] **Step 2: Run RED**

  ```powershell
  node --test --test-name-pattern='Persona|Test Chat|receipt|completion|ready' appmode/tests/onboarding.test.mjs
  ```

  Expected: late stages and methods are absent.

- [ ] **Step 3: Extend the service and controller without adding a second state owner**

  ```javascript
  const lateStageService = Object.freeze({
    loadAgent: ({ signal } = {}) => requestJSON("/agent", { signal }),
    saveAgent: (payload, { signal } = {}) => requestJSON("/agent", {
      method: "PUT", body: payload, signal,
    }),
    testChat: (message, revision, { signal } = {}) => requestJSON("/onboarding/test-chat", {
      method: "POST", body: { message, revision }, signal,
    }),
    complete: (testToken, revision, { signal } = {}) => requestJSON("/onboarding/complete", {
      method: "POST", body: { test_token: testToken, revision }, signal,
    }),
  });
  ```

  Merge these methods into `createOnboardingService`. Keep `testToken` only in controller memory and
  reset it on mount, Persona edit/back, test failure, status refresh and dispose. Derive current step
  only from the last server snapshot plus the volatile receipt. The manual confirmation button calls
  Complete; receiving a test answer alone must never auto-complete. Handoff callback receives
  `{ destination: "knowledge" }` or `{ destination: "portal" }` only after server success.

- [ ] **Step 4: Run GREEN and the full wizard suite**

  ```powershell
  node --test appmode/tests/onboarding.test.mjs
  ```

  Expected: exit `0`; every path lacks a Skip control and no Complete request precedes manual click.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/onboarding.js `
    appmode/tests/onboarding.test.mjs
  git diff --cached --check
  git commit -m "feat: gate onboarding with verified chat"
  ```

### Task 14: Gate Portal bootstrap and add the explicit Settings restart entry

**Files:**

- Create: `appmode/overlay/internal/webui/static/pages/settings.js`
- Create: `appmode/tests/settings.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/app-main.js:1-112`
- Modify: `appmode/overlay/internal/webui/static/core/router.js:65-83`
- Modify: `appmode/tests/app-main.test.mjs`
- Modify: `appmode/tests/navigation.test.mjs`

**Public behavior to verify:** App startup loads onboarding status before creating Portal rail/router.
Required/restart mounts only the wizard; completed mounts Portal exactly once; status error shows a
full-page retry and never guesses completed. Complete disposes wizard before Portal handoff. Settings
is now a minimal real page whose confirmed restart response remounts wizard while keeping live route
active.

- [ ] **Step 1: Write failing bootstrap and Settings tests**

  ```javascript
  test("bootstrap fails closed and never mounts Portal before status", async () => {
    let portalStarts = 0;
    const pending = deferred();
    const app = startApp({
      loadStatus: () => pending.promise,
      startPortal: () => { portalStarts += 1; return { dispose() {} }; },
      mountOnboarding: () => { throw new Error("not ready"); },
      document: testDocument,
      window: testWindow,
    });
    assert.equal(portalStarts, 0);
    pending.resolve({ required: false, phase: "completed", revision: 9 });
    await app.ready;
    assert.equal(portalStarts, 1);
    app.dispose();
  });

  test("settings restart requires explicit confirmation", async () => {
    const calls = [];
    mountSettings({
      container: testDocument.body,
      service: { restart: async (payload) => calls.push(payload) },
      onRestarted: () => {},
    });
    await clickByText(testDocument.body, "Thiết lập lại trợ lý");
    assert.equal(calls.length, 0);
    await clickByText(testDocument.body, "Xác nhận thiết lập lại");
    assert.deepEqual(calls, [{ confirmed: true, revision: 9 }]);
  });
  ```

  Add required/completed/restart/status-error/retry/stale-load/dispose/beforeunload/hash-manipulation/
  wizard-to-Portal/wizard-to-Knowledge cases. Assert rail/toggle are inert and hidden while wizard is
  active, then restored once. Navigation must expose Settings without a `todo` property.

- [ ] **Step 2: Run RED**

  ```powershell
  node --test appmode/tests/app-main.test.mjs appmode/tests/settings.test.mjs appmode/tests/navigation.test.mjs
  ```

  Expected: startup immediately calls `startPortal`, Settings still resolves to roadmap, and the new
  test module is missing.

- [ ] **Step 3: Add one top-level bootstrap owner**

  ```javascript
  export function startApp({
    document: documentRef = globalThis.document,
    window: windowRef = globalThis.window,
    loadStatus = () => requestJSON("/onboarding/status"),
    startPortal: mountPortal = startPortal,
    mountOnboarding = mountOnboardingPage,
  } = {}) {
    let disposed = false;
    let active = null;
    let loadRevision = 0;
    const ready = boot();

    function replaceActive(next) {
      active?.dispose();
      active = next;
    }

    function dispose() {
      if (disposed) return;
      disposed = true;
      loadRevision += 1;
      replaceActive(null);
    }

    return Object.freeze({ dispose, ready, retry: boot });
  }
  ```

  Remove the direct module-bottom `startPortal` call and invoke `startApp` there instead. Boot status
  fail-closed; required status adds an onboarding body class, hides rail/toggle accessibly and mounts
  wizard in `main`. Completed status removes the class and calls existing `startPortal`. Implement
  Settings as a real route in `implementedPages`, remove only its roadmap marker, load current status
  for revision, show a two-click confirmation and call `/onboarding/restart`. Wire the handoff without
  globals: `startApp` passes `onRestartOnboarding(status)` into `startPortal`; `startPortal` passes it
  as page context through `createPortalController`; Settings calls that injected callback with the
  restart response. The top-level owner disposes Portal before mounting the wizard. Do not add
  unrelated settings controls.

- [ ] **Step 4: Run GREEN and router regressions**

  ```powershell
  node --test appmode/tests/app-main.test.mjs appmode/tests/settings.test.mjs appmode/tests/navigation.test.mjs appmode/tests/router.test.mjs
  ```

  Expected: exit `0`; Portal never mounts before a successful completed status and restart never
  occurs from a single click.

- [ ] **Step 5: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/settings.js `
    appmode/overlay/internal/webui/static/app-main.js `
    appmode/overlay/internal/webui/static/core/router.js `
    appmode/tests/settings.test.mjs `
    appmode/tests/app-main.test.mjs `
    appmode/tests/navigation.test.mjs
  git diff --cached --check
  git commit -m "feat: gate portal with onboarding"
  ```

### Task 15: Scope the legacy-styled wizard, package every asset and verify the whole product

**Files:**

- Modify: `appmode/overlay/internal/webui/static/portal.css`
- Modify: `appmode/tests/shell.test.mjs`
- Modify: `appmode/overlay/internal/daemon/app_shell_test.go`
- Modify: `tests/build-app.Tests.ps1`
- Modify: `docs/PORTAL-VERIFICATION.md`

**Public behavior to verify:** The wizard visually belongs to the old Portal design, all new CSS is
scoped, every new ES module is embedded and copied into the packaged app, fresh/upgraded smoke flows
are documented, and all existing Node/Go/build/deployment gates remain green.

- [ ] **Step 1: Write failing CSS-scope and packaged-asset tests**

  ```javascript
  test("onboarding selectors are scoped away from legacy Portal pages", async () => {
    const css = await readFile(new URL("../overlay/internal/webui/static/portal.css", import.meta.url), "utf8");
    const onboardingRules = css.match(/[^{}]+\{[^{}]*\}/g)
      .filter((rule) => /onboarding|wizard/.test(rule));
    assert.ok(onboardingRules.length > 0);
    for (const rule of onboardingRules) {
      const selector = rule.slice(0, rule.indexOf("{"));
      assert.match(selector, /\[data-onboarding\]|\.onboarding-shell/);
    }
  });
  ```

  Extend the Go embedded-asset table and Pester package assertions with these exact paths:

  ```text
  assets/components/provider-connect.js
  assets/components/persona-fields.js
  assets/pages/onboarding.js
  assets/pages/settings.js
  ```

  Expected package scans must also reject clear onboarding test tokens, user prompts, answers,
  credential canaries and Account config-directory canaries.

- [ ] **Step 2: Run RED**

  ```powershell
  node --test appmode/tests/shell.test.mjs
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppShell'
  $env:ZALOBOT_REPO = 'C:\Users\manva\OneDrive\Máy tính\agentdc'
  pwsh -NoProfile -Command `
    '$r = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru; if ($r.FailedCount) { exit 1 }'
  ```

  Expected: new selector/asset assertions fail before CSS and package expectations are updated.

- [ ] **Step 3: Add only scoped styles and verification instructions**

  Reuse existing CSS custom properties, panel, button, badge, form and typography rules. Add scoped
  layouts for full-screen shell, visible three-step progress, Provider cards, Connect progress/log,
  Persona grid, chat bubbles and completion CTA. Include reduced-motion handling and narrow viewport
  stacking. Do not restyle `.portal-body`, rail, Providers, Combos, Agent or Memory selectors without
  an onboarding ancestor.

  In `docs/PORTAL-VERIFICATION.md`, add manual flows with expected results for:

  - fresh DB through Codex;
  - fresh DB through Claude Code;
  - upgraded DB proving old route/account pool remains live before Complete;
  - refresh at Connect/Setup/Persona/Test;
  - daemon restart at persisted phases;
  - failed/expired test receipt and Complete rollback;
  - Settings restart and both final CTAs.

- [ ] **Step 4: Run focused GREEN**

  ```powershell
  node --test appmode/tests/shell.test.mjs
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestAppShell'
  $env:ZALOBOT_REPO = 'C:\Users\manva\OneDrive\Máy tính\agentdc'
  pwsh -NoProfile -Command `
    '$r = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru; if ($r.FailedCount) { exit 1 }'
  ```

  Expected: all three commands exit `0`; packaged asset and canary checks pass.

- [ ] **Step 5: Run the complete verification matrix**

  ```powershell
  npm test --prefix appmode
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all
  $env:ZALOBOT_REPO = 'C:\Users\manva\OneDrive\Máy tính\agentdc'
  pwsh -NoProfile -Command `
    '$r = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru; if ($r.FailedCount) { exit 1 }'
  pwsh -NoProfile -Command `
    '$r = Invoke-Pester -Script .\tests\memory-v2-deployment.Tests.ps1 -PassThru; if ($r.FailedCount) { exit 1 }'
  git diff --check
  git status --short
  ```

  Expected: Node, all staged Go packages, BuildApp and Memory V2 deployment gates exit `0`; no
  unrelated or generated runtime files appear.

- [ ] **Step 6: Review acceptance evidence before the final commit**

  Verify explicitly from test names/output:

  1. fresh and upgraded databases are required exactly once for onboarding V1;
  2. live Combo/route/account pool is unchanged before Complete;
  3. Persona has no mustache hole and always has display name;
  4. Test Chat pins staging and writes no Memory/session/live telemetry;
  5. Complete is one-time and rollback-safe;
  6. bootstrap fail-closes and Settings restart is explicit;
  7. old Providers/Agent/Portal UI regressions are green;
  8. package contains every new module and no secret/test content.

- [ ] **Step 7: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/portal.css `
    appmode/tests/shell.test.mjs `
    appmode/overlay/internal/daemon/app_shell_test.go `
    tests/build-app.Tests.ps1 `
    docs/PORTAL-VERIFICATION.md
  git diff --cached --check
  git commit -m "test: verify packaged onboarding wizard"
  ```

## Final review gate

After all task commits, invoke `:requesting-code-review` for a spec-compliance pass and a code-quality
pass. Resolve only verified findings, rerun the affected focused tests, then rerun the complete
verification matrix from Task 15. Do not claim completion from an earlier green run after code has
changed.

The branch is ready for `:finishing-a-development-branch` only when:

- `git status --short` is clean;
- every acceptance item above has direct test evidence;
- the final reviewer has no unresolved blocking finding;
- no push, PR or merge has been performed without a fresh explicit user instruction.
