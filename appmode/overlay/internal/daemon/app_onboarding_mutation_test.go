package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agentdc/internal/store"
)

func TestAppOnboardingProviderStrictRequestValidation(t *testing.T) {
	tests := []struct {
		name, body, code string
		status           int
	}{
		{"unknown", `{"revision":1,"kind":"codex","extra":true}`, "ONBOARDING_REQUEST_INVALID", http.StatusBadRequest},
		{"malformed", `{"revision":1,`, "ONBOARDING_REQUEST_INVALID", http.StatusBadRequest},
		{"trailing", `{"revision":1,"kind":"codex"}{}`, "ONBOARDING_REQUEST_INVALID", http.StatusBadRequest},
		{"empty", ``, "ONBOARDING_REQUEST_INVALID", http.StatusBadRequest},
		{"oversize", `{"revision":1,"kind":"codex","padding":"` + strings.Repeat("x", 70<<10) + `"}`, "ONBOARDING_REQUEST_TOO_LARGE", http.StatusRequestEntityTooLarge},
		{"zero revision", `{"revision":0,"kind":"codex"}`, "ONBOARDING_REVISION_INVALID", http.StatusUnprocessableEntity},
		{"negative revision", `{"revision":-1,"kind":"codex"}`, "ONBOARDING_REVISION_INVALID", http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			rr := env.serve(http.MethodPut, "/onboarding/provider", tt.body)
			requireOnboardingCode(t, rr, tt.status, tt.code)
			if got := env.state(t); got.Revision != 1 || got.Phase != store.OnboardingPhaseProvider {
				t.Fatalf("invalid request changed state: %+v", got)
			}
		})
	}
}

func TestAppOnboardingProviderMapsUnsupportedAndStaleRevision(t *testing.T) {
	tests := []struct {
		name, body, code string
		status           int
	}{
		{"unsupported", `{"revision":1,"kind":"openai"}`, "ONBOARDING_PROVIDER_UNSUPPORTED", http.StatusUnprocessableEntity},
		{"stale", `{"revision":9,"kind":"codex"}`, "ONBOARDING_REVISION_CONFLICT", http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			rr := env.serve(http.MethodPut, "/onboarding/provider", tt.body)
			requireOnboardingCode(t, rr, tt.status, tt.code)
		})
	}
}

func TestAppOnboardingRestartRequiresConfirmationAndCompletedFlow(t *testing.T) {
	t.Run("confirmation", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		rr := env.serve(http.MethodPost, "/onboarding/restart", `{"revision":1,"confirmed":false}`)
		requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, "ONBOARDING_RESTART_CONFIRMATION_REQUIRED")
		if got := env.state(t); got.Revision != 1 {
			t.Fatalf("unconfirmed restart changed state: %+v", got)
		}
	})
	t.Run("before completed", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		rr := env.serve(http.MethodPost, "/onboarding/restart", `{"revision":1,"confirmed":true}`)
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
		if got := env.state(t); got.Revision != 1 || got.Phase != store.OnboardingPhaseProvider {
			t.Fatalf("invalid restart changed state: %+v", got)
		}
	})
}

func TestAppOnboardingRestartPreservesLiveAccountRouteAndDirectory(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	seedOnboardingRoute(t, env, "codex", "codex", true)
	dir := accountConfigDir(env.dataDir, "codex", "live-account")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	canaryPath := filepath.Join(dir, "live.canary")
	canaryBefore := []byte{0, 1, 2, 3, 0xff}
	if err := os.WriteFile(canaryPath, canaryBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: "live-account", ProviderID: "codex", Label: "live account",
		ConfigDir: dir, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	route, err := env.a.st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	env.setState(t, store.OnboardingState{
		CompletedVersion: store.CurrentOnboardingVersion,
		Phase:            store.OnboardingPhaseCompleted,
		ProviderKind:     "codex",
		ProviderID:       "codex",
		AccountID:        "live-account",
		ModelID:          "model",
		StagedComboID:    route.ComboID,
		Revision:         3,
	})
	beforeRouting := onboardingRoutingDigest(t, env)
	jobCtx, cancelJob := context.WithCancel(context.Background())
	job := newManualConnectJob("codex", cancelJob)
	connectMgr = &connectManager{job: job, jobKind: "codex"}
	go func() {
		<-jobCtx.Done()
		close(job.done)
	}()
	t.Cleanup(func() {
		cancelJob()
		select {
		case <-job.done:
		case <-time.After(time.Second):
			t.Error("live Connect cleanup goroutine did not stop")
		}
	})

	rr := env.serve(http.MethodPost, "/onboarding/restart", `{"revision":3,"confirmed":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	got := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
	if got.CompletedVersion != store.CurrentOnboardingVersion || !got.RestartInProgress ||
		!got.Required || got.Phase != store.OnboardingPhaseProvider || got.ProviderKind != "" || got.Revision != 4 {
		t.Fatalf("restart response=%+v", got)
	}
	if jobCtx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("restart canceled the live completed Connect slot")
	}
	accounts, err := env.a.st.LLMAccounts("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != "live-account" || !accounts[0].Enabled ||
		accounts[0].ConfigDir != dir {
		t.Fatalf("restart changed live Account: %+v", accounts)
	}
	canaryAfter, err := os.ReadFile(canaryPath)
	if err != nil || !bytes.Equal(canaryAfter, canaryBefore) {
		t.Fatalf("restart changed live Account canary: bytes=%v err=%v", canaryAfter, err)
	}
	if afterRouting := onboardingRoutingDigest(t, env); afterRouting != beforeRouting {
		t.Fatalf("restart changed Combo/route data:\nbefore=%s\nafter=%s", beforeRouting, afterRouting)
	}
}

func assertOnboardingOperationRejectedBeforeSideEffects(
	t *testing.T,
	state store.OnboardingState,
	method, path, body string,
) {
	t.Helper()
	env := newOnboardingRouteTestEnv(t)
	if err := env.a.st.EnsureProviderForKind("codex"); err != nil {
		t.Fatal(err)
	}
	dir := accountConfigDir(env.dataDir, "codex", "rejected-staging")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "canary"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: "rejected-staging", ProviderID: "codex", Label: "rejected",
		ConfigDir: dir, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	state.ProviderKind = "codex"
	state.ProviderID = "codex"
	state.AccountID = "rejected-staging"
	state.Revision = 1
	env.setState(t, state)
	before := env.state(t)

	jobCtx, cancelJob := context.WithCancel(context.Background())
	job := newManualConnectJob("codex", cancelJob)
	connectMgr = &connectManager{job: job, jobKind: "codex"}
	go func() {
		<-jobCtx.Done()
		close(job.done)
	}()
	t.Cleanup(func() {
		cancelJob()
		select {
		case <-job.done:
		case <-time.After(time.Second):
			t.Error("matching Connect cleanup goroutine did not stop")
		}
	})

	rr := env.serve(method, path, body)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if jobCtx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("rejected onboarding operation canceled matching Connect")
	}
	if after := env.state(t); after != before {
		t.Fatalf("rejected operation changed state: before=%+v after=%+v", before, after)
	}
	requireAccountPresent(t, env, "codex", "rejected-staging", true)
	if contents, err := os.ReadFile(filepath.Join(dir, "canary")); err != nil || string(contents) != "keep" {
		t.Fatalf("rejected operation damaged staging directory: contents=%q err=%v", contents, err)
	}
}

func TestAppOnboardingProviderRejectsCurrentCompletionBeforeSideEffects(t *testing.T) {
	assertOnboardingOperationRejectedBeforeSideEffects(t, store.OnboardingState{
		CompletedVersion: store.CurrentOnboardingVersion,
		Phase:            store.OnboardingPhaseCompleted,
	}, http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
}

func TestAppOnboardingRestartRejectsEveryOtherPhaseBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name  string
		state store.OnboardingState
	}{
		{"provider", store.OnboardingState{Phase: store.OnboardingPhaseProvider}},
		{"connect", store.OnboardingState{Phase: store.OnboardingPhaseConnect}},
		{"setup", store.OnboardingState{Phase: store.OnboardingPhaseSetup}},
		{"persona", store.OnboardingState{Phase: store.OnboardingPhasePersona}},
		{"test", store.OnboardingState{Phase: store.OnboardingPhaseTest}},
		{"older completion", store.OnboardingState{CompletedVersion: store.CurrentOnboardingVersion - 1, Phase: store.OnboardingPhaseCompleted}},
		{"future completion", store.OnboardingState{CompletedVersion: store.CurrentOnboardingVersion + 1, Phase: store.OnboardingPhaseCompleted}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertOnboardingOperationRejectedBeforeSideEffects(t, tt.state,
				http.MethodPost, "/onboarding/restart", `{"revision":1,"confirmed":true}`)
		})
	}
}

func TestAppOnboardingAlreadyCanceledRequestHasNoSideEffects(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	dir := seedOnboardingStagingAccount(t, env, "codex", "owned", false)
	before := env.state(t)
	jobCtx, cancelJob := context.WithCancel(context.Background())
	job := newManualConnectJob("codex", cancelJob)
	connectMgr = &connectManager{job: job, jobKind: "codex"}
	t.Cleanup(func() {
		cancelJob()
		close(job.done)
	})
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	req := httptest.NewRequest(http.MethodPut, "/onboarding/provider",
		strings.NewReader(`{"revision":1,"kind":"claude-code"}`)).WithContext(requestCtx)
	rr := env.serveRequest(req)
	requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_CLEANUP_FAILED")
	if jobCtx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("already-canceled HTTP request canceled Connect")
	}
	if after := env.state(t); after != before {
		t.Fatalf("already-canceled request changed state: before=%+v after=%+v", before, after)
	}
	requireAccountPresent(t, env, "codex", "owned", true)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("already-canceled request removed staging directory: %v", err)
	}
}

func seedOnboardingStagingAccount(t *testing.T, env *onboardingRouteTestEnv, kind, accountID string, enabled bool) string {
	t.Helper()
	if err := env.a.st.EnsureProviderForKind(kind); err != nil {
		t.Fatalf("EnsureProviderForKind(%s) = %v", kind, err)
	}
	dir := accountConfigDir(env.dataDir, kind, accountID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s) = %v", dir, err)
	}
	if err := env.a.st.CreateLLMAccount(store.LLMAccount{
		ID: accountID, ProviderID: kind, Label: "staging", ConfigDir: dir, Enabled: enabled,
	}); err != nil {
		t.Fatalf("CreateLLMAccount(%s) = %v", accountID, err)
	}
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseConnect, ProviderKind: kind, ProviderID: kind,
		AccountID: accountID, Revision: 1,
	})
	return dir
}

func requireAccountPresent(t *testing.T, env *onboardingRouteTestEnv, providerID, accountID string, want bool) {
	t.Helper()
	accounts, err := env.a.st.LLMAccounts(providerID)
	if err != nil {
		t.Fatalf("LLMAccounts(%s) = %v", providerID, err)
	}
	found := false
	for _, account := range accounts {
		if account.ID == accountID {
			found = true
		}
	}
	if found != want {
		t.Fatalf("account %q present=%v; want %v (accounts=%+v)", accountID, found, want, accounts)
	}
}

func TestAppOnboardingProviderRemovesExactOwnedStagingData(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	dir := seedOnboardingStagingAccount(t, env, "codex", "owned", false)
	canary := filepath.Join(filepath.Dir(dir), "keep")
	if err := os.MkdirAll(canary, 0o700); err != nil {
		t.Fatal(err)
	}
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("rejected begin removed owned dir: %v", err)
	}
	if info, err := os.Stat(canary); err != nil || !info.IsDir() {
		t.Fatalf("sibling canary was damaged: info=%v err=%v", info, err)
	}
	requireAccountPresent(t, env, "codex", "owned", true)
}

func TestAppOnboardingProviderRetriesAlreadyMissingOwnedDirectory(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	dir := seedOnboardingStagingAccount(t, env, "codex", "owned", false)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	requireAccountPresent(t, env, "codex", "owned", true)
}

func TestAppOnboardingProviderPreservesEnabledAccountAndMapsInvalidStaging(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	dir := seedOnboardingStagingAccount(t, env, "codex", "enabled", true)
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if got := env.state(t); got.Revision != 1 || got.AccountID != "enabled" {
		t.Fatalf("state changed: %+v", got)
	}
	requireAccountPresent(t, env, "codex", "enabled", true)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("enabled account dir damaged: %v", err)
	}
}

func TestAppOnboardingCleanupFailurePreservesStateAndDoesNotLeakDetails(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	dir := seedOnboardingStagingAccount(t, env, "codex", "owned", false)
	original := removeAllOnboardingAccountRooted
	removeAllOnboardingAccountRooted = func(*os.Root, string) error {
		return errors.New("SQL failure at " + dir)
	}
	t.Cleanup(func() { removeAllOnboardingAccountRooted = original })
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if got := env.state(t); got.Revision != 1 || got.AccountID != "owned" {
		t.Fatalf("cleanup failure changed state: %+v", got)
	}
	requireAccountPresent(t, env, "codex", "owned", true)
	if body := strings.ToLower(rr.Body.String()); strings.Contains(body, strings.ToLower(dir)) || strings.Contains(body, "sql") {
		t.Fatalf("response leaked internal error: %s", rr.Body.String())
	}
}

func newManualConnectJob(kind string, cancel context.CancelFunc) *connectJob {
	return &connectJob{
		state: connectState{Kind: kind, Phase: phaseDetecting}, cancel: cancel, done: make(chan struct{}),
	}
}

func TestAppOnboardingProviderRejectsWithoutCancelingMatchingConnect(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 1})
	ctx, cancel := context.WithCancel(context.Background())
	job := newManualConnectJob("codex", cancel)
	job.onboarding = &connectOnboardingContext{Revision: 1, Kind: "codex"}
	connectMgr = &connectManager{job: job, jobKind: "codex"}
	t.Cleanup(func() {
		cancel()
		select {
		case <-job.done:
		default:
			close(job.done)
		}
	})
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if ctx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("rejected begin canceled matching Connect")
	}
}

func TestAppLLMConnectStartWaitsForOnboardingMutation(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	started := make(chan struct{})
	connectMgr = &connectManager{
		runner:        &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut},
		ensure:        env.a.st.EnsureProviderForKind,
		createAccount: env.a.st.CreateLLMAccount,
		dataDir:       env.dataDir,
		newID: func() string {
			close(started)
			return "serialized-connect"
		},
		logger:       env.a.logger,
		loginTimeout: 5 * time.Second,
		pollInterval: 5 * time.Millisecond,
	}

	onboardingMutationMu.Lock()
	locked := true
	defer func() {
		if locked {
			onboardingMutationMu.Unlock()
		}
	}()
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- env.serve(http.MethodPost, "/llm/providers/codex/connect", `{"label":"next"}`)
	}()
	select {
	case <-started:
		t.Fatal("Connect entered manager before onboarding mutation lock was released")
	case <-time.After(150 * time.Millisecond):
	}
	onboardingMutationMu.Unlock()
	locked = false
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Connect did not enter manager after onboarding mutation lock was released")
	}
	select {
	case rr := <-response:
		if rr.Code != http.StatusOK {
			t.Fatalf("connect status=%d body=%s", rr.Code, rr.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Connect request did not return")
	}
	connectMgr.cancel("codex")
	waitConnectJobDone(t, connectMgr)
}

func TestAppLLMConnectStartCanceledRequestDoesNotEnterManager(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	started := make(chan struct{})
	connectMgr = &connectManager{
		runner:        &fakeRunner{installed: true, loginURL: "https://x", auth: authLoggedOut},
		ensure:        env.a.st.EnsureProviderForKind,
		createAccount: env.a.st.CreateLLMAccount,
		dataDir:       env.dataDir,
		newID: func() string {
			close(started)
			return "must-not-start"
		},
		logger: env.a.logger,
	}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	req := httptest.NewRequest(http.MethodPost, "/llm/providers/claude-code/connect",
		strings.NewReader(`{"label":"canceled"}`)).WithContext(requestCtx)
	rr := env.serveRequest(req)
	requireOnboardingCode(t, rr, http.StatusRequestTimeout, "CONNECT_REQUEST_CANCELED")
	select {
	case <-started:
		t.Fatal("canceled Connect request entered manager")
	default:
	}
}

func TestConnectOnboardingHandlerValidatesContextBeforeStartingJob(t *testing.T) {
	tests := []struct {
		name        string
		state       store.OnboardingState
		kind        string
		body        string
		status      int
		code        string
		wantStart   bool
		wantContext *connectOnboardingContext
	}{
		{
			name:  "normal request has no onboarding context",
			state: store.OnboardingState{Phase: store.OnboardingPhaseProvider, Revision: 1},
			kind:  "codex", body: `{"label":"normal"}`, status: http.StatusOK, wantStart: true,
		},
		{
			name: "zero revision", state: store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 2},
			kind: "codex", body: `{"onboarding_revision":0}`, status: http.StatusUnprocessableEntity, code: "ONBOARDING_REVISION_INVALID",
		},
		{
			name: "negative revision", state: store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 2},
			kind: "codex", body: `{"onboarding_revision":-2}`, status: http.StatusUnprocessableEntity, code: "ONBOARDING_REVISION_INVALID",
		},
		{
			name: "stale revision", state: store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 2},
			kind: "codex", body: `{"onboarding_revision":1}`, status: http.StatusConflict, code: "ONBOARDING_REVISION_CONFLICT",
		},
		{
			name: "wrong phase", state: store.OnboardingState{
				Phase: store.OnboardingPhaseSetup, ProviderKind: "codex", ProviderID: "codex", AccountID: "account", Revision: 2,
			},
			kind: "codex", body: `{"onboarding_revision":2}`, status: http.StatusConflict, code: "ONBOARDING_PHASE_INVALID",
		},
		{
			name: "wrong kind", state: store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 2},
			kind: "claude-code", body: `{"onboarding_revision":2}`, status: http.StatusUnprocessableEntity, code: "ONBOARDING_PROVIDER_UNSUPPORTED",
		},
		{
			name: "dirty connect staging", state: store.OnboardingState{
				Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", ProviderID: "old", Revision: 2,
			},
			kind: "codex", body: `{"onboarding_revision":2}`, status: http.StatusInternalServerError, code: "ONBOARDING_STATE_UNAVAILABLE",
		},
		{
			name: "correct context", state: store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 2},
			kind: "codex", body: `{"onboarding_revision":2}`, status: http.StatusOK, wantStart: true,
			wantContext: &connectOnboardingContext{Revision: 2, Kind: "codex"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			env.setState(t, tt.state)
			if tt.state.Phase == store.OnboardingPhaseConnect || tt.state.Phase == store.OnboardingPhaseSetup {
				env.replaceProviderStages(t, appOnboardingProviderWire{
					Kind: "codex", Status: "pending", Position: 0,
				})
			}
			starts := 0
			connectMgr = &connectManager{
				runner:        &fakeRunner{installed: true, auth: authLoggedOut},
				ensure:        env.a.st.EnsureProviderForKind,
				createAccount: env.a.st.CreateLLMAccount,
				dataDir:       env.dataDir,
				newID:         func() string { starts++; return "context-account" },
				logger:        env.a.logger,
				loginTimeout:  5 * time.Second,
				pollInterval:  5 * time.Millisecond,
			}
			rr := env.serve(http.MethodPost, "/llm/providers/"+tt.kind+"/connect", tt.body)
			if tt.code != "" {
				requireOnboardingCode(t, rr, tt.status, tt.code)
			} else if rr.Code != tt.status {
				t.Fatalf("status=%d body=%s; want %d", rr.Code, rr.Body.String(), tt.status)
			}
			if got := starts > 0; got != tt.wantStart {
				t.Fatalf("manager started=%v; want %v", got, tt.wantStart)
			}
			if !tt.wantStart {
				if _, err := os.Stat(filepath.Join(env.dataDir, "accounts")); !os.IsNotExist(err) {
					t.Fatalf("rejected request created account root: %v", err)
				}
				return
			}
			connectMgr.mu.Lock()
			job := connectMgr.job
			connectMgr.mu.Unlock()
			if !equalConnectOnboardingContext(job.onboarding, tt.wantContext) {
				t.Fatalf("job context=%+v; want %+v", job.onboarding, tt.wantContext)
			}
			connectMgr.cancel(tt.kind)
			waitConnectJobDone(t, connectMgr)
		})
	}
}

func TestConnectOnboardingRouteStagesDisabledAccountAndAdvancesState(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	selected := env.serve(http.MethodPut, "/onboarding/providers", `{"revision":1,"selected_kinds":["codex"]}`)
	if selected.Code != http.StatusOK {
		t.Fatalf("select provider status=%d body=%s", selected.Code, selected.Body.String())
	}
	begun := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":2,"kind":"codex"}`)
	if begun.Code != http.StatusOK {
		t.Fatalf("begin provider status=%d body=%s", begun.Code, begun.Body.String())
	}
	state := env.state(t)
	createCalls := 0
	connectMgr = &connectManager{
		runner:           &fakeRunner{installed: true, auth: authLoggedIn},
		ensure:           env.a.st.EnsureProviderForKind,
		ensureOnboarding: env.a.st.EnsureOnboardingProviderForKind,
		ensureModels: func(kind string) error {
			return env.a.st.ReplaceLLMModels(kind, store.LLMModelDiscovered, cliProviderModels(env.a.st, kind, kind))
		},
		createAccount:  func(store.LLMAccount) error { createCalls++; return nil },
		bindOnboarding: env.a.st.BindOnboardingAccount,
		dataDir:        env.dataDir,
		newID:          func() string { return "onboarding-account" },
		logger:         env.a.logger,
		loginTimeout:   500 * time.Millisecond,
		pollInterval:   5 * time.Millisecond,
	}

	rr := env.serve(http.MethodPost, "/llm/providers/codex/connect",
		fmt.Sprintf(`{"label":"onboarding","onboarding_revision":%d}`, state.Revision))
	if rr.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", rr.Code, rr.Body.String())
	}
	terminal := waitPhase(t, connectMgr, "codex", phaseConnected)
	if terminal.ProviderID == "" || terminal.AccountID == "" {
		t.Fatalf("terminal=%+v", terminal)
	}
	if createCalls != 0 {
		t.Fatalf("onboarding used createAccount %d times", createCalls)
	}
	accounts, err := env.a.st.LLMAccounts(terminal.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != terminal.AccountID || accounts[0].Enabled {
		t.Fatalf("onboarding accounts=%+v", accounts)
	}
	providers, err := env.a.st.LLMProviders()
	if err != nil {
		t.Fatal(err)
	}
	var connectedProvider *store.LLMProvider
	for i := range providers {
		if providers[i].ID == terminal.ProviderID {
			connectedProvider = &providers[i]
			break
		}
	}
	if connectedProvider == nil || connectedProvider.Enabled {
		t.Fatalf("onboarding Providers=%+v; want connected Provider disabled", providers)
	}
	onboarding := env.state(t)
	if onboarding.AccountID != terminal.AccountID || onboarding.ProviderID != terminal.ProviderID ||
		onboarding.Phase != store.OnboardingPhaseSetup || onboarding.Revision != state.Revision+1 {
		t.Fatalf("state=%+v", onboarding)
	}
	body, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), accounts[0].ConfigDir) || strings.Contains(string(body), "configDir") {
		t.Fatalf("terminal leaked config dir: %s", body)
	}
}

func TestConnectOnboardingStateChangeDuringLoginFailsClosed(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	selected := env.serve(http.MethodPut, "/onboarding/providers", `{"revision":1,"selected_kinds":["codex"]}`)
	if selected.Code != http.StatusOK {
		t.Fatalf("select provider status=%d body=%s", selected.Code, selected.Body.String())
	}
	begun := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":2,"kind":"codex"}`)
	if begun.Code != http.StatusOK {
		t.Fatalf("begin provider status=%d body=%s", begun.Code, begun.Body.String())
	}
	state := env.state(t)
	runner := &gatedAccountLabelRunner{
		fakeRunner: fakeRunner{installed: true, auth: authLoggedIn},
		entered:    make(chan struct{}), release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(runner.release) }) })
	connectMgr = &connectManager{
		runner:           runner,
		ensure:           env.a.st.EnsureProviderForKind,
		ensureOnboarding: env.a.st.EnsureOnboardingProviderForKind,
		ensureModels:     func(string) error { return nil },
		createAccount:    env.a.st.CreateLLMAccount,
		bindOnboarding:   env.a.st.BindOnboardingAccount,
		dataDir:          env.dataDir,
		newID:            func() string { return "stale-account" },
		logger:           env.a.logger,
		loginTimeout:     500 * time.Millisecond,
		pollInterval:     5 * time.Millisecond,
	}
	rr := env.serve(http.MethodPost, "/llm/providers/codex/connect",
		fmt.Sprintf(`{"onboarding_revision":%d}`, state.Revision))
	if rr.Code != http.StatusOK {
		t.Fatalf("connect status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("login did not reach pre-persistence gate")
	}
	if _, err := env.db.Exec(`UPDATE app_onboarding_state SET revision = revision + 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(runner.release) })
	terminal := waitPhase(t, connectMgr, "codex", phaseError)
	if terminal.ProviderID != "" || terminal.AccountID != "" || strings.Contains(strings.ToLower(terminal.Error), "revision") {
		t.Fatalf("stale terminal leaked detail or IDs: %+v", terminal)
	}
	waitConnectJobDone(t, connectMgr)
	requireAccountPresent(t, env, "codex", "stale-account", false)
	if _, err := os.Stat(accountConfigDir(env.dataDir, "codex", "stale-account")); !os.IsNotExist(err) {
		t.Fatalf("stale bind left config dir: %v", err)
	}
	got := env.state(t)
	if got.Phase != store.OnboardingPhaseConnect || got.AccountID != "" || got.Revision != state.Revision+1 {
		t.Fatalf("stale bind advanced onboarding: %+v", got)
	}
}

func TestAppOnboardingProviderDoesNotCancelUnrelatedConnect(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 1})
	ctx, cancel := context.WithCancel(context.Background())
	job := newManualConnectJob("claude-code", cancel)
	connectMgr = &connectManager{job: job, jobKind: "claude-code"}
	t.Cleanup(func() { cancel(); close(job.done) })
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if ctx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("unrelated connect job was canceled")
	}
}

func TestAppOnboardingProviderDoesNotCancelSameKindNormalConnect(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	job := newManualConnectJob("codex", cancel)
	connectMgr = &connectManager{job: job, jobKind: "codex"}
	t.Cleanup(func() { cancel(); close(job.done) })

	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if ctx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("onboarding mutation canceled a normal same-kind Connect")
	}
}

func TestAppOnboardingProviderRejectsMismatchedConnectContextWithoutCancel(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{
		Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 4,
	})
	ctx, cancel := context.WithCancel(context.Background())
	job := newManualConnectJob("codex", cancel)
	job.onboarding = &connectOnboardingContext{Revision: 3, Kind: "codex"}
	connectMgr = &connectManager{job: job, jobKind: "codex"}
	t.Cleanup(func() { cancel(); close(job.done) })
	before := env.state(t)

	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":4,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if ctx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("mismatched onboarding Connect was canceled")
	}
	if after := env.state(t); after != before {
		t.Fatalf("mismatched context changed state: before=%+v after=%+v", before, after)
	}
}

func TestAppOnboardingRejectedBeginLeavesConnectActive(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 1})
	ctx, cancel := context.WithCancel(context.Background())
	job := newManualConnectJob("codex", cancel)
	job.onboarding = &connectOnboardingContext{Revision: 1, Kind: "codex"}
	connectMgr = &connectManager{job: job, jobKind: "codex"}
	t.Cleanup(func() { cancel(); close(job.done) })
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if ctx.Err() != nil || job.snapshot().Phase == phaseCanceled {
		t.Fatal("rejected begin canceled Connect")
	}
}

func TestAppOnboardingProviderPhaseRejectionPreservesState(t *testing.T) {
	env := newOnboardingRouteTestEnv(t)
	env.setState(t, store.OnboardingState{Phase: store.OnboardingPhaseConnect, ProviderKind: "codex", Revision: 1})
	before := env.state(t)
	rr := env.serve(http.MethodPut, "/onboarding/provider", `{"revision":1,"kind":"claude-code"}`)
	requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PHASE_INVALID")
	if got := env.state(t); got != before {
		t.Fatalf("rejected begin changed state: before=%+v after=%+v", before, got)
	}
}
