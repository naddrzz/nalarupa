package engine

import (
	"slices"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/history"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// orNoop is the patch, or the one that changes nothing (Rust's `unwrap_or_default`).
func orNoop(p history.Patch) history.Patch {
	if p == nil {
		return history.Noop()
	}
	return p
}

// applyTo applies p to the document root *root and returns the patch that undoes it.
func applyTo(p history.Patch, root **jsonv.Object) history.Patch {
	var v jsonv.Value = *root
	inv := history.Apply(p, &v)
	if o, ok := v.(*jsonv.Object); ok {
		*root = o
	}
	return inv
}

func cloneRoot(o *jsonv.Object) *jsonv.Object { return jsonv.Clone(o).(*jsonv.Object) }

// Commit records before as an undo step and marks the document changed. A commit that lands in the middle of
// a drag (an agent's `execute`) is its own step taken from the pre-drag document, and the drag's snapshot takes
// the change along, so the drag undoes without it.
func (e *Engine) Commit(before *jsonv.Object) {
	step := history.Diff(e.Doc.Root, before)
	if step == nil {
		return
	}
	if start := e.DragBefore; start != nil {
		// Undoing this step starts from the drag's snapshot with the change in it, so the patch is relative to that.
		merged, _ := merge3(before, e.Doc.Root, start).(*jsonv.Object)
		if merged == nil {
			merged = jsonv.NewObject()
		}
		step = orNoop(history.Diff(merged, start))
		e.DragBefore = merged
	}
	e.hasCoalesce = false
	e.undo = append(e.undo, step)
	if len(e.undo) > undoLimit {
		e.undo = slices.Delete(e.undo, 0, 1)
	}
	e.redo = nil
	e.Touch()
}

// EndDrag commits a drag (or batch) that started from before. If it ended where it started, no undo step is
// added and a document that was clean when it began stays clean.
func (e *Engine) EndDrag(before *jsonv.Object, wasClean bool) {
	if history.Same(before, e.Doc.Root) {
		e.KeepClean(wasClean)
	} else {
		e.Commit(before)
	}
}

// redraw: something the canvas shows changed, not the document; a document that was clean stays clean.
func (e *Engine) redraw(clean bool) {
	e.Version++
	e.KeepClean(clean)
}

// Touch marks the document changed and drops selected ids that no longer exist (instance paths stay).
func (e *Engine) Touch() {
	e.Version++
	e.Selection = slices.DeleteFunc(e.Selection, func(id string) bool {
		return !strings.Contains(id, "/") && e.Doc.Find(id) == nil
	})
	// Phase 8d: in-flight agent previews are layered on the latest document here.
}

// Undo steps back. It is ignored during a drag: DragEnd would otherwise push the pre-drag snapshot as a new
// step over the swapped document.
func (e *Engine) Undo() bool {
	if e.DragBefore != nil || len(e.undo) == 0 {
		return false
	}
	step := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.hasCoalesce = false
	unflagged, settle := e.unflagged()
	e.redo = append(e.redo, applyTo(step, &e.Doc.Root))
	if settle {
		e.settleGenerating(unflagged)
	}
	e.Touch()
	e.cleanIfSaved()
	return true
}

// Redo steps forward; ignored during a drag.
func (e *Engine) Redo() bool {
	if e.DragBefore != nil || len(e.redo) == 0 {
		return false
	}
	step := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.hasCoalesce = false
	unflagged, settle := e.unflagged()
	e.undo = append(e.undo, applyTo(step, &e.Doc.Root))
	if settle {
		e.settleGenerating(unflagged)
	}
	e.Touch()
	e.cleanIfSaved()
	return true
}

// unflagged is, before an undo or redo with no agent running, the nodes not flagged `placeholder` (see
// settleGenerating); false while an agent runs.
func (e *Engine) unflagged() (map[string]bool, bool) {
	if e.Agent.Running {
		return nil, false
	}
	ids := map[string]bool{}
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok && !jsonv.IsTrue(jsonv.Get(n, "placeholder")) {
			ids[id] = true
		}
	})
	return ids, true
}

// settleGenerating: history holds the `placeholder: true` an agent set while it built a design; with no agent
// running, an undo or redo must not bring "Generating" back onto a finished design. Only a flag the step
// itself put back on an existing node is cleared (out of history too), so a frame an external agent is still
// building, or an AI Pen draws into, keeps its flag.
func (e *Engine) settleGenerating(unflagged map[string]bool) {
	var back []string
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok && jsonv.IsTrue(jsonv.Get(n, "placeholder")) {
			if unflagged[id] && !slices.Contains(e.Agent.Pens, id) {
				back = append(back, id)
			}
		}
	})
	off := jsonv.NewObject()
	off.Set("placeholder", false)
	for _, id := range back {
		e.ApplyGenerated(id, off, true)
	}
}

// EditOrDrag applies f as one undo step, or live during a drag (DragEnd commits it once).
func (e *Engine) EditOrDrag(f func(*doc.Doc) error) error {
	if e.DragBefore == nil {
		return e.Edit(f)
	}
	if err := f(e.Doc); err != nil {
		return err
	}
	e.Touch()
	return nil
}

// EditCoalesced is Edit, but consecutive edits with the same key share one undo step (one inspector field
// focus or scrub gesture is one step).
func (e *Engine) EditCoalesced(key string, f func(*doc.Doc) error) error {
	if e.DragBefore != nil {
		return e.EditOrDrag(f)
	}
	merge := e.hasCoalesce && e.coalesce == key && len(e.undo) > 0
	before := cloneRoot(e.Doc.Root)
	if err := f(e.Doc); err != nil {
		e.Doc.Root = before
		return err
	}
	if !history.Same(before, e.Doc.Root) {
		if merge {
			// The step turned before into the group's first document; now it must start from the edited one.
			var first jsonv.Value = before
			history.Apply(e.undo[len(e.undo)-1], &first)
			e.undo[len(e.undo)-1] = orNoop(history.Diff(e.Doc.Root, first))
			e.redo = nil
			e.Touch()
		} else {
			e.Commit(before)
		}
		e.coalesce, e.hasCoalesce = key, true
	}
	return nil
}

// Batch runs several edits (made with EditOrDrag) as one undo step; all or nothing.
func (e *Engine) Batch(f func(*Engine) error) error {
	if e.DragBefore != nil {
		return f(e)
	}
	before := cloneRoot(e.Doc.Root)
	wasClean := !e.Dirty()
	e.DragBefore = cloneRoot(before)
	err := f(e)
	e.DragBefore = nil
	if err == nil {
		e.EndDrag(before, wasClean)
	} else {
		e.Doc.Root = before
		e.Touch()
		e.KeepClean(wasClean)
	}
	return err
}

// KeepClean: after a rolled-back edit the document is as it was, so if it was clean it stays clean (a
// document switch is refused while it is dirty), however far the version moved.
func (e *Engine) KeepClean(wasClean bool) {
	if wasClean {
		e.Saved = e.Version
	}
}

// cleanIfSaved: an undo or redo that lands on the saved document leaves it clean, whatever the version says.
func (e *Engine) cleanIfSaved() {
	e.KeepClean(history.Same(e.Doc.Root, e.savedRoot))
}

// markSaved: the document as it is now is the saved one. An open coalesced edit ends here, so the next edit is
// its own undo step and undo can land on the saved document.
func (e *Engine) markSaved() {
	e.Saved = e.Version
	e.hasCoalesce = false
	e.savedRoot = cloneRoot(e.Doc.Root)
}

// Edit applies a document mutation as one undoable step; on error the document is left as it was.
func (e *Engine) Edit(f func(*doc.Doc) error) error {
	before := cloneRoot(e.Doc.Root)
	if err := f(e.Doc); err != nil {
		e.Doc.Root = before
		return err
	}
	e.Commit(before)
	return nil
}

func (e *Engine) resetHistory() {
	e.undo, e.redo = nil, nil
	e.Selection = nil
	e.Renderer.Slides = nil
	e.slid = map[string]bool{}
	e.Script = ScriptState{}
	e.Touch()
	e.markSaved()
}

// merge3 is ours with the changes that turned base into theirs applied; where both changed the same value,
// theirs wins. Lists of nodes (`id`) merge per node, the rest as a whole. The result shares nothing with its
// arguments.
func merge3(base, theirs, ours jsonv.Value) jsonv.Value {
	if jsonv.Equal(theirs, base) {
		return jsonv.Clone(ours)
	}
	if jsonv.Equal(ours, base) {
		return jsonv.Clone(theirs)
	}
	if b, t, o := jsonv.Obj(base), jsonv.Obj(theirs), jsonv.Obj(ours); b != nil && t != nil && o != nil {
		out := jsonv.NewObject()
		o.Each(func(k string, ov jsonv.Value) {
			bv, inBase := b.Get(k)
			tv, inTheirs := t.Get(k)
			switch {
			case inBase && inTheirs:
				out.Set(k, merge3(bv, tv, ov))
			case !inBase && inTheirs:
				out.Set(k, merge3(nil, tv, ov))
			case !inBase && !inTheirs:
				out.Set(k, jsonv.Clone(ov))
			}
			// In base but gone from theirs: they removed it.
		})
		t.Each(func(k string, tv jsonv.Value) {
			if !o.Has(k) && !b.Has(k) {
				out.Set(k, jsonv.Clone(tv))
			}
		})
		return out
	}
	if b, t, o := jsonv.Arr(base), jsonv.Arr(theirs), jsonv.Arr(ours); b != nil && t != nil && o != nil && nodeList(b) && nodeList(t) && nodeList(o) {
		return mergeNodes(b.Items, t.Items, o.Items)
	}
	return jsonv.Clone(theirs)
}

// nodeList reports that every item has a string id.
func nodeList(a *jsonv.Array) bool {
	for _, n := range a.Items {
		if _, ok := jsonv.Str(jsonv.Get(n, "id")); !ok {
			return false
		}
	}
	return true
}

func idOf(n jsonv.Value) string { return jsonv.StrOr(jsonv.Get(n, "id")) }

func indexOfID(a []jsonv.Value, id string) int {
	return slices.IndexFunc(a, func(n jsonv.Value) bool { return idOf(n) == id })
}

// mergeNodes is merge3 of node lists, per node.
func mergeNodes(b, t, o []jsonv.Value) jsonv.Value {
	var out []jsonv.Value
	for _, n := range o {
		bi, ti := indexOfID(b, idOf(n)), indexOfID(t, idOf(n))
		switch {
		case bi >= 0 && ti >= 0:
			out = append(out, merge3(b[bi], t[ti], n))
		case bi < 0:
			out = append(out, jsonv.Clone(n))
		}
	}
	for i, n := range t {
		if indexOfID(b, idOf(n)) >= 0 || indexOfID(o, idOf(n)) >= 0 {
			continue
		}
		at := 0
		for j := i - 1; j >= 0; j-- {
			if p := indexOfID(out, idOf(t[j])); p >= 0 {
				at = p + 1
				break
			}
		}
		out = slices.Insert(out, at, jsonv.Clone(n))
	}
	// A reorder by theirs re-sequences the shared nodes in the slots they already fill.
	shared := func(a, other []jsonv.Value) []string {
		var ids []string
		for _, n := range a {
			if indexOfID(other, idOf(n)) >= 0 {
				ids = append(ids, idOf(n))
			}
		}
		return ids
	}
	if !slices.Equal(shared(b, t), shared(t, b)) {
		var slots []int
		for i, n := range out {
			if indexOfID(t, idOf(n)) >= 0 {
				slots = append(slots, i)
			}
		}
		moved := make([]jsonv.Value, len(slots))
		for k, i := range slots {
			moved[k] = out[i]
		}
		slices.SortStableFunc(moved, func(x, y jsonv.Value) int { return indexOfID(t, idOf(x)) - indexOfID(t, idOf(y)) })
		for k, i := range slots {
			out[i] = moved[k]
		}
	}
	if out == nil {
		out = []jsonv.Value{}
	}
	return jsonv.NewArray(out...)
}
