import test from "node:test";
import assert from "node:assert/strict";

import {
  createOnboardingPage,
  createOnboardingService,
} from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, installDOM, text } from "./helpers/dom-harness.mjs";
import { onboardingStatus } from "./helpers/onboarding-fixtures.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const settle = async () => { await flush(); await flush(); };
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const form = (root) => find(root, (node) => node.tagName === "FORM");
const input = (root) => find(root, (node) => node.tagName === "INPUT");

function appError(code, status = 409) {
  return Object.assign(new Error(`private ${code}`), { name: "AppAPIError", code, status });
}

function agent(overrides = {}) {
  return { ready: true, display_name: "Bé Mi", placeholders: [], ...overrides };
}

function result(revision, overrides = {}) {
  return {
    answer: "Xin chào, mình là Bé Mi.",
    bot_name: "Bé Mi",
    provider_id: "codex",
    model_id: "gpt-5.6-terra",
    position: 0,
    test_token: "volatile-token",
    expires_at: new Date(Date.now() + 600_000).toISOString(),
    revision,
    ...overrides,
  };
}

function service(overrides = {}) {
  return {
    status: () => Promise.resolve(onboardingStatus("test")),
    selectProvider: () => Promise.reject(new Error("not used")),
    setup: () => Promise.reject(new Error("not used")),
    loadAgent: () => Promise.resolve(agent()),
    saveAgent: () => Promise.reject(new Error("not used")),
    testChat: (_message, revision) => Promise.resolve(result(revision + 1)),
    complete: () => Promise.reject(new Error("not used")),
    ...overrides,
  };
}

function mount(t, options = {}) {
  const dom = installDOM();
  const host = document.createElement("div");
  const page = createOnboardingPage({
    initialStatus: onboardingStatus("test"),
    service: service(),
    ...options,
  });
  page.mount(host);
  t.after(() => {
    page.dispose();
    dom.restore();
  });
  return { page, host };
}

function submit(root) {
  form(root).dispatchEvent({ type: "submit" });
}

test("revision conflict refreshes identity and retries the exact new request body", async (t) => {
  const calls = [];
  let attempts = 0;
  const authoritative = onboardingStatus("test", {
    revision: 11,
    provider_kind: "claude-code",
    provider_id: "claude-code",
    account_id: "account-2",
    model_id: "claude-sonnet",
  });
  const requestService = createOnboardingService({
    requestJSON(path, options = {}) {
      calls.push({ path, options });
      if (path === "/agent") return Promise.resolve(agent({ display_name: "Trợ lý An" }));
      if (path === "/onboarding/status") return Promise.resolve(authoritative);
      if (path === "/onboarding/test-chat") {
        attempts++;
        if (attempts === 1) return Promise.reject(appError("ONBOARDING_REVISION_CONFLICT"));
        return Promise.resolve(result(options.body.revision + 1, {
          answer: "Xin chào, mình là Trợ lý An.",
          bot_name: "Trợ lý An",
          provider_id: "claude-code",
          model_id: "claude-sonnet",
        }));
      }
      throw new Error(`unexpected request: ${path}`);
    },
  });
  const { host } = mount(t, { service: requestService });
  await settle();
  submit(host);
  await settle();
  assert.equal(attempts, 1, "refresh must not auto-repeat Test Chat");
  submit(host);
  await settle();

  assert.deepEqual(calls.filter(({ path }) => path === "/onboarding/test-chat")
    .map(({ options }) => options.body), [
    { message: "Xin chào", revision: 7 },
    { message: "Xin chào", revision: 11 },
  ]);
  assert.equal(calls.filter(({ path }) => path === "/onboarding/status").length, 1);
  assert.ok(button(host, "Ổn, dùng cấu hình này"));
});

test("phase invalid adopts an authoritative new phase and provider", async (t) => {
  let statuses = 0;
  const authoritative = onboardingStatus("persona", {
    revision: 13,
    provider_kind: "claude-code",
    provider_id: "claude-code",
    account_id: "account-2",
    model_id: "claude-sonnet",
  });
  const { host } = mount(t, {
    service: service({
      testChat: () => Promise.reject(appError("ONBOARDING_PHASE_INVALID")),
      status: () => { statuses++; return Promise.resolve(authoritative); },
      loadAgent: () => Promise.resolve(agent({ display_name: "Trợ lý An" })),
    }),
  });
  await settle();
  submit(host);
  await settle();

  assert.equal(statuses, 1);
  assert.match(text(host), /Trợ lý của bạn là ai/u);
  assert.doesNotMatch(text(host), /Thử trò chuyện/u);
});

test("persona changed opens the shared editor and saves with the current Test revision", async (t) => {
  let loads = 0;
  let saved;
  const { host } = mount(t, {
    service: service({
      testChat: () => Promise.reject(appError("ONBOARDING_PERSONA_CHANGED")),
      loadAgent: () => Promise.resolve(++loads === 1 ? agent() : agent({
        ready: false,
        display_name: "",
        placeholders: [{ key: "TEN_BOT", count: 1, sample: "Tên bot" }],
      })),
      saveAgent(payload) {
        saved = payload;
        return Promise.resolve({
          ready: true,
          display_name: "Tên mới",
          placeholders: [],
          onboarding_phase: "test",
          onboarding_revision: 8,
        });
      },
    }),
  });
  await settle();
  submit(host);
  await settle();
  assert.match(text(host), /Trợ lý của bạn là ai/u);
  assert.equal(button(host, "Ổn, dùng cấu hình này"), null);

  const field = input(host);
  field.value = " Tên mới ";
  field.dispatchEvent({ type: "input" });
  submit(host);
  await settle();
  assert.deepEqual(saved, {
    values: { TEN_BOT: "Tên mới" },
    display_name: "Tên mới",
    require_complete: true,
    onboarding_revision: 7,
  });
  assert.equal(input(host).value, "Xin chào");
  assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
});

test("broken staging refreshes once, follows moved state, or falls back to Provider", async (t) => {
  for (const code of ["ONBOARDING_STAGING_INVALID", "ONBOARDING_NO_MODEL"]) {
    await t.test(`${code} unchanged`, async (subtest) => {
      let statuses = 0;
      const { host } = mount(subtest, {
        service: service({
          testChat: () => Promise.reject(appError(code)),
          status: () => { statuses++; return Promise.resolve(onboardingStatus("test", { revision: 9 })); },
        }),
      });
      await settle();
      submit(host);
      await settle();
      assert.equal(statuses, 1);
      assert.match(text(host), /Chọn nhà cung cấp/u);
      assert.match(text(host), /chọn lại nhà cung cấp/i);
      assert.doesNotMatch(text(host), /Thử trò chuyện với/u);
    });

    await t.test(`${code} moved`, async (subtest) => {
      const { host } = mount(subtest, {
        service: service({
          testChat: () => Promise.reject(appError(code)),
          status: () => Promise.resolve(onboardingStatus("persona", {
            revision: 10,
            provider_kind: "claude-code",
            provider_id: "claude-code",
            account_id: "account-2",
            model_id: "claude-sonnet",
          })),
        }),
      });
      await settle();
      submit(host);
      await settle();
      assert.match(text(host), /Trợ lý của bạn là ai/u);
      assert.doesNotMatch(text(host), /Chọn nhà cung cấp/u);
    });
  }
});

test("broken staging corruption and refresh failure fail closed at Provider", async (t) => {
  for (const [name, statusResult] of [
    ["corrupt", () => {
      const malformed = onboardingStatus("test");
      delete malformed.current_version;
      return Promise.resolve(malformed);
    }],
    ["request failure", () => Promise.reject(new Error("private status detail"))],
  ]) {
    await t.test(name, async (subtest) => {
      let statuses = 0;
      const { host } = mount(subtest, {
        service: service({
          testChat: () => Promise.reject(appError("ONBOARDING_STAGING_INVALID")),
          status: () => { statuses++; return statusResult(); },
        }),
      });
      await settle();
      submit(host);
      await settle();
      assert.equal(statuses, 1);
      assert.match(text(host), /Chọn nhà cung cấp/u);
      assert.doesNotMatch(text(host), /private status detail|Thử trò chuyện với/u);
    });
  }
});

test("busy refresh is one-shot and only an explicit retry starts another Test", async (t) => {
  let tests = 0;
  let statuses = 0;
  const { host } = mount(t, {
    service: service({
      testChat: (_message, revision) => {
        tests++;
        return tests === 1
          ? Promise.reject(appError("ONBOARDING_TEST_BUSY"))
          : Promise.resolve(result(revision + 1));
      },
      status: () => { statuses++; return Promise.resolve(onboardingStatus("test", { revision: 9 })); },
    }),
  });
  await settle();
  submit(host);
  await settle();
  assert.deepEqual({ tests, statuses }, { tests: 1, statuses: 1 });
  submit(host);
  await settle();
  assert.deepEqual({ tests, statuses }, { tests: 2, statuses: 1 });
  assert.ok(button(host, "Ổn, dùng cấu hình này"));
});

test("conflict refresh failure is retryable and still adopts the new revision once", async (t) => {
  let statuses = 0;
  const revisions = [];
  const { host } = mount(t, {
    service: service({
      testChat: (_message, revision) => {
        revisions.push(revision);
        return revisions.length === 1
          ? Promise.reject(appError("ONBOARDING_REVISION_CONFLICT"))
          : Promise.resolve(result(revision + 1));
      },
      status: () => {
        statuses++;
        return statuses === 1
          ? Promise.reject(new Error("private refresh failure"))
          : Promise.resolve(onboardingStatus("test", { revision: 12 }));
      },
    }),
  });
  await settle();
  submit(host);
  await settle();
  assert.match(text(host), /Chưa thể tiếp tục/u);
  assert.doesNotMatch(text(host), /private refresh failure/u);
  button(host, "Thử lại").click();
  await settle();
  assert.deepEqual({ statuses, revisions }, { statuses: 2, revisions: [7] });
  submit(host);
  await settle();
  assert.deepEqual(revisions, [7, 12]);
  assert.ok(button(host, "Ổn, dùng cấu hình này"));
});

test("disposing during Test error refresh aborts and suppresses the continuation", async (t) => {
  let resolveStatus;
  let statusSignal;
  let handoffs = 0;
  const pendingStatus = new Promise((resolve) => { resolveStatus = resolve; });
  const { page, host } = mount(t, {
    service: service({
      testChat: () => Promise.reject(appError("ONBOARDING_PHASE_INVALID")),
      status: (signal) => { statusSignal = signal; return pendingStatus; },
    }),
    onComplete: () => { handoffs++; },
  });
  await settle();
  submit(host);
  await flush();
  assert.ok(statusSignal instanceof AbortSignal);
  page.dispose();
  assert.equal(statusSignal.aborted, true);
  resolveStatus(onboardingStatus("completed", { revision: 20 }));
  await settle();
  assert.equal(text(host), "");
  assert.equal(handoffs, 0);
});

test("local Test errors stay safe and never start status refreshes", async (t) => {
  const cases = [
    ["ONBOARDING_PERSONA_NOT_APPLIED", /chưa áp dụng đúng Persona/i],
    ["ONBOARDING_TEST_ANSWER_INVALID", /Chưa thể trò chuyện thử/i],
    ["ONBOARDING_TEST_TIMEOUT", /Chưa thể trò chuyện thử/i],
    ["ONBOARDING_REQUEST_CANCELED", /Chưa thể trò chuyện thử/i],
    ["ONBOARDING_TEST_FAILED", /Chưa thể trò chuyện thử/i],
  ];
  for (const [code, copy] of cases) {
    await t.test(code, async (subtest) => {
      let statuses = 0;
      const { host } = mount(subtest, {
        service: service({
          testChat: () => Promise.reject(appError(code)),
          status: () => { statuses++; return Promise.resolve(onboardingStatus("test")); },
        }),
      });
      await settle();
      submit(host);
      await settle();
      assert.equal(statuses, 0);
      assert.match(text(host), copy);
      assert.doesNotMatch(text(host), new RegExp(`private ${code}`, "u"));
      assert.ok(button(host, "Thử lại"));
      assert.ok(button(host, "Quay lại chỉnh Persona"));
      assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
    });
  }
});
