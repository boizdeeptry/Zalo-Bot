import test from "node:test";
import assert from "node:assert/strict";

import { AppAPIError } from "../overlay/internal/webui/static/core/api.js";
import { createCombosPage, createComboService } from "../overlay/internal/webui/static/pages/combos.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const settle = async () => { await flush(); await flush(); };
const hasClass = (node, className) => node.classList?.contains(className) ?? false;

function button(root, label) {
  return find(root, (node) => node.tagName === "BUTTON" && text(node) === label);
}

const comboRows = (main) => findAll(main, (node) => hasClass(node, "crow"));
const memberRows = (main) => findAll(main, (node) => hasClass(node, "fact"));
const memberLabel = (row) => text(find(row, (node) => hasClass(node, "mv")));
const memberLabels = (main) => memberRows(main).map(memberLabel);
const liveBadge = (row) => text(find(row, (node) => hasClass(node, "live")));
const radioOf = (row) => find(row, (node) => node.getAttribute?.("type") === "radio");
const typeBadge = (row) => text(find(row, (node) => hasClass(node, "ctype")));

// Model picker modal: đắp lên document.body (như overlay của Agents), tra qua data-combos-overlay.
const overlay = () => find(document.body, (node) => node.getAttribute?.("data-combos-overlay") != null);
const pickRows = (root) => findAll(root, (node) => hasClass(node, "pick-model"));
const pickGroups = (root) => findAll(root, (node) => hasClass(node, "pick-group-head"));
const pickSearch = (root) => find(root, (node) => node.tagName === "INPUT");
const pickRow = (root, name) => find(root, (node) => hasClass(node, "pick-model")
  && text(node).includes(name));

const putCalls = (calls) => calls
  .filter((call) => /^\/llm\/combos\/[^/]+$/.test(call.path) && call.options.method === "PUT")
  .map((call) => ({ path: call.path, body: call.options.body }));

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
      id: "claude-code", name: "Claude Code", kind: "claude-code", enabled: true, system: true,
      credential_configured: false, credential_unreadable: false,
      models: [{ model_id: "haiku", name: "Haiku", source: "manual", available: true }],
    },
    {
      id: "anthropic-off", name: "Anthropic nghỉ", kind: "anthropic", enabled: false, system: false,
      credential_configured: true, credential_unreadable: false,
      models: [{ model_id: "claude-4", name: "Claude 4", source: "discovered", available: true }],
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

// --- trang Combos: cột danh sách trái ---

test("Combos renders one row per combo with an active radio and a type badge", async (t) => {
  const { main } = await mounted(t);

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "Combos");
  const rows = comboRows(main);
  assert.equal(rows.length, 2);
  assert.equal(radioOf(rows[0]).checked, true);
  assert.equal(radioOf(rows[1]).checked, false);
  assert.deepEqual(rows.map(typeBadge), ["Fallback", "Round Robin"]);
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

// --- danh sách mắt xích chỉ-đọc (thay ô chọn kép cũ) ---

test("the active combo's member list shows provider·model labels and a running badge", async (t) => {
  const { main } = await mounted(t);

  assert.deepEqual(memberLabels(main), ["OpenAI chính · GPT-5", "Claude Code · Haiku"]);
  assert.equal(liveBadge(memberRows(main)[0]), "đang chạy");
  assert.equal(liveBadge(memberRows(main)[1]), "");
});

test("selecting a combo swaps the member list without a running badge", async (t) => {
  const { main } = await mounted(t);
  find(comboRows(main)[1], (node) => hasClass(node, "cname")).click();

  assert.deepEqual(memberLabels(main), ["Gemini · Gemini 2"]);
  // c2 không phải combo đang chạy, nên không mắt xích nào của nó đeo huy hiệu.
  assert.equal(liveBadge(memberRows(main)[0]), "");
});

// --- model picker modal ---

test("opening the picker lists every connected provider's models grouped by provider", async (t) => {
  const { main } = await mounted(t);
  button(main, "Thêm model").click();

  const modal = overlay();
  assert.ok(modal, "the Thêm model button must open the picker overlay");
  // Chỉ Provider đang bật + có model: OpenAI chính, Gemini, Claude Code. Anthropic nghỉ bị loại.
  assert.deepEqual(pickGroups(modal).map(text), ["OpenAI chính", "Gemini", "Claude Code"]);
  assert.equal(pickRows(modal).length, 4);
});

test("typing in the picker search filters the visible models", async (t) => {
  const { main } = await mounted(t);
  button(main, "Thêm model").click();
  const modal = overlay();
  const search = pickSearch(modal);
  search.value = "gpt";
  search.dispatchEvent({ type: "input" });

  assert.equal(pickRows(modal).length, 2);
  assert.deepEqual(pickGroups(modal).map(text), ["OpenAI chính"]);
});

test("clicking a non-member model adds it and auto-saves carrying the combo revision; the next toggle uses the returned revision", async (t) => {
  const { calls, main } = await mounted(t, comboAPI({
    // revision + 100 chứng minh client nhặt revision TỪ PHẢN HỒI chứ không tự cộng 1.
    save: (id, body) => ({
      id, name: "Chính", type: body.type, active: true,
      revision: body.revision + 100,
      entries: body.entries.map((entry, position) => ({ position, ...entry })),
    }),
  }));
  button(main, "Thêm model").click();
  pickRow(overlay(), "Gemini 2").click();
  await settle();

  assert.ok(memberLabels(main).includes("Gemini · Gemini 2"), "the clicked model becomes a member");
  const first = putCalls(calls).at(-1);
  assert.equal(first.body.revision, 4);
  assert.ok(
    first.body.entries.some((entry) => entry.provider_id === "gemini" && entry.model_id === "gemini-2"),
    "the PUT body must include the new member",
  );

  // Bấm lại vào Gemini 2 (giờ đã là thành viên) → gỡ ra; lượt PUT thứ hai phải mang revision vừa nhận.
  pickRow(overlay(), "Gemini 2").click();
  await settle();

  const second = putCalls(calls).at(-1);
  assert.equal(second.body.revision, 104, "the second save must carry the revision the first save returned");
  assert.equal(memberLabels(main).includes("Gemini · Gemini 2"), false, "clicking a member again removes it");
});

test("a change enqueued while the first save is in flight carries the revision the first save returned", async (t) => {
  // Khác test trên (nó settle() giữa hai lượt nên PUT #1 xong hẳn trước PUT #2). Ở đây GIỮ PUT #1
  // treo lơ lửng rồi mới đổi lần hai — chứng minh saveChain xếp hàng: PUT #2 chờ PUT #1 xong rồi mới
  // bay, và nó mang revision PUT #1 TRẢ VỀ chứ không phải revision gốc đã cũ (không double-fire cũ).
  let release;
  let saves = 0;
  const { calls, main } = await mounted(t, comboAPI({
    save: (id, body) => {
      saves += 1;
      const bump = saves === 1 ? 100 : 1;
      const reply = {
        id, name: "Chính", type: body.type, active: true,
        revision: body.revision + bump,
        entries: body.entries.map((entry, position) => ({ position, ...entry })),
      };
      if (saves === 1) return new Promise((resolve) => { release = () => resolve(reply); });
      return reply;
    },
  }));
  // Đổi #1: xoá mắt xích claude/haiku → PUT #1 bay (revision 4) rồi treo.
  button(memberRows(main)[1], "Xoá").click();
  await flush();
  assert.equal(putCalls(calls).length, 1, "the first change fires its save immediately");

  // Đổi #2 trong lúc PUT #1 CÒN treo: tắt mắt xích còn lại. saveChain phải giữ PUT #2 lại.
  const box = find(memberRows(main)[0], (node) => node.getAttribute?.("type") === "checkbox");
  box.checked = false;
  box.dispatchEvent({ type: "change" });
  await flush();
  assert.equal(putCalls(calls).length, 1, "the second change must wait behind the in-flight save");

  release();
  await settle();

  const puts = putCalls(calls);
  assert.equal(puts.length, 2, "exactly two PUTs — no stale double-fire");
  assert.equal(puts[0].body.revision, 4, "the first save uses the original revision");
  assert.equal(puts[1].body.revision, 104, "the queued save carries the revision the first save returned");
  // PUT #2 gộp cả hai lượt đổi (đã xoá haiku + tắt gpt-5), không phải một bản nháp cũ.
  assert.deepEqual(puts[1].body.entries, [{ provider_id: "openai-1", model_id: "gpt-5", enabled: false }]);
});

test("clicking a model already in the combo removes it and auto-saves", async (t) => {
  const { calls, main } = await mounted(t);
  button(main, "Thêm model").click();
  pickRow(overlay(), "GPT-5").click();
  await settle();

  assert.deepEqual(memberLabels(main), ["Claude Code · Haiku"]);
  const put = putCalls(calls).at(-1);
  assert.equal(put.body.entries.some((entry) => entry.model_id === "gpt-5"), false);
});

test("removing a member from the list auto-saves", async (t) => {
  const { calls, main } = await mounted(t);
  button(memberRows(main)[1], "Xoá").click();
  await settle();

  assert.deepEqual(memberLabels(main), ["OpenAI chính · GPT-5"]);
  const put = putCalls(calls).at(-1);
  assert.deepEqual(put.body.entries, [{ provider_id: "openai-1", model_id: "gpt-5", enabled: true }]);
});

test("reordering a member from the list auto-saves the new order", async (t) => {
  const { calls, main } = await mounted(t);
  button(memberRows(main)[0], "Xuống").click();
  await settle();

  assert.deepEqual(memberLabels(main), ["Claude Code · Haiku", "OpenAI chính · GPT-5"]);
  const put = putCalls(calls).at(-1);
  assert.deepEqual(put.body.entries.map((entry) => entry.model_id), ["haiku", "gpt-5"]);
});

test("toggling a member's enable checkbox auto-saves", async (t) => {
  const { calls, main } = await mounted(t);
  const box = find(memberRows(main)[0], (node) => node.getAttribute?.("type") === "checkbox");
  box.checked = false;
  box.dispatchEvent({ type: "change" });
  await settle();

  const put = putCalls(calls).at(-1);
  assert.equal(put.body.entries[0].enabled, false);
});

test("the per-combo type selector auto-saves on change", async (t) => {
  const { calls, main } = await mounted(t);
  const typeSelect = find(main, (node) => node.getAttribute?.("id") === "combo-type");
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();

  assert.equal(putCalls(calls).at(-1).body.type, "round_robin");
});

// --- CAS revision: tự chữa qua một lượt tải lại, còn kẹt thì mời tải lại tay ---

test("a save conflict re-reads the fresh revision and retries the save once", async (t) => {
  let saves = 0;
  let reads = 0;
  const { calls, main } = await mounted(t, comboAPI({
    combos: () => {
      reads += 1;
      // Lượt đọc đầu (lúc mount) mang revision 4; lượt đọc sau khi xung đột mang 9.
      if (reads <= 1) return COMBOS;
      return { combos: [{ ...COMBOS.combos[0], revision: 9 }, COMBOS.combos[1]] };
    },
    save: (id, body) => {
      saves += 1;
      if (saves === 1) {
        throw new AppAPIError({ code: "COMBO_REVISION_CONFLICT", message: "xung đột", status: 409 });
      }
      return {
        id, name: "Chính", type: body.type, active: true,
        revision: body.revision + 1,
        entries: body.entries.map((entry, position) => ({ position, ...entry })),
      };
    },
  }));
  const typeSelect = find(main, (node) => node.getAttribute?.("id") === "combo-type");
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();

  const puts = putCalls(calls);
  assert.equal(puts.length, 2, "a conflict must trigger exactly one retry");
  assert.equal(puts[0].body.revision, 4, "the first save uses the stale revision");
  assert.equal(puts[1].body.revision, 9, "the retry uses the revision from the re-read");
  assert.equal(button(main, "Tải lại combo"), null, "a self-healed save must not offer the reload control");
});

test("a persistent conflict keeps the members and offers a reload", async (t) => {
  const { main } = await mounted(t, comboAPI({
    save: () => {
      throw new AppAPIError({
        code: "COMBO_REVISION_CONFLICT",
        message: "Combo đã được lưu ở nơi khác trong lúc bạn đang sửa",
        status: 409,
      });
    },
  }));
  const typeSelect = find(main, (node) => node.getAttribute?.("id") === "combo-type");
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();

  assert.match(text(editorNotice(main)), /lưu ở nơi khác/);
  assert.ok(button(main, "Tải lại combo"), "a persistent conflict offers the reload control");
  // Danh sách mắt xích còn nguyên: không bị vẽ lại thành trống.
  assert.deepEqual(memberLabels(main), ["OpenAI chính · GPT-5", "Claude Code · Haiku"]);
});

test("reloading a combo re-syncs the list rows to the fresh fetch", async (t) => {
  let reads = 0;
  const { main } = await mounted(t, comboAPI({
    save: () => {
      throw new AppAPIError({ code: "COMBO_REVISION_CONFLICT", message: "xung đột", status: 409 });
    },
    combos: () => {
      reads += 1;
      if (reads <= 1) return COMBOS;
      // Ai đó đổi kiểu c1 fallback → round_robin ở nơi khác; lượt tải lại phải cập nhật huy hiệu.
      return { combos: [{ ...COMBOS.combos[0], type: "round_robin", revision: 9 }, COMBOS.combos[1]] };
    },
  }));
  const typeSelect = find(main, (node) => node.getAttribute?.("id") === "combo-type");
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();
  button(main, "Tải lại combo").click();
  await settle();

  assert.deepEqual(comboRows(main).map(typeBadge), ["Round Robin", "Round Robin"]);
});

test("clicking reload closes an open model picker", async (t) => {
  const { main } = await mounted(t, comboAPI({
    save: () => {
      throw new AppAPIError({ code: "COMBO_REVISION_CONFLICT", message: "xung đột", status: 409 });
    },
  }));
  // Xung đột dai → hiện nút "Tải lại combo".
  const typeSelect = find(main, (node) => node.getAttribute?.("id") === "combo-type");
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();
  // Mở modal rồi tải lại: tải lại vứt bản nháp nên checkmark của modal đã lỗi thời — modal phải đóng.
  button(main, "Thêm model").click();
  assert.ok(overlay(), "the picker is open before reload");
  button(main, "Tải lại combo").click();
  await settle();

  assert.equal(overlay(), null, "reload must close the open picker");
});

test("a combo deleted elsewhere self-corrects during an auto-save conflict", async (t) => {
  let reads = 0;
  const { main } = await mounted(t, comboAPI({
    save: () => {
      throw new AppAPIError({ code: "COMBO_REVISION_CONFLICT", message: "xung đột", status: 409 });
    },
    combos: () => {
      reads += 1;
      if (reads <= 1) return COMBOS;
      return { combos: [COMBOS.combos[1]] }; // c1 (đang chọn) đã bị xoá ở nơi khác
    },
  }));
  const typeSelect = find(main, (node) => node.getAttribute?.("id") === "combo-type");
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();

  assert.equal(comboRows(main).length, 1);
  assert.deepEqual(memberLabels(main), ["Gemini · Gemini 2"]);
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
