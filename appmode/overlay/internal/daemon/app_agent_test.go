package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentdc/internal/store"
)

func newAppAgentTestAPI(persona, roster string) *api {
	return &api{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		zalo: &zaloDeps{cfg: zaloConfig{
			PersonaPath: persona,
			RosterPath:  roster,
			Model:       "haiku",
			KBRoots:     []string{`D:\\knowledge`},
		}},
	}
}

func newAppAgentStoreTestAPI(t *testing.T, persona, roster string) *api {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "portal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := newAppAgentTestAPI(persona, roster)
	a.st = st
	return a
}

func appAgentRequest(t *testing.T, a *api, method, target string, body any) *httptest.ResponseRecorder {
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
	recorder := httptest.NewRecorder()
	switch {
	case target == "/agent" && method == http.MethodGet:
		a.handleAgentGet(recorder, req)
	case target == "/agent" && method == http.MethodPut:
		a.handleAgentPut(recorder, req)
	default:
		t.Fatalf("unsupported test request %s %s", method, target)
	}
	return recorder
}

func TestAppAgentReportsSortedPlaceholdersAndFillsThem(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "persona.md")
	original := "Xin chào {{TEN_BOT}}.\nĐơn vị {{CONG_TY}} gọi {{TEN_BOT}}.\n"
	if err := os.WriteFile(persona, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAppAgentTestAPI(persona, filepath.Join(dir, "roster.md"))

	got := appAgentRequest(t, a, http.MethodGet, "/agent", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("GET /agent status = %d, body = %s", got.Code, got.Body.String())
	}
	var state struct {
		Ready        bool          `json:"ready"`
		Placeholders []placeholder `json:"placeholders"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Ready {
		t.Fatal("GET /agent ready = true while placeholders remain")
	}
	if len(state.Placeholders) != 2 || state.Placeholders[0].Key != "CONG_TY" || state.Placeholders[1].Key != "TEN_BOT" {
		t.Fatalf("GET /agent placeholders = %#v; want sorted CONG_TY, TEN_BOT", state.Placeholders)
	}
	if state.Placeholders[1].Count != 2 {
		t.Fatalf("TEN_BOT count = %d; want 2", state.Placeholders[1].Count)
	}

	got = appAgentRequest(t, a, http.MethodPut, "/agent", map[string]any{
		"values": map[string]string{"CONG_TY": "Công ty Mở", "TEN_BOT": "An Nhiên"},
	})
	if got.Code != http.StatusOK {
		t.Fatalf("PUT /agent status = %d, body = %s", got.Code, got.Body.String())
	}
	written, err := os.ReadFile(persona)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(written), "{{TEN_BOT}}") || !strings.Contains(string(written), "An Nhiên") {
		t.Fatalf("persona after PUT = %q", written)
	}
	backup, err := os.ReadFile(persona + ".goc")
	if err != nil {
		t.Fatalf("read original backup: %v", err)
	}
	if string(backup) != original {
		t.Fatalf("original backup changed: got %q", backup)
	}
}

func TestAppAgentDetectsBroadMustachePlaceholders(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(persona, []byte("{{lower}} {{Tên bot}} {{with space}} {{has-dash}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAppAgentTestAPI(persona, filepath.Join(dir, "roster.md"))

	rr := appAgentRequest(t, a, http.MethodGet, "/agent", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /agent status = %d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeAppRouteJSON[struct {
		Ready        bool          `json:"ready"`
		Placeholders []placeholder `json:"placeholders"`
	}](t, rr.Body.Bytes())
	if got.Ready || len(got.Placeholders) != 4 {
		t.Fatalf("broad placeholder state = %+v", got)
	}
	want := []string{"Tên bot", "has-dash", "lower", "with space"}
	for i, key := range want {
		if got.Placeholders[i].Key != key {
			t.Fatalf("placeholder[%d] = %q; want %q", i, got.Placeholders[i].Key, key)
		}
	}
}

func TestAppAgentUnclosedMustacheIsNotReady(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(persona, []byte("Tên bot: {{TEN_BOT"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAppAgentTestAPI(persona, filepath.Join(dir, "roster.md"))

	rr := appAgentRequest(t, a, http.MethodGet, "/agent", nil)
	got := decodeAppRouteJSON[struct {
		Ready           bool   `json:"ready"`
		ValidationError string `json:"validation_error"`
	}](t, rr.Body.Bytes())
	if got.Ready || got.ValidationError == "" {
		t.Fatalf("unclosed mustache response = %+v", got)
	}
}

func TestAppAgentReadyRequiresValidStoredDisplayName(t *testing.T) {
	tests := []struct {
		name        string
		displayName string
		wantReady   bool
	}{
		{name: "missing name", wantReady: false},
		{name: "valid name", displayName: "An Nhiên", wantReady: true},
		{name: "newline name", displayName: "An\nNhiên", wantReady: false},
		{name: "delimiter name", displayName: "An {{Nhiên", wantReady: false},
		{name: "overlong name", displayName: strings.Repeat("ệ", 61), wantReady: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			persona := filepath.Join(dir, "persona.md")
			if err := os.WriteFile(persona, []byte("Persona hoàn chỉnh.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			a := newAppAgentStoreTestAPI(t, persona, filepath.Join(dir, "roster.md"))
			if tt.displayName != "" {
				if err := a.st.SetAgentDisplayName(tt.displayName); err != nil {
					t.Fatal(err)
				}
			}

			rr := appAgentRequest(t, a, http.MethodGet, "/agent", nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("GET /agent status = %d body=%s", rr.Code, rr.Body.String())
			}
			got := decodeAppRouteJSON[struct {
				Ready           bool   `json:"ready"`
				ValidationError string `json:"validation_error"`
			}](t, rr.Body.Bytes())
			if got.Ready != tt.wantReady {
				t.Fatalf("ready = %t; want %t; response=%s", got.Ready, tt.wantReady, rr.Body.String())
			}
			if !tt.wantReady && got.ValidationError == "" {
				t.Fatalf("invalid display name has no validation_error: %s", rr.Body.String())
			}
		})
	}
}

func TestAppAgentMultilineMustacheIsMalformedNotPlaceholder(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(persona, []byte("Tên {{foo\nbar}}.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAppAgentStoreTestAPI(t, persona, filepath.Join(dir, "roster.md"))
	if err := a.st.SetAgentDisplayName("An Nhiên"); err != nil {
		t.Fatal(err)
	}

	rr := appAgentRequest(t, a, http.MethodGet, "/agent", nil)
	got := decodeAppRouteJSON[struct {
		Ready           bool          `json:"ready"`
		Placeholders    []placeholder `json:"placeholders"`
		ValidationError string        `json:"validation_error"`
	}](t, rr.Body.Bytes())
	if got.Ready || len(got.Placeholders) != 0 || got.ValidationError == "" {
		t.Fatalf("multiline mustache response = %+v", got)
	}
}

func TestAppAgentRejectsInvalidValuesWithoutWrites(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: "  "},
		{name: "newline", value: "An\nNhiên"},
		{name: "opening delimiter", value: "An {{ Nhiên"},
		{name: "closing delimiter", value: "An }} Nhiên"},
		{name: "over 60 Unicode code points", value: strings.Repeat("ệ", 61)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			persona := filepath.Join(dir, "persona.md")
			original := []byte("Tên bot: {{TEN_BOT}}\n")
			if err := os.WriteFile(persona, original, 0o600); err != nil {
				t.Fatal(err)
			}
			a := newAppAgentStoreTestAPI(t, persona, filepath.Join(dir, "roster.md"))

			rr := appAgentRequest(t, a, http.MethodPut, "/agent", map[string]any{
				"values": map[string]string{"TEN_BOT": tt.value},
			})
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s; want 400", rr.Code, rr.Body.String())
			}
			if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
				t.Fatalf("invalid value changed persona to %q (%v)", got, err)
			}
			if _, err := os.Stat(persona + ".goc"); !os.IsNotExist(err) {
				t.Fatalf("invalid value created backup: %v", err)
			}
			if name, err := a.st.AgentDisplayName(); err != nil || name != "" {
				t.Fatalf("invalid value changed display name to %q (%v)", name, err)
			}
		})
	}
}

func TestAppAgentTENBOTIsAuthoritativeDisplayName(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(persona, []byte("Tên bot: {{TEN_BOT}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAppAgentStoreTestAPI(t, persona, filepath.Join(dir, "roster.md"))

	rr := appAgentRequest(t, a, http.MethodPut, "/agent", map[string]any{
		"values": map[string]string{"TEN_BOT": "  An Nhiên  "},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT /agent status = %d body=%s", rr.Code, rr.Body.String())
	}
	if name, err := a.st.AgentDisplayName(); err != nil || name != "An Nhiên" {
		t.Fatalf("AgentDisplayName() = %q, %v", name, err)
	}
	get := appAgentRequest(t, a, http.MethodGet, "/agent", nil)
	state := decodeAppRouteJSON[struct {
		DisplayName string `json:"display_name"`
	}](t, get.Body.Bytes())
	if state.DisplayName != "An Nhiên" {
		t.Fatalf("GET /agent display_name = %q", state.DisplayName)
	}
}

func TestAppAgentRejectsConflictingDisplayName(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "persona.md")
	original := []byte("Tên bot: {{TEN_BOT}}\n")
	if err := os.WriteFile(persona, original, 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAppAgentStoreTestAPI(t, persona, filepath.Join(dir, "roster.md"))

	rr := appAgentRequest(t, a, http.MethodPut, "/agent", map[string]any{
		"values":       map[string]string{"TEN_BOT": "An Nhiên"},
		"display_name": "Tên khác",
	})
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s; want 422", rr.Code, rr.Body.String())
	}
	if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
		t.Fatalf("conflict changed persona to %q (%v)", got, err)
	}
}

func TestAppAgentRejectsInvalidDisplayNamesWithoutWrites(t *testing.T) {
	tests := []string{"", "An\nNhiên", "An {{ Nhiên", "An }} Nhiên", strings.Repeat("ệ", 61)}
	for _, displayName := range tests {
		t.Run(fmt.Sprintf("%q", displayName), func(t *testing.T) {
			dir := t.TempDir()
			persona := filepath.Join(dir, "persona.md")
			original := []byte("Chưa có tên bot trong mẫu\n")
			if err := os.WriteFile(persona, original, 0o600); err != nil {
				t.Fatal(err)
			}
			a := newAppAgentStoreTestAPI(t, persona, filepath.Join(dir, "roster.md"))

			rr := appAgentRequest(t, a, http.MethodPut, "/agent", map[string]any{
				"values":              map[string]string{},
				"display_name":        displayName,
				"require_complete":    true,
				"onboarding_revision": 1,
			})
			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d body=%s; want 422", rr.Code, rr.Body.String())
			}
			if got, err := os.ReadFile(persona); err != nil || !bytes.Equal(got, original) {
				t.Fatalf("invalid display name changed persona to %q (%v)", got, err)
			}
		})
	}
}

func TestAppAgentAtomicReplacementLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(path, []byte("cũ"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAppFileAtomic(path, []byte("mới — UTF-8"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mới — UTF-8" {
		t.Fatalf("atomic replacement = %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "persona.md" {
		t.Fatalf("atomic replacement left files: %#v", entries)
	}
}

func TestAppAgentFailedReplacementPreservesOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(path, []byte("bản đang dùng"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("replace failed")
	err := writeAppFileAtomicWith(path, []byte("bản chưa hoàn tất"), 0o600, func(_, _ string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("atomic replacement error = %v, want %v", err, wantErr)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bản đang dùng" {
		t.Fatalf("failed replacement changed original to %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "persona.md" {
		t.Fatalf("failed replacement left files: %#v", entries)
	}
}

func TestAppAgentBackupIsPublishedOnlyAfterCompleteWrite(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "persona.md.goc")
	wantErr := errors.New("publish failed")
	err := writeAppBackupOnceWith(backup, []byte("bản gốc đầy đủ"), 0o600, func(_, _ string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("backup publication error = %v, want %v", err, wantErr)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("failed publication exposed final backup: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed backup publication left files: %#v", entries)
	}

	if err := writeAppBackupOnce(backup, []byte("bản gốc đầy đủ"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAppBackupOnce(backup, []byte("không được ghi đè"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "bản gốc đầy đủ" {
		t.Fatalf("existing backup was replaced with %q", got)
	}
}

func TestAppPersonaWritesUTF8WithoutBOMAndRefreshesReadiness(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "persona.md")
	original := "Tên bot: {{TEN_BOT}}\n"
	if err := os.WriteFile(persona, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAppAgentStoreTestAPI(t, persona, filepath.Join(dir, "roster.md"))
	if err := a.st.SetAgentDisplayName("An Nhiên"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPut, "/agent/persona/persona", bytes.NewBufferString(
		`{"text":"Tên bot: An Nhiên\r\nGiọng nói: thân thiện — rõ ràng\r\n"}`,
	))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", "persona")
	recorder := httptest.NewRecorder()
	a.handlePersonaPut(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT persona status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var state struct {
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.Ready {
		t.Fatal("persona response ready = false after the last placeholder was removed")
	}
	written, err := os.ReadFile(persona)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(written, []byte{0xef, 0xbb, 0xbf}) {
		t.Fatal("persona contains a UTF-8 BOM")
	}
	if bytes.Contains(written, []byte("\r\n")) || !strings.Contains(string(written), "thân thiện — rõ ràng") {
		t.Fatalf("persona content = %q", written)
	}
	backup, err := os.ReadFile(persona + ".goc")
	if err != nil {
		t.Fatalf("read persona backup: %v", err)
	}
	if string(backup) != original {
		t.Fatalf("persona backup = %q; want original %q", backup, original)
	}
}

func TestAppPersonaDoesNotCreateMissingPersonaWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	persona := filepath.Join(dir, "missing-persona.md")
	a := newAppAgentTestAPI(persona, filepath.Join(dir, "roster.md"))
	req := httptest.NewRequest(http.MethodPut, "/agent/persona/persona", strings.NewReader(`{"text":"Giọng mới"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", "persona")
	recorder := httptest.NewRecorder()

	a.handlePersonaPut(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("missing persona PUT status = %d, want 500; body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(persona); !os.IsNotExist(err) {
		t.Fatalf("missing persona was created without a backup: %v", err)
	}
}

func TestAppPersonaGetRejectsMissingPersonaButAllowsMissingRoster(t *testing.T) {
	dir := t.TempDir()
	a := newAppAgentTestAPI(filepath.Join(dir, "persona.md"), filepath.Join(dir, "roster.md"))

	personaReq := httptest.NewRequest(http.MethodGet, "/agent/persona/persona", nil)
	personaReq.SetPathValue("name", "persona")
	personaResponse := httptest.NewRecorder()
	a.handlePersonaGet(personaResponse, personaReq)
	if personaResponse.Code != http.StatusInternalServerError {
		t.Fatalf("missing persona GET status = %d, want 500; body = %s", personaResponse.Code, personaResponse.Body.String())
	}

	rosterReq := httptest.NewRequest(http.MethodGet, "/agent/persona/roster", nil)
	rosterReq.SetPathValue("name", "roster")
	rosterResponse := httptest.NewRecorder()
	a.handlePersonaGet(rosterResponse, rosterReq)
	if rosterResponse.Code != http.StatusOK {
		t.Fatalf("missing roster GET status = %d, want 200; body = %s", rosterResponse.Code, rosterResponse.Body.String())
	}
}
