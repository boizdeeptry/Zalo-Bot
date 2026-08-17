# Onboarding modal trên nền lưới

## Mục tiêu

Trong giai đoạn chưa triển khai khung Portal mới, onboarding chỉ hiển thị wizard modal bắt buộc trên nền lưới toàn màn hình. Topbar, sidebar và thẻ trạng thái trang trí phía sau được tạm ẩn nhưng cấu trúc hiện có vẫn được giữ để có thể kích hoạt lại sau.

Mọi nội dung người dùng nhìn thấy phải thuộc về Tư Vấn Zalo. Không hiển thị tên, câu so sánh hoặc mô tả nhắc tới sản phẩm tham chiếu.

## Phạm vi

- Giữ nguyên toàn bộ flow Provider → Connect → Setup → Persona → Chat thử → Hoàn tất.
- Giữ một semantic modal không thể bỏ qua, focus ban đầu và các rào chắn accessibility hiện có.
- Mở rộng lớp canvas lưới phủ toàn viewport.
- Tạm ẩn topbar, sidebar và summary card trang trí bằng CSS có scope trong `.onboarding-shell`.
- Đổi copy giới thiệu sang “Tư Vấn Zalo” và mô tả thuần chức năng.

## Không làm

- Không xoá cấu trúc dashboard khỏi mã nguồn.
- Không thay đổi Portal sau khi onboarding hoàn tất.
- Không thay đổi backend, API, revision hoặc cơ chế xác minh.

## Kiểm thử

- DOM test xác nhận chỉ modal và canvas lưới có mặt về mặt trình bày; các phần trang trí còn lại bị ẩn.
- Copy test xác nhận không còn chuỗi tên sản phẩm tham chiếu trong bundle UI onboarding.
- CSS test xác nhận canvas phủ toàn màn hình, các thành phần trang trí bị ẩn và modal vẫn responsive/focus-visible.
- Chạy toàn bộ test Portal và build lại bản preview sạch.
