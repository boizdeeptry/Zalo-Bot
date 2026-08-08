# CLI provider models + 9Router-style combo model picker — plan

> **For automated executors:** invoke `:subagent-driven-development`. Steps use `- [ ]` checkboxes.

**Goal:** (A) CLI subscription providers (esp. Claude Code) show their available models automatically; (B) combo creation uses a 9Router-style **modal** to pick models from connected providers (search + click to add/remove, auto-saved), replacing the per-row dropdowns.

**Root cause found in testing:** Claude Code shows 0 models because it has **no `providerAdapter`** (routes via `runClaude`, not `cliAdapter`), so `handleLLMProviderDiscover` (which needs an adapter) can't return its static models (`cliDescriptors["claude-code"].modelSeeds` = sonnet/opus/fable). Codex only shows models after a manual Discover. Empty models → empty combo picker.

**TDD mode:** yes

**Design decisions (locked):**
- CLI model lists are STATIC (`cliDescriptors[kind].modelSeeds` for codex/gemini-cli/claude-code). Use them as the source; no live adapter/credential needed.
- **Auto-populate** so the user never needs a manual Discover: seed claude-code's models at daemon startup (it's the always-seeded provider), and populate the connecting kind's models on connect success. Also make the Discover endpoint work for claude-code (descriptor path) so the refresh button works.
- **Combo modal** picks WHICH models are members; membership add/remove **auto-saves** (PUT `/llm/combos/{id}` with the CAS revision, revision updated from the response) to match 9Router's "changes saved automatically". Member order (fallback), enable/disable, and type stay as controls and also auto-save. Keep the store/API unchanged (members are still `{provider_id, model_id, enabled}`).

**Branch:** `feat/cli-models-combo-modal` off `main` @ `4f48d94`. **Repos:** overlay only (no base-repo change).

**Test loops:** Go `go-check.ps1 -Repo $env:ZALOBOT_REPO [-Run <re>]`; Portal `npm --prefix appmode test`; full gate `build-app.ps1`. Load env at every shell.

---

## File structure

| File | Task | Change |
|------|------|--------|
| `daemon/app_llm_cli.go` | CM1 | `descriptorModels(kind) []store.LLMModel` helper (from modelSeeds) |
| `daemon/app_llm_api.go` | CM1 | `handleLLMProviderDiscover`: claude-code (no-adapter CLI kind) → return descriptor models |
| `daemon/app_llm_connect.go` | CM1 | connect success → populate the kind's models (add `ensureModels` hook to connectManager) |
| `daemon/app_routes.go` or startup | CM1 | at startup, populate claude-code's models (always-seeded provider) |
| `webui/static/pages/combos.js` | CM2 | model-picker modal + auto-save; member list (reorder/enable/remove) |
| `webui/static/pages/route-editor.js` | CM2 | reuse/adapt what's needed; the modal replaces the provider+model dropdowns |
| `webui/static/portal.css` | CM3 | modal + picker styles (scoped) |
| tests | all | Go (discover claude-code, populate), Portal (modal add/remove/auto-save) |

---

### Task CM1: CLI static model population (Claude Code + auto-populate)

**Files:** `daemon/app_llm_cli.go`, `daemon/app_llm_api.go`, `daemon/app_llm_connect.go`, `daemon/app_routes.go` (startup); tests in the matching `_test.go`.

**Behavior:** After daemon start, `GET /llm/providers/claude-code` (detail) shows Claude's models (sonnet/opus/fable). After connecting a codex/claude account, that provider shows its models. The Discover endpoint works for claude-code.

- [ ] **Step 1: failing tests**
  - `descriptorModels("claude-code")` returns 3 `store.LLMModel` (sonnet/opus/fable, `Source=discovered`, `Available=true`, `ProviderID` set by caller); `descriptorModels("openai")` → nil.
  - Discover: `handleLLMProviderDiscover` for the claude-code provider (no adapter) returns 200 with the descriptor models (not the PROVIDER_DISCOVER_FAILED error). (Mirror the existing discover endpoint test; claude-code has no adapter, so this is the new branch.)
  - Populate: after `ensureCLIProviderModels(st, "claude-code", "claude-code")`, `st.LLMModels("claude-code")` has the 3 models.

- [ ] **Step 2: run — FAIL.**

- [ ] **Step 3: implement**
  - `app_llm_cli.go`: `func descriptorModels(kind, providerID string) []store.LLMModel` — if `d, ok := cliDescriptors[kind]; ok` and `len(d.modelSeeds)>0`, map each seed to `store.LLMModel{ProviderID: providerID, ModelID: m.id, Name: m.name, Source: store.LLMModelDiscovered, Available: true}`; else nil.
  - `app_llm_api.go` `handleLLMProviderDiscover`: after loading the provider `p`, if `newLLMAdapter` has no adapter for `p.Kind` BUT `descriptorModels(p.Kind, p.ID)` is non-empty (i.e. claude-code), `ReplaceLLMModels(p.ID, discovered, descriptorModels(...))` and return them — no credential/adapter needed. (Keep the existing adapter path for codex/gemini/HTTP.)
  - `ensureCLIProviderModels(st *store.Store, providerID, kind string)`: `if models := descriptorModels(kind, providerID); len(models)>0 { st.ReplaceLLMModels(providerID, store.LLMModelDiscovered, models) }`. Idempotent (ReplaceLLMModels replaces the discovered set).
  - Connect: add `ensureModels func(kind string) error` to `connectManager` (wired to `func(k string){ ensureCLIProviderModels(a.st, k, k); return nil }` — providerID==kind for CLI subscription providers). Call it in `run()` right after `m.ensure(kind)` succeeds (before/after createAccount).
  - Startup: in `registerAppRoutes` (runs once per daemon, like `connectMgr` refresh), call `ensureCLIProviderModels(a.st, "claude-code", "claude-code")` so Claude's models show even before connecting.

- [ ] **Step 4: run — PASS**, FULL go-check green.
- [ ] **Step 5: refactor** — the codex/gemini cliAdapter.Discover already returns modelSeeds; leave it (this task only adds the claude-code + auto-populate paths, no dup).
- [ ] **Step 6: commit** `feat(models): populate CLI provider models (Claude Code discover + auto-populate on connect/startup)`.

---

### Task CM2: Combo model-picker modal + auto-save (frontend)

**Files:** `webui/static/pages/combos.js` (+ `route-editor.js` as needed), `appmode/tests/combos.test.mjs`.

**Behavior:** The selected combo's member editor shows an **"Thêm model"** button → opens a **modal**: a search box + all connected providers' models grouped by provider, each clickable; clicking a model **adds** it as a member (or removes if already present), and the change is **saved automatically** (PUT with the combo's revision, revision updated from the response). The member list (below) shows the chosen models with reorder (fallback order) + enable + remove, also auto-saved. Type selector (Fallback / Round Robin) auto-saves too.

- [ ] **Step 1: failing tests** in `combos.test.mjs` (mirror the DOM-harness pattern):
  - Opening the modal renders every connected provider's models grouped (mock `/llm/providers` with 2 providers each having models); a search filters them.
  - Clicking a model in the modal adds it as a member AND fires `PUT /llm/combos/{id}` with that member + the current revision; the response's new revision is retained (a second toggle uses the updated revision, no conflict).
  - Clicking an already-added model removes it (and auto-saves).
  - The member list reflects the additions; removing/reordering from the list also auto-saves.

- [ ] **Step 2: run — FAIL** (`npm --prefix appmode test`).

- [ ] **Step 3: implement**
  - Build the modal in `combos.js`: `openModelPicker(combo)` → an overlay with search + provider-grouped model rows (from the page's `providers` list, only providers with `models`). Each row shows provider name + model name; a checkmark/active state if the model is already a member. Click toggles membership in the draft.
  - Auto-save: on any member change (add/remove via modal, reorder/enable/remove in the list, type change), call `service.save(combo.id, {revision, type, entries})`; on success set `revision = saved.revision`; on `COMBO_REVISION_CONFLICT` reload the combo then retry once (or show the reload notice). Debounce rapid toggles if needed (a short trailing debounce is fine; per-toggle PUT is acceptable for small combos).
  - Replace the per-row provider+model dropdowns (`route-editor` selects) with: the member LIST (read-only provider:model label + reorder/enable/remove) + the modal for adding. Keep the live "đang chạy" badge + status.
  - Keep the existing combo list (left), activate, create, delete unchanged.

- [ ] **Step 4: run — PASS**, Portal green; FULL go-check (static-embed compiles).
- [ ] **Step 5: refactor** — if `route-editor.js` helpers are now unused by combos, trim; keep what the member list reuses.
- [ ] **Step 6: commit** `feat(portal): 9Router-style combo model picker modal with auto-save`.

---

### Task CM3: CSS + full build gate + E2E

- [ ] **Step 1: CSS** — scoped `.combos-page` modal + picker styles (overlay, search, grouped rows, active state, member list), light+dark, inside the `combos:begin/end` markers (the scoping test covers it). `.pv-*`-style tokens.
- [ ] **Step 2: full build gate** — `build-app.ps1 … -Out F:\dist\_verify-20260808`. 7/7, canary clean. (Re-run once on the known `TestStreamThroughTheConfiguredServer` flake.)
- [ ] **Step 3: E2E** — from the packaged app (login already available): confirm Claude Code detail shows its 3 models; open a combo, add a model via the modal, confirm it persists (reload) and the member list updates. Use a specific spawned PID only.
- [ ] **Step 4: commit** `style(portal): combo model-picker modal styles; full gate green`.

---

## Final review
Whole-branch review: model-populate paths (claude-code discover + startup + connect) correct + idempotent; the modal auto-save handles revision/CAS conflict; security invariants (no config_dir/credential in bodies; auth-wrapped) intact; combos store/API unchanged. Full gate green → `/ship` (merge to main).

## Risks
- **Auto-save + CAS revision** — rapid toggles could conflict; handle `COMBO_REVISION_CONFLICT` by re-reading the combo and retrying, and keep the revision synced from each save response.
- **claude-code models before connect** — startup-populated so they show even with 0 accounts; routing to Claude with 0 accounts still falls back to default (unchanged) — the model list is informational for the combo picker.
