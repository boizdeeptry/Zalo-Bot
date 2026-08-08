# Claude connect-in-Portal + multi-account — design

**Date:** 2026-08-08
**Branch:** `feat/claude-cliadapter` (off `main` @ `8b92c57`)
**Supersedes:** the original "Task 11 — merge Claude into cliAdapter" framing (rejected — see Non-goals).
**Status:** design captured; **BLOCKED on a NEEDS-LOGIN checkpoint** before planning/implementation (see "Capture-first checkpoint").

---

## Why the original Task 11 was rejected

Task 11 was "merge Claude Code into the generic `cliAdapter` family." Research (agent Explore, 2026-08-08) showed this is the **wrong abstraction**:

- The generic `cliAdapter` is for **stateless** CLIs (codex, gemini): prompt in → answer out, all context carried *in the prompt*, cwd inherited, read-only args only.
- Claude in this bot is **agentic**: the current `runClaude` → `execZaloRunner` path (base `duty.go:1710-1795`) runs Claude Code headless with **mechanical KB tool access** — `-p --output-format stream-json --verbose`, `--allowed-tools Read Grep Glob WebFetch`, and crucially **`--add-dir <each KB root>` + `--add-dir <files dir>`** so Claude's tools actually Grep/Read the corpus and the customer's files on a retrieval-miss. Plus `step` streaming, the 5-min `zaloTurnBudget`, a scratch cwd, `CLAUDECODE*` env stripping, and `MAX_THINKING_TOKENS`.
- The `claude-code` cliAdapter descriptor has **none** of that. Merging would regress Claude from an agentic KB-reading consultant to a blind prompt-answerer (retrieval-miss + every file-read turn would fail). To *not* regress, we'd rebuild all of `execZaloRunner` inside the "generic" adapter — at which point it's claude-special anyway.

**Decision:** keep `runClaude`/`execZaloRunner` as Claude's execution path. Do NOT route Claude through `cliAdapter`. Claude stays the terminal safety net in routing (the RR-eligible-only fix already keeps it as the net).

## Goal

Two user-facing wins, reframed from Task 11:

1. **Claude connect-in-Portal** — a buyer adds a Claude account without opening a terminal: click Connect → Claude's browser login opens → finish in the browser → the bot uses that login.
2. **Multi-account Claude** — several Claude logins with per-turn round-robin selection (parity with #3's codex multi-account), each isolated in its own `CLAUDE_CONFIG_DIR`.

Plus the small cleanup: unify the seed kind `claude_code` → `claude-code`.

## Non-goals

- Merging Claude into `cliAdapter` (rejected above).
- Bundling the Claude Code binary (native installer, ~100 MB/platform — too heavy). Connect **detects + requires manual install** if Claude is missing.
- Changing Claude's agentic execution model (KB `--add-dir`, stream-json, streaming, budget all stay).
- Device-auth code UI (Claude has no device-auth — see below).

---

## Research findings (verified 2026-08-08; the ones marked ⚠ still need a real login to fully confirm)

1. **Claude login is silent browser-OAuth.** `claude auth login [--claudeai|--console|--sso|--email]` — there is **no** `--device-auth`. Spawned with an isolated `CLAUDE_CONFIG_DIR`, it printed **nothing** to stdout/stderr and blocked until killed. So it opens the OS default browser to Anthropic's OAuth page and blocks on a localhost callback; **there is no URL/code to display in the Portal.**
2. **`CLAUDE_CONFIG_DIR` isolates a login** (confirmed): a fresh dir reads logged-out independently via `claude auth status --json` → `{"loggedIn":false,…}`, and Claude writes its config (`.claude.json`, `backups/`) into that dir.
3. **`claude auth status --json`** → `{loggedIn, authMethod, apiProvider}` — already consumed by `probeClaudeAuth`/`claudeAuthFromJSON` (`app_llm_cli.go:209-249`), exits non-zero when logged out but still prints valid JSON.
4. **No npm install path** — `claude` is a native install (`claude.com/claude-code`), not `npm i -g`. `#45`'s bundled-npm on-demand install does NOT apply.
5. ⚠ **Browser-open from the hidden packaged daemon** — unverified: does `claude auth login`, spawned by the run.bat-launched hidden daemon, actually open the buyer's default browser and exit 0 on success? Needs a real login.
6. ⚠ **Per-account routing** — unverified end-to-end: does `execZaloRunner` with `CLAUDE_CONFIG_DIR=<account dir>` actually consult that account's login? (Isolation is confirmed for `auth status`; the full consult turn is not yet.)

## Kind-mismatch surface to unify (from Explore, file:line)

Rename seed kind `claude_code` → `claude-code` and align every consumer onto the hyphen:
- `store/app_schema.go:93` seed `VALUES('claude-code','Claude Code','claude_code',…)` → `claude-code`, + a one-time idempotent `UPDATE llm_providers SET kind='claude-code' WHERE id='claude-code' AND kind='claude_code'`. Fix pins in `app_schema_test.go:39`, `app_llm_test.go:54-55`.
- `daemon/app_llm_accounts.go:82` `envVarFor` `case "claude_code"` → `"claude-code"` (returns `CLAUDE_CONFIG_DIR`). Pin `app_llm_accounts_test.go:77`.
- `daemon/app_llm_connect.go:60` `subscriptionKinds` += `claude-code`.
- `webui/static/pages/providers.js:14` `CONNECTABLE_KINDS` += `claude-code`.
- `store/app_llm.go:179` `subscriptionDisplayName` already `claude-code` (hyphen) — keep.
- `daemon/app_llm_api_shared.go:52,357` create-guard for `claude_code` → keep claude-code non-creatable via the API path (connect creates it), verify `app_llm_api_test.go:396-400`.
- `newLLMAdapter` (`app_llm_router.go:483`) stays WITHOUT a claude-code case (Claude does NOT go through cliAdapter — Non-goal). Router keeps recognizing Claude by `claudeCodeProviderID` for the `runClaude` path.

---

## Architecture

### Part 1 — Claude connect flow (overlay only)

Reuse the #2 connect state-machine shape (`app_llm_connect.go`: `connectManager`, `connectRunner`, per-account config dir, `handleLLMConnect*`), but Claude's login step differs from codex's device-auth:

- **`connectRunner` for Claude** — either a `kind`-branch inside `defaultConnectRunner` or a sibling runner selected by kind:
  - `detect(claude-code)` → `resolveCLIProgram` nativeBin `claude` on PATH. Missing → not-installed.
  - `install(claude-code, …)` → **not supported**: return a typed error carrying the manual-install instruction ("Cài Claude Code tại claude.com/claude-code rồi thử lại"). The state machine surfaces it as a clear, non-retryable install failure. (No npm.)
  - `login(claude-code, configDir)` → spawn `claude auth login --claudeai` with env `CLAUDE_CONFIG_DIR=configDir`. Returns **empty loginURL + empty code** (nothing to show) and a `wait()` that blocks on process exit (exit 0 = success). The process opens the buyer's browser and blocks; `killPidTree` on ctx-cancel.
  - `pollAuth(claude-code, configDir)` → `claude auth status --json` with `CLAUDE_CONFIG_DIR=configDir` → `claudeAuthFromJSON`. Backup signal to `wait()`-exit-0.
- **State machine**: `awaiting_login` (message: "Đang mở trình duyệt — hoàn tất đăng nhập Claude ở tab vừa mở") → `polling` (wait for `wait()` exit 0 OR `pollAuth`==loggedIn) → `connected` → `CreateLLMAccount`. `connectState.LoginURL`/`Code` stay empty for Claude — **the frontend must not require them.**
- **`run.bat`**: no global `CLAUDE_CONFIG_DIR` (multi-account sets it per turn in Part 2). The connect flow sets `CLAUDE_CONFIG_DIR=<accountConfigDir(dataDir,"claude-code",id)>` when spawning login.
- **Frontend** (`providers.js` connect panel): `CONNECTABLE_KINDS += claude-code`; when the connecting kind is `claude-code`, render the **browser-flow copy** (no code block) — "trình duyệt sẽ mở để đăng nhập Claude" — reusing `paintConnect` but branching on kind/`code`-absent. `subscriptionDisplayName`/gallery already show Claude Code.

### Part 2 — Multi-account Claude routing (base AgentDC repo + overlay)

- **Base change (`AgentDC/internal/daemon/duty.go`)**: `execZaloRunner` / its `zaloConfig` gains a way to receive a per-turn **`ConfigDir`** and add `CLAUDE_CONFIG_DIR=<ConfigDir>` to the `claude` command's env (`consultArgv`/`Run` env building at `duty.go:1729`). This is a **committed change to the base AgentDC repo** (the build's `Assert-CleanGitSource` requires the base repo have no *uncommitted* changes — a committed change is fine, but it now lives in AgentDC's git history, the first base-repo change this product line makes). Keep it minimal + behavior-preserving when `ConfigDir==""` (falls back to today's default Claude config).
- **Overlay wiring (`app_llm_router.go` `appClaudeRunner`)**: before running Claude, pick a `claude-code` account via `accountSel` (round-robin+cooldown, same as codex), and pass its `ConfigDir` into the base runner. Reuse `makeAccountEnv`/`accountSel` shapes from #3. 0 accounts → behave as today (default Claude config / the seeded provider) so a fresh install still answers.
- **Account model**: Claude accounts are rows in `llm_accounts` with `provider_id="claude-code"`, each `ConfigDir=<data>\accounts\claude-code\<uuid>` — identical to codex accounts (#3). Connect creates them; the accounts panel (#3 T8) already lists/deletes per provider.

## Security invariants (unchanged, must hold)

- `connectState`/`llmProviderBody` never carry `config_dir`/credential (pinned tests). Claude's `CLAUDE_CONFIG_DIR` is internal, never in Portal bodies.
- All connect + account routes stay `a.auth`-wrapped + allowlisted.
- No credential through the daemon — Claude holds its own OAuth token in `CLAUDE_CONFIG_DIR` (like codex in `CODEX_HOME`).
- The package credential/identity canary stays unweakened.
- Attachment→Claude boundary unchanged (Claude still the agentic file-reader).

## File structure

| File | Part | Change |
|------|------|--------|
| `store/app_schema.go` | 1 | seed kind `claude_code`→`claude-code` + idempotent UPDATE |
| `daemon/app_llm_accounts.go` | 1 | `envVarFor` `claude_code`→`claude-code` |
| `daemon/app_llm_connect.go` | 1 | Claude branch: silent-login runner (no device-code), `subscriptionKinds`+=claude-code |
| `webui/static/pages/providers.js` | 1 | `CONNECTABLE_KINDS`+=claude-code; browser-flow connect copy (no code block) |
| `launcher/app/run.bat` | 1 | (maybe) nothing — per-account dir set by connect + Part 2 |
| **`AgentDC/internal/daemon/duty.go`** | 2 | **base repo**: `execZaloRunner` accepts per-turn `CLAUDE_CONFIG_DIR` |
| `daemon/app_llm_router.go` | 2 | `appClaudeRunner` selects a claude-code account, passes ConfigDir to base runner |
| tests across both | 1+2 | connect state (claude branch), envVarFor, account selection, migration, canary |

## Testing (TDD)

- Store: kind migration idempotent + `claude-code` seed; account CRUD for `provider_id=claude-code` (reuses #3).
- Connect: Claude runner branch via a fake (detect ok / install-unsupported error / login returns empty url+code + wait / pollAuth) — state machine reaches `connected` on wait-exit-0; `connectState` carries no config_dir (canary).
- Router: `appClaudeRunner` picks an account and threads `CLAUDE_CONFIG_DIR`; 0 accounts → default path; fake the base runner seam.
- Base (AgentDC): `execZaloRunner` adds `CLAUDE_CONFIG_DIR` to the claude env when ConfigDir set; empty → unchanged.
- Portal: connect panel renders the browser-flow (no code) for claude-code; gallery gating.
- Full build gate green; security canary unweakened.

## Capture-first checkpoint (BLOCKS planning/impl)

Before writing the plan, a **real Claude login** on this machine is required (your OAuth action) to confirm the ⚠ items:

1. Run `claude auth login --claudeai` once (any config dir) so Claude is logged in here — needed to capture real behavior + verify routing.
2. Verify: spawning `claude auth login` with a fresh `CLAUDE_CONFIG_DIR` opens the browser and **exits 0** on completion (the `wait()` signal); `claude auth status --json` flips to `loggedIn:true` in that dir; and a consult turn with `CLAUDE_CONFIG_DIR=<that dir>` uses that login.
3. Only then: `:writing-plans` → execute (subagent-driven, capture-first, per #4's rhythm).

## Open questions / risks

1. **Base-repo change** — first modification to the AgentDC base repo from this product line; it becomes part of AgentDC's git history (not the overlay). Must be minimal + behavior-preserving at `ConfigDir==""`.
2. **Browser-open from a hidden daemon** (⚠5) — if the packaged daemon can't trigger the buyer's browser, Part 1's UX breaks; fallback would be to surface the OAuth URL (but the CLI doesn't print one — may need `--help`/a flag we haven't found, or documenting a manual `claude auth login` step). Resolve during capture-first.
3. **Login blocks indefinitely** — the connect flow must bound it (timeout + cancel + killPidTree), same as codex's 5-min login timeout.
4. **Manual install** — if Claude isn't installed, connect can't auto-install; the UX must clearly send the buyer to claude.com/claude-code.
