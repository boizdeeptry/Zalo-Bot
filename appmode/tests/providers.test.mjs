import test from "node:test";
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { createProviderService, createProvidersPage } from "../overlay/internal/webui/static/pages/providers.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const staticRoot = new URL("../overlay/internal/webui/static/", import.meta.url);
const flush = () => new Promise((r) => setImmediate(r));
const hasClass = (n, c) => n.classList?.contains(c) ?? false;
const cards = (main) => findAll(main, (n) => hasClass(n, "pv-card"));
const cardName = (card) => text(find(card, (n) => hasClass(n, "pv-name")));

const CLAUDE_ADDED = {
  id: "claude-code", name: "Claude Code", kind: "claude-code", system: true, enabled: true,
  credential_configured: false, credential_unreadable: false, last_check_status: "", last_error: "",
  models: [{ model_id: "sonnet", name: "Claude Sonnet", source: "manual", available: true }],
};
const CODEX_CONNECTED = {
  id: "codex", name: "OpenAI Codex", kind: "codex", system: false, enabled: true,
  credential_configured: false, credential_unreadable: false, last_check_status: "", last_error: "",
  models: [],
  accounts: [{ id: "a1", label: "Tài khoản 1", email: "", enabled: true }],
};
function mountPage(t, handler, opts = {}) {
  const dom = installDOM();
  const calls = [];
  const page = createProvidersPage({ pollMs: 0, ...opts, request: async (path, options = {}) => {
    const { signal, ...recorded } = options; calls.push({ path, options: recorded }); return handler(path, options);
  }});
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(() => { mounted.dispose(); dom.restore(); });
  return { calls, main, mounted };
}
const listWith = (providers) => (path, options = {}) => {
  if (path === "/llm/providers" && !options.method) return { providers, kinds: [] };
  throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
};

test("gallery renders the 6-provider catalogue in two groups", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  assert.deepEqual(cards(main).map(cardName),
    ["Claude Code", "OpenAI Codex", "OpenAI", "Anthropic", "Gemini", "OpenRouter"]);
  const groups = findAll(main, (n) => hasClass(n, "pv-group"));
  assert.equal(groups.length, 2);
});

test("the subscription group carries the green official-CLI safety badge, never a ban warning", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  const tag = find(main, (n) => hasClass(n, "pv-safe-tag"));
  assert.ok(tag, "subscription group must show the safety badge");
  assert.match(text(tag), /chính chủ/i);
  for (const bad of ["Risk Notice", "banned", "restricted", "proxy/router"]) {
    assert.ok(!text(main).includes(bad), `gallery must not contain "${bad}"`);
  }
});

test("an added provider shows connected-ish status, an unlisted one shows not connected", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  const claude = cards(main).find((c) => cardName(c) === "Claude Code");
  const codex = cards(main).find((c) => cardName(c) === "OpenAI Codex");
  assert.ok(hasClass(find(claude, (n) => hasClass(n, "pv-status")), "on"));
  assert.ok(hasClass(find(codex, (n) => hasClass(n, "pv-status")), "off"));
  assert.match(text(codex), /Chưa kết nối/);
});

test("a subscription provider with a connected account shows Đã kết nối, not credential-gated", async (t) => {
  const { main } = mountPage(t, listWith([CODEX_CONNECTED]));
  await flush();
  const codex = cards(main).find((c) => cardName(c) === "OpenAI Codex");
  const status = find(codex, (n) => hasClass(n, "pv-status"));
  // codex-proxy có 0 credential nhưng CÓ account → phải "Đã kết nối" (không rơi về "Chưa kết nối").
  assert.ok(hasClass(status, "on"), "codex with an enabled account must read as connected");
  assert.match(text(status), /Đã kết nối/);
});

test("typing in the search box filters cards by name", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  const box = find(main, (n) => n.tagName === "INPUT" && n.getAttribute?.("type") === "search");
  assert.ok(box);
  box.value = "codex";
  box.dispatchEvent({ type: "input" });
  assert.deepEqual(cards(main).map(cardName), ["OpenAI Codex"]);
});

test("clicking a card opens its detail; back returns to the gallery", async (t) => {
  const { main } = mountPage(t, listWith([CLAUDE_ADDED]));
  await flush();
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  assert.ok(detail);
  assert.equal(text(find(detail, (n) => hasClass(n, "pv-name"))), "OpenAI Codex");
  // Codex chạy qua PROXY nên detail phải mang huy hiệu RỦI RO (không phải huy hiệu an toàn) — nói thật
  // về nguy cơ khoá tài khoản, không dán nhãn "không rủi ro".
  const riskBadge = find(detail, (n) => hasClass(n, "pv-risk-badge"));
  assert.ok(riskBadge, "Codex (proxy) detail must show the risk badge, not a safety badge");
  assert.match(text(riskBadge), /rủi ro|khoá/i);
  assert.equal(find(detail, (n) => hasClass(n, "pv-safe-badge")), null, "no false safety badge on a proxy provider");
  find(detail, (n) => hasClass(n, "pv-back")).click();
  await flush();
  assert.ok(find(main, (n) => hasClass(n, "pv-gallery")));
  assert.equal(find(main, (n) => hasClass(n, "pv-detail")), null);
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

  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
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
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  const labels = findAll(detail, (n) => hasClass(n, "pv-account-label")).map(text);
  assert.deepEqual(labels, ["Tài khoản 1"]);
  assert.ok(find(detail, (n) => n.tagName === "BUTTON" && /Xoá/.test(text(n))), "each account has a delete button");
  assert.ok(find(detail, (n) => n.tagName === "BUTTON" && /Thêm account/.test(text(n))), "add button reads '+ Thêm account' when accounts exist");
});

test("deleting an account issues DELETE .../accounts/{id} and refreshes", async (t) => {
  let listCalls = 0, deleted = false;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listCalls++;
      return { providers: [deleted ? { ...CODEX_CONNECTED, accounts: [] } : CODEX_CONNECTED], kinds: [] };
    }
    if (path === "/llm/providers/codex/accounts/a1" && options.method === "DELETE") { deleted = true; return null; }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Xoá/.test(text(n))).click();
  await flush();
  assert.ok(calls.some((c) => c.path === "/llm/providers/codex/accounts/a1" && c.options.method === "DELETE"), "DELETE issued");
  assert.ok(listCalls >= 2, "delete triggers a provider-list refresh");
});

test("'+ Thêm account' reuses the connect flow (opens the label prompt)", async (t) => {
  const { main } = mountPage(t, listWith([CODEX_CONNECTED]));
  await flush();
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Thêm account/.test(text(n))).click();
  await flush();
  assert.ok(find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))), "add-account opens the connect prompt");
});

test("subscription connect: click drives phases through to connected and refreshes the list", async (t) => {
  const phases = ["awaiting_login", "polling", "connected"];
  let i = 0;
  let listCalls = 0;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) { listCalls++; return { providers: [], kinds: [] }; }
    if (path === "/llm/providers/codex/connect" && options.method === "POST") return { kind: "codex", phase: "detecting" };
    if (path === "/llm/providers/codex/connect" && !options.method) {
      const p = phases[Math.min(i++, phases.length - 1)];
      return { kind: "codex", phase: p, loginUrl: p === "awaiting_login" ? "https://auth.example/x" : "" };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();

  const start = find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n)));
  assert.ok(start, "prompt shows a start button");
  start.click();
  // Each phase transition round-trips through the poll loop's sleep(pollMs=0), a setTimeout
  // macrotask. flush() (setImmediate) never lets that timer fire — repeatedly scheduling
  // setImmediate from inside a resolved setImmediate callback starves the timers phase — so this
  // wait uses setTimeout(0) directly to give the loop real event-loop turns.
  for (let k = 0; k < 50 && listCalls < 2; k++) await new Promise((r) => setTimeout(r, 0));

  const post = calls.find((c) => c.path === "/llm/providers/codex/connect" && c.options.method === "POST");
  assert.ok(post && typeof post.options.body.label === "string" && post.options.body.label.length > 0, "POST connect carries a label");
  assert.ok(listCalls >= 2, "connected triggers a provider-list refresh");
});

// The backend sets loginUrl/code exactly as it flips awaiting_login -> polling, so the URL+code
// must render during POLLING, not only awaiting_login (a real E2E bug: gating the display on
// phase==="awaiting_login" meant the user never saw the code they must type). Mock stuck on
// polling-with-url is the robust guard.
test("running connect (polling) renders the login link + device-auth code", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return { providers: [], kinds: [] };
    if (path === "/llm/providers/codex/connect" && options.method === "POST") return { kind: "codex", phase: "detecting" };
    if (path === "/llm/providers/codex/connect" && !options.method) {
      return { kind: "codex", phase: "polling", loginUrl: "https://auth.example/x", code: "EQ0J-QKCPZ" };
    }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
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

// Claude's login is a plain URL link — no device-auth code. The panel must render the clickable
// link and NO code block (paintConnect's `code ? … : null` guard holds when code is absent).
// Mirrors the codex polling test above, minus the code.
test("claude connect (polling) renders the login link but no device-code block", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return { providers: [], kinds: [] };
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

// Claude can't be auto-installed (native install, not npm), so an install-step failure surfaces a
// generic error. The claude connect error panel must point the user at claude.com/claude-code.
test("claude connect failure points the user at claude.com/claude-code", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return { providers: [], kinds: [] };
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
    if (path === "/llm/providers" && !options.method) return { providers: [], kinds: [] };
    if (path === "/llm/providers/codex/connect" && options.method === "POST") return { kind: "codex", phase: "detecting" };
    if (path === "/llm/providers/codex/connect" && !options.method) { connectGets++; return { kind: "codex", phase: "polling" }; }
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
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

test("cancel: clicking Huỷ while polling sends a DELETE to the connect endpoint", async (t) => {
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return { providers: [], kinds: [] };
    if (path === "/llm/providers/codex/connect" && options.method === "POST") return { kind: "codex", phase: "detecting" };
    if (path === "/llm/providers/codex/connect" && !options.method) return { kind: "codex", phase: "polling" };
    if (path === "/llm/providers/codex/connect" && options.method === "DELETE") return { ok: true };
    throw new Error(`Unexpected: ${options.method || "GET"} ${path}`);
  });
  await flush();
  cards(main).find((c) => cardName(c) === "OpenAI Codex").click();
  await flush();
  const detail = find(main, (n) => hasClass(n, "pv-detail"));
  find(detail, (n) => n.tagName === "BUTTON" && /Thêm kết nối/.test(text(n))).click();
  await flush();
  find(main, (n) => n.tagName === "BUTTON" && /Bắt đầu/.test(text(n))).click();
  await flush();

  const cancel = find(main, (n) => n.tagName === "BUTTON" && /Huỷ/.test(text(n)));
  assert.ok(cancel, "a cancel button is shown while the connect flow runs");
  cancel.click();
  await flush();

  assert.ok(calls.some((c) => c.path === "/llm/providers/codex/connect" && c.options.method === "DELETE"),
    "cancel issues a DELETE to the connect endpoint");
});

test("Test all runs the saved-provider test and refreshes status", async (t) => {
  let checked = false;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method)
      return { providers: [{ ...CLAUDE_ADDED, last_check_status: checked ? "ok" : "" }], kinds: [] };
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

// Khoá thử nghiệm của tầng Portal, cùng một chuỗi với llmPackageCanary bên Go. Cửa chặn gói
// (tests/build-app.Tests.ps1) quét đúng chuỗi con này trong gói đã dựng và đòi 0 lần khớp; quét
// một chuỗi không tệp nào trong repo mang thì luôn xanh, nên mọi khoá gõ vào test phải là nó.
//
// Đường rò có thật: trang này được nhúng thẳng vào agentdc.exe, nên một khoá copy từ đây sang
// pages/providers.js đi ra bản bán mà không nằm trong tệp văn bản nào của gói.
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

// Cửa chặn nguồn của tầng Portal. Assets nhúng thẳng vào agentdc.exe, nên một khoá còn sót trong
// bất kỳ tệp nào dưới static/ đều đi ra bản bán mà không nằm trong tệp văn bản nào của gói để quét.
//
// Duyệt CẢ CÂY chứ không riêng trang Providers: trang này là chỗ dễ bị dán khoá nhất, nhưng một
// khoá trong core/api.js rời khỏi máy y hệt, và một phép kiểm chỉ đọc những tệp mà người viết nó
// tình cờ đang sửa thì chỉ canh được đúng ngày nó ra đời.
//
// Bắt theo HÌNH DẠNG chứ không theo canary: cửa chặn gói tìm đúng chuỗi canary, nên nó bỏ lọt khoá
// THẬT của người đang gỡ lỗi — đúng thứ tệ nhất được để lại. Bốn tiền tố là bốn nhà cung cấp đang
// hỗ trợ, cùng bộ với mẫu che cuối cùng trong sanitizeProviderError.
//
// Không in ra chuỗi khớp: thông báo hỏng của test đi vào log, và chuỗi đó chính là thứ đang bị tố
// cáo. Tên tệp và số lượng đủ để biết phải đi tìm ở đâu.
//
// Cờ /g vì phép kiểm đếm mọi lần khớp trong một tệp. Dùng lại được qua nhiều lượt: String.match
// với mẫu global đặt lastIndex về 0 trước khi chạy, nên assert.match ở đầu test không làm lệch
// vòng lặp phía sau.
const KEY_SHAPE = /\b(?:sk-|xai-|gsk_|AIza)[A-Za-z0-9_-]{8,}/g;

test("no embedded Portal asset carries an API-key literal", async () => {
  // .html và .css chứ không riêng .js: `//go:embed static` lấy CẢ thư mục, nên index.html và
  // portal.css cũng nằm trong agentdc.exe — và một thẻ <script> nội tuyến trong index.html là chỗ
  // dán khoá không kém phần thực tế.
  const names = (await readdir(staticRoot, { recursive: true })).filter((n) => /\.(js|html|css)$/.test(n));
  // Ghim ĐỆ QUY bằng một mục lồng có tên, không bằng số lượng: một ngưỡng đếm đặt đúng bằng con số
  // hôm nay sẽ đỏ khi ai đó xoá một trang, với thông báo nói rằng phép duyệt hỏng.
  assert.ok(names.includes(join("pages", "providers.js")), `walk missed pages/providers.js: ${names}`);
  // Chính mẫu này là toàn bộ cơ chế phát hiện, và mọi khẳng định bên dưới đều là khẳng định VẮNG
  // MẶT: thay nó bằng một mẫu không khớp gì thì cả vòng lặp vẫn xanh. Một dòng khoá mẫu ở đây là
  // thứ duy nhất phân biệt "không có khoá nào" với "không tìm nữa".
  assert.match("sk-live-AbCdEfGh12345678", KEY_SHAPE);

  for (const name of names) {
    const source = await readFile(new URL(name, staticRoot), "utf8");
    const found = source.match(KEY_SHAPE) ?? [];
    assert.equal(found.length, 0, `${name} carries ${found.length} key-shaped literal(s)`);
  }
});
