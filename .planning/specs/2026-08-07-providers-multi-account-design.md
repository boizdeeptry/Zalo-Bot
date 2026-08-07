# Multi-account cho provider subscription — design

> Sub-project **#3/4** của "Providers UI kiểu 9Router". Thứ tự: #1 gallery+detail (XONG) → #2 connect (spec+plan, tạm dừng) → **#3 multi-account (spec này)** → #4 combos. Cùng nhánh `feat/cli-subscription-providers`, ship một lần ở cuối.

## Goal

Cho mỗi provider **subscription** (Claude Code, OpenAI Codex) đăng nhập **NHIỀU tài khoản**, mỗi tài khoản một phiên CLI **độc lập** (không đè nhau), và tự **xoay vòng** giữa các tài khoản mỗi lượt để **nhân trần quota** của subscription + né tài khoản vừa dính giới hạn. Người mua **không mở terminal** (zero-terminal như #2).

## Non-goals (để sub-project khác / execute-time)

- **KHÔNG** multi-account cho provider API-key (OpenAI/Anthropic/Gemini/OpenRouter) — nhóm đó là form-dán-khoá, không có khái niệm "phiên đăng nhập nhiều tài khoản".
- **KHÔNG** xoay vòng **giữa hai provider khác nhau** (Claude ↔ ChatGPT) để "đa dạng phong cách" — đó là **trục provider**, thuộc **#4 Combos** (RoundRobin/Fusion). #3 chỉ xoay ở **trục account** (nhiều login của *cùng* một provider). Hai trục lồng nhau: mỗi lượt chọn provider (chuỗi fallback / sau này combo #4) → rồi chọn account trong provider đó (#3).
- **KHÔNG** sticky theo hội thoại (thread) — model cuối là **round-robin theo lượt + cooldown**, đã hội tụ trong /discuss. Vì thế **không** cần plumb `threadID` vào selector.
- **KHÔNG** đo quota thật của subscription — `claude`/`codex` **không** có lệnh đọc quota còn lại (đã xác minh `claude --help`: chỉ `auth/doctor/setup-token/...`; giới hạn chỉ hiện ra dưới dạng **lỗi khi chạy**). "Rảnh nhất" = heuristic cục bộ (round-robin + cooldown), không phải quota thật.
- **KHÔNG** ghim tên biến env / cú pháp login CHÍNH XÁC trong spec — capture-first lúc CLI thật có mặt và đăng nhập thật (checkpoint như #2 và engine Task 8).

## Bối cảnh (đã chốt trong /discuss)

- Engine + UI#1 xong; #2 connect có spec+plan (tạm dừng chờ môi trường ổn định). #3 dựng **trên** cơ chế connect của #2.
- Provider subscription = Claude Code (`claude`, native exe, kind `claude_code`, id `claude-code` seed sẵn) + OpenAI Codex (`codex`, node package, kind/id `codex` do #2 tạo khi connect). Gemini đã bỏ.
- **Sự thật spawn hiện tại**: `cliAdapter.spawn` (`app_llm_cli.go:359-372`) dựng `exec.Command` ở dòng 371 **không set `cmd.Env`** → mọi lượt dùng thư mục config **mặc định của máy** (`~/.codex`, `~/.claude`) = login chung với việc người mua tự dùng CLI. Có sẵn seam test `a.run` (nil = spawn thật).
- **Sự thật store**: schema idempotent (`CREATE TABLE IF NOT EXISTS` + `INSERT OR IGNORE`, chạy mỗi lần mở DB), `schema_version` = 2, bảng con dùng `REFERENCES llm_providers(id) ON DELETE CASCADE` (như `llm_models`). CRUD theo pattern `inLLMTx`/`assertOneRow`/`boolInt`.
- Ràng buộc bất di bất dịch: **KHÔNG credential nào qua daemon** (phiên nằm trong thư mục config của CLI), **spawn CLI chính chủ** (không proxy), zero-terminal.

## Quyết định đã chốt (/discuss)

1. **#3 gồm cả runtime rotation** (không chỉ quản lý account) — round-robin + cooldown ở trục account. Style-diversity trục provider → #4.
2. **"Account rảnh nhất" = round-robin + cooldown** (không đếm lượt, không quota thật): xoay đều; account dính rate-limit thì cho nghỉ N phút.
3. **Định danh account = nhãn do user đặt** (+ email best-effort nếu CLI phơi ra). Không dùng email làm khoá/đường dẫn.
4. **Mọi login sống trong thư mục config app tự quản, cô lập** khỏi CLI người mua tự dùng — kể cả account đầu. Kéo theo: **#2 phải account-dir-aware ngay từ đầu** (sửa spec+plan #2, đã duyệt gộp), và **runtime spawn set env per-account mỗi lượt**.

## Kiến trúc

### Định tuyến 2 trục

```
mỗi lượt → chọn PROVIDER (chuỗi fallback hiện tại; #4 mở rộng)
             → chọn ACCOUNT trong provider đó  ← #3, phần mới
                 → set env config_dir → spawn CLI chính chủ
```

Account selector **lồng bên trong** việc chọn provider; không đụng logic chọn-provider (giữ nguyên cho #4 gắn round-robin/fusion vào mà không sửa #3).

### Data model — bảng `llm_accounts` (schema v3)

```sql
CREATE TABLE IF NOT EXISTS llm_accounts (
  id          TEXT PRIMARY KEY,                 -- app tự sinh (short id); KHÔNG dùng email
  provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  label       TEXT NOT NULL,                    -- user đặt ("TK chính")
  email       TEXT NOT NULL DEFAULT '',         -- best-effort nếu CLI phơi ra
  config_dir  TEXT NOT NULL,                    -- đường dẫn tuyệt đối tới thư mục config của account
  enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  added_at    TEXT NOT NULL DEFAULT ''
);
```

- **Không cột credential** — phiên nằm trong `config_dir`, đúng luật "không credential qua daemon".
- Khoá `provider_id` sang dòng provider-kind (codex/claude-code); `ON DELETE CASCADE` như `llm_models`.
- Bump `schema_version` → 3 trong `appLLMSchema` (thêm khối `CREATE TABLE IF NOT EXISTS` + `UPDATE app_meta ... '3'`).
- Store methods mới (theo pattern sẵn có): `LLMAccounts(providerID) ([]LLMAccount, error)`, `CreateLLMAccount(a LLMAccount) error`, `DeleteLLMAccount(id string) error`. Kiểu `LLMAccount` phản chiếu cột (không có credential).

### Thư mục config per-account

- Layout: `<dataDir>/accounts/<kind>/<accountId>/` — `<dataDir>` = **thư mục chứa DB SQLite của Portal** (dùng lại resolver data-dir sẵn có của daemon; KHÔNG tự suy diễn `%APPDATA%\ags`). `<kind>` = `codex` | `claude_code`. `accountId` = short id app sinh.
- Helper daemon `accountConfigDir(kind, id) string` dựng path; `os.MkdirAll(dir, 0o700)` lúc connect (như `app_knowledge.go:267`).
- Ánh xạ env lúc spawn (một bảng nhỏ theo kind):
  - codex → `CODEX_HOME=<config_dir>` (engine đã xác nhận auth nằm ở `CODEX_HOME`, comment `app_llm_cli.go:48`).
  - claude-code → `CLAUDE_CONFIG_DIR=<config_dir>` — **tên biến chốt capture-first** lúc test login thật.

### Tái dùng Connect (#2) — và phần #2 phải sửa

- Connect job #2 hiện keyed theo `kind`, dùng dir mặc định. **Sửa**: job nhận đích account `{kind, accountId, configDir}`.
  - `install` giữ **mức máy** (cài CLI một lần cho cả máy, không per-account).
  - `login` / `pollAuth` chạy với **env trỏ vào `configDir`** của account → phiên rơi vào đúng thư mục account.
  - Khi `connected` → đảm bảo dòng provider-kind tồn tại (EnsureProviderForKind như #2) **rồi** `CreateLLMAccount(row)` gắn account.
- #2 single-account = **account đầu tiên**: nút "Connect" của #2 thêm ô **nhãn** (mặc định "Tài khoản 1"); connectManager tạo `accountId` + `configDir` trước khi chạy job.
- **Amend #2** (spec+plan) sẽ sửa cùng lúc viết spec này: connect job + endpoint mang `accountId`/`configDir`; `EnsureProviderForKind` vẫn giữ; thêm bước `CreateLLMAccount` ở `connected`. Thứ tự thực thi vẫn **#2 → #3**.

### Selector runtime

- `accountSelector` sống trên struct `api` (dài hạn, qua các lượt), guard `sync.Mutex`. Mỗi kind giữ: con trỏ round-robin + map `accountID → cooldownUntil`.
- `pick(kind string, accounts []LLMAccount) (LLMAccount, bool)`: trong các account `enabled`, ưu tiên cái **không** cooldown (round-robin đều theo con trỏ); nếu **tất cả** cooldown → chọn cái **hết cooldown sớm nhất**. Trả `false` **chỉ khi 0 account enabled**.
- `penalize(kind, accountID string)`: đặt `cooldownUntil = now + cooldownWindow`.
- Tích hợp tại `cliAdapter.spawn`, ngay **trước** khi build `cmd` (dòng 371): pick account → `cmd.Env = append(os.Environ(), envVarFor(kind)+"="+acc.configDir)`. Sau spawn, nếu lỗi phân loại là **rate-limit** → `penalize`.
- **Seam test** song song `a.run`: adapter nhận hàm `accountEnv func(kind string) (env []string, penalize func(rateLimited bool), ok bool)`; mặc định = selector+store thật, test tiêm giả (dir tạm + penalize no-op) → test selection **không** cần dir/CLI thật.
- `cooldownWindow` = hằng số `const` (khởi điểm 15 phút), `// ponytail:` ceiling "tune nếu cần". State **in-memory**, restart daemon reset con trỏ + cooldown — vô hại (cùng lắm một lần dính rate-limit lại). Không persist (YAGNI).

## Data flow

`GET /llm/providers/{id}` (detail) → liệt kê `LLMAccounts` → panel Kết nối. "Thêm account" → connect #2 (POST vào dir account mới) → poll → `connected` tạo dòng `llm_accounts` → `refresh()` thấy account mới.

Runtime: tin nhắn khách → router chọn provider (chuỗi fallback) → `cliAdapter` cho kind đó → `accountSelector.pick` → set env `config_dir` → spawn CLI chính chủ → lỗi rate-limit thì `penalize`. **Không token/khoá nào đi qua daemon** ở bất kỳ bước nào — phiên nằm trong `config_dir`.

## Error handling (fallback 2 tầng)

- **Provider có ≥1 account enabled** → luôn dùng được: cooldown chỉ **hạ ưu tiên**, không loại cứng khi là lựa chọn duy nhất (một account vừa rate-limit có thể đã hồi; xấu nhất là một lượt phí rồi rơi provider kế theo đường lỗi thường).
- **Provider 0 account enabled** → `pick` trả `false` → adapter trả **lỗi credential loại TIẾP-TỤC-chuỗi** → chuỗi fallback rơi xuống **provider kế** (hành vi cũ). Bảo đảm "không account" **không** map vào loại DỪNG-chuỗi (khác với "CLI chưa cài" ở `app_llm_cli.go:365-367`).
- **Rate-limit của một account** → `penalize` + để router xử như lỗi lượt đó (rơi account khác/provider kế).
- **Xoá account** → `DeleteLLMAccount(id)` + xoá `config_dir` (logout thật, `os.RemoveAll`). Xoá account **cuối** của kind → provider-kind còn "0 kết nối" → card về trạng thái chưa kết nối như fresh (không xoá dòng provider-kind; nó vẫn là mục catalog).
- Lỗi tạo dir / ghi store lúc connect → phase `error` của connect job (thông báo gọn, không lộ đường dẫn nhạy cảm), không tạo account nửa vời.

## Testing

- **Backend Go** (`daemon/app_llm_accounts_test.go`, `store/app_llm_test.go` mở rộng):
  - `accountSelector` table-tests: round-robin xoay đúng; cooldown hạ ưu tiên; **all-cooldown → soonest-expiring**; 0 account enabled → `ok=false`; `penalize` đặt cooldown; thread-safe (chạy `-race`).
  - `cliAdapter` set **đúng env** cho kind qua seam `accountEnv` giả (không spawn CLI thật); rate-limit → gọi penalize; `ok=false` → lỗi loại tiếp-tục-chuỗi.
  - Store `llm_accounts`: `Create/List/Delete`, cascade khi xoá provider, `DeleteLLMAccount` id không tồn tại → `ErrNotFound`.
- **Frontend Portal** (`appmode/tests/providers.test.mjs` mở rộng): panel liệt kê **nhiều** account (nhãn + trạng thái); "Thêm account" mở connect flow; "Xoá" gọi `DELETE` + `refresh()`; card về "0 kết nối" khi hết account. Canary "no API-key literal" **giữ nguyên, không nới**.
- **Capture-first** (checkpoint như #2/Task8, cần môi trường ổn định + đăng nhập thật): tên biến env claude (`CLAUDE_CONFIG_DIR`), `codex login`/`claude` login **vào dir chỉ định** (xác nhận phiên rơi đúng thư mục), và CLI có phơi **email** account script-được không. KHÔNG bịa output.
- **Cổng đóng**: `pwsh scripts/go-check.ps1` (Go all + gofmt) + `npm --prefix appmode test` + full `build-app.ps1` xanh (gồm cổng credential không báo nhầm trên thư mục config account).

## File structure

- **Mới**: `appmode/overlay/internal/daemon/app_llm_accounts.go` — `accountSelector` (pick/penalize), `accountConfigDir`, `envVarFor(kind)`.
- **Mới**: `appmode/overlay/internal/daemon/app_llm_accounts_test.go` — selector + adapter-env tests.
- **Sửa**: `appmode/overlay/internal/store/app_schema.go` — bảng `llm_accounts`, bump `schema_version` → 3.
- **Sửa**: `appmode/overlay/internal/store/app_llm.go` — kiểu `LLMAccount` + `LLMAccounts`/`CreateLLMAccount`/`DeleteLLMAccount`.
- **Sửa**: `appmode/overlay/internal/daemon/app_llm_cli.go` — `spawn` gọi selector + set `cmd.Env`; seam `accountEnv`.
- **Sửa**: `appmode/overlay/internal/daemon/app_llm_connect.go` (#2) — connect job/endpoint mang `accountId`/`configDir`, `connected` gọi `CreateLLMAccount`.
- **Sửa**: `appmode/overlay/internal/webui/static/pages/providers.js` + `portal.css` — panel Kết nối liệt kê account + Thêm/Xoá.
- **Sửa**: `appmode/tests/providers.test.mjs` — test panel multi-account.
- **Sửa (amend #2)**: `.planning/specs/2026-08-07-providers-connect-design.md` + `.planning/plans/2026-08-07-providers-connect.md` — account-dir-aware.

## Rủi ro / câu hỏi mở

- **Login vào dir chỉ định** — cần chắc `codex login` tôn trọng `CODEX_HOME` và `claude` tôn trọng `CLAUDE_CONFIG_DIR` khi spawn nền (không tự rơi về `~/.codex`/`~/.claude`). Rủi ro chính, chốt capture-first. Nếu một CLI **không** cho relocate → account đầu buộc dùng dir mặc định (rơi về phương án bất đối xứng đã loại) — sẽ báo lại làm điểm quyết định.
- **Phân loại rate-limit** — engine phải phân biệt lỗi rate-limit (→ cooldown) với lỗi khác; xem `classifyCLIError`. Nếu taxonomy chưa tách rate-limit riêng, thêm một loại/nhận diện (không nới hợp đồng che stderr). Chốt capture-first với output lỗi thật.
- **"Không account" vs "CLI chưa cài"** — hai cái đều là "credential" nhưng một cái phải TIẾP-TỤC-chuỗi, một cái DỪNG. Cần đọc kỹ classify + router trước khi sửa để không đảo hành vi fallback hiện có.
- **Phụ thuộc #2** — #3 execute được chỉ sau khi #2 (account-aware) đã build. Cùng nhánh, cùng môi trường capture-first — gộp checkpoint đăng nhập của #2 và #3 làm một lần cho gọn.
- **Cổng credential trên thư mục account** — `Assert-AppPackage` quét khoá; thư mục config account (chứa token phiên) **không** được đóng vào gói (là runtime data trên máy khách, sinh sau cài) — xác nhận nó nằm ngoài cây gói.
