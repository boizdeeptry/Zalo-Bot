import test from "node:test";
import assert from "node:assert/strict";

import {
  createOnboardingPage,
  createOnboardingService,
} from "../overlay/internal/webui/static/pages/onboarding.js";
import {
  normalizeSetupResult,
  normalizeStatus,
  normalizeTestResponse,
} from "../overlay/internal/webui/static/pages/onboarding-contract.js";
import { find, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));

function status(overrides = {}) {
  return onboardingStatus("test", overrides);
}

function response(overrides = {}) {
  return {
    answer: "Xin chào, mình là Bé Mi.",
    bot_name: "Bé Mi",
    provider_id: "codex",
    model_id: "gpt-5.6-terra",
    position: 0,
    test_token: "opaque-test-token",
    expires_at: new Date(Date.now() + 600_000).toISOString(),
    revision: 8,
    ...overrides,
  };
}

function service(overrides = {}) {
  return {
    status: () => Promise.resolve(status()),
    updateProviders: () => Promise.reject(new Error("not used")),
    beginProvider: () => Promise.reject(new Error("not used")),
    backToProviders: () => Promise.reject(new Error("not used")),
    selectProvider: () => Promise.reject(new Error("not used")),
    setup: () => Promise.reject(new Error("not used")),
    loadAgent: () => Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [] }),
    saveAgent: () => Promise.reject(new Error("not used")),
    testChat: () => Promise.resolve(response()),
    complete: () => Promise.reject(new Error("not used")),
    ...overrides,
  };
}

function mount(t, options = {}) {
  const dom = installDOM();
  const host = document.createElement("div");
  const page = createOnboardingPage({
    initialStatus: status(),
    service: service(),
    ...options,
  });
  page.mount(host);
  t.after(() => {
    page.dispose();
    dom.restore();
  });
  return { page, host };
}

test("Test response requires the normalized answer to contain the authoritative name", () => {
  const snapshot = status();
  const now = Date.now();
  assert.equal(normalizeTestResponse(response({
    answer: "Xin chào, tôi là trợ lý của bạn.",
  }), { displayName: "Bé Mi", snapshot, now }), null);

  for (const [displayName, answer] of [
    ["Bé Mi", "Xin chào, tôi là  BÉ\u2003\tMI."],
    ["Bé Mi", "Xin chào, tôi là BE\u0301 MI."],
    ["ος", "Είμαι ΟΣ"],
    ["SORA", "Đây là ſora"],
  ]) {
    const result = normalizeTestResponse(response({ answer, bot_name: displayName }), {
      displayName, snapshot, now,
    });
    assert.equal(result?.token, "opaque-test-token", `${displayName} should match ${answer}`);
  }
});

test("Test Chat gives the persona-specific request error a distinct recovery path", async (t) => {
  const requestError = Object.assign(new Error("private provider detail"), {
    status: 422,
    code: "ONBOARDING_PERSONA_NOT_APPLIED",
  });
  const requestBackedService = createOnboardingService({
    requestJSON(path) {
      if (path === "/agent") {
        return Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [] });
      }
      if (path === "/onboarding/test-chat") return Promise.reject(requestError);
      throw new Error(`unexpected request: ${path}`);
    },
  });
  const { host } = mount(t, {
    service: requestBackedService,
  });
  await flush();
  find(host, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();

  assert.match(text(host), /Bot đã phản hồi nhưng chưa áp dụng đúng Persona/u);
  assert.doesNotMatch(text(host), /private provider detail/u);
  assert.ok(button(host, "Thử lại"));
  assert.ok(button(host, "Quay lại chỉnh Persona"));
  assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
});

test("status validates and retains exact version, required, and restart markers", () => {
  const fresh = normalizeStatus(status({ current_version: 2, completed_version: 1 }));
  assert.deepEqual({
    required: fresh?.required,
    current_version: fresh?.current_version,
    completed_version: fresh?.completed_version,
    restart_in_progress: fresh?.restart_in_progress,
  }, { required: true, current_version: 2, completed_version: 1, restart_in_progress: false });
  const restarted = normalizeStatus(status({
    phase: "provider", current_version: 2, completed_version: 2, restart_in_progress: true,
  }));
  assert.equal(restarted?.phase, "provider");
  assert.equal(normalizeStatus(status({ phase: "completed", current_version: 2 }))?.required, false);

  const malformed = [
    { current_version: 0 }, { current_version: -1 }, { current_version: 1.5 },
    { current_version: "1" }, { completed_version: -1 }, { completed_version: 2 },
    { completed_version: 0.5 }, { completed_version: "0" }, { restart_in_progress: "false" },
    { required: false },
    { phase: "completed", required: true },
    { phase: "completed", restart_in_progress: true },
    { phase: "test", completed_version: 1, restart_in_progress: false },
    { phase: "test", completed_version: 0, restart_in_progress: true },
  ];
  for (const overrides of malformed) assert.equal(normalizeStatus(status(overrides)), null, JSON.stringify(overrides));
  for (const field of ["required", "current_version", "completed_version", "restart_in_progress"]) {
    const missing = status();
    delete missing[field];
    assert.equal(normalizeStatus(missing), null, `missing ${field}`);
  }
});

test("Setup-to-Persona retains lifecycle markers", () => {
  const lifecycle = normalizeStatus(status({ phase: "setup", revision: 3 }));
  const setupResponse = {
    revision: 4,
    provider_kind: "codex",
    provider_id: "codex",
    account_id: "account-1",
    model_id: "gpt-5.6-terra",
    staged_combo_id: "combo-staged",
    combo_name: "Codex mặc định",
  };
  const expected = {
    accountID: "account-1", revision: 3, providerKind: "codex", providerID: "codex", lifecycle,
  };
  const result = normalizeSetupResult(setupResponse, expected);
  assert.deepEqual({
    current_version: result?.current_version,
    completed_version: result?.completed_version,
    restart_in_progress: result?.restart_in_progress,
  }, { current_version: 1, completed_version: 0, restart_in_progress: false });
  const malformedLifecycle = { ...lifecycle };
  delete malformedLifecycle.current_version;
  assert.equal(normalizeSetupResult(setupResponse, {
    ...expected, lifecycle: malformedLifecycle,
  }), null);
});

test("missing and contradictory lifecycle markers fail closed initially and after refresh", async (t) => {
  await t.test("initial", async (subtest) => {
    let handoffs = 0;
    const missingCurrentVersion = status({ phase: "completed" });
    delete missingCurrentVersion.current_version;
    const { host } = mount(subtest, {
      initialStatus: missingCurrentVersion,
      onComplete: () => { handoffs++; },
    });
    await flush();
    assert.doesNotMatch(text(host), /đã sẵn sàng/u);
    assert.equal(button(host, "Vào Portal"), null);
    assert.equal(handoffs, 0);
  });

  await t.test("refreshed", async (subtest) => {
    let handoffs = 0;
    const conflict = Object.assign(new Error("private"), {
      status: 409, code: "ONBOARDING_REVISION_CONFLICT",
    });
    const { host } = mount(subtest, {
      service: service({
        testChat: () => Promise.resolve(response()),
        complete: () => Promise.reject(conflict),
        status: () => Promise.resolve(status({
          phase: "completed", current_version: 1, completed_version: 2,
          required: false, revision: 9,
        })),
      }),
      onComplete: () => { handoffs++; },
    });
    await flush();
    find(host, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
    await flush();
    button(host, "Ổn, dùng cấu hình này").click();
    await flush();
    assert.doesNotMatch(text(host), /đã sẵn sàng/u);
    assert.equal(button(host, "Vào Portal"), null);
    assert.equal(handoffs, 0);
  });
});
