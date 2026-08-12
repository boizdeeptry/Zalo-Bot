import { requestJSON } from "../core/api.js";
import { isProviderConnected } from "../core/providers-status.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";
import {
  createProviderConnect,
  INSTALL_CEILING,
  phaseProgress,
} from "../components/provider-connect.js";

export { INSTALL_CEILING, phaseProgress };

export const PROVIDER_CATALOG = [
  { kind: "claude-code", name: "Claude Code", group: "subscription", prefix: "cc", logoColor: "#c8613b" },
  { kind: "codex", name: "OpenAI Codex", group: "subscription", prefix: "cx", logoColor: "#0f7a63" },
  { kind: "openai", name: "OpenAI", group: "apikey", prefix: "oa", logoColor: "#10a37f" },
  { kind: "anthropic", name: "Anthropic", group: "apikey", prefix: "an", logoColor: "#c8613b" },
  { kind: "gemini", name: "Gemini", group: "apikey", prefix: "gm", logoColor: "#3f6ff5" },
  { kind: "openrouter", name: "OpenRouter", group: "apikey", prefix: "or", logoColor: "#5b5ef0" },
];
// CONNECTABLE_KINDS mirrors the backend subscriptionKinds: only these can run the in-Portal login
// flow. Two DIFFERENT login shapes: codex shows a device-auth code, claude-code shows a plain browser
// URL (no code). (Routing differs too — codex runs via cliAdapter, claude-code via runClaude for KB
// --add-dir access — but that's a backend concern; here we only drive the login UI.)
const CONNECTABLE_KINDS = new Set(["codex", "claude-code"]);
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
    // Connect flow addresses by KIND, not provider id: codex has no provider row yet the first
    // time someone connects it, so there is no id to key the path off of.
    connectStart: (kind, label, onboardingRevision = 0) => {
      const body = { label };
      if (Number.isSafeInteger(onboardingRevision) && onboardingRevision > 0) {
        body.onboarding_revision = onboardingRevision;
      }
      return request(`/llm/providers/${encodeURIComponent(kind)}/connect`, {
        method: "POST", body,
      });
    },
    connectStatus: (kind) => request(`/llm/providers/${encodeURIComponent(kind)}/connect`),
    connectCancel: (kind) => request(`/llm/providers/${encodeURIComponent(kind)}/connect`, { method: "DELETE" }),
    removeAccount: (providerId, accountId) => request(
      `${providerPath(providerId, "/accounts")}/${encodeURIComponent(accountId)}`,
      { method: "DELETE" },
    ),
  });
}

function galleryStatus(entry, byKind) {
  const p = byKind.get(entry.kind);
  if (!p) return { cls: "off", label: "Chưa kết nối" };
  // Boolean "đã nối" đến TỪ helper dùng chung (cùng luật với picker Combos + hasAnyConnectedProvider
  // bên Go); các nhánh dưới chỉ chọn NHÃN, không tự quyết on/off — để nhãn và picker không trôi khỏi nhau.
  const cls = isProviderConnected(p) ? "on" : "off";
  if (p.credential_unreadable) return { cls, label: "Cần đăng nhập lại" };
  // Gói thuê bao (codex/claude): "đã kết nối" = CÓ TÀI KHOẢN đăng nhập, KHÔNG phải credential — chúng
  // chạy proxy/CLI bằng phiên riêng của account, không có credential đi qua daemon. Nhánh này chỉ dựng
  // NHÃN (đếm số tài khoản); cls đã do isProviderConnected quyết.
  if (entry.group === "subscription") {
    const accounts = Array.isArray(p.accounts) ? p.accounts.filter((a) => a.enabled !== false) : [];
    if (accounts.length) {
      return { cls, label: accounts.length > 1 ? `Đã kết nối · ${accounts.length} tài khoản` : "Đã kết nối" };
    }
    // KHÔNG có nhánh p.system "Sẵn sàng" ở đây: dưới no-default, một claude-code hệ thống mà 0 account
    // đăng nhập KHÔNG trả lời được — nó im. "Đã nối" = có account bật, nên 0 account = "Chưa kết nối",
    // không phải một nhãn xanh gây hiểu nhầm.
    return { cls, label: "Chưa kết nối" };
  }
  if (p.last_check_status === "ok") return { cls, label: "Đã kết nối" };
  if (p.system) return { cls, label: "Sẵn sàng" };
  if (!p.credential_configured) return { cls, label: "Chưa kết nối" };
  return { cls, label: "Đã thêm" };
}
function card(entry, byKind, onOpen) {
  const st = galleryStatus(entry, byKind);
  return element("button", { className: "pv-card",
    attributes: { type: "button", "aria-label": `Mở ${entry.name}` }, on: { click() { onOpen(entry.kind); } } },
    element("span", { className: `pv-logo pv-logo-${entry.kind}`, attributes: { "aria-hidden": "true" } }),
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
// PROXY_KINDS: provider chạy qua PROXY (gọi thẳng API bằng phiên đăng nhập) thay vì CLI chính chủ.
// Proxy nhanh/nhiều tài khoản NHƯNG mang rủi ro nhà cung cấp hạn chế/khoá tài khoản — badge phải nói
// đúng, không bán "an toàn tuyệt đối". Hôm nay chỉ codex; claude-code/gemini-cli vẫn CLI chính chủ.
const PROXY_KINDS = new Set(["codex"]);

// safeTag là nhãn CẤP NHÓM: nhóm thuê bao giờ TRỘN (Claude chính chủ + Codex proxy) nên KHÔNG dán
// blanket "không rủi ro" ở đây — nói trung tính, để badge chi tiết từng provider mang sự thật riêng.
const safeTag = () => element("span", { className: "pv-safe-tag" },
  element("span", { className: "pv-dot" }),
  element("span", { text: "Claude chính chủ · Codex proxy" }),
);
// testAllButton nhận service/refresh làm tham số thay vì đóng bao (closure) — group()/renderGallery
// giữ nguyên là hàm thuần trên tham số đầu vào, không đụng trạng thái của mount(). Promise.allSettled
// vì một Provider lỗi (mất mạng, khoá hết hạn) không được chặn phần còn lại của lượt kiểm tra chung.
function testAllButton(entries, byKind, service, refresh) {
  const btn = element("button", { className: "pv-btn", attributes: { type: "button" }, text: "Kiểm tra tất cả" });
  btn.addEventListener("click", async () => {
    btn.disabled = true;
    try {
      await Promise.allSettled(entries.map((e) => {
        const p = byKind.get(e.kind);
        return p ? service.testSaved(p.id) : Promise.resolve();
      }));
      await refresh();
    } finally {
      btn.disabled = false;
    }
  });
  return btn;
}
function renderGallery(entries, byKind, onOpen, headFor) {
  const sub = entries.filter((e) => e.group === "subscription");
  const api = entries.filter((e) => e.group === "apikey");
  return element("div", { className: "pv-gallery" },
    group("Gói thuê bao", sub, byKind, onOpen, headFor(sub, byKind, true)),
    group("API Key", api, byKind, onOpen, headFor(api, byKind, false)));
}

function detailHead(entry, byKind) {
  const p = byKind.get(entry.kind);
  const count = p && Array.isArray(p.models) ? `${p.models.length} model` : "0 kết nối";
  return element("div", { className: "pv-detail-head" },
    element("span", { className: `pv-logo lg pv-logo-${entry.kind}`, attributes: { "aria-hidden": "true" } }),
    element("div", {}, element("div", { className: "pv-name", text: entry.name }),
      element("div", { className: "pv-sub", text: count })),
  );
}
const safeBadge = (kind) => (PROXY_KINDS.has(kind)
  ? element("div", { className: "pv-risk-badge" },
    element("span", { text: "Chạy qua PROXY — gọi thẳng API bằng phiên đăng nhập của tài khoản (như 9Router). Nhanh và"
      + " dùng được nhiều tài khoản, NHƯNG nhà cung cấp có thể hạn chế hoặc KHOÁ tài khoản. Không phải đăng nhập chính chủ." }))
  : element("div", { className: "pv-safe-badge" },
    element("span", { text: "Đăng nhập chính chủ qua CLI — không giả client, không proxy, không rủi ro khoá tài khoản." })));

function renderConnections(entry, p, ui) {
  const accounts = Array.isArray(p?.accounts) ? p.accounts : [];
  let body;
  if (accounts.length) {
    // Multi-account provider (e.g. codex): list each connected account with a delete button,
    // instead of the single-row "system" or "empty" states below.
    body = element("div", { className: "pv-account-list" }, accounts.map((a) => element("div", { className: "pv-account" },
      element("span", { className: "pv-account-label", text: a.label }),
      a.email ? element("span", { className: "pv-account-email", text: a.email }) : null,
      element("span", { className: `pv-account-status ${a.enabled ? "on" : "off"}`, text: a.enabled ? "Bật" : "Tắt" }),
      element("button", {
        className: "pv-btn danger",
        attributes: { type: "button", "aria-label": `Xoá ${a.label}` },
        text: "Xoá",
        on: { click() { void ui.onRemoveAccount(p.id, a.id); } },
      }),
    )));
  } else if (p && p.system) {
    body = element("div", { className: "pv-conn-row", text: `Chạy cục bộ trên máy này (${entry.name}).` });
  } else if (ui.connectable) {
    // Connectable subscription with zero accounts (codex, first visit): point at the add button
    // instead of the generic empty copy below.
    body = element("div", { className: "pv-conn-empty", text: "Chưa có tài khoản — bấm + Thêm kết nối để đăng nhập." });
  } else {
    body = element("div", { className: "pv-conn-empty", text: "Chưa có kết nối — No connections yet." });
  }
  // Label switches to "+ Thêm account" once at least one account is connected — same startConnect
  // flow underneath (ui.onAdd), just numbered for the next account via defaultLabel().
  const addText = accounts.length ? "+ Thêm account" : "+ Thêm kết nối";
  const add = ui.connectable
    ? element("button", {
        className: "pv-btn primary",
        // Disabled while this Provider's reusable Connect component is already open.
        attributes: { type: "button", disabled: ui.connecting, title: ui.connecting ? "Đang kết nối…" : undefined },
        text: addText,
        on: { click() { ui.onAdd(entry.kind); } },
      })
    : element("button", {
        className: "pv-btn primary",
        attributes: { type: "button", disabled: true, title: "Sắp có" },
        text: addText,
      });
  return element("section", { className: "pv-panel pv-connections" },
    element("div", { className: "pv-panel-head" }, element("h3", { text: "Kết nối" })),
    body, add, ui.connecting ? ui.connectSlot : null,
  );
}
function renderModels(entry, p) {
  const models = p && Array.isArray(p.models) ? p.models : [];
  const grid = models.length
    ? element("div", { className: "pv-model-grid" }, models.map((m) => element("div", { className: "pv-model" },
        element("span", { className: "pv-mid", text: `${entry.prefix}/${m.model_id}` }),
        element("span", { className: "pv-mname", text: m.name || m.model_id }),
      )))
    : element("div", { className: "pv-conn-empty", text: "Chưa có model." });
  // Add model deferred to #4 (Combos): disabled, no handler.
  const addModel = element("button", {
    className: "pv-btn",
    attributes: { type: "button", disabled: true, title: "Có ở Combos (#4)" },
    text: "+ Thêm model",
  });
  return element("section", { className: "pv-panel pv-models" },
    element("div", { className: "pv-panel-head" }, element("h3", { text: "Model khả dụng" })),
    grid, addModel,
  );
}

function renderDetail(entry, byKind, onBack, connUI) {
  const p = byKind.get(entry.kind);
  return element("div", { className: "pv-detail" },
    element("button", { className: "pv-back", attributes: { type: "button" }, text: "Về Providers", on: { click: onBack } }),
    detailHead(entry, byKind),
    entry.group === "subscription" ? safeBadge(entry.kind) : null,
    renderConnections(entry, p, connUI),
    renderModels(entry, p),
  );
}

export function createProvidersPage({ request = requestJSON, pollMs = 1500, crawlMs = 500 } = {}) {
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
        "Các nhà cung cấp mô hình mà bot được phép gọi. Thứ tự thử nằm ở mục Combos.",
      );
      const live = element("span", { className: "note", attributes: { "aria-live": "polite" } });

      // view cầm trạng thái điều hướng: "gallery" hoặc kind của Provider đang xem chi tiết.
      // paint() là điểm vẽ DUY NHẤT đọc view này để quyết định vẽ gì vào root.
      let view = "gallery";
      let providerConnect = null;
      let providerConnectKind = "";
      let providerConnectSlot = null;
      let providerConnectOpen = false;

      function disposeProviderConnect() {
        providerConnect?.dispose();
        providerConnect = null;
        providerConnectKind = "";
        providerConnectSlot = null;
        providerConnectOpen = false;
      }

      // Một detail sở hữu đúng một component Connect. Rời detail huỷ component để poll/timer/listener
      // không sống qua điều hướng; paint lại cùng detail chỉ dùng lại controller hiện có.
      const openDetail = (kind) => { disposeProviderConnect(); view = kind; paint(); };
      const backToGallery = () => { disposeProviderConnect(); view = "gallery"; paint(); };

      const byKindFrom = (providers) => new Map(providers.map((p) => [normalizeKind(p.kind), p]));
      let lastProviders = [];
      // Mặc định true để KHÔNG nháy banner trước lượt fetch đầu; refresh() ghi đè bằng giá trị thật.
      // Chỉ hiện banner khi backend nói HẲN false (thiếu trường = backend cũ = không kết luận "chưa nối").
      let hasConnectedProvider = true;
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

      const defaultLabel = (p) => `Tài khoản ${(p?.accounts?.length ?? 0) + 1}`;

      function startConnect(kind) {
        const p = byKindFrom(lastProviders).get(normalizeKind(kind));
        if (!providerConnect || providerConnectKind !== kind) return;
        providerConnectOpen = true;
        providerConnect.start({ label: defaultLabel(p) });
        paint();
      }

      // removeAccount deletes one account off a provider then refreshes — same shape as the
      // model-removal path, no local optimistic state to keep in sync.
      async function removeAccount(providerId, accountId) {
        await service.removeAccount(providerId, accountId);
        await refresh();
      }

      function ensureProviderConnect(entry) {
        if (providerConnect && providerConnectKind === entry.kind) return;
        disposeProviderConnect();
        const slot = element("div", { className: "pv-connect" });
        let component;
        component = createProviderConnect({
          kind: entry.kind,
          service,
          pollDelayMs: pollMs,
          crawlDelayMs: crawlMs,
          onConnected: async () => {
            if (disposed || providerConnect !== component) return;
            providerConnectOpen = false;
            await refresh();
          },
          onBack: () => {
            if (disposed || providerConnect !== component) return;
            providerConnectOpen = false;
            paint();
          },
        });
        providerConnect = component;
        providerConnectKind = entry.kind;
        providerConnectSlot = slot;
        component.mount(slot);
      }

      // headFor đóng bao service/refresh của mount() — group-head cần gọi testSaved() và refresh()
      // thật, còn renderGallery thì không nên biết tới hai thứ đó để vẫn là hàm thuần trên tham số.
      const headFor = (groupEntries, byKind, isSubscription) => element("span", { className: "pv-head" },
        isSubscription ? safeTag() : null,
        testAllButton(groupEntries, byKind, service, refresh));

      // noProviderBanner cảnh báo người trực: chưa nối gì thì bot IM với khách (không có Claude mặc
      // định để rơi về). null khi đã có provider nối — chỗ gọi phải filter(Boolean) trước khi đưa vào
      // native root.replaceChildren (nó ép null thành chuỗi "null", không tự bỏ như element()).
      function noProviderBanner() {
        if (hasConnectedProvider) return null;
        return element("div", {
          className: "pv-no-provider",
          attributes: { "data-no-provider-warning": "true", role: "status" },
          text: "Chưa kết nối provider nào — bot sẽ IM LẶNG với người nhắn. Hãy Connect một provider.",
        });
      }

      function galleryNode() {
        const filtered = query
          ? PROVIDER_CATALOG.filter((e) => e.name.toLowerCase().includes(query))
          : PROVIDER_CATALOG;
        return renderGallery(filtered, byKindFrom(lastProviders), openDetail, headFor);
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
            const connectable = entry.group === "subscription" && CONNECTABLE_KINDS.has(entry.kind);
            if (connectable) ensureProviderConnect(entry);
            const connUI = {
              connectable,
              connecting: providerConnectOpen && providerConnectKind === entry.kind,
              connectSlot: providerConnectSlot,
              onAdd: startConnect,
              onRemoveAccount: removeAccount,
            };
            root.replaceChildren(header(), renderDetail(entry, byKindFrom(lastProviders), backToGallery, connUI));
            return;
          }
          disposeProviderConnect();
          view = "gallery";
        }
        paintGallery();
        // filter(Boolean) vì root là DOM element THẬT: native replaceChildren ép null → chuỗi "null"
        // (một text node lạ ở đầu lưới cho mọi máy đã cấu hình đúng), khác element() tự bỏ null.
        root.replaceChildren(...[header(), noProviderBanner(), toolbar(), searchBar(), gallerySlot].filter(Boolean));
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
        // Chỉ vẽ toolbar gallery khi đang ở gallery — reload lúc đang xem detail không được nháy
        // ngược về toolbar gallery rồi mới vẽ lại detail ở paint().
        if (view === "gallery") root.replaceChildren(header(), toolbar());
        try {
          const data = await service.list();
          if (disposed || revision !== listRevision) return;
          const providers = Array.isArray(data?.providers) ? data.providers : [];
          live.textContent = "";
          lastProviders = providers;
          hasConnectedProvider = data?.hasConnectedProvider !== false;
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
          disposeProviderConnect();
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createProvidersPage().mount(container);
}
