# Memory Center và đồng bộ Memory với Zalo CLI session

Ngày: 2026-08-09
Trạng thái: chờ người dùng duyệt spec

## 1. Bối cảnh

Portal hiện có hai cơ chế nhớ ở backend nhưng chưa có một trang quản trị thực sự:

- `zalo_memory`: ghi chú do bot chắt lọc cho riêng một `thread_id`.
- `zalo_lessons`: bài học do người trực sửa câu bot, áp dụng cho mọi hội thoại.

Trang Zalo chỉ cho xem tối đa 100 ghi chú của hội thoại đang mở. Mục `Memory` trong Portal là
placeholder; không thể tìm kiếm, thêm, sửa, xoá hoặc ghim. Bản session mới giữ một Claude CLI
session cho mỗi người/nhóm, nhưng memory và lessons chỉ nằm trong bootstrap prompt. Một thay đổi
trong SQLite chưa có revision/delta để cập nhật session đang `--resume`.

Bản package đang chạy tại `D:\TuvanZalo` cũng cũ hơn nhánh hiện tại: database chưa có bảng
`app_zalo_cli_sessions`. Sau khi hoàn thành tính năng phải tạo và kiểm thử một package mới trước
khi thay binary/assets của bản chạy, đồng thời giữ nguyên `brain\` và `data\`.

## 2. Mục tiêu

1. Thay placeholder `Memory` bằng trang quản trị có giao diện legacy giống Portal hiện tại.
2. Cho phép quản trị memory theo đúng phạm vi:
   - ghi chú riêng cho từng người/nhóm;
   - bài học chung áp dụng cho mọi hội thoại.
3. Cho phép tìm, thêm, sửa, xoá và ghim; mọi thay đổi có hiệu lực ở lượt trả lời kế tiếp.
4. Đồng bộ thay đổi vào Claude CLI session đang `--resume` mà không buộc xoay session.
5. Giữ context có giới hạn, không biến memory thành knowledge base và không cho trích dẫn memory.
6. Migrate an toàn database cũ và build lại bản Portal chứa cả session mới lẫn Memory Center.

## 3. Không nằm trong phạm vi

- Không đưa memory vào `brain/wiki`, Obsidian hoặc quy trình ingest.
- Không dùng model call để tự gộp, tóm tắt hay dọn memory.
- Không triển khai vector database, embedding hoặc semantic search cho memory.
- Không thay Provider Routing, model routing hoặc cơ chế trả lời Knowledge.
- Không thêm multi-workspace, phân quyền nhiều người trực hoặc audit log hoàn chỉnh.
- Không tự sửa persona từ lessons; người dùng vẫn phải chủ động biên tập persona.
- Không hỗ trợ khôi phục sau khi người dùng xác nhận xoá ở MVP.

## 4. Các phương án đã cân nhắc

### A. Memory Center + revision snapshot vào session — chọn

Giữ hai bảng memory hiện có, bổ sung metadata quản trị và revision. Khi revision thay đổi, lượt
`--resume` tiếp theo nhận một snapshot memory/lessons mới có tuyên bố thay thế snapshot cũ.

Ưu điểm: cập nhật ngay lượt sau, không mất mạch hội thoại, không xoay session chỉ vì sửa một ghi
chú, và giữ được ranh giới memory/knowledge. Nhược điểm: cần migration, CRUD API và thêm trạng thái
revision vào session.

### B. Chỉ làm trang CRUD và xoay session khi memory đổi

Đơn giản hơn ở prompt, nhưng mọi thao tác sửa nhỏ đều bỏ session đang hoạt động, phải bootstrap lại
persona, lịch sử, KB candidates và toàn bộ contract. Độ trễ và token tăng không cần thiết.

### C. Lưu memory thành Markdown trong brain

Dễ xem bằng Obsidian nhưng phá ranh giới an toàn: chữ do bot tự viết có thể bị xem như nguồn thật
và trích dẫn lại. Phương án này bị loại.

## 5. Kiến trúc được chọn

```text
Zalo message
    │
    ├─ Claude trả JSON có note
    │      └─ INSERT zalo_memory ── tăng revision của thread
    │
    ├─ Người trực sửa câu bot
    │      └─ INSERT zalo_lessons ─ tăng revision lessons toàn cục
    │
    └─ Lượt trả lời tiếp theo
           ├─ session mới/rotation: full bootstrap + snapshot hiện tại
           └─ session resume:
                 revision không đổi → chỉ gửi message delta
                 revision thay đổi → message delta + snapshot thay thế

Memory Center
    ├─ GET: tổng quan, thread memories, lessons
    └─ POST/PUT/DELETE: transaction + tăng đúng revision
```

Memory Center không gọi mô hình. Đây là CRUD local trên SQLite nên thao tác phải phản hồi gần như
tức thời.

## 6. Mô hình dữ liệu và migration

### 6.1. `zalo_memory`

Giữ các cột hiện có và bổ sung idempotently:

- `pinned INTEGER NOT NULL DEFAULT 0`
- `source TEXT NOT NULL DEFAULT 'agent'` — `agent`, `operator` hoặc `legacy`
- `source_message_id INTEGER NOT NULL DEFAULT 0`
- `updated_at TEXT NOT NULL DEFAULT ''`

Ghi chú tự sinh tiếp tục đi qua `AddZaloMemory`; các cột mới dùng default nên không phá upstream.
Trigger insert gắn `source_message_id` với tin inbound mới nhất của đúng thread khi nguồn là
`agent`, và tăng revision của thread. Dòng cũ không suy đoán ngược nguồn tin; UI ghi `legacy` khi
không có provenance đáng tin.

### 6.2. `zalo_lessons`

Bổ sung:

- `pinned INTEGER NOT NULL DEFAULT 0`
- `updated_at TEXT NOT NULL DEFAULT ''`

Trigger insert tăng revision lessons toàn cục. `thread_id`, `bot_text`, `better`, `note` hiện có
vẫn là provenance chính của bài học.

### 6.3. Revision

Tạo bảng nội bộ:

```sql
app_memory_revisions(
  scope TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  PRIMARY KEY(scope, scope_id)
)
```

- Ghi chú dùng `scope='thread'`, `scope_id=<thread_id>`.
- Lessons dùng `scope='lessons'`, `scope_id=''`.
- Insert tự động tăng revision bằng trigger.
- Update/delete từ API tăng revision trong cùng transaction với mutation.
- Migration seed revision `1` cho thread đã có memory và cho lessons nếu đã có dữ liệu.

`app_zalo_cli_sessions` bổ sung hai cột content-free:

- `memory_revision INTEGER NOT NULL DEFAULT 0`
- `lessons_revision INTEGER NOT NULL DEFAULT 0`

Hai cột chỉ là cursor số, không chứa prompt, tin nhắn hay nội dung memory. `appSchemaVersion` tăng
từ `2` lên `3`; migration vẫn bảo toàn metadata schema cao hơn phiên bản hiện tại và chạy lặp lại
an toàn.

## 7. Quy tắc chọn memory cho prompt

- Ghi chú: tối đa 12 dòng cho đúng thread.
- Lessons: tối đa 8 dòng toàn cục.
- Dòng ghim được chọn trước; phần chỗ còn lại lấy các dòng mới nhất.
- API từ chối ghim dòng thứ 13 của một thread hoặc lesson thứ 9 bằng `409`, để không phá trần
  context đã cam kết.
- Trước khi render prompt, tập đã chọn được sắp theo thời gian cũ đến mới.
- Memory và lessons vẫn mang nhãn rõ ràng: context không đáng tin tuyệt đối, không phải nguồn và
  không bao giờ được đưa vào `sources`.

Không tự deduplicate ở MVP. Xoá/sửa là quyết định của người trực; một model không được tự quyết
ký ức nào của chính nó là đúng.

## 8. Đồng bộ với session `--resume`

### 8.1. Bootstrap hoặc rotation

Full bootstrap dùng snapshot hiện tại như trước. Sau khi lượt hoàn tất thành công, session lưu
`memory_revision` và `lessons_revision` đã quan sát trước khi gọi Claude.

### 8.2. Resume không có thay đổi

Chỉ gửi message delta, KB candidates và attachments hiện tại. Không lặp memory/lessons.

### 8.3. Resume có thay đổi

Delta prompt thêm một khối snapshot authoritative:

- ghi rõ snapshot này thay thế toàn bộ memory/lessons cũ trong transcript;
- chứa cả trạng thái rỗng để thao tác xoá có thể vô hiệu hoá ký ức cũ;
- tiếp tục nhắc đây không phải nguồn trích dẫn.

Không đưa memory/lessons vào prompt fingerprint, vì thay đổi memory không cần xoay session. Nếu
memory được tạo bởi chính câu trả lời vừa xong, trigger tăng revision sau khi model chạy; cursor
session vẫn giữ revision quan sát trước lượt đó, nên lượt kế tiếp chắc chắn nhận snapshot mới.
Nếu người dùng sửa memory trong lúc model đang chạy, cùng nguyên tắc này bảo đảm thay đổi không bị
đánh dấu nhầm là đã đồng bộ.

Lỗi đọc revision hoặc snapshot chỉ được log và luồng trả lời tiếp tục với session hiện có. Memory
làm câu trả lời cá nhân hơn nhưng không được phép làm bot ngừng trả lời.

## 9. API Portal

Các route đều dùng auth/cookie hiện tại và parameterized SQL:

- `GET /memory?q=&pinned=` — metrics và tối đa 200 thread gần nhất, kể cả thread chưa có memory,
  để người trực có thể chủ động thêm ghi chú đầu tiên.
- `GET /memory/threads/{tid}` — tối đa 100 ghi chú, kèm tên thread, revision và preview tin nguồn.
- `POST /memory/threads/{tid}` — thêm ghi chú do người trực viết.
- `PUT /memory/threads/{tid}/{id}` — sửa text và trạng thái pinned.
- `DELETE /memory/threads/{tid}/{id}` — xoá sau khi UI xác nhận.
- `GET /memory/lessons?q=&pinned=` — tối đa 100 bài học.
- `POST /memory/lessons` — thêm bài học chung.
- `PUT /memory/lessons/{id}` — sửa nội dung và pinned.
- `DELETE /memory/lessons/{id}` — xoá sau khi UI xác nhận.

Giới hạn đầu vào dùng lại quy tắc hiện tại: memory tối đa 240 rune; mỗi trường lesson tối đa 200
rune; trim khoảng trắng; ghi chú rỗng bị từ chối. ID không thuộc thread trả `404`. Pin vượt trần
trả `409`. Error trả về UI không chứa SQL, đường dẫn nhạy cảm hoặc nội dung prompt.

Nút thêm ghi chú áp dụng cho thread đang chọn; khi chưa chọn thread thì bị disable. Thread có
memory được xếp trước, sau đó tới các thread gần nhất chưa có memory. Search chạy server-side trên
tên thread và nội dung memory để danh sách 200 dòng không biến thành giới hạn tìm kiếm giả.

## 10. Giao diện

Thay route placeholder bằng `pages/memory.js`, giữ nguyên `Segoe UI`, palette tối, rail, nút, viền,
khoảng cách và breakpoint legacy. Không đưa framework UI mới vào project.

### 10.1. Trang chính

- Header `Memory` và mô tả ngắn.
- Ba metrics: ghi chú đang dùng, người/nhóm có memory, bài học chung.
- Hai tab: `Người & nhóm` và `Bài học chung`.

### 10.2. Người & nhóm

- Search theo tên thread hoặc nội dung ghi chú.
- Bộ lọc chỉ ghi chú đã ghim.
- Pane trái là danh sách người/nhóm, số ghi chú và preview.
- Pane phải là các memory của thread đang chọn, revision đã đồng bộ, nguồn/ngày và thao tác
  `Sửa`, `Xoá`, ghim/bỏ ghim.
- `Mở hội thoại` mở trang `/zalo` với thread được truyền qua query; nếu trang Zalo không tìm thấy
  thread thì vẫn mở Conversations bình thường và không báo lỗi giả.
- Add/edit dùng sheet/modal theo pattern AI Agents; delete dùng confirm rõ tên đối tượng.

### 10.3. Bài học chung

- Danh sách câu bot đã nói, câu sửa tốt hơn, lý do và hội thoại nguồn.
- Search, thêm, sửa, xoá, ghim/bỏ ghim.
- Nhãn luôn nói rõ lessons áp dụng cho mọi hội thoại.

### 10.4. Trạng thái và accessibility

- Empty state thật cho database hiện đang có 0 memory/0 lessons.
- Loading, mutation pending, lỗi và thành công đi qua vùng `aria-live`.
- Tab dùng semantics tab; sheet giữ focus trap/Escape/focus restore như AI Agents.
- Desktop dùng split-pane; mobile xếp danh sách và detail theo một cột, không cuộn ngang.

## 11. Đơn vị triển khai

- `internal/store/app_memory.go`: model quản trị, query, validation, CRUD, revision và prompt
  selection.
- `internal/store/app_schema.go`: schema v3 và migration idempotent.
- `internal/daemon/app_memory.go`: HTTP handlers và JSON contracts.
- `internal/daemon/app_routes.go`: đăng ký/auth routes.
- `internal/daemon/app_zalo_session_prompt.go`: memory refresh snapshot trong delta.
- `internal/daemon/app_zalo_session_hook.go`: đọc/persist revision cùng session completion.
- `internal/store/app_zalo_cli_session.go`: hai revision cursors và CAS completion.
- `internal/webui/static/pages/memory.js`: service + page lifecycle.
- `internal/webui/static/core/router.js` và `app-main.js`: route thật thay placeholder.
- `internal/webui/static/portal.css`: CSS chỉ scope dưới `.memory-page`/overlay của trang.
- Seam nhỏ cho `/zalo?thread=...` nếu upstream chưa hỗ trợ mở thread từ query.

Mỗi file giữ một trách nhiệm; không trộn CRUD Memory vào `app_knowledge.go` hay logic Provider.

## 12. Xử lý lỗi và đồng thời

- Mọi mutation DB dùng transaction; mutation và revision tăng cùng thành công hoặc cùng rollback.
- Session completion tiếp tục CAS theo generation; revision cursor không được ghi vào generation
  đã bị thay thế.
- Hai lượt cùng thread vẫn đi qua thread gate hiện có.
- Memory edit trong lúc Claude chạy không mất: completion chỉ lưu revision đã đọc trước model.
- Xoá yêu cầu confirm ở UI và `DELETE` đúng cả `thread_id` lẫn `id` để không xoá nhầm thread.
- API read lỗi hiển thị thông báo nhưng không xoá dữ liệu đang hiện trên trang.
- Không ghi nội dung memory/lessons vào log lỗi, session routing table hoặc telemetry.

## 13. Kiểm thử

### Store và migration

- Migrate fresh, từ schema v2, chạy hai lần và metadata phiên bản tương lai.
- Cột mới, trigger, seed revision và tính content-free của session table.
- CRUD memory/lessons, validation, ownership theo thread và rollback.
- Pin priority, giới hạn 12/8 và thứ tự cũ đến mới.
- Insert tự động tăng đúng thread revision; lesson tăng global revision.

### Session

- Bootstrap có snapshot và persist hai revision sau lượt thành công.
- Resume không đổi revision không lặp snapshot.
- Add/edit/delete memory tạo snapshot thay thế ở lượt kế tiếp.
- Global lesson đổi được mọi thread nhận ở lượt kế tiếp; memory thread A không làm thread B refresh.
- Snapshot rỗng vô hiệu hoá memory cũ.
- Mutation trong lúc model chạy vẫn chờ đồng bộ ở lượt sau.
- Recovery/rotation, message cursor, context token và generation race hiện có vẫn pass.

### HTTP và UI

- Route/method/auth, giới hạn input, 404/409 và response không rò rỉ chi tiết.
- Service gọi đúng endpoint; metrics, tabs, search, empty/loading/error states.
- Add/edit/delete/pin, confirm destructive action, sheet focus lifecycle và dispose/abort.
- Legacy navigation, Knowledge, Agents, Models, `/zalo` và mobile rail không regression.

### Package

- Chạy Go tests trên staged source, Node UI tests, PowerShell package tests và source cleanliness.
- Build package mới vào thư mục artifact tách biệt.
- Smoke-test bằng bản sao database cũ để chứng minh migration v3 và giữ nguyên dữ liệu.
- Chỉ sau khi pass mới thay binary/static assets của bản test; không xoá hoặc ghi đè `brain\`,
  `data\`, credentials hay repo `_build\`.

## 14. Tiêu chí hoàn thành

- Mục Memory không còn nhãn `chưa có` và khớp mockup đã duyệt.
- Người dùng quản trị được memory per person/group và global lessons từ Portal.
- Mutation có hiệu lực ở lượt Zalo kế tiếp dù session đang resume.
- Memory không xuất hiện như KB source và không làm session rotate không cần thiết.
- Database/package cũ migrate không mất dữ liệu.
- Tất cả test liên quan và package gates pass; package mới mở được để người dùng test.

## 15. Rủi ro còn lại

- Model vẫn có thể chắt lọc một ghi chú sai; Memory Center làm nó nhìn thấy và sửa được, không thể
  bảo đảm model không bao giờ ghi sai.
- Snapshot thay thế là chỉ dẫn trong transcript, không xoá vật lý text cũ khỏi Claude session.
  Nếu quan sát thực tế cho thấy model vẫn bám memory đã xoá, bước an toàn tiếp theo là đánh dấu
  session rotate sau delete; không bật trước khi có bằng chứng vì làm tăng độ trễ.
- Ghi chú legacy không có nguồn tin đáng tin nên UI phải nói rõ, không suy đoán provenance.

Không còn câu hỏi mở cho MVP.
