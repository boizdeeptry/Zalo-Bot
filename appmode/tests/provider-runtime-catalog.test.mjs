import test from "node:test";
import assert from "node:assert/strict";

import {
  PROVIDER_CATALOG_ERROR,
  normalizeProviderRuntimeResponse,
} from "../overlay/internal/webui/static/core/provider-runtime-catalog.js";
import {
  CODEX_CONNECTED,
  providerRuntimeResponse,
} from "./helpers/provider-runtime-fixtures.mjs";

const clone = (value) => structuredClone(value);

test("runtime provider metadata is detached, deeply frozen, and stripped to its public allowlist", () => {
  const payload = providerRuntimeResponse([{
    ...CODEX_CONNECTED,
    credential: "must-not-survive",
    config_dir: "D:/private/provider",
    command: ["private-cli", "login"],
    env: { PRIVATE_TOKEN: "secret" },
    models: [{
      model_id: "future-model", name: "Future Model", source: "manual", available: true,
      endpoint: "https://private.invalid",
    }],
    accounts: [{
      id: "a1", label: "Tài khoản 1", email: "one@example.test", enabled: true,
      config_dir: "D:/private/account",
    }],
  }]);
  payload.provider_options[0] = {
    ...payload.provider_options[0],
    credential: "secret",
    config_dir: "D:/private/runtime",
    command: "private-cli",
    env: ["PRIVATE_TOKEN"],
    callback: () => {},
  };

  const normalized = normalizeProviderRuntimeResponse(payload);
  const body = normalized.providers[0];
  const option = normalized.provider_options[0];

  assert.deepEqual(Object.keys(option), [
    "kind", "display_name", "description", "group", "connectable", "connection_mode",
    "execution_mode", "visible", "prefix", "theme_color", "beta", "ui_order",
  ]);
  assert.deepEqual(Object.keys(body), [
    "id", "name", "kind", "endpoint", "enabled", "system", "credential_configured",
    "credential_unreadable", "last_check_status", "last_error", "last_checked_at", "models",
    "accounts", "connection_mode",
  ]);
  assert.equal("config_dir" in body, false);
  assert.equal("endpoint" in body.models[0], false);
  assert.equal("config_dir" in body.accounts[0], false);
  assert.ok(Object.isFrozen(normalized));
  assert.ok(Object.isFrozen(normalized.providers));
  assert.ok(Object.isFrozen(body));
  assert.ok(Object.isFrozen(body.models));
  assert.ok(Object.isFrozen(body.models[0]));
  assert.ok(Object.isFrozen(body.accounts));
  assert.ok(Object.isFrozen(body.accounts[0]));
  assert.ok(Object.isFrozen(normalized.provider_options));
  assert.ok(Object.isFrozen(option));

  payload.providers[0].name = "forged";
  payload.provider_options[0].display_name = "forged";
  assert.equal(body.name, "Codex");
  assert.equal(option.display_name, "Codex");
});

test("runtime provider metadata preserves exact kinds and accepts a safe synthetic account runtime", () => {
  const payload = providerRuntimeResponse([], { hasConnectedProvider: true });
  payload.provider_options.splice(1, 0, {
    kind: "future-cli",
    display_name: "Future CLI",
    description: "Runtime thử nghiệm không cần frontend literal",
    group: "subscription",
    connectable: true,
    connection_mode: "account",
    execution_mode: "local",
    visible: true,
    prefix: "fu",
    theme_color: "#123abc",
    beta: true,
    ui_order: 15,
  });

  const normalized = normalizeProviderRuntimeResponse(payload);
  assert.deepEqual(normalized.provider_options.map((entry) => entry.kind).slice(0, 3), [
    "codex", "future-cli", "claude-code",
  ]);
  assert.equal(normalized.provider_options[1].kind, "future-cli");
  assert.equal(normalized.provider_options[1].execution_mode, "local");
});

test("runtime provider metadata rejects malformed catalogs with one generic error", async (t) => {
  const cases = {
    "missing provider_options": (payload) => { delete payload.provider_options; },
    "duplicate exact kind": (payload) => { payload.provider_options[1].kind = payload.provider_options[0].kind; },
    "underscore kind is not canonicalized": (payload) => { payload.provider_options[0].kind = "future_cli"; },
    "duplicate ui_order": (payload) => { payload.provider_options[1].ui_order = payload.provider_options[0].ui_order; },
    "decreasing ui_order": (payload) => { payload.provider_options.reverse(); },
    "unknown group": (payload) => { payload.provider_options[0].group = "other"; },
    "unknown connection mode": (payload) => { payload.provider_options[0].connection_mode = "oauth"; },
    "unknown execution mode": (payload) => { payload.provider_options[0].execution_mode = "shell"; },
    "invalid visibility": (payload) => { payload.provider_options[0].visible = "yes"; },
    "invalid connectable": (payload) => { payload.provider_options[0].connectable = 1; },
    "invalid beta": (payload) => { payload.provider_options[0].beta = "false"; },
    "invalid connected summary": (payload) => { payload.hasConnectedProvider = "false"; },
    "invalid theme hex": (payload) => { payload.provider_options[0].theme_color = "red"; },
    "invalid prefix": (payload) => { payload.provider_options[0].prefix = "TOO-LONG!"; },
    "zero ui order": (payload) => { payload.provider_options[0].ui_order = 0; },
    "oversized display name": (payload) => { payload.provider_options[0].display_name = "x".repeat(129); },
    "oversized description": (payload) => { payload.provider_options[0].description = "x".repeat(1025); },
    "hidden runtime with account mode": (payload) => {
      const hidden = payload.provider_options.at(-1);
      hidden.connection_mode = "account";
      hidden.connectable = true;
    },
    "visible runtime with no mode": (payload) => {
      payload.provider_options[0].connection_mode = "none";
      payload.provider_options[0].connectable = false;
    },
    "account runtime in API-key group": (payload) => { payload.provider_options[0].group = "apikey"; },
    "credential runtime marked connectable": (payload) => { payload.provider_options[2].connectable = true; },
  };

  for (const [name, mutate] of Object.entries(cases)) {
    await t.test(name, () => {
      const payload = providerRuntimeResponse();
      mutate(payload);
      assert.throws(
        () => normalizeProviderRuntimeResponse(payload),
        (error) => error instanceof Error && error.message === PROVIDER_CATALOG_ERROR,
      );
    });
  }
});

test("every returned provider body requires an exact option and matching connection mode", async (t) => {
  const cases = {
    "missing visible option": (payload) => {
      payload.providers = [{ ...CODEX_CONNECTED }];
      payload.provider_options = payload.provider_options.filter((entry) => entry.kind !== "codex");
    },
    "missing hidden option": (payload) => {
      payload.providers = [{
        id: "gemini-cli", name: "Gemini CLI", kind: "gemini-cli", endpoint: "", enabled: true,
        system: true, credential_configured: false, credential_unreadable: false,
        last_check_status: "", last_error: "", last_checked_at: "", models: [], accounts: [],
        connection_mode: "none",
      }];
      payload.provider_options = payload.provider_options.filter((entry) => entry.kind !== "gemini-cli");
    },
    "mismatched body mode": (payload) => {
      payload.providers = [{ ...CODEX_CONNECTED, connection_mode: "credential" }];
    },
    "normalized-looking body kind": (payload) => {
      payload.providers = [{ ...CODEX_CONNECTED, kind: "codex_cli" }];
    },
  };

  for (const [name, mutate] of Object.entries(cases)) {
    await t.test(name, () => {
      const payload = providerRuntimeResponse();
      mutate(payload);
      assert.throws(
        () => normalizeProviderRuntimeResponse(payload),
        (error) => error instanceof Error && error.message === PROVIDER_CATALOG_ERROR,
      );
    });
  }
});

test("normalization never freezes or retains caller-owned arrays", () => {
  const payload = clone(providerRuntimeResponse([CODEX_CONNECTED]));
  const normalized = normalizeProviderRuntimeResponse(payload);
  assert.notEqual(normalized.providers, payload.providers);
  assert.notEqual(normalized.providers[0].models, payload.providers[0].models);
  assert.notEqual(normalized.provider_options, payload.provider_options);
  assert.equal(Object.isFrozen(payload), false);
  assert.equal(Object.isFrozen(payload.providers), false);
});
