package store

import (
	"database/sql"
	"fmt"
)

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
INSERT INTO app_meta(key, value) VALUES ('schema_version', '2')
ON CONFLICT(key) DO UPDATE SET value = excluded.value;
`

func migrateApp(db *sql.DB) error {
	if _, err := db.Exec(appFoundationSchema); err != nil {
		return fmt.Errorf("migrate Portal foundation: %w", err)
	}
	return nil
}
