// Package editor is the direct manipulation behind the canvas UI (core/src/editor.rs): snap targets, marquee
// hits, drop targets, reparenting that keeps the on-screen position, wrap and unwrap, flex-layout inference
// (Shift+A), align, duplicate, the node clipboard and vector path editing. Its commands join the engine's
// dispatcher (engine.Handle) and run with the engine lock held.
//
// Floating-point products get explicit conversions so they are never fused into a multiply-add.
package editor

import (
	"math"
	"slices"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/libm"
	"github.com/naddrzz/nalarupa/core-go/render"
)

// Rect is (x, y, w, h) in document (world) units.
type Rect = render.Rect

// Affine is [a, b, c, d, e, f] (x' = a·x + c·y + e, y' = b·x + d·y + f).
type Affine = [6]float64

var upright = Affine{1, 0, 0, 1, 0, 0}

// Num is editor.rs `num`: integral values stay integers in the saved document; others keep two decimals.
func Num(v float64) jsonv.Value {
	r := math.Round(float64(v*100)) / 100
	if r == math.Trunc(r) && math.Abs(r) < 1e15 {
		return jsonv.Int(int64(r))
	}
	return jsonv.Float(r)
}

// fmin and fmax are Rust's f64::min and max: a NaN operand gives the other one.
func fmin(a, b float64) float64 {
	if math.IsNaN(a) || b < a {
		return b
	}
	return a
}

func fmax(a, b float64) float64 {
	if math.IsNaN(a) || b > a {
		return b
	}
	return a
}

// pi is a variable so expressions with it round in float64 step by step, as Rust's do.
var pi = math.Pi

// toRadians and toDegrees are f64::to_radians and to_degrees.
func toRadians(d float64) float64 { return d * (pi / 180) }
func toDegrees(r float64) float64 { return r * 57.2957795130823208767981548141051703 }

func overlaps(a, b Rect) bool {
	return a.X <= b.X+b.W && b.X <= a.X+a.W && a.Y <= b.Y+b.H && b.Y <= a.Y+a.H
}

func contains(outer, inner Rect) bool {
	return inner.X >= outer.X && inner.Y >= outer.Y && inner.X+inner.W <= outer.X+outer.W && inner.Y+inner.H <= outer.Y+outer.H
}

// union is the box around boxes; false when there are none.
func union(boxes []Rect) (Rect, bool) {
	if len(boxes) == 0 {
		return Rect{}, false
	}
	a := boxes[0]
	for _, b := range boxes[1:] {
		x0, y0 := fmin(a.X, b.X), fmin(a.Y, b.Y)
		a = Rect{X: x0, Y: y0, W: fmax(a.X+a.W, b.X+b.W) - x0, H: fmax(a.Y+a.H, b.Y+b.H) - y0}
	}
	return a, true
}

// compose is m ∘ t, m given even when it is the identity (Rust multiplies by it, which can turn -0 into 0).
func compose(m, t Affine) Affine { return render.Compose(&m, t) }

func invert(m Affine) (Affine, bool) {
	det := float64(m[0]*m[3]) - float64(m[1]*m[2])
	if !(math.Abs(det) > 1e-12) {
		return Affine{}, false
	}
	a, b, c, d := m[3]/det, -m[1]/det, -m[2]/det, m[0]/det
	return Affine{a, b, c, d, -(float64(a*m[4]) + float64(c*m[5])), -(float64(b*m[4]) + float64(d*m[5]))}, true
}

func apply(m Affine, x, y float64) (float64, float64) {
	return float64(m[0]*x) + float64(m[2]*y) + m[4], float64(m[1]*x) + float64(m[3]*y) + m[5]
}

func matrix(f *render.Flat) Affine {
	if f.M == nil {
		return upright
	}
	return *f.M
}

// kids is the children of id (the top-level nodes for the document).
func kids(d *doc.Doc, id string) []jsonv.Value {
	if id == doc.Root {
		return d.Children()
	}
	return jsonv.Items(jsonv.Get(d.Find(id), "children"))
}

func idOf(n jsonv.Value) string { return jsonv.StrOr(jsonv.Get(n, "id")) }

type flatAt struct {
	i int
	f *render.Flat
}

// view is a read-only view of the document and its laid-out boxes (the engine must be synced).
type view struct {
	doc   *doc.Doc
	flats []*render.Flat
	byID  map[string]flatAt
	// nodes holds the document's nodes by id, built on first use (a lookup in the tree walks it).
	nodes map[string]jsonv.Value
}

func newView(e *engine.Engine) *view {
	v := &view{doc: e.Doc, flats: e.Renderer.Flats(), byID: map[string]flatAt{}}
	for i, f := range v.flats {
		v.byID[f.ID] = flatAt{i, f}
	}
	return v
}

// node is Doc.Find without a walk of the tree per call.
func (v *view) node(id string) jsonv.Value {
	if v.nodes == nil {
		v.nodes = map[string]jsonv.Value{}
		doc.Walk(v.doc.Children(), func(n jsonv.Value) {
			if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
				if _, seen := v.nodes[id]; !seen {
					v.nodes[id] = n
				}
			}
		})
	}
	return v.nodes[id]
}

func (v *view) flat(id string) *render.Flat { return v.byID[id].f }

func (v *view) rect(id string) (Rect, bool) {
	f := v.flat(id)
	if f == nil {
		return Rect{}, false
	}
	return Rect{X: f.X, Y: f.Y, W: f.W, H: f.H}, true
}

// painted is the bounds of what the node paints (its box through its rotation/flip matrix), on the canvas or,
// with frame (a container's matrix), in that container's own coordinates.
func (v *view) painted(id string, frame *Affine) (Rect, bool) {
	c, ok := v.corners(id, frame)
	if !ok {
		return Rect{}, false
	}
	return union(points(c[:]))
}

func points(c [][2]float64) []Rect {
	out := make([]Rect, len(c))
	for i, p := range c {
		out[i] = Rect{X: p[0], Y: p[1]}
	}
	return out
}

// corners of the painted box (top-left, top-right, bottom-left, bottom-right), see painted.
func (v *view) corners(id string, frame *Affine) ([4][2]float64, bool) {
	var out [4][2]float64
	f := v.flat(id)
	if f == nil {
		return out, false
	}
	for i, d := range [4][2]float64{{0, 0}, {f.W, 0}, {0, f.H}, {f.W, f.H}} {
		x, y := f.X+d[0], f.Y+d[1]
		if f.M != nil {
			x, y = apply(*f.M, x, y)
		}
		if frame != nil {
			x, y = render.Unapply(*frame, x, y)
		}
		out[i] = [2]float64{x, y}
	}
	return out, true
}

// touches reports whether the painted box touches r (separating-axis test, so a turned box does not count
// through the corners of its bounds).
func (v *view) touches(id string, r Rect) bool {
	c, ok := v.corners(id, nil)
	if !ok {
		return false
	}
	rc := [4][2]float64{{r.X, r.Y}, {r.X + r.W, r.Y}, {r.X, r.Y + r.H}, {r.X + r.W, r.Y + r.H}}
	b, _ := union(points(c[:]))
	if !overlaps(b, r) {
		return false
	}
	span := func(pts [4][2]float64, a [2]float64) (float64, float64) {
		lo, hi := math.MaxFloat64, -math.MaxFloat64
		for _, p := range pts {
			d := float64(p[0]*a[0]) + float64(p[1]*a[1])
			lo, hi = fmin(lo, d), fmax(hi, d)
		}
		return lo, hi
	}
	e1 := [2]float64{c[1][0] - c[0][0], c[1][1] - c[0][1]}
	e2 := [2]float64{c[2][0] - c[0][0], c[2][1] - c[0][1]}
	for _, a := range [4][2]float64{e1, {-e1[1], e1[0]}, e2, {-e2[1], e2[0]}} {
		if a[0] == 0 && a[1] == 0 {
			continue
		}
		q0, q1 := span(c, a)
		w0, w1 := span(rc, a)
		// Tolerance for rounding in the rotation matrix (an exact touch counts).
		if !(q0 <= w1+1e-9 && w0 <= q1+1e-9) {
			return false
		}
	}
	return true
}

func (v *view) ty(id string) string { return jsonv.StrOr(jsonv.Get(v.node(id), "type")) }

// layout is the main axis of a frame with auto layout (0 = horizontal, 1 = vertical), like layout.rs; false
// without one.
func (v *view) layout(id string) (int, bool) {
	n := v.node(id)
	if n == nil || !jsonv.StrIs(jsonv.Get(n, "type"), "frame") {
		return 0, false
	}
	switch jsonv.StrOr(jsonv.Get(n, "layout")) {
	case "none":
		return 0, false
	case "vertical":
		return 1, true
	}
	return 0, true
}

func (v *view) hasLayout(id string) bool {
	_, ok := v.layout(id)
	return ok
}

// inFlow: placed by its parent's auto layout (x/y are ignored).
func (v *view) inFlow(id string) bool {
	if jsonv.StrIs(jsonv.Get(v.node(id), "layoutPosition"), "absolute") {
		return false
	}
	p, _, ok := v.doc.ParentOf(id)
	return ok && v.hasLayout(p)
}

func (v *view) acceptsChildren(id string) bool {
	if id == doc.Root {
		return true
	}
	t := v.ty(id)
	return !strings.Contains(id, "/") && (t == "frame" || t == "group")
}

// origin is the world position of the node's own origin (what its children's x/y are relative to). Groups
// are the union of their (turned) children, so their origin sits before the box, where layout put it.
func (v *view) origin(id string) (x, y float64, ok bool) {
	if id == doc.Root {
		return 0, 0, true
	}
	if v.node(id) == nil {
		return 0, 0, false
	}
	f := v.flat(id)
	if f == nil {
		return 0, 0, false
	}
	return f.X - f.LocalX, f.Y - f.LocalY, true
}

func (v *view) originOr0(id string) (float64, float64) {
	x, y, _ := v.origin(id)
	return x, y
}

// turn is a rotation and flips to store on a node (see placed).
type turn struct {
	deg    float64
	fx, fy bool
}

// placed is where id goes under parent to paint where it does now: its origin relative to the parent's
// origin (in the parent's own, possibly turned, space) and, when the old and new parents are turned
// differently, the turn that folds the difference in.
func (v *view) placed(id, parent string) (ox, oy float64, t *turn, ok bool) {
	f := v.flat(id)
	if f == nil {
		return 0, 0, nil, false
	}
	pm := upright
	if p := v.flat(parent); p != nil {
		pm = matrix(p)
	}
	toParent, ok := invert(pm)
	if !ok {
		return 0, 0, nil, false
	}
	n := compose(toParent, matrix(f))
	x, y, ok := v.origin(id)
	if !ok {
		return 0, 0, nil, false
	}
	px, py, ok := v.origin(parent)
	if !ok {
		return 0, 0, nil, false
	}
	x, y = apply(n, x, y)
	sx, sy := 1.0, 1.0
	if f.FlipX {
		sx = -1
	}
	if f.FlipY {
		sy = -1
	}
	r := -toRadians(f.Rotation)
	sin, cos := libm.SinCos(r)
	own := [4]float64{cos * sx, sin * sx, -sin * sy, cos * sy}
	same := true
	for i := range 4 {
		if !(math.Abs(n[i]-own[i]) < 1e-9) {
			same = false
		}
	}
	if same {
		return x - px, y - py, nil, true
	}
	// Keep the node's own flips unless the mirroring changed (then flip x), and read the rotation off the first column.
	if math.Signbit(float64(n[0]*n[3])-float64(n[1]*n[2])) != (sx*sy < 0) {
		sx = -sx
	}
	deg := -toDegrees(libm.Atan2(n[1]*sx, n[0]*sx))
	return x - px, y - py, &turn{deg, sx < 0, sy < 0}, true
}

// paintOrder sorts ids by where they paint (unknown ones last).
func (v *view) paintOrder(ids []string) []string {
	out := slices.Clone(ids)
	key := func(id string) int {
		if a, ok := v.byID[id]; ok {
			return a.i
		}
		return math.MaxInt
	}
	slices.SortStableFunc(out, func(a, b string) int { return key(a) - key(b) })
	return out
}

// freezeFill: `fill_container` collapses outside auto layout, so it is frozen at the current size.
func (v *view) freezeFill(id string) *jsonv.Object {
	m := jsonv.NewObject()
	n, f := v.node(id), v.flat(id)
	if n == nil || f == nil {
		return m
	}
	for _, kv := range []struct {
		k string
		v float64
	}{{"width", f.W}, {"height", f.H}} {
		if s, ok := jsonv.Str(jsonv.Get(n, kv.k)); ok && strings.HasPrefix(s, "fill_container") {
			m.Set(kv.k, Num(kv.v))
		}
	}
	return m
}

// setTurn stores the rotation and flips from placed.
func setTurn(props *jsonv.Object, t *turn) {
	if t != nil {
		props.Set("rotation", Num(t.deg))
		props.Set("flipX", t.fx)
		props.Set("flipY", t.fy)
	}
}
