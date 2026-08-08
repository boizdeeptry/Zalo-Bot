import test from "node:test";
import assert from "node:assert/strict";

import {
  LLM_ERROR_FIX,
  addEntry,
  canMove,
  createMemberEditor,
  entryWarning,
  failureText,
  modelChoices,
  moveEntry,
  patchEntry,
  providerChoices,
  removeEntry,
  snapshotOf,
  statusText,
  tallyText,
} from "../overlay/internal/webui/static/pages/route-editor.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, className) => node.classList?.contains(className) ?? false;

// --- các hàm thuần: kiểm thẳng không qua DOM ---

const link = (providerID, modelID, enabled = true) => ({
  provider_id: providerID,
  model_id: modelID,
  enabled,
});
const CLAUDE_TAIL = link("claude-code", "haiku");

test("removeEntry drops the final link too", () => {
  const entries = [link("openai-1", "gpt-5"), CLAUDE_TAIL];
  assert.deepEqual(removeEntry(entries, 1), [link("openai-1", "gpt-5")]);
});

test("removeEntry drops any other link", () => {
  assert.deepEqual(
    removeEntry([link("openai-1", "gpt-5"), CLAUDE_TAIL], 0),
    [CLAUDE_TAIL],
  );
});

test("removeEntry leaves an out-of-range index untouched", () => {
  const entries = [link("openai-1", "gpt-5"), CLAUDE_TAIL];
  assert.equal(removeEntry(entries, 5), entries);
});

test("moveEntry swaps the final link with the one above it", () => {
  const entries = [link("openai-1", "gpt-5"), CLAUDE_TAIL];
  assert.deepEqual(moveEntry(entries, 1, -1), [CLAUDE_TAIL, link("openai-1", "gpt-5")]);
});

test("moveEntry pushes a link down past a Claude Code link", () => {
  const entries = [link("openai-1", "gpt-5"), CLAUDE_TAIL];
  assert.deepEqual(moveEntry(entries, 0, 1), [CLAUDE_TAIL, link("openai-1", "gpt-5")]);
});

test("moveEntry refuses to move off either end of the chain", () => {
  const entries = [link("openai-1", "gpt-5"), link("gemini", "gemini-2"), CLAUDE_TAIL];
  assert.deepEqual(moveEntry(entries, 0, -1), entries);
  assert.deepEqual(moveEntry(entries, 5, 1), entries);
});

test("moveEntry swaps neighbours without touching the original array", () => {
  const first = link("openai-1", "gpt-5");
  const second = link("gemini", "gemini-2");
  const entries = [first, second, CLAUDE_TAIL];

  assert.deepEqual(moveEntry(entries, 0, 1), [second, first, CLAUDE_TAIL]);
  assert.deepEqual(entries, [first, second, CLAUDE_TAIL]);
});

test("addEntry appends to the chain without touching the original array", () => {
  const fresh = link("gemini", "gemini-2");
  const entries = [link("openai-1", "gpt-5")];

  assert.deepEqual(addEntry(entries, fresh), [link("openai-1", "gpt-5"), fresh]);
  assert.deepEqual(entries, [link("openai-1", "gpt-5")]);
});

test("addEntry appends after a Claude Code link too", () => {
  const fresh = link("gemini", "gemini-2");
  assert.deepEqual(
    addEntry([link("openai-1", "gpt-5"), CLAUDE_TAIL], fresh),
    [link("openai-1", "gpt-5"), CLAUDE_TAIL, fresh],
  );
});

test("patchEntry updates one entry and keeps the others by reference", () => {
  const entries = [link("openai-1", "gpt-5"), CLAUDE_TAIL];
  const next = patchEntry(entries, 0, { model_id: "gpt-5-mini" });

  assert.deepEqual(next, [link("openai-1", "gpt-5-mini"), CLAUDE_TAIL]);
  assert.deepEqual(entries, [link("openai-1", "gpt-5"), CLAUDE_TAIL]);
  assert.equal(next[1], entries[1], "the untouched entry must not be rebuilt");
});

test("snapshotOf coerces server entries and defaults a missing revision", () => {
  const snapshot = snapshotOf({
    revision: 7,
    entries: [{ position: 0, provider_id: "openai-1", model_id: "gpt-5", enabled: true }],
  });

  assert.deepEqual(snapshot, {
    revision: 7,
    entries: [{ provider_id: "openai-1", model_id: "gpt-5", enabled: true }],
  });
  assert.deepEqual(snapshotOf(null), { revision: 0, entries: [] });
});

test("canMove only refuses the two ends of the chain", () => {
  const entries = [link("a", "1"), link("b", "2"), link("c", "3")];
  assert.equal(canMove(entries, 0, -1), false);
  assert.equal(canMove(entries, 2, 1), false);
  assert.equal(canMove(entries, 0, 1), true);
  assert.equal(canMove(entries, 1, -1), true);
});

// --- lựa chọn dựng từ danh sách Provider: giữ lựa chọn không còn hợp lệ thay vì lặng lẽ đổi ---

const PROVIDERS = [
  {
    id: "openai-1", name: "OpenAI chính", enabled: true,
    models: [
      { model_id: "gpt-5", name: "GPT-5", available: true },
      { model_id: "gpt-5-mini", name: "GPT-5 mini", available: true },
    ],
  },
  { id: "gemini", name: "Gemini", enabled: true, models: [{ model_id: "gemini-2", name: "Gemini 2", available: true }] },
  { id: "anthropic-1", name: "Anthropic dự phòng", enabled: false, models: [{ model_id: "claude-4", name: "Claude 4", available: true }] },
  {
    id: "claude-code", name: "Claude Code", enabled: true,
    models: [
      { model_id: "haiku", name: "Haiku", available: true },
      { model_id: "sonnet", name: "Sonnet", available: true },
    ],
  },
];
const idsOf = (choices) => choices.map((choice) => choice.id);

test("providerChoices offers only enabled providers", () => {
  assert.deepEqual(idsOf(providerChoices(PROVIDERS, "openai-1")), ["openai-1", "gemini", "claude-code"]);
});

test("providerChoices keeps a link pointed at a now-disabled provider, flagged off", () => {
  const choices = providerChoices(PROVIDERS, "anthropic-1");
  assert.equal(choices[0].id, "anthropic-1");
  assert.match(choices[0].label, /đang tắt/);
});

test("modelChoices offers only available models", () => {
  assert.deepEqual(idsOf(modelChoices(PROVIDERS, "openai-1", "gpt-5")), ["gpt-5", "gpt-5-mini"]);
});

test("modelChoices keeps a model the provider no longer offers, flagged", () => {
  const choices = modelChoices(PROVIDERS, "openai-1", "gpt-4-legacy");
  assert.equal(choices[0].id, "gpt-4-legacy");
  assert.match(choices[0].label, /không còn dùng được/);
});

test("entryWarning names each way a link can be broken, and stays silent when healthy", () => {
  assert.match(entryWarning(PROVIDERS, link("anthropic-1", "claude-4")), /đang tắt — chuỗi bỏ qua/);
  assert.match(entryWarning(PROVIDERS, link("openai-1", "")), /Chưa chọn model/);
  assert.match(entryWarning(PROVIDERS, link("openai-1", "gpt-4-legacy")), /gpt-4-legacy không còn dùng được/);
  assert.equal(entryWarning(PROVIDERS, link("openai-1", "gpt-5")), "");
});

// --- câu trạng thái: hàm thuần sinh chữ, không đọc gì từ ngoài ---

const nameOf = (id) => PROVIDERS.find((provider) => provider.id === id)?.name || id;

test("failureText names the provider that failed and its fix", () => {
  const line = failureText({ last_error_kind: "credential", last_error_provider_id: "openai-1" }, nameOf);
  assert.match(line, /OpenAI chính \(credential\)/);
  assert.match(line, /nhập lại ở mục Providers/i);
});

test("failureText still names the provider for an error kind it does not know", () => {
  const line = failureText({ last_error_kind: "canceled", last_error_provider_id: "gemini" }, nameOf);
  assert.match(line, /Gemini \(canceled\)/);
  assert.match(line, /Xem log Runtime/);
});

test("failureText is empty when there is no last error", () => {
  assert.equal(failureText({}, nameOf), "");
});

test("LLM_ERROR_FIX says a policy refusal stops the chain", () => {
  assert.match(LLM_ERROR_FIX.policy, /DỪNG/);
});

test("tallyText counts the telemetry window, and is empty with no attempts", () => {
  assert.equal(tallyText({ attempts: 12, fallbacks: 2 }), "12 lượt gần đây, 2 lần né.");
  assert.equal(tallyText({ attempts: 0 }), "");
});

test("statusText is empty before the first call and explains an idle chain", () => {
  assert.equal(statusText(null, nameOf), "");
  assert.match(statusText({ active_provider_id: "", attempts: 0 }, nameOf), /Chưa có lượt gọi nào/);
});

// --- createMemberEditor: các CỬA CHẶN hành vi, kiểm thẳng qua DOM ---
//
// Editor là cùng cỗ máy trang Models cũ dùng, chỉ đóng gói theo một combo. Các bài dưới canh đúng
// những hành vi mà comment trong mã gọi là "lặng lẽ làm sai người dùng": huy hiệu dán nhầm hàng,
// lượt sửa lúc đang lưu bị nuốt, con trỏ rơi sau khi dời, lỗi trạng thái bị xoá oan.

const SNAPSHOT = {
  revision: 4,
  entries: [link("openai-1", "gpt-5"), link("claude-code", "haiku")],
};
const RUNNING = { active_provider_id: "openai-1", active_model_id: "gpt-5", last_success_at: "2026-08-06T04:00:00Z", attempts: 12, fallbacks: 2 };

function mountEditor(t, opts = {}) {
  const dom = installDOM();
  const saves = [];
  const editor = createMemberEditor({
    providers: opts.providers ?? PROVIDERS,
    snapshot: opts.snapshot ?? SNAPSHOT,
    type: opts.type ?? "fallback",
    live: opts.live ?? false,
    save: opts.save ?? ((payload) => {
      saves.push(payload);
      return { revision: payload.revision + 1, type: payload.type, entries: payload.entries };
    }),
    reload: opts.reload,
    onSaved: opts.onSaved ?? (() => {}),
  });
  const main = document.createElement("main");
  main.append(editor.node);
  t.after(() => { editor.dispose(); dom.restore(); });
  return { editor, main, saves };
}

const rows = (main) => findAll(main, (node) => hasClass(node, "fact"));
const selects = (row) => findAll(row, (node) => node.tagName === "SELECT");
const badge = (row) => text(find(row, (node) => hasClass(node, "live")));
const warning = (row) => text(find(row, (node) => hasClass(node, "fn")));
const note = (main) => find(main, (node) => hasClass(node, "note"));
const statusLine = (main) => find(main, (node) => hasClass(node, "chainstatus"));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON" && text(node) === label);
function choose(select, value) {
  select.value = value;
  select.dispatchEvent({ type: "change" });
}

test("the running badge sits on the matching link and leaves one repointed elsewhere", (t) => {
  const { main, editor } = mountEditor(t, { live: true });
  editor.setStatus(RUNNING);
  assert.equal(badge(rows(main)[0]), "đang chạy");
  assert.equal(badge(rows(main)[1]), "");

  // Huy hiệu tra bản nháp theo VỊ TRÍ: repoint hàng 0 sang provider khác thì nó phải rời hàng đó,
  // không dính lại theo một bản sao provider/model của lúc vẽ.
  choose(selects(rows(main)[0])[0], "gemini");
  assert.equal(badge(rows(main)[0]), "");
});

test("a link on a disabled provider keeps its choice with a step-over warning", (t) => {
  const { main } = mountEditor(t, {
    snapshot: { revision: 4, entries: [link("anthropic-1", "claude-4"), link("claude-code", "haiku")] },
  });
  const row = rows(main)[0];
  assert.equal(selects(row)[0].value, "anthropic-1");
  assert.match(warning(row), /Anthropic dự phòng đang tắt — chuỗi bỏ qua/);
});

test("a link on a dropped model keeps it with a warning instead of switching", (t) => {
  const { main } = mountEditor(t, {
    snapshot: { revision: 4, entries: [link("openai-1", "gpt-4-legacy"), link("claude-code", "haiku")] },
  });
  const row = rows(main)[0];
  assert.equal(selects(row)[1].value, "gpt-4-legacy");
  assert.match(warning(row), /gpt-4-legacy không còn dùng được/);
});

test("saving refuses a chain with nothing enabled before it calls save", (t) => {
  const { main, saves } = mountEditor(t, {
    snapshot: { revision: 4, entries: [link("openai-1", "gpt-5", false)] },
  });
  button(main, "Lưu").click();

  assert.equal(saves.length, 0);
  assert.match(text(note(main)), /ít nhất một mắt xích đang bật/);
});

test("saving refuses a link with no model chosen before it calls save", (t) => {
  const { main, saves } = mountEditor(t, {
    snapshot: { revision: 4, entries: [link("openai-1", ""), link("claude-code", "haiku")] },
  });
  button(main, "Lưu").click();

  assert.equal(saves.length, 0);
  assert.match(text(note(main)), /Chọn model cho OpenAI chính/);
});

test("an edit made while the save is in flight is not thrown away", async (t) => {
  let release = () => {};
  const saves = [];
  const { main } = mountEditor(t, {
    save: (payload) => {
      saves.push(payload);
      if (saves.length === 1) {
        return new Promise((resolve) => { release = () => resolve({ revision: 5, entries: payload.entries }); });
      }
      return { revision: 6, entries: payload.entries };
    },
  });
  button(main, "Lưu").click();
  await flush();
  button(main, "Thêm mắt xích").click();
  release();
  await flush();
  button(main, "Lưu").click();
  await flush();

  // Nhánh lưu thành công chỉ nhận revision từ phản hồi, KHÔNG cả snapshot — nên mắt xích thêm trong
  // lúc PUT còn bay vẫn còn ở lượt lưu kế tiếp.
  assert.equal(saves[1].entries.length, 3);
});

// threeLinkChain: hai mắt xích dời được cộng một cái đuôi — chuỗi ngắn nhất mà Lên và Xuống đều có việc.
const THREE = {
  revision: 4,
  entries: [link("openai-1", "gpt-5"), link("gemini", "gemini-2"), link("claude-code", "haiku")],
};

function assertFocusLandedIn(row, what) {
  const active = document.activeElement;
  assert.ok(active, `${what} must leave focus somewhere`);
  assert.equal(find(row, (node) => node === active), active, `${what} must leave focus in the moved row`);
  assert.equal(active.disabled, false, `${what} must leave focus on a usable control`);
}

test("focus follows a link moved down", (t) => {
  const { main } = mountEditor(t, { snapshot: THREE });
  button(rows(main)[0], "Xuống").click();
  assertFocusLandedIn(rows(main)[1], "moving a link down");
});

test("focus follows a link moved up to the top of the chain", (t) => {
  const { main } = mountEditor(t, { snapshot: THREE });
  button(rows(main)[1], "Lên").click();
  assertFocusLandedIn(rows(main)[0], "moving a link up");
});

test("a status-read error survives an unrelated edit and clears when the poll recovers", (t) => {
  const { main, editor } = mountEditor(t, { live: true });
  editor.setStatus(RUNNING, "không đọc được trạng thái");
  assert.match(text(statusLine(main)), /không đọc được trạng thái/);

  // Sửa một hàng chạy applyStatus lại — câu lỗi được GIỮ, không bị lượt sửa xoá oan.
  choose(selects(rows(main)[0])[1], "gpt-5-mini");
  assert.match(text(statusLine(main)), /không đọc được trạng thái/);

  // Lượt đọc lành lại (statusError rỗng) mới gỡ câu lỗi khỏi màn hình.
  editor.setStatus(RUNNING, "");
  assert.doesNotMatch(text(statusLine(main)), /không đọc được trạng thái/);
});
