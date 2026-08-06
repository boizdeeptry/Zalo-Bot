# Provider management and fallback routing — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Add secure Provider management and a global, hot-reloadable fallback chain to the existing Zalo Portal while preserving its legacy UI and Claude Code behavior.

**TDD mode:** yes

**Architecture:** The Go daemon owns SQLite configuration, DPAPI credentials, official HTTP Provider adapters, routing, and content-free telemetry. Each Zalo turn reads one immutable route snapshot; text-only turns may fall back across API Providers, while turns with attachments bypass every API Provider and run through Claude Code. The Portal adds a Providers editor and turns Models into a route-chain editor using the existing authenticated JSON API and legacy UI primitives.

**Tech stack:** Go and `net/http`; SQLite through the repository's existing driver; Windows DPAPI through the existing `golang.org/x/sys/windows` dependency; browser-native ES modules and Node's built-in test runner. API contracts follow the official [OpenAI Responses and Models APIs](https://platform.openai.com/docs/api-reference), [Anthropic Messages and Models APIs](https://docs.anthropic.com/en/api), [Gemini generateContent and Models APIs](https://ai.google.dev/api), and [OpenRouter chat completions and Models APIs](https://openrouter.ai/docs/api/api-reference/overview).

**Spec:** `.planning/specs/2026-08-06-provider-routing-design.md`

**Research:** skipped — no research artifact exists; the approved spec and a direct survey of the local codebase and official Provider APIs were used.

---

## File structure

### Store and secrets

- Create `appmode/overlay/internal/store/app_llm.go`: Provider, model, route, revision, and telemetry persistence APIs.
- Create `appmode/overlay/internal/store/app_llm_test.go`: public store behavior, transaction, conflict, delete guard, and snapshot tests.
- Modify `appmode/overlay/internal/store/app_schema.go`: schema version 2 and the four LLM tables plus indexes.
- Modify `appmode/overlay/internal/store/app_schema_test.go`: idempotent migration and system Provider seed assertions.
- Create `appmode/overlay/internal/daemon/app_secret.go`: package-level secret protector interface and redaction helpers.
- Create `appmode/overlay/internal/daemon/app_secret_windows.go`: current-user DPAPI encryption and decryption.
- Create `appmode/overlay/internal/daemon/app_secret_other.go`: fail-closed implementation for non-Windows builds.
- Create `appmode/overlay/internal/daemon/app_secret_test.go`: ciphertext, round-trip, corruption, and redaction behavior.

### Provider execution and routing

- Create `appmode/overlay/internal/daemon/app_llm_types.go`: Provider kinds, normalized requests/responses, error taxonomy, and adapter interface.
- Create `appmode/overlay/internal/daemon/app_llm_http.go`: bounded HTTP client and OpenAI, Anthropic, Gemini, and OpenRouter adapters.
- Create `appmode/overlay/internal/daemon/app_llm_http_test.go`: fake-server request, response, discovery, and error-classification contracts.
- Create `appmode/overlay/internal/daemon/app_llm_router.go`: immutable snapshot routing, timeouts, Claude Code delegation, and telemetry emission.
- Create `appmode/overlay/internal/daemon/app_llm_router_test.go`: fallback, stop, cancellation, attachment bypass, and telemetry behavior.
- Create `appmode/overlay/internal/daemon/app_llm_api.go`: authenticated Provider, model, route, connection-test, discovery, and status handlers.
- Create `appmode/overlay/internal/daemon/app_llm_api_test.go`: HTTP behavior, secret masking, validation, and conflict tests.
- Modify `appmode/overlay/internal/daemon/app_routes.go`: register and cookie-authorize the `/llm` routes and bootstrap the legacy model once.

### Staging seam and Portal

- Modify `scripts/BuildApp.psm1`: apply one guarded `duty.go` seam that wraps the existing `zaloRunner` per turn.
- Modify `tests/build-app.Tests.ps1`: prove the new seam is inserted exactly once and the source repository is untouched.
- Modify `appmode/overlay/internal/daemon/app_foundation_test.go`: route registration and mutation-header coverage.
- Modify `appmode/overlay/internal/daemon/app_shell_test.go`: embedded Provider page asset coverage.
- Create `appmode/overlay/internal/webui/static/pages/providers.js`: Provider list, sheet editor, credential mutations, connection test, discovery, and manual models.
- Create `appmode/tests/providers.test.mjs`: Providers service and DOM lifecycle behavior.
- Replace `appmode/overlay/internal/webui/static/pages/models.js`: fallback-chain editor with revision-aware save.
- Replace `appmode/tests/models.test.mjs`: route-chain behavior and conflict-preserved draft.
- Modify `appmode/overlay/internal/webui/static/core/router.js`: add the Providers navigation item and route.
- Modify `appmode/overlay/internal/webui/static/app-main.js`: lazy-load Providers.
- Modify `appmode/overlay/internal/webui/static/portal.css`: scoped legacy facts, sheet, route-row, status, and mobile styles.
- Modify `appmode/tests/navigation.test.mjs`, `appmode/tests/router.test.mjs`, `appmode/tests/app-main.test.mjs`, `appmode/tests/shell.test.mjs`, and `appmode/tests/ui.test.mjs`: preserve navigation, routing, stale-load, and mobile contracts.

---

### Task 1: Persist Providers, models, route revisions, and content-free telemetry

**Files:**
- Create: `appmode/overlay/internal/store/app_llm.go`
- Create: `appmode/overlay/internal/store/app_llm_test.go`
- Modify: `appmode/overlay/internal/store/app_schema.go`
- Modify: `appmode/overlay/internal/store/app_schema_test.go`

**Public behavior to verify:** The store atomically exposes a valid global route, rejects stale revisions and referenced Provider deletion, and never stores message content in telemetry.

- [ ] **Step 1: Write failing public store tests**

  Add tests that migrate an in-memory store through `New`, then call the exported store methods. Use these table-facing types consistently throughout the plan:

  ```go
  type LLMProvider struct {
      ID, Name, Kind string
      Enabled, System, CredentialConfigured bool
      CredentialCipher []byte
      LastCheckStatus, LastError string
      LastCheckedAt *time.Time
  }

  type LLMModel struct {
      ProviderID, ModelID, Name, Source string
      Available bool
  }

  type LLMRouteEntry struct {
      Position int
      ProviderID, ModelID string
      Enabled bool
  }

  type LLMRouteSnapshot struct {
      Revision int64
      Entries []LLMRouteEntry
  }
  ```

  Cover: the seeded `claude-code` system Provider; model upsert and replacement; route revision `1`; successful compare-and-swap to revision `2`; stale revision returning `ErrLLMRouteConflict`; disabled/missing Provider or model rejection; Claude Code required as the enabled final entry; referenced Provider deletion returning `ErrLLMProviderInUse`; and mutation of a returned slice not affecting the next snapshot. Record an `LLMAttempt` and query status, then inspect the schema with `PRAGMA table_info(llm_attempts)` to prove no prompt, request, response, or message column exists.

- [ ] **Step 2: Run the staged build and confirm the tests fail for the right reason**

  Run:

  ```powershell
  pwsh -NoProfile -File .\build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out (Join-Path $env:TEMP ('provider-store-red-' + [guid]::NewGuid().ToString('N')))
  ```

  Expected: FAIL during `go test` because `LLMProvider`, `LLMRouteSnapshot`, and the new `Store` methods do not exist.

- [ ] **Step 3: Implement schema version 2 and store methods**

  Add `llm_providers`, `llm_models`, `llm_route_entries`, and `llm_attempts`. Use foreign keys, unique `(provider_id, model_id)`, unique route `position`, an index on attempt start time, and `CHECK` constraints for booleans. Seed only the protected Provider metadata in migration:

  ```sql
  INSERT OR IGNORE INTO llm_providers(id, name, kind, enabled, system_provider)
  VALUES ('claude-code', 'Claude Code', 'claude_code', 1, 1);
  INSERT OR IGNORE INTO app_meta(key, value) VALUES ('llm_route_revision', '1');
  UPDATE app_meta SET value = '2' WHERE key = 'schema_version';
  ```

  Implement `LLMProviders`, `CreateLLMProvider`, `UpdateLLMProvider`, `DeleteLLMProvider`, `SetLLMCredentialCipher`, `ClearLLMCredential`, `ReplaceLLMModels`, `AddLLMModel`, `DeleteLLMModel`, `LLMRoute`, `ReplaceLLMRoute`, `BootstrapClaudeRoute`, `RecordLLMAttempt`, and `LLMStatus`. `ReplaceLLMRoute` must validate and replace entries inside one SQL transaction, update the revision with `WHERE value = expected`, and roll back on every validation or conflict error. Cap attempts to the newest 500 rows after insert.

- [ ] **Step 4: Run the staged build and confirm it passes**

  Run the Step 2 command with output prefix `provider-store-green-`.

  Expected: PASS for all Go, Portal, Zalo transport, build, and package gates.

- [ ] **Step 5: Refactor and commit**

  Keep SQL transaction helpers private and wrap errors with operation names. Re-run the build, then:

  ```powershell
  git add appmode/overlay/internal/store/app_schema.go appmode/overlay/internal/store/app_schema_test.go appmode/overlay/internal/store/app_llm.go appmode/overlay/internal/store/app_llm_test.go
  git commit -m "feat: persist provider routing configuration"
  ```

### Task 2: Protect Provider credentials with Windows DPAPI

**Files:**
- Create: `appmode/overlay/internal/daemon/app_secret.go`
- Create: `appmode/overlay/internal/daemon/app_secret_windows.go`
- Create: `appmode/overlay/internal/daemon/app_secret_other.go`
- Create: `appmode/overlay/internal/daemon/app_secret_test.go`

**Public behavior to verify:** A saved API key round-trips only for the current Windows user, corrupted ciphertext becomes a re-entry-required error, and rendered/loggable errors contain no credentials or authorization values.

- [ ] **Step 1: Write failing behavior tests**

  Define the public package contract in tests and exercise it through `protectProviderSecret`, `unprotectProviderSecret`, and `sanitizeProviderError`:

  ```go
  func TestProviderSecretRoundTripAndRedaction(t *testing.T) {
      key := []byte("sk-provider-test-secret")
      encrypted, err := protectProviderSecret(key)
      if err != nil { t.Fatal(err) }
      if bytes.Contains(encrypted, key) { t.Fatal("ciphertext contains plaintext") }
      plain, err := unprotectProviderSecret(encrypted)
      if err != nil { t.Fatal(err) }
      if !bytes.Equal(plain, key) { t.Fatal("DPAPI round trip changed key") }
      got := sanitizeProviderError("Authorization: Bearer sk-provider-test-secret; x-api-key=sk-provider-test-secret")
      if strings.Contains(got, "sk-provider-test-secret") { t.Fatalf("secret leaked: %q", got) }
  }
  ```

  Add a corruption test requiring `errors.Is(err, ErrCredentialUnreadable)` and a non-Windows test requiring a fail-closed `ErrCredentialUnsupported` rather than plaintext storage.

- [ ] **Step 2: Run and confirm RED**

  Run the Task 1 staged-build command with output prefix `provider-secret-red-`.

  Expected: FAIL because the protector functions and credential sentinel errors do not exist.

- [ ] **Step 3: Implement the minimum protector**

  Define:

  ```go
  var ErrCredentialUnreadable = errors.New("provider credential unreadable")
  var ErrCredentialUnsupported = errors.New("provider credential protection unsupported")
  ```

  On Windows call `windows.CryptProtectData` and `windows.CryptUnprotectData` with `CRYPTPROTECT_UI_FORBIDDEN`, a fixed application entropy value, and explicit zeroing of temporary plaintext buffers. Use build tags so non-Windows builds compile and return `ErrCredentialUnsupported`. Sanitize bearer tokens, `Authorization`, `x-api-key`, `api-key`, and JSON fields named `key`, `token`, or `secret`; never include a Provider response body in the sanitized result.

- [ ] **Step 4: Run and confirm GREEN**

  Run the Task 1 staged-build command with output prefix `provider-secret-green-`.

  Expected: PASS, including the Windows DPAPI round-trip.

- [ ] **Step 5: Refactor and commit**

  Keep OS-specific code isolated behind build tags and re-run the tests.

  ```powershell
  git add appmode/overlay/internal/daemon/app_secret.go appmode/overlay/internal/daemon/app_secret_windows.go appmode/overlay/internal/daemon/app_secret_other.go appmode/overlay/internal/daemon/app_secret_test.go
  git commit -m "feat: protect provider credentials with DPAPI"
  ```

### Task 3: Call and discover models from the four official API Providers

**Files:**
- Create: `appmode/overlay/internal/daemon/app_llm_types.go`
- Create: `appmode/overlay/internal/daemon/app_llm_http.go`
- Create: `appmode/overlay/internal/daemon/app_llm_http_test.go`

**Public behavior to verify:** Every API Provider receives the normalized prompt and model through its official protocol, returns normalized text/models, and maps failures into the shared fallback taxonomy.

- [ ] **Step 1: Write failing fake-server tests**

  Define and test this contract:

  ```go
  type llmRequest struct { Model, Prompt string }
  type llmResponse struct { Text string }
  type llmErrorKind string
  const (
      llmErrorNetwork llmErrorKind = "network"
      llmErrorTimeout llmErrorKind = "timeout"
      llmErrorRateLimit llmErrorKind = "rate_limit"
      llmErrorUpstream llmErrorKind = "upstream"
      llmErrorCredential llmErrorKind = "credential"
      llmErrorModel llmErrorKind = "model"
      llmErrorRequest llmErrorKind = "request"
      llmErrorPolicy llmErrorKind = "policy"
  )
  type providerAdapter interface {
      Generate(context.Context, llmRequest, []byte) (llmResponse, error)
      Test(context.Context, string, []byte) error
      Discover(context.Context, []byte) ([]store.LLMModel, error)
  }
  ```

  For each adapter, point its base URL at `httptest.Server` and assert method, fixed path, auth header, model, and prompt. Assert parsing for OpenAI Responses text, Anthropic content blocks, Gemini candidates, and OpenRouter chat choices. Assert model-list normalization and stable sorting. Table-test network error, deadline, `429`, `500`, `401`, `403`, `404`, `400`, `422`, and policy/safety responses. A malformed or empty successful response must be `upstream` and fallback-eligible; raw bodies and keys must not appear in errors.

- [ ] **Step 2: Run and confirm RED**

  Run the staged-build command with output prefix `provider-adapters-red-`.

  Expected: FAIL because the adapter interface, constructors, and normalized errors do not exist.

- [ ] **Step 3: Implement bounded adapters**

  Use one injected `http.Client`, `io.LimitReader` with a 2 MiB response cap, JSON decoders, and these fixed production endpoints:

  ```text
  OpenAI:    POST https://api.openai.com/v1/responses; GET /v1/models
  Anthropic: POST https://api.anthropic.com/v1/messages; GET /v1/models
  Gemini:    POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent; GET /v1beta/models
  OpenRouter: POST https://openrouter.ai/api/v1/chat/completions; GET /api/v1/models
  ```

  Set the required bearer, `x-api-key`, and `anthropic-version: 2023-06-01` headers. OpenRouter also gets the static `HTTP-Referer` and `X-Title` attribution headers its docs ask for (see `.planning/research/9router-RESEARCH.md` §4). OpenAI and OpenRouter speak the same wire protocol, so share one request/response codec between them rather than writing it twice — §1 of that note explains why. `Test` must perform a harmless model-list request. `Discover` returns the old cached list unchanged at the service layer when the adapter errors; the adapter itself returns an error and no partial list. Implement `isFallbackEligible` to return true only for network, timeout, rate-limit, and upstream kinds.

- [ ] **Step 4: Run and confirm GREEN**

  Run the staged-build command with output prefix `provider-adapters-green-`.

  Expected: PASS with no live network calls.

- [ ] **Step 5: Refactor and commit**

  Centralize bounded decoding and status classification without merging Provider-specific payload structs.

  ```powershell
  git add appmode/overlay/internal/daemon/app_llm_types.go appmode/overlay/internal/daemon/app_llm_http.go appmode/overlay/internal/daemon/app_llm_http_test.go
  git commit -m "feat: add official LLM provider adapters"
  ```

### Task 4: Route each message through one immutable fallback snapshot

**Files:**
- Create: `appmode/overlay/internal/daemon/app_llm_router.go`
- Create: `appmode/overlay/internal/daemon/app_llm_router_test.go`

**Public behavior to verify:** Text-only messages follow the saved chain with controlled fallback, while attachment messages call only Claude Code and no in-flight turn changes route after a concurrent save.

- [ ] **Step 1: Write failing router tests**

  Use recording fake adapters and fake Claude runner. Cover first-entry success; `429` then success; network and `5xx` fallback; credential/model/request/policy stop; no Provider twice; Claude final; all unavailable; per-API timeout; 60-second API-chain budget using injected shorter test durations; parent cancellation; telemetry ordering; and immutable snapshots. The attachment test must pass a canary path and bytes in its fixture, assert zero API adapter calls, and assert neither value appears in any serialized adapter input or telemetry.

  ```go
  runner := newAppLLMRunner(appLLMRunnerConfig{
      Store: st, Adapters: adapters, Claude: claude,
      HasAttachments: true, PerProviderTimeout: time.Second,
      APIChainTimeout: 2 * time.Second,
  })
  got, err := runner.Run(t.Context(), "prompt with retrieved snippets", nil)
  ```

- [ ] **Step 2: Run and confirm RED**

  Run the staged-build command with output prefix `provider-router-red-`.

  Expected: FAIL because `newAppLLMRunner` and router configuration do not exist.

- [ ] **Step 3: Implement the routing runner**

  Make the runner satisfy the existing daemon `zaloRunner` interface. Load and deep-copy `LLMRouteSnapshot` once at `Run` entry. If `HasAttachments`, select the final Claude entry immediately. Otherwise, create a 60-second child context for only the API portion, then call enabled entries in order with a 25-second per-Provider child context. Decrypt credentials immediately before an API call and zero the plaintext after use. Stop on non-fallback errors; record one content-free attempt per call; delegate the final system entry to the injected Claude runner. Preserve the original `step` callback only for Claude Code.

- [ ] **Step 4: Run and confirm GREEN**

  Run the staged-build command with output prefix `provider-router-green-`.

  Expected: PASS for all routing and existing answer tests.

- [ ] **Step 5: Refactor and commit**

  Keep snapshot selection, one-attempt execution, and telemetry conversion as small private functions.

  ```powershell
  git add appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_router_test.go
  git commit -m "feat: route Zalo replies through provider fallback"
  ```

### Task 5: Integrate routing into the staged daemon without restart

**Files:**
- Modify: `scripts/BuildApp.psm1` (inside `Apply-AppSeams`)
- Modify: `tests/build-app.Tests.ps1` (seam fixture section)
- Modify: `appmode/overlay/internal/daemon/app_llm_router.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`

**Public behavior to verify:** The existing Zalo answer pipeline uses the newest saved route on the next message, keeps the old snapshot for an in-flight message, migrates `data/model.txt` to Claude Code once, and requires no daemon restart.

- [ ] **Step 1: Write failing seam and runtime tests**

  Extend the PowerShell fixture to require exactly one staged replacement of:

  ```go
  a.answerZalo(ctx, deps.cfg, deps.run, threadID, question, step, reply, files...)
  ```

  with:

  ```go
  a.answerZalo(ctx, deps.cfg, a.appZaloRunner(deps.cfg, deps.run, len(files) > 0), threadID, question, step, reply, files...)
  ```

  Assert the source `duty.go` remains byte-identical and applying seams twice fails. In Go tests, save route A, create a runner, block its first adapter call, save route B, and prove the blocked turn completes on A while a new runner uses B. Bootstrap a temporary `data/model.txt` containing `sonnet`, assert the system route ends with `claude-code/sonnet`, run bootstrap again after a route edit, and prove the edit is not overwritten.

- [ ] **Step 2: Run and confirm RED**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\build-app.Tests.ps1
  ```

  Expected: FAIL because the duty seam and `appZaloRunner` do not exist.

- [ ] **Step 3: Implement the guarded runtime seam**

  Extend `Apply-AppSeams` to read and write `internal/daemon/duty.go` with `Replace-ExactlyOnce` and `Assert-SignatureAbsent`. Implement `appZaloRunner` so each invocation constructs a fresh routed runner and creates a same-package `execZaloRunner` with a copied `zaloConfig.Model` for the selected Claude model; retain the injected base runner when its model already matches so upstream fake-runner tests remain valid. Call `BootstrapClaudeRoute` from `registerAppRoutes`, reading the legacy model through the same validation used by `/kb/model`; default to the current `zaloConfig` model when the file is absent. Keep `data/model.txt` untouched for rollback.

- [ ] **Step 4: Run focused and full verification**

  Run the build-script fixture command from Step 2, then the staged-build command with output prefix `provider-runtime-green-`.

  Expected: both PASS; the source repository status is unchanged and the existing Zalo tests remain green.

- [ ] **Step 5: Refactor and commit**

  Keep the new seam adjacent to the existing answer call and avoid modifying upstream source directly.

  ```powershell
  git add scripts/BuildApp.psm1 tests/build-app.Tests.ps1 appmode/overlay/internal/daemon/app_llm_router.go appmode/overlay/internal/daemon/app_llm_router_test.go appmode/overlay/internal/daemon/app_routes.go
  git commit -m "feat: activate provider routes per Zalo turn"
  ```

### Task 6: Expose secure Provider administration APIs

**Files:**
- Create: `appmode/overlay/internal/daemon/app_llm_api.go`
- Create: `appmode/overlay/internal/daemon/app_llm_api_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Modify: `appmode/overlay/internal/daemon/app_foundation_test.go`

**Public behavior to verify:** Authenticated Portal callers can manage Providers, credentials, models, route revisions, tests, discovery, and status without ever receiving a plaintext secret.

- [ ] **Step 1: Write failing HTTP behavior tests**

  Register the app routes on `httptest.Server` and cover these exact endpoints:

  ```text
  GET    /llm/providers
  POST   /llm/providers
  PUT    /llm/providers/{id}
  DELETE /llm/providers/{id}
  PUT    /llm/providers/{id}/credential
  DELETE /llm/providers/{id}/credential
  POST   /llm/providers/test
  POST   /llm/providers/{id}/test
  POST   /llm/providers/{id}/discover
  POST   /llm/providers/{id}/models
  DELETE /llm/providers/{id}/models
  GET    /llm/route
  PUT    /llm/route
  GET    /llm/status
  ```

  Assert cookie auth and the existing `X-Agentdc-Portal: 1` mutation guard; strict JSON with a 1 MiB request cap; allowlisted Provider kinds; stable generated IDs; fixed endpoints; no secret field in any GET; blank credential on Provider update preserves the old key; explicit replace and clear; unsaved draft testing does not write; successful discovery atomically replaces discovered models while preserving manual models; failed discovery preserves all cached models; route conflict returns HTTP `409` with code `ROUTE_REVISION_CONFLICT`; and system Provider edit/delete returns HTTP `422`.

- [ ] **Step 2: Run and confirm RED**

  Run the staged-build command with output prefix `provider-api-red-`.

  Expected: FAIL because the `/llm` routes and handlers are absent.

- [ ] **Step 3: Implement handlers and safe error envelopes**

  Use existing `a.auth` and mutation conventions. Return errors only as:

  ```json
  {"error":{"code":"PROVIDER_CREDENTIAL_INVALID","message":"OpenAI cần API key hợp lệ","fields":{"credential":"Nhập lại API key"}}}
  ```

  `GET /llm/providers` returns `credential_configured` and `credential_unreadable`, never ciphertext. Accept credentials only in JSON bodies. For saved tests/discovery, load and decrypt server-side. For draft test, hold plaintext only for the call and zero the byte slice afterward. Register every exact route pattern in `appPortalRoutePatterns` so cookie access matches the existing Portal.

- [ ] **Step 4: Run and confirm GREEN**

  Run the staged-build command with output prefix `provider-api-green-`.

  Expected: PASS, including negative assertions that response bodies and captured logs do not contain test keys.

- [ ] **Step 5: Refactor and commit**

  Share strict decoding, Provider lookup, credential loading, and error writing helpers.

  ```powershell
  git add appmode/overlay/internal/daemon/app_llm_api.go appmode/overlay/internal/daemon/app_llm_api_test.go appmode/overlay/internal/daemon/app_routes.go appmode/overlay/internal/daemon/app_foundation_test.go
  git commit -m "feat: expose secure provider management API"
  ```

### Task 7: Add the legacy-style Providers page

**Files:**
- Create: `appmode/overlay/internal/webui/static/pages/providers.js`
- Create: `appmode/tests/providers.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/core/router.js`
- Modify: `appmode/overlay/internal/webui/static/app-main.js`
- Modify: `appmode/overlay/internal/webui/static/portal.css`
- Modify: `appmode/tests/navigation.test.mjs`
- Modify: `appmode/tests/router.test.mjs`
- Modify: `appmode/tests/app-main.test.mjs`
- Modify: `appmode/tests/shell.test.mjs`
- Modify: `appmode/tests/ui.test.mjs`
- Modify: `appmode/overlay/internal/daemon/app_shell_test.go`

**Public behavior to verify:** Users can add and manage API Providers from a responsive legacy-style page while credentials remain masked and Claude Code remains protected.

- [ ] **Step 1: Write failing DOM and navigation tests**

  Add `Providers` next to `Models` in expected navigation and assert `#providers` routing/lazy loading, stale-load disposal, embedded asset presence, and mobile rail behavior. In `providers.test.mjs`, inject a recording request function and assert service paths/methods; `.facts` Provider rows; status/model counts; add/edit sheet; type and name fields; password input that never receives saved plaintext; separate replace/clear actions; draft and saved connection tests; discovery success; discovery failure preserving displayed cached models; manual model addition; disabled/delete actions; protected Claude Code controls; Escape close; Tab focus trap; focus restoration; abort on dispose; and `aria-live` feedback.

  ```js
  const service = createProviderService(recordingRequest);
  await service.replaceCredential("openai-1", "sk-test");
  assert.deepEqual(calls.at(-1), {
    path: "/llm/providers/openai-1/credential",
    options: { method: "PUT", body: { credential: "sk-test" } },
  });
  ```

- [ ] **Step 2: Run and confirm RED**

  Run:

  ```powershell
  node --test appmode/tests/providers.test.mjs appmode/tests/navigation.test.mjs appmode/tests/router.test.mjs appmode/tests/app-main.test.mjs appmode/tests/shell.test.mjs appmode/tests/ui.test.mjs
  ```

  Expected: FAIL because the Providers module and route do not exist.

- [ ] **Step 3: Implement the Providers UI**

  Export `createProviderService`, `createProvidersPage`, and `mount`. Reuse `requestJSON`, `element`, `pageHeader`, `.facts`, buttons, notes, and the AI Agents sheet lifecycle. Provider type is editable only while creating. A blank credential leaves the stored key untouched; replace and clear are explicit buttons. On successful test, invoke discovery and render returned models; on discovery failure, show the sanitized message without clearing the current model list and keep manual model entry available. Scope all new CSS under `.providers-page` or `.provider-sheet`; retain the existing rail breakpoint and visible focus treatment.

- [ ] **Step 4: Run and confirm GREEN**

  Run the Step 2 command, then:

  ```powershell
  npm --prefix appmode test
  ```

  Expected: all Portal tests PASS.

- [ ] **Step 5: Refactor and commit**

  Extract only page-local render helpers; keep request behavior in the exported service for direct testing.

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/tests/providers.test.mjs appmode/overlay/internal/webui/static/core/router.js appmode/overlay/internal/webui/static/app-main.js appmode/overlay/internal/webui/static/portal.css appmode/tests/navigation.test.mjs appmode/tests/router.test.mjs appmode/tests/app-main.test.mjs appmode/tests/shell.test.mjs appmode/tests/ui.test.mjs appmode/overlay/internal/daemon/app_shell_test.go
  git commit -m "feat: add legacy provider management page"
  ```

### Task 8: Replace Models with the global fallback-chain editor

**Files:**
- Replace: `appmode/overlay/internal/webui/static/pages/models.js`
- Replace: `appmode/tests/models.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/portal.css`

**Public behavior to verify:** Users can compose, enable, and reorder a valid global Provider/model chain, save it without restart, and retain their draft if another editor wins the revision race.

- [ ] **Step 1: Write failing route-editor tests**

  Assert `createModelService` loads `/llm/providers`, `/llm/route`, and `/llm/status`, and saves with `PUT /llm/route` plus the loaded revision. DOM tests must cover add; remove; enable/disable; Provider then model selection; Up/Down buttons; keyboard activation; active Provider/model badge; validation requiring at least one enabled entry; Claude Code always enabled, final, non-removable, and non-movable; save success changing the revision without restart/reload calls; API `409` preserving all local controls and showing a reload action; reload replacing the draft only after explicit confirmation; abort/dispose; and responsive row labels.

  ```js
  await service.save({
    revision: 4,
    entries: [
      { provider_id: "openai-1", model_id: "gpt-5-mini", enabled: true },
      { provider_id: "claude-code", model_id: "sonnet", enabled: true },
    ],
  });
  assert.equal(calls.at(-1).path, "/llm/route");
  assert.equal(calls.at(-1).options.method, "PUT");
  ```

- [ ] **Step 2: Run and confirm RED**

  Run:

  ```powershell
  node --test appmode/tests/models.test.mjs
  ```

  Expected: FAIL because the current page calls `/kb/model`, exposes three fixed choices, and expects a daemon restart.

- [ ] **Step 3: Implement the route-chain page**

  Replace restart state with a local draft `{revision, entries}`. Populate choices only from enabled Providers and their available models, while retaining any currently referenced unavailable model with a warning so the user can repair it. Render each entry as a legacy panel row with native buttons for Up, Down, remove, and enable; disable illegal Claude actions. On save, send the entire draft and update revision from the response. Detect `AppAPIError` code `ROUTE_REVISION_CONFLICT`, preserve the draft exactly, and offer an explicit reload button. Poll `/llm/status` only when the page is mounted and stop polling through its `AbortController` on dispose.

- [ ] **Step 4: Run and confirm GREEN**

  Run the focused command, then `npm --prefix appmode test`.

  Expected: all Portal tests PASS and no test references `waitForServerRestart` for Models.

- [ ] **Step 5: Refactor and commit**

  Keep draft mutation functions pure so order and Claude invariants remain easy to test.

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/models.js appmode/tests/models.test.mjs appmode/overlay/internal/webui/static/portal.css
  git commit -m "feat: edit provider fallback chain in Models"
  ```

### Task 9: Verify security, regression behavior, and distributable packaging

**Files:**
- Modify: `tests/build-app.Tests.ps1`
- Modify: `appmode/overlay/internal/daemon/app_llm_api_test.go`
- Modify: `appmode/overlay/internal/daemon/app_llm_router_test.go`
- Modify: `appmode/tests/providers.test.mjs`
- Modify: `appmode/tests/models.test.mjs`

**Public behavior to verify:** A distributable build passes all existing and new tests, contains the legacy Portal and Provider routing, and contains no test credential or Zalo credential.

- [ ] **Step 1: Add failing cross-layer acceptance tests**

  Add a router acceptance test with two fake HTTP Providers where the first returns `429`, the second returns valid answer JSON expected by `answerZalo`, and status reports one fallback plus the successful Provider/model. Add package assertions that search staged text assets, configuration files, and the built binary for a unique canary API key; require zero matches. Keep the existing `/zalo`, Knowledge, Agents, CSS, credential gate, source-cleanliness, and mobile tests enabled.

  ```powershell
  $canary = 'sk-package-must-never-contain-7f36d2'
  $matches = Get-ChildItem -LiteralPath $gotPackage -Recurse -File | Select-String -SimpleMatch $canary
  if ($matches) { throw 'package contains plaintext provider credential' }
  ```

- [ ] **Step 2: Run and confirm RED**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\build-app.Tests.ps1
  ```

  Expected: FAIL until the package gate includes the Provider canary scan and the cross-layer fallback fixture reports the expected status.

- [ ] **Step 3: Complete only the integration fixes exposed by acceptance tests**

  Wire the fake adapter registry through the router test seam, ensure telemetry is committed before `Run` returns, and extend `Assert-AppPackage` or its Pester caller to scan bounded package files without printing matched secret bytes. Do not change fallback categories, add Provider types, or modify `/zalo` behavior in this task.

- [ ] **Step 4: Run the complete verification matrix**

  Run:

  ```powershell
  npm --prefix appmode test
  pwsh -NoProfile -File .\tests\build-app.Tests.ps1
  $out = Join-Path $env:TEMP ('provider-final-' + [guid]::NewGuid().ToString('N'))
  pwsh -NoProfile -File .\build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out $out
  git -C $env:ZALOBOT_REPO status --short
  git status --short
  ```

  Expected: Portal tests PASS; Pester PASS; staged Go tests, Zalo build/tests/typecheck, binary build, and package gate PASS; upstream source status is unchanged; this worktree shows only the intended task files before commit.

- [ ] **Step 5: Commit the verified integration**

  ```powershell
  git add tests/build-app.Tests.ps1 appmode/overlay/internal/daemon/app_llm_api_test.go appmode/overlay/internal/daemon/app_llm_router_test.go appmode/tests/providers.test.mjs appmode/tests/models.test.mjs
  git commit -m "test: verify provider routing package"
  ```

---

## Final review checklist

- [ ] Every Provider key is accepted only in a JSON body, encrypted before SQLite, omitted from responses, sanitized from errors/logs, and absent from the package.
- [ ] Fallback occurs only for network, timeout, `429`, `5xx`, or malformed/empty successful upstream responses; credential, model, request, and policy errors stop the chain.
- [ ] Attachment turns make zero API Provider calls and delegate directly to the final Claude Code route.
- [ ] Route writes are atomic and revision-checked; in-flight turns keep their snapshot; the next message observes the committed route without restart.
- [ ] Claude Code is seeded from the legacy model, remains last/enabled/system-protected, and `data/model.txt` is retained for rollback.
- [ ] Telemetry has Provider/model/timing/outcome/fallback metadata and no customer or generated content.
- [ ] Providers and Models match the existing legacy UI, preserve sheet accessibility, and keep the current `/zalo` and mobile rail contracts.
- [ ] The full distributable build and source-cleanliness gates pass before branch handoff.
