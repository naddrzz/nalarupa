---
title: 'Go rewrite phase 10a: fix the shadow repaint seam'
type: 'bugfix'
ticket: ''
created: '2026-11-17'
status: 'built'
baseline_revision: 'df456fb'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8e-c-abi.md'
warnings: []
deferred: []
---

<intent-contract>

## Intent

**Problem:** One smoke check fails against the Go DLL: "an edited canvas matches a full render around a rotated, flipped shadow", with a largest difference of 21 (`app/scripts/smoke.ts:515-531`). After `move_by` there and back, the canvas repainted in bands differs from a solo full render of the same document near band edges. Phase 4e moved the canvas onto the resvg path and dropped vello. Rust has the same seam on its resvg path (`NALARUPA_RASTER=resvg`) and passes only on vello. This is a cutover blocker deferred from 8e.

**Approach:** Default per the maintainer's standing "Fix the seam" choice:
1. Diagnose the cause in `core-go/render`: the repaint/dirty-region and band logic, and how a shadow (blur plus offset) under a rotated, flipped transform is clipped per band.
2. Fix it in Go so a banded or partial repaint equals the full render to within 1.

The likely cause is that each band renders the filter with a region clipped to the band, so the blur near the band edge reads transparent pixels. Each band must render the filter with enough margin (the blur extent plus offset, transformed), or the dirty region must be widened, as vello effectively does.

## Boundaries & Constraints

**Always:**
- The smoke assertion passes unchanged (worst ≤ 1) against the Go DLL.
- Full renders (`solo`, export, PNG/PDF) stay byte-identical to before. Only partial or banded repaint changes. The existing render/resvg parity tests keep passing.
- Add a Go test in `core-go/render` (or `engine`) reproducing the smoke scenario: ellipse `flipY` + rotation 45 + shadow {offset 10,10; blur 4}, move by +1 then -1, compare the live canvas with a solo render, worst ≤ 1. The test must fail before the fix.
- Record in Implementation Notes that this diverges from Rust/resvg (whose seam remains) toward vello's result.

**Never:**
- No change to `app/` (including smoke), Rust, or the export path output.
- No performance regression beyond a bounded margin per band; note the cost.

</intent-contract>

## Code Map

- `app/scripts/smoke.ts:515-531`: the check.
- `core-go/render`: the repaint, dirty regions, bands and caching (`nr_render` → `render`).
- `core-go/resvg`: filter region and blur handling.
- `core/src/render.rs`, `core/src/raster.rs`: how Rust's vello path and its resvg path handle repaint, for reference.

## Tasks & Acceptance

**Execution:**
- [x] Diagnose, with a Go repro test that fails first.
- [x] Fix, keeping the full-render bytes unchanged.
- [x] Rebuild the DLL and run `smoke` against it (all of smoke must pass; the long-line timing check can be load-sensitive, so rerun on an idle machine if it alone fails), plus `paint-check` and `viewport-check`.

**Acceptance Criteria:**
- Given the Go DLL, when `bun run smoke` runs, then it prints `smoke ok`.
- Given MinGW gcc as `CC`, when `gofmt -l .`, `go vet ./...` and `go test -count=1 ./...` run, then they pass, including the live parity tests if `.parity-live` is present.

## Implementation Notes

**Root cause.** The filter is not what is clipped: a filter group already gets a layer of its target plus 512 px (the `Nalarupa:` patch in `resvg/render.go`), and a band-for-band resvg experiment showed the drop shadow alone matches. The seam comes from the rasterizer (tiny-skia's edge clipper, ported in `core-go/skia`): a curve that crosses the pixmap's edge is chopped there and each piece is flattened on its own, so the cut piece's antialiased edge moves by up to ~32 levels along its whole length (tens of pixels from the cut). A plain ellipse painted in 128-row bands already differs from one tall pixmap; so does one cut by a narrower column. The full paint cuts curves at its band edges, the old repaint at the dirty rectangle's edges and its own band edges, so the two disagreed (21 in the smoke scenario at 16 threads; the full paint was the seamed one there: its band edge at y=128 ran through the shadow's knockout mask and ellipse, while the repaint happened to match a single-pixmap render).

**Fix** (`core-go/render/view.go`, `paint.go`): `repaint` splits the dirty rectangles by the cached view's own bands (same height as its full paint) and, per band, repaints the union of what falls in it. `paintBands` paints such a band at the full paint's size, origin and transform into a scratch buffer, culling to the dirty area (`drawCulled` takes a separate `cull` rect; whether a plain group is descended still depends on the band's view alone, so each drawn node goes through the same calls), then copies back only that area. Every curve is therefore cut exactly where the full paint cuts it. A temporary check that full-painted the view after each repaint found every repaint in the render and canvas-parity tests byte-identical (worst 0). A band that panics now drops the cached view (the next frame paints afresh) instead of keeping stale pixels. Full paints (first frames, solo, live zoom, export, PNG/PDF) are untouched: the `first-*`, `solo`, `pan-zoom` canvas scenarios and the export parity tests stay byte-exact.

**Test.** `render/seam_test.go` `TestRepaintMatchesFullRender`: the smoke scenario (ellipse flipY + rotation 45 + shadow 10,10 blur 4, moved +1 then back) and an unchanged ellipse cut by a moved rectangle's repaint, at 1, 4 and 16 threads, live vs solo worst ≤ 1. Before the fix: 21 (smoke doc, 16 threads) and 24–32 (second doc).

**Cost.** Per touched band: a band-sized scratch buffer and background fill, and isolated groups (clip/opacity/filter frames) overlapping the dirty area get layers clamped to the band instead of the dirty rectangle. Moving a small node in a 1920×1080 view: effects.pen 35 → 40 ms, gradients.pen 9.2 → 10 ms, shapes.pen 1.45 → 2.0 ms. (Repainting whole full-width bands was tried first and cost 3–12×; rejected.)

**Divergence from Rust.** Rust's resvg path (`NALARUPA_RASTER=resvg`) keeps the seam: it repaints the dirty rectangle alone. Go's repaints now equal its full paint, as vello's result does. The canvas parity scenarios that record Rust repaints got a stated `tolerance` (`edits-glass` 32/0.05%, `edits` 64/0.1%, `slides-settle` 48/0.08%, `slides` 48/0.05%; observed 23/0.028%, 58/0.055%, 45/0.045%, 45/0.027%), and the engine replay `sync.jsonl` a `# frames within 1:` line (one channel off by 1), a directive the replay parity test now reads (Rust and Go replays skip `#` lines).

**Review patches (accepted with the allowances as the recorded divergence).**
1. The canvas `tolerance` now applies only to frames served from a cached view that edits have repainted since its full paint (`tolFor`, `runScenario`'s third result); first, full, solo and viewport-only live-zoom frames are compared byte-exact. Every frame the old allowance was needed for is in that set (edits-glass 4, 7; edits 11–17, 32–44; slides 18–30; slides-settle 18–23). `runScenario` also permanently compares each repainted view with a full paint of the same view (same band height) and fails above 1 per channel; it is 0 throughout.
2. The replay `# frames within N:` directive is capped at 2 (`maxFramesWithin`); a higher N fails the sequence (checked with N = 3).
3. The view stores the band height of its full paint (`view.band`); `repaint` cuts the same bands instead of recomputing them from `threads`. `TestRepaintKeepsTheViewsBands` paints at 16 threads, edits at 1, and compares with a 16-thread full render: 21 with the recomputed height, ≤ 1 now.
4. Repaint band buffers come from a `sync.Pool` (`scratch`), reused across repaints; `paintBand` fills the whole band, so a reused buffer needs no clearing. Per-rect splitting is unchanged.

**Checks.** gofmt clean; `go vet ./...`; `go test -count=1 ./...` passes with `.parity-live` (live Rust comparison) — `TestSlidingHugeShadowedNodeRendersQuickly` once timed out under the full parallel run (6 s vs 5 s) and passes alone (0.9 s) and in a rerun of `./render`. DLL rebuilt with `build.sh`; `smoke` (smoke ok), `paint-check`, `viewport-check` pass against it. After the review patches: gofmt, vet and the whole suite pass again; DLL rebuilt; `smoke ok` (its long-line timing check failed once while `go test` loaded the machine and passed on the idle rerun).

## Plan Change Log

- The plan required existing parity tests to keep passing unchanged. Because the full paint itself carried the seam, a seamless repaint necessarily differs from Rust's resvg repaint. The orchestrator accepted stated tolerances on repaint frames only, plus a permanent check that each Go repaint equals a Go full paint (within 1), as the recorded divergence from Rust (consistent with the maintainer's default "fix the seam").

## Review Triage Log

### 2026-11-17 — Review pass
- verdicts: 5 findings — high 0, medium 0, low 4, info 1
- findings:
  - `[low]` `[patch]` The parity tolerances covered every frame. — Fixed: repaint frames only, plus the permanent repaint-equals-full-paint check.
  - `[low]` `[patch]` `# frames within N:` was uncapped. — Fixed: capped at 2.
  - `[low]` `[patch]` The band height was recomputed from the threads. — Fixed: stored in the view, plus `TestRepaintKeepsTheViewsBands`.
  - `[low]` `[patch]` A scratch buffer was allocated on every repaint. — Fixed: pooled.
  - `[info]` `[reject]` Overlapping split rects are repainted twice. — Harmless extra work.

## Verification

**Commands:**
- `cd core-go && gofmt -l . && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && ... go test -count=1 ./...`
- `cd core-go && bash build.sh`, then `cd app && NALARUPA_CORE=<abs dll> bun run smoke` (and `paint-check`, `viewport-check`)

## Auto Run Result (orchestrator finalize)

- **Summary:** Repaints are redrawn per band at the full paint's geometry, so an edited canvas equals a full render. This fixes smoke's shadow seam, which was the cutover blocker from 8e.
- **Review:** quick lens. Four low patches.
- **Verification:**
  - gofmt, `go vet` and `go test ./...` pass, with live Rust parity.
  - smoke passes (`smoke ok`) against the rebuilt DLL.
  - paint-check and viewport-check passed before the review patches and were not rerun after them.
