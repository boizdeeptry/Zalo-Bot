import { element, pageHeader } from "../core/ui.js";

export function createRoadmapPage(route) {
  if (!route || !route.todo) {
    throw new TypeError("Roadmap page requires a todo route");
  }

  return Object.freeze({
    mount(container) {
      container.append(
        pageHeader(route.label, "Phần này chưa có."),
        element("div", { className: "hint", text: route.todo }),
      );
      return { dispose() {} };
    },
  });
}
