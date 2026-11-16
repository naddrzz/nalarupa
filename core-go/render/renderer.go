// Package render is the canvas half of render.rs and all of hit.rs on the resvg path: one cached usvg tree
// per top-level node, rebuilt only when that node (or shared components and variables) changes. The canvas
// keeps one raster of the viewport plus a margin: panning inside it is a copy, zooming scales it until the
// UI asks for a sharp frame, and edits repaint only their dirty rectangle. Painting culls per node and
// splits into bands painted on goroutines. Agent-inserted nodes slide into place (slides.go), and clicks
// are resolved against the laid-out boxes (hit.go).
//
// A Renderer is used from one goroutine at a time (Rust's sits under the engine lock). Floating-point
// products are wrapped in explicit conversions so they are never fused into a multiply-add.
package render

import (
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

const (
	// margin is how many device pixels are kept around the viewport on each side.
	margin = 512
	// maxPreviewScale bounds the zoom ratio a scaled preview is used for; beyond it the frame is repainted.
	maxPreviewScale = 3.0
	// liveBudgetMS: live zoom frames are painted sharp (viewport only) while that is estimated to fit it.
	liveBudgetMS = 30.0
)

// Ctx is render.rs `Ctx`: the document, its imported libraries, the fonts (text measuring and the font
// database usvg resolves against) and the image and icon assets.
type Ctx struct {
	Doc    *doc.Doc
	Libs   []resolve.Lib
	Fonts  *text.Fonts
	Assets *svg.Assets
	// Scripts runs the script nodes of a laid-out top-level node (live: a run not finished shows its previous
	// output and finishes in the background). nil runs them with script.ExpandScripts.
	Scripts func(n *resolve.RNode, r *resolve.Resolver, live bool)
	// Runs, when set, notes the script runs a layout that is not live needs instead of waiting for them
	// (script::record_runs): they draw as running until script.Runs.Run has run them.
	Runs *script.Runs
	// Finished counts the background script runs that have finished; nil is script.Finished.
	Finished func() uint64
}

func (c *Ctx) finished() uint64 {
	if c.Finished == nil {
		return script.Finished()
	}
	return c.Finished()
}

func (c *Ctx) expandScripts(n *resolve.RNode, r *resolve.Resolver, live bool) {
	if c.Scripts != nil {
		c.Scripts(n, r, live)
		return
	}
	script.ExpandScripts(n, r, c.Fonts, c.Doc.Dir(), c.Doc.Path, live, c.Runs)
}

// LaidOut resolves and lays out the top-level node at index; script nodes run to completion first.
func (c *Ctx) LaidOut(index int) *resolve.RNode {
	return c.laidOutIn(resolve.New(c.Doc.Root, c.Libs), index, false)
}

// LaidOutLive is LaidOut for callers that hold the engine lock: a script node whose run is not finished
// shows its previous output (or a "running" box).
func (c *Ctx) LaidOutLive(index int) *resolve.RNode {
	return c.laidOutIn(resolve.New(c.Doc.Root, c.Libs), index, true)
}

// laidOutIn is LaidOut with a resolver built by the caller, so a pass over many layers indexes the
// document once.
func (c *Ctx) laidOutIn(r *resolve.Resolver, index int, live bool) *resolve.RNode {
	kids := c.Doc.Children()
	if index < 0 || index >= len(kids) {
		return nil
	}
	n := r.Resolve(kids[index])
	if n == nil {
		return nil
	}
	layout.Layout(n, c.Fonts)
	// Script nodes need their laid-out size before they can run.
	c.expandScripts(n, r, live)
	return n
}

// TreeOptions is render.rs `tree_options`. Its image resolver counts nesting per parse, so each goroutine
// takes its own.
func (c *Ctx) TreeOptions() usvg.Options {
	opt := usvg.DefaultOptions()
	opt.FontDB = c.Fonts.Database()
	opt.FontResolver = usvg.FontsResolver(c.Fonts)
	opt.ImageHrefResolver = svg.ImageHrefResolver()
	opt.ResourcesDir = c.Doc.Dir()
	return opt
}

func (c *Ctx) emitter() *svg.Svg { return svg.New(c.Fonts, c.Assets, c.Doc.Dir()) }

// parse is usvg's parse with base (TreeOptions) and a fresh image resolver; nil on error. fontLock
// serialises the parses that reach the font database (text, and images that may hold text), whose caches
// are not safe for concurrent use.
func parse(markup string, base usvg.Options, fontLock *sync.Mutex) *usvg.Tree {
	if fontLock != nil && (strings.Contains(markup, "<text") || strings.Contains(markup, "<image")) {
		fontLock.Lock()
		defer fontLock.Unlock()
	}
	opt := base
	opt.ImageHrefResolver = svg.ImageHrefResolver()
	t, err := usvg.FromStr(markup, &opt)
	if err != nil {
		return nil
	}
	return t
}

// parse parses on the calling goroutine.
func (c *Ctx) parse(markup string) *usvg.Tree { return parse(markup, c.TreeOptions(), nil) }

// Flat is a flattened, laid-out node, used for hit testing, overlays and agent queries.
type Flat struct {
	ID, Name, Type string
	X, Y, W, H     float64
	Depth          int
	// Parent is the parent's id when HasParent (a top-level node has none).
	Parent    string
	HasParent bool
	// Problem is "" or what is wrong with the node.
	Problem string
	// Placeholder is a frame marked `placeholder: true` (the agent is still working on it).
	Placeholder bool
	// Pending has an image fill that is still being generated.
	Pending bool
	// Faded: this node or an ancestor has opacity 0, so nothing of it is painted.
	Faded bool
	// M is the painted transform [a, b, c, d, e, f] (x' = a·x + c·y + e, y' = b·x + d·y + f) from this box's
	// coordinates to the canvas: own and ancestors' rotations and flips. nil is the identity.
	M *[6]float64
	// LocalX, LocalY is the local bounds' offset from the origin (non-zero for groups); Rotation, FlipX and
	// FlipY are the node's own.
	LocalX, LocalY float64
	Rotation       float64
	FlipX, FlipY   bool
	// Radius holds the corner radii [tl, tr, br, bl] of a rectangle or frame (zero for other types).
	Radius [4]float64
	// Clip: the node clips its children to its box (a `clip` frame), so they cannot be hit outside it.
	Clip bool
	// shape is what an ellipse, path, line or stroke-only rectangle paints; nil means its box is the surface.
	shape *Shape
	// sig changes with the node's own props, box, parent and sibling index: then it must repaint.
	sig uint64
	// reach is how far shadows, blurs and strokes paint outside the box; infinite when the node or an
	// ancestor is rotated or flipped.
	reach float64
}

// Layer is one top-level node: its tree and its flattened nodes.
type Layer struct {
	hash uint64
	tree *usvg.Tree
	// backdrops are the doc-space boxes of its background blurs and how far around each its input reaches.
	backdrops []backdrop
	BBox      Rect
	// paint is everything the layer paints, including shadows and blur outside a clipping frame.
	paint Rect
	Flat  []Flat
	// scripted holds a script node: built when finished background runs had finished, so a later finished
	// run rebuilds the layer (the node's JSON, and so hash, did not change).
	scripted bool
	finished uint64
}

// view is the cached raster: pix covers world-device pixels starting at (x0, y0) = doc x zoom.
type view struct {
	pix    []byte
	pw, ph uint32
	x0, y0 int32
	zoom   float64
	bg     [3]uint8
	solo   string
}

// Renderer holds the layers, the cached view and the slides. The zero value is ready to use.
type Renderer struct {
	Layers map[string]*Layer
	Order  []string
	// Pixels is the last frame (RGBA, opaque).
	Pixels []byte
	view   *view
	// dirty holds the doc-space areas changed since the cached view was painted.
	dirty []Rect
	// restack: the stacking order changed, so the cached view is unusable.
	restack bool
	// shared is the variables, themes and components hash of the last sync; a change can restyle anything.
	shared uint64
	// MsPerMP is the cost of the last full paint per megapixel (0 = not measured yet). A replay pins it to
	// pick which path a live zoom frame takes.
	MsPerMP float64
	// Slides are the agent-inserted nodes sliding into place.
	Slides []*Slide
	// Now is the slide clock; nil is time.Now.
	Now func() time.Time
	// Threads is how many bands a paint and how many chunks a sync splits into; 0 is the CPU count
	// clamped to 1..16, as Rust's available_parallelism.
	Threads int
}

func (r *Renderer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Renderer) threads() int {
	if r.Threads > 0 {
		return r.Threads
	}
	return min(max(runtime.NumCPU(), 1), 16)
}

// Dirty is the doc-space areas changed since the cached view was painted (merged at the next Render).
func (r *Renderer) Dirty() []Rect { return r.dirty }

// ancestorsOf appends n and the descendants of n on the way to node id (not id itself); false if absent.
func ancestorsOf(n *resolve.RNode, id string, chain *[]*resolve.RNode) bool {
	*chain = append(*chain, n)
	for _, k := range n.Kids {
		if k.ID == id || ancestorsOf(k, id, chain) {
			return true
		}
	}
	*chain = (*chain)[:len(*chain)-1]
	return false
}

// dimmed reports whether id or one of its ancestors has an opacity below 1, with variables resolved for
// the theme active there; one that does not resolve to a number counts as dimmed.
func dimmed(d *doc.Doc, id string) bool {
	var chain []jsonv.Value
	var path func(nodes []jsonv.Value) bool
	path = func(nodes []jsonv.Value) bool {
		for _, n := range nodes {
			chain = append(chain, n)
			if jsonv.StrIs(jsonv.Get(n, "id"), id) || path(jsonv.Items(jsonv.Get(n, "children"))) {
				return true
			}
			chain = chain[:len(chain)-1]
		}
		return false
	}
	path(d.Children())
	for _, o := range resolve.Along(d.Root, chain, "opacity") {
		if o == nil {
			continue
		}
		if v, ok := resolve.Num(o); !ok || v < 1 {
			return true
		}
	}
	return false
}

func valueString(v jsonv.Value, present bool) string {
	if !present {
		return ""
	}
	return string(jsonv.Marshal(v))
}

func sharedHash(d *doc.Doc, libs []resolve.Lib, fonts uint64) uint64 {
	// A downloaded font changes how text lays out; a downloaded icon or image fills in what was blank.
	var s strings.Builder
	s.WriteString(ftoaU(fonts))
	s.WriteString(valueString(d.Root.Get("variables")))
	s.WriteString(valueString(d.Root.Get("themes")))
	doc.Walk(d.Children(), func(n jsonv.Value) {
		if jsonv.IsTrue(jsonv.Get(n, "reusable")) {
			s.Write(jsonv.Marshal(n))
		}
	})
	for _, l := range libs {
		s.WriteString(l.Alias)
		s.WriteString(ftoaU(uint64(len(jsonv.Items(jsonv.Get(l.Root, "children"))))))
		// Instances resolve a library's variables in its own scope.
		if o := jsonv.Obj(l.Root); o != nil {
			s.WriteString(valueString(o.Get("variables")))
			s.WriteString(valueString(o.Get("themes")))
		}
	}
	s.WriteString(d.Dir())
	return svg.FxHash(s.String())
}

func ftoaU(v uint64) string { return string(jsonv.Int(int64(v))) }

// built is a layer prepared by Sync, with the old layer it replaces.
type built struct {
	id     string
	layer  *Layer
	markup string
	ok     bool
	old    *Layer
}

// Sync rebuilds the layers whose content changed since the last sync and returns their ids.
func (r *Renderer) Sync(cx *Ctx) []string {
	finished := cx.finished()
	shared := sharedHash(cx.Doc, cx.Libs, cx.Fonts.Generation+cx.Assets.Generation)
	reshared := r.shared != shared
	r.shared = shared
	hidden := map[string]bool{}
	for _, s := range r.Slides {
		hidden[s.ID] = true
	}
	if r.Layers == nil {
		r.Layers = map[string]*Layer{}
	}
	var order []string
	keep := map[string]*Layer{}
	var builds []built
	resolver := resolve.New(cx.Doc.Root, cx.Libs)
	for i, node := range cx.Doc.Children() {
		id := jsonv.StrOr(jsonv.Get(node, "id"))
		hash := svg.FxHash(string(jsonv.Marshal(node))) ^ rotl(shared, 17)
		// A node starting or ending its slide rebuilds its layer.
		for h := range hidden {
			if doc.FindIn([]jsonv.Value{node}, h) != nil {
				hash ^= rotl(svg.FxHash(h), 29)
			}
		}
		l, had := r.Layers[id]
		delete(r.Layers, id)
		if had && l.hash == hash && (!l.scripted || l.finished == finished) {
			keep[id] = l
		} else {
			layer, markup, ok := prepareLayer(cx, resolver, i, hash, finished, hidden)
			b := built{id: id, layer: layer, markup: markup, ok: ok}
			if had {
				b.old = l
			}
			builds = append(builds, b)
		}
		order = append(order, id)
	}
	rebuilt := make([]string, len(builds))
	for i, b := range builds {
		rebuilt[i] = b.id
	}
	// Parsing (text shaping included) dominates: do it on goroutines, a chunk of layers each.
	trees := make([]*usvg.Tree, len(builds))
	scans := make([][]backdrop, len(builds))
	chunk := max((len(builds)+r.threads()-1)/r.threads(), 1)
	var fontLock sync.Mutex
	var wg sync.WaitGroup
	opts := cx.TreeOptions()
	for start := 0; start < len(builds); start += chunk {
		end := min(start+chunk, len(builds))
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A parse that panics leaves its layer without a tree, as a failed thread does in Rust.
			for i := start; i < end; i++ {
				func() {
					defer func() { _ = recover() }()
					if builds[i].ok {
						if t := parse(builds[i].markup, opts, &fontLock); t != nil {
							trees[i], scans[i] = t, sceneBackdrops(t)
						}
					}
				}()
			}
		}()
	}
	wg.Wait()
	for i, b := range builds {
		layer := b.layer
		if t := trees[i]; t != nil {
			bb, _ := t.Root().AbsLayerBoundingBox()
			layer.paint = union(layer.paint, Rect{X: float64(bb.X()), Y: float64(bb.Y()), W: float64(bb.Width()), H: float64(bb.Height())})
			layer.tree = t
			layer.backdrops = scans[i]
		}
		switch {
		case b.old != nil && !reshared:
			r.dirty = changedNodes(b.old, layer, r.dirty)
		case b.old != nil:
			r.dirty = append(r.dirty, b.old.paint, layer.paint)
		default:
			r.dirty = append(r.dirty, layer.paint)
		}
		keep[b.id] = layer
	}
	// The layers left are gone. Rust visits them in hash map order; here in their old stacking order.
	for _, id := range r.Order {
		if gone, ok := r.Layers[id]; ok {
			r.dirty = append(r.dirty, gone.paint)
			delete(r.Layers, id)
		}
	}
	for _, gone := range r.Layers {
		r.dirty = append(r.dirty, gone.paint)
	}
	// Same layers in a different stacking order: every overlap may change.
	oldSet, newSet := map[string]bool{}, map[string]bool{}
	for _, id := range r.Order {
		oldSet[id] = true
	}
	for _, id := range order {
		newSet[id] = true
	}
	var a, b []string
	for _, id := range r.Order {
		if newSet[id] {
			a = append(a, id)
		}
	}
	for _, id := range order {
		if oldSet[id] {
			b = append(b, id)
		}
	}
	if !equalStrings(a, b) {
		r.restack = true
	}
	r.Layers = keep
	r.Order = order
	return rebuilt
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Flats is every flattened node, layer by layer in stacking order.
func (r *Renderer) Flats() []*Flat {
	var out []*Flat
	for _, id := range r.Order {
		if l, ok := r.Layers[id]; ok {
			for i := range l.Flat {
				out = append(out, &l.Flat[i])
			}
		}
	}
	return out
}

// LiveNode is a node that needs an animated overlay (see Live).
type LiveNode struct {
	Flat *Flat
	// Clip is the canvas box its clipping ancestors leave visible, when HasClip.
	Clip    Rect
	HasClip bool
	// Cuts are the box and painted corner radii of each clipping ancestor that rounds its corners.
	Cuts []Cut
}

// Cut is a rounded clipping ancestor of a LiveNode.
type Cut struct {
	Box    Rect
	Radius [4]float64
}

// Live is render.rs `live`: the nodes that need animated overlays (placeholder frames, pending images),
// each with the canvas box its clipping ancestors leave visible and the box and painted corner radii of
// each of those ancestors that rounds its corners (a rotated one is only its bounding box). A node clipped
// away entirely, or with opacity 0 itself or in an ancestor, is left out.
func (r *Renderer) Live() []LiveNode {
	// A flipped frame paints its corners mirrored: the radii as they show on the canvas.
	shown := func(f *Flat) [4]float64 {
		a, b, c, d := f.Radius[0], f.Radius[1], f.Radius[2], f.Radius[3]
		m := [6]float64{1, 0, 0, 1, 0, 0}
		if f.M != nil {
			m = *f.M
		}
		if m[0] < 0 {
			a, b, c, d = b, a, d, c
		}
		if m[3] < 0 {
			return [4]float64{d, c, b, a}
		}
		return [4]float64{a, b, c, d}
	}
	var out []LiveNode
	for _, id := range r.Order {
		layer, ok := r.Layers[id]
		if !ok {
			continue
		}
		var path []int
		for i := range layer.Flat {
			f := &layer.Flat[i]
			if len(path) > f.Depth {
				path = path[:f.Depth]
			}
			if (f.Placeholder || f.Pending) && !f.Faded {
				var frames []*Flat
				for _, p := range path {
					if layer.Flat[p].Clip {
						frames = append(frames, &layer.Flat[p])
					}
				}
				ln := LiveNode{Flat: f}
				for j, p := range frames {
					b := canvasBox(p)
					if j == 0 {
						ln.Clip, ln.HasClip = b, true
						continue
					}
					a := ln.Clip
					x0, y0, x1, y1 := fmax(a.X, b.X), fmax(a.Y, b.Y), fmin(a.X+a.W, b.X+b.W), fmin(a.Y+a.H, b.Y+b.H)
					ln.Clip = Rect{X: x0, Y: y0, W: fmax(x1-x0, 0), H: fmax(y1-y0, 0)}
				}
				for _, p := range frames {
					if p.M != nil && (abs(p.M[1]) > 1e-9 || abs(p.M[2]) > 1e-9) {
						continue
					}
					rad := shown(p)
					if rad[0] > 0 || rad[1] > 0 || rad[2] > 0 || rad[3] > 0 {
						ln.Cuts = append(ln.Cuts, Cut{canvasBox(p), rad})
					}
				}
				own := canvasBox(f)
				c := ln.Clip
				if !ln.HasClip || c.W > 0 && c.H > 0 && c.X < own.X+own.W && c.X+c.W > own.X && c.Y < own.Y+own.H && c.Y+c.H > own.Y {
					out = append(out, ln)
				}
			}
			path = append(path, i)
		}
	}
	return out
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// Find is the flattened node id, or nil.
func (r *Renderer) Find(id string) *Flat {
	for _, lid := range r.Order {
		if l, ok := r.Layers[lid]; ok {
			for i := range l.Flat {
				if l.Flat[i].ID == id {
					return &l.Flat[i]
				}
			}
		}
	}
	return nil
}

// Bounds is the union of all top-level bounding boxes; false without layers.
func (r *Renderer) Bounds() (Rect, bool) {
	first := true
	var x0, y0, x1, y1 float64
	for _, id := range r.Order {
		l, ok := r.Layers[id]
		if !ok {
			continue
		}
		b := l.BBox
		if first {
			x0, y0, x1, y1, first = b.X, b.Y, b.X+b.W, b.Y+b.H, false
			continue
		}
		x0, y0, x1, y1 = fmin(x0, b.X), fmin(y0, b.Y), fmax(x1, b.X+b.W), fmax(y1, b.Y+b.H)
	}
	if first {
		return Rect{}, false
	}
	return Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, true
}
