import { element } from "../core/ui.js";

const MEMORY_CATEGORIES = Object.freeze([
  ["profile", "Hồ sơ"],
  ["family", "Gia đình"],
  ["interest", "Sở thích"],
  ["preference", "Ưu tiên"],
  ["health", "Sức khoẻ"],
  ["financial", "Tài chính"],
  ["address", "Địa chỉ"],
  ["identity", "Danh tính"],
  ["order", "Đơn hàng"],
]);

export function currentMemoryMutationBody(state) {
  const detail = state?.detail;
  if (!detail) throw new Error("Phạm vi Memory chưa sẵn sàng. Hãy tải lại rồi thử lại.");
  const uid = typeof state.selectedSubjectUID === "string" ? state.selectedSubjectUID : "";
  const loadedUID = typeof detail.selected_uid === "string" ? detail.selected_uid : uid;
  if (loadedUID !== uid) {
    throw new Error("Phạm vi Memory đang thay đổi. Hãy đợi tải xong rồi thử lại.");
  }
  const rawRevision = (uid ? detail.subject_revision : detail.common_revision) ?? detail.revision;
  const expectedRevision = Number(rawRevision);
  if (!Number.isSafeInteger(expectedRevision) || expectedRevision < 0) {
    throw new Error("Revision Memory không hợp lệ. Hãy tải lại rồi thử lại.");
  }
  return { uid, expected_revision: expectedRevision };
}

function textField({ id, name, label, value = "", maxlength = 240, multiline = false, readOnly = false }) {
  const input = element(multiline ? "textarea" : "input", {
    attributes: {
      id, name, maxlength,
      ...(multiline ? {} : { type: "text" }),
      ...(readOnly ? { readonly: true, "aria-readonly": "true" } : {}),
    },
  });
  input.value = value;
  return {
    input,
    node: element("div", { className: `memory-dialog-field${readOnly ? " is-readonly" : ""}` },
      element("label", { attributes: { for: id }, text: label }),
      input,
    ),
  };
}

function categoryField({ id, value = "profile" }) {
  const input = element("select", {
    attributes: { id, name: "category", required: true },
  }, MEMORY_CATEGORIES.map(([category, label]) =>
    element("option", { attributes: { value: category }, text: label })));
  input.value = value;
  return {
    input,
    node: element("div", { className: "memory-dialog-field" },
      element("label", { attributes: { for: id }, text: "Loại thông tin" }),
      input,
    ),
  };
}

function pinnedField({ id, checked = false }) {
  const input = element("input", { attributes: { id, name: "pinned", type: "checkbox" } });
  input.checked = Boolean(checked);
  return {
    input,
    node: element("label", { className: "memory-dialog-check", attributes: { for: id } },
      input,
      element("span", { text: "Ghim ghi chú này" }),
    ),
  };
}

function selectedScopeLabel(state) {
  const detail = state.detail || {};
  const thread = detail.thread || {};
  const uid = state.selectedSubjectUID || "";
  if (thread.thread_type === "group" && !uid) return `phạm vi chung của ${thread.name || "nhóm đã chọn"}`;
  const member = Array.isArray(detail.members)
    ? detail.members.find((candidate) => candidate.uid === uid)
    : null;
  return member?.name || thread.name || "hội thoại đã chọn";
}

function memoryFields(state, memory, { includePinned = false } = {}) {
  const body = {
    ...currentMemoryMutationBody(state),
    memory_key: String(memory?.memory_key || "").trim(),
    text: String(memory?.text || "").trim(),
    category: String(memory?.category || "").trim(),
  };
  if (includePinned) body.pinned = Boolean(memory?.pinned);
  return body;
}

function reportLocalError(dialog, announce, input, message) {
  dialog.setError(message);
  announce(message);
  input?.focus();
}

export function createThreadMemoryActions({ state, service, openDialog, runMutation, announce }) {
  let editorRevision = 0;

  const nextID = (name) => `memory-${name}-${++editorRevision}`;

  function openEditor(memory, opener) {
    const editing = Boolean(memory);
    const key = textField({
      id: nextID("key"), name: "memory_key", label: "Khoá ghi nhớ",
      value: memory?.memory_key || "", maxlength: 80,
    });
    const value = textField({
      id: nextID("text"), name: "text", label: "Nội dung ghi chú",
      value: memory?.text || "", maxlength: 240, multiline: true,
    });
    const category = categoryField({ id: nextID("category"), value: memory?.category || "profile" });
    const pinned = pinnedField({ id: nextID("pinned"), checked: memory?.pinned });
    let dialog;
    dialog = openDialog({
      title: editing ? "Sửa ghi chú" : "Thêm ghi chú",
      description: `Áp dụng cho ${selectedScopeLabel(state)}. Có hiệu lực từ lượt Zalo kế tiếp.`,
      fields: [key.node, value.node, category.node, pinned.node],
      inputs: [key.input, value.input, category.input, pinned.input],
      submitLabel: "Lưu",
      onSubmit: () => {
        const body = {
          uid: "",
          expected_revision: 0,
          memory_key: key.input.value.trim(),
          text: value.input.value.trim(),
          category: category.input.value.trim(),
          pinned: Boolean(pinned.input.checked),
        };
        try {
          Object.assign(body, currentMemoryMutationBody(state));
        } catch (error) {
          reportLocalError(dialog, announce, null, error.message);
          return;
        }
        if (!body.memory_key) {
          reportLocalError(dialog, announce, key.input, "Hãy nhập khoá ghi nhớ.");
          return;
        }
        if (!body.text) {
          reportLocalError(dialog, announce, value.input, "Hãy nhập nội dung ghi chú.");
          return;
        }
        if (!MEMORY_CATEGORIES.some(([candidate]) => candidate === body.category)) {
          reportLocalError(dialog, announce, category.input, "Hãy chọn loại thông tin hợp lệ.");
          return;
        }
        const work = editing
          ? () => service.updateThread(state.selectedThreadID, memory.id, body)
          : () => service.createThread(state.selectedThreadID, body);
        void runMutation(work, {
          success: editing ? "Đã lưu ghi chú." : "Đã thêm ghi chú.",
          scope: "thread",
          dialog,
        });
      },
    }, opener);
  }

  function openApproveEditor(memory, opener) {
    const key = textField({
      id: nextID("approve-key"), name: "memory_key", label: "Khoá ghi nhớ",
      value: memory.memory_key || "", maxlength: 80,
    });
    const value = textField({
      id: nextID("approve-text"), name: "text", label: "Nội dung đề xuất",
      value: memory.text || "", maxlength: 240, multiline: true,
    });
    const category = categoryField({ id: nextID("approve-category"), value: memory.category || "profile" });
    const confidence = textField({
      id: nextID("confidence"), name: "confidence", label: "Độ tin cậy",
      value: `${Math.round(Number(memory.confidence || 0) * 100)}%`, readOnly: true,
    });
    let dialog;
    dialog = openDialog({
      title: "Sửa rồi duyệt đề xuất",
      description: `Xem lại đề xuất cho ${selectedScopeLabel(state)} trước khi duyệt.`,
      fields: [key.node, value.node, category.node, confidence.node],
      inputs: [key.input, value.input, category.input, confidence.input],
      submitLabel: "Duyệt",
      onSubmit: () => {
        let body;
        try {
          body = {
            ...currentMemoryMutationBody(state),
            memory_key: key.input.value.trim(),
            text: value.input.value.trim(),
            category: category.input.value.trim(),
          };
        } catch (error) {
          reportLocalError(dialog, announce, null, error.message);
          return;
        }
        if (!body.memory_key) {
          reportLocalError(dialog, announce, key.input, "Hãy nhập khoá ghi nhớ.");
          return;
        }
        if (!body.text) {
          reportLocalError(dialog, announce, value.input, "Hãy nhập nội dung đề xuất.");
          return;
        }
        if (!MEMORY_CATEGORIES.some(([candidate]) => candidate === body.category)) {
          reportLocalError(dialog, announce, category.input, "Hãy chọn loại thông tin hợp lệ.");
          return;
        }
        void runMutation(
          () => service.approve(state.selectedThreadID, memory.id, body),
          { success: "Đã duyệt đề xuất.", scope: "thread", dialog },
        );
      },
    }, opener);
  }

  function openConfirmation({ title, description, submitLabel, success, work }, opener) {
    let dialog;
    dialog = openDialog({
      title, description, submitLabel, destructive: true,
      onSubmit: () => {
        let body;
        try {
          body = currentMemoryMutationBody(state);
        } catch (error) {
          reportLocalError(dialog, announce, null, error.message);
          return;
        }
        void runMutation(() => work(body), { success, scope: "thread", dialog });
      },
    }, opener);
  }

  function runDirect(memory, operation, success) {
    let body;
    try {
      body = operation === "approve"
        ? memoryFields(state, memory)
        : currentMemoryMutationBody(state);
    } catch (error) {
      announce(error.message);
      return;
    }
    const work = operation === "approve"
      ? () => service.approve(state.selectedThreadID, memory.id, body)
      : () => service.restore(state.selectedThreadID, memory.id, body);
    void runMutation(work, { success, scope: "thread" });
  }

  return Object.freeze({
    addThread: (opener) => openEditor(null, opener),
    editThread: (memory, opener) => openEditor(memory, opener),
    editApproveThread: (memory, opener) => openApproveEditor(memory, opener),
    approveThread: (memory) => runDirect(memory, "approve", "Đã duyệt đề xuất."),
    restoreThread: (memory) => runDirect(memory, "restore", "Đã khôi phục ghi chú."),
    rejectThread(memory, opener) {
      openConfirmation({
        title: "Từ chối đề xuất?",
        description: "Đề xuất sẽ được loại bỏ khỏi phạm vi Memory đang chọn.",
        submitLabel: "Xác nhận từ chối",
        success: "Đã từ chối đề xuất.",
        work: (body) => service.reject(state.selectedThreadID, memory.id, body),
      }, opener);
    },
    deleteThread(memory, opener) {
      openConfirmation({
        title: "Xoá ghi chú?",
        description: "Thao tác này không thể hoàn tác. Session Zalo sẽ nhận danh sách mới ở lượt kế tiếp.",
        submitLabel: "Xác nhận xoá",
        success: "Đã xoá ghi chú.",
        work: (body) => service.deleteThread(state.selectedThreadID, memory.id, body),
      }, opener);
    },
    pinThread(memory) {
      let body;
      const nextPinned = memory.status === "expired" ? true : !memory.pinned;
      try {
        body = memoryFields(state, { ...memory, pinned: nextPinned }, { includePinned: true });
      } catch (error) {
        announce(error.message);
        return;
      }
      void runMutation(
        () => service.updateThread(state.selectedThreadID, memory.id, body),
        {
          success: nextPinned ? "Đã ghim ghi chú." : "Đã bỏ ghim ghi chú.",
          scope: "thread",
        },
      );
    },
  });
}
