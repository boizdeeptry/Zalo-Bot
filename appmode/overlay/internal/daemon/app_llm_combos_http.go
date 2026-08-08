package daemon

import (
	"errors"
	"net/http"
	"strings"

	"agentdc/internal/store"
)

// comboBody là hình dạng Portal-facing của một combo. AN TOÀN: chỉ mang id + cờ + members
// (provider_id/model_id/enabled), KHÔNG config dir, KHÔNG credential — Entries dùng lại
// llmRouteEntryBody đúng như PUT /llm/route.
type comboBody struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Type     string              `json:"type"`
	Active   bool                `json:"active"`
	Revision int64               `json:"revision"`
	Entries  []llmRouteEntryBody `json:"entries"`
}

func comboToBody(c store.LLMCombo) comboBody {
	entries := make([]llmRouteEntryBody, 0, len(c.Members))
	for _, e := range c.Members {
		entries = append(entries, llmRouteEntryBody{
			Position: e.Position, ProviderID: e.ProviderID, ModelID: e.ModelID, Enabled: e.Enabled,
		})
	}
	return comboBody{
		ID: c.ID, Name: c.Name, Type: c.Type, Active: c.Active, Revision: c.Revision, Entries: entries,
	}
}

func (a *api) handleLLMComboList(w http.ResponseWriter, _ *http.Request) {
	combos, err := a.st.LLMCombos()
	if err != nil {
		a.writeLLMInternal(w, "không đọc được danh sách combo", err)
		return
	}
	bodies := make([]comboBody, 0, len(combos))
	for _, c := range combos {
		bodies = append(bodies, comboToBody(c))
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"combos": bodies})
}

func (a *api) handleLLMComboCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if !a.decodeLLMBody(w, r, &body) {
		return
	}
	c, err := a.st.CreateLLMCombo(strings.TrimSpace(body.Name), strings.TrimSpace(body.Type))
	if err != nil {
		// Phần lớn lỗi ở đây là name rỗng / type lạ — câu chữ chỉ đúng chỗ sai và chỉ chứa giá trị
		// người gọi vừa gửi. Một lỗi INSERT của database cũng lọt qua đây (store chưa tách sentinel),
		// nên log đầy đủ để nó không biến mất sau một 422 vô nghĩa. Mirror ROUTE_INVALID.
		a.logger.Warn("llm api: không tạo được combo", "err", err)
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "COMBO_INVALID",
			"Combo không hợp lệ: "+err.Error(), nil)
		return
	}
	a.writeJSON(w, http.StatusOK, comboToBody(c))
}

type comboReplaceRequest struct {
	Revision int64               `json:"revision"`
	Type     string              `json:"type"`
	Entries  []llmRouteEntryBody `json:"entries"`
}

func (a *api) handleLLMComboReplace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req comboReplaceRequest
	if !a.decodeLLMBody(w, r, &req) {
		return
	}
	entries := make([]store.LLMRouteEntry, 0, len(req.Entries))
	for _, e := range req.Entries {
		entries = append(entries, store.LLMRouteEntry{
			ProviderID: strings.TrimSpace(e.ProviderID),
			ModelID:    strings.TrimSpace(e.ModelID),
			Enabled:    e.Enabled,
		})
	}
	c, err := a.st.ReplaceLLMComboMembers(id, req.Revision, strings.TrimSpace(req.Type), entries)
	switch {
	case errors.Is(err, store.ErrLLMComboConflict):
		// CAS trả ErrLLMComboConflict cho cả revision cũ LẪN combo không tồn tại (xem store): bản
		// nháp vẫn đúng, chỉ dựa trên bản cũ — mã riêng để Portal giữ nháp và mời tải lại.
		a.writeLLMErr(w, http.StatusConflict, "COMBO_REVISION_CONFLICT",
			"Combo đã được lưu ở nơi khác trong lúc bạn đang sửa; tải lại rồi lưu tiếp", nil)
	case err != nil:
		// validateLLMRoute (dùng lại) chỉ đúng mắt xích sai và chỉ chứa id người gọi gửi lên — đó
		// là thứ Portal cần hiện. Nó cũng trả thẳng lỗi database nên log đầy đủ. Mirror ROUTE_INVALID.
		a.logger.Warn("llm api: không lưu được combo", "err", err)
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "COMBO_INVALID",
			"Combo không hợp lệ: "+err.Error(), nil)
	default:
		a.writeJSON(w, http.StatusOK, comboToBody(c))
	}
}

func (a *api) handleLLMComboActivate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch err := a.st.SetActiveLLMCombo(id); {
	case errors.Is(err, store.ErrNotFound):
		a.writeLLMErr(w, http.StatusNotFound, "COMBO_NOT_FOUND", "Không tìm thấy combo "+id, nil)
	case err != nil:
		a.writeLLMInternal(w, "không đổi được combo đang dùng", err)
	default:
		a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (a *api) handleLLMComboDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	switch err := a.st.DeleteLLMCombo(id); {
	case errors.Is(err, store.ErrLLMComboProtected):
		a.writeLLMErr(w, http.StatusConflict, "COMBO_PROTECTED",
			"Không xoá được combo đang dùng hoặc combo cuối cùng", nil)
	case errors.Is(err, store.ErrNotFound):
		a.writeLLMErr(w, http.StatusNotFound, "COMBO_NOT_FOUND", "Không tìm thấy combo "+id, nil)
	case err != nil:
		a.writeLLMInternal(w, "không xoá được combo", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
