---
title: 'Go rewrite phase 8a: engine core and replay oracle'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: 'a66b8f1e5d69a8e63278cc8d85f61935e0410f90'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-4e-canvas.md'
warnings: ['oversized']
deferred: []
---

<intent-contract>

## Intent

**Problem:** Every editor action and tool goes through the Rust `Engine` (`core/src/engine.rs`): document state, history, open/save, settings, sync and slides, and the download callbacks. The Go core has every subsystem but no engine to hold and drive them, so nothing above it (editor ops, execute integration, streams, the C ABI) can be built.

**Approach:** Port the engine core to a new `core-go/engine` package:
- the `Engine` state (doc, libraries, fonts, assets, renderer, version/synced/saved, undo/redo stacks capped at 100, selection, events capped at 2000, data dir, settings, viewport, reduce motion, coalescing, drag state, inflight downloads)
- `New`, `Offline`, `Open` (migrate, ensure ids, drop pending and placeholder, load libraries, recents), `LoadTemplate` (blank/slides/mobile/empty), `Save` (save-as relocation including history, image localization, atomic settings), and settings with rollback (`set_setting`, `save_settings`, `api_key`/`backend` reads)
- `Sync` with `unslide_glass`, `shift_siblings`, `settle_covered`, `refresh_slides`, `slide_in`, `forget_gone_slides`, `expire_slides` (watching script-node completion)
- history: `commit` with the mid-drag `merge3`, `end_drag`, `undo`/`redo`, `edit`, `edit_or_drag`, `edit_coalesced`, `batch`, `keep_clean`, `clean_if_saved`, `mark_saved`
- the callbacks `font_ready`, `asset_ready`, `resolve_pending`, `apply_generated`, and `settle_generating`
- the non-execute tools (`get_editor_state` markdown, `read_guide`, `get_style`, `open_document`) and `tool_specs`

There is one engine mutex with an edit-gate mutex, mirroring Rust. Agent state is a stub (running, pens) until phase 9. A replay oracle runs the same JSON command sequences through Rust's FFI dispatcher and through the Go engine, and compares the results.

## Boundaries & Constraints

**Always:**
- For the same command sequence and seed, Go and Rust give equal responses (JSON), equal document JSON, equal saved files and the same history behaviour (undo/redo results), plus equal rendered frames where a sequence renders. Random ids are pinned by a seed on both sides.
- Concurrency mirrors Rust: one engine `sync.Mutex`, an edit gate around the commands Rust gates (`lib.rs:91-96`), and download goroutines that take the engine lock to deliver results (fonts, assets). Every goroutine the engine starts recovers panics, as Rust's thread boundaries do.
- `Engine` implements `script.Host` for the parts 8a owns (`Doc`, `Changed`, `SlideIn`, `LaidOut`, `FindEmptySpace`, `Fonts`, `Assets`, `IDs`). `Generate` returns Rust's not-available text until 8c.
- Settings and data-dir file layouts match Rust's exactly, since the Go and Rust cores share one data folder.
- Floating-point products get explicit conversions (no FMA). Reuse every existing `core-go` package. No package gets duplicated logic.

**Never:**
- No editor ops (8b), execute transaction/ToolOut finish/execute_shared (8c), streams (8d), C ABI or DLL (8e), or agent, providers or MCP (phase 9).
- No change to `app/` or Rust behaviour apart from a test-only replay command.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Documents | new, open (old version, pending images, placeholders, libraries), load each template, save, save-as to another folder with relative images | Same doc JSON, saved bytes, relocated paths, recents | Same error text for a missing or broken file |
| History | Edits, coalesced edits, a drag with a commit mid-drag (`merge3`), undo/redo past both ends, more than 100 steps | Same doc after every step, same dirty state | No error expected |
| Sync and slides | Inserts beside siblings, slide start/settle/expire with a pinned clock, glass (background-blur) nodes, script-node completion | Same rebuilt ids, slide states and frames | No error expected |
| Callbacks | font_ready/asset_ready on a clean and on a dirty doc, resolve_pending for an image, apply_generated | Same doc, history and dirty flag | No error expected |
| Settings | set, invalid set with rollback, read masked values | Same settings file and responses | Same error text |
| Tools | get_editor_state, read_guide, get_style, open_document | Same text output | Same error text |
| Concurrency | Downloads finishing while edits run, under `-race` | No race, consistent state | No panic escapes |

</intent-contract>

## Code Map

- `core/src/engine.rs`:
  - struct 152-197, global and poisoned-lock recovery 207-212, `call_tool` 234 (execute part is 8c), `new` 426, `offline` 469
  - `sync`, slides and glass 475-570, streams 577-674 (8d), `commit` 683, history 791-880, files 920-1060, `get_editor_state` 1069
  - `begin_execute`/`end_execute` 1163-1188 (8c), `tool` 1191, `resolve_pending` 1233, `font_ready`/`asset_ready` 1263-1272, `apply_generated` 1282, `tool_specs` 1329
  - tests (26)
- `core/src/lib.rs:30-300` -- the dispatcher. 8a needs it only as the replay oracle; the command shapes are what the replay files use.
- `core/src/guide.rs` and `core/guide/*.md` -- `read_guide`/`get_style` content (`core-go` reads the same `core/guide` files, embedded, keeping their text exact).
- `core-go/render` (Renderer, slides, `Ctx`), `core-go/history` (patches, `same`), `core-go/doc`, `core-go/export`, `core-go/script` (`Host`, script nodes, `Finished`), `core-go/text`, `core-go/svg` -- reuse.
- `core/src/bin/cli.rs` -- add `replay <script.jsonl> [--seed n] [--data dir]`: one `{cmd, ...}` per line through `lib.rs`'s dispatch (reachable as a pub fn, or a test-only `dispatch` re-export), printing each response and finally the doc JSON (and frames for `render` lines).

## Tasks & Acceptance

**Execution:**
- [x] `core/src/lib.rs`, `core/src/bin/cli.rs` -- a test-only `replay` command using the existing dispatcher (no behaviour change).
- [x] `core-go/engine/engine.go` (state, locks, `New`, `Offline`, events), `files.go` (open/save/templates/recents/relocation), `settings.go`, `history.go`, `sync.go` (sync, slides, glass), `callbacks.go`, `tools.go` (non-execute tools, `tool_specs`, editor state), `host.go` (`script.Host`).
- [x] `core-go/engine/replay.go` -- a Go replay driver handling the 8a command subset (`new`, `open`, `save`, `state`, `select`, `update`, `insert`, `delete`, `undo`, `redo`, `drag_begin`/`drag_end`, `set_variable`, `settings_get`/`settings_set`, `tool` for non-execute tools, `render`), keyed like Rust's dispatcher.
- [x] `core-go/engine/engine_test.go` -- the 26 Rust engine tests (those not needing editor/execute/streams/agent) plus each matrix row.
- [x] `core-go/engine/replay_parity_test.go`, `core-go/testdata/replay/*.jsonl` -- sequences per matrix row run through both replays (goldens without the CLI, live with it).

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs (also with `GOAMD64=v3`, and `-race` on `./engine/`), then it passes with golden replay parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./engine/` runs, then every replay sequence gives equal responses, documents, saved files and frames.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- Layout: `engine/engine.go` (state, the lock and edit gate, `New`, `Offline`, `Seed`, `DataDir`, `Global`, events), `history.go` (commit with `merge3`, drag, undo/redo, edit/coalesced/batch, clean state), `sync.go` (sync, slides, glass, `ExpireSlides`, `Render`), `files.go` (open, templates, libraries, save and save as, image localization, relocated history, recents, Rust `Path` helpers), `settings.go`, `callbacks.go`, `tools.go` (editor state, tools, `ToolSpecs`), `host.go`, `replay.go` (the 8a subset of `dispatch` plus `Replayer`). `core-go/guide` ports guide.rs; its topics and the engine's templates are copies of `core/guide/*.md` and `core/templates/*.json`, kept byte-equal by tests (Go cannot embed files outside the module).
- `script.Host` is implemented by `e.Host()`, an adapter over the engine: the engine's `Doc`, `Fonts` and `Assets` fields would clash with the interface's method names. `Host.Generate` now returns an error: the engine's answers Rust's "Generate type '…' is not available" text, so the native fails the call and sets no pending fill, and `Tool("execute")` answers "unknown tool: execute" until 8c.
- Locking: callers hold `Lock`; `Locked` releases on panic. Font and asset `OnReady` callbacks run on the download goroutines and deliver through `guard` (lock plus recover). The edit gate is a second mutex taken before the lock for the commands `lib.rs` gates.
- Rust: `lib.rs`'s `dispatch` is already public, so only `cli.rs` gains `replay`; `Engine::slide_in` starts a slide at `renderer.now()` (the wall clock unless a replay pinned it; no behaviour change otherwise).
- Parity fixes outside the engine: `jsonv.Parse` now reports serde_json's own message and byte column (`serdeCheck`, a port of serde's value reader) and refuses what serde refuses, such as out-of-range numbers; script's `serdeError` approximation is gone. `doc.IOError` writes OS errors as Rust's `io::Error` does (`... (os error N)`; Go's own EISDIR is reported as Windows' access denied), used by `doc.Load`/`Save` and the engine; `doc.Load` rejects invalid UTF-8 with `read_to_string`'s text; `doc.RemoveFile` never removes a folder (a failed save had deleted a folder in its temp file's place).
- Paths follow Rust's `Path` through shared helpers in `core-go/doc/path.go` (`JoinPath`, `ParentPath`, `PathComponents` with upper-cased drive letters, `SamePath`), which replaced export's private `join`/`parent` and the engine's copies; `doc.CreateDirAll` ports `create_dir_all` for saves; font and asset download goroutines recover a panic and report a failed download; Windows system messages come from FormatMessageW with langid 0, as Rust's.
- Replay: one `{cmd}` per line; besides the dispatch commands, `render`, `slide_in`, `tick`, `settle`, `font_ready`, `asset_ready`, `inflight`, `resolve_pending`, `apply_generated`, `doc` and `copy`. Both sides run in a fresh copy of `testdata/replay/files` as the working folder with `--data data` and seed 42; responses, the final document line, frames and every written file (saved documents, copied images, settings.json) compare byte for byte. Seven sequences cover the matrix; `platform.jsonl` (`# live`) holds system error text, the guide's line endings and text layout, and runs live only. Goldens are `testdata/replay/golden` (about 250 KB).
- Concurrency: `TestDownloadsDeliverUnderTheLock` delivers 100 font and asset results from goroutines while edits and syncs run, under `-race`, and checks a panicking delivery leaves the lock free.
- Ported Rust tests: 25 of 26 (`save_as_moves_the_live_preview` needs 8d's streams); the Fake-font case uses a fresh family name since Go's font database cannot drop a system face.

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 4 findings — high 0, medium 3, low 1, false 0, maybe-false 0
- findings:
  - `[medium]` `[patch]` The font and asset download goroutines don't recover panics, so one bad download kills the process, where Rust only loses the thread. — Fix: recover and report it as a failed download, with a test.
  - `[medium]` `[patch]` The engine copies path helpers (`join`, `parentOf`, `components`), and the copies diverge from Rust: a rooted path without a volume, trailing `.` components, and the drive-letter case. — Fix: reuse the Rust-faithful shared helpers and delete the copies, with tests.
  - `[medium]` `[patch]` `Generate` does nothing, while the native tells the agent the image is loading, so a pending URL never resolves. The intent asks for Rust's not-available text until 8c. — Fix: report not-available and insert no pending fill.
  - `[low]` `[patch]` `doc.IOError` differs from Rust when a path component is a file (`MkdirAll` vs `create_dir_all`), and Windows asks for en-US message text instead of the user's language. — Fix: port `create_dir_all` and use `FormatMessageW` with language 0.

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./engine/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

**Results (2026-11-16, Windows, 12 logical CPUs, CLI at `%TEMP%/nalarupa-go-target`):** `go vet ./...` and `go test ./...` pass with golden replay parity (six sequences; `platform` is live only); `GOAMD64=v3 go test` passes on `./engine/`, `./jsonv/`, `./doc/` and `./guide/`; `go test -race ./engine/` passes, also live. Live, all of `go test ./...` passes against the rebuilt release CLI: all seven replay sequences give byte-identical responses, final documents, frames and written files, and the other packages' parity suites still pass after the jsonv, doc and script changes. `cargo test --release` passes (292 tests). Not run on Linux (`./doc/`, `./jsonv/` and `./guide/` vet clean for `GOOS=linux`).

## Auto Run Result

- **Summary:** `core-go/engine` is the engine core:
  - state, the engine mutex and edit gate, `New`/`Offline`, events
  - open, save and save-as with relocation, templates, recents, settings with rollback
  - history (commit with `merge3`, coalesce, drag, batch, undo/redo, dirty tracking)
  - `Sync` with slides and glass
  - the font, asset, pending and generated callbacks
  - the non-execute tools and `tool_specs`
  - a `script.Host` adapter, where `Generate` reports not-available until 8c

  `core-go/guide` embeds the guide content and the engine embeds templates, with drift tests. Shared fixes match Rust's error text: serde-faithful `jsonv.Parse`, `doc.IOError` (`FormatMessageW` language 0), `CreateDirAll` (a `create_dir_all` port), a safe `RemoveFile`, and the shared Rust-faithful `doc` path helpers (`JoinPath`, `ParentPath`, `PathComponents`, `SamePath`). Rust gains a test-only `replay` command, and `slide_in` uses `renderer.now()`.
- **Review:**
  - Patches applied: 4 (3 medium, 1 low). Download goroutines recover, duplicate path helpers are replaced, `Generate` reports not-available, and the I/O error text is corrected.
  - Rejected: none.
- **Follow-up review recommended:** false. Three medium patches meet the count threshold, but each has a deterministic test (panicking downloads, path cases, the generate error) and the full suite, `-race` and live replay re-ran clean, so no unverified risk can be named.
- **Verification:**
  - `go vet` and `go test ./...` pass offline, and `GOAMD64=v3 go test ./...` passes on the whole module.
  - `-race` on `./engine/` passes.
  - Live parity passes on every package, and 7 replay sequences are byte-identical.
  - `cargo test --release` passes, 292 tests.
- **Residual risks:**
  - Go keeps number text exactly as written in loaded files, where Rust rewrites it (deferred).
  - Linux error capitalization is approximate.
  - Goldens assume 12 CPUs.
  - Linux has not been run.
