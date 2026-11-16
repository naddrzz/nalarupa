package editor

import (
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
)

// SnapTargets are the snap candidates for dragging ids: the parent frame and the siblings (groups recursed),
// limited to the view when given. The engine must be synced.
func SnapTargets(e *engine.Engine, ids []string, view *Rect) *jsonv.Array {
	out := jsonv.NewArray()
	out.Items = []jsonv.Value{}
	v := newView(e)
	if len(ids) == 0 {
		return out
	}
	parent, _, ok := e.Doc.ParentOf(ids[0])
	if !ok {
		return out
	}
	skip := map[string]bool{}
	for _, id := range ids {
		skip[id] = true
	}
	var found []*render.Flat
	if v.ty(parent) == "frame" {
		if f := v.flat(parent); f != nil {
			found = append(found, f)
		}
	}
	var siblings func(nodes []jsonv.Value)
	siblings = func(nodes []jsonv.Value) {
		for _, n := range nodes {
			id := idOf(n)
			if skip[id] {
				continue
			}
			if jsonv.StrIs(jsonv.Get(n, "type"), "group") {
				siblings(jsonv.Items(jsonv.Get(n, "children")))
			} else if f := v.flat(id); f != nil {
				found = append(found, f)
			}
		}
	}
	siblings(kids(e.Doc, parent))
	for _, f := range found {
		if view == nil || overlaps(Rect{X: f.X, Y: f.Y, W: f.W, H: f.H}, *view) {
			out.Items = append(out.Items, jsonv.Value(engine.Object("id", f.ID, "x", jsonv.Float(f.X), "y", jsonv.Float(f.Y), "w", jsonv.Float(f.W),
				"h", jsonv.Float(f.H), "m", engine.MatrixJSON(f.M))))
		}
	}
	return out
}

// PaintedBounds is the union of what ids paint on the canvas (their boxes through rotation/flip). The engine
// must be synced.
func PaintedBounds(e *engine.Engine, ids []string) (Rect, bool) {
	v := newView(e)
	var boxes []Rect
	for _, id := range ids {
		if b, ok := v.painted(id, nil); ok {
			boxes = append(boxes, b)
		}
	}
	return union(boxes)
}

// FitBounds is what the top-level layers paint, for Fit all: each layer's painted box plus its descendants'
// (an unclipped frame shows children that overflow it; a clipping frame cuts them off). The engine must be
// synced.
func FitBounds(e *engine.Engine) (Rect, bool) {
	v := newView(e)
	var boxes []Rect
	var rec func(n jsonv.Value)
	rec = func(n jsonv.Value) {
		id, ok := jsonv.Str(jsonv.Get(n, "id"))
		if !ok {
			return
		}
		if b, ok := v.painted(id, nil); ok {
			boxes = append(boxes, b)
		}
		if f := v.flat(id); f != nil && f.Clip {
			return
		}
		for _, c := range jsonv.Items(jsonv.Get(n, "children")) {
			rec(c)
		}
	}
	for _, id := range e.Renderer.Order {
		if n := e.Doc.Find(id); n != nil {
			rec(n)
		}
	}
	return union(boxes)
}

// NodesInRect is the marquee hits (touching counts). Containers fully inside the rectangle are picked whole;
// top-level frames are entered (artboards); deep (Ctrl) picks leaves everywhere. The engine must be synced.
func NodesInRect(e *engine.Engine, r Rect, deep bool) []string {
	v := newView(e)
	out := []string{}
	var rec func(nodes []jsonv.Value, top bool)
	rec = func(nodes []jsonv.Value, top bool) {
		for _, n := range nodes {
			id, ok := jsonv.Str(jsonv.Get(n, "id"))
			if !ok {
				continue
			}
			b, ok := v.painted(id, nil)
			if !ok || !v.touches(id, r) {
				continue
			}
			ty := jsonv.StrOr(jsonv.Get(n, "type"))
			container := ty == "frame" || ty == "group"
			children := jsonv.Items(jsonv.Get(n, "children"))
			switch {
			case container && contains(r, b):
				out = append(out, id)
			case container && (deep || (top && ty == "frame" && len(children) > 0)):
				rec(children, false)
			default:
				out = append(out, id)
			}
		}
	}
	rec(e.Doc.Children(), true)
	return out
}

// DropTarget is where a drag of a w×h selection would land with the pointer at (x, y): the topmost, deepest
// frame under the pointer (not one being dragged) that is big enough for it (any skips the size rule), else
// the document. Auto-layout targets also get the insertion `index` (children minus the dragged ones) and the
// indicator `line` [x1, y1, x2, y2] on the canvas. The returned x/y/w/h is the frame's box before its turn
// `m` (null when upright). parent ("" for none) names the target instead. The engine must be synced.
func DropTarget(e *engine.Engine, x, y, w, h float64, ignore []string, any bool, parent string, hasParent bool) *jsonv.Object {
	v := newView(e)
	ign := map[string]bool{}
	for _, id := range ignore {
		if n := e.Doc.Find(id); n != nil {
			doc.Walk([]jsonv.Value{n}, func(k jsonv.Value) { ign[idOf(k)] = true })
		}
	}
	// `ty` walks the tree, so it comes last: the main scan tests `inside` before `frameOK` to stay linear.
	frameOK := func(f *render.Flat) bool {
		return !strings.Contains(f.ID, "/") && !ign[f.ID] && v.ty(f.ID) == "frame"
	}
	// The pointer in the frame's own layout space (undoing its rotation/flip), where its box and its children's boxes live.
	local := func(f *render.Flat) (float64, float64) {
		if f.M == nil {
			return x, y
		}
		return render.Unapply(*f.M, x, y)
	}
	inside := func(f *render.Flat) bool {
		px, py := local(f)
		return px >= f.X && py >= f.Y && px <= f.X+f.W && py <= f.Y+f.H
	}
	var t *render.Flat
	if hasParent {
		t = v.flat(parent)
	} else {
		// Pre-order is paint order: the last frame under the pointer is the topmost, deepest one.
		for i := len(v.flats) - 1; i >= 0; i-- {
			if f := v.flats[i]; inside(f) && frameOK(f) {
				t = f
				break
			}
		}
		for t != nil && !any && t.HasParent && (t.W < w || t.H < h) {
			var up *render.Flat
			if t.HasParent {
				up = v.flat(t.Parent)
			}
			for up != nil && !frameOK(up) {
				if up.HasParent {
					up = v.flat(up.Parent)
				} else {
					up = nil
				}
			}
			t = up
		}
	}
	if t == nil {
		return engine.Object("id", doc.Root, "layout", "none")
	}
	dir, flow := v.layout(t.ID)
	name := "none"
	if flow {
		name = map[int]string{0: "horizontal", 1: "vertical"}[dir]
	}
	out := engine.Object("id", t.ID, "x", jsonv.Float(t.X), "y", jsonv.Float(t.Y), "w", jsonv.Float(t.W), "h", jsonv.Float(t.H),
		"m", engine.MatrixJSON(t.M), "layout", name)
	if !flow {
		return out
	}
	lo := func(f *render.Flat) float64 {
		if dir == 0 {
			return f.X
		}
		return f.Y
	}
	hi := func(f *render.Flat) float64 {
		if dir == 0 {
			return f.X + f.W
		}
		return f.Y + f.H
	}
	px, py := local(t)
	p := px
	if dir != 0 {
		p = py
	}
	var siblings []jsonv.Value
	for _, k := range kids(e.Doc, t.ID) {
		if !ign[idOf(k)] {
			siblings = append(siblings, k)
		}
	}
	index := len(siblings)
	var prev, next *render.Flat
	for i, k := range siblings {
		f := v.flat(idOf(k))
		if f == nil || jsonv.StrIs(jsonv.Get(k, "layoutPosition"), "absolute") {
			continue
		}
		if (lo(f)+hi(f))/2 > p {
			index, next = i, f
			break
		}
		prev = f
	}
	var pos float64
	switch {
	case prev == nil && next == nil:
		pos = (lo(t) + hi(t)) / 2
	case prev == nil:
		pos = (lo(t) + lo(next)) / 2
	case next == nil:
		pos = (hi(prev) + hi(t)) / 2
	default:
		pos = (hi(prev) + lo(next)) / 2
	}
	out.Set("index", jsonv.Int(int64(index)))
	ends := [2][2]float64{{pos, t.Y}, {pos, t.Y + t.H}}
	if dir != 0 {
		ends = [2][2]float64{{t.X, pos}, {t.X + t.W, pos}}
	}
	line := jsonv.NewArray()
	for _, pt := range ends {
		if t.M != nil {
			pt[0], pt[1] = apply(*t.M, pt[0], pt[1])
		}
		line.Items = append(line.Items, jsonv.Float(pt[0]), jsonv.Float(pt[1]))
	}
	out.Set("line", line)
	return out
}
