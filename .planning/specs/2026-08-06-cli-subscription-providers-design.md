# Provider thuê bao qua CLI, điều khiển hoàn toàn từ Portal

**Ngày:** 2026-08-06

**Trạng thái:** Chờ người dùng duyệt

**Nền:** provider routing, đã merge vào `main` ở `40cd198`

## 1. Mục tiêu

Biến gói thuê bao mà người vận hành đã trả tiền — Claude, ChatGPT, Google AI — thành mắt xích
trong chuỗi fallback đang có, để mỗi lượt trả lời tiêu hạn mức thuê bao thay vì tính tiền theo
token qua API key.

Ràng buộc cứng, và là lý do việc này không chỉ là "thêm một loại Provider": **người mua là người
vận hành bot Zalo, không phải lập trình viên.** Họ không bao giờ được bảo mở terminal. Máy mới thì
chưa nối Provider nào; bấm Bật một cái là Portal tự lo từ cài đặt tới đăng nhập.

## 2. Ngoài phạm vi

### 2.1 Cách gọi API kiểu 9Router — cố ý không làm

Sau khi lấy được token, 9Router gọi thẳng API riêng của hãng và **giả header của app chính chủ**:
`User-Agent: claude-cli/2.1.92`, `apiClient: "google-genai-sdk/..."`, `clientSecret` moi từ binary
của hãng, endpoint `v1internal` không tài liệu, và `cloakToolsOnOAuth` để giấu định nghĩa tool. Với
server hãng, request trông như đến từ app thật.

Chính 9Router đánh dấu cả `claude.js` lẫn `gemini-cli.js` là `deprecated: true` kèm
`deprecationNotice: "RISK_NOTICE"`, và UI của nó hiện banner *"This provider uses a subscription/
OAuth session not officially licensed for proxy/router use. Account may be restricted or banned."*

Phân biệt hai tầng cho rõ, vì dễ lẫn:
- **Đăng nhập** (màn hình OAuth "Claude Code would like to connect… Authorize") là **hợp lệ** —
  người dùng tự đồng ý, dùng đúng OAuth client của hãng. Cả hai cách đều làm được y hệt.
- **Gọi model sau đăng nhập** mới là chỗ khác. 9Router giả app để gọi API — đó là cái hãng dò ra
  và khoá. Thiết kế này gọi model bằng cách **chạy đúng app hãng phát hành**, không có gì để giả.

Sản phẩm này bán cho bên thứ ba; rủi ro khoá tài khoản rơi vào người mua, người không hiểu mình
đang chạy gì. Vì thế chọn đường chạy CLI thật. Đây là quyết định đã chốt với người dùng.

### 2.2 Gác sang spec sau

- **Nhiều tài khoản mỗi hãng** (multi-account). Quay lại ở spec riêng vì Round Robin cần nó.
- **Combos** (Capacity auto-switch / Round Robin / Fusion) trên chuỗi Models. Spec riêng, sau
  multi-account.
- Theo dõi hạn mức còn lại của gói (không hãng nào phát API công khai cho việc đó).
- Đổi Provider theo cuộc hội thoại hoặc theo agent.

## 3. Cơ chế được chọn

Portal **lái chính CLI của hãng**, cho cả đăng nhập lẫn suy luận.

Đăng nhập: Portal spawn lệnh login của CLI; CLI mở trang OAuth thật của hãng (hoặc phát mã device);
người dùng xác thực trực tiếp với hãng; CLI cất token vào kho riêng của nó. **Portal không bao giờ
thấy, không lưu, không chạm vào credential.** Suy luận: Portal spawn CLI ở chế độ không tương tác.

Vì mọi request đều do chính client của hãng phát ra, không có gì để giả dạng — ta *đúng là* client
đó. OpenAI ghi thẳng trong màn hình Codex "your ChatGPT rate limits apply", tức họ biết và chấp nhận
Codex tiêu gói thuê bao.

## 4. Interface thật, đo trên máy này

| | `claude` 2.1.223 | `codex` 0.146.1 | `gemini` 0.54.0 |
|---|---|---|---|
| Không tương tác | `-p` (đang dùng) | `codex exec` | `-p` |
| Model | qua config | `-m` | `-m` |
| Chỉ đọc | cấu hình sẵn | `-s read-only` | `--approval-mode plan` |
| Output cấu trúc | | `--json` (JSONL) | `-o json` |
| Đăng nhập | `auth login --claudeai` | `codex login`, `--device-auth` | không có lệnh riêng |
| Đọc trạng thái | `auth status --json` → JSON | `login status` → exit code | **không có** |

`--claudeai` chọn đúng đường thuê bao, phân biệt với `--console` (tính tiền API). `--device-auth`
của Codex cho phép Portal hiện URL + mã thay vì phụ thuộc trình duyệt mở đúng máy — quan trọng vì
Portal có thể được mở từ máy khác.

**Ba adapter khác nhau về bản chất ở khâu dò trạng thái, không chỉ khác cờ.** Claude trả JSON
(`{"loggedIn":true,"subscriptionType":"team",...}`). Codex trả exit code. Gemini không có gì.

Đo lúc viết spec: chưa đăng nhập codex/gemini, nên định dạng output của `codex exec --json` và vị
trí file credential của Gemini còn là câu hỏi mở (§14). Không viết adapter cho một CLI chưa chạy
được — plan phải xác minh trên CLI thật trước khi khoá định dạng.

## 5. Kiến trúc

### 5.1 `local_cli` là một `providerAdapter`

Cài đặt `providerAdapter{Generate, Test, Discover}` bằng spawn tiến trình, không HTTP.

- `Generate` — spawn CLI, prompt qua stdin, đọc stdout, ánh xạ về `llmResponse{Text}`.
- `Test` — đọc trạng thái đăng nhập, không tốn lượt gọi.
- `Discover` — trả danh sách model tĩnh theo hãng. CLI không liệt kê model như API.

Router không đổi. Nó vốn chỉ biết `providerAdapter`; timeout, huỷ, fallback, telemetry dùng lại
nguyên vẹn. Context cancellation ánh xạ sang kill cây tiến trình — mẫu này đã có sẵn trong
`stopZaloTransport`.

### 5.2 Claude Code nhập vào cùng họ

Claude Code hôm nay đã là một CLI được spawn, nhưng bị hard-code thành nhánh riêng (`runClaude`,
`execZaloRunner`, một seam trong `duty.go`). Gộp nó vào `local_cli` **bớt** một trường hợp đặc biệt
chứ không thêm, và ăn khớp với quyết định ở §6 vốn đã bỏ vị thế đặc biệt của nó.

Đây là phần rủi ro nhất của thiết kế: nó động vào seam `duty.go` vừa ổn định và vừa suýt rò đường
dẫn tệp của khách. Kế hoạch triển khai phải giữ test canary đính kèm xanh xuyên suốt. Nếu phải
chọn, ưu tiên giữ hành vi cũ hơn là gộp cho đẹp.

### 5.3 Cài CLI theo yêu cầu

Bấm Bật một Provider chưa có CLI → Portal chạy `npm install -g <gói>` bằng `node.exe` đóng sẵn
trong gói, hiện tiến trình, rồi mới mở luồng đăng nhập.

Gói không phình thêm và CLI luôn bản mới. Đổi lại: lần đầu cần mạng và mất khoảng 30-60 giây, nên
UI phải nói rõ đang làm gì thay vì đứng im.

### 5.4 Dò trạng thái đăng nhập

Mỗi hãng một cách, đúng như §4 cho thấy:

- **Claude** — `auth status --json`, đọc `loggedIn` và `subscriptionType`.
- **Codex** — `codex login status`, phân biệt bằng exit code.
- **Gemini** — không có cơ chế. Dò sự tồn tại của file credential trong `~/.gemini`. **Đây là chi
  tiết nội bộ của hãng và có thể vỡ khi họ cập nhật.** Phải cô lập sau một hàm, có test ghim, và
  khi dò hỏng thì báo "không xác định được" chứ không báo "chưa đăng nhập" — đoán sai theo hướng đó
  sẽ đẩy người dùng vào vòng đăng nhập lại vô ích.

Chỉ dò khi mở trang và sau khi thay đổi, không polling. Cùng lý do đã cấm polling
`GET /llm/providers`: mỗi lần dò là một tiến trình được spawn.

## 6. Thay đổi bất biến: Claude Code không còn là lưới an toàn bắt buộc

Thiết kế hiện tại **bắt buộc** Claude Code là mắt xích cuối đang bật; `validateLLMRoute` từ chối
lưu chuỗi nào khác. Điều đó mâu thuẫn với "máy mới chưa nối Provider nào".

Quyết định: **chuỗi được phép rỗng**, và ràng buộc đổi từ "phải kết thúc bằng Claude Code" thành
"mắt xích cuối phải đang bật". Portal chặn ở màn hình onboarding: chưa nối Provider nào thì hiện
"Chọn một Provider để bắt đầu", và bot trả lời rõ là chưa sẵn sàng thay vì im lặng đánh rơi tin.

Đây là thay đổi có sức lan rộng nhất trong spec này. Nó chạm `validateLLMRoute`, `BootstrapClaudeRoute`,
bất biến khoá control trong `models.js`, và cả những test đã ghim hành vi cũ.

## 7. Ranh giới đính kèm được nới

Hôm nay mọi lượt có tệp đi thẳng Claude Code, không API Provider nào nhận đường dẫn lẫn nội dung,
và có test canary bảo vệ.

Quyết định: **lượt có tệp được đi qua bất kỳ `local_cli` nào**, vẫn tiếp tục bỏ qua bốn Provider
HTTP. Chuỗi dự phòng nhờ đó phủ được cả lượt có ảnh, thay vì hỏng khi Claude Code hết hạn mức.

Hệ quả phải nói với người mua: **ảnh của khách có thể tới OpenAI hoặc Google tuỳ lượt.** Portal
phải nêu điều này ở chỗ bật Provider, không giấu trong tài liệu. Test canary được sửa chứ không
xoá: nó vẫn phải chứng minh bốn Provider HTTP không bao giờ nhận đường dẫn hay nội dung tệp.

## 8. Quyền của CLI

Tất cả chạy chế độ chỉ-đọc, ngang với cấu hình Claude Code hiện tại: `-s read-only` cho Codex,
`--approval-mode plan` cho Gemini.

Lý do là bảo mật, không phải hiệu năng. Tin nhắn của khách Zalo là đầu vào không tin cậy đi thẳng
vào một agent lập trình có quyền ghi file và chạy shell. Để mặc định thì một câu khách gõ trở thành
chỉ thị chạy trên máy người bán. Test phải chứng minh `Generate` **không bao giờ** dựng argv có cờ
bỏ sandbox.

## 9. Phân loại lỗi

Không thêm loại mới. Chín `llmErrorKind` giữ nguyên, `isFallbackEligible` vẫn đúng bốn loại.

- Hết hạn mức thuê bao → `rate_limit`, được fallback. Đây là điểm mấu chốt của cả tính năng: hết
  hạn mức Claude thì rơi sang ChatGPT.
- Chưa đăng nhập → `credential`, dừng chuỗi. Người vận hành phải hành động; thử tiếp chỉ tốn thời
  gian.
- CLI chưa cài → `credential`. Cùng hình dạng: cần người can thiệp.
- Tiến trình bị huỷ → `canceled`, không ghi telemetry. Giữ nguyên hành vi hiện tại.

Phân biệt "hết hạn mức" với "chưa đăng nhập" từ output CLI là chỗ dễ sai nhất. Mỗi adapter phải có
test bảng cho từng dạng, và khi không phân biệt được thì mặc định về `rate_limit` — đoán sai theo
hướng đó chỉ tốn một lượt thử Provider sau, còn đoán sai hướng kia làm chết cả chuỗi.

## 10. Portal

Trang Providers thêm nhóm "Gói thuê bao" liệt kê ba hãng, mặc định **chưa kết nối**.

Bật một cái chạy qua các trạng thái, mỗi trạng thái hiện rõ: đang cài CLI → chờ đăng nhập (kèm URL
và mã nếu là device auth) → đã kết nối, hiện email và loại gói. Có nút Ngắt kết nối gọi lệnh logout
của CLI.

Trang Models không đổi. Nó chỉ thấy Provider + model, không quan tâm bên dưới là HTTP hay tiến
trình — đó là lợi ích của việc `local_cli` cài đặt cùng interface.

Học từ 9Router: **registry khai báo**. Mỗi hãng là *dữ liệu* — tên gói npm, tên binary, cờ, cách dò
trạng thái — không phải một nhánh code. 150+ Provider của 9Router ứng với một nhúm format vì nó
tách descriptor khỏi codec; ở đây ba hãng ứng với một adapter cộng ba descriptor. Đây là phần đáng
học nhất từ 9Router, tách khỏi phần giả-app mà mình không lấy.

## 11. Kiểm thử

- Bảng cờ dòng lệnh cho từng hãng: `Generate` dựng đúng argv, và **không bao giờ** có cờ bỏ
  sandbox.
- Ánh xạ lỗi: hết hạn mức, chưa đăng nhập, chưa cài, bị huỷ — từ output thật của từng CLI.
- Dò trạng thái: cả ba đường, gồm cả nhánh Gemini hỏng phải trả "không xác định".
- Huỷ: context bị huỷ thì cây tiến trình chết, không để lại mồ côi. Đây là bug Task 5 vừa sửa; test
  phải ghim để nó không quay lại theo đường khác.
- Canary đính kèm: bốn Provider HTTP vẫn không nhận đường dẫn hay nội dung tệp, kể cả khi
  `local_cli` được phép.
- Chuỗi rỗng: bot báo chưa sẵn sàng, không đánh rơi tin nhắn im lặng.
- Cổng gói: `Assert-NoProviderCredential` vẫn xanh — CLI không đưa credential vào gói, nhưng cổng
  phải chứng minh chứ không giả định.

## 12. Rủi ro

- **CLI đổi giao diện.** Cả ba đang phát triển nhanh (Codex vừa báo có bản mới ngay khi cài). Cô
  lập trong descriptor, ghim bằng test đọc `--help` thật, và hỏng thì phải hỏng ồn ào.
- **Dò trạng thái Gemini dựa vào chi tiết nội bộ.** Rủi ro cao nhất trong spec. Cô lập, ghim test,
  và fail về "không xác định".
- **Gộp Claude Code động vào seam vừa ổn định.** Giữ toàn bộ test hiện có xanh xuyên suốt.
- **Chi phí spawn mỗi lượt.** Chưa đo. Nếu quá chậm thì tính tiến trình thường trú — nhưng chỉ sau
  khi có số, và nó kéo theo quản vòng đời process.
- **Điều khoản dịch vụ.** Chạy CLI chính chủ ở chế độ không tương tác là dùng đúng công cụ hãng
  phát hành — bỏ đi cái vector khoá-vì-phát-hiện-client-giả. Nhưng dùng gói *cá nhân* để chạy bot
  thương mại phục vụ khách thứ ba là vùng xám điều khoản, đúng với mọi cách. Nêu trong README của
  gói; đây là quyết định kinh doanh của người mua.

## 13. Tiêu chí nghiệm thu

- Máy mới, chưa cài CLI nào: bấm Bật một Provider → cài xong, đăng nhập xong, trả lời được, không
  một lần mở terminal.
- Hết hạn mức Provider đầu → chuỗi rơi sang Provider sau, telemetry ghi đúng một lần chuyển.
- Chưa đăng nhập → chuỗi dừng, Portal nêu đúng Provider nào và cần làm gì.
- Lượt có ảnh đi qua `local_cli` được, nhưng bốn Provider HTTP vẫn không nhận đường dẫn hay nội
  dung.
- Không lượt nào chạy CLI ngoài chế độ chỉ-đọc.
- Huỷ lượt không để lại tiến trình mồ côi.
- Gói vẫn qua cổng quét credential và toàn bộ test hồi quy.

## 14. Câu hỏi còn mở, plan phải đóng trước khi khoá định dạng

- Chi phí khởi động thật của mỗi CLI chưa đo. Nếu vượt ngân sách 25 giây mỗi Provider thì §5.1 phải
  xét lại.
- `codex exec --json` trả JSONL sự kiện; cần xác định sự kiện nào mang câu trả lời cuối. Chưa chạy
  vì chưa đăng nhập.
- Gemini ghi credential ra file nào khi đăng nhập — chỉ biết được sau lần đăng nhập đầu.
- Ba lệnh logout chưa kiểm.
- Cách phân biệt "hết hạn mức" với "chưa đăng nhập" từ output thật của Codex và Gemini — chưa xác
  minh được cho tới khi đăng nhập và chạy hết hạn mức thật.

## 15. Việc nối tiếp sau spec này

Hai spec riêng, mỗi cái một vòng `/discuss`, theo thứ tự:

1. **Multi-account** — nhiều tài khoản mỗi Provider, nhân hạn mức thuê bao. Là tiền đề cho Round
   Robin.
2. **Combos** — chuỗi Models chọn được chiến lược: Capacity auto-switch (mở rộng §7), Round Robin
   (cần multi-account), Fusion (hỏi song song + giám khảo). Người dùng đã chọn cả ba; ghi nhận ở
   đây để không rơi.
