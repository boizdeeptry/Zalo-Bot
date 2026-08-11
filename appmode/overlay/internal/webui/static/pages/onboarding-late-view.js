import { createPersonaFields } from "../components/persona-fields.js";
import { element } from "../core/ui.js";

function actionButton(label, primary = false) {
  return element("button", {
    className: `onboarding-button onboarding-button--${primary ? "primary" : "secondary"}`,
    attributes: { type: "button" },
    text: label,
  });
}

export function createPersonaStage({ agent, listen, onChange, onSubmit, onBack }) {
  const counter = element("p", {
    className: "onboarding-persona-counter",
    attributes: { role: "status", "aria-live": "polite" },
  });
  const error = element("p", {
    className: "onboarding-error",
    attributes: { role: "alert", hidden: true },
  });
  const submit = element("button", {
    className: "onboarding-button onboarding-button--primary",
    attributes: { type: "submit" },
    text: "Tiếp tục",
  });
  const back = actionButton("Chọn lại nhà cung cấp");
  const form = element("form", { className: "onboarding-persona-form" });
  const fields = createPersonaFields({
    agent,
    onChange: (result) => {
      setValidation(result);
      error.hidden = true;
      onChange(result);
    },
    onLastEnter: (result) => onSubmit(result),
  });

  function setValidation(result, serverRemaining = 0) {
    const remaining = Math.max(result?.remaining ?? 0, serverRemaining);
    counter.textContent = `Còn ${remaining} mục cần điền`;
    submit.disabled = remaining > 0;
  }

  function setBusy(busy) {
    submit.disabled = busy || fields.validate().remaining > 0;
    back.disabled = busy;
  }

  function setError(message, serverRemaining = 0) {
    error.textContent = message;
    error.hidden = false;
    setValidation(fields.validate(), serverRemaining);
  }

  listen(form, "submit", (event) => {
    event?.preventDefault?.();
    onSubmit(fields.validate());
  });
  listen(back, "click", onBack);
  fields.mount(form);
  form.append(counter, error, element("div", { className: "onboarding-actions" }, back, submit));
  setValidation(fields.validate());
  return {
    content: element(
      "section",
      { className: "onboarding-stage onboarding-persona-stage" },
      element("p", { className: "onboarding-eyebrow", text: "Cá nhân hoá" }),
      element("h1", { text: "Trợ lý của bạn là ai" }),
      form,
    ),
    fields,
    setBusy,
    setError,
    setValidation,
    dispose: () => fields.dispose(),
  };
}

function chatBubble(className, label, content) {
  return element(
    "div",
    { className },
    element("strong", { className: "onboarding-chat-label", text: label }),
    element("p", { className: "onboarding-chat-text", text: content }),
  );
}

export function createTestStage({
  displayName,
  message,
  result,
  errorMessage,
  busy,
  canComplete,
  listen,
  onInput,
  onSend,
  onRetry,
  onBack,
  onComplete,
}) {
  const field = element("input", {
    className: "onboarding-chat-input",
    attributes: {
      type: "text",
      required: true,
      "aria-label": "Tin nhắn thử",
      "aria-describedby": "onboarding-chat-hint",
    },
  });
  field.value = message;
  const send = element("button", {
    className: "onboarding-button onboarding-button--primary",
    attributes: { type: "submit", disabled: busy },
    text: busy ? "Đang gửi…" : "Gửi thử",
  });
  const error = element("p", {
    className: "onboarding-error",
    attributes: { role: "alert", hidden: !errorMessage },
    text: errorMessage,
  });
  const form = element(
    "form",
    { className: "onboarding-chat-form" },
    field,
    element("p", {
      className: "onboarding-chat-hint",
      attributes: { id: "onboarding-chat-hint", role: "status", "aria-live": "polite" },
      text: "Tối đa 500 ký tự.",
    }),
    send,
  );
  listen(field, "input", () => onInput(field.value));
  listen(field, "keydown", (event) => {
    if (event?.key !== "Enter" || event.isComposing || event.keyCode === 229) return;
    event.preventDefault?.();
    onSend(field.value, field);
  });
  listen(form, "submit", (event) => {
    event?.preventDefault?.();
    onSend(field.value, field);
  });
  const back = actionButton("Quay lại chỉnh Persona");
  listen(back, "click", onBack);
  const retry = (result || errorMessage) ? actionButton("Thử lại") : null;
  if (retry) {
    retry.disabled = busy;
    listen(retry, "click", onRetry);
  }
  const confirm = canComplete ? actionButton("Ổn, dùng cấu hình này", true) : null;
  if (confirm) {
    confirm.disabled = busy;
    listen(confirm, "click", onComplete);
  }
  const transcript = result ? element(
    "div",
    {
      className: "onboarding-chat-transcript",
      attributes: { "aria-label": "Kết quả trò chuyện thử", "aria-live": "polite" },
    },
    chatBubble("onboarding-chat-user", "Bạn", result.message),
    chatBubble("onboarding-chat-bot", displayName, result.answer),
  ) : null;
  return {
    content: element(
      "section",
      { className: "onboarding-stage onboarding-test-stage" },
      element("p", { className: "onboarding-eyebrow", text: "Trò chuyện thử" }),
      element("h1", { text: `Thử trò chuyện với ${displayName}` }),
      form,
      error,
      transcript,
      element("div", { className: "onboarding-actions" }, back, retry, confirm),
    ),
    field,
    setBusy(value) {
      send.disabled = value;
      if (confirm) confirm.disabled = value;
      if (retry) retry.disabled = value;
    },
    showError(messageText) {
      error.textContent = messageText;
      error.hidden = !messageText;
    },
  };
}

export function createDoneStage({ displayName, listen, onPortal, onKnowledge }) {
  const portal = actionButton("Vào Portal", true);
  const knowledge = actionButton("Thêm kiến thức ngay");
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
