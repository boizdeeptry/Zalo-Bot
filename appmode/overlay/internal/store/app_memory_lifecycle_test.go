package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"agentdc/internal/ipc"
)

func seedAppMemoryGroupMember(t *testing.T, s *Store, groupID, uid, name, avatar string) {
	t.Helper()
	if err := s.UpsertZaloUser(ipc.ZaloUser{
		UID: uid, DisplayName: name, Avatar: avatar,
	}); err != nil {
		t.Fatalf("UpsertZaloUser(%s): %v", uid, err)
	}
	if err := s.UpsertZaloGroupMemberForTest(groupID, uid); err != nil {
		t.Fatalf("UpsertZaloGroupMemberForTest(%s, %s): %v", groupID, uid, err)
	}
}

func seedAppMemoryGroup(t *testing.T, s *Store) {
	t.Helper()
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "group-a", Name: "Nhóm A", Avatar: "group.png", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	seedAppMemoryGroupMember(t, s, "group-a", "u-1", "Chị Lan", "u1.png")
	seedAppMemoryGroupMember(t, s, "group-a", "u-2", "Anh Minh", "u2.png")
}

func appMemoryTestRevision(t *testing.T, s *Store, threadID, uid string) int64 {
	t.Helper()
	var revision int64
	if err := s.db.QueryRow(`SELECT COALESCE((SELECT revision
FROM app_memory_subject_revisions WHERE thread_id = ? AND uid = ?), 0)`,
		threadID, uid).Scan(&revision); err != nil {
		t.Fatalf("read revision for %s/%s: %v", threadID, uid, err)
	}
	return revision
}

func appMemoryTestRowCount(t *testing.T, s *Store, id int64) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM zalo_memory WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatalf("count Memory %d: %v", id, err)
	}
	return count
}

func TestAppMemoryProposalApprovalAndRejectionAreAtomic(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	oldID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation",
		category: "profile", text: "là giáo viên", status: "active",
		createdAt: now.Add(-24 * time.Hour), lastConfirmedAt: now.Add(-24 * time.Hour),
	})
	pendingID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation",
		category: "profile", text: "là y sĩ", status: "pending", proposalAction: "replace",
		supersedesID: oldID, createdAt: now.Add(-time.Hour), confidence: 0.62,
	})
	rejectedID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "health.note",
		category: "health", text: "đề xuất nhạy cảm", status: "pending",
		proposalAction: "add", createdAt: now.Add(-time.Minute), confidence: 0.5,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 1)

	approved, err := s.ApproveAppThreadMemory("group-a", pendingID, AppMemoryDecisionInput{
		UID: "u-1", MemoryKey: "profile.occupation", Text: "là bác sĩ",
		Category: "profile", ExpectedRevision: 1, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "active" || approved.SupersedesID != oldID ||
		approved.Text != "là bác sĩ" || approved.ProposalAction != "" {
		t.Fatalf("approved replacement = %#v", approved)
	}
	if approved.ExpiresAt == nil || !approved.ExpiresAt.Equal(now.Add(appMemoryLongDuration)) {
		t.Fatalf("approved expiry = %v; want %s", approved.ExpiresAt, now.Add(appMemoryLongDuration))
	}
	var oldStatus string
	if err := s.db.QueryRow(`SELECT status FROM zalo_memory WHERE id = ?`, oldID).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "superseded" || appMemoryTestRevision(t, s, "group-a", "u-1") != 2 {
		t.Fatalf("old status/revision = %q/%d; want superseded/2",
			oldStatus, appMemoryTestRevision(t, s, "group-a", "u-1"))
	}

	if err := s.RejectAppThreadMemory("group-a", rejectedID, AppMemoryDecisionInput{
		UID: "u-1", ExpectedRevision: 2, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if appMemoryTestRowCount(t, s, rejectedID) != 0 || appMemoryTestRowCount(t, s, approved.ID) != 1 {
		t.Fatal("reject did not remove only the selected pending proposal")
	}
	if revision := appMemoryTestRevision(t, s, "group-a", "u-1"); revision != 2 {
		t.Fatalf("reject revision = %d; want unchanged 2", revision)
	}
	addID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "family.child",
		category: "family", text: "có một con", status: "pending",
		proposalAction: "add", createdAt: now, confidence: 0.8,
	})
	added, err := s.ApproveAppThreadMemory("group-a", addID, AppMemoryDecisionInput{
		UID: "u-1", MemoryKey: "family.children", Text: "có hai con",
		Category: "family", ExpectedRevision: 2, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if added.Status != "active" || added.SupersedesID != 0 || added.MemoryKey != "family.children" ||
		appMemoryTestRevision(t, s, "group-a", "u-1") != 3 {
		t.Fatalf("approved add = %#v; revision %d", added,
			appMemoryTestRevision(t, s, "group-a", "u-1"))
	}
}

func TestAppMemoryRestoreAndPinRecalculateExpiry(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	expiredAt := now.Add(-time.Hour)
	id := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "interest.topic",
		category: "interest", text: "thích trồng lan", status: "expired",
		createdAt: now.Add(-60 * 24 * time.Hour), lastConfirmedAt: now.Add(-31 * 24 * time.Hour),
		expiresAt: &expiredAt,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 3)

	restored, err := s.RestoreAppThreadMemory("group-a", id, AppMemoryDecisionInput{
		UID: "u-1", ExpectedRevision: 3, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != "active" || restored.ExpiresAt == nil ||
		!restored.ExpiresAt.Equal(now.Add(appMemoryShortDuration)) {
		t.Fatalf("restored = %#v", restored)
	}
	if revision := appMemoryTestRevision(t, s, "group-a", "u-1"); revision != 4 {
		t.Fatalf("restore revision = %d; want 4", revision)
	}

	pinned, err := s.UpdateAppThreadMemoryV2("group-a", id, AppThreadMemoryInput{
		UID: "u-1", Text: restored.Text, MemoryKey: restored.MemoryKey,
		Category: restored.Category, Pinned: true, ExpectedRevision: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !pinned.Pinned || pinned.ExpiresAt != nil {
		t.Fatalf("pinned = %#v; want nil expiry", pinned)
	}
	unpinned, err := s.UpdateAppThreadMemoryV2("group-a", id, AppThreadMemoryInput{
		UID: "u-1", Text: restored.Text, MemoryKey: restored.MemoryKey,
		Category: restored.Category, Pinned: false, ExpectedRevision: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if unpinned.ExpiresAt == nil || !unpinned.ExpiresAt.Equal(now.Add(appMemoryShortDuration)) {
		t.Fatalf("unpin expiry = %v; want %s", unpinned.ExpiresAt, now.Add(appMemoryShortDuration))
	}

	oldBasis := now.Add(-365 * 24 * time.Hour)
	pinnedID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.old",
		category: "profile", text: "thông tin cũ", status: "active", pinned: true,
		createdAt: oldBasis, lastConfirmedAt: oldBasis,
	})
	before := time.Now().UTC()
	refreshed, err := s.UpdateAppThreadMemoryV2("group-a", pinnedID, AppThreadMemoryInput{
		UID: "u-1", Text: "thông tin cũ", MemoryKey: "profile.old",
		Category: "profile", Pinned: false, ExpectedRevision: 6,
	})
	after := time.Now().UTC()
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ExpiresAt == nil || refreshed.ExpiresAt.Before(before.Add(appMemoryLongDuration)) ||
		refreshed.ExpiresAt.After(after.Add(appMemoryLongDuration)) {
		t.Fatalf("expired-basis unpin expiry = %v; want unpin time + duration", refreshed.ExpiresAt)
	}
	pinExpiredID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.expired",
		category: "profile", text: "cần giữ", status: "expired", createdAt: oldBasis,
	})
	pinExpired, err := s.UpdateAppThreadMemoryV2("group-a", pinExpiredID, AppThreadMemoryInput{
		UID: "u-1", Text: "cần giữ", MemoryKey: "profile.expired",
		Category: "profile", Pinned: true, ExpectedRevision: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pinExpired.Status != "active" || !pinExpired.Pinned || pinExpired.ExpiresAt != nil {
		t.Fatalf("pin expired = %#v; want restored active pin", pinExpired)
	}
}

func TestAppMemoryForgetDeletesActiveLineageButPendingDeleteIsExact(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	oldID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.job", category: "profile",
		text: "cũ", status: "superseded", createdAt: now.Add(-3 * time.Hour),
	})
	activeID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.job", category: "profile",
		text: "mới", status: "active", supersedesID: oldID, createdAt: now.Add(-2 * time.Hour),
	})
	pendingID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.job", category: "profile",
		text: "đề xuất", status: "pending", proposalAction: "replace", supersedesID: activeID,
		createdAt: now.Add(-time.Hour),
	})
	canaryID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-2", memoryKey: "profile.job", category: "profile",
		text: "U2-LINEAGE-CANARY", status: "pending", proposalAction: "replace",
		supersedesID: activeID, createdAt: now,
	})
	otherPendingID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "health.note", category: "health",
		text: "pending riêng", status: "pending", proposalAction: "add", createdAt: now,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 8)
	if err := s.AddZaloMessage(ipc.ZaloMessage{
		ThreadID: "group-a", Direction: ipc.ZaloIn, Author: "Chị Lan", AuthorUID: "u-1",
		Body: "nguồn vẫn còn", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	sourceID, err := s.LatestZaloMessageID("group-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE zalo_memory SET source_message_id = ? WHERE id = ?`,
		sourceID, activeID); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAppThreadMemory("group-a", otherPendingID, AppThreadMemoryInput{
		UID: "u-1", ExpectedRevision: 8,
	}); err != nil {
		t.Fatal(err)
	}
	if appMemoryTestRowCount(t, s, otherPendingID) != 0 || appMemoryTestRowCount(t, s, pendingID) != 1 ||
		appMemoryTestRevision(t, s, "group-a", "u-1") != 8 {
		t.Fatal("pending delete changed more than the exact proposal or bumped active revision")
	}

	if err := s.DeleteAppThreadMemory("group-a", activeID, AppThreadMemoryInput{
		UID: "u-1", ExpectedRevision: 8,
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{oldID, activeID, pendingID} {
		if count := appMemoryTestRowCount(t, s, id); count != 0 {
			t.Fatalf("lineage row %d count = %d; want 0", id, count)
		}
	}
	if appMemoryTestRowCount(t, s, canaryID) != 1 {
		t.Fatal("cross-UID lineage canary was deleted")
	}
	if revision := appMemoryTestRevision(t, s, "group-a", "u-1"); revision != 9 {
		t.Fatalf("active delete revision = %d; want 9", revision)
	}
	var sourceCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM zalo_messages WHERE id = ?`, sourceID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 {
		t.Fatal("forget deleted the source message")
	}
}

func TestAppMemoryScopePinLimitAndManualContentRevisionArePerUID(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for index := 0; index < appMemoryPinLimit; index++ {
		insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
			threadID: "group-a", uid: "u-1", memoryKey: "profile.pin",
			category: "profile", text: "u1 pin", status: "active", pinned: true,
			createdAt: now.Add(time.Duration(index) * time.Minute),
		})
	}
	created, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
		UID: "u-2", Text: "ghi chú tay", Pinned: true, ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.MemoryKey != "manual."+formatMemoryID(created.ID) || created.Category != "profile" ||
		created.Status != "active" || created.Source != "operator" || created.UID != "u-2" {
		t.Fatalf("manual create = %#v", created)
	}
	if _, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
		UID: "u-1", MemoryKey: "profile.extra", Category: "profile",
		Text: "vượt giới hạn", Pinned: true, ExpectedRevision: 0,
	}); !errors.Is(err, ErrAppMemoryPinLimit) {
		t.Fatalf("u-1 thirteenth pin = %v; want ErrAppMemoryPinLimit", err)
	}
	updated, err := s.UpdateAppThreadMemoryV2("group-a", created.ID, AppThreadMemoryInput{
		UID: "u-2", MemoryKey: created.MemoryKey, Category: "profile",
		Text: "ghi chú tay đã sửa", Pinned: true, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Text != "ghi chú tay đã sửa" || appMemoryTestRevision(t, s, "group-a", "u-2") != 2 {
		t.Fatalf("manual update = %#v; revision %d", updated,
			appMemoryTestRevision(t, s, "group-a", "u-2"))
	}
}

func TestAppMemoryConflictAndCrossUIDDoNotPartiallyMutate(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	activeID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.job", category: "profile",
		text: "bác sĩ", status: "active", createdAt: now,
	})
	pendingID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "health.note", category: "health",
		text: "nhạy cảm", status: "pending", proposalAction: "add", createdAt: now,
	})
	expiredID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "interest.old", category: "interest",
		text: "đã cũ", status: "expired", createdAt: now,
	})
	u2PendingID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-2", memoryKey: "identity.secret", category: "identity",
		text: "U2-SECRET", status: "pending", proposalAction: "add", createdAt: now,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 5)
	setAppMemoryV2TestRevision(t, s, "group-a", "u-2", 5)

	assertConflict := func(name string, mutate func() error) {
		t.Helper()
		if err := mutate(); !errors.Is(err, ErrAppMemoryConflict) {
			t.Fatalf("%s error = %v; want ErrAppMemoryConflict", name, err)
		}
		if revision := appMemoryTestRevision(t, s, "group-a", "u-1"); revision != 5 {
			t.Fatalf("%s revision = %d; want unchanged 5", name, revision)
		}
	}
	assertConflict("approve", func() error {
		_, err := s.ApproveAppThreadMemory("group-a", pendingID, AppMemoryDecisionInput{
			UID: "u-1", MemoryKey: "health.note", Text: "nhạy cảm", Category: "health",
			ExpectedRevision: 4, Now: now,
		})
		return err
	})
	assertConflict("reject", func() error {
		return s.RejectAppThreadMemory("group-a", pendingID, AppMemoryDecisionInput{
			UID: "u-1", ExpectedRevision: 4, Now: now,
		})
	})
	assertConflict("restore", func() error {
		_, err := s.RestoreAppThreadMemory("group-a", expiredID, AppMemoryDecisionInput{
			UID: "u-1", ExpectedRevision: 4, Now: now,
		})
		return err
	})
	assertConflict("update", func() error {
		_, err := s.UpdateAppThreadMemoryV2("group-a", activeID, AppThreadMemoryInput{
			UID: "u-1", MemoryKey: "profile.job", Category: "profile", Text: "đã đổi",
			ExpectedRevision: 4,
		})
		return err
	})
	assertConflict("delete", func() error {
		return s.DeleteAppThreadMemory("group-a", activeID, AppThreadMemoryInput{
			UID: "u-1", ExpectedRevision: 4,
		})
	})
	assertConflict("create", func() error {
		_, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
			UID: "u-1", MemoryKey: "profile.new", Category: "profile", Text: "mới",
			ExpectedRevision: 4,
		})
		return err
	})
	if appMemoryTestRowCount(t, s, activeID) != 1 || appMemoryTestRowCount(t, s, pendingID) != 1 ||
		appMemoryTestRowCount(t, s, expiredID) != 1 {
		t.Fatal("stale mutations partially changed rows")
	}

	if _, err := s.ApproveAppThreadMemory("group-a", u2PendingID, AppMemoryDecisionInput{
		UID: "u-1", MemoryKey: "identity.secret", Text: "không được thấy", Category: "identity",
		ExpectedRevision: 5, Now: now,
	}); !errors.Is(err, ErrAppMemoryNotFound) {
		t.Fatalf("cross-UID approve error = %v; want ErrAppMemoryNotFound", err)
	}
	if err := s.DeleteAppThreadMemory("group-a", u2PendingID, AppThreadMemoryInput{
		UID: "u-1", ExpectedRevision: 5,
	}); !errors.Is(err, ErrAppMemoryNotFound) {
		t.Fatalf("cross-UID delete error = %v; want ErrAppMemoryNotFound", err)
	}
	if appMemoryTestRowCount(t, s, u2PendingID) != 1 {
		t.Fatal("cross-UID mutation changed canary")
	}
}

func TestAppMemoryConflictChecksExplicitCommonDeleteZeroRevision(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "group-a", Name: "Nhóm A", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	id := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.rule", category: "preference",
		text: "quy tắc chung", status: "active", createdAt: now,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "", 3)
	if _, err := s.db.Exec(`INSERT INTO app_memory_revisions(scope, scope_id, revision)
VALUES ('thread', 'group-a', 4)`); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAppThreadMemory("group-a", id, AppThreadMemoryInput{
		UID: "", ExpectedRevision: 0,
	}); !errors.Is(err, ErrAppMemoryConflict) {
		t.Fatalf("explicit common delete error = %v; want ErrAppMemoryConflict", err)
	}
	if appMemoryTestRowCount(t, s, id) != 1 ||
		appMemoryTestRevision(t, s, "group-a", "") != 3 {
		t.Fatal("explicit stale common delete partially changed Memory state")
	}
	var legacyRevision int64
	if err := s.db.QueryRow(`SELECT revision FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = 'group-a'`).Scan(&legacyRevision); err != nil {
		t.Fatal(err)
	}
	if legacyRevision != 4 {
		t.Fatalf("explicit stale common delete legacy revision = %d; want 4", legacyRevision)
	}

	if err := s.DeleteAppThreadMemory("group-a", id); err != nil {
		t.Fatalf("legacy two-argument delete: %v", err)
	}
	if appMemoryTestRowCount(t, s, id) != 0 ||
		appMemoryTestRevision(t, s, "group-a", "") != 4 {
		t.Fatal("legacy two-argument delete did not remain compatible")
	}
}

func TestDeleteAppThreadMemoryV2AlwaysRequiresExactRevision(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "group-a", Name: "Nhóm A", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
		MemoryKey: "group.rule", Category: "preference", Text: "quy tắc chung",
		ExpectedRevision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteAppThreadMemoryV2("group-a", created.ID, AppThreadMemoryInput{
		ExpectedRevision: 0,
	}); !errors.Is(err, ErrAppMemoryConflict) {
		t.Fatalf("strict stale delete error = %v; want ErrAppMemoryConflict", err)
	}
	if appMemoryTestRowCount(t, s, created.ID) != 1 {
		t.Fatal("strict stale delete removed Memory")
	}
	if err := s.DeleteAppThreadMemoryV2("group-a", created.ID, AppThreadMemoryInput{
		ExpectedRevision: 1,
	}); err != nil {
		t.Fatalf("strict current delete: %v", err)
	}

	legacy, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
		MemoryKey: "group.legacy", Category: "preference", Text: "legacy",
		ExpectedRevision: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAppThreadMemory("group-a", legacy.ID); err != nil {
		t.Fatalf("legacy two-argument delete: %v", err)
	}
}

func TestAppMemoryLifecycleRejectsWhitespaceDecoratedIdentities(t *testing.T) {
	newScopedStore := func(t *testing.T) *Store {
		t.Helper()
		s := newStore(t)
		seedAppMemoryGroup(t, s)
		return s
	}

	t.Run("create UID", func(t *testing.T) {
		s := newScopedStore(t)
		if _, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
			UID: " u-1 ", MemoryKey: "profile.note", Category: "profile",
			Text: "không được tạo", ExpectedRevision: 0,
		}); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("decorated create UID error = %v; want ErrAppMemoryInvalid", err)
		}
		detail, err := s.AppThreadMemoryScope("group-a", "u-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if detail.Revision != 0 || len(detail.Active) != 0 {
			t.Fatalf("decorated create mutated canonical scope: %#v", detail)
		}
	})

	t.Run("create thread ID", func(t *testing.T) {
		s := newScopedStore(t)
		if _, err := s.CreateAppThreadMemoryV2(" group-a ", AppThreadMemoryInput{
			UID: "u-1", MemoryKey: "profile.note", Category: "profile",
			Text: "không được tạo", ExpectedRevision: 0,
		}); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("decorated create thread error = %v; want ErrAppMemoryInvalid", err)
		}
		detail, err := s.AppThreadMemoryScope("group-a", "u-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if detail.Revision != 0 || len(detail.Active) != 0 {
			t.Fatalf("decorated thread create mutated canonical scope: %#v", detail)
		}
	})

	t.Run("update", func(t *testing.T) {
		s := newScopedStore(t)
		created, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
			UID: "u-1", MemoryKey: "profile.note", Category: "profile",
			Text: "nguyên bản", ExpectedRevision: 0,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateAppThreadMemoryV2("group-a", created.ID, AppThreadMemoryInput{
			UID: " u-1 ", MemoryKey: "profile.note", Category: "profile",
			Text: "không được sửa", ExpectedRevision: 1,
		}); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("decorated update error = %v; want ErrAppMemoryInvalid", err)
		}
		detail, err := s.AppThreadMemoryScope("group-a", "u-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if detail.Revision != 1 || len(detail.Active) != 1 || detail.Active[0].Text != "nguyên bản" {
			t.Fatalf("decorated update mutated canonical scope: %#v", detail)
		}
	})

	t.Run("delete", func(t *testing.T) {
		s := newScopedStore(t)
		created, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
			UID: "u-1", MemoryKey: "profile.note", Category: "profile",
			Text: "phải còn", ExpectedRevision: 0,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DeleteAppThreadMemoryV2("group-a", created.ID, AppThreadMemoryInput{
			UID: " u-1 ", ExpectedRevision: 1,
		}); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("decorated delete error = %v; want ErrAppMemoryInvalid", err)
		}
		if appMemoryTestRowCount(t, s, created.ID) != 1 {
			t.Fatal("decorated delete removed canonical Memory")
		}
	})

	seedPending := func(t *testing.T, s *Store) int64 {
		t.Helper()
		if _, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
			ThreadID: "group-a", SubjectUID: "u-1", Now: time.Now(),
			Operations: []AppMemoryOperation{{
				Action: "add", MemoryKey: "health.note", Value: "nhạy cảm",
				Category: "health", Confidence: 1,
			}},
		}); err != nil {
			t.Fatal(err)
		}
		detail, err := s.AppThreadMemoryScope("group-a", "u-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Pending) != 1 {
			t.Fatalf("pending detail = %#v", detail)
		}
		return detail.Pending[0].ID
	}

	t.Run("approve", func(t *testing.T) {
		s := newScopedStore(t)
		id := seedPending(t, s)
		if _, err := s.ApproveAppThreadMemory("group-a", id, AppMemoryDecisionInput{
			UID: " u-1 ", MemoryKey: "health.note", Text: "nhạy cảm",
			Category: "health", ExpectedRevision: 0, Now: time.Now(),
		}); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("decorated approve error = %v; want ErrAppMemoryInvalid", err)
		}
		if appMemoryTestRowCount(t, s, id) != 1 {
			t.Fatal("decorated approve changed pending Memory")
		}
	})

	t.Run("reject", func(t *testing.T) {
		s := newScopedStore(t)
		id := seedPending(t, s)
		if err := s.RejectAppThreadMemory("group-a", id, AppMemoryDecisionInput{
			UID: " u-1 ", ExpectedRevision: 0,
		}); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("decorated reject error = %v; want ErrAppMemoryInvalid", err)
		}
		if appMemoryTestRowCount(t, s, id) != 1 {
			t.Fatal("decorated reject removed pending Memory")
		}
	})

	t.Run("restore", func(t *testing.T) {
		s := newScopedStore(t)
		past := time.Now().Add(-31 * 24 * time.Hour)
		if _, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
			ThreadID: "group-a", SubjectUID: "u-1", Now: past,
			Operations: []AppMemoryOperation{{
				Action: "add", MemoryKey: "interest.old", Value: "đã cũ",
				Category: "interest", Confidence: 1,
			}},
		}); err != nil {
			t.Fatal(err)
		}
		detail, err := s.AppThreadMemoryScope("group-a", "u-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Expired) != 1 {
			t.Fatalf("expired detail = %#v", detail)
		}
		id := detail.Expired[0].ID
		if _, err := s.RestoreAppThreadMemory("group-a", id, AppMemoryDecisionInput{
			UID: " u-1 ", ExpectedRevision: detail.Revision, Now: time.Now(),
		}); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("decorated restore error = %v; want ErrAppMemoryInvalid", err)
		}
		detail, err = s.AppThreadMemoryScope("group-a", "u-1", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if len(detail.Expired) != 1 || detail.Expired[0].ID != id {
			t.Fatalf("decorated restore changed canonical Memory: %#v", detail)
		}
	})
}

func TestAppMemoryConflictChecksStrictCommonCreateAndUpdate(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "group-a", Name: "Nhóm A", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	id := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.rule", category: "preference",
		text: "quy tắc chung", status: "active", createdAt: now,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "", 3)
	if _, err := s.db.Exec(`INSERT INTO app_memory_revisions(scope, scope_id, revision)
VALUES ('thread', 'group-a', 4)`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
		UID: "", MemoryKey: "", Category: "", Text: "ghi chú chung mới",
		ExpectedRevision: 0,
	}); !errors.Is(err, ErrAppMemoryConflict) {
		t.Fatalf("strict common create error = %v; want ErrAppMemoryConflict", err)
	}
	if _, err := s.UpdateAppThreadMemoryV2("group-a", id, AppThreadMemoryInput{
		UID: "", MemoryKey: "", Category: "", Text: "đã bị sửa",
		ExpectedRevision: 0,
	}); !errors.Is(err, ErrAppMemoryConflict) {
		t.Fatalf("strict common update error = %v; want ErrAppMemoryConflict", err)
	}
	var count int
	var text string
	if err := s.db.QueryRow(`SELECT COUNT(*), MIN(text) FROM zalo_memory
WHERE thread_id = 'group-a' AND uid = ''`).Scan(&count, &text); err != nil {
		t.Fatal(err)
	}
	if count != 1 || text != "quy tắc chung" ||
		appMemoryTestRevision(t, s, "group-a", "") != 3 {
		t.Fatalf("stale strict mutations changed row/revision: count=%d text=%q revision=%d",
			count, text, appMemoryTestRevision(t, s, "group-a", ""))
	}
	var legacyRevision int64
	if err := s.db.QueryRow(`SELECT revision FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = 'group-a'`).Scan(&legacyRevision); err != nil {
		t.Fatal(err)
	}
	if legacyRevision != 4 {
		t.Fatalf("stale strict mutations legacy revision = %d; want 4", legacyRevision)
	}
}

func TestAppMemoryConflictPreventsDuplicateActiveKeysInExactScope(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "group-b", Name: "Nhóm B", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	seedAppMemoryGroupMember(t, s, "group-b", "u-1", "Chị Lan", "u1.png")
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	destinationID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.destination",
		category: "profile", text: "đích đang hoạt động", status: "active", createdAt: now,
	})
	editableID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.editable",
		category: "profile", text: "dòng có thể sửa", status: "active", createdAt: now,
	})
	pendingAddID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "health.pending",
		category: "health", text: "đề xuất thêm", status: "pending",
		proposalAction: "add", createdAt: now,
	})
	targetID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.target",
		category: "profile", text: "đích thay thế", status: "active", createdAt: now,
	})
	pendingReplacementID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.target",
		category: "profile", text: "đề xuất thay thế", status: "pending",
		proposalAction: "replace", supersedesID: targetID, createdAt: now,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 10)

	assertConflict := func(name string, mutate func() error) {
		t.Helper()
		if err := mutate(); !errors.Is(err, ErrAppMemoryConflict) {
			t.Fatalf("%s error = %v; want ErrAppMemoryConflict", name, err)
		}
		if revision := appMemoryTestRevision(t, s, "group-a", "u-1"); revision != 10 {
			t.Fatalf("%s revision = %d; want unchanged 10", name, revision)
		}
	}
	assertConflict("manual create", func() error {
		_, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
			UID: "u-1", MemoryKey: "profile.destination", Category: "profile",
			Text: "đụng khóa", ExpectedRevision: 10,
		})
		return err
	})
	assertConflict("manual key edit", func() error {
		_, err := s.UpdateAppThreadMemoryV2("group-a", editableID, AppThreadMemoryInput{
			UID: "u-1", MemoryKey: "profile.destination", Category: "profile",
			Text: "dòng có thể sửa", ExpectedRevision: 10,
		})
		return err
	})
	assertConflict("pending add approval", func() error {
		_, err := s.ApproveAppThreadMemory("group-a", pendingAddID, AppMemoryDecisionInput{
			UID: "u-1", MemoryKey: "profile.destination", Category: "profile",
			Text: "đề xuất thêm", ExpectedRevision: 10, Now: now,
		})
		return err
	})
	assertConflict("edited replacement approval", func() error {
		_, err := s.ApproveAppThreadMemory("group-a", pendingReplacementID, AppMemoryDecisionInput{
			UID: "u-1", MemoryKey: "profile.destination", Category: "profile",
			Text: "đề xuất thay thế", ExpectedRevision: 10, Now: now,
		})
		return err
	})

	var editableKey, editableText, pendingAddStatus, pendingReplacementStatus, targetStatus string
	if err := s.db.QueryRow(`SELECT memory_key, text FROM zalo_memory WHERE id = ?`, editableID).Scan(
		&editableKey, &editableText,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT status FROM zalo_memory WHERE id = ?`, pendingAddID).Scan(
		&pendingAddStatus,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT status FROM zalo_memory WHERE id = ?`, pendingReplacementID).Scan(
		&pendingReplacementStatus,
	); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT status FROM zalo_memory WHERE id = ?`, targetID).Scan(
		&targetStatus,
	); err != nil {
		t.Fatal(err)
	}
	var destinationCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM zalo_memory
WHERE thread_id = 'group-a' AND uid = 'u-1' AND memory_key = 'profile.destination'
  AND status = 'active'`).Scan(&destinationCount); err != nil {
		t.Fatal(err)
	}
	if destinationCount != 1 || destinationID <= 0 || editableKey != "profile.editable" ||
		editableText != "dòng có thể sửa" || pendingAddStatus != "pending" ||
		pendingReplacementStatus != "pending" || targetStatus != "active" {
		t.Fatalf("collision attempts partially mutated rows: count=%d editable=%q/%q add=%q replace=%q target=%q",
			destinationCount, editableKey, editableText, pendingAddStatus,
			pendingReplacementStatus, targetStatus)
	}

	if _, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
		UID: "u-2", MemoryKey: "profile.destination", Category: "profile",
		Text: "cùng khóa UID khác", ExpectedRevision: 0,
	}); err != nil {
		t.Fatalf("same key in another UID: %v", err)
	}
	if _, err := s.CreateAppThreadMemoryV2("group-b", AppThreadMemoryInput{
		UID: "u-1", MemoryKey: "profile.destination", Category: "profile",
		Text: "cùng khóa thread khác", ExpectedRevision: 0,
	}); err != nil {
		t.Fatalf("same key in another thread: %v", err)
	}
}

func TestAppMemoryMutationReturnsItsTransactionRow(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertZaloThreadInfo(ipc.ZaloThreadInfo{
		ID: "group-a", Name: "Nhóm A", ThreadType: ipc.ZaloThreadGroup,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	id := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.rule", category: "preference",
		text: "trước cập nhật", status: "active", createdAt: now,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "", 1)
	if _, err := s.db.Exec(`CREATE TRIGGER app_memory_test_slow_update
AFTER UPDATE OF text ON zalo_memory
WHEN NEW.id = ` + formatMemoryID(id) + ` AND NEW.text = 'transaction-write'
BEGIN
  SELECT SUM(value) FROM (
    WITH RECURSIVE values_to_sum(value) AS (
      VALUES(1) UNION ALL SELECT value + 1 FROM values_to_sum WHERE value < 2000000
    )
    SELECT value FROM values_to_sum
  );
END`); err != nil {
		t.Fatal(err)
	}

	type mutationResult struct {
		memory AppThreadMemory
		err    error
	}
	result := make(chan mutationResult, 1)
	go func() {
		memory, err := s.UpdateAppThreadMemoryV2("group-a", id, AppThreadMemoryInput{
			UID: "", MemoryKey: "group.rule", Category: "preference",
			Text: "transaction-write", ExpectedRevision: 1,
		})
		result <- mutationResult{memory: memory, err: err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for s.db.Stats().InUse != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.db.Stats().InUse != 1 {
		t.Fatal("mutation never acquired the store connection")
	}
	waitsBefore := s.db.Stats().WaitCount
	concurrent := make(chan error, 1)
	go func() {
		_, err := s.db.Exec(`UPDATE zalo_memory SET text = 'queued-writer' WHERE id = ?`, id)
		concurrent <- err
	}()
	for s.db.Stats().WaitCount == waitsBefore && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.db.Stats().WaitCount == waitsBefore {
		t.Fatal("competing writer was not queued behind the mutation transaction")
	}

	var got mutationResult
	select {
	case got = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Memory update")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	select {
	case err := <-concurrent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for competing writer")
	}
	if got.memory.Text != "transaction-write" {
		t.Fatalf("returned text = %q; want this transaction's value", got.memory.Text)
	}
	var storedText string
	if err := s.db.QueryRow(`SELECT text FROM zalo_memory WHERE id = ?`, id).Scan(&storedText); err != nil {
		t.Fatal(err)
	}
	if storedText != "queued-writer" {
		t.Fatalf("stored text = %q; want proof queued writer ran", storedText)
	}
}

func TestAppMemoryScopeLoadsMultipleProposalTargetsWithoutCrossScopeLeak(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	firstTargetID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.first",
		category: "profile", text: "FIRST-OLD", status: "active", createdAt: now,
	})
	secondTargetID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.second",
		category: "profile", text: "SECOND-OLD", status: "active", createdAt: now,
	})
	crossScopeTargetID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-2", memoryKey: "profile.secret",
		category: "profile", text: "U2-TARGET-CANARY", status: "active", createdAt: now,
	})
	for _, row := range []appMemoryV2TestRow{
		{threadID: "group-a", uid: "u-1", memoryKey: "profile.first", category: "profile", text: "FIRST-NEW", status: "pending", proposalAction: "replace", supersedesID: firstTargetID, createdAt: now},
		{threadID: "group-a", uid: "u-1", memoryKey: "profile.second", category: "profile", text: "SECOND-NEW", status: "pending", proposalAction: "replace", supersedesID: secondTargetID, createdAt: now},
		{threadID: "group-a", uid: "u-1", memoryKey: "profile.malicious", category: "profile", text: "CROSS-SCOPE-PROPOSAL", status: "pending", proposalAction: "replace", supersedesID: crossScopeTargetID, createdAt: now},
	} {
		insertAppMemoryV2TestRow(t, s, row)
	}

	detail, err := s.AppThreadMemoryScope("group-a", "u-1", now)
	if err != nil {
		t.Fatal(err)
	}
	targets := make(map[string]*AppThreadMemory)
	for _, proposal := range detail.Pending {
		targets[proposal.Text] = proposal.ProposalTarget
	}
	if targets["FIRST-NEW"] == nil || targets["FIRST-NEW"].ID != firstTargetID ||
		targets["FIRST-NEW"].Text != "FIRST-OLD" {
		t.Fatalf("first target = %#v", targets["FIRST-NEW"])
	}
	if targets["SECOND-NEW"] == nil || targets["SECOND-NEW"].ID != secondTargetID ||
		targets["SECOND-NEW"].Text != "SECOND-OLD" {
		t.Fatalf("second target = %#v", targets["SECOND-NEW"])
	}
	if targets["CROSS-SCOPE-PROPOSAL"] != nil {
		t.Fatalf("cross-scope target leaked = %#v", targets["CROSS-SCOPE-PROPOSAL"])
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if containsJSONText(encoded, "U2-TARGET-CANARY") {
		t.Fatalf("proposal targets leaked cross-scope text: %s", encoded)
	}
}

func containsJSONText(document []byte, value string) bool {
	for start := 0; start+len(value) <= len(document); start++ {
		if string(document[start:start+len(value)]) == value {
			return true
		}
	}
	return false
}

func formatMemoryID(id int64) string {
	return fmt.Sprintf("%d", id)
}
