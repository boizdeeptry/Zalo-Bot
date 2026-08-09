package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

const appSchemaVersion int64 = 3

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
  updated_at         TEXT NOT NULL,
  memory_revision    INTEGER NOT NULL DEFAULT 0,
  lessons_revision   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_app_zalo_cli_sessions_updated_at
  ON app_zalo_cli_sessions(updated_at);
CREATE TABLE IF NOT EXISTS app_memory_revisions (
  scope    TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  PRIMARY KEY(scope, scope_id)
);
`

type appColumnMigration struct {
	table      string
	column     string
	definition string
}

var appV3Columns = []appColumnMigration{
	{table: "zalo_memory", column: "pinned", definition: "INTEGER NOT NULL DEFAULT 0"},
	{table: "zalo_memory", column: "source", definition: "TEXT NOT NULL DEFAULT 'agent'"},
	{table: "zalo_memory", column: "source_message_id", definition: "INTEGER NOT NULL DEFAULT 0"},
	{table: "zalo_memory", column: "updated_at", definition: "TEXT NOT NULL DEFAULT ''"},
	{table: "zalo_lessons", column: "pinned", definition: "INTEGER NOT NULL DEFAULT 0"},
	{table: "zalo_lessons", column: "updated_at", definition: "TEXT NOT NULL DEFAULT ''"},
	{table: "app_zalo_cli_sessions", column: "memory_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
	{table: "app_zalo_cli_sessions", column: "lessons_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
}

const appZaloMemoryProvenanceTriggerSchema = `
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
`

const appZaloMemoryRevisionTriggerSchema = `
CREATE TRIGGER IF NOT EXISTS app_zalo_memory_insert_revision
AFTER INSERT ON zalo_memory
BEGIN
  INSERT INTO app_memory_revisions(scope, scope_id, revision)
  VALUES ('thread', NEW.thread_id, 1)
  ON CONFLICT(scope, scope_id) DO UPDATE SET revision = revision + 1;
END;
`

const appZaloLessonTriggerSchema = `
CREATE TRIGGER IF NOT EXISTS app_zalo_lessons_insert_revision
AFTER INSERT ON zalo_lessons
BEGIN
  INSERT INTO app_memory_revisions(scope, scope_id, revision)
  VALUES ('lessons', '', 1)
  ON CONFLICT(scope, scope_id) DO UPDATE SET revision = revision + 1;
END;
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
	legacyMemory := false
	for _, migration := range appV3Columns {
		added, err := appEnsureColumn(tx, migration)
		if err != nil {
			return err
		}
		if migration.table == "zalo_memory" && migration.column == "source" {
			legacyMemory = added
		}
	}
	if err := appMigrateMemoryRevisions(tx, legacyMemory); err != nil {
		return err
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

func appEnsureColumn(tx *sql.Tx, migration appColumnMigration) (bool, error) {
	exists, err := appTableExists(tx, migration.table)
	if err != nil || !exists {
		return false, err
	}
	rows, err := tx.Query(`PRAGMA table_info(` + migration.table + `)`)
	if err != nil {
		return false, fmt.Errorf("inspect Portal table %s: %w", migration.table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan Portal table %s: %w", migration.table, err)
		}
		if name == migration.column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("inspect Portal table %s: %w", migration.table, err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close Portal table inspection %s: %w", migration.table, err)
	}
	if found {
		return false, nil
	}
	if _, err := tx.Exec(`ALTER TABLE ` + migration.table + ` ADD COLUMN ` +
		migration.column + ` ` + migration.definition); err != nil {
		return false, fmt.Errorf("add Portal column %s.%s: %w", migration.table, migration.column, err)
	}
	return true, nil
}

func appTableExists(tx *sql.Tx, table string) (bool, error) {
	var count int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect Portal table %s: %w", table, err)
	}
	return count == 1, nil
}

func appMigrateMemoryRevisions(tx *sql.Tx, legacyMemory bool) error {
	memoryExists, err := appTableExists(tx, "zalo_memory")
	if err != nil {
		return err
	}
	messagesExist, err := appTableExists(tx, "zalo_messages")
	if err != nil {
		return err
	}
	if memoryExists {
		if legacyMemory {
			if _, err := tx.Exec(`UPDATE zalo_memory
SET source = 'legacy', source_message_id = 0, updated_at = created_at`); err != nil {
				return fmt.Errorf("mark legacy Zalo memory provenance: %w", err)
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO app_memory_revisions(scope, scope_id, revision)
SELECT 'thread', thread_id, 1 FROM zalo_memory GROUP BY thread_id`); err != nil {
			return fmt.Errorf("seed Zalo memory revisions: %w", err)
		}
		if _, err := tx.Exec(appZaloMemoryRevisionTriggerSchema); err != nil {
			return fmt.Errorf("create Zalo memory revision trigger: %w", err)
		}
		if messagesExist {
			if _, err := tx.Exec(appZaloMemoryProvenanceTriggerSchema); err != nil {
				return fmt.Errorf("create Zalo memory provenance trigger: %w", err)
			}
		}
	}

	lessonsExist, err := appTableExists(tx, "zalo_lessons")
	if err != nil {
		return err
	}
	if !lessonsExist {
		return nil
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO app_memory_revisions(scope, scope_id, revision)
SELECT 'lessons', '', 1 WHERE EXISTS (SELECT 1 FROM zalo_lessons LIMIT 1)`); err != nil {
		return fmt.Errorf("seed Zalo lesson revision: %w", err)
	}
	if _, err := tx.Exec(appZaloLessonTriggerSchema); err != nil {
		return fmt.Errorf("create Zalo lesson trigger: %w", err)
	}
	return nil
}
