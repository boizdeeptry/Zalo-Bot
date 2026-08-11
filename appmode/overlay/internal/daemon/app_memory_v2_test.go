package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentdc/internal/store"
)

type appMemoryCodedTestError struct {
	secret string
	code   int
}

func (err appMemoryCodedTestError) Error() string { return err.secret }
func (err appMemoryCodedTestError) Code() int     { return err.code }

func TestMemoryV2HTTPOpaqueScopesNeverAliasCanonicalIdentities(t *testing.T) {
	newScopedAPI := func(t *testing.T) *api {
		t.Helper()
		a := newAppMemoryTestAPI(t)
		seedAppMemoryHTTPGroup(t, a, "group-a", "u-1")
		if _, err := a.st.CreateAppThreadMemoryV2("group-a", store.AppThreadMemoryInput{
			UID: "u-1", MemoryKey: "identity.private", Text: "U1-PRIVATE-MEMORY",
			Category: "identity", ExpectedRevision: 0,
		}); err != nil {
			t.Fatal(err)
		}
		return a
	}

	t.Run("decorated query UID cannot read canonical member", func(t *testing.T) {
		a := newScopedAPI(t)
		response := serveAppMemoryJSON(t, a, http.MethodGet,
			"/memory/threads/group-a?uid=%20u-1%20", nil)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body = %s", response.Code, response.Body.String())
		}
		assertAppMemoryBodyOmits(t, response, "U1-PRIVATE-MEMORY")
	})

	t.Run("decorated body UID cannot mutate canonical member", func(t *testing.T) {
		a := newScopedAPI(t)
		response := serveAppMemoryJSON(t, a, http.MethodPost, "/memory/threads/group-a", map[string]any{
			"uid": " u-1 ", "memory_key": "profile.decorated", "text": "không được thêm",
			"category": "profile", "expected_revision": 1,
		})
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body = %s", response.Code, response.Body.String())
		}
		assertAppMemoryBodyOmits(t, response, "U1-PRIVATE-MEMORY")
		detail, err := a.st.AppThreadMemoryScope("group-a", "u-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if detail.Revision != 1 || len(detail.Active) != 1 ||
			detail.Active[0].Text != "U1-PRIVATE-MEMORY" {
			t.Fatalf("decorated UID mutated canonical scope: %#v", detail)
		}
	})

	t.Run("decorated thread path cannot read canonical thread", func(t *testing.T) {
		a := newScopedAPI(t)
		response := serveAppMemoryJSON(t, a, http.MethodGet,
			"/memory/threads/%20group-a%20?uid=u-1", nil)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body = %s", response.Code, response.Body.String())
		}
		assertAppMemoryBodyOmits(t, response, "U1-PRIVATE-MEMORY")
	})

	t.Run("decorated thread path cannot mutate canonical thread", func(t *testing.T) {
		a := newScopedAPI(t)
		response := serveAppMemoryJSON(t, a, http.MethodPost,
			"/memory/threads/%20group-a%20", map[string]any{
				"uid": "u-1", "memory_key": "profile.decorated", "text": "không được thêm",
				"category": "profile", "expected_revision": 1,
			})
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422, body = %s", response.Code, response.Body.String())
		}
		assertAppMemoryBodyOmits(t, response, "U1-PRIVATE-MEMORY")
		detail, err := a.st.AppThreadMemoryScope("group-a", "u-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if detail.Revision != 1 || len(detail.Active) != 1 ||
			detail.Active[0].Text != "U1-PRIVATE-MEMORY" {
			t.Fatalf("decorated thread mutated canonical scope: %#v", detail)
		}
	})

	t.Run("canonical IDs still work", func(t *testing.T) {
		a := newScopedAPI(t)
		response := serveAppMemoryJSON(t, a, http.MethodGet,
			"/memory/threads/group-a?uid=u-1", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body = %s", response.Code, response.Body.String())
		}
		detail := decodeAppMemoryResponse[store.AppThreadMemoryDetail](t, response)
		if detail.SelectedUID != "u-1" || len(detail.Active) != 1 ||
			detail.Active[0].Text != "U1-PRIVATE-MEMORY" {
			t.Fatalf("canonical detail = %#v", detail)
		}
	})
}

func TestMemoryV2HTTPInternalErrorLogsOnlySafeRootDiagnostic(t *testing.T) {
	const secret = "U2-PRIVATE-MEMORY SQL SECRET"
	var logOutput bytes.Buffer
	a := &api{logger: slog.New(slog.NewJSONHandler(&logOutput, nil))}
	response := httptest.NewRecorder()
	a.writeAppMemoryError(response, fmt.Errorf("WRAPPER-SECRET: %w", appMemoryCodedTestError{
		secret: secret,
		code:   26,
	}))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "không xử lý được memory") ||
		strings.Contains(response.Body.String(), secret) ||
		strings.Contains(response.Body.String(), "WRAPPER-SECRET") {
		t.Fatalf("unsafe internal error body = %q", response.Body.String())
	}

	var entry map[string]any
	if err := json.Unmarshal(logOutput.Bytes(), &entry); err != nil {
		t.Fatalf("decode log %q: %v", logOutput.String(), err)
	}
	if entry["error_code"] != "memory_internal_error" {
		t.Fatalf("safe error_code = %#v, log = %s", entry["error_code"], logOutput.String())
	}
	errorType, _ := entry["error_type"].(string)
	if !strings.Contains(errorType, "appMemoryCodedTestError") {
		t.Fatalf("root error_type = %#v, log = %s", entry["error_type"], logOutput.String())
	}
	if entry["cause_code"] != float64(26) {
		t.Fatalf("cause_code = %#v, log = %s", entry["cause_code"], logOutput.String())
	}
	if strings.Contains(logOutput.String(), secret) || strings.Contains(logOutput.String(), "WRAPPER-SECRET") {
		t.Fatalf("internal log leaked arbitrary error text: %s", logOutput.String())
	}
}

func TestMemoryV2HTTPLifecycleCannotAccessAnotherUIDRows(t *testing.T) {
	const (
		pendingCanary = "U2-PRIVATE-PENDING"
		expiredCanary = "U2-PRIVATE-EXPIRED"
	)
	a := newAppMemoryTestAPI(t)
	seedAppMemoryHTTPGroup(t, a, "group-a", "u-1", "u-2")
	now := time.Now()
	applyAppMemoryHTTPProposal(t, a, "group-a", "u-2", "health.private",
		pendingCanary, "health", 1, now)
	applyAppMemoryHTTPProposal(t, a, "group-a", "u-2", "interest.private",
		expiredCanary, "interest", 1, now.Add(-31*24*time.Hour))
	u2Detail, err := a.st.AppThreadMemoryScope("group-a", "u-2", now)
	if err != nil {
		t.Fatal(err)
	}
	pending := findAppMemoryHTTPRow(t, u2Detail.Pending, "health.private")
	expired := findAppMemoryHTTPRow(t, u2Detail.Expired, "interest.private")

	tests := []struct {
		name string
		path string
		body map[string]any
	}{
		{
			name: "approve",
			path: "/memory/threads/group-a/" + strconv.FormatInt(pending.ID, 10) + "/approve",
			body: map[string]any{
				"uid": "u-1", "memory_key": "health.private", "text": "không được duyệt",
				"category": "health", "expected_revision": 0,
			},
		},
		{
			name: "reject",
			path: "/memory/threads/group-a/" + strconv.FormatInt(pending.ID, 10) + "/reject",
			body: map[string]any{"uid": "u-1", "expected_revision": 0},
		},
		{
			name: "restore",
			path: "/memory/threads/group-a/" + strconv.FormatInt(expired.ID, 10) + "/restore",
			body: map[string]any{"uid": "u-1", "expected_revision": 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := serveAppMemoryJSON(t, a, http.MethodPost, tt.path, tt.body)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404, body = %s", response.Code, response.Body.String())
			}
			assertAppMemoryBodyOmits(t, response, pendingCanary)
			assertAppMemoryBodyOmits(t, response, expiredCanary)
		})
	}

	u2Detail, err = a.st.AppThreadMemoryScope("group-a", "u-2", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := findAppMemoryHTTPRow(t, u2Detail.Pending, "health.private"); got.ID != pending.ID ||
		got.Text != pendingCanary {
		t.Fatalf("cross-UID lifecycle changed pending row: %#v", got)
	}
	if got := findAppMemoryHTTPRow(t, u2Detail.Expired, "interest.private"); got.ID != expired.ID ||
		got.Text != expiredCanary {
		t.Fatalf("cross-UID lifecycle changed expired row: %#v", got)
	}
}
