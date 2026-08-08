package store

import (
	"errors"
	"testing"
)

// newLLMStore (app_llm_test.go) mở qua Open(":memory:") nên đã chạy migrateApp — migration
// gieo combo 'default' active=1. Dùng lại nó thay vì dựng thêm một constructor thứ hai.

func TestCreateComboIsInactiveByDefault(t *testing.T) {
	st := newLLMStore(t)
	c, err := st.CreateLLMCombo("Xoay vòng", "round_robin")
	if err != nil {
		t.Fatalf("CreateLLMCombo = %v", err)
	}
	if c.Active {
		t.Error("new combo is active; want inactive (default stays active)")
	}
	// Handler HTTP echo lại struct này KHÔNG đọc lại DB, nên các trường phải đúng ngay tại đây.
	if c.ID == "" {
		t.Error("CreateLLMCombo returned empty ID")
	}
	if c.Name != "Xoay vòng" || c.Type != "round_robin" || c.Revision != 1 {
		t.Errorf("returned combo = (%q,%q,rev %d); want (Xoay vòng,round_robin,1)", c.Name, c.Type, c.Revision)
	}
	combos, err := st.LLMCombos()
	if err != nil || len(combos) != 2 {
		t.Fatalf("LLMCombos() = %d combos, %v; want 2", len(combos), err)
	}
}

func TestCreateComboRejectsBadInput(t *testing.T) {
	st := newLLMStore(t)
	if _, err := st.CreateLLMCombo("", "fallback"); err == nil {
		t.Error("CreateLLMCombo empty name = nil; want error")
	}
	if _, err := st.CreateLLMCombo("x", "bogus"); err == nil {
		t.Error(`CreateLLMCombo type "bogus" = nil; want error`)
	}
}

func TestSetActiveComboFlipsExactlyOne(t *testing.T) {
	st := newLLMStore(t)
	c, _ := st.CreateLLMCombo("Xoay vòng", "round_robin")
	if err := st.SetActiveLLMCombo(c.ID); err != nil {
		t.Fatalf("SetActiveLLMCombo = %v", err)
	}
	combos, _ := st.LLMCombos()
	active := 0
	for _, x := range combos {
		if x.Active {
			active++
		}
	}
	if active != 1 {
		t.Errorf("active combos = %d; want exactly 1", active)
	}
}

func TestDeleteComboRefusesActiveAndLast(t *testing.T) {
	st := newLLMStore(t)
	if err := st.DeleteLLMCombo("default"); !errors.Is(err, ErrLLMComboProtected) {
		t.Fatalf("delete active = %v; want ErrLLMComboProtected", err)
	}
	c, _ := st.CreateLLMCombo("Xoay vòng", "round_robin")
	if err := st.DeleteLLMCombo(c.ID); err != nil {
		t.Fatalf("delete inactive = %v; want nil", err)
	}
	if err := st.DeleteLLMCombo("default"); !errors.Is(err, ErrLLMComboProtected) {
		t.Fatalf("delete last = %v; want ErrLLMComboProtected", err)
	}
}

// Khoá ngoại TẮT trên connection này (DSN không bật foreign_keys), nên ON DELETE CASCADE
// không chạy — members phải bị xoá tay trong cùng transaction, nếu không sẽ để lại member mồ côi.
func TestDeleteComboRemovesMembers(t *testing.T) {
	st := newLLMStore(t)
	c, _ := st.CreateLLMCombo("Xoay vòng", "round_robin")
	// Chèn thẳng một member: API thêm member thuộc task sau, test đi qua db nội bộ (cùng package).
	if _, err := st.db.Exec(
		`INSERT INTO llm_combo_members(combo_id, position, provider_id, model_id, enabled) VALUES(?,0,'claude-code','x',1)`,
		c.ID); err != nil {
		t.Fatalf("seed combo member: %v", err)
	}
	if err := st.DeleteLLMCombo(c.ID); err != nil {
		t.Fatalf("DeleteLLMCombo = %v", err)
	}
	var n int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM llm_combo_members WHERE combo_id = ?`, c.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("members after delete = %d; want 0 (orphan members left behind)", n)
	}
}
