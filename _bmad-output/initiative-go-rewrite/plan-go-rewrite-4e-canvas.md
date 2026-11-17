---
title: 'Go rewrite phase 4e: canvas renderer and hit testing'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'built'
baseline_revision: '657732ca2c3a7f50fc35d5c105c841ca3f5a3fc0'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
followup_review_recommended: false
context:
  - '{project-root}/_bmad-output/initiative-go-rewrite/decisions.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4d-resvg-render.md'
warnings: ['oversized']
deferred: []
---

<intent-contract>

## Intent

**Problem:** The editor's canvas is drawn by `render.rs`'s `Renderer`: per-layer caching, dirty rectangles, a viewport cache, parallel bands and slide animations, with clicks resolved by `hit.rs`. The Go core can export single nodes but has no canvas renderer and no hit testing, so it cannot drive the app yet.

**Approach:** Port the canvas half of `core/src/render.rs` and all of `core/src/hit.rs` to a new `core-go/render` package, on the resvg path only, so vello (`raster.rs`) is dropped:
- `Renderer` with `Layer`, `Flat` and `View`
- `sync` (shared-hash layer invalidation and `prepare_layer` on goroutines), `shift_siblings`
- the slide machinery (`Slide`, `can_slide_into`, `settle_covered`, `unslide_with`, `refresh_slides`, `expire_slides`)
- `flat`/`live`/`find`/`bounds`
- `render` (viewport plus 512 px margin cache, pan as crop, zoom preview, dirty-rect `repaint`, parallel bands, `draw_culled`, solo and sharp modes)
- `hit_chain`, `find_empty_space`, `unapply`, `top_index`, `find_rnode`, `is_pending_url`

Script-node layout comes later (phase 5) through the same hook `Ctx.laid_out` uses. A test-only Rust CLI subcommand runs `Renderer` scenarios under `NALARUPA_RASTER=resvg` for comparison.

## Boundaries & Constraints

**Always:**
- Behaviour matches Rust's `Renderer` running with `Raster::Resvg`: the same output bytes from `render`, the same layer invalidation (which ids `sync` returns as rebuilt), the same `Flat` entries (ids, parents, boxes, matrices, clip and cut lists from `live`), the same dirty areas, hit chains and empty-space results. Pixel tolerances only follow the 4d rules (libm, `rcpps`, JPEG decode, cubic clip), stated per scenario.
- Concurrency: bands and layer preparation run on goroutines, with results identical to a serial run. The `Renderer` is used from one goroutine at a time, as Rust's is under the engine lock. No package-level mutable state.
- `Ctx` (doc, libraries, `text.Fonts`, `svg.Assets`) mirrors `render.rs`'s `Ctx`, with `laid_out`/`laid_out_live` taking an injectable script-node hook that defaults to "not run".
- Reuse `core-go/svg` (`Svg`, `visual_bounds`, `geometry`, `fills`, `color`), `core-go/usvg`, `core-go/resvg` and `core-go/skia`. No new geometry or paint types.
- Floating-point products get explicit conversions (no FMA). No cgo.

**Never:**
- No vello or `raster.rs` port, no PDF (4f), no shaders (phase 6), no engine or FFI (phase 8).
- No change to `app/`, the FFI, or Rust behaviour apart from the test-only CLI subcommand.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| First paint | Templates and the 4a/4d fixtures at several viewport sizes, cameras and zooms (0.1, 1, 2.5, 8), each background color, sharp on and off | Same RGB bytes as Rust | No error expected |
| Pan and zoom cache | A sequence of renders panning inside and outside the margin and zooming in and out | Same bytes at every step | No error expected |
| Edits | Node property updates, inserts, deletes and moves between renders | Same rebuilt-id lists from `sync`, same dirty areas, same bytes after repaint as a cold render | No stale pixels |
| Solo | `solo` set to a top-level id | Same bytes | Unknown id is treated as Rust treats it |
| Slides | `shift_siblings` plus slide start, settle, refresh and expire over time steps | Same slide list states and the same bytes per step | No error expected |
| Hit testing | Points on fills, strokes (inside, center and outside alignment, per-side widths, caps), holes (even-odd), rotated and flipped nodes, clipped children, gaps | Same `hit_chain` results | Empty chain off-canvas |
| Empty space | `find_empty_space` with each direction, padding and anchor | Same JSON result | No error expected |

</intent-contract>

## Code Map

- `core/src/render.rs`:
  - `Slide` 38-96, `Flat` 97, `Layer` 132, `Raster` 157 (only the Resvg variant is needed), `View` 170, `Renderer` 180
  - `Renderer` methods 419-944 (`sync` 421, `shift_siblings` 492, the slides 553-808, `flat`/`live`/`find`/`bounds` 809-869, `render` 870)
  - `Painter` 945, the helpers 984-1220 (`merge`, `under_backdrops`, `changed_nodes`, `dirty_areas`, `reach`, `crop_into`, `repaint`, `paint_band` resvg branch, `canvas_box`, `draw_culled`, `has_hairline`)
  - `prepare_layer` 1227, `compose`/`unapply` 1308-1328, `hit_chain` 1329, `find_empty_space` 1365, `is_pending_url` 1392
  - the 34 tests (port the ones that don't need vello, and drop vello-vs-resvg comparisons)
- `core/src/hit.rs` -- `Shape::of`, `hits`, the stroke-band distance and 16-step curve flattening, and its 7 tests. It needs `svg::geometry`/`fills`/`color` (ported in `core-go/svg`) and svgtypes' `SimplifyingPathParser` (port, or reuse `core-go/usvg`'s svgtypes).
- `core/src/engine.rs:480-570` -- how the engine calls `shift_siblings`, `sync`, `settle_covered`, `refresh_slides` and `expire_slides`, which defines the sequence the CLI scenario runner should replay.
- `core-go/svg`, `core-go/usvg`, `core-go/resvg`, `core-go/export` (its `Ctx` from 4d) -- to reuse and extend.
- `core/src/bin/cli.rs` -- add the scenario runner here.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- add `canvas <doc.pen> <scenario.json> <out-dir>`. It forces `Raster::Resvg`, is offline, and runs steps (`render {w,h,cam,zoom,bg,solo,sharp}`, `update {id,props}`, `insert`, `delete`, `move`, `sync`, `slide {id}`, `tick {ms}`, `hit {x,y}`, `empty {w,h,dir,pad,anchor}`, `flat`, `live`). It writes each render's RGB bytes and a JSON log of every other step's result.
- [x] `core-go/render/renderer.go`, `layer.go`, `view.go`, `slides.go`, `paint.go` -- the renderer.
- [x] `core-go/render/hit.go` -- the `hit.rs` port plus `hit_chain`.
- [x] `core-go/render/space.go` -- `find_empty_space`.
- [x] `core-go/render/*_test.go` -- the ported Rust tests, plus a serial-vs-parallel band equality test and a cached-vs-cold render equality test.
- [x] `core-go/render/parity_test.go`, `core-go/testdata/canvas/*.json` scenarios plus goldens -- every matrix row, golden for text-free scenarios and live for the rest.

**Acceptance Criteria:**
- Given only Go, when `go test ./...` runs (also with `GOAMD64=v3` and `-race` on `./render/`), then it passes with golden canvas parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./render/` runs, then every scenario step matches Rust within its stated tolerance.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- Layout: `render/renderer.go` (Ctx, Renderer, Layer, Flat, `Sync`, `Flats`/`Live`/`Find`/`Bounds`, shared hash), `layer.go` (`prepareLayer`, `reach`, `changedNodes`, `dirtyAreas`, `merge`, `underBackdrops`), `view.go` (`Render`, the cached view, `repaint`, `cropInto`), `paint.go` (bands on goroutines, `paintBand` resvg branch, `drawCulled`, `hasHairline`), `slides.go`, `hit.go` (hit.rs plus `HitChain`, `Unapply`), `space.go`, `scene.go`, `num.go`. `usvg.SimplifyPath` exports svgtypes' simplifying parser for hit.rs; `svg.HasBackdropBlur` is exported for `shift_siblings`.
- Backdrops: Rust's `under_backdrops` reads them off the vello display list (`raster::Scene::new`), which `sync` builds even on the resvg path. `scene.go` is that builder's control flow without the display list (isolation, `nrsh:` shadows, `nrbb:` blurs, `effect_of`, knockouts, simple clips, pattern and image fallbacks, the `inside`/`failed`/`alpha` state), so a background blur counts exactly when Rust records it. The rest of raster.rs (vello drawing) is not ported.
- Determinism hooks in Rust (no behaviour change): `Renderer::clock: Option<Instant>` with `now()` (slides read it instead of `Instant::now()`), `Slide::new_at`, `Slide::state` public, `ms_per_mp` public, `Renderer::dirty()`. The CLI pins the clock and pins `ms_per_mp` before every render (0: a live zoom frame repaints the viewport; `slow`: 1e9, the scaled preview). Go has `Renderer.Now`, `MsPerMP`, `Threads`.
- Band partition depends on the thread count (as in Rust: `available_parallelism` clamped to 1..16, here `runtime.NumCPU`). Goldens record it (12 on this machine) and golden runs paint with it; live runs use the CPU count on both sides.
- Concurrency: layer trees parse on goroutines with options built once per sync and a fresh image resolver per parse; parses whose markup holds `<text` or `<image` share a lock, because text.Fonts' fallback cache and lazy face data are not safe for concurrent use. Bands paint on goroutines; a panicking band or parse is recovered as Rust's joined threads are.
- Deleted layers add their paint box to the dirty list in their old stacking order (Rust: hash map order); parity compares dirty lists as sets.
- `solo` "" means none. `State` and `duration` use Go's `math.Exp`/`math.Log`, hit distances `math.Hypot` (libm-level differences only; every scenario matched exactly).
- CLI `canvas` replays the engine's `sync` (`shift_siblings`, `sync`, `settle_covered` with re-sync, `refresh_slides`) and `slide_in`, without `unslide_glass`/`holds_glass` (engine, phase 8). Inserted nodes take random ids on both sides; the parity test maps Go's ids to Rust's from the insert step on. The slide scenarios enable nodes stored with `enabled: false` instead of inserting, so their ids are known.
- Ported tests: all 7 hit.rs tests and all 34 render.rs tests (vello-vs-resvg loops run resvg only), plus `TestParallelBandsMatchSerial` (equal bytes) and `TestCachedFrameMatchesCold`. Rust's render tests run on vello; on the resvg path three need a bound on band-origin effects, stated in each: text overflow allows 24 stale channels (glyph edge antialiasing, measured 9; a ghost is hundreds), the 150-step cached-view walk allows w·h/5000 pixels past 16 levels (the rotated ellipse's blurred shadow, measured 5 at step 0; Rust's resvg path gives the same 5, checked with the CLI), and the cached-vs-cold frame allows len/20000 channels past 16.
- Scenarios (`testdata/canvas/*.json`, docs in `testdata/canvas/docs`): first paint of the shapes, effects, gradients, images fixtures and the blank and mobile templates at zooms 0.1, 1, 2.5 and 8 with several sizes, cameras and backgrounds, sharp and live; pan and zoom (crop, outside the margin, live viewport repaint, scaled preview, out-of-range preview); edits (props, clip off, opacity, effect, insert, delete, move under another parent, rotation, top-level insert, restack, top-level delete, root restyle); glass edits (backdrop growth); solo with a known and an unknown id; slides (reflow glides, fade, nested and rotated parents, solo, expiry), settle (a later node covering a slide), wait (a slide never shown expires, a blend drops a slide); hit points on fills, holes, ring, inside/outside/per-side strokes, square and round caps, curves, rotated and flipped nodes, clipped children, groups, gaps and off-canvas; empty space in every direction with pads and anchors; Flat and live dumps. `text` and `text-edits` hold text and run live only. Goldens: `testdata/canvas/golden` (log JSON plus gzipped RGB frames, about 1 MB).

## Plan Change Log

## Review Triage Log

### 2026-11-15 — Review pass
- verdicts: 4 findings — high 0, medium 1, low 3, false 0, maybe-false 0
- findings:
  - `[medium]` `[patch]` `renderView` allocates the cold-paint view buffer twice (a discarded `NewPixmap` size check, then `make`), and `MsPerMP` includes the extra allocation, so the live-zoom path decision can diverge from Rust. — Fix: allocate once.
  - `[low]` `[reject]` `TestCachedFrameMatchesCold`, `TestTextOverflowLeavesNoGhosts` and `TestCachedViewMatchesColdRender` allow small pixel differences, which the Always rule's tolerance causes don't cover. — Rust runs these ported tests on vello. On the resvg path, painting from a different origin moves the same few edge pixels in Rust too (the implementer measured 5 in both). The 18 parity scenarios are byte-identical, so no Go defect is hidden. The fix would be to edit this plan's rule.
  - `[low]` `[patch]` `Find` allocates the full `Flats()` slice on every lookup. — Fix: iterate the layers directly.
  - `[low]` `[patch]` `SimplifyPath` builds a map literal for every segment. — Fix: use a switch.

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./render/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./render/` -- expected: pass, no race
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-15, Windows, 12 logical CPUs, CLI at `%TEMP%/nalarupa-go-target`):** `go vet ./...` and `go test ./...` pass with golden canvas parity; so does `GOAMD64=v3 go test ./render/`. `go test -race ./render/` passes with no race (with `NALARUPA_CLI`, so the text scenarios ran too). Live, all of `go test ./...` passes against the rebuilt release CLI (also `GOAMD64=v3` for render): all 18 canvas scenarios match Rust step for step, and every render is byte-identical (no tolerance was needed). `cargo test --release` passes (292 tests). Not run on Linux.

## Auto Run Result

- **Summary:** `core-go/render` ports the canvas half of `render.rs` and all of `hit.rs` on the resvg path. vello is dropped, except for its background-blur decision logic, which `scene.go` ports so the same blurs are recorded. It covers:
  - `Renderer`, `Layer`, `Flat` and `View`
  - `sync` with goroutine layer preparation, `shift_siblings` and the slide machinery
  - `flat`/`live`/`find`/`bounds`
  - `render` (margin cache, pan crop, zoom preview, dirty-rect repaint, parallel bands, `draw_culled`, solo and sharp)
  - `hit_chain`, `find_empty_space` and `unapply`

  `Ctx.Scripts` is the phase 5 hook. Rust gains additive replay hooks in `render.rs` (an optional pinned slide clock, public `ms_per_mp`, `dirty()`, `Slide::new_at`, public `Slide::state`), with no behaviour change while the clock is unset, and a test-only `canvas` scenario runner.
- **Files:**
  - `core-go/render/*.go`: the renderer and hit testing
  - `core-go/usvg/svgtypes_simple.go`: `SimplifyPath`
  - `core-go/svg/svg.go`: `HasBackdropBlur`
  - `core-go/testdata/canvas/`: 18 scenarios with docs and goldens
  - `core/src/render.rs` and `core/src/bin/cli.rs`: hooks and the oracle
- **Review:**
  - Patches applied: 3 (1 medium, 2 low). The cold-paint buffer is allocated once, `Find` doesn't allocate, and `SimplifyPath` uses a switch instead of a map literal.
  - Rejected: 1 low (small pixel allowances in three ported tests, where Rust on the resvg path moves the same pixels).
- **Follow-up review recommended:** false (patched: 0 high, 1 medium, 2 low).
- **Verification:**
  - `go vet` and `go test` pass, and so does `GOAMD64=v3` on `./render/` and `./usvg/`.
  - `-race` on `./render/` passes (live).
  - Live parity against the release CLI passes: 18 scenarios, every frame byte-identical. It was re-run after the patches.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - Goldens assume 12 bands (CPU count), and live runs assume Go and Rust see the same CPU count.
  - Parses with text or images serialize on the font-cache lock.
  - Engine glass handling is left for phase 8.
  - Linux has not been run.
