import test from "node:test";
import assert from "node:assert/strict";

import {
  createOnboardingPage,
  createOnboardingService,
} from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const never = () => new Promise(() => {});

function completedStatus(overrides = {}) {
  return onboardingStatus("completed", { revision: 10, ...overrides });
}

function pageService(overrides = {}) {
  const status = overrides.status ?? never;
  return {
    status,
    bootstrapStatus: overrides.bootstrapStatus ?? status,
    updateProviders: never,
    beginProvider: never,
    backToProviders: never,
    setup: never,
    loadAgent: () => Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [] }),
    bootstrap: never,
    ...overrides,
  };
}

test("status service keeps the public projection and forwards its AbortSignal", async () => {
  const signal = new AbortController().signal;
  const calls = [];
  const response = { ...completedStatus(), private_receipt: "must-not-cross" };
  const service = createOnboardingService({
    requestJSON(path, options) {
      calls.push({ path, options });
      return Promise.resolve(response);
    },
  });

  const projected = await service.status(signal);

  assert.deepEqual(calls, [{ path: "/onboarding/status", options: { signal } }]);
  assert.equal(Object.hasOwn(projected, "private_receipt"), false);
  assert.equal(projected.phase, "completed");
});

test("completed resume labels Done from Agent and offers Knowledge independently", async (t) => {
  const dom = installDOM();
  const host = document.createElement("div");
  const destinations = [];
  const page = createOnboardingPage({
    initialStatus: completedStatus(),
    service: pageService({
      loadAgent: () => Promise.resolve({
        ready: true,
        display_name: "Bé Mi",
        placeholders: [{ key: "TEN_DOANH_NGHIEP", count: 1, sample: "Cửa hàng Mầm" }],
      }),
    }),
    onComplete: ({ destination }) => destinations.push(destination),
  });
  page.mount(host);
  t.after(() => { page.dispose(); dom.restore(); });

  await flush();
  assert.match(text(host), /Bé Mi đã sẵn sàng!/u);
  assert.ok(button(host, "Vào Portal"));
  assert.ok(button(host, "Thêm tài liệu doanh nghiệp"));

  button(host, "Thêm tài liệu doanh nghiệp").click();
  await flush();
  assert.deepEqual(destinations, ["knowledge"]);
});
