---
title: 'Go rewrite phase 9a: SSE and model clients'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: '5dcc79fd86cd743308b851ddf33c543a00e69426'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8e-c-abi.md'
warnings: []
deferred:
  - summary: >-
      Transport differences from ureq: Go's default transport negotiates HTTP/2 over TLS and uses the system trust store, and `network error:`/`stream interrupted:` messages carry Go's error text after the shared prefix.
    evidence: |-
      ureq 3 speaks HTTP/1.1 only with bundled Mozilla roots. No test or UI matches the full text. Revisit if a gateway breaks on HTTP/2 SSE or a user's trust store differs.
    location: >-
      core-go/model/sse.go
    severity: low
---

<intent-contract>

## Intent

**Problem:** The Go core has no model clients. The agent loop (9b) and AI Pen (9c) both stream model replies through `core/src/sse.rs`, `claude.rs`, `openai_chat.rs`, `openai_responses.rs` and the provider resolution in `provider.rs`. None of these have been ported.

**Approach:** Add a `core-go/model` package (one package; files `sse.go`, `claude.go`, `openai_chat.go`, `openai_responses.go`, `provider.go`). It is a line-by-line port using `net/http` with context cancellation, plus a cancel flag polled the way Rust does (an `*atomic.Bool` checked every 200 ms). Add the provider-resolution functions to the engine side where they read settings: `current`, `model`, `endpoint`, `from_config`, `pick_model`, `active`, and `test`, the body of `provider_test`. The `fake_server` mock becomes a Go `httptest` helper. Every Rust unit test in these five files is ported, so Rust's tests are the oracle.

## Boundaries & Constraints

**Always:**
- Same wire bytes as Rust:
  - request paths, headers (`anthropic-version: 2023-06-01`, `x-api-key` left out when blank, `anthropic-beta`, `authorization: Bearer`) and JSON bodies, compared field by field in the tests the way Rust's tests compare them
  - base-URL rules from `Client::at` and `endpoint`: drop the fragment, keep the query, no doubled `/v1`
- Same events: the `Event` enum from `claude.rs` (text, thinking and tool-input deltas, tool use, message end, usage, stop reason) and `tool_input` parsing, plus the OpenAI-to-Claude translation in `openai_chat`/`openai_responses`.
- Same error strings: `api_error` (`error.message` from a body capped at 1 MB), the 401 settings hint, `connection stalled`, and the stop and cut-stream messages.
- Same timeouts and caps: 900 s overall, 120 s header and stream stall (+400 ms where Rust adds it), lines of at most 8 MB, a bounded 64-event channel from the reader goroutine, and cancel noticed within 200 ms.
- Same retry: `openai_chat` retries once without `stream_options` after a 400 that mentions it. Nothing else retries.
- New ids use `doc.RandomU64` where Rust uses `doc::random_u64`.
- Settings are read through the existing `engine` settings accessors (`SaveProvider`/`MaskedProviders` in `core-go/engine/settings.go`). If `model` must not import `engine`, put the settings-reading functions (`current`, `from_config`, `active`, `test`) in `engine` and keep `model` free of the engine.

**Never:**
- No agent loop, chats, Claude Code CLI, MCP, pen or dispatch changes (9b-9d). `provider_test` stays a stub in dispatch until 9d, but the function it will call is written and tested here.
- No change to Rust behaviour or `app/`.
- No real network calls in tests.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| SSE framing | multi-line `data:`, comments, CRLF, a final event without its blank line, a line over 8 MB | Same events as `sse.rs` | Oversize line is an error, as in Rust |
| Claude stream | text/thinking/tool deltas, usage, stop | Same `Event` sequence | 401 gives the settings hint; HTTP error gives `error.message` |
| Stop | cancel during header wait and during a silent stream | Returns within about 200 ms with Rust's stopped result | – |
| Stall | no bytes for 120 s (shortened in tests by a variable, as Rust tests do, or the same constant if Rust's tests use it) | `connection stalled` | – |
| OpenAI chat | tool-call fragments, cut stream, a 400 for `stream_options` | Translated events; one retry without `stream_options` | Errors as in `openai_chat.rs` tests |
| OpenAI responses | the three Rust test cases | Same | Same |
| Provider resolution | backend/provider settings, a missing key, model picking | Same choices as `provider.rs` | Same messages |

</intent-contract>

## Code Map

- `core/src/sse.rs` (100 lines): the reader goroutine/channel, framing, stall.
- `core/src/claude.rs` (238): `Client::at`, the request, `Event`, `tool_input`, and tests at 161-238.
- `core/src/openai_chat.rs` (470): `api_error`, translation, the `stream_options` retry at 46, and tests at 259-470.
- `core/src/openai_responses.rs` (402): tests at 261-402.
- `core/src/provider.rs` (328): `current`, `model`, `endpoint`, `from_config`, `pick_model`, `active`, `test`, `save`/`masked` (already in Go), and `fake_server`/`sse()` test helpers at 143-328.
- `core-go/engine/settings.go`: existing provider settings in Go. `core-go/doc/random.go`: `RandomU64`.
- `core/src/agent.rs:1016-1029`: how the agent builds the Claude request body (`max_tokens`, effort, fallbacks, betas, speed). If the request builder lives in claude.rs/provider.rs, port it here; if it lives in agent.rs, leave it for 9b.

## Tasks & Acceptance

**Execution:**
- [x] `core-go/model/sse.go` + tests
- [x] `core-go/model/claude.go` + tests
- [x] `core-go/model/openai_chat.go` + tests
- [x] `core-go/model/openai_responses.go` + tests
- [x] `core-go/model/provider.go` (and/or `core-go/engine/provider.go` for the settings-bound part) + tests, with a shared `httptest` fake server helper
- [x] Record any Rust behaviour that could not be mirrored exactly in Implementation Notes

**Acceptance Criteria:**
- Given each `#[test]` in sse.rs, claude.rs, openai_chat.rs, openai_responses.rs and provider.rs, when the Go port of it runs, then it passes with the same assertions.
- Given MinGW gcc as `CC`, when `go vet ./...` and `go test -count=1 ./...` run in `core-go`, then they pass, with gofmt clean.
- Given `-race`, when `go test -race ./model/` runs, then it passes.

## Implementation Notes

- `core-go/model` (imports only `jsonv` and `doc`, never `engine`):
  - `sse.go`: `post` (sse.rs). The worker goroutine sends lines on a 64-slot channel; the caller polls `cancel` every 200 ms and reports `connection stalled: no data from <who> for <s> s`. Lines are capped at 8 MB (`take(LINE_MAX+2).read_until`, ported as `readLine`), decoded as `from_utf8_lossy` (a copy of figma's `lossy`) and trimmed of CR/LF. An HTTP error body is read up to 1 MB (empty when over it or not UTF-8, as ureq's limited `read_to_string` fails then), and its `error.message` is used when it has one.
  - `claude.go`: `Claude`, `NewClaude`, `ClaudeAt`, `Stream`, `Event`/`EventKind` (claude.rs `Event`), `ToolInput`, `claudeError`, and the serde helpers (`obj`, `arr`, `setKey` = `v[k] = x` with null becoming an object, `has`, `u64`).
  - `openai_chat.go`: `Chat`, `NewChat`, `Stream` (one retry without `stream_options` after a `Provider error 400:` that names it), `ChatTranslate`, `ReasoningModel`, `providerError` (openai_chat.rs `api_error`).
  - `openai_responses.go`: `Responses`, `NewResponses`, `Stream`, `ResponsesTranslate`.
  - `provider.go`: the `Provider` interface, `Endpoint`, `FromConfig`, `PickModel`, `Active`, `ProviderModel` (the `provider` arm of `model`), `NoProvider`, `DefaultModel` (agent.rs `DEFAULT_MODEL`, needed by `model`), and `Test`, the background body of lib.rs `provider_test`, which returns the event.
- `core-go/engine/provider.go`: `CurrentProvider` (`current`), `RunModel` (`model`; it returns `(name, ok, err)` for `Result<Option<String>>`), and `ProviderTest(id)`, the locked part of `provider_test` (unknown provider, `from_config`, no models). In 9d, dispatch calls `ProviderTest` under the lock, then `model.Test` on a goroutine, and emits the event. `save`/`masked` were already ported (`SaveProvider`/`MaskedProviders`).
- Tests: all 18 Rust tests are ported (claude.rs 4, openai_chat.rs 6, openai_responses.rs 3, provider.rs 5; sse.rs has none). `saving_a_provider_keeps_its_key_unless_a_new_one_is_given` is in `engine/provider_test.go`, where `SaveProvider` is. The fake server is an `httptest` helper (`fakeServer` in `model/provider_test.go`). The tests that override the stall set the client's `stall` field, as Rust's tests do. Added Go tests:
  - SSE framing: multi-line data, comments, CRLF, a final event without its blank line, the 8 MB line cap at and past the limit, and error statuses
  - `lossy`, `ToolInput`, a keyless Claude client, and `Endpoint`
  - byte-exact request bodies for both translations, checking serde key order
  - `model.Test`, plus engine tests of `CurrentProvider`/`RunModel`/`ProviderTest`

  That is 25 model tests and 3 engine tests.
- JSON is `jsonv` (serde_json `preserve_order`), so translated bodies match Rust's bytes and key order. `Stream` clones the body it is given (Rust takes it by value), so the caller's history is never edited.
- Divergences that cannot be avoided or are deliberate:
  - **Cancellation.** Returning (on stop, stall or error) cancels the request's context, so the goroutine ends at once. Rust abandons its thread until the next line or the global timeout. Rust's separate connect, send and header timeouts (stall + 400 ms) are not set. The caller's stall check ends a silent connect or header wait first in both cores, and its context cancel then ends the worker. The overall timeout is `http.Client.Timeout` = 900 s.
  - **Error text.** Network and read errors carry Go's text after `network error: ` / `stream interrupted: `, not ureq's. The `*url.Error` wrapper is unwrapped, so the URL is not repeated.
  - **Headers.** Go sends its own `User-Agent` (`Go-http-client/1.1`, not `ureq/…`). It also adds `Accept-Encoding: gzip` and decompresses, as ureq does with its default gzip feature.
  - **beta.** `beta` is a string, and `""` sends no `anthropic-beta`. Rust's `Some("")` would send an empty header, which no caller does.
  - **Panics.** A non-object where Rust's `IndexMut` would panic (for example a `content_block` that is a string, then a text delta) panics in Go too (`setKey`). The 9b agent loop must recover, as the Rust agent thread's panic boundary does.
  - **lossy.** `lossy`/`invalidLen` duplicate `figma/kiwi.go` (8 lines). Sharing them would need a common package for one helper.

## Plan Change Log

## Review Triage Log

### 2026-11-16 — Review pass
- verdicts: 3 findings — high 0, medium 1, low 2
- findings:
  - `[medium]` `[patch]` A block index of 2^63 or more wrapped negative and panicked in `content_block_delta`/`content_block_stop`, where Rust skips it. — Fixed: the index is clamped, and a start at that index is a `bad stream event` error. Test `TestAnOutOfRangeBlockIndexIsSkipped`.
  - `[low]` `[defer]` Network and stream error text after the prefix differs from ureq's. — Deferred (frontmatter).
  - `[low]` `[defer]` HTTP/2 and the system trust store vs ureq's HTTP/1.1 and Mozilla roots. — Deferred (frontmatter).

## Verification

**Commands:**
- `cd core-go && gofmt -l . && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass, gofmt prints nothing
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> go test -race -count=1 ./model/` -- expected: pass

**Results (2026-11-16, Windows, MinGW gcc):**
- `gofmt -l .` prints nothing. `go vet ./...` and `go test -count=1 ./...` pass on every package.
- `go test -race -count=1 ./model/` passes.

## Auto Run Result (orchestrator finalize)

- **Summary:** `core-go/model` ports the SSE reader, the Claude client and both OpenAI clients, plus provider resolution. `engine/provider.go` holds the settings-bound part (`CurrentProvider`, `RunModel`, `ProviderTest`).
- **Review:** quick lens. One medium patch (the block index), two low deferred.
- **Verification:**
  - `go test` passes on `./model/` and `./engine/`, including the new test.
  - The implementer's full `go vet`, `go test ./...` and `-race ./model/` pass.
  - gofmt is clean.
