package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

var appOnboardingNewLLMAdapter = newLLMAdapter

func newAppOnboardingTestRunner(
	_ context.Context,
	a *api,
	route store.OnboardingTestRoute,
) (*appOnboardingReadyRunner, error) {
	if a == nil || len(route.Entries) == 0 {
		return nil, store.ErrOnboardingInvalidStagingOwnership
	}
	members := make([]appOnboardingReadyMember, 0, len(route.Entries))
	for index, entry := range route.Entries {
		if entry.Position != index || entry.ProviderID != entry.Kind || entry.AccountID == "" ||
			entry.ModelID == "" || entry.ConfigDir == "" || !appProviderSupportsOnboarding(entry.Kind) {
			return nil, store.ErrOnboardingInvalidStagingOwnership
		}
		entry := entry
		members = append(members, appOnboardingReadyMember{
			Entry: entry,
			Run: func(ctx context.Context, prompt string, step func(string)) (string, error) {
				return runAppOnboardingReadyMember(ctx, a, entry, prompt, step)
			},
		})
	}
	return newAppOnboardingReadyRunner(route.DisplayName, members), nil
}

func runAppOnboardingReadyMember(
	ctx context.Context,
	a *api,
	entry store.OnboardingTestRouteEntry,
	prompt string,
	step func(string),
) (string, error) {
	staged := store.OnboardingStagingAccount{
		AccountID: entry.AccountID, ProviderID: entry.ProviderID,
		ProviderKind: entry.Kind, ConfigDir: entry.ConfigDir,
	}
	logger := a.logger
	if logger == nil {
		logger = slog.Default()
	}
	switch entry.Kind {
	case "codex":
		adapter, ok := appOnboardingNewLLMAdapter(entry.Kind, entry.ProviderID,
			&http.Client{Timeout: defaultLLMProviderTimeout}, logger)
		if !ok {
			return "", errors.New("onboarding staged Provider unavailable")
		}
		switch pinned := adapter.(type) {
		case *codexProxyAdapter:
			pinned.pickConfigDir = makeOnboardingPinnedConfigDir(staged)
		case *cliAdapter:
			pinned.accountEnv = makeOnboardingPinnedAccountEnv(staged)
		default:
			return "", errors.New("onboarding staged Provider adapter invalid")
		}
		response, err := adapter.Generate(ctx, llmRequest{Model: entry.ModelID, Prompt: prompt}, nil)
		if err != nil {
			return "", err
		}
		return response.Text, nil
	case "claude-code":
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
	default:
		return "", store.ErrOnboardingProviderUnsupported
	}
}
