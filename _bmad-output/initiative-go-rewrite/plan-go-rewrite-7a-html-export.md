---
title: 'Go rewrite phase 7a: HTML/CSS export'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'in-progress'
baseline_revision: 'fb44aa21dd7edecac5036388fa9e0e30704a0831'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: ''
review_source: ''
lenses_ran: []
review_loop_iteration: 0
followup_review_recommended: false
context:
  - '{project-root}/_bmad-output/initiative-go-rewrite/decisions.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4a-svg-emitter.md'
warnings: []
deferred: []
---

<intent-contract>

## Intent

**Problem:** The design editor exports nodes as HTML with inline flexbox CSS (`html-css`, `html-tailwind`, `html`), and the Go core has no HTML generator.

**Approach:** Port `html_document`/`html_in` and all their helpers from `core/src/html.rs` to a new `core-go/html` package on `export.Ctx`:
- the node walk with inline CSS, the Google Fonts links with only the weights in use, and the `includeLayerNames`/`includeLayerIds`/`includeHtmlScaffold` options
- `size_css`, the rotated/flipped bounding-box wrapper, and SVG inlining for paths, polygons, lines, arcs and non-solid icons (continuing `svg.Svg` id numbering), with `currentColor` icons
- paint CSS: `fill_layers`, `gradient_css` with the `svg_gradient` data-URI fallback, image `url()`, mesh and shader `raster_layer` PNGs (through `svg` mesh gradients and the 6b shader painter), overlay divs for translucent or blending layers, `stroke_overlay` rings, borders and box-shadows
- shadows (`box-shadow`, `text-shadow`, `drop-shadow`, `shadow_filter`), blur, backdrop-filter, and `text_css`

Export the few `core-go` helpers it needs. A Rust CLI `html --offline` oracle proves byte parity. The export job orchestration (`plan`, `ExportJob`, script file claims) is slice 7c.

## Boundaries & Constraints

**Always:**
- Output is byte-identical to `nalarupa-cli html <doc> <id> <out> --offline` for the same document, node, options and fonts. Floats follow Rust's formatting (`{}` as `FormatFloat('f', -1, 64)`, `{:.3}` as `%.3f`, and the `-0`→`0` handling through `+0.0`). Embedded PNG data URIs (mesh, shader) are compared by decoded pixels within the 6b/4a tolerances.
- `html-tailwind` and `html` produce exactly what `html-css` does, as in Rust.
- Reuse `core-go/svg` (`New`, `StartIDs`, `UID`, `Finish`, `Radii`, `Fills`, `BlendMode`, `Color`, `Esc`, mesh gradients, `Assets.IconWeight`), `core-go/layout` (`LocalAABB`, `Hugs`, `HugsFill`, `Padding`, `StyleOf`), `core-go/resolve` and `core-go/shader` (painter). Export unexported helpers (for example `svg.effectsOf`, `svg.meshGradient`, `resolve.leaf`) instead of copying them.
- Floating-point products get explicit conversions (no FMA). cgo stays only in the packages that already use it.

**Never:**
- No export-job planning, writing, file naming, script claims or engine wiring (7c), and no Figma import (7b).
- No change to `app/`, the FFI, or Rust behaviour apart from the test-only `--offline` flag on the `html` CLI command.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Layout | Frames with horizontal/vertical/none layout, gaps, padding, `fill_container`/`fit_content` with fallbacks, hugging parents, rotated and flipped children | Byte-identical HTML | No error expected |
| Paint | Solid, linear/radial/angular gradients (CSS and SVG fallback), image fills (fill/fit/crop/tile), mesh and shader fills, translucent and blend-mode layers | Same HTML, PNG URIs equal by pixels | No error expected |
| Strokes and effects | Inside/center/outside strokes, per-side widths, clip-frame strokes over children, drop/inner shadows on frames, text and groups, layer and background blur | Byte-identical | No error expected |
| Text | Weights, line height, letter spacing, wrapping modes, vertical alignment, gradient and image text fills, text stroke, line breaks, HTML-escaped content | Byte-identical (live, with installed fonts) | No error expected |
| Vectors and icons | Paths, polygons, lines, arcs, icons with solid and gradient paint | Byte-identical, with SVG ids continuing | No error expected |
| Options | `includeLayerNames`, `includeLayerIds`, `includeHtmlScaffold` on and off; several ids | Byte-identical | No error expected |

</intent-contract>

## Code Map

- `core/src/html.rs`:
  - `html_document` 238, `html_in` 242, `font_query` 283
  - `gradient_css` 346, `svg_gradient` 392, `stroke_overlay` 415, `raster_layer` 472, `fill_layers` 509, `size_css` 540, the tilted wrapper 584, SVG inlining 700, `paint_css` 789, `text_css` 969, `shadow_filter` 1050
  - tests 1080-1843 (28; port the CSS-level ones here, and the deck/PDF/raster/hidden ones go to 7c)
- `core/src/bin/cli.rs:100-104` -- the `html` command. Add `--offline` (calls `Engine::offline()`, as `svg` does).
- `core-go/export` (`Ctx`, `LaidOut`), `core-go/svg` (`paint.go:73` `effectsOf`, `gradient.go:130` `meshGradient`), `core-go/resolve` (`resolve.go:90` `leaf`), `core-go/layout`, `core-go/shader` (painter), `core-go/doc` (`TopIndex`) -- reuse.
- `core-go/svg/parity_test.go` -- the golden-or-live pattern with PNG data-URI comparison by pixels, to copy.

## Tasks & Acceptance

**Execution:**
- [ ] `core/src/bin/cli.rs` -- the test-only `html … --offline` flag.
- [ ] `core-go/svg`, `core-go/resolve` -- export the needed helpers (renames only).
- [ ] `core-go/html/html.go` (document, walk, options, fonts), `size.go`, `paint.go`, `stroke.go`, `effects.go`, `text.go`, `vector.go` -- the port.
- [ ] `core-go/html/html_test.go` -- the ported CSS-level Rust tests plus each matrix row.
- [ ] `core-go/html/parity_test.go`, `core-go/testdata/html/*.pen` -- fixtures per matrix row against `nalarupa-cli html --offline` (goldens for text-free fixtures, live for text).

**Acceptance Criteria:**
- Given MinGW gcc as `CC` (after `go generate ./shader/gpu`), when `go test ./...` runs (also with `GOAMD64=v3`), then it passes with golden HTML parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./html/` runs, then every fixture's HTML matches Rust as stated.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

## Plan Change Log

## Review Triage Log

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)
