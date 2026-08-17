import test from "node:test";
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { createProviderService, createProvidersPage, INSTALL_CEILING, phaseProgress } from "../overlay/internal/webui/static/pages/providers.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import {
  CLAUDE_ADDED,
  providerRuntimeResponse,
} from "./helpers/provider-runtime-fixtures.mjs";
const staticRoot = new URL("../overlay/internal/webui/static/", import.meta.url);
const flush = () => new Promise((r) => setImmediate(r));
const settle = async () => { await flush(); await flush(); };
const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((accept, decline) => { resolve = accept; reject = decline; });
  return { promise, resolve, reject };
};
const hasClass = (n, c) => n.classList?.contains(c) ?? false;
const cards = (main) => findAll(main, (n) => hasClass(n, "pv-card"));
const cardName = (card) => text(find(card, (n) => hasClass(n, "pv-name")));
const enabledSwitch = (root) => find(root, (node) => node.getAttribute?.("role") === "switch"
  && node.hasAttribute?.("data-provider-enabled-switch"));
const changeEnabled = (control, enabled) => {
  control.checked = enabled;
  control.dispatchEvent({ type: "change" });
};
const offDialog = () => find(document.body, (node) => node.tagName === "DIALOG");
const dialogButton = (label) => find(offDialog(), (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const enabledError = (root) => find(root, (node) => hasClass(node, "pv-enabled-error"));
const providerUpdates = (calls) => calls.filter((call) => call.options.method === "PUT"
  && /^\/llm\/providers\/[^/]+$/u.test(call.path));
const expectedUpdate = (id, name, enabled) => ({
  path: `/llm/providers/${id}`,
  options: { method: "PUT", body: { name, enabled, credential: "" } },
});
const progressBar = (main) => find(main, (n) => hasClass(n, "pv-progress"));
const fillPct = (main) => {
  const fill = find(main, (n) => hasClass(n, "pv-progress-fill"));
  return fill ? parseFloat(fill.style.width) : NaN;
};
async function openConnectAt(t, kind, cardLabel, status) {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === `/llm/providers/${kind}/connect` && options.method === "POST") return { kind, phase: "detecting" };
    if (path === `/llm/providers/${kind}/connect` && !options.method) return { kind, ...status };
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === cardLabel).click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))).click();
  for (let k = 0; k < 20; k++) await new Promise((r) => setTimeout(r, 0));
  return main;
}
function mountInDocument(t, handler, opts = {}) {
  const calls = [];
  const page = createProvidersPage({ pollMs: 0, ...opts, request: async (path, options = {}) => {
    const { signal, ...recorded } = options; calls.push({ path, options: recorded }); return handler(path, options);
  }});
  const main = document.createElement("main");
  document.body.append(main);
  const mounted = page.mount(main);
  t.after(() => mounted.dispose());
  return { calls, main, mounted };
}
function mountPage(t, handler, opts = {}) {
  const dom = installDOM(); t.after(() => dom.restore());
  return mountInDocument(t, handler, opts);
}
function futureRuntimeResponse(overrides = {}) {
  const response = providerRuntimeResponse([{
    id: "future-cli", name: "Future CLI", kind: "future-cli", enabled: true, system: false,
    connection_mode: "account",
    credential_configured: false, credential_unreadable: false,
    accounts: [{ id: "future-a1", label: "Future account", enabled: true }],
    models: [{ model_id: "future-model", name: "Future Model", source: "manual", available: true }],
    ...overrides,
  }], { hasConnectedProvider: true });
  response.provider_options.splice(1, 0, {
    kind: "future-cli", display_name: "Future CLI",
    description: "Runtime thử nghiệm do server cung cấp", group: "subscription",
    connectable: true, connection_mode: "account", execution_mode: "local", visible: true,
    prefix: "fu", theme_color: "#123abc", beta: true, ui_order: 15,
  });
  return response;
}
const runtimeOnly = (providers) => (path, options = {}) => {
  if (path === "/llm/providers" && !options.method) return providerRuntimeResponse(providers);
  throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
};
test("a synthetic runtime renders, searches, details, badges, generic mark, and Connect from metadata", async (t) => {
  const response = futureRuntimeResponse();
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return response;
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  assert.ok(cards(main).some((entry) => cardName(entry) === "Future CLI"));
  const search = find(main, (node) => node.getAttribute?.("type") === "search");
  search.value = "future";
  search.dispatchEvent({ type: "input" });
  assert.deepEqual(cards(main).map(cardName), ["Future CLI"]);
  cards(main)[0].click();
  const detail = find(main, (node) => hasClass(node, "pv-detail"));
  assert.match(text(detail), /Future Model/);
  assert.match(text(detail), /fu\/future-model/);
  assert.match(text(find(detail, (node) => hasClass(node, "pv-execution-badge"))), /cục bộ|local/i);
  assert.match(text(find(detail, (node) => hasClass(node, "pv-connection-mode"))), /tài khoản/i);
  const mark = find(detail, (node) => node.hasAttribute?.("data-provider-mark"));
  assert.equal(mark?.getAttribute("data-provider-mark"), "fu");
  const connect = find(detail, (node) => node.tagName === "BUTTON" && /Thêm account|Thêm kết nối/.test(text(node)));
  assert.equal(connect?.disabled, false, "connectability comes from the option");
  connect.click();
  assert.ok(find(main, (node) => hasClass(node, "pv-connect-prompt")));
});
test("a persisted hidden runtime remains inspectable but its switch and Connect stay locked", async (t) => {
  const response = providerRuntimeResponse([{
    id: "gemini-cli", name: "Gemini CLI", kind: "gemini-cli", enabled: true, system: false,
    credential_configured: true, credential_unreadable: false, accounts: [{ id: "hidden-account", label: "Hidden", enabled: true }],
    models: [{ model_id: "gemini-2.5-pro", name: "Gemini 2.5 Pro", source: "manual", available: true }],
  }]);
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return response;
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const hiddenCard = cards(main).find((entry) => cardName(entry) === "Gemini CLI");
  assert.ok(hiddenCard, "a persisted hidden runtime remains reachable for inspection");
  assert.equal(find(main, (node) => hasClass(node, "pv-gallery")).contains(hiddenCard), false,
    "persisted hidden runtime stays outside the selectable Provider gallery");
  hiddenCard.click();
  const control = enabledSwitch(main);
  assert.ok(control);
  assert.equal(control.disabled, true);
  assert.equal(control.checked, true);
  assert.equal(control.getAttribute("aria-checked"), "true");
  assert.match(text(find(main, (node) => hasClass(node, "pv-enabled-explanation"))), /ẩn|chỉ đọc/iu);
  assert.match(text(hiddenCard), /ẩn|chỉ đọc/iu); assert.doesNotMatch(text(hiddenCard), /Đã thêm|Đã kết nối|Sẵn sàng/iu);
  assert.match(text(main), /gemini-2\.5-pro/u);
  assert.equal(find(main, (node) => node.tagName === "BUTTON" && /Thêm kết nối|Thêm account/u.test(text(node))), null,
    "hidden runtime detail has no Connect affordance");
});
test("a visible system Provider keeps a locked switch with a safe explanation", async (t) => {
  const { calls, main } = mountPage(t, runtimeOnly([CLAUDE_ADDED])); await flush();
  cards(main).find((card) => cardName(card) === "Claude Code").click();
  const control = enabledSwitch(main);
  assert.deepEqual([control.checked, control.getAttribute("aria-checked"), control.disabled], [true, "true", true]);
  assert.match(text(find(main, (node) => hasClass(node, "pv-enabled-explanation"))), /hệ thống|chỉ đọc/iu);
  control.click(); await flush(); assert.equal(providerUpdates(calls).length, 0);
});
test("a visible option without a persisted Provider cannot mutate availability", async (t) => {
  const { calls, main } = mountPage(t, runtimeOnly([])); await flush();
  cards(main).find((card) => cardName(card) === "Codex").click();
  const control = enabledSwitch(main);
  assert.deepEqual([control.checked, control.disabled], [false, true]);
  assert.match(text(find(main, (node) => hasClass(node, "pv-enabled-explanation"))), /chưa có|kết nối/iu);
  control.click(); await flush(); assert.equal(providerUpdates(calls).length, 0);
});
test("editable ON updates the exact server-driven Provider once and preserves name and credential semantics", async (t) => {
  let enabled = false;
  let listCalls = 0;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      return futureRuntimeResponse({ id: "future-provider-42", name: "Nhãn Future", enabled });
    }
    if (path === "/llm/providers/future-provider-42" && options.method === "PUT") {
      enabled = true;
      return { provider: { id: "future-provider-42", enabled: true } };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((entry) => cardName(entry) === "Future CLI").click();
  const control = enabledSwitch(main);
  assert.equal(control.checked, false);
  assert.equal(control.getAttribute("aria-checked"), "false");
  changeEnabled(control, true);
  await settle();
  const mutations = calls.filter((call) => call.options.method === "PUT");
  assert.deepEqual(mutations, [{
    path: "/llm/providers/future-provider-42",
    options: { method: "PUT", body: { name: "Nhãn Future", enabled: true, credential: "" } },
  }]);
  assert.equal(listCalls, 2, "the successful mutation is reconciled through one authoritative list read");
  assert.equal(enabledSwitch(main).checked, true);
  assert.equal(enabledSwitch(main).getAttribute("aria-checked"), "true");
  assert.equal(offDialog(), null, "ON is direct and never opens the destructive confirmation");
});
test("editable OFF keeps ON until confirm; Hủy, Escape, success, and focus use the shared modal", async (t) => {
  let enabled = true, listCalls = 0; const updateGate = deferred();
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      const response = futureRuntimeResponse({ id: "future-provider-42", enabled, ...(!enabled ? { accounts: [], models: [{ model_id: "new-model", name: "New Model", source: "manual", available: true }] } : {}) });
      if (!enabled) response.provider_options.find((entry) => entry.kind === "future-cli").display_name = "Future CLI updated";
      return response;
    }
    if (path === "/llm/providers/future-provider-42" && options.method === "PUT") {
      return updateGate.promise;
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush(); cards(main).find((entry) => cardName(entry) === "Future CLI").click();
  const control = enabledSwitch(main);
  control.focus();
  changeEnabled(control, false);
  assert.equal(control.checked, true, "OFF is rolled back visually before confirmation");
  assert.equal(control.getAttribute("aria-checked"), "true");
  assert.equal(calls.filter((call) => call.options.method === "PUT").length, 0);
  assert.equal(findAll(document.body, (node) => node.tagName === "DIALOG").length, 1);
  assert.equal(document.activeModalDialog, offDialog());
  assert.equal(offDialog().getAttribute("role"), "alertdialog");
  assert.equal(offDialog().getAttribute("aria-modal"), "true");
  assert.equal(document.activeElement, dialogButton("Hủy"));
  dialogButton("Hủy").click();
  assert.equal(offDialog(), null);
  assert.equal(document.activeElement, control);
  changeEnabled(control, false);
  assert.equal(offDialog().cancel(), false);
  assert.equal(offDialog(), null);
  assert.equal(document.activeElement, control);
  assert.equal(calls.filter((call) => call.options.method === "PUT").length, 0);
  changeEnabled(control, false);
  const modal = offDialog();
  dialogButton("Vẫn tắt").click();
  await flush();
  assert.equal(calls.filter((call) => call.options.method === "PUT").length, 1);
  assert.deepEqual(providerUpdates(calls), [expectedUpdate("future-provider-42", "Future CLI", false)]);
  assert.equal(enabledSwitch(main).getAttribute("aria-checked"), "true");
  assert.equal(offDialog(), modal);
  assert.ok(findAll(modal, (node) => node.tagName === "BUTTON").every((node) => node.disabled));
  enabled = false;
  updateGate.resolve({ provider: { id: "future-provider-42", enabled: false } });
  await settle();
  assert.equal(listCalls, 2);
  assert.equal(offDialog(), null);
  const liveControl = enabledSwitch(main);
  assert.deepEqual([liveControl.checked, liveControl.getAttribute("aria-checked"), liveControl.getAttribute("aria-label")], [false, "false", "Bật hoặc tắt Future CLI updated"]);
  assert.deepEqual([liveControl === control || !control.isConnected, providerUpdates(calls).length], [true, 1]); assert.equal(document.activeElement, liveControl,
    "successful same-ID detail repaint restores focus to its connected live switch");
});
test("OFF waits for the highest issued causal read before closing or offering Retry", async (t) => {
  for (const mode of ["latest read proves OFF", "latest read fails", "target replaced before latest failure", "target hidden before cancel", "target hidden before Escape"]) await t.test(mode, async (t) => {
    const rejectLatest = mode !== "latest read proves OFF", replacement = mode.startsWith("target replaced"), cancelHidden = mode.startsWith("target hidden");
    const dom = installDOM(); t.after(() => dom.restore());
    const aPut = deferred(), bPut = deferred(), cPut = deferred(), ownerRead = deferred(), middleRead = deferred(), latestRead = deferred();
    let aGets = 0, bEnabled = false, cEnabled = false;
    const triple = (a, b, c, replace = false, hide = false) => { const response = providerRuntimeResponse([
      { id: replace ? "provider-a-replacement" : "provider-a", name: "A saved", kind: "openai", enabled: a, system: false, connection_mode: hide ? "none" : "credential", credential_configured: true, credential_unreadable: false },
      { id: "provider-b", name: "B saved", kind: "anthropic", enabled: b, system: false, credential_configured: true, credential_unreadable: false },
      { id: "provider-c", name: "C saved", kind: "gemini", enabled: c, system: false, credential_configured: true, credential_unreadable: false },
      ]);
      if (hide) Object.assign(response.provider_options.find(({ kind }) => kind === "openai"), { visible: false, connectable: false, connection_mode: "none" });
      return response; };
    const aMount = mountInDocument(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method) {
        const call = ++aGets; return call === 1 ? triple(true, false, false)
          : call === 2 ? ownerRead.promise : call === 3 ? middleRead.promise : latestRead.promise;
      }
      if (path === "/llm/providers/provider-a" && options.method === "PUT") return aPut.promise;
      throw new Error(`Unexpected A call: ${options.method || "GET"} ${path}`);
    });
    const peer = (id, gate) => mountInDocument(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method) return triple(false, bEnabled, cEnabled);
      if (path === `/llm/providers/${id}` && options.method === "PUT") return gate.promise;
      throw new Error(`Unexpected peer call: ${options.method || "GET"} ${path}`);
    });
    const bMount = peer("provider-b", bPut), cMount = peer("provider-c", cPut); await settle();
    cards(aMount.main).find((card) => cardName(card) === "OpenAI").click(); cards(bMount.main).find((card) => cardName(card) === "Anthropic").click(); cards(cMount.main).find((card) => cardName(card) === "Google Gemini").click();
    const opener = enabledSwitch(aMount.main); changeEnabled(opener, false); const modal = offDialog();
    if (cancelHidden) {
      changeEnabled(enabledSwitch(bMount.main), true); await settle(); bEnabled = true; bPut.resolve({ ok: true }); while (aGets < 2) await flush();
      ownerRead.resolve(triple(true, true, false, false, true)); await settle(); const live = enabledSwitch(aMount.main), back = find(aMount.main, (node) => hasClass(node, "pv-back")); assert.equal(live.disabled, true); if (mode.endsWith("Escape")) offDialog().cancel(); else dialogButton("Hủy").click();
      assert.deepEqual([offDialog(), providerUpdates(aMount.calls).length], [null, 0]); assert.equal(document.activeElement, back, "cancel restores focus to a connected safe target after a structural handoff"); return;
    }
    dialogButton("Vẫn tắt").click();
    await settle(); aPut.resolve({ ok: true }); while (aGets < 2) await flush();
    changeEnabled(enabledSwitch(bMount.main), true); await settle(); bEnabled = true; bPut.resolve({ ok: true }); while (aGets < 3) await flush();
    if (rejectLatest) { middleRead.resolve(triple(false, true, false, replacement)); await settle(); }
    else { ownerRead.resolve(triple(false, false, false)); await settle(); }
    assert.equal(offDialog(), modal, "an older desired read cannot resolve the dialog");
    changeEnabled(enabledSwitch(cMount.main), true); await settle(); cEnabled = true; cPut.resolve({ ok: true }); while (aGets < 4) await flush();
    if (rejectLatest) { ownerRead.resolve(triple(false, false, false)); await settle(); latestRead.reject(new Error("private latest reconciliation failure")); }
    else latestRead.resolve(triple(false, true, true));
    await settle();
    if (!rejectLatest) {
      assert.equal(offDialog(), modal, "a read issued while waiting cannot bypass an intermediate read"); assert.deepEqual([dialogButton("Thử lại").hidden, dialogButton("Vẫn tắt").disabled], [true, true]);
      middleRead.resolve(triple(false, true, false)); await settle();
    }
    assert.equal(providerUpdates(aMount.calls).length, 1); assert.equal(aGets, 4); assert.deepEqual([bEnabled, cEnabled], [true, true]);
    if (replacement) {
      assert.equal(offDialog(), null); assert.equal(opener.isConnected, false); assert.equal(find(aMount.main, (node) => hasClass(node, "pv-detail")), null); assert.doesNotMatch(text(aMount.main), /private latest reconciliation failure|Thử lại/iu); assert.equal(document.activeElement, find(aMount.main, (node) => hasClass(node, "pv-search-input")));
    } else if (rejectLatest) {
      assert.equal(opener.isConnected, true);
      assert.equal(offDialog(), modal); assert.equal(dialogButton("Thử lại").hidden, false);
      assert.equal(enabledSwitch(aMount.main), opener); assert.equal(opener.checked, false);
      assert.equal(document.activeElement, dialogButton("Thử lại")); assert.doesNotMatch(`${text(aMount.main)} ${text(modal)}`, /private latest reconciliation failure/iu);
    } else {
      assert.equal(offDialog(), null); assert.equal(enabledSwitch(aMount.main), opener);
      assert.equal(opener.checked, false); assert.equal(document.activeElement, opener);
    }
  });
});
test("an exact rejected OFF reconciles one GET and keeps authoritative ON", async (t) => {
  let listCalls = 0;
  const rejected = Object.assign(new Error("private provider token"), { status: 422 });
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      return futureRuntimeResponse({ id: "future-provider-42", enabled: true });
    }
    if (path === "/llm/providers/future-provider-42" && options.method === "PUT") throw rejected;
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((entry) => cardName(entry) === "Future CLI").click();
  changeEnabled(enabledSwitch(main), false);
  dialogButton("Vẫn tắt").click();
  await settle();
  assert.equal(calls.filter((call) => call.options.method === "PUT").length, 1);
  assert.deepEqual(providerUpdates(calls), [expectedUpdate("future-provider-42", "Future CLI", false)]);
  assert.equal(listCalls, 2, "every attempted PUT has exactly one authoritative reconciliation read");
  assert.equal(enabledSwitch(main).checked, true);
  assert.equal(enabledSwitch(main).getAttribute("aria-checked"), "true");
  assert.ok(offDialog(), "the same modal remains available for Retry or Hủy");
  assert.doesNotMatch(text(offDialog()), /private provider token/u);
  dialogButton("Hủy").click();
});
test("a rejected ON response is accepted only when one GET proves the exact desired state", async (t) => {
  let enabled = false;
  let listCalls = 0;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      return futureRuntimeResponse({ id: "future-provider-42", enabled });
    }
    if (path === "/llm/providers/future-provider-42" && options.method === "PUT") {
      enabled = true;
      throw Object.assign(new Error("lost response after commit"), { status: 503 });
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((entry) => cardName(entry) === "Future CLI").click();
  changeEnabled(enabledSwitch(main), true);
  await settle();
  assert.equal(calls.filter((call) => call.options.method === "PUT").length, 1,
    "an ambiguous PUT is never automatically replayed");
  assert.deepEqual(providerUpdates(calls), [expectedUpdate("future-provider-42", "Future CLI", true)]);
  assert.equal(listCalls, 2);
  assert.equal(enabledSwitch(main).checked, true);
  assert.equal(enabledSwitch(main).getAttribute("aria-checked"), "true");
  await settle();
  assert.equal(providerUpdates(calls).length, 1, "reconciliation never auto-replays the mutation");
});
for (const scenario of [
  { name: "accepted state", enabled: true, reject: false },
  { name: "failed state", enabled: false, reject: true },
]) {
  test(`pending ON survives an overlapping stale refresh for ${scenario.name}`, async (t) => {
    let listCalls = 0;
    const updateGate = deferred();
    const staleRefresh = deferred();
    const { calls, main } = mountPage(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method) {
        listCalls++;
        if (listCalls === 1) return futureRuntimeResponse({ id: "future-provider-42", enabled: false });
        if (listCalls === 2) return staleRefresh.promise;
        return futureRuntimeResponse({ id: "future-provider-42", enabled: scenario.enabled });
      }
      if (path === "/llm/providers/future-provider-42" && options.method === "PUT") return updateGate.promise;
      throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
    });
    await flush();
    cards(main).find((entry) => cardName(entry) === "Future CLI").click();
    const original = enabledSwitch(main);
    changeEnabled(original, true);
    await flush();
    find(main, (node) => node.tagName === "BUTTON" && /Về Providers/u.test(text(node))).click();
    find(main, (node) => node.tagName === "BUTTON" && /Tải lại/u.test(text(node))).click();
    await flush();
    if (scenario.reject) {
      staleRefresh.resolve(futureRuntimeResponse({ id: "future-provider-42", enabled: false }));
      await settle();
      cards(main).find((entry) => cardName(entry) === "Future CLI").click();
      updateGate.reject(new Error("private stale navigation response"));
    } else {
      updateGate.resolve({ provider: { id: "future-provider-42", enabled: true } });
      await settle();
      staleRefresh.resolve(futureRuntimeResponse({ id: "future-provider-42", enabled: false }));
    }
    await settle();
    if (!scenario.reject) cards(main).find((entry) => cardName(entry) === "Future CLI").click();
    const live = enabledSwitch(main);
    assert.deepEqual(providerUpdates(calls), [expectedUpdate("future-provider-42", "Future CLI", true)]);
    assert.equal(listCalls, 3, "initial, overlapping refresh, and mandatory reconcile are the only GETs");
    assert.equal(original.isConnected, false);
    assert.equal(enabledSwitch(main), live);
    assert.equal(live.isConnected, true);
    assert.equal(live.checked, scenario.enabled);
    assert.equal(live.getAttribute("aria-checked"), String(scenario.enabled));
    assert.equal(live.disabled, false, "the current switch is released after reconciliation");
    assert.equal(offDialog(), null);
    if (scenario.enabled) assert.equal(enabledError(main).hidden, true);
    else assert.match(text(enabledError(main)), /Chưa thể|giữ nguyên/iu);
    assert.doesNotMatch(text(main), /private stale navigation response/iu);
  });
}
const OFF_RECONCILIATION_FAILURES = [
  {
    name: "resolved PUT with an old authoritative snapshot",
    reconcile: () => futureRuntimeResponse({ id: "future-provider-42", enabled: true }),
  },
  { name: "same-kind replacement removes the stale target", replacement: true,
    reconcile: () => futureRuntimeResponse({ id: "future-provider-replacement", enabled: false }) },
  {
    name: "malformed authoritative list",
    reconcile: () => ({ providers: [], provider_options: [] }),
  },
  {
    name: "reconciliation GET rejection",
    reconcile: () => { throw new Error("private reconciliation detail"); },
  },
];
for (const scenario of OFF_RECONCILIATION_FAILURES) {
  test(`OFF ${scenario.name} ${scenario.replacement ? "ends safely" : "retains the prior switch and same safe modal"}`, async (t) => {
    let listCalls = 0;
    const { calls, main } = mountPage(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method) {
        listCalls++;
        if (listCalls === 1) return futureRuntimeResponse({ id: "future-provider-42", enabled: true });
        return scenario.reconcile();
      }
      if (path === "/llm/providers/future-provider-42" && options.method === "PUT") {
        return { provider: { id: "future-provider-42", enabled: false } };
      }
      throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
    });
    await flush();
    cards(main).find((entry) => cardName(entry) === "Future CLI").click();
    const control = enabledSwitch(main);
    changeEnabled(control, false);
    const modal = offDialog();
    dialogButton("Vẫn tắt").click();
    await settle();
    assert.deepEqual(providerUpdates(calls), [expectedUpdate("future-provider-42", "Future CLI", false)]);
    assert.equal(listCalls, 2, "one attempted PUT has exactly one reconciliation GET after the initial list");
    if (scenario.replacement) {
      assert.equal(control.isConnected, false);
      assert.equal(find(main, (node) => hasClass(node, "pv-detail")), null);
      assert.equal(offDialog(), null, "a vanished exact target cannot offer stale Retry");
      assert.equal(providerUpdates(calls).length, 1);
      return;
    }
    assert.equal(control.isConnected, true, "failure keeps the original detail control attached");
    assert.equal(enabledSwitch(main), control, "failure does not replace the authoritative switch node");
    assert.equal(control.checked, true);
    assert.equal(control.getAttribute("aria-checked"), "true");
    assert.ok(find(main, (node) => hasClass(node, "pv-detail")), "the Provider detail remains intact");
    assert.equal(offDialog(), modal);
    assert.equal(dialogButton("Thử lại").hidden, false);
    assert.match(text(modal), /Chưa thể tắt provider/u);
    assert.doesNotMatch(`${text(main)} ${text(modal)}`,
      /private reconciliation detail|future-provider-replacement/iu);
    await settle();
    assert.equal(providerUpdates(calls).length, 1, "failure does not replay PUT before explicit Retry");
    assert.equal(listCalls, 2, "failure does not start another GET loop");
    dialogButton("Hủy").click();
    assert.equal(document.activeElement, control, "Hủy restores focus to the exact live switch");
  });
}
const ON_RECONCILIATION_FAILURES = [
  {
    name: "old exact-id state",
    renamed: "Tên Provider mới từ máy chủ",
    reconcile: () => futureRuntimeResponse({
      id: "future-provider-42", name: "Tên Provider mới từ máy chủ", enabled: false,
    }),
  },
  {
    name: "same-kind desired state under another id",
    replacement: true,
    reconcile: () => futureRuntimeResponse({ id: "future-provider-replacement", enabled: true }),
  },
  {
    name: "malformed list",
    reconcile: () => ({ providers: [], provider_options: [], debug: "private malformed list detail" }),
  },
  {
    name: "GET error",
    reconcile: () => { throw new Error("private direct reconciliation detail"); },
  },
];
for (const scenario of ON_RECONCILIATION_FAILURES) {
  test(`direct ON ${scenario.name} adopts the exact authoritative snapshot`, async (t) => {
    let listCalls = 0;
    const { calls, main } = mountPage(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method) {
        listCalls++;
        if (listCalls === 1) return futureRuntimeResponse({ id: "future-provider-42", enabled: false });
        return listCalls === 3 && scenario.renamed ? futureRuntimeResponse({ id: "future-provider-42", name: scenario.renamed, enabled: true }) : scenario.reconcile();
      }
      if (path === "/llm/providers/future-provider-42" && options.method === "PUT") {
        return { provider: { id: "future-provider-42", enabled: true } };
      }
      throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
    });
    await flush();
    cards(main).find((entry) => cardName(entry) === "Future CLI").click();
    const control = enabledSwitch(main);
    control.focus();
    changeEnabled(control, true);
    await settle();
    assert.deepEqual(providerUpdates(calls), [expectedUpdate("future-provider-42", "Future CLI", true)]);
    assert.equal(listCalls, 2, "one direct PUT has exactly one reconciliation GET after initial load");
    const repainted = Boolean(scenario.replacement), live = enabledSwitch(main);
    assert.equal(control.isConnected, !repainted);
    assert.equal(live, scenario.replacement ? null : repainted ? enabledSwitch(main) : control);
    assert.equal(find(main, (node) => hasClass(node, "pv-detail")) === null, Boolean(scenario.replacement));
    if (!scenario.replacement) {
      assert.equal(live.checked, false);
      assert.equal(live.getAttribute("aria-checked"), "false");
      assert.equal(enabledError(main).hidden, false); assert.match(text(enabledError(main)), /Chưa thể|giữ nguyên/iu);
    }
    assert.doesNotMatch(text(main),
      /private direct reconciliation detail|private malformed list detail|future-provider-replacement/iu);
    assert.equal(offDialog(), null);
    await settle();
    assert.equal(providerUpdates(calls).length, 1, "direct reconciliation never auto-replays PUT");
    assert.equal(listCalls, 2);
    if (scenario.renamed) {
      let peerEnabled = false; const peer = mountInDocument(t, (path, options = {}) => {
        if (path === "/llm/providers" && !options.method) return futureRuntimeResponse({ id: "future-provider-42", name: scenario.renamed, enabled: peerEnabled }); if (path === "/llm/providers/future-provider-42" && options.method === "PUT") { peerEnabled = true; return { ok: true }; } throw new Error(`Unexpected peer call: ${options.method || "GET"} ${path}`);
      }); await settle(); cards(peer.main).find((entry) => cardName(entry) === "Future CLI").click(); changeEnabled(enabledSwitch(peer.main), true);
      while (listCalls < 3) await flush(); for (let tick = 0; tick < 8; tick++) await flush(); assert.deepEqual(providerUpdates(peer.calls), [expectedUpdate("future-provider-42", scenario.renamed, true)]);
      const feedback = enabledError(main); assert.deepEqual([live.checked, live.getAttribute("aria-checked"), feedback.hidden, text(feedback), listCalls], [true, "true", true, "", 3]);
    }
  });
}
test("rejected OFF closes only when one GET proves the exact desired Provider state", async (t) => {
  let enabled = true;
  let listCalls = 0;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      return futureRuntimeResponse({ id: "future-provider-42", enabled });
    }
    if (path === "/llm/providers/future-provider-42" && options.method === "PUT") {
      enabled = false;
      throw Object.assign(new Error("private OFF response after commit"), { status: 503 });
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((entry) => cardName(entry) === "Future CLI").click();
  const control = enabledSwitch(main);
  control.focus();
  changeEnabled(control, false);
  dialogButton("Vẫn tắt").click();
  await settle();
  assert.deepEqual(providerUpdates(calls), [expectedUpdate("future-provider-42", "Future CLI", false)]);
  assert.equal(listCalls, 2);
  assert.equal(offDialog(), null);
  assert.equal(control.isConnected, true);
  assert.equal(enabledSwitch(main), control);
  assert.equal(control.checked, false);
  assert.equal(control.getAttribute("aria-checked"), "false");
  assert.ok(find(main, (node) => hasClass(node, "pv-detail")));
  assert.equal(document.activeElement, control);
  assert.doesNotMatch(text(main), /private OFF response after commit/iu);
});
test("ambiguous OFF with old GET stays retryable; only explicit Retry sends the next mutation", async (t) => {
  let enabled = true;
  let attempts = 0;
  let listCalls = 0;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      return futureRuntimeResponse({ id: "future-provider-42", enabled });
    }
    if (path === "/llm/providers/future-provider-42" && options.method === "PUT") {
      attempts++;
      if (attempts === 1) throw new Error("lost response before commit");
      enabled = false;
      return { provider: { id: "future-provider-42", enabled: false } };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((entry) => cardName(entry) === "Future CLI").click();
  changeEnabled(enabledSwitch(main), false);
  const modal = offDialog();
  dialogButton("Vẫn tắt").click();
  await settle();
  assert.equal(attempts, 1);
  assert.equal(offDialog(), modal);
  assert.equal(enabledSwitch(main).checked, true);
  assert.equal(enabledSwitch(main).getAttribute("aria-checked"), "true");
  assert.equal(dialogButton("Thử lại").hidden, false);
  assert.equal(listCalls, 2);
  assert.deepEqual(providerUpdates(calls), [expectedUpdate("future-provider-42", "Future CLI", false)]);
  dialogButton("Thử lại").click();
  await settle();
  assert.equal(attempts, 2, "one explicit Retry authorizes exactly one further PUT");
  assert.deepEqual(providerUpdates(calls), [
    expectedUpdate("future-provider-42", "Future CLI", false),
    expectedUpdate("future-provider-42", "Future CLI", false),
  ]);
  assert.equal(listCalls, 3, "two attempted PUTs have two reconciliation GETs plus the initial list");
  assert.equal(offDialog(), null);
  assert.equal(enabledSwitch(main).checked, false);
  assert.equal(enabledSwitch(main).getAttribute("aria-checked"), "false");
});
test("Providers imports the shared OFF helper and carries no second modal implementation", async () => {
  const source = await readFile(new URL("pages/providers.js", staticRoot), "utf8");
  assert.equal((source.match(/from\s+"\.\.\/components\/provider-off-dialog\.js"/gu) ?? []).length, 1,
    "Providers imports the shared helper module exactly once");
  assert.equal((source.match(/\bcreateProviderOffDialog\s*\(/gu) ?? []).length, 1,
    "one page mount owns one shared helper controller");
  assert.doesNotMatch(source,
    /showModal\s*\(|window\.confirm|createElement\s*\(\s*["']dialog|element\s*\(\s*["']dialog|<dialog|role:\s*"alertdialog"|provider-off-dialog-(?:actions|button|status|error)/iu);
  assert.doesNotMatch(source, /opencode/iu, "management stays registry-driven without a literal runtime exception");
  const productionPaths = [
    "components/provider-off-dialog.js",
    "pages/onboarding.js",
    "pages/onboarding-early-view.js",
    "pages/providers.js",
  ];
  const production = (await Promise.all(productionPaths
    .map((path) => readFile(new URL(path, staticRoot), "utf8")))).join("\n");
  assert.equal((production.match(/showModal\s*\(/gu) ?? []).length, 1);
  assert.equal((production.match(/role:\s*"alertdialog"/gu) ?? []).length, 1);
});
test("malformed runtime metadata renders only a generic catalog error", async (t) => {
  const response = futureRuntimeResponse();
  response.provider_options[1].execution_mode = "private-command --token secret-value";
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return response;
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const banner = find(main, (node) => hasClass(node, "banner"));
  assert.match(text(banner), /danh mục Provider/i);
  assert.equal(text(banner).includes("private-command"), false);
  assert.equal(text(banner).includes("secret-value"), false);
  assert.equal(cards(main).length, 0);
});
test("phaseProgress maps each phase to an honest anchor", () => {
  assert.equal(phaseProgress("detecting"), 8);
  assert.equal(phaseProgress("installing"), 12);
  assert.ok(phaseProgress("installing") < INSTALL_CEILING, "installing starts below the crawl ceiling");
  assert.ok(INSTALL_CEILING < 100, "the install ceiling is below 100 — never a fake full bar");
  assert.equal(phaseProgress("awaiting_login"), 90);
  assert.equal(phaseProgress("polling"), 90);
  assert.equal(phaseProgress("connected"), 100);
  assert.equal(phaseProgress("prompt"), null);
  assert.equal(phaseProgress("error"), null);
  assert.equal(phaseProgress("canceled"), null);
});
test("installing renders a progress bar anchored below the ceiling, with the live npm line", async (t) => {
  const main = await openConnectAt(t, "codex", "Codex", { phase: "installing", message: "npm: added 90 packages" });
  assert.ok(progressBar(main), "installing shows a progress bar");
  const pct = fillPct(main);
  assert.ok(pct >= 12 && pct < INSTALL_CEILING, `installing % ${pct} must sit in [12, ${INSTALL_CEILING})`);
  assert.ok(pct < 100, "installing is never 100%");
  assert.equal(find(main, (n) => hasClass(n, "pv-progress--error")), null, "installing bar is not the error style");
  const log = find(main, (n) => hasClass(n, "pv-connect-log"));
  assert.ok(log && /added 90 packages/.test(text(log)), "the live npm line renders under the bar");
});
test("polling jumps the bar to its ~90% anchor, still below 100", async (t) => {
  const main = await openConnectAt(t, "codex", "Codex", { phase: "polling", loginUrl: "https://auth.example/x" });
  assert.ok(progressBar(main), "polling shows a progress bar");
  const pct = fillPct(main);
  assert.equal(pct, 90);
  assert.ok(pct < 100, "polling is never 100%");
});
test("an errored connect freezes the bar and styles it red", async (t) => {
  const main = await openConnectAt(t, "codex", "Codex", { phase: "error", message: "Kết nối thất bại" });
  const bar = progressBar(main);
  assert.ok(bar, "the error panel still shows the (frozen) bar");
  assert.ok(hasClass(bar, "pv-progress--error"), "the frozen bar carries the error style");
  assert.ok(fillPct(main) < 100, "a failed flow never shows 100%");
});
test("Test all runs the saved-provider test and refreshes status", async (t) => {
  let checked = false;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method)
      return providerRuntimeResponse([{ ...CLAUDE_ADDED, last_check_status: checked ? "ok" : "" }]);
    if (path === "/llm/providers/claude-code/test" && options.method === "POST") { checked = true; return { ok: true }; }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const testAll = find(main, (n) => n.tagName === "BUTTON" && /Kiểm tra tất cả/.test(text(n)));
  testAll.click();
  await flush();
  assert.ok(calls.some((c) => c.path === "/llm/providers/claude-code/test"));
});
test("a failed provider list renders the shared error panel, never a blank page", async (t) => {
  const { main } = mountPage(t, () => { throw new Error("không đọc được danh sách Provider"); });
  await flush();
  assert.match(text(find(main, (n) => hasClass(n, "banner"))), /không đọc được danh sách Provider/);
});
const CANARY_KEY = "sk-package-must-never-contain-7f36d2";
test("provider service issues the documented paths and methods", async () => {
  const calls = [];
  const request = async (path, options = {}) => {
    calls.push({ path, options });
    return null;
  };
  const service = createProviderService(request);
  await service.list();
  await service.create({ kind: "openai", name: "Chính", enabled: true, credential: CANARY_KEY });
  await service.update("openai-1", { name: "Đổi tên", enabled: false, credential: "" });
  await service.remove("openai-1");
  await service.replaceCredential("openai-1", CANARY_KEY);
  await service.clearCredential("openai-1");
  await service.testDraft({ kind: "openai", credential: CANARY_KEY });
  await service.testSaved("openai-1");
  await service.discover("openai-1");
  await service.addModel("openai-1", "gpt-5");
  await service.removeModel("openai-1", "gpt 5/preview");
  await service.removeAccount("codex", "a1");
  assert.deepEqual(calls, [
    { path: "/llm/providers", options: {} },
    {
      path: "/llm/providers",
      options: {
        method: "POST",
        body: { kind: "openai", name: "Chính", enabled: true, credential: CANARY_KEY },
      },
    },
    {
      path: "/llm/providers/openai-1",
      options: { method: "PUT", body: { name: "Đổi tên", enabled: false, credential: "" } },
    },
    { path: "/llm/providers/openai-1", options: { method: "DELETE" } },
    {
      path: "/llm/providers/openai-1/credential",
      options: { method: "PUT", body: { credential: CANARY_KEY } },
    },
    { path: "/llm/providers/openai-1/credential", options: { method: "DELETE" } },
    {
      path: "/llm/providers/test",
      options: { method: "POST", body: { kind: "openai", credential: CANARY_KEY, model: "" } },
    },
    { path: "/llm/providers/openai-1/test", options: { method: "POST" } },
    { path: "/llm/providers/openai-1/discover", options: { method: "POST" } },
    {
      path: "/llm/providers/openai-1/models",
      options: { method: "POST", body: { model_id: "gpt-5", name: "" } },
    },
    {
      path: "/llm/providers/openai-1/models?model_id=gpt%205%2Fpreview",
      options: { method: "DELETE" },
    },
    { path: "/llm/providers/codex/accounts/a1", options: { method: "DELETE" } },
  ]);
});
test("Providers page carries no static runtime catalog, connect allowlist, or proxy allowlist", async () => {
  const source = await readFile(new URL("pages/providers.js", staticRoot), "utf8");
  for (const legacy of ["PROVIDER_CATALOG", "CONNECTABLE_KINDS", "PROXY_KINDS"]) {
    assert.equal(source.includes(legacy), false, `${legacy} must come from runtime metadata`);
  }
});
test("provider marks center generic prefixes and hide fallback text behind every branded SVG", async () => {
  const css = await readFile(new URL("portal.css", staticRoot), "utf8");
  const generic = css.match(/\.providers-page \.pv-logo\s*\{([^}]*)\}/)?.[1] ?? "";
  for (const declaration of [
    /display\s*:\s*flex/,
    /align-items\s*:\s*center/,
    /justify-content\s*:\s*center/,
    /color\s*:\s*#fff(?:fff)?\b/i,
    /font-weight\s*:\s*(?:[6-9]00|bold)/,
  ]) assert.match(generic, declaration);
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)];
  const logoKinds = (bodyPattern) => [...new Set(rules
    .filter(([, body]) => bodyPattern.test(body))
    .flatMap(([selectors]) => [...selectors.matchAll(/\.providers-page \.pv-logo-([a-z-]+)/g)]
      .map((match) => match[1])))].sort();
  assert.deepEqual(logoKinds(/color\s*:\s*transparent/), logoKinds(/background-image\s*:/));
});
const KEY_SHAPE = /\b(?:sk-|xai-|gsk_|AIza)[A-Za-z0-9_-]{8,}/g;
test("no embedded Portal asset carries an API-key literal", async () => {
  const names = (await readdir(staticRoot, { recursive: true })).filter((n) => /\.(js|html|css)$/.test(n));
  assert.ok(names.includes(join("pages", "providers.js")), `walk missed pages/providers.js: ${names}`);
  assert.match("sk-live-AbCdEfGh12345678", KEY_SHAPE);
  for (const name of names) {
    const source = await readFile(new URL(name, staticRoot), "utf8");
    const found = source.match(KEY_SHAPE) ?? [];
    assert.equal(found.length, 0, `${name} carries ${found.length} key-shaped literal(s)`);
  }
});
