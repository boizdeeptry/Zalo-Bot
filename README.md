# Zalo-Bot — nguồn đóng gói bản bán

Repo này **không phải** mã nguồn của bot. Mã nguồn nằm trong repo AgentDC; repo này chứa lớp
overlay, launcher, brain skeleton và script đóng gói để dựng ra bản giao cho người mua.

Nguyên tắc của `build-app.ps1`: **không sửa repo nguồn một byte nào.** Nó copy repo sang thư mục
tạm, áp overlay và seam ở đó, build ở đó, rồi xoá. Chạy xong `git status` của repo nguồn phải sạch
y như trước.

## Chuẩn bị máy

Hai đường dẫn thay đổi theo từng máy, nên chúng nằm trong biến môi trường chứ không nằm trong
repo. Đặt một lần cho tài khoản của bạn:

```powershell
[Environment]::SetEnvironmentVariable('ZALOBOT_REPO', 'C:\duong\dan\toi\AgentDC', 'User')
[Environment]::SetEnvironmentVariable('ZALOBOT_PERSONA', 'C:\duong\dan\toi\brain\reference\persona', 'User')
```

| Biến | Trỏ tới | Yêu cầu |
| --- | --- | --- |
| `ZALOBOT_REPO` | Checkout AgentDC | Là repo Git, working tree **sạch**, đã chạy `yarn install` trong `tuvan-zalo\` |
| `ZALOBOT_PERSONA` | Thư mục persona nguồn | Chứa `persona.md` và `roster.md` |

Mở lại terminal sau khi đặt. Công cụ cần có trên PATH: `go`, `npm`, `node`, `python`, `pwsh`.

**Git phải từ 2.19 trở lên.** Bản cũ hơn thiếu những cờ mà script dùng; xem `scripts/BuildApp.psm1`.

## Hai lệnh

Vòng lặp nhanh khi sửa code Go trong `appmode\overlay\` — stage rồi `go test`, ~40 giây:

```powershell
pwsh -NoProfile -File .\scripts\go-check.ps1
```

Đóng gói đầy đủ. Đây là thứ duy nhất chạy hết cửa chặn: test Portal, test và typecheck Zalo,
quét dấu khách hàng, và cửa chặn phiên Zalo:

```powershell
pwsh -NoProfile -File .\build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out '<thu-muc-goi>'
```

`go-check.ps1` **không thay thế** `build-app.ps1`. Chạy full build trước khi commit.

## Cửa chặn phiên Zalo

Mặc định build **xoá** `data\`, và đó không phải một tuỳ chọn cho tiện.

`data\zalo\credentials.json` là phiên Zalo của máy dùng để build. Ai đọc được tệp đó thì vào được
tài khoản Zalo đó. Nếu build trên máy đang đăng nhập rồi nén cả thư mục để bán, người mua nhận
luôn tài khoản Zalo của người bán.

`-KeepData` giữ `data\` qua nhiều lần build khi đang thử. Bản build kèm cờ đó **không được nén để
bán** — build lại không kèm cờ trước khi giao.

## Bố cục

| Đường dẫn | Nội dung |
| --- | --- |
| `appmode\overlay\` | Tệp Go và web đè lên repo nguồn lúc stage |
| `appmode\tests\` | Test Portal (`npm --prefix appmode test`) |
| `scripts\BuildApp.psm1` | Staging, seam, và các cửa chặn |
| `scripts\go-check.ps1` | Vòng lặp Go nhanh |
| `launcher\` | `Start.vbs`, `Stop.bat`, `README.txt` giao cho người mua |
| `brain-skeleton\` | Vault Second Brain giao sẵn trong gói |
| `tests\` | Pester test cho chính script đóng gói |
| `.planning\` | Spec và plan (`/discuss` → `/plan` → `/execute`) |
