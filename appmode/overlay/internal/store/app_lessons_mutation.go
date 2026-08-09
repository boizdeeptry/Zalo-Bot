package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

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
