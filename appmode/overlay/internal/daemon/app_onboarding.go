package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"agentdc/internal/store"
)

const appOnboardingRequestCap int64 = 64 << 10

var (
	onboardingMutationMu sync.Mutex
	onboardingTestMu     sync.Mutex
	onboardingTestActive bool

	errOnboardingUnsafeAccountPath = errors.New("unsafe onboarding account path")

	// These are narrow test seams. Production always removes through the already-opened data
	// directory root and uses a bounded wait long enough for Connect to reap its child process.
	removeAllOnboardingAccountRooted = func(root *os.Root, name string) error {
		return root.RemoveAll(name)
	}
	appOnboardingConnectCancelWait = 12 * time.Second

	appOnboardingTestNow                               = time.Now
	appOnboardingTestRandom  io.Reader                 = rand.Reader
	appOnboardingTestTimeout                           = 120 * time.Second
	appOnboardingTestExecute appOnboardingTestExecutor = defaultAppOnboardingTestExecute
)

type appOnboardingTestExecutor func(
	context.Context,
	*api,
	store.OnboardingState,
	store.OnboardingStagingAccount,
	string,
) (string, error)

type appOnboardingProviderRequest struct {
	Revision int64  `json:"revision"`
	Kind     string `json:"kind"`
}

type appOnboardingRestartRequest struct {
	Revision  int64 `json:"revision"`
	Confirmed bool  `json:"confirmed"`
}

type appOnboardingSetupRequest struct {
	Revision  int64  `json:"revision"`
	AccountID string `json:"account_id"`
}

type appOnboardingSetupResponse struct {
	Revision      int64  `json:"revision"`
	ProviderKind  string `json:"provider_kind"`
	ProviderID    string `json:"provider_id"`
	AccountID     string `json:"account_id"`
	ModelID       string `json:"model_id"`
	StagedComboID string `json:"staged_combo_id"`
	ComboName     string `json:"combo_name"`
}

type appOnboardingTestChatRequest struct {
	Revision int64  `json:"revision"`
	Message  string `json:"message"`
}

type appOnboardingTestChatResponse struct {
	Answer     string `json:"answer"`
	BotName    string `json:"bot_name"`
	ProviderID string `json:"provider_id"`
	ModelID    string `json:"model_id"`
	TestToken  string `json:"test_token"`
	ExpiresAt  string `json:"expires_at"`
	Revision   int64  `json:"revision"`
}

// appOnboardingStatusResponse is deliberately a projection rather than an alias of the Store
// state. Internal paths, staging ids, persona fingerprints and test receipts therefore cannot be
// exposed by accidentally adding a json tag to the persistence struct later.
type appOnboardingStatusResponse struct {
	Required              bool   `json:"required"`
	CurrentVersion        int64  `json:"current_version"`
	CompletedVersion      int64  `json:"completed_version"`
	Phase                 string `json:"phase"`
	ProviderKind          string `json:"provider_kind"`
	SuggestedProviderKind string `json:"suggested_provider_kind"`
	ProviderID            string `json:"provider_id"`
	AccountID             string `json:"account_id"`
	ModelID               string `json:"model_id"`
	RestartInProgress     bool   `json:"restart_in_progress"`
	Revision              int64  `json:"revision"`
}

func (a *api) handleOnboardingStatus(w http.ResponseWriter, _ *http.Request) {
	state, err := a.st.OnboardingState()
	if err != nil {
		a.writeOnboardingStateUnavailable(w, err)
		return
	}
	response, err := a.onboardingStatusResponse(state)
	if err != nil {
		a.writeOnboardingStateUnavailable(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, response)
}

func (a *api) handleOnboardingProvider(w http.ResponseWriter, r *http.Request) {
	var request appOnboardingProviderRequest
	if !a.decodeOnboardingBody(w, r, &request) {
		return
	}
	if !a.requireOnboardingRevision(w, request.Revision) {
		return
	}

	onboardingMutationMu.Lock()
	defer onboardingMutationMu.Unlock()
	if r.Context().Err() != nil {
		a.writeOnboardingCleanupFailed(w, r.Context().Err())
		return
	}

	state, ok := a.onboardingMutationState(w, request.Revision)
	if !ok {
		return
	}
	// Validate before any cleanup: SelectOnboardingProvider also owns this check, but it runs after
	// filesystem deletion by contract and an unsupported value must never cause that side effect.
	if !store.IsOnboardingProviderKind(request.Kind) {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "ONBOARDING_PROVIDER_UNSUPPORTED",
			"Nhà cung cấp này chưa được hỗ trợ trong bước thiết lập", nil)
		return
	}
	upgrade, err := preflightOnboardingProvider(state)
	if err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	if r.Context().Err() != nil {
		a.writeOnboardingCleanupFailed(w, r.Context().Err())
		return
	}
	if upgrade {
		updated, err := a.st.SelectOnboardingProvider(request.Revision, request.Kind, "")
		if err != nil {
			a.writeOnboardingStoreError(w, err)
			return
		}
		a.writeOnboardingStatus(w, updated)
		return
	}
	if !a.cancelOnboardingConnect(w, r.Context(), state) {
		return
	}
	if _, ok := a.onboardingMutationState(w, request.Revision); !ok {
		return
	}
	owned, err := a.st.OnboardingStagingAccount(request.Revision)
	if err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	if r.Context().Err() != nil {
		a.writeOnboardingCleanupFailed(w, r.Context().Err())
		return
	}
	if err := removeOwnedOnboardingAccount(a.cfg.Dir, owned); err != nil {
		a.writeOnboardingCleanupFailed(w, err)
		return
	}
	updated, err := a.st.SelectOnboardingProvider(request.Revision, request.Kind, owned.AccountID)
	if err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	a.writeOnboardingStatus(w, updated)
}

func (a *api) handleOnboardingRestart(w http.ResponseWriter, r *http.Request) {
	var request appOnboardingRestartRequest
	if !a.decodeOnboardingBody(w, r, &request) {
		return
	}
	if !a.requireOnboardingRevision(w, request.Revision) {
		return
	}
	if !request.Confirmed {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "ONBOARDING_RESTART_CONFIRMATION_REQUIRED",
			"Cần xác nhận trước khi thiết lập lại", nil)
		return
	}

	onboardingMutationMu.Lock()
	defer onboardingMutationMu.Unlock()
	if r.Context().Err() != nil {
		a.writeOnboardingCleanupFailed(w, r.Context().Err())
		return
	}

	state, ok := a.onboardingMutationState(w, request.Revision)
	if !ok {
		return
	}
	if err := preflightOnboardingRestart(state); err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	if r.Context().Err() != nil {
		a.writeOnboardingCleanupFailed(w, r.Context().Err())
		return
	}
	// Restart is a staged replacement. The completed Account and route stay live until the new
	// onboarding flow reaches Complete, so this path owns no Connect cancellation or filesystem
	// cleanup and passes no cleanup Account to Store.
	updated, err := a.st.RestartOnboarding(request.Revision, "")
	if err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	a.writeOnboardingStatus(w, updated)
}

func (a *api) handleOnboardingSetup(w http.ResponseWriter, r *http.Request) {
	var request appOnboardingSetupRequest
	if !a.decodeOnboardingBody(w, r, &request) {
		return
	}
	if !a.requireOnboardingRevision(w, request.Revision) {
		return
	}
	if request.AccountID == "" {
		a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidStagingOwnership)
		return
	}

	onboardingMutationMu.Lock()
	defer onboardingMutationMu.Unlock()
	if r.Context().Err() != nil {
		a.writeLLMErr(w, http.StatusRequestTimeout, "ONBOARDING_REQUEST_CANCELED",
			"Yêu cầu thiết lập đã bị huỷ", nil)
		return
	}

	state, err := a.st.OnboardingState()
	if err != nil {
		a.writeOnboardingStateUnavailable(w, err)
		return
	}
	if err := validateOnboardingState(state); err != nil {
		a.writeOnboardingStateUnavailable(w, err)
		return
	}
	if state.Revision == request.Revision+1 {
		if state.Phase != store.OnboardingPhasePersona || state.AccountID != request.AccountID {
			a.writeOnboardingStoreError(w, store.ErrOnboardingConflict)
			return
		}
		staged, err := a.st.StageOnboardingSetup(
			request.Revision,
			request.AccountID,
			state.ModelID,
		)
		if err != nil {
			a.writeOnboardingStoreError(w, err)
			return
		}
		a.writeOnboardingSetup(w, staged)
		return
	}
	if state.Revision != request.Revision {
		a.writeOnboardingStoreError(w, store.ErrOnboardingConflict)
		return
	}
	if state.Phase != store.OnboardingPhaseSetup {
		a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidPhase)
		return
	}
	if state.AccountID != request.AccountID {
		a.writeOnboardingStoreError(w, store.ErrOnboardingInvalidStagingOwnership)
		return
	}
	if _, err := a.st.OnboardingStagingAccount(request.Revision); err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	modelID, err := selectOnboardingModel(a.st, state.ProviderKind, state.ProviderID)
	if err != nil {
		if errors.Is(err, store.ErrOnboardingModelUnavailable) {
			a.writeOnboardingStoreError(w, err)
			return
		}
		a.writeOnboardingStateUnavailable(w, err)
		return
	}
	staged, err := a.st.StageOnboardingSetup(request.Revision, request.AccountID, modelID)
	if err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	a.writeOnboardingSetup(w, staged)
}

func (a *api) handleOnboardingTestChat(w http.ResponseWriter, r *http.Request) {
	var request appOnboardingTestChatRequest
	if !a.decodeOnboardingBody(w, r, &request) {
		return
	}
	if !a.requireOnboardingRevision(w, request.Revision) {
		return
	}
	message, ok := normalizeOnboardingTestMessage(request.Message)
	if !ok {
		a.writeLLMErr(w, http.StatusBadRequest, "ONBOARDING_TEST_MESSAGE_INVALID",
			"Tin nhắn kiểm tra phải có từ 1 đến 500 ký tự và không chứa ký tự điều khiển", nil)
		return
	}
	if !beginAppOnboardingTest() {
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_TEST_BUSY",
			"Một lượt kiểm tra khác đang chạy; hãy đợi lượt đó hoàn tất", nil)
		return
	}
	defer endAppOnboardingTest()

	// Keep the exact validated revision stable through runner completion and
	// receipt CAS. All persona/provider/setup mutations use this same mutex.
	onboardingMutationMu.Lock()
	defer onboardingMutationMu.Unlock()
	if r.Context().Err() != nil {
		a.writeOnboardingTestCanceled(w)
		return
	}
	state, ok := a.onboardingMutationState(w, request.Revision)
	if !ok {
		return
	}
	staged, persona, displayName, err := a.preflightOnboardingTestChat(state)
	if err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	prompt := buildOnboardingTestPrompt(persona, displayName, message)
	testCtx, cancel := context.WithTimeout(r.Context(), appOnboardingTestTimeout)
	defer cancel()
	answer, err := appOnboardingTestExecute(testCtx, a, state, staged, prompt)
	if err != nil || testCtx.Err() != nil {
		switch {
		case r.Context().Err() != nil || errors.Is(testCtx.Err(), context.Canceled):
			a.writeOnboardingTestCanceled(w)
		case errors.Is(testCtx.Err(), context.DeadlineExceeded):
			a.writeLLMErr(w, http.StatusGatewayTimeout, "ONBOARDING_TEST_TIMEOUT",
				"Lượt kiểm tra đã quá thời gian 120 giây", nil)
		default:
			a.writeLLMErr(w, http.StatusBadGateway, "ONBOARDING_TEST_FAILED",
				"Không thể hoàn tất lượt kiểm tra với nhà cung cấp đã chọn", nil)
		}
		return
	}
	answer = strings.TrimSpace(answer)
	if answer == "" || !normalizedOnboardingAnswerContainsName(answer, displayName) {
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "ONBOARDING_TEST_ANSWER_INVALID",
			"Câu trả lời kiểm tra chưa xác nhận đúng tên hiển thị của bot", nil)
		return
	}
	if r.Context().Err() != nil {
		a.writeOnboardingTestCanceled(w)
		return
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(appOnboardingTestRandom, nonce); err != nil {
		a.writeLLMErr(w, http.StatusInternalServerError, "ONBOARDING_TEST_TOKEN_FAILED",
			"Không thể tạo biên nhận kiểm tra an toàn", nil)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(nonce)
	digest := sha256.Sum256([]byte(token))
	now := appOnboardingTestNow().UTC()
	expiresAt := now.Add(10 * time.Minute)
	updated, err := a.st.SaveOnboardingTestReceipt(
		state.Revision,
		state.PersonaFingerprint,
		fmt.Sprintf("%x", digest[:]),
		expiresAt,
	)
	if err != nil {
		a.writeOnboardingStoreError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, appOnboardingTestChatResponse{
		Answer: answer, BotName: displayName, ProviderID: state.ProviderID,
		ModelID: state.ModelID, TestToken: token,
		ExpiresAt: expiresAt.Format(time.RFC3339Nano), Revision: updated.Revision,
	})
}

func beginAppOnboardingTest() bool {
	onboardingTestMu.Lock()
	defer onboardingTestMu.Unlock()
	if onboardingTestActive {
		return false
	}
	onboardingTestActive = true
	return true
}

func endAppOnboardingTest() {
	onboardingTestMu.Lock()
	onboardingTestActive = false
	onboardingTestMu.Unlock()
}

func normalizeOnboardingTestMessage(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	message := strings.TrimSpace(raw)
	if message == "" || utf8.RuneCountInString(message) > 500 {
		return "", false
	}
	for _, r := range message {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return message, true
}

func (a *api) preflightOnboardingTestChat(
	state store.OnboardingState,
) (store.OnboardingStagingAccount, []byte, string, error) {
	if state.CompletedVersion >= store.CurrentOnboardingVersion && !state.RestartInProgress {
		return store.OnboardingStagingAccount{}, nil, "", store.ErrOnboardingInvalidPhase
	}
	if state.Phase != store.OnboardingPhaseTest {
		return store.OnboardingStagingAccount{}, nil, "", store.ErrOnboardingInvalidPhase
	}
	if !store.IsOnboardingProviderKind(state.ProviderKind) ||
		state.ProviderID == "" || state.ProviderID != state.ProviderKind ||
		state.AccountID == "" || state.ModelID == "" || state.StagedComboID == "" ||
		state.PersonaFingerprint == "" {
		return store.OnboardingStagingAccount{}, nil, "", store.ErrOnboardingInvalidStagingOwnership
	}
	staged, err := a.st.OnboardingStagingAccount(state.Revision)
	if err != nil {
		return store.OnboardingStagingAccount{}, nil, "", err
	}
	models, err := a.st.LLMModels(state.ProviderID)
	if err != nil {
		return store.OnboardingStagingAccount{}, nil, "", err
	}
	modelAvailable := false
	for _, model := range models {
		if model.ProviderID == state.ProviderID && model.ModelID == state.ModelID && model.Available {
			modelAvailable = true
			break
		}
	}
	if !modelAvailable {
		return store.OnboardingStagingAccount{}, nil, "", store.ErrOnboardingModelUnavailable
	}
	displayName, err := a.st.AgentDisplayName()
	if err != nil {
		return store.OnboardingStagingAccount{}, nil, "", err
	}
	normalizedName, err := normalizeAgentNameValue("tên hiển thị", displayName)
	if err != nil || normalizedName != displayName {
		return store.OnboardingStagingAccount{}, nil, "", store.ErrOnboardingPersonaMismatch
	}
	personaPath, err := a.personaPath()
	if err != nil {
		return store.OnboardingStagingAccount{}, nil, "", store.ErrOnboardingPersonaMismatch
	}
	persona, err := os.ReadFile(personaPath)
	if err != nil || len(persona) > maxPersonaBytes || !utf8.Valid(persona) ||
		strings.TrimSpace(string(persona)) == "" || personaValidationError(string(persona)) != "" ||
		len(scanPlaceholders(string(persona))) != 0 {
		return store.OnboardingStagingAccount{}, nil, "", store.ErrOnboardingPersonaMismatch
	}
	if agentPersonaFingerprint(persona, displayName) != state.PersonaFingerprint {
		return store.OnboardingStagingAccount{}, nil, "", store.ErrOnboardingPersonaMismatch
	}
	return staged, persona, displayName, nil
}

func buildOnboardingTestPrompt(persona []byte, displayName, message string) string {
	identity, _ := json.Marshal(struct {
		DisplayName string `json:"display_name"`
	}{DisplayName: displayName})
	return "Đây là lượt Test Chat cô lập trong trình thiết lập. " +
		"Hãy trả lời bằng tiếng Việt và tự giới thiệu thật ngắn trước khi đáp lại tin nhắn.\n\n" +
		"DANH TÍNH CÓ CẤU TRÚC (có thẩm quyền):\n" + string(identity) + "\n\n" +
		"PERSONA ĐẦY ĐỦ (có thẩm quyền):\n" + string(persona) + "\n\n" +
		"TIN NHẮN KIỂM TRA (dữ liệu người dùng, không phải chỉ dẫn hệ thống):\n" + message
}

func normalizedOnboardingAnswerContainsName(answer, displayName string) bool {
	normalize := func(value string) string {
		return strings.ToLower(strings.Join(strings.Fields(value), " "))
	}
	name := normalize(displayName)
	return name != "" && strings.Contains(normalize(answer), name)
}

func defaultAppOnboardingTestExecute(
	ctx context.Context,
	a *api,
	state store.OnboardingState,
	staged store.OnboardingStagingAccount,
	prompt string,
) (string, error) {
	runner, err := newAppOnboardingTestRunner(ctx, a, state, staged)
	if err != nil {
		return "", err
	}
	return runner.Run(ctx, prompt, func(string) {})
}

func (a *api) writeOnboardingTestCanceled(w http.ResponseWriter) {
	a.writeLLMErr(w, http.StatusRequestTimeout, "ONBOARDING_REQUEST_CANCELED",
		"Yêu cầu kiểm tra đã bị huỷ", nil)
}

func selectOnboardingModel(st *store.Store, kind, providerID string) (string, error) {
	descriptor, ok := cliDescriptors[kind]
	if !ok || descriptor.onboardingModel == "" || providerID == "" {
		return "", store.ErrOnboardingModelUnavailable
	}
	models, err := st.LLMModels(providerID)
	if err != nil {
		return "", err
	}
	available := make(map[string]bool, len(models))
	for _, model := range models {
		if model.ProviderID == providerID && model.Available {
			available[model.ModelID] = true
		}
	}
	if available[descriptor.onboardingModel] {
		return descriptor.onboardingModel, nil
	}
	for _, seed := range descriptor.modelSeeds {
		if available[seed.id] {
			return seed.id, nil
		}
	}
	return "", store.ErrOnboardingModelUnavailable
}

func (a *api) writeOnboardingSetup(w http.ResponseWriter, staged store.OnboardingSetup) {
	a.writeJSON(w, http.StatusOK, appOnboardingSetupResponse{
		Revision:      staged.Revision,
		ProviderKind:  staged.ProviderKind,
		ProviderID:    staged.ProviderID,
		AccountID:     staged.AccountID,
		ModelID:       staged.ModelID,
		StagedComboID: staged.StagedComboID,
		ComboName:     staged.ComboName,
	})
}

// decodeOnboardingBody accepts exactly one bounded JSON object. Error details are intentionally
// not reflected because decoder errors may disclose field names from a future secret-bearing body.
func (a *api) decodeOnboardingBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, appOnboardingRequestCap)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.writeLLMErr(w, http.StatusRequestEntityTooLarge, "ONBOARDING_REQUEST_TOO_LARGE",
				"Dữ liệu thiết lập vượt quá giới hạn cho phép", nil)
			return false
		}
		a.writeLLMErr(w, http.StatusBadRequest, "ONBOARDING_REQUEST_INVALID",
			"Dữ liệu thiết lập không hợp lệ", nil)
		return false
	}
	if !utf8.Valid(body) {
		a.writeLLMErr(w, http.StatusBadRequest, "ONBOARDING_REQUEST_INVALID",
			"Dữ liệu thiết lập không hợp lệ", nil)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.writeLLMErr(w, http.StatusRequestEntityTooLarge, "ONBOARDING_REQUEST_TOO_LARGE",
				"Dữ liệu thiết lập vượt quá giới hạn cho phép", nil)
			return false
		}
		a.writeLLMErr(w, http.StatusBadRequest, "ONBOARDING_REQUEST_INVALID",
			"Dữ liệu thiết lập không hợp lệ", nil)
		return false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.writeLLMErr(w, http.StatusRequestEntityTooLarge, "ONBOARDING_REQUEST_TOO_LARGE",
				"Dữ liệu thiết lập vượt quá giới hạn cho phép", nil)
			return false
		}
		a.writeLLMErr(w, http.StatusBadRequest, "ONBOARDING_REQUEST_INVALID",
			"Mỗi yêu cầu chỉ được chứa một đối tượng JSON", nil)
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		a.writeLLMErr(w, http.StatusBadRequest, "ONBOARDING_REQUEST_INVALID",
			"Dữ liệu thiết lập phải là một đối tượng JSON", nil)
		return false
	}
	strict := json.NewDecoder(bytes.NewReader(trimmed))
	strict.DisallowUnknownFields()
	if err := strict.Decode(destination); err != nil {
		a.writeLLMErr(w, http.StatusBadRequest, "ONBOARDING_REQUEST_INVALID",
			"Dữ liệu thiết lập không hợp lệ", nil)
		return false
	}
	return true
}

func (a *api) requireOnboardingRevision(w http.ResponseWriter, revision int64) bool {
	if revision > 0 {
		return true
	}
	a.writeLLMErr(w, http.StatusUnprocessableEntity, "ONBOARDING_REVISION_INVALID",
		"Phiên bản trạng thái thiết lập không hợp lệ", nil)
	return false
}

func (a *api) onboardingMutationState(w http.ResponseWriter, expectedRevision int64) (store.OnboardingState, bool) {
	state, err := a.st.OnboardingState()
	if err != nil {
		a.writeOnboardingStateUnavailable(w, err)
		return store.OnboardingState{}, false
	}
	if err := validateOnboardingState(state); err != nil {
		a.writeOnboardingStateUnavailable(w, err)
		return store.OnboardingState{}, false
	}
	if state.Revision != expectedRevision {
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT",
			"Trạng thái thiết lập đã thay đổi; hãy tải lại rồi thử tiếp", nil)
		return store.OnboardingState{}, false
	}
	return state, true
}

func preflightOnboardingProvider(state store.OnboardingState) (upgrade bool, err error) {
	switch state.Phase {
	case store.OnboardingPhaseProvider,
		store.OnboardingPhaseConnect,
		store.OnboardingPhaseSetup,
		store.OnboardingPhasePersona,
		store.OnboardingPhaseTest:
		return false, nil
	case store.OnboardingPhaseCompleted:
		if state.CompletedVersion < store.CurrentOnboardingVersion && !state.RestartInProgress {
			return true, nil
		}
	}
	return false, store.ErrOnboardingInvalidPhase
}

func preflightOnboardingRestart(state store.OnboardingState) error {
	if state.Phase != store.OnboardingPhaseCompleted ||
		state.CompletedVersion != store.CurrentOnboardingVersion || state.RestartInProgress {
		return store.ErrOnboardingInvalidPhase
	}
	return nil
}

// cancelOnboardingConnect cancels only the live job owned by the exact selected kind/revision.
// Normal Connect and other onboarding contexts are unrelated. Waiting for done is essential:
// connectJob closes it only after child/process/config cleanup.
func (a *api) cancelOnboardingConnect(w http.ResponseWriter, ctx context.Context, state store.OnboardingState) bool {
	if state.ProviderKind == "" || connectMgr == nil {
		return true
	}
	manager := connectMgr
	manager.mu.Lock()
	job := manager.job
	if job == nil || manager.jobKind != state.ProviderKind {
		manager.mu.Unlock()
		return true
	}
	expected := &connectOnboardingContext{Revision: state.Revision, Kind: state.ProviderKind}
	finished := false
	select {
	case <-job.done:
		finished = true
	default:
	}
	if finished && !job.cleanupIsPending() {
		manager.mu.Unlock()
		return true
	}
	if job.onboarding == nil {
		manager.mu.Unlock()
		return true
	}
	if !equalConnectOnboardingContext(job.onboarding, expected) {
		manager.mu.Unlock()
		a.writeOnboardingStoreError(w, store.ErrOnboardingConflict)
		return false
	}
	if finished {
		err := manager.cleanupJobConfig(job, state.ProviderKind)
		manager.mu.Unlock()
		if err != nil {
			a.writeOnboardingCleanupFailed(w, err)
			return false
		}
		return true
	}
	manager.mu.Unlock()
	if ctx.Err() != nil {
		a.writeOnboardingCleanupFailed(w, ctx.Err())
		return false
	}
	if !job.requestCancel() {
		a.writeOnboardingCleanupFailed(w, errors.New("matching connect job refused cancellation"))
		return false
	}
	timer := time.NewTimer(appOnboardingConnectCancelWait)
	defer timer.Stop()
	select {
	case <-job.done:
		if job.snapshot().Phase != phaseCanceled {
			a.writeOnboardingCleanupFailed(w, errors.New("matching connect job did not remain canceled"))
			return false
		}
		return true
	case <-ctx.Done():
		a.writeOnboardingCleanupFailed(w, ctx.Err())
		return false
	case <-timer.C:
		a.writeOnboardingCleanupFailed(w, errors.New("matching connect cleanup timed out"))
		return false
	}
}

func (a *api) onboardingStatusResponse(state store.OnboardingState) (appOnboardingStatusResponse, error) {
	if err := validateOnboardingState(state); err != nil {
		return appOnboardingStatusResponse{}, err
	}
	projected := state
	if onboardingUpgradeRequired(state) {
		projected.Phase = store.OnboardingPhaseProvider
		projected.ProviderKind = ""
		projected.ProviderID = ""
		projected.AccountID = ""
		projected.ModelID = ""
	}
	suggested := ""
	if projected.ProviderKind == "" {
		var err error
		suggested, err = a.suggestedOnboardingProviderKind()
		if err != nil {
			return appOnboardingStatusResponse{}, err
		}
	}
	return appOnboardingStatusResponse{
		Required:              state.CompletedVersion < store.CurrentOnboardingVersion || state.RestartInProgress,
		CurrentVersion:        store.CurrentOnboardingVersion,
		CompletedVersion:      state.CompletedVersion,
		Phase:                 projected.Phase,
		ProviderKind:          projected.ProviderKind,
		SuggestedProviderKind: suggested,
		ProviderID:            projected.ProviderID,
		AccountID:             projected.AccountID,
		ModelID:               projected.ModelID,
		RestartInProgress:     state.RestartInProgress,
		Revision:              state.Revision,
	}, nil
}

func onboardingUpgradeRequired(state store.OnboardingState) bool {
	return state.Phase == store.OnboardingPhaseCompleted &&
		state.CompletedVersion < store.CurrentOnboardingVersion && !state.RestartInProgress
}

// validateOnboardingState treats the persisted singleton as untrusted input. Database CHECK
// constraints cover only part of the state machine and can be bypassed by corruption or an older
// build, so handlers must fail closed before suggesting a route, canceling Connect, or touching a
// staging directory. Later tasks intentionally own the finer provider/account/model relationships.
func validateOnboardingState(state store.OnboardingState) error {
	if state.Revision <= 0 {
		return errors.New("onboarding state has a nonpositive revision")
	}
	if state.CompletedVersion < 0 {
		return errors.New("onboarding state has a negative completed version")
	}
	if state.ProviderKind != "" && !store.IsOnboardingProviderKind(state.ProviderKind) {
		return errors.New("onboarding state has an unsupported selected provider")
	}

	switch state.Phase {
	case store.OnboardingPhaseProvider:
	case store.OnboardingPhaseConnect,
		store.OnboardingPhaseSetup,
		store.OnboardingPhasePersona,
		store.OnboardingPhaseTest:
		if !store.IsOnboardingProviderKind(state.ProviderKind) {
			return errors.New("onboarding phase requires a selected provider")
		}
	case store.OnboardingPhaseCompleted:
		if state.RestartInProgress {
			return errors.New("completed onboarding state has restart markers")
		}
	default:
		return errors.New("onboarding state has an unknown phase")
	}

	if state.CompletedVersion >= store.CurrentOnboardingVersion &&
		!state.RestartInProgress && state.Phase != store.OnboardingPhaseCompleted {
		return errors.New("completed onboarding version is in an unfinished phase")
	}
	if state.RestartInProgress &&
		(state.CompletedVersion < store.CurrentOnboardingVersion || state.Phase == store.OnboardingPhaseCompleted) {
		return errors.New("onboarding restart markers are inconsistent")
	}
	return nil
}

func (a *api) suggestedOnboardingProviderKind() (string, error) {
	route, err := a.st.LLMRoute()
	if err != nil {
		return "", err
	}
	providerID := ""
	for _, entry := range route.Entries {
		if entry.Enabled {
			providerID = entry.ProviderID
			break
		}
	}
	if providerID == "" {
		return "", nil
	}
	providers, err := a.st.LLMProviders()
	if err != nil {
		return "", err
	}
	for _, provider := range providers {
		if provider.ID == providerID && store.IsOnboardingProviderKind(provider.Kind) {
			return provider.Kind, nil
		}
	}
	return "", nil
}

func (a *api) writeOnboardingStatus(w http.ResponseWriter, state store.OnboardingState) {
	response, err := a.onboardingStatusResponse(state)
	if err != nil {
		a.writeOnboardingStateUnavailable(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, response)
}

func (a *api) writeOnboardingStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrOnboardingConflict):
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_REVISION_CONFLICT",
			"Trạng thái thiết lập đã thay đổi; hãy tải lại rồi thử tiếp", nil)
	case errors.Is(err, store.ErrOnboardingProviderUnsupported):
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "ONBOARDING_PROVIDER_UNSUPPORTED",
			"Nhà cung cấp này chưa được hỗ trợ trong bước thiết lập", nil)
	case errors.Is(err, store.ErrOnboardingInvalidPhase):
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_PHASE_INVALID",
			"Bước thiết lập hiện tại không cho phép thao tác này", nil)
	case errors.Is(err, store.ErrOnboardingInvalidStagingOwnership):
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_STAGING_INVALID",
			"Dữ liệu kết nối tạm đã thay đổi; hãy tải lại rồi thử tiếp", nil)
	case errors.Is(err, store.ErrOnboardingModelUnavailable):
		a.writeLLMErr(w, http.StatusUnprocessableEntity, "ONBOARDING_NO_MODEL",
			"Không có model khả dụng cho nhà cung cấp đã kết nối", nil)
	case errors.Is(err, store.ErrOnboardingPersonaMismatch):
		a.writeLLMErr(w, http.StatusConflict, "ONBOARDING_PERSONA_CHANGED",
			"Tên hoặc văn phong của bot đã thay đổi; hãy xác nhận lại trước khi kiểm tra", nil)
	default:
		a.writeOnboardingStateUnavailable(w, err)
	}
}

func (a *api) writeOnboardingStateUnavailable(w http.ResponseWriter, cause error) {
	if a.logger != nil {
		a.logger.Error("onboarding api: state unavailable", "err", cause)
	}
	a.writeLLMErr(w, http.StatusInternalServerError, "ONBOARDING_STATE_UNAVAILABLE",
		"Không đọc hoặc cập nhật được trạng thái thiết lập", nil)
}

func (a *api) writeOnboardingCleanupFailed(w http.ResponseWriter, cause error) {
	if a.logger != nil {
		a.logger.Error("onboarding api: cleanup failed", "err", cause)
	}
	a.writeLLMErr(w, http.StatusInternalServerError, "ONBOARDING_CLEANUP_FAILED",
		"Chưa dọn xong dữ liệu kết nối tạm; hãy thử lại", nil)
}

// removeOwnedOnboardingAccount removes only the exact account directory described by Store. The
// deletion is relative to an open os.Root for the resolved data directory, so a concurrent rename
// cannot redirect it to another pathname. Every owned path component must be a real directory;
// links and Windows junctions are rejected even when they resolve inside the data directory.
func removeOwnedOnboardingAccount(dataDir string, owned store.OnboardingStagingAccount) error {
	if owned == (store.OnboardingStagingAccount{}) {
		return nil
	}
	if !store.IsOnboardingProviderKind(owned.ProviderKind) ||
		!validOnboardingAccountID(owned.AccountID) || owned.ConfigDir == "" {
		return errOnboardingUnsafeAccountPath
	}
	relativeTarget, err := onboardingAccountRelativePath(owned.ProviderKind, owned.AccountID)
	if err != nil {
		return err
	}

	accountsRoot, err := filepath.Abs(filepath.Join(dataDir, "accounts"))
	if err != nil {
		return fmt.Errorf("%w: resolve accounts root", errOnboardingUnsafeAccountPath)
	}
	kindRoot, err := filepath.Abs(filepath.Join(accountsRoot, owned.ProviderKind))
	if err != nil {
		return fmt.Errorf("%w: resolve provider root", errOnboardingUnsafeAccountPath)
	}
	expected, err := filepath.Abs(accountConfigDir(dataDir, owned.ProviderKind, owned.AccountID))
	if err != nil {
		return fmt.Errorf("%w: resolve expected account", errOnboardingUnsafeAccountPath)
	}
	actual, err := filepath.Abs(owned.ConfigDir)
	if err != nil || !sameOnboardingPath(actual, expected) {
		return fmt.Errorf("%w: config directory mismatch", errOnboardingUnsafeAccountPath)
	}
	if sameOnboardingPath(actual, accountsRoot) || sameOnboardingPath(actual, kindRoot) ||
		!onboardingPathIsChild(accountsRoot, actual) {
		return fmt.Errorf("%w: account target is too broad", errOnboardingUnsafeAccountPath)
	}

	resolvedDataDir, err := resolveOnboardingPath(dataDir)
	if err != nil {
		return fmt.Errorf("resolve onboarding data directory: %w", err)
	}
	root, err := os.OpenRoot(resolvedDataDir)
	if err != nil {
		return fmt.Errorf("open onboarding data directory: %w", err)
	}
	defer root.Close()

	components := []string{
		"accounts",
		filepath.Join("accounts", owned.ProviderKind),
		relativeTarget,
	}
	for _, component := range components {
		exists, inspectErr := inspectOnboardingRootDirectory(root, component)
		if inspectErr != nil {
			return inspectErr
		}
		if !exists {
			return nil
		}
	}
	if err := removeAllOnboardingAccountRooted(root, relativeTarget); err != nil {
		return fmt.Errorf("remove onboarding account target: %w", err)
	}
	if _, err := root.Lstat(relativeTarget); !os.IsNotExist(err) {
		if err == nil {
			err = errors.New("target still exists")
		}
		return fmt.Errorf("verify onboarding account removal: %w", err)
	}
	return nil
}

func validOnboardingAccountID(accountID string) bool {
	if accountID == "" || accountID == "." || accountID == ".." ||
		filepath.Clean(accountID) != accountID || filepath.Base(accountID) != accountID ||
		filepath.VolumeName(accountID) != "" || strings.ContainsAny(accountID, `/\`) ||
		strings.ContainsRune(accountID, 0) || strings.Contains(accountID, ":") ||
		strings.TrimRight(accountID, " .") != accountID {
		return false
	}

	deviceBase := strings.ToUpper(strings.SplitN(accountID, ".", 2)[0])
	switch deviceBase {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return false
	}
	if len(deviceBase) == 4 {
		prefix, digit := deviceBase[:3], deviceBase[3]
		if (prefix == "COM" || prefix == "LPT") && digit >= '1' && digit <= '9' {
			return false
		}
	}
	return true
}

func onboardingAccountRelativePath(kind, accountID string) (string, error) {
	relative := filepath.Join("accounts", kind, accountID)
	components := strings.Split(relative, string(filepath.Separator))
	if filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" || len(components) != 3 ||
		components[0] != "accounts" || components[1] != kind || components[2] != accountID {
		return "", fmt.Errorf("%w: account path is not exact", errOnboardingUnsafeAccountPath)
	}
	return relative, nil
}

func inspectOnboardingRootDirectory(root *os.Root, name string) (bool, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect onboarding account path %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: account path component is a link", errOnboardingUnsafeAccountPath)
	}
	// Windows junctions are reparse points but are not consistently reported as ModeSymlink.
	// Root.Readlink recognizes both junctions and ordinary symbolic links.
	if _, err := root.Readlink(name); err == nil {
		return false, fmt.Errorf("%w: account path component is a link", errOnboardingUnsafeAccountPath)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%w: account path component is not a directory", errOnboardingUnsafeAccountPath)
	}
	return true, nil
}

func sameOnboardingPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func onboardingPathIsChild(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// resolveOnboardingPath resolves each existing path component before visiting the next one, then
// appends any missing suffix. Component-wise resolution is required on Windows: EvalSymlinks can
// resolve a junction itself but may fail when that junction appears in the middle of a longer path.
// It also catches an existing intermediate link when the final account directory has already gone.
func resolveOnboardingPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	volume := filepath.VolumeName(absolute)
	root := volume + string(filepath.Separator)
	if volume == "" {
		root = string(filepath.Separator)
	}
	remainder := strings.TrimPrefix(absolute, root)
	components := strings.Split(remainder, string(filepath.Separator))
	resolved := root
	for index, component := range components {
		if component == "" {
			continue
		}
		candidate := filepath.Join(resolved, component)
		if _, err := os.Lstat(candidate); err == nil {
			// Windows directory junctions are reparse points but Lstat does not always expose
			// ModeSymlink for them, so filepath.EvalSymlinks may leave the junction untouched.
			// Readlink understands both ordinary symlinks and junctions; try it explicitly.
			if linkTarget, readErr := os.Readlink(candidate); readErr == nil {
				linkTarget = normalizeOnboardingLinkTarget(linkTarget)
				if !filepath.IsAbs(linkTarget) {
					linkTarget = filepath.Join(filepath.Dir(candidate), linkTarget)
				}
				resolved, err = filepath.Abs(linkTarget)
			} else {
				resolved, err = filepath.EvalSymlinks(candidate)
			}
			if err != nil {
				return "", err
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", err
			}
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return filepath.Abs(filepath.Join(append([]string{resolved}, components[index:]...)...))
	}
	return filepath.Abs(resolved)
}

func normalizeOnboardingLinkTarget(target string) string {
	if runtime.GOOS != "windows" {
		return target
	}
	switch {
	case strings.HasPrefix(target, `\??\UNC\`):
		return `\\` + strings.TrimPrefix(target, `\??\UNC\`)
	case strings.HasPrefix(target, `\??\`):
		return strings.TrimPrefix(target, `\??\`)
	case strings.HasPrefix(target, `\\?\UNC\`):
		return `\\` + strings.TrimPrefix(target, `\\?\UNC\`)
	case strings.HasPrefix(target, `\\?\`):
		return strings.TrimPrefix(target, `\\?\`)
	default:
		return target
	}
}
