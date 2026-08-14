# Onboarding đa Provider, cài tuần tự và mở rộng theo catalog

**Ngày:** 2026-08-15

**Trạng thái:** được người dùng uỷ quyền chọn phương án tốt nhất và triển khai không cần hỏi thêm

**Nhánh:** `integration/main-memory-v2`

## 1. Bối cảnh

Onboarding hiện mô phỏng công tắc nhưng thực chất chỉ cho chọn một Provider. Singleton
`app_onboarding_state` chỉ chứa một bộ `provider_kind/provider_id/account_id/model_id`; Connect đầu
tiên chuyển toàn bộ wizard sang Setup rồi Persona. Vì vậy hai công tắc không thể cùng ON và nếu chỉ
sửa frontend, Provider thứ hai sẽ không bao giờ được stage hoặc đưa vào Combo.

Mẫu sản phẩm được quan sát trên máy cho thấy ba nguyên tắc đáng giữ:

- catalog khả năng do server cung cấp, UI không tự hard-code toàn bộ agent;
- ý định người dùng (ON/OFF) tách khỏi trạng thái kỹ thuật (chưa cài/đang kiểm tra/sẵn sàng/lỗi);
- công việc cài đặt là job có tiến trình, có resume/reconcile, không phải một Boolean giả.

Không sao chép branding, mã nguồn, asset hay copy của sản phẩm tham chiếu vào runtime UI. Giao diện
vẫn là Tư Vấn Zalo: modal hiện có trên nền lưới, sidebar tiếp tục ẩn.

OpenCode `1.18.18` đang có trên máy và đã được kiểm chứng thực tế bằng một lượt cô lập:

- `opencode run --pure --format json --model opencode/deepseek-v4-flash-free` trả đúng ba event
  `step_start/text/step_finish` và câu trả lời `OK`;
- event mang `sessionID`; session thử đã được xoá thành công bằng `opencode session delete`;
- `XDG_DATA_HOME/XDG_CONFIG_HOME/XDG_CACHE_HOME/XDG_STATE_HOME` thực sự cô lập được toàn bộ đường
  dữ liệu trên Windows;
- inline config `permission: {"*":"deny"}`, `share:"disabled"`, `autoupdate:false` được resolve
  đúng khi chạy với `--pure`.

OpenCode không cung cấp OS sandbox. Vì vậy chỉ tích hợp runtime nếu mọi cổng cô lập và kiểm thử an
toàn trong mục 10 đạt; không dùng `--auto`, `--yolo` hay bất kỳ cờ bỏ permission nào.

## 2. Mục tiêu

1. Codex, Claude Code và các Provider tương lai có thể cùng ON trước khi cài.
2. Mỗi hàng cài/kết nối độc lập bằng CTA `Bấm để cài.`, nhưng chỉ một job máy chạy tại một thời điểm.
3. Sau khi cài xong một Provider, quay lại danh sách; Provider còn ON nhưng chưa cài vẫn chờ người
   dùng bấm riêng.
4. Chỉ chuyển sang Persona khi mọi Provider đang ON đều đã ready.
5. Thứ tự fallback lấy từ catalog backend, không phụ thuộc thứ tự click/cài/login; Codex đứng trước và
   Claude Code đứng cuối vì adapter Claude hiện là nhánh kết thúc của router.
6. Refresh/restart giữ nguyên lựa chọn, tiến độ, thứ tự và các Account staging.
7. Test Chat chạy trên toàn bộ fallback staging; Complete atomically kích hoạt một Combo nhiều member.
8. Catalog và model ID là dữ liệu server-driven/opaque để thêm runtime hoặc model mới mà không đổi
   schema hay viết lại UI.
9. Tạo integration spike OpenCode sau feature flag; chỉ quảng bá trong catalog production nếu và chỉ
   nếu safety gate, gồm cả OS containment, đạt.

## 3. Ngoài phạm vi

- Không cài song song nhiều CLI hoặc mở hai login flow cùng lúc.
- Không thêm model picker nâng cao trong Onboarding này; mỗi runtime tự chọn model onboarding theo
  policy/catalog. Trang Combos vẫn là nơi chỉnh thứ tự/model sau khi hoàn tất.
- Không dùng OpenCode để đi vòng điều khoản Anthropic Pro/Max. Claude Code vẫn là đường subscription
  chính thức riêng của ứng dụng.
- Không tự động import API key hay credential từ file người dùng khác.
- Không thay giao diện Portal sau Onboarding.
- Không gỡ npm package khi người dùng OFF; chỉ dọn Account/config staging mà Onboarding sở hữu.

## 4. Phương án đã cân nhắc

### A. Chỉ đổi frontend thành hai toggle

Nhanh nhưng sai: Connect đầu tiên vẫn chiếm singleton và chuyển phase, refresh mất lựa chọn thứ hai,
Test/Complete vẫn tạo Combo một member. Loại bỏ.

### B. Kết nối các Provider như Account live rồi chọn lại ở cuối

Ít thay Store hơn nhưng phá invariant “không đổi cấu hình live trước Complete”, khó rollback và để
Account mới lọt vào pool production. Loại bỏ.

### C. Tập Provider staging quan hệ + một active install slot — chọn

Thêm bảng staging nhiều hàng, giữ singleton làm state tổng và active install slot. Connect/Setup cũ
được tái sử dụng tuần tự; Test/Complete đọc toàn bộ hàng ready. Đây là phương án vừa giữ hardening
hiện có, vừa mở rộng thành N Provider/model.

## 5. Kiến trúc và state machine

### 5.1. State tổng

`app_onboarding_state.phase` tiếp tục dùng:

`provider -> connect -> setup -> persona -> test -> completed`

Ý nghĩa mới:

- `provider`: danh sách chọn/cài; có thể đã có 0..N Provider ready;
- `connect`: đúng một Provider đang được cài/login/xác minh;
- `setup`: Account của Provider active đã stage, đang chọn model;
- `persona/test/completed`: mọi Provider được chọn đều ready.

Các cột singleton `provider_*` chỉ là active install slot trong `connect/setup`, rồi là projection của
member ưu tiên 1 trong `persona/test` để tương thích/migration. Bảng staging mới mới là nguồn sự thật
cho route nhiều member.

### 5.2. Bảng staging mới

Schema V8 thêm:

```sql
CREATE TABLE app_onboarding_provider_stages (
  kind        TEXT PRIMARY KEY,
  status      TEXT NOT NULL CHECK (status IN ('pending','ready')),
  position    INTEGER NOT NULL CHECK (position >= 0),
  provider_id TEXT NOT NULL DEFAULT '',
  account_id  TEXT NOT NULL DEFAULT '',
  model_id    TEXT NOT NULL DEFAULT '',
  updated_at  TEXT NOT NULL
);

CREATE UNIQUE INDEX app_onboarding_provider_position
ON app_onboarding_provider_stages(position);
```

Invariant:

- row tồn tại = Provider đang ON;
- mọi row có position liên tục từ 0 theo `route_rank` của catalog, không theo thời điểm cài;
- `pending` phải có ba identity rỗng;
- `ready` phải có exact `provider_id/account_id/model_id` hợp lệ;
- phase `persona/test` yêu cầu ít nhất một row và tất cả row `ready`;
- active singleton ở `connect/setup` phải trỏ tới đúng một row `pending`;
- `staged_combo_id` chỉ được cấp khi chuyển sang Persona;
- singleton `provider_*` chỉ có dữ liệu ở `connect/setup`, và được clear khi quay lại `provider` hoặc
  sang `persona/test`; status legacy có thể project member position 0 từ bảng thay vì lấy singleton.

Store đọc singleton và toàn bộ rows bằng một read transaction `OnboardingSnapshot`; không ghép hai
lượt đọc có thể quan sát state/rows thuộc hai revision khác nhau. Mọi mutation rows, kể cả reindex,
tăng singleton revision nên receipt cũ không thể sống sót qua thay đổi route staging.

Model ID là chuỗi opaque. OpenCode có thể dùng full ID như `opencode/deepseek-v4-flash-free`; runtime
khác có thể thêm model mới mà không migration.

### 5.3. Migration

- Fresh DB tạo V8, singleton Provider phase và bảng stage rỗng.
- DB V7 đang dở dang được ánh xạ đúng một row từ singleton:
  - Provider: không tạo row vì lựa chọn cũ chưa từng được persist;
  - connect: `pending` position 0;
  - setup: row `pending`, Account vẫn do active slot sở hữu;
  - persona/test: `ready`, position 0, giữ exact identity/model trong row rồi clear active singleton.
- DB completed không bị bắt onboarding lại và không tạo staging row.
- Migration nằm trong một SQLite transaction, tăng singleton revision cho flow đang dở để tab V7 bị
  conflict an toàn, idempotent theo schema version và không sửa live Provider/Account/Combo/route.
- `CurrentOnboardingVersion` giữ nguyên; schema V8 không bắt người dùng đã completed chạy lại wizard.

## 6. Catalog mở rộng

Backend cung cấp catalog an toàn trong `GET /onboarding/status`:

```json
{
  "provider_options": [
    {
      "kind": "codex",
      "display_name": "Codex",
      "description": "Tài khoản ChatGPT/Codex local",
      "recommended": true,
      "beta": false,
      "route_rank": 10
    }
  ]
}
```

Catalog đến từ một registry có thứ tự ổn định, không lặp danh sách ở Store, daemon, frontend và test.
`route_rank` là unique và quyết định position sau khi nén tập đã chọn. Codex có rank thấp hơn;
Claude Code luôn có rank cao nhất trong catalog production cho tới khi adapter Claude hỗ trợ
fall-through bình thường. Người dùng có thể đổi thứ tự sau ở trang Combo.
Mỗi runtime tách bốn trách nhiệm:

- `RuntimeDescriptor`: metadata/capability không bí mật;
- `InstallDriver`: detect/install/cancel/progress;
- `AuthDriver`: xác minh hoặc login Account;
- `ModelDriver/Runner`: discover model opaque và thực thi prompt.

Codex/Claude tiếp tục dùng implementation đã hardened. OpenCode dùng driver riêng nhưng đăng ký cùng
registry. Frontend render toàn bộ option từ status; icon không biết trước dùng fallback mark an toàn.

## 7. API và transition

### 7.1. `GET /onboarding/status`

Giữ lifecycle fields hiện có và thêm:

```json
{
  "providers": [
    {
      "kind": "codex",
      "status": "ready",
      "position": 0,
      "provider_id": "codex",
      "account_id": "...",
      "model_id": "gpt-5.6-terra"
    },
    {
      "kind": "claude-code",
      "status": "pending",
      "position": 1,
      "provider_id": "",
      "account_id": "",
      "model_id": ""
    }
  ],
  "provider_options": []
}
```

Không trả config dir, command, package path, credential, receipt hoặc log riêng tư.

### 7.2. `PUT /onboarding/providers`

Body: `{ "revision": N, "selected_kinds": ["codex","claude-code"] }`.

- danh sách unique, catalog-supported, tối đa bounded;
- chỉ nhận phase `provider`; request ở `connect/setup/persona/test/completed` bị từ chối trước mọi
  cancel hoặc cleanup;
- thứ tự request không quyết định route position; Store canonicalize theo catalog rank;
- thêm kind tạo row pending;
- bỏ pending chỉ xoá row;
- selected set mới bắt buộc chứa mọi row ready; ready row không thể OFF trong wizard. Điều này tránh
  xoá credential/config vừa cài mà không có transaction filesystem. Trang Combo/Provider quản lý
  Account live sau Complete;
- có thể thêm hoặc bỏ row pending khi các row khác đã ready. Nếu thao tác bỏ làm cho mọi row còn lại
  đều ready, Store cấp combo UUID và chuyển thẳng sang Persona trong cùng transaction;
- tăng global revision đúng một lần;
- lost response cùng exact list được nhận diện qua một-revision successor và trả snapshot hiện tại;
- stale/mismatch trả conflict, không side effect.

Giới hạn production ban đầu là 8 rows. Hai request cùng revision chỉ một request thắng; request còn
lại GET snapshot và chỉ được nhận là lost-response nếu tập canonical exact và revision đúng successor.

Toggle gọi endpoint này ngay để lựa chọn sống qua refresh. Confirmation OFF hiện có được giữ. Zero
selected hợp lệ nhưng không thể bắt đầu cài hoặc sang Persona.

### 7.3. `PUT /onboarding/provider`

Giữ endpoint để bắt đầu cài một hàng, không còn mang nghĩa thay toàn bộ lựa chọn.

- kind phải có row `pending` và phase phải là `provider`;
- snapshot ready khác được giữ nguyên;
- set active singleton clean, phase `connect`, revision +1;
- cùng exact lost-response successor idempotent; kind khác/stale fail closed.

Provider Connect tiếp tục POST `/llm/providers/{kind}/connect` với revision mới và immediate mode;
không hiện `Bắt đầu kết nối`.

### 7.4. Bind và Setup

- Connect success insert Account disabled và chuyển active slot `connect -> setup` như hiện có.
- `POST /onboarding/setup` nhận thêm exact `kind` cùng account/revision.
- Setup chọn model qua ModelDriver, cập nhật row thành `ready`; position đã được canonicalize theo
  catalog và không đổi theo thời điểm login.
- Còn row pending: global phase quay `provider`, clear active singleton, frontend render danh sách.
- Tất cả ready: tạo `staged_combo_id`, phase `persona`, clear active singleton.
- Response là standard authoritative status snapshot và mang phase kế tiếp (`provider` hoặc `persona`).
- Retry cùng input nhận diện cả hai exact successor, idempotent; no-model/config drift rollback nguyên
  transaction.

### 7.5. Quay lại và thay đổi lựa chọn

Back từ Connect: cancel + dispose + GET authoritative. Nếu vẫn `connect`, chỉ hiện Retry; không giả
lập phase Provider.

Back từ Persona gọi `POST /onboarding/back-to-providers` với exact revision. Transition thật về
`provider`, giữ các row ready nhưng xoá combo draft và receipt; persona fingerprint/file đã lưu được
giữ để form load lại mà không ghi đè. Không còn local-only `state.phase='provider'`. Người dùng có
thể thêm Provider rồi cài nhưng không thể OFF ready row. Endpoint chỉ nhận `persona/test`,
cancel-and-wait Test Chat trước mutation và tăng revision đúng một lần.

Các mutation Provider/Account/model bình thường phải từ chối xoá hoặc thay reference đang thuộc bất
kỳ stage row nào. SQLite hiện không có foreign key cho các bảng này nên guard phải nằm trong Store,
không chỉ ở handler Onboarding.

## 8. UI flow

Ở modal Provider:

- mỗi row có toggle độc lập; cả Codex và Claude Code có thể ON cùng lúc;
- ON pending: `Chưa cài. Bấm để cài.`;
- active: dùng progress bar/log hiện có;
- ready: `Đã sẵn sàng · Ưu tiên {n}`, toggle ON bị khoá trong wizard;
- Provider khác đang ON khi một job chạy hiển thị `Đang chờ`, CTA tạm khoá;
- OFF pending xác nhận như hiện có; ready không OFF trong wizard và giải thích có thể quản lý sau ở
  mục Provider/Combo;
- click CTA của một row chỉ cài row đó;
- xong một row mà còn pending thì tự quay danh sách, không tự cài hàng kế;
- xong row cuối cùng thì tự sang Persona;
- không có nút `Tiếp tục kết nối` hoặc `Bắt đầu kết nối` trong Onboarding.

One-flight dùng page generation + AbortController hiện có. Response muộn, double click hoặc hai tab
không thể tạo hai job/Account nhờ revision CAS và process-global Connect slot.

## 9. Test Chat và Complete nhiều member

### 9.1. Staging fingerprint

Fingerprint receipt bao gồm framing có domain cho toàn bộ ordered stage list:

`kind/provider_id/account_id/model_id/position` + persona fingerprint + display name.

Bất kỳ toggle, reinstall, model/account change, back hoặc reorder nào cũng invalidate receipt.

### 9.2. Test Chat

Runner dựng fallback staging theo position, mỗi entry pin đúng Account disabled tương ứng. Nó thử từ
trên xuống và trả `provider_id/model_id/position` thực sự trả lời. Không đọc live combo, không dùng Account
round-robin, không ghi Memory/session/live attempt telemetry. Một member hỏng được fallback sang member
kế; toàn bộ hỏng mới fail.

### 9.3. Complete

Một Store transaction duy nhất:

1. revalidate revision, receipt, persona và toàn bộ stage rows;
2. revalidate từng Provider/Account/model/config ownership bằng dữ liệu Store đọc lại; daemon chỉ
   cung cấp exact account-to-config-dir bindings cần kiểm tra filesystem, không cung cấp route để tin;
3. bật mọi Provider và staged Account;
4. disable nhưng không xoá các Account cũ cùng từng Provider;
5. thay toàn bộ Combo/legacy route bằng một Combo `fallback` có N member theo position;
6. đồng bộ Combo revision và legacy route revision monotonically;
7. set completed, clear receipt/staging rows và commit.

Mọi failpoint/commit error rollback toàn bộ. Live route cũ không đổi trước Commit.

## 10. OpenCode Beta safety gate

OpenCode mặc định `advertised=false`. Integration spike có thể chạy trong test/dev qua feature flag,
nhưng chỉ xuất hiện trong catalog production nếu implementation đáp ứng tất cả:

1. Detect/install đúng bản phát hành chính thức được pin và verify hash/signature; Windows resolve
   native `opencode-ai/bin/opencode.exe`, không mượn binary từ phần mềm khác trên máy.
2. Mỗi Account có app-owned XDG data/config/cache/state roots; không đọc auth/config global.
3. Workdir là thư mục rỗng do app sở hữu, không phải repo/home của người dùng.
4. Mọi lượt có `--pure`, config inline `permission:{"*":"deny"}`, `share:"disabled"`,
   `autoupdate:false`; không bao giờ có `--auto`, `--yolo`, `--dangerously-skip-permissions`, attach,
   file hoặc command flags.
5. Prompt chỉ đi qua stdin, không xuất hiện trong argv/process listing và không thể thành CLI flag.
6. Chỉ model discover được trong namespace `opencode/` và suffix `-free` được chấp nhận ở Beta.
7. Parser chỉ nhận NDJSON bounded UTF-8, một session ID nhất quán, text bounded và terminal finish;
   tool events hoặc malformed output fail closed.
8. Exact session bị xoá sau success/error/cancel. Nếu cancel trước event đầu, cleanup tìm exact nonce
   title trong account-isolated store; không xoá session ngoài ownership.
9. Process nằm trong managed Windows Job Object/Unix process group hiện có; cancel đợi reap.
10. Có OS containment thực sự giới hạn filesystem/process privilege. Job Object chỉ là lifecycle
    cleanup, không được tính là sandbox. Khi containment chưa có, feature flag chỉ dành cho spike với
    prompt synthetic, không nhận dữ liệu Zalo thật.
11. Tests chứng minh env/config/argv, banned flags, no tool events, cleanup compensation và không lộ
    prompt/output/token vào log.

Spike dùng model miễn phí nên không cần credential. Tích hợp upstream API key/OAuth của OpenCode là
một capability sau, không nằm trong slice này. Nếu bất kỳ gate production nào không chứng minh được,
registry bắt buộc để OpenCode `advertised=false`; Codex/Claude multi-provider vẫn ship đầy đủ.

Nguồn chính thức tham khảo:

- https://github.com/anomalyco/opencode
- https://github.com/anomalyco/opencode/blob/dev/LICENSE
- https://opencode.ai/docs/cli/
- https://opencode.ai/docs/providers
- https://opencode.ai/docs/permissions/

## 11. Error handling và recovery

- Catalog/status malformed: fail closed, không render CTA mutation.
- Toggle lost response: GET status; chỉ accept exact selected set successor.
- Daemon restart ở Connect: active job mất, row vẫn pending; UI hiển thị Retry cài đúng row.
- Setup lost response: exact kind/account/revision idempotence.
- Cancel install: row quay pending; Account/config chưa commit bị cleanup.
- Deselect ready cleanup lỗi: giữ row/Account ownership và Retry; không báo OFF giả.
- Model biến mất: row không ready; không làm hỏng row ready khác hoặc live route.
- Stale tab: 409 rồi authoritative GET; không cancel job hợp lệ trước preflight.
- OpenCode session cleanup lỗi: lượt trả lời fail closed và retry cleanup trước khi cho chạy lượt mới.

## 12. Ranh giới mã nguồn dự kiến

Store:

- `app_schema.go`: V8 table/index + migration.
- `app_onboarding_providers.go` (mới): transactional snapshot, stage-list/selection/begin/setup/back.
- `app_onboarding.go`: Test/Complete dùng ordered stages; giữ persona/receipt logic.
- `app_llm.go`: guard mutation Provider/Account/model đang được stage.

Daemon:

- `app_provider_registry.go` (mới): catalog/capabilities/driver lookup.
- `app_onboarding.go`: API projection, selection, orchestration, multi-stage pre/postflight.
- `app_llm_connect.go`: active stage bind theo kind/revision.
- `app_onboarding_runner.go` (mới): ordered pinned fallback.
- `app_opencode.go` (mới, safety-gated): detect/discover/run/parse/session cleanup.

Portal:

- `onboarding-contract.js`: catalog + stage normalization.
- `onboarding-provider-flow.js`: pure selected-set/reconcile transitions; suggested provider chỉ là
  badge, không được giả thành row ON.
- `onboarding-early-view.js`: generic rows, independent toggles/status/position.
- `onboarding.js`: orchestration only; không vượt giới hạn 800 dòng.
- `provider-connect.js`: giữ default Providers page, Onboarding vẫn immediate.
- `portal.css`: scoped multi-row states, desktop/mobile/focus.

## 13. Kiểm thử

### Store/migration

- fresh V8, V7 migration ở mọi phase, completed không tái onboarding;
- multi-select add/remove/idempotence/CAS/concurrency;
- snapshot transaction không trả singleton/rows lệch revision;
- begin chỉ selected pending; ready rows không bị xoá;
- bind/setup hai Provider, position deterministic theo catalog, auto Persona chỉ khi all-ready;
- pending removal, ready-removal rejection và rollback;
- multi-member Complete success, route order, revision monotonic, failpoint/Commit rollback.

### Daemon

- strict auth/body/catalog/duplicate/unsupported/limit validation;
- Provider/Account/model deletion guards khi còn stage reference;
- stale request không cancel valid Connect/Test;
- exact job cancellation fence và postwait recheck;
- status không rò internal path;
- multi pinned Test fallback và full-list drift detection;
- Complete revalidates every stage and config root.

### Portal

- ON đồng thời 2+ Provider, mỗi row CTA riêng;
- toggle survives refresh; OFF confirmation pending và ready row bị khoá rõ ràng;
- one active install, other selected rows stay visible/waiting;
- first success returns list, last success auto Persona;
- deterministic position/priority labels and status copy;
- lost response/revision conflict/dispose/cancel/reload/two tabs;
- normal Providers page still retains its label prompt;
- accessibility: separate toggle/CTA, no nested controls, alertdialog focus/Escape, mobile no overflow.

### OpenCode

- real-shaped NDJSON parser fixtures, malformed/tool/mixed-session/oversize rejection;
- exact safe argv/env/workdir and banned-arg canary;
- session delete success/error/cancel compensation;
- model namespace/filter/preference;
- no global config/auth/session access;
- optional local smoke only when installed, never consumes user credential.

### Gates

- focused Store/daemon/Portal tests;
- full overlay Go suite;
- full Portal Node/static suite;
- package/build gates;
- desktop and 390px CDP screenshot of both-selected/pending, one-ready/one-pending, and Connect.

## 14. Tiêu chí nghiệm thu

1. Codex và Claude Code có thể cùng ON trước khi cài.
2. Bấm CTA nào chỉ cài Provider đó; không có bước bắt đầu trung gian.
3. Provider thứ hai vẫn ON/pending sau khi Provider đầu ready và có thể cài tiếp.
4. Chỉ một connect job chạy; cancel/retry/refresh không tạo trùng.
5. Catalog rank trở thành fallback order rõ ràng và được lưu bền, không đổi theo thứ tự cài.
6. Onboarding chỉ sang Persona khi mọi Provider ON đều ready.
7. Test Chat chạy đúng staged fallback; Complete tạo một active Combo nhiều member atomically.
8. Live route/Account cũ không đổi trước Complete và rollback nguyên vẹn khi lỗi.
9. Catalog server-driven cho phép thêm runtime/model opaque mà không đổi frontend/schema.
10. OpenCode spike chứng minh được install/discovery/JSONL/session cleanup bằng dữ liệu synthetic,
    nhưng không hiện và không nhận dữ liệu Zalo production cho tới khi có OS containment được test.
11. Modal Tư Vấn Zalo trên nền lưới giữ nguyên; không có branding sản phẩm tham chiếu trong UI.
12. Toàn bộ tests/build/visual verification xanh trước commit cuối và push.

## 15. Giả định được uỷ quyền

Người dùng không muốn trả lời thêm và yêu cầu tự chọn phương án tốt nhất. Thiết kế chọn các mặc định:

- catalog rank = thứ tự chính/fallback; Codex trước, Claude Code cuối;
- toggle được persist ngay;
- cài thủ công từng Provider, không auto-start hàng kế;
- tự sang Persona khi hàng cuối ready;
- pending có thể OFF; ready bị khoá ON trong wizard để không xoá credential/config staging nửa chừng;
- OpenCode chỉ là spike ẩn dùng model miễn phí cho tới khi đủ OS containment và toàn bộ safety gate.

Các giả định này tránh thêm bước UI, giữ khả năng rollback và không mở rộng quyền runtime quá mức.
