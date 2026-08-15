package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"agentdc/internal/store"
)

type appOnboardingReadyMember struct {
	Entry store.OnboardingTestRouteEntry
	Run   func(context.Context, string, func(string)) (string, error)
}

type appOnboardingReadyRunner struct {
	displayName string
	members     []appOnboardingReadyMember
}

func newAppOnboardingReadyRunner(
	displayName string,
	members []appOnboardingReadyMember,
) *appOnboardingReadyRunner {
	cloned := append([]appOnboardingReadyMember(nil), members...)
	return &appOnboardingReadyRunner{displayName: displayName, members: cloned}
}

// Run uses the immutable staged order. A member failure is local to that
// member and falls through; cancellation or deadline of the parent request is
// terminal and is never misclassified as a Provider failure.
func (r *appOnboardingReadyRunner) Run(
	ctx context.Context,
	prompt string,
	step func(string),
) (appOnboardingTestResult, error) {
	if r == nil || len(r.members) == 0 {
		return appOnboardingTestResult{}, errors.New("onboarding provider fallback unavailable")
	}
	var firstSemanticInvalid *appOnboardingTestResult
	for _, member := range r.members {
		if err := ctx.Err(); err != nil {
			return appOnboardingTestResult{}, err
		}
		answer, err := member.Run(ctx, prompt, step)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return appOnboardingTestResult{}, ctxErr
		}
		if err == nil {
			result := appOnboardingTestResult{
				Answer: strings.TrimSpace(answer), ProviderID: member.Entry.ProviderID,
				ModelID: member.Entry.ModelID, Position: member.Entry.Position,
			}
			if result.Answer != "" && normalizedOnboardingAnswerContainsName(result.Answer, r.displayName) {
				return result, nil
			}
			if firstSemanticInvalid == nil {
				captured := result
				firstSemanticInvalid = &captured
			}
			continue
		}
	}
	if firstSemanticInvalid != nil {
		return *firstSemanticInvalid, nil
	}
	return appOnboardingTestResult{}, errors.New("onboarding provider fallback exhausted")
}

func newAppOnboardingTestRunner(
	_ context.Context,
	runtimeContext appRuntimeContext,
	route store.OnboardingTestRoute,
) (*appOnboardingReadyRunner, error) {
	a := runtimeContext.api
	if a == nil || len(route.Entries) == 0 {
		return nil, store.ErrOnboardingInvalidStagingOwnership
	}
	factories, err := captureRuntimeOnboardingMemberFactories(runtimeContext.registry, route)
	if err != nil {
		return nil, err
	}
	// Rooted config inspection is deliberately a separate complete pass. A
	// missing/tampered later registration is therefore rejected before any
	// earlier path is opened or any factory callback is invoked.
	for _, entry := range route.Entries {
		if err := validateRuntimeOnboardingStagingConfigDir(
			runtimeContext.registry,
			a.cfg.Dir,
			store.OnboardingStagingAccount{
				AccountID: entry.AccountID, ProviderID: entry.ProviderID,
				ProviderKind: entry.Kind, ConfigDir: entry.ConfigDir,
			},
		); err != nil {
			return nil, err
		}
	}
	members := make([]appOnboardingReadyMember, 0, len(route.Entries))
	for index, entry := range route.Entries {
		run, err := factories[index](a, entry)
		if err != nil || run == nil {
			return nil, store.ErrOnboardingInvalidStagingOwnership
		}
		members = append(members, appOnboardingReadyMember{
			Entry: entry,
			Run:   run,
		})
	}
	return newAppOnboardingReadyRunner(route.DisplayName, members), nil
}

func validateRuntimeOnboardingRouteRegistrations(
	registry appProviderRuntimeRegistry,
	route store.OnboardingTestRoute,
) error {
	_, err := captureRuntimeOnboardingMemberFactories(registry, route)
	return err
}

func captureRuntimeOnboardingMemberFactories(
	registry appProviderRuntimeRegistry,
	route store.OnboardingTestRoute,
) ([]appOnboardingMemberFactory, error) {
	if !registry.valid || len(route.Entries) == 0 {
		return nil, store.ErrOnboardingInvalidStagingOwnership
	}
	factories := make([]appOnboardingMemberFactory, len(route.Entries))
	kinds := make([]string, len(route.Entries))
	for index, entry := range route.Entries {
		kinds[index] = entry.Kind
		if entry.Position != index || entry.ProviderID != entry.Kind || entry.AccountID == "" ||
			entry.ModelID == "" || entry.ConfigDir == "" || !registry.supportsOnboarding(entry.Kind) {
			return nil, store.ErrOnboardingInvalidStagingOwnership
		}
		factory, ok := registry.onboardingMemberFactory(entry.Kind)
		if !ok || factory == nil {
			return nil, store.ErrOnboardingInvalidStagingOwnership
		}
		factories[index] = factory
	}
	canonical, err := registry.canonicalOnboardingKinds(kinds)
	if err != nil || !slices.Equal(canonical, kinds) {
		return nil, store.ErrOnboardingInvalidStagingOwnership
	}
	return factories, nil
}

type appCodexOnboardingGenerate func(
	context.Context,
	*http.Client,
	string,
	string,
	string,
) (string, int, error)

func newAppCodexOnboardingMemberFactory(
	client *http.Client,
	generate appCodexOnboardingGenerate,
) appOnboardingMemberFactory {
	return func(a *api, entry store.OnboardingTestRouteEntry) (appOnboardingMemberRun, error) {
		if a == nil || client == nil || generate == nil ||
			entry.Kind != "codex" || entry.ProviderID != "codex" {
			return nil, store.ErrOnboardingInvalidStagingOwnership
		}
		staged := store.OnboardingStagingAccount{
			AccountID: entry.AccountID, ProviderID: entry.ProviderID,
			ProviderKind: entry.Kind, ConfigDir: entry.ConfigDir,
		}
		logger := a.logger
		if logger == nil {
			logger = slog.Default()
		}
		adapter := &codexProxyAdapter{
			providerID: entry.ProviderID,
			logger:     logger,
			client:     client,
			generate:   generate,
		}
		adapter.pickConfigDir = makeOnboardingPinnedConfigDir(staged)
		return func(ctx context.Context, prompt string, _ func(string)) (string, error) {
			response, err := adapter.Generate(ctx, llmRequest{Model: entry.ModelID, Prompt: prompt}, nil)
			if err != nil {
				return "", err
			}
			return response.Text, nil
		}, nil
	}
}

func appProductionCodexOnboardingMemberFactory(
	a *api,
	entry store.OnboardingTestRouteEntry,
) (appOnboardingMemberRun, error) {
	return newAppCodexOnboardingMemberFactory(
		&http.Client{Timeout: defaultLLMProviderTimeout},
		codexProxyGenerate,
	)(a, entry)
}

func appProductionClaudeOnboardingMemberFactory(
	a *api,
	entry store.OnboardingTestRouteEntry,
) (appOnboardingMemberRun, error) {
	if a == nil || entry.Kind != "claude-code" || entry.ProviderID != "claude-code" {
		return nil, store.ErrOnboardingInvalidStagingOwnership
	}
	staged := store.OnboardingStagingAccount{
		AccountID: entry.AccountID, ProviderID: entry.ProviderID,
		ProviderKind: entry.Kind, ConfigDir: entry.ConfigDir,
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, prompt string, step func(string)) (string, error) {
		if a.zalo == nil {
			return "", errors.New("onboarding persona runtime unavailable")
		}
		program, prefixArgs, err := resolveCLIProgramContext(ctx, cliDescriptors["claude-code"])
		if err != nil {
			return "", fmt.Errorf("resolve onboarding staged runtime: %w", err)
		}
		runner := appOnboardingPinnedClaudeRunner(
			a.zalo.cfg, staged, entry.ModelID, program, prefixArgs, logger,
		)
		return runner.Run(ctx, prompt, step)
	}, nil
}
