package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"agentdc/internal/ipc"
)

// ErrZaloCLISessionConflict means a caller tried to mutate a session generation
// that has already been replaced or advanced by another caller.
var ErrZaloCLISessionConflict = errors.New("zalo CLI session generation conflict")

// ZaloCLISession is content-free routing state for one Zalo conversation.
type ZaloCLISession struct {
	ThreadID              string
	ClaudeSessionID       string
	Model                 string
	PromptFingerprint     string
	LastError             string
	Generation            int64
	ContextTokens         int64
	TurnCount             int64
	MessageCursor         int64
	MemoryRevision        int64
	LessonsRevision       int64
	MemorySubjectUID      string
	MemorySubjectRevision int64
	MemoryCommonRevision  int64
	RotateBeforeNext      bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// ZaloDeltaMessage preserves the durable message row ID alongside its payload.
type ZaloDeltaMessage struct {
	ID      int64
	Message ipc.ZaloMessage
}

const zaloCLISessionColumns = `thread_id, claude_session_id, generation, model,
prompt_fingerprint, context_tokens, turn_count, message_cursor,
rotate_before_next, last_error, created_at, updated_at,
memory_revision, lessons_revision, memory_subject_uid,
memory_subject_revision, memory_common_revision`

type zaloCLISessionScanner interface {
	Scan(dest ...any) error
}

func scanZaloCLISession(row zaloCLISessionScanner) (ZaloCLISession, error) {
	var session ZaloCLISession
	var rotate int64
	var createdAt, updatedAt string
	if err := row.Scan(
		&session.ThreadID,
		&session.ClaudeSessionID,
		&session.Generation,
		&session.Model,
		&session.PromptFingerprint,
		&session.ContextTokens,
		&session.TurnCount,
		&session.MessageCursor,
		&rotate,
		&session.LastError,
		&createdAt,
		&updatedAt,
		&session.MemoryRevision,
		&session.LessonsRevision,
		&session.MemorySubjectUID,
		&session.MemorySubjectRevision,
		&session.MemoryCommonRevision,
	); err != nil {
		return ZaloCLISession{}, err
	}
	session.RotateBeforeNext = rotate != 0
	var err error
	if session.CreatedAt, err = parseTS(createdAt); err != nil {
		return ZaloCLISession{}, fmt.Errorf("parse Zalo CLI session created_at: %w", err)
	}
	if session.UpdatedAt, err = parseTS(updatedAt); err != nil {
		return ZaloCLISession{}, fmt.Errorf("parse Zalo CLI session updated_at: %w", err)
	}
	return session, nil
}

// ZaloCLISession returns the current mapping for threadID.
func (s *Store) ZaloCLISession(threadID string) (ZaloCLISession, error) {
	session, err := scanZaloCLISession(s.db.QueryRow(
		`SELECT `+zaloCLISessionColumns+` FROM app_zalo_cli_sessions WHERE thread_id = ?`,
		threadID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return ZaloCLISession{}, fmt.Errorf("Zalo CLI session %s: %w", threadID, ErrNotFound)
	}
	if err != nil {
		return ZaloCLISession{}, fmt.Errorf("select Zalo CLI session %s: %w", threadID, err)
	}
	return session, nil
}

// CreateZaloCLISession creates generation one for a thread.
func (s *Store) CreateZaloCLISession(next ZaloCLISession) (ZaloCLISession, error) {
	now := ts(time.Now())
	_, err := s.db.Exec(`INSERT INTO app_zalo_cli_sessions(
thread_id, claude_session_id, generation, model, prompt_fingerprint,
context_tokens, turn_count, message_cursor, rotate_before_next, last_error,
created_at, updated_at, memory_revision, lessons_revision, memory_subject_uid,
memory_subject_revision, memory_common_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		next.ThreadID,
		next.ClaudeSessionID,
		int64(1),
		next.Model,
		next.PromptFingerprint,
		next.ContextTokens,
		next.TurnCount,
		next.MessageCursor,
		next.RotateBeforeNext,
		next.LastError,
		now,
		now,
		next.MemoryRevision,
		next.LessonsRevision,
		next.MemorySubjectUID,
		next.MemorySubjectRevision,
		next.MemoryCommonRevision,
	)
	if err != nil {
		return ZaloCLISession{}, fmt.Errorf("create Zalo CLI session %s: %w", next.ThreadID, err)
	}
	return s.ZaloCLISession(next.ThreadID)
}

// ReplaceZaloCLISession atomically rotates a session at expectedGeneration.
func (s *Store) ReplaceZaloCLISession(expectedGeneration int64, next ZaloCLISession) (ZaloCLISession, error) {
	result, err := s.db.Exec(`UPDATE app_zalo_cli_sessions SET
claude_session_id = ?, generation = generation + 1, model = ?, prompt_fingerprint = ?,
context_tokens = ?, turn_count = ?, message_cursor = MAX(message_cursor, ?), rotate_before_next = ?,
last_error = ?, updated_at = ?, memory_revision = ?, lessons_revision = ?,
memory_subject_uid = '', memory_subject_revision = 0, memory_common_revision = 0
WHERE thread_id = ? AND generation = ?`,
		next.ClaudeSessionID,
		next.Model,
		next.PromptFingerprint,
		next.ContextTokens,
		next.TurnCount,
		next.MessageCursor,
		next.RotateBeforeNext,
		next.LastError,
		ts(time.Now()),
		next.MemoryRevision,
		next.LessonsRevision,
		next.ThreadID,
		expectedGeneration,
	)
	if err != nil {
		return ZaloCLISession{}, fmt.Errorf("replace Zalo CLI session %s: %w", next.ThreadID, err)
	}
	if err := s.requireZaloCLISessionMutation(result, next.ThreadID); err != nil {
		return ZaloCLISession{}, err
	}
	return s.ZaloCLISession(next.ThreadID)
}

// CompleteZaloCLITurn records one completed turn without allowing its message
// cursor to move backwards.
func (s *Store) CompleteZaloCLITurn(
	threadID string,
	expectedGeneration, contextTokens, messageCursor int64,
	memorySubjectUID string,
	memorySubjectRevision, memoryCommonRevision, memoryRevision, lessonsRevision int64,
) (ZaloCLISession, error) {
	result, err := s.db.Exec(`UPDATE app_zalo_cli_sessions SET
context_tokens = ?, turn_count = turn_count + 1,
message_cursor = MAX(message_cursor, ?), updated_at = ?,
memory_revision = ?, lessons_revision = ?, memory_subject_uid = ?,
memory_subject_revision = ?, memory_common_revision = ?
WHERE thread_id = ? AND generation = ?`,
		contextTokens,
		messageCursor,
		ts(time.Now()),
		memoryRevision,
		lessonsRevision,
		memorySubjectUID,
		memorySubjectRevision,
		memoryCommonRevision,
		threadID,
		expectedGeneration,
	)
	if err != nil {
		return ZaloCLISession{}, fmt.Errorf("complete Zalo CLI turn %s: %w", threadID, err)
	}
	if err := s.requireZaloCLISessionMutation(result, threadID); err != nil {
		return ZaloCLISession{}, err
	}
	return s.ZaloCLISession(threadID)
}

// MarkZaloCLISessionForRotation makes the next caller replace this generation.
func (s *Store) MarkZaloCLISessionForRotation(threadID string, expectedGeneration int64, lastError string) error {
	result, err := s.db.Exec(`UPDATE app_zalo_cli_sessions SET
rotate_before_next = 1, last_error = ?, updated_at = ?
WHERE thread_id = ? AND generation = ?`,
		lastError,
		ts(time.Now()),
		threadID,
		expectedGeneration,
	)
	if err != nil {
		return fmt.Errorf("mark Zalo CLI session %s for rotation: %w", threadID, err)
	}
	return s.requireZaloCLISessionMutation(result, threadID)
}

func (s *Store) requireZaloCLISessionMutation(result sql.Result, threadID string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect Zalo CLI session mutation %s: %w", threadID, err)
	}
	if count == 1 {
		return nil
	}
	if _, err := s.ZaloCLISession(threadID); err != nil {
		return err
	}
	return fmt.Errorf("Zalo CLI session %s: %w", threadID, ErrZaloCLISessionConflict)
}

// LatestZaloMessageID returns the newest durable message ID for a thread.
func (s *Store) LatestZaloMessageID(threadID string) (int64, error) {
	var id int64
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(id), 0) FROM zalo_messages WHERE thread_id = ?`,
		threadID,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("latest Zalo message for %s: %w", threadID, err)
	}
	return id, nil
}

// ZaloMessagesAfter returns at most 100 messages after afterID, oldest first.
func (s *Store) ZaloMessagesAfter(threadID string, afterID int64, limit int) ([]ZaloDeltaMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(`
SELECT m.id, m.thread_id, m.direction, m.author, m.author_uid, m.zalo_msgid,
       CASE WHEN m.cli_msgid <> '' THEN m.cli_msgid ELSE COALESCE(si.cli_msgid, '') END,
       m.quote, COALESCE(u.avatar, ''), m.body, m.sources, m.created_at
FROM zalo_messages m
LEFT JOIN zalo_self_ids si ON si.zalo_msgid = m.zalo_msgid AND m.zalo_msgid <> ''
LEFT JOIN zalo_users u ON u.uid = m.author_uid AND m.author_uid <> ''
WHERE m.thread_id = ? AND m.id > ?
ORDER BY m.id ASC LIMIT ?`, threadID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list Zalo messages after %d for %s: %w", afterID, threadID, err)
	}
	out := make([]ZaloDeltaMessage, 0)
	for rows.Next() {
		var delta ZaloDeltaMessage
		var createdAt, quote string
		if err := rows.Scan(
			&delta.ID,
			&delta.Message.ThreadID,
			&delta.Message.Direction,
			&delta.Message.Author,
			&delta.Message.AuthorUID,
			&delta.Message.ZaloMsgID,
			&delta.Message.CliMsgID,
			&quote,
			&delta.Message.AuthorAvatar,
			&delta.Message.Body,
			&delta.Message.Sources,
			&createdAt,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan Zalo delta message: %w", err)
		}
		delta.Message.ID = delta.ID
		if quote != "" {
			delta.Message.Quote = json.RawMessage(quote)
		}
		if delta.Message.CreatedAt, err = parseTS(createdAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("parse Zalo message %d created_at: %w", delta.ID, err)
		}
		out = append(out, delta)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list Zalo messages after %d for %s: %w", afterID, threadID, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close Zalo messages after %d for %s: %w", afterID, threadID, err)
	}

	messages := make([]ipc.ZaloMessage, len(out))
	for i := range out {
		messages[i] = out[i].Message
	}
	if err := s.loadZaloAttachments(messages); err != nil {
		return nil, err
	}
	if err := s.loadZaloReactions(messages); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Message = messages[i]
	}
	return out, nil
}
