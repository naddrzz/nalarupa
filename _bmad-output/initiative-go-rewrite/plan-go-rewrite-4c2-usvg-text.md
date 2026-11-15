---
title: 'Go rewrite phase 4c2: SVG text layout and flattening (usvg port)'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'built'
baseline_revision: '107111f12994b4a38f3a4b629171f22d7b63a8af'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-2-text-shaping.md'
warnings: []
deferred:
  - summary: >-
      Text parity compares f32 bits exactly, while the plan allows 1 ulp where libm or a shaper advance conversion is involved, and the Linux CI job will run the live text parity (rotate, textPath, writing-mode-tb) against glibc trig for the first time.
    evidence: |-
      Unverified: it has only been run on Windows, where it matches bit for bit. 4c1 found Go's correctly rounded trig 1 ulp off glibc on some inputs. Settle by checking the Linux CI job's TestTextParity result. If it fails on 1-ulp coordinates in trig-driven documents, add a per-document ulp allowance for those documents only.
    location: >-
      core-go/usvg/text_parity_test.go
    severity: medium (unverified)
---

<intent-contract>

## Intent

**Problem:** Every text node the editor draws reaches pixels as `<text>` in the `svg.rs` output, which usvg lays out and flattens into glyph paths. The Go usvg port (4c1) still turns `<text>` into an empty placeholder, so no exported or rendered text would appear.

**Approach:** Port usvg 0.47's text support into `core-go/usvg`, replacing the 4c1 placeholder:
- `parser/text.rs`: text chunks, spans, `tspan` positioning, `textPath`, `xml:space`, letter- and word-spacing, decoration, font-family lists
- `tree/text.rs`
- `text/layout.rs`, with the vendored linear code-point patch: bidi, script runs, shaping, font fallback, positioning, decorations, `textPath`
- `text/flatten.rs` (outline glyphs into `Path` groups) and `text/mod.rs`

Fonts, fallback and shaping come from `core-go/text`, which already ports fontdb's database and query, the Noto-first fallback that `Fonts::resolver` hands to usvg, and go-text shaping with the rustybuzz workarounds. Glyph outlines with the `wght` variation come from go-text. The `usvg-tree` oracle learns to load fonts the way the core does.

## Boundaries & Constraints

**Always:**
- Same tree as Rust for the same SVG and fonts: text chunk and span structure, `bounding_box`/`stroke_bounding_box`, positioned glyphs (font, glyph id, cluster, x/y, transforms), and the flattened glyph paths and decoration paths. Coordinates match bit for bit on `f32`, with at most 1 ulp where a libm call or a shaper advance conversion is involved.
- Font selection uses the same resolver the Rust core passes (`text.rs` `Fonts::resolver`: usvg's default font selector, then the memoized Noto-first fallback). It is exposed from `core-go/text` without duplicating its database.
- Bidi reordering and script itemisation give the same runs as `unicode-bidi` 0.3.18 and `unicode-script` 0.5.8 (Unicode 15). Use a Go library only where it matches these. Otherwise port the needed parts with their tables.
- Floating-point products get explicit conversions (no FMA). No cgo.

**Never:**
- No colour or bitmap glyphs (`colr.rs`, sbix/CBDT/SVG glyph tables). Such glyphs flatten to nothing, and the gap is documented.
- No filters (4c3), rasterising (4d) or `writer.rs`.
- No change to `app/`, the FFI, or Rust behaviour apart from the test-only CLI oracle.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| svg.rs text | The 4a live text fixtures' exports (weights, `font-variation-settings`, letter spacing, line height via `tspan` y, alignment, decoration, `xml:space="preserve"`) | Same text nodes, glyph positions and flattened paths as Rust on the same machine | No error expected |
| Fallback | A missing family; mixed Latin + CJK + Arabic + Hebrew + emoji (monochrome Noto Emoji when installed) | Same fonts chosen per cluster and the same paths | No panic |
| Bidi and scripts | An RTL paragraph, mixed-direction runs with numbers, a combining mark sequence | Same run order and glyph positions | No error expected |
| Positioning | `tspan` with x/y/dx/dy lists, `rotate`, `text-anchor`, `baseline-shift`, `textPath` on a curve with `startOffset` | Same glyph transforms | Glyphs past the path end are dropped as Rust drops them |
| Degenerate | Empty text, whitespace only, font-size 0, no fonts at all | Same empty or missing node as Rust | No panic |

</intent-contract>

## Code Map

- `core/vendor/usvg/src/parser/text.rs` (964), `tree/text.rs` (721), `text/layout.rs` (1826; the `Nalarupa:` patch at about line 1807), `text/flatten.rs` (417), `text/mod.rs` (216) -- the port source. `text/colr.rs` is skipped.
- `core-go/usvg/` -- 4c1's port. Find the `Text` placeholder (`<text>` currently gives a node with id, transform and zero bboxes) and the svgtypes `FontFamily`/`parse_font_families` it needs. The tree types and `Options` (font fields kept for this slice) are there.
- `core-go/text/` -- `fontdb.go` (database, query, IDs), `shape.go` and `harfbuzz.go` (shaping with the go-text workarounds), `fonts.go` (`Fonts`, cache and fallback memo). Reuse these and add exported accessors rather than parallel code.
- `core/src/text.rs:124` (`resolver`) and `:412-460` (`cached_fallback`, `pick_fallback`) -- the resolver behaviour the CLI and Go must share.
- `core/src/bin/cli.rs` -- the `usvg-tree` subcommand. Extend it with `--fonts <cache-dir>`, which loads `Fonts::new(cache)` with `online = false` and passes `fontdb` and `resolver()` in `Options` as `render.rs` does. Text nodes should dump their chunks, spans, positioned glyphs and the flattened group.
- `~/.cargo/registry/src/index.crates.io-*/unicode-bidi-0.3.18`, `unicode-script-0.5.8`, `unicode-vo-*` -- the reference behaviour if they have to be ported.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- add `--fonts <dir>` to `usvg-tree` and dump text nodes in full.
- [x] `core-go/text/*.go` -- exported accessors for the font database, the query, the fallback resolver and the shaper that usvg needs.
- [x] `core-go/usvg/text_parse.go`, `text_tree.go` -- the parser and tree types, replacing the placeholder.
- [x] `core-go/usvg/text_layout.go`, `bidi*.go`, `script*.go` (only if no matching Go library exists) -- layout.
- [x] `core-go/usvg/text_flatten.go` -- outline flattening.
- [x] `core-go/usvg/text_test.go` -- the vendored unit tests plus the no-fonts and degenerate rows.
- [x] `core-go/usvg/text_parity_test.go`, `core-go/testdata/usvg/text/*.svg` -- at least 30 documents covering every matrix row, compared live with `usvg-tree --fonts` (skipped without `NALARUPA_CLI`, since text depends on installed fonts). Also rerun the 4c1 corpus documents that contain text.

**Acceptance Criteria:**
- Given only Go, when `go test ./...` runs, then it passes, with live text parity skipped.
- Given the release CLI on the same machine, when `NALARUPA_CLI=… go test ./usvg/` runs, then every text document's tree matches Rust as the matrix states.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- `core-go/text/svgtext.go` exposes what usvg reads: `Database` (the faces of a `Fonts`, via `Fonts.Database()`, or `NewDatabase()` empty), fontdb's query (the matcher in `fontdb.go` now takes any stretch and style), usvg's default fallback (shared with `pickFallback`), `Fonts.Fallback` (the memoized Noto-first fallback), face metrics, `HasChar`, `HasAxis`, rustybuzz-style `Shape` (one bidi run, `smcp`/`kern` features, variations through the go-text workarounds) and outlines. `core-go/usvg/text_layout.go` has `FontResolver`, `DefaultFontResolver` and `FontsResolver(fonts)`, the resolver `text.rs` passes; `Options` gains `FontDB` and `FontResolver`, `Tree.FontDB()` the database glyphs refer to.
- Glyph outlines are ported from ttf-parser 0.25 rather than taken from go-text: `text/outline.go` (glyf, composite glyphs, gvar with inferred deltas, `set_variation` coordinates) and `text/cff.go` (CFF1 charstrings in f32, subroutines, CID, seac). go-text's outlines differ bit-wise: no closing `lineTo` before `close`, an lsb shift, f64 CFF arithmetic and an extra closing line on CFF contours. CFF2 still comes from go-text (with that closing line dropped).
- unicode-bidi 0.3.18 is Unicode 16 and unicode-script 0.5.8 Unicode 17, not 15 as the constraints say; Go's `unicode` is 15 and `golang.org/x/text` is not a dependency, so `bidi.go` ports the crate (byte-indexed, BidiInfo with an LTR paragraph, visual runs of the first paragraph) with tables generated from its `tables.rs` (`bidi_tables.go`), and `script_tables.go`/`vo_tables.go` hold only the ranges layout needs (the 16 scripts without letter spacing, the Upright orientation).
- `kurbo_arclen.go` ports kurbo's cubic arclen/inv_arclen (Gauss-Legendre, ITP solver) with Rust's min/max NaN rules and LLVM's powi order; `stAtan2` (4c1) gives the path normals.
- Arc counts: `refcount.go` counts every fill/stroke a text node holds (chunk spans and their decoration styles, laid-out spans and decoration paths, the flattened group) and deep-clones text, so paint servers on text get the same in-place updates or generated ids as in Rust.
- Color (COLR), SVG and bitmap glyphs are detected through go-text and flattened to nothing (an SVG/bitmap glyph still ends the current outline path, as in Rust); text in a color emoji font therefore differs from Rust. The `emoji-*` corpus documents run only where monochrome Noto Emoji is installed.
- The 4c1 corpus has no text (it was stripped from the exports), so its goldens are unchanged; the placeholder test is replaced by `TestTextWithoutFonts`.
- Beyond the 43 corpus documents and the two `text.pen` exports, a one-off sweep of all 145 installed families (regular and bold italic, Latin/Hebrew/Arabic/Devanagari/Thai/CJK, underline, letter spacing) and of the 8 variable families at weights 100-900 matched the CLI bit for bit, also with `GOAMD64=v3`.

## Plan Change Log

## Review Triage Log

### 2026-11-15 — Review pass
- verdicts: 5 findings — high 0, medium 0, low 4, false 0, maybe-false 1
- findings:
  - `[low]` `[patch]` `Fonts.Fallback` with an empty `used` list panics in `pickFallback`, where Rust returns None. — Fix: return `(0, false)` when `used` is empty, with a test.
  - `[low]` `[reject]` `Database.Metrics` accepts a `post` v2.0 table with a truncated glyph-name index, which ttf-parser drops, so underline metrics differ for such fonts. — Only malformed fonts are affected, and the fix adds table validation.
  - `[low]` `[reject]` A glyph listed in the SVG table is always dropped, while Rust falls back to its outline when the glyph document fails to parse or has no `glyph<id>` node. — This needs fonts with broken SVG glyph tables, and the fix means parsing glyph documents to decide.
  - `[maybe-false]` `[defer]` Exact bit comparison against the plan's 1-ulp allowance, with Linux CI running the live text parity against glibc for the first time. — Deferred: settle by the Linux CI result (frontmatter `deferred`).
  - `[low]` `[patch]` `defaultFallback` looks up the base face inside the loop over all faces (O(N²) per fallback call). — Fix: look the base up once before the loop.

## Verification

**Results (2026-11-15, Windows):** `go vet ./...` and `go test -count=1 ./...` pass (text parity skipped); `GOAMD64=v3 go test ./usvg/ ./text/` passes; with `NALARUPA_CLI` every package passes and `TestTextParity` matches 42 of 43 documents bit for bit (`emoji-mono` skipped: no Noto Emoji) plus both `text.pen` exports; `cargo test --release` passes (292 tests). Not run on Linux.

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./usvg/ ./text/` -- expected: pass
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live text parity run
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

## Auto Run Result

- **Summary:** usvg 0.47's text support is ported into `core-go/usvg`: the text parser and tree, layout with bidi (tables from unicode-bidi, Unicode 16) and scripts (unicode-script, Unicode 17), fallback, positioning, decorations, `textPath`, and outline flattening. It replaces the 4c1 placeholder.
  - `core-go/text` exposes its font database, query, Noto-first fallback, metrics and shaping (`svgtext.go`).
  - `core-go/text` ports ttf-parser's TrueType and variable outlines and its CFF reader (`outline.go`, `cff.go`), because go-text's outlines differ bit-wise. CFF2 still uses go-text.
  - Rust's `usvg-tree` gains `--fonts <dir>`.
- **Files:**
  - `core-go/usvg/text_*.go`, `bidi*.go`, and `script*`/tables: layout, flattening and Unicode tables
  - `core-go/usvg/{convert,convert_paint_server,refcount,tree,options,usvg}.go`: wiring
  - `core-go/text/{svgtext,outline,cff}.go` and `fontdb.go` (any stretch and style): fonts
  - `core-go/testdata/usvg/text/`: 43 documents
  - `core/src/bin/cli.rs`: `--fonts`
- **Review:**
  - Patches applied: 2 low (no panic on an empty `used` in `pickFallback`, and the base face is now looked up once in `defaultFallback`).
  - Deferred: 1 maybe-false (1-ulp risk on Linux CI).
  - Rejected: 2 low (malformed `post` v2.0 tables, broken SVG glyph tables).
- **Follow-up review recommended:** false (patched: 0 high, 0 medium, 2 low).
- **Verification:**
  - `go vet` and `go test` pass, and so does `GOAMD64=v3 go test ./usvg/ ./text/`.
  - Live text parity against the release CLI matches 42 of 43 documents and both svg.rs exports. `emoji-mono` is skipped because Noto Emoji isn't installed here.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - Colour and bitmap emoji glyphs produce nothing (a documented gap).
  - CFF2 fonts may differ by 1 ulp.
  - Linux has not been run.
  - `Database` is not safe for concurrent use.
