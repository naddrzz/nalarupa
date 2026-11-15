- source_plan: none
  summary: Go rendering pipeline replacing render.rs, raster.rs, svg.rs and hit.rs (resvg/usvg/tiny-skia/vello_cpu), with PNG/JPEG/WebP/GIF/PDF export.
  evidence: Split from the full Rust-to-Go rewrite; independently shippable phase with no Go library at resvg/krilla fidelity, so it needs its own plan and parity tests.
- source_plan: none
  summary: Go shader fills replacing shader.rs (WebGL fragment shaders currently compiled by naga and run on wgpu).
  evidence: Split from the full Rust-to-Go rewrite; GPU shader path has no direct Go equivalent and can ship separately from the base renderer.
- source_plan: none
  summary: Go JS execute sandbox replacing script.rs (rquickjs) with an embedded JS engine and the same script API.
  evidence: Split from the full Rust-to-Go rewrite; self-contained subsystem with its own interrupt/timeout semantics.
- source_plan: none
  summary: Go HTML import/export replacing html.rs.
  evidence: Split from the full Rust-to-Go rewrite; reviewable and testable on its own against the document model.
- source_plan: none
  summary: Go Figma clipboard import replacing figma.rs (deflate schema and zstd message chunks).
  evidence: Split from the full Rust-to-Go rewrite; isolated importer that depends only on the document model.
- source_plan: none
  summary: Go editor, engine and C ABI replacing editor.rs, engine.rs and lib.rs, built as a c-shared DLL loadable by bun:ffi with the same exported symbols.
  evidence: Split from the full Rust-to-Go rewrite; the FFI surface is the swap point for the app and ships once the subsystems it wires together exist.
- source_plan: none
  summary: Go design agent, LLM providers and MCP server replacing agent.rs, provider.rs, claude.rs, claude_code.rs, openai_chat.rs, openai_responses.rs, sse.rs, stream.rs, mcp.rs, net.rs and guide.rs.
  evidence: Split from the full Rust-to-Go rewrite; network/agent layer is independent of rendering and needs design-eval runs before and after.
- source_plan: none
  summary: Cut the app over to the Go core (bun:ffi loader, CLI, build scripts, CI workflows, release) and remove core/.
  evidence: Split from the full Rust-to-Go rewrite; final switch that can only ship after every Go phase reaches parity.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-1-document-model.md`
  summary: Go text shaping and font loading replacing text.rs (system fonts, Google Fonts download, Noto fallback, wght variation, SkParagraph-style line height, unicode line breaking), plugged into the phase 1 TextMeasurer interface.
  evidence: Layout needs real shaping for text nodes; phase 1 injects a measurer interface so the document model ships without a Go shaper.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-1-document-model.md`
  summary: Port pen.rs (the AI Pen that streams an SVG from the model into path nodes) together with the agent phase.
  evidence: Investigation showed pen.rs is not .pen format code; it depends on engine, agent, provider, claude and usvg.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-1-document-model.md`
  summary: Give the Go id generator per-run (or per-sandbox) state before porting script.rs, agent or providers, since Rust's seed is thread-local and script.rs reseeds and then counts random_u64 calls (script.rs:689-701, 792).
  evidence: core-go/doc/random.go uses one mutex-guarded process-wide seed (unverified severity medium). Any goroutine that draws between Reseed and GenID shifts the ids; harmless in phase 1, which has no concurrent callers.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-2-text-shaping.md`
  summary: Google Fonts downloading for core-go/text (Ensure, Loading, Online flag, metadata cache, variable-then-static download to the same cache paths as text.rs:466-538, one at a time on a goroutine with a completion callback, tested with httptest).
  evidence: Split from phase 2 at the token gate; offline font discovery and shaping ship without it, and Downloaded already reloads files that land in the cache.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-2-text-shaping.md`
  summary: Thai-first text can measure differently from Rust when the primary font has a legacy `kern` table and no Thai GPOS kerning: go-text v0.3.5 applies the `kern` table only for shapers with fallback positioning, rustybuzz always does.
  evidence: An all-families sweep on Windows (584 cases per string) matched Rust for Latin-, Arabic-, Hebrew-, Devanagari- and CJK-first strings; "สวัสดีชาวโลก Ty" differed in 2 of 584 (Segoe UI Light: T and o lose their kern-table adjustment). Fix upstream in go-text or emulate the kern table.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-2-text-shaping.md`
  summary: Drop the go-text workarounds in core-go/text/harfbuzz.go (opened lookup digests via reflection, identity feature substitutions, lenient GSUB/GPOS rebuild, class-0 pair classes) once go-text fixes them upstream; the reflection one breaks silently if go-text renames the unexported fields.
  evidence: Each was found by the live parity sweep against rustybuzz; the text parity tests catch a regression only on machines that have an affected font.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-2-text-shaping.md`
  summary: core-go/text/fontconfig.go does not reproduce fontconfig-parser's abort-the-rest-of-the-file on an unparsable <match>, <selectfont> or <config>, and was not run against Rust on Linux in this phase's local checks.
  evidence: Only directories, includes and aliases matter to fontdb; a file where fontconfig-parser fails mid-way would give Rust fewer directories than Go. CI's Linux job runs the live text parity.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4a-svg-emitter.md`
  summary: Renderer slice 4b - Go raster core porting the tiny-skia 0.12 (+ tiny-skia-path) subset resvg uses (AA fills nonzero/evenodd, hairlines, stroker with dashes, solid/linear/two-point radial/pattern shaders, 16 blend modes incl. the vendored highp ClipColor patch, alpha/luminance masks, draw_pixmap), tested with ported unit tests and pixel goldens.
  evidence: The renderer was split into slices after investigation; 4b is independent of 4a and feeds 4d.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4a-svg-emitter.md`
  summary: Renderer slice 4c - Go usvg subset (XML, styles, units, paths, transforms, paint servers, use, clip/mask/filter structure, images incl. nested SVG, bboxes, text layout and flattening with core-go/text shaping and go-text glyph outlines, the vendored layout.rs patch), tested by structural diff against the vendored usvg CLI.
  evidence: Every render parses the svg.rs string with usvg; there is no direct document-to-pixels path.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4a-svg-emitter.md`
  summary: Renderer slice 4d - Go resvg renderer and filters (render, clip/mask, box/IIR blur, offset, flood, composite, the four vendored resvg patches, render_node) plus encode_tree PNG/JPEG/lossless WebP export, tested by image diff against `nalarupa-cli render` and the vendored resvg CLI.
  evidence: Depends on 4b and 4c.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4a-svg-emitter.md`
  summary: Renderer slice 4e - Go canvas renderer porting render.rs (layers, sync hashing, dirty rects, goroutine bands, view cache, slides, overlays, find_empty_space, Flat lists) and hit.rs hit testing on the resvg path (vello/raster.rs dropped), with a Rust `canvas` CLI oracle run under NALARUPA_RASTER=resvg.
  evidence: Depends on 4d; hit testing needs render.rs's Flat lists.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4a-svg-emitter.md`
  summary: Renderer slice 4f - Go PDF writer replacing krilla/krilla-svg (one page per tree, 1px = 1pt, paths, shadings, image XObjects, soft masks, clips, ExtGState opacity/blend, rasterised filters, subset fonts with ToUnicode), tested by rasterising and text extraction against a Rust `pdf` CLI subcommand.
  evidence: Depends on 4c (usvg tree) and 4d.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4a-svg-emitter.md`
  summary: Trig-derived SVG numbers (ellipse arcs, polygons, path arcs, gradient and turn matrices) can differ from Rust in the last ulp, because Go's math sin/cos/tan/acos/atan2 differ from the C library's; svg parity compares numbers within 1e-12. Bit parity would need CRT-matching trig (cgo, or a port of the CRT's algorithms).
  evidence: A 28 000-input sweep on Windows: Go's math differs from the UCRT on 23-32 % of inputs, a correctly rounded version still on 3-7 %. Only the polygon fixture hits it; a conic seam through pixel centres can flip one pixel column.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4a-svg-emitter.md`
  summary: svg.Export resolves without libs (`imports`) and skips the engine's open steps (`drop_pending`, `drop_placeholder`) and `expand_scripts`; wire them in with the engine port, and port usvg's `image_href_resolver` depth budget for data-URL SVGs inside SVG images with slice 4c.
  evidence: The CLI oracle runs `Engine::open`, which clears unknown pending urls, so the parity fixtures avoid pending urls, imports and script nodes.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4b1-skia-path.md`
  summary: Renderer slice 4b2 - rasterisation half of the tiny-skia 0.12 port in core-go/skia (edges, edge clipping, AA/hairline scan conversion, alpha runs, blitters, lowp/highp pipelines with the vendored highp ClipColor patch, solid/linear/two-point radial/pattern shaders with filter qualities, 16 blend modes, Mask alpha/luminance, Pixmap/PixmapMut, Painter fill_path/stroke_path/fill_rect/draw_pixmap/apply_mask), parity by pixel against a Rust `skia-raster` CLI oracle.
  evidence: 4b was split in two after sizing the vendored tiny-skia (15.5k lines) plus tiny-skia-path (7.1k lines).
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4b1-skia-path.md`
  summary: Guard core-go/svg, core-go/layout and core-go/text against FMA fusion with explicit float conversions of products (as core-go/skia now does), and run their parity under GOAMD64=v3 / arm64.
  evidence: 4b1 review showed plain a*b+c drifts 1-8 ulps under GOAMD64=v3; svg parity tolerates only 1e-12, so a fused build could fail it.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4c1-usvg-core.md`
  summary: Renderer slice 4c2 - usvg text in core-go/usvg (parser/text.rs, tree/text.rs, text/layout.rs with the vendored linear code-point patch, text/flatten.rs; fallback via core-go/text's Noto-first resolver, shaping via core-go/text, glyph outlines with wght from go-text; colr skipped), replacing 4c1's Text placeholder, parity against the usvg-tree CLI with text flattened.
  evidence: 4c was split three ways after sizing usvg (13.5k lines without writer/main/colr) plus roxmltree/svgtypes/simplecss/kurbo.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4c1-usvg-core.md`
  summary: Renderer slice 4c3 - usvg filters in core-go/usvg (parser/filter.rs, tree/filter.rs: regions, units, primitives incl. the feGaussianBlur/feOffset/feFlood/feComposite svg.rs emits and the rest usvg models), replacing 4c1's Filter placeholder, parity against the usvg-tree CLI.
  evidence: Split from 4c with 4c2; resvg filter rendering itself is slice 4d.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-4c3-usvg-filters.md`
  summary: In the engine/C ABI phase, wrap every exported Go entry point in recover() returning "internal error (panic)" as core/src/lib.rs does with catch_unwind (lib.rs:543, 574), and add transitive recursion guards for pattern href cycles and feImage→group→filter cycles, which currently overflow the stack fatally in Go and Rust.
  evidence: The usvg port mirrors Rust unwrap panics deliberately (mustPositiveF32, unreachable!), and Go stack overflows cannot be recovered, so containment and recursion guards are needed before untrusted imported SVGs reach core-go.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-5a-script-nodes.md`
  summary: Phase 5b - execute sandbox core in core-go/script behind a Host interface (Doc, Changed, SlideIn, LaidOut, FindEmptySpace, Generate, per-run NewID): PRELUDE globals, the 15 natives, settle/limits/describe error strings, globals export/restore, apply_edits, Get/to_json, validate/issue_line, the exact success report text; tested with a fake host and a seeded `nalarupa-cli exec --seed` oracle.
  evidence: script.rs split three ways after investigation; the sandbox core needs no engine beyond the Host interface.
- source_plan: `_bmad-output/initiative-go-rewrite/plan-go-rewrite-5a-script-nodes.md`
  summary: Phase 5c (with the engine phase) - execute transaction (rollback, history commit, editId store), execute_shared with EXECUTE_GATE and cancel, live streaming contexts and seeding (stream.rs complete_prefix), Generate downloads and pens, screenshots/exports through ToolOut::finish, and the app script-check/scriptnode-check.
  evidence: These parts mutate engine state and run under the engine lock, so they belong with phase 8.
