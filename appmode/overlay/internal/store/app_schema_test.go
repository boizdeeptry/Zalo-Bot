package store

import (
	"database/sql"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func createV3MemoryTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		`CREATE TABLE zalo_threads (
  id TEXT PRIMARY KEY,
  thread_type TEXT NOT NULL DEFAULT 'user'
)`,
		`CREATE TABLE zalo_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL,
  direction TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`,
		`CREATE TABLE zalo_memory (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL,
  uid TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL,
  created_at TEXT NOT NULL,
  pinned INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'agent',
  source_message_id INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT ''
)`,
		`CREATE TABLE zalo_lessons (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL DEFAULT '',
  bot_text TEXT NOT NULL DEFAULT '',
  better TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  pinned INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT ''
)`,
		`CREATE TABLE app_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`,
		`CREATE TABLE app_zalo_cli_sessions (
  thread_id TEXT PRIMARY KEY,
  claude_session_id TEXT NOT NULL,
  generation INTEGER NOT NULL,
  model TEXT NOT NULL,
  prompt_fingerprint TEXT NOT NULL,
  context_tokens INTEGER NOT NULL DEFAULT 0,
  turn_count INTEGER NOT NULL DEFAULT 0,
  message_cursor INTEGER NOT NULL DEFAULT 0,
  rotate_before_next INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  memory_revision INTEGER NOT NULL DEFAULT 0,
  lessons_revision INTEGER NOT NULL DEFAULT 0
)`,
		`CREATE TABLE app_memory_revisions (
  scope TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  PRIMARY KEY(scope, scope_id)
)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create legacy table: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO app_meta(key, value) VALUES ('schema_version', '3')`); err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []string{
		appZaloMemoryProvenanceTriggerSchema,
		appZaloMemoryRevisionTriggerSchema,
		appZaloLessonTriggerSchema,
	} {
		if _, err := db.Exec(trigger); err != nil {
			t.Fatalf("create V3 trigger: %v", err)
		}
	}
}

func appTableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func TestMigrateAppIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createV3MemoryTables(t, db)

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	var firstSQLiteSchemaVersion int64
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&firstSQLiteSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := migrateApp(db); err != nil {
		t.Fatalf("second migrateApp: %v", err)
	}
	var secondSQLiteSchemaVersion int64
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&secondSQLiteSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if secondSQLiteSchemaVersion != firstSQLiteSchemaVersion {
		t.Fatalf("second migration changed SQLite schema_version from %d to %d", firstSQLiteSchemaVersion, secondSQLiteSchemaVersion)
	}

	var version string
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "4" {
		t.Fatalf("schema_version = %q; want 4", version)
	}
}

func TestMigrateAppAdvancesSchemaVersionMonotonically(t *testing.T) {
	tests := []struct {
		name       string
		seed       string
		insertSeed bool
		want       string
		wantErr    bool
	}{
		{name: "missing metadata", want: "4"},
		{name: "older metadata", seed: "2", insertSeed: true, want: "4"},
		{name: "current metadata", seed: "4", insertSeed: true, want: "4"},
		{name: "newer metadata", seed: "7", insertSeed: true, want: "7"},
		{name: "malformed metadata", seed: "future", insertSeed: true, want: "future", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE app_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`); err != nil {
				t.Fatal(err)
			}
			if tc.insertSeed {
				if _, err := db.Exec(`INSERT INTO app_meta(key, value) VALUES ('schema_version', ?)`, tc.seed); err != nil {
					t.Fatal(err)
				}
			}

			err = migrateApp(db)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "schema_version") {
					t.Fatalf("migrateApp() error = %v; want schema_version error", err)
				}
			} else if err != nil {
				t.Fatalf("migrateApp() error = %v", err)
			}

			var got string
			if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'schema_version'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("schema_version = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestMigrateAppCreatesContentFreeZaloCLISessionSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}

	columns := appTableColumns(t, db, "app_zalo_cli_sessions")
	want := []string{
		"thread_id", "claude_session_id", "generation", "model", "prompt_fingerprint",
		"context_tokens", "turn_count", "message_cursor", "rotate_before_next",
		"last_error", "created_at", "updated_at", "memory_revision", "lessons_revision",
		"memory_subject_uid", "memory_subject_revision", "memory_common_revision",
	}
	if !slices.Equal(columns, want) {
		t.Fatalf("session columns = %v; want %v", columns, want)
	}
	for _, column := range columns {
		switch strings.ToLower(column) {
		case "prompt", "response", "message", "body", "content", "tool_output", "tool-output":
			t.Errorf("session table stores customer/agent content in forbidden column %q", column)
		}
	}

	var indexCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_app_zalo_cli_sessions_updated_at'`).Scan(&indexCount); err != nil {
		t.Fatal(err)
	}
	if indexCount != 1 {
		t.Fatalf("updated_at index count = %d; want 1", indexCount)
	}
}

func TestMigrateAppV3PreservesLegacyRowsSeedsRevisionsAndTracksAgentProvenance(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createV3MemoryTables(t, db)

	if _, err := db.Exec(`INSERT INTO zalo_threads(id, thread_type) VALUES
('user-1', 'user'), ('group-1', 'group')`); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`INSERT INTO zalo_messages(thread_id, direction, body, created_at)
VALUES ('thread-a', 'in', 'nhận buổi sáng', '2026-08-09T01:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO zalo_memory(
thread_id, uid, text, created_at, pinned, source, source_message_id, updated_at) VALUES
('thread-a', '', 'ghi chú cũ', '2026-08-09T01:01:00Z', 0, 'legacy', 0, '2026-08-09T01:01:00Z'),
('user-1', '', 'direct legacy', '2026-08-09T01:02:00Z', 0, 'legacy', 0, ''),
('group-1', '', 'group legacy', '2026-08-09T01:03:00Z', 1, 'legacy', 0, '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO zalo_lessons(thread_id, bot_text, better, note, created_at)
VALUES ('thread-a', 'câu cũ', 'câu mới', 'ngắn hơn', '2026-08-09T01:02:00Z')`); err != nil {
		t.Fatal(err)
	}

	migrationStarted := time.Now().UTC()
	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	var firstExpiry string
	if err := db.QueryRow(`SELECT expires_at FROM zalo_memory WHERE text = 'direct legacy'`).Scan(&firstExpiry); err != nil {
		t.Fatal(err)
	}
	if err := migrateApp(db); err != nil {
		t.Fatalf("second migrateApp: %v", err)
	}
	var secondExpiry string
	if err := db.QueryRow(`SELECT expires_at FROM zalo_memory WHERE text = 'direct legacy'`).Scan(&secondExpiry); err != nil {
		t.Fatal(err)
	}
	if secondExpiry != firstExpiry {
		t.Fatalf("second migration changed legacy expiry from %q to %q", firstExpiry, secondExpiry)
	}

	if got, want := appTableColumns(t, db, "zalo_memory"), []string{
		"id", "thread_id", "uid", "text", "created_at", "pinned", "source",
		"source_message_id", "updated_at", "memory_key", "category", "confidence",
		"status", "proposal_action", "supersedes_id", "last_confirmed_at", "expires_at",
	}; !slices.Equal(got, want) {
		t.Fatalf("zalo_memory columns = %v; want %v", got, want)
	}
	if got, want := appTableColumns(t, db, "zalo_lessons"), []string{
		"id", "thread_id", "bot_text", "better", "note", "created_at", "pinned", "updated_at",
	}; !slices.Equal(got, want) {
		t.Fatalf("zalo_lessons columns = %v; want %v", got, want)
	}

	var source string
	var sourceMessageID int64
	if err := db.QueryRow(`SELECT source, source_message_id FROM zalo_memory WHERE text = 'ghi chú cũ'`).Scan(&source, &sourceMessageID); err != nil {
		t.Fatal(err)
	}
	if source != "legacy" || sourceMessageID != 0 {
		t.Fatalf("legacy provenance = %q/%d; want legacy/0", source, sourceMessageID)
	}

	var directUID, groupUID string
	if err := db.QueryRow(`SELECT uid FROM zalo_memory WHERE text = 'direct legacy'`).Scan(&directUID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT uid FROM zalo_memory WHERE text = 'group legacy'`).Scan(&groupUID); err != nil {
		t.Fatal(err)
	}
	if directUID != "user-1" || groupUID != "" {
		t.Fatalf("legacy scopes = direct %q group %q", directUID, groupUID)
	}

	var memoryKey, category, status, lastConfirmedAt string
	var confidence float64
	if err := db.QueryRow(`SELECT memory_key, category, confidence, status, last_confirmed_at
FROM zalo_memory WHERE text = 'direct legacy'`).Scan(
		&memoryKey, &category, &confidence, &status, &lastConfirmedAt,
	); err != nil {
		t.Fatal(err)
	}
	if memoryKey != "legacy.2" || category != "profile" || confidence != 1 ||
		status != "active" || lastConfirmedAt != "2026-08-09T01:02:00Z" {
		t.Fatalf("direct legacy metadata = %q/%q/%v/%q/%q", memoryKey, category, confidence, status, lastConfirmedAt)
	}
	directExpiry, err := parseTS(firstExpiry)
	if err != nil {
		t.Fatalf("parse direct legacy expiry %q: %v", firstExpiry, err)
	}
	if directExpiry.Before(migrationStarted.Add(179*24*time.Hour)) || directExpiry.After(time.Now().UTC().Add(181*24*time.Hour)) {
		t.Fatalf("direct legacy expiry = %v; want migration time + 180 days", directExpiry)
	}
	var groupExpiry sql.NullString
	if err := db.QueryRow(`SELECT expires_at FROM zalo_memory WHERE text = 'group legacy'`).Scan(&groupExpiry); err != nil {
		t.Fatal(err)
	}
	if groupExpiry.Valid {
		t.Fatalf("pinned group legacy expiry = %q; want NULL", groupExpiry.String)
	}

	assertRevision := func(scope, scopeID string, want int64) {
		t.Helper()
		var got int64
		if err := db.QueryRow(`SELECT revision FROM app_memory_revisions WHERE scope = ? AND scope_id = ?`, scope, scopeID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("revision %s/%s = %d; want %d", scope, scopeID, got, want)
		}
	}
	assertRevision("thread", "thread-a", 1)
	assertRevision("lessons", "", 1)
	assertSubjectRevision := func(threadID, uid string, want int64) {
		t.Helper()
		var got int64
		if err := db.QueryRow(`SELECT revision FROM app_memory_subject_revisions
WHERE thread_id = ? AND uid = ?`, threadID, uid).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("subject revision %s/%s = %d; want %d", threadID, uid, got, want)
		}
	}
	assertSubjectRevision("thread-a", "", 1)
	assertSubjectRevision("user-1", "user-1", 1)
	assertSubjectRevision("group-1", "", 1)

	for _, index := range []string{
		"idx_zalo_memory_subject_status",
		"idx_zalo_memory_subject_key_status",
		"idx_zalo_memory_status_expires_at",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("Memory V2 index %s count = %d; want 1", index, count)
		}
	}

	if _, err := db.Exec(`INSERT INTO zalo_memory(thread_id, uid, text, created_at)
VALUES ('thread-a', '', 'ghi chú mới', '2026-08-09T01:03:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT source, source_message_id FROM zalo_memory WHERE text = 'ghi chú mới'`).Scan(&source, &sourceMessageID); err != nil {
		t.Fatal(err)
	}
	if source != "agent" || sourceMessageID != 1 {
		t.Fatalf("agent provenance = %q/%d; want agent/1", source, sourceMessageID)
	}
	var revisionTriggerCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
WHERE type = 'trigger' AND name = 'app_zalo_memory_insert_revision'`).Scan(&revisionTriggerCount); err != nil {
		t.Fatal(err)
	}
	if revisionTriggerCount != 0 {
		t.Fatalf("legacy Memory revision trigger count = %d; want 0", revisionTriggerCount)
	}
	assertRevision("thread", "thread-a", 1)
	assertSubjectRevision("thread-a", "", 1)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM zalo_memory`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("memory row count = %d; want 4", count)
	}
	var version string
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != strconv.FormatInt(appSchemaVersion, 10) {
		t.Fatalf("schema_version = %q; want %d", version, appSchemaVersion)
	}
}

func TestMigrateAppV4IndexesHaveExpectedColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createV3MemoryTables(t, db)
	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		want []string
	}{
		{name: "idx_zalo_memory_subject_status", want: []string{"thread_id", "uid", "status", "pinned", "id"}},
		{name: "idx_zalo_memory_subject_key_status", want: []string{"thread_id", "uid", "memory_key", "status"}},
		{name: "idx_zalo_memory_status_expires_at", want: []string{"status", "expires_at"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := db.Query(`PRAGMA index_info(` + tc.name + `)`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var got []string
			for rows.Next() {
				var seqno, cid int
				var name string
				if err := rows.Scan(&seqno, &cid, &name); err != nil {
					t.Fatal(err)
				}
				got = append(got, name)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("%s columns = %v; want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestMigrateAppV4LeavesFutureSchemaVersionUntouched(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE app_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO app_meta(key, value) VALUES ('schema_version', '7')`); err != nil {
		t.Fatal(err)
	}
	var firstSQLiteSchemaVersion, firstObjectCount int64
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&firstSQLiteSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&firstObjectCount); err != nil {
		t.Fatal(err)
	}
	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'schema_version'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "7" {
		t.Fatalf("future schema_version = %q; want 7", got)
	}
	var secondSQLiteSchemaVersion, secondObjectCount int64
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&secondSQLiteSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&secondObjectCount); err != nil {
		t.Fatal(err)
	}
	if secondSQLiteSchemaVersion != firstSQLiteSchemaVersion || secondObjectCount != firstObjectCount {
		t.Fatalf("future schema changed: SQLite version %d -> %d, object count %d -> %d",
			firstSQLiteSchemaVersion, secondSQLiteSchemaVersion, firstObjectCount, secondObjectCount)
	}
}
