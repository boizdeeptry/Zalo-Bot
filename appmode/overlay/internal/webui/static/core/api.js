const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

function isRawBody(value) {
  if (typeof value === "string") return true;
  const constructors = ["Blob", "FormData", "URLSearchParams", "ArrayBuffer"];
  return constructors.some((name) => {
    const Type = globalThis[name];
    return typeof Type === "function" && value instanceof Type;
  });
}

export class AppAPIError extends Error {
  constructor({ code, message, fields = {}, status }) {
    super(message);
    this.name = "AppAPIError";
    this.code = code;
    this.fields = fields;
    this.status = status;
  }
}

export function requestInit(method = "GET", body, options = {}) {
  const normalizedMethod = String(method).toUpperCase();
  const headers = new Headers(options.headers);
  const init = {
    method: normalizedMethod,
    credentials: "same-origin",
    headers,
  };

  if (options.signal) {
    init.signal = options.signal;
  }
  if (!SAFE_METHODS.has(normalizedMethod)) {
    headers.set("X-Agentdc-Portal", "1");
  }
  if (body !== undefined) {
    if (isRawBody(body)) {
      init.body = body;
    } else {
      headers.set("Content-Type", "application/json");
      init.body = JSON.stringify(body);
    }
  }

  return init;
}

async function responsePayload(response) {
  if (response.status === 204) return null;
  const contentType = response.headers.get("Content-Type") || "";
  if (contentType.includes("application/json")) {
    return response.json();
  }
  const text = await response.text();
  return text || null;
}

function apiErrorFrom(response, payload) {
  const detail = payload && typeof payload === "object" ? payload.error : null;
  if (detail && typeof detail === "object") {
    return new AppAPIError({
      code: detail.code || `HTTP_${response.status}`,
      message: detail.message || response.statusText || "Yêu cầu thất bại",
      fields: detail.fields || {},
      status: response.status,
    });
  }

  const message = typeof detail === "string"
    ? detail
    : (typeof payload === "string" && payload) || response.statusText || "Yêu cầu thất bại";
  return new AppAPIError({
    code: `HTTP_${response.status}`,
    message,
    status: response.status,
  });
}

export async function requestJSON(path, options = {}) {
  const {
    method = "GET",
    body,
    fetchImpl = globalThis.fetch,
    headers,
    signal,
  } = options;
  if (typeof fetchImpl !== "function") {
    throw new TypeError("Fetch API is unavailable");
  }

  const response = await fetchImpl(path, requestInit(method, body, { headers, signal }));
  const payload = await responsePayload(response);
  if (!response.ok) {
    throw apiErrorFrom(response, payload);
  }
  return payload;
}
