import test from "node:test";
import assert from "node:assert/strict";

import { AppAPIError } from "../overlay/internal/webui/static/core/api.js";
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

function installKeyDispatcher(doc) {
  const listeners = new Set();
  doc.addEventListener = (type, listener) => { if (type === "keydown") listeners.add(listener); };
  doc.removeEventListener = (type, listener) => { if (type === "keydown") listeners.delete(listener); };
  doc.dispatchKeydown = (event) => {
    for (const listener of [...listeners]) listener(event);
  };
  return { listenerCount: () => listeners.size };
}

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
  const memories = threadID === "thread-a" ? [
    {
      id: 7, thread_id: threadID, uid: threadID, memory_key: "profile.delivery",
      text: "Nhận hàng buổi sáng", category: "preference", confidence: 0.96,
      status: "active", pinned: true,
      source: "agent", source_message_id: 21, source_preview: "Em nhận hàng buổi sáng",
      expires_at: "2026-12-31T00:00:00Z",
      created_at: "2026-08-09T07:00:00Z", updated_at: "2026-08-09T07:00:00Z",
    },
    {
      id: 6, thread_id: threadID, uid: threadID, memory_key: "profile.reply-style",
      text: "Ưu tiên trả lời ngắn", category: "preference", confidence: 1,
      status: "active", pinned: false,
      source: "operator", source_message_id: 0, source_preview: "",
      expires_at: null,
      created_at: "2026-08-08T07:00:00Z", updated_at: "2026-08-08T07:00:00Z",
    },
  ] : [];
  return {
    thread: {
      id: threadID,
      name: threadID === "thread-a" ? "Chị Lan" : "Nhóm Đại lý",
      thread_type: threadID === "thread-a" ? "user" : "group",
      memory_count: threadID === "thread-a" ? 2 : 0,
      pinned_count: threadID === "thread-a" ? 1 : 0,
    },
    selected_uid: threadID === "thread-a" ? threadID : "",
    members: threadID === "thread-a"
      ? [{ uid: threadID, name: "Chị Lan", avatar: "", active: 2, pending: 0, expired: 0 }]
      : [],
    common: { active: 0, pending: 0, expired: 0 },
    common_revision: 0,
    subject_revision: threadID === "thread-a" ? 4 : 0,
    synced_common_revision: 0,
    synced_subject_revision: threadID === "thread-a" ? 3 : 0,
    session_subject_uid: threadID === "thread-a" ? threadID : "",
    synced: threadID !== "thread-a",
    active: memories,
    pending: [],
    expired: [],
    revision: threadID === "thread-a" ? 4 : 0,
    synced_revision: threadID === "thread-a" ? 3 : 0,
    memories,
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

test("memory service encodes filters and member scope using exact read endpoints", async () => {
  const calls = [];
  const service = createMemoryService(async (path, options) => {
    calls.push({ path, options });
    return {};
  });

  await service.overview({ query: "chị Lan & Minh", pinned: true });
  await service.thread("group/a?b");
  await service.thread("group/a?b", "u/1?x");
  await service.lessons({ query: "ngắn hơn", pinned: false });

  assert.equal(calls[0].path, "/memory?q=ch%E1%BB%8B+Lan+%26+Minh&pinned=true");
  assert.equal(calls[1].path, "/memory/threads/group%2Fa%3Fb");
  assert.equal(calls[2].path, "/memory/threads/group%2Fa%3Fb?uid=u%2F1%3Fx");
  assert.equal(calls[3].path, "/memory/lessons?q=ng%E1%BA%AFn+h%C6%A1n");
  assert.ok(calls.every(({ options }) => options === undefined));
});

test("memory service uses exact mutation endpoints and bodies", async () => {
  const calls = [];
  const service = createMemoryService(async (path, options) => {
    calls.push({ path, options });
    return {};
  });
  const threadBody = { text: "Giao sau 9 giờ", pinned: true };
  const lessonBody = {
    thread_id: "thread-a", bot_text: "Giao ngay", better: "Em sẽ kiểm tra", note: "Không hứa", pinned: false,
  };

  await service.createThread("group/a?b", threadBody);
  await service.updateThread("group/a?b", 7, threadBody);
  await service.deleteThread("group/a?b", 7);
  await service.createLesson(lessonBody);
  await service.updateLesson(3, lessonBody);
  await service.deleteLesson(3);

  assert.deepEqual(calls, [
    { path: "/memory/threads/group%2Fa%3Fb", options: { method: "POST", body: threadBody } },
    { path: "/memory/threads/group%2Fa%3Fb/7", options: { method: "PUT", body: threadBody } },
    { path: "/memory/threads/group%2Fa%3Fb/7", options: { method: "DELETE" } },
    { path: "/memory/lessons", options: { method: "POST", body: lessonBody } },
    { path: "/memory/lessons/3", options: { method: "PUT", body: lessonBody } },
    { path: "/memory/lessons/3", options: { method: "DELETE" } },
  ]);
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
  assert.match(text(main), /sẽ đồng bộ khi người này nhắn tiếp · revision 4/);
  const open = find(main, (node) => node.tagName === "A" && text(node) === "Mở hội thoại");
  assert.equal(open.getAttribute("href"), "/zalo?thread=thread-a");
  assert.ok(find(main, (node) => node.getAttribute?.("aria-live") === "polite"));
  assert.ok(requests.every(({ options }) => options.signal instanceof AbortSignal));
});

test("open conversation link encodes the selected thread", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const page = createMemoryPage({
    request: async (path) => {
      if (path === "/memory") {
        return {
          metrics: { memories: 1, threads: 1, lessons: 0 },
          threads: [{
            id: "group/a?b", name: "Nhóm A", avatar: "", thread_type: "group",
            memory_count: 1, pinned_count: 0, preview: "Ghi chú", updated_at: "2026-08-09T08:00:00Z",
          }],
        };
      }
      if (path === "/memory/threads/group%2Fa%3Fb") {
        return {
          thread: { id: "group/a?b", name: "Nhóm A", memory_count: 1, pinned_count: 0 },
          revision: 1,
          synced_revision: 1,
          memories: [{
            id: 1, thread_id: "group/a?b", text: "Ghi chú", pinned: false,
            source: "operator", created_at: "2026-08-09T08:00:00Z", updated_at: "2026-08-09T08:00:00Z",
          }],
        };
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  const link = find(main, (node) => node.tagName === "A" && text(node) === "Mở hội thoại");
  assert.equal(link.getAttribute("href"), "/zalo?thread=group%2Fa%3Fb");
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
  const lesson = find(main, (node) => hasClass(node, "memory-lesson"));
  assert.deepEqual(
    findAll(lesson, (node) => node.tagName === "BUTTON").map(text),
    ["Bỏ ghim", "Sửa", "Xoá"],
  );
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

test("memory thread edit sends trimmed content then refreshes detail", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  const calls = [];
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/thread-a/7" && options.method === "PUT") return detailFixture().memories[0];
      if (path === "/memory/threads/thread-a") return detailFixture();
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  const entry = find(main, (node) => hasClass(node, "memory-entry") && text(node).includes("Nhận hàng buổi sáng"));
  find(entry, (node) => node.tagName === "BUTTON" && text(node) === "Sửa").click();
  await flush();
  const overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  const textarea = find(overlay, (node) => node.tagName === "TEXTAREA" && node.getAttribute("name") === "text");
  textarea.value = "  nhận hàng sau 9 giờ  ";
  find(overlay, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();
  await flush();
  await flush();

  const update = calls.find(({ path, options }) => path === "/memory/threads/thread-a/7" && options.method === "PUT");
  assert.deepEqual(update.options.body, {
    uid: "thread-a", expected_revision: 4,
    memory_key: "profile.delivery", text: "nhận hàng sau 9 giờ", category: "preference", pinned: true,
  });
  assert.ok(update.options.signal instanceof AbortSignal);
  assert.ok(calls.filter(({ path, options }) => path === "/memory/threads/thread-a" && !options.method).length >= 2);
  assert.match(text(find(main, (node) => hasClass(node, "memory-live"))), /Đã lưu ghi chú/);
  assert.equal(find(document.body, (node) => node.hasAttribute?.("data-memory-overlay")), null);
});

test("memory can add a global lesson with trimmed fields", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  const calls = [];
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/thread-a") return detailFixture();
      if (path === "/memory/lessons" && options.method === "POST") return lessonFixture().lessons[0];
      if (path === "/memory/lessons") return lessonFixture();
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  find(main, (node) => node.getAttribute?.("role") === "tab" && text(node) === "Bài học chung").click();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && text(node).includes("Thêm bài học")).click();
  await flush();

  const overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  find(overlay, (node) => node.getAttribute?.("name") === "bot_text").value = "  Đơn sẽ tới ngay  ";
  find(overlay, (node) => node.getAttribute?.("name") === "better").value = "  Em sẽ kiểm tra lại  ";
  find(overlay, (node) => node.getAttribute?.("name") === "note").value = "  Không hứa trước  ";
  const pinned = find(overlay, (node) => node.getAttribute?.("name") === "pinned");
  pinned.checked = true;
  find(overlay, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();
  await flush();

  const create = calls.find(({ path, options }) => path === "/memory/lessons" && options.method === "POST");
  assert.deepEqual(create.options.body, {
    thread_id: "", bot_text: "Đơn sẽ tới ngay", better: "Em sẽ kiểm tra lại", note: "Không hứa trước", pinned: true,
  });
  assert.match(text(find(main, (node) => hasClass(node, "memory-live"))), /Đã thêm bài học/);
});

test("memory delete is confirmed and dialog supports keyboard focus lifecycle", async (t) => {
  const dom = installDOM();
  const keys = installKeyDispatcher(document);
  t.after(dom.restore);
  const calls = [];
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/thread-a/7" && options.method === "DELETE") return null;
      if (path === "/memory/threads/thread-a") return detailFixture();
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  const entry = find(main, (node) => hasClass(node, "memory-entry") && text(node).includes("Nhận hàng buổi sáng"));
  const opener = find(entry, (node) => node.tagName === "BUTTON" && text(node) === "Xoá");
  opener.click();
  await flush();
  let overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  const dialog = find(overlay, (node) => node.getAttribute?.("role") === "dialog");
  assert.equal(dialog.getAttribute("aria-modal"), "true");
  assert.ok(dialog.getAttribute("aria-labelledby"));
  assert.ok(dialog.getAttribute("aria-describedby"));
  assert.equal(calls.some(({ options }) => options.method === "DELETE"), false);
  const cancel = find(overlay, (node) => node.tagName === "BUTTON" && text(node) === "Huỷ");
  const confirm = find(overlay, (node) => node.tagName === "BUTTON" && text(node) === "Xác nhận xoá");
  confirm.focus();
  let prevented = 0;
  overlay.dispatchEvent({ type: "keydown", key: "Tab", preventDefault: () => { prevented++; } });
  assert.equal(prevented, 1);
  assert.equal(document.activeElement, cancel);
  cancel.focus();
  overlay.dispatchEvent({ type: "keydown", key: "Tab", shiftKey: true, preventDefault: () => { prevented++; } });
  assert.equal(document.activeElement, confirm);
  document.dispatchKeydown({ key: "Escape" });
  assert.equal(document.activeElement, opener);
  assert.equal(keys.listenerCount(), 0);

  opener.click();
  await flush();
  overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  find(overlay, (node) => node.tagName === "BUTTON" && text(node) === "Xác nhận xoá").click();
  await flush();
  await flush();
  assert.equal(calls.filter(({ options }) => options.method === "DELETE").length, 1);
  assert.equal(calls.find(({ options }) => options.method === "DELETE").path, "/memory/threads/thread-a/7");
  const restoredDelete = find(main, (node) => node.tagName === "BUTTON" && text(node) === "Xoá" &&
    node.getAttribute("data-memory-row-id") === "7");
  assert.equal(document.activeElement, restoredDelete);
  assert.notEqual(document.activeElement, opener);
  assert.match(text(find(main, (node) => hasClass(node, "memory-live"))), /Đã xoá ghi chú/);
});

test("memory pin failure keeps rendered data and announces the API message", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/thread-a/6" && options.method === "PUT") {
        throw new AppAPIError({
          status: 409,
          code: "memory_pin_limit",
          message: "đã đạt giới hạn nội dung được ghim",
        });
      }
      if (path === "/memory/threads/thread-a") return detailFixture();
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  const entry = find(main, (node) => hasClass(node, "memory-entry") && text(node).includes("Ưu tiên trả lời ngắn"));
  const pin = find(entry, (node) => node.tagName === "BUTTON" && text(node) === "Ghim");
  pin.click();
  await flush();

  assert.match(text(main), /Ưu tiên trả lời ngắn/);
  assert.match(text(find(main, (node) => hasClass(node, "memory-live"))), /đã đạt giới hạn nội dung được ghim/);
  assert.equal(find(main, (node) => node.tagName === "BUTTON" && text(node) === "Ghim").disabled, false);
});

test("memory keeps the last detail visible when post-mutation refresh fails", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  let detailReads = 0;
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/thread-a/6" && options.method === "PUT") return detailFixture().memories[1];
      if (path === "/memory/threads/thread-a") {
        detailReads++;
        if (detailReads > 1) throw new Error("không thể làm mới chi tiết");
        return detailFixture();
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  const entry = find(main, (node) => hasClass(node, "memory-entry") && text(node).includes("Ưu tiên trả lời ngắn"));
  find(entry, (node) => node.tagName === "BUTTON" && text(node) === "Ghim").click();
  await flush();
  await flush();
  await flush();

  assert.match(text(main), /Ưu tiên trả lời ngắn/);
  assert.match(text(main), /Không thể đọc Memory\. Vui lòng thử lại\./);
  assert.doesNotMatch(text(main), /không thể làm mới chi tiết/);
});

test("memory dispose aborts a pending mutation and ignores its late result", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  let resolveMutation;
  let mutationSignal;
  let overviewReads = 0;
  const page = createMemoryPage({
    request: (path, options = {}) => {
      if (path === "/memory") {
        overviewReads++;
        return Promise.resolve(overviewFixture());
      }
      if (path === "/memory/threads/thread-a/6" && options.method === "PUT") {
        mutationSignal = options.signal;
        return new Promise((resolve) => { resolveMutation = resolve; });
      }
      if (path === "/memory/threads/thread-a") return Promise.resolve(detailFixture());
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  await flush();
  await flush();
  const entry = find(main, (node) => hasClass(node, "memory-entry") && text(node).includes("Ưu tiên trả lời ngắn"));
  find(entry, (node) => node.tagName === "BUTTON" && text(node) === "Ghim").click();
  assert.equal(mutationSignal.aborted, false);
  mounted.dispose();
  assert.equal(mutationSignal.aborted, true);
  resolveMutation({});
  await flush();
  assert.equal(overviewReads, 1);
});
