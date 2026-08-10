# Main Provider Routing + Session/Memory V2 — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` or `:executing-plans` to run
> this plan. Steps use `- [ ]` checkboxes.

**Goal:** Merge pinned `origin/main` into the Memory V2 feature line without losing Provider routing,
Claude per-thread sessions, speaker-scoped Memory, Portal behavior, data, or package safety gates.

**TDD mode:** yes — confirmed by the user on 2026-08-10.

**Architecture:** The integration produces schema V5 through capability-aware migration from both V4
lineages. Zalo answer orchestration prepares both a full stateless prompt and a structured Claude
session input; Provider routing chooses the execution engine, while only a successful structured
Claude turn advances session state.

**Tech stack:** Go daemon overlay, SQLite, vanilla JavaScript Portal, Node built-in test runner,
PowerShell/Pester build gates, Windows DPAPI and local CLI adapters.

**Spec:** `.planning/specs/2026-08-10-main-memory-v2-integration-design.md`

**Research:** skipped — this is an integration of two already implemented and documented branches;
the merge-tree, both approved specs and the pinned source commits are the research inputs.

**Pinned merge target:** `b6606d3f1de605f71ba4ea2f43e78459d716cdfa`

---

## Execution rules

- Work only in `D:\TuvanZalo\_build\.worktrees\portal-m1` on
  `integration/main-memory-v2`.
- Do not modify or start migration against `D:\TuvanZalo\data`, `brain`, the live executable or
  live static assets.
- Fetching may update `origin/main`, but the merge must use the pinned full SHA above. If that object
  is unavailable, stop instead of substituting a newer commit.
- Record RED output before each production behavior change. A compile error caused only by unresolved
  conflict markers is not acceptable RED evidence; first restore the valid feature-side baseline for
  the file under test.
- Tasks 1–5 run inside one `git merge --no-commit` transaction. Git cannot create independent normal
  commits while that state is open, so each task ends with a verified staged checkpoint. Task 6 makes
  the single true merge commit after every conflict and required test is green.
- Do not push, merge to `main` or deploy. Those remain explicit user decisions after review.

## File responsibility map

- `appmode/overlay/internal/store/app_schema.go` — sole owner of Portal schema versioning and bridge
  migration.
- `appmode/overlay/internal/store/app_schema_test.go` — real SQLite fixtures for every supported
  upgrade lineage.
- `appmode/overlay/internal/daemon/app_zalo_session_runner.go` — structured runner input/result
  contract and Claude process adapter.
- `appmode/overlay/internal/daemon/app_zalo_session_hook.go` — one answer entrypoint, thread gate,
  prompt/session selection and completion persistence.
- `appmode/overlay/internal/daemon/app_llm_router.go` — one immutable route snapshot, Provider
  fallback, CLI/attachment safety and telemetry.
- `appmode/overlay/internal/daemon/app_llm_router_test.go` — route/session matrix through public runner
  interfaces.
- `appmode/overlay/internal/daemon/app_routes.go` and `app_foundation_test.go` — union of HTTP routes
  and route exposure contract.
- `appmode/overlay/internal/webui/static/core/router.js` and Portal tests — union of Providers,
  Combos and real Memory navigation.
- `scripts/BuildApp.psm1` — exact-once upstream seam application and package safety assertions.
- `tests/build-app.Tests.ps1` — source/stage/package drift regression contract.
- `.planning/plans/2026-08-06-provider-routing.md` — documentation conflict only.

## Temporary Go-test harness while the merge is open

`tests/run-overlay-go-tests.ps1` intentionally refuses a dirty production overlay. During Tasks 2–5,
use a disposable overlay copy so that safety guard remains unchanged:

```powershell
function Invoke-MergeOverlayGoTest {
  param(
    [ValidateSet('store', 'daemon', 'all')][string]$Package,
    [Parameter(Mandatory)][string]$Run
  )
  $project = (Get-Location).Path
  $scratch = Join-Path ([IO.Path]::GetTempPath()) `
    ('zalo-merge-test-' + [guid]::NewGuid().ToString('N'))
  $overlayCopy = Join-Path $scratch 'overlay'
  $stage = Join-Path $scratch 'stage'
  New-Item -ItemType Directory -Force -Path $scratch | Out-Null
  Copy-Item -LiteralPath (Join-Path $project 'appmode\overlay') -Destination $overlayCopy -Recurse
  Import-Module (Join-Path $project 'scripts\BuildApp.psm1') -Force
  try {
    New-AppStage -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' `
      -Overlay $overlayCopy -StageRoot $stage | Out-Null
    Apply-AppSeams -Stage $stage
    $target = switch ($Package) {
      'store' { './internal/store' }
      'daemon' { './internal/daemon' }
      default { './...' }
    }
    Push-Location $stage
    try {
      & go test -count=1 -skip (Get-AppGoTestSkipPattern) -run $Run $target
      if ($LASTEXITCODE -ne 0) { throw "go test failed with exit code $LASTEXITCODE" }
    } finally { Pop-Location }
  } finally {
    if (Test-Path -LiteralPath $scratch) { Remove-Item -LiteralPath $scratch -Recurse -Force }
  }
}
```

Define this function at the start of each PowerShell session used while the merge is open. Every call
below supplies an exact package and regex. The function only copies files to `%TEMP%`; it does not
touch the upstream repo or live package.

### Task 1: Start the pinned merge and establish valid additive baselines

**Files:**

- Modify: `.planning/plans/2026-08-06-provider-routing.md`
- Modify: `appmode/overlay/internal/webui/static/core/router.js`
- Modify: `appmode/tests/navigation.test.mjs`
- Restore valid feature baseline pending later TDD:
  `appmode/overlay/internal/store/app_schema.go`,
  `appmode/overlay/internal/daemon/app_routes.go`,
  `appmode/overlay/internal/daemon/app_foundation_test.go`,
  `scripts/BuildApp.psm1`, `tests/build-app.Tests.ps1`

**Public behavior to verify:** The merged Portal navigation exposes Providers, Combos and the real
Memory page, while the remaining semantic conflict files are valid feature-side code ready for
focused RED tests.

- [ ] **Step 1: Verify the isolated branch and baseline before opening merge state**

  ```powershell
  git status --short --branch
  git rev-parse HEAD
  git cat-file -e b6606d3f1de605f71ba4ea2f43e78459d716cdfa^{commit}
  npm test --prefix appmode
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all
  pwsh -NoProfile -Command `
    '$r = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru; if ($r.FailedCount) { exit 1 }'
  ```

  Expected: clean `integration/main-memory-v2`; Node, staged Go and Pester baselines exit `0`. If any
  baseline is red, stop and report it before merging.

- [ ] **Step 2: Start the real pinned merge and confirm the known conflict set**

  ```powershell
  git merge --no-ff --no-commit b6606d3f1de605f71ba4ea2f43e78459d716cdfa
  git diff --name-only --diff-filter=U
  ```

  Expected: merge pauses with exactly the eight reviewed paths: provider plan, foundation test,
  routes, schema, schema test, router, BuildApp module and BuildApp tests. Any extra path requires a
  fresh merge analysis before continuing.

- [ ] **Step 3: Restore valid feature baselines for semantic files**

  ```powershell
  git checkout --ours -- `
    appmode/overlay/internal/store/app_schema.go `
    appmode/overlay/internal/daemon/app_routes.go `
    appmode/overlay/internal/daemon/app_foundation_test.go `
    scripts/BuildApp.psm1 `
    tests/build-app.Tests.ps1
  ```

  Do not stage these five files yet; later tasks must prove and implement their unions. This action
  removes conflict markers but deliberately retains the known feature behavior as the RED baseline.

- [ ] **Step 4: Write a failing navigation behavior test**

  Resolve `navigation.test.mjs` by retaining all auto-merged tests, then add assertions equivalent to:

  ```javascript
  test('navigation exposes providers combos and the real memory page', () => {
    const items = NAVIGATION.flatMap(({ items }) => items);
    const byID = (id) => items.find((item) => item.id === id);
    assert.equal(byID('providers').label, 'Providers');
    assert.equal(byID('combos').label, 'Combos');
    assert.equal(byID('memory').label, 'Memory');
    assert.equal(byID('memory').todo, undefined);
  });
  ```

  Restore `router.js` to the feature side before running:

  ```powershell
  git checkout --ours -- appmode/overlay/internal/webui/static/core/router.js
  node --test appmode/tests/navigation.test.mjs
  ```

  Expected RED: Providers and/or Combos are absent. A syntax/import failure means the conflict was not
  resolved correctly and must be fixed before continuing.

- [ ] **Step 5: Implement the navigation union and verify GREEN**

  The route list must contain the main entries and feature Memory entry:

  ```javascript
  { id: 'providers', label: 'Providers', page: providersPage },
  { id: 'combos', label: 'Combos', page: combosPage },
  { id: 'memory', label: 'Memory', page: memoryPage },
  ```

  Keep the established ordering and all legacy routes. Then run:

  ```powershell
  node --test appmode/tests/navigation.test.mjs
  npm test --prefix appmode
  ```

  Expected: focused and full Portal suites pass.

- [ ] **Step 6: Resolve the documentation conflict and stage the verified subset**

  Keep main's shipped status in `2026-08-06-provider-routing.md` and preserve only still-valid local
  path notes. Then:

  ```powershell
  git add -- `
    .planning/plans/2026-08-06-provider-routing.md `
    appmode/overlay/internal/webui/static/core/router.js `
    appmode/tests/navigation.test.mjs
  git diff --check
  ```

  Expected: these conflicts disappear from `git diff --name-only --diff-filter=U`. Commit is deferred
  to Task 6 because the merge transaction is still open.

### Task 2: Build a schema V5 bridge for both V4 lineages

**Files:**

- Modify: `appmode/overlay/internal/store/app_schema.go`
- Modify: `appmode/overlay/internal/store/app_schema_test.go`

**Public behavior to verify:** Opening either a Feature V4 or Main V4 database atomically produces
the complete V5 schema without losing Memory, session, Provider, account, Combo, route or telemetry
data.

- [ ] **Step 1: Resolve the test file to a valid union and add failing upgrade fixtures**

  Restore the feature baseline first:

  ```powershell
  git checkout --ours -- appmode/overlay/internal/store/app_schema_test.go
  ```

  Then bring in main's Provider/Combo migration cases from stage 3
  (`git show :3:appmode/overlay/internal/store/app_schema_test.go`). Add public migration tests:

  ```go
  func TestMigrateAppFeatureV4ToV5PreservesMemoryAndAddsLLM(t *testing.T) {
      db := openFeatureV4Fixture(t)
      mustMigrateApp(t, db)
      requireSchemaVersion(t, db, 5)
      requireTable(t, db, "app_memory_subject_revisions")
      requireTable(t, db, "llm_providers")
      requireTable(t, db, "llm_combos")
      requireFeatureFixtureRows(t, db)
  }

  func TestMigrateAppMainV4ToV5PreservesRoutingAndAddsMemory(t *testing.T) {
      db := openMainV4Fixture(t)
      mustMigrateApp(t, db)
      requireSchemaVersion(t, db, 5)
      requireTable(t, db, "app_zalo_cli_sessions")
      requireTable(t, db, "app_memory_subject_revisions")
      requireMainFixtureRows(t, db)
  }
  ```

  Also add `TestMigrateAppV5IsIdempotent` and `TestMigrateAppFutureVersionIsUntouched`. Fixtures must
  insert one representative row for every data family they claim to preserve.

- [ ] **Step 2: Run focused store tests and confirm RED for missing capabilities**

  Use the temporary harness:

  ```powershell
  Invoke-MergeOverlayGoTest -Package store `
    -Run 'TestMigrateApp(FeatureV4|MainV4|V5|Future)'
  ```

  Expected RED on the feature-side schema: Feature V4 lacks `llm_providers`/`llm_combos`; Main V4
  returns early at version 4 and lacks Memory/session capabilities.

- [ ] **Step 3: Implement the minimum atomic V5 bridge**

  The implementation must have one version owner and no version update inside `appLLMSchema`:

  ```go
  const appSchemaVersion int64 = 5

  func migrateApp(db *sql.DB) error {
      tx, err := db.Begin()
      if err != nil { return fmt.Errorf("begin Portal migration: %w", err) }
      defer func() { _ = tx.Rollback() }()

      version, exists, err := appSchemaVersionOf(tx)
      if err != nil { return err }
      if exists && version > appSchemaVersion { return tx.Commit() }
      if exists && version == appSchemaVersion { return tx.Commit() }

      if _, err := tx.Exec(appFoundationSchema); err != nil { return fmt.Errorf("migrate Portal foundation: %w", err) }
      if err := appMigrateMemoryV3(tx); err != nil { return err }
      if err := appMigrateMemoryV4(tx); err != nil { return err }
      if _, err := tx.Exec(appLLMSchema); err != nil { return fmt.Errorf("migrate Portal LLM providers: %w", err) }
      if err := appSetSchemaVersion(tx, appSchemaVersion, exists); err != nil { return err }
      return tx.Commit()
  }
  ```

  Preserve feature validation for malformed/negative versions. `appMigrateMemoryV3/V4` must remain
  idempotent via table/column/trigger inspection. Bring main's LLM schema verbatim except for its
  embedded `UPDATE app_meta SET value = '4' WHERE key = 'schema_version'`; preserve no-default Combo
  behavior and the idempotent
  `claude_code` → `claude-code` normalization.

- [ ] **Step 4: Verify GREEN and regression coverage**

  Run the focused regex above, then the complete integration-sensitive store set:

  ```powershell
  Invoke-MergeOverlayGoTest -Package store `
    -Run 'TestMigrateApp|TestZaloCLISession|TestLLM|TestAppMemory'
  ```

  Expected: both runs pass, second migration does not change fixture counts/revisions, future schema
  remains unchanged and no test reads/writes live data.

- [ ] **Step 5: Stage the schema checkpoint**

  ```powershell
  git add -- `
    appmode/overlay/internal/store/app_schema.go `
    appmode/overlay/internal/store/app_schema_test.go
  git diff --check
  ```

  Commit remains deferred to Task 6.

### Task 3: Compose Provider routing with structured Claude sessions

**Files:**

- Modify: `appmode/overlay/internal/daemon/app_zalo_session_runner.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_hook.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_hook_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router_test.go`

**Public behavior to verify:** API/CLI stateless answers receive a complete prompt without advancing
Claude state; fallback to Claude creates/resumes the right thread session and advances it exactly
once.

- [ ] **Step 1: Add failing structured-routing tests**

  Add the following observable cases through `appZaloStructuredRunner` and real route snapshots:

  ```go
  func TestAppLLMRunnerStructuredAPISuccessUsesFullPromptWithoutAdvancingSession(t *testing.T)
  func TestAppLLMRunnerStructuredFallbackUsesClaudeDeltaAndAdvancesSession(t *testing.T)
  func TestAppZaloVirginSessionStaysFreshAfterStatelessSuccess(t *testing.T)
  func TestAppAnswerZaloBuildsProviderRunnerInsideThreadGate(t *testing.T)
  ```

  The API fake must record its received prompt and return a valid answer. The Claude fake must record
  `SessionID`, `Prompt`, `Resume` and invocation count. Assert:

  ```go
  if gotAPI != fullPrompt { t.Fatalf("API prompt = %q; want full prompt", gotAPI) }
  if result.SessionAdvanced { t.Fatal("stateless API advanced Claude session") }
  if gotClaude.Prompt != deltaPrompt || !result.SessionAdvanced { t.Fatal("Claude fallback lost structured input") }
  if gotClaudeCalls != 1 { t.Fatalf("Claude calls = %d; want 1", gotClaudeCalls) }
  ```

- [ ] **Step 2: Run focused daemon tests and confirm RED for the missing contract**

  First resolve `app_routes.go` and `app_foundation_test.go` to the feature baseline if Git still marks
  them unmerged. Use the temporary harness:

  ```powershell
  Invoke-MergeOverlayGoTest -Package daemon `
    -Run 'TestApp(LLMRunnerStructured|ZaloVirgin|AnswerZaloBuildsProvider)'
  ```

  Expected RED: `appLLMRunner` does not implement `appZaloStructuredRunner`, stateless/full prompt is
  unavailable or `appAnswerZalo` still uses `deps.run` directly.

- [ ] **Step 3: Extend the structured input/result contract**

  Implement:

  ```go
  type appZaloSessionRunInput struct {
      SessionID         string
      Prompt            string
      StatelessPrompt   string
      Resume            bool
      RecoverySessionID string
      BootstrapPrompt   string
  }

  type appZaloRunResult struct {
      Answer            string
      ContextTokens     int64
      UsageMeasured     bool
      OutputBytes       int64
      SessionID         string
      Recovered         bool
      RecoveryAttempted bool
      RecoveryCode      string
      SessionAdvanced   bool
  }
  ```

  `execZaloRunner.appRunZaloSession` sets `SessionAdvanced=true` only after a successful Claude
  `RunSession`. `appRunZaloStructured` passes `selection.bootstrapPrompt` as `StatelessPrompt`,
  processes Memory operations for every successful answer, and returns before completion/recovery
  persistence when `SessionAdvanced` is false.

- [ ] **Step 4: Make the route runner structured without duplicating route policy**

  Refactor `appLLMRunner` so `Run` and `appRunZaloSession` share one internal route loop. The
  structured entrypoint must select prompts and Claude execution as follows:

  ```go
  func (r *appLLMRunner) appRunZaloSession(ctx context.Context, in appZaloSessionRunInput,
      step func(string)) (appZaloRunResult, error) {
      return r.runStructured(ctx, in.StatelessPrompt, step, func(entry store.LLMRouteEntry) (appZaloRunResult, error) {
          runner := r.cfg.Claude(entry.ModelID)
          structured, ok := runner.(appZaloStructuredRunner)
          if !ok { return appZaloRunResult{}, errors.New("llm route: Claude runner lacks session support") }
          return structured.appRunZaloSession(ctx, in, step)
      })
  }
  ```

  API and Codex/Gemini CLI successes wrap text as
  `appZaloRunResult{Answer: text, SessionAdvanced: false}`. Claude result/telemetry/error semantics
  stay identical to main. Do not call both `Run` and `appRunZaloSession` for one entry.

- [ ] **Step 5: Compose routing inside the single answer entrypoint and preserve virgin sessions**

  In `appAnswerZalo`, replace the runner selection with:

  ```go
  run := a.appZaloRunner(deps.cfg, deps.run, threadID, len(files) > 0)
  if structured, ok := run.(appZaloStructuredRunner); ok {
      run = &appZaloStructuredAnswerRunner{a: a, run: structured, zc: deps.cfg,
          threadID: threadID, question: question,
          currentZaloMsgID: appZaloCurrentMsgID(reply.ReplyQuote),
          files: append([]ipc.ZaloAttachment(nil), files...), bootstrapCursor: bootstrapCursor}
  }
  ```

  In session selection, run rotation/fingerprint checks first, then return `bootstrapSelection(session)`
  whenever `session.TurnCount == 0`. This guarantees `Resume=false` until Claude has actually created
  the transcript.

- [ ] **Step 6: Verify GREEN plus existing safety regressions**

  Run the focused regex, then:

  ```powershell
  Invoke-MergeOverlayGoTest -Package daemon `
    -Run 'TestApp(LLM|ZaloSession|ZaloMemory)|TestBuildAppZaloDeltaPrompt'
  ```

  Expected: API/full prompt, fallback/Claude delta, no-provider silent, configured terminal
  escalation, attachment-local-only, recovery, rotation, Memory speaker isolation and one-call tests
  all pass.

- [ ] **Step 7: Stage the runner checkpoint**

  ```powershell
  git add -- appmode/overlay/internal/daemon/app_zalo_session_runner.go `
    appmode/overlay/internal/daemon/app_zalo_session_hook.go `
    appmode/overlay/internal/daemon/app_zalo_session_hook_test.go `
    appmode/overlay/internal/daemon/app_llm_router.go `
    appmode/overlay/internal/daemon/app_llm_router_test.go
  git diff --check
  ```

  Commit remains deferred to Task 6.

### Task 4: Expose the union of Provider, Combo and Memory HTTP behavior

**Files:**

- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Modify: `appmode/overlay/internal/daemon/app_foundation_test.go`
- Review: `appmode/overlay/internal/webui/static/app-main.js`
- Review: `appmode/overlay/internal/webui/static/portal.css`
- Review: `appmode/tests/app-main.test.mjs`
- Review: `appmode/tests/helpers/dom-harness.mjs`

**Public behavior to verify:** Authenticated Portal clients can use every Provider/Account/Combo and
Memory endpoint from one daemon, and all three pages mount without legacy regressions.

- [ ] **Step 1: Add a failing route-union behavior test**

  Resolve `app_foundation_test.go` to a union of existing non-route tests, then define one expected
  route table containing at least:

  ```go
  expected := []string{
      "GET /providers", "POST /providers/{id}/connect", "GET /providers/{id}/accounts",
      "GET /combos", "POST /combos", "PUT /combos/{id}/activate",
      "GET /memory", "GET /memory/threads/{tid}",
      "GET /memory/threads/{tid}/members", "POST /memory/threads/{tid}/{id}/approve",
  }
  ```

  The test must exercise `registerAppRoutes` through a real `http.ServeMux`, not search source text.

- [ ] **Step 2: Run the route test and confirm RED on the feature baseline**

  Use the temporary harness:

  ```powershell
  Invoke-MergeOverlayGoTest -Package daemon -Run 'TestAppRoutes|TestAppFoundation'
  ```

  Expected RED: Provider/Account/Combo paths return no registered handler; existing Memory paths
  remain present.

- [ ] **Step 3: Implement the route union**

  Start from main's route registration and add every feature Memory route, or vice versa. Keep main's
  `connectMgr` initialization and CLI model seeding. Every mutation retains `a.auth` and the Portal
  mutation-header guard inside its handler. Do not register duplicate method/pattern pairs.

- [ ] **Step 4: Verify daemon route GREEN and full Portal behavior**

  Run the focused Go regex, then:

  ```powershell
  npm test --prefix appmode
  ```

  Expected: Provider connect progress, Combo model filtering, Memory member/detail lifecycle,
  Knowledge, Agents, legacy shell and `/zalo` tests pass. If an auto-merged JS/CSS file fails, change
  only the smallest behavior covered by the failing test and rerun it RED→GREEN.

- [ ] **Step 5: Stage the HTTP/UI checkpoint**

  ```powershell
  git add -- appmode/overlay/internal/daemon/app_routes.go `
    appmode/overlay/internal/daemon/app_foundation_test.go `
    appmode/overlay/internal/webui/static/app-main.js `
    appmode/overlay/internal/webui/static/portal.css `
    appmode/tests/app-main.test.mjs `
    appmode/tests/helpers/dom-harness.mjs
  git diff --check
  ```

  Commit remains deferred to Task 6.

### Task 5: Unify BuildApp seams and fail-closed package gates

**Files:**

- Modify: `scripts/BuildApp.psm1`
- Modify: `tests/build-app.Tests.ps1`
- Review/modify if a test requires it: `build-app.ps1`
- Modify: `docs/PORTAL-VERIFICATION.md`

**Public behavior to verify:** A staged package contains both Provider routing and Memory/session
orchestration, never bypasses either, rejects credentials/secrets and keeps the vetted upstream test
skip list narrow.

- [ ] **Step 1: Add failing source/stage/package assertions before importing main helpers**

  Extend `build-app.Tests.ps1` with observable assertions equivalent to:

  ```powershell
  $daemonFiles = Get-ChildItem (Join-Path $gotStage 'internal\daemon') -Filter '*.go' |
    Where-Object { -not $_.Name.EndsWith('_test.go') }
  $answerCalls = ($daemonFiles | ForEach-Object {
    [regex]::Matches([IO.File]::ReadAllText($_.FullName), 'a\.appAnswerZalo\(').Count
  } | Measure-Object -Sum).Sum
  $routeCalls = ($daemonFiles | ForEach-Object {
    [regex]::Matches([IO.File]::ReadAllText($_.FullName), 'a\.appZaloRunner\(').Count
  } | Measure-Object -Sum).Sum
  $hook = [IO.File]::ReadAllText((Join-Path $gotStage 'internal\daemon\app_zalo_session_hook.go'))
  $schema = [IO.File]::ReadAllText((Join-Path $gotStage 'internal\store\app_schema.go'))
  Assert-Equal $answerCalls 1 'single answer entrypoint'
  Assert-Equal $routeCalls 1 'single provider route composition'
  Assert-True ($hook -match 'SessionAdvanced') 'stateless turns must not complete Claude state'
  Assert-True ($schema -match 'appSchemaVersion int64 = 5') 'package must target schema V5'
  Assert-True ($schema -match 'llm_combos') 'package must contain Combo schema'
  Assert-True ($schema -match 'app_memory_subject_revisions') 'package must contain Memory V2 schema'
  ```

  Keep main's no-provider/credential tests and feature's overlay cleanliness, sensitive-content,
  binary-signature and deployment binding tests. The expected skip list is the exact set union of
  reviewed superseded upstream tests; no wildcard beyond the anchored alternation is allowed.

- [ ] **Step 2: Run Pester and confirm RED for missing main safeguards**

  ```powershell
  $r = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru
  if ($r.FailedCount -eq 0) { throw 'expected RED before BuildApp union' }
  ```

  Expected RED: provider credential/no-default helper or package signatures are absent from the
  feature-side `BuildApp.psm1`. Failures caused by unresolved conflict markers must be fixed first.

- [ ] **Step 3: Implement the BuildApp union**

  Keep feature `Apply-AppSeams` as the base and retain its `appAnswerZalo`/`appRunZalo`, operator
  lesson and deep-link seams. Do not re-add main's direct upstream `appZaloRunner` replacement.
  Instead add an exact source assertion that the overlay `app_zalo_session_hook.go` contains one
  `a.appZaloRunner(` call.

  Bring in main's:

  - `Assert-NoProviderCredential` and its export/call sites;
  - provider/Combo package signatures;
  - CLI/provider credential protections;
  - no-default routing assertions;
  - exact reviewed upstream skip entry for `TestJoinGreetsOnceForEveryone`.

  Keep feature's `Assert-CleanGitOverlay`, package sensitive-content scan, binary Memory signatures,
  canary/deploy bindings and seven existing superseded-test entries. With main's one no-default entry,
  the resulting skip regex must be exactly:

  ```text
  ^(?:TestAppJSKnowsTheSessionEndedCloseReason|TestAppJSSendsThePortalMutationHeader|TestPortalReloadedKeyWithLiveSessionReachesTheShell|TestPortalRootServesTheShellWithACookie|TestPortalUsesModalNotBrowserDialogs|TestAgentPortalNoLongerCarriesZalo|TestModalCallsPassAnObject|TestJoinGreetsOnceForEveryone)$
  ```

- [ ] **Step 4: Verify GREEN through Pester and staged Go tests**

  ```powershell
  $r = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru
  if ($r.FailedCount) { exit 1 }
  ```

  Then use the temporary harness:

  ```powershell
  Invoke-MergeOverlayGoTest -Package daemon `
    -Run 'TestAppRoutes|TestMigrateApp|TestAppLLM|TestAppZaloSession'
  Invoke-MergeOverlayGoTest -Package store `
    -Run 'TestMigrateApp|TestLLM|TestAppMemory|TestZaloCLISession'
  ```

  Expected: Pester and both staged Go runs pass; no temp path survives successful cleanup.

- [ ] **Step 5: Update verification documentation and stage the build checkpoint**

  Document schema V5 dual-lineage canary, Provider/no-provider checks and session fallback checks in
  `PORTAL-VERIFICATION.md`. Then:

  ```powershell
  git add -- scripts/BuildApp.psm1 tests/build-app.Tests.ps1 build-app.ps1 `
    docs/PORTAL-VERIFICATION.md
  git diff --check
  ```

  Commit remains deferred to Task 6.

### Task 6: Complete the atomic merge with fresh full verification

**Files:** All paths staged by the merge and Tasks 1–5.

**Public behavior to verify:** The integration branch has a true merge commit with no unresolved
paths, no conflict markers and all source-level suites green.

- [ ] **Step 1: Audit merge completeness before claiming readiness**

  ```powershell
  $unmerged = git diff --name-only --diff-filter=U
  if ($unmerged) { $unmerged; exit 1 }
  rg -n "^(<<<<<<<|=======|>>>>>>>)" --glob '!*.md' .
  git diff --check
  git status --short
  ```

  Expected: no unmerged path, no conflict marker in source and no whitespace error. Review every
  auto-merged path in `git diff --stat` before tests.

- [ ] **Step 2: Run fresh full verification**

  ```powershell
  npm test --prefix appmode
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all
  pwsh -NoProfile -Command `
    '$r = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru; if ($r.FailedCount) { exit 1 }'
  pwsh -NoProfile -Command `
    '$r = Invoke-Pester -Script .\tests\memory-v2-deployment.Tests.ps1 -PassThru; if ($r.FailedCount) { exit 1 }'
  ```

  `run-overlay-go-tests.ps1` may still reject the repository because the merge is uncommitted. If so,
  run the identical `go test ./...` via the temporary harness and record that expected cleanliness
  guard as the only reason the wrapper could not run. All test binaries themselves must be green.

- [ ] **Step 3: Commit the true merge**

  ```powershell
  git add -A
  git commit -m "merge: integrate main providers with Memory V2"
  git show --summary --pretty=raw HEAD
  ```

  Expected: the commit has two parents: the pre-merge integration commit and pinned main
  `b6606d3f1de605f71ba4ea2f43e78459d716cdfa`.

- [ ] **Step 4: Re-run cleanliness-dependent full suites after the merge commit**

  ```powershell
  git status --porcelain
  npm test --prefix appmode
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all
  pwsh -NoProfile -Command `
    '$r = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru; if ($r.FailedCount) { exit 1 }'
  ```

  Expected: clean tree and all commands exit `0`. Any post-commit fix starts a new RED→GREEN cycle and
  becomes a separate follow-up commit.

### Task 7: Build an isolated artifact, smoke-test upgrade paths and obtain code review

**Files:**

- Modify only if a regression is found: files owned by Tasks 2–5.
- Evidence: test output and temporary artifact path; no live-package writes.

**Public behavior to verify:** A real distributable artifact passes package assertions, fresh/no-
provider startup and copied-database migration checks, and an independent reviewer finds no Critical
or Important issue.

- [ ] **Step 1: Build a real isolated package**

  ```powershell
  $artifact = Join-Path ([IO.Path]::GetTempPath()) `
    ('zalo-main-memory-v2-' + [guid]::NewGuid().ToString('N'))
  pwsh -NoProfile -File .\build-app.ps1 `
    -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' `
    -PersonaSource 'D:\TuvanZalo\brain\reference\persona' `
    -Out $artifact
  Import-Module .\scripts\BuildApp.psm1 -Force
  Assert-AppPackage -Out $artifact | Out-Null
  ```

  Expected: build and package assertion exit `0`; the artifact is under `%TEMP%`, not
  `D:\TuvanZalo`.

- [ ] **Step 2: Re-run dual-lineage migration tests against file-backed fixtures**

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store `
    -Run 'TestMigrateApp(FeatureV4|MainV4|V5|Future)'
  ```

  Expected: every fixture passes and source fixture hashes/row assertions remain unchanged except for
  the copied DB's intended V5 additions.

- [ ] **Step 3: Run deployment contract tests without deploying**

  ```powershell
  $r = Invoke-Pester -Script .\tests\memory-v2-deployment.Tests.ps1 -PassThru
  if ($r.FailedCount) { exit 1 }
  ```

  Expected: canary, manifest, stale-source, rollback and safety guards pass. Do not invoke
  `Invoke-MemoryV2Deploy.ps1` against the live home.

- [ ] **Step 4: Request independent review against the approved plan**

  Dispatch the bundled `code-reviewer` with:

  ```text
  DESCRIPTION: Integrated pinned main Provider/Account/Combo routing with per-thread Claude sessions
               and speaker-scoped Memory V2 through schema V5 and a session-aware route contract.
  PLAN_OR_REQUIREMENTS: .planning/plans/2026-08-10-main-memory-v2-integration.md
  BASE_SHA: 1904427
  HEAD_SHA: the exact value printed by `git rev-parse HEAD` immediately before dispatch
  ```

  Fix every Critical and Important finding with a failing regression test first, rerun the affected
  suite, commit the fix and ask for a focused re-review.

- [ ] **Step 5: Perform the final fresh verification gate**

  ```powershell
  git status --porcelain
  npm test --prefix appmode
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all
  pwsh -NoProfile -Command `
    '$a = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru; ' +
    '$b = Invoke-Pester -Script .\tests\memory-v2-deployment.Tests.ps1 -PassThru; ' +
    'if ($a.FailedCount -or $b.FailedCount) { exit 1 }'
  ```

  Expected: clean tree and zero failures. Report the exact command outputs, merge commit, review
  disposition and artifact path. Do not push or deploy until the user chooses that next action.
