package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

const CurrentOnboardingVersion int64 = 1

const onboardingTestReceiptTTL = 10 * time.Minute

const maxOnboardingRouteRevision int64 = 1<<63 - 1

const (
	OnboardingPhaseProvider  = "provider"
	OnboardingPhaseConnect   = "connect"
	OnboardingPhaseSetup     = "setup"
	OnboardingPhasePersona   = "persona"
	OnboardingPhaseTest      = "test"
	OnboardingPhaseCompleted = "completed"
)

var (
	ErrOnboardingStateMissing            = errors.New("onboarding state is missing")
	ErrOnboardingConflict                = errors.New("onboarding revision conflict")
	ErrOnboardingProviderUnsupported     = errors.New("unsupported onboarding provider")
	ErrOnboardingInvalidPhase            = errors.New("invalid onboarding phase")
	ErrOnboardingInvalidStagingOwnership = errors.New("invalid onboarding staging ownership")
	ErrOnboardingModelUnavailable        = errors.New("onboarding model is unavailable")
	ErrOnboardingPersonaMismatch         = errors.New("onboarding persona fingerprint changed")
	ErrOnboardingInvalidTestReceipt      = errors.New("invalid onboarding test receipt")
	ErrOnboardingTestRequired            = errors.New("onboarding verified test receipt required")
	ErrOnboardingTestExpired             = errors.New("onboarding verified test receipt expired")
	ErrOnboardingConfigurationChanged    = errors.New("onboarding configuration changed")
	ErrOnboardingCommitFailed            = errors.New("onboarding completion commit failed")
	onboardingPersonaBeforeCAS           = func(*sql.Tx) error { return nil }
	onboardingTestReceiptBeforeCommit    = func(context.Context) {}
	onboardingCompleteFailpoint          = func(string) error { return nil }
	// Test seam around the actual Commit call. The production default delegates directly to
	// tx.Commit; tests replace it only serially and restore it before returning.
	onboardingCompleteCommit = func(tx *sql.Tx) error { return tx.Commit() }
)

const (
	onboardingCompleteStageAccounts          = "provider-account-enable-disable"
	onboardingCompleteStageMemberDelete      = "member-delete"
	onboardingCompleteStageLegacyRouteDelete = "legacy-route-delete"
	onboardingCompleteStageComboDelete       = "combo-delete"
	onboardingCompleteStageComboInsert       = "combo-insert"
	onboardingCompleteStageMemberInsert      = "member-insert"
	onboardingCompleteStageRouteSync         = "route-projection-revision-sync"
	onboardingCompleteStageStateUpdate       = "final-state-update"
	onboardingCompleteStageBeforeCommit      = "before-commit"
)

// OnboardingTestReceiptPolicy binds receipt validity to the one clock reading
// captured by the caller. Store validates the relationship again inside the
// transaction instead of trusting an arbitrary expiry supplied by HTTP code.
type OnboardingTestReceiptPolicy struct {
	IssuedAt  time.Time
	ExpiresAt time.Time
}

func NewOnboardingTestReceiptPolicy(issuedAt time.Time) OnboardingTestReceiptPolicy {
	return OnboardingTestReceiptPolicy{
		IssuedAt:  issuedAt,
		ExpiresAt: issuedAt.Add(onboardingTestReceiptTTL),
	}
}

type OnboardingState struct {
	CompletedVersion   int64
	Phase              string
	ProviderKind       string
	ProviderID         string
	AccountID          string
	ModelID            string
	StagedComboID      string
	PersonaFingerprint string
	TestNonceHash      string
	TestExpiresAt      string
	RestartInProgress  bool
	Revision           int64
	UpdatedAt          string
}

type OnboardingStagingAccount struct {
	AccountID    string
	ProviderID   string
	ProviderKind string
	ConfigDir    string
}

// OnboardingSetup is the staged Auto-combo descriptor. It deliberately is not a live LLMCombo:
// setup persists only the singleton fields, while completion owns all live routing writes.
type OnboardingSetup struct {
	OnboardingState
	ComboName string
}

// CompleteOnboardingInput carries only the proof and exact external snapshot needed to
// validate completion. Now is captured once by the caller in UTC so expiry has one
// deterministic boundary. StagingConfigDir is optional for Store-only callers; the daemon
// supplies it to detect config identity drift between its rooted path check and this transaction.
type CompleteOnboardingInput struct {
	Revision           int64
	TestNonceHash      string
	PersonaFingerprint string
	RouteFingerprint   string
	StagingConfigDir   string
	Now                time.Time
}

// OnboardingCompletionInput is kept as a descriptive alias for callers that name the operation
// rather than the method. Both names describe the same strictly typed transaction input.
type OnboardingCompletionInput = CompleteOnboardingInput

func IsOnboardingProviderKind(kind string) bool {
	return kind == "codex" || kind == "claude-code"
}

// EnsureOnboardingProviderForKind creates the exact subscription Provider needed by onboarding
// as disabled staging. An existing exact Provider is accepted without changing its enabled state.
func (s *Store) EnsureOnboardingProviderForKind(kind string) error {
	canonicalKind, err := canonicalOnboardingProviderKind(kind)
	if err != nil {
		return err
	}
	kind = canonicalKind
	name, err := onboardingProviderDisplayName(kind)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin onboarding Provider ensure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var existingKind string
	err = tx.QueryRow(`SELECT kind FROM llm_providers WHERE id = ?`, kind).Scan(&existingKind)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO llm_providers(id, name, kind, enabled)
VALUES (?, ?, ?, 0)`, kind, name, kind); err != nil {
			return fmt.Errorf("insert disabled onboarding Provider %q: %w", kind, err)
		}
	case err != nil:
		return fmt.Errorf("read onboarding Provider %q: %w", kind, err)
	case existingKind != kind:
		return fmt.Errorf(
			"%w: Provider %q has kind %q instead of %q",
			ErrOnboardingInvalidStagingOwnership,
			kind,
			existingKind,
			kind,
		)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit onboarding Provider ensure: %w", err)
	}
	return nil
}

// OnboardingState returns the required singleton state. A missing row is an
// explicit error so callers cannot mistake a damaged database for completion.
func (s *Store) OnboardingState() (OnboardingState, error) {
	state, err := scanOnboardingState(s.db.QueryRow(onboardingStateSelect))
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingState{}, ErrOnboardingStateMissing
	}
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding state: %w", err)
	}
	return state, nil
}

// BindOnboardingAccount atomically records a freshly authenticated CLI Account as disabled
// staging and advances the singleton from connect to setup. Store deliberately does not touch
// ConfigDir: the Connect job owns filesystem cleanup unless this transaction commits.
func (s *Store) BindOnboardingAccount(
	expectedRevision int64,
	kind string,
	account LLMAccount,
) (OnboardingSnapshot, error) {
	canonicalKind, err := canonicalOnboardingProviderKind(kind)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	kind = canonicalKind
	if account.ID == "" || account.ProviderID == "" || account.ConfigDir == "" ||
		account.ProviderID != kind || account.Enabled {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding Account must be disabled and match provider kind %q",
			ErrOnboardingInvalidStagingOwnership,
			kind,
		)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("begin onboarding Account bind: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapshot, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if snapshot.State.Revision != expectedRevision {
		if onboardingRevisionIsOneAhead(expectedRevision, snapshot.State.Revision) {
			payload := onboardingProviderBindPayload(kind, account, snapshot.Stages)
			provenanceMatches, err := onboardingProviderMutationReceiptMatchesInTx(
				tx,
				onboardingProviderMutationBind,
				expectedRevision,
				snapshot.State.Revision,
				payload,
			)
			if err != nil {
				return OnboardingSnapshot{}, err
			}
			if provenanceMatches && onboardingProviderBindLostResponseMatchesInTx(tx, snapshot, kind, account) {
				return commitOnboardingProviderRead(tx, snapshot, "lost-response Account bind")
			}
		}
		return OnboardingSnapshot{}, onboardingConflict(expectedRevision, snapshot.State.Revision)
	}
	state := snapshot.State
	if state.Phase != OnboardingPhaseConnect {
		return OnboardingSnapshot{}, fmt.Errorf(
			"bind onboarding Account from %q: %w",
			state.Phase,
			ErrOnboardingInvalidPhase,
		)
	}
	if state.ProviderKind != kind || !onboardingConnectStateIsClean(state) {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding connect state does not own a clean %q staging slot",
			ErrOnboardingInvalidStagingOwnership,
			kind,
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
	if state.Revision >= maxOnboardingRouteRevision {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding revision cannot advance beyond %d",
			ErrOnboardingConflict,
			maxOnboardingRouteRevision,
		)
	}

	var providerKind string
	if err := tx.QueryRow(
		`SELECT kind FROM llm_providers WHERE id = ?`, account.ProviderID,
	).Scan(&providerKind); errors.Is(err, sql.ErrNoRows) {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: Provider %q is missing",
			ErrOnboardingInvalidStagingOwnership,
			account.ProviderID,
		)
	} else if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("read onboarding Provider %q: %w", account.ProviderID, err)
	}
	if providerKind != kind {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: Provider %q does not have kind %q",
			ErrOnboardingInvalidStagingOwnership,
			account.ProviderID,
			kind,
		)
	}

	if _, err := tx.Exec(`INSERT INTO llm_accounts(
id, provider_id, label, email, config_dir, enabled, added_at
) VALUES(?,?,?,?,?,?,?)`,
		account.ID,
		account.ProviderID,
		account.Label,
		account.Email,
		account.ConfigDir,
		0,
		formatNullableTS(account.AddedAt),
	); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: insert onboarding Account %q: %v",
			ErrOnboardingInvalidStagingOwnership,
			account.ID,
			err,
		)
	}

	updatedAt := ts(time.Now())
	result, err := tx.Exec(`UPDATE app_onboarding_state SET
phase = ?, provider_id = ?, account_id = ?, revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ? AND provider_kind = ?
  AND provider_id = '' AND account_id = '' AND model_id = '' AND staged_combo_id = ''
  AND test_nonce_hash = '' AND test_expires_at = ''`,
		OnboardingPhaseSetup,
		account.ProviderID,
		account.ID,
		updatedAt,
		expectedRevision,
		OnboardingPhaseConnect,
		kind,
	)
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("update onboarding Account binding: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("read onboarding Account bind result: %w", err)
	}
	if changed != 1 {
		return OnboardingSnapshot{}, ErrOnboardingConflict
	}
	if err := writeOnboardingProviderMutationReceiptInTx(
		tx,
		onboardingProviderMutationBind,
		expectedRevision,
		expectedRevision+1,
		onboardingProviderBindPayload(kind, account, snapshot.Stages),
	); err != nil {
		return OnboardingSnapshot{}, err
	}
	updated, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := onboardingProvidersCommit(tx); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("commit onboarding Account bind: %w", err)
	}
	return updated, nil
}

// StageOnboardingSetup marks exactly one selected pending Provider ready. If
// another selected row remains pending, the singleton returns to Provider;
// only the last ready row allocates the staged Combo and advances to Persona.
func (s *Store) StageOnboardingSetup(
	expectedRevision int64,
	kind string,
	accountID string,
	modelID string,
) (OnboardingSnapshot, error) {
	canonicalKind, err := canonicalOnboardingProviderKind(kind)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	kind = canonicalKind
	if accountID == "" || modelID == "" {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding setup requires exact Provider, Account and model",
			ErrOnboardingInvalidStagingOwnership,
		)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("begin onboarding setup staging: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	snapshot, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if snapshot.State.Revision != expectedRevision {
		if onboardingRevisionIsOneAhead(expectedRevision, snapshot.State.Revision) {
			record, recordErr := onboardingProviderAccountRecordInTx(tx, accountID)
			if recordErr == nil {
				payload := onboardingProviderSetupPayload(kind, accountID, modelID, record, snapshot.Stages)
				provenanceMatches, err := onboardingProviderMutationReceiptMatchesInTx(
					tx,
					onboardingProviderMutationSetup,
					expectedRevision,
					snapshot.State.Revision,
					payload,
				)
				if err != nil {
					return OnboardingSnapshot{}, err
				}
				if provenanceMatches && onboardingProviderSetupLostResponseMatchesInTx(
					tx,
					snapshot,
					kind,
					accountID,
					modelID,
				) {
					return commitOnboardingProviderRead(tx, snapshot, "lost-response setup")
				}
			}
		}
		return OnboardingSnapshot{}, onboardingConflict(expectedRevision, snapshot.State.Revision)
	}
	state := snapshot.State
	if state.Phase != OnboardingPhaseSetup {
		return OnboardingSnapshot{}, fmt.Errorf(
			"stage onboarding setup from %q: %w",
			state.Phase,
			ErrOnboardingInvalidPhase,
		)
	}
	if state.Revision >= maxOnboardingRouteRevision {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding revision cannot advance beyond %d",
			ErrOnboardingConflict,
			maxOnboardingRouteRevision,
		)
	}
	if state.ProviderKind != kind || state.ProviderID != kind || state.AccountID != accountID ||
		state.ModelID != "" || state.StagedComboID != "" ||
		state.TestNonceHash != "" || state.TestExpiresAt != "" {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding setup request does not match the staged state",
			ErrOnboardingInvalidStagingOwnership,
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
	account, err := onboardingProviderAccountRecordInTx(tx, accountID)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if !onboardingProviderAccountOwnsSetup(account, kind, accountID) {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding Account is not the exact disabled staging owner",
			ErrOnboardingInvalidStagingOwnership,
		)
	}
	bindProvenance, err := onboardingProviderMutationReceiptMatchesInTx(
		tx,
		onboardingProviderMutationBind,
		expectedRevision-1,
		expectedRevision,
		onboardingProviderBindRecordPayload(kind, account, snapshot.Stages),
	)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if !bindProvenance {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding Account configuration changed after bind",
			ErrOnboardingConfigurationChanged,
		)
	}
	available, err := onboardingProviderModelIsAvailableInTx(tx, state.ProviderID, modelID)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if !available {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: selected model is not available for the staged Provider",
			ErrOnboardingModelUnavailable,
		)
	}
	if err := onboardingProviderSetupBeforeCAS(tx); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("before onboarding setup CAS: %w", err)
	}

	// Re-read every owner after the pre-CAS boundary so account/config/model or
	// stage drift cannot be committed between discovery and staging.
	refreshed, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if refreshed.State != state || !onboardingProviderStagesEqual(refreshed.Stages, snapshot.Stages) {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding setup ownership changed before commit",
			ErrOnboardingConfigurationChanged,
		)
	}
	refreshedInspection, err := inspectOnboardingProviderStages(refreshed.Stages)
	if err != nil || !refreshedInspection.positionsCanonical {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding Provider stages changed before commit",
			ErrOnboardingConfigurationChanged,
		)
	}
	refreshedTarget := refreshedInspection.byKind[kind]
	if refreshedTarget != target {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding target stage changed before commit",
			ErrOnboardingConfigurationChanged,
		)
	}
	account, err = onboardingProviderAccountRecordInTx(tx, accountID)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if !onboardingProviderAccountOwnsSetup(account, kind, accountID) {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding Account changed before commit",
			ErrOnboardingInvalidStagingOwnership,
		)
	}
	bindProvenance, err = onboardingProviderMutationReceiptMatchesInTx(
		tx,
		onboardingProviderMutationBind,
		expectedRevision-1,
		expectedRevision,
		onboardingProviderBindRecordPayload(kind, account, refreshed.Stages),
	)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if !bindProvenance {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: onboarding Account configuration changed before commit",
			ErrOnboardingConfigurationChanged,
		)
	}
	available, err = onboardingProviderModelIsAvailableInTx(tx, state.ProviderID, modelID)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if !available {
		return OnboardingSnapshot{}, fmt.Errorf(
			"%w: selected model changed before commit",
			ErrOnboardingModelUnavailable,
		)
	}

	updatedAt := ts(time.Now())
	result, err := tx.Exec(`UPDATE app_onboarding_provider_stages SET
status = 'ready', provider_id = ?, account_id = ?, model_id = ?, updated_at = ?
WHERE kind = ? AND status = 'pending'
  AND provider_id = '' AND account_id = '' AND model_id = ''`,
		state.ProviderID,
		accountID,
		modelID,
		updatedAt,
		kind,
	)
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("mark onboarding Provider ready: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return OnboardingSnapshot{}, ErrOnboardingConflict
	}

	remainingPending := false
	for stageKind, stage := range refreshedInspection.byKind {
		if stageKind != kind && stage.Status == onboardingProviderStagePending {
			remainingPending = true
			break
		}
	}
	phase := OnboardingPhaseProvider
	comboID := ""
	personaFingerprint := state.PersonaFingerprint
	if !remainingPending {
		phase = OnboardingPhasePersona
		comboID = uuid.NewString()
		personaFingerprint = ""
	}
	result, err = tx.Exec(`UPDATE app_onboarding_state SET
phase = ?, provider_kind = '', provider_id = '', account_id = '', model_id = '',
staged_combo_id = ?, persona_fingerprint = ?, test_nonce_hash = '', test_expires_at = '',
revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ? AND provider_kind = ?
  AND provider_id = ? AND account_id = ? AND model_id = '' AND staged_combo_id = ''
  AND persona_fingerprint = ? AND test_nonce_hash = '' AND test_expires_at = ''`,
		phase,
		comboID,
		personaFingerprint,
		updatedAt,
		expectedRevision,
		OnboardingPhaseSetup,
		kind,
		state.ProviderID,
		accountID,
		state.PersonaFingerprint,
	)
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("update onboarding setup singleton: %w", err)
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return OnboardingSnapshot{}, ErrOnboardingConflict
	}
	updated, err := onboardingSnapshotInTx(tx, nil)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := writeOnboardingProviderMutationReceiptInTx(
		tx,
		onboardingProviderMutationSetup,
		expectedRevision,
		expectedRevision+1,
		onboardingProviderSetupPayload(kind, accountID, modelID, account, updated.Stages),
	); err != nil {
		return OnboardingSnapshot{}, err
	}
	if err := onboardingProvidersCommit(tx); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("commit onboarding setup staging: %w", err)
	}
	return updated, nil
}

// AdvanceOnboardingPersona atomically publishes the authoritative display name
// and advances the exact persona revision to the test phase. A prior test
// receipt is always cleared because it cannot attest to the new persona.
func (s *Store) AdvanceOnboardingPersona(
	expectedRevision int64,
	fingerprint string,
	displayName string,
) (OnboardingState, error) {
	return s.advanceOnboardingPersona(expectedRevision, fingerprint, displayName, "")
}

// AdvanceOnboardingPersonaWithRecovery additionally commits the filesystem
// recovery token in the same transaction as the name and phase transition.
func (s *Store) AdvanceOnboardingPersonaWithRecovery(
	expectedRevision int64,
	fingerprint string,
	displayName string,
	recoveryToken string,
) (OnboardingState, error) {
	return s.advanceOnboardingPersona(expectedRevision, fingerprint, displayName, recoveryToken)
}

func (s *Store) advanceOnboardingPersona(
	expectedRevision int64,
	fingerprint string,
	displayName string,
	recoveryToken string,
) (OnboardingState, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("begin onboarding persona advance: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingState{}, err
	}
	if state.Revision != expectedRevision {
		return OnboardingState{}, onboardingConflict(expectedRevision, state.Revision)
	}
	if state.Phase != OnboardingPhasePersona && state.Phase != OnboardingPhaseTest {
		return OnboardingState{}, fmt.Errorf(
			"advance onboarding persona from %q: %w",
			state.Phase,
			ErrOnboardingInvalidPhase,
		)
	}
	if fingerprint == "" || displayName == "" {
		return OnboardingState{}, fmt.Errorf("advance onboarding persona: fingerprint and display name are required")
	}
	if err := setAgentPersonaMetaInTx(tx, displayName, recoveryToken); err != nil {
		return OnboardingState{}, fmt.Errorf("store onboarding persona metadata: %w", err)
	}
	if err := onboardingPersonaBeforeCAS(tx); err != nil {
		return OnboardingState{}, fmt.Errorf("before onboarding persona CAS: %w", err)
	}

	updatedAt := ts(time.Now())
	result, err := tx.Exec(`UPDATE app_onboarding_state SET
phase = ?, persona_fingerprint = ?, test_nonce_hash = '', test_expires_at = '',
revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ?`,
		OnboardingPhaseTest,
		fingerprint,
		updatedAt,
		expectedRevision,
		state.Phase,
	)
	if err != nil {
		return OnboardingState{}, fmt.Errorf("update onboarding persona: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding persona update result: %w", err)
	}
	if changed != 1 {
		return OnboardingState{}, ErrOnboardingConflict
	}
	updated, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OnboardingState{}, fmt.Errorf("commit onboarding persona advance: %w", err)
	}
	return updated, nil
}

// UpdateAgentPersona atomically stores the display name and invalidates any
// active onboarding persona/test result after a normal persona edit. Completed
// onboarding remains completed unless it still carries a stale receipt.
func (s *Store) UpdateAgentPersona(displayName, recoveryToken string) (OnboardingState, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("begin agent persona update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingState{}, err
	}
	if err := setAgentPersonaMetaInTx(tx, displayName, recoveryToken); err != nil {
		return OnboardingState{}, err
	}

	dirtyReceipt := state.PersonaFingerprint != "" || state.TestNonceHash != "" || state.TestExpiresAt != ""
	activePersona := state.Phase == OnboardingPhasePersona || state.Phase == OnboardingPhaseTest
	if activePersona || dirtyReceipt {
		targetPhase := state.Phase
		if activePersona {
			targetPhase = OnboardingPhasePersona
		}
		updatedAt := ts(time.Now())
		result, err := tx.Exec(`UPDATE app_onboarding_state SET
phase = ?, persona_fingerprint = '', test_nonce_hash = '', test_expires_at = '',
revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ?`,
			targetPhase,
			updatedAt,
			state.Revision,
			state.Phase,
		)
		if err != nil {
			return OnboardingState{}, fmt.Errorf("invalidate onboarding after agent persona update: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return OnboardingState{}, fmt.Errorf("read agent persona invalidation result: %w", err)
		}
		if changed != 1 {
			return OnboardingState{}, ErrOnboardingConflict
		}
		state, err = onboardingStateInTx(tx)
		if err != nil {
			return OnboardingState{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return OnboardingState{}, fmt.Errorf("commit agent persona update: %w", err)
	}
	return state, nil
}

// InvalidateOnboardingPersona returns a tested active onboarding flow to its
// persona step and clears any persisted persona receipt. A completed flow stays
// completed, but its stale receipt can no longer attest to the edited document.
func (s *Store) InvalidateOnboardingPersona() (OnboardingState, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("begin onboarding persona invalidation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingState{}, err
	}
	dirtyPersona := state.PersonaFingerprint != "" || state.TestNonceHash != "" || state.TestExpiresAt != ""
	targetPhase := state.Phase
	if state.Phase == OnboardingPhaseTest {
		targetPhase = OnboardingPhasePersona
	}
	if state.Phase != OnboardingPhaseTest && !dirtyPersona {
		if err := tx.Commit(); err != nil {
			return OnboardingState{}, fmt.Errorf("commit no-op onboarding persona invalidation: %w", err)
		}
		return state, nil
	}

	updatedAt := ts(time.Now())
	result, err := tx.Exec(`UPDATE app_onboarding_state SET
phase = ?, persona_fingerprint = '', test_nonce_hash = '', test_expires_at = '',
revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ?`,
		targetPhase,
		updatedAt,
		state.Revision,
		state.Phase,
	)
	if err != nil {
		return OnboardingState{}, fmt.Errorf("invalidate onboarding persona: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding persona invalidation result: %w", err)
	}
	if changed != 1 {
		return OnboardingState{}, ErrOnboardingConflict
	}
	updated, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OnboardingState{}, fmt.Errorf("commit onboarding persona invalidation: %w", err)
	}
	return updated, nil
}

// CompleteOnboarding is the sole owner of onboarding activation. Every validation read and every
// live routing mutation is performed on the same SQLite transaction, so readers continue to see
// the previous route until Commit and any failure restores all affected tables together.
func (s *Store) CompleteOnboarding(
	ctx context.Context,
	input CompleteOnboardingInput,
) (OnboardingState, error) {
	if input.Revision <= 0 || input.PersonaFingerprint == "" ||
		!validOnboardingSHA256Hex(input.TestNonceHash) ||
		!validOnboardingSHA256Hex(input.RouteFingerprint) ||
		!validOnboardingTestReceiptTime(input.Now) {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("begin transaction", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := onboardingStateInTxContext(ctx, tx)
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read singleton", err)
	}
	if state.Revision != input.Revision {
		return OnboardingState{}, onboardingConflict(input.Revision, state.Revision)
	}
	if !onboardingCompletionIsRequired(state) || state.Phase != OnboardingPhaseTest ||
		state.TestNonceHash == "" || state.TestExpiresAt == "" {
		return OnboardingState{}, ErrOnboardingTestRequired
	}
	route, err := onboardingTestRouteInTx(ctx, tx, input.Revision)
	if err != nil {
		switch {
		case errors.Is(err, ErrOnboardingConflict):
			return OnboardingState{}, err
		case errors.Is(err, ErrOnboardingInvalidPhase),
			errors.Is(err, ErrOnboardingInvalidTestReceipt):
			return OnboardingState{}, ErrOnboardingTestRequired
		case errors.Is(err, ErrOnboardingInvalidStagingOwnership),
			errors.Is(err, ErrOnboardingModelUnavailable),
			errors.Is(err, ErrOnboardingConfigurationChanged),
			errors.Is(err, ErrOnboardingProviderUnsupported):
			return OnboardingState{}, ErrOnboardingConfigurationChanged
		default:
			return OnboardingState{}, onboardingCompleteCommitError("read staged Test route", err)
		}
	}
	if route.Fingerprint != input.RouteFingerprint || len(route.Entries) != 1 {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	boundReceipt, err := OnboardingTestReceiptHash(input.TestNonceHash, route.Fingerprint)
	if err != nil {
		return OnboardingState{}, ErrOnboardingTestRequired
	}
	storedHash, ok := decodeOnboardingSHA256Hex(state.TestNonceHash)
	if !ok || subtle.ConstantTimeCompare(storedHash, mustDecodeOnboardingSHA256Hex(boundReceipt)) != 1 {
		return OnboardingState{}, ErrOnboardingTestRequired
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, state.TestExpiresAt)
	if err != nil || !validOnboardingTestReceiptTime(expiresAt) {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	if !expiresAt.After(input.Now) {
		return OnboardingState{}, ErrOnboardingTestExpired
	}
	if state.PersonaFingerprint == "" || state.PersonaFingerprint != input.PersonaFingerprint {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	persistedState := state
	entry := route.Entries[0]
	state.ProviderKind = entry.Kind
	state.ProviderID = entry.ProviderID
	state.AccountID = entry.AccountID
	state.ModelID = entry.ModelID
	if err := validateOnboardingCompletionState(state); err != nil {
		return OnboardingState{}, err
	}
	comboName, err := onboardingComboName(state.ProviderKind)
	if err != nil {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}

	var providerName, providerKind string
	var providerEnabled int64
	err = tx.QueryRowContext(ctx, `SELECT name, kind, enabled FROM llm_providers WHERE id = ?`,
		state.ProviderID).Scan(&providerName, &providerKind, &providerEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read selected Provider", err)
	}
	if providerKind != state.ProviderKind || !validOnboardingCompletionName(providerName) ||
		(providerEnabled != 0 && providerEnabled != 1) {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}

	var accountProviderID, accountLabel, accountConfigDir string
	var accountEnabled int64
	err = tx.QueryRowContext(ctx, `SELECT provider_id, label, config_dir, enabled
FROM llm_accounts WHERE id = ?`, state.AccountID).Scan(
		&accountProviderID, &accountLabel, &accountConfigDir, &accountEnabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read staged Account", err)
	}
	if accountProviderID != state.ProviderID || accountEnabled != 0 ||
		!validOnboardingCompletionName(accountLabel) || strings.TrimSpace(accountConfigDir) == "" ||
		(input.StagingConfigDir != "" && accountConfigDir != input.StagingConfigDir) {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}

	var modelName string
	var modelAvailable int64
	err = tx.QueryRowContext(ctx, `SELECT name, available FROM llm_models
WHERE provider_id = ? AND model_id = ?`, state.ProviderID, state.ModelID).Scan(
		&modelName, &modelAvailable,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read staged model", err)
	}
	if modelAvailable != 1 || !validOnboardingCompletionName(modelName) {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}

	var routeRevisionText string
	err = tx.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE key = 'llm_route_revision'`).
		Scan(&routeRevisionText)
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read route revision", err)
	}
	routeRevision, err := strconv.ParseInt(routeRevisionText, 10, 64)
	if err != nil || routeRevision < 0 || routeRevision == maxOnboardingRouteRevision {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	var comboCount, maximumComboRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(revision), 0)
FROM llm_combos`).Scan(&comboCount, &maximumComboRevision); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read maximum Combo revision", err)
	}
	if comboCount < 0 || maximumComboRevision < 0 || maximumComboRevision == maxOnboardingRouteRevision {
		return OnboardingState{}, ErrOnboardingConfigurationChanged
	}
	var legacyRouteCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_route_entries`).Scan(&legacyRouteCount); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read legacy route projection count", err)
	}
	nextRouteRevision := int64(1)
	// app_meta is seeded to revision 1 before a first route exists. That one pristine shape may
	// create revision 1; any materialized route state or later metadata is revision history and
	// must advance monotonically even if projection rows were externally removed.
	if comboCount > 0 || legacyRouteCount > 0 || routeRevision > 1 {
		maximumRouteRevision := routeRevision
		if maximumComboRevision > maximumRouteRevision {
			maximumRouteRevision = maximumComboRevision
		}
		nextRouteRevision = maximumRouteRevision + 1
	}

	if _, err := tx.ExecContext(ctx, `UPDATE llm_providers SET enabled = 1 WHERE id = ?`, state.ProviderID); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("enable selected Provider", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE llm_accounts SET enabled = 0
WHERE provider_id = ? AND id <> ?`, state.ProviderID, state.AccountID); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("disable prior same-Provider Accounts", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE llm_accounts SET enabled = 1
WHERE id = ? AND provider_id = ? AND enabled = 0`, state.AccountID, state.ProviderID); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("enable staged Account", err)
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageAccounts); err != nil {
		return OnboardingState{}, err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_combo_members`); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("delete old Combo members", err)
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageMemberDelete); err != nil {
		return OnboardingState{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_route_entries`); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("delete legacy route projection", err)
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageLegacyRouteDelete); err != nil {
		return OnboardingState{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM llm_combos`); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("delete old Combos", err)
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageComboDelete); err != nil {
		return OnboardingState{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_combos(id, name, type, active, revision)
	VALUES (?, ?, 'fallback', 1, ?)`, state.StagedComboID, comboName, nextRouteRevision); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("insert completed Combo", err)
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageComboInsert); err != nil {
		return OnboardingState{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_combo_members(
combo_id, position, provider_id, model_id, enabled
) VALUES (?, 0, ?, ?, 1)`, state.StagedComboID, state.ProviderID, state.ModelID); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("insert completed Combo member", err)
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageMemberInsert); err != nil {
		return OnboardingState{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO llm_route_entries(
position, provider_id, model_id, enabled
) VALUES (0, ?, ?, 1)`, state.ProviderID, state.ModelID); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("insert legacy route projection", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_meta SET value = ?
	WHERE key = 'llm_route_revision'`, strconv.FormatInt(nextRouteRevision, 10)); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("advance route revision", err)
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageRouteSync); err != nil {
		return OnboardingState{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_onboarding_provider_stages`); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("clear completed Provider stages", err)
	}

	result, err := tx.ExecContext(ctx, `UPDATE app_onboarding_state SET
completed_version = ?, phase = ?, staged_combo_id = '', persona_fingerprint = '',
test_nonce_hash = '', test_expires_at = '', restart_in_progress = 0,
provider_kind = ?, provider_id = ?, account_id = ?, model_id = ?,
revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ? AND provider_kind = ?
  AND provider_id = ? AND account_id = ? AND model_id = ? AND staged_combo_id = ?
  AND persona_fingerprint = ? AND test_nonce_hash = ? AND test_expires_at = ?`,
		CurrentOnboardingVersion,
		OnboardingPhaseCompleted,
		state.ProviderKind,
		state.ProviderID,
		state.AccountID,
		state.ModelID,
		ts(input.Now),
		input.Revision,
		OnboardingPhaseTest,
		persistedState.ProviderKind,
		persistedState.ProviderID,
		persistedState.AccountID,
		persistedState.ModelID,
		persistedState.StagedComboID,
		persistedState.PersonaFingerprint,
		persistedState.TestNonceHash,
		persistedState.TestExpiresAt,
	)
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("update completed singleton", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read completed singleton update", err)
	}
	if changed != 1 {
		return OnboardingState{}, ErrOnboardingConflict
	}
	updated, err := onboardingStateInTxContext(ctx, tx)
	if err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("read completed singleton", err)
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageStateUpdate); err != nil {
		return OnboardingState{}, err
	}
	if err := runOnboardingCompleteFailpoint(onboardingCompleteStageBeforeCommit); err != nil {
		return OnboardingState{}, err
	}
	if err := ctx.Err(); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("request canceled before commit", err)
	}
	if err := onboardingCompleteCommit(tx); err != nil {
		return OnboardingState{}, onboardingCompleteCommitError("commit transaction", err)
	}
	return updated, nil
}

func onboardingCompletionIsRequired(state OnboardingState) bool {
	switch {
	case state.RestartInProgress:
		return state.CompletedVersion == CurrentOnboardingVersion
	case state.CompletedVersion < CurrentOnboardingVersion:
		return state.CompletedVersion >= 0
	default:
		return false
	}
}

func validateOnboardingCompletionState(state OnboardingState) error {
	if !IsOnboardingProviderKind(state.ProviderKind) || state.ProviderID != state.ProviderKind ||
		!validOnboardingCompletionID(state.AccountID) ||
		!validOnboardingCompletionID(state.ModelID) {
		return ErrOnboardingConfigurationChanged
	}
	parsedComboID, err := uuid.Parse(state.StagedComboID)
	if err != nil || parsedComboID.String() != strings.ToLower(state.StagedComboID) {
		return ErrOnboardingConfigurationChanged
	}
	return nil
}

func validOnboardingCompletionID(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validOnboardingCompletionName(value string) bool {
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func decodeOnboardingSHA256Hex(value string) ([]byte, bool) {
	if !validOnboardingSHA256Hex(value) {
		return nil, false
	}
	decoded, err := hex.DecodeString(value)
	return decoded, err == nil && len(decoded) == 32
}

func mustDecodeOnboardingSHA256Hex(value string) []byte {
	decoded, _ := hex.DecodeString(value)
	return decoded
}

func runOnboardingCompleteFailpoint(stage string) error {
	if err := onboardingCompleteFailpoint(stage); err != nil {
		return onboardingCompleteCommitError(stage, err)
	}
	return nil
}

func onboardingCompleteCommitError(operation string, cause error) error {
	return fmt.Errorf("%w: %s: %v", ErrOnboardingCommitFailed, operation, cause)
}

func validOnboardingTestReceiptPolicy(policy OnboardingTestReceiptPolicy) bool {
	if !validOnboardingTestReceiptTime(policy.IssuedAt) ||
		!validOnboardingTestReceiptTime(policy.ExpiresAt) {
		return false
	}
	return policy.ExpiresAt.Equal(policy.IssuedAt.Add(onboardingTestReceiptTTL))
}

func validOnboardingTestReceiptTime(value time.Time) bool {
	if value.IsZero() || value.Year() < 1970 || value.Year() > 9999 {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func validOnboardingSHA256Hex(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func onboardingSetupStateIsClean(state OnboardingState) bool {
	return IsOnboardingProviderKind(state.ProviderKind) && state.ProviderID != "" &&
		state.AccountID != "" && state.ModelID == "" && state.StagedComboID == "" &&
		state.PersonaFingerprint == "" && state.TestNonceHash == "" && state.TestExpiresAt == ""
}

func onboardingSetupRetryMatches(
	state OnboardingState,
	expectedRevision int64,
	accountID string,
	modelID string,
) bool {
	return expectedRevision > 0 && state.Phase == OnboardingPhasePersona &&
		IsOnboardingProviderKind(state.ProviderKind) && state.ProviderID != "" &&
		accountID != "" && state.AccountID == accountID && modelID != "" &&
		state.ModelID == modelID && state.StagedComboID != "" &&
		state.PersonaFingerprint == "" && state.TestNonceHash == "" && state.TestExpiresAt == ""
}

func onboardingComboName(kind string) (string, error) {
	switch kind {
	case "codex":
		return "Mặc định · Codex", nil
	case "claude-code":
		return "Mặc định · Claude", nil
	default:
		return "", fmt.Errorf("%w: %q", ErrOnboardingProviderUnsupported, kind)
	}
}

func onboardingConnectStateIsClean(state OnboardingState) bool {
	return state.ProviderID == "" && state.AccountID == "" && state.ModelID == "" &&
		state.StagedComboID == "" && state.TestNonceHash == "" && state.TestExpiresAt == ""
}

// OnboardingStagingAccount returns only a disabled Account exactly owned by
// the requested onboarding revision. ConfigDir is metadata for the caller's
// cleanup; Store never removes it from the filesystem.
func (s *Store) OnboardingStagingAccount(expectedRevision int64) (OnboardingStagingAccount, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingStagingAccount{}, fmt.Errorf("begin onboarding staging inspection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingStagingAccount{}, err
	}
	if state.Revision != expectedRevision {
		return OnboardingStagingAccount{}, onboardingConflict(expectedRevision, state.Revision)
	}
	if state.AccountID == "" {
		if err := tx.Commit(); err != nil {
			return OnboardingStagingAccount{}, fmt.Errorf("commit empty onboarding staging inspection: %w", err)
		}
		return OnboardingStagingAccount{}, nil
	}

	staging, err := onboardingStagingAccountInTx(tx, state)
	if err != nil {
		return OnboardingStagingAccount{}, err
	}
	if err := tx.Commit(); err != nil {
		return OnboardingStagingAccount{}, fmt.Errorf("commit onboarding staging inspection: %w", err)
	}
	return staging, nil
}

// SelectOnboardingProvider starts the selected Provider's connect phase. Any
// prior disabled staging Account is deleted only after the caller confirms it
// cleaned the matching Account directory.
func (s *Store) SelectOnboardingProvider(
	expectedRevision int64,
	kind string,
	cleanedAccountID string,
) (OnboardingState, error) {
	if !IsOnboardingProviderKind(kind) {
		return OnboardingState{}, fmt.Errorf("%w: %q", ErrOnboardingProviderUnsupported, kind)
	}
	return s.transitionOnboarding(expectedRevision, cleanedAccountID, func(state *OnboardingState) (bool, error) {
		upgrade := state.Phase == OnboardingPhaseCompleted &&
			state.CompletedVersion < CurrentOnboardingVersion && !state.RestartInProgress
		switch state.Phase {
		case OnboardingPhaseProvider,
			OnboardingPhaseConnect,
			OnboardingPhaseSetup,
			OnboardingPhasePersona,
			OnboardingPhaseTest:
		case OnboardingPhaseCompleted:
			if !upgrade {
				return false, fmt.Errorf("select onboarding provider from %q: %w", state.Phase, ErrOnboardingInvalidPhase)
			}
		default:
			return false, fmt.Errorf("select onboarding provider from %q: %w", state.Phase, ErrOnboardingInvalidPhase)
		}
		state.Phase = OnboardingPhaseConnect
		state.ProviderKind = kind
		clearOnboardingStaging(state)
		// A completed older-version flow may point at an enabled live Account. It is historical
		// active configuration, not disposable onboarding staging, so starting the upgrade clears
		// only the singleton fields and never asks the caller to delete its Account or directory.
		return !upgrade, nil
	})
}

// RestartOnboarding begins a new current-version flow while retaining the fact
// that an earlier onboarding version completed successfully.
func (s *Store) RestartOnboarding(
	expectedRevision int64,
	cleanedAccountID string,
) (OnboardingState, error) {
	return s.transitionOnboarding(expectedRevision, cleanedAccountID, func(state *OnboardingState) (bool, error) {
		if state.Phase != OnboardingPhaseCompleted ||
			state.CompletedVersion != CurrentOnboardingVersion ||
			state.RestartInProgress {
			return false, fmt.Errorf("restart onboarding from phase %q: %w", state.Phase, ErrOnboardingInvalidPhase)
		}
		state.Phase = OnboardingPhaseProvider
		state.ProviderKind = ""
		state.RestartInProgress = true
		clearOnboardingStaging(state)
		// The completed Account and route are live service data, not disposable staging. Restart
		// begins a replacement flow while the old configuration keeps serving until Complete.
		return false, nil
	})
}

const onboardingStateSelect = `SELECT
completed_version, phase, provider_kind, provider_id, account_id, model_id,
staged_combo_id, persona_fingerprint, test_nonce_hash, test_expires_at,
restart_in_progress, revision, updated_at
FROM app_onboarding_state WHERE id = 1`

type onboardingRowScanner interface {
	Scan(dest ...any) error
}

func scanOnboardingState(row onboardingRowScanner) (OnboardingState, error) {
	var state OnboardingState
	var restartInProgress int64
	err := row.Scan(
		&state.CompletedVersion,
		&state.Phase,
		&state.ProviderKind,
		&state.ProviderID,
		&state.AccountID,
		&state.ModelID,
		&state.StagedComboID,
		&state.PersonaFingerprint,
		&state.TestNonceHash,
		&state.TestExpiresAt,
		&restartInProgress,
		&state.Revision,
		&state.UpdatedAt,
	)
	if err != nil {
		return OnboardingState{}, err
	}
	state.RestartInProgress = restartInProgress != 0
	return state, nil
}

func onboardingStateInTx(tx *sql.Tx) (OnboardingState, error) {
	state, err := scanOnboardingState(tx.QueryRow(onboardingStateSelect))
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingState{}, ErrOnboardingStateMissing
	}
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding state in transaction: %w", err)
	}
	return state, nil
}

func onboardingStateInTxContext(ctx context.Context, tx *sql.Tx) (OnboardingState, error) {
	state, err := scanOnboardingState(tx.QueryRowContext(ctx, onboardingStateSelect))
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingState{}, ErrOnboardingStateMissing
	}
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding state in transaction: %w", err)
	}
	return state, nil
}

func onboardingStagingAccountInTx(
	tx *sql.Tx,
	state OnboardingState,
) (OnboardingStagingAccount, error) {
	var staging OnboardingStagingAccount
	var enabled int64
	err := tx.QueryRow(`SELECT
a.id, a.provider_id, p.kind, a.config_dir, a.enabled
FROM llm_accounts a
JOIN llm_providers p ON p.id = a.provider_id
WHERE a.id = ?`, state.AccountID).Scan(
		&staging.AccountID,
		&staging.ProviderID,
		&staging.ProviderKind,
		&staging.ConfigDir,
		&enabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingStagingAccount{}, fmt.Errorf(
			"%w: onboarding Account %q does not exist with its Provider",
			ErrOnboardingInvalidStagingOwnership,
			state.AccountID,
		)
	}
	if err != nil {
		return OnboardingStagingAccount{}, fmt.Errorf("read onboarding staging Account %q: %w", state.AccountID, err)
	}
	if staging.AccountID != state.AccountID ||
		staging.ProviderID != state.ProviderID ||
		staging.ProviderKind != state.ProviderKind ||
		enabled != 0 {
		return OnboardingStagingAccount{}, fmt.Errorf(
			"%w: onboarding Account %q is not the exact disabled staging owner",
			ErrOnboardingInvalidStagingOwnership,
			state.AccountID,
		)
	}
	return staging, nil
}

func onboardingStagingAccountInTxContext(
	ctx context.Context,
	tx *sql.Tx,
	state OnboardingState,
) (OnboardingStagingAccount, error) {
	var staging OnboardingStagingAccount
	var enabled int64
	err := tx.QueryRowContext(ctx, `SELECT
a.id, a.provider_id, p.kind, a.config_dir, a.enabled
FROM llm_accounts a
JOIN llm_providers p ON p.id = a.provider_id
WHERE a.id = ?`, state.AccountID).Scan(
		&staging.AccountID,
		&staging.ProviderID,
		&staging.ProviderKind,
		&staging.ConfigDir,
		&enabled,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingStagingAccount{}, fmt.Errorf(
			"%w: onboarding Account %q does not exist with its Provider",
			ErrOnboardingInvalidStagingOwnership,
			state.AccountID,
		)
	}
	if err != nil {
		return OnboardingStagingAccount{}, fmt.Errorf("read onboarding staging Account %q: %w", state.AccountID, err)
	}
	if staging.AccountID != state.AccountID ||
		staging.ProviderID != state.ProviderID ||
		staging.ProviderKind != state.ProviderKind ||
		enabled != 0 {
		return OnboardingStagingAccount{}, fmt.Errorf(
			"%w: onboarding Account %q is not the exact disabled staging owner",
			ErrOnboardingInvalidStagingOwnership,
			state.AccountID,
		)
	}
	return staging, nil
}

func (s *Store) transitionOnboarding(
	expectedRevision int64,
	cleanedAccountID string,
	apply func(*OnboardingState) (cleanupOwnedStaging bool, err error),
) (OnboardingState, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("begin onboarding transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingState{}, err
	}
	if state.Revision != expectedRevision {
		return OnboardingState{}, onboardingConflict(expectedRevision, state.Revision)
	}
	ownedState := state
	cleanupOwnedStaging, err := apply(&state)
	if err != nil {
		return OnboardingState{}, err
	}
	if cleanupOwnedStaging {
		if err := deleteOnboardingStagingAccount(tx, ownedState, cleanedAccountID); err != nil {
			return OnboardingState{}, err
		}
	} else if cleanedAccountID != "" {
		return OnboardingState{}, fmt.Errorf(
			"%w: transition owns no disposable staging Account but cleanup named %q",
			ErrOnboardingInvalidStagingOwnership,
			cleanedAccountID,
		)
	}

	state.Revision++
	state.UpdatedAt = ts(time.Now())
	result, err := tx.Exec(`UPDATE app_onboarding_state SET
completed_version = ?, phase = ?, provider_kind = ?, provider_id = ?, account_id = ?,
model_id = ?, staged_combo_id = ?, persona_fingerprint = ?, test_nonce_hash = ?,
test_expires_at = ?, restart_in_progress = ?, revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ?`,
		state.CompletedVersion,
		state.Phase,
		state.ProviderKind,
		state.ProviderID,
		state.AccountID,
		state.ModelID,
		state.StagedComboID,
		state.PersonaFingerprint,
		state.TestNonceHash,
		state.TestExpiresAt,
		state.RestartInProgress,
		state.UpdatedAt,
		expectedRevision,
	)
	if err != nil {
		return OnboardingState{}, fmt.Errorf("update onboarding state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding update result: %w", err)
	}
	if changed != 1 {
		return OnboardingState{}, ErrOnboardingConflict
	}
	updated, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OnboardingState{}, fmt.Errorf("commit onboarding transition: %w", err)
	}
	return updated, nil
}

func deleteOnboardingStagingAccount(
	tx *sql.Tx,
	state OnboardingState,
	cleanedAccountID string,
) error {
	if state.AccountID == "" {
		if cleanedAccountID != "" {
			return fmt.Errorf(
				"%w: state owns no Account but cleanup named %q",
				ErrOnboardingInvalidStagingOwnership,
				cleanedAccountID,
			)
		}
		return nil
	}
	if cleanedAccountID != state.AccountID {
		return fmt.Errorf(
			"%w: cleanup Account %q does not match owned Account %q",
			ErrOnboardingInvalidStagingOwnership,
			cleanedAccountID,
			state.AccountID,
		)
	}
	staging, err := onboardingStagingAccountInTx(tx, state)
	if err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM llm_accounts
WHERE id = ? AND provider_id = ? AND enabled = 0
  AND EXISTS (
    SELECT 1 FROM llm_providers p
    WHERE p.id = llm_accounts.provider_id AND p.kind = ?
  )`, staging.AccountID, staging.ProviderID, staging.ProviderKind)
	if err != nil {
		return fmt.Errorf("delete onboarding staging Account %q: %w", staging.AccountID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read onboarding staging delete result: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf(
			"%w: onboarding Account %q changed before deletion",
			ErrOnboardingInvalidStagingOwnership,
			staging.AccountID,
		)
	}
	return nil
}

func clearOnboardingStaging(state *OnboardingState) {
	state.ProviderID = ""
	state.AccountID = ""
	state.ModelID = ""
	state.StagedComboID = ""
	state.PersonaFingerprint = ""
	state.TestNonceHash = ""
	state.TestExpiresAt = ""
}

func onboardingConflict(expectedRevision, currentRevision int64) error {
	return fmt.Errorf(
		"%w: expected revision %d, current revision %d",
		ErrOnboardingConflict,
		expectedRevision,
		currentRevision,
	)
}
