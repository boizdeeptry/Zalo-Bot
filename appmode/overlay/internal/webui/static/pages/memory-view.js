import { element } from "../core/ui.js";

const CATEGORY_LABELS = Object.freeze({
  profile: "Hồ sơ",
  family: "Gia đình",
  interest: "Sở thích",
  preference: "Ưu tiên",
  health: "Sức khoẻ",
  financial: "Tài chính",
  address: "Địa chỉ",
  identity: "Danh tính",
  order: "Đơn hàng",
});

const STATUS_LABELS = Object.freeze({
  active: "Đang hoạt động",
  pending: "Chờ duyệt",
  expired: "Đã hết hạn",
});

function localizedDate(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "không rõ ngày";
  return date.toLocaleDateString("vi-VN", {
    day: "2-digit",
    month: "2-digit",
    year: "numeric",
    timeZone: "UTC",
  });
}

export function memorySourceText(memory) {
  if (memory?.source === "agent" && memory.source_preview) {
    return `Nguồn: tin nhắn “${memory.source_preview}”`;
  }
  if (memory?.source === "operator") return "Nguồn: người trực thêm";
  return "Nguồn: dữ liệu cũ, chưa có tin gốc đáng tin";
}

function statusChip(status) {
  const normalized = STATUS_LABELS[status] ? status : "active";
  return element("span", {
    className: `memory-chip memory-chip-status is-${normalized}`,
    text: STATUS_LABELS[normalized],
  });
}

function categoryChip(category) {
  const label = CATEGORY_LABELS[category] || "Khác";
  return element("span", { className: "memory-chip", text: label });
}

function confidenceChip(confidence) {
  const value = Number(confidence);
  if (!Number.isFinite(value)) return null;
  const percent = Math.round(Math.min(1, Math.max(0, value)) * 100);
  return element("span", { className: "memory-chip", text: `Tin cậy ${percent}%` });
}

function memoryMetadata(memory, { status = memory?.status, includeStatus = true } = {}) {
  const chips = [];
  if (includeStatus) chips.push(statusChip(status));
  chips.push(categoryChip(memory?.category));
  const confidence = confidenceChip(memory?.confidence);
  if (confidence) chips.push(confidence);
  chips.push(element("span", { className: "memory-chip memory-chip-source", text: memorySourceText(memory) }));
  if (memory?.expires_at) {
    chips.push(element("span", {
      className: "memory-chip memory-chip-expiry",
      text: `Hết hạn ${localizedDate(memory.expires_at)}`,
    }));
  }
  return element("div", { className: "memory-entry-meta memory-chips" }, chips);
}

function actionButton(label, onClick, { disabled = false, destructive = false } = {}) {
  const button = element("button", {
    className: `memory-action${destructive ? " is-destructive" : ""}`,
    attributes: { type: "button", disabled },
    text: label,
    on: { click: (event) => onClick(event.currentTarget) },
  });
  button.disabled = disabled;
  return button;
}

function activeActions(memory, actions, pending) {
  return element("div", { className: "memory-entry-actions" },
    actionButton(memory.pinned ? "Bỏ ghim" : "Ghim", (opener) => actions.pinThread(memory, opener), { disabled: pending }),
    actionButton("Sửa", (opener) => actions.editThread(memory, opener), { disabled: pending }),
    actionButton("Xoá", (opener) => actions.deleteThread(memory, opener), { disabled: pending, destructive: true }),
  );
}

function memoryCard(memory, status, actions, pending) {
  return element("article", { className: `memory-entry memory-status-card is-${status}` },
    element("div", { className: "memory-pin", attributes: { "aria-hidden": "true" }, text: memory.pinned ? "★" : "" }),
    element("div", { className: "memory-entry-body" },
      element("div", { className: "memory-entry-text", text: memory.text }),
      memoryMetadata(memory, { status }),
      status === "active" ? activeActions(memory, actions, pending) : null,
    ),
  );
}

export function replacementCard(memory) {
  const target = memory.proposal_target;
  return element("article", { className: "memory-entry memory-status-card memory-replacement is-pending" },
    element("div", { className: "memory-pin", attributes: { "aria-hidden": "true" }, text: memory.pinned ? "★" : "" }),
    element("div", { className: "memory-entry-body" },
      element("div", { className: "memory-replacement-status" }, statusChip("pending")),
      element("div", { className: "memory-replacement-grid" },
        element("div", { className: "memory-replacement-side" },
          element("div", { className: "memory-replacement-label", text: "Thông tin hiện tại" }),
          element("div", { className: "memory-entry-text", text: target.text }),
          memoryMetadata(target, { status: "active" }),
        ),
        element("div", { className: "memory-replacement-side is-proposed" },
          element("div", { className: "memory-replacement-label", text: "Đề xuất mới" }),
          element("div", { className: "memory-entry-text", text: memory.text }),
          memoryMetadata(memory, { status: "pending", includeStatus: false }),
        ),
      ),
    ),
  );
}

function sectionEmpty(status) {
  const content = {
    active: ["Chưa có ghi chú cho hội thoại này", "Chưa có ghi chú đang hoạt động trong phạm vi này."],
    pending: ["Không có ghi chú chờ duyệt", "Các đề xuất cần xem lại sẽ hiện ở đây."],
    expired: ["Không có ghi chú hết hạn", "Ghi chú hết thời hạn sẽ được giữ ở đây."],
  }[status];
  return element("div", { className: "memory-empty memory-section-empty" },
    element("div", { className: "memory-empty-title", text: content[0] }),
    element("div", { className: "note", text: content[1] }),
  );
}

export function memorySection(status, items, actions, pending) {
  const memories = Array.isArray(items) ? items : [];
  return element("section", {
    className: `memory-status-section is-${status}`,
    attributes: { "aria-labelledby": `memory-section-${status}` },
  },
  element("div", { className: "memory-status-heading" },
    element("h3", { attributes: { id: `memory-section-${status}` }, text: STATUS_LABELS[status] }),
    element("span", { className: "memory-status-count", text: memories.length }),
  ),
  memories.length
    ? element("div", { className: "memory-entries" }, memories.map((entry) =>
      status === "pending" && entry.proposal_action === "replace" && entry.proposal_target
        ? replacementCard(entry)
        : memoryCard(entry, status, actions, pending)))
    : sectionEmpty(status),
  );
}

export function memorySections(detail, actions, pending) {
  const active = Array.isArray(detail?.active)
    ? detail.active
    : (Array.isArray(detail?.memories) ? detail.memories : []);
  return element("div", { className: "memory-status-sections" },
    memorySection("active", active, actions, pending),
    memorySection("pending", detail?.pending, actions, pending),
    memorySection("expired", detail?.expired, actions, pending),
  );
}

function scopeCountText(scope) {
  return `${Number(scope?.active || 0)} đang hoạt động · ${Number(scope?.pending || 0)} chờ duyệt`;
}

export function memberSelector(detail, selectedUID, onSelect) {
  if (detail?.thread?.thread_type !== "group") return null;
  const memberScopes = Array.isArray(detail.members) ? detail.members : [];
  const scopes = [
    { uid: "", name: "Chung cho nhóm", counts: detail.common },
    ...memberScopes.map((member, index) => ({
      uid: member.uid,
      name: member.name || `Thành viên ${index + 1}`,
      counts: member,
    })),
  ];
  const selectFromKeyboard = (event, index) => {
    let nextIndex = index;
    if (event.key === "ArrowRight") nextIndex = (index + 1) % scopes.length;
    else if (event.key === "ArrowLeft") nextIndex = (index - 1 + scopes.length) % scopes.length;
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = scopes.length - 1;
    else return;
    event.preventDefault();
    onSelect(scopes[nextIndex].uid, { focus: true });
  };
  return element("div", {
    className: "memory-member-tabs",
    attributes: { role: "tablist", "aria-label": "Phạm vi ghi nhớ" },
  }, scopes.map((scope, index) => {
    const selected = scope.uid === selectedUID;
    return element("button", {
      className: `memory-member-tab${selected ? " is-active" : ""}`,
      attributes: {
        id: `memory-member-tab-${index}`,
        type: "button",
        role: "tab",
        "aria-selected": String(selected),
        "aria-controls": "memory-subject-panel",
        tabindex: selected ? "0" : "-1",
        "data-subject-uid": scope.uid,
      },
      text: `${scope.name} ${scopeCountText(scope.counts)}`,
      on: {
        click: () => onSelect(scope.uid),
        keydown: (event) => selectFromKeyboard(event, index),
      },
    });
  }));
}
