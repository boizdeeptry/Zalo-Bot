package store

import (
	"database/sql"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func createLegacyMemoryTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
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
  created_at TEXT NOT NULL
)`,
		`CREATE TABLE zalo_lessons (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL DEFAULT '',
  bot_text TEXT NOT NULL DEFAULT '',
  better TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("create legacy table: %v", err)
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

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	if err := migrateApp(db); err != nil {
		t.Fatalf("second migrateApp: %v", err)
	}

	var version string
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "3" {
		t.Fatalf("schema_version = %q; want 3", version)
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
		{name: "missing metadata", want: "3"},
		{name: "older metadata", seed: "2", insertSeed: true, want: "3"},
		{name: "current metadata", seed: "3", insertSeed: true, want: "3"},
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
	createLegacyMemoryTables(t, db)

	if _, err := db.Exec(`INSERT INTO zalo_messages(thread_id, direction, body, created_at)
VALUES ('thread-a', 'in', 'nhận buổi sáng', '2026-08-09T01:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO zalo_memory(thread_id, uid, text, created_at)
VALUES ('thread-a', '', 'ghi chú cũ', '2026-08-09T01:01:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO zalo_lessons(thread_id, bot_text, better, note, created_at)
VALUES ('thread-a', 'câu cũ', 'câu mới', 'ngắn hơn', '2026-08-09T01:02:00Z')`); err != nil {
		t.Fatal(err)
	}

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	if err := migrateApp(db); err != nil {
		t.Fatalf("second migrateApp: %v", err)
	}

	if got, want := appTableColumns(t, db, "zalo_memory"), []string{
		"id", "thread_id", "uid", "text", "created_at", "pinned", "source",
		"source_message_id", "updated_at",
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
	assertRevision("thread", "thread-a", 2)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM zalo_memory`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("memory row count = %d; want 2", count)
	}
	var version string
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != strconv.FormatInt(appSchemaVersion, 10) {
		t.Fatalf("schema_version = %q; want %d", version, appSchemaVersion)
	}
}
