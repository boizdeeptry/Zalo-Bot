const hasOwn = (value, key) => Object.hasOwn(value, key);

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
  const identity = phase === "provider" ? {
    provider_kind: "", provider_id: "", account_id: "", model_id: "",
  } : phase === "connect" ? {
    provider_kind: "codex", provider_id: "", account_id: "", model_id: "",
  } : phase === "setup" ? {
    provider_kind: "codex", provider_id: "codex", account_id: "account-1", model_id: "",
  } : {
    provider_kind: "codex", provider_id: "codex",
    account_id: "account-1", model_id: "gpt-5.6-terra",
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
    ...overrides,
  };
}
