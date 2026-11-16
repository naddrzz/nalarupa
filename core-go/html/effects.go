package html

// Shadows, blur and backdrop blur (the second half of html.rs `paint_css`, and `shadow_filter`).

import (
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// shadowFx is one enabled shadow effect: offset, blur radius and CSS colour.
type shadowFx struct {
	inner      bool
	x, y, blur float64
	color      string
}

// shadowList is the node's enabled shadows. Defaults: opaque black, no blur; an invalid colour drops the shadow.
func shadowList(n *resolve.RNode) []shadowFx {
	var effects []jsonv.Value
	switch v, _ := prop(n, "effect"); x := v.(type) {
	case *jsonv.Array:
		effects = x.Items
	case *jsonv.Object:
		effects = []jsonv.Value{x}
	}
	var list []shadowFx
	for _, e := range effects {
		if !jsonv.StrIs(jsonv.Get(e, "type"), "shadow") || jsonv.IsFalse(jsonv.Get(e, "enabled")) {
			continue
		}
		c, ok := jsonv.Str(jsonv.Get(e, "color"))
		if !ok {
			c = "#000000"
		}
		col, ok := cssColor(c)
		if !ok {
			continue
		}
		off := jsonv.Get(e, "offset")
		list = append(list, shadowFx{
			inner: jsonv.StrIs(jsonv.Get(e, "shadowType"), "inner"),
			x:     numOr(jsonv.Get(off, "x"), 0), y: numOr(jsonv.Get(off, "y"), 0), blur: numOr(jsonv.Get(e, "blur"), 0),
			color: col,
		})
	}
	return list
}

// paintCSS writes the background, radius, stroke, shadows and blur of n into css. It returns the overlay boxes
// (translucent image fills, gradient strokes) that go first inside the element, and the ones that go after its
// children (the inner stroke of a clipping frame, which is drawn above them).
func (h *writer) paintCSS(n *resolve.RNode, css *strings.Builder) (string, string) {
	var o paintOut
	if n.Type != "text" && n.Type != "icon" {
		h.fillsAndStrokes(n, css, &o)
	}
	list := shadowList(n)
	// The box has no shadow of its own to follow: text and groups cast the shadow of their glyphs and children,
	// and CSS cannot draw an inner shadow there.
	var filters []string
	gradientText := false
	if f := fills(n, "fill"); n.Type == "text" && len(f) > 0 {
		t, _ := paintType(f[0])
		gradientText = t == "gradient" || t == "image"
	}
	switch {
	case n.Type == "text" || n.Type == "group" || n.Type == "icon":
		var outer []shadowFx
		for _, s := range list {
			if !s.inner {
				outer = append(outer, s)
			}
		}
		if n.Type == "text" && !gradientText {
			var t []string
			for i := len(outer) - 1; i >= 0; i-- {
				s := outer[i]
				t = append(t, ff(s.x)+"px "+ff(s.y)+"px "+ff(s.blur)+"px "+s.color)
			}
			if len(t) > 0 {
				css.WriteString("text-shadow:" + strings.Join(t, ",") + ";")
			}
		} else if len(outer) == 1 {
			// A text-shadow would show through gradient text, whose colour is transparent. `drop-shadow()` takes
			// the standard deviation, half the blur radius.
			s := outer[0]
			filters = append(filters, "drop-shadow("+ff(s.x)+"px "+ff(s.y)+"px "+ff(s.blur/2)+"px "+s.color+")")
		} else if len(outer) > 0 {
			// Chained `drop-shadow()`s would cast shadows of the earlier shadows: every one needs the original alpha.
			filters = append(filters, shadowFilter(outer))
		}
	case n.Type == "frame" || visibleFill(n):
		// Like the canvas, a shape without a visible fill casts nothing (a box-shadow of a transparent box would
		// still paint its outer band); a frame always casts its outer shadow, which is only drawn outside it.
		// A drop shadow is cast by the outline with the stroke's outer part: it spreads by that reach. The canvas paints
		// the later effect on top and the first CSS shadow is the topmost, so the list goes in reverse.
		reach := strokeReach(n)
		for i := len(list) - 1; i >= 0; i-- {
			s := list[i]
			switch {
			case !s.inner && reach > 0:
				o.shadows = append(o.shadows, ff(s.x)+"px "+ff(s.y)+"px "+ff(s.blur)+"px "+ff(reach)+"px "+s.color)
			case s.inner:
				o.shadows = append(o.shadows, "inset "+ff(s.x)+"px "+ff(s.y)+"px "+ff(s.blur)+"px "+s.color)
			default:
				o.shadows = append(o.shadows, ff(s.x)+"px "+ff(s.y)+"px "+ff(s.blur)+"px "+s.color)
			}
		}
	}
	if len(o.above) > 0 {
		o.over.WriteString(`<div style="position:absolute;inset:0;border-radius:inherit;pointer-events:none;box-shadow:` + strings.Join(o.above, ",") + ";\"></div>\n")
	}
	positioned := func() bool { return strings.Contains(css.String(), "position:") }
	// The overlays after the children stack against this box.
	if o.over.Len() > 0 && !positioned() {
		css.WriteString("position:relative;")
	}
	under := o.under.String()
	// Overlays would cover the element's own inset shadows (inner strokes and shadows): paint those above them.
	if under != "" {
		var inner, rest []string
		for _, s := range o.shadows {
			if strings.HasPrefix(s, "inset ") {
				inner = append(inner, s)
			} else {
				rest = append(rest, s)
			}
		}
		o.shadows = rest
		if len(inner) > 0 {
			under += `<div style="` + overlay + "inset:0;box-shadow:" + strings.Join(inner, ",") + ";\"></div>\n"
		}
		// Overlays stack against this box, inside it.
		if !positioned() {
			css.WriteString("position:relative;")
		}
		// Overlays that blend with the backdrop go in the page's stacking order, not in a box of their own.
		if blendsBackdrop(n) {
			under = strings.ReplaceAll(under, "z-index:-1;", "")
		} else {
			css.WriteString("isolation:isolate;")
		}
	}
	if len(o.shadows) > 0 {
		css.WriteString("box-shadow:" + strings.Join(o.shadows, ",") + ";")
	}
	// CSS `blur()` takes the standard deviation, which is half the radius.
	fx := svg.EffectsOf(n)
	if fx.Blur > 0 {
		filters = append(filters, "blur("+ff(math.Round(fx.Blur*100)/100)+"px)")
	}
	if len(filters) > 0 {
		css.WriteString("filter:" + strings.Join(filters, " ") + ";")
	}
	// Like the canvas, a node below full opacity shows no background blur.
	if op, ok := n.F("opacity"); fx.Backdrop > 0 && (!ok || op >= 1) {
		b := ff(math.Round(fx.Backdrop*100) / 100)
		css.WriteString("backdrop-filter:blur(" + b + "px);-webkit-backdrop-filter:blur(" + b + "px);")
	}
	return under, o.over.String()
}

// shadowFilter is one `filter` casting every outer shadow from the element's own alpha (unlike chained
// `drop-shadow()`s, which shadow each other), as an inline SVG filter. Its blur is the standard deviation, half
// the radius.
func shadowFilter(shadows []shadowFx) string {
	var f, merge strings.Builder
	m := 0.0
	for i, s := range shadows {
		is := strconv.Itoa(i)
		f.WriteString(`<feGaussianBlur in="SourceAlpha" stdDeviation="` + ff(s.blur/2) + `"/><feOffset dx="` + ff(s.x) + `" dy="` + ff(s.y) + `" result="o` + is +
			`"/><feFlood flood-color="` + s.color + `"/><feComposite in2="o` + is + `" operator="in" result="s` + is + `"/>`)
		merge.WriteString(`<feMergeNode in="s` + is + `"/>`)
		// The region must hold every offset, blurred shadow: a percentage of the element would clip big ones. The
		// element's size is not known here, so it starts a margin before the origin and runs far past any element.
		m = fmax(m, fmax(math.Abs(s.x), math.Abs(s.y))+float64(s.blur*1.5))
	}
	ms := ff(math.Ceil(m) + 1)
	doc := `<svg xmlns="http://www.w3.org/2000/svg"><filter id="s" filterUnits="userSpaceOnUse" x="-` + ms + `" y="-` + ms +
		`" width="100000" height="100000" color-interpolation-filters="sRGB">` + f.String() + `<feMerge>` + merge.String() +
		`<feMergeNode in="SourceGraphic"/></feMerge></filter></svg>`
	return "url('data:image/svg+xml," + strings.NewReplacer("%", "%25", "<", "%3C", ">", "%3E", "#", "%23", `"`, "%22").Replace(doc) + "#s')"
}
