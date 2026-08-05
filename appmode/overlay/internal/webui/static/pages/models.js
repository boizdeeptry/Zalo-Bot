import { requestJSON } from "../core/api.js";
import { waitForServerRestart } from "../core/state.js";
import { element, errorPanel, pageHeader, statusPanel } from "../core/ui.js";

const MODEL_INFO = Object.freeze({
  haiku: Object.freeze({ name: "Haiku", tag: "Nhanh nhất, tiết kiệm nhất", description: "Phù hợp hỏi đáp thường ngày dựa trên wiki." }),
  sonnet: Object.freeze({ name: "Sonnet", tag: "Cân bằng", description: "Suy luận tốt hơn khi cần nối nhiều nguồn." }),
  opus: Object.freeze({ name: "Opus", tag: "Sâu nhất, chi phí cao nhất", description: "Dành cho câu hỏi khó cần suy luận sâu." }),
});

export function createModelService(request = requestJSON) {
  if (typeof request !== "function") {
    throw new TypeError("Model service requires an API request function");
  }
  return Object.freeze({
    load: () => request("/kb/model"),
    save: (model) => request("/kb/model", { method: "PUT", body: { model } }),
  });
}

function phaseMessage(phase) {
  switch (phase) {
    case "stopping": return "Đã lưu. Đang chờ daemon cũ tắt…";
    case "starting": return "Daemon đã tắt. Đang chờ bản mới khởi động…";
    case "ready": return "Daemon mới đã sẵn sàng. Đang tải lại trang…";
    case "timeout": return "Khởi động lại lâu hơn dự kiến. Hãy mở Start.vbs nếu Portal không trở lại.";
    default: return "Đang áp dụng mô hình…";
  }
}

export function createModelsPage({
  request = requestJSON,
  waitForRestart = waitForServerRestart,
  reload = () => globalThis.location?.reload(),
  AbortController: AbortControllerImpl = globalThis.AbortController,
} = {}) {
  return Object.freeze({
    mount(container) {
      const controller = new AbortControllerImpl();
      const service = createModelService((path, options = {}) => request(path, {
        ...options,
        signal: controller.signal,
      }));
      const root = element("div", { className: "models-page" });
      let disposed = false;
      container.append(root);

      const renderError = (error) => {
        if (!disposed && error?.name !== "AbortError") {
          root.replaceChildren(
            pageHeader("Mô hình", "Chọn mức cân bằng giữa tốc độ, chi phí và độ sâu suy luận."),
            errorPanel(error),
          );
        }
      };

      async function load() {
        root.replaceChildren(
          pageHeader("Mô hình", "Chọn mức cân bằng giữa tốc độ, chi phí và độ sâu suy luận."),
          statusPanel({ tone: "neutral", title: "Đang đọc cấu hình", body: "Kiểm tra mô hình đang chạy và lựa chọn đã lưu…" }),
        );
        try {
          const data = await service.load();
          if (disposed) return;
          renderChoices(data);
        } catch (error) {
          renderError(error);
        }
      }

      function renderChoices(data) {
        const choices = Array.isArray(data.choices) ? data.choices.filter((model) => Object.hasOwn(MODEL_INFO, model)) : [];
        let chosen = choices.includes(data.saved) ? data.saved : data.active;
        if (!choices.includes(chosen)) chosen = choices[0] || "haiku";
        const choiceBox = element("div", { className: "model-grid" });
        const notice = element("div", { className: "action-status", attributes: { "aria-live": "polite" } });
        const save = element("button", { className: "button button--primary", attributes: { type: "button" }, text: "Lưu và khởi động lại" });

        const draw = () => {
          choiceBox.replaceChildren(...choices.map((model) => {
            const info = MODEL_INFO[model];
            return element(
              "button",
              {
                className: `model-card${model === chosen ? " is-selected" : ""}`,
                attributes: { type: "button", "aria-pressed": model === chosen },
                on: { click: () => { chosen = model; draw(); } },
              },
              element("span", { className: "model-card__top" },
                element("strong", { text: info.name }),
                model === data.active ? element("span", { className: "active-badge", text: "Đang chạy" }) : null,
              ),
              element("span", { className: "model-card__description", text: info.description }),
              element("span", { className: "model-card__tag", text: info.tag }),
            );
          }));
        };
        draw();

        save.addEventListener("click", async () => {
          save.disabled = true;
          notice.textContent = "Đang lưu lựa chọn…";
          try {
            const result = await service.save(chosen);
            if (disposed) return;
            if (!result.restarting) {
              notice.textContent = `Đã lưu ${chosen}, nhưng không tự mở lại được (${result.reason || "không rõ lý do"}). Đóng rồi mở lại phần mềm để áp dụng.`;
              save.disabled = false;
              return;
            }
            const restarted = await waitForRestart({
              probe: async () => {
                await service.load();
                return true;
              },
              signal: controller.signal,
              onPhase: (phase) => { if (!disposed) notice.textContent = phaseMessage(phase) },
              reload,
            });
            if (!restarted && !disposed) save.disabled = false;
          } catch (error) {
            if (!disposed && error?.name !== "AbortError") {
              notice.replaceChildren(errorPanel(error));
              save.disabled = false;
            }
          }
        });

        root.replaceChildren(
          pageHeader("Mô hình", "Chọn mức cân bằng giữa tốc độ, chi phí và độ sâu suy luận."),
          statusPanel({
            tone: "neutral",
            title: data.active ? `Đang chạy ${MODEL_INFO[data.active]?.name || data.active}` : "Chưa xác định mô hình đang chạy",
            body: data.saved && data.saved !== data.active
              ? `Đã lưu ${MODEL_INFO[data.saved]?.name || data.saved}; cần khởi động lại để áp dụng.`
              : "Lưu lựa chọn sẽ tự khởi động lại daemon; hội thoại và phiên Zalo vẫn được giữ.",
          }),
          element("section", { className: "section-block", attributes: { "aria-labelledby": "model-choice-title" } },
            element("div", { className: "section-heading" },
              element("h2", { attributes: { id: "model-choice-title" }, text: "Chọn mô hình trả lời" }),
              element("p", { text: "Chỉ ba bí danh an toàn được chấp nhận." }),
            ),
            choiceBox,
            element("div", { className: "form-actions" }, save),
            notice,
          ),
        );
      }

      void load();
      return {
        dispose() {
          disposed = true;
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createModelsPage().mount(container);
}
