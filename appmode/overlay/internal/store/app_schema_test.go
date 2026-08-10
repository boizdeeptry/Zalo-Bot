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
	if version != "4" {
		t.Fatalf("schema_version = %q; want 4", version)
	}

	// Provider hệ thống được gieo trong migration chứ không phải lúc chạy: sau §6 route không
	// còn BẮT BUỘC kết thúc bằng Claude Code, nhưng hàng Provider này vẫn phải có — nó là lưới an
	// toàn mà mọi chuỗi có Claude Code trỏ tới, và cửa chặn sửa/xoá Provider hệ thống dựa vào nó.
	var kind string
	var system int
	if err := db.QueryRow(
		`SELECT kind, system_provider FROM llm_providers WHERE id = 'claude-code'`).Scan(&kind, &system); err != nil {
		t.Fatalf("read seeded claude-code provider: %v", err)
	}
	if kind != "claude-code" || system != 1 {
		t.Fatalf("seeded claude-code kind/system_provider = %q/%d; want claude-code/1", kind, system)
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

// Đường nâng cấp: một máy đã cài từ trước có hàng claude-code với kind='claude_code' (gạch
// dưới). migrateApp phải sửa nó thành 'claude-code' (gạch nối) — INSERT OR IGNORE không đụng
// tới hàng đã có, nên cần UPDATE riêng. Không sửa thì kind lệch descriptor/envVarFor vĩnh viễn.
func TestMigrateAppUnifiesClaudeKind(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := migrateApp(db); err != nil {
		t.Fatal(err)
	}
	// Ép về kind cũ (gạch dưới) như một máy đã cài trước khi hợp nhất.
	if _, err := db.Exec(
		`UPDATE llm_providers SET kind = 'claude_code' WHERE id = 'claude-code'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateApp(db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	var kind string
	if err := db.QueryRow(
		`SELECT kind FROM llm_providers WHERE id = 'claude-code'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "claude-code" {
		t.Errorf("claude-code kind after re-migration = %q; want claude-code (hyphen)", kind)
	}
}

// §7: migration KHÔNG gieo combo mặc định — máy mới ship RỖNG để không có định tuyến mặc định.
// (Bảng llm_combos vẫn được tạo; chỉ không có hàng nào.)
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
		t.Fatalf("count combos: %v", err)
	}
	if combos != 0 {
		t.Errorf("combos after migration = %d; want 0 (no seeded default)", combos)
	}

	var version string
	if err := db.QueryRow(`SELECT value FROM app_meta WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != "4" {
		t.Errorf("schema_version = %q; want 4", version)
	}
}
