import { requestJSON } from "../core/api.js";
import { normalizeProviderRuntimeResponse } from "../core/provider-runtime-catalog.js";
import { isProviderConnected } from "../core/providers-status.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";
import { createProviderOffDialog } from "../components/provider-off-dialog.js";
import {
  createProviderConnect,
  INSTALL_CEILING,
  phaseProgress,
} from "../components/provider-connect.js";

export { INSTALL_CEILING, phaseProgress };

function providerPath(id, suffix = "") {
  const value = String(id ?? "").trim();
  if (!value) throw new TypeError("Provider id is required");
  return `/llm/providers/${encodeURIComponent(value)}${suffix}`;
}

const providerMutationHandoff = (() => {
  const listeners = new Set();
  let settlementEpoch = 0;
  return Object.freeze({
    currentEpoch: () => settlementEpoch,
    subscribe(owner, listener) {
      const subscription = { owner, listener };
      listeners.add(subscription);
      return () => listeners.delete(subscription);
    },
    async run(owner, operation) {
      try { await operation(); } catch { /* The authoritative read decides ambiguous writes. */ }
      const epoch = ++settlementEpoch;
      for (const subscription of [...listeners]) {
        if (subscription.owner === owner) continue;
        try { subscription.listener(epoch); } catch { /* A page refresh owns its own safe failure UI. */ }
      }
      return epoch;
    },
  });
})();

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
  if (entry.connection_mode === "account") {
    const accounts = Array.isArray(p.accounts) ? p.accounts.filter((a) => a.enabled === true) : [];
    if (accounts.length) {
      return { cls, label: accounts.length > 1 ? `Đã kết nối · ${accounts.length} tài khoản` : "Đã kết nối" };
    }
    // KHÔNG có nhánh p.system "Sẵn sàng" ở đây: dưới no-default, một claude-code hệ thống mà 0 account
    // đăng nhập KHÔNG trả lời được — nó im. "Đã nối" = có account bật, nên 0 account = "Chưa kết nối",
    // không phải một nhãn xanh gây hiểu nhầm.
    return { cls, label: "Chưa kết nối" };
  }
  if (!p.credential_configured) return { cls, label: "Chưa kết nối" };
  return { cls, label: "Đã thêm" };
}

function providerMark(entry, large = false) {
  const mark = element("span", {
    className: `pv-logo${large ? " lg" : ""} pv-logo-${entry.kind}`,
    attributes: {
      "aria-hidden": "true",
      "data-provider-mark": entry.prefix,
      title: entry.display_name,
    },
    text: entry.prefix.toUpperCase(),
  });
  mark.style.backgroundColor = entry.theme_color;
  return mark;
}

function card(entry, byKind, onOpen, status = null) {
  const st = status ?? galleryStatus(entry, byKind);
  return element("button", { className: "pv-card",
    attributes: { type: "button", "aria-label": `Mở ${entry.display_name}` }, on: { click() { onOpen(entry.kind); } } },
    providerMark(entry),
    element("span", { className: "pv-meta" },
      element("span", { className: "pv-name", text: entry.display_name }),
      element("span", { className: `pv-status ${st.cls}`, text: st.label })),
  );
}
function group(title, entries, byKind, onOpen, extraHead = null) {
  return element("section", { className: "pv-group" },
    element("div", { className: "pv-group-head" }, element("h2", { text: title }), extraHead),
    element("div", { className: "pv-grid" }, entries.map((e) => card(e, byKind, onOpen))));
}
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
    group("Gói thuê bao", sub, byKind, onOpen, headFor(sub, byKind)),
    group("API Key", api, byKind, onOpen, headFor(api, byKind)));
}

function renderPersistedHidden(entries, byKind, onOpen) {
  if (!entries.length) return null;
  return element("section", { className: "pv-managed-hidden" },
    element("div", { className: "pv-group-head" },
      element("h2", { text: "Runtime đã lưu (chỉ đọc)" })),
    element("div", { className: "pv-grid" }, entries.map((entry) => card(entry, byKind, onOpen, { cls: "off", label: "Runtime ẩn · Chỉ đọc" }))));
}

function detailHead(entry, byKind) {
  const p = byKind.get(entry.kind);
  const count = p && Array.isArray(p.models) ? `${p.models.length} model` : "0 kết nối";
  return element("div", { className: "pv-detail-head" },
    providerMark(entry, true),
    element("div", {}, element("div", { className: "pv-name", text: entry.display_name }),
      element("div", { className: "pv-sub", text: count })),
  );
}
function executionBadge(entry) {
  switch (entry.execution_mode) {
    case "proxy":
      return element("div", { className: "pv-execution-badge pv-risk-badge" },
        element("span", { text: "Chạy qua PROXY — có rủi ro nhà cung cấp hạn chế hoặc khoá tài khoản." }));
    case "official_cli":
      return element("div", { className: "pv-execution-badge pv-safe-badge" },
        element("span", { text: "Chạy qua CLI chính chủ." }));
    case "local":
      return element("div", { className: "pv-execution-badge pv-safe-badge" },
        element("span", { text: "Runtime chạy cục bộ trên máy này." }));
    case "api":
    default:
      return element("div", { className: "pv-execution-badge pv-safe-badge" },
        element("span", { text: "Gọi API chính thức bằng credential đã lưu." }));
  }
}

function connectionBadge(entry) {
  const label = entry.connection_mode === "account"
    ? "Kết nối bằng tài khoản"
    : entry.connection_mode === "credential"
      ? "Kết nối bằng API key"
      : "Không hỗ trợ kết nối mới";
  return element("div", { className: "pv-connection-mode", text: label });
}

function enabledExplanation(entry, provider) {
  if (!provider) return "Chưa có Provider đã lưu; hãy kết nối trước khi quản lý trạng thái.";
  if (entry.visible !== true) return "Runtime ẩn chỉ đọc; không thể bật hoặc tắt tại đây.";
  if (provider.system === true) return "Provider hệ thống là chế độ chỉ đọc; không thể bật hoặc tắt tại đây.";
  return provider.enabled ? "Provider đang được sử dụng." : "Provider hiện không được sử dụng.";
}

function syncEnabledPanel(panel, entry, provider, { busy = false } = {}) {
  const enabled = provider?.enabled === true;
  const editable = Boolean(provider && entry.visible === true && provider.system === false);
  panel.control.checked = enabled;
  panel.control.setAttribute("aria-checked", String(enabled));
  panel.control.disabled = busy || !editable;
  panel.explanation.textContent = busy ? "Đang đối soát trạng thái Provider…" : enabledExplanation(entry, provider);
}

function createEnabledPanel(entry, provider, onChange) {
  const explanation = element("p", { className: "pv-enabled-explanation" });
  const feedback = element("p", {
    className: "pv-enabled-error",
    attributes: { role: "alert", "aria-live": "assertive", hidden: true },
  });
  const control = element("input", {
    className: "pv-enabled-switch",
    attributes: {
      type: "checkbox",
      role: "switch",
      "data-provider-enabled-switch": "true",
      "aria-label": `Bật hoặc tắt ${entry.display_name}`,
    },
    on: { change(event) { onChange(panel, event.currentTarget.checked); } },
  });
  const panel = {
    control,
    explanation,
    feedback,
    providerId: provider?.id ?? "",
    kind: entry.kind,
    node: element("section", { className: "pv-panel pv-enabled" },
      element("div", { className: "pv-panel-head" }, element("h3", { text: "Trạng thái sử dụng" }), control),
      explanation,
      feedback),
  };
  syncEnabledPanel(panel, entry, provider);
  return panel;
}

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
    body = element("div", { className: "pv-conn-row", text: `Chạy cục bộ trên máy này (${entry.display_name}).` });
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

function renderDetail(entry, byKind, backControl, connUI, enabledPanel) {
  const p = byKind.get(entry.kind);
  return element("div", { className: "pv-detail" },
    backControl,
    detailHead(entry, byKind),
    executionBadge(entry),
    connectionBadge(entry),
    enabledPanel.node,
    entry.visible ? renderConnections(entry, p, connUI) : null,
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
      const durableMutationService = createProviderService(request);
      const handoffOwner = Object.freeze({});
      const root = element("div", { className: "providers-page" });
      let disposed = false;
      let providerRequestEpoch = 0;
      let adoptedProviderKey = { settlementEpoch: -1, requestEpoch: -1 };
      let issuedProviderKey = adoptedProviderKey;
      const pendingProviderReads = new Map();
      let releaseProviderReads;
      const providerReadsDisposed = new Promise((resolve) => { releaseProviderReads = resolve; });
      let enabledRevision = 0;
      let enabledBusy = false;
      let currentEnabledPanel = null, currentDetailBack = null;
      container.append(root);

      const header = () => pageHeader(
        "Providers",
        "Các nhà cung cấp mô hình mà bot được phép gọi. Thứ tự thử nằm ở mục Combos.",
      );
      const live = element("span", { className: "note", attributes: { "aria-live": "polite" } });

      let view = "gallery";
      let providerConnect = null;
      let providerConnectKind = "";
      let providerConnectSlot = null;
      let providerConnectOpen = false;
      const providerOffDialog = createProviderOffDialog({
        listen(node, type, listener) {
          node.addEventListener(type, listener);
          return node;
        },
        onConfirm: ({ providerId, panel }, signal) => updateProviderEnabled(providerId, false, panel, {
          signal,
          showInlineError: false,
        }),
      });
      const unsubscribeHandoff = providerMutationHandoff.subscribe(handoffOwner, (settlementEpoch) => {
        if (!disposed) void refresh(settlementEpoch, { background: true }).catch(() => {});
      });

      function disposeProviderConnect() {
        providerConnect?.dispose();
        providerConnect = null;
        providerConnectKind = "";
        providerConnectSlot = null;
        providerConnectOpen = false;
      }

      const openDetail = (kind) => { disposeProviderConnect(); view = kind; paint(); };
      const backToGallery = () => { disposeProviderConnect(); view = "gallery"; paint(); };

      const byKindFrom = (providers) => new Map(providers.map((p) => [p.kind, p]));
      const byIDFrom = (providers) => new Map(providers.map((p) => [p.id, p]));
      let lastProviders = [];
      let runtimeOptions = [];
      // Chỉ hiện banner khi backend nói HẲN false; thiếu trường backend cũ không kết luận "chưa nối".
      let hasConnectedProvider = true;
      let query = "";
      const searchBox = element("input", { className: "pv-search-input",
        attributes: { type: "search", placeholder: "Tìm provider…", "aria-label": "Tìm provider" },
        on: { input(e) { query = e.currentTarget.value.trim().toLowerCase(); paintGallery(); } },
      });
      const searchBar = () => element("div", { className: "pv-search" }, searchBox);
      const gallerySlot = element("div", {});

      const defaultLabel = (p) => `Tài khoản ${(p?.accounts?.length ?? 0) + 1}`;

      function startConnect(kind) {
        const p = byKindFrom(lastProviders).get(kind);
        const entry = runtimeOptions.find((option) => option.kind === kind);
        if (!providerConnect || providerConnectKind !== kind
          || entry?.visible !== true || entry.connectable !== true) return;
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

      const headFor = (groupEntries, byKind) => element("span", { className: "pv-head" },
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
        const byKind = byKindFrom(lastProviders);
        const visible = runtimeOptions.filter((entry) => entry.visible);
        const hidden = runtimeOptions.filter((entry) => !entry.visible && byKind.has(entry.kind));
        const filtered = query
          ? visible.filter((entry) => `${entry.display_name} ${entry.description}`.toLowerCase().includes(query))
          : visible;
        const filteredHidden = query
          ? hidden.filter((entry) => `${entry.display_name} ${entry.description}`.toLowerCase().includes(query))
          : hidden;
        return element("div", { className: "pv-provider-browser" },
          renderGallery(filtered, byKind, openDetail, headFor),
          renderPersistedHidden(filteredHidden, byKind, openDetail));
      }

      function setEnabledError(panel, visible) {
        panel.feedback.textContent = visible
          ? "Chưa thể đổi trạng thái Provider. Trạng thái hiện tại vẫn được giữ nguyên."
          : "";
        panel.feedback.hidden = !visible;
      }

      function mountedEnabledPanel(providerId, fallback) {
        if (currentEnabledPanel?.providerId === providerId && currentEnabledPanel.control.isConnected) {
          return currentEnabledPanel;
        }
        return fallback?.providerId === providerId && fallback.control.isConnected ? fallback : null;
      }

      function providerKeyIsOlder(candidate, reference) {
        return candidate.settlementEpoch < reference.settlementEpoch
          || (candidate.settlementEpoch === reference.settlementEpoch
            && candidate.requestEpoch < reference.requestEpoch);
      }

      function beginProviderRead(settlementEpoch) {
        const key = { settlementEpoch, requestEpoch: ++providerRequestEpoch };
        let finish;
        const done = new Promise((resolve) => { finish = resolve; });
        const read = { key, done, finish };
        pendingProviderReads.set(key.requestEpoch, read);
        if (!providerKeyIsOlder(key, issuedProviderKey)) issuedProviderKey = key;
        return read;
      }

      const providerKeyIsStale = (key) => providerKeyIsOlder(key, adoptedProviderKey)
        || providerKeyIsOlder(key, issuedProviderKey);

      function finishProviderRead(read) {
        pendingProviderReads.delete(read.key.requestEpoch);
        read.finish();
      }

      async function afterDominatingReads(ownerKey, signal, decide) {
        for (;;) {
          const watermark = issuedProviderKey;
          const pending = [...pendingProviderReads.values()].filter(({ key }) =>
            providerKeyIsOlder(ownerKey, key) && !providerKeyIsOlder(watermark, key));
          if (pending.length) {
            await Promise.race([Promise.all(pending.map(({ done }) => done)), providerReadsDisposed]);
          }
          if (disposed || signal?.aborted) return false;
          if (!providerKeyIsOlder(watermark, issuedProviderKey)) return decide(watermark);
        }
      }

      function adoptProviderData(data, key) {
        if (providerKeyIsStale(key)) return null;
        const previous = { providers: lastProviders, options: runtimeOptions };
        lastProviders = data.providers;
        runtimeOptions = data.provider_options;
        hasConnectedProvider = data.hasConnectedProvider;
        adoptedProviderKey = key;
        live.textContent = "";
        return previous;
      }

      function detailProjection(entry, provider) {
        return JSON.stringify([
          entry && [entry.kind, entry.display_name, entry.visible, entry.connectable,
            entry.connection_mode, entry.execution_mode, entry.prefix, entry.theme_color],
          provider && [provider.id, provider.kind, provider.system,
            (provider.accounts ?? []).map((account) => [account.id, account.label, account.email, account.enabled]),
            (provider.models ?? []).map((model) => [model.model_id, model.name])],
        ]);
      }
      function reconcileCurrentView(previous) {
        if (view === "gallery") { paint(); return; }
        if (currentEnabledPanel?.control.isConnected) setEnabledError(currentEnabledPanel, false);
        const before = byKindFrom(previous.providers).get(view);
        const current = byKindFrom(lastProviders).get(view);
        const beforeEntry = previous.options.find((option) => option.kind === view);
        const currentEntry = runtimeOptions.find((option) => option.kind === view);
        const structural = !beforeEntry || !currentEntry || before?.id !== current?.id
          || beforeEntry.visible !== currentEntry.visible
          || beforeEntry.connectable !== currentEntry.connectable
          || beforeEntry.connection_mode !== currentEntry.connection_mode;
        if (structural) {
          disposeProviderConnect();
          paint();
          return;
        }
        if (detailProjection(beforeEntry, before) !== detailProjection(currentEntry, current)) {
          const reusable = currentEnabledPanel?.providerId === current?.id ? currentEnabledPanel : null;
          disposeProviderConnect();
          paint(reusable);
          return;
        }
        if (currentEnabledPanel?.kind === view && currentEnabledPanel.control.isConnected) {
          currentEnabledPanel.providerId = current?.id ?? "";
          syncEnabledPanel(currentEnabledPanel, currentEntry, current, { busy: enabledBusy });
        } else {
          paint();
        }
      }

      function renderEnabledReconciliation(before, entry, current, currentEntry, panel, failed, showInlineError) {
        if (!current || current.kind !== before.kind) {
          if (view === before.kind) {
            disposeProviderConnect();
            view = "gallery";
            paint();
          } else if (view === "gallery") {
            paint();
          }
          return;
        }
        if (view === "gallery") {
          paint();
        }
        const target = mountedEnabledPanel(current.id, panel);
        if (!target) return;
        target.providerId = current.id;
        target.kind = current.kind;
        syncEnabledPanel(target, currentEntry, current);
        setEnabledError(target, failed && showInlineError);
      }

      async function updateProviderEnabled(providerId, desired, panel, {
        signal,
        showInlineError = true,
      } = {}) {
        const before = byIDFrom(lastProviders).get(providerId);
        const entry = before && runtimeOptions.find((option) => option.kind === before.kind);
        const editable = Boolean(before && entry?.visible === true && before.system === false);
        if (disposed || enabledBusy || signal?.aborted || !editable || panel.providerId !== providerId) return false;

        const startingPanel = mountedEnabledPanel(providerId, panel);
        if (!startingPanel) return false;
        enabledBusy = true;
        const revision = ++enabledRevision;
        setEnabledError(startingPanel, false);
        syncEnabledPanel(startingPanel, entry, before, { busy: true });
        try {
          const settlementEpoch = await providerMutationHandoff.run(
            handoffOwner,
            () => durableMutationService.update(providerId, {
              name: before.name, enabled: desired, credential: "",
            }),
          );
          if (disposed || revision !== enabledRevision || signal?.aborted) return false;

          const read = beginProviderRead(settlementEpoch);
          try {
            const data = normalizeProviderRuntimeResponse(await service.list());
            if (!disposed && revision === enabledRevision && !signal?.aborted) {
              const previous = adoptProviderData(data, read.key);
              if (previous) reconcileCurrentView(previous);
            }
          } catch {
            // The stable highest-causal snapshot below decides an ambiguous read or write.
          } finally {
            finishProviderRead(read);
          }
          if (disposed || revision !== enabledRevision || signal?.aborted) return false;
          return await afterDominatingReads(read.key, signal, (frontier) => {
            const authoritative = !providerKeyIsOlder(adoptedProviderKey, frontier),
              current = lastProviders.find((provider) => provider.id === providerId);
            const exact = Boolean(current && current.kind === before.kind);
            const currentEntry = exact
              ? runtimeOptions.find((option) => option.kind === current.kind)
              : null;
            const succeeded = authoritative && exact && current.enabled === desired;
            renderEnabledReconciliation(before, entry, current, currentEntry, panel,
              !succeeded, showInlineError);
            return succeeded || !exact;
          });
        } finally {
          if (revision === enabledRevision) {
            enabledBusy = false;
            const current = byIDFrom(lastProviders).get(currentEnabledPanel?.providerId);
            const currentEntry = runtimeOptions.find((option) => option.kind === currentEnabledPanel?.kind);
            if (currentEntry && currentEnabledPanel?.control.isConnected) {
              syncEnabledPanel(currentEnabledPanel, currentEntry, current);
            }
          }
        }
      }

      function changeProviderEnabled(panel, desired) {
        const provider = byIDFrom(lastProviders).get(panel.providerId);
        const entry = provider && runtimeOptions.find((option) => option.kind === provider.kind);
        const editable = Boolean(provider && entry?.visible === true && provider.system === false);
        if (!editable || enabledBusy || desired === provider.enabled) {
          if (entry) syncEnabledPanel(panel, entry, provider);
          return;
        }
        syncEnabledPanel(panel, entry, provider);
        if (desired) {
          void updateProviderEnabled(provider.id, true, panel);
          return;
        }
        providerOffDialog.open({
          label: provider.name,
          value: { providerId: provider.id, panel },
          opener: panel.control,
          resolveFocusFallback: () => currentEnabledPanel?.control.isConnected && !currentEnabledPanel.control.disabled
            ? currentEnabledPanel.control : currentDetailBack?.isConnected ? currentDetailBack : searchBox.isConnected ? searchBox : null,
          description: `Nếu tắt ${provider.name}, Provider này sẽ không còn được dùng để trả lời. Bạn vẫn muốn tắt chứ?`,
        });
      }

      function paintGallery() {
        gallerySlot.replaceChildren(galleryNode());
      }

      function paint(reusableEnabledPanel = null) {
        if (view !== "gallery") {
          const byKind = byKindFrom(lastProviders);
          const entry = runtimeOptions.find((option) => option.kind === view
            && (option.visible || byKind.has(option.kind)));
          if (entry) {
            const provider = byKind.get(entry.kind);
            const connectable = entry.visible === true && entry.connectable === true;
            if (connectable) ensureProviderConnect(entry);
            const connUI = {
              connectable,
              connecting: providerConnectOpen && providerConnectKind === entry.kind,
              connectSlot: providerConnectSlot,
              onAdd: startConnect,
              onRemoveAccount: removeAccount,
            };
            const enabledPanel = reusableEnabledPanel?.providerId === (provider?.id ?? "")
              && reusableEnabledPanel.kind === entry.kind
              ? reusableEnabledPanel : createEnabledPanel(entry, provider, changeProviderEnabled);
            const backControl = element("button", { className: "pv-back", attributes: { type: "button" }, text: "Về Providers", on: { click: backToGallery } });
            enabledPanel.control.setAttribute("aria-label", `Bật hoặc tắt ${entry.display_name}`);
            syncEnabledPanel(enabledPanel, entry, provider, { busy: enabledBusy });
            root.replaceChildren(header(), live, renderDetail(entry, byKind, backControl, connUI, enabledPanel));
            currentEnabledPanel = enabledPanel; currentDetailBack = backControl;
            return;
          }
          disposeProviderConnect();
          view = "gallery";
        }
        currentEnabledPanel = null; currentDetailBack = null;
        paintGallery();
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

      async function refresh(settlementEpoch = providerMutationHandoff.currentEpoch(), {
        background = false,
      } = {}) {
        const read = beginProviderRead(settlementEpoch);
        if (!background) live.textContent = "Đang đọc danh sách Provider…";
        if (!background && view === "gallery") root.replaceChildren(header(), toolbar());
        try {
          const data = normalizeProviderRuntimeResponse(await service.list());
          if (disposed) return;
          const previous = adoptProviderData(data, read.key);
          if (previous) reconcileCurrentView(previous);
        } catch (error) {
          if (disposed || providerKeyIsStale(read.key) || error?.name === "AbortError") return;
          if (background) {
            live.textContent = "Chưa thể cập nhật danh sách Provider. Dữ liệu hiện tại vẫn được giữ nguyên.";
            return;
          }
          live.textContent = "";
          root.replaceChildren(header(), toolbar(), errorPanel(error));
        } finally {
          finishProviderRead(read);
        }
      }

      void refresh();
      return {
        dispose() {
          disposed = true;
          enabledRevision++;
          pendingProviderReads.clear();
          releaseProviderReads();
          unsubscribeHandoff();
          providerOffDialog.dispose();
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
