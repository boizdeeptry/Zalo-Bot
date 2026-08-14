# Onboarding Provider một chạm

## Mục tiêu

Rút gọn bước chọn Provider trong Onboarding thành một thao tác cài/kết nối rõ ràng:

- gạt Provider sang ON chỉ chọn Provider ở frontend;
- hàng đang ON hiển thị đúng copy `Chưa cài. Bấm để cài.`;
- bấm `Bấm để cài.` chạy ngay chuỗi chọn Provider → kiểm tra/cài CLI nếu thiếu → đăng nhập/kết nối;
- không còn nút `Tiếp tục kết nối` và không hiện bước trung gian `Bắt đầu kết nối` trong Onboarding;
- gạt OFF khi mới chọn nhưng chưa bắt đầu cài phải xác nhận trước.

Flow backend và các cổng an toàn hiện có vẫn được giữ nguyên.

## Quyết định thiết kế

### 1. Trạng thái trước khi cài chỉ tồn tại ở frontend

Ở phase `provider`, mỗi Provider có một nút ON/OFF riêng. Chỉ một Provider được ON tại một thời điểm.

- OFF: Provider chưa được chọn.
- ON: Provider được chọn cục bộ, chưa gọi API.
- ON hiển thị dòng `Chưa cài. Bấm để cài.`; phần `Bấm để cài.` là một button/link-button riêng có thể focus và kích hoạt bằng bàn phím.

Toàn bộ hàng không còn là một button duy nhất để tránh lồng control tương tác. Toggle và CTA cài đặt là hai control anh em trong một row không tương tác.

Copy `Chưa cài` là trạng thái thiết lập của Onboarding, không phải kết quả dò máy. Backend hiện chỉ kiểm tra CLI khi job connect bắt đầu; nếu CLI đã tồn tại, job tự bỏ qua cài đặt và đi thẳng tới đăng nhập/kết nối.

### 2. Một click chạy toàn bộ chuỗi Connect

Khi người dùng bấm `Bấm để cài.`:

1. Khóa toggle và CTA theo cơ chế one-flight.
2. Gọi tuần tự `PUT /onboarding/provider` với Provider và revision hiện tại.
3. Chỉ nhận successor hợp lệ: phase `connect`, đúng Provider, revision tăng đúng 1 và chưa có staging identity.
4. Mount Provider Connect bằng nhãn nội bộ `Onboarding` và revision mới.
5. Khởi chạy connect ngay, không render prompt nhập nhãn và không render nút `Bắt đầu kết nối`.
6. Giữ nguyên detect → install nếu thiếu → login → poll → bind staging account → Setup hiện có.

Provider Connect nhận tùy chọn khởi chạy ngay chỉ dành cho Onboarding. Mặc định của component không đổi, nên trang Providers vẫn có form nhãn tài khoản và nút `Bắt đầu kết nối` như hiện tại.

Nếu Onboarding được mở lại ở phase `connect`, controller dùng cùng chế độ khởi chạy ngay để tiếp tục hoặc nối lại job cùng Provider/revision. Backend hiện đã bảo vệ cùng-context idempotency và từ chối context khác đang bận.

### 3. OFF và chuyển Provider phải có xác nhận

Khi Provider đang ON nhưng CTA cài đặt chưa được kích hoạt:

- bấm toggle OFF mở một confirmation panel ngay trong modal Onboarding;
- chọn `Huỷ` giữ Provider ở ON và trả focus về toggle;
- chọn `Vẫn tắt` xóa lựa chọn cục bộ, không gọi backend;
- chọn ON một Provider khác cũng phải đi qua cùng xác nhận vì thao tác đó đồng thời tắt Provider đang chọn.

Confirmation dùng semantic dialog/alert phù hợp, có tên truy cập được, quản lý focus và hỗ trợ Escape như hành động `Huỷ`.

Sau khi chuỗi API đã bắt đầu, màn hình chuyển sang Connect và không còn toggle. Người dùng dừng bằng nút `Huỷ` trong progress panel. Không mô tả thao tác này là gỡ cài đặt: backend chỉ hủy job, không hoàn tác CLI đã được npm cài và không có endpoint đưa state về phase `provider`.

### 4. Lỗi, retry và response bị mất

- Double-click chỉ tạo một mutation chain.
- Nếu `PUT /onboarding/provider` lỗi hoặc response bị mất, frontend tải lại `/onboarding/status` trước khi cho retry.
- Nếu status là đúng successor `connect` cho Provider/revision vừa yêu cầu, tiếp tục Connect thay vì gửi lại PUT cũ.
- Nếu server vẫn ở state ban đầu, trả về row ON và cho bấm lại.
- Nếu state đã chuyển sang phase hợp lệ khác, render state authoritative đó.
- Nếu không thể chứng minh trạng thái an toàn, hiển thị lỗi chung và nút `Thử lại`; không suy đoán request đã thất bại.
- Các lỗi Connect tiếp tục dùng progress/log, cancel reconciliation và validation hiện có.

## Phạm vi mã nguồn

- `onboarding-early-view.js`: row, toggle, CTA và confirmation UI.
- `onboarding.js`: local selection/off confirmation, one-flight và nối chuỗi chọn Provider với Connect.
- `provider-connect.js`: chế độ start ngay có opt-in; mặc định trang Providers không đổi.
- `portal.css`: trạng thái row/link/toggle/confirmation responsive và focus-visible.
- Test frontend liên quan đến Onboarding, Provider Connect và regression trang Providers.

Không cần thay đổi backend hoặc database cho happy path này.

## Không làm

- Không thêm cơ chế gỡ CLI.
- Không giả lập backend đã quay về phase `provider` sau khi job Connect bị hủy.
- Không thay đổi nhãn tài khoản tùy chỉnh ở trang Providers.
- Không bỏ revision, identity, staging, Test Chat hoặc Complete gates.
- Không hiển thị tên hay nội dung liên quan tới sản phẩm tham chiếu trong runtime UI.

## Tiêu chí nghiệm thu

- ON hiển thị `Chưa cài. Bấm để cài.` đúng như thiết kế.
- CTA là control riêng và một click bắt đầu connect/install mà không qua hai nút trung gian cũ.
- Không có `Tiếp tục kết nối` ở Welcome và không có `Bắt đầu kết nối` trong Connect của Onboarding.
- Trang Providers thông thường vẫn giữ prompt `Bắt đầu kết nối`.
- OFF/chuyển Provider trước khi cài bắt buộc xác nhận; `Huỷ` và `Vẫn tắt` hoạt động đúng, không gọi API.
- Double-click, dispose, refresh, lost response và stale revision không tạo job/mutation trùng.
- Desktop và mobile không bị tràn ngang; keyboard focus và screen-reader labels hợp lệ.
- Toàn bộ Portal Node/static tests xanh và bản preview được build lại để kiểm tra trực tiếp.
