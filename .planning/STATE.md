# Planning state

step: execute
current_topic: providers-connect
current_spec: .planning/specs/2026-08-07-providers-connect-design.md
current_plan: .planning/plans/2026-08-07-providers-connect.md
last_updated: 2026-08-07

## Đang làm: #2 Connect trong Portal (zero-terminal)

Sub-project #2/4 của Providers UI. Fresh install chưa provider nào → bấm Connect trên một provider
subscription (Claude Code/OpenAI Codex) mở luồng đăng nhập NGAY trong Portal (không bắt khách mở
terminal): dò CLI đã cài chưa → cài-theo-yêu-cầu nếu thiếu → chạy login CLI (device-auth/OAuth) →
poll auth status → lật sang Connected. Biến nút "Có ở bước Connect (#2)" (đang disabled ở UI#1) thành
luồng thật. UI#1 (gallery) XONG @ b44a010; engine XONG. Ship cả nhánh một lần sau khi đủ #2–#4.

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
