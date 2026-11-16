---
title: 'Go rewrite phase 5a2: quickjs-ng engine through cgo'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'in-progress'
baseline_revision: ''
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-5a-script-nodes.md'
warnings: []
deferred: []
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
- [ ] `core-go/script/qjs/*.c`, `*.h`, `LICENSE`, `README.md` -- the vendored quickjs-ng sources.
- [ ] `core-go/script/qjs/qjs.go` (+ a small `wrap.c`/`wrap.h` if needed for callbacks) -- cgo bindings for the calls `js.go` needs.
- [ ] `core-go/script/js.go` -- rebuilt on `qjs`, with the workarounds removed.
- [ ] `core-go/script/*_test.go`, `parity_test.go` -- tightened to exact error text and columns, plus the review-probe fixtures as parity cases (`core-go/testdata/script/probes*.pen`).
- [ ] `core-go/go.mod`, `go.sum` -- drop the modernc modules.
- [ ] `.github/workflows/ci.yml` -- `CGO_ENABLED=1` and the compiler for the `core-go` steps.

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs in `core-go` (also with `GOAMD64=v3`, and `-race` on `./script/`), then it passes.
- Given the release CLI, when `NALARUPA_CLI=… go test ./...` runs, then every script-node fixture, including the review probes, matches Rust's `__status` text and output exactly, apart from any per-case difference stated in Implementation Notes.
- Given the repository, when `go list -m all` runs in `core-go`, then neither `modernc.org/libquickjs` nor `modernc.org/libc` appears.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

## Plan Change Log

## Review Triage Log

## Verification

**Commands:**
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./script/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)
