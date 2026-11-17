package html

// Text as a `<p>` (html.rs `text_css` and the text arm of `element`).

import (
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

func (h *writer) textCSS(n *resolve.RNode, css *strings.Builder) {
	st := layout.StyleOf(n)
	h.families = append(h.families, font{st.Family, st.Weight})
	css.WriteString("font-family:'" + svg.Esc(cssStr(st.Family)) + "',sans-serif;font-size:" + ff(st.Size) + "px;font-weight:" + strconv.Itoa(int(st.Weight)) + ";")
	if st.Italic {
		css.WriteString("font-style:italic;")
	}
	// Line heights round to whole pixels.
	if st.LineHeight > 0 {
		if lh := math.Round(st.LineHeight * st.Size); lh > 0 {
			css.WriteString("line-height:" + ff(lh) + "px;")
		}
	}
	if st.LetterSpacing != 0 {
		css.WriteString("letter-spacing:" + ff(st.LetterSpacing) + "px;")
	}
	// Only the schema's values: anything else would be written raw into the style attribute.
	if a, _ := n.S("textAlign"); sIs(n, "textAlign", "left", "center", "right", "justify") {
		css.WriteString("text-align:" + a + ";")
	}
	// Like the canvas, fixed-width text keeps its newlines and runs of spaces and breaks a word wider than the box,
	// except in a box of width 0, where a word is never split.
	switch {
	case sOr(n, "textGrowth", "auto") == "auto":
		css.WriteString("white-space:pre;")
	case n.W > 0:
		css.WriteString("white-space:pre-wrap;overflow-wrap:anywhere;")
	default:
		css.WriteString("white-space:pre-wrap;")
	}
	var deco []string
	if n.B("underline") {
		deco = append(deco, "underline")
	}
	if n.B("strikethrough") {
		deco = append(deco, "line-through")
	}
	if len(deco) > 0 {
		css.WriteString("text-decoration:" + strings.Join(deco, " ") + ";")
	}
	// A gradient or image fill is the background clipped to the glyphs.
	fillOpacity := 1.0
	clip, clipped := "", false
	all := fills(n, "fill")
	if len(all) > 0 {
		if o := jsonv.Obj(all[0]); o != nil {
			switch t, _ := paintType(o); t {
			case "gradient":
				clip, clipped = gradientCSS(o)
			case "image":
				var a float64
				if clip, a, clipped = imageLayer(o); clipped {
					fillOpacity = a
				}
			}
		}
	}
	if clipped {
		css.WriteString("background:" + clip + ";-webkit-background-clip:text;background-clip:text;color:transparent;")
		// Decorations default to the transparent colour; an opaque one lets the clipped gradient show through them.
		if len(deco) > 0 {
			css.WriteString("text-decoration-color:#000000;")
		}
		// A background has no opacity of its own: the element takes it (a later declaration beats the node's), which fades a stroke too.
		if fillOpacity < 1 {
			op, ok := n.F("opacity")
			if !ok {
				op = 1
			}
			css.WriteString("opacity:" + ff(op*fillOpacity) + ";")
		}
	} else if len(all) > 0 {
		if s, ok := solid(all[0]); ok {
			if c, ok := cssColor(s); ok {
				css.WriteString("color:" + c + ";")
			}
		}
	}
	// Like shapes, text takes the top width; CSS has no inner stroke, so it paints centered.
	var width float64
	var hasWidth bool
	if v, ok := prop(n, "strokeWidth"); ok {
		if s := jsonv.Obj(v); s != nil {
			top, _ := s.Get("top")
			width, hasWidth = num(top)
		} else {
			width, hasWidth = num(v)
		}
	}
	stroke, hasStroke := "", false
	if s := fills(n, "stroke"); len(s) > 0 {
		if c, ok := solid(s[0]); ok {
			stroke, hasStroke = cssColor(c)
		}
	}
	if hasStroke && hasWidth && width > 0 {
		// Stroke-only text is an outline: the inherited color must not fill the glyphs.
		if len(all) == 0 {
			css.WriteString("color:transparent;")
		}
		if sIs(n, "strokeAlignment", "outer", "outside") {
			css.WriteString("-webkit-text-stroke:" + ff(width*2) + "px " + stroke + ";paint-order:stroke fill;")
		} else {
			css.WriteString("-webkit-text-stroke:" + ff(width) + "px " + stroke + ";")
		}
	}
}

// text writes a text node with its style css.
func (h *writer) text(n *resolve.RNode, attrs, css string) {
	// Esc drops control characters, newlines too, so the lines are escaped one by one.
	content, _ := n.S("content")
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = svg.Esc(l)
	}
	// Like the canvas, only a fixed-size box places its text in the middle or at the bottom; overflow spills
	// evenly (middle) or upward (bottom). The text stays direct content, as a gradient or image fill clips to
	// it: a flex container wraps it in an anonymous item, and its newlines are kept by `pre-wrap`.
	if j := textJustify(n); j != "" {
		h.out.WriteString("<p" + attrs + ` style="` + css + "display:flex;flex-direction:column;justify-content:" + j + ";\">" + strings.Join(lines, "\n") + "</p>\n")
	} else {
		h.out.WriteString("<p" + attrs + ` style="` + css + `">` + strings.Join(lines, "<br>") + "</p>\n")
	}
}
