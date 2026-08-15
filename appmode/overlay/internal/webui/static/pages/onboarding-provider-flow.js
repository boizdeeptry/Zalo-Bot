import {
  OnboardingProviderResponseError,
  canonicalProviderKinds,
  normalizeStatus,
  projectStatus,
  successorRevision,
} from "./onboarding-contract.js";

const LIFECYCLE_FIELDS = Object.freeze([
  "required", "current_version", "completed_version", "restart_in_progress",
]);
const ACTIVE_FIELDS = Object.freeze([
  "phase", "provider_kind", "provider_id", "account_id", "model_id", "revision",
]);
const OPTION_FIELDS = Object.freeze([
  "kind", "display_name", "description", "recommended", "beta", "advertised", "route_rank",
]);
const STAGE_FIELDS = Object.freeze([
  "kind", "status", "provider_id", "account_id", "model_id", "position",
]);

function arraysEqual(left, right, fields) {
  return left.length === right.length && left.every((entry, index) => {
    const candidate = right[index];
    return fields.every((field) => entry[field] === candidate[field]);
  });
}

function fieldsEqual(left, right, fields) {
  return fields.every((field) => left[field] === right[field]);
}

function sameCatalog(left, right) {
  return arraysEqual(left.provider_options, right.provider_options, OPTION_FIELDS);
}

function sameStages(left, right) {
  return arraysEqual(left.providers, right.providers, STAGE_FIELDS);
}

function sameLifecycle(left, right) {
  return fieldsEqual(left, right, LIFECYCLE_FIELDS);
}

function sameMutationState(left, right) {
  return fieldsEqual(left, right, [...LIFECYCLE_FIELDS, ...ACTIVE_FIELDS])
    && sameCatalog(left, right) && sameStages(left, right);
}

function normalizedState(value, normalize) {
  let result;
  try {
    result = normalize ? normalize(value) : normalizeStatus(projectStatus(value));
  } catch {
    throw new OnboardingProviderResponseError();
  }
  if (!result) throw new OnboardingProviderResponseError();
  return result;
}

function isAborted(error, signal) {
  return signal?.aborted === true || error?.name === "AbortError";
}

function throwIfAborted(signal) {
  if (!signal?.aborted) return;
  if (typeof signal.throwIfAborted === "function") signal.throwIfAborted();
  throw Object.assign(new Error("Aborted"), { name: "AbortError" });
}

function isMalformedSuccess(error, predicate) {
  if (error instanceof OnboardingProviderResponseError) return true;
  if (typeof predicate === "function" && predicate(error)) return true;
  return error?.code === "INVALID_RESPONSE"
    && Number.isInteger(error?.status) && error.status >= 200 && error.status < 300;
}

function validateDependencies(input, mutationName) {
  if (!input || typeof input[mutationName] !== "function" || typeof input.loadStatus !== "function") {
    throw new TypeError("Onboarding provider flow dependencies are invalid");
  }
}

function canonicalSelection(state, selectedKinds) {
  const canonical = canonicalProviderKinds(selectedKinds, state.provider_options);
  const selected = new Set(canonical);
  for (const stage of state.providers) {
    if (stage.status === "ready" && !selected.has(stage.kind)) {
      throw new RangeError("Ready onboarding providers must remain selected");
    }
  }
  return canonical;
}

function selectedStagesMatch(before, after, kinds) {
  if (after.providers.length !== kinds.length) return false;
  const previous = new Map(before.providers.map((stage) => [stage.kind, stage]));
  return after.providers.every((stage, position) => {
    if (stage.kind !== kinds[position] || stage.position !== position) return false;
    const old = previous.get(stage.kind);
    if (!old) return stage.status === "pending";
    if (old.status === "pending") return stage.status === "pending";
    return stage.status === "ready"
      && stage.provider_id === old.provider_id
      && stage.account_id === old.account_id
      && stage.model_id === old.model_id;
  });
}

export function isExactSelectionSuccessor(beforeValue, afterValue, selectedKinds) {
  const before = normalizeStatus(projectStatus(beforeValue));
  const after = normalizeStatus(projectStatus(afterValue));
  if (!before || !after || before.phase !== "provider"
    || after.revision !== successorRevision(before.revision)
    || !sameLifecycle(before, after) || !sameCatalog(before, after)) return false;
  let canonical;
  try {
    canonical = canonicalSelection(before, selectedKinds);
  } catch {
    return false;
  }
  if (!selectedStagesMatch(before, after, canonical)) return false;
  const allReady = after.providers.length > 0
    && after.providers.every(({ status }) => status === "ready");
  return after.phase === (allReady ? "persona" : "provider");
}

export function isExactBeginSuccessor(beforeValue, afterValue, kind) {
  const before = normalizeStatus(projectStatus(beforeValue));
  const after = normalizeStatus(projectStatus(afterValue));
  const target = before?.providers.find((stage) => stage.kind === kind);
  return Boolean(before && after && before.phase === "provider"
    && target?.status === "pending"
    && after.phase === "connect" && after.provider_kind === kind
    && after.revision === successorRevision(before.revision)
    && sameLifecycle(before, after) && sameCatalog(before, after) && sameStages(before, after));
}

function directSelectionResult(before, after, selectedKinds) {
  if (isExactSelectionSuccessor(before, after, selectedKinds)) return true;
  let canonical;
  try {
    canonical = canonicalSelection(before, selectedKinds);
  } catch {
    return false;
  }
  return sameMutationState(before, after)
    && after.phase === "provider"
    && after.providers.length === canonical.length
    && after.providers.every((stage, index) => stage.kind === canonical[index]);
}

export async function persistProviderSetWithReconciliation(input) {
  validateDependencies(input, "updateProviders");
  const before = normalizedState(input.state, input.normalize);
  if (before.phase !== "provider") throw new RangeError("Onboarding provider phase is invalid");
  if (!successorRevision(before.revision)) {
    throw new RangeError("Onboarding revision has no safe successor");
  }
  const selectedKinds = canonicalSelection(before, input.selectedKinds);
  throwIfAborted(input.signal);
  try {
    const response = await input.updateProviders(before.revision, [...selectedKinds], input.signal);
    throwIfAborted(input.signal);
    const after = normalizedState(response, input.normalize);
    if (!directSelectionResult(before, after, selectedKinds)) {
      throw new OnboardingProviderResponseError();
    }
    return { kind: "updated", state: after };
  } catch (error) {
    if (isMalformedSuccess(error, input.isMalformedSuccess) || isAborted(error, input.signal)) throw error;
    const response = await input.loadStatus(input.signal);
    throwIfAborted(input.signal);
    const after = normalizedState(response, input.normalize);
    return isExactSelectionSuccessor(before, after, selectedKinds)
      ? { kind: "updated", state: after }
      : { kind: "moved", state: after };
  }
}

export async function beginProviderInstallWithReconciliation(input) {
  validateDependencies(input, "beginProvider");
  const before = normalizedState(input.state, input.normalize);
  if (before.phase !== "provider") throw new RangeError("Onboarding provider phase is invalid");
  if (!successorRevision(before.revision)) {
    throw new RangeError("Onboarding revision has no safe successor");
  }
  const [kind] = canonicalProviderKinds([input.kind], before.provider_options);
  const stage = before.providers.find((candidate) => candidate.kind === kind);
  if (stage?.status !== "pending") throw new RangeError("Onboarding provider is not pending");
  throwIfAborted(input.signal);
  try {
    const response = await input.beginProvider(before.revision, kind, input.signal);
    throwIfAborted(input.signal);
    const after = normalizedState(response, input.normalize);
    if (!isExactBeginSuccessor(before, after, kind)) {
      throw new OnboardingProviderResponseError();
    }
    return { kind: "started", state: after };
  } catch (error) {
    if (isMalformedSuccess(error, input.isMalformedSuccess) || isAborted(error, input.signal)) throw error;
    const response = await input.loadStatus(input.signal);
    throwIfAborted(input.signal);
    const after = normalizedState(response, input.normalize);
    return isExactBeginSuccessor(before, after, kind)
      ? { kind: "started", state: after }
      : { kind: "moved", state: after };
  }
}
