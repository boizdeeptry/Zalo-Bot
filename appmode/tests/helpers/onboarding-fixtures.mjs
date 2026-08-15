const hasOwn = (value, key) => Object.hasOwn(value, key);

export function onboardingProviderOptions() {
  return [
    {
      kind: "codex",
      display_name: "Codex",
      description: "Kết nối tài khoản ChatGPT/Codex trên máy này",
      recommended: true,
      beta: false,
      advertised: true,
      route_rank: 10,
    },
    {
      kind: "claude-code",
      display_name: "Claude Code",
      description: "Kết nối tài khoản Claude Code trên máy này",
      recommended: false,
      beta: false,
      advertised: true,
      route_rank: 100,
    },
  ];
}

export function pendingProvider(kind, position) {
  return {
    kind, status: "pending", position,
    provider_id: "", account_id: "", model_id: "",
  };
}

export function readyProvider(kind, position, overrides = {}) {
  return {
    kind,
    status: "ready",
    position,
    provider_id: kind,
    account_id: `${kind}-account`,
    model_id: `${kind}-model`,
    ...overrides,
  };
}

function defaultProviders(phase, overrides) {
  const kind = hasOwn(overrides, "provider_kind") ? overrides.provider_kind : "codex";
  if (phase === "connect" || phase === "setup") return [pendingProvider(kind, 0)];
  if (phase === "persona" || phase === "test") {
    return [readyProvider(kind, 0, {
      provider_id: hasOwn(overrides, "provider_id") ? overrides.provider_id : kind,
      account_id: hasOwn(overrides, "account_id") ? overrides.account_id : "account-1",
      model_id: hasOwn(overrides, "model_id") ? overrides.model_id : "gpt-5.6-terra",
    })];
  }
  return [];
}

export function onboardingStatus(defaultPhase = "test", overrides = {}) {
  const phase = hasOwn(overrides, "phase") ? overrides.phase : defaultPhase;
  const currentVersion = hasOwn(overrides, "current_version")
    ? overrides.current_version
    : 1;
  const restartInProgress = hasOwn(overrides, "restart_in_progress")
    ? overrides.restart_in_progress
    : false;
  const completedVersion = hasOwn(overrides, "completed_version")
    ? overrides.completed_version
    : (phase === "completed" || restartInProgress ? currentVersion : 0);
  const required = hasOwn(overrides, "required")
    ? overrides.required
    : completedVersion < currentVersion || restartInProgress;
  const providers = hasOwn(overrides, "providers")
    ? overrides.providers
    : defaultProviders(phase, overrides);
  const first = Array.isArray(providers) ? providers[0] : null;
  const identity = phase === "connect" ? {
    provider_kind: first?.kind ?? "codex", provider_id: "", account_id: "", model_id: "",
  } : phase === "setup" ? {
    provider_kind: first?.kind ?? "codex",
    provider_id: first?.kind ?? "codex",
    account_id: "account-1",
    model_id: "",
  } : (phase === "persona" || phase === "test") ? {
    provider_kind: first?.kind ?? "",
    provider_id: first?.provider_id ?? "",
    account_id: first?.account_id ?? "",
    model_id: first?.model_id ?? "",
  } : {
    provider_kind: "", provider_id: "", account_id: "", model_id: "",
  };
  return {
    required,
    current_version: currentVersion,
    completed_version: completedVersion,
    restart_in_progress: restartInProgress,
    phase,
    suggested_provider_kind: "",
    ...identity,
    revision: 7,
    providers,
    provider_options: hasOwn(overrides, "provider_options")
      ? overrides.provider_options
      : onboardingProviderOptions(),
    ...overrides,
  };
}
