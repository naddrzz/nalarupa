---
title: 'Go rewrite phase 5b: execute sandbox core'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: '98b3d105b1b617a0f4070af6bd3554a87f2ea014'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-5a2-quickjs-ng.md'
warnings: ['oversized']
deferred: []
---

<intent-contract>

## Intent

**Problem:** The design agent and MCP clients edit documents by sending JavaScript to the `execute` tool (`core/src/script.rs`). The Go core has the quickjs-ng runtime (5a2) but not the sandbox around it: the prelude API, the natives bridging to the document, limits and error reporting, persisted globals, validation and the report text the agent reads. Without these it cannot serve the agent.

**Approach:** Port the engine-independent core of `execute` to `core-go/script` behind a `Host` interface that the engine phase will implement:
- the PRELUDE globals (`document`, `Insert`, `Copy`, `Update`, `Replace`, `Move`, `Delete`, `SetVariables`, `GetVariables`, `Print`, `TakeScreenshot`, `Export`, `FindEmptySpace`, `Generate`, `Get`, with `Get`'s visitor API) and the 15 string natives
- the sandbox runtime (64 MB heap, 512 KB stack, 30 s budget or 2 s for previews, interrupt and cancel, `settle` with heap checks every 1024 jobs and unhandled-rejection failure) and `describe`'s exact error strings
- globals export and restore
- `apply_edits`, `Get`/`to_json`, `validate`/`issue_line`, and the success report text

A per-run id source replaces the process-wide generator (deferred from phase 1). A fake host built on `doc` + `render.Ctx` proves it against a seeded Rust `exec` oracle.

## Boundaries & Constraints

**Always:**
- The agent-visible text matches Rust byte for byte for the same document, script and seed:
  - every `describe` error string (stopped, timeout with `{:.1}` seconds, `{name}: {message} (line N)`, `Uncaught {v}`, out of memory, internal panic)
  - the rollback wrapper text
  - the success report (`OK`, `Note:` lines, `## Created nodes by name`, the globals carry-over line, `## Print output`, `## Issues detected (fix them in the next call)`)
- The resulting document (saved JSON) matches Rust's for the same seed, including generated ids. The per-run id source is seeded per call, as Rust's `prepare` reseeds from `args.__seed`, and supports the `resume_ids`/`ids_drawn` probe. It is owned by the run, not by a package global, so concurrent runs cannot shift each other's ids. The pending-image URL (`:322`) and the edit id (`:836`) draw from it in the same order as Rust.
- `Host` covers exactly what the sandbox needs from the engine: `Doc`, `Changed`, `SlideIn`, `LaidOut`, `FindEmptySpace`, `Generate`, and the id source. A `Locker` for shared mode is defined but used only by 5c. Engine-only behaviour (rollback commit, history, undo, emit, streams, pens, the gate, export planning) stays out and is recorded for 5c.
- Runtimes come from 5a2's `core-go/script` engine wrapper. No second engine wrapper, and cgo stays confined to `core-go/script/qjs`.
- A panic inside the run is recovered and reported as Rust's `internal error (panic) while running the script`.

**Never:**
- No transaction or history commit, streaming or preview contexts, real Generate downloads, pens, screenshots/exports execution through `ToolOut::finish`, `execute_shared` gating, or app checks (5c, with the engine).
- No change to `app/`, the FFI, or Rust behaviour apart from test-only CLI flags.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Edits | Scripts using every prelude call (insert with names, copy, update, replace, move, delete, variables) on the templates and fixtures | Same report text and saved document as `nalarupa-cli exec --seed` | No error expected |
| Get and visitor | `Get` by path, with options, and with a visitor using `parentCtx`, `depth`, `index`, `bounds`, `problems`, `skipChildren` | Same returned JSON and report | No error expected |
| Errors | A thrown Error on line N, a non-Error throw, a syntax error, an unhandled rejection, a bad node in `Insert` | Same `describe` text and rollback wrapper, document unchanged | Rolled back |
| Limits | An endless loop (30 s and 2 s preview budgets), 100 MB allocation, deep recursion, cancel mid-run | Same timeout, OOM, stack and stopped texts | No hang or crash |
| Globals | A second call reading globals set by the first (JSON-able values kept, functions dropped, `__base` excluded) | Same carry-over line and values | No error expected |
| Validation | Edits producing low contrast, near-miss alignment, small text, overlapping siblings | Same `## Issues detected` lines | Reported, not failed |
| Seeds | The same script with seeds 1 and 2, and the resume probe after a preview | The same ids as Rust per seed. Different seeds give different ids | No error expected |

</intent-contract>

## Code Map

- `core/src/script.rs`:
  - PRELUDE 92-135, `EXPORT_GLOBALS` 137, the natives 151, Get/Copy reading 376-476
  - `sandbox` and globals restore around 508-560, `settle` 567, `describe` 605
  - `resume_ids`/`ids_drawn` 688-702, `execute` 751, `prepare` and reseed 791, `execute_shared` 810 (5c), the rollback wrapper 838
  - the report 864-915, `shot_tree`/`shot_target`/`issue_line` 920-939 (`issue_line` only), `validate` 1015 (with fonts, icons and colour checks to 1123)
  - tests: transaction/repair/globals 4, streaming 6 (5c), warnings 5, limits and promises 3 (2642-2685). Port the ones that need no engine.
- `core/src/engine.rs:1199` (`tool("execute")`) and `:243` (`call_tool`) -- the call shapes, and what the engine does around `execute` (for the `Host` boundary).
- `core/src/doc.rs:768-800` and `core-go/doc/random.go` -- the generator to reproduce per run.
- `core-go/script/js.go` -- 5a2's engine wrapper: limits, interrupt, job pump, rejection tracker, heap. Extend it with what the sandbox needs (globals snapshot, native function registration) without a second wrapper.
- `core-go/render` (`Ctx.LaidOut`, `FindEmptySpace`, `TopIndex`, `FindRNode`), `core-go/doc`, `core-go/text` (`Fonts` for validation), `core-go/svg` (colors and assets checks) -- reuse.
- `core/src/bin/cli.rs` `exec` (89-113) -- add `--seed <n>` (sets `__seed`), `--globals <json>` and `--preview` (2 s budget) as test-only flags, so runs are reproducible.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- `exec` flags `--seed`, `--globals`, `--preview`. The output keeps the existing text format and prints the final globals JSON on a marked line.
- [x] `core-go/doc/random.go` -- add a per-run `IDSource` (seed, draw, reseed, probe) and keep the package-level functions as thin wrappers for existing callers.
- [x] `core-go/script/host.go` -- the `Host` and `Locker` interfaces, plus a `DocHost` test implementation over `doc` + `render.Ctx`.
- [x] `core-go/script/prelude.go` (embedded PRELUDE), `natives.go`, `sandbox.go` (`Run`, `settle`, `describe`, globals), `edits.go` (`apply_edits`), `get.go` (`Get`/`to_json`), `validate.go`, `report.go`.
- [x] `core-go/script/execute_test.go` -- the ported Rust tests that need no engine, plus each matrix row.
- [x] `core-go/script/exec_parity_test.go`, `core-go/testdata/exec/*.{pen,js}` -- each matrix row compared with `nalarupa-cli exec --seed` on report text, saved document and globals. Goldens are used without the CLI, live output with it.

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs (also with `GOAMD64=v3`, and `-race` on `./script/`), then it passes with golden exec parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./script/` runs, then every exec case's report text, saved document and globals equal Rust's.
- Given two runs in parallel with different seeds, when both finish, then each run's ids equal a serial run with that seed.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- **Rust CLI (test-only)**: `exec` takes `--seed <n>` (`__seed`), `--globals <json>` (the `agent` globals before the call), `--preview` (calls `script::execute_within` with `PREVIEW_TIMEOUT`; `execute_within` became `pub`, nothing else changed) and `--offline` (added: online font and icon downloads would change the validation text). After the report it prints `=== globals: <json>` (the agent's globals after the call). The parity test runs it with a fresh `NALARUPA_DATA`.
- **Ids (`doc/random.go`)**: `IDSource` (`NewIDSource`, `Reseed`, `Next`, `Resume` = `resume_ids`, `Drawn` = `ids_drawn`). `Doc.IDs` names the generator a document's mutations draw from (nil: the process-wide one, which `Reseed`/`RandomU64` still wrap); the zero `IDSource` draws as seed 1. `Execute` sets `Doc().IDs = Host.IDs()` for the call (restoring it after), so one source is enforced whatever the host does, and reseeds it from `__seed`; the pending-image url and the edit id draw from it in Rust's order (checked: `e-generate.js`'s edit id only matches with the pending draw before it).
- **Host**: `Doc`, `Changed`, `SlideIn`, `LaidOut`, `FindEmptySpace`, `Generate`, `IDs`, plus `Fonts` and `Assets`, which the plan's list missed: `validate` needs them for the font and icon checks. `Generate` is only the engine's half (start the download); the sandbox draws the pending url, sets the fill, bumps the version and writes the note, and records `Generate("svg")` as `Outcome.Pens`. `Locker` is declared, unused (5c).
- **Shared lookups**: `render.TopIndex` and `render.FindRNode` moved to `doc.(*Doc).TopIndex` and `resolve.(*RNode).Find`, which render and script both use.
- **DocHost lives in `script/dochost_test.go`** (package `script_test`), not `host.go`: `render` imports `script`, so `script` cannot import `render`. It opens documents as `Engine::open` does (migrate, repair ids) with offline fonts and assets, and `agents` keeps what the engine keeps between calls (per-agent globals, failed snippets).
- **qjs natives** (`qjs/native.go`, `wrap.c`): a native is an instance of the `RustFunction` class, which now has a call hook (rquickjs's class does too), with Function.prototype and `length` 0, so it pushes no stack frame and stacks match rquickjs's (`at Update (eval_script:6:53)` then `at <eval> (input:2:7)`, compared in `e-caught.js`). Arguments are JS ToString'd; an error is thrown as `JS_NewError` plus an own `message` (rquickjs's `throw_message`). A Go panic in a native is kept, an uncatchable error unwinds the JavaScript, and the Runtime call that entered JS (`Eval`, `Call`, `RunJob`) raises the panic again in Go; `Execute` reports Rust's `internal error (panic) while running the script` (also from inside `try` and from a promise job).
- **Execute** (`report.go`): `source` (the `input`/`edits` checks and their texts), snapshot, reseed, run, then either the rollback (the document's root is put back; `Outcome.Before`) with `RollbackText` and an edit id, or the report and `Outcome` (globals, touched, read, shots, exports, pens, icons, limit) for the engine to commit. The report stops after the issues; screenshot and export lines are 5c.
- **serde numbers**: JSON that JavaScript writes reaches the document and the globals as serde_json 1.0.154 holds it. Rust builds serde_json without `float_roundtrip` or `arbitrary_precision`, so a float is parsed by its fast path (not always the nearest f64: `1.1805916207174113e+21` reads one step below 2**70, which then shows in later calls) and written by zmij. `natives.go` ports that parser (`serdeNumber`), and `jsonv.FormatFloat` now writes zmij's signed exponent (`1e+21`, was ryu's `1e21`; no golden held one). JSON argument errors use serde's text for a value no JSON starts with (`expected value at line 1 column 1`) and for blank input; other parse errors keep jsonv's text.
- **Parity, measured against the release CLI on Windows** (`testdata/exec/cases.json`, goldens in `testdata/exec/golden`): every step of edits (every prelude call, instance paths, component copy, replace-variables), Get (paths, options, instance descendants, visitors with all context fields), 11 error kinds (line numbers, non-Error throws, syntax, rejections, bad nodes, bad arguments, caught native errors with stacks, a NUL in the source), limits (2 s preview and 30 s loops, 100 MB, recursion, job OOM, job spin), globals over four calls (JSON-able kept, functions, `let`, undefined and cycles dropped, a failed call keeps none), validation of shapes and of text, numbers, the CLI's empty-template fallback (a document that does not exist yet, a failed first call that saves nothing), the mobile and slides templates (`testdata/exec/{mobile,slides}.pen`, byte copies of `core/templates`; slides lays out text, so live only), and seeds 1, 2 and 1 with the preview budget: report text, error flag, globals and saved document all equal. `validate-text` lays out text with the installed fonts, so it is compared live only; `limits-30s` is skipped with `-short`.
- **Residual differences**: the recursion depth from 5a2 (only the count differs, not the overflow text); `contrast` uses Go's `math.Pow` where Rust uses the C runtime's `pow`, which may differ by one ulp at an exact threshold; a lone surrogate in an argument's JSON, which serde refuses and jsonv accepts; Rust's Windows build embeds the prelude with CRLF line ends (git checkout), the Go one with LF: only `Insert.toString()` would show it, line and column numbers are equal.
- **For 5c (engine-only, not ported)**: the history commit and undo step, `forget_gone_slides`/`touch`/`keep_clean` on rollback, the failed-snippet and per-agent globals maps, `note_flagged`, the `agent_edit` event, starting the AI Pen, `shot_tree` and the screenshot/export lines and planning, `settle_icons`, `execute_shared` with `Locker` and Stop, `stream_step`/`reset_streams` (they use `IDSource.Resume`/`Drawn`), real Generate downloads and `inflight`, `ToolOut::finish`.

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 7 findings — high 0, medium 2, low 5, false 0, maybe-false 0
- findings:
  - `[medium]` `[patch]` Run-owned ids depend on the host setting `Doc().IDs = IDs()`. An engine host could leave it nil, and ids would then ignore `__seed` and share the process generator. — Fix: `Execute` sets and restores `Doc().IDs` for the call, with a test.
  - `[medium]` `[patch]` A zero-value `IDSource` has seed 0, so xorshift returns 0 forever and the second `GenID` hangs. — Fix: treat seed 0 as `0|1`, as Rust's `| 1` does, with a test.
  - `[low]` `[patch]` `serdeError` omits `"` from the characters a JSON value can start with. — Fix: add it.
  - `[low]` `[patch]` `get.go` copies `render.TopIndex`/`FindRNode` because of an import cycle. — Fix: move them to a shared package and delete the copies.
  - `[low]` `[patch]` The "Edits" row has cases on `shapes.pen` only. — Fix: add cases on the empty-template fallback and one more fixture.
  - `[low]` `[reject]` The resume probe after a preview isn't compared with Rust. — Probe-and-resume only matters for streaming previews, which are 5c. Its Rust comparison belongs there (see the 5c deferred-work entry). Adding a probe oracle now needs a new CLI mode.
  - `[low]` `[patch]` `GOAMD64=v3` was evidenced only on three packages. — Fix: the orchestrator runs `GOAMD64=v3 go test ./...` on the whole module in final verification.

## Verification

**Commands:**
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./script/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (run on an idle machine: timing tests are load-sensitive)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-16, Windows, MinGW gcc 16.2, CLI rebuilt at `%TEMP%/nalarupa-go-target`):**
- `gofmt -l` and `go vet ./...` clean; `go test -count=1 ./...` passes with the exec goldens (the 30 s case included).
- `go test -race -count=1 ./script/` passes; `GOAMD64=v3 go test ./script/ ./doc/ ./jsonv/` passes.
- Live, `NALARUPA_CLI=… go test -count=1 ./...` passes: every exec case equals Rust on report text, error flag, globals and saved document (`validate-text` and `limits-30s` included), and the existing layout, script-node, render, export and PDF parity still passes.
- `TestParallelRunsKeepTheirIDs`: four seeds run on four goroutines give the ids of the serial runs.
- `cargo test --release` passes (292 tests).
- Not run on Linux.

## Auto Run Result

- **Summary:** `core-go/script` gains the engine-independent `execute` sandbox behind `Host`/`Locker`:
  - the embedded PRELUDE and 15 natives (registered in `qjs` the way rquickjs does)
  - the sandbox with limits, cancel, `settle` and `describe`'s exact strings, and panic recovery to Rust's text
  - globals export and restore, `apply_edits`, `Get`/`to_json`, `validate`/`issue_line`, and the report text
  - `Execute`, which rolls back on error and returns an `Outcome`

  `core-go/doc` gains a per-run `IDSource`, which `Execute` enforces through `Doc.IDs`. A zero-value source is safe. `jsonv` now writes serde_json's `e+` exponents and parses serde's numbers. `TopIndex`/`FindRNode` moved to `doc`/`resolve`. Rust gains test-only `exec` flags (`--seed`, `--globals`, `--preview`, `--offline`), and `execute_within` is now `pub`.
- **Files:**
  - `core-go/script/{host,prelude,natives,sandbox,edits,get,validate,report}.go`, `core-go/script/qjs/native.go` and `js.go`: the sandbox
  - `core-go/doc/{doc,random}.go`, `core-go/jsonv/jsonv.go`, `core-go/resolve`, `core-go/render/slides.go`, `core-go/svg/paint.go`: supporting changes
  - `core-go/testdata/exec/`: cases and goldens
  - `core/src/bin/cli.rs` and `core/src/script.rs`: the oracle flags
- **Review:**
  - Patches applied: 6 (2 medium, 4 low). Ids are enforced per run, the zero-value `IDSource` is fixed, serde's start characters are complete, the duplicate helpers are removed, template/fixture exec cases are added, and the whole module runs under `GOAMD64=v3`.
  - Rejected: 1 low (resume-probe Rust comparison, which belongs with 5c streaming).
- **Follow-up review recommended:** false (patched: 0 high, 2 medium, 4 low). Both medium patches are covered by deterministic tests.
- **Verification:**
  - With MinGW `CC`: `go vet` and `go test ./...` pass, `GOAMD64=v3 go test ./...` passes on the whole module, and `-race` on `./script/` passes.
  - Live parity against the rebuilt release CLI passes on every package, including the exec cases.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - Recursion depth differs, as recorded in 5a2.
  - The contrast check uses Go's `math.Pow`.
  - Uncommon serde error wordings are not reproduced.
  - Linux has not been run.
  - Engine-only parts are left for 5c.
