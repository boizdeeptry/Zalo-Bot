package daemon

import (
	"context"
	"errors"
	"net/http"

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
	onboardingMutationMu.Lock()
	if r.Context().Err() != nil {
		onboardingMutationMu.Unlock()
		a.writeOnboardingTestCanceled(w)
		return
	}
	snapshot, err := runtimeContext.onboardingStore().OnboardingSnapshot()
	if err != nil {
		onboardingMutationMu.Unlock()
		a.writeOnboardingStateUnavailable(w, err)
		return
	}
	if err := validateRuntimeOnboardingSnapshot(runtimeContext.registry, snapshot); err != nil {
		onboardingMutationMu.Unlock()
		a.writeOnboardingStateUnavailable(w, err)
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
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_BOOTSTRAP_PERSONA_PENDING",
			"Persona đóng gói chưa sẵn sàng để tự động hoàn tất", nil)
	case store.OnboardingPhaseTest:
		onboardingMutationMu.Unlock()
		runtimeContext.bootstrapOnboardingTest(w, r, expectedRevision)
	default:
		onboardingMutationMu.Unlock()
		a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidPhase)
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
