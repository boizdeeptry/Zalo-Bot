package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"

	"agentdc/internal/providercatalog"
	"agentdc/internal/store"
)

type appRuntimeNoopConnectDriver struct{}

func (appRuntimeNoopConnectDriver) Detect() (bool, error) { return true, nil }
func (appRuntimeNoopConnectDriver) Install(context.Context, func(string)) error {
	return nil
}
func (appRuntimeNoopConnectDriver) Login(
	context.Context,
	string,
) (string, string, func() error, error) {
	return "", "", func() error { return nil }, nil
}
func (appRuntimeNoopConnectDriver) PollAuth(string) authState  { return authLoggedIn }
func (appRuntimeNoopConnectDriver) AccountLabel(string) string { return "Future account" }

func appRuntimeSyntheticOptions() []providercatalog.Option {
	return []providercatalog.Option{
		{
			Kind: "claude-code", DisplayName: "Claude Code",
			Description: "Kết nối tài khoản Claude Code trên máy này",
			Advertised:  true, RouteRank: 100,
		},
		{
			Kind: "future-cli", DisplayName: "Future CLI",
			Description: "Kết nối runtime thử nghiệm an toàn",
			Advertised:  true, RouteRank: 50,
		},
		{
			Kind: "codex", DisplayName: "Codex",
			Description: "Kết nối tài khoản ChatGPT/Codex trên máy này",
			Recommended: true, Advertised: true, RouteRank: 10,
		},
	}
}

func appRuntimeFutureRegistration() appProviderRuntimeRegistration {
	return appProviderRuntimeRegistration{
		Metadata: appProviderMetadata{
			Kind: "future-cli", DisplayName: "Future CLI",
			Description: "Kết nối runtime thử nghiệm an toàn",
			Group:       appProviderGroupSubscription, Prefix: "fc", ThemeColor: "#334455",
			UIOrder: 15, CatalogRank: 50,
			Visibility:       appProviderVisibilityVisible,
			ConnectionMode:   appProviderConnectionAccount,
			ExecutionMode:    appProviderExecutionLocal,
			AttachmentPolicy: appProviderAttachmentNone,
		},
		Connect:               func(*slog.Logger) appConnectDriver { return appRuntimeNoopConnectDriver{} },
		EnsureAccountProvider: func(*store.Store) error { return nil },
		SeedConnectedModels:   func(*store.Store, string) error { return nil },
		SelectOnboardingModel: func(*store.Store, string) (string, error) {
			return "future-model", nil
		},
		NewOnboardingMember: func(
			*api,
			store.OnboardingTestRouteEntry,
		) (appOnboardingMemberRun, error) {
			return func(context.Context, string, func(string)) (string, error) {
				return "Future answer", nil
			}, nil
		},
		NewAdapter: func(
			string,
			*store.Store,
			*http.Client,
			*slog.Logger,
		) (providerAdapter, error) {
			return nil, nil
		},
	}
}

func appRuntimeSyntheticRegistrations() []appProviderRuntimeRegistration {
	registrations := appProductionProviderRuntimeRegistrations()
	return append(registrations, appRuntimeFutureRegistration())
}

func appRuntimeRegistrationIndex(
	t *testing.T,
	registrations []appProviderRuntimeRegistration,
	kind string,
) int {
	t.Helper()
	for index := range registrations {
		if registrations[index].Metadata.Kind == kind {
			return index
		}
	}
	t.Fatalf("registration %q not found", kind)
	return -1
}

func appRuntimeTerminalCallback(
	*appLLMRunner,
	context.Context,
	store.LLMRouteEntry,
	appLLMRouteInput,
	func(string),
) (appZaloRunResult, error) {
	return appZaloRunResult{}, nil
}

func appRuntimeAttachmentFactory(*appLLMRunner) appLocalAttachmentRun {
	return func(
		*appLLMRunner,
		context.Context,
		store.LLMRouteEntry,
		appLLMRouteInput,
		func(string),
	) (appZaloRunResult, error) {
		return appZaloRunResult{}, nil
	}
}

func appRuntimeStructuredFactory(*api, zaloConfig) appStructuredSessionRunner {
	return func(
		context.Context,
		string,
		*store.ZaloCLISession,
	) (zaloRunner, appZaloClaudeBinding) {
		return silentZaloRunner{}, appZaloClaudeBinding{}
	}
}

func TestAppProviderRuntimeRegistryProductionCompatibility(t *testing.T) {
	registry := productionAppProviderRuntimeRegistry()
	options := registry.onboardingOptions()
	if got := []string{options[0].Kind, options[1].Kind}; !slices.Equal(got, []string{"codex", "claude-code"}) {
		t.Fatalf("onboarding options = %q; want [codex claude-code]", got)
	}

	management := registry.managementOptions()
	wantKinds := []string{
		"codex", "claude-code", "openai", "anthropic", "gemini", "openrouter", "gemini-cli",
	}
	gotKinds := make([]string, len(management))
	for index, option := range management {
		gotKinds[index] = option.Kind
		if option.Kind == "gemini-cli" {
			if option.Visible || option.ConnectionMode != appProviderConnectionNone || option.Connectable {
				t.Fatalf("hidden Gemini CLI metadata = %+v", option)
			}
		} else if !option.Visible {
			t.Fatalf("production option %q unexpectedly hidden: %+v", option.Kind, option)
		}
	}
	if !slices.Equal(gotKinds, wantKinds) {
		t.Fatalf("management kinds = %q; want %q", gotKinds, wantKinds)
	}

	for _, kind := range []string{"codex", "claude-code"} {
		if !registry.supportsOnboarding(kind) || !registry.supportsConnect(kind) {
			t.Fatalf("%q must support onboarding and Connect", kind)
		}
		if _, ok := registry.connectDriverFactory(kind); !ok {
			t.Fatalf("%q Connect factory is missing", kind)
		}
		if _, ok := registry.accountProviderStrategy(kind); !ok {
			t.Fatalf("%q account strategy is missing", kind)
		}
		if _, ok := registry.connectedModelSeeder(kind); !ok {
			t.Fatalf("%q model seeder is missing", kind)
		}
	}
	if _, ok := registry.providerAdapterFactory("codex"); !ok {
		t.Fatal("Codex standard adapter factory is missing")
	}
	if _, ok := registry.terminalRouteRunner("claude-code"); !ok {
		t.Fatal("Claude terminal runner is missing")
	}
	if _, ok := registry.localAttachmentRunnerFactory("claude-code"); !ok {
		t.Fatal("Claude typed attachment factory is missing")
	}
	if _, ok := registry.structuredSessionRunnerFactory("claude-code"); !ok {
		t.Fatal("Claude structured-session factory is missing")
	}
	if _, ok := registry.providerAdapterFactory("gemini-cli"); !ok {
		t.Fatal("hidden Gemini CLI adapter factory is missing")
	}
	if _, ok := registry.localAttachmentRunnerFactory("gemini-cli"); !ok {
		t.Fatal("hidden Gemini CLI attachment factory is missing")
	}
	if registry.supportsConnect("gemini-cli") || registry.contributesReadiness("gemini-cli") {
		t.Fatal("hidden Gemini CLI contributed Connect or readiness")
	}
}

func TestAppProviderRuntimeRegistryAcceptsSyntheticRankedRegistration(t *testing.T) {
	registry, err := newAppProviderRuntimeRegistry(
		appRuntimeSyntheticOptions(),
		appRuntimeSyntheticRegistrations(),
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := registry.canonicalOnboardingKinds([]string{"claude-code", "future-cli", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"codex", "future-cli", "claude-code"}) {
		t.Fatalf("canonical kinds = %q", got)
	}
	if !registry.supportsOnboarding("future-cli") || !registry.supportsConnect("future-cli") {
		t.Fatal("synthetic runtime did not publish its validated capabilities")
	}
	if _, ok := registry.onboardingMemberFactory("future-cli"); !ok {
		t.Fatal("synthetic member factory is missing")
	}
	if _, ok := registry.providerAdapterFactory("future-cli"); !ok {
		t.Fatal("synthetic adapter factory is missing")
	}

	option, ok := registry.Catalog().Option("future-cli")
	if !ok || option.RouteRank != 50 {
		t.Fatalf("synthetic Catalog option = %+v, present=%v", option, ok)
	}
	metadata, ok := registry.managementOption("future-cli")
	if !ok || metadata.ExecutionMode != appProviderExecutionLocal || !metadata.Visible {
		t.Fatalf("synthetic management metadata = %+v, present=%v", metadata, ok)
	}
}

func TestAppProviderRuntimeRegistryRejectsInvalidRegistration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*[]providercatalog.Option, *[]appProviderRuntimeRegistration)
	}{
		{"zero catalog", func(options *[]providercatalog.Option, _ *[]appProviderRuntimeRegistration) {
			*options = nil
		}},
		{"duplicate kind", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			*registrations = append(*registrations, appRuntimeFutureRegistration())
		}},
		{"duplicate UI order", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			future := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			codex := appRuntimeRegistrationIndex(t, *registrations, "codex")
			(*registrations)[future].Metadata.UIOrder = (*registrations)[codex].Metadata.UIOrder
		}},
		{"duplicate catalog rank", func(options *[]providercatalog.Option, _ *[]appProviderRuntimeRegistration) {
			(*options)[1].RouteRank = 10
		}},
		{"missing registration", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			*registrations = append((*registrations)[:index], (*registrations)[index+1:]...)
		}},
		{"catalog display mismatch", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Metadata.DisplayName = "Different"
		}},
		{"catalog description mismatch", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Metadata.Description = "Different"
		}},
		{"catalog beta mismatch", func(options *[]providercatalog.Option, _ *[]appProviderRuntimeRegistration) {
			(*options)[1].Beta = true
		}},
		{"catalog rank mismatch", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Metadata.CatalogRank = 49
		}},
		{"account missing Connect", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Connect = nil
		}},
		{"account missing model seeder", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].SeedConnectedModels = nil
		}},
		{"account missing account strategy", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].EnsureAccountProvider = nil
		}},
		{"account missing model selector", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].SelectOnboardingModel = nil
		}},
		{"account missing member factory", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].NewOnboardingMember = nil
		}},
		{"credential missing compiled adapter", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].NewAdapter = nil
		}},
		{"visible connection none", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Metadata.ConnectionMode = appProviderConnectionNone
		}},
		{"hidden account capabilities", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Metadata.Visibility = appProviderVisibilityHidden
			(*registrations)[index].Metadata.ConnectionMode = appProviderConnectionNone
		}},
		{"unspecified visibility", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Metadata.Visibility = appProviderVisibilityUnspecified
		}},
		{"both standard and terminal path", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Terminal = appRuntimeTerminalCallback
		}},
		{"no live path", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].NewAdapter = nil
		}},
		{"attachment claim missing typed factory", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].Metadata.AttachmentPolicy = appProviderAttachmentLocal
		}},
		{"attachment factory missing typed claim", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].NewLocalAttachmentRun = appRuntimeAttachmentFactory
		}},
		{"structured factory without terminal", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].NewStructuredSession = appRuntimeStructuredFactory
		}},
		{"terminal before another catalog runtime", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "future-cli")
			(*registrations)[index].NewAdapter = nil
			(*registrations)[index].Terminal = appRuntimeTerminalCallback
		}},
		{"invalid kind", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].Metadata.Kind = "OpenAI"
		}},
		{"invalid group", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].Metadata.Group = appProviderGroup("other")
		}},
		{"invalid prefix", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].Metadata.Prefix = "../oa"
		}},
		{"invalid theme", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].Metadata.ThemeColor = "green"
		}},
		{"invalid UI order", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].Metadata.UIOrder = -1
		}},
		{"invalid connection mode", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].Metadata.ConnectionMode = appProviderConnectionMode("url")
		}},
		{"invalid execution mode", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].Metadata.ExecutionMode = appProviderExecutionMode("shell")
		}},
		{"invalid attachment policy", func(_ *[]providercatalog.Option, registrations *[]appProviderRuntimeRegistration) {
			index := appRuntimeRegistrationIndex(t, *registrations, "openai")
			(*registrations)[index].Metadata.AttachmentPolicy = appProviderAttachmentPolicy("remote")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := slices.Clone(appRuntimeSyntheticOptions())
			registrations := slices.Clone(appRuntimeSyntheticRegistrations())
			test.mutate(&options, &registrations)
			if _, err := newAppProviderRuntimeRegistry(options, registrations); err == nil {
				t.Fatal("invalid operational registry was accepted")
			}
		})
	}
}

func TestAppProviderRuntimeRegistryOpenCodeDeniedAtConstructorAndEveryAccessor(t *testing.T) {
	options := slices.Clone(appRuntimeSyntheticOptions())
	options = append(options, providercatalog.Option{
		Kind: "opencode", DisplayName: "OpenCode", Description: "Synthetic only",
		Advertised: true, RouteRank: 200,
	})
	registration := appRuntimeFutureRegistration()
	registration.Metadata.Kind = "opencode"
	registration.Metadata.DisplayName = "OpenCode"
	registration.Metadata.Description = "Synthetic only"
	registration.Metadata.Prefix = "oc"
	registration.Metadata.UIOrder = 200
	registration.Metadata.CatalogRank = 200
	registrations := append(appRuntimeSyntheticRegistrations(), registration)
	if _, err := newAppProviderRuntimeRegistry(options, registrations); err == nil {
		t.Fatal("fully populated OpenCode registration was accepted")
	}

	registry := productionAppProviderRuntimeRegistry()
	checks := map[string]bool{}
	_, checks["registration"] = registry.registration("opencode")
	_, checks["management option"] = registry.managementOption("opencode")
	_, checks["Connect"] = registry.connectDriverFactory("opencode")
	_, checks["account strategy"] = registry.accountProviderStrategy("opencode")
	_, checks["model seeder"] = registry.connectedModelSeeder("opencode")
	_, checks["model selector"] = registry.onboardingModelSelector("opencode")
	_, checks["member factory"] = registry.onboardingMemberFactory("opencode")
	_, checks["adapter factory"] = registry.providerAdapterFactory("opencode")
	_, checks["attachment factory"] = registry.localAttachmentRunnerFactory("opencode")
	_, checks["terminal runner"] = registry.terminalRouteRunner("opencode")
	_, checks["structured factory"] = registry.structuredSessionRunnerFactory("opencode")
	for name, exposed := range checks {
		if exposed {
			t.Errorf("OpenCode exposed through %s accessor", name)
		}
	}
	if registry.supportsOnboarding("opencode") || registry.supportsConnect("opencode") ||
		registry.contributesReadiness("opencode") {
		t.Fatal("OpenCode exposed through a capability projection")
	}
	if _, err := registry.canonicalOnboardingKinds([]string{"opencode"}); !errors.Is(err, providercatalog.ErrUnsupportedKind) {
		t.Fatalf("canonical OpenCode error = %v; want unsupported kind", err)
	}
	for _, option := range registry.managementOptions() {
		if option.Kind == "opencode" {
			t.Fatal("OpenCode exposed through management projection")
		}
	}
}

func TestAppProviderRuntimeRegistryForgedOpenCodeDeniedAtEveryAccessor(t *testing.T) {
	production := productionAppProviderRuntimeRegistry()
	options := append(providercatalog.Default().Options(), providercatalog.Option{
		Kind: "opencode", DisplayName: "OpenCode", Description: "Synthetic only",
		Advertised: true, RouteRank: 200,
	})
	poisonedCatalog, err := providercatalog.New(options)
	if err != nil {
		t.Fatal(err)
	}
	opencode := appRuntimeFutureRegistration()
	opencode.Metadata.Kind = "opencode"
	opencode.Metadata.DisplayName = "OpenCode"
	opencode.Metadata.Description = "Synthetic only"
	opencode.Metadata.Prefix = "oc"
	opencode.Metadata.UIOrder = 200
	opencode.Metadata.CatalogRank = 200
	opencode.Metadata.AttachmentPolicy = appProviderAttachmentLocal
	opencode.NewLocalAttachmentRun = appRuntimeAttachmentFactory
	opencode.Terminal = appRuntimeTerminalCallback
	opencode.NewStructuredSession = appRuntimeStructuredFactory

	byKind := make(map[string]appProviderRuntimeRegistration, len(production.byKind)+1)
	for kind, registration := range production.byKind {
		byKind[kind] = registration
	}
	byKind["opencode"] = opencode
	management := append(slices.Clone(production.management), opencode.Metadata.managementOption())
	poisoned := appProviderRuntimeRegistry{
		valid: true, catalog: poisonedCatalog, byKind: byKind, management: management,
	}

	if exposed := poisoned.Catalog().Options(); len(exposed) != 0 {
		t.Errorf("Catalog exposed forged denied runtime: %+v", exposed)
	}
	for _, option := range poisoned.managementOptions() {
		if option.Kind == "opencode" {
			t.Error("management projection exposed forged denied runtime")
		}
	}
	for _, option := range poisoned.onboardingOptions() {
		if option.Kind == "opencode" {
			t.Error("onboarding projection exposed forged denied runtime")
		}
	}
	for _, kind := range poisoned.readinessAccountKinds() {
		if kind == "opencode" {
			t.Error("readiness projection exposed forged denied runtime")
		}
	}
	if _, err := poisoned.canonicalOnboardingKinds([]string{"codex"}); err == nil {
		t.Error("canonicalization trusted a Catalog poisoned by a denied runtime")
	}
	if _, err := poisoned.canonicalOnboardingKinds([]string{"opencode"}); !errors.Is(err, providercatalog.ErrUnsupportedKind) {
		t.Errorf("canonical denied runtime error = %v; want unsupported kind", err)
	}

	checks := map[string]bool{}
	_, checks["registration"] = poisoned.registration("opencode")
	_, checks["management option"] = poisoned.managementOption("opencode")
	checks["onboarding capability"] = poisoned.supportsOnboarding("opencode")
	checks["Connect capability"] = poisoned.supportsConnect("opencode")
	checks["readiness"] = poisoned.contributesReadiness("opencode")
	checks["credential readiness"] = poisoned.contributesCredentialReadiness("opencode")
	_, checks["Connect factory"] = poisoned.connectDriverFactory("opencode")
	_, checks["account strategy"] = poisoned.accountProviderStrategy("opencode")
	_, checks["model seeder"] = poisoned.connectedModelSeeder("opencode")
	_, checks["model selector"] = poisoned.onboardingModelSelector("opencode")
	_, checks["member factory"] = poisoned.onboardingMemberFactory("opencode")
	_, checks["adapter factory"] = poisoned.providerAdapterFactory("opencode")
	_, checks["attachment factory"] = poisoned.localAttachmentRunnerFactory("opencode")
	_, checks["terminal runner"] = poisoned.terminalRouteRunner("opencode")
	_, checks["structured factory"] = poisoned.structuredSessionRunnerFactory("opencode")
	for name, exposed := range checks {
		if exposed {
			t.Errorf("forged OpenCode exposed through %s", name)
		}
	}
}

func TestAppProviderRuntimeRegistryDefensiveCopiesAndConcurrentReads(t *testing.T) {
	options := appRuntimeSyntheticOptions()
	registrations := appRuntimeSyntheticRegistrations()
	registry, err := newAppProviderRuntimeRegistry(options, registrations)
	if err != nil {
		t.Fatal(err)
	}

	options[1].Kind = "mutated"
	future := appRuntimeRegistrationIndex(t, registrations, "future-cli")
	registrations[future].Metadata.DisplayName = "Mutated"
	registrations[future].Connect = nil

	projected := registry.managementOptions()
	projected[0].Kind = "mutated"
	catalogOptions := registry.Catalog().Options()
	catalogOptions[0].Kind = "mutated"
	metadata, ok := registry.managementOption("future-cli")
	if !ok || metadata.DisplayName != "Future CLI" {
		t.Fatalf("input mutation reached registry: %+v, present=%v", metadata, ok)
	}
	if _, ok := registry.connectDriverFactory("future-cli"); !ok {
		t.Fatal("input callback mutation reached registry")
	}
	if got := registry.managementOptions()[0].Kind; got != "codex" {
		t.Fatalf("projection mutation reached registry: first kind = %q", got)
	}
	if got := registry.Catalog().Options()[0].Kind; got != "codex" {
		t.Fatalf("Catalog output mutation reached registry: first kind = %q", got)
	}

	const readers = 32
	var wait sync.WaitGroup
	errorsFound := make(chan error, readers)
	for reader := 0; reader < readers; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				canonical, err := registry.canonicalOnboardingKinds(
					[]string{"claude-code", "future-cli", "codex"},
				)
				if err != nil || !slices.Equal(canonical, []string{"codex", "future-cli", "claude-code"}) {
					errorsFound <- fmt.Errorf("canonical=%q err=%v", canonical, err)
					return
				}
				if option, ok := registry.managementOption("future-cli"); !ok || option.Kind != "future-cli" {
					errorsFound <- fmt.Errorf("option=%+v present=%v", option, ok)
					return
				}
				if _, ok := registry.providerAdapterFactory("future-cli"); !ok {
					errorsFound <- errors.New("future adapter missing")
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func TestAppProviderRuntimeRegistrySyntheticDoesNotMutateProduction(t *testing.T) {
	before := productionAppProviderRuntimeRegistry()
	beforeOptions := before.managementOptions()
	if _, err := newAppProviderRuntimeRegistry(
		appRuntimeSyntheticOptions(),
		appRuntimeSyntheticRegistrations(),
	); err != nil {
		t.Fatal(err)
	}
	after := productionAppProviderRuntimeRegistry()
	if !reflect.DeepEqual(after.managementOptions(), beforeOptions) {
		t.Fatalf("production metadata changed:\nbefore=%+v\nafter=%+v", beforeOptions, after.managementOptions())
	}
	if after.supportsOnboarding("future-cli") || after.supportsConnect("future-cli") {
		t.Fatal("synthetic registration leaked into production")
	}
	if _, ok := after.providerAdapterFactory("future-cli"); ok {
		t.Fatal("synthetic factory leaked into production")
	}
}

func TestAppProviderRuntimeRegistryHiddenAndDeniedRowsDoNotContributeReadiness(t *testing.T) {
	for _, kind := range []string{"gemini-cli", "opencode", "unknown-provider"} {
		t.Run(kind, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			if err := env.a.st.EnsureAccountRuntimeProvider(kind, "Hidden runtime"); err != nil {
				t.Fatal(err)
			}
			if err := env.a.st.CreateLLMAccount(store.LLMAccount{
				ID: "account-1", ProviderID: kind, Label: "Hidden", ConfigDir: t.TempDir(), Enabled: true,
			}); err != nil {
				t.Fatal(err)
			}
			if err := env.a.st.SetLLMCredentialCipher(kind, []byte("not-a-real-credential")); err != nil {
				t.Fatal(err)
			}
			if env.a.hasAnyConnectedProvider() {
				t.Fatalf("%q contributed readiness through account or credential", kind)
			}
		})
	}

	t.Run("visible credential runtime", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		if err := env.a.st.CreateLLMProvider(store.LLMProvider{
			ID: "openai-1", Kind: "openai", Name: "OpenAI", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		if err := env.a.st.SetLLMCredentialCipher("openai-1", []byte("not-a-real-credential")); err != nil {
			t.Fatal(err)
		}
		if !env.a.hasAnyConnectedProvider() {
			t.Fatal("visible credential runtime did not contribute readiness")
		}
	})
}

func TestAppProviderRuntimeRegistryContextUsesCatalogAndPassedConnectManager(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	registry, err := newAppProviderRuntimeRegistry(
		appRuntimeSyntheticOptions(),
		appRuntimeSyntheticRegistrations(),
	)
	if err != nil {
		t.Fatal(err)
	}
	manager := &connectManager{}
	ctx := appRuntimeContext{api: env.a, registry: registry, connect: manager}
	snapshot, err := ctx.onboardingStore().ReplaceOnboardingProviderSelection(
		1,
		[]string{"future-cli", "codex"},
	)
	if err != nil {
		t.Fatalf("context-bound Onboarding Store rejected synthetic catalog: %v", err)
	}
	if got := []string{snapshot.Stages[0].Kind, snapshot.Stages[1].Kind}; !slices.Equal(got, []string{"codex", "future-cli"}) {
		t.Fatalf("context-bound stages = %q", got)
	}

	previous := connectMgr
	t.Cleanup(func() { connectMgr = previous })
	mux := http.NewServeMux()
	registerAppRoutesWithContext(mux, ctx)
	if connectMgr != manager {
		t.Fatal("context route registration rebuilt or replaced the passed Connect manager")
	}
}
