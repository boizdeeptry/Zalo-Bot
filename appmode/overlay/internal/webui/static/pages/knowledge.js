import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader, statusPanel } from "../core/ui.js";

const ACCEPTED_EXTENSIONS = ".pdf,.md,.txt,.csv,.docx,.xlsx,.pptx,.png,.jpg,.jpeg,.webp";
const POLL_INTERVAL_MS = 2000;

export function createKnowledgeService(request = requestJSON) {
  if (typeof request !== "function") {
    throw new TypeError("Knowledge service requires an API request function");
  }
  return Object.freeze({
    load: () => request("/kb"),
    upload: (formData) => request("/kb/upload", { method: "POST", body: formData }),
    start: () => request("/kb/ingest", { method: "POST" }),
    stop: () => request("/kb/ingest", { method: "DELETE" }),
  });
}

function metric(value, label) {
  return element(
    "article",
    { className: "metric-card" },
    element("strong", { text: value ?? 0 }),
    element("span", { text: label }),
  );
}

function renderFileList(container, files) {
  const names = Array.isArray(files) ? files : [];
  if (names.length === 0) {
    container.replaceChildren(element("li", { className: "file-list__empty", text: "Chưa có tệp nào" }));
    return;
  }
  container.replaceChildren(...names.map((name) => element("li", { text: name })));
}

function ingestDescription(ingest = {}) {
  if (ingest.running) {
    return { tone: "progress", title: "Đang biên soạn", body: `Agent đã chạy ${ingest.elapsed_sec || 0} giây.` };
  }
  if (ingest.done && ingest.err) {
    return { tone: "danger", title: "Lượt biên soạn gặp lỗi", body: ingest.err };
  }
  if (ingest.done) {
    return { tone: "success", title: "Đã biên soạn xong", body: "Kho wiki đã sẵn sàng để trợ lý tra cứu." };
  }
  return { tone: "neutral", title: "Sẵn sàng nhận tài liệu", body: "Tệp nguồn được giữ nguyên trong raw; chỉ wiki được biên soạn." };
}

export function createKnowledgePage({
  request = requestJSON,
  setInterval: schedule = globalThis.setInterval,
  clearInterval: cancelSchedule = globalThis.clearInterval,
  AbortController: AbortControllerImpl = globalThis.AbortController,
} = {}) {
  let timer = null;
  let controller = null;
  let disposed = false;
  let refreshAction = () => {};

  const page = {
    startPolling() {
      if (timer === null) {
        timer = schedule(() => refreshAction(), POLL_INTERVAL_MS);
      }
      return timer;
    },

    dispose() {
      disposed = true;
      if (timer !== null) {
        cancelSchedule(timer);
        timer = null;
      }
      controller?.abort();
    },

    mount(container) {
      if (disposed) throw new Error("Disposed Knowledge page cannot be mounted");
      controller = new AbortControllerImpl();
      const service = createKnowledgeService((path, options = {}) => request(path, {
        ...options,
        signal: controller.signal,
      }));
      let revision = 0;

      const status = element("div", { className: "knowledge-status" });
      const metrics = element("div", { className: "metric-grid" });
      const actionStatus = element("div", { className: "action-status", attributes: { "aria-live": "polite" } });
      const start = element("button", { className: "button button--primary", attributes: { type: "button" }, text: "Biên soạn vào wiki" });
      const stop = element("button", { className: "button button--ghost", attributes: { type: "button" }, text: "Huỷ lượt đang chạy" });
      stop.hidden = true;
      const rawList = element("ul", { className: "file-list" });
      const wikiList = element("ul", { className: "file-list" });
      const steps = element("pre", { className: "ingest-steps", attributes: { tabindex: "0" } });
      steps.hidden = true;
      const fileInput = element("input", {
        className: "visually-hidden",
        attributes: {
          id: "knowledge-files",
          type: "file",
          multiple: true,
          accept: ACCEPTED_EXTENSIONS,
        },
      });
      const choose = element("button", { className: "button button--secondary", attributes: { type: "button" }, text: "Chọn tệp" });
      const dropzone = element(
        "div",
        {
          className: "upload-zone",
          on: {
            dragover: (event) => {
              event.preventDefault();
              dropzone.classList.add("is-dragging");
            },
            dragleave: () => dropzone.classList.remove("is-dragging"),
            drop: (event) => {
              event.preventDefault();
              dropzone.classList.remove("is-dragging");
              if (event.dataTransfer?.files?.length) void uploadFiles(event.dataTransfer.files);
            },
          },
        },
        element("div", {},
          element("h3", { text: "Tải tệp nguồn" }),
          element("p", { text: "PDF, Office, Markdown, văn bản, bảng dữ liệu và hình ảnh · tối đa 50 MB mỗi tệp." }),
        ),
        choose,
        fileInput,
      );

      function applyState(data) {
        const ingest = data.ingest || {};
        status.replaceChildren(statusPanel(ingestDescription(ingest)));
        metrics.replaceChildren(
          metric(data.raw_count, "tệp nguồn trong raw"),
          metric(data.wiki_count, "trang đã biên soạn"),
        );
        start.disabled = Boolean(ingest.running);
        stop.hidden = !ingest.running;
        renderFileList(rawList, data.raw_files);
        renderFileList(wikiList, data.wiki_files);
        const lines = Array.isArray(ingest.steps) ? ingest.steps : [];
        steps.hidden = lines.length === 0;
        if (lines.length) {
          steps.textContent = lines.join("\n");
          steps.scrollTop = steps.scrollHeight;
        }
      }

      async function refresh() {
        const current = ++revision;
        try {
          const data = await service.load();
          if (!disposed && current === revision) applyState(data);
        } catch (error) {
          if (!disposed && current === revision && error?.name !== "AbortError") {
            actionStatus.replaceChildren(errorPanel(error));
          }
        }
      }
      refreshAction = () => void refresh();

      async function uploadFiles(files) {
        if (disposed || !files?.length) return;
        const form = new FormData();
        for (const file of files) form.append("file", file);
        actionStatus.textContent = `Đang tải ${files.length} tệp…`;
        try {
          const result = await service.upload(form);
          if (disposed) return;
          const saved = Array.isArray(result.saved) ? result.saved : [];
          const skipped = Array.isArray(result.skipped) ? result.skipped : [];
          actionStatus.textContent = `Đã nhận ${saved.length} tệp${skipped.length ? ` · Bỏ qua: ${skipped.join(", ")}` : ""}`;
          fileInput.value = "";
          await refresh();
        } catch (error) {
          if (!disposed && error?.name !== "AbortError") actionStatus.replaceChildren(errorPanel(error));
        }
      }

      choose.addEventListener("click", () => fileInput.click());
      fileInput.addEventListener("change", () => {
        if (fileInput.files?.length) void uploadFiles(fileInput.files);
      });
      start.addEventListener("click", async () => {
        actionStatus.textContent = "Đang bắt đầu lượt biên soạn…";
        try {
          await service.start();
          await refresh();
        } catch (error) {
          if (!disposed && error?.name !== "AbortError") actionStatus.replaceChildren(errorPanel(error));
        }
      });
      stop.addEventListener("click", async () => {
        actionStatus.textContent = "Đang huỷ…";
        try {
          await service.stop();
          await refresh();
        } catch (error) {
          if (!disposed && error?.name !== "AbortError") actionStatus.replaceChildren(errorPanel(error));
        }
      });

      container.append(
        pageHeader("Tri thức", "Đưa tài liệu nguồn vào raw, rồi biên soạn thành wiki để trợ lý tra cứu."),
        status,
        element("section", { className: "section-block", attributes: { "aria-labelledby": "knowledge-summary-title" } },
          element("div", { className: "section-heading" },
            element("h2", { attributes: { id: "knowledge-summary-title" }, text: "Kho tài liệu" }),
            element("p", { text: "Tệp nguồn không bị sửa hoặc ghi đè." }),
          ),
          metrics,
        ),
        element("section", { className: "section-block", attributes: { "aria-labelledby": "knowledge-upload-title" } },
          element("div", { className: "section-heading" },
            element("h2", { attributes: { id: "knowledge-upload-title" }, text: "Nhập và biên soạn" }),
            element("p", { text: "Biên soạn chỉ chạy khi bạn bấm và có dùng chi phí mô hình." }),
          ),
          dropzone,
          element("div", { className: "form-actions knowledge-actions" }, start, stop),
          actionStatus,
          steps,
        ),
        element("section", { className: "section-block source-columns", attributes: { "aria-label": "Danh sách tệp tri thức" } },
          element("article", { className: "source-column" }, element("h2", { text: "raw — tệp nguồn" }), rawList),
          element("article", { className: "source-column" }, element("h2", { text: "wiki — trang đã biên soạn" }), wikiList),
        ),
      );

      status.replaceChildren(statusPanel({ tone: "neutral", title: "Đang đọc kho tri thức", body: "Kiểm tra tệp nguồn và trạng thái biên soạn…" }));
      void refresh();
      page.startPolling();
      return { dispose: () => page.dispose() };
    },
  };

  return Object.freeze(page);
}

export function mount(container) {
  return createKnowledgePage().mount(container);
}
