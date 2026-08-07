# Planning state

step: ship
current_topic: cli-subscription-providers
current_spec: .planning/specs/2026-08-06-cli-subscription-providers-design.md
current_plan: .planning/plans/2026-08-06-cli-subscription-providers.md
last_updated: 2026-08-07

## Đã ship

- **Provider routing** — merge vào `main` ở `40cd198`. Bốn API Provider với chuỗi
  fallback toàn cục, khoá mã hoá DPAPI, lượt có tệp đi thẳng Claude Code, cổng quét
  credential trong gói. Chưa push lên `origin`.

## Sẵn sàng ship (chạy `/ship`)

- **CLI subscription providers (engine)** — nhánh `feat/cli-subscription-providers` @ `5c2bb39`.
  Final whole-branch review **CLEAN → SHIP** (0 critical; 1 important dead-code `BootstrapClaudeRoute`
  đã gỡ ở `5c2bb39`; các suggestion còn lại gắn vào Task 11). Bốn tính chất an ninh đều có test ghim
  (argv read-only + `--` shield, kill cả cây, gói không chứa credential, canary tệp không chạm HTTP).
  - **Claude + ChatGPT** là mắt xích subscription của chuỗi fallback: codex qua `cliAdapter` mới;
    Claude qua `runClaude` (giữ nguyên). **Gemini BỎ** — Google khai tử login cá nhân của gemini-cli
    (2026-08, → Antigravity); adapter Gemini dormant. codex parser ghim theo capture THẬT (stdout ==
    `-o` file); codex-config cô lập khỏi config máy khách (`2a8902a`, `--ignore-user-config` + effort cap).
  - **Task 11 HOÃN** sang Combos (gộp Claude vào cliAdapter). **Task 12** verify matrix GREEN: go-check,
    Portal 126/126, build fixtures, full `build-app.ps1` (94.8 MB, nguồn AgentDC sạch trước/sau, cổng
    quét credential pass), acceptance `TestChainFallsFromHTTPToCLI` (HTTP 429 → codex).
  - Onboarding zero-terminal (bundling npm, install-on-demand, login-in-Portal, state machine
    disconnected→connected) = **plan follow-on riêng**, chưa làm.

## Xếp hàng, mỗi cái một vòng /discuss

1. **Multi-account** — nhiều tài khoản mỗi Provider. Tiền đề cho Round Robin.
2. **Combos** — chuỗi Models chọn chiến lược: Capacity auto-switch, Round Robin,
   Fusion. Người dùng đã chọn cả ba.
