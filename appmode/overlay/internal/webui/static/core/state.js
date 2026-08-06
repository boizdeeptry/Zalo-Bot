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
