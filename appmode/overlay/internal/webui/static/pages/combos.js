import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";
import {
  TYPE_LABELS,
  createMemberEditor,
  messageOf,
  revisionOf,
  snapshotOf,
} from "./route-editor.js";

const STATUS_POLL_MS = 5000;
const TYPE_ORDER = ["fallback", "round_robin"];

function comboPath(id, suffix = "") {
  const value = String(id ?? "").trim();
  if (!value) throw new TypeError("Combo id is required");
  return `/llm/combos/${encodeURIComponent(value)}${suffix}`;
}

// createComboService dựng THÂN của mọi lượt ghi tại đây thay vì chuyển tiếp thẳng bản nháp: /llm từ
// chối trường lạ, và bản nháp mang thêm position lẫn các trường trang tự thêm thì một lượt lưu hợp
// lệ sẽ thành 400. Đóng gói ở một chỗ thì hợp đồng đó chỉ phải đúng một lần — y như createModelService.
export function createComboService(request = requestJSON) {
  if (typeof request !== "function") {
    throw new TypeError("Combo service requires an API request function");
  }
  return Object.freeze({
    providers: () => request("/llm/providers"),
    status: () => request("/llm/status"),
    list: () => request("/llm/combos"),
    create: ({ name, type }) => request("/llm/combos", {
      method: "POST",
      body: { name, type },
    }),
    save: (id, { revision, type, entries }) => request(comboPath(id), {
      method: "PUT",
      body: {
        revision,
        type,
        entries: entries.map((entry) => ({
          provider_id: entry.provider_id,
          model_id: entry.model_id,
          enabled: Boolean(entry.enabled),
        })),
      },
    }),
    activate: (id) => request(comboPath(id, "/activate"), { method: "POST" }),
    remove: (id) => request(comboPath(id), { method: "DELETE" }),
  });
}

function typeLabel(type) {
  return TYPE_LABELS[type] ?? type;
}

export function createCombosPage({
  request = requestJSON,
  setInterval: schedule = globalThis.setInterval,
  clearInterval: cancelSchedule = globalThis.clearInterval,
  AbortController: AbortControllerImpl = globalThis.AbortController,
} = {}) {
  return Object.freeze({
    mount(container) {
      const controller = new AbortControllerImpl();
      const service = createComboService((path, options = {}) => request(path, {
        ...options,
        signal: controller.signal,
      }));
      const root = element("div", { className: "combos-page" });
      let disposed = false;
      let providers = [];
      let combos = [];
      let selectedId = "";
      let status = null;
      let statusError = "";
      let editor = null;
      container.append(root);

      const header = () => pageHeader(
        "Combos",
        "Bộ chuỗi Provider/model mà bot chọn giữa. Combo đang dùng là bộ bot chạy khi trả lời khách.",
      );
      const rowsBox = element("div", { className: "clist" });
      const editorSlot = element("div", { className: "ceditor" });
      const note = element("span", { className: "note", attributes: { "aria-live": "polite" } });
      const say = (message) => { note.textContent = message; };

      const nameInput = element("input", {
        className: "cname-input",
        attributes: { type: "text", placeholder: "Tên combo mới", "aria-label": "Tên combo mới" },
      });
      const newTypeSelect = element("select", {
        attributes: { "aria-label": "Kiểu combo mới" },
      }, TYPE_ORDER.map((id) => element("option", { attributes: { value: id }, text: typeLabel(id) })));
      const createBtn = element("button", {
        className: "btn go",
        attributes: { type: "button" },
        text: "Tạo combo",
      });
      createBtn.addEventListener("click", () => { void create(); });
      const creator = element("div", { className: "cnew" },
        element("div", { className: "cnew-head", text: "Combo mới" },),
        element("div", { className: "row" }, nameInput, newTypeSelect, createBtn),
      );

      // drawList chỉ vẽ lại các HÀNG combo — nameInput/creator/note giữ nguyên qua mỗi lượt vẽ để một
      // lượt activate/xoá không thổi bay tên combo đang gõ dở hay câu thông báo vừa hiện.
      function drawList() {
        if (!combos.length) {
          rowsBox.replaceChildren(element("div", { className: "none", text: "Chưa có combo nào." }));
          return;
        }
        rowsBox.replaceChildren(...combos.map(comboRow));
      }

      function comboRow(combo) {
        const chosen = combo.id === selectedId;
        const radio = element("input", {
          attributes: { type: "radio", name: "combo-active", "aria-label": `Dùng combo ${combo.name || combo.id}` },
        });
        radio.checked = Boolean(combo.active);
        radio.addEventListener("change", () => { void activate(combo.id); });

        const name = element("button", {
          className: "cname",
          attributes: { type: "button" },
          text: combo.name || combo.id,
        });
        name.addEventListener("click", () => {
          if (selectedId === combo.id) return;
          selectedId = combo.id;
          drawList();
          drawEditor();
        });

        const badge = element("span", { className: "ctype", text: typeLabel(combo.type) });
        const del = element("button", {
          className: "btn",
          attributes: { type: "button", "aria-label": `Xoá combo ${combo.name || combo.id}` },
          text: "Xoá",
        });
        del.addEventListener("click", () => { void remove(combo.id); });

        return element("div", { className: chosen ? "crow on" : "crow" },
          element("label", { className: "cactive" }, radio, element("span", { text: "Đang dùng" })),
          name,
          badge,
          del,
        );
      }

      function drawEditor() {
        if (editor) { editor.dispose(); editor = null; }
        const combo = combos.find((entry) => entry.id === selectedId);
        if (!combo) {
          editorSlot.replaceChildren(element("div", {
            className: "none",
            text: "Chọn một combo để sửa mắt xích.",
          }));
          return;
        }
        editor = createMemberEditor({
          providers,
          snapshot: snapshotOf(combo),
          type: combo.type,
          live: Boolean(combo.active),
          save: (payload) => service.save(combo.id, payload),
          reload: async () => {
            const fresh = await service.list();
            combos = Array.isArray(fresh?.combos) ? fresh.combos : [];
            const found = combos.find((entry) => entry.id === combo.id);
            if (!found) throw new Error("combo không còn tồn tại");
            return { revision: found.revision, entries: snapshotOf(found).entries, type: found.type };
          },
          onSaved: (saved) => {
            // Đồng bộ revision + entries + type vào state danh sách để lần dựng lại editor kế tiếp
            // (đổi combo rồi quay lại) không lưu đè một revision đã cũ.
            combos = combos.map((entry) => (entry.id === combo.id
              ? {
                  ...entry,
                  revision: revisionOf(saved, entry.revision),
                  type: saved?.type ?? entry.type,
                  entries: Array.isArray(saved?.entries) ? saved.entries : entry.entries,
                }
              : entry));
          },
          conflictCode: "COMBO_REVISION_CONFLICT",
        });
        editorSlot.replaceChildren(editor.node);
        editor.setStatus(status, statusError);
      }

      async function refreshList() {
        const list = await service.list();
        if (disposed) return;
        combos = Array.isArray(list?.combos) ? list.combos : [];
        if (!combos.some((combo) => combo.id === selectedId)) {
          selectedId = (combos.find((combo) => combo.active) ?? combos[0])?.id ?? "";
        }
        drawList();
        drawEditor();
      }

      async function activate(id) {
        say("Đang đổi combo đang dùng…");
        try {
          await service.activate(id);
          if (disposed) return;
          await refreshList();
          if (!disposed) say("Đã đổi combo đang dùng. Bot dùng ngay.");
        } catch (error) {
          if (disposed || error?.name === "AbortError") return;
          // Tải lại để bỏ chấm radio vừa gạt hụt, rồi báo lý do.
          await refreshList().catch(() => {});
          if (!disposed) say(`không đổi được combo: ${messageOf(error)}`);
        }
      }

      async function remove(id) {
        try {
          await service.remove(id);
          if (disposed) return;
          if (selectedId === id) selectedId = "";
          await refreshList();
          if (!disposed) say("Đã xoá combo.");
        } catch (error) {
          if (disposed || error?.name === "AbortError") return;
          if (error?.code === "COMBO_PROTECTED") {
            // 409 combo đang dùng / combo cuối cùng: không có gì đổ vỡ, chỉ báo và giữ nguyên danh sách.
            say(messageOf(error));
            return;
          }
          say(`không xoá được combo: ${messageOf(error)}`);
        }
      }

      async function create() {
        const name = nameInput.value.trim();
        if (!name) {
          say("Đặt tên cho combo mới trước khi tạo.");
          return;
        }
        const type = TYPE_ORDER.includes(newTypeSelect.value) ? newTypeSelect.value : "fallback";
        createBtn.disabled = true;
        say("Đang tạo combo mới…");
        try {
          const combo = await service.create({ name, type });
          if (disposed) return;
          nameInput.value = "";
          selectedId = combo?.id ?? selectedId;
          await refreshList();
          if (!disposed) say("Đã tạo combo mới. Gạt radio để cho bot dùng.");
        } catch (error) {
          if (disposed || error?.name === "AbortError") return;
          say(`không tạo được combo: ${messageOf(error)}`);
        } finally {
          if (!disposed) createBtn.disabled = false;
        }
      }

      async function refreshStatus() {
        try {
          const next = await service.status();
          if (disposed) return;
          status = next;
          statusError = "";
          editor?.setStatus(status, statusError);
        } catch (error) {
          if (disposed || error?.name === "AbortError") return;
          statusError = `không đọc được trạng thái: ${messageOf(error)}`;
          editor?.setStatus(status, statusError);
        }
      }

      async function load() {
        root.replaceChildren(header());
        try {
          // GET /llm/providers giải mã một lần cho từng Provider, nên nó chỉ chạy lúc mount — vòng
          // hỏi trạng thái bên dưới KHÔNG được chạm vào nó.
          const [list, providerList, statusResp] = await Promise.all([
            service.list(),
            service.providers(),
            service.status(),
          ]);
          if (disposed) return;
          combos = Array.isArray(list?.combos) ? list.combos : [];
          providers = Array.isArray(providerList?.providers) ? providerList.providers : [];
          status = statusResp;
          selectedId = (combos.find((combo) => combo.active) ?? combos[0])?.id ?? "";
          root.replaceChildren(
            header(),
            element("div", { className: "cols" },
              element("div", { className: "combos-list" }, rowsBox, creator, note),
              editorSlot,
            ),
          );
          drawList();
          drawEditor();
          const timer = schedule(() => {
            if (disposed) return;
            void refreshStatus();
          }, STATUS_POLL_MS);
          controller.signal.addEventListener("abort", () => cancelSchedule(timer));
        } catch (error) {
          if (disposed || error?.name === "AbortError") return;
          root.replaceChildren(header(), errorPanel(error));
        }
      }

      void load();
      return {
        dispose() {
          disposed = true;
          if (editor) { editor.dispose(); editor = null; }
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createCombosPage().mount(container);
}
