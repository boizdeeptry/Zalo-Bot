import { AppAPIError, requestJSON as sharedRequestJSON } from "../core/api.js";
import { validatePersonaValue } from "../components/persona-fields.js";

// Compatibility only for the pre-multi-provider renderer. New onboarding state
// is authorized exclusively by the advertised catalog in each status snapshot.
export const SUPPORTED_PROVIDERS = new Set(["codex", "claude-code"]);
export const SUPPORTED_PHASES = new Set([
  "provider", "connect", "setup", "persona", "test", "completed",
]);
export const SAFE_ERROR = "Không thể tiếp tục thiết lập. Trạng thái chưa hợp lệ hoặc đã thay đổi.";
export const SAFE_SETUP_ERROR = "Chưa thể chuẩn bị cấu hình. Vui lòng thử lại.";

export class OnboardingProviderResponseError extends TypeError {
  constructor() {
    super("Invalid onboarding provider response");
    this.name = "OnboardingProviderResponseError";
  }
}

const STATUS_FIELDS = Object.freeze([
  "required", "current_version", "completed_version", "phase", "provider_kind",
  "suggested_provider_kind", "provider_id", "account_id", "model_id",
  "restart_in_progress", "revision", "providers", "provider_options",
]);
const PROVIDER_OPTION_FIELDS = Object.freeze([
  "kind", "display_name", "description", "recommended", "beta", "advertised", "route_rank",
]);
const PROVIDER_STAGE_FIELDS = Object.freeze([
  "kind", "status", "provider_id", "account_id", "model_id", "position",
]);
const LIFECYCLE_FIELDS = Object.freeze([
  "required", "current_version", "completed_version", "restart_in_progress",
]);
const IDENTIFIER_LIMIT = 1_000;
const OPAQUE_VALUE_LIMIT = 4_096;
const AGENT_SAMPLE_LIMIT = 100_000;
const MAX_PROVIDER_OPTIONS = 64;
const MAX_PROVIDER_STAGES = 8;
const CONTROL_CHARACTERS = /[\u0000-\u001f\u007f-\u009f]/;

export const isRecord = (value) => Boolean(value)
  && typeof value === "object" && !Array.isArray(value);
export const positiveRevision = (value) => Number.isSafeInteger(value) && value > 0 ? value : 0;
export const successorRevision = (value) => positiveRevision(value)
  && value < Number.MAX_SAFE_INTEGER ? value + 1 : 0;

export function semanticString(value, { allowEmpty = false, limit = IDENTIFIER_LIMIT } = {}) {
  if (typeof value !== "string"
    || value.length > limit
    || value.trim() !== value
    || CONTROL_CHARACTERS.test(value)
    || (!allowEmpty && !value)) return null;
  return value;
}

function optionalSemanticString(record, field) {
  if (!Object.hasOwn(record, field)) return "";
  return semanticString(record[field], { allowEmpty: true });
}

export function projectStatus(value) {
  if (!isRecord(value)) return value;
  const projected = {};
  for (const field of STATUS_FIELDS) {
    if (!Object.hasOwn(value, field)) continue;
    if (field === "provider_options") {
      projected[field] = projectCollection(value[field], PROVIDER_OPTION_FIELDS);
    } else if (field === "providers") {
      projected[field] = projectCollection(value[field], PROVIDER_STAGE_FIELDS);
    } else {
      projected[field] = value[field];
    }
  }
  return Object.freeze(projected);
}

function projectCollection(value, fields) {
  if (!Array.isArray(value)) return value;
  return Object.freeze(value.map((entry) => {
    if (!isRecord(entry)) return entry;
    const result = {};
    for (const field of fields) {
      if (Object.hasOwn(entry, field)) result[field] = entry[field];
    }
    return Object.freeze(result);
  }));
}

function invalidProviderOptions() {
  return new TypeError("Invalid provider options");
}

function invalidProviderStages() {
  return new TypeError("Invalid providers");
}

export function normalizeProviderOptions(value) {
  if (!Array.isArray(value) || value.length === 0 || value.length > MAX_PROVIDER_OPTIONS) {
    throw invalidProviderOptions();
  }
  const kinds = new Set();
  const ranks = new Set();
  let previousRank = -1;
  const options = value.map((raw) => {
    if (!isRecord(raw)) throw invalidProviderOptions();
    const kind = semanticString(raw.kind);
    const displayName = semanticString(raw.display_name);
    const description = semanticString(raw.description);
    const rank = raw.route_rank;
    if (!kind || !displayName || !description
      || typeof raw.recommended !== "boolean" || typeof raw.beta !== "boolean"
      || raw.advertised !== true || !Number.isSafeInteger(rank) || rank < 0
      || rank <= previousRank || kinds.has(kind) || ranks.has(rank)) {
      throw invalidProviderOptions();
    }
    kinds.add(kind);
    ranks.add(rank);
    previousRank = rank;
    return Object.freeze({
      kind,
      display_name: displayName,
      description,
      recommended: raw.recommended,
      beta: raw.beta,
      advertised: true,
      route_rank: rank,
    });
  });
  return Object.freeze(options);
}

export function normalizeProviderStages(value, providerOptions) {
  if (!Array.isArray(value) || value.length > MAX_PROVIDER_STAGES
    || !Array.isArray(providerOptions) || providerOptions.length === 0) {
    throw invalidProviderStages();
  }
  const optionIndex = new Map(providerOptions.map(({ kind }, index) => [kind, index]));
  const seen = new Set();
  let previousOptionIndex = -1;
  const stages = value.map((raw, position) => {
    if (!isRecord(raw)) throw invalidProviderStages();
    const kind = semanticString(raw.kind);
    const providerID = semanticString(raw.provider_id, { allowEmpty: true });
    const accountID = semanticString(raw.account_id, { allowEmpty: true });
    const modelID = semanticString(raw.model_id, { allowEmpty: true, limit: OPAQUE_VALUE_LIMIT });
    const catalogIndex = optionIndex.get(kind);
    if (!kind || catalogIndex === undefined || seen.has(kind)
      || raw.position !== position || catalogIndex <= previousOptionIndex
      || providerID === null || accountID === null || modelID === null) {
      throw invalidProviderStages();
    }
    if (raw.status === "pending") {
      if (providerID || accountID || modelID) throw invalidProviderStages();
    } else if (raw.status === "ready") {
      if (providerID !== kind || !accountID || !modelID) throw invalidProviderStages();
    } else {
      throw invalidProviderStages();
    }
    seen.add(kind);
    previousOptionIndex = catalogIndex;
    return Object.freeze({
      kind,
      status: raw.status,
      provider_id: providerID,
      account_id: accountID,
      model_id: modelID,
      position,
    });
  });
  return Object.freeze(stages);
}

function requireProviderKind(kind) {
  if (!SUPPORTED_PROVIDERS.has(kind)) {
    throw new RangeError("Onboarding provider kind is not supported");
  }
  return kind;
}

function requireDynamicProviderKind(kind) {
  const valid = semanticString(kind);
  if (!valid) throw new RangeError("Onboarding provider kind is invalid");
  return valid;
}

export function canonicalProviderKinds(selectedKinds, providerOptions) {
  if (!Array.isArray(selectedKinds) || selectedKinds.length > MAX_PROVIDER_STAGES
    || !Array.isArray(providerOptions)) {
    throw new RangeError("Onboarding provider selection is invalid");
  }
  const rank = new Map(providerOptions.map((option, index) => [option.kind, index]));
  const seen = new Set();
  const result = selectedKinds.map((kind) => {
    const valid = semanticString(kind);
    if (!valid || !rank.has(valid) || seen.has(valid)) {
      throw new RangeError("Onboarding provider selection is invalid");
    }
    seen.add(valid);
    return valid;
  });
  result.sort((left, right) => rank.get(left) - rank.get(right));
  return Object.freeze(result);
}

function requireRevision(revision) {
  const valid = positiveRevision(revision);
  if (!valid) throw new RangeError("Onboarding revision must be a positive safe integer");
  return valid;
}

function requireAccountID(accountId) {
  const valid = semanticString(accountId);
  if (!valid) throw new TypeError("Onboarding account id is invalid");
  return valid;
}

function requireSuccessor(revision) {
  const successor = successorRevision(revision);
  if (!successor) throw new RangeError("Onboarding revision has no safe successor");
  return successor;
}

function mapMalformedSuccess(error) {
  if (error instanceof AppAPIError && error.code === "INVALID_RESPONSE"
    && Number.isInteger(error.status) && error.status >= 200 && error.status < 300) {
    throw new OnboardingProviderResponseError();
  }
  throw error;
}

function authoritativeStatus(response) {
  const normalized = normalizeStatus(projectStatus(response));
  if (!normalized) throw new OnboardingProviderResponseError();
  return normalized;
}

function sameKinds(stages, kinds) {
  return stages.length === kinds.length && stages.every((stage, index) => stage.kind === kinds[index]);
}

function validSetupPhase(normalized) {
  const allReady = normalized.providers.length > 0
    && normalized.providers.every(({ status }) => status === "ready");
  return normalized.phase === "persona"
    ? allReady
    : normalized.phase === "provider" && normalized.providers.some(({ status }) => status === "pending");
}

function validSetupSuccess(response, { kind, accountID, revision }) {
  const normalized = authoritativeStatus(response);
  const stage = normalized.providers.find((candidate) => candidate.kind === kind);
  if (normalized.revision !== successorRevision(revision)
    || !validSetupPhase(normalized)
    || stage?.status !== "ready" || stage.provider_id !== kind
    || stage.account_id !== accountID || !stage.model_id) {
    throw new OnboardingProviderResponseError();
  }
  return normalized;
}

export function normalizeSetupResponse(response, { accountID, revision, providerKind = "" }) {
  if (!isRecord(response)) return null;
  const kind = semanticString(response.provider_kind);
  const providerID = semanticString(response.provider_id);
  const returnedAccountID = semanticString(response.account_id);
  const modelID = semanticString(response.model_id);
  const stagedComboID = semanticString(response.staged_combo_id);
  const comboName = semanticString(response.combo_name);
  if (response.revision !== successorRevision(revision)
    || (Object.hasOwn(response, "phase") && response.phase !== "persona")
    || !SUPPORTED_PROVIDERS.has(kind)
    || (providerKind && kind !== providerKind)
    || providerID !== kind || returnedAccountID !== accountID
    || !modelID || !stagedComboID || !comboName) return null;
  return {
    phase: "persona", revision: response.revision, provider_kind: kind,
    provider_id: providerID, account_id: returnedAccountID, model_id: modelID,
    staged_combo_id: stagedComboID, combo_name: comboName,
  };
}

export function normalizeStatus(snapshot) {
  if (!isRecord(snapshot) || !SUPPORTED_PHASES.has(snapshot.phase)) return null;
  for (const field of STATUS_FIELDS) {
    if (!Object.hasOwn(snapshot, field)) return null;
  }
  let providerOptions;
  let providers;
  try {
    providerOptions = normalizeProviderOptions(snapshot.provider_options);
    providers = normalizeProviderStages(snapshot.providers, providerOptions);
  } catch {
    return null;
  }
  const advertisedKinds = new Set(providerOptions.map(({ kind }) => kind));
  const completed = snapshot.phase === "completed";
  const currentVersion = Number.isSafeInteger(snapshot.current_version) && snapshot.current_version > 0
    ? snapshot.current_version : 0;
  const completedVersion = Number.isSafeInteger(snapshot.completed_version)
    && snapshot.completed_version >= 0 ? snapshot.completed_version : -1;
  const restartInProgress = snapshot.restart_in_progress;
  if (!currentVersion || completedVersion < 0 || completedVersion > currentVersion
    || typeof restartInProgress !== "boolean" || typeof snapshot.required !== "boolean") return null;
  if (completed) {
    if (snapshot.required || restartInProgress || completedVersion !== currentVersion) return null;
  } else if (!snapshot.required
    || (restartInProgress ? completedVersion !== currentVersion : completedVersion >= currentVersion)) {
    return null;
  }
  const revision = positiveRevision(snapshot.revision);
  const providerKindValue = optionalSemanticString(snapshot, "provider_kind");
  const suggestionValue = optionalSemanticString(snapshot, "suggested_provider_kind");
  const providerID = optionalSemanticString(snapshot, "provider_id");
  const accountID = optionalSemanticString(snapshot, "account_id");
  const modelID = optionalSemanticString(snapshot, "model_id");
  if (!revision || [providerKindValue, suggestionValue, providerID, accountID, modelID].includes(null)) {
    return null;
  }
  if ((providerKindValue && !advertisedKinds.has(providerKindValue))
    || (suggestionValue && !advertisedKinds.has(suggestionValue))) return null;
  const providerKind = providerKindValue;
  const suggestedProviderKind = suggestionValue;
  const stageForKind = providers.find(({ kind }) => kind === providerKind);
  const activeClean = !providerKind && !providerID && !accountID && !modelID;
  if (snapshot.phase === "provider") {
    if (!activeClean) return null;
  } else if (snapshot.phase === "connect") {
    if (!stageForKind || stageForKind.status !== "pending"
      || providerID || accountID || modelID) return null;
  } else if (snapshot.phase === "setup") {
    if (!stageForKind || stageForKind.status !== "pending"
      || providerID !== providerKind || !accountID || modelID) return null;
  } else if (snapshot.phase === "persona" || snapshot.phase === "test") {
    const first = providers[0];
    if (!first || providers.some(({ status }) => status !== "ready")
      || providerKind !== first.kind || providerID !== first.provider_id
      || accountID !== first.account_id || modelID !== first.model_id) return null;
  } else if (!activeClean || providers.length !== 0) {
    return null;
  }
  return Object.freeze({
    required: snapshot.required,
    current_version: currentVersion,
    completed_version: completedVersion,
    restart_in_progress: restartInProgress,
    phase: snapshot.phase,
    provider_kind: providerKind,
    suggested_provider_kind: suggestedProviderKind,
    provider_id: providerID,
    account_id: accountID,
    model_id: modelID,
    revision,
    providers,
    provider_options: providerOptions,
  });
}

function hasExactFields(value, fields) {
  if (!isRecord(value)) return false;
  const keys = Object.keys(value);
  return keys.length === fields.length && fields.every((field) => Object.hasOwn(value, field));
}

export function normalizeStrictStatus(snapshot) {
  if (!hasExactFields(snapshot, STATUS_FIELDS)
    || !Array.isArray(snapshot.provider_options)
    || !snapshot.provider_options.every((entry) => hasExactFields(entry, PROVIDER_OPTION_FIELDS))
    || !Array.isArray(snapshot.providers)
    || !snapshot.providers.every((entry) => hasExactFields(entry, PROVIDER_STAGE_FIELDS))) return null;
  return normalizeStatus(snapshot);
}

export function normalizeProviderSelection(snapshot, { providerKind, revision }) {
  const normalized = normalizeStatus(snapshot);
  const selected = normalized?.providers.find(({ kind }) => kind === providerKind);
  if (!normalized || normalized.phase !== "connect"
    || normalized.provider_kind !== providerKind
    || normalized.revision !== successorRevision(revision)
    || selected?.status !== "pending"
    || normalized.provider_id || normalized.account_id || normalized.model_id) return null;
  return normalized;
}

export function normalizeConnectedSetup(snapshot, { providerKind, accountID, revision }) {
  const normalized = normalizeStatus(snapshot);
  if (!normalized || normalized.phase !== "setup"
    || normalized.provider_kind !== providerKind || normalized.provider_id !== providerKind
    || normalized.account_id !== accountID || normalized.model_id
    || normalized.revision !== successorRevision(revision)) return null;
  return normalized;
}

export function normalizeSetupResult(result, expected) {
  const standard = normalizeSetupStatus(result, expected);
  if (standard) return standard;
  const normalized = normalizeSetupResponse(result, {
    accountID: expected.accountID,
    revision: expected.revision,
    providerKind: expected.providerKind,
  });
  const lifecycle = normalizeStatus(expected.lifecycle);
  if (!normalized || normalized.provider_id !== expected.providerID
    || lifecycle?.phase !== "setup" || lifecycle.required !== true) return null;
  return {
    required: true,
    current_version: lifecycle.current_version,
    completed_version: lifecycle.completed_version,
    restart_in_progress: lifecycle.restart_in_progress,
    suggested_provider_kind: "",
    ...normalized,
  };
}

export function normalizeSetupStatus(result, expected) {
  const normalized = normalizeStatus(projectStatus(result));
  const lifecycle = normalizeStatus(expected.lifecycle);
  const stage = normalized?.providers.find(({ kind }) => kind === expected.providerKind);
  const sourceStage = lifecycle?.providers.find(({ kind }) => kind === expected.providerKind);
  const catalogStable = normalized && lifecycle
    && collectionsEqual(normalized.provider_options, lifecycle.provider_options, PROVIDER_OPTION_FIELDS);
  const lifecycleStable = normalized && lifecycle
    && LIFECYCLE_FIELDS.every((field) => normalized[field] === lifecycle[field]);
  const stagesStable = normalized && lifecycle
    && normalized.providers.length === lifecycle.providers.length
    && normalized.providers.every((candidate, index) => {
      const source = lifecycle.providers[index];
      if (candidate.kind !== source.kind || candidate.position !== source.position) return false;
      if (candidate.kind === expected.providerKind) return source.status === "pending";
      return PROVIDER_STAGE_FIELDS.every((field) => candidate[field] === source[field]);
    });
  const expectedPhase = normalized?.providers.every(({ status }) => status === "ready")
    ? "persona" : "provider";
  if (!normalized || !lifecycle || lifecycle.phase !== "setup"
    || lifecycle.provider_kind !== expected.providerKind
    || lifecycle.provider_id !== expected.providerID
    || lifecycle.account_id !== expected.accountID
    || normalized.revision !== successorRevision(expected.revision)
    || normalized.phase !== expectedPhase || !validSetupPhase(normalized)
    || !lifecycleStable || !catalogStable || !stagesStable
    || sourceStage?.status !== "pending"
    || stage?.status !== "ready" || stage.provider_id !== expected.providerID
    || stage.account_id !== expected.accountID || !stage.model_id) return null;
  return normalized;
}

function collectionsEqual(left, right, fields) {
  return left.length === right.length && left.every((entry, index) => {
    const candidate = right[index];
    return fields.every((field) => entry[field] === candidate[field]);
  });
}

export function normalizeAgentResponse(response) {
  if (!isRecord(response) || typeof response.ready !== "boolean"
    || typeof response.display_name !== "string" || response.display_name.length > IDENTIFIER_LIMIT
    || !Array.isArray(response.placeholders)) return null;
  const placeholders = [];
  for (const hole of response.placeholders) {
    if (!isRecord(hole)) return null;
    const key = semanticString(hole.key);
    const count = Number.isSafeInteger(hole.count) && hole.count > 0 ? hole.count : 0;
    const sample = Object.hasOwn(hole, "sample") ? hole.sample : "";
    if (!key || !count || typeof sample !== "string" || sample.length > AGENT_SAMPLE_LIMIT) return null;
    const projected = { key, count, sample };
    if (Object.hasOwn(hole, "value")) {
      if (typeof hole.value !== "string" || hole.value.length > IDENTIFIER_LIMIT) return null;
      projected.value = hole.value;
    }
    placeholders.push(projected);
  }
  return { ready: response.ready, display_name: response.display_name, placeholders };
}

export function validDisplayName(value) {
  const result = validatePersonaValue(value);
  return result.ok && result.value === value ? value : "";
}

export function terminalIdentity(terminal, expectedKind) {
  if (!isRecord(terminal)) return null;
  const kind = semanticString(terminal.kind);
  const providerID = semanticString(terminal.providerId);
  const accountID = semanticString(terminal.accountId);
  if (kind !== expectedKind || providerID !== expectedKind || !accountID) return null;
  return { kind, providerID, accountID };
}

export function validateOnboardingPageService(service) {
  for (const method of [
    "status", "bootstrapStatus", "updateProviders", "beginProvider", "setup", "backToProviders",
    "loadAgent", "bootstrap",
  ]) {
    if (typeof service?.[method] !== "function") {
      throw new TypeError(`Onboarding page service requires ${method}()`);
    }
  }
}

export function validateConnectController(controller) {
  for (const method of ["mount", "start", "cancel", "dispose"]) {
    if (typeof controller?.[method] !== "function") {
      throw new TypeError(`Provider Connect controller requires ${method}()`);
    }
  }
}

export function createOnboardingService({ requestJSON = sharedRequestJSON } = {}) {
  if (typeof requestJSON !== "function") {
    throw new TypeError("Onboarding service requires a requestJSON function");
  }
  const beginProvider = (kind, revision, signal, { legacy = false } = {}) => {
    if (!legacy && Number.isSafeInteger(kind)) [kind, revision] = [revision, kind];
    const providerKind = legacy ? requireProviderKind(kind) : requireDynamicProviderKind(kind);
    const currentRevision = requireRevision(revision);
    requireSuccessor(currentRevision);
    return Promise.resolve(requestJSON("/onboarding/provider", {
      method: "PUT", body: { kind: providerKind, revision: currentRevision }, signal,
    })).catch(mapMalformedSuccess).then((response) => {
      const normalized = normalizeProviderSelection(response, {
        providerKind, revision: currentRevision,
      });
      if (!normalized) throw new OnboardingProviderResponseError();
      return normalized;
    });
  };
  const rawStatus = (signal) => requestJSON("/onboarding/status", { signal });
  return Object.freeze({
    status(signal) {
      return Promise.resolve(rawStatus(signal)).then(projectStatus);
    },
    bootstrapStatus(signal) {
      return rawStatus(signal);
    },
    updateProviders(selectedKinds, revision, signal) {
      if (Number.isSafeInteger(selectedKinds)) {
        [selectedKinds, revision] = [revision, selectedKinds];
      }
      if (!Array.isArray(selectedKinds) || selectedKinds.length > MAX_PROVIDER_STAGES) {
        throw new RangeError("Onboarding provider selection is invalid");
      }
      const requestedKinds = [];
      const seen = new Set();
      for (const kind of selectedKinds) {
        const valid = requireDynamicProviderKind(kind);
        if (seen.has(valid)) throw new RangeError("Onboarding provider selection is invalid");
        seen.add(valid);
        requestedKinds.push(valid);
      }
      const currentRevision = requireRevision(revision);
      requireSuccessor(currentRevision);
      return Promise.resolve(requestJSON("/onboarding/providers", {
        method: "PUT",
        body: { revision: currentRevision, selected_kinds: [...requestedKinds] },
        signal,
      })).catch(mapMalformedSuccess).then((response) => {
        const normalized = authoritativeStatus(response);
        let canonical;
        try {
          canonical = canonicalProviderKinds(requestedKinds, normalized.provider_options);
        } catch {
          throw new OnboardingProviderResponseError();
        }
        const noOp = normalized.revision === currentRevision;
        const expectedPhase = !noOp && normalized.providers.length > 0
          && normalized.providers.every(({ status }) => status === "ready")
          ? "persona" : "provider";
        if (![currentRevision, successorRevision(currentRevision)].includes(normalized.revision)
          || normalized.phase !== expectedPhase || !sameKinds(normalized.providers, canonical)) {
          throw new OnboardingProviderResponseError();
        }
        return normalized;
      });
    },
    beginProvider(kind, revision, signal) {
      return beginProvider(kind, revision, signal);
    },
    selectProvider(kind, revision, signal) {
      return beginProvider(kind, revision, signal, { legacy: true });
    },
    setup(kindOrAccountID, accountOrRevision, revisionOrSignal, signal) {
      const modern = typeof accountOrRevision === "string";
      const providerKind = modern ? requireDynamicProviderKind(kindOrAccountID) : "";
      const exactAccountID = requireAccountID(modern ? accountOrRevision : kindOrAccountID);
      const currentRevision = requireRevision(modern ? revisionOrSignal : accountOrRevision);
      const requestSignal = modern ? signal : revisionOrSignal;
      requireSuccessor(currentRevision);
      return Promise.resolve(requestJSON("/onboarding/setup", {
        method: "POST",
        body: modern
          ? { kind: providerKind, account_id: exactAccountID, revision: currentRevision }
          : { account_id: exactAccountID, revision: currentRevision },
        signal: requestSignal,
      })).catch(mapMalformedSuccess).then((response) => {
        if (modern) {
          return validSetupSuccess(response, {
            kind: providerKind, accountID: exactAccountID, revision: currentRevision,
          });
        }
        const normalized = normalizeSetupResponse(response, {
          accountID: exactAccountID, revision: currentRevision,
        });
        if (!normalized) throw new TypeError("Invalid onboarding setup response");
        return normalized;
      });
    },
    backToProviders(revision, signal) {
      const currentRevision = requireRevision(revision);
      requireSuccessor(currentRevision);
      return Promise.resolve(requestJSON("/onboarding/back-to-providers", {
        method: "POST", body: { revision: currentRevision }, signal,
      })).catch(mapMalformedSuccess).then((response) => {
        const normalized = authoritativeStatus(response);
        if (normalized.phase !== "provider"
          || normalized.revision !== successorRevision(currentRevision)) {
          throw new OnboardingProviderResponseError();
        }
        return normalized;
      });
    },
    loadAgent({ signal } = {}) {
      return requestJSON("/agent", { signal });
    },
    bootstrap(revision, signal) {
      const currentRevision = requireRevision(revision);
      return requestJSON("/onboarding/bootstrap", {
        method: "POST", body: { revision: currentRevision }, signal,
      });
    },
  });
}
