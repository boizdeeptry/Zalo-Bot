import {
  normalizeStatus,
  normalizeStrictStatus,
  successorRevision,
} from "./onboarding-contract.js";

const LIFECYCLE_FIELDS = Object.freeze([
  "required", "current_version", "completed_version", "restart_in_progress",
]);
const ACTIVE_FIELDS = Object.freeze([
  "provider_kind", "suggested_provider_kind", "provider_id", "account_id", "model_id",
]);
const OPTION_FIELDS = Object.freeze([
  "kind", "display_name", "description", "recommended", "beta", "advertised", "route_rank",
]);
const STAGE_FIELDS = Object.freeze([
  "kind", "status", "provider_id", "account_id", "model_id", "position",
]);

function fieldsEqual(left, right, fields) {
  return fields.every((field) => left[field] === right[field]);
}

function collectionsEqual(left, right, fields) {
  return left.length === right.length && left.every((entry, index) => {
    const candidate = right[index];
    return fieldsEqual(entry, candidate, fields);
  });
}

function catalogEqual(left, right) {
  return collectionsEqual(left.provider_options, right.provider_options, OPTION_FIELDS);
}

function stagesEqual(left, right) {
  return collectionsEqual(left.providers, right.providers, STAGE_FIELDS);
}

function advancedRevision(revision, distance) {
  let result = revision;
  for (let index = 0; index < distance; index++) {
    result = successorRevision(result);
    if (!result) return 0;
  }
  return result;
}

function expectedCompletedRevision(snapshot) {
  if (snapshot.phase === "persona") return advancedRevision(snapshot.revision, 3);
  if (snapshot.phase === "test") return advancedRevision(snapshot.revision, 2);
  return snapshot.phase === "completed" ? snapshot.revision : 0;
}

function exactCompletedSuccessor(before, after) {
  return Boolean(after
    && after.phase === "completed"
    && after.revision === expectedCompletedRevision(before)
    && after.required === false
    && after.current_version === before.current_version
    && after.completed_version === before.current_version
    && after.restart_in_progress === false
    && after.suggested_provider_kind === before.suggested_provider_kind
    && catalogEqual(before, after));
}

function exactPartialSuccessor(before, after) {
  if (!after || after.phase !== "test"
    || !fieldsEqual(before, after, LIFECYCLE_FIELDS)
    || !fieldsEqual(before, after, ACTIVE_FIELDS)
    || !catalogEqual(before, after)
    || !stagesEqual(before, after)) return false;
  const allowed = before.phase === "persona"
    ? [advancedRevision(before.revision, 1), advancedRevision(before.revision, 2)]
    : before.phase === "test" ? [advancedRevision(before.revision, 1)] : [];
  return allowed.includes(after.revision);
}

function failed(state) {
  return { kind: "failed", state };
}

function ambiguousFailure(error) {
  return !Number.isInteger(error?.status);
}

async function reconcileBootstrap(before, signal, loadStatus) {
  let raw;
  try {
    raw = await loadStatus(signal?.aborted ? undefined : signal);
  } catch {
    return failed(before);
  }
  if (signal?.aborted) return failed(before);
  const after = normalizeStrictStatus(raw);
  if (exactCompletedSuccessor(before, after)) return { kind: "completed", state: after };
  if (exactPartialSuccessor(before, after)) return { kind: "partial", state: after };
  return failed(before);
}

export async function runOnboardingBootstrap({
  snapshot,
  signal,
  bootstrap,
  loadStatus,
}) {
  if (typeof bootstrap !== "function" || typeof loadStatus !== "function") {
    throw new TypeError("Onboarding bootstrap dependencies are invalid");
  }
  const before = normalizeStatus(snapshot);
  if (!before) return failed(snapshot);
  if (before.phase === "completed") return { kind: "completed", state: before };
  if (!["persona", "test"].includes(before.phase)
    || !expectedCompletedRevision(before)
    || signal?.aborted) return failed(before);

  let raw;
  try {
    raw = await bootstrap(before.revision, signal);
  } catch (error) {
    return ambiguousFailure(error)
      ? reconcileBootstrap(before, signal, loadStatus)
      : failed(before);
  }
  const after = normalizeStrictStatus(raw);
  if (!after) return failed(before);
  if (signal?.aborted) return reconcileBootstrap(before, signal, loadStatus);
  return exactCompletedSuccessor(before, after)
    ? { kind: "completed", state: after }
    : failed(before);
}
