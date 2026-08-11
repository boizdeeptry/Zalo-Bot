import { element } from "../core/ui.js";

export const INSTALL_CEILING = 85;
export const MAX_CONNECT_LOG_LENGTH = 1_000;

const PHASE_ANCHOR = Object.freeze({
  detecting: 8,
  installing: 12,
  awaiting_login: 90,
  polling: 90,
  connected: 100,
});
const TERMINAL_PHASES = new Set(["connected", "error", "canceled"]);
const BACKEND_PHASES = new Set([
  "detecting",
  "installing",
  "awaiting_login",
  "polling",
  "connected",
  "error",
  "canceled",
]);
const IDLE_MESSAGE = "Không còn tác vụ kết nối đang hoạt động. Vui lòng thử lại.";
const PROTOCOL_ERROR_MESSAGE = "Phản hồi trạng thái kết nối không hợp lệ. Vui lòng thử lại.";
const CONTROL_CHARACTERS = /[\u0000-\u001f\u007f-\u009f]/;

export function phaseProgress(phase) {
  return PHASE_ANCHOR[phase] ?? null;
}

function safeLogLine(value) {
  if (typeof value !== "string") return "";
  return value
    .replace(/[\u0000-\u001f\u007f-\u009f]/g, "")
    .slice(0, MAX_CONNECT_LOG_LENGTH);
}

function safeSemanticString(value) {
  if (typeof value !== "string"
    || value.length > MAX_CONNECT_LOG_LENGTH
    || CONTROL_CHARACTERS.test(value)
    || value.trim() !== value) {
    return "";
  }
  return value;
}

function safeIdentifier(value) {
  return safeSemanticString(value);
}

function safeHTTPSURL(value) {
  const candidate = safeSemanticString(value);
  if (!candidate) return "";
  try {
    const parsed = new URL(candidate);
    return parsed.protocol === "https:" && parsed.hostname ? candidate : "";
  } catch {
    return "";
  }
}

function positiveRevision(value) {
  const revision = Number(value);
  return Number.isSafeInteger(revision) && revision > 0 ? revision : 0;
}

function validateService(service) {
  for (const method of ["connectStart", "connectStatus", "connectCancel"]) {
    if (typeof service?.[method] !== "function") {
      throw new TypeError(`Provider Connect service requires ${method}()`);
    }
  }
}

function normalizeServiceSnapshot(snapshot, shape = "status") {
  const value = snapshot && typeof snapshot === "object" && !Array.isArray(snapshot)
    ? snapshot
    : {};
  if (shape === "cancel") {
    return {
      ok: value.ok === true,
      message: safeLogLine(value.message),
      error: safeLogLine(value.error),
    };
  }
  const normalized = {
    kind: safeIdentifier(value.kind),
    phase: value.phase,
    message: safeLogLine(value.message),
    log: safeLogLine(value.log),
    error: safeLogLine(value.error),
    loginUrl: safeHTTPSURL(value.loginUrl),
    code: safeLogLine(value.code),
    providerId: safeIdentifier(value.providerId),
    accountId: safeIdentifier(value.accountId),
  };
  if (value.phase === "idle") {
    return { ...normalized, phase: "error", message: IDLE_MESSAGE, error: "" };
  }
  if (typeof value.phase !== "string" || !BACKEND_PHASES.has(value.phase)) {
    return { ...normalized, phase: "error", message: PROTOCOL_ERROR_MESSAGE, error: "" };
  }
  return normalized;
}

export function createProviderConnect({
  kind,
  service,
  onConnected,
  onBack = null,
  pollDelayMs = 700,
  crawlDelayMs = 500,
  now = Date.now,
  setIntervalFn = setInterval,
  clearIntervalFn = clearInterval,
  setTimeoutFn = setTimeout,
  clearTimeoutFn = clearTimeout,
} = {}) {
  const providerKind = String(kind ?? "").trim();
  if (!providerKind) throw new TypeError("Provider Connect requires a kind");
  validateService(service);
  if (typeof onConnected !== "function") {
    throw new TypeError("Provider Connect requires an onConnected callback");
  }
  if (onBack !== null && typeof onBack !== "function") {
    throw new TypeError("Provider Connect onBack must be a function");
  }
  if (typeof now !== "function") throw new TypeError("Provider Connect now must be a function");

  let container = null;
  let disposed = false;
  let generation = 0;
  let connect = null;
  let connectPct = 0;
  let connectStartedAt = 0;
  let connectTimer = null;
  let pollTimer = null;
  let releasePoll = null;
  let cancelPromise = null;
  let progressFillRef = null;
  let progressBarRef = null;
  let progressPctRef = null;
  let progressElapsedRef = null;
  const listeners = new Set();

  const ownsRun = (run) => !disposed && generation === run && connect?.kind === providerKind;
  const elapsedText = () => `· ${Math.max(0, Math.floor((now() - connectStartedAt) / 1_000))}s`;

  function clearListeners() {
    for (const binding of listeners) {
      binding.node.removeEventListener(binding.type, binding.listener);
    }
    listeners.clear();
  }

  function listen(node, type, listener) {
    node.addEventListener(type, listener);
    listeners.add({ node, type, listener });
    return node;
  }

  function clearPollTimer() {
    if (pollTimer !== null) {
      clearTimeoutFn(pollTimer);
      pollTimer = null;
    }
    const release = releasePoll;
    releasePoll = null;
    if (release) release(false);
  }

  function stopConnectTimer() {
    if (connectTimer !== null) {
      clearIntervalFn(connectTimer);
      connectTimer = null;
    }
  }

  function stopTimers() {
    stopConnectTimer();
    clearPollTimer();
  }

  function anchorTo(phase) {
    const anchor = phaseProgress(phase);
    if (anchor !== null) connectPct = Math.max(connectPct, anchor);
  }

  function tickConnect() {
    if (!connect || TERMINAL_PHASES.has(connect.phase)) {
      stopConnectTimer();
      return;
    }
    if (connect.phase === "installing") {
      connectPct = Math.min(
        INSTALL_CEILING - 0.1,
        connectPct + (INSTALL_CEILING - connectPct) * 0.08,
      );
    }
    if (progressFillRef) {
      const rounded = Math.round(connectPct);
      progressFillRef.style.width = `${connectPct}%`;
      progressBarRef?.setAttribute("aria-valuenow", String(rounded));
      if (progressPctRef) progressPctRef.textContent = `${rounded}%`;
    }
    if (progressElapsedRef) progressElapsedRef.textContent = elapsedText();
  }

  function startConnectTimer() {
    stopConnectTimer();
    connectTimer = setIntervalFn(tickConnect, Math.max(0, Number(crawlDelayMs) || 0));
    if (connectTimer && typeof connectTimer.unref === "function") connectTimer.unref();
  }

  function waitToPoll(run) {
    clearPollTimer();
    return new Promise((resolve) => {
      releasePoll = resolve;
      pollTimer = setTimeoutFn(() => {
        pollTimer = null;
        releasePoll = null;
        resolve(ownsRun(run));
      }, Math.max(0, Number(pollDelayMs) || 0));
      if (pollTimer && typeof pollTimer.unref === "function") pollTimer.unref();
    });
  }

  function renderProgress(frozen) {
    const fill = element("div", { className: "pv-progress-fill" });
    fill.style.width = `${connectPct}%`;
    const bar = element("div", {
      className: `pv-progress${frozen ? " pv-progress--error" : ""}`,
      attributes: {
        role: "progressbar",
        "aria-valuemin": "0",
        "aria-valuemax": "100",
        "aria-valuenow": String(Math.round(connectPct)),
      },
    }, fill);
    const pct = element("span", {
      className: "pv-progress-pct",
      text: `${Math.round(connectPct)}%`,
    });
    const elapsed = element("span", { className: "pv-progress-elapsed", text: elapsedText() });
    if (!frozen) {
      progressFillRef = fill;
      progressBarRef = bar;
      progressPctRef = pct;
      progressElapsedRef = elapsed;
    }
    return element(
      "div",
      { className: "pv-progress-wrap" },
      bar,
      element("div", { className: "pv-progress-row" }, pct, elapsed),
    );
  }

  function phaseLabel(phase, message) {
    switch (phase) {
      case "detecting": return "Đang kiểm tra…";
      case "installing": return "Đang cài đặt gói…";
      case "awaiting_login": return providerKind === "claude-code"
        ? "Bấm link để đăng nhập Claude ở trình duyệt vừa mở, rồi chờ xác nhận…"
        : "Mở trang đăng nhập, nhập mã bên dưới, rồi chờ xác nhận…";
      case "polling": return "Đang xác nhận đăng nhập…";
      case "connected": return "Đã kết nối.";
      default: return message || "";
    }
  }

  function invokeBack() {
    if (!onBack) return;
    try {
      const result = onBack();
      if (result && typeof result.catch === "function") result.catch(() => {});
    } catch {
      // Navigation hooks are outside the component lifecycle.
    }
  }

  function dismiss() {
    if (disposed) return;
    generation++;
    cancelPromise = null;
    stopTimers();
    connect = null;
    render();
    invokeBack();
  }

  function renderPrompt() {
    const input = element("input", {
      className: "pv-connect-label",
      attributes: { type: "text", value: connect.label, "aria-label": "Tên tài khoản" },
    });
    const begin = listen(element("button", {
      className: "pv-btn primary",
      attributes: { type: "button" },
      text: "Bắt đầu kết nối",
    }), "click", () => {
      void runConnect(input.value, connect.onboardingRevision);
    });
    const close = listen(element("button", {
      className: "pv-btn",
      attributes: { type: "button" },
      text: "Huỷ",
    }), "click", dismiss);
    return element("div", { className: "pv-connect-prompt" }, input, begin, close);
  }

  function renderTerminal() {
    const installHint = providerKind === "claude-code" && connect.phase === "error"
      ? element(
          "div",
          { className: "pv-connect-hint" },
          element("span", { text: "Chưa cài Claude Code? Tải tại " }),
          element("a", {
            attributes: {
              href: "https://claude.com/claude-code",
              target: "_blank",
              rel: "noopener",
            },
            text: "claude.com/claude-code",
          }),
        )
      : null;
    const close = listen(element("button", {
      className: "pv-btn",
      attributes: { type: "button" },
      text: "Đóng",
    }), "click", dismiss);
    return element(
      "div",
      { className: "pv-connect-status" },
      renderProgress(true),
      element("div", {
        className: "pv-connect-message",
        text: connect.message || (connect.phase === "canceled" ? "Đã huỷ kết nối." : "Kết nối thất bại."),
      }),
      installHint,
      close,
    );
  }

  function renderLive() {
    const loginLink = typeof connect.loginUrl === "string" && connect.loginUrl.startsWith("https://")
      ? element("a", {
          className: "pv-btn primary",
          attributes: { href: connect.loginUrl, target: "_blank", rel: "noopener" },
          text: "Mở trang đăng nhập",
        })
      : null;
    const codeBlock = connect.code
      ? element(
          "div",
          { className: "pv-connect-code" },
          element("span", { className: "pv-connect-code-label", text: "Mã đăng nhập:" }),
          element("code", { className: "pv-connect-code-value", text: connect.code }),
        )
      : null;
    const logLine = connect.message
      ? element("div", { className: "pv-connect-log", text: connect.message })
      : null;
    const cancelWarning = connect.cancelWarning
      ? element("div", {
          className: "pv-connect-log pv-connect-warning",
          attributes: { role: "status" },
          text: connect.cancelWarning,
        })
      : null;
    const cancel = listen(element("button", {
      className: "pv-btn",
      attributes: { type: "button" },
      text: "Huỷ",
    }), "click", () => { void cancelConnect(); });
    return element(
      "div",
      { className: "pv-connect-live" },
      renderProgress(false),
      cancelWarning,
      logLine,
      element(
        "div",
        { className: "pv-connect-status" },
        element("div", {
          className: "pv-connect-message",
          text: phaseLabel(connect.phase, connect.message),
        }),
        loginLink,
        codeBlock,
        cancel,
      ),
    );
  }

  function render() {
    progressFillRef = null;
    progressBarRef = null;
    progressPctRef = null;
    progressElapsedRef = null;
    clearListeners();
    if (!container || disposed) return;
    if (!connect) {
      container.replaceChildren();
      return;
    }
    if (connect.phase === "prompt") {
      container.replaceChildren(renderPrompt());
      return;
    }
    if (connect.phase === "error" || connect.phase === "canceled") {
      container.replaceChildren(renderTerminal());
      return;
    }
    container.replaceChildren(renderLive());
  }

  function setStatus(status) {
    const phase = status.phase;
    const previousPhase = connect.phase;
    connect = {
      ...connect,
      phase,
      message: status.error || status.message || status.log,
      loginUrl: status.loginUrl || connect.loginUrl,
      code: status.code || connect.code,
    };
    if (phase !== previousPhase) anchorTo(phase);
    render();
  }

  async function finishConnected(status) {
    const providerId = status.providerId;
    const accountId = status.accountId;
    if (!providerId || !accountId) {
      generation++;
      stopTimers();
      connect = {
        ...connect,
        phase: "error",
        message: "Kết nối hoàn tất nhưng thiếu định danh Provider hoặc tài khoản.",
      };
      render();
      return;
    }

    generation++;
    stopTimers();
    connect = null;
    render();
    try {
      await onConnected({ kind: providerKind, providerId, accountId });
    } catch {
      // A consumer callback cannot resurrect or reject the completed connect run.
    }
  }

  async function applyServiceStatus(run, snapshot) {
    const status = normalizeServiceSnapshot(snapshot);
    if (!ownsRun(run)) return true;
    if (status.phase === "connected"
      && (!status.providerId || !status.accountId)) {
      await finishConnected(status);
      return true;
    }
    setStatus(status);
    if (status.phase === "connected") {
      await finishConnected(status);
      return true;
    }
    if (status.phase === "error" || status.phase === "canceled") {
      generation++;
      stopTimers();
      return true;
    }
    return false;
  }

  async function runConnect(label, onboardingRevision) {
    if (disposed) return;
    const run = ++generation;
    cancelPromise = null;
    stopTimers();
    connectPct = 0;
    connectStartedAt = now();
    connect = {
      kind: providerKind,
      phase: "detecting",
      label: String(label ?? ""),
      onboardingRevision: positiveRevision(onboardingRevision),
    };
    anchorTo("detecting");
    startConnectTimer();
    render();

    try {
      const started = await service.connectStart(providerKind, connect.label, connect.onboardingRevision);
      if (await applyServiceStatus(run, started)) return;
      for (;;) {
        if (!ownsRun(run)) return;
        const status = await service.connectStatus(providerKind);
        if (await applyServiceStatus(run, status)) return;
        if (!await waitToPoll(run)) return;
      }
    } catch (error) {
      if (!ownsRun(run) || error?.name === "AbortError") return;
      generation++;
      stopTimers();
      connect = {
        ...connect,
        phase: "error",
        message: safeLogLine(error?.message) || "Kết nối thất bại",
      };
      render();
    }
  }

  function cancelWarningText(detail) {
    const message = safeLogLine(detail) || "Không thể xác nhận yêu cầu huỷ kết nối.";
    return safeLogLine(`${message} Tiếp tục theo dõi kết nối.`);
  }

  async function reconcileCanceledRun(run) {
    for (;;) {
      if (!ownsRun(run)) return;
      try {
        const status = await service.connectStatus(providerKind);
        if (await applyServiceStatus(run, status)) return;
      } catch (error) {
        if (!ownsRun(run)) return;
        connect = {
          ...connect,
          cancelWarning: cancelWarningText(error?.message),
        };
        render();
      }
      if (!await waitToPoll(run)) return;
    }
  }

  function startCancelReconciliation(canceledState, detail) {
    const run = ++generation;
    connect = {
      ...canceledState,
      cancelWarning: cancelWarningText(detail),
    };
    startConnectTimer();
    render();
    void reconcileCanceledRun(run).catch((error) => {
      if (!ownsRun(run)) return;
      connect = {
        ...connect,
        cancelWarning: cancelWarningText(error?.message),
      };
      try {
        render();
      } catch {
        // The reconciliation task is fire-and-forget; never leak a rejected promise to the page.
      }
    });
  }

  function cancelConnect() {
    if (disposed) return Promise.resolve(false);
    if (!connect || connect.phase === "prompt" || TERMINAL_PHASES.has(connect.phase)) {
      dismiss();
      return Promise.resolve(true);
    }
    if (cancelPromise) return cancelPromise;

    const canceledState = connect;
    const cancellation = ++generation;
    stopTimers();
    cancelPromise = (async () => {
      try {
        const result = normalizeServiceSnapshot(
          await service.connectCancel(providerKind),
          "cancel",
        );
        if (disposed || generation !== cancellation) return false;
        cancelPromise = null;
        if (result?.ok === true) {
          connect = { ...canceledState, phase: "canceled" };
          render();
          return true;
        }
        startCancelReconciliation(
          canceledState,
          result?.message || result?.error || "Không thể huỷ kết nối.",
        );
        return false;
      } catch (error) {
        if (disposed || generation !== cancellation) return false;
        cancelPromise = null;
        startCancelReconciliation(canceledState, error?.message || "Không thể huỷ kết nối.");
        return false;
      }
    })();
    return cancelPromise;
  }

  function mount(nextContainer) {
    if (!nextContainer || typeof nextContainer.replaceChildren !== "function") {
      throw new TypeError("Provider Connect mount requires a container");
    }
    if (disposed) return controller;
    if (container && container !== nextContainer) container.replaceChildren();
    container = nextContainer;
    render();
    return controller;
  }

  function start({ label = "", onboardingRevision = 0 } = {}) {
    if (disposed) return false;
    generation++;
    cancelPromise = null;
    stopTimers();
    connect = {
      kind: providerKind,
      phase: "prompt",
      label: String(label ?? ""),
      onboardingRevision: positiveRevision(onboardingRevision),
    };
    render();
    return true;
  }

  function dispose() {
    if (disposed) return;
    disposed = true;
    generation++;
    cancelPromise = null;
    stopTimers();
    clearListeners();
    connect = null;
    container?.replaceChildren();
    container = null;
  }

  const controller = Object.freeze({ mount, start, cancel: cancelConnect, dispose });
  return controller;
}
