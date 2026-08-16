import { createProviderConnect } from "../components/provider-connect.js";
import { createProviderOffDialog } from "../components/provider-off-dialog.js";
import { element } from "../core/ui.js";
import { createProviderService } from "./providers.js";
import {
  SAFE_ERROR, SAFE_SETUP_ERROR, createOnboardingService,
  normalizeAgentResponse, normalizeConnectedSetup,
  normalizeSetupResult, normalizeStatus, normalizeStrictStatus, positiveRevision, semanticString, successorRevision,
  terminalIdentity, validDisplayName, validateConnectController,
  validateOnboardingPageService,
} from "./onboarding-contract.js";
import { runOnboardingBootstrap } from "./onboarding-bootstrap.js";
import { cancelConnectAndReadStatus } from "./onboarding-provider-selection.js";
import { beginProviderInstallWithReconciliation, persistProviderSetWithReconciliation } from "./onboarding-provider-flow.js";
import { createBootstrapStage, createDoneStage } from "./onboarding-late-view.js";
import { createConnectStage, createLoadingStatus, createRetryButton, createSafeErrorStage,
  createSetupStage, createWelcomeStage, focusProviderControl, renderOnboardingShell } from "./onboarding-early-view.js";
export { createOnboardingService };
const CONNECT_STOPPED = "Kết nối đã dừng. Bấm Thử lại để tải trạng thái và tiếp tục.";
const SAFE_BOOTSTRAP_ERROR = "Chưa thể chuẩn bị trợ lý. Cấu hình cũ vẫn được giữ nguyên.";
export function createOnboardingPage({
  initialStatus, service = createOnboardingService(), connectService = createProviderService(),
  connectFactory = createProviderConnect, onComplete = () => {},
  connectOptions = {},
} = {}) {
  validateOnboardingPageService(service);
  if (typeof connectFactory !== "function") {
    throw new TypeError("Onboarding page requires a connect factory");
  }
  if (typeof onComplete !== "function") throw new TypeError("onComplete must be a function");
  let state = normalizeStatus(initialStatus);
  let host = null, root = null, activeController = null;
  let connectController = null, connectBackPromise = null, connectTerminalPromise = null;
  let setupInFlight = null, doneView = null;
  let disposed = false, generation = 0, connectKey = "", agentName = "";
  let providerBusy = false, bootstrapBusy = false, bootstrapAttempted = false, handoffDone = false;
  let providerOffDialog = null;
  const listeners = new Set();
  function listen(node, type, listener) {
    node.addEventListener(type, listener); listeners.add({ node, type, listener });
    return node;
  }
  function clearListeners() {
    for (const binding of listeners) binding.node.removeEventListener(binding.type, binding.listener);
    listeners.clear();
  }
  function providerOffController() {
    if (providerOffDialog) return providerOffDialog;
    const dialogListen = (node, type, listener) => {
      node.addEventListener(type, listener);
      return node;
    };
    providerOffDialog = createProviderOffDialog({
      listen: dialogListen,
      onConfirm: ({ kind }, signal) => updateProviderSet(kind, false, { preserveView: true, signal }),
    });
    return providerOffDialog;
  }
  function abortActive() { activeController?.abort(); activeController = null; }
  function invalidate() {
    generation++;
    abortActive();
    return generation;
  }
  function beginOperation() {
    const run = invalidate();
    const controller = new AbortController();
    activeController = controller;
    return { run, controller };
  }
  function owns(run) { return !disposed && root && generation === run; }
  function releaseOperation(run, controller) {
    if (generation === run && activeController === controller) activeController = null;
  }
  function disposeConnect() {
    const instance = connectController;
    connectController = null; connectKey = ""; connectBackPromise = null;
    if (!instance) return;
    try {
      instance.dispose();
    } catch {
      // A broken child controller must not retain the onboarding host.
    }
  }
  function shell(phase, content) { renderOnboardingShell(root, phase, content); }
  function renderSafeError(message = SAFE_ERROR, retry = refreshStatus) {
    disposeConnect();
    clearListeners();
    shell(state?.phase || "provider", createSafeErrorStage(message, createRetryButton(listen, retry)));
  }
  function renderLoading() {
    connectTerminalPromise = null; disposeConnect(); clearListeners();
    shell(state?.phase || "provider", createLoadingStatus("Đang tải lại trạng thái…"));
  }
  function renderWelcome(message = "") {
    connectTerminalPromise = null; disposeConnect(); clearListeners();
    shell("provider", createWelcomeStage({
      state, message, busy: providerBusy, listen,
      onToggle: (kind, enabled) => { void updateProviderSet(kind, enabled); },
      onRequestOff: ({ kind, label, opener }) => providerOffController().open({
        label,
        value: { kind },
        opener,
        description: `Nếu tắt ${label} trước khi cài, lựa chọn này sẽ bị bỏ. Bạn vẫn muốn tắt chứ?`,
      }),
      onInstall: (kind) => { void beginProviderInstall(kind); },
      onRetry: refreshStatus,
    }));
  }
  async function updateProviderSet(kind, enabled, { preserveView = false, signal } = {}) {
    const snapshot = normalizeStatus(state);
    if (providerBusy || snapshot?.phase !== "provider") return false;
    const current = snapshot.providers.map((stage) => stage.kind);
    const stage = snapshot.providers.find((candidate) => candidate.kind === kind);
    if ((enabled && stage) || (!enabled && (!stage || stage.status === "ready"))) return false;
    const selectedKinds = enabled ? [...current, kind] : current.filter((selected) => selected !== kind);
    agentName = "";
    providerBusy = true;
    const { run, controller } = beginOperation();
    const abort = () => controller.abort();
    if (signal?.aborted) abort();
    else signal?.addEventListener("abort", abort, { once: true });
    if (!preserveView) renderWelcome();
    try {
      const result = await persistProviderSetWithReconciliation({
        state: snapshot, selectedKinds, signal: controller.signal,
        updateProviders: (revision, kinds, signal) => service.updateProviders(kinds, revision, signal),
        loadStatus: (signal) => service.status(signal),
      });
      if (!owns(run)) return false;
      state = result.state;
      providerBusy = false;
      renderCurrent();
      if (state.phase === "provider") focusProviderControl(root, kind);
      return result.kind === "updated";
    } catch (error) {
      if (!owns(run) || error?.name === "AbortError") return false;
      providerBusy = false;
      if (!preserveView) renderSafeError();
      return false;
    } finally {
      signal?.removeEventListener("abort", abort);
      releaseOperation(run, controller);
    }
  }
  async function beginProviderInstall(kind) {
    const snapshot = normalizeStatus(state);
    if (providerBusy || snapshot?.phase !== "provider"
      || snapshot.providers.find((stage) => stage.kind === kind)?.status !== "pending") return false;
    agentName = "";
    providerBusy = true;
    const { run, controller } = beginOperation();
    renderWelcome();
    try {
      const result = await beginProviderInstallWithReconciliation({
        state: snapshot, kind, signal: controller.signal,
        beginProvider: (revision, providerKind, signal) => service.beginProvider(providerKind, revision, signal),
        loadStatus: (signal) => service.status(signal),
      });
      if (!owns(run)) return false;
      state = result.state;
      providerBusy = false;
      if (result.kind === "moved" && state.phase === "provider"
        && state.revision === snapshot.revision) {
        renderWelcome(SAFE_ERROR);
        return false;
      }
      renderCurrent();
      return result.kind === "started";
    } catch (error) {
      if (!owns(run) || error?.name === "AbortError") return false;
      providerBusy = false;
      renderSafeError();
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
    agentName = "";
    const { run, controller } = beginOperation();
    const instance = connectController;
    connectBackPromise = (async () => {
      const nextState = await cancelConnectAndReadStatus({
        connect: instance, service, signal: controller.signal,
      });
      if (connectController === instance) {
        connectController = null;
        connectKey = "";
        connectTerminalPromise = null;
      }
      connectBackPromise = null;
      if (!owns(run)) return false;
      if (!nextState) { renderSafeError(); return false; }
      state = nextState;
      providerBusy = false;
      if (state.phase === "connect") { renderSafeError(CONNECT_STOPPED); return true; }
      renderCurrent();
      return true;
    })().finally(() => releaseOperation(run, controller));
    return connectBackPromise;
  }
  function renderConnect() {
    clearListeners();
    const kind = state.provider_kind;
    const key = `${kind}\u0000${state.revision}`;
    const slot = element("div", { className: "onboarding-connect-slot" });
    shell("connect", createConnectStage(state, slot));
    if (connectController && connectKey === key) {
      connectController.mount(slot);
      return;
    }
    connectTerminalPromise = null;
    disposeConnect();
    const stageRun = generation;
    try {
      const instance = connectFactory({
        ...connectOptions, kind, service: connectService,
        onConnected: (terminal) => handleConnected(stageRun, kind, state.revision, terminal),
        onBack: () => goBackFromConnect(stageRun),
      });
      validateConnectController(instance);
      connectController = instance;
      connectKey = key;
      instance.mount(slot);
      if (instance.start({
        label: "Onboarding", onboardingRevision: state.revision, immediate: true,
      }) === false) {
        throw new Error("Provider Connect refused to start");
      }
    } catch {
      disposeConnect();
      renderSafeError();
    }
  }
  function renderSetup(status, message = "") {
    disposeConnect(); clearListeners();
    const retry = status === "error" ? createRetryButton(listen, refreshStatus) : null;
    shell("setup", createSetupStage(state, status, message || SAFE_SETUP_ERROR, retry));
  }
  function startSetup(snapshot) {
    const accountID = semanticString(snapshot?.account_id), revision = positiveRevision(snapshot?.revision);
    const providerKind = semanticString(snapshot?.provider_kind), providerID = semanticString(snapshot?.provider_id);
    if (!accountID || !revision || !providerKind || providerID !== providerKind
      || !snapshot.provider_options?.some((option) => option.kind === providerKind)) {
      renderSafeError();
      return Promise.resolve(false);
    }
    if (!successorRevision(revision)) {
      renderSafeError();
      return Promise.resolve(false);
    }
    const key = `${providerKind}\u0000${accountID}\u0000${revision}`;
    if (setupInFlight?.key === key) return setupInFlight.promise;
    const { run, controller } = beginOperation();
    renderSetup("loading");
    const promise = (async () => {
      try {
        const response = await service.setup(providerKind, accountID, revision, controller.signal);
        if (!owns(run)) return false;
        const nextState = normalizeSetupResult(response, { accountID, revision, providerKind, providerID, lifecycle: snapshot });
        if (!nextState) {
          setupInFlight = null;
          renderSetup("error", SAFE_SETUP_ERROR);
          return false;
        }
        state = nextState;
        if (nextState.phase === "provider") {
          setupInFlight = null;
          renderCurrent();
          return true;
        }
        setupInFlight = null;
        renderCurrent();
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
    disposeConnect(); clearListeners();
    shell(phase, createLoadingStatus(message));
  }
  function renderBootstrap(errorMessage = "") {
    disposeConnect(); clearListeners();
    shell(state?.phase || "persona", createBootstrapStage({
      busy: bootstrapBusy,
      errorMessage,
      listen,
      onRetry: () => { void retryBootstrap(); },
      onBack: () => { void backFromBootstrap(); },
    }));
  }
  function loadBootstrapStatus(signal) {
    return service.bootstrapStatus(signal);
  }
  function acceptBootstrapResult(result) {
    if (result.kind === "completed") {
      state = result.state;
      bootstrapBusy = false;
      agentName = "";
      renderDone();
      return true;
    }
    if (result.kind === "partial") state = result.state;
    bootstrapBusy = false;
    renderBootstrap(SAFE_BOOTSTRAP_ERROR);
    return false;
  }
  async function startBootstrap(snapshot) {
    const expected = normalizeStatus(snapshot);
    if (bootstrapBusy || !["persona", "test"].includes(expected?.phase)) return false;
    bootstrapBusy = true;
    const { run, controller } = beginOperation();
    renderBootstrap();
    try {
      const result = await runOnboardingBootstrap({
        snapshot: expected,
        signal: controller.signal,
        bootstrap: (revision, signal) => service.bootstrap(revision, signal),
        loadStatus: loadBootstrapStatus,
      });
      if (!owns(run)) return false;
      return acceptBootstrapResult(result);
    } catch (error) {
      if (!owns(run) || controller.signal.aborted || error?.name === "AbortError") return false;
      bootstrapBusy = false;
      renderBootstrap(SAFE_BOOTSTRAP_ERROR);
      return false;
    } finally {
      releaseOperation(run, controller);
    }
  }
  function renderAutomaticBootstrap() {
    if (bootstrapAttempted) {
      renderBootstrap(SAFE_BOOTSTRAP_ERROR);
      return;
    }
    bootstrapAttempted = true;
    void startBootstrap(state);
  }
  async function retryBootstrap() {
    if (bootstrapBusy || !["persona", "test"].includes(state?.phase)) return false;
    bootstrapBusy = true;
    const { run, controller } = beginOperation();
    renderBootstrap();
    try {
      const response = await loadBootstrapStatus(controller.signal);
      if (!owns(run)) return false;
      const authoritative = normalizeStrictStatus(response);
      if (!authoritative) {
        bootstrapBusy = false;
        renderBootstrap(SAFE_BOOTSTRAP_ERROR);
        return false;
      }
      state = authoritative;
      if (authoritative.phase === "completed") {
        bootstrapBusy = false;
        agentName = "";
        renderDone();
        return true;
      }
      if (!["persona", "test"].includes(authoritative.phase)) {
        bootstrapBusy = false;
        renderCurrent();
        return false;
      }
      const result = await runOnboardingBootstrap({
        snapshot: authoritative,
        signal: controller.signal,
        bootstrap: (revision, signal) => service.bootstrap(revision, signal),
        loadStatus: loadBootstrapStatus,
      });
      if (!owns(run)) return false;
      return acceptBootstrapResult(result);
    } catch (error) {
      if (!owns(run) || controller.signal.aborted || error?.name === "AbortError") return false;
      bootstrapBusy = false;
      renderBootstrap(SAFE_BOOTSTRAP_ERROR);
      return false;
    } finally {
      releaseOperation(run, controller);
    }
  }
  async function backFromBootstrap() {
    const snapshot = normalizeStatus(state);
    if (bootstrapBusy || !["persona", "test"].includes(snapshot?.phase)
      || !successorRevision(snapshot.revision)) return false;
    bootstrapBusy = true;
    const { run, controller } = beginOperation();
    renderBootstrap();
    try {
      let response;
      try {
        response = await service.backToProviders(snapshot.revision, controller.signal);
      } catch (error) {
        if (controller.signal.aborted || error?.name === "AbortError") throw error;
        response = await service.status(controller.signal);
      }
      if (!owns(run)) return false;
      const authoritative = normalizeStatus(response);
      if (!authoritative) {
        bootstrapBusy = false;
        renderBootstrap(SAFE_BOOTSTRAP_ERROR);
        return false;
      }
      state = authoritative;
      bootstrapBusy = false;
      agentName = "";
      if (["persona", "test"].includes(authoritative.phase)) {
        renderBootstrap(SAFE_BOOTSTRAP_ERROR);
        return false;
      }
      renderCurrent();
      return authoritative.phase === "provider";
    } catch (error) {
      if (!owns(run) || controller.signal.aborted || error?.name === "AbortError") return false;
      bootstrapBusy = false;
      renderBootstrap(SAFE_BOOTSTRAP_ERROR);
      return false;
    } finally {
      releaseOperation(run, controller);
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
    disposeConnect(); clearListeners();
    doneView = createDoneStage({
      displayName: agentName, listen,
      onPortal: () => handoff("portal"),
      onKnowledge: () => handoff("knowledge"),
    });
    shell("completed", doneView.content);
  }
  async function loadCompletedAgent() {
    const { run, controller } = beginOperation();
    renderStageLoading("completed", "Đang tải tên trợ lý…");
    try {
      const response = await service.loadAgent({ signal: controller.signal });
      if (!owns(run)) return false;
      const authoritative = normalizeAgentResponse(response);
      const name = validDisplayName(authoritative?.display_name);
      if (!authoritative || !name) {
        renderSafeError();
        return false;
      }
      agentName = name;
      renderDone();
      return true;
    } catch (error) {
      if (!owns(run) || controller.signal.aborted || error?.name === "AbortError") return false;
      renderSafeError();
      return false;
    } finally {
      releaseOperation(run, controller);
    }
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
      case "persona":
      case "test": renderAutomaticBootstrap(); break;
      case "completed": renderDone(); break;
      default: renderSafeError();
    }
  }
  async function refreshStatus() {
    agentName = "";
    bootstrapBusy = false;
    const { run, controller } = beginOperation();
    renderLoading();
    try {
      const response = await service.status(controller.signal);
      if (!owns(run)) return false;
      const nextState = normalizeStatus(response);
      if (!nextState) {
        state = null;
        renderSafeError();
        return false;
      }
      state = nextState;
      providerBusy = false;
      renderCurrent();
      return true;
    } catch (error) {
      if (!owns(run) || controller.signal.aborted || error?.name === "AbortError") return false;
      renderSafeError();
      return false;
    } finally {
      releaseOperation(run, controller);
    }
  }
  function mount(container) {
    if (!container || typeof container.replaceChildren !== "function") {
      throw new TypeError("Onboarding page mount requires a host");
    }
    if (disposed) return controller;
    if (host === container && root?.parentNode === container) return controller;
    if (root && host && host !== container) {
      root.remove();
      host = container;
      host.replaceChildren(root);
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
    agentName = "";
    providerBusy = false;
    bootstrapBusy = false;
    providerOffDialog?.dispose();
    providerOffDialog = null;
    invalidate();
    clearListeners();
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
