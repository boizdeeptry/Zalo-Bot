import test from "node:test";
import assert from "node:assert/strict";
import { createProvidersPage } from "../overlay/internal/webui/static/pages/providers.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import {
  CLAUDE_ADDED,
  CODEX_CONNECTED,
  providerRuntimeResponse,
} from "./helpers/provider-runtime-fixtures.mjs";

const flush = () => new Promise((r) => setImmediate(r));
const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
};
async function waitFor(condition, description, timeoutMs = 1000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() <= deadline) {
    const result = condition();
    if (result) return result;
    await new Promise((r) => setTimeout(r, 10));
  }
  throw new Error(`Timed out waiting for ${description}`);
}
const hasClass = (n, c) => n.classList?.contains(c) ?? false;
const cards = (main) => findAll(main, (n) => hasClass(n, "pv-card"));
const cardName = (card) => text(find(card, (n) => hasClass(n, "pv-name")));
const enabledSwitch = (root) => find(root, (node) => node.getAttribute?.("role") === "switch"
  && node.hasAttribute?.("data-provider-enabled-switch"));
const changeEnabled = (control, enabled) => { control.checked = enabled; control.dispatchEvent({ type: "change" }); };
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
  const dom = installDOM();
  t.after(() => dom.restore());
  return mountInDocument(t, handler, opts);
}
const listWith = (providers) => (path, options = {}) => {
  if (path === "/llm/providers" && !options.method) return providerRuntimeResponse(providers);
  throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
};
const apiProvider = (id, kind, enabled = false) => ({
  id, kind, enabled, name: `${kind} saved`, system: false,
  credential_configured: true, credential_unreadable: false,
});
const pairResponse = (a, b) => providerRuntimeResponse([
  apiProvider("provider-a", "openai", a), apiProvider("provider-b", "anthropic", b),
]);
test("gallery renders the 6-provider catalogue in two groups", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  assert.deepEqual(cards(main).map(cardName),
    ["Codex", "Claude Code", "OpenAI", "Anthropic", "Google Gemini", "OpenRouter"]);
  const groups = findAll(main, (n) => hasClass(n, "pv-group"));
  assert.equal(groups.length, 2);
});
test("execution badges come from each runtime instead of a blanket subscription safety claim", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  assert.equal(find(main, (n) => hasClass(n, "pv-safe-tag")), null,
    "mixed subscription runtimes must not receive one blanket safety badge");
  cards(main).find((entry) => cardName(entry) === "Codex").click();
  assert.match(text(find(main, (n) => hasClass(n, "pv-risk-badge"))), /proxy/i);
  find(main, (n) => hasClass(n, "pv-back")).click();
  cards(main).find((entry) => cardName(entry) === "Claude Code").click();
  assert.match(text(find(main, (n) => hasClass(n, "pv-safe-badge"))), /CLI|chính chủ/i);
});
test("a system subscription provider with no connected account shows 'Chưa kết nối', never 'Sẵn sàng'", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  const claude = cards(main).find((c) => cardName(c) === "Claude Code");
  const codex = cards(main).find((c) => cardName(c) === "Codex");
  const claudeStatus = find(claude, (n) => hasClass(n, "pv-status"));
  assert.ok(hasClass(claudeStatus, "off"), "system claude-code with no account is not connected");
  assert.match(text(claudeStatus), /Chưa kết nối/);
  assert.ok(!text(claudeStatus).includes("Sẵn sàng"), "no misleading 'Sẵn sàng' label under no-default");
  assert.ok(hasClass(find(codex, (n) => hasClass(n, "pv-status")), "off"));
  assert.match(text(codex), /Chưa kết nối/);
});
test("a subscription provider with a connected account shows Đã kết nối, not credential-gated", async (t) => {
  const { main } = mountPage(t, listWith([CODEX_CONNECTED]));
  await flush();
  const codex = cards(main).find((c) => cardName(c) === "Codex");
  const status = find(codex, (n) => hasClass(n, "pv-status"));
  assert.ok(hasClass(status, "on"), "codex with an enabled account must read as connected");
  assert.match(text(status), /Đã kết nối/);
});
test("banner cảnh báo hiện khi 0 provider connected", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const banner = find(main, (n) => n.hasAttribute?.("data-no-provider-warning"));
  assert.ok(banner, "phải có banner khi 0 provider connected");
  assert.match(text(banner), /im lặng|chưa kết nối/i);
});
test("banner ẩn khi có provider connected", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return providerRuntimeResponse([CODEX_CONNECTED], { hasConnectedProvider: true });
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  assert.equal(find(main, (n) => n.hasAttribute?.("data-no-provider-warning")), null);
});
test("banner ẩn khi payload không có trường hasConnectedProvider (backend cũ)", async (t) => {
  const legacy = providerRuntimeResponse([CODEX_CONNECTED]);
  delete legacy.hasConnectedProvider;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return legacy;
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  assert.equal(find(main, (n) => n.hasAttribute?.("data-no-provider-warning")), null);
});
test("typing in the search box filters cards by name", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  const box = find(main, (n) => n.tagName === "INPUT" && n.getAttribute?.("type") === "search");
  assert.ok(box);
  box.value = "codex";
  box.dispatchEvent({ type: "input" });
  assert.deepEqual(cards(main).map(cardName), ["Codex"]);
});
test("clicking a card opens its detail; back returns to the gallery", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  assert.ok(detail);
  assert.equal(text(find(detail, (n) => hasClass(n, "pv-name"))), "Codex");
  const riskBadge = find(detail, (n) => hasClass(n, "pv-risk-badge"));
  assert.ok(riskBadge, "Codex (proxy) detail must show the risk badge, not a safety badge");
  assert.match(text(riskBadge), /rủi ro|khoá/i);
  assert.equal(find(detail, (n) => hasClass(n, "pv-safe-badge")), null, "no false safety badge on a proxy provider");
  find(detail, (n) => hasClass(n, "pv-back")).click();
  await flush();
  assert.ok(find(main, (n) => hasClass(n, "pv-gallery")));
  assert.equal(find(main, (n) => hasClass(n, "pv-detail")), null);
});
test("detail exposes the persisted Provider enabled state as an authoritative switch", async (t) => {
  const { main } = mountPage(t, listWith([CODEX_CONNECTED]));
  await flush();
  cards(main).find((card) => cardName(card) === "Codex").click();
  const control = enabledSwitch(main);
  assert.ok(control, "a persisted Provider detail has an enabled switch");
  assert.equal(control.getAttribute("type"), "checkbox");
  assert.equal(control.checked, true);
  assert.equal(control.getAttribute("aria-checked"), "true");
  assert.equal(control.disabled, false);
  assert.match(control.getAttribute("aria-label"), /Codex/u);
});
test("a reconciliation rebuilds changed detail data but preserves an unchanged Connect prompt", async (t) => {
  for (const mode of ["hidden", "stale error", "background error"]) await t.test(mode, async (t) => {
    const mutation = deferred(), stale = deferred(), fresh = deferred(), externalPut = deferred(),
      a = apiProvider("provider-a", "openai"), hidden = mode === "hidden";
    let listCalls = 0, connectPosts = 0;
    const runtime = (enabled, hide = false) => {
      const response = providerRuntimeResponse([{ ...a, enabled }, {
        ...CODEX_CONNECTED, connection_mode: hide ? "none" : "account",
      }]);
      if (hide) Object.assign(response.provider_options.find((entry) => entry.kind === "codex"),
        { visible: false, connectable: false, connection_mode: "none" });
      return response;
    };
    const { calls, main } = mountPage(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method) {
        const call = ++listCalls;
        if (!hidden && call > 1) return call === 2 ? stale.promise : fresh.promise;
        return runtime(call > 1, hidden && call > 1);
      }
      if (path === "/llm/providers/provider-a" && options.method === "PUT") return mutation.promise;
      if (path === "/llm/providers/codex/connect" && options.method === "POST") {
        connectPosts++; return { kind: "codex", phase: "error", message: "stale" };
      }
      throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
    });
    const external = hidden ? null : mountInDocument(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method) return pairResponse(false, false);
      if (path === "/llm/providers/provider-b" && options.method === "PUT") return externalPut.promise;
      throw new Error(`Unexpected external call: ${options.method || "GET"} ${path}`);
    });
    await flush(); cards(main).find((card) => cardName(card) === "OpenAI").click();
    changeEnabled(enabledSwitch(main), true);
    await waitFor(() => calls.some((call) => call.options.method === "PUT"), "A mutation dispatch");
    find(main, (node) => hasClass(node, "pv-back")).click(); cards(main)
      .find((card) => cardName(card) === "Codex").click();
    find(main, (node) => node.tagName === "BUTTON" && /Thêm account/u.test(text(node))).click();
    const prompt = find(main, (node) => hasClass(node, "pv-connect-prompt"));
    const input = find(prompt, (node) => node.tagName === "INPUT"),
      begin = find(prompt, (node) => node.tagName === "BUTTON" && /Bắt đầu/u.test(text(node)));
    input.focus();
    if (hidden) {
      mutation.resolve({ ok: true }); await waitFor(() => listCalls === 2, "A authoritative reconciliation");
    } else {
      cards(external.main).find((card) => cardName(card) === "Anthropic").click();
      changeEnabled(enabledSwitch(external.main), true);
      await waitFor(() => external.calls.some((call) => call.options.method === "PUT"), "external mutation");
      externalPut.resolve({ ok: true }); await waitFor(() => listCalls === 2, "epoch-one listener read");
      if (mode === "background error") {
        stale.reject(new Error("private current listener failure")); await flush();
        assert.equal(prompt.isConnected, true); assert.equal(document.activeElement, input);
        const feedback = find(main, (node) => node.getAttribute?.("aria-live") === "polite");
        assert.equal(feedback.isConnected, true);
        assert.match(text(feedback), /cập nhật|đối soát|thử lại/iu);
        assert.doesNotMatch(text(main), /private current listener failure/iu);
      }
      mutation.resolve({ ok: true }); await waitFor(() => listCalls === 3, "epoch-two owner read");
      if (mode === "stale error") {
        stale.reject(new Error("private stale listener failure")); await flush();
        assert.equal(prompt.isConnected, true); assert.equal(document.activeElement, input);
        assert.doesNotMatch(text(main), /private stale listener|Thử lại/iu);
      }
      fresh.resolve(runtime(true));
    }
    await flush();
    if (hidden) {
      assert.equal(prompt.isConnected, false); assert.equal(begin.isConnected, false);
      assert.match(text(main), /ẩn|chỉ đọc/iu);
      assert.equal(find(main, (node) => /Thêm account|Thêm kết nối/u.test(text(node))), null);
      begin.click(); await flush();
    } else {
      assert.deepEqual([find(main, (node) => hasClass(node, "pv-connect-prompt")),
        document.activeElement], [prompt, input]);
      assert.doesNotMatch(text(main), /private current listener|Thử lại/iu);
      assert.equal(text(find(main, (node) => node.getAttribute?.("aria-live") === "polite")), "");
    }
    assert.equal(connectPosts, 0, "a stale or merely observed prompt never dispatches Connect");
    assert.equal(calls.filter((call) => call.options.method === "PUT").length, 1);
  });
});
test("later mutation settlements dominate earlier snapshots in either response order", async (t) => {
  for (const [name, freshFirst, reject] of [["fresh resolves first", true],
    ["stale resolves first", false], ["stale rejects after fresh", true, true]]) await t.test(name, async (t) => {
    const dom = installDOM(); t.after(() => dom.restore());
    const aPut = deferred(), bPut = deferred(), stale = deferred(), fresh = deferred();
    let aGets = 0, bGets = 0;
    const mountProvider = (id, gate, read) => mountInDocument(t, (path, options = {}) => {
      if (path === "/llm/providers" && !options.method) return read();
      if (path === `/llm/providers/${id}` && options.method === "PUT") return gate.promise;
      throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
    });
    const aMount = mountProvider("provider-a", aPut, () => (++aGets === 1
      ? pairResponse(false, false) : aGets === 2 ? stale.promise : fresh.promise));
    const bMount = mountProvider("provider-b", bPut, () => (++bGets < 3
      ? pairResponse(bGets > 1, false) : pairResponse(true, true)));
    await waitFor(() => aGets === 1 && bGets === 1, "both initial lists");
    for (const [mount, label] of [[aMount, "OpenAI"], [bMount, "Anthropic"]]) {
      cards(mount.main).find((card) => cardName(card) === label).click();
      changeEnabled(enabledSwitch(mount.main), true);
    }
    await waitFor(() => aMount.calls.some((call) => call.options.method === "PUT")
      && bMount.calls.some((call) => call.options.method === "PUT"), "both mutation dispatches");
    aPut.resolve({ ok: true }); await waitFor(() => aGets === 2 && bGets === 2, "epoch-one reads");
    bPut.resolve({ ok: true }); await waitFor(() => aGets === 3 && bGets === 3, "epoch-two reads");
    (freshFirst ? fresh : stale).resolve(freshFirst ? pairResponse(true, true) : pairResponse(true, false));
    await flush();
    if (reject) stale.reject(new Error("private stale owner failure"));
    else (freshFirst ? stale : fresh).resolve(freshFirst ? pairResponse(true, false) : pairResponse(true, true));
    await flush(); await flush();
    assert.deepEqual([enabledSwitch(aMount.main).checked, enabledSwitch(aMount.main)
      .getAttribute("aria-checked")], [true, "true"]);
    assert.doesNotMatch(text(aMount.main), /private stale owner|Chưa thể/iu);
    find(aMount.main, (node) => hasClass(node, "pv-back")).click();
    cards(aMount.main).find((card) => cardName(card) === "Anthropic").click();
    assert.equal(enabledSwitch(aMount.main).checked, true, "A retains B's later settled state");
    assert.deepEqual([aMount, bMount].map((mount) => mount.calls.filter(
      (call) => call.options.method === "PUT").length), [1, 1]);
  });
});
test("disposing a pending ON hands authoritative state to a successor without old-page reconciliation", async (t) => {
  let committed = false;
  let oldLists = 0;
  const updateGate = deferred();
  const { calls, main, mounted } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      oldLists++;
      return providerRuntimeResponse([{ ...CODEX_CONNECTED, enabled: committed }]);
    }
    if (path === "/llm/providers/codex" && options.method === "PUT") return updateGate.promise;
    throw new Error(`Unexpected old page call: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((card) => cardName(card) === "Codex").click();
  const oldControl = enabledSwitch(main);
  changeEnabled(oldControl, true);
  await flush();
  mounted.dispose();
  main.remove();
  let successorLists = 0;
  const successorMain = document.createElement("main");
  document.body.append(successorMain);
  const successor = createProvidersPage({ request: async (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      successorLists++;
      return providerRuntimeResponse([{ ...CODEX_CONNECTED, enabled: committed }]);
    }
    throw new Error(`Unexpected successor call: ${options.method || "GET"} ${path}`);
  }}).mount(successorMain);
  t.after(() => successor.dispose());
  await flush(); await flush();
  cards(successorMain).find((card) => cardName(card) === "Codex").click();
  assert.equal(successorLists, 1);
  assert.equal(enabledSwitch(successorMain).checked, false, "the successor initially observes pre-commit OFF");
  committed = true;
  updateGate.resolve({ ok: true });
  await waitFor(() => successorLists === 2, "a successor reconciliation after the old PUT settles");
  await flush();
  assert.equal(oldLists, 1, "dispose aborts instead of starting an old reconciliation GET");
  assert.equal(oldControl.isConnected, false);
  assert.doesNotMatch(text(main), /Thử lại|private/iu, "the disposed page emits no late UI");
  assert.equal(successorLists, 2);
  assert.equal(enabledSwitch(successorMain).checked, true);
  assert.equal(calls.filter((call) => call.options.method === "PUT").length, 1, "handoff never replays PUT");
  assert.equal(find(document.body, (node) => node.tagName === "DIALOG"), null);
  assert.doesNotMatch(text(successorMain), /private|retry/iu);
});
test("detail lists available models with the 9Router-style prefix", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  cards(main).find((c) => cardName(c) === "Claude Code").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  assert.deepEqual(findAll(detail, (n) => hasClass(n, "pv-mid")).map(text), ["cc/sonnet"]);
});
test("both subscription CLIs (codex, claude-code) have an enabled add-connection button", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  let detail = find(main, (n) => hasClass(n, "pv-detail"));
  assert.match(text(find(detail, (n) => hasClass(n, "pv-connections"))), /Chưa có tài khoản/);
  let add = find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n)));
  assert.ok(add && !add.disabled, "codex is connectable — add button is enabled");
  assert.notEqual(add.getAttribute("title"), "Sắp có");
  find(detail, (n) => hasClass(n, "pv-back")).click();
  await flush();
  cards(main).find((c) => cardName(c) === "Claude Code").click();
  await flush();
  detail = find(main, (n) => hasClass(n, "pv-detail"));
  add = find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n)));
  assert.ok(add && !add.disabled, "claude-code is now connectable — add button is enabled");
  assert.notEqual(add.getAttribute("title"), "Sắp có");
});
test("subscription detail lists connected accounts with a delete button and a '+ Thêm account' button", async (t) => {
  const { main } = mountPage(t, listWith([CODEX_CONNECTED]));
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  const labels = findAll(detail, (n) => hasClass(n, "pv-account-label")).map(text);
  assert.deepEqual(labels, ["Tài khoản 1"]);
  assert.ok(find(detail, (n) => n.tagName === "BUTTON" && /Xoá/.test(text(n))), "each account has a delete button");
  assert.ok(find(detail, (n) => n.tagName === "BUTTON" && /Thêm account/.test(text(n))), "add button reads '+ Thêm account' when accounts exist");
});
test("account deletion repaints every changed detail projection from its authoritative GET", async (t) => {
  let listCalls = 0, deleted = false;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      const response = providerRuntimeResponse([deleted ? {
        ...CODEX_CONNECTED, name: "Codex persisted renamed", system: true, accounts: [],
        models: [{ model_id: "new-model", name: "New Model", source: "manual", available: true }],
      } : CODEX_CONNECTED]);
      if (deleted) Object.assign(response.provider_options.find((entry) => entry.kind === "codex"),
        { display_name: "Codex mới", prefix: "new", execution_mode: "official_cli" });
      return response;
    }
    if (path === "/llm/providers/codex/accounts/a1" && options.method === "DELETE") { deleted = true; return null; }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Xoá/.test(text(n))).click();
  await waitFor(() => listCalls === 2, "post-delete Provider GET"); await flush();
  assert.ok(calls.some((c) => c.path === "/llm/providers/codex/accounts/a1" && c.options.method === "DELETE"), "DELETE issued");
  assert.equal(listCalls, 2, "delete triggers one provider-list refresh");
  assert.deepEqual(findAll(main, (node) => hasClass(node, "pv-account-label")).map(text), []);
  assert.match(text(main), /Codex mới|new\/new-model|New Model|CLI chính chủ/iu);
  assert.equal(enabledSwitch(main).disabled, true, "the reconciled system flag locks the switch");
});
test("'+ Thêm account' reuses the connect flow (opens the label prompt)", async (t) => {
  const { calls, main } = mountPage(t, listWith([CODEX_CONNECTED]));
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Thêm account/.test(text(n))).click();
  await flush();
  assert.ok(find(main, (node) => node.tagName === "BUTTON"
    && /Bắt đầu kết nối/.test(text(node))), "add-account opens the connect prompt");
  assert.equal(calls.some((call) => call.path.endsWith("/connect")
    && call.options.method === "POST"), false);
});
test("subscription connect: click drives phases through to connected and refreshes the list", async (t) => {
  const phases = ["awaiting_login", "polling", "connected"];
  let i = 0;
  let listCalls = 0;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      const accounts = listCalls > 1
        ? [...CODEX_CONNECTED.accounts, { id: "a2", label: "Tài khoản 2", email: "", enabled: true }]
        : CODEX_CONNECTED.accounts;
      return providerRuntimeResponse([{ ...CODEX_CONNECTED, accounts }]);
    }
    if (path === "/llm/providers/codex/connect" && options.method === "POST") return { kind: "codex", phase: "detecting" };
    if (path === "/llm/providers/codex/connect" && !options.method) {
      const p = phases[Math.min(i++, phases.length - 1)];
      return {
        kind: "codex",
        phase: p,
        loginUrl: p === "awaiting_login" ? "https://auth.example/x" : "",
        providerId: p === "connected" ? "codex" : "",
        accountId: p === "connected" ? "a2" : "",
      };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm account|Thêm kết nối/.test(text(n))).click();
  await flush();
  const start = find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n)));
  assert.ok(start, "prompt shows a start button");
  start.click();
  for (let k = 0; k < 50 && listCalls < 2; k++) await new Promise((r) => setTimeout(r, 0));
  const post = calls.find((c) => c.path === "/llm/providers/codex/connect" && c.options.method === "POST");
  assert.ok(post && typeof post.options.body.label === "string" && post.options.body.label.length > 0, "POST connect carries a label");
  assert.equal(Object.hasOwn(post.options.body, "onboarding_revision"), false,
    "ordinary Providers connect omits onboarding_revision entirely");
  assert.equal(listCalls, 2, "one connected terminal refreshes the provider list exactly once");
  assert.deepEqual(findAll(main, (node) => hasClass(node, "pv-account-label")).map(text),
    ["Tài khoản 1", "Tài khoản 2"], "an added account repaints the same Provider detail");
});
test("a connected snapshot without terminal ids cannot refresh the Provider detail", async (t) => {
  let listCalls = 0;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      return providerRuntimeResponse();
    }
    if (path === "/llm/providers/codex/connect" && options.method === "POST") {
      return { kind: "codex", phase: "detecting" };
    }
    if (path === "/llm/providers/codex/connect" && !options.method) {
      return { kind: "codex", phase: "connected" };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((cardNode) => cardName(cardNode) === "Codex").click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Thêm kết nối/.test(text(node))).click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Bắt đầu/.test(text(node))).click();
  await flush();
  assert.equal(listCalls, 1, "an incomplete terminal payload never invokes the refresh callback");
  assert.match(text(main), /thiếu định danh Provider hoặc tài khoản/);
  assert.notEqual(find(main, (node) => hasClass(node, "pv-progress"))?.getAttribute("aria-valuenow"), "100",
    "an invalid connected snapshot never paints completed progress");
});
test("a contradictory connected identity cannot refresh the Provider detail", async (t) => {
  let listCalls = 0;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      return providerRuntimeResponse();
    }
    if (path === "/llm/providers/codex/connect" && options.method === "POST") {
      return { kind: "codex", phase: "detecting" };
    }
    if (path === "/llm/providers/codex/connect" && !options.method) {
      return {
        kind: "claude-code",
        phase: "connected",
        providerId: "claude-code",
        accountId: "foreign-account",
      };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((cardNode) => cardName(cardNode) === "Codex").click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Thêm kết nối/.test(text(node))).click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Bắt đầu/.test(text(node))).click();
  for (let turn = 0; turn < 20; turn++) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(listCalls, 1, "a foreign connected identity never invokes the account refresh callback");
  assert.match(text(main), /phản hồi trạng thái kết nối không hợp lệ/i);
  assert.notEqual(find(main, (node) => hasClass(node, "pv-progress"))?.getAttribute("aria-valuenow"), "100",
    "a contradictory identity never paints completed progress");
});
test("running connect (polling) renders the login link + device-auth code", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === "/llm/providers/codex/connect" && options.method === "POST") return { kind: "codex", phase: "detecting" };
    if (path === "/llm/providers/codex/connect" && !options.method) {
      return { kind: "codex", phase: "polling", loginUrl: "https://auth.example/x", code: "EQ0J-QKCPZ" };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))).click();
  for (let k = 0; k < 20; k++) await new Promise((r) => setTimeout(r, 0));
  const link = find(main, (n) => n.tagName === "A" && /Mở trang đăng nhập/.test(text(n)));
  assert.ok(link, "the login link must render while the connect is polling");
  assert.equal(link.getAttribute("href"), "https://auth.example/x");
  const codeEl = find(main, (n) => hasClass(n, "pv-connect-code-value"));
  assert.ok(codeEl, "the device-auth code must render while the connect is polling");
  assert.equal(text(codeEl), "EQ0J-QKCPZ");
});
test("claude connect (polling) renders the login link but no device-code block", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === "/llm/providers/claude-code/connect" && options.method === "POST") return { kind: "claude-code", phase: "detecting" };
    if (path === "/llm/providers/claude-code/connect" && !options.method) {
      return { kind: "claude-code", phase: "polling", loginUrl: "https://claude.ai/login/x" };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "Claude Code").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))).click();
  for (let k = 0; k < 20; k++) await new Promise((r) => setTimeout(r, 0));
  const link = find(main, (n) => n.tagName === "A" && /Mở trang đăng nhập/.test(text(n)));
  assert.ok(link, "the login link must render for claude");
  assert.equal(link.getAttribute("href"), "https://claude.ai/login/x");
  assert.equal(find(main, (n) => hasClass(n, "pv-connect-code-value")), null, "claude has no device-auth code block");
});
test("claude connect failure points the user at claude.com/claude-code", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === "/llm/providers/claude-code/connect" && options.method === "POST") return { kind: "claude-code", phase: "detecting" };
    if (path === "/llm/providers/claude-code/connect" && !options.method) {
      return { kind: "claude-code", phase: "error", message: "Không cài được Claude" };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "Claude Code").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))).click();
  for (let k = 0; k < 20; k++) await new Promise((r) => setTimeout(r, 0));
  const link = find(main, (n) => n.tagName === "A" && /claude\.com\/claude-code/.test(text(n)));
  assert.ok(link, "claude connect failure must link to the install page");
  assert.equal(link.getAttribute("href"), "https://claude.com/claude-code");
});
test("navigating back to the gallery stops the connect poll loop", async (t) => {
  let connectGets = 0;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === "/llm/providers/codex/connect" && options.method === "POST") return { kind: "codex", phase: "detecting" };
    if (path === "/llm/providers/codex/connect" && !options.method) { connectGets++; return { kind: "codex", phase: "polling" }; }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))).click();
  for (let k = 0; k < 20; k++) await new Promise((r) => setTimeout(r, 0));
  const seenBeforeNav = connectGets;
  assert.ok(seenBeforeNav > 0, "the poll loop made at least one status GET before navigating away");
  find(main, (n) => hasClass(n, "pv-back")).click();
  await flush();
  for (let k = 0; k < 20; k++) await new Promise((r) => setTimeout(r, 0));
  assert.equal(connectGets, seenBeforeNav, "no further connect-status GETs after leaving the detail page");
});
test("cancel: an accepted DELETE renders canceled and stops the live flow", async (t) => {
  let connectGets = 0;
  const statusGate = deferred();
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === "/llm/providers/codex/connect" && options.method === "POST") return { kind: "codex", phase: "detecting" };
    if (path === "/llm/providers/codex/connect" && !options.method) { connectGets++; return statusGate.promise; }
    if (path === "/llm/providers/codex/connect" && options.method === "DELETE") return { ok: true };
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))).click();
  await waitFor(() => connectGets === 1, "the first status request to be in flight");
  const cancel = find(main, (n) => n.tagName === "BUTTON" && /Huỷ/.test(text(n)));
  assert.ok(cancel, "a cancel button is shown while the connect flow runs");
  cancel.click();
  await flush();
  assert.ok(calls.some((c) => c.path === "/llm/providers/codex/connect" && c.options.method === "DELETE"),
    "cancel issues a DELETE to the connect endpoint");
  assert.match(text(main), /Đã huỷ kết nối/, "an accepted cancel is rendered as canceled");
  statusGate.resolve({ kind: "codex", phase: "polling" });
  await flush();
  assert.equal(connectGets, 1, "the released stale status request cannot restart polling after cancel");
  assert.match(text(main), /Đã huỷ kết nối/, "a stale status result cannot overwrite accepted cancel");
});
test("cancel: a rejected ownership claim warns, refreshes live status, and keeps polling", async (t) => {
  let deleteSeen = false;
  let connectGets = 0;
  let statusGetsAfterDelete = 0;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === "/llm/providers/codex/connect" && options.method === "POST") {
      return { kind: "codex", phase: "detecting" };
    }
    if (path === "/llm/providers/codex/connect" && options.method === "DELETE") {
      deleteSeen = true;
      return { ok: false };
    }
    if (path === "/llm/providers/codex/connect" && !options.method) {
      connectGets++;
      if (deleteSeen) statusGetsAfterDelete++;
      return {
        kind: "codex",
        phase: "polling",
        message: deleteSeen ? "Kết nối vẫn đang được hoàn tất." : "",
      };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  }, { pollMs: 0 });
  await flush();
  cards(main).find((cardNode) => cardName(cardNode) === "Codex").click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Thêm kết nối/.test(text(node))).click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Bắt đầu/.test(text(node))).click();
  await waitFor(() => connectGets >= 1, "the first status snapshot");
  find(main, (node) => node.tagName === "BUTTON" && /Huỷ/.test(text(node))).click();
  await waitFor(() => statusGetsAfterDelete >= 1, "an authoritative status refresh after ok:false");
  assert.doesNotMatch(text(main), /Đã huỷ kết nối/, "ok:false never paints an optimistic canceled state");
  assert.match(text(main), /Kết nối vẫn đang được hoàn tất/, "the refreshed live phase remains visible");
  assert.ok(find(main, (node) => hasClass(node, "pv-connect-warning")),
    "uncertain cancellation is rendered as a non-terminal warning");
  const getsAfterRefresh = statusGetsAfterDelete;
  await waitFor(() => statusGetsAfterDelete > getsAfterRefresh, "the reconciliation generation to continue polling");
});
test("cancel: a DELETE transport error stays nonterminal and polling continues", async (t) => {
  let connectGets = 0;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === "/llm/providers/codex/connect" && options.method === "POST") {
      return { kind: "codex", phase: "detecting" };
    }
    if (path === "/llm/providers/codex/connect" && !options.method) {
      connectGets++;
      return { kind: "codex", phase: "polling" };
    }
    if (path === "/llm/providers/codex/connect" && options.method === "DELETE") {
      throw new Error("Không thể huỷ kết nối");
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  }, { pollMs: 0 });
  await flush();
  cards(main).find((cardNode) => cardName(cardNode) === "Codex").click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Thêm kết nối/.test(text(node))).click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Bắt đầu/.test(text(node))).click();
  await waitFor(() => connectGets >= 1, "the first status snapshot");
  find(main, (node) => node.tagName === "BUTTON" && /Huỷ/.test(text(node))).click();
  await waitFor(() => /Không thể huỷ kết nối/.test(text(main)), "the cancel-specific warning");
  assert.match(text(main), /Đang xác nhận đăng nhập/, "the authoritative polling phase remains visible");
  assert.match(text(main), /Tiếp tục theo dõi kết nối/, "the warning explains that status tracking continues");
  assert.ok(find(main, (node) => hasClass(node, "pv-connect-warning")),
    "the feedback is rendered as a non-terminal warning");
  const getsWithWarning = connectGets;
  await waitFor(() => connectGets > getsWithWarning, "status polling after the DELETE error");
});
test("cancel: a reconciliation GET error keeps the live phase and polling active", async (t) => {
  let connectGets = 0;
  let failNextStatus = false;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providerRuntimeResponse();
    if (path === "/llm/providers/codex/connect" && options.method === "POST") {
      return { kind: "codex", phase: "detecting" };
    }
    if (path === "/llm/providers/codex/connect" && options.method === "DELETE") return { ok: false };
    if (path === "/llm/providers/codex/connect" && !options.method) {
      connectGets++;
      if (failNextStatus) {
        failNextStatus = false;
        throw new Error("Không đọc được trạng thái sau khi huỷ");
      }
      return { kind: "codex", phase: "polling", message: "Đăng nhập vẫn đang chờ xác nhận." };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  }, { pollMs: 0 });
  await flush();
  cards(main).find((cardNode) => cardName(cardNode) === "Codex").click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Thêm kết nối/.test(text(node))).click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Bắt đầu/.test(text(node))).click();
  await waitFor(() => connectGets >= 1, "the initial polling status");
  failNextStatus = true;
  find(main, (node) => node.tagName === "BUTTON" && /Huỷ/.test(text(node))).click();
  await waitFor(() => /Không đọc được trạng thái sau khi huỷ/.test(text(main)), "the reconciliation warning");
  assert.match(text(main), /Đang xác nhận đăng nhập/, "the last authoritative phase survives reconciliation failure");
  assert.match(text(main), /Đăng nhập vẫn đang chờ xác nhận/, "the last backend message remains visible");
  const getsWithWarning = connectGets;
  await waitFor(() => connectGets > getsWithWarning, "status polling after reconciliation failure");
});
test("cancel: stale old polls cannot refresh, while fresh reconciliation can", async (t) => {
  const deleteGate = deferred();
  const deleteStarted = deferred();
  const staleConnected = deferred();
  const reconciledConnected = deferred();
  let connectGets = 0;
  let listGets = 0;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listGets++;
      return providerRuntimeResponse(listGets > 1 ? [CODEX_CONNECTED] : [], {
        hasConnectedProvider: listGets > 1,
      });
    }
    if (path === "/llm/providers/codex/connect" && options.method === "POST") {
      return { kind: "codex", phase: "detecting" };
    }
    if (path === "/llm/providers/codex/connect" && options.method === "DELETE") {
      deleteStarted.resolve();
      return deleteGate.promise;
    }
    if (path === "/llm/providers/codex/connect" && !options.method) {
      connectGets++;
      return connectGets === 1 ? staleConnected.promise : reconciledConnected.promise;
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  }, { pollMs: 0 });
  await flush();
  cards(main).find((cardNode) => cardName(cardNode) === "Codex").click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Thêm kết nối/.test(text(node))).click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && /Bắt đầu/.test(text(node))).click();
  await waitFor(() => connectGets === 1, "the old status request to be in flight");
  find(main, (node) => node.tagName === "BUTTON" && /Huỷ/.test(text(node))).click();
  await deleteStarted.promise;
  staleConnected.resolve({
    kind: "codex",
    phase: "connected",
    providerId: "codex",
    accountId: "stale-account",
  });
  await flush();
  assert.equal(listGets, 1, "the invalidated old poll cannot invoke the refresh callback");
  deleteGate.reject(new Error("Không thể huỷ kết nối muộn"));
  await waitFor(() => connectGets === 2, "the fresh reconciliation status request");
  reconciledConnected.resolve({
    kind: "codex",
    phase: "connected",
    providerId: "codex",
    accountId: "a1",
  });
  await waitFor(() => listGets === 2, "the reconciled connected provider refresh");
  assert.match(text(main), /Tài khoản 1/, "the fresh authoritative generation refreshes the account");
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(listGets, 2, "the reconciled connected callback fires exactly once");
});
