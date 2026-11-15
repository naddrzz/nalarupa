---
title: 'Go rewrite phase 4c1: SVG parser core (usvg port, no text or filters)'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'in-progress'
baseline_revision: 'f20ad6f5ce52089d7d7b9e1a246d3d6b2c0c8492'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4b1-skia-path.md'
warnings: ['oversized']
deferred: []
---

<intent-contract>

## Intent

**Problem:** Every Rust render parses the SVG string from `svg.rs` (plus inlined icons and nested SVG images) into a `usvg::Tree` before painting. The Go renderer needs the same tree, or pixels drift no matter how exact the rasteriser is.

**Approach:** Port the vendored usvg 0.47 parser, except text and filters, to a new `core-go/usvg` package:
- XML loading (roxmltree semantics)
- the svgtree with CSS (simplecss) and presentation-attribute resolution
- the svgtypes parsers it needs (colors, lengths, numbers, transforms, paths, paint, view boxes, `preserveAspectRatio`)
- units, shapes, path conversion, including kurbo's arc-to-cubic
- `use`/`symbol`/nested `svg`, `switch`, clip paths, masks, markers, paint servers (linear and radial gradients, patterns)
- images (data, file, nested SVG, svgz), with the image-href resolver hook
- the tree with groups, transforms and bounding boxes, built on `core-go/skia` paths and transforms

Text elements are parsed into a placeholder `Text` node and filters into a placeholder `Filter` reference, to be filled by slices 4c2 and 4c3. A new test-only Rust CLI subcommand dumps the real tree for comparison.

## Boundaries & Constraints

**Always:**
- Same tree as the vendored usvg for the same input and options: node order, ids, group flattening and isolation decisions, transforms (abs and rel), path segments, paints, opacities, clip/mask/marker resolution, and bounding boxes (`bounding_box`, `stroke_bounding_box`, `abs_*`, `layer_bounding_box`). Comparison is bit for bit on `f32`, with at most 1 ulp where a libm call (`atan2`, `sin`, `cos`, `sqrt`, `tan`) is involved.
- Same accept/reject behaviour: a document usvg refuses (malformed XML, an unknown root, too many elements, nesting too deep) returns an error with the same `usvg::Error` kind.
- Reuse `core-go/skia` (`Path`, `PathBuilder`, `Transform`, `Rect`, `NonZeroRect`, `Size`, `Stroke`) instead of new geometry types. Floating-point products get explicit conversions (no FMA), as in 4b.
- Image loading goes through an options struct mirroring `usvg::Options` (`resources_dir`, `dpi`, `default_size`, `image_href_resolver`, `style_sheet`, font settings kept for 4c2), so `core-go/svg`'s `Assets` can plug in as `render.rs` does.
- No cgo and no new dependencies apart from what `core-go` already has.

**Never:**
- No text layout or flattening, no filter primitives or regions, no `writer.rs`, `main.rs` or `colr.rs`.
- No rasterising (slice 4d).
- No change to `app/`, the FFI or Rust behaviour other than the test-only CLI subcommand.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| svg.rs output | Text- and filter-free exports from the 4a fixtures (shapes, fills, strokes, gradients, patterns, clips, masks, blends, opacity) | Same tree as usvg | No error expected |
| Icons | The inlined icon markup from the 4a fixtures (circle, line, polyline, polygon, ellipse, rect with rx/ry, `currentColor`, stroke attributes on `<svg>`) | Same tree | No error expected |
| Structure | `use` with transforms and nested `use`, `symbol` with viewBox and preserveAspectRatio, nested `svg`, `switch`, `display:none`/`visibility`, CSS `<style>` with class/id/type selectors and `!important` | Same tree | No error expected |
| Paint servers | Gradients with `href` inheritance, units, spread, a transform, a single stop, degenerate (zero-length, zero-radius); patterns with viewBox and nested content | Same tree, including usvg's fallbacks for degenerate servers | No error expected |
| Images | PNG/JPEG/GIF/WebP data URIs, a relative file, nested SVG, svgz, a missing file, a resolver override | Same image nodes (kind, size, view box, rendering mode) | A missing image is dropped as usvg drops it |
| Invalid input | Malformed XML, a non-svg root, an element count over the limit, a `use` cycle, a negative width | Same error kind, or the same skip/clamp as usvg | No panic |

</intent-contract>

## Code Map

- `core/vendor/usvg/src/` -- the port source:
  - `parser/` (svgtree `mod.rs` 1083, `parse.rs` 811, `names.rs` 722; `converter.rs` 1074, `paint_server.rs` 1091, `use_node.rs` 370, `marker.rs` 489, `image.rs` 348, `shapes.rs` 329, `style.rs` 296, `clippath.rs`, `mask.rs`, `switch.rs`, `units.rs`, `options.rs`)
  - `tree/` (`mod.rs` 1916, `geom.rs` 185)
  - Skip `parser/text.rs`, `parser/filter.rs`, `tree/text.rs`, `tree/filter.rs`, `text/`, `writer.rs` and `main.rs` beyond the placeholders.
- `~/.cargo/registry/src/index.crates.io-*/` -- the dependency semantics to port:
  - `roxmltree-0.21.*` (XML, entities, namespaces, limits)
  - `svgtypes-0.15.*` (the parsers listed in the Intent)
  - `simplecss-0.2.*` (selectors and specificity)
  - `kurbo-0.13.*` (arc to cubic only)
  - `imagesize-0.14.*` (image dimensions)
- `core-go/skia` -- path, transform and rect types to reuse.
- `core-go/svg/assets.go` -- the image resolution and limits that the `image_href_resolver` hook calls in `render.rs` (`image_href_resolver` at `svg.rs:369`).
- `core/src/render.rs` (around `node_tree_at` 316) -- the `usvg::Options` the core passes.
- `core/src/bin/cli.rs` -- the existing oracles show the pattern. `resvg::usvg` is the vendored crate.

## Tasks & Acceptance

**Execution:**
- [ ] `core/src/bin/cli.rs` -- add `usvg-tree <file.svg> [--resources <dir>]`. It parses with the same `Options` as `render.rs`, fonts offline, and prints the tree as JSON: per node the kind, id, rel/abs transform, bounding boxes (f32 bits), and for paths their segments, fill and stroke (paint, opacity, rule, width, caps, joins, dash). It also gives clip/mask/marker subtrees and image kind/size/view box, with a placeholder for text and filters. A parse error prints its kind.
- [ ] `core-go/usvg/xml.go` -- XML with roxmltree semantics.
- [ ] `core-go/usvg/svgtypes*.go` -- the value parsers.
- [ ] `core-go/usvg/svgtree*.go`, `css.go` -- the svgtree and styles.
- [ ] `core-go/usvg/convert*.go` (converter, shapes, use, switch, clip, mask, marker, paint server, image, units) -- conversion into the tree.
- [ ] `core-go/usvg/tree.go` -- the tree types, bounding boxes and `Options`.
- [ ] `core-go/usvg/*_test.go` -- the ported unit tests of the files listed above.
- [ ] `core-go/usvg/parity_test.go`, `core-go/testdata/usvg/*.svg` plus goldens -- at least 60 documents covering every matrix row, including the 4a fixture exports with text and filters removed. Goldens are used without the CLI, live output with it.

**Acceptance Criteria:**
- Given only Go, when `go test ./usvg/` runs (also with `GOAMD64=v3`), then the unit tests and golden tree parity pass.
- Given the release CLI, when `NALARUPA_CLI=… go test ./usvg/` runs, then every document's tree matches the live crate.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

## Plan Change Log

## Review Triage Log

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./usvg/` -- expected: pass
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./usvg/` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)
