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

function memoryCard(memory) {
  return element("article", { className: "memory-entry" },
    element("div", { className: "memory-pin", attributes: { "aria-hidden": "true" }, text: memory.pinned ? "★" : "" }),
    element("div", { className: "memory-entry-body" },
      element("div", { className: "memory-entry-text", text: memory.text }),
      element("div", { className: "memory-entry-meta", text: `${formatDate(memory.updated_at || memory.created_at)} · ${sourceText(memory)}` }),
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

function threadDetail(state) {
  if (state.detailLoading) {
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
      ? element("div", { className: "memory-entries" }, memories.map(memoryCard))
      : emptyState("Chưa có ghi chú cho hội thoại này", "Bấm Thêm ghi chú để lưu điều cần nhớ cho riêng người hoặc nhóm này."),
  );
}

function threadsPanel(state, selectThread) {
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
    threads.map((thread) => threadRow(thread, thread.id === state.selectedThreadID, selectThread)),
  ),
  threadDetail(state),
  );
}

function lessonCard(lesson) {
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
  );
}

function lessonsPanel(state) {
  if (state.lessonsLoading) {
    return element("div", { className: "memory-panel", attributes: { role: "tabpanel", "aria-labelledby": "memory-tab-lessons" }, text: "Đang đọc bài học chung…" });
  }
  const lessons = Array.isArray(state.lessons?.lessons) ? state.lessons.lessons : [];
  return element("section", { className: "memory-panel", attributes: { role: "tabpanel", "aria-labelledby": "memory-tab-lessons" } },
    element("div", { className: "memory-scope-note", text: `Áp dụng cho mọi hội thoại · revision ${Number(state.lessons?.revision || 0)}` }),
    lessons.length
      ? element("div", { className: "memory-lessons" }, lessons.map(lessonCard))
      : emptyState("Chưa có bài học chung", "Khi người trực sửa câu trả lời, bài học có thể được lưu ở đây."),
  );
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
    attributes: { type: "button", disabled: addDisabled },
    text: state.tab === "threads" ? "+ Thêm ghi chú" : "+ Thêm bài học",
  });
  add.disabled = addDisabled;

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
    children.push(state.tab === "threads" ? threadsPanel(state, actions.selectThread) : lessonsPanel(state));
  }
  if (state.error) children.push(errorPanel(state.error));
  children.push(element("div", {
    className: "memory-live note",
    attributes: { "aria-live": "polite", role: "status" },
    text: state.message,
  }));
  children.push(element("div", { className: "hint memory-boundary", text: "Memory không phải Knowledge Base và không được dùng làm nguồn trích dẫn. Thay đổi có hiệu lực ở lượt Zalo kế tiếp." }));
  root.replaceChildren(children);
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
        error: null, message: "", disposed: false,
      };
      let overviewRevision = 0;
      let detailRevision = 0;
      let lessonRevision = 0;
      let searchTimer = null;
      container.append(root);

      const render = () => {
        if (!state.disposed) renderMemoryPage(root, state, actions);
      };

      async function loadDetail(threadID) {
        const revision = ++detailRevision;
        state.detailLoading = true;
        state.detail = null;
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

      const loadCurrent = () => state.tab === "threads" ? loadOverview() : loadLessons();
      const actions = {
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
          state.error = null;
          void loadDetail(threadID);
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
          if (searchTimer !== null) cancelSchedule(searchTimer);
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createMemoryPage().mount(container);
}
