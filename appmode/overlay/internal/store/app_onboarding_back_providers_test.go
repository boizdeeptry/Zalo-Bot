package store

import (
	"database/sql"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBackOnboardingToProvidersKeepsReadyStagesAndPersona(t *testing.T) {
	for _, phase := range []string{OnboardingPhasePersona, OnboardingPhaseTest} {
		t.Run(phase, func(t *testing.T) {
			st, source := seedOnboardingBackReadyForTest(t, phase, 41)
			beforeStages := slices.Clone(source.Stages)

			got, err := st.BackOnboardingToProviders(source.State.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if got.State.Phase != OnboardingPhaseProvider || got.State.Revision != 42 ||
				got.State.ProviderKind != "" || got.State.ProviderID != "" ||
				got.State.AccountID != "" || got.State.ModelID != "" ||
				got.State.StagedComboID != "" || got.State.TestNonceHash != "" ||
				got.State.TestExpiresAt != "" ||
				got.State.PersonaFingerprint != source.State.PersonaFingerprint {
				t.Fatalf("Back state = %+v; source=%+v", got.State, source.State)
			}
			if !slices.Equal(got.Stages, beforeStages) {
				t.Fatalf("Back stages = %+v; want %+v", got.Stages, beforeStages)
			}

			retry, err := st.BackOnboardingToProviders(source.State.Revision)
			if err != nil {
				t.Fatalf("exact lost-response retry = %v", err)
			}
			if retry.State != got.State || !slices.Equal(retry.Stages, got.Stages) {
				t.Fatalf("retry = %+v; want %+v", retry, got)
			}
			if receipt := onboardingProviderMutationReceiptForTest(t, st); !strings.Contains(receipt, `"operation":"back-to-providers"`) {
				t.Fatalf("Back receipt = %q", receipt)
			}
		})
	}
}

func TestBackOnboardingToProvidersRejectsInvalidSourceWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		phase  string
		mutate func(*testing.T, *Store, *OnboardingState)
	}{
		{name: "provider current revision", phase: OnboardingPhaseProvider},
		{name: "connect", phase: OnboardingPhaseConnect},
		{name: "empty stages", phase: OnboardingPhasePersona, mutate: func(t *testing.T, st *Store, _ *OnboardingState) {
			if _, err := st.db.Exec(`DELETE FROM app_onboarding_provider_stages`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "pending stage", phase: OnboardingPhasePersona, mutate: func(t *testing.T, st *Store, _ *OnboardingState) {
			if _, err := st.db.Exec(`UPDATE app_onboarding_provider_stages SET
status = 'pending', provider_id = '', account_id = '', model_id = '' WHERE kind = 'claude-code'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "noncanonical positions", phase: OnboardingPhasePersona, mutate: func(t *testing.T, st *Store, _ *OnboardingState) {
			if _, err := st.db.Exec(`UPDATE app_onboarding_provider_stages SET position = position + 2`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "active identity", phase: OnboardingPhasePersona, mutate: func(_ *testing.T, _ *Store, state *OnboardingState) {
			state.ProviderKind, state.ProviderID = "codex", "codex"
		}},
		{name: "invalid combo", phase: OnboardingPhasePersona, mutate: func(_ *testing.T, _ *Store, state *OnboardingState) {
			state.StagedComboID = "not-a-uuid"
		}},
		{name: "test missing fingerprint", phase: OnboardingPhaseTest, mutate: func(_ *testing.T, _ *Store, state *OnboardingState) {
			state.PersonaFingerprint = ""
		}},
		{name: "test invalid receipt hash", phase: OnboardingPhaseTest, mutate: func(_ *testing.T, _ *Store, state *OnboardingState) {
			state.TestNonceHash = "private-not-a-hash"
		}},
		{name: "test invalid receipt expiry", phase: OnboardingPhaseTest, mutate: func(_ *testing.T, _ *Store, state *OnboardingState) {
			state.TestExpiresAt = "tomorrow"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seedPhase := test.phase
			if seedPhase != OnboardingPhaseTest {
				seedPhase = OnboardingPhasePersona
			}
			st, source := seedOnboardingBackReadyForTest(t, seedPhase, 51)
			state := source.State
			state.Phase = test.phase
			if test.phase == OnboardingPhaseConnect {
				state.ProviderKind = "codex"
				state.StagedComboID = ""
			}
			if test.phase == OnboardingPhaseProvider {
				state.StagedComboID = ""
			}
			if test.mutate != nil {
				test.mutate(t, st, &state)
			}
			setOnboardingStateForTest(t, st, state)
			before := onboardingProviderMutationDigestForTest(t, st)

			if _, err := st.BackOnboardingToProviders(state.Revision); err == nil {
				t.Fatal("Back unexpectedly succeeded")
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatalf("rejected Back mutated database:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestBackOnboardingToProvidersLostResponseRequiresExactReceiptAndSuccessor(t *testing.T) {
	t.Run("tampered receipt", func(t *testing.T) {
		st, source := seedOnboardingBackReadyForTest(t, OnboardingPhaseTest, 61)
		if _, err := st.BackOnboardingToProviders(source.State.Revision); err != nil {
			t.Fatal(err)
		}
		receipt := onboardingProviderMutationReceiptForTest(t, st)
		setOnboardingProviderMutationReceiptForTest(t, st, mutateOnboardingProviderReceiptForTest(
			t, receipt, func(value *onboardingProviderMutationReceiptFixture) {
				value.Operation = onboardingProviderMutationBegin
			},
		))
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BackOnboardingToProviders(source.State.Revision); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("tampered replay error = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("tampered replay mutated database")
		}
	})

	t.Run("unrelated provider-shaped revision", func(t *testing.T) {
		st, source := seedOnboardingBackReadyForTest(t, OnboardingPhasePersona, 71)
		state := source.State
		state.Phase = OnboardingPhaseProvider
		state.StagedComboID = ""
		state.Revision++
		setOnboardingStateForTest(t, st, state)
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BackOnboardingToProviders(source.State.Revision); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("unrelated successor error = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("unrelated successor replay mutated database")
		}
	})
}

func TestBackOnboardingToProvidersRetainedFingerprintSurvivesSelectionLifecycleAndReplay(t *testing.T) {
	st, source := seedOnboardingBackReadyForTest(t, OnboardingPhaseTest, 101)
	if _, err := st.db.Exec(`DELETE FROM app_onboarding_provider_stages WHERE kind = 'claude-code'`); err != nil {
		t.Fatal(err)
	}
	back, err := st.BackOnboardingToProviders(source.State.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if back.State.PersonaFingerprint != source.State.PersonaFingerprint || len(back.Stages) != 1 {
		t.Fatalf("Back = %+v; source=%+v", back, source)
	}
	added, err := st.ReplaceOnboardingProviderSelection(back.State.Revision, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if added.State.Phase != OnboardingPhaseProvider || len(added.Stages) != 2 ||
		added.Stages[1].Status != onboardingProviderStagePending {
		t.Fatalf("selection add = %+v", added)
	}
	persona, err := st.ReplaceOnboardingProviderSelection(added.State.Revision, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if persona.State.Phase != OnboardingPhasePersona ||
		persona.State.PersonaFingerprint != source.State.PersonaFingerprint ||
		persona.State.StagedComboID == "" || len(persona.Stages) != 1 {
		t.Fatalf("selection Persona successor = %+v", persona)
	}
	replay, err := st.ReplaceOnboardingProviderSelection(added.State.Revision, []string{"codex"})
	if err != nil || replay.State != persona.State || !slices.Equal(replay.Stages, persona.Stages) {
		t.Fatalf("selection exact replay = %+v err=%v; want %+v", replay, err, persona)
	}

	state := persona.State
	state.PersonaFingerprint = "malformed-retained-fingerprint"
	setOnboardingStateForTest(t, st, state)
	before := onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.ReplaceOnboardingProviderSelection(added.State.Revision, []string{"codex"}); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("malformed retained fingerprint replay error = %v; want conflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatal("malformed retained fingerprint replay mutated database")
	}
}

func TestBackOnboardingToProvidersOverflowConcurrencyAndCommitRollback(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		st, source := seedOnboardingBackReadyForTest(t, OnboardingPhasePersona, math.MaxInt64)
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BackOnboardingToProviders(source.State.Revision); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("overflow error = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("overflow mutated database")
		}
	})

	t.Run("concurrent exact request", func(t *testing.T) {
		st, source := seedOnboardingBackReadyForTest(t, OnboardingPhasePersona, 81)
		start := make(chan struct{})
		results := make(chan OnboardingSnapshot, 2)
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				got, err := st.BackOnboardingToProviders(source.State.Revision)
				results <- got
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent Back = %v", err)
			}
		}
		for got := range results {
			if got.State.Phase != OnboardingPhaseProvider || got.State.Revision != 82 {
				t.Fatalf("concurrent Back snapshot = %+v", got)
			}
		}
	})

	t.Run("actual commit rollback", func(t *testing.T) {
		st, source := seedOnboardingBackReadyForTest(t, OnboardingPhaseTest, 91)
		oldCommit := onboardingProvidersCommit
		onboardingProvidersCommit = func(*sql.Tx) error { return errors.New("injected Back Commit error") }
		t.Cleanup(func() { onboardingProvidersCommit = oldCommit })
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BackOnboardingToProviders(source.State.Revision); err == nil {
			t.Fatal("Back actual Commit error unexpectedly succeeded")
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("Back actual Commit error escaped rollback")
		}
	})
}

func seedOnboardingBackReadyForTest(
	t *testing.T,
	phase string,
	revision int64,
) (*Store, OnboardingSnapshot) {
	t.Helper()
	st := openAppStoreForTest(t)
	stages := []OnboardingProviderStage{
		readyOnboardingProviderStageForTest("codex", 0),
		readyOnboardingProviderStageForTest("claude-code", 1),
	}
	for _, stage := range stages {
		if err := st.EnsureOnboardingProviderForKind(stage.Kind); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateLLMAccount(LLMAccount{
			ID: stage.AccountID, ProviderID: stage.ProviderID, Label: stage.Kind,
			ConfigDir: "D:/private/" + stage.AccountID,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddLLMModel(LLMModel{
			ProviderID: stage.ProviderID, ModelID: stage.ModelID, Name: stage.ModelID,
			Source: LLMModelDiscovered, Available: true,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, provider_id, account_id, model_id, updated_at
) VALUES (?, 'ready', ?, ?, ?, ?, ?)`, stage.Kind, stage.Position, stage.ProviderID,
			stage.AccountID, stage.ModelID, "2026-08-15T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	state := OnboardingState{
		Phase: phase, StagedComboID: uuid.NewString(), Revision: revision,
		UpdatedAt: "2026-08-15T00:00:00Z",
	}
	if phase == OnboardingPhaseTest {
		state.PersonaFingerprint = strings.Repeat("c", 64)
		state.TestNonceHash = strings.Repeat("a", 64)
		state.TestExpiresAt = time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	}
	setOnboardingStateForTest(t, st, state)
	return st, OnboardingSnapshot{State: state, Stages: stages}
}
