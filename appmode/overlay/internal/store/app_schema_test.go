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

func execAppFixture(t *testing.T, db *sql.DB, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create app migration fixture: %v", err)
		}
	}
}

func appTableExistsForTest(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func appSchemaVersionForTest(t *testing.T, db *sql.DB) string {
	t.Helper()
	var version string
	if err := db.QueryRow(
		`SELECT value FROM app_meta WHERE key = 'schema_version'`,
	).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func openMigratedAppTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func createFeatureV4Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	execAppFixture(t, db,
		`CREATE TABLE zalo_threads (
  id TEXT PRIMARY KEY,
  thread_type TEXT NOT NULL DEFAULT 'user'
);
CREATE TABLE zalo_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL,
  direction TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE zalo_memory (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL,
  uid TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL,
  created_at TEXT NOT NULL,
  pinned INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'agent',
  source_message_id INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT '',
  memory_key TEXT NOT NULL DEFAULT '',
  category TEXT NOT NULL DEFAULT 'profile',
  confidence REAL NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'active',
  proposal_action TEXT NOT NULL DEFAULT '',
  supersedes_id INTEGER NOT NULL DEFAULT 0,
  last_confirmed_at TEXT NOT NULL DEFAULT '',
  expires_at TEXT
);
CREATE TABLE zalo_lessons (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL DEFAULT '',
  bot_text TEXT NOT NULL DEFAULT '',
  better TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  pinned INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE app_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE app_zalo_cli_sessions (
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
  lessons_revision INTEGER NOT NULL DEFAULT 0,
  memory_subject_uid TEXT NOT NULL DEFAULT '',
  memory_subject_revision INTEGER NOT NULL DEFAULT 0,
  memory_common_revision INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_app_zalo_cli_sessions_updated_at
  ON app_zalo_cli_sessions(updated_at);
CREATE TABLE app_memory_revisions (
  scope TEXT NOT NULL,
  scope_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  PRIMARY KEY(scope, scope_id)
);
CREATE TABLE app_memory_subject_revisions (
  thread_id TEXT NOT NULL,
  uid TEXT NOT NULL,
  revision INTEGER NOT NULL,
  PRIMARY KEY(thread_id, uid)
);
CREATE INDEX idx_zalo_memory_subject_status
  ON zalo_memory(thread_id, uid, status, pinned, id);
CREATE INDEX idx_zalo_memory_subject_key_status
  ON zalo_memory(thread_id, uid, memory_key, status);
CREATE INDEX idx_zalo_memory_status_expires_at
  ON zalo_memory(status, expires_at);`,
		`INSERT INTO app_meta(key, value) VALUES ('schema_version', '4');
INSERT INTO zalo_threads(id, thread_type) VALUES ('feature-user', 'user');
INSERT INTO zalo_memory(
  id, thread_id, uid, text, created_at, pinned, source, source_message_id, updated_at,
  memory_key, category, confidence, status, proposal_action, supersedes_id,
  last_confirmed_at, expires_at
) VALUES (
  41, 'feature-user', 'feature-user', 'feature memory', '2026-08-08T01:00:00Z',
  0, 'user', 17, '2026-08-08T02:00:00Z', 'profile.name', 'profile', 0.82,
  'active', '', 0, '2026-08-08T02:00:00Z', '2027-01-01T00:00:00Z'
);
INSERT INTO zalo_lessons(
  id, thread_id, bot_text, better, note, created_at, pinned, updated_at
) VALUES (
  51, 'feature-user', 'old answer', 'better answer', 'feature lesson',
  '2026-08-08T03:00:00Z', 1, '2026-08-08T03:00:00Z'
);
INSERT INTO app_zalo_cli_sessions(
  thread_id, claude_session_id, generation, model, prompt_fingerprint,
  context_tokens, turn_count, message_cursor, rotate_before_next, last_error,
  created_at, updated_at, memory_revision, lessons_revision,
  memory_subject_uid, memory_subject_revision, memory_common_revision
) VALUES (
  'feature-user', 'session-feature', 3, 'sonnet', 'fingerprint-feature',
  1200, 8, 29, 1, 'previous error', '2026-08-08T01:00:00Z',
  '2026-08-08T04:00:00Z', 8, 3, 'feature-user', 9, 4
);
INSERT INTO app_memory_revisions(scope, scope_id, revision) VALUES
  ('thread', 'feature-user', 8), ('lessons', '', 3);
INSERT INTO app_memory_subject_revisions(thread_id, uid, revision)
  VALUES ('feature-user', 'feature-user', 9);`,
		appZaloMemoryProvenanceTriggerSchema,
		appZaloLessonTriggerSchema,
	)
}

// createProductionV5Fixture models an installed V5 database, where the memory/session
// feature family and the provider/account/route feature family already coexist. V6 may
// add only the two Claude binding columns; every pre-existing row must survive verbatim.
func createProductionV5Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	createFeatureV4Fixture(t, db)
	execAppFixture(t, db,
		`CREATE TABLE llm_providers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  system_provider INTEGER NOT NULL DEFAULT 0 CHECK (system_provider IN (0, 1)),
  credential_cipher BLOB NOT NULL DEFAULT x'',
  last_check_status TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  last_checked_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE llm_models (
  provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  model_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'discovered')),
  available INTEGER NOT NULL DEFAULT 1 CHECK (available IN (0, 1)),
  PRIMARY KEY (provider_id, model_id)
);
CREATE TABLE llm_route_entries (
  position INTEGER PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id),
  model_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))
);
CREATE TABLE llm_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id TEXT NOT NULL,
  model_id TEXT NOT NULL,
  started_at TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'error')),
  error_kind TEXT NOT NULL DEFAULT '',
  fell_back INTEGER NOT NULL DEFAULT 0 CHECK (fell_back IN (0, 1)),
  next_provider_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_llm_attempts_started ON llm_attempts(started_at);
CREATE TABLE llm_accounts (
  id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  label TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '',
  config_dir TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  added_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE llm_combos (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'fallback' CHECK (type IN ('fallback','round_robin')),
  active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE llm_combo_members (
  combo_id TEXT NOT NULL REFERENCES llm_combos(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id),
  model_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  PRIMARY KEY (combo_id, position)
);`,
		`UPDATE app_meta SET value = '5' WHERE key = 'schema_version';
INSERT INTO app_meta(key, value) VALUES ('llm_route_revision', '31');
INSERT INTO zalo_messages(id, thread_id, direction, body, created_at)
  VALUES (61, 'feature-user', 'in', 'feature message', '2026-08-08T04:30:00Z');
INSERT INTO llm_providers(
  id, name, kind, enabled, system_provider, credential_cipher,
  last_check_status, last_error, last_checked_at
) VALUES
  ('claude-code', 'Claude Code V5', 'claude-code', 1, 1, x'CAFE', 'ok', '', '2026-08-08T05:00:00Z'),
  ('provider-v5', 'Provider V5', 'openai-compatible', 1, 0, x'BEEF', 'error', 'timeout', '2026-08-08T05:01:00Z');
INSERT INTO llm_models(provider_id, model_id, name, source, available)
  VALUES ('provider-v5', 'model-v5', 'Model V5', 'discovered', 1),
    ('claude-code', 'haiku', 'Claude Haiku', 'manual', 1);
INSERT INTO llm_route_entries(position, provider_id, model_id, enabled)
  VALUES (0, 'provider-v5', 'model-v5', 1), (1, 'claude-code', 'haiku', 1);
INSERT INTO llm_attempts(
  id, provider_id, model_id, started_at, duration_ms, outcome,
  error_kind, fell_back, next_provider_id
) VALUES (
  81, 'provider-v5', 'model-v5', '2026-08-08T05:02:00Z', 317,
  'error', 'timeout', 1, 'claude-code'
);
INSERT INTO llm_accounts(id, provider_id, label, email, config_dir, enabled, added_at)
  VALUES ('account-v5', 'provider-v5', 'V5 account', 'v5@example.test',
    'D:/provider-v5', 1, '2026-08-08T05:03:00Z'),
    ('account-claude-v5', 'claude-code', 'Claude V5', 'claude@example.test',
    'D:/claude-v5', 1, '2026-08-08T05:04:00Z');
INSERT INTO llm_combos(id, name, type, active, revision)
  VALUES ('combo-v5', 'V5 fallback', 'fallback', 1, 7);
INSERT INTO llm_combo_members(combo_id, position, provider_id, model_id, enabled)
  VALUES ('combo-v5', 0, 'provider-v5', 'model-v5', 1),
    ('combo-v5', 1, 'claude-code', 'haiku', 1);`,
	)
}

// createProductionV6Fixture models the final V6 shape without calling migrateApp,
// so a V7 migration test cannot accidentally prepare its fixture at V7.
func createProductionV6Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	createProductionV5Fixture(t, db)
	execAppFixture(t, db,
		`ALTER TABLE app_zalo_cli_sessions
  ADD COLUMN claude_account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE app_zalo_cli_sessions
  ADD COLUMN claude_config_dir TEXT NOT NULL DEFAULT '';
UPDATE app_zalo_cli_sessions
SET claude_account_id = 'account-claude-v5',
    claude_config_dir = 'D:/claude-v5'
WHERE thread_id = 'feature-user';
UPDATE app_meta SET value = '6' WHERE key = 'schema_version';`,
	)
}

func createMainV4Fixture(t *testing.T, db *sql.DB) {
	t.Helper()
	execAppFixture(t, db,
		`CREATE TABLE app_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE llm_providers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  system_provider INTEGER NOT NULL DEFAULT 0 CHECK (system_provider IN (0, 1)),
  credential_cipher BLOB NOT NULL DEFAULT x'',
  last_check_status TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  last_checked_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE llm_models (
  provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  model_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'discovered')),
  available INTEGER NOT NULL DEFAULT 1 CHECK (available IN (0, 1)),
  PRIMARY KEY (provider_id, model_id)
);
CREATE TABLE llm_route_entries (
  position INTEGER PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id),
  model_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))
);
CREATE TABLE llm_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id TEXT NOT NULL,
  model_id TEXT NOT NULL,
  started_at TEXT NOT NULL,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  outcome TEXT NOT NULL CHECK (outcome IN ('ok', 'error')),
  error_kind TEXT NOT NULL DEFAULT '',
  fell_back INTEGER NOT NULL DEFAULT 0 CHECK (fell_back IN (0, 1)),
  next_provider_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_llm_attempts_started ON llm_attempts(started_at);
CREATE TABLE llm_accounts (
  id TEXT PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  label TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '',
  config_dir TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  added_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE llm_combos (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'fallback' CHECK (type IN ('fallback','round_robin')),
  active INTEGER NOT NULL DEFAULT 0 CHECK (active IN (0,1)),
  revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE llm_combo_members (
  combo_id TEXT NOT NULL REFERENCES llm_combos(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id),
  model_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  PRIMARY KEY (combo_id, position)
);
CREATE TABLE zalo_threads (
  id TEXT PRIMARY KEY,
  thread_type TEXT NOT NULL DEFAULT 'user'
);
CREATE TABLE zalo_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL,
  direction TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE TABLE zalo_memory (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL,
  uid TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE zalo_lessons (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  thread_id TEXT NOT NULL DEFAULT '',
  bot_text TEXT NOT NULL DEFAULT '',
  better TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);`,
		`INSERT INTO app_meta(key, value) VALUES
  ('schema_version', '4'), ('llm_route_revision', '17');
INSERT INTO llm_providers(
  id, name, kind, enabled, system_provider, credential_cipher,
  last_check_status, last_error, last_checked_at
) VALUES
  ('claude-code', 'Claude Code customized', 'claude-code', 1, 1, x'CAFE', 'ok', '', '2026-08-07T01:00:00Z'),
  ('provider-main', 'Provider Main', 'openai-compatible', 1, 0, x'BEEF', 'error', 'timeout', '2026-08-07T02:00:00Z');
INSERT INTO llm_models(provider_id, model_id, name, source, available)
  VALUES ('provider-main', 'model-main', 'Model Main', 'discovered', 1);
INSERT INTO llm_route_entries(position, provider_id, model_id, enabled)
  VALUES (0, 'provider-main', 'model-main', 1);
INSERT INTO llm_attempts(
  id, provider_id, model_id, started_at, duration_ms, outcome,
  error_kind, fell_back, next_provider_id
) VALUES (
  71, 'provider-main', 'model-main', '2026-08-07T03:00:00Z', 245,
  'error', 'timeout', 1, 'claude-code'
);
INSERT INTO llm_accounts(id, provider_id, label, email, config_dir, enabled, added_at)
  VALUES ('account-main', 'provider-main', 'Main account', 'main@example.test',
    'D:/provider-main', 1, '2026-08-07T04:00:00Z');
INSERT INTO llm_combos(id, name, type, active, revision)
  VALUES ('combo-main', 'Main fallback', 'fallback', 1, 6);
INSERT INTO llm_combo_members(combo_id, position, provider_id, model_id, enabled)
  VALUES ('combo-main', 0, 'provider-main', 'model-main', 1);
INSERT INTO zalo_threads(id, thread_type) VALUES ('main-user', 'user');
INSERT INTO zalo_messages(id, thread_id, direction, body, created_at)
  VALUES (21, 'main-user', 'in', 'incoming', '2026-08-07T05:00:00Z');
INSERT INTO zalo_memory(id, thread_id, uid, text, created_at)
  VALUES (31, 'main-user', '', 'main memory', '2026-08-07T05:01:00Z');
INSERT INTO zalo_lessons(id, thread_id, bot_text, better, note, created_at)
  VALUES (32, 'main-user', 'old', 'better', 'main lesson', '2026-08-07T05:02:00Z');`,
	)
}

func mainV4DataSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	queries := map[string]string{
		"zalo_memory": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT id || '|' || thread_id || '|' || uid || '|' || text || '|' || created_at AS row_value
  FROM zalo_memory ORDER BY id
)`,
		"llm_providers": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT id || '|' || name || '|' || kind || '|' || enabled || '|' || system_provider || '|' ||
    hex(credential_cipher) || '|' || last_check_status || '|' || last_error || '|' || last_checked_at AS row_value
  FROM llm_providers ORDER BY id
)`,
		"llm_models": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT provider_id || '|' || model_id || '|' || name || '|' || source || '|' || available AS row_value
  FROM llm_models ORDER BY provider_id, model_id
)`,
		"llm_route_entries": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT position || '|' || provider_id || '|' || model_id || '|' || enabled AS row_value
  FROM llm_route_entries ORDER BY position
)`,
		"llm_attempts": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT id || '|' || provider_id || '|' || model_id || '|' || started_at || '|' || duration_ms || '|' ||
    outcome || '|' || error_kind || '|' || fell_back || '|' || next_provider_id AS row_value
  FROM llm_attempts ORDER BY id
)`,
		"llm_accounts": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT id || '|' || provider_id || '|' || label || '|' || email || '|' || config_dir || '|' ||
    enabled || '|' || added_at AS row_value
  FROM llm_accounts ORDER BY id
)`,
		"llm_combos": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT id || '|' || name || '|' || type || '|' || active || '|' || revision AS row_value
  FROM llm_combos ORDER BY id
)`,
		"llm_combo_members": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT combo_id || '|' || position || '|' || provider_id || '|' || model_id || '|' || enabled AS row_value
  FROM llm_combo_members ORDER BY combo_id, position
)`,
	}
	snapshot := make(map[string]string, len(queries))
	for table, query := range queries {
		var value string
		if err := db.QueryRow(query).Scan(&value); err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		snapshot[table] = value
	}
	return snapshot
}

func productionV5DataSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	snapshot := mainV4DataSnapshot(t, db)
	queries := map[string]string{
		"app_meta": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT quote(key) || '|' || quote(value) AS row_value
  FROM app_meta WHERE key <> 'schema_version' ORDER BY key
)`,
		"zalo_threads": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT quote(id) || '|' || quote(thread_type) AS row_value FROM zalo_threads ORDER BY id
)`,
		"zalo_messages": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT quote(id) || '|' || quote(thread_id) || '|' || quote(direction) || '|' ||
    quote(body) || '|' || quote(created_at) AS row_value FROM zalo_messages ORDER BY id
)`,
		"zalo_memory_full": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT quote(id) || '|' || quote(thread_id) || '|' || quote(uid) || '|' || quote(text) || '|' ||
    quote(created_at) || '|' || quote(pinned) || '|' || quote(source) || '|' ||
    quote(source_message_id) || '|' || quote(updated_at) || '|' || quote(memory_key) || '|' ||
    quote(category) || '|' || quote(confidence) || '|' || quote(status) || '|' ||
    quote(proposal_action) || '|' || quote(supersedes_id) || '|' ||
    quote(last_confirmed_at) || '|' || quote(expires_at) AS row_value
  FROM zalo_memory ORDER BY id
)`,
		"zalo_lessons": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT quote(id) || '|' || quote(thread_id) || '|' || quote(bot_text) || '|' ||
    quote(better) || '|' || quote(note) || '|' || quote(created_at) || '|' ||
    quote(pinned) || '|' || quote(updated_at) AS row_value FROM zalo_lessons ORDER BY id
)`,
		"app_zalo_cli_sessions": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT quote(thread_id) || '|' || quote(claude_session_id) || '|' || quote(generation) || '|' ||
    quote(model) || '|' || quote(prompt_fingerprint) || '|' || quote(context_tokens) || '|' ||
    quote(turn_count) || '|' || quote(message_cursor) || '|' || quote(rotate_before_next) || '|' ||
    quote(last_error) || '|' || quote(created_at) || '|' || quote(updated_at) || '|' ||
    quote(memory_revision) || '|' || quote(lessons_revision) || '|' ||
    quote(memory_subject_uid) || '|' || quote(memory_subject_revision) || '|' ||
    quote(memory_common_revision) AS row_value FROM app_zalo_cli_sessions ORDER BY thread_id
)`,
		"app_memory_revisions": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT quote(scope) || '|' || quote(scope_id) || '|' || quote(revision) AS row_value
  FROM app_memory_revisions ORDER BY scope, scope_id
)`,
		"app_memory_subject_revisions": `SELECT COALESCE(group_concat(row_value, char(10)), '') FROM (
  SELECT quote(thread_id) || '|' || quote(uid) || '|' || quote(revision) AS row_value
  FROM app_memory_subject_revisions ORDER BY thread_id, uid
)`,
	}
	for table, query := range queries {
		var value string
		if err := db.QueryRow(query).Scan(&value); err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		snapshot[table] = value
	}
	return snapshot
}

func TestMigrateAppFeatureV4ToV7PreservesMemoryAndAddsLLM(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createFeatureV4Fixture(t, db)

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	if got := appSchemaVersionForTest(t, db); got != "7" {
		t.Fatalf("schema_version = %q; want 7", got)
	}
	for _, table := range []string{
		"llm_providers", "llm_models", "llm_route_entries", "llm_attempts",
		"llm_accounts", "llm_combos", "llm_combo_members",
	} {
		if !appTableExistsForTest(t, db, table) {
			t.Errorf("LLM capability table %s was not created", table)
		}
	}

	var sessionID, model, fingerprint, lastError, subjectUID string
	var generation, contextTokens, turnCount, cursor, rotate int
	var memoryRevision, lessonsRevision, subjectRevision, commonRevision int
	if err := db.QueryRow(`SELECT
  claude_session_id, generation, model, prompt_fingerprint, context_tokens,
  turn_count, message_cursor, rotate_before_next, last_error, memory_revision,
  lessons_revision, memory_subject_uid, memory_subject_revision, memory_common_revision
FROM app_zalo_cli_sessions WHERE thread_id = 'feature-user'`).Scan(
		&sessionID, &generation, &model, &fingerprint, &contextTokens,
		&turnCount, &cursor, &rotate, &lastError, &memoryRevision,
		&lessonsRevision, &subjectUID, &subjectRevision, &commonRevision,
	); err != nil {
		t.Fatal(err)
	}
	if sessionID != "session-feature" || generation != 3 || model != "sonnet" ||
		fingerprint != "fingerprint-feature" || contextTokens != 1200 || turnCount != 8 ||
		cursor != 29 || rotate != 1 || lastError != "previous error" || memoryRevision != 8 ||
		lessonsRevision != 3 || subjectUID != "feature-user" || subjectRevision != 9 || commonRevision != 4 {
		t.Fatalf("feature V4 session changed during migration: %q/%d/%q/%q/%d/%d/%d/%d/%q/%d/%d/%q/%d/%d",
			sessionID, generation, model, fingerprint, contextTokens, turnCount, cursor,
			rotate, lastError, memoryRevision, lessonsRevision, subjectUID, subjectRevision, commonRevision)
	}

	var text, memoryKey, category, status, expiresAt string
	var confidence float64
	if err := db.QueryRow(`SELECT text, memory_key, category, confidence, status, expires_at
FROM zalo_memory WHERE id = 41`).Scan(
		&text, &memoryKey, &category, &confidence, &status, &expiresAt,
	); err != nil {
		t.Fatal(err)
	}
	if text != "feature memory" || memoryKey != "profile.name" || category != "profile" ||
		confidence != 0.82 || status != "active" || expiresAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("feature V4 memory changed during migration: %q/%q/%q/%v/%q/%q",
			text, memoryKey, category, confidence, status, expiresAt)
	}
	var lessonID, lessonPinned int
	var lessonThread, botText, better, note, lessonCreatedAt, lessonUpdatedAt string
	if err := db.QueryRow(`SELECT
  id, thread_id, bot_text, better, note, created_at, pinned, updated_at
FROM zalo_lessons WHERE id = 51`).Scan(
		&lessonID, &lessonThread, &botText, &better, &note,
		&lessonCreatedAt, &lessonPinned, &lessonUpdatedAt,
	); err != nil {
		t.Fatal(err)
	}
	if lessonID != 51 || lessonThread != "feature-user" || botText != "old answer" ||
		better != "better answer" || note != "feature lesson" ||
		lessonCreatedAt != "2026-08-08T03:00:00Z" || lessonPinned != 1 ||
		lessonUpdatedAt != "2026-08-08T03:00:00Z" {
		t.Fatalf("feature V4 lesson changed during migration: %d/%q/%q/%q/%q/%q/%d/%q",
			lessonID, lessonThread, botText, better, note, lessonCreatedAt, lessonPinned, lessonUpdatedAt)
	}
	for _, trigger := range []string{
		"app_zalo_memory_insert_provenance", "app_zalo_lessons_insert_revision",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
WHERE type = 'trigger' AND name = ?`, trigger).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("surviving Feature V4 trigger %s count = %d; want 1", trigger, count)
		}
	}
	var threadRevision, persistedSubjectRevision int
	if err := db.QueryRow(`SELECT revision FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = 'feature-user'`).Scan(&threadRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM app_memory_subject_revisions
WHERE thread_id = 'feature-user' AND uid = 'feature-user'`).Scan(&persistedSubjectRevision); err != nil {
		t.Fatal(err)
	}
	if threadRevision != 8 || persistedSubjectRevision != 9 {
		t.Fatalf("feature V4 revisions changed: thread=%d subject=%d; want 8/9",
			threadRevision, persistedSubjectRevision)
	}
	var kind string
	var systemProvider, combos int
	if err := db.QueryRow(`SELECT kind, system_provider FROM llm_providers
WHERE id = 'claude-code'`).Scan(&kind, &systemProvider); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM llm_combos`).Scan(&combos); err != nil {
		t.Fatal(err)
	}
	if kind != "claude-code" || systemProvider != 1 || combos != 0 {
		t.Fatalf("LLM defaults = kind %q/system %d/combos %d; want claude-code/1/0",
			kind, systemProvider, combos)
	}
}

func TestMigrateAppV7SeedsRequiredOnboarding(t *testing.T) {
	db := openMigratedAppTestDB(t)
	if got := appSchemaVersionForTest(t, db); got != "7" {
		t.Fatalf("schema_version = %q; want 7", got)
	}
	if CurrentOnboardingVersion != 1 {
		t.Fatalf("CurrentOnboardingVersion = %d; want 1", CurrentOnboardingVersion)
	}
	st := &Store{db: db}
	got, err := st.OnboardingState()
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletedVersion != 0 || got.Phase != OnboardingPhaseProvider || got.Revision != 1 {
		t.Fatalf("initial onboarding state = %+v", got)
	}
	if got.UpdatedAt == "" {
		t.Fatal("initial onboarding updated_at is empty")
	}
}

func TestMigrateAppV7PreservesV6Data(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createProductionV6Fixture(t, db)
	wantData := productionV5DataSnapshot(t, db)
	var wantAccountID, wantConfigDir string
	if err := db.QueryRow(`SELECT claude_account_id, claude_config_dir
FROM app_zalo_cli_sessions WHERE thread_id = 'feature-user'`).Scan(
		&wantAccountID, &wantConfigDir,
	); err != nil {
		t.Fatal(err)
	}

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	if got := appSchemaVersionForTest(t, db); got != "7" {
		t.Fatalf("schema_version = %q; want 7", got)
	}
	gotData := productionV5DataSnapshot(t, db)
	for table, want := range wantData {
		if got := gotData[table]; got != want {
			t.Errorf("%s changed during V6-to-V7 migration:\n got %q\nwant %q", table, got, want)
		}
	}
	var gotAccountID, gotConfigDir string
	if err := db.QueryRow(`SELECT claude_account_id, claude_config_dir
FROM app_zalo_cli_sessions WHERE thread_id = 'feature-user'`).Scan(
		&gotAccountID, &gotConfigDir,
	); err != nil {
		t.Fatal(err)
	}
	if gotAccountID != wantAccountID || gotConfigDir != wantConfigDir {
		t.Errorf("Zalo session binding changed from %q/%q to %q/%q",
			wantAccountID, wantConfigDir, gotAccountID, gotConfigDir)
	}
}

func TestMigrateAppV7EnforcesOnboardingSingleton(t *testing.T) {
	db := openMigratedAppTestDB(t)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM app_onboarding_state`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("onboarding row count = %d; want 1", count)
	}
	if _, err := db.Exec(`INSERT INTO app_onboarding_state(id, phase, updated_at)
VALUES (2, 'provider', '2026-08-11T00:00:00Z')`); err == nil {
		t.Fatal("inserting a second onboarding row succeeded; want singleton CHECK failure")
	}
}

func TestMigrateAppV7RejectsUnknownOnboardingPhase(t *testing.T) {
	db := openMigratedAppTestDB(t)
	if _, err := db.Exec(`UPDATE app_onboarding_state SET phase = 'unknown' WHERE id = 1`); err == nil {
		t.Fatal("setting an unknown onboarding phase succeeded; want CHECK failure")
	}
}

func TestMigrateAppMainV4ToV7PreservesRoutingAndAddsMemory(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createMainV4Fixture(t, db)

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	if got := appSchemaVersionForTest(t, db); got != "7" {
		t.Fatalf("schema_version = %q; want 7", got)
	}
	for _, table := range []string{
		"app_zalo_cli_sessions", "app_memory_revisions", "app_memory_subject_revisions",
	} {
		if !appTableExistsForTest(t, db, table) {
			t.Errorf("Memory capability table %s was not created", table)
		}
	}
	for _, column := range []string{
		"memory_key", "category", "confidence", "status", "proposal_action",
		"supersedes_id", "last_confirmed_at", "expires_at",
	} {
		if !slices.Contains(appTableColumns(t, db, "zalo_memory"), column) {
			t.Errorf("zalo_memory is missing V5 column %s", column)
		}
	}
	for _, column := range []string{
		"memory_subject_uid", "memory_subject_revision", "memory_common_revision",
	} {
		if !slices.Contains(appTableColumns(t, db, "app_zalo_cli_sessions"), column) {
			t.Errorf("app_zalo_cli_sessions is missing V5 column %s", column)
		}
	}

	var providerName, providerKind, providerStatus, providerError string
	var providerEnabled, providerSystem int
	var providerCredential []byte
	if err := db.QueryRow(`SELECT
  name, kind, enabled, system_provider, credential_cipher, last_check_status, last_error
FROM llm_providers WHERE id = 'provider-main'`).Scan(
		&providerName, &providerKind, &providerEnabled, &providerSystem,
		&providerCredential, &providerStatus, &providerError,
	); err != nil {
		t.Fatal(err)
	}
	if providerName != "Provider Main" || providerKind != "openai-compatible" ||
		providerEnabled != 1 || providerSystem != 0 || string(providerCredential) != "\xbe\xef" ||
		providerStatus != "error" || providerError != "timeout" {
		t.Fatalf("main V4 provider changed: %q/%q/%d/%d/%x/%q/%q",
			providerName, providerKind, providerEnabled, providerSystem,
			providerCredential, providerStatus, providerError)
	}
	var modelName, source string
	var available int
	if err := db.QueryRow(`SELECT name, source, available FROM llm_models
WHERE provider_id = 'provider-main' AND model_id = 'model-main'`).Scan(
		&modelName, &source, &available,
	); err != nil {
		t.Fatal(err)
	}
	if modelName != "Model Main" || source != "discovered" || available != 1 {
		t.Fatalf("main V4 model changed: %q/%q/%d", modelName, source, available)
	}
	var routeProvider, routeModel string
	var routeEnabled int
	if err := db.QueryRow(`SELECT provider_id, model_id, enabled FROM llm_route_entries
WHERE position = 0`).Scan(&routeProvider, &routeModel, &routeEnabled); err != nil {
		t.Fatal(err)
	}
	if routeProvider != "provider-main" || routeModel != "model-main" || routeEnabled != 1 {
		t.Fatalf("main V4 route changed: %q/%q/%d", routeProvider, routeModel, routeEnabled)
	}
	var attemptProvider, attemptModel, outcome, errorKind, nextProvider string
	var duration, fellBack int
	if err := db.QueryRow(`SELECT
  provider_id, model_id, duration_ms, outcome, error_kind, fell_back, next_provider_id
FROM llm_attempts WHERE id = 71`).Scan(
		&attemptProvider, &attemptModel, &duration, &outcome, &errorKind, &fellBack, &nextProvider,
	); err != nil {
		t.Fatal(err)
	}
	if attemptProvider != "provider-main" || attemptModel != "model-main" || duration != 245 ||
		outcome != "error" || errorKind != "timeout" || fellBack != 1 || nextProvider != "claude-code" {
		t.Fatalf("main V4 attempt changed: %q/%q/%d/%q/%q/%d/%q",
			attemptProvider, attemptModel, duration, outcome, errorKind, fellBack, nextProvider)
	}
	var accountLabel, accountEmail, configDir string
	var accountEnabled int
	if err := db.QueryRow(`SELECT label, email, config_dir, enabled FROM llm_accounts
WHERE id = 'account-main'`).Scan(&accountLabel, &accountEmail, &configDir, &accountEnabled); err != nil {
		t.Fatal(err)
	}
	if accountLabel != "Main account" || accountEmail != "main@example.test" ||
		configDir != "D:/provider-main" || accountEnabled != 1 {
		t.Fatalf("main V4 account changed: %q/%q/%q/%d",
			accountLabel, accountEmail, configDir, accountEnabled)
	}
	var comboName, comboType string
	var comboActive, comboRevision, memberPosition, memberEnabled int
	if err := db.QueryRow(`SELECT name, type, active, revision FROM llm_combos
WHERE id = 'combo-main'`).Scan(&comboName, &comboType, &comboActive, &comboRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT position, enabled FROM llm_combo_members
WHERE combo_id = 'combo-main' AND provider_id = 'provider-main' AND model_id = 'model-main'`).Scan(
		&memberPosition, &memberEnabled,
	); err != nil {
		t.Fatal(err)
	}
	if comboName != "Main fallback" || comboType != "fallback" || comboActive != 1 ||
		comboRevision != 6 || memberPosition != 0 || memberEnabled != 1 {
		t.Fatalf("main V4 combo changed: %q/%q/%d/%d member=%d/%d",
			comboName, comboType, comboActive, comboRevision, memberPosition, memberEnabled)
	}

	var memoryText, memoryUID, memoryKey, sourceName, updatedAt string
	if err := db.QueryRow(`SELECT text, uid, memory_key, source, updated_at FROM zalo_memory
WHERE id = 31`).Scan(&memoryText, &memoryUID, &memoryKey, &sourceName, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if memoryText != "main memory" || memoryUID != "main-user" || memoryKey != "legacy.31" ||
		sourceName != "legacy" || updatedAt != "2026-08-07T05:01:00Z" {
		t.Fatalf("main V4 memory migration = %q/%q/%q/%q/%q",
			memoryText, memoryUID, memoryKey, sourceName, updatedAt)
	}
	var memoryCount, lessonCount, threadRevision, lessonRevision, subjectRevision int
	if err := db.QueryRow(`SELECT COUNT(*) FROM zalo_memory`).Scan(&memoryCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM zalo_lessons`).Scan(&lessonCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = 'main-user'`).Scan(&threadRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM app_memory_revisions
WHERE scope = 'lessons' AND scope_id = ''`).Scan(&lessonRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM app_memory_subject_revisions
WHERE thread_id = 'main-user' AND uid = 'main-user'`).Scan(&subjectRevision); err != nil {
		t.Fatal(err)
	}
	if memoryCount != 1 || lessonCount != 1 || threadRevision != 1 || lessonRevision != 1 || subjectRevision != 1 {
		t.Fatalf("main V4 rows/revisions = memory %d lesson %d thread %d lesson %d subject %d; want 1 each",
			memoryCount, lessonCount, threadRevision, lessonRevision, subjectRevision)
	}
}

func TestMigrateAppMainV4RollsBackOnLateLLMFailure(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createMainV4Fixture(t, db)
	execAppFixture(t, db,
		`UPDATE llm_providers SET kind = 'claude_code' WHERE id = 'claude-code'`,
		`CREATE TRIGGER force_llm_kind_update_failure
BEFORE UPDATE OF kind ON llm_providers
WHEN OLD.id = 'claude-code'
BEGIN
  SELECT RAISE(ABORT, 'forced late LLM migration failure');
END;`,
	)
	wantData := mainV4DataSnapshot(t, db)

	err = migrateApp(db)
	if err == nil || !strings.Contains(err.Error(), "forced late LLM migration failure") {
		t.Fatalf("migrateApp() error = %v; want forced late LLM migration failure", err)
	}
	if got := appSchemaVersionForTest(t, db); got != "4" {
		t.Fatalf("schema_version after rollback = %q; want 4", got)
	}
	for _, table := range []string{
		"app_zalo_cli_sessions", "app_memory_revisions", "app_memory_subject_revisions",
	} {
		if appTableExistsForTest(t, db, table) {
			t.Errorf("rolled-back migration left table %s behind", table)
		}
	}
	if got, want := appTableColumns(t, db, "zalo_memory"), []string{
		"id", "thread_id", "uid", "text", "created_at",
	}; !slices.Equal(got, want) {
		t.Errorf("zalo_memory columns after rollback = %v; want %v", got, want)
	}
	gotData := mainV4DataSnapshot(t, db)
	for table, want := range wantData {
		if got := gotData[table]; got != want {
			t.Errorf("%s changed after rollback:\n got %q\nwant %q", table, got, want)
		}
	}
}

func TestMigrateAppV7IsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createFeatureV4Fixture(t, db)

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE llm_providers
SET name = 'Claude Code edited' WHERE id = 'claude-code'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE app_meta SET value = '23' WHERE key = 'llm_route_revision'`); err != nil {
		t.Fatal(err)
	}
	var firstSQLiteSchemaVersion, firstObjectCount, firstMemoryCount int
	var firstThreadRevision, firstSubjectRevision int
	var firstExpiry string
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&firstSQLiteSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&firstObjectCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM zalo_memory`).Scan(&firstMemoryCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = 'feature-user'`).Scan(&firstThreadRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM app_memory_subject_revisions
WHERE thread_id = 'feature-user' AND uid = 'feature-user'`).Scan(&firstSubjectRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT expires_at FROM zalo_memory WHERE id = 41`).Scan(&firstExpiry); err != nil {
		t.Fatal(err)
	}

	if err := migrateApp(db); err != nil {
		t.Fatalf("second migrateApp: %v", err)
	}
	var secondSQLiteSchemaVersion, secondObjectCount, secondMemoryCount int
	var secondThreadRevision, secondSubjectRevision int
	var secondExpiry, providerName, routeRevision string
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&secondSQLiteSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&secondObjectCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM zalo_memory`).Scan(&secondMemoryCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = 'feature-user'`).Scan(&secondThreadRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM app_memory_subject_revisions
WHERE thread_id = 'feature-user' AND uid = 'feature-user'`).Scan(&secondSubjectRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT expires_at FROM zalo_memory WHERE id = 41`).Scan(&secondExpiry); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT name FROM llm_providers WHERE id = 'claude-code'`).Scan(&providerName); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'llm_route_revision'`).Scan(&routeRevision); err != nil {
		t.Fatal(err)
	}
	if got := appSchemaVersionForTest(t, db); got != "7" {
		t.Fatalf("schema_version = %q; want 7", got)
	}
	if secondSQLiteSchemaVersion != firstSQLiteSchemaVersion || secondObjectCount != firstObjectCount ||
		secondMemoryCount != firstMemoryCount || secondThreadRevision != firstThreadRevision ||
		secondSubjectRevision != firstSubjectRevision || secondExpiry != firstExpiry ||
		providerName != "Claude Code edited" || routeRevision != "23" {
		t.Fatalf("second migration changed V7 state: SQLite %d->%d objects %d->%d memory %d->%d thread revision %d->%d subject revision %d->%d expiry %q->%q provider %q route revision %q",
			firstSQLiteSchemaVersion, secondSQLiteSchemaVersion, firstObjectCount, secondObjectCount,
			firstMemoryCount, secondMemoryCount, firstThreadRevision, secondThreadRevision,
			firstSubjectRevision, secondSubjectRevision, firstExpiry, secondExpiry,
			providerName, routeRevision)
	}
}

func TestMigrateAppV5ToV7AddsClaudeBindingWithoutChangingSession(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createProductionV5Fixture(t, db)
	wantData := productionV5DataSnapshot(t, db)

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	if got := appSchemaVersionForTest(t, db); got != "7" {
		t.Fatalf("schema_version = %q; want 7", got)
	}
	columns := appTableColumns(t, db, "app_zalo_cli_sessions")
	for _, column := range []string{"claude_account_id", "claude_config_dir"} {
		if !slices.Contains(columns, column) {
			t.Errorf("app_zalo_cli_sessions is missing V6 column %s", column)
		}
	}
	gotData := productionV5DataSnapshot(t, db)
	for table, want := range wantData {
		if got := gotData[table]; got != want {
			t.Errorf("%s changed during V5-to-V6 migration:\n got %q\nwant %q", table, got, want)
		}
	}
	assertBlankBinding := func(stage string) {
		t.Helper()
		var accountID, configDir string
		if err := db.QueryRow(`SELECT claude_account_id, claude_config_dir
FROM app_zalo_cli_sessions WHERE thread_id = 'feature-user'`).Scan(
			&accountID, &configDir,
		); err != nil {
			t.Fatal(err)
		}
		if accountID != "" || configDir != "" {
			t.Errorf("%s Claude binding = %q/%q; want empty/empty", stage, accountID, configDir)
		}
	}
	assertBlankBinding("first migration")

	var firstSQLiteSchemaVersion, firstObjectCount int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&firstSQLiteSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&firstObjectCount); err != nil {
		t.Fatal(err)
	}
	if err := migrateApp(db); err != nil {
		t.Fatalf("second migrateApp: %v", err)
	}
	if got := appSchemaVersionForTest(t, db); got != "7" {
		t.Fatalf("schema_version after second migration = %q; want 7", got)
	}
	var secondSQLiteSchemaVersion, secondObjectCount int
	if err := db.QueryRow(`PRAGMA schema_version`).Scan(&secondSQLiteSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master`).Scan(&secondObjectCount); err != nil {
		t.Fatal(err)
	}
	if secondSQLiteSchemaVersion != firstSQLiteSchemaVersion || secondObjectCount != firstObjectCount {
		t.Errorf("second migration changed schema: SQLite %d->%d objects %d->%d",
			firstSQLiteSchemaVersion, secondSQLiteSchemaVersion, firstObjectCount, secondObjectCount)
	}
	secondData := productionV5DataSnapshot(t, db)
	for table, want := range gotData {
		if got := secondData[table]; got != want {
			t.Errorf("%s changed during second V7 migration:\n got %q\nwant %q", table, got, want)
		}
	}
	assertBlankBinding("second migration")
}

func TestMigrateAppAdvancesSchemaVersionMonotonically(t *testing.T) {
	tests := []struct {
		name       string
		seed       string
		insertSeed bool
		want       string
		wantErr    bool
	}{
		{name: "missing metadata", want: "7"},
		{name: "V1 metadata", seed: "1", insertSeed: true, want: "7"},
		{name: "older metadata", seed: "2", insertSeed: true, want: "7"},
		{name: "previous metadata", seed: "6", insertSeed: true, want: "7"},
		{name: "current metadata", seed: "7", insertSeed: true, want: "7"},
		{name: "newer metadata", seed: "8", insertSeed: true, want: "8"},
		{name: "malformed metadata", seed: "future", insertSeed: true, want: "future", wantErr: true},
		{name: "negative metadata", seed: "-1", insertSeed: true, want: "-1", wantErr: true},
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
		"thread_id", "claude_session_id", "claude_account_id", "claude_config_dir",
		"generation", "model", "prompt_fingerprint",
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
	migrationFinished := time.Now().UTC()
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
	if directExpiry.Before(migrationStarted.Add(180*24*time.Hour)) ||
		directExpiry.After(migrationFinished.Add(180*24*time.Hour)) {
		t.Fatalf("direct legacy expiry = %v; want between %v and %v",
			directExpiry, migrationStarted.Add(180*24*time.Hour), migrationFinished.Add(180*24*time.Hour))
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

func TestMigrateAppFutureVersionIsUntouched(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE app_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
INSERT INTO app_meta(key, value) VALUES ('schema_version', '8')`); err != nil {
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
	if got != "8" {
		t.Fatalf("future schema_version = %q; want 8", got)
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
	for _, table := range []string{"app_zalo_cli_sessions", "app_memory_revisions", "llm_providers", "llm_combos"} {
		if appTableExistsForTest(t, db, table) {
			t.Errorf("future schema gained current-only table %s", table)
		}
	}
}

func TestMigrateAppKeepsEditedSystemProvider(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createMainV4Fixture(t, db)

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	var name, revision string
	if err := db.QueryRow(`SELECT name FROM llm_providers WHERE id = 'claude-code'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'llm_route_revision'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if name != "Claude Code customized" {
		t.Errorf("claude-code name after migration = %q; want edited name", name)
	}
	if revision != "17" {
		t.Errorf("llm_route_revision after migration = %q; want 17", revision)
	}
}

func TestMigrateAppUnifiesClaudeKind(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createMainV4Fixture(t, db)
	if _, err := db.Exec(
		`UPDATE llm_providers SET kind = 'claude_code' WHERE id = 'claude-code'`); err != nil {
		t.Fatal(err)
	}

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := db.QueryRow(
		`SELECT kind FROM llm_providers WHERE id = 'claude-code'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "claude-code" {
		t.Errorf("claude-code kind after V4-to-V7 migration = %q; want claude-code", kind)
	}
}

func TestMigrateAppSeedsNoDefaultCombo(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	var combos int
	if err := db.QueryRow(`SELECT COUNT(*) FROM llm_combos`).Scan(&combos); err != nil {
		t.Fatal(err)
	}
	if combos != 0 {
		t.Errorf("combos after migration = %d; want 0", combos)
	}
	if got := appSchemaVersionForTest(t, db); got != "7" {
		t.Errorf("schema_version = %q; want 7", got)
	}
}
