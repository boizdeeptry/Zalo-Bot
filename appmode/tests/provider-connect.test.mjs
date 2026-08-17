import test from "node:test";
import assert from "node:assert/strict";

import {
  createProviderConnect,
  INSTALL_CEILING,
  MAX_CONNECT_LOG_LENGTH,
  phaseProgress,
} from "../overlay/internal/webui/static/components/provider-connect.js";
import { find, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((onResolve, onReject) => {
    resolve = onResolve;
    reject = onReject;
  });
  return { promise, resolve, reject };
};
const hasClass = (node, className) => node.classList?.contains(className) ?? false;
const button = (root, label) => find(
  root,
  (node) => node.tagName === "BUTTON" && text(node).includes(label),
);

function captureUnhandledRejections(t) {
  const reasons = [];
  const listener = (reason) => reasons.push(reason);
  process.on("unhandledRejection", listener);
  t.after(() => process.removeListener("unhandledRejection", listener));
  return reasons;
}

function createClock() {
  let nextID = 1;
  const intervals = new Map();
  const timeouts = new Map();
  const timer = (entries, callback) => {
    const id = nextID++;
    entries.set(id, callback);
    return id;
  };
  return {
    setIntervalFn: (callback) => timer(intervals, callback),
    clearIntervalFn: (id) => intervals.delete(id),
    setTimeoutFn: (callback) => timer(timeouts, callback),
    clearTimeoutFn: (id) => timeouts.delete(id),
    tickIntervals() {
      for (const callback of [...intervals.values()]) callback();
    },
    runTimeouts() {
      const callbacks = [...timeouts.values()];
      timeouts.clear();
      for (const callback of callbacks) callback();
    },
    get intervalCount() { return intervals.size; },
    get timeoutCount() { return timeouts.size; },
  };
}

function mountConnect(t, {
  kind = "codex",
  service,
  onConnected = () => {},
  onBack = null,
  ...options
} = {}) {
  const dom = installDOM();
  const slot = document.createElement("div");
  slot.className = "pv-connect";
  const controller = createProviderConnect({ kind, service, onConnected, onBack, ...options });
  controller.mount(slot);
  t.after(() => {
    controller.dispose();
    dom.restore();
  });
  return { controller, slot };
}

function serviceWithStatus(status, calls = []) {
  return {
    connectStart(kind, label, onboardingRevision) {
      calls.push({ op: "start", kind, label, onboardingRevision });
      return Promise.resolve({ kind, phase: "detecting" });
    },
    connectStatus(kind) {
      calls.push({ op: "status", kind });
      return typeof status === "function" ? status() : Promise.resolve({ kind, ...status });
    },
    connectCancel(kind) {
      calls.push({ op: "cancel", kind });
      return Promise.resolve({ ok: true });
    },
  };
}

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

test("phaseProgress preserves the Providers phase anchors", () => {
  assert.equal(phaseProgress("detecting"), 8);
  assert.equal(phaseProgress("installing"), 12);
  assert.ok(phaseProgress("installing") < INSTALL_CEILING);
  assert.ok(INSTALL_CEILING < 100);
  assert.equal(phaseProgress("awaiting_login"), 90);
  assert.equal(phaseProgress("polling"), 90);
  assert.equal(phaseProgress("connected"), 100);
  assert.equal(phaseProgress("prompt"), null);
  assert.equal(phaseProgress("error"), null);
  assert.equal(phaseProgress("canceled"), null);
});

test("idle, missing, and unknown ordinary status phases stop as protocol errors without timer leaks", async (t) => {
  const cases = [
    ["idle", { phase: "idle" }, /không còn tác vụ kết nối/i],
    ["missing", {}, /trạng thái kết nối không hợp lệ/i],
    ["unknown", { phase: "teleporting" }, /trạng thái kết nối không hợp lệ/i],
  ];

  for (const [name, status, expectedMessage] of cases) {
    await t.test(name, async (subtest) => {
      const clock = createClock();
      const connected = [];
      let statusCalls = 0;
      const { controller, slot } = mountConnect(subtest, {
        service: {
          connectStart: () => Promise.resolve({ phase: "detecting" }),
          connectStatus: () => { statusCalls++; return Promise.resolve(status); },
          connectCancel: () => Promise.resolve({ ok: true }),
        },
        onConnected: (result) => connected.push(result),
        ...clock,
      });

      controller.start({ label: `Invalid ${name}` });
      button(slot, "Bắt đầu").click();
      await flush();
      await flush();

      assert.match(text(slot), expectedMessage);
      assert.ok(hasClass(find(slot, (node) => hasClass(node, "pv-progress")), "pv-progress--error"));
      assert.equal(statusCalls, 1);
      assert.deepEqual(connected, []);
      assert.equal(clock.intervalCount, 0);
      assert.equal(clock.timeoutCount, 0);
    });
  }
});

test("idle, missing, and unknown reconciliation phases terminate the fresh generation without leaks", async (t) => {
  const cases = [
    ["idle", { phase: "idle" }, /không còn tác vụ kết nối/i],
    ["missing", {}, /trạng thái kết nối không hợp lệ/i],
    ["unknown", { phase: "teleporting" }, /trạng thái kết nối không hợp lệ/i],
  ];

  for (const [name, reconciledStatus, expectedMessage] of cases) {
    await t.test(name, async (subtest) => {
      const clock = createClock();
      const staleStatus = deferred();
      const connected = [];
      let statusCalls = 0;
      const { controller, slot } = mountConnect(subtest, {
        service: {
          connectStart: () => Promise.resolve({ phase: "detecting" }),
          connectStatus: () => {
            statusCalls++;
            return statusCalls === 1 ? staleStatus.promise : Promise.resolve(reconciledStatus);
          },
          connectCancel: () => Promise.resolve({ ok: false }),
        },
        onConnected: (result) => connected.push(result),
        ...clock,
      });

      controller.start({ label: `Reconcile ${name}` });
      button(slot, "Bắt đầu").click();
      await flush();
      button(slot, "Huỷ").click();
      await flush();
      await flush();

      assert.match(text(slot), expectedMessage);
      assert.ok(hasClass(find(slot, (node) => hasClass(node, "pv-progress")), "pv-progress--error"));
      assert.equal(statusCalls, 2);
      assert.deepEqual(connected, []);
      assert.equal(clock.intervalCount, 0);
      assert.equal(clock.timeoutCount, 0);

      staleStatus.resolve({ phase: "connected", providerId: "codex", accountId: "stale" });
      await flush();
      assert.deepEqual(connected, []);
    });
  }
});

test("an invalid start snapshot terminates before the first status request", async (t) => {
  const clock = createClock();
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "idle" }),
      connectStatus: () => { statusCalls++; return Promise.resolve({ phase: "polling" }); },
      connectCancel: () => Promise.resolve({ ok: true }),
    },
    ...clock,
  });

  controller.start({ label: "No job" });
  button(slot, "Bắt đầu").click();
  await flush();

  assert.match(text(slot), /không còn tác vụ kết nối/i);
  assert.equal(statusCalls, 0);
  assert.equal(clock.intervalCount, 0);
  assert.equal(clock.timeoutCount, 0);
});

test("malformed fields in a start snapshot are discarded before rendering", async (t) => {
  const clock = createClock();
  const statusGate = deferred();
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({
        phase: "polling",
        loginUrl: 42,
        code: { value: "poison" },
        message: ["poison"],
        log: { value: "poison" },
      }),
      connectStatus: () => statusGate.promise,
      connectCancel: () => Promise.resolve({ ok: true }),
    },
    ...clock,
  });

  controller.start({ label: "Malformed start" });
  button(slot, "Bắt đầu").click();
  await flush();

  assert.ok(find(slot, (node) => hasClass(node, "pv-connect-live")));
  assert.equal(find(slot, (node) => node.tagName === "A"), null);
  assert.equal(find(slot, (node) => hasClass(node, "pv-connect-code")), null);
  assert.doesNotMatch(text(slot), /poison|\[object Object\]/);
  assert.equal(clock.intervalCount, 1);
});

test("ordinary status snapshots discard invalid fields, validate HTTPS, and bound display strings", async (t) => {
  const clock = createClock();
  const oversizedCode = "X".repeat(MAX_CONNECT_LOG_LENGTH + 200);
  const { controller, slot } = mountConnect(t, {
    service: serviceWithStatus({
      phase: "polling",
      loginUrl: "https://",
      code: oversizedCode,
      message: { value: "poison" },
      log: ["poison"],
    }),
    ...clock,
  });

  controller.start({ label: "Malformed status" });
  button(slot, "Bắt đầu").click();
  await flush();

  assert.ok(find(slot, (node) => hasClass(node, "pv-connect-live")));
  assert.equal(find(slot, (node) => node.tagName === "A"), null, "an invalid HTTPS URL is discarded");
  const code = find(slot, (node) => hasClass(node, "pv-connect-code-value"));
  assert.ok(code);
  assert.equal(text(code).length, MAX_CONNECT_LOG_LENGTH);
  assert.doesNotMatch(text(slot), /poison|\[object Object\]/);
  assert.equal(clock.timeoutCount, 1);
});

test("oversized HTTPS URLs are rejected instead of rendered after truncation", async (t) => {
  const clock = createClock();
  const { controller, slot } = mountConnect(t, {
    service: serviceWithStatus({
      phase: "polling",
      loginUrl: `https://auth.example/${"a".repeat(MAX_CONNECT_LOG_LENGTH)}`,
    }),
    ...clock,
  });

  controller.start({ label: "Oversized URL" });
  button(slot, "Bắt đầu").click();
  await flush();

  assert.equal(find(slot, (node) => node.tagName === "A"), null);
  assert.equal(clock.timeoutCount, 1);
});

test("connected snapshots with non-string terminal ids never invoke the callback", async (t) => {
  const clock = createClock();
  const connected = [];
  const { controller, slot } = mountConnect(t, {
    service: serviceWithStatus({
      phase: "connected",
      providerId: 42,
      accountId: ["a1"],
    }),
    onConnected: (result) => connected.push(result),
    ...clock,
  });

  controller.start({ label: "Malformed IDs" });
  button(slot, "Bắt đầu").click();
  await flush();

  assert.deepEqual(connected, []);
  assert.match(text(slot), /thiếu định danh Provider hoặc tài khoản/);
  assert.ok(hasClass(find(slot, (node) => hasClass(node, "pv-progress")), "pv-progress--error"));
  assert.equal(clock.intervalCount, 0);
  assert.equal(clock.timeoutCount, 0);
});

test("control-bearing and oversized terminal ids are rejected instead of rewritten", async (t) => {
  const cases = [
    ["control-bearing provider id", "codex\nforged", "a1", /phản hồi trạng thái kết nối không hợp lệ/i],
    ["oversized account id", "codex", "a".repeat(MAX_CONNECT_LOG_LENGTH + 1), /thiếu định danh Provider hoặc tài khoản/],
  ];

  for (const [name, providerId, accountId, expectedMessage] of cases) {
    await t.test(name, async (subtest) => {
      const clock = createClock();
      const connected = [];
      const { controller, slot } = mountConnect(subtest, {
        service: serviceWithStatus({ phase: "connected", providerId, accountId }),
        onConnected: (result) => connected.push(result),
        ...clock,
      });

      controller.start({ label: name });
      button(slot, "Bắt đầu").click();
      await flush();

      assert.deepEqual(connected, []);
      assert.match(text(slot), expectedMessage);
      assert.equal(clock.intervalCount, 0);
      assert.equal(clock.timeoutCount, 0);
    });
  }
});

test("start and ordinary status snapshots reject contradictory provider identities", async (t) => {
  const cases = [
    [
      "immediate nonterminal start kind mismatch",
      "start",
      "codex",
      { kind: "claude-code", phase: "polling" },
    ],
    [
      "immediate start kind mismatch",
      "start",
      "codex",
      { kind: "claude-code", phase: "connected", providerId: "codex", accountId: "a1" },
    ],
    [
      "immediate start provider id mismatch",
      "start",
      "codex",
      { kind: "codex", phase: "connected", providerId: "claude-code", accountId: "a1" },
    ],
    [
      "ordinary status swaps Claude for Codex",
      "status",
      "claude-code",
      { kind: "codex", phase: "connected", providerId: "codex", accountId: "a1" },
    ],
    [
      "ordinary status changes kind case",
      "status",
      "codex",
      { kind: "Codex", phase: "connected", providerId: "codex", accountId: "a1" },
    ],
    [
      "ordinary status pads the provider id",
      "status",
      "codex",
      { kind: "codex", phase: "connected", providerId: " codex", accountId: "a1" },
    ],
  ];

  for (const [name, path, kind, snapshot] of cases) {
    await t.test(name, async (subtest) => {
      const clock = createClock();
      const connected = [];
      const unhandled = captureUnhandledRejections(subtest);
      let statusCalls = 0;
      const { controller, slot } = mountConnect(subtest, {
        kind,
        service: {
          connectStart: () => Promise.resolve(
            path === "start" ? snapshot : { kind, phase: "detecting" },
          ),
          connectStatus: () => {
            statusCalls++;
            return Promise.resolve(snapshot);
          },
          connectCancel: () => Promise.resolve({ ok: true }),
        },
        onConnected: (result) => connected.push(result),
        ...clock,
      });

      controller.start({ label: name });
      button(slot, "Bắt đầu").click();
      await flush();
      await flush();

      assert.deepEqual(connected, []);
      assert.match(text(slot), /phản hồi trạng thái kết nối không hợp lệ/i);
      assert.ok(hasClass(find(slot, (node) => hasClass(node, "pv-progress")), "pv-progress--error"));
      assert.equal(statusCalls, path === "status" ? 1 : 0);
      assert.equal(clock.intervalCount, 0);
      assert.equal(clock.timeoutCount, 0);
      assert.deepEqual(unhandled, []);
    });
  }
});

test("cancel reconciliation rejects contradictory provider identities on every recovery branch", async (t) => {
  const cases = [
    [
      "ok:false reconciliation pads kind",
      "rejected",
      { kind: "codex ", phase: "connected", providerId: "codex", accountId: "a1" },
    ],
    [
      "ok:false reconciliation controls provider id",
      "rejected",
      { kind: "codex", phase: "connected", providerId: "codex\n", accountId: "a1" },
    ],
    [
      "DELETE error reconciliation controls kind",
      "transport",
      { kind: "codex\u0000", phase: "connected", providerId: "codex", accountId: "a1" },
    ],
    [
      "DELETE error reconciliation changes provider id case",
      "transport",
      { kind: "codex", phase: "connected", providerId: "Codex", accountId: "a1" },
    ],
  ];

  for (const [name, cancelMode, snapshot] of cases) {
    await t.test(name, async (subtest) => {
      const clock = createClock();
      const staleStatus = deferred();
      const connected = [];
      const unhandled = captureUnhandledRejections(subtest);
      let statusCalls = 0;
      const { controller, slot } = mountConnect(subtest, {
        service: {
          connectStart: () => Promise.resolve({ kind: "codex", phase: "detecting" }),
          connectStatus: () => {
            statusCalls++;
            return statusCalls === 1 ? staleStatus.promise : Promise.resolve(snapshot);
          },
          connectCancel: () => cancelMode === "rejected"
            ? Promise.resolve({ ok: false })
            : Promise.reject(new Error("DELETE failed")),
        },
        onConnected: (result) => connected.push(result),
        ...clock,
      });

      controller.start({ label: name });
      button(slot, "Bắt đầu").click();
      await flush();
      button(slot, "Huỷ").click();
      await flush();
      await flush();

      assert.equal(statusCalls, 2);
      assert.deepEqual(connected, []);
      assert.match(text(slot), /phản hồi trạng thái kết nối không hợp lệ/i);
      assert.ok(hasClass(find(slot, (node) => hasClass(node, "pv-progress")), "pv-progress--error"));
      assert.equal(clock.intervalCount, 0);
      assert.equal(clock.timeoutCount, 0);
      assert.deepEqual(unhandled, []);

      staleStatus.resolve({
        kind: "codex",
        phase: "connected",
        providerId: "codex",
        accountId: "stale",
      });
      await flush();
      assert.deepEqual(connected, []);
      assert.deepEqual(unhandled, []);
    });
  }
});

test("an exact immediate connected identity invokes the callback once", async (t) => {
  const clock = createClock();
  const connected = [];
  const unhandled = captureUnhandledRejections(t);
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({
        kind: "codex",
        phase: "connected",
        providerId: "codex",
        accountId: "a1",
      }),
      connectStatus: () => {
        statusCalls++;
        return Promise.resolve({ phase: "connected", providerId: "codex", accountId: "duplicate" });
      },
      connectCancel: () => Promise.resolve({ ok: true }),
    },
    onConnected: (result) => connected.push(result),
    ...clock,
  });

  controller.start({ label: "Exact identity" });
  button(slot, "Bắt đầu").click();
  await flush();

  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "a1" }]);
  assert.equal(statusCalls, 0);
  assert.equal(clock.intervalCount, 0);
  assert.equal(clock.timeoutCount, 0);
  assert.deepEqual(unhandled, []);
});

test("malformed cancel and reconciliation snapshots stay safe, retry, and never reject unhandled", async (t) => {
  const clock = createClock();
  const staleStatus = deferred();
  const connected = [];
  const unhandled = captureUnhandledRejections(t);
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        statusCalls++;
        if (statusCalls === 1) return staleStatus.promise;
        if (statusCalls === 2) {
          return Promise.resolve({
            phase: "polling",
            loginUrl: 42,
            code: ["poison"],
            message: { value: "poison" },
            log: ["poison"],
          });
        }
        return Promise.resolve({ phase: "connected", providerId: {}, accountId: 7 });
      },
      connectCancel: () => Promise.resolve({
        ok: "true",
        message: { value: "poison" },
        error: ["poison"],
      }),
    },
    onConnected: (result) => connected.push(result),
    ...clock,
  });

  controller.start({ label: "Malformed reconciliation" });
  button(slot, "Bắt đầu").click();
  await flush();
  button(slot, "Huỷ").click();
  await flush();
  await flush();

  const warning = find(slot, (node) => hasClass(node, "pv-connect-warning"));
  assert.ok(warning);
  assert.match(text(warning), /Tiếp tục theo dõi kết nối/);
  assert.doesNotMatch(text(slot), /poison|\[object Object\]/);
  assert.equal(find(slot, (node) => node.tagName === "A"), null);
  assert.equal(find(slot, (node) => hasClass(node, "pv-connect-code")), null);
  assert.equal(clock.timeoutCount, 1, "malformed nonterminal reconciliation still retries");

  clock.runTimeouts();
  await flush();
  await flush();

  assert.deepEqual(connected, []);
  assert.match(text(slot), /thiếu định danh Provider hoặc tài khoản/);
  assert.equal(clock.intervalCount, 0);
  assert.equal(clock.timeoutCount, 0);
  assert.deepEqual(unhandled, []);

  staleStatus.resolve({ phase: "connected", providerId: "codex", accountId: "stale" });
  await flush();
  assert.deepEqual(connected, []);
});
