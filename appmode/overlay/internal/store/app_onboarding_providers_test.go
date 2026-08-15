package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestReplaceOnboardingProviderSelectionPersistsCanonicalTwoKinds(t *testing.T) {
	st := openAppStoreForTest(t)

	got, err := st.ReplaceOnboardingProviderSelection(1, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Revision != 2 || got.State.Phase != OnboardingPhaseProvider {
		t.Fatalf("state = %+v; want Provider revision 2", got.State)
	}
	assertProviderStageOrderForTest(t, got.Stages, []string{"codex", "claude-code"})
	for _, stage := range got.Stages {
		if stage.Status != "pending" || stage.ProviderID != "" || stage.AccountID != "" || stage.ModelID != "" {
			t.Fatalf("new selected stage is not clean pending: %+v", stage)
		}
	}
	assertProviderActiveStateCleanForTest(t, got.State, false)
}

func TestReplaceOnboardingProviderSelectionAcceptsZeroAndExactNoOp(t *testing.T) {
	st := openAppStoreForTest(t)
	before := onboardingProviderMutationDigestForTest(t, st)

	empty, err := st.ReplaceOnboardingProviderSelection(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.State.Revision != 1 || len(empty.Stages) != 0 {
		t.Fatalf("zero-selection no-op = %+v; want original empty Provider snapshot", empty)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("zero-selection no-op changed database:\nbefore=%s\nafter=%s", before, after)
	}

	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	before = onboardingProviderMutationDigestForTest(t, st)
	retry, err := st.ReplaceOnboardingProviderSelection(selected.State.Revision, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if retry.State.Revision != selected.State.Revision || !slices.Equal(retry.Stages, selected.Stages) {
		t.Fatalf("exact no-op = %+v; want %+v", retry, selected)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("exact no-op changed database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestReplaceOnboardingProviderSelectionRejectsInvalidInputWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		kinds []string
	}{
		{name: "duplicate", kinds: []string{"codex", "codex"}},
		{name: "blank", kinds: []string{""}},
		{name: "unsupported", kinds: []string{"future-runtime"}},
		{name: "over limit", kinds: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			before := onboardingProviderMutationDigestForTest(t, st)
			if _, err := st.ReplaceOnboardingProviderSelection(1, test.kinds); err == nil {
				t.Fatalf("ReplaceOnboardingProviderSelection(%q) unexpectedly succeeded", test.kinds)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatalf("invalid input changed database:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestReplaceOnboardingProviderSelectionAddsRemovesAndReindexesPending(t *testing.T) {
	st := openAppStoreForTest(t)
	first, err := st.ReplaceOnboardingProviderSelection(1, []string{"claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.ReplaceOnboardingProviderSelection(first.State.Revision, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	assertProviderStageOrderForTest(t, second.Stages, []string{"codex", "claude-code"})
	third, err := st.ReplaceOnboardingProviderSelection(second.State.Revision, []string{"claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	if third.State.Revision != 4 {
		t.Fatalf("revision = %d; want 4 after three actual mutations", third.State.Revision)
	}
	assertProviderStageOrderForTest(t, third.Stages, []string{"claude-code"})
}

func TestReplaceOnboardingProviderSelectionRepairsNoncanonicalPositions(t *testing.T) {
	st := openV8FixtureWithStages(t, []OnboardingProviderStage{
		{Kind: "claude-code", Status: "pending", Position: 0},
		{Kind: "codex", Status: "pending", Position: 1},
	})

	got, err := st.ReplaceOnboardingProviderSelection(1, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Revision != 2 {
		t.Fatalf("revision = %d; want 2 for position repair", got.State.Revision)
	}
	assertProviderStageOrderForTest(t, got.Stages, []string{"codex", "claude-code"})
}

func TestReplaceOnboardingProviderSelectionRejectsReadyRemovalWithoutMutation(t *testing.T) {
	st := openV8FixtureWithStages(t, []OnboardingProviderStage{
		readyOnboardingProviderStageForTest("codex", 0),
		{Kind: "claude-code", Status: "pending", Position: 1},
	})
	setOnboardingStateForTest(t, st, OnboardingState{Phase: OnboardingPhaseProvider, Revision: 7})
	before := onboardingProviderMutationDigestForTest(t, st)

	if _, err := st.ReplaceOnboardingProviderSelection(7, []string{"claude-code"}); !errors.Is(err, ErrOnboardingConfigurationChanged) {
		t.Fatalf("ready removal error = %v; want ErrOnboardingConfigurationChanged", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("ready removal changed database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestReplaceOnboardingProviderSelectionLastPendingRemovalAdvancesPersona(t *testing.T) {
	st := openV8FixtureWithStages(t, []OnboardingProviderStage{
		readyOnboardingProviderStageForTest("codex", 0),
		{Kind: "claude-code", Status: "pending", Position: 1},
	})
	setOnboardingStateForTest(t, st, OnboardingState{Phase: OnboardingPhaseProvider, Revision: 7})

	got, err := st.ReplaceOnboardingProviderSelection(7, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Phase != OnboardingPhasePersona || got.State.Revision != 8 {
		t.Fatalf("state = %+v; want Persona revision 8", got.State)
	}
	if _, err := uuid.Parse(got.State.StagedComboID); err != nil {
		t.Fatalf("staged Combo ID %q is not a UUID: %v", got.State.StagedComboID, err)
	}
	assertProviderActiveStateCleanForTest(t, got.State, true)
	assertProviderStageOrderForTest(t, got.Stages, []string{"codex"})
	if got.Stages[0].Status != "ready" {
		t.Fatalf("remaining stage = %+v; want ready", got.Stages[0])
	}
}

func TestReplaceOnboardingProviderSelectionPhaseTableRejectsWithoutMutation(t *testing.T) {
	for _, phase := range []string{
		OnboardingPhaseConnect,
		OnboardingPhaseSetup,
		OnboardingPhasePersona,
		OnboardingPhaseTest,
		OnboardingPhaseCompleted,
	} {
		t.Run(phase, func(t *testing.T) {
			st := openAppStoreForTest(t)
			setOnboardingStateForTest(t, st, OnboardingState{Phase: phase, Revision: 5})
			before := onboardingProviderMutationDigestForTest(t, st)
			if _, err := st.ReplaceOnboardingProviderSelection(5, []string{"codex"}); !errors.Is(err, ErrOnboardingInvalidPhase) {
				t.Fatalf("phase %q error = %v; want ErrOnboardingInvalidPhase", phase, err)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatalf("phase %q rejection changed database:\nbefore=%s\nafter=%s", phase, before, after)
			}
		})
	}
}

func TestReplaceOnboardingProviderSelectionConcurrentSameRevisionHasOneWinner(t *testing.T) {
	st := openAppStoreForTest(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, kinds := range [][]string{{"codex"}, {"claude-code"}} {
		kinds := kinds
		go func() {
			ready.Done()
			<-start
			_, err := st.ReplaceOnboardingProviderSelection(1, kinds)
			results <- err
		}()
	}
	ready.Wait()
	close(start)

	winners := 0
	conflicts := 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrOnboardingConflict):
			conflicts++
		default:
			t.Fatalf("concurrent mutation error = %v; want success or conflict", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent results: winners=%d conflicts=%d; want 1/1", winners, conflicts)
	}
	snapshot, err := st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.Revision != 2 || len(snapshot.Stages) != 1 {
		t.Fatalf("winning snapshot = %+v; want one selection at revision 2", snapshot)
	}
}

func TestReplaceOnboardingProviderSelectionAcceptsOnlyExactLostResponseSuccessor(t *testing.T) {
	st := openAppStoreForTest(t)
	committed, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	if receipt := onboardingProviderMutationReceiptForTest(t, st); receipt == "" {
		t.Fatal("selection mutation did not persist replay provenance")
	}
	retry, err := st.ReplaceOnboardingProviderSelection(1, []string{"claude-code", "codex"})
	if err != nil {
		t.Fatalf("exact lost response retry = %v", err)
	}
	if retry.State.Revision != committed.State.Revision || !slices.Equal(retry.Stages, committed.Stages) {
		t.Fatalf("lost response snapshot = %+v; want %+v", retry, committed)
	}
	before := onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex"}); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("mismatched lost response error = %v; want conflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("mismatched lost response changed database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestReplaceOnboardingProviderSelectionRejectsUnrelatedRestartSuccessor(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion: CurrentOnboardingVersion,
		Phase:            OnboardingPhaseCompleted,
		Revision:         21,
	})
	if _, err := st.RestartOnboarding(21, ""); err != nil {
		t.Fatal(err)
	}
	before := onboardingProviderMutationDigestForTest(t, st)

	if _, err := st.ReplaceOnboardingProviderSelection(21, nil); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("stale selection after unrelated Restart error = %v; want conflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("stale selection after Restart changed database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestReplaceOnboardingProviderSelectionRejectsUnrelatedNonemptyProviderSuccessor(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion: CurrentOnboardingVersion,
		Phase:            OnboardingPhaseCompleted,
		Revision:         31,
	})
	if _, err := st.RestartOnboarding(31, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, updated_at
) VALUES ('codex', 'pending', 0, '2026-08-15T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	before := onboardingProviderMutationDigestForTest(t, st)

	if _, err := st.ReplaceOnboardingProviderSelection(31, []string{"codex"}); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("stale nonempty selection after unrelated transition error = %v; want conflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("stale nonempty selection changed database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestOnboardingProviderMutationReceiptRejectsMalformedTamperedAndStaleValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, receipt string) string
	}{
		{
			name: "malformed",
			mutate: func(_ *testing.T, _ string) string {
				return "{"
			},
		},
		{
			name: "tampered payload",
			mutate: func(t *testing.T, receipt string) string {
				return mutateOnboardingProviderReceiptForTest(t, receipt, func(value *onboardingProviderMutationReceiptFixture) {
					value.PayloadSHA256 = strings.Repeat("0", 64)
				})
			},
		},
		{
			name: "stale result revision",
			mutate: func(t *testing.T, receipt string) string {
				return mutateOnboardingProviderReceiptForTest(t, receipt, func(value *onboardingProviderMutationReceiptFixture) {
					value.ResultRevision = 1
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			if _, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex"}); err != nil {
				t.Fatal(err)
			}
			receipt := onboardingProviderMutationReceiptForTest(t, st)
			if receipt == "" {
				t.Fatal("selection mutation did not persist replay provenance")
			}
			setOnboardingProviderMutationReceiptForTest(t, st, test.mutate(t, receipt))
			before := onboardingProviderMutationDigestForTest(t, st)

			if _, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex"}); !errors.Is(err, ErrOnboardingConflict) {
				t.Fatalf("retry with %s receipt error = %v; want conflict", test.name, err)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatalf("retry with %s receipt changed database", test.name)
			}
		})
	}
}

func TestOnboardingProviderMutationReceiptOperationCannotCrossAccept(t *testing.T) {
	t.Run("Replace requires Replace receipt", func(t *testing.T) {
		st := openAppStoreForTest(t)
		if _, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex"}); err != nil {
			t.Fatal(err)
		}
		receipt := onboardingProviderMutationReceiptForTest(t, st)
		tampered := strings.Replace(receipt, `"operation":"replace-selection"`, `"operation":"begin-provider"`, 1)
		if tampered == receipt {
			t.Fatalf("unexpected Replace receipt shape: %s", receipt)
		}
		setOnboardingProviderMutationReceiptForTest(t, st, tampered)
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex"}); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("Replace retry with Begin receipt error = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("cross-operation Replace retry changed database")
		}
	})

	t.Run("Begin requires Begin receipt", func(t *testing.T) {
		st := openAppStoreForTest(t)
		selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.BeginOnboardingProvider(selected.State.Revision, "codex"); err != nil {
			t.Fatal(err)
		}
		receipt := onboardingProviderMutationReceiptForTest(t, st)
		tampered := strings.Replace(receipt, `"operation":"begin-provider"`, `"operation":"replace-selection"`, 1)
		if tampered == receipt {
			t.Fatalf("unexpected Begin receipt shape: %s", receipt)
		}
		setOnboardingProviderMutationReceiptForTest(t, st, tampered)
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BeginOnboardingProvider(selected.State.Revision, "codex"); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("Begin retry with Replace receipt error = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("cross-operation Begin retry changed database")
		}
	})
}

func TestReplaceOnboardingProviderSelectionDoesNotTreatRevisionZeroAsLostResponse(t *testing.T) {
	st := openAppStoreForTest(t)
	before := onboardingProviderMutationDigestForTest(t, st)

	if _, err := st.ReplaceOnboardingProviderSelection(0, nil); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("revision zero error = %v; want conflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("revision zero changed database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestReplaceOnboardingProviderSelectionAcceptsPersonaLostResponseOnlyWhenExact(t *testing.T) {
	st := openV8FixtureWithStages(t, []OnboardingProviderStage{
		readyOnboardingProviderStageForTest("codex", 0),
		{Kind: "claude-code", Status: "pending", Position: 1},
	})
	setOnboardingStateForTest(t, st, OnboardingState{Phase: OnboardingPhaseProvider, Revision: 11})
	committed, err := st.ReplaceOnboardingProviderSelection(11, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := st.ReplaceOnboardingProviderSelection(11, []string{"codex"})
	if err != nil {
		t.Fatalf("Persona lost response retry = %v", err)
	}
	if retry.State != committed.State || !slices.Equal(retry.Stages, committed.Stages) {
		t.Fatalf("Persona lost response = %+v; want %+v", retry, committed)
	}
	if _, err := st.ReplaceOnboardingProviderSelection(11, []string{"codex", "claude-code"}); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("Persona mismatched retry error = %v; want conflict", err)
	}
	if _, err := st.ReplaceOnboardingProviderSelection(committed.State.Revision, []string{"codex"}); !errors.Is(err, ErrOnboardingInvalidPhase) {
		t.Fatalf("current Persona mutation error = %v; want invalid phase", err)
	}
}

func TestReplaceOnboardingProviderSelectionAllReadyExactNoOpStaysProvider(t *testing.T) {
	st := openV8FixtureWithStages(t, []OnboardingProviderStage{
		readyOnboardingProviderStageForTest("codex", 0),
	})
	fingerprint := strings.Repeat("c", 64)
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseProvider, PersonaFingerprint: fingerprint, Revision: 41,
	})
	before := onboardingProviderMutationDigestForTest(t, st)

	got, err := st.ReplaceOnboardingProviderSelection(41, []string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Phase != OnboardingPhaseProvider || got.State.Revision != 41 || got.State.StagedComboID != "" {
		t.Fatalf("all-ready exact no-op state = %+v; want unchanged Provider revision 41", got.State)
	}
	if got.State.PersonaFingerprint != fingerprint {
		t.Fatalf("all-ready no-op lost saved Persona fingerprint: %+v", got.State)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("all-ready exact no-op changed database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestReplaceOnboardingProviderSelectionRevisionBoundary(t *testing.T) {
	t.Run("mutation at MaxInt64 rejects", func(t *testing.T) {
		st := openAppStoreForTest(t)
		setOnboardingStateForTest(t, st, OnboardingState{
			Phase: OnboardingPhaseProvider, Revision: maxOnboardingRouteRevision,
		})
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.ReplaceOnboardingProviderSelection(
			maxOnboardingRouteRevision,
			[]string{"codex"},
		); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("MaxInt64 mutation error = %v; want conflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("MaxInt64 mutation changed database")
		}
	})

	t.Run("lost response from MaxInt64 minus one", func(t *testing.T) {
		st := openAppStoreForTest(t)
		baseRevision := maxOnboardingRouteRevision - 1
		setOnboardingStateForTest(t, st, OnboardingState{
			Phase: OnboardingPhaseProvider, Revision: baseRevision,
		})
		committed, err := st.ReplaceOnboardingProviderSelection(baseRevision, []string{"codex"})
		if err != nil {
			t.Fatal(err)
		}
		if committed.State.Revision != maxOnboardingRouteRevision {
			t.Fatalf("boundary result revision = %d; want MaxInt64", committed.State.Revision)
		}
		retry, err := st.ReplaceOnboardingProviderSelection(baseRevision, []string{"codex"})
		if err != nil {
			t.Fatalf("boundary lost-response retry = %v", err)
		}
		if retry.State != committed.State || !slices.Equal(retry.Stages, committed.Stages) {
			t.Fatalf("boundary retry = %+v; want %+v", retry, committed)
		}
	})
}

func TestBeginOnboardingProviderPreservesOtherSelectedRows(t *testing.T) {
	st := openAppStoreForTest(t)
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.BeginOnboardingProvider(selected.State.Revision, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Phase != OnboardingPhaseConnect || got.State.ProviderKind != "claude-code" ||
		got.State.Revision != selected.State.Revision+1 {
		t.Fatalf("begin state = %+v; want Claude Connect at next revision", got.State)
	}
	assertProviderActiveStateCleanForTest(t, got.State, false)
	if !slices.Equal(got.Stages, selected.Stages) {
		t.Fatalf("begin changed selected rows: got %+v want %+v", got.Stages, selected.Stages)
	}
}

func TestBeginOnboardingProviderAcceptsOnlyExactLostResponseSuccessor(t *testing.T) {
	st := openAppStoreForTest(t)
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := st.BeginOnboardingProvider(selected.State.Revision, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if receipt := onboardingProviderMutationReceiptForTest(t, st); receipt == "" {
		t.Fatal("begin mutation did not persist replay provenance")
	}
	retry, err := st.BeginOnboardingProvider(selected.State.Revision, "codex")
	if err != nil {
		t.Fatalf("exact begin lost response retry = %v", err)
	}
	if retry.State != committed.State || !slices.Equal(retry.Stages, committed.Stages) {
		t.Fatalf("begin lost response = %+v; want %+v", retry, committed)
	}
	before := onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.BeginOnboardingProvider(selected.State.Revision, "claude-code"); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("different-kind lost response error = %v; want conflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("different-kind lost response changed database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestBeginOnboardingProviderRejectsUnselectedReadyDirtyAndOtherPhases(t *testing.T) {
	t.Run("unselected", func(t *testing.T) {
		st := openAppStoreForTest(t)
		if _, err := st.BeginOnboardingProvider(1, "codex"); !errors.Is(err, ErrOnboardingInvalidStagingOwnership) {
			t.Fatalf("unselected begin error = %v; want invalid ownership", err)
		}
	})
	t.Run("ready", func(t *testing.T) {
		st := openV8FixtureWithStages(t, []OnboardingProviderStage{readyOnboardingProviderStageForTest("codex", 0)})
		if _, err := st.BeginOnboardingProvider(1, "codex"); !errors.Is(err, ErrOnboardingInvalidStagingOwnership) {
			t.Fatalf("ready begin error = %v; want invalid ownership", err)
		}
	})
	t.Run("dirty pending identity", func(t *testing.T) {
		st := openV8FixtureWithStages(t, []OnboardingProviderStage{{
			Kind: "codex", Status: "pending", Position: 0, AccountID: "unexpected",
		}})
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BeginOnboardingProvider(1, "codex"); !errors.Is(err, ErrOnboardingConfigurationChanged) {
			t.Fatalf("dirty pending begin error = %v; want configuration changed", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("dirty pending begin changed database")
		}
	})
	t.Run("dirty active singleton", func(t *testing.T) {
		st := openV8FixtureWithStages(t, []OnboardingProviderStage{{Kind: "codex", Status: "pending", Position: 0}})
		setOnboardingStateForTest(t, st, OnboardingState{
			Phase: OnboardingPhaseProvider, ProviderID: "unexpected", Revision: 4,
		})
		if _, err := st.BeginOnboardingProvider(4, "codex"); !errors.Is(err, ErrOnboardingConfigurationChanged) {
			t.Fatalf("dirty singleton begin error = %v; want configuration changed", err)
		}
	})
	for _, phase := range []string{
		OnboardingPhaseConnect, OnboardingPhaseSetup, OnboardingPhasePersona,
		OnboardingPhaseTest, OnboardingPhaseCompleted,
	} {
		t.Run(phase, func(t *testing.T) {
			st := openV8FixtureWithStages(t, []OnboardingProviderStage{{Kind: "codex", Status: "pending", Position: 0}})
			setOnboardingStateForTest(t, st, OnboardingState{Phase: phase, Revision: 6})
			before := onboardingProviderMutationDigestForTest(t, st)
			if _, err := st.BeginOnboardingProvider(6, "codex"); !errors.Is(err, ErrOnboardingInvalidPhase) {
				t.Fatalf("phase %q begin error = %v; want invalid phase", phase, err)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatalf("phase %q begin changed database", phase)
			}
		})
	}
}

func TestBeginOnboardingProviderRejectsInvalidKindWithoutMutation(t *testing.T) {
	st := openV8FixtureWithStages(t, []OnboardingProviderStage{{Kind: "codex", Status: "pending", Position: 0}})
	for _, kind := range []string{"", "Codex", "future-runtime"} {
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BeginOnboardingProvider(1, kind); err == nil {
			t.Fatalf("BeginOnboardingProvider(%q) unexpectedly succeeded", kind)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("invalid kind %q changed database", kind)
		}
	}
}

func TestOnboardingProviderMutationsActualCommitErrorRollsBack(t *testing.T) {
	// Package-level Commit seam is replaced only by this serial test and restored before return.
	st := openAppStoreForTest(t)
	oldCommit := onboardingProvidersCommit
	onboardingProvidersCommit = func(*sql.Tx) error { return errors.New("injected actual Commit error") }
	t.Cleanup(func() { onboardingProvidersCommit = oldCommit })
	before := onboardingProviderMutationDigestForTest(t, st)

	if _, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"}); err == nil {
		t.Fatal("ReplaceOnboardingProviderSelection() actual Commit error unexpectedly succeeded")
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("actual Commit error escaped rollback:\nbefore=%s\nafter=%s", before, after)
	}
	if _, err := st.OnboardingSnapshot(); err != nil {
		t.Fatalf("Store unusable after Commit rollback: %v", err)
	}

	onboardingProvidersCommit = oldCommit
	selected, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"})
	if err != nil {
		t.Fatalf("retry after Commit error = %v", err)
	}
	if selected.State.Revision != 2 {
		t.Fatalf("retry revision = %d; want 2", selected.State.Revision)
	}
}

func readyOnboardingProviderStageForTest(kind string, position int) OnboardingProviderStage {
	return OnboardingProviderStage{
		Kind: kind, Status: "ready", Position: position,
		ProviderID: kind, AccountID: kind + "-account", ModelID: kind + "-model",
	}
}

func assertProviderStageOrderForTest(t *testing.T, stages []OnboardingProviderStage, wantKinds []string) {
	t.Helper()
	if len(stages) != len(wantKinds) {
		t.Fatalf("stage count = %d; want %d: %+v", len(stages), len(wantKinds), stages)
	}
	for position, wantKind := range wantKinds {
		if stages[position].Kind != wantKind || stages[position].Position != position {
			t.Fatalf("stage %d = %+v; want kind %q at canonical position", position, stages[position], wantKind)
		}
	}
}

func assertProviderActiveStateCleanForTest(t *testing.T, state OnboardingState, allowCombo bool) {
	t.Helper()
	if state.ProviderID != "" || state.AccountID != "" || state.ModelID != "" ||
		state.PersonaFingerprint != "" || state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Fatalf("active onboarding state is dirty: %+v", state)
	}
	if state.Phase != OnboardingPhaseConnect && state.ProviderKind != "" {
		t.Fatalf("inactive Provider kind = %q; want empty", state.ProviderKind)
	}
	if !allowCombo && state.StagedComboID != "" {
		t.Fatalf("unexpected staged Combo ID %q", state.StagedComboID)
	}
}

func onboardingProviderMutationDigestForTest(t *testing.T, st *Store) string {
	t.Helper()
	base := completeOnboardingDigestForTest(t, st)
	rows, err := st.db.Query(`SELECT
kind, status, position, provider_id, account_id, model_id, updated_at
FROM app_onboarding_provider_stages ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	stageDigest := ""
	for rows.Next() {
		var kind, status, providerID, accountID, modelID, updatedAt string
		var position int
		if err := rows.Scan(&kind, &status, &position, &providerID, &accountID, &modelID, &updatedAt); err != nil {
			t.Fatal(err)
		}
		stageDigest += fmt.Sprintf("%q/%q/%d/%q/%q/%q/%q;", kind, status, position, providerID, accountID, modelID, updatedAt)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return base + " stages=" + stageDigest + " receipt=" + onboardingProviderMutationReceiptForTest(t, st)
}

const onboardingProviderMutationReceiptKeyForTest = "onboarding_provider_mutation_receipt_v1"

func onboardingProviderMutationReceiptForTest(t *testing.T, st *Store) string {
	t.Helper()
	var value string
	err := st.db.QueryRow(`SELECT value FROM app_meta WHERE key = ?`,
		onboardingProviderMutationReceiptKeyForTest,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func setOnboardingProviderMutationReceiptForTest(t *testing.T, st *Store, value string) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO app_meta(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		onboardingProviderMutationReceiptKeyForTest,
		value,
	); err != nil {
		t.Fatal(err)
	}
}

func mutateOnboardingProviderReceiptForTest(
	t *testing.T,
	receipt string,
	mutate func(*onboardingProviderMutationReceiptFixture),
) string {
	t.Helper()
	var value onboardingProviderMutationReceiptFixture
	if err := json.Unmarshal([]byte(receipt), &value); err != nil {
		t.Fatalf("decode receipt %q: %v", receipt, err)
	}
	mutate(&value)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

type onboardingProviderMutationReceiptFixture struct {
	Version        int    `json:"version"`
	Operation      string `json:"operation"`
	BaseRevision   int64  `json:"base_revision"`
	ResultRevision int64  `json:"result_revision"`
	PayloadSHA256  string `json:"payload_sha256"`
}
