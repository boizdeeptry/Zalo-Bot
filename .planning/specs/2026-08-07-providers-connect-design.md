# Connect trong Portal (zero-terminal) — design

> Sub-project **#2/4** của "Providers UI kiểu 9Router". Thứ tự: #1 gallery+detail (XONG) → **#2 connect (spec này)** → #3 multi-account → #4 combos. Cùng nhánh `feat/cli-subscription-providers`, ship một lần ở cuối.

## Goal

Biến nút "Add Connection" (hiện disabled ở trang chi tiết provider từ UI#1) thành luồng đăng nhập THẬT chạy hoàn toàn trong Portal cho **provider subscription** (Claude Code, OpenAI Codex): dò CLI → cài nếu thiếu → login (OAuth) → poll → Connected. **Người mua không bao giờ phải mở terminal.**

## AMENDMENT (từ #3, 2026-08-07): connect account-dir-aware ngay từ đầu

> Chốt khi thiết kế #3 multi-account (user duyệt gộp). #2 KHÔNG còn dùng thư mục config mặc định của máy; mọi login (kể cả account ĐẦU) sống trong thư mục app tự quản, cô lập. Nền #3 (bảng `llm_accounts`, `accountConfigDir`, `envVarFor`, `CreateLLMAccount`) ĐÃ có (commits `5ed2f68`…`df9fa62`). Khi thực thi #2, dựng theo các delta sau:

- **Connect job mang đích account** `{kind, accountId, configDir}` thay vì chỉ `kind`. `install` vẫn ở mức máy (cài CLI một lần); `login`/`pollAuth` chạy với env `<envVarFor(kind)>=<configDir>` (codex→`CODEX_HOME`, claude-code→`CLAUDE_CONFIG_DIR`) → phiên rơi vào đúng thư mục account, KHÔNG đè `~/.codex`/`~/.claude`.
- **`POST /llm/providers/{kind}/connect` nhận body `{label}`.** `connectManager` sinh `accountId` (short id), `configDir = accountConfigDir(a.cfg.Dir, kind, accountId)`, `os.MkdirAll(configDir, 0o700)` TRƯỚC khi chạy job.
- **Khi `connected`:** `EnsureProviderForKind(kind)` **rồi** `store.CreateLLMAccount(store.LLMAccount{ID: accountId, ProviderID: <id của kind>, Label: label, ConfigDir: configDir, Enabled: true, AddedAt: now})`. (Email best-effort nếu capture-first Task 9 xác nhận CLI phơi ra.)
- **`EnsureProviderForKind` vẫn giữ** — đảm bảo dòng provider-kind tồn tại trước khi gắn account.
- Nút "Connect" của #2 thêm ô **nhãn** (mặc định "Tài khoản 1"); một lần connect = tạo MỘT account. Nhiều account = bấm "Thêm account" nhiều lần (frontend #3 Task 8).
- Capture-first (#2 Task 5 gộp với #3 Task 9): xác nhận `codex login`/`claude` login **tôn trọng** `CODEX_HOME`/`CLAUDE_CONFIG_DIR` khi spawn nền (phiên rơi đúng dir), chốt tên biến claude, và CLI có phơi email không.

## Non-goals (để sub-project khác / execute-time)

- **KHÔNG** làm connect cho provider API-key (OpenAI/Anthropic/Gemini/OpenRouter — nút của nhóm đó vẫn disabled "sắp có"); đó là luồng form-dán-khoá khác hẳn, tách riêng.
- **KHÔNG** multi-account ở #2 (một lần connect = một account ĐẦU trong dir app-quản; xoay vòng/nhiều account là #3 — xem AMENDMENT).
- **KHÔNG** Round Robin / Sticky / Combos → #3/#4.
- **KHÔNG** ghim lệnh install/login CHÍNH XÁC trong spec: chúng là chi tiết execute-time, ghim **capture-first** khi CLI thật có mặt và anh đăng nhập thật (như Task 8 của engine). Spec cam kết KIẾN TRÚC + ranh giới, không phải cú pháp CLI.

## Bối cảnh (đã chốt trong /discuss)

- Engine + UI#1 xong. Provider subscription = Claude Code (`claude`, native exe) + OpenAI Codex (`codex`, node package). Gemini đã bỏ.
- Ràng buộc: gói chỉ ship `app\node\node.exe` → cần bundle npm để cài codex; claude cài qua installer chính chủ Windows. KHÔNG credential nào qua daemon (CLI tự giữ phiên). Giữ spawn CLI chính chủ (không proxy).
- Sự thật môi trường (execute-time sẽ xác minh lại): `claude` có `claude auth login` / `claude auth status --json` (thoát≠0 + JSON khi logged-out) / `claude setup-token` (token dài hạn cho subscription) / `claude install`; codex có `codex login` (đường codex.js đã dịch chỗ sau khi nâng node — resolve qua `npm root -g`, KHÔNG hardcode).

## Approach (đã chọn) + lý do

**Một "connect job" có trạng thái trong daemon + Portal poll.** Login phải chờ khách hoàn tất OAuth trong trình duyệt (30s–2 phút): một job stateful sống qua reload trang và cancel được; SSE phức tạp hơn không cần, blocking-request thì trói một request quá lâu. Portal poll `GET .../connect` mỗi ~1.5s.

Trải nghiệm login = **tự mở trình duyệt + hiện link dự phòng trong Portal + poll** (lựa chọn C): daemon spawn `<cli> login` (CLI tự mở trình duyệt mặc định), đồng thời bắt URL từ stdout đưa vào Portal làm nút "Mở trang đăng nhập" — sống cả khi auto-open hỏng (chạy như service / máy không browser mặc định).

Auto-install nằm TRONG luồng (lựa chọn A) để trọn "zero-terminal".

## Kiến trúc

### State machine (mỗi provider kind subscription)

```
detect → (install nếu thiếu) → login → poll auth → connected
                     ↓ lỗi           ↓ lỗi/timeout
                   error            error   |  (DELETE bất kỳ lúc nào) → canceled
```

Phase: `detecting | installing | awaiting_login | polling | connected | error | canceled`.
- **detect** — `resolveCLIProgram(descriptor)` (engine): CLI resolve được không (qua `npm root -g` cho codex, `exec.LookPath` cho claude).
- **install** (chỉ khi detect thiếu) — codex: bundled-npm `install -g @openai/codex`; claude: installer chính chủ Windows. Dòng tiến trình đẩy vào `message`. Lệnh THẬT ghim capture-first.
- **login** — spawn `<cli> login`; CLI tự mở trình duyệt; parse stdout lấy URL OAuth → `loginUrl`. Tiến trình sống chờ OAuth; timeout mặc định 5 phút.
- **poll** — `checkCLIAuth`/`Test` của engine tới khi `authLoggedIn`.
- **connected** — đảm bảo có provider row cho kind trong store (codex chưa có → tạo `kind=codex`; claude-code đã seed), rồi trả `connected`.

**Một job tại một thời điểm** (toàn cục): tránh mở nhiều trình duyệt / đua nhau. Job thứ hai khi đang có job → 409 (hoặc trả trạng thái job đang chạy).

### Đơn vị nhỏ (backend)

- `connectJob` struct: `{kind, phase, message, loginURL, err, cancel func()}` + guard mutex; giữ pid cây install/login để `killPidTree` khi cancel/timeout.
- `connectRunner` seam (như `cliAdapter.run` của engine): `install(kind) error`, `login(ctx, kind) (loginURL string, wait func() error)`, `pollAuth(kind) authState`. Mặc định = lệnh thật; test tiêm giả. Đây là điểm test toàn bộ state machine KHÔNG spawn CLI thật.
- `connectManager`: giữ job hiện tại (một), khởi động/đọc/huỷ; thread-safe.

### Endpoint mới (`app_routes.go`)

- `POST /llm/providers/{kind}/connect` — bắt đầu job cho kind subscription (chỉ `claude-code`|`codex`; kind khác → 400). Trả trạng thái đầu. Idempotent-ish: đang chạy job cùng kind → trả trạng thái đó; job khác đang chạy → 409.
- `GET /llm/providers/{kind}/connect` — poll `{phase, message, loginUrl?, error?}`.
- `DELETE /llm/providers/{kind}/connect` — huỷ job (killPidTree cả cây), phase → `canceled`.

### Frontend (`pages/providers.js`)

Trang chi tiết: nút "Thêm kết nối" (subscription) từ disabled → **thật**. Bấm → `POST .../connect` → mở panel connect + poll `GET .../connect` mỗi ~1.5s (dừng poll khi `connected|error|canceled` hoặc rời trang/dispose):
- `detecting` → "Đang kiểm tra…"
- `installing` → "Đang cài <CLI>… <message>"
- `awaiting_login` → "Đang chờ đăng nhập" + nút **"Mở trang đăng nhập"** (mở `loginUrl`) + copy + **Huỷ**
- `polling` → "Đang xác thực…"
- `connected` → "Đã kết nối ✓" → gọi `refresh()` (card + detail lật Connected)
- `error` → thông báo + nút "Thử lại"
Huỷ → `DELETE .../connect`. Poll gắn vào `AbortController` của trang (dispose huỷ poll).

### Store (`store/app_llm.go`)

Khi job vào `connected`, đảm bảo provider row cho kind tồn tại: `codex` chưa có → tạo (`kind=codex`, `system=false` nhưng KHÔNG credential — subscription qua CLI); `claude-code` đã seed → no-op. Hàm nhỏ `EnsureProviderForKind(kind)` idempotent.

## Data flow

`Connect` (click) → `POST /llm/providers/{kind}/connect` → job chạy nền (detect→install→login→poll→connected), cập nhật trạng thái trong bộ nhớ → Portal `GET` poll đọc trạng thái → khi `connected`, store có row → `refresh()` đọc `GET /llm/providers` thấy provider connected → gallery/detail xanh. Không token/khoá nào đi qua các endpoint này.

## Error handling

- detect: resolve lỗi ngoài "không thấy" (quyền, npm root lỗi) → `error` với message rõ, KHÔNG nhảy install mù.
- install fail (mạng/quyền/npm) → phase `error`, message gọn (KHÔNG lộ stderr thô — hợp đồng che của engine), nút Thử lại.
- login timeout (5 phút không logged-in) → `error` "hết giờ đăng nhập", killPidTree.
- poll: `authUnknown` kéo dài → coi như chưa xong, tiếp poll tới timeout; `authLoggedOut` sau khi login mà vẫn out → `error`.
- cancel giữa chừng → killPidTree cả cây (install hoặc login), phase `canceled`, dọn job.
- Endpoint: kind không phải subscription → 400; job khác đang chạy → 409; GET khi không có job → 404/empty.

## Testing

- **Backend** `daemon/app_llm_connect_test.go` (Go, table/subtests): tiêm `connectRunner` giả để chốt: (a) detect-có → bỏ qua install → login → poll → connected; (b) detect-thiếu → install → …; (c) install fail → error; (d) login timeout → error + runner.kill được gọi; (e) cancel giữa login → canceled + kill cả cây; (f) job thứ hai khi đang chạy → 409; (g) connected tạo provider row cho codex (idempotent). KHÔNG spawn CLI/trình duyệt thật.
- **Frontend** `appmode/tests/providers.test.mjs` (node --test): mock `request` trả từng phase → assert panel hiện đúng phase, `loginUrl` thành nút mở link, Huỷ gọi DELETE, `connected` gọi refresh và card lật Connected; poll dừng khi terminal.
- **Capture-first lúc execute**: lệnh install (codex npm, claude installer) + `<cli> login` THẬT + một lần anh đăng nhập thật, ghim `loginUrl` parse + cú pháp lệnh. KHÔNG bịa output.
- Cổng đóng: `npm --prefix appmode test` + `pwsh scripts/go-check.ps1` + full `build-app.ps1` xanh (gồm bundle npm mới trong `Assert-AppPackage`).

## File structure

- **Mới:** `appmode/overlay/internal/daemon/app_llm_connect.go` — `connectJob`, `connectRunner` (seam), `connectManager`, handlers `handleLLMConnect{Start,Status,Cancel}`.
- **Mới:** `appmode/overlay/internal/daemon/app_llm_connect_test.go` — state-machine tests.
- **Sửa:** `appmode/overlay/internal/daemon/app_routes.go` — đăng ký 3 endpoint (+ allowlist path đầu tệp).
- **Sửa:** `appmode/overlay/internal/store/app_llm.go` — `EnsureProviderForKind`.
- **Sửa:** `appmode/overlay/internal/webui/static/pages/providers.js` — panel Connect + poll + cancel; `portal.css` — style panel/phase/link.
- **Sửa:** `appmode/tests/providers.test.mjs` — test panel Connect.
- **Sửa:** `scripts/BuildApp.psm1` + `Assert-AppPackage` — bundle npm vào `app\node` (để cài codex), cập nhật cổng kiểm gói.

## Rủi ro / câu hỏi mở

- **Login spawn có cần TTY không** — nếu `codex login`/`claude auth login` đòi TTY khi spawn nền thì auto-open + parse-URL có thể trục trặc; giảm nhẹ bằng nhánh link/`setup-token`, chốt capture-first. **Rủi ro chính của #2.**
- **claude cài trên máy sạch** — `claude install` cần claude sẵn (vòng lặp); dùng installer chính chủ Windows (`install.ps1`) hoặc npm `@anthropic-ai/claude-code`; chốt capture-first.
- **Bundle npm** — kích thước + `Assert-AppPackage` phải chấp nhận cây npm; xác nhận cổng credential không báo nhầm trên node_modules của npm.
- **Môi trường đang trôi** — node vừa nâng v22 làm dịch chỗ codex; execute #2 nên chạy trên môi trường đã ổn định (CLI cài lại, đăng nhập lại) để capture-first cho đúng.
