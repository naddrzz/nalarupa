---
title: 'Go rewrite phase 8a: engine core and replay oracle'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'in-progress'
baseline_revision: ''
route: 'full'
route_source: 'auto'
risk: 'high'
review: ''
review_source: ''
lenses_ran: []
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
- [ ] `core/src/lib.rs`, `core/src/bin/cli.rs` -- a test-only `replay` command using the existing dispatcher (no behaviour change).
- [ ] `core-go/engine/engine.go` (state, locks, `New`, `Offline`, events), `files.go` (open/save/templates/recents/relocation), `settings.go`, `history.go`, `sync.go` (sync, slides, glass), `callbacks.go`, `tools.go` (non-execute tools, `tool_specs`, editor state), `host.go` (`script.Host`).
- [ ] `core-go/engine/replay.go` -- a Go replay driver handling the 8a command subset (`new`, `open`, `save`, `state`, `select`, `update`, `insert`, `delete`, `undo`, `redo`, `drag_begin`/`drag_end`, `set_variable`, `settings_get`/`settings_set`, `tool` for non-execute tools, `render`), keyed like Rust's dispatcher.
- [ ] `core-go/engine/engine_test.go` -- the 26 Rust engine tests (those not needing editor/execute/streams/agent) plus each matrix row.
- [ ] `core-go/engine/replay_parity_test.go`, `core-go/testdata/replay/*.jsonl` -- sequences per matrix row run through both replays (goldens without the CLI, live with it).

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs (also with `GOAMD64=v3`, and `-race` on `./engine/`), then it passes with golden replay parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./engine/` runs, then every replay sequence gives equal responses, documents, saved files and frames.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

## Plan Change Log

## Review Triage Log

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./engine/` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./...` -- expected: pass, live (on an idle machine)
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)
