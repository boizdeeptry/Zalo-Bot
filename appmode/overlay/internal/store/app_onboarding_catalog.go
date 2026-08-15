package store

import (
	"fmt"

	"agentdc/internal/providercatalog"
)

// OnboardingStore binds Store onboarding operations to one immutable Provider
// catalog. The value carries no mutable registry or runtime behavior.
type OnboardingStore struct {
	store   *Store
	catalog providercatalog.Catalog
}

// Onboarding returns a value wrapper that authorizes onboarding Provider kinds
// through exactly catalog. An invalid or zero Catalog fails closed.
func (s *Store) Onboarding(catalog providercatalog.Catalog) OnboardingStore {
	return OnboardingStore{store: s, catalog: catalog}
}

func (s OnboardingStore) OnboardingState() (OnboardingState, error) {
	return s.store.onboardingState(s.catalog)
}

func (s OnboardingStore) OnboardingSnapshot() (OnboardingSnapshot, error) {
	return s.store.onboardingSnapshot(s.catalog)
}

func (s OnboardingStore) OnboardingStagingAccount(
	expectedRevision int64,
) (OnboardingStagingAccount, error) {
	return s.store.onboardingStagingAccount(s.catalog, expectedRevision)
}

func (s OnboardingStore) ReplaceOnboardingProviderSelection(
	expectedRevision int64,
	kinds []string,
) (OnboardingSnapshot, error) {
	return s.store.replaceOnboardingProviderSelection(s.catalog, expectedRevision, kinds)
}

func (s OnboardingStore) BeginOnboardingProvider(
	expectedRevision int64,
	kind string,
) (OnboardingSnapshot, error) {
	return s.store.beginOnboardingProvider(s.catalog, expectedRevision, kind)
}

func (s OnboardingStore) BackOnboardingToProviders(
	expectedRevision int64,
) (OnboardingSnapshot, error) {
	return s.store.backOnboardingToProviders(s.catalog, expectedRevision)
}

func (s OnboardingStore) EnsureOnboardingProviderForKind(kind string) error {
	return s.store.ensureOnboardingProviderForKind(s.catalog, kind)
}

func (s OnboardingStore) SelectOnboardingProvider(
	expectedRevision int64,
	kind string,
	cleanedAccountID string,
) (OnboardingState, error) {
	return s.store.selectOnboardingProvider(s.catalog, expectedRevision, kind, cleanedAccountID)
}

// Existing Store methods remain compatibility entry points bound to the
// production Codex/Claude catalog.
func (s *Store) OnboardingState() (OnboardingState, error) {
	return s.Onboarding(providercatalog.Default()).OnboardingState()
}

func (s *Store) OnboardingSnapshot() (OnboardingSnapshot, error) {
	return s.Onboarding(providercatalog.Default()).OnboardingSnapshot()
}

func (s *Store) OnboardingStagingAccount(
	expectedRevision int64,
) (OnboardingStagingAccount, error) {
	return s.Onboarding(providercatalog.Default()).OnboardingStagingAccount(expectedRevision)
}

func (s *Store) ReplaceOnboardingProviderSelection(
	expectedRevision int64,
	kinds []string,
) (OnboardingSnapshot, error) {
	return s.Onboarding(providercatalog.Default()).
		ReplaceOnboardingProviderSelection(expectedRevision, kinds)
}

func (s *Store) BeginOnboardingProvider(
	expectedRevision int64,
	kind string,
) (OnboardingSnapshot, error) {
	return s.Onboarding(providercatalog.Default()).BeginOnboardingProvider(expectedRevision, kind)
}

func (s *Store) BackOnboardingToProviders(
	expectedRevision int64,
) (OnboardingSnapshot, error) {
	return s.Onboarding(providercatalog.Default()).BackOnboardingToProviders(expectedRevision)
}

func (s *Store) EnsureOnboardingProviderForKind(kind string) error {
	return s.Onboarding(providercatalog.Default()).EnsureOnboardingProviderForKind(kind)
}

func (s *Store) SelectOnboardingProvider(
	expectedRevision int64,
	kind string,
	cleanedAccountID string,
) (OnboardingState, error) {
	return s.Onboarding(providercatalog.Default()).
		SelectOnboardingProvider(expectedRevision, kind, cleanedAccountID)
}

func validateOnboardingCatalog(catalog providercatalog.Catalog) error {
	if _, err := catalog.CanonicalSelectedKinds(nil); err != nil {
		return fmt.Errorf("%w: Provider Catalog is invalid: %v", ErrOnboardingConfigurationChanged, err)
	}
	return nil
}

func onboardingCatalogHasAdvertisedKind(catalog providercatalog.Catalog, kind string) bool {
	option, supported := catalog.Option(kind)
	return supported && option.Advertised
}

func validateOnboardingStateForCatalog(
	catalog providercatalog.Catalog,
	state OnboardingState,
) error {
	if err := validateOnboardingCatalog(catalog); err != nil {
		return err
	}
	if state.ProviderKind == "" {
		return nil
	}
	if !onboardingCatalogHasAdvertisedKind(catalog, state.ProviderKind) {
		return fmt.Errorf(
			"%w: persisted Provider kind %q is outside the active Catalog",
			ErrOnboardingConfigurationChanged,
			state.ProviderKind,
		)
	}
	return nil
}

func validateOnboardingSnapshotForCatalog(
	catalog providercatalog.Catalog,
	snapshot OnboardingSnapshot,
) error {
	if err := validateOnboardingStateForCatalog(catalog, snapshot.State); err != nil {
		return err
	}
	inspection, err := inspectOnboardingProviderStagesForCatalog(catalog, snapshot.Stages)
	if err != nil {
		return err
	}
	if !inspection.positionsCanonical {
		return fmt.Errorf(
			"%w: persisted Provider positions are not canonical for the active Catalog",
			ErrOnboardingConfigurationChanged,
		)
	}
	return nil
}
