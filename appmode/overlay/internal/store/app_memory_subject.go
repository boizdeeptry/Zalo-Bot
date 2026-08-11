package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type AppMemorySubject struct {
	MessageID  int64
	UID        string
	ThreadType string
}

// AppInboundMemorySubject resolves the current Memory subject only from the
// durable inbound row identified by the exact thread and Zalo message IDs.
func (s *Store) AppInboundMemorySubject(threadID, zaloMsgID string) (AppMemorySubject, error) {
	if err := validateAppMemoryIdentity(threadID, true); err != nil {
		return AppMemorySubject{}, err
	}
	if err := validateAppMemoryIdentity(zaloMsgID, true); err != nil {
		return AppMemorySubject{}, err
	}

	var subject AppMemorySubject
	err := s.db.QueryRow(`SELECT m.id, m.author_uid, t.thread_type
FROM zalo_messages m
JOIN zalo_threads t ON t.id = m.thread_id
WHERE m.thread_id = ? AND m.zalo_msgid = ? AND m.direction = 'in'
ORDER BY m.id DESC LIMIT 1`, threadID, zaloMsgID).Scan(
		&subject.MessageID, &subject.UID, &subject.ThreadType,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AppMemorySubject{}, nil
	}
	if err != nil {
		return AppMemorySubject{}, fmt.Errorf("resolve inbound Memory subject: %w", err)
	}
	if err := validateAppMemoryIdentity(subject.UID, false); err != nil {
		return AppMemorySubject{}, fmt.Errorf("%w: invalid durable Memory subject", ErrAppMemoryInvalid)
	}
	if subject.UID == "" && subject.ThreadType == "user" {
		subject.UID = threadID
	}
	return subject, nil
}
