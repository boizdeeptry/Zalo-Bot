package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	appMemoryPendingRetention = 30 * 24 * time.Hour
	appMemoryMaxOperations    = 3
	appMemoryMaxValueRunes    = 240
	appMemoryAutoConfidence   = 0.85
	appMemoryLongDuration     = 180 * 24 * time.Hour
	appMemoryShortDuration    = 30 * 24 * time.Hour
)

var (
	appMemoryKeyPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,79}$`)
	appMemoryAllowedCategories = map[string]struct{}{
		"profile": {}, "family": {}, "interest": {}, "preference": {},
		"health": {}, "financial": {}, "address": {}, "identity": {}, "order": {},
	}
	appMemoryAutoCategories = map[string]time.Duration{
		"profile": appMemoryLongDuration, "family": appMemoryLongDuration,
		"interest": appMemoryShortDuration, "preference": appMemoryShortDuration,
	}
)

type AppMemoryOperation struct {
	Action     string
	MemoryKey  string
	Value      string
	Category   string
	Confidence float64
	TargetID   int64
}

type AppMemoryApplyInput struct {
	ThreadID         string
	SubjectUID       string
	SourceMessageID  int64
	AllowedTargetIDs map[int64]struct{}
	Operations       []AppMemoryOperation
	Now              time.Time
}

type AppMemoryApplyResult struct {
	Active  int
	Pending int
	Ignored int
}

type appValidatedMemoryOperation struct {
	action          string
	memoryKey       string
	value           string
	normalizedValue string
	category        string
	confidence      float64
	targetID        int64
}

type appMemoryMutationOutcome uint8

const (
	appMemoryMutationNoop appMemoryMutationOutcome = iota
	appMemoryMutationActive
	appMemoryMutationPending
	appMemoryMutationIgnored
)

func (s *Store) ApplyAppMemoryOperations(input AppMemoryApplyInput) (AppMemoryApplyResult, error) {
	threadID, subjectUID, err := validateAppPromptMemoryScope(input.ThreadID, input.SubjectUID)
	if err != nil {
		return AppMemoryApplyResult{}, err
	}
	operationCount := len(input.Operations)
	if operationCount > appMemoryMaxOperations {
		operationCount = appMemoryMaxOperations
	}
	if operationCount == 0 {
		return AppMemoryApplyResult{}, nil
	}
	if subjectUID == "" {
		return AppMemoryApplyResult{Ignored: operationCount}, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return AppMemoryApplyResult{}, fmt.Errorf("begin applying Memory operations for %s: %w", threadID, err)
	}
	defer func() { _ = tx.Rollback() }()

	var result AppMemoryApplyResult
	activeChanged := false
	for _, rawOperation := range input.Operations[:operationCount] {
		operation, valid := validateAppMemoryOperation(rawOperation)
		if !valid {
			result.Ignored++
			continue
		}
		outcome, changed, err := appApplyMemoryOperation(
			tx, threadID, subjectUID, input.SourceMessageID,
			input.AllowedTargetIDs, operation, input.Now,
		)
		if err != nil {
			return AppMemoryApplyResult{}, err
		}
		activeChanged = activeChanged || changed
		switch outcome {
		case appMemoryMutationActive:
			result.Active++
		case appMemoryMutationPending:
			result.Pending++
		case appMemoryMutationIgnored:
			result.Ignored++
		}
	}
	if activeChanged {
		if err := appBumpSubjectMemoryRevision(tx, threadID, subjectUID); err != nil {
			return AppMemoryApplyResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AppMemoryApplyResult{}, fmt.Errorf("commit Memory operations for %s: %w", threadID, err)
	}
	return result, nil
}

func validateAppMemoryOperation(operation AppMemoryOperation) (appValidatedMemoryOperation, bool) {
	if !appMemoryKeyPattern.MatchString(operation.MemoryKey) {
		return appValidatedMemoryOperation{}, false
	}
	if _, allowed := appMemoryAllowedCategories[operation.Category]; !allowed {
		return appValidatedMemoryOperation{}, false
	}
	if math.IsNaN(operation.Confidence) || math.IsInf(operation.Confidence, 0) ||
		operation.Confidence < 0 || operation.Confidence > 1 {
		return appValidatedMemoryOperation{}, false
	}
	if !utf8.ValidString(operation.Value) {
		return appValidatedMemoryOperation{}, false
	}
	value := strings.TrimSpace(operation.Value)
	if strings.ContainsAny(value, "\r\n\u0085\u2028\u2029") || len([]rune(value)) > appMemoryMaxValueRunes {
		return appValidatedMemoryOperation{}, false
	}
	switch operation.Action {
	case "add":
		if value == "" || operation.TargetID != 0 {
			return appValidatedMemoryOperation{}, false
		}
	case "replace":
		if value == "" || operation.TargetID <= 0 {
			return appValidatedMemoryOperation{}, false
		}
	case "forget":
		if value != "" || operation.TargetID <= 0 {
			return appValidatedMemoryOperation{}, false
		}
	default:
		return appValidatedMemoryOperation{}, false
	}
	return appValidatedMemoryOperation{
		action: operation.Action, memoryKey: operation.MemoryKey, value: value,
		normalizedValue: appNormalizeMemoryValue(value), category: operation.Category,
		confidence: operation.Confidence, targetID: operation.TargetID,
	}, true
}

func appNormalizeMemoryValue(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func appApplyMemoryOperation(
	tx *sql.Tx,
	threadID, subjectUID string,
	sourceMessageID int64,
	allowedTargetIDs map[int64]struct{},
	operation appValidatedMemoryOperation,
	now time.Time,
) (appMemoryMutationOutcome, bool, error) {
	switch operation.action {
	case "add":
		return appApplyMemoryAdd(tx, threadID, subjectUID, sourceMessageID, operation, now)
	case "replace":
		return appApplyMemoryReplace(
			tx, threadID, subjectUID, sourceMessageID, allowedTargetIDs, operation, now,
		)
	case "forget":
		return appApplyMemoryForget(tx, threadID, subjectUID, allowedTargetIDs, operation, now)
	default:
		return appMemoryMutationIgnored, false, nil
	}
}

type appMemoryActiveRow struct {
	id        int64
	threadID  string
	uid       string
	memoryKey string
	category  string
	text      string
	status    string
	pinned    bool
	expiresAt sql.NullString
}

func appApplyMemoryAdd(
	tx *sql.Tx,
	threadID, subjectUID string,
	sourceMessageID int64,
	operation appValidatedMemoryOperation,
	now time.Time,
) (appMemoryMutationOutcome, bool, error) {
	active, found, err := appFindActiveMemoryByKey(tx, threadID, subjectUID, operation.memoryKey, now)
	if err != nil {
		return appMemoryMutationNoop, false, err
	}
	if found && appNormalizeMemoryValue(active.text) == operation.normalizedValue {
		if err := appConfirmActiveMemory(tx, active, now); err != nil {
			return appMemoryMutationNoop, false, err
		}
		return appMemoryMutationActive, false, nil
	}
	if found {
		if err := appUpsertPendingReplacement(
			tx, threadID, subjectUID, sourceMessageID, active.id, operation, now,
		); err != nil {
			return appMemoryMutationNoop, false, err
		}
		return appMemoryMutationPending, false, nil
	}

	duration, automatic := appMemoryAutoCategories[operation.category]
	if automatic && operation.confidence >= appMemoryAutoConfidence {
		if err := appInsertActiveMemory(
			tx, threadID, subjectUID, sourceMessageID, operation, now, duration,
		); err != nil {
			return appMemoryMutationNoop, false, err
		}
		return appMemoryMutationActive, true, nil
	}
	if err := appInsertPendingAdd(tx, threadID, subjectUID, sourceMessageID, operation, now); err != nil {
		return appMemoryMutationNoop, false, err
	}
	return appMemoryMutationPending, false, nil
}

func appFindActiveMemoryByKey(
	tx *sql.Tx,
	threadID, subjectUID, memoryKey string,
	now time.Time,
) (appMemoryActiveRow, bool, error) {
	var row appMemoryActiveRow
	err := tx.QueryRow(`SELECT id, thread_id, uid, memory_key, category, text, status, pinned, expires_at
FROM zalo_memory
WHERE thread_id = ? AND uid = ? AND memory_key = ? AND status = 'active'
  AND (expires_at IS NULL OR expires_at > ?)
ORDER BY id DESC LIMIT 1`, threadID, subjectUID, memoryKey, ts(now)).Scan(
		&row.id, &row.threadID, &row.uid, &row.memoryKey, &row.category,
		&row.text, &row.status, &row.pinned, &row.expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return appMemoryActiveRow{}, false, nil
	}
	if err != nil {
		return appMemoryActiveRow{}, false, fmt.Errorf("find active Memory by key: %w", err)
	}
	return row, true, nil
}

func appConfirmActiveMemory(tx *sql.Tx, active appMemoryActiveRow, now time.Time) error {
	var expiresAt any
	if !active.pinned {
		expiresAt = ts(now.Add(appMemoryDuration(active.category)))
	}
	if _, err := tx.Exec(`UPDATE zalo_memory
SET last_confirmed_at = ?, expires_at = ?, updated_at = ?
WHERE id = ? AND thread_id = ? AND uid = ? AND status = 'active'`,
		ts(now), expiresAt, ts(now), active.id, active.threadID, active.uid,
	); err != nil {
		return fmt.Errorf("confirm active Memory %d: %w", active.id, err)
	}
	return nil
}

func appMemoryDuration(category string) time.Duration {
	if duration, ok := appMemoryAutoCategories[category]; ok {
		return duration
	}
	return appMemoryLongDuration
}

func appInsertActiveMemory(
	tx *sql.Tx,
	threadID, subjectUID string,
	sourceMessageID int64,
	operation appValidatedMemoryOperation,
	now time.Time,
	duration time.Duration,
) error {
	if _, err := tx.Exec(`INSERT INTO zalo_memory(
thread_id, uid, memory_key, category, text, confidence, status, proposal_action,
supersedes_id, pinned, source, source_message_id, created_at, updated_at,
last_confirmed_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, 'active', '', 0, 0, 'agent', ?, ?, ?, ?, ?)`,
		threadID, subjectUID, operation.memoryKey, operation.category, operation.value,
		operation.confidence, sourceMessageID, ts(now), ts(now), ts(now), ts(now.Add(duration)),
	); err != nil {
		return fmt.Errorf("insert active Memory: %w", err)
	}
	return nil
}

func appInsertPendingAdd(
	tx *sql.Tx,
	threadID, subjectUID string,
	sourceMessageID int64,
	operation appValidatedMemoryOperation,
	now time.Time,
) error {
	if _, err := tx.Exec(`INSERT INTO zalo_memory(
thread_id, uid, memory_key, category, text, confidence, status, proposal_action,
supersedes_id, pinned, source, source_message_id, created_at, updated_at,
last_confirmed_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, 'pending', 'add', 0, 0, 'agent', ?, ?, ?, '', NULL)`,
		threadID, subjectUID, operation.memoryKey, operation.category, operation.value,
		operation.confidence, sourceMessageID, ts(now), ts(now),
	); err != nil {
		return fmt.Errorf("insert pending Memory add: %w", err)
	}
	return nil
}

func appApplyMemoryReplace(
	tx *sql.Tx,
	threadID, subjectUID string,
	sourceMessageID int64,
	allowedTargetIDs map[int64]struct{},
	operation appValidatedMemoryOperation,
	now time.Time,
) (appMemoryMutationOutcome, bool, error) {
	if _, allowed := allowedTargetIDs[operation.targetID]; !allowed {
		return appMemoryMutationIgnored, false, nil
	}
	target, found, err := appReadMemoryTarget(tx, operation.targetID)
	if err != nil {
		return appMemoryMutationNoop, false, err
	}
	if !found || !appMemoryTargetIsActiveInScope(target, threadID, subjectUID, now) {
		return appMemoryMutationIgnored, false, nil
	}
	if err := appUpsertPendingReplacement(
		tx, threadID, subjectUID, sourceMessageID, target.id, operation, now,
	); err != nil {
		return appMemoryMutationNoop, false, err
	}
	return appMemoryMutationPending, false, nil
}

func appReadMemoryTarget(tx *sql.Tx, id int64) (appMemoryActiveRow, bool, error) {
	var row appMemoryActiveRow
	err := tx.QueryRow(`SELECT id, thread_id, uid, memory_key, category, text, status, pinned, expires_at
FROM zalo_memory WHERE id = ?`, id).Scan(
		&row.id, &row.threadID, &row.uid, &row.memoryKey, &row.category,
		&row.text, &row.status, &row.pinned, &row.expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return appMemoryActiveRow{}, false, nil
	}
	if err != nil {
		return appMemoryActiveRow{}, false, fmt.Errorf("read Memory target %d: %w", id, err)
	}
	return row, true, nil
}

func appMemoryTargetIsActiveInScope(
	target appMemoryActiveRow,
	threadID, subjectUID string,
	now time.Time,
) bool {
	if target.threadID != threadID || target.uid != subjectUID || target.status != "active" {
		return false
	}
	if !target.expiresAt.Valid {
		return true
	}
	expiresAt, err := parseTS(target.expiresAt.String)
	return err == nil && expiresAt.After(now)
}

func appUpsertPendingReplacement(
	tx *sql.Tx,
	threadID, subjectUID string,
	sourceMessageID, targetID int64,
	operation appValidatedMemoryOperation,
	now time.Time,
) error {
	var pendingID int64
	err := tx.QueryRow(`SELECT id FROM zalo_memory
WHERE thread_id = ? AND uid = ? AND status = 'pending'
  AND proposal_action = 'replace' AND supersedes_id = ? AND created_at >= ?
ORDER BY id DESC LIMIT 1`,
		threadID, subjectUID, targetID, ts(now.Add(-appMemoryPendingRetention)),
	).Scan(&pendingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("find pending Memory replacement for %d: %w", targetID, err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.Exec(`INSERT INTO zalo_memory(
thread_id, uid, memory_key, category, text, confidence, status, proposal_action,
supersedes_id, pinned, source, source_message_id, created_at, updated_at,
last_confirmed_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, 'pending', 'replace', ?, 0, 'agent', ?, ?, ?, '', NULL)`,
			threadID, subjectUID, operation.memoryKey, operation.category, operation.value,
			operation.confidence, targetID, sourceMessageID, ts(now), ts(now),
		); err != nil {
			return fmt.Errorf("insert pending Memory replacement for %d: %w", targetID, err)
		}
		return nil
	}
	if _, err := tx.Exec(`UPDATE zalo_memory
SET memory_key = ?, category = ?, text = ?, confidence = ?, source = 'agent',
    source_message_id = ?, created_at = ?, updated_at = ?, last_confirmed_at = '',
    expires_at = NULL
WHERE id = ? AND thread_id = ? AND uid = ? AND status = 'pending'
  AND proposal_action = 'replace' AND supersedes_id = ?`,
		operation.memoryKey, operation.category, operation.value, operation.confidence,
		sourceMessageID, ts(now), ts(now), pendingID, threadID, subjectUID, targetID,
	); err != nil {
		return fmt.Errorf("refresh pending Memory replacement %d: %w", pendingID, err)
	}
	if _, err := tx.Exec(`DELETE FROM zalo_memory
WHERE thread_id = ? AND uid = ? AND status = 'pending'
  AND proposal_action = 'replace' AND supersedes_id = ? AND created_at >= ? AND id <> ?`,
		threadID, subjectUID, targetID, ts(now.Add(-appMemoryPendingRetention)), pendingID,
	); err != nil {
		return fmt.Errorf("remove competing Memory replacements for %d: %w", targetID, err)
	}
	return nil
}

func appApplyMemoryForget(
	tx *sql.Tx,
	threadID, subjectUID string,
	allowedTargetIDs map[int64]struct{},
	operation appValidatedMemoryOperation,
	now time.Time,
) (appMemoryMutationOutcome, bool, error) {
	if _, allowed := allowedTargetIDs[operation.targetID]; !allowed {
		return appMemoryMutationIgnored, false, nil
	}
	target, found, err := appReadMemoryTarget(tx, operation.targetID)
	if err != nil {
		return appMemoryMutationNoop, false, err
	}
	if !found {
		return appMemoryMutationNoop, false, nil
	}
	if target.threadID != threadID || target.uid != subjectUID {
		return appMemoryMutationIgnored, false, nil
	}
	if !appMemoryTargetIsActiveInScope(target, threadID, subjectUID, now) {
		return appMemoryMutationNoop, false, nil
	}
	activeChanged, err := appDeleteMemoryLineage(tx, threadID, subjectUID, target.id)
	if err != nil {
		return appMemoryMutationNoop, false, err
	}
	return appMemoryMutationActive, activeChanged, nil
}

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

func appValidateMemoryFields(memoryKey, text, category string) error {
	if !appMemoryKeyPattern.MatchString(memoryKey) {
		return fmt.Errorf("%w: invalid memory key", ErrAppMemoryInvalid)
	}
	if _, ok := appMemoryAllowedCategories[category]; !ok {
		return fmt.Errorf("%w: invalid memory category", ErrAppMemoryInvalid)
	}
	if text == "" || len([]rune(text)) > appMemoryMaxValueRunes ||
		strings.ContainsAny(text, "\r\n\u0085\u2028\u2029") {
		return fmt.Errorf("%w: invalid memory text", ErrAppMemoryInvalid)
	}
	return nil
}

func appRequireExpectedMemoryRevision(
	tx *sql.Tx,
	threadID, uid string,
	expected int64,
	legacy bool,
) error {
	var current int64
	if err := tx.QueryRow(`SELECT COALESCE((SELECT revision
FROM app_memory_subject_revisions WHERE thread_id = ? AND uid = ?), 0)`,
		threadID, uid,
	).Scan(&current); err != nil {
		return fmt.Errorf("read subject Memory revision for %s: %w", threadID, err)
	}
	if !legacy && current != expected {
		return fmt.Errorf("%w: expected %d, current %d", ErrAppMemoryConflict, expected, current)
	}
	return nil
}

func appRequireAvailableActiveMemoryKey(
	tx *sql.Tx,
	threadID, uid, memoryKey string,
	excludeID int64,
) error {
	var conflictID int64
	err := tx.QueryRow(`SELECT id FROM zalo_memory
WHERE thread_id = ? AND uid = ? AND memory_key = ? AND status = 'active' AND id <> ?
ORDER BY id DESC LIMIT 1`, threadID, uid, memoryKey, excludeID).Scan(&conflictID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check active Memory key availability: %w", err)
	}
	return fmt.Errorf("%w: active memory key already exists", ErrAppMemoryConflict)
}

func appBumpMemoryRevisions(tx *sql.Tx, threadID, uid string) error {
	if err := appBumpMemoryRevision(tx, "thread", threadID); err != nil {
		return err
	}
	return appBumpSubjectMemoryRevision(tx, threadID, uid)
}

const appMemoryLineageCTE = `WITH RECURSIVE lineage(id) AS (
  SELECT ?
  UNION
  SELECT CASE WHEN linked.id = lineage.id THEN linked.supersedes_id ELSE linked.id END
  FROM zalo_memory linked JOIN lineage
    ON linked.id = lineage.id OR linked.supersedes_id = lineage.id
  WHERE linked.thread_id = ? AND linked.uid = ? AND linked.supersedes_id > 0
)`

func appDeleteMemoryLineage(
	tx *sql.Tx,
	threadID, uid string,
	id int64,
) (bool, error) {
	var activeCount int
	if err := tx.QueryRow(appMemoryLineageCTE+`
SELECT COUNT(*) FROM zalo_memory
WHERE thread_id = ? AND uid = ? AND status = 'active' AND id IN (SELECT id FROM lineage)`,
		id, threadID, uid, threadID, uid,
	).Scan(&activeCount); err != nil {
		return false, fmt.Errorf("inspect Memory lineage %d: %w", id, err)
	}
	result, err := tx.Exec(appMemoryLineageCTE+`
DELETE FROM zalo_memory
WHERE thread_id = ? AND uid = ? AND id IN (SELECT id FROM lineage)`,
		id, threadID, uid, threadID, uid,
	)
	if err != nil {
		return false, fmt.Errorf("delete Memory lineage %d: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect Memory lineage delete %d: %w", id, err)
	}
	if count == 0 {
		return false, fmt.Errorf("memory %d in %s: %w", id, threadID, ErrAppMemoryNotFound)
	}
	return activeCount > 0, nil
}
