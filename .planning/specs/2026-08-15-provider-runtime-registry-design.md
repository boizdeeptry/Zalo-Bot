# Registry runtime Provider hợp nhất và mở rộng end-to-end

## 1. Bối cảnh

Onboarding V8 hiện đã lưu một tập Provider theo hàng, cài tuần tự, Test Chat theo fallback và
Complete thành Combo nhiều member. Portal onboarding cũng render từ catalog server thay vì hard-code
hai hàng. Audit cuối vẫn tìm thấy một khoảng trống so với mục tiêu “thêm runtime/model mới mà không
đổi frontend/schema”:

- daemon chỉ có map capability Boolean, còn Connect, model selection, Test Chat và live route tự
  phân nhánh theo `codex`/`claude-code`;
- Store mutation vẫn dùng catalog tĩnh để quyết định kind hợp lệ;
- trang Providers/Combos ngoài wizard còn suy ra subscription kind từ Set tĩnh;
- Claude là terminal route nhưng invariant “terminal phải cuối” chưa được registry kiểm chứng.

Vì người dùng đã uỷ quyền tự chọn phương án tốt nhất và không chờ thêm câu hỏi, tài liệu này chốt
phần corrective theo tiêu chí nghiệm thu đã duyệt của spec multi-provider. Đây không phải mở thêm
OpenCode production: OpenCode vẫn bị deny độc lập cho tới khi có OS sandbox và smoke synthetic thành
công.

## 2. Mục tiêu

1. Một runtime production chỉ được quảng bá khi có đủ metadata và driver cho mọi capability mà nó
   công bố.
2. Thêm một subscription runtime dùng route adapter chuẩn chỉ cần thêm một data-only catalog option và
   một registration/driver hoàn chỉnh; không sửa Store schema, frontend onboarding, Providers, Combos
   hoặc switch trong core flow.
3. Store lưu kind/model opaque nhưng mọi mutation/inspection Onboarding đều nhận cùng một catalog
   bất biến đã được daemon registry validate; Store không hard-code hai kind và cũng không chấp nhận
   chuỗi tuỳ ý.
4. Connect, chọn model Setup và Test Chat dispatch qua callback của registry, không switch theo kind.
5. Live router nhận chính sách terminal/attachment/structured-session từ registry. Terminal runtime
   phải có rank cao nhất; catalog sai bị từ chối trước khi phục vụ.
6. `/llm/providers` trả catalog UI an toàn từ server và mỗi Provider trả connection mode; Providers và
   Combos không còn Set subscription tĩnh.
7. Một fake third runtime phải đi qua selection → begin → bind/setup → Test fallback → Complete/live
   route trong test mà không thay code core.
8. Giữ nguyên mọi ranh giới privacy: không trả credential, config dir, command, env hoặc callback qua
   HTTP.

## 3. Ngoài phạm vi

- Không quảng bá hoặc đưa OpenCode vào routing thật.
- Không thiết kế sandbox/credential OAuth cho OpenCode.
- Không cho người dùng nhập endpoint tuỳ ý.
- Không thay đổi schema V8 hoặc thứ tự fallback đã persist.
- Không biến registry thành plugin nạp DLL/script từ ngoài; đây là registry Go compile-time, được
  validate tập trung.
- Không thay logic provider API-key hiện có ngoài việc đưa metadata trình bày/connection mode ra một
  projection chung.

## 4. Phương án đã cân nhắc

### A. Giữ capability map và thêm vài switch

Ít diff nhất nhưng một provider mới vẫn phải sửa 5–7 file core và dễ được quảng bá trước khi có runner.
Không đạt mục tiêu mở rộng.

### B. Registry declarative chỉ chứa enum route/auth

Giảm một phần hard-code nhưng core vẫn switch theo enum; thêm loại runtime mới lại sửa core. Phù hợp
nếu chỉ bao giờ có Codex/Claude, trái với mục tiêu hiện tại.

### C. Registry entry mang callback driver đã typed — chọn

Mỗi entry chứa metadata an toàn và callback nội bộ cho Connect, model selection, Test member và route
adapter/terminal behavior. Constructor validate dependency graph trước khi registry được publish. Core
chỉ gọi interface/callback. Tradeoff là refactor nhiều file, nhưng đây là phương án duy nhất chứng minh
được third runtime end-to-end mà không thêm switch.

## 5. Kiến trúc

### 5.1. Data-only catalog và operational registry

`providercatalog` tiếp tục sở hữu type metadata an toàn và default options để migration V7 biết Codex/
Claude. Nó thêm một pure `Catalog` validator/canonicalizer. `New(options)` copy input, kiểm kind/rank/
metadata, sắp xếp route rank và giữ slice/map ở field private; mọi accessor trả defensive copy.
`Default()` trả catalog production bất biến. Các package function cũ chỉ là compatibility wrapper gọi
`Default()` và không có mutable package-global swap cho test. Zero-value/invalid Catalog fail closed.
Route rank là durable fallback-order contract; đổi rank production cần migration tường minh, không được
coi là sửa metadata vô hại.

Daemon tạo `appProviderRuntimeRegistry` từ một `providercatalog.Catalog` Onboarding và các
`appProviderRuntimeRegistration` compile-time. Registry bao phủ cả subscription runtime lẫn API-key
runtime; chỉ registration có đầy đủ Onboarding capability mới phải khớp một option trong Catalog.
Mỗi entry gồm metadata an toàn và behavior seam package-private:

```go
type appProviderRuntimeRegistration struct {
    Metadata               appProviderMetadata
    Connect                appConnectDriverFactory
    SeedConnectedModels    appConnectedModelSeeder
    SelectOnboardingModel  appOnboardingModelSelector
    NewOnboardingMember    appOnboardingMemberFactory
    NewAdapter             appProviderAdapterFactory
    NewLocalAttachmentRun  appLocalAttachmentRunnerFactory
    Terminal               appTerminalRouteRunner
    NewStructuredSession   appStructuredSessionRunnerFactory
}
```

Callback types là package-private. Metadata HTTP là một projection mới, không serialize registration.
Constructor thực hiện:

- kind/display/description/UI order unique, bounded và canonical; theme là hex cố định; kind là ASCII
  path-safe segment;
- mọi registration không-denied có safe management metadata và `visible`; visible registration dùng
  `connection_mode=account|credential`, còn hidden live-only bắt buộc `visible=false`,
  `connection_mode=none`, compiled behavior và không góp readiness/selectability;
- mọi option advertised trong Onboarding Catalog phải có đúng một registration khớp kind/display/rank;
  registration credential-only có thể không nằm trong Catalog;
- Onboarding runtime chỉ được quảng bá khi Connect + connected-model seeder + model selector + member
  factory + một live route path đều tồn tại;
- `connection_mode=account` bắt buộc driver/account strategy; `credential` bắt buộc fixed-endpoint
  compiled adapter, không nhận URL tuỳ ý;
- đúng một live route policy chuẩn hoặc terminal; attachment và structured-session chỉ xuất hiện qua
  typed factory tương ứng, không phải Boolean tự khai; structured-session chỉ hợp lệ cùng terminal;
- trong Onboarding Catalog, mọi terminal entry phải đứng sau tất cả non-terminal entry theo route rank;
- `opencode` luôn bị `appProviderProductionDenied` ở constructor lẫn mọi lookup/projection/factory,
  bất kể registration đưa vào test.

Registry production được dựng một lần từ các API-key runtime hiện có, Codex, Claude và một registration
hidden/non-Onboarding cho `gemini-cli` để giữ live-adapter/attachment compatibility hiện tại. Hidden
Gemini CLI không xuất hiện trong Onboarding, gallery/CTA/readiness, không có Connect; nó chỉ chạy khi một
persisted route hợp lệ đã tham chiếu nó. `/llm/providers` vẫn project safe option `visible:false` để Portal
hiểu Provider/model/Combo cũ mà không biến nó thành selectable. Tests dựng
registry riêng có fake third runtime; không mutate production global trong parallel tests. Một value
context giữ dependency tường minh mà không sửa upstream `api` struct:

```go
type appRuntimeContext struct {
    api      *api
    registry appProviderRuntimeRegistry
    connect  *connectManager
}

func (c appRuntimeContext) onboardingStore() store.OnboardingStore {
    return c.api.st.Onboarding(c.registry.Catalog())
}
```

`registerAppRoutes` chỉ dựng production context/wrapper; route helpers và tests nhận context. Connect
manager được dựng với cùng registry/context và không đọc production global.

### 5.2. Store dùng catalog bất biến, không dùng global

Không sửa `Store` struct của upstream và không dùng `sync.Map`/mutable package global. Overlay thêm một
value wrapper:

```go
type OnboardingStore struct {
    store   *Store
    catalog providercatalog.Catalog
}

func (s *Store) Onboarding(catalog providercatalog.Catalog) OnboardingStore
```

Mọi API V8 liên quan `OnboardingState`, `OnboardingSnapshot`, `OnboardingStagingAccount`, selection,
begin, ensure, bind, setup, Back, Test route/Save/Clear receipt và Complete có method trên
`OnboardingStore`; Save/Clear re-read route bằng đúng Catalog sau external I/O. Helper nội bộ như stage
inspection, lost-response validator, completion binding và Test route nhận catalog tường minh. Raw
snapshot trong transaction có thể được đọc trước mutation để sửa vị trí cũ, nhưng public inspection
phải validate bằng catalog trước khi trả. Các method cùng tên hiện có trên
`*Store` chỉ là compatibility wrapper dùng `providercatalog.Default()`. Daemon production luôn tạo
wrapper từ chính catalog nằm trong operational registry; tests fake-third cũng truyền cùng catalog đó
xuyên toàn flow.

Store kiểm:

- kind tồn tại và advertised trong catalog được truyền vào, semantic, bounded, unique;
- catalog route-rank order trở thành persisted position; request order bị bỏ qua như contract hiện tại;
- status/identity/account/model ownership đúng;
- receipt/fingerprint/CAS/rollback giữ nguyên.

V7 migration chỉ backfill singleton kind tồn tại/advertised trong `Default()`; unknown/corrupt kind fail
closed và có migration regression test. Legacy exported helpers
chỉ phục vụ compatibility và phải gọi pure default catalog; active V8 flow không hard-code Set. Catalog
không làm runtime executable: mọi side effect daemon vẫn bắt buộc lookup registration/callback tương
ứng. Vì vậy cả hai lớp fail closed độc lập: Store từ chối kind ngoài catalog, daemon từ chối catalog
entry thiếu runtime binding.

`IsOnboardingProviderKind` được giữ duy nhất như compatibility wrapper qua `Default()`; active V8 flow
không gọi nó.

### 5.3. Connect driver

`connectManager` giữ state machine, one-flight, cancellation, cleanup và persistence như hiện tại.
Handler resolve registration/driver trước khi `connectManager.start` tạo account/config directory. Manager
nhận driver đã bind kind; `defaultConnectRunner` không switch kind nữa và chỉ forward:

```go
type appConnectDriver interface {
    Detect() (bool, error)
    Install(context.Context, func(string)) error
    Login(context.Context, string) (url, code string, wait func() error, err error)
    PollAuth(string) authState
    AccountLabel(string) string
}
```

Codex/Claude implement bằng cách bọc logic đã test hiện có. Không đổi protocol OAuth, command, config
root hoặc cleanup. Sau xác thực nhưng trước `BindOnboardingAccount`, registration
`SeedConnectedModels` seed/discover model cho đúng `providerID`; core không gọi
`cliProviderModels(kind, kind)`. Nếu seeding lỗi, Connect cleanup Account/config và phase chưa chuyển sang
Setup. Unknown/missing driver fail trước khi tạo config/account. Store có
generic account-runtime ensure nhận `(kind, displayName)` đã được registry validate. Nó chỉ chấp nhận
singleton Provider exact `ID == kind && Kind == kind`, giữ nguyên enabled state, và fail closed nếu exact
ID mang kind khác hoặc tồn tại bất kỳ sibling Provider row cùng account-mode kind. Helper subscription
cũ chỉ delegate default metadata.

### 5.4. Setup và Test Chat

Model selector là callback registration. Default selector dùng primary + seeds của CLI descriptor,
nhưng core Setup không đọc `cliDescriptors`.

Onboarding Test runner dựng member bằng `NewOnboardingMember` của registration. Vòng fallback,
semantic answer check, cancellation, staged fingerprint và receipt không đổi. Missing registration
hoặc kind khác snapshot fail trước external I/O.

Mọi helper nhạy registry—canonicalization, status projection, snapshot validation, suggestion, rooted
config validation/cleanup, Connect admission, Setup và Test member construction—nhận
`appRuntimeContext`/registry tường minh. Không helper active-flow nào đọc production global giữa chừng.
Persona handlers trong `app_agent.go` cũng dùng services-aware snapshot/validator, để fake runtime không
rơi lại default Catalog giữa Persona và Test.

### 5.5. Live route

Non-terminal runtime trả `providerAdapter` qua `NewAdapter` và đi qua vòng fallback/telemetry chuẩn.
Driver tự thực hiện account/config wiring; core không type-assert `*cliAdapter`/`*codexProxyAdapter`.
Runtime có attachment dùng typed local factory riêng; một HTTP adapter không thể bật quyền đọc file bằng
Boolean. Terminal runtime cung cấp callback nhận `appLLMRunner`, route entry và input; Claude registration
gọi `runClaude`. Core dùng registry để:

- tìm structured-session terminal entry;
- quyết định entry đọc attachment;
- giữ terminal ngoài round-robin;
- tìm terminal successor khi API budget hết;
- dispatch terminal mà không so chuỗi `claude-code`.

Readiness gate trước router cũng dùng registry nhưng giữ nguyên connection-status contract:
`connection_mode=account` cần ít nhất một Account enabled thuộc exact singleton Provider; `credential`
cần credential configured, độc lập với `Provider.Enabled`. Router/Combo vẫn lọc Provider disabled ở bước
usability riêng. `appZaloRunner` không đọc static
`subscriptionKinds`, nên runtime mới không bị giữ im trước khi adapter factory được gọi.
`appAnswerZalo` trong session hook lấy runner từ `productionAppRuntimeContext(a)`; synthetic tests gọi cùng
context-bound runner factory với local registry. Không thêm registry field vào upstream `api` và không có
mutable global swap.

Registry validation cấm rank catalog sau terminal. Ngoài ra combo replace, combo activation và live-runner
construction đều validate route thực tế: terminal member phải là member cuối; unknown/tampered kind fail
closed. Vì vậy manual reorder hay DB cũ cũng không tạo fallback không thể tới.

Một `appLLMRouteMutationMu` serialize mọi daemon mutation có thể đổi route/Combo: replace member, activate
Combo và Complete Onboarding. Với Complete, thứ tự lock cố định là Onboarding mutation lock trước, route
mutation lock sau; không code nào lấy theo chiều ngược. Handler giữ lock qua read → registry validation →
Store write. Live-runner vẫn validate lại snapshot như defense-in-depth trước external I/O.

### 5.6. Catalog UI Providers/Combos

Response `GET /llm/providers` giữ `providers`/`hasConnectedProvider` và thêm `provider_options`. Mỗi option
chỉ có metadata trình bày:

```json
{
  "kind": "codex",
  "display_name": "Codex",
  "description": "...",
  "group": "subscription",
  "connectable": true,
  "connection_mode": "account",
  "execution_mode": "proxy",
  "visible": true,
  "prefix": "cx",
  "theme_color": "#0f7a63",
  "beta": false,
  "ui_order": 10
}
```

API-key và subscription kinds đều project từ runtime registry; API-key registration chỉ tham chiếu
fixed-endpoint compiled factory. `execution_mode` là enum an toàn (`api`, `proxy`, `official_cli`, `local`)
để UI không gắn nhầm badge “CLI chính thức/không rủi ro” cho runtime mới. Unknown icon dùng generic mark;
frontend không cần code cho kind mới.

Hai projection không bị trộn domain:

- `/onboarding/status.provider_options` chỉ là `registry.Catalog().Options()` và giữ `route_rank` để
  canonicalize fallback;
- `/llm/providers.provider_options` chứa mọi non-denied runtime registration với safe metadata và
  `visible`, sắp theo unique `ui_order`; chỉ `visible:true` được gallery/create/connect/add-to-combo. Hidden
  option chỉ giúp hiển thị existing Provider/model/Combo và không dùng để quyết định route/readiness.

Mỗi `llmProviderBody` thêm `connection_mode`; `isProviderConnected` chỉ đọc field này. Payload thiếu/
invalid fail closed thành disconnected. Providers page normalize/freeze catalog trước render; không
fallback sang static list nếu response đã malformed. Combo picker tự dùng provider bodies nên model ID
vẫn opaque.
`connection_mode=none` chỉ hợp lệ cho option/body hidden; status helper luôn trả disconnected và picker
không cho thêm mới, nhưng existing Combo member vẫn render bằng safe option/provider body.

## 6. Data flow third runtime

1. Registry constructor nhận fake/runtime entry rank giữa Codex và Claude và validate đủ callbacks.
2. Status/Providers catalog project entry theo rank.
3. `PUT /onboarding/providers` canonicalize qua registry; `OnboardingStore` dùng đúng catalog của
   registry để persist opaque kind/position.
4. `PUT /onboarding/provider` begin đúng pending row.
5. Handler resolve driver trước mkdir; Connect manager chạy driver, seed model thành công rồi mới bind
   Account disabled vào row.
6. Setup lookup model selector, persist opaque model ID và ready row.
7. Test runner lookup member callback; fallback trả actual winning kind/model/position.
8. Complete activate exact bindings và route order.
9. Live router lookup adapter callback và phục vụ entry; không sửa core switch/frontend/schema.

Fake contract được pin để tests không tự diễn giải: kind `future-cli`, display `Future CLI`, rank `50`,
`connection_mode:"account"`, `execution_mode:"local"`, provider ID bằng kind, model ID
`future-model`, non-terminal adapter và in-memory Connect/Test callbacks không spawn process/network hay
tự truy cập filesystem. State machine thật vẫn được phép tạo/xoá đúng account root bên dưới `t.TempDir`
để chứng minh lifecycle/cleanup.

## 7. Error handling và concurrency

- Registry invalid: daemon startup/test constructor fail closed; không publish partial catalog.
- Runtime missing sau snapshot: mutation/Test trả unsupported/configuration-changed trước side effect.
- Connect callback lỗi: giữ state machine error/cancel semantics hiện có, không phản chiếu raw stderr.
- Catalog HTTP malformed: Portal fail closed, không render mutation CTA.
- Two tabs/lost response/CAS: giữ reconciliation V8 hiện tại.
- Terminal order invalid: dưới route-mutation lock, reject registry, combo replace/activation và live route construction; không
  silently skip entries.
- Server option/body không bao giờ chứa callback, endpoint credential, config path hoặc command args.

## 8. File boundaries

- `internal/providercatalog/catalog.go`: pure catalog constructor/canonicalizer; default compatibility.
- `internal/store/app_schema.go`: V7 backfill validates kind through `Default()` before creating V8 stage.
- `internal/store/app_onboarding_providers.go`, `app_onboarding.go`,
  `app_onboarding_test_route.go` và tests: `OnboardingStore` + catalog-aware kind/order validation;
  `*Store` compatibility wrappers dùng default catalog.
- `internal/store/app_llm.go`: generic trusted `(kind, displayName)` Provider ensure; legacy helper dùng
  default catalog.
- `internal/daemon/app_provider_runtime.go` (new): registration types, validation, production registry.
- `internal/daemon/app_provider_registry.go`: projections/capability helpers delegate registry.
- `internal/daemon/app_llm_connect.go`: resolve driver trước side effect; state machine delegates driver và
  connected-model seeder.
- `internal/daemon/app_onboarding.go`: model selector delegates registry.
- `internal/daemon/app_agent.go`: Persona/Test boundary dùng services-aware snapshot validation.
- `internal/daemon/app_onboarding_runner.go`: member factory delegates registry.
- `internal/daemon/app_llm_router.go`, `app_llm_combos_http.go`: adapter/terminal/attachment policy và exact
  route-order/readiness validation delegate registry.
- `internal/daemon/app_zalo_session_hook.go`: production answer path lấy context-bound Zalo runner.
- `internal/daemon/app_llm_api*.go`: safe `provider_options` + `connection_mode` projection.
- `static/pages/providers.js`: dynamic provider options.
- `static/core/providers-status.js`: server connection mode.
- `static/pages/combos.js`: no static subscription classification.

## 9. Test strategy

### Registry

- reject duplicate kind/UI order/catalog rank, missing driver/model seeder, invalid connection/execution
  mode, both/no route path, unsafe attachment/structured claim, terminal not last;
- OpenCode denied even if a test registration claims advertised/full capability;
- defensive copies/no mutable callback leakage in projections.

### Third runtime contract

- fake rank 50 appears between Codex and Claude;
- fake callbacks dùng đúng `future-cli`/`future-model`, không tự có filesystem/process/network side effect;
- default catalog rejects fake kind; synthetic catalog accepts it without global mutation;
- zero/invalid Catalog and default inspection reject a persisted fake row fail closed;
- selection/begin/bind/setup through `OnboardingStore` accepts the catalog-authorized opaque kind/model;
- Test fallback calls fake member exactly at persisted position;
- Test route plus Save/Clear receipt revalidation use the same synthetic Catalog;
- Complete produces exact binding/route and live router invokes fake adapter;
- acceptance call đi vào qua `appZaloRunner`, qua registry-aware readiness rồi mới tới fake live adapter;
- combo mutation/activation rejects a terminal member followed by any entry;
- concurrent replace/activate/Complete cannot pass validation on one route then commit another;
- no code path compares fake kind string outside registration/test.

### Connect/router

- existing Codex and Claude contract suites remain green;
- hidden `gemini-cli` adapter/attachment regressions remain green; Providers/Combos nhận option
  `visible:false`, existing persisted member vẫn render nhưng gallery/create/connect/readiness/add-new đều bỏ;
- missing driver fails before config dir/install/login;
- terminal rank invariant and round-robin exclusion;
- cancellation/timeout/telemetry privacy unchanged.

### Portal

- Providers renders fake catalog row and opens connect based on server metadata;
- Combo picker treats `connection_mode:"account"` correctly without static Set;
- malformed/missing metadata fails closed;
- onboarding future-runtime tests use rank before terminal and remain green;
- all touched JS files stay at most 800 lines.

## 10. OpenCode boundary

OpenCode is not a third-runtime test subject. Fake driver is memory-only and deterministic. OpenCode
stays outside production registry and its failed real synthetic smoke is recorded separately as a
closed gate. A later production proposal must pass successful fixed-prompt smoke plus real OS
containment, credential and retention review.

## 11. Tiêu chí nghiệm thu

1. Production Codex/Claude behavior và multi-provider Onboarding không đổi.
2. Registry không thể quảng bá entry thiếu bất kỳ driver bắt buộc nào.
3. Fake third runtime hoàn thành toàn bộ flow và live call qua registration-only extension.
4. Store/frontend/schema không chứa allowlist third runtime; Store authorization đến từ catalog value
   được truyền tường minh, không mutable global.
5. Providers/Combos render/classify fake runtime từ server metadata, không static Set.
6. Terminal runtime order được validate; không có persisted unreachable fallback.
7. OpenCode vẫn absent khỏi status/catalog/connect/router và không nhận dữ liệu thật.
8. Focused/full Go, Portal/static, build gate và visual states đều xanh trước push.

## 12. Uỷ quyền và lựa chọn mặc định

Người dùng đã yêu cầu không hỏi thêm và tự chọn phương án tốt nhất. Vì vậy:

- TDD = yes;
- fake third runtime rank 50, giữa Codex 10 và Claude 100;
- Claude giữ terminal behavior hiện tại nhưng invariant được registry hoá;
- dynamic metadata được thêm vào response hiện có thay vì tạo endpoint mới;
- OpenCode safe-closed không bị dùng để ép test extensibility pass.
