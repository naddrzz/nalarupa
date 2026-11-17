---
title: 'Go rewrite phase 7c: export job planning and writing'
type: 'refactor'
ticket: ''
created: '2026-11-16'
status: 'built'
baseline_revision: '8b2d65d5a1df03285a8c479b92f49ff1b61ea57f'
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
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-7a-html-export.md'
warnings: []
deferred: []
---

<intent-contract>

## Intent

**Problem:** Exports from the inspector and from the agent's `Export(...)` script call go through `html.rs`'s planning and job code (`plan`, `plan_files`, `ExportJob` with settle, replan and run, the write mutex, script-file claims, file naming). The Go core has every encoder (PNG/JPEG/WebP/SVG/PDF/HTML) but nothing that turns ids, a format and an output path into written files.

**Approach:** Port that orchestration to `core-go/export` on `export.Ctx`:
- `Plan(ctx, dataDir, ids, format, out, opts)`, producing a `Job` with its files, the asset keys it waits on, and the recorded script runs:
  - png/jpeg/webp through `NodeTreeAt`/`EncodeTree`
  - svg through `Export` + `Embedded`, with images inlined only when written
  - pdf through `PDFTrees`/`EncodePDF`, defaulting to every visible top-level frame
  - html-css/html-tailwind/html through `html.Document`
- `Job.Settle(waiter, deadline)`, `Replan`, and `Run` (one process-wide write mutex, exclusive creation for script exports, recorded in a script-written set)
- `CheckScriptExport` and `ClaimScriptFiles` behind an injectable `PathPolicy` (Rust uses `agent::path_allowed`/`normalize`, which come in phase 9)
- `file_stem`/`file_name`, and the error strings (`hidden`, missing node, damaged images)

The engine's `export()` entry and `ToolOut` wiring remain for phase 8.

## Boundaries & Constraints

**Always:**
- Same files as Rust for the same request: names, count, formats, contents (bytes for SVG/HTML, decoded pixels within the 4d tolerances for rasters, the 4f PDF checks), the deck/multi-node layout, and the error strings (hidden layers, missing ids, an empty selection, an unknown format, a path refused by policy).
- Waiting is injected (`Waiter func(keys []string, until time.Time)`), as is the path policy (`PathPolicy func(file, base, docPath string) bool`). The defaults are no waiting and allow-all, and engine and agent supply the real ones later.
- Writes are serialised by one mutex and a script export never overwrites an existing file, as in Rust.
- Reuse `core-go/export`, `core-go/html`, `core-go/pdf`, `core-go/svg`. Floating-point products get explicit conversions (no FMA).

**Never:**
- No engine lock or doc cloning (phase 8), no agent path rules (phase 9), no `ToolOut` integration.
- No change to `app/`, the FFI, or Rust behaviour apart from test-only CLI additions.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Formats | One node exported as png, jpeg, webp, svg, pdf, html-css, html-tailwind, html at scales 1 and 2 | Same file names and contents as Rust | No error expected |
| Many nodes | Several ids and a slides deck | Same file set and names (deck and per-node) | No error expected |
| PDF default | pdf with no ids | Every visible top-level frame, as Rust | No error expected |
| SVG images | An SVG export whose image files change after planning | Images read when written, as Rust | No error expected |
| Damaged and hidden | A damaged image in a PDF, a hidden layer, a missing id | Same skip or the same error string | Error as Rust |
| Script exports | Exporting to an existing file, a refused path, two claims of the same name | Same refusals and claims | Error as Rust |

</intent-contract>

## Code Map

- `core/src/html.rs:27-237` -- `plan`, `ExportJob{stale, settle, replan, run}`, `check_script_export`, `claim_script_files`, `export` (160, engine part out of scope), `plan_files` (194), `run` (124) with the write mutex and `SCRIPT_WRITTEN`. `:1063` is `file_name`. The tests in 1080-1843 that 7a left (`svg_export_reads_images_after_planning`, `exports_slides_deck_and_rasters`, `pdf_export_skips_damaged_images`, the two hidden-layer tests).
- `core/src/engine.rs:41,57-85` (`ToolOut` exports: settle and replan) and `script.rs:905-907` (script `Export` builds the job) -- the call shapes the Go API must serve later.
- `core-go/export` (`Ctx`, `NodeTreeAt`, `NodeImage`, `EncodeTree`, `PDFTrees`, `EncodePDF`), `core-go/svg` (`Export`, `InlineEmbedded`), `core-go/html` (`Document`) -- reuse.
- `core/src/bin/cli.rs` -- add `export <doc> <format> <out> [ids...] [--offline]`, which plans, settles without waiting, and runs, then prints the written paths. This is the oracle.

## Tasks & Acceptance

**Execution:**
- [x] `core/src/bin/cli.rs` -- the test-only `export` oracle.
- [x] `core-go/export/job.go` (`Plan`, `Job`, `Settle`, `Replan`, `Run`, write mutex, script set), `naming.go` (`FileStem`, `FileName`), `policy.go` (`PathPolicy`, `CheckScriptExport`, `ClaimScriptFiles`).
- [x] `core-go/export/job_test.go` -- the remaining Rust html.rs tests plus each matrix row.
- [x] `core-go/export/job_parity_test.go`, `core-go/testdata/exportjob/` -- requests per matrix row compared with the CLI on file names and contents (goldens without the CLI, live with it).

**Acceptance Criteria:**
- Given MinGW gcc as `CC`, when `go test ./...` runs (also with `GOAMD64=v3`), then it passes with golden export-job parity.
- Given the release CLI, when `NALARUPA_CLI=… go test ./export/` runs, then every request writes the same files as Rust, compared as stated.
- Given the Rust core, when `cargo test --release` runs, then it passes.

## Implementation Notes

- `core-go/html` imports `core-go/export`, so `export` cannot call `html.Document`: it calls the package variable `export.HTMLDocument`, which `html`'s `init` sets (the `svg.DefaultShader` pattern). Anything that writes html exports links `core-go/html`; unlinked, an html export fails with a stated error.
- API: `Plan(c, dataDir, ids, format, out, opts) (*Job, error)`; `Job.Files`, `Stale`, `Settle(Waiter, until)`, `Replan(c, dataDir)`, `Run`; `Waits` and `Runs` are exported as in Rust. `CheckScriptExport(doc, dataDir, format, out, PathPolicy)` and `ClaimScriptFiles(job)`. `FileStem`, `FileName`. Planning records script runs through `Ctx.Runs` (restored afterwards) and downloads through `Assets.Record`.
- Paths are joined as Rust's `PathBuf::join` does (no cleaning), so written paths and error strings read as Rust's.
- The script-written set keys files by a port of agent.rs `normalize` (absolute, `..` and links resolved), unexported in `policy.go`. Phase 9 can move it to the agent.
- The CLI oracle also takes `--opts <json>` (scales 1 and 2, quality, the html options) and `--script` (check and claim as a script's `Export`, with Rust's real `path_allowed`). The Go parity test supplies an equivalent `PathPolicy`.
- Parity (`testdata/exportjob/fixtures.json`): there are 48 golden requests on text-free fixtures (`deck.pen`, `damaged.pen`, `empty.pen`) and 5 live-only requests on the slides template (text). Each request runs on its own copy of the fixture folder. Results (paths relative to the copy, or the error with the copy written as `<W>`) are in `golden/results.json`, and the Rust files are in `golden/<request>/`. SVG and HTML are compared byte for byte, and PNG and WebP by exact pixels. JPEG is compared by pixels within measured per-request encoder tolerances (Go's image/jpeg, as in 4d). PDF is compared by page count, page sizes and image count, because the PDF bytes are checked by the 4f parity.
- "Two claims of the same name" is a script export of the same id twice (`script-same-name`): Rust and Go write the one file twice. Claims across separate exports are covered by `TestScriptExportClaims`, because each CLI run is a new process.

## Plan Change Log

- 2026-11-16: The html formats reach `html.Document` through `export.HTMLDocument`, set by `html`'s `init`. Otherwise `export` would import `html`, which is an import cycle. The CLI `export` command gained `--opts` and `--script` beyond the planned signature, because the matrix needs scales, options and script refusals. `export_test.go` gained a test-only `PlannedSVG` hook for the ported `svg_export_reads_images_after_planning`.

## Review Triage Log

### 2026-11-16 — Review pass
- The export base is Rust's `Path::parent` of the document path as written. It is no longer `Doc.Dir()`, which cleans the path. `outPath` joins a relative result onto the base a second time, as `out_path` does.
- `normalize` is a faithful port: `std::path::absolute` per platform (`..` kept on POSIX), Rust `ancestors` and `strip_prefix`, links resolved before `..`.
- `Ctx.Scripts` now gets the `*script.Runs` to record into, so a custom hook's job is stale too.
- Parity: page sizes are compared in file order. Both sides use `W/data` as the data folder. Written paths are kept as written (`<W>` for the copy), so mixed separators are compared. There are new forward-slash and relative document-path requests. A request with no golden entry fails. An unsaved document cannot be expressed through the CLI, so it is covered by `TestExportsSlidesDeckAndRasters`.

### 2026-11-16 — Review pass
- verdicts: 10 findings — high 0, medium 2, low 8, false 0, maybe-false 0
- findings:
  - `[medium]` `[patch]` `exportBase` uses the cleaned `filepath.Dir`, so written paths and refusal text differ from Rust's raw `Path::parent` (slashes, and `.` for a relative doc). — Fix: a raw parent for export, plus fixtures.
  - `[low]` `[patch]` `outPath` joins once, where Rust joins the base twice for a relative result. — Fix: match Rust, with a test.
  - `[medium]` `[patch]` `resolvePath` cleans `..` before resolving links, unlike `normalize`, so on Unix a script export can overwrite a file it didn't write. — Fix: a faithful port, with a symlink test.
  - `[low]` `[patch]` Record mode is lost when `Ctx.Scripts` is custom. — Fix: route `Runs` through the hook, with a test.
  - `[low]` `[reject]` Raw `os` I/O error strings differ from Rust's `(os error N)` text. — Uncommon in everyday use, and the fix means mapping OS error codes.
  - `[low]` `[patch]` `pdfSummary` sorts the page boxes, so page order isn't compared. — Fix: keep file order.
  - `[low]` `[patch]` Go and the CLI run with different data dirs, so the unsaved-document default path isn't compared. — Fix: use the same dir, plus a case.
  - `[low]` `[patch]` A case missing from `results.json` passes silently in golden mode. — Fix: fail when an entry is missing.
  - `[low]` `[patch]` `GOAMD64=v3` was run on two packages only. — Fix: the orchestrator runs `GOAMD64=v3 go test ./...` on the whole module.
  - `[low]` `[reject]` The dangling-link refusal isn't tested on Windows, because symlinks need privileges there. — An environment limitation: Linux CI runs the test, and junction support would add test complexity.

## Verification

**Commands:**
- `cd core-go && go generate ./shader/gpu && CGO_ENABLED=1 CC=<MinGW gcc> go vet ./... && CGO_ENABLED=1 CC=<MinGW gcc> go test -count=1 ./...` -- expected: pass
- `cd core-go && CGO_ENABLED=1 CC=<MinGW gcc> NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 ./export/` -- expected: pass, live
- `cd core && cargo test --release` -- expected: pass (separate `CARGO_TARGET_DIR`; on this machine cargo is at `~/.rustup/toolchains/stable-x86_64-pc-windows-msvc/bin`)

## Auto Run Result

- **Verification (2026-11-16):**
  - `go vet ./...` and `go test -count=1 ./...` pass on every package. `GOAMD64=v3` passes on `./export/` and `./html/`.
  - Live parity (`NALARUPA_CLI`) passes on `./export/` and `./html/`: 48 golden requests and 5 live ones.
  - `cargo test --release` passes (292 tests).
- **Residual risks:**
  - JPEG tolerances were measured on one machine (Windows).
  - Error strings in the goldens hold paths with `/` (converted). On Linux they compare directly but were not run there.
  - `normalize` is a Go port of agent.rs. Its Windows form (no `\?\` prefix) only has to agree with itself.
- **Finalize (orchestrator, after review):**
  - **Summary:** `core-go/export` gains the export job:
    - `Plan`, `Job` (`Settle`/`Replan`/`Run`), and `FileStem`/`FileName`
    - path joins as `PathBuf::join`, and `PathPolicy`, `CheckScriptExport` and `ClaimScriptFiles` with a faithful `normalize`
    - `HTMLDocument` hooked from `html`, and a `Ctx.Scripts` hook that now receives `Runs`

    Rust gains a test-only `export` oracle.
  - **Review:**
    - Patches applied: 8 (2 medium, 6 low). These cover the raw `Path::parent` base, the double join in `out_path`, faithful `normalize`, record mode with a custom hook, PDF page order, the shared data dir, missing goldens failing, and `GOAMD64=v3` on the whole module.
    - Rejected: 2 low (raw OS error strings, and the Windows symlink test).
  - **Follow-up review recommended:** false. The two medium patches have deterministic tests; the symlink case still needs a Linux run.
  - **Verification after patches:**
    - `go vet` and `go test ./...` pass offline, and `GOAMD64=v3 go test ./...` passes on the whole module.
    - Live parity passes on every package.
    - A pre-existing `TestSlots` isolation flake (5a2; it assumed an empty process-wide cache and slot) is fixed by resetting them at the start of the test, and it passes with `-count=5`.
