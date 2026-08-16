import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { createOnboardingPage } from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const byClass = (root, name) => find(root, (node) => hasClass(node, name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON" && text(node).includes(label));
const providerCard = (root, kind) => find(root, (node) => hasClass(node, "onboarding-provider-card")
  && node.dataset.providerKind === kind);
const providerToggle = (root, kind) => find(root, (node) => hasClass(node, "onboarding-provider-toggle")
  && node.dataset.providerKind === kind);

function assertFullPageFrame(host) {
  const dashboards = findAll(host, (node) => hasClass(node, "onboarding-dashboard"));
  const dialogs = findAll(host, (node) => node.getAttribute?.("role") === "dialog");
  assert.equal(dashboards.length, 1);
  assert.equal(dialogs.length, 0);
  const [dashboard] = dashboards;
  const card = byClass(host, "onboarding-dialog");
  assert.ok(card);
  assert.equal(dashboard.getAttribute("aria-hidden"), "true");
  const decorativeElements = findAll(dashboard, (node) => Boolean(node.tagName));
  assert.ok(decorativeElements.every((node) => ["DIV", "SPAN"].includes(node.tagName)));
  assert.equal(card.getAttribute("aria-modal"), null);
  assert.equal(card.getAttribute("aria-labelledby"), "onboarding-dialog-title");
  assert.equal(document.activeElement, card);
  const dismissers = findAll(card, (node) => node.tagName === "BUTTON"
    && /đóng|bỏ qua|close|skip/iu.test(`${text(node)} ${node.getAttribute("aria-label") ?? ""}`));
  assert.equal(dismissers.length, 0);
  return { dashboard, card };
}

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
  const status = overrides.status ?? (() => Promise.resolve(providerStatus()));
  return {
    status,
    bootstrapStatus: overrides.bootstrapStatus ?? status,
    updateProviders: () => Promise.reject(new Error("not used")),
    beginProvider: () => Promise.reject(new Error("not used")),
    backToProviders: () => Promise.reject(new Error("not used")),
    selectProvider: () => Promise.resolve(connectStatus()),
    setup: () => Promise.resolve(setupResult()),
    loadAgent: () => Promise.resolve({ ready: true, display_name: "Bé Mi", placeholders: [] }),
    bootstrap: () => new Promise(() => {}),
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

test("provider phase uses a non-modal Tư Vấn Zalo card over the retained grid canvas", (t) => {
  const { host } = mount(t, {
    initialStatus: providerStatus({ suggested_provider_kind: "codex" }),
  });
  const { card } = assertFullPageFrame(host);
  const dashboard = byClass(host, "onboarding-dashboard");
  assert.match(text(dashboard), /0\/2.*chưa thiết lập.*Chưa dùng:.*Claude Code, Codex/su);
  assert.doesNotMatch(text(dashboard), /2\/2.*sẵn sàng/su);
  assert.ok(byClass(host, "onboarding-agent-setup-stage"));
  assert.match(text(host), /Tư Vấn Zalo/u);
  assert.match(text(host), /Thiết lập trợ lý Zalo/u);
  assert.match(text(host), /Trạng thái/u);
  assert.match(text(host), /Chọn nhà cung cấp muốn dùng/u);
  assert.match(text(providerCard(host, "codex")), /Đề xuất/u);
  assert.match(text(providerCard(host, "codex")), /Chưa dùng/u);
  assert.match(text(providerCard(host, "claude-code")), /Chưa dùng/u);
  const cards = findAll(host, (node) => hasClass(node, "onboarding-provider-card"));
  assert.deepEqual(cards.map((card) => card.dataset.providerKind), ["codex", "claude-code"]);
  assert.ok(cards.every((card) => card.tagName === "DIV"));
  assert.equal(findAll(host, (node) => hasClass(node, "onboarding-provider-mark")).length, 2);
  const toggles = findAll(host, (node) => hasClass(node, "onboarding-provider-toggle"));
  assert.equal(toggles.length, 2);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "false");
  assert.equal(button(providerCard(host, "codex"), "Bấm để cài."), null);
  assert.equal(button(providerCard(host, "claude-code"), "Bấm để cài."), null);
  assert.equal(button(host, "Tiếp tục kết nối"), null);
  const switches = findAll(host, (node) => hasClass(node, "onboarding-agent-switch"));
  assert.equal(switches.length, 2);
  assert.equal(switches.filter((node) => hasClass(node, "is-on")).length, 0);
  card.dispatchEvent({ type: "keydown", key: "Escape" });
  byClass(host, "onboarding-backdrop").click();
  assert.equal(byClass(host, "onboarding-dialog"), card);
  providerCard(host, "claude-code").click();
  assert.equal(find(host, (node) => node.getAttribute?.("role") === "alertdialog"), null);
});

test("connect phase stays inside the same Tư Vấn Zalo card", (t) => {
  const instances = [];
  const { host } = mount(t, {
    initialStatus: connectStatus({ revision: 6, provider_kind: "claude-code" }),
    connectFactory: connectFactory(instances),
  });
  assertFullPageFrame(host);
  assert.ok(byClass(host, "onboarding-agent-setup-stage"));
  assert.match(text(host), /Tư Vấn Zalo/u);
  assert.match(text(host), /Đang kết nối/u);
  assert.match(text(host), /Claude Code/u);
  assert.match(text(host), /Provider Connect/u);
  const provider = byClass(host, "onboarding-connect-provider-row");
  assert.ok(provider);
  assert.equal(provider.tagName, "DIV");
  assert.match(text(provider), /Claude Code/u);
  assert.ok(byClass(provider, "onboarding-provider-mark"));
  assert.ok(find(provider, (node) => hasClass(node, "onboarding-agent-switch") && hasClass(node, "is-on")));
  assert.ok(byClass(host, "onboarding-connect-slot"));
  assert.equal(instances.length, 1);
  assert.deepEqual(instances[0].starts, [{
    label: "Onboarding", onboardingRevision: 6, immediate: true,
  }]);
});

test("setup phase stays in the Tư Vấn Zalo card before automatic bootstrap", async (t) => {
  const pending = new Promise(() => {});
  let setups = 0;
  const { host } = mount(t, {
    initialStatus: setupStatus({ revision: 9, account_id: "account-7" }),
    service: service({
      setup(kind, accountId, revision) {
        setups++;
        assert.deepEqual({ kind, accountId, revision }, {
          kind: "codex", accountId: "account-7", revision: 9,
        });
        return pending;
      },
    }),
  });
  await flush();
  assert.equal(setups, 1);
  assertFullPageFrame(host);
  assert.ok(byClass(host, "onboarding-agent-setup-stage"));
  assert.match(text(host), /Tư Vấn Zalo/u);
  assert.match(text(host), /Hàng đợi thiết lập/u);
  assert.match(text(host), /Đang thiết lập/u);
});

test("onboarding UI source does not mention the reference product", async () => {
  const modules = [
    "../overlay/internal/webui/static/app-main.js",
    "../overlay/internal/webui/static/components/persona-fields.js",
    "../overlay/internal/webui/static/components/provider-connect.js",
    "../overlay/internal/webui/static/components/provider-off-dialog.js",
    "../overlay/internal/webui/static/pages/onboarding.js",
    "../overlay/internal/webui/static/pages/onboarding-bootstrap.js",
    "../overlay/internal/webui/static/pages/onboarding-contract.js",
    "../overlay/internal/webui/static/pages/onboarding-early-view.js",
    "../overlay/internal/webui/static/pages/onboarding-late-view.js",
  ];
  const source = (await Promise.all(modules.map((path) => readFile(
    new URL(path, import.meta.url), "utf8",
  ).catch((error) => error?.code === "ENOENT" ? "" : Promise.reject(error))))).join("\n");
  assert.doesNotMatch(source, /\bAGS\b|AGENTSEE/iu);
});
