import test from "node:test";
import assert from "node:assert/strict";
import { createOnboardingPage, createOnboardingService } from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus, pendingProvider, readyProvider } from "./helpers/onboarding-fixtures.mjs";
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
function setupSuccess({
  revision = 4, kind = "codex", accountID = "account-1", modelID = "gpt-5.6-terra",
} = {}) {
  const stage = readyProvider(kind, 0, {
    provider_id: kind, account_id: accountID, model_id: modelID,
  });
  return onboardingStatus("persona", { revision, providers: [stage] });
}
function baseService(overrides = {}) {
  const status = overrides.status ?? (() => Promise.resolve(providerStatus()));
  return {
    status,
    bootstrapStatus: overrides.bootstrapStatus ?? status,
    updateProviders: (kinds, revision) => Promise.resolve(providerStatus({
      revision: revision + 1,
      providers: kinds.map((kind, position) => pendingProvider(kind, position)),
    })),
    beginProvider: () => Promise.resolve(connectStatus()),
    backToProviders: () => Promise.resolve(providerStatus()),
    selectProvider: () => Promise.resolve(connectStatus()),
    setup: () => Promise.resolve(setupSuccess()), loadAgent: () => Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [] }),
    bootstrap: () => new Promise(() => {}),
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
test("Welcome renders Provider setup without a wizard rail and explains automatic bootstrap", async (t) => {
  const { host } = mountPage(t);
  const cards = findAll(host, (node) => hasClass(node, "onboarding-provider-card"));
  const steps = findAll(host, (node) => hasClass(node, "onboarding-step-label"));
  assert.equal(cards.length, 2);
  assert.match(text(cards[0]), /Codex.*Chưa dùng/u);
  assert.match(text(cards[1]), /Claude Code.*Chưa dùng/u);
  assert.equal(steps.length, 0);
  assert.equal(byClass(host, "onboarding-rail"), null);
  assert.match(text(host), /Tư Vấn Zalo/u);
  assert.match(text(host), /Thiết lập trợ lý Zalo/u);
  assert.equal(button(host, "Tiếp tục"), null);
  const warning = text(byClass(host, "onboarding-provider-warning"));
  assert.match(warning, /đăng nhập.*kết nối được xác minh.*Đang chuẩn bị trợ lý…/iu);
  assert.doesNotMatch(warning, /Chat thử|bấm Hoàn tất|Persona|cá nhân hoá/iu);
  providerToggle(host, "codex").click();
  await flush();
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "false");
  assert.equal(text(installButton(host, "codex")), "Bấm để cài.");
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
  const calls = [];
  let bootstraps = 0;
  let captured;
  const { host } = mountPage(t, {
    initialStatus: connectStatus({ revision: 6 }),
    service: baseService({
      status(signal) {
        calls.push({ op: "status", signal });
        return Promise.resolve(setupStatus({ revision: 7 }));
      },
      setup(kind, accountId, revision, signal) {
        calls.push({ op: "setup", kind, accountId, revision, signal });
        return setupGate.promise;
      },
      bootstrap: () => { bootstraps++; return new Promise(() => {}); },
    }),
    connectFactory(options) {
      captured = options;
      return { mount() {}, start() {}, cancel: () => Promise.resolve(true), dispose() {} };
    },
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
  assert.match(text(host), /Nhà cung cấp đã được xác minh.*Đang chuẩn bị trợ lý…/su);
  assert.doesNotMatch(text(host), /cá nhân hoá|Persona|Trò chuyện thử/iu);
  setupGate.resolve(setupSuccess({ revision: 8 }));
  await flush();
  assert.equal(bootstraps, 1);
  assert.match(text(host), /Đang chuẩn bị trợ lý…/u);
  await Promise.all([first, duplicate, whileSetupIsPending]);
});
test("first provider Setup keeps every row visible then returns to the provider list", async (t) => {
  const response = deferred();
  const calls = [];
  const stages = [pendingProvider("codex", 0), pendingProvider("claude-code", 1)];
  const initial = setupStatus({
    revision: 51, providers: stages, provider_kind: "codex",
    provider_id: "codex", account_id: "acct-codex",
  });
  const { host } = mountPage(t, {
    initialStatus: initial,
    service: baseService({
      setup(...args) { calls.push(args); return response.promise; },
    }),
  });

  assert.match(text(providerCard(host, "codex")), /Đang thiết lập/u);
  assert.match(text(providerCard(host, "claude-code")), /Đang chờ/u);
  assert.deepEqual(calls[0].slice(0, 3), ["codex", "acct-codex", 51]);
  assert.ok(calls[0][3] instanceof AbortSignal);

  response.resolve(providerStatus({
    revision: 52,
    providers: [
      readyProvider("codex", 0, { account_id: "acct-codex", model_id: "model-c" }),
      pendingProvider("claude-code", 1),
    ],
  }));
  await flush(); await flush();
  assert.match(text(host), /Chọn nhà cung cấp/u);
  assert.match(text(providerCard(host, "codex")), /Đã sẵn sàng · Ưu tiên 1/u);
  assert.match(text(providerCard(host, "claude-code")), /Chưa cài\. Bấm để cài\./u);
});
test("last provider Setup advances into automatic bootstrap with the exact clicked identity", async (t) => {
  const calls = [];
  let bootstraps = 0;
  const readyCodex = readyProvider("codex", 0, { account_id: "acct-c", model_id: "model-c" });
  const pendingClaude = pendingProvider("claude-code", 1);
  const readyClaude = readyProvider("claude-code", 1, { account_id: "acct-a", model_id: "model-a" });
  const initial = setupStatus({
    revision: 61, providers: [readyCodex, pendingClaude], provider_kind: "claude-code",
    provider_id: "claude-code", account_id: "acct-a",
  });
  const final = personaStatus({
    revision: 62, providers: [readyCodex, readyClaude], provider_kind: "codex",
    provider_id: "codex", account_id: "acct-c", model_id: "model-c",
  });
  const { host } = mountPage(t, {
    initialStatus: initial,
    service: baseService({
      setup(...args) { calls.push(args); return Promise.resolve(final); },
      bootstrap: () => { bootstraps++; return new Promise(() => {}); },
    }),
  });

  await flush();
  assert.deepEqual(calls[0].slice(0, 3), ["claude-code", "acct-a", 61]);
  assert.equal(bootstraps, 1);
  assert.match(text(host), /Đang chuẩn bị trợ lý…/u);
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
      setup(kind, accountId, revision, signal) {
        calls.push({ kind, accountId, revision, signal });
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
  second.resolve(setupSuccess({ revision: 22, accountID: "resume-account" }));
  await flush();
});
test("Setup Retry renders an authoritative non-Setup phase without stale POST", async (t) => {
  const cases = [
    ["persona", personaStatus({ revision: 9 }), "onboarding-bootstrap-stage", "Đang chuẩn bị trợ lý…"],
    ["test", { ...personaStatus({ revision: 9 }), phase: "test" }, "onboarding-bootstrap-stage", "Đang chuẩn bị trợ lý…"],
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
      assert.equal(dialog?.getAttribute("role"), null);
      assert.equal(dialog?.getAttribute("aria-modal"), null);
      assert.equal(find(host, (node) => node.getAttribute?.("role") === "dialog"), null);
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
          setup(kind, accountId, revision) {
            calls.push({ kind, accountId, revision });
            if (calls.length === 1) return Promise.reject(new Error("stale"));
            return Promise.resolve(setupSuccess({
              revision: revision + 1, kind: authoritative.provider_kind,
              accountID: authoritative.account_id,
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
        { kind: "codex", accountId: "account-1", revision: 3 },
        { kind: authoritative.provider_kind, accountId: authoritative.account_id, revision: authoritative.revision },
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
test("Persona resume starts bootstrap once without Provider, Connect, or Setup", async (t) => {
  let selects = 0; let setups = 0; let bootstraps = 0;
  const connectFactory = fakeConnectFactory();
  const { host } = mountPage(t, {
    initialStatus: personaStatus(),
    service: baseService({
      selectProvider: () => { selects++; },
      setup: () => { setups++; },
      bootstrap: () => { bootstraps++; return new Promise(() => {}); },
    }),
    connectFactory,
  });
  await flush();
  assert.match(text(host), /Đang chuẩn bị trợ lý…/u);
  assert.equal(bootstraps, 1);
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
test("dispose aborts Setup, clears listeners, and suppresses stale rendering and callbacks", async (t) => {
  const gate = deferred();
  let signal;
  let completions = 0;
  const { page, host } = mountPage(t, {
    initialStatus: setupStatus(),
    service: baseService({
      setup(_kind, _accountId, _revision, setupSignal) {
        signal = setupSignal;
        return gate.promise;
      },
    }),
    onComplete: () => { completions++; },
  });
  page.dispose();
  page.dispose();
  assert.equal(signal.aborted, true);
  gate.resolve(setupSuccess());
  await flush();
  assert.equal(text(host), "");
  assert.equal(completions, 0);
  assert.equal(page.mount(host), page);
  assert.equal(text(host), "");
});
test("the real Provider Connect starts immediately and receives onboarding revision", async (t) => {
  const connectCalls = [];
  const setupCalls = [];
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
      setup(kind, accountId, revision) {
        setupCalls.push({ kind, accountId, revision });
        return Promise.resolve(setupSuccess({ revision: 53, accountID: accountId }));
      },
    }),
  });
  const starts = findAll(host, (node) => node.tagName === "BUTTON"
    && text(node).includes("Bắt đầu kết nối"));
  assert.equal(starts.length, 0, "immediate onboarding has no second start prompt");
  await flush();
  await flush();
  assert.deepEqual(connectCalls, [{ kind: "codex", label: "Onboarding", onboardingRevision: 51 }]);
  assert.deepEqual(setupCalls, [{ kind: "codex", accountId: "actual-account", revision: 52 }]);
});
