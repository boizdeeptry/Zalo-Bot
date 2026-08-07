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
// CONNECTABLE_KINDS mirrors the backend subscriptionKinds: only these can run the in-Portal login
// flow today. claude-code joins when its kind is unified and it routes through cliAdapter.
const CONNECTABLE_KINDS = new Set(["codex"]);
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
    connectStart: (kind, label) => request(`/llm/providers/${encodeURIComponent(kind)}/connect`, {
      method: "POST", body: { label },
    }),
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
    group("Gói thuê bao — CLI chính chủ", sub, byKind, onOpen, headFor(sub, byKind, true)),
    group("API Key", api, byKind, onOpen, headFor(api, byKind, false)));
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
  } else {
    body = element("div", { className: "pv-conn-empty", text: "Chưa có kết nối — No connections yet." });
  }
  // Label switches to "+ Thêm account" once at least one account is connected — same startConnect
  // flow underneath (ui.onAdd), just numbered for the next account via defaultLabel().
  const addText = accounts.length ? "+ Thêm account" : "+ Thêm kết nối";
  const add = ui.connectable
    ? element("button", {
        className: "pv-btn primary",
        // Disabled while a connect flow for this entry is already open — a second click can't
        // fire a second runConnect (belt, alongside the connectRun guard in runConnect itself).
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
    entry.group === "subscription" ? safeBadge() : null,
    renderConnections(entry, p, connUI),
    renderModels(entry, p),
  );
}

export function createProvidersPage({ request = requestJSON, pollMs = 1500 } = {}) {
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
      // Rời khỏi detail (mở kind khác, hoặc quay lại gallery) phải chấm dứt một luồng connect đang
      // chạy — bump connectRun ở đây để vòng lặp poll của runConnect tự thấy mình lỗi thời ở lượt
      // kiểm kế tiếp và dừng lại, không tiếp tục refresh() nền sau khi người dùng đã rời trang.
      const openDetail = (kind) => { connectRun++; connect = null; view = kind; paint(); };
      const backToGallery = () => { connectRun++; connect = null; view = "gallery"; paint(); };

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

      // connect cầm trạng thái luồng đăng nhập in-Portal cho MỘT kind tại một thời điểm — null khi
      // panel đang đóng. connectSlot là node ổn định giống gallerySlot: paintConnect() chỉ thay nội
      // dung của nó, không đụng root, nên không làm mất focus ô nhập tên tài khoản.
      let connect = null; // { kind, phase, label, message?, loginUrl? }
      // connectRun là token đơn điệu: MỖI lượt mở panel (startConnect) hoặc rời detail (openDetail/
      // backToGallery) tăng nó lên một. runConnect chụp giá trị của mình vào myRun lúc bắt đầu; mọi
      // điểm kiểm "đây còn phải là lượt của tôi không" so connectRun !== myRun thay vì so connect?.kind
      // !== kind — kind không đổi khi bấm lại nút hay điều hướng, nên phép so sánh cũ không bắt được
      // vòng lặp cũ vẫn đang chạy nền.
      let connectRun = 0;
      const connectSlot = element("div", { className: "pv-connect" });
      const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
      const defaultLabel = (p) => `Tài khoản ${(p?.accounts?.length ?? 0) + 1}`;

      // startConnect mở panel ở chế độ "prompt" (nhập tên + bấm Bắt đầu). Đây là hàm mà nút
      // "Thêm account" ở trang Accounts (việc sau) cũng sẽ gọi lại — nên export ra ngoài paint().
      function startConnect(kind) {
        const p = byKindFrom(lastProviders).get(normalizeKind(kind));
        connectRun++; // invalidate any prior run before opening a fresh prompt
        connect = { kind, phase: "prompt", label: defaultLabel(p) };
        paint();
        paintConnect();
      }

      function closeConnect() {
        connect = null;
        paint();
      }

      // removeAccount deletes one account off a provider then refreshes — same shape as the
      // model-removal path, no local optimistic state to keep in sync.
      async function removeAccount(providerId, accountId) {
        await service.removeAccount(providerId, accountId);
        await refresh();
      }

      async function cancelConnect(kind) {
        await service.connectCancel(kind);
        // Nếu vòng lặp poll trong runConnect chưa kịp quan sát "canceled" (ví dụ đang await sleep),
        // tự vẽ trạng thái đã huỷ ngay — người dùng không phải chờ tick kế tiếp mới thấy phản hồi.
        if (connect?.kind === kind && connect.phase !== "canceled") {
          connect = { ...connect, phase: "canceled" };
          paintConnect();
        }
      }

      function phaseLabel(phase, message) {
        switch (phase) {
          case "detecting": return "Đang kiểm tra…";
          case "installing": return message || "Đang cài…";
          case "awaiting_login": return "Chờ đăng nhập trong trình duyệt…";
          case "polling": return "Đang xác nhận đăng nhập…";
          case "connected": return "Đã kết nối.";
          default: return message || "";
        }
      }

      // paintConnect chỉ vẽ lại connectSlot — cùng nguyên tắc với paintGallery().
      function paintConnect() {
        if (!connect) { connectSlot.replaceChildren(); return; }
        if (connect.phase === "prompt") {
          const input = element("input", { className: "pv-connect-label",
            attributes: { type: "text", value: connect.label, "aria-label": "Tên tài khoản" } });
          connectSlot.replaceChildren(element("div", { className: "pv-connect-prompt" },
            input,
            element("button", { className: "pv-btn primary", attributes: { type: "button" },
              text: "Bắt đầu kết nối",
              on: { click() { void runConnect(connect.kind, input.value); } } }),
            element("button", { className: "pv-btn", attributes: { type: "button" },
              text: "Huỷ", on: { click: closeConnect } }),
          ));
          return;
        }
        if (connect.phase === "error" || connect.phase === "canceled") {
          connectSlot.replaceChildren(element("div", { className: "pv-connect-status" },
            element("div", { className: "pv-connect-message",
              text: connect.message || (connect.phase === "canceled" ? "Đã huỷ kết nối." : "Kết nối thất bại.") }),
            element("button", { className: "pv-btn", attributes: { type: "button" },
              text: "Đóng", on: { click: closeConnect } }),
          ));
          return;
        }
        // Đang chạy: detecting / installing / awaiting_login / polling. Chỉ dựng <a href> khi URL
        // thật là https:// — một backend hỏng hay giả mạo trả về javascript:/data: không được phép
        // trở thành một liên kết bấm được trong trang.
        const loginLink = connect.phase === "awaiting_login" && connect.loginUrl?.startsWith("https://")
          ? element("a", { className: "pv-btn primary",
              attributes: { href: connect.loginUrl, target: "_blank", rel: "noopener" },
              text: "Mở trang đăng nhập" })
          : null;
        connectSlot.replaceChildren(element("div", { className: "pv-connect-status" },
          element("div", { className: "pv-connect-message", text: phaseLabel(connect.phase, connect.message) }),
          loginLink,
          element("button", { className: "pv-btn", attributes: { type: "button" },
            text: "Huỷ", on: { click() { void cancelConnect(connect.kind); } } }),
        ));
      }

      // runConnect lái luồng: POST bắt đầu, rồi GET lặp lại tới khi connected/error/canceled. Mỗi
      // vòng kiểm connectRun !== myRun để tự dừng — KHÔNG connect?.kind !== kind: kind không đổi
      // khi bấm lại nút hay điều hướng sang detail khác rồi quay lại cùng kind, nên phép so sánh
      // theo kind bỏ lọt đúng ca gây lỗi (vòng lặp cũ chạy nền, đè trạng thái của lượt mới).
      async function runConnect(kind, label) {
        const myRun = ++connectRun;
        connect = { kind, phase: "detecting", label };
        paintConnect();
        try {
          await service.connectStart(kind, label);
          for (;;) {
            if (disposed || connectRun !== myRun) return;
            const st = await service.connectStatus(kind);
            if (disposed || connectRun !== myRun) return;
            connect = { ...connect, phase: st.phase, message: st.message, loginUrl: st.loginUrl || connect.loginUrl };
            paintConnect();
            if (st.phase === "connected") { connect = null; await refresh(); return; }
            if (st.phase === "error" || st.phase === "canceled") return;
            await sleep(pollMs);
          }
        } catch (error) {
          // Lượt cũ/lỗi muộn không được đè connect: {...null, phase:"error"} sinh ra một đối tượng
          // thiếu kind là đúng thứ lỗi cần chặn.
          if (disposed || connectRun !== myRun || error?.name === "AbortError") return;
          connect = { ...connect, phase: "error", message: error?.message || "Kết nối thất bại" };
          paintConnect();
        }
      }

      // headFor đóng bao service/refresh của mount() — group-head cần gọi testSaved() và refresh()
      // thật, còn renderGallery thì không nên biết tới hai thứ đó để vẫn là hàm thuần trên tham số.
      const headFor = (groupEntries, byKind, isSubscription) => element("span", { className: "pv-head" },
        isSubscription ? safeTag() : null,
        testAllButton(groupEntries, byKind, service, refresh));

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
            const connUI = {
              connectable: entry.group === "subscription" && CONNECTABLE_KINDS.has(entry.kind),
              connecting: connect?.kind === entry.kind,
              connectSlot,
              onAdd: startConnect,
              onRemoveAccount: removeAccount,
            };
            root.replaceChildren(header(), renderDetail(entry, byKindFrom(lastProviders), backToGallery, connUI));
            // Slot node vẽ lại ở paint() mất nội dung cũ — refill nếu panel đang mở cho đúng kind này.
            if (connect?.kind === entry.kind) paintConnect();
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
        // Chỉ vẽ toolbar gallery khi đang ở gallery — reload lúc đang xem detail không được nháy
        // ngược về toolbar gallery rồi mới vẽ lại detail ở paint().
        if (view === "gallery") root.replaceChildren(header(), toolbar());
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
