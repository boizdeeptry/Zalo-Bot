# Planning state

step: ship
current_topic: providers-gallery-ui
current_spec: .planning/specs/2026-08-07-providers-gallery-ui-design.md
current_plan: .planning/plans/2026-08-07-providers-gallery-ui.md
last_updated: 2026-08-07

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
