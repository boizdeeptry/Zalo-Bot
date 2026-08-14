import test from "node:test";
import assert from "node:assert/strict";
import { createOnboardingPage } from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const byClass = (root, name) => find(root, (node) => hasClass(node, name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON" && text(node).includes(label));
const providerCard = (root, kind) => find(root, (node) => hasClass(node, "onboarding-provider-card")
  && node.dataset.providerKind === kind);

function providerStatus(overrides = {}) {
  return onboardingStatus("provider", { revision: 1, ...overrides });
}

function connectStatus(overrides = {}) {
  return providerStatus({
    phase: "connect", provider_kind: "codex", revision: 2, ...overrides,
  });
}

function setupStatus(overrides = {}) {
  return providerStatus({
    phase: "setup", provider_kind: "codex", provider_id: "codex",
    account_id: "account-1", revision: 3, ...overrides,
  });
}

function setupResult(overrides = {}) {
  return {
    revision: 4, provider_kind: "codex", provider_id: "codex", account_id: "account-1",
    model_id: "gpt-5.6-terra", staged_combo_id: "combo-staged",
    combo_name: "Codex mặc định", ...overrides,
  };
}

function service(overrides = {}) {
  return {
    status: () => Promise.resolve(providerStatus()),
    selectProvider: () => Promise.resolve(connectStatus()),
    setup: () => Promise.resolve(setupResult()),
    loadAgent: () => Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [] }),
    saveAgent: () => Promise.reject(new Error("not used")),
    testChat: () => Promise.reject(new Error("not used")),
    complete: () => Promise.reject(new Error("not used")),
    ...overrides,
  };
}

function connectService() {
  return {
    connectStart: () => Promise.resolve({ kind: "codex", phase: "detecting" }),
    connectStatus: () => new Promise(() => {}),
    connectCancel: () => Promise.resolve({ ok: true }),
  };
}

function connectFactory(instances = []) {
  return (options) => {
    const instance = {
      options,
      starts: [],
      mount(slot) { this.slot = slot; return this; },
      start(value) { this.starts.push(value); return true; },
      cancel: () => Promise.resolve(true),
      dispose() {},
    };
    instances.push(instance);
    return instance;
  };
}

function mount(t, options = {}) {
  const dom = installDOM();
  const host = document.createElement("div");
  const page = createOnboardingPage({
    initialStatus: providerStatus(),
    service: service(),
    connectService: connectService(),
    ...options,
  });
  page.mount(host);
  t.after(() => { page.dispose(); dom.restore(); });
  return { host, page };
}

test("provider phase uses an AGS-like Agent Setup surface with separated selection status", (t) => {
  let selects = 0;
  const { host } = mount(t, {
    initialStatus: providerStatus({ suggested_provider_kind: "codex" }),
    service: service({ selectProvider: () => { selects++; return Promise.resolve(connectStatus()); } }),
  });
  assert.ok(byClass(host, "onboarding-agent-setup-stage"));
  assert.match(text(host), /Agent Setup/u);
  assert.match(text(host), /Thiết lập Agent/u);
  assert.match(text(host), /Trạng thái/u);
  assert.match(text(host), /Chọn provider muốn dùng/u);
  assert.match(text(providerCard(host, "codex")), /Đã chọn/u);
  assert.match(text(providerCard(host, "claude-code")), /Chưa chọn/u);
  assert.equal(findAll(host, (node) => hasClass(node, "onboarding-provider-card")).length, 2);
  providerCard(host, "claude-code").click();
  assert.equal(selects, 0, "clicking a row only changes local selection");
  assert.match(text(providerCard(host, "claude-code")), /Đã chọn/u);
  assert.ok(button(host, "Tiếp tục"));
});

test("connect phase keeps Provider Connect inside the same Agent Setup status surface", (t) => {
  const instances = [];
  const { host } = mount(t, {
    initialStatus: connectStatus({ revision: 6, provider_kind: "claude-code" }),
    connectFactory: connectFactory(instances),
  });
  assert.ok(byClass(host, "onboarding-agent-setup-stage"));
  assert.match(text(host), /Agent Setup/u);
  assert.match(text(host), /Đang kết nối/u);
  assert.match(text(host), /Claude Code/u);
  assert.match(text(host), /Provider Connect/u);
  assert.ok(byClass(host, "onboarding-connect-slot"));
  assert.equal(instances.length, 1);
  assert.deepEqual(instances[0].starts, [{ label: "Onboarding", onboardingRevision: 6 }]);
});

test("setup phase looks like an inline AGS setup queue before Persona starts", async (t) => {
  const pending = new Promise(() => {});
  let setups = 0;
  const { host } = mount(t, {
    initialStatus: setupStatus({ revision: 9, account_id: "account-7" }),
    service: service({
      setup(accountId, revision) {
        setups++;
        assert.deepEqual({ accountId, revision }, { accountId: "account-7", revision: 9 });
        return pending;
      },
    }),
  });
  await flush();
  assert.equal(setups, 1);
  assert.ok(byClass(host, "onboarding-agent-setup-stage"));
  assert.match(text(host), /Agent Setup/u);
  assert.match(text(host), /Hàng đợi thiết lập/u);
  assert.match(text(host), /Đang thiết lập/u);
});
