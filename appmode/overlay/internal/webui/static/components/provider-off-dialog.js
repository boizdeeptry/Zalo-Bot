import { element } from "../core/ui.js";

const SAFE_ERROR = "Chưa thể tắt provider. Trạng thái hiện tại vẫn được giữ nguyên.";
let activeOwner = null;

function semanticText(value, field) {
  if (typeof value !== "string" || value.trim() !== value || !value) {
    throw new TypeError(`${field} must be a non-empty trimmed string`);
  }
  return value;
}

export function createProviderOffDialog({ listen, onConfirm } = {}) {
  if (typeof listen !== "function" || typeof onConfirm !== "function") {
    throw new TypeError("Provider OFF dialog requires listen and onConfirm functions");
  }

  const owner = Object.freeze({});
  const bindings = [];
  const bind = (node, type, listener) => {
    listen(node, type, listener);
    bindings.push({ node, type, listener });
    return node;
  };
  const cancelButton = element("button", {
    className: "provider-off-dialog-button provider-off-dialog-cancel",
    attributes: { type: "button" },
    text: "Hủy",
  });
  const retryButton = element("button", {
    className: "provider-off-dialog-button provider-off-dialog-retry",
    attributes: { type: "button", hidden: true },
    text: "Thử lại",
  });
  const confirmButton = element("button", {
    className: "provider-off-dialog-button provider-off-dialog-confirm",
    attributes: { type: "button" },
    text: "Vẫn tắt",
  });
  const description = element("p", {
    className: "provider-off-dialog-description",
    attributes: { id: "provider-off-dialog-description" },
  });
  const status = element("p", {
    className: "provider-off-dialog-status",
    attributes: { role: "status", "aria-live": "polite", hidden: true },
  });
  const error = element("p", {
    className: "provider-off-dialog-error",
    attributes: { role: "alert", "aria-live": "assertive", hidden: true },
  });
  const dialog = element(
    "dialog",
    {
      className: "provider-off-dialog",
      attributes: {
        role: "alertdialog",
        "aria-modal": "true",
        "aria-labelledby": "provider-off-dialog-title",
        "aria-describedby": "provider-off-dialog-description",
      },
    },
    element("h2", { attributes: { id: "provider-off-dialog-title" }, text: "Xác nhận tắt provider" }),
    description,
    status,
    error,
    element("div", { className: "provider-off-dialog-actions" }, cancelButton, retryButton, confirmButton),
  );

  let disposed = false;
  let busy = false;
  let generation = 0;
  let pendingController = null;
  let opener = null;
  let resolveFocusFallback = null;
  let value;

  function owns(run) {
    return !disposed && activeOwner === owner && dialog.open && generation === run;
  }

  function resetView() {
    busy = false;
    status.hidden = true;
    status.textContent = "";
    error.hidden = true;
    error.textContent = "";
    retryButton.hidden = true;
    confirmButton.hidden = false;
    cancelButton.disabled = false;
    retryButton.disabled = false;
    confirmButton.disabled = false;
  }

  function restoreFocus(target, fallbackResolver) {
    if (target?.isConnected && target.disabled !== true) {
      target.focus({ preventScroll: true });
      return;
    }
    let fallback = null;
    try { fallback = fallbackResolver?.(); } catch { return; }
    if (fallback?.isConnected && fallback.disabled !== true && typeof fallback.focus === "function") {
      fallback.focus({ preventScroll: true });
    }
  }

  function close({ restore = true, abort = true } = {}) {
    if (activeOwner !== owner && !dialog.open) return false;
    const target = opener;
    const fallbackResolver = resolveFocusFallback;
    opener = null;
    resolveFocusFallback = null;
    value = undefined;
    generation += 1;
    if (abort) pendingController?.abort();
    pendingController = null;
    busy = false;
    if (dialog.open) dialog.close();
    dialog.remove();
    if (activeOwner === owner) activeOwner = null;
    if (restore) restoreFocus(target, fallbackResolver);
    return true;
  }

  function showFailure() {
    busy = false;
    status.hidden = true;
    status.textContent = "";
    error.textContent = SAFE_ERROR;
    error.hidden = false;
    confirmButton.hidden = true;
    retryButton.hidden = false;
    cancelButton.disabled = false;
    retryButton.disabled = false;
    confirmButton.disabled = false;
    retryButton.focus({ preventScroll: true });
  }

  async function attempt() {
    if (disposed || busy || activeOwner !== owner || !dialog.open) return false;
    busy = true;
    error.hidden = true;
    error.textContent = "";
    status.textContent = "Đang tắt provider…";
    status.hidden = false;
    cancelButton.disabled = true;
    retryButton.disabled = true;
    confirmButton.disabled = true;
    const run = ++generation;
    const controller = new AbortController();
    pendingController = controller;
    let result = false;
    try {
      result = await onConfirm(value, controller.signal);
    } catch {
      result = false;
    }
    if (!owns(run) || controller.signal.aborted) return false;
    pendingController = null;
    if (result === true) {
      close({ abort: false });
      return true;
    }
    showFailure();
    return false;
  }

  function open(input = {}) {
    if (disposed || activeOwner || dialog.open) return false;
    const label = semanticText(input.label, "Provider label");
    if (!input.opener || typeof input.opener.focus !== "function") {
      throw new TypeError("Provider OFF dialog requires an opener");
    }
    const copy = input.description === undefined
      ? `Nếu tắt ${label}, provider này sẽ không còn được sử dụng. Bạn vẫn muốn tắt chứ?`
      : semanticText(input.description, "Provider OFF description");
    opener = input.opener;
    resolveFocusFallback = typeof input.resolveFocusFallback === "function"
      ? input.resolveFocusFallback : null;
    value = input.value;
    description.textContent = copy;
    resetView();
    activeOwner = owner;
    document.body.append(dialog);
    try {
      dialog.showModal();
    } catch (cause) {
      dialog.remove();
      activeOwner = null;
      opener = null;
      resolveFocusFallback = null;
      value = undefined;
      throw cause;
    }
    cancelButton.focus({ preventScroll: true });
    return true;
  }

  function dispose() {
    if (disposed) return;
    disposed = true;
    close();
    for (const binding of bindings) {
      binding.node.removeEventListener(binding.type, binding.listener);
    }
    bindings.length = 0;
  }

  bind(cancelButton, "click", () => { if (!busy) close(); });
  bind(confirmButton, "click", () => { void attempt(); });
  bind(retryButton, "click", () => { void attempt(); });
  bind(dialog, "cancel", (event) => {
    event.preventDefault();
    if (!busy) close();
  });

  return Object.freeze({ open, dispose });
}
