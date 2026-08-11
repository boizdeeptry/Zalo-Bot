# Claude connect-in-Portal + multi-account — implementation plan

> **For automated executors:** invoke `:subagent-driven-development`. Steps use `- [ ]` checkboxes.

**Goal:** Buyers add Claude account(s) via the Portal (browser-OAuth, zero-terminal); several Claude logins round-robin per turn, each isolated in its own `CLAUDE_CONFIG_DIR`. Unify kind `claude_code`→`claude-code`.

**TDD mode:** yes

**Architecture:** Keep `runClaude`/`execZaloRunner` as Claude's agentic execution path (do NOT merge into `cliAdapter` — it would lose `--add-dir` KB access). Add: (Part 1) a Claude branch in the existing #2 connect state-machine — `claude auth login --claudeai` prints an OAuth URL to stdout + completes via localhost callback + exits 0; (Part 2) thread a per-account `CLAUDE_CONFIG_DIR` into the base `execZaloRunner`, selected by the existing `accountSel`.

**Spec:** `.planning/specs/2026-08-08-claude-connect-design.md` (capture-first VERIFIED with a real login).

**Repos:** overlay `Zalo-Bot/appmode/overlay/internal/...` (module `agentdc`) AND — for Part 2 only — the **base** `AgentDC/internal/daemon/duty.go` (first base-repo change; committed to AgentDC's own git).

**Test loops:** Go `pwsh -NoProfile -File .\scripts\go-check.ps1 -Repo $env:ZALOBOT_REPO [-Run <re>]`; Portal `npm --prefix appmode test`; full gate `build-app.ps1`. Env vars don't propagate — load `ZALOBOT_REPO`/`ZALOBOT_PERSONA` at the top of every shell.

**Baseline:** branch `feat/claude-cliadapter` off `main` @ `8b92c57`; head `c6c554e` (docs). Suite green (89-commit main). Logged-in capture dir `C:\Users\Admin\AppData\Local\zalo-claude-capture` (`congnghe@midu.vn`) available for Part-2 E2E.

**Verified login facts (from capture-first):** `claude auth login --claudeai` stdout →
`Opening browser to sign in…` / `If the browser didn't open, visit: <URL>` / `Paste code here if prompted >`; on authorize → `Login successful.` + **exit 0**. `claude auth status --json` → `{loggedIn, email, …}`.

---

## File structure

| File | Task | Change |
|------|------|--------|
| `store/app_schema.go` | CC1 | seed kind `claude_code`→`claude-code` + idempotent UPDATE |
| `daemon/app_llm_accounts.go` | CC1 | `envVarFor` `claude_code`→`claude-code` |
| `store/app_schema_test.go`, `store/app_llm_test.go`, `daemon/app_llm_accounts_test.go` | CC1 | fix pins |
| `daemon/app_llm_connect.go` | CC2 | Claude branch (login/detect/install/pollAuth/label), `subscriptionKinds`+=claude-code |
| `daemon/app_llm_connect_test.go` | CC2 | claude login URL scan + fake state machine |
| `webui/static/pages/providers.js` | CC3 | `CONNECTABLE_KINDS`+=claude-code (panel already handles url-no-code) |
| `appmode/tests/providers.test.mjs` | CC3 | claude connectable + url-no-code render |
| **`AgentDC/internal/daemon/duty.go`** | CC4 | **base**: `zaloConfig.ConfigDir` + append `CLAUDE_CONFIG_DIR` |
| **`AgentDC/internal/daemon/duty_test.go`** | CC4 | base test |
| `daemon/app_llm_router.go` | CC5 | `appClaudeRunner` selects claude-code account |
| `daemon/app_llm_router_test.go` | CC5 | selection test |

---

### Task CC1: Kind unification (`claude_code` → `claude-code`)

**Files:** `store/app_schema.go`, `daemon/app_llm_accounts.go`, + fix pins in `store/app_schema_test.go`, `store/app_llm_test.go`, `daemon/app_llm_accounts_test.go`.

**Behavior:** the seeded Claude provider has `kind='claude-code'` (hyphen) on both fresh installs and upgrades; `envVarFor("claude-code")` returns `CLAUDE_CONFIG_DIR`.

- [ ] **Step 1: failing tests.** In `app_schema_test.go` add:
  ```go
  func TestMigrateAppUnifiesClaudeKind(t *testing.T) {
      db := openMigratedAppDB(t) // same helper the other migrate tests use
      // simulate a pre-existing underscore row (upgrade path)
      if _, err := db.Exec(`UPDATE llm_providers SET kind='claude_code' WHERE id='claude-code'`); err != nil { t.Fatal(err) }
      if err := migrateApp(db); err != nil { t.Fatalf("re-migrate: %v", err) }
      var kind string
      if err := db.QueryRow(`SELECT kind FROM llm_providers WHERE id='claude-code'`).Scan(&kind); err != nil { t.Fatal(err) }
      if kind != "claude-code" { t.Errorf("claude kind = %q; want claude-code", kind) }
  }
  ```
  And add an `envVarFor` test in `app_llm_accounts_test.go` (or wherever `envVarFor` is tested): `envVarFor("claude-code")` → `("CLAUDE_CONFIG_DIR", true)`; `envVarFor("claude_code")` → `("", false)`.

- [ ] **Step 2: run — FAIL** (`-Run 'MigrateApp|envVarFor|EnvVar'`): kind is still `claude_code`; envVarFor still matches underscore.

- [ ] **Step 3: implement.**
  - `store/app_schema.go`: change the seed `INSERT … VALUES('claude-code','Claude Code','claude_code',1,1)` → `'claude-code'`; add before the version line: `UPDATE llm_providers SET kind='claude-code' WHERE id='claude-code' AND kind='claude_code';` (idempotent upgrade).
  - `daemon/app_llm_accounts.go:82`: `case "claude_code":` → `case "claude-code":`.
  - Fix the pins: `app_schema_test.go` / `app_llm_test.go` assertions expecting `claude_code` → `claude-code`; `app_llm_accounts_test.go:77` envVarFor pin → hyphen.
  - Check `app_llm_api_shared.go` create-guard + `app_llm_api_test.go:396-400`: they intentionally block creating a `claude_code`/`claude-code` provider via the API (connect owns it). Update the guard string to `claude-code` so the block still holds; keep the test asserting rejection (update its kind string).

- [ ] **Step 4: run — PASS**, then FULL go-check (kind rename can ripple).

- [ ] **Step 5: refactor** — grep the overlay for any remaining `claude_code` (underscore) string; only the base-repo `duty.go` may keep its own (unrelated) usages — do NOT touch base here.

- [ ] **Step 6: commit** `feat(store): unify Claude provider kind claude_code→claude-code`.

---

### Task CC2: Claude connect runner (browser-OAuth) + `subscriptionKinds`

**Files:** `daemon/app_llm_connect.go`, `daemon/app_llm_connect_test.go`.

**Behavior:** connecting kind `claude-code` spawns `claude auth login --claudeai` (env `CLAUDE_CONFIG_DIR=<account dir>`), surfaces the printed OAuth URL (no device-code), waits for exit 0, and labels the account with the email from `auth status`.

- [ ] **Step 1: failing tests.**
  ```go
  func TestScanClaudeLoginURL(t *testing.T) {
      sample := "Opening browser to sign in…\n" +
          "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&client_id=abc&state=xyz\n" +
          "Paste code here if prompted > "
      url := scanClaudeLoginURL(strings.NewReader(sample))
      if url != "https://claude.com/cai/oauth/authorize?code=true&client_id=abc&state=xyz" {
          t.Errorf("scanClaudeLoginURL = %q", url)
      }
  }
  ```
  Plus a state-machine test with a fake runner whose `login` returns a URL + a `wait()` that returns nil (exit 0), asserting the job reaches `connected` and `CreateLLMAccount` is called with the fake email label, and `connectState` carries **no** `config_dir` (canary).

- [ ] **Step 2: run — FAIL** (undefined `scanClaudeLoginURL`).

- [ ] **Step 3: implement** in `app_llm_connect.go`:
  - `subscriptionKinds["claude-code"] = true`.
  - `scanClaudeLoginURL(r io.Reader) string`: reuse the ANSI-strip + a regex for the line after `visit: ` (mirror `scanDeviceAuth`), capture the `https://…` URL, no code.
  - In `defaultConnectRunner`, branch each method by kind (`switch kind`), keeping codex as-is and adding `claude-code`:
    - `detect`: `resolveCLIProgram(cliDescriptors["claude-code"])` (nativeBin `claude`) — installed if resolves.
    - `install`: return `fmt.Errorf("cài Claude Code tại claude.com/claude-code rồi thử lại")` — a clear non-npm manual-install error (the state machine surfaces it as install-failed).
    - `login`: `exec.Command(claudeBin, "auth","login","--claudeai")`, `cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+configDir)`, `StdoutPipe`; scan via `scanClaudeLoginURL`; return `(url, "" /*no code*/, wait=cmd.Wait, nil)`; `killPidTree` on ctx cancel (mirror codex `login`).
    - `pollAuth`: `claude auth status --json` with `CLAUDE_CONFIG_DIR=configDir` → `claudeAuthFromJSON`.
  - **Email label**: add `accountLabel(kind, configDir string) string` to the `connectRunner` interface (codex returns ""; claude runs `auth status --json` and returns the `email`). In `connectManager.run`, on connected: `label := m.runner.accountLabel(kind, job.configDir); if label == "" { label = job.label }`; use it in `CreateLLMAccount`. Update the fake runner + interface.
  - Extend `claudeAuthFromJSON` (or add a sibling) to also expose `email` (it currently reads `loggedIn`).

- [ ] **Step 4: run — PASS**, FULL go-check green.

- [ ] **Step 5: refactor** — dedupe the ANSI-strip regex with `scanDeviceAuth`.

- [ ] **Step 6: commit** `feat(connect): Claude browser-OAuth login runner + email-labeled account`.

---

### Task CC3: Frontend — Claude connectable + url-no-code panel

**Files:** `webui/static/pages/providers.js`, `appmode/tests/providers.test.mjs`.

- [ ] **Step 1: failing test** — mock `/llm/providers/claude-code/connect/start` etc.; assert claude-code is connectable (button enabled, not "Sắp có") and the panel renders the **login URL link** with **no code block** (`paintConnect` already guards `code ? … : null`).
- [ ] **Step 2: run — FAIL** (claude-code still "Sắp có").
- [ ] **Step 3: implement** — `CONNECTABLE_KINDS.add("claude-code")`; copy for claude connect step ("Bấm link đăng nhập Claude ở trình duyệt vừa mở"); account rows already show label (now email).
- [ ] **Step 4: run — PASS** (Portal suite green).
- [ ] **Step 5: refactor** — none.
- [ ] **Step 6: commit** `feat(portal): enable Claude connect (url link, no device-code)`.

---

### Task CC4: BASE repo — `execZaloRunner` per-account `CLAUDE_CONFIG_DIR`

**Files:** `AgentDC/internal/daemon/duty.go`, `AgentDC/internal/daemon/duty_test.go` (the **base** AgentDC repo).

**Behavior:** when `zaloConfig.ConfigDir != ""`, the `claude` command runs with `CLAUDE_CONFIG_DIR=<ConfigDir>`; when empty, behavior is unchanged.

- [ ] **Step 1: failing test** in `duty_test.go`: construct `execZaloRunner{cfg: zaloConfig{ConfigDir: "X", KBRoots: […]}}` and assert the built `cmd.Env` (factor the env-building into a testable helper if needed) contains `CLAUDE_CONFIG_DIR=X`; with `ConfigDir:""` it does not add the key. (If `Run` is hard to unit-test, extract `claudeEnv(cfg, base []string) []string` and test that.)
- [ ] **Step 2: run — FAIL.** Test command: `go -C C:\Users\Admin\Desktop\AgentDC test ./internal/daemon -run <name>` (base repo has its own go.mod).
- [ ] **Step 3: implement**:
  - Add `ConfigDir string` to the `zaloConfig` struct (its definition in `duty.go`, near `Model`/`KBRoots`/`FilesDir`/`WorkDir`/`ThinkingTokens`).
  - In `execZaloRunner.Run`, after `cmd.Env = prof.Env(os.Environ())` (duty.go:1728), add:
    ```go
    if e.cfg.ConfigDir != "" {
        // Per-account Claude login (Zalo multi-account). Appended AFTER prof.Env so it wins;
        // empty = default config, behavior unchanged.
        cmd.Env = append(cmd.Env, "CLAUDE_CONFIG_DIR="+e.cfg.ConfigDir)
    }
    ```
- [ ] **Step 4: run — PASS**; run the base package `go -C …\AgentDC test ./internal/daemon`.
- [ ] **Step 5: refactor** — keep the change minimal + behavior-preserving; add a one-line struct-field comment.
- [ ] **Step 6: commit IN THE BASE REPO**: `cd C:\Users\Admin\Desktop\AgentDC && git add internal/daemon/duty.go internal/daemon/duty_test.go && git commit -m "feat(zalo): execZaloRunner accepts per-account CLAUDE_CONFIG_DIR"`. **This is a base-repo commit** — the overlay build's `Assert-CleanGitSource` requires the base repo be *committed*-clean (no uncommitted changes), which this satisfies. Note it in the report.

---

### Task CC5: Overlay — `appClaudeRunner` selects a Claude account

**Files:** `daemon/app_llm_router.go`, `daemon/app_llm_router_test.go`.

**Behavior:** with ≥1 enabled `claude-code` account, each Claude turn runs under a round-robin-selected account's `CLAUDE_CONFIG_DIR`; with 0 accounts, the default path (and the test-injection `base` seam) is preserved.

- [ ] **Step 1: failing test** — a router/`appClaudeRunner` test: seed 2 claude-code accounts (dirs A, B); over successive Claude turns the runner's `zaloConfig.ConfigDir` alternates A→B (round-robin via `accountSel`). Use the existing router test harness; assert on the `ConfigDir` passed to a fake base-runner constructor, or via a seam. With 0 accounts, assert it returns `base` (identity).
- [ ] **Step 2: run — FAIL.**
- [ ] **Step 3: implement** — rewrite `appClaudeRunner`:
  ```go
  func (a *api) appClaudeRunner(zc zaloConfig, base zaloRunner, model string) zaloRunner {
      // Multi-account: pick a claude-code account (round-robin+cooldown). 0 accounts → default
      // config + preserve the base test-injection seam.
      if accounts, err := a.st.LLMAccounts(claudeCodeProviderID); err == nil && len(accounts) > 0 {
          if acc, ok := accountSel.pick("claude-code", accounts); ok {
              zc.ConfigDir = acc.ConfigDir
              if model != "" && model != zc.Model {
                  zc.Model = model
              }
              return execZaloRunner{cfg: zc, logger: a.logger}
          }
      }
      if model == "" || model == zc.Model {
          return base
      }
      zc.Model = model
      return execZaloRunner{cfg: zc, logger: a.logger}
  }
  ```
  (`claudeCodeProviderID = "claude-code"` already exists; `accountSel`/`store.LLMAccounts` already exist.)
- [ ] **Step 4: run — PASS**, FULL go-check green.
- [ ] **Step 5: refactor** — `ponytail:` note that penalize-on-rate-limit isn't wired for Claude yet (runClaude has no rate-limit classifier); round-robin selection only, cooldown is a follow-up.
- [ ] **Step 6: commit** `feat(router): appClaudeRunner selects a per-turn Claude account`.

---

### Task CC6: Full build gate + real connect E2E

- [ ] **Step 1: full build gate** — `build-app.ps1 … -Out F:\dist\_verify-20260808`. Expect 7/7, canary `sach`, package assembled. (Re-run once if it fails ONLY on the known `TestStreamThroughTheConfiguredServer` flake.)
- [ ] **Step 2: real connect E2E** (login already done) — from the packaged app or a daemon: (a) confirm claude-code shows connectable in the Portal; (b) drive `POST …/claude-code/connect/start` → verify it spawns `claude auth login`, surfaces the URL, and (using the already-logged-in state / a fresh login) reaches `connected` + creates an account labeled with the email; (c) with that account present, a routed Claude turn runs under its `CLAUDE_CONFIG_DIR` (check the daemon writes into that account dir). Use a specific spawned PID only — never `Get-Process claude | Kill`.
- [ ] **Step 3: commit** any embed/build fix; record the E2E result.

---

## Final review
1. Whole-branch integration review (overlay diff `main..HEAD` + the base-repo `duty.go` commit): shape agreement (connect ↔ store ↔ router), the base change is minimal + behavior-preserving at `ConfigDir==""`, security invariants (no config_dir in Portal bodies, auth-wrapped routes, canary) intact, kind-unification complete (no stray `claude_code`).
2. Full build gate green.
3. `/ship` — merge `feat/claude-cliadapter` → main (fast-forward), and the base `duty.go` commit stays in AgentDC's history.

## Risks
- **Base-repo change (CC4)** — first AgentDC modification; keep minimal. The overlay build asserts base *committed*-clean, which a commit satisfies.
- **`hasTrustDialogAccepted`** — if a routed Claude turn in a fresh per-account dir hits the "workspace not trusted" warning and it blocks (it didn't in the probe — still answered), seed `hasTrustDialogAccepted:true` into the account's `.claude.json` at connect time. Verify in CC6.
- **Browser-open from the hidden daemon** — the printed URL is the guaranteed fallback (Portal shows it); verify in CC6.
