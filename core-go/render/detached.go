package render

import (
	"maps"
	"math"
	"runtime"
	"slices"
	"sort"
	"time"

	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

type slideClipSnap struct {
	tree *usvg.Tree
	dx   float64
	dy   float64
}

// SlideSnapshot is an immutable snapshot of one active Slide at a specific timestamp,
// allowing slide compositing to run completely outside the Engine lock.
type SlideSnapshot struct {
	ID          string
	DX          float64
	DY          float64
	Alpha       float64
	BeforeAlpha float64
	Waiting     bool
	Smooth      bool
	From        [2]float64
	Tree        *usvg.Tree
	TreeHash    uint64
	BeforeTree  *usvg.Tree
	BeforeClips []*usvg.Tree
	BeforeHash  uint64
	Over        *usvg.Tree
	Clips       []slideClipSnap
	LayerRank   int
}

// SceneSnapshot holds immutable layer pointers and pre-sampled slide states captured under
// Engine.Lock so pixel rasterization can execute off-lock on a background worker.
type SceneSnapshot struct {
	Version   uint64
	Layers    map[string]*Layer
	Order     []string
	Slides    []SlideSnapshot
	Bounds    Rect
	HasBounds bool
	Streaming bool
	Threads   int
}

// Snapshot captures an immutable SceneSnapshot from an already-synced Renderer.
// Must be called while holding the Engine lock.
func (r *Renderer) Snapshot(version uint64, now time.Time, solo string, streaming bool) SceneSnapshot {
	b, hasB := r.Bounds()
	snap := SceneSnapshot{
		Version:   version,
		Layers:    maps.Clone(r.Layers),
		Order:     slices.Clone(r.Order),
		Bounds:    b,
		HasBounds: hasB,
		Streaming: streaming,
		Threads:   r.threads(),
	}
	if len(r.Slides) == 0 {
		return snap
	}

	var shown map[string]bool
	if solo != "" {
		shown = map[string]bool{}
		if l, ok := r.Layers[solo]; ok {
			for i := range l.Flat {
				shown[l.Flat[i].ID] = true
			}
		}
	}
	for _, s := range r.Slides {
		if (shown == nil || shown[s.ID]) && !s.IsHeld(now) && !s.started {
			startAt := now
			if s.cursorHeld && !s.holdDeadline.IsZero() && s.holdDeadline.Before(now) {
				startAt = s.holdDeadline
			}
			s.cursorHeld = false
			s.start, s.started = startAt, true
		}
	}

	type st struct{ dx, dy, alpha float64 }
	states := make(map[string]st, len(r.Slides))
	for _, s := range r.Slides {
		dx, dy, a := s.State(now)
		states[s.ID] = st{
			dx:    float64(s.lin[0]*dx) + float64(s.lin[2]*dy),
			dy:    float64(s.lin[1]*dx) + float64(s.lin[3]*dy),
			alpha: a,
		}
	}

	slides := make([]SlideSnapshot, 0, len(r.Slides))
	for _, s := range r.Slides {
		if (s.tree == nil && s.beforeTree == nil) || (shown != nil && !shown[s.ID]) {
			continue
		}
		cur := states[s.ID]
		dx, dy, alpha := cur.dx, cur.dy, cur.alpha
		for _, a := range s.ancestors {
			if as, ok := states[a]; ok {
				dx += as.dx
				dy += as.dy
				alpha *= as.alpha
			}
		}
		var clips []slideClipSnap
		if len(s.clips) > 0 {
			clips = make([]slideClipSnap, 0, len(s.clips))
			for _, c := range s.clips {
				if c.tree == nil {
					continue
				}
				cdx, cdy := 0.0, 0.0
				for _, o := range c.owners {
					if os, ok := states[o]; ok {
						cdx += os.dx
						cdy += os.dy
					}
				}
				clips = append(clips, slideClipSnap{tree: c.tree, dx: cdx, dy: cdy})
			}
		}
		slides = append(slides, SlideSnapshot{
			ID:          s.ID,
			DX:          dx,
			DY:          dy,
			Alpha:       alpha,
			BeforeAlpha: s.BeforeAlpha(now),
			Waiting:     s.IsHeld(now) || (s.delay > 0 && s.elapsed(now) < s.delay),
			Smooth:      s.smooth,
			From:        s.from,
			Tree:        s.tree,
			TreeHash:    s.treeHash,
			BeforeTree:  s.beforeTree,
			BeforeClips: slices.Clone(s.beforeClips),
			BeforeHash:  s.beforeHash,
			Over:        s.over,
			Clips:       clips,
			LayerRank:   r.layerOf(s.ID),
		})
	}
	sort.SliceStable(slides, func(i, j int) bool {
		return slides[i].LayerRank < slides[j].LayerRank
	})
	snap.Slides = slides
	return snap
}

type detachedSlideCache struct {
	treeHash uint64
	zoom     float64
	key      [4]int64
	pm       *skia.Pixmap
}

type layerCacheKey struct {
	hash     uint64
	scripted bool
	finished uint64
}

func keyOfLayer(l *Layer) layerCacheKey {
	if l == nil {
		return layerCacheKey{}
	}
	var fin uint64
	if l.scripted {
		fin = l.finished
	}
	return layerCacheKey{hash: l.hash, scripted: l.scripted, finished: fin}
}

// DetachedRenderer rasterizes SceneSnapshots on a dedicated worker goroutine without holding Engine.Lock.
type DetachedRenderer struct {
	pix         []byte
	basePix     []byte
	baseValid   bool
	baseHashes  map[string]layerCacheKey
	baseOrder   []string
	baseW       uint32
	baseH       uint32
	baseVX      int32
	baseVY      int32
	baseZoom    float64
	baseBG      [3]uint8
	baseSolo    string
	ovPix       []byte
	ovValid     bool
	ovHashes    map[string]layerCacheKey
	ovOrder     []string
	ovW         uint32
	ovH         uint32
	ovVX        int32
	ovVY        int32
	ovZoom      float64
	ovBG        [3]uint8
	ovSolo      string
	ovDocRect   Rect
	slideCaches map[string]*detachedSlideCache
}

// NewDetachedRenderer creates a worker-private DetachedRenderer.
func NewDetachedRenderer() *DetachedRenderer {
	return &DetachedRenderer{
		slideCaches: make(map[string]*detachedSlideCache),
	}
}

func treeHasHeavyFilter(g *usvg.Group, depth int) bool {
	if g == nil || depth > 4 {
		return false
	}
	if len(g.Filters()) > 0 {
		return true
	}
	for _, n := range g.Children() {
		if sub, ok := n.(*usvg.Group); ok {
			if treeHasHeavyFilter(sub, depth+1) {
				return true
			}
		}
	}
	return false
}

func snapshotHasHeavyFilter(snap SceneSnapshot, solo string, view Rect) bool {
	for _, id := range snap.Order {
		if solo != "" && id != solo {
			continue
		}
		l, ok := snap.Layers[id]
		if !ok || l.tree == nil || !overlaps(l.paint, view) {
			continue
		}
		if len(l.backdrops) > 0 || treeHasHeavyFilter(l.tree.Root(), 0) {
			return true
		}
	}
	return false
}

func (d *DetachedRenderer) sameBase(snap SceneSnapshot, w, h uint32, vx, vy int32, zoom float64, bg [3]uint8, solo string) bool {
	if !d.baseValid || d.baseW != w || d.baseH != h || d.baseVX != vx || d.baseVY != vy ||
		math.Abs(d.baseZoom-zoom) > 1e-9 || d.baseBG != bg || d.baseSolo != solo {
		return false
	}
	if !equalStrings(d.baseOrder, snap.Order) || len(d.baseHashes) != len(snap.Layers) {
		return false
	}
	for id, l := range snap.Layers {
		if h0, ok := d.baseHashes[id]; !ok || h0 != keyOfLayer(l) {
			return false
		}
	}
	return true
}

func (d *DetachedRenderer) saveBase(snap SceneSnapshot, w, h uint32, vx, vy int32, zoom float64, bg [3]uint8, solo string) {
	need := int(w * h * 4)
	d.basePix = resize(d.basePix, need)
	copy(d.basePix, d.pix[:need])
	if d.baseHashes == nil {
		d.baseHashes = make(map[string]layerCacheKey, len(snap.Layers))
	} else {
		clear(d.baseHashes)
	}
	for id, l := range snap.Layers {
		d.baseHashes[id] = keyOfLayer(l)
	}
	d.baseOrder = append(d.baseOrder[:0], snap.Order...)
	d.baseW, d.baseH = w, h
	d.baseVX, d.baseVY = vx, vy
	d.baseZoom = zoom
	d.baseBG = bg
	d.baseSolo = solo
	d.baseValid = true
}

// Render paints snap for viewport (w, h, camX, camY, zoom) completely off-lock and returns RGBA8 bytes.
func (d *DetachedRenderer) Render(snap SceneSnapshot, w, h uint32, camX, camY, zoom float64, bg [3]uint8, solo string, sharp bool) []byte {
	w, h = min(max(w, 1), 8192), min(max(h, 1), 8192)
	zoom = fmin(fmax(zoom, 0.01), 256)
	vx, vy := i32(math.Round(camX*zoom)), i32(math.Round(camY*zoom))
	need := int(w * h * 4)
	d.pix = resize(d.pix, need)

	if d.sameBase(snap, w, h, vx, vy, zoom, bg, solo) && len(d.basePix) == need {
		copy(d.pix, d.basePix)
	} else {
		threads := snap.Threads
		if threads <= 0 {
			threads = min(max(runtime.NumCPU(), 1), 16)
		}
		viewDoc := Rect{X: float64(vx) / zoom, Y: float64(vy) / zoom, W: float64(w) / zoom, H: float64(h) / zoom}
		p := &painter{
			layers:  snap.Layers,
			order:   snap.Order,
			threads: threads,
		}
		// When a visible layer carries full-frame blur/shadow filters, splitting into 128px horizontal strips
		// causes resvg's ±512px filter region padding to rasterize the entire filtered container once per strip.
		// Using a single viewport pass (or >= 768px tall strips on huge viewports) eliminates 10x-14x overdraw.
		band := p.band(h)
		if snapshotHasHeavyFilter(snap, solo, viewDoc) {
			if h <= 1440 {
				band = h
			} else {
				band = max((h+1)/2, 768)
			}
		}
		if p.paintBands(solo, zoom, bg, vx, vy, w, h, band, d.pix, nil) {
			d.saveBase(snap, w, h, vx, vy, zoom, bg, solo)
		}
	}

	if len(snap.Slides) > 0 {
		d.compositeDetachedSlides(snap, w, h, camX, camY, zoom, solo)
	} else if len(d.slideCaches) > 0 {
		clear(d.slideCaches)
	}
	return d.pix
}

// RenderOverview rasterizes a low-to-medium resolution whole-document backdrop off-lock so rapid zoom-out
// or long pans always display coherent surrounding content with zero blank-edge pop-in.
func (d *DetachedRenderer) RenderOverview(snap SceneSnapshot, maxSide uint32, bg [3]uint8, solo string) ([]byte, uint32, uint32, Rect, bool) {
	if !snap.HasBounds || snap.Bounds.W <= 0 || snap.Bounds.H <= 0 || len(snap.Order) == 0 {
		return nil, 0, 0, Rect{}, false
	}
	maxSide = min(max(maxSide, 256), 2048)
	pad := fmax(fmax(snap.Bounds.W, snap.Bounds.H)*0.10, 120.0)
	bx := snap.Bounds.X - pad
	by := snap.Bounds.Y - pad
	bw := snap.Bounds.W + 2*pad
	bh := snap.Bounds.H + 2*pad

	zoom := fmin(float64(maxSide)/bw, float64(maxSide)/bh)
	if !(zoom > 0) || math.IsNaN(zoom) || math.IsInf(zoom, 0) {
		return nil, 0, 0, Rect{}, false
	}
	zoom = fmin(zoom, 2.0)
	ow := uint32(min(max(int(math.Round(bw*zoom)), 1), int(maxSide)))
	oh := uint32(min(max(int(math.Round(bh*zoom)), 1), int(maxSide)))
	vx := i32(math.Round(bx * zoom))
	vy := i32(math.Round(by * zoom))
	docRect := Rect{
		X: float64(vx) / zoom,
		Y: float64(vy) / zoom,
		W: float64(ow) / zoom,
		H: float64(oh) / zoom,
	}

	need := int(ow * oh * 4)
	if d.ovValid && d.ovW == ow && d.ovH == oh && d.ovVX == vx && d.ovVY == vy &&
		math.Abs(d.ovZoom-zoom) <= 1e-9 && d.ovBG == bg && d.ovSolo == solo &&
		equalStrings(d.ovOrder, snap.Order) && len(d.ovHashes) == len(snap.Layers) && len(d.ovPix) == need {
		same := true
		for id, l := range snap.Layers {
			if h0, ok := d.ovHashes[id]; !ok || h0 != keyOfLayer(l) {
				same = false
				break
			}
		}
		if same {
			return d.ovPix[:need], ow, oh, d.ovDocRect, true
		}
	}

	d.ovPix = resize(d.ovPix, need)
	threads := snap.Threads
	if threads <= 0 {
		threads = min(max(runtime.NumCPU(), 1), 16)
	}
	p := &painter{
		layers:  snap.Layers,
		order:   snap.Order,
		threads: threads,
	}
	band := p.band(oh)
	if snapshotHasHeavyFilter(snap, solo, docRect) {
		band = oh
	}
	if !p.paintBands(solo, zoom, bg, vx, vy, ow, oh, band, d.ovPix, nil) {
		return nil, 0, 0, Rect{}, false
	}
	if d.ovHashes == nil {
		d.ovHashes = make(map[string]layerCacheKey, len(snap.Layers))
	} else {
		clear(d.ovHashes)
	}
	for id, l := range snap.Layers {
		d.ovHashes[id] = keyOfLayer(l)
	}
	d.ovOrder = append(d.ovOrder[:0], snap.Order...)
	d.ovW, d.ovH = ow, oh
	d.ovVX, d.ovVY = vx, vy
	d.ovZoom = zoom
	d.ovBG = bg
	d.ovSolo = solo
	d.ovDocRect = docRect
	d.ovValid = true
	return d.ovPix[:need], ow, oh, docRect, true
}

func (d *DetachedRenderer) compositeDetachedSlides(snap SceneSnapshot, w, h uint32, camX, camY, zoom float64, solo string) {
	vx, vy := i32(math.Round(camX*zoom)), i32(math.Round(camY*zoom))
	out, ok := skia.PixmapMutFromBytes(d.pix, w, h)
	if !ok {
		return
	}

	activeIDs := make(map[string]bool, len(snap.Slides)*2)
	for _, s := range snap.Slides {
		activeIDs[s.ID] = true
		beforeKey := s.ID + "#before"
		if s.BeforeTree != nil {
			activeIDs[beforeKey] = true
		}
		if (s.Tree == nil && s.BeforeTree == nil) || (s.Waiting && s.BeforeAlpha <= 1e-3) {
			continue
		}
		m := fmax(math.Abs(s.From[0]), math.Abs(s.From[1])) + 2*slideOffset
		view := Rect{X: camX - m, Y: camY - m, W: float64(w)/zoom + 2*m, H: float64(h)/zoom + 2*m}

		var coverMinX, coverMinY, coverMaxX, coverMaxY int32
		hasPainted := false
		var bpx, bpy int32
		var readyBPM *skia.Pixmap

		if s.BeforeTree != nil && s.BeforeAlpha > 1e-3 {
			bb0, _ := s.BeforeTree.Root().AbsLayerBoundingBox()
			b0 := Rect{X: float64(bb0.X()), Y: float64(bb0.Y()), W: float64(bb0.Width()), H: float64(bb0.Height())}
			if overlaps(b0, view) {
				bx0, by0 := i32(math.Floor(fmax(b0.X, view.X)*zoom)), i32(math.Floor(fmax(b0.Y, view.Y)*zoom))
				bx1 := i32(math.Ceil(fmin(b0.X+b0.W, view.X+view.W) * zoom))
				by1 := i32(math.Ceil(fmin(b0.Y+b0.H, view.Y+view.H) * zoom))
				bkey := [4]int64{int64(bx0), int64(by0), int64(max(bx1-bx0, 1)), int64(max(by1-by0, 1))}
				bsc := d.slideCaches[beforeKey]
				if bsc == nil || (s.BeforeHash != 0 && bsc.treeHash != s.BeforeHash) || bsc.zoom != zoom || bsc.key != bkey {
					if bpm, ok := skia.NewPixmap(uint32(bkey[2]), uint32(bkey[3])); ok {
						resvg.Render(s.BeforeTree, skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom), float32(-bx0), float32(-by0)), bpm.AsMut())
						applyStaticClipTrees(bpm, s.BeforeClips, zoom, bx0, by0)
						bsc = &detachedSlideCache{treeHash: s.BeforeHash, zoom: zoom, key: bkey, pm: bpm}
						d.slideCaches[beforeKey] = bsc
					}
				}
				if bsc != nil && bsc.pm != nil {
					readyBPM = bsc.pm
					bpx, bpy = bx0-vx, by0-vy
				}
			}
		}

		var px, py int32
		var readyPM *skia.Pixmap
		drawTF := skia.Identity()
		useSubpixel := false
		if !s.Waiting && s.Tree != nil && (s.BeforeTree == nil || s.Alpha > 1e-3) {
			bb, _ := s.Tree.Root().AbsLayerBoundingBox()
			b := Rect{X: float64(bb.X()), Y: float64(bb.Y()), W: float64(bb.Width()), H: float64(bb.Height())}
			if overlaps(b, view) {
				x0, y0 := i32(math.Floor(fmax(b.X, view.X)*zoom)), i32(math.Floor(fmax(b.Y, view.Y)*zoom))
				x1 := i32(math.Ceil(fmin(b.X+b.W, view.X+view.W) * zoom))
				y1 := i32(math.Ceil(fmin(b.Y+b.H, view.Y+view.H) * zoom))
				key := [4]int64{int64(x0), int64(y0), int64(max(x1-x0, 1)), int64(max(y1-y0, 1))}

				sc := d.slideCaches[s.ID]
				if sc == nil || (s.TreeHash != 0 && sc.treeHash != s.TreeHash) || sc.zoom != zoom || sc.key != key {
					if pm, ok := skia.NewPixmap(uint32(key[2]), uint32(key[3])); ok {
						resvg.Render(s.Tree, skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom), float32(-x0), float32(-y0)), pm.AsMut())
						sc = &detachedSlideCache{treeHash: s.TreeHash, zoom: zoom, key: key, pm: pm}
						d.slideCaches[s.ID] = sc
					}
				}
				if sc != nil && sc.pm != nil {
					pm := sc.pm
					rx, ry := i32(math.Round(s.DX*zoom)), i32(math.Round(s.DY*zoom))
					px, py = x0-vx+rx, y0-vy+ry
					if s.Smooth {
						fx := float32(s.DX*zoom - float64(rx))
						fy := float32(s.DY*zoom - float64(ry))
						if math.Abs(float64(fx)) > 1e-3 || math.Abs(float64(fy)) > 1e-3 {
							useSubpixel = true
							drawTF = skia.TransformFromRow(1, 0, 0, 1, fx, fy)
						}
					}

					if len(s.Clips) > 0 {
						cut, _ := skia.PixmapFromVec(append([]byte(nil), pm.Data()...), pm.Size())
						for _, c := range s.Clips {
							shape, ok := skia.NewPixmap(cut.Width(), cut.Height())
							if !ok {
								continue
							}
							tf := skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom),
								float32(-x0-rx)+float32(math.Round(c.dx*zoom)), float32(-y0-ry)+float32(math.Round(c.dy*zoom)))
							resvg.Render(c.tree, tf, shape.AsMut())
							cut.ApplyMask(skia.MaskFromPixmap(shape.AsRef(), skia.MaskTypeAlpha))
						}
						pm = cut
					}
					readyPM = pm
				}
			}
		}

		if readyBPM != nil && readyPM != nil && s.Alpha > 1e-3 && s.Alpha < 0.999 {
			if ux0, uy0, ux1, uy1, ok := blendEditPixmaps(out, w, h, bpx, bpy, readyBPM, px, py, readyPM, drawTF, useSubpixel, s.Alpha); ok {
				coverMinX, coverMinY, coverMaxX, coverMaxY = ux0, uy0, ux1, uy1
				hasPainted = true
			}
		} else {
			if readyBPM != nil && (readyPM == nil || s.Alpha <= 1e-3) {
				bpaint := skia.DefaultPixmapPaint()
				bpaint.Opacity = float32(fmin(fmax(s.BeforeAlpha, 0), 1))
				out.DrawPixmap(bpx, bpy, readyBPM.AsRef(), &bpaint, skia.Identity(), nil)
				coverMinX, coverMinY = bpx, bpy
				coverMaxX, coverMaxY = bpx+int32(readyBPM.Width()), bpy+int32(readyBPM.Height())
				hasPainted = true
			}
			if readyPM != nil {
				paint := skia.DefaultPixmapPaint()
				paint.Opacity = float32(fmin(fmax(s.Alpha, 0), 1))
				if useSubpixel {
					paint.Quality = skia.FilterQualityBilinear
				}
				out.DrawPixmap(px, py, readyPM.AsRef(), &paint, drawTF, nil)
				if !hasPainted {
					coverMinX, coverMinY = px, py
					coverMaxX, coverMaxY = px+int32(readyPM.Width()), py+int32(readyPM.Height())
					hasPainted = true
				} else {
					coverMinX, coverMinY = min(coverMinX, px), min(coverMinY, py)
					coverMaxX, coverMaxY = max(coverMaxX, px+int32(readyPM.Width())), max(coverMaxY, py+int32(readyPM.Height()))
				}
			}
		}

		if !hasPainted {
			continue
		}

		var above []string
		if s.LayerRank >= 0 && s.LayerRank+1 < len(snap.Order) {
			above = snap.Order[s.LayerRank+1:]
		}
		cx0, cy0, cx1, cy1 := max(coverMinX, 0), max(coverMinY, 0), min(coverMaxX, int32(w)), min(coverMaxY, int32(h))
		if cx1 <= cx0 || cy1 <= cy0 {
			continue
		}
		cover := Rect{X: float64(cx0+vx) / zoom, Y: float64(cy0+vy) / zoom, W: float64(cx1-cx0) / zoom, H: float64(cy1-cy0) / zoom}
		var trees []*usvg.Tree
		if s.Over != nil {
			trees = append(trees, s.Over)
		}
		for _, id := range above {
			if solo != "" && solo != id {
				continue
			}
			if l, ok := snap.Layers[id]; ok && overlaps(l.paint, cover) && l.tree != nil {
				trees = append(trees, l.tree)
			}
		}
		if len(trees) == 0 {
			continue
		}
		over, ok := skia.NewPixmap(uint32(cx1-cx0), uint32(cy1-cy0))
		if !ok {
			continue
		}
		tf := skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom), float32(-(cx0 + vx)), float32(-(cy0 + vy)))
		for _, t := range trees {
			if hasHairline(t.Root()) {
				resvg.Render(t, tf, over.AsMut())
			} else {
				drawCulled(t.Root(), tf, cover, cover, over.AsMut())
			}
		}
		def := skia.DefaultPixmapPaint()
		out.DrawPixmap(cx0, cy0, over.AsRef(), &def, skia.Identity(), nil)
	}

	for id := range d.slideCaches {
		if !activeIDs[id] {
			delete(d.slideCaches, id)
		}
	}
}
