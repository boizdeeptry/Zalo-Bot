package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentdc/internal/store"
)

const (
	runtimeLiveFutureKind  = "future-cli"
	runtimeLiveFutureModel = "future-model"
)

func runtimeLiveRegistry(
	t *testing.T,
	mutate func([]appProviderRuntimeRegistration),
) appProviderRuntimeRegistry {
	t.Helper()
	registrations := appRuntimeSyntheticRegistrations()
	if mutate != nil {
		mutate(registrations)
	}
	registry, err := newAppProviderRuntimeRegistry(appRuntimeSyntheticOptions(), registrations)
	if err != nil {
		t.Fatalf("newAppProviderRuntimeRegistry() = %v", err)
	}
	return registry
}

func runtimeLiveContext(
	t *testing.T,
	env *onboardingRouteTestEnv,
	registry appProviderRuntimeRegistry,
) appRuntimeContext {
	t.Helper()
	return appRuntimeContext{
		api: env.a, registry: registry, connect: env.a.newConnectManager(),
	}
}

func runtimeLiveServe(
	t *testing.T,
	ctx appRuntimeContext,
	method string,
	path string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return runtimeLiveServeHandler(t, runtimeLiveHandler(t, ctx), method, path, body)
}

func runtimeLiveHandler(t *testing.T, ctx appRuntimeContext) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	registerAppRoutesWithContext(mux, ctx)
	return mux
}

func runtimeLiveServeHandler(
	t *testing.T,
	handler http.Handler,
	method string,
	path string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set(portalHeader, "1")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func runtimeLiveSeedAccountRuntime(
	t *testing.T,
	env *onboardingRouteTestEnv,
	kind string,
	displayName string,
	modelID string,
	accountEnabled bool,
) {
	t.Helper()
	if err := env.a.st.EnsureAccountRuntimeProvider(kind, displayName); err != nil {
		t.Fatalf("EnsureAccountRuntimeProvider(%q) = %v", kind, err)
	}
	accountID := kind + "-live-account"
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: accountID, ProviderID: kind, Label: "Live account",
		ConfigDir: env.dataDir, Enabled: accountEnabled,
	}); err != nil {
		t.Fatalf("CreateLLMAccount(%q) = %v", kind, err)
	}
	if modelID != "" {
		if err := env.a.st.AddLLMModel(store.LLMModel{
			ProviderID: kind, ModelID: modelID, Name: "Live model",
			Source: store.LLMModelManual, Available: true,
		}); err != nil {
			t.Fatalf("AddLLMModel(%q/%q) = %v", kind, modelID, err)
		}
	}
}

func runtimeLiveSaveRoute(
	t *testing.T,
	env *onboardingRouteTestEnv,
	entries ...store.LLMRouteEntry,
) store.LLMRouteSnapshot {
	t.Helper()
	ensureActiveCombo(t, env.a.st)
	current, err := env.a.st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := env.a.st.ReplaceLLMRoute(current.Revision, entries)
	if err != nil {
		t.Fatalf("ReplaceLLMRoute(%+v) = %v", entries, err)
	}
	return saved
}

func runtimeLiveWait(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func runtimeLiveReceive[T any](t *testing.T, ch <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out receiving %s", label)
		var zero T
		return zero
	}
}

func TestRuntimeLiveSyntheticProviderAnswersThroughAppZaloRunner(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	answer := okAdapter("future live answer")
	var factoryCalls atomic.Int32
	registry := runtimeLiveRegistry(t, func(registrations []appProviderRuntimeRegistration) {
		future := appRuntimeRegistrationIndex(t, registrations, runtimeLiveFutureKind)
		registrations[future].NewAdapter = func(
			providerID string,
			_ *store.Store,
			_ *http.Client,
			_ *slog.Logger,
		) (providerAdapter, error) {
			if providerID != runtimeLiveFutureKind {
				t.Fatalf("future factory providerID = %q", providerID)
			}
			factoryCalls.Add(1)
			return answer, nil
		}
	})
	ctx := runtimeLiveContext(t, env, registry)
	runtimeLiveSeedAccountRuntime(
		t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, true,
	)
	runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: runtimeLiveFutureKind, ModelID: runtimeLiveFutureModel, Enabled: true,
	})

	runner := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread-future", false)
	got, err := runner.Run(t.Context(), "private prompt", func(string) {})
	if err != nil {
		t.Fatalf("synthetic appZaloRunner.Run() = %v", err)
	}
	if got != "future live answer" || factoryCalls.Load() != 1 || len(answer.seen()) != 1 {
		t.Fatalf("answer=%q factoryCalls=%d adapterCalls=%d", got, factoryCalls.Load(), len(answer.seen()))
	}
}

func TestRuntimeLiveReadinessUsesConnectionModeNotStaticKinds(t *testing.T) {
	t.Run("account mode uses exact singleton and enabled Account, not Provider enabled", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		ctx := runtimeLiveContext(t, env, runtimeLiveRegistry(t, nil))
		runtimeLiveSeedAccountRuntime(
			t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, true,
		)
		if _, err := env.db.Exec(`UPDATE llm_providers SET enabled = 0 WHERE id = ?`, runtimeLiveFutureKind); err != nil {
			t.Fatal(err)
		}
		if !ctx.hasAnyConnectedProvider() {
			t.Fatal("disabled account-mode Provider hid its enabled exact Account from readiness")
		}
	})

	t.Run("account mode rejects a forged sibling singleton", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		ctx := runtimeLiveContext(t, env, runtimeLiveRegistry(t, nil))
		runtimeLiveSeedAccountRuntime(
			t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, true,
		)
		if _, err := env.db.Exec(`INSERT INTO llm_providers(id,name,kind,enabled) VALUES(?,?,?,1)`,
			"future-sibling", "Forged sibling", runtimeLiveFutureKind); err != nil {
			t.Fatal(err)
		}
		if ctx.hasAnyConnectedProvider() {
			t.Fatal("corrupt account-runtime sibling contributed readiness")
		}
	})

	t.Run("account mode ignores an exact singleton with no enabled Account", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		ctx := runtimeLiveContext(t, env, runtimeLiveRegistry(t, nil))
		runtimeLiveSeedAccountRuntime(
			t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, false,
		)
		if ctx.hasAnyConnectedProvider() {
			t.Fatal("account runtime with only disabled Accounts contributed readiness")
		}
	})

	t.Run("credential mode ignores Provider enabled", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		ctx := runtimeLiveContext(t, env, runtimeLiveRegistry(t, nil))
		if err := env.a.st.CreateLLMProvider(store.LLMProvider{
			ID: "openai-disabled", Kind: "openai", Name: "OpenAI", Enabled: false,
		}); err != nil {
			t.Fatal(err)
		}
		if err := env.a.st.SetLLMCredentialCipher("openai-disabled", []byte("opaque-cipher")); err != nil {
			t.Fatal(err)
		}
		if !ctx.hasAnyConnectedProvider() {
			t.Fatal("configured credential runtime did not contribute readiness while disabled")
		}
	})

	t.Run("credential mode requires a configured credential", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		ctx := runtimeLiveContext(t, env, runtimeLiveRegistry(t, nil))
		if err := env.a.st.CreateLLMProvider(store.LLMProvider{
			ID: "openai-empty", Kind: "openai", Name: "OpenAI", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		if ctx.hasAnyConnectedProvider() {
			t.Fatal("credential runtime without a credential contributed readiness")
		}
	})

	t.Run("hidden and unknown kinds never contribute readiness", func(t *testing.T) {
		for _, kind := range []string{"gemini-cli", "opencode", "unknown-provider"} {
			t.Run(kind, func(t *testing.T) {
				env := newOnboardingRouteTestEnv(t)
				ctx := runtimeLiveContext(t, env, runtimeLiveRegistry(t, nil))
				if err := env.a.st.EnsureAccountRuntimeProvider(kind, "Hidden fixture"); err != nil {
					t.Fatal(err)
				}
				if err := env.a.st.CreateLLMAccount(store.LLMAccount{
					ID: kind + "-account", ProviderID: kind, Label: "Hidden",
					ConfigDir: env.dataDir, Enabled: true,
				}); err != nil {
					t.Fatal(err)
				}
				if err := env.a.st.SetLLMCredentialCipher(kind, []byte("opaque-cipher")); err != nil {
					t.Fatal(err)
				}
				if ctx.hasAnyConnectedProvider() {
					t.Fatalf("%q contributed readiness", kind)
				}
			})
		}
	})
}

func runtimeLiveSeedProductionRouteMembers(t *testing.T, env *onboardingRouteTestEnv) {
	t.Helper()
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
	runtimeLiveSeedAccountRuntime(t, env, "claude-code", "Claude Code", "claude-model", true)
}

func TestRuntimeLiveRejectsTerminalBeforeLastOnReplaceActivateAndRun(t *testing.T) {
	terminalFirst := []store.LLMRouteEntry{
		{ProviderID: "claude-code", ModelID: "claude-model", Enabled: true},
		{ProviderID: "codex", ModelID: "codex-model", Enabled: true},
	}

	t.Run("legacy route PUT", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		runtimeLiveSeedProductionRouteMembers(t, env)
		current := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: "codex", ModelID: "codex-model", Enabled: true,
		})
		ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
		body := fmt.Sprintf(`{"revision":%d,"entries":[`+
			`{"provider_id":"claude-code","model_id":"claude-model","enabled":true},`+
			`{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`, current.Revision)
		rr := runtimeLiveServe(t, ctx, http.MethodPut, "/llm/route", body)
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "ROUTE_RUNTIME_INVALID") {
			t.Fatalf("legacy PUT status=%d body=%s", rr.Code, rr.Body.String())
		}
		after, err := env.a.st.LLMRoute()
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != current.Revision || !slices.Equal(after.Entries, current.Entries) {
			t.Fatalf("rejected legacy PUT mutated route: before=%+v after=%+v", current, after)
		}
	})

	t.Run("combo replace", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		runtimeLiveSeedProductionRouteMembers(t, env)
		current := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: "codex", ModelID: "codex-model", Enabled: true,
		})
		ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
		body := fmt.Sprintf(`{"revision":%d,"type":"fallback","entries":[`+
			`{"provider_id":"claude-code","model_id":"claude-model","enabled":true},`+
			`{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`, current.Revision)
		rr := runtimeLiveServe(t, ctx, http.MethodPut, "/llm/combos/"+current.ComboID, body)
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "COMBO_RUNTIME_INVALID") {
			t.Fatalf("replace status=%d body=%s", rr.Code, rr.Body.String())
		}
		after, err := env.a.st.LLMRoute()
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != current.Revision || !slices.Equal(after.Entries, current.Entries) {
			t.Fatalf("rejected replace mutated route: before=%+v after=%+v", current, after)
		}
	})

	t.Run("combo activation", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		runtimeLiveSeedProductionRouteMembers(t, env)
		active := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: "codex", ModelID: "codex-model", Enabled: true,
		})
		candidate, err := env.a.st.CreateLLMCombo("Terminal first", "fallback")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.a.st.ReplaceLLMComboMembers(
			candidate.ID, candidate.Revision, candidate.Type, terminalFirst,
		); err != nil {
			t.Fatal(err)
		}
		ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
		rr := runtimeLiveServe(t, ctx, http.MethodPost, "/llm/combos/"+candidate.ID+"/activate", `{}`)
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "COMBO_RUNTIME_INVALID") {
			t.Fatalf("activate status=%d body=%s", rr.Code, rr.Body.String())
		}
		after, err := env.a.st.LLMRoute()
		if err != nil {
			t.Fatal(err)
		}
		if after.ComboID != active.ComboID {
			t.Fatalf("invalid activation changed active combo from %q to %q", active.ComboID, after.ComboID)
		}
	})

	t.Run("live capture before factories", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		runtimeLiveSeedProductionRouteMembers(t, env)
		runtimeLiveSaveRoute(t, env, terminalFirst...)
		var adapterFactories atomic.Int32
		registrations := appProductionProviderRuntimeRegistrations()
		codex := appRuntimeRegistrationIndex(t, registrations, "codex")
		registrations[codex].NewAdapter = func(
			string, *store.Store, *http.Client, *slog.Logger,
		) (providerAdapter, error) {
			adapterFactories.Add(1)
			return okAdapter("must not run"), nil
		}
		registry, err := newAppProviderRuntimeRegistry(
			productionAppProviderRuntimeRegistry().Catalog().Options(), registrations,
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx := runtimeLiveContext(t, env, registry)
		_, err = ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false).
			Run(t.Context(), "private prompt", func(string) {})
		if !errors.Is(err, ErrZaloSilent) {
			t.Fatalf("invalid live route error = %v; want ErrZaloSilent", err)
		}
		if adapterFactories.Load() != 0 {
			t.Fatalf("adapter factories ran before whole-route validation: %d", adapterFactories.Load())
		}
	})

	t.Run("disabled terminal is unusable", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		runtimeLiveSeedProductionRouteMembers(t, env)
		runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: "claude-code", ModelID: "claude-model", Enabled: true,
		})
		if _, err := env.db.Exec(`UPDATE llm_providers SET enabled = 0 WHERE id = 'claude-code'`); err != nil {
			t.Fatal(err)
		}
		var terminalCalls atomic.Int32
		registrations := appProductionProviderRuntimeRegistrations()
		claude := appRuntimeRegistrationIndex(t, registrations, "claude-code")
		registrations[claude].Terminal = func(
			*appLLMRunner, context.Context, store.LLMRouteEntry, appLLMRouteInput, func(string),
		) (appZaloRunResult, error) {
			terminalCalls.Add(1)
			return appZaloRunResult{Answer: "must not run"}, nil
		}
		registry, err := newAppProviderRuntimeRegistry(
			productionAppProviderRuntimeRegistry().Catalog().Options(), registrations,
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx := runtimeLiveContext(t, env, registry)
		_, err = ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false).
			Run(t.Context(), "private prompt", func(string) {})
		if err == nil {
			t.Fatal("route with only a disabled terminal unexpectedly answered")
		}
		if terminalCalls.Load() != 0 {
			t.Fatalf("disabled terminal calls=%d; want 0", terminalCalls.Load())
		}
	})
}

func TestRuntimeLiveLegacyRoutePutUsesSameLockAndValidation(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	runtimeLiveSeedProductionRouteMembers(t, env)
	active := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: "codex", ModelID: "codex-model", Enabled: true,
	})
	candidate, err := env.a.st.CreateLLMCombo("Candidate", "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.a.st.ReplaceLLMComboMembers(candidate.ID, candidate.Revision, candidate.Type,
		[]store.LLMRouteEntry{{ProviderID: "claude-code", ModelID: "claude-model", Enabled: true}}); err != nil {
		t.Fatal(err)
	}

	firstWritten := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondBeforeLock := make(chan struct{})
	secondAfterLock := make(chan struct{})
	var firstOnce, secondBeforeOnce, secondAfterOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(release)
	ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
	ctx.routeCheckpoint = func(operation, phase string) {
		switch {
		case operation == "route-put" && phase == "written":
			firstOnce.Do(func() { close(firstWritten) })
			<-releaseFirst
		case operation == "combo-activate" && phase == "before_lock":
			secondBeforeOnce.Do(func() { close(secondBeforeLock) })
		case operation == "combo-activate" && phase == "locked":
			secondAfterOnce.Do(func() { close(secondAfterLock) })
		}
	}
	handler := runtimeLiveHandler(t, ctx)

	putDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		putDone <- runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
			`{"revision":%d,"entries":[{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`,
			active.Revision,
		))
	}()
	runtimeLiveWait(t, firstWritten, "legacy route written barrier")
	if appLLMRouteMutationMu.TryLock() {
		appLLMRouteMutationMu.Unlock()
		t.Fatal("legacy PUT did not hold the route fence through its Store write")
	}
	activateDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		activateDone <- runtimeLiveServeHandler(t, handler, http.MethodPost, "/llm/combos/"+candidate.ID+"/activate", `{}`)
	}()
	runtimeLiveWait(t, secondBeforeLock, "combo activation before-lock barrier")
	release()
	if rr := runtimeLiveReceive(t, putDone, "legacy PUT response"); rr.Code != http.StatusOK {
		t.Fatalf("legacy PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := runtimeLiveReceive(t, activateDone, "activation response"); rr.Code != http.StatusOK {
		t.Fatalf("activation status=%d body=%s", rr.Code, rr.Body.String())
	}
	runtimeLiveWait(t, secondAfterLock, "combo activation after-lock barrier")
	final, err := env.a.st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if final.ComboID != candidate.ID || len(final.Entries) != 1 ||
		final.Entries[0].ProviderID != "claude-code" {
		t.Fatalf("serialized final route = %+v; want activated candidate", final)
	}
}

func TestRuntimeLiveLogsGenericRouteReadFailure(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
	var logs bytes.Buffer
	env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
	ctx.routeCheckpoint = func(operation, phase string) {
		if operation == appLLMRouteOperationCapture && phase == appLLMRoutePhaseLocked {
			if err := env.a.st.Close(); err != nil {
				t.Errorf("close Store at route-read boundary: %v", err)
			}
		}
	}

	runner := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
	if _, ok := runner.(silentZaloRunner); !ok {
		t.Fatalf("route read failure runner=%T; want silentZaloRunner", runner)
	}
	got := logs.String()
	if !strings.Contains(got, "error_kind=route_read_failed") {
		t.Fatalf("route read failure log=%q; want generic error kind", got)
	}
	for _, private := range []string{env.dataDir, "portal.db", "database is closed"} {
		if strings.Contains(got, private) {
			t.Fatalf("route read failure log exposed private detail %q: %q", private, got)
		}
	}
}

func TestRuntimeLiveComboMutationsOwnRouteFenceThroughWrite(t *testing.T) {
	tests := []struct {
		name                string
		ownerOperation      string
		contenderOperation  string
		wantContenderStatus int
	}{
		{
			name: "combo replace owns fence", ownerOperation: "combo-replace",
			contenderOperation: "combo-activate", wantContenderStatus: http.StatusOK,
		},
		{
			name: "combo activate owns fence", ownerOperation: "combo-activate",
			contenderOperation: "route-put", wantContenderStatus: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			runtimeLiveSeedProductionRouteMembers(t, env)
			active := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
				ProviderID: "codex", ModelID: "codex-model", Enabled: true,
			})
			candidate, err := env.a.st.CreateLLMCombo("Candidate", "fallback")
			if err != nil {
				t.Fatal(err)
			}
			candidate, err = env.a.st.ReplaceLLMComboMembers(
				candidate.ID, candidate.Revision, candidate.Type,
				[]store.LLMRouteEntry{{ProviderID: "claude-code", ModelID: "claude-model", Enabled: true}},
			)
			if err != nil {
				t.Fatal(err)
			}

			ownerWritten := make(chan struct{})
			releaseOwner := make(chan struct{})
			contenderBefore := make(chan struct{})
			contenderLocked := make(chan struct{})
			var ownerOnce, beforeOnce, lockedOnce, releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(releaseOwner) }) })
			ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
			ctx.routeCheckpoint = func(operation, phase string) {
				switch {
				case operation == test.ownerOperation && phase == "written":
					ownerOnce.Do(func() { close(ownerWritten) })
					<-releaseOwner
				case operation == test.contenderOperation && phase == "before_lock":
					beforeOnce.Do(func() { close(contenderBefore) })
				case operation == test.contenderOperation && phase == "locked":
					lockedOnce.Do(func() { close(contenderLocked) })
				}
			}
			handler := runtimeLiveHandler(t, ctx)

			ownerDone := make(chan *httptest.ResponseRecorder, 1)
			if test.ownerOperation == "combo-replace" {
				go func() {
					ownerDone <- runtimeLiveServeHandler(t, handler, http.MethodPut,
						"/llm/combos/"+active.ComboID, fmt.Sprintf(
							`{"revision":%d,"type":"fallback","entries":[{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`,
							active.Revision,
						))
				}()
			} else {
				go func() {
					ownerDone <- runtimeLiveServeHandler(t, handler, http.MethodPost,
						"/llm/combos/"+candidate.ID+"/activate", `{}`)
				}()
			}
			runtimeLiveWait(t, ownerWritten, "combo owner written barrier")
			if appLLMRouteMutationMu.TryLock() {
				appLLMRouteMutationMu.Unlock()
				t.Fatal("combo mutation released route fence before its Store write checkpoint")
			}

			contenderDone := make(chan *httptest.ResponseRecorder, 1)
			if test.contenderOperation == "combo-activate" {
				go func() {
					contenderDone <- runtimeLiveServeHandler(t, handler, http.MethodPost,
						"/llm/combos/"+candidate.ID+"/activate", `{}`)
				}()
			} else {
				go func() {
					contenderDone <- runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
						`{"revision":%d,"entries":[{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`,
						active.Revision,
					))
				}()
			}
			runtimeLiveWait(t, contenderBefore, "contender before combo owner fence")
			releaseOnce.Do(func() { close(releaseOwner) })
			ownerResponse := runtimeLiveReceive(t, ownerDone, "combo owner response")
			contenderResponse := runtimeLiveReceive(t, contenderDone, "combo contender response")
			runtimeLiveWait(t, contenderLocked, "contender after combo owner fence")
			if ownerResponse.Code != http.StatusOK {
				t.Fatalf("owner status=%d body=%s", ownerResponse.Code, ownerResponse.Body.String())
			}
			if contenderResponse.Code != test.wantContenderStatus {
				t.Fatalf("contender status=%d body=%s want=%d",
					contenderResponse.Code, contenderResponse.Body.String(), test.wantContenderStatus)
			}
		})
	}
}

func TestRuntimeLiveRouteMutationIsAtomicWithComplete(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedOnboardingRoute(t, env, "old-live", "openai", true)
	oldRoute, err := env.a.st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	token, now := seedAppOnboardingCompletionMulti(t, env, 181)
	oldNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now }
	t.Cleanup(func() { appOnboardingCompleteNow = oldNow })

	completeWritten := make(chan struct{})
	releaseComplete := make(chan struct{})
	writerBeforeLock := make(chan struct{})
	writerLocked := make(chan struct{})
	var completeOnce, beforeOnce, lockedOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseComplete) }) }
	t.Cleanup(release)
	ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
	ctx.routeCheckpoint = func(operation, phase string) {
		switch {
		case operation == "onboarding-complete" && phase == "written":
			completeOnce.Do(func() { close(completeWritten) })
			<-releaseComplete
		case operation == "route-put" && phase == "before_lock":
			beforeOnce.Do(func() { close(writerBeforeLock) })
		case operation == "route-put" && phase == "locked":
			lockedOnce.Do(func() { close(writerLocked) })
		}
	}
	handler := runtimeLiveHandler(t, ctx)

	completeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completeDone <- runtimeLiveServeHandler(t, handler, http.MethodPost, "/onboarding/complete", fmt.Sprintf(
			`{"revision":181,"test_token":%q}`, token,
		))
	}()
	runtimeLiveWait(t, completeWritten, "Complete written barrier")
	if onboardingMutationMu.TryLock() {
		onboardingMutationMu.Unlock()
		t.Fatal("Complete did not hold onboardingMutationMu before and throughout its route fence")
	}
	if appLLMRouteMutationMu.TryLock() {
		appLLMRouteMutationMu.Unlock()
		t.Fatal("Complete did not hold appLLMRouteMutationMu through its Store commit")
	}
	writerDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		writerDone <- runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
			`{"revision":%d,"entries":[{"provider_id":"old-live","model_id":"model","enabled":true}]}`,
			oldRoute.Revision,
		))
	}()
	runtimeLiveWait(t, writerBeforeLock, "legacy writer before Complete route lock")
	release()
	if rr := runtimeLiveReceive(t, completeDone, "Complete response"); rr.Code != http.StatusOK {
		t.Fatalf("Complete status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := runtimeLiveReceive(t, writerDone, "stale writer response"); rr.Code != http.StatusConflict ||
		!strings.Contains(rr.Body.String(), "ROUTE_REVISION_CONFLICT") {
		t.Fatalf("stale writer status=%d body=%s", rr.Code, rr.Body.String())
	}
	runtimeLiveWait(t, writerLocked, "legacy writer after Complete route lock")
	final, err := env.a.st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	want := []store.LLMRouteEntry{
		{Position: 0, ProviderID: "codex", ModelID: "codex-model", Enabled: true},
		{Position: 1, ProviderID: "claude-code", ModelID: "claude-model", Enabled: true},
	}
	if !slices.Equal(final.Entries, want) {
		t.Fatalf("final route=%+v; want exact completed staged route=%+v", final, want)
	}
}

func TestRuntimeLiveCompleteAcquiresOnboardingBeforeRouteFence(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	token, now := seedAppOnboardingCompletionMulti(t, env, 186)
	oldNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now }
	t.Cleanup(func() { appOnboardingCompleteNow = oldNow })

	beforeRouteLock := make(chan struct{})
	var beforeOnce sync.Once
	ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
	ctx.routeCheckpoint = func(operation, phase string) {
		if operation == "onboarding-complete" && phase == "before_lock" {
			beforeOnce.Do(func() { close(beforeRouteLock) })
		}
	}
	handler := runtimeLiveHandler(t, ctx)

	appLLMRouteMutationMu.Lock()
	routeReleased := false
	defer func() {
		if !routeReleased {
			appLLMRouteMutationMu.Unlock()
		}
	}()
	completeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completeDone <- runtimeLiveServeHandler(t, handler, http.MethodPost, "/onboarding/complete", fmt.Sprintf(
			`{"revision":186,"test_token":%q}`, token,
		))
	}()
	runtimeLiveWait(t, beforeRouteLock, "Complete before route lock")
	onboardingWasHeld := !onboardingMutationMu.TryLock()
	if !onboardingWasHeld {
		onboardingMutationMu.Unlock()
	}
	appLLMRouteMutationMu.Unlock()
	routeReleased = true
	rr := runtimeLiveReceive(t, completeDone, "lock-order Complete response")
	if !onboardingWasHeld {
		t.Fatal("Complete reached the route fence without first owning onboardingMutationMu")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("lock-order Complete status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRuntimeLiveCompleteSecondPreflightRechecksPersistedRoute(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	token, now := seedAppOnboardingCompletionMulti(t, env, 191)
	oldNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now }
	t.Cleanup(func() { appOnboardingCompleteNow = oldNow })
	ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
	oldAfterWait := appOnboardingProviderAfterTestWait
	appOnboardingProviderAfterTestWait = func() {
		if _, err := env.db.Exec(`UPDATE llm_models SET available = 0 WHERE provider_id = 'claude-code'`); err != nil {
			t.Errorf("persist staged model drift: %v", err)
		}
	}
	t.Cleanup(func() { appOnboardingProviderAfterTestWait = oldAfterWait })

	rr := runtimeLiveServe(t, ctx, http.MethodPost, "/onboarding/complete", fmt.Sprintf(
		`{"revision":191,"test_token":%q}`, token,
	))
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "ONBOARDING_CONFIGURATION_CHANGED") {
		t.Fatalf("second-preflight status=%d body=%s", rr.Code, rr.Body.String())
	}
	state := env.state(t)
	if state.Phase != store.OnboardingPhaseTest || state.Revision != 191 {
		t.Fatalf("persisted route drift committed onboarding: %+v", state)
	}
}

func TestRuntimeLiveFactoriesStayInsideCapturedRouteFence(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	factoryEntered := make(chan struct{})
	releaseFactory := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFactory) }) }
	t.Cleanup(release)
	registry := runtimeLiveRegistry(t, func(registrations []appProviderRuntimeRegistration) {
		future := appRuntimeRegistrationIndex(t, registrations, runtimeLiveFutureKind)
		registrations[future].NewAdapter = func(
			string, *store.Store, *http.Client, *slog.Logger,
		) (providerAdapter, error) {
			close(factoryEntered)
			<-releaseFactory
			return okAdapter("future"), nil
		}
	})
	ctx := runtimeLiveContext(t, env, registry)
	runtimeLiveSeedAccountRuntime(
		t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, true,
	)
	snapshot := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: runtimeLiveFutureKind, ModelID: runtimeLiveFutureModel, Enabled: true,
	})
	mutationBeforeLock := make(chan struct{})
	mutationLocked := make(chan struct{})
	var beforeOnce, lockedOnce sync.Once
	ctx.routeCheckpoint = func(operation, phase string) {
		if operation == "route-put" && phase == "before_lock" {
			beforeOnce.Do(func() { close(mutationBeforeLock) })
		}
		if operation == "route-put" && phase == "locked" {
			lockedOnce.Do(func() { close(mutationLocked) })
		}
	}
	handler := runtimeLiveHandler(t, ctx)
	built := make(chan zaloRunner, 1)
	go func() {
		built <- ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
	}()
	runtimeLiveWait(t, factoryEntered, "live adapter factory")
	if appLLMRouteMutationMu.TryLock() {
		appLLMRouteMutationMu.Unlock()
		t.Fatal("live factory did not retain the route fence during Store-backed construction")
	}
	putDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		putDone <- runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
			`{"revision":%d,"entries":[{"provider_id":"future-cli","model_id":"future-model","enabled":true}]}`,
			snapshot.Revision,
		))
	}()
	runtimeLiveWait(t, mutationBeforeLock, "route mutation before lock while live factory is blocked")
	release()
	runtimeLiveWait(t, mutationLocked, "route mutation after live factory capture")
	if rr := runtimeLiveReceive(t, putDone, "route mutation after factory capture"); rr.Code != http.StatusOK {
		t.Fatalf("route PUT status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := runtimeLiveReceive(t, built, "captured live runner").(*appLLMRunner); !ok {
		t.Fatal("live factory did not finish with an appLLMRunner")
	}
}

func TestRuntimeLiveCapturedRunnerKeepsExactRouteAcrossMutation(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	futureAnswer := okAdapter("future snapshot")
	codexAnswer := okAdapter("codex next snapshot")
	registry := runtimeLiveRegistry(t, func(registrations []appProviderRuntimeRegistration) {
		future := appRuntimeRegistrationIndex(t, registrations, runtimeLiveFutureKind)
		registrations[future].NewAdapter = func(
			string, *store.Store, *http.Client, *slog.Logger,
		) (providerAdapter, error) {
			return futureAnswer, nil
		}
		codex := appRuntimeRegistrationIndex(t, registrations, "codex")
		registrations[codex].NewAdapter = func(
			string, *store.Store, *http.Client, *slog.Logger,
		) (providerAdapter, error) {
			return codexAnswer, nil
		}
	})
	ctx := runtimeLiveContext(t, env, registry)
	runtimeLiveSeedAccountRuntime(
		t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, true,
	)
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
	before := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: runtimeLiveFutureKind, ModelID: runtimeLiveFutureModel, Enabled: true,
	})

	captured := make(chan struct{})
	releaseCapture := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCapture) }) }
	t.Cleanup(release)
	mutationBeforeLock := make(chan struct{})
	mutationLocked := make(chan struct{})
	var captureOnce, beforeOnce, lockedOnce sync.Once
	ctx.routeCheckpoint = func(operation, phase string) {
		switch {
		case operation == "live-capture" && phase == "captured":
			captureOnce.Do(func() { close(captured) })
			<-releaseCapture
		case operation == "route-put" && phase == "before_lock":
			beforeOnce.Do(func() { close(mutationBeforeLock) })
		case operation == "route-put" && phase == "locked":
			lockedOnce.Do(func() { close(mutationLocked) })
		}
	}
	handler := runtimeLiveHandler(t, ctx)
	oldRunner := make(chan zaloRunner, 1)
	go func() {
		oldRunner <- ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
	}()
	runtimeLiveWait(t, captured, "old live capture")
	if appLLMRouteMutationMu.TryLock() {
		appLLMRouteMutationMu.Unlock()
		t.Fatal("captured runner did not retain the route fence until publication")
	}
	putDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		putDone <- runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
			`{"revision":%d,"entries":[{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`,
			before.Revision,
		))
	}()
	runtimeLiveWait(t, mutationBeforeLock, "mutation behind captured live route")
	release()
	first := runtimeLiveReceive(t, oldRunner, "old captured runner")
	if rr := runtimeLiveReceive(t, putDone, "route mutation behind capture"); rr.Code != http.StatusOK {
		t.Fatalf("route mutation status=%d body=%s", rr.Code, rr.Body.String())
	}
	runtimeLiveWait(t, mutationLocked, "mutation after captured live route")
	got, err := first.Run(t.Context(), "old prompt", func(string) {})
	if err != nil || got != "future snapshot" {
		t.Fatalf("old captured runner result=%q err=%v", got, err)
	}
	next := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
	got, err = next.Run(t.Context(), "new prompt", func(string) {})
	if err != nil || got != "codex next snapshot" {
		t.Fatalf("next captured runner result=%q err=%v", got, err)
	}
	if len(futureAnswer.seen()) != 1 || len(codexAnswer.seen()) != 1 {
		t.Fatalf("snapshot adapter calls future=%d codex=%d", len(futureAnswer.seen()), len(codexAnswer.seen()))
	}
}

func TestRuntimeLiveReadsRouteOnlyAfterAcquiringCaptureFence(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	futureAnswer := okAdapter("stale route must not run")
	codexAnswer := okAdapter("post-lock route")
	registry := runtimeLiveRegistry(t, func(registrations []appProviderRuntimeRegistration) {
		future := appRuntimeRegistrationIndex(t, registrations, runtimeLiveFutureKind)
		registrations[future].NewAdapter = func(
			string, *store.Store, *http.Client, *slog.Logger,
		) (providerAdapter, error) {
			return futureAnswer, nil
		}
		codex := appRuntimeRegistrationIndex(t, registrations, "codex")
		registrations[codex].NewAdapter = func(
			string, *store.Store, *http.Client, *slog.Logger,
		) (providerAdapter, error) {
			return codexAnswer, nil
		}
	})
	ctx := runtimeLiveContext(t, env, registry)
	runtimeLiveSeedAccountRuntime(
		t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, true,
	)
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
	before := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: runtimeLiveFutureKind, ModelID: runtimeLiveFutureModel, Enabled: true,
	})

	beforeLock := make(chan struct{})
	var beforeOnce sync.Once
	ctx.routeCheckpoint = func(operation, phase string) {
		if operation == "live-capture" && phase == "before_lock" {
			beforeOnce.Do(func() { close(beforeLock) })
		}
	}
	appLLMRouteMutationMu.Lock()
	released := false
	defer func() {
		if !released {
			appLLMRouteMutationMu.Unlock()
		}
	}()
	built := make(chan zaloRunner, 1)
	go func() {
		built <- ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
	}()
	runtimeLiveWait(t, beforeLock, "live capture before route lock")
	if _, err := env.a.st.ReplaceLLMRoute(before.Revision, []store.LLMRouteEntry{{
		ProviderID: "codex", ModelID: "codex-model", Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	appLLMRouteMutationMu.Unlock()
	released = true
	runner := runtimeLiveReceive(t, built, "post-lock route runner")
	got, err := runner.Run(t.Context(), "private prompt", func(string) {})
	if err != nil || got != "post-lock route" {
		t.Fatalf("post-lock route result=%q err=%v", got, err)
	}
	if len(futureAnswer.seen()) != 0 || len(codexAnswer.seen()) != 1 {
		t.Fatalf("adapter calls stale=%d post-lock=%d", len(futureAnswer.seen()), len(codexAnswer.seen()))
	}
}

func TestRuntimeLiveDoesNotHoldRouteLockAcrossGenerate(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	generateEntered := make(chan struct{})
	releaseGenerate := make(chan struct{})
	adapter := &fakeAdapter{fn: func(context.Context, llmRequest) (llmResponse, error) {
		close(generateEntered)
		<-releaseGenerate
		return llmResponse{Text: "generated"}, nil
	}}
	registry := runtimeLiveRegistry(t, func(registrations []appProviderRuntimeRegistration) {
		future := appRuntimeRegistrationIndex(t, registrations, runtimeLiveFutureKind)
		registrations[future].NewAdapter = func(
			string, *store.Store, *http.Client, *slog.Logger,
		) (providerAdapter, error) {
			return adapter, nil
		}
	})
	ctx := runtimeLiveContext(t, env, registry)
	runtimeLiveSeedAccountRuntime(
		t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, true,
	)
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
	before := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: runtimeLiveFutureKind, ModelID: runtimeLiveFutureModel, Enabled: true,
	})
	runner := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
	runDone := make(chan error, 1)
	go func() {
		_, err := runner.Run(t.Context(), "private prompt", func(string) {})
		runDone <- err
	}()
	runtimeLiveWait(t, generateEntered, "blocked Generate")
	handler := runtimeLiveHandler(t, ctx)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseGenerate) }) }
	t.Cleanup(release)
	putDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		putDone <- runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
			`{"revision":%d,"entries":[{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`,
			before.Revision,
		))
	}()
	var rr *httptest.ResponseRecorder
	select {
	case rr = <-putDone:
	case <-time.After(3 * time.Second):
		release()
		_ = runtimeLiveReceive(t, runDone, "Generate cleanup")
		_ = runtimeLiveReceive(t, putDone, "route mutation cleanup")
		t.Fatal("route mutation remained blocked while Provider Generate was running")
	}
	if rr.Code != http.StatusOK {
		release()
		t.Fatalf("route mutation while Generate blocked status=%d body=%s", rr.Code, rr.Body.String())
	}
	release()
	if err := runtimeLiveReceive(t, runDone, "blocked Generate result"); err != nil {
		t.Fatalf("blocked Generate result=%v", err)
	}
}

func TestRuntimeLiveCrossAPISameRevisionHasSingleWinner(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	runtimeLiveSeedProductionRouteMembers(t, env)
	before := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: "codex", ModelID: "codex-model", Enabled: true,
	})
	ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
	ready := make(chan struct{})
	releaseWriters := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWriters) }) }
	t.Cleanup(release)
	var arrivals atomic.Int32
	ctx.routeCheckpoint = func(operation, phase string) {
		if phase != "before_lock" || (operation != "route-put" && operation != "combo-replace") {
			return
		}
		if arrivals.Add(1) == 2 {
			close(ready)
		}
		<-releaseWriters
	}
	handler := runtimeLiveHandler(t, ctx)
	type taggedResponse struct {
		operation string
		response  *httptest.ResponseRecorder
	}
	responses := make(chan taggedResponse, 2)
	go func() {
		responses <- taggedResponse{operation: "route-put", response: runtimeLiveServeHandler(
			t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
				`{"revision":%d,"entries":[{"provider_id":"claude-code","model_id":"claude-model","enabled":true}]}`,
				before.Revision,
			),
		)}
	}()
	go func() {
		responses <- taggedResponse{operation: "combo-replace", response: runtimeLiveServeHandler(
			t, handler, http.MethodPut, "/llm/combos/"+before.ComboID, fmt.Sprintf(
				`{"revision":%d,"type":"fallback","entries":[{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`,
				before.Revision,
			),
		)}
	}()
	runtimeLiveWait(t, ready, "two same-revision writers")
	release()
	first := runtimeLiveReceive(t, responses, "first same-revision response")
	second := runtimeLiveReceive(t, responses, "second same-revision response")
	codes := []int{first.response.Code, second.response.Code}
	slices.Sort(codes)
	if !slices.Equal(codes, []int{http.StatusOK, http.StatusConflict}) {
		t.Fatalf("same-revision statuses=%v bodies=%s / %s", codes, first.response.Body.String(), second.response.Body.String())
	}
	winner := first
	if second.response.Code == http.StatusOK {
		winner = second
	}
	wantProvider := "codex"
	if winner.operation == "route-put" {
		wantProvider = "claude-code"
	}
	after, err := env.a.st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision+1 || len(after.Entries) != 1 ||
		after.Entries[0].ProviderID != wantProvider {
		t.Fatalf("same-revision final route=%+v; winner=%s want provider=%s at r+1",
			after, winner.operation, wantProvider)
	}
}

func TestRuntimeLiveStaleMutationConflictsBeforeRuntimeValidation(t *testing.T) {
	t.Run("legacy route PUT", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		runtimeLiveSeedProductionRouteMembers(t, env)
		before := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: "codex", ModelID: "codex-model", Enabled: true,
		})
		ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
		handler := runtimeLiveHandler(t, ctx)

		winner := runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
			`{"revision":%d,"entries":[{"provider_id":"claude-code","model_id":"claude-model","enabled":true}]}`,
			before.Revision,
		))
		if winner.Code != http.StatusOK {
			t.Fatalf("winning legacy PUT status=%d body=%s", winner.Code, winner.Body.String())
		}
		winningRoute, err := env.a.st.LLMRoute()
		if err != nil {
			t.Fatal(err)
		}

		stale := runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/route", fmt.Sprintf(
			`{"revision":%d,"entries":[`+
				`{"provider_id":"claude-code","model_id":"claude-model","enabled":true},`+
				`{"provider_id":"codex","model_id":"codex-model","enabled":true}]}`,
			before.Revision,
		))
		if stale.Code != http.StatusConflict ||
			!strings.Contains(stale.Body.String(), "ROUTE_REVISION_CONFLICT") {
			t.Fatalf("stale runtime-invalid legacy PUT status=%d body=%s; want 409 conflict",
				stale.Code, stale.Body.String())
		}
		after, err := env.a.st.LLMRoute()
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != winningRoute.Revision || !slices.Equal(after.Entries, winningRoute.Entries) {
			t.Fatalf("stale legacy PUT changed winner: winner=%+v after=%+v", winningRoute, after)
		}
	})

	t.Run("combo replace", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		runtimeLiveSeedProductionRouteMembers(t, env)
		before := runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: "codex", ModelID: "codex-model", Enabled: true,
		})
		ctx := runtimeLiveContext(t, env, productionAppProviderRuntimeRegistry())
		handler := runtimeLiveHandler(t, ctx)

		winner := runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/combos/"+before.ComboID, fmt.Sprintf(
			`{"revision":%d,"type":"fallback","entries":[`+
				`{"provider_id":"claude-code","model_id":"claude-model","enabled":true}]}`,
			before.Revision,
		))
		if winner.Code != http.StatusOK {
			t.Fatalf("winning combo replace status=%d body=%s", winner.Code, winner.Body.String())
		}
		winningRoute, err := env.a.st.LLMRoute()
		if err != nil {
			t.Fatal(err)
		}

		stale := runtimeLiveServeHandler(t, handler, http.MethodPut, "/llm/combos/"+before.ComboID, fmt.Sprintf(
			`{"revision":%d,"type":"fallback","entries":[`+
				`{"provider_id":"unknown-runtime","model_id":"unknown-model","enabled":true}]}`,
			before.Revision,
		))
		if stale.Code != http.StatusConflict ||
			!strings.Contains(stale.Body.String(), "COMBO_REVISION_CONFLICT") {
			t.Fatalf("stale runtime-invalid combo replace status=%d body=%s; want 409 conflict",
				stale.Code, stale.Body.String())
		}
		after, err := env.a.st.LLMRoute()
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != winningRoute.Revision || !slices.Equal(after.Entries, winningRoute.Entries) {
			t.Fatalf("stale combo replace changed winner: winner=%+v after=%+v", winningRoute, after)
		}
	})
}

func TestRuntimeLiveAttachmentAndStructuredFactoriesAreTyped(t *testing.T) {
	t.Run("synthetic local attachment callback", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		var localCalls atomic.Int32
		stateless := okAdapter("stateless adapter must not receive attachment")
		registry := runtimeLiveRegistry(t, func(registrations []appProviderRuntimeRegistration) {
			future := appRuntimeRegistrationIndex(t, registrations, runtimeLiveFutureKind)
			registrations[future].Metadata.AttachmentPolicy = appProviderAttachmentLocal
			registrations[future].NewAdapter = func(
				string, *store.Store, *http.Client, *slog.Logger,
			) (providerAdapter, error) {
				return stateless, nil
			}
			registrations[future].NewLocalAttachmentRun = func(*appLLMRunner) appLocalAttachmentRun {
				return func(
					_ *appLLMRunner, _ context.Context, entry store.LLMRouteEntry,
					input appLLMRouteInput, _ func(string),
				) (appZaloRunResult, error) {
					localCalls.Add(1)
					if entry.ProviderID != runtimeLiveFutureKind || entry.ModelID != runtimeLiveFutureModel ||
						input.statelessPrompt != "attachment prompt" {
						t.Errorf("typed attachment entry=%+v input=%+v", entry, input)
					}
					return appZaloRunResult{Answer: "typed local answer"}, nil
				}
			}
		})
		ctx := runtimeLiveContext(t, env, registry)
		runtimeLiveSeedAccountRuntime(
			t, env, runtimeLiveFutureKind, "Future CLI", runtimeLiveFutureModel, true,
		)
		runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: runtimeLiveFutureKind, ModelID: runtimeLiveFutureModel, Enabled: true,
		})
		got, err := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", true).
			Run(t.Context(), "attachment prompt", func(string) {})
		if err != nil || got != "typed local answer" || localCalls.Load() != 1 {
			t.Fatalf("typed attachment result=%q err=%v calls=%d", got, err, localCalls.Load())
		}
		if len(stateless.seen()) != 0 {
			t.Fatalf("stateless adapter received attachment calls=%d", len(stateless.seen()))
		}
	})

	t.Run("synthetic non-Claude structured terminal callback", func(t *testing.T) {
		const terminalKind = "future-terminal"
		const terminalModel = "future-terminal-model"
		env := newOnboardingRouteTestEnv(t)
		structured := &fakeStructuredClaude{answer: "structured answer"}
		var resolutionCalls, terminalCalls atomic.Int32
		registrations := appProductionProviderRuntimeRegistrations()
		registrations = append(registrations, appProviderRuntimeRegistration{
			Metadata: appProviderMetadata{
				Kind: terminalKind, DisplayName: "Future Terminal",
				Description: "Synthetic typed terminal runtime",
				Group:       appProviderGroupSubscription, Prefix: "ft", ThemeColor: "#445566", UIOrder: 80,
				Visibility: appProviderVisibilityHidden, ConnectionMode: appProviderConnectionNone,
				ExecutionMode: appProviderExecutionLocal, AttachmentPolicy: appProviderAttachmentNone,
			},
			Terminal: func(
				runner *appLLMRunner,
				ctx context.Context,
				entry store.LLMRouteEntry,
				input appLLMRouteInput,
				step func(string),
			) (appZaloRunResult, error) {
				terminalCalls.Add(1)
				if entry.ProviderID != terminalKind || entry.ModelID != terminalModel || input.structured == nil {
					t.Errorf("synthetic terminal entry=%+v input=%+v", entry, input)
				}
				resolved := runner.cfg.ResolvedStructured[entry.ProviderID]
				typed, ok := resolved.(appZaloStructuredRunner)
				if !ok {
					return appZaloRunResult{}, errors.New("synthetic structured runner was not resolved")
				}
				return typed.appRunZaloSession(ctx, *input.structured, step)
			},
			NewStructuredSession: func(*api, zaloConfig) appStructuredSessionRunner {
				return func(
					_ context.Context, model string, current *store.ZaloCLISession,
				) (zaloRunner, appZaloClaudeBinding) {
					resolutionCalls.Add(1)
					if model != terminalModel || current != nil {
						t.Errorf("structured resolver model=%q current=%+v", model, current)
					}
					return structured, appZaloClaudeBinding{
						State: appZaloClaudeBindingSelected, AccountID: "opaque", Model: model,
					}
				}
			},
		})
		registry, err := newAppProviderRuntimeRegistry(
			productionAppProviderRuntimeRegistry().Catalog().Options(), registrations,
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx := runtimeLiveContext(t, env, registry)
		runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
		if err := env.a.st.EnsureAccountRuntimeProvider(terminalKind, "Future Terminal"); err != nil {
			t.Fatal(err)
		}
		if err := env.a.st.AddLLMModel(store.LLMModel{
			ProviderID: terminalKind, ModelID: terminalModel, Name: "Future Terminal",
			Source: store.LLMModelManual, Available: true,
		}); err != nil {
			t.Fatal(err)
		}
		runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: terminalKind, ModelID: terminalModel, Enabled: true,
		})
		runner := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
		resolver, ok := runner.(appZaloSessionRouteResolver)
		if !ok {
			t.Fatalf("runner %T has no structured route resolver", runner)
		}
		resolved, binding := resolver.appResolveZaloSessionRoute(t.Context(), nil)
		if resolutionCalls.Load() != 1 || binding.State != appZaloClaudeBindingSelected || binding.Model != terminalModel {
			t.Fatalf("structured resolution calls=%d binding=%+v", resolutionCalls.Load(), binding)
		}
		result, err := resolved.(appZaloStructuredRunner).appRunZaloSession(
			t.Context(), appZaloSessionRunInput{Prompt: "structured prompt"}, func(string) {},
		)
		if err != nil || result.Answer != "structured answer" || terminalCalls.Load() != 1 {
			t.Fatalf("structured terminal result=%+v err=%v", result, err)
		}
	})

	t.Run("terminal without structured capability gets no compatibility resolver", func(t *testing.T) {
		const terminalKind = "plain-terminal"
		const terminalModel = "plain-model"
		env := newOnboardingRouteTestEnv(t)
		registrations := appProductionProviderRuntimeRegistrations()
		registrations = append(registrations, appProviderRuntimeRegistration{
			Metadata: appProviderMetadata{
				Kind: terminalKind, DisplayName: "Plain Terminal", Description: "No structured capability",
				Group: appProviderGroupSubscription, Prefix: "pt", ThemeColor: "#556677", UIOrder: 80,
				Visibility: appProviderVisibilityHidden, ConnectionMode: appProviderConnectionNone,
				ExecutionMode: appProviderExecutionLocal, AttachmentPolicy: appProviderAttachmentNone,
			},
			Terminal: func(
				runner *appLLMRunner,
				_ context.Context,
				entry store.LLMRouteEntry,
				_ appLLMRouteInput,
				_ func(string),
			) (appZaloRunResult, error) {
				if runner.cfg.StructuredSessions[entry.ProviderID] != nil {
					return appZaloRunResult{}, errors.New("plain terminal inherited a structured resolver")
				}
				return appZaloRunResult{Answer: "plain terminal answer"}, nil
			},
		})
		registry, err := newAppProviderRuntimeRegistry(
			productionAppProviderRuntimeRegistry().Catalog().Options(), registrations,
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx := runtimeLiveContext(t, env, registry)
		runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
		if err := env.a.st.EnsureAccountRuntimeProvider(terminalKind, "Plain Terminal"); err != nil {
			t.Fatal(err)
		}
		if err := env.a.st.AddLLMModel(store.LLMModel{
			ProviderID: terminalKind, ModelID: terminalModel, Name: "Plain Terminal",
			Source: store.LLMModelManual, Available: true,
		}); err != nil {
			t.Fatal(err)
		}
		runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
			ProviderID: terminalKind, ModelID: terminalModel, Enabled: true,
		})
		runner := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
		resolver := runner.(appZaloSessionRouteResolver)
		resolved, binding := resolver.appResolveZaloSessionRoute(t.Context(), nil)
		if resolved != runner || binding != (appZaloClaudeBinding{}) {
			t.Fatalf("plain terminal unexpectedly resolved structured state: runnerChanged=%v binding=%+v",
				resolved != runner, binding)
		}
		got, err := runner.Run(t.Context(), "private prompt", func(string) {})
		if err != nil || got != "plain terminal answer" {
			t.Fatalf("plain terminal result=%q err=%v", got, err)
		}
	})
}

func TestRuntimeLiveAttachmentUsesResolvedTerminalBinding(t *testing.T) {
	const terminalKind = "bound-terminal"
	const terminalModel = "bound-model"
	env := newOnboardingRouteTestEnv(t)
	accountA := &fakeStructuredClaude{answer: "account A"}
	accountB := &fakeStructuredClaude{answer: "account B"}
	var resolverCalls, terminalCalls, localCalls atomic.Int32
	registrations := appProductionProviderRuntimeRegistrations()
	registrations = append(registrations, appProviderRuntimeRegistration{
		Metadata: appProviderMetadata{
			Kind: terminalKind, DisplayName: "Bound Terminal", Description: "Bound attachment terminal",
			Group: appProviderGroupSubscription, Prefix: "bt", ThemeColor: "#667788", UIOrder: 80,
			Visibility: appProviderVisibilityHidden, ConnectionMode: appProviderConnectionNone,
			ExecutionMode: appProviderExecutionLocal, AttachmentPolicy: appProviderAttachmentLocal,
		},
		Terminal: func(
			*appLLMRunner,
			context.Context,
			store.LLMRouteEntry,
			appLLMRouteInput,
			func(string),
		) (appZaloRunResult, error) {
			terminalCalls.Add(1)
			return appZaloRunResult{Answer: "normal terminal callback"}, nil
		},
		NewLocalAttachmentRun: func(*appLLMRunner) appLocalAttachmentRun {
			return func(
				current *appLLMRunner,
				ctx context.Context,
				entry store.LLMRouteEntry,
				input appLLMRouteInput,
				step func(string),
			) (appZaloRunResult, error) {
				localCalls.Add(1)
				return current.runClaude(ctx, entry, input, step)
			}
		},
		NewStructuredSession: func(*api, zaloConfig) appStructuredSessionRunner {
			return func(
				context.Context, string, *store.ZaloCLISession,
			) (zaloRunner, appZaloClaudeBinding) {
				call := resolverCalls.Add(1)
				if call == 1 {
					return accountA, appZaloClaudeBinding{
						State: appZaloClaudeBindingSelected, AccountID: "account-A", Model: terminalModel,
					}
				}
				return accountB, appZaloClaudeBinding{
					State: appZaloClaudeBindingSelected, AccountID: "account-B", Model: terminalModel,
				}
			}
		},
	})
	registry, err := newAppProviderRuntimeRegistry(
		productionAppProviderRuntimeRegistry().Catalog().Options(), registrations,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := runtimeLiveContext(t, env, registry)
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
	if err := env.a.st.EnsureAccountRuntimeProvider(terminalKind, "Bound Terminal"); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.AddLLMModel(store.LLMModel{
		ProviderID: terminalKind, ModelID: terminalModel, Name: "Bound Terminal",
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: terminalKind, ModelID: terminalModel, Enabled: true,
	})

	runner := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false)
	resolved, binding := runner.(appZaloSessionRouteResolver).appResolveZaloSessionRoute(t.Context(), nil)
	if binding.State != appZaloClaudeBindingSelected || binding.AccountID != "account-A" {
		t.Fatalf("resolved binding=%+v; want account A", binding)
	}
	attached := resolved.(appZaloAttachmentAwareRunner).appWithZaloAttachments(true)
	result, err := attached.(appZaloStructuredRunner).appRunZaloSession(
		t.Context(), appZaloSessionRunInput{Prompt: "private attachment prompt"}, func(string) {},
	)
	if err != nil || result.Answer != "account A" {
		t.Fatalf("bound attachment result=%+v err=%v; want account A", result, err)
	}
	if resolverCalls.Load() != 1 {
		t.Fatalf("structured resolver calls=%d; want exactly 1", resolverCalls.Load())
	}
	if terminalCalls.Load() != 0 || localCalls.Load() != 1 {
		t.Fatalf("attachment callbacks terminal=%d local=%d; want typed local callback once only",
			terminalCalls.Load(), localCalls.Load())
	}
	inputsA, legacyA := accountA.seen()
	inputsB, legacyB := accountB.seen()
	if len(inputsA) != 1 || legacyA != 0 || len(inputsB) != 0 || legacyB != 0 {
		t.Fatalf("bound execution calls A=%d/%d B=%d/%d; want A structured once only",
			len(inputsA), legacyA, len(inputsB), legacyB)
	}
}

func TestRuntimeLiveRegistryRejectsHTTPAttachmentClaimEvenWithCallback(t *testing.T) {
	registrations := appProductionProviderRuntimeRegistrations()
	openAI := appRuntimeRegistrationIndex(t, registrations, "openai")
	registrations[openAI].Metadata.AttachmentPolicy = appProviderAttachmentLocal
	registrations[openAI].NewLocalAttachmentRun = appRuntimeAttachmentFactory
	if _, err := newAppProviderRuntimeRegistry(
		productionAppProviderRuntimeRegistry().Catalog().Options(), registrations,
	); err == nil {
		t.Fatal("HTTP/API runtime with a typed local callback was accepted")
	}
}

func TestRuntimeLiveOpenCodeDeniedAtEveryLookup(t *testing.T) {
	registry := productionAppProviderRuntimeRegistry()
	if _, ok := registry.providerAdapterFactory("opencode"); ok {
		t.Fatal("OpenCode adapter lookup succeeded")
	}
	if _, ok := registry.localAttachmentRunnerFactory("opencode"); ok {
		t.Fatal("OpenCode attachment lookup succeeded")
	}
	if _, ok := registry.terminalRouteRunner("opencode"); ok {
		t.Fatal("OpenCode terminal lookup succeeded")
	}
	if _, ok := registry.structuredSessionRunnerFactory("opencode"); ok {
		t.Fatal("OpenCode structured lookup succeeded")
	}

	env := newOnboardingRouteTestEnv(t)
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
	if err := env.a.st.EnsureAccountRuntimeProvider("opencode", "Forged OpenCode"); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.AddLLMModel(store.LLMModel{
		ProviderID: "opencode", ModelID: "forged-model", Name: "Forged",
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: "opencode", ModelID: "forged-model", Enabled: true,
	})
	ctx := runtimeLiveContext(t, env, registry)
	_, err := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false).
		Run(t.Context(), "private prompt", func(string) {})
	if !errors.Is(err, ErrZaloSilent) {
		t.Fatalf("forged OpenCode live route error=%v; want ErrZaloSilent", err)
	}
}

func TestRuntimeLiveProductionZaloHookUsesContextBoundRunner(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app_zalo_session_hook.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	matches := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		fun, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, receiverOK := fun.X.(*ast.Ident)
		if !receiverOK || receiver.Name != "a" || fun.Sel.Name != "appAnswerZaloWithRunnerFactory" {
			return true
		}
		for _, argument := range call.Args {
			runner, ok := argument.(*ast.SelectorExpr)
			if !ok || runner.Sel.Name != "appZaloRunner" {
				continue
			}
			contextCall, ok := runner.X.(*ast.CallExpr)
			if !ok {
				continue
			}
			contextFactory, factoryOK := contextCall.Fun.(*ast.Ident)
			if !factoryOK || contextFactory.Name != "productionAppRuntimeContext" ||
				len(contextCall.Args) != 1 {
				continue
			}
			apiArgument, ok := contextCall.Args[0].(*ast.Ident)
			if ok && apiArgument.Name == "a" {
				matches++
			}
		}
		return true
	})
	if matches != 1 {
		t.Fatalf("direct production context runner arguments=%d; want 1", matches)
	}
}

func TestRuntimeLiveHiddenGeminiCLICompatibility(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	answer := okAdapter("hidden Gemini answer")
	var localCalls atomic.Int32
	registrations := appProductionProviderRuntimeRegistrations()
	geminiCLI := appRuntimeRegistrationIndex(t, registrations, "gemini-cli")
	registrations[geminiCLI].NewAdapter = func(
		providerID string, _ *store.Store, _ *http.Client, _ *slog.Logger,
	) (providerAdapter, error) {
		if providerID != "gemini-cli" {
			t.Fatalf("hidden factory providerID=%q", providerID)
		}
		return answer, nil
	}
	registrations[geminiCLI].NewLocalAttachmentRun = func(*appLLMRunner) appLocalAttachmentRun {
		return func(
			_ *appLLMRunner, _ context.Context, entry store.LLMRouteEntry,
			input appLLMRouteInput, _ func(string),
		) (appZaloRunResult, error) {
			localCalls.Add(1)
			if entry.ProviderID != "gemini-cli" || entry.ModelID != "gemini-model" ||
				input.statelessPrompt != "attachment prompt" {
				t.Errorf("hidden Gemini attachment entry=%+v input=%+v", entry, input)
			}
			return appZaloRunResult{Answer: "hidden Gemini attachment"}, nil
		}
	}
	registry, err := newAppProviderRuntimeRegistry(
		productionAppProviderRuntimeRegistry().Catalog().Options(), registrations,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := runtimeLiveContext(t, env, registry)
	runtimeLiveSeedAccountRuntime(t, env, "codex", "Codex", "codex-model", true)
	if err := env.a.st.EnsureAccountRuntimeProvider("gemini-cli", "Gemini CLI"); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.AddLLMModel(store.LLMModel{
		ProviderID: "gemini-cli", ModelID: "gemini-model", Name: "Gemini",
		Source: store.LLMModelManual, Available: true,
	}); err != nil {
		t.Fatal(err)
	}
	runtimeLiveSaveRoute(t, env, store.LLMRouteEntry{
		ProviderID: "gemini-cli", ModelID: "gemini-model", Enabled: true,
	})
	got, err := ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", false).
		Run(t.Context(), "private prompt", func(string) {})
	if err != nil || got != "hidden Gemini answer" || len(answer.seen()) != 1 {
		t.Fatalf("hidden Gemini result=%q err=%v calls=%d", got, err, len(answer.seen()))
	}
	got, err = ctx.appZaloRunner(zaloConfig{}, silentZaloRunner{}, "thread", true).
		Run(t.Context(), "attachment prompt", func(string) {})
	if err != nil || got != "hidden Gemini attachment" || localCalls.Load() != 1 {
		t.Fatalf("hidden Gemini attachment result=%q err=%v localCalls=%d", got, err, localCalls.Load())
	}
	if len(answer.seen()) != 1 {
		t.Fatalf("hidden Gemini stateless adapter received attachment; calls=%d", len(answer.seen()))
	}
	option, ok := registry.managementOption("gemini-cli")
	if !ok || option.Visible || option.ConnectionMode != appProviderConnectionNone {
		t.Fatalf("hidden Gemini metadata=%+v present=%v", option, ok)
	}
}
