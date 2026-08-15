package daemon

import (
	"fmt"

	"agentdc/internal/providercatalog"
)

// appProviderCapability is deliberately independent of CLI descriptors. A CLI
// may be runnable elsewhere in the Portal without being safe for onboarding,
// Connect or subscription routing.
type appProviderCapability struct {
	Onboarding   bool
	Connect      bool
	Subscription bool
}

// OpenCode remains synthetic-test-only until a separately reviewed production
// containment design exists. Keep this deny independent from catalog metadata,
// environment variables and capability-map contents so none can promote it.
func appProviderProductionDenied(kind string) bool {
	return kind == "opencode"
}

var appProviderCapabilities = map[string]appProviderCapability{
	"codex":       {Onboarding: true, Connect: true, Subscription: true},
	"claude-code": {Onboarding: true, Connect: true, Subscription: true},
}

type appProviderOption struct {
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
	Beta        bool   `json:"beta"`
	Advertised  bool   `json:"advertised"`
	RouteRank   int    `json:"route_rank"`
}

func appProviderOptions() []appProviderOption {
	options := providercatalog.Options()
	result := make([]appProviderOption, 0, len(options))
	for _, option := range options {
		if appProviderProductionDenied(option.Kind) {
			continue
		}
		capability, supported := appProviderCapabilities[option.Kind]
		if !option.Advertised || !supported || !capability.Onboarding {
			continue
		}
		result = append(result, appProviderOption{
			Kind:        option.Kind,
			DisplayName: option.DisplayName,
			Description: option.Description,
			Recommended: option.Recommended,
			Beta:        option.Beta,
			Advertised:  option.Advertised,
			RouteRank:   option.RouteRank,
		})
	}
	return result
}

func appProviderSupportsOnboarding(kind string) bool {
	if appProviderProductionDenied(kind) {
		return false
	}
	capability, exists := appProviderCapabilities[kind]
	return exists && capability.Onboarding
}

func appProviderSupportsConnect(kind string) bool {
	if appProviderProductionDenied(kind) {
		return false
	}
	capability, exists := appProviderCapabilities[kind]
	return exists && capability.Connect
}

func appCanonicalOnboardingKinds(kinds []string) ([]string, error) {
	for _, kind := range kinds {
		if appProviderProductionDenied(kind) {
			return nil, fmt.Errorf("%w: Provider is not production-enabled", providercatalog.ErrUnsupportedKind)
		}
	}
	canonical, err := providercatalog.CanonicalSelectedKinds(kinds)
	if err != nil {
		return nil, err
	}
	for _, kind := range canonical {
		if !appProviderSupportsOnboarding(kind) {
			return nil, fmt.Errorf("%w: Provider lacks onboarding capability", providercatalog.ErrUnsupportedKind)
		}
	}
	return canonical, nil
}

func appSubscriptionKinds() map[string]bool {
	result := make(map[string]bool)
	for kind, capability := range appProviderCapabilities {
		if appProviderProductionDenied(kind) {
			continue
		}
		if capability.Subscription {
			result[kind] = true
		}
	}
	return result
}

var subscriptionKinds = appSubscriptionKinds()
