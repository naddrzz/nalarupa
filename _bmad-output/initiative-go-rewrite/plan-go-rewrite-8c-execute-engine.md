---
title: 'Go rewrite phase 8c: execute in the engine'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: 'c4bac735af0adec1913d29c9059f4298ac8d8ee3'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-5b-execute-sandbox.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8a-engine-core.md'
warnings: []
deferred: []
---

<intent-contract>

## Intent

**Problem:** The `execute` tool, the agent's and MCP clients' way to edit documents, still answers "unknown tool" in the Go engine. The 5b sandbox core exists, but the engine half of `script.rs`/`engine.rs` does not:
- the transaction (history commit or rollback, the failed-snippet `editId` store)
- `execute_shared` with `EXECUTE_GATE` and cancel
- `ToolOut.finish` (script nodes, icon settling, download waits, screenshots, export jobs)
- real `Generate` image downloads into `resolve_pending`
- the `begin_execute`/`end_execute` activity events

**Approach:** Make the 8a engine the full `script.Host` and port the engine half of `execute` to `core-go/engine`:
- `Tool("execute")` and `CallTool` routing `execute` through `ExecuteShared`, which takes the edit gate as Rust's `EXECUTE_GATE` does, runs the JS with the engine unlocked in shared mode through the `Locker`, and supports a cancel flag
- the commit and rollback around `script.Execute`'s `Outcome`, the editId store and repair flow
- `ToolOut` and `Finish` run with the engine lock released: script nodes, a 12 s icon settle, download waits, stale-shot redraws from the call's doc, PNG encoding, and 7c export jobs settled, replanned and run with the engine's waiter and an agent-path policy hook
- the `Generate` download goroutine (AI/stock image) resolving through `resolve_pending`, with `inflight` tracking
- `begin_execute`/`end_execute` events

Agent-only parts (per-agent globals, pens, Stop flags) become hooks that phase 9 fills.

## Boundaries & Constraints

**Always:**
- For the same replay sequence and seed (`__seed` passed as Rust does), `tool execute` responses (text, error flag, image count), the document, history (undo after an execute undoes the whole call), saved files, and written export files match Rust. Screenshot images are compared by decoded pixels within the 4d tolerances.
- Lock order and gating mirror Rust: the edit gate serialises executes and gated edits, the engine lock is not held across JS execution in shared mode or across `Finish`'s slow work (AGENTS.md: "never hold `engine()` across slow work"), and cancel interrupts a running snippet with Rust's stopped text.
- Generate downloads go through injectable base URLs and HTTP client (as phases 3/4a), never touch the network in tests unless `NALARUPA_NETWORK=1`, and every goroutine recovers panics.
- Reuse `core-go/script` (`Execute`, `Outcome`, `Host`, `Locker`), `core-go/export` (job, waiter, policy), `core-go/engine`. No duplicated logic. Floating-point products get explicit conversions (no FMA).

**Never:**
- No streaming previews (8d), C ABI (8e), or agent loop/providers/MCP/pens (phase 9). The agent hooks default to inert.
- No change to `app/` or Rust behaviour apart from extending the test-only replay (for example a `cancel` step).

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Commit | A successful execute with inserts and updates, then undo and redo | Same report text, doc and history as Rust | No error expected |
| Rollback | A failing execute (throw, bad node), then a retry with the returned `editId` | Same rollback text, doc unchanged, repair accepted | Rolled back |
| Finish | An execute that inserts icons, takes `TakeScreenshot`, and calls `Export` in each format | Same text, same written files, screenshots within tolerance | Same errors for refused paths |
| Script nodes | An execute that inserts a script node, then a screenshot of it | Same output once the node has run | No error expected |
| Generate | `Generate('ai', …)` against a local test server, success and failure | Same pending then resolved fill, and the same doc and history after resolve | Same failure handling |
| Gate and cancel | A long execute while gated edits arrive, and a cancel mid-run | Edits wait, cancel gives Rust's stopped text, doc rolled back | No deadlock, no race |

</intent-contract>

## Code Map

- `core/src/script.rs`:
  - `execute` 751, `prepare` and seed 791, `execute_shared` 810 (`EXECUTE_GATE`, cancel), the rollback wrapper 838, the report 864-915
  - `shot_tree`/`shot_target` 920/933, `n_generate` 316-340 (pending URL, inflight, download thread)
  - the transaction, repair, globals, screenshot and export tests (see 5b's list; port the engine-bound ones)
- `core/src/engine.rs`:
  - `ToolOut` and `finish` 41-150, `call_tool` 234, `begin_execute`/`end_execute` 1163-1188, `tool` 1191, `resolve_pending` 1233, inflight
- `core/src/html.rs:27-192` -- the export job (ported in 7c) as `ToolOut` uses it.
- `core-go/script` (`Execute`, `Host`, `Locker`, `Outcome`), `core-go/engine` (8a/8b, `e.Host()`, history, `ResolvePending`, `Inflight`), `core-go/export` (`Plan`, `Job`, `Waiter`, `PathPolicy`) -- reuse and extend.
- `core/src/bin/cli.rs` replay and `core-go/engine/replay.go` -- add `tool execute` (with args including `__seed`), and a `cancel` step if needed.

## Tasks & Acceptance

**Execution:**
- [x] `core-go/engine/execute.go` -- `ExecuteShared`, gate and cancel, commit and rollback, the editId store, begin/end events, the agent hooks.
- [x] `core-go/engine/toolout.go` -- `ToolOut` and `Finish` (script nodes, icon settle, waits, shots, export jobs).
- [x] `core-go/engine/generate.go` -- the Generate downloader, inflight, and resolution through `ResolvePending`.
- [x] `core-go/engine/host.go` -- the full `script.Host` and `Locker`.
- [x] `core/src/bin/cli.rs`, `core-go/engine/replay.go` -- the execute (and cancel) replay commands.
- [x] `core-go/engine/execute_test.go` -- the engine-bound Rust script/engine tests, plus each matrix row (concurrency under `-race`).
- [x] `core-go/testdata/replay/execute-*.jsonl` (+ goldens) -- sequences per matrix row (Generate through a local server only in Go tests, not in Rust replay).

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs (also with `GOAMD64=v3`, and `-race` on `./engine/ ./script/`), then it passes with golden replay parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./engine/` runs, then every execute replay sequence matches Rust as stated.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- Layout (the plan's `ExecuteShared` is `CallTool` with `script.Call.Locker`): `engine/execute.go` (`AgentHooks`, `beginExecute`/`endExecute`, `CallTool`, `execute`: the commit or rollback around `script.Execute`), `toolout.go` (`shotTree`, `planOutputs`, `ToolOut.Finish`, `settleIcons`, `WaitDownloads`), `generate.go` (the photo download), `host.go` (`Generate` starts it). `Tool("execute")` is engine.rs `tool` (the lock held across the JavaScript, "Another execute call is still running." while one runs); `CallTool` is `call_tool` (the edit gate, then the lock, released by `script.Execute` while the JavaScript runs). Answers carry their unfinished work in an unexported `pending`; `Finish(e)` does it without the lock.
- `script.Call.Locker`: `Execute` is entered with the lock held, releases it around `run` and takes it again; each native takes it for its own work (script.rs `with` without a bound engine), so `validate` and the rollback run under it. The rollback restores the root of `Host.Doc()` as it is after the run.
- `Generate`: the sandbox now sets the pending fill before `Host.Generate` starts the download, as Rust does (`update`, `version += 1`, `inflight`, spawn), so a bad target starts no download. The engine downloads `<ImageBase>/seed/<fxhash>/1600/1000` with `Assets.Client` through `svg.Fetch` (net.rs `get_bytes`, now exported) into `<doc folder>/images` (linked `./images/…`) or `<data>/images` (linked by its path with `/`); a panic counts as a failed download. Like Rust it ignores `Offline`, so no replay calls it; `TestGenerateDownloadsAndResolves` runs it against `httptest`.
- Agent hooks (inert when nil): `Stop` (the Stop channel of a call by an agent), `NoteFlagged`, `StartPen` (with `Agent.Stops` at the call's start and the frame's size), `ExportAllowed` (nil is `export.WritePolicy`, a port of agent.rs `path_allowed` for "Write"). Without `StartPen` nothing is pushed to `Agent.Pens`.
- Shared fixes: `export.Ctx` and `svg.NodeSVGAt`/`svg.Export` take the document's libraries (render.rs `Ctx` has them; Go resolved exports without them); export's `normalize` keeps an empty path empty, as Rust's `absolute("")` fails (a bare-name document's export base is "", inside which every path is).
- Replay: `tool` now goes through `CallTool` and `Finish` outside the lock, as lib.rs does. A `tool` step with `png: true` (both replays; the one Rust change) answers with its screenshots' names, `<step>.<k>.png`, written to the out folder. The parity test compares those, and written `.png` files, by decoded pixels (exact), `.jpeg` within 12 levels, `.pdf` by page count and sizes; goldens hold the PNGs as `<name>.<step>.<k>.png`. Sequences: `execute-commit` (every prelude edit, slides, undo/redo, globals per agent, events, a call inside a drag, refused arguments, a read-only call), `execute-rollback` (thrown errors, a bad node, a non-Error throw, clean stays clean, repairs: not found, ambiguous, `all`, both arguments, a forgotten editId), `execute-finish` (screenshots of a frame, `document`, a nested, a missing and a hidden node; Export in png, webp, svg, jpeg, pdf and html-css; refusals; a missing icon's warning; exporting a file again), `execute-scripts` (a script node's screenshot and export). All equal Rust's, byte for byte apart from the encoded images.
- Not replayed against Rust: cancel (Rust raises Stop only for a running built-in agent, which the replay has none of, so no `cancel` step was added; `TestARunningScriptLeavesTheEngineFreeAndStopsWithTheAgent` checks it with the `Stop` hook, together with the gate), Generate (network), and a refusal outside the document's folder (replays open bare file names, whose export base "" admits every path; `TestScriptExportNeverReplacesAFileItDidNotWrite` checks it).
- Script runs and their slots are process-wide, so a replay that runs a script node another sequence later renders can change that sequence's frames in the one Go test process (Rust runs each sequence in its own); `execute-scripts` uses its own node and input.
- Ported Rust tests (`execute_test.go`): bad children, transaction and repair, bulk insert slides, rolled-back slides, glass, blended inserts, semi-transparent ancestors, clean after rollback, per-agent globals, the running script with Stop, screenshots after the call and waiting for downloads, script exports after the call, waiting, and never replacing, shots and exports of slow script nodes off the lock. Streaming ones are 8d's; `ui_exports_*` need the dispatcher's `export` (8e).

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 2 findings — high 0, medium 1, low 1, false 0, maybe-false 0
- findings:
  - `[medium]` `[patch]` `Executing` is restored without `defer`. A panic in report or validate leaves it true for good, which blocks every later execute and doc switch. It also stays true during validate, where Rust has cleared it. — Fix: restore with `defer` and clear it where Rust does, with tests.
  - `[low]` `[patch]` The replay `png` builds `export.Ctx` without `Libs`, unlike `ToolOut` and Rust. — Fix: pass the libraries, with a test.

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./engine/ ./script/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-16, Windows, MinGW gcc, CLI rebuilt at `%TEMP%/nalarupa-go-target`):** `go vet ./...` and `go test ./...` pass with golden parity (19 replay sequences offline; `platform` and `editor-text` live only); `GOAMD64=v3 go test ./...` passes on the whole module; `go test -race ./engine/ ./script/` passes. Live, `NALARUPA_CLI=… go test ./...` passes on every package: the four execute sequences equal Rust's responses, documents, events and written files, their screenshots and PNG exports by exact pixels, the JPEG within the tolerance, the PDF by pages. `cargo test --release` passes (292 tests). Not run on Linux. Seen once under load, before `execute-scripts` was isolated: `sync` step 41's frame differed when another sequence had already run its script node in the same process (see the notes).

## Auto Run Result (orchestrator finalize)

- **Summary:** `execute` runs in the Go engine:
  - `CallTool` takes the edit gate and unlocks while the JS runs (`script.Call.Locker`), and `Tool("execute")` holds the lock
  - commit and rollback, the editId store, events
  - `ToolOut`/`Finish` with the lock released (script nodes, icon and download waits, screenshots, export jobs)
  - the Generate downloader resolving through `ResolvePending`
  - inert agent hooks (`Stop`, `NoteFlagged`, `StartPen`, `ExportAllowed`) with `export.WritePolicy`

  `export.Ctx`, `svg.NodeSVGAt` and `svg.Export` now take libraries.
- **Review:**
  - Patches applied: 2 (1 medium, 1 low). `Executing` is cleared at Rust's point through `Call.Ran` plus a `defer`, and the replay `png` passes `Libs`.
- **Follow-up review recommended:** false (patched: 0 high, 1 medium, 1 low).
- **Verification:**
  - `go vet` and `go test ./...` pass offline.
  - `GOAMD64=v3` and `-race` on `./engine/` and `./script/` pass.
  - Live parity passes on every package, with the four execute sequences byte-identical (`engine`, `editor` and `script` re-run live after the patches).
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - Cancel and Generate are tested in Go only (Rust would need a running agent, or the network).
  - The process-wide script cache couples replays within one test process.
  - The `sync` replay flaked once under load.
  - Linux has not been run.
