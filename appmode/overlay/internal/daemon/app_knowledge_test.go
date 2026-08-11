package daemon

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentdc/internal/config"
	"agentdc/internal/store"
)

func TestRunIngestCancelsHangingNPMDiscovery(t *testing.T) {
	fixture := newHangingNPMDiscovery(t)
	previous := ingest
	ingest = &ingestState{running: true}
	t.Cleanup(func() { ingest = previous })

	ctx, cancel := context.WithCancel(t.Context())
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	a := newAppKnowledgeTestAPI(t)
	a.st = st
	dir := t.TempDir()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runIngest(ctx, func() {}, dir)
	}()
	fixture.waitStarted(t)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		fixture.release()
		<-done
		t.Fatal("runIngest did not cancel while npm discovery was hanging")
	}
	if got := ingest.snapshot()["err"]; got != "đã huỷ" {
		t.Fatalf("ingest error = %q; want cancellation message", got)
	}
}

func newAppKnowledgeTestAPI(t *testing.T) *api {
	t.Helper()
	return &api{
		cfg:    config.Config{Dir: t.TempDir()},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func appKnowledgeBrain(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"raw", "wiki"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(envBrainDir, dir)
	return dir
}

func appUploadRequest(t *testing.T, filename, contents string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/kb/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestAppKnowledgeUploadIsExclusiveAndAllowlisted(t *testing.T) {
	brain := appKnowledgeBrain(t)
	a := newAppKnowledgeTestAPI(t)

	first := httptest.NewRecorder()
	a.handleKBUpload(first, appUploadRequest(t, "hướng-dẫn.md", "bản đầu"))
	if first.Code != http.StatusOK {
		t.Fatalf("safe upload status = %d, body = %s", first.Code, first.Body.String())
	}
	written, err := os.ReadFile(filepath.Join(brain, "raw", "hướng-dẫn.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "bản đầu" {
		t.Fatalf("safe upload content = %q", written)
	}

	duplicate := httptest.NewRecorder()
	a.handleKBUpload(duplicate, appUploadRequest(t, "hướng-dẫn.md", "đè lên"))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate upload status = %d, want 409; body = %s", duplicate.Code, duplicate.Body.String())
	}
	written, err = os.ReadFile(filepath.Join(brain, "raw", "hướng-dẫn.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != "bản đầu" {
		t.Fatalf("duplicate upload overwrote raw file: %q", written)
	}

	rejected := httptest.NewRecorder()
	a.handleKBUpload(rejected, appUploadRequest(t, "installer.exe", "MZ"))
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf(".exe upload status = %d, want 400; body = %s", rejected.Code, rejected.Body.String())
	}
	if _, err := os.Stat(filepath.Join(brain, "raw", "installer.exe")); !os.IsNotExist(err) {
		t.Fatalf("rejected executable exists in raw: %v", err)
	}

	for _, unsafeName := range []string{"installer.exe:payload.md", "CON.md", "trailing.md."} {
		rejected = httptest.NewRecorder()
		a.handleKBUpload(rejected, appUploadRequest(t, unsafeName, "hidden"))
		if rejected.Code != http.StatusBadRequest {
			t.Errorf("unsafe Windows filename %q status = %d, want 400; body = %s", unsafeName, rejected.Code, rejected.Body.String())
		}
	}
}

func TestAppKnowledgeUploadRequestCapIs64MiB(t *testing.T) {
	if maxUploadTotal != 64<<20 {
		t.Fatalf("maxUploadTotal = %d, want 64 MiB", maxUploadTotal)
	}
}

func TestAppKnowledgeRejectsOversizedRequestWith413(t *testing.T) {
	brain := appKnowledgeBrain(t)
	a := newAppKnowledgeTestAPI(t)
	req := httptest.NewRequest(http.MethodPost, "/kb/upload", strings.NewReader("--boundary--"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	req.ContentLength = maxUploadTotal + 1
	recorder := httptest.NewRecorder()

	a.handleKBUpload(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status = %d, want 413; body = %s", recorder.Code, recorder.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(brain, "raw"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("oversized upload wrote files: %#v", entries)
	}
}

func TestAppKnowledgeRejectsConcurrentIngest(t *testing.T) {
	brain := appKnowledgeBrain(t)
	if err := os.WriteFile(filepath.Join(brain, "raw", "source.md"), []byte("nguồn"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newAppKnowledgeTestAPI(t)
	previous := ingest
	ingest = &ingestState{running: true}
	t.Cleanup(func() { ingest = previous })

	recorder := httptest.NewRecorder()
	a.handleKBIngest(recorder, httptest.NewRequest(http.MethodPost, "/kb/ingest", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("concurrent ingest status = %d, want 409; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestAppKnowledgeStopCancelsAndFinishCleansState(t *testing.T) {
	cancelled := false
	previous := ingest
	ingest = &ingestState{running: true, cancel: func() { cancelled = true }}
	t.Cleanup(func() { ingest = previous })
	a := newAppKnowledgeTestAPI(t)
	recorder := httptest.NewRecorder()

	a.handleKBIngestStop(recorder, httptest.NewRequest(http.MethodDelete, "/kb/ingest", nil))

	if recorder.Code != http.StatusNoContent || !cancelled {
		t.Fatalf("stop status = %d, cancelled = %v", recorder.Code, cancelled)
	}
	ingest.finish("agent failed")
	if ingest.running || !ingest.done || ingest.cancel != nil || ingest.err != "agent failed" {
		t.Fatalf("finished ingest state = %#v", ingest)
	}
}

func TestAppModelRejectsValuesOutsideAllowlist(t *testing.T) {
	a := newAppKnowledgeTestAPI(t)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/kb/model", strings.NewReader(`{"model":"sonnet; Remove-Item"}`))
	req.Header.Set("Content-Type", "application/json")
	a.handleKBModelPut(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid model status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(modelFile(a.cfg.Dir)); !os.IsNotExist(err) {
		t.Fatalf("invalid model created config file: %v", err)
	}
}
