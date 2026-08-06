import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { NAVIGATION, ROUTES } from "../overlay/internal/webui/static/core/router.js";
import { renderNavigation } from "../overlay/internal/webui/static/core/shell.js";
import { createRoadmapPage } from "../overlay/internal/webui/static/pages/roadmap.js";
import { find, findAll, installDOM, text } from "./helpers/dom-harness.mjs";

const staticRoot = new URL("../overlay/internal/webui/static/", import.meta.url);

test("desktop navigation renders every group and roadmap pages stay offline", (t) => {
  const dom = installDOM();
  assert.equal(typeof dom.restore, "function");
  t.after(dom.restore);
  const hadFetch = Object.hasOwn(globalThis, "fetch");
  const savedFetch = globalThis.fetch;
  let fetchCount = 0;
  globalThis.fetch = () => {
    fetchCount += 1;
    throw new Error("roadmap pages must not call fetch");
  };
  t.after(() => {
    if (hadFetch) globalThis.fetch = savedFetch;
    else delete globalThis.fetch;
  });

  const nav = document.createElement("nav");
  renderNavigation({ container: nav, groups: NAVIGATION, activeId: "knowledge" });

  const headings = findAll(nav, (node) => node.classList?.contains("railhead"));
  assert.deepEqual(headings.map(text), ["BUILD", "OPERATE", "GOVERN", "SYSTEM"]);

  const items = findAll(nav, (node) => node.classList?.contains("nav"));
  assert.equal(items.length, 14);
  assert.ok(find(nav, (node) => node.dataset?.route === "providers"));
  const knowledge = find(nav, (node) => node.dataset?.route === "knowledge");
  assert.equal(knowledge.getAttribute("aria-current"), "page");
  const workflows = find(nav, (node) => node.dataset?.route === "workflows");
  assert.equal(workflows.classList.contains("todo"), true);
  const conversations = find(nav, (node) => node.getAttribute?.("href") === "/zalo");
  assert.ok(conversations);

  const icon = knowledge.firstElementChild;
  assert.equal(icon.tagName, "SPAN");
  assert.equal(icon.className, "ic");
  assert.equal(icon.getAttribute("aria-hidden"), "true");

  const page = document.createElement("section");
  const disposer = createRoadmapPage(ROUTES.workflows).mount(page);
  assert.equal(text(page), `WorkflowsPhần này chưa có.${ROUTES.workflows.todo}`);
  assert.equal(typeof disposer.dispose, "function");
  assert.equal(fetchCount, 0);
});

test("desktop shell loads isolated Portal CSS with the legacy rail dimensions", async () => {
  const [index, css] = await Promise.all([
    readFile(new URL("index.html", staticRoot), "utf8"),
    readFile(new URL("portal.css", staticRoot), "utf8"),
  ]);

  assert.match(index, /href="\/assets\/app\.css"/);
  assert.match(index, /href="\/assets\/portal\.css"/);
  assert.match(index, /id="rail"/);
  assert.match(index, /id="main"/);
  const portalBody = css.match(/\.portal-body\s*\{([^}]*)\}/s)?.[1] || "";
  for (const token of ["panel", "sunk", "line", "ink", "dim", "live"]) {
    assert.doesNotMatch(
      portalBody,
      new RegExp(`--${token}\\s*:`),
      `Portal must inherit the legacy --${token} token supplied by app.css`,
    );
  }
  assert.match(css, /#rail\s*\{[^}]*width:\s*208px/s);
  assert.match(css, /#main\s*\{[^}]*padding:\s*22px 26px/s);
  assert.match(css, /@media\s*\(max-width:\s*720px\)/);
  assert.match(css, /#rail\.is-open\s*\{/);
  assert.match(css, /#rail-toggle\s*\{/);
  // Outline hướng vào trong là bản vá cho #main chạm mép; một trang mới không được xoá nó.
  assert.match(css, /#main:focus-visible\s*\{[^}]*outline-offset:\s*-2px/s);
});

// providerRegions lấy phần thân giữa mỗi cặp dấu providers:begin/end.
//
// Cắt theo DẤU chứ không lọc theo chữ "provider" trong selector: một luật xổng phạm vi là một
// luật KHÔNG còn chữ đó, nên lọc theo tên chỉ soi được đúng những luật vốn đã đúng.
function providerRegions(css) {
  return [...css.matchAll(/\/\* providers:begin[\s\S]*?\*\/([\s\S]*?)\/\* providers:end \*\//g)]
    .map(([, body]) => body);
}

// selectorsIn tách theo "}" chứ không theo dòng, nên một danh sách selector trải nhiều dòng vẫn
// được xét đủ từng phần.
function selectorsIn(region) {
  return region
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .split("}")
    .map((rule) => rule.split("{")[0].trim())
    .filter(Boolean)
    .flatMap((list) => list.split(",").map((part) => part.trim()).filter(Boolean));
}

test("every rule in the Provider CSS regions stays scoped to its page or sheet", async () => {
  const css = await readFile(new URL("portal.css", staticRoot), "utf8");

  const regions = providerRegions(css);
  assert.equal(regions.length, 2, "expected a desktop and a mobile Provider region");

  const selectors = regions.flatMap(selectorsIn);
  assert.ok(selectors.length >= 20, `expected the Provider rules inside the markers, got ${selectors.length}`);
  for (const selector of selectors) {
    assert.match(
      selector,
      /^\.providers-page\b|^\.provider-sheet\b/,
      `Provider rule "${selector}" must stay scoped`,
    );
  }
});
