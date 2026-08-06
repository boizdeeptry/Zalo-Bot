import test from "node:test";
import assert from "node:assert/strict";

import { createModelService, createModelsPage } from "../overlay/internal/webui/static/pages/models.js";
import { waitForServerRestart } from "../overlay/internal/webui/static/core/state.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, className) => node.classList?.contains(className) ?? false;

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
  const restarted = await waitForServerRestart({ probe: async () => probes.shift(), wait: async () => {}, onPhase: (phase) => phases.push(phase), reload: () => { reloads++ }, maxAttempts: 8 });
  assert.equal(restarted, true);
  assert.equal(reloads, 1);
  assert.deepEqual(phases, ["stopping", "stopping", "starting", "starting", "ready"]);
});

test("restart polling never reloads while only the old daemon is visible", async () => {
  let reloads = 0;
  const restarted = await waitForServerRestart({ probe: async () => true, wait: async () => {}, reload: () => { reloads++ }, maxAttempts: 3 });
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
    probe: () => { probeCount++; if (probeCount === 1) return false; markSecondProbe(); return new Promise((resolve) => { resolveProbe = resolve }); },
    wait: async () => {}, reload: () => { reloads++ }, signal: controller.signal, maxAttempts: 2,
  });
  await secondProbeStarted;
  controller.abort();
  resolveProbe(true);
  assert.equal(await polling, false);
  assert.equal(reloads, 0);
});

test("Models renders the legacy page contract and saves the chosen model", async (t) => {
  const dom = installDOM();
  const calls = [];
  let restarts = 0;
  const page = createModelsPage({
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (!options.method) return { active: "haiku", saved: "sonnet", choices: ["haiku", "sonnet", "opus"] };
      return { restarting: true };
    },
    waitForRestart: async ({ onPhase }) => { restarts++; onPhase("starting"); return false; },
  });
  t.after(dom.restore);
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "Models");
  assert.equal(text(find(main, (node) => hasClass(node, "sub"))), "Mô hình mà bot dùng để viết câu trả lời.");
  for (const className of ["status-panel", "section-block", "model-grid"]) assert.equal(findAll(main, (node) => hasClass(node, className)).length, 0);
  const picks = findAll(main, (node) => hasClass(node, "pick"));
  assert.equal(picks.length, 3);
  const sonnet = picks.find((node) => text(find(node, (child) => hasClass(child, "nm"))) === "Sonnet");
  assert.ok(hasClass(sonnet, "on"));
  assert.equal(sonnet.getAttribute("aria-pressed"), "true");
  assert.equal(sonnet.children[1].style.minWidth, "0");
  assert.equal(find(sonnet, (node) => hasClass(node, "ds")).tagName, "DIV");
  const haiku = picks.find((node) => text(find(node, (child) => hasClass(child, "nm"))).startsWith("Haiku"));
  assert.equal(text(find(haiku, (node) => hasClass(node, "nm"))), "Haiku  · đang chạy");
  assert.equal(text(find(haiku, (node) => hasClass(node, "tag"))), "nhanh nhất, rẻ nhất");
  assert.equal(text(find(sonnet, (node) => hasClass(node, "tag"))), "cân bằng");
  picks.find((node) => text(find(node, (child) => hasClass(child, "nm"))) === "Opus").click();
  const selectedOpus = findAll(main, (node) => hasClass(node, "pick"))
    .find((node) => text(find(node, (child) => hasClass(child, "nm"))) === "Opus");
  assert.ok(hasClass(selectedOpus, "on"));
  assert.equal(selectedOpus.getAttribute("aria-pressed"), "true");
  const save = find(main, (node) => hasClass(node, "btn") && hasClass(node, "go"));
  assert.equal(text(save), "Lưu");
  save.click();
  await flush();
  assert.deepEqual(calls.at(-1), { path: "/kb/model", options: { method: "PUT", body: { model: "opus" }, signal: calls[0].options.signal } });
  assert.equal(restarts, 1);
});

test("Models ignores a save that resolves after disposal", async (t) => {
  const dom = installDOM();
  let resolveSave;
  let restarts = 0;
  let reloads = 0;
  const page = createModelsPage({
    request: (path, options = {}) => {
      if (!options.method) return Promise.resolve({ active: "haiku", saved: "haiku", choices: ["haiku", "sonnet", "opus"] });
      return new Promise((resolve) => { resolveSave = resolve; });
    },
    waitForRestart: async () => { restarts++; return true; },
    reload: () => { reloads++; },
  });
  t.after(dom.restore);
  const main = document.createElement("main");
  const mounted = page.mount(main);
  await flush();
  const save = find(main, (node) => hasClass(node, "btn") && hasClass(node, "go"));
  save.click();
  const beforeDisposal = text(main);
  mounted.dispose();
  resolveSave({ restarting: true });
  await flush();

  assert.equal(restarts, 0);
  assert.equal(reloads, 0);
  assert.equal(text(main), beforeDisposal);
});
