import test from "node:test";
import assert from "node:assert/strict";

import { createKnowledgePage } from "../overlay/internal/webui/static/pages/knowledge.js";
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
  t.after(page.dispose);
  t.after(dom.restore);

  const main = document.createElement("main");
  page.mount(main);
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
