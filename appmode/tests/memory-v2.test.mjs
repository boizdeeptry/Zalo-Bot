import test from "node:test";
import assert from "node:assert/strict";

import { createMemoryPage } from "../overlay/internal/webui/static/pages/memory.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, className) => node.classList?.contains(className) ?? false;

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function overviewFixture(threads = [groupThread("group-a", "Nhóm Đại lý")]) {
  return {
    metrics: { memories: 7, threads: threads.length, lessons: 1 },
    threads,
  };
}

function groupThread(id, name) {
  return {
    id, name, avatar: "", thread_type: "group",
    memory_count: 7, pinned_count: 1, preview: "Ghi nhớ chung",
    updated_at: "2026-08-10T08:00:00Z",
  };
}

function memory({
  id, uid = "uid-lan-opaque", text: value, category = "profile", confidence = 0.96,
  status = "active", source = "agent", sourcePreview = "Khách đã xác nhận",
  expiresAt = "2026-08-31T00:00:00Z", proposalAction = "", supersedesID = 0,
  proposalTarget,
}) {
  return {
    id, thread_id: "group-a", thread_name: "Nhóm Đại lý", uid,
    memory_key: "profile.opaque-key", text: value, category, confidence, status,
    proposal_action: proposalAction, supersedes_id: supersedesID,
    ...(proposalTarget ? { proposal_target: proposalTarget } : {}),
    pinned: false, source, source_message_id: 991, source_preview: sourcePreview,
    last_confirmed_at: "2026-08-10T01:00:00Z", expires_at: expiresAt,
    created_at: "2026-08-10T01:00:00Z", updated_at: "2026-08-10T01:00:00Z",
  };
}

const members = [
  { uid: "uid-lan-opaque", name: "Chị Lan", avatar: "", active: 1, pending: 1, expired: 1 },
  { uid: "uid-minh-opaque", name: "Anh Minh", avatar: "", active: 1, pending: 0, expired: 0 },
  { uid: "uid-empty-opaque", name: "Cô Mai", avatar: "", active: 0, pending: 0, expired: 0 },
];

function detailFixture(uid = "") {
  const base = {
    thread: { ...groupThread("group-a", "Nhóm Đại lý") },
    selected_uid: uid,
    members,
    common: { active: 1, pending: 2, expired: 0 },
    common_revision: 6,
    subject_revision: uid ? 9 : 0,
    synced_common_revision: 6,
    synced_subject_revision: uid === "uid-minh-opaque" ? 9 : 8,
    session_subject_uid: uid === "uid-minh-opaque" ? uid : "uid-previous-speaker",
    synced: uid === "" || uid === "uid-minh-opaque",
    revision: uid ? 9 : 6,
    synced_revision: uid === "uid-minh-opaque" ? 9 : uid ? 8 : 6,
  };
  if (uid === "") {
    return {
      ...base,
      active: [memory({ id: 11, uid: "", text: "GROUP-COMMON-MEMORY", category: "preference" })],
      pending: [], expired: [],
    };
  }
  if (uid === "uid-lan-opaque") {
    const previous = memory({
      id: 42, text: "Khách dùng địa chỉ giao hàng cũ", category: "address",
      confidence: 0.91, source: "operator", sourcePreview: "", expiresAt: "2026-08-30T00:00:00Z",
    });
    return {
      ...base,
      active: [memory({ id: 71, text: "LAN-ACTIVE-MEMORY", category: "profile" })],
      pending: [memory({
        id: 73, text: "Khách đề nghị giao tới địa chỉ mới", category: "address",
        confidence: 0.72, status: "pending", proposalAction: "replace", supersedesID: 42,
        proposalTarget: previous,
      })],
      expired: [memory({
        id: 74, text: "LAN-EXPIRED-MEMORY", category: "interest", confidence: 0.83,
        status: "expired", expiresAt: "2026-08-09T00:00:00Z",
      })],
    };
  }
  if (uid === "uid-minh-opaque") {
    return {
      ...base,
      active: [memory({ id: 81, uid, text: "U2-PRIVATE-MEMORY", category: "financial" })],
      pending: [], expired: [],
    };
  }
  return { ...base, active: [], pending: [], expired: [] };
}

function memberTabs(root) {
  return findAll(root, (node) =>
    node.getAttribute?.("role") === "tab" && hasClass(node.parentElement, "memory-member-tabs"));
}

function memberTab(root, uid) {
  return find(root, (node) =>
    hasClass(node, "memory-member-tab") && node.getAttribute?.("data-subject-uid") === uid);
}

function assertFocusedMemberTab(root, uid) {
  const tablist = find(root, (node) => hasClass(node, "memory-member-tabs"));
  const active = document.activeElement;
  assert.ok(active, "a scope tab should have focus");
  assert.equal(active.parentElement, tablist, "focused scope tab must belong to the current tablist");
  assert.equal(active.getAttribute("data-subject-uid"), uid);
  assert.equal(active.getAttribute("aria-selected"), "true");
  assert.equal(active.getAttribute("tabindex"), "0");
  assert.match(active.id, /^memory-member-tab-\d+$/);
  assert.doesNotMatch(active.id, /uid|opaque/i);
  const panel = find(root, (node) => node.id === "memory-subject-panel");
  assert.equal(panel.getAttribute("aria-labelledby"), active.id);
  return active;
}

test("member tab keyboard navigation preserves mounted focus through immediate and async renders", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const detailRequests = [];
  let servedInitial = false;
  const page = createMemoryPage({
    request: (path) => {
      if (path === "/memory") return Promise.resolve(overviewFixture());
      if (path === "/memory/threads/group-a" && !servedInitial) {
        servedInitial = true;
        return Promise.resolve(detailFixture(""));
      }
      const request = deferred();
      detailRequests.push({ path, ...request });
      return request.promise;
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  assert.equal(document.activeElement, null, "initial render must not steal focus");

  async function navigate(key, uid, path) {
    const selected = find(main, (node) =>
      hasClass(node, "memory-member-tab") && node.getAttribute?.("aria-selected") === "true");
    selected.focus();
    selected.dispatchEvent({ type: "keydown", key });

    const immediate = assertFocusedMemberTab(main, uid);
    assert.equal(detailRequests.at(-1).path, path);
    detailRequests.at(-1).resolve(detailFixture(uid));
    await flush();
    await flush();
    const settled = assertFocusedMemberTab(main, uid);
    assert.equal(settled.id, immediate.id, "scope tab identity must remain stable after detail resolves");
  }

  await navigate("End", "uid-empty-opaque", "/memory/threads/group-a?uid=uid-empty-opaque");
  await navigate("Home", "", "/memory/threads/group-a");
  await navigate("ArrowRight", "uid-lan-opaque", "/memory/threads/group-a?uid=uid-lan-opaque");
  await navigate("ArrowLeft", "", "/memory/threads/group-a");
});

test("member loading renders neutral sync state and mouse selection does not steal outside focus", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const lanRequest = deferred();
  const page = createMemoryPage({
    request: (path) => {
      if (path === "/memory") return Promise.resolve(overviewFixture());
      if (path === "/memory/threads/group-a") return Promise.resolve(detailFixture(""));
      if (path === "/memory/threads/group-a?uid=uid-lan-opaque") return lanRequest.promise;
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const outside = document.createElement("button");
  document.body.append(outside);
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  assert.match(text(main), /Đã đồng bộ với phiên Zalo/);

  outside.focus();
  memberTab(main, "uid-lan-opaque").click();
  assert.equal(document.activeElement, outside);
  assert.match(text(main), /Đang kiểm tra trạng thái đồng bộ…/);
  assert.doesNotMatch(text(main), /Đã đồng bộ với phiên Zalo|sẽ đồng bộ khi người này nhắn tiếp/);
  const loadingSync = find(main, (node) => hasClass(node, "memory-sync"));
  assert.equal(hasClass(loadingSync, "is-synced"), false);

  lanRequest.resolve(detailFixture("uid-lan-opaque"));
  await flush();
  await flush();
  assert.equal(document.activeElement, outside);
  assert.match(text(main), /sẽ đồng bộ khi người này nhắn tiếp/);
});

test("failed member load restores coherent data, selected tab, focus, and permits retry", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const firstLan = deferred();
  let lanAttempts = 0;
  const page = createMemoryPage({
    request: (path) => {
      if (path === "/memory") return Promise.resolve(overviewFixture());
      if (path === "/memory/threads/group-a") return Promise.resolve(detailFixture(""));
      if (path === "/memory/threads/group-a?uid=uid-lan-opaque") {
        lanAttempts++;
        return lanAttempts === 1 ? firstLan.promise : Promise.resolve(detailFixture("uid-lan-opaque"));
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  memberTab(main, "").focus();
  memberTab(main, "").dispatchEvent({ type: "keydown", key: "ArrowRight" });
  assertFocusedMemberTab(main, "uid-lan-opaque");
  firstLan.reject({ internal: "DATABASE-DETAIL-CANARY" });
  await flush();
  await flush();

  assertFocusedMemberTab(main, "");
  assert.match(text(main), /GROUP-COMMON-MEMORY/);
  assert.doesNotMatch(text(main), /LAN-ACTIVE-MEMORY/);
  const alert = find(main, (node) => node.getAttribute?.("role") === "alert");
  assert.match(text(alert), /Không thể đọc Memory\. Vui lòng thử lại\./);
  assert.doesNotMatch(text(main), /DATABASE-DETAIL-CANARY|\[object Object\]/);

  document.activeElement.dispatchEvent({ type: "keydown", key: "ArrowRight" });
  assertFocusedMemberTab(main, "uid-lan-opaque");
  await flush();
  await flush();
  assert.equal(lanAttempts, 2);
  assertFocusedMemberTab(main, "uid-lan-opaque");
  assert.match(text(main), /LAN-ACTIVE-MEMORY/);
});

test("late stale member failure cannot roll back a newer focused selection", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const lanRequest = deferred();
  const minhRequest = deferred();
  const page = createMemoryPage({
    request: (path) => {
      if (path === "/memory") return Promise.resolve(overviewFixture());
      if (path === "/memory/threads/group-a") return Promise.resolve(detailFixture(""));
      if (path === "/memory/threads/group-a?uid=uid-lan-opaque") return lanRequest.promise;
      if (path === "/memory/threads/group-a?uid=uid-minh-opaque") return minhRequest.promise;
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  memberTab(main, "").focus();
  memberTab(main, "").dispatchEvent({ type: "keydown", key: "ArrowRight" });
  assertFocusedMemberTab(main, "uid-lan-opaque");
  document.activeElement.dispatchEvent({ type: "keydown", key: "ArrowRight" });
  assertFocusedMemberTab(main, "uid-minh-opaque");
  minhRequest.resolve(detailFixture("uid-minh-opaque"));
  await flush();
  await flush();
  assertFocusedMemberTab(main, "uid-minh-opaque");

  lanRequest.reject(new Error("Phản hồi cũ đến muộn."));
  await flush();
  assertFocusedMemberTab(main, "uid-minh-opaque");
  assert.match(text(main), /U2-PRIVATE-MEMORY/);
  assert.doesNotMatch(text(main), /Phản hồi cũ đến muộn/);
});

test("group member selector renders counts and isolates active, pending, and expired Memory", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const requests = [];
  const page = createMemoryPage({
    request: async (path) => {
      requests.push(path);
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/group-a") return detailFixture("");
      if (path === "/memory/threads/group-a?uid=uid-lan-opaque") return detailFixture("uid-lan-opaque");
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  const tablist = find(main, (node) => hasClass(node, "memory-member-tabs"));
  assert.equal(tablist.getAttribute("role"), "tablist");
  assert.equal(tablist.getAttribute("aria-label"), "Phạm vi ghi nhớ");
  const tabs = memberTabs(main);
  assert.deepEqual(tabs.map((node) => text(node).replace(/\s+/g, " ").trim()), [
    "Chung cho nhóm 1 đang hoạt động · 2 chờ duyệt",
    "Chị Lan 1 đang hoạt động · 1 chờ duyệt",
    "Anh Minh 1 đang hoạt động · 0 chờ duyệt",
    "Cô Mai 0 đang hoạt động · 0 chờ duyệt",
  ]);
  assert.equal(tabs[0].getAttribute("aria-selected"), "true");
  assert.equal(tabs[0].getAttribute("aria-controls"), "memory-subject-panel");
  assert.equal(tabs[0].getAttribute("tabindex"), "0");
  assert.equal(tabs[1].getAttribute("tabindex"), "-1");

  tabs[1].click();
  await flush();
  await flush();

  assert.equal(requests.at(-1), "/memory/threads/group-a?uid=uid-lan-opaque");
  assert.match(text(main), /Đang hoạt động/);
  assert.match(text(main), /Chờ duyệt/);
  assert.match(text(main), /Đã hết hạn/);
  assert.match(text(main), /LAN-ACTIVE-MEMORY/);
  assert.match(text(main), /LAN-EXPIRED-MEMORY/);
  assert.doesNotMatch(text(main), /U2-PRIVATE-MEMORY/);

  const statuses = findAll(main, (node) => hasClass(node, "memory-chip-status")).map(text);
  assert.deepEqual(statuses, ["Đang hoạt động", "Chờ duyệt", "Đang hoạt động", "Đã hết hạn"]);
  assert.match(text(main), /Hồ sơ/);
  assert.match(text(main), /Tin cậy 96%/);
  assert.match(text(main), /Nguồn: tin nhắn “Khách đã xác nhận”/);
  assert.match(text(main), /Hết hạn 31\/08\/2026/);
  assert.match(text(main), /sẽ đồng bộ khi người này nhắn tiếp/);
});

test("status-specific pending replacement compares customer-facing old and new metadata without raw IDs", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const page = createMemoryPage({
    request: async (path) => {
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/group-a") return detailFixture("");
      if (path === "/memory/threads/group-a?uid=uid-lan-opaque") return detailFixture("uid-lan-opaque");
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  memberTabs(main)[1].click();
  await flush();
  await flush();

  const replacement = find(main, (node) => hasClass(node, "memory-replacement"));
  assert.ok(replacement);
  assert.match(text(replacement), /Thông tin hiện tại/);
  assert.match(text(replacement), /Khách dùng địa chỉ giao hàng cũ/);
  assert.match(text(replacement), /Đề xuất mới/);
  assert.match(text(replacement), /Khách đề nghị giao tới địa chỉ mới/);
  assert.match(text(replacement), /Tin cậy 91%/);
  assert.match(text(replacement), /Tin cậy 72%/);
  assert.match(text(replacement), /Nguồn: người trực thêm/);
  assert.doesNotMatch(text(replacement), /(?:ID|Mã)\s*(?:42|73)/i);
  assert.doesNotMatch(text(replacement), /profile\.opaque-key/);
});

test("common and member scope plus private chat selection use exact API UIDs without an extra choice", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const requests = [];
  const privateDetail = {
    ...detailFixture("private-user-opaque"),
    thread: {
      id: "private-user-opaque", name: "Chị Hà", avatar: "", thread_type: "user",
      memory_count: 1, pinned_count: 0, preview: "PRIVATE-ACTIVE",
    },
    selected_uid: "private-user-opaque",
    members: [{ uid: "private-user-opaque", name: "Chị Hà", avatar: "", active: 1, pending: 0, expired: 0 }],
    active: [memory({ id: 91, uid: "private-user-opaque", text: "PRIVATE-ACTIVE" })],
    pending: [], expired: [], synced: true, subject_revision: 2, synced_subject_revision: 2,
    session_subject_uid: "private-user-opaque", revision: 2, synced_revision: 2,
  };
  const page = createMemoryPage({
    request: async (path) => {
      requests.push(path);
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/group-a") return detailFixture("");
      if (path === "/memory/threads/group-a?uid=uid-lan-opaque") return detailFixture("uid-lan-opaque");
      if (path === "/memory/threads/private-user-opaque") return privateDetail;
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();

  memberTabs(main)[1].click();
  await flush();
  await flush();
  memberTabs(main)[0].click();
  await flush();
  await flush();
  assert.equal(requests.at(-1), "/memory/threads/group-a");
  assert.match(text(main), /GROUP-COMMON-MEMORY/);

  const nextOverview = overviewFixture([{
    id: "private-user-opaque", name: "Chị Hà", avatar: "", thread_type: "user",
    memory_count: 1, pinned_count: 0, preview: "PRIVATE-ACTIVE", updated_at: "2026-08-10T08:00:00Z",
  }]);
  const privatePage = createMemoryPage({
    request: async (path) => {
      requests.push(path);
      if (path === "/memory") return nextOverview;
      if (path === "/memory/threads/private-user-opaque") return privateDetail;
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const privateMain = document.createElement("main");
  const privateMounted = privatePage.mount(privateMain);
  t.after(privateMounted.dispose);
  await flush();
  await flush();

  assert.equal(requests.at(-1), "/memory/threads/private-user-opaque");
  assert.match(text(privateMain), /PRIVATE-ACTIVE/);
  assert.match(text(privateMain), /Đã đồng bộ với phiên Zalo/);
  assert.equal(memberTabs(privateMain).length, 0);
});

test("member scope renders explicit empty sections", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const page = createMemoryPage({
    request: async (path) => {
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/group-a") return detailFixture("");
      if (path === "/memory/threads/group-a?uid=uid-empty-opaque") return detailFixture("uid-empty-opaque");
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  memberTabs(main)[3].click();
  await flush();
  await flush();

  assert.match(text(main), /Chưa có ghi chú đang hoạt động/);
  assert.match(text(main), /Không có ghi chú chờ duyệt/);
  assert.match(text(main), /Không có ghi chú hết hạn/);
});

test("rapid member and thread switches reject stale scope responses and disposal aborts them", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const signals = [];
  const calls = [];
  let resolveLan;
  const threads = [groupThread("group-a", "Nhóm Đại lý"), groupThread("group-b", "Nhóm Kho")];
  const groupB = {
    ...detailFixture(""),
    thread: { ...groupThread("group-b", "Nhóm Kho") },
    active: [memory({ id: 101, uid: "", text: "GROUP-B-COMMON" })],
  };
  const page = createMemoryPage({
    request: (path, options = {}) => {
      calls.push(path);
      signals.push(options.signal);
      if (path === "/memory") return Promise.resolve(overviewFixture(threads));
      if (path === "/memory/threads/group-a") return Promise.resolve(detailFixture(""));
      if (path === "/memory/threads/group-a?uid=uid-lan-opaque") {
        return new Promise((resolve) => { resolveLan = resolve; });
      }
      if (path === "/memory/threads/group-a?uid=uid-minh-opaque") {
        return Promise.resolve(detailFixture("uid-minh-opaque"));
      }
      if (path === "/memory/threads/group-b") return Promise.resolve(groupB);
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  await flush();
  await flush();

  memberTabs(main)[1].click();
  await flush();
  memberTabs(main)[2].click();
  await flush();
  await flush();
  resolveLan(detailFixture("uid-lan-opaque"));
  await flush();
  assert.match(text(main), /U2-PRIVATE-MEMORY/);
  assert.doesNotMatch(text(main), /LAN-ACTIVE-MEMORY/);

  const groupBRow = find(main, (node) => hasClass(node, "memory-thread") && text(node).includes("Nhóm Kho"));
  groupBRow.click();
  await flush();
  await flush();
  assert.equal(calls.at(-1), "/memory/threads/group-b");
  assert.doesNotMatch(calls.at(-1), /uid=/);
  assert.match(text(main), /GROUP-B-COMMON/);

  mounted.dispose();
  assert.ok(signals.every((signal) => signal instanceof AbortSignal && signal.aborted));
});
