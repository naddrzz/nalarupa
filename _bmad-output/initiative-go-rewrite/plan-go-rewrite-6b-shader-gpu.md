---
title: 'Go rewrite phase 6b: shader fills on the GPU (wgpu-native)'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: 'c719a26f7aeb574191d30dc05c1ac1e791e46b99'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-6a-shader-transpile.md'
warnings: []
deferred:
  - summary: >-
      On hardware Vulkan adapters (this machine's Intel UHD 770), hash-noise shaders differ from Rust because wgpu-native cannot mark float ops NoContraction as Rust's strict_spirv does.
    evidence: |-
      The lattice hash differs on 4016 of 4096 pixels, and the noise fixture on 17423 of 76800 pixels (max 225). DX12, WARP and lavapipe give 0 and are asserted. Options: a SPIR-V passthrough with NoContraction (needs a toolchain), or accept the gap.
    location: >-
      core-go/shader/gpu
    severity: medium
  - summary: >-
      wgpu-native bundles naga 29 while the Rust core uses naga 30, so gl_PointCoord compiles in Rust and fails in Go on DX12.
    evidence: |-
      Kept as a known exception in the compile-error parity. It goes away when wgpu-native moves to wgpu 30.
    location: >-
      core-go/shader/gpu (pinned wgpu-native v29.0.1.1)
    severity: low
  - summary: >-
      A lost GPU device aborts the Go process (wgpu-native's poll panics through handle_error_fatal), where Rust returns a `GPU:` error and recreates the device.
    evidence: |-
      glue.c's nr_poll and nr_pop_scope call wgpuDevicePoll directly. Fixing it needs a device-lost callback and avoiding the fatal poll path. This must be done before the engine phase relies on shader fills.
    location: >-
      core-go/shader/gpu/glue.c
    severity: medium
  - summary: >-
      `go generate ./shader/gpu` must run before building core-go (it downloads the pinned wgpu-native, SHA-256 checked), and there are no pins for macOS or Windows arm64.
    evidence: |-
      The library is gitignored and not vendored. CI runs go generate. The release build and other platforms need pins.
    location: >-
      core-go/shader/gpu/fetch_wgpu.go
    severity: low
---

<intent-contract>

## Intent

**Problem:** Shader fills still draw the red error hatch in Go, because `svg.ShaderPainter` has no implementation. Rust compiles the transpiled GLSL with naga and draws it on a wgpu device (`core/src/shader.rs`).

**Approach:** Add the GPU half in a new cgo package, `core-go/shader/gpu`, on wgpu-native (a Go binding such as `github.com/cogentcore/webgpu`, or a thin cgo binding over a pinned wgpu-native release). It covers:
- device creation with Rust's backend and power selection, and the WARP/lavapipe software fallback
- shader modules from 6a's GLSL 450 through wgpu-native's GLSL input (naga inside wgpu-native), with the WGSL full-screen-triangle vertex shader
- the uniform buffer from 6a's `Pack`, the image/SDF/backdrop textures and samplers exactly as `shader.rs` sets them up
- the 256-row scissored tiled draw with a 20 s wait, aligned readback and the rgb≤a clamp
- the PNG (base64, `SCALE=2`, demultiplied, fast compression) and program caches with their limits, one process-wide GPU mutex, and the error caching and reset rules

Wire it in as `svg.ShaderPainter` (with `UsesBackdrop` also requiring a successful compile) and into export and canvas. Prove pixel parity against `nalarupa-cli svg`/`image`.

## Boundaries & Constraints

**Always:**
- The API mirrors `shader.rs` (`png`, `render`, `uses_backdrop`, `adapter`): same cache keys and limits (PNG cache cleared after 32 entries, programs after 64), the same texture formats, sizes and caps (SDF RGBA16F up to 1024 px, backdrop up to 2048 px, the 1×1 transparent fallback), row orders, sampler modes, the `nr_FragCoord` unit mapping, `@time` 0 and `@mouse` at the node centre.
- Error strings match Rust: `shader validation: …`, `shader compile: …`, `no GPU adapter for shader fills: …`, `GPU device for shader fills: …`, `GPU: …`, `empty fill`, and naga parse errors as `line {N-2}: {kind}` joined by newlines (map wgpu-native's compilation messages to that form). An init error is cached for good, and only `GPU:` errors reset the device.
- Pixel parity: shader fills rendered on the same machine and backend as Rust (WARP on this Windows machine, and lavapipe on Linux CI with `mesa-vulkan-drivers`) match within ±3 per channel. Fixtures with a stricter achievable bound state it.
- cgo is confined to `core-go/shader/gpu`. The wgpu-native library comes from the binding's Go module, or a pinned release whose checksum is recorded. Linking works with MinGW on Windows (d3d12/dxgi system libraries) and gcc on Linux. CI keeps `CGO_ENABLED=1` and installs the Vulkan software driver it already installs for Rust.
- The GPU mutex holds for a whole draw, as Rust's does. No GPU object is shared across goroutines without it.

**Never:**
- No glslang or other SPIR-V toolchain. If Rust's Vulkan `strict_spirv` NoContraction behaviour cannot be reproduced through wgpu-native, record the gap as deferred with measurements rather than adding a toolchain.
- No change to `app/`, the FFI, or Rust behaviour apart from test-only CLI additions.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Draw | 6a's shader fixtures with uniforms, colors, an image sampler, `@sdf` on rect, ellipse and path, at sizes up to 4096 px (tiles) | Pixels within ±3 of Rust's export of the same node | No error expected |
| Backdrop | A `@backdrop` shader over earlier siblings | Within ±3 of Rust | No error expected |
| Compile errors | A naga parse error on line N, a validation error | Same error text, and the hatch drawn and logged as Rust does | Hatch |
| No adapter | `WGPU_BACKEND` set to an unavailable backend, then the fallback | Same fallback or the same `no GPU adapter …` error, cached | Error cached |
| Caches | 40 distinct fills, then repeats | Same hit/miss behaviour and limits as Rust | No error expected |
| Concurrency | Exports from several goroutines at once under `-race` | Correct pixels, no race, draws serialised | No error expected |

</intent-contract>

## Code Map

- `core/src/shader.rs`:
  - device 773 (backends, power, `force_fallback_adapter`), `adapter` 802, `png` 824 (cache, demultiply, fast compression)
  - `render` 867, the wrapper unit mapping around 909, the error reset at 921, `strict_spirv` 932
  - draw and tiles 1022-1157 (textures, samplers, readback, clamp), and test `renders_on_the_gpu`
- `core-go/shader` -- 6a's `Transpile`, `Parse`, `Program.Pack`, `Pixels`, `SDF`, `F16`, `UsesBackdrop`.
- `core-go/svg/paint.go` -- `ShaderPainter` (`PNG` with `*Backdrop`, `UsesBackdrop`), to implement. `core-go/export` and `core-go/render` -- where the painter is set for export and canvas.
- `core/src/html.rs:485` -- `raster_layer` also uses shaders (phase 7 wires it). Leave a reusable entry point.
- `.github/workflows/ci.yml` -- the Linux job already installs `mesa-vulkan-drivers libvulkan1`. Make sure `core-go` tests can find lavapipe, and set the Windows job's `CC`/libraries for linking.
- `core/src/bin/cli.rs` -- `svg … --offline` and `image` already embed shader PNGs. Add a test-only `shader-render <frag> <uniforms.json> <w> <h> <outline.json> <out.rgba>` if a direct pixel oracle is simpler.

## Tasks & Acceptance

**Execution:**
- [x] `core-go/go.mod` -- the binding or wgpu-native dependency (pinned). (Pinned release instead, see the change log.)
- [x] `core-go/shader/gpu/device.go`, `draw.go`, `cache.go`, `errors.go` -- the GPU half.
- [x] `core-go/shader/painter.go` -- the `svg.ShaderPainter` implementation, set by default in export and canvas.
- [x] `core/src/bin/cli.rs` -- the optional `shader-render` oracle.
- [x] `core-go/shader/gpu/*_test.go` -- the ported `renders_on_the_gpu`, plus error, adapter-fallback, cache and concurrency tests (skipped with a message when no adapter exists, failing under `CI`).
- [x] `core-go/shader/gpu/parity_test.go`, `core-go/testdata/shader/` -- pixel parity per matrix row against the CLI (live only, since pixels depend on the adapter).
- [x] `.github/workflows/ci.yml` -- linking and software-adapter setup for the `core-go` steps.

**Acceptance Criteria:**
- Given MinGW gcc as `CC` on this machine, when `go test ./...` runs, then it passes, and the GPU tests run on WARP.
- Given the release CLI, when `NALARUPA_CLI=… go test ./shader/... ./export/ ./svg/` runs, then every shader fixture matches Rust within its stated tolerance, and errors match exactly.
- Given a document with a shader fill, when it is exported through `core-go/export`, then it no longer shows the error hatch.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- wgpu-native **v29.0.1.1** (the newest release; the Rust core is on wgpu/naga 30.0.1, wgpu-native has no 30 yet) is linked statically through a thin cgo binding: `core-go/shader/gpu/glue.{h,c}` builds every descriptor as `shader.rs` does, `gpu.go` holds the cgo flags. `go generate ./shader/gpu` runs `fetch_wgpu.go`, which downloads the release archive for the platform, checks its SHA-256 (recorded in the file: `windows_amd64` the MinGW `gnu` build, `linux_amd64`) and keeps the headers and `libwgpu_native.a` in `shader/gpu/wgpu-native/<goos>_<goarch>/` (gitignored). On Windows only `-lws2_32 -luserenv -lntdll` are named: D3D12/DXGI/the shader compilers come through import libraries inside the archive or are loaded at run time. Linux links with `-lm -ldl -lpthread` (checked with gcc 11 and lavapipe in WSL Ubuntu 22.04).
- Package layout: `shader/gpu` is the device (`Adapter`, `Compile`, `Draw`, one mutex for all GPU work, per-device module and pipeline caches); `shader/painter.go` is `shader.rs`'s `program`/`png`/`render` with the program (cleared past 64) and PNG (past 32) caches, `Painter` (UsesBackdrop = transpile + compile check) and `Render` (the reusable entry point for html.rs's `raster_layer`). `shader` registers `svg.DefaultShader` in `init`, which `svg.New` hands every emitter; `export` and `render` import `shader` for that effect, `svg`'s own tests keep the hatch.
- naga runs inside wgpu-native (GLSL source, stage fragment), so `compile`'s errors are rebuilt from wgpu-native's error tree: its GLSL `ParseErrors` arrive in Debug form (`Error { kind: …, meta: Span { start, end } }`), parsed and worded with naga's `ErrorKind` Display, the line taken from the span offset in the GLSL minus 2; validation errors are the codespan `Shader validation error: …` line; other module and pipeline errors are `shader compile: ` plus the tree with wgpu-native's entry point names mapped to the wgpu crate's (`Device::create_render_pipeline`). Request-adapter errors are wgpu-core's message: wgpu-native has GL but the Rust core does not, so GL is never handed to wgpu-native and a requested GL reads "gl support not compiled in"; an empty backend set is answered without wgpu-native (it reads 0 as every backend).
- `wgpuDevicePoll` has no timeout, so waits poll without blocking until the queue is empty (or the map callback ran), for at most 20 s, then `GPU: The requested Wait timed out before the submission was completed.` (wgpu's `PollError::Timeout`). A failed map is `GPU: Error occurred when trying to async map a buffer`.
- Instance flags are wgpu's release default (`VALIDATION_INDIRECT_CALL`), the DX12 compiler wgpu's default (`Auto`), the device's limits wgpu-native's defaults. Texture data reaches C pinned (`runtime.Pinner`), not copied.
- Results on this machine (Intel UHD 770; Rust picks it through Vulkan, not WARP, and so does Go): every `render.json` case is bit-identical to `nalarupa-cli shader-render` (max delta 0, up to 4096×4096 with tiles, SDF/backdrop caps, images, clamp), on Vulkan and with `WGPU_BACKEND=dx12`; the backdrop document exported through `core-go/export` matches `nalarupa-cli image` within 3 (scales 1 and 2); all 20 compile-error sources give Rust's exact text; the no-adapter errors for `webgpu`, `metal`, an unknown name, `gl` and `gl,metal` match. Except the NoContraction gap below, which the `noise` case and the lattice test log on Vulkan.
- Verified: `go vet ./...` and `go test ./...` pass offline and live (`NALARUPA_CLI`, rebuilt release CLI); `go test -race ./shader/...` passes on Windows and on lavapipe (WSL); `cargo test --release` passes (292 tests).

## Plan Change Log

- No Go module dependency: `github.com/cogentcore/webgpu`'s last release (v0.23.0, May 2025) predates wgpu-native 29 by a year and would put naga even further from the Rust core's 30, so a thin cgo binding over a pinned wgpu-native release (checksums in `fetch_wgpu.go`, fetched by `go generate`) replaces the `go.mod` entry. CI runs `go generate ./shader/gpu` before `go vet`.
- `shader/gpu` cannot hold the PNG and program caches (it would import `shader`, which imports it), so they live in `shader/painter.go`; `gpu/cache.go` keeps the per-device module and pipeline caches.
- The CLI oracle takes a scale and a `--backdrop` flag: `shader-render <file.frag> <uniforms.json|-> <w> <h> <scale> <outline.json> <out.rgba> [--backdrop]`, images relative to the shader's folder, the backdrop a fixed gradient (`shader::oracle_backdrop`).
- WARP: Rust picks this machine's Intel GPU (Vulkan), so the GPU tests run there; `WGPU_BACKEND=dx12` was run too. WARP and lavapipe are reached on the CI runners. lavapipe was also run locally (WSL, extracted `mesa-vulkan-drivers`) for the non-live tests.
- NoContraction (`strict_spirv`) cannot be reproduced: wgpu-native v29 exposes no SPIR-V passthrough feature and no SPIR-V out of naga, and the plan rules out another toolchain. Deferred with measurements: on Intel UHD 770 Vulkan the lattice hash of `renders_on_the_gpu` differs in 4016 of 4096 pixels (Rust: 0) and the `noise` fixture has 17423 of 76800 pixels over 3 (max 225); on Intel DX12 and on llvmpipe both are 0. The lattice test and the `noise` case log instead of asserting on hardware Vulkan adapters only (not llvmpipe/lavapipe, WARP or DX12, where they assert).
- naga 29 vs 30: `gl_PointCoord` compiles to HLSL in naga 30 but not 29, so on DX12 Rust draws it and Go reports `shader compile: …HLSL: Custom("Unsupported builtin PointCoord")`. Kept as a known gap in `TestParityCompileErrors`; it goes with a wgpu-native 30 bump.
- Without an adapter the order of errors differs: Rust's naga check runs on the CPU before the device starts, Go's needs the device, so a broken shader reports the device error (still the hatch), and `UsesBackdrop` is false (no backdrop is painted for a fill that then fails anyway).

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 7 findings — high 0, medium 2, low 4, false 1, maybe-false 0
- findings:
  - `[low]` `[reject]` wgpu-native validates with adapter capabilities (for example `MULTISAMPLED_SHADING`), where Rust uses `Capabilities::empty()`, so a shader reading `gl_SampleID` draws in Go but is rejected in Rust. — Rare built-ins in a design tool. The fix means a capability guard in transpile, with no naga in Go.
  - `[medium]` `[patch]` `program` holds the `programs` mutex while `Compile` waits for the GPU mutex, so cache hits and `UsesBackdrop` stall behind unrelated draws. — Fix: don't hold it across the compile.
  - `[low]` `[patch]` `backendsFromEnv` drops `noop`. — Fix: accept it as wgpu does, with a test.
  - `[low]` `[patch]` The lost-device abort and the other 6b gaps are in deferred-work.md but not in this plan's frontmatter. — Fix: frontmatter `deferred` filled by the orchestrator at finalize.
  - `[false]` `[reject]` The local GPU tests didn't run on WARP. — Rust selects the same Intel/Vulkan adapter here, and WARP is only Rust's fallback. Parity on the adapter Rust uses is the intent, and dx12 was also run.
  - `[medium]` `[patch]` The NoContraction exemption covers every Vulkan adapter, lavapipe included, and the lattice-hash check only logs. — Fix: exempt hardware Vulkan only, and assert elsewhere.
  - `[low]` `[patch]` Concurrency is covered only through `shader.Render`/`PNG`, not through export. — Fix: a concurrent export test.

## Verification

**Commands:**
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass, with GPU tests run on WARP
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./shader/...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

## Auto Run Result

- **Summary:**
  - `core-go/shader/gpu` is a thin cgo binding over pinned wgpu-native v29.0.1.1, linked statically and fetched by `go generate` with SHA-256 checked. It covers device and adapter selection with the software fallback, the GLSL module through wgpu-native's naga, the tiled draw, readback, clamp, textures and samplers, one GPU mutex, Rust's error wording and the init/reset rules.
  - `core-go/shader/painter.go` adds the PNG and program caches, `Render`, `PNG` and `Painter`, set as `svg.DefaultShader`, so export and canvas draw real shader fills instead of the hatch.
  - Rust gains a test-only `shader-render` oracle.
  - CI runs `go generate` before vet.
- **Review:**
  - Patches applied: 5 (2 medium, 3 low). The `programs` lock no longer waits on the GPU, `noop` is accepted, the NoContraction exemption is limited to hardware Vulkan with asserts elsewhere, a concurrent export test is added, and the plan's deferred list is filled in.
  - Rejected: 1 low (capability-gated built-ins) and 1 false (WARP wording).
  - Deferred: 4 (Vulkan NoContraction, naga 29 vs 30, lost-device abort, `go generate` and platform pins).
- **Follow-up review recommended:** false (patched: 0 high, 2 medium, 3 low). Both medium patches are covered by deterministic tests (`-race`, asserts on DX12).
- **Verification:**
  - `go vet` and `go test ./...` pass.
  - `-race` on `./shader/...` passes.
  - Live parity passes on every package (`shader`, `export` and `svg` re-run after the patches). Render cases are bit-identical on Vulkan and DX12, except the deferred hardware-Vulkan noise gap.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - The deferred items above.
  - Windows CI linking and lavapipe pixel parity have not been run yet.
