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

test("persona normalization matches Go whitespace edges and rejects internal controls", () => {
  const goSpaces = [
    "\u0009",
    "\u0085",
    "\u00a0",
    "\u1680",
    "\u2007",
    "\u2028",
    "\u2029",
    "\u202f",
    "\u205f",
    "\u3000",
  ];
  for (const space of goSpaces) {
    assert.deepEqual(validatePersonaValue(`${space}Bé Mi${space}`), {
      ok: true,
      value: "Bé Mi",
      error: "",
    });
  }
  assert.equal(validatePersonaValue("\u0085").error, "chưa điền");

  for (const value of [
    "Bé\u0085Mi",
    "Bé\u0000Mi",
    "Bé\tMi",
    "Bé\u001bMi",
    "Bé\u2028Mi",
    "Bé\u2029Mi",
  ]) {
    const result = validatePersonaValue(value);
    assert.equal(result.ok, false, JSON.stringify(value));
    assert.notEqual(result.error, "", JSON.stringify(value));
  }
});

test("persona validation preserves combining marks and ZWJ emoji while counting code points", () => {
  assert.deepEqual(validatePersonaValue("  a\u0301o  "), {
    ok: true,
    value: "a\u0301o",
    error: "",
  });
  const family = "👨‍👩‍👧‍👦";
  const exactlySixty = `${family.repeat(8)}🙂🙂🙂🙂`;
  assert.equal([...exactlySixty].length, 60);
  assert.equal(validatePersonaValue(exactlySixty).ok, true);
  assert.equal(validatePersonaValue(`${exactlySixty}a`).ok, false);
});

test("field model preserves every server record and key while humanizing only labels", () => {
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
    dangerousKey,
  ]);
  assert.deepEqual(model.fields.map(({ id }) => id), [
    "hole:0",
    "hole:1",
    "hole:2",
    "hole:3",
  ]);
  assert.equal(model.fields[0].label, "Tên bot");
  assert.equal(model.fields[0].sample, "Mẫu từ máy chủ");
  assert.equal(model.fields[0].placeholder, "ví dụ: trợ lý An — tên bot tự gọi mình");
  assert.equal(model.fields[0].initialValue, "Tên cũ");
  assert.equal(model.fields[1].label.startsWith("Vai trò đặc biệt"), true);
  assert.equal(model.fields[1].key, dangerousKey);
  assert.equal(model.fields[1].sample, "<b>không phải HTML</b>");
  assert.equal(model.fields[1].placeholder, "điền giá trị");
  assert.equal(model.fields[2].label, "Chuyên gia");
  assert.match(model.fields[2].placeholder, /người mà tri thức thuộc về/);
  assert.equal(model.fields[3].sample, "bản trùng");
  assert.equal(model.fields.some(({ kind }) => kind === "display-name"), false);
});

test("model ignores malformed records but preserves repeated and case-distinct hole records", () => {
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

  assert.deepEqual(model.fields.map(({ key }) => key), ["TEN_BOT", "TEN_BOT", "ten_bot"]);
  assert.deepEqual(model.fields.map(({ count }) => count), [1, 2, 1]);
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
    fieldErrors: {},
    remaining: 0,
    firstErrorKey: null,
    firstErrorId: null,
  });
});

test("legacy-ready persona renders and validates only a dedicated display-name field", () => {
  const model = createPersonaFieldModel({ ready: true, placeholders: [], display_name: "" });

  assert.deepEqual(model.fields.map(({ key, kind }) => ({ key, kind })), [
    { key: "display_name", kind: "display-name" },
  ]);
  assert.equal(model.fields[0].id, "display-name");
  assert.deepEqual(model.validate({ displayName: "  Trợ lý An  " }), {
    ok: true,
    values: {},
    displayName: "Trợ lý An",
    errors: {},
    fieldErrors: {},
    remaining: 0,
    firstErrorKey: null,
    firstErrorId: null,
  });
  assert.deepEqual(model.validate(), {
    ok: false,
    values: {},
    displayName: "",
    errors: { display_name: "tên hiển thị chưa điền" },
    fieldErrors: { "display-name": "tên hiển thị chưa điền" },
    remaining: 1,
    firstErrorKey: "display_name",
    firstErrorId: "display-name",
  });
});

test("valid persisted display name stays authoritative without a duplicate field", () => {
  const model = createPersonaFieldModel({
    display_name: "\u0085  Trợ lý An \u3000",
    placeholders: [{ key: "don-vi", count: 1 }],
  });
  assert.deepEqual(model.fields.map(({ kind, key }) => ({ kind, key })), [
    { kind: "persona", key: "don-vi" },
  ]);
  assert.deepEqual(model.validate({ values: { "don-vi": " Công ty Mở " } }), {
    ok: true,
    values: { "don-vi": "Công ty Mở" },
    displayName: "Trợ lý An",
    errors: {},
    fieldErrors: {},
    remaining: 0,
    firstErrorKey: null,
    firstErrorId: null,
  });

  const ready = createPersonaFieldModel({ display_name: "  Trợ lý An  ", placeholders: [] });
  assert.equal(ready.fields.length, 0);
  assert.equal(ready.validate().displayName, "Trợ lý An");
  assert.equal(ready.validate().ok, true);
});

test("invalid persisted display name fails closed with an editable dedicated field", () => {
  const model = createPersonaFieldModel({
    display_name: "Tên\u0000bot",
    placeholders: [{ key: "don-vi", count: 1 }],
  });
  assert.deepEqual(model.fields.map(({ kind }) => kind), ["persona", "display-name"]);
  const result = model.validate({ values: { "don-vi": "Công ty Mở" } });
  assert.equal(result.ok, false);
  assert.equal(result.firstErrorKey, "display_name");
  assert.equal(result.firstErrorId, "display-name");
});

test("synthetic display name stays distinct from legitimate display_name holes", () => {
  const model = createPersonaFieldModel({
    placeholders: [
      { key: "display_name", count: 1 },
      { key: "display_name", count: 2 },
    ],
  });
  assert.deepEqual(model.fields.map(({ id, key }) => ({ id, key })), [
    { id: "hole:0", key: "display_name" },
    { id: "hole:1", key: "display_name" },
    { id: "display-name", key: "display_name" },
  ]);

  const missingDedicated = model.validate({
    values: { display_name: "Giá trị trong persona" },
    displayName: "",
  });
  assert.equal(missingDedicated.remaining, 1);
  assert.equal(missingDedicated.firstErrorKey, "display_name");
  assert.equal(missingDedicated.firstErrorId, "display-name");
  assert.deepEqual(missingDedicated.fieldErrors, {
    "display-name": "tên hiển thị chưa điền",
  });

  const missingHoles = model.validate({
    values: { display_name: "" },
    displayName: "Tên bot",
  });
  assert.equal(missingHoles.remaining, 2);
  assert.equal(missingHoles.firstErrorKey, "display_name");
  assert.equal(missingHoles.firstErrorId, "hole:0");
  assert.deepEqual(Object.keys(missingHoles.fieldErrors), [
    "hole:0",
    "hole:1",
  ]);
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
  assert.equal(inputs[0].getAttribute("data-persona-key"), null);
  assert.equal(inputs[0].getAttribute("data-persona-field-id"), "hole:0");
  assert.equal(
    [...inputs[0].attributes.values()].some((value) => value.includes(unsafeKey)),
    false,
  );
  assert.equal(inputs[0].getAttribute("placeholder"), "điền giá trị");
  assert.equal(text(host).includes("<img src=x onerror=alert(1)>") , true);
  assert.equal(findAll(host, (node) => node.tagName === "IMG" || node.tagName === "SVG").length, 0);
  assert.equal(controller.remaining, 2);

  inputs[1].value = "  Bé Mi  ";
  inputs[1].dispatchEvent({ type: "input" });
  assert.equal(controller.remaining, 1);
  assert.equal(changes.at(-1).displayName, "Bé Mi");

  inputs[0].focus();
  const composingFirst = { type: "keydown", key: "Enter", isComposing: true };
  inputs[0].dispatchEvent(composingFirst);
  assert.equal(composingFirst.defaultPrevented, false);
  assert.equal(document.activeElement, inputs[0]);
  assert.equal(finalEnters, 0);

  const imeFirst = { type: "keydown", key: "Enter", keyCode: 229 };
  inputs[0].dispatchEvent(imeFirst);
  assert.equal(imeFirst.defaultPrevented, false);
  assert.equal(document.activeElement, inputs[0]);

  const firstEnter = { type: "keydown", key: "Enter" };
  inputs[0].dispatchEvent(firstEnter);
  assert.equal(firstEnter.defaultPrevented, true);
  assert.equal(document.activeElement, inputs[1]);

  const composingFinal = { type: "keydown", key: "Enter", isComposing: true };
  inputs[1].dispatchEvent(composingFinal);
  assert.equal(composingFinal.defaultPrevented, false);
  assert.equal(document.activeElement, inputs[1]);
  assert.equal(finalEnters, 0);

  const imeFinal = { type: "keydown", key: "Enter", keyCode: 229 };
  inputs[1].dispatchEvent(imeFinal);
  assert.equal(imeFinal.defaultPrevented, false);
  assert.equal(document.activeElement, inputs[1]);
  assert.equal(finalEnters, 0);

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

test("DOM controller synchronizes repeated keys and focuses collision errors by internal identity", (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const host = document.createElement("div");
  const controller = createPersonaFields({
    agent: {
      placeholders: [
        { key: "display_name", count: 1 },
        { key: "display_name", count: 2 },
      ],
    },
  });
  controller.mount(host);
  const inputs = findAll(host, (node) => node.tagName === "INPUT");
  assert.equal(inputs.length, 3);

  inputs[0].value = "Giá trị một";
  inputs[0].dispatchEvent({ type: "input" });
  assert.equal(inputs[1].value, "Giá trị một");
  let result = controller.validate();
  assert.equal(result.firstErrorId, "display-name");
  controller.focusFirstError(result);
  assert.equal(document.activeElement, inputs[2]);

  inputs[2].value = "Tên bot";
  inputs[2].dispatchEvent({ type: "input" });
  inputs[1].value = "";
  inputs[1].dispatchEvent({ type: "input" });
  assert.equal(inputs[0].value, "");
  result = controller.validate();
  assert.equal(result.firstErrorId, "hole:0");
  controller.focusFirstError(result);
  assert.equal(document.activeElement, inputs[0]);
});

test("input events allow 60 astral code points and reject the 61st without native maxlength", (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const changes = [];
  const host = document.createElement("div");
  const controller = createPersonaFields({
    agent: { placeholders: [{ key: "TEN_BOT", count: 1 }] },
    onChange: (result) => changes.push(result),
  });
  controller.mount(host);
  const input = find(host, (node) => node.tagName === "INPUT");
  assert.equal(input.getAttribute("maxlength"), null);

  input.value = "🙂".repeat(60);
  input.dispatchEvent({ type: "input" });
  assert.equal(changes.at(-1).ok, true);
  assert.equal(changes.at(-1).remaining, 0);

  input.value = "🙂".repeat(61);
  input.dispatchEvent({ type: "input" });
  assert.equal(changes.at(-1).ok, false);
  assert.equal(changes.at(-1).remaining, 1);
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

  controller.render({ placeholders: [], display_name: "" });
  staleInput.dispatchEvent({ type: "input" });
  assert.equal(changes, 0);
  const currentInput = find(host, (node) => node.tagName === "INPUT");
  assert.equal(currentInput.value, "");
  currentInput.dispatchEvent({ type: "input" });
  assert.equal(changes, 1);

  controller.dispose();
  currentInput.dispatchEvent({ type: "input" });
  assert.equal(changes, 1);
  assert.equal(host.childNodes.length, 0);
});
