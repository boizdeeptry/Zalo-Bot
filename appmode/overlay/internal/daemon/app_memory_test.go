package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

func newAppMemoryTestAPI(t *testing.T) *api {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &api{
		st: st, portalOpen: true,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func serveAppMemoryJSON(t *testing.T, a *api, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set(portalHeader, "1")
	}
	recorder := httptest.NewRecorder()
	mux := http.NewServeMux()
	a.registerAppRoutes(mux)
	mux.ServeHTTP(recorder, req)
	return recorder
}

func decodeAppMemoryResponse[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode status %d body %q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return out
}

func TestMemoryThreadHTTPCRUDUsesAuthenticatedRoutes(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	if err := a.st.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "thread/a", Name: "Chị Lan", ThreadType: ipc.ZaloThreadUser,
	}); err != nil {
		t.Fatal(err)
	}

	createdResponse := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread%2Fa", map[string]any{
		"text": "  nhận hàng buổi sáng  ", "pinned": true,
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	created := decodeAppMemoryResponse[store.AppThreadMemory](t, createdResponse)
	if created.Text != "nhận hàng buổi sáng" || created.Source != "operator" || !created.Pinned {
		t.Fatalf("created memory = %#v", created)
	}

	detailResponse := serveAppMemoryJSON(t, a, http.MethodGet, "/memory/threads/thread%2Fa", nil)
	if detailResponse.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", detailResponse.Code, detailResponse.Body.String())
	}
	detail := decodeAppMemoryResponse[store.AppThreadMemoryDetail](t, detailResponse)
	if detail.Thread.Name != "Chị Lan" || detail.Revision != 1 || len(detail.Memories) != 1 {
		t.Fatalf("detail = %#v", detail)
	}

	updatedResponse := serveAppMemoryJSON(t, a, http.MethodPut,
		"/memory/threads/thread%2Fa/"+strconv.FormatInt(created.ID, 10), map[string]any{
			"text": "nhận hàng sau 9 giờ", "pinned": false,
		})
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	updated := decodeAppMemoryResponse[store.AppThreadMemory](t, updatedResponse)
	if updated.Text != "nhận hàng sau 9 giờ" || updated.Pinned {
		t.Fatalf("updated memory = %#v", updated)
	}

	wrongOwner := serveAppMemoryJSON(t, a, http.MethodPut,
		"/memory/threads/other/"+strconv.FormatInt(created.ID, 10), map[string]any{
			"text": "không được sửa", "pinned": false,
		})
	if wrongOwner.Code != http.StatusNotFound {
		t.Fatalf("wrong owner status = %d, body = %s", wrongOwner.Code, wrongOwner.Body.String())
	}

	deletedResponse := serveAppMemoryJSON(t, a, http.MethodDelete,
		"/memory/threads/thread%2Fa/"+strconv.FormatInt(created.ID, 10), nil)
	if deletedResponse.Code != http.StatusNoContent || deletedResponse.Body.Len() != 0 {
		t.Fatalf("delete status/body = %d/%q", deletedResponse.Code, deletedResponse.Body.String())
	}
}

func TestMemoryOverviewAndLessonsHTTPContracts(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	if err := a.st.UpsertZaloThread("thread-a", "Chị Lan"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreateAppThreadMemory("thread-a", store.AppThreadMemoryInput{
		Text: "giao hàng lạnh", Pinned: true,
	}); err != nil {
		t.Fatal(err)
	}

	overviewResponse := serveAppMemoryJSON(t, a, http.MethodGet, "/memory?q=giao+h%C3%A0ng&pinned=true", nil)
	if overviewResponse.Code != http.StatusOK {
		t.Fatalf("overview status = %d, body = %s", overviewResponse.Code, overviewResponse.Body.String())
	}
	overview := decodeAppMemoryResponse[store.AppMemoryOverview](t, overviewResponse)
	if overview.Metrics.Memories != 1 || len(overview.Threads) != 1 || overview.Threads[0].ID != "thread-a" {
		t.Fatalf("overview = %#v", overview)
	}

	lessonResponse := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/lessons", map[string]any{
		"thread_id": "thread-a", "bot_text": "câu cũ", "better": "câu mới", "note": "ngắn hơn", "pinned": true,
	})
	if lessonResponse.Code != http.StatusCreated {
		t.Fatalf("lesson create status = %d, body = %s", lessonResponse.Code, lessonResponse.Body.String())
	}
	lesson := decodeAppMemoryResponse[store.AppLesson](t, lessonResponse)

	listResponse := serveAppMemoryJSON(t, a, http.MethodGet, "/memory/lessons?q=c%C3%A2u+m%E1%BB%9Bi&pinned=true", nil)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("lesson list status = %d, body = %s", listResponse.Code, listResponse.Body.String())
	}
	list := decodeAppMemoryResponse[store.AppLessonList](t, listResponse)
	if list.Revision != 1 || len(list.Lessons) != 1 || list.Lessons[0].ID != lesson.ID {
		t.Fatalf("lesson list = %#v", list)
	}

	updateResponse := serveAppMemoryJSON(t, a, http.MethodPut,
		"/memory/lessons/"+strconv.FormatInt(lesson.ID, 10), map[string]any{
			"thread_id": "thread-a", "better": "câu tốt hơn", "note": "rõ hơn", "pinned": false,
		})
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("lesson update status = %d, body = %s", updateResponse.Code, updateResponse.Body.String())
	}
	deleteResponse := serveAppMemoryJSON(t, a, http.MethodDelete,
		"/memory/lessons/"+strconv.FormatInt(lesson.ID, 10), nil)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("lesson delete status = %d, body = %s", deleteResponse.Code, deleteResponse.Body.String())
	}
}

func TestMemoryHTTPMapsValidationNotFoundAndPinLimitWithoutLeaks(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	if err := a.st.UpsertZaloThread("thread-a", "Chị Lan"); err != nil {
		t.Fatal(err)
	}

	invalid := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread-a", map[string]any{"text": ""})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
	badID := serveAppMemoryJSON(t, a, http.MethodPut, "/memory/threads/thread-a/not-a-number", map[string]any{
		"text": "hợp lệ",
	})
	if badID.Code != http.StatusBadRequest {
		t.Fatalf("bad ID status = %d, body = %s", badID.Code, badID.Body.String())
	}
	missing := serveAppMemoryJSON(t, a, http.MethodDelete, "/memory/threads/thread-a/999", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, body = %s", missing.Code, missing.Body.String())
	}
	for i := 1; i <= 12; i++ {
		response := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread-a", map[string]any{
			"text": "ghim " + strconv.Itoa(i), "pinned": true,
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("pin %d status = %d, body = %s", i, response.Code, response.Body.String())
		}
	}
	conflict := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread-a", map[string]any{
		"text": "ghim 13", "pinned": true,
	})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("pin conflict status = %d, body = %s", conflict.Code, conflict.Body.String())
	}

	for _, response := range []*httptest.ResponseRecorder{invalid, badID, missing, conflict} {
		body := strings.ToLower(response.Body.String())
		for _, forbidden := range []string{"sqlite", "select ", "insert ", "prompt", "thread-a/999"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("status %d leaked %q in %q", response.Code, forbidden, body)
			}
		}
	}
}

func TestMemoryHTTPRejectsMalformedAndTrailingJSON(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	if err := a.st.UpsertZaloThread("thread-a", "Chị Lan"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"text":`, `{"text":"hợp lệ"}{"pinned":true}`} {
		req := httptest.NewRequest(http.MethodPost, "/memory/threads/thread-a", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(portalHeader, "1")
		recorder := httptest.NewRecorder()
		mux := http.NewServeMux()
		a.registerAppRoutes(mux)
		mux.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, response = %s", body, recorder.Code, recorder.Body.String())
		}
	}
}
