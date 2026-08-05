import test from "node:test";
import assert from "node:assert/strict";

import {
  createKnowledgePage,
  createKnowledgeService,
} from "../overlay/internal/webui/static/pages/knowledge.js";
import { createModelService } from "../overlay/internal/webui/static/pages/models.js";
import { waitForServerRestart } from "../overlay/internal/webui/static/core/state.js";

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

test("model service only addresses the model endpoint", async () => {
  const calls = [];
  const service = createModelService(async (path, options = {}) => {
    calls.push({ path, options });
    return {};
  });

  await service.load();
  await service.save("sonnet");

  assert.deepEqual(calls, [
    { path: "/kb/model", options: {} },
    { path: "/kb/model", options: { method: "PUT", body: { model: "sonnet" } } },
  ]);
});

test("restart polling observes down then up before reloading", async () => {
  const probes = [true, true, false, false, true];
  const phases = [];
  let reloads = 0;

  const restarted = await waitForServerRestart({
    probe: async () => probes.shift(),
    wait: async () => {},
    onPhase: (phase) => phases.push(phase),
    reload: () => { reloads++ },
    maxAttempts: 8,
  });

  assert.equal(restarted, true);
  assert.equal(reloads, 1);
  assert.deepEqual(phases, ["stopping", "stopping", "starting", "starting", "ready"]);
});

test("restart polling never reloads while only the old daemon is visible", async () => {
  let reloads = 0;
  const restarted = await waitForServerRestart({
    probe: async () => true,
    wait: async () => {},
    reload: () => { reloads++ },
    maxAttempts: 3,
  });

  assert.equal(restarted, false);
  assert.equal(reloads, 0);
});

test("restart polling ignores a probe that resolves after disposal", async () => {
  const controller = new AbortController();
  let resolveProbe;
  let probeCount = 0;
  let reloads = 0;
  let markSecondProbe;
  const secondProbeStarted = new Promise((resolve) => { markSecondProbe = resolve });
  const polling = waitForServerRestart({
    probe: () => {
      probeCount++;
      if (probeCount === 1) return false;
      markSecondProbe();
      return new Promise((resolve) => { resolveProbe = resolve });
    },
    wait: async () => {},
    reload: () => { reloads++ },
    signal: controller.signal,
    maxAttempts: 2,
  });
  await secondProbeStarted;

  controller.abort();
  resolveProbe(true);
  const restarted = await polling;

  assert.equal(restarted, false);
  assert.equal(reloads, 0);
});
