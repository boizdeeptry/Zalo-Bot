package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"agentdc/internal/providercatalog"

	"github.com/google/uuid"
)

var (
	onboardingSnapshotAfterStateRead = func() {}
	// Serial Store tests use this hook to prove that setup revalidates all
	// staging ownership immediately before its compare-and-swap writes.
	onboardingProviderSetupBeforeCAS = func(*sql.Tx) error { return nil }
	// Test seam around the actual Commit call. Production delegates directly to
	// sql.Tx.Commit; serial Store tests may replace and restore it.
	onboardingProvidersCommit = func(tx *sql.Tx) error { return tx.Commit() }
)

const (
	onboardingProviderMutationReceiptKey     = "onboarding_provider_mutation_receipt_v1"
	onboardingProviderMutationReceiptVersion = 1
	onboardingProviderMutationDomain         = "agentdc/onboarding-provider-mutation/v1"
	onboardingProviderMutationReplace        = "replace-selection"
	onboardingProviderMutationBegin          = "begin-provider"
	onboardingProviderMutationBind           = "bind-account"
	onboardingProviderMutationSetup          = "stage-setup"
	onboardingProviderMutationBack           = "back-to-providers"
)

type onboardingProviderMutationReceipt struct {
	Version        int    `json:"version"`
	Operation      string `json:"operation"`
	BaseRevision   int64  `json:"base_revision"`
	ResultRevision int64  `json:"result_revision"`
	PayloadSHA256  string `json:"payload_sha256"`
}

type OnboardingProviderStage struct {
	Kind       string
	Status     string
	ProviderID string
	AccountID  string
	ModelID    string
	Position   int
}

type OnboardingSnapshot struct {
	State  OnboardingState
	Stages []OnboardingProviderStage
}

type onboardingProviderAccountRecord struct {
	Account      LLMAccount
	ProviderKind string
	AddedAt      string
}

// OnboardingSnapshot reads the singleton and its ordered Provider stages from
// one SQLite transaction so callers cannot observe parts of different commits.
func (s *Store) OnboardingSnapshot() (OnboardingSnapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("begin onboarding snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapshot, err := onboardingSnapshotInTx(tx, onboardingSnapshotAfterStateRead)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("commit onboarding snapshot: %w", err)
	}
	return snapshot, nil
}

// ReplaceOnboardingProviderSelection persists the complete selected set while
// preserving every ready row. Request order is ignored; catalog route rank owns
// the durable stage positions.
func (s *Store) ReplaceOnboardingProviderSelection(
	expectedRevision int64,
	kinds []string,
) (OnboardingSnapshot, error) {
	canonicalKinds, err := providercatalog.CanonicalSelectedKinds(kinds)
	if err != nil {
		return OnboardingSnapshot{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("begin onboarding Provider selection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapshot, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if snapshot.State.Revision != expectedRevision {
		if onboardingRevisionIsOneAhead(expectedRevision, snapshot.State.Revision) {
			provenanceMatches, err := onboardingProviderMutationReceiptMatchesInTx(
				tx,
				onboardingProviderMutationReplace,
				expectedRevision,
				snapshot.State.Revision,
				onboardingProviderSelectionPayload(canonicalKinds, snapshot.State.PersonaFingerprint),
			)
			if err != nil {
				return OnboardingSnapshot{}, err
			}
			if provenanceMatches && replaceOnboardingProviderLostResponseMatches(snapshot, canonicalKinds) {
				return commitOnboardingProviderRead(tx, snapshot, "lost-response selection")
			}
		}
		return OnboardingSnapshot{}, onboardingConflict(expectedRevision, snapshot.State.Revision)
	}
	if snapshot.State.Phase != OnboardingPhaseProvider {
		return OnboardingSnapshot{}, fmt.Errorf(
			"replace onboarding Provider selection from %q: %w",
			snapshot.State.Phase,
			ErrOnboardingInvalidPhase,
		)
	}
	if !onboardingProviderInactiveStateIsClean(snapshot.State) {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: Provider phase owns dirty active or receipt fields",
			ErrOnboardingConfigurationChanged,
		)
	}
	if !validOptionalOnboardingPersonaFingerprint(snapshot.State.PersonaFingerprint) {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: Provider phase owns an invalid persona fingerprint",
			ErrOnboardingConfigurationChanged,
		)
	}

	current, err := inspectOnboardingProviderStages(snapshot.Stages)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	selected := make(map[string]struct{}, len(canonicalKinds))
	for _, kind := range canonicalKinds {
		selected[kind] = struct{}{}
	}
	for _, stage := range snapshot.Stages {
		if stage.Status != onboardingProviderStageReady {
			continue
		}
		if _, retained := selected[stage.Kind]; !retained {
			return OnboardingSnapshot{}, fmt.Errorf(
				"%w: ready Provider %q must remain selected",
				ErrOnboardingConfigurationChanged,
				stage.Kind,
			)
		}
	}
	if providerStagesMatchCanonicalSelection(snapshot.Stages, canonicalKinds) {
		return commitOnboardingProviderRead(tx, snapshot, "no-op selection")
	}
	if snapshot.State.Revision >= maxOnboardingRouteRevision {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding revision cannot advance beyond %d",
			ErrOnboardingConflict,
			maxOnboardingRouteRevision,
		)
	}

	updatedAt := ts(time.Now())
	if err := replaceOnboardingProviderRowsInTx(tx, current.byKind, canonicalKinds, updatedAt); err != nil {
		return OnboardingSnapshot{}, err
	}
	phase := OnboardingPhaseProvider
	stagedComboID := ""
	if len(canonicalKinds) > 0 && selectedOnboardingProvidersAreAllReady(current.byKind, canonicalKinds) {
		phase = OnboardingPhasePersona
		stagedComboID = uuid.NewString()
	}
	if err := updateOnboardingProviderSingletonInTx(
		tx,
		expectedRevision,
		phase,
		"",
		stagedComboID,
		updatedAt,
	); err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := writeOnboardingProviderMutationReceiptInTx(
		tx,
		onboardingProviderMutationReplace,
		expectedRevision,
		expectedRevision+1,
		onboardingProviderSelectionPayload(canonicalKinds, snapshot.State.PersonaFingerprint),
	); err != nil {
		return OnboardingSnapshot{}, err
	}
	updated, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := onboardingProvidersCommit(tx); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("commit onboarding Provider selection: %w", err)
	}
	return updated, nil
}

// BeginOnboardingProvider assigns exactly one selected pending Provider to the
// singleton Connect slot. Other selected rows remain unchanged.
func (s *Store) BeginOnboardingProvider(
	expectedRevision int64,
	kind string,
) (OnboardingSnapshot, error) {
	canonical, err := providercatalog.CanonicalSelectedKinds([]string{kind})
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	kind = canonical[0]

	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("begin onboarding Provider connect: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapshot, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if snapshot.State.Revision != expectedRevision {
		if onboardingRevisionIsOneAhead(expectedRevision, snapshot.State.Revision) {
			provenanceMatches, err := onboardingProviderMutationReceiptMatchesInTx(
				tx,
				onboardingProviderMutationBegin,
				expectedRevision,
				snapshot.State.Revision,
				[]string{kind},
			)
			if err != nil {
				return OnboardingSnapshot{}, err
			}
			if provenanceMatches && beginOnboardingProviderLostResponseMatches(snapshot, kind) {
				return commitOnboardingProviderRead(tx, snapshot, "lost-response begin")
			}
		}
		return OnboardingSnapshot{}, onboardingConflict(expectedRevision, snapshot.State.Revision)
	}
	if snapshot.State.Phase != OnboardingPhaseProvider {
		return OnboardingSnapshot{}, fmt.Errorf(
			"begin onboarding Provider from %q: %w",
			snapshot.State.Phase,
			ErrOnboardingInvalidPhase,
		)
	}
	if !onboardingProviderInactiveStateIsClean(snapshot.State) {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: Provider phase owns dirty active or receipt fields",
			ErrOnboardingConfigurationChanged,
		)
	}
	stages, err := inspectOnboardingProviderStages(snapshot.Stages)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if !stages.positionsCanonical {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: selected Provider positions are not canonical",
			ErrOnboardingConfigurationChanged,
		)
	}
	target, selected := stages.byKind[kind]
	if !selected || target.Status != onboardingProviderStagePending {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: Provider %q is not selected pending staging",
			ErrOnboardingInvalidStagingOwnership,
			kind,
		)
	}
	if snapshot.State.Revision >= maxOnboardingRouteRevision {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding revision cannot advance beyond %d",
			ErrOnboardingConflict,
			maxOnboardingRouteRevision,
		)
	}

	updatedAt := ts(time.Now())
	if err := updateOnboardingProviderSingletonInTx(
		tx,
		expectedRevision,
		OnboardingPhaseConnect,
		kind,
		"",
		updatedAt,
	); err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := writeOnboardingProviderMutationReceiptInTx(
		tx,
		onboardingProviderMutationBegin,
		expectedRevision,
		expectedRevision+1,
		[]string{kind},
	); err != nil {
		return OnboardingSnapshot{}, err
	}
	updated, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := onboardingProvidersCommit(tx); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("commit onboarding Provider begin: %w", err)
	}
	return updated, nil
}

// BackOnboardingToProviders returns an exact Persona/Test draft to Provider
// selection without deleting any staged Provider, Account, model or persona.
// The persisted operation receipt makes a one-revision retry distinguishable
// from an unrelated transition that happens to have the same visible shape.
func (s *Store) BackOnboardingToProviders(expectedRevision int64) (OnboardingSnapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("begin onboarding Back transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapshot, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if snapshot.State.Revision != expectedRevision {
		if onboardingRevisionIsOneAhead(expectedRevision, snapshot.State.Revision) {
			payload := onboardingProviderBackPayload(snapshot)
			provenanceMatches, err := onboardingProviderMutationReceiptMatchesInTx(
				tx,
				onboardingProviderMutationBack,
				expectedRevision,
				snapshot.State.Revision,
				payload,
			)
			if err != nil {
				return OnboardingSnapshot{}, err
			}
			if provenanceMatches && onboardingProviderBackSuccessorMatchesInTx(tx, snapshot) {
				return commitOnboardingProviderRead(tx, snapshot, "Back lost response")
			}
		}
		return OnboardingSnapshot{}, onboardingConflict(expectedRevision, snapshot.State.Revision)
	}
	if snapshot.State.Phase != OnboardingPhasePersona && snapshot.State.Phase != OnboardingPhaseTest {
		return OnboardingSnapshot{}, fmt.Errorf(
			"Back onboarding from %q: %w",
			snapshot.State.Phase,
			ErrOnboardingInvalidPhase,
		)
	}
	if err := validateOnboardingProviderBackSourceInTx(tx, snapshot); err != nil {
		return OnboardingSnapshot{}, err
	}
	if snapshot.State.Revision >= maxOnboardingRouteRevision {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding revision cannot advance beyond %d",
			ErrOnboardingConflict,
			maxOnboardingRouteRevision,
		)
	}

	state := snapshot.State
	updatedAt := ts(time.Now())
	result, err := tx.Exec(`UPDATE app_onboarding_state SET
phase = ?, provider_kind = '', provider_id = '', account_id = '', model_id = '',
staged_combo_id = '', test_nonce_hash = '', test_expires_at = '',
revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ?
  AND provider_kind = '' AND provider_id = '' AND account_id = '' AND model_id = ''
  AND staged_combo_id = ? AND persona_fingerprint = ?
  AND test_nonce_hash = ? AND test_expires_at = ?
  AND completed_version = ? AND restart_in_progress = ?`,
		OnboardingPhaseProvider,
		updatedAt,
		expectedRevision,
		state.Phase,
		state.StagedComboID,
		state.PersonaFingerprint,
		state.TestNonceHash,
		state.TestExpiresAt,
		state.CompletedVersion,
		boolInt(state.RestartInProgress),
	)
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("update onboarding Back transition: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return OnboardingSnapshot{}, ErrOnboardingConflict
	}
	updated, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := writeOnboardingProviderMutationReceiptInTx(
		tx,
		onboardingProviderMutationBack,
		expectedRevision,
		expectedRevision+1,
		onboardingProviderBackPayload(updated),
	); err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := onboardingProvidersCommit(tx); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("commit onboarding Back transition: %w", err)
	}
	return updated, nil
}

const (
	onboardingProviderStagePending = "pending"
	onboardingProviderStageReady   = "ready"
)

type onboardingProviderStageInspection struct {
	byKind             map[string]OnboardingProviderStage
	canonicalKinds     []string
	positionsCanonical bool
}

func onboardingSnapshotInTx(tx *sql.Tx, afterStateRead func()) (OnboardingSnapshot, error) {
	state, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if afterStateRead != nil {
		afterStateRead()
	}
	stages, err := onboardingProviderStagesInTx(tx)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	return OnboardingSnapshot{State: state, Stages: stages}, nil
}

func onboardingProviderStagesInTx(tx *sql.Tx) ([]OnboardingProviderStage, error) {
	rows, err := tx.Query(`SELECT
kind, status, provider_id, account_id, model_id, position
FROM app_onboarding_provider_stages
ORDER BY position`)
	if err != nil {
		return nil, fmt.Errorf("read onboarding Provider stages: %w", err)
	}
	var stages []OnboardingProviderStage
	for rows.Next() {
		var stage OnboardingProviderStage
		if err := rows.Scan(
			&stage.Kind,
			&stage.Status,
			&stage.ProviderID,
			&stage.AccountID,
			&stage.ModelID,
			&stage.Position,
		); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan onboarding Provider stage: %w", err)
		}
		stages = append(stages, stage)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("read onboarding Provider stages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close onboarding Provider stages: %w", err)
	}
	return stages, nil
}

func inspectOnboardingProviderStages(
	stages []OnboardingProviderStage,
) (onboardingProviderStageInspection, error) {
	kinds := make([]string, len(stages))
	byKind := make(map[string]OnboardingProviderStage, len(stages))
	for index, stage := range stages {
		if stage.Position < 0 || stage.Position >= providercatalog.MaxSelectedKinds {
			return onboardingProviderStageInspection{}, fmt.Errorf(
				"%w: Provider %q has unsafe position %d",
				ErrOnboardingConfigurationChanged,
				stage.Kind,
				stage.Position,
			)
		}
		switch stage.Status {
		case onboardingProviderStagePending:
			if stage.ProviderID != "" || stage.AccountID != "" || stage.ModelID != "" {
				return onboardingProviderStageInspection{}, fmt.Errorf(
					"%w: pending Provider %q owns identity fields",
					ErrOnboardingConfigurationChanged,
					stage.Kind,
				)
			}
		case onboardingProviderStageReady:
			if stage.ProviderID == "" || stage.AccountID == "" || stage.ModelID == "" {
				return onboardingProviderStageInspection{}, fmt.Errorf(
					"%w: ready Provider %q is missing identity fields",
					ErrOnboardingConfigurationChanged,
					stage.Kind,
				)
			}
		default:
			return onboardingProviderStageInspection{}, fmt.Errorf(
				"%w: Provider %q has status %q",
				ErrOnboardingConfigurationChanged,
				stage.Kind,
				stage.Status,
			)
		}
		if _, duplicate := byKind[stage.Kind]; duplicate {
			return onboardingProviderStageInspection{}, fmt.Errorf(
				"%w: Provider %q is selected more than once",
				ErrOnboardingConfigurationChanged,
				stage.Kind,
			)
		}
		byKind[stage.Kind] = stage
		kinds[index] = stage.Kind
	}
	canonicalKinds, err := providercatalog.CanonicalSelectedKinds(kinds)
	if err != nil {
		return onboardingProviderStageInspection{}, fmt.Errorf(
			"%w: invalid persisted Provider selection: %v",
			ErrOnboardingConfigurationChanged,
			err,
		)
	}
	positionsCanonical := len(canonicalKinds) == len(stages)
	for position, kind := range canonicalKinds {
		stage, exists := byKind[kind]
		if !exists || stage.Position != position {
			positionsCanonical = false
			break
		}
	}
	return onboardingProviderStageInspection{
		byKind:             byKind,
		canonicalKinds:     canonicalKinds,
		positionsCanonical: positionsCanonical,
	}, nil
}

func canonicalOnboardingProviderKind(kind string) (string, error) {
	canonical, err := providercatalog.CanonicalSelectedKinds([]string{kind})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrOnboardingProviderUnsupported, err)
	}
	return canonical[0], nil
}

func onboardingProviderDisplayName(kind string) (string, error) {
	canonical, err := canonicalOnboardingProviderKind(kind)
	if err != nil {
		return "", err
	}
	for _, option := range providercatalog.Options() {
		if option.Kind == canonical && option.Advertised {
			return option.DisplayName, nil
		}
	}
	return "", fmt.Errorf("%w: Provider catalog metadata is unavailable", ErrOnboardingConfigurationChanged)
}

func onboardingProviderAccountRecordInTx(
	tx *sql.Tx,
	accountID string,
) (onboardingProviderAccountRecord, error) {
	var record onboardingProviderAccountRecord
	var enabled int64
	err := tx.QueryRow(`SELECT
a.id, a.provider_id, a.label, a.email, a.config_dir, a.enabled, a.added_at, p.kind
FROM llm_accounts a
JOIN llm_providers p ON p.id = a.provider_id
WHERE a.id = ?`, accountID).Scan(
		&record.Account.ID,
		&record.Account.ProviderID,
		&record.Account.Label,
		&record.Account.Email,
		&record.Account.ConfigDir,
		&enabled,
		&record.AddedAt,
		&record.ProviderKind,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return onboardingProviderAccountRecord{}, fmt.Errorf(
			"%w: onboarding Account does not exist with its Provider",
			ErrOnboardingInvalidStagingOwnership,
		)
	}
	if err != nil {
		return onboardingProviderAccountRecord{}, fmt.Errorf("read onboarding Account: %w", err)
	}
	record.Account.Enabled = enabled != 0
	if record.AddedAt != "" {
		addedAt, err := parseTS(record.AddedAt)
		if err != nil {
			return onboardingProviderAccountRecord{}, fmt.Errorf(
				"%w: onboarding Account timestamp is invalid",
				ErrOnboardingConfigurationChanged,
			)
		}
		record.Account.AddedAt = &addedAt
	}
	return record, nil
}

func onboardingProviderAccountOwnsSetup(
	record onboardingProviderAccountRecord,
	kind string,
	accountID string,
) bool {
	return record.Account.ID == accountID && record.Account.ProviderID == kind &&
		record.ProviderKind == kind && !record.Account.Enabled && record.Account.ConfigDir != ""
}

func onboardingProviderAccountMatchesRequest(
	record onboardingProviderAccountRecord,
	kind string,
	account LLMAccount,
) bool {
	return onboardingProviderAccountOwnsSetup(record, kind, account.ID) &&
		record.Account.ProviderID == account.ProviderID &&
		record.Account.Label == account.Label && record.Account.Email == account.Email &&
		record.Account.ConfigDir == account.ConfigDir && !account.Enabled &&
		record.AddedAt == formatNullableTS(account.AddedAt)
}

func onboardingProviderModelIsAvailableInTx(
	tx *sql.Tx,
	providerID string,
	modelID string,
) (bool, error) {
	var available int64
	err := tx.QueryRow(`SELECT available FROM llm_models
WHERE provider_id = ? AND model_id = ?`, providerID, modelID).Scan(&available)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read onboarding model: %w", err)
	}
	return available == 1, nil
}

func onboardingProviderBindPayload(
	kind string,
	account LLMAccount,
	stages []OnboardingProviderStage,
) []string {
	return onboardingProviderBindPayloadFields(
		kind,
		account.ID,
		account.ProviderID,
		account.Label,
		account.Email,
		account.ConfigDir,
		strconv.FormatBool(account.Enabled),
		formatNullableTS(account.AddedAt),
		stages,
	)
}

func onboardingProviderBindRecordPayload(
	kind string,
	record onboardingProviderAccountRecord,
	stages []OnboardingProviderStage,
) []string {
	return onboardingProviderBindPayloadFields(
		kind,
		record.Account.ID,
		record.Account.ProviderID,
		record.Account.Label,
		record.Account.Email,
		record.Account.ConfigDir,
		strconv.FormatBool(record.Account.Enabled),
		record.AddedAt,
		stages,
	)
}

func onboardingProviderBindPayloadFields(
	kind string,
	accountID string,
	providerID string,
	label string,
	email string,
	configDir string,
	enabled string,
	addedAt string,
	stages []OnboardingProviderStage,
) []string {
	// Only the SHA-256 digest of this framed payload is persisted; account
	// labels, email and config paths never are.
	payload := make([]string, 0, 8+1+providercatalog.MaxSelectedKinds*6)
	payload = append(payload, kind, accountID, providerID, label, email, configDir, enabled, addedAt)
	return appendOnboardingProviderStagesToPayload(payload, stages)
}

func onboardingProviderSetupPayload(
	kind string,
	accountID string,
	modelID string,
	record onboardingProviderAccountRecord,
	stages []OnboardingProviderStage,
) []string {
	payload := []string{
		kind,
		accountID,
		modelID,
		record.Account.ProviderID,
		record.ProviderKind,
		record.Account.Label,
		record.Account.Email,
		record.Account.ConfigDir,
		strconv.FormatBool(record.Account.Enabled),
		record.AddedAt,
	}
	return appendOnboardingProviderStagesToPayload(payload, stages)
}

func onboardingProviderStagesEqual(left, right []OnboardingProviderStage) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func appendOnboardingProviderStagesToPayload(
	payload []string,
	stages []OnboardingProviderStage,
) []string {
	payload = append(payload, strconv.Itoa(len(stages)))
	for _, stage := range stages {
		payload = append(payload,
			stage.Kind,
			stage.Status,
			stage.ProviderID,
			stage.AccountID,
			stage.ModelID,
			strconv.Itoa(stage.Position),
		)
	}
	return payload
}

func providerStagesMatchCanonicalSelection(stages []OnboardingProviderStage, kinds []string) bool {
	if len(stages) != len(kinds) {
		return false
	}
	for position, kind := range kinds {
		if stages[position].Kind != kind || stages[position].Position != position {
			return false
		}
	}
	return true
}

func replaceOnboardingProviderRowsInTx(
	tx *sql.Tx,
	current map[string]OnboardingProviderStage,
	canonicalKinds []string,
	updatedAt string,
) error {
	// Valid persisted positions are bounded below 8. Moving all rows by 8 first
	// avoids transient UNIQUE(position) collisions while the canonical order changes.
	if _, err := tx.Exec(`UPDATE app_onboarding_provider_stages
SET position = position + ?`, providercatalog.MaxSelectedKinds); err != nil {
		return fmt.Errorf("reserve onboarding Provider positions: %w", err)
	}
	selected := make(map[string]struct{}, len(canonicalKinds))
	for _, kind := range canonicalKinds {
		selected[kind] = struct{}{}
	}
	for kind, stage := range current {
		if _, retained := selected[kind]; retained {
			continue
		}
		if stage.Status != onboardingProviderStagePending {
			return fmt.Errorf(
				"%w: ready Provider %q cannot be removed",
				ErrOnboardingConfigurationChanged,
				kind,
			)
		}
		result, err := tx.Exec(`DELETE FROM app_onboarding_provider_stages
WHERE kind = ? AND status = 'pending'`, kind)
		if err != nil {
			return fmt.Errorf("remove pending onboarding Provider %q: %w", kind, err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return fmt.Errorf("remove pending onboarding Provider %q: %w", kind, ErrOnboardingConflict)
		}
	}
	for position, kind := range canonicalKinds {
		if _, exists := current[kind]; exists {
			result, err := tx.Exec(`UPDATE app_onboarding_provider_stages
SET position = ?, updated_at = ? WHERE kind = ?`, position, updatedAt, kind)
			if err != nil {
				return fmt.Errorf("reindex onboarding Provider %q: %w", kind, err)
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return fmt.Errorf("reindex onboarding Provider %q: %w", kind, ErrOnboardingConflict)
			}
			continue
		}
		if _, err := tx.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, updated_at
) VALUES (?, 'pending', ?, ?)`, kind, position, updatedAt); err != nil {
			return fmt.Errorf("select pending onboarding Provider %q: %w", kind, err)
		}
	}
	return nil
}

func selectedOnboardingProvidersAreAllReady(
	current map[string]OnboardingProviderStage,
	canonicalKinds []string,
) bool {
	for _, kind := range canonicalKinds {
		stage, exists := current[kind]
		if !exists || stage.Status != onboardingProviderStageReady {
			return false
		}
	}
	return true
}

func updateOnboardingProviderSingletonInTx(
	tx *sql.Tx,
	expectedRevision int64,
	phase string,
	providerKind string,
	stagedComboID string,
	updatedAt string,
) error {
	result, err := tx.Exec(`UPDATE app_onboarding_state SET
phase = ?, provider_kind = ?, provider_id = '', account_id = '', model_id = '',
staged_combo_id = ?, test_nonce_hash = '', test_expires_at = '',
revision = revision + 1, updated_at = ?
WHERE id = 1 AND phase = 'provider' AND revision = ?`,
		phase,
		providerKind,
		stagedComboID,
		updatedAt,
		expectedRevision,
	)
	if err != nil {
		return fmt.Errorf("update onboarding Provider singleton: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read onboarding Provider singleton update: %w", err)
	}
	if changed != 1 {
		return onboardingConflict(expectedRevision, expectedRevision)
	}
	return nil
}

func onboardingProviderInactiveStateIsClean(state OnboardingState) bool {
	return state.ProviderKind == "" && state.ProviderID == "" && state.AccountID == "" &&
		state.ModelID == "" && state.StagedComboID == "" &&
		state.TestNonceHash == "" && state.TestExpiresAt == ""
}

func onboardingProviderConnectSuccessorIsClean(state OnboardingState, kind string) bool {
	return state.Phase == OnboardingPhaseConnect && state.ProviderKind == kind &&
		state.ProviderID == "" && state.AccountID == "" && state.ModelID == "" &&
		state.StagedComboID == "" && state.TestNonceHash == "" && state.TestExpiresAt == ""
}

func replaceOnboardingProviderLostResponseMatches(
	snapshot OnboardingSnapshot,
	canonicalKinds []string,
) bool {
	if !validOptionalOnboardingPersonaFingerprint(snapshot.State.PersonaFingerprint) {
		return false
	}
	stages, err := inspectOnboardingProviderStages(snapshot.Stages)
	if err != nil || !stages.positionsCanonical ||
		!providerStagesMatchCanonicalSelection(snapshot.Stages, canonicalKinds) {
		return false
	}
	allReady := len(canonicalKinds) > 0 &&
		selectedOnboardingProvidersAreAllReady(stages.byKind, canonicalKinds)
	switch snapshot.State.Phase {
	case OnboardingPhaseProvider:
		if !onboardingProviderInactiveStateIsClean(snapshot.State) {
			return false
		}
		return len(canonicalKinds) == 0 || !allReady
	case OnboardingPhasePersona:
		if snapshot.State.ProviderKind != "" || snapshot.State.ProviderID != "" ||
			snapshot.State.AccountID != "" || snapshot.State.ModelID != "" ||
			snapshot.State.TestNonceHash != "" || snapshot.State.TestExpiresAt != "" ||
			!allReady {
			return false
		}
		_, err := uuid.Parse(snapshot.State.StagedComboID)
		return err == nil
	default:
		return false
	}
}

func onboardingProviderSelectionPayload(
	canonicalKinds []string,
	personaFingerprint string,
) []string {
	payload := make([]string, 0, len(canonicalKinds)+2)
	payload = append(payload, strconv.Itoa(len(canonicalKinds)))
	payload = append(payload, canonicalKinds...)
	return append(payload, personaFingerprint)
}

func validOptionalOnboardingPersonaFingerprint(value string) bool {
	return value == "" || validOnboardingSHA256Hex(value)
}

func beginOnboardingProviderLostResponseMatches(snapshot OnboardingSnapshot, kind string) bool {
	if !onboardingProviderConnectSuccessorIsClean(snapshot.State, kind) {
		return false
	}
	stages, err := inspectOnboardingProviderStages(snapshot.Stages)
	if err != nil || !stages.positionsCanonical {
		return false
	}
	target, selected := stages.byKind[kind]
	return selected && target.Status == onboardingProviderStagePending
}

func onboardingProviderBindLostResponseMatchesInTx(
	tx *sql.Tx,
	snapshot OnboardingSnapshot,
	kind string,
	account LLMAccount,
) bool {
	state := snapshot.State
	if state.Phase != OnboardingPhaseSetup || state.ProviderKind != kind ||
		state.ProviderID != kind || state.AccountID != account.ID || state.ModelID != "" ||
		state.StagedComboID != "" || state.TestNonceHash != "" || state.TestExpiresAt != "" {
		return false
	}
	stages, err := inspectOnboardingProviderStages(snapshot.Stages)
	if err != nil || !stages.positionsCanonical {
		return false
	}
	target, selected := stages.byKind[kind]
	if !selected || target.Status != onboardingProviderStagePending {
		return false
	}
	record, err := onboardingProviderAccountRecordInTx(tx, account.ID)
	return err == nil && onboardingProviderAccountMatchesRequest(record, kind, account)
}

func onboardingProviderSetupLostResponseMatchesInTx(
	tx *sql.Tx,
	snapshot OnboardingSnapshot,
	kind string,
	accountID string,
	modelID string,
) bool {
	stages, err := inspectOnboardingProviderStages(snapshot.Stages)
	if err != nil || !stages.positionsCanonical {
		return false
	}
	target, selected := stages.byKind[kind]
	if !selected || target.Status != onboardingProviderStageReady ||
		target.ProviderID != kind || target.AccountID != accountID || target.ModelID != modelID {
		return false
	}
	record, err := onboardingProviderAccountRecordInTx(tx, accountID)
	if err != nil || !onboardingProviderAccountOwnsSetup(record, kind, accountID) {
		return false
	}
	available, err := onboardingProviderModelIsAvailableInTx(tx, kind, modelID)
	if err != nil || !available {
		return false
	}
	allReady := selectedOnboardingProvidersAreAllReady(stages.byKind, stages.canonicalKinds)
	state := snapshot.State
	activeClean := state.ProviderKind == "" && state.ProviderID == "" &&
		state.AccountID == "" && state.ModelID == "" &&
		state.TestNonceHash == "" && state.TestExpiresAt == ""
	if !activeClean {
		return false
	}
	if allReady {
		if state.Phase != OnboardingPhasePersona || state.PersonaFingerprint != "" {
			return false
		}
		parsed, err := uuid.Parse(state.StagedComboID)
		return err == nil && parsed.String() == state.StagedComboID
	}
	return state.Phase == OnboardingPhaseProvider && state.StagedComboID == ""
}

func onboardingProviderBackPayload(snapshot OnboardingSnapshot) []string {
	payload := []string{
		strconv.FormatInt(snapshot.State.CompletedVersion, 10),
		strconv.FormatBool(snapshot.State.RestartInProgress),
		snapshot.State.PersonaFingerprint,
	}
	return appendOnboardingProviderStagesToPayload(payload, snapshot.Stages)
}

func validateOnboardingProviderBackSourceInTx(tx *sql.Tx, snapshot OnboardingSnapshot) error {
	state := snapshot.State
	if state.ProviderKind != "" || state.ProviderID != "" || state.AccountID != "" ||
		state.ModelID != "" || state.StagedComboID == "" {
		return fmt.Errorf(
			"%w: Back source owns invalid active staging identity",
			ErrOnboardingInvalidStagingOwnership,
		)
	}
	comboID, err := uuid.Parse(state.StagedComboID)
	if err != nil || comboID.String() != state.StagedComboID {
		return fmt.Errorf(
			"%w: Back source has an invalid staged Combo",
			ErrOnboardingConfigurationChanged,
		)
	}
	if state.CompletedVersion >= CurrentOnboardingVersion && !state.RestartInProgress {
		return fmt.Errorf("%w: completed onboarding is not an active draft", ErrOnboardingInvalidPhase)
	}
	if state.RestartInProgress && state.CompletedVersion < CurrentOnboardingVersion {
		return fmt.Errorf("%w: onboarding restart markers are inconsistent", ErrOnboardingConfigurationChanged)
	}
	if state.Phase == OnboardingPhasePersona {
		if !validOptionalOnboardingPersonaFingerprint(state.PersonaFingerprint) ||
			state.TestNonceHash != "" || state.TestExpiresAt != "" {
			return fmt.Errorf("%w: Persona Back source owns a Test receipt", ErrOnboardingConfigurationChanged)
		}
	} else if !validOnboardingProviderBackTestReceipt(state) {
		return fmt.Errorf("%w: Test Back source has an invalid receipt", ErrOnboardingConfigurationChanged)
	}
	return validateOnboardingProviderBackReadyStagesInTx(tx, snapshot.Stages)
}

func onboardingProviderBackSuccessorMatchesInTx(tx *sql.Tx, snapshot OnboardingSnapshot) bool {
	state := snapshot.State
	if state.Phase != OnboardingPhaseProvider || state.ProviderKind != "" ||
		state.ProviderID != "" || state.AccountID != "" || state.ModelID != "" ||
		state.StagedComboID != "" || state.TestNonceHash != "" || state.TestExpiresAt != "" ||
		!validOptionalOnboardingPersonaFingerprint(state.PersonaFingerprint) ||
		(state.CompletedVersion >= CurrentOnboardingVersion && !state.RestartInProgress) ||
		(state.RestartInProgress && state.CompletedVersion < CurrentOnboardingVersion) {
		return false
	}
	return validateOnboardingProviderBackReadyStagesInTx(tx, snapshot.Stages) == nil
}

func validateOnboardingProviderBackReadyStagesInTx(
	tx *sql.Tx,
	stages []OnboardingProviderStage,
) error {
	inspection, err := inspectOnboardingProviderStages(stages)
	if err != nil {
		return err
	}
	if len(stages) == 0 || !inspection.positionsCanonical {
		return fmt.Errorf(
			"%w: Back requires a nonempty canonical Provider stage set",
			ErrOnboardingConfigurationChanged,
		)
	}
	for _, stage := range stages {
		if stage.Status != onboardingProviderStageReady || stage.ProviderID != stage.Kind {
			return fmt.Errorf(
				"%w: Back requires every selected Provider to be ready",
				ErrOnboardingConfigurationChanged,
			)
		}
		var providerKind string
		if err := tx.QueryRow(`SELECT kind FROM llm_providers WHERE id = ?`, stage.ProviderID).
			Scan(&providerKind); err != nil || providerKind != stage.Kind {
			return fmt.Errorf(
				"%w: staged Provider reference changed",
				ErrOnboardingConfigurationChanged,
			)
		}
		account, err := onboardingProviderAccountRecordInTx(tx, stage.AccountID)
		if err != nil || !onboardingProviderAccountOwnsSetup(account, stage.Kind, stage.AccountID) {
			return fmt.Errorf(
				"%w: staged Account reference changed",
				ErrOnboardingConfigurationChanged,
			)
		}
		available, err := onboardingProviderModelIsAvailableInTx(tx, stage.ProviderID, stage.ModelID)
		if err != nil || !available {
			return fmt.Errorf(
				"%w: staged model reference changed",
				ErrOnboardingConfigurationChanged,
			)
		}
	}
	return nil
}

func validOnboardingProviderBackTestReceipt(state OnboardingState) bool {
	if !validOnboardingSHA256Hex(state.PersonaFingerprint) ||
		!validOnboardingSHA256Hex(state.TestNonceHash) ||
		state.TestExpiresAt == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, state.TestExpiresAt)
	if err != nil || expiresAt.Format(time.RFC3339Nano) != state.TestExpiresAt {
		return false
	}
	_, offset := expiresAt.Zone()
	return offset == 0
}

func writeOnboardingProviderMutationReceiptInTx(
	tx *sql.Tx,
	operation string,
	baseRevision int64,
	resultRevision int64,
	payload []string,
) error {
	if !validOnboardingProviderMutationOperation(operation) ||
		!onboardingRevisionIsOneAhead(baseRevision, resultRevision) {
		return fmt.Errorf(
			"%w: invalid onboarding Provider mutation receipt boundary",
			ErrOnboardingConfigurationChanged,
		)
	}
	receipt := onboardingProviderMutationReceipt{
		Version:        onboardingProviderMutationReceiptVersion,
		Operation:      operation,
		BaseRevision:   baseRevision,
		ResultRevision: resultRevision,
		PayloadSHA256:  onboardingProviderMutationPayloadSHA256(operation, payload),
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode onboarding Provider mutation receipt: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO app_meta(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		onboardingProviderMutationReceiptKey,
		string(encoded),
	); err != nil {
		return fmt.Errorf("write onboarding Provider mutation receipt: %w", err)
	}
	return nil
}

func onboardingProviderMutationReceiptMatchesInTx(
	tx *sql.Tx,
	operation string,
	baseRevision int64,
	resultRevision int64,
	payload []string,
) (bool, error) {
	if !validOnboardingProviderMutationOperation(operation) ||
		!onboardingRevisionIsOneAhead(baseRevision, resultRevision) {
		return false, nil
	}
	var encoded string
	err := tx.QueryRow(`SELECT value FROM app_meta WHERE key = ?`,
		onboardingProviderMutationReceiptKey,
	).Scan(&encoded)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read onboarding Provider mutation receipt: %w", err)
	}
	receipt, valid := parseOnboardingProviderMutationReceipt(encoded)
	if !valid {
		return false, nil
	}
	return receipt.Version == onboardingProviderMutationReceiptVersion &&
		receipt.Operation == operation &&
		receipt.BaseRevision == baseRevision &&
		receipt.ResultRevision == resultRevision &&
		receipt.PayloadSHA256 == onboardingProviderMutationPayloadSHA256(operation, payload), nil
}

func parseOnboardingProviderMutationReceipt(
	encoded string,
) (onboardingProviderMutationReceipt, bool) {
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var receipt onboardingProviderMutationReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return onboardingProviderMutationReceipt{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return onboardingProviderMutationReceipt{}, false
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || string(canonical) != encoded {
		return onboardingProviderMutationReceipt{}, false
	}
	if receipt.Version != onboardingProviderMutationReceiptVersion ||
		!validOnboardingProviderMutationOperation(receipt.Operation) ||
		receipt.BaseRevision <= 0 ||
		!onboardingRevisionIsOneAhead(receipt.BaseRevision, receipt.ResultRevision) ||
		!validOnboardingSHA256Hex(receipt.PayloadSHA256) {
		return onboardingProviderMutationReceipt{}, false
	}
	return receipt, true
}

func onboardingProviderMutationPayloadSHA256(operation string, payload []string) string {
	var framed strings.Builder
	writeFrame := func(value string) {
		framed.WriteString(fmt.Sprintf("%d:", len(value)))
		framed.WriteString(value)
	}
	writeFrame(onboardingProviderMutationDomain)
	writeFrame(operation)
	writeFrame(fmt.Sprintf("%d", len(payload)))
	for _, value := range payload {
		writeFrame(value)
	}
	digest := sha256.Sum256([]byte(framed.String()))
	return fmt.Sprintf("%x", digest[:])
}

func validOnboardingProviderMutationOperation(operation string) bool {
	return operation == onboardingProviderMutationReplace ||
		operation == onboardingProviderMutationBegin ||
		operation == onboardingProviderMutationBind ||
		operation == onboardingProviderMutationSetup ||
		operation == onboardingProviderMutationBack
}

func onboardingRevisionIsOneAhead(expectedRevision, currentRevision int64) bool {
	return expectedRevision > 0 && expectedRevision < maxOnboardingRouteRevision &&
		currentRevision == expectedRevision+1
}

func commitOnboardingProviderRead(
	tx *sql.Tx,
	snapshot OnboardingSnapshot,
	operation string,
) (OnboardingSnapshot, error) {
	if err := tx.Commit(); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("commit onboarding Provider %s: %w", operation, err)
	}
	return snapshot, nil
}
