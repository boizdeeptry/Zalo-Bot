package store

import (
	"database/sql"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

type appMemoryV2StoredRow struct {
	id                                    int64
	memoryKey, category, text, status     string
	proposalAction                        string
	supersedesID                          int64
	pinned                                bool
	confidence                            float64
	source                                string
	sourceMessageID                       int64
	createdAt, updatedAt, lastConfirmedAt string
	expiresAt                             sql.NullString
}

func appMemoryV2StoredRows(t *testing.T, s *Store, threadID, uid string) []appMemoryV2StoredRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT id, memory_key, category, text, status, proposal_action,
supersedes_id, pinned, confidence, source, source_message_id, created_at, updated_at,
last_confirmed_at, expires_at
FROM zalo_memory WHERE thread_id = ? AND uid = ? ORDER BY id`, threadID, uid)
	if err != nil {
		t.Fatalf("list stored Memory V2 rows: %v", err)
	}
	defer rows.Close()
	var out []appMemoryV2StoredRow
	for rows.Next() {
		var row appMemoryV2StoredRow
		if err := rows.Scan(
			&row.id, &row.memoryKey, &row.category, &row.text, &row.status,
			&row.proposalAction, &row.supersedesID, &row.pinned, &row.confidence,
			&row.source, &row.sourceMessageID, &row.createdAt, &row.updatedAt,
			&row.lastConfirmedAt, &row.expiresAt,
		); err != nil {
			t.Fatalf("scan stored Memory V2 row: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list stored Memory V2 rows: %v", err)
	}
	return out
}

func appMemoryV2SubjectRevision(t *testing.T, s *Store, threadID, uid string) int64 {
	t.Helper()
	var revision int64
	if err := s.db.QueryRow(`SELECT COALESCE((SELECT revision
FROM app_memory_subject_revisions WHERE thread_id = ? AND uid = ?), 0)`,
		threadID, uid,
	).Scan(&revision); err != nil {
		t.Fatalf("read Memory V2 subject revision: %v", err)
	}
	return revision
}

func TestApplyAppMemoryOperationsSafeAdd(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	input := AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 41, Now: now,
		Operations: []AppMemoryOperation{{
			Action: "add", MemoryKey: "profile.occupation", Value: "là dược sĩ",
			Category: "profile", Confidence: 0.96,
		}},
	}

	result, err := s.ApplyAppMemoryOperations(input)
	if err != nil {
		t.Fatal(err)
	}
	if want := (AppMemoryApplyResult{Active: 1}); result != want {
		t.Fatalf("apply result = %#v; want %#v", result, want)
	}
	rows := appMemoryV2StoredRows(t, s, "group-a", "u-1")
	if len(rows) != 1 {
		t.Fatalf("stored rows = %#v; want one active row", rows)
	}
	row := rows[0]
	if row.memoryKey != "profile.occupation" || row.category != "profile" ||
		row.text != "là dược sĩ" || row.status != "active" || row.proposalAction != "" ||
		row.supersedesID != 0 || row.source != "agent" || row.sourceMessageID != 41 ||
		row.lastConfirmedAt != ts(now) || !row.expiresAt.Valid ||
		row.expiresAt.String != ts(now.Add(180*24*time.Hour)) {
		t.Fatalf("stored active row = %#v", row)
	}
	if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 1 {
		t.Fatalf("subject revision = %d; want 1", revision)
	}
}

func TestApplyAppMemoryOperationsPolicyBoundaries(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		category   string
		confidence float64
		wantStatus string
		wantExpiry time.Duration
	}{
		{name: "safe profile threshold activates", category: "profile", confidence: 0.85, wantStatus: "active", wantExpiry: 180 * 24 * time.Hour},
		{name: "safe preference threshold activates", category: "preference", confidence: 0.85, wantStatus: "active", wantExpiry: 30 * 24 * time.Hour},
		{name: "uncertain preference waits", category: "preference", confidence: 0.84, wantStatus: "pending"},
		{name: "sensitive health waits", category: "health", confidence: 1, wantStatus: "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
				ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 41, Now: now,
				Operations: []AppMemoryOperation{{
					Action: "add", MemoryKey: "person.fact", Value: "một sự thật",
					Category: tt.category, Confidence: tt.confidence,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			rows := appMemoryV2StoredRows(t, s, "group-a", "u-1")
			if len(rows) != 1 || rows[0].status != tt.wantStatus {
				t.Fatalf("rows = %#v; want one %s row", rows, tt.wantStatus)
			}
			if tt.wantStatus == "active" {
				if result != (AppMemoryApplyResult{Active: 1}) ||
					!rows[0].expiresAt.Valid || rows[0].expiresAt.String != ts(now.Add(tt.wantExpiry)) {
					t.Fatalf("active result/row = %#v / %#v", result, rows[0])
				}
				if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 1 {
					t.Fatalf("subject revision = %d; want 1", revision)
				}
			} else {
				if result != (AppMemoryApplyResult{Pending: 1}) ||
					rows[0].proposalAction != "add" || rows[0].expiresAt.Valid {
					t.Fatalf("pending result/row = %#v / %#v", result, rows[0])
				}
				if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 0 {
					t.Fatalf("pending subject revision = %d; want 0", revision)
				}
			}
		})
	}
}

func TestApplyAppMemoryOperationsDuplicateConfirmsWithoutMultiplying(t *testing.T) {
	s := newStore(t)
	old := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	oldExpiry := now.Add(time.Hour)
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation",
		category: "profile", text: "LÀ   DƯỢC SĨ", status: "active", createdAt: old,
		expiresAt: &oldExpiry, lastConfirmedAt: old,
	})
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "interest.topic",
		category: "interest", text: "Cà Phê", status: "active", pinned: true,
		createdAt: old, lastConfirmedAt: old,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 7)

	result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 42, Now: now,
		Operations: []AppMemoryOperation{
			{Action: "add", MemoryKey: "profile.occupation", Value: "  là\t dược sĩ  ", Category: "profile", Confidence: 0.9},
			{Action: "add", MemoryKey: "interest.topic", Value: "cà phê", Category: "interest", Confidence: 0.9},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (AppMemoryApplyResult{Active: 2}) {
		t.Fatalf("apply result = %#v; want two confirmations", result)
	}
	rows := appMemoryV2StoredRows(t, s, "group-a", "u-1")
	if len(rows) != 2 {
		t.Fatalf("stored rows = %#v; duplicates must not multiply rows", rows)
	}
	if rows[0].text != "LÀ   DƯỢC SĨ" || rows[0].lastConfirmedAt != ts(now) ||
		!rows[0].expiresAt.Valid || rows[0].expiresAt.String != ts(now.Add(180*24*time.Hour)) {
		t.Fatalf("confirmed profile row = %#v", rows[0])
	}
	if rows[1].text != "Cà Phê" || rows[1].lastConfirmedAt != ts(now) || rows[1].expiresAt.Valid {
		t.Fatalf("confirmed pinned row = %#v", rows[1])
	}
	if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 7 {
		t.Fatalf("confirmation revision = %d; want unchanged 7", revision)
	}
}

func TestApplyAppMemoryOperationsConflictKeepsOnePendingReplacement(t *testing.T) {
	s := newStore(t)
	old := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	expires := now.Add(90 * 24 * time.Hour)
	activeID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation",
		category: "profile", text: "là dược sĩ", status: "active", createdAt: old,
		expiresAt: &expires, lastConfirmedAt: old,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 4)
	first, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 51, Now: now,
		Operations: []AppMemoryOperation{{
			Action: "add", MemoryKey: "profile.occupation", Value: "là giáo viên",
			Category: "profile", Confidence: 0.96,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != (AppMemoryApplyResult{Pending: 1}) {
		t.Fatalf("first conflict result = %#v", first)
	}
	rows := appMemoryV2StoredRows(t, s, "group-a", "u-1")
	if len(rows) != 2 || rows[0].id != activeID || rows[0].status != "active" ||
		rows[0].text != "là dược sĩ" || rows[1].status != "pending" ||
		rows[1].proposalAction != "replace" || rows[1].supersedesID != activeID {
		t.Fatalf("rows after conflict = %#v", rows)
	}
	pendingID := rows[1].id
	identicalAt := now.Add(time.Hour)
	identical, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 52, Now: identicalAt,
		Operations: []AppMemoryOperation{{
			Action: "replace", MemoryKey: "profile.occupation", Value: " LÀ  GIÁO VIÊN ",
			Category: "profile", Confidence: 0.88, TargetID: activeID,
		}}, AllowedTargetIDs: map[int64]struct{}{activeID: {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if identical != (AppMemoryApplyResult{Pending: 1}) {
		t.Fatalf("identical proposal result = %#v", identical)
	}
	rows = appMemoryV2StoredRows(t, s, "group-a", "u-1")
	if len(rows) != 2 || rows[1].id != pendingID || rows[1].text != "LÀ  GIÁO VIÊN" ||
		rows[1].sourceMessageID != 52 || rows[1].confidence != 0.88 ||
		rows[1].createdAt != ts(identicalAt) || rows[1].updatedAt != ts(identicalAt) {
		t.Fatalf("refreshed identical proposal = %#v", rows)
	}

	competingAt := now.Add(2 * time.Hour)
	competing, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 53, Now: competingAt,
		Operations: []AppMemoryOperation{{
			Action: "replace", MemoryKey: "profile.occupation", Value: "là kỹ sư",
			Category: "profile", Confidence: 0.91, TargetID: activeID,
		}}, AllowedTargetIDs: map[int64]struct{}{activeID: {}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if competing != (AppMemoryApplyResult{Pending: 1}) {
		t.Fatalf("competing proposal result = %#v", competing)
	}
	rows = appMemoryV2StoredRows(t, s, "group-a", "u-1")
	if len(rows) != 2 || rows[1].id != pendingID || rows[1].text != "là kỹ sư" ||
		rows[1].sourceMessageID != 53 || rows[1].createdAt != ts(competingAt) {
		t.Fatalf("competing proposal rows = %#v", rows)
	}
	if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 4 {
		t.Fatalf("conflict revision = %d; want unchanged 4", revision)
	}
}

func TestApplyAppMemoryOperationsProcessesOnlyFirstThree(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 41, Now: now,
		Operations: []AppMemoryOperation{
			{Action: "add", MemoryKey: "profile.one", Value: "một", Category: "profile", Confidence: 0.9},
			{Action: "add", MemoryKey: "profile.two", Value: "hai", Category: "profile", Confidence: 0.9},
			{Action: "add", MemoryKey: "profile.three", Value: "ba", Category: "profile", Confidence: 0.9},
			{Action: "add", MemoryKey: "profile.four", Value: "bốn", Category: "profile", Confidence: 0.9},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (AppMemoryApplyResult{Active: 3}) {
		t.Fatalf("apply result = %#v; want only three active operations", result)
	}
	rows := appMemoryV2StoredRows(t, s, "group-a", "u-1")
	if len(rows) != 3 || rows[0].memoryKey != "profile.one" ||
		rows[1].memoryKey != "profile.two" || rows[2].memoryKey != "profile.three" {
		t.Fatalf("stored capped operations = %#v", rows)
	}
	if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 1 {
		t.Fatalf("batch subject revision = %d; want one bump", revision)
	}
}

func TestApplyAppMemoryOperationsRejectsEmptySubject(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "", SourceMessageID: 41, Now: now,
		Operations: []AppMemoryOperation{
			{Action: "add", MemoryKey: "profile.one", Value: "một", Category: "profile", Confidence: 0.9},
			{Action: "add", MemoryKey: "profile.two", Value: "hai", Category: "profile", Confidence: 0.9},
			{Action: "add", MemoryKey: "profile.three", Value: "ba", Category: "profile", Confidence: 0.9},
			{Action: "add", MemoryKey: "profile.four", Value: "bốn", Category: "profile", Confidence: 0.9},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (AppMemoryApplyResult{Ignored: 3}) {
		t.Fatalf("empty-subject result = %#v; want first three operations ignored", result)
	}
	if rows := appMemoryV2StoredRows(t, s, "group-a", ""); len(rows) != 0 {
		t.Fatalf("empty subject created common Memory rows = %#v", rows)
	}
	if revision := appMemoryV2SubjectRevision(t, s, "group-a", ""); revision != 0 {
		t.Fatalf("common revision = %d; want unchanged 0", revision)
	}
}

func TestApplyAppMemoryOperationsRejectsInvalidOperations(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		op   AppMemoryOperation
	}{
		{name: "action", op: AppMemoryOperation{Action: "update", MemoryKey: "profile.fact", Value: "x", Category: "profile", Confidence: 0.9}},
		{name: "add target", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "x", Category: "profile", Confidence: 0.9, TargetID: 1}},
		{name: "replace target", op: AppMemoryOperation{Action: "replace", MemoryKey: "profile.fact", Value: "x", Category: "profile", Confidence: 0.9}},
		{name: "forget target", op: AppMemoryOperation{Action: "forget", MemoryKey: "profile.fact", Category: "profile", Confidence: 0.9}},
		{name: "forget value", op: AppMemoryOperation{Action: "forget", MemoryKey: "profile.fact", Value: "x", Category: "profile", Confidence: 0.9, TargetID: 1}},
		{name: "uppercase key", op: AppMemoryOperation{Action: "add", MemoryKey: "Profile.fact", Value: "x", Category: "profile", Confidence: 0.9}},
		{name: "key over 80 bytes", op: AppMemoryOperation{Action: "add", MemoryKey: strings.Repeat("a", 81), Value: "x", Category: "profile", Confidence: 0.9}},
		{name: "category", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "x", Category: "secret", Confidence: 0.9}},
		{name: "nan confidence", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "x", Category: "profile", Confidence: math.NaN()}},
		{name: "infinite confidence", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "x", Category: "profile", Confidence: math.Inf(1)}},
		{name: "negative confidence", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "x", Category: "profile", Confidence: -0.01}},
		{name: "high confidence", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "x", Category: "profile", Confidence: 1.01}},
		{name: "empty value", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "  ", Category: "profile", Confidence: 0.9}},
		{name: "multiline value", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "line one\nline two", Category: "profile", Confidence: 0.9}},
		{name: "next line value", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "line one\u0085line two", Category: "profile", Confidence: 0.9}},
		{name: "line separator value", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "line one\u2028line two", Category: "profile", Confidence: 0.9}},
		{name: "paragraph separator value", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: "line one\u2029line two", Category: "profile", Confidence: 0.9}},
		{name: "invalid UTF-8 value", op: AppMemoryOperation{Action: "add", MemoryKey: "profile.fact", Value: string([]byte{0xff}), Category: "profile", Confidence: 0.9}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			otherID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
				threadID: "group-b", uid: "u-1", memoryKey: "profile.fact",
				category: "profile", text: "other scope", status: "active", createdAt: now,
			})
			result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
				ThreadID: "group-a", SubjectUID: "u-1", Now: now,
				Operations: []AppMemoryOperation{tt.op},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result != (AppMemoryApplyResult{Ignored: 1}) {
				t.Fatalf("apply result = %#v; want one ignored", result)
			}
			if rows := appMemoryV2StoredRows(t, s, "group-a", "u-1"); len(rows) != 0 {
				t.Fatalf("invalid operation created rows = %#v", rows)
			}
			rows := appMemoryV2StoredRows(t, s, "group-b", "u-1")
			if len(rows) != 1 || rows[0].id != otherID || rows[0].text != "other scope" {
				t.Fatalf("invalid operation mutated another scope = %#v", rows)
			}
		})
	}
	s := newStore(t)
	if _, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{ThreadID: "  ", Now: now}); !errors.Is(err, ErrAppMemoryInvalid) {
		t.Fatalf("blank scope error = %v; want ErrAppMemoryInvalid", err)
	}
}

func TestApplyAppMemoryOperationsValueRuneBoundary(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		value      string
		wantResult AppMemoryApplyResult
		wantRows   int
	}{
		{name: "240 runes accepted", value: strings.Repeat("ạ", 240), wantResult: AppMemoryApplyResult{Active: 1}, wantRows: 1},
		{name: "241 runes ignored", value: strings.Repeat("ạ", 241), wantResult: AppMemoryApplyResult{Ignored: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
				ThreadID: "group-a", SubjectUID: "u-1", Now: now,
				Operations: []AppMemoryOperation{{
					Action: "add", MemoryKey: "profile.fact", Value: tt.value,
					Category: "profile", Confidence: 0.9,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result != tt.wantResult {
				t.Fatalf("apply result = %#v; want %#v", result, tt.wantResult)
			}
			if rows := appMemoryV2StoredRows(t, s, "group-a", "u-1"); len(rows) != tt.wantRows {
				t.Fatalf("stored rows = %#v; want %d", rows, tt.wantRows)
			}
		})
	}
}

func TestApplyAppMemoryOperationsRequiresAuthoritativeReplaceTarget(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		threadID  string
		uid       string
		allowed   bool
		wantApply bool
	}{
		{name: "allowed exact scope", threadID: "group-a", uid: "u-1", allowed: true, wantApply: true},
		{name: "not in snapshot", threadID: "group-a", uid: "u-1"},
		{name: "different thread", threadID: "group-b", uid: "u-1", allowed: true},
		{name: "different subject", threadID: "group-a", uid: "u-2", allowed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			targetID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
				threadID: tt.threadID, uid: tt.uid, memoryKey: "profile.occupation",
				category: "profile", text: "là dược sĩ", status: "active", createdAt: now,
			})
			allowed := map[int64]struct{}{}
			if tt.allowed {
				allowed[targetID] = struct{}{}
			}
			result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
				ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 42,
				AllowedTargetIDs: allowed, Now: now,
				Operations: []AppMemoryOperation{{
					Action: "replace", MemoryKey: "profile.occupation", Value: "là giáo viên",
					Category: "profile", Confidence: 1, TargetID: targetID,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantApply {
				if result != (AppMemoryApplyResult{Pending: 1}) {
					t.Fatalf("allowed replace result = %#v", result)
				}
				rows := appMemoryV2StoredRows(t, s, "group-a", "u-1")
				if len(rows) != 2 || rows[1].proposalAction != "replace" || rows[1].supersedesID != targetID {
					t.Fatalf("allowed replace rows = %#v", rows)
				}
			} else if result != (AppMemoryApplyResult{Ignored: 1}) {
				t.Fatalf("rejected replace result = %#v", result)
			}
		})
	}
}

func TestApplyAppMemoryOperationsForgetDeletesExactLineage(t *testing.T) {
	s := newStore(t)
	old := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	seedAppMemoryThread(t, s, "group-a", "Nhóm A")
	messageID := seedAppMemoryInbound(t, s, "group-a", "hãy quên nghề nghiệp của tôi")
	ancestorID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation.old",
		category: "profile", text: "từng là giáo viên", status: "superseded", createdAt: old,
		sourceMessageID: messageID,
	})
	activeID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation.current",
		category: "profile", text: "là dược sĩ", status: "active", createdAt: old,
		supersedesID: ancestorID,
	})
	insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation.proposed",
		category: "profile", text: "là kỹ sư", status: "pending", proposalAction: "replace",
		supersedesID: activeID, createdAt: old,
	})
	disconnectedID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation.current",
		category: "profile", text: "unrelated expired fact", status: "expired", createdAt: old,
	})
	otherID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "interest.topic",
		category: "interest", text: "cà phê", status: "active", createdAt: old,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 9)
	result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: messageID,
		AllowedTargetIDs: map[int64]struct{}{activeID: {}}, Now: now,
		Operations: []AppMemoryOperation{{
			Action: "forget", MemoryKey: "profile.occupation.current", Value: "",
			Category: "profile", Confidence: 1, TargetID: activeID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (AppMemoryApplyResult{Active: 1}) {
		t.Fatalf("forget result = %#v; want one immediately applied operation", result)
	}
	rows := appMemoryV2StoredRows(t, s, "group-a", "u-1")
	if len(rows) != 2 || rows[0].id != disconnectedID || rows[1].id != otherID {
		t.Fatalf("rows after forgetting lineage = %#v", rows)
	}
	if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 10 {
		t.Fatalf("forget revision = %d; want 10", revision)
	}
	var messages int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM zalo_messages WHERE id = ?`, messageID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if messages != 1 {
		t.Fatalf("source message count = %d; want preserved", messages)
	}
	stale, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", AllowedTargetIDs: map[int64]struct{}{activeID: {}},
		Now: now.Add(time.Minute), Operations: []AppMemoryOperation{{
			Action: "forget", MemoryKey: "profile.occupation.current", Category: "profile",
			Confidence: 1, TargetID: activeID,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale != (AppMemoryApplyResult{}) || appMemoryV2SubjectRevision(t, s, "group-a", "u-1") != 10 {
		t.Fatalf("stale forget result/revision = %#v/%d; want no-op/10",
			stale, appMemoryV2SubjectRevision(t, s, "group-a", "u-1"))
	}
}

func TestApplyAppMemoryOperationsConcurrentCallsPreserveInvariants(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	activeID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
		threadID: "group-a", uid: "u-1", memoryKey: "profile.occupation",
		category: "profile", text: "là dược sĩ", status: "active", createdAt: now,
	})
	setAppMemoryV2TestRevision(t, s, "group-a", "u-1", 6)

	operations := []AppMemoryOperation{
		{Action: "add", MemoryKey: "profile.occupation", Value: " LÀ  DƯỢC SĨ ", Category: "profile", Confidence: 0.9},
		{Action: "add", MemoryKey: "profile.occupation", Value: "là giáo viên", Category: "profile", Confidence: 0.9},
		{Action: "add", MemoryKey: "profile.occupation", Value: "là\tdược sĩ", Category: "profile", Confidence: 0.9},
		{Action: "add", MemoryKey: "profile.occupation", Value: "là kỹ sư", Category: "profile", Confidence: 0.9},
		{Action: "add", MemoryKey: "profile.occupation", Value: "LÀ DƯỢC SĨ", Category: "profile", Confidence: 0.9},
		{Action: "add", MemoryKey: "profile.occupation", Value: "là kiến trúc sư", Category: "profile", Confidence: 0.9},
	}
	start := make(chan struct{})
	type applyResult struct {
		result AppMemoryApplyResult
		err    error
	}
	results := make(chan applyResult, len(operations))
	var workers sync.WaitGroup
	for _, operation := range operations {
		operation := operation
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
				ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 41,
				Now: now, Operations: []AppMemoryOperation{operation},
			})
			results <- applyResult{result: result, err: err}
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var activeResults, pendingResults int
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent ApplyAppMemoryOperations: %v", result.err)
		}
		activeResults += result.result.Active
		pendingResults += result.result.Pending
	}
	if activeResults != 3 || pendingResults != 3 {
		t.Fatalf("concurrent result totals = active %d pending %d; want 3/3", activeResults, pendingResults)
	}
	rows := appMemoryV2StoredRows(t, s, "group-a", "u-1")
	if len(rows) != 2 || rows[0].id != activeID || rows[0].status != "active" ||
		rows[0].text != "là dược sĩ" || rows[1].status != "pending" ||
		rows[1].proposalAction != "replace" || rows[1].supersedesID != activeID {
		t.Fatalf("rows after concurrent calls = %#v", rows)
	}
	if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 6 {
		t.Fatalf("revision after confirmations/conflicts = %d; want unchanged 6", revision)
	}
}

func TestApplyAppMemoryOperationsRejectsUnauthorizedForgetTargets(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		threadID string
		uid      string
		allowed  bool
	}{
		{name: "not in snapshot", threadID: "group-a", uid: "u-1"},
		{name: "different thread", threadID: "group-b", uid: "u-1", allowed: true},
		{name: "different subject", threadID: "group-a", uid: "u-2", allowed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			targetID := insertAppMemoryV2TestRow(t, s, appMemoryV2TestRow{
				threadID: tt.threadID, uid: tt.uid, memoryKey: "profile.occupation",
				category: "profile", text: "là dược sĩ", status: "active", createdAt: now,
			})
			allowed := map[int64]struct{}{}
			if tt.allowed {
				allowed[targetID] = struct{}{}
			}
			result, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
				ThreadID: "group-a", SubjectUID: "u-1", AllowedTargetIDs: allowed, Now: now,
				Operations: []AppMemoryOperation{{
					Action: "forget", MemoryKey: "profile.occupation", Category: "profile",
					Confidence: 1, TargetID: targetID,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result != (AppMemoryApplyResult{Ignored: 1}) {
				t.Fatalf("unauthorized forget result = %#v", result)
			}
			rows := appMemoryV2StoredRows(t, s, tt.threadID, tt.uid)
			if len(rows) != 1 || rows[0].id != targetID {
				t.Fatalf("unauthorized forget mutated target = %#v", rows)
			}
		})
	}
}

func TestApplyAppMemoryOperationsRollsBackBatchOnSQLError(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if _, err := s.db.Exec(`CREATE TRIGGER app_memory_v2_test_abort
BEFORE INSERT ON zalo_memory WHEN NEW.memory_key = 'profile.fail'
BEGIN SELECT RAISE(ABORT, 'forced Memory policy failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.ApplyAppMemoryOperations(AppMemoryApplyInput{
		ThreadID: "group-a", SubjectUID: "u-1", SourceMessageID: 41, Now: now,
		Operations: []AppMemoryOperation{
			{Action: "add", MemoryKey: "profile.good", Value: "must roll back", Category: "profile", Confidence: 0.9},
			{Action: "add", MemoryKey: "profile.fail", Value: "forces SQL error", Category: "profile", Confidence: 0.9},
		},
	})
	if err == nil {
		t.Fatal("ApplyAppMemoryOperations returned nil after forced SQL failure")
	}
	if rows := appMemoryV2StoredRows(t, s, "group-a", "u-1"); len(rows) != 0 {
		t.Fatalf("rows survived rollback = %#v", rows)
	}
	if revision := appMemoryV2SubjectRevision(t, s, "group-a", "u-1"); revision != 0 {
		t.Fatalf("revision survived rollback = %d", revision)
	}
}
