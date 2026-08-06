import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { AppAPIError } from "../overlay/internal/webui/static/core/api.js";
import {
  addEntry,
  createModelService,
  createModelsPage,
  moveEntry,
  removeEntry,
  tailIndex,
} from "../overlay/internal/webui/static/pages/models.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const staticRoot = new URL("../overlay/internal/webui/static/", import.meta.url);
const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, className) => node.classList?.contains(className) ?? false;

function button(root, label) {
  return find(root, (node) => node.tagName === "BUTTON" && text(node) === label);
}

function rows(main) {
  return findAll(main, (node) => hasClass(node, "fact"));
}

function selects(row) {
  return findAll(row, (node) => node.tagName === "SELECT");
}

function optionIDs(select) {
  return select.children.map((option) => option.getAttribute("value"));
}

function toggle(row) {
  return find(row, (node) => node.getAttribute?.("type") === "checkbox");
}

function notice(main) {
  return find(main, (node) => node.getAttribute?.("aria-live") === "polite");
}

function chain(main) {
  return rows(main).map((row) => {
    const [provider, model] = selects(row);
    return `${provider.value}/${model.value}${toggle(row).checked ? "" : " (tắt)"}`;
  });
}

function choose(select, value) {
  select.value = value;
  select.dispatchEvent({ type: "change" });
}

const PROVIDERS = {
  kinds: [{ kind: "openai", label: "OpenAI", endpoint: "https://api.openai.com" }],
  providers: [
    {
      id: "openai-1",
      name: "OpenAI chính",
      kind: "openai",
      enabled: true,
      system: false,
      credential_configured: true,
      credential_unreadable: false,
      models: [
        { model_id: "gpt-5", name: "GPT-5", source: "discovered", available: true },
        { model_id: "gpt-5-mini", name: "GPT-5 mini", source: "discovered", available: true },
      ],
    },
    {
      id: "gemini",
      name: "Gemini",
      kind: "gemini",
      enabled: true,
      system: false,
      credential_configured: true,
      credential_unreadable: false,
      models: [{ model_id: "gemini-2", name: "Gemini 2", source: "discovered", available: true }],
    },
    {
      id: "anthropic-1",
      name: "Anthropic dự phòng",
      kind: "anthropic",
      enabled: false,
      system: false,
      credential_configured: true,
      credential_unreadable: false,
      models: [{ model_id: "claude-4", name: "Claude 4", source: "discovered", available: true }],
    },
    {
      id: "claude-code",
      name: "Claude Code",
      kind: "claude_code",
      enabled: true,
      system: true,
      credential_configured: false,
      credential_unreadable: false,
      models: [
        { model_id: "haiku", name: "Haiku", source: "manual", available: true },
        { model_id: "sonnet", name: "Sonnet", source: "manual", available: true },
      ],
    },
  ],
};

const ROUTE = {
  revision: 4,
  entries: [
    { position: 0, provider_id: "openai-1", model_id: "gpt-5", enabled: true },
    { position: 1, provider_id: "claude-code", model_id: "haiku", enabled: true },
  ],
};

const STATUS = {
  active_provider_id: "openai-1",
  active_model_id: "gpt-5",
  last_success_at: "2026-08-06T04:00:00Z",
  attempts: 12,
  fallbacks: 2,
};

// routeAPI trả một handler cho ba lượt đọc và lượt ghi chuỗi. Mặc định lượt PUT dội lại đúng
// thân vừa nhận kèm revision mới — máy chủ thật cũng trả snapshot vừa lưu.
function routeAPI({ providers = PROVIDERS, route = ROUTE, status = STATUS, save } = {}) {
  return (path, options = {}) => {
    if (path === "/llm/providers" && !options.method) return providers;
    if (path === "/llm/status" && !options.method) {
      return typeof status === "function" ? status() : status;
    }
    if (path === "/llm/route" && !options.method) {
      return typeof route === "function" ? route() : route;
    }
    if (path === "/llm/route" && options.method === "PUT") {
      if (save) return save(options.body);
      return {
        revision: options.body.revision + 1,
        entries: options.body.entries.map((entry, position) => ({ position, ...entry })),
      };
    }
    throw new Error(`Unexpected request: ${options.method || "GET"} ${path}`);
  };
}

// mountPage dựng trang với một request ghi lại mọi lượt gọi, và với bộ hẹn giờ giả để lượt hỏi
// trạng thái chạy đúng lúc test muốn thay vì theo đồng hồ thật.
//
// signal bị tách khỏi options đã ghi: trang gắn nó vào MỌI lượt gọi, nên để lại thì mọi phép so
// sánh thân request đều phải nhắc tới nó. Việc gắn signal được kiểm riêng ở test dispose.
function mountPage(t, handler) {
  const dom = installDOM();
  const calls = [];
  const timers = [];
  const cleared = [];
  const page = createModelsPage({
    request: async (path, options = {}) => {
      const { signal, ...recorded } = options;
      calls.push({ path, options: recorded });
      return handler(path, options);
    },
    setInterval: (fn) => {
      timers.push(fn);
      return timers.length;
    },
    clearInterval: (id) => cleared.push(id),
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(() => {
    mounted.dispose();
    dom.restore();
  });
  return {
    calls,
    cleared,
    main,
    mounted,
    timerCount: () => timers.length,
    async tick() {
      for (const fn of timers) fn();
      await flush();
    },
  };
}

async function mounted(t, handler = routeAPI()) {
  const harness = mountPage(t, handler);
  await flush();
  return harness;
}

// --- bản nháp: hàm thuần ---
//
// Kiểm thẳng chứ không qua DOM. Bất biến "Claude Code là mắt xích cuối" được canh hai lớp: nút
// disabled ở lớp DOM, và các hàm này ở lớp dữ liệu. Lớp DOM không kiểm hộ được lớp dưới — nút đã
// disabled thì không phát sự kiện, nên "bấm vào không có gì xảy ra" đúng dù cửa chặn có hay không.

const link = (providerID, modelID, enabled = true) => ({
  provider_id: providerID,
  model_id: modelID,
  enabled,
});
const CLAUDE_TAIL = link("claude-code", "haiku");

test("tailIndex locks the final link only when it is Claude Code", () => {
  assert.equal(tailIndex([link("openai-1", "gpt-5"), CLAUDE_TAIL]), 1);
  assert.equal(tailIndex([CLAUDE_TAIL, link("openai-1", "gpt-5")]), -1);
  assert.equal(tailIndex([]), -1);
});

test("removeEntry refuses to drop the locked tail", () => {
  const entries = [link("openai-1", "gpt-5"), CLAUDE_TAIL];
  assert.deepEqual(removeEntry(entries, 1), entries);
});

test("removeEntry drops any other link", () => {
  assert.deepEqual(
    removeEntry([link("openai-1", "gpt-5"), CLAUDE_TAIL], 0),
    [CLAUDE_TAIL],
  );
});

test("moveEntry refuses to move the locked tail", () => {
  const entries = [link("openai-1", "gpt-5"), CLAUDE_TAIL];
  assert.deepEqual(moveEntry(entries, 1, -1), entries);
});

test("moveEntry refuses to push a link past the locked tail", () => {
  const entries = [link("openai-1", "gpt-5"), CLAUDE_TAIL];
  assert.deepEqual(moveEntry(entries, 0, 1), entries);
});

test("moveEntry refuses to move off either end of the chain", () => {
  const entries = [link("openai-1", "gpt-5"), link("gemini", "gemini-2"), CLAUDE_TAIL];
  assert.deepEqual(moveEntry(entries, 0, -1), entries);
  assert.deepEqual(moveEntry(entries, 5, 1), entries);
});

test("moveEntry swaps neighbours without touching the original array", () => {
  const first = link("openai-1", "gpt-5");
  const second = link("gemini", "gemini-2");
  const entries = [first, second, CLAUDE_TAIL];

  assert.deepEqual(moveEntry(entries, 0, 1), [second, first, CLAUDE_TAIL]);
  assert.deepEqual(entries, [first, second, CLAUDE_TAIL]);
});

test("addEntry appends when the chain has no Claude Code tail", () => {
  const fresh = link("gemini", "gemini-2");
  const entries = [link("openai-1", "gpt-5")];

  assert.deepEqual(addEntry(entries, fresh), [link("openai-1", "gpt-5"), fresh]);
  assert.deepEqual(entries, [link("openai-1", "gpt-5")]);
});

test("addEntry inserts before the locked tail", () => {
  const fresh = link("gemini", "gemini-2");
  assert.deepEqual(
    addEntry([link("openai-1", "gpt-5"), CLAUDE_TAIL], fresh),
    [link("openai-1", "gpt-5"), fresh, CLAUDE_TAIL],
  );
});

test("model service reads providers, route, and status, and saves the whole chain", async () => {
  const calls = [];
  const service = createModelService(async (path, options = {}) => {
    calls.push({ path, options });
    return {};
  });

  await service.providers();
  await service.route();
  await service.status();
  await service.save({
    revision: 4,
    entries: [
      { provider_id: "openai-1", model_id: "gpt-5-mini", enabled: true },
      { provider_id: "claude-code", model_id: "sonnet", enabled: true },
    ],
  });

  assert.deepEqual(calls, [
    { path: "/llm/providers", options: {} },
    { path: "/llm/route", options: {} },
    { path: "/llm/status", options: {} },
    {
      path: "/llm/route",
      options: {
        method: "PUT",
        body: {
          revision: 4,
          entries: [
            { provider_id: "openai-1", model_id: "gpt-5-mini", enabled: true },
            { provider_id: "claude-code", model_id: "sonnet", enabled: true },
          ],
        },
      },
    },
  ]);
});

test("model service rejects a missing request function", () => {
  assert.throws(() => createModelService(null), TypeError);
});

test("Models renders the saved chain in order", async (t) => {
  const { main } = await mounted(t);

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "Models");
  assert.deepEqual(chain(main), ["openai-1/gpt-5", "claude-code/haiku"]);
});

test("adding a link inserts it above Claude Code", async (t) => {
  const { main } = await mounted(t);
  button(main, "Thêm mắt xích").click();

  assert.deepEqual(chain(main), ["openai-1/gpt-5", "openai-1/gpt-5", "claude-code/haiku"]);
});

test("adding a link says what is missing when no provider is switched on", async (t) => {
  const { main } = await mounted(t, routeAPI({
    providers: {
      kinds: PROVIDERS.kinds,
      providers: PROVIDERS.providers.map((entry) => ({ ...entry, enabled: false })),
    },
  }));
  button(main, "Thêm mắt xích").click();

  assert.equal(rows(main).length, 2);
  assert.match(text(notice(main)), /Chưa có Provider nào đang bật/);
});

test("removing a link drops it from the chain", async (t) => {
  const { main } = await mounted(t);
  button(rows(main)[0], "Xoá").click();

  assert.deepEqual(chain(main), ["claude-code/haiku"]);
});

test("switching a link off keeps it in the chain as disabled", async (t) => {
  const { calls, main } = await mounted(t);
  const box = toggle(rows(main)[0]);
  box.checked = false;
  box.dispatchEvent({ type: "change" });
  button(main, "Lưu").click();
  await flush();

  assert.deepEqual(chain(main), ["openai-1/gpt-5 (tắt)", "claude-code/haiku"]);
  assert.equal(calls.at(-1).options.body.entries[0].enabled, false);
});

test("choosing a provider offers only that provider's models and picks the first", async (t) => {
  const { main } = await mounted(t);
  const [provider, model] = selects(rows(main)[0]);
  choose(provider, "gemini");

  assert.deepEqual(optionIDs(model), ["gemini-2"]);
  assert.deepEqual(chain(main), ["gemini/gemini-2", "claude-code/haiku"]);
});

test("choosing a model keeps the provider and travels with the save", async (t) => {
  const { calls, main } = await mounted(t);
  choose(selects(rows(main)[0])[1], "gpt-5-mini");
  button(main, "Lưu").click();
  await flush();

  assert.deepEqual(calls.at(-1).options.body.entries[0], {
    provider_id: "openai-1",
    model_id: "gpt-5-mini",
    enabled: true,
  });
});

test("only enabled providers are offered as chain links", async (t) => {
  const { main } = await mounted(t);

  assert.deepEqual(optionIDs(selects(rows(main)[0])[0]), ["openai-1", "gemini", "claude-code"]);
});

test("a link on a disabled provider keeps its choice and says how to repair it", async (t) => {
  const { main } = await mounted(t, routeAPI({
    route: {
      revision: 4,
      entries: [
        { position: 0, provider_id: "anthropic-1", model_id: "claude-4", enabled: true },
        { position: 1, provider_id: "claude-code", model_id: "haiku", enabled: true },
      ],
    },
  }));

  const row = rows(main)[0];
  assert.equal(selects(row)[0].value, "anthropic-1");
  assert.match(text(find(row, (node) => hasClass(node, "fn"))), /Anthropic dự phòng đang tắt/);
});

test("a link on a model the provider no longer offers keeps it with a warning", async (t) => {
  const { main } = await mounted(t, routeAPI({
    route: {
      revision: 4,
      entries: [
        { position: 0, provider_id: "openai-1", model_id: "gpt-4-legacy", enabled: true },
        { position: 1, provider_id: "claude-code", model_id: "haiku", enabled: true },
      ],
    },
  }));

  const row = rows(main)[0];
  assert.equal(selects(row)[1].value, "gpt-4-legacy");
  assert.ok(optionIDs(selects(row)[1]).includes("gpt-4-legacy"));
  assert.match(text(find(row, (node) => hasClass(node, "fn"))), /gpt-4-legacy không còn dùng được/);
});

// threeLinkChain: hai mắt xích dời được cộng lưới an toàn — chuỗi ngắn nhất mà Lên và Xuống đều
// có việc để làm.
function threeLinkChain() {
  return routeAPI({
    route: {
      revision: 4,
      entries: [
        { position: 0, provider_id: "openai-1", model_id: "gpt-5", enabled: true },
        { position: 1, provider_id: "gemini", model_id: "gemini-2", enabled: true },
        { position: 2, provider_id: "claude-code", model_id: "haiku", enabled: true },
      ],
    },
  });
}

test("Down swaps a link with the one below it", async (t) => {
  const { main } = await mounted(t, threeLinkChain());
  button(rows(main)[0], "Xuống").click();

  assert.deepEqual(chain(main), ["gemini/gemini-2", "openai-1/gpt-5", "claude-code/haiku"]);
});

test("Up swaps a link with the one above it", async (t) => {
  const { main } = await mounted(t, threeLinkChain());
  button(rows(main)[1], "Lên").click();

  assert.deepEqual(chain(main), ["gemini/gemini-2", "openai-1/gpt-5", "claude-code/haiku"]);
});

test("the first link cannot move up and the last movable link cannot move down", async (t) => {
  const { main } = await mounted(t);
  const row = rows(main)[0];

  assert.equal(button(row, "Lên").disabled, true);
  assert.equal(button(row, "Xuống").disabled, true);
});

// Nút gốc là <button>, nên bàn phím kích hoạt được mà không cần thêm gì; điều KHÔNG tự có là chỗ
// đứng của con trỏ sau lượt vẽ lại — mất nó thì bấm Xuống hai lần liên tiếp là bất khả thi.
test("reorder controls are native buttons the keyboard can activate", async (t) => {
  const { main } = await mounted(t);
  const row = rows(main)[0];

  for (const label of ["Lên", "Xuống", "Xoá"]) {
    assert.equal(button(row, label).tagName, "BUTTON");
    assert.equal(button(row, label).getAttribute("type"), "button");
  }
});

// assertFocusLandedIn đòi con trỏ nằm TRONG hàng vừa dời và trên một nút còn bấm được. Khẳng
// định nó dừng ở đúng nút vừa bấm là sai: ở cả hai đích thường gặp nút đó đã tắt, và trình duyệt
// thật bỏ qua focus() trên nút tắt rồi thả con trỏ về <body>.
function assertFocusLandedIn(row, what) {
  const active = document.activeElement;
  assert.ok(active, `${what} must leave focus somewhere`);
  assert.equal(find(row, (node) => node === active), active, `${what} must leave focus in the moved row`);
  assert.equal(active.disabled, false, `${what} must leave focus on a usable control`);
}

test("focus follows a link moved down into the slot above the safety net", async (t) => {
  const { main } = await mounted(t, threeLinkChain());
  button(rows(main)[0], "Xuống").click();

  assertFocusLandedIn(rows(main)[1], "moving a link down");
});

test("focus follows a link moved up to the top of the chain", async (t) => {
  const { main } = await mounted(t, threeLinkChain());
  button(rows(main)[1], "Lên").click();

  assertFocusLandedIn(rows(main)[0], "moving a link up");
});

test("the running provider and model wear a badge", async (t) => {
  const { main } = await mounted(t);

  assert.equal(text(find(rows(main)[0], (node) => hasClass(node, "live"))), "đang chạy");
  assert.equal(text(find(rows(main)[1], (node) => hasClass(node, "live"))), "");
});

test("the badge leaves a link that was repointed at another provider", async (t) => {
  const { main } = await mounted(t);
  const badge = (row) => text(find(row, (node) => hasClass(node, "live")));
  assert.equal(badge(rows(main)[0]), "đang chạy");

  choose(selects(rows(main)[0])[0], "gemini");

  assert.equal(badge(rows(main)[0]), "");
});

test("the status line explains a chain where no badge is showing", async (t) => {
  const { main } = await mounted(t, routeAPI({
    status: { ...STATUS, active_provider_id: "", active_model_id: "" },
  }));

  assert.match(text(find(main, (node) => hasClass(node, "chainstatus"))), /Chưa có lượt gọi nào/);
});

test("the status line stays quiet while a badge is doing the talking", async (t) => {
  const { main } = await mounted(t);

  assert.equal(text(find(main, (node) => hasClass(node, "chainstatus"))), "");
});

test("a failed status poll says so instead of leaving the chain looking idle", async (t) => {
  const backing = routeAPI();
  const { main } = await mounted(t, (path, options = {}) => {
    if (path === "/llm/status") throw new Error("không đọc được trạng thái");
    return backing(path, options);
  });

  assert.match(text(find(main, (node) => hasClass(node, "chainstatus"))), /không đọc được trạng thái/);
});

test("a status poll failure survives an unrelated edit to the chain", async (t) => {
  const backing = routeAPI();
  const { main } = await mounted(t, (path, options = {}) => {
    if (path === "/llm/status") throw new Error("không đọc được trạng thái");
    return backing(path, options);
  });
  choose(selects(rows(main)[0])[0], "gemini");

  assert.match(text(find(main, (node) => hasClass(node, "chainstatus"))), /không đọc được trạng thái/);
});

// Mặt còn lại của statusError: giữ câu lỗi qua mọi lượt sửa bản nháp mà không xoá nó khi lượt đọc
// đã lành lại thì tệ hơn cái nó thay thế — một lượt hỏng duy nhất sẽ ghim câu lỗi tới hết đời
// trang, đè lên mọi lượt đọc thành công về sau.
test("a status poll that recovers clears the error it left behind", async (t) => {
  let broken = true;
  const backing = routeAPI();
  const { main, tick } = await mounted(t, (path, options = {}) => {
    if (path === "/llm/status" && broken) throw new Error("không đọc được trạng thái");
    return backing(path, options);
  });
  assert.match(text(find(main, (node) => hasClass(node, "chainstatus"))), /không đọc được trạng thái/);

  broken = false;
  await tick();

  assert.equal(
    text(find(main, (node) => hasClass(node, "chainstatus"))),
    "",
    "a recovered poll must take its old error off the screen",
  );
});

test("a chain with nothing enabled is refused before it reaches the API", async (t) => {
  const { calls, main } = await mounted(t, routeAPI({ route: { revision: 0, entries: [] } }));
  button(main, "Lưu").click();
  await flush();

  assert.equal(calls.some(({ options }) => options.method === "PUT"), false);
  assert.match(text(notice(main)), /ít nhất một mắt xích đang bật/);
});

test("a link with no model chosen is refused before it reaches the API", async (t) => {
  const { calls, main } = await mounted(t, routeAPI({
    route: {
      revision: 4,
      entries: [
        { position: 0, provider_id: "openai-1", model_id: "", enabled: true },
        { position: 1, provider_id: "claude-code", model_id: "haiku", enabled: true },
      ],
    },
  }));
  button(main, "Lưu").click();
  await flush();

  assert.equal(calls.some(({ options }) => options.method === "PUT"), false);
  assert.match(text(notice(main)), /Chọn model cho OpenAI chính/);
});

test("Claude Code stays enabled and cannot be switched off", async (t) => {
  const { main } = await mounted(t);
  const box = toggle(rows(main)[1]);

  assert.equal(box.checked, true);
  assert.equal(box.disabled, true);
});

test("Claude Code cannot be removed or moved", async (t) => {
  const { main } = await mounted(t);
  const row = rows(main)[1];

  // Chỉ khẳng định nút đã tắt. Bấm thêm rồi soi chuỗi là vô nghĩa: nút tắt không phát sự kiện,
  // nên phép khẳng định đó đúng nhờ dòng ngay trên chứ không nhờ cửa chặn trong removeEntry —
  // cửa chặn ấy có bài kiểm riêng gọi thẳng hàm.
  for (const label of ["Lên", "Xuống", "Xoá"]) {
    assert.equal(button(row, label).disabled, true, `Claude Code must not offer "${label}"`);
  }
});

test("Claude Code stays the final link when a new one is added", async (t) => {
  const { main } = await mounted(t);
  button(main, "Thêm mắt xích").click();
  button(main, "Thêm mắt xích").click();

  assert.equal(chain(main).at(-1), "claude-code/haiku");
});

// Bất biến gắn với mắt xích CUỐI, không với mọi mắt xích mang tên Claude Code: thử haiku trước
// rồi rơi xuống sonnet là một chuỗi hợp lệ, và mắt xích haiku ở giữa phải sửa được như mọi cái khác.
test("a Claude Code link that is not the last one behaves like any other", async (t) => {
  const { main } = await mounted(t, routeAPI({
    route: {
      revision: 4,
      entries: [
        { position: 0, provider_id: "claude-code", model_id: "sonnet", enabled: true },
        { position: 1, provider_id: "openai-1", model_id: "gpt-5", enabled: true },
        { position: 2, provider_id: "claude-code", model_id: "haiku", enabled: true },
      ],
    },
  }));
  const row = rows(main)[0];

  assert.equal(button(row, "Xoá").disabled, false);
  assert.equal(toggle(row).disabled, false);
  assert.equal(selects(row)[0].disabled, false);
  button(row, "Xuống").click();
  assert.deepEqual(chain(main), ["openai-1/gpt-5", "claude-code/sonnet", "claude-code/haiku"]);
});

test("the Claude Code provider is fixed while its model is not", async (t) => {
  const { main } = await mounted(t);
  const [provider, model] = selects(rows(main)[1]);

  assert.equal(provider.disabled, true);
  assert.equal(model.disabled, false);
  assert.deepEqual(optionIDs(model), ["haiku", "sonnet"]);
});

test("a successful save sends the whole draft with the loaded revision", async (t) => {
  const { calls, main } = await mounted(t);
  button(main, "Lưu").click();
  await flush();

  assert.deepEqual(calls.at(-1), {
    path: "/llm/route",
    options: {
      method: "PUT",
      body: {
        revision: 4,
        entries: [
          { provider_id: "openai-1", model_id: "gpt-5", enabled: true },
          { provider_id: "claude-code", model_id: "haiku", enabled: true },
        ],
      },
    },
  });
  assert.match(text(notice(main)), /Đã lưu/);
});

test("a second save carries the revision the server returned", async (t) => {
  const { calls, main } = await mounted(t);
  button(main, "Lưu").click();
  await flush();
  button(main, "Lưu").click();
  await flush();

  assert.equal(calls.at(-1).options.body.revision, 5);
});

// Không khởi động lại, không tải lại trang, và không đọc lại chuỗi: lượt PUT đã trả về snapshot
// vừa lưu. Một lượt GET /llm/route thứ hai ở đây nghĩa là trang đang vứt bản nháp đi rồi đọc lại.
test("a successful save reloads neither the route nor the provider list", async (t) => {
  const { calls, main } = await mounted(t);
  const before = calls.length;
  button(main, "Lưu").click();
  await flush();

  assert.deepEqual(calls.slice(before).map(({ path, options }) => `${options.method || "GET"} ${path}`), [
    "PUT /llm/route",
  ]);
});

test("an edit made while the save is in flight is not thrown away", async (t) => {
  let release = () => {};
  const backing = routeAPI();
  const harness = mountPage(t, (path, options = {}) => {
    if (path === "/llm/route" && options.method === "PUT") {
      return new Promise((resolve) => {
        release = () => resolve({ revision: 5, entries: options.body.entries });
      });
    }
    return backing(path, options);
  });
  await flush();
  button(harness.main, "Lưu").click();
  await flush();
  button(harness.main, "Thêm mắt xích").click();
  release();
  await flush();

  // Đọc THÂN của lượt lưu kế tiếp, không đọc màn hình: nhánh lưu thành công cố ý không vẽ lại,
  // nên hàng vừa thêm còn nằm đó dù bản nháp phía sau đã bị thay hay chưa — nhìn DOM ở đây là
  // nhìn vào kết quả của cú bấm Thêm, không phải vào bản nháp.
  button(harness.main, "Lưu").click();
  await flush();

  assert.equal(
    harness.calls.at(-1).options.body.entries.length,
    3,
    "the second save must still carry the link added while the first was in flight",
  );
});

test("a revision conflict keeps the draft and its controls exactly as they were", async (t) => {
  const { main } = await mounted(t, routeAPI({
    save: () => {
      throw new AppAPIError({
        code: "ROUTE_REVISION_CONFLICT",
        message: "Chuỗi đã được lưu ở nơi khác trong lúc bạn đang sửa",
        status: 409,
      });
    },
  }));
  choose(selects(rows(main)[0])[1], "gpt-5-mini");
  button(main, "Thêm mắt xích").click();
  const before = rows(main);
  button(main, "Lưu").click();
  await flush();

  // Chính những NODE cũ, không phải những node mới mang giá trị y hệt: vẽ lại đúng bản nháp cho
  // ra một màn hình trông không đổi nhưng ném mất chỗ đứng của con trỏ và vị trí cuộn.
  assert.equal(rows(main).length, before.length);
  assert.ok(rows(main).every((row, index) => row === before[index]), "the 409 branch must not rebuild the rows");
  assert.deepEqual(chain(main), ["openai-1/gpt-5-mini", "openai-1/gpt-5", "claude-code/haiku"]);
  assert.match(text(notice(main)), /lưu ở nơi khác/);
  assert.ok(button(main, "Tải lại chuỗi"));
  // Các nút của hàng vẫn sống: bản nháp còn sửa được sau lượt hỏng, không phải chỉ còn nhìn.
  button(rows(main)[1], "Xoá").click();
  assert.deepEqual(chain(main), ["openai-1/gpt-5-mini", "claude-code/haiku"]);
});

test("a conflicted draft survives until the reload button is pressed", async (t) => {
  let reads = 0;
  const { main } = await mounted(t, routeAPI({
    route: () => {
      reads += 1;
      if (reads === 1) return ROUTE;
      return {
        revision: 9,
        entries: [
          { position: 0, provider_id: "gemini", model_id: "gemini-2", enabled: true },
          { position: 1, provider_id: "claude-code", model_id: "sonnet", enabled: true },
        ],
      };
    },
    save: () => {
      throw new AppAPIError({ code: "ROUTE_REVISION_CONFLICT", message: "xung đột", status: 409 });
    },
  }));
  button(main, "Thêm mắt xích").click();
  button(main, "Lưu").click();
  await flush();
  assert.equal(chain(main).length, 3);

  button(main, "Tải lại chuỗi").click();
  await flush();

  assert.deepEqual(chain(main), ["gemini/gemini-2", "claude-code/sonnet"]);
  assert.equal(button(main, "Tải lại chuỗi"), null);
});

test("a reloaded chain saves against the revision it just read", async (t) => {
  let reads = 0;
  const { calls, main } = await mounted(t, routeAPI({
    route: () => {
      reads += 1;
      return { revision: reads === 1 ? 4 : 9, entries: ROUTE.entries };
    },
    save: (body) => {
      if (body.revision === 4) {
        throw new AppAPIError({ code: "ROUTE_REVISION_CONFLICT", message: "xung đột", status: 409 });
      }
      return { revision: body.revision + 1, entries: ROUTE.entries };
    },
  }));
  button(main, "Lưu").click();
  await flush();
  button(main, "Tải lại chuỗi").click();
  await flush();
  button(main, "Lưu").click();
  await flush();

  assert.equal(calls.at(-1).options.body.revision, 9);
  assert.match(text(notice(main)), /Đã lưu/);
});

test("a rejected chain shows the reason the server gave", async (t) => {
  const { main } = await mounted(t, routeAPI({
    save: () => {
      throw new AppAPIError({
        code: "ROUTE_INVALID",
        message: "Chuỗi không hợp lệ: route trỏ tới Provider đang tắt: gemini",
        status: 422,
      });
    },
  }));
  button(main, "Lưu").click();
  await flush();

  assert.match(text(notice(main)), /route trỏ tới Provider đang tắt: gemini/);
  assert.equal(button(main, "Tải lại chuỗi"), null);
});

test("a failed load renders the shared error panel", async (t) => {
  const { main } = await mounted(t, () => {
    throw new Error("không đọc được chuỗi fallback");
  });

  assert.match(text(find(main, (node) => hasClass(node, "banner"))), /không đọc được chuỗi fallback/);
});

test("the status poll refreshes the running badge without redrawing the chain", async (t) => {
  let active = "openai-1";
  const { main, tick } = await mounted(t, routeAPI({
    status: () => ({ ...STATUS, active_provider_id: active, active_model_id: active === "openai-1" ? "gpt-5" : "haiku" }),
  }));
  const rowBefore = rows(main)[0];
  active = "claude-code";
  await tick();

  assert.equal(text(find(rows(main)[1], (node) => hasClass(node, "live"))), "đang chạy");
  assert.equal(rows(main)[0], rowBefore);
});

test("the status poll never re-reads the provider list", async (t) => {
  const { calls, tick } = await mounted(t);
  await tick();
  await tick();

  assert.equal(calls.filter(({ path }) => path === "/llm/providers").length, 1);
});

test("dispose stops the status poll", async (t) => {
  const { calls, cleared, mounted: page, tick } = await mounted(t);
  const before = calls.length;
  page.dispose();
  await tick();

  assert.deepEqual(cleared, [1]);
  assert.equal(calls.length, before);
});

test("dispose aborts the in-flight route request", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const signals = [];
  const page = createModelsPage({
    request: (path, options = {}) => {
      signals.push(options.signal);
      return new Promise(() => {});
    },
    setInterval: () => 1,
    clearInterval: () => {},
  });
  const main = document.createElement("main");
  const instance = page.mount(main);
  await flush();

  assert.equal(signals.length > 0, true);
  assert.equal(signals[0].aborted, false);
  instance.dispose();
  assert.equal(signals.every((signal) => signal.aborted), true);
});

test("a load resolving after disposal never renders the chain", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const pending = [];
  const page = createModelsPage({
    request: () => new Promise((resolve) => pending.push(resolve)),
    setInterval: () => 1,
    clearInterval: () => {},
  });
  const main = document.createElement("main");
  const instance = page.mount(main);
  await flush();
  instance.dispose();
  pending[0](PROVIDERS);
  pending[1](ROUTE);
  await flush();

  assert.equal(rows(main).length, 0);
});

// Ở bề ngang điện thoại mỗi hàng xếp dọc, nên từng ô phải tự nói nó là gì; nhãn cũng chính là
// thứ trình đọc màn hình đọc lên cho ô chọn.
test("every control in a row carries its own label", async (t) => {
  const { main } = await mounted(t);
  const row = rows(main)[0];
  const labels = findAll(row, (node) => node.tagName === "LABEL");

  assert.deepEqual(labels.map(text), ["Provider", "Model", "Bật"]);
  const controls = [...selects(row), toggle(row)];
  assert.deepEqual(labels.map((label) => label.getAttribute("for")), controls.map((node) => node.getAttribute("id")));
  assert.equal(new Set(controls.map((node) => node.getAttribute("id"))).size, 3);
});

test("row control ids stay unique across the whole chain", async (t) => {
  const { main } = await mounted(t);
  button(main, "Thêm mắt xích").click();
  const ids = findAll(main, (node) => node.getAttribute?.("id")).map((node) => node.getAttribute("id"));

  assert.equal(new Set(ids).size, ids.length);
});

// modelRegions lấy phần thân giữa mỗi cặp dấu models:begin/end.
//
// Cắt theo DẤU chứ không lọc theo chữ "models" trong selector: một luật xổng phạm vi là một luật
// KHÔNG còn chữ đó, nên lọc theo tên chỉ soi được đúng những luật vốn đã đúng.
function modelRegions(css) {
  return [...css.matchAll(/\/\* models:begin[\s\S]*?\*\/([\s\S]*?)\/\* models:end \*\//g)]
    .map(([, body]) => body);
}

function selectorsIn(region) {
  return region
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .split("}")
    .map((rule) => rule.split("{")[0].trim())
    .filter(Boolean)
    .flatMap((list) => list.split(",").map((part) => part.trim()).filter(Boolean));
}

test("every rule in the Models CSS regions stays scoped to the page", async () => {
  const css = await readFile(new URL("portal.css", staticRoot), "utf8");

  const regions = modelRegions(css);
  assert.equal(regions.length, 2, "expected a desktop and a mobile Models region");

  const selectors = regions.flatMap(selectorsIn);
  assert.ok(selectors.length >= 8, `expected the Models rules inside the markers, got ${selectors.length}`);
  for (const selector of selectors) {
    assert.match(selector, /^\.models-page\b/, `Models rule "${selector}" must stay scoped`);
  }
});

// Nửa còn lại của "nhãn cho hàng ở bề ngang điện thoại": nhãn chỉ CẦN tồn tại vì hàng xếp dọc ở
// đó. Phép kiểm phạm vi bên trên vẫn xanh khi cả khối mobile bị xoá sạch, nên nó không canh được
// điều này. Chỉ hai luật lưới bị canh — luật màu chữ, cỡ chữ thì không, canh chúng chỉ tạo báo
// động giả mỗi lần chỉnh mỹ thuật.
test("the Models CSS lays rows out in columns and stacks them at phone width", async () => {
  const [desktop, mobile] = modelRegions(await readFile(new URL("portal.css", staticRoot), "utf8"));

  assert.match(desktop, /\.models-page \.fact\s*\{[^}]*grid-template-columns:\s*auto/s);
  assert.match(mobile, /\.models-page \.fact\s*\{[^}]*grid-template-columns:\s*1fr/s);
});

// Nửa Models của cửa chặn nguồn trong providers.test.mjs. Trang này không có ô nhập khoá, nhưng nó
// đọc cùng danh sách Provider và cũng được nhúng vào agentdc.exe — một khoá dán vào đây rời khỏi
// máy y hệt, và không phép kiểm nào khác của tầng này nhìn vào nội dung tệp nguồn.
test("the Models page source carries no API-key literal", async () => {
  const source = await readFile(new URL("pages/models.js", staticRoot), "utf8");

  const found = source.match(/\b(?:sk-|xai-|gsk_|AIza)[A-Za-z0-9_-]{8,}/g) ?? [];
  assert.equal(found.length, 0, `pages/models.js carries ${found.length} key-shaped literal(s)`);
});
