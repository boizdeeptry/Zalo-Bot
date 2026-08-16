const option = (
  kind,
  displayName,
  description,
  group,
  connectable,
  connectionMode,
  executionMode,
  visible,
  prefix,
  themeColor,
  uiOrder,
  beta = false,
) => ({
  kind,
  display_name: displayName,
  description,
  group,
  connectable,
  connection_mode: connectionMode,
  execution_mode: executionMode,
  visible,
  prefix,
  theme_color: themeColor,
  beta,
  ui_order: uiOrder,
});

export const PROVIDER_RUNTIME_OPTIONS = Object.freeze([
  option("codex", "Codex", "Kết nối tài khoản ChatGPT/Codex trên máy này",
    "subscription", true, "account", "proxy", true, "cx", "#0f7a63", 10),
  option("claude-code", "Claude Code", "Kết nối tài khoản Claude Code trên máy này",
    "subscription", true, "account", "official_cli", true, "cc", "#c8613b", 20),
  option("openai", "OpenAI", "Kết nối OpenAI bằng API key",
    "apikey", false, "credential", "api", true, "oa", "#10a37f", 30),
  option("anthropic", "Anthropic", "Kết nối Anthropic bằng API key",
    "apikey", false, "credential", "api", true, "an", "#c8613b", 40),
  option("gemini", "Google Gemini", "Kết nối Google Gemini bằng API key",
    "apikey", false, "credential", "api", true, "gm", "#3f6ff5", 50),
  option("openrouter", "OpenRouter", "Kết nối OpenRouter bằng API key",
    "apikey", false, "credential", "api", true, "or", "#5b5ef0", 60),
  option("gemini-cli", "Gemini CLI", "Runtime CLI tương thích cho định tuyến đã lưu",
    "subscription", false, "none", "official_cli", false, "gc", "#3f6ff5", 70),
]);

const modeFor = (kind) => PROVIDER_RUNTIME_OPTIONS
  .find((entry) => entry.kind === kind)?.connection_mode ?? "none";

const LEGACY_CREDENTIAL_KINDS = [
  { kind: "openai", label: "OpenAI", endpoint: "https://api.openai.com" },
  { kind: "anthropic", label: "Anthropic", endpoint: "https://api.anthropic.com" },
  { kind: "gemini", label: "Google Gemini", endpoint: "https://generativelanguage.googleapis.com" },
  { kind: "openrouter", label: "OpenRouter", endpoint: "https://openrouter.ai" },
];

export function providerBody(provider) {
  return {
    endpoint: "",
    last_check_status: "",
    last_error: "",
    last_checked_at: "",
    models: [],
    accounts: [],
    ...provider,
    connection_mode: provider.connection_mode ?? modeFor(provider.kind),
  };
}

export function providerRuntimeResponse(providers = [], overrides = {}) {
  return {
    providers: providers.map(providerBody),
    kinds: LEGACY_CREDENTIAL_KINDS.map((entry) => ({ ...entry })),
    provider_options: PROVIDER_RUNTIME_OPTIONS.map((entry) => ({ ...entry })),
    hasConnectedProvider: false,
    ...overrides,
  };
}

export const CLAUDE_ADDED = providerBody({
  id: "claude-code", name: "Claude Code", kind: "claude-code", system: true, enabled: true,
  credential_configured: false, credential_unreadable: false, last_check_status: "", last_error: "",
  models: [{ model_id: "sonnet", name: "Claude Sonnet", source: "manual", available: true }],
});

export const CODEX_CONNECTED = providerBody({
  id: "codex", name: "Codex", kind: "codex", system: false, enabled: true,
  credential_configured: false, credential_unreadable: false, last_check_status: "", last_error: "",
  models: [],
  accounts: [{ id: "a1", label: "Tài khoản 1", email: "", enabled: true }],
});

export const COMBO_PROVIDER_RESPONSE = providerRuntimeResponse([
  {
    id: "openai-1", name: "OpenAI chính", kind: "openai", enabled: true, system: false,
    credential_configured: true, credential_unreadable: false,
    models: [
      { model_id: "gpt-5", name: "GPT-5", source: "discovered", available: true },
      { model_id: "gpt-5-mini", name: "GPT-5 mini", source: "discovered", available: true },
    ],
  },
  {
    id: "gemini", name: "Gemini", kind: "gemini", enabled: true, system: false,
    credential_configured: true, credential_unreadable: false,
    models: [{ model_id: "gemini-2", name: "Gemini 2", source: "discovered", available: true }],
  },
  {
    id: "claude-code", name: "Claude Code", kind: "claude-code", enabled: true, system: true,
    credential_configured: false, credential_unreadable: false,
    models: [{ model_id: "haiku", name: "Haiku", source: "manual", available: true }],
  },
  {
    id: "anthropic-off", name: "Anthropic nghỉ", kind: "anthropic", enabled: false, system: false,
    credential_configured: false, credential_unreadable: false,
    models: [{ model_id: "claude-4", name: "Claude 4", source: "discovered", available: true }],
  },
], { hasConnectedProvider: true });
