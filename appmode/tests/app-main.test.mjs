import test from "node:test";
import assert from "node:assert/strict";

import { createPortalController } from "../overlay/internal/webui/static/app-main.js";
import { createRouteHost } from "../overlay/internal/webui/static/core/router.js";

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((onResolve, onReject) => {
    resolve = onResolve;
    reject = onReject;
  });
  return { promise, reject, resolve };
}

function page(name) {
  return { name, mount() { return { dispose() {} }; } };
}

function controllerHarness({ loadPage, routeHost } = {}) {
  let hash = "#agents";
  const mounts = [];
  const focusCalls = [];
  const navigation = [];
  const titles = [];
  const errors = [];
  const reports = [];
  let disposeCalls = 0;
  const content = {
    replaceChildren(...children) {
      errors.push(children);
    },
  };
  const host = routeHost ?? {
    mount(routePage, context) {
      mounts.push({ page: routePage, routeId: context.routeId });
    },
    dispose() {
      disposeCalls += 1;
    },
  };
  const controller = createPortalController({
    nav: {},
    content,
    getHash: () => hash,
    loadPage,
    routeHost: host,
    renderNavigation: ({ activeId }) => navigation.push(activeId),
    renderError: (error) => ({ routeError: error }),
    setTitle: (title) => titles.push(title),
    focusContent: () => focusCalls.push(hash),
    reportError: (...args) => reports.push(args),
  });

  return {
    content,
    controller,
    errors,
    focusCalls,
    get disposeCalls() { return disposeCalls; },
    mounts,
    navigation,
    reports,
    setHash(value) { hash = value; },
    titles,
  };
}

test("a stale route resolving after a newer route never mounts or focuses", async () => {
  const agents = deferred();
  const models = deferred();
  const harness = controllerHarness({
    loadPage: (route) => ({ agents, models })[route.id].promise,
  });

  const first = harness.controller.navigate();
  harness.setHash("#models");
  const second = harness.controller.navigate();
  const modelsPage = page("models");
  models.resolve(modelsPage);
  await second;
  agents.resolve(page("agents"));
  await first;

  assert.deepEqual(harness.mounts, [{ page: modelsPage, routeId: "models" }]);
  assert.equal(harness.focusCalls.length, 1);
  assert.deepEqual(harness.navigation, ["agents", "models"]);
  assert.deepEqual(harness.titles, [
    "AI Agents · Trợ lý Zalo",
    "Models · Trợ lý Zalo",
  ]);
});

test("a stale route rejection cannot replace the newer mounted page", async () => {
  const agents = deferred();
  const models = deferred();
  const harness = controllerHarness({
    loadPage: (route) => ({ agents, models })[route.id].promise,
  });

  const first = harness.controller.navigate();
  harness.setHash("#models");
  const second = harness.controller.navigate();
  const modelsPage = page("models");
  models.resolve(modelsPage);
  await second;
  agents.reject(new Error("stale load failed"));
  await first;

  assert.deepEqual(harness.mounts, [{ page: modelsPage, routeId: "models" }]);
  assert.equal(harness.errors.length, 0);
  assert.equal(harness.reports.length, 0);
});

test("cleanup failure cannot hide the current route error or run cleanup twice", async () => {
  let hash = "#agents";
  let cleanupCalls = 0;
  const routeError = new Error("models failed to load");
  const rendered = [];
  const reports = [];
  const content = {
    replaceChildren(...children) {
      rendered.push(children);
    },
  };
  const controller = createPortalController({
    nav: {},
    content,
    getHash: () => hash,
    loadPage: async (route) => {
      if (route.id === "models") throw routeError;
      return {
        mount() {
          return {
            dispose() {
              cleanupCalls += 1;
              throw new Error("agent cleanup failed");
            },
          };
        },
      };
    },
    routeHost: createRouteHost(content),
    renderNavigation() {},
    renderError: (error) => ({ routeError: error }),
    setTitle() {},
    focusContent() {},
    reportError: (...args) => reports.push(args),
  });

  await controller.navigate();
  hash = "#models";
  await controller.navigate();

  assert.equal(cleanupCalls, 1);
  assert.equal(rendered.at(-1)[0].routeError, routeError);
  assert.equal(reports[0][1], routeError);
  assert.match(reports[1][1].message, /agent cleanup failed/);
});

test("dispose invalidates pending navigation and disposes the host once", async () => {
  const models = deferred();
  const agentsPage = page("agents");
  const harness = controllerHarness({
    loadPage: (route) => route.id === "agents"
      ? Promise.resolve(agentsPage)
      : models.promise,
  });

  await harness.controller.navigate();
  harness.setHash("#models");
  const pending = harness.controller.navigate();
  harness.controller.dispose();
  harness.controller.dispose();
  models.resolve(page("models"));
  await pending;

  assert.deepEqual(harness.mounts, [{ page: agentsPage, routeId: "agents" }]);
  assert.equal(harness.focusCalls.length, 1);
  assert.equal(harness.disposeCalls, 1);
});
