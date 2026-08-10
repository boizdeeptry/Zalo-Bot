import test from "node:test";
import assert from "node:assert/strict";

import { isProviderConnected } from "../overlay/internal/webui/static/core/providers-status.js";

// Luật "đã kết nối" dùng chung cho nhãn trang Providers và bộ lọc picker Combos — ghim từng nhánh ở
// đây để một lượt sửa nhãn không lặng lẽ đổi bộ lọc (và ngược lại).

test("a subscription provider needs at least one enabled account", () => {
  const base = { kind: "claude-code", system: true, credential_configured: false };
  assert.equal(isProviderConnected({ ...base }), false, "no accounts field → not connected");
  assert.equal(isProviderConnected({ ...base, accounts: [] }), false, "empty accounts → not connected");
  assert.equal(
    isProviderConnected({ ...base, accounts: [{ id: "a1", enabled: false }] }),
    false,
    "only disabled accounts → not connected",
  );
  assert.equal(
    isProviderConnected({ ...base, accounts: [{ id: "a1", enabled: true }] }),
    true,
    "an enabled account → connected (system/credential irrelevant for subscription)",
  );
  // enabled mặc định (không có trường): coi như bật.
  assert.equal(isProviderConnected({ ...base, accounts: [{ id: "a1" }] }), true);
});

test("kind chuẩn hoá _ thành - trước khi tra danh sách thuê bao", () => {
  assert.equal(isProviderConnected({ kind: "claude_code", accounts: [{ enabled: true }] }), true);
  assert.equal(isProviderConnected({ kind: "claude_code", credential_configured: true }), false,
    "một kind thuê bao 0 account KHÔNG được credential cứu");
});

test("an API provider connects via ok status, system, or a configured credential", () => {
  assert.equal(isProviderConnected({ kind: "openai" }), false, "nothing configured → not connected");
  assert.equal(isProviderConnected({ kind: "openai", last_check_status: "ok" }), true);
  assert.equal(isProviderConnected({ kind: "openai", system: true }), true);
  assert.equal(isProviderConnected({ kind: "openai", credential_configured: true }), true);
  // enabled không tính vào "đã nối" (cùng cách hasAnyConnectedProvider bên Go).
  assert.equal(isProviderConnected({ kind: "openai", enabled: false, credential_configured: true }), true);
});

test("credential_unreadable is never connected, whatever the kind", () => {
  assert.equal(
    isProviderConnected({ kind: "openai", credential_configured: true, credential_unreadable: true }),
    false,
  );
  assert.equal(
    isProviderConnected({ kind: "codex", credential_unreadable: true, accounts: [{ enabled: true }] }),
    false,
  );
});

test("a missing provider is not connected", () => {
  assert.equal(isProviderConnected(null), false);
  assert.equal(isProviderConnected(undefined), false);
});
