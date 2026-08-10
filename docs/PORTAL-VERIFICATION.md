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

`tests\build-app.Tests.ps1` là hợp đồng checkpoint trực tiếp: script chạy đúng 10 cổng top-level theo kiểu fail-fast, in một dòng `PASS:` cho mỗi cổng và trả exit khác 0 ngay khi có lỗi. Không dùng số lượng test do Pester enumerate làm tiêu chí chấp nhận.

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

## Kế hoạch smoke Memory V2 — chưa chạy

Sáu hành trình dưới đây là checklist cho lần triển khai Memory V2 sau khi candidate được duyệt; tài liệu này chưa ghi nhận hành trình nào đã chạy hay đã đạt. Chỉ dùng tài khoản/liên hệ thử nghiệm và dữ liệu giả, không dùng tên, số điện thoại, địa chỉ, tình trạng sức khoẻ, thông tin tài chính hoặc bí mật thật. Ảnh chụp và log phải che ID/token và không được chép nguyên prompt, câu trả lời hay nội dung khách hàng.

### Thứ tự backup và thay binary bắt buộc — chưa chạy

Không được sao chép database đang hoạt động. Khi được duyệt triển khai, người vận hành phải thực hiện đúng thứ tự sau và dừng ngay khi một cổng thất bại:

1. Lưu `$ErrorActionPreference`, đặt thành `Stop` trong toàn bộ thao tác rồi luôn khôi phục trong `finally`; vẫn kiểm tra riêng `$LASTEXITCODE` sau mọi native command. Resolve và in đường dẫn tuyệt đối của candidate, live binary, live data, `Start.vbs`, thư mục backup và hai file tạm chính xác `D:\TuvanZalo\app\agentdc.memory-v2.staged.exe` / `agentdc.memory-v2.rollback.exe`. Hai file tạm phải nằm trong live app directory, khác live binary và chưa tồn tại trước khi dừng; mọi collision phải fail trước stop.
2. Trước khi dừng daemon, tính SHA-256 candidate và so sánh không phân biệt hoa/thường với hash đã duyệt `940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45`; khác một ký tự cũng phải dừng. Chép candidate sang file staged cùng directory và xác minh staged hash cũng bằng hash đã duyệt. Không gọi `Stop.bat` trong runbook unattended vì file đó kết thúc bằng `pause >nul`. Đặt process environment `AGENTDC_HOME=D:\TuvanZalo\data`, `AGENTDC_PORT=8770`, gọi chính xác `D:\TuvanZalo\app\agentdc.exe daemon stop --force`, rồi yêu cầu native exit code bằng 0.
3. Xác nhận process `agentdc.exe` có executable path đúng `D:\TuvanZalo\app\agentdc.exe` đã kết thúc. Nếu còn process sau timeout, không chép data và không thay binary.
4. Chỉ sau khi xác nhận daemon đã dừng mới tạo thư mục backup có timestamp.
5. Chép live `agentdc.exe` và **toàn bộ** `D:\TuvanZalo\data` vào backup; ghi hash binary cũ và đường dẫn bản sao.
6. Xác nhận `backup\data\agentdc.db` tồn tại, mở **bản backup** bằng SQLite URI `mode=ro`, chạy `PRAGMA quick_check` và đọc `app_meta.schema_version`. Chỉ tiếp tục khi `quick_check` trả `ok` và schema đọc được; không chạy migration hay câu lệnh ghi trên bản backup.
7. Sau khi mọi bằng chứng backup đạt, xác minh lại staged candidate hash và rollback staging path vẫn chưa tồn tại, đặt cờ `liveMutationAttempted` **ngay trước** thao tác, rồi dùng `[IO.File]::Replace` để thay atomically từ staged file cùng directory sang live binary. Đọc lại hash live binary và chỉ khi bằng hash đã duyệt mới dọn đúng staging path do runbook tạo còn sót (nếu có) rồi khởi động qua `wscript.exe D:\TuvanZalo\Start.vbs` với cửa sổ ẩn.
8. Nếu `liveMutationAttempted` đã được đặt thì phải rollback bất kể `File.Replace` báo thành công hay lỗi: xác minh lại backup binary bằng hash cũ, chép nó vào rollback staging path cùng directory, xác minh staged rollback hash, rồi atomically `File.Replace` nếu live destination còn tồn tại hoặc `File.Move` nếu bị mất. Chỉ khởi động binary cũ sau khi hash live đã khôi phục đúng. Nếu lỗi xảy ra trước mutation, tuyệt đối không thay binary và chỉ khởi động lại binary cũ khi stop đã làm nó dừng. Mọi lỗi rollback/restart phải terminating và fail-closed; giữ backup cùng staging evidence khi thất bại, rồi khôi phục process environment và `$ErrorActionPreference` trong `finally`.

Trong nhóm, **Chung cho nhóm** là scope dùng chung cho mọi người trong đúng nhóm đó; mỗi tab thành viên là scope riêng chỉ của người đang chọn. Trạng thái **Đã đồng bộ với phiên Zalo** chỉ có nghĩa phiên đã nhận cả revision chung và revision của đúng thành viên đang chọn. Khi revision chung hoặc revision thành viên đổi, Portal phải hiện rằng Memory sẽ đồng bộ ở lượt nhắn tiếp; đổi người nói phải thay toàn bộ scope thành viên, không cộng dồn Memory của người trước.

1. **Đổi giữa hai thành viên trong nhóm, không lẫn Memory.** Tạo hai liên hệ thử nghiệm A và B trong một nhóm thử nghiệm, với hai ghi chú vô hại dễ phân biệt (ví dụ “màu thử nghiệm xanh” và “màu thử nghiệm cam”), cùng một ghi chú **Chung cho nhóm**. Mở Memory của nhóm, lần lượt chọn A rồi B và cho mỗi người gửi một lượt. Kỳ vọng: ghi chú chung xuất hiện ở cả hai lượt; tab và prompt của B không chứa Memory riêng của A, và ngược lại; sau lượt của mỗi người, trạng thái đồng bộ chỉ phản ánh revision chung cộng revision của chính người đó. Lưu ảnh hai tab và dấu vết refresh đã lược bỏ nội dung.

2. **Duyệt đề xuất thay thế xung đột và đồng bộ ở lượt kế.** Với thành viên A, tạo Memory thử nghiệm đang active, rồi tạo đề xuất pending cùng `memory_key` nhưng giá trị khác. Chọn **Duyệt**. Kỳ vọng: đề xuất trở thành active, giá trị cũ không còn active, lineage/provenance thay thế vẫn đúng, và Portal báo chờ đồng bộ. Cho A nhắn lượt kế tiếp; kỳ vọng lượt này có refresh của scope thành viên với revision mới, chỉ giá trị đã duyệt được dùng, rồi Portal chuyển sang **Đã đồng bộ với phiên Zalo**. Lưu ảnh trước/sau duyệt, revision và dấu vết refresh đã lược bỏ nội dung.

3. **Từ chối đề xuất nhạy cảm, không bao giờ prompt-active.** Dùng dữ liệu giả mang nhãn thử nghiệm thuộc một category nhạy cảm để tạo proposal pending; không dùng dữ liệu nhạy cảm thật. Xác nhận proposal chỉ nằm ở **Chờ duyệt**, sau đó chọn **Từ chối** và cho đúng thành viên nhắn tiếp. Kỳ vọng: proposal bị loại bỏ, không xuất hiện ở **Đang dùng**, và không có trong snapshot Memory active gửi vào prompt ở bất kỳ lượt nào. Lưu ảnh pending/trạng thái sau từ chối cùng bằng chứng prompt-safe chỉ ghi ID/category/count, không ghi giá trị.

4. **Khôi phục Memory hết hạn rồi ghim.** Chuẩn bị một dòng thử nghiệm ở **Đã hết hạn**, chọn **Khôi phục**, rồi chọn **Ghim**. Kỳ vọng: dòng chuyển sang **Đang dùng**, thời hạn được tính lại khi khôi phục; sau khi ghim, dòng hiển thị đã ghim và không còn tự hết hạn. Revision tăng sau từng mutation và trạng thái chờ lượt nhắn tiếp được hiển thị đúng. Lưu ảnh ba trạng thái và revision tương ứng.

5. **Khách riêng tư yêu cầu quên, xoá vật lý lineage nhưng giữ tin nhắn.** Trong một chat riêng của liên hệ thử nghiệm, tạo một Memory vô hại có lineage nhìn thấy được, rồi gửi yêu cầu rõ ràng chỉ quên Memory đó. Kỳ vọng: sau khi xử lý, toàn bộ các revision/superseded row thuộc đúng lineage bị xoá vật lý, Memory không còn trong Portal hoặc prompt; bản ghi tin nhắn nguồn và tin nhắn yêu cầu quên vẫn còn trong lịch sử hội thoại. Chứng minh bằng truy vấn SQLite chỉ đọc ghi số lượng lineage trước/sau và ID tin nhắn còn tồn tại; không chép nội dung tin nhắn vào biên bản.

6. **Hai lượt không đổi của cùng người nói, lượt hai không refresh.** Sau khi một lượt của A đã đồng bộ revision chung và revision thành viên, gửi thêm hai lượt vô hại từ A mà không sửa, thêm, hết hạn, ghim hay xoá Memory ở giữa. Kỳ vọng: lượt đầu chỉ refresh nếu còn revision chưa đồng bộ; lượt không đổi kế tiếp không có log/khối **Memory refresh**, trong khi mỗi lượt vẫn tạo đúng một lần gọi runner và trả lời bình thường. Lưu dấu vết chẩn đoán đã lược bỏ nội dung, thể hiện thread, revision và có/không có refresh.

Với mỗi hành trình, biên bản triển khai sau này phải ghi thời gian, candidate SHA-256, contact/thread thử nghiệm, revision trước/sau, kết quả PASS/FAIL và đường dẫn evidence; không thay các mục trên thành PASS nếu chưa thực sự quan sát. Trước khi smoke phải lưu đường dẫn backup, SHA-256 của binary cũ và candidate, kết quả `quick_check`/schema trên bản database được chép sau khi daemon dừng, cùng trạng thái daemon. Nếu bất kỳ hành trình nào thất bại, dừng smoke, giữ nguyên database đã migration và bản backup để chẩn đoán, khôi phục **chỉ** binary cũ theo thủ tục rollback rồi ghi lại hash/trạng thái khởi động; không xoá evidence.

## Real-service smoke: chưa chạy

Milestone 1 chưa đăng nhập tài khoản Zalo thật, chưa khởi động transport thật và chưa gửi tin. Phần smoke với dịch vụ thật được để dành cho milestone tích hợp sau, trên tài khoản thử nghiệm và có người vận hành giám sát.
