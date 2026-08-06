import test from "node:test";
import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import { join } from "node:path";

import { AppAPIError } from "../overlay/internal/webui/static/core/api.js";
import {
  createProviderService,
  createProvidersPage,
} from "../overlay/internal/webui/static/pages/providers.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const staticRoot = new URL("../overlay/internal/webui/static/", import.meta.url);
const flush = () => new Promise((resolve) => setImmediate(resolve));

// Khoá thử nghiệm của tầng Portal, cùng một chuỗi với llmPackageCanary bên Go. Cửa chặn gói
// (tests/build-app.Tests.ps1) quét đúng chuỗi con này trong gói đã dựng và đòi 0 lần khớp; quét
// một chuỗi không tệp nào trong repo mang thì luôn xanh, nên mọi khoá gõ vào test phải là nó.
//
// Đường rò có thật: trang này được nhúng thẳng vào agentdc.exe, nên một khoá copy từ đây sang
// pages/providers.js đi ra bản bán mà không nằm trong tệp văn bản nào của gói.
const CANARY_KEY = "sk-package-must-never-contain-7f36d2";

function hasClass(node, className) {
  return node.classList?.contains(className) ?? false;
}

function button(root, label) {
  return find(root, (node) => node.tagName === "BUTTON" && text(node) === label);
}

function installKeyDispatcher(doc) {
  const listeners = new Set();
  doc.addEventListener = (type, listener) => { if (type === "keydown") listeners.add(listener); };
  doc.removeEventListener = (type, listener) => { if (type === "keydown") listeners.delete(listener); };
  doc.dispatchKeydown = (event) => {
    for (const listener of [...listeners]) listener(event);
  };
  return { listenerCount: () => listeners.size };
}

const KINDS = [
  { kind: "openai", label: "OpenAI", endpoint: "https://api.openai.com" },
  { kind: "anthropic", label: "Anthropic", endpoint: "https://api.anthropic.com" },
];

function provider(overrides = {}) {
  return {
    id: "openai",
    name: "OpenAI chính",
    kind: "openai",
    endpoint: "https://api.openai.com",
    enabled: true,
    system: false,
    credential_configured: true,
    credential_unreadable: false,
    last_check_status: "ok",
    last_error: "",
    last_checked_at: "2026-08-06T04:00:00Z",
    models: [{ model_id: "gpt-5", name: "GPT-5", source: "discovered", available: true }],
    ...overrides,
  };
}

const CLAUDE_CODE = provider({
  id: "claude-code",
  name: "Claude Code",
  kind: "claude_code",
  endpoint: "",
  system: true,
  credential_configured: false,
  last_check_status: "",
  last_checked_at: "",
  models: [{ model_id: "haiku", name: "Haiku", source: "manual", available: true }],
});

// mountPage dựng trang với một request ghi lại mọi lượt gọi. handler nhận (path, options) và trả
// về payload, hoặc ném lỗi để mô phỏng phía máy chủ.
//
// signal bị tách khỏi options đã ghi: trang gắn nó vào MỌI lượt gọi, nên để lại thì mọi phép so
// sánh thân request đều phải nhắc tới nó. Việc gắn signal được kiểm riêng ở test dispose.
function mountPage(t, handler) {
  const dom = installDOM();
  const keys = installKeyDispatcher(document);
  const calls = [];
  const page = createProvidersPage({
    request: async (path, options = {}) => {
      const { signal, ...recorded } = options;
      calls.push({ path, options: recorded });
      return handler(path, options);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(() => {
    mounted.dispose();
    dom.restore();
  });
  return { calls, keys, main, mounted };
}

function listOnly(providers = [provider(), CLAUDE_CODE]) {
  return (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return { providers, kinds: KINDS };
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  };
}

// savedList cho phép lượt PUT đi trọn vẹn, nên sheet đóng và trang tải lại thật. listOnly làm hỏng
// lượt ghi, và một test lưu chạy trên nhánh lỗi thì không nói được gì về nhánh thành công.
function savedList(providers = [provider()]) {
  return (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return { providers, kinds: KINDS };
    if (path === "/llm/providers/openai" && options.method === "PUT") {
      return { provider: providers[0] };
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  };
}

function rowFor(main, name) {
  return findAll(main, (node) => hasClass(node, "fact"))
    .find((row) => text(find(row, (node) => hasClass(node, "fkk"))) === name);
}

async function openSheet(main, name) {
  button(rowFor(main, name), "Sửa").click();
  await flush();
  return find(document.body, (node) => hasClass(node, "provider-sheet"));
}

async function openAddSheet(main) {
  button(main, "Thêm Provider").click();
  await flush();
  return find(document.body, (node) => hasClass(node, "provider-sheet"));
}

function keyInput(sheet) {
  return find(sheet, (node) => node.getAttribute?.("type") === "password");
}

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
  ]);
});

test("Providers lists every provider as a legacy fact row", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "Providers");
  const facts = find(main, (node) => hasClass(node, "facts"));
  assert.ok(facts);
  assert.deepEqual(
    findAll(facts, (node) => hasClass(node, "fact"))
      .map((row) => text(find(row, (node) => hasClass(node, "fkk")))),
    ["OpenAI chính", "Claude Code"],
  );
});

test("a provider row shows its type, model count, and last check status", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();

  const value = find(rowFor(main, "OpenAI chính"), (node) => hasClass(node, "fvv"));
  assert.equal(text(value), "OpenAI1 model · đã kiểm, chạy tốt");
});

test("a provider row reports an unreadable credential instead of a check result", async (t) => {
  const { main } = mountPage(t, listOnly([
    provider({ credential_unreadable: true }),
  ]));
  await flush();

  const value = find(rowFor(main, "OpenAI chính"), (node) => hasClass(node, "fvv"));
  assert.match(text(value), /cần nhập lại API key/);
});

test("a disabled provider is marked as switched off in its row", async (t) => {
  const { main } = mountPage(t, listOnly([provider({ enabled: false })]));
  await flush();

  const value = find(rowFor(main, "OpenAI chính"), (node) => hasClass(node, "fvv"));
  assert.match(text(value), /đang tắt/);
});

test("the add sheet offers an editable provider type built from the API catalogue", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openAddSheet(main);

  const select = find(sheet, (node) => node.tagName === "SELECT");
  assert.equal(select.disabled, false);
  assert.deepEqual(
    select.children.map((option) => [option.getAttribute("value"), text(option)]),
    [["openai", "OpenAI"], ["anthropic", "Anthropic"]],
  );
});

test("the add sheet follows the chosen type with the endpoint the key will reach", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openAddSheet(main);
  const endpoint = find(sheet, (node) => hasClass(node, "sp"));
  assert.equal(text(endpoint), "https://api.openai.com");

  const select = find(sheet, (node) => node.tagName === "SELECT");
  select.value = "anthropic";
  select.dispatchEvent({ type: "change" });

  assert.equal(text(endpoint), "https://api.anthropic.com");
});

test("the edit sheet locks the provider type", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");

  assert.equal(find(sheet, (node) => node.tagName === "SELECT").disabled, true);
});

test("the edit sheet seeds the name field with the saved name", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");

  const name = find(sheet, (node) => node.getAttribute?.("type") === "text");
  assert.equal(name.value, "OpenAI chính");
});

test("the API key field is a password input that never receives the saved key", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");

  const key = keyInput(sheet);
  assert.ok(key);
  assert.equal(key.value, "");
  assert.equal(key.getAttribute("autocomplete"), "new-password");
  assert.match(text(sheet), /Đã cấu hình/);
});

test("saving a blank API key keeps the stored key untouched", async (t) => {
  const { calls, main } = mountPage(t, savedList());
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  find(sheet, (node) => node.getAttribute?.("type") === "text").value = "Tên mới";
  find(sheet, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();

  // Sheet đã đóng nghĩa là lượt lưu đi hết nhánh thành công; không có nó thì phép khẳng định
  // "không đụng tới /credential" bên dưới chỉ đúng vì lượt lưu đã hỏng từ trước.
  assert.equal(find(document.body, (node) => hasClass(node, "provider-sheet")), null);
  assert.deepEqual(calls.filter(({ options }) => options.method), [{
    path: "/llm/providers/openai",
    options: { method: "PUT", body: { name: "Tên mới", enabled: true, credential: "" } },
  }]);
});

test("the enabled toggle travels with the provider save", async (t) => {
  const { calls, main } = mountPage(t, savedList());
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  find(sheet, (node) => node.getAttribute?.("type") === "checkbox").checked = false;
  find(sheet, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();

  assert.equal(calls.find(({ options }) => options.method === "PUT").options.body.enabled, false);
});

test("creating a provider posts the chosen type, name, and typed key", async (t) => {
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers" && options.method === "POST") return { provider: provider() };
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openAddSheet(main);
  find(sheet, (node) => node.tagName === "SELECT").value = "anthropic";
  find(sheet, (node) => node.getAttribute?.("type") === "text").value = "Anthropic phụ";
  keyInput(sheet).value = CANARY_KEY;
  find(sheet, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();

  assert.deepEqual(calls.find(({ options }) => options.method === "POST"), {
    path: "/llm/providers",
    options: {
      method: "POST",
      body: { kind: "anthropic", name: "Anthropic phụ", enabled: true, credential: CANARY_KEY },
    },
  });
});

test("replacing the credential is its own action carrying only the typed key", async (t) => {
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers/openai/credential" && options.method === "PUT") return null;
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  keyInput(sheet).value = CANARY_KEY;
  button(sheet, "Thay khoá").click();
  await flush();

  assert.deepEqual(calls.at(-1), {
    path: "/llm/providers/openai/credential",
    options: { method: "PUT", body: { credential: CANARY_KEY } },
  });
  // Bản rõ vừa gõ không được ở lại trong DOM sau khi đã gửi đi.
  assert.equal(keyInput(sheet).value, "");
});

test("replacing the credential refuses an empty field instead of calling the API", async (t) => {
  const { calls, main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  button(sheet, "Thay khoá").click();
  await flush();

  assert.equal(calls.some(({ path }) => path.endsWith("/credential")), false);
  assert.match(text(find(sheet, (node) => hasClass(node, "sheetfoot"))), /Nhập API key mới/);
});

test("clearing the credential is a separate explicit action", async (t) => {
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers/openai/credential" && options.method === "DELETE") return null;
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  keyInput(sheet).value = CANARY_KEY;
  button(sheet, "Xoá khoá").click();
  await flush();

  assert.deepEqual(calls.at(-1), {
    path: "/llm/providers/openai/credential",
    options: { method: "DELETE" },
  });
  assert.equal(keyInput(sheet).value, "");
});

test("testing an unsaved provider posts the typed key to the draft endpoint", async (t) => {
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers/test" && options.method === "POST") return { ok: true };
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openAddSheet(main);
  keyInput(sheet).value = CANARY_KEY;
  button(sheet, "Kiểm tra kết nối").click();
  await flush();

  assert.deepEqual(calls.at(-1), {
    path: "/llm/providers/test",
    options: { method: "POST", body: { kind: "openai", credential: CANARY_KEY, model: "" } },
  });
});

test("a passing saved test discovers models and renders them", async (t) => {
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers/openai/test" && options.method === "POST") return { ok: true };
    if (path === "/llm/providers/openai/discover" && options.method === "POST") {
      return {
        models: [
          { model_id: "gpt-5", name: "GPT-5", source: "discovered", available: true },
          { model_id: "gpt-5-mini", name: "GPT-5 mini", source: "discovered", available: true },
        ],
      };
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  button(sheet, "Kiểm tra kết nối").click();
  await flush();

  assert.deepEqual(calls.map(({ path }) => path).slice(-2), [
    "/llm/providers/openai/test",
    "/llm/providers/openai/discover",
  ]);
  assert.deepEqual(
    findAll(sheet, (node) => hasClass(node, "mid")).map(text),
    ["gpt-5", "gpt-5-mini"],
  );
});

test("a failed discovery keeps the models already on screen", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers/openai/test" && options.method === "POST") return { ok: true };
    if (path === "/llm/providers/openai/discover" && options.method === "POST") {
      throw new Error("Không lấy được danh sách model từ OpenAI chính");
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  button(sheet, "Kiểm tra kết nối").click();
  await flush();

  assert.deepEqual(findAll(sheet, (node) => hasClass(node, "mid")).map(text), ["gpt-5"]);
  assert.match(text(sheet), /Không lấy được danh sách model từ OpenAI chính/);
  assert.ok(button(sheet, "Thêm model"));
});

test("an unreadable credential moves focus to the key field with the server hint", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider({ credential_unreadable: true })], kinds: KINDS };
    }
    if (path === "/llm/providers/openai/test" && options.method === "POST") {
      throw new AppAPIError({
        code: "PROVIDER_CREDENTIAL_UNREADABLE",
        message: "OpenAI chính có API key nhưng máy này không giải mã được",
        fields: { credential: "Nhập lại API key" },
        status: 422,
      });
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  button(sheet, "Kiểm tra kết nối").click();
  await flush();

  assert.equal(document.activeElement, keyInput(sheet));
  assert.match(text(sheet), /Nhập lại API key/);
});

test("a manual model id is added through the models endpoint", async (t) => {
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers/openai/models" && options.method === "POST") {
      return {
        models: [
          { model_id: "gpt-5", name: "GPT-5", source: "discovered", available: true },
          { model_id: "o5-preview", name: "o5-preview", source: "manual", available: true },
        ],
      };
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  find(sheet, (node) => hasClass(node, "madd")).value = "o5-preview";
  button(sheet, "Thêm model").click();
  await flush();

  assert.deepEqual(calls.at(-1), {
    path: "/llm/providers/openai/models",
    options: { method: "POST", body: { model_id: "o5-preview", name: "" } },
  });
  assert.deepEqual(
    findAll(sheet, (node) => hasClass(node, "mid")).map(text),
    ["gpt-5", "o5-preview"],
  );
});

test("deleting a provider closes the sheet and reloads the list", async (t) => {
  let listLoads = 0;
  const { calls, main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listLoads++;
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers/openai" && options.method === "DELETE") return null;
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  button(sheet, "Xoá Provider").click();
  await flush();

  assert.deepEqual(calls.find(({ options }) => options.method === "DELETE"), {
    path: "/llm/providers/openai",
    options: { method: "DELETE" },
  });
  assert.equal(find(document.body, (node) => hasClass(node, "provider-sheet")), null);
  assert.equal(listLoads, 2);
});

test("a refused delete is announced through the sheet live region", async (t) => {
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      return { providers: [provider()], kinds: KINDS };
    }
    if (path === "/llm/providers/openai" && options.method === "DELETE") {
      throw new Error("Chuỗi fallback còn dùng OpenAI chính");
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  button(sheet, "Xoá Provider").click();
  await flush();

  const live = find(sheet, (node) => node.getAttribute?.("aria-live") === "polite");
  assert.ok(live);
  assert.match(text(live), /Chuỗi fallback còn dùng OpenAI chính/);
});

test("Claude Code has no credential, save, or delete controls", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openSheet(main, "Claude Code");

  assert.equal(keyInput(sheet), null);
  for (const label of ["Thay khoá", "Xoá khoá", "Xoá Provider", "Lưu", "Kiểm tra kết nối"]) {
    assert.equal(button(sheet, label), null, `Claude Code must not offer "${label}"`);
  }
  assert.ok(button(sheet, "Thêm model"));
});

test("Escape closes the sheet and returns focus to its trigger", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const trigger = button(rowFor(main, "OpenAI chính"), "Sửa");
  await openSheet(main, "OpenAI chính");
  document.dispatchKeydown({ key: "Escape" });

  assert.equal(find(document.body, (node) => hasClass(node, "provider-sheet")), null);
  assert.equal(document.activeElement, trigger);
});

test("Tab wraps inside the sheet instead of escaping it", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");
  const save = button(sheet, "Lưu");
  const name = find(sheet, (node) => node.getAttribute?.("type") === "text");

  save.focus();
  let prevented = 0;
  sheet.dispatchEvent({ type: "keydown", key: "Tab", preventDefault: () => { prevented++ } });
  assert.equal(prevented, 1);
  assert.equal(document.activeElement, name);

  sheet.dispatchEvent({
    type: "keydown",
    key: "Tab",
    shiftKey: true,
    preventDefault: () => { prevented++ },
  });
  assert.equal(prevented, 2);
  assert.equal(document.activeElement, save);
});

test("closing an unchanged sheet restores focus and leaves the list alone", async (t) => {
  let listLoads = 0;
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listLoads++;
      return { providers: [provider()], kinds: KINDS };
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  const trigger = button(rowFor(main, "OpenAI chính"), "Sửa");
  const sheet = await openSheet(main, "OpenAI chính");
  button(sheet, "Đóng").click();
  await flush();

  assert.equal(find(document.body, (node) => hasClass(node, "provider-sheet")), null);
  assert.equal(document.activeElement, trigger);
  assert.equal(listLoads, 1);
});

test("closing a sheet after a model change reloads the row behind it", async (t) => {
  let listLoads = 0;
  const stored = [{ model_id: "gpt-5", name: "GPT-5", source: "discovered", available: true }];
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listLoads++;
      return { providers: [provider({ models: [...stored] })], kinds: KINDS };
    }
    if (path === "/llm/providers/openai/models" && options.method === "POST") {
      stored.push({ model_id: "o5", name: "o5", source: "manual", available: true });
      return { models: [...stored] };
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  assert.match(text(rowFor(main, "OpenAI chính")), /1 model/);

  const sheet = await openSheet(main, "OpenAI chính");
  find(sheet, (node) => hasClass(node, "madd")).value = "o5";
  button(sheet, "Thêm model").click();
  await flush();
  button(sheet, "Đóng").click();
  await flush();

  assert.equal(listLoads, 2);
  assert.match(text(rowFor(main, "OpenAI chính")), /2 model/);
});

// Kiểm tra kết nối KHÔNG đọc gì thêm nhưng máy chủ vẫn ghi lại kết quả kiểm, nên hàng phía sau
// cũng cũ theo — đây là chỗ dễ sót nhất nếu đánh dấu theo từng nút thay vì theo lượt ghi.
test("closing a sheet after a connection test reloads the stale check status", async (t) => {
  let listLoads = 0;
  let status = "";
  const { main } = mountPage(t, (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) {
      listLoads++;
      return { providers: [provider({ last_check_status: status })], kinds: KINDS };
    }
    if (path === "/llm/providers/openai/test" && options.method === "POST") {
      status = "ok";
      return { ok: true };
    }
    if (path === "/llm/providers/openai/discover" && options.method === "POST") {
      return { models: [] };
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  });
  await flush();
  assert.match(text(rowFor(main, "OpenAI chính")), /chưa kiểm tra lần nào/);

  const sheet = await openSheet(main, "OpenAI chính");
  button(sheet, "Kiểm tra kết nối").click();
  await flush();
  button(sheet, "Đóng").click();
  await flush();

  assert.equal(listLoads, 2);
  assert.match(text(rowFor(main, "OpenAI chính")), /đã kiểm, chạy tốt/);
});

test("the edit sheet opens with focus on the name field", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openSheet(main, "OpenAI chính");

  assert.equal(document.activeElement, find(sheet, (node) => node.getAttribute?.("type") === "text"));
});

// Phần tử focus được đầu tiên trên sheet này là nút Xoá của một hàng model; mở hộp thoại ra mà
// con trỏ đã nằm sẵn trên một nút phá là điều không được xảy ra.
test("the Claude Code sheet opens on the model input, not on a delete button", async (t) => {
  const { main } = mountPage(t, listOnly());
  await flush();
  const sheet = await openSheet(main, "Claude Code");

  assert.equal(document.activeElement, find(sheet, (node) => hasClass(node, "madd")));
  assert.notEqual(text(document.activeElement), "Xoá");
});

test("dispose removes an open sheet and its Escape listener", async (t) => {
  const { keys, main, mounted } = mountPage(t, listOnly());
  await flush();
  await openSheet(main, "OpenAI chính");
  assert.equal(keys.listenerCount(), 1);

  mounted.dispose();

  assert.equal(find(document.body, (node) => hasClass(node, "provider-sheet")), null);
  assert.equal(keys.listenerCount(), 0);
});

test("dispose aborts the in-flight provider request", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  const signals = [];
  let resolveList;
  const page = createProvidersPage({
    request: (path, options = {}) => {
      signals.push(options.signal);
      if (path === "/llm/providers") return new Promise((resolve) => { resolveList = resolve; });
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  await flush();

  assert.equal(signals[0].aborted, false);
  mounted.dispose();
  assert.equal(signals[0].aborted, true);
  resolveList({ providers: [provider()], kinds: KINDS });
  await flush();
  assert.equal(find(main, (node) => hasClass(node, "facts")), null);
});

test("a provider list resolving after a newer load never replaces it", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  const pending = [];
  const page = createProvidersPage({
    request: () => new Promise((resolve) => { pending.push(resolve); }),
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  button(main, "Tải lại").click();
  await flush();

  pending[1]({ providers: [provider({ name: "Bản mới" })], kinds: KINDS });
  await flush();
  pending[0]({ providers: [provider({ name: "Bản cũ" })], kinds: KINDS });
  await flush();

  assert.deepEqual(
    findAll(main, (node) => hasClass(node, "fkk")).map(text),
    ["Bản mới"],
  );
});

test("a failed provider list resolving after a newer one never replaces it", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  const pending = [];
  const page = createProvidersPage({
    request: () => new Promise((_resolve, reject) => { pending.push(reject); }),
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  button(main, "Tải lại").click();
  await flush();

  pending[1](new Error("hỏng lần mới"));
  await flush();
  pending[0](new Error("hỏng lần cũ"));
  await flush();

  assert.match(text(find(main, (node) => hasClass(node, "banner"))), /hỏng lần mới/);
});

test("a failed provider list renders the shared error panel", async (t) => {
  const { main } = mountPage(t, () => {
    throw new Error("không đọc được danh sách Provider");
  });
  await flush();

  assert.match(text(find(main, (node) => hasClass(node, "banner"))), /không đọc được danh sách Provider/);
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
