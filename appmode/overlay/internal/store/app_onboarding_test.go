package store

import (
	"errors"
	"testing"
)

func openAppStoreForTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf(`Open(":memory:") = %v`, err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOnboardingStateReadsSingleton(t *testing.T) {
	st := openAppStoreForTest(t)
	if _, err := st.db.Exec(`UPDATE app_onboarding_state SET
completed_version = 1,
phase = 'test',
provider_kind = 'openai-compatible',
provider_id = 'provider-1',
account_id = 'account-1',
model_id = 'model-1',
staged_combo_id = 'combo-1',
persona_fingerprint = 'persona-hash',
test_nonce_hash = 'nonce-hash',
test_expires_at = '2026-08-11T08:00:00Z',
restart_in_progress = 1,
revision = 9,
updated_at = '2026-08-11T07:00:00Z'
WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	got, err := st.OnboardingState()
	if err != nil {
		t.Fatal(err)
	}
	want := OnboardingState{
		CompletedVersion:   1,
		Phase:              OnboardingPhaseTest,
		ProviderKind:       "openai-compatible",
		ProviderID:         "provider-1",
		AccountID:          "account-1",
		ModelID:            "model-1",
		StagedComboID:      "combo-1",
		PersonaFingerprint: "persona-hash",
		TestNonceHash:      "nonce-hash",
		TestExpiresAt:      "2026-08-11T08:00:00Z",
		RestartInProgress:  true,
		Revision:           9,
		UpdatedAt:          "2026-08-11T07:00:00Z",
	}
	if got != want {
		t.Fatalf("OnboardingState() = %+v; want %+v", got, want)
	}
}

func TestOnboardingStateMissingRowFailsClosed(t *testing.T) {
	st := openAppStoreForTest(t)
	if _, err := st.db.Exec(`DELETE FROM app_onboarding_state WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OnboardingState(); !errors.Is(err, ErrOnboardingStateMissing) {
		t.Fatalf("error = %v; want ErrOnboardingStateMissing", err)
	}
}
