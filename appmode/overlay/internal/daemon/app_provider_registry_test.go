package daemon

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"agentdc/internal/providercatalog"
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

func TestProviderRegistryNeverAdvertisesOpenCodeWithoutContainment(t *testing.T) {
	for _, option := range appProviderOptions() {
		if option.Kind == "opencode" {
			t.Fatal("OpenCode was advertised")
		}
	}
	if appProviderSupportsOnboarding("opencode") || appProviderSupportsConnect("opencode") {
		t.Fatal("OpenCode unexpectedly has a production capability")
	}
	if appSubscriptionKinds()["opencode"] || subscriptionKinds["opencode"] {
		t.Fatal("OpenCode unexpectedly has a subscription route")
	}
	if _, exists := cliDescriptors["opencode"]; exists {
		t.Fatal("OpenCode unexpectedly has a production CLI runner")
	}
	_, err := appCanonicalOnboardingKinds([]string{"opencode"})
	if !errors.Is(err, providercatalog.ErrUnsupportedKind) {
		t.Fatalf("error = %v; want unsupported kind", err)
	}
}

func TestProviderRegistryOpenCodeDenyIgnoresEnvironmentFlagsAndCapabilityMutation(t *testing.T) {
	for _, key := range []string{
		"AGENTDC_OPENCODE_SPIKE_BINARY",
		"AGENTDC_ENABLE_OPENCODE",
		"AGENTDC_OPENCODE_ENABLED",
	} {
		t.Setenv(key, `C:\explicit\opencode.exe`)
	}
	old, existed := appProviderCapabilities["opencode"]
	appProviderCapabilities["opencode"] = appProviderCapability{
		Onboarding: true, Connect: true, Subscription: true,
	}
	t.Cleanup(func() {
		if existed {
			appProviderCapabilities["opencode"] = old
		} else {
			delete(appProviderCapabilities, "opencode")
		}
	})

	if appProviderSupportsOnboarding("opencode") || appProviderSupportsConnect("opencode") {
		t.Fatal("OpenCode bypassed the production deny gate")
	}
	if appSubscriptionKinds()["opencode"] {
		t.Fatal("OpenCode bypassed the subscription deny gate")
	}
	for _, option := range appProviderOptions() {
		if option.Kind == "opencode" {
			t.Fatal("OpenCode environment flag created a catalog option")
		}
	}
}

func TestProviderRegistryHTTPNeverAdvertisesSelectsOrConnectsOpenCode(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	status := env.serve(http.MethodGet, "/onboarding/status", "")
	if status.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d body=%s", status.Code, status.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingStatusWire](t, status)
	for _, option := range got.Options {
		if option.Kind == "opencode" {
			t.Fatal("status endpoint advertised OpenCode")
		}
	}

	selected := env.serve(
		http.MethodPut,
		"/onboarding/providers",
		`{"revision":1,"selected_kinds":["opencode"]}`,
	)
	requireOnboardingCode(
		t,
		selected,
		http.StatusUnprocessableEntity,
		"ONBOARDING_PROVIDER_UNSUPPORTED",
	)
	if state := env.state(t); state.Revision != 1 || state.Phase != store.OnboardingPhaseProvider {
		t.Fatalf("OpenCode selection mutated onboarding state: %+v", state)
	}

	connect := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/llm/providers/opencode/connect", nil)
	request.SetPathValue("kind", "opencode")
	env.a.handleLLMConnectStart(connect, request)
	if connect.Code != http.StatusBadRequest {
		t.Fatalf("OpenCode Connect = %d body=%s", connect.Code, connect.Body.String())
	}
}
