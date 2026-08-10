# Provider thuê bao qua CLI — Research

**Researched:** 2026-08-06
**Domain:** Spawn-per-turn adapter cho ba CLI thuê bao (claude / codex / gemini) trong router `providerAdapter` sẵn có
**Confidence:** HIGH cho phần đo được trên máy này (spawn cost, flags, auth shape, seam code); MEDIUM/LOW cho phần cần đăng nhập codex/gemini (định dạng output khi có quota, file credential Gemini) — đã tách xuống `## Awaiting`.

Mọi số liệu dưới đây ĐO THẬT trên máy này 2026-08-06 (Windows 11, PowerShell 5.1 — không có pwsh 7). `claude` 2.1.223 đã đăng nhập (gói `team`); `codex` 0.146.1 và `gemini` 0.54.0 CHƯA đăng nhập.

---

## Summary

Kiến trúc "spawn mỗi lượt" của spec §5.1 **đứng vững**: lượt `claude -p` thật (có đăng nhập) đo được median 8.1s, max 10s — dưới xa ngân sách 25s/Provider. Node-boot của gemini ~2.8s, status của codex ~0.15s. Không có bất ngờ kiến trúc; **plan KHÔNG phải mở màn bằng spike tiến trình thường trú.**

Ba CLI đều có đủ cờ non-interactive, read-only, và model mà spec cần — đã xác minh trên `--help` của đúng bản đang cài, không đoán. Ba điểm khác spec: (1) read-only của claude KHÔNG phải `--permission-mode plan` mà là **whitelist `--allowed-tools`** (profile hiện tại cấm thêm permission-mode); (2) `npm` KHÔNG được đóng trong gói — chỉ có `node.exe` — nên §5.3 phải thêm việc bundle npm trước khi chạy được; (3) gọi CLI cài qua npm từ Go trên Windows phải đi thẳng `node.exe <cli>.js`, KHÔNG gọi qua shim `.ps1`/`.cmd`.

**Primary recommendation:** Viết một adapter `local_cli` duy nhất + ba descriptor dữ liệu (đúng bài học 9Router). Spawn bằng `node.exe <đường-dẫn-cli.js>` cho codex/gemini và `claude.exe` trực tiếp cho claude; huỷ lượt phải gọi `killPidTree` (không dựa `exec.CommandContext`). Đóng gói npm vào `app\node`. Giữ nguyên đường Claude Code KB-consult ngoài ngân sách 25s.

---

## 1. Spawn cost — ĐO TRƯỚC, và nó viable

| Lệnh | Điều kiện | min | median | max | Ý nghĩa |
|---|---|---|---|---|---|
| `claude -p "2+2"` | ĐÃ đăng nhập, lượt thuê bao thật | 7.53s | **8.10s** | 9.98s | Lượt cold-start thật, dưới 25s [VERIFIED: đo trên máy] |
| `claude auth status --json` | Test path | 0.64s | 0.66s | 0.68s | Dò trạng thái rất rẻ (native exe) |
| `codex login status` | Test path + node/rust boot | 0.13s | 0.14s | 0.22s | Rất nhanh (codex là binary Rust qua node shim) |
| `gemini -p "2+2"` | CHƯA đăng nhập, fast-fail exit 41 | 2.79s | 2.81s | 2.84s | Proxy cho node-boot của gemini (~2.8s) |
| `codex exec "2+2"` | CHƯA đăng nhập | — | — | ~12s | Header in ngay, rồi retry 5×WS + 5×HTTPS tới 401 (đường LỖI, không phải lượt thường) |

**Kết luận (HIGH):** Spawn-per-turn viable cho cả ba. Node-boot ~2.8s là sàn; một lượt thật của gemini/codex ≈ node-boot + suy luận, vẫn dưới 25s. Claude lượt "2+2" 8.1s.

**Cảnh báo gotcha — long pole:** Đường **Claude Code KB-consult** hiện tại đo được **42–114s** (ghi trong comment `execZaloRunner`/`runClaude`, vì nó `--add-dir` cả knowledge base). `claude -p "2+2"` 8s là lượt TRỐNG, không phải lượt consult. Router hôm nay CỐ Ý chạy Claude Code bằng ctx GỐC (không phải ngân sách 25s của chuỗi API — xem `app_llm_router.go:190`). Khi gộp claude-code vào `local_cli` (§5.2), **không được đặt nó dưới `defaultLLMProviderTimeout=25s`** hoặc mọi lượt KB dài sẽ bị giết ở giây 25. Đây là tương tác nguy hiểm nhất của việc gộp.

**Codex retry:** khi lỗi mạng/auth, codex tự retry ~10 lần (~12s) trước khi thoát. Lượt THÀNH CÔNG trả về nhanh, nhưng một lượt lỗi tạm thời có thể ăn gần hết 25s. Ghi nhận cho ngân sách timeout của codex.

---

## 2. Non-interactive argv — xác minh trên `--help` của bản đang cài

Bảng dưới đã VERIFY từng cờ trên `--help` (không lấy từ spec). [VERIFIED: `--help` trên máy 2026-08-06]

### claude 2.1.223
- **Non-interactive:** `-p` / `--print`. Prompt: **positional HOẶC stdin**. Base app hiện dùng **stdin** (`execZaloRunner`: `cmd.Stdin = strings.NewReader(prompt)`).
- **Model:** `--model <model>` (alias `opus`/`sonnet`/`fable` hoặc tên đầy đủ `claude-fable-5`).
- **Read-only:** **KHÔNG dùng `--permission-mode plan`.** Cơ chế thật = whitelist `--allowed-tools Read Grep Glob WebFetch` (xem `ConsultReadOnly()` trong `internal/agent/profile.go`). Profile ghi rõ: *"NEVER add a permission-mode flag"*. Kèm `--output-format stream-json --verbose` để stream từng bước; parse event `result` làm câu trả lời.
- **Structured output:** `--output-format json` (một khối) hoặc `stream-json` (realtime, base app đang dùng); `--json-schema <schema>` để ràng buộc shape.
- **Cờ CẤM (test phải chặn):** `--dangerously-skip-permissions`, `--allow-dangerously-skip-permissions`, `--permission-mode bypassPermissions`.

### codex 0.146.1
- **Non-interactive:** `codex exec [PROMPT]`. Prompt **positional** HOẶC stdin (nếu `-` hoặc bỏ trống → đọc stdin; nếu vừa pipe stdin vừa có prompt → stdin nối thành khối `<stdin>`). **Quan trọng:** `codex exec` mặc định ĐỌC stdin ("Reading additional input from stdin..."). Từ Go phải **đóng stdin hoặc cấp stdin rỗng** để không treo.
- **Model:** `-m` / `--model` (mặc định thấy trong header: `gpt-5.5`).
- **Read-only:** `-s read-only` (values: `read-only`, `workspace-write`, `danger-full-access`). `codex exec` MẶC ĐỊNH đã `sandbox: read-only`, `approval: never` (thấy trong header) — vẫn truyền `-s read-only` tường minh để không phụ thuộc config.toml của người vận hành.
- **Structured output:** `--json` (JSONL sự kiện) **và** `-o` / `--output-last-message <FILE>` (ghi câu trả lời CUỐI ra file — hợp đồng gọn nhất, tránh parse JSONL); `--output-schema <FILE>`.
- **Nên thêm:** `--skip-git-repo-check` (chạy ngoài git repo — cwd Zalo-Bot không phải git repo), `--ephemeral` (không ghi session vào `~/.codex/sessions`), `--color never`, `-C/--cd <DIR>`.
- **Cờ CẤM:** `--dangerously-bypass-approvals-and-sandbox`, `-s workspace-write`, `-s danger-full-access`.

### gemini 0.54.0
- **Non-interactive:** `-p` / `--prompt <string>` (positional `[query..]` mặc định INTERACTIVE — phải dùng `-p`). Prompt nối vào stdin nếu có.
- **Model:** `-m` / `--model`.
- **Read-only:** `--approval-mode plan` (choices: `default`, `auto_edit`, `yolo`, `plan`; `plan` = read-only mode). [VERIFIED]
- **Structured output:** `-o` / `--output-format json` (choices: `text`, `json`, `stream-json`).
- **Nên thêm:** `--skip-trust` (tránh treo ở dialog workspace-trust).
- **Cờ CẤM:** `-y` / `--yolo`, `--approval-mode yolo`, `--approval-mode auto_edit`, `--raw-output` (mặc định gemini SANITIZE output — untrusted Zalo input nên GIỮ mặc định).

---

## 3. Auth detection — ba cơ chế, và cái mong manh

| Provider | Cơ chế | Shape/exit ĐO THẬT | Cost | Confidence |
|---|---|---|---|---|
| claude | `claude auth status --json` | `{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","email":"...","orgId":"...","orgName":"...","subscriptionType":"team"}` exit 0 | 0.65s | HIGH [VERIFIED] |
| codex | `codex login status` | chưa login → stdout "Not logged in", **exit 1**. Logged-in → exit 0 (suy ra). | 0.15s | HIGH cho nhánh chưa-login; nhánh logged-in exit-0 chưa xác minh được |
| gemini | KHÔNG có lệnh status | Không có subcommand auth/login/status (commands: mcp/extensions/skills/hooks/gemma/query). `gemini -p` chưa-auth → "Please set an Auth method in your `~/.gemini/settings.json`..." **exit 41** | 2.8s | dò-bằng-spawn tốn kém, không dùng cho Test |

**Gemini — chi tiết mong manh (rủi ro cao nhất, đúng §5.4):**
`~/.gemini` HIỆN chỉ có `history/`, `tmp/`, `projects.json` (+ file `.tmp`). **KHÔNG có** `oauth_creds.json`, **KHÔNG có** `settings.json`. Sau khi đăng nhập Google OAuth (LOGIN_WITH_GOOGLE), gemini-cli ghi credential ra `~/.gemini/oauth_creds.json` (và `google_accounts.json`) và đặt kiểu auth trong `settings.json`.

- **Đường dò đề xuất:** probe tồn tại + đọc được của `~/.gemini/oauth_creds.json`. Nếu **thiếu HOẶC không đọc được → trả "không xác định" (unknown), KHÔNG trả "chưa đăng nhập".** Đoán sai hướng "chưa đăng nhập" đẩy người dùng vào vòng login vô ích (§5.4).
- **Confidence tên file:** LOW-MEDIUM — suy từ kiến thức source gemini-cli, CHƯA verify vì chưa login. Phải xác nhận sau lần login đầu → **Awaiting.**
- Cô lập sau một hàm, test ghim, fail-về-unknown (đúng §5.4, §12).

---

## 4. Descriptor data trích từ 9Router + npm registry

### npm package names & versions [VERIFIED: `npm view` 2026-08-06, latest = bản đang cài]
| Vendor | npm package | version | Ghi chú |
|---|---|---|---|
| claude-code | `@anthropic-ai/claude-code` | 2.1.223 | Nhưng `claude` đang cài là **native exe** ở `~/.local/bin/claude.exe`, KHÔNG qua npm global. |
| codex | `@openai/codex` | 0.146.1 | Cài qua npm global. |
| gemini | `@google/gemini-cli` | 0.54.0 | Cài qua npm global. |

### bin entry (đường CLI thật để gọi từ Go) [VERIFIED: đọc shim + package.json]
- codex: `node_modules/@openai/codex/bin/codex.js` (package.json `bin.codex`)
- gemini: `node_modules/@google/gemini-cli/bundle/gemini.js` (package.json `bin.gemini`)

### Model lists 9Router (seed cho `Discover` tĩnh) [CITED: api.github.com/repos/decolua/9router/.../registry]
- **codex.js** — `id:"codex"`, `category:"oauth"`, `deprecated:true`, `format:"openai-responses"`, `baseUrl:"https://chatgpt.com/backend-api/codex/responses"` (đây là đường giả-app RỦI RO ta KHÔNG lấy — chỉ lấy danh sách model). Models: `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.3-codex-spark` (+ mỗi cái một biến thể `-review`), + image models `gpt-5.5-image`/`gpt-5.4-image`/`gpt-5.3-image`.
- **gemini-cli.js** — `id:"gemini-cli"`, `category:"free"`, `deprecated:true`, OAuth Google. Models: `gemini-3.1-pro-preview`, `gemini-3-pro-preview`, `gemini-3-flash-preview`, `gemini-3.1-flash-lite-preview`, `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-2.5-flash-lite`.
- **claude (CLI):** alias từ `claude --help`: `opus`/`sonnet`/`fable` hoặc tên đầy đủ. (9router `anthropic.js` — API-key — ở research trước liệt kê `claude-sonnet-4-20250514`.)

**Caveat quan trọng (MEDIUM):** danh sách 9Router là model của *proxy 9router*, không đảm bảo trùng thứ mà `-m` của CLI chấp nhận. Nguồn quyền uy cho codex là `~/.codex/models_cache.json` (207KB, có trên máy). Seed `Discover` bằng một tập tối giản đã kiểm (vd codex: `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini`) và verify `-m` chấp nhận — đừng bê nguyên list 9router.

---

## 5. Bản đồ overlay code + seam §5.2 (đường dẫn thật)

### Adapter mới đặt ở đâu
- Interface: `providerAdapter{Generate,Test,Discover}` — `app_llm_types.go:71`. `credential` là THAM SỐ mỗi lượt, không phải field.
- Bốn adapter HTTP hiện tại: `app_llm_http.go` (mỗi cái = struct nhúng `llmHTTP` + `providerID`). Adapter mới `local_cli` **song song** nhưng transport là spawn tiến trình, không HTTP — đặt ở tệp mới, vd `app_llm_cli.go`.
- Đăng ký: `newLLMAdapter(kind, providerID, client)` — `app_llm_router.go:290`. Thêm case cho kind CLI. **Lưu ý:** kind `"gemini"` ĐÃ là Gemini HTTP API; CLI phải dùng kind PHÂN BIỆT (vd `"codex"`, `"gemini-cli"`, `"claude-code"`).
- `appLLMAdapters()` — `app_llm_router.go:406` — dựng adapter từ provider đã lưu; adapter CLI không cần `http.Client`.

### Seam §5.2 (base AgentDC, string-patch lúc build)
- **BuildApp.psm1** patch `duty.go`: thay
  `a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)`
  → `a.answerZalo(ctx, deps.cfg, a.appZaloRunner(deps.cfg, deps.run, threadID, len(files) > 0), threadID, question, step, reply, files...)` (BuildApp.psm1:252-256). Có `Assert-SignatureAbsent 'a.appZaloRunner('` (221) và assert **đúng 1** call site `answerZalo` (262-266) — canary drift, hỏng thì build đỏ.
- `answerZalo` — `AgentDC/internal/daemon/duty.go:444`.
- Đường Claude Code hiện tại = **`execZaloRunner.Run`** — `duty.go:1710-1795`. Dùng `agent.ConsultReadOnly()` (`profile.go:146`): binary `claude` qua `exec.LookPath`, args `-p --session-id {SID} --output-format stream-json --verbose --allowed-tools Read Grep Glob WebFetch` + `--model` + `--add-dir <KBRoots>` + `--add-dir <FilesDir>`; prompt qua **stdin**; env `MAX_THINKING_TOKENS`, `EnvStrip`. Parse `result` event của stream-json. **Gộp claude-code vào local_cli phải bảo toàn TẤT CẢ những thứ này** — đây là descriptor "giàu" hơn hẳn codex/gemini, và là phần rủi ro nhất (§5.2, §12).
- `runClaude` mà spec §5.2 nhắc = **method của router** `func (r *appLLMRunner) runClaude` — `app_llm_router.go:190-217` (không phải hàm base). Nó chạy mắt xích cuối bằng ctx GỐC + nhận `step`. Việc gộp làm biến mất nhánh đặc biệt này.

### §6 store seams — `overlay/internal/store/app_llm.go`
- `systemProviderID = "claude-code"` (26).
- `validateLLMRoute` (450): hiện **từ chối chuỗi rỗng** + đòi mắt xích cuối = `claude-code` đang bật. §6 đổi: **cho phép rỗng**, bỏ ràng buộc "phải là claude-code", giữ "mắt xích cuối phải đang bật" (chỉ có nghĩa khi chuỗi khác rỗng).
- `BootstrapClaudeRoute` (417): hiện gieo chuỗi một-mắt-xích claude-code lúc khởi động nếu rỗng. §6: máy mới KHÔNG force-seed → sửa để rỗng-là-hợp-lệ (no-op khi rỗng, không gieo claude-code).
- `DeleteLLMProvider` (192) chặn xoá provider còn được route trỏ tới (`ErrLLMProviderInUse`).
- Router `app_llm_router.go:114` giả định `entries[len-1]` LUÔN là Claude Code. §6 phá giả định này → logic tách mắt xích cuối phải viết lại.

### §7 attachment gate — `app_llm_router.go`
- `HasAttachments` (348) + `threadHasAttachments` (361): hiện lượt-có-tệp đi THẲNG `runClaude` (115-117), bỏ qua mọi API. §7: cho phép đi qua **bất kỳ `local_cli`**, vẫn bỏ qua 4 provider HTTP. Router phải đổi từ "có tệp → Claude Code" thành "có tệp → local_cli đủ điều kiện đầu tiên".

### Kill-tree §5.1 — điểm nối bắt buộc
- `killPidTree(pid, sid, logger)` — `AgentDC/internal/daemon/headless.go:258` → `runTaskkillTree(pid)` — `treekill_windows.go:29` = `taskkill /PID <pid> /T /F` (giết CẢ CÂY). Có bản `treekill_other.go` cho non-Windows.
- **`execZaloRunner` hiện dùng `exec.CommandContext` trần** — khi ctx huỷ chỉ giết tiến trình con TRỰC TIẾP. Với `local_cli` cây là `node.exe → codex.js → [rust binary]` (codex) hoặc `node.exe → gemini.js`, nên `CommandContext` sẽ để lại mồ côi. **Adapter mới phải bắt `ctx.Done()` và gọi `killPidTree(cmd.Process.Pid, ...)`** — đúng bug Task 5, và plan phải có test spawn cây nhiều tầng thật rồi khẳng định không còn mồ côi (§5.1, §11).

---

## 6. `npm install -g` từ node đóng sẵn — feasibility

### npm KHÔNG được đóng trong gói — gap phải đóng
- Gói bundle (BuildApp.psm1:365 `Assert-AppPackage`) yêu cầu `app\node\node.exe` — và `F:\ZaloBot-test\app\node\` CHỈ có `node.exe` (71MB). **KHÔNG có `node_modules/npm`.** [VERIFIED: ls thư mục]
- ⇒ Spec §5.3 "chạy `npm install -g` bằng node.exe đóng sẵn" **KHÔNG chạy được như đang viết.** Plan phải: (a) đóng thêm npm vào `app\node` (copy `node_modules/npm` + `npm.cmd`/`npm-cli.js`), rồi gọi `node.exe app\node\node_modules\npm\bin\npm-cli.js install -g ...`; hoặc (b) một cơ chế cài khác. Đây là một TASK BẮT BUỘC + sửa `Assert-AppPackage` để yêu cầu npm.

### Global install lands ở đâu
- `npm prefix -g` (dev) = `C:\Program Files\nodejs`; **global bin = chính prefix dir** trên Windows (nên shim `codex.ps1` nằm ngay ở `C:\Program Files\nodejs`). [VERIFIED]
- Với node đóng sẵn: đặt `--prefix <app\node>` (hoặc env `npm_config_prefix`) để install nằm TRONG gói, tự chứa. CLI sẽ ở `app\node\node_modules\@openai\codex\bin\codex.js` v.v.

### GỌI CLI cài-qua-npm từ Go trên Windows — GOTCHA quan trọng nhất của area này
- Global install tạo 3 shim/bin: `codex` (bash), `codex.cmd`, `codex.ps1`. **KHÔNG** `exec.Command("codex")`:
  - `.ps1` không nằm trong `PATHEXT` → Go không chạy được trực tiếp.
  - `.cmd` cần `cmd.exe`, và có hiểm hoạ escaping tham số (CVE-2024-24576; Go 1.23 siết chạy `.bat`/`.cmd`).
- **Cách đúng:** gọi thẳng entry JS bằng node đóng sẵn — đúng y hệt việc shim `.ps1` làm bên trong (`node node_modules/.../cli.js $args`):
  - codex: `exec.Command(nodeExe, filepath.Join(prefix, "node_modules/@openai/codex/bin/codex.js"), "exec", "-s", "read-only", "--skip-git-repo-check", "--ephemeral", "-m", model, prompt)`
  - gemini: `exec.Command(nodeExe, filepath.Join(prefix, "node_modules/@google/gemini-cli/bundle/gemini.js"), "-p", prompt, "-m", model, "--approval-mode", "plan", "--skip-trust")`
- **claude khác hẳn:** là native exe → `exec.Command(claudeExe, args...)` trực tiếp (LookPath như hôm nay). Không đụng node.

---

## 7. Prior art — hợp đồng output nào ổn định, cái nào phải parse mò

- **claude:** `--output-format stream-json`/`json` là hợp đồng có tài liệu (Claude Code SDK). Base app ĐÃ parse event `result` (`parseStreamLine`, `duty.go`). **Tái dùng nguyên.** [VERIFIED: code base]
- **codex:** `--json` phát JSONL events **và** `-o/--output-last-message <FILE>` ghi câu trả lời CUỐI ra file. **Đề xuất dùng `-o <file>`** làm hợp đồng chính (đọc 1 file, không phải phân loại event type trong JSONL). Event type nào mang câu trả lời cuối trong `--json` → chưa xác minh (chưa login) → Awaiting.
- **gemini:** `-o json` là structured output có tài liệu.
- **Phần phải parse mò / chưa ghim:** phân biệt `rate_limit` (hết hạn mức) vs `credential` (chưa đăng nhập) từ stderr thật của codex/gemini lúc quota cạn — CHƯA verify (chưa login). §9 đã chốt: khi không phân biệt được, **mặc định `rate_limit`** (đoán sai hướng này chỉ tốn một lượt thử Provider sau; đoán sai hướng kia giết cả chuỗi). Cần ghim bằng output THẬT sau khi login → Awaiting.

---

## 8. Security / read-only (§8)

### Read-only mỗi hãng (test phải chứng minh `Generate` dựng ĐÚNG những cờ này)
- claude: `--allowed-tools Read Grep Glob WebFetch` (whitelist — KHÔNG `--permission-mode plan`).
- codex: `-s read-only`.
- gemini: `--approval-mode plan`.

### Cờ CẤM (test bảng phải chứng minh `Generate` KHÔNG BAO GIỜ phát)
- claude: `--dangerously-skip-permissions`, `--allow-dangerously-skip-permissions`, `--permission-mode bypassPermissions`.
- codex: `--dangerously-bypass-approvals-and-sandbox`, `-s workspace-write`, `-s danger-full-access`.
- gemini: `-y`/`--yolo`, `--approval-mode yolo`, `--approval-mode auto_edit`, `--raw-output`.

### ASVS áp dụng
| ASVS | Áp dụng | Control chuẩn |
|---|---|---|
| V5 Input Validation | yes | Tin Zalo (untrusted) đi vào argv → truyền qua **arg slice của `exec.Command` / stdin, KHÔNG BAO GIỜ nội suy vào chuỗi shell**. Không `cmd.exe /c "...prompt..."`. |
| V6 Cryptography | yes | Credential do CLI tự giữ (`~/.codex`, `~/.gemini`, `~/.claude`) — Portal không thấy/không lưu (§3 spec). DPAPI của 4 provider HTTP không đổi. |
| V2/V4 | partial | Sandbox read-only là access-control ở tầng agent (§8). |

**Rò dữ liệu khách:** lượt-có-tệp đi qua local_cli → ảnh khách CÓ THỂ tới OpenAI/Google (§7) — Portal phải nêu ở chỗ bật Provider. Canary đính kèm: **4 provider HTTP không bao giờ nhận đường dẫn/nội dung tệp** — sửa, không xoá (§7, §11). Cổng `Assert-NoProviderCredential` (BuildApp.psm1:299) quét MỌI tệp ngoài `node_modules` — phải xanh; CLI cất credential NGOÀI gói nên không vi phạm, nhưng cổng phải chứng minh.

---

## 9. Architectural Responsibility Map

| Capability | Primary Tier | Rationale |
|---|---|---|
| Spawn CLI + parse output (`Generate`) | Daemon (Go) — adapter `local_cli` | Song song 4 adapter HTTP; router không đổi |
| Dò đăng nhập (`Test`) | Daemon (Go) — mỗi hãng một đường | claude JSON / codex exit / gemini file-probe |
| Model list (`Discover`) | Daemon (Go) — tĩnh từ descriptor | CLI không liệt kê model như API |
| Huỷ = giết cây tiến trình | Daemon (Go) — `killPidTree` | node→cli→[rust]; CommandContext không đủ |
| Cài CLI theo yêu cầu | Portal + node đóng gói | `node npm-cli.js install -g` (cần bundle npm) |
| Login OAuth | CLI của hãng | Portal spawn `login`; không chạm credential |
| Giữ credential | CLI của hãng (`~/.codex` v.v.) | Portal không thấy/không lưu |
| Route invariant (rỗng/last-enabled) | store (`app_llm.go`) + UI (`models.js`) | §6 |

---

## 10. Runtime State Inventory (đây là phase refactor + adapter mới)

| Category | Tìm thấy | Việc cần |
|---|---|---|
| Stored data | Máy đã cài có route `claude-code` do `BootstrapClaudeRoute` gieo. Kind của provider `claude-code` hiện KHÔNG nằm trong {openai,anthropic,gemini,openrouter} (nên `newLLMAdapter` trả false → router đi nhánh Claude func). | Gộp §5.2: cấp cho `claude-code` một kind mà `newLLMAdapter` map sang `local_cli`. **Migration:** hoặc UPDATE cột `kind` của hàng `claude-code` cũ, hoặc map theo ID trong code. Route cũ vẫn hợp lệ sau §6 (last-enabled). Không cần migrate dữ liệu route (rỗng = không có hàng). |
| Live service config | Không có (không đăng ký service ngoài). | None — verified: không có webhook/scheduler nào ôm chuỗi này. |
| OS-registered state | Không có. | None. |
| Secrets/env vars | Credential CLI ở `~/.codex`, `~/.gemini`, `~/.claude` (CLI tự giữ, KHÔNG qua DPAPI). `MAX_THINKING_TOKENS` env cho claude phải giữ. 4 provider HTTP vẫn DPAPI. | None cho key; giữ env claude khi gộp. |
| Build artifacts | Global npm install tạo shim `.ps1/.cmd` — nhưng ta gọi qua `node cli.js` nên shim vô can. `~/.codex/models_cache.json` là nguồn model của codex. **`app\node` thiếu npm** (mục 6). | Build phải thêm npm vào `app\node` + sửa `Assert-AppPackage`. |

---

## 11. Validation Architecture

**Test framework:** Go test qua overlay build check — `pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO` (cwd Zalo-Bot không build trực tiếp; overlay copy lên bản sao AgentDC). Cộng `providers.test.mjs` (Portal) và cổng PowerShell trong `BuildApp.psm1`.

**Test hiện có (ghim hành vi cũ — sẽ phải sửa):** `app_llm_router_test.go`, `app_llm_http_test.go`, `app_llm_api_test.go`, `store/app_llm_test.go`, `duty_test.go` (base, gồm `TestAnswerZaloPutsAnEarlierFileIntoThePrompt`), `models.js` unit test cho `isLocked/tailIndex/canMove/move`.

**Wave 0 gaps — test mới cần trước khi code:**
- [ ] Bảng argv cho từng hãng: `Generate` dựng đúng argv + **không bao giờ có cờ bỏ sandbox** (danh sách mục 8).
- [ ] Ánh xạ lỗi bảng: hết hạn mức / chưa đăng nhập / chưa cài / bị huỷ — từ output THẬT từng CLI (phần codex/gemini quota chờ login → Awaiting).
- [ ] Dò trạng thái: 3 đường, gồm nhánh Gemini hỏng → "không xác định".
- [ ] Huỷ: ctx huỷ → `killPidTree` giết cây `node→cli`, spawn cây nhiều tầng thật, khẳng định 0 mồ côi.
- [ ] Canary đính kèm: 4 provider HTTP không nhận path/nội dung tệp, kể cả khi local_cli được phép.
- [ ] Chuỗi rỗng: bot báo "chưa sẵn sàng", không đánh rơi tin im lặng.
- [ ] `validateLLMRoute`/`BootstrapClaudeRoute`/`models.js` sau §6: sửa test ghim invariant cũ.
- [ ] Cổng gói: `Assert-NoProviderCredential` xanh + `Assert-AppPackage` yêu cầu npm mới.

**Sampling:** per-task `go-check.ps1`; phase gate = full overlay build + cổng PowerShell xanh trước `/ship`.

---

## 12. Environment Availability

| Dependency | Required by | Available | Version | Fallback |
|---|---|---|---|---|
| `claude` (native exe) | provider claude-code | ✓ (logged in, `team`) | 2.1.223 | — |
| `codex` (npm global) | provider codex | ✓ (chưa login) | 0.146.1 | cài theo yêu cầu |
| `gemini` (npm global) | provider gemini-cli | ✓ (chưa login) | 0.54.0 | cài theo yêu cầu |
| `node.exe` đóng gói | spawn codex/gemini + npm install | ✓ | — | `app\node\node.exe` |
| `npm` đóng gói | `install -g` theo yêu cầu | ✗ | — | **KHÔNG có fallback — build phải thêm npm** |
| `taskkill /T` | kill-tree | ✓ | Windows | `runTaskkillTree` |

**Thiếu, chặn:** npm không đóng trong gói (mục 6). Plan phải thêm việc bundle npm hoặc §5.3 không chạy.

---

## Sources

**Primary (HIGH) — đo/đọc trên máy này 2026-08-06:**
- `claude`/`codex`/`gemini` `--help`, `auth status --json`, `login status`, timing Measure-Command; `~/.gemini` `~/.codex` `~/.claude` listing; `npm view`; `npm prefix -g`; shim `codex.ps1`/`gemini.ps1`; `F:\ZaloBot-test\app\node`.
- Code: `overlay/internal/daemon/app_llm_{types,http,router}.go`, `overlay/internal/store/app_llm.go`, `overlay/internal/webui/static/pages/models.js`, `AgentDC/internal/daemon/{duty.go,headless.go,treekill_windows.go}`, `AgentDC/internal/agent/profile.go`, `scripts/BuildApp.psm1`.

**Secondary (MEDIUM):**
- 9Router registry `codex.js`, `gemini-cli.js` (model lists) — api.github.com/repos/decolua/9router. Danh mục file registry đầy đủ đã liệt kê (có `claude.js`, `gemini-cli.js`, `codex.js`, `openai.js`, `gemini.js`).
- `.planning/research/9router-RESEARCH.md` (research trước — descriptor tách codec).

**Tertiary (LOW) — chưa verify, cần login:**
- Tên file credential Gemini (`~/.gemini/oauth_creds.json`).
- Event type mang câu trả lời cuối trong `codex exec --json`.
- stderr phân biệt rate_limit vs credential của codex/gemini.

---

## Decisions for plan

> Áp thẳng, mỗi dòng một quyết định. Phần lý do ở các mục trên.

**Kiến trúc & spawn:**
1. **Spawn-per-turn viable — KHÔNG mở plan bằng spike tiến trình thường trú.** claude lượt thật median 8.1s / max 10s, gemini node-boot 2.8s, codex status 0.15s — tất cả dưới 25s (đo 2026-08-06).
2. **Một adapter `local_cli` + ba descriptor dữ liệu** (không ba bản sao). Đặt tệp mới `app_llm_cli.go` song song `app_llm_http.go`; đăng ký trong `newLLMAdapter` (`app_llm_router.go:290`) với kind PHÂN BIỆT: `codex`, `gemini-cli`, `claude-code` (kind `gemini` đã là HTTP API — không tái dùng).
3. **Gọi codex/gemini từ Go bằng `node.exe <cli>.js`, KHÔNG qua shim.** codex = `node app\node\node_modules\@openai\codex\bin\codex.js exec ...`; gemini = `node ...\@google\gemini-cli\bundle\gemini.js -p ...`. Bare `codex`/`gemini` là `.ps1`/`.cmd` shim — Go không chạy `.ps1`, và `.cmd` dính CVE-2024-24576. **claude là native exe → `exec.Command(claudeExe, ...)` trực tiếp.**
4. **Huỷ lượt PHẢI gọi `killPidTree(cmd.Process.Pid,...)`** (`headless.go:258` → `taskkill /T`), KHÔNG dựa `exec.CommandContext` — cây là `node→cli→[rust]`, CommandContext để lại mồ côi (bug Task 5). Test spawn cây nhiều tầng, khẳng định 0 mồ côi.
5. **Claude Code KB-consult phải giữ ngân sách >25s (đo 42–114s), KHÔNG đặt dưới `defaultLLMProviderTimeout=25s`.** Khi gộp claude-code vào local_cli, mắt xích cuối vẫn chạy bằng ctx gốc như `runClaude` hôm nay. Codex đặt timeout dư cho retry ~12s của nó.

**Argv (đã verify `--help` bản đang cài — dùng nguyên):**
6. claude: `-p --session-id <uuid> --output-format stream-json --verbose --allowed-tools Read Grep Glob WebFetch --model <m> --add-dir <KBRoots> --add-dir <FilesDir>`, prompt qua **stdin**, parse event `result`. Read-only = whitelist allowed-tools, **KHÔNG `--permission-mode plan`**.
7. codex: `exec -s read-only --skip-git-repo-check --ephemeral --color never -m <m> -o <outfile> <prompt>`. Đọc câu trả lời từ file `-o` (không parse JSONL). **Đóng/cấp stdin rỗng** (codex exec mặc định đọc stdin → treo nếu bỏ ngỏ).
8. gemini: `-p <prompt> -m <m> --approval-mode plan --skip-trust -o json`.
9. **Test bảng phải chặn cờ bỏ sandbox** (§8): claude `--dangerously-skip-permissions`/`--permission-mode bypassPermissions`; codex `--dangerously-bypass-approvals-and-sandbox`/`-s workspace-write`/`-s danger-full-access`; gemini `-y`/`--yolo`/`--approval-mode yolo|auto_edit`/`--raw-output`.

**Auth detection:**
10. claude Test = `claude auth status --json` → parse `loggedIn` + `subscriptionType` + `email` (exit 0, ~0.65s).
11. codex Test = `codex login status` → exit 0 = logged in, non-0 = chưa (đo: chưa login → "Not logged in" exit 1, ~0.15s).
12. **gemini Test = probe file `~/.gemini/oauth_creds.json`; thiếu HOẶC không đọc được → trả "unknown", KHÔNG trả "chưa đăng nhập".** Cô lập sau một hàm, test ghim. (Tên file confidence LOW → xác nhận sau login, mục Awaiting.)

**§6 route invariant (store + UI, đổi cùng nhau):**
13. `validateLLMRoute` (`store/app_llm.go:450`): cho phép entries RỖNG; bỏ ràng buộc "phải kết thúc bằng claude-code"; giữ "mắt xích cuối phải đang bật" (chỉ khi khác rỗng).
14. `BootstrapClaudeRoute` (417): KHÔNG force-seed claude-code trên máy mới; rỗng-là-hợp-lệ.
15. Router `app_llm_router.go:114` bỏ giả định `entries[len-1]==claude-code`; `models.js` `isLocked/tailIndex` (CLAUDE_ID) bỏ đặc quyền claude-code. Sửa test ghim invariant cũ ở cả hai chỗ.
16. **Migration:** cấp kind local_cli cho hàng provider `claude-code` cũ (UPDATE `kind` hoặc map theo ID). Route cũ vẫn hợp lệ, không cần migrate dữ liệu route.

**§7 attachment:**
17. Lượt-có-tệp đi qua **local_cli đủ điều kiện đầu tiên** (không cứng vào Claude Code); vẫn bỏ qua 4 provider HTTP. Canary sửa-không-xoá: 4 HTTP không bao giờ nhận path/nội dung tệp. `threadHasAttachments`/`HasAttachments` giữ, đổi đích.

**§5.3 cài theo yêu cầu (có gap):**
18. **npm KHÔNG có trong gói — task bắt buộc: bundle npm vào `app\node`** (copy `node_modules/npm` + entry), gọi `node app\node\node_modules\npm\bin\npm-cli.js install -g <pkg> --prefix app\node`, và sửa `Assert-AppPackage` (BuildApp.psm1:362) yêu cầu npm. Không có bước này §5.3 không chạy.
19. npm packages: `@openai/codex`, `@google/gemini-cli` (claude đã là native exe, không cần npm install). Install với `--prefix app\node` để tự chứa; CLI ra `app\node\node_modules\...`.

**§9 lỗi:**
20. Không thêm `llmErrorKind`. Khi không phân biệt được rate_limit vs credential từ output CLI → **mặc định `rate_limit`** (fallback). Chưa cài → `credential`. Bị huỷ → `canceled`, không telemetry.

**Descriptor seed model (`Discover` tĩnh):**
21. Seed từ 9Router NHƯNG verify `-m` chấp nhận (đừng bê nguyên list proxy). codex: `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini` (nguồn quyền uy = `~/.codex/models_cache.json`). gemini: `gemini-2.5-pro`, `gemini-2.5-flash`, `gemini-3-pro-preview`… claude: alias `opus`/`sonnet` + tên đầy đủ.

---

## Awaiting

Những mục dưới KHÔNG giải được nếu không đăng nhập codex/gemini. Chúng chỉ chỉnh MỘT hằng số trong descriptor hoặc một nhánh parse — kiến trúc không đổi. Cần chạy sau khi login (§14 spec):

1. **Gemini credential file** — đăng nhập gemini một lần, rồi `ls ~/.gemini` để xác nhận CHÍNH XÁC tên file credential (kỳ vọng `oauth_creds.json`; cũng ghi lại `google_accounts.json`, và khoá auth trong `settings.json`). Ghim path đó vào hàm dò + test.
2. **codex `--json` event cuối** — đăng nhập codex, chạy `codex exec --json "hi"`, ghi lại event type nào mang câu trả lời cuối (để đối chiếu với đường `-o <file>` đã chọn; nếu `-o` đủ thì không cần parse JSONL).
3. **Phân biệt rate_limit vs credential** — đăng nhập rồi tạo tình huống hết hạn mức thật trên codex VÀ gemini; ghi lại stderr/exit chính xác của "hết hạn mức" so với "chưa đăng nhập". Cho tới lúc đó §9 mặc định `rate_limit`.
4. **Ba lệnh logout** — kiểm `claude auth logout` (hoặc `/logout`), `codex logout`, cách gemini logout (không có subcommand rõ — có thể xoá `~/.gemini/oauth_creds.json` hoặc `gemini mcp`-style). Nút "Ngắt kết nối" (§10) cần đúng lệnh.
5. **codex logged-in exit 0** — xác nhận `codex login status` trả exit 0 khi đã đăng nhập (mới chỉ verify được nhánh chưa-login exit 1).
6. **claude `auth login --claudeai`** — help của subcommand `auth` chưa dump trong session này; xác nhận cờ `--claudeai` (đường thuê bao) tồn tại ở bản 2.1.223 trước khi khoá luồng login của Portal.
