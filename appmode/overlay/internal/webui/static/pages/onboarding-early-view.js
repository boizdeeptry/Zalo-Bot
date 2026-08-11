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
  const cards = [["codex", "Codex"], ["claude-code", "Claude Code"]].map(([kind, label]) => {
    const card = element("button", {
      className: `onboarding-provider-card${selectedProvider === kind ? " is-selected" : ""}`,
      attributes: {
        type: "button",
        "data-provider-kind": kind,
        "aria-pressed": String(selectedProvider === kind),
      },
      text: label,
    });
    return listen(card, "click", () => onSelect(kind));
  });
  const proceed = actionButton("Tiếp tục", true);
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
    { className: "onboarding-stage onboarding-provider-stage" },
    element("p", { className: "onboarding-eyebrow", text: "Chào mừng" }),
    element("h1", { text: "Chọn nhà cung cấp" }),
    element("p", {
      className: "onboarding-provider-intro",
      text: "Chọn công cụ AI bạn muốn dùng cho trợ lý.",
    }),
    element("div", { className: "onboarding-provider-grid" }, cards),
    element("p", {
      className: "onboarding-provider-warning",
      text: "Bạn cần đăng nhập. Cấu hình đang dùng chỉ thay đổi sau khi kết nối được xác minh và bạn bấm Hoàn tất.",
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
    { className: "onboarding-stage onboarding-connect-stage" },
    element("h1", { text: `Kết nối ${providerName(kind)}` }),
    element("p", {
      className: "onboarding-connect-intro",
      text: "Đăng nhập và chờ hệ thống xác minh tài khoản.",
    }),
    slot,
  );
}

export function createSetupStage(status, message, retryButton) {
  const complete = status === "complete";
  const error = status === "error";
  return element(
    "section",
    { className: "onboarding-stage onboarding-setup-stage" },
    element("h1", { text: "Kết nối nhà cung cấp" }),
    element("p", {
      className: `onboarding-setup-status${complete ? " is-complete" : ""}`,
      attributes: { role: "status" },
      text: complete ? "✓" : "Đang chuẩn bị cấu hình…",
    }),
    error ? element("p", {
      className: "onboarding-error", attributes: { role: "alert" }, text: message,
    }) : null,
    error ? retryButton : null,
  );
}
