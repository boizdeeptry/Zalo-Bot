package daemon

import (
	"errors"
	"net/http"
	"slices"
	"sync"

	"agentdc/internal/store"
)

// appLLMRouteMutationMu serializes every daemon operation that can replace or
// select the live route. Complete always acquires onboardingMutationMu first;
// no other route operation acquires the onboarding mutex.
var appLLMRouteMutationMu sync.Mutex

const (
	appLLMRouteOperationPut      = "route-put"
	appLLMRouteOperationReplace  = "combo-replace"
	appLLMRouteOperationActivate = "combo-activate"
	appLLMRouteOperationComplete = "onboarding-complete"
	appLLMRouteOperationCapture  = "live-capture"

	appLLMRoutePhaseBeforeLock = "before_lock"
	appLLMRoutePhaseLocked     = "locked"
	appLLMRoutePhaseValidated  = "validated"
	appLLMRoutePhaseCaptured   = "captured"
	appLLMRoutePhaseWritten    = "written"
)

func (ctx appRuntimeContext) appLLMRouteCheckpoint(operation, phase string) {
	if ctx.routeCheckpoint != nil {
		ctx.routeCheckpoint(operation, phase)
	}
}

type appResolvedLiveRouteEntry struct {
	entry        store.LLMRouteEntry
	provider     store.LLMProvider
	registration appProviderRuntimeRegistration
}

func appCanonicalRouteEntries(entries []store.LLMRouteEntry) []store.LLMRouteEntry {
	result := slices.Clone(entries)
	for index := range result {
		result[index].Position = index
	}
	return result
}

// resolveLiveRoute validates a complete route before any runtime callback is
// invoked. Callers hold appLLMRouteMutationMu across the Store reads and use
// the returned registration values as the immutable callback snapshot.
func (ctx appRuntimeContext) resolveLiveRoute(
	entries []store.LLMRouteEntry,
	requireProviderEnabled bool,
) ([]appResolvedLiveRouteEntry, map[string]bool, error) {
	if ctx.api == nil || ctx.api.st == nil || !ctx.registry.valid {
		return nil, nil, errors.New("live Provider runtime context is unavailable")
	}
	providers, err := ctx.api.st.LLMProviders()
	if err != nil {
		return nil, nil, errors.New("live Provider catalog is unavailable")
	}
	byID := make(map[string]store.LLMProvider, len(providers))
	for _, provider := range providers {
		if provider.ID == "" {
			return nil, nil, errors.New("live Provider identity is invalid")
		}
		if _, exists := byID[provider.ID]; exists {
			return nil, nil, errors.New("live Provider identity is duplicated")
		}
		byID[provider.ID] = provider
	}

	resolved := make([]appResolvedLiveRouteEntry, 0, len(entries))
	disabled := make(map[string]bool)
	for index, entry := range entries {
		if entry.Position != index || entry.ProviderID == "" || entry.ModelID == "" {
			return nil, nil, errors.New("live route shape is invalid")
		}
		provider, exists := byID[entry.ProviderID]
		if !exists {
			return nil, nil, errors.New("live route Provider is unavailable")
		}
		registration, exists := ctx.registry.registration(provider.Kind)
		if !exists || registration.Metadata.Kind != provider.Kind {
			return nil, nil, errors.New("live route Provider runtime is unavailable")
		}
		if registration.Terminal != nil && index != len(entries)-1 {
			return nil, nil, errors.New("terminal Provider must be the final route member")
		}
		if index == len(entries)-1 && !entry.Enabled {
			return nil, nil, errors.New("final route member is disabled")
		}
		if !provider.Enabled {
			disabled[provider.ID] = true
			if requireProviderEnabled {
				return nil, nil, errors.New("live route Provider is disabled")
			}
		}
		models, err := ctx.api.st.LLMModels(provider.ID)
		if err != nil {
			return nil, nil, errors.New("live route model catalog is unavailable")
		}
		modelAvailable := false
		for _, model := range models {
			if model.ModelID == entry.ModelID && model.Available {
				modelAvailable = true
				break
			}
		}
		if !modelAvailable {
			return nil, nil, errors.New("live route model is unavailable")
		}
		resolved = append(resolved, appResolvedLiveRouteEntry{
			entry: entry, provider: provider, registration: registration,
		})
	}
	return resolved, disabled, nil
}

func (registry appProviderRuntimeRegistry) validateOnboardingLiveRoute(
	entries []store.OnboardingTestRouteEntry,
) error {
	if !registry.valid || len(entries) == 0 {
		return errors.New("onboarding live route is unavailable")
	}
	for index, entry := range entries {
		if entry.Position != index || entry.Kind == "" || entry.ProviderID == "" || entry.ModelID == "" {
			return errors.New("onboarding live route shape is invalid")
		}
		registration, exists := registry.registration(entry.Kind)
		if !exists || !registrationSupportsOnboarding(registration) {
			return errors.New("onboarding live runtime is unavailable")
		}
		if registration.Terminal != nil && index != len(entries)-1 {
			return errors.New("onboarding terminal Provider is not final")
		}
	}
	return nil
}

func (ctx appRuntimeContext) hasAnyConnectedProvider() bool {
	if ctx.api == nil || ctx.api.st == nil || !ctx.registry.valid {
		return false
	}
	providers, err := ctx.api.st.LLMProviders()
	if err != nil {
		return false
	}

	for _, option := range ctx.registry.managementOptions() {
		if !option.Visible || option.ConnectionMode != appProviderConnectionAccount {
			continue
		}
		kindCount := 0
		exact := false
		for _, provider := range providers {
			if provider.Kind != option.Kind {
				continue
			}
			kindCount++
			if provider.ID == option.Kind {
				exact = true
			}
		}
		if kindCount != 1 || !exact {
			continue
		}
		accounts, err := ctx.api.st.LLMAccounts(option.Kind)
		if err != nil {
			continue
		}
		for _, account := range accounts {
			if account.ProviderID == option.Kind && account.Enabled {
				return true
			}
		}
	}

	for _, provider := range providers {
		registration, exists := ctx.registry.registration(provider.Kind)
		if !exists || registration.Metadata.Visibility != appProviderVisibilityVisible ||
			registration.Metadata.ConnectionMode != appProviderConnectionCredential {
			continue
		}
		if provider.CredentialConfigured {
			return true
		}
	}
	return false
}

// appZaloRunner captures one immutable route/runtime snapshot. Runtime
// factories are invoked only after every member has resolved and validated,
// while the route fence still prevents their Store-backed wiring from mixing
// with a concurrent Complete or route mutation. The fence is released before
// this runner can execute any Provider I/O.
func (ctx appRuntimeContext) appZaloRunner(
	zc zaloConfig,
	base zaloRunner,
	threadID string,
	hasNewFiles bool,
) zaloRunner {
	_ = threadID
	if !ctx.hasAnyConnectedProvider() {
		return silentZaloRunner{}
	}
	ctx.appLLMRouteCheckpoint(appLLMRouteOperationCapture, appLLMRoutePhaseBeforeLock)
	appLLMRouteMutationMu.Lock()
	ctx.appLLMRouteCheckpoint(appLLMRouteOperationCapture, appLLMRoutePhaseLocked)
	defer appLLMRouteMutationMu.Unlock()

	snapshot, err := ctx.api.st.LLMRoute()
	if err != nil {
		ctx.api.logger.Error("llm route: snapshot unavailable", "error_kind", "route_read_failed")
		return silentZaloRunner{}
	}
	if len(snapshot.Entries) == 0 {
		return silentZaloRunner{}
	}
	snapshot.Entries = slices.Clone(snapshot.Entries)
	resolved, disabled, err := ctx.resolveLiveRoute(snapshot.Entries, false)
	if err != nil {
		ctx.api.logger.Error("llm route: runtime snapshot rejected", "error_kind", "runtime_invalid")
		return silentZaloRunner{}
	}
	ctx.appLLMRouteCheckpoint(appLLMRouteOperationCapture, appLLMRoutePhaseValidated)

	client := &http.Client{Timeout: defaultLLMProviderTimeout}
	config := appLLMRunnerConfig{
		Route:               snapshot,
		Store:               ctx.api.st,
		Adapters:            make(map[string]providerAdapter),
		TerminalRoutes:      make(map[string]appTerminalRouteRunner),
		LocalAttachmentRuns: make(map[string]appLocalAttachmentRun),
		StructuredSessions:  make(map[string]appStructuredSessionRunner),
		Disabled:            disabled,
		Credential:          ctx.api.appLLMCredential,
		HasAttachments:      hasNewFiles,
		Logger:              ctx.api.logger,
	}
	_ = base

	localFactories := make(map[string]appLocalAttachmentRunnerFactory)
	for _, member := range resolved {
		providerID := member.entry.ProviderID
		registration := member.registration
		if registration.NewAdapter != nil {
			if _, exists := config.Adapters[providerID]; !exists {
				adapter, factoryErr := registration.NewAdapter(
					providerID, ctx.api.st, client, ctx.api.logger,
				)
				if factoryErr != nil || adapter == nil {
					ctx.api.logger.Error("llm route: adapter factory rejected", "error_kind", "factory_unavailable")
					return silentZaloRunner{}
				}
				config.Adapters[providerID] = adapter
			}
		}
		if registration.Terminal != nil {
			config.TerminalRoutes[providerID] = registration.Terminal
		}
		if registration.NewLocalAttachmentRun != nil {
			localFactories[providerID] = registration.NewLocalAttachmentRun
		}
		if registration.NewStructuredSession != nil {
			structured := registration.NewStructuredSession(ctx.api, zc)
			if structured == nil {
				ctx.api.logger.Error("llm route: structured factory rejected", "error_kind", "factory_unavailable")
				return silentZaloRunner{}
			}
			config.StructuredSessions[providerID] = structured
		}
	}
	runner, ok := newAppLLMRunnerWithLocalFactories(config, localFactories)
	if !ok {
		ctx.api.logger.Error("llm route: attachment factory rejected", "error_kind", "factory_unavailable")
		return silentZaloRunner{}
	}
	ctx.appLLMRouteCheckpoint(appLLMRouteOperationCapture, appLLMRoutePhaseCaptured)
	return runner
}

func (ctx appRuntimeContext) validateRouteMutation(entries []store.LLMRouteEntry) error {
	_, _, err := ctx.resolveLiveRoute(entries, false)
	return err
}

func (ctx appRuntimeContext) comboForActivation(id string) (store.LLMCombo, error) {
	combos, err := ctx.api.st.LLMCombos()
	if err != nil {
		return store.LLMCombo{}, err
	}
	for _, combo := range combos {
		if combo.ID == id {
			return combo, nil
		}
	}
	return store.LLMCombo{}, store.ErrNotFound
}
