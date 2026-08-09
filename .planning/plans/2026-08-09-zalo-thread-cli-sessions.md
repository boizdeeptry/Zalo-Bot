# Zalo per-thread Claude CLI sessions — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Preserve one resumable Claude Code session per Zalo person/group, send only prompt deltas after bootstrap, and rotate safely before context growth hurts latency.

**TDD mode:** yes

**Architecture:** SQLite stores the `thread_id` to Claude session mapping, generation, usage estimate, turn count, and message cursor. A reference-counted keyed gate serializes the complete answer pipeline per thread, while a session-aware runner chooses `--session-id` or `--resume`, builds bootstrap/delta prompts, and rotates or recovers without changing the existing answer validation and outbox flow. All production changes remain in the build overlay; guarded seams are applied only to the temporary stage.

**Tech stack:** Go 1.25-compatible standard library, existing SQLite store/driver, Claude Code 2.1.222 headless stream JSON, PowerShell/Pester staging tests.

**Spec:** `.planning/specs/2026-08-09-zalo-thread-cli-sessions-design.md`

**Research:** skipped — no research artifact exists; the approved spec, local Claude CLI help, and current upstream implementation were inspected directly.

---

## File structure

- Create `appmode/overlay/internal/store/app_zalo_cli_session.go`: persistent session mapping, compare-and-swap updates, cursor and delta queries.
- Create `appmode/overlay/internal/store/app_zalo_cli_session_test.go`: migration, isolation, generation, monotonic cursor, delta ordering, and content-free schema tests.
- Modify `appmode/overlay/internal/store/app_schema.go`: add the `app_zalo_cli_sessions` table and schema version update.
- Modify `appmode/overlay/internal/store/app_schema_test.go`: idempotent migration and version assertions.
- Create `appmode/overlay/internal/daemon/app_zalo_thread_gate.go`: reference-counted keyed mutex.
- Create `appmode/overlay/internal/daemon/app_zalo_thread_gate_test.go`: same-thread serialization, cross-thread concurrency, cancellation cleanup.
- Create `appmode/overlay/internal/daemon/app_zalo_session_prompt.go`: prompt fingerprint, bootstrap/delta selection, token estimate, and rotation policy.
- Create `appmode/overlay/internal/daemon/app_zalo_session_prompt_test.go`: delta contents, fingerprint, usage, and rotation tests.
- Create `appmode/overlay/internal/daemon/app_zalo_session_runner.go`: session-aware Claude process execution and one-time resume recovery.
- Create `appmode/overlay/internal/daemon/app_zalo_session_runner_test.go`: argv, session reuse/isolation, usage parsing, recovery, and secret/content-free persistence tests.
- Create `appmode/overlay/internal/daemon/app_zalo_session_hook.go`: gate-aware `answerZalo` wrapper and structured runner dispatch used by staged seams.
- Create `appmode/overlay/internal/daemon/app_zalo_session_hook_test.go`: cursor timing, operator catch-up, restart persistence, and legacy fake-runner compatibility.
- Modify `scripts/BuildApp.psm1`: insert two exact, fail-closed `duty.go` seams.
- Modify `tests/build-app.Tests.ps1`: assert seams exactly once and upstream source byte-for-byte unchanged.
- Modify `.planning/plans/2026-08-06-provider-routing.md`: make future Claude routing delegate to the session runner instead of recreating stateless Claude runners.

---

### Task 1: Persist a content-free Claude session mapping per Zalo thread

**Files:**
- Create: `appmode/overlay/internal/store/app_zalo_cli_session.go`
- Create: `appmode/overlay/internal/store/app_zalo_cli_session_test.go`
- Modify: `appmode/overlay/internal/store/app_schema.go`
- Modify: `appmode/overlay/internal/store/app_schema_test.go`

**Public behavior to verify:** Store callers can independently create, resume, rotate, and advance sessions for each Zalo thread, and retrieve only messages after the saved cursor.

- [ ] **Step 1: Write failing behavior tests**

  Test this public contract through an in-memory `Store`:

  ```go
  type ZaloCLISession struct {
      ThreadID, ClaudeSessionID, Model, PromptFingerprint, LastError string
      Generation, ContextTokens, TurnCount, MessageCursor int64
      RotateBeforeNext bool
      CreatedAt, UpdatedAt time.Time
  }

  type ZaloDeltaMessage struct {
      ID int64
      Message ipc.ZaloMessage
  }
  ```

  Cover `ZaloCLISession(threadID)`, `CreateZaloCLISession`, `ReplaceZaloCLISession(expectedGeneration, next)`, `CompleteZaloCLITurn`, `MarkZaloCLISessionForRotation`, `LatestZaloMessageID`, and `ZaloMessagesAfter`. Assert different user/group thread IDs never share mappings, stale generation returns `ErrZaloCLISessionConflict`, cursor never decreases, delta rows are old-to-new, and the session table has no prompt/response/message/body/content/tool-output column.

- [ ] **Step 2: Run the staged build and confirm RED**

  ```powershell
  $out = Join-Path $env:TEMP ('zalo-session-store-red-' + [guid]::NewGuid().ToString('N'))
  pwsh -NoProfile -File .\build-app.ps1 -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' -PersonaSource 'D:\TuvanZalo\brain\reference\persona' -Out $out
  ```

  Expected: FAIL during Go compilation because the session types and store methods do not exist.

- [ ] **Step 3: Implement the minimum schema and store API**

  Add the exact table from the spec, an index on `updated_at`, and update `schema_version` without changing upstream `zalo_messages`. `CreateZaloCLISession` inserts generation 1. `ReplaceZaloCLISession` updates only when generation matches and increments it in the same statement. `CompleteZaloCLITurn` uses `MAX(message_cursor, ?)` semantics and increments `turn_count`. `ZaloMessagesAfter` joins attachments using the existing store conventions and applies a bounded limit of 100 rows.

- [ ] **Step 4: Run the staged build and confirm GREEN**

  Repeat Step 2 with output prefix `zalo-session-store-green-`.

  Expected: full build checkpoint PASS.

- [ ] **Step 5: Refactor while green**

  Keep row scanning and timestamp parsing private; copy returned values so callers cannot mutate store state. Re-run Step 4.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_schema.go appmode/overlay/internal/store/app_schema_test.go appmode/overlay/internal/store/app_zalo_cli_session.go appmode/overlay/internal/store/app_zalo_cli_session_test.go
  git commit -m "feat: persist Zalo Claude sessions by thread"
  ```

### Task 2: Serialize complete answer turns per thread without blocking other threads

**Files:**
- Create: `appmode/overlay/internal/daemon/app_zalo_thread_gate.go`
- Create: `appmode/overlay/internal/daemon/app_zalo_thread_gate_test.go`

**Public behavior to verify:** Two turns for one thread run in order, while turns for different threads may run concurrently and all temporary lock entries are reclaimed.

- [ ] **Step 1: Write failing gate behavior tests**

  Exercise this interface:

  ```go
  type appZaloThreadGate struct {
      mu sync.Mutex
      entries map[string]*appZaloThreadGateEntry
  }

  func (g *appZaloThreadGate) Acquire(ctx context.Context, threadID string) (release func(), err error)
  func (g *appZaloThreadGate) Len() int
  ```

  Start two goroutines for `thread-a` and prove the second cannot enter before the first release. Start `thread-a` and `thread-b` and prove both enter before either releases. Cancel a waiter and assert `context.Canceled`, then release the holder and wait until `Len()` is zero. Call a returned release twice and assert it is harmless.

- [ ] **Step 2: Run and confirm RED**

  Run the Task 1 staged-build command with prefix `zalo-session-gate-red-`.

  Expected: FAIL because `appZaloThreadGate` is undefined.

- [ ] **Step 3: Implement the reference-counted keyed gate**

  Use a small per-key buffered channel semaphore plus reference count protected by the map mutex. Increment references before waiting, decrement on cancellation or release, and delete only when references reach zero. Reject an empty thread ID. Do not hold the map mutex while waiting for a thread token.

- [ ] **Step 4: Run and confirm GREEN**

  Run the staged build with prefix `zalo-session-gate-green-`.

  Expected: all gate and existing race-sensitive daemon tests PASS.

- [ ] **Step 5: Refactor while green**

  Keep acquisition cleanup in one helper and run the focused test under `go test -race` in a staged checkout if the platform supports it.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_zalo_thread_gate.go appmode/overlay/internal/daemon/app_zalo_thread_gate_test.go
  git commit -m "feat: serialize Zalo turns per thread"
  ```

### Task 3: Build bootstrap/delta prompts and deterministic rotation decisions

**Files:**
- Create: `appmode/overlay/internal/daemon/app_zalo_session_prompt.go`
- Create: `appmode/overlay/internal/daemon/app_zalo_session_prompt_test.go`

**Public behavior to verify:** A new/rotated session receives the full safe prompt, while a resumable session receives only new messages, current knowledge, and relevant files, and rotation occurs at the approved limits.

- [ ] **Step 1: Write failing prompt-policy tests**

  Define these package contracts in the tests:

  ```go
  const appZaloContextRotateTokens int64 = 130000
  const appZaloMaxSessionTurns int64 = 48

  type appZaloSessionPromptInput struct {
      Config zaloConfig
      Question string
      CurrentZaloMsgID string
      History []ipc.ZaloMessage
      Delta []store.ZaloDeltaMessage
      Found []passage
      Files []ipc.ZaloAttachment
  }

  func appZaloPromptFingerprint(zc zaloConfig, threadID string) string
  func buildAppZaloDeltaPrompt(appZaloSessionPromptInput) string
  func appZaloShouldRotate(store.ZaloCLISession, model, fingerprint string) bool
  ```

  Assert bootstrap still comes from `buildConsultPrompt`; delta includes messages after cursor as JSON Lines inside fixed untrusted boundaries, current retrieved quotes in their existing citable representation, and file metadata in a separate untrusted JSONL boundary. Assert role is generated from trusted direction/operator fields rather than display name; newline and closing-tag payloads cannot escape JSON; display name, body and title are capped UTF-8-safely at 300 bytes; current question is suppressed only when an inbound delta record has the same non-empty durable `ZaloMsgID` as `CurrentZaloMsgID`; same text with another ID, an empty identity, or a current ID absent from a full 100-row page must append the fallback. Assert generated `CẢNH BÁO CỦA TRANG NÀY` directives retain authority in the final trust reminder and the bootstrap persona marker is absent from delta. Fingerprints must change for model, persona, roster, overlay, cite mode, `OwnerUID`, KB roots, or contract version. Rotation fires at 130,000 tokens, 48 turns, a changed fingerprint/model, or an explicit flag, but not one unit below each limit.

- [ ] **Step 2: Run and confirm RED**

  Run the staged build with prefix `zalo-session-prompt-red-`.

  Expected: FAIL because prompt policy functions do not exist.

- [ ] **Step 3: Implement prompt policy**

  Hash a versioned, length-delimited sequence with SHA-256 so concatenation cannot collide, including `OwnerUID` because bootstrap embeds it in the authorization contract. Read persona, roster and thread overlay through existing helpers. Serialize transcript records with application-generated `role`, capped `display_name` and capped `body` as JSON Lines inside `<untrusted_conversation_jsonl>`; serialize capped file metadata separately inside `<untrusted_customer_files_jsonl>`. Keep retrieved KB passages in the existing citable representation, cap the conversation JSONL payload at 16 KiB while preserving newest records, and append the fixed trust reminder after every external-material section. The reminder must preserve the authority of application-generated page-warning directives while rejecting conversation/file data as instructions. Dedupe the current event by non-empty durable Zalo message identity only, never by text. Use byte/3 as conservative token estimate only when measured usage is unavailable.

- [ ] **Step 4: Run and confirm GREEN**

  Run the staged build with prefix `zalo-session-prompt-green-`.

  Expected: all prompt and existing prompt-contract tests PASS.

- [ ] **Step 5: Refactor while green**

  Keep role mapping in one private helper and test every direction/operator combination, including an inbound display name equal to `người trực`. Re-run the tests.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_zalo_session_prompt.go appmode/overlay/internal/daemon/app_zalo_session_prompt_test.go
  git commit -m "feat: build bounded Zalo session prompts"
  ```

### Task 4: Execute Claude with session creation, resume, usage parsing, and recovery

**Files:**
- Create: `appmode/overlay/internal/daemon/app_zalo_session_runner.go`
- Create: `appmode/overlay/internal/daemon/app_zalo_session_runner_test.go`

**Public behavior to verify:** The runner creates a named Claude session once, resumes it on later turns, captures optional usage, and retries only a missing/corrupt resume with a fresh session.

- [ ] **Step 1: Write failing runner tests against a fake Claude executable**

  Inject process execution through:

  ```go
  type appZaloClaudeCommand func(context.Context, string, []string, string, []string) (io.ReadCloser, io.ReadCloser, func() error, error)

  type appZaloRunResult struct {
      Answer string
      ContextTokens int64
      UsageMeasured bool
      OutputBytes int64
  }
  ```

  Record argv/stdin and return stream-json fixtures. Assert a new session has `-p --session-id <uuid>` and no `--resume`; an existing session has `-p --resume <same uuid>` and no `--session-id`; model, add-dir, allowed tools, stream-json and environment stripping remain intact. Parse `usage.input_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens`, and `output_tokens`. Unknown/missing usage must not fail the answer. A stderr fixture matching missing/corrupt session triggers one fresh bootstrap attempt; timeout, network, permission, model, and generic exit errors trigger zero retries. Error strings are clipped and persisted only as safe codes.

- [ ] **Step 2: Run and confirm RED**

  Run the staged build with prefix `zalo-session-runner-red-`.

  Expected: FAIL because the session runner and usage result types do not exist.

- [ ] **Step 3: Implement the session-aware process runner**

  Reuse `agent.ConsultReadOnly`, `consultArgv`, `parseStreamLine`, scanner limit, work directory, thinking-token environment and cancellation behavior. For resume argv, replace the exact adjacent `--session-id <uuid>` pair from `consultArgv` with `--resume <uuid>`; fail if the pair is absent or repeated. Parse usage in parallel with existing step/result parsing. Classify resume failure from a narrow allowlist of sanitized CLI messages and never store raw stderr.

- [ ] **Step 4: Run and confirm GREEN**

  Run the staged build with prefix `zalo-session-runner-green-`.

  Expected: runner fixtures and the complete existing suite PASS with no live Claude call.

- [ ] **Step 5: Refactor while green**

  Keep argv transformation, stream parsing, and safe error classification separate. Re-run all daemon tests.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_zalo_session_runner.go appmode/overlay/internal/daemon/app_zalo_session_runner_test.go
  git commit -m "feat: resume Claude sessions for Zalo"
  ```

### Task 5: Orchestrate session state, message cursors, and prompt deltas

**Files:**
- Create: `appmode/overlay/internal/daemon/app_zalo_session_hook.go`
- Create: `appmode/overlay/internal/daemon/app_zalo_session_hook_test.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_runner.go`

**Public behavior to verify:** The first successful turn records a session and cursor; the next turn resumes with only new messages, survives daemon object recreation, and rotates without an extra model call.

- [ ] **Step 1: Write failing orchestration tests**

  Exercise these seam-facing methods:

  ```go
  func (a *api) appAnswerZalo(deps *zaloDeps, threadID, question string, reply ipc.ZaloOutboxDraft, files []ipc.ZaloAttachment) error
  func (a *api) appRunZalo(ctx context.Context, run zaloRunner, zc zaloConfig, threadID, question, currentZaloMsgID string, history []ipc.ZaloMessage, found []passage, files []ipc.ZaloAttachment, step func(string)) (string, error)
  ```

  With a recording command fixture, run two turns on the same thread and assert one session ID, then `--resume`; add an operator message between them and assert it is in delta. Recreate `api` with the same store and assert it still resumes. Run a second thread and assert a different UUID. Seed context at 129,999 then 130,000 tokens and assert only the latter creates a new generation. Assert 48 turns, fingerprint/model change and resume-not-found also rotate. Assert `LatestZaloMessageID` is committed only after the answer pipeline completes and no separate summary/model invocation occurs.

- [ ] **Step 2: Run and confirm RED**

  Run the staged build with prefix `zalo-session-hook-red-`.

  Expected: FAIL because the seam-facing orchestration methods are absent.

- [ ] **Step 3: Implement orchestration**

  `appAnswerZalo` acquires the thread gate before starting the existing per-turn timeout, then calls `answerZalo` with a session-capable runner and advances the cursor after successful store completion. It extracts the current Zalo `msgId` from the last batched event's `reply.ReplyQuote` and passes that stable identity into `appRunZalo`; Task 3 does not parse reply quotes. `appRunZalo` detects that runner through a private structured-run interface; fake runners and API Provider runners continue to receive `buildConsultPrompt` through their existing `Run` method. Use compare-and-swap generation on every state update; a conflict reloads state once rather than overwriting a concurrent generation.

- [ ] **Step 4: Run and confirm GREEN**

  Run the staged build with prefix `zalo-session-hook-green-`.

  Expected: orchestration, answer, citation, attachment, memory, handoff and outbox tests PASS.

- [ ] **Step 5: Refactor while green**

  Keep state selection, runner execution and completion update in separate private functions. Re-run the suite.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_zalo_session_hook.go appmode/overlay/internal/daemon/app_zalo_session_hook_test.go appmode/overlay/internal/daemon/app_zalo_session_runner.go
  git commit -m "feat: orchestrate Zalo thread sessions"
  ```

### Task 6: Apply guarded staging seams without modifying upstream source

**Files:**
- Modify: `scripts/BuildApp.psm1`
- Modify: `tests/build-app.Tests.ps1`

**Public behavior to verify:** Packaged builds activate thread sessions exactly once while the tracked upstream `duty.go` remains byte-identical.

- [ ] **Step 1: Write failing Pester seam tests**

  Extend the fixture to require replacement of the existing timeout/answer block with `a.appAnswerZalo(...)`, and replacement of:

  ```go
  raw, err := run.Run(ctx, buildConsultPrompt(pz, question, history, found, files...), step)
  ```

  by:

  ```go
  raw, err := a.appRunZalo(ctx, run, pz, threadID, question, appZaloCurrentMsgID(reply.ReplyQuote), history, found, files, step)
  ```

  Assert both signatures occur exactly once in the stage, neither appears in source, source hashes match before/after, missing needles fail before partial writes, and applying seams twice fails with the inserted-signature message.

- [ ] **Step 2: Run and confirm RED**

  ```powershell
  $pester = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru
  if ($pester.FailedCount -ne 0) { throw "Pester failed: $($pester.FailedCount)" }
  ```

  Expected: FAIL because `Apply-AppSeams` does not patch `duty.go`.

- [ ] **Step 3: Implement atomic `duty.go` seam application**

  Read all four target files before transforming any. Use `Replace-ExactlyOnce` and `Assert-SignatureAbsent` for both new needles, preserve the source newline style, and write all staged files only after every replacement succeeds. Ensure the timeout is constructed inside `appAnswerZalo` after gate acquisition.

- [ ] **Step 4: Run focused and full verification**

  Run the Pester command, then the staged build with prefix `zalo-session-seams-green-`.

  Expected: Pester and the full package build PASS; upstream status is unchanged.

- [ ] **Step 5: Refactor while green**

  Keep the duty transformation adjacent to existing server/store/Zalo transformations and re-run Pester.

- [ ] **Step 6: Commit**

  ```powershell
  git add scripts/BuildApp.psm1 tests/build-app.Tests.ps1
  git commit -m "build: activate Zalo thread session seams"
  ```

### Task 7: Reconcile Provider routing and verify the distributable package

**Files:**
- Modify: `.planning/plans/2026-08-06-provider-routing.md`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_hook_test.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_runner_test.go`
- Modify: `tests/build-app.Tests.ps1`

**Public behavior to verify:** The shipped package preserves session continuity and all existing Portal/Zalo behavior, while the future Provider plan explicitly keeps API turns stateless and Claude turns resumable.

- [ ] **Step 1: Add failing acceptance assertions**

  Add a staged integration test that sends two synthetic turns for one thread through the seam, captures fake Claude argv and proves create-then-resume. Add a different-thread assertion and a package scan proving no prompt canary, response canary, raw stderr canary, Zalo credential or Provider credential is present.

- [ ] **Step 2: Run and confirm RED**

  Run Pester and the staged build.

  Expected: FAIL until the full staged path preserves the same session ID and the package scan covers every named canary.

- [ ] **Step 3: Reconcile the Provider plan and integration behavior**

  Change Provider Task 5 so API adapters remain stateless, attachment/API fallback into Claude delegates to the existing session-aware structured runner, model/fingerprint changes rotate the thread session, and no second `duty.go` answer seam is introduced. Make only integration fixes required by the acceptance assertions.

- [ ] **Step 4: Run the complete verification matrix**

  ```powershell
  npm --prefix appmode test
  $pester = Invoke-Pester -Script .\tests\build-app.Tests.ps1 -PassThru
  if ($pester.FailedCount -ne 0) { throw "Pester failed: $($pester.FailedCount)" }
  $out = Join-Path $env:TEMP ('zalo-thread-session-final-' + [guid]::NewGuid().ToString('N'))
  pwsh -NoProfile -File .\build-app.ps1 -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' -PersonaSource 'D:\TuvanZalo\brain\reference\persona' -Out $out
  git -C 'C:\Users\manva\OneDrive\Máy tính\agentdc' status --short
  git status --short
  ```

  Expected: Portal tests, Pester, staged Go tests, Zalo build/tests/typecheck, binary build and package gates PASS; upstream status remains unchanged; only intended files remain before commit.

- [ ] **Step 5: Refactor and inspect the final diff**

  Remove duplicated fixtures, run `git diff --check`, and confirm the provider-plan edit describes only the already-approved integration boundary. Re-run Step 4 after any cleanup.

- [ ] **Step 6: Commit**

  ```powershell
  git add .planning/plans/2026-08-06-provider-routing.md appmode/overlay/internal/daemon/app_zalo_session_hook_test.go appmode/overlay/internal/daemon/app_zalo_session_runner_test.go tests/build-app.Tests.ps1
  git commit -m "test: verify resumable Zalo thread sessions"
  ```

---

## Final review checklist

- [ ] Same Zalo thread creates once and resumes the same UUID; different threads never share it.
- [ ] Prompt delta includes all messages after the cursor and does not repeat the bootstrap persona/contract.
- [ ] Context, turn, fingerprint, model and explicit flags rotate before the next turn without an extra model call.
- [ ] Missing/corrupt resume retries once; all other failures retain existing handoff behavior and do not double-spend.
- [ ] Same-thread turns serialize from history read through outbox/message completion; different threads remain concurrent.
- [ ] Daemon recreation preserves mappings in SQLite and resumes when Claude transcripts exist.
- [ ] Session storage and package contain no prompt, answer, tool output, raw stderr, or credentials.
- [ ] Existing citation, attachment, memory, handoff, Portal, mobile, Zalo transport and package gates pass.
- [ ] Provider routing delegates Claude selections to the session runner and does not add a competing duty seam.
