---
title: 'Go rewrite phase 4d: resvg renderer, filters and image export'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'built'
baseline_revision: '05610aaaee81de9fbc30e8ccdf6cde60d71ab8d2'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4b2-skia-raster.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4c3-usvg-filters.md'
warnings: []
deferred:
  - summary: >-
      JPEG images decode up to 3 levels per channel away from Rust: Go's image/jpeg IDCT is not zune-jpeg's.
    evidence: |-
      testdata/render/images/images-jpeg.svg: max channel delta 3 on about 3.5% of the pixels measured; fixtures.json states maxDelta 3, maxShare 0.05. The chroma upsampling and color conversion already follow zune-jpeg (resvg/image_yuv.go); 4:4:4 files differ by at most 2 before scaling. Exact parity needs zune-jpeg's integer IDCT behind a JPEG entropy decoder, i.e. a port of the decode path of zune-jpeg 0.5.
    location: >-
      core-go/resvg/image.go (decodeJPEG)
    severity: low
---

<intent-contract>

## Intent

**Problem:** The Go core can now build the SVG, parse it into a usvg tree and rasterise paths, but nothing yet paints a tree. So no node can be exported as an image, and the canvas slice has no renderer to build on.

**Approach:**
- Port the vendored resvg 0.47 into a new `core-go/resvg` package, with all four `Nalarupa:` patches. It covers `render`, `render_node`, groups and layers, paths, images (raster and nested trees), clips, masks, and every filter (box and IIR blur, offset, flood, composite, blend, merge, morphology, color matrix, component transfer, convolve, displacement, lighting, turbulence, tile, image).
- Port `render.rs`'s export path into a new `core-go/export` package: `node_tree_at`, `node_image` and `encode_tree`.
  - PNG uses `image/png`, written from tiny-skia's demultiplied pixels.
  - JPEG is flattened on white and encoded with `image/jpeg`.
  - Lossless WebP is a pure Go port of the `image-webp` encoder.
- Prove pixel parity against `nalarupa-cli render` and a new test-only CLI subcommand that renders an SVG file with the real resvg.

## Boundaries & Constraints

**Always:**
- The resvg port draws through `core-go/skia` and reads `core-go/usvg` trees, with no parallel geometry or paint types. It keeps resvg's layer, isolation, opacity, clip and mask order and the four patches (`lib.rs:68`, `render.rs:78` and `:85`, `filter/mod.rs:362` and `:1136`).
- Pixel parity: premultiplied RGBA bytes must match exactly, except where a difference traces to a libm call (CRT vs Go), `rcpps`, or the deferred 4b2 cubic-clip case. Those are allowed a stated tolerance per fixture (max channel delta and max differing-pixel share), recorded in the fixture list.
- Export sizes follow `encode_tree` exactly: round(w·scale) and round(h·scale) clamped to 1..8192, scale clamped to 0.05..8, and an error string for a pixmap that is too large.
- PNG and lossless WebP files decode to exactly the pixels Rust's files decode to. The file bytes may differ. JPEG is compared by decoded pixels within a stated tolerance, since the encoders differ.
- Floating-point products get explicit conversions (no FMA). No cgo, and no new dependencies besides what `core-go` has.

**Never:**
- No canvas layers, tiles, dirty rectangles, hit testing or slides (slice 4e), no PDF (4f), no shaders (phase 6).
- No change to `app/`, the FFI, or Rust behaviour apart from test-only CLI additions.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Node export PNG | Every top-level node of the 4a svg fixtures, `templates/{blank,mobile}`, and the 4c corpus scenes rendered at scale 1 and 2 | Pixels equal `nalarupa-cli render` (tolerance only as stated per fixture) | No error expected |
| Filters | The 4c3 filter corpus (every primitive) rendered via the resvg oracle | Pixels equal (stated tolerance for blur/lighting/turbulence only where libm-driven) | No error expected |
| Clips, masks, opacity, blend | Nested clip paths, luminance and alpha masks, group opacity with each blend mode | Pixels equal | No error expected |
| Images | PNG/JPEG/GIF/WebP raster images with each rendering mode, a nested SVG image, a missing image | Pixels equal | Missing images draw nothing, as in Rust |
| Text | The 4c2 live text corpus rendered with installed fonts | Pixels equal on the same machine | No error expected |
| Formats | Same node as png, jpeg (q 1, 80, 100) and webp, plus scales 0.01 and 20 | Same dimensions as Rust. PNG and WebP decode to equal pixels, JPEG within the stated tolerance | A too-large pixmap gives the same error string |

</intent-contract>

## Code Map

- `core/vendor/resvg/src/` -- the port source (3.7k lines without `main.rs`): `lib.rs` (`render`, `render_node`, patch at 68), `render.rs` (patches at 78 and 85), `path.rs`, `image.rs`, `clip.rs`, `mask.rs`, `geom.rs`, and `filter/` (`mod.rs` 1146 with patches at 362 and 1136, `box_blur.rs`, `iir_blur.rs`, `lighting.rs`, `turbulence.rs`, `color_matrix.rs`, `convolve_matrix.rs`, `component_transfer.rs`, `composite.rs`, `displacement_map.rs`, `morphology.rs`).
- `core/src/render.rs:202-229` (`encode_tree`), `:285-330` (`node_svg_at`, `node_tree_at`, `node_png`, `node_image`), and `tree_options()` -- the export path to port. The usvg options include `svg::image_href_resolver`.
- `~/.cargo/registry/src/index.crates.io-*/image-webp-*/src/encoder.rs` -- the lossless WebP encoder to port.
- `core-go/skia` (Pixmap, Painter, Mask, shaders, blend), `core-go/usvg` (trees, `Options`), `core-go/svg` (`Export`, `Assets`, `ImageHrefResolver`), `core-go/text` (fonts for usvg options) -- reuse all of these.
- `core/src/bin/cli.rs` -- `render <doc> <dir> [scale]` already writes one PNG per top-level node. Add `resvg <file.svg> <out.rgba> [--fonts dir] [--scale s]`, which writes premultiplied bytes, and `image <doc> <id> <format> <scale> <quality> <out>`, which calls `node_image`.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- add the `resvg` and `image` subcommands (test-only).
- [x] `core-go/resvg/render.go`, `path.go`, `image.go`, `clip.go`, `mask.go` -- the renderer.
- [x] `core-go/resvg/filter*.go` -- the filters.
- [x] `core-go/export/export.go` -- `NodeTreeAt`, `NodeImage`, `EncodeTree`.
- [x] `core-go/export/webp.go` -- the lossless WebP encoder port.
- [x] `core-go/resvg/*_test.go`, `core-go/export/*_test.go` -- unit tests (blur kernels, the patches, encoder round trips).
- [x] `core-go/resvg/parity_test.go`, `core-go/export/parity_test.go`, and fixtures under `core-go/testdata/render/` -- every matrix row. Goldens (raw `.rgba`) are used for text-free cases, live output for the rest. A failure reports the max channel delta, the count of differing pixels and the first differing pixel.

**Acceptance Criteria:**
- Given only Go, when `go test ./...` runs (also with `GOAMD64=v3`), then it passes with golden render parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./resvg/ ./export/` runs, then every fixture matches within its stated tolerance.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- Layout: `resvg/render.go` (lib.rs, render.rs, geom.rs), `path.go`, `image.go` and `image_yuv.go`, `clip.go` (clip.rs and mask.rs), `filter.go` (filter/mod.rs), `filter_blur.go` (box and IIR), `filter_pixel.go` (color matrix, component transfer, arithmetic composite, convolve, displacement, morphology), `filter_lighting.go`, `filter_turbulence.go`, `num.go` (Rust casts, float-cmp, strict-num). `export/export.go` and `export/webp.go`.
- render::Context is left out: after the Nalarupa patches its `max_bbox` has no reader (rustc warns that it is dead).
- Filter images follow Rust's Rc: `take` hands over an image no stored result shares and copies one that is shared.
- Raster images use the Go decoders. PNG follows `decode_png` (8-bit normalisation, 16-bit high byte, premultiply_u8). Lossy WebP gets image-webp's fancy upsampling and YUV conversion on x/image/webp's planes (VP8 is exact), and `has_alpha` decides between rgb_to_pixmap and rgba_to_pixmap. JPEG gets zune-jpeg's upsampling (over the MCU-padded planes, which Go keeps) and color conversion; the IDCT difference is deferred (frontmatter).
- `svg.NodeSVGAt` is render.rs `node_svg_at` with its scale; `svg.Export` now calls it at scale 1. `export.Ctx` holds the document, `*text.Fonts` (measurer and usvg font database) and the assets; scripts are not expanded (phase 5).
- The WebP encoder ports the Rgba8 path only (what `encode_tree` uses), with std's BinaryHeap sift order mirrored. Every WebP export in the corpus is byte-identical to Rust's; the length-limit branch sorts stably where Rust sorts unstably, which can only change the bytes, not the pixels.
- CLI: `resvg` and `image` as planned; `render` and `image` take `--offline` (as `svg` does), because an online engine leaves out images and icons it is still fetching.
- Goldens: `testdata/render/fixtures.json` lists the corpora (testdata/usvg and usvg/filter at scales 1 and 2, testdata/render/svg at 1 and 2, testdata/render/images at 1, testdata/usvg/text live only) and the tolerances. The raw `.rgba` goldens are stored gzipped (`.rgba.gz`, 3.2 MB in all; raw they would be over 70 MB). Export goldens are Rust's own files (`testdata/render/export`, 1.3 MB).
- Tolerances: `render/blend-opacity` +-1 on 0.1% (rcpps in color-dodge/burn, as in 4b2); `images/images-jpeg` 3 levels on 5% (decoder IDCT; measured 3 levels on 3.5%); one entry per JPEG export (`export/<fixture>.<node>.q<quality>@<scale>`) with its measured max delta, share of differing pixels and mean delta (the plan's JPEG encoder difference: DCT and quantisation rounding, and image/jpeg always subsamples chroma where jpeg-encoder does not at quality 90 and above); only the few-pixel exports at scale 0.01 get a share of 1. Everything else, including every filter primitive, blur, lighting and turbulence, matches bit for bit.
- The "too large pixmap" error cannot be reached through NodeImage (sizes clamp to 8192, which allocates); `TestErrorParity` compares the error strings of a missing node, a missing instance child and hidden nodes instead.

## Plan Change Log

## Review Triage Log

### 2026-11-15 — Review pass
- verdicts: 7 findings — high 0, medium 0, low 7, false 0, maybe-false 0
- findings:
  - `[low]` `[reject]` `decodeGIF`: Go's `image/gif` rejects a GIF whose pixels index past a short palette, so the image draws nothing, while the `gif` crate leaves those pixels transparent. — Rare malformed GIFs. The fix means rewriting palette tables in the GIF stream or porting the decoder.
  - `[low]` `[patch]` The JPEG decode tolerance's documentation is inconsistent (3.5% vs 5%, and "a level or two" vs a measured max of 3). The cause itself is already deferred. — Fixed the evidence text in frontmatter and the `decodeJPEG` comment.
  - `[low]` `[patch]` JPEG export tolerance is keyed per format and quality, so the loose values needed by the tiny scale-0.01 exports cover every q80 export. — Fix: key per fixture, keeping loose values only for the tiny exports.
  - `[low]` `[patch]` `TestRenderNodeRefusesEmptyGroup` is vacuous: its node is nil, so it asserts nothing. — Fix: build a tree where `RenderNode` must return false, and assert it.
  - `[low]` `[patch]` The `render.rs:85` patch (clamping a filter-free layer to the target) has no unit test. — Fix: add one.
  - `[low]` `[patch]` The test CLI `image` subcommand holds `engine()` while it rasterises and encodes, against AGENTS.md. — Fix: `node_tree_at` under the guard, drop the guard, then `encode_tree`.
  - `[low]` `[patch]` Stray empty files `0`, `heightMax` and `widthMax` in the repo root. — Deleted.

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./resvg/ ./export/` -- expected: pass
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-15, Windows, CLI at `%TEMP%/nalarupa-go-target`):** `go vet ./...` and `go test ./...` pass, and so does `GOAMD64=v3 go test ./resvg/ ./export/`. Live, all of `go test ./...` passes against the release CLI (also `GOAMD64=v3` for resvg and export): 303 resvg renders (the usvg, filter, render and images corpora) plus the 4c2 text corpus at scales 1 and 2, 34 node PNG exports (with the live text fixture), 26 format exports and 4 error strings. `cargo test --release` passes (292 tests). Not run on Linux.

## Auto Run Result

- **Summary:**
  - `core-go/resvg` ports the vendored resvg 0.47 with all four `Nalarupa:` patches: groups and layers, paths, raster and nested-SVG images, clips, masks, and every filter primitive.
  - `core-go/export` ports `render.rs`'s export path (`NodeTreeAt`, `NodeImage`, `NodePNG`, `EncodeTree`) with PNG, JPEG (flattened on white) and a pure Go lossless WebP encoder, whose output is byte-identical to Rust's in the corpus.
  - `svg.NodeSVGAt` adds scaled node export.
  - Rust gains test-only `resvg` and `image` subcommands, plus `--offline` on `render`.
- **Files:**
  - `core-go/resvg/*.go`: the renderer and filters
  - `core-go/export/*.go`: export and the WebP encoder
  - `core-go/svg/export.go`: `NodeSVGAt`
  - `core-go/testdata/render/`: `fixtures.json` with per-fixture tolerances, fixtures and gzipped goldens
  - `core/src/bin/cli.rs`: the oracles
- **Review:**
  - Patches applied: 6 low.
    - JPEG export tolerance is now per fixture.
    - The JPEG decode figures are consistent.
    - The vacuous refusal test was replaced with a real one.
    - A test was added for the `render.rs:85` clamp.
    - The CLI `image` command no longer holds `engine()` while it encodes.
    - Stray files were removed.
  - Deferred: 1 low, from implementation (zune-jpeg IDCT rounding).
  - Rejected: 1 low (malformed GIF palette indices).
- **Follow-up review recommended:** false (patched: 0 high, 0 medium, 6 low).
- **Verification:**
  - `go vet` and `go test` pass, and so does `GOAMD64=v3 go test ./resvg/ ./export/`.
  - Live parity against the rebuilt release CLI passes in full: 303 renders, the text corpus, node, format and error exports.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - JPEG decode is within 3 levels on about 3.5% of pixels.
  - JPEG export differs, because of chroma subsampling.
  - Malformed GIFs draw nothing.
  - Script nodes are not run before export (phase 5).
  - Tolerances are platform-specific (SSE2/AVX2), and Linux has not been run.
