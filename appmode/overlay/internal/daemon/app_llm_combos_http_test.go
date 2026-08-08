package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentdc/internal/store"
)

// getCombos gọi handler list và giải mã envelope {combos:[…]}.
func getCombos(t *testing.T, a *api) []comboBody {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/llm/combos", nil)
	a.handleLLMComboList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /llm/combos = %d; want 200 (body=%s)", rec.Code, rec.Body)
	}
	assertNoConfigLeak(t, rec.Body.Bytes())
	var out struct {
		Combos []comboBody `json:"combos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode combos: %v (body=%s)", err, rec.Body)
	}
	return out.Combos
}

// errCode rút Code trong envelope lỗi {error:{code,…}}.
func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode error body: %v (body=%s)", err, body)
	}
	return out.Error.Code
}

// assertNoConfigLeak canary: KHÔNG bề mặt combo nào được rò config dir hay đường dẫn account.
// Combo hôm nay chỉ có provider_id+model_id nên không có gì để rò — chốt lại để một trường thêm
// sau này không lặng lẽ mang config_dir ra Portal.
func assertNoConfigLeak(t *testing.T, body []byte) {
	t.Helper()
	s := string(body)
	if strings.Contains(s, "config_dir") || strings.Contains(s, "accounts\\") {
		t.Errorf("combo response rò đường dẫn nội bộ: %s", s)
	}
}

// seedComboAPIProvider dựng một Provider API kèm một model, đủ để validateLLMRoute cho PUT đi qua.
func seedComboAPIProvider(t *testing.T, a *api, id, modelID string) {
	t.Helper()
	if err := a.st.CreateLLMProvider(store.LLMProvider{ID: id, Name: id, Kind: "openai", Enabled: true}); err != nil {
		t.Fatalf("CreateLLMProvider(%q) = %v", id, err)
	}
	if err := a.st.AddLLMModel(store.LLMModel{
		ProviderID: id, ModelID: modelID, Name: modelID, Source: store.LLMModelManual, Available: true,
	}); err != nil {
		t.Fatalf("AddLLMModel(%q,%q) = %v", id, modelID, err)
	}
}

func TestComboEndpoints(t *testing.T) {
	a := newAppRouteAPI(t)

	// GET: combo default được gieo sẵn (active, fallback).
	combos := getCombos(t, a)
	if len(combos) != 1 {
		t.Fatalf("initial combos = %d; want 1 (seeded default)", len(combos))
	}
	if def := combos[0]; def.ID != "default" || def.Type != "fallback" || !def.Active {
		t.Fatalf("default combo = %+v; want id=default type=fallback active=true", def)
	}

	// DELETE combo default: đang active VÀ là combo cuối → 409 COMBO_PROTECTED.
	recDel := httptest.NewRecorder()
	reqDel := httptest.NewRequest("DELETE", "/llm/combos/default", nil)
	reqDel.SetPathValue("id", "default")
	a.handleLLMComboDelete(recDel, reqDel)
	if recDel.Code != http.StatusConflict {
		t.Fatalf("DELETE /llm/combos/default = %d; want 409", recDel.Code)
	}
	if code := errCode(t, recDel.Body.Bytes()); code != "COMBO_PROTECTED" {
		t.Errorf("delete-protected code = %q; want COMBO_PROTECTED", code)
	}
	assertNoConfigLeak(t, recDel.Body.Bytes())

	// POST create {name,type:round_robin} → 200, id mới, inactive.
	recNew := httptest.NewRecorder()
	reqNew := httptest.NewRequest("POST", "/llm/combos", strings.NewReader(`{"name":"Xoay vòng","type":"round_robin"}`))
	a.handleLLMComboCreate(recNew, reqNew)
	if recNew.Code != http.StatusOK {
		t.Fatalf("POST /llm/combos = %d; want 200 (body=%s)", recNew.Code, recNew.Body)
	}
	assertNoConfigLeak(t, recNew.Body.Bytes())
	var created comboBody
	if err := json.Unmarshal(recNew.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" || created.ID == "default" {
		t.Fatalf("created id = %q; want a fresh non-default id", created.ID)
	}
	if created.Active {
		t.Error("created combo active=true; want inactive")
	}
	if created.Type != "round_robin" {
		t.Errorf("created type = %q; want round_robin", created.Type)
	}

	// POST activate combo mới → 200; con trỏ active phải xoay sang nó.
	recAct := httptest.NewRecorder()
	reqAct := httptest.NewRequest("POST", "/llm/combos/"+created.ID+"/activate", nil)
	reqAct.SetPathValue("id", created.ID)
	a.handleLLMComboActivate(recAct, reqAct)
	if recAct.Code != http.StatusOK {
		t.Fatalf("POST activate = %d; want 200 (body=%s)", recAct.Code, recAct.Body)
	}
	for _, c := range getCombos(t, a) {
		switch c.ID {
		case created.ID:
			if !c.Active {
				t.Error("sau activate: combo mới không active")
			}
		case "default":
			if c.Active {
				t.Error("sau activate: default vẫn active")
			}
		}
	}

	// PUT members hợp lệ + đúng revision (combo mới đang ở revision 1) → 200, revision 2.
	seedComboAPIProvider(t, a, "openai-1", "gpt-5-mini")
	putBody := `{"revision":1,"type":"round_robin","entries":[{"provider_id":"openai-1","model_id":"gpt-5-mini","enabled":true}]}`
	recPut := httptest.NewRecorder()
	reqPut := httptest.NewRequest("PUT", "/llm/combos/"+created.ID, strings.NewReader(putBody))
	reqPut.SetPathValue("id", created.ID)
	a.handleLLMComboReplace(recPut, reqPut)
	if recPut.Code != http.StatusOK {
		t.Fatalf("PUT combo (valid) = %d; want 200 (body=%s)", recPut.Code, recPut.Body)
	}
	assertNoConfigLeak(t, recPut.Body.Bytes())
	var replaced comboBody
	if err := json.Unmarshal(recPut.Body.Bytes(), &replaced); err != nil {
		t.Fatalf("decode put response: %v", err)
	}
	if replaced.Revision != 2 || len(replaced.Entries) != 1 {
		t.Errorf("replaced = rev %d entries %d; want rev 2 entries 1", replaced.Revision, len(replaced.Entries))
	}

	// PUT lại với revision 1 (giờ đã cũ, combo ở revision 2) → 409 COMBO_REVISION_CONFLICT.
	recStale := httptest.NewRecorder()
	reqStale := httptest.NewRequest("PUT", "/llm/combos/"+created.ID, strings.NewReader(putBody))
	reqStale.SetPathValue("id", created.ID)
	a.handleLLMComboReplace(recStale, reqStale)
	if recStale.Code != http.StatusConflict {
		t.Fatalf("PUT combo (stale rev) = %d; want 409 (body=%s)", recStale.Code, recStale.Body)
	}
	if code := errCode(t, recStale.Body.Bytes()); code != "COMBO_REVISION_CONFLICT" {
		t.Errorf("stale-rev code = %q; want COMBO_REVISION_CONFLICT", code)
	}

	// POST create với name rỗng → 422 COMBO_INVALID (store trả "cần tên").
	recBad := httptest.NewRecorder()
	reqBad := httptest.NewRequest("POST", "/llm/combos", strings.NewReader(`{"name":"","type":"fallback"}`))
	a.handleLLMComboCreate(recBad, reqBad)
	if recBad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /llm/combos (empty name) = %d; want 422 (body=%s)", recBad.Code, recBad.Body)
	}
	if code := errCode(t, recBad.Body.Bytes()); code != "COMBO_INVALID" {
		t.Errorf("empty-name code = %q; want COMBO_INVALID", code)
	}

	// activate combo không tồn tại → 404 COMBO_NOT_FOUND.
	recActNF := httptest.NewRecorder()
	reqActNF := httptest.NewRequest("POST", "/llm/combos/nope/activate", nil)
	reqActNF.SetPathValue("id", "nope")
	a.handleLLMComboActivate(recActNF, reqActNF)
	if recActNF.Code != http.StatusNotFound {
		t.Fatalf("POST activate(nope) = %d; want 404 (body=%s)", recActNF.Code, recActNF.Body)
	}
	if code := errCode(t, recActNF.Body.Bytes()); code != "COMBO_NOT_FOUND" {
		t.Errorf("activate-missing code = %q; want COMBO_NOT_FOUND", code)
	}

	// delete combo không tồn tại → 404 COMBO_NOT_FOUND.
	recDelNF := httptest.NewRecorder()
	reqDelNF := httptest.NewRequest("DELETE", "/llm/combos/nope", nil)
	reqDelNF.SetPathValue("id", "nope")
	a.handleLLMComboDelete(recDelNF, reqDelNF)
	if recDelNF.Code != http.StatusNotFound {
		t.Fatalf("DELETE /llm/combos/nope = %d; want 404 (body=%s)", recDelNF.Code, recDelNF.Body)
	}
	if code := errCode(t, recDelNF.Body.Bytes()); code != "COMBO_NOT_FOUND" {
		t.Errorf("delete-missing code = %q; want COMBO_NOT_FOUND", code)
	}
}

// TestComboRoutesRegisteredAndCookieReachable mirror TestAppRoutesAreRegisteredAndCookieReachable:
// 5 route combo mới phải khớp đúng pattern qua ServeMux VÀ nằm trong allowlist cookie của Portal.
func TestComboRoutesRegisteredAndCookieReachable(t *testing.T) {
	a := newAppRouteAPI(t)
	t.Cleanup(func() { connectMgr = nil })
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)

	tests := []struct{ pattern, path string }{
		{"GET /llm/combos", "/llm/combos"},
		{"POST /llm/combos", "/llm/combos"},
		{"PUT /llm/combos/{id}", "/llm/combos/default"},
		{"POST /llm/combos/{id}/activate", "/llm/combos/default/activate"},
		{"DELETE /llm/combos/{id}", "/llm/combos/default"},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			method, _, _ := strings.Cut(tt.pattern, " ")
			req := httptest.NewRequest(method, tt.path, nil)
			_, gotPattern := mux.Handler(req)
			if gotPattern != tt.pattern {
				t.Fatalf("matched pattern = %q; want %q", gotPattern, tt.pattern)
			}
			if !cookieAllowedPaths[tt.pattern] {
				t.Fatalf("cookieAllowedPaths[%q] = false; Portal cannot reach route", tt.pattern)
			}
		})
	}
}
