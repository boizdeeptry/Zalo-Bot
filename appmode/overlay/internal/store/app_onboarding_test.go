package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
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

func openV8FixtureWithStages(t *testing.T, stages []OnboardingProviderStage) *Store {
	t.Helper()
	st := openAppStoreForTest(t)
	for _, stage := range stages {
		if _, err := st.db.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, provider_id, account_id, model_id, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			stage.Kind, stage.Status, stage.Position, stage.ProviderID,
			stage.AccountID, stage.ModelID, "2026-08-14T03:00:00Z",
		); err != nil {
			t.Fatalf("insert onboarding Provider stage: %v", err)
		}
	}
	return st
}

func TestOnboardingSnapshotReadsSingletonAndOrdersProviderStages(t *testing.T) {
	stages := []OnboardingProviderStage{
		{
			Kind: "claude-code", Status: "pending", Position: 1,
		},
		{
			Kind: "codex", Status: "ready", Position: 0,
			ProviderID: "codex", AccountID: "codex-account", ModelID: "gpt-5.6-terra",
		},
	}
	st := openV8FixtureWithStages(t, stages)
	if _, err := st.db.Exec(`UPDATE app_onboarding_state SET
phase = 'provider', staged_combo_id = 'combo-staged', restart_in_progress = 1,
revision = 23, updated_at = '2026-08-14T03:00:00Z'
WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	got, err := st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	wantState := OnboardingState{
		Phase: OnboardingPhaseProvider, StagedComboID: "combo-staged",
		RestartInProgress: true, Revision: 23, UpdatedAt: "2026-08-14T03:00:00Z",
	}
	if got.State != wantState {
		t.Fatalf("snapshot state = %+v; want %+v", got.State, wantState)
	}
	wantStages := []OnboardingProviderStage{stages[1], stages[0]}
	if !slices.Equal(got.Stages, wantStages) {
		t.Fatalf("snapshot stages = %+v; want ordered %+v", got.Stages, wantStages)
	}
}

func TestOnboardingSnapshotCannotMixConcurrentCommits(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "snapshot.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	escapedPath := strings.ReplaceAll(url.PathEscape(filepath.ToSlash(dbPath)), "%2F", "/")
	writer, err := sql.Open(
		"sqlite",
		"file:"+escapedPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)",
	)
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })

	oldState := OnboardingState{
		Phase: OnboardingPhaseProvider, StagedComboID: "old-combo",
		Revision: 23, UpdatedAt: "2026-08-14T03:00:00Z",
	}
	oldStages := []OnboardingProviderStage{
		{
			Kind: "codex", Status: "ready", Position: 0,
			ProviderID: "codex", AccountID: "old-codex", ModelID: "old-model",
		},
		{Kind: "claude-code", Status: "pending", Position: 1},
	}
	setOnboardingStateForTest(t, st, oldState)
	for _, stage := range oldStages {
		if _, err := st.db.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, provider_id, account_id, model_id, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			stage.Kind, stage.Status, stage.Position, stage.ProviderID,
			stage.AccountID, stage.ModelID, oldState.UpdatedAt,
		); err != nil {
			t.Fatal(err)
		}
	}

	newState := OnboardingState{
		Phase: OnboardingPhaseProvider, StagedComboID: "new-combo",
		Revision: 24, UpdatedAt: "2026-08-14T04:00:00Z",
	}
	newStages := []OnboardingProviderStage{
		{
			Kind: "claude-code", Status: "ready", Position: 0,
			ProviderID: "claude-code", AccountID: "new-claude", ModelID: "new-model",
		},
		{Kind: "codex", Status: "pending", Position: 1},
	}
	readerAtBarrier := make(chan struct{})
	writerResult := make(chan error, 1)
	go func() {
		<-readerAtBarrier
		tx, err := writer.Begin()
		if err != nil {
			writerResult <- err
			return
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.Exec(`UPDATE app_onboarding_state SET
staged_combo_id = ?, revision = ?, updated_at = ? WHERE id = 1`,
			newState.StagedComboID, newState.Revision, newState.UpdatedAt,
		); err != nil {
			writerResult <- err
			return
		}
		if _, err := tx.Exec(`DELETE FROM app_onboarding_provider_stages`); err != nil {
			writerResult <- err
			return
		}
		for _, stage := range newStages {
			if _, err := tx.Exec(`INSERT INTO app_onboarding_provider_stages(
kind, status, position, provider_id, account_id, model_id, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				stage.Kind, stage.Status, stage.Position, stage.ProviderID,
				stage.AccountID, stage.ModelID, newState.UpdatedAt,
			); err != nil {
				writerResult <- err
				return
			}
		}
		writerResult <- tx.Commit()
	}()

	oldBarrier := onboardingSnapshotAfterStateRead
	var writerErr error
	onboardingSnapshotAfterStateRead = func() {
		close(readerAtBarrier)
		writerErr = <-writerResult
	}
	t.Cleanup(func() { onboardingSnapshotAfterStateRead = oldBarrier })

	got, err := st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if writerErr != nil {
		t.Fatalf("commit concurrent onboarding snapshot: %v", writerErr)
	}
	switch got.State.Revision {
	case oldState.Revision:
		if got.State != oldState || !slices.Equal(got.Stages, oldStages) {
			t.Fatalf("mixed old singleton with stages from another commit: %+v", got)
		}
	case newState.Revision:
		if got.State != newState || !slices.Equal(got.Stages, newStages) {
			t.Fatalf("mixed new singleton with stages from another commit: %+v", got)
		}
	default:
		t.Fatalf("snapshot has unknown revision: %+v", got)
	}

	onboardingSnapshotAfterStateRead = oldBarrier
	fresh, err := st.OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if fresh.State != newState || !slices.Equal(fresh.Stages, newStages) {
		t.Fatalf("fresh snapshot after writer commit = %+v; want new commit", fresh)
	}
}

func TestOnboardingSnapshotMissingSingletonFailsClosed(t *testing.T) {
	st := openAppStoreForTest(t)
	if _, err := st.db.Exec(`DELETE FROM app_onboarding_state WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OnboardingSnapshot(); !errors.Is(err, ErrOnboardingStateMissing) {
		t.Fatalf("OnboardingSnapshot() error = %v; want ErrOnboardingStateMissing", err)
	}
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

func TestAdvanceOnboardingPersonaStoresNameAndAdvancesExactlyOnce(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhasePersona, ProviderKind: "codex", ProviderID: "codex",
		AccountID: "account", ModelID: "model", StagedComboID: "combo",
		TestNonceHash: "old-receipt", TestExpiresAt: "2026-08-11T08:00:00Z",
		Revision: 7,
	})

	got, err := st.AdvanceOnboardingPersona(7, "persona-fingerprint", "An Nhiên")
	if err != nil {
		t.Fatalf("AdvanceOnboardingPersona() = %v", err)
	}
	if got.Phase != OnboardingPhaseTest || got.Revision != 8 ||
		got.PersonaFingerprint != "persona-fingerprint" ||
		got.TestNonceHash != "" || got.TestExpiresAt != "" {
		t.Fatalf("advanced state = %+v", got)
	}
	if name, err := st.AgentDisplayName(); err != nil || name != "An Nhiên" {
		t.Fatalf("AgentDisplayName() = %q, %v", name, err)
	}
}

func TestAdvanceOnboardingPersonaFromTestReplacesFingerprintAndReceiptExactlyOnce(t *testing.T) {
	st := openAppStoreForTest(t)
	if err := st.SetAgentDisplayName("Old"); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseTest, ProviderKind: "codex", ProviderID: "codex",
		AccountID: "account", ModelID: "model", StagedComboID: "combo",
		PersonaFingerprint: "old-fingerprint", TestNonceHash: "old-receipt",
		TestExpiresAt: "2099-08-12T08:00:00Z", Revision: 11,
	})

	got, err := st.AdvanceOnboardingPersonaWithRecovery(11, "new-fingerprint", "New", "recovery-token")
	if err != nil {
		t.Fatalf("AdvanceOnboardingPersonaWithRecovery() = %v", err)
	}
	if got.Phase != OnboardingPhaseTest || got.Revision != 12 ||
		got.PersonaFingerprint != "new-fingerprint" ||
		got.TestNonceHash != "" || got.TestExpiresAt != "" {
		t.Fatalf("Test-to-Test state = %+v", got)
	}
	if name, err := st.AgentDisplayName(); err != nil || name != "New" {
		t.Fatalf("AgentDisplayName() = %q, %v", name, err)
	}
	if committed, err := st.AgentPersonaRecoveryCommitted("recovery-token"); err != nil || !committed {
		t.Fatalf("AgentPersonaRecoveryCommitted() = %t, %v", committed, err)
	}
}

func TestAdvanceOnboardingPersonaFromTestUnchangedConfirmationStillInvalidatesReceipt(t *testing.T) {
	st := openAppStoreForTest(t)
	if err := st.SetAgentDisplayName("Bot"); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "same-fingerprint", TestNonceHash: "old-receipt",
		TestExpiresAt: "2099-08-12T08:00:00Z", Revision: 13,
	})

	got, err := st.AdvanceOnboardingPersona(13, "same-fingerprint", "Bot")
	if err != nil {
		t.Fatalf("AdvanceOnboardingPersona() = %v", err)
	}
	if got.Phase != OnboardingPhaseTest || got.Revision != 14 ||
		got.PersonaFingerprint != "same-fingerprint" ||
		got.TestNonceHash != "" || got.TestExpiresAt != "" {
		t.Fatalf("unchanged Test confirmation = %+v", got)
	}
}

func TestAdvanceOnboardingPersonaAllowsOnlyOneConcurrentTestSourceWinner(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "old-fingerprint", TestNonceHash: "old-receipt",
		TestExpiresAt: "2099-08-12T08:00:00Z", Revision: 17,
	})
	type result struct {
		fingerprint string
		name        string
		err         error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for i, fingerprint := range []string{"winner-a", "winner-b"} {
		name := fmt.Sprintf("Bot %d", i)
		go func(fingerprint, name string) {
			<-start
			_, err := st.AdvanceOnboardingPersona(17, fingerprint, name)
			results <- result{fingerprint: fingerprint, name: name, err: err}
		}(fingerprint, name)
	}
	close(start)
	first, second := <-results, <-results
	winners := []result{}
	losers := []result{}
	for _, got := range []result{first, second} {
		if got.err == nil {
			winners = append(winners, got)
		} else if errors.Is(got.err, ErrOnboardingConflict) {
			losers = append(losers, got)
		} else {
			t.Fatalf("concurrent advance error = %v", got.err)
		}
	}
	if len(winners) != 1 || len(losers) != 1 {
		t.Fatalf("concurrent results = %+v / %+v; want one winner and one conflict", first, second)
	}
	state := mustOnboardingStateForTest(t, st)
	if state.Phase != OnboardingPhaseTest || state.Revision != 18 ||
		state.PersonaFingerprint != winners[0].fingerprint ||
		state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Fatalf("concurrent winner state = %+v; winner=%+v", state, winners[0])
	}
	if name, err := st.AgentDisplayName(); err != nil || name != winners[0].name {
		t.Fatalf("concurrent winner name = %q, %v; want %q", name, err, winners[0].name)
	}
}

func TestAdvanceOnboardingPersonaCASPinsCapturedSourcePhase(t *testing.T) {
	for _, tt := range []struct {
		name, source, drift string
	}{
		{name: "persona cannot drift to test", source: OnboardingPhasePersona, drift: OnboardingPhaseTest},
		{name: "test cannot drift to persona", source: OnboardingPhaseTest, drift: OnboardingPhasePersona},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			if err := st.SetAgentDisplayName("Original"); err != nil {
				t.Fatal(err)
			}
			setOnboardingStateForTest(t, st, OnboardingState{
				Phase: tt.source, ProviderKind: "codex",
				PersonaFingerprint: "old-fingerprint", TestNonceHash: "old-receipt",
				TestExpiresAt: "2099-08-12T08:00:00Z", Revision: 23,
			})
			before := mustOnboardingStateForTest(t, st)
			oldBeforeCAS := onboardingPersonaBeforeCAS
			onboardingPersonaBeforeCAS = func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE app_onboarding_state SET phase = ? WHERE id = 1`, tt.drift)
				return err
			}
			t.Cleanup(func() { onboardingPersonaBeforeCAS = oldBeforeCAS })

			_, err := st.AdvanceOnboardingPersonaWithRecovery(
				23,
				"new-fingerprint",
				"Changed",
				"new-recovery-token",
			)
			if !errors.Is(err, ErrOnboardingConflict) {
				t.Fatalf("AdvanceOnboardingPersonaWithRecovery() error = %v; want %v", err, ErrOnboardingConflict)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("source-phase CAS failure changed state: before=%+v after=%+v", before, after)
			}
			if name, err := st.AgentDisplayName(); err != nil || name != "Original" {
				t.Fatalf("source-phase CAS failure changed name to %q (%v)", name, err)
			}
			if committed, err := st.AgentPersonaRecoveryCommitted("new-recovery-token"); err != nil || committed {
				t.Fatalf("source-phase CAS failure committed token = %t, %v", committed, err)
			}
		})
	}
}

func TestAdvanceOnboardingPersonaRejectsStaleAndWrongPhaseWithoutMetadataMutation(t *testing.T) {
	tests := []struct {
		name     string
		phase    string
		revision int64
		wantErr  error
	}{
		{name: "stale", phase: OnboardingPhasePersona, revision: 8, wantErr: ErrOnboardingConflict},
		{name: "provider phase", phase: OnboardingPhaseProvider, revision: 7, wantErr: ErrOnboardingInvalidPhase},
		{name: "setup phase", phase: OnboardingPhaseSetup, revision: 7, wantErr: ErrOnboardingInvalidPhase},
		{name: "connect phase", phase: OnboardingPhaseConnect, revision: 7, wantErr: ErrOnboardingInvalidPhase},
		{name: "completed phase", phase: OnboardingPhaseCompleted, revision: 7, wantErr: ErrOnboardingInvalidPhase},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			if err := st.SetAgentDisplayName("Original"); err != nil {
				t.Fatal(err)
			}
			setOnboardingStateForTest(t, st, OnboardingState{
				Phase: tt.phase, ProviderKind: "codex", Revision: 7,
			})
			before := mustOnboardingStateForTest(t, st)

			if _, err := st.AdvanceOnboardingPersona(tt.revision, "new-fingerprint", "Changed"); !errors.Is(err, tt.wantErr) {
				t.Fatalf("AdvanceOnboardingPersona() error = %v; want %v", err, tt.wantErr)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("failed advance changed state: before=%+v after=%+v", before, after)
			}
			if name, err := st.AgentDisplayName(); err != nil || name != "Original" {
				t.Fatalf("failed advance changed display name to %q (%v)", name, err)
			}
		})
	}
}

func TestAdvanceOnboardingPersonaCASFailureRollsBackDisplayName(t *testing.T) {
	st := openAppStoreForTest(t)
	if err := st.SetAgentDisplayName("Original"); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhasePersona, ProviderKind: "codex", Revision: 4,
	})
	before := mustOnboardingStateForTest(t, st)
	if _, err := st.db.Exec(`CREATE TRIGGER fail_persona_advance
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'injected persona CAS failure'); END`); err != nil {
		t.Fatal(err)
	}

	if _, err := st.AdvanceOnboardingPersona(4, "fingerprint", "Changed"); err == nil {
		t.Fatal("AdvanceOnboardingPersona() error = nil; want injected failure")
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("failed advance changed state: before=%+v after=%+v", before, after)
	}
	if name, err := st.AgentDisplayName(); err != nil || name != "Original" {
		t.Fatalf("failed advance changed display name to %q (%v)", name, err)
	}
}

func TestInvalidateOnboardingPersonaReturnsTestFlowToPersona(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "fingerprint", TestNonceHash: "receipt",
		TestExpiresAt: "2026-08-11T08:00:00Z", Revision: 11,
	})

	got, err := st.InvalidateOnboardingPersona()
	if err != nil {
		t.Fatalf("InvalidateOnboardingPersona() = %v", err)
	}
	if got.Phase != OnboardingPhasePersona || got.Revision != 12 ||
		got.PersonaFingerprint != "" || got.TestNonceHash != "" || got.TestExpiresAt != "" {
		t.Fatalf("invalidated state = %+v", got)
	}
}

func TestInvalidateOnboardingPersonaIsNoopOutsidePersonaFlow(t *testing.T) {
	st := openAppStoreForTest(t)
	before := mustOnboardingStateForTest(t, st)

	got, err := st.InvalidateOnboardingPersona()
	if err != nil {
		t.Fatalf("InvalidateOnboardingPersona() = %v", err)
	}
	if got != before {
		t.Fatalf("inactive invalidation changed state: before=%+v after=%+v", before, got)
	}
}

func TestInvalidateOnboardingPersonaClearsCompletedReceiptWithoutReopeningFlow(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion:   CurrentOnboardingVersion,
		Phase:              OnboardingPhaseCompleted,
		ProviderKind:       "codex",
		PersonaFingerprint: "fingerprint",
		TestNonceHash:      "receipt",
		TestExpiresAt:      "2026-08-11T08:00:00Z",
		Revision:           15,
	})

	got, err := st.InvalidateOnboardingPersona()
	if err != nil {
		t.Fatalf("InvalidateOnboardingPersona() = %v", err)
	}
	if got.Phase != OnboardingPhaseCompleted || got.CompletedVersion != CurrentOnboardingVersion ||
		got.Revision != 16 || got.PersonaFingerprint != "" ||
		got.TestNonceHash != "" || got.TestExpiresAt != "" {
		t.Fatalf("completed invalidation state = %+v", got)
	}
}

func TestUpdateAgentPersonaStoresNameAndInvalidatesReceiptInOneTransaction(t *testing.T) {
	st := openAppStoreForTest(t)
	if err := st.SetAgentDisplayName("Old"); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "fingerprint", TestNonceHash: "receipt",
		TestExpiresAt: "2026-08-11T08:00:00Z", Revision: 7,
	})

	got, err := st.UpdateAgentPersona("New", "recovery-token")
	if err != nil {
		t.Fatalf("UpdateAgentPersona() = %v", err)
	}
	if got.Phase != OnboardingPhasePersona || got.Revision != 8 ||
		got.PersonaFingerprint != "" || got.TestNonceHash != "" || got.TestExpiresAt != "" {
		t.Fatalf("updated state = %+v", got)
	}
	if name, err := st.AgentDisplayName(); err != nil || name != "New" {
		t.Fatalf("AgentDisplayName() = %q, %v", name, err)
	}
	if committed, err := st.AgentPersonaRecoveryCommitted("recovery-token"); err != nil || !committed {
		t.Fatalf("AgentPersonaRecoveryCommitted() = %t, %v", committed, err)
	}
}

func TestUpdateAgentPersonaPreservesCompletedState(t *testing.T) {
	st := openAppStoreForTest(t)
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion: CurrentOnboardingVersion,
		Phase:            OnboardingPhaseCompleted,
		Revision:         12,
	})
	before := mustOnboardingStateForTest(t, st)

	got, err := st.UpdateAgentPersona("New", "completed-token")
	if err != nil {
		t.Fatalf("UpdateAgentPersona() = %v", err)
	}
	if got != before {
		t.Fatalf("completed state changed: before=%+v after=%+v", before, got)
	}
	if name, err := st.AgentDisplayName(); err != nil || name != "New" {
		t.Fatalf("AgentDisplayName() = %q, %v", name, err)
	}
	if committed, err := st.AgentPersonaRecoveryCommitted("completed-token"); err != nil || !committed {
		t.Fatalf("AgentPersonaRecoveryCommitted() = %t, %v", committed, err)
	}
}

func TestUpdateAgentPersonaFailureRollsBackNameTokenAndState(t *testing.T) {
	st := openAppStoreForTest(t)
	if err := st.SetAgentDisplayName("Old"); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseTest, ProviderKind: "codex",
		PersonaFingerprint: "fingerprint", TestNonceHash: "receipt", Revision: 4,
	})
	before := mustOnboardingStateForTest(t, st)
	if _, err := st.db.Exec(`CREATE TRIGGER fail_agent_edit_state
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'injected agent edit failure'); END`); err != nil {
		t.Fatal(err)
	}

	if _, err := st.UpdateAgentPersona("New", "failed-token"); err == nil {
		t.Fatal("UpdateAgentPersona() error = nil; want injected failure")
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("failed update changed state: before=%+v after=%+v", before, after)
	}
	if name, err := st.AgentDisplayName(); err != nil || name != "Old" {
		t.Fatalf("failed update changed name to %q (%v)", name, err)
	}
	if committed, err := st.AgentPersonaRecoveryCommitted("failed-token"); err != nil || committed {
		t.Fatalf("failed token committed = %t, %v", committed, err)
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

func TestBindOnboardingAccountAcceptsExactMatchingDisabledProvider(t *testing.T) {
	st := openAppStoreForTest(t)
	if _, err := st.db.Exec(`INSERT INTO llm_providers(id, name, kind, enabled)
VALUES ('codex', 'Codex', 'codex', 0)`); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseConnect, ProviderKind: "codex", Revision: 11,
	})
	before := mustOnboardingStateForTest(t, st)
	account := validOnboardingAccountForTest()

	got, err := st.BindOnboardingAccount(before.Revision, "codex", account)
	if err != nil {
		t.Fatalf("BindOnboardingAccount() = %v", err)
	}
	if got.Phase != OnboardingPhaseSetup || got.ProviderID != "codex" ||
		got.AccountID != account.ID || got.Revision != before.Revision+1 {
		t.Fatalf("bound state = %+v", got)
	}
	accounts, err := st.LLMAccounts("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != account.ID || accounts[0].Enabled {
		t.Fatalf("staged accounts = %+v; want one disabled Account", accounts)
	}
	var providerEnabled int
	if err := st.db.QueryRow(`SELECT enabled FROM llm_providers WHERE id = 'codex'`).Scan(&providerEnabled); err != nil {
		t.Fatal(err)
	}
	if providerEnabled != 0 {
		t.Fatalf("bind changed Provider enabled=%d; want 0", providerEnabled)
	}
}

func TestEnsureOnboardingProviderForKindCreatesDisabledAndPreservesExistingState(t *testing.T) {
	t.Run("fresh provider is disabled", func(t *testing.T) {
		st := openAppStoreForTest(t)
		if err := st.EnsureOnboardingProviderForKind("codex"); err != nil {
			t.Fatalf("EnsureOnboardingProviderForKind() = %v", err)
		}
		var kind string
		var enabled int
		if err := st.db.QueryRow(
			`SELECT kind, enabled FROM llm_providers WHERE id = 'codex'`,
		).Scan(&kind, &enabled); err != nil {
			t.Fatal(err)
		}
		if kind != "codex" || enabled != 0 {
			t.Fatalf("Provider kind=%q enabled=%d; want codex/0", kind, enabled)
		}
	})

	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_enabled_%t_is_unchanged", enabled), func(t *testing.T) {
			st := openAppStoreForTest(t)
			if _, err := st.db.Exec(`INSERT INTO llm_providers(id, name, kind, enabled)
VALUES ('codex', 'Existing', 'codex', ?)`, boolInt(enabled)); err != nil {
				t.Fatal(err)
			}
			if err := st.EnsureOnboardingProviderForKind("codex"); err != nil {
				t.Fatalf("EnsureOnboardingProviderForKind() = %v", err)
			}
			var got int
			if err := st.db.QueryRow(
				`SELECT enabled FROM llm_providers WHERE id = 'codex'`,
			).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != boolInt(enabled) {
				t.Fatalf("existing Provider enabled=%d; want %d", got, boolInt(enabled))
			}
		})
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

func TestStageOnboardingSetupStagesDescriptorWithoutLiveRoutingMutation(t *testing.T) {
	st := openAppStoreForTest(t)
	insertOnboardingProviderForTest(t, st, "live-provider", "openai")
	if err := st.AddLLMModel(LLMModel{
		ProviderID: "live-provider", ModelID: "live-model", Name: "Live model",
		Source: LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	combo, err := st.CreateLLMCombo("Live combo", "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceLLMComboMembers(combo.ID, combo.Revision, "fallback", []LLMRouteEntry{{
		ProviderID: "live-provider", ModelID: "live-model", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveLLMCombo(combo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO llm_route_entries(position, provider_id, model_id, enabled)
VALUES (0, 'live-provider', 'live-model', 1)`); err != nil {
		t.Fatal(err)
	}

	seedStageOnboardingSetupForTest(t, st, "codex", "staged-account", "gpt-5.6-terra", true, 7)
	beforeLive := onboardingLiveRoutingBytesForTest(t, st)

	got, err := st.StageOnboardingSetup(7, "staged-account", "gpt-5.6-terra")
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != OnboardingPhasePersona || got.Revision != 8 || got.ProviderKind != "codex" ||
		got.ProviderID != "codex" || got.AccountID != "staged-account" ||
		got.ModelID != "gpt-5.6-terra" || got.ComboName != "Mặc định · Codex" {
		t.Fatalf("StageOnboardingSetup() = %+v", got)
	}
	if _, err := uuid.Parse(got.StagedComboID); err != nil {
		t.Fatalf("staged combo id %q is not a UUID: %v", got.StagedComboID, err)
	}
	if afterLive := onboardingLiveRoutingBytesForTest(t, st); afterLive != beforeLive {
		t.Fatalf("StageOnboardingSetup changed live routing rows:\nbefore=%q\nafter=%q", beforeLive, afterLive)
	}
}

func TestStageOnboardingSetupLostResponseRetryReturnsExactDescriptor(t *testing.T) {
	st := openAppStoreForTest(t)
	seedStageOnboardingSetupForTest(t, st, "claude-code", "staged-account", "sonnet", true, 11)

	first, err := st.StageOnboardingSetup(11, "staged-account", "sonnet")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := st.StageOnboardingSetup(11, "staged-account", "sonnet")
	if err != nil {
		t.Fatal(err)
	}
	if retry != first {
		t.Fatalf("retry = %+v; want exact original %+v", retry, first)
	}
	if state := mustOnboardingStateForTest(t, st); state.Revision != 12 {
		t.Fatalf("retry revision = %d; want 12", state.Revision)
	}

	before := mustOnboardingStateForTest(t, st)
	if _, err := st.StageOnboardingSetup(11, "different-account", "sonnet"); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("mismatched retry error = %v; want ErrOnboardingConflict", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("mismatched retry changed state: before=%+v after=%+v", before, after)
	}
}

func TestStageOnboardingSetupRejectsInvalidStateWithoutMutation(t *testing.T) {
	tests := []struct {
		name             string
		phase            string
		expectedRevision int64
		requestAccount   string
		accountEnabled   bool
		modelAvailable   bool
		wantErr          error
	}{
		{name: "wrong account", phase: OnboardingPhaseSetup, expectedRevision: 5, requestAccount: "other", modelAvailable: true, wantErr: ErrOnboardingInvalidStagingOwnership},
		{name: "enabled account", phase: OnboardingPhaseSetup, expectedRevision: 5, requestAccount: "staged-account", accountEnabled: true, modelAvailable: true, wantErr: ErrOnboardingInvalidStagingOwnership},
		{name: "stale revision", phase: OnboardingPhaseSetup, expectedRevision: 4, requestAccount: "staged-account", modelAvailable: true, wantErr: ErrOnboardingConflict},
		{name: "wrong phase", phase: OnboardingPhaseConnect, expectedRevision: 5, requestAccount: "staged-account", modelAvailable: true, wantErr: ErrOnboardingInvalidPhase},
		{name: "unavailable model", phase: OnboardingPhaseSetup, expectedRevision: 5, requestAccount: "staged-account", wantErr: ErrOnboardingModelUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			seedStageOnboardingSetupForTest(t, st, "codex", "staged-account", "gpt-5.6-terra", tt.modelAvailable, 5)
			if tt.accountEnabled {
				if _, err := st.db.Exec(`UPDATE llm_accounts SET enabled = 1 WHERE id = 'staged-account'`); err != nil {
					t.Fatal(err)
				}
			}
			state := mustOnboardingStateForTest(t, st)
			state.Phase = tt.phase
			setOnboardingStateForTest(t, st, state)
			beforeState := mustOnboardingStateForTest(t, st)
			beforeLive := onboardingLiveRoutingBytesForTest(t, st)

			if _, err := st.StageOnboardingSetup(tt.expectedRevision, tt.requestAccount, "gpt-5.6-terra"); !errors.Is(err, tt.wantErr) {
				t.Fatalf("StageOnboardingSetup error = %v; want %v", err, tt.wantErr)
			}
			if after := mustOnboardingStateForTest(t, st); after != beforeState {
				t.Fatalf("rejected setup changed state: before=%+v after=%+v", beforeState, after)
			}
			if afterLive := onboardingLiveRoutingBytesForTest(t, st); afterLive != beforeLive {
				t.Fatalf("rejected setup changed live routing: before=%q after=%q", beforeLive, afterLive)
			}
		})
	}
}

func seedStageOnboardingSetupForTest(
	t *testing.T,
	st *Store,
	kind, accountID, modelID string,
	modelAvailable bool,
	revision int64,
) {
	t.Helper()
	if kind != "claude-code" {
		insertOnboardingProviderForTest(t, st, kind, kind)
	}
	insertOnboardingAccountForTest(t, st, accountID, kind, false, "D:/onboarding/"+accountID)
	if err := st.AddLLMModel(LLMModel{
		ProviderID: kind, ModelID: modelID, Name: modelID,
		Source: LLMModelManual, Available: modelAvailable,
	}); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseSetup, ProviderKind: kind, ProviderID: kind,
		AccountID: accountID, Revision: revision,
	})
}

func onboardingLiveRoutingBytesForTest(t *testing.T, st *Store) string {
	t.Helper()
	queries := []string{
		`SELECT id, name, type, active, revision FROM llm_combos ORDER BY id`,
		`SELECT combo_id, position, provider_id, model_id, enabled FROM llm_combo_members ORDER BY combo_id, position`,
		`SELECT position, provider_id, model_id, enabled FROM llm_route_entries ORDER BY position`,
	}
	var snapshot strings.Builder
	for _, query := range queries {
		rows, err := st.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		fmt.Fprintf(&snapshot, "%s|", query)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			fmt.Fprintf(&snapshot, "%#v;", values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot.String()
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

func TestSaveOnboardingTestReceiptStoresHashAndExpiresAtomically(t *testing.T) {
	st := openAppStoreForTest(t)
	seedOnboardingTestReceiptForTest(t, st, 17)
	beforeRouting := onboardingLiveRoutingBytesForTest(t, st)
	issuedAt := time.Date(2099, 8, 11, 8, 0, 0, 0, time.UTC)
	expiresAt := issuedAt.Add(10 * time.Minute)
	hash := strings.Repeat("ab", sha256.Size)

	got, err := st.SaveOnboardingTestReceipt(
		context.Background(),
		17,
		"persona-fingerprint",
		hash,
		OnboardingTestReceiptPolicy{IssuedAt: issuedAt, ExpiresAt: expiresAt},
	)
	if err != nil {
		t.Fatalf("SaveOnboardingTestReceipt() = %v", err)
	}
	if got.Phase != OnboardingPhaseTest || got.Revision != 18 ||
		got.PersonaFingerprint != "persona-fingerprint" || got.TestNonceHash != hash ||
		got.TestExpiresAt != expiresAt.Format(time.RFC3339Nano) {
		t.Fatalf("saved receipt state = %+v", got)
	}
	if afterRouting := onboardingLiveRoutingBytesForTest(t, st); afterRouting != beforeRouting {
		t.Fatalf("receipt changed live routing: before=%q after=%q", beforeRouting, afterRouting)
	}
}

func TestSaveOnboardingTestReceiptRejectsInvalidStateAndInputWithoutMutation(t *testing.T) {
	validHash := strings.Repeat("ab", sha256.Size)
	validIssuedAt := time.Date(2099, 8, 11, 8, 0, 0, 0, time.UTC)
	validPolicy := OnboardingTestReceiptPolicy{
		IssuedAt:  validIssuedAt,
		ExpiresAt: validIssuedAt.Add(10 * time.Minute),
	}
	tests := []struct {
		name        string
		mutate      func(*Store, *OnboardingState)
		revision    int64
		fingerprint string
		hash        string
		policy      OnboardingTestReceiptPolicy
		wantErr     error
	}{
		{name: "stale revision", revision: 16, fingerprint: "persona-fingerprint", hash: validHash, policy: validPolicy, wantErr: ErrOnboardingConflict},
		{name: "wrong phase", revision: 17, fingerprint: "persona-fingerprint", hash: validHash, policy: validPolicy, mutate: func(_ *Store, state *OnboardingState) { state.Phase = OnboardingPhasePersona }, wantErr: ErrOnboardingInvalidPhase},
		{name: "fingerprint changed", revision: 17, fingerprint: "different", hash: validHash, policy: validPolicy, wantErr: ErrOnboardingPersonaMismatch},
		{name: "missing combo", revision: 17, fingerprint: "persona-fingerprint", hash: validHash, policy: validPolicy, mutate: func(_ *Store, state *OnboardingState) { state.StagedComboID = "" }, wantErr: ErrOnboardingInvalidStagingOwnership},
		{name: "enabled account", revision: 17, fingerprint: "persona-fingerprint", hash: validHash, policy: validPolicy, mutate: func(st *Store, _ *OnboardingState) {
			_, _ = st.db.Exec(`UPDATE llm_accounts SET enabled = 1 WHERE id = 'staged-account'`)
		}, wantErr: ErrOnboardingInvalidStagingOwnership},
		{name: "unavailable model", revision: 17, fingerprint: "persona-fingerprint", hash: validHash, policy: validPolicy, mutate: func(st *Store, _ *OnboardingState) {
			_, _ = st.db.Exec(`UPDATE llm_models SET available = 0 WHERE provider_id = 'codex'`)
		}, wantErr: ErrOnboardingModelUnavailable},
		{name: "bad hash", revision: 17, fingerprint: "persona-fingerprint", hash: "not-a-sha256", policy: validPolicy, wantErr: ErrOnboardingInvalidTestReceipt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			seedOnboardingTestReceiptForTest(t, st, 17)
			state := mustOnboardingStateForTest(t, st)
			if tt.mutate != nil {
				tt.mutate(st, &state)
				setOnboardingStateForTest(t, st, state)
			}
			before := mustOnboardingStateForTest(t, st)

			if _, err := st.SaveOnboardingTestReceipt(context.Background(), tt.revision, tt.fingerprint, tt.hash, tt.policy); !errors.Is(err, tt.wantErr) {
				t.Fatalf("SaveOnboardingTestReceipt() error = %v; want %v", err, tt.wantErr)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("rejected receipt changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestSaveOnboardingTestReceiptRejectsInvalidExpiryPolicyWithoutMutation(t *testing.T) {
	issuedAt := time.Date(2099, 8, 11, 8, 0, 0, 123, time.UTC)
	tests := []struct {
		name   string
		policy OnboardingTestReceiptPolicy
	}{
		{name: "zero issued at", policy: OnboardingTestReceiptPolicy{ExpiresAt: issuedAt.Add(10 * time.Minute)}},
		{name: "nonsensical issued at", policy: OnboardingTestReceiptPolicy{IssuedAt: time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC), ExpiresAt: time.Date(1, 1, 1, 0, 10, 0, 1, time.UTC)}},
		{name: "non UTC issued at", policy: OnboardingTestReceiptPolicy{IssuedAt: issuedAt.In(time.FixedZone("UTC+7", 7*60*60)), ExpiresAt: issuedAt.Add(10 * time.Minute)}},
		{name: "non UTC expiry", policy: OnboardingTestReceiptPolicy{IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(10 * time.Minute).In(time.FixedZone("UTC+7", 7*60*60))}},
		{name: "already expired", policy: OnboardingTestReceiptPolicy{IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(-time.Nanosecond)}},
		{name: "less than ten minutes", policy: OnboardingTestReceiptPolicy{IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(10*time.Minute - time.Nanosecond)}},
		{name: "more than ten minutes", policy: OnboardingTestReceiptPolicy{IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(10*time.Minute + time.Nanosecond)}},
		{name: "arbitrary expiry", policy: OnboardingTestReceiptPolicy{IssuedAt: issuedAt, ExpiresAt: time.Date(2199, 1, 1, 0, 0, 0, 0, time.UTC)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			seedOnboardingTestReceiptForTest(t, st, 17)
			before := mustOnboardingStateForTest(t, st)

			_, err := st.SaveOnboardingTestReceipt(
				context.Background(),
				17,
				"persona-fingerprint",
				strings.Repeat("ab", sha256.Size),
				tt.policy,
			)
			if !errors.Is(err, ErrOnboardingInvalidTestReceipt) {
				t.Fatalf("SaveOnboardingTestReceipt() error = %v; want %v", err, ErrOnboardingInvalidTestReceipt)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("invalid policy changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestSaveOnboardingTestReceiptCancellationBeforeCommitRollsBack(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "client cancellation",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
			wantErr: context.Canceled,
		},
		{
			name: "hard deadline",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			seedOnboardingTestReceiptForTest(t, st, 17)
			before := mustOnboardingStateForTest(t, st)
			issuedAt := time.Date(2099, 8, 11, 8, 0, 0, 0, time.UTC)
			ctx, cancel := tt.context()
			defer cancel()
			oldBeforeCommit := onboardingTestReceiptBeforeCommit
			onboardingTestReceiptBeforeCommit = func(ctx context.Context) {
				if errors.Is(tt.wantErr, context.Canceled) {
					cancel()
				}
				<-ctx.Done()
			}
			t.Cleanup(func() { onboardingTestReceiptBeforeCommit = oldBeforeCommit })

			_, err := st.SaveOnboardingTestReceipt(
				ctx,
				17,
				"persona-fingerprint",
				strings.Repeat("ab", sha256.Size),
				OnboardingTestReceiptPolicy{IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(10 * time.Minute)},
			)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("SaveOnboardingTestReceipt() error = %v; want %v", err, tt.wantErr)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("canceled pre-commit receipt changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestClearOnboardingTestReceiptClearsOnlyExactCommittedReceipt(t *testing.T) {
	st := openAppStoreForTest(t)
	seedOnboardingTestReceiptForTest(t, st, 17)
	issuedAt := time.Date(2099, 8, 11, 8, 0, 0, 0, time.UTC)
	policy := NewOnboardingTestReceiptPolicy(issuedAt)
	hash := strings.Repeat("ab", sha256.Size)
	saved, err := st.SaveOnboardingTestReceipt(
		context.Background(), 17, "persona-fingerprint", hash, policy,
	)
	if err != nil {
		t.Fatalf("SaveOnboardingTestReceipt() = %v", err)
	}

	cleared, err := st.ClearOnboardingTestReceipt(
		context.Background(), saved.Revision, "persona-fingerprint", hash, policy,
	)
	if err != nil {
		t.Fatalf("ClearOnboardingTestReceipt() = %v", err)
	}
	if cleared.Revision != saved.Revision+1 || cleared.TestNonceHash != "" || cleared.TestExpiresAt != "" {
		t.Fatalf("cleared state = %+v; want monotonic revision and empty receipt", cleared)
	}
}

func TestClearOnboardingTestReceiptMismatchNeverClearsNewerReceipt(t *testing.T) {
	tests := []struct {
		name        string
		revision    func(OnboardingState) int64
		fingerprint string
		hash        string
		policy      func(OnboardingTestReceiptPolicy) OnboardingTestReceiptPolicy
	}{
		{name: "stale revision", revision: func(saved OnboardingState) int64 { return saved.Revision - 1 }, fingerprint: "persona-fingerprint", hash: strings.Repeat("ab", sha256.Size)},
		{name: "different fingerprint", revision: func(saved OnboardingState) int64 { return saved.Revision }, fingerprint: "other", hash: strings.Repeat("ab", sha256.Size)},
		{name: "different hash", revision: func(saved OnboardingState) int64 { return saved.Revision }, fingerprint: "persona-fingerprint", hash: strings.Repeat("cd", sha256.Size)},
		{name: "different expiry", revision: func(saved OnboardingState) int64 { return saved.Revision }, fingerprint: "persona-fingerprint", hash: strings.Repeat("ab", sha256.Size), policy: func(policy OnboardingTestReceiptPolicy) OnboardingTestReceiptPolicy {
			return NewOnboardingTestReceiptPolicy(policy.IssuedAt.Add(time.Minute))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			seedOnboardingTestReceiptForTest(t, st, 17)
			issuedAt := time.Date(2099, 8, 11, 8, 0, 0, 0, time.UTC)
			policy := NewOnboardingTestReceiptPolicy(issuedAt)
			hash := strings.Repeat("ab", sha256.Size)
			saved, err := st.SaveOnboardingTestReceipt(
				context.Background(), 17, "persona-fingerprint", hash, policy,
			)
			if err != nil {
				t.Fatalf("SaveOnboardingTestReceipt() = %v", err)
			}
			before := mustOnboardingStateForTest(t, st)
			matchPolicy := policy
			if tt.policy != nil {
				matchPolicy = tt.policy(policy)
			}

			_, err = st.ClearOnboardingTestReceipt(
				context.Background(), tt.revision(saved), tt.fingerprint, tt.hash, matchPolicy,
			)
			if !errors.Is(err, ErrOnboardingConflict) {
				t.Fatalf("ClearOnboardingTestReceipt() error = %v; want %v", err, ErrOnboardingConflict)
			}
			if after := mustOnboardingStateForTest(t, st); after != before {
				t.Fatalf("mismatched compensation changed receipt: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestSaveOnboardingTestReceiptCASFailureRollsBack(t *testing.T) {
	st := openAppStoreForTest(t)
	seedOnboardingTestReceiptForTest(t, st, 8)
	before := mustOnboardingStateForTest(t, st)
	if _, err := st.db.Exec(`CREATE TRIGGER fail_onboarding_test_receipt
BEFORE UPDATE ON app_onboarding_state
BEGIN SELECT RAISE(ABORT, 'injected receipt CAS failure'); END`); err != nil {
		t.Fatal(err)
	}

	_, err := st.SaveOnboardingTestReceipt(
		context.Background(),
		8,
		"persona-fingerprint",
		strings.Repeat("cd", sha256.Size),
		OnboardingTestReceiptPolicy{
			IssuedAt:  time.Date(2099, 8, 11, 8, 0, 0, 0, time.UTC),
			ExpiresAt: time.Date(2099, 8, 11, 8, 10, 0, 0, time.UTC),
		},
	)
	if err == nil {
		t.Fatal("SaveOnboardingTestReceipt() error = nil; want injected failure")
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("failed receipt changed state: before=%+v after=%+v", before, after)
	}
}

func TestSaveOnboardingTestReceiptDoesNotMaskModelReadFailure(t *testing.T) {
	st := openAppStoreForTest(t)
	seedOnboardingTestReceiptForTest(t, st, 8)
	before := mustOnboardingStateForTest(t, st)
	if _, err := st.db.Exec(`DROP TABLE llm_models`); err != nil {
		t.Fatal(err)
	}

	_, err := st.SaveOnboardingTestReceipt(
		context.Background(),
		8,
		"persona-fingerprint",
		strings.Repeat("ef", sha256.Size),
		OnboardingTestReceiptPolicy{
			IssuedAt:  time.Date(2099, 8, 11, 8, 0, 0, 0, time.UTC),
			ExpiresAt: time.Date(2099, 8, 11, 8, 10, 0, 0, time.UTC),
		},
	)
	if err == nil || errors.Is(err, ErrOnboardingModelUnavailable) {
		t.Fatalf("SaveOnboardingTestReceipt() error = %v; want unmasked Store read failure", err)
	}
	if after := mustOnboardingStateForTest(t, st); after != before {
		t.Fatalf("model read failure changed state: before=%+v after=%+v", before, after)
	}
}

func seedOnboardingTestReceiptForTest(t *testing.T, st *Store, revision int64) {
	t.Helper()
	insertOnboardingProviderForTest(t, st, "codex", "codex")
	insertOnboardingAccountForTest(t, st, "staged-account", "codex", false, "D:/onboarding/staged-account")
	if err := st.AddLLMModel(LLMModel{
		ProviderID: "codex", ModelID: "gpt-5.6-terra", Name: "Terra",
		Source: LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentDisplayName("Bé Mi"); err != nil {
		t.Fatal(err)
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		Phase: OnboardingPhaseTest, ProviderKind: "codex", ProviderID: "codex",
		AccountID: "staged-account", ModelID: "gpt-5.6-terra", StagedComboID: "staged-combo",
		PersonaFingerprint: "persona-fingerprint", Revision: revision,
	})
}

const onboardingCompletionComboIDForTest = "5f9967c7-93cf-4ac8-9da8-f6a7c1af8801"

func TestCompleteOnboardingActivatesVerifiedReceiptAtomically(t *testing.T) {
	st := openAppStoreForTest(t)
	now := seedCompleteOnboardingForTest(t, st, false, 51)
	beforeRevision := onboardingRouteRevisionForTest(t, st)

	got, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
		Revision: 51, TestNonceHash: completeOnboardingHashForTest(),
		PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account",
		Now: now,
	})
	if err != nil {
		t.Fatalf("CompleteOnboarding() = %v", err)
	}
	if got.Phase != OnboardingPhaseCompleted || got.CompletedVersion != CurrentOnboardingVersion ||
		got.RestartInProgress || got.Revision != 52 || got.ProviderKind != "codex" ||
		got.ProviderID != "codex" || got.AccountID != "staged-account" ||
		got.ModelID != "gpt-5.6-terra" || got.StagedComboID != "" ||
		got.PersonaFingerprint != "" || got.TestNonceHash != "" || got.TestExpiresAt != "" {
		t.Fatalf("completed state = %+v", got)
	}

	providers, err := st.LLMProviders()
	if err != nil {
		t.Fatal(err)
	}
	providerEnabled := map[string]bool{}
	for _, provider := range providers {
		providerEnabled[provider.ID] = provider.Enabled
	}
	if !providerEnabled["codex"] || !providerEnabled["other-provider"] {
		t.Fatalf("provider enablement = %#v; selected and unrelated providers must be preserved/enabled", providerEnabled)
	}
	accounts, err := st.LLMAccounts("codex")
	if err != nil {
		t.Fatal(err)
	}
	accountEnabled := map[string]bool{}
	for _, account := range accounts {
		accountEnabled[account.ID] = account.Enabled
	}
	if !accountEnabled["staged-account"] || accountEnabled["old-live"] || accountEnabled["old-disabled"] || len(accountEnabled) != 3 {
		t.Fatalf("same-provider accounts = %#v", accountEnabled)
	}
	otherAccounts, err := st.LLMAccounts("other-provider")
	if err != nil {
		t.Fatal(err)
	}
	if len(otherAccounts) != 1 || otherAccounts[0].ID != "other-live" || !otherAccounts[0].Enabled {
		t.Fatalf("other-provider accounts changed: %+v", otherAccounts)
	}
	combos, err := st.LLMCombos()
	if err != nil {
		t.Fatal(err)
	}
	wantRouteRevision := beforeRevision + 1
	if len(combos) != 1 || combos[0].ID != onboardingCompletionComboIDForTest ||
		combos[0].Name != "Mặc định · Codex" || combos[0].Type != "fallback" ||
		!combos[0].Active || combos[0].Revision != wantRouteRevision || len(combos[0].Members) != 1 {
		t.Fatalf("completed combos = %+v", combos)
	}
	wantMember := LLMRouteEntry{Position: 0, ProviderID: "codex", ModelID: "gpt-5.6-terra", Enabled: true}
	if combos[0].Members[0] != wantMember {
		t.Fatalf("completed member = %+v; want %+v", combos[0].Members[0], wantMember)
	}
	route, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if route.ComboID != onboardingCompletionComboIDForTest || route.Type != "fallback" ||
		route.Revision != wantRouteRevision || len(route.Entries) != 1 || route.Entries[0] != wantMember {
		t.Fatalf("completed route = %+v", route)
	}
	if projection := onboardingLegacyRouteForTest(t, st); projection != "0:codex:gpt-5.6-terra:1" {
		t.Fatalf("legacy route projection = %q", projection)
	}
	if revision := onboardingRouteRevisionForTest(t, st); revision != wantRouteRevision {
		t.Fatalf("legacy route revision = %d; want %d", revision, wantRouteRevision)
	}

	if _, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
		Revision: 51, TestNonceHash: completeOnboardingHashForTest(),
		PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
	}); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("one-time retry error = %v; want ErrOnboardingConflict", err)
	}
}

func TestCompleteOnboardingRouteRevisionCannotABAStaleDraft(t *testing.T) {
	st := openAppStoreForTest(t)
	now := seedCompleteOnboardingForTest(t, st, false, 53)
	oldRoute, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	staleDraft := []LLMRouteEntry{{
		ProviderID: "other-provider", ModelID: "old-model", Enabled: true,
	}}
	legacyRevision := onboardingRouteRevisionForTest(t, st)

	if _, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
		Revision: 53, TestNonceHash: completeOnboardingHashForTest(),
		PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	wantRevision := legacyRevision + 1
	if completed.Revision != wantRevision || completed.Revision <= oldRoute.Revision ||
		onboardingRouteRevisionForTest(t, st) != wantRevision {
		t.Fatalf("route revisions old=%d legacy=%d completed=%d; want completed=%d",
			oldRoute.Revision, legacyRevision, completed.Revision, wantRevision)
	}
	beforeStaleWrite := completeOnboardingDigestForTest(t, st)
	if _, err := st.ReplaceLLMRoute(oldRoute.Revision, staleDraft); !errors.Is(err, ErrLLMRouteConflict) {
		t.Fatalf("ReplaceLLMRoute(stale revision %d) = %v; want ErrLLMRouteConflict", oldRoute.Revision, err)
	}
	if after := completeOnboardingDigestForTest(t, st); after != beforeStaleWrite {
		t.Fatalf("stale route draft changed completed projection:\nbefore=%s\nafter=%s", beforeStaleWrite, after)
	}
}

func TestCompleteOnboardingRouteRevisionUsesDivergentMaximum(t *testing.T) {
	for _, tt := range []struct {
		name                string
		comboRevision, meta int64
		want                int64
	}{
		{name: "legacy ahead", comboRevision: 7, meta: 44, want: 45},
		{name: "combo ahead", comboRevision: 88, meta: 3, want: 89},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			now := seedCompleteOnboardingForTest(t, st, false, 54)
			if _, err := st.db.Exec(`UPDATE llm_combos SET revision = ?`, tt.comboRevision); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`UPDATE app_meta SET value = ? WHERE key = 'llm_route_revision'`,
				fmt.Sprintf("%d", tt.meta)); err != nil {
				t.Fatal(err)
			}

			if _, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
				Revision: 54, TestNonceHash: completeOnboardingHashForTest(),
				PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
			}); err != nil {
				t.Fatal(err)
			}
			route, err := st.LLMRoute()
			if err != nil {
				t.Fatal(err)
			}
			if route.Revision != tt.want || onboardingRouteRevisionForTest(t, st) != tt.want {
				t.Fatalf("completed route/meta revisions = %d/%d; want %d",
					route.Revision, onboardingRouteRevisionForTest(t, st), tt.want)
			}
		})
	}
}

func TestCompleteOnboardingFreshRouteStartsAtRevisionOne(t *testing.T) {
	st := openAppStoreForTest(t)
	now := seedCompleteOnboardingForTest(t, st, false, 55)
	if _, err := st.db.Exec(`DELETE FROM llm_combo_members`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM llm_combos`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM llm_route_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE app_meta SET value = '1' WHERE key = 'llm_route_revision'`); err != nil {
		t.Fatal(err)
	}

	if _, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
		Revision: 55, TestNonceHash: completeOnboardingHashForTest(),
		PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	route, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if route.Revision != 1 || onboardingRouteRevisionForTest(t, st) != 1 {
		t.Fatalf("fresh route/meta revisions = %d/%d; want 1/1",
			route.Revision, onboardingRouteRevisionForTest(t, st))
	}
}

func TestCompleteOnboardingPreservesLegacyRevisionWithoutProjectionRows(t *testing.T) {
	st := openAppStoreForTest(t)
	now := seedCompleteOnboardingForTest(t, st, false, 57)
	if _, err := st.db.Exec(`DELETE FROM llm_combo_members`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM llm_combos`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM llm_route_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE app_meta SET value = '44' WHERE key = 'llm_route_revision'`); err != nil {
		t.Fatal(err)
	}

	if _, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
		Revision: 57, TestNonceHash: completeOnboardingHashForTest(),
		PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	route, err := st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if route.Revision != 45 || onboardingRouteRevisionForTest(t, st) != 45 {
		t.Fatalf("historical route/meta revisions = %d/%d; want 45/45",
			route.Revision, onboardingRouteRevisionForTest(t, st))
	}
}

func TestCompleteOnboardingRouteRevisionOverflowRollsBack(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Store) error
	}{
		{name: "legacy maximum", mutate: func(st *Store) error {
			_, err := st.db.Exec(`UPDATE app_meta SET value = '9223372036854775807' WHERE key = 'llm_route_revision'`)
			return err
		}},
		{name: "combo maximum", mutate: func(st *Store) error {
			_, err := st.db.Exec(`UPDATE llm_combos SET revision = 9223372036854775807`)
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			now := seedCompleteOnboardingForTest(t, st, false, 56)
			if err := tt.mutate(st); err != nil {
				t.Fatal(err)
			}
			before := completeOnboardingDigestForTest(t, st)
			if _, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
				Revision: 56, TestNonceHash: completeOnboardingHashForTest(),
				PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
			}); !errors.Is(err, ErrOnboardingConfigurationChanged) {
				t.Fatalf("CompleteOnboarding() error = %v; want ErrOnboardingConfigurationChanged", err)
			}
			if after := completeOnboardingDigestForTest(t, st); after != before {
				t.Fatalf("overflow changed complete digest:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestCompleteOnboardingRestartReplacesLiveRouteOnlyAtCommit(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "complete.db")
	st, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := seedCompleteOnboardingForTest(t, st, true, 61)
	before := completeOnboardingDigestForTest(t, st)
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	oldFailpoint := onboardingCompleteFailpoint
	onboardingCompleteFailpoint = func(stage string) error {
		if stage != onboardingCompleteStageBeforeCommit {
			return nil
		}
		if during := completeOnboardingDigestFromDBForTest(t, raw); during != before {
			t.Fatalf("uncommitted replacement became visible:\nbefore=%s\nduring=%s", before, during)
		}
		return nil
	}
	t.Cleanup(func() { onboardingCompleteFailpoint = oldFailpoint })

	got, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
		Revision: 61, TestNonceHash: completeOnboardingHashForTest(),
		PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletedVersion != CurrentOnboardingVersion || got.RestartInProgress || got.Revision != 62 {
		t.Fatalf("restart completion state = %+v", got)
	}
	if after := completeOnboardingDigestForTest(t, st); after == before {
		t.Fatal("restart completion did not replace the live projection")
	}
}

func TestCompleteOnboardingRollsBackEveryStatementGroup(t *testing.T) {
	stages := []string{
		onboardingCompleteStageAccounts,
		onboardingCompleteStageMemberDelete,
		onboardingCompleteStageLegacyRouteDelete,
		onboardingCompleteStageComboDelete,
		onboardingCompleteStageComboInsert,
		onboardingCompleteStageMemberInsert,
		onboardingCompleteStageRouteSync,
		onboardingCompleteStageStateUpdate,
		onboardingCompleteStageBeforeCommit,
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			st := openAppStoreForTest(t)
			now := seedCompleteOnboardingForTest(t, st, false, 71)
			before := completeOnboardingDigestForTest(t, st)
			oldFailpoint := onboardingCompleteFailpoint
			onboardingCompleteFailpoint = func(current string) error {
				if current == stage {
					return errors.New("injected completion failure")
				}
				return nil
			}
			t.Cleanup(func() { onboardingCompleteFailpoint = oldFailpoint })

			_, err := st.CompleteOnboarding(context.Background(), CompleteOnboardingInput{
				Revision: 71, TestNonceHash: completeOnboardingHashForTest(),
				PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
			})
			if !errors.Is(err, ErrOnboardingCommitFailed) {
				t.Fatalf("stage %q error = %v; want ErrOnboardingCommitFailed", stage, err)
			}
			if after := completeOnboardingDigestForTest(t, st); after != before {
				t.Fatalf("stage %q escaped rollback:\nbefore=%s\nafter=%s", stage, before, after)
			}
		})
	}
}

func TestCompleteOnboardingActualCommitErrorRollsBackAndReceiptCanRetry(t *testing.T) {
	// This test intentionally stays nonparallel because it replaces the package-level Commit seam.
	st := openAppStoreForTest(t)
	now := seedCompleteOnboardingForTest(t, st, false, 72)
	before := completeOnboardingDigestForTest(t, st)
	oldCommit := onboardingCompleteCommit
	commitCalls := 0
	onboardingCompleteCommit = func(*sql.Tx) error {
		commitCalls++
		return errors.New("injected actual Commit error")
	}
	t.Cleanup(func() { onboardingCompleteCommit = oldCommit })
	input := CompleteOnboardingInput{
		Revision: 72, TestNonceHash: completeOnboardingHashForTest(),
		PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
	}

	if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, ErrOnboardingCommitFailed) {
		t.Fatalf("CompleteOnboarding() actual Commit error = %v; want ErrOnboardingCommitFailed", err)
	}
	if commitCalls != 1 {
		t.Fatalf("actual Commit seam calls = %d; want 1", commitCalls)
	}
	if after := completeOnboardingDigestForTest(t, st); after != before {
		t.Fatalf("actual Commit error escaped rollback:\nbefore=%s\nafter=%s", before, after)
	}
	state := mustOnboardingStateForTest(t, st)
	if state.TestNonceHash != completeOnboardingHashForTest() || state.TestExpiresAt == "" || state.Revision != 72 {
		t.Fatalf("actual Commit error consumed reusable receipt: %+v", state)
	}
	if _, err := st.LLMRoute(); err != nil {
		t.Fatalf("Store unusable after actual Commit error: %v", err)
	}

	onboardingCompleteCommit = oldCommit
	completed, err := st.CompleteOnboarding(context.Background(), input)
	if err != nil {
		t.Fatalf("retry after actual Commit error = %v", err)
	}
	if completed.Phase != OnboardingPhaseCompleted || completed.Revision != 73 {
		t.Fatalf("retry completion = %+v", completed)
	}
}

func TestCompleteOnboardingRejectsInvalidReceiptAndConfigurationWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Store, *OnboardingState, *CompleteOnboardingInput)
		want   error
	}{
		{name: "stale revision", want: ErrOnboardingConflict, mutate: func(_ *Store, _ *OnboardingState, input *CompleteOnboardingInput) { input.Revision-- }},
		{name: "wrong phase", want: ErrOnboardingTestRequired, mutate: func(_ *Store, state *OnboardingState, _ *CompleteOnboardingInput) {
			state.Phase = OnboardingPhasePersona
		}},
		{name: "missing receipt", want: ErrOnboardingTestRequired, mutate: func(_ *Store, state *OnboardingState, _ *CompleteOnboardingInput) { state.TestNonceHash = "" }},
		{name: "wrong receipt", want: ErrOnboardingTestRequired, mutate: func(_ *Store, _ *OnboardingState, input *CompleteOnboardingInput) {
			input.TestNonceHash = strings.Repeat("cd", sha256.Size)
		}},
		{name: "expired receipt", want: ErrOnboardingTestExpired, mutate: func(_ *Store, state *OnboardingState, input *CompleteOnboardingInput) {
			state.TestExpiresAt = input.Now.Format(time.RFC3339Nano)
		}},
		{name: "persona changed", want: ErrOnboardingConfigurationChanged, mutate: func(_ *Store, _ *OnboardingState, input *CompleteOnboardingInput) {
			input.PersonaFingerprint = "changed"
		}},
		{name: "provider missing", want: ErrOnboardingConfigurationChanged, mutate: func(st *Store, _ *OnboardingState, _ *CompleteOnboardingInput) {
			_, _ = st.db.Exec(`DELETE FROM llm_providers WHERE id = 'codex'`)
		}},
		{name: "provider kind drift", want: ErrOnboardingConfigurationChanged, mutate: func(st *Store, _ *OnboardingState, _ *CompleteOnboardingInput) {
			_, _ = st.db.Exec(`UPDATE llm_providers SET kind = 'openai' WHERE id = 'codex'`)
		}},
		{name: "account enabled drift", want: ErrOnboardingConfigurationChanged, mutate: func(st *Store, _ *OnboardingState, _ *CompleteOnboardingInput) {
			_, _ = st.db.Exec(`UPDATE llm_accounts SET enabled = 1 WHERE id = 'staged-account'`)
		}},
		{name: "account provider drift", want: ErrOnboardingConfigurationChanged, mutate: func(st *Store, _ *OnboardingState, _ *CompleteOnboardingInput) {
			_, _ = st.db.Exec(`UPDATE llm_accounts SET provider_id = 'other-provider' WHERE id = 'staged-account'`)
		}},
		{name: "config drift", want: ErrOnboardingConfigurationChanged, mutate: func(st *Store, _ *OnboardingState, _ *CompleteOnboardingInput) {
			_, _ = st.db.Exec(`UPDATE llm_accounts SET config_dir = 'D:/changed' WHERE id = 'staged-account'`)
		}},
		{name: "model unavailable", want: ErrOnboardingConfigurationChanged, mutate: func(st *Store, _ *OnboardingState, _ *CompleteOnboardingInput) {
			_, _ = st.db.Exec(`UPDATE llm_models SET available = 0 WHERE provider_id = 'codex'`)
		}},
		{name: "model name invalid", want: ErrOnboardingConfigurationChanged, mutate: func(st *Store, _ *OnboardingState, _ *CompleteOnboardingInput) {
			_, _ = st.db.Exec(`UPDATE llm_models SET name = '' WHERE provider_id = 'codex'`)
		}},
		{name: "combo id invalid", want: ErrOnboardingConfigurationChanged, mutate: func(_ *Store, state *OnboardingState, _ *CompleteOnboardingInput) { state.StagedComboID = "not-a-uuid" }},
		{name: "already completed", want: ErrOnboardingTestRequired, mutate: func(_ *Store, state *OnboardingState, _ *CompleteOnboardingInput) {
			state.Phase = OnboardingPhaseCompleted
			state.CompletedVersion = CurrentOnboardingVersion
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := openAppStoreForTest(t)
			now := seedCompleteOnboardingForTest(t, st, false, 81)
			state := mustOnboardingStateForTest(t, st)
			input := CompleteOnboardingInput{
				Revision: 81, TestNonceHash: completeOnboardingHashForTest(),
				PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
			}
			tt.mutate(st, &state, &input)
			setOnboardingStateForTest(t, st, state)
			before := completeOnboardingDigestForTest(t, st)
			if _, err := st.CompleteOnboarding(context.Background(), input); !errors.Is(err, tt.want) {
				t.Fatalf("CompleteOnboarding() error = %v; want %v", err, tt.want)
			}
			if after := completeOnboardingDigestForTest(t, st); after != before {
				t.Fatalf("rejected completion mutated data:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestCompleteOnboardingAllowsExactlyOneConcurrentWinner(t *testing.T) {
	st := openAppStoreForTest(t)
	now := seedCompleteOnboardingForTest(t, st, false, 91)
	input := CompleteOnboardingInput{
		Revision: 91, TestNonceHash: completeOnboardingHashForTest(),
		PersonaFingerprint: "persona-fingerprint", StagingConfigDir: "D:/onboarding/staged-account", Now: now,
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := st.CompleteOnboarding(context.Background(), input)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrOnboardingConflict):
			conflicts++
		default:
			t.Fatalf("concurrent completion error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent outcomes success=%d conflict=%d", successes, conflicts)
	}
}

func seedCompleteOnboardingForTest(t *testing.T, st *Store, restart bool, revision int64) time.Time {
	t.Helper()
	if err := st.EnsureOnboardingProviderForKind("codex"); err != nil {
		t.Fatal(err)
	}
	insertOnboardingAccountForTest(t, st, "staged-account", "codex", false, "D:/onboarding/staged-account")
	insertOnboardingAccountForTest(t, st, "old-live", "codex", true, "D:/live/old")
	insertOnboardingAccountForTest(t, st, "old-disabled", "codex", false, "D:/old/disabled")
	insertOnboardingProviderForTest(t, st, "other-provider", "openai")
	insertOnboardingAccountForTest(t, st, "other-live", "other-provider", true, "D:/other/live")
	if err := st.AddLLMModel(LLMModel{
		ProviderID: "codex", ModelID: "gpt-5.6-terra", Name: "Terra",
		Source: LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddLLMModel(LLMModel{
		ProviderID: "other-provider", ModelID: "old-model", Name: "Old",
		Source: LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	combo, err := st.CreateLLMCombo("Old live", "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReplaceLLMComboMembers(combo.ID, combo.Revision, "fallback", []LLMRouteEntry{{
		ProviderID: "other-provider", ModelID: "old-model", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveLLMCombo(combo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO llm_route_entries(position, provider_id, model_id, enabled)
VALUES (0, 'other-provider', 'old-model', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE app_meta SET value = '41' WHERE key = 'llm_route_revision'`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 11, 12, 0, 0, 123, time.UTC)
	completedVersion := int64(0)
	if restart {
		completedVersion = CurrentOnboardingVersion
	}
	setOnboardingStateForTest(t, st, OnboardingState{
		CompletedVersion: completedVersion, Phase: OnboardingPhaseTest,
		ProviderKind: "codex", ProviderID: "codex", AccountID: "staged-account",
		ModelID: "gpt-5.6-terra", StagedComboID: onboardingCompletionComboIDForTest,
		PersonaFingerprint: "persona-fingerprint", TestNonceHash: completeOnboardingHashForTest(),
		TestExpiresAt:     now.Add(time.Minute).Format(time.RFC3339Nano),
		RestartInProgress: restart, Revision: revision,
	})
	return now
}

func completeOnboardingHashForTest() string {
	digest := sha256.Sum256([]byte("verified-test-token"))
	return fmt.Sprintf("%x", digest[:])
}

func onboardingRouteRevisionForTest(t *testing.T, st *Store) int64 {
	t.Helper()
	var value int64
	if err := st.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM app_meta WHERE key = 'llm_route_revision'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func onboardingLegacyRouteForTest(t *testing.T, st *Store) string {
	t.Helper()
	var value string
	if err := st.db.QueryRow(`SELECT COALESCE(group_concat(position || ':' || provider_id || ':' || model_id || ':' || enabled, ','), '')
FROM llm_route_entries`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func completeOnboardingDigestForTest(t *testing.T, st *Store) string {
	t.Helper()
	return completeOnboardingDigestFromDBForTest(t, st.db)
}

func completeOnboardingDigestFromDBForTest(t *testing.T, db *sql.DB) string {
	t.Helper()
	queries := []string{
		`SELECT completed_version, phase, provider_kind, provider_id, account_id, model_id, staged_combo_id, persona_fingerprint, test_nonce_hash, test_expires_at, restart_in_progress, revision, updated_at FROM app_onboarding_state ORDER BY id`,
		`SELECT id, name, kind, enabled, system_provider, hex(credential_cipher), last_check_status, last_error, last_checked_at FROM llm_providers ORDER BY id`,
		`SELECT id, provider_id, label, email, config_dir, enabled, added_at FROM llm_accounts ORDER BY id`,
		`SELECT provider_id, model_id, name, source, available FROM llm_models ORDER BY provider_id, model_id`,
		`SELECT id, name, type, active, revision FROM llm_combos ORDER BY id`,
		`SELECT combo_id, position, provider_id, model_id, enabled FROM llm_combo_members ORDER BY combo_id, position`,
		`SELECT position, provider_id, model_id, enabled FROM llm_route_entries ORDER BY position`,
		`SELECT key, value FROM app_meta WHERE key = 'llm_route_revision'`,
	}
	var digest strings.Builder
	for _, query := range queries {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatalf("digest query %q: %v", query, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		fmt.Fprintf(&digest, "%s|", query)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			fmt.Fprintf(&digest, "%#v;", values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return digest.String()
}
