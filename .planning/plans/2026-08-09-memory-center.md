# Memory Center — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Build the legacy-styled Portal Memory Center and make per-thread memory plus global lessons refresh exactly once when their revisions change in a resumed Zalo CLI session.

**TDD mode:** yes

**Architecture:** Extend the overlay-owned SQLite schema with management metadata and monotonic revision cursors, expose transactional store methods through authenticated Portal routes, then use those cursors to append an authoritative replacement snapshot only when a resumed session is stale. The UI remains framework-free and lazy-loaded through the existing Portal router; packaging continues to stage the upstream repository and apply narrow seams without modifying upstream source.

**Tech stack:** Go 1.24, `database/sql` with `modernc.org/sqlite`, `net/http` method-pattern routes, vanilla ES modules, Node's built-in test runner, PowerShell build/package harness.

**Spec:** `.planning/specs/2026-08-09-memory-center-design.md`

**Research:** skipped — the approved design is based on a direct survey of the current overlay, upstream store, and package pipeline.

---

## File structure

- `appmode/overlay/internal/store/app_schema.go` owns schema version 3, idempotent column creation, revision table/triggers, and migration seeding.
- `appmode/overlay/internal/store/app_memory.go` owns Portal memory models, validation, transactional CRUD, list/search queries, revision reads, source previews, and pinned-first prompt selection.
- `appmode/overlay/internal/store/app_zalo_cli_session.go` owns the two revision cursors persisted with each content-free CLI session.
- `appmode/overlay/internal/daemon/app_memory.go` owns HTTP request/response contracts and safe error-to-status mapping for the Memory API.
- `appmode/overlay/internal/daemon/app_routes.go` owns authenticated route registration and cookie reachability.
- `appmode/overlay/internal/daemon/app_zalo_session_prompt.go` owns rendering of authoritative replacement snapshots in a delta prompt.
- `appmode/overlay/internal/daemon/app_zalo_session_hook.go` owns pre-call revision capture and generation-safe persistence after a successful turn.
- `appmode/overlay/internal/webui/static/pages/memory.js` owns the Memory service, page state, split-pane rendering, mutations, dialog lifecycle, and disposal.
- `appmode/overlay/internal/webui/static/core/router.js` and `app-main.js` make Memory a real lazy-loaded route.
- `appmode/overlay/internal/webui/static/portal.css` contains styles scoped to `.memory-page` and `[data-memory-overlay]`.
- `scripts/BuildApp.psm1` applies the narrow `/zalo?thread=` selection seam to staged upstream assets.
- `tests/run-overlay-go-tests.ps1` creates a disposable staged upstream tree and runs focused overlay Go tests without modifying upstream.
- Store, daemon, session, UI, navigation, and package tests remain next to the code they exercise.

---

### Task 1: Schema v3 preserves old data and creates revision cursors

**Files:**
- Modify: `appmode/overlay/internal/store/app_schema_test.go`
- Modify: `appmode/overlay/internal/store/app_schema.go`
- Modify: `appmode/overlay/internal/store/app_zalo_cli_session_test.go`
- Modify: `appmode/overlay/internal/store/app_zalo_cli_session.go`
- Create: `tests/run-overlay-go-tests.ps1`

**Public behavior to verify:** Opening a fresh or v2 database twice yields the same schema v3, retains existing memory/lessons, preserves a future metadata version, and keeps the CLI session table content-free while adding two numeric revision cursors.

- [ ] **Step 1: Write failing migration and session-cursor tests**

  Extend the schema tests to create the upstream tables before `migrateApp`, seed legacy rows, and assert the exact new columns, seeded revisions, trigger behavior, and idempotency. Update the session lifecycle test so completion carries the revisions observed before the model call.

  ```go
  func TestMigrateAppV3SeedsLegacyMemoryRevisionsAndIsIdempotent(t *testing.T) {
      db := newAppSchemaTestDB(t)
      mustExec(t, db, `INSERT INTO zalo_memory(thread_id, uid, text, created_at)
          VALUES ('thread-a', '', 'thích nhận buổi sáng', '2026-08-09T01:00:00Z')`)
      mustExec(t, db, `INSERT INTO zalo_lessons(thread_id, bot_text, better, note, created_at)
          VALUES ('thread-a', 'cũ', 'mới', 'ngắn hơn', '2026-08-09T01:00:00Z')`)

      if err := migrateApp(db); err != nil { t.Fatal(err) }
      if err := migrateApp(db); err != nil { t.Fatal(err) }

      assertColumnNames(t, db, "zalo_memory", []string{
          "id", "thread_id", "uid", "text", "created_at",
          "pinned", "source", "source_message_id", "updated_at",
      })
      assertColumnNames(t, db, "zalo_lessons", []string{
          "id", "thread_id", "bot_text", "better", "note", "created_at",
          "pinned", "updated_at",
      })
      assertRevision(t, db, "thread", "thread-a", 1)
      assertRevision(t, db, "lessons", "", 1)
  }

  func TestCompleteZaloCLITurnPersistsObservedMemoryRevisions(t *testing.T) {
      s := newStore(t)
      created, err := s.CreateZaloCLISession(ZaloCLISession{
          ThreadID: "thread-a", ClaudeSessionID: "session-a", Model: "sonnet",
          PromptFingerprint: "persona-v1",
      })
      if err != nil { t.Fatal(err) }

      got, err := s.CompleteZaloCLITurn("thread-a", created.Generation, 4096, 12, 7, 3)
      if err != nil { t.Fatal(err) }
      if got.MemoryRevision != 7 || got.LessonsRevision != 3 {
          t.Fatalf("revisions = %d/%d; want 7/3", got.MemoryRevision, got.LessonsRevision)
      }
  }
  ```

- [ ] **Step 2: Run the focused tests and confirm they fail for the right reason**

  Run in a staged upstream copy created by the existing test harness:

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'TestMigrateAppV3|TestCompleteZaloCLITurnPersistsObservedMemoryRevisions'
  ```

  Expected: FAIL because schema version 3, new columns, revisions, and the six-argument completion method do not exist yet. If the helper script is absent, add it as the first test-harness change using `New-AppStage` and `Apply-AppSeams`; it must print the retained stage path on failure and run `go test ./internal/store -run <pattern>` there.

- [ ] **Step 3: Implement the minimum schema and cursor changes**

  Set `appSchemaVersion` to 3. Keep `CREATE TABLE IF NOT EXISTS` for fresh databases, then use `PRAGMA table_info` through an `appEnsureColumn` helper before creating triggers. Seed one revision for each legacy-populated scope without incrementing existing revision rows.

  ```go
  const appSchemaVersion int64 = 3

  type appColumnMigration struct {
      table      string
      column     string
      definition string
  }

  var appV3Columns = []appColumnMigration{
      {"zalo_memory", "pinned", "INTEGER NOT NULL DEFAULT 0"},
      {"zalo_memory", "source", "TEXT NOT NULL DEFAULT 'agent'"},
      {"zalo_memory", "source_message_id", "INTEGER NOT NULL DEFAULT 0"},
      {"zalo_memory", "updated_at", "TEXT NOT NULL DEFAULT ''"},
      {"zalo_lessons", "pinned", "INTEGER NOT NULL DEFAULT 0"},
      {"zalo_lessons", "updated_at", "TEXT NOT NULL DEFAULT ''"},
      {"app_zalo_cli_sessions", "memory_revision", "INTEGER NOT NULL DEFAULT 0"},
      {"app_zalo_cli_sessions", "lessons_revision", "INTEGER NOT NULL DEFAULT 0"},
  }

  const appMemoryRevisionSchema = `
  CREATE TABLE IF NOT EXISTS app_memory_revisions (
    scope TEXT NOT NULL,
    scope_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    PRIMARY KEY(scope, scope_id)
  );
  CREATE TRIGGER IF NOT EXISTS app_zalo_memory_insert_revision
  AFTER INSERT ON zalo_memory BEGIN
    INSERT INTO app_memory_revisions(scope, scope_id, revision)
    VALUES ('thread', NEW.thread_id, 1)
    ON CONFLICT(scope, scope_id) DO UPDATE SET revision = revision + 1;
  END;
  CREATE TRIGGER IF NOT EXISTS app_zalo_lessons_insert_revision
  AFTER INSERT ON zalo_lessons BEGIN
    INSERT INTO app_memory_revisions(scope, scope_id, revision)
    VALUES ('lessons', '', 1)
    ON CONFLICT(scope, scope_id) DO UPDATE SET revision = revision + 1;
  END;`
  ```

  Create the provenance trigger before the revision trigger so upstream `AddZaloMemory` remains unchanged while new agent rows obtain the latest inbound source from the same thread:

  ```sql
  CREATE TRIGGER IF NOT EXISTS app_zalo_memory_insert_provenance
  AFTER INSERT ON zalo_memory
  WHEN NEW.source = 'agent' AND NEW.source_message_id = 0
  BEGIN
    UPDATE zalo_memory
    SET source_message_id = COALESCE((
      SELECT id FROM zalo_messages
      WHERE thread_id = NEW.thread_id AND direction = 'in'
      ORDER BY id DESC LIMIT 1
    ), 0)
    WHERE id = NEW.id;
  END;
  ```

  Add `MemoryRevision` and `LessonsRevision` to `ZaloCLISession`; include both in every SELECT/SCAN and INSERT/replace path. Change completion to:

  ```go
  func (s *Store) CompleteZaloCLITurn(
      threadID string,
      expectedGeneration, contextTokens, messageCursor, memoryRevision, lessonsRevision int64,
  ) (ZaloCLISession, error)
  ```

  Its CAS update writes both revisions in the same statement that advances `turn_count` and the message cursor. Replacement resets both cursors to zero for a new generation.

- [ ] **Step 4: Run the focused tests and confirm they pass**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'TestMigrateApp|TestCompleteZaloCLITurn|TestZaloCLISessionLifecycle'
  ```

  Expected: PASS, including repeated migration and future schema metadata preservation.

- [ ] **Step 5: Refactor while green**

  Keep schema inspection, identifier validation, and column creation in small helpers. Re-run the same command after `gofmt`.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_schema.go appmode/overlay/internal/store/app_schema_test.go appmode/overlay/internal/store/app_zalo_cli_session.go appmode/overlay/internal/store/app_zalo_cli_session_test.go tests/run-overlay-go-tests.ps1
  git commit -m "feat: migrate memory state to schema v3"
  ```

### Task 2: Store APIs manage thread memory and global lessons transactionally

**Files:**
- Create: `appmode/overlay/internal/store/app_memory.go`
- Create: `appmode/overlay/internal/store/app_memory_test.go`

**Public behavior to verify:** Callers can search, add, edit, delete, and pin owned records; every successful mutation bumps only its scope revision, pin limits are enforced, provenance is returned, and prompt selections are pinned-first but rendered oldest-to-newest.

- [ ] **Step 1: Write failing behavior tests through exported Store methods**

  Cover an empty database, threads without memory, server-side search, operator provenance, agent source-message inference, 240/200-rune validation, ownership mismatch, update/delete rollback, pin 13/9 conflicts, selection order, and scope isolation.

  ```go
  func TestAppThreadMemoryCRUDIsOwnedAndBumpsOnlyItsRevision(t *testing.T) {
      s := newStore(t)
      seedThreadAndInbound(t, s, "thread-a", "Lan", "nhận hàng buổi sáng")
      seedThreadAndInbound(t, s, "thread-b", "Minh", "gọi sau 17 giờ")

      created, err := s.CreateAppThreadMemory("thread-a", AppThreadMemoryInput{
          Text: "Chị Lan thích nhận hàng buổi sáng", Pinned: true,
      })
      if err != nil { t.Fatal(err) }
      if created.Source != "operator" || created.SourceMessageID != 0 {
          t.Fatalf("operator provenance = %#v", created)
      }
      assertAppMemoryRevision(t, s, "thread-a", 1)
      assertAppMemoryRevision(t, s, "thread-b", 0)

      if _, err := s.UpdateAppThreadMemory("thread-b", created.ID, AppThreadMemoryInput{
          Text: "không được chạm", Pinned: false,
      }); !errors.Is(err, ErrAppMemoryNotFound) {
          t.Fatalf("cross-thread update = %v; want ErrAppMemoryNotFound", err)
      }
      assertAppMemoryRevision(t, s, "thread-a", 1)
  }

  func TestAppPromptSelectionPrioritizesPinnedThenRendersChronologically(t *testing.T) {
      s := newStore(t)
      seedMemories(t, s, "thread-a", 14, map[int]bool{0: true, 1: true})
      selected, revision, err := s.AppPromptMemory("thread-a", 12)
      if err != nil { t.Fatal(err) }
      if revision != 14 || len(selected) != 12 { t.Fatalf("revision/length = %d/%d", revision, len(selected)) }
      if !selected[0].Pinned || !selected[1].Pinned { t.Fatalf("pinned rows lost: %#v", selected[:2]) }
      assertOldestFirst(t, selected)
  }
  ```

- [ ] **Step 2: Run the tests and confirm the missing API failure**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store -Run 'TestApp(ThreadMemory|Lesson|Prompt|MemoryOverview)'
  ```

  Expected: FAIL because `AppThreadMemoryInput`, `CreateAppThreadMemory`, `AppPromptMemory`, lesson CRUD, and overview queries are undefined.

- [ ] **Step 3: Implement the store contract and transaction helper**

  Define stable errors and JSON-ready models without changing upstream IPC types:

  ```go
  var (
      ErrAppMemoryNotFound = errors.New("memory not found")
      ErrAppMemoryPinLimit = errors.New("memory pin limit reached")
      ErrAppMemoryInvalid  = errors.New("invalid memory input")
  )

  type AppMemoryRevision struct {
      Memory int64 `json:"memory"`
      Lessons int64 `json:"lessons"`
  }

  type AppThreadMemoryInput struct {
      Text string `json:"text"`
      Pinned bool `json:"pinned"`
  }

  type AppThreadMemory struct {
      ID int64 `json:"id"`
      ThreadID string `json:"thread_id"`
      ThreadName string `json:"thread_name"`
      Text string `json:"text"`
      Pinned bool `json:"pinned"`
      Source string `json:"source"`
      SourceMessageID int64 `json:"source_message_id"`
      SourcePreview string `json:"source_preview"`
      CreatedAt time.Time `json:"created_at"`
      UpdatedAt time.Time `json:"updated_at"`
  }

  type AppLessonInput struct {
      ThreadID string `json:"thread_id"`
      BotText string `json:"bot_text"`
      Better string `json:"better"`
      Note string `json:"note"`
      Pinned bool `json:"pinned"`
  }
  ```

  Implement `AppMemoryOverview(q string, pinnedOnly bool)`, `AppThreadMemories(threadID string)`, `CreateAppThreadMemory`, `UpdateAppThreadMemory`, `DeleteAppThreadMemory`, `AppLessons`, `CreateAppLesson`, `UpdateAppLesson`, `DeleteAppLesson`, `AppMemoryRevisions`, `AppPromptMemory`, and `AppPromptLessons`. All mutations use a transaction and this revision statement:

  ```sql
  INSERT INTO app_memory_revisions(scope, scope_id, revision)
  VALUES (?, ?, 1)
  ON CONFLICT(scope, scope_id) DO UPDATE SET revision = revision + 1
  ```

  For selection, fetch pinned rows plus newest unpinned rows within the remaining capacity, deduplicate by ID, then `slices.SortFunc` by `CreatedAt` and ID ascending. The overview query starts from `zalo_threads`, left joins aggregated memory counts/previews, searches both thread name and memory text with escaped `LIKE`, places threads with memory first, and caps the response at 200.

  Preserve upstream agent writes: the insert trigger changes newly inserted `source='agent'` rows to the latest inbound `zalo_messages.id` of the same thread only when `source_message_id=0`; legacy rows present before v3 are migrated to `source='legacy'`.

- [ ] **Step 4: Run all store tests and confirm they pass**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package store
  ```

  Expected: PASS with no leaked rows after rejected ownership or pin-limit mutations.

- [ ] **Step 5: Refactor while green**

  Share only validation, timestamp scanning, and revision increment helpers. Keep memory and lesson SQL explicit so their different scopes cannot be accidentally mixed. Run `gofmt` and the full store command again.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/store/app_memory.go appmode/overlay/internal/store/app_memory_test.go
  git commit -m "feat: add transactional memory store"
  ```

### Task 3: Authenticated Memory API exposes safe CRUD contracts

**Files:**
- Create: `appmode/overlay/internal/daemon/app_memory.go`
- Create: `appmode/overlay/internal/daemon/app_memory_test.go`
- Modify: `appmode/overlay/internal/daemon/app_routes.go`
- Modify: `appmode/overlay/internal/daemon/app_foundation_test.go`

**Public behavior to verify:** An authenticated Portal client can use every documented Memory endpoint and receives `400`, `404`, or `409` without SQL, paths, prompt text, or record contents leaking into errors.

- [ ] **Step 1: Write failing route and handler tests**

  Add all nine patterns to the route registration contract. Exercise handlers through `ServeHTTP` so method routing, auth wrapping, JSON decoding, path parameters, and response status are observed together.

  ```go
  func TestMemoryRoutesAreRegisteredAndCookieReachable(t *testing.T) {
      a := newAppMemoryTestAPI(t)
      mux := http.NewServeMux()
      a.registerAppRoutes(mux)
      for _, pattern := range []string{
          "GET /memory", "GET /memory/threads/{tid}", "POST /memory/threads/{tid}",
          "PUT /memory/threads/{tid}/{id}", "DELETE /memory/threads/{tid}/{id}",
          "GET /memory/lessons", "POST /memory/lessons",
          "PUT /memory/lessons/{id}", "DELETE /memory/lessons/{id}",
      } {
          method, pathPattern, _ := strings.Cut(pattern, " ")
          requestPath := strings.NewReplacer("{tid}", "thread-a", "{id}", "1").Replace(pathPattern)
          _, got := mux.Handler(httptest.NewRequest(method, requestPath, nil))
          if got != pattern { t.Fatalf("route %q matched %q", pattern, got) }
          if !cookieAllowedPaths[pattern] { t.Fatalf("cookie not allowed for %q", pattern) }
      }
  }

  func TestMemoryThreadMutationStatusMappingDoesNotLeakInternals(t *testing.T) {
      a := newAppMemoryTestAPI(t)
      response := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread-a", `{"text":""}`)
      if response.Code != http.StatusBadRequest { t.Fatalf("status = %d", response.Code) }
      if strings.Contains(strings.ToLower(response.Body.String()), "sqlite") {
          t.Fatalf("internal detail leaked: %s", response.Body.String())
      }
  }
  ```

- [ ] **Step 2: Run daemon tests and confirm the route/handler failures**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestMemory|TestAppRoutes'
  ```

  Expected: FAIL because the Memory route patterns and handlers are absent.

- [ ] **Step 3: Implement handlers and safe error mapping**

  Register routes through the existing `a.auth` wrapper and add every pattern to `appPortalRoutePatterns`. Parse numeric IDs with `strconv.ParseInt`, cap request bodies with `http.MaxBytesReader`, reject trailing JSON tokens, and centralize status mapping:

  ```go
  func (a *api) writeAppMemoryError(w http.ResponseWriter, err error) {
      switch {
      case errors.Is(err, store.ErrAppMemoryInvalid):
          a.writeErr(w, http.StatusBadRequest, "dữ liệu memory không hợp lệ")
      case errors.Is(err, store.ErrAppMemoryNotFound):
          a.writeErr(w, http.StatusNotFound, "không tìm thấy memory")
      case errors.Is(err, store.ErrAppMemoryPinLimit):
          a.writeErr(w, http.StatusConflict, "đã đạt giới hạn nội dung được ghim")
      default:
          a.logger.Error("memory request failed", "err", err)
          a.writeErr(w, http.StatusInternalServerError, "không xử lý được memory")
      }
  }
  ```

  Return `201` for creates, `200` for reads/updates, and `204` for deletes. `GET /memory` parses `q` and `pinned`; the detail and lesson reads include their current revision in the payload so the UI can explain when changes will synchronize.

- [ ] **Step 4: Run daemon tests and confirm they pass**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'TestMemory|TestAppRoutes'
  ```

  Expected: PASS for auth reachability, CRUD, input limits, ownership, pin conflicts, and sanitized failures.

- [ ] **Step 5: Refactor while green**

  Keep JSON decode and ID parse helpers private to `app_memory.go`; avoid coupling them to Knowledge handlers. Re-run the focused daemon tests after `gofmt`.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_memory.go appmode/overlay/internal/daemon/app_memory_test.go appmode/overlay/internal/daemon/app_routes.go appmode/overlay/internal/daemon/app_foundation_test.go
  git commit -m "feat: expose Portal memory API"
  ```

### Task 4: Resumed sessions receive memory only when its revision changes

**Files:**
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_prompt_test.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_prompt.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_hook_test.go`
- Modify: `appmode/overlay/internal/daemon/app_zalo_session_hook.go`

**Public behavior to verify:** A new or rotated session receives the full snapshot once; an unchanged resumed session receives no repeated memory; a changed or emptied scope receives one authoritative replacement snapshot and persists only the pre-call revisions after success.

- [ ] **Step 1: Write failing session behavior tests**

  Test both prompt rendering and orchestration. Include thread isolation, a global lesson refresh visible to two threads, deletion producing an explicit empty replacement, model failure leaving cursors unchanged, and a mutation during the model call remaining pending for the next turn.

  ```go
  func TestDeltaPromptAddsOnlyChangedAuthoritativeMemorySnapshots(t *testing.T) {
      got := buildAppZaloDeltaPromptResult(appZaloSessionPromptInput{
          Config: zaloConfig{}, Question: "Còn nhớ em nhận lúc nào không?",
          MemoryRefresh: &appZaloMemoryRefresh{
              Memory: []ipc.ZaloMemory{{Text: "nhận hàng buổi sáng"}},
              MemoryRevision: 4,
              LessonsRevision: 2,
          },
      }).Prompt
      if !strings.Contains(got, "thay thế toàn bộ snapshot memory trước đó") {
          t.Fatalf("missing replacement contract: %s", got)
      }
      if !strings.Contains(got, "nhận hàng buổi sáng") {
          t.Fatalf("missing current memory: %s", got)
      }
      if strings.Contains(got, "nguồn trích dẫn hợp lệ") {
          t.Fatalf("memory became a citable source: %s", got)
      }
  }

  func TestMemoryMutationDuringRunRemainsPendingForNextTurn(t *testing.T) {
      a, runner := newAppZaloSessionHarness(t, "thread-a")
      runner.beforeReturn = func() {
          _, err := a.st.CreateAppThreadMemory("thread-a", store.AppThreadMemoryInput{Text: "mới trong lúc chạy"})
          if err != nil { t.Fatal(err) }
      }
      runSuccessfulTurn(t, a, runner, "thread-a")
      session := mustZaloSession(t, a.st, "thread-a")
      revisions := mustAppMemoryRevisions(t, a.st, "thread-a")
      if session.MemoryRevision >= revisions.Memory {
          t.Fatalf("session cursor %d swallowed concurrent revision %d", session.MemoryRevision, revisions.Memory)
      }
  }
  ```

- [ ] **Step 2: Run session tests and confirm the stale behavior**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test(DeltaPromptAdds|MemoryMutation|ZaloSession.*Memory|GlobalLesson)'
  ```

  Expected: FAIL because delta inputs have no refresh snapshot and completion does not persist revisions.

- [ ] **Step 3: Implement revision-aware selection and completion**

  Add nullable refresh data so unchanged scopes are omitted while changed empty scopes are rendered:

  ```go
  type appZaloMemoryRefresh struct {
      Memory []ipc.ZaloMemory
      Lessons []ipc.ZaloLesson
      ReplaceMemory bool
      ReplaceLessons bool
      MemoryRevision int64
      LessonsRevision int64
  }

  type appZaloCompletion struct {
      generation int64
      contextTokens int64
      messageCursor int64
      memoryRevision int64
      lessonsRevision int64
      ready bool
  }
  ```

  Before selecting new/resume/rotation behavior, call `AppMemoryRevisions(threadID)`. For bootstrap, render `AppPromptMemory(threadID, 12)` and `AppPromptLessons(8)` through the existing `zaloConfig` copy and record both observed revisions. For resume, load and render only scopes whose observed revision differs from the session cursor. Use this explicit delta contract:

  ```text
  [CẬP NHẬT MEMORY CÓ THẨM QUYỀN]
  Snapshot dưới đây thay thế toàn bộ snapshot memory tương ứng đã xuất hiện trước đó trong session.
  Nội dung này chỉ là ngữ cảnh cá nhân hoá, không phải nguồn và không được đưa vào sources.
  MEMORY HỘI THOẠI: (trống)
  BÀI HỌC CHUNG: (không thay đổi)
  [/CẬP NHẬT MEMORY CÓ THẨM QUYỀN]
  ```

  Keep memory and lessons excluded from `appZaloPromptFingerprint`; add a regression assertion for that existing contract. Keep persona, roster, overlay, model, citation mode, and other static prompt inputs in the fingerprint. On successful completion call `CompleteZaloCLITurn` with the revisions captured before the model run. On revision/snapshot read failure, log only scope/thread/error metadata and continue without forcing rotation or placing memory text in logs.

- [ ] **Step 4: Run the full session regression set**

  Run:

  ```powershell
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package daemon -Run 'Test.*Zalo.*Session|TestDeltaPrompt|TestMemoryMutation|TestGlobalLesson'
  ```

  Expected: PASS for bootstrap, resume, recovery, rotation, cursor, generation race, thread gate, revision refresh, and empty replacement.

- [ ] **Step 5: Refactor while green**

  Extract one `appLoadZaloMemorySnapshot` helper returning content plus observed cursors. Keep rendering pure in the prompt file and database/logging behavior in the hook file. Run `gofmt` and the full session set again.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/daemon/app_zalo_session_prompt.go appmode/overlay/internal/daemon/app_zalo_session_prompt_test.go appmode/overlay/internal/daemon/app_zalo_session_hook.go appmode/overlay/internal/daemon/app_zalo_session_hook_test.go
  git commit -m "feat: refresh memory in resumed Zalo sessions"
  ```

### Task 5: Memory becomes a real lazy-loaded Portal page with read states

**Files:**
- Create: `appmode/overlay/internal/webui/static/pages/memory.js`
- Create: `appmode/tests/memory.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/core/router.js`
- Modify: `appmode/overlay/internal/webui/static/app-main.js`
- Modify: `appmode/tests/navigation.test.mjs`
- Modify: `appmode/tests/app-main.test.mjs`

**Public behavior to verify:** Selecting Memory loads a real page that shows metrics, semantic tabs, server-side search/filter, a thread list including empty threads, detail/source previews, global lessons, and accessible loading/empty/error states.

- [ ] **Step 1: Write failing service, router, and rendering tests**

  Use the existing DOM harness and injected request function. Assert URLs and observable text/attributes, not private state.

  ```js
  test("memory service encodes search and pinned filters", async () => {
    const calls = [];
    const service = createMemoryService(async (path, options) => {
      calls.push({ path, options });
      return { metrics: {}, threads: [] };
    });
    await service.overview({ query: "chị Lan & Minh", pinned: true });
    assert.equal(calls[0].path, "/memory?q=ch%E1%BB%8B+Lan+%26+Minh&pinned=true");
  });

  test("memory page renders metrics, tabs, empty threads and source previews", async () => {
    const { document, main } = createDOMHarness();
    const page = createMemoryPage({ request: memoryFixtureRequest() });
    const mounted = page.mount(main);
    await flushTasks();
    assert.match(main.textContent, /Ghi chú đang dùng/);
    assert.equal(findAll(main, node => node.getAttribute?.("role") === "tab").length, 2);
    assert.match(main.textContent, /Chưa có ghi chú cho hội thoại này/);
    mounted.dispose();
  });
  ```

  Update the navigation contract so the Memory item has no placeholder metadata field, and assert `app-main.js` lazy-loads `pages/memory.js` without importing it during unrelated routes.

- [ ] **Step 2: Run Node tests and confirm they fail for missing page/route behavior**

  Run:

  ```powershell
  npm --prefix .\appmode test -- --test-name-pattern="memory|navigation|lazy"
  ```

  Expected: FAIL because `pages/memory.js` is missing and Memory still carries placeholder metadata.

- [ ] **Step 3: Implement the read-only page slice**

  Export a service with exact endpoints and a page factory:

  ```js
  export function createMemoryService(request = requestJSON) {
    const query = ({ query = "", pinned = false } = {}) => {
      const params = new URLSearchParams();
      if (query.trim()) params.set("q", query.trim());
      if (pinned) params.set("pinned", "true");
      const value = params.toString();
      return value ? `?${value}` : "";
    };
    return Object.freeze({
      overview: (filters) => request(`/memory${query(filters)}`),
      thread: (threadID) => request(`/memory/threads/${encodeURIComponent(threadID)}`),
      lessons: (filters) => request(`/memory/lessons${query(filters)}`),
    });
  }

  export function createMemoryPage({ request = requestJSON } = {}) {
    return Object.freeze({
      mount(container) {
        const controller = new AbortController();
        const service = createMemoryService((path, options = {}) =>
          request(path, { ...options, signal: controller.signal }));
        const root = element("div", { className: "memory-page" });
        container.append(root);
        const state = { tab: "threads", query: "", pinned: false, selectedThreadID: "", disposed: false };
        void loadOverview({ root, service, state });
        return { dispose() { state.disposed = true; controller.abort(); } };
      },
    });
  }
  ```

  Render the approved header, three metrics, `role=tablist`, `role=tab`, `aria-selected`, search, pinned checkbox, split list/detail, timestamps, source labels, revision badge, global-scope explanation, and `aria-live=polite`. Debounce server search by 250 ms and cancel stale render revisions so late responses never overwrite a newer selection.

- [ ] **Step 4: Run the focused UI tests and confirm they pass**

  Run:

  ```powershell
  npm --prefix .\appmode test -- --test-name-pattern="memory|navigation|lazy"
  ```

  Expected: PASS for service URLs, route metadata, lazy loading, read states, tab semantics, stale responses, and disposal.

- [ ] **Step 5: Refactor while green**

  Keep render helpers pure where possible; use `element` and `pageHeader` from `core/ui.js`; do not introduce a UI dependency. Re-run the focused tests.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/memory.js appmode/tests/memory.test.mjs appmode/overlay/internal/webui/static/core/router.js appmode/overlay/internal/webui/static/app-main.js appmode/tests/navigation.test.mjs appmode/tests/app-main.test.mjs
  git commit -m "feat: add Memory Center read experience"
  ```

### Task 6: Portal users can add, edit, pin, and delete memory accessibly

**Files:**
- Modify: `appmode/overlay/internal/webui/static/pages/memory.js`
- Modify: `appmode/tests/memory.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/portal.css`
- Modify: `appmode/tests/legacy-pages.test.mjs`

**Public behavior to verify:** Users can perform every memory/lesson mutation from the approved legacy UI, receive immediate status feedback, confirm deletes, and use the dialog fully by keyboard on desktop and mobile without regressions.

- [ ] **Step 1: Write failing mutation and accessibility tests**

  Assert methods/bodies, disabled Add state without a selected thread, refresh after success, preserved rendered data after read failure, confirmation before DELETE, pin conflict feedback, Escape, Tab focus cycling, focus restore, abort on dispose, and no unscoped Memory CSS selectors.

  ```js
  test("thread edit sends trimmed content then refreshes detail", async () => {
    const calls = [];
    const fixture = memoryMutationHarness({ calls });
    await fixture.openThread("thread-a");
    fixture.click("Sửa");
    fixture.fill("textarea", "  nhận hàng sau 9 giờ  ");
    fixture.submitDialog();
    await flushTasks();
    assert.deepEqual(calls.find(call => call.options?.method === "PUT"), {
      path: "/memory/threads/thread-a/7",
      options: { method: "PUT", body: { text: "nhận hàng sau 9 giờ", pinned: false }, signal: fixture.signal },
    });
    assert.match(fixture.liveRegion.textContent, /Đã lưu/);
  });

  test("delete requires confirmation and restores opener focus", async () => {
    const fixture = memoryMutationHarness();
    const opener = fixture.click("Xoá");
    assert.equal(fixture.deleteCalls, 0);
    fixture.click("Xác nhận xoá");
    await flushTasks();
    assert.equal(fixture.deleteCalls, 1);
    assert.equal(fixture.document.activeElement, opener);
  });
  ```

- [ ] **Step 2: Run mutation tests and confirm actions are absent**

  Run:

  ```powershell
  npm --prefix .\appmode test -- --test-name-pattern="memory.*(add|edit|delete|pin|dialog|mobile)|legacy.*sheet"
  ```

  Expected: FAIL because read-only controls do not yet expose mutations or dialog behavior.

- [ ] **Step 3: Implement mutations, dialogs, and scoped CSS**

  Extend the service with exact methods:

  ```js
  createThread: (threadID, body) => request(`/memory/threads/${encodeURIComponent(threadID)}`, { method: "POST", body }),
  updateThread: (threadID, id, body) => request(`/memory/threads/${encodeURIComponent(threadID)}/${id}`, { method: "PUT", body }),
  deleteThread: (threadID, id) => request(`/memory/threads/${encodeURIComponent(threadID)}/${id}`, { method: "DELETE" }),
  createLesson: (body) => request("/memory/lessons", { method: "POST", body }),
  updateLesson: (id, body) => request(`/memory/lessons/${id}`, { method: "PUT", body }),
  deleteLesson: (id) => request(`/memory/lessons/${id}`, { method: "DELETE" }),
  ```

  Build add/edit and confirm dialogs using the Agents sheet pattern: `role=dialog`, `aria-modal=true`, labelled/described IDs, Escape close, backdrop close, focus trap, and opener focus restore. Disable controls while a mutation is pending; on success close and refresh overview plus the active pane; on failure preserve the current DOM and announce the sanitized API message.

  Add only scoped rules such as:

  ```css
  .memory-page .memory-metrics { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:12px; }
  .memory-page .memory-split { display:grid; grid-template-columns:minmax(240px,0.8fr) minmax(0,1.7fr); min-width:0; }
  .memory-page .memory-list,
  .memory-page .memory-detail { min-width:0; overflow-wrap:anywhere; }
  [data-memory-overlay] .sheet { width:min(620px,calc(100vw - 24px)); }
  @media (max-width: 760px) {
    .memory-page .memory-metrics,
    .memory-page .memory-split { grid-template-columns:1fr; }
  }
  ```

- [ ] **Step 4: Run all Portal tests and confirm they pass**

  Run:

  ```powershell
  npm --prefix .\appmode test
  ```

  Expected: PASS for Memory and all existing Agents, Knowledge, Models, navigation, router, shell, and API behavior.

- [ ] **Step 5: Refactor while green**

  Share a single dialog lifecycle and mutation runner within `memory.js`; keep thread and lesson field rendering separate. Verify the CSS file contains no bare selectors introduced for Memory controls and rerun all Portal tests.

- [ ] **Step 6: Commit**

  ```powershell
  git add appmode/overlay/internal/webui/static/pages/memory.js appmode/tests/memory.test.mjs appmode/overlay/internal/webui/static/portal.css appmode/tests/legacy-pages.test.mjs
  git commit -m "feat: manage memory from the Portal"
  ```

### Task 7: “Open conversation” selects the requested Zalo thread safely

**Files:**
- Modify: `scripts/BuildApp.psm1`
- Modify: `tests/build-app.Tests.ps1`
- Modify: `appmode/overlay/internal/webui/static/pages/memory.js`
- Modify: `appmode/tests/memory.test.mjs`

**Public behavior to verify:** Clicking “Mở hội thoại” navigates to `/zalo?thread=<encoded-id>`; the staged Zalo page selects that thread when present and otherwise opens Conversations normally without an error banner.

- [ ] **Step 1: Write failing link and seam tests**

  Assert the Memory link encoding in Node, then extend the package fixture with a minimal Zalo script and assert `Apply-AppSeams` injects one query-selection block exactly once and refuses a missing or duplicated anchor.

  ```js
  test("open conversation link encodes the selected thread", async () => {
    const fixture = await renderThreadDetail({ id: "group/a?b", name: "Nhóm A" });
    const link = fixture.findLink("Mở hội thoại");
    assert.equal(link.getAttribute("href"), "/zalo?thread=group%2Fa%3Fb");
  });
  ```

  ```powershell
  $patched = Get-Content -Raw (Join-Path $stage 'internal\webui\static\zalo.js')
  Assert-ContainsOnce $patched "const requestedThreadID = new URLSearchParams(window.location.search).get('thread')"
  ```

- [ ] **Step 2: Run the focused tests and confirm the seam is missing**

  Run:

  ```powershell
  npm --prefix .\appmode test -- --test-name-pattern="open conversation"
  pwsh -NoProfile -File .\tests\build-app.Tests.ps1
  ```

  Expected: the Node test fails because the link is absent or unencoded, and the PowerShell test fails because the query-selection seam is not applied.

- [ ] **Step 3: Implement the narrow deep-link seam**

  Render the Memory action as a normal anchor. In `Apply-AppSeams`, add these declarations after upstream's `let curThread = null;`:

  ```js
  const requestedThreadID = new URLSearchParams(window.location.search).get('thread');
  let requestedThreadHandled = false;
  ```

  Then insert this block immediately after upstream's `threads = ths || [];` assignment. It uses the actual `curThread`, `refreshMemCount`, `feedSig`, `outboxSig`, `listSig`, and `feed` contracts already present in `zalo.js`, and avoids calling `open()` recursively from the current refresh:

  ```js
  if (!requestedThreadHandled) {
    requestedThreadHandled = true;
    if (requestedThreadID && threads.some((thread) => thread.id === requestedThreadID)) {
      curThread = requestedThreadID;
      void refreshMemCount();
      feedSig = outboxSig = listSig = '';
      el('feed').textContent = '';
      atBottom = true;
    }
  }
  ```

  The seam must use the same exact-anchor count checks as the existing route, migration, answer, and runner seams, and must not show an error when the requested thread is absent.

- [ ] **Step 4: Run both test groups and confirm they pass**

  Run:

  ```powershell
  npm --prefix .\appmode test -- --test-name-pattern="open conversation"
  pwsh -NoProfile -File .\tests\build-app.Tests.ps1
  ```

  Expected: PASS, including repeat-application and missing-anchor safety cases.

- [ ] **Step 5: Refactor while green**

  Keep the new seam adjacent to the existing web UI seams and name its anchor/error messages specifically. Re-run both commands.

- [ ] **Step 6: Commit**

  ```powershell
  git add scripts/BuildApp.psm1 tests/build-app.Tests.ps1 appmode/overlay/internal/webui/static/pages/memory.js appmode/tests/memory.test.mjs
  git commit -m "feat: deep link Memory to Zalo threads"
  ```

### Task 8: Build, migrate a copied database, visually verify, and update the test package

**Files:**
- Modify only if a failing gate identifies a scoped defect in the files above.
- Build artifact: `D:\TuvanZalo\_artifacts\memory-center`
- Runtime test package after all gates pass: `D:\TuvanZalo`

**Public behavior to verify:** A newly built package starts against a copy of the existing database, migrates it to v3 without data loss, serves the approved legacy Memory UI, and retains all existing session, Knowledge, Zalo, and package behavior.

- [ ] **Step 1: Add a failing package assertion before building**

  Extend `Assert-AppPackage` coverage so a package missing the new Memory module or schema marker fails:

  ```powershell
  Assert-PathExists (Join-Path $Out 'app\agentdc.exe')
  Assert-BinaryContains (Join-Path $Out 'app\agentdc.exe') '/memory/threads/'
  Assert-BinaryContains (Join-Path $Out 'app\agentdc.exe') 'app_memory_revisions'
  ```

  Run `pwsh -NoProfile -File .\tests\build-app.Tests.ps1` and confirm the new fixture fails until its staged overlay includes Memory Center.

- [ ] **Step 2: Run the complete verification suite from a clean source state**

  Run:

  ```powershell
  git diff --check
  npm --prefix .\appmode test
  pwsh -NoProfile -File .\tests\build-app.Tests.ps1
  pwsh -NoProfile -File .\tests\run-overlay-go-tests.ps1 -Package all
  git status --short
  ```

  Expected: all commands exit 0; `git status --short` lists only the intended tracked changes from this task, if any.

- [ ] **Step 3: Build into a separate artifact directory**

  Do not target `D:\TuvanZalo`, because it contains the working repository and live `brain`/`data`. Run:

  ```powershell
  pwsh -NoProfile -File .\build-app.ps1 `
    -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' `
    -Out 'D:\TuvanZalo\_artifacts\memory-center' `
    -PersonaSource 'D:\TuvanZalo\brain\reference\persona'
  ```

  Expected: all seven build phases pass, upstream git status remains clean, and the artifact contains no Zalo credentials.

- [ ] **Step 4: Smoke-test migration using a copy of the live database**

  Resolve the exact current database path read-only, copy it under the artifact's `data` directory, record row counts for `zalo_threads`, `zalo_messages`, `zalo_memory`, and `zalo_lessons`, then start the artifact on a non-conflicting local port. Open Portal Memory and verify schema version 3, both session revision columns, unchanged row counts, empty states for the current 0/0 memory data, and successful create/edit/pin/delete of a disposable test memory in the copied database only.

  ```powershell
  sqlite3 'D:\TuvanZalo\_artifacts\memory-center\data\agentdc.db' `
    "select value from app_meta where key='schema_version'; pragma table_info(app_zalo_cli_sessions); select count(*) from zalo_messages;"
  ```

  Expected: schema version `3`; `memory_revision` and `lessons_revision` are present; pre-smoke row counts match the copied source; no file under the live `D:\TuvanZalo\brain` or `D:\TuvanZalo\data` is modified.

- [ ] **Step 5: Perform browser and responsive verification**

  Check the built Portal at desktop width near 1366 px and mobile widths 736 px and 360 px. Verify legacy typography/palette/rail, no horizontal overflow, both tabs, search, pinned filter, source preview, keyboard dialog cycle, destructive confirmation, success/error live announcements, and `/zalo?thread=` fallback. Capture screenshots in the external visualization directory, not in git.

- [ ] **Step 6: Install only verified runtime artifacts into the test package**

  Stop the currently running test process. Resolve the timestamp to an explicit directory name, back up `D:\TuvanZalo\app\agentdc.exe` there, then replace only that executable with `D:\TuvanZalo\_artifacts\memory-center\app\agentdc.exe`; Portal static assets are embedded in that binary. Preserve `brain`, `data`, credentials, launchers, transport, and `_build` exactly. Restart the Portal and repeat the Memory page plus one resumed-session smoke test.

- [ ] **Step 7: Commit any final scoped test-harness change and push**

  ```powershell
  git add tests/build-app.Tests.ps1
  git commit -m "test: gate Memory Center package"  # run only when the file changed
  git status --short --branch
  git push origin feature/portal-m1-foundation
  ```

  Expected: working tree clean, local branch matches the remote branch, and the live test package opens for user testing with existing data intact.
