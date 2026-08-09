import test from "node:test";
import assert from "node:assert/strict";

import {
  createMemoryPage,
  createMemoryService,
} from "../overlay/internal/webui/static/pages/memory.js";
import {
  find,
  findAll,
  installDOM,
  text,
} from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, className) => node.classList?.contains(className) ?? false;

function overviewFixture() {
  return {
    metrics: { memories: 2, threads: 1, lessons: 1 },
    threads: [
      {
        id: "thread-a", name: "Chị Lan", avatar: "", thread_type: "user",
        memory_count: 2, pinned_count: 1, preview: "Nhận hàng buổi sáng",
        updated_at: "2026-08-09T08:00:00Z",
      },
      {
        id: "thread-b", name: "Nhóm Đại lý", avatar: "", thread_type: "group",
        memory_count: 0, pinned_count: 0, preview: "",
        updated_at: "2026-08-08T08:00:00Z",
      },
    ],
  };
}

function detailFixture(threadID = "thread-a") {
  return {
    thread: {
      id: threadID,
      name: threadID === "thread-a" ? "Chị Lan" : "Nhóm Đại lý",
      memory_count: threadID === "thread-a" ? 2 : 0,
      pinned_count: threadID === "thread-a" ? 1 : 0,
    },
    revision: threadID === "thread-a" ? 4 : 0,
    synced_revision: threadID === "thread-a" ? 3 : 0,
    memories: threadID === "thread-a" ? [
      {
        id: 7, thread_id: threadID, text: "Nhận hàng buổi sáng", pinned: true,
        source: "agent", source_message_id: 21, source_preview: "Em nhận hàng buổi sáng",
        created_at: "2026-08-09T07:00:00Z", updated_at: "2026-08-09T07:00:00Z",
      },
      {
        id: 6, thread_id: threadID, text: "Ưu tiên trả lời ngắn", pinned: false,
        source: "operator", source_message_id: 0, source_preview: "",
        created_at: "2026-08-08T07:00:00Z", updated_at: "2026-08-08T07:00:00Z",
      },
    ] : [],
  };
}

function lessonFixture() {
  return {
    revision: 2,
    lessons: [{
      id: 3, thread_id: "thread-a", thread_name: "Chị Lan",
      bot_text: "Đơn sẽ tới ngay", better: "Em sẽ kiểm tra thời gian giao cụ thể",
      note: "Không hứa khi chưa có dữ liệu", pinned: true,
      created_at: "2026-08-09T06:00:00Z", updated_at: "2026-08-09T06:00:00Z",
    }],
  };
}

test("memory service encodes filters and uses exact read endpoints", async () => {
  const calls = [];
  const service = createMemoryService(async (path, options) => {
    calls.push({ path, options });
    return {};
  });

  await service.overview({ query: "chị Lan & Minh", pinned: true });
  await service.thread("group/a?b");
  await service.lessons({ query: "ngắn hơn", pinned: false });

  assert.equal(calls[0].path, "/memory?q=ch%E1%BB%8B+Lan+%26+Minh&pinned=true");
  assert.equal(calls[1].path, "/memory/threads/group%2Fa%3Fb");
  assert.equal(calls[2].path, "/memory/lessons?q=ng%E1%BA%AFn+h%C6%A1n");
  assert.ok(calls.every(({ options }) => options === undefined));
});

test("Memory page renders legacy metrics, semantic tabs, thread detail and provenance", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const requests = [];
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      requests.push({ path, options });
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/thread-a") return detailFixture();
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "Memory");
  assert.match(text(main), /Điều bot nhớ về từng cuộc trò chuyện/);
  const metrics = findAll(main, (node) => hasClass(node, "memory-metric"));
  assert.equal(metrics.length, 3);
  assert.deepEqual(metrics.map((metric) => text(find(metric, (node) => hasClass(node, "n")))), ["2", "1", "1"]);

  const tabs = findAll(main, (node) => node.getAttribute?.("role") === "tab");
  assert.equal(tabs.length, 2);
  assert.deepEqual(tabs.map(text), ["Người & nhóm", "Bài học chung"]);
  assert.equal(tabs[0].getAttribute("aria-selected"), "true");
  assert.equal(tabs[1].getAttribute("aria-selected"), "false");

  assert.match(text(main), /Chị Lan/);
  assert.match(text(main), /Nhận hàng buổi sáng/);
  assert.match(text(main), /Nguồn: tin nhắn “Em nhận hàng buổi sáng”/);
  assert.match(text(main), /Sẽ đồng bộ ở lượt Zalo kế tiếp · revision 4/);
  const open = find(main, (node) => node.tagName === "A" && text(node) === "Mở hội thoại");
  assert.equal(open.getAttribute("href"), "/zalo?thread=thread-a");
  assert.ok(find(main, (node) => node.getAttribute?.("aria-live") === "polite"));
  assert.ok(requests.every(({ options }) => options.signal instanceof AbortSignal));
});

test("Memory page switches to global lessons and keeps scope explicit", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const page = createMemoryPage({
    request: async (path) => {
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/thread-a") return detailFixture();
      if (path === "/memory/lessons") return lessonFixture();
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  page.mount(main);
  await flush();
  await flush();

  const lessonsTab = find(main, (node) => node.getAttribute?.("role") === "tab" && text(node) === "Bài học chung");
  lessonsTab.click();
  await flush();

  assert.equal(lessonsTab.getAttribute("aria-selected"), "false");
  const activeLessonsTab = find(main, (node) => node.getAttribute?.("role") === "tab" && text(node) === "Bài học chung");
  assert.equal(activeLessonsTab.getAttribute("aria-selected"), "true");
  assert.match(text(main), /Áp dụng cho mọi hội thoại/);
  assert.match(text(main), /Không hứa khi chưa có dữ liệu/);
  assert.match(text(main), /Em sẽ kiểm tra thời gian giao cụ thể/);
});

test("Memory page debounces server search and pinned filtering", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const requests = [];
  let scheduled = null;
  const page = createMemoryPage({
    request: async (path) => {
      requests.push(path);
      return path.startsWith("/memory/lessons") ? lessonFixture() : overviewFixture();
    },
    setTimeout: (callback) => { scheduled = callback; return 41; },
    clearTimeout: () => { scheduled = null; },
  });
  const main = document.createElement("main");
  page.mount(main);
  await flush();
  await flush();

  const search = find(main, (node) => node.tagName === "INPUT" && node.getAttribute("type") === "search");
  const beforeSearch = requests.length;
  search.value = "chị Lan & Minh";
  search.dispatchEvent({ type: "input" });
  assert.equal(requests.length, beforeSearch);
  scheduled();
  await flush();
  assert.equal(
    requests.filter((path) => path === "/memory" || path.startsWith("/memory?")).at(-1),
    "/memory?q=ch%E1%BB%8B+Lan+%26+Minh",
  );

  const pinned = find(main, (node) => node.tagName === "INPUT" && node.getAttribute("type") === "checkbox");
  pinned.checked = true;
  pinned.dispatchEvent({ type: "change" });
  await flush();
  assert.equal(
    requests.filter((path) => path === "/memory" || path.startsWith("/memory?")).at(-1),
    "/memory?q=ch%E1%BB%8B+Lan+%26+Minh&pinned=true",
  );
});

test("Memory page ignores stale detail and aborts requests on dispose", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  let resolveA;
  const signals = [];
  const page = createMemoryPage({
    request: (path, options = {}) => {
      signals.push(options.signal);
      if (path === "/memory") return Promise.resolve(overviewFixture());
      if (path === "/memory/threads/thread-a") return new Promise((resolve) => { resolveA = resolve; });
      if (path === "/memory/threads/thread-b") return Promise.resolve(detailFixture("thread-b"));
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  await flush();
  const threadB = find(main, (node) => hasClass(node, "memory-thread") && text(node).includes("Nhóm Đại lý"));
  threadB.click();
  await flush();
  resolveA(detailFixture("thread-a"));
  await flush();

  assert.match(text(main), /Chưa có ghi chú cho hội thoại này/);
  assert.doesNotMatch(text(main), /Nguồn: tin nhắn/);
  mounted.dispose();
  assert.ok(signals.every((signal) => signal.aborted));
});

test("Memory page renders a real empty state", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const page = createMemoryPage({
    request: async () => ({ metrics: { memories: 0, threads: 0, lessons: 0 }, threads: [] }),
  });
  const main = document.createElement("main");
  page.mount(main);
  await flush();

  assert.match(text(main), /Chưa có người hoặc nhóm nào/);
  const add = find(main, (node) => node.tagName === "BUTTON" && text(node).includes("Thêm ghi chú"));
  assert.equal(add.disabled, true);
});
