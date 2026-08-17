import test from "node:test";
import assert from "node:assert/strict";

import {
  normalizeSetupResult,
  normalizeStatus,
} from "../overlay/internal/webui/static/pages/onboarding-contract.js";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

function status(overrides = {}) {
  return onboardingStatus("test", overrides);
}

test("status validates and retains exact version, required, and restart markers", () => {
  const fresh = normalizeStatus(status({ current_version: 2, completed_version: 1 }));
  assert.deepEqual({
    required: fresh?.required,
    current_version: fresh?.current_version,
    completed_version: fresh?.completed_version,
    restart_in_progress: fresh?.restart_in_progress,
  }, { required: true, current_version: 2, completed_version: 1, restart_in_progress: false });
  const restarted = normalizeStatus(status({
    phase: "provider", current_version: 2, completed_version: 2, restart_in_progress: true,
  }));
  assert.equal(restarted?.phase, "provider");
  assert.equal(normalizeStatus(status({ phase: "completed", current_version: 2 }))?.required, false);

  const malformed = [
    { current_version: 0 }, { current_version: -1 }, { current_version: 1.5 },
    { current_version: "1" }, { completed_version: -1 }, { completed_version: 2 },
    { completed_version: 0.5 }, { completed_version: "0" }, { restart_in_progress: "false" },
    { required: false },
    { phase: "completed", required: true },
    { phase: "completed", restart_in_progress: true },
    { phase: "test", completed_version: 1, restart_in_progress: false },
    { phase: "test", completed_version: 0, restart_in_progress: true },
  ];
  for (const overrides of malformed) assert.equal(normalizeStatus(status(overrides)), null, JSON.stringify(overrides));
  for (const field of ["required", "current_version", "completed_version", "restart_in_progress"]) {
    const missing = status();
    delete missing[field];
    assert.equal(normalizeStatus(missing), null, `missing ${field}`);
  }
});

test("Setup-to-Persona retains lifecycle markers", () => {
  const lifecycle = normalizeStatus(status({ phase: "setup", revision: 3 }));
  const setupResponse = {
    revision: 4,
    provider_kind: "codex",
    provider_id: "codex",
    account_id: "account-1",
    model_id: "gpt-5.6-terra",
    staged_combo_id: "combo-staged",
    combo_name: "Codex mặc định",
  };
  const expected = {
    accountID: "account-1", revision: 3, providerKind: "codex", providerID: "codex", lifecycle,
  };
  const result = normalizeSetupResult(setupResponse, expected);
  assert.deepEqual({
    current_version: result?.current_version,
    completed_version: result?.completed_version,
    restart_in_progress: result?.restart_in_progress,
  }, { current_version: 1, completed_version: 0, restart_in_progress: false });
  const malformedLifecycle = { ...lifecycle };
  delete malformedLifecycle.current_version;
  assert.equal(normalizeSetupResult(setupResponse, {
    ...expected, lifecycle: malformedLifecycle,
  }), null);
});
