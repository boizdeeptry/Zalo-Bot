import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import * as appMain from "../overlay/internal/webui/static/app-main.js";
import { ROUTES, createRouteHost } from "../overlay/internal/webui/static/core/router.js";
import { find, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

const { createPortalController, loadRoutePage } = appMain;

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

function controllerHarness({ loadPage, pageContext, routeHost } = {}) {
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
      mounts.push({ context, page: routePage, routeId: context.routeId });
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
    pageContext,
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

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

function retryButton(root) {
  return find(root, (node) => node.tagName === "BUTTON" && text(node) === "Thử lại");
}

const completedStatus = (overrides = {}) => onboardingStatus("completed", overrides);
const requiredStatus = (overrides = {}) => onboardingStatus("provider", overrides);
const restartingStatus = (overrides = {}) => onboardingStatus("provider", {
  completed_version: 1,
  current_version: 1,
  required: true,
  restart_in_progress: true,
  ...overrides,
});

function appHarness(t, {
  loadStatus,
  hash = "#agents",
  portalFactory,
  wizardFactory,
  portalDispose,
  wizardDispose,
} = {}) {
  const dom = installDOM();
  t.after(dom.restore);
  const skip = document.createElement("a");
  const toggle = document.createElement("button");
  const rail = document.createElement("nav");
  const nav = document.createElement("div");
  const main = document.createElement("main");
  skip.className = "skip-link";
  skip.setAttribute("href", "#main");
  toggle.id = "rail-toggle";
  rail.id = "rail";
  nav.setAttribute("data-portal-nav", "");
  main.setAttribute("data-portal-content", "");
  for (const node of [skip, toggle, rail, nav]) node.inert = false;
  rail.append(nav);
  document.body.className = "portal-body";
  document.body.append(skip, toggle, rail, main);
  const bySelector = new Map([
    [".skip-link", skip],
    ["#rail-toggle", toggle],
    ["#rail", rail],
    ["[data-portal-nav]", nav],
    ["[data-portal-content]", main],
  ]);
  document.querySelector = (selector) => bySelector.get(selector) ?? null;

  const listeners = new Map();
  const windowRef = {
    location: { hash },
    addEventListener(type, listener) {
      if (!listeners.has(type)) listeners.set(type, new Set());
      listeners.get(type).add(listener);
    },
    removeEventListener(type, listener) {
      listeners.get(type)?.delete(listener);
    },
    dispatch(type) {
      for (const listener of [...(listeners.get(type) ?? [])]) listener({ type });
    },
  };
  const events = [];
  const portals = [];
  const wizards = [];
  const startPortal = (context) => {
    events.push("mount:portal");
    const build = () => {
      const portal = {
        context,
        disposeCalls: 0,
        dispose() {
          this.disposeCalls++;
          events.push("dispose:portal");
          return portalDispose?.(this);
        },
      };
      portals.push(portal);
      return portal;
    };
    return portalFactory ? portalFactory(context, build) : build();
  };
  const mountOnboarding = (context) => {
    events.push("mount:wizard");
    const build = () => {
      const wizard = {
        context,
        disposeCalls: 0,
        dispose() {
          this.disposeCalls++;
          events.push("dispose:wizard");
          return wizardDispose?.(this);
        },
      };
      wizards.push(wizard);
      return wizard;
    };
    return wizardFactory ? wizardFactory(context, build) : build();
  };
  const app = appMain.startApp({
    document,
    window: windowRef,
    loadStatus,
    mountOnboarding,
    startPortal,
  });
  t.after(() => app.dispose());
  return { app, events, listeners, main, nav, portals, rail, skip, toggle, windowRef, wizards };
}

test("the top-level application exposes the sole startup owner", () => {
  assert.equal(typeof appMain.startApp, "function");
});

test("startup immediately gates the Portal while authoritative status is pending", async (t) => {
  const gate = deferred();
  const harness = appHarness(t, { loadStatus: (signal) => {
    assert.ok(signal instanceof AbortSignal);
    return gate.promise;
  } });

  assert.equal(harness.portals.length, 0);
  assert.equal(harness.wizards.length, 0);
  assert.equal(harness.rail.hidden, true);
  assert.equal(harness.skip.getAttribute("aria-hidden"), "true");
  assert.equal(harness.skip.inert, true);
  assert.equal(harness.toggle.getAttribute("aria-hidden"), "true");
  assert.equal(harness.nav.inert, true);
  assert.equal(document.body.classList.contains("onboarding-gated"), true);
  assert.match(text(harness.main), /đang.*thiết lập/iu);
  assert.equal(harness.listeners.get("hashchange")?.size ?? 0, 0);
  harness.windowRef.dispatch("hashchange");
  assert.equal(harness.portals.length, 0);

  gate.resolve(completedStatus());
  await harness.app.ready;
});

test("a completed lifecycle restores the shell and mounts Portal exactly once", async (t) => {
  const harness = appHarness(t, { loadStatus: async () => completedStatus() });
  const settled = await harness.app.ready;

  assert.equal(settled, true);
  assert.equal(harness.portals.length, 1);
  assert.equal(harness.wizards.length, 0);
  assert.equal(harness.rail.hidden, false);
  assert.equal(harness.rail.hasAttribute("aria-hidden"), false);
  assert.equal(harness.skip.hasAttribute("aria-hidden"), false);
  assert.equal(harness.skip.inert, false);
  assert.equal(harness.nav.inert, false);
  assert.equal(document.body.classList.contains("onboarding-gated"), false);
  assert.equal(Object.isFrozen(harness.app), true);
});

test("required and restart lifecycles mount only the gated wizard", async (t) => {
  for (const status of [requiredStatus(), restartingStatus()]) {
    await t.test(status.restart_in_progress ? "restart" : "required", async (subtest) => {
      const harness = appHarness(subtest, { loadStatus: async () => status });
      await harness.app.ready;
      assert.equal(harness.portals.length, 0);
      assert.equal(harness.wizards.length, 1);
      assert.deepEqual(harness.wizards[0].context.initialStatus, status);
      assert.equal(harness.rail.hidden, true);
      assert.equal(document.body.classList.contains("onboarding-gated"), true);
    });
  }
});

test("malformed status and load errors fail closed with a safe Retry", async (t) => {
  for (const first of [
    completedStatus({ completed_version: 0 }),
    new Error("SECRET backend detail"),
  ]) {
    await t.test(first instanceof Error ? "error" : "contradiction", async (subtest) => {
      let calls = 0;
      const harness = appHarness(subtest, {
        loadStatus: async () => {
          calls++;
          if (calls === 1 && first instanceof Error) throw first;
          return calls === 1 ? first : completedStatus();
        },
      });
      assert.equal(await harness.app.ready, false);
      assert.equal(harness.portals.length, 0);
      assert.doesNotMatch(text(harness.main), /SECRET/u);
      const retry = find(harness.main, (node) => node.tagName === "BUTTON" && text(node) === "Thử lại");
      assert.ok(retry);
      retry.click();
      await new Promise((resolve) => setTimeout(resolve, 0));
      assert.equal(harness.portals.length, 1);
    });
  }
});

test("double retry aborts superseded loads and stale settlements cannot mount", async (t) => {
  const attempts = [deferred(), deferred(), deferred()];
  const signals = [];
  let index = 0;
  const harness = appHarness(t, {
    loadStatus(signal) {
      signals.push(signal);
      return attempts[index++].promise;
    },
  });
  const second = harness.app.retry();
  const third = harness.app.retry();
  assert.equal(signals[0].aborted, true);
  assert.equal(signals[1].aborted, true);
  attempts[1].resolve(completedStatus());
  await second;
  assert.equal(harness.portals.length, 0);
  attempts[2].resolve(requiredStatus({ revision: 9 }));
  await third;
  attempts[0].reject(new Error("stale SECRET"));
  await harness.app.ready;
  assert.equal(harness.portals.length, 0);
  assert.equal(harness.wizards.length, 1);
  assert.doesNotMatch(text(harness.main), /SECRET/u);
});

test("dispose aborts pending status and beforeunload disposes the active app once", async (t) => {
  await t.test("pending", async (subtest) => {
    const gate = deferred();
    let signal;
    const harness = appHarness(subtest, { loadStatus(value) { signal = value; return gate.promise; } });
    harness.app.dispose();
    harness.app.dispose();
    assert.equal(signal.aborted, true);
    gate.resolve(completedStatus());
    assert.equal(await harness.app.ready, false);
    assert.equal(harness.portals.length, 0);
  });

  await t.test("beforeunload", async (subtest) => {
    const harness = appHarness(subtest, { loadStatus: async () => completedStatus() });
    await harness.app.ready;
    harness.windowRef.dispatch("beforeunload");
    harness.windowRef.dispatch("beforeunload");
    assert.equal(harness.portals[0].disposeCalls, 1);
    assert.equal(harness.listeners.get("beforeunload")?.size ?? 0, 0);
  });
});

test("wizard completion disposes first, revalidates once, then routes through Portal", async (t) => {
  const completedGate = deferred();
  let loads = 0;
  const harness = appHarness(t, {
    loadStatus: async () => {
      loads++;
      return loads === 1 ? requiredStatus() : completedGate.promise;
    },
  });
  await harness.app.ready;
  const handoff = harness.wizards[0].context.onComplete;
  const first = handoff({ destination: "knowledge" });
  const duplicate = handoff({ destination: "portal" });
  assert.equal(harness.wizards[0].disposeCalls, 1);
  assert.deepEqual(harness.events, ["mount:wizard", "dispose:wizard"]);
  assert.equal(loads, 2);
  completedGate.resolve(completedStatus({ revision: 12 }));
  await first;
  await duplicate;
  assert.deepEqual(harness.events, ["mount:wizard", "dispose:wizard", "mount:portal"]);
  assert.equal(harness.windowRef.location.hash, "#knowledge");
});

test("malformed wizard destinations are ignored without leaving the wizard", async (t) => {
  let loads = 0;
  const harness = appHarness(t, {
    loadStatus: async () => { loads++; return requiredStatus(); },
  });
  await harness.app.ready;
  assert.equal(await harness.wizards[0].context.onComplete({ destination: "settings" }), false);
  assert.equal(loads, 1);
  assert.equal(harness.wizards[0].disposeCalls, 0);
  assert.equal(harness.portals.length, 0);
});

test("validated Settings restart disposes Portal before mounting the wizard", async (t) => {
  const harness = appHarness(t, { loadStatus: async () => completedStatus() });
  await harness.app.ready;
  const restart = harness.portals[0].context.onRestartOnboarding;
  assert.equal(restart(restartingStatus({ revision: 12 })), true,
    "Settings may have refreshed a newer completed revision than startup loaded");
  assert.deepEqual(harness.events, ["mount:portal", "dispose:portal", "mount:wizard"]);
  assert.equal(harness.wizards.length, 1);
  assert.equal(harness.rail.hidden, true);
  assert.equal(restart(restartingStatus({ provider_id: "codex", revision: 13 })), false);
  assert.equal(harness.wizards.length, 1);
});

test("an uncertain committed restart is reconciled once by the application owner", async (t) => {
  await t.test("server committed the restart", async (subtest) => {
    const gate = deferred();
    let loads = 0;
    const harness = appHarness(subtest, {
      loadStatus: async () => ++loads === 1 ? completedStatus() : gate.promise,
    });
    await harness.app.ready;

    const reconcile = harness.portals[0].context.onRestartOnboarding;
    const first = reconcile();
    const duplicate = reconcile();
    assert.equal(loads, 2);
    assert.equal(harness.portals[0].disposeCalls, 0,
      "Portal stays owned until the authoritative status proves a restart");
    gate.resolve(restartingStatus({ revision: 12 }));
    assert.equal(await first, true);
    assert.equal(await duplicate, true);
    assert.deepEqual(harness.events, ["mount:portal", "dispose:portal", "mount:wizard"]);
  });

  await t.test("server did not commit the restart", async (subtest) => {
    let loads = 0;
    const harness = appHarness(subtest, {
      loadStatus: async () => { loads++; return completedStatus({ revision: loads + 10 }); },
    });
    await harness.app.ready;

    assert.equal(await harness.portals[0].context.onRestartOnboarding(), false);
    assert.equal(loads, 2);
    assert.deepEqual(harness.events, ["mount:portal"]);
    assert.equal(harness.portals[0].disposeCalls, 0);
    assert.equal(harness.rail.hidden, false);
  });
});

test("a throwing Portal disposer blocks restart until Retry confirms cleanup", async (t) => {
  let cleanupCalls = 0;
  let loads = 0;
  const harness = appHarness(t, {
    loadStatus: async () => ++loads === 1 ? completedStatus() : restartingStatus(),
    portalDispose() {
      cleanupCalls++;
      if (cleanupCalls === 1) throw new Error("SECRET cleanup detail");
    },
  });
  await harness.app.ready;

  const result = harness.portals[0].context.onRestartOnboarding(restartingStatus());
  assert.equal(await Promise.resolve(result), false);
  assert.equal(harness.wizards.length, 0);
  assert.equal(harness.rail.hidden, true);
  assert.ok(retryButton(harness.main));
  retryButton(harness.main).click();
  await flush();

  assert.equal(loads, 2);
  assert.equal(cleanupCalls, 2);
  assert.equal(harness.wizards.length, 1);
  assert.deepEqual(harness.events, [
    "mount:portal", "dispose:portal", "dispose:portal", "mount:wizard",
  ]);
});

test("a throwing wizard disposer blocks completion until Retry confirms cleanup", async (t) => {
  let cleanupCalls = 0;
  let loads = 0;
  const harness = appHarness(t, {
    loadStatus: async () => ++loads === 1 ? requiredStatus() : completedStatus(),
    wizardDispose() {
      cleanupCalls++;
      if (cleanupCalls === 1) throw new Error("SECRET cleanup detail");
    },
  });
  await harness.app.ready;

  assert.equal(await harness.wizards[0].context.onComplete({ destination: "portal" }), false);
  assert.equal(loads, 1, "authoritative GET waits until cleanup is confirmed");
  assert.equal(harness.portals.length, 0);
  assert.ok(retryButton(harness.main));
  retryButton(harness.main).click();
  await flush();

  assert.equal(loads, 2);
  assert.equal(cleanupCalls, 2);
  assert.equal(harness.portals.length, 1);
});

test("mount failures fail closed and Retry authoritatively mounts one successor", async (t) => {
  await t.test("asynchronous Portal mount rejection", async (subtest) => {
    const rejection = deferred();
    rejection.promise.catch(() => {});
    let attempts = 0;
    const harness = appHarness(subtest, {
      loadStatus: async () => completedStatus(),
      portalFactory(_context, build) {
        attempts++;
        return attempts === 1 ? rejection.promise : build();
      },
    });
    rejection.reject(new Error("SECRET portal mount detail"));
    assert.equal(await harness.app.ready, false);
    assert.equal(harness.portals.length, 0);
    assert.ok(retryButton(harness.main));
    retryButton(harness.main).click();
    await flush();
    assert.equal(attempts, 2);
    assert.equal(harness.portals.length, 1);
  });

  await t.test("synchronous wizard mount throw", async (subtest) => {
    let attempts = 0;
    const harness = appHarness(subtest, {
      loadStatus: async () => requiredStatus(),
      wizardFactory(_context, build) {
        attempts++;
        if (attempts === 1) throw new Error("SECRET wizard mount detail");
        return build();
      },
    });
    assert.equal(await harness.app.ready, false);
    assert.equal(harness.wizards.length, 0);
    assert.ok(retryButton(harness.main));
    retryButton(harness.main).click();
    await flush();
    assert.equal(attempts, 2);
    assert.equal(harness.wizards.length, 1);
  });
});

test("a mount resolving after application disposal is immediately cleaned up", async (t) => {
  const mountGate = deferred();
  const harness = appHarness(t, {
    loadStatus: async () => requiredStatus(),
    wizardFactory: (_context, build) => mountGate.promise.then(build),
  });
  await flush();
  assert.deepEqual(harness.events, ["mount:wizard"]);
  harness.app.dispose();
  mountGate.resolve();

  assert.equal(await harness.app.ready, false);
  assert.equal(harness.wizards.length, 1);
  assert.equal(harness.wizards[0].disposeCalls, 1);
  assert.deepEqual(harness.events, ["mount:wizard", "dispose:wizard"]);
});

test("a stale route resolving after a newer route never mounts or focuses", async () => {
  const agents = deferred();
  const combos = deferred();
  const harness = controllerHarness({
    loadPage: (route) => ({ agents, combos })[route.id].promise,
  });

  const first = harness.controller.navigate();
  harness.setHash("#combos");
  const second = harness.controller.navigate();
  const combosPage = page("combos");
  combos.resolve(combosPage);
  await second;
  agents.resolve(page("agents"));
  await first;

  assert.deepEqual(harness.mounts.map(({ page: mounted, routeId }) => ({ page: mounted, routeId })), [
    { page: combosPage, routeId: "combos" },
  ]);
  assert.equal(harness.focusCalls.length, 1);
  assert.deepEqual(harness.navigation, ["agents", "combos"]);
  assert.deepEqual(harness.titles, [
    "AI Agents · Trợ lý Zalo",
    "Combos · Trợ lý Zalo",
  ]);
});

test("a stale route rejection cannot replace the newer mounted page", async () => {
  const agents = deferred();
  const combos = deferred();
  const harness = controllerHarness({
    loadPage: (route) => ({ agents, combos })[route.id].promise,
  });

  const first = harness.controller.navigate();
  harness.setHash("#combos");
  const second = harness.controller.navigate();
  const combosPage = page("combos");
  combos.resolve(combosPage);
  await second;
  agents.reject(new Error("stale load failed"));
  await first;

  assert.deepEqual(harness.mounts.map(({ page: mounted, routeId }) => ({ page: mounted, routeId })), [
    { page: combosPage, routeId: "combos" },
  ]);
  assert.equal(harness.errors.length, 0);
  assert.equal(harness.reports.length, 0);
});

test("route mounts receive the injected immutable page context", async () => {
  const callback = () => {};
  const routePage = page("settings");
  const harness = controllerHarness({
    loadPage: async () => routePage,
    pageContext: { onRestartOnboarding: callback },
  });
  harness.setHash("#settings");

  await harness.controller.navigate();

  assert.equal(harness.mounts[0].routeId, "settings");
  assert.equal(harness.mounts[0].context.onRestartOnboarding, callback);
  assert.equal(Object.isFrozen(harness.mounts[0].context), true);
});

test("cleanup failure cannot hide the current route error or run cleanup twice", async () => {
  let hash = "#agents";
  let cleanupCalls = 0;
  const routeError = new Error("combos failed to load");
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
      if (route.id === "combos") throw routeError;
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
  hash = "#combos";
  await controller.navigate();

  assert.equal(cleanupCalls, 1);
  assert.equal(rendered.at(-1)[0].routeError, routeError);
  assert.equal(reports[0][1], routeError);
  assert.match(reports[1][1].message, /agent cleanup failed/);
});

test("the providers hash lazy-loads the real Providers module", async () => {
  const mounts = [];
  const reports = [];
  const controller = createPortalController({
    nav: {},
    content: { replaceChildren() {} },
    getHash: () => "#providers",
    routeHost: {
      mount: (page, context) => mounts.push({ page, routeId: context.routeId }),
      dispose() {},
    },
    renderNavigation() {},
    renderError: (error) => error,
    setTitle() {},
    focusContent() {},
    reportError: (...args) => reports.push(args),
  });

  await controller.navigate();

  assert.deepEqual(reports, []);
  assert.equal(mounts.length, 1);
  assert.equal(mounts[0].routeId, "providers");
  assert.equal(typeof mounts[0].page.mount, "function");
});

test("dispose invalidates pending navigation and disposes the host once", async () => {
  const combos = deferred();
  const agentsPage = page("agents");
  const harness = controllerHarness({
    loadPage: (route) => route.id === "agents"
      ? Promise.resolve(agentsPage)
      : combos.promise,
  });

  await harness.controller.navigate();
  harness.setHash("#combos");
  const pending = harness.controller.navigate();
  harness.controller.dispose();
  harness.controller.dispose();
  combos.resolve(page("combos"));
  await pending;

  assert.deepEqual(harness.mounts.map(({ page: mounted, routeId }) => ({ page: mounted, routeId })), [
    { page: agentsPage, routeId: "agents" },
  ]);
  assert.equal(harness.focusCalls.length, 1);
  assert.equal(harness.disposeCalls, 1);
});

test("Memory route lazy-loads the real page module", async () => {
  const page = await loadRoutePage(ROUTES.memory);
  assert.equal(typeof page.mount, "function");
});

test("keyboard-focused selects retain the shared two-pixel focus indicator", async () => {
  const css = await readFile(new URL(
    "../overlay/internal/webui/static/portal.css",
    import.meta.url,
  ), "utf8");
  const selectFocus = css.match(/\.portal-body select:focus-visible\s*\{([^}]*)\}/s)?.[1] ?? "";

  assert.doesNotMatch(selectFocus, /outline\s*:\s*none\b/i);
  assert.match(css, /\.portal-body :focus-visible\s*\{[^}]*outline\s*:\s*2px\s+solid\s+var\(--live\)/s);
});
