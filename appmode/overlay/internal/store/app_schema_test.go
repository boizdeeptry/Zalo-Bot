package store

import (
	"database/sql"
	"slices"
	"strings"
	"testing"
)

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
	if version != "2" {
		t.Fatalf("schema_version = %q; want 2", version)
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

	rows, err := db.Query(`PRAGMA table_info(app_zalo_cli_sessions)`)
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
	want := []string{
		"thread_id", "claude_session_id", "generation", "model", "prompt_fingerprint",
		"context_tokens", "turn_count", "message_cursor", "rotate_before_next",
		"last_error", "created_at", "updated_at",
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
