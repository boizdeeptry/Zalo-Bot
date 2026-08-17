package daemon

// OpenCode remains synthetic-test-only until a separately reviewed production
// containment design exists. Keep this deny independent from catalog metadata,
// environment variables and capability-map contents so none can promote it.
func appProviderProductionDenied(kind string) bool {
	return kind == "opencode"
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
	return productionAppProviderRuntimeRegistry().onboardingOptions()
}

func appProviderSupportsOnboarding(kind string) bool {
	return productionAppProviderRuntimeRegistry().supportsOnboarding(kind)
}

func appProviderSupportsConnect(kind string) bool {
	return productionAppProviderRuntimeRegistry().supportsConnect(kind)
}

func appCanonicalOnboardingKinds(kinds []string) ([]string, error) {
	return productionAppProviderRuntimeRegistry().canonicalOnboardingKinds(kinds)
}
