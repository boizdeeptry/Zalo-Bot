package store

import (
	"fmt"
)

var onboardingSnapshotAfterStateRead = func() {}

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

// OnboardingSnapshot reads the singleton and its ordered Provider stages from
// one SQLite transaction so callers cannot observe parts of different commits.
func (s *Store) OnboardingSnapshot() (OnboardingSnapshot, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("begin onboarding snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := onboardingStateInTx(tx)
	if err != nil {
		return OnboardingSnapshot{}, err
	}
	onboardingSnapshotAfterStateRead()
	rows, err := tx.Query(`SELECT
kind, status, provider_id, account_id, model_id, position
FROM app_onboarding_provider_stages
ORDER BY position`)
	if err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("read onboarding Provider stages: %w", err)
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
			return OnboardingSnapshot{}, fmt.Errorf("scan onboarding Provider stage: %w", err)
		}
		stages = append(stages, stage)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return OnboardingSnapshot{}, fmt.Errorf("read onboarding Provider stages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("close onboarding Provider stages: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OnboardingSnapshot{}, fmt.Errorf("commit onboarding snapshot: %w", err)
	}
	return OnboardingSnapshot{State: state, Stages: stages}, nil
}
