import { requestJSON } from "../core/api.js";
import { waitForServerRestart } from "../core/state.js";
import { element, pageHeader } from "../core/ui.js";

const MODEL_INFO = Object.freeze({
  haiku: Object.freeze({ name: "Haiku", tag: "nhanh nhất, rẻ nhất", description: "Trả lời nhanh, chi phí thấp nhất. Đủ cho hỏi đáp thường ngày dựa trên wiki. Mặc định." }),
  sonnet: Object.freeze({ name: "Sonnet", tag: "cân bằng", description: "Suy luận tốt hơn Haiku, vẫn nhanh. Chọn khi khách hỏi những câu cần nối nhiều nguồn." }),
  opus: Object.freeze({ name: "Opus", tag: "sâu nhất, đắt nhất", description: "Suy luận sâu nhất, chậm hơn và tốn hơn nhiều. Cân nhắc kỹ nếu lượng tin lớn." }),
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

      const header = () => pageHeader("Models", "Mô hình mà bot dùng để viết câu trả lời.");
      const renderError = (error) => {
        if (!disposed && error?.name !== "AbortError") {
          root.replaceChildren(
            header(),
            element("p", { className: "note", text: `không đọc được cấu hình: ${error.message || error}` }),
          );
        }
      };

      async function load() {
        root.replaceChildren(
          header(),
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
        const choiceBox = element("div", { className: "picks" });
        const notice = element("span", { className: "note", attributes: { "aria-live": "polite" } });
        const save = element("button", { className: "btn go", attributes: { type: "button" }, text: "Lưu" });

        const draw = () => {
          choiceBox.replaceChildren(...choices.map((model) => {
            const info = MODEL_INFO[model];
            return element(
              "button",
              {
                className: `pick${model === chosen ? " on" : ""}`,
                attributes: { type: "button", "aria-pressed": String(model === chosen) },
                on: { click: () => { chosen = model; draw(); } },
              },
              element("span", { className: "dot", attributes: { "aria-hidden": "true" } }),
              (() => {
                const body = element("span");
                body.style.minWidth = "0";
                const name = element("span", { className: "nm", text: info.name });
                if (model === data.active) {
                  const running = element("span", { className: "running", text: "  · đang chạy" });
                  running.style.fontWeight = "400";
                  running.style.fontSize = "11.5px";
                  running.style.color = "var(--live)";
                  name.append(running);
                }
                body.append(name, element("div", { className: "ds", text: info.description }));
                return body;
              })(),
              element("span", { className: "tag", text: info.tag }),
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
              notice.textContent = `không lưu được: ${error.message || error}`;
              save.disabled = false;
            }
          }
        });

        root.replaceChildren(
          header(),
          choiceBox,
          element("div", { className: "row" }, save, notice),
          element("div", { className: "hint", text: "Bấm Lưu là phần mềm tự khởi động lại để áp dụng, mất khoảng 10 giây. Trang này tự tải lại khi xong. Hội thoại, danh bạ và tri thức không mất gì, phiên Zalo cũng không phải quét lại." }),
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
