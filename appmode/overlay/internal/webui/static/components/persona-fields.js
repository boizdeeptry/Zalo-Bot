export const MAX_PERSONA_VALUE_CODE_POINTS = 60;

const MAX_LABEL_CODE_POINTS = 80;
const MAX_SAMPLE_CODE_POINTS = 160;
const DISPLAY_NAME_KEY = "display_name";
const FRIENDLY_FIELDS = Object.freeze({
  TEN_BOT: Object.freeze({
    label: "Tên bot",
    placeholder: "ví dụ: trợ lý An — tên bot tự gọi mình",
  }),
  TEN_CHUYEN_GIA: Object.freeze({
    label: "Chuyên gia",
    placeholder: "ví dụ: Anh Nam — người mà tri thức thuộc về",
  }),
});
const DISPLAY_NAME_FIELD = Object.freeze({
  label: "Tên hiển thị",
  placeholder: "ví dụ: trợ lý An — tên bot tự gọi mình",
});

let controllerSequence = 0;

function clipCodePoints(value, maximum) {
  return [...String(value ?? "")].slice(0, maximum).join("");
}

function humanizeKey(key) {
  const separated = String(key).replace(/[-_\s]+/gu, " ").trim();
  if (!separated) return "Giá trị";
  const lowered = separated.toLocaleLowerCase("vi");
  return clipCodePoints(
    `${lowered.charAt(0).toLocaleUpperCase("vi")}${lowered.slice(1)}`,
    MAX_LABEL_CODE_POINTS,
  );
}

function normalizedCount(value) {
  return Number.isSafeInteger(value) && value > 0 ? value : 1;
}

function normalizedHoles(agent) {
  const holes = Array.isArray(agent?.placeholders) ? agent.placeholders : [];
  const result = [];
  for (const hole of holes) {
    if (!hole || typeof hole !== "object" || typeof hole.key !== "string" || !hole.key) continue;
    result.push(hole);
  }
  return result;
}

function fieldErrorLabel(field) {
  if (field.kind === "display-name") return "tên hiển thị";
  return `{{${clipCodePoints(field.key, MAX_LABEL_CODE_POINTS)}}}`;
}

function initialRawValue(input, field) {
  const values = input?.values && typeof input.values === "object" ? input.values : input;
  if (field.kind === "persona" && values && Object.hasOwn(values, field.key)) {
    return values[field.key];
  }
  if (field.kind === "display-name") {
    if (input && Object.hasOwn(input, "displayName")) return input.displayName;
    if (input && Object.hasOwn(input, "display_name")) return input.display_name;
  }
  return field.initialValue;
}

export function validatePersonaValue(rawValue) {
  const value = String(rawValue ?? "").trim();
  if (!value) return { ok: false, value, error: "chưa điền" };
  if ([...value].length > MAX_PERSONA_VALUE_CODE_POINTS) {
    return {
      ok: false,
      value,
      error: `dài quá ${MAX_PERSONA_VALUE_CODE_POINTS} ký tự — đây là một cái tên, không phải một câu`,
    };
  }
  if (/[\r\n]/u.test(value) || value.includes("{{") || value.includes("}}")) {
    return { ok: false, value, error: "không được chứa xuống dòng hay {{ }}" };
  }
  return { ok: true, value, error: "" };
}

export function createPersonaFieldModel(agent = {}) {
  const displayName = typeof agent?.display_name === "string" ? agent.display_name : "";
  const holes = normalizedHoles(agent);
  const hasBotName = holes.some(({ key }) => key === "TEN_BOT");
  const firstValues = new Map();
  const fields = holes.map((hole, index) => {
    const friendly = FRIENDLY_FIELDS[hole.key];
    const hasServerSample = typeof hole.sample === "string" && hole.sample.length > 0;
    const ownValue = typeof hole.value === "string" ? hole.value : "";
    const candidate = ownValue || (hole.key === "TEN_BOT" ? displayName : "");
    if (!firstValues.has(hole.key)) firstValues.set(hole.key, candidate);
    return Object.freeze({
      id: `hole:${index}:${hole.key}`,
      kind: "persona",
      key: hole.key,
      label: friendly?.label ?? humanizeKey(hole.key),
      count: normalizedCount(hole.count),
      sample: hasServerSample ? clipCodePoints(hole.sample, MAX_SAMPLE_CODE_POINTS) : "",
      placeholder: friendly?.placeholder ?? "điền giá trị",
      initialValue: firstValues.get(hole.key),
    });
  });
  if (!hasBotName) {
    fields.push(Object.freeze({
      id: "display-name",
      kind: "display-name",
      key: DISPLAY_NAME_KEY,
      label: DISPLAY_NAME_FIELD.label,
      count: 1,
      sample: "",
      placeholder: DISPLAY_NAME_FIELD.placeholder,
      initialValue: displayName,
    }));
  }

  function validate(input = {}) {
    const valueEntries = [];
    const errorEntries = [];
    const fieldErrorEntries = [];
    const recordedValues = new Set();
    const recordedErrors = new Set();
    let authoritativeDisplayName = "";
    let firstErrorKey = null;
    let firstErrorId = null;
    for (const field of fields) {
      const result = validatePersonaValue(initialRawValue(input, field));
      if (field.kind === "persona" && !recordedValues.has(field.key)) {
        valueEntries.push([field.key, result.value]);
        recordedValues.add(field.key);
      }
      if (field.key === "TEN_BOT" || field.kind === "display-name") {
        authoritativeDisplayName = result.value;
      }
      if (!result.ok) {
        const message = `${fieldErrorLabel(field)} ${result.error}`;
        fieldErrorEntries.push([field.id, message]);
        if (!recordedErrors.has(field.key)) {
          errorEntries.push([field.key, message]);
          recordedErrors.add(field.key);
        }
        if (firstErrorId === null) {
          firstErrorKey = field.key;
          firstErrorId = field.id;
        }
      }
    }
    return {
      ok: fieldErrorEntries.length === 0,
      values: Object.fromEntries(valueEntries),
      displayName: authoritativeDisplayName,
      errors: Object.fromEntries(errorEntries),
      fieldErrors: Object.fromEntries(fieldErrorEntries),
      remaining: fieldErrorEntries.length,
      firstErrorKey,
      firstErrorId,
    };
  }

  return Object.freeze({
    fields: Object.freeze(fields),
    validate,
    remaining(input = {}) { return validate(input).remaining; },
  });
}

function makeElement(tagName, { className = "", text = "", attributes = {} } = {}) {
  const node = document.createElement(tagName);
  if (className) node.className = className;
  if (text) node.textContent = text;
  for (const [name, value] of Object.entries(attributes)) {
    if (value !== null && value !== undefined && value !== false) {
      node.setAttribute(name, value === true ? "" : String(value));
    }
  }
  return node;
}

export function createPersonaFields({
  agent = {},
  onChange = () => {},
  onLastEnter,
  onEnterLast,
  onSubmit,
} = {}) {
  if (typeof onChange !== "function") throw new TypeError("Persona fields onChange must be a function");
  const finalEnter = onLastEnter ?? onEnterLast ?? onSubmit;
  if (finalEnter !== undefined && typeof finalEnter !== "function") {
    throw new TypeError("Persona fields final Enter hook must be a function");
  }

  const instance = ++controllerSequence;
  const root = makeElement("div", { attributes: { style: "max-width:660px" } });
  let model = createPersonaFieldModel(agent);
  let entries = [];
  let disposed = false;
  let remaining = model.fields.length;

  function detachEntries() {
    for (const entry of entries) {
      entry.input.removeEventListener("input", entry.onInput);
      entry.input.removeEventListener("keydown", entry.onKeydown);
    }
    entries = [];
  }

  function read() {
    const valueEntries = [];
    let displayName = "";
    for (const { field, input } of entries) {
      if (field.kind === "persona") valueEntries.push([field.key, input.value]);
      if (field.key === "TEN_BOT" || field.kind === "display-name") displayName = input.value;
    }
    return { values: Object.fromEntries(valueEntries), displayName };
  }

  function applyValidation(result) {
    remaining = result.remaining;
    root.setAttribute("data-persona-remaining", remaining);
    for (const { field, input } of entries) {
      const invalid = Object.hasOwn(result.fieldErrors, field.id);
      input.setAttribute("aria-invalid", String(invalid));
      if (invalid) input.setAttribute("data-persona-error", result.fieldErrors[field.id]);
      else input.removeAttribute("data-persona-error");
    }
    return result;
  }

  function validate() {
    return applyValidation(model.validate(read()));
  }

  function focusFirstError(result = validate()) {
    if (!result?.firstErrorId) return false;
    const entry = entries.find(({ field }) => field.id === result.firstErrorId);
    entry?.input.focus();
    return Boolean(entry);
  }

  function draw(nextAgent) {
    if (disposed) return;
    detachEntries();
    model = createPersonaFieldModel(nextAgent);
    const rows = model.fields.map((field, index) => {
      const inputId = `persona-field-${instance}-${index + 1}`;
      const label = makeElement("label", { attributes: { for: inputId } });
      label.append(
        makeElement("span", { className: "fk", text: field.label }),
        makeElement("span", {
          className: "fc",
          text: field.kind === "persona" ? `${field.count} chỗ trong tệp` : "tên hiển thị",
        }),
      );
      const input = makeElement("input", {
        attributes: {
          id: inputId,
          type: "text",
          placeholder: field.placeholder,
          required: true,
          "data-persona-key": field.key,
          "data-persona-kind": field.kind,
          "data-persona-field-id": field.id,
        },
      });
      input.value = field.initialValue;
      const onInput = () => {
        if (disposed) return;
        if (field.kind === "persona") {
          for (const entry of entries) {
            if (entry.input !== input && entry.field.kind === "persona" && entry.field.key === field.key) {
              entry.input.value = input.value;
            }
          }
        }
        const result = validate();
        onChange(result);
      };
      const onKeydown = (event) => {
        if (disposed || event?.key !== "Enter") return;
        event.preventDefault();
        const next = entries[index + 1]?.input;
        if (next) next.focus();
        else finalEnter?.(validate(), event);
      };
      input.addEventListener("input", onInput);
      input.addEventListener("keydown", onKeydown);
      entries.push({ field, input, onInput, onKeydown });
      const row = makeElement("div", { className: "field" });
      row.append(label, input);
      if (field.sample) row.append(makeElement("div", { className: "fs", text: field.sample }));
      return row;
    });
    root.replaceChildren(...rows);
    applyValidation(model.validate(read()));
  }

  draw(agent);

  return Object.freeze({
    element: root,
    mount(container) {
      if (disposed) return root;
      container.append(root);
      return root;
    },
    read,
    validate,
    focusFirstError,
    focusFirst() {
      entries[0]?.input.focus();
      return entries.length > 0;
    },
    render: draw,
    dispose() {
      if (disposed) return;
      disposed = true;
      detachEntries();
      root.remove();
    },
    get remaining() { return remaining; },
  });
}
