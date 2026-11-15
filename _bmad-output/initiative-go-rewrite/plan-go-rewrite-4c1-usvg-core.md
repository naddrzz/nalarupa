---
title: 'Go rewrite phase 4c1: SVG parser core (usvg port, no text or filters)'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'built'
baseline_revision: '4769701dc649a66e59755382d561751c7e11e97e'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4b1-skia-path.md'
warnings: ['oversized']
deferred:
  - summary: >-
      An feImage whose href is an image file or data URL does not use up the generated image and clip-path ids that Rust's image::convert_inner takes (a clipPath id for preserveAspectRatio slice), so later generated ids shift.
    evidence: |-
      Review repro against the live CLI: with a sliced data-URL feImage followed by a sliced <image>, Go gives clipPath1 where Rust gives clipPath2. The filter placeholder only checks that a primitive exists. svg.rs never emits feImage. The fix belongs with the filter primitives in slice 4c3.
    location: >-
      core-go/usvg/convert_filter.go (hasFilterPrimitives)
    severity: low
  - summary: >-
      A pattern cycle through href (p5 href=#p6, whose child fills with url(#p5)) recurses until a fatal stack overflow in both Go and Rust, so an untrusted imported SVG can kill the process.
    evidence: |-
      This comes from the port source: parse.rs find_recursive_pattern never follows the pattern's own href, and the release CLI also crashes (0xc00000fd). Go matches Rust, but it is a crash on untrusted input. Fix by following href links in the recursive-pattern check, in Go (and ideally Rust), with a test.
    location: >-
      core-go/usvg/svgtree_parse.go (recursive pattern check), convert_paint_server.go (convertPattern)
    severity: medium
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
- [x] `core/src/bin/cli.rs` -- add `usvg-tree <file.svg> [--resources <dir>]`. It parses with the same `Options` as `render.rs`, fonts offline, and prints the tree as JSON: per node the kind, id, rel/abs transform, bounding boxes (f32 bits), and for paths their segments, fill and stroke (paint, opacity, rule, width, caps, joins, dash). It also gives clip/mask/marker subtrees and image kind/size/view box, with a placeholder for text and filters. A parse error prints its kind.
- [x] `core-go/usvg/xml.go` -- XML with roxmltree semantics.
- [x] `core-go/usvg/svgtypes*.go` -- the value parsers.
- [x] `core-go/usvg/svgtree*.go`, `css.go` -- the svgtree and styles.
- [x] `core-go/usvg/convert*.go` (converter, shapes, use, switch, clip, mask, marker, paint server, image, units) -- conversion into the tree.
- [x] `core-go/usvg/tree.go` -- the tree types, bounding boxes and `Options`.
- [x] `core-go/usvg/*_test.go` -- the ported unit tests of the files listed above.
- [x] `core-go/usvg/parity_test.go`, `core-go/testdata/usvg/*.svg` plus goldens -- at least 60 documents covering every matrix row, including the 4a fixture exports with text and filters removed. Goldens are used without the CLI, live output with it.

**Acceptance Criteria:**
- Given only Go, when `go test ./usvg/` runs (also with `GOAMD64=v3`), then the unit tests and golden tree parity pass.
- Given the release CLI, when `NALARUPA_CLI=… go test ./usvg/` runs, then every document's tree matches the live crate.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- One package, prefixed by origin: `xml*` (roxmltree 0.21.1), `st*` (svgtypes), `css*` (simplecss 0.2.2), `kurbo*` (kurbo 0.13.1 arc to cubic, shared by svgtypes paths and usvg shapes), `imgsz*` (imagesize 0.14), `svg*`/`eid`/`aid` (svgtree), unprefixed tree and converter code. Shared float helpers live in `f32.go`.
- Tree fields are unexported, as Rust's `pub(crate)`; the public API is the getters. `Paint` is an interface over `Color`, `*LinearGradient`, `*RadialGradient`, `*Pattern`, so `==` matches Rust's `PartialEq` (Arc identity). `refcount.go` reproduces the `Arc::get_mut` checks `update_paint_servers` makes, so a server is edited in place or copied under a generated id exactly as in Rust.
- sin, cos, tan and atan2 are evaluated in double-double and rounded once (`svgtypes_math.go`): they matched the MSVC libm on every corpus document, where Go's `math` functions drift by an ulp.
- Placeholders: `<text>` becomes a `*Text` with its id and absolute transform and zero bounding boxes (slice 4c2); `convertFilters` keeps filter.rs's Ok/Err outcomes, ids and filter regions (so layer boxes match) but no primitives (slice 4c3). The oracle has no fonts, so Rust drops text nodes; documents with text differ until 4c2.
- `core-go/svg/resolver.go` adds `svg.ImageHrefResolver()`, the port of svg.rs `image_href_resolver` (depth-limited nested SVG data urls) plugged into `Options.ImageHrefResolver`, as `render.rs` does. It counts depth per resolver instead of per thread, so each parse needs its own.
- The parity test (`package usvg_test`) dumps the tree through the public getters only, in the CLI's JSON shape, and reports the JSON path of the first difference. 96 documents in `core-go/testdata/usvg` (`res/` holds the referenced images and nested SVGs); roxmltree's ast fixtures are in `core-go/testdata/usvg-xml`.

## Plan Change Log

- usvg 0.47 depends on svgtypes 0.16.1, not 0.15 as the Code Map said; 0.16.1 is ported.
- usvg defines `Error::ElementsLimitReached` but never returns it: the 1,000,000-node and depth limits surface as `ParsingFailed` with roxmltree's `NodesLimitReached`, and the Go port does the same (corpus `elements-limit`, `depth-limit`).

## Review Triage Log

### 2026-11-15 — Review pass
- verdicts: 2 findings — high 0, medium 1, low 1, false 0, maybe-false 0
- findings:
  - `[low]` `[defer]` A data-URL or file `feImage` doesn't use up the generated image and clip-path ids that Rust's `image::convert_inner` takes, so later ids shift (reproduced: `clipPath1` vs `clipPath2`). — The intent excludes filter primitives. Deferred to slice 4c3 (frontmatter `deferred`).
  - `[medium]` `[defer]` A pattern cycle through `href` overflows the stack, fatally, in both Go and Rust. — Pre-existing: it comes from `find_recursive_pattern` in the port source, and the Rust CLI crashes the same way. Deferred as a crash on untrusted input (frontmatter `deferred`).

## Auto Run Result

- **Summary:** `core-go/usvg` ports the vendored usvg 0.47 parser without text and filter primitives:
  - XML (roxmltree 0.21.1) and value parsers (svgtypes 0.16.1, which usvg 0.47 actually uses)
  - CSS (simplecss), kurbo arc-to-cubic and imagesize
  - svgtree, the converters (shapes, use, switch, clip, mask, marker, paint servers, images, units), the tree with bounding boxes, and `Options`/`ImageHrefResolver`

  Text and filters are placeholders for 4c2 and 4c3. `core-go/svg/resolver.go` adds the svg.rs image-href resolver, and Rust gains a test-only `usvg-tree` oracle.
- **Files:**
  - `core-go/usvg/*.go`: the port, about 22k lines with tests
  - `core-go/svg/resolver.go`: the resolver
  - `core-go/testdata/usvg/` (96 documents with goldens) and `core-go/testdata/usvg-xml/`: fixtures
  - `core/src/bin/cli.rs`: the oracle
- **Review:**
  - Patches applied: 0.
  - Deferred: 2 (one low, handled by 4c3; one medium, a pre-existing pattern-cycle crash).
  - Rejected: none.
- **Follow-up review recommended:** false (no patches).
- **Verification:**
  - `go vet` and `go test` pass, and so does `GOAMD64=v3 go test ./usvg/`.
  - All 96 documents match the release CLI bit for bit, live.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - On Linux, a live run may drift by 1 ulp in arcs and rotations: Go's trig is correctly rounded, glibc's differs, and the test compares exactly.
  - `svg.ImageHrefResolver()` keeps per-instance depth state, so each concurrent parse needs its own resolver.
  - Rust panics such as `unreachable!()` are mirrored.

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./usvg/` -- expected: pass
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./usvg/` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-15, Windows):** `go vet ./...` and `go test ./...` pass; `GOAMD64=v3 go test ./usvg/` passes; live parity matches the release CLI bit for bit on all 96 documents; `cargo test --release` passes (292 tests). Not run on Linux.
