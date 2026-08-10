# Portal Zalo — biên bản xác minh Milestone 1

- Ngày xác minh: 2026-08-05
- Commit nguồn AgentDC: `84612cc9f0c2491dbd10346269a41378e7633c9b`
- Nhánh overlay: `feature/portal-m1-foundation`

Biên bản này ghi lại một lần chạy trên máy của người xác minh. Đường dẫn cụ thể của máy đó đã
được bỏ vì chúng không tái lập được ở nơi khác; xem `README.md` để chuẩn bị máy của bạn.

## Lệnh checkpoint

Chạy từ gốc repo đóng gói, trên nhánh overlay:

```powershell
if ([string]::IsNullOrWhiteSpace($env:ZALOBOT_REPO)) {
  throw 'Đặt ZALOBOT_REPO tới một checkout AgentDC sạch trước khi chạy checkpoint.'
}
pwsh -NoProfile -File .\tests\build-app.Tests.ps1

pwsh -NoProfile -File .\build-app.ps1 `
  -Repo $env:ZALOBOT_REPO `
  -PersonaSource $env:ZALOBOT_PERSONA `
  -Out (Join-Path $env:TEMP 'Kiem thu Portal M1')
```

`tests\build-app.Tests.ps1` là hợp đồng checkpoint trực tiếp: script hiện chạy đúng 11 cổng top-level theo kiểu fail-fast, in một dòng `PASS:` cho mỗi cổng và trả exit khác 0 ngay khi có lỗi. Script nhận `-UpstreamRepo` hoặc `ZALOBOT_REPO`, kiểm tra đó là Git checkout tồn tại và không chứa đường dẫn máy cụ thể. Không dùng số lượng test do Pester enumerate làm tiêu chí chấp nhận.

Checkout upstream phải sạch cả ngay trước staging lẫn sau acceptance. Repo/output được so bằng physical
volume identity lấy từ Win32 handle, không chỉ chuỗi path; alias `\\?\`, 8.3, SUBST/reparse hoặc lỗi
canonicalization đều không được dùng để vượt protected-root gate. `Clear-AppOutput` lặp lại phép kiểm
physical identity và quét toàn cây reparse ngay trước lần xóa đầu tiên.

Lệnh build thứ hai tự chạy checkpoint trước khi tạo binary: bộ Go test hiện bỏ qua **đúng tám** assertion đã duyệt, sau đó chạy Portal test, Zalo compile/test/typecheck, build, cắt dependency phát triển và kiểm tra gói cuối. Bảng M1 ngay dưới là biên bản lịch sử ngày 2026-08-05, khi danh sách skip lúc đó còn đúng bảy tên; không dùng con số lịch sử đó làm contract hiện tại.

## Kết quả tự động

| Cổng xác minh | Kết quả |
|---|---|
| PowerShell build fixtures (lịch sử 2026-08-05) | PASS — path có dấu/khoảng trắng, upstream sạch, đúng bảy test lúc đó được skip, output an toàn, persona, package và overlay seams |
| Go `test -skip <7 assertion cũ> ./...` (lịch sử) | PASS — toàn bộ package có test |
| Portal `npm test` | PASS — 18/18 |
| Zalo TypeScript compile | PASS |
| Zalo transport `npm test` | PASS — 3/3 |
| Zalo transport `npm run typecheck` | PASS |
| Package structure | PASS — 1.208 tệp, 93,2 MB; đủ binary, transport, Node, launcher, README và brain |
| Credential/identity gates | PASS — không có `credentials.json`, không còn chuỗi danh tính khách hàng |
| Upstream cleanliness | PASS — `git status --short` rỗng trước và sau build |

## Candidate tích hợp Provider + Memory/session — hợp đồng xác minh

Phần này là checklist cho candidate schema V5 đang tích hợp; nó **không** ghi nhận một deployment,
smoke Zalo thật hoặc kiểm tra thủ công đã hoàn tất. Các biên bản rollout schema 3→4 bên dưới vẫn là
lịch sử của binary trước và không được dùng để authorize candidate V5.

### Cổng tự động bắt buộc

Chạy trên candidate đã resolve sạch conflict, trước commit/đóng gói:

```powershell
# Runner checkpoint thật của repo; phải exit 0 và in đủ các PASS fail-fast.
if ([string]::IsNullOrWhiteSpace($env:ZALOBOT_REPO)) {
  throw 'Đặt ZALOBOT_REPO tới một checkout AgentDC sạch.'
}
pwsh -NoProfile -File .\tests\build-app.Tests.ps1

# Nếu máy có Pester, chạy cùng contract qua Pester và yêu cầu FailedCount = 0.
$result = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru
if ($result.FailedCount) { exit 1 }

# Trên disposable stage đã tạo bởi New-AppStage + Apply-AppSeams từ upstream đã pin:
go test -count=1 -run 'TestAppRoutes|TestMigrateApp|TestAppLLM|TestAppZaloSession' ./internal/daemon ./internal/store
go test -count=1 -skip '^(?:TestAppJSKnowsTheSessionEndedCloseReason|TestAppJSSendsThePortalMutationHeader|TestPortalReloadedKeyWithLiveSessionReachesTheShell|TestPortalRootServesTheShellWithACookie|TestPortalUsesModalNotBrowserDialogs|TestAgentPortalNoLongerCarriesZalo|TestModalCallsPassAnObject|TestJoinGreetsOnceForEveryone)$' ./...
```

Skip regex phải khớp **đúng tám tên** trên, có neo đầu/cuối; không wildcard và không được tự động
nuốt test tương lai có tiền tố/hậu tố gần giống.

### Schema V5: canary cho cả hai lineage V4

Không chạy canary này trên live DB. Tạo hai bản sao SQLite tạm độc lập, giữ nguyên source:

1. **Feature V4 → V5:** fixture có session/Memory V2 (`app_zalo_cli_sessions`,
   `app_memory_revisions`, `app_memory_subject_revisions`, proposal/lesson provenance). Sau migration,
   toàn bộ Memory/session row và revision phải giữ nguyên; Provider/model/account/Combo/route capability
   phải xuất hiện và không có Combo mặc định.
2. **Main V4 → V5:** fixture có Provider/model/account/Combo/member/route/attempt cùng credential cipher
   giả. Sau migration, toàn bộ routing/account/Combo row phải giữ nguyên; session/Memory V2 capability
   phải xuất hiện. Không log hoặc đưa cipher/canary vào evidence.
3. Cả hai bản sao phải đạt `PRAGMA quick_check=ok`, `app_meta.schema_version=5`; migration V5 lần hai
   không đổi dữ liệu. Fixture schema tương lai `>5` phải không bị mutation; lỗi muộn phải rollback toàn
   transaction và giữ version V4.

Các test chuẩn tương ứng gồm `TestMigrateAppFeatureV4ToV5PreservesMemoryAndAddsLLM`,
`TestMigrateAppMainV4ToV5PreservesRoutingAndAddsMemory`, `TestMigrateAppV5IsIdempotent`,
`TestMigrateAppMainV4RollsBackOnLateLLMFailure` và `TestMigrateAppSeedsNoDefaultCombo`.

### Provider, no-provider và ranh giới session

- Package binary phải mang đồng thời Memory V2, `/llm/providers`, `/llm/combos`, `llm_providers`,
  `llm_combos`, `app_zalo_cli_sessions`, `llm_accounts`, `@openai/codex`,
  `@anthropic-ai/claude-code`, `CODEX_HOME`, `CLAUDE_CONFIG_DIR` và marker no-provider; thiếu một
  signature phải fail-closed. Runtime đóng gói phải có `app\node\npm.cmd` cùng
  `app\node\node_modules\npm\bin\npm-cli.js`; checkpoint chạy `node npm-cli.js --version` với timeout,
  bằng chính `app\node\node.exe` và `npm-cli.js` bên trong candidate — không dùng host Node và không
  tải mạng. Checkpoint cũng chạy chính packaged `npm.cmd --version` qua bounded `cmd.exe /d /s /c`,
  yêu cầu shim production-shaped delegate về adjacent packaged node/npm-cli và trả cùng semver.
  Connect/cài package mới vẫn cần npm registry online. Public Provider credential scanner
  chỉ nhận package root rồi tự enumerate toàn bộ cây an toàn; caller không thể truyền subset file.
  Package phải từ
  chối Zalo credential, mọi canary `APP_TEST_` và Provider canary ở text/config/hidden file/binary;
  lỗi scan/read không được biến thành PASS và thông báo không được lộ secret.
- Máy mới không có Combo mặc định. Không Provider hoặc không active route trả `ErrZaloSilent` ở cổng
  vào và không gọi Claude mặc định; một Claude entry đã cấu hình nhưng terminal không chạy được phải
  leo thang, không bị hiểu nhầm thành trạng thái chưa cấu hình.
- Một lượt API/Codex/Gemini stateless nhận **full bootstrap prompt**, trả
  `SessionAdvanced=false`, không tăng turn/cursor/session Claude. Nếu API lỗi đủ điều kiện fallback,
  Claude nhận đúng delta/session ID và chỉ một lần chạy thành công mới trả `SessionAdvanced=true` rồi
  advance session đúng một lần. Virgin session sau stateless success vẫn fresh (`Resume=false`) cho
  lần Claude đầu tiên.
- `appAnswerZalo` là một entrypoint duy nhất; Provider route factory được compose đúng một lần **sau**
  khi đã acquire per-thread gate. Đây là invariant thực, dù source dùng method value
  `a.appZaloRunner` thay vì một direct call. Gate dùng Go AST parse toàn bộ package production
  `internal/daemon` (chủ đích loại `_test.go`), bỏ qua comment và từ chối selector/call ở file hoặc
  function khác, duplicate hoặc route đứng trước `Acquire`. Trước seam, toàn production tree phải có
  **zero** selector reference và zero direct call `a.appAnswerZalo(...)`; phép replace được guard chính
  xác mới chèn reference duy nhất dưới dạng direct call, và post-stage acceptance yêu cầu cả hai count
  bằng một. Vì vậy parenthesized invocation hay capture method value cũng bị từ chối.
  Attachment-local-only, recovery, rotation và speaker
  isolation phải tiếp tục xanh.

Các kiểm tra trọng tâm gồm
`TestAppLLMRunnerStructuredAPISuccessUsesFullPromptWithoutAdvancingSession`,
`TestAppLLMRunnerStructuredFallbackUsesClaudeDeltaAndAdvancesSession`,
`TestAppZaloVirginSessionStaysFreshAfterStatelessSuccess`,
`TestAppAnswerZaloBuildsProviderRunnerInsideThreadGate`, `TestAppZaloRunnerSilentWhenNoProvider` và
`TestRunClaudeEscalatesWhenTerminalDeclines`. Chỉ ghi PASS/live/manual sau khi chính lệnh hoặc thao tác
đã được thực hiện và evidence không chứa prompt, response, credential hay dữ liệu khách hàng.

## Package HTTP smoke

Khởi động riêng `app\agentdc.exe daemon` trên port tạm với `AGENTDC_HOME` cô lập và không khởi động Zalo transport thật. Kết quả:

| Đường dẫn | HTTP | Hợp đồng quan sát được |
|---|---:|---|
| `/status` | 200 | Daemon cô lập đã sẵn sàng |
| `/` | 200 | Management shell có `rail`, `main` và nạp `app-main.js` |
| `/assets/app-main.js` | 200 | Portal controller hiện tại được nhúng |
| `/assets/core/router.js` | 200 | Router hiện tại được nhúng |
| `/assets/pages/knowledge.js` | 200 | Trang Knowledge hiện tại được nhúng |
| `/zalo` | 200 | Zalo conversation shell vẫn được phục vụ |
| `/assets/pages/overview.js` | 404 | Asset cũ đã được bỏ có chủ đích |

Navigation hiện tại trong `core/router.js` mang các nhãn **AI Agents**, **Knowledge**, **Providers**,
**Combos**, **Memory** và **Conversations**. `TestAppPortalShellAssetsAreEmbedded` ghim
`pages/overview.js` là asset đã loại bỏ, còn `TestAppPackageServesManagementAssetsAndZalo` ghim các
management asset và `/zalo` đang được phục vụ; không dùng lại nhãn hoặc asset của shell cũ làm tiêu
chí smoke.

## Checklist smoke Memory V2

Sáu hành trình dưới đây là checklist cho lần triển khai Memory V2 sau khi candidate được duyệt. Phần tự động đã chạy được ghi riêng trong biên bản ngày 2026-08-10; phần cần lượt Zalo thật vẫn để mở. Chỉ dùng tài khoản/liên hệ thử nghiệm và dữ liệu giả, không dùng tên, số điện thoại, địa chỉ, tình trạng sức khoẻ, thông tin tài chính hoặc bí mật thật. Ảnh chụp và log phải che ID/token và không được chép nguyên prompt, câu trả lời hay nội dung khách hàng.

### Thứ tự backup và thay binary bắt buộc

Không được sao chép database đang hoạt động. Hai entry point dưới đây là runbook có hiệu lực; không
chép lại các đoạn PowerShell thủ công vào shell. Canary phải hoàn tất trước và manifest `PASS` mới,
đúng candidate/path/hash, schema `3→4`, J1–J6, isolation và clean stop phải được deploy script xác minh
**trước bất kỳ lệnh stop nào**:

```powershell
$stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ')
$manifest = Join-Path $PWD "docs\evidence\memory-v2-canary-$stamp.json"
$transcript = Join-Path $PWD "docs\evidence\memory-v2-canary-$stamp.transcript.json"

pwsh -NoProfile -File .\scripts\Invoke-MemoryV2Canary.ps1 `
  -CandidatePath 'D:\TuvanZalo\_artifacts\memory-v2\app\agentdc.exe' `
  -ApprovedCandidateHash '940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45' `
  -SourceHome 'D:\TuvanZalo\_backups\memory-v2-20260810-113825\data' `
  -CanaryHome (Join-Path ([IO.Path]::GetTempPath()) "agentdc-memory-v2-canary-$stamp") `
  -LiveRoot 'D:\TuvanZalo' -Port 8784 -ManifestPath $manifest -TranscriptPath $transcript `
  -ExpectedLiveSchema 3 -ExpectedTargetSchema 4
if ($LASTEXITCODE -ne 0) { throw 'Memory V2 canary failed' }

pwsh -NoProfile -File .\scripts\Invoke-MemoryV2Deploy.ps1 `
  -ManifestPath $manifest `
  -CandidatePath 'D:\TuvanZalo\_artifacts\memory-v2\app\agentdc.exe' `
  -ApprovedCandidateHash '940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45' `
  -LiveRoot 'D:\TuvanZalo' -BackupRoot 'D:\TuvanZalo\_backups' -Port 8770 `
  -ExpectedLiveSchema 3 -ExpectedTargetSchema 4
if ($LASTEXITCODE -ne 0) { throw 'Memory V2 deployment failed or rolled back' }
```

Hai giá trị `3 -> 4` trên chỉ ghi lại rollout lịch sử. Live hiện đã ở schema 4 nên manifest cũ **không
được phép** authorize thêm một deployment: preflight read-only sẽ fail trước staging/`StopLive`. Rollout
tiếp theo phải tạo canary mới từ retained backup đã dừng của đúng schema live hiện hành, rồi truyền rõ
schema live và schema target mới; không tái sử dụng manifest 3→4 này.

Hai script cơ học hóa thứ tự sau và dừng ngay khi một cổng thất bại:

1. Lưu `$ErrorActionPreference`, đặt thành `Stop` trong toàn bộ thao tác rồi luôn khôi phục trong `finally`; vẫn kiểm tra riêng `$LASTEXITCODE` sau mọi native command. Resolve và in đường dẫn tuyệt đối của candidate, live binary, live data, `Start.vbs`, thư mục backup và bốn recovery path chính xác trong `D:\TuvanZalo\app`: `agentdc.memory-v2.staged.exe`, `agentdc.memory-v2.rollback.exe`, `agentdc.memory-v2.replaced-old.exe` và `agentdc.memory-v2.failed-candidate.exe`. Mọi path phải nằm trong live app directory, khác live binary và chưa tồn tại trước khi dừng; mọi collision phải fail trước stop.
2. Trước khi staging hoặc dừng daemon, tính SHA-256 candidate và so sánh không phân biệt hoa/thường với hash đã duyệt `940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45`; khác một ký tự cũng phải dừng. Dùng read-only WAL-aware inspection trên live DB, yêu cầu `quick_check=ok` và schema bằng `ExpectedLiveSchema`; mismatch phải fail trước `StopLive`. Sau đó mới chép candidate sang file staged cùng directory và xác minh staged hash cũng bằng hash đã duyệt. Không gọi `Stop.bat` trong runbook unattended vì file đó kết thúc bằng `pause >nul`. Đặt process environment `AGENTDC_HOME=D:\TuvanZalo\data`, `AGENTDC_PORT=8770`, gọi chính xác `D:\TuvanZalo\app\agentdc.exe daemon stop --force`, rồi yêu cầu native exit code bằng 0.
3. Xác nhận process `agentdc.exe` có executable path đúng `D:\TuvanZalo\app\agentdc.exe` đã kết thúc. Nếu còn process sau timeout, không chép data và không thay binary.
4. Chỉ sau khi xác nhận daemon đã dừng mới tạo thư mục backup có timestamp.
5. Chép live `agentdc.exe` và **toàn bộ** `D:\TuvanZalo\data` vào backup; ghi hash binary cũ và đường dẫn bản sao.
6. Xác nhận `backup\data\agentdc.db` tồn tại, chụp baseline size/hash/mtime của DB/WAL/SHM, mở **bản backup** bằng SQLite URI `mode=ro&immutable=1`, chạy `PRAGMA quick_check` và đọc `app_meta.schema_version`. Chỉ tiếp tục khi `quick_check=ok`, schema bằng chính `ExpectedLiveSchema` đã qua preflight (3 trong rollout lịch sử) và baseline trước/sau giống tuyệt đối; không chạy migration hay câu lệnh ghi trên bản backup.
7. Sau khi mọi bằng chứng backup đạt, xác minh lại staged candidate hash và ba recovery path còn lại vẫn chưa tồn tại, đặt cờ `liveMutationAttempted` **ngay trước** thao tác, rồi dùng `[IO.File]::Replace(staged, live, replaced-old)` với **backup path không rỗng trong cùng directory**. Runtime trên máy này từ chối `$null`. Đọc lại hash live binary và hash automatic replace backup; chỉ khởi động qua `wscript.exe D:\TuvanZalo\Start.vbs` khi chúng lần lượt bằng candidate hash và old hash.
8. Nếu `liveMutationAttempted` đã được đặt thì phải rollback bất kể `File.Replace` báo thành công hay lỗi: xác minh lại backup binary bằng hash cũ, chép nó vào rollback staging path cùng directory, xác minh staged rollback hash, rồi atomically `File.Replace(rollback-stage, live, failed-candidate)` với non-empty backup path nếu live destination còn tồn tại hoặc `File.Move` nếu bị mất. Chỉ khởi động binary cũ sau khi hash live đã khôi phục đúng. Nếu lỗi xảy ra trước mutation, tuyệt đối không thay binary và chỉ khởi động lại binary cũ khi stop đã làm nó dừng. Mọi lỗi rollback/restart phải terminating và fail-closed; giữ backup cùng staging evidence khi thất bại, rồi khôi phục process environment và `$ErrorActionPreference` trong `finally`.
9. Sau start, trong timeout hữu hạn phải đồng thời chứng minh installed hash, đúng executable path của process sở hữu đúng một listener loopback, `/status` HTTP 200 và live DB `quick_check=ok`/schema bằng `ExpectedTargetSchema` (4 trong rollout lịch sử). Bất kỳ nhánh nào thất bại đều đi qua rollback binary atomically bằng backup path cùng directory không rỗng, xác minh old hash, restart old executable và kiểm tra lại exact path/listener/HTTP trong timeout; backup timestamp luôn được giữ.

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
| `tests/build-app.Tests.ps1` | PASS — đúng 11/11 checkpoint top-level |
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

### Quality remediation: gate cơ học và isolation thật — 2026-08-10 12:54–13:00 ICT

Review sau đó phát hiện canary 8782 ở trên vẫn **không đạt isolation**: copied home nằm trong
`D:\TuvanZalo`, được chép nguyên từ backup nên ban đầu mang cả `daemon.lock`, live `token`,
`zalo-transport.pid`, `zalo-transport.log` và `zalo\credentials.json`. PID file không còn sau khi
daemon canary chạy, còn credentials vẫn còn trong home cũ; việc không cấu hình transport và không
quan sát outbound Zalo không đủ chứng minh process không thể tương tác với phiên/PID đã chép. Vì vậy
PASS cũ chỉ còn là bằng chứng migration/API; nó không phải bằng chứng canary cô lập an toàn.

Quality fix thêm runbook thực thi và regression suite:

- `scripts\Invoke-MemoryV2Canary.ps1` gọi module để đọc source bằng URI immutable, sao chép home ra
  ngoài live root, từ chối source là live root/app/data (chỉ nhận retained backup), xóa **chỉ trên bản copy** lock/token/transport PID/log/default credentials, loại
  toàn bộ `AGENTDC_ZALO_*`, `ZALO_*`, `AGENTDC_TOKEN` và `AGENTDC_URL` khỏi child environment, rồi
  tự chạy migration/J1–J6/clean stop và xuất manifest + transcript JSON đã lược bỏ bí mật.
- `scripts\Invoke-MemoryV2Deploy.ps1` bắt buộc validate manifest trước stop: version/PASS/freshness,
  canonical candidate path/hash, source/target schema đã khai báo, toàn bộ journeys/isolation,
  process/listener đã dừng và evidence hashes. Transcript đã hash cũng phải parse được, redacted,
  PASS/đúng port/freshness và đủ unique PASS event source/migration/J1–J6/stop/final. Thiếu hoặc sai một
  trường thì fail trước mutation.
- Backup DB được đọc `mode=ro&immutable=1`; DB/WAL/SHM size/hash/mtime trước/sau mỗi lượt đọc phải
  giống tuyệt đối. Sau start, script kiểm bounded exact process path, listener loopback, `/status`,
  installed hash và live DB. Mọi lỗi sau mutation chạy full same-directory atomic rollback với backup
  path không rỗng, verify old hash, restart old executable và bounded health, đồng thời giữ backup.

Lần safe-script đầu trên port 8783 dừng ở assertion J4 sau khi J1–J3 PASS vì response pin hợp lệ bỏ
field JSON `expires_at` khi nil; harness đã giả định field luôn tồn tại. Cleanup native stop đạt,
process/listener đều vắng, không phải lỗi sản phẩm và không chạm live. Evidence FAIL được giữ nguyên:

- Manifest `docs/evidence/memory-v2-safe-canary-20260810T1254ICT.json`, SHA-256
  `D48AAD92D574617C4DA0CD24FC28A1913BD6A98578E8D368939DF41E0DC3EB58`.
- Transcript cạnh manifest, SHA-256
  `D42CC1427B57963C6C9DEA64651E0C7ABFB21B2BF2A7910A5AC0F87AD1F833F7`.

Sau regression fix, một canary mới hoàn chỉnh chạy bằng script mới:

| Cổng safe remediation | Kết quả |
|---|---|
| Candidate | Canonical path `D:\TuvanZalo\_artifacts\memory-v2\app\agentdc.exe`; SHA-256 `940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45` |
| Copied home / port | `C:\Users\manva\AppData\Local\Temp\agentdc-memory-v2-safe-canary-20260810T1300ICT`, ngoài `D:\TuvanZalo`; port riêng 8784 |
| Pre-start isolation | Credential/token/PID/lock/log đã scrub trên copy; transport-related env đã clear; port ban đầu trống; không có Zalo transport PID/credentials sau chạy |
| Immutable migration | Source/copy DB SHA-256 trước start cùng bằng `68DCE8FCCF2491BE5D1CD7B4C8859ED241F21D5E03F5FDC894856CC4DFD01933`; schema 3/`quick_check=ok` → schema 4/`quick_check=ok`; stable V3 fields/digests bằng nhau |
| J1–J6 | Cả sáu giá trị trong manifest `true`: scope isolation/sync, approve, reject, restore+pin, lineage delete+message retention, unchanged revision reads |
| Final canary | Clean stop; không listener/process candidate; final DB schema 4, `quick_check=ok`, SHA-256 `EB03D28385845416BCA3654692F83A0862276098A882D4FB6AC1E65BB9A3B504` |
| Manifest | `docs/evidence/memory-v2-safe-canary-20260810T1300ICT.json`; SHA-256 `56607B6E793428120940CBF7BD2DCF558791D0FAAD95A554E9D0968D2859FD52` |
| Transcript | `docs/evidence/memory-v2-safe-canary-20260810T1300ICT.transcript.json`; SHA-256 `E0935E82D0685A2253EE332A6DF9F7FEE334850B7128FD3AB5CA62B2E99F45D4` |
| Live isolation | Live không stop/replace/restart: PID 8424, exact path, `/status` HTTP 200 và approved executable hash không đổi sau canary |

Safe remediation này sửa assurance/isolation và cơ học hóa mọi lần triển khai tương lai. Nó vẫn
không thể hồi tố trình tự 11:40/11:45 của deployment ban đầu thành pre-deploy compliant.

### Cổng cuối sau quality remediation

| Lệnh/cổng | Kết quả |
|---|---|
| `npm test --prefix appmode` | PASS — 89/89, fail 0 |
| `run-overlay-go-tests.ps1 -Package all` | PASS — toàn bộ package Go |
| `tests/build-app.Tests.ps1` | PASS — 10/10 checkpoint; executable Memory V2 gates parse và giữ đúng safety ordering |
| `tests/memory-v2-deployment.Tests.ps1` | PASS — 5/5 checkpoint: export/parse, outside-root scrub, cả bảy default closure giữ module-private helper, manifest fail-closed và full rollback/restart health |
| Parser/static safety | PASS — ba script/module PowerShell parse sạch; Python AST parse sạch; manifest → live-schema preflight → staging đều đứng trước `StopLive`; schema args bắt buộc; không có `File.Replace(..., $null)` |
| Evidence integrity/redaction | PASS — manifest/transcript hash đúng; manifest PASS qua validator; 17 giá trị secret live được so khớp cục bộ, không giá trị nào xuất hiện trong evidence |
| Live/canary isolation | PASS — live vẫn PID 8424, exact path/hash, HTTP 200, DB `quick_check=ok`/schema 4; port 8783/8784 đóng và không có process chạy từ candidate path |
| `git diff --check` | PASS |

### Rollout operator rewrite thành structured lesson — 2026-08-10 14:53–15:26 ICT

Candidate này chỉ đổi đường ghi khi người trực sửa câu trả lời: thay legacy `AddZaloMemory` bằng
`CreateAppLesson(store.AppLessonInput{...})`; không đổi schema live đang là 4. Binary được build từ
commit `10c1fc55797b0baec1197903ec7a3cd5e85737ef`, path
`D:\TuvanZalo\_artifacts\memory-v2-operator-lesson\app\agentdc.exe`, SHA-256
`5034A8BAB0A8E97CCB4695DE8B92A7AFAEA4F3628322C4AC93D5706CC5ED6C0B`.

Source V4 được giữ tại
`D:\TuvanZalo\_backups\memory-v2-operator-source-20260810T145303ICT`: binary SHA-256
`940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45`; DB
`data\agentdc.db` SHA-256 `220BBB32F45DEC344B60B7610F979990E0C94C02F9B3881EA807C7085A53D5C2`,
`quick_check=ok`, schema 4, immutable baseline SHA-256
`6DA05DD33A1C9A83911197DF6EE0EBC899A173B1140B49F7F4F39C0D44B40FA9`.

Canary V4→V4 đầu tiên đã PASS trước lần deploy thứ nhất và được giữ làm bằng chứng redacted:

- Manifest `docs/evidence/memory-v2-operator-canary-20260810T145941ICT.json`, SHA-256
  `4206FC6A0A79E0B49621DFAD13AC777A174E13E18BFCFDD545B50848321D81CC`.
- Transcript cạnh manifest, SHA-256
  `A49D6AF3717014B466A7A418856449160DA11C4BFD2DF6F9F2DE77C76A953E50`.
- Port 8785; J1–J6 và toàn bộ isolation flag `true`; process/listener dừng sạch; source DB không đổi;
  final canary DB SHA-256 `C9C0AD8FDA42A46D37F610D69088E4BE0319149B78BACB4DC8162D9C1E7B694F`;
  migration comparison SHA-256
  `FB43ABA57CFFF01C0A0E6CD6A4CBAEF6427DADCD981C0500743E1BCC73480CC4`.

Lần gọi `Invoke-MemoryV2Deploy.ps1` đầu tiên fail-closed **trước staging và trước stop live**: default
closure `InspectLive` không phân giải được private helper `Invoke-MemoryV2SQLiteTool` sau
`.GetNewClosure()`. Không có live mutation, không cần rollback; live cũ vẫn đúng path/hash, HTTP 200 và
DB schema 4. Regression tái hiện lỗi bằng module import bình thường, sau đó fix capture trực tiếp các
module-bound helper scriptblock cho cả bảy default closure. Fix ở commit
`bad6761d38981e888d91bdc3ecb48737f184b19e`; spec review và quality review đều PASS, 0/0/0 phát hiện,
rồi commit mới được push fast-forward. Không hand-copy binary.

Sau review, canary mới được tạo bằng chính tooling đã commit trên port riêng 8786:

| Cổng canary mới | Kết quả |
|---|---|
| Copied home | `C:\Users\manva\AppData\Local\Temp\agentdc-memory-v2-operator-canary-20260810T082228Z`, ngoài live root |
| Candidate/source | Candidate hash đúng approved hash; source DB byte-for-byte vẫn là `220BBB32F45DEC344B60B7610F979990E0C94C02F9B3881EA807C7085A53D5C2`, schema 4/`quick_check=ok` |
| Isolation/J1–J6 | Toàn bộ flag `true`; credential Zalo và transport runtime vắng; candidate process/listener dừng sạch; evidence không chứa literal secret đã kiểm tra |
| Final canary DB | SHA-256 `CB2A2A98069B4A4902848F83E4F817A84DFB9CFDB0226F91D7DE4FFC086F2FD2`, schema 4/`quick_check=ok` |
| Manifest | `docs/evidence/memory-v2-operator-canary-20260810T082228Z.json`; SHA-256 `14846044E36F59691F7D98E90DA63E1902F5A0234E40449E66194EED154DFF23` |
| Transcript | `docs/evidence/memory-v2-operator-canary-20260810T082228Z.transcript.json`; SHA-256 `C83F2531B328047A8168BD047F13152268F3871B4430F246F2930564B6EBCE74` |

Retry duy nhất qua committed `Invoke-MemoryV2Deploy.ps1` PASS. Backup deploy ở
`D:\TuvanZalo\_backups\memory-v2-20260810T082525Z-66547463`: `agentdc.exe` và
`file-replace-backup-agentdc.exe` đều có old hash
`940211E40306C29C0DCA1D234C1F884FD06E53D6655D2C1574A7ED64524AAC45`; backup DB SHA-256
`375EC7D74109400B3A79E574E1835B309F1C53FBCFBD419CE9B8A3114AC1BCEB`, immutable
`quick_check=ok`, schema 4, baseline stable, baseline SHA-256
`C14CBC9761912CCB1A5091105E757529A271AB286087842FE60D152A70B1FFF3`; WAL/SHM không tồn tại.

Post-deploy độc lập xác nhận live PID 6068, executable path đúng
`D:\TuvanZalo\app\agentdc.exe`, một listener loopback port 8770 thuộc chính PID đó, installed SHA-256
đúng candidate, `/status` HTTP 200 và live DB `quick_check=ok`/schema 4. `/`, `/zalo` cùng 15 asset
CSS/JS thực tế của shell/core/pages đều HTTP 200; bốn recovery path không còn collision.

Các cổng chạy lại sau deploy đều PASS: focused staged Go 4/4 (session seam và ba operator-lesson
contract), full staged Go theo vetted gate lịch sử với đúng bảy legacy asset assertion bị supersede, Portal Node
89/89, deployment 5/5 và package 10/10 ở lần chạy lịch sử đó. Contract hiện tại dùng đúng tám skip.
Không gửi tin/reaction/file Zalo thật và không dùng dữ liệu
khách hàng trong canary. Quan sát visual local cùng lượt Zalo thử nghiệm vẫn là phần chờ người vận hành,
không được đánh dấu PASS trong biên bản này.

### Phần còn chờ người vận hành

- [ ] Mở Portal local, xác nhận trực quan tab **Chung cho nhóm**, hai tab thành viên, badge đồng bộ và các nút Duyệt/Từ chối/Khôi phục/Ghim.
- [ ] Dùng tài khoản/liên hệ Zalo thử nghiệm để quan sát refresh ở lượt kế sau mutation và không refresh ở lượt không đổi tiếp theo.
- [ ] Xác nhận daemon resume đúng session của một thread thử nghiệm, mỗi lượt chỉ gọi runner một lần; không ghi nội dung khách hàng vào evidence.

## Real-service smoke: chưa chạy

Lần triển khai 2026-08-10 không gửi tin, reaction hay file tới bất kỳ tài khoản Zalo nào và không dùng dữ liệu khách hàng làm fixture. Smoke bằng lượt Zalo thật được để dành cho tài khoản thử nghiệm, có người vận hành giám sát; trạng thái transport hiện hữu không được dùng làm bằng chứng rằng sáu hành trình đã chạy.
