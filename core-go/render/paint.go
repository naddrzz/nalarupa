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

// band is the height of the bands a paint h pixels tall splits into.
func (p *painter) band(h uint32) uint32 {
	threads := uint32(p.threads)
	return max((h+threads-1)/threads, 128)
}

// area is the world-device pixels [x0, x1) × [y0, y1).
type area struct{ x0, y0, x1, y1 int32 }

func (a area) empty() bool { return a.x1 <= a.x0 || a.y1 <= a.y0 }

// paint paints world-device pixels [x0, x0+w) × [y0, y0+h) at zoom into out (w×h RGBA, opaque) in
// horizontal bands on goroutines. False if a band panicked.
func (p *painter) paint(solo string, zoom float64, bg [3]uint8, x0, y0 int32, w, h uint32, out []byte) bool {
	return p.paintBands(solo, zoom, bg, x0, y0, w, h, p.band(h), out, nil)
}

// scratch holds the band buffers repaints paint into (*[]byte), reused across repaints.
var scratch sync.Pool

// paintBands is paint in bands band rows tall that, when only is not nil, repaints just only[i] (inside
// band i) of each band i; the other pixels keep theirs. The band is still painted at its full size, origin
// and transform, drawing what reaches that area, so the area comes out as a full paint gives it.
func (p *painter) paintBands(solo string, zoom float64, bg [3]uint8, x0, y0 int32, w, h, band uint32, out []byte, only []area) bool {
	if len(out) != int(w*h*4) || band == 0 {
		return false
	}
	chunk := int(w * band * 4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := true
	for i := 0; i*chunk < len(out); i++ {
		part := out[i*chunk : min((i+1)*chunk, len(out))]
		by := y0 + int32(uint32(i)*band)
		bh := uint32(len(part) / (int(w) * 4))
		view := Rect{X: float64(x0) / zoom, Y: float64(by) / zoom, W: float64(w) / zoom, H: float64(bh) / zoom}
		cull := view
		var a area
		if only != nil {
			if i >= len(only) || only[i].empty() {
				continue
			}
			a = only[i]
			cull = Rect{X: float64(a.x0) / zoom, Y: float64(a.y0) / zoom, W: float64(a.x1-a.x0) / zoom, H: float64(a.y1-a.y0) / zoom}
		}
		job := func() {
			defer func() {
				if recover() != nil {
					mu.Lock()
					ok = false
					mu.Unlock()
				}
			}()
			if only == nil {
				paintBand(p.layers, p.order, solo, zoom, bg, x0, by, w, bh, view, cull, part)
				return
			}
			buf, _ := scratch.Get().(*[]byte)
			if buf == nil || cap(*buf) < len(part) {
				b := make([]byte, len(part))
				buf = &b
			}
			defer scratch.Put(buf)
			// paintBand fills the whole band first, so what the buffer held does not matter.
			tmp := (*buf)[:len(part)]
			paintBand(p.layers, p.order, solo, zoom, bg, x0, by, w, bh, view, cull, tmp)
			stride := int(w) * 4
			from, to := int(a.x0-x0)*4, int(a.x1-x0)*4
			for row := int(a.y0 - by); row < int(a.y1-by); row++ {
				copy(part[row*stride+from:row*stride+to], tmp[row*stride+from:row*stride+to])
			}
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

// paintBand paints the band [x0, x0+w) × [y0, y0+h) (view in doc space) into out, drawing only what
// reaches cull (doc space, inside view).
func paintBand(layers map[string]*Layer, order []string, solo string, zoom float64, bg [3]uint8, x0, y0 int32, w, h uint32, view, cull Rect, out []byte) {
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
		if !ok || !overlaps(l.paint, cull) || l.tree == nil {
			continue
		}
		if hasHairline(l.tree.Root()) {
			resvg.Render(l.tree, tf, pm)
		} else {
			drawCulled(l.tree.Root(), tf, view, cull, pm)
		}
	}
}

// drawCulled draws only the nodes that reach cull (doc space, inside view). resvg gives every isolated
// group (opacity, clip, filter) its own layer of up to 5x the target, even off-screen, so plain groups are
// descended into and invisible subtrees are skipped entirely. Whether a group is descended into depends on
// view alone, so a node is drawn the same way whatever cull is.
func drawCulled(g *usvg.Group, tf skia.Transform, view, cull Rect, pm skia.PixmapMut) {
	parent := tf.PreConcat(g.AbsTransform())
	for _, node := range g.Children() {
		bb, ok := node.AbsLayerBoundingBox()
		if !ok {
			continue
		}
		r := Rect{X: float64(bb.X()), Y: float64(bb.Y()), W: float64(bb.Width()), H: float64(bb.Height())}
		if !overlaps(r, cull) {
			continue
		}
		if sub, isGroup := node.(*usvg.Group); isGroup {
			inside := r.X >= view.X && r.Y >= view.Y && r.X+r.W <= view.X+view.W && r.Y+r.H <= view.Y+view.H
			if !sub.ShouldIsolate() && !inside && !hasHairline(sub) {
				drawCulled(sub, tf, view, cull, pm)
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
