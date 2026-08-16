import test from "node:test";
import assert from "node:assert/strict";

import {
  createWelcomeStage,
  focusProviderControl,
} from "../overlay/internal/webui/static/pages/onboarding-early-view.js";
import {
  createOnboardingPage,
} from "../overlay/internal/webui/static/pages/onboarding.js";
import { createProviderOffDialog } from "../overlay/internal/webui/static/components/provider-off-dialog.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus, pendingProvider, readyProvider } from "./helpers/onboarding-fixtures.mjs";

const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const byClass = (root, name) => find(root, (node) => hasClass(node, name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const providerRow = (root, kind) => find(root, (node) =>
  hasClass(node, "onboarding-provider-card") && node.dataset.providerKind === kind);
const providerToggle = (root, kind) => find(root, (node) =>
  hasClass(node, "onboarding-provider-toggle") && node.dataset.providerKind === kind);
const installButton = (root, kind) => byClass(providerRow(root, kind), "onboarding-provider-install");
const confirmation = () => find(document.body, (node) => node.tagName === "DIALOG"
  && node.getAttribute?.("role") === "alertdialog");

function mountWelcome(t, initialSelection = "", message = "", onListen = () => {}) {
  const dom = installDOM();
  const host = document.createElement("div");
  document.body.append(host);
  const events = [];
  const selected = new Set(initialSelection ? [initialSelection] : []);
  const listen = (node, type, listener) => {
    onListen();
    node.addEventListener(type, listener);
    return node;
  };
  const offDialog = createProviderOffDialog({
    listen,
    async onConfirm(kind) {
      events.push({ type: "toggle", kind, enabled: false });
      selected.delete(kind);
      render();
      return true;
    },
  });
  const render = () => host.replaceChildren(createWelcomeStage({
    state: pageStatus({
      providers: [...selected].map((kind, position) => pendingProvider(kind, position)),
    }),
    message,
    busy: false,
    listen,
    onToggle(kind, enabled) {
      events.push({ type: "toggle", kind, enabled });
      if (enabled) selected.add(kind);
      render();
    },
    onRequestOff: ({ kind, label, opener }) => offDialog.open({
      label,
      value: kind,
      opener,
      description: `Nếu tắt ${label} trước khi cài, lựa chọn này sẽ bị bỏ. Bạn vẫn muốn tắt chứ?`,
    }),
    onInstall(kind) { events.push({ type: "install", kind }); },
    onRetry() { events.push({ type: "retry" }); },
  }));
  render();
  t.after(() => { offDialog.dispose(); dom.restore(); });
  return { host, events };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));
const settle = async () => { await flush(); await flush(); };
function deferred() {
  let resolve; let reject;
  const promise = new Promise((onResolve, onReject) => { resolve = onResolve; reject = onReject; });
  return { promise, resolve, reject };
}
function pageStatus(overrides = {}) {
  return onboardingStatus("provider", { revision: 1, ...overrides });
}
function connectionStatus(overrides = {}) {
  return pageStatus({ phase: "connect", provider_kind: "codex", revision: 2, ...overrides });
}
function pageService(overrides = {}) {
  const status = overrides.status ?? (() => Promise.resolve(pageStatus()));
  return {
    status,
    bootstrapStatus: overrides.bootstrapStatus ?? status,
    updateProviders: () => new Promise(() => {}),
    beginProvider: () => new Promise(() => {}),
    backToProviders: () => new Promise(() => {}),
    selectProvider: () => Promise.resolve(connectionStatus()),
    setup: () => new Promise(() => {}),
    loadAgent: () => new Promise(() => {}),
    bootstrap: () => new Promise(() => {}),
    ...overrides,
  };
}
function connectService() {
  return {
    connectStart: () => new Promise(() => {}),
    connectStatus: () => new Promise(() => {}),
    connectCancel: () => Promise.resolve({ ok: true }),
  };
}
function connectFactory() {
  const instances = [];
  const factory = (options) => {
    const instance = {
      options, starts: [], slot: null,
      mount(slot) { this.slot = slot; return this; },
      start(context) { this.starts.push(context); return true; },
      cancel: () => Promise.resolve(true),
      dispose() { this.slot?.replaceChildren(); },
    };
    instances.push(instance);
    return instance;
  };
  factory.instances = instances;
  return factory;
}
function mountPage(t, options = {}) {
  const dom = installDOM();
  const host = document.createElement("div");
  document.body.append(host);
  const page = createOnboardingPage({
    initialStatus: pageStatus(), service: pageService(),
    connectService: connectService(), ...options,
  });
  page.mount(host);
  t.after(() => { page.dispose(); dom.restore(); });
  return { host, page };
}

test("ON exposes a separate install action without the old Continue action", (t) => {
  const { host, events } = mountWelcome(t);

  providerToggle(host, "codex").click();

  const row = providerRow(host, "codex");
  const toggle = providerToggle(host, "codex");
  const install = installButton(host, "codex");
  assert.equal(row.tagName, "DIV");
  assert.equal(toggle.getAttribute("aria-pressed"), "true");
  assert.equal(toggle.getAttribute("aria-label"), "Tắt Codex");
  assert.equal(text(byClass(row, "onboarding-provider-install-state")), "Chưa cài. Bấm để cài.");
  assert.equal(text(install), "Bấm để cài.");
  assert.equal(button(host, "Tiếp tục kết nối"), null);
  assert.deepEqual(events, [{ type: "toggle", kind: "codex", enabled: true }]);
});

test("OFF is noninteractive outside its toggle and has no install action", (t) => {
  const { host, events } = mountWelcome(t);
  const row = providerRow(host, "claude-code");

  assert.equal(row.tagName, "DIV");
  assert.equal(text(byClass(row, "onboarding-provider-install-state")), "Chưa dùng");
  assert.equal(installButton(host, "claude-code"), null);
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-label"), "Bật Claude Code");
  row.click();
  assert.deepEqual(events, []);

  const nestedButtons = findAll(host, (node) => node.tagName === "BUTTON")
    .flatMap((control) => findAll(control, (node) => node !== control && node.tagName === "BUTTON"));
  assert.deepEqual(nestedButtons, []);
});

test("turning the current provider OFF opens one named native modal", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");

  origin.click();

  const warning = confirmation();
  assert.ok(warning);
  assert.equal(warning.tagName, "DIALOG");
  assert.equal(warning.open, true);
  assert.equal(warning.getAttribute("aria-labelledby"), "provider-off-dialog-title");
  assert.equal(warning.getAttribute("aria-describedby"), "provider-off-dialog-description");
  assert.equal(warning.getAttribute("aria-modal"), "true");
  assert.deepEqual(findAll(document.body, (node) => node.getAttribute?.("role") === "alertdialog"), [warning]);
  assert.equal(text(byClass(warning, "provider-off-dialog-description")),
    "Nếu tắt Codex trước khi cài, lựa chọn này sẽ bị bỏ. Bạn vẫn muốn tắt chứ?");
  assert.equal(document.activeElement, button(warning, "Hủy"));
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.deepEqual(events, []);
});

test("Hủy closes confirmation, keeps selection, and restores toggle focus", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");
  origin.click();

  button(confirmation(), "Hủy").click();

  assert.equal(confirmation(), null);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(document.activeElement, origin);
  assert.deepEqual(events, []);
});

test("Escape cancels confirmation and restores the originating toggle", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");
  origin.click();

  const warning = confirmation();
  const cancel = button(warning, "Hủy");
  assert.equal(document.activeElement, cancel);
  const accepted = warning.cancel();

  assert.equal(accepted, false);
  assert.equal(confirmation(), null);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(document.activeElement, origin);
  assert.deepEqual(events, []);
});

test("repeated confirmation cancellation retains a bounded listener set", (t) => {
  let bindings = 0;
  const { host } = mountWelcome(t, "codex", "", () => { bindings++; });
  const origin = providerToggle(host, "codex");
  origin.click();
  button(confirmation(), "Hủy").click();
  const stableBindings = bindings;

  for (let cycle = 0; cycle < 5; cycle++) {
    origin.click();
    button(confirmation(), "Hủy").click();
  }

  assert.equal(bindings, stableBindings);
});

test("Vẫn tắt applies OFF only after authoritative confirmation", async (t) => {
  const { host, events } = mountWelcome(t, "codex");
  providerToggle(host, "codex").click();

  button(confirmation(), "Vẫn tắt").click();
  await settle();

  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
  assert.equal(installButton(host, "codex"), null);
  assert.deepEqual(events, [{ type: "toggle", kind: "codex", enabled: false }]);
});

test("native modal makes every background toggle and install action inert", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  providerToggle(host, "codex").click();

  const controls = [
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-toggle")),
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-install")),
  ];
  assert.ok(controls.length > 0);
  controls.forEach((control) => control.click());
  assert.deepEqual(events, []);
  assert.equal(document.activeModalDialog, confirmation());
  assert.equal(button(confirmation(), "Hủy").disabled, false);
  assert.equal(button(confirmation(), "Vẫn tắt").disabled, false);
});

test("native modal makes Retry inert until cancellation", (t) => {
  const { host, events } = mountWelcome(t, "codex", "Không thể kết nối");
  const retry = button(host, "Thử lại");
  assert.equal(retry.disabled, false);

  providerToggle(host, "codex").click();

  assert.equal(retry.disabled, false);
  retry.click();
  assert.deepEqual(events, []);

  button(confirmation(), "Hủy").click();
  assert.equal(retry.disabled, false);
  retry.click();
  assert.deepEqual(events, [{ type: "retry" }]);
});

test("install CTA is one-flight and disables all provider controls", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const install = installButton(host, "codex");

  install.click();
  install.click();

  assert.deepEqual(events, [{ type: "install", kind: "codex" }]);
  const controls = [
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-toggle")),
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-install")),
  ];
  assert.ok(controls.every((control) => control.disabled));
});

test("install one-flight also locks Retry", (t) => {
  const { host, events } = mountWelcome(t, "codex", "Không thể kết nối");
  const install = installButton(host, "codex");
  const retry = button(host, "Thử lại");

  install.click();
  retry.click();
  retry.click();
  install.click();

  assert.equal(retry.disabled, true);
  assert.deepEqual(events, [{ type: "install", kind: "codex" }]);
});

test("focusProviderControl targets the dedicated provider toggle", (t) => {
  const dom = installDOM();
  t.after(() => dom.restore());
  const listen = (node, type, listener) => {
    node.addEventListener(type, listener);
    return node;
  };
  const stage = createWelcomeStage({
    state: pageStatus({ providers: [pendingProvider("codex", 0)] }),
    message: "",
    busy: false,
    listen,
    onToggle() {},
    onInstall() {},
    onRetry() {},
  });

  assert.equal(focusProviderControl(stage, "codex"), true);
  assert.equal(document.activeElement, providerToggle(stage, "codex"));
  assert.notEqual(document.activeElement, providerRow(stage, "codex"));
});

test("pending OFF confirms accessibly while ready rows stay locked ON", async (t) => {
  const ready = readyProvider("codex", 0);
  const pending = pendingProvider("claude-code", 1);
  const calls = [];
  const { host } = mountPage(t, {
    initialStatus: pageStatus({ revision: 31, providers: [ready, pending] }),
    service: pageService({
      updateProviders(kinds, revision, signal) {
        calls.push({ kinds, revision, signal });
        return Promise.resolve(onboardingStatus("persona", { revision: 32, providers: [ready] }));
      },
    }),
  });

  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(providerToggle(host, "codex").disabled, true);
  assert.match(text(providerRow(host, "codex")), /Đã sẵn sàng · Ưu tiên 1/u);
  assert.match(text(providerRow(host, "codex")), /Quản lý sau ở mục Provider\/Combo\./u);
  assert.match(text(providerRow(host, "claude-code")), /Chưa cài\. Bấm để cài\./u);

  const origin = providerToggle(host, "claude-code");
  origin.click();
  assert.equal(calls.length, 0);
  assert.ok(confirmation());
  assert.equal(document.activeElement, button(confirmation(), "Hủy"));
  confirmation().cancel();
  assert.equal(confirmation(), null);
  assert.equal(document.activeElement, origin);
  assert.equal(origin.getAttribute("aria-pressed"), "true");

  origin.click();
  const activeDialog = confirmation();
  button(activeDialog, "Vẫn tắt").click();
  assert.equal(confirmation(), activeDialog);
  await settle();
  assert.deepEqual(calls.map(({ kinds, revision }) => ({ kinds, revision })), [
    { kinds: ["codex"], revision: 31 },
  ]);
  assert.equal(providerToggle(host, "claude-code"), null);
});

test("failed OFF reconciliation stays in the same modal and Retry remains one-flight", async (t) => {
  const retained = pendingProvider("codex", 0);
  let mutations = 0;
  let loads = 0;
  const { host } = mountPage(t, {
    initialStatus: pageStatus({ revision: 71, providers: [retained] }),
    service: pageService({
      updateProviders(kinds, revision) {
        mutations += 1;
        assert.deepEqual({ kinds, revision }, { kinds: [], revision: 71 });
        if (mutations === 1) return Promise.reject(new Error("private mutation detail"));
        return Promise.resolve(pageStatus({ revision: 72, providers: [] }));
      },
      status() {
        loads += 1;
        return Promise.reject(new Error("private reconciliation detail"));
      },
    }),
  });

  providerToggle(host, "codex").click();
  const modal = confirmation();
  button(modal, "Vẫn tắt").click();
  button(modal, "Vẫn tắt").click();
  await settle();

  assert.equal(mutations, 1);
  assert.equal(loads, 1);
  assert.equal(confirmation(), modal);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.doesNotMatch(text(modal), /private mutation detail|private reconciliation detail/u);
  assert.match(text(modal), /Chưa thể tắt provider/u);

  button(modal, "Thử lại").click();
  button(modal, "Thử lại").click();
  await settle();
  assert.equal(mutations, 2);
  assert.equal(confirmation(), null);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
});

test("provider mutation is one-flight and disposal aborts its late response", async (t) => {
  const pending = deferred();
  const calls = [];
  const { host, page } = mountPage(t, {
    initialStatus: pageStatus({ revision: 41 }),
    service: pageService({
      updateProviders(kinds, revision, signal) {
        calls.push({ kinds, revision, signal });
        return pending.promise;
      },
    }),
  });
  const retained = providerToggle(host, "codex");

  retained.click(); retained.click(); page.mount(host); retained.click();
  assert.equal(calls.length, 1);
  assert.equal(retained.disabled, true);
  page.dispose();
  assert.equal(calls[0].signal.aborted, true);
  pending.resolve(pageStatus({ revision: 42, providers: [pendingProvider("codex", 0)] }));
  await settle();
  assert.equal(text(host), "");
});

test("dirty persisted Connect snapshots fail closed before side effects", async (t) => {
  for (const [field, value] of [
    ["provider_id", "codex"], ["account_id", "account-1"], ["model_id", "model-1"],
  ]) {
    await t.test(field, (subtest) => {
      const calls = [];
      const factory = connectFactory();
      const { host } = mountPage(subtest, {
        initialStatus: connectionStatus({ [field]: value }),
        service: pageService({
          status: () => { calls.push("status"); },
          selectProvider: () => { calls.push("select"); },
          setup: () => { calls.push("setup"); },
        }),
        connectFactory: factory,
      });

      assert.deepEqual(calls, []);
      assert.equal(factory.instances.length, 0);
      assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
    });
  }
});

test("persisted Connect resumes immediate mode once without selection", (t) => {
  let selects = 0; let statuses = 0;
  const factory = connectFactory();
  const { host, page } = mountPage(t, {
    initialStatus: connectionStatus({ provider_kind: "claude-code", revision: 61 }),
    service: pageService({
      selectProvider: () => { selects++; },
      status: () => { statuses++; },
    }),
    connectFactory: factory,
  });

  page.mount(host);
  assert.equal(selects, 0);
  assert.equal(statuses, 0);
  assert.equal(factory.instances.length, 1);
  assert.equal(factory.instances[0].options.kind, "claude-code");
  assert.deepEqual(factory.instances[0].starts, [{
    label: "Onboarding", onboardingRevision: 61, immediate: true,
  }]);
});

test("Connect Back renders an authoritative moved phase without selection or restart", async (t) => {
  const calls = [];
  const factory = connectFactory();
  const { host } = mountPage(t, {
    initialStatus: connectionStatus({ revision: 62 }),
    service: pageService({
      status() {
        calls.push("status");
        return Promise.resolve(pageStatus({
          revision: 70, suggested_provider_kind: "claude-code",
          providers: [pendingProvider("claude-code", 0)],
        }));
      },
      selectProvider: () => { calls.push("select"); },
      setup: () => { calls.push("setup"); },
    }),
    connectFactory: factory,
  });

  await factory.instances[0].options.onBack();

  assert.deepEqual(calls, ["status"]);
  assert.equal(factory.instances.length, 1);
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "true");
});

test("Connect Back status failure is retryable without restarting", async (t) => {
  let statuses = 0;
  const factory = connectFactory();
  const { host } = mountPage(t, {
    initialStatus: connectionStatus({ revision: 63 }),
    service: pageService({
      status() { statuses++; return Promise.reject(new Error("private status failure")); },
    }),
    connectFactory: factory,
  });

  await factory.instances[0].options.onBack();

  assert.equal(statuses, 1);
  assert.equal(factory.instances.length, 1);
  assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
  assert.equal(button(host, "Thử lại").tagName, "BUTTON");
  assert.doesNotMatch(text(host), /private status failure/u);
});

test("disposing during Connect Back aborts GET and suppresses its late status", async (t) => {
  const statusGate = deferred();
  let signal;
  const factory = connectFactory();
  const { host, page } = mountPage(t, {
    initialStatus: connectionStatus({ revision: 64 }),
    service: pageService({
      status(requestSignal) { signal = requestSignal; return statusGate.promise; },
    }),
    connectFactory: factory,
  });

  const back = factory.instances[0].options.onBack();
  await flush();
  page.dispose();
  assert.equal(signal.aborted, true);
  statusGate.resolve(pageStatus({ revision: 65, suggested_provider_kind: "claude-code" }));
  await back;

  assert.equal(text(host), "");
  assert.equal(factory.instances.length, 1);
});
