# Portal Zalo — biên bản xác minh Milestone 1

- Ngày xác minh: 2026-08-05
- Nguồn AgentDC chỉ đọc: `C:\Users\manva\OneDrive\Máy tính\agentdc`
- Commit nguồn AgentDC: `84612cc9f0c2491dbd10346269a41378e7633c9b`
- Nhánh overlay: `feature/portal-m1-foundation`
- Gói đã xác minh: `C:\Users\manva\AppData\Local\Temp\Kiểm thử Portal M1 final a013`

## Lệnh checkpoint

Chạy từ repository `_build`/worktree của nhánh overlay:

```powershell
pwsh -NoProfile -File .\tests\build-app.Tests.ps1

pwsh -NoProfile -File .\build-app.ps1 `
  -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' `
  -PersonaSource 'D:\TuvanZalo\brain\reference\persona' `
  -Out 'C:\Users\manva\AppData\Local\Temp\Kiểm thử Portal M1 final a013'
```

Lệnh build thứ hai tự chạy checkpoint trước khi tạo binary: bộ Go test với đúng bảy assertion asset cũ được bỏ qua, Portal test, Zalo compile/test/typecheck, build, cắt dependency phát triển và kiểm tra gói cuối.

## Kết quả tự động

| Cổng xác minh | Kết quả |
|---|---|
| PowerShell build fixtures | PASS — path có dấu/khoảng trắng, upstream sạch, đúng bảy test được skip, output an toàn, persona, package và overlay seams |
| Go `test -skip <7 assertion cũ> ./...` | PASS — toàn bộ package có test |
| Portal `npm test` | PASS — 18/18 |
| Zalo TypeScript compile | PASS |
| Zalo transport `npm test` | PASS — 3/3 |
| Zalo transport `npm run typecheck` | PASS |
| Package structure | PASS — 1.208 tệp, 93,2 MB; đủ binary, transport, Node, launcher, README và brain |
| Credential/identity gates | PASS — không có `credentials.json`, không còn chuỗi danh tính khách hàng |
| Upstream cleanliness | PASS — `git status --short` rỗng trước và sau build |

## Package HTTP smoke

Khởi động riêng `app\agentdc.exe daemon` trên port tạm với `AGENTDC_HOME` cô lập và không khởi động Zalo transport thật. Kết quả:

| Đường dẫn | HTTP |
|---|---:|
| `/status` | 200 |
| `/` | 200 |
| `/assets/app-main.js` | 200 |
| `/zalo` | 200 |

Các asset nhúng tại `/`, `/assets/core/router.js` và `/assets/pages/overview.js` có đủ nhãn **Tổng quan**, **Trợ lý AI**, **Tri thức**, **Mô hình** và liên kết **Hội thoại**.

## Real-service smoke: chưa chạy

Milestone 1 chưa đăng nhập tài khoản Zalo thật, chưa khởi động transport thật và chưa gửi tin. Phần smoke với dịch vụ thật được để dành cho milestone tích hợp sau, trên tài khoản thử nghiệm và có người vận hành giám sát.
