// Package html is the HTML+CSS export of nodes (core/src/html.rs `html_document`): each node becomes an element
// with inline CSS, auto layout maps 1:1 to flexbox, and what CSS cannot draw (paths, polygons, lines, arcs, icons
// with a paint, mesh gradients and shaders) is inlined as SVG or a PNG background. `html-tailwind` and `html` are
// this same output. The output is byte-identical to Rust's.
//
// Floating-point products that feed an addition are wrapped in explicit conversions, so they are never fused
// into a multiply-add.
package html

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	// Shader fills run on the GPU (it sets svg.DefaultShader).
	_ "github.com/naddrzz/nalarupa/core-go/shader"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// The export job writes html-css, html-tailwind and html files through Document.
func init() { export.HTMLDocument = Document }

// font is a family and weight the page uses, for its Google Fonts links.
type font struct {
	family string
	weight uint16
}

// Document is the HTML export of nodes ids (possibly nested), in order. opts holds `includeLayerNames` (default
// on: `data-name`), `includeLayerIds` (`data-id`) and `includeHtmlScaffold` (default on: a whole page with the
// Google Fonts it uses; off: the elements alone).
func Document(c *export.Ctx, ids []string, opts jsonv.Value) (string, error) {
	h := &writer{
		names:  !jsonv.IsFalse(jsonv.Get(opts, "includeLayerNames")),
		ids:    jsonv.IsTrue(jsonv.Get(opts, "includeLayerIds")),
		base:   c.Doc.Dir(),
		fonts:  c.Fonts,
		assets: c.Assets,
	}
	for _, id := range ids {
		top := c.Doc.TopIndex(id)
		if top < 0 {
			return "", errors.New(c.Missing(id))
		}
		root := c.LaidOut(top)
		if root == nil {
			return "", errors.New(c.Missing(id))
		}
		n := root.Find(id)
		if n == nil {
			return "", errors.New(c.Missing(id))
		}
		h.node(n, nil)
	}
	if jsonv.IsFalse(jsonv.Get(opts, "includeHtmlScaffold")) {
		return h.out.String(), nil
	}
	// One stylesheet per family with only the weights in use: Google rejects a request
	// naming a weight the family does not have.
	fonts := h.families
	sort.Slice(fonts, func(i, j int) bool {
		if fonts[i].family != fonts[j].family {
			return fonts[i].family < fonts[j].family
		}
		return fonts[i].weight < fonts[j].weight
	})
	fonts = dedup(fonts)
	var link strings.Builder
	for i := 0; i < len(fonts); {
		fam := fonts[i].family
		var weights []string
		for _, f := range fonts {
			if f.family == fam {
				weights = append(weights, strconv.Itoa(int(f.weight)))
			}
		}
		i += len(weights)
		if q, ok := fontQuery(fam); ok {
			link.WriteString(`<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=` + q + `:wght@` + strings.Join(weights, ";") + "&display=swap\">\n")
		}
	}
	return "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n<title>" +
		svg.Esc(c.Doc.Title()) + "</title>\n" + link.String() +
		"\n<style>*{box-sizing:border-box;margin:0;padding:0}body{display:flex;flex-direction:column;align-items:center;gap:40px;padding:40px 0;background:#f4f4f5}</style>\n</head>\n<body>\n" +
		h.out.String() + "</body>\n</html>\n", nil
}

func dedup(fonts []font) []font {
	out := fonts[:0]
	for i, f := range fonts {
		if i == 0 || f != fonts[i-1] {
			out = append(out, f)
		}
	}
	return out
}

// fontQuery is a family name as the `family=` query value of a Google Fonts URL; false when it holds characters
// a font name cannot have (it would only break out of the attribute or the query).
func fontQuery(fam string) (string, bool) {
	if fam == "" {
		return "", false
	}
	for _, c := range fam {
		if !alphanumeric(c) && c != ' ' && c != '-' && c != '_' && c != '.' {
			return "", false
		}
	}
	var q strings.Builder
	for _, c := range fam {
		switch {
		case c == ' ':
			q.WriteByte('+')
		case c < 0x80:
			q.WriteRune(c)
		default:
			for _, b := range []byte(string(c)) {
				fmt.Fprintf(&q, "%%%02X", b)
			}
		}
	}
	return q.String(), true
}

// alphanumeric is Rust's `char::is_alphanumeric`: the Alphabetic property or a number (Nd, Nl, No).
func alphanumeric(c rune) bool {
	return unicode.IsLetter(c) || unicode.Is(unicode.Other_Alphabetic, c) || unicode.IsNumber(c)
}

// cssStr is s as the inside of a single-quoted CSS string (before HTML-escaping it for the `style` attribute).
// A raw line break ends a CSS string (the declaration is dropped), so those become hex escapes.
func cssStr(s string) string {
	return strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\a `, "\r", `\d `, "\x0c", `\c `).Replace(s)
}

// writer is html.rs `Html`: the page being written and what it collects on the way.
type writer struct {
	out      strings.Builder
	names    bool
	ids      bool
	base     string
	families []font
	fonts    layout.TextMeasurer
	assets   *svg.Assets
	// svgIDs is the last id the inline SVGs used; the next one numbers its ids after it.
	svgIDs int
}

// node writes n. A rotated or flipped node in a flow (or at the top level) sits in a wrapper of its bounding box.
func (h *writer) node(n, parent *resolve.RNode) {
	inFlow := parent != nil && parent.Type == "frame" && !sIs(parent, "layout", "none") && !sIs(n, "layoutPosition", "absolute")
	r, ok := n.F("rotation")
	tilted := ok && r != 0 || n.B("flipX") || n.B("flipY")
	if !((inFlow || parent == nil) && tilted) {
		h.element(n, parent, nil)
		return
	}
	// The canvas places a rotated or flipped child by its bounding box, so it takes that much room in the
	// flow: the box is a wrapper of that size and the element sits in it with its origin where the layout put it.
	// A top-level layer is the same: its paint reaches up and left of its origin, where the page cannot scroll.
	l := layout.LocalAABB(n)
	h.out.WriteString(`<div style="position:relative;flex-shrink:0;width:` + ff(px(l[2]-l[0])) + `px;height:` + ff(px(l[3]-l[1])) + "px;\">\n")
	h.element(n, parent, &[2]float64{n.BX - l[0], n.BY - l[1]})
	h.out.WriteString("</div>\n")
}

func (h *writer) attrs(n *resolve.RNode) string {
	a := ""
	if h.names {
		a += ` data-name="` + svg.Esc(n.Name()) + `"`
	}
	if h.ids {
		a += ` data-id="` + svg.Esc(n.ID) + `"`
	}
	return a
}

// ── shared helpers ──────────────────────────────────────────────────────────

// pi is a variable so pi/180 rounds in float64, as Rust's to_radians does.
var pi = math.Pi

func toRad(d float64) float64 { return d * (pi / 180) }

// ff writes a float as Rust's `{}` does: the shortest digits that round-trip, never an exponent.
func ff(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// px rounds so trig noise (6e-15 at 90deg) stays out of the CSS; `+ 0` turns -0 into 0.
func px(v float64) float64 {
	return math.Round(v*1e6)/1e6 + 0
}

// fmax is Rust's f64::max: a NaN operand gives the other one.
func fmax(a, b float64) float64 {
	if math.IsNaN(a) || b > a {
		return b
	}
	return a
}

// clamp is Rust's f64::clamp (NaN stays NaN).
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func num(v jsonv.Value) (float64, bool) { return resolve.Num(v) }

func numOr(v jsonv.Value, d float64) float64 {
	if x, ok := num(v); ok {
		return x
	}
	return d
}

func str(v jsonv.Value) string {
	s, _ := jsonv.Str(v)
	return s
}

// sIs is `n.s(k) == Some(v)` for any of vals.
func sIs(n *resolve.RNode, k string, vals ...string) bool {
	s, ok := n.S(k)
	if !ok {
		return false
	}
	for _, v := range vals {
		if s == v {
			return true
		}
	}
	return false
}

// sOr is `n.s(k).unwrap_or(d)`.
func sOr(n *resolve.RNode, k, d string) string {
	if s, ok := n.S(k); ok {
		return s
	}
	return d
}

// prop is `n.p.get(k)`: the value and whether it is set.
func prop(n *resolve.RNode, k string) (jsonv.Value, bool) { return n.P.Get(k) }

// fills is svg.rs `fills` of `n.p.get(k)`.
func fills(n *resolve.RNode, k string) []jsonv.Value {
	v, _ := n.P.Get(k)
	return svg.Fills(v)
}

// paintType is `f["type"].as_str()`.
func paintType(f jsonv.Value) (string, bool) { return jsonv.Str(jsonv.Get(f, "type")) }

// fillType is `o.get("type").and_then(Value::as_str).unwrap_or("color")`.
func fillType(o *jsonv.Object) string {
	if t, ok := paintType(o); ok {
		return t
	}
	return "color"
}

// solid is a paint's flat colour text: a string, or the `color` of an object.
func solid(f jsonv.Value) (string, bool) {
	if s, ok := jsonv.Str(f); ok {
		return s, true
	}
	return jsonv.Str(jsonv.Get(f, "color"))
}

// blendOf is `blend_mode(f["blendMode"].as_str())`, "" for none.
func blendOf(f jsonv.Value) string { return svg.BlendMode(str(jsonv.Get(f, "blendMode"))) }
