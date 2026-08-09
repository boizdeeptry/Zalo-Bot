# Thiết kế Claude CLI session theo hội thoại Zalo

**Ngày:** 2026-08-09

**Trạng thái:** Đã được người dùng xác nhận ngày 2026-08-09; định dạng prompt nội bộ được gia cố theo security review; hợp đồng khôi phục được sửa hẹp theo control flow Claude Code 2.1.222 đã ghim, không đổi phạm vi người dùng đã duyệt

**Repository:** `D:\TuvanZalo\_build\.worktrees\portal-m1`

**Nhánh:** `feature/portal-m1-foundation`

## 1. Bối cảnh

Mỗi lượt trả lời Zalo hiện chạy một tiến trình `claude -p` với `--session-id` mới. Lịch sử không nằm trong Claude session mà được dựng lại từ SQLite: tối đa 10 tin, 4 KiB lịch sử, 12 ghi chú hội thoại và 8 bài học toàn cục. Cách này giữ kích thước context gần như cố định nhưng làm Claude mất mạch hội thoại tự nhiên, lặp lại phần prompt ổn định và tạo một transcript CLI mới cho từng lượt.

Claude Code 2.1.222 trên máy hiện tại hỗ trợ cả `--session-id <uuid>` và `--resume <session-id>`. Thay đổi này dùng khả năng resume để một người hoặc một nhóm Zalo có một Claude session liên tục, đồng thời chủ động xoay session trước khi context dài gây chậm hoặc compact bất ngờ.

## 2. Mục tiêu

- Một cuộc trò chuyện riêng và một nhóm Zalo được ánh xạ độc lập từ `thread_id` sang Claude session.
- Lượt đầu tạo session bằng `--session-id`; lượt sau tiếp tục bằng `--resume`.
- Lượt resume chỉ gửi phần hội thoại mới và knowledge liên quan, không lặp toàn bộ prompt khởi tạo.
- Mỗi thread chỉ có một lượt Claude chạy tại một thời điểm.
- Session được xoay chủ động theo context, số lượt hoặc thay đổi cấu hình.
- Session mới nhận đủ bàn giao để không mất danh tính, ghi nhớ, lịch sử gần nhất và nguyên tắc an toàn.
- Daemon restart vẫn tiếp tục được session nếu transcript Claude còn tồn tại.
- Giữ nguyên kiểm tra JSON, trích dẫn, handoff, outbox, attachment và chế độ auto/review/manual hiện có.

## 3. Ngoài phạm vi

- Giữ một tiến trình Claude/PTY sống liên tục cho từng thread.
- Đồng bộ transcript Claude giữa nhiều máy hoặc nhiều tài khoản Windows.
- Hiển thị, sửa hoặc xóa Claude transcript từ Portal trong bản đầu.
- Tóm tắt bằng một lượt model riêng chỉ để xoay session.
- Chia một nhóm thành nhiều session theo từng thành viên.
- Thay đổi chính sách Provider routing đã duyệt; API Provider vẫn là lượt stateless.

## 4. Các phương án đã cân nhắc

### A. Session có thể resume theo thread, prompt delta và xoay chủ động — chọn

Giữ tính liên tục tự nhiên theo người/nhóm nhưng chặn context tăng vô hạn. Cần thêm bảng ánh xạ, khóa theo thread, parser usage và logic khôi phục khi transcript không còn.

### B. Giữ mỗi lượt là session mới

Đơn giản, tốc độ ổn định và không có tranh chấp resume, nhưng mạch hội thoại chỉ được mô phỏng bằng lịch sử rút gọn. Đây là hành vi hiện tại và không đáp ứng yêu cầu.

### C. Giữ một tiến trình Claude tương tác sống cho mỗi thread

Loại bỏ chi phí khởi động CLI nhưng tiêu tốn nhiều process/PTY, khó phục hồi sau daemon restart và không phù hợp khi có nhiều khách hoặc nhóm không hoạt động. Không chọn.

## 5. Kiến trúc

### 5.1 `ZaloCLISessionStore`

Một API store nhỏ trong overlay chịu trách nhiệm duy nhất cho trạng thái session:

- Đọc session hiện hành theo `thread_id`.
- Tạo hoặc thay session bằng compare-and-swap generation.
- Ghi model, prompt fingerprint, context estimate, số lượt và message cursor.
- Đánh dấu session cần xoay mà không xóa transcript Claude.
- Đọc các tin Zalo có ID lớn hơn cursor để dựng delta chính xác.

SQLite là nguồn sự thật cho mapping. Bộ nhớ tiến trình chỉ giữ khóa đồng thời, không giữ session ID làm nguồn chính.

### 5.2 `ZaloThreadGate`

Một keyed mutex có reference count tuần tự hóa toàn bộ `answerZalo` theo `thread_id`. Khóa bắt đầu trước khi đọc lịch sử và kết thúc sau khi answer/handoff đã ghi xong. Hai thread khác nhau vẫn chạy song song; hai tin của cùng thread chạy đúng thứ tự và lượt sau nhìn thấy output của lượt trước.

Entry mutex được xóa khỏi map khi không còn holder hoặc waiter để map không tăng vô hạn.

### 5.3 `ZaloSessionRunner`

Runner cùng package `daemon` dùng lại profile read-only, `consultArgv`, `parseStreamLine` và các giới hạn hiện có.

- Session mới: `claude -p --session-id <uuid>` với full bootstrap prompt.
- Session đang hoạt động: `claude -p --resume <uuid>` với delta prompt.
- Mỗi lời gọi vẫn là một process headless ngắn hạn; session liên tục nằm trong transcript Claude, không phải process sống.
- Runner thu usage từ event `result` khi trường này tồn tại. Parser khoan dung với phiên bản CLI không trả usage.
- Chỉ chữ ký đã kiểm `No conversation found with session ID` cho phép retry bootstrap ngay trong cùng lượt. Chữ ký chính xác `Failed to resume session <requested UUID>` trả mã sạch `resume_unusable`, không retry cùng lượt; orchestration xoay mapping để lượt kế tiếp bootstrap session mới.
- Plain stdout, stderr, prompt và nội dung khách không được ghi vào bảng session telemetry.

### 5.4 Seam vào upstream

Build overlay không sửa repository upstream trực tiếp. `Apply-AppSeams` thêm hai seam được kiểm tra đúng một lần:

1. Bao lời gọi `answerZalo` trong `triggerZaloDuty` bằng gate theo thread.
2. Thay lời gọi runner trong `answerZalo` bằng helper có đủ dữ liệu cấu trúc để chọn bootstrap hoặc delta.

Fake runner trong test upstream tiếp tục đi đường cũ. Chỉ `execZaloRunner` production được thay bằng session runner, tránh làm thay đổi hàng loạt test hiện có.

Task orchestration lấy `msgId` của Zalo event hiện tại từ `reply_quote` của event cuối trong batch và truyền nó vào prompt policy dưới tên `CurrentZaloMsgID`. Prompt policy chỉ so identity đã được truyền vào; nó không tự parse `reply_quote`.

## 6. Mô hình dữ liệu

Thêm bảng `app_zalo_cli_sessions`:

- `thread_id TEXT PRIMARY KEY`
- `claude_session_id TEXT NOT NULL`
- `generation INTEGER NOT NULL`
- `model TEXT NOT NULL`
- `prompt_fingerprint TEXT NOT NULL`
- `context_tokens INTEGER NOT NULL DEFAULT 0`
- `turn_count INTEGER NOT NULL DEFAULT 0`
- `message_cursor INTEGER NOT NULL DEFAULT 0`
- `rotate_before_next INTEGER NOT NULL DEFAULT 0`
- `last_error TEXT NOT NULL DEFAULT ''`
- `created_at TEXT NOT NULL`
- `updated_at TEXT NOT NULL`

Không lưu prompt, response, tool output, API key hoặc nội dung tin nhắn trong bảng này. `message_cursor` trỏ đến ID của `zalo_messages`; nội dung vẫn chỉ có một nguồn là bảng tin hiện tại.

Xóa một Zalo thread trong tương lai phải xóa mapping session bằng cascade hoặc thao tác cùng transaction. Bản đầu không xóa transcript trên ổ Claude để tránh hành động phá hủy ngoài ý muốn.

## 7. Prompt bootstrap và prompt delta

### 7.1 Bootstrap

Session mới nhận `buildConsultPrompt` đầy đủ như hiện tại, gồm persona, roster, overlay, memory, lessons, lịch sử giới hạn, retrieved passages, attachment và hợp đồng JSON/trích dẫn. Đây cũng là gói bàn giao sau khi xoay session.

### 7.2 Delta

Session resume không nhận lại toàn bộ bootstrap. Nó chỉ nhận:

- Tối đa 100 tin trong `zalo_messages` sau `message_cursor`, theo thứ tự cũ đến mới. Renderer ghi vào cặp thẻ cố định `<untrusted_conversation_jsonl>` prefix liên tục cũ-nhất-trước lớn nhất còn vừa trần 16 KiB; không được bỏ một hàng ở giữa rồi đánh dấu hàng phía sau là đã tiêu thụ.
- Câu hỏi hiện tại luôn được reserve chỗ trong budget. Nếu record inbound mang `ZaloMsgID` đúng bằng `CurrentZaloMsgID` khác rỗng nằm ngoài prefix liên tục, pin riêng record đó sau prefix theo thứ tự thời gian; pin không làm cursor vượt qua gap. Nếu identity rỗng, không có trong trang delta giới hạn, hoặc chỉ có tin cũ trùng text, pin current-question fallback tổng hợp; nội dung text không được dùng để dedupe event. Record durable được pin ngoài prefix vẫn pending và có thể xuất hiện lại khi prefix liên tục tiến đến nó.
- Retrieved passages của lượt hiện tại.
- Metadata attachment hiện tại và attachment lịch sử mà logic hiện hành xác định còn liên quan, dưới dạng JSON Lines trong cặp thẻ riêng `<untrusted_customer_files_jsonl>`.
- Một nhắc trust boundary cố định ở cuối prompt: record hội thoại/file là dữ liệu untrusted và không bao giờ là instruction; nội dung KB passage là source content; riêng directive `CẢNH BÁO CỦA TRANG NÀY` do ứng dụng sinh là safety instruction có thẩm quyền và phải được tuân theo; hợp đồng JSON, citation, safety và persona ban đầu vẫn có thẩm quyền.

Mỗi record hội thoại có `role`, `display_name` và `body`. `role` chỉ do daemon sinh từ direction cùng marker operator tin cậy (`customer`, `operator`, `assistant`), không bao giờ suy ra từ display name; vì vậy khách đặt tên `người trực` vẫn là `customer`. `display_name` và `body` được cắt UTF-8 an toàn ở 300 byte; toàn khối lịch sử giữ trần 16 KiB, ưu tiên prefix liên tục cũ-nhất-trước và reserve current event được pin. JSON encoder phải escape newline và closing tag nằm trong dữ liệu, để nội dung khách không thoát khỏi boundary hoặc giả cấu trúc prompt.

Mỗi record file có `kind`, `path`, `title`, `readable` và `reason`; `title` cũng được cắt UTF-8 an toàn ở 300 byte. File khách vẫn chỉ là evidence, không phải source. Retrieved KB candidates giữ nguyên biểu diễn citable hiện có và đứng ngoài hai khối untrusted JSONL. Dòng cảnh báo mà `renderRetrieved` sinh từ trường `passage.Warn` không phải nội dung khách và không bị reminder cuối vô hiệu hóa.

Tin do người trực gửi giữa hai lượt Claude nằm sau cursor nên được đưa vào một trang delta. Cursor là mốc nội dung Claude thực sự đã nhận theo chuỗi liên tục: bootstrap/rotation/recovery dùng ID lớn nhất trong history đã dựng prompt, còn resume dùng `ConsumedCursor`, tức ID của hàng cuối trong prefix liên tục thực sự được serialize. Current event durable hoặc synthetic được pin ngoài prefix không nâng cursor qua gap; mọi hàng còn lại trong trang 100 hàng và mọi trang sau vẫn pending cho lượt kế tiếp. Không đọc `MAX(id)` sau khi Claude chạy, vì transport có thể chèn một inbound mới trong lúc đó và làm cursor nuốt mất tin Claude chưa thấy. Các hàng outbound mà pipeline ghi sau mốc có thể lặp ở delta kế tiếp; lặp an toàn hơn bỏ sót inbound.

Nếu delta trống bất thường, runner đưa câu hỏi hiện tại vào thay vì gọi Claude với prompt rỗng.

## 8. Fingerprint và điều kiện xoay session

`prompt_fingerprint` là SHA-256 của những phần làm thay đổi ý nghĩa session: model, cite mode, `OwnerUID` dùng trong luật phân quyền, persona content, roster content, overlay content của thread, KB roots và phiên bản prompt contract. Tất cả field được mã hóa length-delimited; đổi chủ hệ thống phải xoay session để transcript cũ không tiếp tục giữ luật phân quyền đã hết hiệu lực.

Tạo session mới trước lượt kế tiếp khi một trong các điều kiện đúng:

- Context quan sát/ước lượng đạt 65% cửa sổ mặc định 200.000 token, tức 130.000 token.
- Đã hoàn tất 48 lượt trong session mà usage không đủ tin cậy.
- Model hoặc fingerprint thay đổi.
- Mapping bị đánh dấu `rotate_before_next`.
- Mapping được đánh dấu xoay sau mã `resume_unusable`, nghĩa là Claude không resume được đúng UUID đã yêu cầu nhưng không có discriminator an toàn để retry trong cùng lượt.

Context token ưu tiên đọc từ usage của event `result`: tổng input, cache creation, cache read và output mà CLI cung cấp. Nếu usage thiếu, dùng ước lượng bảo thủ từ tổng byte prompt và stream output chia 3, cộng dồn theo session. Giá trị chỉ dùng để xoay sớm, không dùng để tính tiền.

Không chờ tới 100% vì mục tiêu là tránh lượt compact hoặc lỗi context trên tin của khách. Các hằng 65%, 200.000 và 48 là backend constants trong MVP, chưa cần UI.

## 9. Luồng một lượt

1. Transport ghi tin Zalo vào SQLite như hiện tại.
2. Duty loop nhận keyed lock của `thread_id`.
3. Đọc mapping session và tính fingerprint hiện tại.
4. Nếu không có mapping hoặc cần xoay, tạo UUID và bootstrap prompt.
5. Nếu mapping hợp lệ, đọc tối đa 100 message sau cursor, dựng prefix delta liên tục vừa budget và pin current event nếu nó nằm ngoài prefix.
6. Chạy Claude bằng `--session-id` hoặc `--resume`.
7. Parse answer và tiếp tục toàn bộ validation, citation, memory, handoff và outbox hiện tại.
8. Khi `answerZalo` kết thúc thành công về mặt store, nâng message cursor đến high-water liên tục đã cấp cho Claude trước khi chạy: bootstrap dùng history maximum, resume dùng `ConsumedCursor`; current pin ngoài prefix không tham gia high-water. Không dùng ID mới nhất sau pipeline.
9. Nếu đã chạm ngưỡng, đánh dấu xoay trước lượt sau; không làm khách đợi thêm một lượt tóm tắt.
10. Nhả keyed lock.

Mỗi group có một `thread_id`, nên cả nhóm dùng chung một session. Mỗi chat riêng có `thread_id` riêng, nên không có context chéo giữa hai người.

## 10. Khôi phục và lỗi

- Resume trả chữ ký đã kiểm `No conversation found with session ID`: đánh dấu mapping cũ, tạo UUID mới và thử lại đúng một lần ngay trong lượt bằng bootstrap prompt.
- Resume trả đúng `Failed to resume session <requested UUID>`: trả mã sạch `resume_unusable`, không thử lại trong lượt hiện tại, giữ cursor cũ và để Task 5 đánh dấu mapping xoay; lượt kế tiếp tạo UUID mới cùng bootstrap prompt. Claude Code 2.1.222 không phát discriminator ổn định riêng cho transcript JSON hỏng, nên không ghép fragment stdout/stderr để tạo bất kỳ chữ ký recovery nào.
- Timeout, cancellation, lỗi mạng hoặc lỗi Claude khác: không tự chạy lại vì có thể tốn tiền hai lần; giữ cursor cũ và báo theo đường handoff hiện tại.
- Daemon chết giữa lượt: cursor chưa nâng; lần sau delta có thể lặp lại tin cuối nhưng không mất tin. Idempotency outbox hiện tại tiếp tục ngăn gửi trùng theo cơ chế sẵn có.
- SQLite ghi trạng thái session thất bại sau khi Claude trả lời: answer vẫn qua cửa hiện tại; log lỗi và xoay session ở lượt sau thay vì làm mất câu trả lời hợp lệ.
- Không đọc được usage: tiếp tục dựa vào turn count và byte estimate.
- Hai tin cùng thread: tin thứ hai chờ gate; context deadline của lượt chỉ bắt đầu sau khi lấy gate để thời gian chờ hàng đợi không ăn vào ngân sách Claude.

`last_error` chỉ lưu mã lỗi đã làm sạch như `resume_not_found`, `resume_unusable` hoặc `usage_unavailable`, không lưu stderr thô.

## 11. Tương thích Provider routing

API Provider vẫn nhận prompt stateless cùng retrieved snippets như spec Provider đã duyệt. Session mapping chỉ áp dụng khi route chọn Claude Code.

Nếu nhiều lượt được API Provider trả lời trước khi Claude Code được dùng lại, `message_cursor` của Claude chưa nâng. Lần Claude tiếp theo nhận prefix liên tục cũ-nhất-trước của các tin mới cùng current event được pin; phần chưa vừa budget tiếp tục pending qua các lượt resume sau, nên không có hàng nào bị cursor bỏ qua.

Task runtime seam trong plan Provider phải dùng `ZaloSessionRunner` thay vì tạo lại `execZaloRunner` mỗi lượt. Session feature được triển khai trước Provider feature để chỉ có một owner cho seam `duty.go`.

## 12. Hiệu năng

- Prompt delta giảm phần lặp ổn định sau lượt đầu.
- Go retrieval tiếp tục chạy trước model; snippets đủ thì prompt yêu cầu trả lời trực tiếp, giảm tool round-trip nhưng không cấm Read/Grep/Glob/WebFetch khi cần.
- Không thêm model call để tóm tắt khi xoay session; dùng memory và history đã có.
- Thread khác chạy song song; chỉ cùng thread bị tuần tự hóa.
- SQLite vẫn dùng một connection như hiện tại; truy vấn mapping/cursor ngắn và không giữ transaction trong lúc Claude chạy.
- Việc spawn một `claude -p` mỗi lượt vẫn còn. Giữ process sống được hoãn vì chi phí RAM và phục hồi phức tạp hơn lợi ích MVP.

## 13. Kiểm thử

### Store

- Migration idempotent.
- Mapping tách biệt giữa user và group thread.
- Compare-and-swap generation, cursor tăng đơn điệu và snapshot copy.
- Delta query trả đúng tin sau cursor theo thứ tự.
- Bảng session không có cột nội dung.

### Runner

- Lượt đầu dùng `--session-id`; lượt hai cùng thread dùng `--resume` cùng UUID.
- Thread khác nhận UUID khác.
- Resume dùng delta, không chứa lại bootstrap marker/persona đầy đủ.
- Operator message sau cursor xuất hiện trong prefix trước khi cursor được phép vượt qua nó; backlog vượt 16 KiB tiếp tục pending qua các lượt sau.
- Trang 100 record gần giới hạn body vẫn giữ correction đầu trang, pin current event, trả `ConsumedCursor` dưới page tail và bắt đầu lượt sau từ hàng pending đầu tiên.
- Usage parser có dữ liệu, thiếu dữ liệu và định dạng lạ.
- Xoay ở 130.000 token, 48 lượt, đổi model và đổi fingerprint.
- Resume-not-found đã kiểm retry đúng một lần bằng bootstrap; exact requested-UUID `resume_unusable` không retry cùng lượt, wrong UUID/`--print mode` không được nhận nhầm; lỗi khác không retry.
- Prompt/stdout/stderr không lọt vào bảng session.

### Đồng thời

- Hai lượt cùng thread chạy tuần tự và lượt sau thấy output lượt trước.
- Hai thread khác chạy đồng thời.
- Cancellation trong khi chạy nhả khóa.
- Keyed mutex entry được thu hồi sau khi hết waiter.

### Regression và đóng gói

- Toàn bộ `answerZalo`, citation, attachment, memory, handoff, outbox và UI hiện tại vẫn qua test.
- Build seam được áp dụng đúng một lần và không sửa upstream source.
- Full Go tests, Portal tests, Zalo build/tests/typecheck và package credential gate đều qua.

## 14. Tiêu chí nghiệm thu

- Hai tin liên tiếp của cùng người/nhóm dùng cùng Claude session ID; tin thứ hai chạy bằng `--resume`.
- Hai thread khác nhau không dùng chung session ID hoặc message delta.
- Tin tiếp theo không lặp full bootstrap prompt khi session còn hợp lệ.
- Tin do người trực gửi giữa các lượt được Claude thấy trước khi cursor vượt qua nó; nếu backlog vượt budget, các resume liên tiếp xử lý prefix pending theo thứ tự.
- Resume cursor chỉ đến `ConsumedCursor` của prefix liên tục đã serialize; current pin hoặc page tail phía sau gap không thể làm mất phần backlog còn lại.
- Session tự xoay trước khi vượt ngưỡng và session mới vẫn biết memory cùng lịch sử gần nhất.
- Daemon restart tiếp tục session nếu transcript còn; transcript mất với chữ ký missing đã kiểm thì tự phục hồi ngay một lần, còn `resume_unusable` chỉ bootstrap session mới ở lượt kế tiếp.
- Không xuất hiện trả lời trùng do hai lượt cùng thread chạy song song.
- Response path không thêm một model call chỉ để quản lý session.
- Provider routing tương lai không vô hiệu hóa session Claude theo thread.

## 15. Rủi ro và biện pháp

- Claude CLI đổi schema stream: parser usage phải optional; turn/byte threshold luôn là fallback.
- Transcript tích lũy tool output nhanh hơn estimate: ngưỡng 65% và hard cap 48 lượt tạo khoảng an toàn.
- Prompt delta bỏ sót tin người trực: dùng `ConsumedCursor` từ prefix liên tục đã serialize, không suy từ timestamp/body và không lấy page tail khi budget đã cắt payload.
- Resume song song làm hỏng transcript: keyed gate bao toàn bộ answer pipeline theo thread.
- Persona hoặc chính sách đổi nhưng session giữ luật cũ: fingerprint buộc xoay trước lượt kế tiếp.
- Retry có thể nhân đôi chi phí: chỉ retry chữ ký resume-not-found đã kiểm, đúng một lần, với runner chỉ-đọc; generic/corrupt không có discriminator an toàn thì tuyệt đối không retry cùng lượt.
- Session mapping còn nhưng transcript bị dọn ngoài ứng dụng: chữ ký missing phục hồi ngay; `resume_unusable` đánh dấu xoay và phục hồi bằng bootstrap cùng generation mới ở lượt kế tiếp.
