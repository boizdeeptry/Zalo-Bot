# Thiết kế Onboarding Wizard bắt buộc, có resume và staged activation

**Ngày:** 2026-08-11

**Trạng thái:** Thiết kế đã được người dùng xác nhận ngày 2026-08-11; chờ xác nhận spec

**Repository:** `D:\TuvanZalo\_build\.worktrees\portal-m1`

**Nhánh:** `integration/main-memory-v2`

**Đầu nhánh trước spec:** `8949f64f14ed6667b8fb87400cda54ff2d537f90`

## 1. Bối cảnh

Portal hiện đã có:

- quản lý Provider, Account, Model và Combo;
- Connect flow cho `codex` và `claude-code`, gồm detect/install/login/poll/cancel, progress và log;
- trang Agent đọc các placeholder trong `persona.md` và điền chúng qua `GET/PUT /agent`;
- router/consult pipeline thật, Zalo session theo thread và Memory V2;
- chính sách máy mới không tự gieo Combo mặc định: khi chưa có Provider/route hợp lệ, bot im lặng an toàn.

Những capability này đang nằm ở các trang riêng. Người dùng mới phải tự hiểu thứ tự Provider →
Connect → Combo → Persona → thử bot, trong khi một cấu hình thiếu một mắt xích có thể trông như đã
lưu nhưng bot vẫn không trả lời. Portal cần một luồng lần đầu có thứ tự bắt buộc và tự chứng minh
cấu hình hoạt động trước khi cho vào giao diện chính.

Thiết kế cũng phải áp dụng cho máy đã cài bản cũ. Những máy đó có thể đang phục vụ Zalo bằng Combo
và persona thật, nên onboarding không được phá cấu hình live nếu Connect, test chat hoặc thao tác
hoàn tất bị lỗi.

## 2. Mục tiêu

- Mọi installation phải hoàn thành Onboarding Wizard phiên bản 1 một lần sau khi cập nhật.
- Dẫn người dùng qua đúng thứ tự: chọn Provider, Connect, Auto-combo, Persona, Chat thử và hoàn tất.
- Chỉ hỗ trợ hai Provider subscription trong wizard: `codex` và `claude-code`.
- Tái sử dụng Connect panel hiện có, gồm progress thật, log, cancel và polling lifecycle đã được
  kiểm thử.
- Auto-combo tự chọn một model phù hợp, không bắt người dùng hiểu Provider/model/Combo ở lần đầu.
- Không cho qua Persona khi còn bất kỳ placeholder `{{...}}` nào.
- Chat thử phải chạy qua cùng router/runner/consult contract dùng ở production, với Provider, model,
  persona và tên bot vừa cấu hình.
- Không có nút Skip cho Chat thử. Chỉ một test hợp lệ và xác nhận rõ của người dùng mới cho Complete.
- Có thể resume an toàn sau refresh, mất mạng hoặc restart daemon.
- Routing live cũ tiếp tục hoạt động cho đến khi Complete thành công.
- Thay toàn bộ Combo cũ bằng một Combo mới như người dùng đã chọn, nhưng chỉ ở transaction Complete.
- Giữ mọi Provider và Account cũ; wizard không xoá credential hay config directory cũ. Account vừa
  Connect được stage ở trạng thái disabled để không lọt vào round-robin live trước Complete.
- Giữ nguyên giao diện Portal cũ: wizard dùng chung token, font, panel, nút và ngôn ngữ hình ảnh.

## 3. Ngoài phạm vi

- Không onboarding API-key Provider như OpenAI API, Anthropic API, Gemini API hoặc OpenRouter.
- Không cho chọn thủ công model hoặc kiểu Combo trong wizard; các thao tác nâng cao vẫn ở Portal.
- Không ingest tài liệu trong wizard. Màn hình cuối chỉ dẫn sang mục Kiến thức.
- Không kết nối Zalo account trong wizard.
- Không tạo hoặc resume Zalo CLI session từ Chat thử.
- Không ghi Memory, lesson, transcript, prompt hoặc model response từ Chat thử.
- Không xoá Provider/Account cũ khi người dùng chọn Provider mới. Complete được phép disable, nhưng
  không xoá, các Account cũ cùng Provider để production chỉ dùng Account vừa được xác minh.
- Không tự suy đoán hoặc phục hồi persona từ `persona.md.goc` rồi ghi đè các chỉnh sửa persona hiện có.
- Không thay thế các trang Providers, Combos hoặc Agent sau onboarding.

## 4. Các phương án đã cân nhắc

### A. Hard reset ngay trong từng bước

Connect xong thì xoá Combo cũ, kích hoạt Combo mới và ghi persona ngay; test chat chạy trên cấu hình
live. Cách này ít state staging nhưng một lỗi sau bước Setup có thể để bot production chạy trên cấu
hình chưa được người dùng duyệt. Đây cũng là đường dễ mất Combo cũ nhất nếu tiến trình bị ngắt.

### B. Giữ toàn bộ cấu hình cũ và chỉ thêm Combo mới

Wizard tạo một Combo mới cạnh các Combo hiện có rồi kích hoạt khi xong. Đây là phương án an toàn
với dữ liệu nhưng không đáp ứng quyết định thay toàn bộ Combo cũ, và sau nhiều lần onboarding có thể
để lại các Combo trùng hoặc khó hiểu.

### C. Staged routing migration, Complete có transaction — chọn

Connect tạo một Account staging disabled vì auth cần một config directory thật, nhưng không xoá hoặc
đổi Account live cũ. Auto-combo chỉ ghi descriptor nháp vào onboarding state. Chat thử chạy router
bằng snapshot nháp và ép dùng đúng Account staging, không kích hoạt nó trên live route. Khi người
dùng xác nhận câu trả lời, một transaction duy nhất bật Account staging, disable các Account cũ cùng
Provider, xoá Combo cũ, tạo và kích hoạt Combo đã test, rồi ghi onboarding version hoàn tất.

Ưu điểm: cấu hình live cũ không bị đổi route trước khi test; refresh/retry không tạo Combo rác; lỗi
Complete rollback toàn bộ thay đổi Combo. Nhược điểm: cần một đường router test nhận route snapshot
và một state machine backend riêng. Đây là chi phí hợp lý để onboarding an toàn cho cả fresh install
và máy đang vận hành.

## 5. Trải nghiệm người dùng

Wizard là một shell toàn màn hình đứng trước Portal router. Nó không dùng rail điều hướng của Portal
cho đến khi hoàn tất, nhưng dùng nguyên design token và component style hiện có. Thanh tiến trình chỉ
hiện ba chặng người dùng hiểu được: **Kết nối → Cá nhân hoá → Trò chuyện thử**. Auto-combo và commit
cấu hình là thao tác kỹ thuật, không phải step riêng trên thanh tiến trình.

### 5.1. Chào mừng và chọn Provider

- Tiêu đề: **Hãy thiết lập trợ lý Zalo của bạn**.
- Hai thẻ lựa chọn: **OpenAI Codex** và **Claude Code**.
- Chỉ một Provider được chọn.
- Với máy nâng cấp, Provider của Combo đang active được chọn sẵn nếu thuộc hai loại hỗ trợ.
- Dòng cảnh báo: **Bạn sẽ cần đăng nhập lại. Cấu hình hiện tại chỉ được thay thế sau khi kiểm tra
  thành công.**
- Nút **Tiếp tục** bị khoá khi chưa chọn Provider.

### 5.2. Connect

- Dùng cùng Connect controller/panel với trang Providers, không copy logic polling sang wizard.
- Hiện progress bar theo phase thật, phần trăm, thời gian đã chạy, login URL/device code và dòng log
  gần nhất đã được sanitize.
- Có **Huỷ** và **Quay lại chọn Provider**. Đổi Provider phải cancel job hiện tại trước.
- Wizard luôn khởi động một Connect mới, kể cả Provider đã có Account hợp lệ.
- Account mới persist dưới trạng thái staging disabled. Account cũ không bị tắt hoặc xoá trước
  Complete, nên bot live không đổi account pool trong lúc wizard đang chạy.
- `connected` chỉ được trả sau khi Provider row, model seed và Account row đều đã lưu.
- Terminal state `connected` bổ sung `providerId` và `accountId`; không bao giờ trả `configDir` hoặc
  credential.
- Khi connected, UI tự gọi Setup đúng một lần rồi chuyển sang dòng trung gian:
  **Đang chuẩn bị cấu hình… ✓**.

### 5.3. Persona

- Tiêu đề: **Trợ lý của bạn là ai?**
- `GET /agent` tiếp tục là nguồn placeholder và sample context.
- Form dựng động từ toàn bộ placeholder, không hard-code chỉ hai field.
- Các key quen thuộc có nhãn thân thiện, ví dụ `TEN_BOT` → **Tên bot** và `TEN_CHUYEN_GIA` →
  **Chuyên gia**; key lạ vẫn hiện nhãn được humanize và dòng sample từ server.
- Mỗi value được trim, bắt buộc khác rỗng, tối đa 60 Unicode code point, không chứa newline hoặc
  cặp `{{`/`}}`.
- UI hiện **Còn N mục cần điền**, focus field lỗi đầu tiên và khoá nút tiếp tục khi chưa đầy đủ.
- `PUT /agent` nhận thêm `require_complete: true`. Ở mode này server render trong memory, quét lại
  toàn bộ placeholder rồi mới ghi file; còn placeholder thì trả 422 và không ghi một phần.
- Cổng readiness dùng broad mustache scan `{{...}}`, không chỉ regex uppercase cũ. Placeholder lạ,
  lowercase hoặc malformed-but-closed đều phải được xử lý hoặc báo rõ; không được coi persona ready.
- Backup `persona.md.goc` vẫn được tạo một lần trước lần ghi đầu và không bao giờ bị ghi đè.

### 5.4. Tên hiển thị trên máy cũ

Một persona đã cấu hình từ bản cũ có thể không còn `{{TEN_BOT}}`; server không thể suy ngược tên bot
một cách đáng tin từ prose đã render. Không được phục hồi template `.goc` vì việc đó có thể xoá chỉnh
sửa tay sau lần cấu hình đầu.

Vì vậy profile Agent có thêm `agent_display_name` dạng structured metadata:

- nếu lần điền hiện tại có key `TEN_BOT`, value đó đồng thời cập nhật `agent_display_name`;
- nếu persona đã ready nhưng chưa có metadata này, wizard hiện đúng một field bắt buộc **Tên hiển
  thị của bot** và không ghi lại persona;
- tên hiển thị được đưa vào bootstrap/consult identity contract thật, không chỉ dùng làm nhãn UI;
- Chat thử bắt buộc câu trả lời chứa tên này;
- `GET /agent` trả `display_name`; `PUT /agent` cho cập nhật `display_name` cùng validation 60 ký tự.

Nhờ vậy fresh install vẫn điền template như hiện tại, còn máy cũ không mất persona đã tuỳ biến nhưng
vẫn có một danh tính có cấu trúc cho UI và prompt.

### 5.5. Chat thử

- Giao diện chat gọn, input được prefill **Xin chào** và người dùng có thể sửa, tối đa 500 ký tự.
- Nút gửi gọi `POST /onboarding/test-chat`; chỉ cho một lượt đang chạy tại một thời điểm.
- Server thêm chỉ dẫn authoritative: trả lời ngắn gọn bằng tiếng Việt, giới thiệu đúng display name,
  tuân thủ persona và không nhắc onboarding/cấu hình kỹ thuật.
- Bubble trả lời mang display name vừa cấu hình, ví dụ **Bé Mi**.
- Sau response hợp lệ, hiện **Ổn, dùng cấu hình này**, **Thử lại** và **Quay lại chỉnh Persona**.
- Không có Skip. Chỉ nút **Ổn, dùng cấu hình này** được phép gọi Complete.
- Nếu Provider trả lời nhưng thiếu display name, UI báo: **Bot đã phản hồi nhưng chưa áp dụng đúng
  Persona**; lượt đó không sinh test token.
- Refresh trước khi xác nhận làm mất token phía client và buộc Chat thử lại. Nội dung bot không được
  lưu để phục hồi màn hình.

### 5.6. Hoàn tất

- Complete thành công chuyển sang màn hình: **{Tên bot} đã sẵn sàng!**
- Mô tả: **Bạn có thể bổ sung tài liệu ở mục Kiến thức để trợ lý tư vấn chính xác hơn.**
- Nút chính **Vào Portal** mount Portal router bình thường.
- Nút phụ **Thêm kiến thức ngay** mount Portal và điều hướng tới route Kiến thức.
- Sau onboarding, phần Cài đặt có entry **Thiết lập lại trợ lý**. Entry này khởi động lại current
  onboarding version bằng một thao tác xác nhận riêng; tự refresh bình thường không mở lại wizard.

## 6. Kiến trúc backend

### 6.1. Hai version độc lập

`schema_version` tiếp tục quản lý hình dạng SQLite và tăng từ V6 lên V7 khi thêm onboarding state.
`currentOnboardingVersion = 1` là version sản phẩm độc lập. Không dùng schema version để suy ra người
dùng đã hoàn thành wizard.

Mọi database fresh hoặc nâng cấp nhận singleton onboarding row với `completed_version = 0`. Vì vậy
mọi máy phải chạy wizard phiên bản 1 một lần như đã quyết định. Complete đặt
`completed_version = currentOnboardingVersion`.

Một phiên bản wizard tương lai chỉ gate khi `completed_version < currentOnboardingVersion`; không
cần reset schema hoặc xoá lịch sử migration.

### 6.2. Bảng trạng thái

V7 thêm bảng singleton:

```sql
CREATE TABLE app_onboarding_state (
  id                  INTEGER PRIMARY KEY CHECK (id = 1),
  completed_version   INTEGER NOT NULL DEFAULT 0,
  phase               TEXT NOT NULL,
  provider_kind       TEXT NOT NULL DEFAULT '',
  provider_id         TEXT NOT NULL DEFAULT '',
  account_id          TEXT NOT NULL DEFAULT '',
  model_id            TEXT NOT NULL DEFAULT '',
  staged_combo_id     TEXT NOT NULL DEFAULT '',
  persona_fingerprint TEXT NOT NULL DEFAULT '',
  test_nonce_hash     TEXT NOT NULL DEFAULT '',
  test_expires_at     TEXT NOT NULL DEFAULT '',
  restart_in_progress INTEGER NOT NULL DEFAULT 0 CHECK (restart_in_progress IN (0, 1)),
  revision            INTEGER NOT NULL DEFAULT 1,
  updated_at          TEXT NOT NULL
);
```

Schema thật phải có CHECK cho phase hợp lệ và migration idempotent theo pattern hiện có. Bảng không
lưu credential, config directory, connect log, prompt, user message hoặc model response.

`agent_display_name` là metadata Agent, lưu dưới `app_meta` với helper typed riêng; không đặt trong
onboarding row vì nó tiếp tục được dùng sau khi wizard kết thúc.

### 6.3. State machine

Các phase persist:

```text
provider → connect → setup → persona → test → completed
```

`awaiting_confirmation` là state phía client gắn với test token ngắn hạn; sau refresh server trả
`test` để buộc chạy lại. Server là nguồn sự thật và từ chối mọi mutation không đúng phase/revision.

Quy tắc invalidation:

- chọn hoặc đổi Provider xoá provider/account/model/combo staging cũ, persona fingerprint và test
  receipt; nếu state đang sở hữu một Account staging disabled từ Connect thành công trước đó, server
  xoá đúng row/config directory staging đó sau khi kiểm tra path nằm dưới data accounts root;
- Connect success gắn đúng Provider và Account terminal vào state;
- Setup thay đổi model/combo staging và vô hiệu test receipt;
- `PUT /agent` hoặc chỉnh persona document làm fingerprint đổi và vô hiệu test receipt;
- test success chỉ cấp receipt khi state và fingerprint vẫn đúng snapshot lúc bắt đầu;
- Complete revalidate toàn bộ snapshot ngay trước transaction.

### 6.4. Connect integration

Không tạo connect manager thứ hai. Connect component tiếp tục gọi:

- `POST /llm/providers/{kind}/connect`
- `GET /llm/providers/{kind}/connect`
- `DELETE /llm/providers/{kind}/connect`

`connectState` được mở rộng bằng `providerId` và `accountId`, chỉ populate sau khi Provider/models và
Account đã persist. Hai ID là identifier đã lộ qua API Providers, không phải secret. `configDir` và
credential vẫn là internal-only.

Wizard gửi `onboarding_revision` trong body Connect. Handler chỉ chấp nhận nó khi state đang ở phase
Connect và kind khớp; Account thành công được tạo `enabled=false` rồi bind atomically vào onboarding
state. Trang Providers không gửi field này và giữ nguyên hành vi tạo Account enabled như hiện tại.
Nhờ đó cùng một manager/component phục vụ hai bề mặt nhưng Account staging không tham gia
`accountSelector` của bot live.

Account staging là resource do onboarding sở hữu, không phải Account cũ của người dùng. Nó được giữ
qua refresh/restart để resume, nhưng được dọn khi người dùng đổi Provider hoặc khởi động lại flow.
Cleanup chỉ nhắm ID đã lưu trong singleton state, xác minh resolved path nằm dưới
`<dataDir>/accounts/<kind>/<accountId>` và không dùng glob.

Sau terminal success, wizard gọi Setup với `accountId`. Setup kiểm tra Account tồn tại, đang disabled,
đúng bằng Account staging mà state sở hữu và thuộc đúng `provider_kind`; client không thể trỏ một
Account tuỳ ý hoặc Account live của Provider khác.

### 6.5. Auto-combo policy

`cliDescriptor` thêm `onboardingModel` để lựa chọn là explicit, không phụ thuộc thứ tự `modelSeeds`:

- `codex` → `gpt-5.6-terra`;
- `claude-code` → `sonnet`.

Setup chọn theo thứ tự:

1. `onboardingModel` nếu model tồn tại và available;
2. model available đầu tiên trong `modelSeeds` theo thứ tự descriptor;
3. nếu không có model khả dụng, trả `ONBOARDING_NO_MODEL` và giữ phase Setup.

Setup mint một `staged_combo_id`, đặt tên **Mặc định · Codex** hoặc **Mặc định · Claude**, kiểu
`fallback`, một enabled entry `provider_id/model_id`, rồi chỉ ghi descriptor này vào onboarding
state. Không ghi `llm_combos`, `llm_combo_members` hoặc `llm_route_entries` tại bước này.

Setup idempotent theo `revision + provider_kind + account_id`: retry cùng input trả cùng combo/model
và không tăng revision; input khác hoặc stale revision bị từ chối.

### 6.6. Persona identity contract

Readiness của Agent trở thành tổ hợp:

- persona file đọc được;
- broad placeholder scan không còn lỗ;
- `agent_display_name` khác rỗng và hợp lệ.

Display name được thêm vào bootstrap identity section của prompt thật trước persona body. Persona
vẫn sở hữu văn phong, chuyên môn và quy tắc trả lời; structured display name chỉ sở hữu tên tự xưng
và nhãn UI. Fingerprint của Zalo/consult prompt phải bao gồm display name để session cũ rotate khi
tên thay đổi, giống persona content thay đổi hiện tại.

### 6.7. Test-chat execution

`POST /onboarding/test-chat` dựng một immutable snapshot gồm:

- onboarding revision;
- provider/staging-account/model và one-entry fallback route staging;
- display name;
- current persona content và fingerprint;
- user message đã trim.

Nó gọi cùng route selection, provider adapter/Claude runner, sanitization và output contract của
production, nhưng qua một onboarding execution context cô lập. Account resolver bị pin vào đúng
`account_id` staging thay vì gọi round-robin selector:

- không tạo `app_zalo_cli_sessions`;
- không đọc/ghi Zalo thread transcript;
- không gọi Memory proposal/apply hooks;
- không ghi prompt/response;
- không cập nhật telemetry được Portal diễn giải là live Provider hiện tại;
- không fallback sang route live cũ;
- chỉ route một entry staging được phép chạy.

Nếu cần quan sát timing/error, dùng logger event chỉ chứa provider kind, model, duration và error kind;
không dùng `llm_attempts` live nếu nó làm thay đổi `LLMStatus.ActiveProviderID/ModelID`.

Hard timeout của endpoint là 120 giây và tôn trọng request cancellation. Mọi process con phải đi qua
managed CLI lifecycle hiện có để cancel authoritative và không để orphan process.

Một test đạt khi đồng thời:

1. runner success;
2. sanitized answer khác rỗng;
3. provider/model thực tế đúng staging snapshot;
4. persona fingerprint không đổi trong lúc chạy;
5. answer chứa display name sau Unicode case-fold và whitespace normalization;
6. onboarding revision không đổi.

Khi đạt, server mint token ngẫu nhiên 256-bit, trả raw token một lần cho client, chỉ lưu SHA-256 hash
và expiry 10 phút. Thay Provider, Setup lại, PUT Agent hoặc sửa persona document phải xoá hash này.
Answer chỉ có trong HTTP response và memory ngắn hạn của tab.

### 6.8. Complete transaction

`POST /onboarding/complete` nhận `revision` và raw `test_token`. Server:

1. hash token và constant-time compare với receipt trong state;
2. kiểm tra chưa hết hạn;
3. tính lại persona/display-name fingerprint;
4. kiểm tra Account staging còn tồn tại, thuộc đúng Provider và chứa auth vừa xác minh; kiểm tra
   Provider enabled và model available;
5. mở một SQLite transaction;
6. bật Account staging và disable — không xoá — các Account khác cùng Provider;
7. xoá `llm_combo_members`, `llm_combos` và legacy active route projection hiện có;
8. insert đúng Combo staging đã test, một member enabled và active;
9. đồng bộ `llm_route_entries`/route revision theo invariant Store hiện tại;
10. set phase `completed`, `completed_version = 1`, `restart_in_progress = 0`, xoá receipt và tăng
    revision;
11. commit.

Store cần một method chuyên biệt sở hữu toàn transaction; handler không ghép nhiều Store call rời.
Nếu bất kỳ bước nào lỗi, rollback giữ nguyên toàn bộ Combo/route và trạng thái enabled của Account
cũ, còn onboarding vẫn ở phase Test. Không Provider/Account nào bị DELETE.

## 7. API contract

Mọi route dưới đây dùng `a.auth` và cùng cookie/portal mutation-header policy với các Portal API hiện
có. JSON error dùng code ổn định để frontend map hành vi.

### `GET /onboarding/status`

Response chính:

```json
{
  "required": true,
  "current_version": 1,
  "completed_version": 0,
  "phase": "persona",
  "revision": 4,
  "provider": { "kind": "codex", "provider_id": "codex", "account_id": "..." },
  "setup": { "combo_id": "...", "combo_name": "Mặc định · Codex", "model_id": "gpt-5.6-terra" },
  "agent": { "ready": false, "display_name": "", "placeholders_remaining": 2 }
}
```

Khi completed, `required=false`, phase `completed`. Response không chứa test receipt hoặc secret.

### `PUT /onboarding/provider`

Body: `{ "kind": "codex|claude-code", "revision": N }`.

Chọn Provider, reset staging/test receipt và chuyển phase Connect. Kind khác trả
`ONBOARDING_PROVIDER_UNSUPPORTED`. Stale revision trả `ONBOARDING_REVISION_CONFLICT`.

Connect component sau đó gọi route Connect hiện có với body
`{ "label": "...", "onboarding_revision": N }`. Field revision là discriminator khiến Account mới
được stage disabled; không tạo một endpoint OAuth thứ hai.

### `POST /onboarding/setup`

Body: `{ "account_id": "...", "revision": N }`.

Validate Connect terminal, account ownership và model policy; trả Combo/model staging. Endpoint
idempotent cho cùng input.

### `PUT /agent`

Wizard dùng body:

```json
{
  "values": { "TEN_BOT": "Bé Mi", "TEN_CHUYEN_GIA": "Chị Hoa" },
  "display_name": "Bé Mi",
  "require_complete": true,
  "onboarding_revision": 4
}
```

Default `require_complete=false` giữ tương thích trang Agent hiện tại. Mode complete trả 422
`AGENT_PLACEHOLDERS_REMAIN` và danh sách field nếu render còn lỗ; không ghi file một phần. Khi có
`onboarding_revision`, handler chỉ nhận state ở Persona/Test, invalidate receipt, lưu fingerprint và
chuyển state sang Test trong cùng logical operation. `values` được phép rỗng nếu persona đã ready và
request chỉ xác nhận/cập nhật display name của máy cũ.

### `POST /onboarding/test-chat`

Body: `{ "message": "Xin chào", "revision": N }`.

Success trả:

```json
{
  "answer": "Xin chào, mình là Bé Mi...",
  "bot_name": "Bé Mi",
  "provider_id": "codex",
  "model_id": "gpt-5.6-terra",
  "test_token": "one-time opaque value",
  "expires_at": "..."
}
```

Response thiếu tên bot không phải success; server trả 422 `ONBOARDING_PERSONA_NOT_APPLIED` mà không
cấp token. Một test đang chạy khác trả 409 `ONBOARDING_TEST_BUSY`.

### `POST /onboarding/complete`

Body: `{ "revision": N, "test_token": "..." }`.

Success trả `{ "completed": true, "onboarding_version": 1, "combo_id": "..." }`.

Các lỗi chính: `ONBOARDING_TEST_REQUIRED`, `ONBOARDING_TEST_EXPIRED`,
`ONBOARDING_CONFIGURATION_CHANGED`, `ONBOARDING_REVISION_CONFLICT` và
`ONBOARDING_COMMIT_FAILED`.

### `POST /onboarding/restart`

Chỉ dùng từ Cài đặt sau khi đã completed. Yêu cầu `{ "confirmed": true, "revision": N }`, xoá staging
receipt, đặt `restart_in_progress=1` và phase Provider nhưng giữ `completed_version` cho đến khi
replacement Complete thành công. Status coi `restart_in_progress=1` là required dù completed version
đã hiện hành; route live cũ vẫn hoạt động. Không có silent reset từ frontend và V1 không có Cancel
restart: confirmation phải nói rõ người dùng cần hoàn tất lại wizard để trở về Portal.

## 8. Frontend boundaries

### 8.1. Bootstrap gate

`app-main.js` tải onboarding status trước khi mount Portal router:

- required hoặc restart in progress → mount Onboarding page;
- completed → mount shell/router hiện tại;
- status load error → render retryable full-page error, không đoán completed;
- Complete success → dispose wizard trước rồi mount Portal đúng một lần.

Đổi hash/URL thủ công khi required không mount page Portal. Đây là UX gate; API vẫn dùng auth và
validation riêng, không dựa vào việc route bị ẩn để bảo mật.

### 8.2. Component extraction

Connect UI hiện đang nằm sâu trong `createProvidersPage`. Để thực sự tái sử dụng và không sinh hai
polling state machine, tách phần này thành component/controller nhỏ, dự kiến:

- `static/components/provider-connect.js`: service orchestration, run token, polling, progress timer,
  render panel và dispose;
- `pages/providers.js`: dùng component trong Provider detail;
- `pages/onboarding.js`: dùng cùng component và nhận terminal success callback.

Tương tự, phần field rendering/validation của Agent có thể tách thành
`static/components/persona-fields.js`; `agents.js` và wizard dùng chung rule/hint. Không chuyển cả
trang Providers/Agent vào wizard và không duplicate regex/validation.

### 8.3. Onboarding page controller

`pages/onboarding.js` sở hữu duy nhất:

- snapshot status/revision;
- step render;
- abort controller cho request hiện tại;
- một Connect component instance;
- một test-chat in-flight flag và volatile test token;
- transition callback sang Portal.

Mọi async continuation phải kiểm tra disposed/run token trước khi paint. `dispose()` cancel timer,
polling và request; nó không tự cancel một Connect job đã hoàn tất.

CSS đặt trong một vùng có `[data-onboarding]` hoặc class page riêng. Không dùng selector global có thể
làm thay đổi rail, Providers, Combos, Memory hoặc Agent UI cũ.

## 9. Error handling và recovery

- Connect detect/install/login/poll error giữ người dùng ở Connect với log sanitize, **Thử lại** và
  **Quay lại**.
- Cancel Connect là authoritative như contract hiện tại; UI không auto-next từ completion đến muộn.
- Setup lỗi model/account không chạm Combo live và có thể retry.
- Đổi Provider chỉ dọn Account staging disabled mà state sở hữu; không xoá hoặc disable Account live
  của người dùng.
- Persona validation trả lỗi field-specific; write/backup error giữ nguyên file cũ.
- Test timeout, cancel, CLI exit hoặc response thiếu tên không xoá Provider/Persona/staging.
- Restart daemon làm mất Connect job: status quay Connect với thông báo cần đăng nhập lại.
- Restart/refresh sau test nhưng trước Complete buộc test lại; không persist answer.
- Mọi mutation dùng optimistic `revision`; hai tab không thể cùng Complete hoặc ghi state cũ.
- Complete revalidation lỗi trả về đúng bước cần sửa. Transaction failure rollback route live và
  trạng thái enabled của Account cũ.
- Nếu status row bị thiếu trên database V7, Store fail closed bằng lỗi migration/state rõ ràng; không
  tự coi onboarding completed.

## 10. Security và privacy

- Không route nào trả credential, token vendor hoặc account config directory.
- Device auth code hiện có được phép hiển thị vì là mã một lần có expiry; log vẫn phải sanitize.
- Test token là opaque, random, TTL 10 phút, lưu server dưới hash và constant-time compare.
- User message tối đa 500 ký tự; display/placeholder value tối đa 60 code point và không nhận newline
  hay mustache delimiter.
- Test prompt/answer không vào SQLite, Memory, Zalo logs hoặc application logs.
- CLI giữ read-only/sandbox args và banned-argument guard hiện có; onboarding không tạo command path
  mới bỏ qua adapter safety.
- Test context không cung cấp Zalo attachment, thread transcript hoặc customer identity.
- HTML dùng DOM `textContent`/element helper hiện có; không render answer bằng `innerHTML`.
- Các mutation tiếp tục cần Portal mutation header; cookie-only cross-site request không đủ.

## 11. File và module dự kiến

Backend:

- `internal/store/app_schema.go`: V7 migration và singleton row.
- `internal/store/app_onboarding.go`: state model, transition CAS, setup staging và Complete transaction.
- `internal/store/app_onboarding_test.go`: migration/state/transaction tests.
- `internal/daemon/app_onboarding.go`: HTTP handlers, model policy, test receipt và isolated execution.
- `internal/daemon/app_onboarding_test.go`: handler/state/test-chat tests.
- `internal/daemon/app_llm_connect.go`: terminal Provider/Account IDs và staged-disabled persistence,
  không đổi credential boundary.
- `internal/daemon/app_llm_cli.go`: explicit `onboardingModel`.
- `internal/daemon/app_agent.go` và prompt fingerprint path: require-complete scan, display name và
  identity contract.
- `internal/daemon/app_routes.go`: route registration và cookie allowlist.

Frontend:

- `static/components/provider-connect.js`: Connect component tái sử dụng.
- `static/components/persona-fields.js`: placeholder field/validation tái sử dụng.
- `static/pages/onboarding.js`: wizard controller và UI.
- `static/app-main.js`: bootstrap gate và handoff vào Portal.
- `static/portal.css`: chỉ thêm CSS đã scope cho onboarding/components.

Tests/build:

- `appmode/tests/onboarding.test.mjs`.
- Điều chỉnh `providers.test.mjs`, `agents.test.mjs`, `app-main.test.mjs` cho component extraction và
  bootstrap gate.
- BuildApp/static manifest/package gates phải nhận các module mới.
- `docs/PORTAL-VERIFICATION.md` thêm smoke flow fresh DB và upgraded DB.

Tên file có thể điều chỉnh theo convention trong lúc lập plan, nhưng ranh giới trách nhiệm không được
gộp toàn bộ state machine, router test và DOM vào một file lớn.

## 12. Chiến lược kiểm thử

### 12.1. Store và migration

- Fresh DB tạo V7, singleton state version 0 và không tạo Combo mặc định.
- Nâng từ mọi fixture V1–V6 giữ nguyên Provider, Account, Model, Combo, route, session và Memory.
- Database cũ luôn required cho onboarding V1.
- Mỗi transition đúng tăng revision; stale revision không ghi gì.
- Setup retry cùng input idempotent; đổi Provider invalidate staging/receipt.
- Complete success xoá mọi Combo cũ, tạo đúng một active Combo đã test và set version trong cùng
  transaction.
- Inject lỗi giữa các câu SQL chứng minh rollback giữ nguyên Combo/route cũ và version 0.

### 12.2. Backend API

- Auth/method/body-size/JSON validation cho mọi route mới.
- Unsupported kind, account sai Provider, account disabled và no-model mapping đúng error code.
- Đổi Provider dọn đúng Account staging và không thể thoát khỏi accounts root hoặc xoá Account live.
- Connect terminal ID chỉ xuất hiện sau persistence; onboarding Account persist disabled;
  cancel/error không lộ ID/config dir.
- `require_complete` không ghi partial persona; broad mustache scan bắt placeholder ngoài regex cũ.
- Display name path cho fresh persona và legacy-ready persona.
- Test-chat spy chứng minh đúng staged Provider/model/persona/display name, pin đúng Account vừa
  Connect và không gọi account round-robin/live fallback.
- Test-chat không tạo session, Memory, lesson, transcript hoặc live attempt status.
- Empty answer, missing name, timeout, cancel và concurrent test đều không cấp receipt.
- Receipt one-time, hash-only, TTL, constant-time validation và invalidation sau config change.
- Complete stale/expired/mismatch không đổi Combo.

### 12.3. Frontend unit/integration

- Bootstrap required/completed/error mount đúng tree và dispose đúng một lần.
- Welcome chỉ cho Codex/Claude, preselect hợp lệ và hiển thị cảnh báo nâng cấp.
- Connect component tiếp tục qua toàn phase, auto-next đúng một lần, cancel/back không revive stale run.
- Setup loading/success/error và retry không gửi request trùng.
- Persona dynamic fields, label/hint, counter, focus lỗi và legacy display-name-only path.
- Persona submit gắn revision, transition sang Test và stale submit không ghi file/state.
- Không thể đi thẳng tới Test hoặc Complete bằng DOM/hash manipulation.
- Chat prefill, one-in-flight, response bubble, missing-name error, retry/back và token loss sau refresh.
- Complete success CTA vào Portal/Knowledge; Complete failure giữ wizard.
- Mọi CSS selector onboarding nằm trong scope.

### 12.4. End-to-end và đóng gói

- Fresh DB: Codex flow hoàn chỉnh.
- Fresh DB: Claude Code flow hoàn chỉnh.
- Upgraded DB có active Combo/persona: bot live cũ vẫn giữ nguyên route/account pool trước Complete;
  sau Complete chỉ Combo mới active, Account vừa test enabled, Account cũ cùng Provider còn nguyên
  nhưng disabled, còn Provider khác giữ nguyên.
- Mất mạng ở Connect/Setup/Test; restart daemon ở từng persisted phase; hai tab tranh revision.
- Complete fault injection chứng minh rollback.
- Portal Node tests, toàn bộ overlay Go tests, BuildApp checkpoint và Memory V2 deployment gate tiếp
  tục xanh.
- Package scan không chứa connect credential, test prompt/answer hoặc canary secret.

## 13. Acceptance criteria

1. Một database có `completed_version < 1` không mount Portal router trước khi wizard hoàn tất.
2. Người dùng chỉ thấy ba chặng dễ hiểu; Auto-combo chạy ẩn và hiển thị kết quả model ngắn gọn.
3. Connect Codex/Claude dùng cùng component và backend manager với trang Providers.
4. Wizard luôn yêu cầu một Connect mới; Account mới stage disabled, còn Connect thất bại không xoá
   Account/config cũ hoặc đổi account pool live.
5. Persona không thể qua nếu còn bất kỳ `{{...}}` nào hoặc thiếu display name.
6. Legacy persona không placeholder không bị phục hồi/ghi đè; người dùng chỉ bổ sung display name nếu
   metadata còn thiếu.
7. Chat thử dùng Provider/model/persona staging thật, câu trả lời chứa đúng tên bot và không có side
   effect lên Memory/Zalo/live telemetry.
8. Không có Skip; Complete yêu cầu một receipt hợp lệ, chưa hết hạn và manual confirmation.
9. Trước Complete, Combo/route live cũ không thay đổi.
10. Complete thành công để lại đúng một Combo mới active, bật Account vừa test, disable nhưng không
    xoá Account cũ cùng Provider và giữ mọi Provider; Complete lỗi giữ nguyên toàn bộ Combo và trạng
    thái Account cũ.
11. Refresh/restart resume đúng persisted phase; refresh sau Chat thử buộc test lại.
12. Hoàn tất hiển thị **{Tên bot} đã sẵn sàng!** và có CTA vào Portal hoặc Kiến thức.
13. UI cũ ngoài onboarding không thay đổi bố cục hoặc CSS.
14. Toàn bộ test/build/deployment gate hiện có tiếp tục đạt.

## 14. Rủi ro và biện pháp

- **Model alias thay đổi:** `onboardingModel` luôn được đối chiếu store availability và có fallback
  deterministic; không ghi một ID không tồn tại.
- **Connect success đến sau cancel:** tái sử dụng authoritative cancel/run-token guard đã có; terminal
  ID chỉ publish sau persistence.
- **Persona cũ không còn placeholder:** dùng structured display name, không suy đoán từ prose hoặc
  restore `.goc`.
- **Test cho kết quả ngẫu nhiên:** system instruction ngắn, kiểm tên theo normalized text và cho retry;
  manual approval vẫn là gate cuối.
- **Test staging vô tình đổi live status:** onboarding execution không ghi live attempt telemetry và
  không dùng active route snapshot.
- **Crash giữa Complete:** thay đổi Account enabled, Combo và version nằm trong một SQLite
  transaction; persona đã là một save độc lập, có atomic write và backup riêng.
- **Hai tab hoặc double-click:** optimistic revision, one-in-flight frontend guard và one-time receipt.
- **Refactor Connect làm hồi quy Providers:** component extraction phải giữ nguyên service contract và
  chạy lại toàn bộ `providers.test.mjs` trước khi wizard dùng nó.

## 15. Quyết định cuối cùng

- Bắt buộc onboarding V1 cho cả fresh và upgraded database.
- Luôn Connect lại Provider đã chọn.
- Giữ Provider/Account cũ; Account onboarding stage disabled. Complete bật Account vừa test, disable
  Account cũ cùng Provider và thay toàn bộ Combo.
- Dùng staged route cho Test Chat, không kích hoạt trước.
- Codex mặc định `gpt-5.6-terra`; Claude Code mặc định `sonnet`, có fallback availability.
- Persona phải hết mọi mustache placeholder và có structured display name.
- Test Chat không Skip, không side effect và cần manual approval bằng receipt ngắn hạn.
- Complete là transaction duy nhất đổi Combo/route live và onboarding version.

Spec không còn câu hỏi mở. Bước tiếp theo sau khi người dùng xác nhận spec là viết implementation
plan bằng `:writing-plans`; chưa triển khai code trong giai đoạn này.
