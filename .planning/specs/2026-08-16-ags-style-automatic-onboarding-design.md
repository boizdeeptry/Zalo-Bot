# Onboarding tự động kiểu AGS cho Tư Vấn Zalo

**Ngày:** 2026-08-16

**Trạng thái:** Đã chốt theo chỉ đạo người dùng: không bắt cấu hình tri thức ba bước; OFF Provider phải mở modal xác nhận thật.
**Phụ thuộc:** `.planning/specs/2026-08-15-onboarding-multi-provider-design.md`,
`.planning/specs/2026-08-15-provider-runtime-registry-design.md`

## 1. Vấn đề

Flow hiện tại đưa người dùng qua:

`Provider -> Connect -> Setup -> Persona -> Test Chat -> Complete`

Ba checkpoint cuối là hàng rào kỹ thuật tốt nhưng không phải ba quyết định mà người dùng cần đưa ra.
Persona của gói đã có nguồn build rõ ràng; Knowledge có thể trống mà bot vẫn chạy an toàn; Test Chat chỉ
cần chứng minh route staging thật hoạt động trước khi kích hoạt.

Kết quả khảo sát tĩnh AGS/Bé Mầm Mini:

- “Cài não” cài runtime/proxy/agent/service; sau đó người dùng thêm account và chọn model.
- Persona Bé Mầm được compile sẵn; short-term history được ghép tự động.
- Không có KB/RAG bắt buộc trong onboarding Bé Mầm.
- AGS Memory và KB là module tùy chọn, chạy bất đồng bộ sau khi chat đã hoạt động.
- Toggle selection, install state và readiness là ba state riêng.
- Xác nhận disable dùng native modal; ON không cần confirm.

Tư Vấn Zalo cần học mô hình progressive setup này nhưng vẫn giữ domain Knowledge/RAG thật, receipt,
rollback và route staging đã harden.

## 2. Mục tiêu

1. Người dùng chỉ chọn/cài Provider; không thấy Persona, Test Chat hay nút Complete trong onboarding.
2. Cả hai hoặc nhiều Provider có thể ON trước khi cài; mỗi Provider được cài tuần tự bằng CTA riêng.
3. Khi Provider cuối cùng đang ON trở thành ready, hệ thống tự chuẩn bị Persona, probe fallback thật và
   Complete.
4. Persona đã hoàn chỉnh được đóng gói từ `-PersonaSource`; không biến lại thành placeholder runtime.
5. Knowledge/tài liệu doanh nghiệp là bước tùy chọn sau khi bot sẵn sàng.
6. OFF một Provider pending luôn mở một native modal xác nhận. Hủy/Escape không đổi state.
7. Mọi lỗi bootstrap giữ route live cũ, Account staging disabled và cho Retry/reconcile sau refresh.

## 3. Không làm

- Không bỏ các phase durable `persona`/`test` khỏi Store.
- Không bỏ staged Test, receipt hash, transition lease, cancellation fence hay atomic Complete.
- Không tự tạo receipt giả hoặc coi HTTP 200 là bằng chứng model đã trả lời.
- Không tự ingest tài liệu doanh nghiệp, đoán nội dung ngành hoặc bật RAG từ dữ liệu không được duyệt.
- Không copy branding, hard-coded OpenCode bootstrap, CPA port hay provider IDs của AGS.
- Không cho OFF ready row trong onboarding; quản lý credential đã ready tiếp tục ở Providers/Combos.

## 4. Phương án

### 4.1. Chọn: một bootstrap endpoint server-side

Thêm:

```http
POST /onboarding/bootstrap
Content-Type: application/json

{"revision": 42}
```

Endpoint nhận phase `persona` hoặc `test`, chạy các typed core hiện có trong một operation được quản lý,
và chỉ trả authoritative status đã completed hoặc safe error category.

Lý do chọn:

- raw Test token không rời server;
- Store vẫn phát hành receipt thật, bound route/persona/model/account;
- server kiểm soát lease/cancel/revision và lost response;
- refresh có thể resume từ durable Persona/Test;
- UI chỉ có một progress state, không phải giả lập nhiều mutation.

### 4.2. Loại: frontend gọi ngầm chuỗi endpoint cũ

Frontend có thể tự `PUT /agent -> POST /onboarding/test -> POST /onboarding/complete`, nhưng raw token sống
trong browser, abort/lost-response giữa ba call phức tạp và dispose có thể làm UI nhận success muộn. Không dùng.

### 4.3. Loại: bỏ Test/receipt

Chuyển thẳng Setup -> Complete làm mất bằng chứng staged Account/model/persona thực sự trả lời, làm yếu rollback
và có thể kích hoạt Combo chết. Không dùng.

## 5. Persona đóng gói sẵn

### 5.1. `PersonaSource` là input hoàn chỉnh

Build đang bắt buộc `-PersonaSource`. Từ thay đổi này, input đó phải là Persona deploy được ngay:

- `persona.md` UTF-8, bounded, không còn mustache hole mở hoặc đóng;
- `roster.md` và overlay vẫn tùy chọn như hiện tại, nhưng file nào có mặt cũng không được còn hole;
- thêm `identity.json` có schema v1, `display_name` đã chuẩn hóa và danh sách identity cố ý xuất hiện
  trong Persona;
- build fail closed nếu file thiếu, field thừa, display name không khớp policy hoặc Persona còn placeholder.

Ví dụ:

```json
{
  "version": 1,
  "display_name": "boizdeeptry",
  "persona_identities": [
    {"role": "assistant", "value": "boizdeeptry"},
    {"role": "expert", "value": "Anh Trường"}
  ]
}
```

`genpersona.py` không được thay tên đã điền thành `{{TEN_BOT}}`/`{{TEN_CHUYEN_GIA}}` trong package nữa.
Source build được copy nguyên byte sau validation vào working Persona `brain/reference/persona/` và immutable
bootstrap default `app/defaults/persona/`; build manifest bind digest của cả hai bản. `.goc` là backup của
Persona deploy này, không phải template trống cho wizard: daemon chỉ tạo nó lazily trước lần sửa Persona đầu
tiên, và bytes/digest phải bằng immutable default `persona.md`. Fresh package dùng working copy ngay;
immutable default chỉ phục vụ exact first-seed/legacy migration, không được dùng để overwrite bản đã chỉnh.

Build ghi strict immutable `app/defaults/persona/build-manifest.json` schema v1. Manifest chứa digest của
`identity.json`, per-file SHA-256 + tree digest của exact default file set, và tối
đa tám hash legacy generic Persona được phép migrate; manifest không tự nằm trong tree digest. Với package
hiện tại, allowlist gồm exact generic hash `8c6ca4f7aa19096a0619c706286f7cff97452c3c4025c03ab59b083895bbacf2`.
Daemon chỉ tin display name đọc từ default `identity.json` sau khi strict-parse manifest, khớp identity digest
và re-hash immutable default tree. Manifest không lặp identity literal ở ngoài exact Persona file set.
Working `brain/reference/persona/identity.json` là bản người dùng nhìn/sửa, không bao giờ tự nó là trust root.

Wire shape được pin để build PowerShell và daemon Go không tự diễn giải khác nhau:

```json
{
  "version": 1,
  "files": [
    {"path": "identity.json", "bytes": 196, "sha256": "<64 lowercase hex>"},
    {"path": "persona.md", "bytes": 19782, "sha256": "<64 lowercase hex>"},
    {"path": "roster.md", "bytes": 3459, "sha256": "<64 lowercase hex>"}
  ],
  "tree_sha256": "<64 lowercase hex>",
  "legacy_persona_sha256": [
    "8c6ca4f7aa19096a0619c706286f7cff97452c3c4025c03ab59b083895bbacf2"
  ]
}
```

`files` sorted theo exact slash-normalized relative path và chỉ chứa allowed file set; `bytes` là byte length
không phải rune count. `tree_sha256` dùng domain `agentdc/persona-default-tree/v1\0`, rồi cho từng file theo
thứ tự: uint64 big-endian length(path), UTF-8 path, uint64 big-endian length(content), raw content. JSON phải
canonical UTF-8/LF, không BOM, không duplicate/case-folded/unknown key; placeholder `<...>` ở ví dụ là mô tả
schema, build thật luôn ghi digest cụ thể và scanner cấm literal angle-placeholder đó trong package.

`identity.json` được parse strict: chỉ schema v1, không field lạ/trùng, tối đa tám identity; `role` thuộc enum
`assistant|expert|business`, `value` là UTF-8 đã trim/NFC, bounded và unique. `display_name` phải bằng identity
`assistant` duy nhất. Đây là manifest khai báo identity có chủ đích, không phải một regex allowlist rộng.

Build phải chạy preflight này trước khi clear hoặc ghi bất kỳ byte nào vào `$Out`:

1. resolve và mở/đọc exact allowed file set của `PersonaSource` vào một immutable byte snapshot;
2. validate strict `identity.json`, Persona complete, roster/overlay và tính digest từ snapshot;
3. kiểm tra mọi `persona_identities[].value` thực sự có trong tập file Persona được phép;
4. chạy privacy/credential scan nguồn với ngoại lệ chỉ cho exact literal đã khai báo, chỉ trong exact file set
   `persona.md`, `roster.md`, `identity.json`, `overlay/**` dưới hai root Persona đích
   `brain/reference/persona/` và `app/defaults/persona/`;
5. chỉ sau toàn bộ preflight PASS mới clear output, ghi cả hai root từ chính byte snapshot (không re-read
   source) và verify digest đích.

Exact source-side `persona.md.goc` được phép tồn tại như backup của operator nhưng không phải build input:
preflight chỉ xác minh nó là regular non-reparse bounded file rồi loại khỏi snapshot, scan exception, copy và
manifest. Mọi file lạ khác ngoài `persona.md`, `roster.md`, `identity.json`, `overlay/**` vẫn fail closed.

Scanner cuối package vẫn cấm token, credential, session, customer/config canary và mọi identity lịch sử không
được khai báo ở mọi nơi. Một identity được khai báo cũng vẫn bị cấm ngoài exact file set nêu trên. Ngoại lệ
duy nhất là `brain/reference/persona/persona.md.goc`, và chỉ khi digest/bytes bằng immutable default
`app/defaults/persona/persona.md`; file backup khác hoặc mismatch đều fail closed. Không miễn cả binary, log,
data, launcher, JS hay tài liệu khác. Call `genpersona.py` cũ bị xóa hoặc thay bằng validator thuần không
mutation; không có giai đoạn tạo hole sau preflight.

### 5.2. Không ghi đè Persona người dùng

Launcher truyền exact immutable root qua `AGENTDC_ZALO_PERSONA_DEFAULT_DIR`; daemon không suy nó từ working
path hay prose. Bootstrap đọc working Persona, immutable default và build manifest đã xác minh:

- Persona complete/user-edited: giữ nguyên, chỉ fingerprint và tiếp tục.
- Working Persona thiếu trên first seed, hoặc exact untouched legacy generic template có hash trong bounded
  allowlist của build manifest: tạo `.goc` từ immutable default (không backup lại template invalid), rồi copy
  default sang working path bằng temp-file + fsync + atomic rename và re-read/digest trước khi tiếp tục.
- Persona có hole nhưng không khớp exact legacy hash, malformed hoặc recovery pending: fail closed, không ghi;
  UI chỉ dẫn mở Agents để sửa rồi Retry.

Không suy ngược tên từ prose và không overwrite một Persona đã chỉnh.

## 6. Durable flow

Public UI nhìn thấy:

`Provider selection/install -> Đang chuẩn bị trợ lý -> Sẵn sàng`

Store vẫn chạy:

`provider -> connect -> setup -> persona -> test -> completed`

### 6.1. Từ Persona

1. Validate exact revision, Catalog, all-ready ordered stages, clean active slot và Persona recovery state.
2. Validate current Persona has no placeholder and load exact `display_name` from trusted identity metadata or
   existing committed agent metadata.
3. Gọi catalog-neutral Persona mutation core để persist fingerprint/display name và advance `persona -> test`
   đúng một revision.
4. Không thay route live và không enable staged Account.

### 6.2. Từ Test

1. Capture atomic ready-route snapshot qua cùng runtime Catalog.
2. Chạy fixed internal probe, ví dụ `Xin chào`, qua ordered staged fallback và exact pinned Account/model.
3. Semantic validator yêu cầu non-empty answer và persona identity đúng như Test Chat hiện tại.
4. Re-read exact route/persona/account/model; lưu route-bound receipt thật bằng nonce nội bộ.
5. Gọi Complete core với exact raw nonce chỉ còn trong server operation.
6. Complete atomically enable staged Accounts, replace Combo/legacy route, clear staging và advance revision.

Nếu operation bắt đầu ở Persona revision `r` và thành công, ba durable mutation là Persona advance `r+1`,
Save/replace receipt `r+2`, Complete `r+3`; response completed phải đúng `r+3`. Nếu bắt đầu ở Test revision
`r`, Save/replace receipt tạo `r+1` và Complete tạo `r+2`; response phải đúng `r+2`. Client normalizer không
chấp nhận revision/phase khác và dùng GET authoritative để reconcile lost response.

### 6.3. Lease và deadlock

Current Test lease sống tới khi handler Test trả về, còn Complete chờ Test dừng. Bootstrap không được gọi handler
này lồng handler kia.

Tách typed cores và cung cấp lease promotion có ownership rõ ràng:

- mỗi bootstrap tạo một opaque owner token không lấy từ request/body và acquire Test lease gắn token đó;
- external probe chạy ngoài global mutation lock, nhưng Test lease vẫn giữ ownership;
- sau probe, lock đúng thứ tự `onboardingMutationMu -> onboardingTestMu`; dưới hai lock phải kiểm request chưa
  cancel, active lease đúng token, state vẫn là `active-test`, không có foreign transition/cancel;
- atomically đổi chính lease đó từ `active-test(owner)` sang `provider-transition(owner)`. Promotion không gọi
  cancel/wait, không đóng `done` và tuyệt đối không chờ chính owner;
- deferred Test cleanup là owner-aware: chỉ clear/close khi lease vẫn là `active-test` cùng token; nếu đã
  promoted thì transition cleanup cùng token là bên duy nhất release/close;
- chỉ sau promotion mới acquire route mutation lock, rồi re-read/revalidate, Save receipt và Complete;
- foreign cancel/transition, owner mismatch hoặc cancellation trước promotion đều fail closed trước Complete;
  receipt đã tạo về sau chỉ được compensate bởi exact owner/snapshot và stale cleanup không xóa receipt mới.

Không hold global mutation lock trong thời gian model I/O.

### 6.4. Retry, refresh và idempotence

- UI chỉ có một bootstrap request in-flight cho `(phase,revision)`.
- Reload ở Persona/Test tự gọi lại bootstrap.
- Existing receipt không có raw nonce sau lost response được thay bằng receipt mới qua exact current snapshot;
  stale compensation không được xóa receipt mới.
- Nếu response Complete mất, GET status completed là authoritative success. Server không suy ra một stale
  `r -> r+2/r+3` là cùng operation khi không còn receipt bootstrap riêng.
- Request ở Completed chỉ là no-op khi body revision bằng exact current revision; stale revision trả 409 và
  client reconcile GET. Không chạy model lại.
- Hai request cùng Persona/Test revision được one-flight gate quyết định; request thua không được tiếp tục trên
  revision mới và chỉ nhận busy/conflict/current snapshot theo contract an toàn.
- Abort/dispose không được biến thành “chắc chắn chưa commit”; client luôn reconcile GET.

## 7. UI

### 7.1. Provider stage

- Toggle ON/OFF biểu diễn selected set authoritative, không biểu diễn installed/readiness.
- Nhiều toggle có thể ON đồng thời.
- CTA `Bấm để cài.` chỉ chạy đúng row; một install job mỗi lúc; row khác hiện `Đang chờ`.
- Sau row cuối ready, thay stage bằng `Đang chuẩn bị trợ lý…`; không render Persona/Test form.
- Bootstrap error hiển thị message an toàn, `Thử lại` và `Quay lại Provider`.
- Done stage có CTA vào Portal và CTA tùy chọn `Thêm tài liệu doanh nghiệp`; Knowledge không chặn Done.

### 7.2. OFF confirmation là modal thật

Onboarding là full-page blocking gate với card nhìn như modal hiện tại, nhưng shell nền không mang
`role=dialog/aria-modal=true`. Khi OFF một pending Provider:

1. giữ toggle ON cho tới khi user xác nhận;
2. append một native `<dialog role="alertdialog" aria-modal="true">` làm modal duy nhất;
3. gọi `showModal()`; nền onboarding inert/không tương tác;
4. focus `Hủy` trước, không focus destructive action;
5. Escape/cancel/`Hủy` đóng modal, không gọi API, restore focus về toggle gốc;
6. `Vẫn tắt` khóa modal, persist selected set mới, rồi chỉ đóng sau authoritative response/reconciliation;
7. lỗi giữ modal mở với safe error và cho Retry/Hủy.

Modal có labelled/described IDs, focus-visible, status `aria-live`, no nested interactive controls, desktop và
390x844 không overflow. Ready toggle vẫn locked ON và giải thích quản lý sau ở Provider/Combo.

Đây là một contract chung, không chỉ là ngoại lệ của onboarding: nếu trang Providers hiện tại hoặc tương lai
có control đổi Provider từ enabled/ON sang disabled/OFF, control đó phải dùng cùng native modal lifecycle và
chỉ gọi mutation sau `Vẫn tắt`. Không dùng `window.confirm`, popover, toast có hai nút hay dialog lồng nhau.
Mỗi thao tác OFF chỉ được có đúng một modal active; mutation API/backend nội bộ không tự mở modal thứ hai.

## 8. Knowledge sau onboarding

Package vẫn có `brain/raw` và `brain/wiki`; bot chạy được khi chúng trống và phải nói chưa có căn cứ thay vì bịa.
Knowledge page cho upload/ingest sau khi Done. Ingest status, source approval và retrieval citations tiếp tục là
domain riêng, không bị trộn vào Provider readiness.

Conversation memory có thể tự hoạt động theo policy hiện tại. Việc tạo long-term memory/knowledge proposal phải
không block câu trả lời đang chạy và không tự publish tài liệu chưa duyệt.

## 9. API và module boundaries

- Store giữ phase/receipt/Complete APIs hiện tại; chỉ thêm typed internal promotion/core nếu cần.
- Daemon thêm bootstrap request/response strict JSON và `appOnboardingBootstrap` coordinator.
- Runtime registry cung cấp same Catalog, pinned Test member factory và live policy; bootstrap không switch kind.
- Build layer sở hữu PersonaSource/identity validation; daemon không đoán build identity.
- Frontend service thêm `bootstrap(revision, signal)`; orchestration nằm module riêng để giữ
  `onboarding.js <= 800`.
- View tạo native confirmation dialog qua một helper có dispose/focus lifecycle, không tự gọi API.

## 10. Error contract

- Invalid JSON/duplicate keys/revision: 400/409 trước side effect.
- Persona incomplete/recovery pending: 409 category an toàn; không trả path/content/placeholder values.
- Staging/model/account/config drift: existing 409/422 mappings.
- Probe all-failed: sanitized 502; timeout 504; cancellation không Complete.
- Bootstrap busy: deterministic 409 hoặc current snapshot; không start job thứ hai.
- Post-commit response projection chỉ dùng committed snapshot + immutable registry metadata; không có DB read
  phụ làm success thành 500.

## 11. TDD và acceptance

### Build

- filled Persona + identity packages byte-exact;
- hole/malformed/unknown identity fields fail build;
- source untouched; backup exact; no customer token/config leak;
- legacy exact template migration only, user-edited Persona never overwritten.

### Store/daemon

- Persona start `r` -> fixed staged fallback -> real receipt -> completed `r+3`;
- Test resume `r` -> new receipt -> Complete `r+2`;
- first member fail/second pass; all fail; cancellation/timeout;
- lost response, concurrent Retry, stale revision, receipt replacement/compensation;
- persona/stage/account/model/config drift at every fence;
- no deadlock between Test lease and Complete; no external I/O under mutation lock;
- route/account old config unchanged on every failure;
- synthetic third runtime passes without core kind switch;
- OpenCode remains denied.

### Portal

- no Persona/Test/Complete controls in onboarding;
- auto bootstrap once, refresh resume, abort fences, lost-response reconciliation;
- Knowledge CTA is optional after Done;
- both Provider toggles ON, sequential install/waiting row;
- pending OFF opens native modal; cancel/Escape/no API/focus restore;
- Provider-management OFF cũng dùng đúng một native modal, không có browser confirm/dialog lồng;
- confirm waits for server/reconcile; error keeps modal usable;
- exactly one active `aria-modal`, background inert;
- desktop and true 390x844 screenshots: both ON, active+waiting, bootstrap progress, OFF modal, Done.

## 12. Completion criteria

1. Người dùng không còn thấy wizard Persona/Test ba bước.
2. Sau Provider cuối ready, system tự probe và Complete bằng receipt thật.
3. Fresh exact package dùng PersonaSource complete mà không hỏi tên/tri thức.
4. Knowledge trống không chặn bot; Knowledge setup ở Portal sau Done.
5. Mọi OFF do người dùng khởi tạo luôn có đúng một native modal xác nhận và không đổi state trước confirm.
6. Refresh/retry/cancel/lost-response không kích hoạt route sai hoặc mất staging ownership.
7. Full Go/Portal/build gates, independent reviews và exact-HEAD visual evidence PASS.
