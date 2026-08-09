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
	Text   string `json:"text"`
	Pinned bool   `json:"pinned"`
}

type AppThreadMemory struct {
	ID              int64     `json:"id"`
	ThreadID        string    `json:"thread_id"`
	ThreadName      string    `json:"thread_name"`
	Text            string    `json:"text"`
	Pinned          bool      `json:"pinned"`
	Source          string    `json:"source"`
	SourceMessageID int64     `json:"source_message_id"`
	SourcePreview   string    `json:"source_preview"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AppThreadMemoryDetail struct {
	Thread         AppMemoryThread   `json:"thread"`
	Revision       int64             `json:"revision"`
	SyncedRevision int64             `json:"synced_revision"`
	Memories       []AppThreadMemory `json:"memories"`
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
	rows, err := s.db.Query(`
SELECT t.id, t.name, t.avatar, t.thread_type,
       (SELECT COUNT(*) FROM zalo_memory m WHERE m.thread_id = t.id),
       (SELECT COUNT(*) FROM zalo_memory m WHERE m.thread_id = t.id AND m.pinned = 1),
       COALESCE((SELECT m.text FROM zalo_memory m
                 WHERE m.thread_id = t.id ORDER BY m.pinned DESC, m.id DESC LIMIT 1), ''),
       t.updated_at
FROM zalo_threads t
WHERE (? = '' OR t.name LIKE ? ESCAPE '\' COLLATE NOCASE OR
       EXISTS (SELECT 1 FROM zalo_memory m
               WHERE m.thread_id = t.id AND m.text LIKE ? ESCAPE '\' COLLATE NOCASE))
  AND (? = 0 OR EXISTS (SELECT 1 FROM zalo_memory m
                        WHERE m.thread_id = t.id AND m.pinned = 1))
ORDER BY CASE WHEN EXISTS (SELECT 1 FROM zalo_memory m WHERE m.thread_id = t.id)
              THEN 0 ELSE 1 END,
         t.updated_at DESC, t.id ASC
LIMIT ?`, trimmed, pattern, pattern, pinned, appOverviewLimit)
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
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return AppThreadMemoryDetail{}, fmt.Errorf("%w: thread ID is required", ErrAppMemoryInvalid)
	}
	var out AppThreadMemoryDetail
	var updatedAt string
	if err := s.db.QueryRow(`SELECT id, name, avatar, thread_type, updated_at
FROM zalo_threads WHERE id = ?`, threadID).Scan(
		&out.Thread.ID, &out.Thread.Name, &out.Thread.Avatar, &out.Thread.ThreadType, &updatedAt,
	); errors.Is(err, sql.ErrNoRows) {
		return AppThreadMemoryDetail{}, fmt.Errorf("thread %s: %w", threadID, ErrAppMemoryNotFound)
	} else if err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("read memory thread %s: %w", threadID, err)
	}
	var err error
	out.Thread.UpdatedAt, err = parseTS(updatedAt)
	if err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("parse memory thread %s updated_at: %w", threadID, err)
	}
	if err := s.db.QueryRow(`SELECT
  COUNT(*), COALESCE(SUM(CASE WHEN pinned = 1 THEN 1 ELSE 0 END), 0)
FROM zalo_memory WHERE thread_id = ?`, threadID).Scan(
		&out.Thread.MemoryCount, &out.Thread.PinnedCount,
	); err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("count memory for %s: %w", threadID, err)
	}
	if err := s.db.QueryRow(`SELECT COALESCE(revision, 0) FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = ?`, threadID).Scan(&out.Revision); errors.Is(err, sql.ErrNoRows) {
		out.Revision = 0
	} else if err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("read memory revision for %s: %w", threadID, err)
	}
	if err := s.db.QueryRow(`SELECT COALESCE((SELECT memory_revision
FROM app_zalo_cli_sessions WHERE thread_id = ?), 0)`, threadID).Scan(&out.SyncedRevision); err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("read synced memory revision for %s: %w", threadID, err)
	}

	rows, err := s.db.Query(appThreadMemorySelect+`
WHERE m.thread_id = ? ORDER BY m.pinned DESC, m.id DESC LIMIT ?`, threadID, appThreadMemoryLimit)
	if err != nil {
		return AppThreadMemoryDetail{}, fmt.Errorf("list memory for %s: %w", threadID, err)
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
		return AppThreadMemoryDetail{}, fmt.Errorf("list memory for %s: %w", threadID, err)
	}
	if len(out.Memories) > 0 {
		out.Thread.Preview = out.Memories[0].Text
	}
	return out, nil
}

func (s *Store) CreateAppThreadMemory(threadID string, input AppThreadMemoryInput) (AppThreadMemory, error) {
	threadID = strings.TrimSpace(threadID)
	input, err := validateAppThreadMemoryInput(threadID, input)
	if err != nil {
		return AppThreadMemory{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("begin memory create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := appRequireMemoryThread(tx, threadID); err != nil {
		return AppThreadMemory{}, err
	}
	if input.Pinned {
		if err := appEnforcePinLimit(tx, "zalo_memory", "thread_id", threadID, 0, appMemoryPinLimit); err != nil {
			return AppThreadMemory{}, err
		}
	}
	now := ts(time.Now())
	result, err := tx.Exec(`INSERT INTO zalo_memory(
thread_id, uid, text, created_at, pinned, source, source_message_id, updated_at)
VALUES (?, '', ?, ?, ?, 'operator', 0, ?)`, threadID, input.Text, now, input.Pinned, now)
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("create memory for %s: %w", threadID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("read created memory ID for %s: %w", threadID, err)
	}
	if err := appBumpMemoryRevision(tx, "thread", threadID); err != nil {
		return AppThreadMemory{}, err
	}
	if err := appBumpSubjectMemoryRevision(tx, threadID, ""); err != nil {
		return AppThreadMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppThreadMemory{}, fmt.Errorf("commit memory create for %s: %w", threadID, err)
	}
	return s.appThreadMemoryByID(threadID, id)
}

func (s *Store) UpdateAppThreadMemory(threadID string, id int64, input AppThreadMemoryInput) (AppThreadMemory, error) {
	threadID = strings.TrimSpace(threadID)
	input, err := validateAppThreadMemoryInput(threadID, input)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if id <= 0 {
		return AppThreadMemory{}, fmt.Errorf("%w: memory ID must be positive", ErrAppMemoryInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("begin memory update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var wasPinned bool
	var uid string
	if err := tx.QueryRow(`SELECT pinned, uid FROM zalo_memory WHERE thread_id = ? AND id = ?`, threadID, id).Scan(&wasPinned, &uid); errors.Is(err, sql.ErrNoRows) {
		return AppThreadMemory{}, fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	} else if err != nil {
		return AppThreadMemory{}, fmt.Errorf("read memory %d in %s: %w", id, threadID, err)
	}
	if input.Pinned && !wasPinned {
		if err := appEnforcePinLimit(tx, "zalo_memory", "thread_id", threadID, id, appMemoryPinLimit); err != nil {
			return AppThreadMemory{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE zalo_memory SET text = ?, pinned = ?, updated_at = ?
WHERE thread_id = ? AND id = ?`, input.Text, input.Pinned, ts(time.Now()), threadID, id); err != nil {
		return AppThreadMemory{}, fmt.Errorf("update memory %d in %s: %w", id, threadID, err)
	}
	if err := appBumpMemoryRevision(tx, "thread", threadID); err != nil {
		return AppThreadMemory{}, err
	}
	if err := appBumpSubjectMemoryRevision(tx, threadID, uid); err != nil {
		return AppThreadMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppThreadMemory{}, fmt.Errorf("commit memory update %d in %s: %w", id, threadID, err)
	}
	return s.appThreadMemoryByID(threadID, id)
}

func (s *Store) DeleteAppThreadMemory(threadID string, id int64) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || id <= 0 {
		return fmt.Errorf("%w: thread and memory IDs are required", ErrAppMemoryInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin memory delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var uid string
	if err := tx.QueryRow(`SELECT uid FROM zalo_memory WHERE thread_id = ? AND id = ?`, threadID, id).Scan(&uid); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	} else if err != nil {
		return fmt.Errorf("read memory %d in %s: %w", id, threadID, err)
	}
	result, err := tx.Exec(`DELETE FROM zalo_memory WHERE thread_id = ? AND id = ?`, threadID, id)
	if err != nil {
		return fmt.Errorf("delete memory %d in %s: %w", id, threadID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect memory delete %d in %s: %w", id, threadID, err)
	}
	if count != 1 {
		return fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	if err := appBumpMemoryRevision(tx, "thread", threadID); err != nil {
		return err
	}
	if err := appBumpSubjectMemoryRevision(tx, threadID, uid); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit memory delete %d in %s: %w", id, threadID, err)
	}
	return nil
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

func (s *Store) CreateAppLesson(input AppLessonInput) (AppLesson, error) {
	input, err := validateAppLessonInput(input)
	if err != nil {
		return AppLesson{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppLesson{}, fmt.Errorf("begin lesson create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if input.ThreadID != "" {
		if err := appRequireMemoryThread(tx, input.ThreadID); err != nil {
			return AppLesson{}, err
		}
	}
	if input.Pinned {
		if err := appEnforcePinLimit(tx, "zalo_lessons", "", "", 0, appLessonPinLimit); err != nil {
			return AppLesson{}, err
		}
	}
	now := ts(time.Now())
	result, err := tx.Exec(`INSERT INTO zalo_lessons(
thread_id, bot_text, better, note, created_at, pinned, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, input.ThreadID, input.BotText, input.Better, input.Note, now, input.Pinned, now)
	if err != nil {
		return AppLesson{}, fmt.Errorf("create lesson: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AppLesson{}, fmt.Errorf("read created lesson ID: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AppLesson{}, fmt.Errorf("commit lesson create: %w", err)
	}
	return s.appLessonByID(id)
}

func (s *Store) UpdateAppLesson(id int64, input AppLessonInput) (AppLesson, error) {
	input, err := validateAppLessonInput(input)
	if err != nil {
		return AppLesson{}, err
	}
	if id <= 0 {
		return AppLesson{}, fmt.Errorf("%w: lesson ID must be positive", ErrAppMemoryInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppLesson{}, fmt.Errorf("begin lesson update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if input.ThreadID != "" {
		if err := appRequireMemoryThread(tx, input.ThreadID); err != nil {
			return AppLesson{}, err
		}
	}
	var wasPinned bool
	if err := tx.QueryRow(`SELECT pinned FROM zalo_lessons WHERE id = ?`, id).Scan(&wasPinned); errors.Is(err, sql.ErrNoRows) {
		return AppLesson{}, fmt.Errorf("lesson %d: %w", id, ErrAppMemoryNotFound)
	} else if err != nil {
		return AppLesson{}, fmt.Errorf("read lesson %d: %w", id, err)
	}
	if input.Pinned && !wasPinned {
		if err := appEnforcePinLimit(tx, "zalo_lessons", "", "", id, appLessonPinLimit); err != nil {
			return AppLesson{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE zalo_lessons SET
thread_id = ?, bot_text = ?, better = ?, note = ?, pinned = ?, updated_at = ?
WHERE id = ?`, input.ThreadID, input.BotText, input.Better, input.Note, input.Pinned, ts(time.Now()), id); err != nil {
		return AppLesson{}, fmt.Errorf("update lesson %d: %w", id, err)
	}
	if err := appBumpMemoryRevision(tx, "lessons", ""); err != nil {
		return AppLesson{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppLesson{}, fmt.Errorf("commit lesson update %d: %w", id, err)
	}
	return s.appLessonByID(id)
}

func (s *Store) DeleteAppLesson(id int64) error {
	if id <= 0 {
		return fmt.Errorf("%w: lesson ID must be positive", ErrAppMemoryInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin lesson delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`DELETE FROM zalo_lessons WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete lesson %d: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect lesson delete %d: %w", id, err)
	}
	if count != 1 {
		return fmt.Errorf("lesson %d: %w", id, ErrAppMemoryNotFound)
	}
	if err := appBumpMemoryRevision(tx, "lessons", ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit lesson delete %d: %w", id, err)
	}
	return nil
}

func (s *Store) AppMemoryRevisions(threadID string) (AppMemoryRevision, error) {
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
	if limit <= 0 || limit > appMemoryPinLimit {
		limit = appMemoryPinLimit
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, fmt.Errorf("begin prompt memory read for %s: %w", threadID, err)
	}
	defer func() { _ = tx.Rollback() }()
	var revision int64
	if err := tx.QueryRow(`SELECT COALESCE((SELECT revision FROM app_memory_revisions
WHERE scope = 'thread' AND scope_id = ?), 0)`, threadID).Scan(&revision); err != nil {
		return nil, 0, fmt.Errorf("read prompt memory revision for %s: %w", threadID, err)
	}
	rows, err := tx.Query(`SELECT thread_id, uid, text, created_at FROM (
  SELECT id, thread_id, uid, text, created_at FROM zalo_memory
  WHERE thread_id = ? ORDER BY pinned DESC, id DESC LIMIT ?
) ORDER BY id ASC`, threadID, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("select prompt memory for %s: %w", threadID, err)
	}
	out := make([]ipc.ZaloMemory, 0, limit)
	for rows.Next() {
		var memory ipc.ZaloMemory
		var createdAt string
		if err := rows.Scan(&memory.ThreadID, &memory.UID, &memory.Text, &createdAt); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("scan prompt memory for %s: %w", threadID, err)
		}
		memory.CreatedAt, err = parseTS(createdAt)
		if err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("parse prompt memory for %s: %w", threadID, err)
		}
		out = append(out, memory)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, fmt.Errorf("select prompt memory for %s: %w", threadID, err)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, fmt.Errorf("close prompt memory for %s: %w", threadID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("commit prompt memory read for %s: %w", threadID, err)
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
       CASE WHEN m.updated_at <> '' THEN m.updated_at ELSE m.created_at END
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
	var createdAt, updatedAt string
	if err := row.Scan(
		&out.ID, &out.ThreadID, &out.ThreadName, &out.Text, &out.Pinned, &out.Source,
		&out.SourceMessageID, &out.SourcePreview, &createdAt, &updatedAt,
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

func validateAppThreadMemoryInput(threadID string, input AppThreadMemoryInput) (AppThreadMemoryInput, error) {
	if threadID == "" {
		return AppThreadMemoryInput{}, fmt.Errorf("%w: thread ID is required", ErrAppMemoryInvalid)
	}
	input.Text = strings.TrimSpace(input.Text)
	if input.Text == "" {
		return AppThreadMemoryInput{}, fmt.Errorf("%w: memory text is required", ErrAppMemoryInvalid)
	}
	if len([]rune(input.Text)) > maxZaloMemoryLen {
		return AppThreadMemoryInput{}, fmt.Errorf("%w: memory exceeds %d runes", ErrAppMemoryInvalid, maxZaloMemoryLen)
	}
	return input, nil
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

func appRequireMemoryThread(tx *sql.Tx, threadID string) error {
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM zalo_threads WHERE id = ?`, threadID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("thread %s: %w", threadID, ErrAppMemoryNotFound)
	} else if err != nil {
		return fmt.Errorf("read memory thread %s: %w", threadID, err)
	}
	return nil
}

func appEnforcePinLimit(
	tx *sql.Tx,
	table, scopeColumn, scopeID string,
	excludeID int64,
	limit int,
) error {
	query := `SELECT COUNT(*) FROM ` + table + ` WHERE pinned = 1 AND id <> ?`
	args := []any{excludeID}
	if scopeColumn != "" {
		query += ` AND ` + scopeColumn + ` = ?`
		args = append(args, scopeID)
	}
	var count int
	if err := tx.QueryRow(query, args...).Scan(&count); err != nil {
		return fmt.Errorf("count pinned memory: %w", err)
	}
	if count >= limit {
		return fmt.Errorf("%w: maximum %d", ErrAppMemoryPinLimit, limit)
	}
	return nil
}

func appBumpMemoryRevision(tx *sql.Tx, scope, scopeID string) error {
	if _, err := tx.Exec(`INSERT INTO app_memory_revisions(scope, scope_id, revision)
VALUES (?, ?, 1)
ON CONFLICT(scope, scope_id) DO UPDATE SET revision = revision + 1`, scope, scopeID); err != nil {
		return fmt.Errorf("bump %s memory revision: %w", scope, err)
	}
	return nil
}

func appBumpSubjectMemoryRevision(tx *sql.Tx, threadID, uid string) error {
	if _, err := tx.Exec(`INSERT INTO app_memory_subject_revisions(thread_id, uid, revision)
VALUES (?, ?, 1)
ON CONFLICT(thread_id, uid) DO UPDATE SET revision = revision + 1`, threadID, uid); err != nil {
		return fmt.Errorf("bump subject Memory revision: %w", err)
	}
	return nil
}

func appMemoryLikePattern(query string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return `%` + replacer.Replace(query) + `%`
}
