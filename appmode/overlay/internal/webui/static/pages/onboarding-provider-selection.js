import {
  OnboardingProviderResponseError,
  normalizeProviderSelection,
  normalizeStatus,
} from "./onboarding-contract.js";

const STATUS_IDENTITY = Object.freeze([
  "required", "current_version", "completed_version", "restart_in_progress",
  "phase", "provider_kind", "suggested_provider_kind", "provider_id",
  "account_id", "model_id", "revision",
]);

function sameStatus(left, right) {
  return Boolean(left && right)
    && STATUS_IDENTITY.every((field) => left[field] === right[field]);
}

function aborted(error, signal) {
  return signal?.aborted === true || error?.name === "AbortError";
}

export async function selectProviderWithReconciliation({
  service, snapshot, providerKind, signal,
}) {
  const expected = { providerKind, revision: snapshot.revision };
  const original = normalizeStatus(snapshot);
  let response;
  try {
    response = await service.selectProvider(providerKind, snapshot.revision, signal);
  } catch (error) {
    if (error instanceof OnboardingProviderResponseError) return { kind: "invalid" };
    if (aborted(error, signal)) throw error;
    try {
      response = await service.status(signal);
    } catch (statusError) {
      if (aborted(statusError, signal)) throw statusError;
      return { kind: "unknown" };
    }
    const state = normalizeStatus(response);
    if (!state) return { kind: "invalid" };
    const selected = normalizeProviderSelection(state, expected);
    if (selected) return { kind: "selected", state: selected };
    if (sameStatus(state, original)) return { kind: "retry", state };
    return { kind: "moved", state };
  }
  const state = normalizeProviderSelection(response, expected);
  return state ? { kind: "selected", state } : { kind: "invalid" };
}
