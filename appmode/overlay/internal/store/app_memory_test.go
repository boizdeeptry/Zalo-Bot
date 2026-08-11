package store

import (
	"encoding/json"
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
	if _, err := s.CreateZaloCLISession(ZaloCLISession{
		ThreadID: "thread-a", ClaudeSessionID: "session-a", Model: "sonnet",
		PromptFingerprint: "persona-v1", MemoryRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	detail, err := s.AppThreadMemories("thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Memories) != 0 || detail.Revision != 3 || detail.SyncedRevision != 1 {
		t.Fatalf("detail after delete = %#v; want empty revision 3 synced at 1", detail)
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

func TestAppMemoryOverviewDoesNotSurfaceInactiveOrGroupPersonalFacts(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "group-a", Name: "Nhóm A", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	seedAppMemoryGroupMember(t, s, "group-a", "u-1", "Chị Lan", "u1.png")
	seedAppMemoryThread(t, s, "u-private", "Chị Mai")
	now := time.Now()
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.rule", category: "preference",
		text: "GROUP-COMMON-ACTIVE", status: "active", createdAt: now.Add(-5 * time.Minute),
	})
	canaries := []appMemoryV2TestRow{
		{threadID: "group-a", uid: "u-1", memoryKey: "profile.private", category: "profile", text: "GROUP-PERSONAL-ACTIVE-CANARY", status: "active", pinned: true, createdAt: now.Add(-4 * time.Minute)},
		{threadID: "group-a", memoryKey: "group.old", category: "preference", text: "SUPERSEDED-PIN-CANARY", status: "superseded", pinned: true, createdAt: now.Add(-3 * time.Minute)},
		{threadID: "group-a", memoryKey: "health.pending", category: "health", text: "PENDING-SENSITIVE-CANARY", status: "pending", proposalAction: "add", pinned: true, createdAt: now.Add(-2 * time.Minute)},
		{threadID: "group-a", memoryKey: "group.expired", category: "interest", text: "EXPIRED-CANARY", status: "expired", pinned: true, createdAt: now.Add(-time.Minute)},
		{threadID: "u-private", uid: "u-private", memoryKey: "health.pending", category: "health", text: "DIRECT-PENDING-CANARY", status: "pending", proposalAction: "add", pinned: true, createdAt: now},
	}
	for _, row := range canaries {
		insertAppMemoryV2TestRow(t, s, row)
	}
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "u-private", uid: "u-private", memoryKey: "profile.active",
		category: "profile", text: "DIRECT-ACTIVE", status: "active",
		createdAt: now.Add(-time.Minute),
	})

	overview, err := s.AppMemoryOverview("", false)
	if err != nil {
		t.Fatal(err)
	}
	previews := make(map[string]string)
	for _, thread := range overview.Threads {
		previews[thread.ID] = thread.Preview
	}
	if previews["group-a"] != "GROUP-COMMON-ACTIVE" || previews["u-private"] != "DIRECT-ACTIVE" {
		t.Fatalf("safe previews = %#v", previews)
	}
	for _, canary := range []string{
		"GROUP-PERSONAL-ACTIVE-CANARY", "SUPERSEDED-PIN-CANARY",
		"PENDING-SENSITIVE-CANARY", "EXPIRED-CANARY", "DIRECT-PENDING-CANARY",
	} {
		searched, err := s.AppMemoryOverview(canary, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(searched.Threads) != 0 {
			t.Fatalf("overview search surfaced %q: %#v", canary, searched.Threads)
		}
	}
	pinned, err := s.AppMemoryOverview("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned.Threads) != 0 {
		t.Fatalf("inactive or group-personal pins surfaced in overview: %#v", pinned.Threads)
	}
}

func TestAppMemoryRestoreRejectsActiveKeyCollisionInExactScope(t *testing.T) {
	const memoryKey = "profile.occupation"
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

	t.Run("same scope conflicts without mutation", func(t *testing.T) {
		s := newStore(t)
		seedAppMemoryGroup(t, s)
		expiredID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
			threadID: "group-a", uid: "u-1", memoryKey: memoryKey,
			category: "profile", text: "nghề cũ", status: "expired", createdAt: now.Add(-2 * time.Hour),
		})
		activeID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
			threadID: "group-a", uid: "u-1", memoryKey: memoryKey,
			category: "profile", text: "nghề mới", status: "active", createdAt: now.Add(-time.Hour),
		})
		setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 7)
		if _, err := s.db.Exec(`INSERT INTO app_memory_revisions(scope, scope_id, revision)
VALUES ('thread', 'group-a', 11)`); err != nil {
			t.Fatal(err)
		}

		if _, err := s.RestoreAppThreadMemory("group-a", expiredID, AppMemoryDecisionInput{
			UID: "u-1", ExpectedRevision: 7, Now: now,
		}); !errors.Is(err, ErrAppMemoryConflict) {
			t.Fatalf("restore collision error = %v; want ErrAppMemoryConflict", err)
		}
		var expiredStatus, activeStatus string
		if err := s.db.QueryRow(`SELECT status FROM zalo_memory WHERE id = ?`, expiredID).Scan(&expiredStatus); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT status FROM zalo_memory WHERE id = ?`, activeID).Scan(&activeStatus); err != nil {
			t.Fatal(err)
		}
		var activeCount int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM zalo_memory
WHERE thread_id = 'group-a' AND uid = 'u-1' AND memory_key = ? AND status = 'active'`,
			memoryKey).Scan(&activeCount); err != nil {
			t.Fatal(err)
		}
		var threadRevision int64
		if err := s.db.QueryRow(`SELECT revision FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = 'group-a'`).Scan(&threadRevision); err != nil {
			t.Fatal(err)
		}
		if expiredStatus != "expired" || activeStatus != "active" || activeCount != 1 {
			t.Fatalf("restore collision states = expired:%q active:%q count:%d", expiredStatus, activeStatus, activeCount)
		}
		if subjectRevision := appMemoryTestRevision(t, s, "group-a", "u-1"); subjectRevision != 7 || threadRevision != 11 {
			t.Fatalf("restore collision revisions = subject:%d thread:%d; want 7/11", subjectRevision, threadRevision)
		}
	})

	t.Run("other uid and thread do not conflict", func(t *testing.T) {
		s := newStore(t)
		seedAppMemoryGroup(t, s)
		if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
			ID: "group-b", Name: "Nhóm B", ThreadType: ipc.ZaloThreadGroup,
		}); err != nil {
			t.Fatal(err)
		}
		seedAppMemoryGroupMember(t, s, "group-b", "u-1", "Chị Lan", "u1.png")
		expiredID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
			threadID: "group-a", uid: "u-1", memoryKey: memoryKey,
			category: "profile", text: "cần khôi phục", status: "expired", createdAt: now.Add(-2 * time.Hour),
		})
		insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
			threadID: "group-a", uid: "u-2", memoryKey: memoryKey,
			category: "profile", text: "UID khác", status: "active", createdAt: now,
		})
		insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
			threadID: "group-b", uid: "u-1", memoryKey: memoryKey,
			category: "profile", text: "nhóm khác", status: "active", createdAt: now,
		})
		setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 7)

		restored, err := s.RestoreAppThreadMemory("group-a", expiredID, AppMemoryDecisionInput{
			UID: "u-1", ExpectedRevision: 7, Now: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if restored.Status != "active" || appMemoryTestRevision(t, s, "group-a", "u-1") != 8 {
			t.Fatalf("cross-scope restore = %#v; revision %d", restored,
				appMemoryTestRevision(t, s, "group-a", "u-1"))
		}
	})
}

func TestAppMemoryScopeReturnsOnlySelectedGroupSubject(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.rule", category: "preference",
		text: "quy tắc chung", status: "active", createdAt: now.Add(-4 * time.Hour), expiresAt: &future,
	})
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.pending", category: "address",
		text: "đề xuất chung", status: "pending", proposalAction: "add", createdAt: now,
	})
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.expired", category: "interest",
		text: "chung hết hạn", status: "expired", createdAt: now,
	})
	targetID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.job", category: "profile",
		text: "là giáo viên", status: "active", createdAt: now.Add(-3 * time.Hour), expiresAt: &future,
	})
	if err := s.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: "group-a", Direction: ipc.ZaloIn, Author: "Chị Lan", AuthorUID: "u-1",
		Body: "em là bác sĩ", CreatedAt: now.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	sourceID, err := s.LatestZaloMessageID("group-a")
	if err != nil {
		t.Fatal(err)
	}
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.job", category: "profile",
		text: "là bác sĩ", status: "pending", proposalAction: "replace", supersedesID: targetID,
		createdAt: now.Add(-time.Hour), confidence: 0.7, sourceMessageID: sourceID,
	})
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "interest.old", category: "interest",
		text: "thích sách cũ", status: "expired", createdAt: now,
	})
	for _, status := range []string{"active", "pending", "expired"} {
		insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
			threadID: "group-a", uid: "u-2", memoryKey: "profile.canary." + status,
			category: "profile", text: "U2-PRIVATE-" + status, status: status,
			proposalAction: map[bool]string{true: "add"}[status == "pending"], createdAt: now,
		})
	}
	setAppMemoryV2TestRevision(t, s, "group-a", "", 2)
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 7)
	setAppMemoryV2TestRevision(t, s, "group-a", "u-2", 11)
	if _, err := s.CreateZaloCLISession(ZaloCLISession{
		ThreadID: "group-a", ClaudeSessionID: "session-a", Model: "sonnet",
		PromptFingerprint: "v2", MemorySubjectUID: "u-1",
		MemorySubjectRevision: 7, MemoryCommonRevision: 2,
	}); err != nil {
		t.Fatal(err)
	}

	detail, err := s.AppThreadMemoryScope("group-a", "u-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if detail.SelectedUID != "u-1" || detail.CommonRevision != 2 || detail.SubjectRevision != 7 ||
		detail.SyncedCommonRevision != 2 || detail.SyncedSubjectRevision != 7 || !detail.Synced {
		t.Fatalf("scope revisions/sync = %#v", detail)
	}
	if detail.Common.Active != 1 || detail.Common.Pending != 1 || detail.Common.Expired != 1 {
		t.Fatalf("common counts = %#v", detail.Common)
	}
	if len(detail.Members) != 2 || detail.Members[0].UID != "u-1" ||
		detail.Members[0].Name != "Chị Lan" || detail.Members[0].Avatar != "u1.png" ||
		detail.Members[0].Active != 1 || detail.Members[0].Pending != 1 || detail.Members[0].Expired != 1 {
		t.Fatalf("members = %#v", detail.Members)
	}
	if len(detail.Active) != 1 || len(detail.Pending) != 1 || len(detail.Expired) != 1 {
		t.Fatalf("sections = active %#v pending %#v expired %#v",
			detail.Active, detail.Pending, detail.Expired)
	}
	proposal := detail.Pending[0]
	if proposal.ProposalTarget == nil || proposal.ProposalTarget.ID != targetID ||
		proposal.ProposalTarget.Text != "là giáo viên" || proposal.SourceMessageID != sourceID ||
		proposal.SourcePreview != "em là bác sĩ" || proposal.Confidence != 0.7 {
		t.Fatalf("proposal presentation = %#v", proposal)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"U2-PRIVATE-active", "U2-PRIVATE-pending", "U2-PRIVATE-expired"} {
		if containsJSONText(encoded, canary) {
			t.Fatalf("selected u-1 detail leaked %q: %s", canary, encoded)
		}
	}

	common, err := s.AppThreadMemoryScope("group-a", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if common.SelectedUID != "" || len(common.Active) != 1 || len(common.Pending) != 1 ||
		len(common.Expired) != 1 || common.Active[0].UID != "" {
		t.Fatalf("common scope = %#v", common)
	}
	encoded, err = json.Marshal(common)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"U2-PRIVATE-active", "U2-PRIVATE-pending", "U2-PRIVATE-expired"} {
		if containsJSONText(encoded, canary) {
			t.Fatalf("common detail leaked %q: %s", canary, encoded)
		}
	}
}

func TestAppMemoryScopeAutoSelectsPrivateChatSubject(t *testing.T) {
	s := newStore(t)
	seedAppMemoryThread(t, s, "u-private", "Chị Mai")
	if err := s.UpsertZaloUser(ipc.ZaloUser{
		UID: "u-private", DisplayName: "Chị Mai", Avatar: "mai.png",
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "u-private", uid: "u-private", memoryKey: "profile.name",
		category: "profile", text: "khách riêng", status: "active", createdAt: now,
	})
	setAppMemoryV2TestRevision(t, s, "u-private", "u-private", 4)

	detail, err := s.AppThreadMemoryScope("u-private", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if detail.SelectedUID != "u-private" || len(detail.Members) != 1 ||
		detail.Members[0].UID != "u-private" || len(detail.Active) != 1 ||
		detail.Active[0].Text != "khách riêng" || detail.SubjectRevision != 4 {
		t.Fatalf("private scope = %#v", detail)
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
