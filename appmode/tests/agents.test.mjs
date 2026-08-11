import test from "node:test";
import assert from "node:assert/strict";

import {
  createAgentService,
  createAgentsPage,
  fillHoles,
  handleQuickFillEnter,
  scanHoles,
} from "../overlay/internal/webui/static/pages/agents.js";
import {
  find,
  findAll,
  installDOM,
  text,
} from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));

function hasClass(node, className) {
  return node.classList?.contains(className) ?? false;
}

test("scanHoles counts uppercase placeholders only", () => {
  assert.deepEqual(
    scanHoles("{{TEN_BOT}} {{TEN_BOT}} {{ten}}"),
    [{ key: "TEN_BOT", count: 2 }],
  );
});

test("editor quick fill intercepts Enter instead of saving the outer form", () => {
  let prevented = 0;
  let applied = 0;
  const handled = handleQuickFillEnter({
    key: "Enter",
    preventDefault: () => { prevented++ },
  }, () => { applied++ });

  assert.equal(handled, true);
  assert.equal(prevented, 1);
  assert.equal(applied, 1);
});

test("editor quick fill replaces repeated roster placeholders locally", () => {
  assert.equal(
    fillHoles("Gọi {{TEN_THANH_VIEN}}; chào {{TEN_THANH_VIEN}}.", { TEN_THANH_VIEN: "Mai" }),
    "Gọi Mai; chào Mai.",
  );
});

test("agent service uses the shared API contract for quick fill and persona saves", async () => {
  const calls = [];
  const request = async (path, options = {}) => {
    calls.push({ path, options });
    return { ready: true, placeholders: [] };
  };
  const service = createAgentService(request);

  await service.load();
  await service.fill({ TEN_BOT: "An Nhiên" });
  await service.fill({ TEN_BOT: "Bé Mi" }, "Bé Mi");
  await service.fill({}, "Tên legacy");
  await service.loadDocument("persona");
  await service.saveDocument("persona", "Giọng Việt — UTF-8");

  assert.deepEqual(calls, [
    { path: "/agent", options: {} },
    { path: "/agent", options: { method: "PUT", body: { values: { TEN_BOT: "An Nhiên" } } } },
    { path: "/agent", options: { method: "PUT", body: { values: { TEN_BOT: "Bé Mi" }, display_name: "Bé Mi" } } },
    { path: "/agent", options: { method: "PUT", body: { values: {}, display_name: "Tên legacy" } } },
    { path: "/agent/persona/persona", options: {} },
    { path: "/agent/persona/persona", options: { method: "PUT", body: { text: "Giọng Việt — UTF-8" } } },
  ]);
});

test("Agent page keeps its quick-fill layout and sends normalized values plus display_name", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const requests = [];
  let loads = 0;
  const page = createAgentsPage({
    request: async (path, options = {}) => {
      requests.push({ path, options });
      if (path === "/agent" && options.method === "PUT") return { ready: true };
      if (path === "/agent") {
        loads++;
        return {
          ready: loads > 1,
          display_name: "",
          placeholders: loads > 1 ? [] : [
            { key: "TEN_BOT", count: 1, sample: "Tên bot từ tệp" },
            { key: "don-vi", count: 2, sample: "Đơn vị {{don-vi}}" },
          ],
          tools: [],
          kb_roots: [],
        };
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();

  const form = find(main, (node) => node.tagName === "FORM");
  const fields = findAll(form, (node) => hasClass(node, "field"));
  const inputs = findAll(form, (node) => node.tagName === "INPUT");
  assert.equal(fields.length, 2);
  assert.equal(inputs.length, 2);
  assert.equal(form.firstElementChild.getAttribute("style"), "max-width:660px");
  assert.equal(find(form, (node) => hasClass(node, "row")).children[0].className, "btn go");
  assert.equal(inputs[0].getAttribute("placeholder"), "Tên bot từ tệp");
  inputs[0].value = "  Bé Mi  ";
  inputs[1].value = "  Công ty Mở  ";
  form.dispatchEvent({ type: "submit" });
  await flush();
  await flush();

  const put = requests.find(({ path, options }) => path === "/agent" && options.method === "PUT");
  assert.deepEqual(put.options.body, {
    values: { TEN_BOT: "Bé Mi", "don-vi": "Công ty Mở" },
    display_name: "Bé Mi",
  });
  assert.equal(Object.hasOwn(put.options.body, "require_complete"), false);
  assert.equal(loads, 2);
});

test("Agent page supports a legacy display-name-only save without a duplicate TEN_BOT input", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const requests = [];
  const page = createAgentsPage({
    request: async (path, options = {}) => {
      requests.push({ path, options });
      if (path === "/agent" && options.method === "PUT") return { ready: true };
      if (path === "/agent") return {
        ready: true,
        display_name: "Tên cũ",
        placeholders: [],
        tools: [],
        kb_roots: [],
      };
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();

  const form = find(main, (node) => node.tagName === "FORM");
  const inputs = findAll(form, (node) => node.tagName === "INPUT");
  assert.equal(inputs.length, 1);
  assert.equal(inputs[0].getAttribute("data-persona-kind"), "display-name");
  assert.equal(inputs[0].value, "Tên cũ");
  inputs[0].value = "  Tên mới  ";
  form.dispatchEvent({ type: "submit" });
  await flush();

  const put = requests.find(({ options }) => options.method === "PUT");
  assert.deepEqual(put.options.body, { values: {}, display_name: "Tên mới" });
});

test("Agent page prevents double save, reports failures, and restores field focus", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  let puts = 0;
  let rejectSave;
  const page = createAgentsPage({
    request: (path, options = {}) => {
      if (path === "/agent" && options.method === "PUT") {
        puts++;
        return new Promise((_resolve, reject) => { rejectSave = reject; });
      }
      if (path === "/agent") return Promise.resolve({
        ready: false,
        display_name: "",
        placeholders: [{ key: "TEN_BOT", count: 1 }],
        tools: [],
        kb_roots: [],
      });
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();

  const form = find(main, (node) => node.tagName === "FORM");
  const input = find(form, (node) => node.tagName === "INPUT");
  const save = find(form, (node) => node.tagName === "BUTTON");
  input.value = "Bé Mi";
  form.dispatchEvent({ type: "submit" });
  form.dispatchEvent({ type: "submit" });
  assert.equal(puts, 1);
  assert.equal(save.disabled, true);

  rejectSave(new Error("máy chủ bận"));
  await flush();
  assert.equal(save.disabled, false);
  assert.equal(document.activeElement, input);
  assert.match(text(form), /không lưu được: máy chủ bận/);
});

test("disposing the Agent page while a save is pending prevents a stale reload", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  let loads = 0;
  let resolveSave;
  const page = createAgentsPage({
    request: (path, options = {}) => {
      if (path === "/agent" && options.method === "PUT") {
        return new Promise((resolve) => { resolveSave = resolve; });
      }
      if (path === "/agent") {
        loads++;
        return Promise.resolve({
          ready: false,
          placeholders: [{ key: "TEN_BOT", count: 1 }],
          tools: [],
          kb_roots: [],
        });
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  await flush();
  const form = find(main, (node) => node.tagName === "FORM");
  find(form, (node) => node.tagName === "INPUT").value = "Bé Mi";
  form.dispatchEvent({ type: "submit" });
  mounted.dispose();
  resolveSave({ ready: true });
  await flush();

  assert.equal(loads, 1);
});
