package daemon

import (
	"errors"
	"net/http"
	"net/http/httptest"
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
	registry := productionAppProviderRuntimeRegistry()
	for _, kind := range []string{"codex", "claude-code"} {
		if !registry.supportsOnboarding(kind) || !registry.supportsConnect(kind) {
			t.Fatalf("%q must support onboarding and Connect", kind)
		}
	}
	for _, kind := range []string{"", "gemini-cli", "openai", "Codex"} {
		if registry.supportsOnboarding(kind) || registry.supportsConnect(kind) {
			t.Fatalf("%q unexpectedly has onboarding or Connect capability", kind)
		}
	}
	if _, exists := cliDescriptors["gemini-cli"]; !exists {
		t.Fatal("test precondition: gemini-cli CLI descriptor is missing")
	}

	wantSubscriptionKinds := []string{"claude-code", "codex"}
	gotSubscriptionKinds := registry.readinessAccountKinds()
	slices.Sort(gotSubscriptionKinds)
	if !slices.Equal(gotSubscriptionKinds, wantSubscriptionKinds) {
		t.Fatalf("subscription kinds = %q; want %q", gotSubscriptionKinds, wantSubscriptionKinds)
	}
}

func TestAppProviderRegistryCapabilityOwnsDaemonValidation(t *testing.T) {
	registry, err := newAppProviderRuntimeRegistry(
		appRuntimeSyntheticOptions(),
		appRuntimeSyntheticRegistrations(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.supportsOnboarding("future-cli") || !registry.supportsConnect("future-cli") {
		t.Fatal("local immutable registry did not authorize the synthetic runtime")
	}
	canonical, err := registry.canonicalOnboardingKinds([]string{"claude-code", "future-cli", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(canonical, []string{"codex", "future-cli", "claude-code"}) {
		t.Fatalf("canonical kinds = %q", canonical)
	}
	if productionAppProviderRuntimeRegistry().supportsOnboarding("future-cli") {
		t.Fatal("local synthetic registry mutated production validation")
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
	if productionAppProviderRuntimeRegistry().contributesReadiness("opencode") {
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
	if appProviderSupportsOnboarding("opencode") || appProviderSupportsConnect("opencode") {
		t.Fatal("OpenCode bypassed the production deny gate")
	}
	if productionAppProviderRuntimeRegistry().contributesReadiness("opencode") {
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
