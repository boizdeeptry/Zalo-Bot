package store

import (
	"database/sql"
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

	// Provider hệ thống được gieo trong migration chứ không phải lúc chạy: route hợp lệ
	// BẮT BUỘC kết thúc bằng Claude Code, nên thiếu hàng này thì không lưu nổi route nào.
	var kind string
	var system int
	if err := db.QueryRow(
		`SELECT kind, system_provider FROM llm_providers WHERE id = 'claude-code'`).Scan(&kind, &system); err != nil {
		t.Fatalf("read seeded claude-code provider: %v", err)
	}
	if kind != "claude_code" || system != 1 {
		t.Fatalf("seeded claude-code kind/system_provider = %q/%d; want claude_code/1", kind, system)
	}

	var revision string
	if err := db.QueryRow(
		`SELECT value FROM app_meta WHERE key = 'llm_route_revision'`).Scan(&revision); err != nil {
		t.Fatalf("read llm_route_revision: %v", err)
	}
	if revision != "1" {
		t.Fatalf("llm_route_revision = %q; want 1", revision)
	}
}

// Lần chạy thứ hai không được gieo đè: nếu INSERT OR IGNORE trượt thì cấu hình Provider
// người dùng đã sửa sẽ bị trả về mặc định mỗi lần mở máy.
func TestMigrateAppKeepsEditedSystemProvider(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`UPDATE llm_providers SET name = 'Claude Code (đã đổi tên)' WHERE id = 'claude-code'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE app_meta SET value = '7' WHERE key = 'llm_route_revision'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateApp(db); err != nil {
		t.Fatalf("second migrateApp: %v", err)
	}

	var name, revision string
	if err := db.QueryRow(`SELECT name FROM llm_providers WHERE id = 'claude-code'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Claude Code (đã đổi tên)" {
		t.Errorf("claude-code name after re-migration = %q; want the edited name", name)
	}
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key = 'llm_route_revision'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != "7" {
		t.Errorf("llm_route_revision after re-migration = %q; want 7", revision)
	}
}
