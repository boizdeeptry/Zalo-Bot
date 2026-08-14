import { element } from "../core/ui.js";

const STEP_LABELS = Object.freeze(["Kết nối", "Cá nhân hoá", "Trò chuyện thử"]);

export const providerName = (kind) => kind === "claude-code" ? "Claude Code" : "Codex";

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

function currentStepIndex(phase) {
  if (phase === "persona") return 1;
  if (phase === "test" || phase === "completed") return 2;
  return 0;
}

function rail(phase) {
  const current = currentStepIndex(phase);
  return element(
    "nav",
    { className: "onboarding-rail", attributes: { "aria-label": "Tiến trình thiết lập" } },
    element("ol", { className: "onboarding-step-list" }, STEP_LABELS.map((label, index) => element(
      "li",
      {
        className: `onboarding-step${index === current ? " is-current" : ""}`,
        attributes: index === current ? { "aria-current": "step" } : {},
      },
      element("span", { className: "onboarding-step-index", text: index + 1 }),
      element("span", { className: "onboarding-step-label", text: label }),
    ))),
  );
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

export function renderOnboardingShell(root, phase, content) {
  const dialog = element(
    "section",
    {
      className: "onboarding-dialog",
      attributes: {
        role: "dialog",
        "aria-modal": "true",
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
      rail(phase),
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

function providerStatusBadge(selected) {
  return element("span", {
    className: `onboarding-agent-badge${selected ? " is-selected" : ""}`,
    text: selected ? "Đã chọn" : "Chưa chọn",
  });
}

function providerMark(kind) {
  return element("span", {
    className: `onboarding-provider-mark onboarding-provider-mark--${kind}`,
    attributes: { "aria-hidden": "true" },
    text: kind === "claude-code" ? "✣" : "◎",
  });
}

function providerSwitch(selected) {
  return element("span", {
    className: `onboarding-agent-switch${selected ? " is-on" : ""}`,
    attributes: { "aria-hidden": "true" },
  }, element("span", { className: "onboarding-agent-switch-knob" }));
}

function providerDetails(kind, label) {
  return element("span", { className: "onboarding-agent-row-main" },
    element("strong", { text: label }),
    element("small", { text: kind === "claude-code" ? "Claude Desktop/CLI account" : "Codex local account" }),
  );
}

function providerRow(kind, label, selected, listen, onToggle, onInstall) {
  const toggle = element("button", {
    className: "onboarding-provider-toggle",
    attributes: {
      type: "button",
      "data-provider-kind": kind,
      "aria-label": `${selected ? "Tắt" : "Bật"} ${label}`,
      "aria-pressed": String(selected),
    },
  }, providerSwitch(selected));
  const install = selected ? element("button", {
    className: "onboarding-provider-install",
    attributes: { type: "button" },
    text: "Bấm để cài.",
  }) : null;
  const status = element(
    "small",
    { className: "onboarding-provider-install-state" },
    element("span", { text: selected ? "Chưa cài. " : "Chưa dùng" }),
    install,
  );
  listen(toggle, "click", () => onToggle(kind, toggle));
  if (install) listen(install, "click", onInstall);
  const card = element("div", {
    className: `onboarding-provider-card onboarding-agent-row${selected ? " is-selected" : ""}`,
    attributes: { "data-provider-kind": kind },
  }, providerMark(kind), element(
    "span",
    { className: "onboarding-agent-row-main" },
    element("strong", { text: label }),
    status,
  ), element(
    "span",
    { className: "onboarding-agent-row-tail" },
    providerStatusBadge(selected),
    toggle,
  ));
  return { card, install, toggle };
}

function connectedProviderRow(kind) {
  return element(
    "div",
    { className: "onboarding-connect-provider-row onboarding-agent-row is-selected" },
    providerMark(kind),
    providerDetails(kind, providerName(kind)),
    element("span", { className: "onboarding-agent-row-tail" },
      element("span", { className: "onboarding-agent-badge is-selected", text: "Đã chọn" }),
      providerSwitch(true),
    ),
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
  selectedProvider,
  message,
  listen,
  onSelect,
  onProceed,
  onRetry,
}) {
  let installing = false, confirmationOpen = false;
  let confirmationOrigin = null, replacementKind = "";
  let retry = null;
  const confirmationHost = element("div");
  const rowViews = [];
  const providerControls = () => [
    ...rowViews.flatMap(({ toggle, install }) => install ? [toggle, install] : [toggle]),
    ...(retry ? [retry] : []),
  ];
  const disableProviderControls = (disabled) => {
    for (const control of providerControls()) control.disabled = disabled;
  };
  const dismissConfirmation = (restoreFocus = true) => {
    const origin = confirmationOrigin;
    confirmationOpen = false;
    confirmationOrigin = null;
    confirmationHost.replaceChildren();
    disableProviderControls(false);
    if (restoreFocus) origin?.focus({ preventScroll: true });
  };
  const cancel = actionButton("Huỷ");
  const confirm = actionButton("Vẫn tắt", true);
  const confirmationNode = element(
    "section",
    {
      className: "onboarding-provider-confirm",
      attributes: {
        role: "alertdialog",
        "aria-labelledby": "onboarding-provider-confirm-title",
        "aria-describedby": "onboarding-provider-confirm-description",
      },
    },
    element("h2", {
      attributes: { id: "onboarding-provider-confirm-title" },
      text: "Xác nhận tắt provider",
    }),
    element("p", {
      className: "onboarding-provider-confirm-description",
      attributes: { id: "onboarding-provider-confirm-description" },
      text: `Nếu tắt ${providerName(selectedProvider)} trước khi cài, lựa chọn này sẽ bị bỏ. Bạn vẫn muốn tắt chứ?`,
    }),
    element("div", { className: "onboarding-actions" }, cancel, confirm),
  );
  listen(cancel, "click", () => dismissConfirmation());
  listen(confirm, "click", () => {
    const replacement = replacementKind;
    dismissConfirmation(false);
    onSelect(replacement);
  });
  listen(confirmationNode, "keydown", (event) => {
    if (event.key !== "Escape") return;
    event.preventDefault();
    dismissConfirmation();
  });
  const showConfirmation = (replacement, origin) => {
    if (confirmationOpen || installing) return;
    confirmationOpen = true;
    confirmationOrigin = origin;
    replacementKind = replacement;
    confirmationHost.replaceChildren(confirmationNode);
    disableProviderControls(true);
    cancel.focus({ preventScroll: true });
  };
  const toggleProvider = (kind, origin) => {
    if (installing || confirmationOpen) return;
    if (!selectedProvider) {
      onSelect(kind);
      return;
    }
    showConfirmation(kind === selectedProvider ? "" : kind, origin);
  };
  const installProvider = () => {
    if (installing || confirmationOpen || !selectedProvider) return;
    installing = true;
    disableProviderControls(true);
    onProceed();
  };
  for (const [kind, label] of [["claude-code", "Claude Code"], ["codex", "Codex"]]) {
    rowViews.push(providerRow(
      kind,
      label,
      selectedProvider === kind,
      listen,
      toggleProvider,
      installProvider,
    ));
  }
  const cards = rowViews.map(({ card }) => card);
  retry = message ? createRetryButton(listen, onRetry) : null;
  return element(
    "section",
    { className: "onboarding-stage onboarding-provider-stage onboarding-agent-setup-stage" },
    ...setupHeader(
      "Thiết lập trợ lý Zalo",
      "Chọn nhà cung cấp muốn dùng cho trợ lý Zalo. Lựa chọn này chỉ được áp dụng sau khi kết nối được xác minh.",
    ),
    setupStatusPanel({
      title: "Trạng thái",
      status: selectedProvider ? "Đã có lựa chọn" : "Chưa chọn",
      detail: selectedProvider
        ? `Đang chọn ${providerName(selectedProvider)}. Có thể bắt đầu cài đặt ngay trong hàng bên dưới.`
        : "Bật một provider bên dưới để chuẩn bị kết nối.",
    }),
    element("div", { className: "onboarding-provider-grid onboarding-agent-list" }, cards),
    confirmationHost,
    element("p", {
      className: "onboarding-provider-warning",
      text: "Bạn cần đăng nhập. Cấu hình đang dùng chỉ thay đổi sau khi kết nối được xác minh, Chat thử đạt yêu cầu và bạn bấm Hoàn tất.",
    }),
    message ? element("p", {
      className: "onboarding-error", attributes: { role: "alert" }, text: message,
    }) : null,
    retry ? element("div", { className: "onboarding-actions" }, retry) : null,
  );
}

export function createConnectStage(kind, slot) {
  return element(
    "section",
    { className: "onboarding-stage onboarding-connect-stage onboarding-agent-setup-stage" },
    element("p", { className: "onboarding-eyebrow", text: "Tư Vấn Zalo" }),
    element("h1", { text: `Kết nối ${providerName(kind)}` }),
    element("p", {
      className: "onboarding-connect-intro",
      text: "Đang kết nối provider đã chọn. Provider Connect giữ nguyên thanh tiến trình và log để bạn nhìn được hệ thống đang làm gì.",
    }),
    connectedProviderRow(kind),
    setupStatusPanel({
      title: "Trạng thái",
      status: "Đang kết nối",
      detail: `${providerName(kind)} đang được xác minh. Nếu quay lại, cấu hình thật vẫn chưa bị đổi.`,
    }),
    slot,
  );
}

export function createSetupStage(status, message, retryButton) {
  const complete = status === "complete";
  const error = status === "error";
  return element(
    "section",
    { className: "onboarding-stage onboarding-setup-stage onboarding-agent-setup-stage" },
    ...setupHeader(
      "Hàng đợi thiết lập",
      "Nhà cung cấp đã được xác minh. Tư Vấn Zalo đang chuẩn bị cấu hình trước khi chuyển sang bước cá nhân hoá.",
    ),
    setupStatusPanel({
      title: "Trạng thái",
      status: complete ? "Hoàn tất" : (error ? "Cần thử lại" : "Đang thiết lập"),
      detail: complete
        ? "✓ Thiết lập xong. Đang mở bước cá nhân hoá Persona…"
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
