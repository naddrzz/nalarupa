<!-- bmad:context -->
<!-- Verified 2026-10-18 against 31eac66. Managed by bmad-project-context; edits inside this block are replaced on refresh. Keep anything you want preserved outside the markers. -->

## nalarupa

AI design editor: a Rust core (`core/`, a cdylib loaded through `bun:ffi`) and a GPUIX/React UI run by Bun (`app/`). Windows is the release platform; CI, nightly QA and Claude automation run on a self-hosted Linux runner (`.github/workflows/`). Work is tracked in GitHub issues.

## Policy

- Never push to `dev` or `main`; open a PR against `dev`. `main` is release-only and moved by the maintainer.
- Public repo: never name, quote, or copy code, assets, kits or text from other design tools. The `.pen` compatibility literals already in code (`pencil:` URL prefix, `"pencil:pending"`, the `pencil` script alias, `data-pen-node-clipboard`) are the only exception. Research notes stay outside the repo, never in `docs/`.
- Regenerate README screenshots only with `bun run shots` (it uses a fresh `NALARUPA_DATA`, so no personal recents or chats leak).

## Where things are

- `core/guide/*.md` is runtime content compiled into the in-app agent (`core/src/guide.rs`), not instructions for you; edit it only to change what that agent is told.
- `core/vendor/resvg` is resvg 0.47 with two patches (search `Nalarupa:`); bump resvg, usvg and krilla-svg together and re-apply them.

## Running and verifying

- Build checks against your own core: set `CARGO_TARGET_DIR` to a separate folder and `NALARUPA_CORE=<that dir>/release/nalarupa_core.dll` (`libnalarupa_core.so` on Linux); a running app or e2e locks `core/target/release/nalarupa_core.dll` (link error LNK1104).
- On Linux run GUI checks as `bash scripts/wl-run.sh bun run e2e` from `app/`; Xvfb does not work, GPUIX's Linux build has no X11 backend.
- On Linux `a11y-check`, `window-check`, `exe-check` are Windows-only; name the skipped checks in the PR.
- On Windows a real mouse moving over the background e2e window cancels simulated drags; rerun a step that "dropped early" with the cursor idle.

## Conventions that differ from defaults

- Never hold `engine()` across slow work: render, poll and boxes share its mutex and the UI freezes; do heavy work after the guard drops (see `ToolOut::finish`, `core/src/engine.rs`).
- Set rquickjs runtime handlers (`set_interrupt_handler`) outside `Context::with`; inside it they deadlock (feature `parallel`).
- GPUI does not bubble clicks: give a small clickable child a painted fill (`'#00000001'`), not `transparent`, so it gets its own hitbox.
- Draw focus rings with `FocusRing` (`app/src/ui.tsx`), never `boxShadow`, which GPUI paints as a filled box.
- Keys reach only the focused element's `onKeyDown`; window-level keys go in the `render(<App/>, { onKeyDown })` options in `app/app.tsx`.

## Known pitfalls

- GPUIX automation: chords use hyphens (`ctrl-a`; `ctrl+a` silently does nothing); `fill()` hangs on text containing double quotes; `<input>` values and `style` cannot be read, so assert on visible text or bounds; synthetic clicks carry no `clickCount` (double-click handlers use a 450 ms fallback).
- GPUIX `fill()` can miss an `<input>` inside `<virtual-list>` (an orphan focus handle); click the field, then send keystrokes without an `elementId` (see `editor-e2e`). Under the slow Linux renderer, poll with `until()` instead of fixed sleeps.
- Give every `<textarea>` an `aria-label`; its placeholder is not its screen-reader name and `a11y-check` fails.
- Match each file's line endings when patching by string; the repo mixes CRLF and LF.

<!-- /bmad:context -->
