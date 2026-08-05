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
  assert.equal(items.length, 13);
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
  assert.match(css, /\.portal-body\s*\{[^}]*--panel:\s*var\(--surface\)/s);
  assert.match(css, /\.portal-body\s*\{[^}]*--sunk:\s*var\(--surface-soft\)/s);
  assert.match(css, /\.portal-body\s*\{[^}]*--line:\s*var\(--border\)/s);
  assert.match(css, /\.portal-body\s*\{[^}]*--ink:\s*var\(--text\)/s);
  assert.match(css, /\.portal-body\s*\{[^}]*--dim:\s*var\(--muted\)/s);
  assert.match(css, /\.portal-body\s*\{[^}]*--live:\s*var\(--accent\)/s);
  assert.match(css, /#rail\s*\{[^}]*width:\s*208px/s);
  assert.match(css, /#main\s*\{[^}]*padding:\s*22px 26px/s);
  assert.match(css, /@media\s*\(max-width:\s*720px\)/);
  assert.match(css, /#rail\.is-open\s*\{/);
  assert.match(css, /#rail-toggle\s*\{/);
});
