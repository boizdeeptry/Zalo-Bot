# Onboarding Provider một chạm — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Cho phép người dùng bật Provider, bấm trực tiếp `Bấm để cài.` và đi thẳng vào detect/cài/đăng nhập mà không qua hai nút kết nối trung gian, đồng thời xác nhận an toàn khi tắt lựa chọn chưa cài.

**TDD mode:** yes

**Architecture:** Giữ ON/OFF trước khi cài hoàn toàn ở frontend; CTA cài đặt tuần tự mutation chọn Provider rồi khởi chạy Provider Connect ở chế độ `immediate`. Một helper mới cô lập việc đối chiếu response/lost-response với trạng thái authoritative, giúp `onboarding.js` không vượt giới hạn 800 dòng. Chế độ prompt mặc định của Provider Connect và trang Providers không đổi.

**Tech stack:** Vanilla ES modules, CSS, Node `node:test`, DOM harness hiện có, backend Go/SQLite contract giữ nguyên.

**Spec:** `.planning/specs/2026-08-14-onboarding-one-click-provider-design.md`

**Research:** skipped — thay đổi frontend nhỏ; đã khảo sát trực tiếp controller, component, API contract và test hiện tại bằng hai audit read-only.

**Workspace:** `D:/TuvanZalo/_build/.worktrees/portal-m1` đã là Git worktree riêng trên branch `integration/main-memory-v2`.

---

## File structure

- Create `appmode/overlay/internal/webui/static/pages/onboarding-provider-selection.js` — một trách nhiệm: chạy selection mutation và phân loại authoritative reconciliation thành `selected`, `retry`, `moved`, `invalid` hoặc `unknown`.
- Create `appmode/tests/onboarding-provider-one-click.test.mjs` — behavior tests mới cho row/toggle/CTA/confirmation và orchestration một chạm; tránh đẩy `onboarding-early.test.mjs` vượt 800 dòng.
- Modify `appmode/overlay/internal/webui/static/components/provider-connect.js` — thêm opt-in `immediate: true` vào `start()`; mặc định vẫn render prompt.
- Modify `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js` — row không tương tác chứa hai control anh em, confirmation inline và copy mới.
- Modify `appmode/overlay/internal/webui/static/pages/onboarding.js` — dùng CTA cài đặt, helper reconciliation và Provider Connect immediate.
- Modify `appmode/overlay/internal/webui/static/portal.css` — style row, link-button, toggle, confirmation, focus và mobile.
- Modify `appmode/tests/provider-connect.test.mjs` — contract immediate mode và default prompt regression.
- Modify `appmode/tests/onboarding-early.test.mjs` — cập nhật các assertion cũ đang phụ thuộc `Tiếp tục kết nối`/prompt.
- Modify `appmode/tests/onboarding-ags-setup.test.mjs` — cập nhật DOM/copy của setup surface hiện có.
- Modify `appmode/tests/providers.test.mjs` — giữ regression trang Providers vẫn chờ click `Bắt đầu kết nối`.
- Modify `appmode/tests/shell.test.mjs` — CSS/accessibility/static copy contract.

---

### Task 1: Provider Connect hỗ trợ chế độ khởi chạy ngay có opt-in

**Files:**
- Modify: `appmode/tests/provider-connect.test.mjs`
- Modify: `appmode/tests/providers.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/components/provider-connect.js` (quanh `start()`, dòng 621–633)

**Public behavior to verify:** `start({ immediate: true })` gọi connect ngay với đúng label/revision và không render prompt; mọi caller không truyền cờ vẫn phải bấm `Bắt đầu kết nối`.

- [ ] **Step 1: Viết behavior tests thất bại**

  Thêm test vào `provider-connect.test.mjs` dùng API công khai của controller:

  ```js
  test("immediate start skips the prompt and starts with the pinned onboarding context", async (t) => {
    const calls = [];
    const { controller, slot } = mountConnect(t, {
      service: {
        connectStart(kind, label, onboardingRevision) {
          calls.push({ kind, label, onboardingRevision });
          return Promise.resolve({ kind, phase: "detecting" });
        },
        connectStatus: () => new Promise(() => {}),
        connectCancel: () => Promise.resolve({ ok: true }),
      },
    });

    assert.equal(controller.start({
      label: "Onboarding", onboardingRevision: 12, immediate: true,
    }), true);
    await flush();

    assert.deepEqual(calls, [{ kind: "codex", label: "Onboarding", onboardingRevision: 12 }]);
    assert.equal(button(slot, "Bắt đầu kết nối"), null);
    assert.match(text(slot), /Đang kiểm tra|Đang kết nối/u);
  });
  ```

  Trong test `'+ Thêm account' reuses the connect flow`, destructure cả `calls` từ `mountPage()` rồi giữ hoặc tăng cường regression công khai:

  ```js
  assert.ok(find(main, (node) => node.tagName === "BUTTON"
    && /Bắt đầu kết nối/.test(text(node))));
  assert.equal(calls.some((call) => call.path.endsWith("/connect")
    && call.options.method === "POST"), false);
  ```

- [ ] **Step 2: Chạy RED**

  Run:

  ```powershell
  node --test appmode/tests/provider-connect.test.mjs appmode/tests/providers.test.mjs
  ```

  Expected: test immediate FAIL vì `start()` vẫn render `phase: "prompt"` và chưa gọi `connectStart`; regression Providers vẫn PASS.

- [ ] **Step 3: Cài đặt tối thiểu**

  Đổi signature và thêm nhánh strict-boolean vào `start()`:

  ```js
  function start({ label = "", onboardingRevision = 0, immediate = false } = {}) {
    if (disposed) return false;
    if (immediate === true) {
      void runConnect(label, onboardingRevision);
      return true;
    }
    generation++;
    cancelPromise = null;
    stopTimers();
    connect = {
      kind: providerKind,
      phase: "prompt",
      label: String(label ?? ""),
      onboardingRevision: positiveRevision(onboardingRevision),
    };
    render();
    return true;
  }
  ```

  Không tạo đường POST thứ hai; nhánh mới phải tái dùng nguyên `runConnect()` để giữ normalization, generation guards, polling, cancel và terminal validation hiện có.

- [ ] **Step 4: Chạy GREEN và refactor**

  Run lại command Step 2. Expected: cả hai file PASS; test Providers chứng minh không có POST trước click. Chạy thêm:

  ```powershell
  node --check appmode/overlay/internal/webui/static/components/provider-connect.js
  ```

  Expected: exit `0`.

- [ ] **Step 5: Commit lát component**

  ```powershell
  git add -- appmode/overlay/internal/webui/static/components/provider-connect.js appmode/tests/provider-connect.test.mjs appmode/tests/providers.test.mjs
  git commit -m "feat: start onboarding provider connect immediately"
  ```

---

### Task 2: Row ON/OFF, CTA cài đặt và xác nhận tắt

**Files:**
- Create: `appmode/tests/onboarding-provider-one-click.test.mjs`
- Modify: `appmode/tests/onboarding-ags-setup.test.mjs`
- Modify: `appmode/tests/shell.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js` (quanh `providerDetails`, `providerRow`, `createWelcomeStage`)
- Modify: `appmode/overlay/internal/webui/static/portal.css` (khối onboarding quanh dòng 621–685 và responsive quanh 820–835)

**Public behavior to verify:** ON hiển thị đúng `Chưa cài. Bấm để cài.`, toggle và CTA là control riêng; OFF/chuyển Provider mở xác nhận, còn `Huỷ`/Escape không đổi lựa chọn và `Vẫn tắt` mới áp dụng thay đổi.

- [ ] **Step 1: Viết behavior tests thất bại**

  Tạo `onboarding-provider-one-click.test.mjs` với harness trực tiếp cho export `createWelcomeStage()`:

  ```js
  import test from "node:test";
  import assert from "node:assert/strict";
  import {
    createWelcomeStage,
  } from "../overlay/internal/webui/static/pages/onboarding-early-view.js";
  import { find, installDOM, text } from "./helpers/dom-harness.mjs";

  const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
    && text(node).includes(label));
  const providerRow = (root, kind) => find(root, (node) =>
    node.classList?.contains("onboarding-provider-card")
      && node.dataset.providerKind === kind);
  const providerToggle = (root, kind) => find(root, (node) =>
    node.classList?.contains("onboarding-provider-toggle")
      && node.dataset.providerKind === kind);

  function mountWelcome(t, initialSelection = "") {
    const dom = installDOM();
    const host = document.createElement("div");
    const events = [];
    let selectedProvider = initialSelection;
    const listen = (node, type, listener) => {
      node.addEventListener(type, listener);
      return node;
    };
    const render = () => host.replaceChildren(createWelcomeStage({
      selectedProvider,
      message: "",
      listen,
      onSelect(kind) {
        events.push({ type: "select", kind });
        selectedProvider = kind;
        render();
      },
      onProceed() { events.push({ type: "install", kind: selectedProvider }); },
      onRetry() {},
    }));
    render();
    t.after(() => dom.restore());
    return { host, events };
  }
  ```

  Test DOM công khai, không gọi closure nội bộ:

  ```js
  test("ON exposes a separate install action without the old Continue action", (t) => {
    const { host, events } = mountWelcome(t);
    providerToggle(host, "codex").click();

    assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
    assert.match(text(providerRow(host, "codex")), /Chưa cài\.\s*Bấm để cài\./u);
    assert.ok(button(providerRow(host, "codex"), "Bấm để cài."));
    assert.equal(button(host, "Tiếp tục kết nối"), null);
    assert.deepEqual(events, [{ type: "select", kind: "codex" }]);
  });
  ```

  Thêm matrix OFF/chuyển Provider:

  ```js
  test("OFF and provider replacement require confirmation without server mutation", (t) => {
    const { host, events } = mountWelcome(t, "codex");
    providerToggle(host, "codex").click();
    const warning = find(host, (node) => node.getAttribute?.("role") === "alertdialog");
    assert.ok(warning);
    assert.equal(document.activeElement, button(warning, "Huỷ"));

    button(warning, "Huỷ").click();
    assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
    assert.deepEqual(events, []);

    providerToggle(host, "claude-code").click();
    button(host, "Vẫn tắt").click();
    assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
    assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "true");
    assert.deepEqual(events, [{ type: "select", kind: "claude-code" }]);
  });
  ```

  Bổ sung test `keydown Escape` tương đương `Huỷ`, focus quay lại toggle khởi phát, không có button lồng trong button, CTA của row OFF không tồn tại, và double-click CTA chỉ gọi callback một lần.

  Trong `shell.test.mjs`, yêu cầu selector focus-visible cho `.onboarding-provider-toggle`, `.onboarding-provider-install`, confirmation có chiều rộng bounded và media rule không tạo overflow.

- [ ] **Step 2: Chạy RED**

  Run:

  ```powershell
  node --test appmode/tests/onboarding-provider-one-click.test.mjs appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/shell.test.mjs
  ```

  Expected: FAIL vì row hiện là một button nguyên khối, không toggle OFF, chưa có CTA/alertdialog và vẫn còn `Tiếp tục kết nối`.

- [ ] **Step 3: Cài đặt DOM và confirmation tối thiểu**

  Refactor `providerRow()` thành container. Cấu trúc bắt buộc:

  ```js
  const toggle = element("button", {
    className: "onboarding-provider-toggle",
    attributes: {
      type: "button",
      "data-provider-kind": kind,
      "aria-label": `${selected ? "Tắt" : "Bật"} ${label}`,
      "aria-pressed": String(selected),
    },
  }, providerSwitch(selected));
  const install = selected ? element("button", {
    className: "onboarding-provider-install",
    attributes: { type: "button" },
    text: "Bấm để cài.",
  }) : null;
  const status = element("small", { className: "onboarding-provider-install-state" },
    element("span", { text: selected ? "Chưa cài. " : "Chưa dùng" }),
    install,
  );
  listen(toggle, "click", () => onToggle(kind, toggle));
  if (install) listen(install, "click", onInstall);
  return element("div", {
    className: `onboarding-provider-card onboarding-agent-row${selected ? " is-selected" : ""}`,
    attributes: { "data-provider-kind": kind },
  },
  providerMark(kind),
  element("span", { className: "onboarding-agent-row-main" },
    element("strong", { text: label }), status),
  element("span", { className: "onboarding-agent-row-tail" },
    providerStatusBadge(selected), toggle));
  ```

  `createWelcomeStage()` giữ `pendingReplacement` trong closure của view:

  - không có selection: toggle ON gọi `onSelect(kind)` ngay;
  - đang ON mà bấm chính toggle hoặc Provider khác: render confirmation `role="alertdialog"` trong stage;
  - alertdialog dùng `aria-labelledby="onboarding-provider-confirm-title"` và `aria-describedby="onboarding-provider-confirm-description"`; không đặt `aria-modal` thứ hai bên trong modal Onboarding;
  - khi alertdialog mở, disable các toggle/CTA phía sau và chỉ để `Huỷ`/`Vẫn tắt` tương tác;
  - `Huỷ` và Escape đóng confirmation rồi focus lại toggle khởi phát;
  - `Vẫn tắt` gọi `onSelect("")` khi OFF, hoặc `onSelect(replacementKind)` khi chuyển;
  - CTA gọi callback `onProceed()` hiện có theo cơ chế one-flight, disable toàn bộ toggle/CTA của view;
  - xóa hoàn toàn button `Tiếp tục kết nối` và copy hướng dẫn phụ thuộc button đó.

  Confirmation dùng đúng copy chức năng:

  ```js
  element("p", {
    text: `Nếu tắt ${providerName(selectedProvider)} trước khi cài, lựa chọn này sẽ bị bỏ. Bạn vẫn muốn tắt chứ?`,
  })
  ```

- [ ] **Step 4: Cài đặt CSS và accessibility**

  Đổi `.onboarding-provider-card` thành row container và thêm các selector scoped:

  ```css
  .onboarding-shell .onboarding-provider-card { display:flex; align-items:center; gap:12px; }
  .onboarding-shell .onboarding-provider-toggle {
    display:inline-grid; flex:0 0 auto; place-items:center; border:0; padding:0;
    border-radius:999px; color:inherit; background:transparent; cursor:pointer;
  }
  .onboarding-shell .onboarding-provider-install {
    border:0; padding:0; color:var(--onboarding-cyan); background:transparent;
    font:inherit; font-weight:800; text-decoration:underline; cursor:pointer;
  }
  .onboarding-shell .onboarding-provider-toggle:focus-visible,
  .onboarding-shell .onboarding-provider-install:focus-visible {
    outline:2px solid var(--onboarding-cyan); outline-offset:3px;
  }
  .onboarding-shell .onboarding-provider-confirm {
    width:min(360px,100%); margin:12px auto 0; border:1px solid var(--onboarding-border);
    border-radius:9px; padding:12px; background:#151b30;
  }
  ```

  Mobile `390px` phải cho `.onboarding-agent-row-main` co lại, tail/toggle không tràn và confirmation dùng `width:100%` trong stage.

- [ ] **Step 5: Chạy GREEN và commit**

  Run lại command Step 2, rồi:

  ```powershell
  node --check appmode/overlay/internal/webui/static/pages/onboarding-early-view.js
  git diff --check
  ```

  Expected: test PASS, syntax exit `0`, không whitespace error.

  ```powershell
  git add -- appmode/overlay/internal/webui/static/pages/onboarding-early-view.js appmode/overlay/internal/webui/static/portal.css appmode/tests/onboarding-provider-one-click.test.mjs appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/shell.test.mjs
  git commit -m "feat: add onboarding provider toggle confirmation"
  ```

---

### Task 3: Nối CTA với mutation, immediate Connect và lost-response reconciliation

**Files:**
- Create: `appmode/overlay/internal/webui/static/pages/onboarding-provider-selection.js`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding.js` (imports, `renderWelcome`, `selectProvider`, `renderConnect`)
- Modify: `appmode/tests/onboarding-provider-one-click.test.mjs`
- Modify: `appmode/tests/onboarding-early.test.mjs`

**Public behavior to verify:** Một click CTA tạo đúng một `PUT`, dùng revision successor để tự `POST` Connect, tự phục hồi response PUT bị mất bằng GET authoritative và không cho late/disposed work đổi màn hình.

- [ ] **Step 1: Viết orchestration tests thất bại**

  Mở rộng import/harness của file test mới để chạy qua `createOnboardingPage`:

  ```js
  import {
    createOnboardingPage,
  } from "../overlay/internal/webui/static/pages/onboarding.js";
  import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

  const flush = () => new Promise((resolve) => setImmediate(resolve));
  function deferred() {
    let resolve;
    let reject;
    const promise = new Promise((onResolve, onReject) => {
      resolve = onResolve;
      reject = onReject;
    });
    return { promise, resolve, reject };
  }
  function providerStatus(overrides = {}) {
    return onboardingStatus("provider", { revision: 1, ...overrides });
  }
  function connectStatus(overrides = {}) {
    return providerStatus({
      phase: "connect", provider_kind: "codex", revision: 2, ...overrides,
    });
  }
  function pageService(overrides = {}) {
    return {
      status: () => Promise.resolve(providerStatus()),
      selectProvider: () => Promise.resolve(connectStatus()),
      setup: () => Promise.reject(new Error("not used")),
      loadAgent: () => Promise.reject(new Error("not used")),
      saveAgent: () => Promise.reject(new Error("not used")),
      testChat: () => Promise.reject(new Error("not used")),
      complete: () => Promise.reject(new Error("not used")),
      ...overrides,
    };
  }
  function fakeConnectFactory() {
    const instances = [];
    const factory = (options) => {
      const instance = {
        options,
        starts: [],
        mount() { return this; },
        start(value) { this.starts.push(value); return true; },
        cancel: () => Promise.resolve(true),
        dispose() {},
      };
      instances.push(instance);
      return instance;
    };
    factory.instances = instances;
    return factory;
  }
  function mountProviderPage(t, {
    revision = 1,
    suggestedProvider = "codex",
    connectFactory = fakeConnectFactory(),
    selectProvider = () => Promise.resolve(connectStatus({ revision: revision + 1 })),
    status = () => Promise.resolve(providerStatus({ revision, suggested_provider_kind: suggestedProvider })),
  } = {}) {
    const dom = installDOM();
    const host = document.createElement("div");
    const page = createOnboardingPage({
      initialStatus: providerStatus({
        revision, suggested_provider_kind: suggestedProvider,
      }),
      service: pageService({ selectProvider, status }),
      connectService: {
        connectStart: () => Promise.resolve({ kind: "codex", phase: "detecting" }),
        connectStatus: () => new Promise(() => {}),
        connectCancel: () => Promise.resolve({ ok: true }),
      },
      connectFactory,
    });
    page.mount(host);
    t.after(() => {
      page.dispose();
      dom.restore();
    });
    return { page, host, connectFactory };
  }
  ```

  Thêm test qua controller công khai:

  ```js
  test("install CTA performs one selection then immediate Connect with the successor revision", async (t) => {
    const selectGate = deferred();
    const selectCalls = [];
    const connectFactory = fakeConnectFactory();
    const { host } = mountProviderPage(t, {
      revision: 11,
      suggestedProvider: "codex",
      connectFactory,
      selectProvider(kind, revision, signal) {
        selectCalls.push({ kind, revision, signal });
        return selectGate.promise;
      },
    });

    const install = button(host, "Bấm để cài.");
    install.click();
    install.click();
    assert.equal(selectCalls.length, 1);
    selectGate.resolve(connectStatus({ revision: 12 }));
    await flush();

    assert.deepEqual(connectFactory.instances[0].starts, [{
      label: "Onboarding", onboardingRevision: 12, immediate: true,
    }]);
    assert.equal(button(host, "Bắt đầu kết nối"), null);
  });
  ```

  Thêm lost-response matrix:

  - selection reject + GET đúng `connect`, same kind, `r+1` → Connect immediate, không PUT lại;
  - selection reject + GET đúng snapshot `provider`, same revision/lifecycle → row vẫn ON, lỗi an toàn, CTA retry dùng revision authoritative;
  - selection reject + GET phase hợp lệ khác → render phase authoritative;
  - selection reject + GET malformed/reject → safe error, không Connect;
  - aborted/disposed selection → không GET reconciliation, không render late;
  - revision `Number.MAX_SAFE_INTEGER` → không PUT/GET/POST;
  - resume trực tiếp ở phase `connect` → không PUT, `start(... immediate:true)` đúng một lần.

  Cập nhật các test cũ trong `onboarding-early.test.mjs` để tìm CTA thay cho `Tiếp tục`, và đảo assertion real component từ “không POST trước click prompt” thành “POST tự động, không có prompt”.

- [ ] **Step 2: Chạy RED**

  Run:

  ```powershell
  node --test appmode/tests/onboarding-provider-one-click.test.mjs appmode/tests/onboarding-early.test.mjs appmode/tests/provider-connect.test.mjs
  ```

  Expected: FAIL vì page chưa truyền `immediate:true`, chưa gọi selection từ CTA và catch hiện tại chưa GET để reconcile lost response.

- [ ] **Step 3: Tạo helper reconciliation hoàn chỉnh**

  Tạo `onboarding-provider-selection.js`:

  ```js
  import {
    normalizeProviderSelection, normalizeStatus,
  } from "./onboarding-contract.js";

  const STATUS_IDENTITY = Object.freeze([
    "required", "current_version", "completed_version", "restart_in_progress",
    "phase", "provider_kind", "suggested_provider_kind", "provider_id",
    "account_id", "model_id", "revision",
  ]);

  function sameStatus(left, right) {
    return STATUS_IDENTITY.every((field) => left[field] === right[field]);
  }

  function aborted(error, signal) {
    return signal?.aborted === true || error?.name === "AbortError";
  }

  export async function selectProviderWithReconciliation({
    service, snapshot, providerKind, signal,
  }) {
    const requested = { providerKind, revision: snapshot.revision };
    try {
      const response = await service.selectProvider(providerKind, snapshot.revision, signal);
      const selected = normalizeProviderSelection(response, requested);
      return selected ? { kind: "selected", state: selected } : { kind: "invalid" };
    } catch (error) {
      if (aborted(error, signal)) throw error;
      try {
        const authoritative = normalizeStatus(await service.status(signal));
        if (!authoritative) return { kind: "invalid" };
        const selected = normalizeProviderSelection(authoritative, requested);
        if (selected) return { kind: "selected", state: selected };
        if (sameStatus(snapshot, authoritative)) return { kind: "retry", state: authoritative };
        return { kind: "moved", state: authoritative };
      } catch (statusError) {
        if (aborted(statusError, signal)) throw statusError;
        return { kind: "unknown" };
      }
    }
  }
  ```

  Không log raw error hoặc response; helper chỉ trả discriminator và snapshot đã normalize.

- [ ] **Step 4: Nối helper vào page controller**

  Trong `renderWelcome()`, giữ callback `onProceed: () => { void selectProvider(); }`; cập nhật `onSelect(kind)` để chấp nhận chuỗi rỗng do `Vẫn tắt`, chỉ gọi `focusProviderControl` khi kind thuộc `SUPPORTED_PROVIDERS`.

  Thêm import controller dùng đúng tên helper:

  ```js
  import {
    selectProviderWithReconciliation,
  } from "./onboarding-provider-selection.js";
  ```

  Trong `selectProvider()`, capture `requestedKind` và `snapshot` trước `beginOperation()`, gọi helper, kiểm tra `owns(run)` trước mọi render:

  - `selected`: nhận state successor, cập nhật selection và `renderCurrent()`;
  - `retry`: nhận snapshot authoritative, giữ `requestedKind` ON và `renderWelcome(SAFE_ERROR)`;
  - `moved`: nhận state authoritative, derive selection từ state rồi `renderCurrent()`;
  - `invalid`/`unknown`: `renderSafeError()`;
  - AbortError/dispose: return `false` không render.

  Trong `renderConnect()` truyền cờ opt-in:

  ```js
  if (instance.start({
    label: "Onboarding", onboardingRevision: state.revision, immediate: true,
  }) === false) {
    throw new Error("Provider Connect refused to start");
  }
  ```

  `onboarding.js` hiện đúng 800 dòng. Sau thay đổi phải vẫn `<=800`; dùng helper mới để chứa reconciliation, không nén/minify các flow Persona/Test/Complete và không dồn nhiều statement không liên quan lên một dòng.

- [ ] **Step 5: Chạy GREEN, line gate và commit**

  Run focused command Step 2, sau đó:

  ```powershell
  node --check appmode/overlay/internal/webui/static/pages/onboarding-provider-selection.js
  node --check appmode/overlay/internal/webui/static/pages/onboarding.js
  $onboardingLines = (Get-Content appmode/overlay/internal/webui/static/pages/onboarding.js).Count
  $earlyTestLines = (Get-Content appmode/tests/onboarding-early.test.mjs).Count
  $oneClickTestLines = (Get-Content appmode/tests/onboarding-provider-one-click.test.mjs).Count
  if ($onboardingLines -gt 800 -or $earlyTestLines -gt 800 -or $oneClickTestLines -gt 800) { throw "800-line cap exceeded" }
  git diff --check
  ```

  Expected: focused tests PASS, syntax exit `0`, both capped files `<=800`, diff clean.

  ```powershell
  git add -- appmode/overlay/internal/webui/static/pages/onboarding-provider-selection.js appmode/overlay/internal/webui/static/pages/onboarding.js appmode/tests/onboarding-provider-one-click.test.mjs appmode/tests/onboarding-early.test.mjs
  git commit -m "feat: install onboarding providers in one click"
  ```

---

## Final verification and preview

- [ ] Chạy toàn bộ nhóm Onboarding/Provider:

  ```powershell
  node --test appmode/tests/onboarding-provider-one-click.test.mjs appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs appmode/tests/onboarding.test.mjs appmode/tests/onboarding-recovery.test.mjs appmode/tests/onboarding-regressions.test.mjs appmode/tests/provider-connect.test.mjs appmode/tests/providers.test.mjs appmode/tests/app-main.test.mjs appmode/tests/shell.test.mjs
  ```

  Expected: tất cả PASS; runtime Onboarding không chứa `Tiếp tục kết nối` hoặc `Bắt đầu kết nối`, trang Providers vẫn chứa prompt của nó.

- [ ] Chạy toàn bộ Portal suite:

  ```powershell
  npm --prefix appmode test
  ```

  Expected: toàn bộ Node/static tests PASS, không unhandled rejection hoặc timer leak.

- [ ] Chạy syntax, line-cap, copy và Git hygiene:

  ```powershell
  node --check appmode/overlay/internal/webui/static/components/provider-connect.js
  node --check appmode/overlay/internal/webui/static/pages/onboarding-provider-selection.js
  node --check appmode/overlay/internal/webui/static/pages/onboarding-early-view.js
  node --check appmode/overlay/internal/webui/static/pages/onboarding.js
  $onboardingLines = (Get-Content appmode/overlay/internal/webui/static/pages/onboarding.js).Count
  $earlyViewLines = (Get-Content appmode/overlay/internal/webui/static/pages/onboarding-early-view.js).Count
  $earlyTestLines = (Get-Content appmode/tests/onboarding-early.test.mjs).Count
  $oneClickTestLines = (Get-Content appmode/tests/onboarding-provider-one-click.test.mjs).Count
  if ($onboardingLines -gt 800 -or $earlyViewLines -gt 800 -or $earlyTestLines -gt 800 -or $oneClickTestLines -gt 800) { throw "800-line cap exceeded" }
  git diff --check
  git status --short
  ```

  Expected: syntax exit `0`, line caps hợp lệ, diff clean; chỉ có các file thuộc plan nếu chưa commit.

- [ ] Dispatch independent spec-compliance review, sau đó code-quality review. Không bỏ qua Critical/Important; sửa theo TDD và chạy lại focused + full suite.

- [ ] Xác minh process đang sở hữu port `8770` có đúng executable preview hiện tại, dừng bằng `agentdc daemon stop`, chờ port được giải phóng; không kill theo tên process. Sau đó build package preview mới từ commit đã verify:

  ```powershell
  pwsh -NoProfile -File .\build-app.ps1 `
    -Repo 'C:\Users\manva\OneDrive\Máy tính\agentdc' `
    -Out 'D:\TuvanZalo\_artifacts\tuvanzalo-onboarding-preview' `
    -PersonaSource 'D:\TuvanZalo\brain\reference\persona'
  ```

  Expected: build exit `0`; package gates PASS. Khởi động daemon từ đúng executable mới và xác nhận `/status` trả PID mới trước khi QA.

- [ ] QA trực tiếp ở `2048x1024` và viewport CSS `390x844`:

  - ON hiển thị đúng `Chưa cài. Bấm để cài.`;
  - OFF và chuyển Provider hiện confirmation, focus/Cancel/`Vẫn tắt` đúng;
  - CTA một click chuyển thẳng sang progress/log, không thấy hai nút cũ;
  - không overflow ngang, lưới nền và modal hiện tại không đổi;
  - trang Providers vẫn mở prompt nhãn tài khoản bình thường.
