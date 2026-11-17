package svg

import "github.com/naddrzz/nalarupa/core-go/usvg"

// ImageHrefResolver is svg.rs `image_href_resolver`: usvg's default resolver, except that a data url
// holding an SVG is refused when its nesting, summed over images embedded in images, exceeds MaxSVGDepth.
// Assets.Image vets the top-level image; this covers a shallow SVG that carries a deep one as a `data:`
// url, which usvg parses recursively. Rust counts the depth in a thread-local; here each resolver counts
// its own, so give every parse (goroutine) its own resolver.
func ImageHrefResolver() usvg.ImageHrefResolver {
	inner := usvg.DefaultDataResolver()
	used := 0
	return usvg.ImageHrefResolver{
		ResolveData: func(mime string, data []byte, opt *usvg.Options) (usvg.ImageKind, bool) {
			depth := 0
			if isSVGPayload(mime, data) {
				depth = svgDepth(data, MaxSVGDepth-used)
			}
			if used+depth > MaxSVGDepth {
				return nil, false
			}
			used += depth
			kind, ok := inner(mime, data, opt)
			used -= depth
			return kind, ok
		},
		ResolveString: usvg.DefaultStringResolver(),
	}
}

// isSVGPayload reports whether usvg parses a data url of this MIME type and content as an SVG (it sniffs
// `text/plain`).
func isSVGPayload(mime string, b []byte) bool {
	return (mime == "image/svg+xml" || mime == "text/plain") && imageExt(b) == "svg"
}
