import { requestJSON } from "../core/api.js";
import { element, errorPanel, pageHeader } from "../core/ui.js";
import {
  TYPE_LABELS,
  TYPE_ORDER,
  addEntry,
  canMove,
  entryWarning,
  messageOf,
  moveEntry,
  patchEntry,
  removeEntry,
  revisionOf,
  snapshotOf,
  statusText,
} from "./route-editor.js";

const STATUS_POLL_MS = 5000;

// Gợi ý dưới ô chọn kiểu combo. Chỉ hai kiểu §Combos hỗ trợ; đặt cạnh editor vì đây là chữ giao diện
// của editor chứ không phải hàm thuần dùng chung.
const TYPE_HINTS = Object.freeze({
  fallback: "Bot thử từ trên xuống, dừng ở mắt xích đầu tiên trả lời được.",
  round_robin: "Mỗi lượt bắt đầu ở một mắt xích khác rồi mới fallback"
    + " — chia tải giữa các tài khoản/Provider.",
});

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

// createComboEditor dựng cột phải: ô chọn kiểu, nút "Thêm model" mở modal chọn model kiểu 9Router,
// danh sách mắt xích chỉ-đọc (sắp thứ tự / bật-tắt / xoá), dòng trạng thái + huy hiệu "đang chạy".
// Nó KHÔNG tự gọi API — trang truyền `save`/`reload` (đã khoá vào đúng combo). MỌI thay đổi thành
// viên hay kiểu đều TỰ LƯU: không còn nút Lưu. Xung đột revision (409) thì đọc lại revision mới và
// thử lưu lại đúng một lần; còn kẹt mới mời tải lại tay.
//
//   providers   danh sách Provider để dựng modal + phân giải tên.
//   snapshot    { revision, entries } của combo đang chọn.
//   type        kiểu combo hiện tại; đi kèm mỗi lượt tự lưu.
//   live        combo này có đang chạy không — huy hiệu "đang chạy" + dòng trạng thái chỉ hiện khi đúng.
//   save        async ({ revision, type, entries }) => body đã lưu.
//   reload      async () => { revision, entries, type } đọc lại từ máy chủ (và tự chữa nếu combo bị xoá).
//   onSaved     (body) => void, để trang đồng bộ revision/entries/kiểu vào state danh sách.
//   conflictCode mã lỗi 409 revision.
export function createComboEditor({
  providers = [],
  snapshot = { revision: 0, entries: [] },
  type = "fallback",
  live = false,
  save,
  reload,
  onSaved = () => {},
  conflictCode = "COMBO_REVISION_CONFLICT",
} = {}) {
  if (typeof save !== "function") throw new TypeError("Combo editor requires a save function");

  let draft = { revision: revisionOf(snapshot), entries: snapshot.entries ?? [] };
  let curType = TYPE_ORDER.includes(type) ? type : "fallback";
  let status = null;
  let statusError = "";
  let badges = [];
  let pendingFocus = null;
  let disposed = false;
  let closePicker = () => {};
  // saveChain nối các lượt tự lưu thành một hàng: mỗi persist đọc draft.revision tại lúc CHẠY (sau khi
  // lượt trước đã đồng bộ revision), nên bấm thêm/bớt nhanh liên tiếp không gửi một revision cũ.
  let saveChain = Promise.resolve();

  const nameOf = (providerID) => providers
    .find((provider) => provider.id === providerID)?.name || providerID;
  const modelLabelOf = (providerID, modelID) => providers
    .find((provider) => provider.id === providerID)?.models
    ?.find((model) => model.model_id === modelID)?.name || modelID;

  const statusLine = element("div", { className: "chainstatus" });
  const rowsBox = element("div", { className: "facts" });
  const note = element("span", { className: "note", attributes: { "aria-live": "polite" } });
  const say = (message) => { note.textContent = message; };
  const reloadSlot = element("span", { className: "reslot" });
  const hint = element("div", { className: "hint" });

  const typeSelect = element("select", { attributes: { id: "combo-type" } },
    TYPE_ORDER.map((id) => element("option", { attributes: { value: id }, text: TYPE_LABELS[id] })));
  typeSelect.value = curType;
  const drawHint = () => { hint.textContent = TYPE_HINTS[curType] ?? ""; };
  typeSelect.addEventListener("change", () => {
    curType = TYPE_ORDER.includes(typeSelect.value) ? typeSelect.value : "fallback";
    drawHint();
    void autoSave();
  });
  drawHint();

  const addBtn = element("button", { className: "btn go", attributes: { type: "button" }, text: "Thêm model" });
  addBtn.addEventListener("click", () => openPicker());
  const reloadBtn = element("button", { className: "btn", attributes: { type: "button" }, text: "Tải lại combo" });
  const showReload = () => { reloadBtn.disabled = false; reloadSlot.replaceChildren(reloadBtn); };
  const hideReload = () => reloadSlot.replaceChildren();

  // --- tự lưu + CAS revision ---

  function autoSave() {
    saveChain = saveChain.then(() => persist()).catch(() => {});
    return saveChain;
  }

  function applySaved(saved) {
    draft = { ...draft, revision: revisionOf(saved, draft.revision) };
    onSaved(saved);
    hideReload();
    say(`Đã lưu.${live ? " Bot dùng ngay, không cần khởi động lại." : ""}`);
  }

  async function persist() {
    if (disposed) return;
    try {
      applySaved(await save({ revision: draft.revision, type: curType, entries: draft.entries }));
    } catch (error) {
      if (disposed || error?.name === "AbortError") return;
      if (error?.code === conflictCode) { await retryAfterConflict(error); return; }
      say(`không lưu được: ${messageOf(error)}`);
    }
  }

  // retryAfterConflict đọc lại revision mới rồi gửi LẠI đúng bản nháp đang hiển thị — KHÔNG vẽ lại,
  // nên lượt sửa của người dùng không bị nuốt. Còn xung đột nữa (người khác lại ghi chen) thì dừng và
  // mời tải lại tay thay vì lặp vô hạn.
  async function retryAfterConflict(firstError) {
    let fresh;
    try {
      fresh = await reload();
    } catch {
      // reload tự chữa (combo bị xoá ở nơi khác) hoặc editor đã bị dispose — thoát êm, trang lo phần còn lại.
      return;
    }
    if (disposed) return;
    draft = { ...draft, revision: revisionOf(fresh, draft.revision) };
    try {
      applySaved(await save({ revision: draft.revision, type: curType, entries: draft.entries }));
    } catch (error) {
      if (disposed || error?.name === "AbortError") return;
      if (error?.code === conflictCode) {
        say(`${messageOf(firstError)}. Tải lại combo nếu muốn bỏ bản đang sửa.`);
        showReload();
        return;
      }
      say(`không lưu được: ${messageOf(error)}`);
    }
  }

  reloadBtn.addEventListener("click", async () => {
    if (typeof reload !== "function") return;
    reloadBtn.disabled = true;
    say("Đang đọc lại combo đang lưu…");
    try {
      const fresh = await reload();
      if (disposed) return;
      // Vứt bản nháp đang sửa nghĩa là mọi checkmark trong modal (nếu đang mở) cũng đã lỗi thời — đóng
      // nó lại thay vì để nó phản chiếu một bản nháp không còn tồn tại.
      closePicker();
      draft = { revision: revisionOf(fresh), entries: fresh.entries ?? [] };
      curType = TYPE_ORDER.includes(fresh.type) ? fresh.type : curType;
      typeSelect.value = curType;
      drawHint();
      hideReload();
      drawRows();
      say("Đã thay bản đang sửa bằng combo đang lưu.");
    } catch (error) {
      if (disposed || error?.name === "AbortError") return;
      say(`không đọc lại được: ${messageOf(error)}`);
      reloadBtn.disabled = false;
    }
  });

  // --- danh sách mắt xích ---

  // applyStatus tra bản nháp theo VỊ TRÍ: huy hiệu "đang chạy" + dòng trạng thái chỉ có nghĩa khi combo
  // này đang chạy, nên cả hai câm khi live=false.
  function applyStatus() {
    badges.forEach((node, index) => {
      const entry = draft.entries[index];
      const running = live && Boolean(status) && Boolean(entry)
        && status.active_provider_id === entry.provider_id
        && status.active_model_id === entry.model_id;
      node.textContent = running ? "đang chạy" : "";
    });
    statusLine.textContent = live ? (statusError || statusText(status, nameOf)) : "";
  }

  function buildRow(entry, index) {
    const idBase = `member-${index}`;
    const badge = element("span", { className: "live" });
    const label = element("span", {
      className: "mv",
      text: `${nameOf(entry.provider_id)} · ${modelLabelOf(entry.provider_id, entry.model_id)}`,
    });
    const warning = element("div", { className: "fn", text: entryWarning(providers, entry) });

    const enabledBox = element("input", { attributes: { id: `${idBase}-on`, type: "checkbox" } });
    enabledBox.checked = Boolean(entry.enabled);
    enabledBox.addEventListener("change", () => {
      draft = { ...draft, entries: patchEntry(draft.entries, index, { enabled: Boolean(enabledBox.checked) }) };
      void autoSave();
    });

    const mover = (labelText, delta) => {
      const node = element("button", {
        className: "btn",
        attributes: {
          type: "button",
          disabled: !canMove(draft.entries, index, delta),
          "aria-label": `${labelText}: ${nameOf(entry.provider_id)}`,
        },
        text: labelText,
      });
      node.addEventListener("click", () => {
        // Con trỏ đi theo mắt xích vừa dời: sau lượt vẽ lại nút cũ không còn tồn tại.
        pendingFocus = { index: index + delta, kind: labelText === "Lên" ? "up" : "down" };
        draft = { ...draft, entries: moveEntry(draft.entries, index, delta) };
        drawRows();
        void autoSave();
      });
      return node;
    };
    const up = mover("Lên", -1);
    const down = mover("Xuống", 1);

    const remove = element("button", {
      className: "btn",
      attributes: { type: "button", "aria-label": `Xoá mắt xích ${nameOf(entry.provider_id)}` },
      text: "Xoá",
    });
    remove.addEventListener("click", () => {
      draft = { ...draft, entries: removeEntry(draft.entries, index) };
      drawRows();
      void autoSave();
    });

    const node = element("div", { className: "fact" },
      element("div", { className: "fp" }, element("span", { text: `${index + 1}` }), badge),
      element("div", { className: "fc" }, label),
      element("div", { className: "fc check" },
        enabledBox,
        element("label", { attributes: { for: `${idBase}-on` }, text: "Bật" }),
      ),
      element("div", { className: "fb" }, up, down, remove),
      warning,
    );
    return { node, badge, controls: { up, down } };
  }

  function drawRows() {
    if (!draft.entries.length) {
      badges = [];
      rowsBox.replaceChildren(element("div", {
        className: "none",
        text: "Combo chưa có model nào. Bấm “Thêm model” để chọn.",
      }));
      applyStatus();
      return;
    }
    const built = draft.entries.map(buildRow);
    badges = built.map((row) => row.badge);
    rowsBox.replaceChildren(...built.map((row) => row.node));
    applyStatus();
    if (pendingFocus) {
      const moved = built[pendingFocus.index]?.controls;
      const wanted = moved?.[pendingFocus.kind];
      const fallback = moved?.[pendingFocus.kind === "up" ? "down" : "up"];
      (wanted?.disabled ? fallback : wanted)?.focus();
      pendingFocus = null;
    }
  }

  // --- modal chọn model kiểu 9Router: bấm để thêm, bấm lại để bỏ, thay đổi tự lưu ---

  function isMember(providerID, modelID) {
    return draft.entries.some((entry) => entry.provider_id === providerID && entry.model_id === modelID);
  }

  function toggleMember(providerID, modelID) {
    draft = {
      ...draft,
      entries: isMember(providerID, modelID)
        ? draft.entries.filter((entry) => !(entry.provider_id === providerID && entry.model_id === modelID))
        : addEntry(draft.entries, { provider_id: providerID, model_id: modelID, enabled: true }),
    };
    say("");
    drawRows();
    void autoSave();
  }

  function openPicker() {
    closePicker();
    // Chỉ Provider đang bật + có model: một hàng model không có gì để thêm chỉ làm rối modal.
    const pickable = providers.filter((provider) => provider.enabled
      && Array.isArray(provider.models) && provider.models.length);
    const back = element("div", { className: "sheetback", attributes: { "data-combos-overlay": "" } });
    const search = element("input", {
      className: "pick-search",
      attributes: { type: "text", placeholder: "Tìm model…", "aria-label": "Tìm model" },
    });
    const listBox = element("div", { className: "pick-list" });

    const matches = (query, provider, model) => !query
      || (model.name || "").toLowerCase().includes(query)
      || String(model.model_id).toLowerCase().includes(query)
      || (provider.name || provider.id).toLowerCase().includes(query);

    const modelRow = (provider, model) => {
      const active = isMember(provider.id, model.model_id);
      const row = element("button", {
        className: active ? "pick-model on" : "pick-model",
        attributes: { type: "button", "aria-pressed": String(active) },
      },
        element("span", { className: "pick-check", text: active ? "✓" : "" }),
        element("span", { className: "pick-mname", text: model.name || model.model_id }),
        element("span", { className: "pick-mid", text: model.model_id }),
      );
      row.addEventListener("click", () => {
        toggleMember(provider.id, model.model_id);
        renderList();
      });
      return row;
    };

    const renderList = () => {
      const query = search.value.trim().toLowerCase();
      const groups = pickable
        .map((provider) => ({ provider, models: provider.models.filter((model) => matches(query, provider, model)) }))
        .filter((group) => group.models.length);
      if (!groups.length) {
        listBox.replaceChildren(element("div", { className: "none", text: "Không có model nào khớp." }));
        return;
      }
      listBox.replaceChildren(...groups.map((group) => element("div", { className: "pick-group" },
        element("div", { className: "pick-group-head", text: group.provider.name || group.provider.id }),
        ...group.models.map((model) => modelRow(group.provider, model)),
      )));
    };
    search.addEventListener("input", renderList);

    const closeBtn = element("button", {
      className: "btn", attributes: { type: "button", "aria-label": "Đóng" }, text: "Đóng",
    });
    const sheet = element("div", {
      className: "sheet",
      attributes: { role: "dialog", "aria-modal": "true", "aria-label": "Thêm model vào combo" },
    },
      element("div", { className: "sheethead" },
        element("span", { className: "st", text: "Thêm model vào combo" }),
        closeBtn,
      ),
      element("div", { className: "pick-hint", text: "Bấm để thêm, bấm lại để bỏ. Thay đổi tự lưu." }),
      search,
      listBox,
    );

    let closed = false;
    const onKey = (event) => { if (event.key === "Escape") close(); };
    const teardown = (restoreFocus) => {
      if (closed) return;
      closed = true;
      document.removeEventListener?.("keydown", onKey);
      back.remove();
      closePicker = () => {};
      if (restoreFocus) addBtn.focus?.();
    };
    const close = () => teardown(true);
    closePicker = () => teardown(false);
    closeBtn.addEventListener("click", close);
    back.addEventListener("click", (event) => { if (event.target === back) close(); });
    document.addEventListener?.("keydown", onKey);

    back.append(sheet);
    renderList();
    document.body.append(back);
    search.focus?.();
  }

  const node = element("div", { className: "models-page" },
    statusLine,
    element("div", { className: "fc" },
      element("label", { className: "fk", attributes: { for: "combo-type" }, text: "Kiểu combo" }),
      typeSelect,
    ),
    element("div", { className: "row" }, addBtn),
    rowsBox,
    element("div", { className: "row" }, reloadSlot, note),
    hint,
  );
  drawRows();

  return Object.freeze({
    node,
    setStatus(nextStatus, nextError = "") {
      status = nextStatus;
      statusError = nextError;
      applyStatus();
    },
    dispose() {
      disposed = true;
      closePicker();
    },
  });
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

        const badge = element("span", { className: `ctype ctype-${combo.type}`, text: typeLabel(combo.type) });
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
        editor = createComboEditor({
          providers,
          snapshot: snapshotOf(combo),
          type: combo.type,
          live: Boolean(combo.active),
          save: (payload) => service.save(combo.id, payload),
          reload: async () => {
            const fresh = await service.list();
            combos = Array.isArray(fresh?.combos) ? fresh.combos : [];
            const found = combos.find((entry) => entry.id === combo.id);
            // Danh sách vừa đổi (revision, kiểu, combo đang dùng) — vẽ lại rows để radio/huy hiệu/tên
            // khớp lại thay vì đứng cũ tới lượt bấm không liên quan kế tiếp.
            drawList();
            if (!found) {
              // Combo bị xoá ở nơi khác trong lúc đang sửa: nhặt lại combo đang dùng/đầu tiên rồi dựng
              // lại editor theo lựa chọn mới. drawEditor dispose editor hiện tại, nên lượt throw dưới
              // đây chỉ để thoát handler reload cũ — nó tự bail qua cờ disposed, không hiện câu lỗi.
              selectedId = (combos.find((entry) => entry.active) ?? combos[0])?.id ?? "";
              drawEditor();
              say("Combo này đã bị xoá ở nơi khác — đã chuyển sang combo hiện có.");
              throw new Error("combo không còn tồn tại");
            }
            return { revision: found.revision, entries: snapshotOf(found).entries, type: found.type };
          },
          onSaved: (saved) => {
            // Đồng bộ revision + entries + kiểu vào state danh sách để lần dựng lại editor kế tiếp không
            // lưu đè một revision cũ, rồi vẽ lại rows để huy hiệu kiểu bắt kịp lượt đổi kiểu vừa tự lưu.
            combos = combos.map((entry) => (entry.id === combo.id
              ? {
                  ...entry,
                  revision: revisionOf(saved, entry.revision),
                  type: saved?.type ?? entry.type,
                  entries: Array.isArray(saved?.entries) ? saved.entries : entry.entries,
                }
              : entry));
            drawList();
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
