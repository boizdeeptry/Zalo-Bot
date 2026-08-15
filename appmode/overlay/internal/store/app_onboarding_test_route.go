package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	onboardingTestRouteFingerprintDomain = "agentdc/onboarding-test-route/v1"
	onboardingTestReceiptHashDomain      = "agentdc/onboarding-test-receipt/v1"
)

// OnboardingTestRouteEntry is one immutable, disabled route member used only by
// the onboarding verification run. ConfigDir is runtime metadata and is compared
// exactly at the final Store fence, but is deliberately excluded from the public
// route fingerprint so no local path is persisted in a receipt.
type OnboardingTestRouteEntry struct {
	Position   int
	Kind       string
	ProviderID string
	AccountID  string
	ModelID    string
	ConfigDir  string
}

// OnboardingTestRoute is the authoritative transaction-local view used before
// and after external onboarding Test Chat I/O.
type OnboardingTestRoute struct {
	State       OnboardingState
	Entries     []OnboardingTestRouteEntry
	DisplayName string
	Fingerprint string
}

// OnboardingTestRouteFingerprint returns a versioned, domain-separated digest.
// Every value is framed with an unsigned 64-bit length, so different field
// boundaries can never produce the same byte stream.
func OnboardingTestRouteFingerprint(
	entries []OnboardingTestRouteEntry,
	personaFingerprint string,
	displayName string,
) (string, error) {
	if len(entries) == 0 || !validOnboardingSHA256Hex(personaFingerprint) ||
		!validOnboardingCompletionName(displayName) || displayName != strings.TrimSpace(displayName) {
		return "", ErrOnboardingConfigurationChanged
	}
	hash := sha256.New()
	writeOnboardingFingerprintFrame(hash.Write, onboardingTestRouteFingerprintDomain)
	writeOnboardingFingerprintFrame(hash.Write, strconv.Itoa(len(entries)))
	for index, entry := range entries {
		if entry.Position != index || !validOnboardingCompletionID(entry.Kind) ||
			!validOnboardingCompletionID(entry.ProviderID) ||
			!validOnboardingCompletionID(entry.AccountID) ||
			!validOnboardingCompletionID(entry.ModelID) {
			return "", ErrOnboardingConfigurationChanged
		}
		writeOnboardingFingerprintFrame(hash.Write, strconv.Itoa(entry.Position))
		writeOnboardingFingerprintFrame(hash.Write, entry.Kind)
		writeOnboardingFingerprintFrame(hash.Write, entry.ProviderID)
		writeOnboardingFingerprintFrame(hash.Write, entry.AccountID)
		writeOnboardingFingerprintFrame(hash.Write, entry.ModelID)
	}
	writeOnboardingFingerprintFrame(hash.Write, personaFingerprint)
	writeOnboardingFingerprintFrame(hash.Write, displayName)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// OnboardingTestReceiptHash binds an opaque nonce digest to the exact staged
// route that produced the successful answer. Neither secret is stored here.
func OnboardingTestReceiptHash(nonceHash string, routeFingerprint string) (string, error) {
	if !validOnboardingSHA256Hex(nonceHash) || !validOnboardingSHA256Hex(routeFingerprint) {
		return "", ErrOnboardingInvalidTestReceipt
	}
	hash := sha256.New()
	writeOnboardingFingerprintFrame(hash.Write, onboardingTestReceiptHashDomain)
	writeOnboardingFingerprintFrame(hash.Write, nonceHash)
	writeOnboardingFingerprintFrame(hash.Write, routeFingerprint)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeOnboardingFingerprintFrame(write func([]byte) (int, error), value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = write(length[:])
	_, _ = write([]byte(value))
}

// OnboardingTestRoute reads and validates the complete staged route in one
// transaction. Returned slices are newly allocated and safe for the caller to
// retain as an immutable preflight snapshot.
func (s *Store) OnboardingTestRoute(
	ctx context.Context,
	expectedRevision int64,
) (OnboardingTestRoute, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OnboardingTestRoute{}, fmt.Errorf("begin onboarding Test route: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	route, err := onboardingTestRouteInTx(ctx, tx, expectedRevision)
	if err != nil {
		return OnboardingTestRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return OnboardingTestRoute{}, fmt.Errorf("commit onboarding Test route read: %w", err)
	}
	return cloneOnboardingTestRoute(route), nil
}

func onboardingTestRouteInTx(
	ctx context.Context,
	tx *sql.Tx,
	expectedRevision int64,
) (OnboardingTestRoute, error) {
	state, err := onboardingStateInTxContext(ctx, tx)
	if err != nil {
		return OnboardingTestRoute{}, err
	}
	if state.Revision != expectedRevision {
		return OnboardingTestRoute{}, onboardingConflict(expectedRevision, state.Revision)
	}
	if state.Phase != OnboardingPhaseTest || !onboardingCompletionIsRequired(state) {
		return OnboardingTestRoute{}, fmt.Errorf(
			"read onboarding Test route from %q: %w",
			state.Phase,
			ErrOnboardingInvalidPhase,
		)
	}
	if state.ProviderKind != "" || state.ProviderID != "" || state.AccountID != "" ||
		state.ModelID != "" || !validOnboardingSHA256Hex(state.PersonaFingerprint) {
		return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
	}
	comboID, err := uuid.Parse(state.StagedComboID)
	if err != nil || comboID.String() != strings.ToLower(state.StagedComboID) {
		return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
	}
	if (state.TestNonceHash == "") != (state.TestExpiresAt == "") {
		return OnboardingTestRoute{}, ErrOnboardingInvalidTestReceipt
	}
	if state.TestNonceHash != "" {
		if !validOnboardingSHA256Hex(state.TestNonceHash) {
			return OnboardingTestRoute{}, ErrOnboardingInvalidTestReceipt
		}
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, state.TestExpiresAt)
		if parseErr != nil || expiresAt.Format(time.RFC3339Nano) != state.TestExpiresAt ||
			!validOnboardingTestReceiptTime(expiresAt) {
			return OnboardingTestRoute{}, ErrOnboardingInvalidTestReceipt
		}
	}

	stages, err := onboardingProviderStagesInTx(tx)
	if err != nil {
		return OnboardingTestRoute{}, err
	}
	inspection, err := inspectOnboardingProviderStages(stages)
	if err != nil {
		return OnboardingTestRoute{}, err
	}
	if len(stages) == 0 || !inspection.positionsCanonical {
		return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
	}
	entries := make([]OnboardingTestRouteEntry, 0, len(stages))
	for _, stage := range stages {
		if stage.Status != onboardingProviderStageReady || stage.ProviderID != stage.Kind {
			return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
		}
		var providerKind, providerName string
		var providerEnabled int64
		err := tx.QueryRowContext(ctx, `SELECT kind, name, enabled
FROM llm_providers WHERE id = ?`, stage.ProviderID).Scan(
			&providerKind,
			&providerName,
			&providerEnabled,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
		}
		if err != nil {
			return OnboardingTestRoute{}, fmt.Errorf("read onboarding Test Provider: %w", err)
		}
		if providerKind != stage.Kind || (providerEnabled != 0 && providerEnabled != 1) ||
			!validOnboardingCompletionName(providerName) {
			return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
		}
		account, err := onboardingProviderAccountRecordInTx(tx, stage.AccountID)
		if err != nil {
			return OnboardingTestRoute{}, err
		}
		if !onboardingProviderAccountOwnsSetup(account, stage.Kind, stage.AccountID) ||
			strings.TrimSpace(account.Account.ConfigDir) == "" {
			return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
		}
		available, err := onboardingProviderModelIsAvailableInTx(tx, stage.ProviderID, stage.ModelID)
		if err != nil {
			return OnboardingTestRoute{}, err
		}
		if !available {
			return OnboardingTestRoute{}, ErrOnboardingModelUnavailable
		}
		entries = append(entries, OnboardingTestRouteEntry{
			Position: stage.Position, Kind: stage.Kind, ProviderID: stage.ProviderID,
			AccountID: stage.AccountID, ModelID: stage.ModelID,
			ConfigDir: account.Account.ConfigDir,
		})
	}
	var displayName string
	err = tx.QueryRowContext(ctx, `SELECT value FROM app_meta WHERE key = ?`, agentDisplayNameMetaKey).
		Scan(&displayName)
	if errors.Is(err, sql.ErrNoRows) {
		return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
	}
	if err != nil {
		return OnboardingTestRoute{}, fmt.Errorf("read onboarding Test display name: %w", err)
	}
	if !validOnboardingCompletionName(displayName) || displayName != strings.TrimSpace(displayName) {
		return OnboardingTestRoute{}, ErrOnboardingInvalidStagingOwnership
	}
	fingerprint, err := OnboardingTestRouteFingerprint(entries, state.PersonaFingerprint, displayName)
	if err != nil {
		return OnboardingTestRoute{}, err
	}
	return OnboardingTestRoute{
		State: state, Entries: entries, DisplayName: displayName, Fingerprint: fingerprint,
	}, nil
}

func cloneOnboardingTestRoute(route OnboardingTestRoute) OnboardingTestRoute {
	cloned := route
	cloned.Entries = append([]OnboardingTestRouteEntry(nil), route.Entries...)
	return cloned
}

func onboardingTestRoutesEqual(left, right OnboardingTestRoute) bool {
	if left.State != right.State || left.DisplayName != right.DisplayName ||
		left.Fingerprint != right.Fingerprint || len(left.Entries) != len(right.Entries) {
		return false
	}
	for index := range left.Entries {
		if left.Entries[index] != right.Entries[index] {
			return false
		}
	}
	return true
}

// SaveOnboardingTestReceipt performs the final post-I/O TOCTOU fence inside
// the same transaction that stores the route-bound receipt.
func (s *Store) SaveOnboardingTestReceipt(
	ctx context.Context,
	expected OnboardingTestRoute,
	nonceHash string,
	policy OnboardingTestReceiptPolicy,
) (OnboardingState, error) {
	if expected.State.Revision < 1 || expected.State.Revision == maxOnboardingRouteRevision ||
		!validOnboardingSHA256Hex(expected.Fingerprint) ||
		!validOnboardingSHA256Hex(nonceHash) || !validOnboardingTestReceiptPolicy(policy) {
		return OnboardingState{}, ErrOnboardingInvalidTestReceipt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OnboardingState{}, fmt.Errorf("begin onboarding test receipt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	actual, err := onboardingTestRouteInTx(ctx, tx, expected.State.Revision)
	if err != nil {
		return OnboardingState{}, err
	}
	if !onboardingTestRoutesEqual(actual, expected) {
		return OnboardingState{}, classifyOnboardingTestRouteMismatch(actual, expected)
	}
	boundHash, err := OnboardingTestReceiptHash(nonceHash, actual.Fingerprint)
	if err != nil {
		return OnboardingState{}, err
	}
	expiresText := policy.ExpiresAt.Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE app_onboarding_state SET
test_nonce_hash = ?, test_expires_at = ?, revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ? AND provider_kind = ''
  AND provider_id = '' AND account_id = '' AND model_id = '' AND staged_combo_id = ?
  AND persona_fingerprint = ? AND test_nonce_hash = ? AND test_expires_at = ?`,
		boundHash,
		expiresText,
		ts(policy.IssuedAt),
		actual.State.Revision,
		OnboardingPhaseTest,
		actual.State.StagedComboID,
		actual.State.PersonaFingerprint,
		actual.State.TestNonceHash,
		actual.State.TestExpiresAt,
	)
	if err != nil {
		return OnboardingState{}, fmt.Errorf("save onboarding test receipt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding test receipt result: %w", err)
	}
	if changed != 1 {
		return OnboardingState{}, ErrOnboardingConflict
	}
	updated, err := onboardingStateInTxContext(ctx, tx)
	if err != nil {
		return OnboardingState{}, err
	}
	onboardingTestReceiptBeforeCommit(ctx)
	if err := ctx.Err(); err != nil {
		return OnboardingState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OnboardingState{}, fmt.Errorf("commit onboarding test receipt: %w", err)
	}
	return updated, nil
}

// ClearOnboardingTestReceipt compensates only the exact committed route-bound
// receipt; any route, persona, name, config, receipt, or revision drift fails.
func (s *Store) ClearOnboardingTestReceipt(
	ctx context.Context,
	expected OnboardingTestRoute,
	nonceHash string,
	policy OnboardingTestReceiptPolicy,
) (OnboardingState, error) {
	if expected.State.Revision < 1 || expected.State.Revision == maxOnboardingRouteRevision ||
		!validOnboardingSHA256Hex(expected.Fingerprint) ||
		!validOnboardingSHA256Hex(nonceHash) || !validOnboardingTestReceiptPolicy(policy) {
		return OnboardingState{}, ErrOnboardingInvalidTestReceipt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OnboardingState{}, fmt.Errorf("begin onboarding receipt compensation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	actual, err := onboardingTestRouteInTx(ctx, tx, expected.State.Revision)
	if err != nil {
		return OnboardingState{}, err
	}
	if !onboardingTestRoutesEqual(actual, expected) {
		return OnboardingState{}, ErrOnboardingConflict
	}
	boundHash, err := OnboardingTestReceiptHash(nonceHash, actual.Fingerprint)
	if err != nil {
		return OnboardingState{}, err
	}
	if actual.State.TestNonceHash != boundHash ||
		actual.State.TestExpiresAt != policy.ExpiresAt.Format(time.RFC3339Nano) {
		return OnboardingState{}, ErrOnboardingConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE app_onboarding_state SET
test_nonce_hash = '', test_expires_at = '', revision = revision + 1, updated_at = ?
WHERE id = 1 AND revision = ? AND phase = ? AND persona_fingerprint = ?
  AND test_nonce_hash = ? AND test_expires_at = ?`,
		ts(policy.IssuedAt), actual.State.Revision, OnboardingPhaseTest,
		actual.State.PersonaFingerprint, boundHash, policy.ExpiresAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return OnboardingState{}, fmt.Errorf("clear onboarding test receipt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return OnboardingState{}, fmt.Errorf("read onboarding receipt compensation result: %w", err)
	}
	if changed != 1 {
		return OnboardingState{}, ErrOnboardingConflict
	}
	updated, err := onboardingStateInTxContext(ctx, tx)
	if err != nil {
		return OnboardingState{}, err
	}
	if err := ctx.Err(); err != nil {
		return OnboardingState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OnboardingState{}, fmt.Errorf("commit onboarding receipt compensation: %w", err)
	}
	return updated, nil
}

func classifyOnboardingTestRouteMismatch(actual, expected OnboardingTestRoute) error {
	if actual.State.Revision != expected.State.Revision {
		return ErrOnboardingConflict
	}
	if actual.State.PersonaFingerprint != expected.State.PersonaFingerprint {
		return ErrOnboardingPersonaMismatch
	}
	return ErrOnboardingInvalidStagingOwnership
}
