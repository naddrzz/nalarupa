---
title: 'Go rewrite phase 4f: PDF export'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'built'
baseline_revision: '1908c5c65ac2aa2ab38cec1bc230e4a7a8e0735d'
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
warnings: []
deferred:
  - summary: >-
      CFF subsets are rebuilt from the outlines without hints; Rust's subsetter keeps the font's charstrings, hints included.
    evidence: |-
      testdata/usvg/text/export-cff.svg (Miriam Libre): Go's and Rust's PDFs rasterised by Windows.Data.Pdf differ on 0.34% of the pixels by more than 8 levels (mean 0.19); fixtures.json states delta 8, maxShare 0.005, meanDelta 0.25. Exact parity needs a CFF subset that copies charstrings, global and local subroutines and the Private DICTs (SID-keyed fonts rewritten as CID-keyed).
    location: >-
      core-go/pdf/subset.go (outlineCFF)
    severity: low
  - summary: >-
      Color glyphs are not drawn in color. A face without outlines (bitmap-only, such as CBDT Noto Color Emoji) becomes a Type 3 font whose glyphs keep their text but draw nothing; a COLR/SVG glyph of a face with outlines is embedded as a plain CID glyph. krilla draws both as Type 3 color glyphs. A Type 3 font holds at most 256 glyphs here (krilla opens another).
    evidence: |-
      core-go/usvg's flattening leaves such glyphs out (4c2), so there is no Go color glyph renderer to draw from. The text stays extractable (TestOutlinelessFaceIsType3). No parity fixture has one (testdata/usvg/text/emoji-mono.svg matches Rust exactly).
    location: >-
      core-go/pdf/svg.go (text), core-go/pdf/font.go
    severity: low
  - summary: >-
      The parity tolerances against the PNG export are measured on Windows (Windows.Data.Pdf) only; the pdftoppm bound used on Linux CI is an estimate.
    evidence: |-
      Linux was not run. Go-vs-Rust PDF rasters are exact on Windows for every case but export-cff and export-variable, so they should hold with pdftoppm too; the "png.pdftoppm" entry (8% beyond 32, mean 6) is set from the Windows outliers.
    location: >-
      core-go/testdata/pdf/fixtures.json
    severity: low
  - summary: >-
      The exact Go-vs-Rust PDF raster checks ("pdf" entry, delta 0) and the export-cff/export-variable bounds become hard failures on Linux CI with pdftoppm/FreeType, but were only measured with Windows.Data.Pdf.
    evidence: |-
      Unverified. Poppler hints and rounds glyphs through FreeType, which may differ from the Windows renderer for the same PDF. Settle by the Linux CI result. If it fails, add a "pdf.pdftoppm" tolerance entry measured there.
    location: >-
      core-go/testdata/pdf/fixtures.json, .github/workflows/ci.yml
    severity: medium (unverified)
---

<intent-contract>

## Intent

**Problem:** PDF export (`render.rs` `pdf_trees` and `encode_pdf`, used by `html.rs`'s export) goes through krilla and krilla-svg. The Go core has no PDF writer, so it cannot export PDFs.

**Approach:** Write a PDF writer in a new `core-go/pdf` package that draws `core-go/usvg` trees the way krilla-svg does, one page per tree with 1 px = 1 pt:
- paths with fill and stroke (rules, dashes, caps, joins, miter)
- linear and radial gradients as shadings, with their spread modes
- patterns as tiling patterns, and raster images as image XObjects with alpha as an SMask
- clip paths as clipping paths, masks as soft masks, and group opacity and blend modes through ExtGState
- text as real text in embedded, subset TrueType/CFF fonts with ToUnicode maps, so it stays selectable and searchable
- filters rasterised with `core-go/resvg` into images, as krilla-svg does

Port `pdf_trees` (`Assets.verify` around the tree build, skipping damaged images) and `encode_pdf` into `core-go/export`. Prove it with structure checks, text extraction, and raster comparison against Rust's PDFs.

## Boundaries & Constraints

**Always:**
- Output is a valid PDF 1.7 file (xref, trailer, objects with correct byte offsets, compressed streams with Go's `compress/zlib`). Page size equals the tree size in points, with one page per tree in the given order.
- What krilla-svg draws as vector stays vector (paths, gradients, clips, masks, text). What it rasterises (filters, and anything it falls back on) is rasterised here too, at the same resolution rule (read krilla-svg's source). Raster images keep their pixels: PNG/GIF/WebP losslessly through Flate, JPEG passed through as DCT.
- Text: each glyph is drawn from the subset font at the position usvg's layout gave it. The ToUnicode maps clusters back to their source text, so extracted text equals Rust's PDF text for the same tree.
- Visual parity: a Go PDF and Rust's PDF of the same tree rasterised by the same PDF rasterizer must match within a stated per-fixture tolerance. They must also match `core-go/export`'s PNG of that node within a looser stated tolerance.
- Errors match Rust: an empty page (`empty page`), and a damaged image left out instead of failing the document.
- Floating-point products get explicit conversions (no FMA). No cgo, and no new Go dependencies.

**Never:**
- No PDF/A, encryption, forms, annotations, outlines or metadata beyond krilla's defaults (Producer, CreationDate only if krilla writes them).
- No byte-identical match with krilla's file. The structure is our own.
- No change to `app/`, the FFI, or Rust behaviour apart from a test-only CLI subcommand.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Shapes and paint | The 4a/4d fixtures' nodes (solid, linear, radial, pattern, image fills; strokes; opacity; blend modes) | Valid PDF; rasterised it matches Rust's PDF within tolerance | No error expected |
| Clips and masks | Nested clips, luminance and alpha masks | Matches within tolerance | No error expected |
| Text | The 4c2 live text corpus nodes (weights, variable fonts, CFF and TrueType, fallback scripts, decorations) | Extracted text equals Rust's PDF text, and the raster matches within tolerance | No error expected |
| Filters | The effects fixtures (shadows, blur, background blur) and the filter corpus | Filtered content rasterised, matching within tolerance | No error expected |
| Multi-page | Three nodes of different sizes in one call | Three pages with the right sizes, in order | No error expected |
| Damaged image | A node with a corrupt PNG image fill | PDF without that image, other content intact | No failure of the whole document |
| Empty | A zero-size tree | The `empty page` error | Error returned |

</intent-contract>

## Code Map

- `core/src/render.rs:331-356` -- `pdf_trees` (with `assets.verify` and `catch_unwind`) and `encode_pdf`. `core/src/html.rs:137` and `:226` are the callers (the HTML/PDF export phase will wire Go's equivalents).
- `~/.cargo/registry/src/index.crates.io-*/krilla-svg-0.8.1/src/` (1.2k lines) -- the reference for how each usvg node maps to PDF content: what is vector, what is rasterised and at what scale, how masks, clips and text are handled. Mirror its decisions.
- `~/.cargo/registry/src/index.crates.io-*/krilla-0.8.2/src/` (24k lines) -- a reference only, for font subsetting and ToUnicode, shading construction and image encoding choices. Don't port it wholesale. Write the subset this project needs.
- `core-go/usvg` (trees, text spans and positioned glyphs from 4c2), `core-go/text` (font data and outline readers for subsetting: TrueType `glyf`/`loca`, CFF), `core-go/resvg` (rasterising filters), `core-go/export` (`Ctx`, node trees, `Assets.verify`) -- reuse all of these.
- `core/src/bin/cli.rs` -- add `pdf <doc> <out.pdf> <id>...`, which calls `pdf_trees` then `encode_pdf`.
- Rasterizers for tests:
  - On Windows, the built-in `Windows.Data.Pdf` API through PowerShell (`PdfDocument.LoadFromFileAsync`, `page.RenderToStreamAsync` to PNG). No install is needed.
  - On Linux CI, `pdftoppm` from `poppler-utils` (add it to the ci.yml apt line).
  - Text extraction uses `pdftotext`, which is on PATH here as Xpdf 4.00 and is part of poppler on Linux.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- add the test-only `pdf` subcommand.
- [x] `core-go/pdf/writer.go` -- objects, streams, xref and trailer.
- [x] `core-go/pdf/content.go` -- content streams: paths, paint, state, clips.
- [x] `core-go/pdf/shading.go` -- gradients.
- [x] `core-go/pdf/image.go` -- images and SMask.
- [x] `core-go/pdf/mask.go` -- soft masks and transparency groups.
- [x] `core-go/pdf/font.go`, `subset.go`, `tounicode.go` -- text.
- [x] `core-go/pdf/svg.go` -- the usvg tree walk mirroring krilla-svg, including rasterised filters.
- [x] `core-go/export/pdf.go` -- `PDFTrees` and `EncodePDF`.
- [x] `core-go/pdf/*_test.go` -- structure tests: parse back the xref, check offsets and object counts, and that streams inflate.
- [x] `core-go/pdf/parity_test.go`, `core-go/testdata/pdf/fixtures.json` -- every matrix row: text extraction equality and raster comparison through the available rasterizer (skipped with a message when none is available, failing under `CI`).
- [x] `.github/workflows/ci.yml` -- add `poppler-utils` to the Linux job's apt install.

**Acceptance Criteria:**
- Given only Go, when `go test ./...` runs, then the structure tests pass, and parity tests that need the CLI or a rasterizer are skipped with a message.
- Given the release CLI on Windows, when `NALARUPA_CLI=… go test ./pdf/` runs, then every fixture's text extraction equals Rust's and its raster matches within the stated tolerance.
- Given a Go-written PDF, when it is opened by the Windows PDF API and by `pdftotext`, then both read it without error.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- Layout: `pdf/writer.go` (objects, streams, xref, number formatting), `content.go` (krilla's ContentBuilder and Surface: the simulated graphics state, `apply_isolated_op`, sub-streams for opacity, isolation and masks, bounding boxes), `shading.go` (axial, radial and PostScript function shadings, stitching and exponential functions, shading soft masks for stop opacities), `mask.go` (form XObjects, transparency groups, soft masks, tiling patterns, ExtGState dedup), `image.go`, `font.go` (font instances, CIDs, the single-glyph text spanner, Type 0/CIDFont/descriptor writing), `subset.go`, `tounicode.go`, and `svg.go` (the krilla-svg tree walk and `Encode`). `export/pdf.go` holds `PDFTrees` and `EncodePDF`.
- The walk mirrors krilla-svg 0.8 with its default settings (text embedded, filter scale 4): clip paths are native when simple (one even-odd shape at most) and alpha soft masks otherwise; radial gradients get no focal radius and ignore their spread method, as in krilla-svg; each glyph is drawn on its own with the paint countered by the glyph's inverse transform; paint order stroke-then-fill outlines the stroke.
- Fonts: one Type 0 font per face and variation set (krilla-svg's `resolve_variations`, opsz included). CIDs are assigned in first-use order and equal the subset's glyph ids. TrueType at its default location copies the glyf entries (component closure, component ids remapped, cvt/fpgm/prep kept); at other coordinates the varied outlines become simple glyphs rounded with `floor(x + 0.5)`, implied on-curve midpoints dropped, advance `round(advance)`, LSB = xMin. CFF and CFF2 become a CID-keyed CFF (identity charset, one FD) whose charstrings draw the varied outlines at a 1000-unit em; that loses the hints (deferred). `/W` holds the advances at the variation coordinates (`text.Database.Advance`).
- `text/embed.go` adds what embedding reads from the font database: `FaceData`, `Axes`, `Coords`, `Advance`, `UnitsPerEm`. `resvg.DecodeRGBA` exposes the GIF and WebP decoding resvg uses, so PDF images carry the same pixels.
- Images are decoded when drawn, and one that does not decode is left out; `PDFTrees` additionally verifies image data through `Assets.Verify`, as Rust does. PNG keeps 16-bit samples; alpha becomes an SMask only when not all opaque.
- Tests: `pdf/pdf_test.go` has `ParseStructure` (xref offsets, object count, `/Length` and endstream, every Flate stream inflating, page boxes in order) and unit tests (a paint-heavy SVG, three pages, the empty page, a damaged data-URL image, number formatting, ToUnicode, the CFF read back by go-text's parser, simple glyph encoding, sfnt checksums). `export/pdf_test.go` checks that `PDFTrees` leaves out the damaged images of `testdata/pdf/damaged.pen` and resets verification. `pdf/parity_test.go` runs 109 cases: the document nodes of the 4a/4d fixtures (absolute paths, so image fills resolve), a three-page case, the damaged-image document, and the render/svg, render/images, usvg/filter and usvg/text corpora.
- Measured on Windows (Windows.Data.Pdf at 2 px per point, the display scale factor measured and divided out): pdftotext extracts the same text from every Go and Rust file; Go's and Rust's rasters are identical except export-cff (hints) and export-variable (rounding of instanced outlines). Against the PNG export the differences are the renderer's anti-aliasing, glyph rendering and image smoothing (at most 1.18% beyond 32), plus outliers stated per case, chiefly render/svg/paint, whose repeating and reflecting gradients (PostScript function shadings) Windows.Data.Pdf does not paint, in Rust's PDF either.

## Plan Change Log

- 2026-11-15: the CLI also gets a test-only `pdf-svg <file.svg> <out.pdf> [--fonts dir]` subcommand (`encode_pdf` of an SVG file's tree, with the `resvg` subcommand's options, which now share one `options` function). The text corpus (variable fonts, CFF, fallback scripts) and the clip, mask and filter corpora are SVG files, and `svg/live/text.pen` uses only Arial and Segoe UI, so Rust's PDFs of the SVG corpora need it.
- 2026-11-15: a missing pdftotext skips only the text comparison; a missing rasterizer or pdftotext fails the test under CI on Linux only, since only the Linux job installs poppler-utils (the Windows job always has Windows.Data.Pdf, and no pdftotext).

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 7 findings — high 0, medium 1, low 5, false 0, maybe-false 1
- findings:
  - `[low]` `[patch]` Zero trees give a zero-page PDF, where krilla's `finish` adds a default empty A4 page. — Fix: write one empty 595×842 page, and the test asserts it.
  - `[medium]` `[patch]` Glyphs from faces without outlines (bitmap-only fonts such as Noto Color Emoji, installed on Linux CI) are skipped, so their text and ToUnicode entries vanish, where krilla uses Type 3 fonts. — Fix: route them through a Type 3 font that keeps the text, with a test. The fix is local and adds no public surface, so it is patched rather than re-planned.
  - `[maybe-false]` `[defer]` The exact Go-vs-Rust PDF raster checks become hard failures on Linux CI, but were measured on Windows only. — Deferred, unverified medium (frontmatter): settle by the Linux CI result.
  - `[low]` `[patch]` `text.UnitsPerEm` is dead code. — Deleted.
  - `[low]` `[patch]` The unused test type `segPen`, with a stale comment. — Deleted.
  - `[low]` `[patch]` The `ParseStructure` test helper panics on an out-of-range xref offset. — Fix: bounds check and an error naming the object.
  - `[low]` `[patch]` Stray empty file `8)` in the repo root. — Deleted.

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./pdf/` -- expected: pass
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./pdf/ ./export/` -- expected: pass, with raster and text parity run through Windows.Data.Pdf and pdftotext
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-15, Windows, CLI at `%TEMP%/nalarupa-go-target`):** `go vet ./...` and `go test -count=1 ./...` pass, and so does `GOAMD64=v3 go test ./pdf/`. Live, `NALARUPA_CLI=... go test -count=1 ./pdf/ ./export/` passes (also with `GOAMD64=v3`): 109 PDF parity cases through Windows.Data.Pdf and pdftotext (Xpdf 4.00), with `usvg/filter/convolve-matrix.svg` skipped because the Rust CLI crashes on it (0xc0000409). `cargo test --release` passes (292 tests). Not run on Linux.

## Auto Run Result

- **Summary:** `core-go/pdf` is a PDF writer that draws usvg trees the way krilla-svg does, one page per tree at 1 px = 1 pt:
  - vector paths, gradient shadings, tiling patterns, clips, soft masks, and ExtGState opacity and blend
  - images as XObjects (JPEG passed through, the rest Flate, alpha as SMask), with damaged images left out
  - text in subset TrueType/CFF/CFF2 fonts with ToUnicode, and outline-less faces as Type 3 fonts that keep their text
  - filters rasterised at krilla-svg's 4× rule, and an empty A4 page when there are no trees

  `core-go/export/pdf.go` adds `PDFTrees` and `EncodePDF`. Rust gains test-only `pdf` and `pdf-svg` subcommands, and CI installs `poppler-utils`.
- **Files:**
  - `core-go/pdf/*.go`: the writer, fonts, subsetting, ToUnicode, shading, images, masks and the tree walk
  - `core-go/export/pdf.go`: export
  - `core-go/text/embed.go` and `svgtext.go` (`LoadFontData`): font access
  - `core-go/resvg/image.go` (`DecodeRGBA`): image pixels
  - `core-go/testdata/pdf/fixtures.json`: cases and tolerances
  - `core/src/bin/cli.rs`: the oracles
  - `.github/workflows/ci.yml`: poppler
- **Review:**
  - Patches applied: 6 (1 medium, 5 low). These add the Type 3 path for outline-less faces, the empty-document A4 page and a `ParseStructure` bounds check, and remove dead `UnitsPerEm`, unused `segPen` and a stray file.
  - Deferred: 4. Three came from the implementation (CFF hints, colour glyphs monochrome, the pdftoppm PNG bound estimate). One came from review (exact checks unverified on Linux).
- **Follow-up review recommended:** false (patched: 0 high, 1 medium, 5 low).
- **Verification:**
  - `go vet` and `go test` pass, and so does `GOAMD64=v3 go test ./pdf/`.
  - Live, 109 PDF parity cases pass against the release CLI through Windows.Data.Pdf and pdftotext. The export, text and usvg suites pass live after the patches.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - Linux CI tolerances are unverified.
  - CFF text loses hints, within its stated tolerance.
  - Colour emoji are monochrome or blank, with their text kept.
  - A Type 3 font holds at most 256 glyphs.
  - Windows.Data.Pdf doesn't paint repeat/reflect gradients, so that case has a wide tolerance.
  - The Rust CLI crashes on `convolve-matrix.svg`, so that case is skipped.
