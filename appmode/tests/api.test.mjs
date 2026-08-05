import test from "node:test";
import assert from "node:assert/strict";

import {
  AppAPIError,
  requestInit,
  requestJSON,
} from "../overlay/internal/webui/static/core/api.js";

test("mutation adds the Portal header and JSON content type", () => {
  const init = requestInit("PUT", { name: "x" });

  assert.equal(init.headers.get("X-Agentdc-Portal"), "1");
  assert.equal(init.headers.get("Content-Type"), "application/json");
  assert.equal(init.body, JSON.stringify({ name: "x" }));
  assert.equal(init.credentials, "same-origin");
});

test("read request does not add mutation-only headers", () => {
  const init = requestInit("GET");

  assert.equal(init.headers.has("X-Agentdc-Portal"), false);
  assert.equal(init.headers.has("Content-Type"), false);
  assert.equal("body" in init, false);
});

test("structured API failures become AppAPIError values", async () => {
  const fetchImpl = async () => new Response(JSON.stringify({
    error: {
      code: "INVALID_NAME",
      message: "Tên chưa hợp lệ",
      fields: { name: "required" },
    },
  }), {
    status: 422,
    headers: { "Content-Type": "application/json" },
  });

  await assert.rejects(
    requestJSON("/agent", { method: "PUT", body: {}, fetchImpl }),
    (error) => {
      assert.equal(error instanceof AppAPIError, true);
      assert.equal(error.code, "INVALID_NAME");
      assert.equal(error.message, "Tên chưa hợp lệ");
      assert.deepEqual(error.fields, { name: "required" });
      assert.equal(error.status, 422);
      return true;
    },
  );
});
