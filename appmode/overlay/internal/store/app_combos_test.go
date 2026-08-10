package store

import (
	"errors"
	"testing"
)

// newLLMStore (app_llm_test.go) mở qua Open(":memory:") nên đã chạy migrateApp. §7 bỏ gieo combo
// mặc định, nên store mới RỖNG (0 combo). Test cần một combo active có sẵn gọi seedDefaultCombo.

// §7: máy mới ship RỖNG — 0 combo, không có combo active, LLMRoute trả entries rỗng. Đây là điều
// kiện để bản cài mới KHÔNG có định tuyến mặc định (router im, §6).
func TestFreshStoreHasNoDefaultCombo(t *testing.T) {
	st := newLLMStore(t)
	combos, err := st.LLMCombos()
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) != 0 {
		t.Fatalf("fresh store combos = %d; want 0 (no default)", len(combos))
	}
	route, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if len(route.Entries) != 0 {
		t.Fatalf("fresh store route entries = %d; want 0", len(route.Entries))
	}
}

func TestCreateComboIsInactiveByDefault(t *testing.T) {
	st := newLLMStore(t)
	seedDefaultCombo(t, st)
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
	seedDefaultCombo(t, st)
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

func TestReplaceComboMembersIsCAS(t *testing.T) {
	st := newLLMStore(t)
	seedDefaultCombo(t, st)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
	entries := []LLMRouteEntry{{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true}}
	saved, err := st.ReplaceLLMComboMembers("default", 1, "round_robin", entries)
	if err != nil {
		t.Fatalf("ReplaceLLMComboMembers(rev 1) = %v", err)
	}
	if saved.Revision != 2 || saved.Type != "round_robin" || len(saved.Members) != 1 {
		t.Fatalf("saved = rev %d type %q members %d; want rev 2 round_robin 1", saved.Revision, saved.Type, len(saved.Members))
	}
	if _, err := st.ReplaceLLMComboMembers("default", 1, "fallback", entries); !errors.Is(err, ErrLLMComboConflict) {
		t.Fatalf("stale rev = %v; want ErrLLMComboConflict", err)
	}
}

func TestLLMRouteResolvesActiveCombo(t *testing.T) {
	st := newLLMStore(t)
	seedDefaultCombo(t, st)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
	if _, err := st.ReplaceLLMComboMembers("default", 1, "round_robin",
		[]LLMRouteEntry{{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	snap, err := st.LLMRoute()
	if err != nil {
		t.Fatalf("LLMRoute() = %v", err)
	}
	if snap.Type != "round_robin" || snap.ComboID != "default" || len(snap.Entries) != 1 {
		t.Errorf("snapshot = type %q combo %q entries %d; want round_robin/default/1", snap.Type, snap.ComboID, len(snap.Entries))
	}
}

// Một combo ĐANG TẮT vẫn giữ Provider "đang dùng": nó có thể được bật lại sau, và xoá Provider
// nó trỏ tới để lại chuỗi cụt. Chốt điều này để một hồi quy `WHERE active = 1` không lọt qua.
func TestDeleteProviderBlockedByInactiveComboMember(t *testing.T) {
	st := newLLMStore(t)
	addAPIProvider(t, st, "openai-1", "gpt-5-mini", true)
	c, err := st.CreateLLMCombo("Dự phòng", "fallback")
	if err != nil {
		t.Fatalf("CreateLLMCombo = %v", err)
	}
	if c.Active {
		t.Fatal("combo mới phải inactive (điều kiện của test)")
	}
	if _, err := st.ReplaceLLMComboMembers(c.ID, 1, "fallback",
		[]LLMRouteEntry{{ProviderID: "openai-1", ModelID: "gpt-5-mini", Enabled: true}}); err != nil {
		t.Fatalf("ReplaceLLMComboMembers(inactive combo) = %v", err)
	}
	if err := st.DeleteLLMProvider("openai-1"); !errors.Is(err, ErrLLMProviderInUse) {
		t.Fatalf("DeleteLLMProvider referenced by INACTIVE combo = %v; want ErrLLMProviderInUse", err)
	}
}

// Khoá ngoại TẮT trên connection này (DSN không bật foreign_keys), nên ON DELETE CASCADE
// không chạy — members phải bị xoá tay trong cùng transaction, nếu không sẽ để lại member mồ côi.
func TestDeleteComboRemovesMembers(t *testing.T) {
	st := newLLMStore(t)
	seedDefaultCombo(t, st) // cần combo thứ 2 để 'Xoay vòng' không phải combo cuối (DeleteLLMCombo chặn combo cuối)
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
