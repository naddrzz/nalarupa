package render

import (
	"sync"

	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// painter is what painting needs from the renderer, apart from the cached view.
type painter struct {
	layers  map[string]*Layer
	order   []string
	threads int
	// serial paints the bands one after another (tests compare it with the goroutine run).
	serial bool
}

// paint paints world-device pixels [x0, x0+w) × [y0, y0+h) at zoom into out (w×h RGBA, opaque) in
// horizontal bands on goroutines. False if a band panicked.
func (p *painter) paint(solo string, zoom float64, bg [3]uint8, x0, y0 int32, w, h uint32, out []byte) bool {
	if len(out) != int(w*h*4) {
		return false
	}
	threads := uint32(p.threads)
	band := max((h+threads-1)/threads, 128)
	chunk := int(w * band * 4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := true
	for i := 0; i*chunk < len(out); i++ {
		part := out[i*chunk : min((i+1)*chunk, len(out))]
		by := y0 + int32(uint32(i)*band)
		bh := uint32(len(part) / (int(w) * 4))
		job := func() {
			defer func() {
				if recover() != nil {
					mu.Lock()
					ok = false
					mu.Unlock()
				}
			}()
			paintBand(p.layers, p.order, solo, zoom, bg, x0, by, w, bh, part)
		}
		if p.serial {
			job()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			job()
		}()
	}
	wg.Wait()
	return ok
}

func paintBand(layers map[string]*Layer, order []string, solo string, zoom float64, bg [3]uint8, x0, y0 int32, w, h uint32, out []byte) {
	view := Rect{X: float64(x0) / zoom, Y: float64(y0) / zoom, W: float64(w) / zoom, H: float64(h) / zoom}
	pm, ok := skia.PixmapMutFromBytes(out, w, h)
	if !ok {
		return
	}
	pm.Fill(skia.ColorFromRGBA8(bg[0], bg[1], bg[2], 255))
	tf := skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom), float32(-x0), float32(-y0))
	for _, id := range order {
		if solo != "" && id != solo {
			continue
		}
		l, ok := layers[id]
		if !ok || !overlaps(l.paint, view) || l.tree == nil {
			continue
		}
		if hasHairline(l.tree.Root()) {
			resvg.Render(l.tree, tf, pm)
		} else {
			drawCulled(l.tree.Root(), tf, view, pm)
		}
	}
}

// drawCulled draws only the nodes that reach view (doc space). resvg gives every isolated group (opacity,
// clip, filter) its own layer of up to 5x the target, even off-screen, so plain groups are descended into
// and invisible subtrees are skipped entirely.
func drawCulled(g *usvg.Group, tf skia.Transform, view Rect, pm skia.PixmapMut) {
	parent := tf.PreConcat(g.AbsTransform())
	for _, node := range g.Children() {
		bb, ok := node.AbsLayerBoundingBox()
		if !ok {
			continue
		}
		r := Rect{X: float64(bb.X()), Y: float64(bb.Y()), W: float64(bb.Width()), H: float64(bb.Height())}
		if !overlaps(r, view) {
			continue
		}
		if sub, isGroup := node.(*usvg.Group); isGroup {
			inside := r.X >= view.X && r.Y >= view.Y && r.X+r.W <= view.X+view.W && r.Y+r.H <= view.Y+view.H
			if !sub.ShouldIsolate() && !inside && !hasHairline(sub) {
				drawCulled(sub, tf, view, pm)
				continue
			}
		}
		// RenderNode shifts by the node's layer box (it targets a node-sized pixmap); undo that.
		resvg.RenderNode(node, parent.PreTranslate(bb.X(), bb.Y()), pm)
	}
}

// hasHairline: a stroked horizontal or vertical path has no layer box of its own, so RenderNode cannot
// draw it alone; its group is then drawn whole (the group's box includes the stroke).
func hasHairline(g *usvg.Group) bool {
	for _, n := range g.Children() {
		if p, ok := n.(*usvg.Path); ok && p.Stroke() != nil {
			if _, has := p.AbsLayerBoundingBox(); !has {
				return true
			}
		}
	}
	return false
}
