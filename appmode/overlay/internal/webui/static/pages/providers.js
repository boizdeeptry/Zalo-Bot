import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";

const FOCUSABLE_TAGS = new Set(["BUTTON", "INPUT", "SELECT", "TEXTAREA"]);

function providerPath(id, suffix = "") {
  const value = String(id ?? "").trim();
  if (!value) throw new TypeError("Provider id is required");
  return `/llm/providers/${encodeURIComponent(value)}${suffix}`;
}

// createProviderService dựng THÂN của mọi lượt gọi tại đây thay vì chuyển tiếp thẳng đối tượng
// người gọi đưa: /llm từ chối trường lạ, nên một phím gõ thừa ở tầng trên sẽ thành 400 chứ không
// bị bỏ qua. Đóng gói ở một chỗ thì hợp đồng đó chỉ phải đúng một lần.
export function createProviderService(request = requestJSON) {
  if (typeof request !== "function") {
    throw new TypeError("Provider service requires an API request function");
  }
  return Object.freeze({
    list: () => request("/llm/providers"),
    create: ({ kind, name, enabled, credential }) => request("/llm/providers", {
      method: "POST",
      body: { kind, name, enabled: Boolean(enabled), credential: credential ?? "" },
    }),
    update: (id, { name, enabled, credential }) => request(providerPath(id), {
      method: "PUT",
      body: { name, enabled: Boolean(enabled), credential: credential ?? "" },
    }),
    remove: (id) => request(providerPath(id), { method: "DELETE" }),
    replaceCredential: (id, credential) => request(providerPath(id, "/credential"), {
      method: "PUT",
      body: { credential },
    }),
    clearCredential: (id) => request(providerPath(id, "/credential"), { method: "DELETE" }),
    testDraft: ({ kind, credential, model = "" }) => request("/llm/providers/test", {
      method: "POST",
      body: { kind, credential, model },
    }),
    testSaved: (id) => request(providerPath(id, "/test"), { method: "POST" }),
    discover: (id) => request(providerPath(id, "/discover"), { method: "POST" }),
    addModel: (id, modelID) => request(providerPath(id, "/models"), {
      method: "POST",
      body: { model_id: modelID, name: "" },
    }),
    removeModel: (id, modelID) => request(
      `${providerPath(id, "/models")}?model_id=${encodeURIComponent(modelID)}`,
      { method: "DELETE" },
    ),
  });
}

function messageOf(error) {
  return error instanceof Error ? error.message : String(error);
}

// focusableNodes duyệt cây thay vì querySelectorAll để bẫy Tab dùng CHUNG một đường cho mọi lượt
// chạy, và để danh sách luôn khớp với DOM hiện tại sau mỗi lần vẽ lại danh sách model.
function focusableNodes(root, found = []) {
  for (const child of root.childNodes ?? []) {
    if (FOCUSABLE_TAGS.has(child.tagName) && !child.disabled) found.push(child);
    focusableNodes(child, found);
  }
  return found;
}

function kindLabel(provider, kinds) {
  if (provider.system) return "Provider hệ thống";
  return kinds.find((entry) => entry.kind === provider.kind)?.label || provider.kind;
}

function statusText(provider) {
  if (provider.credential_unreadable) return "cần nhập lại API key";
  if (!provider.system && !provider.credential_configured) return "chưa có API key";
  if (provider.last_check_status === "error") {
    return `lần kiểm gần nhất hỏng: ${provider.last_error || "không rõ lý do"}`;
  }
  if (provider.last_check_status === "ok") return "đã kiểm, chạy tốt";
  return "chưa kiểm tra lần nào";
}

function summaryText(provider) {
  const models = Array.isArray(provider.models) ? provider.models : [];
  const parts = [`${models.length} model`, statusText(provider)];
  if (!provider.enabled) parts.unshift("đang tắt");
  return parts.join(" · ");
}

function providerFacts(providers, kinds, onEdit) {
  if (!providers.length) {
    return element("div", { className: "hint", text: "Chưa có Provider nào." });
  }
  return element("div", { className: "facts" }, providers.map((provider) => {
    const edit = element("button", {
      className: "btn",
      attributes: { type: "button", "aria-label": `Sửa ${provider.name}` },
      text: "Sửa",
      on: { click(event) { onEdit(provider, event.currentTarget); } },
    });
    return element("div", { className: "fact" },
      element("div", { className: "fkk", text: provider.name }),
      element("div", { className: "fvv" },
        element("span", { text: kindLabel(provider, kinds) }),
        element("span", { className: "fn", text: summaryText(provider) }),
      ),
      edit,
    );
  }));
}

function labelledField(inputId, label, control, hintNode) {
  return element("div", { className: "field" },
    element("label", { attributes: { for: inputId } }, element("span", { className: "fk", text: label })),
    control,
    hintNode,
  );
}

// providerSheet dựng sheet thêm/sửa và tự gọi service.
//
// Gọi thẳng thay vì trả callback lên trang: mọi thao tác ở đây đều báo kết quả vào CÙNG một ô
// aria-live của sheet, nên tách người gọi ra chỉ để chuyền lỗi ngược lại đúng chỗ cũ.
function providerSheet({ service, provider, kinds, editorId, onClose, onSaved }) {
  const creating = !provider;
  const system = Boolean(provider?.system);
  const idBase = `provider-sheet-${editorId}`;
  const headingId = `${idBase}-title`;
  const descriptionId = `${idBase}-note`;
  let models = Array.isArray(provider?.models) ? [...provider.models] : [];

  const note = element("span", {
    className: "note",
    attributes: { id: descriptionId, "aria-live": "polite" },
  });
  const say = (message) => { note.textContent = message; };

  const nameHint = element("span", { className: "fs" });
  const keyHint = element("span", { className: "fs" });
  const nameInput = element("input", {
    attributes: { id: `${idBase}-name`, type: "text", maxlength: 120, placeholder: "tên để nhận ra Provider này" },
  });
  // Ô khoá LUÔN rỗng, kể cả khi Provider đã có khoá: không có endpoint nào trả khoá đã lưu về, và
  // rỗng chính là cách API hiểu "giữ nguyên khoá cũ".
  const keyInput = element("input", {
    attributes: {
      id: `${idBase}-key`,
      type: "password",
      autocomplete: "new-password",
      spellcheck: false,
      placeholder: creating ? "dán API key" : "để trống nếu giữ khoá đang lưu",
    },
  });
  const enabledInput = element("input", { attributes: { id: `${idBase}-enabled`, type: "checkbox" } });
  // Loại Provider quyết định endpoint, và endpoint là thứ khoá thật được gửi tới — nên nó chỉ mở
  // lúc thêm mới, không bao giờ đổi được trên một Provider đã có khoá.
  const kindSelect = element("select", {
    attributes: { id: `${idBase}-kind`, disabled: !creating },
  }, kinds.map((entry) => element("option", { attributes: { value: entry.kind }, text: entry.label })));
  kindSelect.value = creating ? (kinds[0]?.kind ?? "") : provider.kind;
  // Endpoint đi theo ô chọn loại: nó là địa chỉ mà khoá thật sẽ được gửi tới, nên để nó đứng yên
  // sau khi người dùng đổi loại là hiện sai đúng cái điều duy nhất đáng kiểm trước khi dán khoá.
  const endpointOf = (kind) => kinds.find((entry) => entry.kind === kind)?.endpoint || "";
  const endpointNote = element("div", {
    className: "sp",
    text: system ? "chạy cục bộ, không gọi HTTP" : (provider?.endpoint || endpointOf(kindSelect.value)),
  });
  kindSelect.addEventListener("change", () => {
    endpointNote.textContent = endpointOf(kindSelect.value);
  });
  if (!creating) nameInput.value = provider.name || "";
  enabledInput.checked = creating ? true : Boolean(provider.enabled);

  // hintFields gắn lời nhắc của máy chủ vào đúng ô. /llm đã trả sẵn map fields cho việc này, nên
  // PROVIDER_CREDENTIAL_UNREADABLE và các mã khoá khác đều rơi vào cùng một nhánh.
  const hintFields = (error) => {
    const fields = error?.fields;
    if (!fields || typeof fields !== "object") return;
    if (fields.name) nameHint.textContent = fields.name;
    if (fields.credential) {
      keyHint.textContent = fields.credential;
      keyInput.focus();
    }
  };

  const guarded = (button, working, failure, action) => async () => {
    button.disabled = true;
    nameHint.textContent = "";
    keyHint.textContent = "";
    say(working);
    try {
      await action();
    } catch (error) {
      if (error?.name !== "AbortError") {
        say(`${failure}: ${messageOf(error)}`);
        hintFields(error);
      }
    } finally {
      button.disabled = false;
    }
  };

  const modelList = element("div", { className: "mlist" });
  const drawModels = () => {
    if (!models.length) {
      modelList.replaceChildren(element("div", { className: "mrow none", text: "chưa có model nào" }));
      return;
    }
    modelList.replaceChildren(...models.map((model) => {
      const remove = element("button", {
        className: "btn",
        attributes: { type: "button", "aria-label": `Xoá model ${model.model_id}` },
        text: "Xoá",
      });
      remove.addEventListener("click", guarded(remove, "đang xoá model…", "không xoá được model", async () => {
        await service.removeModel(provider.id, model.model_id);
        models = models.filter((entry) => entry.model_id !== model.model_id);
        drawModels();
        say(`Đã xoá model ${model.model_id}.`);
      }));
      return element("div", { className: "mrow" },
        element("span", { className: "mid", text: model.model_id }),
        element("span", { className: "fn", text: `${model.name || model.model_id} · ${model.source === "manual" ? "tự nhập" : "tự tải"}` }),
        remove,
      );
    }));
  };

  const modelInput = element("input", {
    className: "madd",
    attributes: { id: `${idBase}-model`, type: "text", placeholder: "mã model, ví dụ gpt-5" },
  });
  const addModel = element("button", { className: "btn", attributes: { type: "button" }, text: "Thêm model" });
  addModel.addEventListener("click", guarded(addModel, "đang thêm model…", "không thêm được model", async () => {
    const modelID = modelInput.value.trim();
    if (!modelID) {
      say("Nhập mã model rồi bấm Thêm model.");
      return;
    }
    const data = await service.addModel(provider.id, modelID);
    if (Array.isArray(data?.models)) models = data.models;
    modelInput.value = "";
    drawModels();
    say(`Đã thêm model ${modelID}.`);
  }));

  const modelSection = element("section", { className: "models" },
    element("div", { className: "mh", text: "Model dùng được" }),
    modelList,
    element("div", { className: "mrow maddrow" },
      element("label", { attributes: { for: `${idBase}-model` }, text: "Thêm thủ công" }),
      modelInput,
      addModel,
    ),
  );
  drawModels();

  const testButton = element("button", { className: "btn", attributes: { type: "button" }, text: "Kiểm tra kết nối" });
  testButton.addEventListener("click", guarded(testButton, "đang kiểm tra kết nối…", "không kiểm tra được", async () => {
    if (creating) {
      const credential = keyInput.value.trim();
      if (!credential) {
        say("Nhập API key rồi bấm Kiểm tra kết nối.");
        return;
      }
      await service.testDraft({ kind: kindSelect.value, credential });
      say("Khoá vừa nhập gọi được Provider. Bấm Lưu để thêm.");
      return;
    }
    await service.testSaved(provider.id);
    say("Kết nối tốt. Đang lấy danh sách model…");
    try {
      const data = await service.discover(provider.id);
      if (Array.isArray(data?.models)) models = data.models;
      drawModels();
      say(`Kết nối tốt. Đã lấy ${models.length} model.`);
    } catch (error) {
      if (error?.name === "AbortError") return;
      // Danh sách đang hiện được GIỮ NGUYÊN: khám phá hỏng nghĩa là không liên lạc được, không
      // phải Provider hết model — và chuỗi fallback đang trỏ vào đúng những model đó.
      say(`Kết nối tốt, nhưng không lấy được danh sách model: ${messageOf(error)}. Danh sách cũ giữ nguyên, có thể nhập mã model thủ công.`);
    }
  }));

  const replaceKey = element("button", { className: "btn", attributes: { type: "button" }, text: "Thay khoá" });
  replaceKey.addEventListener("click", guarded(replaceKey, "đang thay khoá…", "không thay được khoá", async () => {
    const credential = keyInput.value.trim();
    if (!credential) {
      say("Nhập API key mới rồi bấm Thay khoá.");
      return;
    }
    await service.replaceCredential(provider.id, credential);
    keyInput.value = "";
    say("Đã thay khoá.");
  }));

  const clearKey = element("button", { className: "btn", attributes: { type: "button" }, text: "Xoá khoá" });
  clearKey.addEventListener("click", guarded(clearKey, "đang xoá khoá…", "không xoá được khoá", async () => {
    await service.clearCredential(provider.id);
    keyInput.value = "";
    say("Đã xoá khoá. Provider này sẽ không gọi được cho tới khi nhập khoá mới.");
  }));

  const removeProvider = element("button", { className: "btn", attributes: { type: "button" }, text: "Xoá Provider" });
  removeProvider.addEventListener("click", guarded(removeProvider, "đang xoá Provider…", "không xoá được Provider", async () => {
    await service.remove(provider.id);
    await onSaved();
  }));

  const save = element("button", { className: "btn go", attributes: { type: "submit" }, text: "Lưu" });
  const close = element("button", { className: "btn", attributes: { type: "button" }, text: "Đóng", on: { click: onClose } });

  const submit = guarded(save, "đang lưu…", "không lưu được", async () => {
    const name = nameInput.value.trim();
    if (!name) {
      say("Nhập tên để nhận ra Provider này.");
      nameInput.focus();
      return;
    }
    // Ô khoá trống là trạng thái BÌNH THƯỜNG của một lần sửa, và API hiểu chuỗi rỗng là "giữ khoá
    // đang lưu". Thay và xoá khoá có nút riêng ngay bên cạnh.
    const draft = { name, enabled: Boolean(enabledInput.checked), credential: keyInput.value.trim() };
    if (creating) await service.create({ kind: kindSelect.value, ...draft });
    else await service.update(provider.id, draft);
    await onSaved();
  });

  const credentialField = labelledField(`${idBase}-key`, "API key", keyInput, [
    element("span", {
      className: "fs",
      text: creating
        ? "Khoá được mã hoá bằng kho khoá của Windows và không bao giờ hiện lại."
        : (provider.credential_unreadable
          ? "Đã lưu API key nhưng máy này không giải mã được. Nhập khoá mới rồi bấm Thay khoá."
          : (provider.credential_configured
            ? "Đã cấu hình. Portal không đọc lại được khoá đã lưu."
            : "Chưa có API key.")),
    }),
    keyHint,
  ]);

  const form = element("form", { on: { submit(event) { event.preventDefault(); void submit(); } } },
    element("div", { className: "sheethead" },
      element("div", { className: "st", attributes: { id: headingId }, text: creating ? "Thêm Provider" : `Sửa ${provider.name}` }),
      endpointNote,
    ),
    system
      ? element("div", { className: "hint", text: `${provider.name} là Provider hệ thống: nó chạy bằng Claude Code trên máy này, nên không có API key, không sửa tên và không xoá được. Chỉ danh sách model là đổi được.` })
      : [
        labelledField(`${idBase}-kind`, "Loại Provider", kindSelect, element("span", {
          className: "fs",
          text: creating ? "Endpoint cố định theo loại; chọn xong không đổi được." : "Loại chỉ chọn được lúc thêm mới.",
        })),
        labelledField(`${idBase}-name`, "Tên hiển thị", nameInput, nameHint),
        credentialField,
        element("div", { className: "field check" },
          enabledInput,
          element("label", { attributes: { for: `${idBase}-enabled` }, text: "Bật Provider này" }),
        ),
        element("div", { className: "row" }, testButton, creating ? null : [replaceKey, clearKey]),
      ],
    creating ? null : modelSection,
    element("div", { className: "sheetfoot" },
      note,
      system || creating ? null : removeProvider,
      close,
      system ? null : save,
    ),
  );

  return { form, headingId, descriptionId };
}

export function createProvidersPage({ request = requestJSON } = {}) {
  return Object.freeze({
    mount(container) {
      const controller = new AbortController();
      const service = createProviderService((path, options = {}) => request(path, {
        ...options,
        signal: controller.signal,
      }));
      const root = element("div", { className: "providers-page" });
      let disposed = false;
      let listRevision = 0;
      let sheetSerial = 0;
      let dismissSheet = () => {};
      let kinds = [];
      container.append(root);

      const header = () => pageHeader(
        "Providers",
        "Các nhà cung cấp mô hình mà bot được phép gọi. Thứ tự thử nằm ở mục Models.",
      );
      const live = element("span", { className: "note", attributes: { "aria-live": "polite" } });

      const openSheet = (provider, opener) => {
        dismissSheet();
        const back = element("div", { className: "sheetback provider-sheet" });
        let closed = false;
        const onKey = (event) => { if (event.key === "Escape") close(); };
        const dismiss = (restoreFocus) => {
          if (closed) return;
          closed = true;
          document.removeEventListener?.("keydown", onKey);
          back.remove();
          if (dismissSheet === disposeSheet) dismissSheet = () => {};
          if (restoreFocus) opener?.focus();
        };
        const close = () => dismiss(true);
        const disposeSheet = () => dismiss(false);
        dismissSheet = disposeSheet;
        document.addEventListener?.("keydown", onKey);
        back.addEventListener("click", (event) => { if (event.target === back) close(); });
        back.addEventListener("keydown", (event) => {
          if (event.key !== "Tab") return;
          const focusable = focusableNodes(back);
          if (!focusable.length) return;
          const position = focusable.indexOf(document.activeElement);
          if (event.shiftKey && position <= 0) {
            event.preventDefault();
            focusable.at(-1)?.focus();
          } else if (!event.shiftKey && (position === -1 || position === focusable.length - 1)) {
            event.preventDefault();
            focusable[0]?.focus();
          }
        });

        const sheet = providerSheet({
          service,
          provider,
          kinds,
          editorId: `${++sheetSerial}`,
          onClose: close,
          onSaved: async () => {
            dismiss(false);
            await refresh();
          },
        });
        back.append(element("div", {
          className: "sheet",
          attributes: {
            role: "dialog",
            "aria-modal": "true",
            "aria-labelledby": sheet.headingId,
            "aria-describedby": sheet.descriptionId,
          },
        }, sheet.form));
        document.body.append(back);
        queueMicrotask(() => focusableNodes(back)[0]?.focus());
      };

      const toolbar = () => element("div", { className: "row" },
        element("button", {
          className: "btn go",
          attributes: { type: "button" },
          text: "Thêm Provider",
          on: { click(event) { openSheet(null, event.currentTarget); } },
        }),
        element("button", {
          className: "btn",
          attributes: { type: "button" },
          text: "Tải lại",
          on: { click() { void refresh(); } },
        }),
        live,
      );

      // refresh chỉ chạy lúc mount, sau mỗi lượt ghi, và khi người dùng bấm Tải lại. KHÔNG hẹn giờ:
      // mỗi lượt GET /llm/providers giải mã một lần cho từng Provider để biết khoá còn mở được không.
      async function refresh() {
        const revision = ++listRevision;
        live.textContent = "Đang đọc danh sách Provider…";
        root.replaceChildren(header(), toolbar());
        try {
          const data = await service.list();
          if (disposed || revision !== listRevision) return;
          kinds = Array.isArray(data?.kinds) ? data.kinds : [];
          const providers = Array.isArray(data?.providers) ? data.providers : [];
          live.textContent = "";
          root.replaceChildren(
            header(),
            toolbar(),
            providerFacts(providers, kinds, openSheet),
            element("div", { className: "hint", text: "Khoá API được mã hoá bằng kho khoá của Windows, và Portal không đọc lại được. Chuyển sang máy hoặc tài khoản Windows khác thì phải nhập lại." }),
          );
        } catch (error) {
          if (disposed || revision !== listRevision || error?.name === "AbortError") return;
          live.textContent = "";
          root.replaceChildren(header(), toolbar(), errorPanel(error));
        }
      }

      void refresh();
      return {
        dispose() {
          disposed = true;
          listRevision++;
          dismissSheet();
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createProvidersPage().mount(container);
}
