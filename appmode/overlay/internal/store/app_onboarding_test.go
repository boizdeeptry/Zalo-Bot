package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
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

func TestBindOnboardingAccountStagesDisabledAccountAndAdvancesOnce(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "codex", "codex")
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 7,
	})
	before := mustOnboardingStateForTest(t, st)
	addedAt := time.Date(2026, 8, 11, 7, 30, 0, 0, time.UTC)
	account := LLMAccount{
		ID: "account-new", ProviderID: "codex", Label: "New account",
		ConfigDir: "D:/onboarding/new", Enabled: false, AddedAt: &addedAt,
	}

	got, err := st.BindOnboardingAccount(before.Revision, "codex", account)
	if err != nil {
		t.Fatalf("BindOnboardingAccount() = %v", err)
	}
	if got.Phase != OnboardingPhaseSetup || got.ProviderID != "codex" ||
		got.AccountID != account.ID || got.Revision != before.Revision+1 {
		t.Fatalf("bound state = %+v", got)
	}
	if got.ProviderKind != before.ProviderKind || got.CompletedVersion != before.CompletedVersion ||
		got.RestartInProgress != before.RestartInProgress {
		t.Fatalf("bind changed flow markers: before=%+v got=%+v", before, got)
	}
	accounts, err := st.LLMAccounts("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != account.ID || accounts[0].Enabled ||
		accounts[0].ConfigDir != account.ConfigDir {
		t.Fatalf("staged accounts = %+v", accounts)
	}
}

func TestBindOnboardingAccountRejectsInvalidStateAndRollsBack(t *testing.T) {
	tests := []struct {
		name            string
		state           OnboardingState
		kind            string
		account         LLMAccount
		providerID      string
		providerKind    string
		providerEnabled bool
		duplicate       bool
		wantErr         error
	}{
		{
			name: "stale revision", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 4},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingConflict,
		},
		{
			name: "wrong phase", state: OnboardingState{Phase: OnboardingPhaseSetup, ProviderKind: "codex", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidPhase,
		},
		{
			name: "wrong selected kind", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "claude-code", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "enabled account", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 5},
			kind: "codex", account: func() LLMAccount { a := validOnboardingAccountForTest(); a.Enabled = true; return a }(),
			providerID: "codex", providerKind: "codex", providerEnabled: true, wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "account provider mismatch", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 5},
			kind: "codex", account: func() LLMAccount { a := validOnboardingAccountForTest(); a.ProviderID = "other"; return a }(),
			providerID: "codex", providerKind: "codex", providerEnabled: true, wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "missing provider", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "provider kind mismatch", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "claude-code", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "disabled provider", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: false,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "dirty provider id", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", ProviderID: "old", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "dirty account id", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", AccountID: "old", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "dirty model", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", ModelID: "old", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "dirty combo", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", StagedComboID: "old", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "dirty fingerprint", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", PersonaFingerprint: "old", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "dirty receipt", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", TestNonceHash: "old", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "dirty receipt expiry", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", TestExpiresAt: "2026-08-11T08:00:00Z", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			wantErr: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "duplicate account", state: OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 5},
			kind: "codex", account: validOnboardingAccountForTest(), providerID: "codex", providerKind: "codex", providerEnabled: true,
			duplicate: true, wantErr: ErrOnboardingInvalidStagingOwnership,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			if tt.providerID != "" {
				if _, err := st.db.Exec(`INSERT INTO llm_providers(id, name, kind, enabled)
VALUES (?, ?, ?, ?)`, tt.providerID, tt.providerID, tt.providerKind, boolInt(tt.providerEnabled)); err != nil {
					t.Fatal(err)
				}
			}
			if tt.duplicate {
				insertOnboardingAccountForTest(t, st, tt.account.ID, tt.account.ProviderID, false, "D:/existing")
			}
			setOnboardingStateForTest(t, st, tt.state)
			before := mustOnboardingStateForTest(t, st)
			expectedRevision := before.Revision
			if tt.name == "stale revision" {
				expectedRevision--
			}

			if _, err := st.BindOnboardingAccount(expectedRevision, tt.kind, tt.account); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v; want %v", err, tt.wantErr)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("failed bind changed state: before=%+v after=%+v", before, after)
			}
			accounts, err := st.LLMAccounts(tt.account.ProviderID)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if tt.duplicate {
				wantCount = 1
			}
			if len(accounts) != wantCount {
				t.Fatalf("accounts after failed bind = %+v; want count %d", accounts, wantCount)
			}
		})
	}
}

func TestBindOnboardingAccountRejectsInvalidInputWithoutMutation(t *testing.T) {
	tests := []struct {
		name    string
		kind    string
		account LLMAccount
		wantErr error
	}{
		{name: "unsupported kind", kind: "openai", account: validOnboardingAccountForTest(), wantErr: ErrOnboardingProviderUnsupported},
		{name: "empty account id", kind: "codex", account: func() LLMAccount { a := validOnboardingAccountForTest(); a.ID = ""; return a }(), wantErr: ErrOnboardingInvalidStagingOwnership},
		{name: "empty provider id", kind: "codex", account: func() LLMAccount { a := validOnboardingAccountForTest(); a.ProviderID = ""; return a }(), wantErr: ErrOnboardingInvalidStagingOwnership},
		{name: "empty config dir", kind: "codex", account: func() LLMAccount { a := validOnboardingAccountForTest(); a.ConfigDir = ""; return a }(), wantErr: ErrOnboardingInvalidStagingOwnership},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			before := mustOnboardingStateForTest(t, st)
			if _, err := st.BindOnboardingAccount(before.Revision, tt.kind, tt.account); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v; want %v", err, tt.wantErr)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("invalid input changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestBindOnboardingAccountCASFailureRollsBackInsertedAccount(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "codex", "codex")
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 9,
	})
	before := mustOnboardingStateForTest(t, st)
	if _, err := st.db.Exec(`CREATE TRIGGER onboarding_test_change_revision_during_bind
BEFORE INSERT ON llm_accounts WHEN NEW.id = 'account-new'
BEGIN
  UPDATE app_onboarding_state SET revision = revision + 1 WHERE id = 1;
END`); err != nil {
		t.Fatal(err)
	}

	if _, err := st.BindOnboardingAccount(before.Revision, "codex", validOnboardingAccountForTest()); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("error = %v; want ErrOnboardingConflict", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("CAS rollback changed state: before=%+v after=%+v", before, after)
	}
	if onboardingAccountExistsForTest(t, st, "account-new") {
		t.Fatal("Account insert survived failed onboarding CAS")
	}
}

func validOnboardingAccountForTest() LLMAccount {
	return LLMAccount{
		ID: "account-new", ProviderID: "codex", Label: "New account",
		ConfigDir: "D:/onboarding/new", Enabled: false,
	}
}

func TestOnboardingStagingAccountReturnsExactDisabledOwnership(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "provider-old", "claude-code")
	insertOnboardingAccountForTest(t, st, "old-account", "provider-old", false, "D:/onboarding/old")
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase:        OnboardingPhaseSetup,
		ProviderKind: "claude-code",
		ProviderID:   "provider-old",
		AccountID:    "old-account",
		Revision:     7,
		UpdatedAt:    "2026-08-11T07:00:00Z",
	})
	before := mustOnboardingStateForTest(t, st)

	got, err := st.OnboardingStagingAccount(before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	want := OnboardingStagingAccount{
		AccountID:    "old-account",
		ProviderID:   "provider-old",
		ProviderKind: "claude-code",
		ConfigDir:    "D:/onboarding/old",
	}
	if got != want {
		t.Fatalf("OnboardingStagingAccount() = %+v; want %+v", got, want)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("inspection mutated state: before=%+v after=%+v", before, after)
	}
	if !onboardingAccountExistsForTest(t, st, "old-account") {
		t.Fatal("inspection deleted the staging account")
	}
}

func TestOnboardingStagingAccountEmptyOwnershipReturnsZeroDescriptor(t *testing.T) {
	st := openAppStoreForTest(t)
	state := mustOnboardingStateForTest(t, st)
	got, err := st.OnboardingStagingAccount(state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if got != (OnboardingStagingAccount{}) {
		t.Fatalf("OnboardingStagingAccount() = %+v; want zero descriptor", got)
	}
}

func TestOnboardingStagingAccountRejectsStaleRevision(t *testing.T) {
	st := openAppStoreForTest(t)
	state := mustOnboardingStateForTest(t, st)
	if _, err := st.OnboardingStagingAccount(state.Revision - 1); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("stale error = %v; want ErrOnboardingConflict", err)
	}
}

func TestOnboardingStagingAccountRejectsInvalidOwnershipWithoutMutation(t *testing.T) {
	tests := []struct {
		name              string
		stateProviderID   string
		stateProviderKind string
		accountEnabled    bool
		insertAccount     bool
	}{
		{name: "enabled account", stateProviderID: "provider-old", stateProviderKind: "claude-code", accountEnabled: true, insertAccount: true},
		{name: "missing account", stateProviderID: "provider-old", stateProviderKind: "claude-code"},
		{name: "provider ID mismatch", stateProviderID: "provider-other", stateProviderKind: "claude-code", insertAccount: true},
		{name: "provider kind mismatch", stateProviderID: "provider-old", stateProviderKind: "codex", insertAccount: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			insertOnboardingProviderForTest(t, st, "provider-old", "claude-code")
			insertOnboardingProviderForTest(t, st, "provider-other", "claude-code")
			if tt.insertAccount {
				insertOnboardingAccountForTest(t, st, "old-account", "provider-old", tt.accountEnabled, "D:/onboarding/old")
			}
			setOnboardingStateForTest(t, st, OnboardingState{
				Phase:        OnboardingPhaseSetup,
				ProviderKind: tt.stateProviderKind,
				ProviderID:   tt.stateProviderID,
				AccountID:    "old-account",
				Revision:     8,
				UpdatedAt:    "2026-08-11T07:00:00Z",
			})
			before := mustOnboardingStateForTest(t, st)

			if _, err := st.OnboardingStagingAccount(before.Revision); !errors.Is(err, ErrOnboardingInvalidStagingOwnership) {
				t.Fatalf("error = %v; want ErrOnboardingInvalidStagingOwnership", err)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("invalid inspection mutated state: before=%+v after=%+v", before, after)
			}
			if tt.insertAccount && !onboardingAccountExistsForTest(t, st, "old-account") {
				t.Fatal("invalid inspection deleted the account")
			}
		})
	}
}

func TestSelectOnboardingProviderAcceptsOnlySupportedKinds(t *testing.T) {
	for _, kind := range []string{"codex", "claude-code"} {
		t.Run(kind, func(t *testing.T) {
			st := openAppStoreForTest(t)
			old := mustOnboardingStateForTest(t, st)
			got, err := st.SelectOnboardingProvider(old.Revision, kind, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.ProviderKind != kind || got.Phase != OnboardingPhaseConnect || got.Revision != old.Revision+1 {
				t.Fatalf("state = %+v", got)
			}
		})
	}

	for _, kind := range []string{"", "openai-compatible", "Claude-Code", " codex", "codex "} {
		t.Run(fmt.Sprintf("reject_%q", kind), func(t *testing.T) {
			st := openAppStoreForTest(t)
			before := mustOnboardingStateForTest(t, st)
			if _, err := st.SelectOnboardingProvider(before.Revision, kind, ""); !errors.Is(err, ErrOnboardingProviderUnsupported) {
				t.Fatalf("error = %v; want ErrOnboardingProviderUnsupported", err)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("unsupported provider mutated state: before=%+v after=%+v", before, after)
			}
		})
	}

	for _, tt := range []struct {
		kind string
		want bool
	}{
		{kind: "codex", want: true},
		{kind: "claude-code", want: true},
		{kind: "", want: false},
		{kind: "openai-compatible", want: false},
		{kind: " codex", want: false},
	} {
		if got := IsOnboardingProviderKind(tt.kind); got != tt.want {
			t.Errorf("IsOnboardingProviderKind(%q) = %t; want %t", tt.kind, got, tt.want)
		}
	}
}

func TestSelectOnboardingProviderFromRequiredPhasesClearsStagingOnce(t *testing.T) {
	phases := []string{
		OnboardingPhaseProvider,
		OnboardingPhaseConnect,
		OnboardingPhaseSetup,
		OnboardingPhasePersona,
		OnboardingPhaseTest,
	}
	for i, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			st := openAppStoreForTest(t)
			providerID := fmt.Sprintf("provider-old-%d", i)
			insertOnboardingProviderForTest(t, st, providerID, "claude-code")
			insertOnboardingAccountForTest(t, st, "old-account", providerID, false, "D:/onboarding/old")
			setOnboardingStateForTest(t, st, OnboardingState{
				CompletedVersion:   0,
				Phase:              phase,
				ProviderKind:       "claude-code",
				ProviderID:         providerID,
				AccountID:          "old-account",
				ModelID:            "model-old",
				StagedComboID:      "combo-old",
				PersonaFingerprint: "persona-old",
				TestNonceHash:      "nonce-old",
				TestExpiresAt:      "2026-08-11T08:00:00Z",
				Revision:           10,
				UpdatedAt:          "2026-08-11T07:00:00Z",
			})
			old := mustOnboardingStateForTest(t, st)

			cleanup, err := st.OnboardingStagingAccount(old.Revision)
			if err != nil {
				t.Fatal(err)
			}
			kind := "codex"
			if i%2 == 1 {
				kind = "claude-code"
			}
			got, err := st.SelectOnboardingProvider(old.Revision, kind, cleanup.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			if cleanup.AccountID != "old-account" || got.ProviderKind != kind ||
				got.Phase != OnboardingPhaseConnect || got.Revision != old.Revision+1 {
				t.Fatalf("state=%+v cleanup=%+v", got, cleanup)
			}
			if got.CompletedVersion != old.CompletedVersion || got.RestartInProgress != old.RestartInProgress {
				t.Fatalf("selection changed completion markers: old=%+v got=%+v", old, got)
			}
			assertOnboardingStagingClearedForTest(t, got, false)
			if got.UpdatedAt == old.UpdatedAt {
				t.Fatalf("updated_at was not refreshed: %q", got.UpdatedAt)
			}
			updated, err := time.Parse(time.RFC3339Nano, got.UpdatedAt)
			if err != nil || updated.Location() != time.UTC {
				t.Fatalf("updated_at = %q (%v); want UTC RFC3339", got.UpdatedAt, err)
			}
			if onboardingAccountExistsForTest(t, st, cleanup.AccountID) {
				t.Fatal("owned staging account survived selection")
			}

			if _, err := st.SelectOnboardingProvider(old.Revision, "claude-code", ""); !errors.Is(err, ErrOnboardingConflict) {
				t.Fatalf("stale error = %v", err)
			}
			if after := mustOnboardingStateForTest(t, st); after != got {
				t.Fatalf("stale selection mutated state: before=%+v after=%+v", got, after)
			}
		})
	}
}

func TestSelectOnboardingProviderRejectsCompletedPhase(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion: CurrentOnboardingVersion,
		Phase:            OnboardingPhaseCompleted,
		Revision:         3,
		UpdatedAt:        "2026-08-11T07:00:00Z",
	})
	before := mustOnboardingStateForTest(t, st)
	if _, err := st.SelectOnboardingProvider(before.Revision, "codex", ""); !errors.Is(err, ErrOnboardingInvalidPhase) {
		t.Fatalf("error = %v; want ErrOnboardingInvalidPhase", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("completed selection mutated state: before=%+v after=%+v", before, after)
	}
}

func TestOnboardingUpgradeSelectPreservesLiveAccountAndCompletion(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "provider-live", "codex")
	insertOnboardingAccountForTest(t, st, "live-account", "provider-live", true, "D:/live/codex")
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion:   CurrentOnboardingVersion - 1,
		Phase:              OnboardingPhaseCompleted,
		ProviderKind:       "codex",
		ProviderID:         "provider-live",
		AccountID:          "live-account",
		ModelID:            "live-model",
		StagedComboID:      "live-combo",
		PersonaFingerprint: "live-persona",
		TestNonceHash:      "live-receipt",
		TestExpiresAt:      "2026-08-11T08:00:00Z",
		Revision:           7,
	})
	before := mustOnboardingStateForTest(t, st)

	got, err := st.SelectOnboardingProvider(before.Revision, "claude-code", "")
	if err != nil {
		t.Fatalf("SelectOnboardingProvider(upgrade) = %v", err)
	}
	if got.CompletedVersion != before.CompletedVersion || got.Phase != OnboardingPhaseConnect ||
		got.ProviderKind != "claude-code" || got.Revision != before.Revision+1 || got.RestartInProgress {
		t.Fatalf("upgrade state = %+v", got)
	}
	assertOnboardingStagingClearedForTest(t, got, false)
	if !onboardingAccountExistsForTest(t, st, "live-account") {
		t.Fatal("upgrade selection deleted the previously completed live Account")
	}
	var enabled int
	var configDir string
	if err := st.db.QueryRow(`SELECT enabled, config_dir FROM llm_accounts WHERE id = 'live-account'`).Scan(&enabled, &configDir); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 || configDir != "D:/live/codex" {
		t.Fatalf("live Account changed: enabled=%d config_dir=%q", enabled, configDir)
	}
}

func TestSelectOnboardingProviderRequiresExactCleanedAccountID(t *testing.T) {
	for _, cleanedAccountID := range []string{"", "other-account", " old-account"} {
		t.Run(fmt.Sprintf("cleaned_%q", cleanedAccountID), func(t *testing.T) {
			st := openAppStoreForTest(t)
			insertOnboardingProviderForTest(t, st, "provider-old", "claude-code")
			insertOnboardingAccountForTest(t, st, "old-account", "provider-old", false, "D:/onboarding/old")
			setOnboardingStateForTest(t, st, OnboardingState{
				Phase:        OnboardingPhaseSetup,
				ProviderKind: "claude-code",
				ProviderID:   "provider-old",
				AccountID:    "old-account",
				Revision:     5,
				UpdatedAt:    "2026-08-11T07:00:00Z",
			})
			before := mustOnboardingStateForTest(t, st)

			if _, err := st.SelectOnboardingProvider(before.Revision, "codex", cleanedAccountID); !errors.Is(err, ErrOnboardingInvalidStagingOwnership) {
				t.Fatalf("error = %v; want ErrOnboardingInvalidStagingOwnership", err)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("invalid cleanup mutated state: before=%+v after=%+v", before, after)
			}
			if !onboardingAccountExistsForTest(t, st, "old-account") {
				t.Fatal("invalid cleanup deleted the staging account")
			}
		})
	}
}

func TestSelectOnboardingProviderRejectsCleanedAccountWhenStateOwnsNone(t *testing.T) {
	st := openAppStoreForTest(t)
	before := mustOnboardingStateForTest(t, st)
	if _, err := st.SelectOnboardingProvider(before.Revision, "codex", "unexpected-account"); !errors.Is(err, ErrOnboardingInvalidStagingOwnership) {
		t.Fatalf("error = %v; want ErrOnboardingInvalidStagingOwnership", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("invalid cleanup mutated state: before=%+v after=%+v", before, after)
	}
}

func TestSelectOnboardingProviderStaleRevisionPreservesOwnedStaging(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "provider-old", "claude-code")
	insertOnboardingAccountForTest(t, st, "old-account", "provider-old", false, "D:/onboarding/old")
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase:        OnboardingPhaseSetup,
		ProviderKind: "claude-code",
		ProviderID:   "provider-old",
		AccountID:    "old-account",
		Revision:     9,
		UpdatedAt:    "2026-08-11T07:00:00Z",
	})
	before := mustOnboardingStateForTest(t, st)

	if _, err := st.SelectOnboardingProvider(before.Revision-1, "codex", "old-account"); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("stale error = %v; want ErrOnboardingConflict", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("stale selection mutated state: before=%+v after=%+v", before, after)
	}
	if !onboardingAccountExistsForTest(t, st, "old-account") {
		t.Fatal("stale selection deleted the owned staging account")
	}
}

func TestSelectOnboardingProviderDeletesOnlyExactStagingAccount(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "provider-old", "claude-code")
	insertOnboardingProviderForTest(t, st, "provider-other", "codex")
	insertOnboardingAccountForTest(t, st, "old-account", "provider-old", false, "D:/onboarding/old")
	insertOnboardingAccountForTest(t, st, "same-provider-live", "provider-old", true, "D:/live/same")
	insertOnboardingAccountForTest(t, st, "same-provider-disabled", "provider-old", false, "D:/disabled/same")
	insertOnboardingAccountForTest(t, st, "other-provider-live", "provider-other", true, "D:/live/other")
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase:        OnboardingPhasePersona,
		ProviderKind: "claude-code",
		ProviderID:   "provider-old",
		AccountID:    "old-account",
		Revision:     6,
		UpdatedAt:    "2026-08-11T07:00:00Z",
	})
	old := mustOnboardingStateForTest(t, st)

	if _, err := st.SelectOnboardingProvider(old.Revision, "codex", "old-account"); err != nil {
		t.Fatal(err)
	}
	if onboardingAccountExistsForTest(t, st, "old-account") {
		t.Fatal("exact staging account was not deleted")
	}
	for _, accountID := range []string{"same-provider-live", "same-provider-disabled", "other-provider-live"} {
		if !onboardingAccountExistsForTest(t, st, accountID) {
			t.Errorf("unrelated account %q was deleted", accountID)
		}
	}
}

func TestSelectOnboardingProviderRejectsInvalidStagingWithoutDeleting(t *testing.T) {
	for _, tt := range []struct {
		name              string
		stateProviderID   string
		stateProviderKind string
		enabled           bool
		insertAccount     bool
	}{
		{name: "enabled account", stateProviderID: "provider-old", stateProviderKind: "claude-code", enabled: true, insertAccount: true},
		{name: "missing account", stateProviderID: "provider-old", stateProviderKind: "claude-code"},
		{name: "provider ID mismatch", stateProviderID: "provider-other", stateProviderKind: "claude-code", insertAccount: true},
		{name: "provider kind mismatch", stateProviderID: "provider-old", stateProviderKind: "codex", insertAccount: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			insertOnboardingProviderForTest(t, st, "provider-old", "claude-code")
			insertOnboardingProviderForTest(t, st, "provider-other", "claude-code")
			if tt.insertAccount {
				insertOnboardingAccountForTest(t, st, "old-account", "provider-old", tt.enabled, "D:/onboarding/old")
			}
			setOnboardingStateForTest(t, st, OnboardingState{
				Phase:        OnboardingPhaseTest,
				ProviderKind: tt.stateProviderKind,
				ProviderID:   tt.stateProviderID,
				AccountID:    "old-account",
				Revision:     12,
				UpdatedAt:    "2026-08-11T07:00:00Z",
			})
			before := mustOnboardingStateForTest(t, st)

			if _, err := st.SelectOnboardingProvider(before.Revision, "codex", "old-account"); !errors.Is(err, ErrOnboardingInvalidStagingOwnership) {
				t.Fatalf("error = %v; want ErrOnboardingInvalidStagingOwnership", err)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("invalid staging mutated state: before=%+v after=%+v", before, after)
			}
			if tt.insertAccount && !onboardingAccountExistsForTest(t, st, "old-account") {
				t.Fatal("invalid staging account was deleted")
			}
		})
	}
}

func TestSelectOnboardingProviderRollsBackAccountDeleteOnCASFailure(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "provider-old", "claude-code")
	insertOnboardingAccountForTest(t, st, "old-account", "provider-old", false, "D:/onboarding/old")
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase:        OnboardingPhaseSetup,
		ProviderKind: "claude-code",
		ProviderID:   "provider-old",
		AccountID:    "old-account",
		Revision:     14,
		UpdatedAt:    "2026-08-11T07:00:00Z",
	})
	before := mustOnboardingStateForTest(t, st)
	if _, err := st.db.Exec(`CREATE TRIGGER onboarding_test_force_cas_failure
BEFORE DELETE ON llm_accounts WHEN OLD.id = 'old-account'
BEGIN
  UPDATE app_onboarding_state SET revision = revision + 1 WHERE id = 1;
END`); err != nil {
		t.Fatal(err)
	}

	if _, err := st.SelectOnboardingProvider(before.Revision, "codex", "old-account"); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("error = %v; want ErrOnboardingConflict", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("failed CAS changed state: before=%+v after=%+v", before, after)
	}
	if !onboardingAccountExistsForTest(t, st, "old-account") {
		t.Fatal("account deletion survived CAS rollback")
	}
}

func TestRestartOnboardingRejectsBeforeCurrentCompletion(t *testing.T) {
	tests := []struct {
		name              string
		phase             string
		completedVersion  int64
		restartInProgress bool
	}{
		{name: "pre-completion phase", phase: OnboardingPhaseTest, completedVersion: 0},
		{name: "old completed version", phase: OnboardingPhaseCompleted, completedVersion: CurrentOnboardingVersion - 1},
		{name: "future completed version", phase: OnboardingPhaseCompleted, completedVersion: CurrentOnboardingVersion + 1},
		{name: "already restarting", phase: OnboardingPhaseCompleted, completedVersion: CurrentOnboardingVersion, restartInProgress: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			setOnboardingStateForTest(t, st, OnboardingState{
				CompletedVersion:  tt.completedVersion,
				Phase:             tt.phase,
				RestartInProgress: tt.restartInProgress,
				Revision:          4,
				UpdatedAt:         "2026-08-11T07:00:00Z",
			})
			before := mustOnboardingStateForTest(t, st)
			if _, err := st.RestartOnboarding(before.Revision, ""); !errors.Is(err, ErrOnboardingInvalidPhase) {
				t.Fatalf("error = %v; want ErrOnboardingInvalidPhase", err)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("invalid restart mutated state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestRestartOnboardingPreservesLiveAccountAndRouting(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "provider-live", "codex")
	if err := st.AddLLMModel(LLMModel{
		ProviderID: "provider-live", ModelID: "model-live", Name: "Live model",
		Source: LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	insertOnboardingAccountForTest(t, st, "live-account", "provider-live", true, "D:/live/account")
	combo, err := st.CreateLLMCombo("live combo", "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceLLMComboMembers(combo.ID, combo.Revision, "fallback", []LLMRouteEntry{{
		ProviderID: "provider-live", ModelID: "model-live", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveLLMCombo(combo.ID); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion:   CurrentOnboardingVersion,
		Phase:              OnboardingPhaseCompleted,
		ProviderKind:       "codex",
		ProviderID:         "provider-live",
		AccountID:          "live-account",
		ModelID:            "model-live",
		StagedComboID:      combo.ID,
		PersonaFingerprint: "persona-old",
		TestNonceHash:      "nonce-old",
		TestExpiresAt:      "2026-08-11T08:00:00Z",
		Revision:           21,
		UpdatedAt:          "2026-08-11T07:00:00Z",
	})
	old := mustOnboardingStateForTest(t, st)
	beforeRouting := onboardingRoutingDigestForTest(t, st)

	got, err := st.RestartOnboarding(old.Revision, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletedVersion != old.CompletedVersion || !got.RestartInProgress ||
		got.Phase != OnboardingPhaseProvider || got.Revision != old.Revision+1 {
		t.Fatalf("restart state = %+v; old = %+v", got, old)
	}
	assertOnboardingStagingClearedForTest(t, got, true)
	accounts, err := st.LLMAccounts("provider-live")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != "live-account" || !accounts[0].Enabled ||
		accounts[0].ConfigDir != "D:/live/account" {
		t.Fatalf("restart changed live Account: %+v", accounts)
	}
	if afterRouting := onboardingRoutingDigestForTest(t, st); afterRouting != beforeRouting {
		t.Fatalf("restart changed Combo/route data:\nbefore=%s\nafter=%s", beforeRouting, afterRouting)
	}
	if _, err := st.RestartOnboarding(old.Revision, ""); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("repeated stale restart error = %v; want ErrOnboardingConflict", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != got {
		t.Fatalf("repeated stale restart mutated state: before=%+v after=%+v", got, after)
	}
	if _, err := st.RestartOnboarding(got.Revision, ""); !errors.Is(err, ErrOnboardingInvalidPhase) {
		t.Fatalf("restart while in progress error = %v; want ErrOnboardingInvalidPhase", err)
	}
}

func TestRestartOnboardingRejectsCleanupOwnershipForLiveAccount(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "provider-live", "codex")
	insertOnboardingAccountForTest(t, st, "live-account", "provider-live", true, "D:/live/account")
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion: CurrentOnboardingVersion,
		Phase:            OnboardingPhaseCompleted,
		ProviderKind:     "codex",
		ProviderID:       "provider-live",
		AccountID:        "live-account",
		Revision:         15,
		UpdatedAt:        "2026-08-11T07:00:00Z",
	})
	before := mustOnboardingStateForTest(t, st)
	beforeAccounts, err := st.LLMAccounts("provider-live")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RestartOnboarding(before.Revision, "live-account"); !errors.Is(err, ErrOnboardingInvalidStagingOwnership) {
		t.Fatalf("RestartOnboarding(nonempty cleanup) error = %v; want ErrOnboardingInvalidStagingOwnership", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("invalid restart cleanup mutated state: before=%+v after=%+v", before, after)
	}
	afterAccounts, err := st.LLMAccounts("provider-live")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%#v", afterAccounts) != fmt.Sprintf("%#v", beforeAccounts) {
		t.Fatalf("invalid restart cleanup changed live Account: before=%+v after=%+v", beforeAccounts, afterAccounts)
	}
}

func TestSelectOnboardingProviderAfterRestartPreservesRestartMarkers(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion: CurrentOnboardingVersion,
		Phase:            OnboardingPhaseCompleted,
		Revision:         30,
		UpdatedAt:        "2026-08-11T07:00:00Z",
	})
	completed := mustOnboardingStateForTest(t, st)
	restarted, err := st.RestartOnboarding(completed.Revision, "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.SelectOnboardingProvider(restarted.Revision, "claude-code", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletedVersion != completed.CompletedVersion || !got.RestartInProgress ||
		got.Phase != OnboardingPhaseConnect || got.ProviderKind != "claude-code" ||
		got.Revision != restarted.Revision+1 {
		t.Fatalf("provider-after-restart state = %+v", got)
	}
}

func insertOnboardingProviderForTest(t *testing.T, st *Store, id, kind string) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO llm_providers(id, name, kind, enabled)
VALUES (?, ?, ?, 1)`, id, id, kind); err != nil {
		t.Fatal(err)
	}
}

func insertOnboardingAccountForTest(
	t *testing.T,
	st *Store,
	id string,
	providerID string,
	enabled bool,
	configDir string,
) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO llm_accounts(
id, provider_id, label, config_dir, enabled, added_at
) VALUES (?, ?, ?, ?, ?, '2026-08-11T07:00:00Z')`, id, providerID, id, configDir, enabled); err != nil {
		t.Fatal(err)
	}
}

func setOnboardingStateForTest(t *testing.T, st *Store, state OnboardingState) {
	t.Helper()
	if state.UpdatedAt == "" {
		state.UpdatedAt = "2026-08-11T07:00:00Z"
	}
	if _, err := st.db.Exec(`UPDATE app_onboarding_state SET
completed_version = ?, phase = ?, provider_kind = ?, provider_id = ?, account_id = ?,
model_id = ?, staged_combo_id = ?, persona_fingerprint = ?, test_nonce_hash = ?,
test_expires_at = ?, restart_in_progress = ?, revision = ?, updated_at = ?
WHERE id = 1`,
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
		state.Revision,
		state.UpdatedAt,
	); err != nil {
		t.Fatal(err)
	}
}

func mustOnboardingStateForTest(t *testing.T, st *Store) OnboardingState {
	t.Helper()
	state, err := st.OnboardingState()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func onboardingAccountExistsForTest(t *testing.T, st *Store, accountID string) bool {
	t.Helper()
	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM llm_accounts WHERE id = ?`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count == 1
}

func onboardingRoutingDigestForTest(t *testing.T, st *Store) string {
	t.Helper()
	combos, err := st.LLMCombos()
	if err != nil {
		t.Fatal(err)
	}
	route, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("combos=%#v route=%#v", combos, route)
}

func assertOnboardingStagingClearedForTest(t *testing.T, state OnboardingState, clearProviderKind bool) {
	t.Helper()
	if clearProviderKind && state.ProviderKind != "" {
		t.Errorf("provider kind = %q; want empty", state.ProviderKind)
	}
	if state.ProviderID != "" || state.AccountID != "" || state.ModelID != "" ||
		state.StagedComboID != "" || state.PersonaFingerprint != "" ||
		state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Errorf("staging fields not cleared: %+v", state)
	}
}
