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
INSERT OR IGNORE INTO app_meta(key, value) VALUES ('schema_version', '1');
`

// appLLMSchema là schema phiên bản 2: cấu hình Provider, model, chuỗi fallback và telemetry.
//
// Boolean đi kèm CHECK vì SQLite không có kiểu bool — không có ràng buộc thì một bản ghi
// enabled = 2 vẫn vào được, và mọi chỗ đọc sau đó phải tự đoán nó nghĩa là gì.
//
// KHÔNG có cột nào chứa prompt, tin nhắn khách hay câu trả lời. Đó là ràng buộc của thiết kế
// chứ không phải chuyện chưa cần tới: telemetry được đọc bởi Portal và đi vào log, nên một cột
// nội dung là một chỗ dữ liệu khách rò ra mà không ai để ý.
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

-- Khoá logic là (provider_id, model_id): cùng một tên model tồn tại ở nhiều Provider, và
-- chuỗi route chỉ có nghĩa khi biết gọi model đó QUA ai.
CREATE TABLE IF NOT EXISTS llm_models (
  provider_id TEXT NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  model_id    TEXT NOT NULL,
  name        TEXT NOT NULL DEFAULT '',
  -- source có CHECK vì ReplaceLLMModels khoanh vùng XOÁ theo đúng cột này: một giá trị lệch
  -- ('Discovered', thừa dấu cách) tạo ra model không lần đồng bộ nào dọn được, và chúng trông
  -- y hệt model thật trong danh sách.
  source      TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'discovered')),
  available   INTEGER NOT NULL DEFAULT 1 CHECK (available IN (0, 1)),
  PRIMARY KEY (provider_id, model_id)
);

-- position là PRIMARY KEY chứ không phải một cột thứ tự thường: thứ tự CHÍNH LÀ danh tính
-- của một mục route, và hai mục cùng vị trí làm thứ tự fallback trở nên không xác định.
CREATE TABLE IF NOT EXISTS llm_route_entries (
  position    INTEGER PRIMARY KEY,
  provider_id TEXT NOT NULL REFERENCES llm_providers(id),
  model_id    TEXT NOT NULL,
  enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))
);

-- Cố ý KHÔNG có khoá ngoại sang llm_providers: xoá một Provider không được xoá lịch sử vận
-- hành của nó, nếu không thì mọi lần gỡ Provider đều làm số liệu tự sửa lại quá khứ.
CREATE TABLE IF NOT EXISTS llm_attempts (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  provider_id      TEXT NOT NULL,
  model_id         TEXT NOT NULL,
  started_at       TEXT NOT NULL,
  duration_ms      INTEGER NOT NULL DEFAULT 0,
  -- outcome cùng lý do: LLMStatus tìm Provider đang phục vụ bằng outcome = 'ok', nên một giá
  -- trị ngoài danh sách làm lượt thành công đó lặng lẽ không bao giờ được tính.
  outcome          TEXT NOT NULL CHECK (outcome IN ('ok', 'error')),
  error_kind       TEXT NOT NULL DEFAULT '',
  fell_back        INTEGER NOT NULL DEFAULT 0 CHECK (fell_back IN (0, 1)),
  next_provider_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_llm_attempts_started ON llm_attempts(started_at);

-- OR IGNORE ở cả hai dòng vì migration chạy MỖI lần mở database: gieo đè sẽ trả tên Provider
-- và revision route về mặc định mỗi lần khởi động, xoá đúng thứ người dùng vừa sửa.
INSERT OR IGNORE INTO llm_providers(id, name, kind, enabled, system_provider)
VALUES ('claude-code', 'Claude Code', 'claude_code', 1, 1);
INSERT OR IGNORE INTO app_meta(key, value) VALUES ('llm_route_revision', '1');
UPDATE app_meta SET value = '2' WHERE key = 'schema_version';
`

func migrateApp(db *sql.DB) error {
	if _, err := db.Exec(appFoundationSchema); err != nil {
		return fmt.Errorf("migrate Portal foundation: %w", err)
	}
	if _, err := db.Exec(appLLMSchema); err != nil {
		return fmt.Errorf("migrate Portal LLM providers: %w", err)
	}
	return nil
}
