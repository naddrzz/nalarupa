package svg

// Outlines, transforms and bounds of laid-out nodes (svg.rs `geometry`, `radii`, `visual_bounds`, ...).

import (
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/libm"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// Rect is (x, y, w, h).
type Rect struct{ X, Y, W, H float64 }

// pi is a variable so expressions with it round in float64 step by step, as Rust's do.
var pi = math.Pi

// toRad is Rust's `to_radians`.
func toRad(d float64) float64 { return d * (pi / 180) }

// f writes a float as Rust's `{}` does: the shortest digits that round-trip, never an exponent.
func f(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// f2 is Rust's `{:.2}`.
func f2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// fmax and fmin are Rust's f64::max and min: a NaN operand gives the other one.
func fmax(a, b float64) float64 {
	if math.IsNaN(a) || b > a {
		return b
	}
	return a
}

func fmin(a, b float64) float64 {
	if math.IsNaN(a) || b < a {
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

func itoa(i int) string { return strconv.Itoa(i) }

// num is resolve.rs `num` on an optional value.
func num(v jsonv.Value) (float64, bool) { return resolve.Num(v) }

func numOr(v jsonv.Value, d float64) float64 {
	if x, ok := num(v); ok {
		return x
	}
	return d
}

// fOr is `n.f(k).unwrap_or(d)`.
func fOr(n *resolve.RNode, k string, d float64) float64 {
	if v, ok := n.F(k); ok {
		return v
	}
	return d
}

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

func box(n *resolve.RNode) Rect { return Rect{n.X, n.Y, n.W, n.H} }

func inflate(r Rect, m float64) Rect { return Rect{r.X - m, r.Y - m, r.W + 2*m, r.H + 2*m} }

func union(a, b Rect) Rect {
	x0, y0 := fmin(a.X, b.X), fmin(a.Y, b.Y)
	return Rect{x0, y0, fmax(a.X+a.W, b.X+b.W) - x0, fmax(a.Y+a.H, b.Y+b.H) - y0}
}

// Geo is the fill outline of a shape node in absolute coordinates.
type Geo struct {
	D       string
	EvenOdd bool
	Closed  bool
}

func (g Geo) rule(attr string) string {
	if g.EvenOdd {
		return " " + attr + `="evenodd"`
	}
	return ""
}

// Geometry is the fill outline of a shape node.
func Geometry(n *resolve.RNode) Geo {
	evenodd := sIs(n, "fillRule", "evenodd")
	switch n.Type {
	case "ellipse":
		// Holes and arcs are drawn as nested contours, so the ellipse always needs evenodd.
		return Geo{ellipseD(n), true, true}
	case "polygon":
		return Geo{polygonD(n), evenodd, true}
	case "path":
		d, closed := pathGeometry(n)
		return Geo{d, evenodd, closed}
	case "line":
		return Geo{"M" + f(n.X) + " " + f(n.Y) + "L" + f(n.X+n.W) + " " + f(n.Y+n.H), false, false}
	}
	r := Radii(n)
	return Geo{rrectD(n.X, n.Y, n.W, n.H, [4][2]float64{{r[0], r[0]}, {r[1], r[1]}, {r[2], r[2]}, {r[3], r[3]}}), false, true}
}

func ellipseD(n *resolve.RNode) string {
	cx, cy, rx, ry := n.X+n.W/2, n.Y+n.H/2, n.W/2, n.H/2
	inner := clamp(fOr(n, "innerRadius", 0), 0, 1)
	start := fOr(n, "startAngle", 0)
	sweep := clamp(fOr(n, "sweepAngle", 360), -360, 360)
	pt := func(a, k float64) (float64, float64) {
		sin, cos := libm.SinCos(toRad(a))
		return cx + float64(rx*k*cos), cy - float64(ry*k*sin)
	}
	full := func(k float64) string {
		return "M" + f(cx+rx*k) + " " + f(cy) + "A" + f(rx*k) + " " + f(ry*k) + " 0 1 0 " + f(cx-rx*k) + " " + f(cy) +
			"A" + f(rx*k) + " " + f(ry*k) + " 0 1 0 " + f(cx+rx*k) + " " + f(cy) + "Z"
	}
	if math.Abs(sweep) >= 359.9999 {
		if inner > 0 {
			return full(1) + full(inner)
		}
		return full(1)
	}
	end := start + sweep
	large := "0"
	if math.Abs(sweep) > 180 {
		large = "1"
	}
	sfOut, sfIn := "1", "0"
	if sweep > 0 {
		sfOut, sfIn = "0", "1"
	}
	p0x, p0y := pt(start, 1)
	p1x, p1y := pt(end, 1)
	d := "M" + f(p0x) + " " + f(p0y) + "A" + f(rx) + " " + f(ry) + " 0 " + large + " " + sfOut + " " + f(p1x) + " " + f(p1y)
	if inner > 0 {
		q1x, q1y := pt(end, inner)
		q0x, q0y := pt(start, inner)
		d += "L" + f(q1x) + " " + f(q1y) + "A" + f(rx*inner) + " " + f(ry*inner) + " 0 " + large + " " + sfIn + " " + f(q0x) + " " + f(q0y) + "Z"
	} else {
		d += "L" + f(cx) + " " + f(cy) + "Z"
	}
	return d
}

// polygonD is a regular polygon on the inscribed ellipse (first vertex top-center, clockwise), its corners
// rounded by `cornerRadius`; no `polygonCount` (or < 3) draws nothing, more than 1000 is capped.
func polygonD(n *resolve.RNode) string {
	const maxPolygonCount = 1000.0
	c, ok := n.F("polygonCount")
	if !ok || !(c >= 3) {
		return ""
	}
	count := int(fmin(c, maxPolygonCount))
	cx, cy, rx, ry := n.X+n.W/2, n.Y+n.H/2, n.W/2, n.H/2
	pts := make([][2]float64, count)
	for i := range pts {
		sin, cos := libm.SinCos(toRad(-90 + 360*float64(i)/float64(count)))
		pts[i] = [2]float64{cx + float64(rx*cos), cy + float64(ry*sin)}
	}
	r := 0.0
	cr, _ := n.P.Get("cornerRadius")
	if a, isArr := cr.(*jsonv.Array); isArr {
		if len(a.Items) > 0 {
			r = numOr(a.Items[0], 0)
		}
	} else {
		r = numOr(cr, 0)
	}
	r = fmax(r, 0)
	dist := func(a, b [2]float64) float64 { return math.Hypot(a[0]-b[0], a[1]-b[1]) }
	minEdge := math.MaxFloat64
	for i := range count {
		minEdge = fmin(minEdge, dist(pts[i], pts[(i+1)%count]))
	}
	fr := fmin(r, minEdge/2)
	var d strings.Builder
	for i := range count {
		p0, p1, p2 := pts[(i+count-1)%count], pts[i], pts[(i+1)%count]
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		l1, l2 := dist(p0, p1), dist(p2, p1)
		if fr <= 0 || l1 <= 0 || l2 <= 0 {
			d.WriteString(cmd + f(p1[0]) + " " + f(p1[1]))
			continue
		}
		v1 := [2]float64{(p0[0] - p1[0]) / l1, (p0[1] - p1[1]) / l1}
		v2 := [2]float64{(p2[0] - p1[0]) / l2, (p2[1] - p1[1]) / l2}
		theta := math.Acos(clamp(v1[0]*v2[0]+v1[1]*v2[1], -1, 1))
		t := fr / math.Tan(theta/2)
		ax, ay := p1[0]+v1[0]*t, p1[1]+v1[1]*t
		bx, by := p1[0]+v2[0]*t, p1[1]+v2[1]*t
		d.WriteString(cmd + f(ax) + " " + f(ay) + "A" + f(fr) + " " + f(fr) + " 0 0 1 " + f(bx) + " " + f(by))
	}
	return d.String() + "Z"
}

// pathGeometry maps the path from its viewBox (default: the tight curve bounds) onto the node box,
// numerically so stroke widths do not scale; closed reports whether every subpath is closed.
func pathGeometry(n *resolve.RNode) (string, bool) {
	d, _ := n.S("geometry")
	var vb [4]float64
	got := false
	if a, ok := jsonv.Get(n.P, "viewBox").(*jsonv.Array); ok && len(a.Items) == 4 {
		vb = [4]float64{numOr(a.Items[0], 0), numOr(a.Items[1], 0), numOr(a.Items[2], 1), numOr(a.Items[3], 1)}
		got = true
	}
	if !got {
		if b, ok := PathBBox(d); ok {
			vb = [4]float64{b.X, b.Y, b.W, b.H}
		}
	}
	vx, vy, vw, vh := vb[0], vb[1], vb[2], vb[3]
	sx, sy := 1.0, 1.0
	if vw > 0 {
		sx = n.W / vw
	}
	if vh > 0 {
		sy = n.H / vh
	}
	px := func(x float64) string { return f(n.X + (x-vx)*sx) }
	py := func(y float64) string { return f(n.Y + (y-vy)*sy) }
	var out strings.Builder
	closed, open := true, false
	segs, _ := parsePath(d)
	for _, s := range segs {
		switch s.kind {
		case segMove:
			closed = closed && !open
			open = false
			out.WriteString("M" + px(s.x) + " " + py(s.y))
		case segLine:
			open = true
			out.WriteString("L" + px(s.x) + " " + py(s.y))
		case segCurve:
			open = true
			out.WriteString("C" + px(s.x1) + " " + py(s.y1) + " " + px(s.x2) + " " + py(s.y2) + " " + px(s.x) + " " + py(s.y))
		case segQuad:
			open = true
			out.WriteString("Q" + px(s.x1) + " " + py(s.y1) + " " + px(s.x) + " " + py(s.y))
		case segClose:
			open = false
			out.WriteString("Z")
		}
	}
	return out.String(), closed && !open
}

// PathBBox is the tight bounds of an SVG path (curve extrema, not control points); false when it is
// empty or does not parse.
func PathBBox(d string) (Rect, bool) {
	b := [4]float64{math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64}
	add := func(x, y float64) { b = [4]float64{fmin(b[0], x), fmin(b[1], y), fmax(b[2], x), fmax(b[3], y)} }
	var cur, start [2]float64
	cubicT := func(p0, p1, p2, p3 float64) []float64 {
		a, bb, c := -p0+float64(3*p1)-float64(3*p2)+p3, 2*(p0-float64(2*p1)+p2), p1-p0
		var roots []float64
		if math.Abs(a) < 1e-12 {
			if math.Abs(bb) >= 1e-12 {
				roots = []float64{-c / bb}
			}
		} else if disc := float64(bb*bb) - float64(4*a*c); disc >= 0 {
			roots = []float64{(-bb + math.Sqrt(disc)) / (2 * a), (-bb - math.Sqrt(disc)) / (2 * a)}
		}
		out := roots[:0]
		for _, t := range roots {
			if t > 0 && t < 1 {
				out = append(out, t)
			}
		}
		return out
	}
	segs, failed := parsePath(d)
	if failed {
		return Rect{}, false
	}
	for _, s := range segs {
		switch s.kind {
		case segMove:
			add(s.x, s.y)
			cur, start = [2]float64{s.x, s.y}, [2]float64{s.x, s.y}
		case segLine:
			add(s.x, s.y)
			cur = [2]float64{s.x, s.y}
		case segCurve:
			at := func(t, p0, p1, p2, p3 float64) float64 {
				u := 1 - t
				return float64(u*u*u*p0) + float64(3*u*u*t*p1) + float64(3*u*t*t*p2) + float64(t*t*t*p3)
			}
			ts := append(cubicT(cur[0], s.x1, s.x2, s.x), cubicT(cur[1], s.y1, s.y2, s.y)...)
			for _, t := range ts {
				add(at(t, cur[0], s.x1, s.x2, s.x), at(t, cur[1], s.y1, s.y2, s.y))
			}
			add(s.x, s.y)
			cur = [2]float64{s.x, s.y}
		case segQuad:
			at := func(t, p0, p1, p2 float64) float64 {
				return float64((1-t)*(1-t)*p0) + float64(2*(1-t)*t*p1) + float64(t*t*p2)
			}
			for _, q := range [][3]float64{{cur[0], s.x1, s.x}, {cur[1], s.y1, s.y}} {
				den := q[0] - float64(2*q[1]) + q[2]
				t := -1.0
				if math.Abs(den) > 1e-12 {
					t = (q[0] - q[1]) / den
				}
				if t > 0 && t < 1 {
					add(at(t, cur[0], s.x1, s.x), at(t, cur[1], s.y1, s.y))
				}
			}
			add(s.x, s.y)
			cur = [2]float64{s.x, s.y}
		case segClose:
			cur = start
		}
	}
	if !(b[2] >= b[0]) {
		return Rect{}, false
	}
	return Rect{b[0], b[1], b[2] - b[0], b[3] - b[1]}, true
}

// Radii are the corner radii [tl, tr, br, bl], scaled down together when adjacent radii overflow a side.
func Radii(n *resolve.RNode) [4]float64 {
	var r [4]float64
	switch v, ok := n.P.Get("cornerRadius"); a := v.(type) {
	case *jsonv.Array:
		if len(a.Items) == 4 {
			for i := range r {
				r[i] = numOr(a.Items[i], 0)
			}
		} else {
			x := numOr(v, 0)
			r = [4]float64{x, x, x, x}
		}
	default:
		if ok {
			x := numOr(v, 0)
			r = [4]float64{x, x, x, x}
		}
	}
	for i := range r {
		r[i] = fmax(r[i], 0)
	}
	return scaleRadii(r, n.W, n.H)
}

// scaleRadii scales radii down together when adjacent ones overflow a w×h box's side (CSS rule).
func scaleRadii(r [4]float64, w, h float64) [4]float64 {
	k := 1.0
	for _, q := range []float64{w / (r[0] + r[1]), h / (r[1] + r[2]), w / (r[2] + r[3]), h / (r[3] + r[0])} {
		if !math.IsInf(q, 0) && !math.IsNaN(q) {
			k = fmin(k, q)
		}
	}
	for i := range r {
		r[i] *= k
	}
	return r
}

// rrectD is a rounded rect with per-corner elliptical radii [tl, tr, br, bl], clockwise.
func rrectD(x, y, w, h float64, r [4][2]float64) string {
	tl, tr, br, bl := r[0], r[1], r[2], r[3]
	square := true
	for _, c := range r {
		if !(c[0] <= 0 || c[1] <= 0) {
			square = false
		}
	}
	if square {
		return "M" + f(x) + " " + f(y) + "H" + f(x+w) + "V" + f(y+h) + "H" + f(x) + "Z"
	}
	return "M" + f(x+tl[0]) + " " + f(y) + "H" + f(x+w-tr[0]) + "A" + f(tr[0]) + " " + f(tr[1]) + " 0 0 1 " + f(x+w) + " " + f(y+tr[1]) +
		"V" + f(y+h-br[1]) + "A" + f(br[0]) + " " + f(br[1]) + " 0 0 1 " + f(x+w-br[0]) + " " + f(y+h) +
		"H" + f(x+bl[0]) + "A" + f(bl[0]) + " " + f(bl[1]) + " 0 0 1 " + f(x) + " " + f(y+h-bl[1]) +
		"V" + f(y+tl[1]) + "A" + f(tl[0]) + " " + f(tl[1]) + " 0 0 1 " + f(x+tl[0]) + " " + f(y) + "Z"
}

// rectStrokeD is a rect stroke (per-side widths [t, r, b, l], already ×k): the band between the box grown
// and shrunk by half widths. For inner/center strokes a radius below half the adjacent widths goes square.
func rectStrokeD(n *resolve.RNode, w [4]float64, trim, roundJoin bool) string {
	t, r, b, l := w[0]/2, w[1]/2, w[2]/2, w[3]/2
	adj := [4][2]float64{{l, t}, {r, t}, {r, b}, {l, b}}
	rad := Radii(n)
	for i := range rad {
		if trim && rad[i] < fmin(adj[i][0], adj[i][1]) {
			rad[i] = 0
		}
	}
	var outer, inner [4][2]float64
	for i := range rad {
		h, v := adj[i][0], adj[i][1]
		switch {
		case rad[i] > 0:
			o := rad[i] + fmin(h, v)
			outer[i] = [2]float64{o, o}
			inner[i] = [2]float64{fmax(rad[i]-h, 0), fmax(rad[i]-v, 0)}
		case roundJoin:
			outer[i] = [2]float64{h, v}
		}
	}
	d := rrectD(n.X-l, n.Y-t, n.W+l+r, n.H+t+b, outer)
	if iw, ih := n.W-l-r, n.H-t-b; iw > 0 && ih > 0 {
		d += rrectD(n.X+l, n.Y+t, iw, ih, inner)
	}
	return d
}

// IsTurned reports whether the node has its own rotation or flip.
func IsTurned(n *resolve.RNode) bool {
	r, ok := n.F("rotation")
	return ok && math.Abs(r) > 1e-6 || n.B("flipX") || n.B("flipY")
}

func flips(n *resolve.RNode) (float64, float64) {
	sx, sy := 1.0, 1.0
	if n.B("flipX") {
		sx = -1
	}
	if n.B("flipY") {
		sy = -1
	}
	return sx, sy
}

// OwnMatrix is the node's own rotation (CCW) and flips about its origin, the matrix form of `transform`.
func OwnMatrix(n *resolve.RNode) [6]float64 {
	t := -toRad(fOr(n, "rotation", 0))
	sx, sy := flips(n)
	sin, cos := libm.SinCos(t)
	a, b, c, d := cos*sx, sin*sx, -sin*sy, cos*sy
	px, py := n.X-n.BX, n.Y-n.BY
	return [6]float64{a, b, c, d, px - float64(a*px) - float64(c*py), py - float64(b*px) - float64(d*py)}
}

// transform is the rotation (CCW) and flips about the node's origin as an SVG transform list.
func transform(n *resolve.RNode) string {
	px, py := n.X-n.BX, n.Y-n.BY
	tf := ""
	if r, ok := n.F("rotation"); ok && math.Abs(r) > 1e-6 {
		tf += "rotate(" + f(-r) + " " + f(px) + " " + f(py) + ")"
	}
	if sx, sy := flips(n); sx < 0 || sy < 0 {
		tf += " matrix(" + f(sx) + " 0 0 " + f(sy) + " " + f(px*(1-sx)) + " " + f(py*(1-sy)) + ")"
	}
	return strings.TrimLeft(tf, " ")
}

// valignOff is the offset of a fixed-size text's lines from the top of its box.
func valignOff(n *resolve.RNode, h, total float64) float64 {
	if !sIs(n, "textGrowth", "fixed-width", "fixed-width-height") {
		return 0
	}
	switch s, _ := n.S("textAlignVertical"); s {
	case "middle":
		return (h - total) / 2
	case "bottom":
		return h - total
	}
	return 0
}

// ownBox is the node's box; a text's also covers the lines that overflow its fixed box.
func ownBox(n *resolve.RNode) Rect {
	own := box(n)
	total := float64(len(n.Lines)) * n.LineH
	if n.Type != "text" || total <= n.H {
		return own
	}
	return union(own, Rect{n.X, n.Y + valignOff(n, n.H, total), n.W, total})
}

// extent is the node's box and everything it draws outside it, in the node's own space.
func extent(n *resolve.RNode) Rect {
	own := ownBox(n)
	if n.Type == "frame" && n.B("clip") {
		return own
	}
	for _, k := range n.Kids {
		own = union(own, turn(k, extent(k)))
	}
	return own
}

// turn is the axis-aligned box of r after the node's rotation and flips, rounded to 1e-6.
func turn(n *resolve.RNode, r Rect) Rect {
	if !IsTurned(n) {
		return r
	}
	m := OwnMatrix(n)
	x1, y1 := r.X+r.W, r.Y+r.H
	o := [4]float64{math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64}
	for _, p := range [][2]float64{{r.X, r.Y}, {x1, r.Y}, {r.X, y1}, {x1, y1}} {
		x, y := m[0]*p[0]+m[2]*p[1]+m[4], m[1]*p[0]+m[3]*p[1]+m[5]
		o = [4]float64{fmin(o[0], x), fmin(o[1], y), fmax(o[2], x), fmax(o[3], y)}
	}
	for i := range o {
		o[i] = math.Round(o[i]*1e6) / 1e6
	}
	return Rect{o[0], o[1], o[2] - o[0], o[3] - o[1]}
}

// VisualBounds is the area an export covers: the box, the stroke's outer part, the children of groups and
// unclipped frames, drop shadows and layer blur, under the node's own rotation and flips.
func VisualBounds(n *resolve.RNode) Rect {
	w := 0.0
	if n.P.Has("stroke") {
		w = maxStroke(n)
	}
	out := w / 2
	switch s, _ := n.S("strokeAlignment"); s {
	case "inner":
		out = 0
	case "outer":
		out = w
	}
	var b Rect
	if n.Type == "group" {
		if len(n.Kids) == 0 {
			b = Rect{n.X, n.Y, 0, 0}
		} else {
			b = VisualBounds(n.Kids[0])
			for _, k := range n.Kids[1:] {
				b = union(b, VisualBounds(k))
			}
		}
	} else {
		b = inflate(ownBox(n), out)
	}
	if n.Type == "frame" && !n.B("clip") {
		for _, k := range n.Kids {
			b = union(b, VisualBounds(k))
		}
	}
	fx := EffectsOf(n)
	base := b
	for _, s := range fx.shadows {
		if !s.inner {
			b = union(b, inflate(Rect{base.X + s.dx, base.Y + s.dy, base.W, base.H}, 3*s.sigma))
		}
	}
	return turn(n, inflate(b, 3*fx.Blur))
}

func maxStroke(n *resolve.RNode) float64 {
	v, ok := n.P.Get("strokeWidth")
	if !ok {
		return 0
	}
	if o, isObj := v.(*jsonv.Object); isObj {
		m := 0.0
		o.Each(func(_ string, x jsonv.Value) {
			if w, ok := num(x); ok {
				m = fmax(m, w)
			}
		})
		return m
	}
	return numOr(v, 0)
}

// localAABB is layout's local_aabb.
func localAABB(n *resolve.RNode) [4]float64 { return layout.LocalAABB(n) }
