function freezeItems(items) {
  return Object.freeze(items.map((item) => Object.freeze(item)));
}

export const NAVIGATION = Object.freeze([
  Object.freeze({
    heading: "BUILD",
    items: freezeItems([
      { id: "agents", icon: "🤖", label: "AI Agents" },
      { id: "knowledge", icon: "🧠", label: "Knowledge" },
      {
        id: "workflows",
        icon: "🔄",
        label: "Workflows",
        todo: "Chuỗi bước tự động: nhận tin thì làm gì, khi nào chuyển người thật, khi nào gửi tệp.",
      },
      {
        id: "tools",
        icon: "🛠",
        label: "Tools & MCP",
        todo: "Nối agent với công cụ ngoài qua MCP: CRM, đơn hàng, tồn kho.",
      },
      { id: "providers", icon: "🔌", label: "Providers" },
      { id: "combos", icon: "🧩", label: "Combos" },
      { id: "memory", icon: "🗃", label: "Memory" },
    ]),
  }),
  Object.freeze({
    heading: "OPERATE",
    items: freezeItems([
      { id: "convo", icon: "💬", label: "Conversations", href: "/zalo" },
      {
        id: "analytics",
        icon: "📊",
        label: "Analytics",
        todo: "Số tin, số lượt bot trả lời, tỉ lệ phải chuyển người thật, cảm xúc khách để lại.",
      },
      {
        id: "eval",
        icon: "🧪",
        label: "Evaluation",
        todo: "Bộ câu hỏi mẫu chạy lại sau mỗi lần sửa văn phong, để biết sửa xong tốt hơn hay xấu đi.",
      },
      {
        id: "deploy",
        icon: "🚀",
        label: "Deployments",
        todo: "Chạy nhiều tài khoản Zalo, hoặc chuyển sang máy chủ.",
      },
    ]),
  }),
  Object.freeze({
    heading: "GOVERN",
    items: freezeItems([
      {
        id: "security",
        icon: "🛡",
        label: "Security",
        todo: "Ai vào được portal, ai đọc được hội thoại, nhật ký truy cập.",
      },
      {
        id: "workspace",
        icon: "🏢",
        label: "Workspace",
        todo: "Nhiều người trực cùng dùng, phân quyền theo người.",
      },
    ]),
  }),
  Object.freeze({
    heading: "SYSTEM",
    items: freezeItems([
      { id: "settings", icon: "⚙", label: "Settings" },
    ]),
  }),
]);

export const ROUTES = Object.freeze(
  Object.fromEntries(
    NAVIGATION.flatMap(({ items }) => items)
      .filter(({ href }) => !href)
      .map((item) => [item.id, item]),
  ),
);

export function routeFromHash(hash) {
  const value = String(hash ?? "")
    .replace(/^#/, "")
    .split(/[?&]/, 1)[0]
    .trim()
    .toLowerCase();

  return Object.hasOwn(ROUTES, value) ? value : "knowledge";
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

      const dispose = disposeCurrent;
      disposeCurrent = () => {};
      dispose();
      container.replaceChildren();
      const result = page.mount(container, context);
      if (result && typeof result.then === "function") {
        throw new TypeError("Route page mount must return its disposer synchronously");
      }
      disposeCurrent = disposerFrom(result);
    },

    dispose() {
      const dispose = disposeCurrent;
      disposeCurrent = () => {};
      dispose();
    },
  });
}
