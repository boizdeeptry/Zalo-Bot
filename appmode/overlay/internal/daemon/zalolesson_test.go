package daemon

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentdc/internal/config"
	"agentdc/internal/ipc"
	"agentdc/internal/store"
)

// lessonSetup dựng một hội thoại có sẵn một tin của BOT.
func lessonSetup(t *testing.T, body string) (*api, string) {
	t.Helper()
	_, cfg, reg := newTestServer(t, nil)
	a := &api{cfg: cfg, st: reg.st, reg: reg, logger: slog.New(slog.DiscardHandler), portal: newPortalStore()}
	if err := reg.st.UpsertZaloThread("t1", "Khách"); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := reg.st.AddZaloMessage(ipc.ZaloMessage{
			ThreadID: "t1", Direction: ipc.ZaloOut, Body: body, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return a, "t1"
}

// TestOperatorRewriteBecomesStructuredGlobalLesson ghim PHẦN B của vòng trực Bé Mi.
//
// Operator rewrites are reusable response-quality lessons, not customer facts. They must be
// Portal-visible and advance only the global lesson revision so every resident CLI session sees
// a refresh on its next turn.
func TestOperatorRewriteBecomesStructuredGlobalLesson(t *testing.T) {
	a, tid := lessonSetup(t, "Dạ sản phẩm này chắc chắn giúp con cao thêm 5cm ạ")
	before, err := a.st.AppMemoryRevisions(tid)
	if err != nil {
		t.Fatal(err)
	}

	a.noteOperatorRewrite(tid, "Dạ em không hứa số cm cụ thể được ạ, tuỳ thể trạng từng bé")

	mem, err := a.st.ZaloMemory(tid, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem) != 0 {
		t.Fatalf("legacy Memory rows = %+v; want 0 because an operator rewrite is not a customer fact", mem)
	}
	lessons, err := a.st.AppLessons("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons.Lessons) != 1 {
		t.Fatalf("structured lessons = %+v; want exactly one", lessons.Lessons)
	}
	lesson := lessons.Lessons[0]
	if lesson.ThreadID != tid ||
		lesson.BotText != "Dạ sản phẩm này chắc chắn giúp con cao thêm 5cm ạ" ||
		lesson.Better != "Dạ em không hứa số cm cụ thể được ạ, tuỳ thể trạng từng bé" ||
		!strings.Contains(lesson.Note, "người trực sửa lại") {
		t.Fatalf("structured lesson = %+v; want thread, clipped bot/better text and rewrite note", lesson)
	}
	after, err := a.st.AppMemoryRevisions(tid)
	if err != nil {
		t.Fatal(err)
	}
	if after.Memory != before.Memory || after.Lessons != before.Lessons+1 ||
		lessons.Revision != after.Lessons {
		t.Fatalf("revisions before=%+v after=%+v list=%d; want lessons +1 only",
			before, after, lessons.Revision)
	}
}

func TestOperatorRewriteStructuredFieldsRemainTrimmedAndBounded(t *testing.T) {
	a, tid := lessonSetup(t, "  "+strings.Repeat("b", maxLessonPart+20)+"  ")
	a.noteOperatorRewrite(tid, "  "+strings.Repeat("o", maxLessonPart+20)+"  ")

	lessons, err := a.st.AppLessons("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons.Lessons) != 1 {
		t.Fatalf("structured lessons = %+v; want exactly one", lessons.Lessons)
	}
	lesson := lessons.Lessons[0]
	if got := len([]rune(lesson.BotText)); got != maxLessonPart+1 ||
		!strings.HasSuffix(lesson.BotText, "…") {
		t.Errorf("bot_text = %q (%d runes); want current %d-byte clip plus ellipsis",
			lesson.BotText, got, maxLessonPart)
	}
	if got := len([]rune(lesson.Better)); got != maxLessonPart+1 ||
		!strings.HasSuffix(lesson.Better, "…") {
		t.Errorf("better = %q (%d runes); want current %d-byte clip plus ellipsis",
			lesson.Better, got, maxLessonPart)
	}
	if lesson.BotText != strings.TrimSpace(lesson.BotText) ||
		lesson.Better != strings.TrimSpace(lesson.Better) {
		t.Fatalf("structured lesson retained outer whitespace: %+v", lesson)
	}
}

// TestNoLessonWhenOperatorSpeaksTwice: người trực gõ hai tin liền không phải "sửa chính mình".
func TestNoLessonWhenOperatorSpeaksTwice(t *testing.T) {
	a, tid := lessonSetup(t, "")
	if err := a.st.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: tid, Direction: ipc.ZaloOut, Author: ipc.ZaloAuthorOperator,
		Body: "Dạ chị chờ em chút", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	a.noteOperatorRewrite(tid, "Dạ em xem xong rồi ạ")

	lessons, err := a.st.AppLessons("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons.Lessons) != 0 {
		t.Errorf("ghi %d bài học; muốn 0 — nói tiếp lượt của mình không phải sửa ai", len(lessons.Lessons))
	}
}

// TestNoLessonForAnOldBotMessage: một câu tay của hôm sau không phải sửa câu của hôm trước.
//
// Hỏng theo hướng "bỏ sót" thì mất một dòng. Hỏng theo hướng ngược lại thì prompt mang một bài học
// BỊA, và nó lái mọi câu trả lời sau đó.
func TestNoLessonForAnOldBotMessage(t *testing.T) {
	_, cfg, reg := newTestServer(t, nil)
	a := &api{cfg: cfg, st: reg.st, reg: reg, logger: slog.New(slog.DiscardHandler), portal: newPortalStore()}
	if err := reg.st.UpsertZaloThread("t1", "Khách"); err != nil {
		t.Fatal(err)
	}
	if err := reg.st.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: "t1", Direction: ipc.ZaloOut, Body: "câu của hôm qua",
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	a.noteOperatorRewrite("t1", "câu của hôm nay")

	lessons, err := a.st.AppLessons("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons.Lessons) != 0 {
		t.Errorf("ghi %d bài học cho một tin 24 giờ trước; muốn 0", len(lessons.Lessons))
	}
}

// TestNoLessonWhenTheTextIsTheSame: gửi lại đúng câu đó không phải một bài học.
func TestNoLessonWhenTheTextIsTheSame(t *testing.T) {
	a, tid := lessonSetup(t, "Dạ em chào anh ạ")
	// Khác dấu câu và hoa thường; vẫn là cùng một câu.
	a.noteOperatorRewrite(tid, "dạ em chào anh ạ!")

	lessons, err := a.st.AppLessons("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons.Lessons) != 0 {
		t.Errorf("ghi %d bài học; muốn 0 — một khác biệt dấu câu không phải bài học", len(lessons.Lessons))
	}
}

// TestReplyEndpointRecordsTheLessonAndQueuesNextTurnRefresh đi qua ĐÚNG endpoint portal gọi.
//
// The session deliberately starts at the old lesson revision. The endpoint mutation must leave it
// stale, and the following structured CLI turn must receive the authoritative global refresh.
func TestReplyEndpointRecordsTheLessonAndQueuesNextTurnRefresh(t *testing.T) {
	ts, cfg, reg := newTestServer(t, nil)
	if err := reg.st.UpsertZaloThread("t1", "Khách"); err != nil {
		t.Fatal(err)
	}
	if err := reg.st.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: "t1", Direction: ipc.ZaloOut, Body: "câu bot nói hơi cứng", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	zaloCfg := appZaloHookConfig(t)
	initial := appZaloCreateHookSession(t, reg.st, zaloCfg, "t1", appZaloTestSessionID)

	res := authedReq(t, ts, cfg.Token, http.MethodPost, "/zalo/threads/t1/reply",
		ipc.ZaloReplyRequest{Body: "Dạ em nói lại cho mềm hơn ạ"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST reply = %d; muốn 200", res.StatusCode)
	}

	lessons, err := reg.st.AppLessons("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons.Lessons) != 1 || lessons.Revision != initial.LessonsRevision+1 {
		t.Fatalf("lessons=%+v revision=%d session=%+v; want one pending global refresh",
			lessons.Lessons, lessons.Revision, initial)
	}
	mem, err := reg.st.ZaloMemory("t1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem) != 0 {
		t.Fatalf("endpoint wrote legacy Memory rows: %+v", mem)
	}
	stale, err := reg.st.ZaloCLISession("t1")
	if err != nil {
		t.Fatal(err)
	}
	if stale.LessonsRevision != initial.LessonsRevision || stale.LessonsRevision >= lessons.Revision {
		t.Fatalf("session revision=%d lesson revision=%d; want next-turn refresh pending",
			stale.LessonsRevision, lessons.Revision)
	}

	a := appZaloAPIWithStore(cfg, reg.st)
	run := &appZaloHookRunner{results: []appZaloRunResult{{Answer: "answer"}}}
	if _, err := a.appRunZalo(context.Background(), run, zaloCfg, "t1", "câu kế tiếp", "", nil, nil, nil, func(string) {}); err != nil {
		t.Fatal(err)
	}
	inputs, legacyCalls := run.snapshot()
	if len(inputs) != 1 || legacyCalls != 0 || !inputs[0].Resume ||
		!strings.Contains(inputs[0].Prompt, `"scope":"global_lessons"`) ||
		!strings.Contains(inputs[0].Prompt, "câu bot nói hơi cứng") ||
		!strings.Contains(inputs[0].Prompt, "Dạ em nói lại cho mềm hơn ạ") {
		t.Fatalf("next turn did not receive structured lesson refresh: inputs=%+v legacy=%d",
			inputs, legacyCalls)
	}
}

// TestReplyEndpointKeepsSendingWhenLessonInsertFails preserves the best-effort contract: the
// operator reply is primary and must still return success when lesson storage is unavailable.
func TestReplyEndpointKeepsSendingWhenLessonInsertFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lesson-failure.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.Config{Dir: t.TempDir(), Port: 0, Token: testToken}
	logger := slog.New(slog.DiscardHandler)
	reg := NewRegistry(st, cfg, logger)
	t.Cleanup(reg.StopAll)
	srv := New(context.Background(), cfg, st, reg, nil, logger, "test", nil)
	ts := httptest.NewServer(srv.Handler)
	t.Cleanup(ts.Close)
	if err := st.UpsertZaloThread("t1", "Khách"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: "t1", Direction: ipc.ZaloOut, Body: "câu bot cần sửa", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	rawDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rawDB.Close() })
	if _, err := rawDB.Exec(`CREATE TRIGGER app_test_fail_operator_lesson
BEFORE INSERT ON zalo_lessons BEGIN SELECT RAISE(ABORT, 'lesson write failed'); END`); err != nil {
		t.Fatal(err)
	}

	res := authedReq(t, ts, cfg.Token, http.MethodPost, "/zalo/threads/t1/reply",
		ipc.ZaloReplyRequest{Body: "Dạ người trực vẫn gửi câu trả lời"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST reply with failed lesson insert = %d; want 200", res.StatusCode)
	}
	lessons, err := st.AppLessons("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lessons.Lessons) != 0 || lessons.Revision != 0 {
		t.Fatalf("failed insert left lesson state: %+v", lessons)
	}
	mem, err := st.ZaloMemory("t1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mem) != 0 {
		t.Fatalf("failed lesson insert fell back to legacy Memory: %+v", mem)
	}
}
