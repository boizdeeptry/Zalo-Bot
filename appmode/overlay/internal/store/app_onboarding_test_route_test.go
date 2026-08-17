package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOnboardingTestRouteFingerprintStagesIsDeterministicFramedAndOrderSensitive(t *testing.T) {
	personaFingerprint := strings.Repeat("a", sha256.Size*2)
	first := []OnboardingTestRouteEntry{{
		Position: 0, Kind: "codex", ProviderID: "codex", AccountID: "ab", ModelID: "c",
	}}
	boundaryCollision := []OnboardingTestRouteEntry{{
		Position: 0, Kind: "codex", ProviderID: "codex", AccountID: "a", ModelID: "bc",
	}}

	want, err := OnboardingTestRouteFingerprint(first, personaFingerprint, "Bé Mi")
	if err != nil {
		t.Fatal(err)
	}
	again, err := OnboardingTestRouteFingerprint(slices.Clone(first), personaFingerprint, "Bé Mi")
	if err != nil || again != want {
		t.Fatalf("deterministic fingerprint = %q, %v; want %q", again, err, want)
	}
	if decoded, err := hex.DecodeString(want); err != nil || len(decoded) != sha256.Size {
		t.Fatalf("fingerprint = %q, %v; want canonical SHA-256", want, err)
	}
	colliding, err := OnboardingTestRouteFingerprint(boundaryCollision, personaFingerprint, "Bé Mi")
	if err != nil {
		t.Fatal(err)
	}
	if colliding == want {
		t.Fatal("uint64 framing did not distinguish an ambiguous field boundary")
	}

	ordered := []OnboardingTestRouteEntry{
		{Position: 0, Kind: "codex", ProviderID: "codex", AccountID: "codex-account", ModelID: "codex-model"},
		{Position: 1, Kind: "claude-code", ProviderID: "claude-code", AccountID: "claude-account", ModelID: "claude-model"},
	}
	reordered := []OnboardingTestRouteEntry{
		{Position: 0, Kind: "claude-code", ProviderID: "claude-code", AccountID: "claude-account", ModelID: "claude-model"},
		{Position: 1, Kind: "codex", ProviderID: "codex", AccountID: "codex-account", ModelID: "codex-model"},
	}
	orderedFingerprint, err := OnboardingTestRouteFingerprint(ordered, personaFingerprint, "Bé Mi")
	if err != nil {
		t.Fatal(err)
	}
	reorderedFingerprint, err := OnboardingTestRouteFingerprint(reordered, personaFingerprint, "Bé Mi")
	if err != nil {
		t.Fatal(err)
	}
	if orderedFingerprint == reorderedFingerprint {
		t.Fatal("ordered route fingerprint survived member reorder")
	}
}

func TestOnboardingTestRouteFingerprintStagesReadsCanonicalReadyRouteAtomically(t *testing.T) {
	st, expected := seedOnboardingTestRouteForTask6(t, 41)

	route, err := st.OnboardingTestRoute(context.Background(), expected.State.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if route.State != expected.State || route.DisplayName != expected.DisplayName ||
		route.Fingerprint == "" || !slices.Equal(route.Entries, expected.Entries) {
		t.Fatalf("route = %+v; want %+v", route, expected)
	}
	route.Entries[0].AccountID = "caller-mutated"
	again, err := st.OnboardingTestRoute(context.Background(), expected.State.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(again.Entries, expected.Entries) {
		t.Fatalf("caller mutation changed persisted route: %+v", again.Entries)
	}
}

func TestOnboardingTestRouteFingerprintStagesAllowsEnabledLiveProvider(t *testing.T) {
	st, expected := seedOnboardingTestRouteForTask6(t, 47)
	if _, err := st.db.Exec(`UPDATE llm_providers SET enabled=1 WHERE id='codex'`); err != nil {
		t.Fatal(err)
	}

	route, err := st.OnboardingTestRoute(context.Background(), expected.State.Revision)
	if err != nil {
		t.Fatalf("OnboardingTestRoute() = %v; enabled live Provider must remain usable", err)
	}
	if !slices.Equal(route.Entries, expected.Entries) || route.Fingerprint != expected.Fingerprint {
		t.Fatalf("enabled Provider changed staged route: got=%+v want=%+v", route, expected)
	}
}

func TestOnboardingTestRouteFingerprintStagesRejectsOwnershipDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Store, OnboardingTestRoute)
		want   error
	}{
		{name: "stale revision", want: ErrOnboardingConflict, mutate: func(t *testing.T, st *Store, route OnboardingTestRoute) {
			state := route.State
			state.Revision++
			setOnboardingStateForTest(t, st, state)
		}},
		{name: "dirty active singleton", want: ErrOnboardingInvalidStagingOwnership, mutate: func(t *testing.T, st *Store, route OnboardingTestRoute) {
			state := route.State
			state.ProviderKind = "codex"
			setOnboardingStateForTest(t, st, state)
		}},
		{name: "pending member", want: ErrOnboardingInvalidStagingOwnership, mutate: func(t *testing.T, st *Store, _ OnboardingTestRoute) {
			_, err := st.db.Exec(`UPDATE app_onboarding_provider_stages SET
status='pending', provider_id='', account_id='', model_id='' WHERE kind='codex'`)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Provider enabled malformed", want: ErrOnboardingInvalidStagingOwnership, mutate: func(t *testing.T, st *Store, _ OnboardingTestRoute) {
			setOnboardingProviderEnabledRawForTask6(t, st, "codex", 2)
		}},
		{name: "Account enabled", want: ErrOnboardingInvalidStagingOwnership, mutate: func(t *testing.T, st *Store, _ OnboardingTestRoute) {
			if _, err := st.db.Exec(`UPDATE llm_accounts SET enabled=1 WHERE id='codex-account'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "model unavailable", want: ErrOnboardingModelUnavailable, mutate: func(t *testing.T, st *Store, _ OnboardingTestRoute) {
			if _, err := st.db.Exec(`UPDATE llm_models SET available=0 WHERE provider_id='codex' AND model_id='codex-model'`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "blank config metadata", want: ErrOnboardingInvalidStagingOwnership, mutate: func(t *testing.T, st *Store, _ OnboardingTestRoute) {
			if _, err := st.db.Exec(`UPDATE llm_accounts SET config_dir='' WHERE id='codex-account'`); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, expected := seedOnboardingTestRouteForTask6(t, 51)
			test.mutate(t, st, expected)
			before := onboardingProviderMutationDigestForTest(t, st)
			_, err := st.OnboardingTestRoute(context.Background(), expected.State.Revision)
			if !errors.Is(err, test.want) {
				t.Fatalf("OnboardingTestRoute() error = %v; want %v", err, test.want)
			}
			if after := onboardingProviderMutationDigestForTest(t, st); after != before {
				t.Fatal("rejected route read mutated database")
			}
		})
	}
}

func TestOnboardingTestRouteFingerprintStagesDoesNotMaskDatabaseReadFailures(t *testing.T) {
	for _, table := range []string{"llm_providers", "app_meta"} {
		t.Run(table, func(t *testing.T) {
			st, expected := seedOnboardingTestRouteForTask6(t, 56)
			if _, err := st.db.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			_, err := st.OnboardingTestRoute(context.Background(), expected.State.Revision)
			if err == nil || errors.Is(err, ErrOnboardingInvalidStagingOwnership) ||
				errors.Is(err, ErrOnboardingModelUnavailable) {
				t.Fatalf("OnboardingTestRoute() error = %v; want unmasked database read failure", err)
			}
		})
	}
}

func TestOnboardingTestReceiptFingerprintStagesBindsExactRouteAndCompensatesExactly(t *testing.T) {
	st, route := seedOnboardingTestRouteForTask6(t, 61)
	nonceDigest := sha256.Sum256([]byte("opaque-test-token"))
	nonceHash := hex.EncodeToString(nonceDigest[:])
	policy := NewOnboardingTestReceiptPolicy(time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))

	saved, err := st.SaveOnboardingTestReceipt(context.Background(), route, nonceHash, policy)
	if err != nil {
		t.Fatal(err)
	}
	boundHash, err := OnboardingTestReceiptHash(nonceHash, route.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if saved.TestNonceHash != boundHash || saved.TestNonceHash == nonceHash || saved.Revision != 62 {
		t.Fatalf("saved receipt = %+v; want route-bound hash %q", saved, boundHash)
	}

	savedRoute := route
	savedRoute.State = saved
	cleared, err := st.ClearOnboardingTestReceipt(context.Background(), savedRoute, nonceHash, policy)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Revision != 63 || cleared.TestNonceHash != "" || cleared.TestExpiresAt != "" {
		t.Fatalf("cleared receipt = %+v", cleared)
	}
}

func TestOnboardingTestReceiptFingerprintStagesReplacesOnlyExactPriorReceipt(t *testing.T) {
	st, original := seedOnboardingTestRouteForTask6(t, 66)
	firstNonce := sha256.Sum256([]byte("first-opaque-test-token"))
	firstHash := hex.EncodeToString(firstNonce[:])
	firstPolicy := NewOnboardingTestReceiptPolicy(time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))
	first, err := st.SaveOnboardingTestReceipt(context.Background(), original, firstHash, firstPolicy)
	if err != nil {
		t.Fatal(err)
	}

	secondNonce := sha256.Sum256([]byte("second-opaque-test-token"))
	secondHash := hex.EncodeToString(secondNonce[:])
	secondPolicy := NewOnboardingTestReceiptPolicy(firstPolicy.IssuedAt.Add(time.Second))
	beforeStale := mustOnboardingStateForTest(t, st)
	if _, err := st.SaveOnboardingTestReceipt(context.Background(), original, secondHash, secondPolicy); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("stale SaveOnboardingTestReceipt() error = %v; want conflict", err)
	}
	if afterStale := mustOnboardingStateForTest(t, st); afterStale != beforeStale {
		t.Fatalf("stale replacement mutated receipt: before=%+v after=%+v", beforeStale, afterStale)
	}

	refreshed, err := st.OnboardingTestRoute(context.Background(), first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.SaveOnboardingTestReceipt(context.Background(), refreshed, secondHash, secondPolicy)
	if err != nil {
		t.Fatalf("exact receipt replacement failed: %v", err)
	}
	wantSecond, err := OnboardingTestReceiptHash(secondHash, refreshed.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != first.Revision+1 || second.TestNonceHash != wantSecond ||
		second.TestExpiresAt != secondPolicy.ExpiresAt.Format(time.RFC3339Nano) ||
		second.TestNonceHash == first.TestNonceHash {
		t.Fatalf("replacement = %+v; want exact second receipt %q", second, wantSecond)
	}
}

func TestOnboardingTestReceiptFingerprintStagesCompensationNeverErasesNewerReplacement(t *testing.T) {
	st, original := seedOnboardingTestRouteForTask6(t, 76)
	firstHash := strings.Repeat("11", sha256.Size)
	firstPolicy := NewOnboardingTestReceiptPolicy(time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))
	first, err := st.SaveOnboardingTestReceipt(context.Background(), original, firstHash, firstPolicy)
	if err != nil {
		t.Fatal(err)
	}
	firstCommitted := original
	firstCommitted.State = first

	refreshed, err := st.OnboardingTestRoute(context.Background(), first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	secondHash := strings.Repeat("22", sha256.Size)
	secondPolicy := NewOnboardingTestReceiptPolicy(firstPolicy.IssuedAt.Add(time.Second))
	second, err := st.SaveOnboardingTestReceipt(context.Background(), refreshed, secondHash, secondPolicy)
	if err != nil {
		t.Fatalf("newer replacement failed: %v", err)
	}

	if _, err := st.ClearOnboardingTestReceipt(context.Background(), firstCommitted, firstHash, firstPolicy); !errors.Is(err, ErrOnboardingConflict) {
		t.Fatalf("stale compensation error = %v; want conflict", err)
	}
	if current := mustOnboardingStateForTest(t, st); current != second {
		t.Fatalf("stale compensation erased newer receipt: got=%+v want=%+v", current, second)
	}
}

func TestOnboardingTestReceiptFingerprintStagesRejectsPostflightRouteDrift(t *testing.T) {
	st, expected := seedOnboardingTestRouteForTask6(t, 71)
	nonceDigest := sha256.Sum256([]byte("opaque-test-token"))
	nonceHash := hex.EncodeToString(nonceDigest[:])
	policy := NewOnboardingTestReceiptPolicy(time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))
	if _, err := st.db.Exec(`UPDATE llm_accounts SET config_dir=? WHERE id='claude-account'`, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	before := onboardingProviderMutationDigestForTest(t, st)
	if _, err := st.SaveOnboardingTestReceipt(context.Background(), expected, nonceHash, policy); !errors.Is(err, ErrOnboardingInvalidStagingOwnership) {
		t.Fatalf("SaveOnboardingTestReceipt() error = %v; want staging drift", err)
	}
	if after := onboardingProviderMutationDigestForTest(t, st); after != before {
		t.Fatal("rejected receipt save mutated database")
	}
}

func seedOnboardingTestRouteForTask6(t *testing.T, revision int64) (*Store, OnboardingTestRoute) {
	t.Helper()
	st := openAppStoreForTest(t)
	entries := []OnboardingTestRouteEntry{
		{Position: 0, Kind: "codex", ProviderID: "codex", AccountID: "codex-account", ModelID: "codex-model", ConfigDir: t.TempDir()},
		{Position: 1, Kind: "claude-code", ProviderID: "claude-code", AccountID: "claude-account", ModelID: "claude-model", ConfigDir: t.TempDir()},
	}
	for _, entry := range entries {
		if err := st.EnsureOnboardingProviderForKind(entry.Kind); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE llm_providers SET enabled=0 WHERE id=?`, entry.ProviderID); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateLLMAccount(LLMAccount{
			ID: entry.AccountID, ProviderID: entry.ProviderID, Label: entry.AccountID,
			ConfigDir: entry.ConfigDir, Enabled: false,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddLLMModel(LLMModel{
			ProviderID: entry.ProviderID, ModelID: entry.ModelID, Name: entry.ModelID,
			Source: LLMModelManual, Available: true,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`INSERT INTO app_onboarding_provider_stages(
kind,status,position,provider_id,account_id,model_id,updated_at
) VALUES (?, 'ready', ?, ?, ?, ?, '2026-08-15T00:00:00Z')`,
			entry.Kind, entry.Position, entry.ProviderID, entry.AccountID, entry.ModelID,
		); err != nil {
			t.Fatal(err)
		}
	}
	const displayName = "Bé Mi"
	if err := st.SetAgentDisplayName(displayName); err != nil {
		t.Fatal(err)
	}
	state := OnboardingState{
		Phase: OnboardingPhaseTest, StagedComboID: uuid.NewString(),
		PersonaFingerprint: strings.Repeat("a", sha256.Size*2), Revision: revision,
	}
	setOnboardingStateForTest(t, st, state)
	state = mustOnboardingStateForTest(t, st)
	fingerprint, err := OnboardingTestRouteFingerprint(entries, state.PersonaFingerprint, displayName)
	if err != nil {
		t.Fatal(err)
	}
	return st, OnboardingTestRoute{
		State: state, Entries: entries, DisplayName: displayName, Fingerprint: fingerprint,
	}
}

func setOnboardingProviderEnabledRawForTask6(t *testing.T, st *Store, providerID string, enabled int64) {
	t.Helper()
	conn, err := st.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `UPDATE llm_providers SET enabled=? WHERE id=?`, enabled, providerID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
}
