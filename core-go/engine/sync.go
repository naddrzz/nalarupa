package engine

import (
	"slices"
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
}

// SlideIn lets an agent Insert below the top level slide into place.
func (e *Engine) SlideIn(id string) {
	// Sibling glides start from these slides too, so this one check stills both.
	if e.ReduceMotion {
		return
	}
	// A slide is snapshotted alone, so a background blur in it would blur nothing until it settles: such a node
	// appears in place, and so does a sliding ancestor that now holds it.
	if e.HoldsGlass(id) {
		e.slid[id] = true
		e.Renderer.UnslideWith(e.Doc, id)
		return
	}
	// Past the cap nothing more slides, so skip the parent lookup (a linear search per insert).
	if len(e.Renderer.Slides) >= render.MaxSlides {
		e.slid[id] = true
		return
	}
	room := false
	if p, _, ok := e.Doc.ParentOf(id); ok {
		room = e.Renderer.CanSlideInto(e.Doc, p)
	}
	fresh := !e.slid[id]
	e.slid[id] = true
	if fresh && room {
		e.Renderer.Slides = append(e.Renderer.Slides, render.NewSlide(id, e.now()))
	}
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
}

// ExpireSlides: settled slides go back into the cached view at the next sync (the document is unchanged), and
// so does a script node whose background run finished.
func (e *Engine) ExpireSlides() {
	scripts := script.Finished()
	// As Rust's `expired || replace(seen) != scripts`: the count is only taken when no slide expired.
	if e.Renderer.ExpireSlides() {
		e.synced = 0
		return
	}
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
