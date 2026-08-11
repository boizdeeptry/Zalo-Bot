import test from "node:test";
import assert from "node:assert/strict";

import {
  createOnboardingPage,
  createOnboardingService,
} from "../overlay/internal/webui/static/pages/onboarding.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((onResolve, onReject) => {
    resolve = onResolve;
    reject = onReject;
  });
  return { promise, resolve, reject };
}

const hasClass = (node, name) => Boolean(node?.classList?.contains(name));
const button = (root, label) => find(root, (node) => node.tagName === "BUTTON"
  && text(node).includes(label));
const input = (root) => find(root, (node) => node.tagName === "INPUT");
const byClass = (root, name) => find(root, (node) => hasClass(node, name));
const futureExpiry = () => new Date(Date.now() + 600_000).toISOString();

function testStatus(overrides = {}) {
  return {
    required: true,
    phase: "test",
    provider_kind: "codex",
    suggested_provider_kind: "",
    provider_id: "codex",
    account_id: "account-1",
    model_id: "gpt-5.6-terra",
    revision: 7,
    ...overrides,
  };
}

function personaStatus(overrides = {}) {
  return { ...testStatus({ phase: "persona", revision: 4 }), ...overrides };
}

function agent(overrides = {}) {
  return {
    ready: true,
    display_name: "Bé Mi",
    placeholders: [],
    ...overrides,
  };
}

function testResult(overrides = {}) {
  return {
    answer: "Xin chào, mình là Bé Mi.",
    bot_name: "Bé Mi",
    provider_id: "codex",
    model_id: "gpt-5.6-terra",
    test_token: "opaque-test-token",
    expires_at: futureExpiry(),
    revision: 8,
    ...overrides,
  };
}

function baseService(overrides = {}) {
  return {
    status: () => Promise.resolve(testStatus()),
    selectProvider: () => Promise.reject(new Error("not used")),
    setup: () => Promise.reject(new Error("not used")),
    loadAgent: () => Promise.resolve(agent()),
    saveAgent: () => Promise.resolve({
      ready: true,
      display_name: "Bé Mi",
      onboarding_phase: "test",
      onboarding_revision: 5,
    }),
    testChat: (_message, revision) => Promise.resolve(testResult({ revision: revision + 1 })),
    complete: () => Promise.resolve({
      completed: true,
      onboarding_version: 1,
      combo_id: "combo-new",
    }),
    ...overrides,
  };
}

function mountPage(t, options = {}) {
  const dom = installDOM();
  const host = document.createElement("div");
  const page = createOnboardingPage({
    initialStatus: testStatus(),
    service: baseService(),
    ...options,
  });
  page.mount(host);
  t.after(() => {
    page.dispose();
    dom.restore();
  });
  return { page, host };
}

function setInputValue(field, value) {
  field.value = value;
  field.dispatchEvent({ type: "input" });
}

function submit(form) {
  form.dispatchEvent({ type: "submit" });
}

function assertNoSkip(root) {
  assert.equal(button(root, "Bỏ qua"), null);
  assert.equal(button(root, "Skip"), null);
  assert.doesNotMatch(text(root), /Bỏ qua|Skip/u);
}

function domAttributes(root) {
  const values = [];
  for (const node of findAll(root, (candidate) => candidate?.attributes instanceof Map)) {
    for (const [name, value] of node.attributes) values.push(`${name}=${value}`);
  }
  return values.join("\n");
}

test("late-stage service sends exact paths, bodies, and AbortSignals", async () => {
  const calls = [];
  const signal = new AbortController().signal;
  const service = createOnboardingService({
    requestJSON(path, options = {}) {
      calls.push({ path, options });
      return Promise.resolve({ ok: true });
    },
  });
  const payload = { values: { TEN_BOT: "Bé Mi" }, display_name: "Bé Mi" };

  await service.loadAgent({ signal });
  await service.saveAgent(payload, { signal });
  await service.testChat("Xin chào", 7, { signal });
  await service.complete("opaque", 8, { signal });

  assert.deepEqual(calls, [
    { path: "/agent", options: { signal } },
    { path: "/agent", options: { method: "PUT", body: payload, signal } },
    {
      path: "/onboarding/test-chat",
      options: { method: "POST", body: { message: "Xin chào", revision: 7 }, signal },
    },
    {
      path: "/onboarding/complete",
      options: { method: "POST", body: { test_token: "opaque", revision: 8 }, signal },
    },
  ]);
});

test("Persona mounts shared dynamic fields, updates counter, focuses errors, and saves exact payload", async (t) => {
  const calls = [];
  const saveGate = deferred();
  const { host } = mountPage(t, {
    initialStatus: personaStatus(),
    service: baseService({
      loadAgent(options) {
        calls.push({ op: "load", options });
        return Promise.resolve(agent({
          ready: false,
          display_name: "",
          placeholders: [
            { key: "TEN_BOT", count: 2, sample: "Tên trong lời giới thiệu" },
            { key: "vai-tro", count: 1, sample: "Vai trò {{vai-tro}}" },
          ],
        }));
      },
      saveAgent(payload, options) {
        calls.push({ op: "save", payload, options });
        return saveGate.promise;
      },
    }),
  });
  await flush();

  const form = find(host, (node) => node.tagName === "FORM");
  const fields = findAll(form, (node) => node.tagName === "INPUT");
  assert.deepEqual(findAll(form, (node) => hasClass(node, "fk")).map(text), ["Tên bot", "Vai tro"]);
  assert.deepEqual(findAll(form, (node) => hasClass(node, "fs")).map(text), [
    "Tên trong lời giới thiệu", "Vai trò {{vai-tro}}",
  ]);
  assert.match(text(byClass(form, "onboarding-persona-counter")), /Còn 2 mục cần điền/);
  assert.equal(button(form, "Tiếp tục").disabled, true);
  submit(form);
  assert.equal(calls.filter(({ op }) => op === "save").length, 0);
  assert.equal(document.activeElement, fields[0]);

  setInputValue(fields[0], "  Bé Mi  ");
  assert.match(text(byClass(form, "onboarding-persona-counter")), /Còn 1 mục cần điền/);
  setInputValue(fields[1], "  Tư vấn viên  ");
  assert.match(text(byClass(form, "onboarding-persona-counter")), /Còn 0 mục cần điền/);
  assert.equal(button(form, "Tiếp tục").disabled, false);
  submit(form);
  submit(form);
  assert.equal(calls.filter(({ op }) => op === "save").length, 1);
  const save = calls.find(({ op }) => op === "save");
  assert.deepEqual(save.payload, {
    values: { TEN_BOT: "Bé Mi", "vai-tro": "Tư vấn viên" },
    display_name: "Bé Mi",
    require_complete: true,
    onboarding_revision: 4,
  });
  assert.ok(save.options.signal instanceof AbortSignal);
  assert.equal(button(form, "Tiếp tục").disabled, true);

  saveGate.resolve({
    ready: true,
    display_name: "Bé Mi",
    onboarding_phase: "test",
    onboarding_revision: 5,
  });
  await flush();
  assert.equal(input(host).value, "Xin chào");
  assertNoSkip(host);
});

test("Persona supports legacy display-only state, retains valid names, and handles safe 422 errors", async (t) => {
  await t.test("missing legacy name", async (subtest) => {
    const saves = [];
    const error = Object.assign(new Error("private {{raw}}"), {
      status: 422,
      code: "AGENT_PLACEHOLDERS_REMAIN",
      fields: { "vai-trò": "private detail", "bad\nkey": "SECRET" },
    });
    const { host } = mountPage(subtest, {
      initialStatus: personaStatus(),
      service: baseService({
        loadAgent: () => Promise.resolve(agent({ display_name: "" })),
        saveAgent(payload) {
          saves.push(payload);
          return saves.length === 1 ? Promise.reject(error) : Promise.resolve({
            ready: true,
            display_name: "Bot Cũ",
            onboarding_phase: "test",
            onboarding_revision: 5,
          });
        },
      }),
    });
    await flush();
    const form = find(host, (node) => node.tagName === "FORM");
    const onlyInput = input(form);
    assert.equal(onlyInput.getAttribute("data-persona-kind"), "display-name");
    setInputValue(onlyInput, " Bot Cũ ");
    submit(form);
    await flush();
    assert.equal(document.activeElement, onlyInput);
    assert.match(text(form), /Còn 1 mục cần điền/);
    assert.doesNotMatch(text(form), /private|SECRET|bad/u);
    assert.deepEqual(saves[0], {
      values: {}, display_name: "Bot Cũ", require_complete: true, onboarding_revision: 4,
    });
  });

  await t.test("valid legacy name remains hidden and retained", async (subtest) => {
    let payload;
    const { host } = mountPage(subtest, {
      initialStatus: personaStatus(),
      service: baseService({
        loadAgent: () => Promise.resolve(agent({ display_name: "Trợ lý An" })),
        saveAgent(value) {
          payload = value;
          return Promise.resolve({
            ready: true,
            display_name: "Trợ lý An",
            onboarding_phase: "test",
            onboarding_revision: 5,
          });
        },
      }),
    });
    await flush();
    const form = find(host, (node) => node.tagName === "FORM");
    assert.equal(input(form), null);
    submit(form);
    await flush();
    assert.deepEqual(payload, {
      values: {}, display_name: "Trợ lý An", require_complete: true, onboarding_revision: 4,
    });
  });
});

test("Persona malformed successors fail closed without entering Test", async (t) => {
  const invalid = [
    { ready: false, display_name: "Bé Mi", onboarding_phase: "test", onboarding_revision: 5 },
    { ready: true, display_name: "Tên khác", onboarding_phase: "test", onboarding_revision: 5 },
    { ready: true, display_name: "Bé Mi", onboarding_phase: "persona", onboarding_revision: 5 },
    { ready: true, display_name: "Bé Mi", onboarding_phase: "test", onboarding_revision: 4 },
    { ready: true, display_name: "Bé Mi", onboarding_phase: "test", onboarding_revision: 6 },
  ];
  for (const response of invalid) {
    await t.test(JSON.stringify(response), async (subtest) => {
      const { host } = mountPage(subtest, {
        initialStatus: personaStatus(),
        service: baseService({ saveAgent: () => Promise.resolve(response) }),
      });
      await flush();
      submit(find(host, (node) => node.tagName === "FORM"));
      await flush();
      assert.ok(byClass(host, "onboarding-error-stage"));
      assert.equal(byClass(host, "onboarding-test-stage"), null);
    });
  }
});

test("Back from Test reloads Agent and the frontend re-saves against the Test revision", async (t) => {
  let loads = 0;
  const saves = [];
  const { host } = mountPage(t, {
    service: baseService({
      loadAgent: () => {
        loads++;
        return Promise.resolve(agent({ display_name: "Tên mới" }));
      },
      testChat: (_message, revision) => Promise.resolve(testResult({
        answer: "Xin chào, mình là Tên mới.", bot_name: "Tên mới", revision: revision + 1,
      })),
      saveAgent(payload) {
        saves.push(payload);
        return Promise.resolve({
          ready: true, display_name: "Tên mới",
          onboarding_phase: "test", onboarding_revision: 9,
        });
      },
    }),
  });
  await flush();
  submit(find(host, (node) => node.tagName === "FORM"));
  await flush();
  button(host, "Quay lại chỉnh Persona").click();
  await flush();
  assert.equal(loads, 2);
  assert.match(text(host), /Trợ lý của bạn là ai/);
  assert.equal(input(host), null, "authoritative valid name is retained without a duplicate input");
  submit(find(host, (node) => node.tagName === "FORM"));
  await flush();
  assert.deepEqual(saves, [{
    values: {}, display_name: "Tên mới", require_complete: true, onboarding_revision: 8,
  }]);
  assert.equal(input(host).value, "Xin chào");
});

test("Test Chat starts with Xin chào, enforces 500 Unicode points, and is IME-safe", async (t) => {
  const calls = [];
  const gate = deferred();
  const { host } = mountPage(t, {
    service: baseService({
      testChat(message, revision, options) {
        calls.push({ message, revision, options });
        return gate.promise;
      },
    }),
  });
  await flush();
  const field = input(host);
  const form = find(host, (node) => node.tagName === "FORM");
  assert.equal(field.value, "Xin chào");
  assert.equal(field.hasAttribute("maxlength"), false);
  assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
  assertNoSkip(host);

  setInputValue(field, "🙂".repeat(501));
  submit(form);
  assert.equal(calls.length, 0);
  assert.equal(document.activeElement, field);
  setInputValue(field, "🙂".repeat(500));
  field.dispatchEvent({ type: "keydown", key: "Enter", isComposing: true, keyCode: 229 });
  assert.equal(calls.length, 0);
  field.dispatchEvent({ type: "keydown", key: "Enter", isComposing: false, keyCode: 13 });
  field.dispatchEvent({ type: "keydown", key: "Enter", isComposing: false, keyCode: 13 });
  assert.equal(calls.length, 1);
  assert.equal([...calls[0].message].length, 500);
  assert.deepEqual({ revision: calls[0].revision }, { revision: 7 });
  assert.ok(calls[0].options.signal instanceof AbortSignal);
  assert.equal(button(host, "Gửi thử").disabled, true);
  gate.resolve(testResult());
  await flush();
});

test("Test Chat validates receipt identity, revision, expiry, name, and every required field", async (t) => {
  const invalid = [
    ["empty answer", { answer: "" }, /chưa thể xác minh/i],
    ["wrong name", { bot_name: "Tên khác" }, /chưa áp dụng đúng Persona/i],
    ["wrong provider", { provider_id: "claude-code" }, /chưa thể xác minh/i],
    ["wrong model", { model_id: "other" }, /chưa thể xác minh/i],
    ["missing token", { test_token: "" }, /chưa thể xác minh/i],
    ["unchanged revision", { revision: 7 }, /chưa thể xác minh/i],
    ["jumped revision", { revision: 9 }, /chưa thể xác minh/i],
    ["invalid expiry", { expires_at: "tomorrow" }, /chưa thể xác minh/i],
    ["expired", { expires_at: new Date(Date.now() - 1_000).toISOString() }, /hết hạn/i],
  ];
  for (const [name, override, expected] of invalid) {
    await t.test(name, async (subtest) => {
      const { host } = mountPage(subtest, {
        service: baseService({ testChat: () => Promise.resolve(testResult(override)) }),
      });
      await flush();
      submit(find(host, (node) => node.tagName === "FORM"));
      await flush();
      assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
      assert.ok(button(host, "Thử lại"));
      assert.ok(button(host, "Quay lại chỉnh Persona"));
      assert.match(text(host), expected);
    });
  }
});

test("a successful receipt renders labeled bubbles but never auto-completes", async (t) => {
  let completes = 0;
  const { host } = mountPage(t, {
    service: baseService({ complete: () => { completes++; } }),
  });
  await flush();
  submit(find(host, (node) => node.tagName === "FORM"));
  await flush();

  assert.equal(completes, 0);
  assert.match(text(byClass(host, "onboarding-chat-user")), /Bạn.*Xin chào/u);
  assert.match(text(byClass(host, "onboarding-chat-bot")), /Bé Mi.*mình là Bé Mi/u);
  assert.equal(byClass(host, "onboarding-chat-transcript").getAttribute("aria-live"), "polite");
  assert.ok(button(host, "Ổn, dùng cấu hình này"));
  assertNoSkip(host);
});

test("editing, remounting, retrying, Back, rejection, and cancellation invalidate volatile receipts", async (t) => {
  await t.test("editing and remount", async (subtest) => {
    const { page, host } = mountPage(subtest);
    await flush();
    submit(find(host, (node) => node.tagName === "FORM"));
    await flush();
    setInputValue(input(host), "Câu khác");
    assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
    submit(find(host, (node) => node.tagName === "FORM"));
    await flush();
    assert.ok(button(host, "Ổn, dùng cấu hình này"));
    page.mount(host);
    assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
  });

  await t.test("failed retry and Back cancellation", async (subtest) => {
    const pending = deferred();
    let calls = 0;
    let abortedSignal;
    const { host } = mountPage(subtest, {
      service: baseService({
        testChat(_message, _revision, options) {
          calls++;
          if (calls === 1) return Promise.reject(new Error("timeout SECRET"));
          abortedSignal = options.signal;
          return pending.promise;
        },
      }),
    });
    await flush();
    submit(find(host, (node) => node.tagName === "FORM"));
    await flush();
    assert.doesNotMatch(text(host), /SECRET/u);
    button(host, "Thử lại").click();
    assert.equal(calls, 2);
    button(host, "Quay lại chỉnh Persona").click();
    assert.equal(abortedSignal.aborted, true);
    pending.resolve(testResult());
    await flush();
    assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
    assert.match(text(host), /Trợ lý của bạn là ai/);
  });

  await t.test("editing cancels pending Test and pending Complete", async (subtest) => {
    const testGate = deferred();
    const completeGate = deferred();
    let testSignal;
    let completeSignal;
    let tests = 0;
    const { host } = mountPage(subtest, {
      service: baseService({
        testChat(_message, revision, options) {
          tests++;
          testSignal = options.signal;
          return tests === 1 ? testGate.promise : Promise.resolve(testResult({ revision: revision + 1 }));
        },
        complete(_token, _revision, options) {
          completeSignal = options.signal;
          return completeGate.promise;
        },
      }),
    });
    await flush();
    submit(find(host, (node) => node.tagName === "FORM"));
    setInputValue(input(host), "Đổi khi đang gửi");
    assert.equal(testSignal.aborted, true);
    testGate.resolve(testResult());
    await flush();
    assert.equal(button(host, "Ổn, dùng cấu hình này"), null);

    submit(find(host, (node) => node.tagName === "FORM"));
    await flush();
    button(host, "Ổn, dùng cấu hình này").click();
    setInputValue(input(host), "Đổi khi đang hoàn tất");
    assert.equal(completeSignal.aborted, true);
    completeGate.resolve({ completed: true, onboarding_version: 1, combo_id: "late" });
    await flush();
    assert.doesNotMatch(text(host), /đã sẵn sàng/u);
    assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
  });
});

test("the raw test token never enters DOM attributes, storage, URL, status, or logs", async (t) => {
  const token = "TOKEN-SENTINEL-DO-NOT-RENDER";
  const storageCalls = [];
  const savedLocal = globalThis.localStorage;
  const savedSession = globalThis.sessionStorage;
  const savedLog = console.log;
  globalThis.localStorage = { setItem: (...args) => storageCalls.push(["local", ...args]) };
  globalThis.sessionStorage = { setItem: (...args) => storageCalls.push(["session", ...args]) };
  const logs = [];
  console.log = (...args) => logs.push(args);
  t.after(() => {
    console.log = savedLog;
    if (savedLocal === undefined) delete globalThis.localStorage;
    else globalThis.localStorage = savedLocal;
    if (savedSession === undefined) delete globalThis.sessionStorage;
    else globalThis.sessionStorage = savedSession;
  });
  const { host } = mountPage(t, {
    service: baseService({ testChat: () => Promise.resolve(testResult({ test_token: token })) }),
  });
  await flush();
  submit(find(host, (node) => node.tagName === "FORM"));
  await flush();

  assert.doesNotMatch(text(host), new RegExp(token));
  assert.doesNotMatch(domAttributes(host), new RegExp(token));
  assert.equal(storageCalls.length, 0);
  assert.equal(JSON.stringify(logs).includes(token), false);
  assert.equal(String(globalThis.location ?? "").includes(token), false);
});

test("Complete is manual, one-flight, validates success, and hands off once per CTA", async (t) => {
  for (const [label, destination] of [
    ["Vào Portal", "portal"],
    ["Thêm kiến thức ngay", "knowledge"],
  ]) {
    await t.test(destination, async (subtest) => {
      const completeGate = deferred();
      const calls = [];
      const handoffs = [];
      const { host } = mountPage(subtest, {
        service: baseService({
          complete(token, revision, options) {
            calls.push({ token, revision, options });
            return completeGate.promise;
          },
        }),
        onComplete: (payload) => handoffs.push(payload),
      });
      await flush();
      submit(find(host, (node) => node.tagName === "FORM"));
      await flush();
      const confirm = button(host, "Ổn, dùng cấu hình này");
      confirm.click();
      confirm.click();
      assert.equal(calls.length, 1);
      assert.deepEqual({ token: calls[0].token, revision: calls[0].revision }, {
        token: "opaque-test-token", revision: 8,
      });
      assert.ok(calls[0].options.signal instanceof AbortSignal);
      assert.equal(handoffs.length, 0);
      completeGate.resolve({ completed: true, onboarding_version: 1, combo_id: "combo-new" });
      await flush();
      assert.match(text(host), /Bé Mi đã sẵn sàng!/u);
      assert.match(text(host), /bổ sung tài liệu.*Kiến thức/u);
      const cta = button(host, label);
      cta.click();
      cta.click();
      button(host, destination === "portal" ? "Thêm kiến thức ngay" : "Vào Portal").click();
      assert.deepEqual(handoffs, [{ destination }]);
    });
  }
});

test("Complete recovery clears expired receipts, refreshes conflicts, and safely retries generic errors", async (t) => {
  await t.test("expired/test-required", async (subtest) => {
    for (const code of ["ONBOARDING_TEST_EXPIRED", "ONBOARDING_TEST_REQUIRED"]) {
      await subtest.test(code, async (caseTest) => {
        let completes = 0;
        const { host } = mountPage(caseTest, {
          service: baseService({
            complete() {
              completes++;
              return Promise.reject(Object.assign(new Error("secret"), { code, status: 409 }));
            },
          }),
        });
        await flush();
        submit(find(host, (node) => node.tagName === "FORM"));
        await flush();
        button(host, "Ổn, dùng cấu hình này").click();
        await flush();
        assert.equal(completes, 1);
        assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
        assert.ok(button(host, "Thử lại"));
        assert.doesNotMatch(text(host), /secret/u);
      });
    }
  });

  await t.test("revision/config conflict", async (subtest) => {
    for (const code of ["ONBOARDING_REVISION_CONFLICT", "ONBOARDING_CONFIGURATION_CHANGED"]) {
      await subtest.test(code, async (caseTest) => {
        let statuses = 0;
        const { host } = mountPage(caseTest, {
          service: baseService({
            complete: () => Promise.reject(Object.assign(new Error("conflict"), { code, status: 409 })),
            status() {
              statuses++;
              return Promise.resolve(testStatus({ revision: 20 }));
            },
          }),
        });
        await flush();
        submit(find(host, (node) => node.tagName === "FORM"));
        await flush();
        button(host, "Ổn, dùng cấu hình này").click();
        await flush();
        assert.equal(statuses, 1);
        assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
        assert.equal(input(host).value, "Xin chào");
      });
    }
  });

  await t.test("generic retry", async (subtest) => {
    let completes = 0;
    const { host } = mountPage(subtest, {
      service: baseService({
        complete() {
          completes++;
          if (completes === 1) return Promise.reject(new Error("TOKEN-SHOULD-STAY-PRIVATE"));
          return Promise.resolve({ completed: true, onboarding_version: 1, combo_id: "combo-ok" });
        },
      }),
    });
    await flush();
    submit(find(host, (node) => node.tagName === "FORM"));
    await flush();
    button(host, "Ổn, dùng cấu hình này").click();
    await flush();
    assert.ok(button(host, "Ổn, dùng cấu hình này"));
    assert.doesNotMatch(text(host), /TOKEN-SHOULD-STAY-PRIVATE/u);
    button(host, "Ổn, dùng cấu hình này").click();
    await flush();
    assert.equal(completes, 2);
    assert.match(text(host), /đã sẵn sàng/u);
  });
});

test("malformed Complete responses and stale disposal never render Done or call handoff", async (t) => {
  const invalid = [
    { completed: false, onboarding_version: 1, combo_id: "combo" },
    { completed: true, onboarding_version: 2, combo_id: "combo" },
    { completed: true, onboarding_version: 1, combo_id: "" },
  ];
  for (const response of invalid) {
    await t.test(JSON.stringify(response), async (subtest) => {
      let handoffs = 0;
      const { host } = mountPage(subtest, {
        service: baseService({ complete: () => Promise.resolve(response) }),
        onComplete: () => { handoffs++; },
      });
      await flush();
      submit(find(host, (node) => node.tagName === "FORM"));
      await flush();
      button(host, "Ổn, dùng cấu hình này").click();
      await flush();
      assert.doesNotMatch(text(host), /đã sẵn sàng/u);
      assert.equal(handoffs, 0);
    });
  }

  await t.test("disposed late success", async (subtest) => {
    const gate = deferred();
    let handoffs = 0;
    const { page, host } = mountPage(subtest, {
      service: baseService({ complete: () => gate.promise }),
      onComplete: () => { handoffs++; },
    });
    await flush();
    submit(find(host, (node) => node.tagName === "FORM"));
    await flush();
    button(host, "Ổn, dùng cấu hình này").click();
    page.dispose();
    gate.resolve({ completed: true, onboarding_version: 1, combo_id: "combo" });
    await flush();
    assert.equal(text(host), "");
    assert.equal(handoffs, 0);
  });
});

test("Test resume has no receipt and completed resume requires an authoritative Agent name", async (t) => {
  await t.test("test resume", async (subtest) => {
    const { host } = mountPage(subtest);
    await flush();
    assert.equal(input(host).value, "Xin chào");
    assert.equal(button(host, "Ổn, dùng cấu hình này"), null);
  });

  await t.test("completed resume", async (subtest) => {
    let loads = 0;
    const { host } = mountPage(subtest, {
      initialStatus: testStatus({ required: false, phase: "completed", revision: 9 }),
      service: baseService({
        loadAgent: () => {
          loads++;
          return Promise.resolve(agent({ display_name: "Trợ lý An" }));
        },
      }),
    });
    await flush();
    assert.equal(loads, 1);
    assert.match(text(host), /Trợ lý An đã sẵn sàng!/u);
  });
});
