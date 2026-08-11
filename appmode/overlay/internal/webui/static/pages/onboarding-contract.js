import { requestJSON as sharedRequestJSON } from "../core/api.js";
import { validatePersonaValue } from "../components/persona-fields.js";

export const SUPPORTED_PROVIDERS = new Set(["codex", "claude-code"]);
export const SUPPORTED_PHASES = new Set([
  "provider", "connect", "setup", "persona", "test", "completed",
]);
export const SAFE_ERROR = "Không thể tiếp tục thiết lập. Trạng thái chưa hợp lệ hoặc đã thay đổi.";
export const SAFE_SETUP_ERROR = "Chưa thể chuẩn bị cấu hình. Vui lòng thử lại.";

const STATUS_FIELDS = Object.freeze([
  "required", "current_version", "completed_version", "phase", "provider_kind",
  "suggested_provider_kind", "provider_id", "account_id", "model_id",
  "restart_in_progress", "revision",
]);
const IDENTIFIER_LIMIT = 1_000;
const OPAQUE_TOKEN_LIMIT = 4_096;
const ANSWER_LIMIT = 100_000;
const CONTROL_CHARACTERS = /[\u0000-\u001f\u007f-\u009f]/;
const GO_EDGE_WHITESPACE = /^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+|[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+$/gu;

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
    if (Object.hasOwn(value, field)) projected[field] = value[field];
  }
  return projected;
}

function requireProviderKind(kind) {
  if (!SUPPORTED_PROVIDERS.has(kind)) {
    throw new RangeError("Onboarding provider kind is not supported");
  }
  return kind;
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
  const completed = snapshot.phase === "completed";
  const currentVersion = Number.isSafeInteger(snapshot.current_version) && snapshot.current_version > 0
    ? snapshot.current_version : 0;
  const completedVersion = Number.isSafeInteger(snapshot.completed_version)
    && snapshot.completed_version >= 0 ? snapshot.completed_version : -1;
  const restartInProgress = snapshot.restart_in_progress;
  if (!currentVersion || completedVersion < 0 || completedVersion > currentVersion
    || typeof restartInProgress !== "boolean" || typeof snapshot.required !== "boolean"
    || snapshot.required !== (completedVersion < currentVersion || restartInProgress)
    || snapshot.required !== !completed
    || (restartInProgress && completedVersion !== currentVersion)) return null;
  const revision = positiveRevision(snapshot.revision);
  const providerKindValue = optionalSemanticString(snapshot, "provider_kind");
  const suggestionValue = optionalSemanticString(snapshot, "suggested_provider_kind");
  const providerID = optionalSemanticString(snapshot, "provider_id");
  const accountID = optionalSemanticString(snapshot, "account_id");
  const modelID = optionalSemanticString(snapshot, "model_id");
  if (!revision || [providerKindValue, suggestionValue, providerID, accountID, modelID].includes(null)) {
    return null;
  }
  const providerKind = SUPPORTED_PROVIDERS.has(providerKindValue) ? providerKindValue : "";
  const suggestedProviderKind = SUPPORTED_PROVIDERS.has(suggestionValue) ? suggestionValue : "";
  if (snapshot.phase !== "provider" && !completed && !providerKind) return null;
  if (["setup", "persona", "test"].includes(snapshot.phase)
    && (!accountID || providerID !== providerKind)) return null;
  if (["persona", "test"].includes(snapshot.phase) && !modelID) return null;
  return {
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
  };
}

export function normalizeProviderSelection(snapshot, { providerKind, revision }) {
  const normalized = normalizeStatus(snapshot);
  if (!normalized || normalized.phase !== "connect"
    || normalized.provider_kind !== providerKind
    || normalized.revision !== successorRevision(revision)
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
    if (!key || !count || typeof sample !== "string" || sample.length > ANSWER_LIMIT) return null;
    const projected = { key, count, sample };
    if (Object.hasOwn(hole, "value")) {
      if (typeof hole.value !== "string" || hole.value.length > IDENTIFIER_LIMIT) return null;
      projected.value = hole.value;
    }
    placeholders.push(projected);
  }
  return { ready: response.ready, display_name: response.display_name, placeholders };
}

export function normalizeTestMessage(raw) {
  if (typeof raw !== "string") return null;
  const value = raw.replace(GO_EDGE_WHITESPACE, "");
  if (!value || [...value].length > 500 || CONTROL_CHARACTERS.test(value)) return null;
  return value;
}

function safeAnswer(value) {
  if (typeof value !== "string" || !value.trim() || value.length > ANSWER_LIMIT) return null;
  if (/\u0000/u.test(value)) return null;
  return value;
}

function normalizedIdentityText(value) {
  if (typeof value !== "string") return "";
  return value.normalize("NFKC").trim().replace(/\s+/gu, " ").toUpperCase();
}

export function normalizedAnswerContainsName(answer, displayName) {
  const normalizedName = normalizedIdentityText(displayName);
  return Boolean(normalizedName)
    && normalizedIdentityText(answer).includes(normalizedName);
}

export function normalizeTestResponse(response, { displayName, snapshot, now }) {
  if (!isRecord(response)) return null;
  const answer = safeAnswer(response.answer);
  const botName = semanticString(response.bot_name);
  const providerID = semanticString(response.provider_id);
  const modelID = semanticString(response.model_id);
  const token = semanticString(response.test_token, { limit: OPAQUE_TOKEN_LIMIT });
  const expiresAt = typeof response.expires_at === "string" ? Date.parse(response.expires_at) : NaN;
  if (!answer || botName !== displayName || !normalizedAnswerContainsName(answer, displayName)
    || providerID !== snapshot.provider_id || modelID !== snapshot.model_id
    || !token || !Number.isFinite(expiresAt) || expiresAt <= now
    || response.revision !== successorRevision(snapshot.revision)) return null;
  return {
    answer,
    botName,
    token,
    expiresAt,
    revision: response.revision,
  };
}

export function normalizeCompleteResponse(response) {
  if (!isRecord(response) || response.completed !== true || response.onboarding_version !== 1) {
    return null;
  }
  const comboID = semanticString(response.combo_id);
  return comboID ? { completed: true, onboardingVersion: 1, comboID } : null;
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

export function testResponseError(response, displayName, now) {
  if (isRecord(response) && ((typeof response.bot_name === "string"
    && response.bot_name !== displayName)
    || (safeAnswer(response.answer) && !normalizedAnswerContainsName(response.answer, displayName)))) {
    return "Bot đã phản hồi nhưng chưa áp dụng đúng Persona.";
  }
  const expiry = isRecord(response) && typeof response.expires_at === "string"
    ? Date.parse(response.expires_at) : NaN;
  return Number.isFinite(expiry) && expiry <= now
    ? "Kết quả thử đã hết hạn. Vui lòng thử lại."
    : "Chưa thể xác minh kết quả trò chuyện. Vui lòng thử lại.";
}

export function testChatRecovery(code) {
  if (["ONBOARDING_REVISION_CONFLICT", "ONBOARDING_PHASE_INVALID", "ONBOARDING_TEST_BUSY"].includes(code)) {
    return "refresh";
  }
  if (code === "ONBOARDING_PERSONA_CHANGED") return "persona";
  if (["ONBOARDING_STAGING_INVALID", "ONBOARDING_NO_MODEL"].includes(code)) return "provider";
  return "local";
}

export function safeServerFieldCount(error) {
  if (error?.status !== 422 || error?.code !== "AGENT_PLACEHOLDERS_REMAIN"
    || !isRecord(error.fields)) return 0;
  let count = 0;
  for (const key of Object.keys(error.fields)) {
    if (semanticString(key)) count++;
  }
  return count;
}

export function validateOnboardingPageService(service) {
  for (const method of [
    "status", "selectProvider", "setup", "loadAgent", "saveAgent", "testChat", "complete",
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
  return Object.freeze({
    status(signal) {
      return Promise.resolve(requestJSON("/onboarding/status", { signal })).then(projectStatus);
    },
    selectProvider(kind, revision, signal) {
      const providerKind = requireProviderKind(kind), currentRevision = requireRevision(revision);
      requireSuccessor(currentRevision);
      return Promise.resolve(requestJSON("/onboarding/provider", {
        method: "PUT", body: { kind: providerKind, revision: currentRevision }, signal,
      })).then((response) => {
        const normalized = normalizeProviderSelection(response, { providerKind, revision: currentRevision });
        if (!normalized) throw new TypeError("Invalid onboarding provider response");
        return normalized;
      });
    },
    setup(accountId, revision, signal) {
      const exactAccountID = requireAccountID(accountId), currentRevision = requireRevision(revision);
      requireSuccessor(currentRevision);
      return Promise.resolve(requestJSON("/onboarding/setup", {
        method: "POST", body: { account_id: exactAccountID, revision: currentRevision }, signal,
      })).then((response) => {
        const normalized = normalizeSetupResponse(response, {
          accountID: exactAccountID, revision: currentRevision,
        });
        if (!normalized) throw new TypeError("Invalid onboarding setup response");
        return normalized;
      });
    },
    loadAgent({ signal } = {}) {
      return requestJSON("/agent", { signal });
    },
    saveAgent(payload, { signal } = {}) {
      return requestJSON("/agent", { method: "PUT", body: payload, signal });
    },
    testChat(message, revision, { signal } = {}) {
      const currentRevision = requireRevision(revision);
      requireSuccessor(currentRevision);
      return requestJSON("/onboarding/test-chat", {
        method: "POST", body: { message, revision: currentRevision }, signal,
      });
    },
    complete(testToken, revision, { signal } = {}) {
      const token = semanticString(testToken, { limit: OPAQUE_TOKEN_LIMIT });
      const currentRevision = requireRevision(revision);
      if (!token) throw new TypeError("Onboarding test token is invalid");
      return requestJSON("/onboarding/complete", {
        method: "POST", body: { test_token: token, revision: currentRevision }, signal,
      });
    },
  });
}
