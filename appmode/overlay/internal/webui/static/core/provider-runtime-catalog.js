export const PROVIDER_CATALOG_ERROR = "Không thể đọc danh mục Provider.";

const MAX_KIND_BYTES = 64;
const MAX_DISPLAY_BYTES = 128;
const MAX_DESCRIPTION_BYTES = 1024;
const MAX_PREFIX_BYTES = 8;
const MAX_UI_ORDER = 2_147_483_647;
const MAX_OPTIONS = 64;
const MAX_PROVIDERS = 1024;
const MAX_MODELS = 4096;
const MAX_ACCOUNTS = 1024;
const GROUPS = new Set(["subscription", "apikey"]);
const CONNECTION_MODES = new Set(["account", "credential", "none"]);
const EXECUTION_MODES = new Set(["api", "proxy", "official_cli", "local"]);
const UNSAFE_TEXT = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/u;
const encoder = new TextEncoder();

const invalid = () => { throw new TypeError("invalid provider catalog"); };
const isObject = (value) => value !== null
  && typeof value === "object"
  && !Array.isArray(value);
const byteLength = (value) => encoder.encode(value).length;

function hasUnpairedSurrogate(value) {
  for (let index = 0; index < value.length; index++) {
    const unit = value.charCodeAt(index);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(index + 1);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return true;
      index++;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      return true;
    }
  }
  return false;
}

function safeText(value, maxBytes, { empty = false } = {}) {
  if (typeof value !== "string") invalid();
  if ((!empty && value.length === 0)
    || value.trim() !== value
    || byteLength(value) > maxBytes
    || UNSAFE_TEXT.test(value)
    || hasUnpairedSurrogate(value)) {
    invalid();
  }
  return value;
}

function safeKind(value) {
  const kind = safeText(value, MAX_KIND_BYTES);
  if (!/^[a-z](?:[a-z0-9]|-(?=[a-z0-9]))*$/.test(kind)) invalid();
  return kind;
}

function safeBoolean(value) {
  if (typeof value !== "boolean") invalid();
  return value;
}

function safeArray(value, maximum) {
  if (!Array.isArray(value) || value.length > maximum) invalid();
  return value;
}

function normalizeOption(value) {
  if (!isObject(value)) invalid();
  const kind = safeKind(value.kind);
  const displayName = safeText(value.display_name, MAX_DISPLAY_BYTES);
  const description = safeText(value.description, MAX_DESCRIPTION_BYTES);
  if (!GROUPS.has(value.group)
    || !CONNECTION_MODES.has(value.connection_mode)
    || !EXECUTION_MODES.has(value.execution_mode)) {
    invalid();
  }
  const connectable = safeBoolean(value.connectable);
  const visible = safeBoolean(value.visible);
  const beta = safeBoolean(value.beta);
  const prefix = safeText(value.prefix, MAX_PREFIX_BYTES);
  if (!/^[a-z0-9]{1,8}$/.test(prefix)) invalid();
  const themeColor = safeText(value.theme_color, 7);
  if (!/^#[0-9a-fA-F]{6}$/.test(themeColor)) invalid();
  if (!Number.isSafeInteger(value.ui_order)
    || value.ui_order <= 0
    || value.ui_order > MAX_UI_ORDER) {
    invalid();
  }

  switch (value.connection_mode) {
    case "account":
      if (!visible || !connectable || value.group !== "subscription") invalid();
      break;
    case "credential":
      if (!visible || connectable || value.group !== "apikey") invalid();
      break;
    case "none":
      if (visible || connectable) invalid();
      break;
    default:
      invalid();
  }

  return {
    kind,
    display_name: displayName,
    description,
    group: value.group,
    connectable,
    connection_mode: value.connection_mode,
    execution_mode: value.execution_mode,
    visible,
    prefix,
    theme_color: themeColor,
    beta,
    ui_order: value.ui_order,
  };
}

function normalizeModel(value) {
  if (!isObject(value)) invalid();
  return {
    model_id: safeText(value.model_id, 512),
    name: safeText(value.name, MAX_DISPLAY_BYTES, { empty: true }),
    source: safeText(value.source, 64),
    available: safeBoolean(value.available),
  };
}

function normalizeAccount(value) {
  if (!isObject(value)) invalid();
  return {
    id: safeText(value.id, 256),
    label: safeText(value.label, MAX_DISPLAY_BYTES),
    email: safeText(value.email ?? "", 320, { empty: true }),
    enabled: safeBoolean(value.enabled),
  };
}

function normalizeProvider(value, optionByKind) {
  if (!isObject(value)) invalid();
  const kind = safeKind(value.kind);
  const option = optionByKind.get(kind);
  if (!option || value.connection_mode !== option.connection_mode) invalid();
  const models = safeArray(value.models, MAX_MODELS).map(normalizeModel);
  const accounts = value.accounts === undefined
    ? []
    : safeArray(value.accounts, MAX_ACCOUNTS).map(normalizeAccount);
  return {
    id: safeText(value.id, 256),
    name: safeText(value.name, MAX_DISPLAY_BYTES),
    kind,
    endpoint: safeText(value.endpoint, 2048, { empty: true }),
    enabled: safeBoolean(value.enabled),
    system: safeBoolean(value.system),
    credential_configured: safeBoolean(value.credential_configured),
    credential_unreadable: safeBoolean(value.credential_unreadable),
    last_check_status: safeText(value.last_check_status, 64, { empty: true }),
    last_error: safeText(value.last_error, 2048, { empty: true }),
    last_checked_at: safeText(value.last_checked_at, 64, { empty: true }),
    models,
    accounts,
    connection_mode: option.connection_mode,
  };
}

function deepFreeze(value) {
  if (value && typeof value === "object" && !Object.isFrozen(value)) {
    for (const nested of Object.values(value)) deepFreeze(nested);
    Object.freeze(value);
  }
  return value;
}

function normalize(value) {
  if (!isObject(value)) invalid();
  const rawOptions = safeArray(value.provider_options, MAX_OPTIONS);
  if (rawOptions.length === 0) invalid();

  const options = [];
  const optionByKind = new Map();
  let previousOrder = 0;
  for (const rawOption of rawOptions) {
    const option = normalizeOption(rawOption);
    if (optionByKind.has(option.kind) || option.ui_order <= previousOrder) invalid();
    previousOrder = option.ui_order;
    optionByKind.set(option.kind, option);
    options.push(option);
  }

  const providers = safeArray(value.providers, MAX_PROVIDERS)
    .map((provider) => normalizeProvider(provider, optionByKind));
  let hasConnectedProvider = true;
  if (Object.hasOwn(value, "hasConnectedProvider")) {
    hasConnectedProvider = safeBoolean(value.hasConnectedProvider);
  }
  return deepFreeze({
    providers,
    provider_options: options,
    hasConnectedProvider,
  });
}

export function normalizeProviderRuntimeResponse(value) {
  try {
    return normalize(value);
  } catch {
    throw new Error(PROVIDER_CATALOG_ERROR);
  }
}
