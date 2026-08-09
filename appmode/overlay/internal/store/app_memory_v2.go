package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const appMemoryPendingRetention = 30 * 24 * time.Hour

type AppPromptMemoryItem struct {
	ID        int64
	UID       string
	MemoryKey string
	Category  string
	Text      string
	Pinned    bool
	createdAt time.Time
}

type AppPromptMemorySnapshot struct {
	Common          []AppPromptMemoryItem
	Subject         []AppPromptMemoryItem
	CommonRevision  int64
	SubjectRevision int64
}

func (s *Store) AppPromptMemoryForSubject(
	threadID, subjectUID string,
	limit int,
	now time.Time,
) (AppPromptMemorySnapshot, error) {
	threadID, subjectUID, err := validateAppPromptMemoryScope(threadID, subjectUID)
	if err != nil {
		return AppPromptMemorySnapshot{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppPromptMemorySnapshot{}, fmt.Errorf("begin subject Memory snapshot for %s: %w", threadID, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := appExpirePromptMemory(tx, now); err != nil {
		return AppPromptMemorySnapshot{}, err
	}
	out, err := appReadPromptMemoryRevisions(tx, threadID, subjectUID)
	if err != nil {
		return AppPromptMemorySnapshot{}, err
	}
	if subjectUID == "" {
		out.SubjectRevision = 0
	}
	out.Common, err = appReadPromptMemoryScope(tx, threadID, "", limit, now)
	if err != nil {
		return AppPromptMemorySnapshot{}, err
	}
	out.Subject = make([]AppPromptMemoryItem, 0)
	if subjectUID != "" {
		out.Subject, err = appReadPromptMemoryScope(tx, threadID, subjectUID, limit, now)
		if err != nil {
			return AppPromptMemorySnapshot{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AppPromptMemorySnapshot{}, fmt.Errorf("commit subject Memory snapshot for %s: %w", threadID, err)
	}
	return out, nil
}

func validateAppPromptMemoryScope(threadID, subjectUID string) (string, string, error) {
	threadID = strings.TrimSpace(threadID)
	subjectUID = strings.TrimSpace(subjectUID)
	if threadID == "" {
		return "", "", fmt.Errorf("%w: thread ID is required", ErrAppMemoryInvalid)
	}
	if len([]rune(threadID)) > maxZaloMemoryLen {
		return "", "", fmt.Errorf("%w: thread ID exceeds %d runes", ErrAppMemoryInvalid, maxZaloMemoryLen)
	}
	if len([]rune(subjectUID)) > maxZaloMemoryLen {
		return "", "", fmt.Errorf("%w: subject UID exceeds %d runes", ErrAppMemoryInvalid, maxZaloMemoryLen)
	}
	return threadID, subjectUID, nil
}

type appMemorySubjectScope struct {
	threadID string
	uid      string
}

func appExpirePromptMemory(tx *sql.Tx, now time.Time) error {
	if _, err := tx.Exec(`DELETE FROM zalo_memory
WHERE status = 'pending' AND created_at < ?`, ts(now.Add(-appMemoryPendingRetention))); err != nil {
		return fmt.Errorf("delete overdue pending Memory: %w", err)
	}

	scopes, err := appDuePromptMemoryScopes(tx, now)
	if err != nil || len(scopes) == 0 {
		return err
	}
	if _, err := tx.Exec(`UPDATE zalo_memory
SET status = 'expired', updated_at = ?
WHERE status = 'active' AND expires_at IS NOT NULL AND expires_at <= ?`, ts(now), ts(now)); err != nil {
		return fmt.Errorf("expire due Memory: %w", err)
	}
	for _, scope := range scopes {
		if err := appBumpSubjectMemoryRevision(tx, scope.threadID, scope.uid); err != nil {
			return err
		}
	}
	return nil
}

func appDuePromptMemoryScopes(tx *sql.Tx, now time.Time) ([]appMemorySubjectScope, error) {
	rows, err := tx.Query(`SELECT thread_id, uid FROM zalo_memory
WHERE status = 'active' AND expires_at IS NOT NULL AND expires_at <= ?
GROUP BY thread_id, uid ORDER BY thread_id, uid`, ts(now))
	if err != nil {
		return nil, fmt.Errorf("list due Memory scopes: %w", err)
	}
	defer rows.Close()
	scopes := make([]appMemorySubjectScope, 0)
	for rows.Next() {
		var scope appMemorySubjectScope
		if err := rows.Scan(&scope.threadID, &scope.uid); err != nil {
			return nil, fmt.Errorf("scan due Memory scope: %w", err)
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due Memory scopes: %w", err)
	}
	return scopes, nil
}

func appReadPromptMemoryRevisions(
	tx *sql.Tx,
	threadID, subjectUID string,
) (AppPromptMemorySnapshot, error) {
	var out AppPromptMemorySnapshot
	if err := tx.QueryRow(`SELECT
  COALESCE((SELECT revision FROM app_memory_subject_revisions
            WHERE thread_id = ? AND uid = ''), 0),
  COALESCE((SELECT revision FROM app_memory_subject_revisions
            WHERE thread_id = ? AND uid = ?), 0)`,
		threadID, threadID, subjectUID,
	).Scan(&out.CommonRevision, &out.SubjectRevision); err != nil {
		return AppPromptMemorySnapshot{}, fmt.Errorf("read subject Memory revisions for %s: %w", threadID, err)
	}
	return out, nil
}

func appReadPromptMemoryScope(
	tx *sql.Tx,
	threadID, uid string,
	limit int,
	now time.Time,
) ([]AppPromptMemoryItem, error) {
	if limit <= 0 || limit > appMemoryPinLimit {
		limit = appMemoryPinLimit
	}
	rows, err := tx.Query(`SELECT id, uid, memory_key, category, text, pinned, created_at FROM (
  SELECT id, uid, memory_key, category, text, pinned, created_at FROM zalo_memory
  WHERE thread_id = ? AND uid = ? AND status = 'active'
    AND (expires_at IS NULL OR expires_at > ?)
  ORDER BY pinned DESC, id DESC LIMIT ?
) ORDER BY id ASC`, threadID, uid, ts(now), limit)
	if err != nil {
		return nil, fmt.Errorf("select prompt Memory scope for %s: %w", threadID, err)
	}
	defer rows.Close()
	out := make([]AppPromptMemoryItem, 0, limit)
	for rows.Next() {
		var item AppPromptMemoryItem
		var createdAt string
		if err := rows.Scan(
			&item.ID, &item.UID, &item.MemoryKey, &item.Category, &item.Text, &item.Pinned, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan prompt Memory scope for %s: %w", threadID, err)
		}
		item.createdAt, err = parseTS(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse prompt Memory created_at for %s: %w", threadID, err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("select prompt Memory scope for %s: %w", threadID, err)
	}
	return out, nil
}
