# Planning state

step: execute
current_topic: providers-multi-account
current_spec: .planning/specs/2026-08-07-providers-multi-account-design.md
current_plan: .planning/plans/2026-08-07-providers-multi-account.md
last_updated: 2026-08-07

## Đang làm: #3 Multi-account (spec+plan XONG, TẠM DỪNG chờ /execute phiên mới)

Sub-project #3/4. Mỗi provider subscription (Claude Code/OpenAI Codex) cho NHIỀU account, mỗi account
một phiên CLI độc lập (thư mục config app tự quản dưới `cfg.Dir/accounts/<kind>/<id>`, cô lập khỏi CLI
người mua tự dùng). Runtime round-robin theo lượt + cooldown (KHÔNG sticky-thread, KHÔNG quota thật —
CLI không phơi quota). Style-diversity giữa 2 CLI (trục provider) để #4.
- Spec `.planning/specs/2026-08-07-providers-multi-account-design.md` (đã duyệt; đã sửa taxonomy:
  0 account = credential = DỪNG chuỗi, KHÔNG rơi provider kế — `isFallbackEligible` chỉ cho
  network/timeout/rate_limit/upstream đi tiếp).
- Plan `.planning/plans/2026-08-07-providers-multi-account.md` (commit `6bc9632`), 10 task TDD.

### TRẠNG THÁI: T1–T7 + T5 XONG (backend #3 + amend #2); TẠM DỪNG trước T8 — chờ #2 build + đăng nhập

- **ĐÃ THỰC THI + review sạch** (subagent-driven, mỗi code task implementer + code-reviewer ✅; go-check full xanh sau mỗi task):
  - T1 store `llm_accounts` (schema v3) `5ed2f68`; T2 `accountSelector` `f068ba1` (+guard `0b4c796`);
    T3 env helpers `f039f52`; T4 `cliAdapter.accountEnv`+`spawn`(3-value)+`appLLMAdapters` `156f02f`
    (plan spawn-order fix `cd5ed0d`); T6 provider body `Accounts` `a40cac4`; T7 DELETE account endpoint
    `df9fa62` (+cross-provider guard `12268e9`); T5 amend #2 spec+plan account-dir-aware `c063d2c`.
  - `spawn` ĐÃ đổi chữ ký (thêm penalize) — caller duy nhất `Generate`. Canary/security giữ nguyên
    (config_dir KHÔNG lọt ra body; DELETE có auth + allowlist; không credential qua daemon).
  - **Cảnh báo Task 11 (#4)**: `envVarFor` khớp kind `claude_code` (gạch DƯỚI) — đúng hiện tại; nếu
    Task 11 đưa claude-code vào cliAdapter & đổi kind sang gạch NỐI phải sync `envVarFor` kẻo wiring no-op.
- **CÒN LẠI (đều cần môi trường ổn định + đăng nhập):**
  - **#2 connect** (đã amend account-dir-aware) — build state machine + endpoints (fakeRunner, login-free)
    rồi Task 5 capture-first login THẬT. #2 gọi `CreateLLMAccount` (#3 đã có) khi `connected`.
  - **#3 T8** frontend panel Kết nối — list account (từ T6 body) + Xoá (T7 endpoint) đã sẵn dữ liệu; nút
    "Thêm account" nối vào `startConnect(kind)` của #2 → nên build CÙNG lúc #2 frontend, tránh nửa vời.
  - **#3 T9** capture-first (env var claude, login-vào-dir, email) + **T10** full `build-app` gate.
- Pre-flight ổn định môi trường: xem mục #2 dependency bên dưới.
- **Checkpoint NEEDS-LOGIN gộp #2 Task 5 + #3 Task 9**: cần môi trường ổn định (node v22, codex+claude
  cài lại & đăng nhập THẬT) để ghim env var (`CODEX_HOME` xác nhận, `CLAUDE_CONFIG_DIR` chờ verify),
  login-vào-dir-chỉ-định, và CLI có phơi email không. KHÔNG bịa output.
- Kiến trúc chốt: selector là **singleton cấp package** (base `api` git-clean cấm thêm field; adapter
  dựng mỗi lượt) + seam `cliAdapter.accountEnv`; wire trong `appLLMAdapters` (có `a.cfg.Dir`+`a.st`).
  `spawn` ĐỔI chữ ký (thêm `penalize`) → grep `.spawn(` trước khi build.
- Pre-flight ổn định môi trường: xem mục #2 bên dưới (giống hệt).

**Thứ tự execute trên nhánh: #3 T1–4 → #2 (đã amend) → #3 T6–10.** Ship cả nhánh một lần sau #4.

## Dependency tạm dừng: #2 Connect trong Portal (zero-terminal)

Sub-project #2/4. Fresh install chưa provider nào → bấm Connect mở luồng đăng nhập NGAY trong Portal
(không bắt khách mở terminal): dò CLI → cài nếu thiếu → login CLI → poll → Connected. UI#1 XONG @ b44a010;
engine XONG. #3 dựng trên cơ chế connect này.

### TRẠNG THÁI: TẠM DỪNG — plan #2 đã commit (`84babf9`), chờ `/execute` phiên mới

Pre-flight trước khi `/execute` (môi trường đang trôi — lý do tạm dừng):
- **Ổn định môi trường trước.** Node đã nâng v20→v22 (test scripts đã sửa; xác nhận `node -v`=v22).
  Cài lại + ĐĂNG NHẬP THẬT: `codex` (codex.js dịch chỗ sau nâng node → engine resolve qua `npm root -g`,
  KHÔNG hardcode) và `claude` (đang logged-out; `claude auth status --json` thoát 1 + JSON). Không xác thực,
  Task 5 sẽ kẹt.
- **Tasks 1–4 + 6 KHÔNG cần CLI thật** (fakeRunner + mock request) — chạy được ngay. **Task 5** (connectRunner
  thật: install/login) + **Task 7** (bundle npm + full `build-app.ps1`) cần môi trường ổn định.
- **Task 5 = checkpoint NEEDS-LOGIN** (capture-first như engine Task 8): ghim lệnh install/login + `loginUrl`
  parse khi anh đăng nhập THẬT. KHÔNG bịa output CLI.
- Baseline xanh trước khi bắt đầu: `pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO`
  + `npm --prefix appmode test`. (env vars không truyền xuống shell con — nạp ở đầu mỗi shell qua
  `[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User')`.)

## Đã ship

- **Provider routing** — merge vào `main` ở `40cd198`. Bốn API Provider với chuỗi
  fallback toàn cục, khoá mã hoá DPAPI, lượt có tệp đi thẳng Claude Code, cổng quét
  credential trong gói. Chưa push lên `origin`.

## Trên nhánh, chưa ship — ship MỘT LẦN ở cuối (`feat/cli-subscription-providers`)

Quyết định 2026-08-07: KHÔNG ship engine riêng; hoàn thiện luôn UI kiểu 9Router trên nhánh này rồi `/ship` một lần.

- **CLI subscription ENGINE** — XONG @ `5c2bb39`, final whole-branch review CLEAN → SHIP.
  Claude + ChatGPT là mắt xích subscription (codex qua `cliAdapter`, Claude qua `runClaude`);
  Gemini BỎ (Google khai tử login cá nhân → Antigravity), adapter dormant; codex cô lập khỏi
  config máy khách (`2a8902a`). 4 tính chất an ninh có test ghim. **Task 11** (gộp Claude vào
  cliAdapter) hoãn sang #4 Combos. Xem [[project-zalobot-subscription-engine]].

- **Providers UI kiểu 9Router** — 4 sub-project, cùng nhánh, mỗi cái spec→plan→execute riêng.
  9Router tách **Providers** (gallery + connections/models) và **Combos** (Fallback/RR/Fusion/
  Capacity). Điểm bán hơn 9Router: badge XANH "chính chủ, không rủi ro khoá" thay Risk Notice đỏ.
  - **#1 gallery + trang chi tiết — XONG** (7 task TDD, mỗi task 2 vòng review; final whole-branch review
    SHIP). `providers.js` 531→295, gallery+detail kiểu 9Router, badge xanh, search, Test-all thật, thao
    tác ghi inert (deferred #2/#4), canary bảo mật nguyên. Commits 791c203…f96cdb1.
    - Kèm 2 sửa hạ tầng do Node v20→v22: `appmode/package.json` + `AgentDC/tuvan-zalo/package.json`
      (`node --test <dir>` → glob/no-arg); 1 sửa engine `ecf8bf9` (probeClaudeAuth đọc JSON exit≠0 →
      phát hiện claude logged-out đúng thay vì unknown).
    - Build gate GREEN: go tests, Portal 100/100, tuvan-zalo 3/3, gói 1209 tệp/110MB, cổng credential pass.
    - Follow-up nhỏ (không chặn): unit-test seam cho probeClaudeAuth exit≠0+body; copy "0 kết nối"→"0 model";
      note logoColor nếu catalog thành backend-driven.
    - Nhánh hiện = **engine + UI#1** (đều shippable). CÒN #2 Connect, #3 Multi-account, #4 Combos theo
      ý "hoàn thiện cả nhánh". Quyết định: tiếp #2 (`/discuss`) hay `/ship` sớm engine+UI#1. **CHỜ user.**
  - #2 Connect trong Portal (zero-terminal: install-on-demand + device-auth login, 1 tài khoản).
  - #3 Multi-account (nhiều tài khoản/provider, tách `CODEX_HOME`/`CLAUDE_CONFIG_DIR`, Round Robin + Sticky).
  - #4 Combos (Fallback = chuỗi fallback hiện tại; + Round Robin/Fusion/Capacity) — THAY trang Models;
    gộp luôn Task 11 (Claude vào cliAdapter). Bot trả lời bằng một Combo được chỉ định.
