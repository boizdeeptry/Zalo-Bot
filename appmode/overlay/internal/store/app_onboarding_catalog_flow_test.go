package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	catalogBoundFutureModel       = "future-model"
	catalogBoundFutureDisplayName = "Future Assistant"
	catalogBoundFutureAccountID   = "future-account"
)

func TestCatalogBoundOnboardingFullStoreFlow(t *testing.T) {
	st, onboarding, route, account := prepareCatalogBoundTestRouteForTest(t)
	if len(route.Entries) != 1 || route.Entries[0] != (OnboardingTestRouteEntry{
		Position:   0,
		Kind:       syntheticOnboardingProviderKind,
		ProviderID: syntheticOnboardingProviderKind,
		AccountID:  account.ID,
		ModelID:    catalogBoundFutureModel,
		ConfigDir:  account.ConfigDir,
	}) {
		t.Fatalf("synthetic Test route = %+v; want exact position-zero future route", route.Entries)
	}
	if route.DisplayName != catalogBoundFutureDisplayName {
		t.Fatalf("synthetic Test display name = %q; want %q", route.DisplayName, catalogBoundFutureDisplayName)
	}
	provider := findProvider(t, st, syntheticOnboardingProviderKind)
	if provider.Enabled {
		t.Fatal("synthetic onboarding Provider enabled before completion")
	}
	accounts, err := st.LLMAccounts(syntheticOnboardingProviderKind)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != account.ID || accounts[0].Enabled {
		t.Fatalf("synthetic staged Accounts = %+v; want exact disabled Account", accounts)
	}

	firstNonce := catalogBoundHashForTest("first receipt")
	firstPolicy := NewOnboardingTestReceiptPolicy(time.Date(2099, 8, 16, 1, 0, 0, 0, time.UTC))
	first, err := onboarding.SaveOnboardingTestReceipt(
		context.Background(), route, firstNonce, firstPolicy,
	)
	if err != nil {
		t.Fatalf("save first synthetic receipt: %v", err)
	}

	replaceRoute, err := onboarding.OnboardingTestRoute(context.Background(), first.Revision)
	if err != nil {
		t.Fatalf("re-read synthetic route for receipt replacement: %v", err)
	}
	secondNonce := catalogBoundHashForTest("replacement receipt")
	secondPolicy := NewOnboardingTestReceiptPolicy(firstPolicy.IssuedAt.Add(time.Minute))
	replaced, err := onboarding.SaveOnboardingTestReceipt(
		context.Background(), replaceRoute, secondNonce, secondPolicy,
	)
	if err != nil {
		t.Fatalf("replace synthetic receipt: %v", err)
	}
	replacedRoute := cloneOnboardingTestRoute(replaceRoute)
	replacedRoute.State = replaced
	cleared, err := onboarding.ClearOnboardingTestReceipt(
		context.Background(), replacedRoute, secondNonce, secondPolicy,
	)
	if err != nil {
		t.Fatalf("clear replacement synthetic receipt: %v", err)
	}
	if cleared.TestNonceHash != "" || cleared.TestExpiresAt != "" {
		t.Fatalf("cleared synthetic receipt state = %+v", cleared)
	}

	completionRoute, err := onboarding.OnboardingTestRoute(context.Background(), cleared.Revision)
	if err != nil {
		t.Fatalf("re-read synthetic route for completion receipt: %v", err)
	}
	completionNonce := catalogBoundHashForTest("completion receipt")
	completionPolicy := NewOnboardingTestReceiptPolicy(secondPolicy.IssuedAt.Add(time.Minute))
	receipted, err := onboarding.SaveOnboardingTestReceipt(
		context.Background(), completionRoute, completionNonce, completionPolicy,
	)
	if err != nil {
		t.Fatalf("save synthetic completion receipt: %v", err)
	}
	completed, err := onboarding.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
		Revision:           receipted.Revision,
		TestNonceHash:      completionNonce,
		PersonaFingerprint: completionRoute.State.PersonaFingerprint,
		ConfigBindings: []OnboardingConfigBinding{{
			Kind:      syntheticOnboardingProviderKind,
			AccountID: account.ID,
			ConfigDir: account.ConfigDir,
		}},
		Now: completionPolicy.IssuedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("complete synthetic onboarding: %v", err)
	}
	if completed.Phase != OnboardingPhaseCompleted ||
		completed.CompletedVersion != CurrentOnboardingVersion ||
		completed.Revision != receipted.Revision+1 {
		t.Fatalf("completed synthetic state = %+v", completed)
	}

	provider = findProvider(t, st, syntheticOnboardingProviderKind)
	if !provider.Enabled {
		t.Fatal("synthetic Provider remained disabled after completion")
	}
	accounts, err = st.LLMAccounts(syntheticOnboardingProviderKind)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || !accounts[0].Enabled || accounts[0].ID != account.ID {
		t.Fatalf("synthetic Accounts after completion = %+v", accounts)
	}
	combos, err := st.LLMCombos()
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) != 1 || combos[0].Name != "Mặc định · Future CLI" ||
		len(combos[0].Members) != 1 || combos[0].Members[0].Position != 0 ||
		combos[0].Members[0].ProviderID != syntheticOnboardingProviderKind ||
		combos[0].Members[0].ModelID != catalogBoundFutureModel {
		t.Fatalf("synthetic completed Combo = %+v", combos)
	}
	liveRoute, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if liveRoute.ComboID != combos[0].ID || !slices.Equal(liveRoute.Entries, combos[0].Members) {
		t.Fatalf("synthetic live route = %+v; want Combo members %+v", liveRoute, combos[0].Members)
	}
	var stages int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM app_onboarding_provider_stages`).Scan(&stages); err != nil {
		t.Fatal(err)
	}
	if stages != 0 {
		t.Fatalf("completed synthetic Provider stages = %d; want 0", stages)
	}
}

func TestCatalogBoundTestReceiptRereadsSameCatalog(t *testing.T) {
	st, onboarding, route, _ := prepareCatalogBoundTestRouteForTest(t)
	nonceHash := catalogBoundHashForTest("external I/O receipt")
	policy := NewOnboardingTestReceiptPolicy(time.Date(2099, 8, 16, 2, 0, 0, 0, time.UTC))

	oldBeforeCommit := onboardingTestReceiptBeforeCommit
	t.Cleanup(func() { onboardingTestReceiptBeforeCommit = oldBeforeCommit })
	ctx, cancel := context.WithCancel(context.Background())
	onboardingTestReceiptBeforeCommit = func(context.Context) { cancel() }
	before := completeOnboardingDigestForTest(t, st)
	if _, err := onboarding.SaveOnboardingTestReceipt(ctx, route, nonceHash, policy); !errors.Is(err, context.Canceled) {
		t.Fatalf("synthetic receipt canceled at commit fence = %v; want context.Canceled", err)
	}
	if after := completeOnboardingDigestForTest(t, st); after != before {
		t.Fatalf("canceled synthetic receipt escaped rollback:\nbefore=%s\nafter=%s", before, after)
	}
	onboardingTestReceiptBeforeCommit = oldBeforeCommit

	saved, err := onboarding.SaveOnboardingTestReceipt(
		context.Background(), route, nonceHash, policy,
	)
	if err != nil {
		t.Fatalf("post-I/O synthetic receipt save: %v", err)
	}
	savedRoute := cloneOnboardingTestRoute(route)
	savedRoute.State = saved
	cleared, err := onboarding.ClearOnboardingTestReceipt(
		context.Background(), savedRoute, nonceHash, policy,
	)
	if err != nil {
		t.Fatalf("post-I/O synthetic receipt clear: %v", err)
	}
	if cleared.Revision != saved.Revision+1 || cleared.TestNonceHash != "" || cleared.TestExpiresAt != "" {
		t.Fatalf("cleared synthetic receipt = %+v", cleared)
	}
}

func TestCatalogBoundCompleteUsesSyntheticDisplayAndBindings(t *testing.T) {
	t.Run("catalog display and binding succeed", func(t *testing.T) {
		st, onboarding, route, account := prepareCatalogBoundTestRouteForTest(t)
		if _, err := st.db.Exec(`UPDATE llm_providers SET name = 'Persisted Future Alias'
WHERE id = ?`, syntheticOnboardingProviderKind); err != nil {
			t.Fatal(err)
		}
		route, err := onboarding.OnboardingTestRoute(context.Background(), route.State.Revision)
		if err != nil {
			t.Fatal(err)
		}
		nonceHash := catalogBoundHashForTest("catalog completion")
		policy := NewOnboardingTestReceiptPolicy(time.Date(2099, 8, 16, 3, 0, 0, 0, time.UTC))
		saved, err := onboarding.SaveOnboardingTestReceipt(
			context.Background(), route, nonceHash, policy,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := onboarding.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
			Revision:           saved.Revision,
			TestNonceHash:      nonceHash,
			PersonaFingerprint: route.State.PersonaFingerprint,
			ConfigBindings: []OnboardingConfigBinding{{
				Kind:      syntheticOnboardingProviderKind,
				AccountID: account.ID,
				ConfigDir: account.ConfigDir,
			}},
			Now: policy.IssuedAt.Add(time.Minute),
		}); err != nil {
			t.Fatalf("Catalog-bound CompleteOnboarding() = %v", err)
		}
		combos, err := st.LLMCombos()
		if err != nil {
			t.Fatal(err)
		}
		if len(combos) != 1 || combos[0].Name != "Mặc định · Future CLI" {
			t.Fatalf("completed single-Provider Combo = %+v; want Catalog display name", combos)
		}
		if provider := findProvider(t, st, syntheticOnboardingProviderKind); provider.Name != "Persisted Future Alias" {
			t.Fatalf("completion rewrote persisted Provider metadata: %+v", provider)
		}
	})

	t.Run("wrong binding is atomic", func(t *testing.T) {
		st, onboarding, route, account := prepareCatalogBoundTestRouteForTest(t)
		nonceHash := catalogBoundHashForTest("wrong completion binding")
		policy := NewOnboardingTestReceiptPolicy(time.Date(2099, 8, 16, 3, 30, 0, 0, time.UTC))
		saved, err := onboarding.SaveOnboardingTestReceipt(
			context.Background(), route, nonceHash, policy,
		)
		if err != nil {
			t.Fatal(err)
		}
		before := completeOnboardingDigestForTest(t, st)
		_, err = onboarding.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
			Revision:           saved.Revision,
			TestNonceHash:      nonceHash,
			PersonaFingerprint: route.State.PersonaFingerprint,
			ConfigBindings: []OnboardingConfigBinding{{
				Kind:      "codex",
				AccountID: account.ID,
				ConfigDir: account.ConfigDir,
			}},
			Now: policy.IssuedAt.Add(time.Minute),
		})
		if !errors.Is(err, ErrOnboardingConfigurationChanged) {
			t.Fatalf("wrong Catalog completion binding error = %v; want ErrOnboardingConfigurationChanged", err)
		}
		if after := completeOnboardingDigestForTest(t, st); after != before {
			t.Fatalf("wrong Catalog binding mutated completion data:\nbefore=%s\nafter=%s", before, after)
		}
	})
}

func TestEnsureAccountRuntimeProviderRequiresExactSingleton(t *testing.T) {
	t.Run("missing row creates exact enabled singleton", func(t *testing.T) {
		st := openAppStoreForTest(t)
		if err := st.EnsureAccountRuntimeProvider(
			syntheticOnboardingProviderKind,
			"Future CLI",
		); err != nil {
			t.Fatal(err)
		}
		provider := findProvider(t, st, syntheticOnboardingProviderKind)
		if provider.ID != syntheticOnboardingProviderKind ||
			provider.Kind != syntheticOnboardingProviderKind ||
			provider.Name != "Future CLI" || !provider.Enabled || provider.System ||
			provider.CredentialConfigured {
			t.Fatalf("created account-runtime Provider = %+v", provider)
		}
	})

	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("exact existing enabled=%t is untouched", enabled), func(t *testing.T) {
			st := openAppStoreForTest(t)
			if _, err := st.db.Exec(`INSERT INTO llm_providers(
id, name, kind, enabled, system_provider, credential_cipher,
last_check_status, last_error, last_checked_at
) VALUES (?, 'Existing Name', ?, ?, 0, x'0102', 'warn', 'existing error', '2026-08-16T00:00:00Z')`,
				syntheticOnboardingProviderKind,
				syntheticOnboardingProviderKind,
				boolInt(enabled),
			); err != nil {
				t.Fatal(err)
			}
			before := catalogBoundProviderRowsForTest(t, st)
			for call := 0; call < 2; call++ {
				if err := st.EnsureAccountRuntimeProvider(
					syntheticOnboardingProviderKind,
					"New Catalog Name",
				); err != nil {
					t.Fatalf("idempotent exact ensure call %d: %v", call+1, err)
				}
			}
			if after := catalogBoundProviderRowsForTest(t, st); after != before {
				t.Fatalf("exact ensure rewrote Provider fields:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}

	conflicts := []struct {
		name  string
		rows  []LLMProvider
		query string
	}{
		{
			name: "exact ID has wrong kind",
			rows: []LLMProvider{{
				ID: syntheticOnboardingProviderKind, Name: "Wrong", Kind: "other-runtime", Enabled: true,
			}},
		},
		{
			name: "same-kind sibling exists without exact",
			rows: []LLMProvider{{
				ID: "future-sibling", Name: "Sibling", Kind: syntheticOnboardingProviderKind, Enabled: true,
			}},
		},
		{
			name: "exact and same-kind sibling both exist",
			rows: []LLMProvider{
				{ID: syntheticOnboardingProviderKind, Name: "Exact", Kind: syntheticOnboardingProviderKind},
				{ID: "future-sibling", Name: "Sibling", Kind: syntheticOnboardingProviderKind, Enabled: true},
			},
		},
	}
	for _, test := range conflicts {
		t.Run(test.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			for _, provider := range test.rows {
				if err := st.CreateLLMProvider(provider); err != nil {
					t.Fatal(err)
				}
			}
			before := catalogBoundProviderRowsForTest(t, st)
			err := st.EnsureAccountRuntimeProvider(syntheticOnboardingProviderKind, "Future CLI")
			if !errors.Is(err, ErrLLMProviderSingletonConflict) {
				t.Fatalf("singleton conflict error = %v; want ErrLLMProviderSingletonConflict", err)
			}
			if after := catalogBoundProviderRowsForTest(t, st); after != before {
				t.Fatalf("singleton rejection mutated Provider rows:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}

	invalid := []struct {
		name, kind, displayName string
	}{
		{name: "blank kind", kind: "", displayName: "Future CLI"},
		{name: "uppercase kind", kind: "Future-cli", displayName: "Future CLI"},
		{name: "decorated kind", kind: " future-cli", displayName: "Future CLI"},
		{name: "double hyphen kind", kind: "future--cli", displayName: "Future CLI"},
		{name: "overlong kind", kind: strings.Repeat("a", 65), displayName: "Future CLI"},
		{name: "blank display", kind: syntheticOnboardingProviderKind, displayName: ""},
		{name: "trimmed display", kind: syntheticOnboardingProviderKind, displayName: " Future CLI"},
		{name: "control display", kind: syntheticOnboardingProviderKind, displayName: "Future\nCLI"},
		{name: "format display", kind: syntheticOnboardingProviderKind, displayName: "Future\u200eCLI"},
		{name: "invalid UTF-8 display", kind: syntheticOnboardingProviderKind, displayName: string([]byte{0xff})},
		{name: "overlong display", kind: syntheticOnboardingProviderKind, displayName: strings.Repeat("x", 129)},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			before := catalogBoundProviderRowsForTest(t, st)
			if err := st.EnsureAccountRuntimeProvider(test.kind, test.displayName); err == nil {
				t.Fatal("invalid trusted account-runtime metadata was accepted")
			}
			if after := catalogBoundProviderRowsForTest(t, st); after != before {
				t.Fatalf("invalid metadata mutated Provider rows:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestCatalogBoundFlowRollbackAndDefaultRejection(t *testing.T) {
	t.Run("Bind and Setup commit failures roll back and retries reconcile", func(t *testing.T) {
		st, onboarding, connected := prepareCatalogBoundConnectForTest(t)
		account := catalogBoundFutureAccountForTest()
		oldCommit := onboardingProvidersCommit
		t.Cleanup(func() { onboardingProvidersCommit = oldCommit })
		onboardingProvidersCommit = func(*sql.Tx) error { return errors.New("injected Catalog Bind commit") }
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := onboarding.BindOnboardingAccount(
			connected.State.Revision, syntheticOnboardingProviderKind, account,
		); err == nil {
			t.Fatal("Catalog-bound Bind commit failure unexpectedly succeeded")
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("Catalog-bound Bind commit failure escaped rollback:\nbefore=%s\nafter=%s", before, after)
		}

		onboardingProvidersCommit = oldCommit
		bound, err := onboarding.BindOnboardingAccount(
			connected.State.Revision, syntheticOnboardingProviderKind, account,
		)
		if err != nil {
			t.Fatal(err)
		}
		bindReplay, err := onboarding.BindOnboardingAccount(
			connected.State.Revision, syntheticOnboardingProviderKind, account,
		)
		if err != nil || bindReplay.State != bound.State || !slices.Equal(bindReplay.Stages, bound.Stages) {
			t.Fatalf("Catalog-bound Bind lost-response replay = %+v, %v; want %+v", bindReplay, err, bound)
		}
		addOnboardingMultiModelForTest(
			t, st, syntheticOnboardingProviderKind, catalogBoundFutureModel, true,
		)

		onboardingProvidersCommit = func(*sql.Tx) error { return errors.New("injected Catalog Setup commit") }
		before = onboardingProviderMutationDigestForTest(t, st)
		if _, err := onboarding.StageOnboardingSetup(
			bound.State.Revision,
			syntheticOnboardingProviderKind,
			account.ID,
			catalogBoundFutureModel,
		); err == nil {
			t.Fatal("Catalog-bound Setup commit failure unexpectedly succeeded")
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatalf("Catalog-bound Setup commit failure escaped rollback:\nbefore=%s\nafter=%s", before, after)
		}

		onboardingProvidersCommit = oldCommit
		staged, err := onboarding.StageOnboardingSetup(
			bound.State.Revision,
			syntheticOnboardingProviderKind,
			account.ID,
			catalogBoundFutureModel,
		)
		if err != nil {
			t.Fatal(err)
		}
		setupReplay, err := onboarding.StageOnboardingSetup(
			bound.State.Revision,
			syntheticOnboardingProviderKind,
			account.ID,
			catalogBoundFutureModel,
		)
		if err != nil || setupReplay.State != staged.State || !slices.Equal(setupReplay.Stages, staged.Stages) {
			t.Fatalf("Catalog-bound Setup lost-response replay = %+v, %v; want %+v", setupReplay, err, staged)
		}
	})

	t.Run("default compatibility methods reject a persisted synthetic flow", func(t *testing.T) {
		st, onboarding, connected := prepareCatalogBoundConnectForTest(t)
		account := catalogBoundFutureAccountForTest()
		before := onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.BindOnboardingAccount(
			connected.State.Revision, syntheticOnboardingProviderKind, account,
		); err == nil {
			t.Fatal("default Bind accepted persisted synthetic Provider")
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("default Bind rejection mutated synthetic flow")
		}
		bound, err := onboarding.BindOnboardingAccount(
			connected.State.Revision, syntheticOnboardingProviderKind, account,
		)
		if err != nil {
			t.Fatal(err)
		}
		addOnboardingMultiModelForTest(
			t, st, syntheticOnboardingProviderKind, catalogBoundFutureModel, true,
		)
		before = onboardingProviderMutationDigestForTest(t, st)
		if _, err := st.StageOnboardingSetup(
			bound.State.Revision,
			syntheticOnboardingProviderKind,
			account.ID,
			catalogBoundFutureModel,
		); err == nil {
			t.Fatal("default Setup accepted persisted synthetic Provider")
		}
		if after := onboardingProviderMutationDigestForTest(t, st); after != before {
			t.Fatal("default Setup rejection mutated synthetic flow")
		}
		staged, err := onboarding.StageOnboardingSetup(
			bound.State.Revision,
			syntheticOnboardingProviderKind,
			account.ID,
			catalogBoundFutureModel,
		)
		if err != nil {
			t.Fatal(err)
		}
		tested, err := st.AdvanceOnboardingPersona(
			staged.State.Revision,
			catalogBoundHashForTest("default rejection persona"),
			catalogBoundFutureDisplayName,
		)
		if err != nil {
			t.Fatal(err)
		}
		before = completeOnboardingDigestForTest(t, st)
		if _, err := st.OnboardingTestRoute(context.Background(), tested.Revision); err == nil {
			t.Fatal("default Test route inspected persisted synthetic Provider")
		}
		if after := completeOnboardingDigestForTest(t, st); after != before {
			t.Fatal("default Test-route rejection mutated synthetic flow")
		}
		route, err := onboarding.OnboardingTestRoute(context.Background(), tested.Revision)
		if err != nil {
			t.Fatal(err)
		}
		nonceHash := catalogBoundHashForTest("default receipt rejection")
		policy := NewOnboardingTestReceiptPolicy(time.Date(2099, 8, 16, 4, 0, 0, 0, time.UTC))
		before = completeOnboardingDigestForTest(t, st)
		if _, err := st.SaveOnboardingTestReceipt(
			context.Background(), route, nonceHash, policy,
		); err == nil {
			t.Fatal("default receipt Save accepted persisted synthetic Provider")
		}
		if after := completeOnboardingDigestForTest(t, st); after != before {
			t.Fatal("default receipt-Save rejection mutated synthetic flow")
		}
		saved, err := onboarding.SaveOnboardingTestReceipt(
			context.Background(), route, nonceHash, policy,
		)
		if err != nil {
			t.Fatal(err)
		}
		savedRoute := cloneOnboardingTestRoute(route)
		savedRoute.State = saved
		before = completeOnboardingDigestForTest(t, st)
		if _, err := st.ClearOnboardingTestReceipt(
			context.Background(), savedRoute, nonceHash, policy,
		); err == nil {
			t.Fatal("default receipt Clear accepted persisted synthetic Provider")
		}
		if after := completeOnboardingDigestForTest(t, st); after != before {
			t.Fatal("default receipt-Clear rejection mutated synthetic flow")
		}
		before = completeOnboardingDigestForTest(t, st)
		if _, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
			Revision:           saved.Revision,
			TestNonceHash:      nonceHash,
			PersonaFingerprint: route.State.PersonaFingerprint,
			ConfigBindings: []OnboardingConfigBinding{{
				Kind:      syntheticOnboardingProviderKind,
				AccountID: account.ID,
				ConfigDir: account.ConfigDir,
			}},
			Now: policy.IssuedAt.Add(time.Minute),
		}); err == nil {
			t.Fatal("default Complete accepted persisted synthetic Provider")
		}
		if after := completeOnboardingDigestForTest(t, st); after != before {
			t.Fatal("default Complete rejection mutated synthetic flow")
		}
	})

	t.Run("Complete commit error rolls back and preserves reusable synthetic receipt", func(t *testing.T) {
		st, onboarding, route, account := prepareCatalogBoundTestRouteForTest(t)
		nonceHash := catalogBoundHashForTest("Catalog Complete retry")
		policy := NewOnboardingTestReceiptPolicy(time.Date(2099, 8, 16, 5, 0, 0, 0, time.UTC))
		saved, err := onboarding.SaveOnboardingTestReceipt(
			context.Background(), route, nonceHash, policy,
		)
		if err != nil {
			t.Fatal(err)
		}
		input := CompleteOnboardingInput{
			Revision:           saved.Revision,
			TestNonceHash:      nonceHash,
			PersonaFingerprint: route.State.PersonaFingerprint,
			ConfigBindings: []OnboardingConfigBinding{{
				Kind:      syntheticOnboardingProviderKind,
				AccountID: account.ID,
				ConfigDir: account.ConfigDir,
			}},
			Now: policy.IssuedAt.Add(time.Minute),
		}
		oldCommit := onboardingCompleteCommit
		t.Cleanup(func() { onboardingCompleteCommit = oldCommit })
		onboardingCompleteCommit = func(*sql.Tx) error { return errors.New("injected Catalog Complete commit") }
		before := completeOnboardingDigestForTest(t, st)
		if _, err := onboarding.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingCommitFailed) {
			t.Fatalf("Catalog-bound Complete commit error = %v; want ErrOnboardingCommitFailed", err)
		}
		if after := completeOnboardingDigestForTest(t, st); after != before {
			t.Fatalf("Catalog-bound Complete commit error escaped rollback:\nbefore=%s\nafter=%s", before, after)
		}
		onboardingCompleteCommit = oldCommit
		completed, err := onboarding.CompleteOnboarding(context.Background(), input)
		if err != nil {
			t.Fatalf("Catalog-bound Complete retry after commit error: %v", err)
		}
		if completed.Phase != OnboardingPhaseCompleted || completed.Revision != saved.Revision+1 {
			t.Fatalf("Catalog-bound Complete retry = %+v", completed)
		}
	})
}

func TestCatalogBoundPersonaAndRestartMutationsRemainCatalogNeutral(t *testing.T) {
	st, onboarding, route, _ := prepareCatalogBoundTestRouteForTest(t)
	beforeUpdate, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	updated, err := st.UpdateAgentPersona("Future Updated", "future-recovery")
	if err != nil {
		t.Fatalf("raw UpdateAgentPersona() consulted Default Catalog: %v", err)
	}
	afterUpdate, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if updated.Phase != OnboardingPhasePersona || updated.PersonaFingerprint != "" ||
		updated.TestNonceHash != "" || updated.TestExpiresAt != "" ||
		!slices.Equal(afterUpdate.Stages, beforeUpdate.Stages) {
		t.Fatalf("Catalog-neutral persona update = %+v; stages before=%+v after=%+v",
			updated, beforeUpdate.Stages, afterUpdate.Stages)
	}

	oldBeforeCAS := onboardingPersonaBeforeCAS
	t.Cleanup(func() { onboardingPersonaBeforeCAS = oldBeforeCAS })
	onboardingPersonaBeforeCAS = func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE app_onboarding_state SET revision = revision + 1 WHERE id = 1`)
		return err
	}
	nameBeforeCAS, err := st.AgentDisplayName()
	if err != nil {
		t.Fatal(err)
	}
	snapshotBeforeCAS, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdvanceOnboardingPersona(
		updated.Revision,
		catalogBoundHashForTest("CAS persona"),
		"CAS must roll back",
	); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("raw persona CAS error = %v; want ErrOnboardingConflict", err)
	}
	snapshotAfterCAS, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	nameAfterCAS, err := st.AgentDisplayName()
	if err != nil {
		t.Fatal(err)
	}
	if snapshotAfterCAS.State != snapshotBeforeCAS.State ||
		!slices.Equal(snapshotAfterCAS.Stages, snapshotBeforeCAS.Stages) ||
		nameAfterCAS != nameBeforeCAS {
		t.Fatalf("persona CAS failure mutated Catalog-bound flow: before=%+v/%q after=%+v/%q",
			snapshotBeforeCAS, nameBeforeCAS, snapshotAfterCAS, nameAfterCAS)
	}
	onboardingPersonaBeforeCAS = oldBeforeCAS

	tested, err := st.AdvanceOnboardingPersona(
		updated.Revision,
		catalogBoundHashForTest("neutral persona"),
		catalogBoundFutureDisplayName,
	)
	if err != nil {
		t.Fatalf("raw AdvanceOnboardingPersona() consulted Default Catalog: %v", err)
	}
	afterAdvance, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if tested.Phase != OnboardingPhaseTest || !slices.Equal(afterAdvance.Stages, beforeUpdate.Stages) {
		t.Fatalf("Catalog-neutral persona advance = %+v; stages=%+v", tested, afterAdvance.Stages)
	}
	invalidated, err := st.InvalidateOnboardingPersona()
	if err != nil {
		t.Fatalf("raw InvalidateOnboardingPersona() consulted Default Catalog: %v", err)
	}
	afterInvalidate, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if invalidated.Phase != OnboardingPhasePersona || invalidated.PersonaFingerprint != "" ||
		invalidated.TestNonceHash != "" || invalidated.TestExpiresAt != "" ||
		!slices.Equal(afterInvalidate.Stages, beforeUpdate.Stages) {
		t.Fatalf("Catalog-neutral persona invalidation = %+v; stages=%+v", invalidated, afterInvalidate.Stages)
	}

	completedFixture := OnboardingState{
		CompletedVersion:   CurrentOnboardingVersion,
		Phase:              OnboardingPhaseCompleted,
		ProviderKind:       syntheticOnboardingProviderKind,
		ProviderID:         syntheticOnboardingProviderKind,
		AccountID:          catalogBoundFutureAccountID,
		ModelID:            catalogBoundFutureModel,
		StagedComboID:      route.State.StagedComboID,
		PersonaFingerprint: catalogBoundHashForTest("completed persona"),
		TestNonceHash:      catalogBoundHashForTest("completed receipt"),
		TestExpiresAt:      time.Date(2099, 8, 16, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Revision:           invalidated.Revision + 10,
	}
	setOnboardingStateForTest(t, st, completedFixture)
	beforeRestart, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := st.RestartOnboarding(completedFixture.Revision, "")
	if err != nil {
		t.Fatalf("raw RestartOnboarding() consulted Default Catalog: %v", err)
	}
	afterRestart, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Phase != OnboardingPhaseProvider || !restarted.RestartInProgress ||
		restarted.ProviderKind != "" || restarted.ProviderID != "" || restarted.AccountID != "" ||
		restarted.ModelID != "" || restarted.StagedComboID != "" ||
		restarted.PersonaFingerprint != "" || restarted.TestNonceHash != "" ||
		restarted.TestExpiresAt != "" || !slices.Equal(afterRestart.Stages, beforeRestart.Stages) {
		t.Fatalf("Catalog-neutral restart = %+v; stages before=%+v after=%+v",
			restarted, beforeRestart.Stages, afterRestart.Stages)
	}
	stable := afterRestart
	if _, err := st.RestartOnboarding(completedFixture.Revision, ""); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("stale raw Restart error = %v; want ErrOnboardingConflict", err)
	}
	afterStale, err := onboarding.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if afterStale.State != stable.State || !slices.Equal(afterStale.Stages, stable.Stages) {
		t.Fatalf("stale restart mutated Catalog-bound flow: before=%+v after=%+v", stable, afterStale)
	}
}

func prepareCatalogBoundConnectForTest(
	t *testing.T,
) (*Store, OnboardingStore, OnboardingSnapshot) {
	t.Helper()
	st := openAppStoreForTest(t)
	onboarding := st.Onboarding(syntheticOnboardingCatalog(t))
	if err := onboarding.EnsureOnboardingProviderForKind(syntheticOnboardingProviderKind); err != nil {
		t.Fatal(err)
	}
	provider := findProvider(t, st, syntheticOnboardingProviderKind)
	if provider.Enabled {
		t.Fatal("new onboarding Provider must be disabled")
	}
	selected, err := onboarding.ReplaceOnboardingProviderSelection(
		1,
		[]string{syntheticOnboardingProviderKind},
	)
	if err != nil {
		t.Fatal(err)
	}
	connected, err := onboarding.BeginOnboardingProvider(
		selected.State.Revision,
		syntheticOnboardingProviderKind,
	)
	if err != nil {
		t.Fatal(err)
	}
	return st, onboarding, connected
}

func prepareCatalogBoundTestRouteForTest(
	t *testing.T,
) (*Store, OnboardingStore, OnboardingTestRoute, LLMAccount) {
	t.Helper()
	st, onboarding, connected := prepareCatalogBoundConnectForTest(t)
	account := catalogBoundFutureAccountForTest()
	bound, err := onboarding.BindOnboardingAccount(
		connected.State.Revision,
		syntheticOnboardingProviderKind,
		account,
	)
	if err != nil {
		t.Fatal(err)
	}
	bindReplay, err := onboarding.BindOnboardingAccount(
		connected.State.Revision,
		syntheticOnboardingProviderKind,
		account,
	)
	if err != nil || bindReplay.State != bound.State || !slices.Equal(bindReplay.Stages, bound.Stages) {
		t.Fatalf("synthetic Bind lost-response replay = %+v, %v; want %+v", bindReplay, err, bound)
	}
	addOnboardingMultiModelForTest(
		t,
		st,
		syntheticOnboardingProviderKind,
		catalogBoundFutureModel,
		true,
	)
	staged, err := onboarding.StageOnboardingSetup(
		bound.State.Revision,
		syntheticOnboardingProviderKind,
		account.ID,
		catalogBoundFutureModel,
	)
	if err != nil {
		t.Fatal(err)
	}
	setupReplay, err := onboarding.StageOnboardingSetup(
		bound.State.Revision,
		syntheticOnboardingProviderKind,
		account.ID,
		catalogBoundFutureModel,
	)
	if err != nil || setupReplay.State != staged.State || !slices.Equal(setupReplay.Stages, staged.Stages) {
		t.Fatalf("synthetic Setup lost-response replay = %+v, %v; want %+v", setupReplay, err, staged)
	}
	if staged.State.Phase != OnboardingPhasePersona || len(staged.Stages) != 1 ||
		staged.Stages[0].Position != 0 || staged.Stages[0].Kind != syntheticOnboardingProviderKind ||
		staged.Stages[0].Status != onboardingProviderStageReady {
		t.Fatalf("synthetic staged route = %+v", staged)
	}
	tested, err := st.AdvanceOnboardingPersona(
		staged.State.Revision,
		catalogBoundHashForTest("future persona"),
		catalogBoundFutureDisplayName,
	)
	if err != nil {
		t.Fatal(err)
	}
	route, err := onboarding.OnboardingTestRoute(context.Background(), tested.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return st, onboarding, route, account
}

func catalogBoundFutureAccountForTest() LLMAccount {
	addedAt := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	return LLMAccount{
		ID:         catalogBoundFutureAccountID,
		ProviderID: syntheticOnboardingProviderKind,
		Label:      "Future staging Account",
		Email:      "future@example.test",
		ConfigDir:  "D:/onboarding/future-account",
		Enabled:    false,
		AddedAt:    &addedAt,
	}
}

func catalogBoundHashForTest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest[:])
}

func catalogBoundProviderRowsForTest(t *testing.T, st *Store) string {
	t.Helper()
	rows, err := st.db.Query(`SELECT
id, name, kind, enabled, system_provider, hex(credential_cipher),
last_check_status, last_error, last_checked_at
FROM llm_providers ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var digest strings.Builder
	for rows.Next() {
		var id, name, kind, credential, status, lastError, checkedAt string
		var enabled, system int64
		if err := rows.Scan(
			&id,
			&name,
			&kind,
			&enabled,
			&system,
			&credential,
			&status,
			&lastError,
			&checkedAt,
		); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(
			&digest,
			"%q/%q/%q/%d/%d/%q/%q/%q/%q;",
			id,
			name,
			kind,
			enabled,
			system,
			credential,
			status,
			lastError,
			checkedAt,
		)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return digest.String()
}
