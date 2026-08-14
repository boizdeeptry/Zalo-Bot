# AGS-Visual Onboarding Shell

Date: 2026-08-14
Status: awaiting spec approval
Supersedes the presentation portion of `2026-08-14-ags-like-onboarding-design.md`.

## Goal

Make the gated onboarding experience visually match the installed AGS application closely enough that the same design language is immediately recognizable, while preserving all existing Portal onboarding contracts and safety checks.

The selected scope is onboarding only. After onboarding completes, the user enters the existing Portal UI unchanged.

## Reference and interpretation

The reference is the locally installed AGS `0.9.294` UI running on `127.0.0.1:8765`, plus the two screenshots supplied by the user.

The implementation will independently reproduce the observable layout and interaction language. It will not copy AGS proprietary JavaScript, CSS source, embedded SVG assets, prompts, or branded wording verbatim.

The defining visual characteristics are:

- a full-viewport, dark, monospace application shell;
- a 47px top bar and an approximately 292px left sidebar on desktop;
- a dark diamond-grid main canvas;
- a compact Agent status card centered in the main canvas;
- a dimmed backdrop and a centered Agent Setup modal;
- flat rows, subtle separators, provider logos, compact status copy, and 46x26px lime toggles;
- restrained cyan, orange, lime, amber, and pink-red status accents.

## Chosen approach

Render a self-contained decorative AGS-style dashboard inside the onboarding root, then place one non-dismissible onboarding dialog over it.

The real Portal navigation remains hidden and inert until onboarding completes. Re-enabling it during onboarding would expand the behavior surface, expose unavailable navigation, and weaken the existing startup gate. The decorative shell therefore exists only to provide the AGS visual context and contains no interactive controls.

All onboarding phases reuse the same dialog frame:

`provider -> connect -> setup -> persona -> test -> completed`

Only the content inside the dialog changes. The existing phase machine, revision checks, provider identity checks, receipt handling, retries, and completion gate remain authoritative.

## Architecture

The shell DOM will have this responsibility split:

```text
.onboarding-shell
├── .onboarding-dashboard[aria-hidden="true"]
│   ├── decorative top bar
│   ├── decorative sidebar
│   └── decorative main canvas and Agent summary card
└── .onboarding-backdrop
    └── .onboarding-dialog[role="dialog"][aria-modal="true"]
        ├── compact modal header and progress rail
        └── .onboarding-main
            └── existing phase content
```

The decorative dashboard uses only `div` and `span` elements. It contains no buttons, links, inputs, forms, headings, or focusable elements. This keeps screen readers and keyboard users entirely inside the real onboarding surface.

The onboarding dialog is not dismissible. There is no close button and Escape does not bypass the gate. The header may use a lock/status affordance where AGS displays a close control so the visual balance remains similar without implying that onboarding can be skipped.

## Phase presentation

### Provider

- Modal title: `Cài đặt Agent`.
- Show exactly two interactive rows: Claude Code and Codex.
- Each row contains its provider mark, name, status copy, and an AGS-style switch.
- Selecting a row updates only local UI selection; it does not mutate server configuration.
- Exactly one selected provider can be on at a time.
- The existing explicit `Tiếp tục kết nối` action remains below the rows and owns the server transition.
- The warning that live configuration changes only after verified completion remains visible in a compact contextual panel.

### Connect

- Keep the same modal and selected provider row.
- Reuse the existing Provider Connect component unchanged.
- Show progress percentage, progress bar, current message, and bounded logs inline beneath the selected provider row.
- Cancellation, reconciliation, and retry behavior remain unchanged.

### Setup

- Keep the same modal.
- Replace the wizard-card feeling with one compact queued-work panel.
- Show an indeterminate or measured lime progress treatment and the current setup message.
- On success, transition automatically to Persona as today.

### Persona

- Keep the same modal frame and compact step indication.
- Preserve every required field, placeholder-completeness rule, normalization rule, revision check, and receipt invalidation rule.
- Form inputs use AGS dark surfaces and clear focus rings; no field is removed or made optional.

### Test Chat

- Keep the same modal frame.
- Preserve the prefilled `Xin chào`, the real provider route, name-answer gate, one-flight behavior, retry routes, and RAM-only receipt.
- Failure remains blocking and offers only the existing safe retry/back paths.

### Completed

- Show `Bé Mi đã sẵn sàng!` in the modal with the existing knowledge guidance.
- The existing CTA is the only route into Portal.
- After the CTA succeeds and startup revalidates authoritative status, remove the decorative onboarding shell and reveal the real Portal.

## Visual tokens and layout

All new tokens are scoped beneath `.onboarding-shell` so they cannot alter the existing Portal theme.

Core colors:

- top bar: `#070a15`;
- sidebar/deep surface: `#0a0e1c`;
- main canvas: `#0b1020`;
- modal and raised surface: `#141a2e`;
- primary border: `#313647`;
- softer divider: `#282b38`;
- strong text: approximately `#ededee` to `#ffffff`;
- secondary text: approximately `#94969f`;
- disabled text/icon: approximately `#555967` / `#3b4050`;
- cyan accent: `#54d9f4`;
- orange accent: `#f59e0b`;
- ready/selected lime: `#a9e127`;
- warning amber: approximately `#f5b53b`;
- error pink-red: approximately `#fb7185`.

Typography uses a local monospace stack such as `Cascadia Mono, SFMono-Regular, Consolas, monospace`. Body and row names are 13-14px; supporting status copy is 11-12px; the modal title is approximately 16px.

Desktop geometry:

- top bar: approximately 47px high;
- left sidebar: approximately 292px wide;
- centered summary card: approximately 430x218px;
- modal: `width: min(560px, calc(100vw - 32px))`;
- modal maximum height: `calc(100dvh - 32px)`;
- modal header: approximately 64px high;
- provider rows: approximately 66px high;
- switches: approximately 46x26px with a 20px knob;
- borders are 1px and primary corner radii are 8-13px.

The main canvas uses two subtle diagonal gradients to produce the AGS diamond crosshatch. The modal backdrop is approximately `rgba(0, 0, 0, .5)`. Selected toggles use a muted green track, lime knob, and soft lime glow. Off rows remain visible but muted.

## Responsive behavior

- At narrow widths, the decorative sidebar collapses or disappears because it is non-functional.
- The grid canvas continues to fill the viewport.
- The modal keeps 16px viewport margins, becomes the primary surface, and scrolls internally.
- The modal header remains visible on short screens.
- Provider names and status copy may wrap, but switches stay aligned and reachable.
- Interactive rows never shrink below a practical touch target.
- No horizontal page scrolling is permitted.

## Accessibility

- The real surface exposes exactly one `role="dialog"` with `aria-modal="true"` and an accessible label.
- The decorative dashboard is `aria-hidden="true"` and contains no focusable nodes.
- Existing semantic forms, labels, status regions, progress attributes, and alert regions are retained.
- Focus indicators are clearly visible against dark surfaces.
- Color is not the only indicator: selected, busy, warning, and error states retain text labels.
- There is no close or skip affordance.

## Error handling and safety

- All provider, setup, persona, Test Chat, completion, and reconciliation errors render inside the same dialog.
- Errors do not reveal private provider output or tokens.
- Retry buttons call the existing guarded operations; no new request path is introduced.
- A stale response cannot replace a newer phase because render ownership and generation guards remain unchanged.
- The decorative shell never reads or mutates provider state.

## Change surface

Production changes should be limited to:

- `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js`
  - change the shared shell structure;
  - keep all phase callbacks and service calls unchanged.
- `appmode/overlay/internal/webui/static/portal.css`
  - replace the current two-column wizard styling with the scoped AGS visual shell and responsive rules.

Expected test changes are limited to onboarding and shell DOM/style contracts. No Go backend, API contract, provider connector, route, database, or Portal navigation change is planned.

## Test strategy

Implementation follows strict red-green-refactor TDD.

Add public-DOM tests that fail before production changes and prove:

- every phase renders inside one `.onboarding-dialog`;
- the dialog has `role="dialog"`, `aria-modal="true"`, and an accessible label;
- one decorative dashboard exists and is `aria-hidden`;
- the decorative dashboard contains no headings or interactive controls;
- provider rows expose AGS-style switch semantics while keeping local-only selection;
- Connect, Setup, Persona, Test, and Completed retain the same shell contract;
- no close/skip control exists;
- CSS remains scoped beneath `.onboarding-shell`.

Then run:

- the focused AGS onboarding tests;
- all onboarding, recovery, Provider Connect, app startup, and shell tests;
- the full Portal Node suite;
- packaged static checks;
- visual comparison at a desktop viewport close to 2048x1024 and a narrow mobile viewport.

## Acceptance criteria

- A first glance clearly resembles the supplied AGS screenshots rather than the previous Portal wizard.
- Desktop onboarding visibly contains the AGS-like top bar, left sidebar, diamond grid, centered summary card, dim backdrop, and centered modal.
- Provider rows, toggles, separators, typography, spacing, palette, and modal proportions match the reference closely.
- Only onboarding receives this shell; the completed Portal remains visually unchanged.
- Provider selection still mutates only local state until the explicit connect action.
- Connect progress/logs, Persona completeness, mandatory Test Chat, receipt safety, and completion gating remain unchanged.
- All focused and full Portal tests pass.

## Risks and mitigations

- **Risk: decorative controls appear usable.** Use non-interactive elements, `aria-hidden`, muted styling, and no pointer behavior.
- **Risk: pixel matching harms smaller screens.** Treat reference measurements as desktop targets and use bounded fluid sizing below 900px.
- **Risk: CSS leaks into Portal.** Prefix every rule with `.onboarding-shell` and use onboarding-specific variables.
- **Risk: presentation refactor changes workflow.** Leave `onboarding.js`, service code, Provider Connect, and backend untouched; cover every phase with public-DOM regression tests.

## Open questions

None. The approved scope is onboarding-only visual parity, followed by the existing Portal UI.
