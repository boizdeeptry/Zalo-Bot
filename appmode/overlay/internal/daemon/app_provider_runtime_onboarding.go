package daemon

import (
	"context"
	"net/http"
	"time"

	"agentdc/internal/store"
)

// The *api methods below are compatibility delegates for direct-handler tests
// and older internal callers. Registered production and synthetic routes bind
// the immutable runtime context directly, so a request cannot fall back to a
// different Catalog between onboarding phases.
func (a *api) handleOnboardingStatus(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleOnboardingStatus(w, r)
}

func (a *api) handleOnboardingProviders(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleOnboardingProviders(w, r)
}

func (a *api) handleOnboardingProvider(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleOnboardingProvider(w, r)
}

func (a *api) handleOnboardingSetup(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleOnboardingSetup(w, r)
}

func (a *api) handleOnboardingTestChat(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleOnboardingTestChat(w, r)
}

func (a *api) handleOnboardingBackToProviders(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleOnboardingBackToProviders(w, r)
}

func (a *api) handleOnboardingComplete(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleOnboardingComplete(w, r)
}

func (a *api) handleOnboardingRestart(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleOnboardingRestart(w, r)
}

func (a *api) handleAgentPut(w http.ResponseWriter, r *http.Request) {
	productionAppRuntimeContext(a).handleAgentPut(w, r)
}

func (a *api) preflightOnboardingTestChat(
	ctx context.Context,
	expectedRevision int64,
) (store.OnboardingTestRoute, []byte, error) {
	return productionAppRuntimeContext(a).preflightOnboardingTestChat(ctx, expectedRevision)
}

func (a *api) preflightOnboardingComplete(
	ctx context.Context,
	w http.ResponseWriter,
	expectedRevision int64,
	testToken string,
	now time.Time,
) (appOnboardingCompleteSnapshot, bool) {
	return productionAppRuntimeContext(a).preflightOnboardingComplete(
		ctx, w, expectedRevision, testToken, now,
	)
}

func (a *api) suggestedOnboardingProviderKind() (string, error) {
	return productionAppRuntimeContext(a).suggestedOnboardingProviderKind()
}

func (a *api) writeOnboardingStatus(w http.ResponseWriter, state store.OnboardingState) {
	productionAppRuntimeContext(a).writeOnboardingStatus(w, state)
}

func (a *api) writeOnboardingSnapshot(w http.ResponseWriter, snapshot store.OnboardingSnapshot) {
	productionAppRuntimeContext(a).writeOnboardingSnapshot(w, snapshot)
}
