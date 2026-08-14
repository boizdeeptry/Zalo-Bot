# Multi-provider onboarding and OpenCode — research

## Decisions for plan

- Persist the selected provider set on the server. A frontend-only array is not acceptable because the
  current singleton onboarding state cannot stage or complete more than one provider and would lose the
  second toggle on refresh.
- Keep one process-global install/login slot, but do not couple that slot to selection. Any bounded number
  of catalog-supported providers may be ON; the user starts each pending row explicitly and jobs run one at
  a time.
- Store one staging row per selected runtime/provider. A pending row contains only its kind; a ready row
  owns the exact disabled account, opaque model ID, and stable fallback priority assigned by completion
  order.
- Treat the server as the source of truth for the provider catalog, selection, readiness, active install,
  and fallback order. The Portal must not maintain a duplicated provider enum or pretend a local phase
  transition succeeded.
- Preserve the existing hardened Connect manager, revision CAS, account/config ownership, process cleanup,
  test receipt, and atomic Complete semantics. Extend them to a set of stage rows instead of bypassing them.
- Test Chat must exercise the exact ordered staged fallback and record which member answered. Complete must
  activate all staged members and replace the live fallback route in one SQLite transaction.
- OpenCode `v1.18.18` is viable for a hidden synthetic integration spike, not production Zalo traffic. It
  has no OS sandbox, stores OAuth material as plaintext JSON, and persists every run in its own SQLite DB.
- OpenCode must remain `advertised=false` until a tested OS containment boundary, isolated per-account XDG
  roots, safe stdin/NDJSON transport, exact session cleanup, pinned signed binary verification, and the full
  security gate exist. Job Objects provide lifecycle cleanup only and do not satisfy sandboxing.
- Keep runtime, upstream provider, authentication connection, model, agent profile, and conversation binding
  as separate domain concepts. Do not add OpenCode-specific columns or frontend branches.
- Deliver the production multi-provider onboarding first. Implement the OpenCode adapter only behind a
  disabled feature flag and verify it with synthetic prompts/free models.

## User Constraints

- Codex and Claude Code must both be able to remain ON before either one is installed.
- Clicking `Bấm để cài.` on one row installs/connects only that provider; the other selected row remains ON
  and pending.
- There must be no extra `Bắt đầu kết nối` step in Onboarding.
- Keep the existing Tư Vấn Zalo modal on the grid background and keep the not-yet-used sidebar hidden.
- Do not put the reference product's name, branding, copy, or assets into the application UI.
- Choose the safest extensible design without asking further questions, then implement, verify, commit, and
  push it.

## Verified local product patterns

Static assets from the locally installed reference application show a server-driven catalog, separate user
enablement and readiness states, durable install/remove jobs with progress, readiness probes, and WebSocket
plus polling reconciliation. Provider/account/model management is separate from runtime selection. These
patterns are useful; special-cased runtime IDs, browser-local selection duplication, and hidden readiness
errors are not.

The current Tư Vấn Zalo implementation instead stores exactly one provider/account/model in
`app_onboarding_state`, selects one string in `onboarding.js`, and completes one Combo member. Therefore the
multi-ON requirement crosses Store, daemon, Test Chat, Complete, and Portal; it cannot be a view-only patch.

## OpenCode primary-source findings

- Baseline: `anomalyco/opencode` `v1.18.18`, released 2026-08-13, MIT licensed.
- Native signed Windows builds and npm/Scoop/Chocolatey installation are available.
- `opencode run --pure --format json --model <provider/model>` is noninteractive, accepts prompt input over
  stdin, and emits bounded JSONL events containing a session ID.
- Every run persists a session; there is no ephemeral/no-save flag. Exact session deletion exists but is not
  documented as secure SQLite/WAL erasure.
- ChatGPT Plus/Pro OAuth is officially supported. OpenCode explicitly does not sanction third-party Claude
  Pro/Max subscription routing.
- `opencode serve` exposes structured provider/auth/model/session/SSE/abort APIs and is the better eventual
  transport, protected by a random loopback-only server password.
- The official security policy states OpenCode has no sandbox and permissions are a UX mechanism. Default
  agent permissions allow most coding tools.
- Local synthetic verification proved XDG path isolation, deny-all inline config, free-model discovery,
  JSONL response parsing, and exact test-session deletion on this Windows machine. No user credential or
  production conversation was read.

Primary sources:

- <https://github.com/anomalyco/opencode/releases/tag/v1.18.18>
- <https://github.com/anomalyco/opencode/blob/v1.18.18/README.md>
- <https://github.com/anomalyco/opencode/blob/v1.18.18/LICENSE>
- <https://github.com/anomalyco/opencode/blob/v1.18.18/SECURITY.md>
- <https://github.com/anomalyco/opencode/blob/v1.18.18/packages/web/src/content/docs/cli.mdx>
- <https://github.com/anomalyco/opencode/blob/v1.18.18/packages/web/src/content/docs/providers.mdx>
- <https://github.com/anomalyco/opencode/blob/v1.18.18/packages/web/src/content/docs/server.mdx>
- <https://github.com/anomalyco/opencode/blob/v1.18.18/packages/web/src/content/docs/permissions.mdx>

## Risks that drive task order

1. Schema and migration must land before API/UI because unfinished V7 onboarding states need an exact,
   rollback-safe projection into stage rows.
2. Selection and ready-row removal need ownership-safe recovery before the Portal is allowed to persist OFF.
3. Connect/Setup must return to the provider list while pending rows remain before multi-selection UI can be
   exercised end-to-end.
4. Test Chat fingerprint/fallback and Complete must become multi-row before the final provider list is exposed
   as production-ready; otherwise the UI would promise fallback that runtime execution ignores.
5. The Portal is last among production slices so it consumes stable server contracts and can remain generic.
6. OpenCode is a separate disabled spike after the registry exists; it must not delay or weaken the Codex and
   Claude multi-provider release.
