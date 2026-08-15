package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"agentdc/internal/providercatalog"
	"agentdc/internal/store"
)

const (
	appProviderMetadataMaxDisplayBytes     = 128
	appProviderMetadataMaxDescriptionBytes = 1024
	appProviderMetadataMaxPrefixBytes      = 8
	appProviderMetadataMaxOrder            = int64(1<<31 - 1)
)

var errAppProviderRuntimeRegistry = errors.New("invalid provider runtime registry")

type appProviderGroup string

const (
	appProviderGroupSubscription appProviderGroup = "subscription"
	appProviderGroupAPIKey       appProviderGroup = "apikey"
)

type appProviderVisibility string

const (
	appProviderVisibilityUnspecified appProviderVisibility = ""
	appProviderVisibilityVisible     appProviderVisibility = "visible"
	appProviderVisibilityHidden      appProviderVisibility = "hidden"
)

type appProviderConnectionMode string

const (
	appProviderConnectionAccount    appProviderConnectionMode = "account"
	appProviderConnectionCredential appProviderConnectionMode = "credential"
	appProviderConnectionNone       appProviderConnectionMode = "none"
)

type appProviderExecutionMode string

const (
	appProviderExecutionAPI         appProviderExecutionMode = "api"
	appProviderExecutionProxy       appProviderExecutionMode = "proxy"
	appProviderExecutionOfficialCLI appProviderExecutionMode = "official_cli"
	appProviderExecutionLocal       appProviderExecutionMode = "local"
)

// appProviderAttachmentPolicy is an explicit typed capability declaration.
// A local claim is accepted only when a typed local runner factory accompanies
// it; a Boolean cannot accidentally grant file access.
type appProviderAttachmentPolicy string

const (
	appProviderAttachmentNone  appProviderAttachmentPolicy = "none"
	appProviderAttachmentLocal appProviderAttachmentPolicy = "local"
)

// appProviderMetadata contains only bounded management/UI values. CatalogRank
// is internal validation data and is deliberately absent from the HTTP
// management projection.
type appProviderMetadata struct {
	Kind             string
	DisplayName      string
	Description      string
	Group            appProviderGroup
	Prefix           string
	ThemeColor       string
	UIOrder          int
	CatalogRank      int
	Recommended      bool
	Beta             bool
	Visibility       appProviderVisibility
	ConnectionMode   appProviderConnectionMode
	ExecutionMode    appProviderExecutionMode
	AttachmentPolicy appProviderAttachmentPolicy
}

type appProviderManagementOption struct {
	Kind           string                    `json:"kind"`
	DisplayName    string                    `json:"display_name"`
	Description    string                    `json:"description"`
	Group          appProviderGroup          `json:"group"`
	Connectable    bool                      `json:"connectable"`
	ConnectionMode appProviderConnectionMode `json:"connection_mode"`
	ExecutionMode  appProviderExecutionMode  `json:"execution_mode"`
	Visible        bool                      `json:"visible"`
	Prefix         string                    `json:"prefix"`
	ThemeColor     string                    `json:"theme_color"`
	Beta           bool                      `json:"beta"`
	UIOrder        int                       `json:"ui_order"`
}

type appConnectDriver interface {
	Detect() (bool, error)
	Install(context.Context, func(string)) error
	Login(context.Context, string) (url, code string, wait func() error, err error)
	PollAuth(string) authState
	AccountLabel(string) string
}

type appConnectDriverFactory func(*slog.Logger) appConnectDriver
type appAccountProviderStrategy func(*store.Store) error
type appConnectedModelSeeder func(*store.Store, string) error
type appOnboardingModelSelector func(*store.Store, string) (string, error)
type appOnboardingMemberRun func(context.Context, string, func(string)) (string, error)
type appOnboardingMemberFactory func(
	*api,
	store.OnboardingTestRouteEntry,
) (appOnboardingMemberRun, error)
type appProviderAdapterFactory func(
	providerID string,
	st *store.Store,
	client *http.Client,
	logger *slog.Logger,
) (providerAdapter, error)
type appTerminalRouteRunner func(
	*appLLMRunner,
	context.Context,
	store.LLMRouteEntry,
	appLLMRouteInput,
	func(string),
) (appZaloRunResult, error)
type appLocalAttachmentRun func(
	context.Context,
	store.LLMRouteEntry,
	appLLMRouteInput,
	func(string),
) (appZaloRunResult, error)
type appLocalAttachmentRunnerFactory func(*appLLMRunner) appLocalAttachmentRun
type appStructuredSessionRunner func(
	context.Context,
	string,
	*store.ZaloCLISession,
) (zaloRunner, appZaloClaudeBinding)
type appStructuredSessionRunnerFactory func(*api, zaloConfig) appStructuredSessionRunner

type appProviderRuntimeRegistration struct {
	Metadata              appProviderMetadata
	Connect               appConnectDriverFactory
	EnsureAccountProvider appAccountProviderStrategy
	SeedConnectedModels   appConnectedModelSeeder
	SelectOnboardingModel appOnboardingModelSelector
	NewOnboardingMember   appOnboardingMemberFactory
	NewAdapter            appProviderAdapterFactory
	NewLocalAttachmentRun appLocalAttachmentRunnerFactory
	Terminal              appTerminalRouteRunner
	NewStructuredSession  appStructuredSessionRunnerFactory
}

// appProviderRuntimeRegistry is immutable after construction. Reference-backed
// state is private, no accessor returns a map or the owned option slice, and all
// methods are read-only, so independent registries can be read concurrently.
type appProviderRuntimeRegistry struct {
	valid      bool
	catalog    providercatalog.Catalog
	byKind     map[string]appProviderRuntimeRegistration
	management []appProviderManagementOption
}

func newAppProviderRuntimeRegistry(
	options []providercatalog.Option,
	registrations []appProviderRuntimeRegistration,
) (appProviderRuntimeRegistry, error) {
	if len(options) == 0 {
		return appProviderRuntimeRegistry{}, fmt.Errorf("%w: onboarding Catalog is empty", errAppProviderRuntimeRegistry)
	}
	for _, option := range options {
		if appProviderProductionDenied(option.Kind) {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: Provider %q is production-denied",
				errAppProviderRuntimeRegistry,
				option.Kind,
			)
		}
	}
	catalog, err := providercatalog.New(slices.Clone(options))
	if err != nil {
		return appProviderRuntimeRegistry{}, fmt.Errorf("%w: %v", errAppProviderRuntimeRegistry, err)
	}

	owned := slices.Clone(registrations)
	byKind := make(map[string]appProviderRuntimeRegistration, len(owned))
	seenUIOrders := make(map[int]string, len(owned))
	for _, registration := range owned {
		metadata := registration.Metadata
		if appProviderProductionDenied(metadata.Kind) {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: Provider %q is production-denied",
				errAppProviderRuntimeRegistry,
				metadata.Kind,
			)
		}
		if err := validateAppProviderMetadata(metadata); err != nil {
			return appProviderRuntimeRegistry{}, err
		}
		if _, exists := byKind[metadata.Kind]; exists {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: duplicate Provider kind %q",
				errAppProviderRuntimeRegistry,
				metadata.Kind,
			)
		}
		if previous, exists := seenUIOrders[metadata.UIOrder]; exists {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: UI order %d is shared by %q and %q",
				errAppProviderRuntimeRegistry,
				metadata.UIOrder,
				previous,
				metadata.Kind,
			)
		}
		seenUIOrders[metadata.UIOrder] = metadata.Kind
		if err := validateAppProviderRuntimeDependencies(registration); err != nil {
			return appProviderRuntimeRegistry{}, err
		}
		byKind[metadata.Kind] = registration
	}

	catalogOptions := catalog.Options()
	advertised := make([]providercatalog.Option, 0, len(catalogOptions))
	for _, option := range catalogOptions {
		if !option.Advertised {
			continue
		}
		advertised = append(advertised, option)
		registration, exists := byKind[option.Kind]
		if !exists {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: advertised Provider %q has no runtime registration",
				errAppProviderRuntimeRegistry,
				option.Kind,
			)
		}
		metadata := registration.Metadata
		if metadata.DisplayName != option.DisplayName || metadata.Description != option.Description ||
			metadata.Recommended != option.Recommended || metadata.Beta != option.Beta ||
			metadata.CatalogRank != option.RouteRank {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: runtime metadata does not match Catalog option %q",
				errAppProviderRuntimeRegistry,
				option.Kind,
			)
		}
		if !registrationSupportsOnboarding(registration) {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: advertised Provider %q has incomplete onboarding behavior",
				errAppProviderRuntimeRegistry,
				option.Kind,
			)
		}
	}

	for _, registration := range owned {
		metadata := registration.Metadata
		option, inCatalog := catalog.Option(metadata.Kind)
		if registrationHasOnboardingBehavior(registration) {
			if !inCatalog || !option.Advertised {
				return appProviderRuntimeRegistry{}, fmt.Errorf(
					"%w: onboarding runtime %q is absent from advertised Catalog",
					errAppProviderRuntimeRegistry,
					metadata.Kind,
				)
			}
		} else if metadata.CatalogRank != 0 {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: non-onboarding runtime %q declares a Catalog rank",
				errAppProviderRuntimeRegistry,
				metadata.Kind,
			)
		}
	}

	for index, option := range advertised {
		registration := byKind[option.Kind]
		if registration.Terminal != nil && index != len(advertised)-1 {
			return appProviderRuntimeRegistry{}, fmt.Errorf(
				"%w: terminal Provider %q is not the final Catalog route",
				errAppProviderRuntimeRegistry,
				option.Kind,
			)
		}
	}

	management := make([]appProviderManagementOption, 0, len(owned))
	for _, registration := range owned {
		management = append(management, registration.Metadata.managementOption())
	}
	sort.Slice(management, func(i, j int) bool { return management[i].UIOrder < management[j].UIOrder })
	return appProviderRuntimeRegistry{
		valid: true, catalog: catalog, byKind: byKind, management: management,
	}, nil
}

func validateAppProviderMetadata(metadata appProviderMetadata) error {
	if !providercatalog.ValidKind(metadata.Kind) {
		return fmt.Errorf("%w: invalid Provider kind %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	if !validAppProviderText(metadata.DisplayName, appProviderMetadataMaxDisplayBytes) ||
		!validAppProviderText(metadata.Description, appProviderMetadataMaxDescriptionBytes) {
		return fmt.Errorf("%w: unsafe management metadata for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	if _, err := providercatalog.New([]providercatalog.Option{{
		Kind: metadata.Kind, DisplayName: metadata.DisplayName, Description: metadata.Description,
		RouteRank: metadata.CatalogRank,
	}}); err != nil {
		return fmt.Errorf("%w: unsafe Catalog metadata for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	if metadata.Group != appProviderGroupSubscription && metadata.Group != appProviderGroupAPIKey {
		return fmt.Errorf("%w: invalid group for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	if !validAppProviderPrefix(metadata.Prefix) {
		return fmt.Errorf("%w: invalid UI prefix for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	if !validAppProviderTheme(metadata.ThemeColor) {
		return fmt.Errorf("%w: invalid theme color for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	if metadata.UIOrder <= 0 || int64(metadata.UIOrder) > appProviderMetadataMaxOrder {
		return fmt.Errorf("%w: invalid UI order for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	switch metadata.Visibility {
	case appProviderVisibilityVisible, appProviderVisibilityHidden:
	default:
		return fmt.Errorf("%w: visibility is unspecified for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	switch metadata.ConnectionMode {
	case appProviderConnectionAccount, appProviderConnectionCredential, appProviderConnectionNone:
	default:
		return fmt.Errorf("%w: invalid connection mode for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	switch metadata.ExecutionMode {
	case appProviderExecutionAPI, appProviderExecutionProxy,
		appProviderExecutionOfficialCLI, appProviderExecutionLocal:
	default:
		return fmt.Errorf("%w: invalid execution mode for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	switch metadata.AttachmentPolicy {
	case appProviderAttachmentNone, appProviderAttachmentLocal:
	default:
		return fmt.Errorf("%w: invalid attachment policy for %q", errAppProviderRuntimeRegistry, metadata.Kind)
	}
	return nil
}

func validateAppProviderRuntimeDependencies(registration appProviderRuntimeRegistration) error {
	metadata := registration.Metadata
	standard := registration.NewAdapter != nil
	terminal := registration.Terminal != nil
	if standard == terminal {
		return fmt.Errorf(
			"%w: Provider %q must declare exactly one standard or terminal live path",
			errAppProviderRuntimeRegistry,
			metadata.Kind,
		)
	}
	if (metadata.AttachmentPolicy == appProviderAttachmentLocal) !=
		(registration.NewLocalAttachmentRun != nil) {
		return fmt.Errorf(
			"%w: Provider %q attachment policy lacks its typed local runner",
			errAppProviderRuntimeRegistry,
			metadata.Kind,
		)
	}
	if registration.NewStructuredSession != nil && !terminal {
		return fmt.Errorf(
			"%w: Provider %q structured sessions require a terminal route",
			errAppProviderRuntimeRegistry,
			metadata.Kind,
		)
	}

	switch metadata.Visibility {
	case appProviderVisibilityVisible:
		if metadata.ConnectionMode == appProviderConnectionNone {
			return fmt.Errorf("%w: visible Provider %q has no connection mode", errAppProviderRuntimeRegistry, metadata.Kind)
		}
	case appProviderVisibilityHidden:
		if metadata.ConnectionMode != appProviderConnectionNone || registrationHasOnboardingBehavior(registration) {
			return fmt.Errorf(
				"%w: hidden Provider %q contributes selection, Connect, or readiness",
				errAppProviderRuntimeRegistry,
				metadata.Kind,
			)
		}
	}

	switch metadata.ConnectionMode {
	case appProviderConnectionAccount:
		if metadata.Group != appProviderGroupSubscription || !registrationSupportsOnboarding(registration) {
			return fmt.Errorf(
				"%w: account Provider %q lacks Connect, account, model, or member behavior",
				errAppProviderRuntimeRegistry,
				metadata.Kind,
			)
		}
	case appProviderConnectionCredential:
		if metadata.Group != appProviderGroupAPIKey || registration.NewAdapter == nil ||
			registrationHasOnboardingBehavior(registration) {
			return fmt.Errorf(
				"%w: credential Provider %q lacks a compiled adapter or claims account behavior",
				errAppProviderRuntimeRegistry,
				metadata.Kind,
			)
		}
	case appProviderConnectionNone:
		if metadata.Visibility != appProviderVisibilityHidden {
			return fmt.Errorf("%w: connection mode none is not hidden for %q", errAppProviderRuntimeRegistry, metadata.Kind)
		}
	}
	return nil
}

func registrationHasOnboardingBehavior(registration appProviderRuntimeRegistration) bool {
	return registration.Connect != nil || registration.EnsureAccountProvider != nil ||
		registration.SeedConnectedModels != nil || registration.SelectOnboardingModel != nil ||
		registration.NewOnboardingMember != nil
}

func registrationSupportsOnboarding(registration appProviderRuntimeRegistration) bool {
	return registration.Metadata.Visibility == appProviderVisibilityVisible &&
		registration.Metadata.ConnectionMode == appProviderConnectionAccount &&
		registration.Connect != nil && registration.EnsureAccountProvider != nil &&
		registration.SeedConnectedModels != nil && registration.SelectOnboardingModel != nil &&
		registration.NewOnboardingMember != nil
}

func validAppProviderText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, runeValue := range value {
		if unicode.IsControl(runeValue) || unicode.In(runeValue, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

func validAppProviderPrefix(value string) bool {
	if len(value) == 0 || len(value) > appProviderMetadataMaxPrefixBytes {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func validAppProviderTheme(value string) bool {
	if len(value) != 7 || value[0] != '#' {
		return false
	}
	for _, char := range value[1:] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func (metadata appProviderMetadata) managementOption() appProviderManagementOption {
	visible := metadata.Visibility == appProviderVisibilityVisible
	return appProviderManagementOption{
		Kind: metadata.Kind, DisplayName: metadata.DisplayName, Description: metadata.Description,
		Group: metadata.Group, Connectable: visible && metadata.ConnectionMode == appProviderConnectionAccount,
		ConnectionMode: metadata.ConnectionMode, ExecutionMode: metadata.ExecutionMode,
		Visible: visible, Prefix: metadata.Prefix, ThemeColor: metadata.ThemeColor,
		Beta: metadata.Beta, UIOrder: metadata.UIOrder,
	}
}

func (registry appProviderRuntimeRegistry) Catalog() providercatalog.Catalog {
	if !registry.valid || registry.catalogContainsProductionDenied() {
		return providercatalog.Catalog{}
	}
	return registry.catalog
}

func (registry appProviderRuntimeRegistry) catalogContainsProductionDenied() bool {
	for _, option := range registry.catalog.Options() {
		if appProviderProductionDenied(option.Kind) {
			return true
		}
	}
	return false
}

func (registry appProviderRuntimeRegistry) onboardingOptions() []appProviderOption {
	if !registry.valid {
		return []appProviderOption{}
	}
	options := registry.catalog.Options()
	result := make([]appProviderOption, 0, len(options))
	for _, option := range options {
		if !option.Advertised || appProviderProductionDenied(option.Kind) || !registry.supportsOnboarding(option.Kind) {
			continue
		}
		result = append(result, appProviderOption{
			Kind: option.Kind, DisplayName: option.DisplayName, Description: option.Description,
			Recommended: option.Recommended, Beta: option.Beta, Advertised: option.Advertised,
			RouteRank: option.RouteRank,
		})
	}
	return result
}

func (registry appProviderRuntimeRegistry) managementOptions() []appProviderManagementOption {
	if !registry.valid {
		return []appProviderManagementOption{}
	}
	result := make([]appProviderManagementOption, 0, len(registry.management))
	for _, option := range registry.management {
		if appProviderProductionDenied(option.Kind) {
			continue
		}
		result = append(result, option)
	}
	return result
}

func (registry appProviderRuntimeRegistry) registration(
	kind string,
) (appProviderRuntimeRegistration, bool) {
	if !registry.valid || appProviderProductionDenied(kind) {
		return appProviderRuntimeRegistration{}, false
	}
	registration, exists := registry.byKind[kind]
	return registration, exists
}

func (registry appProviderRuntimeRegistry) managementOption(
	kind string,
) (appProviderManagementOption, bool) {
	registration, exists := registry.registration(kind)
	if !exists {
		return appProviderManagementOption{}, false
	}
	return registration.Metadata.managementOption(), true
}

func (registry appProviderRuntimeRegistry) supportsOnboarding(kind string) bool {
	registration, exists := registry.registration(kind)
	if !exists || !registrationSupportsOnboarding(registration) {
		return false
	}
	option, exists := registry.catalog.Option(kind)
	return exists && option.Advertised
}

func (registry appProviderRuntimeRegistry) supportsConnect(kind string) bool {
	registration, exists := registry.registration(kind)
	return exists && registration.Metadata.Visibility == appProviderVisibilityVisible &&
		registration.Metadata.ConnectionMode == appProviderConnectionAccount && registration.Connect != nil
}

func (registry appProviderRuntimeRegistry) contributesReadiness(kind string) bool {
	registration, exists := registry.registration(kind)
	return exists && registration.Metadata.Visibility == appProviderVisibilityVisible &&
		(registration.Metadata.ConnectionMode == appProviderConnectionAccount ||
			registration.Metadata.ConnectionMode == appProviderConnectionCredential)
}

func (registry appProviderRuntimeRegistry) contributesCredentialReadiness(kind string) bool {
	registration, exists := registry.registration(kind)
	return exists && registration.Metadata.Visibility == appProviderVisibilityVisible &&
		registration.Metadata.ConnectionMode == appProviderConnectionCredential
}

func (registry appProviderRuntimeRegistry) readinessAccountKinds() []string {
	if !registry.valid {
		return []string{}
	}
	result := make([]string, 0)
	for _, option := range registry.management {
		if !appProviderProductionDenied(option.Kind) && option.Visible &&
			option.ConnectionMode == appProviderConnectionAccount {
			result = append(result, option.Kind)
		}
	}
	return result
}

func (registry appProviderRuntimeRegistry) canonicalOnboardingKinds(kinds []string) ([]string, error) {
	for _, kind := range kinds {
		if appProviderProductionDenied(kind) {
			return nil, fmt.Errorf("%w: Provider is not production-enabled", providercatalog.ErrUnsupportedKind)
		}
	}
	canonical, err := registry.Catalog().CanonicalSelectedKinds(kinds)
	if err != nil {
		return nil, err
	}
	for _, kind := range canonical {
		if !registry.supportsOnboarding(kind) {
			return nil, fmt.Errorf("%w: Provider lacks onboarding behavior", providercatalog.ErrUnsupportedKind)
		}
	}
	return canonical, nil
}

func (registry appProviderRuntimeRegistry) connectDriverFactory(kind string) (appConnectDriverFactory, bool) {
	registration, exists := registry.registration(kind)
	return registration.Connect, exists && registration.Connect != nil
}

func (registry appProviderRuntimeRegistry) accountProviderStrategy(kind string) (appAccountProviderStrategy, bool) {
	registration, exists := registry.registration(kind)
	return registration.EnsureAccountProvider, exists && registration.EnsureAccountProvider != nil
}

func (registry appProviderRuntimeRegistry) connectedModelSeeder(kind string) (appConnectedModelSeeder, bool) {
	registration, exists := registry.registration(kind)
	return registration.SeedConnectedModels, exists && registration.SeedConnectedModels != nil
}

func (registry appProviderRuntimeRegistry) onboardingModelSelector(kind string) (appOnboardingModelSelector, bool) {
	registration, exists := registry.registration(kind)
	return registration.SelectOnboardingModel, exists && registration.SelectOnboardingModel != nil
}

func (registry appProviderRuntimeRegistry) onboardingMemberFactory(kind string) (appOnboardingMemberFactory, bool) {
	registration, exists := registry.registration(kind)
	return registration.NewOnboardingMember, exists && registration.NewOnboardingMember != nil
}

func (registry appProviderRuntimeRegistry) providerAdapterFactory(kind string) (appProviderAdapterFactory, bool) {
	registration, exists := registry.registration(kind)
	return registration.NewAdapter, exists && registration.NewAdapter != nil
}

func (registry appProviderRuntimeRegistry) localAttachmentRunnerFactory(
	kind string,
) (appLocalAttachmentRunnerFactory, bool) {
	registration, exists := registry.registration(kind)
	return registration.NewLocalAttachmentRun, exists && registration.NewLocalAttachmentRun != nil
}

func (registry appProviderRuntimeRegistry) terminalRouteRunner(kind string) (appTerminalRouteRunner, bool) {
	registration, exists := registry.registration(kind)
	return registration.Terminal, exists && registration.Terminal != nil
}

func (registry appProviderRuntimeRegistry) structuredSessionRunnerFactory(
	kind string,
) (appStructuredSessionRunnerFactory, bool) {
	registration, exists := registry.registration(kind)
	return registration.NewStructuredSession, exists && registration.NewStructuredSession != nil
}

type appBoundConnectDriver struct {
	kind   string
	runner connectRunner
}

func (driver appBoundConnectDriver) Detect() (bool, error) {
	return driver.runner.detect(driver.kind)
}

func (driver appBoundConnectDriver) Install(ctx context.Context, onLine func(string)) error {
	return driver.runner.install(ctx, driver.kind, onLine)
}

func (driver appBoundConnectDriver) Login(
	ctx context.Context,
	configDir string,
) (string, string, func() error, error) {
	return driver.runner.login(ctx, driver.kind, configDir)
}

func (driver appBoundConnectDriver) PollAuth(configDir string) authState {
	return driver.runner.pollAuth(driver.kind, configDir)
}

func (driver appBoundConnectDriver) AccountLabel(configDir string) string {
	return driver.runner.accountLabel(driver.kind, configDir)
}

func appProductionProviderRuntimeRegistrations() []appProviderRuntimeRegistration {
	return []appProviderRuntimeRegistration{
		appProductionAccountRuntime(
			"codex", "Codex", "Kết nối tài khoản ChatGPT/Codex trên máy này",
			"cx", "#0f7a63", 10, 10, true, appProviderExecutionProxy,
			appProductionCodexOnboardingMemberFactory,
			func(providerID string, st *store.Store, client *http.Client, logger *slog.Logger) (providerAdapter, error) {
				if st == nil {
					return nil, errors.New("Codex adapter requires a Store")
				}
				adapter := &codexProxyAdapter{providerID: providerID, logger: logger, client: client}
				adapter.pickConfigDir = makeAccountConfigDir(st, accountSel, providerID, "codex")
				return adapter, nil
			},
		),
		appProductionClaudeRuntime(),
		appProductionCredentialRuntime(
			"openai", "OpenAI", "Kết nối OpenAI bằng API key", "oa", "#10a37f", 30,
			func(providerID string, _ *store.Store, client *http.Client, _ *slog.Logger) (providerAdapter, error) {
				return newOpenAIAdapter(providerID, client), nil
			},
		),
		appProductionCredentialRuntime(
			"anthropic", "Anthropic", "Kết nối Anthropic bằng API key", "an", "#c8613b", 40,
			func(providerID string, _ *store.Store, client *http.Client, _ *slog.Logger) (providerAdapter, error) {
				return newAnthropicAdapter(providerID, client), nil
			},
		),
		appProductionCredentialRuntime(
			"gemini", "Google Gemini", "Kết nối Google Gemini bằng API key", "gm", "#3f6ff5", 50,
			func(providerID string, _ *store.Store, client *http.Client, _ *slog.Logger) (providerAdapter, error) {
				return newGeminiAdapter(providerID, client), nil
			},
		),
		appProductionCredentialRuntime(
			"openrouter", "OpenRouter", "Kết nối OpenRouter bằng API key", "or", "#5b5ef0", 60,
			func(providerID string, _ *store.Store, client *http.Client, _ *slog.Logger) (providerAdapter, error) {
				return newOpenRouterAdapter(providerID, client), nil
			},
		),
		appProductionHiddenGeminiCLIRuntime(),
	}
}

func appProductionAccountRuntime(
	kind string,
	displayName string,
	description string,
	prefix string,
	theme string,
	uiOrder int,
	catalogRank int,
	recommended bool,
	execution appProviderExecutionMode,
	onboardingMember appOnboardingMemberFactory,
	adapter appProviderAdapterFactory,
) appProviderRuntimeRegistration {
	return appProviderRuntimeRegistration{
		Metadata: appProviderMetadata{
			Kind: kind, DisplayName: displayName, Description: description,
			Group: appProviderGroupSubscription, Prefix: prefix, ThemeColor: theme,
			UIOrder: uiOrder, CatalogRank: catalogRank, Recommended: recommended,
			Visibility: appProviderVisibilityVisible, ConnectionMode: appProviderConnectionAccount,
			ExecutionMode: execution, AttachmentPolicy: appProviderAttachmentNone,
		},
		Connect: func(logger *slog.Logger) appConnectDriver {
			return appBoundConnectDriver{kind: kind, runner: newDefaultConnectRunner(logger)}
		},
		EnsureAccountProvider: func(st *store.Store) error {
			return st.EnsureAccountRuntimeProvider(kind, displayName)
		},
		SeedConnectedModels: func(st *store.Store, providerID string) error {
			models := cliProviderModels(st, providerID, kind)
			return st.ReplaceLLMModels(providerID, store.LLMModelDiscovered, models)
		},
		SelectOnboardingModel: func(st *store.Store, providerID string) (string, error) {
			return selectOnboardingModel(st, kind, providerID)
		},
		NewOnboardingMember: onboardingMember,
		NewAdapter:          adapter,
	}
}

func appProductionClaudeRuntime() appProviderRuntimeRegistration {
	registration := appProductionAccountRuntime(
		"claude-code", "Claude Code", "Kết nối tài khoản Claude Code trên máy này",
		"cc", "#c8613b", 20, 100, false, appProviderExecutionOfficialCLI,
		appProductionClaudeOnboardingMemberFactory, nil,
	)
	registration.Metadata.AttachmentPolicy = appProviderAttachmentLocal
	registration.Terminal = func(
		runner *appLLMRunner,
		ctx context.Context,
		entry store.LLMRouteEntry,
		input appLLMRouteInput,
		step func(string),
	) (appZaloRunResult, error) {
		if runner == nil {
			return appZaloRunResult{}, errors.New("Claude terminal runner is unavailable")
		}
		return runner.runClaude(ctx, entry, input, step)
	}
	registration.NewLocalAttachmentRun = func(runner *appLLMRunner) appLocalAttachmentRun {
		if runner == nil {
			return nil
		}
		return func(
			ctx context.Context,
			entry store.LLMRouteEntry,
			input appLLMRouteInput,
			step func(string),
		) (appZaloRunResult, error) {
			return runner.runClaude(ctx, entry, input, step)
		}
	}
	registration.NewStructuredSession = func(a *api, config zaloConfig) appStructuredSessionRunner {
		if a == nil {
			return nil
		}
		return func(
			ctx context.Context,
			model string,
			current *store.ZaloCLISession,
		) (zaloRunner, appZaloClaudeBinding) {
			return a.appClaudeRunnerForSession(ctx, config, model, current)
		}
	}
	return registration
}

func appProductionCredentialRuntime(
	kind string,
	displayName string,
	description string,
	prefix string,
	theme string,
	uiOrder int,
	adapter appProviderAdapterFactory,
) appProviderRuntimeRegistration {
	return appProviderRuntimeRegistration{
		Metadata: appProviderMetadata{
			Kind: kind, DisplayName: displayName, Description: description,
			Group: appProviderGroupAPIKey, Prefix: prefix, ThemeColor: theme, UIOrder: uiOrder,
			Visibility: appProviderVisibilityVisible, ConnectionMode: appProviderConnectionCredential,
			ExecutionMode: appProviderExecutionAPI, AttachmentPolicy: appProviderAttachmentNone,
		},
		NewAdapter: adapter,
	}
}

func appProductionHiddenGeminiCLIRuntime() appProviderRuntimeRegistration {
	return appProviderRuntimeRegistration{
		Metadata: appProviderMetadata{
			Kind: "gemini-cli", DisplayName: "Gemini CLI",
			Description: "Runtime CLI tương thích cho định tuyến đã lưu",
			Group:       appProviderGroupSubscription, Prefix: "gc", ThemeColor: "#3f6ff5", UIOrder: 70,
			Visibility: appProviderVisibilityHidden, ConnectionMode: appProviderConnectionNone,
			ExecutionMode: appProviderExecutionOfficialCLI, AttachmentPolicy: appProviderAttachmentLocal,
		},
		NewAdapter: func(
			providerID string,
			_ *store.Store,
			_ *http.Client,
			logger *slog.Logger,
		) (providerAdapter, error) {
			return newCLIAdapter(cliDescriptors["gemini-cli"], providerID, logger), nil
		},
		NewLocalAttachmentRun: func(runner *appLLMRunner) appLocalAttachmentRun {
			if runner == nil {
				return nil
			}
			return func(
				ctx context.Context,
				entry store.LLMRouteEntry,
				input appLLMRouteInput,
				_ func(string),
			) (appZaloRunResult, error) {
				adapter := runner.cfg.Adapters[entry.ProviderID]
				if adapter == nil {
					return appZaloRunResult{}, errors.New("Gemini CLI attachment adapter is unavailable")
				}
				return runner.runCLIAttachment(ctx, adapter, entry, input.statelessPrompt)
			}
		},
	}
}

// productionAppProviderRuntimeRegistryValue is constructed exactly once. The
// registration builders below only capture kind strings in closures; no
// closure body (and therefore no cliDescriptors lookup) runs during package
// initialization, avoiding cross-file init-order dependencies.
var productionAppProviderRuntimeRegistryValue = mustProductionAppProviderRuntimeRegistry()

func mustProductionAppProviderRuntimeRegistry() appProviderRuntimeRegistry {
	registry, err := newAppProviderRuntimeRegistry(
		providercatalog.Default().Options(),
		appProductionProviderRuntimeRegistrations(),
	)
	if err != nil {
		panic(fmt.Sprintf("production Provider runtime registry: %v", err))
	}
	return registry
}

func productionAppProviderRuntimeRegistry() appProviderRuntimeRegistry {
	return productionAppProviderRuntimeRegistryValue
}

type appRuntimeContext struct {
	api      *api
	registry appProviderRuntimeRegistry
	connect  *connectManager
}

func (ctx appRuntimeContext) onboardingStore() store.OnboardingStore {
	return ctx.api.st.Onboarding(ctx.registry.Catalog())
}

func productionAppRuntimeContext(a *api) appRuntimeContext {
	return appRuntimeContext{
		api: a, registry: productionAppProviderRuntimeRegistry(), connect: a.newConnectManager(),
	}
}
