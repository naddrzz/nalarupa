---
title: 'Go rewrite phase 8b: editor operations'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: '026401b77a082c37eeacbbc19e3d13c6ff94d8e7'
route: 'full'
route_source: 'auto'
risk: 'high'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
followup_review_recommended: false
context:
  - '{project-root}/_bmad-output/initiative-go-rewrite/decisions.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8a-engine-core.md'
warnings: ['oversized']
deferred: []
---

<intent-contract>

## Intent

**Problem:** Every direct-manipulation action in the editor (snapping, marquee selection, drop targets, reparenting, wrapping in a frame, auto layout, aligning, duplicating, copy and paste, vector path editing, layer listing and reordering) runs in `core/src/editor.rs` and the inline commands of `core/src/lib.rs`. The Go engine (8a) has none of them.

**Approach:** Port `editor.rs` to a new `core-go/editor` package on the 8a engine:
- `View`, `snap_targets`, `painted_bounds`/`fit_bounds`, `nodes_in_rect`, `drop_target`
- `reparent` (keeping canvas position through rotation and flips), `wrap`/`unwrap`
- `infer_flex`, `add_layout` (via `batch`), `set_layout`, `remove_layout`, `align`, `duplicate`
- `copy`: base64 JSON in the `data-nala-node-clipboard` marker plus the variables, themes and components the nodes need
- `decode` and `paste`: our own marker, then the `.pen` marker (`data-pen-node-clipboard`), then Figma (the 7b package), then an SVG data-URL image rectangle, then plain text (Inter 12). Placement into selected containers, beside the sources, in place, or centred in the view, importing missing variables, themes and components.
- `path_edit`/`path_write`

Port `lib.rs`'s inline commands too (`layers`, `reorder`, `node`, `move_by`, plus `insert`/`delete` if 8a left them inline), and extend the 8a replay driver with every editor command so the Rust dispatcher remains the oracle.

## Boundaries & Constraints

**Always:**
- For the same replay sequence and seed, the Go replay gives the same responses, document JSON, history behaviour, selection and clipboard HTML as Rust. The clipboard HTML is byte-identical, including the base64 payload.
- Edits go through the 8a engine's history and edit gate exactly where Rust uses `edit`, `edit_or_drag`, `edit_coalesced` and `batch`, so undo/redo after each editor op matches.
- The `.pen` compatibility literal `data-pen-node-clipboard` is used as the existing code uses it (the AGENTS.md exception). No other names, text or code from other design tools are added.
- Reuse `core-go/engine`, `core-go/render` (flats, hit, bounds), `core-go/figma`, `core-go/doc`, `core-go/layout`, `core-go/svg`. No duplicated logic. Floating-point products get explicit conversions (no FMA).

**Never:**
- No execute integration (8c), streams (8d), C ABI (8e), or agent (phase 9).
- No change to `app/` or Rust behaviour apart from extending the test-only replay.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Structure | reparent/wrap/unwrap rotated and flipped nodes, nested frames, layout parents | Same doc and canvas positions as Rust, undo restores | Same error text for invalid targets |
| Layout | add_layout on scattered children (flex inferred), set_layout changes, remove_layout | Same doc | No error expected |
| Align and duplicate | Align each edge and centre for 1 and n nodes, duplicate with offsets and ids | Same doc and new ids for the seed | No error expected |
| Queries | snap_targets, nodes_in_rect, drop_target, bounds (fit/painted), layers, node | Same JSON | No error expected |
| Clipboard | copy then paste (into a container, beside the sources, in place, centred), pasting `.pen` and Figma clipboard HTML, an SVG data URL, plain text, garbage | Same clipboard HTML and doc, with variables, themes and components imported | Same result for unknown content |
| Paths | path_edit then path_write on paths, polygons and rotated vectors | Same contours and doc | No error expected |
| Reorder and move | reorder in each direction, move_by under drag and coalesced | Same doc and history | No error expected |

</intent-contract>

## Code Map

- `core/src/editor.rs`:
  - `num` 18, `View` 46, `snap_targets` 199, `painted_bounds`/`fit_bounds` 228-235, `nodes_in_rect` 258, `drop_target` 290
  - `reparent` 367, `wrap`/`unwrap` 409/451, `infer_flex` 514, the layout functions 578-669, `align` 700, `duplicate` 746
  - `copy` 784, `needs` 827, `decode` 888 (Figma at 907), `paste` 931, `path_edit`/`path_write` 1196/1213
  - tests (11)
- `core/src/lib.rs:30-300` -- the dispatcher, with the inline `layers` (47) and `reorder` (251-282) logic and the editor command routing, for the replay commands.
- `app/scripts/editor-check.ts`, `drag-check.ts` -- the command sequences these checks send. Build replay files from them, with their expectations.
- `core-go/engine` (`Engine`, history, `Replayer` in `replay.go`), `core-go/figma` (`Decode`), `core-go/render` (`Renderer.Flats`, `HitChain`, bounds) -- reuse and extend.
- `core-go/testdata/replay/` and the 8a replay parity test -- add the editor sequences there.

## Tasks & Acceptance

**Execution:**
- [x] `core-go/editor/view.go`, `snap.go`, `structure.go` (reparent, wrap, unwrap), `layout.go`, `align.go`, `duplicate.go`, `clipboard.go` (copy, needs, decode, paste), `path.go` -- the port.
- [x] `core-go/engine/replay.go` -- the editor and inline commands (`layers`, `reorder`, `node`, `move_by`, `bounds`, `hit`, `boxes`, `children_boxes`, `frames`, `live`, `variables`, `font_state`, `png`, every editor command).
- [x] `core-go/editor/editor_test.go` -- the 11 Rust tests plus each matrix row.
- [x] `core-go/testdata/replay/editor-*.jsonl` (+ goldens) -- sequences per matrix row, including ones derived from editor-check and drag-check.

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs (also with `GOAMD64=v3`, and `-race` on `./engine/ ./editor/`), then it passes with golden replay parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./engine/ ./editor/` runs, then every editor replay sequence matches Rust byte for byte.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- Layout: `editor/view.go` (`Num`, the view, `placed`, `touches`, the affine helpers), `snap.go` (snap targets, painted and fit bounds, marquee, drop target), `structure.go`, `layout.go` (`InferFlex`, add/set/remove layout), `align.go` (align and duplicate: one file, so no `duplicate.go`), `clipboard.go`, `b64.go` (the base64 0.22 STANDARD decoder with its error text: Go's wording differs), `path.go`, `commands.go` (the editor's dispatcher commands).
- Import cycle: editor needs engine, so engine cannot call editor. `engine.Handle(cmd, f)` registers a command from editor's `init`; `dispatch` falls through to it after its own commands. The inline `lib.rs` commands that need no editor code (`layers`, `reorder`, `node`, `hit`, `boxes`, `children_boxes`, `frames`, `live`, `font_state`, `png`) are in `engine/replay.go`; `png` runs before the lock and uses `script.OffLock` like Rust. `replay_parity_test.go` became `package engine_test` and imports editor, so `go test ./engine/` replays every sequence. engine exports `IDsArg`, `Strings`, `Object`, `FlatJSON`, `MatrixJSON`, `RustF64`, `IDs()` and `Layers`/`NodeInfo`/`Reorder` for editor.
- Replay `copy`: the 8a file-copy step and the clipboard command share the name. Both replays (`cli.rs` and `Replayer`) now treat `copy` with `from` as the file copy and anything else as the dispatcher's `copy` (the only Rust change).
- Clipboard session: Rust's process-wide `session()` draws from the thread's id generator on first use; Go keeps it as `Engine.ClipSession`, drawn from `e.IDs()`, so the clipboard HTML is byte-identical for the seed.
- `needs` walks HashSets in Rust, whose order changes per process: with more than one new variable or component in one round, Rust's own output (clipboard HTML, imported variable order) varies between runs. Go sorts each round, one of those orders. The sequences only copy nodes whose needs grow one name per round.
- Parity fixes outside editor: a new leaf package `libm` (the double-double sin, cos, tan and atan2 moved from usvg) now also backs `svg.OwnMatrix`, `layout.LocalAABB` and the ellipse and polygon points in `svg` (each with explicit conversions), and holds `TotalCmp` (shared by figma and editor). Go's `math.Sin/Cos` differ from a correctly rounded libm for about half of all whole-degree angles (cos 90° among them), which changed rotated boxes' `m` and bounds in the last digits. `svg.PathBBox` got explicit conversions (it fused under GOAMD64=v3, changing `path_edit`).
- Sequences (`testdata/replay/editor-*.jsonl`, `editor-check.jsonl`, `drag-check.jsonl`; fixture `files/editor.pen`): structure, layout, align and duplicate, queries, clipboard (one session and a new document), foreign clipboards (.pen marker, foreign session with needs, Figma, SVG, text, garbage), paths with reorder and move_by, and the editor-check and drag-check command sequences (their ids from the seed). `editor-text.jsonl` (`# live`) holds layout around text, which depends on fonts. Goldens grew to about 670 KB.
- Not in the byte replay: a `png`'s bytes (the PNG encoders differ; `TestPNGCommand` checks it equals export's `NodePNG`), and 45° turns (the Windows CRT's sin(π/4) is off by an ulp, so no correctly rounded sin matches it).

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 6 findings — high 0, medium 1, low 5, false 0, maybe-false 0
- findings:
  - `[medium]` `[patch]` `LocalAABB` and `OwnMatrix` products lack FMA guards, and two ellipse/arc lines in `geometry.go` still use `math.Cos`/`Sin`. — Fix: add explicit conversions and use `libm`.
  - `[low]` `[patch]` The `Num` check in `TestQueriesAndBase64Errors` can never fail. — Fix: separate asserts with the correct expected values.
  - `[low]` `[patch]` No polygon or second rotation in the Paths row. — Fix: add a replay case and regenerate the golden.
  - `[low]` `[patch]` Duplicated helpers: `totalCmp`, `lines`, `compose`, `emptySpace`. — Fix: reuse shared ones and delete the copies.
  - `[low]` `[patch]` The `reparent` index `int(u)` overflows to negative. — Fix: clamp it.

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./engine/ ./editor/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-16, Windows, 12 logical CPUs, CLI rebuilt at `%TEMP%/nalarupa-go-target`):** `go vet ./...` and `go test ./...` pass with golden parity (15 sequences; `platform` and `editor-text` live only); `GOAMD64=v3 go test ./...` passes on the whole module; `go test -race ./engine/ ./editor/` passes. Live, `go test ./...` passes on every package against the rebuilt CLI: all 17 replay sequences are byte-identical, and the render, export, html, layout, figma and usvg parity suites still pass after the libm and PathBBox changes. `cargo test --release` passes (292 tests). Not run on Linux.

## Auto Run Result

- **Summary:** `core-go/editor` ports all of `editor.rs`:
  - view, snapping, bounds, marquee and drop target
  - reparent, wrap and unwrap, layout inference and editing, align, duplicate
  - copy and paste (our own marker, the `.pen` marker, Figma, SVG, text)
  - path editing

  It registers through `engine.Handle`. `lib.rs`'s inline query commands are in `engine/replay.go`. The new `libm` package (correctly rounded sin/cos/atan2, `TotalCmp`) now drives rotation, ellipse and polygon math in `svg`/`layout`, and FMA guards are added in `PathBBox`, `LocalAABB` and `OwnMatrix`. The Rust replay `copy` only copies a file when given `from`.
- **Review:**
  - Patches applied: 5 (1 medium, 4 low). These add FMA guards and `libm` in geometry, fix the vacuous test, add polygon and second-angle replay cases, remove duplicated helpers (`TotalCmp`, `script.Lines`, `render.Compose`, `render.FindEmptySpaceOK`), and clamp the reparent index.
  - Rejected: none.
- **Follow-up review recommended:** false (patched: 0 high, 1 medium, 4 low).
- **Verification:**
  - `go vet` and `go test ./...` pass offline, and `GOAMD64=v3 go test ./...` passes on the whole module.
  - `-race` on `./engine/` and `./editor/` passes.
  - Live parity passes on every package, and 17 replay sequences are byte-identical.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - The Windows CRT's sin at 45° is off by 1 ulp, so 45° is avoided in fixtures.
  - Rust's hash-ordered clipboard needs are unstable when several are copied (Go sorts them).
  - `png` is not byte-compared (encoders differ).
  - The `u64::MAX` reparent index differs.
  - Linux has not been run.
