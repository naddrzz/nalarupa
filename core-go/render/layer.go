package render

import (
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// prepareLayer lays out a top-level node and emits its SVG; the tree is parsed later (on goroutines).
// ok is false when the node resolves to nothing (hidden).
func prepareLayer(cx *Ctx, resolver *resolve.Resolver, index int, hash, finished uint64, hidden map[string]bool) (*Layer, string, bool) {
	root := cx.laidOutIn(resolver, index, true)
	if root == nil {
		return &Layer{hash: hash}, "", false
	}
	var flat []Flat
	var stack []string
	var idx []int
	// Per depth: this node or an ancestor is rotated or flipped / its painted matrix / it is hidden while it
	// slides / it has opacity 0.
	var turned, sliding, faded []bool
	var mats []*[6]float64
	scripted := false
	root.Visit(func(n *resolve.RNode, depth int) {
		scripted = scripted || n.Type == "script"
		stack = stack[:min(len(stack), depth)]
		idx = idx[:min(len(idx), depth+1)]
		turned, mats = turned[:min(len(turned), depth)], mats[:min(len(mats), depth)]
		sliding, faded = sliding[:min(len(sliding), depth)], faded[:min(len(faded), depth)]
		op, ok := n.F("opacity")
		if !ok {
			op = 1
		}
		faded = append(faded, op <= 0 || len(faded) > 0 && faded[len(faded)-1])
		rotated := svg.IsTurned(n)
		turned = append(turned, rotated || len(turned) > 0 && turned[len(turned)-1])
		var parentM *[6]float64
		if len(mats) > 0 {
			parentM = mats[len(mats)-1]
		}
		if rotated {
			m := compose(parentM, svg.OwnMatrix(n))
			mats = append(mats, &m)
		} else {
			mats = append(mats, parentM)
		}
		if len(idx) <= depth {
			idx = append(idx, 0)
		} else {
			idx[depth]++
		}
		var sig strings.Builder
		sig.WriteString(n.Type + "|" + ftoa(n.X) + "|" + ftoa(n.Y) + "|" + ftoa(n.W) + "|" + ftoa(n.H) + "|")
		if len(stack) > 0 {
			sig.WriteString("Some(" + strconv.Quote(stack[len(stack)-1]) + ")")
		} else {
			sig.WriteString("None")
		}
		sig.WriteString("|" + strconv.Itoa(idx[depth]))
		n.P.Each(func(k string, v jsonv.Value) {
			if k != "children" {
				sig.WriteString(k)
				sig.Write(jsonv.Marshal(v))
			}
		})
		// The whole subtree: when the slide ends, descendants that overflow the node repaint too.
		sliding = append(sliding, hidden[n.ID] || len(sliding) > 0 && sliding[len(sliding)-1])
		if sliding[depth] {
			sig.WriteString("|sliding")
		}
		f := Flat{
			ID: n.ID, Name: n.Name(), Type: n.Type, X: n.X, Y: n.Y, W: n.W, H: n.H, Depth: depth,
			Problem:     n.Problem,
			Placeholder: n.Type == "frame" && n.B("placeholder"),
			Faded:       faded[depth],
			M:           mats[depth],
			LocalX:      n.BX, LocalY: n.BY,
			FlipX: n.B("flipX"), FlipY: n.B("flipY"),
			Clip:  n.Type == "frame" && n.B("clip"),
			shape: ShapeOf(n),
			sig:   fxhash(sig.String()),
		}
		f.Rotation, _ = n.F("rotation")
		if len(stack) > 0 {
			f.Parent, f.HasParent = stack[len(stack)-1], true
		}
		fill, _ := n.P.Get("fill")
		for _, p := range svg.Fills(fill) {
			if u, ok := jsonv.Str(jsonv.Get(p, "url")); ok && svg.IsPendingURL(u) {
				f.Pending = true
				break
			}
		}
		if n.Type == "rectangle" || n.Type == "frame" {
			f.Radius = svg.Radii(n)
		}
		if turned[depth] {
			f.reach = math.Inf(1)
		} else {
			f.reach = reach(n)
		}
		flat = append(flat, f)
		stack = append(stack, n.ID)
	}, 0)
	const pad = 64.0
	x0, y0, x1, y1 := math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64
	for i := range flat {
		f := &flat[i]
		x0, y0, x1, y1 = fmin(x0, f.X), fmin(y0, f.Y), fmax(x1, f.X+f.W), fmax(y1, f.Y+f.H)
	}
	x0, y0, x1, y1 = x0-pad, y0-pad, x1+pad, y1+pad
	padded := Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	bbox := padded
	if root.B("clip") && root.Type == "frame" {
		bbox = Rect{X: root.X, Y: root.Y, W: root.W, H: root.H}
	}
	s := cx.emitter()
	for id := range hidden {
		s.Skip[id] = true
	}
	s.Node(root)
	return &Layer{hash: hash, BBox: bbox, paint: padded, Flat: flat, scripted: scripted, finished: finished}, s.Finish(nil), true
}

// reach is a generous paint reach outside a node's box from its effects, stroke and text overflow.
func reach(n *resolve.RNode) float64 {
	num := func(v jsonv.Value) float64 {
		f, _ := jsonv.F64(v)
		return math.Abs(f)
	}
	var effects []jsonv.Value
	switch e := jsonv.Get(n.P, "effect").(type) {
	case *jsonv.Array:
		effects = e.Items
	case *jsonv.Object:
		effects = []jsonv.Value{e}
	}
	fx := 0.0
	for _, e := range effects {
		off := jsonv.Get(e, "offset")
		v := fmax(num(jsonv.Get(off, "x")), num(jsonv.Get(off, "y"))) + float64(3*fmax(num(jsonv.Get(e, "blur")), num(jsonv.Get(e, "radius")))) + num(jsonv.Get(e, "spread"))
		fx = fmax(fx, v)
	}
	stroke := 0.0
	if sw, ok := n.P.Get("strokeWidth"); ok {
		if m, isObj := sw.(*jsonv.Object); isObj {
			m.Each(func(_ string, v jsonv.Value) { stroke = fmax(stroke, num(v)) })
		} else {
			stroke = num(sw)
		}
	}
	// Glyphs spill out of a line box shorter than the font's ascent plus descent (tight line heights,
	// overhangs): one em each way. A fixed-size box also lets its lines run past it: lines below the box
	// and words wider than it.
	glyphs := 0.0
	if n.Type == "text" {
		st := layout.StyleOf(n)
		lines := float64(float64(float64(len(n.Lines))*st.Size) * fmax(st.LineHeight, 2))
		wide := 0.0
		for _, l := range n.Lines {
			wide = fmax(wide, l.Width)
		}
		glyphs = st.Size + fmax(fmax(lines-n.H, wide-n.W), 0)
	}
	return 4 + fx + stroke + glyphs
}

// changedNodes appends the doc rects that differ between two builds of one layer: nodes added, removed,
// moved or restyled.
func changedNodes(old, nw *Layer, out []Rect) []Rect {
	// The root carries the frame's own fill, clip and effects: any change there repaints it whole.
	firstSig := func(l *Layer) (uint64, bool) {
		if len(l.Flat) == 0 {
			return 0, false
		}
		return l.Flat[0].sig, true
	}
	a, oka := firstSig(old)
	b, okb := firstSig(nw)
	if a != b || oka != okb {
		return append(out, old.paint, nw.paint)
	}
	before := map[string]int{}
	for i := range old.Flat {
		before[old.Flat[i].ID] = i
	}
	seen := map[string]bool{}
	// (layer: 0 old, 1 new; index into its Flat)
	var changed [][2]int
	for i := range nw.Flat {
		f := &nw.Flat[i]
		seen[f.ID] = true
		o, ok := before[f.ID]
		switch {
		case ok && old.Flat[o].sig == f.sig:
		case ok:
			changed = append(changed, [2]int{0, o}, [2]int{1, i})
		default:
			changed = append(changed, [2]int{1, i})
		}
	}
	for i := range old.Flat {
		if !seen[old.Flat[i].ID] {
			changed = append(changed, [2]int{0, i})
		}
	}
	// A clip frame that changed or went away cuts (or stops cutting) its descendants, and a node moved
	// under another parent changes which clips cut its own: their signatures stay the same, so repaint
	// wherever they reach. Both are covered by the subtree areas below, except a moved node's own
	// subtree, which keeps its signatures: add the moved node.
	for i := range nw.Flat {
		f := &nw.Flat[i]
		if o, ok := before[f.ID]; ok && (old.Flat[o].Parent != f.Parent || old.Flat[o].HasParent != f.HasParent) {
			changed = append(changed, [2]int{0, o}, [2]int{1, i})
		}
	}
	areas := [2][]Rect{dirtyAreas(old), dirtyAreas(nw)}
	rects := make([]Rect, len(changed))
	for i, c := range changed {
		rects[i] = areas[c[0]][c[1]]
	}
	// A rotated or flipped node paints outside its layout box: repaint the whole layer.
	for _, r := range rects {
		if !finite(r.X) || !finite(r.Y) || !finite(r.W) || !finite(r.H) {
			return append(out, old.paint, nw.paint)
		}
	}
	return append(out, rects...)
}

func finite(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) }

// dirtyAreas is, per node of layer, what a change of it can repaint: its own box and reach, joined with
// its descendants' (a container's opacity, blend mode, effect or clip applies to its whole subtree, which
// may stick out of its box), and grown by the reach of every ancestor that paints past its children (a
// group's shadow or blur follows its children's outlines). Two passes over the preorder Flat.
func dirtyAreas(layer *Layer) []Rect {
	flat := layer.Flat
	parent := make([]int, len(flat))
	grow := make([]float64, len(flat))
	var stack []int
	for i := range flat {
		parent[i] = -1
		for len(stack) > 0 && flat[stack[len(stack)-1]].Depth >= flat[i].Depth {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			p := stack[len(stack)-1]
			parent[i] = p
			// reach is 4 for a node that paints nothing past its box (see reach).
			g := 0.0
			if flat[p].reach > 4 {
				g = flat[p].reach
			}
			grow[i] = grow[p] + g
		}
		stack = append(stack, i)
	}
	area := make([]Rect, len(flat))
	for i := range flat {
		f := &flat[i]
		r := f.reach + grow[i]
		area[i] = Rect{X: f.X - r, Y: f.Y - r, W: f.W + float64(2*r), H: f.H + float64(2*r)}
	}
	// Children come after their parent: fold each subtree into it from the back.
	for i := len(flat) - 1; i >= 0; i-- {
		if p := parent[i]; p >= 0 {
			area[p] = union(area[p], area[i])
		}
	}
	return area
}

// merge coalesces overlapping dirty rects; many scattered ones become their union.
func merge(rects []Rect) []Rect {
	rects = append([]Rect(nil), rects...)
	var out []Rect
	for len(rects) > 0 {
		r := rects[len(rects)-1]
		rects = rects[:len(rects)-1]
		for {
			i := -1
			for j, o := range out {
				if overlaps(o, r) {
					i = j
					break
				}
			}
			if i < 0 {
				break
			}
			// swap_remove
			o := out[i]
			out[i] = out[len(out)-1]
			out = out[:len(out)-1]
			r = union(r, o)
		}
		out = append(out, r)
	}
	if len(out) > 8 {
		u := out[0]
		for _, o := range out[1:] {
			u = union(u, o)
		}
		out = []Rect{u}
	}
	return out
}

// underBackdrops grows the rects for background blurs: a blur shows what lies beneath it, so a change
// within its blur reach repaints all of it (and, in turn, any background blur over that).
func underBackdrops(rects []Rect, layers map[string]*Layer, order []string) []Rect {
	var bds []backdrop
	seen := map[string]bool{}
	for _, id := range order {
		if l, ok := layers[id]; ok && !seen[id] {
			seen[id] = true
			bds = append(bds, l.backdrops...)
		}
	}
	for {
		grown := false
		for i := range rects {
			r := &rects[i]
			for _, b := range bds {
				reach := b.reach
				near := overlaps(Rect{X: r.X - reach, Y: r.Y - reach, W: r.W + float64(2*reach), H: r.H + float64(2*reach)}, b.box)
				inside := r.X <= b.box.X && r.Y <= b.box.Y && r.X+r.W >= b.box.X+b.box.W && r.Y+r.H >= b.box.Y+b.box.H
				if near && !inside {
					*r = union(*r, b.box)
					grown = true
				}
			}
		}
		if !grown {
			return rects
		}
		rects = merge(rects)
	}
}
