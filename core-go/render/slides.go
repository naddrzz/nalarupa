package render

import (
	"math"
	"slices"
	"sort"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Insert animation: a node the agent inserts starts 50 units low and transparent; the offset decays at
// 20/s and the opacity at 5/s. Layout siblings the insert moved glide from where they were with the same
// offset decay.
const (
	slideOffset = 50.0
	slideDecay  = 20.0
	fadeDecay   = 5.0
	// slideTime: done at offset < 0.5 and opacity > 0.95, ln(20)/5 ≈ 0.6 s (the offset settles at 0.23 s).
	slideTime = 600 * time.Millisecond
	// slideWait: a slide no frame has shown yet (canvas not on screen) is dropped after this.
	slideWait = 2 * time.Second
	// MaxSlides: every slide is snapshotted on each sync (and checked against all the others), so a bulk
	// insert animates only its first few nodes; the rest appear in place.
	MaxSlides = 32
	// ProgressiveMaxSlides is the slide cap when desktop cursor-coupled progressive drawing is enabled.
	ProgressiveMaxSlides = 64
)

type slideClip struct {
	tree   *usvg.Tree
	owners []string
}

type slideCache struct {
	zoom float64
	key  [4]int64 // x0, y0, w, h
	pm   *skia.Pixmap
}

// Slide is a node sliding into place. It is left out of the cached view and painted from its own snapshot,
// offset and faded, over every frame until it settles.
type Slide struct {
	ID    string
	added time.Time
	// start is the first frame that showed it (when started): the clock starts there, not at the insert.
	start   time.Time
	started bool
	// from is the offset at the start (doc units); it decays to zero.
	from [2]float64
	// fade: it fades in (an inserted node) rather than staying opaque (a sibling the insert moved).
	fade bool
	// fresh: inserted since the last sync, so its layout siblings are still to be checked for moves.
	fresh bool
	// rest is how long after start the offset decay restarts from `from`: a fading node a reflow moved
	// glides on from where it shows, while its fade keeps its own clock.
	rest float64
	// delay holds back a progressive slide's reveal (in seconds after start) so burst/nested inserts materialize sequentially.
	delay float64
	// smooth uses a crisp EaseOutQuint glide + sub-pixel compositing for interactive progressive reveals.
	smooth bool
	// cursorHeld keeps this slide unrevealed (or holding beforeTree for edits) until the agent cursor arrives at it.
	cursorHeld   bool
	holdDeadline time.Time
	isEdit       bool
	beforeTree   *usvg.Tree
	beforeClips  []*usvg.Tree
	beforeHash   uint64
	beforeOrigin [2]float64
	hasBeforeOrg bool
	beforeCache  *slideCache
	// tree is the node alone in absolute doc coordinates, without descendants that slide themselves.
	tree *usvg.Tree
	// lin is the linear part of the ancestors' painted matrix: it carries the offset into the parent's frame.
	lin [4]float64
	// ancestors are the sliding ancestors: their offset and fade carry this node along.
	ancestors []string
	// clips are the clip shapes of the clipping ancestors, each with the ids whose slide moves it.
	clips []slideClip
	// over is what its own top-level frame stacks above it, repainted over it.
	over     *usvg.Tree
	treeHash uint64
	cache    *slideCache
}

// NewSlide is a slide of node id inserted at now.
func NewSlide(id string, now time.Time) *Slide {
	return &Slide{ID: id, added: now, from: [2]float64{0, slideOffset}, fade: true, fresh: true, lin: [4]float64{1, 0, 0, 1}}
}

// NewEditSlide creates a cursor-coupled visual edit transition from beforeTree to the updated node id.
func NewEditSlide(id string, now time.Time, beforeTree *usvg.Tree, beforeClips []*usvg.Tree, beforeHash uint64, beforeOrigin [2]float64, hasBeforeOrg bool) *Slide {
	return &Slide{
		ID:           id,
		added:        now,
		from:         [2]float64{0, 0},
		fade:         true,
		fresh:        false,
		smooth:       true,
		isEdit:       true,
		beforeTree:   beforeTree,
		beforeClips:  beforeClips,
		beforeHash:   beforeHash,
		beforeOrigin: beforeOrigin,
		hasBeforeOrg: hasBeforeOrg,
		lin:          [4]float64{1, 0, 0, 1},
	}
}

const (
	smoothSlideDur = 0.18
	editMorphDur   = 0.17
)

// SetDelay sets a pre-reveal stagger delay (seconds) before this slide begins fading and gliding into place.
func (s *Slide) SetDelay(sec float64) {
	if sec > 0 {
		s.delay = sec
	} else {
		s.delay = 0
	}
}

// SetSmooth enables 180ms EaseOutQuint progressive reveal with sub-pixel compositing.
func (s *Slide) SetSmooth(on bool) {
	s.smooth = on
	if on && !s.isEdit && s.fade && s.from == ([2]float64{0, slideOffset}) {
		s.from = [2]float64{0, 10.0}
	}
}

// SetCursorHeld gates this slide's reveal/edit transition until ReleaseCursor is called (or maxHold elapses).
// Any pre-configured stagger delay is preserved for safety-timeout fallbacks and cleared by ReleaseCursor on arrival.
func (s *Slide) SetCursorHeld(now time.Time, maxHold time.Duration) {
	s.cursorHeld = true
	if maxHold > 0 {
		s.holdDeadline = now.Add(maxHold)
	} else {
		s.holdDeadline = time.Time{}
	}
	if s.smooth && !s.isEdit && s.fade {
		s.from = [2]float64{0, 10.0}
	}
}

// ExtendHold extends the safety hold deadline while an active cursor still has this node queued ahead.
func (s *Slide) ExtendHold(deadline time.Time) {
	if s.cursorHeld && deadline.After(s.holdDeadline) {
		s.holdDeadline = deadline
	}
}

// ReleaseCursor triggers this slide's reveal or edit morph immediately at now.
func (s *Slide) ReleaseCursor(now time.Time) {
	if s.cursorHeld {
		s.cursorHeld = false
		s.delay = 0
		s.start = now
		s.started = true
	}
}

// IsHeld reports whether this slide is still waiting for the agent cursor to arrive at now.
func (s *Slide) IsHeld(now time.Time) bool {
	if !s.cursorHeld {
		return false
	}
	if !s.holdDeadline.IsZero() && !now.Before(s.holdDeadline) {
		return false
	}
	return true
}

// since is `now.duration_since(t)`: saturating at zero.
func since(now, t time.Time) time.Duration { return max(now.Sub(t), 0) }

func (s *Slide) elapsed(now time.Time) float64 {
	if !s.started {
		return 0
	}
	return since(now, s.start).Seconds()
}

// State is (offset x, offset y, opacity) at now.
func (s *Slide) State(now time.Time) (float64, float64, float64) {
	if s.IsHeld(now) {
		if s.fade {
			return s.from[0], s.from[1], 0
		}
		return s.from[0], s.from[1], 1
	}
	t := s.elapsed(now)
	if s.delay > 0 {
		if t < s.delay {
			if s.fade {
				return s.from[0], s.from[1], 0
			}
			return s.from[0], s.from[1], 1
		}
		t -= s.delay
	}
	if s.smooth && s.rest == 0 {
		dur := smoothSlideDur
		if s.isEdit {
			dur = editMorphDur
		}
		u := fmin(fmax(t/dur, 0), 1)
		inv := 1 - u
		inv2 := inv * inv
		// EaseOutQuint (1 - (1-u)^5): fast initial snap for instant feedback, silky settle over final pixels.
		ease := 1 - inv2*inv2*inv
		k := 1 - ease
		a := 1.0
		if s.fade {
			a = ease
		}
		return s.from[0] * k, s.from[1] * k, a
	}
	k := math.Exp(-slideDecay * (t - s.rest))
	a := 1.0
	if s.fade {
		a = 1 - math.Exp(-fadeDecay*t)
	}
	return s.from[0] * k, s.from[1] * k, a
}

// BeforeAlpha returns the pre-edit layer weight during an edit transition (1.0 while held, 1-afterAlpha once released).
func (s *Slide) BeforeAlpha(now time.Time) float64 {
	if s.beforeTree == nil {
		return 0
	}
	if s.IsHeld(now) {
		return 1.0
	}
	_, _, afterAlpha := s.State(now)
	if afterAlpha <= 0 {
		return 1.0
	}
	if afterAlpha >= 1 {
		return 0
	}
	return fmax(0, 1.0-afterAlpha)
}

// duration is how long after start the slide is done: its fade, and its offset decayed from `from`,
// however far a reflow moved it, below 0.5.
func (s *Slide) duration() time.Duration {
	if s.smooth && s.rest == 0 {
		dur := smoothSlideDur
		if s.isEdit {
			dur = editMorphDur
		}
		return time.Duration(math.Round((s.delay + dur) * 1e9))
	}
	far := fmax(math.Abs(s.from[0]), math.Abs(s.from[1]))
	settle := s.delay + s.rest + fmax(math.Log(far/0.5), 0)/slideDecay
	minDur := slideTime
	if s.delay > 0 {
		minDur += time.Duration(math.Round(s.delay * 1e9))
	}
	return max(minDur, time.Duration(math.Round(settle*1e9)))
}

// ShiftSiblings is the layout change animation on insert: children of the new node's ancestors that the
// insert moved (auto-layout reflow) glide from where they were. Call it before Sync (the layers still hold
// the old positions); it returns the layouts it made, for RefreshSlides.
func (r *Renderer) ShiftSiblings(cx *Ctx) map[int]*resolve.RNode {
	roots := map[int]*resolve.RNode{}
	var fresh []string
	for _, s := range r.Slides {
		if s.fresh {
			s.fresh = false
			fresh = append(fresh, s.ID)
		}
	}
	now := r.now()
	// Each move is old layout to new, once: several inserts in one sync share siblings.
	moved := map[string]bool{}
	for _, id := range fresh {
		top := cx.Doc.TopIndex(id)
		if top < 0 {
			continue
		}
		// A brand-new top-level node has no old positions: nothing moved.
		tid, ok := jsonv.Str(jsonv.Get(cx.Doc.Children()[top], "id"))
		if !ok {
			continue
		}
		layer, ok := r.Layers[tid]
		if !ok {
			continue
		}
		// Compared by origin, not box: a group's box is the union of its children, so it moves when a child
		// is added beyond it while the group's own content stays put.
		old := map[string][2]float64{}
		for i := range layer.Flat {
			f := &layer.Flat[i]
			old[f.ID] = [2]float64{f.X - f.LocalX, f.Y - f.LocalY}
		}
		root, done := roots[top]
		if !done {
			root = cx.LaidOutLive(top)
			roots[top] = root
		}
		if root == nil {
			continue
		}
		var chain []*resolve.RNode
		if !ancestorsOf(root, id, &chain) {
			continue
		}
		// What each node glides with after the carry below.
		glides := map[string][2]float64{}
		for j, a := range chain {
			for _, k := range a.Kids {
				o, ok := old[k.ID]
				if !ok {
					continue
				}
				dx, dy := o[0]-(k.X-k.BX), o[1]-(k.Y-k.BY)
				// A gliding ancestor's offset is added to its children: the child glides only the part of
				// its move the ancestor does not carry.
				for _, an := range chain[1 : j+1] {
					g, ok := glides[an.ID]
					if ok && slices.ContainsFunc(r.Slides, func(s *Slide) bool { return s.ID == an.ID && !s.fade }) {
						dx, dy = dx-g[0], dy-g[1]
					}
				}
				glides[k.ID] = [2]float64{dx, dy}
				// A glass node's snapshot would hold no backdrop: it jumps.
				if math.Abs(dx) < 0.5 && math.Abs(dy) < 0.5 || svg.HasBackdropBlur(k) || moved[k.ID] {
					continue
				}
				moved[k.ID] = true
				room := len(r.Slides) < MaxSlides
				i := slices.IndexFunc(r.Slides, func(s *Slide) bool { return s.ID == k.ID })
				switch {
				case i >= 0:
					// Already gliding: continue from where it shows now. A fading node keeps its fade clock
					// and restarts only the offset decay.
					s := r.Slides[i]
					cx0, cy0, _ := s.State(now)
					s.from, s.cache = [2]float64{cx0 + dx, cy0 + dy}, nil
					if s.fade {
						s.rest = s.elapsed(now)
					} else {
						s.started = false
					}
				case room:
					s := NewSlide(k.ID, now)
					s.from, s.fade, s.fresh = [2]float64{dx, dy}, false, false
					r.Slides = append(r.Slides, s)
				}
			}
		}
	}
	return roots
}

// holds reports whether node id's document subtree (below it) contains node inner.
func holds(d *doc.Doc, id, inner string) bool {
	n := d.Find(id)
	if n == nil {
		return false
	}
	kids := jsonv.Arr(jsonv.Get(n, "children"))
	return kids != nil && doc.FindIn(kids.Items, inner) != nil
}

// CanSlideInto reports whether an insert into parent may start its own slide: there is room, parent and
// its ancestors are opaque, and no inserted node already sliding holds parent (its snapshot carries the
// new node along).
func (r *Renderer) CanSlideInto(d *doc.Doc, parent string) bool {
	return r.CanSlideIntoWithCap(d, parent, MaxSlides)
}

// CanSlideIntoWithCap is CanSlideInto with a caller-specified slide capacity.
func (r *Renderer) CanSlideIntoWithCap(d *doc.Doc, parent string, capSlides int) bool {
	if len(r.Slides) >= capSlides || dimmed(d, parent) {
		return false
	}
	for _, s := range r.Slides {
		if s.fade && (s.ID == parent || holds(d, s.ID, parent)) {
			return false
		}
	}
	return true
}

// SnapshotNodeTree builds an isolated pre-edit usvg.Tree, static ancestor clip masks, and doc-space origin
// for node id before a script mutates it.
func (r *Renderer) SnapshotNodeTree(cx *Ctx, id string) (*usvg.Tree, []*usvg.Tree, uint64, [2]float64, bool) {
	top := cx.Doc.TopIndex(id)
	if top < 0 {
		return nil, nil, 0, [2]float64{}, false
	}
	root := cx.LaidOutLive(top)
	if root == nil {
		return nil, nil, 0, [2]float64{}, false
	}
	node := root.Find(id)
	if node == nil || svg.Blends(node) {
		return nil, nil, 0, [2]float64{}, false
	}
	var chain []*resolve.RNode
	ancestorsOf(root, id, &chain)
	var m *[6]float64
	alpha := 1.0
	for _, n := range chain {
		if svg.IsTurned(n) {
			c := Compose(m, svg.OwnMatrix(n))
			m = &c
		}
		op, ok := n.F("opacity")
		if !ok {
			op = 1
		}
		alpha *= fmin(op, 1)
	}
	if alpha <= 0 {
		return nil, nil, 0, [2]float64{}, false
	}
	var clips []*usvg.Tree
	var clipHash uint64
	var turned *[6]float64
	for _, a := range chain {
		if svg.IsTurned(a) {
			c := Compose(turned, svg.OwnMatrix(a))
			turned = &c
		}
		if clipMarkup, ok := svg.ClipSVG(a, turned); ok {
			if ct := cx.parse(clipMarkup); ct != nil {
				clips = append(clips, ct)
				clipHash = rotl(clipHash, 13) ^ svg.FxHash(clipMarkup)
			}
		}
	}
	skip := make(map[string]bool, len(r.Slides))
	for _, s := range r.Slides {
		if s.ID != id {
			skip[s.ID] = true
		}
	}
	e := cx.emitter()
	e.Skip = skip
	e.Node(node)
	e.Wrap(m, alpha)
	markup := e.Finish(nil)
	t := cx.parse(markup)
	if t == nil {
		return nil, nil, 0, [2]float64{}, false
	}
	return t, clips, svg.FxHash(markup) ^ clipHash, [2]float64{node.X - node.BX, node.Y - node.BY}, true
}

// SettleCovered settles the slides a node that does not slide stacks above and overlaps: slides paint
// over the finished view, so they would hide it until they end. Latest first, so a settled slide's own
// content counts against the slides below it. True if any settled; the caller syncs again.
func (r *Renderer) SettleCovered() bool {
	sliding := map[string]bool{}
	protected := map[string]bool{}
	for _, s := range r.Slides {
		sliding[s.ID] = true
		if s.cursorHeld || s.isEdit {
			protected[s.ID] = true
		}
	}
	gone := map[string]bool{}
	for _, id := range r.Order {
		layer, ok := r.Layers[id]
		if !ok {
			continue
		}
		flat := layer.Flat
		var mine []int
		for i := range flat {
			if sliding[flat[i].ID] && !protected[flat[i].ID] {
				mine = append(mine, i)
			}
		}
		for len(mine) > 0 {
			i := mine[len(mine)-1]
			mine = mine[:len(mine)-1]
			s := &flat[i]
			// Everything after its subtree paints above it.
			end := len(flat)
			for j := i + 1; j < len(flat); j++ {
				if flat[j].Depth <= s.Depth {
					end = j
					break
				}
			}
			// Its snapshot paints the box and the descendants that overflow it (not those a clip frame
			// cuts, nor ones that slide themselves), inset so boxes that only touch do not count.
			inset := func(b Rect) Rect {
				dx, dy := 0.0, 0.0
				if b.W > 1 {
					dx = 0.5
				}
				if b.H > 1 {
					dy = 0.5
				}
				return Rect{X: b.X + dx, Y: b.Y + dy, W: b.W - 2*dx, H: b.H - 2*dy}
			}
			// Clipping ancestors cut the snapshot: what they cut paints nothing.
			depth := s.Depth
			var cuts []Rect
			for j := i - 1; j >= 0; j-- {
				a := &flat[j]
				up := a.Depth < depth
				if up {
					depth = a.Depth
				}
				if up && a.Clip {
					cuts = append(cuts, canvasBox(a))
				}
			}
			visible := func(b Rect) (Rect, bool) {
				for _, c := range cuts {
					x0, y0 := fmax(b.X, c.X), fmax(b.Y, c.Y)
					x1, y1 := fmin(b.X+b.W, c.X+c.W), fmin(b.Y+b.H, c.Y+c.H)
					if !(x1 >= x0 && y1 >= y0) {
						return Rect{}, false
					}
					b = Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
				}
				return b, true
			}
			var boxes []Rect
			if b, ok := visible(canvasBox(s)); ok {
				boxes = append(boxes, inset(b))
			}
			skip := -1
			if s.Clip {
				skip = s.Depth
			}
			for j := i + 1; j < end; j++ {
				f := &flat[j]
				if skip >= 0 && f.Depth > skip {
					continue
				}
				skip = -1
				if f.Clip || sliding[f.ID] {
					skip = f.Depth
				}
				if !sliding[f.ID] {
					if b, ok := visible(canvasBox(f)); ok {
						boxes = append(boxes, inset(b))
					}
				}
			}
			// Depth of the sliding node whose subtree is being skipped: it paints as a slide, above.
			under := -1
			covered := false
			for j := end; j < len(flat) && !covered; j++ {
				f := &flat[j]
				if under >= 0 && f.Depth > under {
					continue
				}
				under = -1
				if sliding[f.ID] && !gone[f.ID] {
					under = f.Depth
				}
				if under < 0 && f.Type != "group" {
					fb := canvasBox(f)
					for _, b := range boxes {
						if overlaps(b, fb) {
							covered = true
							break
						}
					}
				}
			}
			if covered {
				gone[s.ID] = true
			}
		}
	}
	r.Slides = slices.DeleteFunc(r.Slides, func(s *Slide) bool { return gone[s.ID] })
	return len(gone) > 0
}

// UnslideWith stops the slides of id and the slides-in of its ancestors: their snapshots would carry it.
func (r *Renderer) UnslideWith(d *doc.Doc, id string) {
	r.Slides = slices.DeleteFunc(r.Slides, func(s *Slide) bool { return s.ID == id || s.fade && holds(d, s.ID, id) })
}

// RefreshSlides snapshots sliding nodes that are new or whose layer was just rebuilt (so a node edited
// mid-slide shows its edit). Each snapshot leaves out descendants that slide themselves. A node that
// blends (itself or in its resolved subtree) needs the content beneath, which its isolated snapshot lacks:
// it appears in place instead, and true is returned when any did.
func (r *Renderer) RefreshSlides(cx *Ctx, rebuilt []string, roots map[int]*resolve.RNode) bool {
	if roots == nil {
		roots = map[int]*resolve.RNode{}
	}
	var blended []string
	ids := make([]string, len(r.Slides))
	for i, s := range r.Slides {
		ids[i] = s.ID
	}
	others := func(self string) map[string]bool {
		m := map[string]bool{}
		for _, i := range ids {
			if i != self {
				m[i] = true
			}
		}
		return m
	}
	for _, s := range r.Slides {
		top := cx.Doc.TopIndex(s.ID)
		if top < 0 {
			s.tree = nil
			continue
		}
		topID := jsonv.StrOr(jsonv.Get(cx.Doc.Children()[top], "id"))
		if s.tree != nil && !slices.Contains(rebuilt, topID) {
			continue
		}
		root, done := roots[top]
		if !done {
			root = cx.LaidOutLive(top)
			roots[top] = root
		}
		var node *resolve.RNode
		if root != nil {
			node = root.Find(s.ID)
		}
		if node == nil {
			s.tree = nil
			continue
		}
		if svg.Blends(node) {
			blended = append(blended, s.ID)
			continue
		}
		// The settled layer paints the node inside its ancestors' groups: carry their rotation, flips and
		// opacity.
		var chain []*resolve.RNode
		ancestorsOf(root, s.ID, &chain)
		var m *[6]float64
		alpha := 1.0
		for _, n := range chain {
			if svg.IsTurned(n) {
				c := Compose(m, svg.OwnMatrix(n))
				m = &c
			}
			op, ok := n.F("opacity")
			if !ok {
				op = 1
			}
			alpha *= fmin(op, 1)
		}
		s.lin = [4]float64{1, 0, 0, 1}
		if m != nil {
			s.lin = [4]float64{m[0], m[1], m[2], m[3]}
		}
		// Each clipping ancestor cuts the node where it cuts it in the layer: its shape under the matrix of
		// the ancestors down to it.
		s.clips = nil
		var turned *[6]float64
		for i, a := range chain {
			if svg.IsTurned(a) {
				c := Compose(turned, svg.OwnMatrix(a))
				turned = &c
			}
			if markup, ok := svg.ClipSVG(a, turned); ok {
				if t := cx.parse(markup); t != nil {
					owners := make([]string, i+1)
					for j, n := range chain[:i+1] {
						owners[j] = n.ID
					}
					s.clips = append(s.clips, slideClip{t, owners})
				}
			}
		}
		// An invisible ancestor hides the node, as it does in the layer.
		if alpha <= 0 {
			s.tree = nil
			continue
		}
		if s.isEdit && s.hasBeforeOrg {
			dx := s.beforeOrigin[0] - (node.X - node.BX)
			dy := s.beforeOrigin[1] - (node.Y - node.BY)
			if math.Abs(dx) >= 0.5 || math.Abs(dy) >= 0.5 {
				s.from = [2]float64{dx, dy}
			} else {
				s.from = [2]float64{0, 0}
			}
		}
		e := cx.emitter()
		e.Skip = others(s.ID)
		e.Node(node)
		e.Wrap(m, alpha)
		markup := e.Finish(nil)
		mHash := svg.FxHash(markup)
		if s.tree == nil || s.treeHash != mHash {
			s.tree = cx.parse(markup)
			s.treeHash = mHash
			s.cache = nil
		}
		s.ancestors = nil
		for _, a := range ids {
			if a == s.ID {
				continue
			}
			if an := root.Find(a); an != nil && an.Find(s.ID) != nil {
				s.ancestors = append(s.ancestors, a)
			}
		}
		// Skipped for a top-level node, when nothing follows it or its ancestors, or when no subsequent non-sliding
		// node's painted reach overlaps this slide's trajectory box (avoids O(slides * page) full-frame SVG parses).
		path := append(append([]*resolve.RNode(nil), chain...), node)
		s.over = nil
		hasFollowing := false
		for w := 0; w+1 < len(path); w++ {
			kids := path[w].Kids
			if len(kids) > 0 && kids[len(kids)-1].ID != path[w+1].ID {
				hasFollowing = true
				break
			}
		}
		if hasFollowing && s.tree != nil && r.hasOverlappingFollower(topID, s, ids) {
			e := cx.emitter()
			e.Skip = others(s.ID)
			e.Above(root, s.ID)
			s.over = cx.parse(e.Finish(nil))
		}
	}
	r.Slides = slices.DeleteFunc(r.Slides, func(s *Slide) bool { return slices.Contains(blended, s.ID) })
	return len(blended) > 0
}

// hasOverlappingFollower reports whether any non-sliding node painted after s in layer topID can overlap
// s's full slide trajectory box. When false, s.over would paint zero pixels inside s's cover rect.
func (r *Renderer) hasOverlappingFollower(topID string, s *Slide, slidingIDs []string) bool {
	layer, ok := r.Layers[topID]
	if !ok || s.tree == nil {
		return true
	}
	bb, ok := s.tree.Root().AbsLayerBoundingBox()
	if !ok {
		return false
	}
	span := math.Hypot(s.from[0], s.from[1]) + 2*slideOffset
	for _, ancID := range s.ancestors {
		for _, other := range r.Slides {
			if other.ID == ancID {
				span += math.Hypot(other.from[0], other.from[1])
			}
		}
	}
	traj := Rect{
		X: float64(bb.X()) - span,
		Y: float64(bb.Y()) - span,
		W: float64(bb.Width()) + 2*span,
		H: float64(bb.Height()) + 2*span,
	}
	if s.beforeTree != nil {
		if bb0, ok0 := s.beforeTree.Root().AbsLayerBoundingBox(); ok0 {
			traj = union(traj, Rect{
				X: float64(bb0.X()) - span,
				Y: float64(bb0.Y()) - span,
				W: float64(bb0.Width()) + 2*span,
				H: float64(bb0.Height()) + 2*span,
			})
		}
	}

	sliding := make(map[string]bool, len(slidingIDs))
	for _, id := range slidingIDs {
		sliding[id] = true
	}
	flat := layer.Flat
	idx := -1
	for i := range flat {
		if flat[i].ID == s.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Wait: sliding nodes are hidden when Sync builds layer.Flat! Let's check if flat contains sliding nodes!
		return true
	}
	end := idx + 1
	for end < len(flat) && flat[end].Depth > flat[idx].Depth {
		end++
	}
	skipDepth := -1
	for j := end; j < len(flat); j++ {
		f := &flat[j]
		if skipDepth >= 0 && f.Depth > skipDepth {
			continue
		}
		skipDepth = -1
		if sliding[f.ID] {
			skipDepth = f.Depth
			continue
		}
		if f.Type == "group" {
			continue
		}
		if math.IsInf(f.reach, 0) {
			return true
		}
		cb := canvasBox(f)
		reach := fmax(f.reach, 4)
		fb := Rect{X: cb.X - reach, Y: cb.Y - reach, W: cb.W + 2*reach, H: cb.H + 2*reach}
		if overlaps(traj, fb) {
			return true
		}
	}
	return false
}

// ExpireSlides drops slides that have settled (or were never shown); true if any ended.
func (r *Renderer) ExpireSlides() bool {
	now := r.now()
	before := len(r.Slides)
	r.Slides = slices.DeleteFunc(r.Slides, func(s *Slide) bool {
		if s.IsHeld(now) {
			return false
		}
		if s.cursorHeld {
			s.cursorHeld = false
			startAt := now
			if !s.holdDeadline.IsZero() && s.holdDeadline.Before(now) {
				startAt = s.holdDeadline
			}
			s.start, s.started = startAt, true
		}
		if s.started {
			return since(now, s.start) >= s.duration()
		}
		return since(now, s.added) >= slideWait
	})
	return len(r.Slides) != before
}

// layerOf is the stacking index of the layer holding node id, or -1.
func (r *Renderer) layerOf(id string) int {
	for i, lid := range r.Order {
		if l, ok := r.Layers[lid]; ok {
			for j := range l.Flat {
				if l.Flat[j].ID == id {
					return i
				}
			}
		}
	}
	return -1
}

func applyStaticClipTrees(pm *skia.Pixmap, clips []*usvg.Tree, zoom float64, x0, y0 int32) {
	if pm == nil || len(clips) == 0 {
		return
	}
	tf := skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom), float32(-x0), float32(-y0))
	for _, ct := range clips {
		if ct == nil {
			continue
		}
		shape, ok := skia.NewPixmap(pm.Width(), pm.Height())
		if !ok {
			continue
		}
		resvg.Render(ct, tf, shape.AsMut())
		pm.ApplyMask(skia.MaskFromPixmap(shape.AsRef(), skia.MaskTypeAlpha))
	}
}

// blendEditPixmaps cross-dissolves pre-edit bpm and post-edit pm in an isolated premultiplied RGBA buffer
// before compositing onto out, keeping shared opaque pixels at alpha=255 (zero background bleed) while
// smoothly fading any shrunk or expanded regions.
func blendEditPixmaps(
	out skia.PixmapMut, w, h uint32,
	bpx, bpy int32, bpm *skia.Pixmap,
	px, py int32, pm *skia.Pixmap,
	drawTF skia.Transform, subpixel bool,
	alpha float64,
) (int32, int32, int32, int32, bool) {
	bx1, by1 := bpx+int32(bpm.Width()), bpy+int32(bpm.Height())
	ax0, ay0 := px, py
	ax1, ay1 := px+int32(pm.Width()), py+int32(pm.Height())
	if subpixel {
		ax0--
		ay0--
		ax1++
		ay1++
	}
	ux0 := max(min(bpx, ax0), 0)
	uy0 := max(min(bpy, ay0), 0)
	ux1 := min(max(bx1, ax1), int32(w))
	uy1 := min(max(by1, ay1), int32(h))
	if ux1 <= ux0 || uy1 <= uy0 {
		return 0, 0, 0, 0, false
	}
	uw, uh := uint32(ux1-ux0), uint32(uy1-uy0)
	stage, ok := skia.NewPixmap(uw, uh)
	if !ok {
		return 0, 0, 0, 0, false
	}
	apaint := skia.DefaultPixmapPaint()
	if subpixel {
		apaint.Quality = skia.FilterQualityBilinear
	}
	stage.AsMut().DrawPixmap(px-ux0, py-uy0, pm.AsRef(), &apaint, drawTF, nil)

	p := uint32(math.Round(fmin(fmax(alpha, 0), 1) * 255))
	invP := 255 - p
	sData := stage.DataMut()
	bData := bpm.Data()
	bw, bh := int32(bpm.Width()), int32(bpm.Height())
	sStride := int(uw) * 4
	bStride := int(bw) * 4

	for ry := int32(0); ry < int32(uh); ry++ {
		by := uy0 + ry - bpy
		sRow := int(ry) * sStride
		if by < 0 || by >= bh {
			for rx := int32(0); rx < int32(uw); rx++ {
				so := sRow + int(rx)*4
				sData[so+0] = uint8((p*uint32(sData[so+0]) + 127) / 255)
				sData[so+1] = uint8((p*uint32(sData[so+1]) + 127) / 255)
				sData[so+2] = uint8((p*uint32(sData[so+2]) + 127) / 255)
				sData[so+3] = uint8((p*uint32(sData[so+3]) + 127) / 255)
			}
			continue
		}
		bRow := int(by) * bStride
		for rx := int32(0); rx < int32(uw); rx++ {
			bx := ux0 + rx - bpx
			so := sRow + int(rx)*4
			var br, bg, bb, ba uint32
			if bx >= 0 && bx < bw {
				bo := bRow + int(bx)*4
				br = uint32(bData[bo+0])
				bg = uint32(bData[bo+1])
				bb = uint32(bData[bo+2])
				ba = uint32(bData[bo+3])
			}
			sData[so+0] = uint8((invP*br + p*uint32(sData[so+0]) + 127) / 255)
			sData[so+1] = uint8((invP*bg + p*uint32(sData[so+1]) + 127) / 255)
			sData[so+2] = uint8((invP*bb + p*uint32(sData[so+2]) + 127) / 255)
			sData[so+3] = uint8((invP*ba + p*uint32(sData[so+3]) + 127) / 255)
		}
	}
	dp := skia.DefaultPixmapPaint()
	out.DrawPixmap(ux0, uy0, stage.AsRef(), &dp, skia.Identity(), nil)
	return ux0, uy0, ux1, uy1, true
}
// compositeSlides paints the sliding nodes over the w×h frame in r.Pixels (device origin = camera·zoom).
// With solo only the nodes inside that top-level frame paint; the cached view leaves every sliding node
// out, so skipping them would blank the presented slide.
func (r *Renderer) compositeSlides(w, h uint32, camX, camY, zoom float64, solo string) {
	now := r.now()
	var shown map[string]bool
	if solo != "" {
		shown = map[string]bool{}
		if l, ok := r.Layers[solo]; ok {
			for i := range l.Flat {
				shown[l.Flat[i].ID] = true
			}
		}
	}
	// A slide in another frame (or still held waiting for the agent cursor) keeps waiting.
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
	// Offsets are in the parent's frame: turn them with its rotation and flips.
	type st struct{ dx, dy, alpha float64 }
	states := map[string]st{}
	for _, s := range r.Slides {
		dx, dy, a := s.State(now)
		states[s.ID] = st{float64(s.lin[0]*dx) + float64(s.lin[2]*dy), float64(s.lin[1]*dx) + float64(s.lin[3]*dy), a}
	}
	vx, vy := i32(math.Round(camX*zoom)), i32(math.Round(camY*zoom))
	out, ok := skia.PixmapMutFromBytes(r.Pixels, w, h)
	if !ok {
		return
	}
	// Bottom frame first: a slide's cover pass repaints the frames above it, which hold nothing of the
	// slides in them, so a higher frame's slide must paint after it.
	ranks := make(map[*Slide]int, len(r.Slides))
	for _, s := range r.Slides {
		ranks[s] = r.layerOf(s.ID)
	}
	sort.SliceStable(r.Slides, func(i, j int) bool { return ranks[r.Slides[i]] < ranks[r.Slides[j]] })
	for _, s := range r.Slides {
		if s.tree == nil || shown != nil && !shown[s.ID] {
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
		waiting := s.IsHeld(now) || (s.delay > 0 && s.elapsed(now) < s.delay)
		beforeAlpha := s.BeforeAlpha(now)
		if waiting && beforeAlpha <= 1e-3 {
			continue
		}
		m := fmax(math.Abs(s.from[0]), math.Abs(s.from[1])) + 2*slideOffset
		view := Rect{X: camX - m, Y: camY - m, W: float64(w)/zoom + 2*m, H: float64(h)/zoom + 2*m}

		var coverMinX, coverMinY, coverMaxX, coverMaxY int32
		hasPainted := false
		var bpx, bpy int32
		var readyBPM *skia.Pixmap

		if s.beforeTree != nil && beforeAlpha > 1e-3 {
			bb0, _ := s.beforeTree.Root().AbsLayerBoundingBox()
			b0 := Rect{X: float64(bb0.X()), Y: float64(bb0.Y()), W: float64(bb0.Width()), H: float64(bb0.Height())}
			if overlaps(b0, view) {
				bx0, by0 := i32(math.Floor(fmax(b0.X, view.X)*zoom)), i32(math.Floor(fmax(b0.Y, view.Y)*zoom))
				bx1 := i32(math.Ceil(fmin(b0.X+b0.W, view.X+view.W) * zoom))
				by1 := i32(math.Ceil(fmin(b0.Y+b0.H, view.Y+view.H) * zoom))
				bkey := [4]int64{int64(bx0), int64(by0), int64(max(bx1-bx0, 1)), int64(max(by1-by0, 1))}
				if s.beforeCache == nil || s.beforeCache.zoom != zoom || s.beforeCache.key != bkey {
					if bpm, ok := skia.NewPixmap(uint32(bkey[2]), uint32(bkey[3])); ok {
						resvg.Render(s.beforeTree, skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom), float32(-bx0), float32(-by0)), bpm.AsMut())
						applyStaticClipTrees(bpm, s.beforeClips, zoom, bx0, by0)
						s.beforeCache = &slideCache{zoom, bkey, bpm}
					}
				}
				if s.beforeCache != nil && s.beforeCache.pm != nil {
					readyBPM = s.beforeCache.pm
					bpx, bpy = bx0-vx, by0-vy
				}
			}
		}

		var px, py int32
		var readyPM *skia.Pixmap
		if !waiting && (s.beforeTree == nil || alpha > 1e-3) {
			bb, _ := s.tree.Root().AbsLayerBoundingBox()
			b := Rect{X: float64(bb.X()), Y: float64(bb.Y()), W: float64(bb.Width()), H: float64(bb.Height())}
			if overlaps(b, view) {
				x0, y0 := i32(math.Floor(fmax(b.X, view.X)*zoom)), i32(math.Floor(fmax(b.Y, view.Y)*zoom))
				x1 := i32(math.Ceil(fmin(b.X+b.W, view.X+view.W) * zoom))
				y1 := i32(math.Ceil(fmin(b.Y+b.H, view.Y+view.H) * zoom))
				key := [4]int64{int64(x0), int64(y0), int64(max(x1-x0, 1)), int64(max(y1-y0, 1))}
				if s.cache == nil || s.cache.zoom != zoom || s.cache.key != key {
					pm, ok := skia.NewPixmap(uint32(key[2]), uint32(key[3]))
					if ok {
						resvg.Render(s.tree, skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom), float32(-x0), float32(-y0)), pm.AsMut())
						s.cache = &slideCache{zoom, key, pm}
					}
				}
				if s.cache != nil && s.cache.pm != nil {
					pm := s.cache.pm
					rx, ry := i32(math.Round(dx*zoom)), i32(math.Round(dy*zoom))
					px, py = x0-vx+rx, y0-vy+ry
					if len(s.clips) > 0 {
						cut, _ := skia.PixmapFromVec(append([]byte(nil), pm.Data()...), pm.Size())
						for _, c := range s.clips {
							cdx, cdy := 0.0, 0.0
							for _, o := range c.owners {
								if os, ok := states[o]; ok {
									cdx, cdy = cdx+os.dx, cdy+os.dy
								}
							}
							shape, ok := skia.NewPixmap(cut.Width(), cut.Height())
							if !ok {
								continue
							}
							tf := skia.TransformFromRow(float32(zoom), 0, 0, float32(zoom),
								float32(-x0-rx)+float32(math.Round(cdx*zoom)), float32(-y0-ry)+float32(math.Round(cdy*zoom)))
							resvg.Render(c.tree, tf, shape.AsMut())
							cut.ApplyMask(skia.MaskFromPixmap(shape.AsRef(), skia.MaskTypeAlpha))
						}
						pm = cut
					}
					readyPM = pm
				}
			}
		}

		if readyBPM != nil && readyPM != nil && alpha > 1e-3 && alpha < 0.999 {
			if ux0, uy0, ux1, uy1, ok := blendEditPixmaps(out, w, h, bpx, bpy, readyBPM, px, py, readyPM, skia.Identity(), false, alpha); ok {
				coverMinX, coverMinY, coverMaxX, coverMaxY = ux0, uy0, ux1, uy1
				hasPainted = true
			}
		} else {
			if readyBPM != nil && (readyPM == nil || alpha <= 1e-3) {
				bpaint := skia.DefaultPixmapPaint()
				bpaint.Opacity = float32(fmin(fmax(beforeAlpha, 0), 1))
				out.DrawPixmap(bpx, bpy, readyBPM.AsRef(), &bpaint, skia.Identity(), nil)
				coverMinX, coverMinY = bpx, bpy
				coverMaxX, coverMaxY = bpx+int32(readyBPM.Width()), bpy+int32(readyBPM.Height())
				hasPainted = true
			}
			if readyPM != nil {
				paint := skia.DefaultPixmapPaint()
				paint.Opacity = float32(fmin(fmax(alpha, 0), 1))
				out.DrawPixmap(px, py, readyPM.AsRef(), &paint, skia.Identity(), nil)
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
		if own := r.layerOf(s.ID); own >= 0 {
			above = r.Order[own+1:]
		}
		cx0, cy0, cx1, cy1 := max(coverMinX, 0), max(coverMinY, 0), min(coverMaxX, int32(w)), min(coverMaxY, int32(h))
		if cx1 <= cx0 || cy1 <= cy0 {
			continue
		}
		cover := Rect{X: float64(cx0+vx) / zoom, Y: float64(cy0+vy) / zoom, W: float64(cx1-cx0) / zoom, H: float64(cy1-cy0) / zoom}
		var trees []*usvg.Tree
		if s.over != nil {
			trees = append(trees, s.over)
		}
		for _, id := range above {
			if solo != "" && solo != id {
				continue
			}
			if l, ok := r.Layers[id]; ok && overlaps(l.paint, cover) && l.tree != nil {
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
}
