---
title: 'Go rewrite phase 5a2: quickjs-ng engine through cgo'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: 'ae3f9834d8021250bc7756f9a96fd242022b8b26'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-5a-script-nodes.md'
warnings: []
deferred:
  - summary: >-
      Recursion limits under the 512 KB stack differ from Rust's MSVC-built quickjs-ng because MinGW gcc gives the interpreter different C frame sizes. A script recursing about 147-325 frames succeeds in Go but throws RangeError in Rust, and the built-in limits differ too.
    evidence: |-
      Review measurements (Rust CLI vs Go with MinGW gcc):
      - plain recursion 146 vs 325
      - JSON.stringify nesting 1169 vs 1287
      - JSON.parse nesting 1854 vs 2007
      - String() of nested arrays 58 vs 98

      The ratios differ per case (0.45, 0.91, 0.92, 0.59), so no single stack budget matches all of them, and cgo cannot use MSVC. All error texts match. Options for the maintainer: accept the deeper limits, or calibrate JS_SetMaxStackSize to the plain-recursion case (146), which would make the built-in limits shallower than Rust's. Implementation Notes overstate the match: only plain recursion is stated there.
    location: >-
      core-go/script/js.go (Limits.Stack), core-go/script/parity_test.go (differs)
    severity: medium
  - summary: >-
      The cgo build and every script limit are unverified on Linux, where Rust builds quickjs-ng with gcc at cargo's release level and cgo uses -O2, so frame sizes and limits may differ again.
    evidence: |-
      Unverified: it has only been run on Windows. Settle by the Linux CI job's script parity result. If it fails on depth-dependent probes, mask or measure them per platform.
    location: >-
      .github/workflows/ci.yml, core-go/script
    severity: medium (unverified)
---

<intent-contract>

## Intent

**Problem:** Phase 5a runs script nodes on Bellard QuickJS (`modernc.org/libquickjs`). Its error wording, call-depth limit, heap accounting and stack columns differ from the quickjs-ng 0.16.2 that Rust's rquickjs 0.14 embeds, and the maintainer has now chosen exact parity through cgo (`decisions.md`, revised 2026-11-16).

**Approach:** Vendor the quickjs-ng 0.16.2 C sources from the local `rquickjs-sys-0.14.0` crate into a new cgo package, `core-go/script/qjs`, compiled the way rquickjs-sys builds them (same source files, `_GNU_SOURCE`, no `NDEBUG`). Rebuild `core-go/script/js.go` on it with rquickjs's setup: `Context::full` intrinsics, memory and stack limits, interrupt handler, job pump, rejection tracker and heap usage. Then remove `modernc.org/libquickjs`, `modernc.org/libc`, the `ngCompat` rewrites, the `stackSlots` cap and the global shims that quickjs-ng provides natively. Script-node behaviour then matches Rust without those workarounds.

## Boundaries & Constraints

**Always:**
- Vendored sources are byte-identical to `rquickjs-sys-0.14.0/quickjs/`: `quickjs.c`, `libregexp.c`, `libunicode.c`, `dtoa.c`, and the headers rquickjs-sys copies (`builtin-*.h`, `cutils.h`, `dtoa.h`, `libregexp*.h`, `libunicode*.h`, `list.h`, `quickjs-atom.h`, `quickjs-opcode.h`, `quickjs-c-atomics.h`, `quickjs.h`). Include `LICENSE` and a `README.md` naming the version and origin. No source edits. If a compile fix is unavoidable, keep it in a separate cgo wrapper file, never in the vendored sources.
- Runtime setup mirrors what `core/src/script.rs` does through rquickjs: a fresh `JSRuntime`/`JSContext` per run, `Context::full` intrinsics, `JS_SetMemoryLimit`, `JS_SetMaxStackSize` (512 KB), an interrupt handler for deadline and cancel armed before the context callback runs (the AGENTS.md deadlock note), `JS_ExecutePendingJob` pumping, the promise rejection tracker, `JS_ComputeMemoryUsage` for the heap check, and eval with filename `input` in sloppy mode.
- Error text comes straight from the engine: name, message coerced to string, and `stack`. Script-node errors and limits must match Rust exactly. That includes the cases the 5a review found (`not a constructor`, `JSON.parse`, BigInt, deep `JSON.stringify` and output nesting, the 16 MB OOM point, stack overflow) and stack columns. Where a residual difference remains because MSVC (Rust's Windows build) and MinGW gcc (cgo) use different C stack frame sizes, measure it and state it per case.
- cgo is confined to `core-go/script/qjs` and only `core-go/script` imports it. JS values never escape a run, and each runtime stays on one goroutine (`runtime.LockOSThread` while a runtime is alive).
- Builds: Windows uses MinGW gcc (`CC=<MinGW gcc>`, since the `gcc` on PATH is a clang shim). Linux CI uses the runner's gcc. `.github/workflows/ci.yml` sets `CGO_ENABLED=1` (and a MinGW `CC` on the Windows job) for the `core-go` step.

**Never:**
- No new JS API, natives, execute tool or engine features beyond 5a's scope (5b and 5c).
- No change to Rust behaviour apart from test-only CLI additions, and no change to `app/` or the FFI.
- No pure-Go fallback kept alongside, and no build tags that silently disable script nodes.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| 5a parity suite | Every `core-go/testdata/script` fixture and all 5a tests | Pass with the `ngCompat`/`stackSlots`/shim workarounds removed, and columns compared (no longer dropped) | No error expected |
| Review probes | The 5a review's probes: `new (()=>1)()`, `JSON.parse("{bad")`, `1n+1`, `e.message=42`, JSON.stringify nesting 80/400, output nesting 74, `new Array(n).fill(1.5)` at 700000/900000, `performance`, `queueMicrotask`, `DisposableStack`, `Error.stackTraceLimit` | Same `__status` text and outcome as `nalarupa-cli layout --scripts` | Engine errors shown as in Rust |
| Limits | An endless loop (2 s), 100 MB allocation, deep recursion | Same timeout, OOM and `RangeError: Maximum call stack size exceeded` texts and lines | No hang or crash |
| Concurrency | Many script nodes expanded in parallel under `-race` | Same results as serial, no race, no runtime shared across goroutines | No error expected |

</intent-contract>

## Code Map

- `~/.cargo/registry/src/index.crates.io-*/rquickjs-sys-0.14.0/quickjs/` -- the sources to vendor (version 0.16.2 at `quickjs.h:1450-1452`). `build.rs:121-160` and `:250-260` list the files, defines and flags.
- `~/.cargo/registry/src/index.crates.io-*/rquickjs-core-0.14*/src/` -- how rquickjs sets up `Runtime::new`, `Context::full`, the interrupt handler, the memory and stack limits, the job pump, exceptions (`Exception::message`/`stack` coerced) and the rejection tracker. Mirror those calls.
- `core/src/script.rs:500-620` (`sandbox`, `settle`, `describe`) and `:1460-1520` (`run_script`) -- what the Rust side configures and how it reads errors.
- `core-go/script/js.go` -- 5a's wrapper over libquickjs, to be rebuilt on `qjs`. Remove `ngCompat`, `stackSlots` and the shims for globals quickjs-ng has natively (`performance`, `queueMicrotask`, `Array.fromAsync`, `Error.captureStackTrace`, `Symbol.dispose`, `Error.stackTraceLimit`, `DisposableStack`).
- `core-go/script/node.go`, `header.go` -- unchanged except where they relied on workarounds. `core-go/script/*_test.go` and `parity_test.go` -- tighten what 5a relaxed (columns, timeout position).
- `core-go/go.mod`/`go.sum` -- drop `modernc.org/libquickjs` and `modernc.org/libc`.
- `.github/workflows/ci.yml` -- the `core-go` steps in both jobs.
- `_bmad-output/initiative-go-rewrite/plan-go-rewrite-5a-script-nodes.md` frontmatter `deferred` -- the engine entries this slice resolves. Note which are now resolved in this plan's Implementation Notes, and leave 5a's plan unedited.

## Tasks & Acceptance

**Execution:**
- [x] `core-go/script/qjs/*.c`, `*.h`, `LICENSE`, `README.md` -- the vendored quickjs-ng sources.
- [x] `core-go/script/qjs/qjs.go` (+ a small `wrap.c`/`wrap.h` if needed for callbacks) -- cgo bindings for the calls `js.go` needs.
- [x] `core-go/script/js.go` -- rebuilt on `qjs`, with the workarounds removed.
- [x] `core-go/script/*_test.go`, `parity_test.go` -- tightened to exact error text and columns, plus the review-probe fixtures as parity cases (`core-go/testdata/script/probes*.pen`).
- [x] `core-go/go.mod`, `go.sum` -- drop the modernc modules.
- [x] `.github/workflows/ci.yml` -- `CGO_ENABLED=1` and the compiler for the `core-go` steps.

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs in `core-go` (also with `GOAMD64=v3`, and `-race` on `./script/`), then it passes.
- Given the release CLI, when `NALARUPA_CLI=… go test ./...` runs, then every script-node fixture, including the review probes, matches Rust's `__status` text and output exactly, apart from any per-case difference stated in Implementation Notes.
- Given the repository, when `go list -m all` runs in `core-go`, then neither `modernc.org/libquickjs` nor `modernc.org/libc` appears.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- **Vendoring (`core-go/script/qjs`)**: the 4 sources and 14 headers of rquickjs-sys `build.rs` plus `LICENSE`, copied with `cmp` checked; no edits. A `.gitattributes` (`* -text`) keeps git's `core.autocrlf` from rewriting them on checkout. cgo flags as `build.rs` sets them for a GNU target: `-D_GNU_SOURCE`, no `NDEBUG`, Windows `-DWIN32_LEAN_AND_MEAN -std=c11`; `-lm` elsewhere. It builds with MinGW gcc 16.2 and cgo's default `-O2` with no output; no compile fix was needed.
- **Glue (`wrap.c`)**, mirroring rquickjs 0.14 (`parallel`, no `rust-alloc`, so `JS_NewRuntime`'s malloc):
  - `qjs_new`: `JS_NewRuntime`, `JS_SetDumpFlags(0)`, the three classes rquickjs's `Opaque::initialize` registers (same names and order, so heap accounting matches), then script.rs's memory limit, 512 KB stack, interrupt handler, and `JS_NewContext` (`Context::full`).
  - `JS_UpdateStackTop` before every eval, call and pending job, as rquickjs does with `parallel`, so the stack limit counts from where each call enters C.
  - Interrupt handler in C: an atomic cancel flag and a deadline on quickjs-ng's own monotonic clock (`js__hrtime_ns`), `now > deadline` as script.rs. No Go callbacks run from C.
  - Rejection tracker in C: calls the named global with `(promise, reason, handled)`; what it throws is dropped (5a's behaviour, unit-tested only, for 5b).
- **Go side**: `qjs.Runtime` locks its goroutine to the OS thread from `New` to `Close`. `script.JS` keeps its API; `Limits.StackSlots` became `Limits.Stack` (bytes). The script prelude now runs as rquickjs's `ctx.eval` does (strict, file `eval_script`) through `ExecStrict`. Removed: `ngCompat`, `stackSlots`, `maxFrames`, the `globals` shims and native `performance.now`, `modernc.org/libquickjs` and `modernc.org/libc` (`go mod tidy` also dropped their indirect modules).
- **Parity, measured against the release CLI on Windows** (`testdata/script/probes.pen`, golden `probes.scripts.json`):
  - Exact, stack columns included: `not a constructor`, the `JSON.parse` SyntaxError (with `<input>:1:2` and `parse (native)` frames), BigInt (`cannot convert BigInt to number`), a coerced `e.message = 42`, caught `undefinedName is not defined` and `Maximum call stack size exceeded`, `Error.stackTraceLimit = 2`, JSON.stringify of nesting 80 and 400, output nesting 63 (accepted) and 64/74 (`Invalid script output`, serde's depth limit), and the globals probe (`performance`, `queueMicrotask`, `Array.fromAsync`, `Error.captureStackTrace`, `Symbol.dispose`/`asyncDispose`, `DisposableStack`, `AsyncDisposableStack`, `Error.stackTraceLimit` 10; enumerable globals `performance+__seed`).
  - The 16 MB heap: `new Array(n).fill(1.5)` passes at n=699913 and runs out of memory from 699914 in both (bisected both sides); 700000 and 900000 are OOM in both.
  - `errors.pen` now matches with columns, and the interrupted loop stops at the same position (`e-loop.js:1:83`), so the parity test compares both.
  - **Residual difference (stated per case)**: plain recursion depth. A 512 KB stack holds 325 frames of `function f(n) { d = n; return f(n + 1) + 1 }` with MinGW gcc (cgo, `-O2`) against 146 with MSVC (Rust's Windows build), from different C frame sizes in the interpreter. Texts are identical (the top 10 frames and the error are the same); only a script that counts its depth sees it. `parity_test.go` masks that count for `probes/depths/depth` only, and `TestScriptLimits` asserts at least 146.
- **5a deferred entries resolved here**: all three. Error positions (columns, interrupted line) now match; the engine is quickjs-ng 0.16.2 (wording, depth cap for built-ins, heap point), apart from the plain-recursion depth above; `DisposableStack` is native.
- **Clock**: the 2 s budget now runs on quickjs-ng's clock (QueryPerformanceCounter on Windows, as Rust's `Instant`), while Go's Windows clock advances by timer ticks; `TestScriptNodeRuns` allows its elapsed time to read up to one tick (16 ms) short of 2 s.
- **Tests**: `TestContextFullGlobals` replaces the shim test; `TestExceptionCoercesMessageAndStack` sets `stack` with `defineProperty` (quickjs-ng's `stack` setter only takes strings); `TestParallelRunsMatchSerial` runs 20 jobs (output, overflow, OOM, throw, deep stringify) serially and on 20 goroutines and compares.

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 3 findings — high 0, medium 2, low 0, false 0, maybe-false 1
- findings:
  - `[medium]` `[defer]` Built-in recursion limits still differ (stringify 1169 vs 1287, parse 1854 vs 2007, `String()` of nested arrays 58 vs 98), while Implementation Notes claim they match. — The cause is MSVC vs MinGW frame sizes, which no single stack budget can match. Deferred with the calibration choice for the maintainer (frontmatter). The overstated note is corrected by this entry.
  - `[medium]` `[defer]` The plain-recursion difference (146 vs 325) changes outcomes, not just a counter: depth 147-325 succeeds in Go and throws in Rust. — Same root cause as the entry above, and grouped with it.
  - `[maybe-false]` `[defer]` Linux has not been run, and Rust's gcc release build may give other limits. — Deferred, unverified (frontmatter).

## Auto Run Result

- **Summary:**
  - Script nodes now run on quickjs-ng 0.16.2 through cgo. The sources are vendored byte-identical from `rquickjs-sys-0.14.0` into `core-go/script/qjs`, with the C glue in `wrap.c`/`wrap.h` and the bindings in `qjs.go`.
  - `js.go` is rebuilt with rquickjs's runtime setup. The `ngCompat` rewrites, `stackSlots` cap and global shims are removed, and the modernc modules are dropped.
  - All 5a review probes match Rust exactly: error texts, stack columns, the OOM point at 699913/699914, and the interrupted-loop position.
  - CI sets `CGO_ENABLED=1`, with MinGW as `CC` on Windows.
- **Files:**
  - `core-go/script/qjs/`: vendored sources, glue, LICENSE, README and `.gitattributes`
  - `core-go/script/{js,node}.go` and tests
  - `core-go/testdata/script/probes.pen` and its `p-*.js` scripts
  - `core-go/go.mod` and `go.sum`
  - `.github/workflows/ci.yml`
- **Review:**
  - Patches applied: 0.
  - Deferred: 2 medium (one root cause, recursion limits from compiler frame sizes) and 1 unverified (Linux).
  - Rejected: none.
- **Follow-up review recommended:** false (no patches).
- **Verification:**
  - With MinGW `CC`, `go vet` and `go test ./...` pass. A load-induced timing failure passed when re-run idle.
  - `GOAMD64=v3` and `-race` on `./script/` pass.
  - Live parity against the release CLI passes in full (`./script/` re-run idle).
  - `go list -m all` shows no modernc modules.
  - Rust is unchanged; `cargo test --release` passed earlier, 292 tests.
- **Residual risks:**
  - Recursion limits differ, as deferred above.
  - Linux has not been run.
  - Timing tests are sensitive to machine load.

## Verification

**Commands:**
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./script/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-16, Windows, MinGW gcc 16.2, CLI at `%TEMP%/nalarupa-go-target`):**
- `go vet ./...` and `gofmt -l` clean. `go test -count=1 ./...` passes (goldens). `GOAMD64=v3 go test ./script/ ./render/ ./export/` passes. `go test -race ./script/` passes.
- Live, `NALARUPA_CLI=… go test -count=1 ./...` passes: every script status equals Rust's exactly, columns and the interrupted position included, apart from the masked recursion count; the 3 PNGs are pixel-identical. (A run started while cargo and the race build loaded the machine failed two timing tests, `TestWorkersAndEviction` and render's `TestFirstLayoutIsLinearInTopLevelLayers`; both pass on an idle machine.)
- `go list -m all` lists no `modernc.org` module. `CGO_ENABLED=0 go build ./script/` fails loudly (build constraints exclude all Go files in `script/qjs`), so script nodes are never silently disabled.
- `cargo test --release` passes (292 tests).
- Not run on Linux; the CI change (`CGO_ENABLED=1`, and `CC=C:/mingw64/bin/gcc.exe` on Windows, where the runner image installs MinGW-w64) is untested until CI runs.
