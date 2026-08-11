import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { createKnowledgePage } from "../overlay/internal/webui/static/pages/knowledge.js";
import { createAgentsPage } from "../overlay/internal/webui/static/pages/agents.js";
import {
  find,
  findAll,
  installDOM,
  text,
} from "./helpers/dom-harness.mjs";

const flush = () => new Promise((resolve) => setImmediate(resolve));

function hasClass(node, className) {
  return node.classList?.contains(className) ?? false;
}

function signature(node) {
  const id = node.tagName !== "INPUT" && node.id ? `#${node.id}` : "";
  const classes = node.className
    ? `.${node.className.trim().split(/\s+/).join(".")}`
    : "";
  return `${node.tagName}${id}${classes}`;
}

function installKeyDispatcher(doc) {
  const listeners = new Set();
  doc.addEventListener = (type, listener) => { if (type === "keydown") listeners.add(listener); };
  doc.removeEventListener = (type, listener) => { if (type === "keydown") listeners.delete(listener); };
  doc.dispatchKeydown = (event) => {
    for (const listener of [...listeners]) listener(event);
  };
  return { listenerCount: () => listeners.size };
}

test("legacy Memory sheet CSS stays scoped and collapses on mobile", async () => {
  const css = await readFile(new URL("../overlay/internal/webui/static/portal.css", import.meta.url), "utf8");
  assert.match(css, /\.memory-page \.memory-metrics\s*\{/);
  assert.match(css, /\.memory-page \.memory-split\s*\{/);
  assert.match(css, /\[data-memory-overlay\] \.sheet\s*\{/);
  assert.match(css, /@media \(max-width: 760px\)[\s\S]*\.memory-page \.memory-split/);
  assert.doesNotMatch(css, /^\s*\.(?:memory-metrics|memory-split|memory-entry|memory-dialog-field)\s*\{/m);
});

test("Knowledge renders the legacy page contract", async (t) => {
  const dom = installDOM();
  const requests = [];
  const page = createKnowledgePage({
    request: async (path, options = {}) => {
      requests.push({ path, options });
      return {
        raw_count: 0,
        wiki_count: 0,
        raw_files: [],
        wiki_files: [],
        ingest: {},
      };
    },
    setInterval: () => 41,
    clearInterval: () => {},
  });
  t.after(dom.restore);

  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();

  assert.deepEqual(main.children.map(signature), [
    "HEADER.pagehead",
    "DIV#cards.cards",
    "DIV#drop",
    "INPUT",
    "DIV.row",
    "DIV#steps",
    "DIV.cols",
    "DIV.hint",
  ]);

  for (const className of ["knowledge-status", "status-panel", "section-block"]) {
    assert.equal(findAll(main, (node) => hasClass(node, className)).length, 0);
  }

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "Knowledge");
  assert.equal(
    text(find(main, (node) => hasClass(node, "sub"))),
    "Tải tệp nguồn lên, rồi để agent biên soạn thành trang wiki. Bot trả lời khách từ wiki.",
  );

  const cards = find(main, (node) => node.id === "cards");
  assert.equal(cards.children.length, 2);
  assert.ok(cards.children.every((node) => hasClass(node, "card")));
  assert.deepEqual(
    cards.children.map((card) => text(find(card, (node) => hasClass(node, "l")))),
    ["tệp trong raw\\", "trang trong wiki\\"],
  );

  const drop = find(main, (node) => node.id === "drop");
  assert.equal(drop.getAttribute("role"), "button");
  assert.equal(drop.getAttribute("tabindex"), "0");
  assert.match(text(drop), /tối đa 64 MiB mỗi lượt tải/);
  assert.equal(
    text(find(drop, (node) => hasClass(node, "big"))),
    "Kéo tệp vào đây, hoặc bấm để chọn",
  );
  assert.equal(
    text(find(drop, (node) => hasClass(node, "small"))),
    "pdf, docx, xlsx, pptx, md, txt, csv, png, jpg · tối đa 64 MiB mỗi lượt tải",
  );

  const fileInput = main.children[3];
  assert.equal(fileInput.id, "knowledge-files");
  assert.equal(fileInput.getAttribute("type"), "file");
  assert.equal(fileInput.hasAttribute("multiple"), true);
  assert.equal(
    fileInput.getAttribute("accept"),
    ".pdf,.md,.txt,.csv,.docx,.xlsx,.pptx,.png,.jpg,.jpeg,.webp",
  );
  assert.equal(fileInput.getAttribute("tabindex"), "-1");
  assert.equal(fileInput.getAttribute("aria-hidden"), "true");
  assert.equal(fileInput.style.display, "none");

  const buttons = findAll(main, (node) => hasClass(node, "btn"));
  assert.deepEqual(buttons.map(text), ["Biên soạn vào wiki", "Huỷ"]);
  assert.ok(buttons.every((node) => !hasClass(node, "go")));

  const row = find(main, (node) => hasClass(node, "row"));
  const ingestState = find(row, (node) => node.id === "istate");
  assert.ok(ingestState);
  assert.equal(ingestState.getAttribute("aria-live"), "polite");

  const fileLists = findAll(main, (node) => hasClass(node, "flist"));
  assert.equal(fileLists.length, 2);
  assert.deepEqual(
    fileLists.map((list) => text(find(list, (node) => hasClass(node, "none")))),
    ["chưa có tệp nào", "chưa có tệp nào"],
  );

  assert.ok(find(main, (node) => node.id === "steps"));
  const columns = find(main, (node) => hasClass(node, "cols"));
  assert.ok(columns);
  assert.deepEqual(
    columns.children.map((column) => ({
      heading: text(find(column, (node) => node.tagName === "H3")),
      listId: find(column, (node) => hasClass(node, "flist"))?.id,
    })),
    [
      { heading: "raw\\ — tệp nguồn", listId: "rawList" },
      { heading: "wiki\\ — trang đã biên soạn", listId: "wikiList" },
    ],
  );
  assert.equal(
    text(main.children.at(-1)),
    "Biên soạn gọi mô hình, nên nó tốn phí trên tài khoản Claude của bạn. Nó chỉ chạy khi bạn bấm.",
  );

  let inputClicks = 0;
  fileInput.addEventListener("click", () => { inputClicks++ });
  drop.dispatchEvent({ type: "click" });
  assert.equal(inputClicks, 1);
  drop.dispatchEvent({ type: "keydown", key: "Enter" });
  assert.equal(inputClicks, 2);
  drop.dispatchEvent({ type: "keydown", key: " " });
  assert.equal(inputClicks, 3);

  let dragoverPrevented = 0;
  drop.dispatchEvent({
    type: "dragover",
    preventDefault: () => { dragoverPrevented++ },
  });
  assert.equal(dragoverPrevented, 1);
  assert.equal(drop.classList.contains("hot"), true);
  drop.dispatchEvent({ type: "dragleave" });
  assert.equal(drop.classList.contains("hot"), false);

  drop.classList.add("hot");
  let dropPrevented = 0;
  drop.dispatchEvent({
    type: "drop",
    dataTransfer: { files: [] },
    preventDefault: () => { dropPrevented++ },
  });
  assert.equal(dropPrevented, 1);
  assert.equal(drop.classList.contains("hot"), false);
  assert.equal(requests.some(({ path }) => path === "/kb/upload"), false);

  const pickerFile = new File(["picker"], "picker.txt", { type: "text/plain" });
  fileInput.files = [pickerFile];
  fileInput.dispatchEvent({ type: "change" });
  await flush();

  let uploads = requests.filter(({ path }) => path === "/kb/upload");
  assert.equal(uploads.length, 1);
  assert.equal(uploads[0].options.method, "POST");
  assert.ok(uploads[0].options.body instanceof FormData);
  assert.deepEqual(uploads[0].options.body.getAll("file"), [pickerFile]);

  const droppedFiles = [
    new File(["first"], "first.md", { type: "text/markdown" }),
    new File(["second"], "second.pdf", { type: "application/pdf" }),
  ];
  drop.classList.add("hot");
  let populatedDropPrevented = 0;
  drop.dispatchEvent({
    type: "drop",
    dataTransfer: { files: droppedFiles },
    preventDefault: () => { populatedDropPrevented++ },
  });
  await flush();

  uploads = requests.filter(({ path }) => path === "/kb/upload");
  assert.equal(populatedDropPrevented, 1);
  assert.equal(drop.classList.contains("hot"), false);
  assert.equal(uploads.length, 2);
  assert.equal(uploads[1].options.method, "POST");
  assert.ok(uploads[1].options.body instanceof FormData);
  assert.deepEqual(uploads[1].options.body.getAll("file"), droppedFiles);
});

test("Agents renders the legacy page contract and keeps a failed draft focused", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  const requests = [];
  const page = createAgentsPage({
    request: async (path, options = {}) => {
      requests.push({ path, options });
      if (path === "/agent") return {
        ready: false,
        placeholders: [{ key: "TEN_BOT", count: 1, sample: "Tên bot" }],
        model: "haiku",
        tools: ["search"],
        persona_name: "persona.md",
        persona_size: 1024,
        roster_path: "D:\\brain\\reference\\persona\\roster.md",
        overlay_dir: "D:\\brain\\overlays",
        kb_roots: ["D:\\brain\\wiki"],
      };
      if (path === "/agent/persona/persona" && !options.method) {
        return { label: "Văn phong", path: "persona.md", text: "Bản gốc {{TEN_BOT}}" };
      }
      if (path === "/agent/persona/persona" && options.method === "PUT") {
        throw new Error("không lưu được");
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  t.after(dom.restore);

  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();

  assert.equal(text(find(main, (node) => node.tagName === "H1")), "AI Agents");
  assert.equal(
    text(find(main, (node) => hasClass(node, "sub"))),
    "Một agent: con trả lời tin nhắn Zalo. Giọng nói của nó nằm trong tệp văn phong.",
  );
  assert.equal(
    text(find(main, (node) => hasClass(node, "banner"))),
    "CHƯA sẵn sàng: văn phong còn 1 chỗ trốngĐiền hết bên dưới rồi bấm Lưu. Chưa điền thì bot sẽ gửi cho khách nguyên chữ trong ngoặc.",
  );
  const banner = find(main, (node) => hasClass(node, "banner"));
  assert.ok(find(banner, (node) => hasClass(node, "bt")));
  assert.ok(find(banner, (node) => hasClass(node, "bd")));
  const fields = findAll(main, (node) => hasClass(node, "field"));
  assert.equal(fields.length, 1);
  for (const className of ["fk", "fc", "fs"]) assert.ok(find(fields[0], (node) => hasClass(node, className)));
  const pageQuickInput = find(fields[0], (node) => node.tagName === "INPUT");
  const quickLabel = find(fields[0], (node) => node.tagName === "LABEL");
  assert.ok(pageQuickInput.id);
  assert.equal(quickLabel.getAttribute("for"), pageQuickInput.id);
  const facts = find(main, (node) => hasClass(node, "facts"));
  assert.ok(facts);
  const factRows = findAll(facts, (node) => hasClass(node, "fact"));
  assert.ok(factRows.length >= 2);
  for (const className of ["fkk", "fvv", "fn"]) assert.ok(find(facts, (node) => hasClass(node, className)));
  const pens = findAll(main, (node) => hasClass(node, "pen"));
  assert.equal(pens.length, 2);

  pens[0].click();
  await flush();
  const sheet = find(document.body, (node) => hasClass(node, "sheetback"));
  assert.ok(sheet);
  assert.equal(sheet.firstElementChild.className, "sheet");
  assert.equal(sheet.firstElementChild.getAttribute("role"), "dialog");
  assert.equal(sheet.firstElementChild.getAttribute("aria-modal"), "true");
  assert.ok(sheet.firstElementChild.getAttribute("aria-labelledby"));
  assert.ok(sheet.firstElementChild.getAttribute("aria-describedby"));
  const textarea = find(sheet, (node) => node.tagName === "TEXTAREA");
  assert.ok(textarea);
  assert.ok(textarea.id);
  assert.ok(find(sheet, (node) => node.tagName === "LABEL" && node.getAttribute("for") === textarea.id));
  for (const className of ["sheethead", "quick", "sheetfoot"]) assert.ok(find(sheet, (node) => hasClass(node, className)));
  const quickInput = find(find(sheet, (node) => hasClass(node, "quick")), (node) => node.tagName === "INPUT");
  assert.ok(quickInput);
  quickInput.value = "An";
  let quickPrevented = 0;
  quickInput.dispatchEvent({ type: "keydown", key: "Enter", preventDefault: () => { quickPrevented++ } });
  assert.equal(quickPrevented, 1);
  assert.equal(textarea.value, "Bản gốc An");
  textarea.value = "Bản nháp chưa lưu";
  const footer = find(sheet, (node) => hasClass(node, "sheetfoot"));
  const close = find(footer, (node) => node.tagName === "BUTTON" && text(node) === "Đóng");
  assert.equal(close.className, "btn");
  const save = find(sheet, (node) => node.tagName === "BUTTON" && text(node) === "Lưu");
  assert.equal(save.className, "btn go");
  save.focus();
  let tabPrevented = 0;
  sheet.dispatchEvent({ type: "keydown", key: "Tab", preventDefault: () => { tabPrevented++ } });
  assert.equal(tabPrevented, 1);
  assert.equal(document.activeElement, textarea);
  textarea.focus();
  sheet.dispatchEvent({ type: "keydown", key: "Tab", shiftKey: true, preventDefault: () => { tabPrevented++ } });
  assert.equal(tabPrevented, 2);
  assert.equal(document.activeElement, save);
  save.focus();
  const form = find(sheet, (node) => node.tagName === "FORM");
  form.dispatchEvent({ type: "submit" });
  await flush();

  assert.equal(textarea.value, "Bản nháp chưa lưu");
  assert.equal(document.activeElement, textarea);
  assert.equal(save.disabled, false);
  assert.match(text(footer), /không lưu được/);
  assert.equal(requests.at(-1).options.method, "PUT");

  close.click();
  assert.equal(document.activeElement, pens[0]);
  pens[0].click();
  await flush();
  document.dispatchKeydown({ key: "Escape" });
  assert.equal(document.activeElement, pens[0]);
});

test("Agents keeps only the latest editor when document reads resolve out of order", async (t) => {
  const dom = installDOM();
  const keys = installKeyDispatcher(document);
  let resolvePersona;
  const page = createAgentsPage({
    request: (path, options = {}) => {
      if (path === "/agent") return Promise.resolve({ ready: true, placeholders: [], tools: [], kb_roots: [] });
      if (path === "/agent/persona/persona" && !options.method) return new Promise((resolve) => { resolvePersona = resolve; });
      if (path === "/agent/persona/roster" && !options.method) return Promise.resolve({ label: "Sổ tay thành viên", path: "roster.md", text: "Roster" });
      throw new Error(`Unexpected request: ${path} ${options.method || "GET"}`);
    },
  });
  t.after(dom.restore);

  const main = document.createElement("main");
  const mounted = page.mount(main);
  t.after(mounted.dispose);
  await flush();
  const [persona, roster] = findAll(main, (node) => hasClass(node, "pen"));
  persona.click();
  roster.click();
  await flush();
  resolvePersona({ label: "Văn phong", path: "persona.md", text: "Persona" });
  await flush();

  const overlays = findAll(document.body, (node) => hasClass(node, "sheetback"));
  assert.equal(overlays.length, 1);
  assert.match(text(overlays[0]), /Sổ tay thành viên/);
  assert.equal(keys.listenerCount(), 1);
  mounted.dispose();
  assert.equal(findAll(document.body, (node) => hasClass(node, "sheetback")).length, 0);
  assert.equal(keys.listenerCount(), 0);
});

test("Agents ignores a document save that resolves after its sheet closes", async (t) => {
  const dom = installDOM();
  installKeyDispatcher(document);
  let resolveSave;
  let agentLoads = 0;
  const page = createAgentsPage({
    request: (path, options = {}) => {
      if (path === "/agent") {
        agentLoads++;
        return Promise.resolve({ ready: true, placeholders: [], tools: [], kb_roots: [] });
      }
      if (path === "/agent/persona/persona" && !options.method) {
        return Promise.resolve({ label: "Văn phong", path: "persona.md", text: "Bản gốc" });
      }
      if (path === "/agent/persona/persona" && options.method === "PUT") {
        return new Promise((resolve) => { resolveSave = resolve; });
      }
      throw new Error(`Unexpected request: ${path}`);
    },
  });
  t.after(page.dispose);
  t.after(dom.restore);

  const main = document.createElement("main");
  page.mount(main);
  await flush();
  findAll(main, (node) => hasClass(node, "pen"))[0].click();
  await flush();
  const sheet = find(document.body, (node) => hasClass(node, "sheetback"));
  find(sheet, (node) => node.tagName === "FORM").dispatchEvent({ type: "submit" });
  find(sheet, (node) => node.tagName === "BUTTON" && text(node) === "Đóng").click();
  resolveSave({});
  await flush();

  assert.equal(find(document.body, (node) => hasClass(node, "sheetback")), null);
  assert.equal(agentLoads, 1);
});
