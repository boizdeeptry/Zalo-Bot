import test from "node:test";
import assert from "node:assert/strict";

import { isProviderConnected } from "../overlay/internal/webui/static/core/providers-status.js";

// Cùng một luật cho nhãn Providers và picker Combos. Quyết định chỉ đến từ connection_mode đã được
// server project/normalizer xác thực; kind, system và trạng thái Provider không được tự đổi domain.

test("account mode needs at least one explicitly enabled account", () => {
  const base = {
    kind: "future-cli", connection_mode: "account", system: true,
    credential_configured: true, credential_unreadable: false,
  };
  assert.equal(isProviderConnected(base), false, "missing accounts stays disconnected");
  assert.equal(isProviderConnected({ ...base, accounts: [] }), false);
  assert.equal(isProviderConnected({ ...base, accounts: [{ id: "a1", enabled: false }] }), false);
  assert.equal(isProviderConnected({ ...base, accounts: [{ id: "a1" }] }), false,
    "missing enabled is not silently promoted");
  assert.equal(isProviderConnected({
    ...base, enabled: false, accounts: [{ id: "a1", enabled: true }],
  }), true, "Provider.enabled is a separate usability filter");
});

test("credential mode uses configured readable credential independent of Provider.enabled", () => {
  const base = { kind: "future-api", connection_mode: "credential", enabled: false };
  assert.equal(isProviderConnected(base), false);
  assert.equal(isProviderConnected({ ...base, system: true }), false, "system is not readiness");
  assert.equal(isProviderConnected({ ...base, last_check_status: "ok" }), false,
    "a stale health check is not credential readiness");
  assert.equal(isProviderConnected({ ...base, credential_configured: true }), true);
  assert.equal(isProviderConnected({
    ...base, credential_configured: true, credential_unreadable: true,
  }), false);
});

test("none, unknown, and missing connection modes always fail closed", () => {
  const apparentlyReady = {
    kind: "gemini-cli", enabled: true, system: true, credential_configured: true,
    accounts: [{ id: "a1", enabled: true }],
  };
  assert.equal(isProviderConnected({ ...apparentlyReady, connection_mode: "none" }), false);
  assert.equal(isProviderConnected({ ...apparentlyReady, connection_mode: "oauth" }), false);
  assert.equal(isProviderConnected(apparentlyReady), false);
  assert.equal(isProviderConnected(null), false);
  assert.equal(isProviderConnected(undefined), false);
});

test("kind spelling cannot switch readiness modes", () => {
  assert.equal(isProviderConnected({
    kind: "claude_code", connection_mode: "credential", credential_configured: true,
  }), true);
  assert.equal(isProviderConnected({
    kind: "openai", connection_mode: "account", accounts: [{ enabled: true }],
  }), true);
});
