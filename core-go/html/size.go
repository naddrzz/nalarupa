package html

import (
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// spec is an optional width or height value (`Option<&Value>`).
type spec struct {
	v   jsonv.Value
	set bool
}

// numSpec is `Some(&Value::from(x))`: a number, or null when x is not finite (serde has no such number).
func numSpec(x float64) spec {
	if x != x || x-x != 0 {
		return spec{nil, true}
	}
	return spec{jsonv.Number(strconv.FormatFloat(x, 'g', -1, 64)), true}
}

func propSpec(n *resolve.RNode, k string) spec {
	v, ok := n.P.Get(k)
	return spec{v, ok}
}

func (s spec) fit() bool {
	t, ok := jsonv.Str(s.v)
	return s.set && ok && strings.HasPrefix(t, "fit_content")
}

// parentDir is the layout of the flow a node sits in (`Option<&str>`): "none" for a parent without one.
type parentDir struct {
	dir string
	set bool
}

// sizeCSS is the CSS of a width (horiz) or height value of a node in a flow of dir.
func sizeCSS(v spec, prop string, pd parentDir, horiz bool) string {
	mainAxis := false
	switch {
	case pd.set && pd.dir == "horizontal":
		mainAxis = horiz
	case pd.set && pd.dir == "vertical":
		mainAxis = !horiz
	// Like the layout engine, any other layout value on a frame (e.g. an imported one) is a row.
	case pd.set && pd.dir != "none":
		mainAxis = horiz
	}
	if !v.set {
		return ""
	}
	if s, ok := v.v.(string); ok {
		switch {
		case strings.HasPrefix(s, "fill_container"):
			if pd.set && pd.dir != "none" {
				if mainAxis {
					return "flex:1 1 0;min-width:0;min-height:0;"
				}
				return "align-self:stretch;"
			}
			if i := strings.IndexByte(s, '('); i >= 0 {
				// No layout to fill: the canvas uses the fallback in the parentheses.
				if n, ok := resolve.ParseFloat(strings.TrimSpace(strings.TrimRight(s[i+1:], ")"))); ok {
					return prop + ":" + ff(fmax(n, 0)) + "px;"
				}
			}
			return prop + ":100%;"
		case strings.HasPrefix(s, "fit_content"):
			return prop + ":fit-content;"
		}
	}
	n, ok := num(v.v)
	if !ok {
		return ""
	}
	// Fixed main-axis sizes do not shrink (the canvas lets them overflow); CSS flex items would.
	if mainAxis {
		return prop + ":" + ff(n) + "px;flex-shrink:0;"
	}
	return prop + ":" + ff(n) + "px;"
}

// textJustify is the `justify-content` placing the text of a fixed-size box in the middle or at the bottom;
// "" keeps it at the top.
func textJustify(n *resolve.RNode) string {
	if !sIs(n, "textGrowth", "fixed-width-height") {
		return ""
	}
	switch v, _ := n.S("textAlignVertical"); v {
	case "middle":
		return "center"
	case "bottom":
		return "flex-end"
	}
	return ""
}

// element writes n; slot is where its unrotated box sits in its wrapper (see node), or nil.
func (h *writer) element(n, parent *resolve.RNode, slot *[2]float64) {
	// A wrapped node is sized in its wrapper, not as a flex item.
	var pd parentDir
	if parent != nil && slot == nil {
		pd = parentDir{"none", true}
		if parent.Type == "frame" {
			pd.dir = sOr(parent, "layout", "horizontal")
		}
	}
	absolute := slot != nil || pd.set && pd.dir == "none" || sIs(n, "layoutPosition", "absolute")
	var css strings.Builder
	switch {
	case slot != nil:
		css.WriteString("position:absolute;left:" + ff(px(slot[0])) + "px;top:" + ff(px(slot[1])) + "px;")
	case parent == nil:
		css.WriteString("position:relative;")
	case absolute:
		css.WriteString("position:absolute;left:" + ff(n.X-parent.X) + "px;top:" + ff(n.Y-parent.Y) + "px;")
	case blendsBackdrop(parent):
		// The parent's blending overlays are not behind its content: a child must be positioned to paint above them.
		css.WriteString("position:relative;")
	}
	textAuto := n.Type == "text" && sOr(n, "textGrowth", "auto") == "auto"
	if !textAuto {
		wv, hv := propSpec(n, "width"), propSpec(n, "height")
		if slot != nil {
			// In a wrapper the laid-out size is final: fill and fit have no flow left to resolve against.
			wv, hv = numSpec(px(n.W)), numSpec(px(n.H))
		}
		// fit_content(N) on a node with nothing to hug is N (the laid-out size); the CSS keyword would collapse it to 0.
		// A frame without a size is fit_content too; when it hugs a fill child CSS would size it from that child's content.
		if wv.fit() && !layout.Hugs(n, 0) || layout.HugsFill(n, 0) {
			wv = numSpec(px(n.W))
		}
		if hv.fit() && !layout.Hugs(n, 1) || layout.HugsFill(n, 1) {
			hv = numSpec(px(n.H))
		}
		css.WriteString(sizeCSS(wv, "width", pd, true))
		css.WriteString(sizeCSS(hv, "height", pd, false))
		// Fixed-width text without a width wraps at width 0 on the canvas: one word per line.
		if n.Type == "text" && !wv.set {
			css.WriteString(sizeCSS(numSpec(px(n.W)), "width", pd, true))
		}
		// Likewise a fixed-size box without a height is as high as the layout made it (0 unless set), which middle and bottom text spills out of.
		if n.Type == "text" && !hv.set && textJustify(n) != "" {
			css.WriteString(sizeCSS(numSpec(px(n.H)), "height", pd, false))
		}
		switch n.Type {
		case "rectangle", "ellipse", "icon", "polygon", "line", "path":
			if !wv.set {
				css.WriteString(sizeCSS(numSpec(n.W), "width", pd, true))
			}
			if !hv.set {
				css.WriteString(sizeCSS(numSpec(n.H), "height", pd, false))
			}
		}
	}
	if o, ok := n.F("opacity"); ok {
		css.WriteString("opacity:" + ff(o) + ";")
	}
	// Rotation is CCW and flips are about the top-left corner.
	tf := ""
	if r, ok := n.F("rotation"); ok && r != 0 {
		tf += "rotate(" + ff(-r) + "deg)"
	}
	if n.B("flipX") || n.B("flipY") {
		sx, sy := "1", "1"
		if n.B("flipX") {
			sx = "-1"
		}
		if n.B("flipY") {
			sy = "-1"
		}
		tf += " scale(" + sx + "," + sy + ")"
	}
	if tf != "" {
		// A group's box starts at (bx, by) from the origin it turns about.
		css.WriteString("transform:" + strings.TrimLeft(tf, " ") + ";transform-origin:" + ff(px(-n.BX)) + "px " + ff(px(-n.BY)) + "px;")
	}
	// Shapes CSS cannot draw are inlined as SVG, which carries their own paint.
	// An icon with a paint other than a plain colour (gradient, image, mesh, shader) is drawn by the SVG exporter too.
	paintIcon := false
	if n.Type == "icon" {
		for _, f := range fills(n, "fill") {
			if t, ok := paintType(f); jsonv.Obj(f) != nil && ok && t != "color" {
				paintIcon = true
			}
		}
	}
	asSVG := paintIcon || n.Type == "path" || n.Type == "polygon" || n.Type == "line"
	if n.Type == "ellipse" {
		inner, _ := n.F("innerRadius")
		sweep, ok := n.F("sweepAngle")
		asSVG = asSVG || inner > 0 || ok && math.Abs(sweep) < 360
	}
	var under, over string
	if !asSVG {
		under, over = h.paintCSS(n, &css)
	}
	attrs := h.attrs(n)
	switch {
	case n.Type == "text":
		h.textCSS(n, &css)
		h.text(n, attrs, css.String())
	case n.Type == "icon" && !asSVG:
		h.icon(n, attrs, css.String())
	case asSVG:
		h.vector(n, attrs, css.String())
	default:
		h.box(n, parent, absolute, pd, &css, attrs, under, over)
	}
}

// box writes a frame, group, rectangle or ellipse with its children between the overlays.
func (h *writer) box(n, parent *resolve.RNode, absolute bool, pd parentDir, css *strings.Builder, attrs, under, over string) {
	positioned := func() bool { return strings.Contains(css.String(), "position:") }
	switch n.Type {
	case "frame":
		switch dir := sOr(n, "layout", "horizontal"); dir {
		// An absolute frame already contains its children; a later position:relative would override it.
		case "none":
			if !absolute && !positioned() {
				css.WriteString("position:relative;")
			}
		default:
			flow := "row"
			if dir == "vertical" {
				flow = "column"
			}
			css.WriteString("display:flex;flex-direction:" + flow + ";")
			// space_between/space_around ignore the gap.
			if g, ok := n.F("gap"); ok && !sIs(n, "justifyContent", "space_between", "space_around") {
				css.WriteString("gap:" + ff(g) + "px;")
			}
			j := "flex-start"
			switch v, _ := n.S("justifyContent"); v {
			case "center":
				j = "center"
			case "end":
				j = "flex-end"
			case "space_between":
				j = "space-between"
			case "space_around":
				j = "space-around"
			}
			a := "flex-start"
			switch v, _ := n.S("alignItems"); v {
			case "center":
				a = "center"
			case "end":
				a = "flex-end"
			}
			css.WriteString("justify-content:" + j + ";align-items:" + a + ";")
			if parent != nil && !absolute {
				css.WriteString("flex-shrink:0;")
			}
		}
		if p := layout.Padding(n); p[0] != 0 || p[1] != 0 || p[2] != 0 || p[3] != 0 {
			css.WriteString("padding:" + ff(p[0]) + "px " + ff(p[1]) + "px " + ff(p[2]) + "px " + ff(p[3]) + "px;")
		}
		if n.B("clip") {
			css.WriteString("overflow:hidden;")
		}
	case "group":
		if !absolute && !positioned() {
			css.WriteString("position:relative;")
		}
		css.WriteString(sizeCSS(numSpec(n.W), "width", pd, true))
		css.WriteString(sizeCSS(numSpec(n.H), "height", pd, false))
	case "ellipse":
		css.WriteString("border-radius:50%;")
	}
	h.out.WriteString("<div" + attrs + ` style="` + css.String() + "\">\n")
	h.out.WriteString(under)
	for _, k := range n.Kids {
		h.node(k, n)
	}
	h.out.WriteString(over)
	h.out.WriteString("</div>\n")
}
