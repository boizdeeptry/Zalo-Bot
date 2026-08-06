import { requestJSON } from "../core/api.js";
import { element, pageHeader } from "../core/ui.js";

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
  return element("div", { className: "card" },
    element("div", { className: "n", text: value ?? 0 }),
    element("div", { className: "l", text: label }),
  );
}

function renderFileList(container, files) {
  const names = Array.isArray(files) ? files : [];
  if (names.length === 0) {
    container.replaceChildren(element("div", { className: "none", text: "chưa có tệp nào" }));
    return;
  }
  container.replaceChildren(...names.map((name) => element("div", { text: name })));
}

function errorMessage(error) {
  return error instanceof Error ? error.message : String(error);
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

      const cards = element("div", { className: "cards", attributes: { id: "cards" } });
      const actionStatus = element("span", {
        attributes: { id: "istate", "aria-live": "polite" },
      });
      actionStatus.style.fontSize = "12.5px";
      actionStatus.style.opacity = ".7";
      const start = element("button", {
        className: "btn",
        attributes: { id: "bIngest", type: "button" },
        text: "Biên soạn vào wiki",
      });
      const stop = element("button", {
        className: "btn",
        attributes: { id: "bStop", type: "button" },
        text: "Huỷ",
      });
      stop.style.display = "none";
      const rawList = element("div", {
        className: "flist",
        attributes: { id: "rawList" },
      });
      const wikiList = element("div", {
        className: "flist",
        attributes: { id: "wikiList" },
      });
      const steps = element("div", { attributes: { id: "steps" } });
      steps.style.display = "none";
      const fileInput = element("input", {
        attributes: {
          id: "knowledge-files",
          type: "file",
          multiple: true,
          accept: ACCEPTED_EXTENSIONS,
          tabindex: "-1",
          "aria-hidden": "true",
        },
      });
      fileInput.style.display = "none";
      const dropzone = element(
        "div",
        {
          attributes: {
            id: "drop",
            role: "button",
            tabindex: "0",
            "aria-controls": "knowledge-files",
          },
          on: {
            click: () => fileInput.click(),
            keydown: (event) => {
              if (event.key !== "Enter" && event.key !== " ") return;
              event.preventDefault();
              fileInput.click();
            },
            dragover: (event) => {
              event.preventDefault();
              dropzone.classList.add("hot");
            },
            dragleave: () => dropzone.classList.remove("hot"),
            drop: (event) => {
              event.preventDefault();
              dropzone.classList.remove("hot");
              if (event.dataTransfer?.files?.length) void uploadFiles(event.dataTransfer.files);
            },
          },
        },
        element("div", { className: "big", text: "Kéo tệp vào đây, hoặc bấm để chọn" }),
        element("div", {
          className: "small",
          text: "pdf, docx, xlsx, pptx, md, txt, csv, png, jpg · tối đa 64 MiB mỗi lượt tải",
        }),
      );

      function applyState(data) {
        const ingest = data.ingest || {};
        cards.replaceChildren(
          metric(data.raw_count, "tệp trong raw\\"),
          metric(data.wiki_count, "trang trong wiki\\"),
        );
        start.disabled = Boolean(ingest.running);
        stop.style.display = ingest.running ? "" : "none";
        renderFileList(rawList, data.raw_files);
        renderFileList(wikiList, data.wiki_files);

        if (ingest.running) {
          actionStatus.textContent = `đang biên soạn · ${ingest.elapsed_sec || 0}s`;
        } else if (ingest.done) {
          actionStatus.textContent = ingest.err ? `lỗi: ${ingest.err}` : "xong";
        } else {
          actionStatus.textContent = "";
        }

        const lines = Array.isArray(ingest.steps) ? ingest.steps : [];
        steps.style.display = lines.length ? "" : "none";
        if (!lines.length) {
          delete steps.dataset.n;
        } else if (steps.dataset.n !== String(lines.length)) {
          steps.dataset.n = String(lines.length);
          steps.textContent = lines.join("\n");
          steps.scrollTop = steps.scrollHeight;
        }
      }

      async function refresh() {
        const current = ++revision;
        try {
          const data = await service.load();
          if (!disposed && current === revision) {
            applyState(data);
            return true;
          }
        } catch (error) {
          if (!disposed && current === revision && error?.name !== "AbortError") {
            actionStatus.textContent = `không đọc được trạng thái: ${errorMessage(error)}`;
          }
        }
        return false;
      }
      refreshAction = () => void refresh();

      async function uploadFiles(files) {
        if (disposed || !files?.length) return;
        const form = new FormData();
        for (const file of files) form.append("file", file);
        actionStatus.textContent = `đang tải ${files.length} tệp…`;
        try {
          const result = await service.upload(form);
          if (disposed) return;
          const saved = Array.isArray(result.saved) ? result.saved : [];
          const skipped = Array.isArray(result.skipped) ? result.skipped : [];
          const message = `đã nhận ${saved.length} tệp${skipped.length ? ` · bỏ qua: ${skipped.join(", ")}` : ""}`;
          fileInput.value = "";
          if (await refresh()) actionStatus.textContent = message;
        } catch (error) {
          if (!disposed && error?.name !== "AbortError") {
            actionStatus.textContent = `tải lên thất bại: ${errorMessage(error)}`;
          }
        }
      }

      fileInput.addEventListener("change", () => {
        if (fileInput.files?.length) void uploadFiles(fileInput.files);
      });
      start.addEventListener("click", async () => {
        if (disposed) return;
        actionStatus.textContent = "đang bắt đầu lượt biên soạn…";
        try {
          await service.start();
          await refresh();
        } catch (error) {
          if (!disposed && error?.name !== "AbortError") {
            actionStatus.textContent = `không bắt đầu được: ${errorMessage(error)}`;
          }
        }
      });
      stop.addEventListener("click", async () => {
        if (disposed) return;
        actionStatus.textContent = "đang huỷ…";
        try {
          await service.stop();
          await refresh();
        } catch (error) {
          if (!disposed && error?.name !== "AbortError") {
            actionStatus.textContent = `không huỷ được: ${errorMessage(error)}`;
          }
        }
      });

      container.append(
        pageHeader(
          "Knowledge",
          "Tải tệp nguồn lên, rồi để agent biên soạn thành trang wiki. Bot trả lời khách từ wiki.",
        ),
        cards,
        dropzone,
        fileInput,
        element("div", { className: "row" }, start, stop, actionStatus),
        steps,
        element("div", { className: "cols" },
          element("div", {},
            element("h3", { text: "raw\\ — tệp nguồn" }),
            rawList,
          ),
          element("div", {},
            element("h3", { text: "wiki\\ — trang đã biên soạn" }),
            wikiList,
          ),
        ),
        element("div", { className: "hint" },
          element("div", {
            text: "Biên soạn gọi mô hình, nên nó tốn phí trên tài khoản Claude của bạn. Nó chỉ chạy khi bạn bấm.",
          }),
        ),
      );

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
