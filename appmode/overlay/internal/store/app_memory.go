package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentdc/internal/ipc"
)

const (
	appThreadMemoryLimit = 100
	appLessonListLimit   = 100
	appOverviewLimit     = 200
	appMemoryPinLimit    = 12
	appLessonPinLimit    = 8
)

var (
	ErrAppMemoryNotFound = errors.New("memory not found")
	ErrAppMemoryPinLimit = errors.New("memory pin limit reached")
	ErrAppMemoryInvalid  = errors.New("invalid memory input")
	ErrAppMemoryConflict = errors.New("memory revision conflict")
)

type AppMemoryRevision struct {
	Memory  int64 `json:"memory"`
	Lessons int64 `json:"lessons"`
}

type AppMemoryMetrics struct {
	Memories int `json:"memories"`
	Threads  int `json:"threads"`
	Lessons  int `json:"lessons"`
}

type AppMemoryThread struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Avatar      string    `json:"avatar"`
	ThreadType  string    `json:"thread_type"`
	MemoryCount int       `json:"memory_count"`
	PinnedCount int       `json:"pinned_count"`
	Preview     string    `json:"preview"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AppMemoryOverview struct {
	Metrics AppMemoryMetrics  `json:"metrics"`
	Threads []AppMemoryThread `json:"threads"`
}

type AppThreadMemoryInput struct {
	UID              string `json:"uid"`
	MemoryKey        string `json:"memory_key"`
	Text             string `json:"text"`
	Category         string `json:"category"`
	Pinned           bool   `json:"pinned"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type AppThreadMemory struct {
	ID              int64            `json:"id"`
	ThreadID        string           `json:"thread_id"`
	ThreadName      string           `json:"thread_name"`
	UID             string           `json:"uid"`
	MemoryKey       string           `json:"memory_key"`
	Text            string           `json:"text"`
	Category        string           `json:"category"`
	Confidence      float64          `json:"confidence"`
	Status          string           `json:"status"`
	ProposalAction  string           `json:"proposal_action"`
	SupersedesID    int64            `json:"supersedes_id"`
	ProposalTarget  *AppThreadMemory `json:"proposal_target,omitempty"`
	Pinned          bool             `json:"pinned"`
	Source          string           `json:"source"`
	SourceMessageID int64            `json:"source_message_id"`
	SourcePreview   string           `json:"source_preview"`
	LastConfirmedAt *time.Time       `json:"last_confirmed_at,omitempty"`
	ExpiresAt       *time.Time       `json:"expires_at,omitempty"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type AppMemoryScopeCounts struct {
	Active  int `json:"active"`
	Pending int `json:"pending"`
	Expired int `json:"expired"`
}

type AppMemoryMember struct {
	UID    string `json:"uid"`
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
	AppMemoryScopeCounts
}

type AppThreadMemoryDetail struct {
	Thread                AppMemoryThread      `json:"thread"`
	SelectedUID           string               `json:"selected_uid"`
	Members               []AppMemoryMember    `json:"members"`
	Common                AppMemoryScopeCounts `json:"common"`
	CommonRevision        int64                `json:"common_revision"`
	SubjectRevision       int64                `json:"subject_revision"`
	SyncedCommonRevision  int64                `json:"synced_common_revision"`
	SyncedSubjectRevision int64                `json:"synced_subject_revision"`
	SessionSubjectUID     string               `json:"session_subject_uid"`
	Synced                bool                 `json:"synced"`
	Active                []AppThreadMemory    `json:"active"`
	Pending               []AppThreadMemory    `json:"pending"`
	Expired               []AppThreadMemory    `json:"expired"`
	Revision              int64                `json:"revision"`
	SyncedRevision        int64                `json:"synced_revision"`
	Memories              []AppThreadMemory    `json:"memories"`
}

type AppLessonInput struct {
	ThreadID string `json:"thread_id"`
	BotText  string `json:"bot_text"`
	Better   string `json:"better"`
	Note     string `json:"note"`
	Pinned   bool   `json:"pinned"`
}

type AppLesson struct {
	ID         int64     `json:"id"`
	ThreadID   string    `json:"thread_id"`
	ThreadName string    `json:"thread_name"`
	BotText    string    `json:"bot_text"`
	Better     string    `json:"better"`
	Note       string    `json:"note"`
	Pinned     bool      `json:"pinned"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type AppLessonList struct {
	Revision int64       `json:"revision"`
	Lessons  []AppLesson `json:"lessons"`
}

func (s *Store) AppMemoryOverview(query string, pinnedOnly bool) (AppMemoryOverview, error) {
	var out AppMemoryOverview
	if err := s.db.QueryRow(`SELECT
  (SELECT COUNT(*) FROM zalo_memory),
  (SELECT COUNT(DISTINCT thread_id) FROM zalo_memory),
  (SELECT COUNT(*) FROM zalo_lessons)`).Scan(
		&out.Metrics.Memories,
		&out.Metrics.Threads,
		&out.Metrics.Lessons,
	); err != nil {
		return AppMemoryOverview{}, fmt.Errorf("read memory metrics: %w", err)
	}

	trimmed := strings.TrimSpace(query)
	pattern := appMemoryLikePattern(trimmed)
	pinned := 0
	if pinnedOnly {
		pinned = 1
	}
	now := ts(time.Now())
	rows, err := s.db.Query(`
SELECT t.id, t.name, t.avatar, t.thread_type,
       (SELECT COUNT(*) FROM zalo_memory m WHERE m.thread_id = t.id),
       (SELECT COUNT(*) FROM zalo_memory m
        WHERE m.thread_id = t.id AND m.pinned = 1 AND m.status = 'active'
          AND (m.expires_at IS NULL OR m.expires_at > ?)
          AND ((t.thread_type = 'group' AND m.uid = '') OR
               (t.thread_type = 'user' AND m.uid = t.id))),
       COALESCE((SELECT m.text FROM zalo_memory m
                 WHERE m.thread_id = t.id AND m.status = 'active'
                   AND (m.expires_at IS NULL OR m.expires_at > ?)
                   AND ((t.thread_type = 'group' AND m.uid = '') OR
                        (t.thread_type = 'user' AND m.uid = t.id))
                 ORDER BY m.pinned DESC, m.id DESC LIMIT 1), ''),
       t.updated_at
FROM zalo_threads t
WHERE (? = '' OR t.name LIKE ? ESCAPE '\' COLLATE NOCASE OR
       EXISTS (SELECT 1 FROM zalo_memory m
               WHERE m.thread_id = t.id AND m.text LIKE ? ESCAPE '\' COLLATE NOCASE
                 AND m.status = 'active' AND (m.expires_at IS NULL OR m.expires_at > ?)
                 AND ((t.thread_type = 'group' AND m.uid = '') OR
                      (t.thread_type = 'user' AND m.uid = t.id))))
  AND (? = 0 OR EXISTS (SELECT 1 FROM zalo_memory m
                        WHERE m.thread_id = t.id AND m.pinned = 1 AND m.status = 'active'
                          AND (m.expires_at IS NULL OR m.expires_at > ?)
                          AND ((t.thread_type = 'group' AND m.uid = '') OR
                               (t.thread_type = 'user' AND m.uid = t.id))))
ORDER BY CASE WHEN EXISTS (SELECT 1 FROM zalo_memory m
                           WHERE m.thread_id = t.id AND m.status = 'active'
                             AND (m.expires_at IS NULL OR m.expires_at > ?)
                             AND ((t.thread_type = 'group' AND m.uid = '') OR
                                  (t.thread_type = 'user' AND m.uid = t.id)))
              THEN 0 ELSE 1 END,
         t.updated_at DESC, t.id ASC
LIMIT ?`, now, now, trimmed, pattern, pattern, now, pinned, now, now, appOverviewLimit)
	if err != nil {
		return AppMemoryOverview{}, fmt.Errorf("list memory threads: %w", err)
	}
	defer rows.Close()
	out.Threads = make([]AppMemoryThread, 0)
	for rows.Next() {
		var thread AppMemoryThread
		var updatedAt string
		if err := rows.Scan(
			&thread.ID, &thread.Name, &thread.Avatar, &thread.ThreadType,
			&thread.MemoryCount, &thread.PinnedCount, &thread.Preview, &updatedAt,
		); err != nil {
			return AppMemoryOverview{}, fmt.Errorf("scan memory thread: %w", err)
		}
		thread.UpdatedAt, err = parseTS(updatedAt)
		if err != nil {
			return AppMemoryOverview{}, fmt.Errorf("parse memory thread %s updated_at: %w", thread.ID, err)
		}
		out.Threads = append(out.Threads, thread)
	}
	if err := rows.Err(); err != nil {
		return AppMemoryOverview{}, fmt.Errorf("list memory threads: %w", err)
	}
	return out, nil
}

func (s *Store) AppThreadMemories(threadID string) (AppThreadMemoryDetail, error) {
	out, err := s.AppThreadMemoryScope(threadID, "", time.Now())
	if err != nil {
		return AppThreadMemoryDetail{}, err
	}
	if err := s.db.QueryRow(`SELECT
  COALESCE((SELECT revision FROM app_memory_revisions
            WHERE scope = 'thread' AND scope_id = ?), 0),
  COALESCE((SELECT memory_revision FROM app_zalo_cli_sessions
            WHERE thread_id = ?), 0)`, threadID, threadID).Scan(
		&out.Revision, &out.SyncedRevision,
	); err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("read legacy Memory revisions for %s: %w", threadID, err)
	}
	uid := ""
	includeLegacyCommon := false
	if out.Thread.ThreadType == ipc.ZaloThreadUser {
		uid = threadID
		includeLegacyCommon = true
	}
	rows, err := s.db.Query(appThreadMemorySelect+`
WHERE m.thread_id = ? AND m.status = 'active'
  AND (m.uid = ? OR (? = 1 AND m.uid = ''))
  AND (m.expires_at IS NULL OR m.expires_at > ?)
ORDER BY m.pinned DESC, m.id DESC LIMIT ?`,
		threadID, uid, includeLegacyCommon, ts(time.Now()), appThreadMemoryLimit)
	if err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("list legacy Memory for %s: %w", threadID, err)
	}
	defer rows.Close()
	out.Memories = make([]AppThreadMemory, 0)
	for rows.Next() {
		memory, err := scanAppThreadMemory(rows)
		if err != nil {
			return AppThreadMemoryDetail{}, err
		}
		out.Memories = append(out.Memories, memory)
	}
	if err := rows.Err(); err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("list legacy Memory for %s: %w", threadID, err)
	}
	out.Active = out.Memories
	out.Thread.MemoryCount = len(out.Memories)
	out.Thread.PinnedCount = 0
	for _, memory := range out.Memories {
		if memory.Pinned {
			out.Thread.PinnedCount++
		}
	}
	if len(out.Memories) > 0 {
		out.Thread.Preview = out.Memories[0].Text
	}
	return out, nil
}

func (s *Store) AppThreadMemoryScope(
	threadID, selectedUID string,
	now time.Time,
) (AppThreadMemoryDetail, error) {
	threadID, selectedUID, err := validateAppPromptMemoryScope(threadID, selectedUID)
	if err != nil {
		return AppThreadMemoryDetail{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("begin scoped Memory read for %s: %w", threadID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var out AppThreadMemoryDetail
	var updatedAt string
	if err := tx.QueryRow(`SELECT id, name, avatar, thread_type, updated_at
FROM zalo_threads WHERE id = ?`, threadID).Scan(
		&out.Thread.ID, &out.Thread.Name, &out.Thread.Avatar,
		&out.Thread.ThreadType, &updatedAt,
	); errors.Is(err, sql.ErrNoRows) {
		return AppThreadMemoryDetail{}, fmt.Errorf("thread %s: %w", threadID, ErrAppMemoryNotFound)
	} else if err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("read Memory thread %s: %w", threadID, err)
	}
	out.Thread.UpdatedAt, err = parseTS(updatedAt)
	if err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("parse Memory thread %s updated_at: %w", threadID, err)
	}
	if out.Thread.ThreadType == ipc.ZaloThreadUser {
		selectedUID = threadID
	}
	out.SelectedUID = selectedUID

	if err := appExpirePromptMemory(tx, now); err != nil {
		return AppThreadMemoryDetail{}, err
	}
	out.Common, err = appReadMemoryScopeCounts(tx, threadID, "")
	if err != nil {
		return AppThreadMemoryDetail{}, err
	}
	out.Members, err = appReadMemoryMembers(tx, out.Thread, selectedUID)
	if err != nil {
		return AppThreadMemoryDetail{}, err
	}
	if err := tx.QueryRow(`SELECT
  COALESCE((SELECT revision FROM app_memory_subject_revisions
            WHERE thread_id = ? AND uid = ''), 0),
  COALESCE((SELECT revision FROM app_memory_subject_revisions
            WHERE thread_id = ? AND uid = ?), 0)`,
		threadID, threadID, selectedUID,
	).Scan(&out.CommonRevision, &out.SubjectRevision); err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("read scoped Memory revisions for %s: %w", threadID, err)
	}
	if selectedUID == "" {
		out.SubjectRevision = 0
	}
	if err := tx.QueryRow(`SELECT
  COALESCE((SELECT memory_subject_uid FROM app_zalo_cli_sessions WHERE thread_id = ?), ''),
  COALESCE((SELECT memory_subject_revision FROM app_zalo_cli_sessions WHERE thread_id = ?), 0),
  COALESCE((SELECT memory_common_revision FROM app_zalo_cli_sessions WHERE thread_id = ?), 0)`,
		threadID, threadID, threadID,
	).Scan(&out.SessionSubjectUID, &out.SyncedSubjectRevision, &out.SyncedCommonRevision); err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("read scoped Memory sync for %s: %w", threadID, err)
	}
	if selectedUID == "" {
		out.Synced = out.SyncedCommonRevision == out.CommonRevision
	} else {
		out.Synced = out.SessionSubjectUID == selectedUID &&
			out.SyncedSubjectRevision == out.SubjectRevision &&
			out.SyncedCommonRevision == out.CommonRevision
	}

	out.Active, err = appReadThreadMemorySection(tx, threadID, selectedUID, "active")
	if err != nil {
		return AppThreadMemoryDetail{}, err
	}
	out.Pending, err = appReadThreadMemorySection(tx, threadID, selectedUID, "pending")
	if err != nil {
		return AppThreadMemoryDetail{}, err
	}
	out.Expired, err = appReadThreadMemorySection(tx, threadID, selectedUID, "expired")
	if err != nil {
		return AppThreadMemoryDetail{}, err
	}
	targets, err := appReadProposalTargets(tx, threadID, selectedUID)
	if err != nil {
		return AppThreadMemoryDetail{}, err
	}
	for i := range out.Pending {
		target, ok := targets[out.Pending[i].SupersedesID]
		if ok {
			out.Pending[i].ProposalTarget = &target
		}
	}
	out.Thread.MemoryCount = len(out.Active) + len(out.Pending) + len(out.Expired)
	for _, memory := range append(append(
		append([]AppThreadMemory(nil), out.Active...), out.Pending...), out.Expired...) {
		if memory.Pinned {
			out.Thread.PinnedCount++
		}
	}
	if len(out.Active) > 0 {
		out.Thread.Preview = out.Active[0].Text
	}
	out.Revision = out.SubjectRevision
	out.SyncedRevision = out.SyncedSubjectRevision
	if selectedUID == "" {
		out.Revision = out.CommonRevision
		out.SyncedRevision = out.SyncedCommonRevision
	}
	out.Memories = out.Active
	if err := tx.Commit(); err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("commit scoped Memory read for %s: %w", threadID, err)
	}
	return out, nil
}

func appReadMemoryScopeCounts(tx *sql.Tx, threadID, uid string) (AppMemoryScopeCounts, error) {
	var out AppMemoryScopeCounts
	if err := tx.QueryRow(`SELECT
  COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'expired' THEN 1 ELSE 0 END), 0)
FROM zalo_memory WHERE thread_id = ? AND uid = ?`, threadID, uid).Scan(
		&out.Active, &out.Pending, &out.Expired,
	); err != nil {
		return AppMemoryScopeCounts{}, fmt.Errorf("count scoped Memory for %s: %w", threadID, err)
	}
	return out, nil
}

func appReadMemoryMembers(
	tx *sql.Tx,
	thread AppMemoryThread,
	selectedUID string,
) ([]AppMemoryMember, error) {
	if thread.ThreadType == ipc.ZaloThreadUser {
		counts, err := appReadMemoryScopeCounts(tx, thread.ID, thread.ID)
		if err != nil {
			return nil, err
		}
		member := AppMemoryMember{UID: thread.ID, Name: thread.Name, Avatar: thread.Avatar}
		member.AppMemoryScopeCounts = counts
		if err := tx.QueryRow(`SELECT
  COALESCE(NULLIF(display_name, ''), NULLIF(zalo_name, ''), ?),
  COALESCE(NULLIF(avatar, ''), ?)
FROM zalo_users WHERE uid = ?`, thread.Name, thread.Avatar, thread.ID).Scan(
			&member.Name, &member.Avatar,
		); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("read private Memory member %s: %w", thread.ID, err)
		}
		return []AppMemoryMember{member}, nil
	}

	rows, err := tx.Query(`SELECT gm.uid,
       COALESCE(NULLIF(u.display_name, ''), NULLIF(u.zalo_name, ''), gm.uid),
       COALESCE(u.avatar, ''),
       COALESCE(SUM(CASE WHEN m.status = 'active' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN m.status = 'pending' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN m.status = 'expired' THEN 1 ELSE 0 END), 0)
FROM zalo_group_members gm
LEFT JOIN zalo_users u ON u.uid = gm.uid
LEFT JOIN zalo_memory m ON m.thread_id = gm.group_id AND m.uid = gm.uid
WHERE gm.group_id = ?
GROUP BY gm.uid, u.display_name, u.zalo_name, u.avatar
ORDER BY gm.uid ASC`, thread.ID)
	if err != nil {
		return nil, fmt.Errorf("list Memory members for %s: %w", thread.ID, err)
	}
	defer rows.Close()
	members := make([]AppMemoryMember, 0)
	for rows.Next() {
		var member AppMemoryMember
		if err := rows.Scan(
			&member.UID, &member.Name, &member.Avatar,
			&member.Active, &member.Pending, &member.Expired,
		); err != nil {
			return nil, fmt.Errorf("scan Memory member for %s: %w", thread.ID, err)
		}
		members = append(members, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Memory members for %s: %w", thread.ID, err)
	}
	return members, nil
}

func appReadThreadMemorySection(
	tx *sql.Tx,
	threadID, uid, status string,
) ([]AppThreadMemory, error) {
	rows, err := tx.Query(appThreadMemorySelect+`
WHERE m.thread_id = ? AND m.uid = ? AND m.status = ?
ORDER BY m.pinned DESC, m.id DESC LIMIT ?`, threadID, uid, status, appThreadMemoryLimit)
	if err != nil {
		return nil, fmt.Errorf("list %s Memory for %s: %w", status, threadID, err)
	}
	defer rows.Close()
	out := make([]AppThreadMemory, 0)
	for rows.Next() {
		memory, err := scanAppThreadMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, memory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %s Memory for %s: %w", status, threadID, err)
	}
	return out, nil
}

func appReadProposalTargets(
	tx *sql.Tx,
	threadID, uid string,
) (map[int64]AppThreadMemory, error) {
	rows, err := tx.Query(appThreadMemorySelect+`
WHERE m.thread_id = ? AND m.uid = ? AND m.id IN (
  SELECT proposal.supersedes_id FROM zalo_memory proposal
  WHERE proposal.thread_id = ? AND proposal.uid = ?
    AND proposal.status = 'pending' AND proposal.supersedes_id > 0
)`, threadID, uid, threadID, uid)
	if err != nil {
		return nil, fmt.Errorf("list proposal targets for %s: %w", threadID, err)
	}
	defer rows.Close()
	targets := make(map[int64]AppThreadMemory)
	for rows.Next() {
		target, err := scanAppThreadMemory(rows)
		if err != nil {
			return nil, err
		}
		targets[target.ID] = target
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list proposal targets for %s: %w", threadID, err)
	}
	return targets, nil
}

func (s *Store) AppLessons(query string, pinnedOnly bool) (AppLessonList, error) {
	trimmed := strings.TrimSpace(query)
	pattern := appMemoryLikePattern(trimmed)
	pinned := 0
	if pinnedOnly {
		pinned = 1
	}
	var out AppLessonList
	if err := s.db.QueryRow(`SELECT COALESCE(revision, 0) FROM app_memory_revisions
WHERE scope = 'lessons' AND scope_id = ''`).Scan(&out.Revision); errors.Is(err, sql.ErrNoRows) {
		out.Revision = 0
	} else if err != nil {
		return AppLessonList{}, fmt.Errorf("read lesson revision: %w", err)
	}
	rows, err := s.db.Query(appLessonSelect+`
WHERE (? = '' OR l.bot_text LIKE ? ESCAPE '\' COLLATE NOCASE OR
       l.better LIKE ? ESCAPE '\' COLLATE NOCASE OR
       l.note LIKE ? ESCAPE '\' COLLATE NOCASE OR
       COALESCE(t.name, '') LIKE ? ESCAPE '\' COLLATE NOCASE)
  AND (? = 0 OR l.pinned = 1)
ORDER BY l.pinned DESC, l.id DESC LIMIT ?`,
		trimmed, pattern, pattern, pattern, pattern, pinned, appLessonListLimit)
	if err != nil {
		return AppLessonList{}, fmt.Errorf("list lessons: %w", err)
	}
	defer rows.Close()
	out.Lessons = make([]AppLesson, 0)
	for rows.Next() {
		lesson, err := scanAppLesson(rows)
		if err != nil {
			return AppLessonList{}, err
		}
		out.Lessons = append(out.Lessons, lesson)
	}
	if err := rows.Err(); err != nil {
		return AppLessonList{}, fmt.Errorf("list lessons: %w", err)
	}
	return out, nil
}

func (s *Store) AppMemoryRevisions(threadID string) (AppMemoryRevision, error) {
	if err := validateAppMemoryIdentity(threadID, true); err != nil {
		return AppMemoryRevision{}, err
	}
	var out AppMemoryRevision
	if err := s.db.QueryRow(`SELECT
  COALESCE((SELECT revision FROM app_memory_revisions
            WHERE scope = 'thread' AND scope_id = ?), 0),
  COALESCE((SELECT revision FROM app_memory_revisions
            WHERE scope = 'lessons' AND scope_id = ''), 0)`, threadID).Scan(
		&out.Memory, &out.Lessons,
	); err != nil {
		return AppMemoryRevision{}, fmt.Errorf("read memory revisions for %s: %w", threadID, err)
	}
	return out, nil
}

func (s *Store) AppPromptMemory(threadID string, limit int) ([]ipc.ZaloMemory, int64, error) {
	if err := validateAppMemoryIdentity(threadID, true); err != nil {
		return nil, 0, err
	}
	subjectUID := ""
	var threadType string
	if err := s.db.QueryRow(`SELECT thread_type FROM zalo_threads WHERE id = ?`,
		threadID).Scan(&threadType); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, fmt.Errorf("read legacy prompt Memory thread %s: %w", threadID, err)
	}
	if threadType == ipc.ZaloThreadUser {
		subjectUID = threadID
	}
	snapshot, err := s.AppPromptMemoryForSubject(threadID, subjectUID, limit, time.Now())
	if err != nil {
		return nil, 0, err
	}
	items := append(append([]AppPromptMemoryItem(nil), snapshot.Common...), snapshot.Subject...)
	out := make([]ipc.ZaloMemory, 0, len(items))
	for _, item := range items {
		out = append(out, ipc.ZaloMemory{
			ThreadID: threadID, UID: item.UID, Text: item.Text, CreatedAt: item.createdAt,
		})
	}
	revision := snapshot.CommonRevision
	if subjectUID != "" {
		revision = snapshot.SubjectRevision
	}
	return out, revision, nil
}

func (s *Store) AppPromptLessons(limit int) ([]ipc.ZaloLesson, int64, error) {
	if limit <= 0 || limit > appLessonPinLimit {
		limit = appLessonPinLimit
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, fmt.Errorf("begin prompt lesson read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var revision int64
	if err := tx.QueryRow(`SELECT COALESCE((SELECT revision FROM app_memory_revisions
WHERE scope = 'lessons' AND scope_id = ''), 0)`).Scan(&revision); err != nil {
		return nil, 0, fmt.Errorf("read prompt lesson revision: %w", err)
	}
	rows, err := tx.Query(`SELECT thread_id, bot_text, better, note, created_at FROM (
  SELECT id, thread_id, bot_text, better, note, created_at FROM zalo_lessons
  ORDER BY pinned DESC, id DESC LIMIT ?
) ORDER BY id ASC`, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("select prompt lessons: %w", err)
	}
	out := make([]ipc.ZaloLesson, 0, limit)
	for rows.Next() {
		var lesson ipc.ZaloLesson
		var createdAt string
		if err := rows.Scan(&lesson.ThreadID, &lesson.BotText, &lesson.Better, &lesson.Note, &createdAt); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("scan prompt lesson: %w", err)
		}
		lesson.CreatedAt, err = parseTS(createdAt)
		if err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("parse prompt lesson: %w", err)
		}
		out = append(out, lesson)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, fmt.Errorf("select prompt lessons: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, fmt.Errorf("close prompt lessons: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit prompt lesson read: %w", err)
	}
	return out, revision, nil
}

const appThreadMemorySelect = `
SELECT m.id, m.thread_id, COALESCE(t.name, ''), m.text, m.pinned, m.source,
       m.source_message_id,
       COALESCE((SELECT sm.body FROM zalo_messages sm
                 WHERE sm.id = m.source_message_id AND sm.thread_id = m.thread_id), ''),
       m.created_at,
       CASE WHEN m.updated_at <> '' THEN m.updated_at ELSE m.created_at END,
       m.uid, m.memory_key, m.category, m.confidence, m.status, m.proposal_action,
       m.supersedes_id, m.last_confirmed_at, m.expires_at
FROM zalo_memory m
LEFT JOIN zalo_threads t ON t.id = m.thread_id`

const appLessonSelect = `
SELECT l.id, l.thread_id, COALESCE(t.name, ''), l.bot_text, l.better, l.note,
       l.pinned, l.created_at,
       CASE WHEN l.updated_at <> '' THEN l.updated_at ELSE l.created_at END
FROM zalo_lessons l
LEFT JOIN zalo_threads t ON t.id = l.thread_id`

type appRowScanner interface {
	Scan(dest ...any) error
}

func scanAppThreadMemory(row appRowScanner) (AppThreadMemory, error) {
	var out AppThreadMemory
	var createdAt, updatedAt, lastConfirmedAt string
	var expiresAt sql.NullString
	if err := row.Scan(
		&out.ID, &out.ThreadID, &out.ThreadName, &out.Text, &out.Pinned, &out.Source,
		&out.SourceMessageID, &out.SourcePreview, &createdAt, &updatedAt,
		&out.UID, &out.MemoryKey, &out.Category, &out.Confidence, &out.Status,
		&out.ProposalAction, &out.SupersedesID, &lastConfirmedAt, &expiresAt,
	); err != nil {
		return AppThreadMemory{}, fmt.Errorf("scan app memory: %w", err)
	}
	var err error
	out.CreatedAt, err = parseTS(createdAt)
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("parse app memory created_at: %w", err)
	}
	out.UpdatedAt, err = parseTS(updatedAt)
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("parse app memory updated_at: %w", err)
	}
	if lastConfirmedAt != "" {
		parsed, err := parseTS(lastConfirmedAt)
		if err != nil {
			return AppThreadMemory{}, fmt.Errorf("parse app memory last_confirmed_at: %w", err)
		}
		out.LastConfirmedAt = &parsed
	}
	if expiresAt.Valid && expiresAt.String != "" {
		parsed, err := parseTS(expiresAt.String)
		if err != nil {
			return AppThreadMemory{}, fmt.Errorf("parse app memory expires_at: %w", err)
		}
		out.ExpiresAt = &parsed
	}
	return out, nil
}

func scanAppLesson(row appRowScanner) (AppLesson, error) {
	var out AppLesson
	var createdAt, updatedAt string
	if err := row.Scan(
		&out.ID, &out.ThreadID, &out.ThreadName, &out.BotText, &out.Better, &out.Note,
		&out.Pinned, &createdAt, &updatedAt,
	); err != nil {
		return AppLesson{}, fmt.Errorf("scan app lesson: %w", err)
	}
	var err error
	out.CreatedAt, err = parseTS(createdAt)
	if err != nil {
		return AppLesson{}, fmt.Errorf("parse app lesson created_at: %w", err)
	}
	out.UpdatedAt, err = parseTS(updatedAt)
	if err != nil {
		return AppLesson{}, fmt.Errorf("parse app lesson updated_at: %w", err)
	}
	return out, nil
}

func (s *Store) appThreadMemoryByID(threadID string, id int64) (AppThreadMemory, error) {
	out, err := scanAppThreadMemory(s.db.QueryRow(appThreadMemorySelect+`
WHERE m.thread_id = ? AND m.id = ?`, threadID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AppThreadMemory{}, fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	return out, err
}

func (s *Store) appLessonByID(id int64) (AppLesson, error) {
	out, err := scanAppLesson(s.db.QueryRow(appLessonSelect+` WHERE l.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AppLesson{}, fmt.Errorf("lesson %d: %w", id, ErrAppMemoryNotFound)
	}
	return out, err
}

func validateAppLessonInput(input AppLessonInput) (AppLessonInput, error) {
	input.ThreadID = strings.TrimSpace(input.ThreadID)
	input.BotText = strings.TrimSpace(input.BotText)
	input.Better = strings.TrimSpace(input.Better)
	input.Note = strings.TrimSpace(input.Note)
	for name, value := range map[string]string{
		"bot_text": input.BotText,
		"better":   input.Better,
		"note":     input.Note,
	} {
		if len([]rune(value)) > maxLessonField {
			return AppLessonInput{}, fmt.Errorf("%w: %s exceeds %d runes", ErrAppMemoryInvalid, name, maxLessonField)
		}
	}
	if input.Better == "" && input.Note == "" {
		return AppLessonInput{}, fmt.Errorf("%w: lesson requires better text or note", ErrAppMemoryInvalid)
	}
	return input, nil
}

func appMemoryLikePattern(query string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return `%` + replacer.Replace(query) + `%`
}
