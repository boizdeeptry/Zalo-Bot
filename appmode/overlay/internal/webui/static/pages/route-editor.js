// route-editor.js giữ phần DÙNG LẠI cho trang Combos khi sửa một chuỗi mắt xích Provider/model: các
// hàm thuần canh biên bản nháp, các hàm dựng lựa chọn từ danh sách Provider, và các hàm sinh câu
// trạng thái. Test gọi thẳng các hàm thuần đã XUẤT. Không hàm nào ở đây tự gọi API — mọi lượt đọc/ghi
// do trang truyền vào. Cỗ máy dựng UI (modal chọn model + danh sách mắt xích + tự lưu) nằm ở combos.js.

export function messageOf(error) {
  return error instanceof Error ? error.message : String(error);
}

// TYPE_LABELS: hai kiểu combo mà §Combos hỗ trợ. Trang Combos dùng nhãn cho huy hiệu trên danh sách
// và cho ô chọn kiểu trong editor.
export const TYPE_LABELS = Object.freeze({
  fallback: "Fallback",
  round_robin: "Round Robin",
});

export const TYPE_ORDER = Object.freeze(["fallback", "round_robin"]);

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

// --- cảnh báo mắt xích + phân giải model từ danh sách Provider ---

function modelsOf(providers, providerID) {
  return providers.find((provider) => provider.id === providerID)?.models ?? [];
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
