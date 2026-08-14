package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const appSchemaVersion int64 = 8

const appFoundationSchema = `
CREATE TABLE IF NOT EXISTS app_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS app_zalo_cli_sessions (
  thread_id          TEXT PRIMARY KEY,
  claude_session_id  TEXT NOT NULL,
  claude_account_id  TEXT NOT NULL DEFAULT '',
  claude_config_dir  TEXT NOT NULL DEFAULT '',
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

// appLLMSchema adds Provider, model, CLI account, named combo, fallback route,
// and attempt telemetry capabilities. It intentionally does not advance
// schema_version: migrateApp owns that write so all capability families become
// visible at the current version only after every migration step succeeds.
//
// No table stores prompts, customer messages, or model responses. Telemetry is
// restricted to routing and operational metadata.
const appLLMSchema = `
CREATE TABLE IF NOT EXISTS llm_providers (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL,
  kind              TEXT NOT NULL,
  enabled           INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  system_provider   INTEGER NOT NULL DEFAULT 0 CHECK (system_provider IN (0, 1)),
  credential_cipher BLOB NOT NULL DEFAULT x'',
  last_check_status TEXT NOT NULL DEFAULT '',
  last_error        TEXT NOT NULL DEFAULT '',
  last_checked_at   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS llm_models (
  provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  model_id    TEXT NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  source      TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'discovered')),
  available   INTEGER NOT NULL DEFAULT 1 CHECK (available IN (0, 1)),
  PRIMARY KEY (provider_id, model_id)
);

CREATE TABLE IF NOT EXISTS llm_route_entries (
  position    INTEGER PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id),
  model_id    TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))
);

CREATE TABLE IF NOT EXISTS llm_attempts (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id      TEXT NOT NULL,
  model_id         TEXT NOT NULL,
  started_at       TEXT NOT NULL,
  duration_ms      INTEGER NOT NULL DEFAULT 0,
  outcome          TEXT NOT NULL CHECK (outcome IN ('ok', 'error')),
  error_kind       TEXT NOT NULL DEFAULT '',
  fell_back        INTEGER NOT NULL DEFAULT 0 CHECK (fell_back IN (0, 1)),
  next_provider_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_llm_attempts_started ON llm_attempts(started_at);

CREATE TABLE IF NOT EXISTS llm_accounts (
  id          TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  label       TEXT NOT NULL,
  email       TEXT NOT NULL DEFAULT '',
  config_dir  TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  added_at    TEXT NOT NULL DEFAULT ''
);

INSERT OR IGNORE INTO llm_providers(id, name, kind, enabled, system_provider)
VALUES ('claude-code', 'Claude Code', 'claude-code', 1, 1);
INSERT OR IGNORE INTO app_meta(key, value) VALUES ('llm_route_revision', '1');

CREATE TABLE IF NOT EXISTS llm_combos (
  id       TEXT PRIMARY KEY,
  name     TEXT NOT NULL,
  type     TEXT NOT NULL DEFAULT 'fallback' CHECK (type IN ('fallback','round_robin')),
  active   INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS llm_combo_members (
  combo_id    TEXT NOT NULL REFERENCES llm_combos(id) ON DELETE CASCADE,
  position    INTEGER NOT NULL,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id),
  model_id    TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  PRIMARY KEY (combo_id, position)
);

UPDATE llm_providers
SET kind = 'claude-code'
WHERE id = 'claude-code' AND kind = 'claude_code';
`

const appOnboardingV7Schema = `
CREATE TABLE IF NOT EXISTS app_onboarding_state (
  id                  INTEGER PRIMARY KEY CHECK (id = 1),
  completed_version   INTEGER NOT NULL DEFAULT 0,
  phase               TEXT NOT NULL CHECK (phase IN ('provider', 'connect', 'setup', 'persona', 'test', 'completed')),
  provider_kind       TEXT NOT NULL DEFAULT '',
  provider_id         TEXT NOT NULL DEFAULT '',
  account_id          TEXT NOT NULL DEFAULT '',
  model_id            TEXT NOT NULL DEFAULT '',
  staged_combo_id     TEXT NOT NULL DEFAULT '',
  persona_fingerprint TEXT NOT NULL DEFAULT '',
  test_nonce_hash     TEXT NOT NULL DEFAULT '',
  test_expires_at     TEXT NOT NULL DEFAULT '',
  restart_in_progress INTEGER NOT NULL DEFAULT 0 CHECK (restart_in_progress IN (0, 1)),
  revision            INTEGER NOT NULL DEFAULT 1,
  updated_at          TEXT NOT NULL
);

INSERT OR IGNORE INTO app_onboarding_state(
  id, completed_version, phase, revision, updated_at
) VALUES (
  1, 0, 'provider', 1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
);
`

const appOnboardingV8Schema = `
CREATE TABLE app_onboarding_provider_stages(
  kind        TEXT PRIMARY KEY,
  status      TEXT NOT NULL CHECK(status IN ('pending','ready')),
  position    INTEGER NOT NULL CHECK(position>=0),
  provider_id TEXT NOT NULL DEFAULT '',
  account_id  TEXT NOT NULL DEFAULT '',
  model_id    TEXT NOT NULL DEFAULT '',
  updated_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX app_onboarding_provider_position
  ON app_onboarding_provider_stages(position);
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

var appV4Columns = []appColumnMigration{
	{table: "zalo_memory", column: "memory_key", definition: "TEXT NOT NULL DEFAULT ''"},
	{table: "zalo_memory", column: "category", definition: "TEXT NOT NULL DEFAULT 'profile'"},
	{table: "zalo_memory", column: "confidence", definition: "REAL NOT NULL DEFAULT 1"},
	{table: "zalo_memory", column: "status", definition: "TEXT NOT NULL DEFAULT 'active'"},
	{table: "zalo_memory", column: "proposal_action", definition: "TEXT NOT NULL DEFAULT ''"},
	{table: "zalo_memory", column: "supersedes_id", definition: "INTEGER NOT NULL DEFAULT 0"},
	{table: "zalo_memory", column: "last_confirmed_at", definition: "TEXT NOT NULL DEFAULT ''"},
	{table: "zalo_memory", column: "expires_at", definition: "TEXT"},
	{table: "app_zalo_cli_sessions", column: "memory_subject_uid", definition: "TEXT NOT NULL DEFAULT ''"},
	{table: "app_zalo_cli_sessions", column: "memory_subject_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
	{table: "app_zalo_cli_sessions", column: "memory_common_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
}

var appV6Columns = []appColumnMigration{
	{table: "app_zalo_cli_sessions", column: "claude_account_id", definition: "TEXT NOT NULL DEFAULT ''"},
	{table: "app_zalo_cli_sessions", column: "claude_config_dir", definition: "TEXT NOT NULL DEFAULT ''"},
}

const appMemoryV4Schema = `
CREATE TABLE IF NOT EXISTS app_memory_subject_revisions (
  thread_id TEXT NOT NULL,
  uid       TEXT NOT NULL,
  revision  INTEGER NOT NULL,
  PRIMARY KEY(thread_id, uid)
);
`

const appMemoryV4Indexes = `
CREATE INDEX IF NOT EXISTS idx_zalo_memory_subject_status
  ON zalo_memory(thread_id, uid, status, pinned, id);
CREATE INDEX IF NOT EXISTS idx_zalo_memory_subject_key_status
  ON zalo_memory(thread_id, uid, memory_key, status);
CREATE INDEX IF NOT EXISTS idx_zalo_memory_status_expires_at
  ON zalo_memory(status, expires_at);
`

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

	var rawVersion string
	versionExists := false
	metaExists, err := appTableExists(tx, "app_meta")
	if err != nil {
		return err
	}
	if metaExists {
		if err := tx.QueryRow(
			`SELECT value FROM app_meta WHERE key = 'schema_version'`,
		).Scan(&rawVersion); errors.Is(err, sql.ErrNoRows) {
			versionExists = false
		} else if err != nil {
			return fmt.Errorf("read Portal schema_version: %w", err)
		} else {
			versionExists = true
		}
	}
	version := int64(0)
	if versionExists {
		var parseErr error
		version, parseErr = strconv.ParseInt(rawVersion, 10, 64)
		if parseErr != nil {
			return fmt.Errorf("parse Portal schema_version %q: %w", rawVersion, parseErr)
		}
		if version < 0 {
			return fmt.Errorf("invalid Portal schema_version %q: version must be non-negative", rawVersion)
		}
	}
	if versionExists && version > appSchemaVersion {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit future Portal schema no-op: %w", err)
		}
		return nil
	}
	if versionExists && version == appSchemaVersion {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit current Portal schema no-op: %w", err)
		}
		return nil
	}

	if _, err := tx.Exec(appFoundationSchema); err != nil {
		return fmt.Errorf("migrate Portal foundation: %w", err)
	}
	if err := appMigrateMemoryV3(tx); err != nil {
		return fmt.Errorf("migrate Portal Memory V3 capability: %w", err)
	}
	if err := appMigrateMemoryV4(tx); err != nil {
		return fmt.Errorf("migrate Portal Memory V4 capability: %w", err)
	}
	if err := appMigrateZaloSessionV6(tx); err != nil {
		return fmt.Errorf("migrate Portal Zalo session V6 capability: %w", err)
	}
	if _, err := tx.Exec(appLLMSchema); err != nil {
		return fmt.Errorf("migrate Portal LLM providers: %w", err)
	}
	if _, err := tx.Exec(appOnboardingV7Schema); err != nil {
		return fmt.Errorf("migrate Portal onboarding V7 capability: %w", err)
	}
	if err := appMigrateOnboardingV8(tx, versionExists, version); err != nil {
		return fmt.Errorf("migrate Portal onboarding V8 capability: %w", err)
	}
	if !versionExists {
		if _, err := tx.Exec(
			`INSERT INTO app_meta(key, value) VALUES ('schema_version', ?)`,
			strconv.FormatInt(appSchemaVersion, 10),
		); err != nil {
			return fmt.Errorf("initialize Portal schema_version: %w", err)
		}
	} else if version < appSchemaVersion {
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

func appMigrateOnboardingV8(tx *sql.Tx, versionExists bool, version int64) error {
	if _, err := tx.Exec(appOnboardingV8Schema); err != nil {
		return fmt.Errorf("create onboarding Provider stages: %w", err)
	}
	if !versionExists || version != 7 {
		return nil
	}
	if _, err := tx.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, provider_id, account_id, model_id, updated_at
)
SELECT
  provider_kind,
  CASE WHEN phase IN ('persona', 'test') THEN 'ready' ELSE 'pending' END,
  0,
  CASE WHEN phase IN ('persona', 'test') THEN provider_id ELSE '' END,
  CASE WHEN phase IN ('persona', 'test') THEN account_id ELSE '' END,
  CASE WHEN phase IN ('persona', 'test') THEN model_id ELSE '' END,
  updated_at
FROM app_onboarding_state
WHERE id = 1 AND phase IN ('connect', 'setup', 'persona', 'test')`); err != nil {
		return fmt.Errorf("backfill onboarding Provider stage: %w", err)
	}
	if _, err := tx.Exec(`UPDATE app_onboarding_state SET
provider_kind = CASE WHEN phase IN ('persona', 'test') THEN '' ELSE provider_kind END,
provider_id = CASE WHEN phase IN ('persona', 'test') THEN '' ELSE provider_id END,
account_id = CASE WHEN phase IN ('persona', 'test') THEN '' ELSE account_id END,
model_id = CASE WHEN phase IN ('persona', 'test') THEN '' ELSE model_id END,
revision = revision + 1
WHERE id = 1 AND phase IN ('connect', 'setup', 'persona', 'test')`); err != nil {
		return fmt.Errorf("transform onboarding V8 singleton: %w", err)
	}
	return nil
}

func appMigrateZaloSessionV6(tx *sql.Tx) error {
	for _, migration := range appV6Columns {
		if _, err := appEnsureColumn(tx, migration); err != nil {
			return err
		}
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

func appMigrateMemoryV3(tx *sql.Tx) error {
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

func appMigrateMemoryV4(tx *sql.Tx) error {
	addedMemoryColumns := make(map[string]bool)
	for _, migration := range appV4Columns {
		added, err := appEnsureColumn(tx, migration)
		if err != nil {
			return err
		}
		if added && migration.table == "zalo_memory" {
			addedMemoryColumns[migration.column] = true
		}
	}
	if _, err := tx.Exec(appMemoryV4Schema); err != nil {
		return fmt.Errorf("create Memory V2 subject revisions: %w", err)
	}

	memoryExists, err := appTableExists(tx, "zalo_memory")
	if err != nil {
		return err
	}
	if !memoryExists {
		return nil
	}
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS app_zalo_memory_insert_revision`); err != nil {
		return fmt.Errorf("drop legacy Zalo memory revision trigger: %w", err)
	}
	if addedMemoryColumns["memory_key"] {
		if _, err := tx.Exec(`UPDATE zalo_memory
SET memory_key = 'legacy.' || id
WHERE memory_key = ''`); err != nil {
			return fmt.Errorf("backfill Memory V2 keys: %w", err)
		}
	}
	if addedMemoryColumns["last_confirmed_at"] {
		if _, err := tx.Exec(`UPDATE zalo_memory
SET last_confirmed_at = CASE
      WHEN last_confirmed_at <> '' THEN last_confirmed_at
      WHEN updated_at <> '' THEN updated_at
      ELSE created_at
    END`); err != nil {
			return fmt.Errorf("backfill Memory V2 confirmation time: %w", err)
		}
	}
	if addedMemoryColumns["memory_key"] {
		threadsExist, err := appTableExists(tx, "zalo_threads")
		if err != nil {
			return err
		}
		if threadsExist {
			if _, err := tx.Exec(`UPDATE zalo_memory
SET uid = thread_id
WHERE uid = '' AND EXISTS (
  SELECT 1 FROM zalo_threads t
  WHERE t.id = zalo_memory.thread_id AND t.thread_type = 'user'
)`); err != nil {
				return fmt.Errorf("backfill direct-thread Memory subjects: %w", err)
			}
		}
	}
	if addedMemoryColumns["expires_at"] {
		expiresAt := ts(time.Now().Add(180 * 24 * time.Hour))
		if _, err := tx.Exec(`UPDATE zalo_memory
SET expires_at = CASE WHEN pinned = 1 THEN NULL ELSE ? END`, expiresAt); err != nil {
			return fmt.Errorf("backfill Memory V2 expiry: %w", err)
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO app_memory_subject_revisions(thread_id, uid, revision)
SELECT thread_id, uid, 1 FROM zalo_memory WHERE status = 'active' GROUP BY thread_id, uid`); err != nil {
		return fmt.Errorf("seed subject Memory revisions: %w", err)
	}
	if _, err := tx.Exec(appMemoryV4Indexes); err != nil {
		return fmt.Errorf("create Memory V2 indexes: %w", err)
	}
	return nil
}
