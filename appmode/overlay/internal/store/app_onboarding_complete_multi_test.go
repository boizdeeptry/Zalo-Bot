package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCompleteOnboardingActivatesOrderedProviderFallbackMulti(t *testing.T) {
	st, stagedRoute, input := seedCompleteOnboardingMultiForTask7(t, 101)
	stagedComboID := stagedRoute.State.StagedComboID
	if _, err := st.db.Exec(`UPDATE llm_providers SET enabled=1 WHERE id='codex'`); err != nil {
		t.Fatal(err)
	}

	completed, err := st.CompleteOnboarding(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Phase != OnboardingPhaseCompleted ||
		completed.CompletedVersion != CurrentOnboardingVersion ||
		completed.Revision != input.Revision+1 || completed.RestartInProgress ||
		completed.ProviderKind != "" || completed.ProviderID != "" ||
		completed.AccountID != "" || completed.ModelID != "" ||
		completed.StagedComboID != "" || completed.PersonaFingerprint != "" ||
		completed.TestNonceHash != "" || completed.TestExpiresAt != "" {
		t.Fatalf("completed singleton = %+v", completed)
	}
	snapshot, err := st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Stages) != 0 {
		t.Fatalf("completed stages = %+v; want empty", snapshot.Stages)
	}

	wantMembers := []LLMRouteEntry{
		{Position: 0, ProviderID: "codex", ModelID: "codex-model", Enabled: true},
		{Position: 1, ProviderID: "claude-code", ModelID: "claude-model", Enabled: true},
	}
	live, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if live.ComboID != stagedComboID || live.Type != "fallback" ||
		!slices.Equal(live.Entries, wantMembers) {
		t.Fatalf("completed route = %+v; want combo %q entries %+v", live, stagedComboID, wantMembers)
	}
	if legacy := onboardingLegacyRouteForTest(t, st); legacy !=
		"0:codex:codex-model:1,1:claude-code:claude-model:1" {
		t.Fatalf("legacy projection = %q", legacy)
	}
	assertTask7ProviderAndAccountsActivated(t, st, "codex", "codex-account", "codex-old-live")
	assertTask7ProviderAndAccountsActivated(t, st, "claude-code", "claude-account", "claude-old-live")
	accounts, err := st.LLMAccounts("unrelated-provider")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || !accounts[0].Enabled || accounts[0].ID != "unrelated-account" {
		t.Fatalf("unrelated accounts = %+v; want unchanged live account", accounts)
	}

	beforeRetry := completeOnboardingDigestForTest(t, st)
	if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingConflict) &&
		!errors.Is(err, ErrOnboardingTestRequired) {
		t.Fatalf("one-time CompleteOnboarding() error = %v; want conflict/test required", err)
	}
	if afterRetry := completeOnboardingDigestForTest(t, st); afterRetry != beforeRetry {
		t.Fatal("one-time receipt retry mutated completed state")
	}
}

func TestCompleteOnboardingNamesSingleProviderFromSharedCatalog(t *testing.T) {
	st, original := seedOnboardingTestRouteForTask6(t, 111)
	if _, err := st.db.Exec(`DELETE FROM app_onboarding_provider_stages WHERE kind='codex'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE app_onboarding_provider_stages SET position=0 WHERE kind='claude-code'`); err != nil {
		t.Fatal(err)
	}
	route, err := st.OnboardingTestRoute(context.Background(), original.State.Revision)
	if err != nil {
		t.Fatal(err)
	}
	nonceHash := completeOnboardingHashForTest()
	issuedAt := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	saved, err := st.SaveOnboardingTestReceipt(
		context.Background(), route, nonceHash, NewOnboardingTestReceiptPolicy(issuedAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
		Revision: saved.Revision, TestNonceHash: nonceHash,
		PersonaFingerprint: saved.PersonaFingerprint,
		ConfigBindings: []OnboardingConfigBinding{{
			Kind: "claude-code", AccountID: "claude-account", ConfigDir: route.Entries[0].ConfigDir,
		}},
		Now: issuedAt.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	combos, err := st.LLMCombos()
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) != 1 || combos[0].Name != "Mặc định · Claude Code" {
		t.Fatalf("single-provider Combo = %+v; want shared catalog display name", combos)
	}
}

func TestCompleteOnboardingRequiresExactConfigBindingSetMulti(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]OnboardingConfigBinding) []OnboardingConfigBinding
	}{
		{name: "empty", mutate: func([]OnboardingConfigBinding) []OnboardingConfigBinding { return nil }},
		{name: "missing", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding { return in[:1] }},
		{name: "extra", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding {
			return append(in, OnboardingConfigBinding{Kind: "codex", AccountID: "extra", ConfigDir: "D:/extra"})
		}},
		{name: "duplicate", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding {
			return append(in, in[0])
		}},
		{name: "swapped directories", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding {
			in[0].ConfigDir, in[1].ConfigDir = in[1].ConfigDir, in[0].ConfigDir
			return in
		}},
		{name: "blank kind", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding { in[0].Kind = ""; return in }},
		{name: "blank account", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding { in[0].AccountID = ""; return in }},
		{name: "blank directory", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding { in[0].ConfigDir = ""; return in }},
		{name: "wrong kind", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding { in[0].Kind = "codex"; return in }},
		{name: "wrong account", mutate: func(in []OnboardingConfigBinding) []OnboardingConfigBinding {
			in[0].AccountID = "codex-account"
			return in
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, _, input := seedCompleteOnboardingMultiForTask7(t, 121)
			input.ConfigBindings = test.mutate(slices.Clone(input.ConfigBindings))
			before := completeOnboardingDigestForTest(t, st)
			if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingConfigurationChanged) {
				t.Fatalf("CompleteOnboarding() error = %v; want configuration changed", err)
			}
			if after := completeOnboardingDigestForTest(t, st); after != before {
				t.Fatalf("invalid binding set mutated database:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestCompleteOnboardingRollsBackMidMemberLoopsAndStageDeleteMulti(t *testing.T) {
	for _, stage := range []string{
		onboardingCompleteStageAccounts,
		onboardingCompleteStageMemberInsert,
		onboardingCompleteStageRouteSync,
	} {
		t.Run("second "+stage, func(t *testing.T) {
			st, _, input := seedCompleteOnboardingMultiForTask7(t, 161)
			before := completeOnboardingDigestForTest(t, st)
			oldFailpoint := onboardingCompleteFailpoint
			calls := 0
			onboardingCompleteFailpoint = func(current string) error {
				if current == stage {
					calls++
					if calls == 2 {
						return errors.New("fail second member")
					}
				}
				return nil
			}
			t.Cleanup(func() { onboardingCompleteFailpoint = oldFailpoint })

			if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingCommitFailed) {
				t.Fatalf("CompleteOnboarding() error = %v; want commit failure", err)
			}
			if calls != 2 {
				t.Fatalf("%q failpoint calls = %d; want 2", stage, calls)
			}
			if after := completeOnboardingDigestForTest(t, st); after != before {
				t.Fatalf("mid-loop failure escaped rollback:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}

	t.Run("stage delete trigger", func(t *testing.T) {
		st, _, input := seedCompleteOnboardingMultiForTask7(t, 171)
		if _, err := st.db.Exec(`CREATE TRIGGER reject_complete_stage_delete
BEFORE DELETE ON app_onboarding_provider_stages
WHEN OLD.position = 1
BEGIN SELECT RAISE(ABORT, 'injected stage delete failure'); END`); err != nil {
			t.Fatal(err)
		}
		before := completeOnboardingDigestForTest(t, st)
		if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingCommitFailed) {
			t.Fatalf("CompleteOnboarding() error = %v; want commit failure", err)
		}
		if after := completeOnboardingDigestForTest(t, st); after != before {
			t.Fatalf("stage delete failure escaped rollback:\nbefore=%s\nafter=%s", before, after)
		}
	})
}

func TestCompleteOnboardingRevisionOverflowDoesNotMutateMulti(t *testing.T) {
	st, _, input := seedCompleteOnboardingMultiForTask7(t, 181)
	state := mustOnboardingStateForTest(t, st)
	state.Revision = maxOnboardingRouteRevision
	setOnboardingStateForTest(t, st, state)
	input.Revision = maxOnboardingRouteRevision
	before := completeOnboardingDigestForTest(t, st)
	if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingConfigurationChanged) {
		t.Fatalf("CompleteOnboarding() overflow error = %v; want configuration changed", err)
	}
	if after := completeOnboardingDigestForTest(t, st); after != before {
		t.Fatal("revision overflow mutated database")
	}
}

func TestCompleteOnboardingRejectsInvalidComboRevisionHistoryMulti(t *testing.T) {
	for _, test := range []struct {
		name   string
		insert string
	}{
		{name: "hidden negative", insert: `INSERT INTO llm_combos(id, name, type, active, revision) VALUES
('invalid-history', 'invalid', 'fallback', 0, -1),
('positive-history', 'positive', 'fallback', 1, 5)`},
		{name: "malformed", insert: `INSERT INTO llm_combos(id, name, type, active, revision) VALUES
('invalid-history', 'invalid', 'fallback', 0, 'not-a-revision'),
('positive-history', 'positive', 'fallback', 1, 5)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, _, input := seedCompleteOnboardingMultiForTask7(t, 191)
			if _, err := st.db.Exec(test.insert); err != nil {
				t.Fatal(err)
			}
			before := completeOnboardingDigestForTest(t, st)
			if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingConfigurationChanged) {
				t.Fatalf("CompleteOnboarding() invalid history error = %v; want configuration changed", err)
			}
			if after := completeOnboardingDigestForTest(t, st); after != before {
				t.Fatal("invalid Combo revision mutated database")
			}
		})
	}
}

func TestCompleteOnboardingRejectsCorruptFinalSingletonMulti(t *testing.T) {
	st, _, input := seedCompleteOnboardingMultiForTask7(t, 201)
	if _, err := st.db.Exec(`CREATE TRIGGER corrupt_completed_onboarding_singleton
AFTER UPDATE OF phase ON app_onboarding_state
WHEN NEW.id = 1 AND NEW.phase = 'completed'
BEGIN
  UPDATE app_onboarding_state SET provider_kind = 'SINGLETON_SECRET' WHERE id = 1;
END`); err != nil {
		t.Fatal(err)
	}
	before := completeOnboardingDigestForTest(t, st)
	if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingCommitFailed) {
		t.Fatalf("CompleteOnboarding() corrupt final singleton error = %v; want commit failure", err)
	}
	if after := completeOnboardingDigestForTest(t, st); after != before {
		t.Fatalf("corrupt final singleton escaped rollback:\nbefore=%s\nafter=%s", before, after)
	}
	if _, err := st.db.Exec(`DROP TRIGGER corrupt_completed_onboarding_singleton`); err != nil {
		t.Fatal(err)
	}
	if completed, err := st.CompleteOnboarding(context.Background(), input); err != nil ||
		completed.Phase != OnboardingPhaseCompleted {
		t.Fatalf("retry after corrupt singleton trigger = %+v, %v", completed, err)
	}
}

func TestCompleteOnboardingRejectsIgnoredRouteRevisionUpdateMulti(t *testing.T) {
	st, _, input := seedCompleteOnboardingMultiForTask7(t, 211)
	if _, err := st.db.Exec(`UPDATE app_meta SET value='41' WHERE key='llm_route_revision'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER ignore_completed_route_revision
BEFORE UPDATE OF value ON app_meta
WHEN OLD.key = 'llm_route_revision'
BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	before := completeOnboardingDigestForTest(t, st)
	if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingCommitFailed) {
		t.Fatalf("CompleteOnboarding() ignored route revision error = %v; want commit failure", err)
	}
	if after := completeOnboardingDigestForTest(t, st); after != before {
		t.Fatalf("ignored route revision escaped rollback:\nbefore=%s\nafter=%s", before, after)
	}
	if _, err := st.db.Exec(`DROP TRIGGER ignore_completed_route_revision`); err != nil {
		t.Fatal(err)
	}
	if completed, err := st.CompleteOnboarding(context.Background(), input); err != nil ||
		completed.Phase != OnboardingPhaseCompleted {
		t.Fatalf("retry after ignored route revision trigger = %+v, %v", completed, err)
	}
	if got := onboardingRouteRevisionForTest(t, st); got != 42 {
		t.Fatalf("route revision after retry = %d; want 42", got)
	}
}

func TestCompleteOnboardingFinalDurabilityFenceRejectsProjectionTriggersMulti(t *testing.T) {
	tests := []struct {
		name        string
		triggerName string
		triggerSQL  string
	}{
		{
			name: "ignored Combo member", triggerName: "ignore_completed_combo_member",
			triggerSQL: `CREATE TRIGGER ignore_completed_combo_member
BEFORE INSERT ON llm_combo_members WHEN NEW.position = 1
BEGIN SELECT RAISE(IGNORE); END`,
		},
		{
			name: "mutated Combo member", triggerName: "mutate_completed_combo_member",
			triggerSQL: `CREATE TRIGGER mutate_completed_combo_member
AFTER INSERT ON llm_combo_members WHEN NEW.position = 1
BEGIN
  UPDATE llm_combo_members SET model_id = 'MUTATION_SECRET'
  WHERE combo_id = NEW.combo_id AND position = NEW.position;
END`,
		},
		{
			name: "mutated legacy route", triggerName: "mutate_completed_legacy_route",
			triggerSQL: `CREATE TRIGGER mutate_completed_legacy_route
AFTER INSERT ON llm_route_entries WHEN NEW.position = 1
BEGIN
  UPDATE llm_route_entries SET model_id = 'MUTATION_SECRET' WHERE position = NEW.position;
END`,
		},
		{
			name: "reenabled sibling Account", triggerName: "reenable_completed_sibling_account",
			triggerSQL: `CREATE TRIGGER reenable_completed_sibling_account
AFTER UPDATE OF enabled ON llm_accounts
WHEN NEW.id = 'codex-account' AND NEW.enabled = 1
BEGIN
  UPDATE llm_accounts SET enabled = 1 WHERE id = 'codex-old-live';
END`,
		},
		{
			name: "ignored staged Account enable", triggerName: "ignore_completed_staged_account",
			triggerSQL: `CREATE TRIGGER ignore_completed_staged_account
BEFORE UPDATE OF enabled ON llm_accounts
WHEN NEW.id = 'claude-account' AND NEW.enabled = 1
BEGIN SELECT RAISE(IGNORE); END`,
		},
		{
			name: "mutated Provider metadata", triggerName: "mutate_completed_provider_metadata",
			triggerSQL: `CREATE TRIGGER mutate_completed_provider_metadata
AFTER UPDATE OF enabled ON llm_providers
WHEN NEW.id = 'codex' AND NEW.enabled = 1
BEGIN UPDATE llm_providers SET name = 'MUTATION_SECRET' WHERE id = NEW.id; END`,
		},
		{
			name: "deleted sibling Account", triggerName: "delete_completed_sibling_account",
			triggerSQL: `CREATE TRIGGER delete_completed_sibling_account
AFTER UPDATE OF enabled ON llm_accounts
WHEN NEW.id = 'codex-account' AND NEW.enabled = 1
BEGIN DELETE FROM llm_accounts WHERE id = 'codex-old-live'; END`,
		},
		{
			name: "mutated model metadata", triggerName: "mutate_completed_model_metadata",
			triggerSQL: `CREATE TRIGGER mutate_completed_model_metadata
AFTER INSERT ON llm_combo_members WHEN NEW.position = 1
BEGIN
  UPDATE llm_models SET source = 'discovered'
  WHERE provider_id = NEW.provider_id AND model_id = NEW.model_id;
END`,
		},
		{
			name: "mutated unrelated app metadata", triggerName: "mutate_completed_app_metadata",
			triggerSQL: `CREATE TRIGGER mutate_completed_app_metadata
AFTER UPDATE OF value ON app_meta
WHEN NEW.key = 'llm_route_revision'
BEGIN UPDATE app_meta SET value = 'MUTATION_SECRET' WHERE key = 'agent_display_name'; END`,
		},
		{
			name: "malformed singleton text type", triggerName: "malform_completed_singleton_type",
			triggerSQL: `CREATE TRIGGER malform_completed_singleton_type
AFTER UPDATE OF phase ON app_onboarding_state
WHEN NEW.phase = 'completed'
BEGIN
  UPDATE app_onboarding_state SET provider_kind = CAST(provider_kind AS BLOB) WHERE id = NEW.id;
END`,
		},
		{
			name: "malformed Combo member text type", triggerName: "malform_completed_member_type",
			triggerSQL: `CREATE TRIGGER malform_completed_member_type
AFTER INSERT ON llm_combo_members
WHEN NEW.position = 1
BEGIN
  UPDATE llm_combo_members SET combo_id = CAST(combo_id AS BLOB)
  WHERE combo_id = NEW.combo_id AND position = NEW.position;
END`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, _, input := seedCompleteOnboardingMultiForTask7(t, 221)
			if _, err := st.db.Exec(test.triggerSQL); err != nil {
				t.Fatal(err)
			}
			before := completeOnboardingDigestForTest(t, st)
			_, err := st.CompleteOnboarding(context.Background(), input)
			if !errors.Is(err, ErrOnboardingCommitFailed) {
				t.Fatalf("CompleteOnboarding() projection trigger error = %v; want commit failure", err)
			}
			if strings.Contains(err.Error(), "MUTATION_SECRET") {
				t.Fatalf("durability error leaked mutated value: %v", err)
			}
			if after := completeOnboardingDigestForTest(t, st); after != before {
				t.Fatalf("projection trigger escaped rollback:\nbefore=%s\nafter=%s", before, after)
			}
			if _, err := st.db.Exec(`DROP TRIGGER ` + test.triggerName); err != nil {
				t.Fatal(err)
			}
			if completed, err := st.CompleteOnboarding(context.Background(), input); err != nil ||
				completed.Phase != OnboardingPhaseCompleted {
				t.Fatalf("retry after projection trigger = %+v, %v", completed, err)
			}
		})
	}
}

func TestCompleteOnboardingRejectsSecondProviderDriftMulti(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{name: "Provider kind", sql: `UPDATE llm_providers SET kind='codex' WHERE id='claude-code'`},
		{name: "Account owner", sql: `UPDATE llm_accounts SET provider_id='codex' WHERE id='claude-account'`},
		{name: "Account config", sql: `UPDATE llm_accounts SET config_dir='D:/drifted' WHERE id='claude-account'`},
		{name: "model unavailable", sql: `UPDATE llm_models SET available=0 WHERE provider_id='claude-code' AND model_id='claude-model'`},
		{name: "stage model", sql: `UPDATE app_onboarding_provider_stages SET model_id='other-model' WHERE kind='claude-code'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, _, input := seedCompleteOnboardingMultiForTask7(t, 141)
			if _, err := st.db.Exec(test.sql); err != nil {
				t.Fatal(err)
			}
			before := completeOnboardingDigestForTest(t, st)
			if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingConfigurationChanged) {
				t.Fatalf("CompleteOnboarding() error = %v; want configuration changed", err)
			}
			if after := completeOnboardingDigestForTest(t, st); after != before {
				t.Fatal("rejected second-member drift mutated database")
			}
		})
	}
}

func seedCompleteOnboardingMultiForTask7(
	t *testing.T,
	baseRevision int64,
) (*Store, OnboardingTestRoute, CompleteOnboardingInput) {
	t.Helper()
	st, route := seedOnboardingTestRouteForTask6(t, baseRevision)
	for _, account := range []LLMAccount{
		{ID: "codex-old-live", ProviderID: "codex", Label: "old", ConfigDir: t.TempDir(), Enabled: true},
		{ID: "claude-old-live", ProviderID: "claude-code", Label: "old", ConfigDir: t.TempDir(), Enabled: true},
	} {
		if err := st.CreateLLMAccount(account); err != nil {
			t.Fatal(err)
		}
	}
	insertOnboardingProviderForTest(t, st, "unrelated-provider", "openai")
	insertOnboardingAccountForTest(t, st, "unrelated-account", "unrelated-provider", true, t.TempDir())

	nonceHash := completeOnboardingHashForTest()
	issuedAt := time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)
	saved, err := st.SaveOnboardingTestReceipt(
		context.Background(), route, nonceHash, NewOnboardingTestReceiptPolicy(issuedAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	route.State = saved
	return st, route, CompleteOnboardingInput{
		Revision: saved.Revision, TestNonceHash: nonceHash,
		PersonaFingerprint: route.State.PersonaFingerprint,
		ConfigBindings: []OnboardingConfigBinding{
			{Kind: route.Entries[1].Kind, AccountID: route.Entries[1].AccountID, ConfigDir: route.Entries[1].ConfigDir},
			{Kind: route.Entries[0].Kind, AccountID: route.Entries[0].AccountID, ConfigDir: route.Entries[0].ConfigDir},
		},
		Now: issuedAt.Add(time.Second),
	}
}

func assertTask7ProviderAndAccountsActivated(
	t *testing.T,
	st *Store,
	providerID string,
	stagedAccountID string,
	oldAccountID string,
) {
	t.Helper()
	providers, err := st.LLMProviders()
	if err != nil {
		t.Fatal(err)
	}
	foundProvider := false
	for _, provider := range providers {
		if provider.ID == providerID {
			foundProvider = true
			if !provider.Enabled {
				t.Fatalf("Provider %q remains disabled", providerID)
			}
		}
	}
	if !foundProvider {
		t.Fatalf("Provider %q missing", providerID)
	}
	accounts, err := st.LLMAccounts(providerID)
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[string]bool, len(accounts))
	for _, account := range accounts {
		states[account.ID] = account.Enabled
	}
	if !states[stagedAccountID] || states[oldAccountID] {
		t.Fatalf("Provider %q account enabled states = %+v", providerID, states)
	}
}
