import test from "node:test";
import assert from "node:assert/strict";
import { createOnboardingPage, createOnboardingService } from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";
const flush = () => new Promise((resolve) => setImmediate(resolve)); const microtask = () => Promise.resolve();
function deferred() {
  let resolve; let reject;
  const promise = new Promise((onResolve, onReject) => { resolve = onResolve; reject = onReject; });
  return { promise, resolve, reject };
}
const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const byClass = (root, name) => find(root, (node) => hasClass(node, name));
function providerCard(root, kind) {
  return find(root, (node) => hasClass(node, "onboarding-provider-card") && node.dataset.providerKind === kind);
}
const providerToggle = (root, kind) => byClass(providerCard(root, kind), "onboarding-provider-toggle");
const installButton = (root, kind) => byClass(providerCard(root, kind), "onboarding-provider-install");
function providerStatus(overrides = {}) {
  return onboardingStatus("provider", { revision: 1, ...overrides });
}
function connectStatus(overrides = {}) {
  return providerStatus({
    phase: "connect", provider_kind: "codex", revision: 2, ...overrides,
  });
}
function setupStatus(overrides = {}) {
  return providerStatus({
    phase: "setup", provider_kind: "codex", provider_id: "codex",
    account_id: "account-1", revision: 3, ...overrides,
  });
}
function personaStatus(overrides = {}) {
  return providerStatus({
    phase: "persona", provider_kind: "codex", provider_id: "codex",
    account_id: "account-1", model_id: "gpt-5.6-terra", revision: 4, ...overrides,
  });
}
function setupResult(overrides = {}) {
  return {
    revision: 4, provider_kind: "codex", provider_id: "codex", account_id: "account-1",
    model_id: "gpt-5.6-terra", staged_combo_id: "combo-staged",
    combo_name: "Codex mặc định", ...overrides,
  };
}
function baseService(overrides = {}) {
  return {
    status: () => Promise.resolve(providerStatus()), selectProvider: () => Promise.resolve(connectStatus()),
    setup: () => Promise.resolve(setupResult()), loadAgent: () => Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [] }),
    saveAgent: () => Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [], onboarding_phase: "test", onboarding_revision: 5 }), testChat: () => Promise.reject(new Error("not used")), complete: () => Promise.reject(new Error("not used")),
    ...overrides,
  };
}
function idleConnectService() {
  return {
    connectStart: () => Promise.resolve({ kind: "codex", phase: "detecting" }),
    connectStatus: () => new Promise(() => {}),
    connectCancel: () => Promise.resolve({ ok: true }),
  };
}
function fakeConnectFactory(log = []) {
  const instances = [];
  const factory = (options) => {
    const instance = {
      options, slot: null, starts: [],
      mount(slot) {
        this.slot = slot; log.push("mount"); return this;
      },
      start(value) {
        this.starts.push(value); log.push("start"); return true;
      },
      cancel() { log.push("cancel"); return Promise.resolve(true); },
      dispose() {
        log.push("dispose"); this.slot?.replaceChildren();
      },
    };
    instances.push(instance);
    log.push("create");
    return instance;
  };
  factory.instances = instances;
  return factory;
}
function controlledTimers() {
  let nextID = 1;
  const timers = new Map();
  return {
    setTimeoutFn(callback) {
      const id = nextID++; timers.set(id, callback); return id;
    },
    clearTimeoutFn(id) { timers.delete(id); },
    runAll() {
      for (const [id, callback] of [...timers]) {
        timers.delete(id); callback();
      }
    },
    get size() { return timers.size; },
  };
}
function mountPage(t, options = {}) {
  const dom = installDOM();
  const host = document.createElement("div");
  const page = createOnboardingPage({
    initialStatus: providerStatus(),
    service: baseService(),
    connectService: idleConnectService(),
    ...options,
  });
  page.mount(host);
  t.after(() => { page.dispose(); dom.restore(); });
  return { page, host };
}
test("onboarding service sends exact paths, bodies, and AbortSignals", async () => {
  const calls = [];
  const signal = new AbortController().signal;
  const service = createOnboardingService({
    requestJSON: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/onboarding/provider") return connectStatus({ revision: 8 });
      if (path === "/onboarding/setup") {
        return {
          revision: 9, provider_kind: "codex", provider_id: "codex",
          account_id: "account-7", model_id: "gpt-5.6-terra",
          staged_combo_id: "combo-7", combo_name: "Codex chính",
          access_token: "SECRET", config_dir: "C:/secret",
        };
      }
      return { ok: true };
    },
  });
  assert.ok(Object.isFrozen(service));
  await service.status(signal);
  await service.selectProvider("codex", 7, signal);
  const setup = await service.setup("account-7", 8, signal);
  assert.deepEqual(calls, [
    { path: "/onboarding/status", options: { signal } },
    {
      path: "/onboarding/provider",
      options: { method: "PUT", body: { kind: "codex", revision: 7 }, signal },
    },
    {
      path: "/onboarding/setup",
      options: { method: "POST", body: { account_id: "account-7", revision: 8 }, signal },
    },
  ]);
  assert.deepEqual(setup, {
    phase: "persona", revision: 9, provider_kind: "codex", provider_id: "codex",
    account_id: "account-7", model_id: "gpt-5.6-terra",
    staged_combo_id: "combo-7", combo_name: "Codex chính",
  });
  assert.throws(() => service.selectProvider("Codex", 7), /supported/i);
  assert.throws(() => service.selectProvider("openai", 7), /supported/i);
  assert.throws(() => service.setup(" account-7 ", 8), /account/i);
  assert.throws(() => service.setup("account-7", 0), /revision/i);
});
test("Setup service rejects every non-successor or incomplete backend response", async (t) => {
  const invalid = [
    ["unchanged revision", { revision: 8 }], ["lower revision", { revision: 7 }],
    ["revision jump", { revision: 10 }], ["unsafe revision", { revision: Number.MAX_SAFE_INTEGER + 1 }],
    ["wrong account", { account_id: "other" }],
    ["kind and provider differ", { provider_id: "claude-code" }],
    ["missing model", { model_id: "" }], ["missing staged combo", { staged_combo_id: undefined }],
    ["missing combo name", { combo_name: "" }], ["contradictory phase", { phase: "setup" }],
    ["unsafe staging id", { staged_combo_id: "bad\u0000id" }],
  ];
  for (const [name, overrides] of invalid) {
    await t.test(name, async () => {
      const service = createOnboardingService({
        requestJSON: () => Promise.resolve(setupResult({
          revision: 9, account_id: "account-7", ...overrides,
        })),
      });
      await assert.rejects(service.setup("account-7", 8), /setup response/i);
    });
  }
  let requests = 0;
  const service = createOnboardingService({ requestJSON: () => { requests++; } });
  assert.throws(() => service.setup("account-7", Number.MAX_SAFE_INTEGER), /revision/i);
  assert.equal(requests, 0);
});
test("Welcome renders Tư Vấn Zalo provider setup, an exact three-step rail, and guarded warning", (t) => {
  const { host } = mountPage(t);
  const cards = findAll(host, (node) => hasClass(node, "onboarding-provider-card"));
  const steps = findAll(host, (node) => hasClass(node, "onboarding-step-label"));
  assert.equal(cards.length, 2);
  assert.match(text(cards[0]), /Claude Code.*Chưa chọn/u);
  assert.match(text(cards[1]), /Codex.*Chưa chọn/u);
  assert.deepEqual(steps.map((step) => text(step)), [
    "Kết nối", "Cá nhân hoá", "Trò chuyện thử",
  ]);
  assert.equal(steps.some((step) => /setup|chuẩn bị/i.test(text(step))), false);
  assert.match(text(host), /Tư Vấn Zalo/u);
  assert.match(text(host), /Thiết lập trợ lý Zalo/u);
  assert.equal(button(host, "Tiếp tục"), null);
  assert.match(text(byClass(host, "onboarding-provider-warning")), /đăng nhập/i);
  assert.match(text(byClass(host, "onboarding-provider-warning")), /xác minh/i);
  assert.match(text(byClass(host, "onboarding-provider-warning")), /Hoàn tất/i);
  providerToggle(host, "codex").click();
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "false");
  assert.equal(text(installButton(host, "codex")), "Bấm để cài.");
});
test("Welcome preselects an eligible persisted provider before a suggestion", (t) => {
  const { host } = mountPage(t, {
    initialStatus: providerStatus({
      provider_kind: "claude-code",
      suggested_provider_kind: "codex",
    }),
  });
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "true");
  assert.equal(text(installButton(host, "claude-code")), "Bấm để cài.");
});
test("Welcome ignores unsupported suggestions and keeps Continue disabled", (t) => {
  const { host } = mountPage(t, {
    initialStatus: providerStatus({ suggested_provider_kind: "openai" }),
  });
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "false");
  assert.equal(installButton(host, "codex"), null);
});
test("provider selection uses the current revision once and adopts the server Connect snapshot", async (t) => {
  const selection = deferred();
  const calls = [];
  const connectFactory = fakeConnectFactory();
  const { host } = mountPage(t, {
    initialStatus: providerStatus({ revision: 11, suggested_provider_kind: "codex" }),
    service: baseService({
      selectProvider(kind, revision, signal) {
        calls.push({ kind, revision, signal });
        return selection.promise;
      },
    }),
    connectFactory,
  });
  const retainedInstall = installButton(host, "codex");
  retainedInstall.click();
  retainedInstall.click();
  assert.equal(calls.length, 1);
  assert.deepEqual({ kind: calls[0].kind, revision: calls[0].revision }, {
    kind: "codex",
    revision: 11,
  });
  assert.ok(calls[0].signal instanceof AbortSignal);
  assert.equal(retainedInstall.disabled, true);
  selection.resolve(connectStatus({ revision: 12, provider_kind: "codex" }));
  await flush();
  assert.equal(connectFactory.instances.length, 1);
  assert.equal(connectFactory.instances[0].options.kind, "codex");
  assert.deepEqual(connectFactory.instances[0].starts, [{
    label: "Onboarding",
    onboardingRevision: 12,
    immediate: true,
  }]);
  assert.match(text(host), /Kết nối Codex/);
});
test("provider selection rejects non-successor, wrong-phase, wrong-kind, and staged responses", async (t) => {
  const invalid = [
    ["unchanged", { revision: 11 }], ["lower", { revision: 10 }],
    ["jump", { revision: 13 }], ["unsafe", { revision: Number.MAX_SAFE_INTEGER + 1 }],
    ["wrong phase", { phase: "setup", provider_id: "codex", account_id: "a" }],
    ["wrong kind", { provider_kind: "claude-code" }], ["not required", { required: false }],
    ["staged provider", { provider_id: "codex" }], ["staged account", { account_id: "a" }],
    ["staged model", { model_id: "gpt" }],
  ];
  for (const [name, overrides] of invalid) {
    await t.test(name, async (subtest) => {
      let selects = 0;
      const response = connectStatus({ revision: 12, ...overrides });
      const apiService = createOnboardingService({ requestJSON: () => Promise.resolve(response) });
      await assert.rejects(apiService.selectProvider("codex", 11), /provider response/i);
      const connectFactory = fakeConnectFactory();
      const { host } = mountPage(subtest, {
        initialStatus: providerStatus({ revision: 11, suggested_provider_kind: "codex" }),
        service: baseService({ selectProvider: () => {
          selects++; return Promise.resolve(response);
        } }),
        connectFactory,
      });
      installButton(host, "codex").click();
      await flush();
      assert.equal(selects, 1); assert.equal(connectFactory.instances.length, 0); assert.ok(byClass(host, "onboarding-error"));
    });
  }
  await t.test("maximum safe revision never mutates", (subtest) => {
    let selects = 0; const apiService = createOnboardingService({ requestJSON: () => { selects++; } });
    assert.throws(() => apiService.selectProvider("codex", Number.MAX_SAFE_INTEGER), /revision/i);
    const { host } = mountPage(subtest, {
      initialStatus: providerStatus({ revision: Number.MAX_SAFE_INTEGER, suggested_provider_kind: "codex" }),
      service: baseService({ selectProvider: () => { selects++; } }),
    });
    installButton(host, "codex").click(); assert.equal(selects, 0);
    assert.ok(byClass(host, "onboarding-error"));
  });
});
test("disposed and stale provider selections cannot render Connect", async (t) => {
  const selection = deferred();
  const connectFactory = fakeConnectFactory();
  const { page, host } = mountPage(t, {
    initialStatus: providerStatus({ suggested_provider_kind: "codex" }),
    service: baseService({ selectProvider: () => selection.promise }),
    connectFactory,
  });
  installButton(host, "codex").click();
  page.dispose();
  selection.resolve(connectStatus());
  await flush();
  assert.equal(connectFactory.instances.length, 0);
  assert.equal(text(host), "");
});
test("selection reconciliation fails safely and Retry reloads authoritative status", async (t) => {
  const selectCalls = [];
  let statusCalls = 0;
  const { host } = mountPage(t, {
    initialStatus: providerStatus({ revision: 3, suggested_provider_kind: "codex" }),
    service: baseService({
      selectProvider(kind, revision) {
        selectCalls.push({ kind, revision });
        if (selectCalls.length === 1) return Promise.reject(new Error("SECRET token=abc"));
        return Promise.resolve(connectStatus({ revision: 10 }));
      },
      status() {
        statusCalls++;
        return Promise.resolve(providerStatus({
          revision: statusCalls === 1 ? 3 : 9, suggested_provider_kind: "codex",
        }));
      },
    }),
    connectFactory: fakeConnectFactory(),
  });
  installButton(host, "codex").click();
  await flush();
  assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
  assert.doesNotMatch(text(host), /SECRET|token=abc/);
  assert.equal(statusCalls, 1);
  button(host, "Thử lại").click();
  await flush();
  assert.equal(statusCalls, 2);
  installButton(host, "codex").click();
  await flush();
  assert.deepEqual(selectCalls[1], { kind: "codex", revision: 9 });
});
test("Connect resume creates and starts exactly one component without re-PUT", (t) => {
  let selects = 0;
  const connectFactory = fakeConnectFactory();
  const { page, host } = mountPage(t, {
    initialStatus: connectStatus({ revision: 6, provider_kind: "claude-code" }),
    service: baseService({ selectProvider: () => { selects++; } }),
    connectFactory,
  });
  page.mount(host);
  assert.equal(selects, 0);
  assert.equal(connectFactory.instances.length, 1);
  assert.deepEqual(connectFactory.instances[0].starts, [{
    label: "Onboarding",
    onboardingRevision: 6,
    immediate: true,
  }]);
  assert.equal(connectFactory.instances[0].options.kind, "claude-code");
});
test("Back invalidates, cancels, disposes, then GETs without restarting a persisted Connect", async (t) => {
  const order = [];
  const cancelGate = deferred();
  let setupCalls = 0, statusCalls = 0, selectCalls = 0, factoryCalls = 0;
  let captured;
  const connectFactory = (options) => {
    factoryCalls++;
    captured = options;
    return {
      mount: () => { order.push("mount"); },
      start: () => { order.push("start"); },
      cancel: () => { order.push("cancel"); return cancelGate.promise; },
      dispose: () => { order.push("dispose"); },
    };
  };
  const { host } = mountPage(t, {
    initialStatus: connectStatus({ revision: 6 }),
    service: baseService({
      status: () => { order.push("status"); statusCalls++; return Promise.resolve(connectStatus({ revision: 6 })); },
      selectProvider: () => { selectCalls++; return Promise.resolve(connectStatus({ revision: 7 })); },
      setup: () => { setupCalls++; return Promise.resolve({}); },
    }),
    connectFactory,
  });
  const backPromise = captured.onBack();
  const lateTerminal = captured.onConnected({
    kind: "codex",
    providerId: "codex",
    accountId: "account-1",
  });
  await microtask();
  assert.equal(statusCalls, 0);
  assert.equal(setupCalls, 0);
  cancelGate.resolve(true);
  await Promise.all([backPromise, lateTerminal]);
  assert.deepEqual(order.slice(-3), ["cancel", "dispose", "status"]);
  assert.equal(statusCalls, 1);
  assert.equal(selectCalls, 0);
  assert.equal(factoryCalls, 1);
  assert.equal(setupCalls, 0);
  assert.equal(button(host, "Thử lại").tagName, "BUTTON");
  assert.doesNotMatch(text(host), /Chọn nhà cung cấp/u);
});
test("a connected terminal reconciles authoritative Setup and deduplicates the POST", async (t) => {
  const setupGate = deferred();
  const timers = controlledTimers();
  const calls = [];
  let captured;
  const { host } = mountPage(t, {
    initialStatus: connectStatus({ revision: 6 }),
    service: baseService({
      status(signal) {
        calls.push({ op: "status", signal });
        return Promise.resolve(setupStatus({ revision: 7 }));
      },
      setup(accountId, revision, signal) {
        calls.push({ op: "setup", accountId, revision, signal });
        return setupGate.promise;
      },
    }),
    connectFactory(options) {
      captured = options;
      return { mount() {}, start() {}, cancel: () => Promise.resolve(true), dispose() {} };
    },
    ...timers,
  });
  const terminal = { kind: "codex", providerId: "codex", accountId: "account-1" };
  const first = captured.onConnected(terminal);
  const duplicate = captured.onConnected(terminal);
  assert.equal(first, duplicate);
  await flush();
  const whileSetupIsPending = captured.onConnected(terminal);
  assert.equal(whileSetupIsPending, first);
  assert.deepEqual(calls.map((call) => call.op), ["status", "setup"]);
  assert.deepEqual(
    { accountId: calls[1].accountId, revision: calls[1].revision },
    { accountId: "account-1", revision: 7 },
  );
  assert.match(text(host), /Đang chuẩn bị cấu hình…/);
  setupGate.resolve(setupResult({ revision: 8 }));
  await microtask();
  await microtask();
  assert.match(text(host), /✓/);
  assert.equal(timers.size, 1);
  timers.runAll();
  await flush();
  assert.match(text(host), /Trợ lý của bạn là ai/);
  await Promise.all([first, duplicate, whileSetupIsPending]);
});
test("Connect reconciliation accepts only the exact successor Setup snapshot", async (t) => {
  const invalid = [
    ["unchanged", { revision: 6 }], ["lower", { revision: 5 }], ["jump", { revision: 8 }],
    ["unsafe", { revision: Number.MAX_SAFE_INTEGER + 1 }],
    ["wrong phase", { phase: "persona", model_id: "gpt" }],
    ["wrong kind", { provider_kind: "claude-code", provider_id: "claude-code" }],
    ["wrong provider", { provider_id: "claude-code" }],
    ["wrong account", { account_id: "other" }], ["not required", { required: false }],
    ["already modeled", { model_id: "gpt" }],
  ];
  for (const [name, overrides] of invalid) {
    await t.test(name, async (subtest) => {
      let setups = 0;
      const connectFactory = fakeConnectFactory();
      const { host } = mountPage(subtest, {
        initialStatus: connectStatus({ revision: 6 }),
        service: baseService({
          status: () => Promise.resolve(setupStatus({ revision: 7, ...overrides })),
          setup: () => { setups++; },
        }),
        connectFactory,
      });
      await connectFactory.instances[0].options.onConnected({
        kind: "codex", providerId: "codex", accountId: "account-1",
      });
      assert.equal(setups, 0);
      assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
    });
  }
  await t.test("maximum Connect revision never fetches status", async (subtest) => {
    let statuses = 0;
    const connectFactory = fakeConnectFactory();
    const { host } = mountPage(subtest, {
      initialStatus: connectStatus({ revision: Number.MAX_SAFE_INTEGER }),
      service: baseService({ status: () => { statuses++; } }),
      connectFactory,
    });
    await connectFactory.instances[0].options.onConnected({
      kind: "codex", providerId: "codex", accountId: "account-1",
    });
    assert.equal(statuses, 0);
    assert.ok(byClass(host, "onboarding-error"));
  });
});
test("Setup page rejects every invalid response without rendering Persona", async (t) => {
  const invalid = [
    ["unchanged", { revision: 3 }], ["lower", { revision: 2 }], ["jump", { revision: 5 }],
    ["unsafe", { revision: Number.MAX_SAFE_INTEGER + 1 }], ["wrong phase", { phase: "setup" }],
    ["wrong kind", { provider_kind: "claude-code", provider_id: "claude-code" }],
    ["wrong provider", { provider_id: "claude-code" }], ["wrong account", { account_id: "other" }],
    ["missing model", { model_id: "" }], ["missing staging", { staged_combo_id: undefined }],
    ["missing combo name", { combo_name: "" }], ["unsafe combo", { combo_name: "bad\u0000name" }],
  ];
  for (const [name, overrides] of invalid) {
    await t.test(name, async (subtest) => {
      let setups = 0;
      const { host } = mountPage(subtest, {
        initialStatus: setupStatus({ revision: 3 }),
        service: baseService({ setup: () => {
          setups++;
          return Promise.resolve(setupResult({ revision: 4, ...overrides }));
        } }),
      });
      await flush();
      assert.equal(setups, 1);
      assert.equal(byClass(host, "onboarding-persona-stage"), null);
      assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
    });
  }
  await t.test("maximum Setup revision never posts", (subtest) => {
    let setups = 0;
    const { host } = mountPage(subtest, {
      initialStatus: setupStatus({ revision: Number.MAX_SAFE_INTEGER }),
      service: baseService({ setup: () => { setups++; } }),
    });
    assert.equal(setups, 0);
    assert.ok(byClass(host, "onboarding-error"));
  });
});
test("Setup resume auto-runs once, shows failure, and Retry starts exactly one new request", async (t) => {
  const first = deferred();
  const second = deferred();
  const calls = [];
  let statusCalls = 0;
  const { host } = mountPage(t, {
    initialStatus: setupStatus({ revision: 21, account_id: "resume-account" }),
    service: baseService({
      status() {
        statusCalls++;
        return Promise.resolve(setupStatus({ revision: 21, account_id: "resume-account" }));
      },
      setup(accountId, revision, signal) {
        calls.push({ accountId, revision, signal });
        return calls.length === 1 ? first.promise : second.promise;
      },
    }),
  });
  assert.equal(calls.length, 1);
  assert.deepEqual(
    { accountId: calls[0].accountId, revision: calls[0].revision },
    { accountId: "resume-account", revision: 21 },
  );
  assert.match(text(host), /Đang chuẩn bị cấu hình…/);
  first.reject(new Error("private config path"));
  await flush();
  assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
  assert.doesNotMatch(text(host), /private config path/);
  const retry = button(host, "Thử lại");
  retry.click();
  retry.click();
  await flush();
  assert.equal(statusCalls, 1);
  assert.equal(calls.length, 2);
  second.resolve(setupResult({ revision: 22, account_id: "resume-account" }));
  await flush();
});
test("Setup Retry renders an authoritative non-Setup phase without stale POST", async (t) => {
  const cases = [
    ["persona", personaStatus({ revision: 9 }), "onboarding-persona-stage", "Trợ lý của bạn là ai"],
    ["test", { ...personaStatus({ revision: 9 }), phase: "test" }, "onboarding-test-stage", "Thử trò chuyện với Bé Mi"],
    ["completed", providerStatus({ phase: "completed", revision: 9 }), "onboarding-done-stage", "Bé Mi đã sẵn sàng!"],
    ["provider", providerStatus({ revision: 9 }), "onboarding-provider-stage", "Thiết lập trợ lý Zalo"],
    ["connect", connectStatus({ revision: 9 }), "onboarding-connect-stage", "Kết nối Codex"],
  ];
  for (const [name, authoritative, className, heading] of cases) {
    await t.test(name, async (subtest) => {
      let setups = 0;
      let statuses = 0;
      const { host } = mountPage(subtest, {
        initialStatus: setupStatus({ revision: 3 }),
        service: baseService({
          setup: () => { setups++; return Promise.reject(new Error("conflict")); },
          status: () => { statuses++; return Promise.resolve(authoritative); },
        }),
        connectFactory: fakeConnectFactory(),
      });
      await flush();
      button(host, "Thử lại").click();
      await flush();
      assert.equal(statuses, 1);
      assert.equal(setups, 1);
      assert.ok(byClass(host, className));
      assert.equal(text(find(host, (node) => node.tagName === "H1")), heading);
      const dialog = byClass(host, "onboarding-dialog");
      assert.equal(dialog?.getAttribute("aria-modal"), "true");
      assert.ok(byClass(host, "onboarding-dashboard"));
    });
  }
});
test("Setup Retry uses the refreshed revision and provider/account identity exactly once", async (t) => {
  const cases = [
    ["revision advanced", setupStatus({ revision: 9 })],
    ["identity changed", setupStatus({
      revision: 12, provider_kind: "claude-code", provider_id: "claude-code", account_id: "claude-account",
    })],
  ];
  for (const [name, authoritative] of cases) {
    await t.test(name, async (subtest) => {
      const calls = [];
      const { host } = mountPage(subtest, {
        initialStatus: setupStatus({ revision: 3 }),
        service: baseService({
          status: () => Promise.resolve(authoritative),
          setup(accountId, revision) {
            calls.push({ accountId, revision });
            if (calls.length === 1) return Promise.reject(new Error("stale"));
            return Promise.resolve(setupResult({
              revision: revision + 1,
              provider_kind: authoritative.provider_kind,
              provider_id: authoritative.provider_id,
              account_id: authoritative.account_id,
            }));
          },
        }),
      });
      await flush();
      const retry = button(host, "Thử lại");
      retry.click();
      retry.click();
      await flush();
      assert.deepEqual(calls, [
        { accountId: "account-1", revision: 3 },
        { accountId: authoritative.account_id, revision: authoritative.revision },
      ]);
    });
  }
});
test("Setup Retry survives a status error, retries GET, and suppresses a disposed stale status", async (t) => {
  let statuses = 0; let setups = 0;
  const staleStatus = deferred();
  const { page, host } = mountPage(t, {
    initialStatus: setupStatus({ revision: 3 }),
    service: baseService({
      setup: () => { setups++; return Promise.reject(new Error("setup failed")); },
      status: () => {
        statuses++;
        if (statuses === 1) return Promise.reject(new Error("status failed"));
        return staleStatus.promise;
      },
    }),
  });
  await flush();
  button(host, "Thử lại").click();
  await flush();
  assert.equal(statuses, 1); assert.equal(setups, 1);
  button(host, "Thử lại").click();
  await flush();
  assert.equal(statuses, 2);
  page.dispose();
  staleStatus.resolve(setupStatus({ revision: 9 }));
  await flush();
  assert.equal(setups, 1); assert.equal(text(host), "");
});
test("malformed Setup resume fails closed until status Retry supplies an account", async (t) => {
  let setupCalls = 0;
  let statusCalls = 0;
  const { host } = mountPage(t, {
    initialStatus: setupStatus({ account_id: "" }),
    service: baseService({
      status: () => {
        statusCalls++; return Promise.resolve(setupStatus({
          account_id: "restored-account", revision: 30,
        }));
      },
      setup: () => { setupCalls++; return new Promise(() => {}); },
    }),
  });
  assert.equal(setupCalls, 0);
  assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
  button(host, "Thử lại").click();
  await flush();
  assert.equal(statusCalls, 1); assert.equal(setupCalls, 1);
});
test("Persona resume renders directly without PUT, Connect, or Setup", async (t) => {
  let selects = 0; let setups = 0;
  const connectFactory = fakeConnectFactory();
  const { host } = mountPage(t, {
    initialStatus: personaStatus(),
    service: baseService({
      selectProvider: () => { selects++; },
      setup: () => { setups++; },
    }),
    connectFactory,
  });
  await flush();
  assert.match(text(host), /Trợ lý của bạn là ai/);
  assert.equal(selects, 0); assert.equal(setups, 0);
  assert.equal(connectFactory.instances.length, 0);
});
test("unknown, non-required, and malformed initial snapshots show retryable safe errors", async (t) => {
  for (const [name, initialStatus] of [
    ["unknown", providerStatus({ phase: "invented", secret: "DO-NOT-RENDER" })],
    ["not required", providerStatus({ required: false })],
    ["bad revision", providerStatus({ revision: 0 })],
  ]) {
    await t.test(name, async (subtest) => {
      let retries = 0;
      const { host } = mountPage(subtest, {
        initialStatus,
        service: baseService({
          status: () => {
            retries++; return Promise.resolve(providerStatus({ phase: "still-invalid" }));
          },
        }),
      });
      assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
      assert.doesNotMatch(text(host), /DO-NOT-RENDER|invented|still-invalid/);
      button(host, "Thử lại").click();
      await flush();
      assert.equal(retries, 1);
      assert.equal(byClass(host, "onboarding-error").getAttribute("role"), "alert");
    });
  }
});
test("dispose aborts Setup, clears timers/listeners, and suppresses stale rendering and callbacks", async (t) => {
  const gate = deferred();
  const timers = controlledTimers();
  let signal;
  let completions = 0;
  const { page, host } = mountPage(t, {
    initialStatus: setupStatus(),
    service: baseService({
      setup(_accountId, _revision, setupSignal) {
        signal = setupSignal;
        return gate.promise;
      },
    }),
    onComplete: () => { completions++; },
    ...timers,
  });
  page.dispose();
  page.dispose();
  assert.equal(signal.aborted, true);
  gate.resolve(setupResult());
  await flush();
  assert.equal(text(host), "");
  assert.equal(timers.size, 0);
  assert.equal(completions, 0);
  assert.equal(page.mount(host), page);
  assert.equal(text(host), "");
});
test("same-host remount does not duplicate provider listeners", async (t) => {
  let selects = 0;
  const gate = deferred();
  const { page, host } = mountPage(t, {
    initialStatus: providerStatus({ suggested_provider_kind: "codex" }),
    service: baseService({
      selectProvider: () => { selects++; return gate.promise; },
    }),
  });
  const retained = installButton(host, "codex");
  page.mount(host);
  retained.click();
  retained.click();
  assert.equal(selects, 1);
});
test("the real Provider Connect starts immediately and receives onboarding revision", async (t) => {
  const connectCalls = [];
  const setupCalls = [];
  const timers = controlledTimers();
  const { host } = mountPage(t, {
    initialStatus: connectStatus({ revision: 51 }),
    connectService: {
      connectStart(kind, label, onboardingRevision) {
        connectCalls.push({ kind, label, onboardingRevision });
        return Promise.resolve({
          kind, phase: "connected", providerId: kind, accountId: "actual-account",
        });
      },
      connectStatus: () => Promise.reject(new Error("must not poll after terminal start")),
      connectCancel: () => Promise.resolve({ ok: true }),
    },
    service: baseService({
      status: () => Promise.resolve(setupStatus({ revision: 52, account_id: "actual-account" })),
      setup(accountId, revision) {
        setupCalls.push({ accountId, revision });
        return Promise.resolve(setupResult({ revision: 53, account_id: accountId }));
      },
    }),
    ...timers,
  });
  const starts = findAll(host, (node) => node.tagName === "BUTTON"
    && text(node).includes("Bắt đầu kết nối"));
  assert.equal(starts.length, 0, "immediate onboarding has no second start prompt");
  await flush();
  await flush();
  assert.deepEqual(connectCalls, [{ kind: "codex", label: "Onboarding", onboardingRevision: 51 }]);
  assert.deepEqual(setupCalls, [{ accountId: "actual-account", revision: 52 }]);
});
