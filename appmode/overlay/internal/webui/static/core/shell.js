import { element } from "./ui.js";

function navigationItem(item, activeId) {
  const selected = item.id === activeId;
  const classes = ["nav"];
  if (selected) classes.push("on");
  if (item.todo) classes.push("todo");

  const attributes = { href: item.href || `#${item.id}` };
  if (!item.href) attributes["data-route"] = item.id;
  if (selected) attributes["aria-current"] = "page";

  return element(
    "a",
    { className: classes.join(" "), attributes },
    element("span", {
      className: "ic",
      attributes: { "aria-hidden": "true" },
      text: item.icon,
    }),
    item.label,
  );
}

export function renderNavigation({ container, groups, activeId }) {
  if (!container || typeof container.replaceChildren !== "function") {
    throw new TypeError("Navigation requires a DOM container");
  }
  if (!Array.isArray(groups)) {
    throw new TypeError("Navigation groups must be an array");
  }

  const fragment = document.createDocumentFragment();
  for (const group of groups) {
    fragment.append(element("div", { className: "railhead", text: group.heading }));
    for (const item of group.items) fragment.append(navigationItem(item, activeId));
  }
  container.replaceChildren(fragment);
}
