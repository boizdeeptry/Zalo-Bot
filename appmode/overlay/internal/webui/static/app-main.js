import { ROUTES, createRouteHost, routeFromHash } from "./core/router.js";
import { element, errorPanel } from "./core/ui.js";

const nav = document.querySelector("[data-portal-nav]");
const content = document.querySelector("[data-portal-content]");
const routeHost = createRouteHost(content);

const pageDefinitions = Object.freeze({
  overview: {
    load: async () => {
      const page = await import("./pages/overview.js");
      return { mount: page.mount };
    },
  },
  agents: {
    load: async () => {
      const page = await import("./pages/agents.js");
      return { mount: page.mount };
    },
  },
  knowledge: {
    load: async () => {
      const { createFoundationPage } = await import("./pages/overview.js");
      return createFoundationPage({
        title: "Tri thức",
        description: "Quản lý tài liệu nguồn và theo dõi quá trình đưa chúng vào kho tri thức.",
      });
    },
  },
  models: {
    load: async () => {
      const { createFoundationPage } = await import("./pages/overview.js");
      return createFoundationPage({
        title: "Mô hình",
        description: "Chọn mô hình phù hợp với tốc độ, chi phí và độ sâu của công việc.",
      });
    },
  },
});

function buildNavigation() {
  const fragment = document.createDocumentFragment();
  for (const route of Object.values(ROUTES)) {
    fragment.append(element(
      "a",
      {
        className: "nav-link",
        attributes: { href: `#${route.id}`, "data-route": route.id },
      },
      element("span", { className: "nav-marker", attributes: { "aria-hidden": "true" } }),
      element("span", { text: route.label }),
    ));
  }
  nav.replaceChildren(fragment);
}

function markActiveRoute(routeId) {
  for (const link of nav.querySelectorAll("[data-route]")) {
    const active = link.dataset.route === routeId;
    link.classList.toggle("is-active", active);
    if (active) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  }
}

let navigationRevision = 0;

async function renderRoute() {
  const revision = ++navigationRevision;
  const routeId = routeFromHash(window.location.hash);
  const definition = pageDefinitions[routeId];
  markActiveRoute(routeId);
  document.title = `${ROUTES[routeId].label} · Portal Zalo`;

  try {
    const page = await definition.load();
    if (revision !== navigationRevision) return;
    await routeHost.mount(page, { routeId });
    if (revision === navigationRevision) content.focus({ preventScroll: true });
  } catch (error) {
    if (revision !== navigationRevision) return;
    console.error("portal route failed", error);
    routeHost.dispose();
    content.replaceChildren(errorPanel(error));
  }
}

buildNavigation();
window.addEventListener("hashchange", renderRoute);
window.addEventListener("beforeunload", () => routeHost.dispose(), { once: true });
renderRoute();
