package daemon

import (
	"os"
	"slices"
	"testing"

	"agentdc/internal/store"
)

func TestAppProviderRegistryPublishesOnlyOrderedOnboardingOptions(t *testing.T) {
	got := appProviderOptions()
	if len(got) != 2 {
		t.Fatalf("option count = %d; want 2: %+v", len(got), got)
	}
	if got[0].Kind != "codex" || got[1].Kind != "claude-code" {
		t.Fatalf("option order = [%q %q]; want [codex claude-code]", got[0].Kind, got[1].Kind)
	}
	for index, option := range got {
		if option.Kind == "" || option.DisplayName == "" || option.Description == "" {
			t.Fatalf("option %d has incomplete safe metadata: %+v", index, option)
		}
		if !option.Advertised {
			t.Fatalf("published option %d is not advertised: %+v", index, option)
		}
		if index > 0 && got[index-1].RouteRank >= option.RouteRank {
			t.Fatalf("route ranks are not strictly increasing: %+v", got)
		}
	}

	got[0].Kind = "mutated"
	fresh := appProviderOptions()
	if fresh[0].Kind != "codex" {
		t.Fatalf("caller mutated registry copy: %+v", fresh)
	}
}

func TestAppProviderRegistryCapabilitiesDoNotFollowCLIDescriptorPresence(t *testing.T) {
	for _, kind := range []string{"codex", "claude-code"} {
		if !appProviderSupportsOnboarding(kind) || !appProviderSupportsConnect(kind) {
			t.Fatalf("%q must support onboarding and Connect", kind)
		}
	}
	for _, kind := range []string{"", "gemini-cli", "openai", "Codex"} {
		if appProviderSupportsOnboarding(kind) || appProviderSupportsConnect(kind) {
			t.Fatalf("%q unexpectedly has onboarding or Connect capability", kind)
		}
	}
	if _, exists := cliDescriptors["gemini-cli"]; !exists {
		t.Fatal("test precondition: gemini-cli CLI descriptor is missing")
	}

	wantSubscriptionKinds := []string{"claude-code", "codex"}
	gotSubscriptionKinds := make([]string, 0, len(subscriptionKinds))
	for kind, supported := range subscriptionKinds {
		if supported {
			gotSubscriptionKinds = append(gotSubscriptionKinds, kind)
		}
	}
	slices.Sort(gotSubscriptionKinds)
	if !slices.Equal(gotSubscriptionKinds, wantSubscriptionKinds) {
		t.Fatalf("subscription kinds = %q; want %q", gotSubscriptionKinds, wantSubscriptionKinds)
	}
}

func TestAppProviderRegistryCapabilityOwnsDaemonValidation(t *testing.T) {
	const kind = "future-onboarding-driver"
	oldCapability, existed := appProviderCapabilities[kind]
	appProviderCapabilities[kind] = appProviderCapability{Onboarding: true, Connect: true}
	t.Cleanup(func() {
		if existed {
			appProviderCapabilities[kind] = oldCapability
		} else {
			delete(appProviderCapabilities, kind)
		}
	})

	if !appProviderSupportsOnboarding(kind) || !appProviderSupportsConnect(kind) {
		t.Fatalf("test capability for %q was not registered", kind)
	}
	if err := validateOnboardingState(store.OnboardingState{
		Phase: store.OnboardingPhaseConnect, ProviderKind: kind, Revision: 1,
	}); err != nil {
		t.Fatalf("registry-supported future Provider failed daemon state validation: %v", err)
	}

	dataDir := t.TempDir()
	configDir := accountConfigDir(dataDir, kind, "future-account")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := store.OnboardingStagingAccount{
		AccountID: "future-account", ProviderID: kind, ProviderKind: kind, ConfigDir: configDir,
	}
	if err := validateOnboardingStagingConfigDir(dataDir, staged); err != nil {
		t.Fatalf("registry-supported future Provider failed config ownership validation: %v", err)
	}
	if err := removeOwnedOnboardingAccount(dataDir, staged); err != nil {
		t.Fatalf("registry-supported future Provider failed rooted cleanup validation: %v", err)
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatalf("rooted cleanup left future Provider directory: %v", err)
	}
}
