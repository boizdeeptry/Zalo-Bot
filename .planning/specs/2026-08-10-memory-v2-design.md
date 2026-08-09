# Memory V2 cho Portal Zalo

**Ngày:** 2026-08-10

**Trạng thái:** Thiết kế đã được người dùng xác nhận

**Repository:** `D:\TuvanZalo\_build\.worktrees\portal-m1`

**Nhánh:** `feature/portal-m1-foundation`

## 1. Bối cảnh

Portal hiện có Memory Center, một Claude Code session bền cho mỗi Zalo thread, và cơ chế
revision chỉ gửi lại Memory khi dữ liệu thay đổi. Claude tự chắt lọc một trường `note` rồi
upstream chèn thẳng một dòng vào `zalo_memory`.

Cơ chế này hữu ích cho chat riêng nhưng chưa đủ an toàn cho nhóm: ghi chú tự động không gắn
`author_uid`, không có thao tác thay thế hoặc quên, không xử lý xung đột, và không có trạng thái
chờ duyệt cho dữ liệu nhạy cảm. Một nhóm có nhiều người nhưng chỉ có một Claude session nên
snapshot Memory cũng phải đổi theo người đang nói mà không làm rò thông tin giữa thành viên.

Memory V2 thay trường ghi chú append-only bằng các thao tác có cấu trúc, được áp chính sách tại
Go daemon. Việc phân tích vẫn nằm trong chính lượt Claude đang trả lời; không có model call thứ
hai và repository nguồn `agentdc` tiếp tục chỉ được đọc khi đóng gói.

## 2. Quyết định đã chốt

- Memory cá nhân trong nhóm có phạm vi `thread_id + subject_uid`; cùng một UID ở nhóm khác có
  Memory riêng.
- Memory chung của một thread vẫn được hỗ trợ bằng `subject_uid = ''`, chủ yếu để giữ tương
  thích dữ liệu nhóm cũ và cho người trực ghi quy tắc riêng của hội thoại.
- Thông tin mới ít nhạy cảm, chắc chắn được tự kích hoạt; thông tin nhạy cảm, chưa chắc hoặc
  mâu thuẫn chuyển sang chờ duyệt.
- Thay thế luôn chờ người trực duyệt; Memory cũ tiếp tục hoạt động cho tới lúc duyệt.
- Khách được yêu cầu quên trực tiếp. Yêu cầu rõ xóa vật lý đúng dòng/lineage; yêu cầu mơ hồ
  không xóa và Claude phải hỏi lại.
- Hạn dùng phụ thuộc loại: `interest`/`preference` 30 ngày, `profile`/`family` 180 ngày, được
  xác nhận lại thì gia hạn, ghim thì không hết hạn.
- Claude đề xuất `memory_ops` trong cùng JSON câu trả lời. Policy engine cục bộ là nơi duy nhất
  quyết định ghi gì vào database.
- UI Portal cũ được giữ nguyên ngôn ngữ hình ảnh và mở rộng ngay trong trang Memory.

## 3. Mục tiêu

- Không để Memory của hai thành viên trong cùng nhóm lẫn sang nhau.
- Không để Memory của cùng một UID đi xuyên nhóm.
- Chống dòng trùng, biểu diễn xung đột thành đề xuất thay thế và hỗ trợ quên có chủ đích.
- Giữ dữ liệu nhạy cảm hoặc chưa chắc khỏi Claude prompt cho tới khi người trực duyệt.
- Đưa đúng snapshot Memory vào session mới hoặc session đang resume mà không gửi lại vô ích
  trong chat riêng/cùng người nói liên tiếp.
- Giữ câu trả lời Zalo khả dụng khi phần Memory lỗi phân tích hoặc lỗi ghi.
- Migrate dữ liệu V3 tại chỗ, giữ nguồn, pin, bài học chung và lịch sử session hiện có.
- Không thêm model call và không sửa trực tiếp repository nguồn `agentdc`.

## 4. Ngoài phạm vi

- Chia một Zalo group thành nhiều Claude Code session.
- Chia sẻ Memory của một người giữa chat riêng và các nhóm.
- Vector database hoặc model thứ hai để phân loại Memory.
- Xóa lịch sử tin nhắn Zalo khi khách yêu cầu quên Memory. Lệnh quên trong thiết kế này chỉ
  xóa dữ liệu Memory; lịch sử vận hành tuân theo chính sách lưu trữ riêng của ứng dụng.
- Thay đổi Knowledge Base, quy tắc trích dẫn, global lessons hoặc Provider routing.
- Tự suy UID từ tên hiển thị. Thiếu UID đáng tin cậy thì không được tạo Memory cá nhân.

## 5. Hợp đồng `memory_ops`

Mọi prompt bootstrap và delta thêm cùng một contract phiên bản hóa. JSON trả lời tiếp tục giữ
các field upstream đang dùng, đặt `note` thành chuỗi rỗng, và có thể thêm tối đa ba thao tác:

```json
{
  "answers": ["..."],
  "sources": [],
  "clarify": "",
  "handoff": "",
  "react": "",
  "close": false,
  "note": "",
  "memory_ops": [
    {
      "action": "add",
      "memory_key": "profile.occupation",
      "value": "là dược sĩ, có nhà thuốc riêng",
      "category": "profile",
      "confidence": 0.96,
      "target_id": 0
    }
  ]
}
```

### 5.1 Field và giới hạn

- `action`: chỉ `add`, `replace`, `forget`.
- `memory_key`: khóa ngữ nghĩa do model đặt, tối đa 80 byte, khớp
  `[a-z0-9][a-z0-9._:-]{0,79}`. Prompt phải tái sử dụng key đang có thay vì đặt key mới cho
  cùng một sự thật.
- `value`: một dòng, tối đa 240 rune sau khi trim; bắt buộc cho `add`/`replace`, rỗng cho
  `forget`.
- `category`: chỉ `profile`, `family`, `interest`, `preference`, `health`, `financial`,
  `address`, `identity`, `order`.
- `confidence`: số hữu hạn trong đoạn `[0,1]`.
- `target_id`: bằng `0` với add; bắt buộc dương với replace/forget và phải trỏ tới Memory active
  đã được đưa vào snapshot của đúng `thread_id + subject_uid` hiện tại.

Claude không được phát `thread_id`, `subject_uid`, ngày hết hạn, trạng thái duyệt hoặc pin. Các
giá trị đó đều do ứng dụng tạo từ metadata tin nhắn và policy cố định.

### 5.2 Tương thích upstream

Overlay thêm contract V2 sau contract upstream cho cả bootstrap và delta. Kết quả Claude được
overlay đọc trước khi trả về `answerZalo`:

1. Nếu JSON hợp lệ, overlay lấy `memory_ops`, đặt `note = ""`, rồi trả JSON đã làm sạch cho
   upstream; field lạ `memory_ops` được Go JSON decoder upstream bỏ qua.
2. Nếu `memory_ops` sai kiểu hoặc có item sai, item đó bị bỏ và câu trả lời vẫn đi tiếp.
3. Nếu toàn bộ JSON sai, overlay không biến nó thành câu trả lời hợp lệ; upstream tiếp tục dùng
   đường escalation hiện có.
4. Bất kể model có cố trả `note` cũ, overlay luôn xóa nó để `AddZaloMemory` legacy không ghi trùng.

## 6. Xác định chủ thể đáng tin cậy

Ứng dụng lấy `currentZaloMsgID` từ reply quote như hiện tại, rồi đọc lại đúng inbound row trong
`zalo_messages` bằng `(thread_id, zalo_msgid)` để lấy `author_uid`.

- Chat riêng: nếu inbound row hợp lệ nhưng UID trống do dữ liệu cũ, chỉ được fallback
  `subject_uid = thread_id` khi thread được xác định là chat một-một.
- Chat nhóm: UID trống hoặc không tìm thấy inbound row nghĩa là không có chủ thể đáng tin cậy.
  Lượt vẫn được trả lời, chỉ Memory chung được đọc và mọi `memory_ops` cá nhân bị bỏ kèm log sạch.
- UID/model-provided display name không bao giờ được dùng làm khóa phân quyền.
- `target_id` được kiểm lại trong transaction; không tin danh sách model trả về.

## 7. Mô hình dữ liệu V4

### 7.1 Mở rộng `zalo_memory`

Giữ các cột V3 và thêm:

- `memory_key TEXT NOT NULL DEFAULT ''`
- `category TEXT NOT NULL DEFAULT 'profile'`
- `confidence REAL NOT NULL DEFAULT 1`
- `status TEXT NOT NULL DEFAULT 'active'`
- `proposal_action TEXT NOT NULL DEFAULT ''`
- `supersedes_id INTEGER NOT NULL DEFAULT 0`
- `last_confirmed_at TEXT NOT NULL DEFAULT ''`
- `expires_at TEXT`

Giá trị status hợp lệ là `active`, `pending`, `expired`, `superseded`. `proposal_action` chỉ có
giá trị trên pending row (`add` hoặc `replace`). Prompt chỉ đọc active row chưa hết hạn.

Index mới phục vụ các truy vấn chính:

- `(thread_id, uid, status, pinned, id)` cho snapshot.
- `(thread_id, uid, memory_key, status)` cho duplicate/conflict/lineage.
- `(status, expires_at)` cho expire/pending cleanup.

Không đặt unique constraint trên `memory_key`: một key có thể có một active row và một pending
replacement cùng lúc. Tính bất biến được giữ bằng transaction service và test concurrency.

### 7.2 Revision theo chủ thể

Thêm bảng:

```sql
CREATE TABLE app_memory_subject_revisions (
  thread_id TEXT NOT NULL,
  uid       TEXT NOT NULL,
  revision  INTEGER NOT NULL,
  PRIMARY KEY (thread_id, uid)
);
```

`uid = ''` là revision Memory chung của thread. Revision chủ thể chỉ tăng khi tập active dùng
cho prompt của chủ thể đó thay đổi. Pending insert/reject không tăng revision. Approve
replacement tăng đúng một lần cho transaction dù vừa supersede row cũ vừa activate row mới.

Các trigger Memory V3 được thay bằng service-level bump trong cùng transaction. Trigger global
lessons giữ nguyên. Mọi mutation Memory từ Portal và `memory_ops` phải đi qua service V2; trường
`note` legacy bị vô hiệu hóa tại seam nên không còn đường production nào chèn ngoài service.

### 7.3 Session cursor

Mở rộng `app_zalo_cli_sessions` bằng:

- `memory_subject_uid TEXT NOT NULL DEFAULT ''`
- `memory_subject_revision INTEGER NOT NULL DEFAULT 0`
- `memory_common_revision INTEGER NOT NULL DEFAULT 0`

Giữ `memory_revision` V3 để migrate/đọc tương thích trong một phiên bản; orchestration V2 không
dùng nó để quyết định snapshot cá nhân. Cursor mới chỉ được commit sau lượt Claude thành công,
cùng CAS generation, message cursor và lessons revision hiện có.

## 8. Policy engine

Policy engine nhận `thread_id`, `subject_uid`, `source_message_id`, tối đa ba op và một clock có
thể inject trong test. Tất cả op của một answer được xử lý tuần tự trong một transaction ngắn.
Một op sai chỉ bị bỏ; lỗi database rollback toàn bộ batch Memory nhưng không làm rơi câu trả lời.

### 8.1 Add

- Normalize key và value để so trùng: trim, Unicode lower-case, gom whitespace; bản hiển thị giữ
  nguyên chữ đã trim.
- Nếu có active row cùng key và normalized value: cập nhật `last_confirmed_at`, gia hạn theo
  category, không tạo row. Pinned row giữ `expires_at = NULL`.
- Nếu có active row cùng key nhưng value khác: biến đề xuất thành pending replacement, kể cả
  model gửi action `add`.
- Nếu không xung đột và category là `profile`, `family`, `interest` hoặc `preference`, confidence
  từ `0.85` trở lên: tạo active row.
- Confidence dưới `0.85`, category không nằm trong allowlist tự động, hoặc category nhạy cảm:
  tạo pending add.

### 8.2 Replace

- Luôn tạo pending replacement.
- Target phải là active row đúng thread, UID và đã xuất hiện trong authoritative snapshot của
  lượt này; sai target thì bỏ op.
- Chỉ cho một pending replacement chưa hết hạn trên cùng target. Đề xuất mới giống hệt chỉ cập
  nhật nguồn/confidence/thời điểm; đề xuất khác thay proposal cũ để Portal không có hai lựa chọn
  cạnh tranh.
- Khi duyệt: transaction kiểm optimistic revision, chuyển target thành superseded, chuyển
  proposal thành active, đặt thời hạn từ lúc duyệt và bump subject revision đúng một lần.
- Khi từ chối: xóa vật lý pending row; target không đổi.

### 8.3 Forget

- Chỉ nhận target active đúng scope đã có trong snapshot. Prompt chỉ phát `forget` khi yêu cầu
  của khách chỉ rõ Memory cần quên; nếu không rõ phải dùng `clarify` và không phát op.
- Transaction xóa vật lý toàn bộ lineage cùng `thread_id + uid + memory_key`: active row, các
  superseded row và pending replacement liên quan. Tin nhắn Zalo nguồn không bị xóa.
- Nếu target đã biến mất, thao tác trở thành idempotent no-op.
- Portal delete một active Memory dùng cùng semantics xóa lineage. Delete pending chỉ xóa đúng
  proposal.

### 8.4 Nhạy cảm, hạn dùng và pin

- `health`, `financial`, `address`, `identity`, `order` luôn pending, không phụ thuộc confidence.
- `interest` và `preference`: hết hạn sau 30 ngày.
- `profile` và `family`: hết hạn sau 180 ngày.
- Category nhạy cảm được duyệt: hết hạn sau 180 ngày, trừ khi người trực ghim.
- Ghim đặt `expires_at = NULL`; bỏ ghim tính hạn mới từ `last_confirmed_at`, hoặc từ thời điểm bỏ
  ghim nếu timestamp cũ đã hết hạn.
- Trước mỗi snapshot, store chuyển các active row đến hạn sang expired và bump revision một lần
  cho từng scope bị ảnh hưởng. Pending quá 30 ngày bị xóa, không bump prompt revision.
- Khôi phục expired row đặt active, `last_confirmed_at = now`, tính hạn mới và bump revision.

## 9. Dựng snapshot và đồng bộ session

Snapshot cho một lượt gồm hai scope tách biệt:

- `thread_common`: active Memory có `uid = ''` của thread.
- `current_subject`: active Memory có `uid = subject_uid` của người đang nói.

Mỗi scope chọn tối đa 12 dòng, ưu tiên pinned rồi mới nhất, sau đó render cũ-trước như V3. Record
JSONL chứa ID, key, category và text để model có thể target chính xác; tất cả nằm trong boundary
untrusted, non-instructional, non-citable.

- Session mới/rotation/recovery: bootstrap có snapshot common + current subject đầy đủ.
- Chat riêng hoặc cùng một người nói liên tiếp: không gửi refresh khi subject/common revision
  đều khớp cursor.
- Khi người nói trong nhóm đổi UID: luôn gửi authoritative replacement cho `current_subject`,
  kể cả revision của người mới không đổi. Directive nói rõ snapshot của người trước không còn là
  Memory của current speaker.
- Common revision đổi thì gửi common replacement; subject revision đổi thì gửi subject
  replacement. Delete-to-empty vẫn gửi `items: []`.
- Mutation xảy ra trong lúc Claude chạy không được đánh dấu đã đồng bộ; cursor cũ được commit và
  lượt sau nhìn thấy revision mới.
- Không có UID đáng tin cậy trong nhóm: chỉ common snapshot được dùng, current subject scope rỗng
  với directive cấm quy thuộc Memory cũ cho người đang nói.

## 10. Portal Memory Center

### 10.1 Read model

`GET /memory/threads/{tid}` nhận query `uid` và trả:

- Thread metadata và danh sách thành viên có tên/avatar từ `zalo_group_members` + `zalo_users`.
- Count active/pending/expired cho Memory chung và từng UID.
- Danh sách theo scope được chọn, gồm provenance, key, category, confidence, trạng thái, hạn dùng,
  replacement target và optimistic revision.
- Sync state riêng của scope: đã đồng bộ nếu session generation hiện tại có cùng
  `memory_subject_uid` và subject/common revisions khớp; nếu khác thì “sẽ đồng bộ khi người này
  nhắn tiếp”.

Overview hiện có mở rộng metrics nhưng giữ query/search/pinned filter và response fields V3 để
UI cũ không gãy.

### 10.2 Mutation API

Giữ các route CRUD hiện tại và mở rộng body bằng `uid`, `memory_key`, `category`,
`expected_revision`. Thêm:

- `POST /memory/threads/{tid}/{id}/approve`
- `POST /memory/threads/{tid}/{id}/reject`
- `POST /memory/threads/{tid}/{id}/restore`

Approve cho phép body sửa `memory_key`, `value`, `category` trước khi áp dụng. Mọi mutation yêu
cầu Portal mutation header hiện có. Revision cũ trả HTTP `409`; sai scope/target hoặc enum trả
`422`; ID không có trả `404`; lỗi database trả thông báo chung `500` và chỉ log chi tiết server.

### 10.3 Giao diện

- Chat nhóm có selector `Chung cho nhóm` và từng thành viên, kèm active/pending count.
- Chat riêng tự chọn UID của người đó; không bắt người trực chọn lại.
- Ba vùng: `Đang hoạt động`, `Chờ duyệt`, `Đã hết hạn`.
- Pending replacement hiển thị cũ/mới cạnh nhau, provenance, category và confidence; thao tác
  `Duyệt`, `Sửa rồi duyệt`, `Từ chối`.
- Active có sửa, ghim, xóa. Expired có khôi phục, ghim, xóa.
- Form thêm thủ công yêu cầu scope rõ ràng; manual entry trở thành active và giữ source
  `operator`.
- Giữ focus trap, Escape, focus restoration, `aria-live`, debounce search, abort controller và
  stale-response guard đã có. Mutation conflict giữ draft đang sửa và yêu cầu tải lại.

## 11. Migration và tương thích

- Tăng `appSchemaVersion` từ 3 lên 4 và chạy toàn bộ thay đổi trong transaction.
- Thêm cột/index/table/session cursor theo hướng idempotent.
- Chat một-một cũ: backfill `zalo_memory.uid = thread_id` chỉ khi thread được chứng minh không
  phải group. Group/không xác định giữ `uid = ''` và hiện là Memory chung.
- Row cũ nhận key tiền định `legacy.<id>`, category `profile`, confidence `1`, status active,
  `last_confirmed_at = updated_at/created_at`. Non-pinned hết hạn 180 ngày từ thời điểm migrate;
  pinned có expiry null.
- Seed subject/common revision từ active rows sau migration. Session cursor mới để zero/empty để
  lượt đầu sau nâng cấp buộc refresh đúng scope.
- Bài học chung, provenance, pin limit, source message, route cũ và dữ liệu `/zalo` giữ nguyên.
- Build seam tiếp tục copy source Git-tracked sang stage, áp overlay và không chạm repository
  nguồn. Package assertion thêm chữ ký schema V4, contract `memory_ops` và endpoint approve.
- Trước khi thay binary đang chạy, deploy flow sao lưu SQLite cùng các file cấu hình hiện hành.

## 12. Đơn vị triển khai

- `internal/store/app_schema.go`: migration V4, index, revision seed và cursor columns.
- `internal/store/app_memory_v2.go`: policy transaction, snapshot theo subject, expiry, proposal
  lifecycle và optimistic revision. `app_memory.go` giữ read models/legacy wrappers, được thu gọn
  nếu cần nhưng không trộn policy với SQL presentation.
- `internal/daemon/app_zalo_memory_contract.go`: kiểu `memory_ops`, JSON sanitize, subject lookup
  và gọi policy service.
- `internal/daemon/app_zalo_session_prompt.go`: contract suffix và hai authoritative Memory
  scopes.
- `internal/daemon/app_zalo_session_hook.go`: lấy subject snapshot, quyết định refresh, xử lý op
  sau model result và commit cursor sau thành công.
- `internal/store/app_zalo_cli_session.go`: cursor subject/common và CAS persistence.
- `internal/daemon/app_memory.go`, `app_routes.go`: read/mutation HTTP V2.
- `internal/webui/static/pages/memory.js`, `portal.css`: member selector, status sections và
  proposal actions trong UI legacy.
- `scripts/BuildApp.psm1`: seam/assertion tối thiểu cần thiết để contract V2 tồn tại trong stage
  mà upstream không bị sửa.
- Các test song hành trong cùng package/module kiểm hành vi qua public store/API/prompt/page
  interfaces.

## 13. Xử lý lỗi và an toàn

- Memory là phụ trợ; lỗi Memory không được làm mất, đổi hoặc gửi hai lần câu trả lời Zalo.
- Không log prompt, value, raw answer, tên khách hoặc nội dung nhạy cảm. Log chỉ thread hash/ID
  vận hành, op kind, safe error code và target numeric ID khi cần.
- Mọi dữ liệu từ model và Memory đi vào prompt dưới JSONL escaped boundary với nhắc lại rằng đây
  không phải instruction hoặc nguồn trích dẫn.
- Không dùng fuzzy match để xóa. Forget/replace yêu cầu target ID đúng scope; dedupe chỉ dùng
  normalized key + value trong scope hiện tại.
- Transaction và optimistic revision ngăn hai tab/operator duyệt đè nhau.
- Input HTTP tiếp tục có body cap, strict single JSON document và không trả lỗi SQLite/raw secret
  cho trình duyệt.

## 14. Chiến lược kiểm thử

Tất cả behavior mới dùng TDD: test public thất bại đúng lý do trước, implementation tối thiểu,
sau đó chạy lại package liên quan.

### Store/migration

- V3 lên V4 giữ text, pin, provenance và phân loại chat riêng/group đúng.
- Hai UID trong một thread đọc hai snapshot khác nhau; cùng UID ở thread khác không nhìn thấy.
- Safe/high-confidence add active; sensitive/low-confidence add pending.
- Exact duplicate chỉ confirm/gia hạn; same-key different-value trở thành pending replacement.
- Approve/reject/restore/delete lineage là transaction, revision tăng đúng số lần.
- Expiry, pending cleanup và pin/unpin dùng fake clock.
- Concurrent expected revision cũ trả conflict; sai target scope không mutate.

### Session/prompt

- Bootstrap chứa contract V2, common + đúng subject; `note` luôn bị sanitize rỗng.
- Same subject/revision resume không gửi Memory lại.
- Đổi speaker buộc authoritative replacement và không chứa item của speaker trước.
- Common/subject revision thay độc lập; empty replacement xóa snapshot cũ.
- Mutation trong lúc run còn pending tới lượt sau; completion CAS không ghi đè generation khác.
- Malformed `memory_ops`, thiếu UID nhóm và store error không làm rơi answer.
- Một lượt chỉ gọi Claude runner đúng một lần.

### HTTP/Portal

- Auth/mutation guard, query UID encoding, strict JSON, `404/409/422/500` mapping.
- Member selector, count, ba status section, replacement diff, approve/edit/reject/restore.
- Loading/empty/error, keyboard focus lifecycle, conflict giữ draft, abort/dispose và stale result.
- Không render Memory của UID khác trong detail đang chọn.

### Package/regression

- `npm test` cho toàn bộ Portal.
- `tests/run-overlay-go-tests.ps1 -Package all` cho staged upstream + overlay.
- Pester build/package tests xác nhận schema/API/contract và quét dữ liệu nhạy cảm.
- Build package thực, chạy daemon trên bản sao data, smoke test migration + Memory API trước khi
  thay binary đang dùng.

## 15. Tiêu chí chấp nhận

- Hai thành viên trong cùng group không bao giờ nhận Memory của nhau trong prompt.
- Memory không đi xuyên group, kể cả cùng `subject_uid`.
- Không có model/Claude call bổ sung cho Memory.
- Unchanged private/same-speaker Memory không bị gửi lại; speaker change luôn có snapshot đúng.
- Pending/expired/superseded không vào prompt; delete-to-empty được đồng bộ ở lượt tiếp theo.
- Safe add, pending sensitive/conflict, dedupe, approve, reject, restore, expiry, pin và forget đều
  có regression test.
- Memory subsystem lỗi vẫn giữ nguyên hành vi answer/handoff/react/close của lượt Zalo.
- V3 data migrate không mất text, pin, provenance, lessons hoặc session mapping.
- Portal giữ UI legacy, hoạt động desktop/mobile và đáp ứng keyboard/accessibility contract.
- Toàn bộ Portal test, staged Go test và package verification đạt trước deploy.

## 16. Rủi ro và biện pháp

- **Model đặt key không ổn định:** prompt luôn đưa ID/key hiện có, yêu cầu reuse; same-key conflict
  vẫn cần người duyệt, không tự overwrite.
- **Group speaker không xác định:** fail closed cho personal Memory, chỉ dùng common scope.
- **Session còn nhớ snapshot speaker trước:** mỗi speaker change gửi replacement có UID và directive
  vô hiệu snapshot trước cho current speaker.
- **Sensitive proposal vẫn tồn tại trong DB:** chỉ nằm local, không vào prompt; pending tự xóa sau
  30 ngày và người trực có thể từ chối/xóa ngay.
- **Overlay phụ thuộc signature upstream:** build seam giữ exact-once guards và package tests; signature
  drift làm build fail rõ thay vì phát hành binary thiếu Memory V2.
- **Expiry trôi mà revision không đổi:** snapshot transaction chủ động expire due rows và bump đúng
  subject revision trước khi quyết định refresh.

