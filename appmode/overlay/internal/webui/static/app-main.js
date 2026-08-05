import { NAVIGATION, ROUTES, createRouteHost, routeFromHash } from "./core/router.js";
import { renderNavigation } from "./core/shell.js";
import { errorPanel } from "./core/ui.js";

const implementedPages = Object.freeze({
  agents: async () => {
    const page = await import("./pages/agents.js");
    return { mount: page.mount };
  },
  knowledge: async () => {
    const page = await import("./pages/knowledge.js");
    return { mount: page.mount };
  },
  models: async () => {
    const page = await import("./pages/models.js");
    return { mount: page.mount };
  },
});

async function loadRoutePage(route) {
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
      routeHost.mount(page, { routeId });
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
} = {}) {
  const nav = documentRef.querySelector("[data-portal-nav]");
  const content = documentRef.querySelector("[data-portal-content]");
  const controller = createPortalController({
    nav,
    content,
    getHash: () => windowRef.location.hash,
    setTitle: (title) => { documentRef.title = title; },
  });
  let stopped = false;

  const onHashChange = () => { void controller.navigate(); };
  const onBeforeUnload = () => dispose();

  function dispose() {
    if (stopped) return;
    stopped = true;
    windowRef.removeEventListener("hashchange", onHashChange);
    windowRef.removeEventListener("beforeunload", onBeforeUnload);
    controller.dispose();
  }

  windowRef.addEventListener("hashchange", onHashChange);
  windowRef.addEventListener("beforeunload", onBeforeUnload, { once: true });
  void controller.navigate();

  return Object.freeze({ dispose, navigate: controller.navigate });
}

if (typeof globalThis.document !== "undefined" && typeof globalThis.window !== "undefined") {
  startPortal({ document: globalThis.document, window: globalThis.window });
}
