package html

// Icons and the shapes CSS cannot draw, inlined as SVG (the icon and SVG arms of html.rs `element`).

import (
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// icon writes an icon with a plain colour fill as its inline SVG tinted by `color`. No fill = invisible icon.
func (h *writer) icon(n *resolve.RNode, attrs, css string) {
	markup := ""
	if all := fills(n, "fill"); len(all) > 0 {
		if s, ok := solid(all[0]); ok {
			if fill, ok := cssColor(s); ok {
				weight, ok := n.F("weight")
				if !ok {
					weight = 200
				}
				src, _ := h.assets.IconWeight(sOr(n, "library", "lucide"), sOr(n, "icon", ""), weight)
				// A bare-path icon (Material Symbols) sets no fill and would paint black; the canvas fills it with the colour.
				if i := strings.Index(src, "<svg"); i >= 0 {
					src = src[i:]
					tag, _, _ := strings.Cut(src, ">")
					paint := ` fill="currentColor"`
					if strings.Contains(tag, ` fill="`) {
						paint = ""
					}
					markup = strings.Replace(src, "<svg", `<svg style="width:100%;height:100%;color:`+fill+`"`+paint, 1)
				}
			}
		}
	}
	h.out.WriteString("<span" + attrs + ` style="display:inline-flex;` + css + `">` + markup + "</span>\n")
}

// vector writes n drawn by the SVG exporter, with ids numbered after the inline SVGs before it. The div's CSS
// already rotates, flips and fades it.
func (h *writer) vector(n *resolve.RNode, attrs, css string) {
	p := jsonv.Clone(n.P).(*jsonv.Object)
	for _, k := range []string{"rotation", "flipX", "flipY", "opacity"} {
		p.Remove(k)
	}
	shape := resolve.Leaf(n.ID, n.Type, p, "")
	shape.X, shape.Y, shape.W, shape.H = n.X, n.Y, n.W, n.H
	s := svg.New(h.fonts, h.assets, h.base).StartIDs(h.svgIDs)
	s.Node(shape)
	h.svgIDs = s.UID()
	// An inline svg sits on the text baseline (display:block keeps a height-0 line in place) and browsers clip it to
	// its viewport; caps, outer strokes and shadows paint past the box.
	markup := strings.Replace(s.Finish(&svg.Rect{X: n.X, Y: n.Y, W: fmax(n.W, 1), H: fmax(n.H, 1)}), "<svg ", `<svg style="display:block;overflow:visible" `, 1)
	h.out.WriteString("<div" + attrs + ` style="` + css + `">` + markup + "</div>\n")
}
