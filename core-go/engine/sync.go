package engine

import (
	"slices"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// shown is the document the canvas shows: the live preview while streams run (not while a script executes),
// else the document.
func (e *Engine) shown() *doc.Doc {
	if e.Preview != nil && !e.Executing {
		return e.Preview
	}
	return e.Doc
}

func (e *Engine) now() time.Time {
	if e.Renderer.Now != nil {
		return e.Renderer.Now()
	}
	return time.Now()
}

// NeedsSync reports whether the cached render layers are behind Version (must be called under Engine.Lock).
func (e *Engine) NeedsSync() bool {
	return e.synced != e.Version
}

// Sync brings the cached render layers up to date with the document.
func (e *Engine) Sync() {
	if e.synced == e.Version {
		return
	}
	e.unslideGlass()
	cx := &render.Ctx{Doc: e.shown(), Libs: e.Libs, Fonts: e.Fonts, Assets: e.Assets}
	roots := e.Renderer.ShiftSiblings(cx)
	rebuilt := e.Renderer.Sync(cx)
	dropped := false
	if len(e.Renderer.Slides) > 0 {
		// The settled slides were left out of the layers: sync again with them shown.
		if e.Renderer.SettleCovered() {
			e.synced = 0
			e.Sync()
			return
		}
		// A slide dropped for a blend mode goes back into the cached view at the next sync.
		dropped = e.Renderer.RefreshSlides(cx, rebuilt, roots)
	}
	if dropped {
		e.synced = 0
	} else {
		e.synced = e.Version
	}
	e.hasSlidesAtomic.Store(len(e.Renderer.Slides) > 0)
}

// SlideIn lets an agent Insert below the top level slide into place.
func (e *Engine) SlideIn(id string) {
	// Sibling glides start from these slides too, so this one check stills both.
	if e.ReduceMotion {
		return
	}
	if e.ProgressiveSlides {
		if n := e.Doc.Find(id); n != nil {
			if kids := progressiveChildTargets(n); len(kids) >= 2 {
				e.slid[id] = true
				for _, cid := range kids {
					e.slideSingle(cid)
				}
				return
			}
		}
	}
	e.slideSingle(id)
}

const (
	progressiveHoldTimeout     = 620 * time.Millisecond
	progressiveLiveHoldTimeout = 750 * time.Millisecond
)

func (e *Engine) progressiveHoldDur() time.Duration {
	if e.Agent.Running || len(e.Streams) > 0 {
		return progressiveLiveHoldTimeout
	}
	return progressiveHoldTimeout
}

// ShownDoc returns the currently displayed document (Preview while streaming, else Doc). Caller holds Engine.Lock.
func (e *Engine) ShownDoc() *doc.Doc { return e.shown() }

// ActiveSlideIDs returns the set of node IDs currently sliding or gated in Renderer.Slides. Caller holds Engine.Lock.
func (e *Engine) ActiveSlideIDs() map[string]bool {
	if len(e.Renderer.Slides) == 0 {
		return nil
	}
	m := make(map[string]bool, len(e.Renderer.Slides))
	for _, s := range e.Renderer.Slides {
		m[s.ID] = true
	}
	return m
}

func rootTargetID(path string) string {
	if idx := strings.IndexByte(path, '/'); idx >= 0 {
		return path[:idx]
	}
	return path
}

func docHolds(d *doc.Doc, id, inner string) bool {
	if d == nil || id == "" || inner == "" {
		return false
	}
	n := d.Find(id)
	if n == nil {
		return false
	}
	kids := jsonv.Arr(jsonv.Get(n, "children"))
	return kids != nil && doc.FindIn(kids.Items, inner) != nil
}

func (e *Engine) slideSingle(id string) *render.Slide {
	// A slide is snapshotted alone, so a background blur in it would blur nothing until it settles: such a node
	// appears in place, and so does a sliding ancestor that now holds it.
	if e.HoldsGlass(id) {
		e.slid[id] = true
		e.Renderer.UnslideWith(e.Doc, id)
		return nil
	}
	// Past the cap nothing more slides, so skip the parent lookup (a linear search per insert).
	capSlides := render.MaxSlides
	if e.ProgressiveSlides {
		capSlides = render.ProgressiveMaxSlides
	}
	if len(e.Renderer.Slides) >= capSlides {
		e.slid[id] = true
		return nil
	}
	room := false
	isTopPlaceholder := false
	if p, _, ok := e.Doc.ParentOf(id); ok {
		room = e.Renderer.CanSlideIntoWithCap(e.Doc, p, capSlides)
		if p == doc.Root {
			if n := e.Doc.Find(id); n != nil && jsonv.IsTrue(jsonv.Get(n, "placeholder")) {
				isTopPlaceholder = true
			}
		}
	}
	fresh := !e.slid[id]
	e.slid[id] = true
	if fresh && room {
		now := e.now()
		sl := render.NewSlide(id, now)
		if e.ProgressiveSlides {
			sl.SetSmooth(true)
			if !isTopPlaceholder {
				sl.SetCursorHeld(now, e.progressiveHoldDur())
			}
		}
		e.Renderer.Slides = append(e.Renderer.Slides, sl)
		e.hasSlidesAtomic.Store(true)
		return sl
	}
	return nil
}

// BeforeModify snapshots path's visual appearance right before Update, Replace, or Move mutates it,
// so the canvas holds the old state until the agent cursor arrives at the edited node.
func (e *Engine) BeforeModify(path string) {
	e.pendingModifyID = ""
	e.pendingModifyTree = nil
	e.pendingModifyClips = nil
	e.pendingModifyHash = 0
	e.pendingModifyHasO = false
	if !e.ProgressiveSlides || e.ReduceMotion || e.Doc == nil {
		return
	}
	targetID := rootTargetID(path)
	if targetID == "" || targetID == doc.Root || (e.activeStream == nil && e.commitSkipEdits[targetID]) {
		return
	}
	n := e.Doc.Find(targetID)
	if n == nil || jsonv.IsTrue(jsonv.Get(n, "reusable")) {
		return
	}
	p, _, ok := e.Doc.ParentOf(targetID)
	if !ok {
		return
	}
	if p == doc.Root && len(jsonv.Items(jsonv.Get(n, "children"))) > 0 {
		return
	}
	for _, s := range e.Renderer.Slides {
		if s.ID == targetID || docHolds(e.Doc, s.ID, targetID) {
			return
		}
	}
	if e.HoldsGlass(targetID) {
		return
	}
	cx := &render.Ctx{Doc: e.Doc, Libs: e.Libs, Fonts: e.Fonts, Assets: e.Assets}
	if tree, clips, hash, org, ok := e.Renderer.SnapshotNodeTree(cx, targetID); ok && tree != nil {
		e.pendingModifyID = targetID
		e.pendingModifyTree = tree
		e.pendingModifyClips = clips
		e.pendingModifyHash = hash
		e.pendingModifyOrg = org
		e.pendingModifyHasO = true
	}
}

// AfterModify registers a cursor-gated edit transition from BeforeModify's pre-edit snapshot to newID.
func (e *Engine) AfterModify(oldPath, newID string) {
	tree := e.pendingModifyTree
	clips := e.pendingModifyClips
	hash := e.pendingModifyHash
	org := e.pendingModifyOrg
	hasO := e.pendingModifyHasO
	prevID := e.pendingModifyID
	e.pendingModifyID = ""
	e.pendingModifyTree = nil
	e.pendingModifyClips = nil
	e.pendingModifyHash = 0
	e.pendingModifyHasO = false

	if !e.ProgressiveSlides || e.ReduceMotion || e.Doc == nil {
		return
	}
	if oldPath == "" {
		e.SlideIn(newID)
		return
	}
	oldTarget := rootTargetID(oldPath)
	targetID := rootTargetID(newID)
	if prevID == "" || prevID != oldTarget || tree == nil || targetID == "" {
		return
	}
	if e.HoldsGlass(targetID) || len(e.Renderer.Slides) >= render.ProgressiveMaxSlides {
		return
	}
	if oldTarget != targetID {
		e.Renderer.Slides = slices.DeleteFunc(e.Renderer.Slides, func(s *render.Slide) bool { return s.ID == oldTarget })
	}
	if st := e.activeStream; st != nil {
		if st.Edited == nil {
			st.Edited = map[string]bool{}
		}
		st.Edited[targetID] = true
	}
	now := e.now()
	sl := render.NewEditSlide(targetID, now, tree, clips, hash, org, hasO)
	sl.SetCursorHeld(now, e.progressiveHoldDur())
	e.Renderer.Slides = append(e.Renderer.Slides, sl)
	e.hasSlidesAtomic.Store(true)
}

// QueueCursorUpdate enqueues cursor-arrived slide releases and ahead-in-queue hold extensions without taking Engine.Lock.
func (e *Engine) QueueCursorUpdate(releaseIDs, holdIDs []string) {
	if len(releaseIDs) == 0 && len(holdIDs) == 0 {
		return
	}
	e.cursorMu.Lock()
	if len(releaseIDs) > 0 {
		e.pendingReleaseID = append(e.pendingReleaseID, releaseIDs...)
	}
	if len(holdIDs) > 0 {
		e.pendingHoldID = append(e.pendingHoldID, holdIDs...)
	}
	e.cursorMu.Unlock()
	if len(releaseIDs) > 0 {
		e.slideReleaseSeq.Add(1)
	}
}

func (e *Engine) drainCursorUpdatesLocked() {
	e.cursorMu.Lock()
	if len(e.pendingReleaseID) == 0 && len(e.pendingHoldID) == 0 {
		e.cursorMu.Unlock()
		return
	}
	rel := e.pendingReleaseID
	hold := e.pendingHoldID
	e.pendingReleaseID = nil
	e.pendingHoldID = nil
	e.cursorMu.Unlock()

	if len(e.Renderer.Slides) == 0 {
		return
	}
	now := e.now()
	d := e.shown()
	if len(hold) > 0 {
		deadline := now.Add(e.progressiveHoldDur())
		for _, hid := range hold {
			if hid == "" {
				continue
			}
			for _, s := range e.Renderer.Slides {
				// Never extend a parent card slide's hold because of inner child IDs inside that card;
				// only extend distinct sibling/future slides whose hold is still live at now.
				if (s.ID == hid || docHolds(d, hid, s.ID)) && s.IsHeld(now) {
					s.ExtendHold(deadline)
				}
			}
		}
	}
	for _, rid := range rel {
		if rid == "" {
			continue
		}
		if exact, ok := strings.CutPrefix(rid, "@exact:"); ok {
			for _, s := range e.Renderer.Slides {
				if s.ID == exact || docHolds(d, s.ID, exact) {
					s.ReleaseCursor(now)
				}
			}
			continue
		}
		isTopRoot := false
		if d != nil {
			if p, _, ok := d.ParentOf(rid); ok && p == doc.Root {
				isTopRoot = true
			}
		}
		for _, s := range e.Renderer.Slides {
			if s.ID == rid || docHolds(d, s.ID, rid) || (!isTopRoot && docHolds(d, rid, s.ID)) {
				s.ReleaseCursor(now)
			}
		}
	}
}

// ExpandProgressiveRootSlides schedules staggered child slides when an Insert into document carries nested children
// (e.g. a monolithic screen literal), so the screen ground appears first and its sections/cards build progressively.
func (e *Engine) ExpandProgressiveRootSlides(d *doc.Doc, ids []string) {
	if !e.ProgressiveSlides || e.ReduceMotion || d == nil {
		return
	}
	prev := e.Doc
	e.Doc = d
	defer func() { e.Doc = prev }()

	for _, id := range ids {
		p, _, ok := d.ParentOf(id)
		if !ok || p != doc.Root {
			continue
		}
		n := d.Find(id)
		if n == nil || jsonv.IsTrue(jsonv.Get(n, "reusable")) {
			continue
		}
		targets := progressiveScreenTargets(n)
		for idx, tid := range targets {
			if sl := e.slideSingle(tid); sl != nil {
				sl.SetDelay(min(float64(idx)*0.03, 0.24))
			}
		}
	}
}

// progressiveChildTargets decomposes a transparent layout wrapper (like a horizontal CardRow with 4 cards)
// into its child elements so cards materialize one-by-one rather than as a single rigid block.
func progressiveChildTargets(n jsonv.Value) []string {
	if !jsonv.StrIs(jsonv.Get(n, "type"), "frame") && !jsonv.StrIs(jsonv.Get(n, "type"), "group") {
		return nil
	}
	// If the container paints its own opaque card surface/border/shadow, keep it intact as one cohesive unit.
	if jsonv.Get(n, "fill") != nil || jsonv.Get(n, "stroke") != nil || jsonv.Get(n, "effect") != nil {
		return nil
	}
	items := jsonv.Items(jsonv.Get(n, "children"))
	if len(items) < 2 {
		return nil
	}
	var out []string
	for _, ch := range items {
		cid := jsonv.StrOr(jsonv.Get(ch, "id"))
		if cid == "" {
			continue
		}
		if sub := progressiveChildTargets(ch); len(sub) >= 2 {
			out = append(out, sub...)
		} else {
			out = append(out, cid)
		}
		if len(out) >= render.ProgressiveMaxSlides {
			break
		}
	}
	return out
}

// progressiveScreenTargets collects ordered visual sections/cards inside a top-level screen frame.
func progressiveScreenTargets(screen jsonv.Value) []string {
	items := jsonv.Items(jsonv.Get(screen, "children"))
	if len(items) == 0 {
		return nil
	}
	var out []string
	for _, ch := range items {
		cid := jsonv.StrOr(jsonv.Get(ch, "id"))
		if cid == "" {
			continue
		}
		if sub := progressiveChildTargets(ch); len(sub) >= 2 {
			out = append(out, sub...)
		} else {
			out = append(out, cid)
		}
		if len(out) >= render.ProgressiveMaxSlides {
			break
		}
	}
	return out
}

// ForgetGoneSlides: a rolled-back script took its inserted nodes with it, so their slides have nothing to
// paint and no clock; they would only end after the slide wait while `animating` stays true.
func (e *Engine) ForgetGoneSlides() {
	live := map[string]bool{}
	for _, id := range e.Doc.AllIDs() {
		live[id] = true
	}
	if e.Preview != nil {
		for _, id := range e.Preview.AllIDs() {
			live[id] = true
		}
	}
	e.Renderer.Slides = slices.DeleteFunc(e.Renderer.Slides, func(s *render.Slide) bool { return !live[s.ID] })
	e.hasSlidesAtomic.Store(len(e.Renderer.Slides) > 0)
	for id := range e.slid {
		if !live[id] {
			delete(e.slid, id)
		}
	}
}

// HoldsGlass reports that node id shows a background blur, a component instance's content included.
func (e *Engine) HoldsGlass(id string) bool {
	var res *resolve.Resolver
	return e.holdsGlassWith(e.Doc, id, &res)
}

// holdsGlassWith is HoldsGlass that builds the document-wide resolver at most once across calls sharing res.
func (e *Engine) holdsGlassWith(d *doc.Doc, id string, res **resolve.Resolver) bool {
	n := d.Find(id)
	if n == nil {
		return false
	}
	if svg.DocHasBackdropBlur(n) {
		return true
	}
	refs := false
	doc.Walk([]jsonv.Value{n}, func(k jsonv.Value) { refs = refs || jsonv.StrIs(jsonv.Get(k, "type"), "ref") })
	if !refs {
		return false
	}
	if *res == nil {
		*res = resolve.New(d.Root, e.Libs)
	}
	r := (*res).Resolve(n)
	return r != nil && svg.HasBackdropBlur(r)
}

// unslideGlass stops every slide whose node now holds a background blur (an update, copy, move, an instance
// whose component gained one, an inspector change): its snapshot would show the backdrop sharp. Sync runs
// it, so every way of changing the document is covered.
func (e *Engine) unslideGlass() {
	if len(e.Renderer.Slides) == 0 {
		return
	}
	var res *resolve.Resolver
	d := e.shown()
	var glass []string
	for _, s := range e.Renderer.Slides {
		if e.holdsGlassWith(d, s.ID, &res) {
			glass = append(glass, s.ID)
		}
	}
	e.Renderer.Slides = slices.DeleteFunc(e.Renderer.Slides, func(s *render.Slide) bool { return slices.Contains(glass, s.ID) })
	e.hasSlidesAtomic.Store(len(e.Renderer.Slides) > 0)
}

// ExpireSlides: settled slides go back into the cached view at the next sync (the document is unchanged), and
// so does a script node whose background run finished.
func (e *Engine) ExpireSlides() {
	e.drainCursorUpdatesLocked()
	scripts := script.Finished()
	// As Rust's `expired || replace(seen) != scripts`: the count is only taken when no slide expired.
	if e.Renderer.ExpireSlides() {
		e.synced = 0
		e.hasSlidesAtomic.Store(len(e.Renderer.Slides) > 0)
		return
	}
	e.hasSlidesAtomic.Store(len(e.Renderer.Slides) > 0)
	if seen := e.scriptsSeen; seen != scripts {
		e.scriptsSeen = scripts
		e.synced = 0
	}
}

// Render brings the layers up to date and paints the viewport (camera in document units) as RGBA; `nr_render`.
func (e *Engine) Render(w, h uint32, camX, camY, zoom float64, bg [3]uint8, solo string, sharp bool) []byte {
	e.ExpireSlides()
	e.Sync()
	return e.Renderer.Render(w, h, camX, camY, zoom, bg, solo, sharp)
}

// SnapshotForRender syncs layers under the engine lock and returns an immutable SceneSnapshot so pixel
// rasterization can execute off-lock on a dedicated render worker goroutine without stalling UI/poll/agent threads.
func (e *Engine) SnapshotForRender(solo string) render.SceneSnapshot {
	var snap render.SceneSnapshot
	e.Locked(func() {
		e.ExpireSlides()
		e.Sync()
		snap = e.Renderer.Snapshot(e.Version, e.now(), solo, len(e.Streams) > 0)
	})
	return snap
}

// TrySnapshotForRender captures an immutable SceneSnapshot without blocking when a slow script or agent holds Engine.Lock.
func (e *Engine) TrySnapshotForRender(solo string) (render.SceneSnapshot, bool) {
	if !e.TryLock() {
		return render.SceneSnapshot{}, false
	}
	defer e.Unlock()
	e.ExpireSlides()
	e.Sync()
	snap := e.Renderer.Snapshot(e.Version, e.now(), solo, len(e.Streams) > 0)
	return snap, true
}
