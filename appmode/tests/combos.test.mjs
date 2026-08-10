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

// Danh sách thẻ: mỗi combo là một .ccard (tên + chip model + ô chọn kiểu + Nhân bản/Sửa model/Xoá).
const cards = (main) => findAll(main, (node) => hasClass(node, "ccard"));
const cardName = (card) => text(find(card, (node) => hasClass(node, "cname")));
const cardChips = (card) => findAll(card, (node) => hasClass(node, "cchip")).map(text);
const cardTypeSel = (card) => find(card, (node) => hasClass(node, "ctype-sel"));
const radioOf = (card) => find(card, (node) => node.getAttribute?.("type") === "radio");

// Modal tạo/sửa (data-combos-modal) vs modal chọn model lồng bên trong (data-combos-overlay).
const comboModal = () => find(document.body, (node) => node.getAttribute?.("data-combos-modal") != null);
const overlay = () => find(document.body, (node) => node.getAttribute?.("data-combos-overlay") != null);

// Danh sách mắt xích sống TRONG modal Sửa model; các helper nhận root là modal đó.
const memberRows = (root) => findAll(root, (node) => hasClass(node, "fact"));
const memberLabel = (row) => text(find(row, (node) => hasClass(node, "mv")));
const memberLabels = (root) => memberRows(root).map(memberLabel);
const liveBadge = (row) => text(find(row, (node) => hasClass(node, "live")));
const editorType = (root) => find(root, (node) => node.getAttribute?.("id") === "combo-type");

const pickRows = (root) => findAll(root, (node) => hasClass(node, "pick-model"));
const pickGroups = (root) => findAll(root, (node) => hasClass(node, "pick-group-head"));
const pickSearch = (root) => find(root, (node) => node.tagName === "INPUT");
const pickRow = (root, name) => find(root, (node) => hasClass(node, "pick-model")
  && text(node).includes(name));
const pickNote = (root) => find(root, (node) => hasClass(node, "pick-note"));

const putCalls = (calls) => calls
  .filter((call) => /^\/llm\/combos\/[^/]+$/.test(call.path) && call.options.method === "PUT")
  .map((call) => ({ path: call.path, body: call.options.body }));

// pageNotice: câu thông báo cấp trang (tạo/đổi/xoá) sống trong thanh trên (.ctop).
function pageNotice(main) {
  const top = find(main, (node) => hasClass(node, "ctop"));
  return find(top, (node) => node.getAttribute?.("aria-live") === "polite");
}

// editorNotice: câu save/tải-lại nằm trên note của EDITOR, giờ ở trong modal Sửa model.
function editorNotice() {
  return find(comboModal(), (node) => hasClass(node, "note")
    && node.getAttribute?.("aria-live") === "polite");
}

// openEdit mở modal Sửa model của một thẻ rồi trả về modal đó.
function openEdit(main, index) {
  button(cards(main)[index], "Sửa model").click();
  return comboModal();
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
      // Chưa nối (API, chưa có credential) + đã tắt → bị loại khỏi picker và KHÔNG nằm trong ghi chú
      // (hàng đã tắt thì không nhắc). Chứng minh bộ lọc mới xét "đã nối", không phải "enabled".
      id: "anthropic-off", name: "Anthropic nghỉ", kind: "anthropic", enabled: false, system: false,
      credential_configured: false, credential_unreadable: false,
      models: [{ model_id: "claude-4", name: "Claude 4", source: "discovered", available: true }],
    },
  ],
};

// Một combo fallback đang chạy và một combo round_robin nghỉ — đủ để kiểm huy hiệu kiểu, chấm radio,
// và modal Sửa model đổi theo combo được mở.
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

// --- trang Combos: danh sách thẻ ---

test("Combos renders one card per combo with an active radio, chips, and a type selector", async (t) => {
  const { main } = await mounted(t);

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "Combos");
  const list = cards(main);
  assert.equal(list.length, 2);
  assert.deepEqual(list.map(cardName), ["Chính", "Luân phiên"]);
  assert.equal(radioOf(list[0]).checked, true);
  assert.equal(radioOf(list[1]).checked, false);
  assert.deepEqual(list.map((card) => cardTypeSel(card).value), ["fallback", "round_robin"]);
  // Chip model hiện provider·model, không cần mở modal.
  assert.deepEqual(cardChips(list[0]), ["OpenAI chính · GPT-5", "Claude Code · Haiku"]);
  assert.deepEqual(cardChips(list[1]), ["Gemini · Gemini 2"]);
});

test("activating a combo posts to its activate endpoint", async (t) => {
  const { calls, main } = await mounted(t);
  radioOf(cards(main)[1]).dispatchEvent({ type: "change" });
  await flush();

  assert.ok(
    calls.some((call) => call.path === "/llm/combos/c2/activate" && call.options.method === "POST"),
    "gating the active radio must POST to the combo's activate endpoint",
  );
});

test("changing a card's type selector saves the new type", async (t) => {
  const { calls, main } = await mounted(t);
  const sel = cardTypeSel(cards(main)[0]);
  sel.value = "round_robin";
  sel.dispatchEvent({ type: "change" });
  await settle();

  const put = putCalls(calls).at(-1);
  assert.equal(put.path, "/llm/combos/c1");
  assert.equal(put.body.type, "round_robin");
  assert.equal(put.body.revision, 4, "the card save carries the combo's current revision");
});

// --- modal Tạo combo ---

test("creating a combo posts name and type from the create modal", async (t) => {
  const { calls, main } = await mounted(t);
  button(main, "Tạo combo").click();
  const modal = comboModal();
  assert.ok(modal, "the Tạo combo button must open the create modal");

  const nameInput = find(modal, (node) => node.tagName === "INPUT" && node.getAttribute?.("type") === "text");
  nameInput.value = "Thử nghiệm";
  const newType = find(modal, (node) => node.getAttribute?.("aria-label") === "Kiểu combo mới");
  newType.value = "round_robin";
  button(modal, "Tạo").click();
  await flush();

  const post = calls.find((call) => call.path === "/llm/combos" && call.options.method === "POST");
  assert.ok(post, "the create control must POST to /llm/combos");
  assert.deepEqual(post.options.body, { name: "Thử nghiệm", type: "round_robin" });
});

test("creating a combo with no name is refused before it reaches the API", async (t) => {
  const { calls, main } = await mounted(t);
  button(main, "Tạo combo").click();
  const modal = comboModal();
  button(modal, "Tạo").click();
  await flush();

  assert.equal(calls.some((call) => call.path === "/llm/combos" && call.options.method === "POST"), false);
  assert.match(text(find(modal, (node) => hasClass(node, "note"))), /Đặt tên cho combo mới/);
});

test("creating a combo with models added in the modal creates then saves those members", async (t) => {
  const { calls, main } = await mounted(t);
  button(main, "Tạo combo").click();
  const modal = comboModal();
  find(modal, (node) => node.tagName === "INPUT" && node.getAttribute?.("type") === "text").value = "Combo có model";

  // Add Model từ TRONG modal Tạo — mở cùng picker (data-combos-overlay) như editor.
  button(modal, "Thêm model").click();
  const picker = overlay();
  assert.ok(picker, "the create modal's Thêm model must open the shared picker");
  pickRow(picker, "GPT-5").click();
  pickRow(picker, "Gemini 2").click();
  // Model đã thêm hiện trong danh sách nháp của modal Tạo (chưa gọi API — chỉ create mới gửi).
  assert.equal(putCalls(calls).length, 0, "adding a model in the create modal must not save before Tạo");

  button(modal, "Tạo").click();
  await settle();

  const post = calls.find((call) => call.path === "/llm/combos" && call.options.method === "POST");
  assert.deepEqual(post.options.body, { name: "Combo có model", type: "fallback" });
  const put = putCalls(calls).find((call) => call.path === "/llm/combos/c-new");
  assert.ok(put, "creating with models must save the chosen members onto the new combo");
  assert.deepEqual(put.body.entries, [
    { provider_id: "openai-1", model_id: "gpt-5", enabled: true },
    { provider_id: "gemini", model_id: "gemini-2", enabled: true },
  ]);
});

test("the active or only combo's delete button is disabled with a reason", async (t) => {
  const { main } = await mounted(t);
  const activeDel = button(cards(main)[0], "Xoá"); // c1 đang dùng
  assert.equal(activeDel.disabled, true, "the active combo can't be deleted");
  assert.match(activeDel.getAttribute("title") || "", /không xoá được/);
  // c2 (không active, còn >1 combo) thì xoá được.
  assert.equal(button(cards(main)[1], "Xoá").disabled, false);
});

test("a protected delete surfaced by the server shows the reason instead of crashing", async (t) => {
  const { main } = await mounted(t, comboAPI({
    remove: () => {
      throw new AppAPIError({
        code: "COMBO_PROTECTED",
        message: "Không xoá được combo đang dùng hoặc combo cuối cùng",
        status: 409,
      });
    },
  }));
  // c2 xoá được ở client, nhưng máy chủ vẫn có thể chặn (đua trạng thái) — Portal phải báo, không sập.
  button(cards(main)[1], "Xoá").click();
  await flush();

  assert.match(text(pageNotice(main)), /Không xoá được combo đang dùng/);
  assert.equal(cards(main).length, 2);
});

// --- nhân bản ---

test("copying a combo creates a duplicate then saves its members", async (t) => {
  const { calls, main } = await mounted(t);
  button(cards(main)[0], "Nhân bản").click();
  await settle();

  const post = calls.find((call) => call.path === "/llm/combos" && call.options.method === "POST");
  assert.deepEqual(post.options.body, { name: "Chính (bản sao)", type: "fallback" });
  const put = putCalls(calls).find((call) => call.path === "/llm/combos/c-new");
  assert.ok(put, "the copy must save the source combo's members onto the new combo");
  assert.deepEqual(put.body.entries, [
    { provider_id: "openai-1", model_id: "gpt-5", enabled: true },
    { provider_id: "claude-code", model_id: "haiku", enabled: true },
  ]);
});

// --- modal Sửa model: danh sách mắt xích ---

test("the active combo's edit modal shows provider·model labels and a running badge", async (t) => {
  const { main } = await mounted(t);
  const modal = openEdit(main, 0);

  assert.deepEqual(memberLabels(modal), ["OpenAI chính · GPT-5", "Claude Code · Haiku"]);
  assert.equal(liveBadge(memberRows(modal)[0]), "đang chạy");
  assert.equal(liveBadge(memberRows(modal)[1]), "");
});

test("opening a non-active combo's edit modal shows its members without a running badge", async (t) => {
  const { main } = await mounted(t);
  const modal = openEdit(main, 1);

  assert.deepEqual(memberLabels(modal), ["Gemini · Gemini 2"]);
  assert.equal(liveBadge(memberRows(modal)[0]), "");
});

// --- model picker modal (lồng trong modal Sửa model) ---

test("the picker offers only connected providers' models and names the unconnected ones", async (t) => {
  const { main } = await mounted(t);
  const modal = openEdit(main, 0);
  button(modal, "Thêm model").click();

  const picker = overlay();
  assert.ok(picker, "the Thêm model button must open the picker overlay");
  // CHỈ Provider đã kết nối + có model: OpenAI chính, Gemini. claude-code (thuê bao hệ thống, 0
  // account) CHƯA nối → bị loại dù enabled=1 — đúng lỗi no-default cần chặn (không chào model seed
  // của một provider bot không route tới được). anthropic-off (chưa credential + đã tắt) cũng bị loại.
  assert.deepEqual(pickGroups(picker).map(text), ["OpenAI chính", "Gemini"]);
  assert.equal(pickRows(picker).length, 3);
  // Ghi chú nêu tên Provider bật-nhưng-chưa-nối để người dùng biết vì sao model của nó vắng mặt.
  const note = pickNote(picker);
  assert.ok(note, "an unconnected enabled provider must be named in a note");
  assert.match(text(note), /Chưa kết nối:\s*Claude Code/);
  assert.match(text(note), /Providers/);
  // anthropic-off đã tắt → không bị nhắc trong ghi chú.
  assert.equal(text(note).includes("Anthropic"), false);
});

test("the picker offers a connected subscription provider's models and drops the note when all are connected", async (t) => {
  // Thuê bao codex CÓ account đang bật (0 credential) → ĐÃ nối → model của nó chọn được. Không có
  // provider bật-mà-chưa-nối nào khác → không hiện ghi chú.
  const providers = {
    kinds: [],
    providers: [{
      id: "codex", name: "OpenAI Codex", kind: "codex", enabled: true, system: false,
      credential_configured: false, credential_unreadable: false,
      accounts: [{ id: "a1", label: "Tài khoản 1", email: "", enabled: true }],
      models: [{ model_id: "gpt-5-codex", name: "GPT-5 Codex", source: "manual", available: true }],
    }],
  };
  const { main } = await mounted(t, comboAPI({ providers }));
  const modal = openEdit(main, 0);
  button(modal, "Thêm model").click();

  const picker = overlay();
  assert.deepEqual(pickGroups(picker).map(text), ["OpenAI Codex"]);
  assert.ok(pickRow(picker, "GPT-5 Codex"), "a connected subscription provider's model is pickable");
  assert.equal(pickNote(picker), null, "no note when every enabled provider is connected");
});

test("typing in the picker search filters the visible models", async (t) => {
  const { main } = await mounted(t);
  const modal = openEdit(main, 0);
  button(modal, "Thêm model").click();
  const picker = overlay();
  const search = pickSearch(picker);
  search.value = "gpt";
  search.dispatchEvent({ type: "input" });

  assert.equal(pickRows(picker).length, 2);
  assert.deepEqual(pickGroups(picker).map(text), ["OpenAI chính"]);
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
  const modal = openEdit(main, 0);
  button(modal, "Thêm model").click();
  pickRow(overlay(), "Gemini 2").click();
  await settle();

  assert.ok(memberLabels(modal).includes("Gemini · Gemini 2"), "the clicked model becomes a member");
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
  assert.equal(memberLabels(modal).includes("Gemini · Gemini 2"), false, "clicking a member again removes it");
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
  const modal = openEdit(main, 0);
  // Đổi #1: xoá mắt xích claude/haiku → PUT #1 bay (revision 4) rồi treo.
  button(memberRows(modal)[1], "Xoá").click();
  await flush();
  assert.equal(putCalls(calls).length, 1, "the first change fires its save immediately");

  // Đổi #2 trong lúc PUT #1 CÒN treo: tắt mắt xích còn lại. saveChain phải giữ PUT #2 lại.
  const box = find(memberRows(modal)[0], (node) => node.getAttribute?.("type") === "checkbox");
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
  const modal = openEdit(main, 0);
  button(modal, "Thêm model").click();
  pickRow(overlay(), "GPT-5").click();
  await settle();

  assert.deepEqual(memberLabels(modal), ["Claude Code · Haiku"]);
  const put = putCalls(calls).at(-1);
  assert.equal(put.body.entries.some((entry) => entry.model_id === "gpt-5"), false);
});

test("removing a member from the list auto-saves", async (t) => {
  const { calls, main } = await mounted(t);
  const modal = openEdit(main, 0);
  button(memberRows(modal)[1], "Xoá").click();
  await settle();

  assert.deepEqual(memberLabels(modal), ["OpenAI chính · GPT-5"]);
  const put = putCalls(calls).at(-1);
  assert.deepEqual(put.body.entries, [{ provider_id: "openai-1", model_id: "gpt-5", enabled: true }]);
});

test("reordering a member from the list auto-saves the new order", async (t) => {
  const { calls, main } = await mounted(t);
  const modal = openEdit(main, 0);
  button(memberRows(modal)[0], "Xuống").click();
  await settle();

  assert.deepEqual(memberLabels(modal), ["Claude Code · Haiku", "OpenAI chính · GPT-5"]);
  const put = putCalls(calls).at(-1);
  assert.deepEqual(put.body.entries.map((entry) => entry.model_id), ["haiku", "gpt-5"]);
});

test("toggling a member's enable checkbox auto-saves", async (t) => {
  const { calls, main } = await mounted(t);
  const modal = openEdit(main, 0);
  const box = find(memberRows(modal)[0], (node) => node.getAttribute?.("type") === "checkbox");
  box.checked = false;
  box.dispatchEvent({ type: "change" });
  await settle();

  const put = putCalls(calls).at(-1);
  assert.equal(put.body.entries[0].enabled, false);
});

test("the editor's type selector inside the modal auto-saves on change", async (t) => {
  const { calls, main } = await mounted(t);
  const modal = openEdit(main, 0);
  const typeSelect = editorType(modal);
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
  const modal = openEdit(main, 0);
  const typeSelect = editorType(modal);
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();

  const puts = putCalls(calls);
  assert.equal(puts.length, 2, "a conflict must trigger exactly one retry");
  assert.equal(puts[0].body.revision, 4, "the first save uses the stale revision");
  assert.equal(puts[1].body.revision, 9, "the retry uses the revision from the re-read");
  assert.equal(button(comboModal(), "Tải lại combo"), null, "a self-healed save must not offer the reload control");
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
  const modal = openEdit(main, 0);
  const typeSelect = editorType(modal);
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();

  assert.match(text(editorNotice()), /lưu ở nơi khác/);
  assert.ok(button(comboModal(), "Tải lại combo"), "a persistent conflict offers the reload control");
  // Danh sách mắt xích còn nguyên: không bị vẽ lại thành trống.
  assert.deepEqual(memberLabels(comboModal()), ["OpenAI chính · GPT-5", "Claude Code · Haiku"]);
});

test("reloading a combo re-syncs the card list to the fresh fetch", async (t) => {
  let reads = 0;
  const { main } = await mounted(t, comboAPI({
    save: () => {
      throw new AppAPIError({ code: "COMBO_REVISION_CONFLICT", message: "xung đột", status: 409 });
    },
    combos: () => {
      reads += 1;
      if (reads <= 1) return COMBOS;
      // Ai đó đổi kiểu c1 fallback → round_robin ở nơi khác; lượt tải lại phải cập nhật thẻ.
      return { combos: [{ ...COMBOS.combos[0], type: "round_robin", revision: 9 }, COMBOS.combos[1]] };
    },
  }));
  const modal = openEdit(main, 0);
  const typeSelect = editorType(modal);
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();
  button(comboModal(), "Tải lại combo").click();
  await settle();

  assert.deepEqual(cards(main).map((card) => cardTypeSel(card).value), ["round_robin", "round_robin"]);
});

test("clicking reload closes an open model picker", async (t) => {
  const { main } = await mounted(t, comboAPI({
    save: () => {
      throw new AppAPIError({ code: "COMBO_REVISION_CONFLICT", message: "xung đột", status: 409 });
    },
  }));
  const modal = openEdit(main, 0);
  // Xung đột dai → hiện nút "Tải lại combo".
  const typeSelect = editorType(modal);
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();
  // Mở picker rồi tải lại: tải lại vứt bản nháp nên checkmark của picker đã lỗi thời — picker phải đóng.
  button(comboModal(), "Thêm model").click();
  assert.ok(overlay(), "the picker is open before reload");
  button(comboModal(), "Tải lại combo").click();
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
      return { combos: [COMBOS.combos[1]] }; // c1 (đang sửa) đã bị xoá ở nơi khác
    },
  }));
  const modal = openEdit(main, 0);
  const typeSelect = editorType(modal);
  typeSelect.value = "round_robin";
  typeSelect.dispatchEvent({ type: "change" });
  await settle();

  assert.equal(cards(main).length, 1, "the deleted combo drops out of the card list");
  assert.equal(cardName(cards(main)[0]), "Luân phiên");
  assert.equal(comboModal(), null, "the edit modal closes when its combo is deleted elsewhere");
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
