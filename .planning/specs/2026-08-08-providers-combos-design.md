# Providers #4 — Combos (routing strategies) — design

**Date:** 2026-08-08
**Branch:** `feat/cli-subscription-providers`
**Sub-project:** #4/4 of the 9Router-style Providers UI. Depends on engine + UI#1 + #2 connect + #3 multi-account (all on-branch). Ship the whole branch once, after this.
**Status:** design approved (scope + approach), spec pending review.

---

## Goal

Replace the single hidden fallback chain (and the **Models** page) with a **Combos** page: the seller keeps several named routing strategies, marks one *active*, and the bot obeys the active combo on every turn. Same green-safe framing as the gallery ("chính chủ, không rủi ro khoá").

## Scope (locked with user)

- **Two combo types:** `fallback` (try in order, first success wins — today's behaviour) and `round_robin` (rotate the starting member each turn, then fall through the rest).
- **Multiple combos, one active** (9Router parity): create / edit / delete / pick-active.
- **Task 11 folded in, hedged, sequenced last:** merge Claude Code into `cliAdapter`, unify kind `claude_code`→`claude-code`. Abandon if it destabilises the routing seam or the security canary — combos still ship with Claude special-cased.

## Non-goals

- **Fusion** (multi-model ensemble/merge) — 2–3× cost + latency for a single customer reply, and picking/merging N answers has no clear product value. Deferred.
- **Capacity** (quota-weighted routing) — CLI subscriptions don't expose real quota, so it degenerates to weighted-random. Deferred.
- **Real quota tracking**, **thread-sticky routing**, **per-combo telemetry split** (telemetry stays global as today).

---

## Current state (what exists, verified in code)

- **One global chain.** `llm_route_entries(position PK, provider_id, model_id, enabled)` — a single ordered list. `Store.LLMRoute()` returns `LLMRouteSnapshot{Revision, Entries}`; `Store.ReplaceLLMRoute(expectedRevision, entries)` is compare-and-swap (`ErrLLMRouteConflict`), revision in `app_meta.llm_route_revision`. Schema version = **3** (`app_schema.go`, idempotent `CREATE TABLE IF NOT EXISTS` + `INSERT OR IGNORE` run every open).
- **`validateLLMRoute` (app_llm.go:508)** — the claude-tail invariant is **already lifted (§6)**: empty is valid; a chain may be all-subscription with no Claude; the only tail rule is *last member must be enabled*; every member's `(provider_id, model_id)` must be a valid, reachable pair. (The contradicting comment in `app_llm_router.go` is stale.)
- **Router (`app_llm_router.go`).** `appLLMRunner.Run` reads the snapshot once, walks entries in order: per-provider 25s (`defaultLLMProviderTimeout`), whole-API-chain 60s (`defaultLLMChainTimeout`). `claude-code` (const `claudeCodeProviderID`) is **terminal-anywhere**: when the walk reaches an enabled `claude-code` entry it runs `runClaude` under the **parent ctx** (long budget, carries `step`) and returns. Attachments force `firstEligibleCLIEntry` (first enabled local_cli). Telemetry rows (`llm_attempts`) carry no customer content.
- **Models page (`webui/static/pages/models.js`).** The fallback-chain editor: `createModelService` (`GET/PUT /llm/route`, `GET /llm/providers`, `GET /llm/status`), pure draft helpers (`moveEntry`/`removeEntry`/`addEntry`/`patchEntry`), `createModelsPage` renders rows (provider select, model select, enable, up/down/remove, live "đang chạy" badge, status line). Comment already reflects the lifted invariant.
- **#3 pattern to mirror.** `accountSel` — a package-level, in-memory round-robin+cooldown selector (`api` struct is base-repo-locked, so no new fields). RR cursor here follows the same shape.

---

## Chosen approach — active-combo indirection (reuse the walk)

A combo is just `{type, ordered members}`. The active combo replaces "the chain":

1. `Store.LLMRoute()` resolves the **active** combo and returns `LLMRouteSnapshot{Revision, Type, Entries}` (add a `Type` field; existing behaviour = `type=fallback`).
2. The router branches on `Type` **once**, before the existing loop:
   - `fallback` → run the loop as today (unchanged).
   - `round_robin` → rotate `entries` by an in-memory per-combo cursor, then run the **same** loop on the rotated slice.
3. Everything else — per-provider/chain budgets, `claude-code` terminal-anywhere, disabled-skip, attachment boundary, telemetry, credential clearing — is reused verbatim.

**Why this and not a strategy-object interface per type:** two types share ~90% of the walk (budgets, telemetry, attachment guard, claude path). A `RoutingStrategy` interface would duplicate that careful logic across implementations and invite drift in exactly the code where drift is dangerous. Rotate-then-reuse is a few lines and keeps one walk. (If Fusion/Capacity ever land, revisit — they genuinely differ and would justify the interface then, not now.)

**Why in-memory RR cursor (not persisted):** matches `accountSel`; avoids a per-turn DB write; RR fairness across daemon restarts is not worth the write contention. Cursor keyed by combo id, reset on restart.

---

## Data model (schema v3 → v4, same idempotent style)

New tables:

```sql
-- A named routing strategy. Exactly one row has active = 1 (enforced in app code
-- inside inLLMTx, same as ReplaceLLMRoute's CAS; SQLite partial-unique is fragile
-- across the idempotent re-run so the invariant lives in the setter, not a constraint).
CREATE TABLE IF NOT EXISTS llm_combos (
  id       TEXT PRIMARY KEY,
  name     TEXT NOT NULL,
  type     TEXT NOT NULL DEFAULT 'fallback' CHECK (type IN ('fallback','round_robin')),
  active   INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1        -- per-combo CAS, replaces the global route revision
);

-- Ordered members of one combo. position is identity within a combo (see llm_route_entries note).
CREATE TABLE IF NOT EXISTS llm_combo_members (
  combo_id    TEXT NOT NULL REFERENCES llm_combos(id) ON DELETE CASCADE,
  position    INTEGER NOT NULL,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id),
  model_id    TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  PRIMARY KEY (combo_id, position)
);

-- Seed one default combo. INSERT OR IGNORE so re-running migration never clobbers edits.
INSERT OR IGNORE INTO llm_combos(id, name, type, active) VALUES ('default', 'Mặc định', 'fallback', 1);
```

**Migration decision — restructure, don't data-copy.** The branch is unshipped; **no buyer DB exists** with a populated route. So `llm_combo_members` supersedes `llm_route_entries`; I do **not** write a fragile every-open copy from the old table. A fresh install gets the empty default combo (valid per `validateLLMRoute`). Any dev chain in `llm_route_entries` is not preserved (re-seed by re-entering it). `llm_route_entries` and `llm_route_revision` are left in place (harmless, unreferenced) rather than dropped, to keep the migration purely additive.

Bump `UPDATE app_meta SET value = '4' WHERE key = 'schema_version'`.

## Store API (in `app_llm.go` / a new `app_combos.go`)

Follow "many small files": put combo CRUD + the RR-aware read in **`store/app_combos.go`**, keep `app_llm.go` focused.

- `LLMCombos() ([]LLMCombo, error)` — all combos with members (for the page).
- `LLMRoute() (LLMRouteSnapshot, error)` — **now resolves the active combo**; snapshot gains `Type` + `ComboID`. Empty/missing active → empty snapshot (router already treats empty as "no routing → base runner").
- `CreateLLMCombo(name, type) (LLMCombo, error)`, `DeleteLLMCombo(id) error` (cannot delete the active one, or the last one), `SetActiveLLMCombo(id) error` (flips active atomically in `inLLMTx`).
- `ReplaceLLMComboMembers(comboID string, expectedRevision int64, type string, entries []LLMRouteEntry) (LLMCombo, error)` — per-combo CAS; validates members via the existing `validateLLMRoute` (unchanged: last-enabled + valid pairs), sets type, bumps the combo's revision. `ErrLLMComboConflict` mirrors `ErrLLMRouteConflict`.

`LLMRouteEntry` is reused as the member shape. `LLMRouteSnapshot` gains `Type string` and `ComboID string`.

## Router change (`app_llm_router.go`)

- `appLLMRunnerConfig` / `Run` read `snapshot.Type`. Before the loop:
  ```go
  entries := slices.Clone(snapshot.Entries)
  if snapshot.Type == "round_robin" {
      entries = rotate(entries, comboRR.next(snapshot.ComboID, len(entries)))
  }
  ```
- `rotate(entries, k) []LLMRouteEntry` — pure, returns `entries[k:] + entries[:k]`. Table-tested.
- `comboRR` — package-level in-memory cursor (mirrors `accountSel`): `next(comboID string, n int) int` returns the current offset and advances `(cursor+1) % n`. `n==0` → 0. Concurrency-safe (mutex; turns can overlap).
- The existing loop, `claude-code` terminal path, attachment guard, budgets, and telemetry are **unchanged** — they run on `entries` whether rotated or not. Attachment turns ignore `Type` (they already pick `firstEligibleCLIEntry`, a boundary, not a strategy).

**RR + claude-code:** if rotation puts an enabled `claude-code` first, the turn goes straight to Claude (its long-budget terminal path) — consistent with the lifted invariant ("claude-code is a member like any other"). No special ordering.

## HTTP / API (`app_llm_http.go` + routes)

- `GET /llm/combos` → `{ combos: [{id,name,type,active,revision,entries:[…]}] }`.
- `POST /llm/combos` `{name,type}` → create.
- `PUT /llm/combos/{id}` `{revision,type,entries}` → replace members + type (CAS).
- `POST /llm/combos/{id}/activate` → set active.
- `DELETE /llm/combos/{id}` → delete (guarded: not active, not last).
- Keep `GET/PUT /llm/route` as a thin alias over the **active** combo for backward-compat with existing tests/telemetry, OR migrate those callers — decide in plan (prefer: `PUT /llm/route` becomes "replace the active combo's members", so the router/telemetry contract is untouched).
- All new routes go through `a.auth` + the portal-route allowlist (same as #2/#3 endpoints). Bodies carry **no** credential/config-dir (nothing to leak here — combos are provider ids + model ids only). The security canary test stays unweakened.

## UI (`webui/static/pages/combos.js`, replacing `models.js` mount)

- **Combos** page. Left: list of combos (name, type badge, member count, active radio, delete). Right/below: the member editor for the selected combo — reuse the existing row machinery from `models.js` (`moveEntry`/`removeEntry`/`addEntry`/`patchEntry`, provider/model selects, enable, up/down, live badge, status line).
- Add: **type selector** (Fallback / Round Robin) per combo; **"Combo mới"** (name + type); **activate** (radio); **delete**.
- Copy: Fallback hint = "thử từ trên xuống, dừng ở mắt xích đầu tiên trả lời được"; Round Robin hint = "mỗi lượt bắt đầu ở một mắt xích khác rồi mới fallback — chia tải giữa các tài khoản/Provider".
- Nav/label: rename the "Models" entry to "Combos" (`app-main.js` / shell nav). Green-safe framing retained.
- `models.js` → `combos.js`: extract the shared pure helpers into a small module both the row editor and tests import; delete `models.js` once `combos.js` covers it (no barrel, no dead file left behind).

## Task 11 (folded in, hedged, LAST)

Only after combos ships green:

- Unify kind: seeded provider `('claude-code', kind='claude_code')` → `claude-code` (hyphen) everywhere; `envVarFor`, `subscriptionDisplayName`, `cliDescriptors["claude-code"].kind` already use hyphen or map both.
- Route Claude through `cliAdapter` with `claudeBudget=true` (descriptor already has the flag: "true = dùng ctx gốc, không đặt dưới 25s"). Remove the `runClaude`/`appClaudeRunner` special path and the `claudeCodeProviderID` terminal branch; Claude becomes a normal member that falls through like any other.
- Sync `CONNECTABLE_KINDS` / `subscriptionKinds` to include `claude-code` so **Claude connect-in-Portal** turns on.
- **Hedge (explicit):** if removing the special path destabilises the routing seam (budget/telemetry/attachment tests) or touches the security canary, **abandon Task 11**, revert to Claude-special, and ship combos without it. The plan's Task 11 must be a self-contained final task that can be dropped.

---

## Error handling

- Store: every combo mutation inside `inLLMTx` (atomic; CAS on revision). Deleting the active/last combo → typed error, surfaced as a 4xx with a clear message. Invalid members → existing `validateLLMRoute` error, verbatim contract.
- Router: unchanged failure taxonomy (`isFallbackEligible`, credential = stop chain, others fall through). RR rotation cannot introduce a new failure mode — same entries, different start.
- UI: validation before save (reuse `draftProblem`); CAS conflict → offer reload; API errors → user-friendly Vietnamese, never raw provider strings (reuse `LLM_ERROR_FIX`).

## Testing (TDD)

- **Store** (`:memory:`): combo CRUD; `SetActiveLLMCombo` flips exactly one active; delete-active / delete-last rejected; `ReplaceLLMComboMembers` CAS + validation; `LLMRoute()` returns the active combo's type+entries.
- **RR** (pure): `rotate` table tests; `comboRR.next` advances and wraps, per-combo isolation, `n==0` safe.
- **Router**: `Run` with `Type=round_robin` starts at the rotated member and still falls through (via the `a.run` fake seam — no real CLI/HTTP). Fallback path unchanged (existing tests stay green).
- **Portal** (`node --test` DOM harness): combos list renders, activate switches, member editor saves, type selector persists. Mirror the existing `models`/`providers` test style.
- **Security/regression**: canary test, attachment→local_cli boundary, "portal carries no Zalo" — all unweakened. Full build gate (go + Portal + Zalo + package scan) green.

## File structure

| File | Change |
|------|--------|
| `store/app_schema.go` | +`llm_combos`, `llm_combo_members`, seed default, bump v4 |
| `store/app_combos.go` | **new** — combo CRUD, `SetActive`, `ReplaceLLMComboMembers`, RR-aware `LLMRoute` helpers |
| `store/app_llm.go` | `LLMRouteSnapshot` +`Type`+`ComboID`; `LLMRoute()` resolves active combo |
| `daemon/app_llm_router.go` | branch on `Type`; `rotate` + `comboRR` singleton |
| `daemon/app_llm_http.go` + routes | combo endpoints, `PUT /llm/route`→active-combo alias |
| `webui/static/pages/combos.js` | **new** — Combos page (reuses model-row helpers) |
| `webui/static/pages/models.js` | delete after extraction |
| `webui/static/app-main.js` / shell nav | "Models" → "Combos" |
| `webui/static/portal.css` | combo list + type badge styles (scoped) |
| tests alongside each | store, router, RR, Portal |

## Open questions / risks

1. **`PUT /llm/route` compatibility.** Preferred: keep it as "replace the active combo's members" so router/telemetry contracts and their tests don't move. Confirm in plan.
2. **Task 11 is genuinely risky** (removes a delicate special path). It's isolated and last; dropping it loses only Claude-as-normal-member + Claude connect, not combos.
3. **Design correction (surfaced):** the "claude-tail invariant per combo" I described at approval does **not** exist in the code (lifted in §6); RR treats all members uniformly. This spec reflects the code as-is — no claude-tail rule, only last-enabled + valid members.
