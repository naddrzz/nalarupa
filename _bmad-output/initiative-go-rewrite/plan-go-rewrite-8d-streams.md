---
title: 'Go rewrite phase 8d: streaming execute previews'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: 'a5744a1e2d911f50ed501fa17bbec70d69e8b036'
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
deferred: []
---

<intent-contract>

## Intent

**Problem:** While the agent is still writing an `execute` snippet, the editor previews it live. The Rust core cuts the streamed source at the last safe statement boundary (`stream.rs`), runs only the new chunk in a long-lived JS context against a preview copy of the document, and reuses the stream's seed in the final `execute` so the preview's node ids survive. The Go engine has none of this, so previews would be empty and ids would change when the call lands.

**Approach:** Port the streaming half:
- to `core-go/script`: `partial_string` (the unterminated `"input"` of partial tool JSON), `complete_prefix` with its string, template, `${}`, comment and continuation rules, and `stream_step` running appended chunks in a live context with seeded `resume_ids`/`ids_drawn`
- to `core-go/engine`: `stream_update`, `stream_end`, `take_stream` (match by key, else by prefix), `rebuild_preview` re-running all streams on a clone when the source is not a pure append, the preview doc swap in `Sync`, and the final `execute` reusing the stream's `__seed`. Generate is skipped in preview mode.

A test-only replay extension drives `stream_update`/`stream_end` directly on both cores, because Rust only reaches them through the agent.

## Boundaries & Constraints

**Always:**
- `partial_string` and `complete_prefix` return exactly what Rust's do (byte offsets and strings) for every input, including truncated escapes, nested templates, regex-like slashes, comments and continuation tokens (`else`/`catch`/`finally`, operators, `.`).
- For the same sequence of stream updates and the final execute, the preview document after each update, the rendered preview frames, and the final document and ids match Rust. That includes the resume-probe id sequence, which closes 5b's deferred comparison.
- The live JS context lives on one goroutine, as 5a2 requires, takes the engine lock only through the `Locker`, and is torn down with the stream. Preview runs use the 2 s budget.
- Reuse `core-go/script` (runtime, natives, `IDSource` with `Drawn`/`Resume`) and `core-go/engine`. No duplicated logic. Floating-point products get explicit conversions (no FMA).

**Never:**
- No agent loop, providers or SSE parsing (phase 9). Only the engine-side stream API phase 9 will call.
- No C ABI (8e). No change to `app/` or Rust behaviour apart from test-only replay steps.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Prefix rules | Partial snippets cut inside strings, templates with `${}` nesting, comments, before `else`/`catch`/`.`/operators, at `;` and newlines | Same prefix length as Rust | No error expected |
| Partial JSON | Tool-input JSON truncated at every byte of a sample | Same `partial_string` result | `None` where Rust gives `None` |
| Append streaming | A snippet streamed in growing chunks that insert, update and name nodes | Same preview doc and frames after each update, and the final execute keeps the preview's ids | No error expected |
| Rewrite | A stream whose source changes before the end (not an append) | Preview rebuilt from scratch, same as Rust | No error expected |
| Several streams | Two concurrent streams, take by key and by prefix | Same previews and final docs | No error expected |
| Errors in preview | A chunk that throws, an endless loop (2 s), `Generate` in a preview | Same preview state (errors don't commit), Generate skipped | No crash or leak |

</intent-contract>

## Code Map

- `core/src/stream.rs` -- `partial_string` 7, `complete_prefix` 63, `continues` 143, tests (2).
- `core/src/script.rs` -- `resume_ids`/`ids_drawn` 688-702, `stream_step` 710, `reset_streams` 746, preview-mode Generate skip 316, the streaming tests 2154-2783.
- `core/src/engine.rs` -- streams 577-674 (`stream_update`, `stream_end`, `take_stream`, `rebuild_preview`), `sync`'s preview doc choice 475, and the streaming engine test 8a left out.
- `core/src/agent.rs:1045, 1328` -- how the agent feeds `partial_string`/`stream_update`. The call shape phase 9 will use.
- `core-go/script` (`Execute`, runtime, `IDSource`), `core-go/engine` (`Sync`, `Preview`, execute from 8c, replay) -- reuse and extend.
- `core/src/bin/cli.rs` replay and `core-go/engine/replay.go` -- add `stream_update {key, source}` and `stream_end {key}` steps calling the engine directly (test-only).

## Tasks & Acceptance

**Execution:**
- [x] `core-go/script/stream.go` -- `PartialString`, `CompletePrefix`, the live stream context, and `StreamStep`.
- [x] `core-go/engine/streams.go` -- `StreamUpdate`, `StreamEnd`, `TakeStream`, `RebuildPreview`, the preview swap, and the seed reuse in execute.
- [x] `core/src/bin/cli.rs`, `core-go/engine/replay.go` -- the stream replay steps.
- [x] `core-go/script/stream_test.go` -- the Rust stream.rs tests plus exhaustive prefix and partial tables, plus a Rust oracle for both functions (a test-only CLI `stream-prefix` mode, or the replay) over a generated corpus.
- [x] `core-go/engine/streams_test.go` -- the Rust streaming tests (script.rs 2154-2783 and the engine one) plus each matrix row.
- [x] `core-go/testdata/replay/stream-*.jsonl` (+ goldens) -- sequences per matrix row.

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs (also with `GOAMD64=v3`, and `-race` on `./engine/ ./script/`), then it passes with golden stream parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./engine/ ./script/` runs, then every stream sequence and prefix/partial case matches Rust.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- `script/stream.go`: `PartialString` and `CompletePrefix` (byte offsets, Rust's `is_ascii_whitespace` without vertical tab, `char::is_alphanumeric` as letters, numbers and Other_Alphabetic; `\u` escapes parsed as `u32::from_str_radix`, a leading `+` included, and the surrogate sum with wrapping u32 arithmetic as the release build computes it). `Live` is one stream's context: its runtime is made, used and freed on a goroutine of its own (`qjs` locks it to its thread), and each step is sent to it while the caller, holding the engine lock, waits; the natives use the host directly (script.rs `bind` with the engine bound), never the lock. `StreamStep` is script.rs `stream_step`: a stalled stream answers its touched ids, a broken or non-append one is closed and answers false, a new one gets a sandbox with the agent's globals; a chunk runs with `PreviewTimeout`, ids resumed with `IDSource.Resume` and counted with `Drawn` on the host's generator, and a native's panic is an error that breaks the stream. A NUL byte breaks it too (rquickjs refuses the source).
- `engine/streams.go`: `Stream`, `Engine.Streams`, `StreamUpdate`, `StreamEnd`, `StreamsOfAgentEnd`, `takeStream`, `resetStreams` and `rebuildPreview`, with the document and preview swapped by `onPreview` (restored by `defer`) and `Executing` raised around each step. `Touch` and a moving `Save` rebuild the preview; `resetHistory` closes the live contexts before forgetting the script state. `beginExecute` takes the stream and passes its seed as `__seed`, its touched count as `seen` and its agent when the call has none.
- Oracle: `nalarupa-cli stream-prefix <cases.json>` (test-only) answers `complete_prefix`/`partial_string` per case. `TestStreamParity` builds 2785 cases (every char-boundary prefix of six JS and five JSON samples, 1500 random token soups, 500 random partial strings); all equal Rust's, golden in `testdata/stream/golden.json`. Replay steps `stream_update {key, agent?, source}` and `stream_end {key}` (both replays) answer the streams (key, agent, source, seed, touched), `version`, `dirty`, `executing` and the preview root.
- Sequences: `stream-append` (growing chunks with frames and events, a user insert drawing its id where the stream's id probe left the sequence, the final call keeping the ids, undo/redo), `stream-rewrite` (non-appends, a throwing first statement, whitespace-only growth, end twice), `stream-several` (two agents, a user edit while streaming, keyless by prefix among the caller's own, keyed, keyless without an agent), `stream-errors` (hoisting after a throw, a real error and the rolled-back final call, an endless loop stalling at 2 s, Generate skipped, save as relocating the preview). All equal Rust's lines, frames and files.
- Tests: the ported script.rs streaming tests (glass by a streamed update, preview then commit, keyless and keyed takes, previews are not edits, a stream at the memory limit stalls, incremental equals cold, statement error midway) and engine.rs `save_as_moves_the_live_preview` in `engine/streams_test.go`, plus a rewrite, endless loop and Generate, reopening, and a concurrency test (renders and a shared execute while two streams update, no context or goroutine left). The panic test is `script`-level (`__PANIC` is only exported to `script`'s tests), with `StreamStep` resume and NUL tests and Generate skipped.
- Known divergence: Rust's id generator is per thread, so a stream step on the agent's thread never moves the ids of a shared `execute` running on another thread. Go steps on the engine's one generator (needed for single-thread replay parity), so a stream updating while another call's script runs unlocked moves that call's id sequence; a seeded final call then gets ids other than its preview's. Ids stay unique; phase 9 may give each run its own generator if that matters.

## Plan Change Log

- 2026-11-16: the whole-module run failed timing-bound off-the-lock tests under parallel load (`TestAgentShotsAndExportsRunScriptNodesOffTheLock`, `TestLiveScriptRunsOffTheLock`, `TestExportsRunScriptNodesOffTheLock`, `TestWorkersAndEviction`, `TestSlots`). Not a regression: with no stream open, 8d adds only an empty `takeStream` scan to an execute and nothing to script node runs, Sync or Finish, and the same command passed at a5744a1; the new streaming tests (a 2 s endless loop, a 64 MB allocation, a render loop) only add load next to them. Those tests (plus `TestRecordMode`, the lock check of `TestARunningScriptLeavesTheEngineFreeAndStopsWithTheAgent` and the stalled-stream check of `TestEndlessLoopsAndGenerateInAPreview`) no longer time anything: `script.SetRunHook` (called with a script's source before each script node run) lets a test hold the runs whose source carries its marker, so "does not wait for the run" is shown by returning while the run is held, and "the lock is free" by taking it from another goroutine while the run is held; a wrong implementation hangs into a one-minute failure instead of passing slowly.

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 5 findings — high 0, medium 1, low 2, false 1, maybe-false 0
- findings:
  - `[false]` `[reject]` No `GOAMD64=v3` result is recorded. — The orchestrator ran `GOAMD64=v3 go test -count=1 ./...` on the whole module after the test fixes, and it passed (also live with `NALARUPA_CLI`).
  - `[medium]` `[patch]` A stream step during another call's shared execute moves the engine id generator, so that call's committed ids no longer match its preview. — Fix: save and restore the generator around each stream step, with a mid-execute test.
  - `[low]` `[reject]` Stream steps (up to 2 s) and preview rebuilds run under the engine lock, against AGENTS.md's "never hold `engine()` across slow work". — Rust does the same (`stream_update`/`rebuild_preview` under the lock), and the plan requires that behaviour. Changing it would diverge from the Rust core. Revisit after cutover if UI stalls show up.
  - `[low]` `[patch]` `holdRuns`/`within` duplicate script's test helpers. — Fix: one shared test-support location.
  - `[low]` `[patch]` `StreamsOfAgentEnd` is untested. — Fix: add a test.

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./engine/ ./script/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-16, Windows, MinGW gcc, CLI rebuilt at `%TEMP%/nalarupa-go-target`):** `go vet ./...` and `go test ./...` pass with golden parity (the stream corpus and the four stream sequences offline). Timing-bound off-the-lock tests failed under whole-module load; after they were made deterministic (Plan Change Log), `go test -count=1 ./...` on the whole module passed three runs in a row. `go test -race ./engine/ ./script/` passes. Live, `NALARUPA_CLI=… go test ./...` passes on every package: 2785 prefix/partial cases and the four stream sequences equal Rust's lines, frames and files. `cargo test --release` passes (292 tests). Not run on Linux.

## Auto Run Result (orchestrator finalize)

- **Summary:** streaming execute previews:
  - `script.PartialString`/`CompletePrefix` (byte-faithful), `Live` contexts on their own goroutines, and `StreamStep` with seeded `Resume`/`Drawn`
  - `engine.StreamUpdate`/`StreamEnd`/`StreamsOfAgentEnd`/`takeStream`/`rebuildPreview` with the preview swap, and seed reuse in execute
  - test-only `stream-prefix` and the `stream_update`/`stream_end` replay steps

  The off-the-lock tests now use deterministic `SetRunHook` + `script/scripttest` synchronization instead of timing.
- **Review:**
  - Patches applied: 3 (1 medium, 2 low). A stream step mid-shared-execute no longer moves the call's ids (the generator state is saved and restored when `Executing` is set, which keeps replay parity), test helpers are unified in `scripttest`, and `StreamsOfAgentEnd` has a test.
  - Rejected: 1 low (stream work under the engine lock mirrors Rust) and 1 false (v3 was run).
- **Follow-up review recommended:** false (patched: 0 high, 1 medium, 2 low).
- **Verification:**
  - Three consecutive whole-module `go test ./...` runs pass after the test rewrite.
  - `go vet` and `go test ./...` pass offline, and `GOAMD64=v3 go test ./...` passes.
  - `-race` on `./engine/` and `./script/...` passes.
  - Live parity passes on every package: 2785 prefix/partial cases and 4 stream sequences.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - Stream steps hold the engine lock for up to 2 s each, as Rust does.
  - Never-ended streams keep their goroutines.
  - Linux has not been run.
