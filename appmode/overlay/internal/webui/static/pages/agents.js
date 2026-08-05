import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader, statusPanel } from "../core/ui.js";

const PLACEHOLDER_PATTERN = /\{\{([A-Z][A-Z_]*)\}\}/g;
const MAX_PLACEHOLDER_VALUE = 60;
const EDITABLE_DOCUMENTS = Object.freeze({
  persona: Object.freeze({ label: "Văn phong", description: "Giọng nói và nguyên tắc trả lời của trợ lý." }),
  roster: Object.freeze({ label: "Sổ tay thành viên", description: "Tên gọi và ngữ cảnh riêng của những người trợ lý cần nhận diện." }),
});

export function scanHoles(text) {
  const counts = new Map();
  for (const match of String(text ?? "").matchAll(PLACEHOLDER_PATTERN)) {
    counts.set(match[1], (counts.get(match[1]) || 0) + 1);
  }
  return [...counts]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, count]) => ({ key, count }));
}

function documentPath(name) {
  if (!Object.hasOwn(EDITABLE_DOCUMENTS, name)) {
    throw new RangeError(`Unsupported agent document: ${name}`);
  }
  return `/agent/persona/${name}`;
}

export function createAgentService(request = requestJSON) {
  if (typeof request !== "function") {
    throw new TypeError("Agent service requires an API request function");
  }
  return Object.freeze({
    load: () => request("/agent"),
    fill: (values) => request("/agent", { method: "PUT", body: { values } }),
    loadDocument: (name) => request(documentPath(name)),
    saveDocument: (name, text) => request(documentPath(name), { method: "PUT", body: { text } }),
  });
}

function fieldLabel(key) {
  return key.toLowerCase().split("_").map((part) => (
    part ? part[0].toUpperCase() + part.slice(1) : ""
  )).join(" ");
}

function validateQuickValue(key, rawValue) {
  const value = String(rawValue ?? "").trim();
  if (!value) throw new Error(`{{${key}}} chưa được điền.`);
  if ([...value].length > MAX_PLACEHOLDER_VALUE) {
    throw new Error(`{{${key}}} không được dài quá ${MAX_PLACEHOLDER_VALUE} ký tự.`);
  }
  if (/[\r\n]/.test(value) || value.includes("{{") || value.includes("}}")) {
    throw new Error(`{{${key}}} không được chứa xuống dòng hay dấu {{ }}.`);
  }
  return value;
}

export function fillHoles(text, values) {
  let filled = String(text ?? "");
  const known = new Set(scanHoles(filled).map((hole) => hole.key));
  for (const [key, rawValue] of Object.entries(values || {})) {
    if (!known.has(key)) continue;
    const value = validateQuickValue(key, rawValue);
    filled = filled.replaceAll(`{{${key}}}`, value);
  }
  return filled;
}

function detailRow(label, value) {
  return element(
    "div",
    { className: "detail-row" },
    element("dt", { text: label }),
    element("dd", { text: value || "—" }),
  );
}

function agentDetails(agent) {
  const roots = Array.isArray(agent.kb_roots) && agent.kb_roots.length
    ? agent.kb_roots.join(" · ")
    : "Chưa cấu hình";
  const tools = Array.isArray(agent.tools) ? agent.tools.join(", ") : "—";
  return element(
    "section",
    { className: "section-block", attributes: { "aria-labelledby": "agent-config-title" } },
    element(
      "div",
      { className: "section-heading" },
      element("h2", { attributes: { id: "agent-config-title" }, text: "Cấu hình đang dùng" }),
      element("p", { text: "Quyền công cụ là chỉ đọc và không đổi từ Portal." }),
    ),
    element(
      "dl",
      { className: "detail-grid" },
      detailRow("Mô hình", agent.model),
      detailRow("Tệp văn phong", agent.persona_name),
      detailRow("Kho tri thức", roots),
      detailRow("Công cụ", tools),
    ),
  );
}

function quickFillSection(agent, onSubmit) {
  const holes = Array.isArray(agent.placeholders) ? agent.placeholders : [];
  if (holes.length === 0) {
    return element(
      "section",
      { className: "section-block", attributes: { "aria-labelledby": "quick-fill-title" } },
      element("div", { className: "section-heading" },
        element("h2", { attributes: { id: "quick-fill-title" }, text: "Danh tính bắt buộc" }),
        element("p", { text: "Đã điền đủ." }),
      ),
      element("p", { className: "empty-note", text: "Không còn chỗ trống dạng {{TEN_HOA}} trong văn phong." }),
    );
  }

  const inputs = new Map();
  const feedback = element("div", { className: "form-feedback", attributes: { "aria-live": "polite" } });
  const fields = holes.map((hole) => {
    const input = element("input", {
      className: "text-input",
      attributes: {
        id: `agent-hole-${hole.key}`,
        name: hole.key,
        maxlength: MAX_PLACEHOLDER_VALUE,
        autocomplete: "off",
        required: true,
      },
    });
    inputs.set(hole.key, input);
    return element(
      "div",
      { className: "form-field" },
      element("label", { attributes: { for: input.id } },
        element("span", { text: fieldLabel(hole.key) }),
        element("code", { text: `{{${hole.key}}}` }),
      ),
      input,
      element("p", {
        className: "field-help",
        text: hole.sample || `Xuất hiện ${hole.count} lần trong văn phong.`,
      }),
    );
  });
  const submit = element("button", { className: "button button--primary", attributes: { type: "submit" }, text: "Lưu các tên" });
  const form = element(
    "form",
    {
      className: "quick-fill-form",
      on: {
        submit: async (event) => {
          event.preventDefault();
          feedback.replaceChildren();
          try {
            const values = Object.fromEntries([...inputs].map(([key, input]) => [key, validateQuickValue(key, input.value)]));
            submit.disabled = true;
            submit.textContent = "Đang lưu…";
            await onSubmit(values);
          } catch (error) {
            feedback.replaceChildren(errorPanel(error));
            submit.disabled = false;
            submit.textContent = "Lưu các tên";
          }
        },
      },
    },
    fields,
    element("div", { className: "form-actions" }, submit),
    feedback,
  );

  return element(
    "section",
    { className: "section-block", attributes: { "aria-labelledby": "quick-fill-title" } },
    element("div", { className: "section-heading" },
      element("h2", { attributes: { id: "quick-fill-title" }, text: "Điền nhanh danh tính" }),
      element("p", { text: `${holes.length} mục cần hoàn thiện` }),
    ),
    form,
  );
}

function editorSection(onOpen) {
  const editorRegion = element("div", {
    className: "editor-region",
    attributes: { id: "agent-editor-region", "aria-live": "polite" },
  });
  const buttons = [];
  const cards = Object.entries(EDITABLE_DOCUMENTS).map(([name, document]) => {
    const open = element("button", {
      className: "button button--secondary",
      attributes: {
        type: "button",
        "aria-controls": editorRegion.id,
        "aria-expanded": "false",
      },
      text: `Mở ${document.label}`,
    });
    buttons.push(open);
    open.addEventListener("click", () => {
      for (const button of buttons) button.setAttribute("aria-expanded", "false");
      open.setAttribute("aria-expanded", "true");
      onOpen(name, editorRegion, {
        opener: open,
        close() {
          open.setAttribute("aria-expanded", "false");
          open.focus();
        },
      });
    });
    return element(
      "article",
      { className: "document-card" },
      element("div", {},
        element("h3", { text: document.label }),
        element("p", { text: document.description }),
      ),
      open,
    );
  });
  return element(
    "section",
    { className: "section-block", attributes: { "aria-labelledby": "agent-documents-title" } },
    element("div", { className: "section-heading" },
      element("h2", { attributes: { id: "agent-documents-title" }, text: "Tệp hướng dẫn" }),
      element("p", { text: "Lưu UTF-8, không BOM, giữ bản gốc .goc." }),
    ),
    element("div", { className: "document-grid" }, cards),
    editorRegion,
  );
}

function documentEditor(document, data, { onCancel, onSave }) {
  const textarea = element("textarea", {
    className: "document-editor",
    attributes: {
      id: `agent-document-${document}`,
      rows: 18,
      spellcheck: true,
      "aria-describedby": `agent-document-${document}-help`,
    },
  });
  textarea.value = data.text || "";
  const quickFill = element("div", { className: "quick-fill-form editor-quick-fill" });
  const feedback = element("div", { className: "form-feedback", attributes: { "aria-live": "polite" } });
  const save = element("button", { className: "button button--primary", attributes: { type: "submit" }, text: "Lưu tệp" });
  function drawQuickFill() {
    const holes = scanHoles(textarea.value);
    if (holes.length === 0) {
      quickFill.replaceChildren(element("p", { className: "empty-note", text: "Không còn chỗ trống nào trong nội dung này." }));
      return;
    }
    const inputs = new Map();
    const note = element("div", { className: "action-status", attributes: { "aria-live": "polite" } });
    const fields = holes.map((hole) => {
      const input = element("input", {
        className: "text-input",
        attributes: { type: "text", maxlength: MAX_PLACEHOLDER_VALUE, autocomplete: "off" },
      });
      inputs.set(hole.key, input);
      return element("div", { className: "form-field" },
        element("label", {},
          element("code", { text: `{{${hole.key}}}` }),
          element("span", { text: `${hole.count} chỗ` }),
        ),
        input,
      );
    });
    const apply = element("button", {
      className: "button button--secondary",
      attributes: { type: "button" },
      text: "Điền vào nội dung",
      on: {
        click: () => {
          note.replaceChildren();
          try {
            const values = {};
            for (const [key, input] of inputs) {
              if (input.value.trim()) values[key] = input.value;
            }
            if (Object.keys(values).length === 0) throw new Error("Chưa điền ô nào.");
            textarea.value = fillHoles(textarea.value, values);
            drawQuickFill();
            textarea.focus();
          } catch (error) {
            note.replaceChildren(errorPanel(error));
          }
        },
      },
    });
    quickFill.replaceChildren(
      element("p", { className: "form-feedback", text: `Điền nhanh ${holes.length} chỗ trống vào bản nháp trước khi lưu:` }),
      fields,
      element("div", { className: "form-actions" }, apply),
      note,
    );
  }

  const form = element(
    "form",
    {
      className: "document-editor-form",
      on: {
        submit: async (event) => {
          event.preventDefault();
          feedback.replaceChildren();
          save.disabled = true;
          save.textContent = "Đang lưu…";
          try {
            await onSave(textarea.value);
          } catch (error) {
            feedback.replaceChildren(errorPanel(error));
            save.disabled = false;
            save.textContent = "Lưu tệp";
          }
        },
      },
    },
    element("div", { className: "editor-heading" },
      element("div", {},
        element("h3", { text: data.label || EDITABLE_DOCUMENTS[document].label }),
        element("p", { attributes: { id: `agent-document-${document}-help` }, text: "Nội dung được chuẩn hóa xuống dòng và ghi an toàn dưới dạng UTF-8." }),
      ),
      element("code", { text: data.path || "" }),
    ),
    quickFill,
    element("label", { className: "visually-hidden", attributes: { for: textarea.id }, text: data.label || EDITABLE_DOCUMENTS[document].label }),
    textarea,
    element("div", { className: "form-actions" },
      save,
      element("button", { className: "button button--ghost", attributes: { type: "button" }, text: "Đóng", on: { click: onCancel } }),
    ),
    feedback,
  );
  drawQuickFill();
  queueMicrotask(() => textarea.focus());
  return form;
}

export function createAgentsPage({ request = requestJSON } = {}) {
  return Object.freeze({
    mount(container) {
      const controller = new AbortController();
      const service = createAgentService((path, options = {}) => request(path, { ...options, signal: controller.signal }));
      const root = element("div", { className: "agents-page" });
      let disposed = false;
      let refreshRevision = 0;
      let editorRevision = 0;
      container.append(root);

      const header = () => pageHeader(
        "Trợ lý AI",
        "Hoàn thiện danh tính, văn phong và sổ tay trước khi trợ lý trò chuyện với khách hàng.",
      );

      const refresh = async () => {
        const revision = ++refreshRevision;
        root.replaceChildren(
          header(),
          statusPanel({ tone: "neutral", title: "Đang kiểm tra trợ lý", body: "Đọc văn phong và các chỗ cần hoàn thiện…" }),
        );
        try {
          const agent = await service.load();
          if (disposed || revision !== refreshRevision) return;
          const holes = Array.isArray(agent.placeholders) ? agent.placeholders : [];
          root.replaceChildren(
            header(),
            statusPanel(agent.ready ? {
              tone: "success",
              title: "Trợ lý đã sẵn sàng",
              body: "Văn phong không còn chỗ trống bắt buộc. Bạn có thể tiếp tục kiểm tra hội thoại.",
            } : {
              tone: "progress",
              title: "Trợ lý chưa sẵn sàng",
              body: `Còn ${holes.length} mục danh tính cần điền trước khi mở cho khách thật.`,
            }),
            agentDetails(agent),
            quickFillSection(agent, async (values) => {
              await service.fill(values);
              await refresh();
            }),
            editorSection(async (name, region, editorControls) => {
              const currentEditor = ++editorRevision;
              region.replaceChildren(statusPanel({ tone: "neutral", title: "Đang mở tệp", body: "Đọc nội dung UTF-8…" }));
              try {
                const data = await service.loadDocument(name);
                if (disposed || currentEditor !== editorRevision) return;
                region.replaceChildren(documentEditor(name, data, {
                  onCancel: () => {
                    editorRevision++;
                    region.replaceChildren();
                    editorControls.close();
                  },
                  onSave: async (text) => {
                    await service.saveDocument(name, text);
                    await refresh();
                  },
                }));
              } catch (error) {
                if (!disposed && currentEditor === editorRevision && error?.name !== "AbortError") {
                  region.replaceChildren(errorPanel(error));
                  editorControls.close();
                }
              }
            }),
          );
        } catch (error) {
          if (!disposed && revision === refreshRevision && error?.name !== "AbortError") {
            root.replaceChildren(header(), errorPanel(error));
          }
        }
      };

      void refresh();
      return {
        dispose() {
          disposed = true;
          refreshRevision++;
          editorRevision++;
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createAgentsPage().mount(container);
}
