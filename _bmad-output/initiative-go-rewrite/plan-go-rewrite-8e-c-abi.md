---
title: 'Go rewrite phase 8e: C ABI, Go DLL and app checks'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: 'ddbe24f6248b96828c7f85d1330a12d401ad620a'
route: 'full'
route_source: 'auto'
risk: 'high'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
followup_review_recommended: false
context:
  - '{project-root}/_bmad-output/initiative-go-rewrite/decisions.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8c-execute-engine.md'
warnings: []
deferred:
  - summary: >-
      `smoke`, `doc-check` and `script-check` stop at agent/MCP steps (`init.mcpPort > 0`, `provider_test`, the MCP server), which are phase 9. Re-run all three against the Go DLL once phase 9 lands.
    evidence: |-
      smoke fails at line 34, doc-check at 504 and script-check at 113. Every non-agent step before those passes, and the Rust DLL passes all three.
    location: >-
      app/scripts/smoke.ts, doc-check.ts, script-check.ts
    severity: medium
  - summary: >-
      smoke's "edited canvas matches a full render around a rotated, flipped shadow" fails by 21 against the Go DLL, because phase 4e dropped the vello rasteriser. Rust also fails it under NALARUPA_RASTER=resvg.
    evidence: |-
      This is a consequence of the 4e decision to render the canvas on the resvg path. Before cutover, the maintainer must choose: port raster.rs (the vello path, or at least its shadow handling), relax that smoke assertion, or fix the repaint seam on the resvg path.
    location: >-
      core-go/render (repaint), app/scripts/smoke.ts
    severity: medium
  - summary: >-
      Text layout of a 160k-character single line is about 1.7x slower in Go (1.39 s vs 0.82 s alone, 4.0-6.6 s inside smoke against a 4000 ms bound), so smoke's linear-time check fails.
    evidence: |-
      It grows linearly (no quadratic bug), and Go shaping (go-text plus the workarounds) is slower. Profile and optimize core-go/text shaping and wrapping before cutover.
    location: >-
      core-go/text (shape.go, wrap.go)
    severity: medium
  - summary: >-
      The Linux c-shared build (`build.sh` .so branch), the ABI tests and the app checks against libnalarupa_core.so have not been run.
    evidence: |-
      Unverified: it has only been run on Windows. Settle by adding the Go-DLL app-check run to Linux CI at cutover, or running it in WSL with a Go toolchain.
    location: >-
      core-go/build.sh, core-go/cmd/core
    severity: medium (unverified)
---

<intent-contract>

## Intent

**Problem:** The Bun app loads the core as a shared library through `bun:ffi` and talks to it through three C exports (`nr_call`, `nr_free`, `nr_render`, in `core/src/lib.rs`). The Go core has the engine, editor, execute and streams, but no C ABI and no shared library, so the app cannot run on it.

**Approach:** Add a `core-go/cmd/core` main package built with `-buildmode=c-shared` into `nalarupa_core.dll` (`libnalarupa_core.so` on Linux):
- `//export nr_call(ptr, len) *C.char` dispatches every non-agent command exactly as `lib.rs` does (same JSON envelope, NUL stripping and error strings), returning C-allocated strings.
- `nr_free` frees them.
- `nr_render(w, h, camX, camY, zoom, bg, solo_ptr, solo_len, flags)` returns a pointer to a reused C-allocated RGBA buffer, valid until the next render.
- Every export and every goroutine recovers panics, mirroring `catch_unwind` (`"internal error (panic)"`, a null render).
- The agent and MCP commands answer Rust's not-available error until phase 9, and `init` skips the MCP start.

Add a build script and a `package.ts` option for the Go DLL, then run the app's non-agent FFI checks against it with `NALARUPA_CORE` pointing at the Go DLL.

## Boundaries & Constraints

**Always:**
- The ABI is identical: symbol names, argument types, the response envelope `{"ok":true,"data":…}` / `{"ok":false,"error":…}`, the ownership rules (`nr_free` on every `nr_call` result, never on render pixels), and the render buffer layout (w·h·4 RGBA).
- Dispatch matches `lib.rs` command by command for non-agent commands, including which commands take the edit gate, `sync` before queries, and `png` off the lock. Reuse `engine.Replayer`/`Handle` dispatch instead of a second dispatcher.
- No Go-heap pointer is handed to C and kept: strings come from `C.CString`, and render pixels live in C memory.
- The DLL links quickjs-ng (cgo) and wgpu-native statically, so one file ships. A missing `go generate ./shader/gpu` fails the build loudly.
- The app checks run unchanged against the Go DLL, through `NALARUPA_CORE` (already supported by `core.ts`). The checks are the oracle.

**Never:**
- No agent loop, providers, chat or MCP (phase 9).
- No change to `app/` source except `package.ts`/build scripts adding the Go-core option, and no change to Rust behaviour.
- No removal of the Rust core (phase 10).

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| App checks | `bun run smoke`, `editor-check`, `doc-check`, `drag-check`, `component-check`, `layout-check`, `text-check`, `paint-check`, `shader-check`, `scriptnode-check`, `script-check`, `issues-check`, `viewport-check`, export-lock-worker with `NALARUPA_CORE=<go dll>` | All pass, as they do with the Rust DLL | Failures reported with the check output |
| Envelope | Malformed JSON, an unknown cmd, an embedded NUL, a command that panics | Same responses as Rust | Panic gives `internal error (panic)`, no crash |
| Render | Several sizes, solo and sharp flags, a render after a resize | Correct pixels, a stable pointer until the next render, no leak across many renders | A panic returns null |
| Agent commands | `agent_send`, `chat_list`, `mcp_install`, and others | Rust's not-available error | No crash |
| Concurrency | Renders and calls from Bun while downloads and an execute run | No deadlock or race | No crash |

</intent-contract>

## Code Map

- `core/src/lib.rs` -- `nr_call` 541 (`catch_unwind` 543, NUL strip, `CString::into_raw` 534), `nr_free` 557, `nr_render` 572, the dispatcher 30-300 (the command list and gating 91-96), `state()` 83.
- `app/src/core.ts:7-61` -- the library lookup (`NALARUPA_CORE` first), `call`/`free`, `toArrayBuffer` on render. `app/scripts/package.ts:48-50` copies the release build. `app/package.json` -- the check scripts.
- `core-go/engine` (`Handle`, `Replayer` dispatch, `CallTool`, `Finish`, streams), `core-go/editor` (registered commands), `core-go/render` -- reuse.
- `core-go/shader/gpu` -- `go generate` prerequisite and static linking flags (MinGW: d3d12/dxgi system libraries).
- AGENTS.md "Running and verifying" -- the `NALARUPA_CORE` usage, and that a running app locks the DLL.

## Tasks & Acceptance

**Execution:**
- [x] `core-go/cmd/core/main.go` -- the exports, envelope, panic recovery and the C render buffer.
- [x] `core-go/engine/dispatch.go` (or the extended `replay.go`) -- the complete non-agent command table, shared with replay, plus the agent stubs.
- [x] `core-go/build.ps1` / `core-go/build.sh` (or `go generate` + `go build` documented in a README section) -- the c-shared build producing `core-go/out/nalarupa_core.dll` / `.so`.
- [x] `app/scripts/package.ts` -- an opt-in flag or env (`NALARUPA_GO_CORE=1`) that packages the Go DLL instead of the Rust one.
- [x] `core-go/cmd/core/abi_test.go` -- a Go test that loads the built library through cgo/`syscall` (or calls the exported functions directly) for envelope, panic, render-buffer and leak checks.
- [x] Run the app checks listed in the matrix with `NALARUPA_CORE` set to the Go DLL, and record the results in Implementation Notes.

**Acceptance Criteria:**
- Given MinGW gcc as `CC` after `go generate ./shader/gpu`, when the build script runs, then a single `nalarupa_core.dll` is produced that exports `nr_call`, `nr_free` and `nr_render`.
- Given that DLL and `NALARUPA_CORE` pointing at it, when each app check in the matrix runs from `app/`, then it passes as it does with the Rust DLL.
- Given MinGW gcc as `CC`, when `go test ./...` runs, then it passes, including the ABI tests.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- `core-go/cmd/core/main.go`: `nr_call(void*, GoUint32) void*`, `nr_free(void*)` and `nr_render(…) void*`. The exports use Go types (`unsafe.Pointer`, `uint32`, `float64`), which have the same C ABI as `bun:ffi`'s `ptr`/`u32`/`f64`. This lets `abi_test.go` call them directly without cgo in a test file. Answers are `C.CString` (NUL stripped, as `to_c`) and `nr_free` is `C.free`. The render copies the renderer's pixels, under the engine lock, into a `C.malloc` buffer that only grows. So the pointer stays the same for the same or a smaller size, and is valid until the next render. A panic in parse, dispatch or marshal answers `internal error (panic)`, and a panic in a render returns null. The deferred unlocks release the gate and the lock.
- `core-go/engine/dispatch.go`: `Dispatch` (moved out of `replay.go`, exported; the replay falls through to it). It adds:
  - `init`, which starts no MCP server, so `mcpPort` 0 and `mcpUrl` `http://127.0.0.1:0/mcp`
  - `export` (`Engine.Export` in `toolout.go`, html.rs `export`: plan under the lock, settle and replan from a snapshot, write without it; `snapshot` is now shared with `planOutputs`)
  - `provider_save`/`provider_remove` (`SaveProvider`/`RemoveProvider` in `settings.go`, a port of provider.rs `save`, settings only)

  The agent, chat, provider-test, Claude Code account and MCP commands answer `AgentUnavailable`. Rust has no such error, so the text is ours: "The agent is not available in this core yet." `agent_stop` and `chat_flush`, which do nothing when no agent runs, answer `true` as Rust does then (the app calls them on Stop and on exit). New provider ids come from `doc.RandomU64`, not the document's id generator. The UI export tests `ui_exports_wait_for_images_still_downloading` and `ui_exports_run_script_nodes_off_the_lock` are ported to `engine/execute_test.go`.
- Fix found by the checks: the Go PDF writer wrote `/MediaBox [0 0 w h]`, but Rust (pdf-writer) writes `/MediaBox[0 0 w h]`, which `doc-check` matches literally. It now writes Rust's form, and the two Go tests that matched the old spacing were updated.
- Build: `core-go/build.ps1` and `build.sh` refuse to start without `shader/gpu/wgpu-native/<goos>_<goarch>/lib/libwgpu_native.a`. They then run `go build -buildmode=c-shared -trimpath -ldflags '-s -w -extldflags=-static'` (no `-static` on Linux). The 24.6 MB dll imports only Windows system dlls, with no libgcc or winpthread, and exports exactly `nr_call`, `nr_free` and `nr_render`. `core-go/out/` is ignored. `package.ts` ships `core-go/out/<lib>` when `NALARUPA_GO_CORE=1`.
- App checks against the Go dll (`NALARUPA_CORE=…\core-go\out\nalarupa_core.dll`, Windows):
  - Pass: `editor-check`, `drag-check`, `component-check`, `layout-check`, `text-check`, `paint-check`, `shader-check`, `scriptnode-check`, `issues-check`, `viewport-check`. `component-check` failed once on its first run in a fresh data folder ("undo restores the component and the instance") and passed on the three runs after it, so it is probably font-download timing.
  - `doc-check`: passes up to line 504, `provider_test` (agent, phase 9). The child with the env key passes. Everything after the provider block is skipped on Windows.
  - `script-check`: passes up to line 113, which calls the MCP server (phase 9).
  - `smoke`: fails at line 34, `init.mcpPort > 0` (phase 9). A scratch copy with the MCP assertions removed passes everything else, including `export-lock-worker` (another thread's calls keep answering during an export), apart from two failures:
    - "an edited canvas matches a full render around a rotated, flipped shadow (largest difference 21)". The Rust dll fails the same way, with the same difference of 21, under `NALARUPA_RASTER=resvg`. It follows from the phase 4e choice to drop vello, not from the ABI.
    - "a long single line lays out in linear time": 160k characters take 4.0-6.6 s against the 4000 ms bound. Rust/resvg takes 2.2 s in the same run. In isolation the Go dll takes 1.39 s and Rust 0.82 s. The ratio is linear, so this is the Go text shaping and flattening being about 1.7x slower, not a quadratic.
  - With the Rust dll (vello), `smoke`, `doc-check`, `component-check` and `script-check` all pass on this machine.

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 9 findings — high 0, medium 4, low 4, false 0, maybe-false 1
- findings:
  - `[medium]` `[defer]` AC2 is not met: `smoke`, `doc-check` and `script-check` fail, at agent/MCP steps (phase 9) plus two non-agent smoke assertions (the shadow seam from dropping vello, and the speed of laying out a long line). — The agent and MCP parts are excluded by intent and re-run in phase 9. The seam and the speed issue are deferred (frontmatter) as cutover blockers needing a fix or a decision.
  - `[medium]` `[patch]` The `ui_exports_*` tests deferred from 8c weren't ported. — Fix: port both.
  - `[low]` `[patch]` `t.Fatal` is called from worker goroutines. — Fix: report through `t.Error`.
  - `[low]` `[patch]` The concurrency row runs with no download in flight. — Fix: an online variant with a local server.
  - `[medium]` `[patch]` `chat_flush`/`agent_stop` return errors, so every app exit logs an error. — Fix: return Rust's no-op success.
  - `[low]` `[patch]` `build.sh` is not executable. — Fixed by the orchestrator (`git update-index --chmod=+x`).
  - `[maybe-false]` `[defer]` The Linux build, ABI tests and app checks have not been run. — Deferred, unverified (frontmatter).
  - `[low]` `[patch]` Provider ids draw from the document's id generator. — Fix: `doc.RandomU64()`.
  - `[low]` `[patch]` `RemoveProvider` turns `null` into `[]`. — Fix: keep `null`.

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && <build script>` -- expected: `out/nalarupa_core.dll` with the three exports
- `cd app && NALARUPA_CORE=<abs path to go dll> bun run smoke` (then each matrix check) -- expected: pass
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-16, Windows, MinGW gcc):**
- `go vet ./...` and `go test -count=1 ./...` pass on every package, `cmd/core` included. `go test -race ./cmd/core` passes.
- `build.sh` and `build.ps1` both produce `out/nalarupa_core.dll` with the three exports.
- `cargo test --release` passes (292 tests).
- App checks: as listed in the Implementation Notes. Ten pass outright. `doc-check`, `script-check` and `smoke` stop only at agent/MCP steps (phase 9). Behind those, `smoke` has two failures that are not from the ABI: the resvg-path shadow repaint, which Rust/resvg shares, and long-line layout speed.
- Not run on Linux.

## Auto Run Result (orchestrator finalize)

- **Summary:** `core-go/cmd/core` builds a single c-shared `nalarupa_core.dll` (24.6 MB, quickjs-ng and wgpu-native linked statically) exporting `nr_call`/`nr_free`/`nr_render` with the same ABI as `lib.rs`:
  - a C-string envelope with NUL stripping
  - a reused C render buffer
  - panic recovery on every export and goroutine

  `engine.Dispatch` (moved out of replay) covers every non-agent command. Agent, chat and MCP commands answer not-available, except `chat_flush`/`agent_stop`, which give Rust's no-op success. `build.ps1`/`build.sh` and `package.ts NALARUPA_GO_CORE=1` are added. The PDF `/MediaBox[...]` spacing now matches Rust.
- **Review:**
  - Patches applied: 7. These port the ui_exports tests, return no-op success for exit-time agent commands, fix worker failure reporting, add an online concurrency variant, draw provider ids from `doc.RandomU64`, keep a provider `null`, and set the `build.sh` exec bit.
  - Deferred: 4 (agent/MCP-blocked app checks, the smoke shadow seam, long-line layout speed, Linux unverified).
- **Follow-up review recommended:** false. The patched items are covered by deterministic tests.
- **Verification:**
  - `go vet` and `go test ./...` pass, and `-race` on `./cmd/core/` and `./engine/` passes.
  - Live parity passes on every package.
  - The DLL was rebuilt. With `NALARUPA_CORE` set to it, the app checks editor, drag, component, paint and viewport pass now, and layout, text, shader, scriptnode and issues passed earlier.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:** see deferred. `smoke`, `doc-check` and `script-check` need phase 9 plus the two smoke fixes.
