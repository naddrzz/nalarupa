---
title: 'Go rewrite phase 4c3: SVG filters (usvg port)'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'built'
baseline_revision: '260a2e5f57cb37d6399ed1769479f4939264796c'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4c1-usvg-core.md'
warnings: []
deferred:
  - summary: >-
      An feImage that links to a group whose child uses the same filter recurses until a fatal stack overflow, because svgFixRecursiveFeImage only catches the direct self-reference.
    evidence: |-
      Review repro: <filter id="f"><feImage xlink:href="#g"/></filter><g id="g"><rect filter="url(#f)"/></g> crashes Go (fatal stack overflow) and the release CLI (stack overflow, exit 127). This comes from the port source, but it is new in Go now that feImage targets are converted. It is a crash on untrusted input. Fix with a transitive recursion check in Go (ideally in Rust too), with a test.
    location: >-
      core-go/usvg/svgtree_parse.go (svgFixRecursiveFeImage), convert_filter.go (convertFeImageInner)
    severity: medium
  - summary: >-
      Mirrored Rust unwrap panics (mustPositiveF32 in feMorphology/feTurbulence, reachable with finite input such as radius=1e38 under objectBoundingBox) have no containment in Go, where Rust's FFI wraps calls in catch_unwind.
    evidence: |-
      core/src/lib.rs:543 and :574 catch panics and return "internal error (panic)". core-go has no recover() outside tests yet. The engine/C ABI phase must add recover at every exported entry point. Until then, a hostile imported SVG can crash the Go process.
    location: >-
      core-go/usvg/convert_filter.go (mustPositiveF32)
    severity: medium
---

<intent-contract>

## Intent

**Problem:** Shadows, layer blur and background blur in the editor reach usvg as SVG filters. `core-go/usvg` still keeps only filter ids, regions and accept/reject decisions, with no primitives, so resvg's filter rendering (slice 4d) would have nothing to draw.

**Approach:** Port usvg 0.47's `parser/filter.rs` and `tree/filter.rs` into `core-go/usvg`, replacing the 4c1 filter placeholder. That covers filter elements, `filter` functions in the `filter` property, primitive regions and units, `in`/`result` linking, color-interpolation, and every primitive usvg models (blend, color matrix, component transfer, composite, convolve matrix, diffuse/specular lighting with light sources, displacement map, drop shadow, flood, Gaussian blur, image, merge, morphology, offset, tile, turbulence). `feImage` with a string href goes through the image converter and uses up the same generated image and clip-path ids as Rust. That fixes the id shift deferred from 4c1.

## Boundaries & Constraints

**Always:**
- Same filter trees as Rust: primitive order, kinds and all their parameters, input/result names (including generated `result` names), regions, the effect of `primitiveUnits`/`filterUnits`, color-interpolation, and the transforms and bounding boxes of filtered groups. `f32` values match bit for bit, with at most 1 ulp where a libm call is involved.
- Same accept/reject behaviour, including an invalid filter that turns the element off, an unknown or erroneous primitive, and a reference to a missing filter.
- Generated ids (`filter`, `image`, `clipPath`) advance exactly as in Rust, including through `feImage`.
- Floating-point products get explicit conversions (no FMA). No cgo and no new dependencies.

**Never:**
- No filter rendering (slice 4d).
- No change to `app/`, the FFI, or Rust behaviour apart from extending the test-only `usvg-tree` dump.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| svg.rs effects | The 4a `effects` fixture exports (drop and inner shadows as feGaussianBlur/feOffset/feFlood/feComposite, layer blur, background blur) | Same filter trees | No error expected |
| All primitives | One document per primitive kind with typical and edge parameters (zero std-dev, negative radius, empty kernel, lighting with each light source, turbulence with stitchTiles) | Same trees, or the same skip or disable as Rust | No panic |
| Filter functions | `filter="blur(2px) drop-shadow(...) grayscale(50%)"` and the other CSS functions | Same primitive chains | An invalid function list is handled as Rust handles it |
| Regions and units | `objectBoundingBox` vs `userSpaceOnUse`, percentages, a missing region, a zero-size element bbox | Same regions, or the element removed as in Rust | No error expected |
| feImage ids | A data-URL `feImage` with `preserveAspectRatio` slice, followed by a sliced `<image>` (the 4c1 repro) | Same generated ids (`clipPath2` on the later image) | No error expected |
| Broken references | `filter="url(#missing)"`, a cycle through `in`, an unknown `in` name | Same results as Rust | No panic |

</intent-contract>

## Code Map

- `core/vendor/usvg/src/parser/filter.rs` (1279) and `tree/filter.rs` (1061) -- the port source.
- `core/vendor/usvg/src/parser/image.rs` (`convert_inner`, the `aspect.slice` branch calling `gen_clip_path_id`, and `gen_image_id`) -- the id side effects `feImage` must reproduce.
- `core-go/usvg/convert_filter.go` -- 4c1's placeholder (`convertFilterURL`, `hasFilterPrimitives`, with a TODO for feImage), to be replaced. `convert_image.go` and the id cache in `convert.go` are where `feImage` hooks in.
- `core-go/usvg/svgtypes*.go` -- 4c1's value parsers. Add filter-function parsing (svgtypes `FilterValueListParser`) if it is missing.
- `core/src/bin/cli.rs` -- the `usvg-tree` dump. Extend it to print each filter's primitives with all parameters as f32 bits.
- `core-go/testdata/svg/effects.*.svg` -- the 4a goldens to use as inputs.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- dump filter primitives in full from `usvg-tree`.
- [x] `core-go/usvg/filter_tree.go` -- the filter tree types.
- [x] `core-go/usvg/convert_filter.go` -- the converter, replacing the placeholder, plus the `feImage` id path.
- [x] `core-go/usvg/filter_test.go` -- the vendored unit tests plus the broken-reference rows.
- [x] `core-go/usvg/parity_test.go`, `core-go/testdata/usvg/filter/*.svg` plus goldens -- at least 40 documents covering every matrix row. Goldens are used without the CLI, live output with it.

**Acceptance Criteria:**
- Given only Go, when `go test ./usvg/` runs (also with `GOAMD64=v3`), then it passes, including golden filter parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./usvg/` runs, then every filter document matches the live crate.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- The `usvg-tree` dump now prints each group's filters in full (`"filters":[FL]`, see the doc comment in `cli.rs`) instead of ids; the tree-level `filters` stays an id list. No existing golden changed: none of the 96 4c1 documents keeps a filter.
- `filter_tree.go` mirrors tree/filter.rs with unexported fields and getters. Rust's enums are Go sum types: `FilterKind` (`*FeBlend` ... `*FeTurbulence`), `ColorMatrixKind`, `TransferFunction`, `LightSource`; `FilterInput` and `CompositeOperator` are comparable structs, so `==` is `PartialEq`.
- `Group.Subroots` visits feImage roots, and so do the collectors that use it. `update_paint_servers` updates a filter's feImage roots only under `Arc::get_mut`, so `refcount.go` now tracks filters as well (groups and trees hold them; their feImage roots are counted once). `image-shared-gradient` covers the shared case.
- Rust's `as` casts from floats saturate (`asI32`, `asU32` in `f32.go`), and `order`/`columns*rows` products wrap as in the release build (`convolve-matrix` f10).
- feImage with a string href takes `gen_image_id` before the subregion and goes through `convertImageInner`, which takes a clip-path id when it slices. The 4c1 deferred id shift is fixed (`image-ids`: the later image gets `clipPath2`).
- Mirrored Rust panic: an infinite `feMorphology` radius or `feTurbulence` baseFrequency (`1e39`) panics in usvg (`PositiveF32::new(..).unwrap()`) and in the port (`TestFilterPanicsLikeRust`).
- Corpus: 45 documents in `core-go/testdata/usvg/filter` (one or more per primitive, the filter functions, regions/units, zero-size bboxes, broken references, href chains and cycles, `in`/`result` linking, color-interpolation, shared filters, filters in use/pattern/mask/clipPath/marker/nested svg, feImage data/file/element/nested-SVG/id cases, and the three 4a `effects` exports). Its resources dir is the folder itself; file images use `../res/`.

## Plan Change Log

## Review Triage Log

### 2026-11-15 — Review pass
- verdicts: 3 findings — high 0, medium 2, low 1, false 0, maybe-false 0
- findings:
  - `[medium]` `[defer]` An `feImage` → group → child-with-same-filter cycle overflows the stack fatally, in both Go and Rust. — Pre-existing in the port source, and the fix needs a transitive recursion check. Deferred (frontmatter), alongside 4c1's pattern-cycle crash.
  - `[medium]` `[defer]` Deliberate mirrored panics (`mustPositiveF32`) contradict the matrix's "No panic", and Go has no `catch_unwind` equivalent. — Rust contains these at the FFI boundary. Deferred to the engine/C ABI phase, which must add `recover()` at every entry point (frontmatter, and deferred-work.md).
  - `[low]` `[patch]` Stray empty files `0` and `Kind` in the repo root. — Deleted before commit.

## Auto Run Result

- **Summary:** usvg 0.47's filters are ported into `core-go/usvg` (`parser/filter.rs` and `tree/filter.rs`), replacing the 4c1 placeholder:
  - filter elements and `href` chains, all CSS filter functions, regions and units, `in`/`result` linking (with generated names), color-interpolation, every primitive, and the transparent stand-in for broken primitives
  - `feImage` image/clipPath id consumption, which fixes the id shift deferred from 4c1
  - `feImage` contents included in subroots, paint-server updates and refcounts

  `usvg-tree` now dumps filters in full.
- **Files:**
  - `core-go/usvg/{filter_tree,convert_filter}.go`: the filter port
  - `core-go/usvg/{convert_paint_server,refcount,svgtree,tree,f32}.go`: wiring
  - `core-go/usvg/filter_test.go` and `parity_test.go`: tests
  - `core-go/testdata/usvg/filter/`: 45 documents
  - `core/src/bin/cli.rs`: the extended dump
- **Review:**
  - Patches applied: 1 low (stray files removed).
  - Deferred: 2 medium (the feImage recursion crash, and panic containment for the engine phase).
  - Rejected: none.
- **Follow-up review recommended:** false (patched: 0 high, 0 medium, 1 low).
- **Verification:**
  - `go vet` and `go test` pass, and so does `GOAMD64=v3 go test ./usvg/`.
  - Live parity against the release CLI passes: 45 filter documents, the 96 core documents, and the text suites.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - The deferred crashes on hostile SVGs.
  - Linux has not been run.

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./usvg/` -- expected: pass
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-15, Windows, CLI at `%TEMP%/nalarupa-go-target`):** `go vet ./...` and `go test ./...` pass; `GOAMD64=v3 go test ./usvg/` passes; live, all 45 filter documents and the 96 main documents match the release CLI bit for bit (also under `GOAMD64=v3`), as do the text and other parity suites; `cargo test --release` passes (292 tests). Not run on Linux.
