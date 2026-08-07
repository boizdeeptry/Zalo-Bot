# Multi-account cho provider subscription — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Cho mỗi provider subscription (Claude Code, OpenAI Codex) đăng nhập nhiều account (mỗi account một phiên CLI cô lập trong thư mục config app tự quản), tự round-robin + cooldown giữa các account mỗi lượt để nhân trần quota — zero-terminal, không credential qua daemon.

**TDD mode:** yes

**Architecture:** Bảng `llm_accounts` (schema v3, không credential) giữ N account/provider. Một `accountSelector` (singleton cấp package, in-memory: con trỏ round-robin + map cooldown, guard mutex) chọn account ở đầu mỗi lượt. `cliAdapter` nhận seam `accountEnv` (song song `a.run`); `spawn` set `cmd.Env` = biến config-dir của account được chọn, và penalize account khi lỗi `rate_limit`. Seam wire trong `appLLMAdapters` (có `a.cfg.Dir` + `a.st` + singleton). "Thêm account" tái dùng luồng connect #2 (đã amend account-dir-aware). Định tuyến 2 trục: chọn provider (chuỗi fallback cũ) → chọn account trong provider (#3); một lượt vẫn chạm đúng một account/provider — nhân quota là qua NHIỀU lượt.

**Tech stack:** Go (`sync`, `os`, `filepath`, `time`, `log/slog`), SQLite qua `store` (pattern `inLLMTx`/`assertOneRow`/`boolInt`); engine seams sẵn có (`cliAdapter.run`, `classifyCLIError`, `checkCLIAuth`, `isFallbackEligible`); ES module Portal (`element`/`requestJSON` + panel connect #2); `node --test` + `dom-harness.mjs`.

**Spec:** `.planning/specs/2026-08-07-providers-multi-account-design.md`

**Research:** skipped — builds on established patterns; no new libraries. Env var `CODEX_HOME` xác nhận (comment `app_llm_cli.go:48`), `CLAUDE_CONFIG_DIR` + login-vào-dir + CLI có phơi email không là capture-first (Task 9, NEEDS-LOGIN).

---

## Execution ordering (ĐỌC TRƯỚC)

Nhánh `feat/cli-subscription-providers`, ship một lần ở cuối. #3 dựng trên #2 (đang tạm dừng). Thứ tự thực thi:

1. **#3 Task 1–4** (store, selector, helpers, adapter-env) — KHÔNG phụ thuộc #2, chạy được ngay với môi trường hiện tại (fake seams, `:memory:`).
2. **#3 Task 5** — amend spec+plan #2 (doc). Sau đó **thực thi #2 (đã amend)** — cần môi trường ổn định + đăng nhập thật (checkpoint của #2). #2 lúc này gọi `store.CreateLLMAccount` (Task 1) khi `connected`.
3. **#3 Task 6–8** (detail body, DELETE endpoint, frontend panel) — phụ thuộc #2 đã build (tái dùng panel connect + `CreateLLMAccount`).
4. **#3 Task 9** capture-first (NEEDS-LOGIN, gộp với checkpoint #2) → **Task 10** cổng đóng.

Fast Go loop: `$env:ZALOBOT_REPO=[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User'); pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO -Run <regex>`. Portal: `npm --prefix appmode test`. (env vars không truyền xuống shell con — nạp ở đầu mỗi shell.)

---

## File structure

- **Sửa** `appmode/overlay/internal/store/app_schema.go` — bảng `llm_accounts`, `schema_version` → 3.
- **Sửa** `appmode/overlay/internal/store/app_llm.go` (+ `app_llm_test.go`, `app_schema_test.go`) — `LLMAccount` + `LLMAccounts`/`CreateLLMAccount`/`DeleteLLMAccount`.
- **Mới** `appmode/overlay/internal/daemon/app_llm_accounts.go` — `accountSelector` (pick/penalize) + singleton, `envVarFor`, `accountConfigDir`, `makeAccountEnv`.
- **Mới** `appmode/overlay/internal/daemon/app_llm_accounts_test.go` — selector + factory + adapter-env tests.
- **Sửa** `appmode/overlay/internal/daemon/app_llm_cli.go` — `cliAdapter.accountEnv` field + `spawn` dùng nó.
- **Sửa** `appmode/overlay/internal/daemon/app_llm_router.go` — `appLLMAdapters` wire `accountEnv` cho codex/claude-code.
- **Sửa** `appmode/overlay/internal/daemon/app_llm_api_shared.go` — `llmProviderBody` kèm `Accounts`.
- **Sửa** `appmode/overlay/internal/daemon/app_llm_api.go` (+ `app_routes.go`) — handler + route + allowlist `DELETE /llm/providers/{id}/accounts/{accountId}`.
- **Sửa** `appmode/overlay/internal/webui/static/pages/providers.js` + `portal.css` (+ `appmode/tests/providers.test.mjs`) — panel Kết nối liệt kê account + Thêm/Xoá.
- **Sửa (amend #2)** `.planning/specs/2026-08-07-providers-connect-design.md` + `.planning/plans/2026-08-07-providers-connect.md`.

---

### Task 1: Bảng `llm_accounts` + store CRUD (schema v3)

**Files:**
- Modify: `appmode/overlay/internal/store/app_schema.go` (const `appLLMSchema`)
- Modify: `appmode/overlay/internal/store/app_llm.go` (thêm type + 3 method)
- Test: `appmode/overlay/internal/store/app_llm_test.go`, `appmode/overlay/internal/store/app_schema_test.go`

**Public behavior to verify:** `CreateLLMAccount` rồi `LLMAccounts(providerID)` trả về đúng account đã tạo; `DeleteLLMAccount` xoá nó; xoá provider cascade xoá account; account KHÔNG mang credential.

- [ ] **Step 1: Viết test đỏ**

  Trong `app_llm_test.go`:
  ```go
  func TestLLMAccountsCRUD(t *testing.T) {
      st := openTestStore(t) // helper sẵn có mở :memory: + migrate; nếu tên khác, dùng bản đang có
      if err := st.CreateLLMAccount(store.LLMAccount{
          ID: "acc1", ProviderID: "codex", Label: "TK chính", ConfigDir: `C:\d\accounts\codex\acc1`, Enabled: true,
      }); err != nil {
          t.Fatalf("CreateLLMAccount = %v; want nil", err)
      }
      got, err := st.LLMAccounts("codex")
      if err != nil || len(got) != 1 || got[0].ID != "acc1" || got[0].Label != "TK chính" {
          t.Fatalf("LLMAccounts = %+v, %v; want 1 account acc1", got, err)
      }
      if err := st.DeleteLLMAccount("acc1"); err != nil {
          t.Fatalf("DeleteLLMAccount = %v; want nil", err)
      }
      if got, _ := st.LLMAccounts("codex"); len(got) != 0 {
          t.Fatalf("after delete LLMAccounts = %+v; want empty", got)
      }
  }

  func TestDeleteProviderCascadesAccounts(t *testing.T) {
      st := openTestStore(t)
      _ = st.CreateLLMProvider(store.LLMProvider{ID: "codex", Name: "OpenAI Codex", Kind: "codex", Enabled: true})
      _ = st.CreateLLMAccount(store.LLMAccount{ID: "a", ProviderID: "codex", Label: "x", ConfigDir: "d", Enabled: true})
      if err := st.DeleteLLMProvider("codex"); err != nil {
          t.Fatalf("DeleteLLMProvider = %v", err)
      }
      if got, _ := st.LLMAccounts("codex"); len(got) != 0 {
          t.Fatalf("accounts survived provider delete: %+v", got)
      }
  }
  ```
  Trong `app_schema_test.go`: cập nhật assert `schema_version` từ `2` → `3` (tìm dòng so `'2'`/`2`).

- [ ] **Step 2: Chạy đỏ** — `... go-check ... -Run 'TestLLMAccounts|TestDeleteProviderCascades|Schema'` → FAIL: `CreateLLMAccount` chưa có / no such table `llm_accounts` / schema_version mismatch.

- [ ] **Step 3: Cài đặt tối thiểu**

  Trong `app_schema.go`, thêm vào cuối chuỗi `appLLMSchema` (TRƯỚC dòng `UPDATE app_meta SET value = '2'`), rồi đổi `'2'` → `'3'`:
  ```sql
  CREATE TABLE IF NOT EXISTS llm_accounts (
    id          TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    email       TEXT NOT NULL DEFAULT '',
    config_dir  TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    added_at    TEXT NOT NULL DEFAULT ''
  );
  ```
  (Ghi chú: khoá ngoại `ON DELETE CASCADE` chỉ là tài liệu vì SQLite tắt enforcement — `DeleteLLMProvider` phải xoá tay account như đã làm với `llm_models`; xem Step 3 bổ sung.)

  Trong `app_llm.go`:
  ```go
  // LLMAccount là một phiên đăng nhập CLI độc lập của một provider subscription.
  // KHÔNG mang credential — phiên nằm trong ConfigDir, không đi qua daemon.
  type LLMAccount struct {
      ID, ProviderID, Label, Email, ConfigDir string
      Enabled                                 bool
      AddedAt                                 *time.Time
  }

  func (s *Store) LLMAccounts(providerID string) ([]LLMAccount, error) {
      rows, err := s.db.Query(`
  SELECT id, provider_id, label, email, config_dir, enabled, added_at
  FROM llm_accounts WHERE provider_id = ? ORDER BY added_at, id`, providerID)
      if err != nil {
          return nil, fmt.Errorf("list llm accounts of %s: %w", providerID, err)
      }
      defer rows.Close()
      var out []LLMAccount
      for rows.Next() {
          var a LLMAccount
          var enabled int
          var addedAt string
          if err := rows.Scan(&a.ID, &a.ProviderID, &a.Label, &a.Email, &a.ConfigDir, &enabled, &addedAt); err != nil {
              return nil, fmt.Errorf("scan llm account of %s: %w", providerID, err)
          }
          a.Enabled = enabled == 1
          if a.AddedAt, err = parseNullableTS(addedAt); err != nil {
              return nil, fmt.Errorf("parse llm account %s added_at: %w", a.ID, err)
          }
          out = append(out, a)
      }
      if err := rows.Err(); err != nil {
          return nil, fmt.Errorf("iterate llm accounts of %s: %w", providerID, err)
      }
      return out, nil
  }

  func (s *Store) CreateLLMAccount(a LLMAccount) error {
      if a.ID == "" || a.ProviderID == "" || a.Label == "" || a.ConfigDir == "" {
          return fmt.Errorf("create llm account: cần id, provider, nhãn và config_dir")
      }
      if _, err := s.db.Exec(`
  INSERT INTO llm_accounts(id, provider_id, label, email, config_dir, enabled, added_at)
  VALUES(?,?,?,?,?,?,?)`, a.ID, a.ProviderID, a.Label, a.Email, a.ConfigDir,
          boolInt(a.Enabled), formatNullableTS(a.AddedAt)); err != nil {
          return fmt.Errorf("create llm account %s: %w", a.ID, err)
      }
      return nil
  }

  func (s *Store) DeleteLLMAccount(id string) error {
      res, err := s.db.Exec(`DELETE FROM llm_accounts WHERE id = ?`, id)
      if err != nil {
          return fmt.Errorf("delete llm account %s: %w", id, err)
      }
      return assertOneRow(res, fmt.Sprintf("delete llm account %s", id))
  }
  ```
  Trong `DeleteLLMProvider` (transaction hiện có), thêm sau lệnh xoá `llm_models`:
  ```go
  _, err = tx.Exec(`DELETE FROM llm_accounts WHERE provider_id = ?`, id)
  return err
  ```

- [ ] **Step 4: Chạy xanh** — `... -Run 'TestLLMAccounts|TestDeleteProviderCascades|Schema'` → PASS.

- [ ] **Step 5: Refactor** — nếu `openTestStore` helper tên khác, khớp theo bản có sẵn; không thêm gì thừa.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/store/app_schema.go appmode/overlay/internal/store/app_llm.go appmode/overlay/internal/store/app_llm_test.go appmode/overlay/internal/store/app_schema_test.go
  git commit -m "feat: llm_accounts table + store CRUD (schema v3)"
  ```

---

### Task 2: `accountSelector` — round-robin + cooldown

**Files:**
- Create: `appmode/overlay/internal/daemon/app_llm_accounts.go`
- Test: `appmode/overlay/internal/daemon/app_llm_accounts_test.go`

**Public behavior to verify:** `pick` xoay vòng qua account enabled; account đang cooldown bị hạ ưu tiên; khi tất cả cooldown chọn cái hết-cooldown-sớm-nhất; 0 account enabled → `ok=false`; an toàn dưới `-race`.

- [ ] **Step 1: Viết test đỏ**
  ```go
  func accs(ids ...string) []store.LLMAccount {
      out := make([]store.LLMAccount, 0, len(ids))
      for _, id := range ids {
          out = append(out, store.LLMAccount{ID: id, ProviderID: "codex", Enabled: true})
      }
      return out
  }

  func TestSelectorRoundRobin(t *testing.T) {
      s := newAccountSelector()
      in := accs("a", "b", "c")
      var got []string
      for i := 0; i < 4; i++ {
          a, ok := s.pick("codex", in)
          if !ok { t.Fatalf("pick %d: ok=false", i) }
          got = append(got, a.ID)
      }
      want := []string{"a", "b", "c", "a"}
      if !slices.Equal(got, want) { t.Fatalf("round-robin = %v; want %v", got, want) }
  }

  func TestSelectorSkipsCooldown(t *testing.T) {
      s := newAccountSelector()
      in := accs("a", "b")
      s.penalize("a")                       // a cooldown
      a, ok := s.pick("codex", in)
      if !ok || a.ID != "b" { t.Fatalf("pick = %q,%v; want b (a cooling)", a.ID, ok) }
  }

  func TestSelectorAllCoolingPicksSoonest(t *testing.T) {
      s := newAccountSelector()
      in := accs("a", "b")
      s.cooldown["a"] = time.Now().Add(time.Minute)   // a hết sớm hơn
      s.cooldown["b"] = time.Now().Add(time.Hour)
      a, ok := s.pick("codex", in)
      if !ok || a.ID != "a" { t.Fatalf("pick = %q,%v; want a (soonest)", a.ID, ok) }
  }

  func TestSelectorNoEnabled(t *testing.T) {
      s := newAccountSelector()
      if _, ok := s.pick("codex", nil); ok { t.Fatalf("pick on 0 accounts: ok=true; want false") }
      dis := []store.LLMAccount{{ID: "x", Enabled: false}}
      if _, ok := s.pick("codex", dis); ok { t.Fatalf("pick on disabled-only: ok=true; want false") }
  }
  ```

- [ ] **Step 2: Chạy đỏ** — `... -Run TestSelector` → FAIL: `newAccountSelector` chưa có.

- [ ] **Step 3: Cài đặt tối thiểu** — tạo `app_llm_accounts.go`:
  ```go
  package daemon

  import (
      "sync"
      "time"

      "agentdc/internal/store"
  )

  // accountCooldownWindow: sau khi một account dính rate-limit, cho nó nghỉ chừng này trước khi
  // lại được ưu tiên. ponytail: hằng số; nâng thành cấu hình nếu vận hành cần tune.
  const accountCooldownWindow = 15 * time.Minute

  // accountSelector chọn account nào của một provider phục vụ một lượt: round-robin qua các
  // account enabled, ưu tiên cái KHÔNG cooldown. State in-memory (con trỏ + cooldown), guard
  // mutex; restart reset — vô hại (cùng lắm một lần dính rate-limit lại).
  type accountSelector struct {
      mu       sync.Mutex
      cursor   map[string]int       // kind → vị trí round-robin kế
      cooldown map[string]time.Time // accountID → thời điểm hết cooldown
  }

  func newAccountSelector() *accountSelector {
      return &accountSelector{cursor: map[string]int{}, cooldown: map[string]time.Time{}}
  }

  // pick trả account cho lượt này. ok=false CHỈ khi không có account enabled nào.
  func (s *accountSelector) pick(kind string, accounts []store.LLMAccount) (store.LLMAccount, bool) {
      s.mu.Lock()
      defer s.mu.Unlock()
      enabled := make([]store.LLMAccount, 0, len(accounts))
      for _, a := range accounts {
          if a.Enabled {
              enabled = append(enabled, a)
          }
      }
      if len(enabled) == 0 {
          return store.LLMAccount{}, false
      }
      now := time.Now()
      start := s.cursor[kind] % len(enabled)
      var soonest store.LLMAccount
      var soonestT time.Time
      for i := 0; i < len(enabled); i++ {
          a := enabled[(start+i)%len(enabled)]
          until, cooling := s.cooldown[a.ID]
          if !cooling || !until.After(now) {
              s.cursor[kind] = (start + i + 1) % len(enabled)
              return a, true
          }
          if soonestT.IsZero() || until.Before(soonestT) {
              soonest, soonestT = a, until
          }
      }
      // Tất cả đang cooldown → cái hết sớm nhất (không bao giờ trả false khi CÓ account enabled).
      s.cursor[kind] = (start + 1) % len(enabled)
      return soonest, true
  }

  // penalize đặt cooldown cho một account vừa dính rate-limit.
  func (s *accountSelector) penalize(accountID string) {
      s.mu.Lock()
      defer s.mu.Unlock()
      s.cooldown[accountID] = time.Now().Add(accountCooldownWindow)
  }

  // accountSel là selector cấp process.
  //
  // ponytail: singleton cấp package vì `api` khai báo ở base repo (build assert git-clean cấm
  // thêm field), và `cliAdapter` dựng mới mỗi lượt nên state không ở đó được. Guard bằng mutex.
  // Nâng cấp: nếu base cho thêm field vào api thì chuyển sang DI qua constructor.
  var accountSel = newAccountSelector()
  ```

- [ ] **Step 4: Chạy xanh** — `... -Run TestSelector` → PASS. Chạy kèm `-race` (go-check đã bật) không báo đua.

- [ ] **Step 5: Refactor** — không cần; giữ nhỏ.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_accounts.go appmode/overlay/internal/daemon/app_llm_accounts_test.go
  git commit -m "feat: accountSelector round-robin + cooldown (in-memory singleton)"
  ```

---

### Task 3: Helpers `envVarFor` / `accountConfigDir` / `makeAccountEnv`

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_accounts.go`
- Test: `appmode/overlay/internal/daemon/app_llm_accounts_test.go`

**Public behavior to verify:** `envVarFor` trả `CODEX_HOME`/`CLAUDE_CONFIG_DIR` cho kind subscription (false cho kind khác); `accountConfigDir` ghép `<dataDir>/accounts/<kind>/<id>`; `makeAccountEnv` (đọc store thật + selector) trả env `<VAR>=<configDir>` của account được chọn + `ok=false` khi 0 account.

- [ ] **Step 1: Viết test đỏ**
  ```go
  func TestEnvVarFor(t *testing.T) {
      for _, tc := range []struct{ kind, want string; ok bool }{
          {"codex", "CODEX_HOME", true},
          {"claude_code", "CLAUDE_CONFIG_DIR", true},
          {"openai", "", false},
      } {
          got, ok := envVarFor(tc.kind)
          if got != tc.want || ok != tc.ok {
              t.Errorf("envVarFor(%q) = %q,%v; want %q,%v", tc.kind, got, ok, tc.want, tc.ok)
          }
      }
  }

  func TestMakeAccountEnvPicksAndFormats(t *testing.T) {
      st := openTestStore(t)
      _ = st.CreateLLMProvider(store.LLMProvider{ID: "codex", Name: "Codex", Kind: "codex", Enabled: true})
      _ = st.CreateLLMAccount(store.LLMAccount{ID: "a1", ProviderID: "codex", Label: "x",
          ConfigDir: `C:\home\accounts\codex\a1`, Enabled: true})
      sel := newAccountSelector()
      envFn := makeAccountEnv(st, sel, "codex", "codex")
      env, penalize, ok := envFn()
      if !ok || penalize == nil {
          t.Fatalf("envFn ok=%v penalize==nil=%v; want true, non-nil", ok, penalize == nil)
      }
      if !slices.Contains(env, `CODEX_HOME=C:\home\accounts\codex\a1`) {
          t.Fatalf("env = %v; want CODEX_HOME=...a1", env)
      }
  }

  func TestMakeAccountEnvNoAccount(t *testing.T) {
      st := openTestStore(t)
      envFn := makeAccountEnv(st, newAccountSelector(), "codex", "codex")
      if _, _, ok := envFn(); ok {
          t.Fatalf("envFn ok=true with 0 accounts; want false")
      }
  }
  ```

- [ ] **Step 2: Chạy đỏ** — `... -Run 'TestEnvVarFor|TestMakeAccountEnv'` → FAIL: chưa có hàm.

- [ ] **Step 3: Cài đặt tối thiểu** — thêm vào `app_llm_accounts.go` (thêm import `os`, `path/filepath`, `log/slog` nếu cần):
  ```go
  // envVarFor trả tên biến môi trường trỏ thư mục config cho một kind subscription.
  // CODEX_HOME xác nhận (app_llm_cli.go:48). CLAUDE_CONFIG_DIR chốt capture-first (Task 9).
  func envVarFor(kind string) (string, bool) {
      switch kind {
      case "codex":
          return "CODEX_HOME", true
      case "claude_code":
          return "CLAUDE_CONFIG_DIR", true
      }
      return "", false
  }

  // accountConfigDir dựng thư mục config của một account, cạnh DB Portal (dataDir = cfg.Dir).
  func accountConfigDir(dataDir, kind, id string) string {
      return filepath.Join(dataDir, "accounts", kind, id)
  }

  // makeAccountEnv trả một closure cho cliAdapter: đọc account của kind, chọn qua selector, và
  // trả env <VAR>=<configDir> + hàm penalize account đó. ok=false khi kind không subscription
  // hoặc 0 account enabled — adapter khi đó trả credential (DỪNG chuỗi, xem spec Error handling).
  func makeAccountEnv(st *store.Store, sel *accountSelector, providerID, kind string) func() ([]string, func(rateLimited bool), bool) {
      return func() ([]string, func(bool), bool) {
          varName, isSub := envVarFor(kind)
          if !isSub {
              return nil, nil, false
          }
          accounts, err := st.LLMAccounts(providerID)
          if err != nil {
              return nil, nil, false
          }
          acc, ok := sel.pick(kind, accounts)
          if !ok {
              return nil, nil, false
          }
          env := append(os.Environ(), varName+"="+acc.ConfigDir)
          penalize := func(rateLimited bool) {
              if rateLimited {
                  sel.penalize(acc.ID)
              }
          }
          return env, penalize, true
      }
  }
  ```

- [ ] **Step 4: Chạy xanh** — `... -Run 'TestEnvVarFor|TestMakeAccountEnv'` → PASS.

- [ ] **Step 5: Refactor** — không.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_accounts.go appmode/overlay/internal/daemon/app_llm_accounts_test.go
  git commit -m "feat: account env helpers (envVarFor, accountConfigDir, makeAccountEnv)"
  ```

---

### Task 4: `cliAdapter.accountEnv` seam + wire vào spawn & appLLMAdapters

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_cli.go` (`cliAdapter` struct + `spawn`)
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go` (`appLLMAdapters` wiring)
- Test: `appmode/overlay/internal/daemon/app_llm_accounts_test.go`

**Public behavior to verify:** khi `accountEnv` trả `ok=true`, `spawn` set `cmd.Env` chứa biến config-dir; khi trả `ok=false`, `Generate` trả `llmErrorCredential` (DỪNG chuỗi); khi CLI trả lỗi `rate_limit`, `spawn` gọi `penalize(true)`.

- [ ] **Step 1: Viết test đỏ** (dùng seam `run` để không spawn CLI thật; kiểm env qua một `run` ghi lại):
  ```go
  func TestSpawnSetsAccountEnvAndPenalizesOnRateLimit(t *testing.T) {
      var penalized bool
      a := newCLIAdapter(cliDescriptors["codex"], "codex", slog.New(slog.DiscardHandler))
      a.accountEnv = func() ([]string, func(bool), bool) {
          return []string{`CODEX_HOME=C:\d\a1`}, func(rl bool) { penalized = rl }, true
      }
      // run giả trả lỗi rate_limit (chuỗi "usage limit" → classifyCLIError = rate_limit).
      a.run = func(ctx context.Context, argv []string, stdin []byte) ([]byte, error) {
          return nil, &cliExit{err: errors.New("x"), stderr: "usage limit reached"}
      }
      _, err := a.Generate(context.Background(), llmRequest{Prompt: "hi", Model: "gpt-5.6-terra"}, nil)
      if llmErrorKindOf(err) != llmErrorRateLimit {
          t.Fatalf("kind = %v; want rate_limit", llmErrorKindOf(err))
      }
      if !penalized {
          t.Fatalf("penalize không được gọi khi rate_limit")
      }
  }

  func TestGenerateNoAccountIsCredential(t *testing.T) {
      a := newCLIAdapter(cliDescriptors["codex"], "codex", slog.New(slog.DiscardHandler))
      a.accountEnv = func() ([]string, func(bool), bool) { return nil, nil, false }
      _, err := a.Generate(context.Background(), llmRequest{Prompt: "hi", Model: "gpt-5.6-terra"}, nil)
      if llmErrorKindOf(err) != llmErrorCredential {
          t.Fatalf("kind = %v; want credential (0 account = chưa đăng nhập)", llmErrorKindOf(err))
      }
  }
  ```
  (Nếu `a.run` bỏ qua env nên không kiểm được `cmd.Env` trực tiếp: test trên kiểm hành vi quan sát được — penalize + kind. Việc set `cmd.Env` được kiểm gián tiếp ở Step 3 qua nhánh `a.run == nil`, và ghim thật ở Task 9.)

- [ ] **Step 2: Chạy đỏ** — `... -Run 'TestSpawnSetsAccountEnv|TestGenerateNoAccount'` → FAIL: field `accountEnv` chưa có.

- [ ] **Step 3: Cài đặt tối thiểu**

  Trong `app_llm_cli.go`, thêm field vào `cliAdapter`:
  ```go
  type cliAdapter struct {
      d          cliDescriptor
      providerID string
      logger     *slog.Logger
      run        func(ctx context.Context, argv []string, stdin []byte) ([]byte, error)
      // accountEnv chọn account cho lượt này (round-robin+cooldown) và trả env <VAR>=<configDir>
      // + penalize. nil = không multi-account (giữ hành vi cũ: env mặc định của máy). ok=false =
      // 0 account enabled → spawn trả credential (DỪNG chuỗi).
      accountEnv func() (env []string, penalize func(rateLimited bool), ok bool)
  }
  ```
  Sửa `Generate` để bắt penalize: đổi `out, err := a.spawn(ctx, argv, stdin)` sang cho `spawn` nhận penalize và gọi khi kind==rate_limit. Cách gọn nhất — set env + lấy penalize TRONG `spawn`, và trả penalize ra để `Generate` gọi sau khi phân loại:
  ```go
  func (a *cliAdapter) Generate(ctx context.Context, req llmRequest, credential []byte) (llmResponse, error) {
      argv := buildCLIArgv(a.d, req)
      var stdin []byte
      if a.d.promptViaStdin {
          stdin = []byte(req.Prompt)
      }
      out, penalize, err := a.spawn(ctx, argv, stdin)
      if err != nil {
          var exit *cliExit
          if errors.As(err, &exit) {
              kind := classifyCLIError(exit.stderr, false)
              if penalize != nil {
                  penalize(kind == llmErrorRateLimit)
              }
              return llmResponse{}, newLLMError(kind, err, "%s: gọi CLI hỏng", a.d.kind)
          }
          return llmResponse{}, err
      }
      return textOrUpstream(a.d.kind+" generate", parseCLIAnswer(a.d, out))
  }
  ```
  Sửa `spawn` để dựng env qua seam và trả penalize. QUAN TRỌNG: khối `accountEnv` phải chạy TRƯỚC
  nhánh `a.run` — nếu không, khi test tiêm cả `a.run` lẫn `a.accountEnv` thì `penalize` không được
  nối và không bao giờ gọi:
  ```go
  func (a *cliAdapter) spawn(ctx context.Context, argv []string, stdin []byte) ([]byte, func(bool), error) {
      // accountEnv TRƯỚC nhánh a.run: penalize phải có ở CẢ đường test (a.run) lẫn đường thật.
      var env []string
      var penalize func(bool)
      if a.accountEnv != nil {
          e, p, ok := a.accountEnv()
          if !ok {
              // 0 account enabled = chưa đăng nhập → credential (DỪNG chuỗi), y như not-installed.
              return nil, nil, newLLMError(llmErrorCredential, nil, "%s: chưa có tài khoản nào đăng nhập", a.d.kind)
          }
          env, penalize = e, p
      }
      if a.run != nil {
          out, err := a.run(ctx, argv, stdin)
          return out, penalize, err
      }
      program, prefixArgs, err := resolveCLIProgram(a.d)
      if err != nil {
          return nil, penalize, newLLMError(classifyCLIError("", true), nil, "%s: CLI chưa cài", a.d.kind)
      }
      cmd := exec.Command(program, append(prefixArgs, argv...)...)
      if env != nil {
          cmd.Env = env
      }
      out, err := runCLIProcess(ctx, cmd, stdin, nil, a.logger)
      return out, penalize, err
  }
  ```
  (Các test router/attachment hiện có set `a.run` nhưng KHÔNG set `a.accountEnv` → khối accountEnv bị bỏ qua, `penalize=nil`, `Generate` bỏ qua — hành vi cũ không đổi. Nếu có caller khác của `spawn`, cập nhật chữ ký; grep `\.spawn(` trước khi build.)

  Trong `app_llm_router.go`, tại `appLLMAdapters` (method trên `*api`): sau khi dựng adapter cho codex/claude-code, wire `accountEnv`. Đọc thân hàm hiện có; với mỗi adapter CLI subscription:
  ```go
  if ca, ok := adapter.(*cliAdapter); ok {
      if _, isSub := envVarFor(kind); isSub {
          ca.accountEnv = makeAccountEnv(a.st, accountSel, providerID, kind)
      }
  }
  ```
  (claude-code hiện chạy qua `runClaude`, chưa qua cliAdapter cho tới Task 11 của engine — nên bước wiring này thực tế chỉ chạm `codex` ở #3; để nhánh `claude_code` sẵn cho khi Task 11 gộp. Ghi chú rõ trong comment.)

- [ ] **Step 4: Chạy xanh** — `... -Run 'TestSpawn|TestGenerateNoAccount|TestAttachment|Router'` → PASS (bao gồm test router cũ, để chắc chữ ký `spawn` mới không vỡ đường có sẵn).

- [ ] **Step 5: Refactor** — nếu `spawn` có caller khác, khớp chữ ký; giữ comment giải thích 3 giá trị trả.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_cli.go appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_accounts_test.go
  git commit -m "feat: cliAdapter.accountEnv seam — per-account env + rate-limit penalize"
  ```

---

### Task 5: Amend spec + plan #2 (account-dir-aware) — doc

**Files:**
- Modify: `.planning/specs/2026-08-07-providers-connect-design.md`
- Modify: `.planning/plans/2026-08-07-providers-connect.md`

**Reason đây là task doc:** #2 chưa build; sửa spec/plan #2 để khi thực thi, #2 dựng connect account-aware ngay từ đầu (tránh làm-rồi-refactor). Không có code/test ở task này.

- [ ] **Step 1: Sửa spec #2** — thêm vào `## Kiến trúc`/`## Non-goals` của spec #2:
  - Connect job mang đích account `{kind, accountId, configDir}`; `install` mức máy, `login`/`pollAuth` chạy với env `<VAR>=<configDir>` (dùng `envVarFor`+`accountConfigDir` của #3).
  - `POST /llm/providers/{kind}/connect` nhận body `{label}`; connectManager sinh `accountId` + `configDir=accountConfigDir(cfg.Dir, kind, accountId)`, `os.MkdirAll(0o700)` trước khi chạy.
  - Khi `connected`: `EnsureProviderForKind(kind)` **rồi** `store.CreateLLMAccount(LLMAccount{ID:accountId, ProviderID, Label:label, ConfigDir, Enabled:true, AddedAt:now})`.
  - Non-goal cũ "một account" → đổi: #2 tạo account ĐẦU; nhiều account là #3 (cùng cơ chế, khác dir).

- [ ] **Step 2: Sửa plan #2** — trong các task connect của plan #2: chữ ký `connectRunner.login`/`connectManager.start` nhận `configDir`; endpoint đọc `label`; task "connected" gọi `CreateLLMAccount`; thêm phụ thuộc "cần `store.LLMAccount`/`CreateLLMAccount` (từ plan #3 Task 1) — chạy #3 Task 1 trước #2". Cập nhật header plan #2: execute sau #3 Task 1–4.

- [ ] **Step 3: Commit**
  ```bash
  git add .planning/specs/2026-08-07-providers-connect-design.md .planning/plans/2026-08-07-providers-connect.md
  git commit -m "docs: amend #2 connect to be account-dir-aware (folds into #3 multi-account)"
  ```

---

### Task 6: `llmProviderBody` kèm danh sách account

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_api_shared.go` (`llmProviderBody` + builder)
- Test: `appmode/overlay/internal/daemon/app_llm_api_test.go`

**Public behavior to verify:** `GET /llm/providers/{id}` cho một provider subscription trả kèm mảng `accounts` (id, label, email, enabled); provider API-key trả `accounts` rỗng/vắng.

- [ ] **Step 1: Viết test đỏ**
  ```go
  func TestProviderBodyIncludesAccounts(t *testing.T) {
      a := newTestAPI(t) // helper sẵn có; nếu khác tên, dùng bản đang dùng ở app_llm_api_test.go
      _ = a.st.CreateLLMProvider(store.LLMProvider{ID: "codex", Name: "Codex", Kind: "codex", Enabled: true})
      _ = a.st.CreateLLMAccount(store.LLMAccount{ID: "a1", ProviderID: "codex", Label: "TK chính", Enabled: true, ConfigDir: "d"})
      body, err := a.llmProviderBody(store.LLMProvider{ID: "codex", Name: "Codex", Kind: "codex", Enabled: true})
      if err != nil { t.Fatalf("llmProviderBody = %v", err) }
      if len(body.Accounts) != 1 || body.Accounts[0].ID != "a1" || body.Accounts[0].Label != "TK chính" {
          t.Fatalf("body.Accounts = %+v; want 1 account a1", body.Accounts)
      }
  }
  ```

- [ ] **Step 2: Chạy đỏ** — `... -Run TestProviderBodyIncludesAccounts` → FAIL: field `Accounts` chưa có.

- [ ] **Step 3: Cài đặt tối thiểu** — trong `app_llm_api_shared.go`:
  ```go
  type llmAccountBody struct {
      ID      string `json:"id"`
      Label   string `json:"label"`
      Email   string `json:"email,omitempty"`
      Enabled bool   `json:"enabled"`
  }
  ```
  Thêm `Accounts []llmAccountBody `json:"accounts,omitempty"`` vào struct `llmProviderBody`. Trong `llmProviderBody(p)`:
  ```go
  accounts, err := a.st.LLMAccounts(p.ID)
  if err != nil {
      return llmProviderBody{}, fmt.Errorf("đọc account của %s: %w", p.ID, err)
  }
  for _, ac := range accounts {
      body.Accounts = append(body.Accounts, llmAccountBody{
          ID: ac.ID, Label: ac.Label, Email: ac.Email, Enabled: ac.Enabled,
      })
  }
  ```
  (Không phơi `config_dir` ra Portal — nó là đường dẫn nội bộ.)

- [ ] **Step 4: Chạy xanh** — `... -Run 'TestProviderBody|LLMProvider'` → PASS.

- [ ] **Step 5: Refactor** — không.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_api_shared.go appmode/overlay/internal/daemon/app_llm_api_test.go
  git commit -m "feat: provider detail body carries account list"
  ```

---

### Task 7: Endpoint `DELETE /llm/providers/{id}/accounts/{accountId}`

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_api.go` (handler)
- Modify: `appmode/overlay/internal/daemon/app_routes.go` (route + allowlist)
- Test: `appmode/overlay/internal/daemon/app_llm_api_test.go`

**Public behavior to verify:** `DELETE .../accounts/{id}` xoá dòng account + `os.RemoveAll(config_dir)`, trả 204/200; account không tồn tại → 404.

- [ ] **Step 1: Viết test đỏ**
  ```go
  func TestDeleteAccountRemovesRowAndDir(t *testing.T) {
      a := newTestAPI(t)
      dir := t.TempDir()
      _ = a.st.CreateLLMProvider(store.LLMProvider{ID: "codex", Name: "Codex", Kind: "codex", Enabled: true})
      _ = a.st.CreateLLMAccount(store.LLMAccount{ID: "a1", ProviderID: "codex", Label: "x", ConfigDir: dir, Enabled: true})
      req := httptest.NewRequest("DELETE", "/llm/providers/codex/accounts/a1", nil)
      req.SetPathValue("id", "codex"); req.SetPathValue("accountId", "a1")
      w := httptest.NewRecorder()
      a.handleLLMAccountDelete(w, req)
      if w.Code != http.StatusNoContent { t.Fatalf("code = %d; want 204", w.Code) }
      if _, err := os.Stat(dir); !os.IsNotExist(err) { t.Fatalf("config_dir vẫn còn: %v", err) }
      if got, _ := a.st.LLMAccounts("codex"); len(got) != 0 { t.Fatalf("row vẫn còn: %+v", got) }
  }
  ```

- [ ] **Step 2: Chạy đỏ** — `... -Run TestDeleteAccount` → FAIL: `handleLLMAccountDelete` chưa có.

- [ ] **Step 3: Cài đặt tối thiểu** — trong `app_llm_api.go` (theo pattern handler sẵn có — `r.PathValue`, `a.writeJSON`/`a.writeLLMErr`):
  ```go
  func (a *api) handleLLMAccountDelete(w http.ResponseWriter, r *http.Request) {
      providerID := r.PathValue("id")
      accountID := r.PathValue("accountId")
      // Đọc config_dir trước khi xoá dòng — sau khi xoá thì không còn đường lấy path để dọn.
      accounts, err := a.st.LLMAccounts(providerID)
      if err != nil {
          a.writeLLMErr(w, http.StatusInternalServerError, "ACCOUNT_READ", "không đọc được tài khoản")
          return
      }
      var dir string
      var found bool
      for _, ac := range accounts {
          if ac.ID == accountID {
              dir, found = ac.ConfigDir, true
              break
          }
      }
      if !found {
          a.writeLLMErr(w, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "không có tài khoản này")
          return
      }
      if err := a.st.DeleteLLMAccount(accountID); err != nil {
          a.writeLLMErr(w, http.StatusInternalServerError, "ACCOUNT_DELETE", "không xoá được tài khoản")
          return
      }
      // Dọn phiên (logout thật). Lỗi dọn dir KHÔNG chặn: dòng đã xoá là nguồn sự thật; một thư mục
      // mồ côi tệ hơn một dòng ma. Log rồi tiếp.
      if err := os.RemoveAll(dir); err != nil {
          a.logger.Warn("xoá thư mục config account thất bại", "account", accountID, "err", err)
      }
      w.WriteHeader(http.StatusNoContent)
  }
  ```

  Trong `app_routes.go`: thêm `"DELETE /llm/providers/{id}/accounts/{accountId}"` vào `appPortalRoutePatterns`, và trong `registerAppRoutes`:
  ```go
  mux.Handle("DELETE /llm/providers/{id}/accounts/{accountId}", a.auth(a.handleLLMAccountDelete))
  ```

- [ ] **Step 4: Chạy xanh** — `... -Run 'TestDeleteAccount|Route'` → PASS.

- [ ] **Step 5: Refactor** — nếu tên helper viết-lỗi khác (`writeLLMErr` vs `writeLLMProviderErr`), khớp bản có sẵn.

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/daemon/app_llm_api.go appmode/overlay/internal/daemon/app_routes.go appmode/overlay/internal/daemon/app_llm_api_test.go
  git commit -m "feat: DELETE account endpoint (row + config dir)"
  ```

---

### Task 8: Frontend — panel Kết nối liệt kê account + Thêm/Xoá

**Files:**
- Modify: `appmode/overlay/internal/webui/static/pages/providers.js`
- Modify: `appmode/overlay/internal/webui/static/portal.css`
- Test: `appmode/tests/providers.test.mjs`

**Public behavior to verify:** trang chi tiết một provider subscription liệt kê các account từ `body.accounts` (nhãn + trạng thái); nút "Xoá" gọi `DELETE .../accounts/{id}` rồi `refresh()`; nút "Thêm account" mở luồng connect #2; provider 0 account hiện "Chưa có tài khoản".

- [ ] **Step 1: Viết test đỏ** (`providers.test.mjs`, dùng `installDOM`/`find` + mock `request`):
  ```js
  test("detail liệt kê account và Xoá gọi DELETE", async () => {
      const calls = [];
      const request = async (method, path) => {
          calls.push(`${method} ${path}`);
          if (method === "GET" && path === "/llm/providers/codex") {
              return { id: "codex", name: "OpenAI Codex", kind: "codex", enabled: true,
                       accounts: [{ id: "a1", label: "TK chính", enabled: true }] };
          }
          if (method === "GET" && path === "/llm/providers") {
              return { providers: [{ id: "codex", name: "OpenAI Codex", kind: "codex" }] };
          }
          return {};
      };
      const dom = installDOM();
      const page = createProvidersPage({ request });
      page.mount(dom.container);
      await openDetail(dom, "codex"); // helper: điều hướng gallery→detail (đã có ở test UI#1)
      assert.ok(text(dom.container).includes("TK chính"), "hiện nhãn account");
      await clickButton(dom, "Xoá");
      assert.ok(calls.includes("DELETE /llm/providers/codex/accounts/a1"), "gọi DELETE account");
  });
  ```
  (Khớp helper `openDetail`/`clickButton` theo bản test UI#1 hiện có; nếu chưa có, dùng `find`/`computer`-tương-đương trong dom-harness.)

- [ ] **Step 2: Chạy đỏ** — `npm --prefix appmode test` → FAIL: panel chưa render account / chưa gọi DELETE.

- [ ] **Step 3: Cài đặt tối thiểu** — trong `providers.js`, mở rộng `renderConnections` (từ UI#1): với provider subscription, đọc `provider.accounts`:
  - Mỗi account → một hàng `pv-account` (nhãn + email nếu có + chấm trạng thái + nút "Xoá").
  - "Xoá" → `await request("DELETE", `/llm/providers/${provider.id}/accounts/${acc.id}`)` rồi `refresh()`.
  - Nút "Thêm account" → gọi luồng connect #2 (hàm `startConnect(kind)` do #2 tạo trong providers.js) với `kind` của provider; connect panel + poll của #2; xong `refresh()`.
  - 0 account → dòng "Chưa có tài khoản — bấm Thêm account để đăng nhập."
  CSS `.pv-account`, `.pv-account-status`, nút Xoá trong `portal.css` dưới `.providers-page` (theme tối, khớp `pv-*` sẵn có).

- [ ] **Step 4: Chạy xanh** — `npm --prefix appmode test` → PASS. Canary "no API-key literal" vẫn xanh (không nới).

- [ ] **Step 5: Refactor** — tách hàm render account nếu `renderConnections` vượt ~40 dòng (giữ file < 800).

- [ ] **Step 6: Commit**
  ```bash
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/overlay/internal/webui/static/portal.css appmode/tests/providers.test.mjs
  git commit -m "feat: connections panel lists accounts + add/remove"
  ```

---

### Task 9: Capture-first checkpoint (NEEDS-LOGIN)

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_llm_accounts.go` (chỉ nếu capture đổi tên biến/hành vi)
- Modify: (có thể) `.planning/specs/...multi-account-design.md` ghi kết quả capture

**Reason:** Ba giả định phải kiểm với CLI thật + đăng nhập thật (như #2 Task 5 / engine Task 8). KHÔNG bịa output. Cần môi trường ổn định (node v22, codex+claude cài lại & đăng nhập).

- [ ] **Step 1: Xác minh env var + login-vào-dir**
  - Đặt `CODEX_HOME=<dir tạm>` rồi `codex login` → xác nhận phiên rơi vào `<dir tạm>` (không phải `~/.codex`). Ghi lại lệnh thật.
  - Đặt `CLAUDE_CONFIG_DIR=<dir tạm>` rồi `claude setup-token`/login → xác nhận phiên rơi vào `<dir tạm>`. **Chốt tên biến** (`CLAUDE_CONFIG_DIR` — nếu sai, sửa `envVarFor`).
  - Nếu một CLI KHÔNG tôn trọng biến → dừng, báo user (điểm quyết định: account đầu buộc dùng dir mặc định — xem Rủi ro spec).

- [ ] **Step 2: Xác minh CLI có phơi email không**
  - Sau khi login, chạy lệnh trạng thái (`codex ... `, `claude auth status --json`) xem có field email/account script-được. Nếu có → điền `LLMAccount.Email` lúc `connected` (#2). Nếu không → để trống (nhãn user là đủ, đã chốt).

- [ ] **Step 3: Ghi kết quả** vào spec (mục capture) + sửa `envVarFor`/`makeAccountEnv` nếu cần. Commit:
  ```bash
  git add -A
  git commit -m "chore: pin account env vars + login-into-dir from real capture (#3)"
  ```

---

### Task 10: Cổng đóng toàn phần + STATE

**Files:**
- Modify: `.planning/STATE.md`

**Reason đây là task verify:** chạy toàn bộ cổng như UI#1/#2 để chắc #3 (và tương tác với #2) không vỡ gì.

- [ ] **Step 1: Go + Portal**
  Run: `$env:ZALOBOT_REPO=[Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User'); pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO` — Expected: tất cả package Go PASS + gofmt sạch.
  Run: `npm --prefix appmode test` — Expected: toàn bộ Portal test PASS, canary API-key literal PASS.

- [ ] **Step 2: Build gói đầy đủ**
  Run: `$env:ZALOBOT_PERSONA=[Environment]::GetEnvironmentVariable('ZALOBOT_PERSONA','User'); pwsh -NoProfile -File .\scripts\build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out <temp>` — Expected: build xanh; `Assert-AppPackage` pass; **xác nhận thư mục `accounts/` KHÔNG bị đóng vào gói** (là runtime data cạnh DB, sinh trên máy khách — không nằm trong cây gói).

- [ ] **Step 3: Cập nhật STATE** — `step: ship` (nếu #2 cũng đã xong) hoặc ghi #3 XONG, chờ #4. Commit:
  ```bash
  git add .planning/STATE.md
  git commit -m "docs: #3 multi-account done, gates green"
  ```
