package store

import (
	"database/sql"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBindOnboardingAccountMultiPreservesSelectedStagesAndAcceptsExactRetry(t *testing.T) {
	st := openAppStoreForTest(t)
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseProvider, PersonaFingerprint: "saved-persona", Revision: selected.State.Revision,
	})
	connected, err := st.BeginOnboardingProvider(selected.State.Revision, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureOnboardingProviderForKind("codex"); err != nil {
		t.Fatal(err)
	}
	account := onboardingMultiAccountForTest("codex", "codex-account")

	bound, err := st.BindOnboardingAccount(connected.State.Revision, "codex", account)
	if err != nil {
		t.Fatal(err)
	}
	if bound.State.Phase != OnboardingPhaseSetup || bound.State.Revision != connected.State.Revision+1 ||
		bound.State.ProviderKind != "codex" || bound.State.ProviderID != "codex" ||
		bound.State.AccountID != account.ID || bound.State.ModelID != "" ||
		bound.State.PersonaFingerprint != "saved-persona" {
		t.Fatalf("bound state = %+v", bound.State)
	}
	if !slices.Equal(bound.Stages, connected.Stages) {
		t.Fatalf("Bind changed selected rows: got %+v want %+v", bound.Stages, connected.Stages)
	}
	accounts, err := st.LLMAccounts("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != account.ID || accounts[0].Enabled ||
		accounts[0].ConfigDir != account.ConfigDir {
		t.Fatalf("staged accounts = %+v", accounts)
	}

	retry, err := st.BindOnboardingAccount(connected.State.Revision, "codex", account)
	if err != nil {
		t.Fatalf("exact lost-response retry = %v", err)
	}
	if retry.State != bound.State || !slices.Equal(retry.Stages, bound.Stages) {
		t.Fatalf("retry = %+v; want %+v", retry, bound)
	}
	before := onboardingProviderMutationDigestForTest(t, st)
	mismatch := account
	mismatch.Label = "different-label"
	if _, err := st.BindOnboardingAccount(connected.State.Revision, "codex", mismatch); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("mismatched retry error = %v; want conflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatal("mismatched Bind retry changed database")
	}
}

func TestBindOnboardingAccountMultiRejectsUnselectedReadyDirtyAndDriftWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		stages []OnboardingProviderStage
		state  OnboardingState
		mutate func(*testing.T, *Store)
		want   error
	}{
		{
			name: "unselected active kind", kind: "claude-code",
			stages: []OnboardingProviderStage{{Kind: "codex", Status: "pending", Position: 0}},
			state:  OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "claude-code", Revision: 9},
			want:   ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "ready target", kind: "codex",
			stages: []OnboardingProviderStage{readyOnboardingProviderStageForTest("codex", 0)},
			state:  OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 9},
			want:   ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "dirty combo", kind: "codex",
			stages: []OnboardingProviderStage{{Kind: "codex", Status: "pending", Position: 0}},
			state: OnboardingState{
				Phase: OnboardingPhaseConnect, ProviderKind: "codex", StagedComboID: uuid.NewString(), Revision: 9,
			},
			want: ErrOnboardingInvalidStagingOwnership,
		},
		{
			name: "wrong phase", kind: "codex",
			stages: []OnboardingProviderStage{{Kind: "codex", Status: "pending", Position: 0}},
			state:  OnboardingState{Phase: OnboardingPhaseProvider, Revision: 9},
			want:   ErrOnboardingInvalidPhase,
		},
		{
			name: "provider kind drift", kind: "codex",
			stages: []OnboardingProviderStage{{Kind: "codex", Status: "pending", Position: 0}},
			state:  OnboardingState{Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 9},
			mutate: func(t *testing.T, st *Store) {
				t.Helper()
				if _, err := st.db.Exec(`UPDATE llm_providers SET kind = 'claude-code' WHERE id = 'codex'`); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrOnboardingInvalidStagingOwnership,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := openV8FixtureWithStages(t, test.stages)
			setOnboardingStateForTest(t, st, test.state)
			if err := st.EnsureOnboardingProviderForKind(test.kind); err != nil {
				// The drift fixture intentionally mutates after Ensure.
				if test.mutate == nil {
					t.Fatal(err)
				}
			}
			if test.mutate != nil {
				test.mutate(t, st)
			}
			before := onboardingProviderMutationDigestForTest(t, st)
			account := onboardingMultiAccountForTest(test.kind, test.kind+"-new-account")
			if _, err := st.BindOnboardingAccount(test.state.Revision, test.kind, account); !errors.Is(err, test.want) {
				t.Fatalf("Bind error = %v; want %v", err, test.want)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatalf("rejected Bind changed database")
			}
		})
	}

	t.Run("stale revision", func(t *testing.T) {
		st, connected := prepareOnboardingMultiConnectForTest(t, "codex")
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BindOnboardingAccount(connected.State.Revision-1, "codex",
			onboardingMultiAccountForTest("codex", "stale-account")); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("stale Bind error = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("stale Bind changed database")
		}
	})
}

func TestStageOnboardingSetupMultiReturnsProviderUntilAllReady(t *testing.T) {
	st := openAppStoreForTest(t)
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseProvider, PersonaFingerprint: "saved-persona", Revision: selected.State.Revision,
	})

	firstBase, firstAccount := bindOnboardingMultiKindForTest(t, st, selected.State.Revision, "codex", "codex-account")
	addOnboardingMultiModelForTest(t, st, "codex", "gpt-5.6-terra", true)
	first, err := st.StageOnboardingSetup(firstBase, "codex", firstAccount.ID, "gpt-5.6-terra")
	if err != nil {
		t.Fatal(err)
	}
	if first.State.Phase != OnboardingPhaseProvider || first.State.Revision != firstBase+1 ||
		first.State.ProviderKind != "" || first.State.ProviderID != "" || first.State.AccountID != "" ||
		first.State.ModelID != "" || first.State.StagedComboID != "" ||
		first.State.PersonaFingerprint != "saved-persona" {
		t.Fatalf("first setup state = %+v", first.State)
	}
	assertProviderStageOrderForTest(t, first.Stages, []string{"codex", "claude-code"})
	assertOnboardingMultiStageForTest(t, first.Stages[0], "codex", "ready", firstAccount.ID, "gpt-5.6-terra")
	assertOnboardingMultiStageForTest(t, first.Stages[1], "claude-code", "pending", "", "")

	firstRetry, err := st.StageOnboardingSetup(firstBase, "codex", firstAccount.ID, "gpt-5.6-terra")
	if err != nil {
		t.Fatalf("first setup retry = %v", err)
	}
	if firstRetry.State != first.State || !slices.Equal(firstRetry.Stages, first.Stages) {
		t.Fatalf("first retry = %+v; want %+v", firstRetry, first)
	}

	lastBase, lastAccount := bindOnboardingMultiKindForTest(t, st, first.State.Revision, "claude-code", "claude-account")
	addOnboardingMultiModelForTest(t, st, "claude-code", "claude-opus", true)
	last, err := st.StageOnboardingSetup(lastBase, "claude-code", lastAccount.ID, "claude-opus")
	if err != nil {
		t.Fatal(err)
	}
	if last.State.Phase != OnboardingPhasePersona || last.State.Revision != lastBase+1 ||
		last.State.ProviderKind != "" || last.State.ProviderID != "" || last.State.AccountID != "" ||
		last.State.ModelID != "" || last.State.PersonaFingerprint != "" ||
		last.State.TestNonceHash != "" || last.State.TestExpiresAt != "" {
		t.Fatalf("last setup state = %+v", last.State)
	}
	if parsed, err := uuid.Parse(last.State.StagedComboID); err != nil || parsed.String() != last.State.StagedComboID {
		t.Fatalf("last setup combo = %q; want canonical UUID", last.State.StagedComboID)
	}
	assertProviderStageOrderForTest(t, last.Stages, []string{"codex", "claude-code"})
	assertOnboardingMultiStageForTest(t, last.Stages[0], "codex", "ready", firstAccount.ID, "gpt-5.6-terra")
	assertOnboardingMultiStageForTest(t, last.Stages[1], "claude-code", "ready", lastAccount.ID, "claude-opus")

	lastRetry, err := st.StageOnboardingSetup(lastBase, "claude-code", lastAccount.ID, "claude-opus")
	if err != nil {
		t.Fatalf("last setup retry = %v", err)
	}
	if lastRetry.State != last.State || !slices.Equal(lastRetry.Stages, last.Stages) {
		t.Fatalf("last retry = %+v; want %+v", lastRetry, last)
	}
}

func TestStageOnboardingSetupMultiRejectsMismatchDriftAndUnrelatedSuccessor(t *testing.T) {
	st := openAppStoreForTest(t)
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	base, account := bindOnboardingMultiKindForTest(t, st, selected.State.Revision, "codex", "codex-account")
	addOnboardingMultiModelForTest(t, st, "codex", "model-a", true)
	addOnboardingMultiModelForTest(t, st, "codex", "model-b", true)
	committed, err := st.StageOnboardingSetup(base, "codex", account.ID, "model-a")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name, kind, accountID, modelID string
	}{
		{name: "kind", kind: "claude-code", accountID: account.ID, modelID: "model-a"},
		{name: "account", kind: "codex", accountID: "other-account", modelID: "model-a"},
		{name: "model", kind: "codex", accountID: account.ID, modelID: "model-b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := onboardingProviderMutationDigestForTest(t, st)
			if _, err := st.StageOnboardingSetup(base, test.kind, test.accountID, test.modelID); !errors.Is(err, ErrOnboardingConflict) {
				t.Fatalf("mismatched %s retry = %v; want conflict", test.name, err)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatalf("mismatched %s retry changed database", test.name)
			}
		})
	}

	// A different legitimate transition advances exactly once and overwrites operation provenance.
	// The old setup request must not cross-accept that unrelated one-ahead state.
	if _, err := st.BeginOnboardingProvider(committed.State.Revision, "claude-code"); err != nil {
		t.Fatal(err)
	}
	before := onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.StageOnboardingSetup(committed.State.Revision, "codex", account.ID, "model-a"); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("setup after unrelated Begin = %v; want conflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatal("setup after unrelated transition changed database")
	}
}

func TestStageOnboardingSetupMultiRejectsAccountConfigModelAndStageDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, string)
		want   error
	}{
		{
			name: "config", want: ErrOnboardingConfigurationChanged,
			mutate: func(t *testing.T, st *Store, accountID string) {
				t.Helper()
				if _, err := st.db.Exec(`UPDATE llm_accounts SET config_dir = 'D:/drifted' WHERE id = ?`, accountID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "account enabled", want: ErrOnboardingInvalidStagingOwnership,
			mutate: func(t *testing.T, st *Store, accountID string) {
				t.Helper()
				if _, err := st.db.Exec(`UPDATE llm_accounts SET enabled = 1 WHERE id = ?`, accountID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "model unavailable", want: ErrOnboardingModelUnavailable,
			mutate: func(t *testing.T, st *Store, _ string) {
				t.Helper()
				if _, err := st.db.Exec(`UPDATE llm_models SET available = 0 WHERE provider_id = 'codex' AND model_id = 'model'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "target stage identity", want: ErrOnboardingConfigurationChanged,
			mutate: func(t *testing.T, st *Store, _ string) {
				t.Helper()
				if _, err := st.db.Exec(`UPDATE app_onboarding_provider_stages SET account_id = 'dirty' WHERE kind = 'codex'`); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
			if err != nil {
				t.Fatal(err)
			}
			base, account := bindOnboardingMultiKindForTest(t, st, selected.State.Revision, "codex", "codex-account")
			addOnboardingMultiModelForTest(t, st, "codex", "model", true)
			test.mutate(t, st, account.ID)
			before := onboardingProviderMutationDigestForTest(t, st)
			if _, err := st.StageOnboardingSetup(base, "codex", account.ID, "model"); !errors.Is(err, test.want) {
				t.Fatalf("Stage error = %v; want %v", err, test.want)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatal("rejected setup changed database")
			}
		})
	}
}

func TestStageOnboardingSetupMultiDetectsDuringTransactionConfigDriftAndRollsBack(t *testing.T) {
	st := openAppStoreForTest(t)
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	base, account := bindOnboardingMultiKindForTest(t, st, selected.State.Revision, "codex", "codex-account")
	addOnboardingMultiModelForTest(t, st, "codex", "model", true)
	before := onboardingProviderMutationDigestForTest(t, st)
	oldBeforeCAS := onboardingProviderSetupBeforeCAS
	onboardingProviderSetupBeforeCAS = func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE llm_accounts SET config_dir = 'D:/during-transaction-drift' WHERE id = ?`, account.ID)
		return err
	}
	t.Cleanup(func() { onboardingProviderSetupBeforeCAS = oldBeforeCAS })

	if _, err := st.StageOnboardingSetup(base, "codex", account.ID, "model"); !errors.Is(err, ErrOnboardingConfigurationChanged) {
		t.Fatalf("during-transaction drift error = %v; want configuration changed", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatal("during-transaction drift escaped rollback")
	}
}

func TestStageOnboardingSetupMultiConcurrentDifferentModelsHasOneWinner(t *testing.T) {
	st := openAppStoreForTest(t)
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	base, account := bindOnboardingMultiKindForTest(t, st, selected.State.Revision, "codex", "codex-account")
	addOnboardingMultiModelForTest(t, st, "codex", "model-a", true)
	addOnboardingMultiModelForTest(t, st, "codex", "model-b", true)

	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, modelID := range []string{"model-a", "model-b"} {
		modelID := modelID
		go func() {
			ready.Done()
			<-start
			_, err := st.StageOnboardingSetup(base, "codex", account.ID, modelID)
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	winners, conflicts := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			winners++
		case errors.Is(err, ErrOnboardingConflict):
			conflicts++
		default:
			t.Fatalf("concurrent setup error = %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent results winners/conflicts = %d/%d; want 1/1", winners, conflicts)
	}
}

func TestBindOnboardingAccountAndStageOnboardingSetupMultiCommitErrorsRollback(t *testing.T) {
	st, connected := prepareOnboardingMultiConnectForTest(t, "codex")
	account := onboardingMultiAccountForTest("codex", "codex-account")
	oldCommit := onboardingProvidersCommit
	onboardingProvidersCommit = func(*sql.Tx) error { return errors.New("injected actual Commit error") }
	t.Cleanup(func() { onboardingProvidersCommit = oldCommit })
	before := onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.BindOnboardingAccount(connected.State.Revision, "codex", account); err == nil {
		t.Fatal("Bind actual Commit error unexpectedly succeeded")
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatal("Bind actual Commit error escaped rollback")
	}

	onboardingProvidersCommit = oldCommit
	bound, err := st.BindOnboardingAccount(connected.State.Revision, "codex", account)
	if err != nil {
		t.Fatal(err)
	}
	addOnboardingMultiModelForTest(t, st, "codex", "model", true)
	onboardingProvidersCommit = func(*sql.Tx) error { return errors.New("injected actual Commit error") }
	before = onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.StageOnboardingSetup(bound.State.Revision, "codex", account.ID, "model"); err == nil {
		t.Fatal("Setup actual Commit error unexpectedly succeeded")
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatal("Setup actual Commit error escaped rollback")
	}
}

func TestBindOnboardingAccountAndStageOnboardingSetupMultiRevisionOverflowRejects(t *testing.T) {
	t.Run("bind", func(t *testing.T) {
		st := openV8FixtureWithStages(t, []OnboardingProviderStage{{Kind: "codex", Status: "pending", Position: 0}})
		setOnboardingStateForTest(t, st, OnboardingState{
			Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: maxOnboardingRouteRevision,
		})
		if err := st.EnsureOnboardingProviderForKind("codex"); err != nil {
			t.Fatal(err)
		}
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BindOnboardingAccount(maxOnboardingRouteRevision, "codex",
			onboardingMultiAccountForTest("codex", "overflow-account")); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("Bind overflow = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("Bind overflow changed database")
		}
	})

	t.Run("setup", func(t *testing.T) {
		st := openAppStoreForTest(t)
		selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
		if err != nil {
			t.Fatal(err)
		}
		_, account := bindOnboardingMultiKindForTest(t, st, selected.State.Revision, "codex", "overflow-account")
		setOnboardingStateForTest(t, st, OnboardingState{
			Phase: OnboardingPhaseSetup, ProviderKind: "codex", ProviderID: "codex",
			AccountID: account.ID, Revision: maxOnboardingRouteRevision,
		})
		addOnboardingMultiModelForTest(t, st, "codex", "model", true)
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.StageOnboardingSetup(maxOnboardingRouteRevision, "codex", account.ID, "model"); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("Setup overflow = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("Setup overflow changed database")
		}
	})
}

func onboardingMultiAccountForTest(kind, accountID string) LLMAccount {
	addedAt := time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC)
	return LLMAccount{
		ID: accountID, ProviderID: kind, Label: kind + " staging", Email: kind + "@example.test",
		ConfigDir: "D:/onboarding/" + accountID, Enabled: false, AddedAt: &addedAt,
	}
}

func prepareOnboardingMultiConnectForTest(t *testing.T, kind string) (*Store, OnboardingSnapshot) {
	t.Helper()
	st := openAppStoreForTest(t)
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := st.BeginOnboardingProvider(selected.State.Revision, kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureOnboardingProviderForKind(kind); err != nil {
		t.Fatal(err)
	}
	return st, connected
}

func bindOnboardingMultiKindForTest(
	t *testing.T,
	st *Store,
	revision int64,
	kind string,
	accountID string,
) (int64, LLMAccount) {
	t.Helper()
	connected, err := st.BeginOnboardingProvider(revision, kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureOnboardingProviderForKind(kind); err != nil {
		t.Fatal(err)
	}
	account := onboardingMultiAccountForTest(kind, accountID)
	bound, err := st.BindOnboardingAccount(connected.State.Revision, kind, account)
	if err != nil {
		t.Fatal(err)
	}
	return bound.State.Revision, account
}

func addOnboardingMultiModelForTest(t *testing.T, st *Store, kind, modelID string, available bool) {
	t.Helper()
	if err := st.AddLLMModel(LLMModel{
		ProviderID: kind, ModelID: modelID, Name: modelID,
		Source: LLMModelDiscovered, Available: available,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertOnboardingMultiStageForTest(
	t *testing.T,
	stage OnboardingProviderStage,
	kind string,
	status string,
	accountID string,
	modelID string,
) {
	t.Helper()
	providerID := ""
	if status == "ready" {
		providerID = kind
	}
	if stage.Kind != kind || stage.Status != status || stage.ProviderID != providerID ||
		stage.AccountID != accountID || stage.ModelID != modelID {
		t.Fatalf("stage = %+v; want kind=%q status=%q account=%q model=%q", stage, kind, status, accountID, modelID)
	}
}
