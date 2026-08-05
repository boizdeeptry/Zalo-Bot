export function createStore(initialState = {}) {
  let state = Object.freeze({ ...initialState });
  const subscribers = new Set();

  return Object.freeze({
    getSnapshot() {
      return state;
    },

    set(update) {
      const patch = typeof update === "function" ? update(state) : update;
      if (!patch || typeof patch !== "object") {
        throw new TypeError("State update must return an object");
      }
      state = Object.freeze({ ...state, ...patch });
      for (const subscriber of subscribers) {
        subscriber(state);
      }
      return state;
    },

    subscribe(subscriber) {
      if (typeof subscriber !== "function") {
        throw new TypeError("State subscriber must be a function");
      }
      subscribers.add(subscriber);
      return () => subscribers.delete(subscriber);
    },
  });
}

function waitForDelay(delayMs, signal) {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("The operation was aborted", "AbortError"));
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, delayMs);
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DOMException("The operation was aborted", "AbortError"));
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

// A restart is complete only after the old daemon has gone down and a daemon is
// reachable again. Seeing one successful probe immediately after save is still
// the old process and must never trigger a reload.
export async function waitForServerRestart({
  probe,
  wait = waitForDelay,
  onPhase = () => {},
  reload = () => globalThis.location?.reload(),
  signal,
  intervalMs = 700,
  maxAttempts = 40,
} = {}) {
  if (typeof probe !== "function") {
    throw new TypeError("Restart polling requires a probe function");
  }

  let sawDown = false;
  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    await wait(intervalMs, signal);
    if (signal?.aborted) return false;

    let up = false;
    try {
      up = Boolean(await probe({ signal }));
    } catch (error) {
      if (signal?.aborted || error?.name === "AbortError") return false;
    }

    if (!sawDown) {
      if (up) {
        onPhase("stopping");
      } else {
        sawDown = true;
        onPhase("starting");
      }
      continue;
    }
    if (!up) {
      onPhase("starting");
      continue;
    }

    onPhase("ready");
    reload();
    return true;
  }

  onPhase("timeout");
  return false;
}
