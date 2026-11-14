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
