---
title: 'Go rewrite phase 6a: shader transpile, SDF and backdrop plumbing'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: '41974622007a1ba9d35dc4cdb0c784907bcaa366'
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

**Problem:** Shader fills (`core/src/shader.rs`) rewrite a WebGL fragment shader into GLSL 450 with a uniform block and samplers, build an SDF texture of the node's outline, and render on the GPU, with an optional `@backdrop` of what was painted beneath. The Go core has none of the CPU half, and its `svg.ShaderPainter` interface cannot express a backdrop. So no GPU slice can be built on top yet.

**Approach:** Port the CPU half of `shader.rs` to a new `core-go/shader` package:
- the uniform annotation parser (`notes`, the `@color @resolution @time @mouse @sdf @backdrop @min/@max/@range @label @default` directives) and `kind` validation
- `transpile` to GLSL 450, with its renames, std140 `NrUniforms` block, sampler pairs and wrapper `main`
- uniform packing (std140 offsets and block size, values from the node's `uniforms`), `f16`
- the outline SDF (analytic rect and ellipse; path coverage, jump flood, blur and Scharr gradient)
- every transpile/kind error string

Extend `svg.ShaderPainter` with the backdrop key and callback and a `UsesBackdrop` check, and port the backdrop SVG build in `svg.rs:1595-1640`, rendered with `core-go/resvg`. GPU drawing is slice 6b. A test-only Rust CLI oracle exposes transpile output and SDF data.

## Boundaries & Constraints

**Always:**
- `transpile` output text, the uniform layout (names, kinds, std140 offsets, block size) and the error strings are byte-identical to Rust's for the same source. naga's parse errors (`line {N-2}: {kind}`) belong to 6b's compile step and are out of scope here. Line numbers are preserved, as Rust blanks comments to their length.
- SDF output matches Rust's f32 values (and the f16 encoding) within 1 ulp per texel where libm or blur rounding is involved, else exactly, for rect, ellipse and path outlines at several sizes, including the 1024 px cap.
- `ShaderPainter` gains, without breaking 4a's error-hatch path:
  - a backdrop key plus a `func(w, h int) (*skia.Pixmap, bool)` callback
  - `UsesBackdrop(src) bool`

  `core-go/svg`'s shader emission then builds the `@backdrop` SVG (`out`/`defs` so far over `#f6f6f6`, the open-group closing) exactly as `svg.rs` does and hands the painter a callback rendering it with `core-go/resvg`.
- No cgo in this slice, and no new dependencies.

**Never:**
- No GPU device, naga compile, draw, readback, PNG cache, or program cache (6b).
- No change to `app/`, the FFI, or Rust behaviour apart from test-only CLI subcommands.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Transpile | Shaders using `gl_FragColor`, a single `out vec4`, `texture2D`, reserved-word identifiers, precision qualifiers, bool/bvec and sampler uniforms, every annotation directive | Same GLSL 450 text and layout JSON as the Rust oracle | No error expected |
| Annotation errors | An unknown `@x`, a bad `@range`, `@sdf` on a non-sampler, an unsupported uniform type | Same error string | Error returned |
| Structure errors | No `main`, no color output, two `out vec4`, use of `gl_PointCoord` or `varying` | Same error string | Error returned |
| Uniform packing | Node `uniforms` with floats, vec2-4, colors, ints, bools, out-of-range values clamped by `@min`/`@max` | Same std140 bytes | Clamped as Rust |
| SDF | `RRect` with per-corner radii, `Ellipse`, an even-odd `Path` with a hole, at 50, 300 and 1500 px | Same distance and gradient texels (and f16 bytes) | No error expected |
| Backdrop SVG | A shader fill using `@backdrop` inside a frame with earlier siblings | Same backdrop SVG string as Rust (captured through the CLI) | No error expected |

</intent-contract>

## Code Map

- `core/src/shader.rs` -- the port source:
  - the annotations at 93, `kind` 149, RESERVED 281, `transpile` 288 (comment blanking, renames, std140 block, sampler pairs, wrapper `main` at about 909)
  - the layout from naga struct offsets around 464 (compute std140 in Go instead)
  - the SDF 559-736, `f16`, `uses_backdrop` 797, the `Outline` and `Backdrop` types
  - the tests `annotations_and_transpile` and `sdf_of_a_circle` (1159-1241)
- `core/src/svg.rs:1593-1640` -- the shader fill emission with the backdrop SVG build and the error hatch. `core-go/svg/paint.go:556` holds 4a's `ShaderPainter` and the hatch, to extend.
- `core-go/resvg`, `core-go/usvg`, `core-go/skia` -- render the backdrop SVG into a `Pixmap`.
- `core/src/bin/cli.rs` -- add `shader-transpile <file.frag>` (prints JSON `{glsl, layout, error}`), `shader-sdf <outline.json> <w> <h>` (writes f32 texels as raw little-endian plus the f16 bytes), and `shader-backdrop <doc> <id>` (prints the backdrop SVG string the emitter would build) as test-only oracles.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- the three test-only oracles.
- [x] `core-go/shader/annotate.go`, `transpile.go` -- annotations, `kind`, `transpile`, errors.
- [x] `core-go/shader/layout.go` -- std140 layout and uniform packing, `f16`.
- [x] `core-go/shader/sdf.go` -- the outline SDF.
- [x] `core-go/svg/paint.go` (and its shader emission) -- the `ShaderPainter` backdrop extension and the backdrop SVG build, keeping the hatch path.
- [x] `core-go/shader/*_test.go` -- the ported Rust tests plus each matrix row.
- [x] `core-go/shader/parity_test.go`, `core-go/testdata/shader/` -- shader sources, outlines and a doc with a backdrop shader, compared with the three oracles (goldens without the CLI, live with it).

**Acceptance Criteria:**
- Given MinGW gcc as `CC` (the module needs cgo since 5a2), when `go test ./...` runs (also with `GOAMD64=v3`), then it passes with golden shader parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./shader/ ./svg/` runs, then transpile text, layout, errors, SDF texels and backdrop SVGs match Rust as stated.
- Given a shader fill with no painter, when it is exported, then the 4a error hatch is unchanged (the existing svg goldens still pass).
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- `core-go/shader` API for 6b: `Transpile(src)` (GLSL text and uniforms), `Parse(src) (*Program, error)` (transpile plus the std140 layout naga's GLSL front end computes, `_nrb` members renamed as `compile` does), `Program.Pack(uniforms, w, h, pw, ph)`, `Pixels(w, h, scale)`, `Program.UsesBackdrop`/`UsesBackdrop(src)`, `SDF(outline, w, h, dpi)` (size plus RGBA f16, rows bottom-up) and `F16`. `Kind` is a type tag plus `Def` and `Flag`, as `shader.rs` packs them into enum payloads.
- `UsesBackdrop(src)` checks transpile only; Rust's also needs naga to accept the shader. 6b's painter decides through `svg.ShaderPainter.UsesBackdrop`, so it can add the compile check.
- `svg.ShaderPainter` is now `UsesBackdrop(src) bool` plus `PNG(src, uniforms, w, h, outline, load, backdrop *Backdrop)`. `svg.Backdrop` carries `Key` (FxHash of the SVG, `svg.rs`'s cache key), `SVG` (the document, so tests and 6b's trace can see it) and `Paint func(w, h int) (*skia.Pixmap, bool)`, which parses with the emitter's fonts (when `Fonts` is a `*text.Fonts`), its folder and a fresh `ImageHrefResolver`, and renders with `core-go/resvg` at `pw/w`, `ph/h`. With no painter the hatch path is unchanged.
- The path SDF needs svgtypes' "None on the first error" rule, so `usvg` gains `SimplifyPathChecked`; `SimplifyPath` wraps it.
- f32 and f64 products that feed a sum are wrapped in explicit conversions, and the blur sums start at -0.0 as Rust 1.81+'s `Sum for f32` does (a zero texel keeps its sign through the f16 encoding). With that, SDF texels match Rust bit for bit on every fixture, at both GOAMD64=v1 and v3, though the comparison allows 1 ulp as stated.
- Rust side, behaviour unchanged: `render`'s uniform packing moved into `pack` (with `pixels`), `sdf` into `sdf_texels` + `half_rows`, so the oracles reuse them; test-only `oracle_transpile`, `oracle_sdf` and the thread-local `BACKDROP_TAP` (empty unless the CLI sets it) are `#[doc(hidden)] pub`.

## Plan Change Log

- `@min`/`@max` do not clamp: the matrix row says "clamped as Rust", but `shader.rs` only validates them (`notes` sets `range` and drops the values), and `components` never clamps. Go matches Rust (no clamping); `TestPack` and the `annotations` fixture pin it. Only `@color @default` components are clamped, as in Rust.
- CLI arguments grew to carry what the oracles need: `shader-transpile <file.frag> [<uniforms.json> <w> <h> <scale>]` prints `{error}`, or `{glsl}` plus either `{compileError}` (naga's message, which only skips the layout check) or `{layout, ubo}`; `shader-sdf <outline.json> <w> <h> <dpi> <out>` writes `<out>.f32` (texels top row first) and `<out>.f16` (bottom row first) and prints the size; `shader-backdrop <doc> <id> [--offline]` prints the backdrop SVGs of the node's SVG export as a JSON array. Outline JSON: `{"kind": "rrect", "radii": [...]}`, `{"kind": "ellipse"}`, `{"kind": "path", "d", "x", "y", "evenodd"}`.
- SDF goldens keep a 24×24 grid of sampled texels per case (`sdf.golden.json`, 181 KB); the full textures (up to 16 MB of f32 each) are compared live only.
- The backdrop fixture keeps every shader fill that precedes a `@backdrop` fill out of its frame: an earlier shader's GPU PNG lands in the backdrop's `defs`, which Go cannot produce before 6b.

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 3 findings — high 0, medium 0, low 3, false 0, maybe-false 0
- findings:
  - `[low]` `[reject]` The matrix expects an error for `gl_PointCoord`, but `Transpile` accepts it, and the change log doesn't record this. — Go matches Rust, whose `transpile` also leaves that check to naga (6b). The fix would be editing this plan's matrix.
  - `[low]` `[patch]` The `pointcoord` fixture also declares `mat2`, so its golden records naga's mat2/std140 error and skips the layout check. — Fix: drop the mat2 uniform and regenerate from the CLI.
  - `[low]` `[patch]` No fixture compares `bvec` uniforms (the block member, rename and layout path) with the oracle. — Fix: add a bvec2/3/4 fixture with packed bytes.

## Verification

**Commands:**
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

## Auto Run Result

- **Summary:** `core-go/shader` ports the CPU half of `shader.rs`:
  - annotations and `kind`
  - `Transpile` to GLSL 450 (renames, std140 `NrUniforms`, sampler pairs, wrapper `main`, line-preserving)
  - the naga-style std140 layout and `Pack`/`Pixels`, `F16`
  - the outline SDF (rect, ellipse, path via coverage, jump flood, blur, Scharr), with every error string

  `svg.ShaderPainter` gains `UsesBackdrop` and a `*Backdrop` (key, SVG, `Paint` via core-go/resvg), with the hatch unchanged. `usvg.SimplifyPathChecked` is added. Rust gets a no-behaviour-change refactor (`pack`/`pixels`, `sdf_texels` + `half_rows`), a thread-local `BACKDROP_TAP`, and test-only `shader-transpile`, `shader-sdf` and `shader-backdrop` CLI oracles.
- **Files:**
  - `core-go/shader/*.go`: the port
  - `core-go/svg/paint.go` and `svg_test.go`: the painter extension
  - `core-go/usvg/svgtypes_simple.go`: `SimplifyPathChecked`
  - `core-go/testdata/shader/`: fixtures
  - `core/src/{shader,svg}.rs` and `core/src/bin/cli.rs`: refactor, tap and oracles
- **Review:**
  - Patches applied: 2 low (the `pointcoord` fixture without mat2, and a new bvec fixture).
  - Rejected: 1 low (`gl_PointCoord` is left to naga, as in Rust).
  - Deviations logged by the implementer: `@min`/`@max` don't clamp, as in Rust, and `UsesBackdrop` is weaker until 6b's naga check.
- **Follow-up review recommended:** false (patched: 0 high, 0 medium, 2 low).
- **Verification:**
  - `go vet` and `go test ./...` pass offline, and `GOAMD64=v3` on `./shader/` and `./svg/` passes.
  - Live parity against the rebuilt CLI passes on every package. `./shader/` and `./svg/` were re-run after the patches.
  - SDF texels are bit-identical.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - The backdrop oracle draws through the GPU (WARP).
  - Linux libm may differ by 1 ulp, which the tolerance covers.
  - The Rust refactor touches production `shader.rs` (`cargo test` and parity confirm unchanged behaviour).
