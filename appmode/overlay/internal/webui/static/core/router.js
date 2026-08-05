export const ROUTES = Object.freeze({
  overview: Object.freeze({ id: "overview", label: "Tổng quan" }),
  agents: Object.freeze({ id: "agents", label: "Trợ lý AI" }),
  knowledge: Object.freeze({ id: "knowledge", label: "Tri thức" }),
  models: Object.freeze({ id: "models", label: "Mô hình" }),
});

export function routeFromHash(hash) {
  const value = String(hash ?? "")
    .replace(/^#/, "")
    .split(/[?&]/, 1)[0]
    .trim()
    .toLowerCase();

  return Object.hasOwn(ROUTES, value) ? value : "overview";
}

function disposerFrom(result) {
  if (typeof result === "function") {
    return result;
  }
  if (result && typeof result.dispose === "function") {
    return () => result.dispose();
  }
  return () => {};
}

export function createRouteHost(container) {
  if (!container || typeof container.replaceChildren !== "function") {
    throw new TypeError("Route host requires a DOM container");
  }

  let disposeCurrent = () => {};

  return Object.freeze({
    mount(page, context = {}) {
      if (!page || typeof page.mount !== "function") {
        throw new TypeError("Route page must expose mount(container, context)");
      }

      disposeCurrent();
      disposeCurrent = () => {};
      container.replaceChildren();
      const result = page.mount(container, context);
      if (result && typeof result.then === "function") {
        throw new TypeError("Route page mount must return its disposer synchronously");
      }
      disposeCurrent = disposerFrom(result);
    },

    dispose() {
      disposeCurrent();
      disposeCurrent = () => {};
    },
  });
}
