# AGS-visual onboarding — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Replace the current two-column onboarding wizard presentation with an onboarding-only AGS-style dashboard and non-dismissible Agent Setup dialog without changing workflow or backend behavior.

**TDD mode:** yes

**Architecture:** `renderOnboardingShell()` will own a decorative, non-interactive dashboard plus one semantic dialog shared by every onboarding phase. Existing phase creators, services, Provider Connect, startup gating, and backend contracts stay intact; scoped CSS supplies the AGS visual language and responsive behavior.

**Tech stack:** browser-native ES modules and DOM APIs, Node `node:test` with the repository DOM harness, scoped CSS; no new dependency.

**Spec:** `.planning/specs/2026-08-14-ags-visual-onboarding-design.md`

**Research:** skipped — small presentation-only change; the installed AGS runtime and reference screenshots were inspected directly.

---

## File structure

- `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js`
  - Owns the shared onboarding shell markup and provider-row presentation.
  - Adds only decorative dashboard nodes, dialog semantics, provider marks, and visual switches.
  - Does not own service calls, revisions, phase transitions, or retries.
- `appmode/overlay/internal/webui/static/portal.css`
  - Owns all AGS visual tokens, desktop geometry, modal styling, row/toggle states, forms, progress, and responsive rules.
  - Every onboarding selector remains scoped beneath `.onboarding-shell`.
- `appmode/tests/onboarding-ags-setup.test.mjs`
  - Verifies the observable AGS shell, dialog semantics, non-interactive decoration, provider switches, and local-only selection.
- `appmode/tests/onboarding-early.test.mjs`
  - Verifies authoritative phase changes keep the same non-dismissible dialog shell.
- `appmode/tests/shell.test.mjs`
  - Verifies the scoped CSS contract contains the reference geometry, palette, grid, backdrop, toggle, and narrow-screen behavior.

### Task 1: Render every onboarding phase in one AGS Agent Setup dialog

**Files:**
- Modify: `appmode/tests/onboarding-ags-setup.test.mjs`
- Modify: `appmode/tests/onboarding-early.test.mjs` (authoritative phase loop around current lines 557-584)
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js` (shared shell and provider rows)

**Public behavior to verify:** Opening or advancing onboarding always shows one accessible, non-dismissible Agent Setup dialog over a non-interactive AGS-style dashboard, while selecting Claude or Codex remains local until the existing Continue action.

- [ ] **Step 1: Write failing public-DOM tests**

  Add this helper to `onboarding-ags-setup.test.mjs` and call it from the provider, connect, and setup tests:

  ```js
  function assertAGSFrame(host) {
    const dashboards = findAll(host, (node) => hasClass(node, "onboarding-dashboard"));
    const dialogs = findAll(host, (node) => hasClass(node, "onboarding-dialog"));
    assert.equal(dashboards.length, 1);
    assert.equal(dialogs.length, 1);
    const [dashboard] = dashboards;
    const [dialog] = dialogs;
    assert.equal(dashboard.getAttribute("aria-hidden"), "true");
    const forbidden = findAll(dashboard, (node) => {
      const tag = node.tagName ?? "";
      const role = node.getAttribute?.("role") ?? "";
      return ["BUTTON", "A", "INPUT", "SELECT", "TEXTAREA", "FORM"].includes(tag)
        || /^H[1-6]$/u.test(tag)
        || node.hasAttribute?.("tabindex")
        || node.hasAttribute?.("contenteditable")
        || ["button", "link", "checkbox", "switch", "textbox"].includes(role);
    });
    assert.equal(forbidden.length, 0);
    assert.equal(dialog.getAttribute("role"), "dialog");
    assert.equal(dialog.getAttribute("aria-modal"), "true");
    assert.equal(dialog.getAttribute("aria-labelledby"), "onboarding-dialog-title");
    assert.equal(document.activeElement, dialog);
    const dismissers = findAll(dialog, (node) => node.tagName === "BUTTON"
      && /đóng|bỏ qua|close|skip/iu.test(`${text(node)} ${node.getAttribute("aria-label") ?? ""}`));
    assert.equal(dismissers.length, 0);
    return { dashboard, dialog };
  }
  ```

  Extend the provider assertion to require `.onboarding-provider-mark`, `.onboarding-agent-switch`, and one selected switch. Assert the visible row order is exactly `claude-code`, then `codex`. Keep the existing assertion that clicking Claude performs zero `selectProvider` calls.

  Extend the Connect test to require one read-only `.onboarding-connect-provider-row` for the exact staged provider, with its mark, status text, and selected switch.

  Dispatch `keydown` with `Escape` on the dialog and click the backdrop, then assert the same dialog remains mounted. This proves the surface has no dismissal handler rather than relying on a private data attribute.

  In the authoritative phase loop in `onboarding-early.test.mjs`, add:

  ```js
  const dialog = byClass(host, "onboarding-dialog");
  assert.ok(dialog, `${name} must remain inside the Agent Setup dialog`);
  assert.equal(dialog.getAttribute("aria-modal"), "true");
  assert.ok(byClass(host, "onboarding-dashboard"));
  ```

- [ ] **Step 2: Run the focused tests and confirm RED**

  Run:

  ```powershell
  node --test appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs
  ```

  Expected: FAIL because `.onboarding-dashboard`, `.onboarding-dialog`, provider marks, and visual switches do not exist.

- [ ] **Step 3: Implement the minimum shared shell and provider markup**

  Add a decorative dashboard builder that uses only non-interactive elements:

  ```js
  function decorativeDashboard() {
    return element("div", { className: "onboarding-dashboard", attributes: { "aria-hidden": "true" } },
      element("div", { className: "onboarding-dashboard-topbar" },
        element("span", { className: "onboarding-dashboard-menu", text: "☰" }),
        element("span", { className: "onboarding-dashboard-agent", text: "◆" }),
        element("span", { className: "onboarding-dashboard-grid", text: "▦" }),
      ),
      element("div", { className: "onboarding-dashboard-sidebar" },
        element("div", { className: "onboarding-dashboard-brand", text: "TUVANZALO" }),
        element("div", { className: "onboarding-dashboard-subtitle", text: "Trợ lý Zalo trên máy của bạn" }),
        element("div", { className: "onboarding-dashboard-project", text: "▰  Chọn trợ lý" }),
        element("div", { className: "onboarding-dashboard-empty", text: "Hoàn tất cài đặt để bắt đầu." }),
      ),
      element("div", { className: "onboarding-dashboard-canvas" },
        element("div", { className: "onboarding-dashboard-summary" },
          element("span", { className: "onboarding-dashboard-eyebrow", text: "AGENT" }),
          element("div", { className: "onboarding-dashboard-count", text: "0/2 sẵn sàng" }),
          element("div", { className: "onboarding-dashboard-used", text: "Đang dùng: Chưa thiết lập" }),
          element("div", { className: "onboarding-dashboard-unused", text: "Chưa dùng: Claude Code, Codex" }),
          element("div", { className: "onboarding-dashboard-manage", text: "✧  Quản lý Agents" }),
        ),
      ),
    );
  }
  ```

  Replace the shared shell with one decorative dashboard and one semantic dialog:

  ```js
  export function renderOnboardingShell(root, phase, content) {
    const dialog = element("section", {
      className: "onboarding-dialog",
      attributes: {
        role: "dialog",
        "aria-modal": "true",
        "aria-labelledby": "onboarding-dialog-title",
        tabindex: "-1",
      },
    },
    element("aside", { className: "onboarding-sidebar" },
      element("span", { className: "onboarding-brand", attributes: { id: "onboarding-dialog-title" }, text: "Cài đặt Agent" }),
      rail(phase),
      element("span", { className: "onboarding-dialog-lock", attributes: { "aria-hidden": "true" }, text: "◆" }),
    ),
    element("div", { className: "onboarding-main" }, content));
    root.replaceChildren(element("div", { className: "onboarding-shell" },
      decorativeDashboard(),
      element("div", { className: "onboarding-backdrop" }, dialog),
    ));
    dialog.focus({ preventScroll: true });
  }
  ```

  The startup owner already hides and marks the real Portal rail/navigation `inert`, the decorative dashboard has no focusable nodes, and the shared dialog receives initial focus after every phase render. Do not add a second custom focus-trap implementation; the existing inert boundary supplies containment and Portal route mounting restores focus to `#main` after completion.

  Change provider ordering to Claude Code then Codex. Add a provider mark and a visual switch inside each existing provider button; keep `aria-pressed`, callbacks, one-flight protection, and status text unchanged:

  ```js
  element("span", {
    className: `onboarding-provider-mark onboarding-provider-mark--${kind}`,
    attributes: { "aria-hidden": "true" },
    text: kind === "claude-code" ? "✣" : "◎",
  })
  ```

  ```js
  element("span", {
    className: `onboarding-agent-switch${selected ? " is-on" : ""}`,
    attributes: { "aria-hidden": "true" },
  }, element("span", { className: "onboarding-agent-switch-knob" }))
  ```

  Add a read-only selected-provider row to `createConnectStage()` using the same mark/status/switch builder. It must have class `.onboarding-connect-provider-row`, contain the exact `providerName(kind)`, and expose no click handler.

- [ ] **Step 4: Run focused tests and confirm GREEN**

  Run:

  ```powershell
  node --test appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs
  ```

  Expected: all tests pass.

- [ ] **Step 5: Refactor only if the shell builder is duplicated or unclear**

  Keep decoration in one function and preserve all existing phase creators. Re-run the focused command after any cleanup.

- [ ] **Step 6: Commit the DOM slice**

  ```powershell
  git add -- appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs appmode/overlay/internal/webui/static/pages/onboarding-early-view.js
  git commit -m "feat: frame onboarding as AGS setup dialog"
  ```

### Task 2: Match AGS desktop geometry, palette, toggles, and responsive behavior

**Files:**
- Modify: `appmode/tests/shell.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/portal.css` (onboarding block around current lines 436-653 and responsive block around current lines 805-830)

**Public behavior to verify:** At desktop size onboarding presents the recognizable AGS shell and centered modal; at narrow size the decoration yields to a scroll-safe, touch-usable dialog without leaking styles into Portal pages.

- [ ] **Step 1: Write a failing scoped-CSS contract test**

  Add a test that reads `portal.css` and extracts the onboarding region. Assert the observable design contract:

  ```js
  test("onboarding CSS carries the scoped AGS visual contract", async () => {
    const css = await readFile(new URL("portal.css", staticRoot), "utf8");
    assert.match(css, /\.onboarding-shell\s*\{[^}]*--onboarding-topbar:\s*#070a15/s);
    assert.match(css, /\.onboarding-shell \.onboarding-dashboard-topbar\s*\{[^}]*height:\s*47px/s);
    assert.match(css, /\.onboarding-shell \.onboarding-dashboard-sidebar\s*\{[^}]*width:\s*292px/s);
    assert.match(css, /\.onboarding-shell \.onboarding-dashboard-canvas\s*\{[^}]*linear-gradient\(45deg/s);
    assert.match(css, /\.onboarding-shell \.onboarding-backdrop\s*\{[^}]*rgba\(0,\s*0,\s*0,\s*\.5\)/s);
    assert.match(css, /\.onboarding-shell \.onboarding-dialog\s*\{[^}]*width:\s*min\(560px,\s*calc\(100vw - 32px\)\)/s);
    assert.match(css, /\.onboarding-shell \.onboarding-agent-switch\s*\{[^}]*width:\s*46px[^}]*height:\s*26px/s);
    assert.match(css, /@media\s*\(max-width:\s*900px\)[\s\S]*\.onboarding-shell \.onboarding-dashboard-sidebar/s);
  });
  ```

  Keep the existing selector-scope test unchanged.

- [ ] **Step 2: Run the CSS test and confirm RED**

  Run:

  ```powershell
  node --test appmode/tests/shell.test.mjs
  ```

  Expected: FAIL because the current onboarding block still describes the Portal two-column wizard and lacks the AGS visual tokens and geometry.

- [ ] **Step 3: Implement the scoped AGS skin**

  Define onboarding-only tokens on `.onboarding-shell` and make it a full-viewport fixed presentation layer:

  ```css
  .onboarding-shell {
    --onboarding-topbar:#070a15;
    --onboarding-deep:#0a0e1c;
    --onboarding-canvas:#0b1020;
    --onboarding-surface:#141a2e;
    --onboarding-border:#313647;
    --onboarding-divider:#282b38;
    --onboarding-text:#ededee;
    --onboarding-muted:#94969f;
    --onboarding-disabled:#555967;
    --onboarding-cyan:#54d9f4;
    --onboarding-orange:#f59e0b;
    --onboarding-lime:#a9e127;
    position:fixed;
    inset:0;
    z-index:80;
    overflow:hidden;
    color:var(--onboarding-text);
    background:var(--onboarding-canvas);
    font-family:"Cascadia Mono",SFMono-Regular,Consolas,monospace;
  }
  ```

  Implement these exact layout responsibilities:

  - `.onboarding-dashboard-topbar`: fixed top row, 47px height, `#070a15`, subtle bottom border.
  - `.onboarding-dashboard-sidebar`: left edge below the bar, 292px width, deep surface, right divider, brand stripe, project selector, and footer-like decoration.
  - `.onboarding-dashboard-canvas`: inset `47px 0 0 292px`, centered content, two 45-degree gradients with `18px 18px` background size.
  - `.onboarding-dashboard-summary`: width bounded to 430px, raised vertical gradient, 1px border, 13px radius, cyan left rail.
  - `.onboarding-backdrop`: viewport overlay using `rgba(0,0,0,.5)`, centered dialog, 16px padding.
  - `.onboarding-dialog`: width `min(560px, calc(100vw - 32px))`, maximum height `calc(100dvh - 32px)`, raised surface, 1px border, 12px radius, internal scrolling, strong shadow.
  - `.onboarding-sidebar`: sticky 64px dialog header with title, compact horizontal progress rail, divider, and non-interactive lock glyph.
  - Provider rows: flat 66px rows with separators, 26px marks, muted off state, visible selected state, and no card gaps.
  - `.onboarding-agent-switch`: 46x26px pill with 20px knob; selected state translates knob 20px and applies the lime track/glow.
  - Existing forms, alerts, progress bars, logs, and actions: retain their selectors but recolor and resize them inside the dialog.

  Add a narrow layout:

  ```css
  @media (max-width:900px) {
    .onboarding-shell .onboarding-dashboard-sidebar { display:none; }
    .onboarding-shell .onboarding-dashboard-canvas { left:0; }
    .onboarding-shell .onboarding-dashboard-summary { width:min(430px, calc(100% - 32px)); }
  }

  @media (max-width:600px) {
    .onboarding-shell .onboarding-backdrop { padding:16px; }
    .onboarding-shell .onboarding-dialog { width:calc(100vw - 32px); max-height:calc(100dvh - 32px); }
    .onboarding-shell .onboarding-sidebar { min-height:58px; padding:10px 12px; }
    .onboarding-shell .onboarding-stage { padding:14px; }
    .onboarding-shell .onboarding-agent-row { min-height:62px; padding:10px 12px; }
  }
  ```

- [ ] **Step 4: Run CSS, DOM, and shell tests and confirm GREEN**

  Run:

  ```powershell
  node --test appmode/tests/shell.test.mjs appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs
  ```

  Expected: all tests pass, including the existing onboarding selector scope guard.

- [ ] **Step 5: Refactor the CSS while green**

  Remove obsolete two-column wizard rules, keep every selector scoped, and avoid redefining legacy Portal tokens on `.portal-body`. Re-run the Task 2 GREEN command.

- [ ] **Step 6: Commit the visual slice**

  ```powershell
  git add -- appmode/tests/shell.test.mjs appmode/overlay/internal/webui/static/portal.css
  git commit -m "feat: match AGS onboarding visuals"
  ```

## Final verification and preview build

- [ ] Run the complete onboarding/startup regression group:

  ```powershell
  node --test appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs appmode/tests/onboarding.test.mjs appmode/tests/onboarding-recovery.test.mjs appmode/tests/onboarding-regressions.test.mjs appmode/tests/provider-connect.test.mjs appmode/tests/app-main.test.mjs appmode/tests/shell.test.mjs
  ```

- [ ] Run the full Portal suite:

  ```powershell
  npm --prefix appmode test
  ```

- [ ] Run syntax, line-cap, diff, and independent-review checks:

  ```powershell
  node --check appmode/overlay/internal/webui/static/pages/onboarding-early-view.js
  git diff --check
  $uiLines = (Get-Content appmode/overlay/internal/webui/static/pages/onboarding-early-view.js).Count
  $testLines = (Get-Content appmode/tests/onboarding-early.test.mjs).Count
  if ($uiLines -gt 800 -or $testLines -gt 800) { throw "800-line cap exceeded" }
  ```

  Expected: syntax exit `0`, no diff errors, both files at or below 800 lines, and an independent reviewer reports no Critical or Important finding.

- [ ] Compare the running preview at desktop `2048x1024` and narrow `390x844` viewports against the reference screenshots. Pass criteria: visible 47px top bar, 292px desktop sidebar, diamond grid, centered summary card, half-opacity backdrop, centered bounded dialog, intact 16px narrow margins, no horizontal overflow, and no focusable decorative node.

- [ ] Build a fresh clean preview package from the verified commit:

  ```powershell
  pwsh -NoProfile -File .\build-app.ps1 `
    -Repo 'D:\TuvanZalo\_artifacts\agentdc-src' `
    -Out 'D:\TuvanZalo\_artifacts\ags-visual-onboarding-preview' `
    -PersonaSource 'D:\TuvanZalo\brain\reference\persona'
  ```

  Expected: exit `0`; Go, Portal, Zalo compile/test/typecheck, persona, package-cleanliness, and final package gates all pass. Launch it only after confirming the previous preview daemon will not conflict on port `8770`.
