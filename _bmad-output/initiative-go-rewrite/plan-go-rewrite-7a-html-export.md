---
title: 'Go rewrite phase 7a: HTML/CSS export'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: 'fcce4d442744fa6eb932d5792ef26d46cfe942f3'
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
- [x] `core/src/bin/cli.rs` -- the test-only `html … --offline` flag.
- [x] `core-go/svg`, `core-go/resolve` -- export the needed helpers (renames only).
- [x] `core-go/html/html.go` (document, walk, options, fonts), `size.go`, `paint.go`, `stroke.go`, `effects.go`, `text.go`, `vector.go` -- the port.
- [x] `core-go/html/html_test.go` -- the ported CSS-level Rust tests plus each matrix row.
- [x] `core-go/html/parity_test.go`, `core-go/testdata/html/*.pen` -- fixtures per matrix row against `nalarupa-cli html --offline` (goldens for text-free fixtures, live for text).

**Acceptance Criteria:**
- Given MinGW gcc as `CC` (after `go generate ./shader/gpu`), when `go test ./...` runs (also with `GOAMD64=v3`), then it passes with golden HTML parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./html/` runs, then every fixture's HTML matches Rust as stated.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- Entry point: `html.Document(ctx *export.Ctx, ids []string, opts jsonv.Value) (string, error)`. `export.Ctx` gains `LaidOut(index)` (render.rs `laid_out`), resolving without libraries as `svg.NodeSVGAt` does. Exported for the port (renames only): `svg.Effects`/`EffectsOf` with `Blur`/`Backdrop`, `svg.MeshGradient`, `svg.Pixmap.EncodePNG`, `resolve.Leaf`.
- Shader fills go through `svg.DefaultShader` (the 6b GPU painter, linked by a blank import of `core-go/shader`), with no backdrop, as Rust's `shader::png(.., None)`.
- `Option<&Value>` sizes are a `spec` (value plus presence): `Value::from(f64)` of a non-finite number is null there, as in serde.
- The CLI's `html` command takes only the default options, so options and several ids are covered by `TestOptions` in `html_test.go`; parity fixtures use the defaults, one top-level node per export.
- Parity fixtures: `testdata/html/{layout,paint,strokes,vectors}.pen` with goldens (text-free, exported with nil fonts), `testdata/html/live/{text,shader}.pen` live only. Shader PNGs are compared within ±3 per channel (6b), mesh PNGs within ±1 (4a). The polygon in `vectors` needs the 4a 1e-12 number tolerance (trigonometry).
- Ported Rust tests: the CSS-level ones (23, `html_test.go`); `svg_export_reads_images_after_planning`, `exports_slides_deck_and_rasters`, `pdf_export_skips_damaged_images` and the two hidden-layer tests are export-job tests left to 7c.

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 2 findings — high 0, medium 0, low 2, false 0, maybe-false 0
- findings:
  - `[low]` `[patch]` The 1e-12 number tolerance applies to all fixtures, though only the `vectors` polygon needs it. — Fix: limit it to `vectors`. Everything else must be byte-equal.
  - `[low]` `[patch]` The number regex absorbs the `22` of `%22` inside percent-encoded data URIs, loosening the tolerance about 200x. — Fix: keep `%XX` escapes out of numbers, with a comparator test.

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

## Auto Run Result

- **Summary:** `core-go/html` ports `html_document`/`html_in` and every helper from `html.rs`: the inline flexbox CSS walk, fonts, options, sizes, the tilted wrapper, SVG inlining, paint CSS with the SVG gradient fallback, mesh and shader raster layers, overlays, strokes, shadows and filters, and text CSS. It exports `svg.Effects`/`EffectsOf`, `MeshGradient`, `Pixmap.EncodePNG`, `resolve.Leaf` and `export.Ctx.LaidOut`. Rust gains a test-only `html --offline`.
- **Review:**
  - Patches applied: 2 low, both in the parity comparator (the ulp tolerance applies to `vectors` only, and `%XX` escapes are tokenized separately).
  - Rejected: none.
- **Follow-up review recommended:** false (patched: 0 high, 0 medium, 2 low).
- **Verification:**
  - `go vet` and `go test ./...` pass, and `GOAMD64=v3` on `./html/` passes.
  - Live parity passes on every package. 7 golden exports and 3 live ones (text, shader) match byte for byte, except `vectors` within 1e-12 and PNG URIs by pixels.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - The options are tested by unit tests only, because the CLI uses the defaults.
  - Shader parity depends on the GPU adapter.
  - Five export-job tests are left for 7c.
