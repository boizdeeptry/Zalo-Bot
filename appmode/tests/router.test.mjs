import test from "node:test";
import assert from "node:assert/strict";

import {
  createRouteHost,
  routeFromHash,
} from "../overlay/internal/webui/static/core/router.js";

test("known and unknown hashes resolve predictably", () => {
  assert.equal(routeFromHash("#knowledge"), "knowledge");
  assert.equal(routeFromHash("#models"), "models");
  assert.equal(routeFromHash("#not-a-page"), "overview");
  assert.equal(routeFromHash(""), "overview");
});

test("mounting a route disposes the old page before clearing its DOM", async () => {
  const events = [];
  const container = {
    replaceChildren() {
      events.push("clear");
    },
  };
  const host = createRouteHost(container);

  await host.mount({
    mount() {
      events.push("mount:first");
      return { dispose: () => events.push("dispose:first") };
    },
  });
  await host.mount({
    mount() {
      events.push("mount:second");
      return { dispose: () => events.push("dispose:second") };
    },
  });
  host.dispose();

  assert.deepEqual(events, [
    "clear",
    "mount:first",
    "dispose:first",
    "clear",
    "mount:second",
    "dispose:second",
  ]);
});
