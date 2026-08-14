import test from "node:test";
import assert from "node:assert/strict";

import {
  createWelcomeStage,
  focusProviderControl,
} from "../overlay/internal/webui/static/pages/onboarding-early-view.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const byClass = (root, name) => find(root, (node) => hasClass(node, name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const providerRow = (root, kind) => find(root, (node) =>
  hasClass(node, "onboarding-provider-card") && node.dataset.providerKind === kind);
const providerToggle = (root, kind) => find(root, (node) =>
  hasClass(node, "onboarding-provider-toggle") && node.dataset.providerKind === kind);
const installButton = (root, kind) => byClass(providerRow(root, kind), "onboarding-provider-install");
const confirmation = (root) => find(root, (node) => node.getAttribute?.("role") === "alertdialog");

function mountWelcome(t, initialSelection = "", message = "") {
  const dom = installDOM();
  const host = document.createElement("div");
  const events = [];
  let selectedProvider = initialSelection;
  const listen = (node, type, listener) => {
    node.addEventListener(type, listener);
    return node;
  };
  const render = () => host.replaceChildren(createWelcomeStage({
    selectedProvider,
    message,
    listen,
    onSelect(kind) {
      events.push({ type: "select", kind });
      selectedProvider = kind;
      render();
    },
    onProceed() { events.push({ type: "install", kind: selectedProvider }); },
    onRetry() { events.push({ type: "retry" }); },
  }));
  render();
  t.after(() => dom.restore());
  return { host, events };
}

test("ON exposes a separate install action without the old Continue action", (t) => {
  const { host, events } = mountWelcome(t);

  providerToggle(host, "codex").click();

  const row = providerRow(host, "codex");
  const toggle = providerToggle(host, "codex");
  const install = installButton(host, "codex");
  assert.equal(row.tagName, "DIV");
  assert.equal(toggle.getAttribute("aria-pressed"), "true");
  assert.equal(toggle.getAttribute("aria-label"), "Tắt Codex");
  assert.equal(text(byClass(row, "onboarding-provider-install-state")), "Chưa cài. Bấm để cài.");
  assert.equal(text(install), "Bấm để cài.");
  assert.equal(button(host, "Tiếp tục kết nối"), null);
  assert.deepEqual(events, [{ type: "select", kind: "codex" }]);
});

test("OFF is noninteractive outside its toggle and has no install action", (t) => {
  const { host, events } = mountWelcome(t);
  const row = providerRow(host, "claude-code");

  assert.equal(row.tagName, "DIV");
  assert.equal(text(byClass(row, "onboarding-provider-install-state")), "Chưa dùng");
  assert.equal(installButton(host, "claude-code"), null);
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-label"), "Bật Claude Code");
  row.click();
  assert.deepEqual(events, []);

  const nestedButtons = findAll(host, (node) => node.tagName === "BUTTON")
    .flatMap((control) => findAll(control, (node) => node !== control && node.tagName === "BUTTON"));
  assert.deepEqual(nestedButtons, []);
});

test("turning the current provider OFF opens a named inline confirmation", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");

  origin.click();

  const warning = confirmation(host);
  assert.ok(warning);
  assert.equal(warning.getAttribute("aria-labelledby"), "onboarding-provider-confirm-title");
  assert.equal(warning.getAttribute("aria-describedby"), "onboarding-provider-confirm-description");
  assert.equal(warning.hasAttribute("aria-modal"), false);
  assert.equal(text(byClass(warning, "onboarding-provider-confirm-description")),
    "Nếu tắt Codex trước khi cài, lựa chọn này sẽ bị bỏ. Bạn vẫn muốn tắt chứ?");
  assert.equal(document.activeElement, button(warning, "Huỷ"));
  assert.deepEqual(events, []);
});

test("Huỷ closes confirmation, keeps selection, and restores toggle focus", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");
  origin.click();

  button(confirmation(host), "Huỷ").click();

  assert.equal(confirmation(host), null);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(document.activeElement, origin);
  assert.deepEqual(events, []);
});

test("Escape cancels confirmation and restores the originating toggle", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const origin = providerToggle(host, "codex");
  origin.click();

  const warning = confirmation(host);
  const event = { type: "keydown", key: "Escape" };
  warning.dispatchEvent(event);

  assert.equal(event.defaultPrevented, true);
  assert.equal(confirmation(host), null);
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(document.activeElement, origin);
  assert.deepEqual(events, []);
});

test("Vẫn tắt applies OFF only after confirmation", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  providerToggle(host, "codex").click();

  button(confirmation(host), "Vẫn tắt").click();

  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
  assert.equal(installButton(host, "codex"), null);
  assert.deepEqual(events, [{ type: "select", kind: "" }]);
});

test("switching providers confirms before applying the replacement", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const replacement = providerToggle(host, "claude-code");

  replacement.click();

  assert.ok(confirmation(host));
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "true");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "false");
  assert.deepEqual(events, []);

  button(confirmation(host), "Vẫn tắt").click();
  assert.equal(providerToggle(host, "codex").getAttribute("aria-pressed"), "false");
  assert.equal(providerToggle(host, "claude-code").getAttribute("aria-pressed"), "true");
  assert.deepEqual(events, [{ type: "select", kind: "claude-code" }]);
});

test("confirmation disables every background toggle and install action", (t) => {
  const { host } = mountWelcome(t, "codex");
  providerToggle(host, "claude-code").click();

  const controls = [
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-toggle")),
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-install")),
  ];
  assert.ok(controls.length > 0);
  assert.ok(controls.every((control) => control.disabled));
  assert.equal(button(confirmation(host), "Huỷ").disabled, false);
  assert.equal(button(confirmation(host), "Vẫn tắt").disabled, false);
});

test("confirmation locks Retry until cancellation", (t) => {
  const { host, events } = mountWelcome(t, "codex", "Không thể kết nối");
  const retry = button(host, "Thử lại");
  assert.equal(retry.disabled, false);

  providerToggle(host, "codex").click();

  assert.equal(retry.disabled, true);
  retry.click();
  assert.deepEqual(events, []);

  button(confirmation(host), "Huỷ").click();
  assert.equal(retry.disabled, false);
  retry.click();
  assert.deepEqual(events, [{ type: "retry" }]);
});

test("install CTA is one-flight and disables all provider controls", (t) => {
  const { host, events } = mountWelcome(t, "codex");
  const install = installButton(host, "codex");

  install.click();
  install.click();

  assert.deepEqual(events, [{ type: "install", kind: "codex" }]);
  const controls = [
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-toggle")),
    ...findAll(host, (node) => hasClass(node, "onboarding-provider-install")),
  ];
  assert.ok(controls.every((control) => control.disabled));
});

test("install one-flight also locks Retry", (t) => {
  const { host, events } = mountWelcome(t, "codex", "Không thể kết nối");
  const install = installButton(host, "codex");
  const retry = button(host, "Thử lại");

  install.click();
  retry.click();
  retry.click();
  install.click();

  assert.equal(retry.disabled, true);
  assert.deepEqual(events, [{ type: "install", kind: "codex" }]);
});

test("focusProviderControl targets the dedicated provider toggle", (t) => {
  const dom = installDOM();
  t.after(() => dom.restore());
  const listen = (node, type, listener) => {
    node.addEventListener(type, listener);
    return node;
  };
  const stage = createWelcomeStage({
    selectedProvider: "codex",
    message: "",
    listen,
    onSelect() {},
    onProceed() {},
    onRetry() {},
  });

  assert.equal(focusProviderControl(stage, "codex"), true);
  assert.equal(document.activeElement, providerToggle(stage, "codex"));
  assert.notEqual(document.activeElement, providerRow(stage, "codex"));
});
