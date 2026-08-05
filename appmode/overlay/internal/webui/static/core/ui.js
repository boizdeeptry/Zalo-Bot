function appendChild(parent, child) {
  if (child === null || child === undefined || child === false) return;
  if (Array.isArray(child)) {
    for (const nested of child) appendChild(parent, nested);
    return;
  }
  parent.append(child instanceof Node ? child : document.createTextNode(String(child)));
}

export function element(tag, options = {}, ...children) {
  const node = document.createElement(tag);
  if (options.className) node.className = options.className;
  if (options.text !== undefined) node.textContent = String(options.text);

  for (const [name, value] of Object.entries(options.attributes || {})) {
    if (value !== false && value !== null && value !== undefined) {
      node.setAttribute(name, value === true ? "" : String(value));
    }
  }
  for (const [event, handler] of Object.entries(options.on || {})) {
    node.addEventListener(event, handler);
  }
  for (const child of children) appendChild(node, child);
  return node;
}

export function pageHeader(title, description, eyebrow = "Portal vận hành") {
  return element(
    "header",
    { className: "page-header" },
    element("p", { className: "eyebrow", text: eyebrow }),
    element("h1", { text: title }),
    element("p", { className: "page-description", text: description }),
  );
}

export function statusPanel({ tone = "neutral", title, body }) {
  return element(
    "section",
    { className: `status-panel status-panel--${tone}`, attributes: { role: "status" } },
    element("span", { className: "status-dot", attributes: { "aria-hidden": "true" } }),
    element(
      "div",
      {},
      element("h2", { text: title }),
      element("p", { text: body }),
    ),
  );
}

export function errorPanel(error) {
  const message = error instanceof Error ? error.message : String(error);
  return statusPanel({
    tone: "danger",
    title: "Không tải được trang",
    body: message,
  });
}
