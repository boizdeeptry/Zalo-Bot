# Planning state

step: execute
current_topic: providers-multi-account
current_spec: .planning/specs/2026-08-07-providers-multi-account-design.md
current_plan: .planning/plans/2026-08-07-providers-multi-account.md
last_updated: 2026-08-07

## Đang làm: #3 Multi-account + #2 Connect — code login-free XONG, DỪNG ở checkpoint login

Sub-project #3/4. Mỗi provider subscription (Claude Code/OpenAI Codex) cho NHIỀU account, mỗi account
một phiên CLI độc lập (thư mục config app tự quản dưới `cfg.Dir/accounts/<kind>/<id>`, cô lập khỏi CLI
người mua tự dùng). Runtime round-robin theo lượt + cooldown (KHÔNG sticky-thread, KHÔNG quota thật —
CLI không phơi quota). Style-diversity giữa 2 CLI (trục provider) để #4.
- Spec `.planning/specs/2026-08-07-providers-multi-account-design.md` (đã duyệt; đã sửa taxonomy:
  0 account = credential = DỪNG chuỗi, KHÔNG rơi provider kế — `isFallbackEligible` chỉ cho
  network/timeout/rate_limit/upstream đi tiếp).
- Plan `.planning/plans/2026-08-07-providers-multi-account.md` (commit `6bc9632`), 10 task TDD.

### TRẠNG THÁI: backend #3 (T1–T7) + #2 connect (T1–T4,T6) + #3 T8 + CSS XONG. CHỈ CÒN checkpoint login + build gate.

- **Backend #3 (T1–T7) + T5-amend** đã xong phiên trước: T1 `llm_accounts` `5ed2f68`; T2 `accountSelector`
  `f068ba1` (+guard `0b4c796`); T3 env helpers `f039f52`; T4 `cliAdapter.accountEnv`+`spawn`(3-value)
  `156f02f`; T6 provider body `Accounts` `a40cac4`; T7 DELETE account endpoint `df9fa62` (+guard `12268e9`);
  T5 amend #2 `c063d2c`.
- **#2 connect + #3 T8 + CSS — XONG phiên NÀY** (subagent-driven, mỗi code task implementer + code-reviewer ✅;
  go-check full xanh / Portal 107/107 sau mỗi task):
  - #2 T1 `EnsureProviderForKind` `14db56e`(+`cbadbce`); T2 state machine (account-dir-aware, happy)
    `7637da4`(+fix ctx-cancel/log/mkdir `78e58b7`); T3 branches install/error/timeout/cancel/one-job
    `9fd2e87`(+`8cbdddf`); T4 endpoints POST/GET/DELETE + routes + **minimal default runner** (detect thật;
    install/login/pollAuth = honest stub "chưa ghim") `fc2e7a6`(+singleton-refresh fix `8713b7e`).
  - #2 T6 frontend connect panel (poll + login link + Huỷ, `pollMs` injectable, `startConnect(kind)` tái dùng)
    `3f8d571`(+run-token fix `a23109d`).
  - #3 T8 frontend accounts list + Xoá + "Thêm account"→startConnect `5786d6d`(+`f9e118a`).
  - CSS connect panel + account rows (scoped `.providers-page`) `7af1443`.
- **QUYẾT ĐỊNH quan trọng phiên này — connect CODEX-ONLY.** Nút thắt tên kind Claude: seed `id=claude-code`
  **kind=`claude_code`** (gạch DƯỚI, app_schema.go:93); `cliDescriptors["claude-code"].kind="claude-code"`
  (gạch NỐI, cố ý, Task 11 mới hợp nhất); `envVarFor` khớp `claude_code`; `EnsureProviderForKind` map dùng
  `claude-code`. Ba nơi lệch cho Claude + Claude chưa qua `cliAdapter` → per-account chưa có tác dụng.
  Nên `subscriptionKinds`/`CONNECTABLE_KINDS` = **{codex}**; claude-code nút "Sắp có". Claude connect
  hoãn tới Task 11 (#4) hợp nhất kind. Codex nhất quán tuyệt đối trên chuỗi "codex".
- **Bất biến bảo mật giữ nguyên**: `connectState`/`llmProviderBody` KHÔNG mang `config_dir`/credential (test
  ghim); 3 route connect + DELETE account đều `a.auth`+allowlist; không credential qua daemon; canary sạch.
- **Kiến trúc mới phiên này**: `connectMgr` **singleton cấp package** (base `api` git-clean cấm thêm field —
  giống `accountSel`), **refresh MỖI `registerAppRoutes`** (không nil-guard) nên không rò singleton cũ giữa
  test. `connectRunner` seam: fake trong test, `defaultConnectRunner` thật (install/login/pollAuth chờ T5b).
  `accountId` = `uuid.NewString()` (dep có sẵn). Poll loop dùng **run-token** (không so kind) chống re-click/điều hướng.

- **CÒN LẠI — đều CẦN đăng nhập / môi trường (đây là bức tường):**
  - **#2 T5b + #3 T9 = CHECKPOINT NEEDS-LOGIN** (gộp): người dùng đăng nhập codex THẬT → ghim: lệnh install
    (`npm i -g @openai/codex` qua node bundle), lệnh login + parse `loginUrl`, xác nhận `CODEX_HOME=configDir`
    ĐƯỢC tôn trọng (phiên rơi đúng dir), `checkCLIAuth` codex-exit wire (hiện trả `authUnknown`), CLI có phơi
    email không. Rồi thay 3 stub trong `defaultConnectRunner` bằng impl thật. KHÔNG bịa output. `claude`
    connect vẫn hoãn (Task 11). `CLAUDE_CONFIG_DIR` chờ verify khi Claude vào cuộc.
  - **#2 T7 / #3 T10 = npm bundle + full build gate**: bundle node+npm vào `app\node` (BuildApp.psm1) để cài
    codex on-demand offline; `Assert-AppPackage` chấp nhận cây npm + canary sạch; full `build-app.ps1`
    (cần `ZALOBOT_PERSONA`); xác nhận thư mục `accounts/` KHÔNG bị đóng gói. Chạy CÙNG T5b để gate cả runner thật.
- **Cảnh báo Task 11 (#4)**: `envVarFor` + `subscriptionDisplayName` + `CONNECTABLE_KINDS` phải sync khi
  Task 11 hợp nhất kind Claude (`claude_code`→`claude-code`) & đưa Claude qua `cliAdapter`.

**Thứ tự còn lại: [phiên login] T5b+T9 (runner thật) → T7/T10 (bundle+gate) → ship cả nhánh (sau #4).**

## #2 Connect trong Portal (zero-terminal) — code XONG, chỉ còn runner thật (T5b)

Sub-project #2/4. Fresh install → bấm Connect mở luồng đăng nhập NGAY trong Portal: dò CLI → cài nếu thiếu
→ login CLI → poll → Connected. UI#1 XONG @ b44a010; engine XONG; plan `84babf9`. Code T1–T4+T6 + amend
account-dir-aware ĐÃ XONG phiên này (xem block trên). Còn T5b (runner thật, NEEDS-LOGIN) + T7 (bundle+gate).

### Pre-flight cho phiên login (T5b+T9, rồi T7/T10)
- **Ổn định môi trường + ĐĂNG NHẬP THẬT.** `node -v` = v22 (đã xác nhận). Cài lại + login `codex`
  (`npm i -g @openai/codex; codex login` — engine resolve codex.js qua `npm root -g`, KHÔNG hardcode).
  `claude` để sau (Task 11). Không đăng nhập → T5b kẹt.
- **Baseline xanh trước khi bắt đầu:** `$env:ZALOBOT_REPO=[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User'); pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO`
  + `npm --prefix appmode test` (hiện: go full xanh, Portal 107/107). Env vars KHÔNG truyền xuống shell con.
- **T5b = NEEDS-LOGIN** (capture-first như engine Task 8): thay 3 stub trong `defaultConnectRunner`
  (`app_llm_connect.go`) — `install`/`login`/`pollAuth` — bằng impl ghim theo output THẬT. Xác nhận
  `CODEX_HOME=configDir` được tôn trọng + `checkCLIAuth` codex-exit wire. KHÔNG bịa output.

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
