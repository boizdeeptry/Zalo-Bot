import test from "node:test";
import assert from "node:assert/strict";

import {
  createProviderConnect,
  INSTALL_CEILING,
  MAX_CONNECT_LOG_LENGTH,
  phaseProgress,
} from "../overlay/internal/webui/static/components/provider-connect.js";
import { createProviderService } from "../overlay/internal/webui/static/pages/providers.js";
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

test("ordinary start sends the exact legacy body without onboarding_revision", async (t) => {
  const calls = [];
  const statusGate = deferred();
  const service = createProviderService(async (path, options = {}) => {
    calls.push({ path, options });
    if (options.method === "POST") return { kind: "codex", phase: "detecting" };
    return statusGate.promise;
  });
  const { controller, slot } = mountConnect(t, { service, pollDelayMs: 0 });

  assert.equal(controller.start({ label: "Tài khoản thường" }), true);
  button(slot, "Bắt đầu").click();
  await flush();

  const post = calls.find((call) => call.options.method === "POST");
  assert.deepEqual(post, {
    path: "/llm/providers/codex/connect",
    options: { method: "POST", body: { label: "Tài khoản thường" } },
  });
  assert.equal(Object.hasOwn(post.options.body, "onboarding_revision"), false);
});

test("positive onboarding revision is sent with the label, while zero is omitted", async (t) => {
  const calls = [];
  const statusGate = deferred();
  const service = createProviderService(async (path, options = {}) => {
    calls.push({ path, options });
    if (options.method === "POST") return { kind: "codex", phase: "detecting" };
    return statusGate.promise;
  });
  const { controller, slot } = mountConnect(t, { service });

  controller.start({ label: "Onboarding", onboardingRevision: 7 });
  button(slot, "Bắt đầu").click();
  await flush();

  assert.deepEqual(calls[0], {
    path: "/llm/providers/codex/connect",
    options: { method: "POST", body: { label: "Onboarding", onboarding_revision: 7 } },
  });

  controller.start({ label: "Không revision", onboardingRevision: 0 });
  button(slot, "Bắt đầu").click();
  await flush();
  assert.deepEqual(calls[2].options.body, { label: "Không revision" });

  controller.start({ label: "Null revision", onboardingRevision: null });
  button(slot, "Bắt đầu").click();
  await flush();
  assert.deepEqual(calls[4].options.body, { label: "Null revision" });
});

test("elapsed time and installing progress use one stopped-on-dispose timer", async (t) => {
  const clock = createClock();
  const startGate = deferred();
  let currentTime = 1_000;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => startGate.promise,
      connectStatus: () => Promise.resolve({ phase: "polling" }),
      connectCancel: () => Promise.resolve({ ok: true }),
    },
    now: () => currentTime,
    crawlDelayMs: 500,
    ...clock,
  });

  controller.start({ label: "Timer" });
  button(slot, "Bắt đầu").click();
  assert.equal(clock.intervalCount, 1);
  assert.equal(find(slot, (node) => hasClass(node, "pv-progress-fill")).style.width, "8%");
  assert.equal(text(find(slot, (node) => hasClass(node, "pv-progress-elapsed"))), "· 0s");

  currentTime = 3_900;
  clock.tickIntervals();
  assert.equal(text(find(slot, (node) => hasClass(node, "pv-progress-elapsed"))), "· 2s");

  controller.dispose();
  assert.equal(clock.intervalCount, 0);
  assert.equal(clock.timeoutCount, 0);
});

test("polling preserves the login link and device-auth code", async (t) => {
  const clock = createClock();
  const { controller, slot } = mountConnect(t, {
    service: serviceWithStatus({
      phase: "polling",
      loginUrl: "https://auth.example/device",
      code: "EQ0J-QKCPZ",
    }),
    ...clock,
  });

  controller.start({ label: "Tài khoản 1" });
  button(slot, "Bắt đầu").click();
  await flush();

  const link = find(slot, (node) => node.tagName === "A" && text(node) === "Mở trang đăng nhập");
  assert.equal(link?.getAttribute("href"), "https://auth.example/device");
  assert.equal(link?.getAttribute("target"), "_blank");
  assert.equal(link?.getAttribute("rel"), "noopener");
  assert.equal(text(find(slot, (node) => hasClass(node, "pv-connect-code-value"))), "EQ0J-QKCPZ");
  assert.equal(find(slot, (node) => hasClass(node, "pv-progress")).getAttribute("aria-valuenow"), "90");
});

test("live terminal output is bounded, control-free, and rendered only as text", async (t) => {
  const clock = createClock();
  const malicious = `<img src=x onerror=alert(1)>\u0000${"a".repeat(MAX_CONNECT_LOG_LENGTH + 200)}`;
  const { controller, slot } = mountConnect(t, {
    service: serviceWithStatus({ phase: "installing", message: malicious }),
    ...clock,
  });

  controller.start({ label: "Log" });
  button(slot, "Bắt đầu").click();
  await flush();

  const log = find(slot, (node) => hasClass(node, "pv-connect-log"));
  assert.ok(text(log).startsWith("<img src=x onerror=alert(1)>"));
  assert.equal(text(log).includes("\u0000"), false);
  assert.ok(text(log).length <= MAX_CONNECT_LOG_LENGTH);
  assert.equal(find(log, (node) => node.tagName === "IMG"), null);
});

test("cancel invalidates an in-flight poll before awaiting the server", async (t) => {
  const statusGate = deferred();
  const cancelGate = deferred();
  const connected = [];
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    pollDelayMs: 0,
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => { statusCalls++; return statusGate.promise; },
      connectCancel: () => cancelGate.promise,
    },
    onConnected: (result) => connected.push(result),
  });

  controller.start({ label: "Cancel" });
  button(slot, "Bắt đầu").click();
  await flush();
  assert.equal(statusCalls, 1);

  button(slot, "Huỷ").click();
  statusGate.resolve({
    phase: "connected",
    providerId: "codex",
    accountId: "stale-account",
  });
  await flush();
  assert.deepEqual(connected, []);
  assert.equal(statusCalls, 1);

  cancelGate.resolve({ ok: true });
  await flush();
  assert.match(text(slot), /Đã huỷ kết nối/);
  assert.deepEqual(connected, []);
});

test("an accepted cancel preserves the last backend message like the Providers UI", async (t) => {
  const clock = createClock();
  const { controller, slot } = mountConnect(t, {
    service: serviceWithStatus({ phase: "installing", message: "npm: added 90 packages" }),
    ...clock,
  });

  controller.start({ label: "Cancel log" });
  button(slot, "Bắt đầu").click();
  await flush();
  button(slot, "Huỷ").click();
  await flush();

  assert.match(text(slot), /npm: added 90 packages/);
  assert.ok(hasClass(find(slot, (node) => hasClass(node, "pv-progress")), "pv-progress--error"));
});

test("ok:false reconciles an authoritative connected result exactly once", async (t) => {
  const staleStatus = deferred();
  const connected = [];
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    pollDelayMs: 0,
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        statusCalls++;
        if (statusCalls === 1) return staleStatus.promise;
        return Promise.resolve({ phase: "connected", providerId: "codex", accountId: "reconciled" });
      },
      connectCancel: () => Promise.resolve({ ok: false }),
    },
    onConnected: (result) => connected.push(result),
  });

  controller.start({ label: "Completed during cancel" });
  button(slot, "Bắt đầu").click();
  await flush();
  button(slot, "Huỷ").click();
  await flush();
  await flush();

  assert.equal(statusCalls, 2, "cancel starts a fresh authoritative status generation");
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "reconciled" }]);

  staleStatus.resolve({ phase: "connected", providerId: "codex", accountId: "stale" });
  await flush();
  assert.equal(connected.length, 1, "the invalidated pre-cancel poll cannot also complete");
});

test("ok:false warns through a nonterminal reconciliation and keeps polling to connected", async (t) => {
  const clock = createClock();
  const staleStatus = deferred();
  const connected = [];
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        statusCalls++;
        if (statusCalls === 1) return staleStatus.promise;
        if (statusCalls === 2) {
          return Promise.resolve({ phase: "polling", message: "Kết nối vẫn đang được hoàn tất." });
        }
        return Promise.resolve({ phase: "connected", providerId: "codex", accountId: "after-warning" });
      },
      connectCancel: () => Promise.resolve({ ok: false, message: "Tác vụ đã đổi trạng thái." }),
    },
    onConnected: (result) => connected.push(result),
    ...clock,
  });

  controller.start({ label: "Reconcile" });
  button(slot, "Bắt đầu").click();
  await flush();
  button(slot, "Huỷ").click();
  await flush();
  await flush();

  const warning = find(slot, (node) => hasClass(node, "pv-connect-warning"));
  assert.ok(warning, "a rejected ownership claim remains visibly nonterminal");
  assert.match(text(warning), /Tiếp tục theo dõi kết nối/);
  assert.match(text(slot), /Kết nối vẫn đang được hoàn tất/);
  assert.equal(clock.timeoutCount, 1, "the fresh generation waits at normal poll cadence");

  clock.runTimeouts();
  await flush();
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "after-warning" }]);

  staleStatus.resolve({ phase: "connected", providerId: "codex", accountId: "stale" });
  await flush();
  assert.equal(connected.length, 1);
});

test("a DELETE transport error warns and continues authoritative polling", async (t) => {
  const clock = createClock();
  const connected = [];
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        statusCalls++;
        if (statusCalls < 3) return Promise.resolve({ phase: "polling", message: "Đang chờ đăng nhập." });
        return Promise.resolve({ phase: "connected", providerId: "codex", accountId: "after-delete-error" });
      },
      connectCancel: () => Promise.reject(new Error("Không thể huỷ kết nối")),
    },
    onConnected: (result) => connected.push(result),
    ...clock,
  });

  controller.start({ label: "DELETE error" });
  button(slot, "Bắt đầu").click();
  await flush();
  button(slot, "Huỷ").click();
  await flush();
  await flush();

  const warning = find(slot, (node) => hasClass(node, "pv-connect-warning"));
  assert.match(text(warning), /Không thể huỷ kết nối/);
  assert.match(text(warning), /Tiếp tục theo dõi kết nối/);
  assert.match(text(slot), /Đang chờ đăng nhập/);
  assert.equal(statusCalls, 2, "the failed DELETE starts a fresh status request");
  assert.equal(clock.timeoutCount, 1);

  clock.runTimeouts();
  await flush();
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "after-delete-error" }]);
});

test("a failed first reconciliation fetch preserves live state, warns, and retries", async (t) => {
  const clock = createClock();
  const connected = [];
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        statusCalls++;
        if (statusCalls === 1) return Promise.resolve({ phase: "polling", message: "Đăng nhập vẫn đang chờ xác nhận." });
        if (statusCalls === 2) return Promise.reject(new Error("Không đọc được trạng thái sau khi huỷ"));
        return Promise.resolve({ phase: "connected", providerId: "codex", accountId: "after-get-error" });
      },
      connectCancel: () => Promise.resolve({ ok: false }),
    },
    onConnected: (result) => connected.push(result),
    ...clock,
  });

  controller.start({ label: "GET error" });
  button(slot, "Bắt đầu").click();
  await flush();
  button(slot, "Huỷ").click();
  await flush();
  await flush();

  const warning = find(slot, (node) => hasClass(node, "pv-connect-warning"));
  assert.match(text(warning), /Không đọc được trạng thái sau khi huỷ/);
  assert.match(text(slot), /Đăng nhập vẫn đang chờ xác nhận/);
  assert.equal(clock.timeoutCount, 1, "a reconciliation failure schedules an authoritative retry");

  clock.runTimeouts();
  await flush();
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "after-get-error" }]);
});

test("a newer manual retry suppresses an in-flight cancel reconciliation callback", async (t) => {
  const reconciliation = deferred();
  const connected = [];
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        statusCalls++;
        if (statusCalls === 1) return Promise.resolve({ phase: "polling" });
        if (statusCalls === 2) return reconciliation.promise;
        return Promise.resolve({ phase: "connected", providerId: "codex", accountId: "new-run" });
      },
      connectCancel: () => Promise.resolve({ ok: false }),
    },
    onConnected: (result) => connected.push(result),
    pollDelayMs: 60_000,
  });

  controller.start({ label: "Old run" });
  button(slot, "Bắt đầu").click();
  await flush();
  button(slot, "Huỷ").click();
  await flush();
  assert.equal(statusCalls, 2);

  controller.start({ label: "New run" });
  reconciliation.resolve({ phase: "connected", providerId: "codex", accountId: "stale-reconcile" });
  await flush();
  assert.deepEqual(connected, []);
  assert.match(text(slot), /Bắt đầu kết nối/);

  button(slot, "Bắt đầu").click();
  await flush();
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "new-run" }]);
});

test("dispose suppresses an in-flight cancel reconciliation callback and retry", async (t) => {
  const reconciliation = deferred();
  const connected = [];
  let statusCalls = 0;
  const clock = createClock();
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        statusCalls++;
        return statusCalls === 1 ? Promise.resolve({ phase: "polling" }) : reconciliation.promise;
      },
      connectCancel: () => Promise.resolve({ ok: false }),
    },
    onConnected: (result) => connected.push(result),
    ...clock,
  });

  controller.start({ label: "Dispose race" });
  button(slot, "Bắt đầu").click();
  await flush();
  button(slot, "Huỷ").click();
  await flush();
  assert.equal(statusCalls, 2);

  controller.dispose();
  reconciliation.resolve({ phase: "connected", providerId: "codex", accountId: "late" });
  await flush();

  assert.deepEqual(connected, []);
  assert.equal(clock.intervalCount, 0);
  assert.equal(clock.timeoutCount, 0);
  assert.equal(text(slot), "");
});

test("a retry is isolated from a stale status result from the prior run", async (t) => {
  const firstStatus = deferred();
  const secondStatus = deferred();
  const connected = [];
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        statusCalls++;
        return statusCalls === 1 ? firstStatus.promise : secondStatus.promise;
      },
      connectCancel: () => Promise.resolve({ ok: true }),
    },
    onConnected: (result) => connected.push(result),
  });

  controller.start({ label: "Cũ" });
  button(slot, "Bắt đầu").click();
  await flush();
  controller.start({ label: "Mới" });
  button(slot, "Bắt đầu").click();
  await flush();

  secondStatus.resolve({ phase: "connected", providerId: "codex", accountId: "new" });
  await flush();
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "new" }]);

  firstStatus.resolve({ phase: "error", message: "Lỗi stale không được vẽ" });
  await flush();
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "new" }]);
  assert.doesNotMatch(text(slot), /Lỗi stale/);
});

test("a terminal connected snapshot fires its callback exactly once", async (t) => {
  const connected = [];
  let statusCalls = 0;
  const { controller, slot } = mountConnect(t, {
    pollDelayMs: 0,
    service: serviceWithStatus(() => {
      statusCalls++;
      return Promise.resolve({ phase: "connected", providerId: "codex", accountId: "a1" });
    }),
    onConnected: (result) => connected.push(result),
  });

  controller.start({ label: "Only once" });
  button(slot, "Bắt đầu").click();
  await flush();
  await flush();

  assert.equal(statusCalls, 1);
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "a1" }]);
  assert.equal(text(slot), "");
});

test("errors freeze the bar and a later start creates a clean retry", async (t) => {
  const secondStatus = deferred();
  let starts = 0;
  const connected = [];
  const { controller, slot } = mountConnect(t, {
    service: {
      connectStart: () => Promise.resolve({ phase: "detecting" }),
      connectStatus: () => {
        starts++;
        if (starts === 1) throw new Error("Kết nối thất bại");
        return secondStatus.promise;
      },
      connectCancel: () => Promise.resolve({ ok: true }),
    },
    onConnected: (result) => connected.push(result),
  });

  controller.start({ label: "Lần 1" });
  button(slot, "Bắt đầu").click();
  await flush();
  assert.match(text(slot), /Kết nối thất bại/);
  assert.ok(hasClass(find(slot, (node) => hasClass(node, "pv-progress")), "pv-progress--error"));

  controller.start({ label: "Lần 2" });
  assert.equal(find(slot, (node) => hasClass(node, "pv-progress--error")), null);
  button(slot, "Bắt đầu").click();
  await flush();
  secondStatus.resolve({ phase: "connected", providerId: "codex", accountId: "retry" });
  await flush();
  assert.deepEqual(connected, [{ kind: "codex", providerId: "codex", accountId: "retry" }]);
});

test("prompt cancel closes the component and calls the optional back hook", (t) => {
  let backs = 0;
  const { controller, slot } = mountConnect(t, {
    service: serviceWithStatus({ phase: "polling" }),
    onBack: () => { backs++; },
  });

  controller.start({ label: "Back" });
  button(slot, "Huỷ").click();

  assert.equal(backs, 1);
  assert.equal(text(slot), "");
});

test("dispose is idempotent, clears timers, and makes retained controls inert", async (t) => {
  const clock = createClock();
  let starts = 0;
  const dom = installDOM();
  t.after(() => dom.restore());
  const slot = document.createElement("div");
  const controller = createProviderConnect({
    kind: "codex",
    service: {
      connectStart: () => { starts++; return Promise.resolve(); },
      connectStatus: () => Promise.resolve({ phase: "polling" }),
      connectCancel: () => Promise.resolve({ ok: true }),
    },
    onConnected: () => {},
    ...clock,
  });
  controller.mount(slot);
  controller.start({ label: "Dispose" });
  const retainedStart = button(slot, "Bắt đầu");

  controller.dispose();
  controller.dispose();
  retainedStart.click();
  await flush();

  assert.equal(starts, 0);
  assert.equal(clock.intervalCount, 0);
  assert.equal(clock.timeoutCount, 0);
  assert.equal(text(slot), "");
  assert.equal(controller.start({ label: "Late" }), false);
});

test("start before mount and double start safely retain only the newest prompt", async (t) => {
  const dom = installDOM();
  t.after(() => dom.restore());
  const calls = [];
  const statusGate = deferred();
  const controller = createProviderConnect({
    kind: "codex",
    service: serviceWithStatus(() => statusGate.promise, calls),
    onConnected: () => {},
  });

  assert.ok(Object.isFrozen(controller), "the public controller is immutable");
  assert.equal(controller.start({ label: "Cũ", onboardingRevision: 3 }), true);
  assert.equal(controller.start({ label: "Mới", onboardingRevision: 4 }), true);
  const slot = document.createElement("div");
  assert.equal(controller.mount(slot), controller);
  assert.equal(find(slot, (node) => hasClass(node, "pv-connect-label")).value, "Mới");

  button(slot, "Bắt đầu").click();
  await flush();
  assert.deepEqual(calls[0], {
    op: "start",
    kind: "codex",
    label: "Mới",
    onboardingRevision: 4,
  });

  controller.dispose();
});
