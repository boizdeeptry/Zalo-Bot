# Provider runtime registry hợp nhất — implementation plan

> **For automated executors:** dùng `:subagent-driven-development` và thực hiện tuần tự từng task.
> Mỗi task phải qua spec review rồi code-quality review trước task kế tiếp.

**Goal:** Loại bỏ các allowlist/switch Provider rải rác, chứng minh một subscription runtime thứ ba có
thể đi xuyên Onboarding, Complete và live Zalo routing bằng một catalog option + registration typed,
trong khi OpenCode tiếp tục bị deny production.

**TDD mode:** yes

**Architecture:** `providercatalog.Catalog` là value bất biến và được truyền vào
`store.OnboardingStore`; daemon dựng một `appProviderRuntimeRegistry` validated chứa metadata an toàn và
typed behavior drivers. `appRuntimeContext` truyền registry/Store/Connect tường minh cho handler, không
test-swap global. `/onboarding/status` chỉ project Onboarding Catalog; `/llm/providers` project toàn bộ
runtime UI metadata. Terminal/attachment/structured behavior được typed và route thật được validate.

**Tech stack:** Go, SQLite, vanilla JavaScript ES modules, Node `node:test`, PowerShell overlay harness.

**Spec:** `.planning/specs/2026-08-15-provider-runtime-registry-design.md`

**Workspace:** `D:/TuvanZalo/_build/.worktrees/portal-m1`, branch `integration/main-memory-v2`.

---

## Execution rules

- Chỉ sửa worktree trên. Không sửa upstream repo
  `C:/Users/manva/OneDrive/Máy tính/agentdc`, không đọc credential/auth/log hội thoại, không dùng dữ liệu
  live `D:/TuvanZalo/data`.
- Mỗi task đúng một commit. Overlay Go harness đòi tree sạch: commit RED tests trước, chạy RED, thêm
  production rồi `git commit --amend --no-edit`. Không coi fixture/syntax/flaky/timeout là RED hợp lệ.
- Mọi implementer phải biết có agent khác trong codebase, không revert edit/commit của người khác và
  chỉ sở hữu files được giao.
- Không thêm mutable registry/catalog global, `Set...ForTest`, `sync.Map` keyed bởi `*Store`/`*api`, hoặc
  package init phụ thuộc thứ tự test. Synthetic registry là local immutable value.
- `providercatalog.Catalog` zero value fail closed. Production route rank là durable contract; không đổi
  rank Codex 10/Claude 100 trong plan này.
- `OnboardingStore` dùng named field `store *Store`, không anonymous-embed, để không bypass catalog.
- Resolve Connect registration trước mkdir/account/config side effect. Seed/discover model phải thành
  công trước `BindOnboardingAccount`.
- `appProviderProductionDenied` phải chạy ở constructor và mọi lookup/projection/factory. Không dùng
  OpenCode làm fake runtime, không chạy OpenCode thật trong plan này.
- Metadata HTTP tuyệt đối không chứa credential, config dir, executable, package, argv, env, callback,
  prompt/answer/receipt/session ID.
- Attachment/structured/terminal là typed factory, không Boolean tự khai. API endpoint là compiled
  factory hiện có, không URL từ request/config.
- Route mutation lock order: `onboardingMutationMu` trước, rồi `appLLMRouteMutationMu`; không path nào
  lấy ngược. Combo replace/activate/Complete giữ lock qua read→validate→write.
- Tất cả JS production/test mới hoặc đã chạm phải `<=800` dòng. Không sửa `combos.js` (925 dòng) nếu
  thay `providers-status.js` đã đủ; nếu bắt buộc thì extract module trước trong RED/GREEN riêng.
- Giữ UI Onboarding modal trên nền lưới, sidebar ẩn và không đưa branding sản phẩm tham chiếu vào source.

## Test command conventions

Chạy từ worktree root:

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all -Run '<catalog-regex>'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run '<regex>'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '<regex>'
node --test appmode/tests/<file>.test.mjs
npm test --prefix appmode
```

Full Go overlay:

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1
```

---

### Task 1: Catalog Provider bất biến và canonical rank

**Files:**
- Modify: `appmode/overlay/internal/providercatalog/catalog.go`
- Modify: `appmode/overlay/internal/providercatalog/catalog_test.go`

**Public behavior:** Constructor tạo Catalog defensive/immutable; zero value và metadata/rank sai fail
closed; package helpers cũ giữ đúng Codex/Claude behavior.

- [ ] **Step 1: Commit RED tests**

Thêm public tests:

```go
func TestCatalogCanonicalizesSyntheticProviderByRank(t *testing.T)
func TestCatalogDefensivelyCopiesInputAndOutput(t *testing.T)
func TestCatalogRejectsInvalidOptions(t *testing.T)
func TestZeroCatalogFailsClosed(t *testing.T)
func TestDefaultCatalogCompatibility(t *testing.T)
```

Fixture synthetic dùng options Codex 10, `future-cli` 50, Claude 100. Input selection
`[claude-code, future-cli, codex]` phải trả `[codex, future-cli, claude-code]`. Table invalid gồm blank,
uppercase/path separator/Unicode-confusable kind, duplicate kind/rank, blank/oversize display/description,
rank âm/overflow, hơn `MaxSelectedKinds`, advertised false được chọn. Bỏ test mutate package `options`.

- [ ] **Step 2: Chạy RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all -Run '^Test(ProviderCatalog|Catalog|ZeroCatalog|DefaultCatalog)'
```

Expected: thiếu `Catalog/New/Default` hoặc immutability assertion fail; không phải harness lỗi.

- [ ] **Step 3: Implement minimum**

Thêm:

```go
type Catalog struct {
    valid   bool
    options []Option
    byKind  map[string]Option
}

func New(options []Option) (Catalog, error)
func Default() Catalog
func (c Catalog) Options() []Option
func (c Catalog) Option(kind string) (Option, bool)
func (c Catalog) CanonicalSelectedKinds(input []string) ([]string, error)
```

Constructor copy, validate bounded ASCII metadata, sort rank, build private map. Accessors copy. Existing
`Options/OptionForKind/CanonicalSelectedKinds` delegate `Default()`; không exported mutable slice/map.

- [ ] **Step 4: GREEN + hygiene**

Chạy focused command rồi full `-Package all`, `gofmt`, `git diff --check`.

- [ ] **Step 5: Commit**

`refactor: make provider catalog immutable`

---

### Task 2: Catalog-bound Store selection, inspection và migration

**Files:**
- Create: `appmode/overlay/internal/store/app_onboarding_catalog.go`
- Create: `appmode/overlay/internal/store/app_onboarding_catalog_test.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_providers.go`
- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_schema.go`
- Modify: `appmode/overlay/internal/store/app_schema_test.go`

**Public behavior:** Default `*Store` vẫn chỉ cho Codex/Claude; wrapper synthetic cho phép future kind
theo rank mà không global mutation; public reads fail closed khi dùng sai catalog; V7 corrupt kind không
được backfill vào V8.

- [ ] **Step 1: Commit RED tests**

Tạo fixture helper `syntheticOnboardingCatalog(t)` và tests:

```go
func TestCatalogBoundOnboardingSelectionPersistsRankOrder(t *testing.T)
func TestDefaultOnboardingStoreRejectsSyntheticKind(t *testing.T)
func TestCatalogBoundSnapshotRejectsUnknownPersistedKind(t *testing.T)
func TestCatalogBoundBeginBackAndLostResponse(t *testing.T)
func TestCatalogBoundStagingInspectionUsesExactCatalog(t *testing.T)
func TestMigrateAppV7RejectsUnknownOnboardingKind(t *testing.T)
```

Chứng minh synthetic wrapper nhận unordered set, persist positions 0/1/2; default wrapper đọc cùng DB
phải reject. Back/lost response receipt dùng đúng catalog. Zero Catalog không mutate revision/rows. V7
unknown kind rollback migration, không để stage row nửa chừng.

- [ ] **Step 2: Chạy RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run '^Test(CatalogBound|DefaultOnboarding|MigrateAppV7RejectsUnknown)'
```

- [ ] **Step 3: Implement wrapper và catalog-threaded helpers**

```go
type OnboardingStore struct {
    store   *Store
    catalog providercatalog.Catalog
}

func (s *Store) Onboarding(c providercatalog.Catalog) OnboardingStore
```

Thêm wrapper methods cho `OnboardingState`, `OnboardingSnapshot`, `OnboardingStagingAccount`,
`ReplaceOnboardingProviderSelection`, `BeginOnboardingProvider`, `BackOnboardingToProviders`,
`EnsureOnboardingProviderForKind`, legacy `SelectOnboardingProvider`. Existing `*Store` methods delegate
`Default()`. Thread Catalog vào stage inspection, canonical/display lookup, replay/back validators.
Không ép raw transaction read validate trước repair; public response/inspection phải validate.

`IsOnboardingProviderKind` chỉ compatibility qua Default, active helper không gọi. V7 backfill dùng
Default option advertised; corrupt/unknown fail transaction.

- [ ] **Step 4: GREEN + broader Store regression**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'Test(OnboardingProvider|OnboardingSnapshot|MigrateApp)'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store
```

Gofmt, diff-check, no mutable global scan.

- [ ] **Step 5: Commit**

`refactor: bind onboarding selection to immutable catalog`

---

### Task 3: Catalog-bound Bind/Setup/Test/Complete và exact Provider singleton

**Files:**
- Modify: `appmode/overlay/internal/store/app_onboarding_catalog.go`
- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_providers.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test_route.go`
- Modify: `appmode/overlay/internal/store/app_llm.go`
- Create: `appmode/overlay/internal/store/app_onboarding_catalog_flow_test.go`

**Public behavior:** Synthetic catalog đi hết Store lifecycle và receipt fences; account-mode Provider là
exact singleton `ID==kind`; mọi failure atomic và default compatibility còn nguyên.

- [ ] **Step 1: Commit RED tests**

Tests phải phủ:

```go
func TestCatalogBoundOnboardingFullStoreFlow(t *testing.T)
func TestCatalogBoundTestReceiptRereadsSameCatalog(t *testing.T)
func TestCatalogBoundCompleteUsesSyntheticDisplayAndBindings(t *testing.T)
func TestEnsureAccountRuntimeProviderRequiresExactSingleton(t *testing.T)
func TestCatalogBoundFlowRollbackAndDefaultRejection(t *testing.T)
func TestCatalogBoundPersonaAndRestartMutationsRemainCatalogNeutral(t *testing.T)
```

Flow: Ensure future Provider → selection/begin → Bind disabled Account → Stage `future-model` → Persona/Test
fixture → `OnboardingTestRoute` → Save/replace/Clear receipt → Complete. Assert exact route position,
display/Combo, provider/account activation, cleanup stage rows. Default Store path reject persisted fake.

Singleton table: create exact row; preserve existing enabled; exact-ID wrong kind reject; sibling same-kind
row reject; failure không tạo/delete/update row. Save/Clear after external-I/O fixture must re-read with
synthetic Catalog. Add commit-hook rollback and lost-response assertions where existing seams apply.
Persona/restart test dùng wrapper snapshot trước/sau raw `Advance/Update/Invalidate/Restart` mutations để
chứng minh chúng không lookup Default Catalog, vẫn giữ/clear future stages đúng contract và CAS fail closed.

- [ ] **Step 2: RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run '^Test(CatalogBoundOnboardingFull|CatalogBoundTest|CatalogBoundComplete|EnsureAccountRuntime|CatalogBoundFlow)'
```

- [ ] **Step 3: Implement minimum**

Move/catalog-thread implementations for `BindOnboardingAccount`, `StageOnboardingSetup`,
`OnboardingTestRoute`, `SaveOnboardingTestReceipt`, `ClearOnboardingTestReceipt`, `CompleteOnboarding` and
their inspection/completion helpers. Existing methods delegate Default.

Add trusted generic account-mode ensure that validates bounded semantic kind/name and enforces exactly one
Provider row whose `id==kind && kind==kind`; sibling same-kind/mismatched exact ID returns stable conflict.
Do not expose it through HTTP without registry admission.

Không thêm `OnboardingStore` wrappers cho `AdvanceOnboardingPersona*`, `UpdateAgentPersona`,
`InvalidateOnboardingPersona`, `RestartOnboarding`: chúng không inspect kind. Catalog-aware caller phải
snapshot/validate trước và sau như test trên.

- [ ] **Step 4: GREEN**

Run focused, then full Store package:

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store
```

Gofmt/diff-check. Verify raw SQL/receipt/log output contains no
ConfigDir beyond existing internal Store contracts.

- [ ] **Step 5: Commit**

`refactor: make onboarding lifecycle catalog aware`

---

### Task 4: Validated operational registry và runtime context

**Files:**
- Create: `appmode/overlay/internal/daemon/app_provider_runtime.go`
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_test.go`
- Modify: `appmode/overlay/internal/daemon/app_provider_registry.go`
- Modify: `appmode/overlay/internal/daemon/app_provider_registry_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_connect.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`

**Public behavior:** Một immutable registry duy nhất sở hữu safe metadata/capabilities/factories; production
helpers delegate nó; OpenCode bị deny dù registration đầy đủ.

- [ ] **Step 1: Commit RED registry tests**

Test constructor với production options và synthetic future registration. Invalid table:

- duplicate kind/UI order/catalog rank;
- onboarding option thiếu registration hoặc registration metadata không khớp catalog;
- account mode thiếu Connect/model seeder/account strategy;
- credential mode không có compiled adapter;
- runtime không-denied thiếu safe management metadata/`visible`; visible runtime có connection mode sai,
  hidden runtime không dùng `visible=false` + `connection_mode=none` hoặc vẫn góp readiness/selectability;
- both/no standard-vs-terminal live path;
- attachment không có typed local runner;
- structured factory không có terminal;
- terminal trước non-terminal catalog rank;
- invalid kind/theme/execution/connection mode;
- `opencode` fully populated vẫn constructor/lookup/projection/factory deny.

Thêm defensive-copy/concurrent read test và test production registry không đổi khi tạo synthetic registry.

- [ ] **Step 2: RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^TestAppProviderRuntimeRegistry'
```

- [ ] **Step 3: Implement types/constructor/context**

Tạo typed metadata enums và package-private callbacks/factories theo spec. Registry giữ private Catalog +
map/sorted safe management-option slice; accessors deny-check trước mỗi lookup và trả copies. Mọi runtime
không-denied có safe metadata + `visible`; visible runtime dùng `connection_mode=account|credential`, hidden
live-only bắt buộc `visible=false`, `connection_mode=none`. Production registrations bọc existing
Codex/Claude/API functions và hidden live-only `gemini-cli` adapter/attachment behavior; Gemini CLI không nằm
Onboarding/gallery/Connect/readiness nhưng có safe hidden option để đọc persisted Provider/model/Combo cũ.
Core chưa dispatch qua callbacks ở task này.

Tạo:

```go
type appRuntimeContext struct {
    api      *api
    registry appProviderRuntimeRegistry
    connect  *connectManager
}
```

và `onboardingStore()`. `registerAppRoutes` dựng production context; thêm internal registration helper
nhận context cho tests. Compatibility `appProviderOptions/Supports*/subscription` delegate registry,
không materialize mutable Set. Cùng task đổi hai callsite còn đọc `subscriptionKinds` trong Connect
admission và live readiness sang production-registry lookup để mỗi checkpoint vẫn compile; Tasks 5/7
mới thread local context đầy đủ.

- [ ] **Step 4: GREEN + legacy registry/OpenCode tests**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(AppProviderRuntimeRegistry|AppProviderRegistry|OpenCode.*Production)'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
```

- [ ] **Step 5: Commit**

`refactor: add validated provider runtime registry`

---

### Task 5: Connect dispatch, model seed và no-side-effect admission

**Files:**
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_connect.go`
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_connect_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_connect.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_connect_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`

**Public behavior:** Handler resolve kind-bound driver trước mkdir; generic manager giữ one-flight/cancel;
model seed thành công trước bind; fake driver đi qua real manager dưới `t.TempDir`.

- [ ] **Step 1: Commit RED tests**

Tests qua runtime context:

```go
func TestRuntimeConnectRejectsMissingDriverBeforeFilesystemMutation(t *testing.T)
func TestRuntimeConnectSeedsModelsBeforeBindingOnboarding(t *testing.T)
func TestRuntimeConnectSeedFailureCleansAccountWithoutAdvancing(t *testing.T)
func TestRuntimeConnectSyntheticDriverUsesOnlyOwnedRoot(t *testing.T)
func TestRuntimeConnectProductionCodexClaudeCompatibility(t *testing.T)
```

Instrument order `resolve → mkdir → detect/install/login/poll → ensure provider → seed → bind/publish`.
Seed failure must leave phase Connect/revision unchanged, no Account/model/staging bind and owned config root
cleaned. Two kinds still share exactly one active slot. Unknown/OpenCode produce no root/process/Store write.

- [ ] **Step 2: RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^TestRuntimeConnect'
```

- [ ] **Step 3: Implement driver binding**

Move existing Codex/Claude detect/install/login/poll/label behavior behind typed drivers without changing
argv/env/auth protocol. Resolve registration before `connectManager.start`. Manager takes bound driver and
bound OnboardingStore methods. Replace `cliProviderModels(st, kind, kind)` with registration seeder using
actual provider ID. Ensure exact singleton through Task 3 Store API.

Connect HTTP admission uses context registry, not static `subscriptionKinds`. Preserve lost response,
cancel/wait, privacy and output normalization.

- [ ] **Step 4: GREEN + broader Connect suite**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(RuntimeConnect|ConnectOnboarding|AppLLMConnect)'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
```

- [ ] **Step 5: Commit**

`refactor: dispatch provider connect through runtime registry`

---

### Task 6: Registry-aware Onboarding status, Setup, Persona và pinned Test

**Files:**
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_onboarding.go`
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_runner.go`
- Modify: `appmode/overlay/internal/daemon/app_agent.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`

**Public behavior:** Toàn active flow dùng cùng context/catalog; `/onboarding/status` chỉ project Catalog;
future runtime qua selection→Setup→Persona→fallback Test không rơi production global.

- [ ] **Step 1: Commit RED tests**

Tests:

```go
func TestRuntimeOnboardingStatusProjectsOnlyContextCatalog(t *testing.T)
func TestRuntimeOnboardingSyntheticSelectionSetupPersonaTest(t *testing.T)
func TestRuntimeOnboardingUsesPinnedMemberFactoryInRankOrder(t *testing.T)
func TestRuntimeOnboardingRejectsMissingRegistrationBeforeExternalIO(t *testing.T)
func TestRuntimePersonaBoundaryDoesNotFallBackToDefaultCatalog(t *testing.T)
```

Fixture Catalog 10/50/100. Status excludes API-only registrations and keeps route rank. HTTP selection/begin
uses context OnboardingStore. Setup selector returns `future-model`. Persona save and Test preflight accept
future stages. Fallback order exact; winning response reports actual kind/model/position. Missing/tampered
kind returns stable private error before callback/config cleanup. No global registry mutation.

- [ ] **Step 2: RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^TestRuntimeOnboarding'
```

- [ ] **Step 3: Implement context threading**

Parameterize canonical/support/status/suggestion/snapshot validators, rooted config inspection/cleanup,
selection/begin/Back, Setup, receipt and Test runner through `appRuntimeContext`. `app_agent.go` Persona
handlers dùng `context.onboardingStore()` cho catalog-aware snapshot preflight và post-write response
validation. Các mutation Persona/Restart vốn không đọc/ghi kind (`UpdateAgentPersona`,
`AdvanceOnboardingPersona*`, `InvalidateOnboardingPersona`, `RestartOnboarding`) tiếp tục gọi `*Store`
trực tiếp; CAS/phase guard giữ atomicity. Không thêm wrapper giả chỉ để đổi receiver.

Replace `selectOnboardingModel` descriptor access with registration callback. Replace Test member kind
switch with `NewOnboardingMember`; driver receives exact staged entry and owns config/account wiring.
Fallback/semantic validation/receipt/cancel leases stay unchanged.

- [ ] **Step 4: GREEN + full Onboarding slices**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(RuntimeOnboarding|AppOnboarding|AppAgentPersona)'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
```

- [ ] **Step 5: Commit**

`refactor: route onboarding through provider runtimes`

---

### Task 7: Live routing, readiness và atomic terminal policy

**Files:**
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_live.go`
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_live_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_combos_http.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_api.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_hook.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`

**Public behavior:** Live factory/readiness/local attachment/terminal/structured policy đến từ registry;
terminal luôn cuối trong route thật; fake completed runtime trả lời qua `appZaloRunner`.

- [ ] **Step 1: Commit RED tests**

Tests:

```go
func TestRuntimeLiveSyntheticProviderAnswersThroughAppZaloRunner(t *testing.T)
func TestRuntimeLiveReadinessUsesConnectionModeNotStaticKinds(t *testing.T)
func TestRuntimeLiveRejectsTerminalBeforeLastOnReplaceActivateAndRun(t *testing.T)
func TestRuntimeLiveLegacyRoutePutUsesSameLockAndValidation(t *testing.T)
func TestRuntimeLiveRouteMutationIsAtomicWithComplete(t *testing.T)
func TestRuntimeLiveAttachmentAndStructuredFactoriesAreTyped(t *testing.T)
func TestRuntimeLiveOpenCodeDeniedAtEveryLookup(t *testing.T)
func TestRuntimeLiveProductionZaloHookUsesContextBoundRunner(t *testing.T)
func TestRuntimeLiveHiddenGeminiCLICompatibility(t *testing.T)
```

Readiness semantics giữ nguyên: account mode = enabled Account thuộc exact singleton; credential mode =
credential configured, độc lập `Provider.Enabled`; usability filter riêng. Build completed future route,
invoke entry point `appZaloRunner`, assert fake adapter actual call. Add race orchestration proving replace /
activate / Complete cannot validate route A rồi commit route B. Tampered persisted terminal-first route fails
before external I/O. HTTP adapter cannot claim local attachment.
Hidden Gemini CLI keeps existing adapter/attachment behavior for a valid persisted route, remains absent
from readiness/Onboarding/gallery/connect/add-new, and projects only safe `visible:false`,
`connection_mode=none` management metadata so existing persisted members remain intelligible.

- [ ] **Step 2: RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^TestRuntimeLive'
```

- [ ] **Step 3: Implement live registry dispatch**

Move `newLLMAdapter`, account/config wiring, terminal Claude dispatch, budget successor, round-robin exclusion,
attachment locality and structured-session resolution behind registry factories. Core không type-assert
concrete adapters hoặc so literal Claude kind.

Tạo context-bound runner factory/method dùng chung cho production và tests. `appAnswerZalo` trong
`app_zalo_session_hook.go` phải gọi runner từ `productionAppRuntimeContext(a)` thay vì method đọc static
global; synthetic test gọi cùng factory với local registry. Không thêm registry field vào upstream `api`.

Add `appLLMRouteMutationMu`; combo replace/activate, legacy `PUT /llm/route`, and Complete hold it across
read/validate/write. Complete takes onboarding lock first. The legacy route handler must not call Store
replace outside this fence. Validate terminal last at registry construction, both route mutation APIs,
activation and runner construction. `hasAnyConnectedProvider` becomes registry-aware while
disabled-provider usability remains separate.

- [ ] **Step 4: GREEN + router/combo regression**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(RuntimeLive|AppLLMRouter|AppLLMCombo|CompleteOnboarding)'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
```

- [ ] **Step 5: Commit**

`refactor: dispatch live routes through provider runtimes`

---

### Task 8: Safe runtime metadata và dynamic Providers/Combos Portal

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_api.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_api_shared.go`
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_http_test.go`
- Create: `appmode/overlay/internal/webui/static/core/provider-runtime-catalog.js`
- Modify: `appmode/overlay/internal/webui/static/core/providers-status.js`
- Modify: `appmode/overlay/internal/webui/static/pages/providers.js`
- Create: `appmode/tests/helpers/provider-runtime-fixtures.mjs`
- Create: `appmode/tests/provider-runtime-catalog.test.mjs`
- Modify and split: `appmode/tests/providers.test.mjs`
- Create: `appmode/tests/providers-actions.test.mjs`
- Modify: `appmode/tests/combos.test.mjs`
- Modify: `appmode/tests/providers-status.test.mjs`

**Public behavior:** `/llm/providers` trả all-runtime safe options; Portal render/classify/connect từ server
metadata, không Set tĩnh hoặc false safety badge; malformed projection fail closed.

- [ ] **Step 1: Split baseline tests, then commit behavioral RED**

Đầu tiên split cơ học `providers.test.mjs` thành `providers.test.mjs` và
`providers-actions.test.mjs`, chạy cả hai GREEN và giữ mỗi file <=800. Tạo shared real-shape fixture cho
`/llm/providers`; migrate mọi fixture trong hai Providers files và `combos.test.mjs` để có
`provider_options` + `connection_mode`, compact `combos.test.mjs` nếu cần để vẫn <=800.

Sau baseline GREEN, thêm Go test exact JSON fields/order và privacy. JS public tests cover fake row,
search/group/detail/connect CTA, generic icon, connection mode and execution badge. Add a persisted
`gemini-cli` Provider/model/route fixture: its matching option is `visible:false`, existing Provider and Combo
member render safely, readiness stays false, and gallery/create/connect/add-to-combo omit it. Matrix malformed:
duplicate kind/order, unknown enum, invalid hex/kind, missing option for any returned provider (including a
hidden body), hidden option with mode other than `none`, visible option with mode `none`, and
credential/config/command/env extra fields. Extra private fields bị project strip ở server; client only retains
allowlist and freezes copies.

Không sửa 925-line `combos.js` nếu helper change đã đủ. Nếu thật sự bắt buộc, dừng và extract module cơ
học + GREEN trước behavioral RED thay vì chạm trực tiếp file >800.

- [ ] **Step 2: RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^TestRuntimeProviderHTTP'
node --test appmode/tests/provider-runtime-catalog.test.mjs appmode/tests/providers-status.test.mjs appmode/tests/providers.test.mjs appmode/tests/providers-actions.test.mjs appmode/tests/combos.test.mjs
```

- [ ] **Step 3: Implement HTTP projection and strict JS normalizer**

`GET /llm/providers` giữ `providers`/`hasConnectedProvider`, thêm option cho mọi runtime không-denied theo
`ui_order`, kể cả safe hidden `gemini-cli` option `visible:false`, `connection_mode=none`. Mỗi returned provider
body phải có đúng matching option; `llmProviderBody` thêm `connection_mode`; execution/risk chỉ ở safe option.
Onboarding response vẫn chỉ dùng `registry.Catalog().Options()` với route rank.

Extract strict normalizer module <=800 lines. `providers.js` bỏ `PROVIDER_CATALOG`, `CONNECTABLE_KINDS`,
`PROXY_KINDS`; render/gating/badge dùng normalized server enums. `visible:false` không được vào gallery,
create/connect CTA hay combo picker, nhưng existing Provider/model/Combo member vẫn render bằng matching safe
option. `isProviderConnected(provider)` đọc `connection_mode`: account→enabled Account,
credential→configured credential, không phụ thuộc Provider enabled; `none` luôn false. Combos tiếp tục gọi
helper và không cần hard-code kind.

- [ ] **Step 4: GREEN + line/syntax gates**

Chạy commands trên, `node --check` cho 3 production modules, line count mọi touched JS <=800,
`git diff --check`, rồi full regressions:

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
npm test --prefix appmode
```

- [ ] **Step 5: Commit**

`feat: render providers from runtime metadata`

---

### Task 9: Cross-layer future runtime acceptance, regressions và final evidence

**Files:**
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_e2e_test.go`
- Modify: `appmode/tests/onboarding-ags-setup.test.mjs`

**Public behavior:** Một test-only registration duy nhất đưa `future-cli/future-model` qua toàn chuỗi thật;
production source không hard-code fake; tất cả gates và missing onboarding visuals được kiểm trên exact HEAD.

- [ ] **Step 1: Commit acceptance characterization**

Test dựng local Catalog/registry/context, fake callbacks no process/network/additional filesystem, rồi:

1. GET status có rank 10/50/100;
2. plural select + begin future;
3. real Connect manager tạo owned config under `t.TempDir`, seed future model, bind;
4. Setup future, hoàn tất các selected rows;
5. Persona/Test fallback trả actual winning member;
6. receipt + Complete activate exact ordered bindings;
7. `appZaloRunner` qua readiness gọi fake live adapter và trả answer;
8. production registry/default Catalog không đổi và OpenCode mọi projection/factory deny.

Thêm concurrent instance test (không global swap) và `rg` assertion/hygiene: literal `future-cli` chỉ ở
test registration/fixtures. Sau Tasks 1–8, acceptance test được kỳ vọng GREEN; đây là final
characterization, không bịa một RED mới. Nếu nó fail, dừng, ghi nhận missing seam bằng focused regression,
rồi sửa minimum và amend cùng commit—không thêm fake switch production.

Sửa false-positive mock `onboarding-ags-setup.test.mjs`: `setup(kind, accountId, revision, signal)`, record
args ngoài mock, return pending result, assert không render error.

Commit hai test changes trước khi chạy để overlay harness có tree sạch:

`test: prove provider runtime extension path`

- [ ] **Step 2: Focused acceptance GREEN**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^TestProviderRuntimeFutureEndToEnd$'
node --test appmode/tests/onboarding-ags-setup.test.mjs
```

Nếu có gap thật, thêm focused RED cho gap đó, sửa minimum, amend commit và chạy lại cả hai commands.

- [ ] **Step 3: Focused + full verification**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1
npm test --prefix appmode
pwsh -NoProfile -File .\tests\build-app.Tests.ps1 `
  -UpstreamRepo 'C:\Users\manva\OneDrive\Máy tính\agentdc'
git diff --check
```

Chạy syntax checks và line gates; scan static branding/token/config path. Full tests phải fresh sau final
amend, không dựa kết quả task cũ.

- [ ] **Step 4: Final independent reviews**

Dispatch spec reviewer và code-quality/security reviewer trên toàn range từ plan base. Không ship nếu có
Critical/Important. Mọi review fix phải strict RED→GREEN, amend Task 9 commit, re-run focused/full gates và
re-review tới PASS.

- [ ] **Step 5: Build exact clean HEAD và visual acceptance**

Build ra target mới:

```powershell
$shortSha = (git rev-parse --short=8 HEAD).Trim()
$target = "D:\TuvanZalo\_artifacts\tuvanzalo-provider-runtime-final-$shortSha"
pwsh -NoProfile -File .\build-app.ps1 `
  -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' `
  -Out $target `
  -PersonaSource 'D:\TuvanZalo\brain\reference\persona'
```

Launch exact exe trên 8770 sau khi graceful-stop đúng PID cũ. CDP capture desktop và true 390×844 từ
exact package cho:

- one ready + one pending;
- active Connect + sibling `Đang chờ`;
- Providers page dynamic catalog/generic runtime row (intercept safe response, không auth thật).

Assert `innerWidth=390`, `scrollWidth<=clientWidth`, modal/right border/CTA visible, sidebar hidden, không có
“Bắt đầu kết nối”. Không sửa tracked files sau build; báo package hash/PID/status/module hashes và screenshot
absolute paths trong handoff để artifact luôn đúng final HEAD. Nếu bất kỳ review/fix nào đổi HEAD, rebuild
vào target SHA mới và bỏ target cũ khỏi acceptance.

---

## Completion gates

- [ ] Default production behavior Codex/Claude/API providers unchanged.
- [ ] Synthetic third runtime passes Store + HTTP + Connect + Setup + Persona/Test + Complete +
      `appZaloRunner` without core string switch.
- [ ] `providercatalog`, Store and daemon have no mutable test registry/catalog swap.
- [ ] Terminal cannot precede another member at replace, activate, Complete or live construction.
- [ ] Providers/Combos classification comes from safe server metadata.
- [ ] OpenCode remains absent/denied at constructor and every lookup/projection/factory.
- [ ] Full Go, Portal, build acceptance, visual evidence and final reviews pass.

OpenCode trusted-SYSTEMROOT hardening and failed-smoke evidence are a separate follow-up spec/plan; this
registry plan must not broaden its production reach.
