package render

import (
	"math"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Hit surface of a painted shape (hit.rs): a click lands on what the shape paints, not on its box.

const (
	// minReach is the pick slack around a thin stroke, in document units.
	minReach = 2.0
	// curveSteps is how many segments a curve is flattened into.
	curveSteps = 16
)

type align uint8

const (
	alignInner align = iota
	alignCenter
	alignOuter
)

// lineCap is how an open subpath's ends paint (SVG's default is butt).
type lineCap uint8

const (
	capButt lineCap = iota
	capRound
	capSquare
)

type poly struct {
	pts    [][2]float64
	closed bool
}

type hitStroke struct {
	// widths per side [top, right, bottom, left] for a rectangle; other shapes paint the top width all round.
	widths [4]float64
	align  align
	cap    lineCap
}

// Shape is the outline of a node in its box coordinates: the filled area and/or the stroke band that paints.
type Shape struct {
	polys   []poly
	evenodd bool
	filled  bool
	stroke  *hitStroke
	// sides is the box of a rectangle whose sides have different stroke widths: its band is tested side by side.
	sides *svg.Rect
}

// paints reports whether a paint list paints anything: an enabled paint that is not fully transparent.
func paints(v jsonv.Value) bool {
	opaque := func(c jsonv.Value) bool {
		s, ok := jsonv.Str(c)
		if !ok {
			return false
		}
		_, a, ok := svg.Color(s)
		return ok && a > 0
	}
	for _, f := range svg.Fills(v) {
		switch o := f.(type) {
		case string:
			if opaque(o) {
				return true
			}
		case *jsonv.Object:
			op := 1.0
			if v, ok := o.Get("opacity"); ok {
				if n, ok := resolve.Num(v); ok {
					op = n
				}
			}
			ty := "color"
			if t, ok := jsonv.Str(jsonv.Get(o, "type")); ok {
				ty = t
			}
			if op > 0 && (ty != "color" || opaque(jsonv.Get(o, "color"))) {
				return true
			}
		}
	}
	return false
}

// ShapeOf is hit.rs `Shape::of`: nil when the box is the surface (a rectangle with a fill, or a shape that
// paints nothing, which stays selectable by its box).
func ShapeOf(n *resolve.RNode) *Shape {
	switch n.Type {
	case "rectangle", "ellipse", "polygon", "path", "line":
	default:
		return nil
	}
	rect := n.Type == "rectangle"
	fill, _ := n.P.Get("fill")
	filled := n.Type != "line" && paints(fill)
	// Like the renderer: a rectangle strokes each side at its own width, other shapes use the top width.
	var widths [4]float64
	if sw, ok := n.P.Get("strokeWidth"); ok {
		if s, isObj := sw.(*jsonv.Object); isObj {
			for i, k := range []string{"top", "right", "bottom", "left"} {
				v, _ := resolve.Num(jsonv.Get(s, k))
				widths[i] = fmax(v, 0)
			}
		} else {
			v, _ := resolve.Num(sw)
			v = fmax(v, 0)
			widths = [4]float64{v, v, v, v}
		}
	}
	if !rect {
		widths = [4]float64{widths[0], widths[0], widths[0], widths[0]}
	}
	stroke, _ := n.P.Get("stroke")
	stroked := (widths[0] > 0 || widths[1] > 0 || widths[2] > 0 || widths[3] > 0) && paints(stroke)
	if !stroked && (!filled || rect) || filled && rect {
		return nil
	}
	geo := svg.Geometry(n)
	polys := flatten(geo.D)
	if len(polys) == 0 {
		return nil
	}
	al := alignCenter
	if geo.Closed {
		switch s, _ := n.S("strokeAlignment"); s {
		case "inner", "inside":
			al = alignInner
		case "outer", "outside":
			al = alignOuter
		}
	}
	cp := capButt
	switch s, _ := n.S("strokeLinecap"); s {
	case "round":
		cp = capRound
	case "square":
		cp = capSquare
	}
	sh := &Shape{polys: polys, evenodd: geo.EvenOdd, filled: filled}
	if rect && (widths[1] != widths[0] || widths[2] != widths[0] || widths[3] != widths[0]) {
		sh.sides = &svg.Rect{X: n.X, Y: n.Y, W: n.W, H: n.H}
	}
	if stroked {
		sh.stroke = &hitStroke{widths: widths, align: al, cap: cp}
	}
	return sh
}

// Hits reports whether the shape paints the point.
func (s *Shape) Hits(x, y float64) bool {
	inside := s.contains(x, y)
	if s.filled && inside {
		return true
	}
	st := s.stroke
	if st == nil {
		return false
	}
	if s.sides != nil {
		return st.onSides(*s.sides, x, y)
	}
	width := st.widths[0]
	reach := st.reach(width)
	// Past an open end a butt cap paints nothing (only the pick slack), a square one half the width.
	over, hasOver := 0.0, true
	switch st.cap {
	case capRound:
		hasOver = false
	case capButt:
		over = reach - width/2
	case capSquare:
		over = reach
	}
	if !s.near(x, y, reach, over, hasOver) {
		return false
	}
	switch st.align {
	case alignInner:
		return inside
	case alignOuter:
		return !inside
	}
	return true
}

// contains reports whether the fill rule paints the point (every subpath is closed implicitly).
func (s *Shape) contains(x, y float64) bool {
	winding, crossings := 0, 0
	for _, p := range s.polys {
		for i, a := range p.pts {
			b := p.pts[(i+1)%len(p.pts)]
			if (a[1] <= y) != (b[1] <= y) && a[0]+float64((y-a[1])/(b[1]-a[1])*(b[0]-a[0])) > x {
				crossings++
				if b[1] > a[1] {
					winding++
				} else {
					winding--
				}
			}
		}
	}
	if s.evenodd {
		return crossings%2 == 1
	}
	return winding != 0
}

// near reports whether the point is within reach of a stroked segment. Joins are round; past an open
// subpath's end the point may lie over beyond it (flat cap), or reach from the end point without over.
func (s *Shape) near(x, y, reach, over float64, hasOver bool) bool {
	for _, p := range s.polys {
		segments := len(p.pts) - 1
		if p.closed {
			segments = len(p.pts)
		}
		for i := range segments {
			a, b := p.pts[i], p.pts[(i+1)%len(p.pts)]
			dx, dy := b[0]-a[0], b[1]-a[1]
			l := float64(dx*dx) + float64(dy*dy)
			t := 0.0
			if l > 0 {
				t = (float64((x-a[0])*dx) + float64((y-a[1])*dy)) / l
			}
			at := func(t float64) float64 {
				return math.Hypot(x-(a[0]+float64(t*dx)), y-(a[1]+float64(t*dy)))
			}
			pastEnd := !p.closed && (i == 0 && t < 0 || i+1 == segments && t > 1)
			var hit bool
			switch {
			case t >= 0 && t <= 1:
				hit = at(t) <= reach
			case hasOver && pastEnd:
				d := t - 1
				if t < 0 {
					d = -t
				}
				hit = at(t) <= reach && float64(d*math.Sqrt(l)) <= over
			default:
				hit = at(min(max(t, 0), 1)) <= reach
			}
			if hit {
				return true
			}
		}
	}
	return false
}

// reach is how far from the outline a stroke of width is hit: the painted band, at least the pick slack.
func (st *hitStroke) reach(width float64) float64 {
	r := width
	if st.align == alignCenter {
		r = width / 2
	}
	return fmax(r, minReach)
}

// onSides is a rectangle's band when its sides differ: each side reaches in and/or out by its own width,
// and a side of width 0 paints nothing.
func (st *hitStroke) onSides(b svg.Rect, x, y float64) bool {
	var in, out [4]float64 // [top, right, bottom, left]
	for i, w := range st.widths {
		r := 0.0
		if w > 0 {
			r = st.reach(w)
		}
		switch st.align {
		case alignInner:
			in[i] = r
		case alignCenter:
			in[i], out[i] = r, r
		case alignOuter:
			out[i] = r
		}
	}
	outside := x < b.X-out[3] || x > b.X+b.W+out[1] || y < b.Y-out[0] || y > b.Y+b.H+out[2]
	hole := x > b.X+in[3] && x < b.X+b.W-in[1] && y > b.Y+in[0] && y < b.Y+b.H-in[2]
	return !outside && !hole
}

// flatten turns an SVG path into polylines (arcs arrive as curves from the simplifying parser).
func flatten(d string) []poly {
	var polys []poly
	var cur, start [2]float64
	// A segment after a close continues from the subpath's start.
	push := func(p [2]float64) {
		if n := len(polys); n > 0 && !polys[n-1].closed {
			polys[n-1].pts = append(polys[n-1].pts, p)
			return
		}
		polys = append(polys, poly{pts: [][2]float64{start, p}})
	}
	for _, seg := range usvg.SimplifyPath(d) {
		switch seg.Kind {
		case 'M':
			polys = append(polys, poly{pts: [][2]float64{{seg.X, seg.Y}}})
			cur, start = [2]float64{seg.X, seg.Y}, [2]float64{seg.X, seg.Y}
		case 'L':
			push([2]float64{seg.X, seg.Y})
			cur = [2]float64{seg.X, seg.Y}
		case 'C':
			for i := 1; i <= curveSteps; i++ {
				t, s := float64(i)/curveSteps, 1-float64(i)/curveSteps
				at := func(p0, p1, p2, p3 float64) float64 {
					return float64(float64(s*s*s)*p0) + float64(float64(3*s*s*t)*p1) + float64(float64(3*s*t*t)*p2) + float64(float64(t*t*t)*p3)
				}
				push([2]float64{at(cur[0], seg.X1, seg.X2, seg.X), at(cur[1], seg.Y1, seg.Y2, seg.Y)})
			}
			cur = [2]float64{seg.X, seg.Y}
		case 'Q':
			for i := 1; i <= curveSteps; i++ {
				t, s := float64(i)/curveSteps, 1-float64(i)/curveSteps
				at := func(p0, p1, p2 float64) float64 {
					return float64(float64(s*s)*p0) + float64(float64(2*s*t)*p1) + float64(float64(t*t)*p2)
				}
				push([2]float64{at(cur[0], seg.X1, seg.X), at(cur[1], seg.Y1, seg.Y)})
			}
			cur = [2]float64{seg.X, seg.Y}
		case 'Z':
			if n := len(polys); n > 0 {
				polys[n-1].closed = true
			}
			cur = start
		}
	}
	out := polys[:0]
	for _, p := range polys {
		if len(p.pts) > 1 {
			out = append(out, p)
		}
	}
	return out
}

// HitChain is render.rs `hit_chain`: the topmost-first chain of nodes under a document point, [top-level,
// child, ..., deepest]. The deepest node is the last-painted one whose box holds the point; a child can be
// hit outside its parent's box unless an ancestor clips it. A group's union box is not a hit surface, so
// only its children can be hit: a point inside its box but on none of them falls through to the layers
// below. The same goes for the empty part of an ellipse's, path's or line's box (see Shape).
func HitChain(r *Renderer, x, y float64) []string {
	type step struct {
		i              int
		open, scripted bool
	}
	var pathBuf [16]step
	path := pathBuf[:0]
	inside := func(f *Flat) bool {
		px, py := x, y
		if f.M != nil {
			px, py = Unapply(*f.M, x, y)
		}
		// A shape's own test also takes the stroke it paints outside its box.
		if f.shape != nil {
			return f.shape.Hits(px, py)
		}
		return px >= f.X && py >= f.Y && px <= f.X+f.W && py <= f.Y+f.H
	}
	for li := len(r.Order) - 1; li >= 0; li-- {
		layer, ok := r.Layers[r.Order[li]]
		if !ok {
			continue
		}
		// Per node, in paint order: is it reachable (no clipping ancestor misses the point), and is it under
		// a script node (whose generated children select the script node).
		path = path[:0]
		hit := -1
		for i := range layer.Flat {
			f := &layer.Flat[i]
			if len(path) > f.Depth {
				path = path[:f.Depth]
			}
			open, scripted := true, false
			if n := len(path); n > 0 {
				p := path[n-1]
				pf := &layer.Flat[p.i]
				open = p.open && (!pf.Clip || inside(pf))
				scripted = p.scripted || pf.Type == "script"
			}
			path = append(path, step{i, open, scripted})
			if open && !scripted && f.Type != "group" && (f.Depth == 0 || f.W > 0 && f.H > 0) && inside(f) {
				hit = i
			}
		}
		if hit < 0 {
			continue
		}
		i := hit
		chain := []string{layer.Flat[i].ID}
		for layer.Flat[i].HasParent {
			p := layer.Flat[i].Parent
			for i--; layer.Flat[i].ID != p; i-- {
			}
			chain = append(chain, p)
		}
		for a, b := 0, len(chain)-1; a < b; a, b = a+1, b-1 {
			chain[a], chain[b] = chain[b], chain[a]
		}
		return chain
	}
	return nil
}

// Unapply maps a canvas point to box coordinates (the inverse of m).
func Unapply(m [6]float64, x, y float64) (float64, float64) {
	det := float64(m[0]*m[3]) - float64(m[1]*m[2])
	x, y = x-m[4], y-m[5]
	return (float64(m[3]*x) - float64(m[2]*y)) / det, (float64(m[0]*y) - float64(m[1]*x)) / det
}
