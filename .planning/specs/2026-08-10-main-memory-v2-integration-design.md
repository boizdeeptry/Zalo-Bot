# Thiết kế tích hợp main Provider Routing với Session và Memory V2

**Ngày:** 2026-08-10

**Trạng thái:** Đã được người dùng xác nhận ngày 2026-08-10; triển khai theo TDD

**Repository:** `D:\TuvanZalo\_build\.worktrees\portal-m1`

**Nhánh tích hợp:** `integration/main-memory-v2`

**Đầu nhánh:**

- Feature: `9d6a4be275d109cc6e07f35d5c6ba8424e9c5de6`
- Main: `b6606d3f1de605f71ba4ea2f43e78459d716cdfa`
- Merge-base: `951ec9390d498b5c8f2ef73b0d244ee722552a56`

## 1. Bối cảnh

Nhánh feature hiện có Claude CLI session bền vững theo `thread_id`, prompt delta, context rotation,
Memory V2 theo đúng người đang nói, Memory Center và các deployment gate tương ứng. `origin/main`
mới có Provider/Account/Combo management, OAuth/connect flows, fallback/round-robin routing,
Codex/Gemini/Claude CLI adapters, no-default behavior và live install progress.

Mô phỏng merge cho thấy 8 content conflict trong 14 file thay đổi chồng lấn. Hai conflict thực sự
có tính kiến trúc:

1. Cả hai nhánh đều ghi `schema_version = 4`, nhưng dùng số 4 cho hai schema khác nhau.
2. Main định tuyến qua `appLLMRunner.Run(prompt)` dạng stateless, còn nhánh feature chỉ kích hoạt
   resume/Memory V2 khi runner hỗ trợ `appRunZaloSession`.

Nếu chỉ chọn `ours` hoặc `theirs`, database có thể bị đánh dấu đã migrate dù thiếu nửa schema, hoặc
bot có thể bỏ qua Provider routing hay bỏ qua session/Memory mà vẫn compile thành công.

## 2. Mục tiêu

- Hợp nhất toàn bộ tính năng Provider/Account/Combo của main với Session/Memory V2 của feature.
- Nâng cấp an toàn từ fresh database, legacy schema V1–V3, Feature V4 và Main V4.
- API Provider tiếp tục stateless nhưng luôn nhận prompt đầy đủ, hiện thời và có Memory V2 đúng
  subject; Claude Code tiếp tục resume đúng session của thread.
- Một lượt route fallback tới Claude Code chỉ gọi Claude một lần và ghi completion đúng session.
- Lượt được phục vụ bởi API/Codex/Gemini stateless không được giả vờ đã advance Claude session.
- Giữ no-provider silent behavior, attachment-local-only boundary, telemetry không chứa nội dung,
  per-thread serialization và cấu hình route có hiệu lực từ tin kế tiếp.
- Portal đồng thời có Providers, Combos và Memory thật, không hồi quy UI legacy.
- Giữ toàn bộ BuildApp drift guard, secret scan, binary signature, canary và rollback gate của cả
  hai nhánh.
- Không thay đổi hay deploy vào `D:\TuvanZalo\data`, `brain`, binary hoặc static assets đang chạy
  trong quá trình tích hợp.

## 3. Ngoài phạm vi

- Không biến API Provider thành stateful session hoặc lưu transcript riêng cho chúng.
- Không đồng bộ Claude transcript giữa các account, Provider hoặc máy.
- Không thay đổi chính sách Memory V2, số dòng snapshot, expiry hay approval lifecycle.
- Không thêm Provider, Combo strategy hoặc UI mới ngoài những gì đã có trên hai nhánh.
- Không tự push, merge vào `main` hoặc deploy live trước khi full verification và người dùng xác nhận.
- Không sửa dữ liệu thật để kiểm thử migration; mọi smoke test dùng fresh DB hoặc bản sao tạm thời.

## 4. Các phương án đã cân nhắc

### A. Schema bridge V5 và router có contract session-aware — chọn

Merge main vào một integration branch. Tạo schema đích V5 có cả hai capability và migration nhận
biết hình dạng thực thay vì tin tuyệt đối vào số version. Mở rộng routing để nhận đồng thời prompt
stateless đầy đủ và input session có cấu trúc; API/CLI stateless dùng prompt đầy đủ, Claude Code dùng
session input.

Ưu điểm: giữ trọn hai hệ tính năng, không làm API giả stateful, không mất resume khi fallback tới
Claude và tạo được regression tests rõ ràng. Nhược điểm: cần refactor nhỏ ở runner contract và bộ
migration test phong phú hơn.

### B. Đặt Provider routing ngoài session và chỉ bật session khi route chọn Claude trước lượt

Chọn Provider trước khi chuẩn bị prompt; nếu là API thì chạy pipeline cũ, nếu là Claude mới vào
Session/Memory V2. Cách này ít sửa router nhưng phải dự đoán route trước khi chạy fallback. Khi API
lỗi rồi fallback tới Claude, session input chưa được chuẩn bị đúng hoặc phải chạy lại orchestration,
dễ gọi hai lần và làm telemetry/context cursor sai.

### C. Giữ hai pipeline trả lời riêng

Portal chọn một chế độ “Provider routing” hoặc “Claude session”. Cách này tránh contract chung nhưng
làm người vận hành mất một nhóm tính năng khi bật nhóm còn lại, trái với mục tiêu tích hợp và tạo hai
nguồn hành vi khó bảo trì.

## 5. Kiến trúc được chọn

### 5.1. Một entrypoint trả lời duy nhất

Build seam tiếp tục thay upstream duty call bằng:

```go
a.appAnswerZalo(deps, threadID, question, reply, files)
```

`appAnswerZalo` sở hữu toàn bộ critical section theo thread và timeout như feature hiện tại. Bên
trong critical section, nó dựng runner cho đúng lượt bằng:

```go
a.appZaloRunner(deps.cfg, deps.run, threadID, len(files) > 0)
```

Không giữ seam main chèn `appZaloRunner` trực tiếp vào lời gọi upstream `answerZalo`, vì call site đó
đã được feature thay thế. Build gate phải chứng minh chỉ còn một production answer entrypoint và
entrypoint này bắt buộc đi qua route runner.

### 5.2. Input có hai biểu diễn prompt

`appZaloSessionRunInput` được mở rộng bằng một prompt stateless đầy đủ:

```go
type appZaloSessionRunInput struct {
    SessionID         string
    Prompt            string // bootstrap hoặc delta dành cho session-aware Claude
    StatelessPrompt   string // full prompt độc lập dành cho API/CLI stateless
    Resume            bool
    RecoverySessionID string
    BootstrapPrompt   string
}
```

`appSelectZaloSession` đã dựng `bootstrapPrompt` gồm persona, history hữu hạn, knowledge, attachment
directive, Memory V2 snapshot và output contract. Giá trị này là `StatelessPrompt`; `Prompt` tiếp
tục là bootstrap ở lượt mới hoặc delta ở lượt resume.

Không dùng delta prompt cho API Provider vì API không có transcript của các lượt trước. Không dùng
legacy `zc.Memory` vì Memory V2 phải được chọn theo trusted current speaker.

### 5.3. `appLLMRunner` hỗ trợ structured execution

Ngoài interface `zaloRunner.Run`, `appLLMRunner` triển khai `appZaloStructuredRunner`:

```go
func (r *appLLMRunner) appRunZaloSession(
    ctx context.Context,
    in appZaloSessionRunInput,
    step func(string),
) (appZaloRunResult, error)
```

Router dùng cùng một snapshot route và taxonomy lỗi hiện có:

- API Provider và Codex/Gemini CLI stateless nhận `in.StatelessPrompt`.
- Khi route tới Claude Code, runner được dựng theo model/account của route và phải hỗ trợ
  `appZaloStructuredRunner`; nó nhận nguyên `in`, gồm session ID, resume và recovery ID.
- Attachment vẫn chỉ được gửi tới local CLI đủ điều kiện; không có HTTP adapter nào nhận path hoặc
  nội dung file.
- No-provider/no-route tiếp tục trả `ErrZaloSilent` ở cổng vào.
- Telemetry tiếp tục chỉ lưu Provider/model/timing/outcome, không lưu prompt hay answer.

`appZaloRunResult` thêm cờ quan sát được, tên dự kiến `SessionAdvanced`. Claude structured success
trả `true`; API/CLI stateless success trả `false`.

### 5.4. Quy tắc cập nhật session

`appRunZaloStructured` luôn sanitize answer và áp dụng `memory_ops` theo snapshot đã chọn, bất kể
Provider nào trả lời. Sau đó:

- `SessionAdvanced=true`: giữ logic recovery, context estimate, message cursor và completion CAS
  hiện tại.
- `SessionAdvanced=false`: không advance cursor, turn count, context token hoặc Memory revision của
  Claude session.

Nếu mapping session vừa được tạo nhưng lượt đầu do API phục vụ, hàng session vẫn ở `turn_count=0`
và chưa có transcript Claude. `appSelectZaloSession` phải coi mọi session có `turn_count=0` là fresh
bootstrap với `Resume=false`. Vì vậy:

- nhiều lượt API liên tiếp đều stateless và dùng prompt đầy đủ;
- khi route lần đầu tới Claude, nó tạo transcript bằng `--session-id` thay vì resume một session
  chưa tồn tại;
- sau khi Claude thành công và completion tăng turn count, lượt Claude sau mới dùng `--resume`.

Nếu API lỗi và fallback tới Claude trong cùng lượt, router chuyển sang structured Claude với đúng
input đã chuẩn bị và chỉ Claude success mới advance session.

### 5.5. Schema đích V5 có capability detection

`appSchemaVersion` tăng thành 5. Schema đích gồm:

- session và Memory V2 tables/columns/indexes/triggers từ feature;
- Provider/model/route/attempt/account/combo tables từ main;
- no-default behavior của main: không seed active combo mặc định;
- `claude-code` system Provider vẫn được tạo idempotently để Portal có thể kết nối, nhưng bot không
  route cho tới khi người dùng tạo và kích hoạt Combo hợp lệ.

Migration nằm trong một transaction và tuân theo thứ tự:

1. Đọc/validate `app_meta.schema_version`; version âm hoặc không parse được vẫn fail closed.
2. Nếu version lớn hơn 5, không sửa schema hay hạ version.
3. Tạo foundation tables idempotently.
4. Chạy Memory V3/V4 capability migration bằng kiểm tra table/column/trigger thực tế, kể cả khi số
   version hiện tại đã là 4.
5. Tạo LLM/Combo schema idempotently; tách câu cập nhật `schema_version` khỏi SQL schema của main.
6. Chỉ sau khi mọi capability thành công mới ghi `schema_version=5` và commit.

Đường nâng cấp bắt buộc:

- Fresh/upstream DB không có `app_meta` → V5 đầy đủ.
- Legacy V1/V2/V3 → V5 đầy đủ và giữ dữ liệu cũ.
- Feature V4 → thêm Provider/Account/Combo, giữ session/Memory và revisions.
- Main V4 → thêm session/Memory V2, giữ Provider/account/combo/route/telemetry.
- V5 chạy migration lần hai không đổi dữ liệu người dùng.
- Future version lớn hơn 5 được giữ nguyên, không bị downgrade.

### 5.6. Portal và API

Routes là phép hợp:

- Provider/model/account/connect/route/combo/status routes từ main.
- Memory summary/member/detail/mutation routes từ feature.

Navigation đích giữ `Providers`, `Combos` và `Memory`; bỏ placeholder Memory của main nhưng giữ trang
Memory thật của feature. Các file Git auto-merge như `app-main.js`, `portal.css`, DOM harness và
navigation tests vẫn phải được review/test về hành vi, không coi auto-merge là đã đúng.

### 5.7. BuildApp và package gate

`Apply-AppSeams` giữ các seam của feature:

- route registration;
- app migration;
- workflow evaluation;
- `appAnswerZalo` và `appRunZalo`;
- operator rewrite thành structured lesson;
- Zalo deep-link.

Provider routing được gọi bên trong overlay `appAnswerZalo`, không được chèn lần hai tại upstream
call site. Bộ gate hợp nhất phải giữ:

- exact-once seam và toàn-tree call-site count;
- no-default/provider credential protections;
- overlay cleanliness;
- package secret scanning;
- Memory/session/provider binary signatures;
- vetted Go skip list, không mở rộng regex để che test mới;
- canary, copied-data smoke test và rollback evidence.

## 6. Xử lý lỗi và bất biến an toàn

- Lỗi schema trước commit rollback toàn bộ; không nâng version nửa chừng.
- Session state/store lỗi giữ fail-safe hiện tại; không ghi completion giả cho stateless response.
- Memory read/apply lỗi không làm mất answer, nhưng không được lộ Memory của speaker trước.
- Provider configuration/policy errors dừng route theo taxonomy main; temporary errors mới fallback.
- Claude terminal trong một route đã cấu hình mà không chạy được phải escalation, không bị nuốt như
  trạng thái chưa cấu hình.
- Attachment/history inspection lỗi chọn đường local chậm/an toàn, không đoán là không có file.
- Không log prompt, answer, API key, OAuth token, customer path hoặc Memory content.
- Không có thao tác test nào ghi vào database hoặc thư mục live.

## 7. Đơn vị thay đổi dự kiến

- `appmode/overlay/internal/store/app_schema.go`: schema V5 và bridge migration.
- `appmode/overlay/internal/store/app_schema_test.go`: fixture cho hai biến thể V4 và idempotence.
- `appmode/overlay/internal/daemon/app_zalo_session_runner.go`: structured/stateless prompt contract và
  session advancement result.
- `appmode/overlay/internal/daemon/app_zalo_session_hook.go`: route composition, virgin-session rule
  và completion gating.
- `appmode/overlay/internal/daemon/app_llm_router.go`: structured route execution dùng chung snapshot,
  taxonomy và telemetry.
- `appmode/overlay/internal/daemon/app_llm_router_test.go`: routing/session behavior matrix.
- `appmode/overlay/internal/daemon/app_routes.go`: hợp routes.
- `appmode/overlay/internal/daemon/app_foundation_test.go`: hợp route coverage.
- `appmode/overlay/internal/webui/static/core/router.js`: Providers + Combos + Memory navigation.
- Các Portal files Git tự merge: review và chỉ sửa khi regression test chứng minh cần thiết.
- `scripts/BuildApp.psm1`: seam/gate hợp nhất.
- `tests/build-app.Tests.ps1`: source/build/package assertions hợp nhất.
- `.planning/plans/2026-08-06-provider-routing.md`: giữ shipped status và ghi chú còn đúng.

Tên field/helper cuối cùng có thể đổi để phù hợp convention hiện có, nhưng không được thay đổi các
bất biến công khai đã nêu ở trên.

## 8. Chiến lược kiểm thử TDD

### 8.1. Migration

- Viết fixture Feature V4 và chứng minh test đỏ vì thiếu LLM tables trước khi có V5 bridge.
- Viết fixture Main V4 và chứng minh test đỏ vì thiếu Memory/session capability.
- Kiểm tra dữ liệu mẫu ở cả hai phía còn nguyên sau upgrade.
- Kiểm tra fresh, V1–V3, idempotent second open, rollback khi một capability fail và future version.

### 8.2. Routing và session

- API success dùng full `StatelessPrompt`, không dùng delta và không advance Claude session.
- Nhiều API success liên tiếp giữ session virgin; Claude đầu tiên dùng fresh `--session-id`.
- API temporary failure → Claude structured fallback dùng đúng session input và advance một lần.
- Claude resume success/recovery/rotation giữ regression behavior hiện có.
- No provider/no active combo vẫn silent; configured terminal failure vẫn escalation.
- Attachment chỉ đi local CLI và không chạm HTTP adapter.
- Provider response có valid/invalid `memory_ops` giữ policy và answer sanitization hiện tại.

### 8.3. Portal/API/build

- Foundation route test chứa cả Provider/Account/Combo và Memory routes.
- Navigation test chứng minh Providers, Combos, Memory đều mount đúng trang và không còn trang Memory
  giữ chỗ.
- Toàn bộ Node tests giữ legacy shell, Knowledge, Agents và `/zalo` behavior.
- Pester chứng minh build seam exact-once, không bypass routing/session, không nới skip list và package
  không chứa secret.

### 8.4. Verification cuối

- Go overlay/store/daemon tests trên staged upstream.
- Toàn bộ Portal Node tests.
- Pester BuildApp/package tests.
- Build package thực vào artifact tách biệt.
- Smoke migration trên bản sao của Feature V4 và Main V4 data.
- E2E local cho no-provider, API Provider, Claude session resume, fallback và Memory theo group member.

## 9. Tiêu chí chấp nhận

- Merge không còn conflict marker và integration branch chứa cả lịch sử main lẫn feature.
- Database từ cả hai loại V4 mở được, nâng thành V5 và giữ dữ liệu/cấu hình.
- Providers, Accounts, Combos, route telemetry, Session và Memory Center cùng hoạt động.
- API Provider luôn nhận prompt đầy đủ; Claude Code vẫn resume theo người/nhóm và xoay context đúng.
- Stateless response không advance Claude cursor; fallback tới Claude advance đúng một lần.
- Memory V2 vẫn theo trusted speaker, không lẫn người trong group và không tạo model call phụ.
- No-default, attachment-local-only, credential secrecy và telemetry content-free vẫn được giữ.
- Legacy Portal UI, Knowledge, Agents, `/zalo`, Provider connect progress và Memory navigation qua
  regression tests.
- Full verification xanh trên artifact tách biệt trước khi có bất kỳ đề xuất deploy nào.

## 10. Rollout và rollback

1. Hoàn thành và commit trên `integration/main-memory-v2`.
2. Build artifact mới ngoài thư mục live.
3. Copy database cần smoke test sang thư mục tạm; không mở DB live bằng binary tích hợp.
4. Chạy canary/migration/API tests trên bản sao.
5. Chỉ khi người dùng yêu cầu deploy: dừng daemon có kiểm soát, backup binary/static/data metadata,
   thay code artifact nhưng giữ nguyên `brain` và `data`, rồi chạy health/E2E.
6. Nếu health hoặc migration validation thất bại, khôi phục artifact trước và bản backup DB tương ứng;
   không chạy binary cũ trên DB đã nâng mà chưa có rollback compatibility xác nhận.

## 11. Rủi ro còn lại và biện pháp

- **API stateless khác Claude transcript:** luôn dùng full stateless prompt và chỉ Claude success mới
  advance session.
- **Hai V4 cùng số:** capability detection và hai fixture upgrade độc lập; không branch chỉ theo số.
- **Auto-merge UI hợp dòng nhưng sai lifecycle:** chạy full DOM/navigation tests và E2E ba trang.
- **Build seam drift do upstream mới:** exact-once + whole-tree call-site assertions fail closed.
- **Migration thành công nhưng binary cũ không đọc V5:** rollout luôn backup và không tự downgrade;
  deploy chỉ sau explicit approval.
- **Provider main tiếp tục thay đổi trong lúc tích hợp:** khóa target tại `b6606d3`; update main lần nữa
  phải mô phỏng merge và verification lại, không lặng lẽ kéo thêm commit vào giữa plan.

Không còn câu hỏi kiến trúc mở. Chế độ triển khai khuyến nghị là TDD.
