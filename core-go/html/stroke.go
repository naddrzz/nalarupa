package html

// Backgrounds, radii and strokes of a box (the first half of html.rs `paint_css`, `stroke_overlay`, `stroke_reach`).

import (
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

var sideNames = [4]string{"top", "right", "bottom", "left"}

// strokeSides is a stroke width as top, right, bottom and left widths: an object's sides (0 when missing, never
// negative), or one positive number for all four; false for anything else.
func strokeSides(w jsonv.Value) ([4]float64, bool) {
	if s, ok := w.(*jsonv.Object); ok {
		var out [4]float64
		for i, k := range sideNames {
			v, _ := s.Get(k)
			out[i] = fmax(numOr(v, 0), 0)
		}
		return out, true
	}
	x, ok := num(w)
	if !ok || !(x > 0) {
		return [4]float64{}, false
	}
	return [4]float64{x, x, x, x}, true
}

// strokeOverlay is a gradient stroke: the gradient fills a box moved out by `reach` of each side width, masked
// down to its per-side ring.
func strokeOverlay(grad string, sides [4]float64, alignment, blend string) string {
	// The box reaches out by the whole width (outer), half of it (centered) or not at all (inner), per side.
	reach := 0.5
	switch alignment {
	case "inner", "inside":
		reach = 0
	case "outer", "outside":
		reach = 1
	}
	pxs := func(f float64) string {
		if sides[1] == sides[0] && sides[2] == sides[0] && sides[3] == sides[0] {
			return ff(float64(sides[0]*f)+0) + "px"
		}
		var out [4]string
		for i, w := range sides {
			out[i] = ff(float64(w*f)+0) + "px"
		}
		return strings.Join(out[:], " ")
	}
	inset, pad := pxs(-reach), pxs(1)
	if blend != "" {
		blend = "mix-blend-mode:" + blend + ";"
	}
	return `<div style="` + overlay + "inset:" + inset + ";padding:" + pad + ";background:" + grad + ";" + blend +
		"-webkit-mask:linear-gradient(#000 0 0) content-box,linear-gradient(#000 0 0);-webkit-mask-composite:xor;mask:linear-gradient(#000 0 0) content-box,linear-gradient(#000 0 0);mask-composite:exclude;\"></div>\n"
}

// overlayOf is strokeOverlay of a stroke width value, "" when it has no width.
func overlayOf(grad string, w jsonv.Value, alignment, blend string) string {
	sides, ok := strokeSides(w)
	if !ok {
		return ""
	}
	return strokeOverlay(grad, sides, alignment, blend)
}

// strokeReach is how far the painted stroke reaches outside the box (the canvas's `Stroke::reach`): the widest
// side for a rectangle or frame, the top width for another shape; none for an inner stroke or without a visible paint.
func strokeReach(n *resolve.RNode) float64 {
	painted := false
	for _, p := range fills(n, "stroke") {
		if c, ok := solid(p); ok {
			if _, ok := cssColor(c); ok {
				painted = true
			}
		}
		if o := jsonv.Obj(p); o != nil && jsonv.StrIs(jsonv.Get(o, "type"), "gradient") {
			if _, ok := gradientCSS(o); ok {
				painted = true
			}
		}
	}
	w := 0.0
	switch v, ok := prop(n, "strokeWidth"); s := v.(type) {
	case *jsonv.Object:
		side := func(k string) float64 {
			x, _ := s.Get(k)
			return fmax(numOr(x, 0), 0)
		}
		if n.Type == "frame" || n.Type == "rectangle" {
			for _, k := range sideNames {
				w = fmax(w, side(k))
			}
		} else {
			w = side("top")
		}
	default:
		if ok {
			w = fmax(numOr(v, 0), 0)
		}
	}
	switch {
	case !painted, sIs(n, "strokeAlignment", "inner", "inside"):
		return 0
	case sIs(n, "strokeAlignment", "outer", "outside"):
		return w
	}
	return w / 2
}

// paintOut is what paintCSS builds besides the element's own CSS.
type paintOut struct {
	// shadows are the box-shadows, topmost first.
	shadows []string
	// above are the inset shadows drawn in an overlay after the children (a clipping frame's).
	above []string
	// under goes first inside the element, over after its children.
	under, over strings.Builder
}

// fillsAndStrokes writes the background, radius and strokes of a box (not text or icons).
func (h *writer) fillsAndStrokes(n *resolve.RNode, css *strings.Builder, o *paintOut) {
	layers := fillLayers(n, func(f *jsonv.Object) (string, bool) { return h.rasterLayer(f, n) })
	// CSS lists the topmost background first. A translucent image cannot be a background layer: it and
	// every fill above it become overlays, and so do all fills when the bottom one blends with the backdrop
	// (a background only blends with its own layers).
	split := len(layers)
	if len(layers) > 0 && layers[0].blend != "" {
		split = 0
	} else {
		for i, l := range layers {
			if l.alpha < 1 {
				split = i
				break
			}
		}
	}
	if split > 0 {
		bg, modes := make([]string, 0, split), make([]string, 0, split)
		blended := false
		for i := split - 1; i >= 0; i-- {
			bg = append(bg, layers[i].css)
			mode := layers[i].blend
			if mode == "" {
				mode = "normal"
			} else {
				blended = true
			}
			modes = append(modes, mode)
		}
		css.WriteString("background:" + strings.Join(bg, ",") + ";")
		if blended {
			css.WriteString("background-blend-mode:" + strings.Join(modes, ",") + ";")
		}
	}
	for _, l := range layers[split:] {
		blend := ""
		if l.blend != "" {
			blend = "mix-blend-mode:" + l.blend + ";"
		}
		o.under.WriteString(`<div style="` + overlay + "inset:0;background:" + l.css + ";opacity:" + ff(l.alpha) + ";" + blend + "\"></div>\n")
	}
	r := svg.Radii(n)
	if r[0] > 0 || r[1] > 0 || r[2] > 0 || r[3] > 0 {
		css.WriteString("border-radius:" + ff(r[0]) + "px " + ff(r[1]) + "px " + ff(r[2]) + "px " + ff(r[3]) + "px;")
	}
	// No stroke without a width; alignment defaults to center. Every stroke is painted, bottom to top.
	strokes := fills(n, "stroke")
	blends := false
	for _, s := range strokes {
		blends = blends || blendOf(s) != ""
	}
	align, _ := n.S("strokeAlignment")
	inner, outer := sIs(n, "strokeAlignment", "inner", "inside"), sIs(n, "strokeAlignment", "outer", "outside")
	clipFrame := n.Type == "frame" && n.B("clip")
	width, hasWidth := prop(n, "strokeWidth")
	for _, paint := range strokes {
		stroke, hasStroke := "", false
		if c, ok := solid(paint); ok {
			stroke, hasStroke = cssColor(c)
		}
		gradient, hasGradient := "", false
		if po := jsonv.Obj(paint); po != nil && jsonv.StrIs(jsonv.Get(po, "type"), "gradient") {
			gradient, hasGradient = gradientCSS(po)
		}
		// A blending stroke is a masked ring too: only an overlay can blend with the layers beneath. Once one
		// is, every stroke is, so the stack keeps its order (a border or box-shadow would paint below it).
		if blends && hasWidth && (hasGradient || hasStroke) {
			l := gradient
			if !hasGradient {
				l = "linear-gradient(" + stroke + "," + stroke + ")"
			}
			o.under.WriteString(overlayOf(l, width, align, blendOf(paint)))
			continue
		}
		s, sides := width.(*jsonv.Object)
		switch {
		// A border has one colour and lies inside the box: only a lone inner stroke can use it, the rest become masked rings.
		case hasStroke && hasWidth && sides && len(strokes) == 1 && inner:
			for _, side := range sideNames {
				v, _ := s.Get(side)
				if w, ok := num(v); ok && w > 0 {
					css.WriteString("border-" + side + ":" + ff(w) + "px solid " + stroke + ";")
				}
			}
		case hasStroke && hasWidth && sides:
			grad := "linear-gradient(" + stroke + "," + stroke + ")"
			if clipFrame && !outer {
				// The inner part of the ring is drawn after the children, which would cover it below them; a centered stroke
				// keeps its outer half where it was (overflow clips it).
				half, _ := strokeSides(s)
				if !inner {
					for i, k := range sideNames {
						v, _ := s.Get(k)
						half[i] = halfSide(v)
					}
				}
				o.over.WriteString(strings.ReplaceAll(strokeOverlay(grad, half, "inner", ""), "z-index:-1;", ""))
				if !inner {
					o.under.WriteString(strokeOverlay(grad, half, "outer", ""))
				}
			} else {
				o.under.WriteString(overlayOf(grad, width, align, ""))
			}
		case hasStroke && hasWidth:
			w, ok := num(width)
			if !ok || !(w > 0) {
				break
			}
			// A centered stroke as an inner and an outer shadow leaves a hairline of the fill where their anti-aliased
			// edges meet on the outline; one ring half outside the box has a single edge per side. A clipping frame
			// would cut the outer half off, and a stack of strokes needs the shadows to keep its order.
			if len(strokes) == 1 && !inner && !outer && !clipFrame {
				half := w / 2
				// The outline's offset curve: each rounded corner grows by half the width.
				radius := "50%"
				if n.Type != "ellipse" {
					var rs [4]string
					for i, v := range svg.Radii(n) {
						if v > 0 {
							rs[i] = ff(v+half) + "px"
						} else {
							rs[i] = "0px"
						}
					}
					radius = strings.Join(rs[:], " ")
				}
				o.under.WriteString(`<div style="position:absolute;z-index:-1;pointer-events:none;inset:` + ff(-half) + "px;border-radius:" + radius +
					";box-shadow:inset 0 0 0 " + ff(w) + "px " + stroke + ";\"></div>\n")
				continue
			}
			// The first box-shadow is the topmost, so each stroke goes in front of the ones below it.
			var own []string
			switch {
			case inner:
				own = []string{"inset 0 0 0 " + ff(w) + "px " + stroke}
			case outer:
				own = []string{"0 0 0 " + ff(w) + "px " + stroke}
			default:
				own = []string{"inset 0 0 0 " + ff(w/2) + "px " + stroke, "0 0 0 " + ff(w/2) + "px " + stroke}
			}
			// An inset shadow paints below the children: a clipping frame draws it in an overlay after them.
			var in, rest []string
			for _, s := range own {
				if clipFrame && strings.HasPrefix(s, "inset ") {
					in = append(in, s)
				} else {
					rest = append(rest, s)
				}
			}
			o.above = append(in, o.above...)
			o.shadows = append(rest, o.shadows...)
		case !hasStroke && hasGradient && hasWidth:
			o.under.WriteString(overlayOf(gradient, width, align, ""))
		}
	}
}

// halfSide is a side width halved as `Value::from(num(v).unwrap_or(0.0) / 2.0)` reads back in strokeOverlay:
// 0 when it is not a finite positive number.
func halfSide(v jsonv.Value) float64 {
	x := numOr(v, 0) / 2
	if x != x || x-x != 0 {
		return 0
	}
	return fmax(x, 0)
}
