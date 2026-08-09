package store

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"agentdc/internal/ipc"
)

func seedAppMemoryThread(t *testing.T, s *Store, id, name string) {
	t.Helper()
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: id, Name: name, ThreadType: ipc.ZaloThreadUser,
	}); err != nil {
		t.Fatalf("UpsertZaloThreadInfo(%s): %v", id, err)
	}
}

func seedAppMemoryInbound(t *testing.T, s *Store, threadID, body string) int64 {
	t.Helper()
	if err := s.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: threadID, Direction: ipc.ZaloIn, Author: "Khách",
		Body: body, CreatedAt: time.Date(2026, 8, 9, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("AddZaloMessage(%s): %v", threadID, err)
	}
	id, err := s.LatestZaloMessageID(threadID)
	if err != nil {
		t.Fatalf("LatestZaloMessageID(%s): %v", threadID, err)
	}
	return id
}

func TestAppThreadMemoryCRUDIsOwnedAndBumpsOnlyItsRevision(t *testing.T) {
	s := newStore(t)
	seedAppMemoryThread(t, s, "thread-a", "Chị Lan")
	seedAppMemoryThread(t, s, "thread-b", "Anh Minh")

	created, err := s.CreateAppThreadMemory("thread-a", AppThreadMemoryInput{
		Text: "  Chị Lan thích nhận hàng buổi sáng  ", Pinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Text != "Chị Lan thích nhận hàng buổi sáng" || created.Source != "operator" ||
		created.SourceMessageID != 0 || !created.Pinned {
		t.Fatalf("created memory = %#v", created)
	}
	revisions, err := s.AppMemoryRevisions("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if revisions.Memory != 1 || revisions.Lessons != 0 {
		t.Fatalf("thread-a revisions = %#v; want 1/0", revisions)
	}
	other, err := s.AppMemoryRevisions("thread-b")
	if err != nil {
		t.Fatal(err)
	}
	if other.Memory != 0 {
		t.Fatalf("thread-b memory revision = %d; want 0", other.Memory)
	}

	if _, err := s.UpdateAppThreadMemory("thread-b", created.ID, AppThreadMemoryInput{
		Text: "không được chạm", Pinned: false,
	}); !errors.Is(err, ErrAppMemoryNotFound) {
		t.Fatalf("cross-thread update = %v; want ErrAppMemoryNotFound", err)
	}
	updated, err := s.UpdateAppThreadMemory("thread-a", created.ID, AppThreadMemoryInput{
		Text: "Chị Lan nhận hàng sau 9 giờ", Pinned: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Text != "Chị Lan nhận hàng sau 9 giờ" || updated.Pinned {
		t.Fatalf("updated memory = %#v", updated)
	}
	if err := s.DeleteAppThreadMemory("thread-b", created.ID); !errors.Is(err, ErrAppMemoryNotFound) {
		t.Fatalf("cross-thread delete = %v; want ErrAppMemoryNotFound", err)
	}
	if err := s.DeleteAppThreadMemory("thread-a", created.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := s.AppThreadMemories("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Memories) != 0 || detail.Revision != 3 {
		t.Fatalf("detail after delete = %#v; want empty revision 3", detail)
	}
}

func TestAppMemoryValidationAndPinLimitsRollback(t *testing.T) {
	s := newStore(t)
	seedAppMemoryThread(t, s, "thread-a", "Chị Lan")

	for _, input := range []AppThreadMemoryInput{
		{Text: "   "},
		{Text: strings.Repeat("ạ", 241)},
	} {
		if _, err := s.CreateAppThreadMemory("thread-a", input); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("CreateAppThreadMemory(%d runes) = %v; want ErrAppMemoryInvalid", len([]rune(input.Text)), err)
		}
	}
	for i := 1; i <= 12; i++ {
		if _, err := s.CreateAppThreadMemory("thread-a", AppThreadMemoryInput{
			Text: fmt.Sprintf("ghim %02d", i), Pinned: true,
		}); err != nil {
			t.Fatalf("create pinned memory %d: %v", i, err)
		}
	}
	if _, err := s.CreateAppThreadMemory("thread-a", AppThreadMemoryInput{
		Text: "ghim 13", Pinned: true,
	}); !errors.Is(err, ErrAppMemoryPinLimit) {
		t.Fatalf("thirteenth pin = %v; want ErrAppMemoryPinLimit", err)
	}
	detail, err := s.AppThreadMemories("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Memories) != 12 || detail.Revision != 12 {
		t.Fatalf("rejected pin changed state: rows=%d revision=%d", len(detail.Memories), detail.Revision)
	}
}

func TestAppMemoryOverviewIncludesEmptyThreadsAndSearchesServerSide(t *testing.T) {
	s := newStore(t)
	seedAppMemoryThread(t, s, "thread-a", "Chị Lan")
	seedAppMemoryThread(t, s, "thread-b", "Anh Minh")
	if _, err := s.CreateAppThreadMemory("thread-a", AppThreadMemoryInput{
		Text: "quan tâm giao hàng lạnh", Pinned: true,
	}); err != nil {
		t.Fatal(err)
	}

	overview, err := s.AppMemoryOverview("", false)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Metrics.Memories != 1 || overview.Metrics.Threads != 1 || len(overview.Threads) != 2 {
		t.Fatalf("overview = %#v", overview)
	}
	if overview.Threads[0].ID != "thread-a" || overview.Threads[1].ID != "thread-b" {
		t.Fatalf("thread order = %#v", overview.Threads)
	}
	for _, query := range []string{"Chị Lan", "giao hàng lạnh"} {
		got, err := s.AppMemoryOverview(query, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Threads) != 1 || got.Threads[0].ID != "thread-a" {
			t.Fatalf("search %q = %#v", query, got.Threads)
		}
	}
	pinned, err := s.AppMemoryOverview("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned.Threads) != 1 || pinned.Threads[0].PinnedCount != 1 {
		t.Fatalf("pinned overview = %#v", pinned.Threads)
	}
}

func TestAppAgentMemoryUsesLatestInboundFromSameThreadAsProvenance(t *testing.T) {
	s := newStore(t)
	seedAppMemoryThread(t, s, "thread-a", "Chị Lan")
	seedAppMemoryThread(t, s, "thread-b", "Anh Minh")
	seedAppMemoryInbound(t, s, "thread-b", "không được dùng")
	wantSourceID := seedAppMemoryInbound(t, s, "thread-a", "em nhận buổi sáng")

	if err := s.AddZaloMemory(ipc.ZaloMemory{ThreadID: "thread-a", Text: "nhận hàng buổi sáng"}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.AppThreadMemories("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Memories) != 1 {
		t.Fatalf("memory count = %d; want 1", len(detail.Memories))
	}
	got := detail.Memories[0]
	if got.Source != "agent" || got.SourceMessageID != wantSourceID || got.SourcePreview != "em nhận buổi sáng" {
		t.Fatalf("agent provenance = %#v", got)
	}
}

func TestAppPromptMemoryPrioritizesPinnedThenRendersOldestFirst(t *testing.T) {
	s := newStore(t)
	seedAppMemoryThread(t, s, "thread-a", "Chị Lan")
	for i := 1; i <= 14; i++ {
		if _, err := s.CreateAppThreadMemory("thread-a", AppThreadMemoryInput{
			Text: fmt.Sprintf("memory-%02d", i), Pinned: i <= 2,
		}); err != nil {
			t.Fatal(err)
		}
	}

	selected, revision, err := s.AppPromptMemory("thread-a", 12)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, memory := range selected {
		texts = append(texts, memory.Text)
	}
	want := []string{
		"memory-01", "memory-02", "memory-05", "memory-06", "memory-07", "memory-08",
		"memory-09", "memory-10", "memory-11", "memory-12", "memory-13", "memory-14",
	}
	if revision != 14 || !reflect.DeepEqual(texts, want) {
		t.Fatalf("prompt memory revision=%d texts=%v; want 14/%v", revision, texts, want)
	}
}

func TestAppLessonCRUDValidationPinLimitAndPromptSelection(t *testing.T) {
	s := newStore(t)
	seedAppMemoryThread(t, s, "thread-a", "Chị Lan")
	if _, err := s.CreateAppLesson(AppLessonInput{}); !errors.Is(err, ErrAppMemoryInvalid) {
		t.Fatalf("empty lesson = %v; want ErrAppMemoryInvalid", err)
	}
	if _, err := s.CreateAppLesson(AppLessonInput{Better: strings.Repeat("a", 201)}); !errors.Is(err, ErrAppMemoryInvalid) {
		t.Fatalf("long lesson = %v; want ErrAppMemoryInvalid", err)
	}

	var first AppLesson
	for i := 1; i <= 10; i++ {
		created, err := s.CreateAppLesson(AppLessonInput{
			ThreadID: "thread-a", BotText: fmt.Sprintf("cũ-%02d", i),
			Better: fmt.Sprintf("mới-%02d", i), Note: "ngắn hơn", Pinned: i == 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			first = created
		}
	}
	updated, err := s.UpdateAppLesson(first.ID, AppLessonInput{
		ThreadID: "thread-a", BotText: "câu cũ", Better: "câu tốt hơn", Note: "rõ hơn", Pinned: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Better != "câu tốt hơn" {
		t.Fatalf("updated lesson = %#v", updated)
	}

	selected, revision, err := s.AppPromptLessons(8)
	if err != nil {
		t.Fatal(err)
	}
	var better []string
	for _, lesson := range selected {
		better = append(better, lesson.Better)
	}
	want := []string{"câu tốt hơn", "mới-04", "mới-05", "mới-06", "mới-07", "mới-08", "mới-09", "mới-10"}
	if revision != 11 || !reflect.DeepEqual(better, want) {
		t.Fatalf("prompt lessons revision=%d better=%v; want 11/%v", revision, better, want)
	}

	for i := 2; i <= 8; i++ {
		if _, err := s.UpdateAppLesson(int64(i), AppLessonInput{
			ThreadID: "thread-a", Better: fmt.Sprintf("pin-%02d", i), Pinned: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpdateAppLesson(9, AppLessonInput{
		ThreadID: "thread-a", Better: "pin-09", Pinned: true,
	}); !errors.Is(err, ErrAppMemoryPinLimit) {
		t.Fatalf("ninth lesson pin = %v; want ErrAppMemoryPinLimit", err)
	}
	beforeDelete, err := s.AppMemoryRevisions("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAppLesson(first.ID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := s.AppMemoryRevisions("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.Lessons != beforeDelete.Lessons+1 {
		t.Fatalf("lesson delete revision = %d; want %d", afterDelete.Lessons, beforeDelete.Lessons+1)
	}
}
