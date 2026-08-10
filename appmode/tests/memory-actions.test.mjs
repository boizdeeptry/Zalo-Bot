import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { AppAPIError } from "../overlay/internal/webui/static/core/api.js";
import { createMemoryPage, createMemoryService } from "../overlay/internal/webui/static/pages/memory.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));
const hasClass = (node, className) => node.classList?.contains(className) ?? false;

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((onResolve, onReject) => {
    resolve = onResolve;
    reject = onReject;
  });
  return { promise, resolve, reject };
}

function installKeyDispatcher(doc) {
  const listeners = new Set();
  doc.addEventListener = (type, listener) => { if (type === "keydown") listeners.add(listener); };
  doc.removeEventListener = (type, listener) => { if (type === "keydown") listeners.delete(listener); };
  doc.dispatchKeydown = (event) => {
    for (const listener of [...listeners]) listener(event);
  };
}

function memory({
  id, text: value, status = "active", pinned = false, category = "profile",
  memoryKey = "profile.occupation", confidence = 0.82, proposalTarget,
}) {
  return {
    id, thread_id: "group-a", uid: "u-1", memory_key: memoryKey, text: value,
    category, confidence, status, pinned, source: "agent", source_preview: "Khách xác nhận",
    proposal_action: proposalTarget ? "replace" : "", supersedes_id: proposalTarget?.id || 0,
    ...(proposalTarget ? { proposal_target: proposalTarget } : {}),
    expires_at: status === "expired" ? "2026-08-09T00:00:00Z" : "2026-12-31T00:00:00Z",
    created_at: "2026-08-10T01:00:00Z", updated_at: "2026-08-10T01:00:00Z",
  };
}

function detailFixture(uid = "", revision = uid ? 4 : 3) {
  const active = memory({ id: 11, text: "Khách là dược sĩ" });
  const base = {
    thread: { id: "group-a", name: "Nhóm Đại lý", thread_type: "group" },
    selected_uid: uid,
    members: [{ uid: "u-1", name: "Chị Lan", active: 1, pending: 1, expired: 1 }],
    common: { active: 0, pending: 0, expired: 0 },
    common_revision: uid ? 3 : revision,
    subject_revision: uid ? revision : 0,
    revision,
    synced_revision: revision,
    synced: true,
  };
  if (!uid) return { ...base, active: [], pending: [], expired: [] };
  return {
    ...base,
    active: [active],
    pending: [memory({ id: 12, text: "Khách là bác sĩ", status: "pending", proposalTarget: active })],
    expired: [memory({
      id: 13, text: "Khách thích trồng lan", status: "expired",
      category: "interest", memoryKey: "interest.hobby", confidence: 0.75, pinned: true,
    })],
  };
}

function overviewFixture() {
  return {
    metrics: { memories: 3, threads: 1, lessons: 0 },
    threads: [{
      id: "group-a", name: "Nhóm Đại lý", thread_type: "group",
      memory_count: 3, pinned_count: 0, preview: "Khách là dược sĩ",
    }],
  };
}

function card(root, marker) {
  return find(root, (node) => hasClass(node, "memory-status-card") && text(node).includes(marker));
}

function buttons(root) {
  return findAll(root, (node) => node.tagName === "BUTTON").map((node) => text(node));
}

function assertConnectedFocus(root, message) {
  const active = document.activeElement;
  assert.ok(active, `${message}: expected an active element`);
  assert.ok(find(root, (node) => node === active), `${message}: focus must belong to the mounted Memory root`);
  return active;
}

function afterMutationDetail(kind) {
  const detail = detailFixture("u-1", 5);
  if (kind === "edit") detail.active[0] = { ...detail.active[0], text: "Khách là bác sĩ" };
  if (kind === "pin") detail.active[0] = { ...detail.active[0], pinned: true };
  if (kind === "approve") {
    const { proposal_target: _target, ...proposal } = detail.pending[0];
    detail.active.push({ ...proposal, status: "active", proposal_action: "", supersedes_id: 11 });
    detail.pending = [];
  }
  if (kind === "restore") {
    detail.active.push({ ...detail.expired[0], status: "active" });
    detail.expired = [];
  }
  if (kind === "delete") detail.active = [];
  if (kind === "reject") detail.pending = [];
  if (kind === "create") {
    detail.active.push(memory({ id: 14, text: "Giao sau 9 giờ", memoryKey: "preference.delivery", category: "preference" }));
  }
  return detail;
}

async function focusScenario(t, kind) {
  const mutation = deferred();
  const refresh = deferred();
  let memberReads = 0;
  const page = createMemoryPage({
    request: (path, options = {}) => {
      if (options.method) return mutation.promise;
      if (path === "/memory") return Promise.resolve(overviewFixture());
      if (path === "/memory/threads/group-a") return Promise.resolve(detailFixture(""));
      if (path === "/memory/threads/group-a?uid=u-1") {
        memberReads++;
        return memberReads === 1 ? Promise.resolve(detailFixture("u-1", 4)) : refresh.promise;
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-1").click();
  await flush();
  await flush();

  let opener;
  if (kind === "create") {
    opener = find(main, (node) => node.tagName === "BUTTON" && text(node).includes("Thêm ghi chú"));
  } else {
    const marker = kind === "approve" || kind === "reject"
      ? "Đề xuất mới"
      : kind === "restore"
        ? "Khách thích trồng lan"
        : "Khách là dược sĩ";
    const label = {
      edit: "Sửa", pin: "Ghim", approve: "Duyệt", restore: "Khôi phục", delete: "Xoá", reject: "Từ chối",
    }[kind];
    opener = find(card(main, marker), (node) => node.tagName === "BUTTON" && text(node) === label);
  }
  opener.focus();
  opener.click();

  if (["edit", "delete", "reject", "create"].includes(kind)) {
    await flush();
    const overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
    if (kind === "edit") {
      find(overlay, (node) => node.getAttribute?.("name") === "text").value = "Khách là bác sĩ";
      find(overlay, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
    } else if (kind === "create") {
      find(overlay, (node) => node.getAttribute?.("name") === "memory_key").value = "preference.delivery";
      find(overlay, (node) => node.getAttribute?.("name") === "text").value = "Giao sau 9 giờ";
      find(overlay, (node) => node.getAttribute?.("name") === "category").value = "preference";
      find(overlay, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
    } else {
      const confirmation = kind === "delete" ? "Xác nhận xoá" : "Xác nhận từ chối";
      find(overlay, (node) => node.tagName === "BUTTON" && text(node) === confirmation).click();
    }
  } else {
    const immediate = assertConnectedFocus(main, `${kind} immediate pending render`);
    assert.equal(immediate.getAttribute("data-memory-action-kind"), kind === "restore" ? "restore" : kind);
  }

  mutation.resolve({});
  await flush();
  await flush();
  const beforeRefresh = assertConnectedFocus(main, `${kind} successful mutation before refresh`);
  refresh.resolve(afterMutationDetail(kind));
  await flush();
  await flush();
  await flush();
  const afterRefresh = assertConnectedFocus(main, `${kind} post-refresh render`);
  return { afterRefresh, beforeRefresh, main };
}

async function mountMemberPage(t, mutationHandler = null) {
  const calls = [];
  let revision = 4;
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (options.method) {
        if (mutationHandler) return mutationHandler(path, options, calls);
        return {};
      }
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/group-a") return detailFixture("");
      if (path === "/memory/threads/group-a?uid=u-1") return detailFixture("u-1", revision++);
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  const member = find(main, (node) =>
    hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-1");
  member.click();
  await flush();
  await flush();
  return { calls, main, mounted };
}

async function backgroundRenderDialogScenario(t, { actionLabel, dismiss }) {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  const refreshedOverview = deferred();
  const calls = [];
  const page = createMemoryPage({
    request: (path, options = {}) => {
      calls.push({ path, options });
      if (options.method) return Promise.resolve({});
      if (path === "/memory") return Promise.resolve(overviewFixture());
      if (path === "/memory?pinned=true") return refreshedOverview.promise;
      if (path === "/memory/threads/group-a") return Promise.resolve(detailFixture(""));
      if (path === "/memory/threads/group-a?uid=u-1") return Promise.resolve(detailFixture("u-1", 4));
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-1").click();
  await flush();
  await flush();

  const pinnedFilter = find(main, (node) =>
    node.tagName === "INPUT" && node.getAttribute("aria-label") === "Chỉ nội dung đã ghim");
  pinnedFilter.checked = true;
  pinnedFilter.dispatchEvent({ type: "change" });
  const opener = find(card(main, "Khách là dược sĩ"), (node) =>
    node.tagName === "BUTTON" && text(node) === actionLabel);
  opener.focus();
  opener.click();
  await flush();

  const overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  assert.ok(overlay, "dialog should open while the filtered overview is pending");
  const dialogFocus = document.activeElement;
  assert.ok(find(overlay, (node) => node === dialogFocus), "dialog should own focus before the background render");

  refreshedOverview.resolve(overviewFixture());
  await flush();
  await flush();
  await flush();
  assert.equal(
    find(document.body, (node) => node.hasAttribute?.("data-memory-overlay")),
    overlay,
    "background overview completion must not close or replace the dialog",
  );
  assert.equal(document.activeElement, dialogFocus, "background root renders must not steal dialog focus");

  if (dismiss === "cancel") {
    find(overlay, (node) => node.tagName === "BUTTON" && text(node) === "Huỷ").click();
  } else {
    document.dispatchKeydown({ key: "Escape" });
  }

  const restored = assertConnectedFocus(main, `${dismiss} after background root render`);
  assert.equal(restored.getAttribute("data-memory-row-id"), "11");
  assert.equal(restored.getAttribute("data-memory-action-kind"), actionLabel === "Sửa" ? "edit" : "delete");
  assert.equal(find(document.body, (node) => node.hasAttribute?.("data-memory-overlay")), null);
  assert.equal(calls.filter(({ options }) => options.method).length, 0, "dismissing must not mutate Memory");
}

test("memory V2 service sends exact scoped mutation endpoints and bodies", async () => {
  const calls = [];
  const service = createMemoryService(async (path, options) => {
    calls.push({ path, options });
    return {};
  });
  const decision = { uid: "u-1", expected_revision: 4 };
  const approval = {
    ...decision,
    memory_key: "profile.occupation",
    text: "là bác sĩ",
    category: "profile",
  };
  const manual = { ...approval, pinned: false };

  await service.approve("group/a?b", "12/x", approval);
  await service.reject("group/a?b", "12/x", decision);
  await service.restore("group/a?b", "13/x", decision);
  await service.createThread("group/a?b", manual);
  await service.updateThread("group/a?b", "14/x", manual);
  await service.deleteThread("group/a?b", "14/x", decision);

  assert.deepEqual(calls, [
    { path: "/memory/threads/group%2Fa%3Fb/12%2Fx/approve", options: { method: "POST", body: approval } },
    { path: "/memory/threads/group%2Fa%3Fb/12%2Fx/reject", options: { method: "POST", body: decision } },
    { path: "/memory/threads/group%2Fa%3Fb/13%2Fx/restore", options: { method: "POST", body: decision } },
    { path: "/memory/threads/group%2Fa%3Fb", options: { method: "POST", body: manual } },
    { path: "/memory/threads/group%2Fa%3Fb/14%2Fx", options: { method: "PUT", body: manual } },
    { path: "/memory/threads/group%2Fa%3Fb/14%2Fx", options: { method: "DELETE", body: decision } },
  ]);
});

test("Cancel restores an edit dialog to a mounted semantic opener after a background overview render", async (t) => {
  await backgroundRenderDialogScenario(t, { actionLabel: "Sửa", dismiss: "cancel" });
});

test("Escape restores a confirm dialog to a mounted semantic opener after a background overview render", async (t) => {
  await backgroundRenderDialogScenario(t, { actionLabel: "Xoá", dismiss: "escape" });
});

test("Memory actions are available only for their active pending and expired lifecycle", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const { main } = await mountMemberPage(t);

  assert.deepEqual(buttons(card(main, "Khách là dược sĩ")), ["Ghim", "Sửa", "Xoá"]);
  assert.deepEqual(buttons(card(main, "Đề xuất mới")), ["Duyệt", "Sửa rồi duyệt", "Từ chối"]);
  assert.deepEqual(buttons(card(main, "Khách thích trồng lan")), ["Khôi phục", "Ghim", "Xoá"]);
  assert.doesNotMatch(text(main), /(?:ID|Mã)\s*(?:11|12|13)/i);
});

test("pending approve and expired restore use the exact selected member revision", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const { calls, main } = await mountMemberPage(t);

  find(card(main, "Đề xuất mới"), (node) => node.tagName === "BUTTON" && text(node) === "Duyệt").click();
  await flush();
  await flush();
  const approve = calls.find(({ path }) => path === "/memory/threads/group-a/12/approve");
  assert.deepEqual(approve.options, {
    method: "POST",
    body: {
      uid: "u-1", expected_revision: 4,
      memory_key: "profile.occupation", text: "Khách là bác sĩ", category: "profile",
    },
    signal: approve.options.signal,
  });
  assert.ok(approve.options.signal instanceof AbortSignal);

  await flush();
  const expired = card(main, "Khách thích trồng lan");
  find(expired, (node) => node.tagName === "BUTTON" && text(node) === "Khôi phục").click();
  await flush();
  const restore = calls.find(({ path }) => path === "/memory/threads/group-a/13/restore");
  assert.deepEqual(restore.options.body, { uid: "u-1", expected_revision: 5 });
  assert.equal(restore.options.method, "POST");
});

test("pending reject requires confirmation and sends only selected member scope", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const { calls, main } = await mountMemberPage(t);
  const opener = find(card(main, "Đề xuất mới"), (node) =>
    node.tagName === "BUTTON" && text(node) === "Từ chối");

  opener.click();
  await flush();
  assert.equal(calls.some(({ path }) => path.endsWith("/reject")), false);
  const overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  assert.match(text(overlay), /Từ chối đề xuất/);
  find(overlay, (node) => node.tagName === "BUTTON" && text(node) === "Xác nhận từ chối").click();
  await flush();

  const rejection = calls.find(({ path }) => path === "/memory/threads/group-a/12/reject");
  assert.equal(rejection.options.method, "POST");
  assert.deepEqual(rejection.options.body, { uid: "u-1", expected_revision: 4 });
});

test("manual add visibly targets the selected member and sends strict trimmed V2 fields", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const { calls, main } = await mountMemberPage(t);

  find(main, (node) => node.tagName === "BUTTON" && text(node).includes("Thêm ghi chú")).click();
  await flush();
  const overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  assert.match(text(overlay), /Chị Lan/);
  assert.doesNotMatch(text(overlay), /u-1/);
  const key = find(overlay, (node) => node.getAttribute?.("name") === "memory_key");
  const value = find(overlay, (node) => node.getAttribute?.("name") === "text");
  const category = find(overlay, (node) => node.getAttribute?.("name") === "category");
  key.value = "  preference.delivery  ";
  value.value = "  giao sau 9 giờ  ";
  category.value = "preference";
  find(overlay, (node) => node.getAttribute?.("name") === "pinned").checked = true;
  find(overlay, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();

  const create = calls.find(({ path, options }) => path === "/memory/threads/group-a" && options.method === "POST");
  assert.deepEqual(create.options.body, {
    uid: "u-1", expected_revision: 4,
    memory_key: "preference.delivery", text: "giao sau 9 giờ", category: "preference", pinned: true,
  });
});

test("edit-and-approve keeps confidence read-only and preserves its trimmed draft on conflict", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  t.after(dom.restore);
  const mutation = deferred();
  const { calls, main } = await mountMemberPage(t, (path) => {
    if (path.endsWith("/approve")) return mutation.promise;
    return {};
  });
  const opener = find(card(main, "Đề xuất mới"), (node) =>
    node.tagName === "BUTTON" && text(node) === "Sửa rồi duyệt");
  opener.click();
  await flush();
  const overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  const key = find(overlay, (node) => node.getAttribute?.("name") === "memory_key");
  const value = find(overlay, (node) => node.getAttribute?.("name") === "text");
  const category = find(overlay, (node) => node.getAttribute?.("name") === "category");
  const confidence = find(overlay, (node) => node.getAttribute?.("name") === "confidence");
  assert.equal(confidence.hasAttribute("readonly"), true);
  assert.equal(confidence.getAttribute("aria-readonly"), "true");
  assert.equal(confidence.value, "82%");
  key.value = "  profile.current-job  ";
  value.value = "  là bác sĩ  ";
  category.value = "profile";

  const form = find(overlay, (node) => node.tagName === "FORM");
  form.dispatchEvent({ type: "submit" });
  form.dispatchEvent({ type: "submit" });
  assert.equal(calls.filter(({ path }) => path.endsWith("/approve")).length, 1);
  assert.equal(find(overlay, (node) => node.tagName === "BUTTON" && text(node) === "Đang lưu…").disabled, true);
  const approval = calls.find(({ path }) => path.endsWith("/approve"));
  assert.deepEqual(approval.options.body, {
    uid: "u-1", expected_revision: 4,
    memory_key: "profile.current-job", text: "là bác sĩ", category: "profile",
  });

  mutation.reject(new AppAPIError({
    status: 409,
    code: "memory_conflict",
    message: "Memory đã thay đổi; hãy tải lại trước khi lưu",
  }));
  await flush();
  await flush();
  const openOverlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  assert.equal(openOverlay, overlay);
  assert.equal(key.value, "  profile.current-job  ");
  assert.equal(value.value, "  là bác sĩ  ");
  assert.equal(category.value, "profile");
  assert.equal(find(overlay, (node) => node.tagName === "BUTTON" && text(node) === "Duyệt").disabled, false);
  assert.match(text(find(main, (node) => hasClass(node, "memory-live"))), /Memory đã thay đổi; hãy tải lại trước khi lưu/);
  assert.match(text(main), /Khách là dược sĩ/);

  document.dispatchKeydown({ key: "Escape" });
  assert.equal(find(document.body, (node) => node.hasAttribute?.("data-memory-overlay")), null);
  assert.equal(document.activeElement, opener);
});

test("active pin edit and delete carry selected member UID and refreshed revisions", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const { calls, main } = await mountMemberPage(t);

  find(card(main, "Khách là dược sĩ"), (node) => node.tagName === "BUTTON" && text(node) === "Ghim").click();
  await flush();
  await flush();
  await flush();
  let updates = calls.filter(({ path, options }) => path === "/memory/threads/group-a/11" && options.method === "PUT");
  assert.deepEqual(updates[0].options.body, {
    uid: "u-1", expected_revision: 4,
    memory_key: "profile.occupation", text: "Khách là dược sĩ", category: "profile", pinned: true,
  });

  find(card(main, "Khách là dược sĩ"), (node) => node.tagName === "BUTTON" && text(node) === "Sửa").click();
  await flush();
  let overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  find(overlay, (node) => node.getAttribute?.("name") === "memory_key").value = "  profile.job  ";
  find(overlay, (node) => node.getAttribute?.("name") === "text").value = "  Khách là bác sĩ  ";
  find(overlay, (node) => node.getAttribute?.("name") === "category").value = "profile";
  find(overlay, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();
  await flush();
  await flush();
  updates = calls.filter(({ path, options }) => path === "/memory/threads/group-a/11" && options.method === "PUT");
  assert.deepEqual(updates[1].options.body, {
    uid: "u-1", expected_revision: 5,
    memory_key: "profile.job", text: "Khách là bác sĩ", category: "profile", pinned: false,
  });

  const deleteOpener = find(card(main, "Khách là dược sĩ"), (node) =>
    node.tagName === "BUTTON" && text(node) === "Xoá");
  deleteOpener.click();
  await flush();
  overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  find(overlay, (node) => node.tagName === "BUTTON" && text(node) === "Xác nhận xoá").click();
  await flush();
  const deletion = calls.find(({ path, options }) => path === "/memory/threads/group-a/11" && options.method === "DELETE");
  assert.deepEqual(deletion.options.body, { uid: "u-1", expected_revision: 6 });
});

test("manual mutation uses explicit common UID and common revision", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const calls = [];
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (options.method === "POST") return {};
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/group-a") return detailFixture("", 3);
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  find(main, (node) => node.tagName === "BUTTON" && text(node).includes("Thêm ghi chú")).click();
  await flush();
  const overlay = find(document.body, (node) => node.hasAttribute?.("data-memory-overlay"));
  assert.match(text(overlay), /phạm vi chung của Nhóm Đại lý/);
  find(overlay, (node) => node.getAttribute?.("name") === "memory_key").value = "preference.group";
  find(overlay, (node) => node.getAttribute?.("name") === "text").value = "Quy tắc chung";
  find(overlay, (node) => node.getAttribute?.("name") === "category").value = "preference";
  find(overlay, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  await flush();
  const create = calls.find(({ options }) => options.method === "POST");
  assert.deepEqual(create.options.body, {
    uid: "", expected_revision: 3,
    memory_key: "preference.group", text: "Quy tắc chung", category: "preference", pinned: false,
  });
});

test("failed post-mutation refresh keeps last detail and announces safe reload failure", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  let memberReads = 0;
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      if (options.method === "PUT") return {};
      if (path === "/memory") return overviewFixture();
      if (path === "/memory/threads/group-a") return detailFixture("");
      if (path === "/memory/threads/group-a?uid=u-1") {
        memberReads++;
        if (memberReads > 1) throw new Error("không thể làm mới chi tiết");
        return detailFixture("u-1", 4);
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-1").click();
  await flush();
  await flush();
  find(card(main, "Khách là dược sĩ"), (node) => node.tagName === "BUTTON" && text(node) === "Ghim").click();
  await flush();
  await flush();
  await flush();

  assert.match(text(main), /Khách là dược sĩ/);
  assert.equal(
    text(find(main, (node) => hasClass(node, "memory-live"))),
    "Không thể đọc Memory. Vui lòng thử lại.",
  );
  assert.doesNotMatch(text(main), /không thể làm mới chi tiết/);
  assert.doesNotMatch(text(main), /\[object Object\]/);
});

test("Memory mutation messages expose only bounded control-free safe 4xx API errors", async (t) => {
  const fallback = "Không thể cập nhật Memory. Vui lòng thử lại.";
  const cases = [
    {
      name: "safe structured conflict",
      error: new AppAPIError({ status: 409, code: "memory_conflict", message: "Memory đã thay đổi; hãy tải lại trước khi lưu" }),
      expected: "Memory đã thay đổi; hãy tải lại trước khi lưu",
    },
    {
      name: "server error",
      error: new AppAPIError({ status: 500, code: "memory_internal_error", message: "SECRET-500" }),
      secret: "SECRET-500",
    },
    {
      name: "raw non-JSON response",
      error: new AppAPIError({ status: 400, code: "HTTP_400", message: "SECRET-RAW-BODY" }),
      secret: "SECRET-RAW-BODY",
    },
    { name: "plain Error", error: new Error("SECRET-PLAIN-ERROR"), secret: "SECRET-PLAIN-ERROR" },
    {
      name: "control characters",
      error: new AppAPIError({ status: 409, code: "memory_conflict", message: "SECRET\nCONTROL" }),
      secret: "SECRET",
    },
    {
      name: "overlong message",
      error: new AppAPIError({ status: 409, code: "memory_conflict", message: `SECRET-${"x".repeat(241)}` }),
      secret: "SECRET-",
    },
    { name: "non-Error value", error: { status: 409, message: "SECRET-NON-ERROR" }, secret: "SECRET-NON-ERROR" },
  ];

  for (const scenario of cases) {
    await t.test(scenario.name, async (t) => {
      const dom = installDOM();
      t.after(dom.restore);
      const { main } = await mountMemberPage(t, () => Promise.reject(scenario.error));
      find(card(main, "Khách là dược sĩ"), (node) => node.tagName === "BUTTON" && text(node) === "Ghim").click();
      await flush();
      await flush();
      const message = text(find(main, (node) => hasClass(node, "memory-live")));
      assert.equal(message, scenario.expected || fallback);
      if (scenario.secret) assert.doesNotMatch(text(main), new RegExp(scenario.secret));
    });
  }
});

test("stale mutation failure cannot overwrite or announce into a newly selected member", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const mutation = deferred();
  const members = [
    { uid: "u-1", name: "Chị Lan", active: 1, pending: 0, expired: 0 },
    { uid: "u-2", name: "Anh Minh", active: 1, pending: 0, expired: 0 },
  ];
  const scopedDetail = (uid) => ({
    ...detailFixture(uid, uid === "u-2" ? 8 : uid ? 4 : 3),
    members,
    active: uid ? [memory({
      id: uid === "u-2" ? 21 : 11,
      text: uid === "u-2" ? "U2-CURRENT-MEMORY" : "U1-OLD-MEMORY",
    })] : [],
    pending: [], expired: [],
  });
  const page = createMemoryPage({
    request: (path, options = {}) => {
      if (options.method === "PUT") return mutation.promise;
      if (path === "/memory") return Promise.resolve(overviewFixture());
      if (path === "/memory/threads/group-a") return Promise.resolve(scopedDetail(""));
      if (path === "/memory/threads/group-a?uid=u-1") return Promise.resolve(scopedDetail("u-1"));
      if (path === "/memory/threads/group-a?uid=u-2") return Promise.resolve(scopedDetail("u-2"));
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-1").click();
  await flush();
  await flush();
  find(card(main, "U1-OLD-MEMORY"), (node) => node.tagName === "BUTTON" && text(node) === "Ghim").click();
  find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-2").click();
  await flush();
  await flush();
  assert.match(text(main), /U2-CURRENT-MEMORY/);

  mutation.reject(new Error("OLD-U1-MUTATION-FAILURE"));
  await flush();
  await flush();
  assert.match(text(main), /U2-CURRENT-MEMORY/);
  assert.doesNotMatch(text(main), /OLD-U1-MUTATION-FAILURE/);
  const selected = find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("aria-selected") === "true");
  assert.equal(selected.getAttribute("data-subject-uid"), "u-2");
});

test("mutation editor styles stay scoped to the legacy Memory page", () => {
  const css = readFileSync(new URL("../overlay/internal/webui/static/portal.css", import.meta.url), "utf8");
  assert.match(css, /\.memory-page \.memory-dialog-field input\[type="text"\]/);
  assert.match(css, /\.memory-page \.memory-dialog-field select/);
  assert.match(css, /\.memory-page \.memory-proposal-actions/);
  assert.doesNotMatch(css, /^\s*\.memory-dialog-field\s/m);
  assert.doesNotMatch(css, /^\s*\.memory-proposal-actions\s/m);
});

test("expired Ghim mutation always requests an active pin in the selected scope", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const { calls, main } = await mountMemberPage(t);
  find(card(main, "Khách thích trồng lan"), (node) =>
    node.tagName === "BUTTON" && text(node) === "Ghim").click();
  await flush();
  const update = calls.find(({ path, options }) =>
    path === "/memory/threads/group-a/13" && options.method === "PUT");
  assert.deepEqual(update.options.body, {
    uid: "u-1", expected_revision: 4,
    memory_key: "interest.hobby", text: "Khách thích trồng lan", category: "interest", pinned: true,
  });
});

test("mutation overview refresh never drops the selected member when filters omit its thread", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  let overviewReads = 0;
  let memberReads = 0;
  const page = createMemoryPage({
    request: async (path, options = {}) => {
      if (options.method === "PUT") return {};
      if (path === "/memory") {
        overviewReads++;
        return overviewReads === 1
          ? overviewFixture()
          : { metrics: { memories: 0, threads: 0, lessons: 0 }, threads: [] };
      }
      if (path === "/memory/threads/group-a") return detailFixture("");
      if (path === "/memory/threads/group-a?uid=u-1") {
        memberReads++;
        return detailFixture("u-1", memberReads === 1 ? 4 : 5);
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-1").click();
  await flush();
  await flush();
  find(card(main, "Khách là dược sĩ"), (node) => node.tagName === "BUTTON" && text(node) === "Ghim").click();
  await flush();
  await flush();
  await flush();

  assert.equal(overviewReads, 2);
  assert.equal(memberReads, 2);
  assert.match(text(main), /Khách là dược sĩ/);
  const selected = find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("aria-selected") === "true");
  assert.equal(selected.getAttribute("data-subject-uid"), "u-1");
  assert.match(text(main), /revision 5/);
});

test("mutation refresh stops at an awaited boundary after the selected member becomes stale", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const staleRefresh = deferred();
  let overviewReads = 0;
  let u1Reads = 0;
  const members = [
    { uid: "u-1", name: "Chị Lan", active: 1, pending: 0, expired: 0 },
    { uid: "u-2", name: "Anh Minh", active: 1, pending: 0, expired: 0 },
  ];
  const scoped = (uid, revision) => ({
    ...detailFixture(uid, revision), members,
    active: uid ? [memory({ id: uid === "u-2" ? 21 : 11, text: uid === "u-2" ? "U2-CURRENT" : "U1-OLD" })] : [],
    pending: [], expired: [],
  });
  const page = createMemoryPage({
    request: (path, options = {}) => {
      if (options.method === "PUT") return Promise.resolve({});
      if (path === "/memory") {
        overviewReads++;
        return Promise.resolve(overviewFixture());
      }
      if (path === "/memory/threads/group-a") return Promise.resolve(scoped("", 3));
      if (path === "/memory/threads/group-a?uid=u-1") {
        u1Reads++;
        return u1Reads === 1 ? Promise.resolve(scoped("u-1", 4)) : staleRefresh.promise;
      }
      if (path === "/memory/threads/group-a?uid=u-2") return Promise.resolve(scoped("u-2", 8));
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  await flush();
  find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-1").click();
  await flush();
  await flush();
  find(card(main, "U1-OLD"), (node) => node.tagName === "BUTTON" && text(node) === "Ghim").click();
  await flush();
  assert.equal(u1Reads, 2, "mutation should be waiting at its exact member detail refresh");

  find(main, (node) => hasClass(node, "memory-member-tab") && node.getAttribute("data-subject-uid") === "u-2").click();
  await flush();
  await flush();
  staleRefresh.resolve(scoped("u-1", 5));
  await flush();
  await flush();

  assert.equal(overviewReads, 1, "stale refresh must stop before the next awaited overview boundary");
  assert.match(text(main), /U2-CURRENT/);
  assert.doesNotMatch(text(main), /U1-OLD/);
});

for (const kind of ["edit", "pin"]) {
  test(`successful ${kind} focus stays on the mounted equivalent row action`, async (t) => {
    const dom = installDOM();
    t.after(dom.restore);
    const { afterRefresh } = await focusScenario(t, kind);
    assert.equal(afterRefresh.getAttribute("data-memory-row-id"), "11");
    assert.equal(afterRefresh.getAttribute("data-memory-action-kind"), kind);
  });
}

for (const [kind, rowID] of [["approve", "12"], ["restore", "13"]]) {
  test(`successful ${kind} focus falls back to a mounted action on the transitioned row`, async (t) => {
    const dom = installDOM();
    t.after(dom.restore);
    const { afterRefresh } = await focusScenario(t, kind);
    assert.equal(afterRefresh.getAttribute("data-memory-row-id"), rowID);
    assert.ok(afterRefresh.getAttribute("data-memory-action-kind"));
  });
}

for (const kind of ["delete", "reject"]) {
  test(`successful ${kind} focus falls back to mounted Add when its row disappears`, async (t) => {
    const dom = installDOM();
    t.after(dom.restore);
    const { afterRefresh } = await focusScenario(t, kind);
    assert.equal(afterRefresh.getAttribute("data-memory-focus-key"), "add");
  });
}

test("successful create focus returns to the newly mounted Add button", async (t) => {
  const dom = installDOM();
  t.after(dom.restore);
  const { afterRefresh } = await focusScenario(t, "create");
  assert.equal(afterRefresh.getAttribute("data-memory-focus-key"), "add");
});
