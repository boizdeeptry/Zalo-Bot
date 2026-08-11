package store

import (
	"database/sql"
	"errors"
	"fmt"
)

const CurrentOnboardingVersion int64 = 1

const (
	OnboardingPhaseProvider  = "provider"
	OnboardingPhaseConnect   = "connect"
	OnboardingPhaseSetup     = "setup"
	OnboardingPhasePersona   = "persona"
	OnboardingPhaseTest      = "test"
	OnboardingPhaseCompleted = "completed"
)

var ErrOnboardingStateMissing = errors.New("onboarding state is missing")
var ErrOnboardingConflict = errors.New("onboarding revision conflict")

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

// OnboardingState returns the required singleton state. A missing row is an
// explicit error so callers cannot mistake a damaged database for completion.
func (s *Store) OnboardingState() (OnboardingState, error) {
	var state OnboardingState
	var restartInProgress int64
	err := s.db.QueryRow(`SELECT
completed_version, phase, provider_kind, provider_id, account_id, model_id,
staged_combo_id, persona_fingerprint, test_nonce_hash, test_expires_at,
restart_in_progress, revision, updated_at
FROM app_onboarding_state WHERE id = 1`).Scan(
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
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingState{}, ErrOnboardingStateMissing
	}
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding state: %w", err)
	}
	state.RestartInProgress = restartInProgress != 0
	return state, nil
}
