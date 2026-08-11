import { createProviderConnect } from "../components/provider-connect.js";
import { element } from "../core/ui.js";
import { createProviderService } from "./providers.js";
import {
  SAFE_ERROR, SAFE_SETUP_ERROR, SUPPORTED_PROVIDERS, createOnboardingService, isRecord,
  normalizeAgentResponse, normalizeCompleteResponse, normalizeConnectedSetup,
  normalizeProviderSelection, normalizeSetupResult, normalizeStatus, normalizeTestMessage,
  normalizeTestResponse, positiveRevision, safeServerFieldCount, semanticString, successorRevision,
  terminalIdentity, testChatRecovery, testResponseError, validDisplayName, validateConnectController,
  validateOnboardingPageService,
} from "./onboarding-contract.js";
import { createDoneStage, createPersonaStage, createTestStage } from "./onboarding-late-view.js";
import {
  createConnectStage, createLoadingStatus, createRetryButton, createSafeErrorStage,
  createSetupStage, createWelcomeStage, renderOnboardingShell,
} from "./onboarding-early-view.js";
export { createOnboardingService };
export function createOnboardingPage({
  initialStatus, service = createOnboardingService(), connectService = createProviderService(),
  connectFactory = createProviderConnect, onComplete = () => {}, setupSuccessDelayMs = 0,
  setTimeoutFn = setTimeout, clearTimeoutFn = clearTimeout, nowFn = Date.now,
  connectOptions = {},
} = {}) {
  validateOnboardingPageService(service);
  if (typeof connectFactory !== "function") {
    throw new TypeError("Onboarding page requires a connect factory");
  }
  if (typeof onComplete !== "function") throw new TypeError("onComplete must be a function");
  if (typeof nowFn !== "function") throw new TypeError("nowFn must be a function");
  let state = normalizeStatus(initialStatus);
  let selectedProvider = state?.phase === "provider"
    ? state.provider_kind || state.suggested_provider_kind
    : "";
  let host = null, root = null, activeController = null, personaTimer = null;
  let connectController = null, connectBackPromise = null, connectTerminalPromise = null;
  let setupInFlight = null, personaView = null, testView = null, doneView = null;
  let disposed = false, generation = 0, connectKey = "", agentName = "";
  let testMessage = "Xin chào", testResult = null, testError = "", testToken = "", testExpiresAt = 0, receiptTimer = null;
  let personaBusy = false, testBusy = false, completeBusy = false, handoffDone = false;
  const listeners = new Set();
  function listen(node, type, listener) {
    node.addEventListener(type, listener);
    listeners.add({ node, type, listener });
    return node;
  }
  function clearListeners() {
    for (const binding of listeners) {
      binding.node.removeEventListener(binding.type, binding.listener);
    }
    listeners.clear();
  }
  function clearPersonaTimer() {
    if (personaTimer === null) return;
    clearTimeoutFn(personaTimer);
    personaTimer = null;
  }
  function disposePersonaView() {
    personaView?.dispose();
    personaView = null;
  }
  function clearReceipt({ clearResult = true } = {}) {
    testToken = "";
    testExpiresAt = 0;
    if (receiptTimer !== null) {
      clearTimeoutFn(receiptTimer);
      receiptTimer = null;
    }
    if (clearResult) testResult = null;
  }
  function abortActive() {
    activeController?.abort();
    activeController = null;
  }
  function invalidate() {
    generation++;
    abortActive();
    clearPersonaTimer();
    return generation;
  }
  function beginOperation() {
    const run = invalidate();
    const controller = new AbortController();
    activeController = controller;
    return { run, controller };
  }
  function owns(run) {
    return !disposed && root && generation === run;
  }
  function releaseOperation(run, controller) {
    if (generation === run && activeController === controller) activeController = null;
  }
  function disposeConnect() {
    const instance = connectController;
    connectController = null;
    connectKey = "";
    connectBackPromise = null;
    if (!instance) return;
    try {
      instance.dispose();
    } catch {
      // A broken child controller must not retain the onboarding host.
    }
  }
  function shell(phase, content) {
    renderOnboardingShell(root, phase, content);
  }
  function renderSafeError(message = SAFE_ERROR, retry = refreshStatus) {
    disposePersonaView();
    disposeConnect();
    clearListeners();
    shell(state?.phase || "provider", createSafeErrorStage(
      message, createRetryButton(listen, retry),
    ));
  }
  function renderLoading() {
    disposePersonaView();
    connectTerminalPromise = null;
    disposeConnect();
    clearListeners();
    shell(state?.phase || "provider", createLoadingStatus("Đang tải lại trạng thái…"));
  }
  function renderWelcome(message = "") {
    disposePersonaView();
    connectTerminalPromise = null;
    disposeConnect();
    clearListeners();
    shell("provider", createWelcomeStage({
      selectedProvider,
      message,
      listen,
      onSelect(kind) {
        selectedProvider = kind;
        renderWelcome(message);
      },
      onProceed: () => { void selectProvider(); },
      onRetry: refreshStatus,
    }));
  }
  async function selectProvider() {
    if (!state || !selectedProvider || !SUPPORTED_PROVIDERS.has(selectedProvider)) return false;
    clearReceipt();
    agentName = "";
    if (!successorRevision(state.revision)) {
      renderSafeError();
      return false;
    }
    const requested = { providerKind: selectedProvider, revision: state.revision };
    const { run, controller } = beginOperation();
    try {
      const response = await service.selectProvider(
        selectedProvider,
        state.revision,
        controller.signal,
      );
      if (!owns(run)) return false;
      const nextState = normalizeProviderSelection(response, requested);
      if (!nextState) {
        renderSafeError();
        return false;
      }
      state = nextState;
      selectedProvider = nextState.provider_kind || nextState.suggested_provider_kind;
      renderCurrent();
      return true;
    } catch (error) {
      if (!owns(run) || error?.name === "AbortError") return false;
      renderWelcome(SAFE_ERROR);
      return false;
    } finally {
      releaseOperation(run, controller);
    }
  }
  async function reconcileConnected(stageRun, connectRevision, terminal) {
    if (!owns(stageRun)) return false;
    if (!successorRevision(connectRevision)) {
      renderSafeError();
      return false;
    }
    const { run, controller } = beginOperation();
    try {
      const response = await service.status(controller.signal);
      if (!owns(run)) return false;
      const nextState = normalizeConnectedSetup(response, {
        providerKind: terminal.kind,
        accountID: terminal.accountID,
        revision: connectRevision,
      });
      if (!nextState) {
        renderSafeError();
        return false;
      }
      state = nextState;
      return startSetup(nextState);
    } catch (error) {
      if (!owns(run) || error?.name === "AbortError") return false;
      renderSafeError();
      return false;
    } finally {
      releaseOperation(run, controller);
    }
  }
  function handleConnected(stageRun, expectedKind, connectRevision, terminal) {
    if (connectTerminalPromise) return connectTerminalPromise;
    if (!owns(stageRun)) return Promise.resolve(false);
    const identity = terminalIdentity(terminal, expectedKind);
    if (!identity) {
      renderSafeError();
      return Promise.resolve(false);
    }
    connectTerminalPromise = reconcileConnected(stageRun, connectRevision, identity);
    return connectTerminalPromise;
  }
  function goBackFromConnect(stageRun) {
    if (connectBackPromise) return connectBackPromise;
    if (!owns(stageRun)) return Promise.resolve(false);
    clearReceipt();
    agentName = "";
    const run = invalidate();
    const instance = connectController;
    connectBackPromise = (async () => {
      try {
        await Promise.resolve(instance?.cancel());
      } catch {
        // The page still releases an unhealthy child controller and returns to Welcome.
      }
      try {
        instance?.dispose();
      } catch {
        // The owned host is cleared below even if the child cleanup throws.
      }
      if (connectController === instance) {
        connectController = null;
        connectKey = "";
        connectTerminalPromise = null;
      }
      connectBackPromise = null;
      if (!owns(run)) return false;
      state = { ...state, phase: "provider" };
      selectedProvider = SUPPORTED_PROVIDERS.has(state.provider_kind)
        ? state.provider_kind
        : "";
      renderWelcome();
      return true;
    })();
    return connectBackPromise;
  }
  function renderConnect() {
    disposePersonaView();
    clearListeners();
    const kind = state.provider_kind;
    const key = `${kind}\u0000${state.revision}`;
    const slot = element("div", { className: "onboarding-connect-slot" });
    shell("connect", createConnectStage(kind, slot));
    if (connectController && connectKey === key) {
      connectController.mount(slot);
      return;
    }
    connectTerminalPromise = null;
    disposeConnect();
    const stageRun = generation;
    try {
      const instance = connectFactory({
        ...connectOptions,
        kind,
        service: connectService,
        onConnected: (terminal) => handleConnected(stageRun, kind, state.revision, terminal),
        onBack: () => goBackFromConnect(stageRun),
      });
      validateConnectController(instance);
      connectController = instance;
      connectKey = key;
      instance.mount(slot);
      if (instance.start({ label: "Onboarding", onboardingRevision: state.revision }) === false) {
        throw new Error("Provider Connect refused to start");
      }
    } catch {
      disposeConnect();
      renderSafeError();
    }
  }
  function renderSetup(status, message = "") {
    disposePersonaView();
    disposeConnect();
    clearListeners();
    const retry = status === "error" ? createRetryButton(listen, refreshStatus) : null;
    shell("setup", createSetupStage(status, message || SAFE_SETUP_ERROR, retry));
  }
  function startSetup(snapshot) {
    const accountID = semanticString(snapshot?.account_id);
    const revision = positiveRevision(snapshot?.revision);
    const providerKind = semanticString(snapshot?.provider_kind);
    const providerID = semanticString(snapshot?.provider_id);
    if (!accountID || !revision || !SUPPORTED_PROVIDERS.has(providerKind)
      || providerID !== providerKind) {
      renderSafeError();
      return Promise.resolve(false);
    }
    if (!successorRevision(revision)) {
      renderSafeError();
      return Promise.resolve(false);
    }
    const key = `${accountID}\u0000${revision}`;
    if (setupInFlight?.key === key) return setupInFlight.promise;
    const { run, controller } = beginOperation();
    renderSetup("loading");
    const promise = (async () => {
      try {
        const response = await service.setup(accountID, revision, controller.signal);
        if (!owns(run)) return false;
        const nextState = normalizeSetupResult(response, { accountID, revision, providerKind, providerID, lifecycle: snapshot });
        if (!nextState) {
          setupInFlight = null;
          renderSetup("error", SAFE_SETUP_ERROR);
          return false;
        }
        state = nextState;
        renderSetup("complete");
        personaTimer = setTimeoutFn(() => {
          personaTimer = null;
          if (!owns(run)) return;
          setupInFlight = null;
          renderCurrent();
        }, Math.max(0, Number(setupSuccessDelayMs) || 0));
        return true;
      } catch (error) {
        if (!owns(run) || error?.name === "AbortError") return false;
        setupInFlight = null;
        renderSetup("error", SAFE_SETUP_ERROR);
        return false;
      } finally {
        releaseOperation(run, controller);
      }
    })();
    setupInFlight = { key, promise };
    return promise;
  }
  function renderStageLoading(phase, message) {
    disposePersonaView();
    disposeConnect();
    clearListeners();
    shell(phase, createLoadingStatus(message));
  }
  function mountPersona(agent) {
    disposePersonaView();
    disposeConnect();
    clearListeners();
    personaBusy = false;
    personaView = createPersonaStage({
      agent,
      listen,
      onChange(result) {
        clearReceipt();
        testError = "";
        personaBusy = false;
        invalidate();
        personaView?.setValidation(result);
      },
      onSubmit: savePersona,
      onBack: backFromPersona,
    });
    shell("persona", personaView.content);
  }
  async function loadAgent(phase, loadingMessage, consume, fail) {
    const { run, controller: requestController } = beginOperation();
    renderStageLoading(phase, loadingMessage);
    try {
      const response = await service.loadAgent({ signal: requestController.signal });
      if (!owns(run)) return false;
      const authoritative = normalizeAgentResponse(response);
      if (!authoritative || consume(authoritative) === false) return fail(false);
      return true;
    } catch (error) {
      if (!owns(run) || requestController.signal.aborted) return false;
      return fail(true);
    } finally {
      releaseOperation(run, requestController);
    }
  }
  function renderPersona() {
    clearReceipt();
    testError = "";
    return loadAgent("persona", "Đang tải Persona…", (authoritative) => {
      agentName = validDisplayName(authoritative.display_name);
      mountPersona(authoritative);
    }, () => { renderSafeError(); return false; });
  }
  async function savePersona(result) {
    clearReceipt();
    if (personaBusy || !personaView) return false;
    const current = result ?? personaView.fields.validate();
    if (!current.ok) {
      personaView.setValidation(current);
      personaView.fields.focusFirstError(current);
      return false;
    }
    const expected = { ...state };
    if (!["persona", "test"].includes(expected?.phase) || !successorRevision(expected.revision)) {
      renderSafeError();
      return false;
    }
    const payload = {
      values: current.values,
      display_name: current.displayName,
      require_complete: true,
      onboarding_revision: expected.revision,
    };
    personaBusy = true;
    personaView.setBusy(true);
    const { run, controller: requestController } = beginOperation();
    try {
      const response = await service.saveAgent(payload, { signal: requestController.signal });
      if (!owns(run)) return false;
      const name = validDisplayName(response?.display_name);
      if (!isRecord(response) || response.ready !== true || name !== payload.display_name
        || !Array.isArray(response.placeholders) || response.placeholders.length !== 0 || response.onboarding_phase !== "test"
        || response.onboarding_revision !== successorRevision(expected.revision)) {
        personaBusy = false;
        renderSafeError();
        return false;
      }
      state = { ...expected, phase: "test", revision: response.onboarding_revision };
      agentName = name;
      testMessage = "Xin chào";
      personaBusy = false;
      renderTest();
      return true;
    } catch (error) {
      if (!owns(run) || requestController.signal.aborted) return false;
      personaBusy = false;
      if (["ONBOARDING_REVISION_CONFLICT", "ONBOARDING_CONFIGURATION_CHANGED"].includes(error?.code)) {
        return refreshStatus();
      }
      const remaining = safeServerFieldCount(error);
      personaView?.setBusy(false);
      personaView?.setError(
        remaining ? "Persona vẫn còn mục cần điền. Vui lòng kiểm tra lại." : "Chưa thể lưu Persona. Vui lòng thử lại.",
        remaining,
      );
      if (!personaView?.fields.focusFirstError()) personaView?.fields.focusFirst();
      return false;
    } finally {
      releaseOperation(run, requestController);
    }
  }
  function backFromPersona() {
    clearReceipt();
    agentName = "";
    personaBusy = false;
    invalidate();
    disposePersonaView();
    state = { ...state, phase: "provider" };
    selectedProvider = SUPPORTED_PROVIDERS.has(state.provider_kind) ? state.provider_kind : "";
    renderWelcome();
  }
  function scheduleReceiptExpiry() {
    if (!testToken || !testExpiresAt) return;
    const delay = Math.min(Math.max(0, testExpiresAt - nowFn()), 2_147_483_647);
    receiptTimer = setTimeoutFn(() => {
      receiptTimer = null;
      if (disposed || !testToken) return;
      if (nowFn() < testExpiresAt) {
        scheduleReceiptExpiry();
        return;
      }
      clearReceipt();
      testError = "Kết quả thử đã hết hạn. Vui lòng thử lại.";
      renderTest();
    }, delay);
  }
  function renderTestUnavailable(message) {
    disposePersonaView();
    disposeConnect();
    clearListeners();
    testView = createTestStage({
      displayName: "trợ lý",
      message: testMessage,
      result: null,
      errorMessage: message,
      busy: false,
      canComplete: false,
      listen,
      onInput: (value) => { testMessage = value; },
      onSend: () => { void loadTestAgent(); },
      onRetry: () => { void loadTestAgent(); },
      onBack: () => { void backFromTest(); },
      onComplete: () => {},
    });
    shell("test", testView.content);
  }
  async function loadTestAgent() {
    clearReceipt();
    return loadAgent("test", "Đang tải tên trợ lý…", (authoritative) => {
      const name = validDisplayName(authoritative?.display_name);
      if (authoritative.ready !== true || authoritative.placeholders.length !== 0 || !name) {
        agentName = "";
        return false;
      }
      agentName = name;
      renderTest();
    }, (requestFailed) => {
      renderTestUnavailable(requestFailed
        ? "Chưa thể tải tên trợ lý. Vui lòng thử lại."
        : "Chưa thể xác minh tên trợ lý. Vui lòng thử lại hoặc chỉnh Persona.");
      return false;
    });
  }
  function renderTest() {
    if (!agentName) {
      void loadTestAgent();
      return;
    }
    if (testToken && nowFn() >= testExpiresAt) {
      clearReceipt();
      testError = "Kết quả thử đã hết hạn. Vui lòng thử lại.";
    }
    disposePersonaView();
    disposeConnect();
    clearListeners();
    testView = createTestStage({
      displayName: agentName,
      message: testMessage,
      result: testResult,
      errorMessage: testError,
      busy: testBusy || completeBusy,
      canComplete: Boolean(testToken),
      listen,
      onInput(value) {
        testMessage = value;
        if (testToken || testResult || testBusy || completeBusy) {
          invalidate();
          clearReceipt();
          testError = "";
          testBusy = false; completeBusy = false;
          renderTest();
        }
      },
      onSend: (value, field) => { void startTest(value, field); },
      onRetry: () => { void startTest(testMessage); },
      onBack: () => { void backFromTest(); },
      onComplete: () => { void completeOnboarding(); },
    });
    shell("test", testView.content);
  }
  async function startTest(rawMessage, focusTarget = null) {
    if (testBusy || completeBusy || !agentName) return false;
    const message = normalizeTestMessage(rawMessage);
    testMessage = rawMessage;
    if (!message) {
      clearReceipt();
      testError = "Tin nhắn cần từ 1 đến 500 ký tự hợp lệ.";
      testView?.showError(testError);
      focusTarget?.focus?.();
      return false;
    }
    const expected = { ...state };
    if (expected.phase !== "test" || !successorRevision(expected.revision)) {
      renderSafeError();
      return false;
    }
    clearReceipt();
    testResult = null;
    testError = "";
    testBusy = true;
    testView?.setBusy(true);
    testView?.showError("");
    const { run, controller: requestController } = beginOperation();
    try {
      const response = await service.testChat(message, expected.revision, {
        signal: requestController.signal,
      });
      if (!owns(run)) return false;
      const result = normalizeTestResponse(response, {
        displayName: agentName,
        snapshot: expected,
        now: nowFn(),
      });
      if (!result) {
        testBusy = false;
        testError = testResponseError(response, agentName, nowFn());
        renderTest();
        return false;
      }
      state = { ...expected, revision: result.revision };
      testMessage = message;
      testResult = { message, answer: result.answer };
      testToken = result.token;
      testExpiresAt = result.expiresAt;
      testBusy = false;
      scheduleReceiptExpiry();
      renderTest();
      return true;
    } catch (error) {
      if (!owns(run) || requestController.signal.aborted) return false;
      testBusy = false;
      clearReceipt();
      const recovery = testChatRecovery(error?.code);
      if (recovery === "refresh") return refreshStatus();
      if (recovery === "provider") return refreshStatus({ fallbackToProvider: true });
      if (recovery === "persona") { agentName = ""; return renderPersona(); }
      testError = error?.code === "ONBOARDING_PERSONA_NOT_APPLIED" ? "Bot đã phản hồi nhưng chưa áp dụng đúng Persona." : "Chưa thể trò chuyện thử. Vui lòng thử lại.";
      renderTest();
      return false;
    } finally {
      releaseOperation(run, requestController);
    }
  }
  async function backFromTest() {
    clearReceipt();
    testBusy = false;
    completeBusy = false;
    invalidate();
    return renderPersona();
  }
  async function completeOnboarding() {
    if (completeBusy || testBusy || !testToken) return false;
    if (nowFn() >= testExpiresAt) {
      clearReceipt();
      testError = "Kết quả thử đã hết hạn. Vui lòng thử lại.";
      renderTest();
      return false;
    }
    const expected = { ...state };
    const receipt = testToken;
    completeBusy = true;
    testView?.setBusy(true);
    const { run, controller: requestController } = beginOperation();
    try {
      const response = await service.complete(receipt, expected.revision, {
        signal: requestController.signal,
      });
      if (!owns(run)) return false;
      if (!normalizeCompleteResponse(response)) {
        completeBusy = false;
        testError = "Chưa thể hoàn tất an toàn. Vui lòng thử lại.";
        renderTest();
        return false;
      }
      clearReceipt();
      completeBusy = false;
      state = { ...expected, required: false, completed_version: expected.current_version,
        restart_in_progress: false, phase: "completed" };
      renderDone();
      return true;
    } catch (error) {
      if (!owns(run) || requestController.signal.aborted) return false;
      completeBusy = false;
      if (["ONBOARDING_TEST_EXPIRED", "ONBOARDING_TEST_REQUIRED"].includes(error?.code)) {
        clearReceipt();
        testError = "Kết quả thử không còn hợp lệ. Vui lòng thử lại.";
        renderTest();
        return false;
      }
      if (["ONBOARDING_REVISION_CONFLICT", "ONBOARDING_CONFIGURATION_CHANGED"].includes(error?.code)) {
        clearReceipt();
        agentName = "";
        return refreshStatus();
      }
      testError = "Chưa thể hoàn tất an toàn. Cấu hình cũ vẫn được giữ nguyên.";
      renderTest();
      return false;
    } finally {
      releaseOperation(run, requestController);
    }
  }
  function handoff(destination) {
    if (disposed || handoffDone || state?.phase !== "completed") return;
    handoffDone = true;
    doneView?.disable();
    try {
      Promise.resolve(onComplete({ destination })).catch(() => {});
    } catch {
      // A host callback cannot revive or duplicate a completed onboarding handoff.
    }
  }
  function renderDone() {
    if (!agentName) {
      void loadCompletedAgent();
      return;
    }
    clearReceipt();
    disposePersonaView();
    disposeConnect();
    clearListeners();
    doneView = createDoneStage({
      displayName: agentName,
      listen,
      onPortal: () => handoff("portal"),
      onKnowledge: () => handoff("knowledge"),
    });
    shell("completed", doneView.content);
  }
  async function loadCompletedAgent() {
    clearReceipt();
    return loadAgent("completed", "Đang tải tên trợ lý…", (authoritative) => {
      const name = validDisplayName(authoritative?.display_name);
      if (!name) return false;
      agentName = name;
      renderDone();
    }, () => { renderSafeError(); return false; });
  }
  function renderCurrent() {
    if (!root || disposed) return;
    if (!state) {
      renderSafeError();
      return;
    }
    switch (state.phase) {
      case "provider": renderWelcome(); break;
      case "connect": renderConnect(); break;
      case "setup": void startSetup(state); break;
      case "persona": void renderPersona(); break;
      case "test": renderTest(); break;
      case "completed": renderDone(); break;
      default: renderSafeError();
    }
  }
  function renderProviderRecovery(snapshot) {
    state = { ...snapshot, phase: "provider" };
    selectedProvider = SUPPORTED_PROVIDERS.has(state.provider_kind) ? state.provider_kind : "";
    renderWelcome("Cấu hình kết nối không còn hợp lệ. Vui lòng chọn lại nhà cung cấp.");
    return false;
  }
  async function refreshStatus({ fallbackToProvider = false } = {}) {
    clearReceipt();
    agentName = ""; testBusy = false; completeBusy = false;
    const fallbackState = state;
    const { run, controller } = beginOperation();
    renderLoading();
    try {
      const response = await service.status(controller.signal);
      if (!owns(run)) return false;
      const nextState = normalizeStatus(response);
      if (!nextState) {
        if (fallbackToProvider) return renderProviderRecovery(fallbackState);
        state = null;
        renderSafeError();
        return false;
      }
      state = nextState;
      if (fallbackToProvider && nextState.phase === "test") return renderProviderRecovery(nextState);
      selectedProvider = nextState.phase === "provider" ? nextState.provider_kind || nextState.suggested_provider_kind : "";
      renderCurrent();
      return true;
    } catch (error) {
      if (!owns(run) || error?.name === "AbortError") return false;
      if (fallbackToProvider) return renderProviderRecovery(fallbackState);
      renderSafeError();
      return false;
    } finally {
      releaseOperation(run, controller);
    }
  }
  function mount(container) {
    if (!container || typeof container.replaceChildren !== "function") throw new TypeError("Onboarding page mount requires a host");
    if (disposed) return controller;
    const hadReceipt = Boolean(testToken);
    const resetTest = state?.phase === "test" && (hadReceipt || testBusy || completeBusy);
    clearReceipt();
    if (resetTest) {
      invalidate();
      completeBusy = false; testBusy = false;
    }
    if (host === container && root?.parentNode === container) {
      if (resetTest) renderTest();
      return controller;
    }
    if (root && host && host !== container) {
      root.remove();
      host = container;
      host.replaceChildren(root);
      if (resetTest) renderTest();
      return controller;
    }
    host = container;
    root = element("div", { className: "onboarding-page" });
    host.replaceChildren(root);
    renderCurrent();
    return controller;
  }
  function dispose() {
    if (disposed) return;
    disposed = true;
    clearReceipt();
    agentName = "";
    personaBusy = false;
    testBusy = false;
    completeBusy = false;
    invalidate();
    clearListeners();
    disposePersonaView();
    disposeConnect();
    connectTerminalPromise = null;
    setupInFlight = null;
    root?.remove();
    root = null;
    host = null;
  }
  const controller = Object.freeze({ mount, dispose });
  return controller;
}
