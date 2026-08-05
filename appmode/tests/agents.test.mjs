import test from "node:test";
import assert from "node:assert/strict";

import {
  createAgentService,
  fillHoles,
  scanHoles,
} from "../overlay/internal/webui/static/pages/agents.js";

test("scanHoles counts uppercase placeholders only", () => {
  assert.deepEqual(
    scanHoles("{{TEN_BOT}} {{TEN_BOT}} {{ten}}"),
    [{ key: "TEN_BOT", count: 2 }],
  );
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
  await service.loadDocument("persona");
  await service.saveDocument("persona", "Giọng Việt — UTF-8");

  assert.deepEqual(calls, [
    { path: "/agent", options: {} },
    { path: "/agent", options: { method: "PUT", body: { values: { TEN_BOT: "An Nhiên" } } } },
    { path: "/agent/persona/persona", options: {} },
    { path: "/agent/persona/persona", options: { method: "PUT", body: { text: "Giọng Việt — UTF-8" } } },
  ]);
});
