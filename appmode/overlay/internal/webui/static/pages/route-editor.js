import { element } from "../core/ui.js";

// route-editor.js giữ phần DÙNG LẠI giữa mọi trang sửa một chuỗi mắt xích Provider/model: các hàm
// thuần canh biên bản nháp, các hàm dựng lựa chọn từ danh sách Provider, các hàm sinh câu trạng
// thái, và cỗ máy dựng hàng + member editor. Trang Combos dựng trên đây; test gọi thẳng các hàm
// thuần đã XUẤT. Không hàm nào ở đây tự gọi API — mọi lượt đọc/ghi do trang truyền vào.

export function messageOf(error) {
  return error instanceof Error ? error.message : String(error);
}

// TYPE_LABELS/TYPE_HINTS: hai kiểu combo mà §Combos hỗ trợ. Trang Combos dùng nhãn cho huy hiệu
// trên danh sách, editor dùng cả nhãn (ô chọn) lẫn gợi ý (dòng dưới ô chọn).
export const TYPE_LABELS = Object.freeze({
  fallback: "Fallback",
  round_robin: "Round Robin",
});

const TYPE_HINTS = Object.freeze({
  fallback: "Bot thử từ trên xuống, dừng ở mắt xích đầu tiên trả lời được.",
  round_robin: "Mỗi lượt bắt đầu ở một mắt xích khác rồi mới fallback"
    + " — chia tải giữa các tài khoản/Provider.",
});

const TYPE_ORDER = ["fallback", "round_robin"];

// --- bản nháp: hàm thuần, không đụng DOM ---
//
// Các hàm này được XUẤT để test gọi thẳng. Không có bất biến "mắt xích cuối cố định": router chạy
// chuỗi ở bất kỳ vị trí nào, nên mọi mắt xích dời/tắt/xoá được như nhau. Máy chủ mới là nơi từ
// chối một chuỗi không đi hết được.

export function canMove(entries, index, delta) {
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

export function patchEntry(entries, index, patch) {
  return entries.map((entry, position) => (position === index ? { ...entry, ...patch } : entry));
}

// addEntry nối mắt xích mới vào CUỐI chuỗi — người dùng tự sắp thứ tự.
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

export function revisionOf(payload, fallback = 0) {
  const revision = Number(payload?.revision);
  return Number.isFinite(revision) ? revision : fallback;
}

// snapshotOf nhận thân từ máy chủ và trả về bản nháp. Ép kiểu từng trường vì đây là mép hệ thống:
// một entries thiếu trường làm hỏng cả trang chứ không chỉ một hàng.
export function snapshotOf(payload) {
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

// keepCurrent giữ lựa chọn đang lưu kể cả khi nó không còn hợp lệ: bỏ nó khỏi danh sách thì ô chọn
// tự nhảy sang giá trị khác và người dùng lưu đè lên một mắt xích họ chưa hề thấy. Giữ lại kèm nhãn
// cảnh báo thì họ thấy chỗ hỏng và sửa được.
function keepCurrent(choices, currentID, label) {
  if (!currentID || choices.some((choice) => choice.id === currentID)) return choices;
  return [{ id: currentID, label }, ...choices];
}

export function providerChoices(providers, currentID) {
  const enabled = providers
    .filter((provider) => provider.enabled)
    .map((provider) => ({ id: provider.id, label: provider.name || provider.id }));
  const known = providers.find((provider) => provider.id === currentID);
  return keepCurrent(enabled, currentID, `${known?.name || currentID} (đang tắt)`);
}

export function modelChoices(providers, providerID, currentID) {
  const available = modelsOf(providers, providerID)
    .filter((model) => model.available)
    .map((model) => ({ id: model.model_id, label: model.name || model.model_id }));
  return keepCurrent(available, currentID, `${currentID} (không còn dùng được)`);
}

export function entryWarning(providers, entry) {
  const provider = providers.find((candidate) => candidate.id === entry.provider_id);
  // provider?. chứ không phải một nhánh "không tìm thấy" riêng: máy chủ từ chối xoá một Provider mà
  // chuỗi còn trỏ tới, nên trạng thái đó không tới được qua API. Dấu ? chỉ để một hàng dữ liệu hỏng
  // làm sai một dòng cảnh báo thay vì ném lỗi và bỏ trắng cả trang.
  if (!provider?.enabled) {
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

// --- câu trạng thái sinh tại đây, không đọc gì từ ngoài ---

// LLM_ERROR_FIX là VIỆC PHẢI LÀM cho từng loại lỗi mà router ghi lại. Chỉ loại lỗi và tên Provider
// ra màn hình, KHÔNG bao giờ câu chữ của Provider: /llm/status cố ý không mang thân phản hồi.
export const LLM_ERROR_FIX = Object.freeze({
  credential: "API key sai hoặc hết hạn — nhập lại ở mục Providers.",
  model: "Model không gọi được — chọn model khác cho mắt xích đó.",
  request: "Request sai định dạng — chuỗi dừng tại đây, báo kỹ thuật.",
  policy: "Provider từ chối nội dung — chuỗi DỪNG tại đây, không rơi xuống mắt xích sau.",
  rate_limit: "Bị giới hạn tần suất — chuỗi đã thử mắt xích kế tiếp.",
  network: "Không gọi tới được — chuỗi đã thử mắt xích kế tiếp.",
  timeout: "Quá hạn — chuỗi đã thử mắt xích kế tiếp.",
  upstream: "Provider báo lỗi — chuỗi đã thử mắt xích kế tiếp.",
});

export function failureText(status, nameOf) {
  const kind = status.last_error_kind;
  if (!kind) return "";
  const who = nameOf(status.last_error_provider_id) || "Provider";
  return `Lượt hỏng gần nhất: ${who} (${kind}).`
    + ` ${LLM_ERROR_FIX[kind] || "Xem log Runtime để biết thêm."}`;
}

// tallyText đếm trên CỬA SỔ telemetry còn giữ lại, không phải từ đầu — nên nhãn không nói "tổng".
export function tallyText(status) {
  const attempts = Number(status.attempts) || 0;
  if (attempts === 0) return "";
  return `${attempts} lượt gần đây, ${Number(status.fallbacks) || 0} lần né.`;
}

function clockOf(iso) {
  const at = new Date(iso ?? "");
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleTimeString();
}

export function statusText(status, nameOf) {
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

// createMemberEditor dựng TOÀN BỘ editor mắt xích cho một chuỗi: dòng trạng thái, ô chọn kiểu, bảng
// hàng Provider/model, và cụm nút Thêm/Lưu/Tải lại. Nó KHÔNG tự gọi API và KHÔNG tự hỏi trạng thái
// — trang truyền vào `save`/`reload` (đã khoá vào đúng combo) và bơm trạng thái qua setStatus. Nhờ
// vậy trang Combos dựng lại editor mỗi lần đổi combo mà chỉ một bộ hẹn giờ trạng thái chạy ở trang.
//
//   providers   danh sách Provider để dựng ô chọn.
//   snapshot    { revision, entries } của combo đang chọn.
//   type        kiểu combo hiện tại; ô chọn kiểu gửi kèm trong lượt Lưu.
//   live        combo này có đang chạy không — huy hiệu "đang chạy" và dòng trạng thái chỉ hiện khi
//               đúng, vì /llm/status phản ánh combo đang chạy chứ không phải combo đang xem.
//   save        async ({ revision, type, entries }) => body đã lưu.
//   reload       async () => { revision, entries, type } đọc lại từ máy chủ.
//   onSaved     (body) => void, để trang đồng bộ revision/entries vào state danh sách.
//   conflictCode mã lỗi 409 revision để giữ nháp và mời tải lại.
export function createMemberEditor({
  providers = [],
  snapshot = { revision: 0, entries: [] },
  type = "fallback",
  live = false,
  save,
  reload,
  onSaved = () => {},
  conflictCode = "COMBO_REVISION_CONFLICT",
} = {}) {
  if (typeof save !== "function") throw new TypeError("Member editor requires a save function");

  let draft = { revision: revisionOf(snapshot), entries: snapshot.entries ?? [] };
  let curType = TYPE_ORDER.includes(type) ? type : "fallback";
  let status = null;
  let statusError = "";
  let badges = [];
  let pendingFocus = null;
  let disposed = false;

  const nameOf = (providerID) => providers
    .find((provider) => provider.id === providerID)?.name || providerID;

  const statusLine = element("div", { className: "chainstatus" });
  const rowsBox = element("div", { className: "facts" });
  const note = element("span", { className: "note", attributes: { "aria-live": "polite" } });
  const say = (message) => { note.textContent = message; };
  const reloadSlot = element("span", { className: "reslot" });
  const hint = element("div", { className: "hint" });

  const typeSelect = element("select", {
    attributes: { id: "combo-type" },
  }, optionNodes(TYPE_ORDER.map((id) => ({ id, label: TYPE_LABELS[id] }))));
  typeSelect.value = curType;
  const drawHint = () => { hint.textContent = TYPE_HINTS[curType] ?? ""; };
  typeSelect.addEventListener("change", () => {
    curType = TYPE_ORDER.includes(typeSelect.value) ? typeSelect.value : "fallback";
    drawHint();
  });
  drawHint();

  const add = element("button", { className: "btn", attributes: { type: "button" }, text: "Thêm mắt xích" });
  const saveBtn = element("button", { className: "btn go", attributes: { type: "button" }, text: "Lưu" });
  const reloadBtn = element("button", { className: "btn", attributes: { type: "button" }, text: "Tải lại combo" });

  const showReload = () => { reloadBtn.disabled = false; reloadSlot.replaceChildren(reloadBtn); };
  const hideReload = () => reloadSlot.replaceChildren();

  // applyStatus tra bản nháp theo VỊ TRÍ thay vì giữ bản sao provider/model của lúc vẽ: đổi Provider
  // trên một hàng không vẽ lại danh sách, nên một bản sao ở đây sẽ dán "đang chạy" lên mắt xích
  // người dùng vừa trỏ đi chỗ khác. Huy hiệu + dòng trạng thái chỉ có nghĩa khi combo này đang chạy.
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

    // Đổi Provider chỉ vẽ lại đúng ô model và dòng cảnh báo, không vẽ lại cả danh sách: một lượt vẽ
    // lại ở đây làm con trỏ rơi khỏi ô người dùng vừa chạm.
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
        // Con trỏ đi theo mắt xích vừa dời: sau lượt vẽ lại nút cũ không còn tồn tại, và mất chỗ
        // đứng thì bấm Xuống hai lần liên tiếp bằng bàn phím là bất khả thi.
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
      attributes: { type: "button", "aria-label": `Xoá mắt xích ${nameOf(entry.provider_id)}` },
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
      // Nút vừa bấm thường TẮT ở đích (đầu/cuối chuỗi). focus() vào một nút disabled không có tác
      // dụng và nút cũ thì replaceChildren vừa tháo — rơi sang nút còn lại của cặp thì người dùng
      // bàn phím vẫn đứng trong hàng vừa dời thay vì bị ném về <body>.
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

  saveBtn.addEventListener("click", async () => {
    const problem = draftProblem(draft.entries, nameOf);
    if (problem) {
      say(problem);
      return;
    }
    saveBtn.disabled = true;
    say("Đang lưu combo…");
    try {
      const saved = await save({ revision: draft.revision, type: curType, entries: draft.entries });
      if (disposed) return;
      hideReload();
      // Chỉ revision đi theo phản hồi, KHÔNG cả snapshot: máy chủ dội lại đúng những mắt xích vừa
      // gửi, nên thay cả bản nháp chỉ nuốt mất lượt sửa người dùng làm trong lúc PUT còn đang bay.
      draft = { ...draft, revision: revisionOf(saved, draft.revision) };
      onSaved(saved);
      say("Đã lưu combo." + (live ? " Bot dùng ngay, không cần khởi động lại." : ""));
    } catch (error) {
      if (disposed || error?.name === "AbortError") return;
      if (error?.code === conflictCode) {
        // Bản nháp KHÔNG bị đụng tới: nó vẫn đúng, chỉ dựa trên một bản cũ. Vẽ lại ở đây là vứt mất
        // chỗ đứng của con trỏ mà chẳng đổi được gì trên màn hình.
        say(`${messageOf(error)}. Tải lại combo nếu muốn bỏ bản đang sửa.`);
        showReload();
        return;
      }
      say(`không lưu được: ${messageOf(error)}`);
    } finally {
      if (!disposed) saveBtn.disabled = false;
    }
  });

  reloadBtn.addEventListener("click", async () => {
    if (typeof reload !== "function") return;
    reloadBtn.disabled = true;
    say("Đang đọc lại combo đang lưu…");
    try {
      const fresh = await reload();
      if (disposed) return;
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

  const node = element("div", { className: "models-page" },
    statusLine,
    element("div", { className: "fc" },
      element("label", { className: "fk", attributes: { for: "combo-type" }, text: "Kiểu combo" }),
      typeSelect,
    ),
    rowsBox,
    element("div", { className: "row" }, add, saveBtn, reloadSlot, note),
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
    },
  });
}
