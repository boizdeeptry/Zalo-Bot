import test from "node:test";
import assert from "node:assert/strict";

import * as settings from "../overlay/internal/webui/static/pages/settings.js";
import {
  find,
  installDOM,
  text,
} from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

const {
  createSettingsController,
  createSettingsService,
  mount,
  normalizeRestartStatus,
} = settings;

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((onResolve, onReject) => {
    resolve = onResolve;
    reject = onReject;
  });
  return { promise, reject, resolve };
}

const completedStatus = (overrides = {}) => onboardingStatus("completed", {
  revision: 11,
  ...overrides,
});

const restartStatus = (overrides = {}) => onboardingStatus("provider", {
  completed_version: 1,
  current_version: 1,
  required: true,
  restart_in_progress: true,
  revision: 12,
  ...overrides,
});

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

function button(root, label) {
  return find(root, (node) => node.tagName === "BUTTON" && text(node).trim() === label);
}

function mountController(t, service, onRestarted = () => {}) {
  const dom = installDOM();
  t.after(dom.restore);
  const host = document.createElement("main");
  document.body.append(host);
  const controller = createSettingsController({ service, onRestarted });
  controller.mount(host);
  t.after(() => controller.dispose());
  return { controller, host };
}

test("Settings service sends only the authoritative GET and exact confirmed restart body", async () => {
  const calls = [];
  const signal = new AbortController().signal;
  const requestJSON = async (path, options = {}) => {
    calls.push({ path, options });
    return path.endsWith("restart") ? restartStatus() : completedStatus();
  };
  const service = createSettingsService(requestJSON);

  assert.deepEqual(await service.status(signal), completedStatus());
  assert.deepEqual(await service.restart(11, signal), restartStatus());
  assert.deepEqual(calls, [
    { path: "/onboarding/status", options: { signal } },
    {
      path: "/onboarding/restart",
      options: { method: "POST", body: { confirmed: true, revision: 11 }, signal },
    },
  ]);
});

test("restart response validation requires an exact clean successor lifecycle", () => {
  assert.deepEqual(normalizeRestartStatus(restartStatus({ ignored: "not projected" }), 11), restartStatus());
  const invalid = [
    restartStatus({ revision: 11 }),
    restartStatus({ revision: 13 }),
    restartStatus({ required: false }),
    restartStatus({ restart_in_progress: false }),
    restartStatus({ phase: "connect", provider_kind: "codex" }),
    restartStatus({ completed_version: 0 }),
    restartStatus({ provider_kind: "codex" }),
    restartStatus({ provider_id: "codex" }),
    restartStatus({ account_id: "account-1" }),
    restartStatus({ model_id: "gpt-5.6-terra" }),
    restartStatus({ suggested_provider_kind: "unsupported" }),
    { ...restartStatus(), revision: Number.MAX_SAFE_INTEGER + 1 },
  ];
  for (const response of invalid) {
    assert.equal(normalizeRestartStatus(response, 11), null, JSON.stringify(response));
  }
});

test("the first click only opens a complete warning and Cancel keeps the Portal active", async (t) => {
  let restarts = 0;
  const { host } = mountController(t, {
    status: async () => completedStatus(),
    restart: async () => { restarts++; return restartStatus(); },
  });
  await flush();

  const restartButton = button(host, "Thiết lập lại trợ lý");
  assert.equal(restartButton.classList.contains("btn"), true,
    "Settings uses the shared button style instead of another page's scoped CSS");
  restartButton.click();
  assert.equal(restarts, 0);
  const warning = find(host, (node) => node.getAttribute?.("role") === "alert");
  assert.match(text(warning), /đăng nhập.*thiết lập lại/iu);
  assert.match(text(warning), /phải.*hoàn tất/iu);
  assert.match(text(warning), /tuyến.*vẫn.*hoạt động.*Complete/iu);
  assert.ok(button(host, "Xác nhận thiết lập lại"));
  button(host, "Huỷ").click();
  assert.equal(button(host, "Xác nhận thiết lập lại"), null);
  assert.ok(button(host, "Thiết lập lại trợ lý"));
  assert.equal(restarts, 0);
});

test("confirmation is one-flight, uses the loaded revision, and hands off once", async (t) => {
  const gate = deferred();
  const calls = [];
  const handoffs = [];
  const { host } = mountController(t, {
    status: async () => completedStatus(),
    restart(revision, signal) {
      calls.push({ revision, signal });
      return gate.promise;
    },
  }, (status) => handoffs.push(status));
  await flush();

  button(host, "Thiết lập lại trợ lý").click();
  const confirm = button(host, "Xác nhận thiết lập lại");
  confirm.click();
  confirm.click();
  assert.equal(calls.length, 1);
  assert.equal(calls[0].revision, 11);
  assert.ok(calls[0].signal instanceof AbortSignal);
  assert.equal(confirm.disabled, true);
  assert.equal(button(host, "Huỷ").disabled, true,
    "confirmation cannot be canceled after the restart mutation owns the request");
  gate.resolve(restartStatus());
  await flush();
  assert.deepEqual(handoffs, [restartStatus()]);
  confirm.click();
  assert.equal(calls.length, 1);
  assert.equal(handoffs.length, 1);
});

test("only a validated completed status offers restart; malformed loads fail closed with Retry", async (t) => {
  let loads = 0;
  const { host } = mountController(t, {
    status: async () => {
      loads++;
      return loads === 1
        ? completedStatus({ completed_version: 0 })
        : completedStatus();
    },
    restart: async () => restartStatus(),
  });
  await flush();
  assert.equal(button(host, "Thiết lập lại trợ lý"), null);
  assert.match(text(host), /không thể.*an toàn/iu);
  button(host, "Thử lại").click();
  await flush();
  assert.equal(loads, 2);
  assert.ok(button(host, "Thiết lập lại trợ lý"));
});

test("a rejected restart reports uncertainty to the application owner without a local GET", async (t) => {
  let loads = 0;
  const handoffs = [];
  const { host } = mountController(t, {
    status: async () => { loads++; return completedStatus(); },
    restart: async () => {
      throw Object.assign(new Error("SECRET conflict detail"), {
        code: "ONBOARDING_REVISION_CONFLICT",
        status: 409,
      });
    },
  }, (...args) => handoffs.push(args));
  await flush();
  button(host, "Thiết lập lại trợ lý").click();
  button(host, "Xác nhận thiết lập lại").click();
  await flush();

  assert.equal(loads, 1, "only the application owner may reconcile the uncertain POST");
  assert.deepEqual(handoffs, [[]]);
  assert.doesNotMatch(text(host), /SECRET/u);
});

test("retry supersedes stale loads and dispose aborts the owned status GET", async (t) => {
  const stale = deferred();
  const fresh = deferred();
  const pending = deferred();
  const signals = [];
  let loads = 0;
  const { controller, host } = mountController(t, {
    status(signal) {
      signals.push(signal);
      loads++;
      if (loads === 1) return stale.promise;
      if (loads === 2) return fresh.promise;
      return pending.promise;
    },
    restart: async () => restartStatus(),
  });

  const retry = controller.retry();
  assert.equal(signals[0].aborted, true);
  fresh.resolve(completedStatus());
  await retry;
  stale.resolve(restartStatus());
  await flush();
  assert.ok(button(host, "Thiết lập lại trợ lý"));

  const abandoned = controller.retry();
  controller.dispose();
  controller.dispose();
  assert.equal(signals.at(-1).aborted, true);
  pending.resolve(completedStatus());
  assert.equal(await abandoned, false);
  assert.equal(text(host), "");
});

test("a committed restart success survives route disposal and hands off exactly once", async (t) => {
  const mutation = deferred();
  const signals = [];
  const handoffs = [];
  const { controller, host } = mountController(t, {
    status: async () => completedStatus(),
    restart(_revision, signal) {
      signals.push(signal);
      return mutation.promise;
    },
  }, (...args) => handoffs.push(args));
  await flush();

  button(host, "Thiết lập lại trợ lý").click();
  button(host, "Xác nhận thiết lập lại").click();
  controller.dispose();
  controller.dispose();
  assert.equal(signals[0].aborted, false,
    "disposing the Settings view must not cancel an already dispatched POST");
  mutation.resolve(restartStatus());
  await flush();

  assert.deepEqual(handoffs, [[restartStatus()]]);
  assert.equal(text(host), "");
});

test("a committed restart rejection survives disposal and reports uncertainty once", async (t) => {
  const mutation = deferred();
  const signals = [];
  const handoffs = [];
  const { controller, host } = mountController(t, {
    status: async () => completedStatus(),
    restart(_revision, signal) {
      signals.push(signal);
      return mutation.promise;
    },
  }, (...args) => handoffs.push(args));
  await flush();

  button(host, "Thiết lập lại trợ lý").click();
  button(host, "Xác nhận thiết lập lại").click();
  controller.dispose();
  mutation.reject(new Error("SECRET transport detail"));
  await flush();

  assert.equal(signals[0].aborted, false);
  assert.deepEqual(handoffs, [[]]);
  assert.equal(text(host), "");
});

test("the route mount adapter forwards its context callback and returns a disposer", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const host = document.createElement("main");
  document.body.append(host);
  const callbacks = [];
  const controller = mount(host, {
    onRestartOnboarding: (status) => callbacks.push(status),
    settingsService: {
      status: async () => completedStatus(),
      restart: async () => restartStatus(),
    },
  });
  t.after(() => controller.dispose());
  await flush();
  button(host, "Thiết lập lại trợ lý").click();
  button(host, "Xác nhận thiết lập lại").click();
  await flush();
  assert.deepEqual(callbacks, [restartStatus()]);
  assert.equal(typeof controller.dispose, "function");
});
