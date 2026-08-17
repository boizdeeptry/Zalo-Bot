import { element } from "../core/ui.js";

function actionButton(label, primary = false) {
  return element("button", {
    className: `onboarding-button onboarding-button--${primary ? "primary" : "secondary"}`,
    attributes: { type: "button" },
    text: label,
  });
}

export function createBootstrapStage({ busy, errorMessage, listen, onRetry, onBack }) {
  const retry = errorMessage ? actionButton("Thử lại", true) : null;
  const back = actionButton("Quay lại Provider");
  if (retry) listen(retry, "click", onRetry);
  listen(back, "click", onBack);
  if (retry) retry.disabled = busy;
  back.disabled = busy;
  return element(
    "section",
    { className: "onboarding-stage onboarding-bootstrap-stage" },
    element("h1", {
      attributes: { "aria-live": "polite" },
      text: "Đang chuẩn bị trợ lý…",
    }),
    errorMessage ? element("p", {
      className: "onboarding-error",
      attributes: { role: "alert" },
      text: errorMessage,
    }) : null,
    element("div", { className: "onboarding-actions" }, back, retry),
  );
}

export function createDoneStage({ displayName, listen, onPortal, onKnowledge }) {
  const portal = actionButton("Vào Portal", true);
  const knowledge = actionButton("Thêm tài liệu doanh nghiệp");
  listen(portal, "click", onPortal);
  listen(knowledge, "click", onKnowledge);
  return {
    content: element(
      "section",
      { className: "onboarding-stage onboarding-done-stage" },
      element("h1", { text: `${displayName} đã sẵn sàng!` }),
      element("p", {
        text: "Bạn có thể bổ sung tài liệu ở mục Kiến thức để trợ lý tư vấn chính xác hơn.",
      }),
      element("div", { className: "onboarding-actions" }, portal, knowledge),
    ),
    disable() {
      portal.disabled = true;
      knowledge.disabled = true;
    },
  };
}
