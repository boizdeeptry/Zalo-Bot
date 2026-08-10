# Claude connect-in-Portal + multi-account — design

**Date:** 2026-08-08
**Branch:** `feat/claude-cliadapter` (off `main` @ `8b92c57`)
**Supersedes:** the original "Task 11 — merge Claude into cliAdapter" framing (rejected — see Non-goals).
**Status:** design captured; **capture-first VERIFIED** with a real Claude login on this machine (2026-08-08) — checkpoint CLEARED, ready for `:writing-plans`.

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

## Research findings (VERIFIED 2026-08-08 with a real login)

1. **Claude login = browser-OAuth that PRINTS a URL and completes via a localhost callback.** (Corrects an earlier "silent" reading caused by killing the probe too fast.) `claude auth login --claudeai` emits to **stdout**:
   ```
   Opening browser to sign in…
   If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&client_id=…&redirect_uri=…&code_challenge=…&state=…
   Paste code here if prompted >
   ```
   It opens the OS default browser AND listens on `127.0.0.1:<ephemeral>` (observed :64321). When the user authorizes, the localhost callback completes it → prints `Login successful.` → **exits 0**. So the Portal **CAN parse + display the URL** (the line after "If the browser didn't open, visit:") like codex — there is **no separate device-code**; the callback (or the paste-code fallback) finishes it. There is **no** `--device-auth` flag; `[--claudeai|--console|--sso|--email]` only.
2. **`CLAUDE_CONFIG_DIR` isolates a login** (confirmed): a fresh dir reads logged-out independently; login lands entirely in that dir (`.claude.json`, `backups/`).
3. **`claude auth status --json`** → `{loggedIn, authMethod, apiProvider, email, orgId, orgName, subscriptionType}`. **Email IS exposed** (e.g. `congnghe@midu.vn`, org "Technology Department", `team`) — so a Claude account can be **labeled with its real email** (better than codex, which exposed none). Consumed today by `probeClaudeAuth`/`claudeAuthFromJSON` (`app_llm_cli.go:209-249`).
4. **No npm install path** — `claude` is a native install (`claude.com/claude-code`), not `npm i -g`. `#45`'s bundled-npm on-demand install does NOT apply; connect detects + requires manual install.
5. **Browser-open + exit-0 signal — VERIFIED**: spawned from a background process (user's session), it opened the browser, completed on authorize, printed `Login successful.`, exit 0. In the packaged hidden daemon the browser-open may differ, but the **printed URL is the guaranteed fallback** (Portal shows it; buyer clicks).
6. **Per-account routing — VERIFIED**: `claude -p "…"` with `CLAUDE_CONFIG_DIR=<that dir>` answered correctly (exit 0), proving inference uses the account in that dir. ⚠ **Trust-dialog note**: a fresh `CLAUDE_CONFIG_DIR` with an untrusted cwd warns `this workspace has not been trusted … set projects[cwd].hasTrustDialogAccepted:true in <dir>\.claude.json` (it still answered). The real runner uses `--add-dir` (grants scope) which likely avoids it; if not, seed `hasTrustDialogAccepted:true` into the per-account `.claude.json` at account-create. Resolve in the plan.

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
  - `login(claude-code, configDir)` → spawn `claude auth login --claudeai` with env `CLAUDE_CONFIG_DIR=configDir`. **Scan stdout** for the `If the browser didn't open, visit: <URL>` line → return that **loginURL** (Code stays empty — Claude has no device-code). Return a `wait()` that blocks on process exit (`Login successful.` + exit 0 = success). It also opens the buyer's browser + listens on a localhost callback; `killPidTree` on ctx-cancel. (Reuses the codex `scanDeviceAuth`-style stdout scan, ANSI-strip included, just a different URL line + no code.)
  - `pollAuth(claude-code, configDir)` → `claude auth status --json` with `CLAUDE_CONFIG_DIR=configDir` → `claudeAuthFromJSON`. Backup signal to `wait()`-exit-0. On success, read the **email** from the same JSON to label the account.
- **State machine**: `awaiting_login` → set **LoginURL** (the parsed OAuth URL; message: "Bấm vào link để đăng nhập Claude, hoặc dùng tab trình duyệt vừa mở") → `polling` (wait for `wait()` exit 0 OR `pollAuth`==loggedIn) → `connected` → `CreateLLMAccount{Label: email}`. `connectState.Code` stays empty for Claude — the frontend shows the URL, no code block.
- **`run.bat`**: no global `CLAUDE_CONFIG_DIR` (multi-account sets it per turn in Part 2). The connect flow sets `CLAUDE_CONFIG_DIR=<accountConfigDir(dataDir,"claude-code",id)>` when spawning login.
- **Frontend** (`providers.js` connect panel): `CONNECTABLE_KINDS += claude-code`; reuse `paintConnect` — Claude has a **loginURL (show the clickable link)** but **no code** (the existing `code ? … : null` guard already handles that). Copy: "Bấm link đăng nhập Claude ở trình duyệt". `subscriptionDisplayName`/gallery already show Claude Code. Account rows label with the captured **email**.

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

## Capture-first checkpoint — CLEARED ✅ (2026-08-08)

Done via a real login on this machine (`CLAUDE_CONFIG_DIR=C:\Users\Admin\AppData\Local\zalo-claude-capture`, account `congnghe@midu.vn`):
1. `claude auth login --claudeai` printed the OAuth URL, opened the browser, completed on authorize, printed `Login successful.`, **exit 0**.
2. `claude auth status --json` → `loggedIn:true` + email in that isolated dir.
3. `claude -p` with `CLAUDE_CONFIG_DIR=<that dir>` answered (exit 0) — per-account inference confirmed.

This logged-in capture dir is available to test the Part-2 routing during execution. Next: `:writing-plans` → execute (subagent-driven, per #4's rhythm).

## Open questions / risks

1. **Base-repo change** — first modification to the AgentDC base repo from this product line; it becomes part of AgentDC's git history (not the overlay). Must be minimal + behavior-preserving at `ConfigDir==""`.
2. **Browser-open from a hidden daemon** (⚠5) — if the packaged daemon can't trigger the buyer's browser, Part 1's UX breaks; fallback would be to surface the OAuth URL (but the CLI doesn't print one — may need `--help`/a flag we haven't found, or documenting a manual `claude auth login` step). Resolve during capture-first.
3. **Login blocks indefinitely** — the connect flow must bound it (timeout + cancel + killPidTree), same as codex's 5-min login timeout.
4. **Manual install** — if Claude isn't installed, connect can't auto-install; the UX must clearly send the buyer to claude.com/claude-code.
