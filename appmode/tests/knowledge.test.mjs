import test from "node:test";
import assert from "node:assert/strict";

import {
  createKnowledgePage,
  createKnowledgeService,
} from "../overlay/internal/webui/static/pages/knowledge.js";

test("knowledge dispose clears polling timer", () => {
  let cleared = 0;
  const page = createKnowledgePage({
    setInterval: () => 41,
    clearInterval: (id) => { if (id === 41) cleared++ },
  });

  page.startPolling();
  page.dispose();

  assert.equal(cleared, 1);
});

test("knowledge service sends uploads and ingest actions through the shared API", async () => {
  const calls = [];
  const request = async (path, options = {}) => {
    calls.push({ path, options });
    return {};
  };
  const service = createKnowledgeService(request);
  const form = new FormData();

  await service.load();
  await service.upload(form);
  await service.start();
  await service.stop();

  assert.deepEqual(calls, [
    { path: "/kb", options: {} },
    { path: "/kb/upload", options: { method: "POST", body: form } },
    { path: "/kb/ingest", options: { method: "POST" } },
    { path: "/kb/ingest", options: { method: "DELETE" } },
  ]);
});
