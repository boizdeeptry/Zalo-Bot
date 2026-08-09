package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const appSchemaVersion int64 = 2

const appFoundationSchema = `
CREATE TABLE IF NOT EXISTS app_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS app_zalo_cli_sessions (
  thread_id          TEXT PRIMARY KEY,
  claude_session_id  TEXT NOT NULL,
  generation         INTEGER NOT NULL,
  model              TEXT NOT NULL,
  prompt_fingerprint TEXT NOT NULL,
  context_tokens     INTEGER NOT NULL DEFAULT 0,
  turn_count         INTEGER NOT NULL DEFAULT 0,
  message_cursor     INTEGER NOT NULL DEFAULT 0,
  rotate_before_next INTEGER NOT NULL DEFAULT 0,
  last_error         TEXT NOT NULL DEFAULT '',
  created_at         TEXT NOT NULL,
  updated_at         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_app_zalo_cli_sessions_updated_at
  ON app_zalo_cli_sessions(updated_at);
`

func migrateApp(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin Portal foundation migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(appFoundationSchema); err != nil {
		return fmt.Errorf("migrate Portal foundation: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO app_meta(key, value) VALUES ('schema_version', ?)`,
		strconv.FormatInt(appSchemaVersion, 10),
	); err != nil {
		return fmt.Errorf("initialize Portal schema_version: %w", err)
	}

	var rawVersion string
	if err := tx.QueryRow(
		`SELECT value FROM app_meta WHERE key = 'schema_version'`,
	).Scan(&rawVersion); err != nil {
		return fmt.Errorf("read Portal schema_version: %w", err)
	}
	version, parseErr := strconv.ParseInt(rawVersion, 10, 64)
	if parseErr != nil {
		return fmt.Errorf("parse Portal schema_version %q: %w", rawVersion, parseErr)
	}
	if version < 0 {
		return fmt.Errorf("invalid Portal schema_version %q: version must be non-negative", rawVersion)
	}
	if version < appSchemaVersion {
		if _, err := tx.Exec(
			`UPDATE app_meta SET value = ? WHERE key = 'schema_version'`,
			strconv.FormatInt(appSchemaVersion, 10),
		); err != nil {
			return fmt.Errorf("advance Portal schema_version: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Portal foundation migration: %w", err)
	}
	return nil
}
