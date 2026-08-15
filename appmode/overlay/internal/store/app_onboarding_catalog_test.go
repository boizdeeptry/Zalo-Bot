package store

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"agentdc/internal/providercatalog"

	"github.com/google/uuid"
)

const syntheticOnboardingProviderKind = "future-cli"

func syntheticOnboardingCatalog(t *testing.T) providercatalog.Catalog {
	t.Helper()
	catalog, err := providercatalog.New([]providercatalog.Option{
		{
			Kind:        "claude-code",
			DisplayName: "Claude Code",
			Description: "Kết nối tài khoản Claude Code trên máy này",
			Advertised:  true,
			RouteRank:   100,
		},
		{
			Kind:        syntheticOnboardingProviderKind,
			DisplayName: "Future CLI",
			Description: "Synthetic account runtime for Store contract tests",
			Advertised:  true,
			RouteRank:   50,
		},
		{
			Kind:        "codex",
			DisplayName: "Codex",
			Description: "Kết nối tài khoản ChatGPT/Codex trên máy này",
			Recommended: true,
			Advertised:  true,
			RouteRank:   10,
		},
	})
	if err != nil {
		t.Fatalf("create synthetic onboarding Catalog: %v", err)
	}
	return catalog
}

func reorderedSyntheticOnboardingCatalog(t *testing.T) providercatalog.Catalog {
	t.Helper()
	options := syntheticOnboardingCatalog(t).Options()
	for index := range options {
		switch options[index].Kind {
		case syntheticOnboardingProviderKind:
			options[index].RouteRank = 5
		case "claude-code":
			options[index].RouteRank = 50
		case "codex":
			options[index].RouteRank = 100
		default:
			t.Fatalf("unexpected synthetic Provider kind %q", options[index].Kind)
		}
	}
	catalog, err := providercatalog.New(options)
	if err != nil {
		t.Fatalf("create reordered synthetic onboarding Catalog: %v", err)
	}
	return catalog
}

func contractedSyntheticOnboardingCatalog(t *testing.T) providercatalog.Catalog {
	t.Helper()
	options := syntheticOnboardingCatalog(t).Options()
	contracted := make([]providercatalog.Option, 0, len(options)-1)
	for _, option := range options {
		if option.Kind != syntheticOnboardingProviderKind {
			contracted = append(contracted, option)
		}
	}
	catalog, err := providercatalog.New(contracted)
	if err != nil {
		t.Fatalf("create contracted synthetic onboarding Catalog: %v", err)
	}
	return catalog
}

func TestCatalogBoundOnboardingSelectionPersistsRankOrder(t *testing.T) {
	st := openAppStoreForTest(t)
	onboarding := st.Onboarding(syntheticOnboardingCatalog(t))

	got, err := onboarding.ReplaceOnboardingProviderSelection(1, []string{
		"claude-code",
		syntheticOnboardingProviderKind,
		"codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"codex", syntheticOnboardingProviderKind, "claude-code"}
	assertProviderStageOrderForTest(t, got.Stages, wantKinds)

	persisted := onboardingProviderStagesForTest(t, st.db)
	if len(persisted) != len(wantKinds) {
		t.Fatalf("persisted stage count = %d; want %d: %+v", len(persisted), len(wantKinds), persisted)
	}
	for position, wantKind := range wantKinds {
		if persisted[position].Kind != wantKind || persisted[position].Position != position {
			t.Fatalf("persisted stage %d = %+v; want kind %q at rank position", position, persisted[position], wantKind)
		}
	}

	replayed, err := onboarding.ReplaceOnboardingProviderSelection(1, []string{
		syntheticOnboardingProviderKind,
		"codex",
		"claude-code",
	})
	if err != nil {
		t.Fatalf("lost-response Replace replay: %v", err)
	}
	if replayed.State != got.State || !slices.Equal(replayed.Stages, got.Stages) {
		t.Fatalf("Replace replay = %+v; want committed %+v", replayed, got)
	}
	before := onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.ReplaceOnboardingProviderSelection(1, []string{"codex", "claude-code"}); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("wrong-Catalog Replace replay error = %v; want ErrOnboardingConflict", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("wrong-Catalog Replace replay mutated database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestCatalogBoundSnapshotRejectsRankMismatchAndSelectionRepairs(t *testing.T) {
	st := openAppStoreForTest(t)
	catalogA := st.Onboarding(syntheticOnboardingCatalog(t))
	catalogB := st.Onboarding(reorderedSyntheticOnboardingCatalog(t))
	selectedKinds := []string{"claude-code", syntheticOnboardingProviderKind, "codex"}

	selected, err := catalogA.ReplaceOnboardingProviderSelection(1, selectedKinds)
	if err != nil {
		t.Fatal(err)
	}
	assertProviderStageOrderForTest(
		t,
		selected.Stages,
		[]string{"codex", syntheticOnboardingProviderKind, "claude-code"},
	)
	beforeMismatch := onboardingProviderMutationDigestForTest(t, st)
	if _, err := catalogB.OnboardingSnapshot(); !errors.Is(err, ErrOnboardingConfigurationChanged) {
		t.Fatalf("Catalog B Snapshot error = %v; want ErrOnboardingConfigurationChanged", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != beforeMismatch {
		t.Fatalf("Catalog B mismatch inspection mutated database:\nbefore=%s\nafter=%s", beforeMismatch, after)
	}

	repaired, err := catalogB.ReplaceOnboardingProviderSelection(
		selected.State.Revision,
		selectedKinds,
	)
	if err != nil {
		t.Fatalf("Catalog B position repair: %v", err)
	}
	if repaired.State.Revision != selected.State.Revision+1 {
		t.Fatalf("Catalog B repair revision = %d; want %d", repaired.State.Revision, selected.State.Revision+1)
	}
	assertProviderStageOrderForTest(
		t,
		repaired.Stages,
		[]string{syntheticOnboardingProviderKind, "claude-code", "codex"},
	)
	inspected, err := catalogB.OnboardingSnapshot()
	if err != nil {
		t.Fatalf("Catalog B Snapshot after repair: %v", err)
	}
	if inspected.State != repaired.State || !slices.Equal(inspected.Stages, repaired.Stages) {
		t.Fatalf("Catalog B Snapshot after repair = %+v; want %+v", inspected, repaired)
	}

	beforeOldCatalog := onboardingProviderMutationDigestForTest(t, st)
	if _, err := catalogA.OnboardingSnapshot(); !errors.Is(err, ErrOnboardingConfigurationChanged) {
		t.Fatalf("Catalog A Snapshot after B repair error = %v; want ErrOnboardingConfigurationChanged", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != beforeOldCatalog {
		t.Fatalf("Catalog A mismatch inspection mutated database:\nbefore=%s\nafter=%s", beforeOldCatalog, after)
	}
}

func TestCatalogBoundStateAndStagingRejectUnknownSiblingStages(t *testing.T) {
	t.Run("State rejects a synthetic-only stage", func(t *testing.T) {
		st := openV8FixtureWithStages(t, []OnboardingProviderStage{
			{Kind: syntheticOnboardingProviderKind, Status: onboardingProviderStagePending, Position: 0},
		})
		before := onboardingProviderMutationDigestForTest(t, st)

		if _, err := st.OnboardingState(); !errors.Is(err, ErrOnboardingConfigurationChanged) {
			t.Fatalf("default OnboardingState() error = %v; want ErrOnboardingConfigurationChanged", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("State inspection mutated database:\nbefore=%s\nafter=%s", before, after)
		}
	})

	t.Run("staging Account rejects an unknown sibling", func(t *testing.T) {
		st := openV8FixtureWithStages(t, []OnboardingProviderStage{
			{Kind: "codex", Status: onboardingProviderStagePending, Position: 0},
			{Kind: syntheticOnboardingProviderKind, Status: onboardingProviderStagePending, Position: 1},
		})
		if err := st.EnsureOnboardingProviderForKind("codex"); err != nil {
			t.Fatal(err)
		}
		account := LLMAccount{
			ID:         "codex-staging-account",
			ProviderID: "codex",
			Label:      "Codex staging",
			ConfigDir:  "D:/private/codex-staging-account",
		}
		if err := st.CreateLLMAccount(account); err != nil {
			t.Fatal(err)
		}
		state := OnboardingState{
			Phase:        OnboardingPhaseSetup,
			ProviderKind: "codex",
			ProviderID:   "codex",
			AccountID:    account.ID,
			Revision:     19,
		}
		setOnboardingStateForTest(t, st, state)
		before := onboardingProviderMutationDigestForTest(t, st)

		if _, err := st.OnboardingStagingAccount(state.Revision); !errors.Is(err, ErrOnboardingConfigurationChanged) {
			t.Fatalf("default OnboardingStagingAccount() error = %v; want ErrOnboardingConfigurationChanged", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("staging inspection mutated database:\nbefore=%s\nafter=%s", before, after)
		}
	})

	t.Run("empty staging fast path rejects a synthetic stage", func(t *testing.T) {
		st := openV8FixtureWithStages(t, []OnboardingProviderStage{
			{Kind: syntheticOnboardingProviderKind, Status: onboardingProviderStagePending, Position: 0},
		})
		before := onboardingProviderMutationDigestForTest(t, st)

		if _, err := st.OnboardingStagingAccount(1); !errors.Is(err, ErrOnboardingConfigurationChanged) {
			t.Fatalf("empty default OnboardingStagingAccount() error = %v; want ErrOnboardingConfigurationChanged", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("empty staging inspection mutated database:\nbefore=%s\nafter=%s", before, after)
		}
	})
}

func TestCatalogBoundLegacySelectRejectsUnknownStagesAtomically(t *testing.T) {
	st := openV8FixtureWithStages(t, []OnboardingProviderStage{
		{Kind: "codex", Status: onboardingProviderStagePending, Position: 0},
		{Kind: syntheticOnboardingProviderKind, Status: onboardingProviderStagePending, Position: 1},
	})
	before := onboardingProviderMutationDigestForTest(t, st)

	if _, err := st.SelectOnboardingProvider(1, "codex", ""); !errors.Is(err, ErrOnboardingConfigurationChanged) {
		t.Fatalf("default SelectOnboardingProvider() error = %v; want ErrOnboardingConfigurationChanged", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("wrong-Catalog legacy Select mutated database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestCatalogBoundSelectionRepairsContractedCatalog(t *testing.T) {
	t.Run("unsupported pending stage is removed", func(t *testing.T) {
		st := openAppStoreForTest(t)
		catalogA := st.Onboarding(syntheticOnboardingCatalog(t))
		catalogB := st.Onboarding(contractedSyntheticOnboardingCatalog(t))
		selected, err := catalogA.ReplaceOnboardingProviderSelection(
			1,
			[]string{"claude-code", syntheticOnboardingProviderKind, "codex"},
		)
		if err != nil {
			t.Fatal(err)
		}

		repaired, err := catalogB.ReplaceOnboardingProviderSelection(
			selected.State.Revision,
			[]string{"claude-code", "codex"},
		)
		if err != nil {
			t.Fatalf("contracted Catalog pending-stage repair: %v", err)
		}
		if repaired.State.Revision != selected.State.Revision+1 {
			t.Fatalf("contracted repair revision = %d; want %d", repaired.State.Revision, selected.State.Revision+1)
		}
		assertProviderStageOrderForTest(t, repaired.Stages, []string{"codex", "claude-code"})
		inspected, err := catalogB.OnboardingSnapshot()
		if err != nil {
			t.Fatalf("contracted Catalog Snapshot after repair: %v", err)
		}
		if inspected.State != repaired.State || !slices.Equal(inspected.Stages, repaired.Stages) {
			t.Fatalf("contracted Snapshot = %+v; want repaired %+v", inspected, repaired)
		}
		var futureStages int
		if err := st.db.QueryRow(
			`SELECT COUNT(*) FROM app_onboarding_provider_stages WHERE kind = ?`,
			syntheticOnboardingProviderKind,
		).Scan(&futureStages); err != nil {
			t.Fatal(err)
		}
		if futureStages != 0 {
			t.Fatalf("contracted repair retained %d unsupported pending stages; want 0", futureStages)
		}
	})

	t.Run("unsupported ready stage cannot be removed", func(t *testing.T) {
		st := openAppStoreForTest(t)
		catalogA := st.Onboarding(syntheticOnboardingCatalog(t))
		catalogB := st.Onboarding(contractedSyntheticOnboardingCatalog(t))
		selected, err := catalogA.ReplaceOnboardingProviderSelection(
			1,
			[]string{"codex", syntheticOnboardingProviderKind, "claude-code"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE app_onboarding_provider_stages SET
status = 'ready', provider_id = ?, account_id = ?, model_id = ?
WHERE kind = ?`,
			syntheticOnboardingProviderKind,
			"future-ready-account",
			"future-model",
			syntheticOnboardingProviderKind,
		); err != nil {
			t.Fatal(err)
		}
		before := onboardingProviderMutationDigestForTest(t, st)

		if _, err := catalogB.ReplaceOnboardingProviderSelection(
			selected.State.Revision,
			[]string{"codex", "claude-code"},
		); !errors.Is(err, ErrOnboardingConfigurationChanged) {
			t.Fatalf("contracted ready-stage removal error = %v; want ErrOnboardingConfigurationChanged", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("contracted ready-stage rejection mutated database:\nbefore=%s\nafter=%s", before, after)
		}
	})
}

func TestCatalogBoundSelectionRejectsMalformedPersistedKinds(t *testing.T) {
	tests := []struct {
		name string
		kind string
	}{
		{name: "empty", kind: ""},
		{name: "over 64 bytes", kind: strings.Repeat("a", 65)},
		{name: "uppercase", kind: "Future-CLI"},
		{name: "path-like", kind: "future/cli"},
		{name: "Unicode confusable", kind: "future-cl\u0456"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := openV8FixtureWithStages(t, []OnboardingProviderStage{
				{
					Kind: test.kind, Status: onboardingProviderStagePending, Position: 0,
				},
			})
			before := onboardingProviderMutationDigestForTest(t, st)

			if _, err := st.ReplaceOnboardingProviderSelection(1, nil); !errors.Is(err, ErrOnboardingConfigurationChanged) {
				t.Fatalf("ReplaceOnboardingProviderSelection() error = %v; want ErrOnboardingConfigurationChanged", err)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatalf("malformed stage rejection mutated database:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestDefaultOnboardingStoreRejectsSyntheticKind(t *testing.T) {
	st := openAppStoreForTest(t)
	onboarding := st.Onboarding(syntheticOnboardingCatalog(t))
	selected, err := onboarding.ReplaceOnboardingProviderSelection(1, []string{
		syntheticOnboardingProviderKind,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := onboardingProviderMutationDigestForTest(t, st)

	if _, err := st.OnboardingSnapshot(); !errors.Is(err, ErrOnboardingConfigurationChanged) {
		t.Fatalf("default OnboardingSnapshot() error = %v; want ErrOnboardingConfigurationChanged", err)
	}
	if _, err := st.BeginOnboardingProvider(selected.State.Revision, syntheticOnboardingProviderKind); err == nil {
		t.Fatal("default BeginOnboardingProvider accepted synthetic kind")
	}
	if err := st.EnsureOnboardingProviderForKind(syntheticOnboardingProviderKind); err == nil {
		t.Fatal("default EnsureOnboardingProviderForKind accepted synthetic kind")
	}
	if _, err := st.SelectOnboardingProvider(
		selected.State.Revision,
		syntheticOnboardingProviderKind,
		"",
	); err == nil {
		t.Fatal("default SelectOnboardingProvider accepted synthetic kind")
	}
	var providerCount int
	if err := st.db.QueryRow(
		`SELECT COUNT(*) FROM llm_providers WHERE id = ?`,
		syntheticOnboardingProviderKind,
	).Scan(&providerCount); err != nil {
		t.Fatal(err)
	}
	if providerCount != 0 {
		t.Fatalf("default rejection created %d synthetic Provider rows; want 0", providerCount)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("default rejection mutated database:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestCatalogBoundSnapshotRejectsUnknownPersistedKind(t *testing.T) {
	t.Run("kind outside exact Catalog", func(t *testing.T) {
		st := openV8FixtureWithStages(t, []OnboardingProviderStage{
			{Kind: "rogue-runtime", Status: onboardingProviderStagePending, Position: 0},
		})
		before := onboardingProviderMutationDigestForTest(t, st)

		if _, err := st.Onboarding(syntheticOnboardingCatalog(t)).OnboardingSnapshot(); !errors.Is(err, ErrOnboardingConfigurationChanged) {
			t.Fatalf("Catalog-bound OnboardingSnapshot() error = %v; want ErrOnboardingConfigurationChanged", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("unknown-kind inspection mutated database:\nbefore=%s\nafter=%s", before, after)
		}
	})

	t.Run("zero Catalog cannot inspect or mutate", func(t *testing.T) {
		fresh := openAppStoreForTest(t)
		zeroFresh := fresh.Onboarding(providercatalog.Catalog{})
		freshBefore := onboardingProviderMutationDigestForTest(t, fresh)
		if _, err := zeroFresh.OnboardingState(); err == nil {
			t.Fatal("zero Catalog inspected a fresh onboarding State")
		}
		if _, err := zeroFresh.OnboardingSnapshot(); err == nil {
			t.Fatal("zero Catalog inspected a fresh onboarding Snapshot")
		}
		if err := zeroFresh.EnsureOnboardingProviderForKind(syntheticOnboardingProviderKind); err == nil {
			t.Fatal("zero Catalog ensured a synthetic Provider")
		}
		if _, err := zeroFresh.SelectOnboardingProvider(
			1,
			syntheticOnboardingProviderKind,
			"",
		); err == nil {
			t.Fatal("zero Catalog selected a synthetic Provider through the legacy API")
		}
		var providerCount int
		if err := fresh.db.QueryRow(
			`SELECT COUNT(*) FROM llm_providers WHERE id = ?`,
			syntheticOnboardingProviderKind,
		).Scan(&providerCount); err != nil {
			t.Fatal(err)
		}
		if providerCount != 0 {
			t.Fatalf("zero Catalog created %d synthetic Provider rows; want 0", providerCount)
		}
		if after := onboardingProviderMutationDigestForTest(t, fresh); after != freshBefore {
			t.Fatalf("zero Catalog mutated fresh database:\nbefore=%s\nafter=%s", freshBefore, after)
		}

		st := openAppStoreForTest(t)
		selected, err := st.Onboarding(syntheticOnboardingCatalog(t)).
			ReplaceOnboardingProviderSelection(1, []string{syntheticOnboardingProviderKind})
		if err != nil {
			t.Fatal(err)
		}
		before := onboardingProviderMutationDigestForTest(t, st)
		zero := st.Onboarding(providercatalog.Catalog{})

		if _, err := zero.OnboardingSnapshot(); err == nil {
			t.Fatal("zero Catalog inspected a persisted Provider stage")
		}
		if _, err := zero.ReplaceOnboardingProviderSelection(
			selected.State.Revision,
			[]string{syntheticOnboardingProviderKind},
		); err == nil {
			t.Fatal("zero Catalog replaced a Provider selection")
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("zero Catalog mutated database:\nbefore=%s\nafter=%s", before, after)
		}
	})
}

func TestCatalogBoundBeginBackAndLostResponse(t *testing.T) {
	t.Run("begin replay", func(t *testing.T) {
		st := openAppStoreForTest(t)
		onboarding := st.Onboarding(syntheticOnboardingCatalog(t))
		selected, err := onboarding.ReplaceOnboardingProviderSelection(
			1,
			[]string{syntheticOnboardingProviderKind},
		)
		if err != nil {
			t.Fatal(err)
		}
		begun, err := onboarding.BeginOnboardingProvider(
			selected.State.Revision,
			syntheticOnboardingProviderKind,
		)
		if err != nil {
			t.Fatal(err)
		}
		if begun.State.Phase != OnboardingPhaseConnect ||
			begun.State.ProviderKind != syntheticOnboardingProviderKind {
			t.Fatalf("begun snapshot = %+v; want synthetic Connect", begun)
		}

		replayed, err := onboarding.BeginOnboardingProvider(
			selected.State.Revision,
			syntheticOnboardingProviderKind,
		)
		if err != nil {
			t.Fatalf("lost-response Begin replay: %v", err)
		}
		if replayed.State != begun.State || !slices.Equal(replayed.Stages, begun.Stages) {
			t.Fatalf("Begin replay = %+v; want committed %+v", replayed, begun)
		}
	})

	t.Run("Back replay", func(t *testing.T) {
		st := openAppStoreForTest(t)
		onboarding := st.Onboarding(syntheticOnboardingCatalog(t))
		seedCatalogBoundReadyProviderForTest(t, st, onboarding, syntheticOnboardingProviderKind)
		source := OnboardingState{
			Phase:         OnboardingPhasePersona,
			StagedComboID: uuid.NewString(),
			Revision:      41,
		}
		setOnboardingStateForTest(t, st, source)

		back, err := onboarding.BackOnboardingToProviders(source.Revision)
		if err != nil {
			t.Fatal(err)
		}
		if back.State.Phase != OnboardingPhaseProvider || back.State.Revision != source.Revision+1 {
			t.Fatalf("Back snapshot = %+v; want Provider revision %d", back, source.Revision+1)
		}
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BackOnboardingToProviders(source.Revision); !errors.Is(err, ErrOnboardingConflict) {
			t.Fatalf("default Back replay error = %v; want ErrOnboardingConflict", err)
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("default Back replay mutated database:\nbefore=%s\nafter=%s", before, after)
		}
		replayed, err := onboarding.BackOnboardingToProviders(source.Revision)
		if err != nil {
			t.Fatalf("lost-response Back replay: %v", err)
		}
		if replayed.State != back.State || !slices.Equal(replayed.Stages, back.Stages) {
			t.Fatalf("Back replay = %+v; want committed %+v", replayed, back)
		}
	})
}

func TestCatalogBoundStagingInspectionUsesExactCatalog(t *testing.T) {
	catalog := syntheticOnboardingCatalog(t)
	st := openAppStoreForTest(t)
	onboarding := st.Onboarding(catalog)
	if err := onboarding.EnsureOnboardingProviderForKind(syntheticOnboardingProviderKind); err != nil {
		t.Fatal(err)
	}
	account := LLMAccount{
		ID:         "future-staging-account",
		ProviderID: syntheticOnboardingProviderKind,
		Label:      "Future staging",
		ConfigDir:  "D:/private/future-staging-account",
	}
	if err := st.CreateLLMAccount(account); err != nil {
		t.Fatal(err)
	}
	state := OnboardingState{
		Phase:        OnboardingPhaseSetup,
		ProviderKind: syntheticOnboardingProviderKind,
		ProviderID:   syntheticOnboardingProviderKind,
		AccountID:    account.ID,
		Revision:     17,
	}
	setOnboardingStateForTest(t, st, state)

	gotState, err := onboarding.OnboardingState()
	if err != nil {
		t.Fatal(err)
	}
	if gotState.ProviderKind != syntheticOnboardingProviderKind {
		t.Fatalf("Catalog-bound state = %+v; want synthetic Provider", gotState)
	}
	got, err := onboarding.OnboardingStagingAccount(state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	want := OnboardingStagingAccount{
		AccountID:    account.ID,
		ProviderID:   syntheticOnboardingProviderKind,
		ProviderKind: syntheticOnboardingProviderKind,
		ConfigDir:    account.ConfigDir,
	}
	if got != want {
		t.Fatalf("Catalog-bound staging Account = %+v; want %+v", got, want)
	}

	before := onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.OnboardingState(); err == nil {
		t.Fatal("default OnboardingState inspected synthetic active kind")
	}
	if _, err := st.OnboardingStagingAccount(state.Revision); err == nil {
		t.Fatal("default OnboardingStagingAccount inspected synthetic active kind")
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatalf("wrong-catalog inspection mutated database:\nbefore=%s\nafter=%s", before, after)
	}

	legacy := openAppStoreForTest(t)
	selected, err := legacy.Onboarding(catalog).SelectOnboardingProvider(
		1,
		syntheticOnboardingProviderKind,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Phase != OnboardingPhaseConnect ||
		selected.ProviderKind != syntheticOnboardingProviderKind {
		t.Fatalf("legacy Catalog-bound selection = %+v; want synthetic Connect", selected)
	}
}

func seedCatalogBoundReadyProviderForTest(
	t *testing.T,
	st *Store,
	onboarding OnboardingStore,
	kind string,
) {
	t.Helper()
	if err := onboarding.EnsureOnboardingProviderForKind(kind); err != nil {
		t.Fatal(err)
	}
	stage := readyOnboardingProviderStageForTest(kind, 0)
	if err := st.CreateLLMAccount(LLMAccount{
		ID:         stage.AccountID,
		ProviderID: stage.ProviderID,
		Label:      kind,
		ConfigDir:  "D:/private/" + stage.AccountID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddLLMModel(LLMModel{
		ProviderID: stage.ProviderID,
		ModelID:    stage.ModelID,
		Name:       stage.ModelID,
		Source:     LLMModelDiscovered,
		Available:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, provider_id, account_id, model_id, updated_at
) VALUES (?, 'ready', 0, ?, ?, ?, '2026-08-16T00:00:00Z')`,
		kind,
		stage.ProviderID,
		stage.AccountID,
		stage.ModelID,
	); err != nil {
		t.Fatal(err)
	}
}
