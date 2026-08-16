package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentdc/internal/store"
)

const onboardingBootstrapPersonaForTest = "BOOTSTRAP-PERSONA-PRIVATE: nói ngắn gọn, chân thành."

func newOnboardingBootstrapHarness(
	t *testing.T,
	kinds []string,
	models map[string]string,
	revision int64,
	mutate func([]appProviderRuntimeRegistration),
) *runtimeOnboardingHarness {
	t.Helper()
	harness := newRuntimeOnboardingHarness(t, mutate)
	snapshot := harness.prepareReadyRoute(t, kinds, models)
	configureAgentPersonaForOnboardingTest(t, harness.env, onboardingBootstrapPersonaForTest)
	if err := harness.env.a.st.SetAgentDisplayName("Bé Mi"); err != nil {
		t.Fatal(err)
	}
	state := snapshot.State
	state.Phase = store.OnboardingPhaseTest
	state.ProviderKind = ""
	state.ProviderID = ""
	state.AccountID = ""
	state.ModelID = ""
	state.PersonaFingerprint = agentPersonaFingerprint(
		[]byte(onboardingBootstrapPersonaForTest), "Bé Mi",
	)
	state.TestNonceHash = ""
	state.TestExpiresAt = ""
	state.Revision = revision
	harness.env.setState(t, state)
	return harness
}

func installOnboardingBootstrapSeams(
	t *testing.T,
	now time.Time,
	randomBytes []byte,
	timeout time.Duration,
) string {
	t.Helper()
	withAppOnboardingTestSeams(
		t,
		defaultAppOnboardingTestExecute,
		func() time.Time { return now },
		appOnboardingRandomFromReader(bytes.NewReader(randomBytes)),
		timeout,
	)
	oldCompleteNow := appOnboardingCompleteNow
	appOnboardingCompleteNow = func() time.Time { return now.Add(time.Second) }
	t.Cleanup(func() { appOnboardingCompleteNow = oldCompleteNow })
	return base64.RawURLEncoding.EncodeToString(randomBytes)
}

func onboardingBootstrapHandler(
	t *testing.T,
	harness *runtimeOnboardingHarness,
	checkpoint func(string, string),
) http.Handler {
	t.Helper()
	ctx := harness.ctx
	ctx.routeCheckpoint = checkpoint
	return runtimeLiveHandler(t, ctx)
}

func serveOnboardingBootstrap(
	handler http.Handler,
	ctx context.Context,
	revision int64,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(
		http.MethodPost,
		"/onboarding/bootstrap",
		strings.NewReader(fmt.Sprintf(`{"revision":%d}`, revision)),
	).WithContext(ctx)
	req.Header.Set(portalHeader, "1")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func onboardingBootstrapLocksFree() bool {
	if !onboardingMutationMu.TryLock() {
		return false
	}
	onboardingMutationMu.Unlock()
	if !appLLMRouteMutationMu.TryLock() {
		return false
	}
	appLLMRouteMutationMu.Unlock()
	return true
}

func onboardingBootstrapState(
	t *testing.T,
	harness *runtimeOnboardingHarness,
) store.OnboardingState {
	t.Helper()
	state, err := harness.ctx.onboardingStore().OnboardingState()
	if err != nil {
		t.Fatalf("OnboardingState() = %v", err)
	}
	return state
}

func TestOnboardingBootstrapFromTestCompletesAtSecondSuccessor(t *testing.T) {
	var calls atomic.Int32
	harness := newOnboardingBootstrapHarness(
		t,
		[]string{"future-cli"},
		map[string]string{"future-cli": "future-model"},
		41,
		func(registrations []appProviderRuntimeRegistration) {
			future := runtimeOnboardingRegistration(t, registrations, "future-cli")
			future.NewOnboardingMember = func(
				_ *api,
				entry store.OnboardingTestRouteEntry,
			) (appOnboardingMemberRun, error) {
				if entry.Kind != "future-cli" || entry.ProviderID != "future-cli" {
					return nil, store.ErrOnboardingInvalidStagingOwnership
				}
				return func(context.Context, string, func(string)) (string, error) {
					calls.Add(1)
					return "Xin chào, tôi là Bé Mi.", nil
				}, nil
			}
		},
	)
	fixedNow := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	randomBytes := bytes.Repeat([]byte{0x41}, sha256.Size)
	rawToken := installOnboardingBootstrapSeams(t, fixedNow, randomBytes, time.Second)
	var logs syncLogBuffer
	harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), 41,
	)
	if rr.Code != http.StatusOK {
		t.Fatalf("bootstrap status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
	if response.Phase != store.OnboardingPhaseCompleted || response.Revision != 43 ||
		response.Required || response.CompletedVersion != store.CurrentOnboardingVersion ||
		len(response.Providers) != 0 {
		t.Fatalf("bootstrap response = %+v; want completed revision 43", response)
	}
	wantOptionKinds := []string{"codex", "future-cli", "claude-code"}
	wantOptionRanks := []int{10, 50, 100}
	gotOptionKinds := make([]string, len(response.ProviderOptions))
	gotOptionRanks := make([]int, len(response.ProviderOptions))
	for index, option := range response.ProviderOptions {
		gotOptionKinds[index] = option.Kind
		gotOptionRanks[index] = option.RouteRank
	}
	if !slices.Equal(gotOptionKinds, wantOptionKinds) ||
		!slices.Equal(gotOptionRanks, wantOptionRanks) {
		t.Fatalf("committed synthetic options = %q/%v; want %q/%v",
			gotOptionKinds, gotOptionRanks, wantOptionKinds, wantOptionRanks)
	}
	if calls.Load() != 1 {
		t.Fatalf("synthetic Provider calls = %d; want 1", calls.Load())
	}
	route, err := harness.env.a.st.LLMRoute()
	if err != nil {
		t.Fatal(err)
	}
	if len(route.Entries) != 1 || route.Entries[0].ProviderID != "future-cli" ||
		route.Entries[0].ModelID != "future-model" || !route.Entries[0].Enabled {
		t.Fatalf("completed live route = %+v", route)
	}
	state := onboardingBootstrapState(t, harness)
	if state.Phase != store.OnboardingPhaseCompleted || state.Revision != 43 ||
		state.TestNonceHash != "" || state.TestExpiresAt != "" {
		t.Fatalf("completed state = %+v", state)
	}
	for _, private := range []string{
		rawToken,
		onboardingBootstrapPersonaForTest,
		accountConfigDir(harness.env.dataDir, "future-cli", "future-cli-account"),
		"test_token",
		"test_nonce_hash",
	} {
		if strings.Contains(rr.Body.String(), private) || strings.Contains(logs.String(), private) {
			t.Fatalf("bootstrap response/log leaked %q: body=%s logs=%s",
				private, rr.Body.String(), logs.String())
		}
	}
	requireOnboardingLeaseGlobalsClean(t)
}

func TestOnboardingBootstrapReplacesExistingReceiptAtSecondSuccessor(t *testing.T) {
	var calls atomic.Int32
	harness := newOnboardingBootstrapHarness(
		t,
		[]string{"future-cli"},
		map[string]string{"future-cli": "future-model"},
		46,
		func(registrations []appProviderRuntimeRegistration) {
			future := runtimeOnboardingRegistration(t, registrations, "future-cli")
			future.NewOnboardingMember = func(
				*api,
				store.OnboardingTestRouteEntry,
			) (appOnboardingMemberRun, error) {
				return func(context.Context, string, func(string)) (string, error) {
					calls.Add(1)
					return "Xin chào, tôi là Bé Mi.", nil
				}, nil
			}
		},
	)
	fixedNow := time.Date(2026, 8, 16, 8, 15, 0, 0, time.UTC)
	route, err := harness.ctx.onboardingStore().OnboardingTestRoute(context.Background(), 46)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := harness.ctx.onboardingStore().SaveOnboardingTestReceipt(
		context.Background(),
		route,
		strings.Repeat("19", sha256.Size),
		store.NewOnboardingTestReceiptPolicy(fixedNow.Add(-time.Minute)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if prior.Revision != 47 || prior.TestNonceHash == "" {
		t.Fatalf("prior receipt = %+v", prior)
	}
	installOnboardingBootstrapSeams(
		t, fixedNow, bytes.Repeat([]byte{0x47}, sha256.Size), time.Second,
	)

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), prior.Revision,
	)
	if rr.Code != http.StatusOK {
		t.Fatalf("existing-receipt bootstrap status=%d body=%s", rr.Code, rr.Body.String())
	}
	response := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
	if response.Phase != store.OnboardingPhaseCompleted || response.Revision != prior.Revision+2 {
		t.Fatalf("existing-receipt completion = %+v; start=%+v", response, prior)
	}
	if calls.Load() != 1 {
		t.Fatalf("existing-receipt retry Provider calls=%d; want exactly 1", calls.Load())
	}
	requireOnboardingLeaseGlobalsClean(t)
}

func TestOnboardingBootstrapUsesRealFallbackAndWinningIdentity(t *testing.T) {
	var callsMu sync.Mutex
	var calls []string
	var lockViolations atomic.Int32
	harness := newOnboardingBootstrapHarness(
		t,
		[]string{"codex", "claude-code"},
		map[string]string{"codex": "codex-model", "claude-code": "claude-model"},
		51,
		func(registrations []appProviderRuntimeRegistration) {
			for _, kind := range []string{"codex", "claude-code"} {
				kind := kind
				registration := runtimeOnboardingRegistration(t, registrations, kind)
				registration.NewOnboardingMember = func(
					_ *api,
					entry store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					if !onboardingBootstrapLocksFree() {
						lockViolations.Add(1)
					}
					return func(context.Context, string, func(string)) (string, error) {
						if !onboardingBootstrapLocksFree() {
							lockViolations.Add(1)
						}
						callsMu.Lock()
						calls = append(calls, entry.ProviderID)
						callsMu.Unlock()
						if kind == "codex" {
							return "", errors.New("FIRST_PROVIDER_PRIVATE_FAILURE")
						}
						return "Xin chào, tôi là Bé Mi từ fallback.", nil
					}, nil
				}
			}
		},
	)
	installOnboardingBootstrapSeams(
		t,
		time.Date(2026, 8, 16, 8, 30, 0, 0, time.UTC),
		bytes.Repeat([]byte{0x52}, sha256.Size),
		time.Second,
	)

	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), context.Background(), 51,
	)
	if rr.Code != http.StatusOK {
		t.Fatalf("fallback bootstrap status=%d body=%s", rr.Code, rr.Body.String())
	}
	callsMu.Lock()
	gotCalls := slices.Clone(calls)
	callsMu.Unlock()
	if !slices.Equal(gotCalls, []string{"codex", "claude-code"}) {
		t.Fatalf("fallback calls = %v; want canonical route order", gotCalls)
	}
	if lockViolations.Load() != 0 {
		t.Fatalf("factory/model I/O observed mutation or route lock %d times", lockViolations.Load())
	}
	if strings.Contains(rr.Body.String(), "fallback") ||
		strings.Contains(rr.Body.String(), "FIRST_PROVIDER_PRIVATE_FAILURE") {
		t.Fatalf("bootstrap exposed answer/provider failure: %s", rr.Body.String())
	}
}

func TestOnboardingBootstrapOneFlightExecutesProviderOnce(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProvider)
	var calls atomic.Int32
	harness := newOnboardingBootstrapHarness(
		t,
		[]string{"future-cli"},
		map[string]string{"future-cli": "future-model"},
		61,
		func(registrations []appProviderRuntimeRegistration) {
			future := runtimeOnboardingRegistration(t, registrations, "future-cli")
			future.NewOnboardingMember = func(
				*api,
				store.OnboardingTestRouteEntry,
			) (appOnboardingMemberRun, error) {
				return func(context.Context, string, func(string)) (string, error) {
					calls.Add(1)
					enterOnce.Do(func() { close(entered) })
					<-release
					return "Xin chào, tôi là Bé Mi.", nil
				}, nil
			}
		},
	)
	installOnboardingBootstrapSeams(
		t,
		time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC),
		bytes.Repeat([]byte{0x61}, sha256.Size),
		time.Second,
	)
	handler := onboardingBootstrapHandler(t, harness, nil)
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstDone <- serveOnboardingBootstrap(handler, context.Background(), 61)
	}()
	runtimeLiveWait(t, entered, "first bootstrap provider")
	second := serveOnboardingBootstrap(handler, context.Background(), 61)
	requireOnboardingCode(t, second, http.StatusConflict, "ONBOARDING_BOOTSTRAP_BUSY")
	testChat := runtimeLiveServeHandler(
		t,
		handler,
		http.MethodPost,
		"/onboarding/test-chat",
		`{"revision":61,"message":"xin chào"}`,
	)
	requireOnboardingCode(t, testChat, http.StatusConflict, "ONBOARDING_TEST_BUSY")
	if calls.Load() != 1 {
		releaseProvider()
		t.Fatalf("concurrent bootstrap Provider calls = %d; want 1", calls.Load())
	}
	releaseProvider()
	first := runtimeLiveReceive(t, firstDone, "first bootstrap response")
	if first.Code != http.StatusOK {
		t.Fatalf("first bootstrap status=%d body=%s", first.Code, first.Body.String())
	}
	if state := onboardingBootstrapState(t, harness); state.Phase != store.OnboardingPhaseCompleted || state.Revision != 63 {
		t.Fatalf("one-flight completed state = %+v", state)
	}
	requireOnboardingLeaseGlobalsClean(t)
}

func TestOnboardingBootstrapCancellationBeforePromotionDoesNotComplete(t *testing.T) {
	requestCtx, cancel := context.WithCancel(context.Background())
	harness := newOnboardingBootstrapHarness(
		t,
		[]string{"future-cli"},
		map[string]string{"future-cli": "future-model"},
		71,
		func(registrations []appProviderRuntimeRegistration) {
			future := runtimeOnboardingRegistration(t, registrations, "future-cli")
			future.NewOnboardingMember = func(
				*api,
				store.OnboardingTestRouteEntry,
			) (appOnboardingMemberRun, error) {
				return func(context.Context, string, func(string)) (string, error) {
					cancel()
					return "Xin chào, tôi là Bé Mi.", nil
				}, nil
			}
		},
	)
	installOnboardingBootstrapSeams(
		t,
		time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC),
		bytes.Repeat([]byte{0x71}, sha256.Size),
		time.Second,
	)
	beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
	rr := serveOnboardingBootstrap(
		onboardingBootstrapHandler(t, harness, nil), requestCtx, 71,
	)
	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("canceled bootstrap status=%d body=%s", rr.Code, rr.Body.String())
	}
	state := onboardingBootstrapState(t, harness)
	if state.Phase != store.OnboardingPhaseTest || state.Revision != 71 || state.TestNonceHash != "" {
		t.Fatalf("cancellation before promotion mutated state: %+v", state)
	}
	if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
		t.Fatalf("cancellation before promotion changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
	}
	requireOnboardingLeaseGlobalsClean(t)
}

func TestOnboardingBootstrapFinalRouteFenceAndPromotedCancellation(t *testing.T) {
	t.Run("final postflight holds ordered locks", func(t *testing.T) {
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			81,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		installOnboardingBootstrapSeams(
			t,
			time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x21}, sha256.Size),
			time.Second,
		)
		var sawLocked atomic.Bool
		var locksHeld atomic.Bool
		handler := onboardingBootstrapHandler(t, harness, func(operation, phase string) {
			if operation != appLLMRouteOperationComplete || phase != appLLMRoutePhaseLocked {
				return
			}
			sawLocked.Store(true)
			mutationHeld := !onboardingMutationMu.TryLock()
			if !mutationHeld {
				onboardingMutationMu.Unlock()
			}
			routeHeld := !appLLMRouteMutationMu.TryLock()
			if !routeHeld {
				appLLMRouteMutationMu.Unlock()
			}
			locksHeld.Store(mutationHeld && routeHeld)
			if _, err := harness.env.db.Exec(
				`UPDATE llm_models SET available=0 WHERE provider_id='future-cli'`,
			); err != nil {
				t.Errorf("inject final model drift: %v", err)
			}
		})

		rr := serveOnboardingBootstrap(handler, context.Background(), 81)
		requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, "ONBOARDING_NO_MODEL")
		if !sawLocked.Load() || !locksHeld.Load() {
			t.Fatalf("final postflight fence sawLocked=%v locksHeld=%v",
				sawLocked.Load(), locksHeld.Load())
		}
		if state := onboardingBootstrapState(t, harness); state.Phase != store.OnboardingPhaseTest ||
			state.Revision != 81 || state.TestNonceHash != "" {
			t.Fatalf("final postflight drift mutated receipt/completion: %+v", state)
		}
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("cancellation after promotion precedes Save", func(t *testing.T) {
		requestCtx, cancel := context.WithCancel(context.Background())
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			86,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		installOnboardingBootstrapSeams(
			t,
			time.Date(2026, 8, 16, 10, 15, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x22}, sha256.Size),
			time.Second,
		)
		var cancelOnce sync.Once
		handler := onboardingBootstrapHandler(t, harness, func(operation, phase string) {
			if operation == appLLMRouteOperationComplete && phase == appLLMRoutePhaseLocked {
				cancelOnce.Do(cancel)
			}
		})
		rr := serveOnboardingBootstrap(handler, requestCtx, 86)
		if rr.Code != http.StatusRequestTimeout {
			t.Fatalf("post-promotion cancellation status=%d body=%s", rr.Code, rr.Body.String())
		}
		if state := onboardingBootstrapState(t, harness); state.Phase != store.OnboardingPhaseTest ||
			state.Revision != 86 || state.TestNonceHash != "" {
			t.Fatalf("post-promotion cancellation saved a receipt: %+v", state)
		}
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("cancellation returned by final postflight keeps context semantics", func(t *testing.T) {
		requestCtx, cancel := context.WithCancel(context.Background())
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			88,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		installOnboardingBootstrapSeams(
			t,
			time.Date(2026, 8, 16, 10, 20, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x23}, sha256.Size),
			time.Second,
		)
		seedOnboardingRoute(t, harness.env, "old-live", "openai", true)
		beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)

		var enteredWithLiveContext atomic.Bool
		oldBeforePostflight := appOnboardingTestBeforePostflight
		appOnboardingTestBeforePostflight = func(ctx context.Context) {
			enteredWithLiveContext.Store(ctx.Err() == nil)
			cancel()
		}
		t.Cleanup(func() { appOnboardingTestBeforePostflight = oldBeforePostflight })

		rr := serveOnboardingBootstrap(
			onboardingBootstrapHandler(t, harness, nil), requestCtx, 88,
		)
		if !enteredWithLiveContext.Load() {
			t.Fatal("final postflight seam did not observe a live Test context")
		}
		requireOnboardingCode(t, rr, http.StatusRequestTimeout, "ONBOARDING_REQUEST_CANCELED")
		state := onboardingBootstrapState(t, harness)
		if state.Phase != store.OnboardingPhaseTest || state.Revision != 88 ||
			state.TestNonceHash != "" || state.TestExpiresAt != "" {
			t.Fatalf("postflight cancellation saved/completed state: %+v", state)
		}
		if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
			t.Fatalf("postflight cancellation changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
		}
		requireOnboardingLeaseGlobalsClean(t)
	})
}

func TestOnboardingBootstrapCompensatesReceiptAfterPromotionFailure(t *testing.T) {
	t.Run("Complete rollback clears exact saved receipt", func(t *testing.T) {
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			91,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		fixedNow := time.Date(2026, 8, 16, 10, 30, 0, 0, time.UTC)
		installOnboardingBootstrapSeams(
			t, fixedNow, bytes.Repeat([]byte{0x31}, sha256.Size), time.Second,
		)
		if _, err := harness.env.db.Exec(`CREATE TRIGGER fail_bootstrap_complete
BEFORE INSERT ON llm_combos BEGIN SELECT RAISE(ABORT, 'BOOTSTRAP_COMMIT_PRIVATE'); END`); err != nil {
			t.Fatal(err)
		}
		beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
		var saved store.OnboardingState
		oldAfterCommit := appOnboardingTestAfterCommit
		var savedErr error
		appOnboardingTestAfterCommit = func() {
			saved, savedErr = harness.ctx.onboardingStore().OnboardingState()
		}
		t.Cleanup(func() { appOnboardingTestAfterCommit = oldAfterCommit })

		rr := serveOnboardingBootstrap(
			onboardingBootstrapHandler(t, harness, nil), context.Background(), 91,
		)
		if savedErr != nil {
			t.Fatalf("read saved bootstrap state: %v", savedErr)
		}
		requireOnboardingCode(t, rr, http.StatusInternalServerError, "ONBOARDING_COMMIT_FAILED")
		if saved.Revision != 92 || saved.TestNonceHash == "" || saved.TestExpiresAt == "" {
			t.Fatalf("Save return snapshot was not r+1 receipt: %+v", saved)
		}
		state := onboardingBootstrapState(t, harness)
		if state.Phase != store.OnboardingPhaseTest || state.Revision != 93 ||
			state.TestNonceHash != "" || state.TestExpiresAt != "" {
			t.Fatalf("exact compensation state = %+v; want empty receipt at r+2", state)
		}
		if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
			t.Fatalf("failed Complete changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
		}
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("daemon Complete preflight drift is compensated", func(t *testing.T) {
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			93,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		fixedNow := time.Date(2026, 8, 16, 10, 37, 0, 0, time.UTC)
		installOnboardingBootstrapSeams(
			t, fixedNow, bytes.Repeat([]byte{0x39}, sha256.Size), time.Second,
		)
		seedOnboardingRoute(t, harness.env, "old-live", "openai", true)
		beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
		var saved store.OnboardingState
		var savedErr error
		oldAfterCommit := appOnboardingTestAfterCommit
		appOnboardingTestAfterCommit = func() {
			saved, savedErr = harness.ctx.onboardingStore().OnboardingState()
			if savedErr != nil {
				return
			}
			route, err := harness.ctx.onboardingStore().OnboardingTestRoute(
				context.Background(), saved.Revision,
			)
			if err != nil {
				t.Errorf("read saved route for drift: %v", err)
				return
			}
			if err := os.Rename(route.Entries[0].ConfigDir, route.Entries[0].ConfigDir+"-drift"); err != nil {
				t.Errorf("rename staged config after Save: %v", err)
			}
		}
		t.Cleanup(func() { appOnboardingTestAfterCommit = oldAfterCommit })

		rr := serveOnboardingBootstrap(
			onboardingBootstrapHandler(t, harness, nil), context.Background(), 93,
		)
		if savedErr != nil {
			t.Fatalf("read saved bootstrap state: %v", savedErr)
		}
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_CONFIGURATION_CHANGED")
		if saved.Revision != 94 || saved.TestNonceHash == "" {
			t.Fatalf("post-Save drift did not observe exact saved state: %+v", saved)
		}
		state := onboardingBootstrapState(t, harness)
		if state.Phase != store.OnboardingPhaseTest || state.Revision != 95 ||
			state.TestNonceHash != "" || state.TestExpiresAt != "" {
			t.Fatalf("post-Save preflight compensation = %+v", state)
		}
		if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
			t.Fatalf("post-Save drift changed old live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
		}
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("stale compensation preserves replacement", func(t *testing.T) {
		requestCtx, cancel := context.WithCancel(context.Background())
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			96,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		fixedNow := time.Date(2026, 8, 16, 10, 45, 0, 0, time.UTC)
		installOnboardingBootstrapSeams(
			t, fixedNow, bytes.Repeat([]byte{0x32}, sha256.Size), time.Second,
		)
		var replacement store.OnboardingState
		var replacementErr error
		oldAfterCommit := appOnboardingTestAfterCommit
		appOnboardingTestAfterCommit = func() {
			current, err := harness.ctx.onboardingStore().OnboardingState()
			if err != nil {
				replacementErr = err
				cancel()
				return
			}
			route, err := harness.ctx.onboardingStore().OnboardingTestRoute(
				context.Background(), current.Revision,
			)
			if err == nil {
				replacement, replacementErr = harness.ctx.onboardingStore().SaveOnboardingTestReceipt(
					context.Background(),
					route,
					strings.Repeat("7a", sha256.Size),
					store.NewOnboardingTestReceiptPolicy(fixedNow.Add(time.Second)),
				)
			} else {
				replacementErr = err
			}
			cancel()
		}
		t.Cleanup(func() { appOnboardingTestAfterCommit = oldAfterCommit })

		rr := serveOnboardingBootstrap(
			onboardingBootstrapHandler(t, harness, nil), requestCtx, 96,
		)
		if replacementErr != nil {
			t.Fatalf("replace bootstrap receipt: %v", replacementErr)
		}
		if rr.Body.Len() != 0 || len(rr.Header()) != 0 {
			t.Fatalf("canceled post-Save client received response: headers=%v body=%s",
				rr.Header(), rr.Body.String())
		}
		if current := onboardingBootstrapState(t, harness); current != replacement || current.Revision != 98 ||
			current.TestNonceHash == "" {
			t.Fatalf("stale compensation erased replacement: got=%+v want=%+v", current, replacement)
		}
		requireOnboardingLeaseGlobalsClean(t)
	})
}

func TestOnboardingBootstrapForeignBackCancelsAndReapsOwner(t *testing.T) {
	entered := make(chan struct{})
	returned := make(chan struct{})
	var enterOnce, returnOnce sync.Once
	harness := newOnboardingBootstrapHarness(
		t,
		[]string{"future-cli"},
		map[string]string{"future-cli": "future-model"},
		99,
		func(registrations []appProviderRuntimeRegistration) {
			future := runtimeOnboardingRegistration(t, registrations, "future-cli")
			future.NewOnboardingMember = func(
				*api,
				store.OnboardingTestRouteEntry,
			) (appOnboardingMemberRun, error) {
				return func(ctx context.Context, _ string, _ func(string)) (string, error) {
					enterOnce.Do(func() { close(entered) })
					<-ctx.Done()
					returnOnce.Do(func() { close(returned) })
					return "", ctx.Err()
				}, nil
			}
		},
	)
	fixedNow := time.Date(2026, 8, 16, 10, 52, 0, 0, time.UTC)
	route, err := harness.ctx.onboardingStore().OnboardingTestRoute(context.Background(), 99)
	if err != nil {
		t.Fatal(err)
	}
	withReceipt, err := harness.ctx.onboardingStore().SaveOnboardingTestReceipt(
		context.Background(),
		route,
		strings.Repeat("2b", sha256.Size),
		store.NewOnboardingTestReceiptPolicy(fixedNow),
	)
	if err != nil {
		t.Fatal(err)
	}
	installOnboardingBootstrapSeams(
		t, fixedNow, bytes.Repeat([]byte{0x59}, sha256.Size), time.Second,
	)
	seedOnboardingRoute(t, harness.env, "old-live", "openai", true)
	beforeLive := appOnboardingLiveRoutingBytes(t, harness.env)
	handler := onboardingBootstrapHandler(t, harness, nil)
	bootstrapDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		bootstrapDone <- serveOnboardingBootstrap(
			handler, context.Background(), withReceipt.Revision,
		)
	}()
	runtimeLiveWait(t, entered, "bootstrap runner before foreign Back")
	backDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		backDone <- runtimeLiveServeHandler(
			t,
			handler,
			http.MethodPost,
			"/onboarding/back-to-providers",
			fmt.Sprintf(`{"revision":%d}`, withReceipt.Revision),
		)
	}()
	runtimeLiveWait(t, returned, "bootstrap runner cancellation by Back")
	bootstrap := runtimeLiveReceive(t, bootstrapDone, "canceled bootstrap response")
	if bootstrap.Code != http.StatusRequestTimeout {
		t.Fatalf("foreign-canceled bootstrap status=%d body=%s", bootstrap.Code, bootstrap.Body.String())
	}
	back := runtimeLiveReceive(t, backDone, "foreign Back response")
	if back.Code != http.StatusOK {
		t.Fatalf("foreign Back status=%d body=%s", back.Code, back.Body.String())
	}
	state := onboardingBootstrapState(t, harness)
	if state.Phase != store.OnboardingPhaseProvider || state.Revision != withReceipt.Revision+1 {
		t.Fatalf("foreign Back final state = %+v", state)
	}
	if afterLive := appOnboardingLiveRoutingBytes(t, harness.env); afterLive != beforeLive {
		t.Fatalf("foreign Back activated/changed live route:\nbefore=%s\nafter=%s", beforeLive, afterLive)
	}
	requireOnboardingLeaseGlobalsClean(t)
}

func TestOnboardingBootstrapLostResponseLeavesCompletedGETAuthoritative(t *testing.T) {
	t.Run("cancellation after Complete never compensates", func(t *testing.T) {
		requestCtx, cancel := context.WithCancel(context.Background())
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			101,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		installOnboardingBootstrapSeams(
			t,
			time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x41}, sha256.Size),
			time.Second,
		)
		var logs syncLogBuffer
		harness.env.a.logger = slog.New(slog.NewTextHandler(&logs, nil))
		var cancelOnce sync.Once
		handler := onboardingBootstrapHandler(t, harness, func(operation, phase string) {
			if operation == appLLMRouteOperationComplete && phase == appLLMRoutePhaseWritten {
				cancelOnce.Do(cancel)
			}
		})
		rr := serveOnboardingBootstrap(handler, requestCtx, 101)
		if rr.Body.Len() != 0 || len(rr.Header()) != 0 {
			t.Fatalf("lost-response client received headers/body: headers=%v body=%s",
				rr.Header(), rr.Body.String())
		}
		state := onboardingBootstrapState(t, harness)
		if state.Phase != store.OnboardingPhaseCompleted || state.Revision != 103 {
			t.Fatalf("post-Complete cancellation state = %+v", state)
		}
		if strings.Contains(strings.ToLower(logs.String()), "compensat") {
			t.Fatalf("post-Complete cancellation attempted compensation: %s", logs.String())
		}
		status := runtimeLiveServeHandler(
			t, handler, http.MethodGet, "/onboarding/status", "",
		)
		if status.Code != http.StatusOK {
			t.Fatalf("authoritative GET status=%d body=%s", status.Code, status.Body.String())
		}
		current := decodeOnboardingResponse[appOnboardingStatusResponse](t, status)
		if current.Phase != store.OnboardingPhaseCompleted || current.Revision != 103 {
			t.Fatalf("authoritative GET = %+v", current)
		}
		requireOnboardingLeaseGlobalsClean(t)
	})

	t.Run("success projection does not reread Store", func(t *testing.T) {
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			106,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào, tôi là Bé Mi.", nil
					}, nil
				}
			},
		)
		installOnboardingBootstrapSeams(
			t,
			time.Date(2026, 8, 16, 11, 15, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x42}, sha256.Size),
			time.Second,
		)
		var closeErr error
		var closeOnce sync.Once
		handler := onboardingBootstrapHandler(t, harness, func(operation, phase string) {
			if operation == appLLMRouteOperationComplete && phase == appLLMRoutePhaseWritten {
				closeOnce.Do(func() { closeErr = harness.env.a.st.Close() })
			}
		})
		rr := serveOnboardingBootstrap(handler, context.Background(), 106)
		if closeErr != nil {
			t.Fatalf("close Store after Complete return: %v", closeErr)
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("pure projection status=%d body=%s", rr.Code, rr.Body.String())
		}
		response := decodeOnboardingResponse[appOnboardingStatusResponse](t, rr)
		if response.Phase != store.OnboardingPhaseCompleted || response.Revision != 108 {
			t.Fatalf("pure committed projection = %+v", response)
		}
		var phase string
		var revision int64
		if err := harness.env.db.QueryRow(
			`SELECT phase, revision FROM app_onboarding_state WHERE id=1`,
		).Scan(&phase, &revision); err != nil {
			t.Fatal(err)
		}
		if phase != store.OnboardingPhaseCompleted || revision != 108 {
			t.Fatalf("raw committed state = %s/%d", phase, revision)
		}
		requireOnboardingLeaseGlobalsClean(t)
	})
}

func TestOnboardingBootstrapRealFailuresAndPostflightDrift(t *testing.T) {
	t.Run("all Provider failures are sanitized", func(t *testing.T) {
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"codex", "claude-code"},
			map[string]string{"codex": "codex-model", "claude-code": "claude-model"},
			111,
			func(registrations []appProviderRuntimeRegistration) {
				for _, kind := range []string{"codex", "claude-code"} {
					registration := runtimeOnboardingRegistration(t, registrations, kind)
					registration.NewOnboardingMember = func(
						*api,
						store.OnboardingTestRouteEntry,
					) (appOnboardingMemberRun, error) {
						return func(context.Context, string, func(string)) (string, error) {
							return "", errors.New("PROVIDER_FAILURE_PRIVATE")
						}, nil
					}
				}
			},
		)
		installOnboardingBootstrapSeams(
			t,
			time.Date(2026, 8, 16, 11, 30, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x51}, sha256.Size),
			time.Second,
		)
		rr := serveOnboardingBootstrap(
			onboardingBootstrapHandler(t, harness, nil), context.Background(), 111,
		)
		requireOnboardingCode(t, rr, http.StatusBadGateway, "ONBOARDING_TEST_FAILED")
		if strings.Contains(rr.Body.String(), "PROVIDER_FAILURE_PRIVATE") {
			t.Fatalf("all-failed response leaked Provider error: %s", rr.Body.String())
		}
	})

	t.Run("semantic failure", func(t *testing.T) {
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			116,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(context.Context, string, func(string)) (string, error) {
						return "Xin chào nhưng thiếu danh tính.", nil
					}, nil
				}
			},
		)
		installOnboardingBootstrapSeams(
			t,
			time.Date(2026, 8, 16, 11, 45, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x52}, sha256.Size),
			time.Second,
		)
		rr := serveOnboardingBootstrap(
			onboardingBootstrapHandler(t, harness, nil), context.Background(), 116,
		)
		requireOnboardingCode(t, rr, http.StatusUnprocessableEntity, "ONBOARDING_PERSONA_NOT_APPLIED")
		if state := onboardingBootstrapState(t, harness); state.Revision != 116 || state.TestNonceHash != "" {
			t.Fatalf("semantic failure saved receipt: %+v", state)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		harness := newOnboardingBootstrapHarness(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
			121,
			func(registrations []appProviderRuntimeRegistration) {
				future := runtimeOnboardingRegistration(t, registrations, "future-cli")
				future.NewOnboardingMember = func(
					*api,
					store.OnboardingTestRouteEntry,
				) (appOnboardingMemberRun, error) {
					return func(ctx context.Context, _ string, _ func(string)) (string, error) {
						<-ctx.Done()
						return "", ctx.Err()
					}, nil
				}
			},
		)
		installOnboardingBootstrapSeams(
			t,
			time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
			bytes.Repeat([]byte{0x53}, sha256.Size),
			0,
		)
		rr := serveOnboardingBootstrap(
			onboardingBootstrapHandler(t, harness, nil), context.Background(), 121,
		)
		requireOnboardingCode(t, rr, http.StatusGatewayTimeout, "ONBOARDING_TEST_TIMEOUT")
	})

	drifts := []struct {
		name     string
		wantCode string
		wantHTTP int
		mutate   func(*testing.T, *runtimeOnboardingHarness, store.OnboardingTestRoute)
	}{
		{
			name: "route", wantCode: "ONBOARDING_STAGING_INVALID", wantHTTP: http.StatusConflict,
			mutate: func(t *testing.T, harness *runtimeOnboardingHarness, route store.OnboardingTestRoute) {
				t.Helper()
				entry := route.Entries[0]
				if err := harness.env.a.st.AddLLMModel(store.LLMModel{
					ProviderID: entry.ProviderID,
					ModelID:    "replacement-model",
					Name:       "replacement-model",
					Source:     store.LLMModelManual,
					Available:  true,
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := harness.env.db.Exec(
					`UPDATE app_onboarding_provider_stages SET model_id='replacement-model' WHERE kind=?`,
					entry.Kind,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "persona", wantCode: "ONBOARDING_PERSONA_CHANGED", wantHTTP: http.StatusConflict,
			mutate: func(t *testing.T, harness *runtimeOnboardingHarness, _ store.OnboardingTestRoute) {
				t.Helper()
				if err := os.WriteFile(harness.env.a.zalo.cfg.PersonaPath, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "account", wantCode: "ONBOARDING_STAGING_INVALID", wantHTTP: http.StatusConflict,
			mutate: func(t *testing.T, harness *runtimeOnboardingHarness, route store.OnboardingTestRoute) {
				t.Helper()
				driftDir := filepath.Join(harness.env.dataDir, "accounts", "future-cli", "drift-account")
				if err := os.MkdirAll(driftDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if _, err := harness.env.db.Exec(
					`UPDATE llm_accounts SET config_dir=? WHERE id=?`, driftDir, route.Entries[0].AccountID,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "model", wantCode: "ONBOARDING_NO_MODEL", wantHTTP: http.StatusUnprocessableEntity,
			mutate: func(t *testing.T, harness *runtimeOnboardingHarness, route store.OnboardingTestRoute) {
				t.Helper()
				if _, err := harness.env.db.Exec(
					`UPDATE llm_models SET available=0 WHERE provider_id=? AND model_id=?`,
					route.Entries[0].ProviderID, route.Entries[0].ModelID,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "config", wantCode: "ONBOARDING_STAGING_INVALID", wantHTTP: http.StatusConflict,
			mutate: func(t *testing.T, _ *runtimeOnboardingHarness, route store.OnboardingTestRoute) {
				t.Helper()
				if err := os.Rename(route.Entries[0].ConfigDir, route.Entries[0].ConfigDir+"-moved"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, drift := range drifts {
		t.Run("postflight "+drift.name+" drift", func(t *testing.T) {
			var mutateOnce sync.Once
			var harness *runtimeOnboardingHarness
			harness = newOnboardingBootstrapHarness(
				t,
				[]string{"future-cli"},
				map[string]string{"future-cli": "future-model"},
				126,
				func(registrations []appProviderRuntimeRegistration) {
					future := runtimeOnboardingRegistration(t, registrations, "future-cli")
					future.NewOnboardingMember = func(
						_ *api,
						entry store.OnboardingTestRouteEntry,
					) (appOnboardingMemberRun, error) {
						return func(context.Context, string, func(string)) (string, error) {
							mutateOnce.Do(func() {
								route := store.OnboardingTestRoute{Entries: []store.OnboardingTestRouteEntry{entry}}
								drift.mutate(t, harness, route)
							})
							return "Xin chào, tôi là Bé Mi.", nil
						}, nil
					}
				},
			)
			installOnboardingBootstrapSeams(
				t,
				time.Date(2026, 8, 16, 12, 15, 0, 0, time.UTC),
				bytes.Repeat([]byte{0x54}, sha256.Size),
				time.Second,
			)
			rr := serveOnboardingBootstrap(
				onboardingBootstrapHandler(t, harness, nil), context.Background(), 126,
			)
			requireOnboardingCode(t, rr, drift.wantHTTP, drift.wantCode)
			if state := onboardingBootstrapState(t, harness); state.Phase != store.OnboardingPhaseTest ||
				state.Revision != 126 || state.TestNonceHash != "" {
				t.Fatalf("postflight %s drift saved receipt: %+v", drift.name, state)
			}
			requireOnboardingLeaseGlobalsClean(t)
		})
	}
}

func TestOnboardingBootstrapStrictRequestAndCompletedNoop(t *testing.T) {
	invalid := []struct {
		name, body, code string
		status           int
	}{
		{name: "unknown field", body: `{"revision":1,"extra":true}`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
		{name: "duplicate field", body: `{"revision":1,"Revision":1}`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
		{name: "trailing object", body: `{"revision":1}{}`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
		{name: "array", body: `[{"revision":1}]`, status: http.StatusBadRequest, code: "ONBOARDING_REQUEST_INVALID"},
		{name: "zero revision", body: `{"revision":0}`, status: http.StatusUnprocessableEntity, code: "ONBOARDING_REVISION_INVALID"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			env := newOnboardingRouteTestEnv(t)
			before := env.state(t)
			rr := env.serve(http.MethodPost, "/onboarding/bootstrap", test.body)
			requireOnboardingCode(t, rr, test.status, test.code)
			if after := env.state(t); after != before {
				t.Fatalf("invalid bootstrap body mutated state: before=%+v after=%+v", before, after)
			}
		})
	}

	t.Run("Persona without a configured working path is a repair conflict", func(t *testing.T) {
		harness := newRuntimeOnboardingHarness(t, nil)
		snapshot := harness.prepareReadyRoute(
			t,
			[]string{"future-cli"},
			map[string]string{"future-cli": "future-model"},
		)
		rr := serveOnboardingBootstrap(
			onboardingBootstrapHandler(t, harness, nil), context.Background(), snapshot.State.Revision,
		)
		requireOnboardingCode(t, rr, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED")
	})

	t.Run("Completed exact revision is no-op and stale conflicts", func(t *testing.T) {
		env := newOnboardingRouteTestEnv(t)
		env.setState(t, store.OnboardingState{
			CompletedVersion: store.CurrentOnboardingVersion,
			Phase:            store.OnboardingPhaseCompleted,
			Revision:         141,
		})
		exact := env.serve(http.MethodPost, "/onboarding/bootstrap", `{"revision":141}`)
		if exact.Code != http.StatusOK {
			t.Fatalf("completed exact no-op status=%d body=%s", exact.Code, exact.Body.String())
		}
		response := decodeOnboardingResponse[appOnboardingStatusResponse](t, exact)
		if response.Phase != store.OnboardingPhaseCompleted || response.Revision != 141 {
			t.Fatalf("completed no-op = %+v", response)
		}
		stale := env.serve(http.MethodPost, "/onboarding/bootstrap", `{"revision":140}`)
		requireOnboardingCode(t, stale, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT")
		if state := env.state(t); state.Revision != 141 || state.Phase != store.OnboardingPhaseCompleted {
			t.Fatalf("stale completed bootstrap mutated state: %+v", state)
		}
	})
}
