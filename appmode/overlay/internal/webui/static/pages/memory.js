import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";

const SEARCH_DELAY_MS = 250;

function filterQuery({ query = "", pinned = false } = {}) {
  const params = new URLSearchParams();
  const trimmed = String(query).trim();
  if (trimmed) params.set("q", trimmed);
  if (pinned) params.set("pinned", "true");
  const encoded = params.toString();
  return encoded ? `?${encoded}` : "";
}

export function createMemoryService(request = requestJSON) {
  if (typeof request !== "function") throw new TypeError("Memory service requires an API request function");
  return Object.freeze({
    overview: (filters) => request(`/memory${filterQuery(filters)}`),
    thread: (threadID) => request(`/memory/threads/${encodeURIComponent(threadID)}`),
    lessons: (filters) => request(`/memory/lessons${filterQuery(filters)}`),
    createThread: (threadID, body) => request(`/memory/threads/${encodeURIComponent(threadID)}`, { method: "POST", body }),
    updateThread: (threadID, id, body) => request(`/memory/threads/${encodeURIComponent(threadID)}/${id}`, { method: "PUT", body }),
    deleteThread: (threadID, id) => request(`/memory/threads/${encodeURIComponent(threadID)}/${id}`, { method: "DELETE" }),
    createLesson: (body) => request("/memory/lessons", { method: "POST", body }),
    updateLesson: (id, body) => request(`/memory/lessons/${id}`, { method: "PUT", body }),
    deleteLesson: (id) => request(`/memory/lessons/${id}`, { method: "DELETE" }),
  });
}

function metric(value, label) {
  return element("div", { className: "card memory-metric" },
    element("div", { className: "n", text: Number(value || 0) }),
    element("div", { className: "l", text: label }),
  );
}

function formatDate(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "không rõ ngày";
  return date.toLocaleDateString("vi-VN", { day: "2-digit", month: "2-digit" });
}

function sourceText(memory) {
  if (memory?.source === "agent" && memory.source_preview) {
    return `Nguồn: tin nhắn “${memory.source_preview}”`;
  }
  if (memory?.source === "operator") return "Nguồn: người trực thêm";
  return "Nguồn: dữ liệu cũ, chưa có tin gốc đáng tin";
}

function emptyState(title, body) {
  return element("div", { className: "memory-empty" },
    element("div", { className: "memory-empty-title", text: title }),
    element("div", { className: "note", text: body }),
  );
}

function actionButton(label, onClick, { disabled = false, destructive = false } = {}) {
  const button = element("button", {
    className: `memory-action${destructive ? " is-destructive" : ""}`,
    attributes: { type: "button", disabled },
    text: label,
    on: { click: (event) => onClick(event.currentTarget) },
  });
  button.disabled = disabled;
  return button;
}

function memoryCard(memory, actions, pending) {
  return element("article", { className: "memory-entry" },
    element("div", { className: "memory-pin", attributes: { "aria-hidden": "true" }, text: memory.pinned ? "★" : "" }),
    element("div", { className: "memory-entry-body" },
      element("div", { className: "memory-entry-text", text: memory.text }),
      element("div", { className: "memory-entry-meta", text: `${formatDate(memory.updated_at || memory.created_at)} · ${sourceText(memory)}` }),
      element("div", { className: "memory-entry-actions" },
        actionButton(memory.pinned ? "Bỏ ghim" : "Ghim", (opener) => actions.pinThread(memory, opener), { disabled: pending }),
        actionButton("Sửa", (opener) => actions.editThread(memory, opener), { disabled: pending }),
        actionButton("Xoá", (opener) => actions.deleteThread(memory, opener), { disabled: pending, destructive: true }),
      ),
    ),
  );
}

function threadRow(thread, selected, onSelect) {
  const count = Number(thread.memory_count || 0);
  return element("button", {
    className: `memory-thread${selected ? " is-selected" : ""}`,
    attributes: { type: "button", "aria-pressed": String(selected) },
    on: { click: () => onSelect(thread.id) },
  },
  element("span", { className: "memory-thread-main" },
    element("span", { className: "memory-thread-name", text: thread.name || thread.id }),
    element("span", { className: "memory-thread-preview", text: thread.preview || "Chưa có ghi chú" }),
  ),
  element("span", { className: "memory-thread-count", text: `${count} ghi chú` }),
  );
}

function threadDetail(state, actions) {
  if (state.detailLoading && !state.detail) {
    return element("div", { className: "memory-detail", text: "Đang đọc ghi chú…" });
  }
  const detail = state.detail;
  if (!detail) {
    return element("div", { className: "memory-detail" },
      emptyState("Chọn một người hoặc nhóm", "Ghi chú và nguồn sẽ hiện ở đây."),
    );
  }
  const thread = detail.thread || {};
  const synchronized = Number(detail.synced_revision || 0) >= Number(detail.revision || 0);
  const syncText = synchronized
    ? `Đã đồng bộ vào session · revision ${Number(detail.revision || 0)}`
    : `Sẽ đồng bộ ở lượt Zalo kế tiếp · revision ${Number(detail.revision || 0)}`;
  const memories = Array.isArray(detail.memories) ? detail.memories : [];
  return element("section", { className: "memory-detail", attributes: { "aria-label": `Ghi chú của ${thread.name || thread.id}` } },
    element("div", { className: "memory-detail-head" },
      element("div", {},
        element("div", { className: "memory-detail-name", text: thread.name || thread.id }),
        element("div", { className: `memory-sync${synchronized ? " is-synced" : ""}`, text: syncText }),
      ),
      element("a", {
        className: "btn",
        attributes: { href: `/zalo?thread=${encodeURIComponent(thread.id || "")}` },
        text: "Mở hội thoại",
      }),
    ),
    memories.length
      ? element("div", { className: "memory-entries" }, memories.map((memory) => memoryCard(memory, actions, state.pending)))
      : emptyState("Chưa có ghi chú cho hội thoại này", "Bấm Thêm ghi chú để lưu điều cần nhớ cho riêng người hoặc nhóm này."),
  );
}

function threadsPanel(state, actions) {
  const threads = Array.isArray(state.overview?.threads) ? state.overview.threads : [];
  if (!threads.length) {
    return element("div", { className: "memory-panel", attributes: { role: "tabpanel", "aria-labelledby": "memory-tab-threads" } },
      emptyState("Chưa có người hoặc nhóm nào", "Hội thoại sẽ xuất hiện ở đây sau khi Portal nhận danh bạ hoặc tin nhắn Zalo."),
    );
  }
  return element("div", {
    className: "memory-split",
    attributes: { role: "tabpanel", "aria-labelledby": "memory-tab-threads" },
  },
  element("div", { className: "memory-thread-list", attributes: { "aria-label": "Người và nhóm" } },
    threads.map((thread) => threadRow(thread, thread.id === state.selectedThreadID, actions.selectThread)),
  ),
  threadDetail(state, actions),
  );
}

function lessonCard(lesson, actions, pending) {
  return element("article", { className: "memory-lesson" },
    element("div", { className: "memory-lesson-head" },
      element("span", { className: "memory-pin", attributes: { "aria-hidden": "true" }, text: lesson.pinned ? "★" : "" }),
      element("strong", { text: lesson.note || "Bài học chung" }),
      element("span", { className: "note", text: formatDate(lesson.updated_at || lesson.created_at) }),
    ),
    lesson.bot_text && element("div", { className: "memory-lesson-line" },
      element("span", { className: "memory-lesson-label", text: "Bot đã nói" }),
      element("span", { text: lesson.bot_text }),
    ),
    lesson.better && element("div", { className: "memory-lesson-line" },
      element("span", { className: "memory-lesson-label", text: "Nên nói" }),
      element("span", { text: lesson.better }),
    ),
    element("div", { className: "memory-entry-meta", text: lesson.thread_name ? `Hội thoại nguồn: ${lesson.thread_name}` : "Do người trực thêm trực tiếp" }),
    element("div", { className: "memory-entry-actions" },
      actionButton(lesson.pinned ? "Bỏ ghim" : "Ghim", (opener) => actions.pinLesson(lesson, opener), { disabled: pending }),
      actionButton("Sửa", (opener) => actions.editLesson(lesson, opener), { disabled: pending }),
      actionButton("Xoá", (opener) => actions.deleteLesson(lesson, opener), { disabled: pending, destructive: true }),
    ),
  );
}

function lessonsPanel(state, actions) {
  if (state.lessonsLoading && !state.lessons) {
    return element("div", { className: "memory-panel", attributes: { role: "tabpanel", "aria-labelledby": "memory-tab-lessons" }, text: "Đang đọc bài học chung…" });
  }
  const lessons = Array.isArray(state.lessons?.lessons) ? state.lessons.lessons : [];
  return element("section", { className: "memory-panel", attributes: { role: "tabpanel", "aria-labelledby": "memory-tab-lessons" } },
    element("div", { className: "memory-scope-note", text: `Áp dụng cho mọi hội thoại · revision ${Number(state.lessons?.revision || 0)}` }),
    lessons.length
      ? element("div", { className: "memory-lessons" }, lessons.map((lesson) => lessonCard(lesson, actions, state.pending)))
      : emptyState("Chưa có bài học chung", "Khi người trực sửa câu trả lời, bài học có thể được lưu ở đây."),
  );
}

function textareaField({ id, name, label, value = "", maxlength, required = false }) {
  const input = element("textarea", {
    attributes: { id, name, maxlength, required },
  });
  input.value = value;
  return {
    input,
    node: element("div", { className: "memory-dialog-field" },
      element("label", { attributes: { for: id }, text: label }),
      input,
    ),
  };
}

function pinnedField({ id, label, checked = false }) {
  const input = element("input", { attributes: { id, name: "pinned", type: "checkbox" } });
  input.checked = Boolean(checked);
  return {
    input,
    node: element("label", { className: "memory-dialog-check", attributes: { for: id } },
      input,
      element("span", { text: label }),
    ),
  };
}

function safeMutationMessage(error) {
  const message = typeof error?.message === "string" ? error.message.trim() : "";
  return message || "Không thể cập nhật Memory. Vui lòng thử lại.";
}

function renderMemoryPage(root, state, actions) {
  const metrics = state.overview?.metrics || {};
  const search = element("input", {
    attributes: {
      type: "search",
      value: state.query,
      placeholder: state.tab === "threads"
        ? "Tìm người, nhóm hoặc nội dung ghi chú…"
        : "Tìm câu bot, câu sửa hoặc lý do…",
      "aria-label": "Tìm trong Memory",
    },
    on: { input: (event) => actions.search(event.currentTarget.value) },
  });
  search.value = state.query;
  const pinned = element("input", {
    attributes: { type: "checkbox", "aria-label": "Chỉ nội dung đã ghim" },
    on: { change: (event) => actions.pinned(Boolean(event.currentTarget.checked)) },
  });
  pinned.checked = state.pinned;
  const addDisabled = state.tab === "threads" && !state.selectedThreadID;
  const add = element("button", {
    className: "btn go memory-add",
    attributes: { type: "button", disabled: addDisabled || state.pending },
    text: state.tab === "threads" ? "+ Thêm ghi chú" : "+ Thêm bài học",
    on: { click: (event) => actions.add(event.currentTarget) },
  });
  add.disabled = addDisabled || state.pending;

  const children = [
    element("div", { className: "memory-heading" },
      pageHeader("Memory", "Điều bot nhớ về từng cuộc trò chuyện và những bài học dùng chung."),
      add,
    ),
    element("div", { className: "memory-metrics cards" },
      metric(metrics.memories, "ghi chú đang dùng"),
      metric(metrics.threads, "người và nhóm"),
      metric(metrics.lessons, "bài học chung"),
    ),
    element("div", { className: "memory-tabs", attributes: { role: "tablist", "aria-label": "Phạm vi memory" } },
      element("button", {
        className: `memory-tab${state.tab === "threads" ? " is-active" : ""}`,
        attributes: { id: "memory-tab-threads", type: "button", role: "tab", "aria-selected": String(state.tab === "threads") },
        text: "Người & nhóm",
        on: { click: () => actions.tab("threads") },
      }),
      element("button", {
        className: `memory-tab${state.tab === "lessons" ? " is-active" : ""}`,
        attributes: { id: "memory-tab-lessons", type: "button", role: "tab", "aria-selected": String(state.tab === "lessons") },
        text: "Bài học chung",
        on: { click: () => actions.tab("lessons") },
      }),
    ),
    element("div", { className: "memory-filter" },
      search,
      element("label", { className: "memory-pinned-filter" }, pinned, element("span", { text: "Chỉ ghi chú đã ghim" })),
    ),
  ];

  if (state.loading && !state.overview) {
    children.push(element("div", { className: "hint", text: "Đang đọc Memory…" }));
  } else {
    children.push(state.tab === "threads" ? threadsPanel(state, actions) : lessonsPanel(state, actions));
  }
  if (state.error) children.push(errorPanel(state.error));
  const live = element("div", {
    className: "memory-live note",
    attributes: { "aria-live": "polite", role: "status" },
    text: state.message,
  });
  children.push(live);
  children.push(element("div", { className: "hint memory-boundary", text: "Memory không phải Knowledge Base và không được dùng làm nguồn trích dẫn. Thay đổi có hiệu lực ở lượt Zalo kế tiếp." }));
  root.replaceChildren(children);
  return live;
}

export function createMemoryPage({
  request = requestJSON,
  setTimeout: schedule = globalThis.setTimeout,
  clearTimeout: cancelSchedule = globalThis.clearTimeout,
} = {}) {
  return Object.freeze({
    mount(container) {
      const controller = new AbortController();
      const service = createMemoryService((path, options = {}) => request(path, { ...options, signal: controller.signal }));
      const root = element("div", { className: "memory-page" });
      const state = {
        tab: "threads", query: "", pinned: false,
        overview: null, detail: null, lessons: null,
        selectedThreadID: "", loading: true, detailLoading: false, lessonsLoading: false,
        error: null, message: "", pending: false, disposed: false,
      };
      let overviewRevision = 0;
      let detailRevision = 0;
      let lessonRevision = 0;
      let mutationRevision = 0;
      let dialogRevision = 0;
      let searchTimer = null;
      let liveRegion = null;
      let activeDialog = null;
      container.append(root);

      const render = () => {
        if (!state.disposed) liveRegion = renderMemoryPage(root, state, actions);
      };

      const announce = (message) => {
        state.message = message;
        if (liveRegion) liveRegion.textContent = message;
      };

      async function loadDetail(threadID) {
        const revision = ++detailRevision;
        state.detailLoading = true;
        render();
        try {
          const detail = await service.thread(threadID);
          if (state.disposed || revision !== detailRevision || threadID !== state.selectedThreadID) return;
          state.detail = detail;
          state.error = null;
        } catch (error) {
          if (state.disposed || revision !== detailRevision || error?.name === "AbortError") return;
          state.error = error;
        } finally {
          if (!state.disposed && revision === detailRevision) {
            state.detailLoading = false;
            render();
          }
        }
      }

      async function loadOverview() {
        const revision = ++overviewRevision;
        state.loading = true;
        render();
        try {
          const overview = await service.overview({ query: state.query, pinned: state.pinned });
          if (state.disposed || revision !== overviewRevision) return;
          state.overview = overview;
          const threads = Array.isArray(overview?.threads) ? overview.threads : [];
          if (!threads.some((thread) => thread.id === state.selectedThreadID)) {
            state.selectedThreadID = threads[0]?.id || "";
            state.detail = null;
          }
          state.error = null;
          if (state.selectedThreadID) void loadDetail(state.selectedThreadID);
        } catch (error) {
          if (state.disposed || revision !== overviewRevision || error?.name === "AbortError") return;
          state.error = error;
        } finally {
          if (!state.disposed && revision === overviewRevision) {
            state.loading = false;
            render();
          }
        }
      }

      async function loadLessons() {
        const revision = ++lessonRevision;
        state.lessonsLoading = true;
        render();
        try {
          const lessons = await service.lessons({ query: state.query, pinned: state.pinned });
          if (state.disposed || revision !== lessonRevision) return;
          state.lessons = lessons;
          state.error = null;
        } catch (error) {
          if (state.disposed || revision !== lessonRevision || error?.name === "AbortError") return;
          state.error = error;
        } finally {
          if (!state.disposed && revision === lessonRevision) {
            state.lessonsLoading = false;
            render();
          }
        }
      }

      const dismissActiveDialog = () => {
        activeDialog?.dismiss(false, true);
      };

      function openDialog({ title, description, fields = [], inputs = [], submitLabel, destructive = false, onSubmit }, opener) {
        dismissActiveDialog();
        const revision = ++dialogRevision;
        const titleID = `memory-dialog-title-${revision}`;
        const descriptionID = `memory-dialog-description-${revision}`;
        const errorID = `memory-dialog-error-${revision}`;
        const overlay = element("div", { className: "sheetback", attributes: { "data-memory-overlay": "" } });
        const error = element("div", {
          className: "memory-dialog-error note",
          attributes: { id: errorID, role: "alert", "aria-live": "assertive" },
        });
        const cancel = element("button", { className: "btn", attributes: { type: "button" }, text: "Huỷ" });
        const submit = element("button", {
          className: `btn ${destructive ? "memory-danger" : "go"}`,
          attributes: { type: "submit" },
          text: submitLabel,
        });
        const controls = [...inputs, cancel, submit];
        let pending = false;
        let closed = false;

        const dialog = {
          dismiss(restoreFocus, force = false) {
            if (closed || (pending && !force)) return;
            closed = true;
            dialogRevision++;
            document.removeEventListener?.("keydown", onDocumentKeydown);
            overlay.remove();
            if (activeDialog === dialog) activeDialog = null;
            if (restoreFocus) opener?.focus();
          },
          setError(message = "") {
            error.textContent = message;
          },
          setPending(nextPending) {
            pending = Boolean(nextPending);
            for (const control of controls) control.disabled = pending;
            submit.textContent = pending ? "Đang lưu…" : submitLabel;
          },
        };
        const close = () => dialog.dismiss(true);
        const onDocumentKeydown = (event) => {
          if (event.key === "Escape" && !pending) close();
        };
        const submitAction = () => {
          if (!pending) void onSubmit(dialog);
        };
        cancel.addEventListener("click", close);
        submit.addEventListener("click", submitAction);
        overlay.addEventListener("click", (event) => {
          if (event.target === overlay && !pending) close();
        });
        overlay.addEventListener("keydown", (event) => {
          if (event.key !== "Tab") return;
          const focusable = (overlay.querySelectorAll
            ? [...overlay.querySelectorAll("button:not([disabled]), input:not([disabled]), textarea:not([disabled])")]
            : controls.filter((control) => !control.disabled));
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
        const form = element("form", {
          on: {
            submit: (event) => {
              event.preventDefault();
              submitAction();
            },
          },
        },
        element("div", { className: "sheethead" },
          element("div", { className: "st", attributes: { id: titleID }, text: title }),
        ),
        element("div", { className: "memory-dialog-description note", attributes: { id: descriptionID }, text: description }),
        fields,
        error,
        element("div", { className: "sheetfoot" }, cancel, submit),
        );
        const sheet = element("div", {
          className: "sheet",
          attributes: {
            role: "dialog",
            "aria-modal": "true",
            "aria-labelledby": titleID,
            "aria-describedby": `${descriptionID} ${errorID}`,
          },
        }, form);
        overlay.append(sheet);
        document.body.append(overlay);
        document.addEventListener?.("keydown", onDocumentKeydown);
        activeDialog = dialog;
        queueMicrotask(() => {
          if (!closed && revision === dialogRevision) (inputs[0] || cancel).focus();
        });
        return dialog;
      }

      async function refreshAfterMutation(scope) {
        await loadOverview();
        if (scope === "lessons" && state.tab === "lessons") await loadLessons();
      }

      async function runMutation(work, { success, scope, dialog = null }) {
        if (state.pending || state.disposed) return;
        const revision = ++mutationRevision;
        state.pending = true;
        dialog?.setError();
        dialog?.setPending(true);
        if (!dialog) render();
        try {
          await work();
          if (state.disposed || revision !== mutationRevision) return;
          state.pending = false;
          announce(success);
          dialog?.dismiss(true, true);
          render();
          await refreshAfterMutation(scope);
        } catch (error) {
          if (state.disposed || revision !== mutationRevision || error?.name === "AbortError") return;
          state.pending = false;
          const message = safeMutationMessage(error);
          announce(message);
          if (dialog) {
            dialog.setPending(false);
            dialog.setError(message);
          } else {
            render();
          }
        }
      }

      function openThreadEditor(memory, opener) {
        const editing = Boolean(memory);
        const textField = textareaField({
          id: `memory-text-${dialogRevision + 1}`,
          name: "text",
          label: "Nội dung ghi chú",
          value: memory?.text || "",
          maxlength: 240,
          required: true,
        });
        const pinField = pinnedField({
          id: `memory-pinned-${dialogRevision + 1}`,
          label: "Ghim ghi chú này",
          checked: memory?.pinned,
        });
        const thread = state.detail?.thread;
        let dialog;
        dialog = openDialog({
          title: editing ? "Sửa ghi chú" : "Thêm ghi chú",
          description: `Chỉ dùng cho ${thread?.name || thread?.id || "hội thoại đã chọn"}. Có hiệu lực từ lượt Zalo kế tiếp.`,
          fields: [textField.node, pinField.node],
          inputs: [textField.input, pinField.input],
          submitLabel: "Lưu",
          onSubmit: () => {
            const body = { text: textField.input.value.trim(), pinned: Boolean(pinField.input.checked) };
            if (!body.text) {
              const message = "Hãy nhập nội dung ghi chú.";
              dialog.setError(message);
              announce(message);
              textField.input.focus();
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

      function openLessonEditor(lesson, opener) {
        const editing = Boolean(lesson);
        const botText = textareaField({
          id: `memory-bot-text-${dialogRevision + 1}`,
          name: "bot_text",
          label: "Bot đã nói (không bắt buộc)",
          value: lesson?.bot_text || "",
          maxlength: 200,
        });
        const better = textareaField({
          id: `memory-better-${dialogRevision + 1}`,
          name: "better",
          label: "Nên nói",
          value: lesson?.better || "",
          maxlength: 200,
        });
        const note = textareaField({
          id: `memory-note-${dialogRevision + 1}`,
          name: "note",
          label: "Bài học / lý do",
          value: lesson?.note || "",
          maxlength: 200,
        });
        const pinField = pinnedField({
          id: `memory-lesson-pinned-${dialogRevision + 1}`,
          label: "Ghim bài học này",
          checked: lesson?.pinned,
        });
        let dialog;
        dialog = openDialog({
          title: editing ? "Sửa bài học chung" : "Thêm bài học chung",
          description: "Bài học này áp dụng cho mọi hội thoại và không được dùng làm nguồn trích dẫn.",
          fields: [botText.node, better.node, note.node, pinField.node],
          inputs: [botText.input, better.input, note.input, pinField.input],
          submitLabel: "Lưu",
          onSubmit: () => {
            const body = {
              thread_id: lesson?.thread_id || "",
              bot_text: botText.input.value.trim(),
              better: better.input.value.trim(),
              note: note.input.value.trim(),
              pinned: Boolean(pinField.input.checked),
            };
            if (!body.better && !body.note) {
              const message = "Hãy nhập câu nên nói hoặc bài học.";
              dialog.setError(message);
              announce(message);
              better.input.focus();
              return;
            }
            const work = editing
              ? () => service.updateLesson(lesson.id, body)
              : () => service.createLesson(body);
            void runMutation(work, {
              success: editing ? "Đã lưu bài học." : "Đã thêm bài học.",
              scope: "lessons",
              dialog,
            });
          },
        }, opener);
      }

      function openDeleteDialog(scope, item, opener) {
        const threadMemory = scope === "thread";
        let dialog;
        dialog = openDialog({
          title: threadMemory ? "Xoá ghi chú?" : "Xoá bài học chung?",
          description: "Thao tác này không thể hoàn tác. Session Zalo sẽ nhận danh sách mới ở lượt kế tiếp.",
          submitLabel: "Xác nhận xoá",
          destructive: true,
          onSubmit: () => {
            const work = threadMemory
              ? () => service.deleteThread(state.selectedThreadID, item.id)
              : () => service.deleteLesson(item.id);
            void runMutation(work, {
              success: threadMemory ? "Đã xoá ghi chú." : "Đã xoá bài học.",
              scope: threadMemory ? "thread" : "lessons",
              dialog,
            });
          },
        }, opener);
      }

      const loadCurrent = () => state.tab === "threads" ? loadOverview() : loadLessons();
      const actions = {
        add(opener) {
          if (state.pending) return;
          if (state.tab === "threads") openThreadEditor(null, opener);
          else openLessonEditor(null, opener);
        },
        tab(tab) {
          if (tab === state.tab) return;
          state.tab = tab;
          state.query = "";
          state.pinned = false;
          state.error = null;
          render();
          if (tab === "lessons") void loadLessons();
          else void loadOverview();
        },
        search(value) {
          state.query = String(value);
          if (searchTimer !== null) cancelSchedule(searchTimer);
          searchTimer = schedule(() => {
            searchTimer = null;
            void loadCurrent();
          }, SEARCH_DELAY_MS);
        },
        pinned(value) {
          state.pinned = Boolean(value);
          if (searchTimer !== null) {
            cancelSchedule(searchTimer);
            searchTimer = null;
          }
          void loadCurrent();
        },
        selectThread(threadID) {
          if (threadID === state.selectedThreadID) return;
          state.selectedThreadID = threadID;
          state.detail = null;
          state.error = null;
          void loadDetail(threadID);
        },
        editThread(memory, opener) {
          if (!state.pending) openThreadEditor(memory, opener);
        },
        deleteThread(memory, opener) {
          if (!state.pending) openDeleteDialog("thread", memory, opener);
        },
        pinThread(memory) {
          const body = { text: memory.text.trim(), pinned: !memory.pinned };
          void runMutation(
            () => service.updateThread(state.selectedThreadID, memory.id, body),
            {
              success: memory.pinned ? "Đã bỏ ghim ghi chú." : "Đã ghim ghi chú.",
              scope: "thread",
            },
          );
        },
        editLesson(lesson, opener) {
          if (!state.pending) openLessonEditor(lesson, opener);
        },
        deleteLesson(lesson, opener) {
          if (!state.pending) openDeleteDialog("lessons", lesson, opener);
        },
        pinLesson(lesson) {
          const body = {
            thread_id: lesson.thread_id || "",
            bot_text: lesson.bot_text.trim(),
            better: lesson.better.trim(),
            note: lesson.note.trim(),
            pinned: !lesson.pinned,
          };
          void runMutation(
            () => service.updateLesson(lesson.id, body),
            {
              success: lesson.pinned ? "Đã bỏ ghim bài học." : "Đã ghim bài học.",
              scope: "lessons",
            },
          );
        },
      };

      render();
      void loadOverview();
      return {
        dispose() {
          if (state.disposed) return;
          state.disposed = true;
          overviewRevision++;
          detailRevision++;
          lessonRevision++;
          mutationRevision++;
          if (searchTimer !== null) cancelSchedule(searchTimer);
          dismissActiveDialog();
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createMemoryPage().mount(container);
}
