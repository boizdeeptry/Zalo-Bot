import { NAVIGATION, ROUTES, createRouteHost, routeFromHash } from "./core/router.js";
import { createRailNavigation, renderNavigation } from "./core/shell.js";
import { errorPanel } from "./core/ui.js";
import {
  SUPPORTED_PROVIDERS,
  createOnboardingService,
  normalizeStatus,
  projectStatus,
} from "./pages/onboarding-contract.js";
import { createOnboardingPage } from "./pages/onboarding.js";

const GATE_CLASS = "onboarding-gated";
const SAFE_GATE_ERROR = "Không thể xác minh trạng thái thiết lập. Portal vẫn được khoá an toàn.";
const DESTINATION_HASHES = Object.freeze({ knowledge: "#knowledge", portal: "#agents" });

const implementedPages = Object.freeze({
  agents: async () => {
    const page = await import("./pages/agents.js");
    return { mount: page.mount };
  },
  knowledge: async () => {
    const page = await import("./pages/knowledge.js");
    return { mount: page.mount };
  },
  combos: async () => {
    const page = await import("./pages/combos.js");
    return { mount: page.mount };
  },
  providers: async () => {
    const page = await import("./pages/providers.js");
    return { mount: page.mount };
  },
  memory: async () => {
    const page = await import("./pages/memory.js");
    return { mount: page.mount };
  },
  settings: async () => {
    const page = await import("./pages/settings.js");
    return { mount: page.mount };
  },
});

export async function loadRoutePage(route) {
  if (route.todo) {
    const page = await import("./pages/roadmap.js");
    return page.createRoadmapPage(route);
  }
  return implementedPages[route.id]();
}

export function createPortalController({
  nav,
  content,
  getHash,
  loadPage = loadRoutePage,
  pageContext = {},
  routeHost = createRouteHost(content),
  renderNavigation: drawNavigation = renderNavigation,
  renderError = errorPanel,
  setTitle = (title) => { globalThis.document.title = title; },
  focusContent = () => content.focus({ preventScroll: true }),
  reportError = (...args) => globalThis.console.error(...args),
} = {}) {
  let navigationRevision = 0;
  let disposed = false;

  async function navigate() {
    if (disposed) return;
    const revision = ++navigationRevision;
    const routeId = routeFromHash(getHash());
    const route = ROUTES[routeId];
    drawNavigation({ container: nav, groups: NAVIGATION, activeId: routeId });
    setTitle(`${route.label} · Trợ lý Zalo`);

    try {
      const page = await loadPage(route);
      if (disposed || revision !== navigationRevision) return;
      const context = Object.freeze({ ...pageContext, routeId });
      routeHost.mount(page, context);
      if (!disposed && revision === navigationRevision) focusContent();
    } catch (error) {
      if (disposed || revision !== navigationRevision) return;
      reportError("portal route failed", error);
      try {
        routeHost.dispose();
      } catch (cleanupError) {
        reportError("portal cleanup failed", cleanupError);
      }
      content.replaceChildren(renderError(error));
    }
  }

  function dispose() {
    if (disposed) return;
    disposed = true;
    navigationRevision += 1;
    routeHost.dispose();
  }

  return Object.freeze({ dispose, navigate });
}

export function startPortal({
  document: documentRef = globalThis.document,
  window: windowRef = globalThis.window,
  onRestartOnboarding = () => {},
} = {}) {
  const nav = documentRef.querySelector("[data-portal-nav]");
  const content = documentRef.querySelector("[data-portal-content]");
  const toggle = documentRef.querySelector("#rail-toggle");
  const rail = documentRef.querySelector("#rail");
  const railController = createRailNavigation({ toggle, rail, navigation: nav });
  const controller = createPortalController({
    nav,
    content,
    getHash: () => windowRef.location.hash,
    pageContext: { onRestartOnboarding },
    setTitle: (title) => { documentRef.title = title; },
  });
  let stopped = false;

  const onHashChange = () => { void controller.navigate(); };

  function dispose() {
    if (stopped) return;
    stopped = true;
    windowRef.removeEventListener("hashchange", onHashChange);
    railController.dispose();
    controller.dispose();
  }

  windowRef.addEventListener("hashchange", onHashChange);
  void controller.navigate();

  return Object.freeze({ dispose, navigate: controller.navigate });
}

function attributeState(node, name) {
  return Object.freeze({
    present: node.hasAttribute(name),
    value: node.getAttribute(name),
  });
}

function restoreAttribute(node, name, state) {
  if (state.present) node.setAttribute(name, state.value ?? "");
  else node.removeAttribute(name);
}

function shellState(node) {
  return Object.freeze({
    ariaHidden: attributeState(node, "aria-hidden"),
    hidden: attributeState(node, "hidden"),
    hiddenProperty: Boolean(node.hidden),
    inert: attributeState(node, "inert"),
    inertProperty: "inert" in node ? Boolean(node.inert) : null,
  });
}

function hideShellNode(node) {
  node.hidden = true;
  node.setAttribute("hidden", "");
  node.setAttribute("aria-hidden", "true");
  if ("inert" in node) {
    node.inert = true;
    node.setAttribute("inert", "");
  }
}

function restoreShellNode(node, state) {
  node.hidden = state.hiddenProperty;
  restoreAttribute(node, "hidden", state.hidden);
  restoreAttribute(node, "aria-hidden", state.ariaHidden);
  if (state.inertProperty !== null) node.inert = state.inertProperty;
  restoreAttribute(node, "inert", state.inert);
}

function activeDisposer(result) {
  if (typeof result === "function") return result;
  if (result && typeof result.dispose === "function") return () => result.dispose();
  return () => {};
}

function normalizedHostRestart(response) {
  const projected = projectStatus(response);
  const status = normalizeStatus(projected);
  if (!status
    || status.phase !== "provider"
    || status.required !== true
    || status.restart_in_progress !== true
    || status.completed_version !== status.current_version
    || projected.provider_kind !== ""
    || projected.provider_id !== ""
    || projected.account_id !== ""
    || projected.model_id !== ""
    || (projected.suggested_provider_kind !== ""
      && !SUPPORTED_PROVIDERS.has(projected.suggested_provider_kind))) return null;
  return status;
}

function defaultOnboardingMount({ container, initialStatus, onComplete }) {
  const page = createOnboardingPage({ initialStatus, onComplete });
  page.mount(container);
  return page;
}

function gatePanel(documentRef, { retry } = {}) {
  const panel = documentRef.createElement("section");
  panel.className = retry ? "onboarding-gate onboarding-gate-error" : "onboarding-gate onboarding-gate-loading";
  panel.setAttribute("role", retry ? "alert" : "status");
  panel.setAttribute("aria-live", retry ? "assertive" : "polite");
  const title = documentRef.createElement("h1");
  title.textContent = retry ? "Chưa thể mở Portal" : "Đang kiểm tra thiết lập…";
  const body = documentRef.createElement("p");
  body.textContent = retry ? SAFE_GATE_ERROR : "Vui lòng chờ trong giây lát.";
  panel.append(title, body);
  if (retry) panel.append(retry);
  return panel;
}

export function startApp({
  document: documentRef = globalThis.document,
  window: windowRef = globalThis.window,
  loadStatus = (signal) => createOnboardingService().status(signal),
  startPortal: mountPortal = startPortal,
  mountOnboarding = defaultOnboardingMount,
} = {}) {
  if (!documentRef || !windowRef || typeof loadStatus !== "function"
    || typeof mountPortal !== "function" || typeof mountOnboarding !== "function") {
    throw new TypeError("Application startup dependencies are invalid");
  }
  const content = documentRef.querySelector("[data-portal-content]");
  const shellNodes = [
    documentRef.querySelector(".skip-link"),
    documentRef.querySelector("#rail-toggle"),
    documentRef.querySelector("#rail"),
    documentRef.querySelector("[data-portal-nav]"),
  ];
  if (!content || shellNodes.some((node) => !node)) {
    throw new TypeError("Application shell is incomplete");
  }

  const snapshots = shellNodes.map(shellState);
  const bodyHadGateClass = documentRef.body.classList.contains(GATE_CLASS);
  let shellGated = false;
  let disposed = false;
  let generation = 0;
  let activeController = null;
  let activeDispose = () => {};
  let activeKind = "";
  let retryBinding = null;
  let restartReconciliation = null;

  function clearRetryBinding() {
    if (!retryBinding) return;
    retryBinding.node.removeEventListener("click", retryBinding.listener);
    retryBinding = null;
  }

  function hideShell() {
    if (shellGated) return;
    shellGated = true;
    shellNodes.forEach(hideShellNode);
    documentRef.body.classList.add(GATE_CLASS);
  }

  function showShell() {
    if (!shellGated) return;
    shellGated = false;
    shellNodes.forEach((node, index) => restoreShellNode(node, snapshots[index]));
    if (!bodyHadGateClass) documentRef.body.classList.remove(GATE_CLASS);
  }

  function isCurrent(run) {
    return !disposed && run === generation;
  }

  function showFailure() {
    hideShell();
    renderRetry();
    return false;
  }

  function disposeActive() {
    try {
      activeDispose();
    } catch {
      return false;
    }
    activeDispose = () => {};
    activeKind = "";
    return true;
  }

  function discardActive() {
    try {
      activeDispose();
    } catch {
      // Application disposal is best-effort; no successor will be mounted.
    }
    activeDispose = () => {};
    activeKind = "";
  }

  function disposeLateMount(mounted) {
    try {
      activeDisposer(mounted)();
    } catch {
      // A stale child cannot block the current owner.
    }
  }

  function adoptActive(kind, mounted, run) {
    if (!isCurrent(run)) {
      disposeLateMount(mounted);
      return false;
    }
    activeDispose = activeDisposer(mounted);
    activeKind = kind;
    return true;
  }

  function mountActive(kind, mount, run) {
    let mounted;
    try {
      mounted = mount();
    } catch {
      return showFailure();
    }
    if (mounted && typeof mounted.then === "function") {
      return Promise.resolve(mounted)
        .then((resolved) => adoptActive(kind, resolved, run))
        .catch(() => (isCurrent(run) ? showFailure() : false));
    }
    return adoptActive(kind, mounted, run);
  }

  function renderLoading() {
    clearRetryBinding();
    content.replaceChildren(gatePanel(documentRef));
  }

  function renderRetry() {
    clearRetryBinding();
    const button = documentRef.createElement("button");
    button.type = "button";
    button.textContent = "Thử lại";
    const listener = () => { void retry(); };
    button.addEventListener("click", listener);
    retryBinding = { listener, node: button };
    content.replaceChildren(gatePanel(documentRef, { retry: button }));
  }

  function beginGate() {
    activeController?.abort();
    activeController = null;
    if (!disposeActive()) return showFailure();
    hideShell();
    renderLoading();
    return true;
  }

  function failClosed() {
    if (!disposeActive()) return showFailure();
    return showFailure();
  }

  function mountWizard(status, run) {
    hideShell();
    let handoffStarted = false;
    const onComplete = ({ destination } = {}) => {
      if (handoffStarted || !Object.hasOwn(DESTINATION_HASHES, destination)) {
        return Promise.resolve(false);
      }
      handoffStarted = true;
      return loadAuthoritative(destination);
    };
    return mountActive("wizard", () => mountOnboarding({
      container: content,
      initialStatus: status,
      onComplete,
    }), run);
  }

  function mountValidatedPortal(destination, run) {
    if (destination && Object.hasOwn(DESTINATION_HASHES, destination)) {
      windowRef.location.hash = DESTINATION_HASHES[destination];
    }
    clearRetryBinding();
    const mounted = mountActive("portal", () => mountPortal({
      document: documentRef,
      window: windowRef,
      onRestartOnboarding,
    }), run);
    if (mounted && typeof mounted.then === "function") {
      return mounted.then((ok) => {
        if (ok && isCurrent(run)) showShell();
        return ok;
      });
    }
    if (mounted) showShell();
    return mounted;
  }

  function transitionToStatus(status, destination, run) {
    if (!isCurrent(run)) return false;
    if (!disposeActive()) return showFailure();
    if (status.required === false) return mountValidatedPortal(destination, run);
    return mountWizard(status, run);
  }

  function reconcileRestart() {
    if (restartReconciliation) return restartReconciliation;
    if (disposed || activeKind !== "portal") return false;
    const run = ++generation;
    activeController?.abort();
    const controller = new AbortController();
    activeController = controller;
    let statusRequest;
    try {
      statusRequest = loadStatus(controller.signal);
    } catch (error) {
      statusRequest = Promise.reject(error);
    }
    const reconciliation = Promise.resolve(statusRequest)
      .then((response) => {
        if (!isCurrent(run) || controller.signal.aborted) return false;
        const status = normalizeStatus(projectStatus(response));
        if (!status) return failClosed();
        if (status.required === false) {
          showShell();
          clearRetryBinding();
          return false;
        }
        return transitionToStatus(status, "", run);
      })
      .catch((error) => {
        if (!isCurrent(run) || controller.signal.aborted || error?.name === "AbortError") {
          return false;
        }
        return failClosed();
      })
      .finally(() => {
        if (run === generation && activeController === controller) activeController = null;
        if (restartReconciliation === reconciliation) restartReconciliation = null;
      });
    restartReconciliation = reconciliation;
    return reconciliation;
  }

  function onRestartOnboarding(response) {
    if (disposed || activeKind !== "portal") return false;
    if (arguments.length === 0) return reconcileRestart();
    const status = normalizedHostRestart(response);
    if (!status) return false;
    const run = ++generation;
    activeController?.abort();
    activeController = null;
    return transitionToStatus(status, "", run);
  }

  function loadAuthoritative(destination = "") {
    if (disposed) return Promise.resolve(false);
    const run = ++generation;
    if (!beginGate()) return Promise.resolve(false);
    const controller = new AbortController();
    activeController = controller;
    let statusRequest;
    try {
      statusRequest = loadStatus(controller.signal);
    } catch (error) {
      statusRequest = Promise.reject(error);
    }
    return Promise.resolve(statusRequest)
      .then((response) => {
        if (disposed || run !== generation || controller.signal.aborted) return false;
        const status = normalizeStatus(projectStatus(response));
        if (!status) {
          return showFailure();
        }
        return transitionToStatus(status, destination, run);
      })
      .catch((error) => {
        if (disposed || run !== generation || controller.signal.aborted || error?.name === "AbortError") {
          return false;
        }
        return showFailure();
      })
      .finally(() => {
        if (run === generation && activeController === controller) activeController = null;
      });
  }

  function retry() {
    return loadAuthoritative();
  }

  const onBeforeUnload = () => dispose();

  function dispose() {
    if (disposed) return;
    disposed = true;
    generation += 1;
    activeController?.abort();
    activeController = null;
    clearRetryBinding();
    windowRef.removeEventListener("beforeunload", onBeforeUnload);
    discardActive();
  }

  hideShell();
  renderLoading();
  windowRef.addEventListener("beforeunload", onBeforeUnload, { once: true });
  const ready = loadAuthoritative();
  return Object.freeze({ dispose, ready, retry });
}

const moduleProtocol = new URL(import.meta.url).protocol;
if (moduleProtocol !== "file:"
  && typeof globalThis.document !== "undefined"
  && typeof globalThis.window !== "undefined"
  && globalThis.document.querySelector("[data-portal-content]")) {
  startApp({ document: globalThis.document, window: globalThis.window });
}
