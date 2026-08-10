# Planning state

step: ship
current_topic: no-default-claude-npm
current_spec: .planning/specs/2026-08-10-no-default-claude-npm-design.md
current_plan: .planning/plans/2026-08-10-no-default-claude-npm.md
last_updated: 2026-08-10

Nhánh `feat/no-default-claude-npm` off main `ccc3ac8`. Execute subagent-driven XONG — T1–T8 code + review sạch,
final whole-branch integration review bắt 1 lỗi thật (ErrZaloSilent rò qua runClaude → bot đã-cấu-hình nuốt
im thay vì leo thang) → fix `92b0e98`. Full build gate XANH 7/7, canary sạch, F:\dist\_verify-nodefault
(3177 tệp/121MB). Base AgentDC: 2 commit `3f224e9`(ErrZaloSilent)+`e05a13c`(zaloConfig.Program) ở lại AgentDC
history (như CC4). Overlay HEAD `b1ea16c`.
- **ND-T10 (E2E-found `b1ea16c`)**: combo model picker CHỈ hiện model của provider ĐÃ KẾT NỐI (trước đây lọc
  theo provider.enabled → hiện model Claude dù 0 account). Tách helper JS dùng chung `isProviderConnected`
  (core/providers-status.js) cho galleryStatus + picker + note "Chưa kết nối". Spec✅ quality-Pass, Portal 109 xanh.
  LƯU Ý: instance đang chạy (pid 20388) build từ `52bf07d` — CHƯA có fix này; cần rebuild để thấy live (hoãn
  tới rebuild trước-ship / sau-onboarding).
- **ND-T11 (E2E-found real bug, `406b7a3`) — codex proxy bị CHẶN bởi credential precheck**: router.go:178
  `appLLMCredential(codex)` trả lỗi "not found" (codex proxy KHÔNG có credential daemon — token ở auth.json)
  → lượt CHẾT trước adapter → mọi combo→codex escalate "the agent did not finish", llm_attempts RỖNG. Bug có
  SẴN trên main từ pivot PX5 (precheck chưa miễn cho proxy/CLI). Fix: `appLLMCredential` trả (nil,nil) khi
  ErrNotFound (provider không-credential đưa nil, adapter tự quyết); vẫn lỗi khi decrypt hỏng. Spec+quality
  Pass, live-verified DPAPI. **XÁC MINH E2E THẬT (2026-08-10)**: rebuild F:\dist\_verify-nd2 → swap app\ vào
  _verify-nodefault (giữ data) → gửi tin Zalo → llm_attempts = codex/gpt-5.5 **ok 4.7s**, bot trả lời (turn
  hoàn tất). "nothing to answer/ask back" là do KB rỗng + persona placeholder (quyết định nội dung, không phải
  routing). CODEX PROXY GIỜ CHẠY E2E QUA COMBO.
- **E2E phát hiện lỗ hổng hệ thống → user quyết làm ONBOARDING WIZARD** (option A: terminal-style TRONG Portal,
  gate app tới khi Provider→Combo→chat-thử xong) làm sub-project kế. Xem [[project-zalobot-onboarding-wizard]].
  "Connect xong vẫn im" gốc = chưa có combo active (routing đi qua Combo).
- **CÒN (checkpoint NEEDS-LOGIN, hành động USER trước /ship):** chạy F:\dist\_verify-nodefault → xác nhận fresh
  data IM + banner "chưa kết nối"; bấm Connect Claude (OAuth thật) → npm i -g @anthropic-ai/claude-code tự chạy
  → account hiện → consult trả lời qua bin\claude.exe (A-native, chạy thẳng PE, không hop node → không mồ côi).
- **Cơ chế chốt (T1 spike): A-native-direct-exe** — claude npm cài binary native thật vào
  `<npm root>\@anthropic-ai\claude-code\bin\claude.exe` (287MB, postinstall copy); resolveCLIProgram trỏ thẳng
  exe (prefixArgs=nil). KHÔNG dùng cli-wrapper.cjs (fallback --ignore-scripts, có hop+orphan). Xem [[reference-claude-code-npm-distribution]].

## Đang làm: No-default provider + Claude auto-install qua npm bundled (spec DUYỆT → /plan)

Nhánh dự kiến off `main` (`43859e4`). Spec `.planning/specs/2026-08-10-no-default-claude-npm-design.md` DUYỆT.
- **Phần 1** bỏ default: cổng "có provider chạy được không" ở overlay TRƯỚC lượt → chưa cấu hình = IM (đường
  như `ZaloManual`, KHÔNG escalate); `appZaloRunner`/`appClaudeRunner` im thay vì rơi `base`; bỏ seed combo
  mặc định; Claude mất đặc quyền mắt-xích-cuối.
- **Phần 2** Claude npm (cơ chế B = node+binJS, khớp codex): descriptor claude-code → `@anthropic-ai/claude-code`
  +`binJS`; `install()` cho phép claude (npm bundled → `data\cli`); base `execZaloRunner` nhận
  `zc.Program`/`ProgramPrefixArgs` **opt-in** (rỗng = LookPath cũ → standalone KHÔNG đổi = ràng buộc tương thích ngược).
- **Phần 3** Portal banner "chưa có provider — bot im lặng".
- **User chốt lúc /discuss**: KHÔNG default nào; chưa kết nối → IM + Portal cảnh báo (không handoff); Claude
  on-demand install (không pre-bundle); cơ chế B.
- **Capture-first (chặn sớm)**: tên npm + node entry thật của Claude Code; `node cli.js auth login`/`-p
  --add-dir`/stream-json/`CLAUDE_CONFIG_DIR` y hệt native (checkpoint như CC login); định vị seam dispatch
  cho cổng im. RỦI RO: nếu Claude Code không còn npm entry headless → B bất khả, quay lại pre-bundle/cài-tay.

## SHIPPED 2026-08-10 → main (`43859e4`) — Combos 9Router UI + Codex PROXY pivot + OAuth connect

Nhánh `feat/cli-models-combo-modal` (22 commit, fast-forward `4f48d94`→`43859e4` vào `main`) đã merge local,
nhánh xoá. Local main giờ ahead origin/main 144 commit — CHƯA push (ship local, không PR). Suite xanh trên
bản merged (fast-forward → HEAD y hệt commit đã test: Go PASS toàn package, Portal 100/100). Gói cuối:
F:\dist\_verify-20260808m (7/7 gate, canary "sach", 3177 tệp/121.0MB) đã swap vào live folder, daemon chạy,
account codex `a45380cc` sống qua swap.
- **CM1–CM5**: CLI static model populate (Claude Code + auto) · combo model-picker modal auto-save ·
  live codex model discovery (đọc `models_cache.json`, green-safe) · redesign Combos = card grid + create/edit
  modal kiểu 9Router (thêm model lúc tạo, toggle active, up/down, copy, delete-guard active/last).
- **PX1–PX8 (Codex PROXY pivot)**: THAY HẲN CLI codex bằng proxy gọi thẳng `chatgpt.com/backend-api/codex/
  responses` (Responses API, SSE→text) — token đọc từ auth.json per-account. Badge TRUNG THỰC: Codex (proxy)
  cảnh báo rủi ro khoá account (amber). Connect = browser-OAuth (authorization_code + PKCE S256, redirect
  `http://localhost:1455|1457/auth/callback`, account_id từ id_token) THAY device-auth — giống 9Router 1 bước.
- **2 fix cuối**: status "Đã kết nối" cho provider thuê bao xét THEO ACCOUNT (proxy không có credential); icon
  hãng chính thức (OpenAI/Claude/Gemini/OpenRouter, SVG trắng nhúng CSP-safe) thay text "CX/CC…".
- Orphan-dir cleanup (`54bb2ba`, session khác): xoá config dir account khi connect fail.
- E2E THẬT đã chạy: OAuth-connected account → proxy → "Xin chào" STATUS 200. Xem [[reference-zalobot-codex-proxy]].
- **CÒN (tùy chọn)**: Claude proxy (đối xứng codex) nếu muốn bỏ luôn CLI Claude. Chưa làm.

## (đã ship) CLI models hiện ra + combo model-picker modal (9Router-style)

Nhánh `feat/cli-models-combo-modal` off main `4f48d94`. User test bản mới thấy 2 lỗ hổng:
- **A**: Claude Code = 0 model. GỐC: claude-code KHÔNG có adapter (chạy runClaude), mà `handleLLMProviderDiscover`
  cần adapter → không lấy được model tĩnh (modelSeeds sonnet/opus/fable trong descriptor). Sửa: descriptor-discover
  cho claude-code + auto-populate model lúc startup (claude-code) + lúc connect (kind). Codex vốn discover được.
- **B**: combo cần **modal chọn model** như 9Router (search + click add/bớt, auto-save) thay vì dropdown từng dòng.
- Plan `.planning/plans/2026-08-08-cli-models-combo-modal.md` — 3 task (CM1 backend models, CM2 modal, CM3 CSS+gate).

## SHIPPED 2026-08-08 → main (`78d1980`) — Claude connect-in-Portal + multi-account

Nhánh `feat/claude-cliadapter` (CC1–CC6 + docs) fast-forward merge vào `main` (`8b92c57`→`78d1980`), nhánh
xoá. **Base AgentDC** có 1 commit riêng `b666721` (execZaloRunner nhận per-account CLAUDE_CONFIG_DIR) — ở lại
history AgentDC. Gói: F:\dist\_verify-20260808 (7/7 gate, 3177 tệp/120.8MB, binary có claude auth+pv-connect-hint).
- Reframe từ Task 11: KHÔNG gộp Claude vào cliAdapter (giữ runClaude agentic KB). Thêm: Claude connect-in-Portal
  (browser-OAuth, in URL, no device-code, label bằng email) + multi-account (accountSel chọn account claude-code,
  CLAUDE_CONFIG_DIR per-turn qua execZaloRunner).
- XÁC MINH THẬT: login (URL+exit0+email), CLAUDE_CONFIG_DIR cô lập, consult stream-json trong workdir mới +
  --add-dir → trả lời sạch KHÔNG kẹt trust-dialog (không cần seed hasTrustDialogAccepted). Final integration
  review Pass (0 Critical). Bước cuối = user bấm Connect trong app (hành động OAuth của họ).
- Local main giờ ahead origin/main — CHƯA push (ship local). Task 11 gốc coi như CLOSED bởi reframe này.

## Claude connect-in-Portal + multi-account — CC1–CC6 XONG, gate XANH → final review + ship

**Execute XONG (subagent-driven, mỗi task implementer + review + fix):**
CC1 kind unify `22162f1` · CC2 Claude browser-OAuth connect runner `ea53233` · CC3 frontend connectable
+ install-hint `2283615` · **CC4 BASE AgentDC** execZaloRunner per-account CLAUDE_CONFIG_DIR `b666721`
(base repo commit — AgentDC clean) · CC5 appClaudeRunner account selection `ef4aabf` · CC6 CSS `d5d28d3`
+ full build gate XANH (7/7, canary sạch, 3177 tệp/120.8MB, binary có `claude auth`+`pv-connect-hint`).
- Login flow XÁC MINH THẬT: `claude auth login` in URL + exit 0 + email; CLAUDE_CONFIG_DIR cô lập; `claude -p`
  với dir đó trả lời đúng account. Daemon connect runner mirror codex (đã review). E2E click-thật = bước user.
- **CÒN**: final integration review (overlay main..HEAD + base commit `b666721`) → /ship (merge main;
  base commit ở lại AgentDC history). Nhánh `feat/claude-cliadapter`.

## (cũ) Claude connect-in-Portal + multi-account — SPEC XONG, checkpoint LOGIN ĐÃ QUA → sẵn sàng /plan

Nhánh `feat/claude-cliadapter` (off main `8b92c57`). **Reframe từ "Task 11 gộp Claude vào cliAdapter"** —
Explore chứng minh gộp sẽ HỎNG KB/tool của Claude (adapter chung cho CLI stateless; Claude agentic đọc KB
qua `--add-dir` trong `execZaloRunner`). BỎ merge, giữ `runClaude`. Thay bằng:
- **Part 1 (overlay)**: Claude connect-in-Portal — `claude auth login` là browser-OAuth IM LẶNG (không device-auth,
  không URL/code ra pipe; mở browser + block tới khi exit 0). `CLAUDE_CONFIG_DIR` cô lập (đã xác nhận). Connect
  runner nhánh claude (no code UI, chờ exit-0), kind unify `claude_code`→`claude-code`.
- **Part 2 (BASE AgentDC repo)**: multi-account — `execZaloRunner` (duty.go) nhận per-turn `CLAUDE_CONFIG_DIR`;
  `appClaudeRunner` chọn account claude-code qua `accountSel`. Đây là thay đổi ĐẦU TIÊN vào base repo.
- Spec: `.planning/specs/2026-08-08-claude-connect-design.md`.
- **checkpoint LOGIN ĐÃ QUA (2026-08-08)** — login thật `CLAUDE_CONFIG_DIR=C:\Users\Admin\AppData\Local\
  zalo-claude-capture`, account `congnghe@midu.vn`. XÁC MINH: `claude auth login` IN URL ra stdout (Portal
  hiện được, KHÔNG silent như tưởng ban đầu) + mở browser + localhost callback + `Login successful.` exit 0;
  `auth status --json` flip loggedIn + **phơi email** (label account bằng email); `claude -p` với CLAUDE_CONFIG_DIR
  trả lời đúng account. Note: dir mới cảnh báo "workspace not trusted" (vẫn trả lời; runner thật dùng --add-dir).
  Dir đã-login này DÙNG LẠI được để test routing Part 2 lúc execute. → SẴN SÀNG `/plan`.
- **Lỗi cần nhớ**: ĐỪNG `Get-Process claude | Kill` — agent runtime LÀ claude, quét trúng cả session mình +
  các claude khác. Chỉ kill đúng PID mình spawn.

## SHIPPED 2026-08-08 → main (`5551c4b`)

Nhánh `feat/cli-subscription-providers` (89 commit: engine + UI#1 gallery + #2 connect + #3 multi-account
+ #4 combos) đã **fast-forward merge vào `main`** (`1741728`→`5551c4b`), suite xanh trên bản merged, nhánh
feature đã xoá. Local main giờ ahead origin/main — CHƯA push (ship local, không PR). Gói cuối:
F:\dist\_verify-20260808 (7/7 gate, 3177 tệp/120.8MB).

**CÒN LẠI (phiên riêng):** Task 11 (task #55) — gộp Claude vào cliAdapter + bật Claude connect. NEEDS-LOGIN
(verify CLAUDE_CONFIG_DIR với Claude login thật). Bắt đầu bằng `/discuss` hoặc branch mới off main.


## #4 Combos — XONG HẾT, final review CLEAN, gate XANH → SẴN SÀNG /ship (Task 11 hoãn)

**#4 execute XONG (subagent-driven, mỗi task implementer + spec review + code review + fix cycles):**
CB1 `1f1ca16` · CB2 `f513cd0` · CB3 `e45c12b` · CB4 `30233d4` · CB5 `922bcad` · CB6 `82c582d` ·
CB7 `96c6aa0`+`bf61491` · **RR-eligible-only fix `953a098`** (thay Task 11: RR xoay CHỈ member đủ điều
kiện — enabled + non-claude — nên claude-code luôn là lưới cuối, không route ~1/N lượt thẳng vào Claude).
- **Final whole-#4 integration review: SHIP IT** (0 Critical). Important #1 (migration v3→v4 không copy
  `llm_route_entries` cũ) = **greenfield, đã đóng**: nhánh chưa ship, mọi buyer nhận data dir mới ở v4;
  combo mặc định rỗng route Claude-direct y như route rỗng cũ. 4 Minor không chặn (dead `/llm/route`
  back-compat; `.models-page` class còn tên cũ; RR cursor không dọn khi xoá combo; check-then-insert đã ghi chú).
- **Final full build gate XANH**: 7/7, canary "sach", 3177 tệp/120.8MB, F:\dist\_verify-20260808.
- **Task 11 (CB8) HOÃN** sang phiên riêng (task #55): refactor seam tinh vi (budget bypass + stream-json +
  step rehome + kind claude_code→claude-code) + NEEDS-LOGIN (verify CLAUDE_CONFIG_DIR với Claude login thật)
  để bật Claude connect. Combos KHÔNG cần nó.

**→ Nhánh `feat/cli-subscription-providers` giờ = engine + UI#1 + #2 connect + #3 multi-account + #4 combos,
tất cả shippable. `/ship` MỘT LẦN. Sau ship: Task 11 phiên riêng.**

## (cũ) #4 Combos — CB1–CB7 XONG + SHIPPABLE (build gate XANH); CB8 (Task 11, hedged) đang thử

**#4 Combos execute (subagent-driven, mỗi task implementer + spec review + code review):**
- CB1 schema v4 `1f1ca16` · CB2 combo CRUD `f513cd0` · CB3 active-combo CAS + LLMRoute `e45c12b` ·
  CB4 router round-robin (rotate+comboRR) `30233d4` · CB5 combo HTTP endpoints (auth+allowlist) `922bcad` ·
  CB6 Portal Combos page (route-editor.js extract, thay Models) `82c582d` · CB7 CSS + scoping test
  `96c6aa0`+`bf61491`. Full build gate XANH (3177 tệp/120.8MB, canary sạch), Portal 97/97, go-check xanh.
- **CB8 = Task 11 (HEDGED, droppable)**: gộp Claude vào cliAdapter, hợp nhất kind claude_code→claude-code,
  claudeBudget path, bật Claude connect. Bỏ nếu phá seam/canary — Combos ship được KHÔNG cần CB8.
- Ghi chú CB4: RR xoay CẢ chuỗi (kể cả claude-code terminal) — spec đã duyệt vậy; CB8 làm claude thành
  member thường sẽ giải quyết tự nhiên. Nếu CB8 bỏ → thêm RR eligible-only (loại claude terminal khỏi anchor).

## (cũ) #2 Connect + #3 Multi-account — CODE XONG + build gate XANH; còn npm-bundle follow-up + E2E + #4

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
  test. `connectRunner` seam: fake trong test, `defaultConnectRunner` thật (install/login/pollAuth ghim ở T5b).
  `accountId` = `uuid.NewString()` (dep có sẵn). Poll loop dùng **run-token** (không so kind) chống re-click/điều hướng.

- **T5b runner THẬT XONG (capture-first từ codex-cli 0.147.0)**: `pollAuth`=`login status` exit-code (env
  `CODEX_HOME=configDir`); `login`=`login --device-auth` stream-parse URL `https://auth.openai.com/codex/device`
  + code một-lần (`scanDeviceAuth`, có test ghim); `install`=`npm i -g <descriptor.npmPackage>`. **`CODEX_HOME`
  cô lập XÁC NHẬN** (dir mặc định=logged-in, dir mới=logged-out). Email KHÔNG phơi qua status → account để trống.
  Commits `16aef56`+`9931835` (reap process/ dùng `envVarFor(kind)`/ test parser); `connectState`+`Code`;
  frontend hiện URL+code + CSS `bde8300`.
- **Full build gate XANH** (`build-app.ps1` + `ZALOBOT_PERSONA`): 7/7 phase, canary "sach: khong con dau
  khach hang nao", 1209 tệp/110.1MB (bằng baseline UI#1 — không phình). Lưu ý: base test
  `TestStreamThroughTheConfiguredServer` (server_timeouts_test.go) FLAKY do timing (pass khi chạy riêng +
  mọi lượt go-check phiên này) — KHÔNG phải regression từ thay đổi connect.

- **#45 npm-bundle (zero-Node) — XONG (2026-08-08, `b7a8ec5`)**: gói giờ bundle CẢ npm (npm/npm.cmd/npx/
  npx.cmd + `node_modules\npm`) vào `app\node` cạnh node.exe; run.bat set `npm_config_prefix=%ROOT%\data\cli`
  (npm i -g cài vào đó + `npm root -g` trả đúng đó → daemon tìm ra codex.js) + `npm_config_cache=%ROOT%\
  data\npm-cache` (npm không ghi ra ngoài goi → chạy được cả từ USB). **KHÔNG đổi logic Go** — `install()`/
  `resolveCLIProgram` thừa kế env launcher. Xác minh trên PATH=CHỈ `app\node` (chặt hơn run.bat prepend):
  npm bundled cài `@openai/codex` vào prefix mới, codex.js ở đúng binJS, node chạy codex-cli 0.147.0, cache
  nằm trong goi. Full build gate XANH, goi 3177 tệp/120.8MB (từ 1209/110.1 — +npm). Còn bước OAuth của user.
- **E2E ĐÃ CHẠY + SỬA XONG 2 lỗi ship-blocker** (gói cn-36df907f, daemon v0.10.0 :8770, data cô lập không
  Zalo-cred). Chạy end-to-end: gallery/detail/CSS, gating (claude-code "Sắp có" disabled, codex enabled),
  "+ Thêm kết nối"→prompt→"Bắt đầu"→POST connect → `accountId=uuid` + `MkdirAll` configDir cô lập
  (`data\accounts\codex\<uuid>\`) → daemon spawn `codex login --device-auth` với `CODEX_HOME`=dir đó (codex
  ghi `log/` vào ĐÚNG dir → **cô lập THẬT trong daemon, không chỉ trong test**).
  - **Lỗi 1 (backend):** codex ANSI-màu output (`\x1b[94m<code>\x1b[0m`, KỂ CẢ khi `NO_COLOR=1`) → `\b` đầu
    `connectCodeRe` không khớp (ký tự trước code là `m`) → `scanDeviceAuth` kẹt chờ code → timeout 45s. **KHÔNG
    phải PTY** — URL/ code ra pipe bình thường sau ~0.7s. Fix: strip SGR escape trong `scanDeviceAuth` (`2bed4e0`).
  - **Lỗi 2 (frontend):** panel chỉ hiện URL/code khi `awaiting_login`, nhưng state machine set chúng lúc
    chuyển sang `polling` → người dùng không bao giờ thấy mã. Fix: hiện khi có url/code bất kể phase (`82a8c76`).
  - **Sau fix — backend E2E XÁC NHẬN**: `GET .../connect` = `{phase:polling, loginUrl:"https://auth.openai.com/
    codex/device", code:"EUF8-2N6RT"}` (SẠCH, không còn ANSI). Bước cuối (user mở URL + nhập mã + authorize →
    connected → account row) là hành động OAuth của user; mọi tầng khác đã chạy thật.
  - **REBUILD+EMBED XONG (2026-08-08)**: `build-app.ps1` → `F:\dist\_verify-20260808` (7/7 phase, canary sạch,
    1209 tệp/110.1MB). Binary mới `app\agentdc.exe` (01:42, 20.4MB) đã `go:embed` `providers.js` bản fix —
    xác nhận bằng grep binary: `loginUrl?.startsWith`×1 + `pv-connect-code-value`×2. Chạy qua `Start.vbs`.
  - CÒN (tùy chọn): một lần bấm-thật để thấy "connected" — hành động OAuth của user qua Start.vbs.
  - **#4 Combos** (+ Task 11: gộp Claude vào `cliAdapter`, hợp nhất kind `claude_code`→`claude-code`). Khi làm
    PHẢI sync `envVarFor` + `subscriptionDisplayName` + `CONNECTABLE_KINDS` để bật Claude connect.
- **Ship CẢ NHÁNH một lần sau #4.**

**Thứ tự còn lại: #4 Combos (+ Task 11 gộp Claude) → ship cả nhánh. #45 npm-bundle XONG. Connect codex chạy
end-to-end kể cả trên máy zero-Node (còn mỗi bước OAuth của user).**

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
