package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type appMemoryV2TestRow struct {
	threadID, uid   string
	memoryKey       string
	category, text  string
	status          string
	pinned          bool
	createdAt       time.Time
	expiresAt       *time.Time
	confidence      float64
	proposalAction  string
	supersedesID    int64
	lastConfirmedAt time.Time
	source          string
	sourceMessageID int64
}

func insertAppMemoryV2TestRow(t *testing.T, s *Store, row appMemoryV2TestRow) int64 {
	t.Helper()
	var expiresAt any
	if row.expiresAt != nil {
		expiresAt = ts(*row.expiresAt)
	}
	lastConfirmedAt := ""
	if !row.lastConfirmedAt.IsZero() {
		lastConfirmedAt = ts(row.lastConfirmedAt)
	}
	source := row.source
	if source == "" {
		source = "agent"
	}
	result, err := s.db.Exec(`INSERT INTO zalo_memory(
thread_id, uid, memory_key, category, text, confidence, status, proposal_action,
supersedes_id, pinned, source, source_message_id, created_at, updated_at,
last_confirmed_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.threadID, row.uid, row.memoryKey, row.category, row.text, row.confidence,
		row.status, row.proposalAction, row.supersedesID, row.pinned, source,
		row.sourceMessageID, ts(row.createdAt), ts(row.createdAt), lastConfirmedAt, expiresAt,
	)
	if err != nil {
		t.Fatalf("insert Memory V2 test row: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read Memory V2 test row ID: %v", err)
	}
	return id
}

func setAppMemoryV2TestRevision(t *testing.T, s *Store, threadID, uid string, revision int64) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO app_memory_subject_revisions(thread_id, uid, revision)
VALUES (?, ?, ?)`, threadID, uid, revision); err != nil {
		t.Fatalf("set Memory V2 test revision: %v", err)
	}
}

func appPromptMemoryTexts(items []AppPromptMemoryItem) []string {
	texts := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, item.Text)
	}
	return texts
}

func TestAppPromptMemoryForSubjectIsolatesThreadAndMember(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	future := now.Add(24 * time.Hour)
	rows := []appMemoryV2TestRow{
		{threadID: "group-a", memoryKey: "group.rule", category: "preference", text: "quy tắc nhóm A", status: "active", createdAt: now.Add(-4 * time.Hour), expiresAt: &future},
		{threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation", category: "profile", text: "u-1 ở nhóm A", status: "active", createdAt: now.Add(-3 * time.Hour), expiresAt: &future},
		{threadID: "group-a", uid: "u-2", memoryKey: "profile.occupation", category: "profile", text: "u-2 ở nhóm A", status: "active", createdAt: now.Add(-2 * time.Hour), expiresAt: &future},
		{threadID: "group-b", uid: "u-1", memoryKey: "profile.occupation", category: "profile", text: "u-1 ở nhóm B", status: "active", createdAt: now.Add(-time.Hour), expiresAt: &future},
	}
	for _, row := range rows {
		insertAppMemoryV2TestRow(t, s, row)
	}
	setAppMemoryV2TestRevision(t, s, "group-a", "", 3)
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 7)
	setAppMemoryV2TestRevision(t, s, "group-a", "u-2", 11)
	setAppMemoryV2TestRevision(t, s, "group-b", "u-1", 13)

	snapshot, err := s.AppPromptMemoryForSubject("group-a", "u-1", 12, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := appPromptMemoryTexts(snapshot.Common); !reflect.DeepEqual(got, []string{"quy tắc nhóm A"}) {
		t.Fatalf("common = %v", got)
	}
	if got := appPromptMemoryTexts(snapshot.Subject); !reflect.DeepEqual(got, []string{"u-1 ở nhóm A"}) {
		t.Fatalf("subject = %v", got)
	}
	if snapshot.CommonRevision != 3 || snapshot.SubjectRevision != 7 {
		t.Fatalf("revisions = common %d subject %d; want 3/7", snapshot.CommonRevision, snapshot.SubjectRevision)
	}
	if snapshot.Subject[0].UID != "u-1" || snapshot.Subject[0].ID == 0 ||
		snapshot.Subject[0].MemoryKey != "profile.occupation" {
		t.Fatalf("subject metadata = %#v", snapshot.Subject[0])
	}
}

func TestAppMemoryScopeAndPolicyRejectWhitespaceDecoratedIdentities(t *testing.T) {
	s := newStore(t)
	seedAppMemoryGroup(t, s)
	if _, err := s.CreateAppThreadMemoryV2("group-a", AppThreadMemoryInput{
		UID: "u-1", MemoryKey: "profile.note", Category: "profile",
		Text: "U1-PRIVATE-MEMORY", ExpectedRevision: 0,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	for _, scope := range []struct {
		threadID string
		uid      string
	}{
		{threadID: "group-a", uid: " u-1 "},
		{threadID: " group-a ", uid: "u-1"},
	} {
		if _, err := s.AppThreadMemoryScope(scope.threadID, scope.uid, now); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("AppThreadMemoryScope(%q, %q) error = %v; want ErrAppMemoryInvalid",
				scope.threadID, scope.uid, err)
		}
		if _, err := s.AppPromptMemoryForSubject(scope.threadID, scope.uid, 12, now); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("AppPromptMemoryForSubject(%q, %q) error = %v; want ErrAppMemoryInvalid",
				scope.threadID, scope.uid, err)
		}
	}

	for _, input := range []AppMemoryApplyInput{
		{
			ThreadID: "group-a", SubjectUID: " u-1 ", Now: now,
			Operations: []AppMemoryOperation{{
				Action: "add", MemoryKey: "profile.decorated", Value: "không được thêm",
				Category: "profile", Confidence: 1,
			}},
		},
		{
			ThreadID: " group-a ", SubjectUID: "u-1", Now: now,
			Operations: []AppMemoryOperation{{
				Action: "add", MemoryKey: "profile.decorated", Value: "không được thêm",
				Category: "profile", Confidence: 1,
			}},
		},
	} {
		if _, err := s.ApplyAppMemoryOperations(input); !errors.Is(err, ErrAppMemoryInvalid) {
			t.Fatalf("ApplyAppMemoryOperations(%q, %q) error = %v; want ErrAppMemoryInvalid",
				input.ThreadID, input.SubjectUID, err)
		}
	}

	detail, err := s.AppThreadMemoryScope("group-a", "u-1", now)
	if err != nil {
		t.Fatal(err)
	}
	if detail.SelectedUID != "u-1" || detail.Revision != 1 || len(detail.Active) != 1 ||
		detail.Active[0].Text != "U1-PRIVATE-MEMORY" {
		t.Fatalf("canonical scope changed = %#v", detail)
	}
}

func TestAppPromptMemoryForSubjectEmptyUIDReturnsCommonOnly(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.rule", category: "preference",
		text: "quy tắc nhóm A", status: "active", createdAt: now.Add(-time.Hour),
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "", 3)

	snapshot, err := s.AppPromptMemoryForSubject("group-a", "", 12, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := appPromptMemoryTexts(snapshot.Common); !reflect.DeepEqual(got, []string{"quy tắc nhóm A"}) {
		t.Fatalf("common = %v", got)
	}
	if snapshot.Subject == nil || len(snapshot.Subject) != 0 {
		t.Fatalf("subject = %#v; want an empty collection for an unknown subject", snapshot.Subject)
	}
	if snapshot.CommonRevision != 3 || snapshot.SubjectRevision != 0 {
		t.Fatalf("revisions = common %d subject %d; want common-only 3/0",
			snapshot.CommonRevision, snapshot.SubjectRevision)
	}
}

func TestAppPromptMemoryForSubjectLegacyWrapperPreservesCreatedAt(t *testing.T) {
	s := newStore(t)
	createdAt := time.Date(2026, 8, 9, 7, 8, 9, 123456789, time.UTC)
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.rule", category: "preference",
		text: "quy tắc nhóm A", status: "active", createdAt: createdAt,
	})

	memory, _, err := s.AppPromptMemory("group-a", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(memory) != 1 || !memory[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("legacy prompt Memory = %#v; want created_at %s", memory, createdAt.Format(time.RFC3339Nano))
	}
}

func TestAppPromptMemoryForSubjectExpiresDueAndDeletesOverduePending(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	due := now.Add(-time.Minute)
	atBoundary := now
	dueIDs := []int64{insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "interest.topic", category: "interest",
		text: "đã hết hạn", status: "active", createdAt: now.Add(-31 * 24 * time.Hour), expiresAt: &due,
	}), insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "interest.boundary", category: "interest",
		text: "hết hạn đúng mốc", status: "active", createdAt: now.Add(-30 * 24 * time.Hour), expiresAt: &atBoundary,
	}), insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", memoryKey: "group.expired", category: "preference",
		text: "quy tắc chung đã hết hạn", status: "active", createdAt: now.Add(-31 * 24 * time.Hour), expiresAt: &due,
	}), insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-b", uid: "u-2", memoryKey: "profile.expired", category: "profile",
		text: "scope khác đã hết hạn", status: "active", createdAt: now.Add(-31 * 24 * time.Hour), expiresAt: &due,
	})}
	pendingID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.pending", category: "profile",
		text: "đề xuất quá hạn", status: "pending", createdAt: now.Add(-31 * 24 * time.Hour),
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 4)
	setAppMemoryV2TestRevision(t, s, "group-a", "", 8)
	setAppMemoryV2TestRevision(t, s, "group-b", "u-2", 10)

	snapshot, err := s.AppPromptMemoryForSubject("group-a", "u-1", 12, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Subject) != 0 {
		t.Fatalf("subject = %#v; want expired and pending rows omitted", snapshot.Subject)
	}
	if snapshot.SubjectRevision != 5 {
		t.Fatalf("subject revision = %d; want 5", snapshot.SubjectRevision)
	}
	if snapshot.CommonRevision != 9 {
		t.Fatalf("common revision = %d; want 9", snapshot.CommonRevision)
	}
	for _, id := range dueIDs {
		var status string
		if err := s.db.QueryRow(`SELECT status FROM zalo_memory WHERE id = ?`, id).Scan(&status); err != nil {
			t.Fatalf("read due Memory %d status: %v", id, err)
		}
		if status != "expired" {
			t.Fatalf("due Memory %d status = %q; want expired", id, status)
		}
	}
	var pendingCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM zalo_memory WHERE id = ?`, pendingID).Scan(&pendingCount); err != nil {
		t.Fatalf("count overdue pending Memory: %v", err)
	}
	if pendingCount != 0 {
		t.Fatalf("overdue pending Memory count = %d; want 0", pendingCount)
	}
	var unrelatedRevision int64
	if err := s.db.QueryRow(`SELECT revision FROM app_memory_subject_revisions
WHERE thread_id = 'group-b' AND uid = 'u-2'`).Scan(&unrelatedRevision); err != nil {
		t.Fatalf("read unrelated subject revision: %v", err)
	}
	if unrelatedRevision != 11 {
		t.Fatalf("unrelated subject revision = %d; want 11", unrelatedRevision)
	}

	again, err := s.AppPromptMemoryForSubject("group-a", "u-1", 12, now)
	if err != nil {
		t.Fatal(err)
	}
	if again.SubjectRevision != 5 || again.CommonRevision != 9 {
		t.Fatalf("second revisions = common %d subject %d; want exactly-once bumps to 9/5",
			again.CommonRevision, again.SubjectRevision)
	}
	if err := s.db.QueryRow(`SELECT revision FROM app_memory_subject_revisions
WHERE thread_id = 'group-b' AND uid = 'u-2'`).Scan(&unrelatedRevision); err != nil {
		t.Fatalf("re-read unrelated subject revision: %v", err)
	}
	if unrelatedRevision != 11 {
		t.Fatalf("second unrelated subject revision = %d; want exactly-once bump to 11", unrelatedRevision)
	}
}

func TestAppPromptMemoryForSubjectBoundsEachScopeIndependently(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	rows := []appMemoryV2TestRow{
		{threadID: "group-a", memoryKey: "common.pinned", category: "profile", text: "common-pinned", status: "active", pinned: true, createdAt: now.Add(-6 * time.Hour)},
		{threadID: "group-a", uid: "u-1", memoryKey: "subject.pinned", category: "profile", text: "subject-pinned", status: "active", pinned: true, createdAt: now.Add(-5 * time.Hour)},
		{threadID: "group-a", memoryKey: "common.old", category: "profile", text: "common-old", status: "active", createdAt: now.Add(-4 * time.Hour)},
		{threadID: "group-a", uid: "u-1", memoryKey: "subject.old", category: "profile", text: "subject-old", status: "active", createdAt: now.Add(-3 * time.Hour)},
		{threadID: "group-a", memoryKey: "common.new", category: "profile", text: "common-new", status: "active", createdAt: now.Add(-2 * time.Hour)},
		{threadID: "group-a", uid: "u-1", memoryKey: "subject.new", category: "profile", text: "subject-new", status: "active", createdAt: now.Add(-time.Hour)},
	}
	for _, row := range rows {
		insertAppMemoryV2TestRow(t, s, row)
	}

	snapshot, err := s.AppPromptMemoryForSubject("group-a", "u-1", 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := appPromptMemoryTexts(snapshot.Common), []string{"common-pinned", "common-new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("common = %v; want %v", got, want)
	}
	if got, want := appPromptMemoryTexts(snapshot.Subject), []string{"subject-pinned", "subject-new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("subject = %v; want %v", got, want)
	}
}
