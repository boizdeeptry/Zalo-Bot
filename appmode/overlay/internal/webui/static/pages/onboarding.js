import { createProviderConnect } from "../components/provider-connect.js";
import { requestJSON as sharedRequestJSON } from "../core/api.js";
import { element } from "../core/ui.js";
import { createProviderService } from "./providers.js";

const SUPPORTED_PROVIDERS = new Set(["codex", "claude-code"]);
const SUPPORTED_PHASES = new Set([
  "provider", "connect", "setup", "persona", "test", "completed",
]);
const STATUS_FIELDS = Object.freeze([
  "required", "current_version", "completed_version", "phase", "provider_kind",
  "suggested_provider_kind", "provider_id", "account_id", "model_id",
  "restart_in_progress", "revision",
]);
const IDENTIFIER_LIMIT = 1_000;
const CONTROL_CHARACTERS = /[\u0000-\u001f\u007f-\u009f]/;
const STEP_LABELS = Object.freeze(["Kết nối", "Cá nhân hoá", "Trò chuyện thử"]);
const SAFE_ERROR = "Không thể tiếp tục thiết lập. Trạng thái chưa hợp lệ hoặc đã thay đổi.";
const SAFE_SETUP_ERROR = "Chưa thể chuẩn bị cấu hình. Vui lòng thử lại.";
const isRecord = (value) => Boolean(value)
  && typeof value === "object" && !Array.isArray(value);
const positiveRevision = (value) => Number.isSafeInteger(value) && value > 0 ? value : 0;
const successorRevision = (value) => positiveRevision(value)
  && value < Number.MAX_SAFE_INTEGER ? value + 1 : 0;

function semanticString(value, { allowEmpty = false } = {}) {
  if (typeof value !== "string"
    || value.length > IDENTIFIER_LIMIT
    || value.trim() !== value
    || CONTROL_CHARACTERS.test(value)
    || (!allowEmpty && !value)) {
    return null;
  }
  return value;
}
function optionalSemanticString(record, field) {
  if (!Object.hasOwn(record, field)) return "";
  return semanticString(record[field], { allowEmpty: true });
}
function projectStatus(value) {
  if (!isRecord(value)) return value;
  const projected = {};
  for (const field of STATUS_FIELDS) {
    if (Object.hasOwn(value, field)) projected[field] = value[field];
  }
  return projected;
}
function requireProviderKind(kind) {
  if (!SUPPORTED_PROVIDERS.has(kind)) {
    throw new RangeError("Onboarding provider kind is not supported");
  }
  return kind;
}
function requireRevision(revision) {
  const valid = positiveRevision(revision);
  if (!valid) throw new RangeError("Onboarding revision must be a positive safe integer");
  return valid;
}
function requireAccountID(accountId) {
  const valid = semanticString(accountId);
  if (!valid) throw new TypeError("Onboarding account id is invalid");
  return valid;
}
function requireSuccessor(revision) {
  const successor = successorRevision(revision);
  if (!successor) throw new RangeError("Onboarding revision has no safe successor");
  return successor;
}
function normalizeSetupResponse(response, { accountID, revision, providerKind = "" }) {
  if (!isRecord(response)) return null;
  const kind = semanticString(response.provider_kind);
  const providerID = semanticString(response.provider_id);
  const returnedAccountID = semanticString(response.account_id);
  const modelID = semanticString(response.model_id);
  const stagedComboID = semanticString(response.staged_combo_id);
  const comboName = semanticString(response.combo_name);
  if (response.revision !== successorRevision(revision)
    || (Object.hasOwn(response, "phase") && response.phase !== "persona")
    || !SUPPORTED_PROVIDERS.has(kind)
    || (providerKind && kind !== providerKind)
    || providerID !== kind || returnedAccountID !== accountID
    || !modelID || !stagedComboID || !comboName) return null;
  return {
    phase: "persona", revision: response.revision, provider_kind: kind,
    provider_id: providerID, account_id: returnedAccountID, model_id: modelID,
    staged_combo_id: stagedComboID, combo_name: comboName,
  };
}
export function createOnboardingService({ requestJSON = sharedRequestJSON } = {}) {
  if (typeof requestJSON !== "function") {
    throw new TypeError("Onboarding service requires a requestJSON function");
  }
  return Object.freeze({
    status(signal) {
      return Promise.resolve(requestJSON("/onboarding/status", { signal })).then(projectStatus);
    },
    selectProvider(kind, revision, signal) {
      const providerKind = requireProviderKind(kind), currentRevision = requireRevision(revision);
      requireSuccessor(currentRevision);
      return Promise.resolve(requestJSON("/onboarding/provider", {
        method: "PUT", body: { kind: providerKind, revision: currentRevision }, signal,
      })).then((response) => {
        const normalized = normalizeProviderSelection(response, { providerKind, revision: currentRevision });
        if (!normalized) throw new TypeError("Invalid onboarding provider response");
        return normalized;
      });
    },
    setup(accountId, revision, signal) {
      const exactAccountID = requireAccountID(accountId);
      const currentRevision = requireRevision(revision);
      requireSuccessor(currentRevision);
      return Promise.resolve(requestJSON("/onboarding/setup", {
        method: "POST",
        body: { account_id: exactAccountID, revision: currentRevision },
        signal,
      })).then((response) => {
        const normalized = normalizeSetupResponse(response, {
          accountID: exactAccountID, revision: currentRevision,
        });
        if (!normalized) throw new TypeError("Invalid onboarding setup response");
        return normalized;
      });
    },
  });
}
function normalizeStatus(snapshot) {
  if (!isRecord(snapshot)
    || snapshot.required !== true
    || !SUPPORTED_PHASES.has(snapshot.phase)) {
    return null;
  }
  const revision = positiveRevision(snapshot.revision);
  const providerKindValue = optionalSemanticString(snapshot, "provider_kind");
  const suggestionValue = optionalSemanticString(snapshot, "suggested_provider_kind");
  const providerID = optionalSemanticString(snapshot, "provider_id");
  const accountID = optionalSemanticString(snapshot, "account_id");
  const modelID = optionalSemanticString(snapshot, "model_id");
  if (!revision
    || providerKindValue === null
    || suggestionValue === null
    || providerID === null
    || accountID === null
    || modelID === null) {
    return null;
  }
  const providerKind = SUPPORTED_PROVIDERS.has(providerKindValue) ? providerKindValue : "";
  const suggestedProviderKind = SUPPORTED_PROVIDERS.has(suggestionValue) ? suggestionValue : "";
  if (snapshot.phase !== "provider" && snapshot.phase !== "completed"
    && !providerKind) {
    return null;
  }
  if ((snapshot.phase === "setup" || snapshot.phase === "persona")
    && (!accountID || !providerID || providerID !== providerKind)) {
    return null;
  }
  if (snapshot.phase === "persona" && !modelID) return null;
  return {
    required: true,
    phase: snapshot.phase,
    provider_kind: providerKind,
    suggested_provider_kind: suggestedProviderKind,
    provider_id: providerID,
    account_id: accountID,
    model_id: modelID,
    revision,
  };
}
function normalizeProviderSelection(snapshot, { providerKind, revision }) {
  const normalized = normalizeStatus(snapshot);
  if (!normalized || normalized.phase !== "connect"
    || normalized.provider_kind !== providerKind
    || normalized.revision !== successorRevision(revision)
    || normalized.provider_id || normalized.account_id || normalized.model_id) return null;
  return normalized;
}
function normalizeConnectedSetup(snapshot, { providerKind, accountID, revision }) {
  const normalized = normalizeStatus(snapshot);
  if (!normalized || normalized.phase !== "setup"
    || normalized.provider_kind !== providerKind || normalized.provider_id !== providerKind
    || normalized.account_id !== accountID || normalized.model_id
    || normalized.revision !== successorRevision(revision)) return null;
  return normalized;
}
function normalizeSetupResult(result, expected) {
  const normalized = normalizeSetupResponse(result, {
    accountID: expected.accountID,
    revision: expected.revision,
    providerKind: expected.providerKind,
  });
  if (!normalized || normalized.provider_id !== expected.providerID) return null;
  return { required: true, suggested_provider_kind: "", ...normalized };
}
const providerName = (kind) => kind === "claude-code" ? "Claude Code" : "Codex";
function currentStepIndex(phase) {
  if (phase === "persona") return 1;
  if (phase === "test" || phase === "completed") return 2;
  return 0;
}
function validatePageService(service) {
  for (const method of ["status", "selectProvider", "setup"]) {
    if (typeof service?.[method] !== "function") {
      throw new TypeError(`Onboarding page service requires ${method}()`);
    }
  }
}
function validateConnectController(controller) {
  for (const method of ["mount", "start", "cancel", "dispose"]) {
    if (typeof controller?.[method] !== "function") {
      throw new TypeError(`Provider Connect controller requires ${method}()`);
    }
  }
}
export function createOnboardingPage({
  initialStatus,
  service = createOnboardingService(),
  connectService = createProviderService(),
  connectFactory = createProviderConnect,
  onComplete = () => {},
  setupSuccessDelayMs = 0,
  setTimeoutFn = setTimeout,
  clearTimeoutFn = clearTimeout,
  connectOptions = {},
} = {}) {
  validatePageService(service);
  if (typeof connectFactory !== "function") {
    throw new TypeError("Onboarding page requires a connect factory");
  }
  if (typeof onComplete !== "function") throw new TypeError("onComplete must be a function");
  let state = normalizeStatus(initialStatus);
  let selectedProvider = state?.phase === "provider"
    ? state.provider_kind || state.suggested_provider_kind
    : "";
  let host = null;
  let root = null;
  let disposed = false;
  let generation = 0;
  let activeController = null;
  let personaTimer = null;
  let connectController = null;
  let connectKey = "";
  let connectBackPromise = null;
  let connectTerminalPromise = null;
  let setupInFlight = null;
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

  function rail(phase) {
    const current = currentStepIndex(phase);
    return element(
      "nav",
      { className: "onboarding-rail", attributes: { "aria-label": "Tiến trình thiết lập" } },
      element("ol", { className: "onboarding-step-list" }, STEP_LABELS.map((label, index) => {
        const attributes = index === current ? { "aria-current": "step" } : {};
        return element(
          "li",
          {
            className: `onboarding-step${index === current ? " is-current" : ""}`,
            attributes,
          },
          element("span", { className: "onboarding-step-index", text: index + 1 }),
          element("span", { className: "onboarding-step-label", text: label }),
        );
      })),
    );
  }

  function shell(phase, content) {
    root.replaceChildren(
      element(
        "div",
        { className: "onboarding-shell" },
        element(
          "aside",
          { className: "onboarding-sidebar" },
          element("div", { className: "onboarding-brand", text: "TuvanZalo" }),
          rail(phase),
        ),
        element("main", { className: "onboarding-main" }, content),
      ),
    );
  }

  function retryButton(action) {
    return listen(element("button", {
      className: "onboarding-button onboarding-button--secondary",
      attributes: { type: "button" },
      text: "Thử lại",
    }), "click", () => { void action(); });
  }

  function renderSafeError(message = SAFE_ERROR, retry = refreshStatus) {
    disposeConnect();
    clearListeners();
    shell(
      state?.phase || "provider",
      element(
        "section",
        { className: "onboarding-stage onboarding-error-stage" },
        element("h1", { text: "Chưa thể tiếp tục" }),
        element("p", {
          className: "onboarding-error",
          attributes: { role: "alert" },
          text: message,
        }),
        retryButton(retry),
      ),
    );
  }

  function renderLoading() {
    connectTerminalPromise = null;
    disposeConnect();
    clearListeners();
    shell(
      state?.phase || "provider",
      element("p", {
        className: "onboarding-status",
        attributes: { role: "status" },
        text: "Đang tải lại trạng thái…",
      }),
    );
  }

  function renderWelcome(message = "") {
    connectTerminalPromise = null;
    disposeConnect();
    clearListeners();
    const cards = [
      ["codex", "Codex"],
      ["claude-code", "Claude Code"],
    ].map(([kind, label]) => listen(element("button", {
      className: `onboarding-provider-card${selectedProvider === kind ? " is-selected" : ""}`,
      attributes: {
        type: "button",
        "data-provider-kind": kind,
        "aria-pressed": String(selectedProvider === kind),
      },
      text: label,
    }), "click", () => {
      selectedProvider = kind;
      renderWelcome(message);
    }));
    const proceed = element("button", {
      className: "onboarding-button onboarding-button--primary",
      attributes: { type: "button", disabled: !selectedProvider },
      text: "Tiếp tục",
    });
    let selecting = false;
    listen(proceed, "click", () => {
      if (selecting || !selectedProvider) return;
      selecting = true;
      proceed.disabled = true;
      for (const card of cards) card.disabled = true;
      void selectProvider();
    });
    const error = message
      ? element("p", {
          className: "onboarding-error",
          attributes: { role: "alert" },
          text: message,
        })
      : null;
    const retry = message ? retryButton(refreshStatus) : null;
    shell(
      "provider",
      element(
        "section",
        { className: "onboarding-stage onboarding-provider-stage" },
        element("p", { className: "onboarding-eyebrow", text: "Chào mừng" }),
        element("h1", { text: "Chọn nhà cung cấp" }),
        element("p", {
          className: "onboarding-provider-intro",
          text: "Chọn công cụ AI bạn muốn dùng cho trợ lý.",
        }),
        element("div", { className: "onboarding-provider-grid" }, cards),
        element("p", {
          className: "onboarding-provider-warning",
          text: "Bạn cần đăng nhập. Cấu hình đang dùng chỉ thay đổi sau khi kết nối được xác minh và bạn bấm Hoàn tất.",
        }),
        error,
        element("div", { className: "onboarding-actions" }, retry, proceed),
      ),
    );
  }

  async function selectProvider() {
    if (!state || !selectedProvider || !SUPPORTED_PROVIDERS.has(selectedProvider)) return false;
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

  function terminalIdentity(terminal, expectedKind) {
    if (!isRecord(terminal)) return null;
    const kind = semanticString(terminal.kind);
    const providerID = semanticString(terminal.providerId);
    const accountID = semanticString(terminal.accountId);
    if (kind !== expectedKind || providerID !== expectedKind || !accountID) return null;
    return { kind, providerID, accountID };
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
    clearListeners();
    const kind = state.provider_kind;
    const key = `${kind}\u0000${state.revision}`;
    const slot = element("div", { className: "onboarding-connect-slot" });
    shell(
      "connect",
      element(
        "section",
        { className: "onboarding-stage onboarding-connect-stage" },
        element("h1", { text: `Kết nối ${providerName(kind)}` }),
        element("p", {
          className: "onboarding-connect-intro",
          text: "Đăng nhập và chờ hệ thống xác minh tài khoản.",
        }),
        slot,
      ),
    );
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
    disposeConnect();
    clearListeners();
    const complete = status === "complete";
    const error = status === "error";
    const statusText = complete ? "✓" : "Đang chuẩn bị cấu hình…";
    shell(
      "setup",
      element(
        "section",
        { className: "onboarding-stage onboarding-setup-stage" },
        element("h1", { text: "Kết nối nhà cung cấp" }),
        element("p", {
          className: `onboarding-setup-status${complete ? " is-complete" : ""}`,
          attributes: { role: "status" },
          text: statusText,
        }),
        error ? element("p", {
          className: "onboarding-error",
          attributes: { role: "alert" },
          text: message || SAFE_SETUP_ERROR,
        }) : null,
        error ? retryButton(refreshStatus) : null,
      ),
    );
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
        const nextState = normalizeSetupResult(response, {
          accountID,
          revision,
          providerKind,
          providerID,
        });
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

  function renderPersona() {
    disposeConnect();
    clearListeners();
    shell(
      "persona",
      element(
        "section",
        { className: "onboarding-stage onboarding-persona-stage" },
        element("p", { className: "onboarding-eyebrow", text: "Cá nhân hoá" }),
        element("h1", { text: "Trợ lý của bạn là ai" }),
        element("p", {
          className: "onboarding-placeholder",
          text: "Bước cá nhân hoá sẽ xuất hiện tại đây.",
        }),
      ),
    );
  }

  function renderLaterPlaceholder(phase) {
    connectTerminalPromise = null;
    disposeConnect();
    clearListeners();
    const completed = phase === "completed";
    shell(
      phase,
      element(
        "section",
        { className: "onboarding-stage onboarding-placeholder-stage" },
        element("h1", { text: completed ? "Thiết lập đã hoàn tất" : "Trò chuyện thử" }),
        element("p", {
          className: "onboarding-placeholder",
          text: completed
            ? "Trợ lý đã sẵn sàng."
            : "Bước trò chuyện thử sẽ xuất hiện tại đây.",
        }),
      ),
    );
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
      case "persona": renderPersona(); break;
      case "test":
      case "completed": renderLaterPlaceholder(state.phase); break;
      default: renderSafeError();
    }
  }

  async function refreshStatus() {
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
      selectedProvider = nextState.phase === "provider"
        ? nextState.provider_kind || nextState.suggested_provider_kind
        : "";
      renderCurrent();
      return true;
    } catch (error) {
      if (!owns(run) || error?.name === "AbortError") return false;
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
