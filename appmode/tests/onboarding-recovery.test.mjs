import test from "node:test";
import assert from "node:assert/strict";

import { createOnboardingService } from "../overlay/internal/webui/static/pages/onboarding.js";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

test("Back-to-Provider keeps its strict successor contract during late-stage recovery", async () => {
  const calls = [];
  const response = onboardingStatus("provider", { revision: 9 });
  const signal = new AbortController().signal;
  const service = createOnboardingService({
    requestJSON(path, options) {
      calls.push({ path, options });
      return Promise.resolve(response);
    },
  });

  const state = await service.backToProviders(8, signal);

  assert.equal(state.phase, "provider");
  assert.equal(state.revision, 9);
  assert.deepEqual(calls, [{
    path: "/onboarding/back-to-providers",
    options: { method: "POST", body: { revision: 8 }, signal },
  }]);
});

test("Back-to-Provider rejects a resolved non-successor without exposing it", async () => {
  const service = createOnboardingService({
    requestJSON: () => Promise.resolve(onboardingStatus("provider", { revision: 12 })),
  });

  await assert.rejects(service.backToProviders(8), {
    name: "OnboardingProviderResponseError",
    message: "Invalid onboarding provider response",
  });
});
