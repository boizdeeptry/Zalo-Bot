import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";
import {
  MAX_PERSONA_VALUE_CODE_POINTS,
  createPersonaFields,
  validatePersonaValue,
} from "../components/persona-fields.js";

const PLACEHOLDER_PATTERN = /\{\{([A-Z][A-Z_]*)\}\}/g;
const MAX_PLACEHOLDER_VALUE = MAX_PERSONA_VALUE_CODE_POINTS;
const EDITABLE_DOCUMENTS = Object.freeze({
  persona: Object.freeze({ label: "Văn phong" }),
  roster: Object.freeze({ label: "Sổ tay thành viên" }),
});
const HINTS = Object.freeze({
  TEN_BOT: "ví dụ: trợ lý An — tên bot tự gọi mình",
  TEN_CHUYEN_GIA: "ví dụ: Anh Nam — người mà tri thức thuộc về",
});

export function scanHoles(text) {
  const counts = new Map();
  for (const match of String(text ?? "").matchAll(PLACEHOLDER_PATTERN)) {
    counts.set(match[1], (counts.get(match[1]) || 0) + 1);
  }
  return [...counts].sort(([a], [b]) => a.localeCompare(b)).map(([key, count]) => ({ key, count }));
}

function documentPath(name) {
  if (!Object.hasOwn(EDITABLE_DOCUMENTS, name)) throw new RangeError(`Unsupported agent document: ${name}`);
  return `/agent/persona/${name}`;
}

export function createAgentService(request = requestJSON) {
  if (typeof request !== "function") throw new TypeError("Agent service requires an API request function");
  return Object.freeze({
    load: () => request("/agent"),
    fill: (values, displayName) => request("/agent", {
      method: "PUT",
      body: displayName === undefined
        ? { values }
        : { values, display_name: displayName },
    }),
    loadDocument: (name) => request(documentPath(name)),
    saveDocument: (name, text) => request(documentPath(name), { method: "PUT", body: { text } }),
  });
}

function validateQuickValue(key, rawValue) {
  const result = validatePersonaValue(rawValue);
  if (!result.ok) throw new Error(`{{${key}}} ${result.error}`);
  return result.value;
}

export function fillHoles(text, values) {
  let filled = String(text ?? "");
  const known = new Set(scanHoles(filled).map((hole) => hole.key));
  for (const [key, rawValue] of Object.entries(values || {})) {
    if (known.has(key)) filled = filled.replaceAll(`{{${key}}}`, validateQuickValue(key, rawValue));
  }
  return filled;
}

export function handleQuickFillEnter(event, apply) {
  if (event?.key !== "Enter") return false;
  event.preventDefault();
  apply();
  return true;
}

function shortPath(path) {
  if (!path) return "(chưa đặt)";
  const marker = String(path).toLowerCase().indexOf("\\brain\\");
  return marker >= 0 ? `brain\\${String(path).slice(marker + 7)}` : String(path);
}

function fact(label, value, note, edit) {
  const row = element("div", { className: "fact" },
    element("div", { className: "fkk", text: label }),
    element("div", { className: "fvv" }, element("span", { text: value || "—" }), note && element("span", { className: "fn", text: note })),
  );
  if (edit) row.append(edit);
  return row;
}

function agentFacts(agent, openDocument) {
  const pencil = (name) => element("button", {
    className: "pen",
    attributes: { type: "button", title: "Sửa nội dung tệp", "aria-label": `Sửa ${EDITABLE_DOCUMENTS[name].label}` },
    // U+1F589 LOWER LEFT PENCIL, không phải U+270E: U+270E để mũi bút ở dưới-PHẢI, ngược với
    // mọi icon sửa mà người dùng đã quen, nên nó đọc ra như một cái bút bị lật.
    text: "🖉",
    on: { click(event) { openDocument(name, event.currentTarget); } },
  });
  return element("div", { className: "facts" },
    fact("Mô hình", agent.model || "(mặc định)", "đổi ở mục Combos"),
    fact("Phạm vi quyền", Array.isArray(agent.tools) ? agent.tools.join(", ") : "", "CHỈ ĐỌC, không đổi được từ đây"),
    fact("Tệp văn phong", agent.persona_name, `${Math.round((agent.persona_size || 0) / 1024)} KB`, pencil("persona")),
    fact("Sổ tay thành viên", shortPath(agent.roster_path), null, pencil("roster")),
    fact("Luật riêng từng nhóm", shortPath(agent.overlay_dir)),
    fact("Thư mục tri thức", (Array.isArray(agent.kb_roots) ? agent.kb_roots : []).map(shortPath).join("  ·  ")),
  );
}

function quickFill(agent, onSave) {
  const holes = Array.isArray(agent?.placeholders) ? agent.placeholders : [];
  if (!holes.some((hole) => hole && typeof hole.key === "string" && hole.key)) return null;
  const note = element("span", { className: "note", attributes: { "aria-live": "polite" } });
  const save = element("button", { className: "btn go", attributes: { type: "submit" }, text: "Lưu văn phong" });
  let saving = false;
  let form;
  let fields;
  const submit = async () => {
    if (saving) return;
    note.textContent = "";
    const validation = fields.validate();
    if (!validation.ok) {
      note.textContent = `không lưu được: ${validation.fieldErrors[validation.firstErrorId]}`;
      fields.focusFirstError(validation);
      return;
    }
    saving = true;
    save.disabled = true;
    note.textContent = "đang lưu…";
    try {
      await onSave(validation.values, validation.displayName);
    } catch (error) {
      saving = false;
      note.textContent = `không lưu được: ${error instanceof Error ? error.message : String(error)}`;
      save.disabled = false;
      fields.focusFirst();
    }
  };
  fields = createPersonaFields({ agent, onLastEnter: () => { void submit(); } });
  form = element("form", {
    on: { submit(event) { event.preventDefault(); void submit(); } },
  }, fields.element, element("div", { className: "row" }, save, note));
  return Object.freeze({ node: form, dispose: fields.dispose });
}

function documentEditor(name, data, { onCancel, onSave }, editorId) {
  const textareaId = `agent-editor-${editorId}-text`;
  const headingId = `agent-editor-${editorId}-title`;
  const descriptionId = `agent-editor-${editorId}-description`;
  const textarea = element("textarea", { attributes: { id: textareaId, rows: 18, spellcheck: false } });
  textarea.value = String(data?.text ?? "");
  const quick = element("div", { className: "quick" });
  const quickNote = element("span", { className: "note", attributes: { "aria-live": "polite" } });
  const footerNote = element("span", { className: "note", attributes: { "aria-live": "polite" } });
  const save = element("button", { className: "btn go", attributes: { type: "submit" }, text: "Lưu" });

  function drawQuick() {
    const holes = scanHoles(textarea.value);
    if (!holes.length) {
      quick.className = "quick done";
      quick.replaceChildren(element("span", { className: "qok", text: "✓ Không còn chỗ trống nào trong nội dung này" }));
      return;
    }
    const inputs = new Map();
    const apply = () => {
      try {
        const values = {};
        for (const [key, input] of inputs) if (input.value.trim()) values[key] = input.value;
        if (!Object.keys(values).length) throw new Error("chưa điền ô nào");
        textarea.value = fillHoles(textarea.value, values);
        drawQuick();
        textarea.focus();
      } catch (error) { quickNote.textContent = error instanceof Error ? error.message : String(error); }
    };
    const fields = holes.map((hole) => {
      const inputId = `agent-editor-${editorId}-hole-${hole.key}`;
      const input = element("input", { attributes: { id: inputId, type: "text", maxlength: MAX_PLACEHOLDER_VALUE, placeholder: HINTS[hole.key] || "điền giá trị" }, on: { keydown: (event) => handleQuickFillEnter(event, apply) } });
      inputs.set(hole.key, input);
      return element("div", { className: "qf" }, element("label", { attributes: { for: inputId } }, element("span", { className: "fk", text: `{{${hole.key}}}` }), element("span", { className: "fc", text: `${hole.count} chỗ` })), input);
    });
    quick.className = "quick";
    quick.replaceChildren(
      element("div", { className: "qh", text: `Điền nhanh ${holes.length} chỗ trống, nếu không muốn sửa nội dung:` }),
      element("div", { className: "qg" }, fields),
      element("div", { className: "qr" }, element("button", { className: "btn", attributes: { type: "button" }, text: "Điền vào nội dung", on: { click: apply } }), quickNote),
    );
  }

  const closeButton = element("button", { className: "btn", attributes: { type: "button" }, text: "Đóng", on: { click: onCancel } });
  const form = element("form", { on: { submit: async (event) => {
    event.preventDefault();
    footerNote.textContent = "đang lưu…";
    save.disabled = true;
    try { await onSave(textarea.value); } catch (error) {
      footerNote.textContent = `không lưu được: ${error instanceof Error ? error.message : String(error)}`;
      save.disabled = false;
      textarea.focus();
    }
  } } },
  element("div", { className: "sheethead" }, element("div", { className: "st", attributes: { id: headingId }, text: `Sửa ${data?.label || EDITABLE_DOCUMENTS[name].label}` }), element("div", { className: "sp", text: data?.path || "" })),
  quick, element("label", { className: "visually-hidden", attributes: { for: textareaId }, text: data?.label || EDITABLE_DOCUMENTS[name].label }), textarea,
  element("div", { className: "sheetfoot" }, element("span", { className: "note", attributes: { id: descriptionId }, text: "Có hiệu lực ngay ở lượt trả lời sau, không cần mở lại phần mềm." }), footerNote, closeButton, save),
  );
  drawQuick();
  queueMicrotask(() => textarea.focus());
  return { form, textarea, closeButton, save, headingId, descriptionId };
}

export function createAgentsPage({ request = requestJSON } = {}) {
  return Object.freeze({ mount(container) {
    const controller = new AbortController();
    const service = createAgentService((path, options = {}) => request(path, { ...options, signal: controller.signal }));
    const root = element("div", { className: "agents-page" });
    let disposed = false;
    let refreshRevision = 0;
    let editorRevision = 0;
    let dismissEditor = () => {};
    let disposeQuickFill = () => {};
    container.append(root);
    const header = () => pageHeader("AI Agents", "Một agent: con trả lời tin nhắn Zalo. Giọng nói của nó nằm trong tệp văn phong.");
    const refresh = async () => {
      const revision = ++refreshRevision;
      disposeQuickFill();
      disposeQuickFill = () => {};
      root.replaceChildren(header(), element("div", { className: "hint", text: "Đang đọc văn phong và các chỗ cần hoàn thiện…" }));
      try {
        const agent = await service.load();
        if (disposed || revision !== refreshRevision) return;
        const holes = Array.isArray(agent?.placeholders) ? agent.placeholders : [];
        const banner = element("section", { className: `banner ${agent?.ready ? "ok" : "bad"}`, attributes: { role: "status" } },
          element("div", { className: "bt", text: agent?.ready ? "Sẵn sàng nói chuyện với khách" : `CHƯA sẵn sàng: văn phong còn ${holes.length} chỗ trống` }),
          element("div", { className: "bd", text: agent?.ready ? "Không còn chỗ trống nào trong tệp văn phong." : "Điền hết bên dưới rồi bấm Lưu. Chưa điền thì bot sẽ gửi cho khách nguyên chữ trong ngoặc." }),
        );
        const openDocument = async (name, opener) => {
          dismissEditor();
          const current = ++editorRevision;
          try {
            const data = await service.loadDocument(name);
            if (disposed || current !== editorRevision) return;
            const back = element("div", { className: "sheetback", attributes: { "data-agents-overlay": "" } });
            let closed = false;
            const onKey = (event) => { if (event.key === "Escape") close(); };
            const dismiss = (restoreFocus) => {
              if (closed) return;
              closed = true;
              editorRevision++;
              document.removeEventListener?.("keydown", onKey);
              back.remove();
              if (dismissEditor === disposeEditor) dismissEditor = () => {};
              if (restoreFocus) opener?.focus();
            };
            const close = () => dismiss(true);
            const disposeEditor = () => dismiss(false);
            dismissEditor = disposeEditor;
            document.addEventListener?.("keydown", onKey);
            back.addEventListener("click", (event) => { if (event.target === back) close(); });
            const editor = documentEditor(name, data, { onCancel: close, onSave: async (value) => {
              await service.saveDocument(name, value);
              if (disposed || closed || current !== editorRevision) return;
              dismiss(false);
              await refresh();
            } }, current);
            const sheet = element("div", {
              className: "sheet",
              attributes: { role: "dialog", "aria-modal": "true", "aria-labelledby": editor.headingId, "aria-describedby": editor.descriptionId },
            }, editor.form);
            back.addEventListener("keydown", (event) => {
              if (event.key !== "Tab") return;
              const focusable = back.querySelectorAll
                ? [...back.querySelectorAll("button:not([disabled]), input:not([disabled]), textarea:not([disabled])")]
                : [editor.textarea, editor.closeButton, editor.save];
              const position = focusable.indexOf(document.activeElement);
              if (event.shiftKey && position <= 0) {
                event.preventDefault();
                focusable.at(-1)?.focus();
              } else if (!event.shiftKey && (position === -1 || position === focusable.length - 1)) {
                event.preventDefault();
                focusable[0]?.focus();
              }
            });
            back.append(sheet);
            document.body.append(back);
          } catch (error) {
            if (!disposed && current === editorRevision && error?.name !== "AbortError") root.append(errorPanel(error));
          }
        };
        const personaFields = quickFill(agent, async (values, displayName) => {
          await service.fill(values, displayName);
          if (!disposed) await refresh();
        });
        if (personaFields) disposeQuickFill = personaFields.dispose;
        const children = [header(), banner];
        if (personaFields) children.push(personaFields.node);
        children.push(agentFacts(agent, openDocument), element("div", { className: "hint", text: "Phạm vi quyền cố định là chỉ-đọc, và đó là chủ đích: agent này tự động trả lời khách, nên nó không có quyền ghi hay xoá bất cứ gì. Con agent biên soạn wiki ở mục Knowledge mới có quyền ghi." }));
        root.replaceChildren(...children);
      } catch (error) {
        if (!disposed && revision === refreshRevision && error?.name !== "AbortError") root.replaceChildren(header(), errorPanel(error));
      }
    };
    void refresh();
    return { dispose() { disposed = true; refreshRevision++; editorRevision++; disposeQuickFill(); dismissEditor(); controller.abort(); } };
  } });
}

export function mount(container) { return createAgentsPage().mount(container); }
