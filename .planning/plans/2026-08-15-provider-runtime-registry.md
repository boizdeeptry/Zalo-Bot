# Provider runtime registry hợp nhất — implementation plan

> **For automated executors:** dùng `:subagent-driven-development` và thực hiện tuần tự từng task.
> Mỗi task phải qua spec review rồi code-quality review trước task kế tiếp.

**Goal:** Loại bỏ các allowlist/switch Provider rải rác, chứng minh một subscription runtime thứ ba đi
xuyên Onboarding/Complete/live Zalo bằng registration typed, đồng thời thay Persona/Test wizard bằng
bootstrap tự động kiểu AGS và giữ OpenCode deny production.

**TDD mode:** yes

**Architecture:** `providercatalog.Catalog` là value bất biến và được truyền vào
`store.OnboardingStore`; daemon dựng một `appProviderRuntimeRegistry` validated chứa metadata an toàn và
typed behavior drivers. `appRuntimeContext` truyền registry/Store/Connect tường minh cho handler, không
test-swap global. `/onboarding/status` chỉ project Onboarding Catalog; `/llm/providers` project toàn bộ
runtime UI metadata. Terminal/attachment/structured behavior được typed và route thật được validate.
Sau Provider cuối ready, server tự advance Persona đóng gói sẵn, chạy staged fallback Test thật, phát receipt
và Complete; Portal chỉ hiển thị progress, còn Knowledge là tùy chọn sau Done.

**Tech stack:** Go, SQLite, vanilla JavaScript ES modules, Node `node:test`, PowerShell overlay harness.

**Specs:** `.planning/specs/2026-08-15-provider-runtime-registry-design.md`,
`.planning/specs/2026-08-16-ags-style-automatic-onboarding-design.md`

**Research:** `.planning/research/onboarding-multi-provider-opencode-RESEARCH.md`; khảo sát tĩnh AGS/Bé Mầm
được ghi trong spec automatic-onboarding (không có research file riêng).

**Workspace:** `D:/TuvanZalo/_build/.worktrees/portal-m1`, branch `integration/main-memory-v2`.

---

## Execution rules

- Chỉ sửa worktree trên. Tasks 9/15 được đọc read-only exact allowed PersonaSource set dưới
  `D:/TuvanZalo/brain/reference/persona/`; riêng Task 9 được ghi đúng `identity.json` và `roster.md` đã nêu,
  còn Task 15 được ghi đúng SHA-named package/evidence roots dưới `D:/TuvanZalo/_artifacts/` đã pin ở Step 4.
  Không ghi file ngoài worktree nào khác. Không sửa upstream repo
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
- Bootstrap Test lease dùng opaque owner token và promotion atomically dưới
  `onboardingMutationMu -> onboardingTestMu -> appLLMRouteMutationMu`; không cancel/wait chính owner và
  không giữ mutation lock trong model I/O.
- Persona start revision `r` chỉ success ở completed `r+3`; Test start `r` chỉ success ở `r+2`.
  Lost response reconcile bằng GET, không suy stale revision là cùng operation khi thiếu provenance.
- Persona/identity build preflight phải PASS trước khi clear/write `$Out`; ngoại lệ privacy chỉ exact declared
  identity trong exact Persona file set. Knowledge trống không chặn build/onboarding/Done.
- Tất cả JS production/test mới hoặc đã chạm phải `<=800` dòng. Không sửa `combos.js` (925 dòng) nếu
  thay `providers-status.js` đã đủ; nếu bắt buộc thì extract module trước trong RED/GREEN riêng.
- Giữ card Onboarding trên nền lưới và sidebar ẩn, nhưng shell là full-page gate không mang
  `role=dialog/aria-modal`; native `<dialog>.showModal()` xác nhận OFF là modal active duy nhất. Không đưa
  branding sản phẩm tham chiếu vào source.

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

- [x] **Step 1: Commit RED tests**

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

- [x] **Step 2: Chạy RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all -Run '^Test(ProviderCatalog|Catalog|ZeroCatalog|DefaultCatalog)'
```

Expected: thiếu `Catalog/New/Default` hoặc immutability assertion fail; không phải harness lỗi.

- [x] **Step 3: Implement minimum**

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

- [x] **Step 4: GREEN + hygiene**

Chạy focused command rồi full `-Package all`, `gofmt`, `git diff --check`.

- [x] **Step 5: Commit**

`refactor: make provider catalog immutable`

---

### Task 2: Catalog-bound Store selection, inspection và migration

**Files:**
- Create: `appmode/overlay/internal/store/app_onboarding_catalog.go`
- Create: `appmode/overlay/internal/store/app_onboarding_catalog_test.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_providers.go`
- Modify: `appmode/overlay/internal/store/app_onboarding.go`
- Modify: `appmode/overlay/internal/store/app_onboarding_test.go`
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
Migrate fixture legacy đang dùng `provider_kind='openai-compatible'` sang một kind thuộc Default Catalog;
fixture này chỉ kiểm snapshot/concurrency và không được trở thành đường tắt mở rộng catalog mặc định.

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
- Modify: `appmode/overlay/internal/daemon/app_provider_runtime.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_http_test.go`
- Create: `appmode/overlay/internal/webui/static/core/provider-runtime-catalog.js`
- Modify: `appmode/overlay/internal/webui/static/core/providers-status.js`
- Modify: `appmode/overlay/internal/webui/static/components/provider-connect.js`
- Modify: `appmode/overlay/internal/webui/static/pages/providers.js`
- Create: `appmode/tests/helpers/provider-runtime-fixtures.mjs`
- Create: `appmode/tests/provider-runtime-catalog.test.mjs`
- Modify and split: `appmode/tests/providers.test.mjs`
- Create: `appmode/tests/providers-actions.test.mjs`
- Modify: `appmode/tests/combos.test.mjs`
- Modify: `appmode/tests/providers-status.test.mjs`
- Modify: `appmode/tests/provider-connect.test.mjs`
- Create: `appmode/tests/provider-connect-flow.test.mjs`

**Public behavior:** `/llm/providers` trả all-runtime safe options; Portal render/classify/connect từ server
metadata, không Set tĩnh hoặc false safety badge; malformed projection fail closed.

- [ ] **Step 1: Split baseline tests, then commit behavioral RED**

Đầu tiên split cơ học `providers.test.mjs` thành `providers.test.mjs` và
`providers-actions.test.mjs`, đồng thời split 1,178-line `provider-connect.test.mjs` thành
`provider-connect.test.mjs` + `provider-connect-flow.test.mjs`; chạy từng cặp GREEN và giữ mỗi file <=760.
Tạo shared real-shape fixture cho
`/llm/providers`; migrate mọi fixture trong hai Providers files và `combos.test.mjs` để có
`provider_options` + `connection_mode`; chuyển provider fixture lặp sang shared helper cho tới khi
`combos.test.mjs <=760` trước behavioral RED.

Sau baseline GREEN, thêm Go test exact JSON fields/order và privacy. JS public tests cover fake row,
search/group/detail/connect CTA, generic icon, connection mode and execution badge. Add a persisted
`gemini-cli` Provider/model/route fixture: its matching option is `visible:false`, existing Provider and Combo
member render safely, readiness stays false, and gallery/create/connect/add-to-combo omit it. Matrix malformed:
duplicate kind/order, unknown enum, invalid hex/kind, missing option for any returned provider (including a
hidden body), hidden option with mode other than `none`, visible option with mode `none`, and
credential/config/command/env extra fields. Extra private fields bị project strip ở server; client only retains
allowlist and freezes copies.

Add Connect presentation regression for a synthetic browser-only account runtime: `awaiting_login` with no
terminal `code` renders browser-login guidance, while any runtime returning a nonempty `code` renders the
device-code instruction. This branch comes from normalized server status, never a literal Provider kind.
Existing Claude-specific install-help copy may remain scoped to Claude.

Không sửa 925-line `combos.js` nếu helper change đã đủ. Nếu thật sự bắt buộc, dừng và extract module cơ
học + GREEN trước behavioral RED thay vì chạm trực tiếp file >800.

- [ ] **Step 2: RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^TestRuntimeProviderHTTP'
node --test appmode/tests/provider-runtime-catalog.test.mjs appmode/tests/providers-status.test.mjs appmode/tests/provider-connect.test.mjs appmode/tests/provider-connect-flow.test.mjs appmode/tests/providers.test.mjs appmode/tests/providers-actions.test.mjs appmode/tests/combos.test.mjs
```

- [ ] **Step 3: Implement HTTP projection and strict JS normalizer**

`GET /llm/providers` giữ `providers`/`hasConnectedProvider`, thêm option cho mọi runtime không-denied theo
`ui_order`, kể cả safe hidden `gemini-cli` option `visible:false`, `connection_mode=none`. Mỗi returned provider
body phải có đúng matching option; `llmProviderBody` thêm `connection_mode`; execution/risk chỉ ở safe option.
Onboarding response vẫn chỉ dùng `registry.Catalog().Options()` với route rank.

Bind `GET /llm/providers` to `appRuntimeContext.handleLLMProviderList` in the context-aware route registration
created by Task 4. Both provider bodies, safe options and `hasConnectedProvider` must come from that exact
request context; synthetic HTTP tests must not fall back to the production registry or a mutable global.

Do not broaden API-key creation in this plan: production credential registrations remain the exact existing
compiled fixed-endpoint set and the legacy authenticated create handler keeps its behavior. The extension proof
uses `connection_mode=account`; adding a novel credential kind requires a separate reviewed API-key task before
it may be marked visible/creatable.

Extract strict normalizer module <=800 lines. `providers.js` bỏ `PROVIDER_CATALOG`, `CONNECTABLE_KINDS`,
`PROXY_KINDS`; render/gating/badge dùng normalized server enums. `visible:false` không được vào gallery,
create/connect CTA hay combo picker, nhưng existing Provider/model/Combo member vẫn render bằng matching safe
option. `isProviderConnected(provider)` đọc `connection_mode`: account→enabled Account,
credential→configured credential, không phụ thuộc Provider enabled; `none` luôn false. Combos tiếp tục gọi
helper và không cần hard-code kind.

In `provider-connect.js`, derive browser-only versus device-code guidance from the returned normalized `code`
field. Do not branch login protocol on `providerKind`; a future account runtime needs no frontend literal.

- [ ] **Step 4: GREEN + line/syntax gates**

Chạy commands trên; `node --check` exact bốn production modules `provider-runtime-catalog.js`,
`providers-status.js`, `provider-connect.js`, `providers.js`; line count mọi touched JS <=800,
`git diff --check`, rồi full regressions:

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
npm test --prefix appmode
```

- [ ] **Step 5: Commit**

`feat: render providers from runtime metadata`

---

### Task 9: Persona complete, immutable manifest và privacy preflight

**Files:**
- Modify: `scripts/BuildApp.psm1`
- Modify: `build-app.ps1`
- Delete: `genpersona.py`
- Create: `tests/build-app-persona.Tests.ps1`
- Modify: `tests/build-app.Tests.ps1`
- Modify: `launcher/app/run.bat`
- Modify: `launcher/README.txt`
- Prepare build input (outside Git, do not stage):
  `D:/TuvanZalo/brain/reference/persona/identity.json` and
  `D:/TuvanZalo/brain/reference/persona/roster.md`

**Public behavior:** Build rejects incomplete/untrusted Persona before touching output; valid source is copied
byte-exact to working/default roots with a strict immutable manifest; fresh package asks no Persona/Knowledge
questions and preserves every unrelated privacy gate.

- [ ] **Step 1: Commit RED build tests**

Create a standalone PowerShell suite so the existing 1,800-line build test does not grow further. Through
exported `BuildApp.psm1` functions and a temp output sentinel, cover:

- duplicate/unknown/missing identity fields, non-NFC/blank/oversize/duplicate identity values, zero/two
  assistant identities, display-name mismatch and more than eight identities;
- `{{...}}`, unmatched braces or invalid UTF-8 in any present persona/roster/overlay file;
- undeclared identity literal, declared literal missing from Persona set, extra source file, symlink/reparse,
  source drift after snapshot and oversized tree;
- exact source-side `persona.md.goc` is permitted only as a regular bounded operator backup, is never read as
  Persona input/copied/manifested; any other unknown sibling fails;
- every preflight failure leaves the output sentinel byte-identical and source untouched;
- valid source produces identical working/default bytes and deterministic
  `app/defaults/persona/build-manifest.json` with schema v1, per-file SHA-256, tree digest,
  `identity.json` digest (without repeating identity literals) and the exact legacy hash
  `8c6ca4f7aa19096a0619c706286f7cff97452c3c4025c03ab59b083895bbacf2`;
- fresh package contains no `.goc`; a `.goc` is accepted by final scan only at exact working backup path and
  only when bytes equal immutable default Persona;
- declared identity is still rejected in binary/data/log/launcher/JS or any non-Persona path; existing
  token/credential/session/customer/config canaries remain rejected.

Also add a static assertion that `build-app.ps1` performs Persona snapshot preflight before the first
`Clear-AppOutput` and no longer invokes `genpersona.py`.
Migrate existing `Assert-AppPackage` README fixtures/order assertions in `build-app.Tests.ps1` from the manual
Portal→Providers→Combos→Zalo sequence to the approved Start→Provider→automatic bootstrap→Portal/Zalo flow;
the real launcher README and synthetic fixture must be checked by the same validator.

- [ ] **Step 2: Run RED**

```powershell
pwsh -NoProfile -File .\tests\build-app-persona.Tests.ps1
```

Expected: missing `Read-AppPersonaSourceSnapshot`, `Write-AppPersonaPackage` and scoped privacy policy, or
the old genpersona invocation assertion fails. A changed sentinel/source is a test failure, not valid RED.

- [ ] **Step 3: Implement immutable snapshot/package**

Add exported module contracts:

```powershell
function Read-AppPersonaSourceSnapshot {
  param([Parameter(Mandatory)][string]$PersonaSource)
  # returns a closed immutable byte snapshot + validated identity + digests
}

function Write-AppPersonaPackage {
  param($Snapshot, [Parameter(Mandatory)][string]$Out)
  # writes working/default roots from Snapshot only, then verifies every digest
}
```

Add both names to the module's explicit `Export-ModuleMember` list so the standalone RED/GREEN suite calls the
same public build implementation as `build-app.ps1`.

Strict-decode `identity.json` as:

```json
{"version":1,"display_name":"boizdeeptry","persona_identities":[
  {"role":"assistant","value":"boizdeeptry"},
  {"role":"expert","value":"Anh Trường"}
]}
```

Write manifest with the exact wire contract consumed in Task 11:

```go
type appPersonaBuildManifest struct {
    Version             int                      `json:"version"` // exact 1
    Files               []appPersonaManifestFile `json:"files"`
    TreeSHA256          string                   `json:"tree_sha256"`
    LegacyPersonaSHA256 []string                 `json:"legacy_persona_sha256"`
}
type appPersonaManifestFile struct {
    Path   string `json:"path"`
    Bytes  uint64 `json:"bytes"`
    SHA256 string `json:"sha256"`
}
```

Actual lengths/digests come from snapshot bytes. Sort `files` by exact slash-relative path. Compute tree hash
as SHA-256 of domain bytes `agentdc/persona-default-tree/v1\0`, then for every file append uint64 big-endian
path length, UTF-8 path, uint64 big-endian content length and raw content. Emit canonical UTF-8/LF without BOM;
manifest itself is outside the tree hash and contains no identity literal.

Run `Read-AppPersonaSourceSnapshot` immediately after path/source cleanliness validation and before any output
clear/write. Remove the mutating `genpersona.py`; write exact snapshot bytes to
`brain/reference/persona/` and `app/defaults/persona/`; emit manifest last and verify destination digests.
Final scan grants literal exceptions only to the exact validated file set and exact `.goc` equality rule.

Using `apply_patch`, create the above `identity.json` in the external build input and replace the single exact
`{{TEN_BOT}}` in its `roster.md` with `boizdeeptry`; do not copy or alter source `persona.md.goc`, and do not
stage external source files. Add
`AGENTDC_ZALO_PERSONA_DEFAULT_DIR=%ROOT%\app\defaults\persona` to launcher env. Rewrite README first-run
order to Start → select/install Provider → automatic “Đang chuẩn bị trợ lý” → Portal/Zalo; remove placeholder
instructions and state that Knowledge is optional after Done while failed setup keeps no-route silence.

- [ ] **Step 4: GREEN + full build gates**

```powershell
pwsh -NoProfile -File .\tests\build-app-persona.Tests.ps1
pwsh -NoProfile -File .\tests\build-app.Tests.ps1 `
  -UpstreamRepo 'C:\Users\manva\OneDrive\Máy tính\agentdc'
```

Verify external source hashes are unchanged except the declared roster/identity edits, `git diff --check`, and
that no tracked/private artifact contains a credential/token/session. Commit only tracked owned files.

- [ ] **Step 5: Commit**

`feat: package a ready onboarding persona`

---

### Task 10: Test-phase automatic bootstrap và owner-safe lease promotion

**Files:**
- Create: `appmode/overlay/internal/daemon/app_onboarding_bootstrap.go`
- Create: `appmode/overlay/internal/daemon/app_onboarding_bootstrap_test.go`
- Create: `appmode/overlay/internal/daemon/app_onboarding_lease_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Modify: `appmode/overlay/internal/daemon/app_foundation_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_back_providers_test.go`

**Public behavior:** `POST /onboarding/bootstrap {revision}` in Test runs the real staged fallback, stores a
real route-bound receipt and atomically Complete at exact `r+2`, without exposing a token or self-deadlocking.

- [ ] **Step 1: Commit RED HTTP/concurrency tests**

Add tests:

```go
func TestOnboardingBootstrapFromTestCompletesAtSecondSuccessor(t *testing.T)
func TestOnboardingBootstrapUsesRealFallbackAndWinningIdentity(t *testing.T)
func TestOnboardingBootstrapCancellationBeforePromotionDoesNotComplete(t *testing.T)
func TestOnboardingBootstrapCompensatesReceiptAfterPromotionFailure(t *testing.T)
func TestOnboardingBootstrapLostResponseLeavesCompletedGETAuthoritative(t *testing.T)
func TestOnboardingBootstrapOneFlightExecutesProviderOnce(t *testing.T)
func TestOnboardingBootstrapStrictRequestAndCompletedNoop(t *testing.T)
func TestOnboardingTestLeasePromotionOwnsCleanup(t *testing.T)
func TestOnboardingTestLeaseRejectsWrongCanceledOrForeignOwner(t *testing.T)
```

Matrix includes first member fail/second pass, all fail sanitized 502, semantic failure, timeout, route/persona/
account/model/config drift at postflight, Complete failpoint rollback, exact receipt replacement, no external
I/O while mutation lock held, raw token absent from response/log/plaintext Store, route auth/cookie registration,
Completed exact-revision no-op and stale revision conflict. Existing Back/Complete must still cancel-and-wait a
foreign normal Test without deadlock.

- [ ] **Step 2: Run RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^Test(OnboardingBootstrap|OnboardingTestLease)'
```

Expected: route/handler/owner lease APIs missing; no timeout/hang is accepted as RED.

- [ ] **Step 3: Implement Test core and lease ownership**

Replace parallel Test globals with server-created pointer identity:

```go
type appOnboardingTestLease struct { /* owner pointer, done, cancel, state */ }
type appOnboardingProviderTransitionLease struct { /* exact owner and sole release */ }

func beginAppOnboardingTest() (*appOnboardingTestLease, bool)
func (l *appOnboardingTestLease) bindCancel(context.CancelFunc) bool
func (l *appOnboardingTestLease) finish()
func promoteAppOnboardingTest(
    ctx context.Context,
    owner *appOnboardingTestLease,
) (*appOnboardingProviderTransitionLease, bool)
```

Promotion is called while holding `onboardingMutationMu`, then locks `onboardingTestMu`, requires exact active
owner/uncanceled context/no cancel request/no foreign transition, and moves ownership without cancel, close or
wait. Stale deferred `finish()` becomes a no-op; promoted transition release is sole closer. Foreign transition
keeps existing cancel/wait semantics.

Extract typed Test execution/postflight/receipt and Complete cores from the existing HTTP handlers; both old
handlers call the cores. Add strict bootstrap request decoding. For Test: admit lease under mutation lock,
capture route/prompt, release lock for provider I/O, reacquire, postflight, promote, acquire
`appLLMRouteMutationMu`, create internal nonce, Save receipt and Complete. On failure after receipt commit, use
bounded exact `ClearOnboardingTestReceipt` compensation; after Complete commit never compensate. Return only
pure committed onboarding status. Persona phase remains a stable 409 until Task 11.

- [ ] **Step 4: GREEN + full daemon**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(OnboardingBootstrap|OnboardingTestLease|AppOnboardingTestChat|CompleteOnboarding|BackToProviders)'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
```

Run gofmt/diff-check; inspect logs/errors for raw answer/token/session/config path.

- [ ] **Step 5: Commit**

`feat: bootstrap onboarding from staged test`

---

### Task 11: Trusted Persona defaults và Persona-to-Completed bootstrap

**Files:**
- Create: `appmode/overlay/internal/daemon/app_persona_defaults.go`
- Create: `appmode/overlay/internal/daemon/app_persona_defaults_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_bootstrap.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_bootstrap_test.go`
- Modify: `appmode/overlay/internal/daemon/app_agent.go`
- Modify: `appmode/overlay/internal/daemon/app_persona.go`
- Modify: `appmode/overlay/internal/daemon/app_agent_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_test.go`
- Modify: `appmode/overlay/internal/daemon/app_onboarding_status_test.go`
- Modify: `appmode/overlay/internal/daemon/app_provider_runtime.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`

**Public behavior:** Fresh Persona phase needs no form: daemon verifies immutable manifest/default, preserves a
complete working Persona, advances once and completes at exact `r+3`; legacy generic is migrated only by exact
hash and every failure leaves live routing/staged ownership safe.

- [ ] **Step 1: Commit RED manifest/Persona tests**

Add tests:

```go
func TestPersonaDefaultsStrictManifestAndTreeDigest(t *testing.T)
func TestPersonaDefaultsRejectWorkingIdentityAsAuthority(t *testing.T)
func TestPackagedPersonaPreservesCompleteOrUserEditedContent(t *testing.T)
func TestPackagedPersonaSeedsMissingAndExactLegacyOnly(t *testing.T)
func TestPackagedPersonaRejectsUnknownHoleAndRecoveryPending(t *testing.T)
func TestOnboardingBootstrapFromPersonaCompletesAtThirdSuccessor(t *testing.T)
func TestOnboardingBootstrapPersonaFailureResumesFromTest(t *testing.T)
func TestOnboardingBootstrapPersonaFailureKeepsOldRouteAndAccounts(t *testing.T)
```

Manifest table rejects duplicate/unknown keys, unsorted/duplicate/case-folded paths, invalid path/hash/tree digest,
`files[].bytes` different from the exact raw byte length, more than eight legacy hashes,
identity digest/default-identity mismatch, reparse/symlink/special/extra/default drift and root outside injected temp.
Fresh path trusts only immutable assistant identity. Existing valid Store display name wins after user edits.
Exact legacy hash migration writes `.goc` from immutable default, never from invalid generic; unknown modified
hole is untouched and returns safe 409. Existing `.goc` equal to the immutable default is accepted idempotently;
mismatched/non-regular/reparse `.goc` fails before any user write and is never overwritten. Persona `r` success
must be exactly `r+3`; probe failure leaves Test `r+1`, disabled staging and old live route.

Migrate shared `newOnboardingRouteTestEnv` to inject a manifest-verified defaults root/context. Update every
legacy `PUT /agent` fixture that relied on no defaults; authoritative-name/fingerprint coverage must expect
`.goc` bytes from immutable default rather than the mutable placeholder working Persona.

- [ ] **Step 2: Run RED**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^Test(PersonaDefaults|PackagedPersona|OnboardingBootstrapFromPersona|OnboardingBootstrapPersona)'
```

- [ ] **Step 3: Implement trusted loader and Persona core**

Add immutable dependency to local runtime context, not a mutable global:

```go
type appPersonaDefaults struct {
    root string
    // parsed immutable manifest and validated byte snapshots
}

func loadAppPersonaDefaults(root string) (appPersonaDefaults, error)
func (c appRuntimeContext) completePackagedOnboardingPersona(
    ctx context.Context,
    expectedRevision int64,
) (store.OnboardingState, error)
```

Production context reads exact `AGENTDC_ZALO_PERSONA_DEFAULT_DIR`; tests inject `t.TempDir`. Loader strict-parses
schema v1, rejects duplicate/case-folded/unknown keys, requires the exact canonical sorted allowed-file list,
opens rooted exact files, validates every declared byte length, per-file/tree SHA-256 and canonical display identity.
It never trusts writable working identity or infers a name from prose.

Persona core runs under mutation lock: validate catalog snapshot/revision/Persona phase, resolve recovery,
read and analyze working Persona, seed only missing/exact legacy through a private temp file, file fsync,
atomic replace and destination re-read/digest before advancing, validate no holes,
choose valid committed Store display name else immutable display name, fingerprint exact bytes, then call
`AdvanceOnboardingPersona[/WithRecovery]`. Extend bootstrap to call this core then the Task 10 Test core.
Completed projection is pure from committed result; Persona start accepts only `r+3`, Test remains `r+2`.

Route every retained Persona mutation path—`PUT /agent/persona/{name}` and both legacy `PUT /agent` branches—
through the same immutable defaults dependency. On the first `persona.md` edit, create exact working
`persona.md.goc` from the manifest-verified immutable default before writing user bytes; never seed it from the
mutable working file. Preserve one-write backup/recovery fencing, and add public HTTP regressions for all three
paths with an already user-edited working Persona plus first edit. Before any write, rooted inspection must
accept an existing backup only when its bytes/digest equal immutable default and reject mismatch, symlink,
reparse or special file without overwriting it. Task 12 may remove the browser caller, but the authenticated
legacy route stays safe until a separate compatibility decision retires it.

- [ ] **Step 4: GREEN + full daemon/build contract**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(PersonaDefaults|PackagedPersona|OnboardingBootstrap|AppAgentPersona)'
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon
pwsh -NoProfile -File .\tests\build-app-persona.Tests.ps1
```

Gofmt/diff-check; verify no Persona bytes/identity/token/path appear in response or log.
Task-level quality review must explicitly audit duplicate-key/case-fold parsing, rooted file identity, digest
framing, backup TOCTOU and fail-before-write behavior before Task 12 starts.

- [ ] **Step 5: Commit**

`feat: complete packaged persona automatically`

---

### Task 12: Portal automatic bootstrap, không Persona/Test wizard

**Files:**
- Create: `appmode/overlay/internal/webui/static/pages/onboarding-bootstrap.js`
- Create: `appmode/tests/onboarding-bootstrap.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding-contract.js`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding.js`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding-late-view.js`
- Modify: `appmode/tests/onboarding.test.mjs`
- Modify: `appmode/tests/onboarding-recovery.test.mjs`
- Modify: `appmode/tests/onboarding-regressions.test.mjs`
- Modify: `appmode/tests/onboarding-early.test.mjs`
- Modify: `appmode/tests/onboarding-multi-provider.test.mjs`
- Modify: `appmode/tests/onboarding-provider-one-click.test.mjs`
- Modify: `appmode/tests/onboarding-ags-setup.test.mjs`

**Public behavior:** Persona/Test/Complete controls disappear; Persona/Test status starts bootstrap once per
mount and shows only one automatic-progress/recovery surface. No raw Test token crosses the browser.

- [ ] **Step 1: Retire obsolete baselines, then commit bootstrap RED**

Delete Persona form/Test token/manual Complete expectations removed by the approved spec; move reusable
Done/recovery assertions into `onboarding-bootstrap.test.mjs`. Migrate every onboarding fixture/service contract
from `saveAgent/testChat/complete` to `bootstrap`, including early/multi/one-click/AGS suites, before removing
the production methods. Run the affected old suites GREEN before new behavior and keep every touched test file
below 760 lines.

Then add public tests:

- service sends strict `{revision}` to `/onboarding/bootstrap`; Persona accepts only Completed `r+3`, Test only
  `r+2`; wrong phase/lifecycle/catalog/private fields fail closed;
- one automatic attempt per mount; receipt/Complete failure that advances revision never creates a loop;
- explicit Retry uses the authoritative current revision; ambiguous/lost response GET accepts exact Completed,
  while partial Test shows Retry instead of silently running again;
- abort/dispose fences every mutation and reconciliation await;
- Persona/Test render only `Đang chuẩn bị trợ lý…`, safe error, `Thử lại`, `Quay lại Provider`; no Persona
  fields, test input, token or Complete button; Done keeps `Vào Portal` and optional Knowledge CTA.

- [ ] **Step 2: Run RED**

```powershell
node --test `
  appmode/tests/onboarding-bootstrap.test.mjs `
  appmode/tests/onboarding.test.mjs `
  appmode/tests/onboarding-recovery.test.mjs `
  appmode/tests/onboarding-regressions.test.mjs `
  appmode/tests/onboarding-early.test.mjs `
  appmode/tests/onboarding-multi-provider.test.mjs `
  appmode/tests/onboarding-provider-one-click.test.mjs `
  appmode/tests/onboarding-ags-setup.test.mjs
```

Expected: bootstrap service/orchestration missing or obsolete controls remain; fixture/syntax errors are not RED.

- [ ] **Step 3: Implement strict reconciliation and progress UI**

Create:

```js
export async function runOnboardingBootstrap({ snapshot, signal, bootstrap, loadStatus }) {
  // returns { kind: "completed"|"partial"|"failed", state }
}
```

Validate exact successor by starting phase, fence Abort after every await and use GET only for an ambiguous
result. Do not auto-retry a changed Test revision in the same mount; explicit Retry/reload owns the next attempt.
Add `service.bootstrap(revision, signal)` and remove browser `saveAgent/testChat/complete`, raw-token and timer
surfaces from onboarding orchestration. `loadAgent` remains only to label Done when status lacks a name. Extract
logic so `onboarding.js` stays <=800 and late view contains progress/error/Done only.

- [ ] **Step 4: GREEN + full Portal/line gates**

Run the focused command, then:

```powershell
node --check appmode/overlay/internal/webui/static/pages/onboarding.js
node --check appmode/overlay/internal/webui/static/pages/onboarding-bootstrap.js
npm test --prefix appmode
```

All touched/new JS files <=800; diff-check and static scan find no raw token/session/config path.

- [ ] **Step 5: Commit**

`feat: bootstrap onboarding automatically`

---

### Task 13: Native OFF modal dùng chung trong Onboarding

**Files:**
- Create: `appmode/overlay/internal/webui/static/components/provider-off-dialog.js`
- Create: `appmode/tests/provider-off-dialog.test.mjs`
- Modify: `appmode/tests/helpers/dom-harness.mjs`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding.js`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js`
- Modify: `appmode/overlay/internal/webui/static/portal.css`
- Modify: `appmode/tests/onboarding-provider-one-click.test.mjs`
- Modify: `appmode/tests/onboarding-early.test.mjs`
- Modify: `appmode/tests/onboarding-multi-provider.test.mjs`
- Modify: `appmode/tests/onboarding-ags-setup.test.mjs`
- Modify: `appmode/tests/shell.test.mjs`

**Public behavior:** Both Providers can remain ON independently. A user-initiated OFF opens exactly one native
modal and performs no mutation until confirm succeeds; the onboarding shell itself is not a modal.

- [ ] **Step 1: Add dialog harness baseline, then commit modal RED**

Teach the DOM harness real `HTMLDialogElement` behavior: `showModal()` sets `open`, `close()` clears it,
`cancel` is preventable and focus/return-focus are observable. Run existing onboarding suites GREEN before RED.

Add public tests:

- pending OFF immediately restores the visual ON state and opens exactly one
  `dialog[open][role=alertdialog][aria-modal=true]` with Hủy focused;
- Escape/Hủy sends zero API calls, closes the dialog and restores focus to the initiating switch;
- confirm locks the same dialog and waits for authoritative mutation/reconciliation; it closes only on success;
- failure leaves the same dialog open with live error/Retry; double click creates no second dialog/request;
- disposing while pending does not apply stale results and returns focus only when the target still exists;
- onboarding shell/full-page card has no `role=dialog` or `aria-modal`; the OFF dialog is the sole modal;
- dialog has exact `aria-labelledby`/`aria-describedby`, error/status uses `aria-live`, and keyboard focus has a
  visible focus indicator at desktop and 390px;
- ready rows remain locked with the Provider/Combo management hint.

- [ ] **Step 2: Run RED**

```powershell
node --test `
  appmode/tests/provider-off-dialog.test.mjs `
  appmode/tests/onboarding-provider-one-click.test.mjs `
  appmode/tests/onboarding-early.test.mjs `
  appmode/tests/onboarding-multi-provider.test.mjs `
  appmode/tests/onboarding-ags-setup.test.mjs `
  appmode/tests/shell.test.mjs
```

Expected: missing native-dialog helper/harness lifecycle or old inline alertdialog/shell modality remains.

- [ ] **Step 3: Implement shared dialog and Onboarding adoption**

Create `createProviderOffDialog({ listen, onConfirm })` around native `<dialog>`/`showModal`; it owns focus,
cancel, busy/error/retry/close/dispose and never invokes mutation before confirm. Wire `onboarding-early-view`
through one async authoritative callback. Remove the old in-page alertdialog, the three-step rail, shell
`role=dialog/aria-modal` and copy exposing Persona/Test/Complete. Add scoped desktop/390×844 modal CSS.

- [ ] **Step 4: GREEN + full Portal gates**

Run the focused command, then:

```powershell
node --check appmode/overlay/internal/webui/static/components/provider-off-dialog.js
node --check appmode/overlay/internal/webui/static/pages/onboarding.js
npm test --prefix appmode
```

Every touched/new JS file <=800; diff-check; exactly one modal helper implementation exists.

- [ ] **Step 5: Commit**

`feat: confirm provider off in a native modal`

---

### Task 14: Provider management switch dùng lại OFF modal

**Files:**
- Modify: `appmode/overlay/internal/webui/static/pages/providers.js`
- Modify: `appmode/tests/providers.test.mjs`
- Modify: `appmode/tests/providers-actions.test.mjs`

**Public behavior:** Providers detail exposes an authoritative enabled switch. ON is direct; OFF reuses the one
shared native modal. System/hidden runtimes remain visible when persisted but cannot be toggled.

- [ ] **Step 1: Commit Provider-management RED**

Using Task 8's split fixtures, add public tests:

- editable Provider ON calls update directly once and reconciles exact server state;
- editable Provider OFF opens the shared modal, preserves enabled UI until confirm and sends no pre-confirm API;
- Hủy/Escape/error/retry/focus behavior is identical to Onboarding and never creates a second modal;
- update preserves existing name/credential semantics; exact server failure leaves enabled state unchanged;
- system/hidden Provider switch is locked with a safe explanation and never dispatches a mutation.

- [ ] **Step 2: Run RED**

```powershell
node --test appmode/tests/providers.test.mjs appmode/tests/providers-actions.test.mjs appmode/tests/provider-off-dialog.test.mjs
```

- [ ] **Step 3: Implement detail switch through the shared helper**

Render the switch from normalized Task 8 runtime metadata. ON calls the existing safe update service directly;
OFF passes the exact Provider/current focus target and authoritative callback to `createProviderOffDialog`.
Do not duplicate dialog markup/state or infer editability from literal kind names. Hidden/system rows render a
locked explanation and remain excluded from gallery/connect/readiness.

- [ ] **Step 4: GREEN + full Portal**

```powershell
node --check appmode/overlay/internal/webui/static/pages/providers.js
node --test appmode/tests/providers.test.mjs appmode/tests/providers-actions.test.mjs appmode/tests/provider-off-dialog.test.mjs
npm test --prefix appmode
```

Line/diff/static privacy gates; no touched JS >800.

- [ ] **Step 5: Commit**

`feat: manage provider availability safely`

---

### Task 15: Cross-layer runtime/bootstrap acceptance, reviews và exact visual evidence

**Files:**
- Create: `appmode/overlay/internal/daemon/app_provider_runtime_e2e_test.go`
- Modify: `appmode/tests/onboarding-ags-setup.test.mjs`

**Public behavior:** One local synthetic registration crosses selection/Connect/Setup/automatic bootstrap/
Complete/live Zalo with no core switch; exact clean package proves the no-three-step UI and native OFF modal.

- [ ] **Step 1: Commit expected-GREEN acceptance characterization**

Test local Catalog/registry/context with `future-cli/future-model`, no process/network/unowned filesystem:

1. status rank 10/50/100;
2. plural select + begin future;
3. real Connect manager creates only owned temp config, seeds model and binds;
4. Setup completes selected rows and enters Persona revision `r`;
5. single `POST /onboarding/bootstrap` runs packaged Persona, real ordered fallback/receipt and reaches
   Completed `r+3` with actual winner;
6. `appZaloRunner` readiness calls fake live adapter;
7. a Test-resume fixture completes `r+2`;
8. production registry/default Catalog unchanged; OpenCode denied at every projection/factory.

Add concurrent local registries (no global swap), literal hygiene, no raw token response/persistence/log and
old route/account unchanged under injected bootstrap failure. Fix the existing AGS setup mock to record exact
`setup(kind, accountId, revision, signal)` outside the mock and stop swallowing its own assertion. After Tasks
1–14 this test is expected GREEN. If it exposes a real production seam, stop Task 15 and write a focused
corrective task with explicit owned files, public RED/GREEN and its own commit/reviews; only then restart Task 15
from its focused acceptance command. Do not patch arbitrary production files into this characterization commit.

Commit before running overlay harness:

`test: prove automatic provider runtime onboarding`

- [ ] **Step 2: Focused and full verification**

```powershell
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run '^TestProviderRuntimeFutureEndToEnd$'
node --test appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-bootstrap.test.mjs appmode/tests/provider-off-dialog.test.mjs
pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1
npm test --prefix appmode
pwsh -NoProfile -File .\tests\build-app-persona.Tests.ps1
pwsh -NoProfile -File .\tests\build-app.Tests.ps1 `
  -UpstreamRepo 'C:\Users\manva\OneDrive\Máy tính\agentdc'
git diff --check
```

Run syntax/line/privacy/branding scans fresh after the final amend; no repeated blind reruns of unrelated
timing flakes.

- [ ] **Step 3: Independent reviews**

Dispatch spec and code-quality/security reviewers over the full plan range. No Critical/Important may remain.
An accepted finding is a hard stop: define a new focused corrective task/commit with named ownership and public
RED→GREEN, review it independently, then restart Task 15 verification and final review. Do not amend unrelated
production fixes into the expected-GREEN acceptance commit.

- [ ] **Step 4: Build exact clean HEAD**

Preflight external `identity.json` and filled roster, then:

```powershell
$shortSha = (git rev-parse --short=8 HEAD).Trim()
$target = "D:\TuvanZalo\_artifacts\tuvanzalo-runtime-auto-final-$shortSha"
$evidence = "D:\TuvanZalo\_artifacts\tuvanzalo-runtime-auto-final-$shortSha-evidence"
$runtime = Join-Path $evidence 'runtime'
pwsh -NoProfile -File .\build-app.ps1 `
  -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' `
  -Out $target `
  -PersonaSource 'D:\TuvanZalo\brain\reference\persona'
```

Verify manifest/default/working digests, no fresh `.goc`, privacy gates and module hashes, including exact
`provider-runtime-catalog.js`, `onboarding-bootstrap.js`, `provider-off-dialog.js`, `onboarding.js` and
`providers.js`. Record a deterministic
immutable-file manifest for `$target`, then copy it byte-identical to fresh `$runtime` and verify the copy before
launch. Do not modify `$target` or tracked files after this build; any later fix requires a new SHA target.

- [ ] **Step 5: Desktop + true 390×844 visual acceptance**

Launch only the verified byte-identical `$runtime` copy on 8770 after graceful-stop of the exact prior PID.
Runtime writes are allowed only under `$runtime/data/**`; after the journeys, re-hash every copied immutable
file and require it to equal `$target`. Capture both widths for:

- both Providers ON;
- active Connect + sibling `Đang chờ`;
- automatic `Đang chuẩn bị trợ lý…` progress;
- pending OFF native modal with Hủy focused;
- Done with Portal + optional Knowledge CTAs;
- Providers dynamic generic row and Provider OFF modal using intercepted safe response, no auth.

Write screenshots/manifest only under the exact `$evidence` sibling root. Report both pristine `$target` hashes
and runtime-copy pre/post immutable hashes; never mutate `$target` after its package hashes are recorded.

Assert `innerWidth=390`, `scrollWidth<=clientWidth`, no Persona/Test/Complete controls, no “Bắt đầu kết nối”,
exactly one open `dialog[aria-modal=true]` only during OFF, Escape makes zero mutation and restores focus.
Report package SHA/hash/PID/status/module hashes and absolute screenshot paths.

---

## Completion gates

- [ ] Default production behavior Codex/Claude/API providers unchanged.
- [ ] Synthetic third runtime passes Store + HTTP + Connect + Setup + automatic Persona/Test/Complete +
      `appZaloRunner` without core string switch.
- [ ] `providercatalog`, Store and daemon have no mutable test registry/catalog swap.
- [ ] Terminal cannot precede another member at replace, activate, Complete or live construction.
- [ ] Providers/Combos classification comes from safe server metadata.
- [ ] Persona source is complete/manifest-bound; invalid source cannot mutate output; Knowledge is optional.
- [ ] Portal exposes no Persona/Test/Complete wizard and never transports a raw Test token.
- [ ] Every user Provider OFF has exactly one native confirmation modal and no pre-confirm mutation.
- [ ] OpenCode remains absent/denied at constructor and every lookup/projection/factory.
- [ ] Full Go, Portal, Persona/build acceptance, exact visual evidence and final reviews pass.

OpenCode trusted-SYSTEMROOT hardening and failed-smoke evidence remain a separate follow-up; this plan must not
broaden its production reach.
