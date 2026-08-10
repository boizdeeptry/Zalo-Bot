# No-default provider + Claude auto-install qua npm bundled — design

**Ngày:** 2026-08-10
**Nhánh dự kiến:** off `main` (`43859e4`) — nhánh mới, ví dụ `feat/no-default-claude-npm`
**Trạng thái:** DRAFT chờ duyệt

---

## Vấn đề

Sau khi Codex chuyển sang PROXY (zero-install), chỉ còn **Claude** phụ thuộc CLI cài tay:

1. `claude-code` là native exe, resolve bằng `exec.LookPath("claude")` ở **hai** call site — connect/detect
   (`resolveCLIProgram`, overlay) và lượt tư vấn thật (`execZaloRunner.Run` → `LookPath(prof.Binary)`,
   **base repo** `internal/daemon/duty.go:1735`). Trên máy khách chưa cài → cả hai fail.
2. `install()` từ chối claude-code (`app_llm_connect.go:329`): "Claude Code chưa cài — cài tại claude.com…".
   App KHÔNG tự cài được (khác codex/gemini vốn npm-install offline vào `data\cli`).
3. **Default combo rỗng route thẳng Claude/`base`** (`app_llm_router.go:527`). Trên máy chưa cài Claude,
   một tin nhắn Zalo bất kỳ → `execZaloRunner` → `LookPath("claude")` fail → lỗi xấu (hoặc escalate/handoff).

## Quyết định của user (chốt trong lúc /discuss)

- **KHÔNG có default nào.** Máy khách mới không tự route đi đâu; người dùng phải tự Connect provider
  (Codex proxy hoặc Claude) rồi tạo/chọn combo mới chạy.
- **Chưa kết nối provider nào → bot IM LẶNG với người nhắn** (không lộ bot lỗi cấu hình) **+ Portal cảnh báo**
  cho admin biết cần setup. KHÔNG escalate/handoff, KHÔNG route về Claude.
- **Claude auto-install khi bấm Connect** (on-demand, giống Codex) — không pre-bundle (vì không còn default
  nào cần Claude có sẵn lúc boot).
- **Cơ chế resolve Claude = B (node+binJS)**, khớp Codex.

## Goals

1. Máy khách mới, chưa cấu hình gì → nhận tin Zalo → **im lặng**, Portal hiện cảnh báo "chưa có provider".
2. Bấm Connect Claude → app tự `npm i -g @anthropic-ai/claude-code` vào `data\cli` (npm bundled, offline) →
   login OAuth → account xuất hiện → combo dùng được Claude.
3. Claude là provider bình thường (như Codex): không đặc quyền "mắt xích cuối", chỉ chạy khi có account +
   được đưa vào combo active.
4. Giữ nguyên hành vi agentic của Claude (KB qua `--add-dir`, stream-json, `CLAUDE_CONFIG_DIR` per-account).

## Non-goals

- KHÔNG làm Claude proxy (giữ CLI agentic — lý do Task 11 gộp bị bỏ trước đây).
- KHÔNG pre-bundle binary Claude vào gói.
- KHÔNG đụng Gemini (dormant/bỏ).
- KHÔNG thêm câu thông báo gửi người nhắn khi chưa cấu hình (user chọn im lặng thuần).
- KHÔNG ô nhập câu-báo-tùy-admin (đã loại ở /discuss).

## Chọn cách tiếp cận

### Cơ chế resolve Claude: B (node+binJS) — ĐÃ CHỌN

| | A — LookPath shim | **B — node+binJS (CHỌN)** |
|---|---|---|
| Descriptor | giữ `nativeBin:"claude"` | `npmPackage`+`binJS`, bỏ `nativeBin` |
| Resolve | `LookPath("claude")` thấy `claude.cmd` npm | `node <cli.js>` như codex |
| Chạy | `claude.cmd` (batch) qua Go exec | `node.exe` (PE thật) |
| Base repo | không đụng | **đụng `execZaloRunner`** (đã có tiền lệ CC4) |
| Rủi ro | Go exec `.cmd` + quoting đối số Windows/Go mới (BatBadBut CVE-2024-24576) | không có bẫy `.cmd`; khớp đường codex đã kiểm chứng |

→ B bền hơn trên Windows và dùng lại đúng pattern codex đã chạy E2E. Đổi lại chạm base repo lần nữa —
user đã đồng ý.

### Ràng buộc then chốt: thay đổi base repo phải TƯƠNG THÍCH NGƯỢC

Base `AgentDC` được dùng cả standalone (agent tổng, không chỉ overlay Zalo). Mọi thay đổi base phải
**cộng thêm, opt-in**: field/nhánh mới, rỗng = hành vi cũ. Standalone không set field mới → chạy y như trước.

## Kiến trúc — 3 phần

### Phần 1 — Bỏ default, im lặng khi chưa cấu hình

**Đảo có chủ đích** nguyên tắc cũ (`app_llm_router.go:509-528`): "route rỗng → rơi về Claude, thà trả lời
còn hơn im". User chọn ngược: chưa cấu hình = im.

- **Cổng "có provider chạy được không"** đặt ở overlay, TRƯỚC khi chạy lượt (đi đường im như `ZaloManual`
  → `return nil`, KHÔNG escalate). "Chạy được" = có ≥1 provider connected **và** combo active có ≥1 member
  resolve ra provider connected.
- Cách phát tín hiệu im (chọn 1 lúc plan, ưu tiên #1):
  1. **Overlay skip trước answerZalo**: định vị seam dispatch Zalo của overlay; nếu không có provider chạy
     được → log + bỏ qua (không gọi answerZalo), set Portal status. KHÔNG chạm base. *(ưu tiên — cần định
     vị seam, capture-first)*
  2. **Sentinel ở base** (nếu #1 bất khả): thêm `var ErrZaloSilent` (base); `answerZalo` `errors.Is` →
     `return nil` trước nhánh escalate; runner im của overlay trả sentinel này. Cộng thêm, opt-in →
     standalone không đổi.
- `appZaloRunner`: route rỗng (`:527`) và đọc-route-hỏng (`:524`,`:532`) → **im**, KHÔNG `return base`.
  (Không còn base-claude hợp lệ để rơi về.)
- `appClaudeRunner`: 0 account claude → **im**, không rơi `base`. Claude chỉ chạy khi có account connected.
- **Bỏ seed combo mặc định** (CB1): Combos mở ra trống → "tạo combo đầu tiên". Claude mất đặc quyền
  "mắt xích cuối / net" trong RR → thành member thường, chỉ route khi user tự thêm vào combo.

### Phần 2 — Claude auto-install khi Connect (cơ chế B)

- **Descriptor** (`app_llm_cli.go`, `cliDescriptors["claude-code"]`): `nativeBin:"claude"` →
  `npmPackage:"@anthropic-ai/claude-code"` + `binJS:<verify>`. GIỮ nguyên `readOnlyArgs`, `bannedArgs`,
  `authMethod:"claude-json"`, `modelSeeds`, `claudeBudget`.
- `resolveCLIProgram` (`:586`): claude-code rơi vào nhánh npm sẵn có (`node <binJS>` qua `npm root -g`).
  Bỏ nhánh `nativeBin` cho claude → gần như tự động, ít sửa.
- `install()` (`app_llm_connect.go:326`): bỏ nhánh từ chối claude-code (`:329-331`); để nó rơi vào
  `npm install -g <pkg>` (pkg giờ non-rỗng). Dùng lại y hệt đường codex (npm bundled → `data\cli`).
- **Connect runner branches Claude** (`:507`,`:613`,`:637` — login `claude auth login`, pollAuth): đang gọi
  `resolveCLIProgram(cliDescriptors["claude-code"])` rồi spawn. Phải dùng `program`+`prefixArgs` trả về
  (giờ `prefixArgs=[binJS]`) → dựng `node <binJS> auth login …`. Verify các call site này đã nối `prefixArgs`
  (viết cho native, prefixArgs=nil) — nếu chưa, sửa để prepend.
- **Base `execZaloRunner.Run`** (`duty.go:1735`): đang `LookPath(prof.Binary)` rồi `exec.CommandContext(bin,
  args…)`. Cần chạy `node <binJS> <args>` cho Claude npm. Cách tương thích ngược:
  - Thêm field vào `zaloConfig` (base): `Program string` + `ProgramPrefixArgs []string`.
  - `execZaloRunner.Run`: nếu `cfg.Program != ""` → dùng nó + prefixArgs; else → `LookPath(prof.Binary)`
    (đường cũ, standalone không đổi).
  - Overlay `appClaudeRunner` set `zc.Program`,`zc.ProgramPrefixArgs` từ `resolveCLIProgram(claude-code)`
    (giống cách nó đã set `zc.ConfigDir` từ account).
- **Multi-account** giữ nguyên: install 1 lần (global `data\cli`), mỗi account riêng `CLAUDE_CONFIG_DIR`.

### Phần 3 — Portal cảnh báo "chưa có provider"

- Backend: một status (vd trong `llmProviderBody` tổng hoặc endpoint dashboard) — `providersConnected int` +
  `activeComboRunnable bool`.
- Frontend: banner/badge trên Providers/Combos/dashboard khi 0 provider connected hoặc combo active không
  chạy được: "Chưa kết nối provider — bot sẽ im lặng. Hãy Connect một provider."
- Đây là cảnh báo READ-ONLY, không đụng bất biến bảo mật (không phơi config_dir/credential).

## File dự kiến chạm

**Overlay (`Zalo-Bot/appmode/overlay/internal/daemon`):**
- `app_llm_cli.go` — descriptor claude-code → npm; `resolveCLIProgram` bỏ nhánh native.
- `app_llm_connect.go` — `install()` cho claude; connect runner dùng prefixArgs.
- `app_llm_router.go` — `appZaloRunner`/`appClaudeRunner` im thay vì base; bỏ seed default.
- `app_llm_api.go` (+ routes) — status "chưa có provider".
- Store: bỏ/không seed combo mặc định (xem CB1 migration).

**Overlay frontend (`…/webui/static`):**
- `pages/providers.js` hoặc dashboard — banner cảnh báo.
- `portal.css` — style banner.

**Base (`AgentDC/internal/daemon`):**
- `duty.go` — `zaloConfig.Program`/`ProgramPrefixArgs` (opt-in); `execZaloRunner.Run` dùng chúng;
  (nếu chọn sentinel) `ErrZaloSilent` + nhánh im trong `answerZalo`.

## Error handling

- Đọc route/DB hỏng → im + log error (không rơi Claude, không escalate). Sai về phía im, không về phía
  route-vào-thứ-không-tồn-tại.
- `install()` npm fail → surface qua state machine connect như install-failed (đã có đường cho codex).
- Login Claude fail/timeout → như hiện tại (connect state machine đã có nhánh).
- Base `execZaloRunner`: `cfg.Program` set nhưng `node`/binJS không thấy → lỗi rõ ("claude npm chưa cài");
  route sẽ đi mắt xích kế hoặc im (không crash).

## Testing

- **Phần 1 (router im):** unit test `appZaloRunner`/`appClaudeRunner` — 0 provider / 0 account / route rỗng
  → trả runner im (hoặc gate skip), KHÔNG phải base. Test seed: Combos mở ra 0 combo active.
- **Phần 2 (resolve/install):** test `resolveCLIProgram(claude-code)` trả `(node, [binJS])`; test `install()`
  dựng `npm install -g @anthropic-ai/claude-code`; test connect runner dựng `node <binJS> auth login …`
  (qua seam, không spawn thật). Base: test `execZaloRunner.Run` dùng `cfg.Program` khi set, else LookPath
  (bảo vệ tương thích ngược standalone).
- **Phần 3:** Portal test (node --test) — banner hiện khi 0 provider, ẩn khi có.
- **Canary + full gate:** `build-app.ps1` 7/7, canary "sach", Portal + Go + Zalo suite xanh.

## Capture-first (NEEDS-LOGIN / verify lúc execute, KHÔNG bịa)

1. Tên npm package thật + node entry (`binJS`) của Claude Code hiện tại (`@anthropic-ai/claude-code` →
   `cli.js`? — xác nhận `npm root -g` + đường .js).
2. `node <cli.js> auth login` in URL OAuth + exit 0 + phơi email; `node <cli.js> -p --add-dir <dir>`
   stream-json trả lời đúng; `CLAUDE_CONFIG_DIR` cô lập — **y hệt** `claude` native (checkpoint như CC login).
3. Định vị seam dispatch Zalo của overlay cho cổng "im" (Phần 1 cách #1); nếu bất khả → chốt sentinel base.
4. Verify connect runner call sites (`:507/:613/:637`) nối `prefixArgs` đúng.

## Rủi ro

- **Base repo touch lần 2** (sau CC4). Giảm thiểu bằng ràng buộc tương thích ngược (opt-in field).
- **Đảo nguyên tắc "thà trả lời còn hơn im"** — có chủ đích, user chọn. Ghi rõ để người đọc sau không tưởng
  là regression.
- Claude Code có thể đã đổi cách phân phối (native-only). Nếu npm package không còn/không có node entry
  chạy headless → B bất khả, phải quay lại pre-bundle hoặc giữ "cài tay". → checkpoint #1/#2 chặn sớm.
- `--add-dir`/trust-dialog trên dir account mới (đã gặp ở CC): runner thật dùng `--add-dir` nên OK, nhưng
  verify lại với đường node+binJS.

## Bất biến bảo mật (giữ nguyên)

Không credential qua daemon; Connect = hành động OAuth của user; canary "sach"; read-only args + banned args
của claude-code không đổi; base AgentDC commit-clean (thay đổi có chủ đích, tương thích ngược).
