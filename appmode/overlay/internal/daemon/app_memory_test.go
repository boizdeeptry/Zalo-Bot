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
	"time"

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

func seedAppMemoryHTTPGroup(t *testing.T, a *api, threadID string, uids ...string) {
	t.Helper()
	if err := a.st.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: threadID, Name: "Nhóm HTTP", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range uids {
		if err := a.st.UpsertZaloUser(ipc.ZaloUser{
			UID: uid, DisplayName: "Thành viên " + uid,
		}); err != nil {
			t.Fatal(err)
		}
		if err := a.st.UpsertZaloGroupMemberForTest(threadID, uid); err != nil {
			t.Fatal(err)
		}
	}
}

func applyAppMemoryHTTPProposal(
	t *testing.T,
	a *api,
	threadID, uid, memoryKey, text, category string,
	confidence float64,
	now time.Time,
) {
	t.Helper()
	result, err := a.st.ApplyAppMemoryOperations(store.AppMemoryApplyInput{
		ThreadID: threadID, SubjectUID: uid, Now: now,
		Operations: []store.AppMemoryOperation{{
			Action: "add", MemoryKey: memoryKey, Value: text,
			Category: category, Confidence: confidence,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Active+result.Pending != 1 {
		t.Fatalf("apply result = %#v; want one active or pending Memory", result)
	}
}

func findAppMemoryHTTPRow(t *testing.T, rows []store.AppThreadMemory, memoryKey string) store.AppThreadMemory {
	t.Helper()
	for _, row := range rows {
		if row.MemoryKey == memoryKey {
			return row
		}
	}
	t.Fatalf("Memory key %q not found in %#v", memoryKey, rows)
	return store.AppThreadMemory{}
}

func assertAppMemoryBodyOmits(t *testing.T, response *httptest.ResponseRecorder, forbidden string) {
	t.Helper()
	if strings.Contains(response.Body.String(), forbidden) {
		t.Fatalf("status %d leaked %q in %q", response.Code, forbidden, response.Body.String())
	}
}

func TestMemoryThreadHTTPCRUDUsesAuthenticatedRoutes(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	if err := a.st.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "thread/a", Name: "Chị Lan", ThreadType: ipc.ZaloThreadUser,
	}); err != nil {
		t.Fatal(err)
	}

	createdResponse := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread%2Fa", map[string]any{
		"uid": "thread/a", "memory_key": "preference.delivery",
		"text": "  nhận hàng buổi sáng  ", "category": "preference",
		"pinned": true, "expected_revision": 0,
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
			"uid": "thread/a", "memory_key": "preference.delivery",
			"text": "nhận hàng sau 9 giờ", "category": "preference",
			"pinned": false, "expected_revision": 1,
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
			"uid": "thread/a", "memory_key": "preference.delivery",
			"text": "không được sửa", "category": "preference",
			"pinned": false, "expected_revision": 2,
		})
	if wrongOwner.Code != http.StatusNotFound {
		t.Fatalf("wrong owner status = %d, body = %s", wrongOwner.Code, wrongOwner.Body.String())
	}

	deletedResponse := serveAppMemoryJSON(t, a, http.MethodDelete,
		"/memory/threads/thread%2Fa/"+strconv.FormatInt(created.ID, 10), map[string]any{
			"uid": "thread/a", "expected_revision": 2,
		})
	if deletedResponse.Code != http.StatusNoContent || deletedResponse.Body.Len() != 0 {
		t.Fatalf("delete status/body = %d/%q", deletedResponse.Code, deletedResponse.Body.String())
	}
}

func TestMemoryV2ThreadHTTPReadIsolatesSelectedMemberAndCommonScopes(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	seedAppMemoryHTTPGroup(t, a, "group-a", "u-1", "u-2")

	if _, err := a.st.CreateAppThreadMemoryV2("group-a", store.AppThreadMemoryInput{
		UID: "u-1", MemoryKey: "profile.note", Text: "U1-ACTIVE-MEMORY",
		Category: "profile", ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreateAppThreadMemoryV2("group-a", store.AppThreadMemoryInput{
		UID: "u-2", MemoryKey: "identity.private", Text: "U2-PRIVATE-MEMORY",
		Category: "identity", ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.st.CreateAppThreadMemoryV2("group-a", store.AppThreadMemoryInput{
		MemoryKey: "preference.common", Text: "COMMON-MEMORY",
		Category: "preference", ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}

	memberResponse := serveAppMemoryJSON(
		t, a, http.MethodGet, "/memory/threads/group-a?uid=u-1", nil,
	)
	if memberResponse.Code != http.StatusOK {
		t.Fatalf("member detail status = %d, body = %s", memberResponse.Code, memberResponse.Body.String())
	}
	member := decodeAppMemoryResponse[store.AppThreadMemoryDetail](t, memberResponse)
	if member.SelectedUID != "u-1" || len(member.Active) != 1 ||
		member.Active[0].UID != "u-1" || member.Active[0].Text != "U1-ACTIVE-MEMORY" {
		t.Fatalf("selected member detail = %#v", member)
	}
	assertAppMemoryBodyOmits(t, memberResponse, "U2-PRIVATE-MEMORY")

	commonResponse := serveAppMemoryJSON(t, a, http.MethodGet, "/memory/threads/group-a", nil)
	if commonResponse.Code != http.StatusOK {
		t.Fatalf("common detail status = %d, body = %s", commonResponse.Code, commonResponse.Body.String())
	}
	common := decodeAppMemoryResponse[store.AppThreadMemoryDetail](t, commonResponse)
	if common.SelectedUID != "" || len(common.Active) != 1 ||
		common.Active[0].UID != "" || common.Active[0].Text != "COMMON-MEMORY" {
		t.Fatalf("common detail = %#v", common)
	}
	assertAppMemoryBodyOmits(t, commonResponse, "U2-PRIVATE-MEMORY")
}

func TestMemoryV2ThreadHTTPDecodesEscapedThreadAndSelectedUIDExactly(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	seedAppMemoryHTTPGroup(t, a, "group/a", "u/1")

	createdResponse := serveAppMemoryJSON(t, a, http.MethodPost,
		"/memory/threads/group%2Fa", map[string]any{
			"uid": "u/1", "memory_key": "profile.escaped", "text": "đúng phạm vi",
			"category": "profile", "expected_revision": 0,
		})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("escaped create status = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	created := decodeAppMemoryResponse[store.AppThreadMemory](t, createdResponse)
	if created.ThreadID != "group/a" || created.UID != "u/1" {
		t.Fatalf("escaped create = %#v", created)
	}

	response := serveAppMemoryJSON(t, a, http.MethodGet,
		"/memory/threads/group%2Fa?uid=u%2F1", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("escaped read status = %d, body = %s", response.Code, response.Body.String())
	}
	detail := decodeAppMemoryResponse[store.AppThreadMemoryDetail](t, response)
	if detail.Thread.ID != "group/a" || detail.SelectedUID != "u/1" ||
		len(detail.Active) != 1 || detail.Active[0].UID != "u/1" {
		t.Fatalf("escaped detail = %#v", detail)
	}
}

func TestMemoryV2ThreadHTTPLifecycleApproveRejectAndRestore(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	seedAppMemoryHTTPGroup(t, a, "group-a", "u-1", "u-2")
	if _, err := a.st.CreateAppThreadMemoryV2("group-a", store.AppThreadMemoryInput{
		UID: "u-2", MemoryKey: "identity.private", Text: "U2-PRIVATE-MEMORY",
		Category: "identity", ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}
	active, err := a.st.CreateAppThreadMemoryV2("group-a", store.AppThreadMemoryInput{
		UID: "u-1", MemoryKey: "profile.occupation", Text: "là giáo viên",
		Category: "profile", ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyAppMemoryHTTPProposal(t, a, "group-a", "u-1", "profile.occupation",
		"là y sĩ", "profile", 0.95, time.Now())
	detail, err := a.st.AppThreadMemoryScope("group-a", "u-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pending := findAppMemoryHTTPRow(t, detail.Pending, "profile.occupation")

	approveResponse := serveAppMemoryJSON(t, a, http.MethodPost,
		"/memory/threads/group-a/"+strconv.FormatInt(pending.ID, 10)+"/approve", map[string]any{
			"uid": "u-1", "memory_key": "profile.occupation", "text": "là bác sĩ",
			"category": "profile", "expected_revision": 1,
		})
	if approveResponse.Code != http.StatusOK {
		t.Fatalf("approve status = %d, body = %s", approveResponse.Code, approveResponse.Body.String())
	}
	approved := decodeAppMemoryResponse[store.AppThreadMemory](t, approveResponse)
	if approved.ID != pending.ID || approved.Status != "active" || approved.Text != "là bác sĩ" ||
		approved.MemoryKey != "profile.occupation" || approved.Category != "profile" ||
		approved.SupersedesID != active.ID {
		t.Fatalf("approved Memory = %#v", approved)
	}
	assertAppMemoryBodyOmits(t, approveResponse, "U2-PRIVATE-MEMORY")
	readback := serveAppMemoryJSON(t, a, http.MethodGet, "/memory/threads/group-a?uid=u-1", nil)
	readbackDetail := decodeAppMemoryResponse[store.AppThreadMemoryDetail](t, readback)
	if readbackDetail.Revision != 2 ||
		findAppMemoryHTTPRow(t, readbackDetail.Active, "profile.occupation").Text != "là bác sĩ" {
		t.Fatalf("approval readback = %#v", readbackDetail)
	}
	assertAppMemoryBodyOmits(t, readback, "U2-PRIVATE-MEMORY")

	applyAppMemoryHTTPProposal(t, a, "group-a", "u-1", "health.note",
		"đề xuất cần bỏ", "health", 1, time.Now())
	detail, err = a.st.AppThreadMemoryScope("group-a", "u-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rejected := findAppMemoryHTTPRow(t, detail.Pending, "health.note")
	rejectResponse := serveAppMemoryJSON(t, a, http.MethodPost,
		"/memory/threads/group-a/"+strconv.FormatInt(rejected.ID, 10)+"/reject", map[string]any{
			"uid": "u-1", "expected_revision": 2,
		})
	if rejectResponse.Code != http.StatusNoContent || rejectResponse.Body.Len() != 0 {
		t.Fatalf("reject status/body = %d/%q", rejectResponse.Code, rejectResponse.Body.String())
	}
	detail, err = a.st.AppThreadMemoryScope("group-a", "u-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range detail.Pending {
		if row.ID == rejected.ID {
			t.Fatalf("rejected proposal still present: %#v", row)
		}
	}

	past := time.Now().Add(-31 * 24 * time.Hour)
	applyAppMemoryHTTPProposal(t, a, "group-a", "u-1", "interest.topic",
		"thích trồng lan", "interest", 0.95, past)
	detail, err = a.st.AppThreadMemoryScope("group-a", "u-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	expired := findAppMemoryHTTPRow(t, detail.Expired, "interest.topic")
	if detail.Revision != 4 {
		t.Fatalf("revision after expiry = %d; want 4", detail.Revision)
	}
	restoreResponse := serveAppMemoryJSON(t, a, http.MethodPost,
		"/memory/threads/group-a/"+strconv.FormatInt(expired.ID, 10)+"/restore", map[string]any{
			"uid": "u-1", "expected_revision": detail.Revision,
		})
	if restoreResponse.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", restoreResponse.Code, restoreResponse.Body.String())
	}
	restored := decodeAppMemoryResponse[store.AppThreadMemory](t, restoreResponse)
	if restored.ID != expired.ID || restored.Status != "active" || restored.Text != "thích trồng lan" {
		t.Fatalf("restored Memory = %#v", restored)
	}
	assertAppMemoryBodyOmits(t, restoreResponse, "U2-PRIVATE-MEMORY")
}

func TestMemoryV2ThreadHTTPManualCRUDUsesSelectedUIDAndRevision(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	seedAppMemoryHTTPGroup(t, a, "group-a", "u-1", "u-2")
	canary, err := a.st.CreateAppThreadMemoryV2("group-a", store.AppThreadMemoryInput{
		UID: "u-2", MemoryKey: "identity.private", Text: "U2-PRIVATE-MEMORY",
		Category: "identity", ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	createResponse := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/group-a", map[string]any{
		"uid": "u-1", "memory_key": "preference.delivery", "text": "giao buổi sáng",
		"category": "preference", "pinned": false, "expected_revision": 0,
	})
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("manual create status = %d, body = %s", createResponse.Code, createResponse.Body.String())
	}
	created := decodeAppMemoryResponse[store.AppThreadMemory](t, createResponse)
	if created.UID != "u-1" || created.Text != "giao buổi sáng" || created.Pinned {
		t.Fatalf("manual create = %#v", created)
	}
	assertAppMemoryBodyOmits(t, createResponse, "U2-PRIVATE-MEMORY")

	updateResponse := serveAppMemoryJSON(t, a, http.MethodPut,
		"/memory/threads/group-a/"+strconv.FormatInt(created.ID, 10), map[string]any{
			"uid": "u-1", "memory_key": "preference.delivery", "text": "giao sau 9 giờ",
			"category": "preference", "pinned": true, "expected_revision": 1,
		})
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("manual update status = %d, body = %s", updateResponse.Code, updateResponse.Body.String())
	}
	updated := decodeAppMemoryResponse[store.AppThreadMemory](t, updateResponse)
	if updated.UID != "u-1" || updated.Text != "giao sau 9 giờ" || !updated.Pinned {
		t.Fatalf("manual update = %#v", updated)
	}

	crossUID := serveAppMemoryJSON(t, a, http.MethodPut,
		"/memory/threads/group-a/"+strconv.FormatInt(canary.ID, 10), map[string]any{
			"uid": "u-1", "memory_key": "identity.private", "text": "không được sửa",
			"category": "identity", "expected_revision": 2,
		})
	if crossUID.Code != http.StatusNotFound {
		t.Fatalf("cross-UID update status = %d, body = %s", crossUID.Code, crossUID.Body.String())
	}
	assertAppMemoryBodyOmits(t, crossUID, "U2-PRIVATE-MEMORY")

	deleteResponse := serveAppMemoryJSON(t, a, http.MethodDelete,
		"/memory/threads/group-a/"+strconv.FormatInt(created.ID, 10), map[string]any{
			"uid": "u-1", "expected_revision": 2,
		})
	if deleteResponse.Code != http.StatusNoContent || deleteResponse.Body.Len() != 0 {
		t.Fatalf("manual delete status/body = %d/%q", deleteResponse.Code, deleteResponse.Body.String())
	}
	readback := serveAppMemoryJSON(t, a, http.MethodGet, "/memory/threads/group-a?uid=u-1", nil)
	detail := decodeAppMemoryResponse[store.AppThreadMemoryDetail](t, readback)
	if detail.Revision != 3 || len(detail.Active) != 0 {
		t.Fatalf("delete readback = %#v", detail)
	}
	assertAppMemoryBodyOmits(t, readback, "U2-PRIVATE-MEMORY")
}

func TestMemoryV2ThreadHTTPStrictCommonMutationsRejectStaleZeroRevision(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	seedAppMemoryHTTPGroup(t, a, "group-a")
	createdResponse := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/group-a", map[string]any{
		"uid": "", "memory_key": "preference.group", "text": "quy tắc chung",
		"category": "preference", "expected_revision": 0,
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("common create status = %d, body = %s", createdResponse.Code, createdResponse.Body.String())
	}
	created := decodeAppMemoryResponse[store.AppThreadMemory](t, createdResponse)

	staleResponses := []*httptest.ResponseRecorder{
		serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/group-a", map[string]any{
			"uid": "", "memory_key": "preference.other", "text": "ghi chú cũ",
			"category": "preference", "expected_revision": 0,
		}),
		serveAppMemoryJSON(t, a, http.MethodPut,
			"/memory/threads/group-a/"+strconv.FormatInt(created.ID, 10), map[string]any{
				"uid": "", "memory_key": "preference.group", "text": "sửa bằng revision cũ",
				"category": "preference", "expected_revision": 0,
			}),
		serveAppMemoryJSON(t, a, http.MethodDelete,
			"/memory/threads/group-a/"+strconv.FormatInt(created.ID, 10), map[string]any{
				"uid": "", "expected_revision": 0,
			}),
	}
	for index, response := range staleResponses {
		if response.Code != http.StatusConflict {
			t.Fatalf("stale mutation %d status = %d, body = %s", index, response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "Memory đã thay đổi; hãy tải lại trước khi lưu") {
			t.Fatalf("stale mutation %d unsafe/unlocalized body = %q", index, response.Body.String())
		}
	}
	readback := serveAppMemoryJSON(t, a, http.MethodGet, "/memory/threads/group-a", nil)
	detail := decodeAppMemoryResponse[store.AppThreadMemoryDetail](t, readback)
	if detail.Revision != 1 || len(detail.Active) != 1 || detail.Active[0].Text != "quy tắc chung" {
		t.Fatalf("stale common mutations changed state: %#v", detail)
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

func TestMemoryV2HTTPMapsValidationNotFoundAndPinLimitWithoutLeaks(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	if err := a.st.UpsertZaloThread("thread-a", "Chị Lan"); err != nil {
		t.Fatal(err)
	}

	invalid := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread-a", map[string]any{"text": ""})
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
	badID := serveAppMemoryJSON(t, a, http.MethodPut, "/memory/threads/thread-a/not-a-number", map[string]any{
		"uid": "thread-a", "memory_key": "profile.note", "text": "hợp lệ",
		"category": "profile", "expected_revision": 0,
	})
	if badID.Code != http.StatusBadRequest {
		t.Fatalf("bad ID status = %d, body = %s", badID.Code, badID.Body.String())
	}
	missing := serveAppMemoryJSON(t, a, http.MethodDelete, "/memory/threads/thread-a/999", map[string]any{
		"uid": "thread-a", "expected_revision": 0,
	})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, body = %s", missing.Code, missing.Body.String())
	}
	for i := 1; i <= 12; i++ {
		response := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread-a", map[string]any{
			"uid": "thread-a", "memory_key": "profile.pin." + strconv.Itoa(i),
			"text": "ghim " + strconv.Itoa(i), "category": "profile", "pinned": true,
			"expected_revision": i - 1,
		})
		if response.Code != http.StatusCreated {
			t.Fatalf("pin %d status = %d, body = %s", i, response.Code, response.Body.String())
		}
	}
	conflict := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread-a", map[string]any{
		"uid": "thread-a", "memory_key": "profile.pin.13", "text": "ghim 13",
		"category": "profile", "pinned": true, "expected_revision": 12,
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

	semanticInvalid := []map[string]any{
		{
			"uid": "thread-a", "memory_key": "profile.note", "text": "hợp lệ",
			"category": "unsupported", "expected_revision": 12,
		},
		{
			"uid": strings.Repeat("u", 241), "memory_key": "profile.note", "text": "hợp lệ",
			"category": "profile", "expected_revision": 12,
		},
		{},
	}
	for index, body := range semanticInvalid {
		response := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/thread-a", body)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("semantic invalid %d status = %d, body = %s", index, response.Code, response.Body.String())
		}
	}
}

func TestMemoryV2HTTPRejectsOversizeMalformedTrailingAndUnknownJSON(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	if err := a.st.UpsertZaloThread("thread-a", "Chị Lan"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{name: "malformed", body: `{"text":`, wantStatus: http.StatusBadRequest},
		{name: "trailing document", body: `{"text":"hợp lệ"}{"pinned":true}`, wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"uid":"thread-a","text":"hợp lệ","surprise":true}`, wantStatus: http.StatusBadRequest},
		{name: "over 16 KiB", body: `{"uid":"thread-a","text":"` + strings.Repeat("x", appMemoryRequestLimit) + `"}`,
			wantStatus: http.StatusRequestEntityTooLarge},
		{name: "valid object then whitespace over 16 KiB",
			body:       `{"uid":"thread-a","text":"hợp lệ"}` + strings.Repeat(" ", appMemoryRequestLimit),
			wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/memory/threads/thread-a", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(portalHeader, "1")
			recorder := httptest.NewRecorder()
			mux := http.NewServeMux()
			a.registerAppRoutes(mux)
			mux.ServeHTTP(recorder, req)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, response = %s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
		})
	}
}

func TestMemoryV2HTTPMutationGuardCoversStrictCRUDAndLifecycleRoutes(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/memory/threads/group-a"},
		{method: http.MethodPut, path: "/memory/threads/group-a/1"},
		{method: http.MethodDelete, path: "/memory/threads/group-a/1"},
		{method: http.MethodPost, path: "/memory/threads/group-a/1/approve"},
		{method: http.MethodPost, path: "/memory/threads/group-a/1/reject"},
		{method: http.MethodPost, path: "/memory/threads/group-a/1/restore"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			mux := http.NewServeMux()
			a.registerAppRoutes(mux)
			mux.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestMemoryV2HTTPRejectsNonPositiveAndNonNumericIDs(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	tests := []struct {
		method string
		path   func(string) string
	}{
		{method: http.MethodPut, path: func(id string) string { return "/memory/threads/group-a/" + id }},
		{method: http.MethodDelete, path: func(id string) string { return "/memory/threads/group-a/" + id }},
		{method: http.MethodPost, path: func(id string) string { return "/memory/threads/group-a/" + id + "/approve" }},
		{method: http.MethodPost, path: func(id string) string { return "/memory/threads/group-a/" + id + "/reject" }},
		{method: http.MethodPost, path: func(id string) string { return "/memory/threads/group-a/" + id + "/restore" }},
	}
	for _, tt := range tests {
		for _, id := range []string{"0", "-1", "not-a-number"} {
			path := tt.path(id)
			t.Run(tt.method+" "+path, func(t *testing.T) {
				response := serveAppMemoryJSON(t, a, tt.method, path, map[string]any{
					"uid": "u-1", "memory_key": "profile.note", "text": "hợp lệ",
					"category": "profile", "expected_revision": 0,
				})
				if response.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400, body = %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestMemoryV2HTTPUsesServeMuxMethodAndRouteRejections(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	wrongMethod := serveAppMemoryJSON(t, a, http.MethodGet,
		"/memory/threads/group-a/1/approve", nil)
	if wrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status = %d, body = %s", wrongMethod.Code, wrongMethod.Body.String())
	}
	missingAction := serveAppMemoryJSON(t, a, http.MethodPost,
		"/memory/threads/group-a/1/archive", map[string]any{})
	if missingAction.Code != http.StatusNotFound {
		t.Fatalf("unsupported action status = %d, body = %s", missingAction.Code, missingAction.Body.String())
	}
}

func TestMemoryV2HTTPReturnsGenericSafeInternalError(t *testing.T) {
	a := newAppMemoryTestAPI(t)
	seedAppMemoryHTTPGroup(t, a, "group-a", "u-1")
	if err := a.st.Close(); err != nil {
		t.Fatal(err)
	}
	response := serveAppMemoryJSON(t, a, http.MethodGet,
		"/memory/threads/group-a?uid=u-1", nil)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("closed store status = %d, body = %s", response.Code, response.Body.String())
	}
	body := strings.ToLower(response.Body.String())
	if !strings.Contains(body, "không xử lý được memory") {
		t.Fatalf("generic 500 body = %q", response.Body.String())
	}
	for _, forbidden := range []string{"sqlite", "sql:", "database is closed", "select ", "group-a"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("generic 500 leaked %q in %q", forbidden, body)
		}
	}
}
