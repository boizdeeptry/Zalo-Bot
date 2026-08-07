# Providers gallery + trang chi tiết (UI kiểu 9Router) — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Thay facts-list + sheet của trang Providers bằng **gallery card chia nhóm + trang chi tiết** kiểu 9Router (badge xanh "chính chủ an toàn" thay Risk Notice), đọc state thật từ `/llm/*`. Thuần frontend.

**TDD mode:** yes

**Architecture:** Viết lại `pages/providers.js` — giữ `createProviderService` (API) và contract `createProvidersPage({request}).mount(container) → {dispose}`, nhưng phần render đổi từ list+sheet sang **gallery** (catalog frontend + status thật) và **detail** (điều hướng hash `#providers/<kind>`). Thao tác GHI (connect, add model, combos) hiển thị nhưng **deferred #2–#4**; thao tác ĐỌC (list, test, discover) chạy thật qua service sẵn có.

**Tech stack:** Vanilla ES modules (không framework). Test: `node:test` + `appmode/tests/helpers/dom-harness.mjs`. UI helpers: `core/ui.js` (`element`, `pageHeader`, `errorPanel`), `core/api.js` (`requestJSON`). Chạy test: `npm --prefix appmode test`. Cổng đóng: `build-app.ps1` (typecheck + package) phải xanh.

**Spec:** `.planning/specs/2026-08-07-providers-gallery-ui-design.md`

**Research:** skipped — thay đổi frontend nhỏ theo đúng khuôn Portal đã có.

**Sub-project:** #1/4 của "Providers UI kiểu 9Router". #2 connect, #3 multi-account, #4 combos sau, cùng nhánh.

---

## File structure

- **Sửa (viết lại phần render):** `appmode/overlay/internal/webui/static/pages/providers.js`
  - GIỮ: `createProviderService(request)` (không đổi — #2/#4 dùng lại các method ghi), contract `createProvidersPage({request}).mount → {dispose}`, `mount(container)` export.
  - THÊM: `PROVIDER_CATALOG` (hằng 6 provider + nhóm/logo/prefix), `renderGallery`, `renderDetail`, router trong trang, search.
  - BỎ: `providerSheet` và facts-list (`providerFacts`) — chức năng ghi chuyển sang #2/#4.
- **Sửa:** `appmode/overlay/internal/webui/static/portal.css` — thêm class `pv-*` (gallery/detail/card/badge) theo mock; scope trong vùng Provider của trang (giữ test "CSS scoped per page" xanh). *Nếu style provider của Task 7 nằm inline trong `providers.js`, để CSS mới cùng chỗ đó cho nhất quán.*
- **Sửa (viết lại):** `appmode/tests/providers.test.mjs` — thay bộ test list+sheet bằng bộ test gallery+detail (bên dưới). **GIỮ NGUYÊN** test cuối `no embedded Portal asset carries an API-key literal` (cửa chặn bảo mật) và test `provider service issues the documented paths` (service không đổi).
- **Không đụng:** `models.js`, backend, seam build, `core/*`.

### DOM contract (class dùng chung giữa impl và test)

Gallery: `.providers-page` (root) › `.pv-search` (chứa `input`) › `.pv-group` (mỗi nhóm) › `.pv-group-head` (nhóm subscription có `.pv-safe-tag`; có nút "Kiểm tra tất cả") › `.pv-grid` › `.pv-card` (chứa `.pv-logo`, `.pv-name`, `.pv-status` + `.on`|`.off`).
Detail: `.pv-detail` › `.pv-back` (nút "Về Providers") › `.pv-detail-head` (`.pv-name`) › `.pv-safe-badge` › `.pv-panel.pv-connections` › `.pv-panel.pv-models` (`.pv-model` › `.pv-mid`).

### Catalog (dùng ở nhiều task — định nghĩa trong providers.js)

```js
// PROVIDER_CATALOG là bộ provider HIỂN THỊ (focused-Zalo), không phải danh sách động.
// `kind` khớp p.Kind engine dùng để đối chiếu GET /llm/providers. `group` phân 2 khu.
// `prefix` chỉ để hiện id model kiểu 9Router (cosmetic ở #1).
export const PROVIDER_CATALOG = [
  { kind: "claude-code", name: "Claude Code", group: "subscription", prefix: "cc", logoColor: "#c8613b" },
  { kind: "codex", name: "OpenAI Codex", group: "subscription", prefix: "cx", logoColor: "#0f7a63" },
  { kind: "openai", name: "OpenAI", group: "apikey", prefix: "oa", logoColor: "#10a37f" },
  { kind: "anthropic", name: "Anthropic", group: "apikey", prefix: "an", logoColor: "#c8613b" },
  { kind: "gemini", name: "Gemini", group: "apikey", prefix: "gm", logoColor: "#3f6ff5" },
  { kind: "openrouter", name: "OpenRouter", group: "apikey", prefix: "or", logoColor: "#5b5ef0" },
];

// Engine seed claude-code với kind "claude_code" (gạch dưới) trong hàng system; catalog dùng
// "claude-code" (gạch nối) cho khớp cliDescriptors mới. Đối chiếu chuẩn hoá cả hai.
const normalizeKind = (k) => String(k ?? "").replace(/_/g, "-");
```

---

### Task 1: Gallery vẽ catalog 6 provider, chia 2 nhóm, phủ trạng thái thật

**Files:**
- Modify: `appmode/overlay/internal/webui/static/pages/providers.js` (thêm `PROVIDER_CATALOG`, `normalizeKind`, `renderGallery`; đổi `refresh` để vẽ gallery)
- Test: `appmode/tests/providers.test.mjs` (viết lại từ đầu — task này đặt nền harness mới)

**Public behavior to verify:** Mount trang → hiện đúng 6 card catalog trong 2 nhóm; provider có trong `GET /llm/providers` (đã thêm) hiện trạng thái từ dữ liệu, provider chưa có hiện "Chưa kết nối".

- [ ] **Step 1: Viết test đỏ**

  Thay toàn bộ `providers.test.mjs`. Giữ `import`/`installDOM`/`mountPage`/`flush`/`hasClass`/`button`/`find`/`findAll`/`text` như cũ; đổi fixture + bỏ helper sheet. Bộ mới bắt đầu:

  ```js
  import test from "node:test";
  import assert from "node:assert/strict";
  import { readdir, readFile } from "node:fs/promises";
  import { join } from "node:path";
  import { createProviderService, createProvidersPage } from "../overlay/internal/webui/static/pages/providers.js";
  import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

  const staticRoot = new URL("../overlay/internal/webui/static/", import.meta.url);
  const flush = () => new Promise((r) => setImmediate(r));
  const hasClass = (n, c) => n.classList?.contains(c) ?? false;
  const cards = (main) => findAll(main, (n) => hasClass(n, "pv-card"));
  const cardName = (card) => text(find(card, (n) => hasClass(n, "pv-name")));

  // GET /llm/providers giả: claude-code đã seed (system), phần còn lại chưa thêm.
  const CLAUDE_ADDED = {
    id: "claude-code", name: "Claude Code", kind: "claude_code", system: true, enabled: true,
    credential_configured: false, credential_unreadable: false, last_check_status: "", last_error: "",
    models: [{ model_id: "sonnet", name: "Claude Sonnet", source: "manual", available: true }],
  };
  function mountPage(t, handler) {
    const dom = installDOM();
    const calls = [];
    const page = createProvidersPage({ request: async (path, options = {}) => {
      const { signal, ...recorded } = options; calls.push({ path, options: recorded }); return handler(path, options);
    }});
    const main = document.createElement("main");
    const mounted = page.mount(main);
    t.after(() => { mounted.dispose(); dom.restore(); });
    return { calls, main, mounted };
  }
  const listWith = (providers) => (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return { providers, kinds: [] };
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  };

  test("gallery renders the 6-provider catalogue in two groups", async (t) => {
    const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
    await flush();
    assert.deepEqual(cards(main).map(cardName),
      ["Claude Code", "OpenAI Codex", "OpenAI", "Anthropic", "Gemini", "OpenRouter"]);
    const groups = findAll(main, (n) => hasClass(n, "pv-group"));
    assert.equal(groups.length, 2);
  });

  test("an added provider shows connected-ish status, an unlisted one shows not connected", async (t) => {
    const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
    await flush();
    const claude = cards(main).find((c) => cardName(c) === "Claude Code");
    const codex = cards(main).find((c) => cardName(c) === "OpenAI Codex");
    assert.ok(hasClass(find(claude, (n) => hasClass(n, "pv-status")), "on"));
    assert.ok(hasClass(find(codex, (n) => hasClass(n, "pv-status")), "off"));
    assert.match(text(codex), /Chưa kết nối/);
  });
  ```

- [ ] **Step 2: Chạy đỏ**

  Run: `npm --prefix appmode test`
  Expected: FAIL — `pv-card` chưa tồn tại (trang còn vẽ `.fact`), `cards(main)` rỗng → assert độ dài sai.

- [ ] **Step 3: Cài đặt tối thiểu**

  Trong `providers.js`: thêm `PROVIDER_CATALOG` + `normalizeKind` (khối ở trên). Thêm `renderGallery`, và đổi `refresh()` để gọi nó thay cho `providerFacts`:

  ```js
  // matchState đối chiếu catalog với provider ĐÃ thêm (theo kind chuẩn hoá).
  function galleryStatus(entry, byKind) {
    const p = byKind.get(entry.kind);
    if (!p) return { cls: "off", label: "Chưa kết nối" };
    if (p.credential_unreadable) return { cls: "off", label: "Cần đăng nhập lại" };
    if (p.last_check_status === "ok") return { cls: "on", label: "Đã kết nối" };
    if (p.system) return { cls: "on", label: "Sẵn sàng" }; // claude-code seed, chạy cục bộ
    if (!p.system && !p.credential_configured) return { cls: "off", label: "Chưa kết nối" };
    return { cls: "on", label: "Đã thêm" };
  }

  function card(entry, byKind, onOpen) {
    const st = galleryStatus(entry, byKind);
    return element("button", {
      className: "pv-card",
      attributes: { type: "button", "aria-label": `Mở ${entry.name}` },
      on: { click() { onOpen(entry.kind); } },
    },
      element("span", { className: "pv-logo", attributes: { style: `background:${entry.logoColor}` }, text: entry.prefix.toUpperCase() }),
      element("span", { className: "pv-meta" },
        element("span", { className: "pv-name", text: entry.name }),
        element("span", { className: `pv-status ${st.cls}`, text: st.label }),
      ),
    );
  }

  function group(title, entries, byKind, onOpen, extraHead = null) {
    return element("section", { className: "pv-group" },
      element("div", { className: "pv-group-head" }, element("h2", { text: title }), extraHead),
      element("div", { className: "pv-grid" }, entries.map((e) => card(e, byKind, onOpen))),
    );
  }

  function renderGallery(providers, onOpen) {
    const byKind = new Map(providers.map((p) => [normalizeKind(p.kind), p]));
    const sub = PROVIDER_CATALOG.filter((e) => e.group === "subscription");
    const api = PROVIDER_CATALOG.filter((e) => e.group === "apikey");
    return element("div", { className: "pv-gallery" },
      group("Gói thuê bao — CLI chính chủ", sub, byKind, onOpen),
      group("API Key", api, byKind, onOpen),
    );
  }
  ```

  Trong `mount`, đổi phần `refresh` thành công để dựng gallery (thay `providerFacts(...)`):

  ```js
  root.replaceChildren(header(), toolbar(), renderGallery(providers, openDetail));
  ```

  Tạm khai báo `const openDetail = (kind) => { /* Task 4 */ };` (no-op) để Task 1 chạy; Task 4 sẽ nối điều hướng. Xoá `providerFacts`, `providerSheet`, `openSheet` và mọi tham chiếu (chúng thuộc luồng ghi #2/#4). Bỏ `toolbar()` cũ (Thêm Provider/Tải lại) — Task 3 thay bằng search + Tải lại nhẹ; tạm giữ một nút "Tải lại" gọi `refresh` để không mất reload.

- [ ] **Step 4: Chạy xanh.** `npm --prefix appmode test` → 2 test mới PASS (+ test service/canary còn giữ).

- [ ] **Step 5: Refactor** — tách `galleryStatus/card/group/renderGallery` thành các hàm nhỏ thuần (đã vậy). Không thêm gì test chưa đòi.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/tests/providers.test.mjs
  git commit -m "feat: providers gallery renders the focused catalogue with real status"
  ```

---

### Task 2: Badge xanh "chính chủ" ở nhóm subscription, không còn Risk Notice

**Files:**
- Modify: `pages/providers.js` (`group` cho nhóm subscription kèm `.pv-safe-tag`)
- Test: `appmode/tests/providers.test.mjs`

**Public behavior to verify:** Nhóm "Gói thuê bao" có badge xanh an toàn; toàn trang KHÔNG có chuỗi "Risk Notice"/"banned"/"restricted".

- [ ] **Step 1: Viết test đỏ**

  ```js
  test("the subscription group carries the green official-CLI safety badge, never a ban warning", async (t) => {
    const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
    await flush();
    const tag = find(main, (n) => hasClass(n, "pv-safe-tag"));
    assert.ok(tag, "subscription group must show the safety badge");
    assert.match(text(tag), /chính chủ/i);
    for (const bad of ["Risk Notice", "banned", "restricted", "proxy/router"]) {
      assert.ok(!text(main).includes(bad), `gallery must not contain "${bad}"`);
    }
  });
  ```

- [ ] **Step 2: Chạy đỏ** — `.pv-safe-tag` chưa có → `find` trả null → assert.ok fail.

- [ ] **Step 3: Cài đặt** — truyền `extraHead` cho nhóm subscription:

  ```js
  const safeTag = () => element("span", { className: "pv-safe-tag" },
    element("span", { className: "pv-dot" }),
    element("span", { text: "Chính chủ · không rủi ro khoá" }),
  );
  // trong renderGallery:
  group("Gói thuê bao — CLI chính chủ", sub, byKind, onOpen, safeTag()),
  ```

- [ ] **Step 4: Chạy xanh.**

- [ ] **Step 5: Refactor** — không cần.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/tests/providers.test.mjs
  git commit -m "feat: green official-CLI safety badge replaces 9Router's ban notice"
  ```

---

### Task 3: Search lọc card theo tên

**Files:**
- Modify: `pages/providers.js` (ô search + lọc client-side)
- Test: `appmode/tests/providers.test.mjs`

**Public behavior to verify:** Gõ vào ô search → chỉ còn card có tên khớp (không phân biệt hoa/thường); nhóm rỗng thì ẩn.

- [ ] **Step 1: Viết test đỏ**

  ```js
  test("typing in the search box filters cards by name", async (t) => {
    const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
    await flush();
    const box = find(main, (n) => n.tagName === "INPUT" && n.getAttribute?.("type") === "search");
    assert.ok(box);
    box.value = "codex";
    box.dispatchEvent({ type: "input" });
    assert.deepEqual(cards(main).map(cardName), ["OpenAI Codex"]);
  });
  ```

- [ ] **Step 2: Chạy đỏ** — chưa có input `type=search`.

- [ ] **Step 3: Cài đặt** — giữ `query` ở state trang; ô search gọi lại render gallery đã lọc. Trong `mount`:

  ```js
  let query = "";
  const searchBox = element("input", { className: "pv-search-input",
    attributes: { type: "search", placeholder: "Tìm provider…", "aria-label": "Tìm provider" },
    on: { input(e) { query = e.currentTarget.value.trim().toLowerCase(); paintGallery(); } },
  });
  const searchBar = () => element("div", { className: "pv-search" }, searchBox);

  // paintGallery vẽ lại chỉ phần gallery từ danh sách provider gần nhất + query, không refetch.
  const byKindFrom = (providers) => new Map(providers.map((p) => [normalizeKind(p.kind), p]));
  let lastProviders = [];
  function galleryNode() {
    const filtered = query
      ? PROVIDER_CATALOG.filter((e) => e.name.toLowerCase().includes(query))
      : PROVIDER_CATALOG;
    return renderGallery(filtered, byKindFrom(lastProviders), openDetail);
  }
  ```

  Đổi chữ ký `renderGallery(entries, byKind, onOpen)` — nhận **catalog đã lọc** + `byKind` dựng sẵn từ `byKindFrom` (KHÔNG tự dựng map bên trong nữa; bỏ dòng `const byKind = new Map(...)` trong `renderGallery` của Task 1). Cập nhật `refresh` để lưu `lastProviders = providers` rồi `paintGallery()`; `paintGallery` thay riêng node gallery trong `root`. Giữ `searchBar()` trong header vùng.

- [ ] **Step 4: Chạy xanh.**

- [ ] **Step 5: Refactor** — đảm bảo search KHÔNG gọi lại API (chỉ lọc `PROVIDER_CATALOG` + dùng `lastProviders`). Thêm ca test phụ nếu muốn: gõ rồi xoá → 6 card trở lại.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/tests/providers.test.mjs
  git commit -m "feat: client-side search filters the provider gallery"
  ```

---

### Task 4: Điều hướng gallery → detail → back, header + badge trên detail

**Files:**
- Modify: `pages/providers.js` (`renderDetail`, router `openDetail`/back)
- Test: `appmode/tests/providers.test.mjs`

**Public behavior to verify:** Bấm một card → hiện `.pv-detail` của provider đó (tên đúng + badge xanh); bấm "Về Providers" → về gallery.

- [ ] **Step 1: Viết test đỏ**

  ```js
  test("clicking a card opens its detail; back returns to the gallery", async (t) => {
    const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
    await flush();
    cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
    await flush();
    const detail = find(main, (n) => hasClass(n, "pv-detail"));
    assert.ok(detail);
    assert.equal(text(find(detail, (n) => hasClass(n, "pv-name"))), "OpenAI Codex");
    assert.ok(find(detail, (n) => hasClass(n, "pv-safe-badge")));
    find(detail, (n) => hasClass(n, "pv-back")).click();
    await flush();
    assert.ok(find(main, (n) => hasClass(n, "pv-gallery")));
    assert.equal(find(main, (n) => hasClass(n, "pv-detail")), null);
  });
  ```

- [ ] **Step 2: Chạy đỏ** — `openDetail` là no-op → không có `.pv-detail`.

- [ ] **Step 3: Cài đặt** — thêm state `view` (`"gallery"` | `{kind}`) và `renderDetail`. `openDetail(kind)` đặt view rồi vẽ; back đặt về gallery:

  ```js
  function detailHead(entry, byKind) {
    const p = byKind.get(entry.kind);
    const count = p && Array.isArray(p.models) ? `${p.models.length} model` : "0 kết nối";
    return element("div", { className: "pv-detail-head" },
      element("span", { className: "pv-logo lg", attributes: { style: `background:${entry.logoColor}` }, text: entry.prefix.toUpperCase() }),
      element("div", null, element("div", { className: "pv-name", text: entry.name }),
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
      // Connections + Models: Task 5
    );
  }
  ```

  Router trong `mount`:

  ```js
  let view = "gallery";
  const openDetail = (kind) => { view = kind; paint(); };
  const backToGallery = () => { view = "gallery"; paint(); };
  function paint() {
    const byKind = byKindFrom(lastProviders);
    if (view === "gallery") { root.replaceChildren(header(), searchBar(), galleryNode()); return; }
    const entry = PROVIDER_CATALOG.find((e) => e.kind === view);
    if (!entry) { view = "gallery"; paint(); return; }
    root.replaceChildren(header(), renderDetail(entry, byKind, backToGallery));
  }
  ```

  Gộp `paintGallery` (Task 3) và `paint` làm một: `paint()` là điểm vẽ duy nhất theo `view`. `refresh()` set `lastProviders` rồi `paint()`.

- [ ] **Step 4: Chạy xanh.** Kiểm luôn các test Task 1–3 vẫn xanh (gallery vẫn vẽ khi `view==="gallery"`).

- [ ] **Step 5: Refactor** — một hàm `paint()` theo `view`; không hash-route thật ở #1 (điều hướng nội bộ đủ cho test và trang; hash sync để #2 nếu cần deep-link).

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/tests/providers.test.mjs
  git commit -m "feat: navigate from gallery card to a provider detail view"
  ```

---

### Task 5: Detail — panel Kết nối (status / No connections + Add Connection deferred) và lưới Model

**Files:**
- Modify: `pages/providers.js` (`renderConnections`, `renderModels` trong detail)
- Test: `appmode/tests/providers.test.mjs`

**Public behavior to verify:** Provider đã thêm hiện Available Models dạng `<prefix>/<modelId>`; provider chưa thêm hiện "No connections yet" + nút "Thêm kết nối" **disabled**; nút ghi (Add model…) disabled ở #1.

- [ ] **Step 1: Viết test đỏ**

  ```js
  test("detail lists available models with the 9Router-style prefix", async (t) => {
    const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
    await flush();
    cards(main).find((c) => cardName(c) === "Claude Code").click();
    await flush();
    const detail = find(main, (n) => hasClass(n, "pv-detail"));
    assert.deepEqual(findAll(detail, (n) => hasClass(n, "pv-mid")).map(text), ["cc/sonnet"]);
  });

  test("an unconnected provider shows an empty connections panel with a disabled add button", async (t) => {
    const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
    await flush();
    cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
    await flush();
    const detail = find(main, (n) => hasClass(n, "pv-detail"));
    assert.match(text(find(detail, (n) => hasClass(n, "pv-connections"))), /No connections yet|Chưa có kết nối/);
    const add = find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n)));
    assert.ok(add && add.disabled, "Add Connection is present but disabled in #1");
  });
  ```

- [ ] **Step 2: Chạy đỏ** — chưa có `.pv-connections`/`.pv-mid`.

- [ ] **Step 3: Cài đặt** — thêm hai panel vào `renderDetail`:

  ```js
  function renderConnections(entry, p) {
    const body = p && p.system
      ? element("div", { className: "pv-conn-row", text: `Chạy cục bộ trên máy này (${entry.name}).` })
      : element("div", { className: "pv-conn-empty", text: "Chưa có kết nối — No connections yet." });
    const add = element("button", {
      className: "pv-btn primary", attributes: { type: "button", disabled: true, title: "Có ở bước Connect (#2)" },
      text: "+ Thêm kết nối",
    });
    return element("section", { className: "pv-panel pv-connections" },
      element("div", { className: "pv-panel-head" }, element("h3", { text: "Kết nối" })),
      body, add,
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
    const addModel = element("button", { className: "pv-btn", attributes: { type: "button", disabled: true, title: "Có ở Combos (#4)" }, text: "+ Thêm model" });
    return element("section", { className: "pv-panel pv-models" },
      element("div", { className: "pv-panel-head" }, element("h3", { text: "Model khả dụng" })),
      grid, addModel,
    );
  }
  ```

  Nối vào `renderDetail` sau `safeBadge()`: `renderConnections(entry, byKind.get(entry.kind))`, `renderModels(entry, byKind.get(entry.kind))`.

- [ ] **Step 4: Chạy xanh.**

- [ ] **Step 5: Refactor** — nút deferred đều `disabled` + `title` chỉ rõ bước sau; không gắn handler ghi nào ở #1.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/tests/providers.test.mjs
  git commit -m "feat: provider detail shows connections panel and prefixed model grid"
  ```

---

### Task 6: "Kiểm tra tất cả" chạy thật (provider đã thêm) + lỗi list không để trắng

**Files:**
- Modify: `pages/providers.js` (nút "Kiểm tra tất cả" mỗi nhóm; nhánh lỗi của `refresh`)
- Test: `appmode/tests/providers.test.mjs`

**Public behavior to verify:** "Kiểm tra tất cả" gọi `POST /llm/providers/{id}/test` cho các provider ĐÃ thêm rồi cập nhật trạng thái card; `GET /llm/providers` lỗi → hiện `errorPanel` (`.banner`), không để trắng.

- [ ] **Step 1: Viết test đỏ**

  ```js
  test("Test all runs the saved-provider test and refreshes status", async (t) => {
    let checked = false;
    const { calls, main } = mountPage(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method)
        return { providers: [{ ...CLAUDE_ADDED, last_check_status: checked ? "ok" : "" }], kinds: [] };
      if (path === "/llm/providers/claude-code/test" && options.method === "POST") { checked = true; return { ok: true }; }
      throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
    });
    await flush();
    const testAll = find(main, (n) => n.tagName === "BUTTON" && /Kiểm tra tất cả/.test(text(n)));
    testAll.click();
    await flush();
    assert.ok(calls.some((c) => c.path === "/llm/providers/claude-code/test"));
  });

  test("a failed provider list renders the shared error panel, never a blank page", async (t) => {
    const { main } = mountPage(t, () => { throw new Error("không đọc được danh sách Provider"); });
    await flush();
    assert.match(text(find(main, (n) => hasClass(n, "banner"))), /không đọc được danh sách Provider/);
  });
  ```

- [ ] **Step 2: Chạy đỏ** — chưa có nút "Kiểm tra tất cả"; nhánh lỗi cần giữ `errorPanel`.

- [ ] **Step 3: Cài đặt** — thêm nút vào `pv-group-head` (chạy `service.testSaved` cho provider đã thêm trong nhóm, rồi `refresh`); giữ nhánh catch của `refresh` dựng `errorPanel(error)` (đã có sẵn từ code cũ — bảo đảm không bị xoá khi thay `providerFacts`):

  ```js
  function testAllButton(entries, byKind) {
    const btn = element("button", { className: "pv-btn", attributes: { type: "button" }, text: "Kiểm tra tất cả" });
    btn.addEventListener("click", async () => {
      btn.disabled = true;
      try {
        await Promise.allSettled(entries.map((e) => {
          const p = byKind.get(e.kind);
          return p && !p.system ? service.testSaved(p.id) : Promise.resolve();
        }));
        await refresh();
      } finally { btn.disabled = false; }
    });
    return btn;
  }
  ```

  Truyền `testAllButton(sub, byKind)` / `testAllButton(api, byKind)` làm `extraHead` (nhóm subscription vẫn kèm `safeTag()` — bọc cả hai trong một span). Nhánh lỗi của `refresh` giữ:

  ```js
  } catch (error) {
    if (disposed || revision !== listRevision || error?.name === "AbortError") return;
    live.textContent = "";
    root.replaceChildren(header(), searchBar(), errorPanel(error));
  }
  ```

- [ ] **Step 4: Chạy xanh.** Toàn bộ test Task 1–6 xanh.

- [ ] **Step 5: Refactor** — `Promise.allSettled` cho test loạt (một provider hỏng không chặn phần còn lại). `testSaved` bỏ qua provider system (claude-code chạy cục bộ, `test` của nó là auth-probe — vẫn gọi được; nếu backend cho phép thì bỏ điều kiện `!p.system`).

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/webui/static/pages/providers.js appmode/tests/providers.test.mjs
  git commit -m "feat: Test-all runs real provider tests; list errors never blank the page"
  ```

---

### Task 7: CSS dark gallery/detail + cổng build + dọn test cũ

**Files:**
- Modify: `appmode/overlay/internal/webui/static/portal.css` (class `pv-*`)
- Modify: `appmode/tests/providers.test.mjs` (xác nhận test canary + service còn nguyên; xoá mọi test sheet còn sót)
- Test: chạy full Portal + `build-app.ps1`

**Reason:** CSS không đổi hành vi DOM (test bám class, không bám pixel), nên gom vào task cuối; đây là cổng "look thật + typecheck + package".

- [ ] **Step 1: Thêm CSS `pv-*` theo mock**

  Thêm khối vào `portal.css` (theo quy ước scope Provider của Task 7). Dùng biến màu tối của Portal nếu có; nếu chưa, đặt trong khối này. Ví dụ khung (khớp mock):

  ```css
  .providers-page .pv-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 12px; }
  .providers-page .pv-card { display: flex; align-items: center; gap: 12px; padding: 14px; border: 1px solid var(--pv-border, #26262b); border-radius: 12px; background: var(--pv-card, #161618); cursor: pointer; }
  .providers-page .pv-logo { width: 38px; height: 38px; border-radius: 9px; display: flex; align-items: center; justify-content: center; color: #fff; font-weight: 600; }
  .providers-page .pv-status.on { color: #34d399; } .providers-page .pv-status.off { color: #74747c; }
  .providers-page .pv-safe-tag { color: #34d399; border: 1px solid rgba(52,211,153,.28); background: rgba(52,211,153,.10); border-radius: 20px; padding: 3px 9px; font-size: 12px; }
  .providers-page .pv-safe-badge { border: 1px solid rgba(52,211,153,.30); background: rgba(52,211,153,.08); color: #7fe9c2; border-radius: 10px; padding: 12px 14px; }
  .providers-page .pv-panel { border: 1px solid #24242a; border-radius: 12px; padding: 16px 18px; margin-top: 16px; }
  .providers-page .pv-model-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); gap: 10px; }
  .providers-page .pv-mid { font-family: ui-monospace, Menlo, monospace; }
  ```

  (Bộ đầy đủ bám `scratchpad/providers-mock.html`; giữ mọi selector dưới `.providers-page` để test "Provider CSS regions stays scoped" còn xanh.)

- [ ] **Step 2: Dọn + xác nhận test**

  Bảo đảm `providers.test.mjs` KHÔNG còn test nào tham chiếu `.fact`/`provider-sheet`/`Thêm Provider` (đã bỏ). GIỮ NGUYÊN 2 test: `provider service issues the documented paths and methods` (service không đổi) và `no embedded Portal asset carries an API-key literal` (canary — copy y nguyên từ bản cũ, gồm `KEY_SHAPE`, `staticRoot`, vòng quét).

- [ ] **Step 3: Chạy full**

  ```powershell
  npm --prefix appmode test
  ```
  Expected: tất cả test Portal xanh (gồm gallery/detail mới + service + canary).

- [ ] **Step 4: Cổng build**

  ```powershell
  $env:ZALOBOT_REPO = [Environment]::GetEnvironmentVariable('ZALOBOT_REPO','User')
  $env:ZALOBOT_PERSONA = [Environment]::GetEnvironmentVariable('ZALOBOT_PERSONA','User')
  pwsh -NoProfile -File .\build-app.ps1 -Repo $env:ZALOBOT_REPO -PersonaSource $env:ZALOBOT_PERSONA -Out (Join-Path $env:TEMP ('pv-'+[guid]::NewGuid().ToString('N').Substring(0,8)))
  ```
  Expected: typecheck + package xanh, "sạch: không còn dấu khách hàng nào". Xoá thư mục Out.

- [ ] **Step 5: Refactor** — không.

- [ ] **Step 6: Commit**

  ```bash
  git add appmode/overlay/internal/webui/static/portal.css appmode/tests/providers.test.mjs
  git commit -m "feat: dark 9Router-style CSS for the providers gallery and detail"
  ```
