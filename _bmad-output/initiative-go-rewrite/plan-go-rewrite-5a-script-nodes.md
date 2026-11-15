---
title: 'Go rewrite phase 5a: QuickJS runtime and script nodes'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: 'e72b42b7887564e47590e1cf96d13b4117df54a7'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4e-canvas.md'
warnings: []
deferred:
  - summary: >-
      Error positions differ: QuickJS and quickjs-ng place stack-frame columns differently, and an interrupted loop stops at a different line.
    evidence: |-
      Found during implementation. The parity test drops columns and the interrupted run's code excerpt. Lines match everywhere else.
    location: >-
      core-go/script/js.go
    severity: low
  - summary: >-
      The JS engine is Bellard QuickJS (modernc.org/libquickjs), not the quickjs-ng 0.16.2 that rquickjs bundles. Error wording differs beyond the two rewrites in ngCompat, the call-depth cap is shallower when built-ins recurse and deeper for plain recursion, and the 16 MB heap limit triggers at a different allocation size.
    evidence: |-
      Review probes against the release CLI:
      - `new (()=>1)()` gives `TypeError:  is not a constructor`, Rust `TypeError: not a constructor`.
      - `JSON.parse("{bad")` and BigInt errors are worded differently.
      - JSON.stringify of nesting 80 overflows in Go (Rust handles 400), and script output nested 74+ deep shows `Script runtime error` instead of `Invalid script output`.
      - `new Array(n).fill(1.5)` hits OOM from n=700000 in Rust but from 900000 in Go.
      - A caught stack overflow or undefined name keeps the QuickJS wording inside the script.

      fastschema/qjs (quickjs-ng on wazero) has no deadline hook, no stack check and unsandboxed std/os globals, so no pure-Go quickjs-ng embedding meets the limits. Exact parity would need quickjs-ng through cgo (the c-shared DLL for bun:ffi needs cgo anyway), which overrides decisions.md's "without cgo". That is a maintainer decision.
    location: >-
      core-go/script/js.go (engine, ngCompat, stackSlots, heap limit)
    severity: medium
  - summary: >-
      DisposableStack (present in rquickjs Context::full) is not provided.
    evidence: |-
      Review typeof probe. The easy globals (performance, queueMicrotask, Array.fromAsync, Error.captureStackTrace, Symbol.dispose/asyncDispose, Error.stackTraceLimit) are shimmed. DisposableStack needs a full implementation.
    location: >-
      core-go/script/js.go (NewJS)
    severity: low
---

<intent-contract>

## Intent

**Problem:** Documents can contain script nodes (`type: "script"`), whose JavaScript generates child nodes during layout. The Go core has the hook (`render.Ctx.Scripts`, `Finished`) but no JavaScript engine, so those nodes stay empty.

**Approach:** Choose and wrap a QuickJS engine that runs inside Go without cgo, then port the script-node half of `core/src/script.rs` to a new `core-go/script` package and plug it into `render.Ctx.Scripts`. The half covers header and `@input` parsing and coercion, `run_script` with its limits and seeded `Math.random`, output validation and id reassignment, the result cache, worker slots, the `FINISHED` counter and `busy`, `Runs` recording, and `expand_scripts`. The JS runtime layer is shared with the execute sandbox (phase 5b).

## Boundaries & Constraints

**Always:**
- Engine choice: prefer `github.com/fastschema/qjs` (quickjs-ng on wazero, the same engine family as rquickjs 0.14's bundled quickjs-ng 0.16.2), so error names, messages, `input:L:C` stack lines and the "call stack" overflow message match. Use `modernc.org/quickjs` only if fastschema/qjs lacks something required here (an interrupt/deadline hook, memory and stack limits, a pending-job pump, or creating a fresh runtime per run). Record the choice and its reason in Implementation Notes. If neither offers all of these, use the closest and record each gap as deferred.
- Script nodes behave as in Rust:
  - the same accepted and rejected headers, input coercion, limits (16 MB heap, 512 KB stack, 2 s run budget, untimed compile), and mulberry32 `Math.random` seeded 0
  - the same output rules (1000 nodes max, no reusable or connection nodes, ids reassigned `sid/k`, `ref` targets resolved, migration from the declared schema) and the `layout:none` host frame
  - the same error titles, descriptions and code excerpts in `__status`
  - the same cache key and eviction, and the same running status in record mode
- Concurrency: worker goroutines replace Rust's 8 threads and 1024 slots, with the same observable results. Every runtime is confined to its goroutine. No package-level mutable state beyond what Rust keeps process-wide (cache, slots, counter), and that state is mutex-guarded.
- No cgo. One new module dependency (the QuickJS package) is allowed.

**Never:**
- No `execute` tool, PRELUDE, natives, globals export, report text, transactions, streaming, Generate or pens (phases 5b and 5c).
- No change to `app/`, the FFI, or Rust behaviour apart from test-only CLI additions.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Runs | A script node with a valid `@schema` header and typed `@input`s, generating frames, text and paths | Same children (ids `sid/k`, layout) as Rust: `image` export pixels equal, `layout` boxes equal | No error expected |
| Inputs | Each input type with valid, missing, out-of-range and wrong-typed values | Same coerced values | Coercion as in Rust |
| Header errors | No header, a bad `@schema`, a malformed `@input` | Same `__status` title, description and excerpt | Error shown in the node |
| Runtime errors | A thrown Error, a non-Error throw, a reference error on line 3 | Same `Script runtime error` text with stack and the `> 3:` excerpt | Error shown in the node |
| Limits | An endless loop, a 100 MB allocation, deep recursion | Same timeout, out-of-memory and "call stack" errors | Error shown, no hang or crash |
| Output rules | More than 1000 nodes, a reusable node, a dangling `ref` | Same rejection or repair as Rust | Error or repair as in Rust |
| Async and cache | A script awaiting a promise; the same node twice; 300 distinct scripts | Same result. The second run hits the cache, and eviction follows Rust's rule | No error expected |

</intent-contract>

## Code Map

- `core/src/script.rs` -- the port source for this slice:
  - `run_script` around 1460-1520 (limits, deadline armed outside `Context::with`, mulberry32 at 1470)
  - the header and input lexer/parser 1214-1443, coercion 1446
  - script nodes 1136-1879 (output checks, `sid/k` ids, host frame, cache, slots, `FINISHED`/`finished()` 1698, `busy()` 1703, `Runs`/`record_runs`/`recorded_runs` 1636-1679, `off_lock` 1683, `expand_scripts` 1834)
  - `describe` 605 (error strings, partly shared with 5b)
  - tests 2799-2977 (script nodes) and 2642-2685 (limits and promises, to port in their script-node form)
- `core-go/render/renderer.go:43-47, 80` -- the `Ctx.Scripts` / `Finished` hook to fill.
- `core-go/svg/svg.go:140`, `core-go/svg/text.go:265` -- where `__status` is drawn.
- `core/src/render.rs:269` -- where `expand_scripts` is called.
- `core/src/bin/cli.rs` -- the `layout` command doesn't expand scripts, but `image` and `canvas` do. Add a test-only `--scripts` flag to `layout` if a box-level oracle is needed.
- AGENTS.md -- the `set_interrupt_handler` note: deadline handling must not deadlock (in Go, arm the deadline before entering the runtime callback).

## Tasks & Acceptance

**Execution:**
- [x] `core-go/script/js.go` -- the engine wrapper: runtime and context per run, limits, deadline and cancel, job pump, rejection tracking, heap usage, and exception-to-text with name, message and stack.
- [x] `core-go/script/header.go` -- the header and `@input` parsing and coercion.
- [x] `core-go/script/node.go` -- `run_script`, output validation and ids, host frame, cache, slots, counters, `Runs`, `ExpandScripts`.
- [x] `core-go/render/renderer.go` -- wire `Ctx.Scripts` to `script.ExpandScripts` by default (keep it injectable).
- [x] `core-go/script/*_test.go` -- the ported Rust script-node tests plus every matrix row.
- [x] `core-go/script/parity_test.go`, `core-go/testdata/script/*.pen` -- documents with script nodes compared with Rust through `image` (pixels) and `layout --scripts` (boxes, if added). Goldens are used without the CLI, live output with it.

**Acceptance Criteria:**
- Given only Go, when `go test ./...` runs (also with `GOAMD64=v3` and `-race` on `./script/`), then it passes, including golden script-node parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./script/ ./render/ ./export/` runs, then script-node documents match Rust.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- **Engine: `modernc.org/libquickjs` v0.13.1** (QuickJS 2026-06-04, transpiled to Go: the engine `modernc.org/quickjs` wraps), used directly with its runtime `modernc.org/libc` v1.74.4. The wrapper is bypassed because its `Eval` fixes the file name to `<eval>` and it has no rejection tracker or heap usage. Pinned below `modernc.org/quickjs` v0.25, which needs Go 1.26.
- **Why not `github.com/fastschema/qjs` (v0.0.6, quickjs-ng 0.10.1 on wazero)**, measured:
  - It has no interrupt hook (its C handler is commented out). The only deadline is `CloseOnContextDone`, which kills the wasm module with no JS error or stack, and made a 1e7-iteration loop take 8 s instead of 1 s.
  - quickjs-ng turns off its stack check on WASI, so deep recursion corrupts wasm memory ("invalid opcode", out-of-bounds traps) instead of throwing.
  - Every context exposes `std`, `os`, timers and a writable mount of the working directory.
  - Its error columns don't match rquickjs's quickjs-ng 0.10 either.
- **libquickjs**, measured: a catchable stack overflow, an uncatchable interrupt with a stack, OOM in 40 ms, the 1e7 loop in 0.47 s, no `console`/`print`, and sloppy eval with a chosen file name.
- **quickjs-ng wording (`ngCompat` in js.go)**: `InternalError: stack overflow` becomes `RangeError: Maximum call stack size exceeded`, `'x' is not defined` becomes `x is not defined`, and stacks keep 10 frames. With that, every status in `testdata/script/errors.pen` equals Rust's apart from frame columns and the interrupted loop's position (see deferred).
- **Globals (`globals` in js.go)**: each context gets what rquickjs's `Context::full` has and this engine lacks:
  - non-enumerable shims for `performance` (`now` is native: monotonic milliseconds since the runtime was made; plus `timeOrigin`), `queueMicrotask`, `Array.fromAsync`, `Error.captureStackTrace`, `Error.stackTraceLimit` (10) and `Symbol.dispose`/`Symbol.asyncDispose`
  - `DisposableStack` is left out (deferred)
  - Stack traces keep 10 frames whatever `Error.stackTraceLimit` is set to.
- **Error text**: `message` and `stack` are read coerced to strings (undefined/null as ""), and `name` only when it is a string, as rquickjs reads them.
- **Deadline**: kept as monotonic nanoseconds since a package start time, so a wall-clock step doesn't change the budget.
- **One FNV-1a**: `svg.FxHash` is exported and used by render and script; the copies in `render/num.go` and script are gone.
- **Limits**: 16 MB heap; 151 call slots (libquickjs counts frames, not bytes; 151 gives the 146 frames Rust reaches in 512 KB); 2 s armed just before the call, written atomically to C memory read by the interrupt handler, so no lock is held. Compile is untimed. `Cancel`, `RunJob`, `TrackRejections` (calls a named global, as `__track`) and `HeapSize` are there for 5b and unit-tested only. The interrupt doesn't fire inside a stream of tiny jobs, so a job loop checks `Expired` between jobs (as `settle` does).
- **Script nodes** (node.go), ported from script.rs:
  - Rust's thread-local `ASKED` becomes an explicit `*script.Runs` (record mode), passed through `ExpandScripts`, `render.Ctx.Runs` and `export.Ctx.Runs`. `OffLock` takes `func(*Runs) T`.
  - Package state, all mutex-guarded: cache, slots, active workers and tick, plus the atomic `finished`. Worker goroutines replace threads; results are deep-copied on every read.
  - Paths join as Rust's `Path::join` does (not cleaned). A file that isn't UTF-8 is "Script not found", as `read_to_string` makes it.
- **Wiring**:
  - `render.Ctx.Scripts` nil now runs `script.ExpandScripts`, and `Finished` nil is `script.Finished`.
  - `svg.NodeSVGAt`/`svg.Export` take an `svg.Expand` hook. `export.Ctx` runs scripts by default (`Scripts`/`Runs` fields), so image and PDF exports show script output, as Rust's `node_svg_at` does. `svg.Export`'s only caller (its parity test) passes nil.
- **Rust**: `nalarupa-cli layout <doc> [<saved>] --scripts` runs `expand_scripts` (not live) with the original document's folder, and dumps `__status` as `"status"`. It's test-only; nothing else changed.
- **Tests**:
  - `header_test.go`: every directive error word for word, blocks, coercion.
  - `node_test.go`: `script_node_runs`, runtime errors, limits, output rules, ids/refs/host frame, promises not run, cache and eviction past 256, slots (back-slot), 8 workers, slot eviction at 1024, record mode/`OffLock`.
  - `js_test.go`: engine primitives.
  - `render_test.go`: `script_node_renders_and_hits_as_one`, `live_script_runs_off_the_lock`, and the export-off-the-lock test through `export.Ctx` + `OffLock`. The agent screenshot/export test waits for 5b.
  - `parity_test.go`: `runs.pen` (inputs of every type, clamping, defaults, enum, a repeated input, migration from 2.10, refs, nested layouts, clip, rotation, opacity, promises) is compared as boxes plus pixel-exact PNGs of 3 frames. `errors.pen` (every error kind and limit) is compared as boxes plus statuses. Goldens are in `testdata/script/golden`.
- **Toolchain workaround**: Go 1.25 crashed compiling a slice-literal `copy` into C memory (`disjointTypes` ICE), so `putValues` writes one value at a time. `go vet`'s unsafeptr check is met by `cptr` (C addresses only).

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 9 findings — high 0, medium 4, low 5, false 0, maybe-false 0
- findings:
  - `[low]` `[reject]` The engine is `modernc.org/libquickjs`, plus a direct `modernc.org/libc` require, where the plan names `modernc.org/quickjs`. — `modernc.org/quickjs` is the wrapper over this same libquickjs engine, which is the allowed fallback (used directly because the wrapper hard-codes the file name to `<eval>`). `libc` is the engine's own required runtime. The engine-level gaps are recorded in the deferred entries.
  - `[medium]` `[defer]` Error wording differs beyond `ngCompat` (not a constructor, JSON.parse, BigInt). — This is inherent to Bellard QuickJS vs quickjs-ng. Deferred with the engine decision (frontmatter).
  - `[medium]` `[patch]` Missing globals that `Context::full` has: performance, queueMicrotask, Array.fromAsync, Error.captureStackTrace, Symbol.dispose/asyncDispose, Error.stackTraceLimit. — Fix: shims plus a typeof test. DisposableStack is deferred.
  - `[medium]` `[defer]` The 151-slot cap is shallower when built-ins recurse (stringify depth 80 vs 400, output nesting 74+ gives a different error). — It counts engine frames, not bytes. Deferred with the engine decision.
  - `[medium]` `[defer]` The 16 MB heap limit triggers at a different size (OOM from n=900000 vs 700000). — Engine memory accounting differs. Deferred with the engine decision.
  - `[low]` `[patch]` A non-string `message`/`stack` is dropped, where rquickjs coerces it. — Fix: coerce with ToString.
  - `[low]` `[patch]` The deadline compares wall-clock UnixNano, dropping the monotonic clock. — Fix: a monotonic comparison.
  - `[low]` `[patch]` `lines()` strips a lone trailing `\r`, unlike `str::lines`. — Fix: match exactly.
  - `[low]` `[patch]` A third copy of `fxhash`. — Fix: reuse one exported copy.

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./script/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./script/` -- expected: pass
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-16, Windows, CLI at `%TEMP%/nalarupa-go-target`):**
- `go vet ./...` passes, also with `GOOS=linux CGO_ENABLED=0`. `gofmt -l` is clean.
- `go test ./...` passes with golden script-node parity. `GOAMD64=v3 go test ./script/ ./render/ ./export/` passes.
- `go test -race ./script/` passes. The 3e5-iteration budget test was sized for the race build's slower engine.
- Live, `NALARUPA_CLI=… go test ./...` passes: script statuses equal Rust's after column normalisation, and the 3 PNGs are pixel-identical.
- `cargo test --release` passes (292 tests).
- Not run on Linux.

## Auto Run Result

- Summary: core-go/script ports script nodes on Bellard QuickJS (modernc.org/libquickjs; fastschema/qjs lacked a deadline hook and stack check): header/@input parsing, run_script with limits and seeded Math.random, output checks, sid/k ids, cache, worker slots, Finished/Busy, Runs, ExpandScripts; wired into render.Ctx and export.Ctx. Rust gains test-only `layout --scripts`.
- Review: 5 patches (globals shims, coerced message/stack, monotonic deadline, str::lines, single svg.FxHash); 3 deferred on the engine (wording, depth cap, heap point) needing a maintainer decision (quickjs-ng via cgo?); 1 rejected.
- Follow-up review recommended: false (patched 0 high, 1 medium, 4 low).
- Verification: go vet/test pass, GOAMD64=v3 and -race on ./script/ pass, live parity against the release CLI passed before the patches; patched packages re-tested (script, svg, render). cargo test --release: 292 passed.
- Residual risks: engine divergences above; Linux not run.
