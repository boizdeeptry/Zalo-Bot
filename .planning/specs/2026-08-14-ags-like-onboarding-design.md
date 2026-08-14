# AGS-like Onboarding Setup

Date: 2026-08-14

## Context

The current Portal onboarding is a strict linear wizard:

`Provider → Connect → Setup → Persona → Test Chat → Complete`

The local AGS app uses a calmer Settings-style setup surface instead:

- status and user selection are visually separated;
- opening setup shows cached state first and does not run slow checks automatically;
- selecting/toggling an agent/provider starts the explicit connect/install action;
- progress is shown inline with percent/log style feedback;
- server status/polling may update health, but it does not silently change the user's selected target;
- completion remains gated by a real successful test, not by simply reaching the end of a form.

## Decision

Replace the early onboarding presentation with an AGS-inspired `Agent Setup` surface while preserving the hardened backend lifecycle and safety gates.

The backend phases remain unchanged:

`provider`, `connect`, `setup`, `persona`, `test`, `completed`

The UI changes are intentionally frontend-first:

1. `provider` renders as an `Agent Setup` dashboard with two provider rows (`codex`, `claude-code`) and explicit selected/status badges.
2. Choosing a provider only changes local selection until the user starts the connection action.
3. `connect` renders inside the same `Agent Setup` visual language and reuses the existing Provider Connect component, including its progress bar and logs.
4. `setup` renders as an inline setup queue/progress card instead of a separate wizard-looking screen.
5. `persona`, `test`, and `complete` stay strict:
   - all persona placeholders must be filled;
   - Test Chat cannot be skipped;
   - the active provider/persona/model identity must still pass server validation before completion.

## Non-goals

- No backend contract rewrite in this slice.
- No AGS plugin marketplace or multi-agent install queue yet.
- No auto silent re-test loop yet.
- No weakening of existing revision, identity, receipt, or completion checks.

## Acceptance checks

- Initial onboarding no longer looks like a generic welcome wizard; it shows `Agent Setup`.
- Provider rows expose selected/not-selected status without mutating the server on click.
- Connect and Setup phases share the same AGS-like status surface.
- Existing Provider Connect progress/log behavior is reused.
- Existing Persona/Test/Complete tests still pass.
- Full Portal Node tests pass.
