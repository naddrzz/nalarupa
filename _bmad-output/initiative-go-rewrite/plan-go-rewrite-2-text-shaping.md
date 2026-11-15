---
title: 'Go rewrite phase 2: offline text shaping'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'built'
baseline_revision: '2809876f223474125e3e5c2a35dedeb96fb2485a'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
context:
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-1-document-model.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** The Go core lays out text through a stub measurer, so every text node's size is wrong until Go can find fonts, shape text and wrap lines as `core/src/text.rs` does.

**Approach:** Add a `core-go/text` package with a `Fonts` type that implements `layout.TextMeasurer` and ports the offline part of `text.rs`: system and cache font discovery, Noto fallback, shaping with the `wght` axis, SkParagraph-style line height and UAX #14 wrapping. Prove it matches Rust through a new Rust CLI `measure` subcommand and live layout parity on text fixtures.

**Decisions:** Shaping, font parsing and line breaking use `github.com/go-text/typesetting`, the only new dependency. Google Fonts downloading (`Ensure`, `Loading`, `Online`, the metadata and download code) is a later phase.

## Boundaries & Constraints

**Always:**
- `Fonts` implements `layout.TextMeasurer` (`core-go/layout/text.go`) unchanged. It also offers `HasFamily`, plus `Downloaded`, which reloads a font file that has appeared in the cache and bumps `Generation`, as `text.rs` does.
- Face selection matches fontdb 0.23: the same system font directories and load order, cache dir loading, `prefer_variable`, the sans-serif pick (Segoe UI, Helvetica, Arial, DejaVu Sans), and the CSS matching query over `[family, "Noto Sans", sans-serif, serif]`.
- Numbers match `text.rs`: unrounded ascent/descent, `line_h = max(1, round(asc+desc+gap))` or `round(lh·size)`, the baseline formulas, letter spacing per cluster including the trailing one, and 0.55 em / 1 em when no face exists.
- Wrapping matches `unicode-linebreak` 0.1.5 (Unicode 15.0) and the `text.rs` wrap rules (the 0.01 tolerance, breaking long words at cluster boundaries, the zero-width rule, hard breaks, hanging trailing spaces, `soft` only on wrapped lines).
- No network access and no cgo.

**Never:**
- No Google Fonts download code.
- No glyph outlines, rendering or usvg font resolver: rendering is a later phase.
- No change to `app/` or the FFI, and no Rust behaviour change apart from adding the CLI subcommand.
- No bundled font files in the repo.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Installed Latin font | Family installed on the machine, with weight, letter spacing and kerning pairs | Metrics, width and wrapped lines equal Rust `measure` within 1e-4 | No error expected |
| Variable cache font | `cache/var/X.ttf` with a `wght` axis, weights 300 and 700 | Different widths per weight, both equal to Rust | No error expected |
| Missing family | Unknown family | Same fallback face and widths as Rust | No error expected |
| Mixed scripts | Latin + CJK + Arabic + emoji | Same fallback faces and widths as Rust for the installed fonts | No panic |
| No faces | Empty font database | 0.55 em per ASCII char, 1 em otherwise | No panic |
| Cache file arrives | A TTF copied into the cache, then `Downloaded` | `Generation` goes up by one, and the family resolves to the new face | No error expected |
| Corrupt font file | Invalid TTF in the cache or system dir | Skipped; other faces still load | No panic |

</frozen-after-approval>

## Code Map

- `core/src/text.rs` -- the port source. Key ranges: `Fonts` fields 74-89, `new` 92-108, `has_family` 132, `downloaded` 166, face lookup 228, `metrics` 245-266, `width` 270, `wrap` 278-327, fallback 331-371 / 429 / 447, `prefer_variable` 376, shaping 402, tests from 540. Skip `ensure`, `loading`, `online` and 466-538 (downloads).
- `core-go/layout/text.go` -- `TextMeasurer`, `TextStyle`, `Metrics`, already ported. `Line` lives in `core-go/resolve`.
- `fontdb-0.23.0/src/lib.rs` in the cargo registry (`~/.cargo/registry/src/index.crates.io-*/`) -- `load_system_fonts` at line 400, the directory order and the `query` matching.
- `core/vendor/usvg/src/text/mod.rs:147` -- `default_fallback_selector`, used by `pick_fallback` when no Noto face fits.
- `unicode-linebreak-0.1.5` in the cargo registry -- the reference line-break behaviour (Unicode 15.0).
- `core/src/bin/cli.rs` -- the `layout` subcommand from phase 1 (`online = false`, temp cache). `measure` goes next to it.
- `core-go/parity_test.go` -- golden or live parity. It writes `.parity-live` so live runs are never cached, and uses `noText` for the text-free fixtures.
- `.github/workflows/ci.yml` -- the Linux job installs `fonts-noto-core`, and both jobs run `go test` with `NALARUPA_CLI`.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- add `measure <cases.json>`. For each case `{text, style, maxW?}` it prints `{metrics, width, lines}` from `Fonts` with `online = false` on a temp cache -- the text parity oracle.
- [x] `core-go/go.mod`, `core-go/go.sum` -- add `github.com/go-text/typesetting` at the latest release.
- [x] `core-go/text/fontdb.go` -- the font database: system directories, load order, cache dir, `prefer_variable`, CSS query.
- [x] `core-go/text/fonts.go` -- `Fonts` and its memo caches (faces, vmetrics, shaped), `Metrics`, `Width`, `HasFamily`, `Downloaded`, `Generation`.
- [x] `core-go/text/shape.go` -- shaping with `wght`, merging glyphs per cluster, the `noto_family` / `pick_fallback` / default-selector fallback.
- [x] `core-go/text/wrap.go` -- the `wrap` algorithm.
- [x] `core-go/text/text_test.go` -- port the `text.rs` tests that don't download, and cover the no-faces, cache-arrival and corrupt-file rows.
- [x] `core-go/text/parity_test.go`, `core-go/testdata/text-cases.json` -- compare with live `measure` output for installed-font, variable (copy an installed variable font into a temp cache), missing-family, mixed-script, line-height and wrap cases. Skipped without `NALARUPA_CLI`.
- [x] `core-go/parity_test.go`, `core-go/testdata/live/text.pen` -- in live mode, lay out text fixtures with the real Go `Fonts` and compare with CLI `layout` output. Golden mode skips `live/`.

**Acceptance Criteria:**
- Given only Go is installed, when `go test ./...` runs in `core-go`, then it passes, and the live-only text parity tests report as skipped.
- Given the release CLI is built on the same machine, when `NALARUPA_CLI=… go test ./...` runs, then every `measure` case and every live text fixture matches Rust.
- Given the Rust core, when `cargo test --release` runs, then it still passes.
- Given both CI jobs, when they run, then the text parity tests run live (not skipped) against the CLI built in that job.

## Implementation Notes

- Line breaking is a port of unicode-linebreak 0.1.5 (its state machine plus `linebreak_tables.go`, generated from the crate's `tables.rs`), not go-text's segmenter: go-text implements Unicode 17 rules (LB15a-d, LB19a, LB20a, LB28a) and breaks differently around quotes in CJK text and word-initial hyphens ("a -b"), which the Always rule "matches unicode-linebreak 0.1.5" excludes. go-text is still the only new module; `golang.org/x/image` comes with it as an indirect requirement.
- Font metadata, table directory, `name`/`OS/2`/`post`, `cmap` lookups, `wght` coordinates (fvar + avar, truncating like ttf-parser) and the MVAR-adjusted vertical metrics are read in `sfnt.go` with ttf-parser 0.25's rules; go-text parses faces for shaping only. Face files are read by range when the database is built and loaded whole only when a face is shaped.
- `fetch`/`ensure`/`load_cached` are ported without the download branch: with `online = false` Rust still loads a cache file that appears for a style on first use, so Go does too.
- `measure <cases.json> [<font-cache>]`: without the second argument it uses the `layout` subcommand's cache folder. A missing `maxW` wraps at an infinite width. The live layout test lays out with `text.New(<temp>/nalarupa-cli-fonts)`, the folder the CLI's `layout` uses.
- go-text v0.3.5 shapes differently from rustybuzz 0.20 in five places, each found by sweeping every installed family against `measure`; `harfbuzz.go` works around them: lookup digests that drop coverage ranges (Calibri loses class kerning), a feature variation that empties every feature it does not replace (Cascadia Code's ccmp at heavy weights), a whole GPOS/GSUB discarded over one inconsistent subtable (Linux Libertine Display), pair class 0 handled as "try the next subtable" (Linux Libertine bold italic), and pair-set devices that ttf-parser never finds (Segoe UI Variable kerning deltas). After them, a sweep of all 146 installed families × 4 styles matched Rust for Latin, Arabic, Hebrew, Devanagari and CJK first strings and every installed variable font at weights 100-900; Thai-first text in a font with a legacy `kern` table still differs (deferred).
- `.parity-live` is also written in `core-go/text`; `.gitignore` covers `core-go/**/.parity-live`.

## Plan Change Log

## Review Triage Log

Pass 1 (lens: quick). Counts: high 0, medium 1, low 3, false 0, maybe-false 0.

| # | Finding | Verdict | Route | Evidence |
|---|---------|---------|-------|----------|
| 1 | `harfbuzz.go` `lenientLayouts`/`classZero`: a PairPos format 2 class at or above the declared count gets a misaligned record, where ttf-parser returns `None` | low | reject | Real, but only on the lenient path, which runs only for fonts go-text rejected as inconsistent, and only when a font uses more classes than it declares (the Linux Libertine case is the opposite). The fix adds a bounds guard. |
| 2 | `TestParityVariableFont` skips on Linux CI (static Noto), and a skip is invisible without `-v`, so acceptance criterion 4 is unmet | medium | patch | The Linux job installs only `fonts-noto-core` and emoji, and the test skips without an installed variable face. Fix: CI supplies a pinned OFL variable TTF through an env var, and the test fails rather than skips under `CI`. No public surface. |
| 3 | `fontconfig.go`: Rust stops reading a config file at its first parse error, Go keeps the later dirs and aliases | low | reject | Documented in the file header. It only matters on machines with a malformed fontconfig file, and the fix means replicating fontconfig-parser's failure semantics. |
| 4 | `text_test.go` `systemFace`: may pick a face from a `.ttc`, so `TestCorruptFontSkipped` fails where a collection sorts first | low | patch | `systemFace` accepts `index == 0` faces from any file, and `loadSource` loads every face of the copied collection. Fix: restrict to single-face files. |

## Design Notes

Text parity is live-only on purpose: the result depends on the fonts installed on a machine, and Rust and Go running on the same machine see the same fonts. That means CI (Noto on Linux, Segoe and Arial on Windows) checks parity without committing font files or machine-specific goldens. A variable-font case needs a variable font that is already installed (for example Segoe UI Variable on Windows). Where none exists, that case is skipped with a message naming the missing font.

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./...` -- expected: all pass, with text parity skipped
- `cd core && cargo test --release` -- expected: all pass (separate `CARGO_TARGET_DIR`)
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test ./...` -- expected: all pass, with text parity run
