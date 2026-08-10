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

## Checklist smoke Memory V2

Sáu hành trình dưới đây là checklist cho lần triển khai Memory V2 sau khi candidate được duyệt. Phần tự động đã chạy được ghi riêng trong biên bản ngày 2026-08-10; phần cần lượt Zalo thật vẫn để mở. Chỉ dùng tài khoản/liên hệ thử nghiệm và dữ liệu giả, không dùng tên, số điện thoại, địa chỉ, tình trạng sức khoẻ, thông tin tài chính hoặc bí mật thật. Ảnh chụp và log phải che ID/token và không được chép nguyên prompt, câu trả lời hay nội dung khách hàng.

### Thứ tự backup và thay binary bắt buộc

Không được sao chép database đang hoạt động. Khi được duyệt triển khai, người vận hành phải thực hiện đúng thứ tự sau và dừng ngay khi một cổng thất bại:

1. Lưu `$ErrorActionPreference`, đặt thành `Stop` trong toàn bộ thao tác rồi luôn khôi phục trong `finally`; vẫn kiểm tra riêng `$LASTEXITCODE` sau mọi native command. Resolve và in đường dẫn tuyệt đối của candidate, live binary, live data, `Start.vbs`, thư mục backup và bốn recovery path chính xác trong `D:\TuvanZalo\app`: `agentdc.memory-v2.staged.exe`, `agentdc.memory-v2.rollback.exe`, `agentdc.memory-v2.replaced-old.exe` và `agentdc.memory-v2.failed-candidate.exe`. Mọi path phải nằm trong live app directory, khác live binary và chưa tồn tại trước khi dừng; mọi collision phải fail trước stop.
2. Trước khi dừng daemon, tính SHA-256 candidate và so sánh không phân biệt hoa/thường với hash đã duyệt `940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45`; khác một ký tự cũng phải dừng. Chép candidate sang file staged cùng directory và xác minh staged hash cũng bằng hash đã duyệt. Không gọi `Stop.bat` trong runbook unattended vì file đó kết thúc bằng `pause >nul`. Đặt process environment `AGENTDC_HOME=D:\TuvanZalo\data`, `AGENTDC_PORT=8770`, gọi chính xác `D:\TuvanZalo\app\agentdc.exe daemon stop --force`, rồi yêu cầu native exit code bằng 0.
3. Xác nhận process `agentdc.exe` có executable path đúng `D:\TuvanZalo\app\agentdc.exe` đã kết thúc. Nếu còn process sau timeout, không chép data và không thay binary.
4. Chỉ sau khi xác nhận daemon đã dừng mới tạo thư mục backup có timestamp.
5. Chép live `agentdc.exe` và **toàn bộ** `D:\TuvanZalo\data` vào backup; ghi hash binary cũ và đường dẫn bản sao.
6. Xác nhận `backup\data\agentdc.db` tồn tại, mở **bản backup** bằng SQLite URI `mode=ro`, chạy `PRAGMA quick_check` và đọc `app_meta.schema_version`. Chỉ tiếp tục khi `quick_check` trả `ok` và schema đọc được; không chạy migration hay câu lệnh ghi trên bản backup.
7. Sau khi mọi bằng chứng backup đạt, xác minh lại staged candidate hash và ba recovery path còn lại vẫn chưa tồn tại, đặt cờ `liveMutationAttempted` **ngay trước** thao tác, rồi dùng `[IO.File]::Replace(staged, live, replaced-old)` với **backup path không rỗng trong cùng directory**. Runtime trên máy này từ chối `$null`. Đọc lại hash live binary và hash automatic replace backup; chỉ khởi động qua `wscript.exe D:\TuvanZalo\Start.vbs` khi chúng lần lượt bằng candidate hash và old hash.
8. Nếu `liveMutationAttempted` đã được đặt thì phải rollback bất kể `File.Replace` báo thành công hay lỗi: xác minh lại backup binary bằng hash cũ, chép nó vào rollback staging path cùng directory, xác minh staged rollback hash, rồi atomically `File.Replace(rollback-stage, live, failed-candidate)` với non-empty backup path nếu live destination còn tồn tại hoặc `File.Move` nếu bị mất. Chỉ khởi động binary cũ sau khi hash live đã khôi phục đúng. Nếu lỗi xảy ra trước mutation, tuyệt đối không thay binary và chỉ khởi động lại binary cũ khi stop đã làm nó dừng. Mọi lỗi rollback/restart phải terminating và fail-closed; giữ backup cùng staging evidence khi thất bại, rồi khôi phục process environment và `$ErrorActionPreference` trong `finally`.

Trong nhóm, **Chung cho nhóm** là scope dùng chung cho mọi người trong đúng nhóm đó; mỗi tab thành viên là scope riêng chỉ của người đang chọn. Trạng thái **Đã đồng bộ với phiên Zalo** chỉ có nghĩa phiên đã nhận cả revision chung và revision của đúng thành viên đang chọn. Khi revision chung hoặc revision thành viên đổi, Portal phải hiện rằng Memory sẽ đồng bộ ở lượt nhắn tiếp; đổi người nói phải thay toàn bộ scope thành viên, không cộng dồn Memory của người trước.

1. **Đổi giữa hai thành viên trong nhóm, không lẫn Memory.** Tạo hai liên hệ thử nghiệm A và B trong một nhóm thử nghiệm, với hai ghi chú vô hại dễ phân biệt (ví dụ “màu thử nghiệm xanh” và “màu thử nghiệm cam”), cùng một ghi chú **Chung cho nhóm**. Mở Memory của nhóm, lần lượt chọn A rồi B và cho mỗi người gửi một lượt. Kỳ vọng: ghi chú chung xuất hiện ở cả hai lượt; tab và prompt của B không chứa Memory riêng của A, và ngược lại; sau lượt của mỗi người, trạng thái đồng bộ chỉ phản ánh revision chung cộng revision của chính người đó. Lưu ảnh hai tab và dấu vết refresh đã lược bỏ nội dung.

2. **Duyệt đề xuất thay thế xung đột và đồng bộ ở lượt kế.** Với thành viên A, tạo Memory thử nghiệm đang active, rồi tạo đề xuất pending cùng `memory_key` nhưng giá trị khác. Chọn **Duyệt**. Kỳ vọng: đề xuất trở thành active, giá trị cũ không còn active, lineage/provenance thay thế vẫn đúng, và Portal báo chờ đồng bộ. Cho A nhắn lượt kế tiếp; kỳ vọng lượt này có refresh của scope thành viên với revision mới, chỉ giá trị đã duyệt được dùng, rồi Portal chuyển sang **Đã đồng bộ với phiên Zalo**. Lưu ảnh trước/sau duyệt, revision và dấu vết refresh đã lược bỏ nội dung.

3. **Từ chối đề xuất nhạy cảm, không bao giờ prompt-active.** Dùng dữ liệu giả mang nhãn thử nghiệm thuộc một category nhạy cảm để tạo proposal pending; không dùng dữ liệu nhạy cảm thật. Xác nhận proposal chỉ nằm ở **Chờ duyệt**, sau đó chọn **Từ chối** và cho đúng thành viên nhắn tiếp. Kỳ vọng: proposal bị loại bỏ, không xuất hiện ở **Đang dùng**, và không có trong snapshot Memory active gửi vào prompt ở bất kỳ lượt nào. Lưu ảnh pending/trạng thái sau từ chối cùng bằng chứng prompt-safe chỉ ghi ID/category/count, không ghi giá trị.

4. **Khôi phục Memory hết hạn rồi ghim.** Chuẩn bị một dòng thử nghiệm ở **Đã hết hạn**, chọn **Khôi phục**, rồi chọn **Ghim**. Kỳ vọng: dòng chuyển sang **Đang dùng**, thời hạn được tính lại khi khôi phục; sau khi ghim, dòng hiển thị đã ghim và không còn tự hết hạn. Revision tăng sau từng mutation và trạng thái chờ lượt nhắn tiếp được hiển thị đúng. Lưu ảnh ba trạng thái và revision tương ứng.

5. **Khách riêng tư yêu cầu quên, xoá vật lý lineage nhưng giữ tin nhắn.** Trong một chat riêng của liên hệ thử nghiệm, tạo một Memory vô hại có lineage nhìn thấy được, rồi gửi yêu cầu rõ ràng chỉ quên Memory đó. Kỳ vọng: sau khi xử lý, toàn bộ các revision/superseded row thuộc đúng lineage bị xoá vật lý, Memory không còn trong Portal hoặc prompt; bản ghi tin nhắn nguồn và tin nhắn yêu cầu quên vẫn còn trong lịch sử hội thoại. Chứng minh bằng truy vấn SQLite chỉ đọc ghi số lượng lineage trước/sau và ID tin nhắn còn tồn tại; không chép nội dung tin nhắn vào biên bản.

6. **Hai lượt không đổi của cùng người nói, lượt hai không refresh.** Sau khi một lượt của A đã đồng bộ revision chung và revision thành viên, gửi thêm hai lượt vô hại từ A mà không sửa, thêm, hết hạn, ghim hay xoá Memory ở giữa. Kỳ vọng: lượt đầu chỉ refresh nếu còn revision chưa đồng bộ; lượt không đổi kế tiếp không có log/khối **Memory refresh**, trong khi mỗi lượt vẫn tạo đúng một lần gọi runner và trả lời bình thường. Lưu dấu vết chẩn đoán đã lược bỏ nội dung, thể hiện thread, revision và có/không có refresh.

Với mỗi hành trình, biên bản triển khai sau này phải ghi thời gian, candidate SHA-256, contact/thread thử nghiệm, revision trước/sau, kết quả PASS/FAIL và đường dẫn evidence; không thay các mục trên thành PASS nếu chưa thực sự quan sát. Trước khi smoke phải lưu đường dẫn backup, SHA-256 của binary cũ và candidate, kết quả `quick_check`/schema trên bản database được chép sau khi daemon dừng, cùng trạng thái daemon. Nếu bất kỳ hành trình nào thất bại, dừng smoke, giữ nguyên database đã migration và bản backup để chẩn đoán, khôi phục **chỉ** binary cũ theo thủ tục rollback rồi ghi lại hash/trạng thái khởi động; không xoá evidence.

## Triển khai Memory V2 — 2026-08-10

- Nhánh/commit candidate đã duyệt: `feature/portal-m1-foundation` / `861658af89989f389d722637865e90f7ca44c4ea`
- Thời gian thao tác: 11:38–11:48 ICT
- Candidate: `D:\TuvanZalo\_artifacts\memory-v2\app\agentdc.exe`
- Live binary: `D:\TuvanZalo\app\agentdc.exe`
- Backup sau khi daemon dừng: `D:\TuvanZalo\_backups\memory-v2-20260810-113825`

### Backup và thay executable

| Bằng chứng | Kết quả |
|---|---|
| Candidate SHA-256 trước stop | `940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45` — khớp hash đã duyệt |
| Daemon cũ | PID 18824, executable path đúng `D:\TuvanZalo\app\agentdc.exe`; native stop exit 0 và process biến mất trước khi copy data |
| Binary cũ / backup binary SHA-256 | `1013F1A2D6F736F7238391B242ABE3BEEDB7B8F6914FCF13E3BA85FDBC384791` — khớp tuyệt đối |
| Backup DB | `data\agentdc.db`, SHA-256 `68DCE8FCCF2491BE5D1CD7B4C8859ED241F21D5E03F5FDC894856CC4DFD01933`; read-only `PRAGMA quick_check=ok`; schema trước migration = 3 |
| Live binary sau replace | 20.500.480 byte; SHA-256 `940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45` |
| Recovery evidence | `rollback-stage-agentdc.exe` và `file-replace-backup-agentdc.exe` trong backup timestamp; mỗi tệp có SHA-256 đúng hash binary cũ |

Lần gọi đầu dùng `File.Replace(source, destination, $null)` dừng trước mutation vì runtime này
trả `MethodInvocationException` HResult `0x80131501`, inner `ArgumentException` với thông báo
`The path is empty. (Parameter 'path')` và HResult `0x80070057`. Audit ngay sau lỗi xác nhận live
binary vẫn mang đúng hash cũ; staged candidate, rollback stage và backup đều còn nguyên nên không
thực hiện rollback giả.

Compatibility được chứng minh chỉ trên hai bản sao trong
`C:\Users\manva\AppData\Local\Temp\agentdc-memory-v2-replace-test-20260810-113928737`:
overload có **backup path không rỗng trong cùng directory** thay atomically, destination nhận đúng
candidate hash và automatic backup nhận đúng old hash. Live replace sau đó dùng đúng form đã chứng
minh này. Hai recovery copy được hash-check rồi chuyển nguyên vẹn vào backup timestamp; app directory
không còn tệp staging/recovery gây collision.

### Trạng thái live sau migration

| Cổng | Kết quả |
|---|---|
| Process | PASS — đúng một process, PID 8424, executable path tuyệt đối đúng live binary |
| `/status` | HTTP 200; version 0.10.0; started_at `2026-08-10T11:40:34.0955246+07:00` |
| Portal/Zalo shell | `/`, `/zalo`, `/assets/app-main.js`, `/assets/zalo.js`, `/assets/zalo.css` đều HTTP 200 |
| Memory V2 bundle/API | `/memory`, `/assets/pages/memory.js`, `memory-view.js`, `memory-actions.js`, `/assets/portal.css` đều HTTP 200 |
| Live DB | read-only `PRAGMA quick_check=ok`; `app_meta.schema_version=4` |
| Live executable | SHA-256 vẫn đúng candidate đã duyệt sau start và sau smoke |

### Smoke an toàn bằng dữ liệu giả

Không gửi tin ra Zalo và không dùng liên hệ thật. Chính installed binary được chạy thêm trên port 8781
với home cô lập tại `D:\TuvanZalo\_backups\memory-v2-20260810-113825\smoke-home`; daemon cô lập đã
dừng sạch sau test. Database evidence cuối có SHA-256
`358FDCBAC3A0D19DA0E338B65B0636BB8834FCAE03A583885A5247C40DFC72B5`,
`quick_check=ok` và schema 4.

| Hành trình | Phần tự động đã quan sát | Kết quả |
|---|---|---|
| 1. Hai thành viên, không lẫn scope | Scope chung và hai UID giả trả đúng từng tập; A/B không thấy row riêng của nhau; common revision 1, mỗi subject revision 1; session seed chỉ đồng bộ đúng A | PASS |
| 2. Duyệt replacement | HTTP approve 200; target cũ thành `superseded`, proposal thành `active`, lineage giữ `supersedes_id`; revision A 1→2 và `synced=false` chờ lượt kế | PASS |
| 3. Từ chối nhạy cảm | HTTP reject 204; pending row bị xoá, không có active row cùng key, revision B giữ nguyên 1 | PASS |
| 4. Khôi phục rồi ghim | Restore 200 tạo lại expiry và revision 2→3; pin 200 đưa revision 3→4, `pinned=true`, expiry `null` | PASS |
| 5. Quên lineage, giữ message | DELETE tương đương store lineage operation trả 204; cả hai revision lineage bị xoá vật lý, subject revision 1→2; hai source message giả vẫn còn | PASS cho store/API; parser yêu cầu quên qua Zalo còn chờ lượt thật |
| 6. Lượt sau không refresh | Hai lần đọc liên tiếp giữ nguyên common/subject revision và sync state; Go gate chạy `TestAppZaloSessionMemoryV2SynchronizesCurrentSpeakerInOneSession` và `TestAppZaloMemoryV2OperationUsesTrustedSubjectAndOneRunnerCall` | PASS tự động; quan sát hai lượt Zalo thật còn chờ người vận hành |

Điều khiển trình duyệt trong Codex đã thử cả `http://127.0.0.1:8770/memory` và
`http://localhost:8770/memory` nhưng client chặn localhost bằng `ERR_BLOCKED_BY_CLIENT`. Vì vậy biên
bản **không** đánh dấu quan sát visual hay thao tác click là PASS. HTTP shell/bundle thật đã đạt; phần
visual và các lượt nhắn Zalo phải được người vận hành kiểm trên tab local đang mở.

### Cổng tự động cuối trước commit

| Lệnh/cổng | Kết quả |
|---|---|
| `npm test --prefix appmode` | PASS — 89/89, fail 0 |
| `run-overlay-go-tests.ps1 -Package all` | PASS — toàn bộ package Go; daemon/store gồm contract speaker-scope, one-runner-call, refresh cursor và no-refresh khi revision không đổi |
| `tests/build-app.Tests.ps1` | PASS — đúng 10/10 checkpoint top-level |
| Parse PowerShell runbook | PASS — block hợp lệ; không còn `File.Replace(..., $null)`; cả forward replace và rollback dùng non-empty same-directory backup path |
| `git diff --check` | PASS |

### Sai lệch thứ tự và canary khắc phục — 2026-08-10 12:04–12:10 ICT

Thiết kế mục 14 yêu cầu chạy daemon của candidate trên bản sao data, smoke migration và Memory API
**trước khi** thay live binary. Trình tự thực tế ban đầu không đạt điều kiện thời gian này: live binary
được thay và start lúc khoảng 11:40, còn isolated API smoke đầu tiên bắt đầu sau đó, khoảng 11:45.
Vì vậy deployment ban đầu không được ghi là tuân thủ đầy đủ pre-deploy canary order.

Live vẫn được bảo vệ bởi các cổng độc lập đã chạy trước/sát mutation: candidate hash được pin, Node/Go/
package gates đạt, daemon cũ dừng trước backup, backup DB schema 3 có `quick_check=ok`, old binary có ba
bản hash-verified, replacement atomically dùng same-directory backup, và ngay sau start live DB schema 4,
health cùng Memory assets/API đều đạt. Những điều này giải thích vì sao live không mất dữ liệu và vẫn
healthy; chúng **không** thay thế hay hồi tố yêu cầu canary phải chạy trước replacement.

Để đóng assurance gap mà không dừng hoặc thay live lần nữa, một canary khắc phục hoàn chỉnh đã chạy
bằng đúng binary SHA-256 đã duyệt trên port riêng 8782, không cấu hình Zalo transport và không gửi dữ
liệu ra ngoài:

| Cổng remediation canary | Kết quả |
|---|---|
| V3 source | `D:\TuvanZalo\_backups\memory-v2-20260810-113825\data`; immutable URI `mode=ro&immutable=1`; DB SHA-256 `68DCE8FCCF2491BE5D1CD7B4C8859ED241F21D5E03F5FDC894856CC4DFD01933`; `quick_check=ok`, schema 3 |
| Exact copied home | `D:\TuvanZalo\_backups\memory-v2-20260810-113825\predeploy-remediation-canary-20260810-120449`; DB hash trước start khớp byte-for-byte V3 source; immutable `quick_check=ok`, schema 3 |
| Candidate migration | PID 10340 trên port 8782; `/status` và `/memory` HTTP 200; stop sạch; bản copy chuyển sang immutable `quick_check=ok`, schema 4; migrated DB SHA-256 trước fixture `D95DE61B55A1C2825FD92173D1FD361930A1909B87CADA82A12A4C71D927C5E6` |
| V3 preservation | Memory 0 row, lessons 0 row và CLI session mapping 1 row giữ nguyên; canonical session digest trước/sau `027EFE865ABEF8DEF1DFFA127DF700008ABD4A5CCBC97ED3FAD83A1B13ACA2A5` |
| Memory API restart | PID 11348 trên port 8782; `/`, `/memory`, `memory.js`, `memory-actions.js` HTTP 200 |
| J1–J6 local | Member/common isolation PASS; approve replacement PASS; reject sensitive pending PASS; restore/pin PASS; physical lineage delete + message retention PASS; repeated revision read stable PASS |
| Final canary DB | Sau clean stop: immutable `quick_check=ok`, schema 4; SHA-256 `914FB59881EA7A8F7EA93795CBB19CBD82EDAC6B262A07B572EAE1BB174F1124` |
| Source immutability | V3 source được đọc immutable; DB/WAL/SHM size và mtime baseline không đổi trong toàn bộ remediation canary |
| Live isolation | Live không bị stop hay replace; vẫn PID 8424, HTTP 200 và approved executable hash sau canary |

Lần gọi API assertion đầu tiên của remediation dừng trước GET vì lỗi ghép tham số PowerShell; không có
mutation nào xảy ra. Lệnh được sửa rồi chạy lại đầy đủ với các kết quả PASS ở trên. Canary này cung cấp
bằng chứng migration/API còn thiếu và runbook mới biến nó thành hard gate trước Step 8, nhưng không
thể làm cho trình tự 11:40/11:45 trong quá khứ trở thành đúng thứ tự.

Các cổng cuối sau khi bổ sung hard gate cũng đạt: Node `89/89`, Go `./...` toàn bộ package, package
`10/10`, kiểm tra thứ tự tĩnh xác nhận canary đứng trước Step 8, PowerShell runbook parse được và
`git diff --check` sạch.

### Phần còn chờ người vận hành

- [ ] Mở Portal local, xác nhận trực quan tab **Chung cho nhóm**, hai tab thành viên, badge đồng bộ và các nút Duyệt/Từ chối/Khôi phục/Ghim.
- [ ] Dùng tài khoản/liên hệ Zalo thử nghiệm để quan sát refresh ở lượt kế sau mutation và không refresh ở lượt không đổi tiếp theo.
- [ ] Xác nhận daemon resume đúng session của một thread thử nghiệm, mỗi lượt chỉ gọi runner một lần; không ghi nội dung khách hàng vào evidence.

## Real-service smoke: chưa chạy

Lần triển khai 2026-08-10 không gửi tin, reaction hay file tới bất kỳ tài khoản Zalo nào và không dùng dữ liệu khách hàng làm fixture. Smoke bằng lượt Zalo thật được để dành cho tài khoản thử nghiệm, có người vận hành giám sát; trạng thái transport hiện hữu không được dùng làm bằng chứng rằng sáu hành trình đã chạy.
