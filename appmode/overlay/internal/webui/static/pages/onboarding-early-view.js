import { element } from "../core/ui.js";

export function providerName(kind, options = []) {
  return options.find((option) => option.kind === kind)?.display_name
    || (kind === "claude-code" ? "Claude Code" : kind === "codex" ? "Codex" : kind);
}

export function focusProviderControl(node, kind) {
  if (node?.classList?.contains("onboarding-provider-toggle")
      && node.dataset.providerKind === kind) {
    node.focus({ preventScroll: true });
    return true;
  }
  for (const child of node?.children ?? []) {
    if (focusProviderControl(child, kind)) return true;
  }
  return false;
}

function decorativeDashboard() {
  return element(
    "div",
    { className: "onboarding-dashboard", attributes: { "aria-hidden": "true" } },
    element("div", { className: "onboarding-dashboard-topbar" },
      element("span", { className: "onboarding-dashboard-menu", text: "☰" }),
      element("span", { className: "onboarding-dashboard-product", text: "◇" }),
      element("span", { className: "onboarding-dashboard-grid", text: "▦" }),
      element("span", { className: "onboarding-dashboard-window", text: "▣" }),
    ),
    element("div", { className: "onboarding-dashboard-sidebar" },
      element("div", { className: "onboarding-dashboard-brand" },
        element("span", { text: "TUVANZALO" }),
        element("span", { text: "Biến AI thành nhân sự thật" }),
      ),
      element("div", { className: "onboarding-dashboard-meter", text: "▰  LOCAL" }),
      element("div", { className: "onboarding-dashboard-project", text: "▰  Chọn dự án  ›" }),
      element("div", { className: "onboarding-dashboard-empty", text: "Chọn dự án để xem cuộc chat." }),
      element("div", { className: "onboarding-dashboard-user", text: "◉  TuvanZalo Local ⌄" }),
    ),
    element("div", { className: "onboarding-dashboard-canvas" },
      element("div", { className: "onboarding-dashboard-summary" },
        element("span", { className: "onboarding-dashboard-kicker", text: "AGENT" }),
        element("div", { className: "onboarding-dashboard-ready" },
          element("span", { text: "0/2" }),
          element("span", { text: "chưa thiết lập" }),
          element("span", { className: "onboarding-dashboard-bars", text: "▮▮" }),
        ),
        element("div", { className: "onboarding-dashboard-active" },
          element("span", { text: "└ Chưa dùng:" }),
          element("span", { text: "Claude Code, Codex" }),
        ),
        element("div", { className: "onboarding-dashboard-manage", text: "✧  Quản lý Agents" }),
      ),
    ),
  );
}

export function renderOnboardingShell(root, _phase, content) {
  const dialog = element(
    "section",
    {
      className: "onboarding-dialog",
      attributes: {
        "aria-labelledby": "onboarding-dialog-title",
        tabindex: "-1",
      },
    },
    element(
      "aside",
      { className: "onboarding-sidebar" },
      element("div", { className: "onboarding-dialog-heading" },
        element("span", {
          className: "onboarding-brand",
          attributes: { id: "onboarding-dialog-title" },
          text: "Thiết lập Tư Vấn Zalo",
        }),
        element("span", { className: "onboarding-dialog-lock", attributes: { "aria-hidden": "true" }, text: "◆" }),
      ),
    ),
    element("main", { className: "onboarding-main" }, content),
  );
  root.replaceChildren(element(
    "div",
    { className: "onboarding-shell" },
    decorativeDashboard(),
    element("div", { className: "onboarding-backdrop" }, dialog),
  ));
  dialog.focus({ preventScroll: true });
}

function actionButton(label, primary = false) {
  return element("button", {
    className: `onboarding-button onboarding-button--${primary ? "primary" : "secondary"}`,
    attributes: { type: "button" },
    text: label,
  });
}

function setupHeader(title, body) {
  return [
    element("p", { className: "onboarding-eyebrow", text: "Tư Vấn Zalo" }),
    element("h1", { text: title }),
    element("p", { className: "onboarding-provider-intro", text: body }),
  ];
}

function providerStatusBadge(label, selected = false) {
  return element("span", {
    className: `onboarding-agent-badge${selected ? " is-selected" : ""}`,
    text: label,
  });
}

function providerMark(option) {
  const known = ["codex", "claude-code"].includes(option.kind) ? option.kind : "generic";
  return element("span", {
    className: `onboarding-provider-mark onboarding-provider-mark--${known}`,
    attributes: { "aria-hidden": "true" },
    text: option.kind === "claude-code" ? "✣" : option.kind === "codex" ? "◎" : "◇",
  });
}

function providerSwitch(selected) {
  return element("span", {
    className: `onboarding-agent-switch${selected ? " is-on" : ""}`,
    attributes: { "aria-hidden": "true" },
  }, element("span", { className: "onboarding-agent-switch-knob" }));
}

function providerRow({ option, stage, suggested, busy, listen, onToggle, onInstall }) {
  const selected = Boolean(stage);
  const ready = stage?.status === "ready";
  const label = option.display_name;
  const toggle = element("button", {
    className: "onboarding-provider-toggle",
    attributes: {
      type: "button",
      "data-provider-kind": option.kind,
      "data-locked": String(ready),
      "aria-label": ready ? `${label} đã sẵn sàng` : `${selected ? "Tắt" : "Bật"} ${label}`,
      "aria-pressed": String(selected),
    },
  }, providerSwitch(selected));
  toggle.disabled = busy || ready;
  const install = selected && !ready ? element("button", {
    className: "onboarding-provider-install",
    attributes: { type: "button", "aria-label": `Cài ${label}` },
    text: "Bấm để cài.",
  }) : null;
  if (install) install.disabled = busy;
  const statusText = ready
    ? `Đã sẵn sàng · Ưu tiên ${stage.position + 1}`
    : selected ? "Chưa cài. " : "Chưa dùng";
  const status = element(
    "small",
    { className: "onboarding-provider-install-state" },
    element("span", { text: statusText }),
    install,
  );
  listen(toggle, "click", () => onToggle(option.kind, toggle));
  if (install) listen(install, "click", () => onInstall(option.kind));
  const badges = [];
  if (suggested) badges.push(providerStatusBadge("Đề xuất"));
  if (ready) badges.push(providerStatusBadge("Sẵn sàng", true));
  const card = element("div", {
    className: `onboarding-provider-card onboarding-agent-row${selected ? " is-selected" : ""}`,
    attributes: { "data-provider-kind": option.kind },
  }, providerMark(option), element(
    "span",
    { className: "onboarding-agent-row-main" },
    element("strong", { text: label }),
    element("small", { className: "onboarding-provider-description", text: option.description }),
    status,
    ready ? element("small", {
      className: "onboarding-provider-ready-hint",
      text: "Quản lý sau ở mục Provider/Combo.",
    }) : null,
  ), element(
    "span",
    { className: "onboarding-agent-row-tail" },
    badges,
    toggle,
  ));
  return { card, install, toggle };
}

function stagedProviderRow(option, stage, activeKind, phase) {
  const active = stage.kind === activeKind;
  const status = stage.status === "ready"
    ? `Đã sẵn sàng · Ưu tiên ${stage.position + 1}`
    : active ? (phase === "setup" ? "Đang thiết lập" : "Đang kết nối") : "Đang chờ";
  return element(
    "div",
    {
      className: `onboarding-provider-card onboarding-connect-provider-row onboarding-agent-row is-selected${active ? " is-active" : ""}`,
      attributes: { "data-provider-kind": stage.kind },
    },
    providerMark(option),
    element("span", { className: "onboarding-agent-row-main" },
      element("strong", { text: option.display_name }),
      element("small", { className: "onboarding-provider-install-state", text: status }),
    ),
    element("span", { className: "onboarding-agent-row-tail" },
      providerStatusBadge(stage.status === "ready" ? "Sẵn sàng" : active ? "Đang chạy" : "Đang chờ", true),
      providerSwitch(true),
    ),
  );
}

function stagedProviderList(state, phase) {
  const options = new Map(state.provider_options.map((option) => [option.kind, option]));
  return element("div", { className: "onboarding-provider-grid onboarding-agent-list" },
    state.providers.map((stage) => stagedProviderRow(
      options.get(stage.kind), stage, state.provider_kind, phase,
    )),
  );
}

function setupStatusPanel({ title, status, detail, children = [] }) {
  return element(
    "div",
    { className: "onboarding-agent-status-panel" },
    element("div", { className: "onboarding-agent-status-head" },
      element("span", { className: "onboarding-agent-status-title", text: title }),
      element("span", { className: "onboarding-agent-badge is-live", text: status }),
    ),
    element("p", { className: "onboarding-agent-status-detail", text: detail }),
    ...children,
  );
}

export function createRetryButton(listen, action) {
  return listen(actionButton("Thử lại"), "click", () => { void action(); });
}

export function createSafeErrorStage(message, retryButton) {
  return element(
    "section",
    { className: "onboarding-stage onboarding-error-stage" },
    element("h1", { text: "Chưa thể tiếp tục" }),
    element("p", { className: "onboarding-error", attributes: { role: "alert" }, text: message }),
    retryButton,
  );
}

export function createLoadingStatus(message) {
  return element("p", {
    className: "onboarding-status",
    attributes: { role: "status" },
    text: message,
  });
}

export function createWelcomeStage({
  state,
  message,
  busy = false,
  listen,
  onToggle,
  onRequestOff = () => false,
  onInstall,
  onRetry,
}) {
  let locked = busy;
  let retry = null;
  const rowViews = [];
  const stages = new Map(state.providers.map((stage) => [stage.kind, stage]));
  const providerControls = () => [
    ...rowViews.flatMap(({ toggle, install }) => install ? [toggle, install] : [toggle]),
    ...(retry ? [retry] : []),
  ];
  const disableProviderControls = (disabled) => {
    for (const control of providerControls()) {
      control.disabled = disabled || control.dataset.locked === "true";
    }
  };
  const run = (action) => {
    if (locked) return;
    locked = true;
    disableProviderControls(true);
    action();
  };
  const toggleProvider = (kind, origin) => {
    const stage = stages.get(kind);
    if (stage?.status === "ready") return;
    if (stage) onRequestOff({
      kind,
      label: providerName(kind, state.provider_options),
      opener: origin,
    });
    else run(() => onToggle(kind, true));
  };
  for (const option of state.provider_options) {
    rowViews.push(providerRow({
      option,
      stage: stages.get(option.kind),
      suggested: state.suggested_provider_kind === option.kind,
      busy,
      listen,
      onToggle: toggleProvider,
      onInstall: (kind) => run(() => onInstall(kind)),
    }));
  }
  const cards = rowViews.map(({ card }) => card);
  retry = message ? createRetryButton(listen, onRetry) : null;
  disableProviderControls(locked);
  const selectedCount = state.providers.length;
  return element(
    "section",
    { className: "onboarding-stage onboarding-provider-stage onboarding-agent-setup-stage" },
    ...setupHeader(
      "Thiết lập trợ lý Zalo",
      "Chọn nhà cung cấp muốn dùng cho trợ lý Zalo. Lựa chọn này chỉ được áp dụng sau khi kết nối được xác minh.",
    ),
    setupStatusPanel({
      title: "Trạng thái",
      status: selectedCount ? `Đã bật ${selectedCount}` : "Chưa chọn",
      detail: selectedCount
        ? "Các provider đã bật được lưu ngay. Bấm cài trên đúng hàng bạn muốn kết nối."
        : "Bật một hoặc nhiều provider bên dưới để chuẩn bị kết nối.",
    }),
    element("div", { className: "onboarding-provider-grid onboarding-agent-list" }, cards),
    element("p", {
      className: "onboarding-provider-warning",
      text: "Bạn cần đăng nhập. Sau khi kết nối được xác minh, hệ thống tự động chuyển sang “Đang chuẩn bị trợ lý…”. Cấu hình đang dùng chỉ thay đổi khi quá trình hoàn thành an toàn.",
    }),
    message ? element("p", {
      className: "onboarding-error", attributes: { role: "alert" }, text: message,
    }) : null,
    retry ? element("div", { className: "onboarding-actions" }, retry) : null,
  );
}

export function createConnectStage(state, slot) {
  const kind = state.provider_kind;
  const label = providerName(kind, state.provider_options);
  return element(
    "section",
    { className: "onboarding-stage onboarding-connect-stage onboarding-agent-setup-stage" },
    element("p", { className: "onboarding-eyebrow", text: "Tư Vấn Zalo" }),
    element("h1", { text: `Kết nối ${label}` }),
    element("p", {
      className: "onboarding-connect-intro",
      text: "Đang kết nối provider đã chọn. Provider Connect giữ nguyên thanh tiến trình và log để bạn nhìn được hệ thống đang làm gì.",
    }),
    stagedProviderList(state, "connect"),
    setupStatusPanel({
      title: "Trạng thái",
      status: "Đang kết nối",
      detail: `${label} đang được xác minh. Nếu quay lại, cấu hình thật vẫn chưa bị đổi.`,
    }),
    slot,
  );
}

export function createSetupStage(state, status, message, retryButton) {
  const complete = status === "complete";
  const error = status === "error";
  return element(
    "section",
    { className: "onboarding-stage onboarding-setup-stage onboarding-agent-setup-stage" },
    ...setupHeader(
      "Hàng đợi thiết lập",
      "Nhà cung cấp đã được xác minh. Tư Vấn Zalo đang chuẩn bị cấu hình rồi tự động chuyển sang “Đang chuẩn bị trợ lý…”.",
    ),
    stagedProviderList(state, "setup"),
    setupStatusPanel({
      title: "Trạng thái",
      status: complete ? "Hoàn tất" : (error ? "Cần thử lại" : "Đang thiết lập"),
      detail: complete
        ? "✓ Thiết lập xong. Đang chuẩn bị trợ lý…"
        : "Đang thiết lập… Đang chuẩn bị cấu hình, chọn model và tạo combo staging.",
      children: [element("p", {
        className: `onboarding-setup-status${complete ? " is-complete" : ""}`,
        attributes: { role: "status" },
        text: complete ? "✓" : "Đang chuẩn bị cấu hình…",
      })],
    }),
    error ? element("p", {
      className: "onboarding-error", attributes: { role: "alert" }, text: message,
    }) : null,
    error ? retryButton : null,
  );
}
