import test from "node:test";
import assert from "node:assert/strict";

import {
  addEntry,
  moveEntry,
  patchEntry,
  removeEntry,
  snapshotOf,
} from "../overlay/internal/webui/static/pages/route-editor.js";

// Kiểm thẳng các hàm thuần chứ không qua DOM. Không có bất biến "mắt xích cuối cố định": mọi mắt
// xích dời/tắt/xoá được như nhau, và các hàm này chỉ còn canh biên (index trong khoảng).

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
