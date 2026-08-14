import test from "node:test";
import assert from "node:assert/strict";

import {
  createWelcomeStage,
  focusProviderControl,
} from "../overlay/internal/webui/static/pages/onboarding-early-view.js";
import {
  createOnboardingPage,
  createOnboardingService,
} from "../overlay/internal/webui/static/pages/onboarding.js";
import { requestJSON as sharedRequestJSON } from "../overlay/internal/webui/static/core/api.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const byClass = (root, name) => find(root, (node) => hasClass(node, name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const providerRow = (root, kind) => find(root, (node) =>
  hasClass(node, "onboarding-provider-card") && node.dataset.providerKind === kind);
const providerToggle = (root, kind) => find(root, (node) =>
  hasClass(node, "onboarding-provider-toggle") && node.dataset.providerKind === kind);
const installButton = (root, kind) => byClass(providerRow(root, kind), "onboarding-provider-install");
const confirmation = (root) => find(root, (node) => node.getAttribute?.("role") === "alertdialog");

function mountWelcome(t, initialSelection = "", message = "") {
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
    message,
    listen,
    onSelect(kind) {
      events.push({ type: "select", kind });
      selectedProvider = kind;
      render();
    },
    onProceed() { events.push({ type: "install", kind: selectedProvider }); },
    onRetry() { events.push({ type: "retry" }); },
  }));
  render();
  t.after(() => dom.restore());
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
function setupStatus(overrides = {}) {
  return pageStatus({
    phase: "setup", provider_kind: "codex", provider_id: "codex",
    account_id: "account-1", revision: 3, ...overrides,
  });
}
function pageService(overrides = {}) {
  return {
    status: () => Promise.resolve(pageStatus()),
    selectProvider: () => Promise.resolve(connectionStatus()),
    setup: () => new Promise(() => {}),
    loadAgent: () => new Promise(() => {}),
    saveAgent: () => new Promise(() => {}),
    testChat: () => new Promise(() => {}),
    complete: () => new Promise(() => {}),
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
  assert.deepEqual(events, [{ type: "select", kind: "codex" }]);
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

test("turning the current provider OFF opens a named inline confirmation", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");

  origin.click();

  const warning = confirmation(host);
  assert.ok(warning);
  assert.equal(warning.getAttribute("aria-labelledby"), "onboarding-provider-confirm-title");
  assert.equal(warning.getAttribute("aria-describedby"), "onboarding-provider-confirm-description");
  assert.equal(warning.hasAttribute("aria-modal"), false);
  assert.equal(text(byClass(warning, "onboarding-provider-confirm-description")),
    "Nếu tắt Codex trước khi cài, lựa chọn này sẽ bị bỏ. Bạn vẫn muốn tắt chứ?");
  assert.equal(document.activeElement, button(warning, "Huỷ"));
  assert.deepEqual(events, []);
});

test("Huỷ closes confirmation, keeps selection, and restores toggle focus", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");
  origin.click();

  button(confirmation(host), "Huỷ").click();

  assert.equal(confirmation(host), null);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(document.activeElement, origin);
  assert.deepEqual(events, []);
});

test("Escape cancels confirmation and restores the originating toggle", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");
  origin.click();

  const warning = confirmation(host);
  const event = { type: "keydown", key: "Escape" };
  warning.dispatchEvent(event);

  assert.equal(event.defaultPrevented, true);
  assert.equal(confirmation(host), null);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(document.activeElement, origin);
  assert.deepEqual(events, []);
});

test("Vẫn tắt applies OFF only after confirmation", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  providerToggle(host, "codex").click();

  button(confirmation(host), "Vẫn tắt").click();

  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
  assert.equal(installButton(host, "codex"), null);
  assert.deepEqual(events, [{ type: "select", kind: "" }]);
});

test("switching providers confirms before applying the replacement", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const replacement = providerToggle(host, "claude-code");

  replacement.click();

  assert.ok(confirmation(host));
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "false");
  assert.deepEqual(events, []);

  button(confirmation(host), "Vẫn tắt").click();
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "true");
  assert.deepEqual(events, [{ type: "select", kind: "claude-code" }]);
});

test("confirmation disables every background toggle and install action", (t) => {
  const { host } = mountWelcome(t, "codex");
  providerToggle(host, "claude-code").click();

  const controls = [
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-toggle")),
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-install")),
  ];
  assert.ok(controls.length > 0);
  assert.ok(controls.every((control) => control.disabled));
  assert.equal(button(confirmation(host), "Huỷ").disabled, false);
  assert.equal(button(confirmation(host), "Vẫn tắt").disabled, false);
});

test("confirmation locks Retry until cancellation", (t) => {
  const { host, events } = mountWelcome(t, "codex", "Không thể kết nối");
  const retry = button(host, "Thử lại");
  assert.equal(retry.disabled, false);

  providerToggle(host, "codex").click();

  assert.equal(retry.disabled, true);
  retry.click();
  assert.deepEqual(events, []);

  button(confirmation(host), "Huỷ").click();
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
    selectedProvider: "codex",
    message: "",
    listen,
    onSelect() {},
    onProceed() {},
    onRetry() {},
  });

  assert.equal(focusProviderControl(stage, "codex"), true);
  assert.equal(document.activeElement, providerToggle(stage, "codex"));
  assert.notEqual(document.activeElement, providerRow(stage, "codex"));
});

test("confirmed OFF deselects locally and only supported selections receive focus", (t) => {
  let selects = 0;
  const { host } = mountPage(t, {
    initialStatus: pageStatus({ suggested_provider_kind: "codex" }),
    service: pageService({ selectProvider: () => { selects++; return new Promise(() => {}); } }),
  });

  providerToggle(host, "codex").click();
  button(confirmation(host), "Vẫn tắt").click();
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "false");
  assert.notEqual(document.activeElement, providerToggle(host, "codex"));

  providerToggle(host, "claude-code").click();
  assert.equal(document.activeElement, providerToggle(host, "claude-code"));
  assert.equal(selects, 0);
});

test("install CTA selects once with pinned context and immediately starts Connect", async (t) => {
  const selection = deferred();
  const calls = [];
  const factory = connectFactory();
  const { host, page } = mountPage(t, {
    initialStatus: pageStatus({ revision: 11, suggested_provider_kind: "codex" }),
    service: pageService({
      selectProvider(kind, revision, signal) {
        calls.push({ kind, revision, signal });
        return selection.promise;
      },
    }),
    connectFactory: factory,
  });
  const install = installButton(host, "codex");

  install.click();
  install.click();
  page.mount(host);
  install.click();

  assert.equal(calls.length, 1);
  assert.deepEqual({ kind: calls[0].kind, revision: calls[0].revision }, {
    kind: "codex", revision: 11,
  });
  assert.ok(calls[0].signal instanceof AbortSignal);
  assert.equal(calls[0].signal.aborted, false);

  selection.resolve(connectionStatus({ revision: 12 }));
  await settle();
  assert.equal(factory.instances.length, 1);
  assert.equal(factory.instances[0].options.kind, "codex");
  assert.deepEqual(factory.instances[0].starts, [{
    label: "Onboarding", onboardingRevision: 12, immediate: true,
  }]);
  assert.match(text(host), /Kết nối Codex/u);
});

test("lost selection response reconciles its exact successor without another PUT", async (t) => {
  let selects = 0; let statuses = 0;
  const factory = connectFactory();
  const { host } = mountPage(t, {
    initialStatus: pageStatus({ revision: 21, suggested_provider_kind: "claude-code" }),
    service: pageService({
      selectProvider() { selects++; return Promise.reject(new Error("private selection failure")); },
      status() {
        statuses++;
        return Promise.resolve(connectionStatus({
          provider_kind: "claude-code", revision: 22,
        }));
      },
    }),
    connectFactory: factory,
  });

  installButton(host, "claude-code").click();
  await settle();

  assert.equal(selects, 1);
  assert.equal(statuses, 1);
  assert.equal(factory.instances.length, 1);
  assert.deepEqual(factory.instances[0].starts, [{
    label: "Onboarding", onboardingRevision: 22, immediate: true,
  }]);
});

test("unchanged reconciliation keeps the requested row ON and retries its revision", async (t) => {
  const snapshot = pageStatus({ revision: 31, suggested_provider_kind: "codex" });
  const calls = [];
  let statuses = 0;
  const factory = connectFactory();
  const { host } = mountPage(t, {
    initialStatus: snapshot,
    service: pageService({
      selectProvider(kind, revision) {
        calls.push({ kind, revision });
        return calls.length === 1
          ? Promise.reject(new Error("SECRET first response"))
          : Promise.resolve(connectionStatus({ revision: 32 }));
      },
      status() { statuses++; return Promise.resolve({ ...snapshot }); },
    }),
    connectFactory: factory,
  });

  installButton(host, "codex").click();
  await settle();
  assert.equal(statuses, 1);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
  assert.doesNotMatch(text(host), /SECRET/u);

  installButton(host, "codex").click();
  await settle();
  assert.deepEqual(calls, [
    { kind: "codex", revision: 31 },
    { kind: "codex", revision: 31 },
  ]);
  assert.equal(factory.instances.length, 1);
});

test("moved reconciliation adopts and renders the authoritative phase", async (t) => {
  const setupCalls = [];
  const factory = connectFactory();
  const { host } = mountPage(t, {
    initialStatus: pageStatus({ revision: 41, suggested_provider_kind: "codex" }),
    service: pageService({
      selectProvider: () => Promise.reject(new Error("lost")),
      status: () => Promise.resolve(setupStatus({ revision: 47 })),
      setup(accountID, revision, signal) {
        setupCalls.push({ accountID, revision, signal });
        return new Promise(() => {});
      },
    }),
    connectFactory: factory,
  });

  installButton(host, "codex").click();
  await settle();
  assert.match(text(host), /Hàng đợi thiết lập/u);
  assert.equal(factory.instances.length, 0);
  assert.equal(setupCalls.length, 1);
  assert.deepEqual({ accountID: setupCalls[0].accountID, revision: setupCalls[0].revision }, {
    accountID: "account-1", revision: 47,
  });
});

test("invalid selection and unavailable reconciliation fail closed without secrets", async (t) => {
  const cases = [
    ["invalid 200", () => Promise.resolve({ phase: "connect", secret: "RAW-200" }), null, 0],
    ["malformed status", () => Promise.reject(new Error("RAW-PUT")),
      () => Promise.resolve({ phase: "connect", secret: "RAW-STATUS" }), 1],
    ["status rejection", () => Promise.reject(new Error("RAW-PUT")),
      () => Promise.reject(new Error("RAW-GET")), 1],
  ];
  for (const [name, selectProvider, statusResult, expectedStatuses] of cases) {
    await t.test(name, async (subtest) => {
      let statuses = 0;
      const factory = connectFactory();
      const { host } = mountPage(subtest, {
        initialStatus: pageStatus({ suggested_provider_kind: "codex" }),
        service: pageService({
          selectProvider,
          status() { statuses++; return statusResult?.(); },
        }),
        connectFactory: factory,
      });
      installButton(host, "codex").click();
      await settle();
      assert.equal(statuses, expectedStatuses);
      assert.equal(factory.instances.length, 0);
      assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
      assert.doesNotMatch(text(host), /RAW|secret/iu);
    });
  }
});

test("real service distinguishes invalid provider responses from uncertain requests", async (t) => {
  await t.test("malformed HTTP 200 does not reconcile", async (subtest) => {
    const requests = [];
    const factory = connectFactory();
    const service = createOnboardingService({
      requestJSON(path, options = {}) {
        requests.push(`${options.method || "GET"} ${path}`);
        if (path === "/onboarding/provider") {
          return Promise.resolve(connectionStatus({
            revision: 72, provider_id: "codex", secret: "RAW-HTTP-200",
          }));
        }
        return Promise.resolve(connectionStatus({ revision: 72 }));
      },
    });
    const { host } = mountPage(subtest, {
      initialStatus: pageStatus({ revision: 71, suggested_provider_kind: "codex" }),
      service,
      connectFactory: factory,
    });

    installButton(host, "codex").click();
    await settle();
    assert.deepEqual(requests, ["PUT /onboarding/provider"]);
    assert.equal(factory.instances.length, 0);
    assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
    assert.doesNotMatch(text(host), /RAW-HTTP-200/u);
  });

  await t.test("request rejection still reconciles with GET", async (subtest) => {
    const requests = [];
    const factory = connectFactory();
    const service = createOnboardingService({
      requestJSON(path, options = {}) {
        requests.push(`${options.method || "GET"} ${path}`);
        if (path === "/onboarding/provider") {
          return Promise.reject(new Error("private network failure"));
        }
        return Promise.resolve(connectionStatus({ revision: 82 }));
      },
    });
    const { host } = mountPage(subtest, {
      initialStatus: pageStatus({ revision: 81, suggested_provider_kind: "codex" }),
      service,
      connectFactory: factory,
    });

    installButton(host, "codex").click();
    await settle();
    assert.deepEqual(requests, [
      "PUT /onboarding/provider", "GET /onboarding/status",
    ]);
    assert.equal(factory.instances.length, 1);
    assert.deepEqual(factory.instances[0].starts, [{
      label: "Onboarding", onboardingRevision: 82, immediate: true,
    }]);
  });
});

test("malformed JSON from a real HTTP 200 does not reconcile", async (t) => {
  const requests = [];
  const factory = connectFactory();
  const fetchImpl = async (path, init) => {
    requests.push(`${init.method} ${path}`);
    if (path === "/onboarding/provider") {
      return new Response("{malformed", {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(JSON.stringify(connectionStatus({ revision: 92 })), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  const service = createOnboardingService({
    requestJSON: (path, options) => sharedRequestJSON(path, { ...options, fetchImpl }),
  });
  const { host } = mountPage(t, {
    initialStatus: pageStatus({ revision: 91, suggested_provider_kind: "codex" }),
    service,
    connectFactory: factory,
  });

  installButton(host, "codex").click();
  await settle();
  assert.deepEqual(requests, ["PUT /onboarding/provider"]);
  assert.equal(factory.instances.length, 0);
  assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
});

test("disposing an in-flight selection prevents reconciliation and late rendering", async (t) => {
  const selection = deferred();
  let signal; let statuses = 0;
  const factory = connectFactory();
  const { host, page } = mountPage(t, {
    initialStatus: pageStatus({ suggested_provider_kind: "codex" }),
    service: pageService({
      selectProvider(_kind, _revision, requestSignal) {
        signal = requestSignal;
        return selection.promise;
      },
      status() { statuses++; return Promise.resolve(connectionStatus()); },
    }),
    connectFactory: factory,
  });

  installButton(host, "codex").click();
  page.dispose();
  assert.equal(signal.aborted, true);
  selection.reject(new Error("late private failure"));
  await settle();
  assert.equal(statuses, 0);
  assert.equal(factory.instances.length, 0);
  assert.equal(text(host), "");
});

test("maximum safe provider revision makes no service or Connect call", (t) => {
  const calls = [];
  const factory = connectFactory();
  const { host } = mountPage(t, {
    initialStatus: pageStatus({
      revision: Number.MAX_SAFE_INTEGER, suggested_provider_kind: "codex",
    }),
    service: pageService({
      selectProvider: () => { calls.push("select"); },
      status: () => { calls.push("status"); },
      setup: () => { calls.push("setup"); },
    }),
    connectFactory: factory,
  });

  installButton(host, "codex").click();
  assert.deepEqual(calls, []);
  assert.equal(factory.instances.length, 0);
  assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
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
