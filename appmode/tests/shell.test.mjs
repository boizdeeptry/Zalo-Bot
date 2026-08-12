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

// regionsFor lấy phần thân giữa mỗi cặp dấu <marker>:begin/end.
//
// Cắt theo DẤU chứ không lọc theo chữ trong selector: một luật xổng phạm vi là một
// luật KHÔNG còn tiền tố đó, nên lọc theo tên chỉ soi được đúng những luật vốn đã đúng.
function regionsFor(css, marker) {
  const re = new RegExp(`/\\* ${marker}:begin[\\s\\S]*?\\*/([\\s\\S]*?)/\\* ${marker}:end \\*/`, "g");
  return [...css.matchAll(re)].map(([, body]) => body);
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

function findMatchingBrace(css, open) {
  let depth = 0;
  for (let index = open; index < css.length; index += 1) {
    const char = css[index];
    if (char === "{") depth += 1;
    if (char === "}") {
      depth -= 1;
      if (depth === 0) return index;
    }
  }
  return -1;
}

function styleRulesIn(css) {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const rules = [];

  function collect(block) {
    let cursor = 0;
    while (cursor < block.length) {
      const open = block.indexOf("{", cursor);
      if (open < 0) return;
      const selector = block.slice(cursor, open).trim();
      const close = findMatchingBrace(block, open);
      if (close < 0) return;
      const body = block.slice(open + 1, close);
      if (selector.startsWith("@")) {
        collect(body);
      } else if (selector) {
        rules.push({ selector, text: `${selector}{${body}}` });
      }
      cursor = close + 1;
    }
  }

  collect(text);
  return rules;
}

test("onboarding selectors are scoped away from legacy Portal pages", async () => {
  const css = await readFile(new URL("portal.css", staticRoot), "utf8");
  const selectors = styleRulesIn(css)
    .filter((rule) => /onboarding|wizard/i.test(rule.text))
    .flatMap((rule) => rule.selector.split(",").map((selector) => selector.trim()).filter(Boolean));

  assert.ok(selectors.length > 0, "expected onboarding or wizard selectors in portal.css");
  for (const selector of selectors) {
    assert.ok(
      selector.includes("[data-onboarding]") || selector.includes(".onboarding-shell"),
      `onboarding selector "${selector}" must stay scoped to the wizard`,
    );
  }
});

// Mỗi khối marker phải giữ mọi luật trong tầm trang của nó. .provider-sheet là lớp cùng-sheet
// duy nhất được phép ngoài .providers-page (detail dùng lại) — giữ nguyên allowance cũ, không nới.
const SCOPED_REGIONS = [
  { label: "Provider", marker: "providers", regions: 2, minSelectors: 20, scope: /^\.providers-page\b|^\.provider-sheet\b/ },
  { label: "Models", marker: "models", regions: 2, minSelectors: 10, scope: /^\.models-page\b/ },
  { label: "Combos", marker: "combos", regions: 1, minSelectors: 10, scope: /^\.combos-page\b/ },
];

for (const { label, marker, regions: regionCount, minSelectors, scope } of SCOPED_REGIONS) {
  test(`every rule in the ${label} CSS regions stays scoped to its page or sheet`, async () => {
    const css = await readFile(new URL("portal.css", staticRoot), "utf8");

    const regions = regionsFor(css, marker);
    assert.equal(regions.length, regionCount, `expected ${regionCount} ${label} region(s)`);

    const selectors = regions.flatMap(selectorsIn);
    assert.ok(
      selectors.length >= minSelectors,
      `expected the ${label} rules inside the markers, got ${selectors.length}`,
    );
    for (const selector of selectors) {
      assert.match(selector, scope, `${label} rule "${selector}" must stay scoped`);
    }
  });
}
