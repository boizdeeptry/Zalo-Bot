import test from "node:test";
import assert from "node:assert/strict";

import { AppAPIError } from "../overlay/internal/webui/static/core/api.js";
import { createCombosPage, createComboService } from "../overlay/internal/webui/static/pages/combos.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, className) => node.classList?.contains(className) ?? false;

function button(root, label) {
  return find(root, (node) => node.tagName === "BUTTON" && text(node) === label);
}

const comboRows = (main) => findAll(main, (node) => hasClass(node, "crow"));
const memberRows = (main) => findAll(main, (node) => hasClass(node, "fact"));
const radioOf = (row) => find(row, (node) => node.getAttribute?.("type") === "radio");
const typeBadge = (row) => text(find(row, (node) => hasClass(node, "ctype")));

// pageNotice: câu thông báo cấp trang (tạo/đổi/xoá) sống trong cột danh sách, tách khỏi note của
// editor để một lượt vẽ lại danh sách không thổi bay nó.
function pageNotice(main) {
  const column = find(main, (node) => hasClass(node, "combos-list"));
  return find(column, (node) => node.getAttribute?.("aria-live") === "polite");
}

// editorNotice: câu save/tải-lại nằm trên note của EDITOR (trong .ceditor), tách khỏi note cấp trang.
function editorNotice(main) {
  const slot = find(main, (node) => hasClass(node, "ceditor"));
  return find(slot, (node) => hasClass(node, "note"));
}

function chainOf(main) {
  return memberRows(main).map((row) => {
    const [provider, model] = findAll(row, (node) => node.tagName === "SELECT");
    return `${provider.value}/${model.value}`;
  });
}

const PROVIDERS = {
  kinds: [],
  providers: [
    {
      id: "openai-1", name: "OpenAI chính", kind: "openai", enabled: true, system: false,
      credential_configured: true, credential_unreadable: false,
      models: [
        { model_id: "gpt-5", name: "GPT-5", source: "discovered", available: true },
        { model_id: "gpt-5-mini", name: "GPT-5 mini", source: "discovered", available: true },
      ],
    },
    {
      id: "gemini", name: "Gemini", kind: "gemini", enabled: true, system: false,
      credential_configured: true, credential_unreadable: false,
      models: [{ model_id: "gemini-2", name: "Gemini 2", source: "discovered", available: true }],
    },
    {
      id: "claude-code", name: "Claude Code", kind: "claude_code", enabled: true, system: true,
      credential_configured: false, credential_unreadable: false,
      models: [{ model_id: "haiku", name: "Haiku", source: "manual", available: true }],
    },
  ],
};

// Một combo fallback đang chạy và một combo round_robin nghỉ — đủ để kiểm huy hiệu kiểu, chấm radio,
// và editor đổi theo combo đang chọn.
const COMBOS = {
  combos: [
    {
      id: "c1", name: "Chính", type: "fallback", active: true, revision: 4,
      entries: [
        { position: 0, provider_id: "openai-1", model_id: "gpt-5", enabled: true },
        { position: 1, provider_id: "claude-code", model_id: "haiku", enabled: true },
      ],
    },
    {
      id: "c2", name: "Luân phiên", type: "round_robin", active: false, revision: 2,
      entries: [
        { position: 0, provider_id: "gemini", model_id: "gemini-2", enabled: true },
      ],
    },
  ],
};

const STATUS = {
  active_provider_id: "openai-1",
  active_model_id: "gpt-5",
  last_success_at: "2026-08-06T04:00:00Z",
  attempts: 12,
  fallbacks: 2,
};

// comboAPI trả một handler cho mọi lượt đọc/ghi combo. Mặc định lượt PUT dội lại đúng thân vừa nhận
// kèm revision mới, lượt POST tạo trả một combo mới nghỉ — như máy chủ thật.
function comboAPI(opts = {}) {
  const providers = opts.providers ?? PROVIDERS;
  const status = opts.status ?? STATUS;
  const combos = opts.combos ?? COMBOS;
  return (path, options = {}) => {
    const method = options.method;
    if (path === "/llm/providers" && !method) return providers;
    if (path === "/llm/status" && !method) return typeof status === "function" ? status() : status;
    if (path === "/llm/combos" && !method) return typeof combos === "function" ? combos() : combos;
    if (path === "/llm/combos" && method === "POST") {
      if (opts.create) return opts.create(options.body);
      return {
        id: "c-new", name: options.body.name, type: options.body.type,
        active: false, revision: 0, entries: [],
      };
    }
    const one = path.match(/^\/llm\/combos\/([^/]+)$/);
    if (one && method === "PUT") {
      if (opts.save) return opts.save(one[1], options.body);
      return {
        id: one[1], name: "Chính", type: options.body.type, active: true,
        revision: options.body.revision + 1,
        entries: options.body.entries.map((entry, position) => ({ position, ...entry })),
      };
    }
    if (one && method === "DELETE") {
      if (opts.remove) return opts.remove(one[1]);
      return null;
    }
    const activate = path.match(/^\/llm\/combos\/([^/]+)\/activate$/);
    if (activate && method === "POST") {
      if (opts.activate) return opts.activate(activate[1]);
      return { ok: true };
    }
    throw new Error(`Unexpected request: ${method || "GET"} ${path}`);
  };
}

function mountPage(t, handler) {
  const dom = installDOM();
  const calls = [];
  const timers = [];
  const cleared = [];
  const page = createCombosPage({
    request: async (path, options = {}) => {
      const { signal, ...recorded } = options;
      calls.push({ path, options: recorded });
      return handler(path, options);
    },
    setInterval: (fn) => { timers.push(fn); return timers.length; },
    clearInterval: (id) => cleared.push(id),
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(() => { mounted.dispose(); dom.restore(); });
  return {
    calls,
    cleared,
    main,
    mounted,
    timerCount: () => timers.length,
    async tick() { for (const fn of timers) fn(); await flush(); },
  };
}

async function mounted(t, handler = comboAPI()) {
  const harness = mountPage(t, handler);
  await flush();
  return harness;
}

// --- combo service: đúng đường và đúng phương thức ---

test("combo service issues the documented paths and methods", async () => {
  const calls = [];
  const service = createComboService(async (path, options = {}) => {
    calls.push({ path, options });
    return {};
  });

  await service.providers();
  await service.status();
  await service.list();
  await service.create({ name: "Thử", type: "fallback" });
  await service.save("c1", {
    revision: 4,
    type: "round_robin",
    entries: [
      { provider_id: "openai-1", model_id: "gpt-5-mini", enabled: true },
      { provider_id: "claude-code", model_id: "haiku", enabled: true },
    ],
  });
  await service.activate("c1");
  await service.remove("c1");

  assert.deepEqual(calls, [
    { path: "/llm/providers", options: {} },
    { path: "/llm/status", options: {} },
    { path: "/llm/combos", options: {} },
    { path: "/llm/combos", options: { method: "POST", body: { name: "Thử", type: "fallback" } } },
    {
      path: "/llm/combos/c1",
      options: {
        method: "PUT",
        body: {
          revision: 4,
          type: "round_robin",
          entries: [
            { provider_id: "openai-1", model_id: "gpt-5-mini", enabled: true },
            { provider_id: "claude-code", model_id: "haiku", enabled: true },
          ],
        },
      },
    },
    { path: "/llm/combos/c1/activate", options: { method: "POST" } },
    { path: "/llm/combos/c1", options: { method: "DELETE" } },
  ]);
});

test("combo service rejects a missing request function", () => {
  assert.throws(() => createComboService(null), TypeError);
});

// --- trang Combos ---

test("Combos renders one row per combo with an active radio and a type badge", async (t) => {
  const { main } = await mounted(t);

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "Combos");
  const rows = comboRows(main);
  assert.equal(rows.length, 2);
  assert.equal(radioOf(rows[0]).checked, true);
  assert.equal(radioOf(rows[1]).checked, false);
  assert.deepEqual(rows.map(typeBadge), ["Fallback", "Round Robin"]);
});

test("the active combo's member editor renders its chain with a running badge", async (t) => {
  const { main } = await mounted(t);

  assert.deepEqual(chainOf(main), ["openai-1/gpt-5", "claude-code/haiku"]);
  assert.equal(text(find(memberRows(main)[0], (node) => hasClass(node, "live"))), "đang chạy");
});

test("selecting a combo swaps the editor to that combo's members without a running badge", async (t) => {
  const { main } = await mounted(t);
  find(comboRows(main)[1], (node) => hasClass(node, "cname")).click();

  assert.deepEqual(chainOf(main), ["gemini/gemini-2"]);
  // c2 không phải combo đang chạy, nên không mắt xích nào của nó đeo huy hiệu dù model có trùng hay không.
  assert.equal(text(find(memberRows(main)[0], (node) => hasClass(node, "live"))), "");
});

test("activating a combo posts to its activate endpoint", async (t) => {
  const { calls, main } = await mounted(t);
  radioOf(comboRows(main)[1]).dispatchEvent({ type: "change" });
  await flush();

  assert.ok(
    calls.some((call) => call.path === "/llm/combos/c2/activate" && call.options.method === "POST"),
    "gating the active radio must POST to the combo's activate endpoint",
  );
});

test("saving the selected combo PUTs to its id with its revision and type", async (t) => {
  const { calls, main } = await mounted(t);
  button(main, "Lưu").click();
  await flush();

  assert.deepEqual(calls.at(-1), {
    path: "/llm/combos/c1",
    options: {
      method: "PUT",
      body: {
        revision: 4,
        type: "fallback",
        entries: [
          { provider_id: "openai-1", model_id: "gpt-5", enabled: true },
          { provider_id: "claude-code", model_id: "haiku", enabled: true },
        ],
      },
    },
  });
});

test("the per-combo type selector travels with the save", async (t) => {
  const { calls, main } = await mounted(t);
  const typeSelect = find(main, (node) => node.getAttribute?.("id") === "combo-type");
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  button(main, "Lưu").click();
  await flush();

  assert.equal(calls.at(-1).options.body.type, "round_robin");
});

test("creating a combo posts name and type to /llm/combos", async (t) => {
  const { calls, main } = await mounted(t);
  const nameInput = find(main, (node) => node.tagName === "INPUT" && node.getAttribute?.("type") === "text");
  nameInput.value = "Thử nghiệm";
  const newType = find(main, (node) => node.getAttribute?.("aria-label") === "Kiểu combo mới");
  newType.value = "round_robin";
  button(main, "Tạo combo").click();
  await flush();

  const post = calls.find((call) => call.path === "/llm/combos" && call.options.method === "POST");
  assert.ok(post, "the create control must POST to /llm/combos");
  assert.deepEqual(post.options.body, { name: "Thử nghiệm", type: "round_robin" });
});

test("creating a combo with no name is refused before it reaches the API", async (t) => {
  const { calls, main } = await mounted(t);
  button(main, "Tạo combo").click();
  await flush();

  assert.equal(calls.some((call) => call.path === "/llm/combos" && call.options.method === "POST"), false);
  assert.match(text(pageNotice(main)), /Đặt tên cho combo mới/);
});

test("a protected delete shows the reason instead of crashing", async (t) => {
  const { main } = await mounted(t, comboAPI({
    remove: () => {
      throw new AppAPIError({
        code: "COMBO_PROTECTED",
        message: "Không xoá được combo đang dùng hoặc combo cuối cùng",
        status: 409,
      });
    },
  }));
  button(comboRows(main)[0], "Xoá").click();
  await flush();

  assert.match(text(pageNotice(main)), /Không xoá được combo đang dùng/);
  assert.equal(comboRows(main).length, 2);
});

test("a revision conflict keeps the draft and offers a reload", async (t) => {
  const { main } = await mounted(t, comboAPI({
    save: () => {
      throw new AppAPIError({
        code: "COMBO_REVISION_CONFLICT",
        message: "Combo đã được lưu ở nơi khác trong lúc bạn đang sửa",
        status: 409,
      });
    },
  }));
  button(main, "Lưu").click();
  await flush();

  assert.match(text(editorNotice(main)), /lưu ở nơi khác/);
  assert.ok(button(main, "Tải lại combo"), "a conflict offers the reload control");
  // Bản nháp còn nguyên: chuỗi hai mắt xích không bị vẽ lại thành trống.
  assert.deepEqual(chainOf(main), ["openai-1/gpt-5", "claude-code/haiku"]);
});

const conflictOnSave = () => {
  throw new AppAPIError({ code: "COMBO_REVISION_CONFLICT", message: "xung đột", status: 409 });
};

test("reloading a combo re-syncs the list rows to the fresh fetch", async (t) => {
  let reads = 0;
  const { main } = await mounted(t, comboAPI({
    save: conflictOnSave,
    combos: () => {
      reads += 1;
      if (reads === 1) return COMBOS;
      // Ai đó đổi kiểu c1 fallback → round_robin ở nơi khác; lượt tải lại phải cập nhật huy hiệu.
      return { combos: [{ ...COMBOS.combos[0], type: "round_robin", revision: 9 }, COMBOS.combos[1]] };
    },
  }));
  button(main, "Lưu").click();
  await flush();
  button(main, "Tải lại combo").click();
  await flush();

  assert.deepEqual(comboRows(main).map(typeBadge), ["Round Robin", "Round Robin"]);
});

test("reloading a combo deleted elsewhere self-corrects instead of throwing", async (t) => {
  let reads = 0;
  const { main } = await mounted(t, comboAPI({
    save: conflictOnSave,
    combos: () => {
      reads += 1;
      if (reads === 1) return COMBOS;
      return { combos: [COMBOS.combos[1]] }; // c1 (đang chọn) đã bị xoá ở nơi khác
    },
  }));
  button(main, "Lưu").click();
  await flush();
  button(main, "Tải lại combo").click();
  await flush();

  assert.equal(comboRows(main).length, 1);
  assert.deepEqual(chainOf(main), ["gemini/gemini-2"]);
  assert.match(text(pageNotice(main)), /đã bị xoá ở nơi khác/);
});

test("a failed load renders the shared error panel", async (t) => {
  const { main } = await mounted(t, () => {
    throw new Error("không đọc được danh sách combo");
  });

  assert.match(text(find(main, (node) => hasClass(node, "banner"))), /không đọc được danh sách combo/);
});

test("dispose stops the status poll", async (t) => {
  const { calls, cleared, mounted: page, tick } = await mounted(t);
  const before = calls.length;
  page.dispose();
  await tick();

  assert.deepEqual(cleared, [1]);
  assert.equal(calls.length, before);
});
