import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { createOnboardingPage, createOnboardingService } from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

let bootstrapAPI = {};
try {
  bootstrapAPI = await import("../overlay/internal/webui/static/pages/onboarding-bootstrap.js");
} catch {
  // The RED commit proves this public orchestration module is still missing.
}

const flush = () => new Promise((resolve) => setImmediate(resolve));
const settle = async () => { await flush(); await flush(); };
const clone = (value) => JSON.parse(JSON.stringify(value));
const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const byClass = (root, name) => find(root, (node) => hasClass(node, name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const never = () => new Promise(() => {});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, resolve, reject };
}

function activeStatus(phase = "persona", revision = 7, overrides = {}) {
  return onboardingStatus(phase, { revision, ...overrides });
}

function completedFor(snapshot, revision) {
  return onboardingStatus("completed", {
    current_version: snapshot.current_version,
    completed_version: snapshot.current_version,
    restart_in_progress: false,
    required: false,
    revision,
    provider_options: clone(snapshot.provider_options),
  });
}

function partialTestFor(snapshot, revision) {
  return onboardingStatus("test", {
    current_version: snapshot.current_version,
    completed_version: snapshot.completed_version,
    restart_in_progress: snapshot.restart_in_progress,
    required: snapshot.required,
    revision,
    providers: clone(snapshot.providers),
    provider_options: clone(snapshot.provider_options),
    provider_kind: snapshot.provider_kind,
    provider_id: snapshot.provider_id,
    account_id: snapshot.account_id,
    model_id: snapshot.model_id,
    suggested_provider_kind: snapshot.suggested_provider_kind,
  });
}

function runBootstrap(input) {
  assert.equal(typeof bootstrapAPI.runOnboardingBootstrap, "function",
    "runOnboardingBootstrap must be exported");
  return bootstrapAPI.runOnboardingBootstrap(input);
}

function pageService(overrides = {}) {
  const status = overrides.status ?? never;
  return {
    status,
    bootstrapStatus: overrides.bootstrapStatus ?? status,
    updateProviders: never,
    beginProvider: never,
    backToProviders: never,
    setup: never,
    loadAgent: () => Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [] }),
    bootstrap: never,
    ...overrides,
  };
}

function mountPage(t, { initialStatus = activeStatus(), service = pageService(), ...options } = {}) {
  const dom = installDOM();
  const host = document.createElement("div");
  const page = createOnboardingPage({ initialStatus, service, ...options });
  page.mount(host);
  t.after(() => { page.dispose(); dom.restore(); });
  return { host, page };
}

function assertProgressOnly(host, { failed = false } = {}) {
  const stage = byClass(host, "onboarding-bootstrap-stage");
  assert.ok(stage);
  assert.equal(text(find(stage, (node) => node.tagName === "H1")), "Đang chuẩn bị trợ lý…");
  const messages = findAll(stage, (node) => node.tagName === "P");
  assert.equal(messages.length, failed ? 1 : 0);
  if (failed) {
    assert.equal(text(messages[0]), "Chưa thể chuẩn bị trợ lý. Cấu hình cũ vẫn được giữ nguyên.");
  }
  assert.equal(findAll(stage, (node) => ["INPUT", "TEXTAREA", "FORM"].includes(node.tagName)).length, 0);
  assert.equal(button(stage, "Tiếp tục"), null);
  assert.equal(button(stage, "Gửi thử"), null);
  assert.equal(button(stage, "Ổn, dùng cấu hình này"), null);
  assert.ok(button(stage, "Quay lại Provider"));
  assert.equal(Boolean(button(stage, "Thử lại")), failed);
}

test("bootstrap service sends strict revision-only POST and removes browser Test/Complete APIs", async () => {
  const calls = [];
  const signal = new AbortController().signal;
  const raw = completedFor(activeStatus(), 10);
  const service = createOnboardingService({
    requestJSON(path, options) {
      calls.push({ path, options });
      return Promise.resolve(raw);
    },
  });

  assert.equal(typeof service.bootstrap, "function");
  assert.equal(Object.hasOwn(service, "saveAgent"), false);
  assert.equal(Object.hasOwn(service, "testChat"), false);
  assert.equal(Object.hasOwn(service, "complete"), false);
  assert.equal(await service.bootstrap(7, signal), raw, "bootstrap response must remain raw");
  assert.deepEqual(calls, [{
    path: "/onboarding/bootstrap",
    options: { method: "POST", body: { revision: 7 }, signal },
  }]);
  assert.throws(() => service.bootstrap(0), /revision/i);
  assert.throws(() => service.bootstrap(Number.MAX_SAFE_INTEGER + 1), /revision/i);
  assert.equal(calls.length, 1);
});

test("direct bootstrap accepts only the exact phase-specific Completed successor", async (t) => {
  for (const [phase, distance] of [["persona", 3], ["test", 2]]) {
    await t.test(phase, async () => {
      const snapshot = activeStatus(phase, 20);
      let gets = 0;
      const expected = completedFor(snapshot, snapshot.revision + distance);
      const result = await runBootstrap({
        snapshot,
        bootstrap: async () => expected,
        loadStatus: async () => { gets++; return expected; },
      });
      assert.deepEqual(result, { kind: "completed", state: expected });
      assert.equal(gets, 0);
    });
  }

  const completed = completedFor(activeStatus(), 90);
  let calls = 0;
  const noOp = await runBootstrap({
    snapshot: completed,
    bootstrap: async () => { calls++; },
    loadStatus: async () => { calls++; },
  });
  assert.deepEqual(noOp, { kind: "completed", state: completed });
  assert.equal(calls, 0, "an exact Completed client state is a no-op");
});

test("resolved bootstrap responses fail closed without GET on shape, phase, lifecycle, or catalog drift", async (t) => {
  const snapshot = activeStatus("persona", 30);
  const exact = completedFor(snapshot, 33);
  const cases = [
    ["wrong revision", completedFor(snapshot, 32)],
    ["wrong phase", partialTestFor(snapshot, 31)],
    ["top-level private field", { ...exact, internal_marker: "hidden" }],
    ["missing public field", (() => { const value = clone(exact); delete value.model_id; return value; })()],
    ["nested private field", (() => {
      const value = clone(exact); value.provider_options[0].internal_marker = true; return value;
    })()],
    ["catalog drift", (() => {
      const value = clone(exact); value.provider_options[0].display_name = "Changed"; return value;
    })()],
    ["lifecycle drift", {
      ...exact,
      current_version: exact.current_version + 1,
      completed_version: exact.current_version + 1,
    }],
    ["suggestion drift", { ...exact, suggested_provider_kind: "codex" }],
  ];
  for (const [name, response] of cases) {
    await t.test(name, async () => {
      let gets = 0;
      const result = await runBootstrap({
        snapshot,
        bootstrap: async () => response,
        loadStatus: async () => { gets++; return exact; },
      });
      assert.deepEqual(result, { kind: "failed", state: snapshot });
      assert.equal(gets, 0, "resolved malformed data must never be laundered through GET");
    });
  }
});

test("known HTTP failures do not GET", async () => {
  const snapshot = activeStatus("test", 40);
  const exact = completedFor(snapshot, 42);
  let gets = 0;
  const stable = await runBootstrap({
    snapshot,
    bootstrap: async () => { throw Object.assign(new Error("private"), { status: 409, code: "CONFLICT" }); },
    loadStatus: async () => { gets++; return exact; },
  });
  assert.deepEqual(stable, { kind: "failed", state: snapshot });
  assert.equal(gets, 0);

  const controller = new AbortController();
  const raced = await runBootstrap({
    snapshot,
    signal: controller.signal,
    bootstrap: async () => {
      controller.abort();
      throw Object.assign(new Error("private"), { status: 409, code: "CONFLICT" });
    },
    loadStatus: async () => { gets++; return exact; },
  });
  assert.deepEqual(raced, { kind: "failed", state: snapshot });
  assert.equal(gets, 0, "a received HTTP failure stays known when Abort races settlement");
});

test("lost-response GET accepts only exact Completed continuity for Persona and Test", async (t) => {
  for (const [phase, distance] of [["persona", 3], ["test", 2]]) {
    await t.test(phase, async () => {
      const snapshot = activeStatus(phase, 40);
      const controller = new AbortController();
      const exact = completedFor(snapshot, snapshot.revision + distance);
      let postSignal;
      let getSignal;
      let gets = 0;
      const reconciled = await runBootstrap({
        snapshot,
        signal: controller.signal,
        bootstrap: async (_revision, signal) => {
          postSignal = signal;
          throw new TypeError("network lost");
        },
        loadStatus: async (signal) => { gets++; getSignal = signal; return exact; },
      });
      assert.deepEqual(reconciled, { kind: "completed", state: exact });
      assert.equal(gets, 1);
      assert.equal(postSignal, controller.signal);
      assert.equal(getSignal, controller.signal);

      const wrong = completedFor(snapshot, snapshot.revision + distance + 1);
      const rejected = await runBootstrap({
        snapshot,
        bootstrap: async () => { throw new TypeError("network lost"); },
        loadStatus: async () => wrong,
      });
      assert.deepEqual(rejected, { kind: "failed", state: snapshot });
    });
  }
});

test("real service does not project private GET fields before bootstrap reconciliation", async (t) => {
  const snapshot = activeStatus("persona", 45);
  const privateCompleted = { ...completedFor(snapshot, 48), internal_marker: "PRIVATE" };
  const calls = [];
  const service = createOnboardingService({
    requestJSON(path, options) {
      calls.push({ path, signal: options.signal });
      if (path === "/onboarding/bootstrap") return Promise.reject(new TypeError("response lost"));
      if (path === "/onboarding/status") return Promise.resolve(privateCompleted);
      if (path === "/agent") {
        return Promise.resolve({ ready: true, display_name: "Wrong", placeholders: [] });
      }
      throw new Error(`unexpected path: ${path}`);
    },
  });
  const { host } = mountPage(t, { initialStatus: snapshot, service });
  await settle();

  assert.deepEqual(calls.map(({ path }) => path), ["/onboarding/bootstrap", "/onboarding/status"]);
  assert.equal(calls[1].signal, calls[0].signal);
  assertProgressOnly(host, { failed: true });
  assert.doesNotMatch(text(host), /PRIVATE|Wrong/u);
});

test("page service rejects a projection-only status wrapper without raw bootstrap status", async () => {
  const snapshot = activeStatus("persona", 49);
  const privateCompleted = { ...completedFor(snapshot, 52), internal_marker: "PRIVATE" };
  const completeService = createOnboardingService({
    requestJSON: () => Promise.resolve(privateCompleted),
  });
  const { bootstrapStatus: _rawStatus, ...projectionOnly } = completeService;
  const projected = await projectionOnly.status();
  assert.equal(Object.hasOwn(projected, "internal_marker"), false);

  assert.throws(() => createOnboardingPage({
    initialStatus: snapshot,
    service: projectionOnly,
  }), /bootstrapStatus\(\)/u);
});

test("ambiguous reconciliation recognizes only bounded partial Test advances", async (t) => {
  const scenarios = [
    [activeStatus("persona", 50), 51, "partial"],
    [activeStatus("persona", 50), 52, "partial"],
    [activeStatus("persona", 50), 50, "failed"],
    [activeStatus("test", 60), 61, "partial"],
    [activeStatus("test", 60), 62, "completed"],
  ];
  for (const [snapshot, revision, kind] of scenarios) {
    await t.test(`${snapshot.phase} ${snapshot.revision} -> ${revision}`, async () => {
      const response = kind === "completed"
        ? completedFor(snapshot, revision)
        : partialTestFor(snapshot, revision);
      let gets = 0;
      const result = await runBootstrap({
        snapshot,
        bootstrap: async () => { throw new TypeError("connection reset"); },
        loadStatus: async () => { gets++; return response; },
      });
      assert.equal(result.kind, kind);
      assert.deepEqual(result.state, kind === "failed" ? snapshot : response);
      assert.equal(gets, 1);
    });
  }
});

test("Abort still reconciles uncertainty but fences state after every await", async () => {
  const snapshot = activeStatus("persona", 70);
  const expected = completedFor(snapshot, 73);
  const controller = new AbortController();
  const mutation = deferred();
  const reconciliation = deferred();
  let gets = 0;
  const operation = runBootstrap({
    snapshot,
    signal: controller.signal,
    bootstrap: () => mutation.promise,
    loadStatus: () => { gets++; return reconciliation.promise; },
  });

  controller.abort();
  mutation.reject(Object.assign(new Error("aborted"), { name: "AbortError" }));
  await flush();
  assert.equal(gets, 1, "an uncertain aborted mutation still performs one authoritative GET");
  reconciliation.resolve(expected);
  assert.deepEqual(await operation, { kind: "failed", state: snapshot });
});

test("a direct POST resolving after Abort is reconciled but cannot publish state", async () => {
  const snapshot = activeStatus("test", 72);
  const expected = completedFor(snapshot, 74);
  const controller = new AbortController();
  const mutation = deferred();
  let gets = 0;
  const operation = runBootstrap({
    snapshot,
    signal: controller.signal,
    bootstrap: () => mutation.promise,
    loadStatus: async () => { gets++; return expected; },
  });

  controller.abort();
  mutation.resolve(expected);
  assert.deepEqual(await operation, { kind: "failed", state: snapshot });
  assert.equal(gets, 1);
});

test("a resolved malformed response stays non-reconcilable even when Abort races its settlement", async () => {
  const snapshot = activeStatus("persona", 75);
  const controller = new AbortController();
  let gets = 0;
  const result = await runBootstrap({
    snapshot,
    signal: controller.signal,
    bootstrap: async () => {
      controller.abort();
      return { ...completedFor(snapshot, 78), internal_marker: true };
    },
    loadStatus: async () => { gets++; return completedFor(snapshot, 78); },
  });

  assert.deepEqual(result, { kind: "failed", state: snapshot });
  assert.equal(gets, 0);
});

test("Persona/Test mount exactly one automatic progress attempt and partial progress never loops", async (t) => {
  for (const phase of ["persona", "test"]) {
    await t.test(phase, async (subtest) => {
      const snapshot = activeStatus(phase, 80);
      let attempts = 0;
      let gets = 0;
      let postSignal;
      let getSignal;
      const { host, page } = mountPage(subtest, {
        initialStatus: snapshot,
        service: pageService({
          bootstrap: (_revision, signal) => {
            attempts++;
            postSignal = signal;
            return Promise.reject(new TypeError("lost"));
          },
          status: (signal) => {
            gets++;
            getSignal = signal;
            return Promise.resolve(partialTestFor(snapshot, snapshot.revision + 1));
          },
        }),
      });
      assertProgressOnly(host);
      page.mount(host);
      await settle();
      assert.equal(attempts, 1);
      assert.equal(gets, 1);
      assert.ok(postSignal instanceof AbortSignal);
      assert.equal(getSignal, postSignal);
      assertProgressOnly(host, { failed: true });
      await settle();
      assert.equal(attempts, 1, "a partial Test state must never auto-loop");
    });
  }
});

test("explicit Retry GETs first, then bootstraps the authoritative Persona/Test revision", async (t) => {
  const initial = activeStatus("persona", 90);
  const latest = activeStatus("test", 101);
  const revisions = [];
  const postSignals = [];
  let retrySignal;
  let gets = 0;
  const { host } = mountPage(t, {
    initialStatus: initial,
    service: pageService({
      bootstrap(revision, signal) {
        revisions.push(revision);
        postSignals.push(signal);
        if (revisions.length === 1) {
          return Promise.reject(Object.assign(new Error("known"), { status: 409, code: "BUSY" }));
        }
        return Promise.resolve(completedFor(latest, 103));
      },
      status: (signal) => { gets++; retrySignal = signal; return Promise.resolve(latest); },
    }),
  });
  await settle();
  assertProgressOnly(host, { failed: true });

  button(host, "Thử lại").click();
  button(host, "Thử lại")?.click();
  await settle();

  assert.equal(gets, 1);
  assert.deepEqual(revisions, [90, 101]);
  assert.ok(retrySignal instanceof AbortSignal);
  assert.equal(postSignals[1], retrySignal);
  assert.notEqual(postSignals[0], retrySignal);
  assert.match(text(host), /Bé Mi đã sẵn sàng!/u);
});

test("explicit Retry treats authoritative Completed as success without another POST", async (t) => {
  const initial = activeStatus("test", 110);
  const completed = completedFor(initial, 120);
  let posts = 0;
  const { host } = mountPage(t, {
    initialStatus: initial,
    service: pageService({
      bootstrap: () => {
        posts++;
        return Promise.reject(Object.assign(new Error("known"), { status: 502, code: "FAILED" }));
      },
      status: () => Promise.resolve(completed),
    }),
  });
  await settle();
  button(host, "Thử lại").click();
  await settle();

  assert.equal(posts, 1);
  assert.match(text(host), /Bé Mi đã sẵn sàng!/u);
});

test("bootstrap error can return to Provider and never exposes private failure text", async (t) => {
  const initial = activeStatus("persona", 130);
  let backs = 0;
  const provider = onboardingStatus("provider", { revision: 131 });
  const { host } = mountPage(t, {
    initialStatus: initial,
    service: pageService({
      bootstrap: () => Promise.reject(Object.assign(new Error("PRIVATE CONFIG DETAIL"), {
        status: 500, code: "INTERNAL",
      })),
      backToProviders(revision) {
        backs++;
        assert.equal(revision, 130);
        return Promise.resolve(provider);
      },
    }),
  });
  await settle();
  assertProgressOnly(host, { failed: true });
  assert.match(text(host), /Chưa thể chuẩn bị trợ lý\. Cấu hình cũ vẫn được giữ nguyên\./u);
  assert.doesNotMatch(text(host), /PRIVATE CONFIG DETAIL/u);

  button(host, "Quay lại Provider").click();
  await settle();
  assert.equal(backs, 1);
  assert.ok(byClass(host, "onboarding-provider-stage"));
});

test("browser bootstrap sources contain no retired Persona, Test, token, or Complete controls", async () => {
  const directory = new URL("../overlay/internal/webui/static/pages/", import.meta.url);
  const names = [
    "onboarding.js", "onboarding-contract.js", "onboarding-bootstrap.js",
    "onboarding-early-view.js", "onboarding-late-view.js",
  ];
  const sources = await Promise.all(names.map((name) => readFile(new URL(name, directory), "utf8")));
  const source = sources.join("\n");
  for (const retired of [
    /test_token/u,
    /onboarding-chat-input/u,
    /onboarding-persona-form/u,
    /createPersonaStage/u,
    /createTestStage/u,
    /normalizeTestMessage/u,
    /normalizeTestResponse/u,
    /normalizeCompleteResponse/u,
    /Ổn, dùng cấu hình này/u,
    /Gửi thử/u,
    /\/onboarding\/test-chat/u,
    /\/onboarding\/complete/u,
  ]) assert.doesNotMatch(source, retired);
  const earlySource = sources[names.indexOf("onboarding-early-view.js")];
  for (const retired of [
    /STEP_LABELS/u,
    /onboarding-rail/u,
    /Cá nhân hoá/u,
    /Trò chuyện thử/u,
    /Chat thử/u,
    /bấm Hoàn tất/u,
    /Persona/u,
  ]) assert.doesNotMatch(earlySource, retired);
});

test("disposing fences a bootstrap POST that resolves late", async (t) => {
  const snapshot = activeStatus("test", 140);
  const gate = deferred();
  let agentLoads = 0;
  let handoffs = 0;
  let reconciliations = 0;
  const { host, page } = mountPage(t, {
    initialStatus: snapshot,
    service: pageService({
      bootstrap: () => gate.promise,
      status: () => { reconciliations++; return Promise.resolve(completedFor(snapshot, 142)); },
      loadAgent: () => { agentLoads++; return Promise.resolve({ ready: true, display_name: "Late", placeholders: [] }); },
    }),
    onComplete: () => { handoffs++; },
  });

  page.dispose();
  gate.resolve(completedFor(snapshot, 142));
  await settle();
  assert.equal(reconciliations, 1);
  assert.equal(agentLoads, 0);
  assert.equal(handoffs, 0);
  assert.equal(text(host), "");
});

test("disposing fences the pending explicit-Retry GET and aborts its exact signal", async (t) => {
  const snapshot = activeStatus("persona", 150);
  const gate = deferred();
  let posts = 0;
  let retrySignal;
  let agentLoads = 0;
  const { host, page } = mountPage(t, {
    initialStatus: snapshot,
    service: pageService({
      bootstrap: () => {
        posts++;
        return Promise.reject(Object.assign(new Error("known"), { status: 503 }));
      },
      status: (signal) => { retrySignal = signal; return gate.promise; },
      loadAgent: () => { agentLoads++; return Promise.resolve({ ready: true, display_name: "Late", placeholders: [] }); },
    }),
  });
  await settle();
  button(host, "Thử lại").click();
  await flush();
  assert.ok(retrySignal instanceof AbortSignal);

  page.dispose();
  assert.equal(retrySignal.aborted, true);
  gate.resolve(completedFor(snapshot, 153));
  await settle();
  assert.equal(posts, 1);
  assert.equal(agentLoads, 0);
  assert.equal(text(host), "");
});

test("disposing fences pending Done label loading and aborts its exact signal", async (t) => {
  const gate = deferred();
  let loadSignal;
  let handoffs = 0;
  const { host, page } = mountPage(t, {
    initialStatus: completedFor(activeStatus(), 160),
    service: pageService({
      loadAgent: ({ signal }) => { loadSignal = signal; return gate.promise; },
    }),
    onComplete: () => { handoffs++; },
  });
  assert.ok(loadSignal instanceof AbortSignal);

  page.dispose();
  assert.equal(loadSignal.aborted, true);
  gate.resolve({ ready: true, display_name: "Late", placeholders: [] });
  await settle();
  assert.equal(handoffs, 0);
  assert.equal(text(host), "");
});

test("disposing fences pending Back mutation and aborts its exact signal", async (t) => {
  const snapshot = activeStatus("persona", 170);
  const gate = deferred();
  let backSignal;
  let fallbackGets = 0;
  const { host, page } = mountPage(t, {
    initialStatus: snapshot,
    service: pageService({
      bootstrap: () => Promise.reject(Object.assign(new Error("known"), { status: 500 })),
      backToProviders: (_revision, signal) => { backSignal = signal; return gate.promise; },
      status: () => { fallbackGets++; return Promise.resolve(onboardingStatus("provider")); },
    }),
  });
  await settle();
  button(host, "Quay lại Provider").click();
  await flush();
  assert.ok(backSignal instanceof AbortSignal);

  page.dispose();
  assert.equal(backSignal.aborted, true);
  gate.resolve(onboardingStatus("provider", { revision: 171 }));
  await settle();
  assert.equal(fallbackGets, 0);
  assert.equal(text(host), "");
});
