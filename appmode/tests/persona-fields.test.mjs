import test from "node:test";
import assert from "node:assert/strict";

import {
  MAX_PERSONA_VALUE_CODE_POINTS,
  createPersonaFieldModel,
  createPersonaFields,
  validatePersonaValue,
} from "../overlay/internal/webui/static/components/persona-fields.js";
import {
  find,
  findAll,
  installDOM,
  text,
} from "./helpers/dom-harness.mjs";

test("persona fields component exposes its public API", () => {
  assert.equal(MAX_PERSONA_VALUE_CODE_POINTS, 60);
  assert.equal(typeof validatePersonaValue, "function");
  assert.equal(typeof createPersonaFieldModel, "function");
  assert.equal(typeof createPersonaFields, "function");
});

test("persona values use backend normalization and Unicode limits", () => {
  assert.deepEqual(validatePersonaValue("  Bé Mi  "), {
    ok: true,
    value: "Bé Mi",
    error: "",
  });
  assert.equal(validatePersonaValue("  ").error, "chưa điền");
  assert.equal(
    validatePersonaValue("Bé\nMi").error,
    "không được chứa xuống dòng hay {{ }}",
  );
  assert.equal(
    validatePersonaValue("Bé {{Mi").error,
    "không được chứa xuống dòng hay {{ }}",
  );
  assert.equal(
    validatePersonaValue("Bé Mi}}").error,
    "không được chứa xuống dòng hay {{ }}",
  );
  assert.equal(validatePersonaValue("ộ".repeat(60)).ok, true);
  assert.equal(validatePersonaValue("🙂".repeat(60)).ok, true);
  assert.equal(
    validatePersonaValue("🙂".repeat(61)).error,
    "dài quá 60 ký tự — đây là một cái tên, không phải một câu",
  );
});

test("field model preserves server order and keys while humanizing only labels", () => {
  const dangerousKey = `vai-trò_đặc biệt"><img src=x>`;
  const model = createPersonaFieldModel({
    display_name: "Tên cũ",
    placeholders: [
      { key: "TEN_BOT", count: 2, sample: "Mẫu từ máy chủ" },
      { key: dangerousKey, count: 1, sample: "<b>không phải HTML</b>" },
      { key: "TEN_CHUYEN_GIA", count: 1 },
      { key: dangerousKey, count: 99, sample: "bản trùng" },
    ],
  });

  assert.deepEqual(model.fields.map(({ key }) => key), [
    "TEN_BOT",
    dangerousKey,
    "TEN_CHUYEN_GIA",
  ]);
  assert.equal(model.fields[0].label, "Tên bot");
  assert.equal(model.fields[0].sample, "Mẫu từ máy chủ");
  assert.equal(model.fields[0].initialValue, "Tên cũ");
  assert.equal(model.fields[1].label.startsWith("Vai trò đặc biệt"), true);
  assert.equal(model.fields[1].key, dangerousKey);
  assert.equal(model.fields[1].sample, "<b>không phải HTML</b>");
  assert.equal(model.fields[2].label, "Tên chuyên gia");
  assert.match(model.fields[2].placeholder, /người mà tri thức thuộc về/);
  assert.equal(model.fields.some(({ kind }) => kind === "display-name"), false);
});

test("model ignores malformed hole records, de-duplicates exact keys, and keeps case-distinct keys", () => {
  const model = createPersonaFieldModel({
    placeholders: [
      null,
      {},
      { key: 7 },
      { key: "" },
      { key: "TEN_BOT", count: "bad" },
      { key: "TEN_BOT", count: 2 },
      { key: "ten_bot", count: -3 },
    ],
  });

  assert.deepEqual(model.fields.map(({ key }) => key), ["TEN_BOT", "ten_bot"]);
  assert.deepEqual(model.fields.map(({ count }) => count), [1, 1]);
});

test("TEN_BOT is authoritative and validation keeps exact value keys", () => {
  const exactKey = `key with spaces-đẹp`;
  const model = createPersonaFieldModel({
    display_name: "Tên cũ",
    placeholders: [
      { key: exactKey, count: 1 },
      { key: "TEN_BOT", count: 1 },
    ],
  });
  const result = model.validate({
    values: {
      [exactKey]: "  Giá trị  ",
      TEN_BOT: "  Bé Mi  ",
    },
    displayName: "không được dùng",
  });

  assert.deepEqual(result, {
    ok: true,
    values: { [exactKey]: "Giá trị", TEN_BOT: "Bé Mi" },
    displayName: "Bé Mi",
    errors: {},
    remaining: 0,
    firstErrorKey: null,
  });
});

test("legacy-ready persona renders and validates only a dedicated display-name field", () => {
  const model = createPersonaFieldModel({ ready: true, placeholders: [], display_name: "" });

  assert.deepEqual(model.fields.map(({ key, kind }) => ({ key, kind })), [
    { key: "display_name", kind: "display-name" },
  ]);
  assert.deepEqual(model.validate({ displayName: "  Trợ lý An  " }), {
    ok: true,
    values: {},
    displayName: "Trợ lý An",
    errors: {},
    remaining: 0,
    firstErrorKey: null,
  });
  assert.deepEqual(model.validate(), {
    ok: false,
    values: {},
    displayName: "",
    errors: { display_name: "tên hiển thị chưa điền" },
    remaining: 1,
    firstErrorKey: "display_name",
  });
});

test("DOM controller renders safely, updates the counter, advances Enter, and focuses the first error", (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const changes = [];
  let finalEnters = 0;
  const unsafeKey = `custom-key"><svg onload=alert(1)>`;
  const controller = createPersonaFields({
    agent: {
      placeholders: [
        { key: unsafeKey, count: 1, sample: "<img src=x onerror=alert(1)>" },
        { key: "TEN_BOT", count: 1 },
      ],
    },
    onChange: (result) => changes.push(result),
    onLastEnter: () => { finalEnters++ },
  });
  const host = document.createElement("div");
  controller.mount(host);

  const inputs = findAll(host, (node) => node.tagName === "INPUT");
  assert.equal(inputs.length, 2);
  assert.equal(inputs[0].getAttribute("data-persona-key"), unsafeKey);
  assert.equal(inputs[0].getAttribute("placeholder"), "<img src=x onerror=alert(1)>");
  assert.equal(text(host).includes("<img src=x onerror=alert(1)>") , true);
  assert.equal(findAll(host, (node) => node.tagName === "IMG" || node.tagName === "SVG").length, 0);
  assert.equal(controller.remaining, 2);

  inputs[1].value = "  Bé Mi  ";
  inputs[1].dispatchEvent({ type: "input" });
  assert.equal(controller.remaining, 1);
  assert.equal(changes.at(-1).displayName, "Bé Mi");

  inputs[0].focus();
  const firstEnter = { type: "keydown", key: "Enter" };
  inputs[0].dispatchEvent(firstEnter);
  assert.equal(firstEnter.defaultPrevented, true);
  assert.equal(document.activeElement, inputs[1]);

  const finalEnter = { type: "keydown", key: "Enter" };
  inputs[1].dispatchEvent(finalEnter);
  assert.equal(finalEnter.defaultPrevented, true);
  assert.equal(finalEnters, 1);

  inputs[0].value = "";
  const invalid = controller.validate();
  assert.equal(invalid.firstErrorKey, unsafeKey);
  assert.equal(controller.focusFirstError(invalid), true);
  assert.equal(document.activeElement, inputs[0]);
});

test("DOM controller read/render/dispose removes stale listeners and callbacks", (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  let changes = 0;
  const host = document.createElement("div");
  const controller = createPersonaFields({
    agent: { placeholders: [{ key: "TEN_BOT", count: 1 }] },
    onChange: () => { changes++ },
  });
  controller.mount(host);
  const staleInput = find(host, (node) => node.tagName === "INPUT");
  staleInput.value = "Cũ";
  assert.deepEqual(controller.read(), { values: { TEN_BOT: "Cũ" }, displayName: "Cũ" });

  controller.render({ placeholders: [], display_name: "Tên mới" });
  staleInput.dispatchEvent({ type: "input" });
  assert.equal(changes, 0);
  const currentInput = find(host, (node) => node.tagName === "INPUT");
  assert.equal(currentInput.value, "Tên mới");
  currentInput.dispatchEvent({ type: "input" });
  assert.equal(changes, 1);

  controller.dispose();
  currentInput.dispatchEvent({ type: "input" });
  assert.equal(changes, 1);
  assert.equal(host.childNodes.length, 0);
});
