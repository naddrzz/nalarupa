---
title: 'Go rewrite phase 10b: app, CLI, CI and release cutover to the Go core'
type: 'refactor'
ticket: ''
created: '2026-11-17'
status: 'ready-for-dev'
baseline_revision: ''
route: 'full'
route_source: 'auto'
risk: 'high'
review: 'quick'
review_source: 'pinned'
lenses_ran: []
review_loop_iteration: 0
followup_review_recommended: false
context:
  - '{project-root}/_bmad-output/initiative-go-rewrite/decisions.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8e-c-abi.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-9c-mcp-pen-dispatch.md'
warnings: []
deferred: []
---

<intent-contract>

## Intent

**Problem:** The Go core passes the app's checks, but the app, the headless CLI, CI, the release workflow and the docs all still build and load the Rust core. The release ships Windows, Linux and macOS (Apple Silicon). The Go build has no darwin target: `core-go/shader/gpu/fetch_wgpu.go` pins only `windows_amd64` and `linux_amd64`, and `build.sh` has no `.dylib` branch. There is also no Go CLI for the user-facing commands that README advertises.

**Approach:** Switch every consumer to the Go core. Keep `core/` (the maintainer's choice) as the parity oracle until Linux and macOS CI run green on the Go core. Removing it is a later step.

1. **Loader** (`app/src/core.ts`): look up `NALARUPA_CORE` first, then next to the executable, then `core-go/out/<lib>`. Drop the `core/target` candidates. The not-found message says to run `build.sh`/`build.ps1` in `core-go/`. The comment says "core" rather than "Rust core".
2. **Packaging** (`app/scripts/package.ts`): package the Go core by default. Drop `NALARUPA_GO_CORE`; no Rust fallback.
3. **Go CLI** (`core-go/cmd/cli`, binary `nalarupa-cli`): the user-facing commands from `core/src/bin/cli.rs`, with the same arguments, output and exit codes: `render`, `svg`, `html`, `exec`, `mcp`. The oracle subcommands stay Rust-only.
4. **macOS:**
   - Add `darwin_arm64` to `fetch_wgpu.go`: the pinned v29.0.1.1 release asset with its SHA-256, read from the release's published checksums or computed from the downloaded asset. Read it from the official wgpu-native GitHub release only.
   - Add the cgo `LDFLAGS` for darwin (the Metal, QuartzCore, Foundation and CoreGraphics frameworks, plus whatever wgpu-native's docs list).
   - Add a `.dylib` branch to `build.sh` (`libnalarupa_core.dylib`, no `-static`).
5. **CI** (`.github/workflows/ci.yml`):
   - Linux and Windows jobs build the Go core with `build.sh` after its tests, and run every app check and e2e step with `NALARUPA_CORE` pointing at it.
   - The Rust build and test stay, as the parity oracle.
6. **Release** (`.github/workflows/release.yml`): the Windows, macOS and Linux jobs set up Go (`go-version-file`), run `go generate ./shader/gpu` and build the core with `build.sh` in place of `cargo build --release`.
   - Windows: MinGW gcc as `CC`.
   - ubuntu-22.04 keeps its old-glibc reason.
   - Any step that copies or signs the core picks up the Go library at the same file name.
7. **Automation:** the docs and prompts in `.github/actions/claude-setup/action.yml` and `claude*.yml` build the Go core. The Rust build stays only where the parity tests need it, and the instructions say both.
8. **Docs:**
   - README: build, check and architecture sections. The bench and icon examples are Rust-only for now; keep them and say so.
   - AGENTS.md "Running and verifying": the Go core build, the `NALARUPA_CORE` path, the MinGW `CC` and `go generate`, and that a running app locks `core-go/out`.
   - The intro calls the core Go plus the Rust oracle.

## Boundaries & Constraints

**Always:**
- The ABI and every app check behave the same. Check locally on Windows: build the DLL, run `bun run typecheck` and the app check list the CI Windows job runs, without `NALARUPA_CORE` set, so that the loader's default lookup finds `core-go/out`.
- Pin new downloads by SHA-256, as `fetch_wgpu.go` already does.
- The Go CLI commands' output matches the Rust CLI for the same inputs. Add a live parity test where cheap, e.g. `render` and `svg` byte comparison through `NALARUPA_CLI`.
- Mark macOS, the release workflow and CI as unverified: they cannot run locally. Also check the YAML syntax (for example `bun x yaml-lint` or a quick parse with Bun's YAML or a JSON conversion).

**Never:**
- Don't delete `core/` or the Rust CI steps that produce the parity oracle.
- Don't push, open a PR or touch `dev`/`main`.
- Don't change app source beyond `core.ts` and the packaging/check scripts that reference the core path.
- Don't name other design tools.

</intent-contract>

## Code Map

- `app/src/core.ts:1-17`: the loader. `app/scripts/package.ts:3,48-53`: packaging.
- `core/src/bin/cli.rs:1-180`: the user commands `render`, `svg`, `html`, `exec` and `mcp`.
- `core-go/cmd/core` (the DLL), `core-go/build.sh` and `build.ps1`, and `core-go/shader/gpu/fetch_wgpu.go` and `gpu.go` (cgo flags).
- `.github/workflows/ci.yml:25-90`, `release.yml:80-260`, `.github/actions/claude-setup/action.yml`, `claude.yml`, `claude-qa.yml`, `claude-ci-fix.yml`.
- `README.md:78,117-165,249-256`, `AGENTS.md`, `docs/release-signing.md:24`.

## Tasks & Acceptance

**Execution:**
- [ ] The loader and packaging default to the Go core.
- [ ] The Go CLI with its user commands, plus a parity test.
- [ ] darwin_arm64 wgpu fetch, cgo flags and the `.dylib` build branch.
- [ ] CI and release workflows build the Go core and run the app checks against it.
- [ ] The automation prompts, README and AGENTS.md describe the Go core.
- [ ] A local Windows run of typecheck and the Windows CI app check list, without `NALARUPA_CORE`, with the results recorded.

**Acceptance Criteria:**
- Given the Go DLL built in `core-go/out` and no `NALARUPA_CORE`, when the Windows CI app check list runs from `app/`, then it passes.
- Given `bun run build` (or `package.ts`), when it packages, then `dist/` contains the Go core.
- Given MinGW gcc as `CC`, when `gofmt -l .`, `go vet ./...` and `go test -count=1 ./...` run, then they pass.
- Given the edited workflows, when they are parsed as YAML, then they are valid.

## Implementation Notes

## Review Triage Log

## Verification

**Commands:**
- `cd core-go && gofmt -l . && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && ... go test -count=1 ./...`
- `cd core-go && bash build.sh`
- `cd app && bun run typecheck && bun run smoke && bun run editor-check && bun run mcp-check && bun run doc-check && bun run script-check && bun run chat-check && bun run drag-check && bun run viewport-check && bun run issues-check` (no `NALARUPA_CORE`)
