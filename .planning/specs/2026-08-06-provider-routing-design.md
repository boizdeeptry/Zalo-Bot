# Thiết kế quản lý Provider và fallback nhẹ cho Portal Zalo

**Ngày:** 2026-08-06

**Trạng thái:** Đã được người dùng xác nhận

**Repository:** repo đóng gói Zalo-Bot (xem `README.md` để chuẩn bị máy)

**Nhánh nền:** `feature/portal-m1-foundation`, đã merge vào `main`

## 1. Bối cảnh

Portal hiện cấu hình một model cho Claude Code và cần mở rộng để người vận hành có thể quản lý nhiều nhà cung cấp LLM. Thiết kế tham khảo mô hình Provider, model và fallback của [9Router](https://github.com/decolua/9router), nhưng không nhúng hoặc chạy nguyên 9Router. 9Router là một proxy độc lập có phạm vi lớn hơn nhiều, gồm hơn 40 Provider, chuyển đổi định dạng, quota, multi-account, analytics, cloud sync và token saver; Portal Zalo chỉ cần một router nhẹ phục vụ bot đang có.

## 2. Mục tiêu

- Quản lý Claude Code, OpenAI, Anthropic, Gemini và OpenRouter từ Portal.
- Bảo vệ API key bằng Windows DPAPI và không bao giờ trả secret về trình duyệt.
- Cho phép kiểm tra kết nối, tự tải model khi Provider hỗ trợ và nhập model thủ công khi cần.
- Cấu hình một chuỗi fallback toàn cục gồm các cặp `Provider + model`.
- Áp dụng cấu hình từ tin nhắn kế tiếp mà không restart daemon.
- Tự fallback có kiểm soát khi Provider gặp lỗi tạm thời.
- Giữ nguyên UI legacy, Knowledge, AI Agents, `/zalo` và mobile rail hiện tại.

## 3. Ngoài phạm vi MVP

- Nhiều tài khoản cho cùng một Provider, round-robin hoặc weighted routing.
- Routing theo cuộc hội thoại, nhóm Zalo hoặc từng agent.
- Quota/reset countdown, token analytics, chi phí và báo cáo theo tháng.
- Custom endpoint, self-hosted Provider hoặc proxy `/v1` cho ứng dụng bên ngoài.
- Cloud sync, OAuth Provider và các token saver như RTK/Headroom.
- Thay đổi prompt, cơ chế nhận/gửi Zalo hoặc giao diện `/zalo`.

## 4. Quyết định chính

- Router nằm trong Go daemon, không nằm trong Node transport.
- Một cấu hình fallback dùng chung cho toàn bộ bot.
- Claude Code là Provider hệ thống, không có API key, không thể xóa và luôn đứng cuối chuỗi.
- Mỗi Provider chỉ được thử một lần trong một lượt trả lời.
- Fallback chỉ xảy ra với timeout, lỗi mạng, HTTP `429` hoặc `5xx`.
- Lỗi credential, model không tồn tại, request không hợp lệ hoặc nội dung bị từ chối sẽ dừng chuỗi và báo lỗi cấu hình.
- Thay đổi cấu hình có hiệu lực từ tin nhắn kế tiếp; lượt đang chạy tiếp tục dùng snapshot cũ.
- MVP chỉ dùng endpoint chính thức của bốn API Provider, không cho nhập URL tùy ý.
- Telemetry không lưu prompt, tin nhắn khách hoặc nội dung câu trả lời.
- API Provider chỉ nhận prompt cùng các đoạn knowledge mà Go đã truy xuất và chèn sẵn. Tin nhắn có file hoặc ảnh đi thẳng đến Claude Code, không upload dữ liệu khách sang API Provider trong MVP.

## 5. Kiến trúc

### 5.1 ProviderStore

Trách nhiệm duy nhất là lưu và đọc cấu hình Provider, model và route từ SQLite hiện có.

Giao diện công khai dự kiến:

- Liệt kê Provider và trạng thái credential đã cấu hình.
- Tạo, sửa, vô hiệu hóa và xóa Provider.
- Thay hoặc xóa credential theo thao tác riêng.
- Thay danh sách model discovered/manual.
- Đọc và cập nhật route bằng optimistic revision.
- Tạo snapshot bất biến cho một lượt router.

Store không trả plaintext credential cho HTTP API. Việc ghi route và tăng revision diễn ra trong một transaction.

### 5.2 ProviderAdapter

Mỗi adapter chỉ chịu trách nhiệm cho giao thức của một Provider:

- `ClaudeCodeAdapter`
- `OpenAIAdapter`
- `AnthropicAdapter`
- `GeminiAdapter`
- `OpenRouterAdapter`

Interface chung nhận request nội bộ cùng context cancellation và trả về response chuẩn hóa hoặc lỗi đã phân loại. Adapter cũng có hai năng lực quản trị: kiểm tra kết nối bằng dữ liệu vô hại và khám phá model nếu API hỗ trợ.

Không adapter nào tự fallback, ghi database hoặc quyết định UI state.

### 5.3 LLMRouter

Router nhận một request trả lời đã được dựng prompt như hiện tại, lấy snapshot cấu hình và thử tuần tự từng route entry đang bật. Router quyết định có fallback hay dừng dựa trên loại lỗi chuẩn hóa, áp timeout tổng, truyền cancellation xuống adapter và phát telemetry.

Transport Zalo chỉ phụ thuộc vào interface trả lời chung, không biết Provider hoặc model cụ thể.

### 5.4 ProviderTelemetry

Telemetry lưu metadata vận hành tối thiểu:

- Provider và model đã được gọi.
- Thời điểm bắt đầu, thời lượng và kết quả.
- Loại lỗi đã chuẩn hóa.
- Có fallback hay không và Provider kế tiếp.
- Provider/model phục vụ thành công gần nhất.

Không lưu request body, prompt, tin nhắn Zalo, response body, API key hoặc authorization header.

## 6. Mô hình dữ liệu

### 6.1 `llm_providers`

- ID ổn định.
- Tên hiển thị.
- Loại: `claude_code`, `openai`, `anthropic`, `gemini`, `openrouter`.
- Trạng thái bật/tắt.
- Credential blob đã mã hóa; rỗng với Claude Code.
- Trạng thái kiểm tra gần nhất, lỗi đã làm sạch và timestamps.
- Cờ Provider hệ thống để bảo vệ Claude Code khỏi sửa/xóa không hợp lệ.

### 6.2 `llm_models`

- Provider ID.
- Model ID chính xác gửi lên Provider.
- Tên hiển thị.
- Nguồn `discovered` hoặc `manual`.
- Trạng thái khả dụng gần nhất.

Khóa logic là `provider_id + model_id`.

### 6.3 `llm_route_entries`

- Thứ tự ưu tiên.
- Provider ID và model ID.
- Trạng thái bật/tắt.
- Revision của cấu hình route.

Route hợp lệ phải có ít nhất một mục bật và kết thúc bằng Claude Code. Một Provider không thể bị xóa khi còn được route tham chiếu.

## 7. Credential và DPAPI

- API key được mã hóa trên Windows bằng DPAPI theo tài khoản người dùng hiện tại trước khi ghi SQLite.
- API chỉ trả `credential_configured: true|false`, không có endpoint đọc lại key.
- Khi sửa Provider và trường key để trống, key hiện có được giữ nguyên.
- Thay key và xóa key là hai mutation tường minh.
- Nếu database được chuyển sang user hoặc máy khác và DPAPI không giải mã được, metadata Provider vẫn tồn tại nhưng trạng thái chuyển thành `cần nhập lại API key`.
- Mọi log và error serialization phải loại API key, bearer token, authorization header và response body có thể chứa secret.

## 8. Luồng định tuyến

1. Bot nhận tin Zalo và dựng request nội bộ theo hành vi hiện tại.
2. Router lấy một snapshot route bất biến.
3. Nếu lượt có file hoặc ảnh, router chọn thẳng Claude Code với model được ghim trong route hệ thống; không gọi API Provider.
4. Với lượt chỉ có text, router thử từng mục đang bật theo thứ tự:
   - Gọi adapter đúng Provider/model.
   - Thành công: trả kết quả ngay và ghi telemetry.
   - Timeout, lỗi mạng, `429`, `5xx`: ghi fallback event và thử mục tiếp theo.
   - Credential/model/request/content error: dừng route và trả lỗi cấu hình đã làm sạch.
5. Nếu các API Provider đều lỗi tạm thời, Claude Code được thử cuối cùng.
6. Nếu toàn bộ chuỗi thất bại, bot không gửi câu trả lời giả và Portal ghi nhận trạng thái không khả dụng.

Timeout mặc định:

- 25 giây cho mỗi API Provider.
- 60 giây cho toàn chuỗi API; Claude Code tiếp tục dùng giới hạn hiện hành.
- Context bị hủy khi lượt bị hủy hoặc daemon dừng.

Các giá trị timeout là cấu hình backend có default an toàn, chưa cần UI trong MVP.

## 9. Áp dụng cấu hình đồng thời

Portal gửi route mới kèm revision đã đọc. Daemon xác thực tất cả Provider/model còn tồn tại và đang bật, xác thực Claude Code ở cuối, sau đó ghi transaction và phát hành snapshot mới.

Nếu revision đã cũ, API trả conflict; Portal giữ bản chỉnh sửa cục bộ và yêu cầu người dùng tải lại trước khi lưu tiếp. Lượt đang chạy không bị thay đổi giữa chừng. Tin nhắn bắt đầu sau commit dùng snapshot mới ngay, không restart daemon.

## 10. HTTP API quản trị

Các route cụ thể sẽ tuân theo convention `/agent` và `/kb` hiện tại, nhưng phải cung cấp các hành vi sau:

- Liệt kê Provider đã làm sạch secret.
- Tạo/sửa/bật/tắt/xóa Provider.
- Thay/xóa credential.
- Kiểm tra kết nối mà chưa bắt buộc lưu thay đổi thất bại.
- Khám phá và đồng bộ model; lỗi discovery không xóa cache model cũ.
- Thêm model thủ công.
- Đọc route cùng revision.
- Ghi toàn bộ route theo transaction và revision.
- Đọc telemetry tổng hợp tối thiểu.

Tất cả mutation tiếp tục yêu cầu Portal mutation header hiện có. Secret không được đặt trong URL hoặc query string.

## 11. Portal UI

### 11.1 Navigation

Thêm `Providers` cạnh `Models` trong nhóm phù hợp của rail legacy. Không đổi menu, route mặc định hoặc CSS của `/zalo` ngoài mục mới này.

### 11.2 Trang Providers

- Dùng component legacy `.facts` để liệt kê tên, loại, số model, trạng thái và lần kiểm tra gần nhất.
- `Thêm Provider` và sửa Provider mở sheet editor cùng ngôn ngữ hình ảnh với AI Agents.
- Form gồm loại Provider, tên hiển thị và API key; endpoint là cố định theo loại trong MVP.
- Key chỉ hiển thị `Đã cấu hình`, không có nút xem plaintext.
- `Kiểm tra kết nối` không tự lưu cấu hình nếu thất bại.
- Kết nối thành công kích hoạt model discovery; model discovery lỗi giữ model cache cũ và cho nhập model ID thủ công.
- Claude Code hiển thị như Provider hệ thống, không có thao tác xóa hoặc credential.

### 11.3 Trang Models

- Thay ba model cố định bằng chuỗi fallback gồm các hàng `Provider + model`.
- Đổi thứ tự bằng nút Lên/Xuống hỗ trợ bàn phím; không dùng drag-and-drop trong MVP.
- Cho thêm, xóa và bật/tắt route entry.
- Hiển thị trạng thái Provider và nhãn `đang dùng` cho Provider/model phục vụ thành công gần nhất.
- Nút `Lưu` áp dụng ngay từ tin nhắn kế tiếp, không restart.
- Claude Code bị ghim cuối và không thể xóa hoặc đưa lên trên.

UI desktop tiếp tục bám contract legacy; mobile dùng rail hiện có và editor phải giữ focus, Escape, Tab trap cùng lifecycle an toàn như sheet AI Agents.

## 12. Xử lý lỗi

- `degraded`: timeout, network, `429`, `5xx`; được phép fallback.
- `configuration error`: credential, model, malformed request; dừng route.
- `policy/content error`: dừng route để không né chính sách bằng Provider khác.
- `unavailable`: tất cả mục đều thất bại.
- `credential unreadable`: DPAPI không giải mã được; yêu cầu nhập lại key.
- `revision conflict`: cấu hình đã đổi ở tab/process khác; không ghi đè.

Thông báo Portal phải nêu Provider/model và hành động khắc phục nhưng không hiển thị response body nhạy cảm.

## 13. Kiểm thử

### Backend

- Unit test mapping request/response và error classification cho từng adapter.
- Router test: thành công ngay, fallback đúng loại lỗi, dừng đúng loại lỗi, Claude Code cuối chuỗi, timeout tổng và cancellation.
- Router test: một lượt có attachment bỏ qua mọi API Provider và dùng Claude Code, không serialize hoặc upload nội dung file.
- Store test: transaction, revision conflict, delete guard và snapshot isolation.
- DPAPI test trên Windows: round-trip, API không trả secret và credential không đọc được chuyển sang trạng thái nhập lại.
- HTTP test cho CRUD, connection test, discovery, route validation và mutation header.

### Portal

- DOM contract cho Providers, sheet editor, credential masking, connection test và model discovery.
- Models contract cho thêm/xóa/bật/tắt, Lên/Xuống bằng bàn phím, Claude Code cuối chuỗi và revision conflict giữ draft.
- Giữ toàn bộ test Knowledge, Agents, shell, router, mobile rail và API hiện có.

### Tích hợp và đóng gói

- Fake HTTP servers mô phỏng bốn API Provider.
- End-to-end router test: Provider đầu trả `429`, Provider sau thành công, telemetry phản ánh fallback.
- Full Go tests, Portal tests, Zalo build/tests/typecheck và package credential gate.
- Kiểm tra package không chứa plaintext API key và `/zalo` không bị CSS/route hồi quy.

## 14. Tiêu chí nghiệm thu

- Người dùng thêm được một API Provider mà key không bao giờ được đọc lại qua Portal.
- Connection test và model discovery hoạt động; có đường nhập model thủ công.
- Người dùng cấu hình và sắp xếp được chuỗi fallback toàn cục.
- Route mới có hiệu lực từ tin nhắn kế tiếp mà daemon không restart.
- Router chỉ fallback trên timeout/network/`429`/`5xx`, dừng trên lỗi cấu hình hoặc policy.
- Tin có file/ảnh chỉ dùng Claude Code; API Provider không nhận đường dẫn hoặc nội dung attachment.
- Claude Code luôn tồn tại ở cuối chuỗi và giữ tương thích với cấu hình hiện tại.
- Telemetry đủ để biết Provider/model, trạng thái và fallback nhưng không chứa nội dung khách.
- Giao diện legacy và toàn bộ hành vi Knowledge, Agents, Models cũ đã thay đổi có chủ đích, `/zalo`, mobile rail và build package vẫn qua regression test.

## 15. Rủi ro và biện pháp

- API Provider thay đổi format: cô lập trong adapter và khóa bằng fixture tests.
- Error classification sai gây fallback không mong muốn: dùng taxonomy chung và test từng status/error class.
- DPAPI gây khó chuyển máy: giữ metadata và trạng thái nhập lại thay vì mất Provider.
- Chuỗi dài tăng độ trễ: giới hạn timeout từng Provider và timeout tổng; chưa cho chuỗi chiến lược phức tạp.
- UI Models hiện tại đổi mục đích: migration giữ Claude Code/model đang chọn thành route mặc định tương đương.
- Secret rò qua log: central redaction và negative tests trên API/log/package.

## 16. Migration và tương thích

Khi schema mới được tạo lần đầu, daemon tạo Provider hệ thống Claude Code và route mặc định dùng model hiện tại. Nếu `data\model.txt` tồn tại, model đó được dùng cho entry Claude Code; nếu không, dùng default hiện hành. Migration không xóa file cũ trong MVP để rollback vẫn khả dụng, nhưng route mới trở thành nguồn cấu hình chính sau khi migration thành công.

Không migration nào thay đổi Knowledge, persona, roster, Zalo credential hoặc lịch sử hội thoại.
