---
title: 'Go rewrite phase 9c: MCP server, AI Pen and agent dispatch'
type: 'refactor'
ticket: ''
created: '2026-11-17'
status: 'built'
baseline_revision: '4957510'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-9b-agent-loop.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8e-c-abi.md'
warnings: []
deferred:
  - summary: >-
      Codex `mcp_install` overwrites `config.toml` when it cannot be read or is not UTF-8, and a stale `[mcp_servers.nalarupa]` port is kept. The Rust code behaves the same way.
    evidence: |-
      `read_to_string(...).unwrap_or_default()` in mcp.rs, mirrored in core-go/agent/mcp.go. To fix in both cores: treat only "not found" as empty.
    location: >-
      core-go/agent/mcp.go (codex)
    severity: low
  - summary: >-
      A silent `claude` CLI keeps a pen alive past Stop until its next line arrives. The Rust code behaves the same way.
    evidence: |-
      penClaudeCode notices cancel only per line. A 150 ms kill watcher, like the agent's, would fix it.
    location: >-
      core-go/agent/pen.go
    severity: low
  - summary: >-
      `mcp-install-check` is skipped on Windows by the script itself, and Linux has not been run.
    evidence: |-
      The Go test covers the same assertions. Run it on Linux CI at cutover.
    location: >-
      app/scripts/mcp-install-check.ts
    severity: low (unverified)
---

<intent-contract>

## Intent

**Problem:** The Go DLL still answers every agent, chat and MCP command with "not available". `core/src/mcp.rs` (230 lines: the Streamable HTTP MCP server and `mcp_install`) and `core/src/pen.rs` (539 lines: AI Pen drawing) are unported, and `init` starts no MCP server. That leaves `smoke`, `doc-check`, `script-check`, `mcp-check` and `chat-check` failing at their agent and MCP steps against the Go DLL.

**Approach:**
- Port `mcp.rs` with `net/http`. Bind `127.0.0.1` on the first free port from 47700 to 47719, accept POST only, return JSON without SSE, and support batches. Keep the local-origin guard (403 otherwise), `/mcp` for external agents, `/mcp/<agent-id>` for the built-in agents (attribution, `spawn_agents`, and an `approve` that blocks until `agent_permission`), and `install`.
- Port `pen.rs` and fill `AgentHooks.StartPen`.
- In `engine.Dispatch`, dispatch every agent, chat, provider-test, Claude account and MCP command exactly as `lib.rs:407-521` does:
  - call `agent.Attach` at startup
  - `init` starts MCP and returns the real `mcpPort`/`mcpUrl`
  - `provider_test` runs `ProviderTest` under the lock, then `model.Test` on a goroutine that emits the event
  - remove the not-available lists
- Port the Rust tests in mcp.rs and pen.rs. Also add the `mcp` CLI command if `core/src/bin/cli.rs:172` exposes one that the app or the docs use; otherwise note it for phase 10.
- Rebuild the DLL and run the app checks against it.

## Boundaries & Constraints

**Always:**
- MCP wire behaviour is identical to Rust: the JSON-RPC responses (`initialize`, `tools/list`, `tools/call`, notifications, errors), the headers, the status codes, the Origin guard, the batch handling, and `ToolOut::finish` (the heavy work after the lock drops).
- One goroutine per request (net/http already does this). `approve` blocks only its own request.
- The pen behaves like Rust: the cancel per pen, the 50 ms redraw throttle, `pen`/`pen_end` events, `usvg.FromStr` with `ImageHrefResolver`, `ApplyGenerated`, and `doc` id generation.
- `mcp_install` writes the same client config files as Rust for `claude-code`, `cursor` and `codex`, and returns the same message string. Tests use a throwaway HOME.
- Every goroutine and HTTP handler recovers panics.
- Never hold the engine lock across slow work.

**Never:**
- No change to `app/` source, apart from nothing. The checks are the oracle and run unchanged.
- No change to Rust behaviour, except a test-only CLI subcommand if one is needed.
- No real model calls: checks use their own fake servers or stand-ins.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| MCP contract | `bun run mcp-check` with the Go DLL | passes | – |
| Origin | a non-local Origin | 403 | – |
| Port | 47700 busy | the next free port; `init` reports it | all 20 busy → Rust's behaviour |
| Approve | a built-in agent's `approve` call, then `agent_permission` | the request unblocks with the answer | stop → deny |
| Pen | `pen` script call, stop mid-draw | `pen`/`pen_end` events, shapes applied | stop cancels |
| Dispatch | each agent/chat/claude/mcp command | same JSON as Rust | same errors |
| App checks | `smoke`, `doc-check`, `script-check`, `mcp-check`, `chat-check`, `mcp-install-check` against the Go DLL | pass, except the parts that need a POSIX-shell stand-in, which Windows skips as with Rust; and the two known smoke failures deferred from 8e (shadow seam, long-line speed) | Record exact results |

</intent-contract>

## Code Map

- `core/src/mcp.rs` (230), with the Origin guard test at 49-66 and the port range at 17.
- `core/src/pen.rs` (539), with tests at 419-539.
- `core/src/lib.rs:407-521` (agent dispatch) and `init` (the MCP start).
- `core/src/bin/cli.rs:172` (the `mcp` command).
- `core-go/agent` (9b): `Attach`, `SpawnChildren`, `AskPermission`, `SpawnSpec`, `PermissionTool`, the command functions, and `e.MCPPort`.
- `core-go/model` (9a): `Test`.
- `core-go/engine`: `dispatch.go`, `replay.go`, `execute.go` (`AgentHooks.StartPen`), `tools.go`, `toolout.go`, `callbacks.go`.
- `core-go/usvg`.
- `app/scripts/{smoke,doc-check,script-check,mcp-check,chat-check,mcp-install-check}.ts`.

## Tasks & Acceptance

**Execution:**
- [x] The MCP server with its tests, plus `install`.
- [x] AI Pen with its tests, plus the `StartPen` hook.
- [x] Dispatch wiring: `init` MCP start, all agent commands, `provider_test`, and remove the stubs.
- [x] `cmd/core` ABI tests still pass, plus an ABI-level test that `init` reports a live MCP port.
- [x] Rebuild the DLL (`core-go/build.sh` or `build.ps1`) and run the app checks with `NALARUPA_CORE` set to it. Record the results.

**Acceptance Criteria:**
- Given each `#[test]` in mcp.rs and pen.rs, when its Go port runs, then it passes.
- Given MinGW gcc as `CC`, when `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...` and `-race` on the changed packages run, then they pass.
- Given the Go DLL, when `mcp-check`, `script-check`, `doc-check`, `chat-check` and `smoke` run, then they pass, except the documented skips and the two 8e-deferred smoke assertions.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- The MCP server and the pen live in package `agent` (`agent/mcp.go`, `agent/pen.go`), not in packages of their own: they need the unexported `command`/`claudeProgram`/`startError`, and `mcp` → `agent` → `engine` would otherwise need more exports.
- `agent/mcp.go` (mcp.rs):
  - `StartMCP(e)` listens on `127.0.0.1` from 47700 to 47719 with `net/http` (one goroutine per request), `MCPURL`, `localOrigin`, `serveMCP`, `toolsFor`, `rpc`, `callTool`, `InstallMCP` and `mergeJSON`.
  - Status codes and bodies are tiny_http's: 403 `forbidden origin`, 404 `not found`, 405 with `Allow: POST`, 400 `bad body` for a body that is not UTF-8, 202 with an empty body, and 200 `application/json` (plain replies are `text/plain; charset=UTF-8`). The path is the raw request target before `?`, as `req.url()`. The body is read up to 64 MiB.
  - `rpc` keeps Rust's rules: a message that is not an object, or has no `id`, gets no reply (an `id: null` does); a batch answers only its requests, and 202 when none; a parse error answers `id: null`, -32700 and `parse error: <serde message>`. `tools/call` gives -32602 for `arguments` that are not an object or null; a null stays null for `spawn_agents`/`approve` and becomes `{}` otherwise.
  - `callTool` runs `SpawnChildren`, `AskPermission` (blocks only its request's goroutine) or `Engine.CallTool`, then `Finish` with the lock released, then logs the external call.
  - A panicking handler aborts its connection without a reply (`http.ErrAbortHandler`), as Rust's request thread dies. The server's error log is discarded, so a DLL never writes to stderr.
  - `InstallMCP` writes the same files and messages as Rust. `claude mcp add …` runs through `command` (an `.exe` only on Windows, as Rust's `Command`).
- `agent/pen.go` (pen.rs): `startPen` (the goroutine), `pen.feed` (the 50 ms throttle, stable node ids from `Doc.GenID`, `pen`/`pen_end`, `ApplyGenerated`), `penGenerate` (the API and provider backends through `RunModel`/`CurrentProvider`, "stopped" is no error), `penArgs`/`penClaudeCode` (the CLI's text deltas, cancel noticed per line as in Rust), `penDraw`, `penWalk`, `pathNode`, `penPaint`, `penHex`, `closeSVG`, `tagEnd`, `tagName` and `growingShape`. `usvg.FromStr` gets an `ImageHrefResolver` that resolves nothing. Numbers in `geometry` use Rust's `{}` (`engine.RustF64`), JSON numbers serde's; the stroke scale is computed in f32 as Rust does; `atan2` is `libm.Atan2`.
- `Attach` now also sets `Hooks.StartPen` (a fresh flag, preset when `StoppedSince`, pushed onto `Agent.Pens`, then `startPen`; the pen removes its own entry by pointer) and `Hooks.StartMCP`.
- Dispatch:
  - `engine.HandleUnlocked` is a second command table run without the engine lock; `agent/commands.go` registers every agent, chat, Claude account and `mcp_install` command in it, with lib.rs's argument defaults and error texts (`id required`, `answer required: allow, deny or auto`, the `agent_instructions` backend switch).
  - `provider_test` is in `engine.Dispatch`: `ProviderTest` under the lock, then `model.Test` on a goroutine that emits the event.
  - `init` calls `Hooks.StartMCP` without the lock when the port is 0, and sets the port only when it succeeds (Rust ignores the error too).
  - `AgentUnavailable`, `agentCommands` and `idleAgentCommands` are gone.
  - `cmd/core` calls `agent.Attach(engine.Global())` once, on the first `nr_call` (not at load, because the engine reads `NALARUPA_DATA` when it is created).
- Tests:
  - All Rust tests ported: `origin_guard_matches_exact_local_hosts` (mcp.rs); `streamed_svg_draws_as_it_grows`, `cli_passes_the_picked_model_only_when_set`, `claude_code_pen_draws_with_the_picked_model` and `close_keeps_prefixes_well_formed` (pen.rs). The Claude Code pen test is `cfg(unix)` in Rust; the Go port runs everywhere, with the test binary as the stand-in (`NALARUPA_FAKE_CLAUDE=pen`).
  - Added in `agent`: the HTTP contract (statuses, bodies, content type, batches, errors, `initialize`, per-agent tool lists, attributed and logged calls), `approve` blocking until `AnswerPermission` and Stop denying it while other requests are served, the port range (the next free port, and Rust's error when all 20 are busy), `install` for every client in a throwaway home (stand-in modes `install`, `install-exists`, `install-fail`, plus a missing CLI), and pens through the hook over the fake API (request, cursor position, children, placeholder cleared, entry removed; Stop mid-draw; a pen started after a Stop; a toast without a key).
  - `cmd/core`: `TestInitStartsTheMCPServer` (a live port in the range answering `ping`, the same port on a second `init` and in `state`) and `TestAgentCommands` (dispatch and error texts) replace the two not-available tests.
- Divergences:
  - A panic in a pen goroutine ends that pen with an `AI Pen: internal error (panic): …` toast, finishes its frame (`pen_end`, `placeholder: false`) and removes its entry (Rust's thread dies and leaves both). A panic in the `provider_test` goroutine emits a failed `provider_test` event with the panic text (Rust emits nothing).
  - `InstallMCP` takes the home folder from `os.UserHomeDir` (`USERPROFILE` on Windows); Rust's `dirs` asks Windows for the profile folder. They differ only when `USERPROFILE` is overridden. On Unix both read `HOME`.
  - tiny_http also sends `Server` and `Date` headers, and Go sends `Date` and `Content-Length`; no client reads them.
- Review patches:
  - **Flaky port test (low).** The server's first port is the package variable `firstMCPPort` (default `DefaultMCPPort`, 47700, used exactly as before). `TestTheServerTakesTheFirstFreePort` moves it to 47900-47919 and restores it afterwards, so it never takes all of the app's range (that could break `cmd/core`'s `TestInitStartsTheMCPServer` in parallel, or a running app). `StartMCP` now wraps `listenMCP`, which also returns the server, so the agent tests close the servers they start.
  - **Stuck frame after a pen panic (low).** The recover path in `startPen` now emits `pen_end` and applies `placeholder: false` through `ApplyGenerated` under the lock, as a normal finish does, without running `feed` again. `TestAPanickingPenStillFinishesItsFrame` (a stream that panics the client) checks the toast, the `pen_end`, the cleared placeholder and the removed entry.
  - After both patches: `gofmt -l .` prints nothing; `go vet ./...` and `go test -count=1 ./...` pass; `go test -race -count=1 ./agent/ ./engine/ ./cmd/core/` passes.
- Left for phase 10: `core/src/bin/cli.rs` has an `mcp` command (start the server and park). No app script or doc uses it, and core-go has no CLI binary yet, so it is not ported.

## Plan Change Log

## Review Triage Log

### 2026-11-17 — Review pass
- verdicts: 4 findings — high 0, medium 0, low 3, info 1
- findings:
  - `[low]` `[patch]` The port-range test took all of 47700-47719 and could break the `cmd/core` init test. — Fixed: `firstMCPPort` var, the test uses 47900+, and test servers are closed.
  - `[low]` `[defer]` The Codex install overwrites an unreadable config.toml. — Rust parity; deferred.
  - `[low]` `[patch]` A panicking pen left its frame as a placeholder. — Fixed: `pen_end` plus clearing the placeholder in the recover path, with a test.
  - `[info]` `[defer]` A silent CLI keeps the pen alive past Stop. — Rust parity; deferred.

## Verification

**Commands:**
- `cd core-go && gofmt -l . && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && ... go test -count=1 ./...` -- expected: pass
- `cd core-go && bash build.sh` -- expected: `out/nalarupa_core.dll`
- `cd app && NALARUPA_CORE=<abs dll> bun run mcp-check` (then `script-check`, `doc-check`, `chat-check`, `mcp-install-check`, `smoke`) -- expected: as in the acceptance criteria
- `cd core && cargo test --release` -- expected: pass

**Results (2026-11-17, Windows, MinGW gcc):**
- `gofmt -l .` prints nothing. `go vet ./...` and `go test -count=1 ./...` pass on every package. `go test -race -count=1 ./agent/ ./engine/ ./cmd/core/` passes.
- `bash build.sh` builds `out/nalarupa_core.dll` (25.6 MB).
- App checks with `NALARUPA_CORE` set to that dll:
  - `mcp-check`: ok (MCP at 47700).
  - `script-check`: ok.
  - `doc-check`: ok, and ok (env key).
  - `chat-check`: ok.
  - `mcp-install-check`: skipped on Windows by the script itself (its stand-in `claude` is a shell script); `TestInstallWritesEachClientsConfig` covers the same assertions in Go.
  - `smoke`: fails only at the known 8e assertion "an edited canvas matches a full render around a rotated, flipped shadow (largest difference 21)". A scratch copy that logs that assertion and the long-line one instead of throwing (then deleted) ran to `smoke ok — MCP at http://127.0.0.1:47700/mcp`: every MCP, agent and pen assertion passes, and the long-line bound passed this time (160k chars in 3240 ms).
- `cargo test --release` was not run: no Rust file changed.
- Not run on Linux.

## Auto Run Result (orchestrator finalize)

- **Summary:** The MCP server, AI Pen and every agent, chat and MCP command are dispatched in the Go DLL. `init` reports a live MCP port. This completes phase 9.
- **Review:** quick lens. Two low patches, two deferred (both Rust parity).
- **Verification:**
  - gofmt, `go vet ./...` and `go test ./...` pass, and `-race` passes on agent, engine and cmd/core.
  - With the Go DLL, `mcp-check`, `script-check`, `doc-check` and `chat-check` pass.
  - `smoke` passes everything except the 8e-deferred shadow seam.
  - The review patches touched only tests and the pen panic path, so the app checks were not re-run after them.
