---
title: 'Go rewrite phase 1: document model in core-go'
type: 'refactor'
ticket: ''
created: '2026-11-14'
status: 'built'
baseline_revision: 'a5cf98a82b3ac752ba38a4d4eb7c83004acc17b8'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
context:
  - '{project-root}/core/guide/schema.md'
  - '{project-root}/GLOSSARY.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** The core is being rewritten from Rust to Go in phases. Every later phase (renderer, script sandbox, importers, engine/FFI, agent) builds on the document model, which so far exists only in Rust.

**Approach:** Create a Go module in a new `core-go/` folder that ports the document model: load/save/edit of `.pen` JSON (`doc.rs`), resolution of components, variables and migration (`resolve.rs`), flex layout (`layout.rs`) and patch history (`history.rs`). Prove it matches Rust with ported unit tests and a parity test against a new Rust CLI subcommand that dumps the laid-out tree.

## Boundaries & Constraints

**Always:**
- Go 1.25, module path `github.com/naddrzz/nalarupa/core-go`, stdlib only. Ordered JSON is built on `encoding/json` `Decoder.Token` with `UseNumber`.
- Object key order survives load → edit → save, as with serde_json `preserve_order`.
- Numbers keep their source text on round trip (`10.0` stays `10.0`). `save` writes 2-space-indented JSON with `": "`, no HTML escaping, and U+2028/U+2029 unescaped, through an atomic temp-file rename, and strips a BOM on load.
- The id generator is the same xorshift (13/7/17) with 5 base-62 characters, so a seeded run yields the same ids as Rust `reseed`.
- Layout takes text measurement through an injected interface (`Metrics`, `Width`, `Wrap`). The pure parts of `text.rs` (`TextStyle::of`, `weight_num`, the `Line` struct) are ported.
- `history` treats `10` and `10.0` as equal, as `engine::same` does.

**Never:**
- No change to `app/`, the FFI or any Rust behaviour other than adding the CLI subcommand.
- No port of `pen.rs` (the AI Pen, which belongs with the agent phase), `text.rs` shaping or font loading, rendering, scripting, HTML, Figma or engine code.
- No third-party Go dependency, and no cgo.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Round trip | Template or fixture `.pen` loaded and saved unchanged | Byte-identical to Rust `save` of the same input | No error expected |
| BOM | File starting with U+FEFF | Loads as without the BOM | No error expected |
| Malformed JSON | Truncated file | `Load` returns an error with the parse position, and the doc is unchanged | Error returned, no panic |
| Old version | Document whose `version` is below 2.19 | `migrate` output equals Rust's | No error expected |
| Missing ref | Instance whose `ref` has no component | Resolved node carries the same `problem` text as Rust | No panic |
| Ref cycle | Component A refs B, B refs A | Resolution stops the same way Rust does | No panic or stack overflow |
| Undo | `diff(a,b)` applied to `a` | Equals `b`, including key order | No error expected |

</frozen-after-approval>

## Code Map

- `core/src/doc.rs` -- `Doc`, `FORMAT_VERSION` "2.19", `ROOT`, tree edits, `find_in`, `walk`, `reseed`/`random_u64`, id generation at lines 776-800, 10 tests from line 857.
- `core/src/resolve.rs` -- `RNode`, `Resolver`, `num`, `along`, `migrate`, 10 tests from line 466. Uses `doc::walk`/`find_in` (lines 99, 105, 130).
- `core/src/layout.rs` -- five-pass flex layout, `padding`, `local_aabb`, `hugs`, `hugs_fill`, `note_style`, 8 tests from line 345. Text calls are in `text_bounds` (272-293) and `place` (318-322). The `text_in_layout` test needs real fonts: port it with a stub measurer.
- `core/src/history.rs` -- `Patch` (Set/Obj/Arr), `Mid`, `diff`, `apply`, 4 tests from line 129, including `restores_key_order`.
- `core/src/engine.rs:1350-1370` -- `same`, numeric equality used by history.
- `core/src/text.rs` -- `Line`, `TextStyle::of` and `weight_num` are ported. Shaping and the wrap algorithm (278-328) are left for later.
- `core/src/bin/cli.rs` -- subcommand dispatch, where `layout` is added.
- `core/templates/{blank,mobile,slides}.json` -- sample documents. `slides.json` has text nodes.
- `.github/workflows/ci.yml` -- `checks` job (lines 23-30, which installs fonts and runs cargo test) and `windows` job (step `id: core`, around lines 61-63).

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- add `layout <doc.pen>`, which opens, resolves and lays out the document and prints every top-level node's `RNode` tree as JSON (id, type, x, y, w, h, problem, children) -- this is the parity oracle.
- [x] `core-go/go.mod`, `core-go/jsonv/` -- ordered JSON value type: decode, encode, serde-compatible pretty print -- a Go map loses key order.
- [x] `core-go/doc/` -- port `doc.rs` with its 10 tests -- base of every edit.
- [x] `core-go/resolve/` -- port `resolve.rs`, `migrate` and the `text.Line` struct with its 10 tests -- produces the render tree.
- [x] `core-go/layout/` -- port `layout.rs` and the pure `TextStyle` parts, plus the `TextMeasurer` interface, with its 8 tests (text tests use a fixed-advance stub) -- box geometry.
- [x] `core-go/history/` -- port `history.rs` and `same` with its 4 tests -- undo.
- [x] `core-go/testdata/` -- text-free fixtures covering components/ref, a missing ref, a ref cycle, variables with two themes, a disabled node, fit/fill/gap/padding and a pre-2.19 document, plus `blank` and `mobile`, with golden `*.layout.json` and `*.saved.json` files generated by the Rust CLI.
- [x] `core-go/parity_test.go` -- compare Go layout and save output with the goldens. When `NALARUPA_CLI` is set, compare with live CLI output instead -- catches drift whenever Rust changes.
- [x] `.github/workflows/ci.yml` -- add `actions/setup-go` and `go vet ./... && go test ./...` in `core-go` to both jobs, with `NALARUPA_CLI` pointing at the freshly built CLI.

**Acceptance Criteria:**
- Given a clean checkout with only Go installed, when `go test ./...` runs in `core-go`, then every ported test and the golden parity tests pass.
- Given the release CLI is built, when the parity test runs with `NALARUPA_CLI` set, then Go and Rust output match for every fixture.
- Given the Rust core, when `cargo test --release` runs, then it still passes, and the existing CLI subcommands behave as before.
- Given the same `reseed` seed, when Go and Rust each generate 100 ids, then the sequences are identical.

## Implementation Notes

- CLI: `layout <doc.pen> [<saved.pen>]`. It opens like `Engine::open` (load, `migrate`, `ensure_ids`) without imports, recents or the data folder, so it has no side effects; the optional second argument saves the opened document with `Doc::save` and produces the `*.saved.json` goldens.
- Numbers keep their source text. serde_json without `arbitrary_precision` rewrites non-canonical spellings (`1.50` → `1.5`, `1e2` → `100.0`), so a byte-identical save holds only for canonically written numbers (all fixtures and templates are). Numbers the Go code creates (`migrate`'s flipped `sweepAngle`) are written like serde/ryu.
- serde_json's `Map::remove` under `preserve_order` is `swap_remove`; `jsonv.Object.Remove` does the same, which the goldens confirm (key order after `migrate` and `null` merges).
- `jsonv.Parse` keeps serde_json's 127-level nesting limit.
- `testdata/ids.json` came from a throwaway Rust example calling `doc::reseed` and `Doc::gen_id` (the CLI has no id command); it is a static golden.
- A component holding an instance of itself (expands 64 levels deep) matched Rust in a live run but its golden is ~400 KB, so it is a Go unit test instead of a fixture; `ref-cycle.pen` keeps the A↔B, self and P↔Q cycles.
- `testdata/.gitattributes` turns off line-ending conversion: Windows checkouts (`core.autocrlf`) would otherwise break the byte comparison.

## Plan Change Log

## Review Triage Log

Pass 1 (lens: quick). Counts: high 0, medium 1, low 3, false 1, maybe-false 0.

| # | Finding | Verdict | Route | Evidence |
|---|---------|---------|-------|----------|
| 1 | `parity_test.go`: test cache hides a rebuilt CLI on local runs | low | patch | The reviewer reproduced it: a replaced stand-in CLI still gave `ok (cached)`, and exec is not tracked by the test cache. CI is unaffected (fresh cache). Fix: make the cache track the CLI file. |
| 2 | `doc/random.go`: generator is process-wide, but Rust's is thread-local | medium | defer | The Go generator is mutex-guarded, so there is no data race. Determinism breaks only if another goroutine draws between `Reseed` and `GenID`, and no phase 1 code does that. The callers that depend on per-thread seeding (`script.rs:689-701,792`, agent, providers) are excluded by the frozen Never list. |
| 3 | `jsonv/codec.go`: accepts lone surrogate escapes and `1e400`, which serde rejects | low | reject | True for `encoding/json`, but Rust never writes such files (Rust strings cannot hold lone surrogates, and serde never emits out-of-range numbers), so only hand-edited or foreign files hit it. The fix adds a raw-token scan guard. |
| 4 | `testdata/ids.json`: static snapshot, not reproducible from the repo | low | patch | It came from a throwaway Rust program that is not committed, and `TestParityIDs` ignores the CLI. Fix: a Rust unit test that asserts the same snapshot, so a Rust change fails `cargo test`. |
| 5 | Acceptance criteria 2 and 3 not verified in the review | false | reject | This is a coverage note, not a defect. In step 3, live parity passed against a release CLI built in the scratch target (all 9 fixtures plus ids). `cargo test` is re-run in final verification. |

## Design Notes

Text-free parity fixtures avoid comparing font-dependent widths: Rust measures with system fonts, while Go uses a stub until the text-shaping phase. Layout of text nodes is covered by the ported unit tests with a stub measurer whose results can be checked by hand (for example, every glyph 10 px wide with a 20 px line height).

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test ./...` -- expected: all pass
- `cd core && cargo test --release` -- expected: all pass (set `CARGO_TARGET_DIR` to a separate folder if the app is running)
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -run Parity ./...` -- expected: pass
