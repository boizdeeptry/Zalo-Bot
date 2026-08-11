package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"agentdc/internal/ipc"
)

type AppMemoryDecisionInput struct {
	UID              string    `json:"uid"`
	MemoryKey        string    `json:"memory_key"`
	Text             string    `json:"text"`
	Category         string    `json:"category"`
	ExpectedRevision int64     `json:"expected_revision"`
	Now              time.Time `json:"-"`
}

func (s *Store) CreateAppThreadMemory(
	threadID string,
	input AppThreadMemoryInput,
) (AppThreadMemory, error) {
	return s.createAppThreadMemory(threadID, input, true)
}

func (s *Store) CreateAppThreadMemoryV2(
	threadID string,
	input AppThreadMemoryInput,
) (AppThreadMemory, error) {
	return s.createAppThreadMemory(threadID, input, false)
}

func (s *Store) createAppThreadMemory(
	threadID string,
	input AppThreadMemoryInput,
	legacy bool,
) (AppThreadMemory, error) {
	input, err := validateAppThreadMemoryInput(threadID, input)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if input.Category == "" {
		input.Category = "profile"
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("begin Memory create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	uid, threadType, err := appResolveMemoryMutationScope(tx, threadID, input.UID)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if err := appRequireMemoryMemberForCreate(tx, threadID, uid, threadType); err != nil {
		return AppThreadMemory{}, err
	}
	if err := appRequireExpectedMemoryRevision(
		tx, threadID, uid, input.ExpectedRevision, legacy,
	); err != nil {
		return AppThreadMemory{}, err
	}
	if input.MemoryKey != "" {
		if err := appRequireAvailableActiveMemoryKey(
			tx, threadID, uid, input.MemoryKey, 0,
		); err != nil {
			return AppThreadMemory{}, err
		}
	}
	if input.Pinned {
		if err := appEnforceMemoryPinLimit(tx, threadID, uid, 0); err != nil {
			return AppThreadMemory{}, err
		}
	}

	now := time.Now()
	var expiresAt any
	if !input.Pinned {
		expiresAt = ts(now.Add(appMemoryDuration(input.Category)))
	}
	result, err := tx.Exec(`INSERT INTO zalo_memory(
thread_id, uid, memory_key, category, text, confidence, status, proposal_action,
supersedes_id, pinned, source, source_message_id, created_at, updated_at,
last_confirmed_at, expires_at)
VALUES (?, ?, ?, ?, ?, 1, 'active', '', 0, ?, 'operator', 0, ?, ?, ?, ?)`,
		threadID, uid, input.MemoryKey, input.Category, input.Text, input.Pinned,
		ts(now), ts(now), ts(now), expiresAt,
	)
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("create Memory for %s: %w", threadID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("read created Memory ID for %s: %w", threadID, err)
	}
	if input.MemoryKey == "" {
		if _, err := tx.Exec(`UPDATE zalo_memory SET memory_key = ?
WHERE id = ? AND thread_id = ? AND uid = ?`,
			fmt.Sprintf("manual.%d", id), id, threadID, uid,
		); err != nil {
			return AppThreadMemory{}, fmt.Errorf("assign manual Memory key %d: %w", id, err)
		}
	}
	if err := appBumpMemoryRevisions(tx, threadID, uid); err != nil {
		return AppThreadMemory{}, err
	}
	created, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppThreadMemory{}, fmt.Errorf("commit Memory create for %s: %w", threadID, err)
	}
	return created, nil
}

func (s *Store) UpdateAppThreadMemory(
	threadID string,
	id int64,
	input AppThreadMemoryInput,
) (AppThreadMemory, error) {
	return s.updateAppThreadMemory(threadID, id, input, true)
}

func (s *Store) UpdateAppThreadMemoryV2(
	threadID string,
	id int64,
	input AppThreadMemoryInput,
) (AppThreadMemory, error) {
	return s.updateAppThreadMemory(threadID, id, input, false)
}

func (s *Store) updateAppThreadMemory(
	threadID string,
	id int64,
	input AppThreadMemoryInput,
	legacy bool,
) (AppThreadMemory, error) {
	input, err := validateAppThreadMemoryInput(threadID, input)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if id <= 0 {
		return AppThreadMemory{}, fmt.Errorf("%w: memory ID must be positive", ErrAppMemoryInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("begin Memory update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	uid, _, err := appResolveMemoryMutationScope(tx, threadID, input.UID)
	if err != nil {
		return AppThreadMemory{}, err
	}
	current, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if current.Status != "active" && current.Status != "expired" {
		return AppThreadMemory{}, fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	if err := appRequireExpectedMemoryRevision(
		tx, threadID, uid, input.ExpectedRevision, legacy,
	); err != nil {
		return AppThreadMemory{}, err
	}
	if input.MemoryKey == "" {
		input.MemoryKey = current.MemoryKey
	}
	if input.Category == "" {
		input.Category = current.Category
	}
	if err := appValidateMemoryFields(input.MemoryKey, input.Text, input.Category); err != nil {
		return AppThreadMemory{}, err
	}
	if current.Status == "active" || input.Pinned {
		if err := appRequireAvailableActiveMemoryKey(
			tx, threadID, uid, input.MemoryKey, id,
		); err != nil {
			return AppThreadMemory{}, err
		}
	}
	if input.Pinned && !current.Pinned {
		if err := appEnforceMemoryPinLimit(tx, threadID, uid, id); err != nil {
			return AppThreadMemory{}, err
		}
	}

	now := time.Now()
	contentChanged := input.Text != current.Text || input.MemoryKey != current.MemoryKey ||
		input.Category != current.Category
	changed := contentChanged || input.Pinned != current.Pinned
	status := current.Status
	lastConfirmedAt := current.LastConfirmedAt
	if contentChanged {
		confirmed := now
		lastConfirmedAt = &confirmed
	}
	if current.Status == "expired" && input.Pinned {
		status = "active"
		confirmed := now
		lastConfirmedAt = &confirmed
		changed = true
	}
	if !changed {
		if err := tx.Commit(); err != nil {
			return AppThreadMemory{}, fmt.Errorf("commit unchanged Memory update %d: %w", id, err)
		}
		return current, nil
	}

	lastConfirmedValue := ""
	if lastConfirmedAt != nil {
		lastConfirmedValue = ts(*lastConfirmedAt)
	}
	var expiresAt any
	if input.Pinned {
		expiresAt = nil
	} else if status == "active" {
		basis := time.Time{}
		if lastConfirmedAt != nil {
			basis = *lastConfirmedAt
		}
		if basis.IsZero() || !basis.Add(appMemoryDuration(input.Category)).After(now) {
			basis = now
			lastConfirmedAt = &basis
			lastConfirmedValue = ts(basis)
		}
		expiresAt = ts(basis.Add(appMemoryDuration(input.Category)))
	} else if current.ExpiresAt != nil {
		expiresAt = ts(*current.ExpiresAt)
	}
	result, err := tx.Exec(`UPDATE zalo_memory
SET memory_key = ?, category = ?, text = ?, pinned = ?, status = ?,
    last_confirmed_at = ?, expires_at = ?, updated_at = ?
WHERE id = ? AND thread_id = ? AND uid = ? AND status IN ('active', 'expired')`,
		input.MemoryKey, input.Category, input.Text, input.Pinned, status,
		lastConfirmedValue, expiresAt, ts(now), id, threadID, uid,
	)
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("update Memory %d in %s: %w", id, threadID, err)
	}
	if err := appRequireSingleMemoryMutation(result, id, threadID); err != nil {
		return AppThreadMemory{}, err
	}
	if current.Status == "active" || status == "active" {
		if err := appBumpMemoryRevisions(tx, threadID, uid); err != nil {
			return AppThreadMemory{}, err
		}
	}
	updated, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppThreadMemory{}, fmt.Errorf("commit Memory update %d in %s: %w", id, threadID, err)
	}
	return updated, nil
}

func (s *Store) DeleteAppThreadMemory(
	threadID string,
	id int64,
	inputs ...AppThreadMemoryInput,
) error {
	if len(inputs) > 1 {
		return fmt.Errorf("%w: one delete input is allowed", ErrAppMemoryInvalid)
	}
	input := AppThreadMemoryInput{}
	legacy := len(inputs) == 0
	if len(inputs) == 1 {
		input = inputs[0]
	}
	return s.deleteAppThreadMemory(threadID, id, input, legacy)
}

func (s *Store) DeleteAppThreadMemoryV2(
	threadID string,
	id int64,
	input AppThreadMemoryInput,
) error {
	return s.deleteAppThreadMemory(threadID, id, input, false)
}

func (s *Store) deleteAppThreadMemory(
	threadID string,
	id int64,
	input AppThreadMemoryInput,
	legacy bool,
) error {
	if err := validateAppMemoryIdentity(threadID, true); err != nil {
		return err
	}
	if err := validateAppMemoryIdentity(input.UID, false); err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("%w: thread and memory IDs are required", ErrAppMemoryInvalid)
	}
	if input.ExpectedRevision < 0 {
		return fmt.Errorf("%w: invalid delete scope or revision", ErrAppMemoryInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin Memory delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	uid, _, err := appResolveMemoryMutationScope(tx, threadID, input.UID)
	if err != nil {
		return err
	}
	current, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return err
	}
	if current.Status != "active" && current.Status != "pending" && current.Status != "expired" {
		return fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	if err := appRequireExpectedMemoryRevision(
		tx, threadID, uid, input.ExpectedRevision, legacy,
	); err != nil {
		return err
	}
	activeChanged := false
	if current.Status == "pending" {
		result, err := tx.Exec(`DELETE FROM zalo_memory
WHERE id = ? AND thread_id = ? AND uid = ? AND status = 'pending'`, id, threadID, uid)
		if err != nil {
			return fmt.Errorf("delete pending Memory %d: %w", id, err)
		}
		if err := appRequireSingleMemoryMutation(result, id, threadID); err != nil {
			return err
		}
	} else {
		activeChanged, err = appDeleteMemoryLineage(tx, threadID, uid, id)
		if err != nil {
			return err
		}
	}
	if activeChanged {
		if err := appBumpMemoryRevisions(tx, threadID, uid); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Memory delete %d in %s: %w", id, threadID, err)
	}
	return nil
}

func (s *Store) ApproveAppThreadMemory(
	threadID string,
	id int64,
	input AppMemoryDecisionInput,
) (AppThreadMemory, error) {
	threadID, input, err := validateAppMemoryDecision(threadID, id, input, true)
	if err != nil {
		return AppThreadMemory{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("begin Memory approval: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	uid, _, err := appResolveMemoryMutationScope(tx, threadID, input.UID)
	if err != nil {
		return AppThreadMemory{}, err
	}
	pending, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if pending.Status != "pending" ||
		(pending.ProposalAction != "add" && pending.ProposalAction != "replace") {
		return AppThreadMemory{}, fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	if err := appRequireExpectedMemoryRevision(
		tx, threadID, uid, input.ExpectedRevision, false,
	); err != nil {
		return AppThreadMemory{}, err
	}
	excludeActiveID := int64(0)
	if pending.ProposalAction == "replace" {
		excludeActiveID = pending.SupersedesID
	}
	if err := appRequireAvailableActiveMemoryKey(
		tx, threadID, uid, input.MemoryKey, excludeActiveID,
	); err != nil {
		return AppThreadMemory{}, err
	}
	if pending.ProposalAction == "replace" {
		if pending.SupersedesID <= 0 {
			return AppThreadMemory{}, fmt.Errorf("%w: replacement target is required", ErrAppMemoryInvalid)
		}
		target, err := appReadMemoryInScopeTx(tx, threadID, uid, pending.SupersedesID)
		if err != nil {
			return AppThreadMemory{}, err
		}
		if target.Status != "active" ||
			(target.ExpiresAt != nil && !target.ExpiresAt.After(input.Now)) {
			return AppThreadMemory{}, fmt.Errorf("memory %d in %s: %w",
				pending.SupersedesID, threadID, ErrAppMemoryNotFound)
		}
		result, err := tx.Exec(`UPDATE zalo_memory SET status = 'superseded', updated_at = ?
WHERE id = ? AND thread_id = ? AND uid = ? AND status = 'active'`,
			ts(input.Now), pending.SupersedesID, threadID, uid,
		)
		if err != nil {
			return AppThreadMemory{}, fmt.Errorf("supersede Memory %d: %w", pending.SupersedesID, err)
		}
		if err := appRequireSingleMemoryMutation(result, pending.SupersedesID, threadID); err != nil {
			return AppThreadMemory{}, err
		}
	}
	expiresAt := ts(input.Now.Add(appMemoryDuration(input.Category)))
	result, err := tx.Exec(`UPDATE zalo_memory
SET status = 'active', proposal_action = '', memory_key = ?, text = ?, category = ?,
    pinned = 0, last_confirmed_at = ?, expires_at = ?, updated_at = ?
WHERE id = ? AND thread_id = ? AND uid = ? AND status = 'pending'`,
		input.MemoryKey, input.Text, input.Category, ts(input.Now), expiresAt,
		ts(input.Now), id, threadID, uid,
	)
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("activate approved Memory %d: %w", id, err)
	}
	if err := appRequireSingleMemoryMutation(result, id, threadID); err != nil {
		return AppThreadMemory{}, err
	}
	if err := appBumpMemoryRevisions(tx, threadID, uid); err != nil {
		return AppThreadMemory{}, err
	}
	approved, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppThreadMemory{}, fmt.Errorf("commit Memory approval %d: %w", id, err)
	}
	return approved, nil
}

func (s *Store) RejectAppThreadMemory(
	threadID string,
	id int64,
	input AppMemoryDecisionInput,
) error {
	threadID, input, err := validateAppMemoryDecision(threadID, id, input, false)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin Memory rejection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	uid, _, err := appResolveMemoryMutationScope(tx, threadID, input.UID)
	if err != nil {
		return err
	}
	pending, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return err
	}
	if pending.Status != "pending" {
		return fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	if err := appRequireExpectedMemoryRevision(
		tx, threadID, uid, input.ExpectedRevision, false,
	); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM zalo_memory
WHERE id = ? AND thread_id = ? AND uid = ? AND status = 'pending'`, id, threadID, uid)
	if err != nil {
		return fmt.Errorf("reject pending Memory %d: %w", id, err)
	}
	if err := appRequireSingleMemoryMutation(result, id, threadID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Memory rejection %d: %w", id, err)
	}
	return nil
}

func (s *Store) RestoreAppThreadMemory(
	threadID string,
	id int64,
	input AppMemoryDecisionInput,
) (AppThreadMemory, error) {
	threadID, input, err := validateAppMemoryDecision(threadID, id, input, false)
	if err != nil {
		return AppThreadMemory{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("begin Memory restore: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	uid, _, err := appResolveMemoryMutationScope(tx, threadID, input.UID)
	if err != nil {
		return AppThreadMemory{}, err
	}
	expired, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if expired.Status != "expired" {
		return AppThreadMemory{}, fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	if err := appRequireExpectedMemoryRevision(
		tx, threadID, uid, input.ExpectedRevision, false,
	); err != nil {
		return AppThreadMemory{}, err
	}
	if err := appRequireAvailableActiveMemoryKey(
		tx, threadID, uid, expired.MemoryKey, expired.ID,
	); err != nil {
		return AppThreadMemory{}, err
	}
	var expiresAt any
	if !expired.Pinned {
		expiresAt = ts(input.Now.Add(appMemoryDuration(expired.Category)))
	}
	result, err := tx.Exec(`UPDATE zalo_memory
SET status = 'active', last_confirmed_at = ?, expires_at = ?, updated_at = ?
WHERE id = ? AND thread_id = ? AND uid = ? AND status = 'expired'`,
		ts(input.Now), expiresAt, ts(input.Now), id, threadID, uid,
	)
	if err != nil {
		return AppThreadMemory{}, fmt.Errorf("restore Memory %d: %w", id, err)
	}
	if err := appRequireSingleMemoryMutation(result, id, threadID); err != nil {
		return AppThreadMemory{}, err
	}
	if err := appBumpMemoryRevisions(tx, threadID, uid); err != nil {
		return AppThreadMemory{}, err
	}
	restored, err := appReadMemoryInScopeTx(tx, threadID, uid, id)
	if err != nil {
		return AppThreadMemory{}, err
	}
	if err := tx.Commit(); err != nil {
		return AppThreadMemory{}, fmt.Errorf("commit Memory restore %d: %w", id, err)
	}
	return restored, nil
}

func validateAppMemoryDecision(
	threadID string,
	id int64,
	input AppMemoryDecisionInput,
	requireEditableFields bool,
) (string, AppMemoryDecisionInput, error) {
	if err := validateAppMemoryIdentity(threadID, true); err != nil {
		return "", AppMemoryDecisionInput{}, err
	}
	if err := validateAppMemoryIdentity(input.UID, false); err != nil {
		return "", AppMemoryDecisionInput{}, err
	}
	input.MemoryKey = strings.TrimSpace(input.MemoryKey)
	input.Text = strings.TrimSpace(input.Text)
	input.Category = strings.TrimSpace(input.Category)
	if id <= 0 {
		return "", AppMemoryDecisionInput{}, fmt.Errorf("%w: thread and memory IDs are required", ErrAppMemoryInvalid)
	}
	if input.ExpectedRevision < 0 {
		return "", AppMemoryDecisionInput{}, fmt.Errorf("%w: expected revision cannot be negative", ErrAppMemoryInvalid)
	}
	if requireEditableFields {
		if err := appValidateMemoryFields(input.MemoryKey, input.Text, input.Category); err != nil {
			return "", AppMemoryDecisionInput{}, err
		}
	}
	if input.Now.IsZero() {
		input.Now = time.Now()
	}
	return threadID, input, nil
}

func validateAppThreadMemoryInput(
	threadID string,
	input AppThreadMemoryInput,
) (AppThreadMemoryInput, error) {
	if err := validateAppMemoryIdentity(threadID, true); err != nil {
		return AppThreadMemoryInput{}, err
	}
	if err := validateAppMemoryIdentity(input.UID, false); err != nil {
		return AppThreadMemoryInput{}, err
	}
	input.Text = strings.TrimSpace(input.Text)
	input.MemoryKey = strings.TrimSpace(input.MemoryKey)
	input.Category = strings.TrimSpace(input.Category)
	if input.Text == "" {
		return AppThreadMemoryInput{}, fmt.Errorf("%w: memory text is required", ErrAppMemoryInvalid)
	}
	if len([]rune(input.Text)) > maxZaloMemoryLen {
		return AppThreadMemoryInput{}, fmt.Errorf("%w: memory exceeds %d runes", ErrAppMemoryInvalid, maxZaloMemoryLen)
	}
	if input.MemoryKey != "" && !appMemoryKeyPattern.MatchString(input.MemoryKey) {
		return AppThreadMemoryInput{}, fmt.Errorf("%w: invalid memory key", ErrAppMemoryInvalid)
	}
	if input.Category != "" {
		if _, ok := appMemoryAllowedCategories[input.Category]; !ok {
			return AppThreadMemoryInput{}, fmt.Errorf("%w: invalid memory category", ErrAppMemoryInvalid)
		}
	}
	if input.ExpectedRevision < 0 {
		return AppThreadMemoryInput{}, fmt.Errorf("%w: expected revision cannot be negative", ErrAppMemoryInvalid)
	}
	return input, nil
}

func appResolveMemoryMutationScope(
	tx *sql.Tx,
	threadID, requestedUID string,
) (string, string, error) {
	if err := validateAppMemoryIdentity(threadID, true); err != nil {
		return "", "", err
	}
	if err := validateAppMemoryIdentity(requestedUID, false); err != nil {
		return "", "", err
	}
	var threadType string
	if err := tx.QueryRow(`SELECT thread_type FROM zalo_threads WHERE id = ?`, threadID).Scan(
		&threadType,
	); errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("thread %s: %w", threadID, ErrAppMemoryNotFound)
	} else if err != nil {
		return "", "", fmt.Errorf("read Memory thread %s: %w", threadID, err)
	}
	if threadType == ipc.ZaloThreadUser {
		if requestedUID != "" && requestedUID != threadID {
			return "", "", fmt.Errorf("memory scope in %s: %w", threadID, ErrAppMemoryNotFound)
		}
		return threadID, threadType, nil
	}
	return requestedUID, threadType, nil
}

func appRequireMemoryMemberForCreate(
	tx *sql.Tx,
	threadID, uid, threadType string,
) error {
	if threadType != ipc.ZaloThreadGroup || uid == "" {
		return nil
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM zalo_group_members
WHERE group_id = ? AND uid = ?`, threadID, uid).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("memory scope in %s: %w", threadID, ErrAppMemoryNotFound)
	} else if err != nil {
		return fmt.Errorf("read Memory member scope in %s: %w", threadID, err)
	}
	return nil
}

func appReadMemoryInScopeTx(
	tx *sql.Tx,
	threadID, uid string,
	id int64,
) (AppThreadMemory, error) {
	out, err := scanAppThreadMemory(tx.QueryRow(appThreadMemorySelect+`
WHERE m.id = ? AND m.thread_id = ? AND m.uid = ?`, id, threadID, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return AppThreadMemory{}, fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	if err != nil {
		return AppThreadMemory{}, err
	}
	return out, nil
}

func appEnforceMemoryPinLimit(
	tx *sql.Tx,
	threadID, uid string,
	excludeID int64,
) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM zalo_memory
WHERE thread_id = ? AND uid = ? AND pinned = 1 AND id <> ?
  AND status IN ('active', 'expired')`, threadID, uid, excludeID).Scan(&count); err != nil {
		return fmt.Errorf("count pinned Memory for %s: %w", threadID, err)
	}
	if count >= appMemoryPinLimit {
		return fmt.Errorf("%w: maximum %d", ErrAppMemoryPinLimit, appMemoryPinLimit)
	}
	return nil
}

func appRequireSingleMemoryMutation(result sql.Result, id int64, threadID string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect Memory mutation %d in %s: %w", id, threadID, err)
	}
	if count != 1 {
		return fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	return nil
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
