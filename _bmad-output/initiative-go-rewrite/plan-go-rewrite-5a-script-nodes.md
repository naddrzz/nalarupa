---
title: 'Go rewrite phase 5a: QuickJS runtime and script nodes'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'in-progress'
baseline_revision: '541ffb5af55102486597b8128c5276414d1f79b1'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4e-canvas.md'
warnings: []
deferred: []
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
- [ ] `core-go/script/js.go` -- the engine wrapper: runtime and context per run, limits, deadline and cancel, job pump, rejection tracking, heap usage, and exception-to-text with name, message and stack.
- [ ] `core-go/script/header.go` -- the header and `@input` parsing and coercion.
- [ ] `core-go/script/node.go` -- `run_script`, output validation and ids, host frame, cache, slots, counters, `Runs`, `ExpandScripts`.
- [ ] `core-go/render/renderer.go` -- wire `Ctx.Scripts` to `script.ExpandScripts` by default (keep it injectable).
- [ ] `core-go/script/*_test.go` -- the ported Rust script-node tests plus every matrix row.
- [ ] `core-go/script/parity_test.go`, `core-go/testdata/script/*.pen` -- documents with script nodes compared with Rust through `image` (pixels) and `layout --scripts` (boxes, if added). Goldens are used without the CLI, live output with it.

**Acceptance Criteria:**
- Given only Go, when `go test ./...` runs (also with `GOAMD64=v3` and `-race` on `./script/`), then it passes, including golden script-node parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./script/ ./render/ ./export/` runs, then script-node documents match Rust.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

## Plan Change Log

## Review Triage Log

## Verification

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./... && GOAMD64=v3 go test -count=1 ./script/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./script/` -- expected: pass
- `cd core-go && NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)
