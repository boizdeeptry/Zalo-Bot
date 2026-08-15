package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"agentdc/internal/store"
)

type runtimeOnboardingHarness struct {
	env      *onboardingRouteTestEnv
	ctx      appRuntimeContext
	registry appProviderRuntimeRegistry
}

func newRuntimeOnboardingHarness(
	t *testing.T,
	mutate func([]appProviderRuntimeRegistration),
) *runtimeOnboardingHarness {
	t.Helper()
	registrations := appRuntimeSyntheticRegistrations()
	if mutate != nil {
		mutate(registrations)
	}
	registry, err := newAppProviderRuntimeRegistry(appRuntimeSyntheticOptions(), registrations)
	if err != nil {
		t.Fatal(err)
	}
	env := newOnboardingRouteTestEnv(t)
	ctx := appRuntimeContext{api: env.a, registry: registry, connect: env.a.newConnectManager()}
	mux := http.NewServeMux()
	registerAppRoutesWithContext(mux, ctx)
	env.mux = mux
	return &runtimeOnboardingHarness{env: env, ctx: ctx, registry: registry}
}

func runtimeOnboardingRegistration(
	t *testing.T,
	registrations []appProviderRuntimeRegistration,
	kind string,
) *appProviderRuntimeRegistration {
	t.Helper()
	index := appRuntimeRegistrationIndex(t, registrations, kind)
	return &registrations[index]
}

func (h *runtimeOnboardingHarness) prepareReadyRoute(
	t *testing.T,
	kinds []string,
	models map[string]string,
) store.OnboardingSnapshot {
	t.Helper()
	onboarding := h.ctx.onboardingStore()
	for _, kind := range kinds {
		if err := onboarding.EnsureOnboardingProviderForKind(kind); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := onboarding.ReplaceOnboardingProviderSelection(1, kinds)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range snapshot.Stages {
		kind := stage.Kind
		accountID := kind + "-account"
		configDir := accountConfigDir(h.env.dataDir, kind, accountID)
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		begun, err := onboarding.BeginOnboardingProvider(snapshot.State.Revision, kind)
		if err != nil {
			t.Fatal(err)
		}
		account := store.LLMAccount{
			ID: accountID, ProviderID: kind, Label: kind + " fixture",
			ConfigDir: configDir, Enabled: false,
		}
		bound, err := onboarding.BindOnboardingAccount(begun.State.Revision, kind, account)
		if err != nil {
			t.Fatal(err)
		}
		modelID := models[kind]
		if err := h.env.a.st.ReplaceLLMModels(kind, store.LLMModelDiscovered, []store.LLMModel{{
			ProviderID: kind, ModelID: modelID, Name: modelID, Available: true,
		}}); err != nil {
			t.Fatal(err)
		}
		snapshot, err = onboarding.StageOnboardingSetup(bound.State.Revision, kind, accountID, modelID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func runtimeOnboardingDefaultExecute(
	t *testing.T,
	ctx appRuntimeContext,
	route store.OnboardingTestRoute,
	prompt string,
) (appOnboardingTestResult, error) {
	t.Helper()
	switch execute := any(defaultAppOnboardingTestExecute).(type) {
	case func(context.Context, *api, store.OnboardingTestRoute, string) (appOnboardingTestResult, error):
		return execute(context.Background(), ctx.api, route, prompt)
	case func(context.Context, appRuntimeContext, store.OnboardingTestRoute, string) (appOnboardingTestResult, error):
		return execute(context.Background(), ctx, route, prompt)
	default:
		t.Fatalf("unexpected default onboarding executor type %T", execute)
		return appOnboardingTestResult{}, errors.New("unreachable")
	}
}

func TestRuntimeOnboardingStatusProjectsOnlyContextCatalog(t *testing.T) {
	harness := newRuntimeOnboardingHarness(t, nil)
	rr := harness.env.serve(http.MethodGet, "/onboarding/status", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := decodeOnboardingResponse[appOnboardingStatusWire](t, rr)
	kinds := make([]string, len(response.Options))
	ranks := make([]int, len(response.Options))
	for index, option := range response.Options {
		kinds[index], ranks[index] = option.Kind, option.RouteRank
	}
	if !slices.Equal(kinds, []string{"codex", "future-cli", "claude-code"}) ||
		!slices.Equal(ranks, []int{10, 50, 100}) {
		t.Fatalf("context onboarding options = %q ranks %v", kinds, ranks)
	}
	for _, forbidden := range []string{"openai", "anthropic", "gemini", "openrouter", "gemini-cli"} {
		if slices.Contains(kinds, forbidden) {
			t.Fatalf("management-only runtime %q leaked into onboarding status", forbidden)
		}
	}
}

func TestRuntimeOnboardingSyntheticSelectionSetupPersonaTest(t *testing.T) {
	harness := newRuntimeOnboardingHarness(t, func(registrations []appProviderRuntimeRegistration) {
		future := runtimeOnboardingRegistration(t, registrations, "future-cli")
		future.SelectOnboardingModel = func(*store.Store, string) (string, error) {
			return "future-model", nil
		}
		future.NewOnboardingMember = func(
			_ *api,
			entry store.OnboardingTestRouteEntry,
		) (appOnboardingMemberRun, error) {
			if entry.Kind != "future-cli" || entry.ProviderID != "future-cli" ||
				entry.ModelID != "future-model" || entry.Position != 0 {
				return nil, store.ErrOnboardingInvalidStagingOwnership
			}
			return func(context.Context, string, func(string)) (string, error) {
				return "Xin chào, tôi là Bé Mi.", nil
			}, nil
		}
	})

	selected := harness.env.serve(http.MethodPut, "/onboarding/providers",
		`{"revision":1,"selected_kinds":["future-cli"]}`)
	if selected.Code != http.StatusOK {
		t.Fatalf("selection status=%d body=%s", selected.Code, selected.Body.String())
	}
	selection := decodeOnboardingResponse[appOnboardingStatusWire](t, selected)
	begun := harness.env.serve(http.MethodPut, "/onboarding/provider",
		`{"revision":2,"kind":"future-cli"}`)
	if begun.Code != http.StatusOK {
		t.Fatalf("begin status=%d body=%s", begun.Code, begun.Body.String())
	}

	onboarding := harness.ctx.onboardingStore()
	if err := onboarding.EnsureOnboardingProviderForKind("future-cli"); err != nil {
		t.Fatal(err)
	}
	configDir := accountConfigDir(harness.env.dataDir, "future-cli", "future-account")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	bound, err := onboarding.BindOnboardingAccount(3, "future-cli", store.LLMAccount{
		ID: "future-account", ProviderID: "future-cli", Label: "Future fixture",
		ConfigDir: configDir, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.env.a.st.ReplaceLLMModels("future-cli", store.LLMModelDiscovered, []store.LLMModel{{
		ProviderID: "future-cli", ModelID: "future-model", Name: "Future Model", Available: true,
	}}); err != nil {
		t.Fatal(err)
	}
	setup := harness.env.serve(http.MethodPost, "/onboarding/setup",
		`{"revision":4,"kind":"future-cli","account_id":"future-account"}`)
	if setup.Code != http.StatusOK {
		t.Fatalf("setup status=%d body=%s selection=%+v bound=%+v", setup.Code, setup.Body.String(), selection, bound)
	}
	setupState := decodeOnboardingResponse[appOnboardingStatusWire](t, setup)
	if setupState.Phase != store.OnboardingPhasePersona || setupState.Revision != 5 ||
		setupState.Providers[0].ModelID != "future-model" {
		t.Fatalf("setup response = %+v", setupState)
	}

	configureAgentPersonaForOnboardingTest(t, harness.env, "Tôi là {{TEN_BOT}}. Luôn trả lời ngắn gọn.\n")
	persona := harness.env.serve(http.MethodPut, "/agent",
		`{"values":{"TEN_BOT":"Bé Mi"},"display_name":"Bé Mi","require_complete":true,"onboarding_revision":5}`)
	if persona.Code != http.StatusOK {
		t.Fatalf("persona status=%d body=%s", persona.Code, persona.Body.String())
	}
	tested := harness.env.serve(http.MethodPost, "/onboarding/test-chat",
		`{"revision":6,"message":"Xin chào"}`)
	if tested.Code != http.StatusOK {
		t.Fatalf("test status=%d body=%s", tested.Code, tested.Body.String())
	}
	result := decodeOnboardingResponse[appOnboardingTestChatResponse](t, tested)
	if result.ProviderID != "future-cli" || result.ModelID != "future-model" ||
		result.Position != 0 || result.Revision != 7 {
		t.Fatalf("synthetic Test result = %+v", result)
	}
}

func TestRuntimeOnboardingUsesPinnedMemberFactoryInRankOrder(t *testing.T) {
	var factoryOrder, runOrder []string
	harness := newRuntimeOnboardingHarness(t, func(registrations []appProviderRuntimeRegistration) {
		for _, kind := range []string{"codex", "future-cli", "claude-code"} {
			kind := kind
			registration := runtimeOnboardingRegistration(t, registrations, kind)
			registration.NewOnboardingMember = func(
				_ *api,
				entry store.OnboardingTestRouteEntry,
			) (appOnboardingMemberRun, error) {
				if entry.Kind != kind || entry.ProviderID != kind {
					return nil, store.ErrOnboardingInvalidStagingOwnership
				}
				factoryOrder = append(factoryOrder, kind)
				return func(context.Context, string, func(string)) (string, error) {
					runOrder = append(runOrder, kind)
					if kind == "codex" {
						return "", errors.New("first member unavailable")
					}
					return "Xin chào, tôi là Bé Mi.", nil
				}, nil
			}
		}
	})
	snapshot := harness.prepareReadyRoute(t,
		[]string{"claude-code", "future-cli", "codex"},
		map[string]string{"codex": "codex-model", "future-cli": "future-model", "claude-code": "claude-model"},
	)
	configureAgentPersonaForOnboardingTest(t, harness.env, "Persona hoàn chỉnh.\n")
	if err := harness.env.a.st.SetAgentDisplayName("Bé Mi"); err != nil {
		t.Fatal(err)
	}
	state, err := harness.env.a.st.AdvanceOnboardingPersona(
		snapshot.State.Revision,
		agentPersonaFingerprint([]byte("Persona hoàn chỉnh.\n"), "Bé Mi"),
		"Bé Mi",
	)
	if err != nil {
		t.Fatal(err)
	}
	route, err := harness.ctx.onboardingStore().OnboardingTestRoute(context.Background(), state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtimeOnboardingDefaultExecute(t, harness.ctx, route, "probe")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(factoryOrder, []string{"codex", "future-cli", "claude-code"}) {
		t.Fatalf("factory order = %q", factoryOrder)
	}
	if !slices.Equal(runOrder, []string{"codex", "future-cli"}) {
		t.Fatalf("fallback run order = %q", runOrder)
	}
	if result.ProviderID != "future-cli" || result.ModelID != "future-model" || result.Position != 1 {
		t.Fatalf("actual winning identity = %+v", result)
	}
}

func TestRuntimeOnboardingProductionCodexFactoryPinsExactEntry(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	client := &http.Client{Timeout: time.Second}
	configDir := filepath.Join(t.TempDir(), "accounts", "codex", "staged-account")
	entry := store.OnboardingTestRouteEntry{
		Position: 0, Kind: "codex", ProviderID: "codex", AccountID: "staged-account",
		ModelID: "gpt-5.6-terra", ConfigDir: configDir,
	}
	var gotClient *http.Client
	var gotConfigDir, gotModel, gotPrompt string
	factory := newAppCodexOnboardingMemberFactory(
		client,
		func(
			_ context.Context,
			usedClient *http.Client,
			usedConfigDir string,
			model string,
			prompt string,
		) (string, int, error) {
			gotClient = usedClient
			gotConfigDir, gotModel, gotPrompt = usedConfigDir, model, prompt
			return "Xin chào, tôi là Bé Mi.", http.StatusOK, nil
		},
	)
	run, err := factory(env.a, entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.ConfigDir = filepath.Join(t.TempDir(), "live-account")
	entry.ModelID = "live-model"
	answer, err := run(context.Background(), "exact staged prompt", func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Xin chào, tôi là Bé Mi." || gotClient != client ||
		gotConfigDir != configDir || gotModel != "gpt-5.6-terra" || gotPrompt != "exact staged prompt" {
		t.Fatalf(
			"production Codex member result=%q client=%p config/model/prompt=%q/%q/%q",
			answer, gotClient, gotConfigDir, gotModel, gotPrompt,
		)
	}
}

func TestRuntimeOnboardingRejectsMissingRegistrationBeforeExternalIO(t *testing.T) {
	var firstFactoryCalls int
	harness := newRuntimeOnboardingHarness(t, func(registrations []appProviderRuntimeRegistration) {
		codex := runtimeOnboardingRegistration(t, registrations, "codex")
		codex.NewOnboardingMember = func(
			*api,
			store.OnboardingTestRouteEntry,
		) (appOnboardingMemberRun, error) {
			firstFactoryCalls++
			return nil, errors.New("must not be called")
		}
	})
	snapshot := harness.prepareReadyRoute(t,
		[]string{"codex", "future-cli"},
		map[string]string{"codex": "codex-model", "future-cli": "future-model"},
	)
	state, err := harness.env.a.st.AdvanceOnboardingPersona(
		snapshot.State.Revision,
		"f0f8cbbfb20b4ee4c8e1ba6ad5b3eabcbb16139590dfc06c8c12ec6f8b1726a0",
		"Bé Mi",
	)
	if err != nil {
		t.Fatal(err)
	}
	route, err := harness.ctx.onboardingStore().OnboardingTestRoute(context.Background(), state.Revision)
	if err != nil {
		t.Fatal(err)
	}
	delete(harness.ctx.registry.byKind, "future-cli")
	_, err = runtimeOnboardingDefaultExecute(t, harness.ctx, route, "probe")
	if !errors.Is(err, store.ErrOnboardingInvalidStagingOwnership) {
		t.Fatalf("missing registration error = %v", err)
	}
	if firstFactoryCalls != 0 {
		t.Fatalf("earlier factory ran %d times before later registration validation", firstFactoryCalls)
	}
	for _, entry := range route.Entries {
		if _, statErr := os.Stat(filepath.Clean(entry.ConfigDir)); statErr != nil {
			t.Fatalf("private staged config changed before rejection: %v", statErr)
		}
	}
}

func TestRuntimePersonaBoundaryDoesNotFallBackToDefaultCatalog(t *testing.T) {
	harness := newRuntimeOnboardingHarness(t, nil)
	snapshot := harness.prepareReadyRoute(t,
		[]string{"future-cli"},
		map[string]string{"future-cli": "future-model"},
	)
	configureAgentPersonaForOnboardingTest(t, harness.env, "Tên tôi là {{TEN_BOT}}.\n")
	rr := harness.env.serve(http.MethodPut, "/agent",
		`{"values":{"TEN_BOT":"Bé Mi"},"display_name":"Bé Mi","require_complete":true,"onboarding_revision":5}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("persona status=%d body=%s snapshot=%+v", rr.Code, rr.Body.String(), snapshot)
	}
	post, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil || post.State.Phase != store.OnboardingPhaseTest || post.State.Revision != 6 ||
		post.Stages[0].Kind != "future-cli" {
		t.Fatalf("context-bound post-write snapshot = %+v, %v", post, err)
	}
}

func TestRuntimeOnboardingReceiptCompensationAndRetryUseContextCatalog(t *testing.T) {
	harness := newRuntimeOnboardingHarness(t, func(registrations []appProviderRuntimeRegistration) {
		future := runtimeOnboardingRegistration(t, registrations, "future-cli")
		future.NewOnboardingMember = func(
			*api,
			store.OnboardingTestRouteEntry,
		) (appOnboardingMemberRun, error) {
			return func(context.Context, string, func(string)) (string, error) {
				return "Xin chào, tôi là Bé Mi.", nil
			}, nil
		}
	})
	snapshot := harness.prepareReadyRoute(t,
		[]string{"future-cli"},
		map[string]string{"future-cli": "future-model"},
	)
	const persona = "Persona hoàn chỉnh.\n"
	configureAgentPersonaForOnboardingTest(t, harness.env, persona)
	if err := harness.env.a.st.SetAgentDisplayName("Bé Mi"); err != nil {
		t.Fatal(err)
	}
	tested, err := harness.env.a.st.AdvanceOnboardingPersona(
		snapshot.State.Revision,
		agentPersonaFingerprint([]byte(persona), "Bé Mi"),
		"Bé Mi",
	)
	if err != nil {
		t.Fatal(err)
	}

	requestContext, cancel := context.WithCancel(context.Background())
	oldAfterCommit := appOnboardingTestAfterCommit
	appOnboardingTestAfterCommit = cancel
	t.Cleanup(func() { appOnboardingTestAfterCommit = oldAfterCommit })
	request := httptest.NewRequest(
		http.MethodPost,
		"/onboarding/test-chat",
		strings.NewReader(`{"revision":6,"message":"xin chào"}`),
	).WithContext(requestContext)
	canceled := harness.env.serveRequest(request)
	if canceled.Body.Len() != 0 || len(canceled.Header()) != 0 {
		t.Fatalf("canceled client received response: headers=%v body=%s", canceled.Header(), canceled.Body.String())
	}
	after, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Revision != tested.Revision+2 || after.State.TestNonceHash != "" ||
		after.State.TestExpiresAt != "" {
		t.Fatalf("context receipt compensation = %+v", after.State)
	}

	appOnboardingTestAfterCommit = func() {}
	retry := harness.env.serve(http.MethodPost, "/onboarding/test-chat",
		`{"revision":8,"message":"xin chào lần nữa"}`)
	if retry.Code != http.StatusOK {
		t.Fatalf("synthetic receipt retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	retried, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil || retried.State.Revision != 9 || retried.State.TestNonceHash == "" {
		t.Fatalf("context receipt retry = %+v, %v", retried.State, err)
	}
}

func TestRuntimeOnboardingBackLostResponseUsesContextCatalog(t *testing.T) {
	harness := newRuntimeOnboardingHarness(t, nil)
	snapshot := harness.prepareReadyRoute(t,
		[]string{"future-cli"},
		map[string]string{"future-cli": "future-model"},
	)
	first := harness.env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":5}`)
	if first.Code != http.StatusOK {
		t.Fatalf("Back status=%d body=%s snapshot=%+v", first.Code, first.Body.String(), snapshot)
	}
	back := decodeOnboardingResponse[appOnboardingStatusWire](t, first)
	if back.Phase != store.OnboardingPhaseProvider || back.Revision != 6 ||
		len(back.Providers) != 1 || back.Providers[0].Kind != "future-cli" {
		t.Fatalf("Back response = %+v", back)
	}
	retry := harness.env.serve(http.MethodPost, "/onboarding/back-to-providers", `{"revision":5}`)
	if retry.Code != http.StatusOK {
		t.Fatalf("Back lost-response retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	replayed := decodeOnboardingResponse[appOnboardingStatusWire](t, retry)
	if replayed.Revision != back.Revision || replayed.Phase != back.Phase ||
		!slices.Equal(replayed.Providers, back.Providers) {
		t.Fatalf("Back lost-response replay = %+v; want %+v", replayed, back)
	}
}

func TestRuntimeOnboardingHTTPMissingRegistrationIsPrivateBeforeFactories(t *testing.T) {
	var factoryCalls int
	harness := newRuntimeOnboardingHarness(t, func(registrations []appProviderRuntimeRegistration) {
		codex := runtimeOnboardingRegistration(t, registrations, "codex")
		codex.NewOnboardingMember = func(
			*api,
			store.OnboardingTestRouteEntry,
		) (appOnboardingMemberRun, error) {
			factoryCalls++
			return nil, errors.New("must not be called")
		}
	})
	snapshot := harness.prepareReadyRoute(t,
		[]string{"codex", "future-cli"},
		map[string]string{"codex": "codex-model", "future-cli": "future-model"},
	)
	const persona = "Persona hoàn chỉnh.\n"
	configureAgentPersonaForOnboardingTest(t, harness.env, persona)
	if err := harness.env.a.st.SetAgentDisplayName("Bé Mi"); err != nil {
		t.Fatal(err)
	}
	tested, err := harness.env.a.st.AdvanceOnboardingPersona(
		snapshot.State.Revision,
		agentPersonaFingerprint([]byte(persona), "Bé Mi"),
		"Bé Mi",
	)
	if err != nil {
		t.Fatal(err)
	}
	delete(harness.ctx.registry.byKind, "future-cli")
	rr := harness.env.serve(http.MethodPost, "/onboarding/test-chat",
		`{"revision":9,"message":"xin chào"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_STAGING_INVALID")
	if factoryCalls != 0 {
		t.Fatalf("earlier factory ran %d times before HTTP admission rejected later registration", factoryCalls)
	}
	if strings.Contains(rr.Body.String(), harness.env.dataDir) || strings.Contains(rr.Body.String(), "future-model") {
		t.Fatalf("private staging identity leaked: %s", rr.Body.String())
	}
	after, err := harness.ctx.onboardingStore().OnboardingSnapshot()
	if err != nil || after.State != tested || after.State.TestNonceHash != "" {
		t.Fatalf("missing registration mutated state: before=%+v after=%+v err=%v", tested, after.State, err)
	}
}
