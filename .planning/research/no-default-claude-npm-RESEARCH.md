# no-default-claude-npm — Research

**Researched:** 2026-08-10
**Domain:** Claude Code distribution (npm) + Zalo-Bot overlay/base daemon wiring
**Confidence:** HIGH (npm facts + codebase anchors verified on this machine; two execute-time checkpoints remain)

---

## Gating verdict

**GREEN — with one material correction to the design's mental model.**

Approach B (`node <binJS>` via `npm root -g`, matching codex) is viable, install works online exactly like codex, and every flag the design needs is present. BUT the design's literal assumption is **wrong**:

> `@anthropic-ai/claude-code` is **NOT** a self-contained node CLI. There is **no `cli.js`.** `bin.claude` points at a native binary (`bin/claude.exe`), and the real 287 MB binary is delivered through a platform-specific **optionalDependency** (`@anthropic-ai/claude-code-win32-x64`), copied into place by a `postinstall: node install.cjs`.

The approach survives because the package ships **`cli-wrapper.cjs`** — a `node`-runnable launcher that spawns the native binary. So `binJS = @anthropic-ai\claude-code\cli-wrapper.cjs` keeps the exact codex rail with near-zero code change. Nothing blocks the plan.

Corrections the plan MUST absorb:
1. **`binJS` = `@anthropic-ai\claude-code\cli-wrapper.cjs`** (NOT `cli.js` — that file does not exist). `node bin\claude.exe` would be wrong (it's a PE, not a script).
2. **"Offline" = self-contained node+npm, not no-network.** `npm i -g` still fetches from the registry at Connect (the buyer is online for OAuth anyway). It auto-pulls the ~287 MB win32-x64 optional dep. This satisfies the user's "on-demand at Connect, do NOT pre-bundle the binary" — no 287 MB in the installer.
3. Two execute-time checkpoints below (stdio through the node→native hop; orphan-on-timeout on the base consult path). Neither kills the approach; both have a documented fallback (**A-native-direct-exe**).

---

## Claude Code npm facts

Verified via `npm view` + tarball extraction + the installed binary, 2026-08-10.

| Fact | Value | Evidence |
|------|-------|----------|
| Package name | `@anthropic-ai/claude-code` (unchanged, not renamed) | [VERIFIED: npm registry] |
| Latest / stable | `2.1.226` (latest), `2.1.220` (stable tag), installed native `2.1.223` | `npm view … dist-tags`; `claude --version` |
| `bin` | `{ "claude": "bin/claude.exe" }` — a **500-byte stub**, not a JS entry | tarball `package/bin/claude.exe` = 500 bytes |
| `dependencies` | `{}` | [VERIFIED] |
| `optionalDependencies` | 8 platform pkgs incl. `@anthropic-ai/claude-code-win32-x64@2.1.226` | [VERIFIED] |
| win32-x64 native pkg | **287,054,042 bytes unpacked** (~90 MB tarball) | `npm view @anthropic-ai/claude-code-win32-x64 dist.unpackedSize` |
| `scripts.postinstall` | `node install.cjs` — copies/hardlinks the native binary from the matched optional dep over the `bin/claude.exe` stub. **Does NOT fetch over the network itself**; the download is npm's normal optional-dep resolution. `--omit=optional` → leaves the stub + prints instructions. | tarball `package/install.cjs` (read in full) |
| Node-runnable entry | `package/cli-wrapper.cjs` — `spawnSync(nativeBinary, argv, {stdio:'inherit'})`. Meant for `--ignore-scripts`, but works generally as long as the optional dep is installed. | tarball `package/cli-wrapper.cjs` (read in full) |
| Tarball file list | `bin/claude.exe`, `cli-wrapper.cjs`, `install.cjs`, `LICENSE.md`, `package.json`, `README.md`, `sdk-tools.d.ts` (7 files, 163 KB) | `tar -tzf` |

**Flag support — all present on installed `claude 2.1.223`** [VERIFIED: `claude --help`, `claude auth --help`, `claude auth login --help`]:

| Design dependency | Present? | Note |
|---|---|---|
| `claude auth login` (browser OAuth, prints URL) | ✅ | `Sign in to your Anthropic account`; flags `--claudeai` (default), `--console`, `--email`, `--sso`. No device code (URL only) — matches `scanClaudeLoginURL`. |
| `claude auth status --json` / `logout` | ✅ | subcommands exist |
| `-p` / `--print` | ✅ | |
| `--output-format stream-json` + `--input-format stream-json` | ✅ | (only with `--print`) |
| `--model <model>` | ✅ | |
| `--add-dir <dirs...>` | ✅ | KB access preserved |
| `--allowedTools` / `--allowed-tools` | ✅ | matches `readOnlyArgs` |
| `CLAUDE_CONFIG_DIR` (env) | ✅ (env var, not a flag) | [CITED: Claude Code docs] per-account isolation; base already injects it (`claudeEnv`, duty.go:1721). Real per-account isolation is an execute checkpoint. |

**This machine:** native claude at `C:\Users\Admin\.local\bin\claude.exe` (280 MB, native installer, NOT npm). `npm root -g` = `C:\Program Files\nodejs\node_modules` (no `@anthropic-ai/` there). node v22.23.1, npm 10.9.8. The packaged app overrides `npm root -g` to `data\cli` via `npm_config_prefix` (run.bat), so resolve targets the bundled prefix, not this dev path.

**Offline-bundle-ability:** Feasible but **not needed and not wanted.** The build (`build-app.ps1:279-301`) bundles `node.exe` + the `npm` module into `app\node`; run.bat prepends it to PATH and sets `npm_config_prefix=data\cli`. `install()` then runs `npm install -g <pkg>` which **fetches from the registry at Connect** (network required) — this is what codex does too. For claude-code this auto-pulls the win32-x64 optional dep (~90 MB download). To make it truly network-free you'd have to vendor the 287 MB binary into the installer — which the non-goals forbid. So: **online install at Connect, no pre-bundle.** GREEN.

---

## Codebase anchors

All line numbers verified 2026-08-10.

### Overlay — `appmode\overlay\internal\daemon`

- **`app_llm_cli.go:83-94`** — `cliDescriptors["claude-code"]`. Change: drop `nativeBin:"claude"` (line 85) → add `npmPackage:"@anthropic-ai/claude-code"` + `binJS:` `@anthropic-ai\claude-code\cli-wrapper.cjs`. Keep `readOnlyArgs`, `bannedArgs`, `authMethod`, `modelSeeds`, `claudeBudget`.
- **`app_llm_cli.go:586-607`** — `resolveCLIProgram`. The npm branch already does `node <binJS>` via `npmGlobalRoot()` + `os.Stat(binJS)` and returns `(node, []string{binJS}, nil)`. **With the descriptor flip, claude-code falls into this branch automatically — no change needed.** The `if d.nativeBin != ""` native branch (587-592) becomes dead (no descriptor uses `nativeBin` after the flip); leave it or delete it (harmless).
- **`app_llm_cli.go:212-236`** — ⚠️ **`probeClaudeAuth` reads `d.nativeBin` directly** (`exec.LookPath(d.nativeBin)`, line 213). After the flip `d.nativeBin==""` → `LookPath("")` fails → always `authUnknown`. **MUST be repointed to `resolveCLIProgram` + `append(prefixArgs, "auth","status","--json")`** (mirror `pollAuthClaude`). This is the one break site the spec did not call out. Note: `probeClaudeAuth` is reached via `cliAdapter.Test`→`checkCLIAuth` (line 174), and claude-code has **no** cliAdapter in production (routed via `runClaude`), so it is effectively test-only today — but it still compiles and its behavior silently degrades, so fix it (or delete it if the planner confirms it is dead).
- **`app_llm_connect.go:326-354`** — `install()`. Delete the claude-code rejection (lines 329-331); claude-code then falls into the generic `npm install -g <pkg>` (line 336) with `pkg="@anthropic-ai/claude-code"`. Same path codex uses. UX note: `install()` buffers npm output and replays after exit (no live progress) — the ~90 MB download means a longer silent "Đang cài…".
- **Connect Claude call sites already thread `prefixArgs` — NO change needed:**
  - `loginClaude` (`app_llm_connect.go:506-556`): line 511 `argv := append(append([]string{}, prefixArgs...), "auth","login","--claudeai")`. With native → `claude auth login`; with npm → `node <wrapper.cjs> auth login`. ✅
  - `pollAuthClaude` (`:612-627`): line 619, same pattern. ✅
  - `accountLabel` (`:633-648`): line 643, same pattern. ✅
  - `detect` (`:302-313`): uses `resolveCLIProgram` → correct pre/post install (stat of `cli-wrapper.cjs`). ✅
- **`app_llm_router.go:513-550`** — `appZaloRunner`. Fallbacks to make **silent** (currently `return base`): route-read error (`:522-526`) and empty entries (`:527-529`). ("Đọc-route-hỏng" and "route rỗng" both here.)
- **`app_llm_router.go:592-607`** — `appClaudeRunner`. 0-account fallback `if model=="" || model==zc.Model { return base }` (`:602-604`) → make **silent**.
- **Silence must NOT be `return base`** anymore: `base` is `execZaloRunner{}` (the base consult runner). With no default, returning it re-introduces the "route to Claude" behavior the user is removing.

### Base — `AgentDC\internal\daemon\duty.go` (touched, opt-in / backward-compatible)

- **`duty.go:37-116`** — `type zaloConfig`. Add opt-in fields: `Program string` + `ProgramPrefixArgs []string`. Empty = old behavior. (`ConfigDir` already here at :90; `Model`, `ThinkingTokens`, etc.)
- **`duty.go:1735-1747`** — `execZaloRunner.Run`. Line 1737 `bin, err := exec.LookPath(prof.Binary)` (prof.Binary=="claude" from `agent.ConsultReadOnly()`, `profile.go:146-149`). Line 1745 `cmd := exec.CommandContext(ctx, bin, args...)`. Change: `if cfg.Program != "" { program, prefix = cfg.Program, cfg.ProgramPrefixArgs } else { LookPath(prof.Binary) }`, then `exec.CommandContext(ctx, program, append(prefix, args...)...)`. Standalone (no Program) unchanged.
- **`duty.go:1718-1733`** — `claudeEnv` already injects `CLAUDE_CONFIG_DIR` + `MAX_THINKING_TOKENS`. No change.
- **Overlay wiring:** `appClaudeRunner` (router:592) sets `zc.Program` + `zc.ProgramPrefixArgs` from `resolveCLIProgram(cliDescriptors["claude-code"])` (same way it sets `zc.ConfigDir` from the account, line 595) before building `execZaloRunner{cfg: zc}`.
- **`app_knowledge.go:463`** also calls `agent.ConsultReadOnly()` — audit during planning whether it spawns claude for a turn (likely just reads the profile for model info; probably unaffected).

---

## Silent-gate seam

**The overlay CANNOT skip `answerZalo` through the current build seam.** Confirmed mechanism:

- Base per-turn dispatch: `triggerZaloDuty` (`duty.go:804-830`) → `answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)` (`:821`), where `deps.run` = base `execZaloRunner` (`newZaloDeps`, `:429`).
- The overlay merges via `Apply-AppSeams` (**`scripts\BuildApp.psm1:249-256`**). The **runner seam** does a single exactly-once string replace of the `answerZalo` argument:
  ```
  a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)
    →
  a.answerZalo(ctx, deps.cfg, a.appZaloRunner(deps.cfg, deps.run, threadID, len(files) > 0),
               threadID, question, step, reply, files...)
  ```
  It replaces **only the runner argument** — `answerZalo` is always called. (`Assert-SignatureAbsent … 'a.appZaloRunner('`, line 221, guards double-apply; a post-replace count asserts exactly 1 `answerZalo` call site, lines 262-266.)
- `answerZalo` escalates on **any** runner failure: `raw, err := run.Run(...)`; `if err != nil { return escalate("the agent did not finish", …) }` (**`duty.go:584-587`**). Empty/`""` output also escalates (`extractJSONObject("")` → JSON unmarshal fails → `escalate("the answer was not the required JSON")`, `:590-591`). `escalate` **flags the thread and sends the handoff line to the customer** (`:482-538`) — exactly what the spec forbids for the unconfigured case.
- There IS a natural silent path, but it's keyed on thread status, not the runner: `if status == ZaloManual || status == ZaloNeedsHuman { … return nil }` (**`:467-471`**). This is the `ZaloManual → return nil` the spec references.

**Verdict on the two spec options:**

- **Option #1 (overlay skips `answerZalo`)** — requires editing the build seam (`Apply-AppSeams`) so the seam becomes a conditional (`if r := a.appZaloRunner(...); r != nil { a.answerZalo(...) }`) AND changing `appZaloRunner`'s contract to return `nil` for "no runnable provider." That touches the security-gated packaging script and changes an invariant (`appZaloRunner` currently never returns nil). **Not preferred.**
- **Option #2 (base sentinel) — RECOMMENDED.** Add `var ErrZaloSilent = errors.New(...)` in base; in `answerZalo` right after `run.Run` (before the escalate-on-error branch at :585) add `if errors.Is(err, ErrZaloSilent) { return nil }`. The overlay's silent runner returns `("", ErrZaloSilent)`. Additive, opt-in, backward-compatible — base `execZaloRunner` never returns it, so standalone is byte-for-byte unchanged. This is the only mechanism that yields clean silence (no escalate/handoff) without touching the build script.

Note: a plain `("", nil)` or `("", someError)` from the silent runner does **not** work — both hit the escalate path. Silence must be an explicit recognized signal.

---

## Decisions for plan

- **Package (unpinned, like codex):** `npmPackage = "@anthropic-ai/claude-code"`. `install()` runs `npm install -g @anthropic-ai/claude-code` — no version pin (mirrors the existing `@openai/codex` install). Flags verified on 2.x (2.1.223); the 2.x line is stable for `auth login`, `-p`, stream-json, `--add-dir`, `--model`, `--allowed-tools`.
- **`binJS = @anthropic-ai\claude-code\cli-wrapper.cjs`** (NOT `cli.js` — does not exist; NOT `bin\claude.exe` — a PE, unrunnable by `node`). resolveCLIProgram's existing npm branch returns `(node, [<npm root>\@anthropic-ai\claude-code\cli-wrapper.cjs])`.
- **Offline install is online-at-Connect.** No pre-bundle of the 287 MB binary. `npm i -g` via bundled node+npm fetches wrapper + win32-x64 optional dep from the registry; postinstall (`node install.cjs`) hardlinks the native binary into place. Do NOT pass `--omit=optional`.
- **Connect call sites need NO prefixArgs change** — `loginClaude`, `pollAuthClaude`, `accountLabel` (and generic `login`/`pollAuth`) already prepend `prefixArgs`. Native gave `prefixArgs=nil`; npm gives `[cli-wrapper.cjs]`. Both flow through unchanged.
- **One break site to fix:** `probeClaudeAuth` (`app_llm_cli.go:213`) reads `d.nativeBin` directly — repoint to `resolveCLIProgram` (or delete if planner confirms it's dead in production). No other production reader of `nativeBin` exists besides the soon-dead resolve branch.
- **Silent gate = base sentinel `ErrZaloSilent` (spec option #2), NOT overlay-skip.** The build seam only substitutes the runner argument; `answerZalo` always runs and escalates on any runner error. Add `ErrZaloSilent` in base + one `errors.Is` guard before the escalate branch (`duty.go:~585`); overlay silent runner returns `("", ErrZaloSilent)`. Opt-in, backward-compatible.
- **Base consult path (`execZaloRunner.Run`):** add opt-in `zaloConfig.Program` + `ProgramPrefixArgs`; use them when set, else `LookPath(prof.Binary)`. Overlay `appClaudeRunner` populates them from `resolveCLIProgram(claude-code)`, alongside the existing `zc.ConfigDir` set.
- **Router silence:** `appZaloRunner` route-error (`:522-526`) + empty-entries (`:527-529`) and `appClaudeRunner` 0-account (`:602-604`) return a silent runner (→ `ErrZaloSilent`), NOT `base`.
- **Fallback if execute checkpoints bite — A-native-direct-exe:** if the node→native hop breaks stdin/stream-json or leaks orphans, add a descriptor field (e.g. `npmBin = @anthropic-ai\claude-code\bin\claude.exe`) + a resolveCLIProgram branch returning `(<npm root>\<npmBin>, nil)`. This runs the real PE directly (no `.cmd`, so NOT the spec's rejected BatBadBut "A"), restoring single-process kill semantics and removing the stdio hop. ~5 lines; all call sites already handle `prefixArgs=nil`.

---

## Open questions / execute-time checkpoints

1. **[NEEDS-LOGIN]** Real `node <cli-wrapper.cjs> auth login --claudeai` with `CLAUDE_CONFIG_DIR=<dir>`: confirm it prints the OAuth URL on stdout (through the wrapper's `stdio:'inherit'` → Go's `StdoutPipe`), completes login, writes creds into the isolated dir, exits 0, and `auth status --json` returns `loggedIn:true` + email. (Same checkpoint class as the prior CC login.)
2. **[NEEDS-VERIFY]** Real consult `node <cli-wrapper.cjs> -p --output-format stream-json --input-format stream-json --add-dir <kb>` with the prompt on stdin: confirm the prompt reaches the native binary and stream-json comes back, both **through the node→native hop** (`spawnSync` stdio inherit). Windows fd inheritance is the risk.
3. **[HAZARD] Orphan-on-timeout, base consult path.** `execZaloRunner.Run` uses `exec.CommandContext` (kills only the direct child). With the node hop, a turn timeout kills `node` but may orphan the native `claude` (280 MB, possibly mid-API-call) until its broken stdout pipe forces it to exit. Verify behavior; if unacceptable, take the A-native-direct-exe fallback (removes the hop) rather than porting `killPidTree` into base. The Connect path (`loginClaude` et al.) already uses `killPidTree` (`taskkill /T`) so it is covered there.
4. **[DECISION for /discuss re-confirm]** The "B vs A" choice in the spec was made assuming claude npm = `cli.js`. That is false. Both surviving variants satisfy the user's stated B-rationale ("no `.cmd`/BatBadBut"): **B-wrapper** (`node cli-wrapper.cjs`, spec-faithful, near-zero change, two execute risks) vs **A-native-direct-exe** (run the real PE directly, no hop, no stdio/orphan risk, ~5 extra lines). Recommend proceeding with B-wrapper and keeping A-native-direct-exe as the pre-approved fallback so execute doesn't stall.
5. **[VERIFY at plan]** Confirm `app_knowledge.go:463` (`agent.ConsultReadOnly()`) does not independently spawn `claude` for a turn (would be a third resolve site). Likely just reads the profile.
   → **RESOLVED at plan (2026-08-10): it IS a third spawn site** — `app_knowledge.go:456` does `exec.LookPath("claude")` + spawns (write-mode KB ingest). Repointed in T4.

---

## Spike results (T1) — 2026-08-10

Installed `@anthropic-ai/claude-code@2.1.226` into a throwaway prefix (`npm i -g`, "added 2 packages in 25s", no `--ignore-scripts`). Verified on this machine (node v22.23.1, npm 10.9.8):

- **`npm root -g` layout:** `<root>\@anthropic-ai\claude-code\` contains `cli-wrapper.cjs` (4997B), `install.cjs`, `bin\`, `node_modules\` (the win32-x64 optional dep is nested here, not top-level).
- **`bin\claude.exe` = 287,053,472 bytes — the REAL native binary** (postinstall `node install.cjs` copied it over the stub). Stable path: `<npm root>\@anthropic-ai\claude-code\bin\claude.exe`.
- **stdio works identically both ways:** `node cli-wrapper.cjs --version` and `bin\claude.exe --version` → both `2.1.226 (Claude Code)`, exit 0. `auth status --json` → both print `{"loggedIn":false,...}`, exit 1 (logged-out, expected). `CLAUDE_CONFIG_DIR` honored.
- **`cli-wrapper.cjs` is explicitly a FALLBACK** (its own header): *"the postinstall script copies the native binary over bin/claude.exe, so this file is never invoked. It exists for environments where postinstall doesn't run (--ignore-scripts) — users can run `node cli-wrapper.cjs` directly and pay the Node-process overhead as the price."* It runs via `spawnSync(binaryPath, argv, {stdio:'inherit'})` (blocking, inherited stdio).
- **Orphan hazard confirmed by construction (no timing test needed):** B-wrapper = `node` → `spawnSync` native child. On Windows there is no process-group cascade, so `exec.CommandContext` killing `node` on a turn timeout would ORPHAN the 287MB `claude.exe`. A-native runs the PE directly → `CommandContext` kills the actual process.

### DECISION: **A-native-direct-exe** (not B-wrapper)

Vendor-intended path, same tiny change, removes BOTH execute-time hazards (stdio hop + orphan). Overrides spec/plan's B-wrapper primary.

**Implications the implementers MUST apply (supersede plan T4/T6 where they say `binJS`/`cli-wrapper.cjs`):**
- Descriptor `claude-code`: `npmPackage:"@anthropic-ai/claude-code"` + **new field `npmBin: `@anthropic-ai\claude-code\bin\claude.exe``** (relative to npm root). NOT `binJS` (that's the node+.js rail for codex/gemini). No `nativeBin`.
- `resolveCLIProgram`: add a branch — `if d.npmBin != "" { root,_ := npmGlobalRoot(); exe := filepath.Join(root, d.npmBin); if _,err := os.Stat(exe); err==nil { return exe, nil, nil } ... }`. Returns `program=exe, prefixArgs=nil`.
- Because `prefixArgs=nil`, ALL connect call sites (`loginClaude`/`pollAuthClaude`/`accountLabel`) and base `execZaloRunner` run `claude.exe <args>` directly — identical to the old native path, so the change is minimal and the T3 base `Program` mechanism still applies (`Program=exe`, `ProgramPrefixArgs=nil`).
- `install()` unchanged from plan (plain `npm install -g @anthropic-ai/claude-code`, no `--ignore-scripts`, no `--omit=optional` → postinstall places the real `bin\claude.exe`).
- Guard (ponytail, optional): if `bin\claude.exe` resolves but is a tiny stub (<1MB, postinstall skipped), fall back to the `node cli-wrapper.cjs` path. T9 verifies the real binary; skip the guard unless E2E shows a stub.

Cleaned up the throwaway prefix after the spike.
