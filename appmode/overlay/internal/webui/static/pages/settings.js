import { requestJSON as sharedRequestJSON } from "../core/api.js";
import { element, pageHeader } from "../core/ui.js";
import {
  normalizeStatus,
  positiveRevision,
  projectStatus,
  successorRevision,
} from "./onboarding-contract.js";

const SAFE_SETTINGS_ERROR = "Không thể đọc trạng thái thiết lập an toàn. Vui lòng thử lại.";

function isCompletedStatus(status) {
  return status?.phase === "completed"
    && status.required === false
    && status.restart_in_progress === false
    && status.completed_version === status.current_version;
}

export function normalizeRestartStatus(response, revision) {
  const expectedRevision = successorRevision(revision);
  if (!expectedRevision) return null;
  const projected = projectStatus(response);
  const status = normalizeStatus(projected);
  if (!status
    || status.revision !== expectedRevision
    || status.phase !== "provider"
    || status.required !== true
    || status.restart_in_progress !== true
    || status.completed_version !== status.current_version
    || projected.provider_kind !== ""
    || projected.provider_id !== ""
    || projected.account_id !== ""
    || projected.model_id !== ""
    || status.providers.length !== 0) return null;
  return status;
}

export function createSettingsService(requestJSON = sharedRequestJSON) {
  if (typeof requestJSON !== "function") {
    throw new TypeError("Settings service requires a requestJSON function");
  }
  return Object.freeze({
    status(signal) {
      return Promise.resolve(requestJSON("/onboarding/status", { signal })).then(projectStatus);
    },
    restart(revision, signal) {
      const currentRevision = positiveRevision(revision);
      if (!currentRevision || !successorRevision(currentRevision)) {
        throw new RangeError("Settings restart revision is invalid");
      }
      return Promise.resolve(requestJSON("/onboarding/restart", {
        method: "POST",
        body: { confirmed: true, revision: currentRevision },
        signal,
      })).then((response) => {
        const status = normalizeRestartStatus(response, currentRevision);
        if (!status) throw new TypeError("Invalid onboarding restart response");
        return status;
      });
    },
  });
}

function validateService(service) {
  for (const method of ["status", "restart"]) {
    if (typeof service?.[method] !== "function") {
      throw new TypeError(`Settings service requires ${method}()`);
    }
  }
}

export function createSettingsController({
  service = createSettingsService(),
  onRestarted = () => {},
} = {}) {
  validateService(service);
  if (typeof onRestarted !== "function") {
    throw new TypeError("Settings restart callback must be a function");
  }

  let host = null;
  let root = null;
  let status = null;
  let statusController = null;
  let statusGeneration = 0;
  let disposed = false;
  let restartBusy = false;
  let handoffDone = false;
  const listeners = new Set();

  function listen(node, type, listener) {
    node.addEventListener(type, listener);
    listeners.add({ listener, node, type });
    return node;
  }

  function clearListeners() {
    for (const binding of listeners) {
      binding.node.removeEventListener(binding.type, binding.listener);
    }
    listeners.clear();
  }

  function replace(...children) {
    if (!root || disposed) return;
    root.replaceChildren(...children);
  }

  function beginStatusOperation() {
    statusGeneration += 1;
    statusController?.abort();
    statusController = new AbortController();
    return { controller: statusController, run: statusGeneration };
  }

  function owns(run) {
    return !disposed && root && run === statusGeneration;
  }

  function release(run, controller) {
    if (run === statusGeneration && statusController === controller) statusController = null;
  }

  function renderLoading() {
    clearListeners();
    replace(
      pageHeader("Settings", "Quản lý vòng đời thiết lập trợ lý."),
      element("section", {
        className: "hint settings-loading",
        attributes: { "aria-live": "polite", role: "status" },
        text: "Đang đọc trạng thái thiết lập…",
      }),
    );
  }

  function renderError() {
    clearListeners();
    const retry = element("button", {
      className: "btn go",
      attributes: { type: "button" },
      text: "Thử lại",
    });
    listen(retry, "click", () => { void retryStatus(); });
    replace(
      pageHeader("Settings", "Quản lý vòng đời thiết lập trợ lý."),
      element("section", { className: "hint settings-error", attributes: { role: "alert" } },
        element("div", { className: "bt", text: "Chưa thể tiếp tục" }),
        element("div", { className: "bd", text: SAFE_SETTINGS_ERROR }),
        retry,
      ),
    );
  }

  function renderReady() {
    clearListeners();
    const restart = element("button", {
      className: "btn",
      attributes: { type: "button" },
      text: "Thiết lập lại trợ lý",
    });
    listen(restart, "click", renderConfirmation);
    replace(
      pageHeader("Settings", "Quản lý vòng đời thiết lập trợ lý."),
      element("section", { className: "hint settings-onboarding" },
        element("h2", { text: "Thiết lập ban đầu" }),
        element("p", { text: "Trợ lý hiện đã hoàn tất thiết lập." }),
        restart,
      ),
    );
  }

  function renderConfirmation() {
    if (!isCompletedStatus(status) || handoffDone) return;
    clearListeners();
    const confirm = element("button", {
      className: "btn",
      attributes: { type: "button", disabled: restartBusy },
      text: restartBusy ? "Đang thiết lập lại…" : "Xác nhận thiết lập lại",
    });
    const cancel = element("button", {
      className: "btn",
      attributes: { type: "button", disabled: restartBusy },
      text: "Huỷ",
    });
    listen(confirm, "click", (event) => {
      event.currentTarget.disabled = true;
      void confirmRestart();
    });
    listen(cancel, "click", renderReady);
    replace(
      pageHeader("Settings", "Quản lý vòng đời thiết lập trợ lý."),
      element("section", { className: "hint settings-restart-warning", attributes: { role: "alert" } },
        element("div", { className: "bt", text: "Xác nhận thiết lập lại" }),
        element("div", { className: "bd", text: "Bạn sẽ phải đăng nhập và thiết lập lại từ đầu. Trình hướng dẫn phải được hoàn tất. Tuyến đang phục vụ vẫn hoạt động cho đến khi bạn bấm Complete." }),
        element("div", { className: "actions" }, confirm, cancel),
      ),
    );
  }

  function notifyRestartOwner(...args) {
    if (handoffDone) return false;
    handoffDone = true;
    restartBusy = false;
    try {
      Promise.resolve(onRestarted(...args)).catch(() => {});
    } catch {
      // The restart settlement is still consumed exactly once.
    }
    return true;
  }

  async function retryStatus() {
    if (disposed || !root) return false;
    const { controller, run } = beginStatusOperation();
    restartBusy = false;
    renderLoading();
    try {
      const response = await service.status(controller.signal);
      if (!owns(run)) return false;
      const normalized = normalizeStatus(projectStatus(response));
      if (!isCompletedStatus(normalized)) {
        status = null;
        renderError();
        return false;
      }
      status = normalized;
      renderReady();
      return true;
    } catch (error) {
      if (!owns(run) || controller.signal.aborted || error?.name === "AbortError") return false;
      status = null;
      renderError();
      return false;
    } finally {
      release(run, controller);
    }
  }

  async function confirmRestart() {
    if (disposed || restartBusy || handoffDone || !isCompletedStatus(status)) return false;
    const expected = status;
    statusGeneration += 1;
    statusController?.abort();
    statusController = null;
    const controller = new AbortController();
    restartBusy = true;
    renderConfirmation();
    try {
      const response = await service.restart(expected.revision, controller.signal);
      const restarted = normalizeRestartStatus(response, expected.revision);
      return restarted ? notifyRestartOwner(restarted) : notifyRestartOwner();
    } catch {
      return notifyRestartOwner();
    }
  }

  function mountController(container) {
    if (!container || typeof container.replaceChildren !== "function") {
      throw new TypeError("Settings page mount requires a host");
    }
    if (disposed) return controller;
    if (root && host === container && root.parentNode === container) return controller;
    host = container;
    root = element("div", { className: "settings-page" });
    host.replaceChildren(root);
    void retryStatus();
    return controller;
  }

  function dispose() {
    if (disposed) return;
    disposed = true;
    statusGeneration += 1;
    statusController?.abort();
    statusController = null;
    clearListeners();
    root?.remove();
    root = null;
    host = null;
    status = null;
  }

  const controller = Object.freeze({ dispose, mount: mountController, retry: retryStatus });
  return controller;
}

export function mount(container, context = {}) {
  const controller = createSettingsController({
    service: context.settingsService ?? createSettingsService(),
    onRestarted: context.onRestartOnboarding ?? (() => {}),
  });
  controller.mount(container);
  return controller;
}
