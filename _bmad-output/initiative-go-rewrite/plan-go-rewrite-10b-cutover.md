---
title: 'Go rewrite phase 10b: app, CLI, CI and release cutover to the Go core'
type: 'refactor'
ticket: ''
created: '2026-11-17'
status: 'built'
baseline_revision: '3c6e311'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-8e-c-abi.md'
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-9c-mcp-pen-dispatch.md'
warnings: []
deferred:
  - summary: >-
      The macOS build (link flags, dylib, minimum version 13, signing), the three release jobs and both CI jobs on the Go core have never run.
    evidence: |-
      They cannot run on this machine. Settle by pushing the branch and running CI and a dry-run release (workflow_dispatch or a PR touching core-go/build.sh), with the maintainer's go-ahead.
    location: >-
      .github/workflows/ci.yml, release.yml, core-go/build.sh, core-go/shader/gpu
    severity: high (unverified)
  - summary: >-
      Removing core/ is blocked. The Go parity tests need the Rust CLI oracle (freeze their goldens first). core/ also still holds the bench and icon tools, core/guide/*.md (the source of the Go copies), core/tests/figma-clipboard.html (used by editor-e2e), core/vendor, and the Cargo.toml version stamp.
    evidence: |-
      This is the maintainer's choice to keep core/ until CI is green on every platform.
    location: >-
      core/
    severity: medium
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
- [x] The loader and packaging default to the Go core.
- [x] The Go CLI with its user commands, plus a parity test.
- [x] darwin_arm64 wgpu fetch, cgo flags and the `.dylib` build branch.
- [x] CI and release workflows build the Go core and run the app checks against it.
- [x] The automation prompts, README and AGENTS.md describe the Go core.
- [x] A local Windows run of typecheck and the Windows CI app check list, without `NALARUPA_CORE`, with the results recorded.

**Acceptance Criteria:**
- Given the Go DLL built in `core-go/out` and no `NALARUPA_CORE`, when the Windows CI app check list runs from `app/`, then it passes.
- Given `bun run build` (or `package.ts`), when it packages, then `dist/` contains the Go core.
- Given MinGW gcc as `CC`, when `gofmt -l .`, `go vet ./...` and `go test -count=1 ./...` run, then they pass.
- Given the edited workflows, when they are parsed as YAML, then they are valid.

## Implementation Notes

**What changed**
- Loader (`app/src/core.ts`): `NALARUPA_CORE`, then next to the executable, then `core-go/out/<lib>`; the `core/target` candidates are gone and the not-found message points at `build.sh`/`build.ps1` in `core-go/`. Packaging (`app/scripts/package.ts`) copies `core-go/out/<lib>`; `NALARUPA_GO_CORE` is gone.
- Go CLI `core-go/cmd/cli` (build as `nalarupa-cli`): `render`, `svg`, `html`, `exec`, `mcp` with cli.rs's arguments, stdout lines, `error: …` on stderr and exit 1. `exec --preview` needed `Engine.ExecuteWithin(args, budget)` (script.rs `execute_within`), a three-line export in `core-go/engine/execute.go`. Live parity test `cmd/cli/parity_test.go` (NALARUPA_CLI only, skipped otherwise): `render` at scale 2 compares decoded pixels exactly, `svg`/`html` bytes with embedded PNG data URIs masked (Go's PNG encoder writes other bytes; the svg package's parity test already compares their pixels).
- macOS: `fetch_wgpu.go` pins `darwin_arm64` → `wgpu-macos-aarch64-release.zip` of v29.0.1.1, SHA-256 `a5797a37…7a3e`, computed from the asset downloaded from the official gfx-rs/wgpu-native release (and equal to the digest GitHub publishes for it); only the zip listing was read. `gpu.go` links `-lobjc` and the Metal, QuartzCore, Foundation, CoreGraphics and CoreFoundation frameworks (wgpu-native's examples link CoreFoundation, QuartzCore and Metal). `build.sh` has a darwin branch → `out/libnalarupa_core.dylib`, no `-static`. The darwin archive also holds a `.dylib`, but `fetch_wgpu.go` keeps only `libwgpu_native.a`, so the static one links.
- MCP version: Rust reported `CARGO_PKG_VERSION`, stamped by `packaging/set-version.sh`; the Go core had a constant "0.1.0", so a release would always report 0.1.0. `agent.version` is now a var that `build.sh` sets with `-X` from `app/package.json` (which set-version.sh stamps too). `build.ps1` and a plain `go build` keep the default.
- CI (`ci.yml`): both jobs run `bash build.sh` after `go test` and set job-level `NALARUPA_CORE` to `core-go/out/<lib>`; the two Windows steps that run the packaged `dist/nalarupa.exe` clear it so they load the core copied next to the exe. The Rust build/test stay (parity oracle, `NALARUPA_CLI`).
- Release (`release.yml`): the three jobs swap rust-cache/cargo for setup-go (`go-version-file`) and `go generate ./shader/gpu && bash build.sh` after set-version; Windows with `CC=C:/mingw64/bin/gcc.exe`; ubuntu-22.04 kept with its glibc comment; the macOS signing loop already signs `*.dylib`. Pull requests touching `core-go/build.sh` or `core-go/shader/gpu/**` now also run the release builds.
- Automation: `claude-setup` builds the Go core after the Rust one (kept for parity); the claude/claude-qa/claude-ci-fix prompts run the Rust tests, then `go vet`, `go test` with `NALARUPA_CLI` and `build.sh`, then the app checks, and say the Go core is in `core-go/out` (found by default) with the Rust oracle in `core/target/release`.
- Docs: README (tagline, CLI line, Development requirements and build, architecture diagram of the Go packages, guide copies, Checks with `NALARUPA_CLI`, bench/icon marked Rust-only), AGENTS.md (intro, guide copies, Running and verifying: build, `NALARUPA_CORE`, MinGW `CC`, `go generate`, the `core-go/out` lock and a side build, Go checks; Rust-only conventions marked), `docs/release-signing.md`, `packaging/set-version.sh` comment.

**Local results (Windows, 2026-11-17)**
- `gofmt -l .` clean; `go vet ./...` ok; `go test -count=1 ./...` all packages ok (MinGW gcc as `CC`, no `NALARUPA_CLI`).
- Rust CLI rebuilt (it predated 9b's core/ change); `go test -count=1 ./cmd/cli` with `NALARUPA_CLI`: render (shapes, effects), svg and html all pass.
- By hand against the Rust CLI: `exec` on edits.js, globals-1.js, e-syntax.js (with `--seed 7 --globals '{"a":1}' --save --offline`) and a TakeScreenshot script with `--preview` on a new file: stdout, exit code and saved document identical (the screenshot PNG differs in bytes only, encoder); missing argument / unknown command: same messages and exit 1 (the help hint names cmd/cli/main.go); `mcp` serves initialize.
- `bash build.sh` → `core-go/out/nalarupa_core.dll`. From `app/` in PowerShell with `NALARUPA_CORE` unset: typecheck, smoke, editor-check, mcp-check, doc-check, script-check, chat-check, drag-check, viewport-check, issues-check all pass. `bun run build` → `dist/nalarupa_core.dll` has the same SHA-256 as `core-go/out/nalarupa_core.dll`; `bun run exe-check` passes.
- Every edited workflow and the action parse as YAML (Bun's YAML), with the expected jobs and steps.

**Review fixes (round 1)**
- `NALARUPA_CLI` was relative (`../core/target/...`), but `go test` runs each package in its own folder, so live parity would not find the CLI. `ci.yml` now sets `${{ github.workspace }}/core/target/release/nalarupa-cli` (`.exe` on Windows). The claude-ci-fix, claude-qa and claude.yml step-3 prompts and the README use `NALARUPA_CLI="$PWD/../core/target/release/nalarupa-cli"`. claude.yml's `--append-system-prompt` is a quoted `claude_args` string the action parses, so it says "with NALARUPA_CLI set to the absolute path of core/target/release/nalarupa-cli" and has no `$PWD`.
- `build.sh` darwin branch exports `MACOSX_DEPLOYMENT_TARGET=13.0` unless already set (the release promises macOS 13+).
- `build.ps1` stamps `agent.version` from `app/package.json` (ConvertFrom-Json), as build.sh does. The AGENTS.md side-build command is unchanged: it leaves the 0.1.0 default.
- claude-qa.yml points at the Go dispatcher (`core-go/engine/dispatch.go` plus `core-go/agent/commands.go`) in both places that named `core/src/lib.rs` `dispatch`.
- `bash build.sh cli` builds `out/nalarupa-cli(.exe)` with the same flags (static MinGW on Windows, version stamp). README and the cmd/cli header document it, after `go generate ./shader/gpu` and with MinGW gcc as `CC` on Windows.
- Re-run: `gofmt -l .` clean, `go vet ./...` ok, `bash build.sh cli` builds a working CLI, `build.ps1` builds the dll, and every edited workflow plus the action parse as YAML.

**Unverified (cannot run here)**
- macOS: the darwin_arm64 link (framework list, `-lobjc`), the dylib build, signing under the hardened runtime and the Gatekeeper step.
- The release workflow on all three runners (MinGW at `C:/mingw64` on windows-2022, the ubuntu-22.04 glibc floor of the Go .so).
- CI: the Linux and Windows jobs with `build.sh` and `NALARUPA_CORE`, the Windows GUI suites (e2e, editor-e2e, file-e2e, a11y, motion, window-check) on the Go core, and the Claude automation setup.
- Not run locally: the GUI e2e suites (CI runs them).

**Before core/ can be removed**
- Green Linux, Windows and macOS (release) runs on the Go core.
- The parity tests and goldens depend on the Rust CLI's oracle subcommands; decide to freeze the goldens (no live comparison) first.
- Still in core/: the bench and icon examples (README), `core/guide/*.md` (source of the Go copies, checked by `guide_test.go`), `core/tests/figma-clipboard.html` (read by `editor-e2e.ts` and written by `figma-fixture.ts`), `core/vendor` (Rust only), and `packaging/set-version.sh` stamping `core/Cargo.toml`.

## Review Triage Log

### 2026-11-17 — Review pass
- verdicts: 5 findings — high 1, medium 1, low 3
- findings:
  - `[high]` `[patch]` A relative `NALARUPA_CLI` broke the live parity tests in CI, because each package runs from its own folder. — Fixed: absolute paths in the workflows, prompts and README.
  - `[medium]` `[patch]` The macOS minimum version was not pinned. — Fixed: `MACOSX_DEPLOYMENT_TARGET=13.0` in build.sh.
  - `[low]` `[patch]` build.ps1 did not stamp the version. — Fixed.
  - `[low]` `[patch]` claude-qa.yml pointed at lib.rs dispatch. — Fixed.
  - `[low]` `[patch]` The CLI build instructions were incomplete. — Fixed: a `bash build.sh cli` target.

## Verification

**Commands:**
- `cd core-go && gofmt -l . && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && ... go test -count=1 ./...`
- `cd core-go && bash build.sh`
- `cd app && bun run typecheck && bun run smoke && bun run editor-check && bun run mcp-check && bun run doc-check && bun run script-check && bun run chat-check && bun run drag-check && bun run viewport-check && bun run issues-check` (no `NALARUPA_CORE`)

## Auto Run Result (orchestrator finalize)

- **Summary:** The app loader, packaging, CI, release, automation prompts and docs now use the Go core. A Go `nalarupa-cli` covers render, svg, html, exec and mcp. A macOS build path is added. `core/` is kept as the parity oracle.
- **Review:** quick lens. Five patches.
- **Verification (Windows):**
  - gofmt, `go vet` and `go test ./...` pass, and the CLI parity test passes live. After the review patches, `cmd/...` and `agent` were re-run and pass.
  - With the default loader lookup (no `NALARUPA_CORE`), the full Windows CI app check list passes.
  - The `bun run build` package carries the Go DLL, and `exe-check` passes.
  - The YAML parses.
- **Unverified:** macOS, release and CI (see deferred).
