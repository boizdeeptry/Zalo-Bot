import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";

const STATUS_POLL_MS = 5000;

// createModelService dựng THÂN của lượt ghi tại đây thay vì chuyển tiếp thẳng bản nháp: /llm từ
// chối trường lạ, và bản nháp mang thêm position lẫn các trường trang tự thêm sau này thì một
// lượt lưu hợp lệ sẽ thành 400. Đóng gói ở một chỗ thì hợp đồng đó chỉ phải đúng một lần.
export function createModelService(request = requestJSON) {
  if (typeof request !== "function") {
    throw new TypeError("Model service requires an API request function");
  }
  return Object.freeze({
    providers: () => request("/llm/providers"),
    route: () => request("/llm/route"),
    status: () => request("/llm/status"),
    save: ({ revision, entries }) => request("/llm/route", {
      method: "PUT",
      body: {
        revision,
        entries: entries.map((entry) => ({
          provider_id: entry.provider_id,
          model_id: entry.model_id,
          enabled: Boolean(entry.enabled),
        })),
      },
    }),
  });
}

function messageOf(error) {
  return error instanceof Error ? error.message : String(error);
}

// --- bản nháp: hàm thuần, không đụng DOM ---
//
// Ba hàm dưới đây được XUẤT để test gọi thẳng. Không còn bất biến "Claude Code là mắt xích cuối":
// §6 cho router chạy chuỗi ở BẤT KỲ vị trí nào — kể cả một chuỗi không có Claude Code — nên mọi
// mắt xích dời/tắt/xoá được như nhau. Máy chủ (validateLLMRoute) mới là nơi từ chối một chuỗi
// không đi hết được.

function canMove(entries, index, delta) {
  const target = index + delta;
  if (index < 0 || index >= entries.length) return false;
  if (target < 0 || target >= entries.length) return false;
  return true;
}

export function moveEntry(entries, index, delta) {
  if (!canMove(entries, index, delta)) return entries;
  const next = [...entries];
  const target = index + delta;
  next[index] = entries[target];
  next[target] = entries[index];
  return next;
}

export function removeEntry(entries, index) {
  if (index < 0 || index >= entries.length) return entries;
  return entries.filter((_, position) => position !== index);
}

function patchEntry(entries, index, patch) {
  return entries.map((entry, position) => (position === index ? { ...entry, ...patch } : entry));
}

// addEntry nối mắt xích mới vào CUỐI chuỗi. Không còn lưới an toàn cố định ở đuôi — người dùng tự
// sắp thứ tự, và máy chủ từ chối một chuỗi không đi hết được.
export function addEntry(entries, entry) {
  return [...entries, entry];
}

function draftProblem(entries, nameOf) {
  if (!entries.some((entry) => entry.enabled)) {
    return "Chuỗi cần ít nhất một mắt xích đang bật.";
  }
  const blank = entries.find((entry) => !entry.model_id);
  if (blank) return `Chọn model cho ${nameOf(blank.provider_id)} trước khi lưu.`;
  return "";
}

function revisionOf(payload, fallback = 0) {
  const revision = Number(payload?.revision);
  return Number.isFinite(revision) ? revision : fallback;
}

// snapshotOf nhận thân từ máy chủ và trả về bản nháp. Ép kiểu từng trường vì đây là mép hệ
// thống: một entries thiếu trường làm hỏng cả trang chứ không chỉ một hàng.
function snapshotOf(payload) {
  const entries = Array.isArray(payload?.entries) ? payload.entries : [];
  return {
    revision: revisionOf(payload),
    entries: entries.map((entry) => ({
      provider_id: String(entry?.provider_id ?? ""),
      model_id: String(entry?.model_id ?? ""),
      enabled: Boolean(entry?.enabled),
    })),
  };
}

// --- lựa chọn dựng từ danh sách Provider ---

function modelsOf(providers, providerID) {
  return providers.find((provider) => provider.id === providerID)?.models ?? [];
}

function firstModelOf(providers, providerID) {
  return modelsOf(providers, providerID).find((model) => model.available)?.model_id ?? "";
}

// keepCurrent giữ lựa chọn đang lưu kể cả khi nó không còn hợp lệ.
//
// Bỏ nó khỏi danh sách thì ô chọn tự nhảy sang giá trị khác, và người dùng lưu đè lên một mắt
// xích họ chưa hề nhìn thấy là sai. Giữ lại kèm nhãn cảnh báo thì họ thấy chỗ hỏng và sửa được.
function keepCurrent(choices, currentID, label) {
  if (!currentID || choices.some((choice) => choice.id === currentID)) return choices;
  return [{ id: currentID, label }, ...choices];
}

function providerChoices(providers, currentID) {
  const enabled = providers
    .filter((provider) => provider.enabled)
    .map((provider) => ({ id: provider.id, label: provider.name || provider.id }));
  const known = providers.find((provider) => provider.id === currentID);
  return keepCurrent(enabled, currentID, `${known?.name || currentID} (đang tắt)`);
}

function modelChoices(providers, providerID, currentID) {
  const available = modelsOf(providers, providerID)
    .filter((model) => model.available)
    .map((model) => ({ id: model.model_id, label: model.name || model.model_id }));
  return keepCurrent(available, currentID, `${currentID} (không còn dùng được)`);
}

function entryWarning(providers, entry) {
  const provider = providers.find((candidate) => candidate.id === entry.provider_id);
  // provider?. chứ không phải một nhánh "không tìm thấy" riêng: máy chủ từ chối xoá một Provider
  // mà chuỗi còn trỏ tới, nên trạng thái đó không tới được qua API. Dấu ? chỉ để một hàng dữ
  // liệu hỏng làm sai một dòng cảnh báo thay vì ném lỗi và bỏ trắng cả trang.
  if (!provider?.enabled) {
    // "Bỏ qua", không phải "hỏng": router xét trạng thái bật/tắt của Provider ở từng lượt gọi, nên
    // chuỗi bước qua mắt xích này và thử mắt xích kế tiếp. Chuỗi vẫn chạy được, và câu chữ ở đây
    // phải nói đúng điều đó thay vì bắt người dùng đi sửa một thứ không chặn gì cả.
    return `${provider?.name || entry.provider_id} đang tắt — chuỗi bỏ qua mắt xích này.`
      + " Bật lại ở mục Providers hoặc chọn Provider khác.";
  }
  if (!entry.model_id) return "Chưa chọn model cho mắt xích này.";
  const model = modelsOf(providers, entry.provider_id)
    .find((candidate) => candidate.model_id === entry.model_id);
  if (!model?.available) return `Model ${entry.model_id} không còn dùng được — chọn model khác.`;
  return "";
}

function optionNodes(choices) {
  return choices.map((choice) => element("option", {
    attributes: { value: choice.id },
    text: choice.label,
  }));
}

// labelledCell gắn nhãn nhìn thấy được cho từng ô. Ở bề ngang điện thoại hàng xếp dọc và bố cục
// lưới không còn nói được ô nào là gì; nhãn cũng chính là thứ trình đọc màn hình đọc lên.
function labelledCell(controlID, label, control) {
  return element("div", { className: "fc" },
    element("label", { className: "fk", attributes: { for: controlID }, text: label }),
    control,
  );
}

// LLM_ERROR_FIX là VIỆC PHẢI LÀM cho từng loại lỗi mà router ghi lại.
//
// Chỉ loại lỗi và tên Provider ra màn hình, KHÔNG bao giờ câu chữ của Provider: /llm/status cố ý
// không mang thân phản hồi, nên bảng này là chỗ duy nhất sinh ra chữ — và nó không đọc gì từ
// bên ngoài.
//
// Loại thứ chín, "canceled", cố ý vắng mặt: router trả về trước khi ghi telemetry cho một lượt bị
// huỷ, nên nó không tới được đây. Câu chung bên dưới lo nốt trường hợp bảng này thiếu một loại.
const LLM_ERROR_FIX = {
  credential: "API key sai hoặc hết hạn — nhập lại ở mục Providers.",
  model: "Model không gọi được — chọn model khác cho mắt xích đó.",
  request: "Request sai định dạng — chuỗi dừng tại đây, báo kỹ thuật.",
  policy: "Provider từ chối nội dung — chuỗi DỪNG tại đây, không rơi xuống Claude Code.",
  rate_limit: "Bị giới hạn tần suất — chuỗi đã thử mắt xích kế tiếp.",
  network: "Không gọi tới được — chuỗi đã thử mắt xích kế tiếp.",
  timeout: "Quá hạn — chuỗi đã thử mắt xích kế tiếp.",
  upstream: "Provider báo lỗi — chuỗi đã thử mắt xích kế tiếp.",
};

// failureText là lý do khách nhận được dòng bàn giao thay vì câu trả lời.
//
// Không có dòng này thì mọi kiểu hỏng trông y hệt nhau trên terminal Runtime — kể cả những kiểu
// người trực chữa trong 30 giây (khoá hết hạn, model đã gỡ).
function failureText(status, nameOf) {
  const kind = status.last_error_kind;
  if (!kind) return "";
  const who = nameOf(status.last_error_provider_id) || "Provider";
  return `Lượt hỏng gần nhất: ${who} (${kind}).`
    + ` ${LLM_ERROR_FIX[kind] || "Xem log Runtime để biết thêm."}`;
}

// tallyText đếm trên CỬA SỔ telemetry còn giữ lại, không phải từ đầu — nên nhãn không nói "tổng".
function tallyText(status) {
  const attempts = Number(status.attempts) || 0;
  if (attempts === 0) return "";
  return `${attempts} lượt gần đây, ${Number(status.fallbacks) || 0} lần né.`;
}

// clockOf đổi một mốc RFC3339 sang giờ máy này. Mốc rỗng hoặc hỏng trả về "" chứ không để
// "Invalid Date" chạy ra màn hình.
function clockOf(iso) {
  const at = new Date(iso ?? "");
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleTimeString();
}

// statusText nói những điều huy hiệu trên hàng KHÔNG nói được: lượt trả lời gần nhất là lúc nào,
// lượt hỏng gần nhất hỏng vì gì và phải làm gì với nó, và chuỗi đã né bao nhiêu lần.
//
// status còn null (trước lượt hỏi đầu tiên) thì để trống — .chainstatus:empty tự ẩn. Lỗi đọc
// trạng thái do refreshStatus tự viết vào ô này.
function statusText(status, nameOf) {
  if (!status) return "";
  const idle = !status.active_provider_id;
  const clock = clockOf(status.last_success_at);
  return [
    idle ? "Chưa có lượt gọi nào kể từ lần khởi động gần nhất." : "",
    !idle && clock ? `Trả lời gần nhất lúc ${clock}.` : "",
    failureText(status, nameOf),
    tallyText(status),
  ].filter(Boolean).join(" ");
}

export function createModelsPage({
  request = requestJSON,
  setInterval: schedule = globalThis.setInterval,
  clearInterval: cancelSchedule = globalThis.clearInterval,
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
      let providers = [];
      let draft = { revision: 0, entries: [] };
      let status = null;
      // statusError sống riêng khỏi status vì applyStatus chạy lại sau MỌI lượt sửa bản nháp:
      // ghi thẳng câu lỗi vào ô thì lần đổi Provider kế tiếp xoá mất nó, mà ô này được giữ đúng
      // để làm chỗ báo lượt đọc trạng thái hỏng.
      let statusError = "";
      let badges = [];
      let pendingFocus = null;
      container.append(root);

      const nameOf = (providerID) => providers
        .find((provider) => provider.id === providerID)?.name || providerID;

      const header = () => pageHeader(
        "Models",
        "Thứ tự Provider và model mà bot thử khi trả lời khách.",
      );
      const statusLine = element("div", { className: "chainstatus" });
      const rowsBox = element("div", { className: "facts" });
      const note = element("span", { className: "note", attributes: { "aria-live": "polite" } });
      const say = (message) => { note.textContent = message; };
      const reloadSlot = element("span", { className: "reslot" });

      const add = element("button", {
        className: "btn",
        attributes: { type: "button" },
        text: "Thêm mắt xích",
      });
      const save = element("button", {
        className: "btn go",
        attributes: { type: "button" },
        text: "Lưu",
      });
      const reload = element("button", {
        className: "btn",
        attributes: { type: "button" },
        text: "Tải lại chuỗi",
      });

      const showReload = () => {
        reload.disabled = false;
        reloadSlot.replaceChildren(reload);
      };
      const hideReload = () => reloadSlot.replaceChildren();

      // applyStatus tra bản nháp theo VỊ TRÍ thay vì giữ bản sao provider/model của lúc vẽ: đổi
      // Provider trên một hàng không vẽ lại danh sách, nên một bản sao ở đây sẽ dán "đang chạy"
      // lên mắt xích người dùng vừa trỏ đi chỗ khác.
      function applyStatus() {
        badges.forEach((node, index) => {
          const entry = draft.entries[index];
          const running = Boolean(status) && Boolean(entry)
            && status.active_provider_id === entry.provider_id
            && status.active_model_id === entry.model_id;
          node.textContent = running ? "đang chạy" : "";
        });
        statusLine.textContent = statusError || statusText(status, nameOf);
      }

      function buildRow(entry, index) {
        const idBase = `chain-${index}`;
        const warning = element("div", { className: "fn" });

        const providerSelect = element("select", {
          attributes: { id: `${idBase}-provider` },
        }, optionNodes(providerChoices(providers, entry.provider_id)));
        providerSelect.value = entry.provider_id;

        const modelSelect = element("select", { attributes: { id: `${idBase}-model` } });
        const drawModels = () => {
          const current = draft.entries[index];
          modelSelect.replaceChildren(
            ...optionNodes(modelChoices(providers, current.provider_id, current.model_id)),
          );
          modelSelect.value = current.model_id;
          warning.textContent = entryWarning(providers, current);
        };

        // Đổi Provider chỉ vẽ lại đúng ô model và dòng cảnh báo, không vẽ lại cả danh sách: một
        // lượt vẽ lại ở đây làm con trỏ rơi khỏi ô người dùng vừa chạm.
        providerSelect.addEventListener("change", () => {
          const providerID = providerSelect.value;
          draft = {
            ...draft,
            entries: patchEntry(draft.entries, index, {
              provider_id: providerID,
              model_id: firstModelOf(providers, providerID),
            }),
          };
          drawModels();
          applyStatus();
        });
        modelSelect.addEventListener("change", () => {
          draft = {
            ...draft,
            entries: patchEntry(draft.entries, index, { model_id: modelSelect.value }),
          };
          warning.textContent = entryWarning(providers, draft.entries[index]);
          applyStatus();
        });

        const enabledBox = element("input", {
          attributes: { id: `${idBase}-on`, type: "checkbox" },
        });
        enabledBox.checked = Boolean(entry.enabled);
        enabledBox.addEventListener("change", () => {
          draft = {
            ...draft,
            entries: patchEntry(draft.entries, index, { enabled: Boolean(enabledBox.checked) }),
          };
        });

        const mover = (label, delta) => {
          const node = element("button", {
            className: "btn",
            attributes: {
              type: "button",
              disabled: !canMove(draft.entries, index, delta),
              "aria-label": `${label}: ${nameOf(entry.provider_id)}`,
            },
            text: label,
          });
          node.addEventListener("click", () => {
            // Con trỏ đi theo mắt xích vừa dời: sau lượt vẽ lại nút cũ không còn tồn tại, và mất
            // chỗ đứng thì bấm Xuống hai lần liên tiếp bằng bàn phím là bất khả thi.
            pendingFocus = { index: index + delta, kind: label === "Lên" ? "up" : "down" };
            draft = { ...draft, entries: moveEntry(draft.entries, index, delta) };
            drawRows();
          });
          return node;
        };
        const up = mover("Lên", -1);
        const down = mover("Xuống", 1);

        const remove = element("button", {
          className: "btn",
          attributes: {
            type: "button",
            "aria-label": `Xoá mắt xích ${nameOf(entry.provider_id)}`,
          },
          text: "Xoá",
        });
        remove.addEventListener("click", () => {
          draft = { ...draft, entries: removeEntry(draft.entries, index) };
          drawRows();
        });

        const badge = element("span", { className: "live" });
        const node = element("div", { className: "fact" },
          element("div", { className: "fp" }, element("span", { text: `${index + 1}` }), badge),
          labelledCell(`${idBase}-provider`, "Provider", providerSelect),
          labelledCell(`${idBase}-model`, "Model", modelSelect),
          element("div", { className: "fc check" },
            enabledBox,
            element("label", { attributes: { for: `${idBase}-on` }, text: "Bật" }),
          ),
          element("div", { className: "fb" }, up, down, remove),
          warning,
        );
        drawModels();
        return { node, badge, controls: { up, down } };
      }

      function drawRows() {
        if (!draft.entries.length) {
          badges = [];
          rowsBox.replaceChildren(element("div", {
            className: "none",
            text: "Chuỗi chưa có mắt xích nào.",
          }));
          applyStatus();
          return;
        }
        const built = draft.entries.map(buildRow);
        badges = built.map((row) => row.badge);
        rowsBox.replaceChildren(...built.map((row) => row.node));
        applyStatus();
        if (pendingFocus) {
          // Nút vừa bấm thường TẮT ở đích: dời lên đầu chuỗi thì Lên tắt, dời xuống sát lưới an
          // toàn thì Xuống tắt. focus() vào một nút disabled không có tác dụng, mà nút cũ thì
          // replaceChildren vừa tháo — con trỏ rơi về <body>, tức là người dùng bàn phím bị ném
          // lên đầu trang sau đúng thao tác thường gặp nhất. Rơi sang nút còn lại của cặp thì họ
          // vẫn đứng trong hàng vừa dời.
          const moved = built[pendingFocus.index]?.controls;
          const wanted = moved?.[pendingFocus.kind];
          const fallback = moved?.[pendingFocus.kind === "up" ? "down" : "up"];
          (wanted?.disabled ? fallback : wanted)?.focus();
          pendingFocus = null;
        }
      }

      add.addEventListener("click", () => {
        const candidate = providers.find((provider) => provider.enabled);
        if (!candidate) {
          say("Chưa có Provider nào đang bật. Bật một Provider ở mục Providers trước.");
          return;
        }
        draft = {
          ...draft,
          entries: addEntry(draft.entries, {
            provider_id: candidate.id,
            model_id: firstModelOf(providers, candidate.id),
            enabled: true,
          }),
        };
        say("");
        drawRows();
      });

      save.addEventListener("click", async () => {
        const problem = draftProblem(draft.entries, nameOf);
        if (problem) {
          say(problem);
          return;
        }
        save.disabled = true;
        say("Đang lưu chuỗi…");
        try {
          const saved = await service.save(draft);
          if (disposed) return;
          hideReload();
          // Chỉ revision đi theo phản hồi, KHÔNG phải cả snapshot: máy chủ dội lại đúng những
          // mắt xích vừa gửi, nên thay cả bản nháp chỉ có một tác dụng thật là nuốt mất lượt sửa
          // người dùng làm trong lúc lượt PUT còn đang bay. Không đọc lại chuỗi và không khởi
          // động lại daemon: router đọc thẳng từ database ở lượt trả lời kế tiếp.
          draft = { ...draft, revision: revisionOf(saved, draft.revision) };
          say("Đã lưu chuỗi. Bot dùng ngay, không cần khởi động lại.");
        } catch (error) {
          if (disposed || error?.name === "AbortError") return;
          if (error?.code === "ROUTE_REVISION_CONFLICT") {
            // Bản nháp KHÔNG bị đụng tới: nó vẫn đúng, chỉ là dựa trên một bản cũ. Vẽ lại ở đây
            // là vứt mất chỗ đứng của con trỏ mà chẳng đổi được gì trên màn hình.
            say(`${messageOf(error)}. Tải lại chuỗi nếu muốn bỏ bản đang sửa.`);
            showReload();
            return;
          }
          say(`không lưu được: ${messageOf(error)}`);
        } finally {
          if (!disposed) save.disabled = false;
        }
      });

      reload.addEventListener("click", async () => {
        reload.disabled = true;
        say("Đang đọc lại chuỗi đang lưu…");
        try {
          const fresh = await service.route();
          if (disposed) return;
          draft = snapshotOf(fresh);
          hideReload();
          drawRows();
          say("Đã thay bản đang sửa bằng chuỗi đang lưu.");
        } catch (error) {
          if (disposed || error?.name === "AbortError") return;
          say(`không đọc lại được: ${messageOf(error)}`);
          reload.disabled = false;
        }
      });

      async function refreshStatus() {
        try {
          const next = await service.status();
          if (disposed) return;
          status = next;
          statusError = "";
          applyStatus();
        } catch (error) {
          if (disposed || error?.name === "AbortError") return;
          statusError = `không đọc được trạng thái: ${messageOf(error)}`;
          applyStatus();
        }
      }

      async function load() {
        root.replaceChildren(header());
        try {
          // GET /llm/providers giải mã một lần cho từng Provider, nên nó chỉ chạy lúc mount —
          // vòng hỏi trạng thái bên dưới KHÔNG được chạm vào nó.
          const [list, route] = await Promise.all([service.providers(), service.route()]);
          if (disposed) return;
          providers = Array.isArray(list?.providers) ? list.providers : [];
          draft = snapshotOf(route);
          root.replaceChildren(
            header(),
            statusLine,
            rowsBox,
            element("div", { className: "row" }, add, save, reloadSlot, note),
            element("div", {
              className: "hint",
              text: "Bot thử từ trên xuống và dừng ở mắt xích đầu tiên trả lời được."
                + " Sắp xếp, bật/tắt hoặc xoá mắt xích tuỳ ý; máy chủ từ chối một chuỗi không đi hết được.",
            }),
          );
          drawRows();
          void refreshStatus();
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
          controller.abort();
        },
      };
    },
  });
}

export function mount(container) {
  return createModelsPage().mount(container);
}
