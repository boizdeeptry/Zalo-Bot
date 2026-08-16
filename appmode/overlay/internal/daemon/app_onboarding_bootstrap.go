package daemon

import (
	"context"
	"errors"
	"net/http"
	"os"

	"agentdc/internal/providercatalog"
	"agentdc/internal/store"
)

const appOnboardingBootstrapProbeMessage = "Xin chào"

type appOnboardingBootstrapRequest struct {
	Revision int64 `json:"revision"`
}

func (runtimeContext appRuntimeContext) handleOnboardingBootstrap(
	w http.ResponseWriter,
	r *http.Request,
) {
	a := runtimeContext.api
	var request appOnboardingBootstrapRequest
	if !a.decodeOnboardingBody(w, r, &request) {
		return
	}
	if !a.requireOnboardingRevision(w, request.Revision) {
		return
	}
	runtimeContext.appOnboardingBootstrap(w, r, request.Revision)
}

// appOnboardingBootstrap owns phase dispatch. Task 10 deliberately leaves the
// Persona branch stable; packaged Persona advancement is added by Task 11.
func (runtimeContext appRuntimeContext) appOnboardingBootstrap(
	w http.ResponseWriter,
	r *http.Request,
	expectedRevision int64,
) {
	a := runtimeContext.api
	// Persona completion must authenticate the immutable package before it
	// enters the global mutation critical section. The core re-reads and
	// validates this snapshot under that lock before advancing.
	personaSnapshot, personaSnapshotErr := runtimeContext.onboardingStore().OnboardingSnapshot()
	if personaSnapshotErr == nil &&
		validateRuntimeOnboardingSnapshot(runtimeContext.registry, personaSnapshot) == nil &&
		personaSnapshot.State.Revision == expectedRevision &&
		personaSnapshot.State.Phase == store.OnboardingPhasePersona {
		updated, err := runtimeContext.completePackagedOnboardingPersona(r.Context(), expectedRevision)
		if err != nil {
			runtimeContext.writePackagedPersonaBootstrapError(w, r, err)
			return
		}
		runtimeContext.bootstrapOnboardingTest(w, r, updated.Revision)
		return
	}
	onboardingMutationMu.Lock()
	if r.Context().Err() != nil {
		onboardingMutationMu.Unlock()
		a.writeOnboardingTestCanceled(w)
		return
	}
	snapshot, err := runtimeContext.onboardingStore().OnboardingSnapshot()
	if err != nil {
		onboardingMutationMu.Unlock()
		a.writeOnboardingBootstrapStoreError(w, err)
		return
	}
	if err := validateRuntimeOnboardingSnapshot(runtimeContext.registry, snapshot); err != nil {
		onboardingMutationMu.Unlock()
		a.writeOnboardingStoreError(w, store.ErrOnboardingConfigurationChanged)
		return
	}
	if snapshot.State.Revision != expectedRevision {
		onboardingMutationMu.Unlock()
		a.writeOnboardingStoreError(w, store.ErrOnboardingConflict)
		return
	}
	switch snapshot.State.Phase {
	case store.OnboardingPhaseCompleted:
		response, projectionErr := runtimeOnboardingStatusResponse(
			runtimeContext.registry, snapshot, "",
		)
		onboardingMutationMu.Unlock()
		if projectionErr != nil {
			a.writeOnboardingStateUnavailable(w, projectionErr)
			return
		}
		a.writeJSON(w, http.StatusOK, response)
	case store.OnboardingPhasePersona:
		onboardingMutationMu.Unlock()
		if personaSnapshotErr != nil {
			a.writeOnboardingBootstrapStoreError(w, personaSnapshotErr)
			return
		}
		a.writeOnboardingStoreError(w, store.ErrOnboardingConflict)
	case store.OnboardingPhaseTest:
		onboardingMutationMu.Unlock()
		runtimeContext.bootstrapOnboardingTest(w, r, expectedRevision)
	default:
		onboardingMutationMu.Unlock()
		a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidPhase)
	}
}

func (runtimeContext appRuntimeContext) writePackagedPersonaBootstrapError(
	w http.ResponseWriter,
	r *http.Request,
	err error,
) {
	a := runtimeContext.api
	if errors.Is(err, errAppPersonaRepairRequired) {
		if a.logger != nil {
			a.logger.Error("onboarding bootstrap: working Persona requires repair")
		}
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_PERSONA_REPAIR_REQUIRED",
			"Văn phong hiện tại cần được kiểm tra trước khi tiếp tục", nil)
		return
	}
	if r.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		a.writeOnboardingTestCanceled(w)
		return
	}
	if errors.Is(err, store.ErrOnboardingConflict) || errors.Is(err, store.ErrOnboardingInvalidPhase) {
		a.writeOnboardingStoreError(w, err)
		return
	}
	if !errors.Is(err, errAppPersonaDefaultsInvalid) {
		a.writeOnboardingBootstrapStoreError(w, err)
		return
	}
	if a.logger != nil {
		a.logger.Error("onboarding bootstrap: packaged Persona rejected", "error_kind", "persona_defaults_invalid")
	}
	a.writeLLMErr(w, http.StatusInternalServerError, "ONBOARDING_PERSONA_DEFAULTS_INVALID",
		"Không thể xác thực văn phong mặc định an toàn", nil)
}

func (a *api) writeOnboardingBootstrapStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, providercatalog.ErrInvalidSelection),
		errors.Is(err, providercatalog.ErrUnsupportedKind),
		errors.Is(err, store.ErrOnboardingConflict),
		errors.Is(err, store.ErrOnboardingProviderUnsupported),
		errors.Is(err, store.ErrOnboardingInvalidPhase),
		errors.Is(err, store.ErrOnboardingInvalidStagingOwnership),
		errors.Is(err, store.ErrOnboardingConfigurationChanged),
		errors.Is(err, store.ErrOnboardingModelUnavailable),
		errors.Is(err, store.ErrOnboardingPersonaMismatch):
		a.writeOnboardingStoreError(w, err)
	default:
		if a.logger != nil {
			a.logger.Error("onboarding bootstrap: state unavailable", "error_kind", "state_unavailable")
		}
		a.writeLLMErr(w, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE",
			"Không đọc hoặc cập nhật được trạng thái thiết lập", nil)
	}
}

// bootstrapOnboardingTest admits exactly one owner under the mutation mutex,
// releases every global lock for factory/model I/O, then promotes that owner
// before taking the live-route fence for final postflight, Save, and Complete.
func (runtimeContext appRuntimeContext) bootstrapOnboardingTest(
	w http.ResponseWriter,
	r *http.Request,
	expectedRevision int64,
) {
	a := runtimeContext.api
	onboardingMutationMu.Lock()
	if err := runtimeContext.resolveOnboardingTestPersonaRecovery(expectedRevision); err != nil {
		onboardingMutationMu.Unlock()
		runtimeContext.writePackagedPersonaBootstrapError(w, r, err)
		return
	}
	lease, admitted := beginAppOnboardingTest()
	if !admitted {
		onboardingMutationMu.Unlock()
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_BOOTSTRAP_BUSY",
			"Một lượt chuẩn bị trợ lý khác đang chạy; hãy đợi rồi thử lại", nil)
		return
	}
	defer lease.finish()
	if r.Context().Err() != nil {
		onboardingMutationMu.Unlock()
		a.writeOnboardingTestCanceled(w)
		return
	}
	prepared, err := runtimeContext.prepareOnboardingTest(
		r.Context(), expectedRevision, appOnboardingBootstrapProbeMessage,
	)
	if err != nil {
		onboardingMutationMu.Unlock()
		a.writeOnboardingStoreError(w, err)
		return
	}
	testCtx, cancel := context.WithTimeout(r.Context(), appOnboardingTestTimeout)
	defer cancel()
	if !lease.bindCancel(cancel) {
		onboardingMutationMu.Unlock()
		a.writeOnboardingTestCanceled(w)
		return
	}
	onboardingMutationMu.Unlock()

	result, err := runtimeContext.executeOnboardingTest(testCtx, prepared)
	if err != nil || testCtx.Err() != nil {
		if !a.writeOnboardingTestContextError(w, r, testCtx) {
			a.writeLLMErr(w, http.StatusBadGateway, "ONBOARDING_TEST_FAILED",
				"Không thể hoàn tất lượt kiểm tra với nhà cung cấp đã chọn", nil)
		}
		return
	}

	onboardingMutationMu.Lock()
	if a.writeOnboardingTestContextError(w, r, testCtx) {
		onboardingMutationMu.Unlock()
		return
	}
	transition, promoted := promoteAppOnboardingTest(testCtx, lease)
	if !promoted {
		onboardingMutationMu.Unlock()
		if !a.writeOnboardingTestContextError(w, r, testCtx) {
			a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_BOOTSTRAP_BUSY",
				"Trạng thái chuẩn bị trợ lý đã thay đổi; hãy tải lại rồi thử tiếp", nil)
		}
		return
	}

	runtimeContext.appLLMRouteCheckpoint(
		appLLMRouteOperationComplete, appLLMRoutePhaseBeforeLock,
	)
	appLLMRouteMutationMu.Lock()
	runtimeContext.appLLMRouteCheckpoint(appLLMRouteOperationComplete, appLLMRoutePhaseLocked)
	fencesHeld := true
	releaseFences := func() {
		if !fencesHeld {
			return
		}
		fencesHeld = false
		appLLMRouteMutationMu.Unlock()
		transition.release()
		onboardingMutationMu.Unlock()
	}
	defer releaseFences()
	if a.writeOnboardingTestContextError(w, r, testCtx) {
		releaseFences()
		return
	}
	result, err = runtimeContext.postflightPreparedOnboardingTest(testCtx, prepared, result)
	if err != nil {
		releaseFences()
		if a.writeOnboardingTestContextError(w, r, testCtx) {
			return
		}
		runtimeContext.writeBootstrapTestPostflightError(w, err)
		return
	}
	if a.writeOnboardingTestContextError(w, r, testCtx) {
		releaseFences()
		return
	}
	saved, err := runtimeContext.savePreparedOnboardingTestReceipt(testCtx, prepared)
	if err != nil {
		releaseFences()
		if a.writeOnboardingTestContextError(w, r, testCtx) {
			return
		}
		if errors.Is(err, errAppOnboardingTestTokenFailed) {
			a.writeLLMErr(w, http.StatusInternalServerError, "ONBOARDING_TEST_TOKEN_FAILED",
				"Không thể tạo biên nhận kiểm tra an toàn", nil)
			return
		}
		a.writeOnboardingStoreError(w, err)
		return
	}

	appOnboardingTestAfterCommit()
	if testCtx.Err() != nil {
		releaseFences()
		runtimeContext.compensateBootstrapReceipt(saved)
		if r.Context().Err() == nil {
			a.writeOnboardingTestContextError(w, r, testCtx)
		}
		return
	}
	completed, _, completeErr := runtimeContext.completeOnboardingLocked(
		testCtx,
		saved.state.Revision,
		saved.token,
		appOnboardingCompleteNow().UTC(),
	)
	if completeErr != nil {
		releaseFences()
		runtimeContext.compensateBootstrapReceipt(saved)
		if r.Context().Err() != nil {
			return
		}
		if a.writeOnboardingTestContextError(w, r, testCtx) {
			return
		}
		a.writeOnboardingCompleteError(w, completeErr)
		return
	}
	response, projectionErr := runtimeOnboardingStatusResponse(
		runtimeContext.registry,
		store.OnboardingSnapshot{State: completed},
		"",
	)
	releaseFences()
	if r.Context().Err() != nil {
		return
	}
	if projectionErr != nil {
		a.writeOnboardingStateUnavailable(w, projectionErr)
		return
	}
	a.writeJSON(w, http.StatusOK, response)
}

func (runtimeContext appRuntimeContext) resolveOnboardingTestPersonaRecovery(expectedRevision int64) error {
	snapshot, err := runtimeContext.onboardingStore().OnboardingSnapshot()
	if err != nil {
		return err
	}
	if err := validateRuntimeOnboardingSnapshot(runtimeContext.registry, snapshot); err != nil {
		return store.ErrOnboardingConfigurationChanged
	}
	if snapshot.State.Revision != expectedRevision {
		return store.ErrOnboardingConflict
	}
	if snapshot.State.Phase != store.OnboardingPhaseTest {
		return store.ErrOnboardingInvalidPhase
	}
	personaPath, err := appPackagedPersonaWorkingPath(runtimeContext.api)
	if err != nil {
		return errAppPersonaRepairRequired
	}
	if _, missing, err := runtimeContext.readPersonaWorking(personaPath); err != nil || missing {
		return errAppPersonaRepairRequired
	}
	journal, err := readAgentPersonaRecovery(personaPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errAppPersonaRepairRequired
	}
	storedToken, err := runtimeContext.api.st.AgentPersonaRecoveryToken()
	if err != nil || storedToken != journal.record.Token {
		return errAppPersonaRepairRequired
	}
	if err := runtimeContext.api.resolveAgentPersonaRecovery(personaPath); err != nil {
		return errAppPersonaRepairRequired
	}
	return nil
}

func (runtimeContext appRuntimeContext) writeBootstrapTestPostflightError(
	w http.ResponseWriter,
	err error,
) {
	a := runtimeContext.api
	switch {
	case errors.Is(err, errAppOnboardingTestAnswerInvalid):
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "ONBOARDING_TEST_ANSWER_INVALID",
			"Câu trả lời kiểm tra không hợp lệ", nil)
	case errors.Is(err, errAppOnboardingPersonaNotApplied):
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "ONBOARDING_PERSONA_NOT_APPLIED",
			"Câu trả lời kiểm tra chưa áp dụng đúng tên hiển thị của bot", nil)
	default:
		a.writeOnboardingStoreError(w, err)
	}
}

func (runtimeContext appRuntimeContext) compensateBootstrapReceipt(
	saved appOnboardingSavedTestReceipt,
) {
	if err := runtimeContext.compensateOnboardingTestReceipt(saved); err != nil &&
		runtimeContext.api.logger != nil {
		runtimeContext.api.logger.Error(
			"onboarding bootstrap: compensate receipt",
			"error_kind", "compensation_failed",
		)
	}
}
