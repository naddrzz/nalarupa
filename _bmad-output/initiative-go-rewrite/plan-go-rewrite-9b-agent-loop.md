---
title: 'Go rewrite phase 9b: agent loop, chats and Claude Code'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: '5754cf3'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-9a-model-clients.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8e-c-abi.md'
warnings: []
deferred:
  - summary: >-
      Linux has not been run for the agent package (the stand-in CLI, signals table, process kill).
    evidence: |-
      Only Windows was verified. Cover in the Linux CI run at cutover.
    location: >-
      core-go/agent
    severity: medium (unverified)
---

<intent-contract>

## Intent

**Problem:** The Go core cannot run the design agent. `core/src/agent.rs` (1943 lines) and `core/src/claude_code.rs` (170) are unported, and neither is the agent half of `engine.rs`/`script.rs` (the `emit` → `record` → `save_chat` hook, plus `note_flagged` and `stopped_since`). These cover:
- the run loop over the API, provider and Claude Code backends
- queueing, Let it cook, `spawn_agents` children, permission requests and stop
- chat persistence
- the agent instructions
- the Claude Code CLI account commands

**Approach:** Port both files into Go, line by line, as a new package `core-go/agent`, or inside `core-go/engine` if import cycles force it. Grow `engine.AgentState` to Rust's full state and fill `AgentHooks` (`Stop`, `NoteFlagged`, and `StartPen` left as a stub until 9c). Wire `Engine.Emit` to record chats.

Expose Go functions, one per command, for 9d to dispatch:
- `agent_send`, `agent_stop`, `agent_reset`, `agent_instructions`, `agent_permission`
- `claude_auth_status`, `claude_auth_login`, `claude_auth_cancel`, `claude_auth_logout`
- `chat_list`, `chat_load`, `chat_delete`, `chat_clear`, `chat_flush`

Also export what `mcp.rs` (9c) needs from the agent: `spawn_children`, `ask_permission`, `spawn_spec`, `PERMISSION_TOOL`, and the per-run MCP config writer and sweep. The model calls use `core-go/model` from 9a. Port every Rust test in agent.rs (about 25) and claude_code.rs; they are the oracle.

## Boundaries & Constraints

**Always:**
- Same events, with the same fields and order: `queued`, `user`, `start`, `text_delta`, `thinking_delta`, `tool_start`, `tool_progress`, `tool_done` (output clipped at 6000, at most 3 images), `message_end`, `note`, `error{text, signIn}`, `done`, `all_done`, `dequeued`, `permission`, `permission_done`, `todos`, `external`. Same error clipping and wording.
- Same threading semantics, with goroutines in place of threads:
  - one goroutine per run and per child, at most 5 children
  - a fresh cancel flag each turn; the SSE reader polls it (9a)
  - the Claude Code process is killed by a watcher polling every 150 ms
  - `stop()` bumps `stops`, cancels pens, drops the queue, denies pending permissions and ends streams
  - 120 ms stream throttle and 300 ms progress throttle
- Never hold the engine lock across model calls, process I/O or file writes (AGENTS.md). The `chat_ops` mutex is always taken before the engine lock.
- Chats are stored as `<data_dir>/sessions/<id>.json`:
  - the shape is `{version:1, id, title, updatedAt, agent, events, messages, session}`
  - writes are atomic (`.json.tmp` then rename), done by one background writer that keeps only the latest snapshot
  - files untouched for 20 days are swept when the list is read
  - ids must be alphanumeric
  - the JSON bytes are the same as Rust's (key order) where a test or another reader compares them
- `agent_instructions` returns byte-identical text to Rust for every backend. This stands in for the design-eval run AGENTS.md asks for, because the instructions are unchanged. Check it live against the Rust CLI the same way earlier phases did, adding a test-only `agent-instructions` subcommand to `core/src/bin/cli.rs` if none exists.
- Claude Code backend: the same argv (flags, `--mcp-config` file, `--resume`, `--permission-mode auto`, `--model`, `--settings {"fastMode":true}`), the prompt on stdin from a writer goroutine, one retry with a fresh session on a missing session, and per-run `claude-code-mcp-*.json` files swept after a day. The MCP URL comes from the engine's MCP port, which stays 0 until 9c/9d. Read it through an accessor so 9c can set it.
- Every goroutine recovers panics: a panic during a run ends that run with an `error` event and `done`, as Rust's run thread does, and never crashes the process. 9a's model package can panic on malformed stream JSON.
- The general `path_allowed`/`normalize`, symlink-aware as in agent.rs, used by permissions, html.rs and export, are ported if not already in Go. Reuse `export.WritePolicy` where it is the same function.

**Never:**
- No MCP HTTP server, AI Pen drawing or dispatch table changes (9c, 9d). Functions are written and tested here and dispatched in 9d.
- No change to what the agent is told: guide files, instructions and tool specs.
- No change to Rust behaviour, except a test-only CLI oracle subcommand.
- No real model or network calls in tests: use the 9a `httptest` fake server and a stand-in `claude` script, as Rust's tests do (`.cmd` on Windows).

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| API run | a fake server replying text plus an `execute` tool call | Events as Rust; the tool runs through `CallTool`; the turn loop continues until end_turn | Model errors become an `error` event, clipped |
| Queue/stop | send while busy, then stop | `queued`, then `dequeued`/stopped events; pending permissions denied | – |
| Children | `spawn_agents` with N up to 5 | children run in parallel with their own `start{child}` | over-limit → Rust's error |
| Permissions | allow, deny, auto | `permission`/`permission_done`; auto persists the setting | Stop denies |
| Chats | send, reload `chat_list`/`chat_load`, delete, clear, 20-day sweep, a bad id | Same JSON as Rust | Bad id → Rust's error |
| Claude Code | a stand-in CLI emitting stream-json; a missing-session reply; a kill on stop | Same events and argv; retry once | Not installed → Rust's message/`signIn` |
| Auth | status parse, login/cancel/logout with the stand-in | Same shapes and `claude_auth` events | – |
| Instructions | each backend | Byte-identical to the Rust CLI | – |
| Panic | a malformed stream event that panics | `error` + `done` events, process alive | – |

</intent-contract>

## Code Map

- `core/src/agent.rs` (1943 lines), including the run spawn (335), `stop` (475), the chat writer (651-703), the request body (1016-1029), the Claude Code run (1227-1270), and the tests (1464-1943).
- `core/src/claude_code.rs` (170 lines), with its tests at 107-170.
- `core/src/engine.rs:897-900`: `emit` → `agent.record`.
- `core/src/script.rs:846,858-860`: `note_flagged`, `stopped_since`, `pen::start`.
- `core/src/html.rs:46,57`: `path_allowed` and `normalize`.
- `core/src/lib.rs:407-521`: how each command calls into the agent. This is for the function signatures only; dispatch is 9d.
- Go code to reuse:
  - `core-go/engine/engine.go:48` (`AgentState`) and `:281` (the `Emit` placeholder)
  - `core-go/engine/execute.go:16` (`AgentHooks`)
  - `tools.go` (`CallTool`, `ToolSpecs`, `EditorState`) and `streams.go`
  - `callbacks.go:90` (`ApplyGenerated`) and `settings.go`
  - `core-go/guide` (`Designing`, `SystemDocs`) and `core-go/model` (9a)
  - `core-go/doc` (`RandomU64`, `Walk`) and `core-go/export/policy.go`

## Tasks & Acceptance

**Execution:**
- [x] The agent package: state, the run loop (API and provider backends), queue, Let it cook, children, permissions, stop, events and instructions.
- [x] Claude Code: the CLI run, account commands, MCP config writer and sweep.
- [x] Chats: the writer goroutine, list, load, delete, clear and flush, plus the `Emit` hook.
- [x] Fill `AgentHooks.Stop` and `NoteFlagged`; give `StopsSince`/the `Pens` cancels the shape 9c needs.
- [x] Port every Rust test in agent.rs and claude_code.rs.
- [x] A live parity test for `agent_instructions` against the Rust CLI (in `parity_test.go` style, honouring `.parity-live`/`NALARUPA_CLI`).
- [x] Record any divergence in Implementation Notes.

**Acceptance Criteria:**
- Given each `#[test]` in agent.rs and claude_code.rs, when its Go port runs, then it passes with the same assertions. Tests that need a POSIX shell stand-in get a Windows `.cmd` stand-in or are skipped only where Rust skips them.
- Given `NALARUPA_CLI` set to the Rust release CLI, when the live parity test runs, then `agent_instructions` matches byte for byte for every backend.
- Given MinGW gcc as `CC`, when `gofmt -l .`, `go vet ./...` and `go test -count=1 ./...` run, then they pass, and `go test -race` passes on the agent package and `./engine/`.
- Given the Rust core, when `cargo test --release` runs, then it passes (only a test-only CLI subcommand was added).

## Implementation Notes

- `core-go/engine/agent.go` (new): `Flag` (Rust's `Arc<AtomicBool>`: `Set`, `IsSet`, and a `Done` channel closed when set, which is what `script.Call.Stop` takes), `Pen` (frame plus its own `Flag`), `Profile`, `Chat`, `Permission` (agent id plus a 1-slot answer channel), `QueueOpts` and the full `AgentState`, with the agent.rs methods that only touch the state: `Profile`, `Record`, `Clear`, `Restore`, `addSaved`, `NewProfile`, plus `PenFrames` and `NowMs`. They live in `engine` because `Emit` records. Rust's tuples became fields (`Parallel`/`Mode`, `Cook`/`Iterate`).
  - `Emit` records `type: "agent"` events and, when `Record` says a message or turn ended, calls the new `AgentHooks.SaveChat`; its error is queued as Rust's toast, before the event.
  - `New` gives `Agent.Cancel` a fresh flag. `Agent.Pens` is now `[]Pen`; `files.go` and `history.go` read `PenFrames()`.
- `core-go/agent` (new package; imports `engine`, `model`, `export`, `guide`, `script`, `doc`, `jsonv`):
  - `agent.go`: `Attach` (sets `Hooks.Stop`, `NoteFlagged`, `SaveChat`), `Send`, `begin`, `startTurn`, `wrapParallel`, `startRun` (Rust `start_thread`), `run` (the thread body), `NoteFlagged`, `clearGenerating`, `takeQueue`, `dropQueue`, `StoppedSince`, `Stop`, `Reset`, `Instructions`, `Backend`, `MaxDesigns`, `FastModels`.
  - `api.go`: `apiTools`, `runAPI`, `cutOffCall`, `toolDoneEvent`, `truncate`, `clipEnds`, `lines` (Rust `str::lines`).
  - `spawn.go`: `SpawnChildren`, `sideDirections`, `childPrompt`, `SpawnSpec`. `permissions.go`: `PermissionTool`, `AskPermission`, `AnswerPermission`.
  - `chats.go`: `chatPath`, the process-wide writer goroutine (a `sync.Cond`, newest snapshot only, atomic `.json.tmp` then rename), `saveChat`, `flushChatSaves`, `Shutdown`, `listChats` with the 20-day sweep, `Chats`, `LoadChat`, `DeleteChat`, `ClearChats`, and the `chatOps` mutex, always taken before the engine lock.
  - `claudecode.go`: `MCPConfig`, `SweepMCPConfigs`, `runClaudeCode` (argv as Rust, prompt on stdin from a goroutine, a 150 ms kill watcher, stderr read as bytes, one retry on a missing session), `errorCause`, `signInHint`, `SignedOut`, `exitError`, `shortTool`, `toolResultParts`.
  - `auth.go` (claude_code.rs): `ClaudeStatus`, `ClaudeLogin`, `loginWith`, `CancelLogin`, `ClaudeLogout`. Rust's `try_wait` is a goroutine that waits on the child and closes `done`.
  - Every function locks the engine itself (`Locked`, so a panic never leaves it held); none holds it across model calls, process I/O or file writes, except where Rust also does (`LoadChat` reads the file under it, as `load_chat` does).
- `core-go/export/policy.go`: `PathAllowed` (agent.rs `path_allowed`) and `Normalize` are exported. `WritePolicy` shares their helpers and keeps Rust's semantics for an empty base (the empty path, which every path is inside), which the replay parity checks. In `PathAllowed`, an empty workspace, document or skills folder stands for `None`.
- `core-go/export/canonical_windows.go` / `canonical_other.go`: `canonical` resolves the deepest existing ancestor in `Normalize`, as Rust's `canonicalize` does.
  - On Windows it opens the file and reads `GetFinalPathNameByHandleW` (through `syscall`). The `\\?\` prefix is dropped and `\\?\UNC\` becomes `\\`, which is safe because both sides of every comparison go through it. Elsewhere it is `EvalSymlinks`.
  - Why: since Go 1.23, `filepath.EvalSymlinks` no longer follows junctions or mount points, so a `mklink /J` junction in the workspace that pointed outside it passed `PathAllowed`.
  - A dangling junction is still followed by hand through `os.Readlink`.
  - `TestJunctionsAreFollowed` (Windows) checks a junction to outside, one that stays inside, and a dangling one. It fails with the old `EvalSymlinks` resolution.
- `core-go/agent/program.go`: `command` launches the CLI as Rust's `Command::new` finds it. Claude Code runs and all three auth commands use it.
  - On Windows, a bare name gets `.exe` when it has no extension and is searched in the application folder, the system folders and PATH, never through PATHEXT, so a `claude.cmd` shim is not run.
  - An explicit path is used as is, which is how Rust's sign-in test reaches its `.cmd` stand-in. Elsewhere it is exec's PATH search.
  - `TestTheCLIIsFoundAsAnExeOnly` checks that a shim alone on PATH counts as not installed.
- `AskPermission`: an id drawn twice denies the older request before replacing it (Rust drops its sender, so its `recv` fails to false). `TestARepeatedPermissionIDDeniesTheOlderRequest` checks it, and hangs without the fix.
- `core-go/model/sse.go`: `lossy` is exported as `model.Lossy`, so the agent decodes CLI output without a third copy.
- `core/src/bin/cli.rs`: a test-only `agent-instructions <backend>` subcommand prints `agent::instructions` as is.
- The functions 9d dispatches:

  | Command | Function |
  |---------|----------|
  | `agent_send` | `Send(e, text, parallel, mode, designs, iterate)`, with lib.rs defaults 1, "split", 1, "layout" |
  | `agent_stop` | `Stop` |
  | `agent_reset` | `Reset` |
  | `agent_instructions` | `Instructions`, with lib.rs's backend switch: provider uses api, an unknown backend is an error, none uses `Backend(e)` |
  | `agent_permission` | `AnswerPermission` |
  | `claude_auth_status`, `_login`, `_cancel`, `_logout` | `ClaudeStatus`, `ClaudeLogin(e, console)`, `CancelLogin`, `ClaudeLogout` |
  | `chat_list`, `_load`, `_delete`, `_clear`, `_flush` | `Chats`, `LoadChat`, `DeleteChat`, `ClearChats`, `Shutdown` |

  9d must also call `agent.Attach(engine.Global())` once at start, and drop these commands from `agentCommands`/`idleAgentCommands`.
- What 9c needs:
  - The MCP server exports are `SpawnChildren`, `AskPermission`, `SpawnSpec`, `PermissionTool`, `Engine.Agent.Profile`, and `Agent.Parallel`/`Agent.Main`, which `tools/list` reads.
  - The MCP server must set `e.MCPPort` under the lock. `MCPConfig` reads it there, and a run fails with "the MCP server is not running" until then, as in Rust.
  - `StartPen` is still nil. 9c sets it in `Attach` to:
    - push `engine.Pen{Frame, Cancel}` with the flag already set when `StoppedSince(e, stops, agent)`
    - start the pen goroutine
    - when the pen ends, remove its own entry by flag pointer (pen.rs)
  - pen.rs takes the model from `RunModel`/`CurrentProvider` (`DefaultModel` when there is none) and the backend from `Backend`.
- Tests (`go test ./agent/`):
  - All 29 Rust tests are ported: 27 in agent.rs and 2 in claude_code.rs. `permission_paths_follow_symlinks` is skipped on Windows, as Rust's `cfg(unix)` does. `mcp_config_is_removed_with_its_run` checks the write and the sweep. Go has no `Drop`, so that the run removes its config is checked by the Claude Code run tests. The sign-in test uses Rust's stand-in, a `.cmd` file on Windows and `sh` elsewhere.
  - 20 Go tests are added (17, plus 3 from the review fixes):
    - API runs on an `httptest` fake: the event order, the request body's key order, the beta header, the tool result and the saved chat. Also queueing while busy, Stop mid-stream, Let it cook, a panicking stream (`error` then `done`, and the next run works), a 401 error, setup without a key, children with every refusal, permissions (allow, deny, auto, Stop, inside-folder, unknown agent), and chats (list order, sweep, bad id, damaged, missing, running guards, delete, clear, saved key order).
    - Claude Code runs: exact argv and stdin, session resume, a missing session retried once, a single signed-out card, the exit and stderr text, Stop killing the CLI, and a missing CLI or MCP server. The stand-in `claude` is the test binary itself (`TestMain` with `NALARUPA_FAKE_CLAUDE`), because a batch file cannot take the multi-line `--append-system-prompt` argument.
    - `TestInstructionsMatchRust`, the live parity check.
- Divergences:
  - **Empty ids.** `Session`, `Main` and `Profile.Parent` use "" for `None`. A saved chat whose session or agent id is the empty string loads as having none, where Rust keeps `Some("")`.
  - **Panics.** A panic in a run becomes an `error` event reading `internal error (panic): <value>`, then `done`, and the agent goes idle. In Rust the thread dies and the agent stays running. The chat writer, the CLI helper goroutines and the sign-in watcher recover too.
  - **Error texts.** The not-found text is Rust's ("program not found" on Windows, `No such file or directory (os error 2)` elsewhere). Other spawn, pipe and write errors use `doc.IOError`.
  - **Exit status.** It is formatted as Rust's `ExitStatus` (`exit code: N` or `0x…` on Windows; `exit status: N` or `signal: N (NAME)` on Unix). Signal names come from a short Linux table, so the text can differ from Rust's on other Unix systems.
  - **Chat order.** `os.ReadDir` sorts by name and Rust's `read_dir` uses OS order, which only matters for chats with equal `updatedAt`.
  - **Instructions parity is live only.** The text holds the guide files as checked out, and `core.autocrlf` makes them CRLF on this machine, so a committed golden would differ between checkouts. Without `NALARUPA_CLI` the test skips.

## Plan Change Log

## Review Triage Log

### 2026-11-17 — Review pass
- verdicts: 3 findings — high 2, medium 0, low 1
- findings:
  - `[high]` `[patch]` On Windows a directory junction got around the permission card, because `EvalSymlinks` stopped following mount points in Go 1.23. — Fixed: `canonical` (`GetFinalPathNameByHandleW`) and `TestJunctionsAreFollowed`.
  - `[high]` `[patch]` The PATHEXT lookup picked up the `claude.cmd` shim, which mangles the multi-line argv and survives Stop. — Fixed: a lookup that finds only `.exe`, as Rust's `Command` does (`agent/program.go`), plus `TestTheCLIIsFoundAsAnExeOnly`.
  - `[low]` `[patch]` A permission id collision left the older request waiting forever. — Fixed: the older request is denied, plus a test.

## Verification

**Commands:**
- `cd core-go && gofmt -l . && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `... go test -race -count=1 ./agent/ ./engine/` -- expected: pass
- Rebuild the Rust CLI if `cli.rs` changed (`CARGO_TARGET_DIR=<scratch target>`, cargo at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`), then run the live parity test with `NALARUPA_CLI` -- expected: pass
- `cd core && cargo test --release` -- expected: pass

**Results (2026-11-17, Windows, MinGW gcc):**
- `gofmt -l .` prints nothing. `go vet ./...` and `go test -count=1 ./...` pass on every package.
- `go test -race -count=1 ./agent/ ./engine/` passes.
- The live run (`NALARUPA_CLI` set to the rebuilt Rust release CLI) passes `go test -count=1 ./...`, including `TestInstructionsMatchRust` for `api` and `claude-code`.
- `cargo test --release` passes (292 tests).
- Not run on Linux.

## Auto Run Result (orchestrator finalize)

- **Summary:** `core-go/agent` ports agent.rs and claude_code.rs: the API, provider and Claude Code runs, queue, Let it cook, children, permissions, stop, chats, the account commands and the instructions. `engine/agent.go` holds the full agent state and chat recording in `Emit`.
- **Review:** quick lens. Three patches (2 high, 1 low).
- **Verification:**
  - gofmt, `go vet ./...` and `go test ./...` pass, and `-race` passes on agent and engine.
  - Instruction parity is live and byte-identical.
  - `cargo test --release` passes 292 tests.
- **Next:** 9c sets `MCPPort` and fills `StartPen`. 9d calls `agent.Attach` and dispatches the commands.
