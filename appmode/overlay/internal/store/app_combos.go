package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrLLMComboProtected chặn xoá combo đang active hoặc combo cuối cùng: bot luôn cần đúng
// một combo active để định tuyến, nên hai trạng thái đó không được để rơi vào rỗng.
var ErrLLMComboProtected = errors.New("llm combo: không xoá được combo đang dùng hoặc combo cuối")

// ErrLLMComboConflict: có người ghi members của combo này giữa lúc người khác đang sửa (CAS),
// đối xứng ErrLLMRouteConflict.
var ErrLLMComboConflict = errors.New("llm combo revision conflict")

// LLMCombo là một chiến lược định tuyến có tên: một chuỗi mắt xích (Members) chạy theo type.
type LLMCombo struct {
	ID, Name, Type string
	Active         bool
	Revision       int64
	Members        []LLMRouteEntry
}

func (s *Store) LLMCombos() ([]LLMCombo, error) {
	rows, err := s.db.Query(`SELECT id, name, type, active, revision FROM llm_combos ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list llm combos: %w", err)
	}
	defer rows.Close()
	var combos []LLMCombo
	for rows.Next() {
		var c LLMCombo
		var active int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &active, &c.Revision); err != nil {
			return nil, fmt.Errorf("scan llm combo: %w", err)
		}
		c.Active = active == 1
		combos = append(combos, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate llm combos: %w", err)
	}
	// Members đọc sau khi cursor đã đóng: pool giữ đúng một connection (SetMaxOpenConns(1)),
	// nên một query lồng trong khi rows còn mở sẽ deadlock.
	for i := range combos {
		members, err := s.comboMembers(combos[i].ID)
		if err != nil {
			return nil, err
		}
		combos[i].Members = members
	}
	return combos, nil
}

// comboMembers đọc members của một combo theo thứ tự position.
func (s *Store) comboMembers(comboID string) ([]LLMRouteEntry, error) {
	rows, err := s.db.Query(
		`SELECT position, provider_id, model_id, enabled FROM llm_combo_members
		 WHERE combo_id = ? ORDER BY position`, comboID)
	if err != nil {
		return nil, fmt.Errorf("read combo members %s: %w", comboID, err)
	}
	defer rows.Close()
	entries := make([]LLMRouteEntry, 0)
	for rows.Next() {
		var e LLMRouteEntry
		var enabled int
		if err := rows.Scan(&e.Position, &e.ProviderID, &e.ModelID, &enabled); err != nil {
			return nil, fmt.Errorf("scan combo member %s: %w", comboID, err)
		}
		e.Enabled = enabled == 1
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate combo members %s: %w", comboID, err)
	}
	return entries, nil
}

// activeCombo trả (id, type, revision) của combo đang active. Luôn có đúng một (seed default,
// SetActive giữ bất biến), nên không tìm thấy là database hỏng — trả lỗi, KHÔNG bịa mặc định.
func (s *Store) activeCombo() (id, typ string, revision int64, err error) {
	err = s.db.QueryRow(
		`SELECT id, type, revision FROM llm_combos WHERE active = 1 LIMIT 1`).Scan(&id, &typ, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, fmt.Errorf("active combo: không có combo nào đang active")
	}
	return id, typ, revision, err
}

// ReplaceLLMComboMembers ghi đè members + type của một combo nếu revision người gọi cầm vẫn mới
// nhất. Hợp lệ hoá qua validateLLMRoute (dùng lại: mắt xích cuối bật + provider/model tồn tại).
//
// CAS trước hợp lệ hoá, và cả hai cùng một transaction: một bản nháp dựa trên revision cũ phải
// được trả lời "hãy tải lại" chứ không phải lỗi trường nào đó, và mọi lỗi đều trả database về
// nguyên trạng (kể cả lần tăng revision).
func (s *Store) ReplaceLLMComboMembers(comboID string, expectedRevision int64, typ string, entries []LLMRouteEntry) (LLMCombo, error) {
	if typ != "fallback" && typ != "round_robin" {
		return LLMCombo{}, fmt.Errorf("replace combo members: type lạ %q", typ)
	}
	next := expectedRevision + 1
	err := s.inLLMTx("replace combo members", func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE llm_combos SET revision = ?, type = ? WHERE id = ? AND revision = ?`,
			next, typ, comboID, expectedRevision)
		if err != nil {
			return err
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrLLMComboConflict // sai revision HOẶC combo không tồn tại
		}
		if err := validateLLMRoute(tx, entries); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM llm_combo_members WHERE combo_id = ?`, comboID); err != nil {
			return err
		}
		for i, e := range entries {
			if _, err := tx.Exec(
				`INSERT INTO llm_combo_members(combo_id, position, provider_id, model_id, enabled) VALUES(?,?,?,?,?)`,
				comboID, i, e.ProviderID, e.ModelID, boolInt(e.Enabled)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return LLMCombo{}, err
	}
	members, err := s.comboMembers(comboID)
	if err != nil {
		return LLMCombo{}, err
	}
	return LLMCombo{ID: comboID, Type: typ, Revision: next, Members: members}, nil
}

// CreateLLMCombo thêm một combo INACTIVE: chỉ đổi combo đang phục vụ qua SetActiveLLMCombo,
// nên combo mới không được tự cướp lượt định tuyến khỏi combo đang chạy.
func (s *Store) CreateLLMCombo(name, typ string) (LLMCombo, error) {
	if name == "" {
		return LLMCombo{}, fmt.Errorf("create combo: cần tên")
	}
	if typ != "fallback" && typ != "round_robin" {
		return LLMCombo{}, fmt.Errorf("create combo: type lạ %q", typ)
	}
	id := uuid.NewString()
	if _, err := s.db.Exec(
		`INSERT INTO llm_combos(id, name, type, active, revision) VALUES(?,?,?,0,1)`,
		id, name, typ); err != nil {
		return LLMCombo{}, fmt.Errorf("create combo: %w", err)
	}
	return LLMCombo{ID: id, Name: name, Type: typ, Active: false, Revision: 1, Members: []LLMRouteEntry{}}, nil
}

// SetActiveLLMCombo bật đúng một combo trong một transaction: tắt tất cả rồi bật cái được chọn.
func (s *Store) SetActiveLLMCombo(id string) error {
	return s.inLLMTx("set active combo", func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_combos WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("không có combo %s: %w", id, ErrNotFound)
		}
		if _, err := tx.Exec(`UPDATE llm_combos SET active = 0`); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE llm_combos SET active = 1 WHERE id = ?`, id)
		return err
	})
}

// DeleteLLMCombo xoá một combo cùng members của nó, chặn khi combo đang active hoặc là combo cuối.
func (s *Store) DeleteLLMCombo(id string) error {
	return s.inLLMTx("delete combo", func(tx *sql.Tx) error {
		var active, total int
		switch err := tx.QueryRow(`SELECT active FROM llm_combos WHERE id = ?`, id).Scan(&active); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("không có combo %s: %w", id, ErrNotFound)
		case err != nil:
			return err
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM llm_combos`).Scan(&total); err != nil {
			return err
		}
		if active == 1 || total <= 1 {
			return ErrLLMComboProtected
		}
		// Xoá members TAY chứ không dựa ON DELETE CASCADE: connection này không bật
		// PRAGMA foreign_keys (xem DSN trong Open), nên cascade là no-op và member sẽ mồ côi.
		if _, err := tx.Exec(`DELETE FROM llm_combo_members WHERE combo_id = ?`, id); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM llm_combos WHERE id = ?`, id)
		return err
	})
}
