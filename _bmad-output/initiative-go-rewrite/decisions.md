# Go rewrite: standing decisions

Decided by the maintainer on 2026-11-15, before phases 4-10. Every later plan follows them.

- **Run mode:** phases 4-10 run one after another through `bmad-build-auto`: plan, implement, review, verify and commit locally on branch `go-rewrite`, with no push. A phase stops only for a real blocker.
- **Renderer:** a pure Go port of the parts of tiny-skia and resvg the core uses (paths, strokes, gradients, patterns and images, blend modes, masks, clips, filters), plus a Go PDF writer in place of krilla. Pixel parity with Rust is checked by image diff with a stated tolerance. No tdewolff/canvas.
- **JS sandbox:** quickjs-ng 0.16.2 (the engine rquickjs 0.14 bundles), compiled through cgo from the C sources vendored from the `rquickjs-sys-0.14.0` crate, so error messages, stack traces and limits match Rust. Revised by the maintainer on 2026-11-16, replacing the earlier "no cgo" choice after phase 5a showed Bellard QuickJS (`modernc.org/libquickjs`) diverges from quickjs-ng; cgo is required for the c-shared DLL anyway.
- **Shader fills:** GPU through wgpu-native via cgo (for example `github.com/cogentcore/webgpu`), with WebGL fragment shaders translated as `shader.rs` does. cgo is acceptable here, because the c-shared DLL for bun:ffi needs cgo anyway.
- **Order:** renderer, then the JS sandbox (with a per-run id generator, see deferred-work), shaders, HTML and Figma import/export, editor/engine/C ABI, agent/providers/MCP/AI Pen, and the app cutover last.
