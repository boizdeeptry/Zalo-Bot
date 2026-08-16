import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { createProviderOffDialog } from "../overlay/internal/webui/static/components/provider-off-dialog.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const settle = async () => { await flush(); await flush(); };
const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const dialog = () => find(document.body, (node) => node.tagName === "DIALOG");
const listen = (node, type, listener) => {
  node.addEventListener(type, listener);
  return node;
};

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, resolve, reject };
}

function setup(t, onConfirm = async () => true) {
  const dom = installDOM();
  const opener = document.createElement("button");
  opener.textContent = "Provider switch";
  document.body.append(opener);
  opener.focus();
  const controller = createProviderOffDialog({ listen, onConfirm });
  t.after(() => { controller.dispose(); dom.restore(); });
  return { controller, opener };
}

test("DOM harness preserves the native modal stack independently of helper ownership", (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const first = document.createElement("dialog");
  const second = document.createElement("dialog");
  document.body.append(first, second);

  first.showModal();
  second.showModal();
  assert.equal(document.activeModalDialog, second);
  second.close();
  assert.equal(document.activeModalDialog, first);
  first.close();
  assert.equal(document.activeModalDialog, null);
});

test("production owns one native OFF implementation and no inline or confirm fallback", async () => {
  const paths = [
    "../overlay/internal/webui/static/components/provider-off-dialog.js",
    "../overlay/internal/webui/static/pages/onboarding.js",
    "../overlay/internal/webui/static/pages/onboarding-early-view.js",
  ];
  const sources = await Promise.all(paths.map((path) => readFile(new URL(path, import.meta.url), "utf8")));
  const all = sources.join("\n");
  assert.equal((all.match(/showModal\s*\(/g) ?? []).length, 1);
  assert.equal((all.match(/role:\s*"alertdialog"/g) ?? []).length, 1);
  assert.doesNotMatch(all, /window\.confirm|onboarding-provider-confirm/);
});

test("native dialog is the sole active modal and starts on the safe action", (t) => {
  const calls = [];
  const { controller, opener } = setup(t, async (...args) => { calls.push(args); return true; });

  assert.equal(controller.open({ label: "Codex", value: { kind: "codex" }, opener }), true);

  const modal = dialog();
  assert.equal(findAll(document.body, (node) => node.tagName === "DIALOG").length, 1);
  assert.equal(modal.open, true);
  assert.equal(document.activeModalDialog, modal);
  assert.equal(modal.getAttribute("role"), "alertdialog");
  assert.equal(modal.getAttribute("aria-modal"), "true");
  assert.equal(modal.getAttribute("aria-labelledby"), "provider-off-dialog-title");
  assert.equal(modal.getAttribute("aria-describedby"), "provider-off-dialog-description");
  assert.equal(text(find(modal, (node) => node.id === "provider-off-dialog-title")), "Xác nhận tắt provider");
  assert.match(text(find(modal, (node) => node.id === "provider-off-dialog-description")), /tắt Codex/u);
  assert.equal(document.activeElement, button(modal, "Hủy"));
  assert.equal(controller.open({ label: "Codex", value: null, opener }), false);
  assert.equal(calls.length, 0);
});

test("Hủy and native Escape close without mutation and restore connected opener focus", (t) => {
  const calls = [];
  const { controller, opener } = setup(t, async (...args) => { calls.push(args); return true; });
  controller.open({ label: "Codex", value: "codex", opener });

  button(dialog(), "Hủy").click();

  assert.equal(dialog(), null);
  assert.equal(document.activeModalDialog, null);
  assert.equal(document.activeElement, opener);
  assert.deepEqual(calls, []);

  controller.open({ label: "Codex", value: "codex", opener });
  assert.equal(dialog().cancel(), false, "the helper prevents native default before closing itself");
  assert.equal(dialog(), null);
  assert.equal(document.activeElement, opener);
  assert.deepEqual(calls, []);
});

test("confirm is one-flight, locks the same modal, and closes only after success", async (t) => {
  const gate = deferred();
  const calls = [];
  const value = Object.freeze({ kind: "future-runtime" });
  const { controller, opener } = setup(t, (received, signal) => {
    calls.push({ received, signal });
    return gate.promise;
  });
  controller.open({ label: "Future Runtime", value, opener });
  const modal = dialog();

  button(modal, "Vẫn tắt").click();
  button(modal, "Vẫn tắt").click();
  await flush();

  assert.equal(calls.length, 1);
  assert.equal(calls[0].received, value);
  assert.equal(calls[0].signal.aborted, false);
  assert.equal(dialog(), modal);
  assert.equal(modal.open, true);
  assert.ok(findAll(modal, (node) => node.tagName === "BUTTON").every((node) => node.disabled));
  const status = find(modal, (node) => hasClass(node, "provider-off-dialog-status"));
  assert.equal(status.getAttribute("role"), "status");
  assert.equal(status.getAttribute("aria-live"), "polite");
  modal.cancel();
  assert.equal(dialog(), modal, "busy native cancel must not close the dialog");

  gate.resolve(true);
  await settle();
  assert.equal(dialog(), null);

  assert.equal(controller.open({ label: "Future Runtime", value, opener }), true);
  assert.equal(findAll(document.body, (node) => node.tagName === "DIALOG").length, 1);
  button(dialog(), "Hủy").click();
});

test("a resolved false result remains retryable in the same modal", async (t) => {
  let calls = 0;
  const { controller, opener } = setup(t, async () => { calls += 1; return false; });
  controller.open({ label: "Codex", value: "codex", opener });
  const modal = dialog();

  button(modal, "Vẫn tắt").click();
  await settle();

  assert.equal(calls, 1);
  assert.equal(dialog(), modal);
  assert.equal(button(modal, "Thử lại").hidden, false);
  button(modal, "Hủy").click();
  assert.equal(dialog(), null);
  assert.equal(document.activeElement, opener);
});

test("an undefined result remains retryable in the same modal", async (t) => {
  let calls = 0;
  const { controller, opener } = setup(t, async () => { calls += 1; });
  controller.open({ label: "Codex", value: "codex", opener });
  const modal = dialog();

  button(modal, "Vẫn tắt").click();
  await settle();

  assert.equal(calls, 1);
  assert.equal(dialog(), modal);
  assert.equal(button(modal, "Thử lại").hidden, false);
  button(modal, "Hủy").click();
});

test("failure stays in the same modal with sanitized live error and bounded Retry", async (t) => {
  const first = deferred();
  const second = deferred();
  let calls = 0;
  const { controller, opener } = setup(t, () => {
    calls += 1;
    return calls === 1 ? first.promise : second.promise;
  });
  controller.open({ label: "Codex", value: "codex", opener });
  const modal = dialog();
  button(modal, "Vẫn tắt").click();
  first.reject(new Error("private provider token and filesystem path"));
  await settle();

  assert.equal(dialog(), modal);
  assert.equal(modal.open, true);
  assert.doesNotMatch(text(modal), /private provider token|filesystem path/u);
  const error = find(modal, (node) => hasClass(node, "provider-off-dialog-error"));
  assert.equal(error.hidden, false);
  assert.equal(error.getAttribute("role"), "alert");
  assert.equal(error.getAttribute("aria-live"), "assertive");
  assert.match(text(error), /Chưa thể tắt provider/u);
  assert.equal(button(modal, "Hủy").disabled, false);

  button(modal, "Thử lại").click();
  button(modal, "Thử lại").click();
  await flush();
  assert.equal(calls, 2);
  assert.equal(dialog(), modal);
  second.resolve(true);
  await settle();
  assert.equal(dialog(), null);
});

test("module-wide ownership prevents a second instance from opening another modal", (t) => {
  const dom = installDOM();
  const firstOpener = document.createElement("button");
  const secondOpener = document.createElement("button");
  document.body.append(firstOpener, secondOpener);
  const first = createProviderOffDialog({ listen, onConfirm: async () => true });
  const second = createProviderOffDialog({ listen, onConfirm: async () => true });
  t.after(() => { first.dispose(); second.dispose(); dom.restore(); });

  assert.equal(first.open({ label: "Codex", value: 1, opener: firstOpener }), true);
  assert.equal(second.open({ label: "Claude Code", value: 2, opener: secondOpener }), false);
  assert.equal(findAll(document.body, (node) => node.tagName === "DIALOG").length, 1);
  button(dialog(), "Hủy").click();
  assert.equal(second.open({ label: "Claude Code", value: 2, opener: secondOpener }), true);
  assert.equal(findAll(document.body, (node) => node.tagName === "DIALOG").length, 1);
});

test("dispose aborts pending confirmation, fences stale completion, and checks opener connectivity", async (t) => {
  const gate = deferred();
  let signal;
  const { controller, opener } = setup(t, (_value, receivedSignal) => {
    signal = receivedSignal;
    return gate.promise;
  });
  controller.open({ label: "Codex", value: "codex", opener });
  button(dialog(), "Vẫn tắt").click();
  await flush();

  controller.dispose();
  assert.equal(signal.aborted, true);
  assert.equal(dialog(), null);
  assert.equal(document.activeElement, opener);
  gate.resolve(false);
  await settle();
  assert.equal(dialog(), null);

  const detached = document.createElement("button");
  document.body.append(detached);
  const replacement = createProviderOffDialog({ listen, onConfirm: async () => true });
  replacement.open({ label: "Claude Code", value: "claude-code", opener: detached });
  detached.remove();
  const retainedFocus = document.createElement("span");
  document.activeElement = retainedFocus;
  replacement.dispose();
  assert.equal(document.activeElement, retainedFocus);
});
