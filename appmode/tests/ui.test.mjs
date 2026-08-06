import test from "node:test";
import assert from "node:assert/strict";

import { NAVIGATION } from "../overlay/internal/webui/static/core/router.js";
import { createRailNavigation, renderNavigation } from "../overlay/internal/webui/static/core/shell.js";
import { find, installDOM } from "./helpers/dom-harness.mjs";

test("mobile rail closes after an internal navigation route and disposal detaches controls", (t) => {
  const dom = installDOM();
  t.after(dom.restore);

  const toggle = document.createElement("button");
  const rail = document.createElement("aside");
  const navigation = document.createElement("nav");
  rail.append(navigation);
  renderNavigation({ container: navigation, groups: NAVIGATION, activeId: "knowledge" });

  const controller = createRailNavigation({ toggle, rail, navigation });
  toggle.click();
  assert.equal(toggle.getAttribute("aria-expanded"), "true");
  assert.equal(rail.classList.contains("is-open"), true);

  const knowledge = find(navigation, (node) => node.dataset?.route === "knowledge");
  navigation.dispatchEvent({ type: "click", target: knowledge.firstElementChild });
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
  assert.equal(rail.classList.contains("is-open"), false);

  controller.dispose();
  toggle.click();
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
});
