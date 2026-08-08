import test from "node:test";
import assert from "node:assert/strict";

import {
  LLM_ERROR_FIX,
  addEntry,
  canMove,
  entryWarning,
  failureText,
  moveEntry,
  patchEntry,
  removeEntry,
  snapshotOf,
  statusText,
  tallyText,
} from "../overlay/internal/webui/static/pages/route-editor.js";

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

// --- entryWarning: cảnh báo mắt xích hỏng (Provider tắt / model bỏ), im khi lành ---

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
