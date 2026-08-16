import test from "node:test";
import assert from "node:assert/strict";

import * as contract from "../overlay/internal/webui/static/pages/onboarding-contract.js";
import { createOnboardingPage } from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, installDOM, text } from "./helpers/dom-harness.mjs";
import {
  onboardingProviderOptions,
  onboardingStatus,
  pendingProvider,
  readyProvider,
} from "./helpers/onboarding-fixtures.mjs";

let flow = {};
try {
  flow = await import("../overlay/internal/webui/static/pages/onboarding-provider-flow.js");
} catch {
  // The first RED run intentionally proves the pure flow module does not exist yet.
}

const futureOption = Object.freeze({
  kind: "future-runtime",
  display_name: "Future Runtime",
  description: "Runtime được quảng bá từ máy chủ",
  recommended: false,
  beta: true,
  advertised: true,
  route_rank: 200,
});

const options = () => [...onboardingProviderOptions(), { ...futureOption }];
const flush = () => new Promise((resolve) => setImmediate(resolve));
const settle = async () => { await flush(); await flush(); };
const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const row = (root, kind) => find(root, (node) => hasClass(node, "onboarding-provider-card")
  && node.dataset.providerKind === kind);
const toggle = (root, kind) => find(row(root, kind), (node) => hasClass(node, "onboarding-provider-toggle"));
const install = (root, kind) => find(row(root, kind), (node) => hasClass(node, "onboarding-provider-install"));
const never = () => new Promise(() => {});

function pageService(overrides = {}) {
  const status = overrides.status ?? never;
  return {
    status,
    bootstrapStatus: overrides.bootstrapStatus ?? status,
    updateProviders: never,
    beginProvider: never,
    backToProviders: never,
    selectProvider: never,
    setup: never,
    loadAgent: never,
    bootstrap: never,
    ...overrides,
  };
}

function pageConnectFactory() {
  const instances = [];
  const factory = (config) => {
    const instance = {
      config, starts: [], slot: null,
      mount(slot) { this.slot = slot; return this; },
      start(value) { this.starts.push(value); return true; },
      cancel: async () => true,
      dispose() { this.slot?.replaceChildren(); },
    };
    instances.push(instance);
    return instance;
  };
  factory.instances = instances;
  return factory;
}

function mountPage(t, initialStatus, service, connectFactory = pageConnectFactory()) {
  const dom = installDOM();
  const host = document.createElement("div");
  const page = createOnboardingPage({ initialStatus, service, connectFactory, connectService: {} });
  page.mount(host);
  t.after(() => { page.dispose(); dom.restore(); });
  return { connectFactory, host, page };
}

function providerStatus(overrides = {}) {
  return onboardingStatus("provider", { provider_options: options(), ...overrides });
}

function connectStatus(kind, providers, overrides = {}) {
  return onboardingStatus("connect", {
    provider_options: options(),
    providers,
    provider_kind: kind,
    provider_id: "",
    account_id: "",
    model_id: "",
    ...overrides,
  });
}

function readyStatus(phase, providers, overrides = {}) {
  const first = providers[0];
  return onboardingStatus(phase, {
    provider_options: options(),
    providers,
    provider_kind: first.kind,
    provider_id: first.provider_id,
    account_id: first.account_id,
    model_id: first.model_id,
    ...overrides,
  });
}

function abortError() {
  return Object.assign(new Error("aborted"), { name: "AbortError" });
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, resolve, reject };
}

const abortFenceScenarios = [
  {
    name: "selected-set",
    successor: () => providerStatus({
      revision: 61,
      providers: [pendingProvider("codex", 0)],
    }),
    run({ mutation, loadStatus, signal }) {
      return flow.persistProviderSetWithReconciliation({
        state: contract.normalizeStatus(providerStatus({
          revision: 60,
          providers: [pendingProvider("codex", 0)],
        })),
        selectedKinds: ["codex"],
        updateProviders: mutation,
        loadStatus,
        signal,
      });
    },
  },
  {
    name: "begin-provider",
    successor: () => connectStatus("codex", [pendingProvider("codex", 0)], { revision: 71 }),
    run({ mutation, loadStatus, signal }) {
      return flow.beginProviderInstallWithReconciliation({
        state: contract.normalizeStatus(providerStatus({
          revision: 70,
          providers: [pendingProvider("codex", 0)],
        })),
        kind: "codex",
        beginProvider: mutation,
        loadStatus,
        signal,
      });
    },
  },
];

test("page service validation requires the plural runtime contract, not legacy selection", () => {
  const methods = [
    "status", "bootstrapStatus", "updateProviders", "beginProvider", "setup", "backToProviders",
    "loadAgent", "bootstrap",
  ];
  const valid = Object.fromEntries(methods.map((method) => [method, never]));
  const create = (service) => createOnboardingPage({
    initialStatus: providerStatus(), service, connectFactory: pageConnectFactory(),
  });

  const page = create(valid);
  page.dispose();

  for (const method of methods) {
    const missing = { ...valid };
    delete missing[method];
    assert.throws(() => create(missing), new RegExp(`${method}\\(\\)`));
  }

  const legacyOnly = {
    status: never, bootstrapStatus: never, selectProvider: never, setup: never, loadAgent: never,
    bootstrap: never,
  };
  assert.throws(() => create(legacyOnly), /updateProviders\(\)/u);
});

test("status accepts a server-advertised future provider and deeply freezes catalog and stages", () => {
  assert.equal(typeof contract.normalizeProviderOptions, "function");
  assert.equal(typeof contract.normalizeProviderStages, "function");
  const raw = providerStatus({ providers: [pendingProvider("future-runtime", 0)] });
  const normalized = contract.normalizeStatus(raw);

  assert.deepEqual(normalized?.provider_options.map(({ kind }) => kind), [
    "codex", "claude-code", "future-runtime",
  ]);
  assert.deepEqual(normalized?.providers.map(({ kind }) => kind), ["future-runtime"]);
  assert.equal(Object.isFrozen(normalized), true);
  assert.equal(Object.isFrozen(normalized?.provider_options), true);
  assert.equal(Object.isFrozen(normalized?.provider_options[0]), true);
  assert.equal(Object.isFrozen(normalized?.providers), true);
  assert.equal(Object.isFrozen(normalized?.providers[0]), true);

  raw.provider_options[0].display_name = "mutated";
  raw.providers[0].kind = "mutated";
  assert.equal(normalized?.provider_options[0].display_name, "Codex");
  assert.equal(normalized?.providers[0].kind, "future-runtime");
});

test("provider selection comes only from staged rows while suggestion stays advisory", () => {
  const normalized = contract.normalizeStatus(providerStatus({
    suggested_provider_kind: "codex",
    providers: [pendingProvider("claude-code", 0)],
  }));

  assert.equal(normalized?.suggested_provider_kind, "codex");
  assert.deepEqual(normalized?.providers.map(({ kind }) => kind), ["claude-code"]);
});

test("keeps two providers on and installs only the clicked pending row", async (t) => {
  const mutations = [];
  const begins = [];
  const pendingBoth = [pendingProvider("codex", 0), pendingProvider("claude-code", 1)];
  let authoritative = providerStatus({ revision: 7, suggested_provider_kind: "codex" });
  const service = pageService({
    status: async () => authoritative,
    updateProviders: async (kinds, revision, signal) => {
      mutations.push({ kinds, revision, signal });
      authoritative = providerStatus({
        revision: revision + 1,
        suggested_provider_kind: "codex",
        providers: kinds.length === 1 ? [pendingProvider(kinds[0], 0)] : pendingBoth,
      });
      return authoritative;
    },
    beginProvider: async (kind, revision, signal) => {
      begins.push({ kind, revision, signal });
      authoritative = connectStatus(kind, pendingBoth, { revision: revision + 1 });
      return authoritative;
    },
  });
  const { connectFactory, host, page } = mountPage(t, authoritative, service);

  assert.equal(toggle(host, "codex").getAttribute("aria-pressed"), "false");
  toggle(host, "codex").click(); await settle();
  toggle(host, "claude-code").click(); await settle();
  assert.deepEqual(mutations.map(({ kinds, revision }) => ({ kinds, revision })), [
    { kinds: ["codex"], revision: 7 },
    { kinds: ["codex", "claude-code"], revision: 8 },
  ]);
  assert.equal(toggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(toggle(host, "claude-code").getAttribute("aria-pressed"), "true");
  assert.equal(install(host, "codex").getAttribute("aria-label"), "Cài Codex");
  assert.equal(install(host, "claude-code").getAttribute("aria-label"), "Cài Claude Code");

  const cta = install(host, "claude-code");
  cta.click(); cta.click(); page.mount(host); cta.click();
  await settle();
  assert.deepEqual(begins.map(({ kind, revision }) => ({ kind, revision })), [
    { kind: "claude-code", revision: 9 },
  ]);
  assert.equal(connectFactory.instances.length, 1);
  assert.deepEqual(connectFactory.instances[0].starts, [{
    label: "Onboarding", onboardingRevision: 10, immediate: true,
  }]);
  assert.match(text(row(host, "codex")), /Đang chờ/u);
  assert.doesNotMatch(text(host), /Bắt đầu kết nối/u);
});

test("renders a future advertised provider with generic copy without selecting its suggestion", (t) => {
  const initial = providerStatus({ revision: 17, suggested_provider_kind: "future-runtime" });
  const { host } = mountPage(t, initial, pageService());
  const future = row(host, "future-runtime");

  assert.match(text(future), /Future Runtime/u);
  assert.match(text(future), /Runtime được quảng bá từ máy chủ/u);
  assert.match(text(future), /Đề xuất/u);
  assert.equal(toggle(host, "future-runtime").getAttribute("aria-pressed"), "false");
  assert.equal(install(host, "future-runtime"), null);
});

test("catalog validation rejects duplicate, hidden, unsafe, mistyped, and noncanonical options", () => {
  const base = options();
  const invalid = [
    null,
    [],
    [...base, { ...futureOption }],
    [base[1], base[0]],
    [{ ...base[0], route_rank: base[1].route_rank }, base[1]],
    [{ ...base[0], advertised: false }, ...base.slice(1)],
    [{ ...base[0], recommended: "true" }, ...base.slice(1)],
    [{ ...base[0], beta: 0 }, ...base.slice(1)],
    [{ ...base[0], route_rank: 1.5 }, ...base.slice(1)],
    [{ ...base[0], kind: " codex" }, ...base.slice(1)],
    [{ ...base[0], display_name: "Codex\u0000" }, ...base.slice(1)],
    [{ ...base[0], description: "" }, ...base.slice(1)],
  ];
  for (const value of invalid) {
    assert.throws(() => contract.normalizeProviderOptions(value), /invalid provider options/i);
    assert.equal(contract.normalizeStatus(providerStatus({ provider_options: value })), null);
  }
});

test("stage validation requires catalog membership, uniqueness, canonical position, and exact identities", () => {
  const catalog = contract.normalizeProviderOptions(options());
  assert.deepEqual(contract.normalizeProviderStages([
    pendingProvider("codex", 0),
    readyProvider("future-runtime", 1),
  ], catalog).map(({ kind }) => kind), ["codex", "future-runtime"]);

  const invalid = [
    [pendingProvider("unknown", 0)],
    [pendingProvider("codex", 0), pendingProvider("codex", 1)],
    [pendingProvider("codex", 1)],
    [pendingProvider("future-runtime", 0), pendingProvider("codex", 1)],
    [{ ...pendingProvider("codex", 0), account_id: "dirty" }],
    [{ ...readyProvider("codex", 0), provider_id: "" }],
    [{ ...readyProvider("codex", 0), provider_id: "other" }],
    [{ ...readyProvider("codex", 0), account_id: " account" }],
    [{ ...readyProvider("codex", 0), model_id: "" }],
    [{ ...pendingProvider("codex", 0), status: "installing" }],
  ];
  for (const stages of invalid) {
    assert.throws(() => contract.normalizeProviderStages(stages, catalog), /invalid providers/i);
  }
});

test("status enforces every phase, active slot, lifecycle, and suggested-badge invariant", () => {
  const pending = [pendingProvider("codex", 0), pendingProvider("claude-code", 1)];
  const ready = [
    readyProvider("codex", 0, { account_id: "account-c", model_id: "model-c" }),
    readyProvider("claude-code", 1, { account_id: "account-a", model_id: "model-a" }),
  ];
  const provider = contract.normalizeStatus(providerStatus({
    providers: pending,
    suggested_provider_kind: "future-runtime",
  }));
  assert.deepEqual(provider?.providers.map(({ kind }) => kind), ["codex", "claude-code"]);
  assert.equal(provider?.suggested_provider_kind, "future-runtime");
  assert.equal(provider?.providers.some(({ kind }) => kind === "future-runtime"), false,
    "a suggestion is only a badge and cannot create an ON row");

  assert.ok(contract.normalizeStatus(connectStatus("claude-code", pending)));
  assert.ok(contract.normalizeStatus(onboardingStatus("setup", {
    provider_options: options(), providers: pending, provider_kind: "claude-code",
    provider_id: "claude-code", account_id: "account-a", model_id: "",
  })));
  assert.ok(contract.normalizeStatus(readyStatus("persona", ready)));
  assert.ok(contract.normalizeStatus(readyStatus("test", ready)));
  assert.ok(contract.normalizeStatus(onboardingStatus("completed", {
    provider_options: options(), providers: [], provider_kind: "", provider_id: "",
    account_id: "", model_id: "",
  })));

  const malformed = [
    providerStatus({ provider_id: "dirty" }),
    connectStatus("future-runtime", pending),
    connectStatus("codex", [ready[0], pendingProvider("claude-code", 1)]),
    onboardingStatus("setup", {
      provider_options: options(), providers: pending, provider_kind: "codex",
      provider_id: "claude-code", account_id: "account", model_id: "",
    }),
    readyStatus("persona", [ready[0], pendingProvider("claude-code", 1)]),
    readyStatus("test", ready, { account_id: "wrong" }),
    onboardingStatus("completed", { provider_options: options(), providers: [ready[0]] }),
    providerStatus({ completed_version: 1, current_version: 1, restart_in_progress: false }),
    providerStatus({ completed_version: 0, current_version: 1, restart_in_progress: true }),
    providerStatus({ revision: Number.MAX_SAFE_INTEGER + 1 }),
    providerStatus({ suggested_provider_kind: "not-advertised" }),
  ];
  for (const value of malformed) assert.equal(contract.normalizeStatus(value), null);
});

test("projectStatus preserves only copied provider collections", () => {
  const raw = providerStatus({
    providers: [pendingProvider("codex", 0)],
    private_path: "C:/secret",
  });
  raw.providers[0].config_dir = "C:/secret";
  raw.provider_options[0].command = "secret-command";
  const projected = contract.projectStatus(raw);

  assert.equal(Object.hasOwn(projected, "private_path"), false);
  assert.equal(Object.hasOwn(projected.providers[0], "config_dir"), false);
  assert.equal(Object.hasOwn(projected.provider_options[0], "command"), false);
  assert.notEqual(projected.providers, raw.providers);
  assert.notEqual(projected.provider_options, raw.provider_options);
  assert.equal(Object.isFrozen(projected.providers), true);
  assert.equal(Object.isFrozen(projected.provider_options), true);
});

test("service sends plural selection, singular begin, exact setup kind, and authoritative Back bodies", async () => {
  const calls = [];
  const signal = new AbortController().signal;
  const pending = [pendingProvider("codex", 0), pendingProvider("claude-code", 1)];
  const codexReady = [
    readyProvider("codex", 0, { account_id: "account-c", model_id: "model-c" }),
    pendingProvider("claude-code", 1),
  ];
  const allReady = [
    codexReady[0],
    readyProvider("claude-code", 1, { account_id: "account-a", model_id: "model-a" }),
  ];
  const responses = new Map([
    ["PUT /onboarding/providers", providerStatus({ revision: 8, providers: pending })],
    ["PUT /onboarding/provider", connectStatus("codex", pending, { revision: 9 })],
    ["POST /onboarding/setup", providerStatus({ revision: 11, providers: codexReady })],
    ["POST /onboarding/back-to-providers", providerStatus({ revision: 13, providers: allReady })],
  ]);
  const service = contract.createOnboardingService({ requestJSON: async (path, init = {}) => {
    calls.push({ path, init });
    return responses.get(`${init.method ?? "GET"} ${path}`) ?? providerStatus();
  } });

  await service.updateProviders(["claude-code", "codex"], 7, signal);
  await service.beginProvider("codex", 8, signal);
  await service.setup("codex", "account-c", 10, signal);
  await service.backToProviders(12, signal);

  assert.deepEqual(calls, [
    {
      path: "/onboarding/providers",
      init: { method: "PUT", body: { revision: 7, selected_kinds: ["claude-code", "codex"] }, signal },
    },
    {
      path: "/onboarding/provider",
      init: { method: "PUT", body: { kind: "codex", revision: 8 }, signal },
    },
    {
      path: "/onboarding/setup",
      init: { method: "POST", body: { kind: "codex", account_id: "account-c", revision: 10 }, signal },
    },
    {
      path: "/onboarding/back-to-providers",
      init: { method: "POST", body: { revision: 12 }, signal },
    },
  ]);
});

test("plural selection service rejects a structurally valid response from the wrong phase", async () => {
  const service = contract.createOnboardingService({
    requestJSON: async () => connectStatus("codex", [pendingProvider("codex", 0)], { revision: 8 }),
  });
  await assert.rejects(service.updateProviders(["codex"], 7), /provider response/i);
});

test("setup service rejects a provider successor after every selected member is ready", async () => {
  const allReady = [
    readyProvider("codex", 0, { account_id: "account-c", model_id: "model-c" }),
    readyProvider("claude-code", 1),
  ];
  const service = contract.createOnboardingService({
    requestJSON: async () => providerStatus({ revision: 11, providers: allReady }),
  });

  await assert.rejects(service.setup("codex", "account-c", 10), /provider response/i);
});

test("setup normalization changes only its exact pending member and preserves catalog plus siblings", () => {
  const lifecycle = contract.normalizeStatus(onboardingStatus("setup", {
    revision: 10,
    provider_options: options(),
    providers: [pendingProvider("codex", 0), readyProvider("claude-code", 1)],
    provider_kind: "codex",
    provider_id: "codex",
    account_id: "account-c",
    model_id: "",
  }));
  const result = providerStatus({
    revision: 11,
    providers: [
      readyProvider("codex", 0, { account_id: "account-c", model_id: "model-c" }),
      readyProvider("claude-code", 1),
    ],
  });
  result.phase = "persona";
  result.provider_kind = "codex";
  result.provider_id = "codex";
  result.account_id = "account-c";
  result.model_id = "model-c";
  const expected = {
    lifecycle, revision: 10, providerKind: "codex", providerID: "codex", accountID: "account-c",
  };
  assert.equal(contract.normalizeSetupStatus(result, expected)?.phase, "persona");

  const missingSibling = {
    ...result,
    providers: [readyProvider("codex", 0, { account_id: "account-c", model_id: "model-c" })],
  };
  assert.equal(contract.normalizeSetupStatus(missingSibling, expected), null);
  const changedCatalog = {
    ...result,
    provider_options: options().map((option) => option.kind === "future-runtime"
      ? { ...option, description: "Catalog drift" } : option),
  };
  assert.equal(contract.normalizeSetupStatus(changedCatalog, expected), null);
});

test("setup normalization rejects every valid lifecycle-marker drift", () => {
  const lifecycle = contract.normalizeStatus(onboardingStatus("setup", {
    current_version: 3,
    completed_version: 1,
    revision: 10,
    provider_options: options(),
    providers: [pendingProvider("codex", 0), readyProvider("claude-code", 1)],
    provider_kind: "codex",
    provider_id: "codex",
    account_id: "account-c",
    model_id: "",
  }));
  const result = providerStatus({
    current_version: 3,
    completed_version: 1,
    revision: 11,
    providers: [
      readyProvider("codex", 0, { account_id: "account-c", model_id: "model-c" }),
      readyProvider("claude-code", 1),
    ],
  });
  result.phase = "persona";
  result.provider_kind = "codex";
  result.provider_id = "codex";
  result.account_id = "account-c";
  result.model_id = "model-c";
  const expected = {
    lifecycle, revision: 10, providerKind: "codex", providerID: "codex", accountID: "account-c",
  };

  for (const drift of [
    { current_version: 4 },
    { completed_version: 2 },
    { completed_version: 3, restart_in_progress: true },
  ]) {
    assert.equal(contract.normalizeSetupStatus({ ...result, ...drift }, expected), null);
  }
});

test("selected-set reconciliation accepts only the exact canonical +1 successor", async () => {
  assert.equal(typeof flow.persistProviderSetWithReconciliation, "function");
  const before = contract.normalizeStatus(providerStatus({
    revision: 20,
    providers: [readyProvider("codex", 0), pendingProvider("claude-code", 1)],
  }));
  const after = providerStatus({
    revision: 21,
    providers: [readyProvider("codex", 0), pendingProvider("future-runtime", 1)],
  });
  let loads = 0;
  const result = await flow.persistProviderSetWithReconciliation({
    state: before,
    selectedKinds: ["future-runtime", "codex"],
    updateProviders: async (revision, selectedKinds) => {
      assert.equal(revision, 20);
      assert.deepEqual(selectedKinds, ["codex", "future-runtime"]);
      throw new Error("lost response");
    },
    loadStatus: async () => { loads++; return after; },
  });

  assert.equal(loads, 1);
  assert.equal(result.kind, "updated");
  assert.deepEqual(result.state.providers.map(({ kind }) => kind), ["codex", "future-runtime"]);

  for (const mismatch of [
    { revision: 22 },
    { providers: [readyProvider("codex", 0), pendingProvider("claude-code", 1)] },
    { current_version: 2 },
    { provider_options: options().map((option) => option.kind === "future-runtime"
      ? { ...option, description: "Catalog đã đổi" } : option) },
  ]) {
    const moved = await flow.persistProviderSetWithReconciliation({
      state: before,
      selectedKinds: ["future-runtime", "codex"],
      updateProviders: async () => { throw new Error("uncertain"); },
      loadStatus: async () => providerStatus({
        revision: 21,
        providers: [readyProvider("codex", 0), pendingProvider("future-runtime", 1)],
        ...mismatch,
      }),
    });
    assert.equal(moved.kind, "moved");
  }
});

test("resolved malformed success and Abort never launder through GET", async () => {
  const before = contract.normalizeStatus(providerStatus({ revision: 30 }));
  let loads = 0;
  await assert.rejects(flow.persistProviderSetWithReconciliation({
    state: before,
    selectedKinds: ["codex"],
    updateProviders: async () => ({ revision: 31, phase: "provider" }),
    loadStatus: async () => { loads++; return providerStatus({ revision: 31 }); },
  }), /provider response/i);
  assert.equal(loads, 0);

  await assert.rejects(flow.persistProviderSetWithReconciliation({
    state: before,
    selectedKinds: ["codex"],
    updateProviders: async () => { throw abortError(); },
    loadStatus: async () => { loads++; return providerStatus({ revision: 31 }); },
  }), { name: "AbortError" });
  assert.equal(loads, 0);
});

for (const scenario of abortFenceScenarios) {
  test(`${scenario.name} reconciliation fences Abort after its mutation resolves`, async () => {
    const controller = new AbortController();
    const mutation = deferred();
    let loads = 0;
    const operation = scenario.run({
      mutation: () => mutation.promise,
      loadStatus: async () => { loads++; return scenario.successor(); },
      signal: controller.signal,
    });

    controller.abort();
    mutation.resolve(scenario.successor());

    await assert.rejects(operation, { name: "AbortError" });
    assert.equal(loads, 0);
  });

  test(`${scenario.name} reconciliation fences Abort after fallback GET resolves`, async () => {
    const controller = new AbortController();
    const status = deferred();
    const loadStarted = deferred();
    const operation = scenario.run({
      mutation: async () => { throw new Error("lost response"); },
      loadStatus: () => {
        loadStarted.resolve();
        return status.promise;
      },
      signal: controller.signal,
    });

    await loadStarted.promise;
    controller.abort();
    status.resolve(scenario.successor());

    await assert.rejects(operation, { name: "AbortError" });
  });
}

test("begin reconciliation preserves full staged arrays and accepts only the exact active successor", async () => {
  assert.equal(typeof flow.beginProviderInstallWithReconciliation, "function");
  const stages = [pendingProvider("codex", 0), pendingProvider("claude-code", 1)];
  const before = contract.normalizeStatus(providerStatus({ revision: 40, providers: stages }));
  const exact = connectStatus("claude-code", stages, { revision: 41 });
  const result = await flow.beginProviderInstallWithReconciliation({
    state: before,
    kind: "claude-code",
    beginProvider: async () => { throw new Error("lost"); },
    loadStatus: async () => exact,
  });
  assert.equal(result.kind, "started");
  assert.equal(result.state.provider_kind, "claude-code");

  const calls = [];
  const direct = await flow.beginProviderInstallWithReconciliation({
    state: before,
    kind: "claude-code",
    beginProvider: async (revision, kind, signal) => {
      calls.push({ revision, kind, signal });
      return exact;
    },
    loadStatus: async () => { throw new Error("must not reconcile"); },
  });
  assert.deepEqual(calls.map(({ revision, kind }) => ({ revision, kind })), [
    { revision: 40, kind: "claude-code" },
  ]);
  assert.equal(direct.kind, "started");

  const moved = await flow.beginProviderInstallWithReconciliation({
    state: before,
    kind: "claude-code",
    beginProvider: async () => { throw new Error("lost"); },
    loadStatus: async () => connectStatus("codex", stages, { revision: 41 }),
  });
  assert.equal(moved.kind, "moved");

  let begins = 0;
  await assert.rejects(flow.beginProviderInstallWithReconciliation({
    state: { ...before, revision: Number.MAX_SAFE_INTEGER },
    kind: "claude-code",
    beginProvider: async () => { begins++; },
    loadStatus: async () => exact,
  }), /revision/i);
  assert.equal(begins, 0);
});
