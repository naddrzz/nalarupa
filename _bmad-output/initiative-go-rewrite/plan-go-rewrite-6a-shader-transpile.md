---
title: 'Go rewrite phase 6a: shader transpile, SDF and backdrop plumbing'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'in-progress'
baseline_revision: '6729d42a1e55525b75815ffa80f4b5204c8b6263'
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
- [ ] `core/src/bin/cli.rs` -- the three test-only oracles.
- [ ] `core-go/shader/annotate.go`, `transpile.go` -- annotations, `kind`, `transpile`, errors.
- [ ] `core-go/shader/layout.go` -- std140 layout and uniform packing, `f16`.
- [ ] `core-go/shader/sdf.go` -- the outline SDF.
- [ ] `core-go/svg/paint.go` (and its shader emission) -- the `ShaderPainter` backdrop extension and the backdrop SVG build, keeping the hatch path.
- [ ] `core-go/shader/*_test.go` -- the ported Rust tests plus each matrix row.
- [ ] `core-go/shader/parity_test.go`, `core-go/testdata/shader/` -- shader sources, outlines and a doc with a backdrop shader, compared with the three oracles (goldens without the CLI, live with it).

**Acceptance Criteria:**
- Given MinGW gcc as `CC` (the module needs cgo since 5a2), when `go test ./...` runs (also with `GOAMD64=v3`), then it passes with golden shader parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./shader/ ./svg/` runs, then transpile text, layout, errors, SDF texels and backdrop SVGs match Rust as stated.
- Given a shader fill with no painter, when it is exported, then the 4a error hatch is unchanged (the existing svg goldens still pass).
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

## Plan Change Log

## Review Triage Log

## Verification

**Commands:**
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)
