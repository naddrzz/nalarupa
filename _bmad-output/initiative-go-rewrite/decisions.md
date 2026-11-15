# Go rewrite: standing decisions

Decided by the maintainer on 2026-11-15, before phases 4-10. Every later plan follows them.

- **Run mode:** phases 4-10 run one after another through `bmad-build-auto`: plan, implement, review, verify and commit locally on branch `go-rewrite`, with no push. A phase stops only for a real blocker.
- **Renderer:** a pure Go port of the parts of tiny-skia and resvg the core uses (paths, strokes, gradients, patterns and images, blend modes, masks, clips, filters), plus a Go PDF writer in place of krilla. Pixel parity with Rust is checked by image diff with a stated tolerance. No tdewolff/canvas.
- **JS sandbox:** QuickJS inside Go without cgo (`modernc.org/quickjs` or `github.com/fastschema/qjs`), so `execute` scripts and their error messages behave as with rquickjs.
- **Shader fills:** GPU through wgpu-native via cgo (for example `github.com/cogentcore/webgpu`), with WebGL fragment shaders translated as `shader.rs` does. cgo is acceptable here, because the c-shared DLL for bun:ffi needs cgo anyway.
- **Order:** renderer, then the JS sandbox (with a per-run id generator, see deferred-work), shaders, HTML and Figma import/export, editor/engine/C ABI, agent/providers/MCP/AI Pen, and the app cutover last.
