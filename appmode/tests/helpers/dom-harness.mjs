class TestNode {
  constructor(ownerDocument = null) {
    this.ownerDocument = ownerDocument;
    this.childNodes = [];
    this.parentNode = null;
  }

  get parentElement() {
    return this.parentNode instanceof TestElement ? this.parentNode : null;
  }

  get children() {
    return this.childNodes.filter((child) => child instanceof TestElement);
  }

  get firstChild() {
    return this.childNodes[0] ?? null;
  }

  get firstElementChild() {
    return this.children[0] ?? null;
  }

  get textContent() {
    return this.childNodes.map((child) => child.textContent).join("");
  }

  set textContent(value) {
    this.replaceChildren();
    const content = String(value ?? "");
    if (content) this.append(content);
  }

  append(...values) {
    for (const value of values) this.#appendValue(value);
  }

  appendChild(node) {
    this.append(node);
    return node;
  }

  replaceChildren(...values) {
    for (const child of this.childNodes) child.parentNode = null;
    this.childNodes = [];
    this.append(...values);
  }

  remove() {
    if (!this.parentNode) return;
    const siblings = this.parentNode.childNodes;
    const index = siblings.indexOf(this);
    if (index !== -1) siblings.splice(index, 1);
    this.parentNode = null;
  }

  #appendValue(value) {
    if (value === null || value === undefined || value === false) return;
    if (Array.isArray(value)) {
      for (const nested of value) this.#appendValue(nested);
      return;
    }
    if (value instanceof TestDocumentFragment) {
      for (const child of [...value.childNodes]) this.#appendValue(child);
      return;
    }

    const node = value instanceof TestNode
      ? value
      : (this.ownerDocument ?? this).createTextNode(String(value));
    node.remove();
    node.parentNode = this;
    if (!node.ownerDocument && this.ownerDocument) node.ownerDocument = this.ownerDocument;
    this.childNodes.push(node);
  }
}

class TestText extends TestNode {
  constructor(data, ownerDocument) {
    super(ownerDocument);
    this.nodeType = 3;
    this.data = String(data);
  }

  get textContent() {
    return this.data;
  }

  set textContent(value) {
    this.data = String(value ?? "");
  }
}

function dataProperty(attribute) {
  return attribute
    .slice(5)
    .replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
}

function dataAttribute(property) {
  return `data-${String(property).replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`)}`;
}

function classNames(element) {
  return element.className.trim().split(/\s+/).filter(Boolean);
}

class TestElement extends TestNode {
  constructor(tagName, ownerDocument) {
    super(ownerDocument);
    this.nodeType = 1;
    this.localName = String(tagName).toLowerCase();
    this.tagName = this.localName.toUpperCase();
    this.attributes = new Map();
    this.style = {};
    this.hidden = false;
    this.disabled = false;
    this.value = "";
    this.files = [];
    this.#listeners = new Map();

    this.dataset = new Proxy({}, {
      get: (_target, property) => {
        if (typeof property !== "string") return undefined;
        return this.getAttribute(dataAttribute(property)) ?? undefined;
      },
      set: (_target, property, value) => {
        this.setAttribute(dataAttribute(property), String(value));
        return true;
      },
      deleteProperty: (_target, property) => {
        this.removeAttribute(dataAttribute(property));
        return true;
      },
      ownKeys: () => [...this.attributes.keys()]
        .filter((name) => name.startsWith("data-"))
        .map(dataProperty),
      getOwnPropertyDescriptor: () => ({ configurable: true, enumerable: true }),
    });

    this.classList = Object.freeze({
      add: (...tokens) => this.#setClasses([...classNames(this), ...tokens]),
      remove: (...tokens) => {
        const removed = new Set(tokens.map(String));
        this.#setClasses(classNames(this).filter((token) => !removed.has(token)));
      },
      toggle: (token, force) => {
        const name = String(token);
        const present = this.classList.contains(name);
        const enabled = force === undefined ? !present : Boolean(force);
        if (enabled && !present) this.classList.add(name);
        if (!enabled && present) this.classList.remove(name);
        return enabled;
      },
      contains: (token) => classNames(this).includes(String(token)),
    });
  }

  #listeners;

  #setClasses(tokens) {
    const names = [...new Set(tokens.map(String).filter(Boolean))];
    this.className = names.join(" ");
  }

  get id() {
    return this.getAttribute("id") ?? "";
  }

  set id(value) {
    this.setAttribute("id", value);
  }

  get className() {
    return this.getAttribute("class") ?? "";
  }

  set className(value) {
    this.setAttribute("class", value);
  }

  setAttribute(name, value) {
    const key = String(name).toLowerCase();
    const content = String(value);
    this.attributes.set(key, content);
    if (key === "hidden") this.hidden = true;
    if (key === "disabled") this.disabled = true;
    if (key === "value") this.value = content;
  }

  getAttribute(name) {
    return this.attributes.get(String(name).toLowerCase()) ?? null;
  }

  hasAttribute(name) {
    return this.attributes.has(String(name).toLowerCase());
  }

  removeAttribute(name) {
    const key = String(name).toLowerCase();
    this.attributes.delete(key);
    if (key === "hidden") this.hidden = false;
    if (key === "disabled") this.disabled = false;
  }

  addEventListener(type, listener) {
    const eventType = String(type);
    if (!this.#listeners.has(eventType)) this.#listeners.set(eventType, new Set());
    this.#listeners.get(eventType).add(listener);
  }

  removeEventListener(type, listener) {
    this.#listeners.get(String(type))?.delete(listener);
  }

  dispatchEvent(event) {
    if (!event || !event.type) throw new TypeError("Event requires a type");
    const originalPreventDefault = typeof event.preventDefault === "function"
      ? event.preventDefault.bind(event)
      : () => {};
    let defaultPrevented = Boolean(event.defaultPrevented);
    event.preventDefault = () => {
      defaultPrevented = true;
      originalPreventDefault();
    };
    if (event.target == null) event.target = this;
    event.currentTarget = this;
    for (const listener of [...(this.#listeners.get(String(event.type)) ?? [])]) {
      if (typeof listener === "function") listener.call(this, event);
      else listener?.handleEvent?.(event);
    }
    event.currentTarget = null;
    try {
      Object.defineProperty(event, "defaultPrevented", {
        configurable: true,
        value: defaultPrevented,
      });
    } catch {
      // Native-style read-only events already report their own state.
    }
    return !defaultPrevented;
  }

  focus() {
    if (this.ownerDocument) this.ownerDocument.activeElement = this;
  }
}

class TestDocumentFragment extends TestNode {
  constructor(ownerDocument) {
    super(ownerDocument);
    this.nodeType = 11;
  }
}

class TestDocument extends TestNode {
  constructor() {
    super(null);
    this.nodeType = 9;
    this.ownerDocument = this;
    this.activeElement = null;
    this.body = this.createElement("body");
    this.append(this.body);
  }

  createElement(tagName) {
    return new TestElement(tagName, this);
  }

  createTextNode(data) {
    return new TestText(data, this);
  }

  createDocumentFragment() {
    return new TestDocumentFragment(this);
  }
}

function matches(node, predicate) {
  return typeof predicate === "function" ? predicate(node) : false;
}

export function find(root, predicate) {
  if (!root) return null;
  if (matches(root, predicate)) return root;
  for (const child of root.childNodes ?? []) {
    const match = find(child, predicate);
    if (match) return match;
  }
  return null;
}

export function findAll(root, predicate) {
  if (!root) return [];
  const matchesFound = matches(root, predicate) ? [root] : [];
  for (const child of root.childNodes ?? []) {
    matchesFound.push(...findAll(child, predicate));
  }
  return matchesFound;
}

export function text(node) {
  return node?.textContent ?? "";
}

export function installDOM() {
  const hadDocument = Object.hasOwn(globalThis, "document");
  const hadNode = Object.hasOwn(globalThis, "Node");
  const savedDocument = globalThis.document;
  const savedNode = globalThis.Node;
  const document = new TestDocument();
  globalThis.document = document;
  globalThis.Node = TestNode;
  let restored = false;

  function restore() {
    if (restored) return;
    restored = true;
    if (hadDocument) globalThis.document = savedDocument;
    else delete globalThis.document;
    if (hadNode) globalThis.Node = savedNode;
    else delete globalThis.Node;
  }

  return Object.freeze({ restore });
}

export { TestDocumentFragment, TestElement, TestNode, TestText };
