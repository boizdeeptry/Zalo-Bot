import { element } from "../core/ui.js";

const STEP_LABELS = Object.freeze(["Kết nối", "Cá nhân hoá", "Trò chuyện thử"]);

export const providerName = (kind) => kind === "claude-code" ? "Claude Code" : "Codex";

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

export function renderOnboardingShell(root, phase, content) {
  root.replaceChildren(element(
    "div",
    { className: "onboarding-shell" },
    element(
      "aside",
      { className: "onboarding-sidebar" },
      element("div", { className: "onboarding-brand", text: "TuvanZalo" }),
      rail(phase),
    ),
    element("main", { className: "onboarding-main" }, content),
  ));
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
    element("p", { className: "onboarding-eyebrow", text: "Agent Setup" }),
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

function providerRow(kind, label, selected, listen, onSelect) {
  const card = element("button", {
    className: `onboarding-provider-card onboarding-agent-row${selected ? " is-selected" : ""}`,
    attributes: {
      type: "button",
      "data-provider-kind": kind,
      "aria-pressed": String(selected),
    },
  }, element("span", { className: "onboarding-agent-row-main" },
    element("strong", { text: label }),
    element("small", { text: kind === "claude-code" ? "Claude Desktop/CLI account" : "Codex local account" }),
  ), providerStatusBadge(selected));
  return listen(card, "click", () => onSelect(kind));
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
  const cards = [["codex", "Codex"], ["claude-code", "Claude Code"]]
    .map(([kind, label]) => providerRow(kind, label, selectedProvider === kind, listen, onSelect));
  const proceed = actionButton("Tiếp tục kết nối", true);
  proceed.disabled = !selectedProvider;
  let selecting = false;
  listen(proceed, "click", () => {
    if (selecting || !selectedProvider) return;
    selecting = true;
    proceed.disabled = true;
    for (const card of cards) card.disabled = true;
    onProceed();
  });
  const retry = message ? createRetryButton(listen, onRetry) : null;
  return element(
    "section",
    { className: "onboarding-stage onboarding-provider-stage onboarding-agent-setup-stage" },
    ...setupHeader(
      "Thiết lập Agent",
      "Chọn nhà cung cấp: Chọn provider muốn dùng cho trợ lý Zalo. Giống AGS, lựa chọn ở đây là ý định của bạn; server chỉ đổi cấu hình thật sau khi xác minh.",
    ),
    setupStatusPanel({
      title: "Trạng thái",
      status: selectedProvider ? "Đã có lựa chọn" : "Chưa chọn",
      detail: selectedProvider
        ? `Đang chọn ${providerName(selectedProvider)}. Bấm Tiếp tục kết nối để bắt đầu xác minh.`
        : "Chọn một provider bên dưới để bắt đầu kết nối.",
    }),
    element("div", { className: "onboarding-provider-grid onboarding-agent-list" }, cards),
    element("p", {
      className: "onboarding-provider-warning",
      text: "Bạn cần đăng nhập. Cấu hình đang dùng chỉ thay đổi sau khi kết nối được xác minh, Chat thử đạt yêu cầu và bạn bấm Hoàn tất.",
    }),
    message ? element("p", {
      className: "onboarding-error", attributes: { role: "alert" }, text: message,
    }) : null,
    element("div", { className: "onboarding-actions" }, retry, proceed),
  );
}

export function createConnectStage(kind, slot) {
  return element(
    "section",
    { className: "onboarding-stage onboarding-connect-stage onboarding-agent-setup-stage" },
    element("p", { className: "onboarding-eyebrow", text: "Agent Setup" }),
    element("h1", { text: `Kết nối ${providerName(kind)}` }),
    element("p", {
      className: "onboarding-connect-intro",
      text: "Đang kết nối provider đã chọn. Provider Connect giữ nguyên thanh tiến trình và log để bạn nhìn được hệ thống đang làm gì.",
    }),
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
      "Provider đã xác minh. Portal đang tạo cấu hình staging giống hàng đợi setup của AGS trước khi chuyển sang Persona.",
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
