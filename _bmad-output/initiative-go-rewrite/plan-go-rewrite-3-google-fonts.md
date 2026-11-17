---
title: 'Go rewrite phase 3: Google Fonts downloads'
type: 'refactor'
ticket: ''
created: '2026-11-15'
status: 'built'
baseline_revision: 'f77dfba16a53d19e387eff39472cdcb55f06a839'
route: 'full'
route_source: 'auto'
risk: 'medium'
review: 'quick'
review_source: 'pinned'
lenses_ran: ['quick']
review_loop_iteration: 0
context:
  - '{project-root}/_bmad-output/initiative-go-rewrite/plan-go-rewrite-2-text-shaping.md'
---

<frozen-after-approval reason="human-owned intent — do not modify unless human renegotiates">

## Intent

**Problem:** `core-go/text` only finds fonts already on disk. A document that uses a Google Fonts family the machine lacks measures with Noto until the family is downloaded, and the Rust core does that download automatically.

**Approach:** Port the download half of `core/src/text.rs` to `core-go/text`: the `Online` flag, `Ensure`, `Loading`, the in-flight set, the Google Fonts metadata cache, and a variable-font-first download with a static CSS fallback. Downloads go to the same cache paths, run one at a time off the caller's goroutine, and report back through a callback. Requests, file names and fallbacks are proven against the Rust URL scheme with `httptest`.

## Boundaries & Constraints

**Always:**
- Same URLs and order as `text.rs:466-538`:
  - the metadata from `fonts.google.com/metadata/fonts`, with the anti-XSSI prefix stripped up to the first `{`, axis tags sorted, families without axes ignored, cached as `cache/google-fonts-metadata.json` and read once per downloader
  - then `raw.githubusercontent.com/google/fonts/main/{ofl,apache,ufl}/{lowercase compact}/{Compact}[-Italic]%5B{axes}%5D.ttf`
  - then `fonts.googleapis.com/css2?family=Fam+Name:wght@W` (or `:ital,wght@1,W`), retried as `?family=Fam+Name`, taking the first `url(...)`
- Same files as Rust: `cache/var/{Compact}[-Italic].ttf` and `cache/{Fam_Name}-{W}[i].ttf`. A download is skipped when its target file already exists.
- HTTP as `net.rs`: 8 s total timeout, `User-Agent: nalarupa/0.1`, a 32 MiB body limit, and any non-2xx response treated as a failure.
- Files are written atomically (temp file, then rename), so a failed or interrupted download never leaves a partial font.
- One download at a time per downloader. The default downloader is shared by every `Fonts`, so the limit is process-wide, as in Rust.
- `Fonts` stays single-goroutine. The download goroutine touches only the cache files and the `OnReady(family, weight, italic)` callback. The caller later calls `Downloaded`, which clears the in-flight entry, loads the file and bumps `Generation`.
- `New` sets `Online = true`, as Rust does. Base URLs and the HTTP client live on the downloader, so tests point them at `httptest`.

**Never:**
- No change to the Rust core, `app/` or the FFI.
- Tests never reach the real network unless an explicit opt-in env var is set.

## I/O & Edge-Case Matrix

| Scenario | Input / State | Expected Output / Behavior | Error Handling |
|----------|--------------|---------------------------|----------------|
| Offline | `Online` false, family missing | No HTTP request, `Loading` false, Noto fallback as in phase 2 | No error expected |
| Variable font | Metadata lists the family with axes `wght,opsz` | Requests `…/ofl/{dir}/{Compact}%5Bopsz,wght%5D.ttf` and writes `cache/var/{Compact}.ttf`. `OnReady` fires once, then `Downloaded` returns true, bumps `Generation`, clears `Loading`, and the family resolves | No error expected |
| License fallthrough | `ofl` returns 404, `apache` returns 200 | The `apache` file is used | No error expected |
| Static, italic | Family has no axes, italic 700 | `css2?family=Fam+Name:ital,wght@1,700`, its first `url(...)` saved as `cache/Fam_Name-700i.ttf` | No error expected |
| CSS retry | The weighted CSS request fails | Retried as `css2?family=Fam+Name` | No error expected |
| All fail | Every request returns 404 or times out | No file written, `OnReady` still fires, `Downloaded` returns false, `Loading` is cleared, Noto Sans is fetched | No panic |
| Metadata cached | Cache file already present with the XSSI prefix | No metadata request, and axes are parsed correctly | No error expected |
| Concurrency | Two families ensured back to back | The server never sees overlapping requests | No error expected |
| Repeat | The same style ensured twice | Only one download | No error expected |

</frozen-after-approval>

## Code Map

- `core/src/text.rs` -- port source: `ensure` 138, `loading` 145, `fetch` 151-163 (where the download starts and `font_ready` is called), `downloaded` 166, `var_file`/`static_file` 455-472, `family_axes` 476-497, `DOWNLOADS` and `download` 499-538.
- `core/src/net.rs` -- timeout, user agent and body limit to copy.
- `core-go/text/fonts.go` -- `Fonts` (lines 49-62), `New` 67, `Downloaded` 101, `ensure` 115, `fetch` 123 (says "Rust also starts a download here"), `loadCached` 134, `varFile`/`staticFile` 170-190. These are the extension points; reuse `varFile`/`staticFile` as they are.
- `core-go/text/shape.go:58` -- fallback calls `f.fetch(notoFamily(c), …)`, which now may download.
- `core-go/text/text_test.go`, `core-go/text/parity_test.go`, `core-go/parity_test.go` -- every `text.New(...)` call. Each must set `Online = false`, matching the Rust CLI's `online = false`.

## Tasks & Acceptance

**Execution:**
- [x] `core-go/text/google.go` -- the downloader type: base URLs, an `*http.Client` with the `net.rs` limits, a mutex, the memoized axes, `download(cache, family, weight, italic)`, and the shared default instance.
- [x] `core-go/text/fonts.go` -- `Online`, `OnReady`, the in-flight set, exported `Ensure` and `Loading`. `fetch` starts a download when the cache has nothing, and `Downloaded` clears the in-flight entry.
- [x] `core-go/text/text_test.go`, `core-go/text/parity_test.go`, `core-go/parity_test.go` -- set `Online = false` after every `New`.
- [x] `core-go/text/google_test.go` -- every matrix row against an `httptest` server that records requests.
- [x] `core-go/text/google_test.go` -- with `NALARUPA_NETWORK=1` and `NALARUPA_CLI`, download one variable and one static-only family into a temp cache, then compare Go measurement with `nalarupa-cli measure <cases> <that cache>`.

**Acceptance Criteria:**
- Given only Go, when `go test ./...` runs in `core-go`, then it passes without opening any connection to the internet.
- Given `NALARUPA_NETWORK=1` and the release CLI, when the opt-in test runs, then the downloaded files load in both Go and Rust, and their measurements match.
- Given `go test -race ./text/`, when the concurrency and repeat tests run, then no data race is reported.

## Implementation Notes

- `downloader` (`google.go`) is unexported: tests are in package `text` and swap `Fonts.dl` for one aimed at `httptest`. `newFonts` was split out of `New` so tests can build an online `Fonts` with an empty database, which makes the Noto Sans fallback observable as a request.
- `compactFamily` was split out of `varFile` (same behaviour) so the GitHub file name and directory reuse it.
- Without `OnReady` a download still runs and caches its file, but the style stays `Loading` until the owner calls `Downloaded`.
- Temporary files are `<target>.*.part`, which the font loader never picks up, so a leftover from a crash is harmless. The metadata cache is written the same way.
- The metadata is parsed into generic JSON values, as Rust reads `serde_json::Value`, so one malformed entry skips only that entry.
- The opt-in network test downloads DM Sans (variable, `ofl/dmsans/DMSans%5Bopsz,wght%5D.ttf`) at 500 and Abril Fatface (static, CSS API) at 400, and skips when either is installed.

## Plan Change Log

## Review Triage Log

Pass 1 (lens: quick). Counts: high 0, medium 0, low 1, false 0, maybe-false 0. In step 3, the matrix audit found that the "Metadata cached" row had no test, and `TestDownloadMetadataCached` was added before review.

| # | Finding | Verdict | Route | Evidence |
|---|---------|---------|-------|----------|
| 1 | `google_test.go` `TestDownloadRepeat`: the duplicate-download check is not deterministic, so a broken dedup would still pass | low | patch | The count and a non-blocking `noMore` are checked right after the second `Ensure`, and a duplicate goroutine's first request lands only after an HTTP round trip. Fix: assert that no in-flight entry is set synchronously, since `fetch` sets it before it spawns the download. |

## Verification

Run on Windows, 2026-11-15: all three commands passed. `-race` needs a real MinGW gcc as `CC` (the `gcc` shim on PATH cannot link `-lsynchronization`). Removing the downloader mutex makes `TestDownloadsOneAtATime` fail. With `NALARUPA_CLI` set, the phase 2 parity tests still pass.

**Commands:**
- `cd core-go && go vet ./... && go test -count=1 ./...` -- expected: all pass, no network
- `cd core-go && go test -race -count=1 ./text/` -- expected: pass, no race (needs cgo for `-race`; if no C toolchain, run it in WSL)
- `cd core-go && NALARUPA_NETWORK=1 NALARUPA_CLI=<target>/release/nalarupa-cli go test -count=1 -run Network ./text/` -- expected: pass
