package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"agentdc/internal/store"
)

var errRuntimeConnectUnsupported = errors.New("runtime Connect is unsupported")

// connectRuntimeBinding is the immutable operational snapshot owned by one
// admitted job. Every function value is resolved from the request's runtime
// context before onboarding inspection, account-ID allocation, or filesystem
// work. The state machine never needs to consult a registry or package global.
type connectRuntimeBinding struct {
	driver                   appConnectDriver
	ensureAccountProvider    func() error
	ensureOnboardingProvider func() error
	seedConnectedModels      func(providerID string) error
	createAccount            func(store.LLMAccount) error
	bindOnboarding           func(
		expectedRevision int64,
		kind string,
		account store.LLMAccount,
	) (store.OnboardingSnapshot, error)
}

func appConnectDriverIsNil(driver appConnectDriver) bool {
	if driver == nil {
		return true
	}
	value := reflect.ValueOf(driver)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// resolveConnectRuntimeBinding performs admission without Store/filesystem
// mutation. A registration can be valid structurally while its factory returns
// a nil driver at runtime, so the produced driver is checked as well as every
// required callback before the handler starts any preflight I/O.
func (ctx appRuntimeContext) resolveConnectRuntimeBinding(
	kind string,
) (connectRuntimeBinding, error) {
	if ctx.api == nil || ctx.api.st == nil || ctx.connect == nil {
		return connectRuntimeBinding{}, fmt.Errorf("%w: runtime context is unavailable", errRuntimeConnectUnsupported)
	}
	registration, exists := ctx.registry.registration(kind)
	if !exists || registration.Metadata.Visibility != appProviderVisibilityVisible ||
		registration.Metadata.ConnectionMode != appProviderConnectionAccount {
		return connectRuntimeBinding{}, fmt.Errorf("%w: Provider %q is not connectable", errRuntimeConnectUnsupported, kind)
	}
	factory := registration.Connect
	accountStrategy := registration.EnsureAccountProvider
	seeder := registration.SeedConnectedModels
	if factory == nil || accountStrategy == nil || seeder == nil {
		return connectRuntimeBinding{}, fmt.Errorf("%w: Provider %q has incomplete Connect behavior", errRuntimeConnectUnsupported, kind)
	}
	driver := factory(ctx.api.logger)
	if appConnectDriverIsNil(driver) {
		return connectRuntimeBinding{}, fmt.Errorf("%w: Provider %q produced no Connect driver", errRuntimeConnectUnsupported, kind)
	}

	// Capture the Store and catalog-bound wrapper as values now. The closures
	// below do not read ctx.registry, connectMgr, or any production singleton.
	st := ctx.api.st
	onboarding := ctx.onboardingStore()
	return connectRuntimeBinding{
		driver: driver,
		ensureAccountProvider: func() error {
			return accountStrategy(st)
		},
		ensureOnboardingProvider: func() error {
			return onboarding.EnsureOnboardingProviderForKind(kind)
		},
		seedConnectedModels: func(providerID string) error {
			if providerID != kind {
				return fmt.Errorf("Connect Provider identity changed from %q to %q", kind, providerID)
			}
			return seeder(st, providerID)
		},
		createAccount: st.CreateLLMAccount,
		bindOnboarding: func(
			expectedRevision int64,
			boundKind string,
			account store.LLMAccount,
		) (store.OnboardingSnapshot, error) {
			return onboarding.BindOnboardingAccount(expectedRevision, boundKind, account)
		},
	}, nil
}

// validateRuntimeConnectOnboardingSnapshot is deliberately Connect-specific.
// Task 6 will make the general daemon projection registry-aware; until then a
// context-bound Connect request must not call validateOnboardingSnapshot,
// whose helpers delegate the production registry.
func validateRuntimeConnectOnboardingSnapshot(
	registry appProviderRuntimeRegistry,
	snapshot store.OnboardingSnapshot,
) error {
	state := snapshot.State
	if state.Revision <= 0 || state.CompletedVersion < 0 {
		return errors.New("Connect onboarding snapshot has invalid version metadata")
	}
	if state.ProviderKind != "" && !registry.supportsOnboarding(state.ProviderKind) {
		return errors.New("Connect onboarding snapshot has an unsupported active Provider")
	}

	kinds := make([]string, len(snapshot.Stages))
	for index, stage := range snapshot.Stages {
		kinds[index] = stage.Kind
		if stage.Position != index || !registry.supportsOnboarding(stage.Kind) {
			return errors.New("Connect onboarding stages are not contiguous or supported")
		}
		switch stage.Status {
		case "pending":
			if stage.ProviderID != "" || stage.AccountID != "" || stage.ModelID != "" {
				return errors.New("pending Connect stage owns persisted identity")
			}
		case "ready":
			if stage.ProviderID == "" || stage.AccountID == "" || stage.ModelID == "" {
				return errors.New("ready Connect sibling has incomplete identity")
			}
		default:
			return errors.New("Connect onboarding stage has an unknown status")
		}
	}
	canonical, err := registry.canonicalOnboardingKinds(kinds)
	if err != nil || !slicesEqualStrings(canonical, kinds) {
		return errors.New("Connect onboarding stages are not canonical")
	}
	if state.Phase != store.OnboardingPhaseConnect {
		return nil
	}
	if !connectOnboardingStateIsClean(state) {
		return errors.New("Connect onboarding state owns staged identity or receipt data")
	}
	stage, selected := onboardingStageForKind(snapshot.Stages, state.ProviderKind)
	if !selected || stage.Status != "pending" || stage.ProviderID != "" ||
		stage.AccountID != "" || stage.ModelID != "" {
		return errors.New("Connect onboarding state does not own one pending Provider")
	}
	return nil
}

type connectRuntimeBindingResolver func(string) (connectRuntimeBinding, error)

func (ctx appRuntimeContext) handleLLMConnectStart(w http.ResponseWriter, r *http.Request) {
	ctx.handleLLMConnectStartWithResolver(w, r, ctx.resolveConnectRuntimeBinding)
}

func (ctx appRuntimeContext) handleLLMConnectStartWithResolver(
	w http.ResponseWriter,
	r *http.Request,
	resolve connectRuntimeBindingResolver,
) {
	a := ctx.api
	kind := r.PathValue("kind")
	binding, err := resolve(kind)
	if err != nil {
		a.writeLLMErr(w, http.StatusBadRequest, "CONNECT_KIND_UNSUPPORTED",
			"loại tài khoản này chưa kết nối được ở bản này", map[string]string{"kind": kind})
		return
	}

	var body connectStartRequest
	if !a.decodeLLMBody(w, r, &body) {
		return
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = "Tài khoản"
	}

	// Admission shares the onboarding mutation gate. Runtime behavior was
	// already resolved above, before this catalog-bound Store inspection.
	onboardingMutationMu.Lock()
	if r.Context().Err() != nil {
		onboardingMutationMu.Unlock()
		a.writeLLMErr(w, http.StatusRequestTimeout, "CONNECT_REQUEST_CANCELED",
			"yêu cầu kết nối đã bị huỷ", nil)
		return
	}
	var onboardingContext *connectOnboardingContext
	if body.OnboardingRevision != nil {
		if !a.requireOnboardingRevision(w, *body.OnboardingRevision) {
			onboardingMutationMu.Unlock()
			return
		}
		snapshot, snapshotErr := ctx.onboardingStore().OnboardingSnapshot()
		if snapshotErr != nil {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStateUnavailable(w, snapshotErr)
			return
		}
		if validationErr := validateRuntimeConnectOnboardingSnapshot(ctx.registry, snapshot); validationErr != nil {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStateUnavailable(w, validationErr)
			return
		}
		state := snapshot.State
		if state.Revision != *body.OnboardingRevision {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStoreError(w, store.ErrOnboardingConflict)
			return
		}
		if !onboardingRequired(state) || state.Phase != store.OnboardingPhaseConnect {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidPhase)
			return
		}
		if state.ProviderKind != kind {
			onboardingMutationMu.Unlock()
			a.writeOnboardingStoreError(w, store.ErrOnboardingProviderUnsupported)
			return
		}
		onboardingContext = &connectOnboardingContext{Revision: state.Revision, Kind: kind}
	}
	state, startErr := ctx.connect.startBound(kind, label, binding, onboardingContext)
	onboardingMutationMu.Unlock()
	if errors.Is(startErr, errConnectBusy) {
		a.writeLLMErr(w, http.StatusConflict, "CONNECT_BUSY", "đang có phiên kết nối khác", nil)
		return
	}
	if errors.Is(startErr, errConnectCleanupPending) {
		a.writeLLMErr(w, http.StatusInternalServerError, "CONNECT_CLEANUP_FAILED",
			connectCleanupFailedMessage, nil)
		return
	}
	if startErr != nil {
		a.writeLLMInternal(w, "không khởi động được kết nối", startErr)
		return
	}
	a.writeJSON(w, http.StatusOK, state)
}

func (ctx appRuntimeContext) handleLLMConnectStatus(w http.ResponseWriter, r *http.Request) {
	state, ok := ctx.connect.status(r.PathValue("kind"))
	if !ok {
		ctx.api.writeJSON(w, http.StatusOK, map[string]any{"phase": "idle"})
		return
	}
	ctx.api.writeJSON(w, http.StatusOK, state)
}

func (ctx appRuntimeContext) handleLLMConnectCancel(w http.ResponseWriter, r *http.Request) {
	ok := ctx.connect.cancel(r.PathValue("kind"))
	ctx.api.writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
}
