package svg

// Fills, strokes and effects as SVG paint (svg.rs `Paint`, `Effects`, `paints`, `gradient`, `shader`).

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// paint is one fill or stroke paint: an SVG paint server value with its alpha and blend mode.
type paint struct {
	value string
	alpha float64
	blend string // "" when none
}

func (p paint) attrs(prop string) string {
	style := ""
	if p.blend != "" {
		style = ` style="mix-blend-mode:` + p.blend + `"`
	}
	return " " + prop + `="` + p.value + `" ` + prop + `-opacity="` + f(p.alpha) + `"` + style
}

var black = []paint{{value: "#000", alpha: 1}}

type shadow struct {
	dx, dy, sigma float64
	color         string
	alpha         float64
	blend         string
	inner         bool
}

type effects struct {
	shadows []shadow
	// blur is the layer blur sigma (radius / 2; several blurs compound).
	blur float64
	// backdrop is the background blur sigma (the largest of several).
	backdrop float64
}

// effectList is the enabled entries of an `effect` value (one object or an array).
func effectList(v jsonv.Value) []jsonv.Value {
	var list []jsonv.Value
	switch x := v.(type) {
	case *jsonv.Array:
		list = x.Items
	case *jsonv.Object:
		list = []jsonv.Value{x}
	}
	out := list[:0:0]
	for _, e := range list {
		if !jsonv.IsFalse(jsonv.Get(e, "enabled")) {
			out = append(out, e)
		}
	}
	return out
}

func effectsOf(n *resolve.RNode) effects {
	var fx effects
	v, _ := n.P.Get("effect")
	for _, e := range effectList(v) {
		sigma := func(k string) float64 { return fmax(numOr(jsonv.Get(e, k), 0), 0) / 2 }
		switch t, _ := jsonv.Str(jsonv.Get(e, "type")); t {
		case "shadow":
			c, a := "#000000", 1.0
			if cv, ok := get(e, "color"); ok {
				s, isStr := jsonv.Str(cv)
				var good bool
				if isStr {
					c, a, good = Color(s)
				}
				if !good {
					continue
				}
			}
			off := jsonv.Get(e, "offset")
			mode, _ := jsonv.Str(jsonv.Get(e, "blendMode"))
			fx.shadows = append(fx.shadows, shadow{
				dx: numOr(jsonv.Get(off, "x"), 0), dy: numOr(jsonv.Get(off, "y"), 0), sigma: sigma("blur"),
				color: c, alpha: a, blend: BlendMode(mode), inner: jsonv.StrIs(jsonv.Get(e, "shadowType"), "inner"),
			})
		case "blur":
			fx.blur = math.Hypot(fx.blur, sigma("radius"))
		case "background_blur":
			fx.backdrop = fmax(fx.backdrop, sigma("radius"))
		}
	}
	return fx
}

// reach is how far drop shadows reach past the source outline.
func (fx effects) reach() float64 {
	r := 0.0
	for _, s := range fx.shadows {
		r = fmax(r, fmax(math.Abs(s.dx), math.Abs(s.dy))+3*s.sigma)
	}
	return r
}

type stroke struct {
	paints []paint
	// widths are top, right, bottom, left.
	widths [4]float64
	// align is "inner", "center" or "outer".
	align string
}

func (s *stroke) k() float64 {
	if s.align == "center" {
		return 1
	}
	return 2
}

// reach is how far the stroke reaches outside the fill outline.
func (s *stroke) reach() float64 {
	w := 0.0
	for _, x := range s.widths {
		w = fmax(w, x)
	}
	switch s.align {
	case "inner":
		return 0
	case "outer":
		return w
	}
	return w / 2
}

// Esc escapes text for SVG attributes and content; control characters other than tab are dropped.
func Esc(s string) string {
	var o strings.Builder
	o.Grow(len(s))
	for _, c := range s {
		switch {
		case c == '&':
			o.WriteString("&amp;")
		case c == '<':
			o.WriteString("&lt;")
		case c == '>':
			o.WriteString("&gt;")
		case c == '"':
			o.WriteString("&quot;")
		case c == '\'':
			o.WriteString("&apos;")
		case c < 0x20 && c != '\t':
		default:
			o.WriteRune(c)
		}
	}
	return o.String()
}

// RGBA8 parses `#RGB`, `#RRGGBB` or `#RRGGBBAA`.
func RGBA8(s string) ([4]uint8, bool) {
	h, ok := strings.CutPrefix(strings.TrimSpace(s), "#")
	if !ok {
		return [4]uint8{}, false
	}
	for i := 0; i < len(h); i++ {
		if !isHex(h[i]) {
			return [4]uint8{}, false
		}
	}
	hx := func(a, b byte) uint8 {
		v, _ := strconv.ParseUint(string([]byte{a, b}), 16, 8)
		return uint8(v)
	}
	switch len(h) {
	case 3:
		return [4]uint8{hx(h[0], h[0]), hx(h[1], h[1]), hx(h[2], h[2]), 255}, true
	case 6:
		return [4]uint8{hx(h[0], h[1]), hx(h[2], h[3]), hx(h[4], h[5]), 255}, true
	case 8:
		return [4]uint8{hx(h[0], h[1]), hx(h[2], h[3]), hx(h[4], h[5]), hx(h[6], h[7])}, true
	}
	return [4]uint8{}, false
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// Color parses a color into (`#rrggbb`, alpha); anything else (`#RGBA`, CSS names, unresolved
// `$variables`) is transparent, i.e. false.
func Color(s string) (string, float64, bool) {
	c, ok := RGBA8(s)
	if !ok {
		return "", 0, false
	}
	return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2]), float64(c[3]) / 255, true
}

func rgba(s string) ([4]float64, bool) {
	c, ok := RGBA8(s)
	if !ok {
		return [4]float64{}, false
	}
	return [4]float64{float64(c[0]) / 255, float64(c[1]) / 255, float64(c[2]) / 255, float64(c[3]) / 255}, true
}

func hex(c [4]float64) string {
	b := func(v float64) uint8 { return uint8(math.Round(clamp(v, 0, 1) * 255)) }
	return fmt.Sprintf("#%02x%02x%02x", b(c[0]), b(c[1]), b(c[2]))
}

// Fills is the enabled fills of a `fill`/`stroke` value (one fill or an array).
func Fills(v jsonv.Value) []jsonv.Value {
	switch x := v.(type) {
	case *jsonv.Array:
		var out []jsonv.Value
		for _, f := range x.Items {
			if !jsonv.IsFalse(jsonv.Get(f, "enabled")) {
				out = append(out, f)
			}
		}
		return out
	case string:
		return []jsonv.Value{x}
	case *jsonv.Object:
		if !jsonv.IsFalse(jsonv.Get(x, "enabled")) {
			return []jsonv.Value{x}
		}
	}
	return nil
}

// Flat is a fill as one flat color, read as paints reads it: a color fill gives its RGBA (transparent
// when unparseable); a gradient, image, mesh or shader gives false.
func Flat(v jsonv.Value) ([4]float64, bool) {
	var c string
	switch x := v.(type) {
	case string:
		c = x
	case *jsonv.Object:
		if t, ok := jsonv.Str(jsonv.Get(x, "type")); ok && t != "color" {
			return [4]float64{}, false
		}
		c, _ = jsonv.Str(jsonv.Get(x, "color"))
	default:
		return [4]float64{}, false
	}
	out, _ := rgba(c)
	return out, true
}

// get is serde's `v.get(k)`: the value and whether v is an object holding k.
func get(v jsonv.Value, k string) (jsonv.Value, bool) {
	if o, ok := v.(*jsonv.Object); ok && o != nil {
		return o.Get(k)
	}
	return nil, false
}

// fillType is `o.get("type").and_then(as_str).unwrap_or("color")`.
func fillType(o *jsonv.Object) string {
	if t, ok := jsonv.Str(jsonv.Get(o, "type")); ok {
		return t
	}
	return "color"
}

// Blends reports whether the node or a descendant paints with a blend mode (a fill, stroke or shadow).
func Blends(n *resolve.RNode) bool {
	paintBlends := func(k string) bool {
		v, _ := n.P.Get(k)
		for _, f := range Fills(v) {
			if m, _ := jsonv.Str(jsonv.Get(f, "blendMode")); BlendMode(m) != "" {
				return true
			}
		}
		return false
	}
	if paintBlends("fill") || paintBlends("stroke") {
		return true
	}
	for _, s := range effectsOf(n).shadows {
		if s.blend != "" {
			return true
		}
	}
	for _, k := range n.Kids {
		if Blends(k) {
			return true
		}
	}
	return false
}

// BlendMode is a blend mode as CSS `mix-blend-mode`, or "" for none. linearBurn is multiply and
// linearDodge (Skia plus) is approximated by screen, since resvg has no additive mode.
func BlendMode(mode string) string {
	switch mode {
	case "darken", "overlay", "difference", "exclusion", "hue", "saturation", "color", "luminosity":
		return mode
	case "multiply", "linearBurn":
		return "multiply"
	case "colorBurn":
		return "color-burn"
	case "light":
		return "lighten"
	case "screen", "linearDodge":
		return "screen"
	case "colorDodge":
		return "color-dodge"
	case "softLight":
		return "soft-light"
	case "hardLight":
		return "hard-light"
	}
	return ""
}

type stop struct {
	pos float64
	c   [4]float64
}

// stopTags writes gradient stops; where alpha changes, extra stops emulate Skia's premultiplied interpolation.
func stopTags(stops []stop) string {
	var out strings.Builder
	push := func(p float64, c [4]float64) {
		out.WriteString(`<stop offset="` + f(p) + `" stop-color="` + hex(c) + `" stop-opacity="` + f(c[3]) + `"/>`)
	}
	for i, s := range stops {
		if i > 0 {
			pp, pc := stops[i-1].pos, stops[i-1].c
			if math.Abs(pc[3]-s.c[3]) > 1e-3 && s.pos > pp {
				for k := 1; k < 8; k++ {
					t := float64(k) / 8
					push(pp+(s.pos-pp)*t, mix(pc, s.c, t))
				}
			}
		}
		push(s.pos, s.c)
	}
	return out.String()
}

// mix is premultiplied interpolation, returned unpremultiplied.
func mix(a, b [4]float64, t float64) [4]float64 {
	al := a[3] + (b[3]-a[3])*t
	if al <= 0 {
		return [4]float64{}
	}
	ch := func(i int) float64 { return (a[i]*a[3]*(1-t) + b[i]*b[3]*t) / al }
	return [4]float64{ch(0), ch(1), ch(2), al}
}

// sample is the color at t with clamped ends.
func sample(stops []stop, t float64) [4]float64 {
	i := sort.Search(len(stops), func(i int) bool { return !(stops[i].pos <= t) })
	switch {
	case i > 0 && i < len(stops) && stops[i].pos > stops[i-1].pos:
		a, b := stops[i-1], stops[i]
		return mix(a.c, b.c, (t-a.pos)/(b.pos-a.pos))
	case i > 0:
		return stops[i-1].c
	case i < len(stops):
		return stops[i].c
	}
	return [4]float64{}
}

// totalLess is Rust's f64::total_cmp as a less-than.
func totalLess(a, b float64) bool {
	key := func(v float64) int64 {
		k := int64(math.Float64bits(v))
		return k ^ int64(uint64(k>>63)>>1)
	}
	return key(a) < key(b)
}

// paints are the paints of a `fill`/`stroke` value. Gradients and images span the node box b.
func (s *Svg) paints(v jsonv.Value, b Rect) []paint {
	var out []paint
	for _, fv := range Fills(v) {
		mode, _ := jsonv.Str(jsonv.Get(fv, "blendMode"))
		blend := BlendMode(mode)
		var value string
		var alpha float64
		switch x := fv.(type) {
		case string:
			var ok bool
			if value, alpha, ok = Color(x); !ok {
				continue
			}
		case *jsonv.Object:
			opacity := numOr(jsonv.Get(x, "opacity"), 1)
			switch fillType(x) {
			case "color":
				c, _ := jsonv.Str(jsonv.Get(x, "color"))
				var ok bool
				if value, alpha, ok = Color(c); !ok {
					continue
				}
			case "gradient":
				id, ok := s.gradient(x, b)
				if !ok {
					continue
				}
				value, alpha = "url(#"+id+")", opacity
			case "image":
				url, _ := jsonv.Str(jsonv.Get(x, "url"))
				// An export inlines (and depth-checks) image files later.
				found, ok := "", false
				if s.Embed {
					found, ok = s.Assets.ImageUnscanned(url, s.Base)
				}
				var href string
				if ok && !strings.HasPrefix(found, "data:") {
					href = s.Embedded.token(found)
				} else {
					href, ok = s.Assets.Image(url, s.Base)
				}
				switch {
				case ok:
					aspect := "none"
					switch m, _ := jsonv.Str(jsonv.Get(x, "mode")); m {
					case "fill":
						aspect = "xMidYMid slice"
					case "fit":
						aspect = "xMidYMid meet"
					}
					id := s.id("p")
					s.defs.WriteString(`<pattern id="` + id + `" patternUnits="userSpaceOnUse" x="` + f(b.X) + `" y="` + f(b.Y) + `" width="` + f(b.W) + `" height="` + f(b.H) +
						`"><image xlink:href="` + Esc(href) + `" width="` + f(b.W) + `" height="` + f(b.H) + `" preserveAspectRatio="` + aspect + `"/></pattern>`)
					value, alpha = "url(#"+id+")", opacity
				case IsPendingURL(url) || s.Assets.Loading(url):
					// Still generating or downloading: nothing is drawn.
					continue
				default:
					// Unavailable image: an 8 px white/black checkerboard at 10 % (`shape` adds the grey outline).
					id := s.id("p")
					s.defs.WriteString(`<pattern id="` + id + `" patternUnits="userSpaceOnUse" x="` + f(b.X) + `" y="` + f(b.Y) +
						`" width="16" height="16"><rect width="16" height="16" fill="#fff"/><rect x="8" width="8" height="8" fill="#000"/><rect y="8" width="8" height="8" fill="#000"/></pattern>`)
					value, alpha = "url(#"+id+")", 0.1
				}
			case "mesh_gradient":
				id, ok := s.rasterPattern(b, func(w, h int) *Pixmap { return meshGradient(x, w, h) })
				if !ok {
					continue
				}
				value, alpha = "url(#"+id+")", opacity
			case "shader":
				value, alpha = "url(#"+s.shader(x, b)+")", opacity
			default:
				continue
			}
		default:
			continue
		}
		out = append(out, paint{value, alpha, blend})
	}
	return out
}

// gradient writes a linear, radial or angular gradient in bbox units: rotation (CCW) before the W×H scale.
func (s *Svg) gradient(o *jsonv.Object, b Rect) (string, bool) {
	colors, ok := jsonv.Get(o, "colors").(*jsonv.Array)
	if !ok {
		return "", false
	}
	stops := make([]stop, 0, len(colors.Items))
	for _, c := range colors.Items {
		col := [4]float64{}
		if cs, ok := jsonv.Str(jsonv.Get(c, "color")); ok {
			col, _ = rgba(cs)
		}
		stops = append(stops, stop{clamp(numOr(jsonv.Get(c, "position"), 0), 0, 1), col})
	}
	if len(stops) == 0 {
		return "", false
	}
	sort.SliceStable(stops, func(i, j int) bool { return totalLess(stops[i].pos, stops[j].pos) })
	pt := func(k, a string, d float64) float64 { return numOr(jsonv.Get(jsonv.Get(o, k), a), d) }
	cx, cy := pt("center", "x", 0.5), pt("center", "y", 0.5)
	half := func(v float64) float64 {
		if v == 0 {
			return 1e-6
		}
		return v / 2
	}
	rx, ry := half(pt("size", "width", 1)), half(pt("size", "height", 1))
	phi := -toRad(numOr(jsonv.Get(o, "rotation"), 0))
	cs, sn := math.Cos(phi), math.Sin(phi)
	up := [2]float64{ry * sn, -ry * cs}
	right := [2]float64{rx * cs, rx * sn}
	bx, by, bw, bh := b.X, b.Y, b.W, b.H
	id := s.id("g")
	// A line's box has no height (or width): its end points go in user space instead of a singular transform.
	flat := math.Abs(bw) < 1e-6 || math.Abs(bh) < 1e-6
	// Rounds away float leftovers (cos 90° = 6e-17) and negative zeros.
	n := func(v float64) string { return f(math.Round(v*1e9)/1e9 + 0) }
	switch t, _ := jsonv.Str(jsonv.Get(o, "gradientType")); t {
	case "angular":
		return s.conic(stops, [2]float64{cx, cy}, up, right, b)
	case "radial":
		m := [6]float64{bw * right[0], bh * right[1], -bw * up[0], -bh * up[1], bx + bw*cx, by + bh*cy}
		if flat {
			// The ellipse collapses to a segment; a circle over its half-length stands in.
			r := math.Hypot(m[1], m[3])
			if math.Abs(bw) >= math.Abs(bh) {
				r = math.Hypot(m[0], m[2])
			}
			s.defs.WriteString(`<radialGradient id="` + id + `" cx="` + n(m[4]) + `" cy="` + n(m[5]) + `" r="` + n(r) + `" gradientUnits="userSpaceOnUse">` + stopTags(stops) + `</radialGradient>`)
		} else {
			s.defs.WriteString(`<radialGradient id="` + id + `" cx="0" cy="0" r="1" gradientUnits="userSpaceOnUse" gradientTransform="matrix(` +
				n(m[0]) + " " + n(m[1]) + " " + n(m[2]) + " " + n(m[3]) + " " + n(m[4]) + " " + n(m[5]) + `)">` + stopTags(stops) + `</radialGradient>`)
		}
	default:
		if flat {
			s.defs.WriteString(`<linearGradient id="` + id + `" x1="` + n(bx+bw*(cx-up[0])) + `" y1="` + n(by+bh*(cy-up[1])) + `" x2="` + n(bx+bw*(cx+up[0])) + `" y2="` + n(by+bh*(cy+up[1])) +
				`" gradientUnits="userSpaceOnUse">` + stopTags(stops) + `</linearGradient>`)
		} else {
			s.defs.WriteString(`<linearGradient id="` + id + `" x1="` + n(cx-up[0]) + `" y1="` + n(cy-up[1]) + `" x2="` + n(cx+up[0]) + `" y2="` + n(cy+up[1]) +
				`" gradientUnits="userSpaceOnUse" gradientTransform="matrix(` + n(bw) + " 0 0 " + n(bh) + " " + n(bx) + " " + n(by) + `)">` + stopTags(stops) + `</linearGradient>`)
		}
	}
	return id, true
}

// OutlineKind is the shape a shader fill's `@sdf` measures from.
type OutlineKind uint8

const (
	// OutlineRRect is a rounded rect with Radii.
	OutlineRRect OutlineKind = iota
	// OutlineEllipse is the full ellipse in the node box.
	OutlineEllipse
	// OutlinePath is the path D, whose node box starts at X, Y.
	OutlinePath
)

// Outline is the outline of the shape whose fills are being emitted (shader.rs `Outline`).
type Outline struct {
	Kind    OutlineKind
	Radii   [4]float64
	D       string
	X, Y    float64
	EvenOdd bool
}

// ShaderPainter runs a shader fill's GLSL source over a w×h node box and returns the base64 PNG of the
// result (shader.rs `png`). load returns an image the shader samples, premultiplied, by its url.
type ShaderPainter interface {
	PNG(src string, uniforms *jsonv.Object, w, h float64, outline Outline, load func(url string) (*Pixmap, bool)) (string, error)
}

// shader is a shader fill as an image pattern; when the file is missing, no painter is set or it fails,
// an error hatch: 1-unit red stripes every 5 units at 135°.
func (s *Svg) shader(o *jsonv.Object, b Rect) string {
	x, y, w, h := b.X, b.Y, b.W, b.H
	url, _ := jsonv.Str(jsonv.Get(o, "url"))
	var data string
	var err error
	outline := Outline{}
	if s.outline != nil {
		outline = *s.outline
	}
	s.outline = nil
	if p, ok := s.Assets.Image(url, s.Base); !ok {
		err = errors.New("Shader file not found: \"" + url + "\"")
	} else if src, e := os.ReadFile(p); e != nil {
		err = e
	} else if s.Shader == nil {
		err = errors.New("no shader painter")
	} else {
		load := func(u string) (*Pixmap, bool) {
			p, ok := s.Assets.Image(u, s.Base)
			if !ok {
				return nil, false
			}
			return DecodeImage(p)
		}
		data, err = s.Shader.PNG(string(src), jsonv.Obj(jsonv.Get(o, "uniforms")), w, h, outline, load)
	}
	s.outline = &outline
	id := s.id("p")
	if err == nil {
		s.defs.WriteString(`<pattern id="` + id + `" patternUnits="userSpaceOnUse" x="` + f(x) + `" y="` + f(y) + `" width="` + f(w) + `" height="` + f(h) +
			`"><image width="` + f(w) + `" height="` + f(h) + `" preserveAspectRatio="none" xlink:href="data:image/png;base64,` + data + `"/></pattern>`)
	} else {
		fmt.Fprintf(os.Stderr, "shader fill %s: %v\n", url, err)
		s.defs.WriteString(`<pattern id="` + id + `" patternUnits="userSpaceOnUse" width="1" height="1" patternTransform="translate(` + f(x) + " " + f(y) +
			`) rotate(135) scale(5)"><rect x="0.4" width="0.2" height="1" fill="#ff0000"/></pattern>`)
	}
	return id
}
