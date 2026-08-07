import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";

export const PROVIDER_CATALOG = [
  { kind: "claude-code", name: "Claude Code", group: "subscription", prefix: "cc", logoColor: "#c8613b" },
  { kind: "codex", name: "OpenAI Codex", group: "subscription", prefix: "cx", logoColor: "#0f7a63" },
  { kind: "openai", name: "OpenAI", group: "apikey", prefix: "oa", logoColor: "#10a37f" },
  { kind: "anthropic", name: "Anthropic", group: "apikey", prefix: "an", logoColor: "#c8613b" },
  { kind: "gemini", name: "Gemini", group: "apikey", prefix: "gm", logoColor: "#3f6ff5" },
  { kind: "openrouter", name: "OpenRouter", group: "apikey", prefix: "or", logoColor: "#5b5ef0" },
];
const normalizeKind = (k) => String(k ?? "").replace(/_/g, "-");

function providerPath(id, suffix = "") {
  const value = String(id ?? "").trim();
  if (!value) throw new TypeError("Provider id is required");
  return `/llm/providers/${encodeURIComponent(value)}${suffix}`;
}

// createProviderService dựng THÂN của mọi lượt gọi tại đây thay vì chuyển tiếp thẳng đối tượng
// người gọi đưa: /llm từ chối trường lạ, nên một phím gõ thừa ở tầng trên sẽ thành 400 chứ không
// bị bỏ qua. Đóng gói ở một chỗ thì hợp đồng đó chỉ phải đúng một lần.
export function createProviderService(request = requestJSON) {
  if (typeof request !== "function") {
    throw new TypeError("Provider service requires an API request function");
  }
  return Object.freeze({
    list: () => request("/llm/providers"),
    create: ({ kind, name, enabled, credential }) => request("/llm/providers", {
      method: "POST",
      body: { kind, name, enabled: Boolean(enabled), credential: credential ?? "" },
    }),
    update: (id, { name, enabled, credential }) => request(providerPath(id), {
      method: "PUT",
      body: { name, enabled: Boolean(enabled), credential: credential ?? "" },
    }),
    remove: (id) => request(providerPath(id), { method: "DELETE" }),
    replaceCredential: (id, credential) => request(providerPath(id, "/credential"), {
      method: "PUT",
      body: { credential },
    }),
    clearCredential: (id) => request(providerPath(id, "/credential"), { method: "DELETE" }),
    // model rỗng: adapter bỏ qua phép đối chiếu model khi chuỗi trống, nên lượt kiểm chỉ xác thực
    // khoá — đúng thứ form hỏi. Trường vẫn phải có mặt vì /llm từ chối thân thiếu lẫn thừa.
    testDraft: ({ kind, credential }) => request("/llm/providers/test", {
      method: "POST",
      body: { kind, credential, model: "" },
    }),
    testSaved: (id) => request(providerPath(id, "/test"), { method: "POST" }),
    discover: (id) => request(providerPath(id, "/discover"), { method: "POST" }),
    addModel: (id, modelID) => request(providerPath(id, "/models"), {
      method: "POST",
      body: { model_id: modelID, name: "" },
    }),
    removeModel: (id, modelID) => request(
      `${providerPath(id, "/models")}?model_id=${encodeURIComponent(modelID)}`,
      { method: "DELETE" },
    ),
  });
}

function galleryStatus(entry, byKind) {
  const p = byKind.get(entry.kind);
  if (!p) return { cls: "off", label: "Chưa kết nối" };
  if (p.credential_unreadable) return { cls: "off", label: "Cần đăng nhập lại" };
  if (p.last_check_status === "ok") return { cls: "on", label: "Đã kết nối" };
  if (p.system) return { cls: "on", label: "Sẵn sàng" };
  if (!p.credential_configured) return { cls: "off", label: "Chưa kết nối" };
  return { cls: "on", label: "Đã thêm" };
}
function card(entry, byKind, onOpen) {
  const st = galleryStatus(entry, byKind);
  return element("button", { className: "pv-card",
    attributes: { type: "button", "aria-label": `Mở ${entry.name}` }, on: { click() { onOpen(entry.kind); } } },
    element("span", { className: "pv-logo", attributes: { style: `background:${entry.logoColor}` }, text: entry.prefix.toUpperCase() }),
    element("span", { className: "pv-meta" },
      element("span", { className: "pv-name", text: entry.name }),
      element("span", { className: `pv-status ${st.cls}`, text: st.label })),
  );
}
function group(title, entries, byKind, onOpen, extraHead = null) {
  return element("section", { className: "pv-group" },
    element("div", { className: "pv-group-head" }, element("h2", { text: title }), extraHead),
    element("div", { className: "pv-grid" }, entries.map((e) => card(e, byKind, onOpen))));
}
const safeTag = () => element("span", { className: "pv-safe-tag" },
  element("span", { className: "pv-dot" }),
  element("span", { text: "Chính chủ · không rủi ro khoá" }),
);
function renderGallery(entries, byKind, onOpen) {
  const sub = entries.filter((e) => e.group === "subscription");
  const api = entries.filter((e) => e.group === "apikey");
  return element("div", { className: "pv-gallery" },
    group("Gói thuê bao — CLI chính chủ", sub, byKind, onOpen, safeTag()),
    group("API Key", api, byKind, onOpen));
}

function detailHead(entry, byKind) {
  const p = byKind.get(entry.kind);
  const count = p && Array.isArray(p.models) ? `${p.models.length} model` : "0 kết nối";
  return element("div", { className: "pv-detail-head" },
    element("span", { className: "pv-logo lg", attributes: { style: `background:${entry.logoColor}` }, text: entry.prefix.toUpperCase() }),
    element("div", {}, element("div", { className: "pv-name", text: entry.name }),
      element("div", { className: "pv-sub", text: count })),
  );
}
const safeBadge = () => element("div", { className: "pv-safe-badge" },
  element("span", { text: "Đăng nhập chính chủ qua CLI — không giả client, không proxy, không rủi ro khoá tài khoản." }),
);
function renderDetail(entry, byKind, onBack) {
  return element("div", { className: "pv-detail" },
    element("button", { className: "pv-back", attributes: { type: "button" }, text: "Về Providers", on: { click: onBack } }),
    detailHead(entry, byKind),
    entry.group === "subscription" ? safeBadge() : null,
    // Connections + Models panels: Task 5
  );
}

export function createProvidersPage({ request = requestJSON } = {}) {
  return Object.freeze({
    mount(container) {
      const controller = new AbortController();
      const service = createProviderService((path, options = {}) => request(path, {
        ...options,
        signal: controller.signal,
      }));
      const root = element("div", { className: "providers-page" });
      let disposed = false;
      let listRevision = 0;
      container.append(root);

      const header = () => pageHeader(
        "Providers",
        "Các nhà cung cấp mô hình mà bot được phép gọi. Thứ tự thử nằm ở mục Models.",
      );
      const live = element("span", { className: "note", attributes: { "aria-live": "polite" } });

      // view cầm trạng thái điều hướng: "gallery" hoặc kind của Provider đang xem chi tiết.
      // paint() là điểm vẽ DUY NHẤT đọc view này để quyết định vẽ gì vào root.
      let view = "gallery";
      const openDetail = (kind) => { view = kind; paint(); };
      const backToGallery = () => { view = "gallery"; paint(); };

      const byKindFrom = (providers) => new Map(providers.map((p) => [normalizeKind(p.kind), p]));
      let lastProviders = [];
      let query = "";
      const searchBox = element("input", { className: "pv-search-input",
        attributes: { type: "search", placeholder: "Tìm provider…", "aria-label": "Tìm provider" },
        on: { input(e) { query = e.currentTarget.value.trim().toLowerCase(); paintGallery(); } },
      });
      const searchBar = () => element("div", { className: "pv-search" }, searchBox);
      // gallerySlot là một node ổn định giữ nguyên vị trí trong root; paintGallery chỉ thay NỘI
      // DUNG của nó (replaceChildren), không đụng tới root hay các anh em header/toolbar/searchBar —
      // nên gõ vào ô search không làm mất focus hay dựng lại nút "Tải lại".
      const gallerySlot = element("div", {});

      function galleryNode() {
        const filtered = query
          ? PROVIDER_CATALOG.filter((e) => e.name.toLowerCase().includes(query))
          : PROVIDER_CATALOG;
        return renderGallery(filtered, byKindFrom(lastProviders), openDetail);
      }

      // paintGallery chỉ vẽ lại gallerySlot — KHÔNG đụng header/toolbar, KHÔNG refetch.
      function paintGallery() {
        gallerySlot.replaceChildren(galleryNode());
      }

      // paint là điểm vẽ toàn cục duy nhất, rẽ theo view. Gallery giữ nguyên cấu trúc
      // header+toolbar+searchBar+gallerySlot của Task 3 (search vẫn hoạt động, không mất focus);
      // detail thay root bằng header + renderDetail. kind lạ (đã bị xoá khỏi catalogue) rơi về gallery.
      function paint() {
        if (view !== "gallery") {
          const entry = PROVIDER_CATALOG.find((e) => e.kind === view);
          if (entry) {
            root.replaceChildren(header(), renderDetail(entry, byKindFrom(lastProviders), backToGallery));
            return;
          }
          view = "gallery";
        }
        paintGallery();
        root.replaceChildren(header(), toolbar(), searchBar(), gallerySlot);
      }

      const toolbar = () => element("div", { className: "row" },
        element("button", {
          className: "btn",
          attributes: { type: "button" },
          text: "Tải lại",
          on: { click() { void refresh(); } },
        }),
        live,
      );

      // refresh chỉ chạy lúc mount, sau mỗi lượt ghi, và khi người dùng bấm Tải lại. KHÔNG hẹn giờ:
      // mỗi lượt GET /llm/providers giải mã một lần cho từng Provider để biết khoá còn mở được không.
      async function refresh() {
        const revision = ++listRevision;
        live.textContent = "Đang đọc danh sách Provider…";
        root.replaceChildren(header(), toolbar());
        try {
          const data = await service.list();
          if (disposed || revision !== listRevision) return;
          const providers = Array.isArray(data?.providers) ? data.providers : [];
          live.textContent = "";
          lastProviders = providers;
          paint();
        } catch (error) {
          if (disposed || revision !== listRevision || error?.name === "AbortError") return;
          live.textContent = "";
          root.replaceChildren(header(), toolbar(), errorPanel(error));
        }
      }

      void refresh();
      return {
        dispose() {
          disposed = true;
          listRevision++;
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createProvidersPage().mount(container);
}
