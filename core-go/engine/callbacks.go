package engine

import (
	"github.com/naddrzz/nalarupa/core-go/history"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// replaceString replaces every string equal to from with to in *v; true when it found one.
func replaceString(v *jsonv.Value, from, to string) bool {
	switch x := (*v).(type) {
	case string:
		if x == from {
			*v = to
			return true
		}
	case *jsonv.Array:
		hit := false
		for i := range x.Items {
			hit = replaceString(&x.Items[i], from, to) || hit
		}
		return hit
	case *jsonv.Object:
		hit := false
		for _, k := range x.Keys() {
			hit = replaceString(x.Ref(k), from, to) || hit
		}
		return hit
	}
	return false
}

// ResolvePending: a background image finished, so its pending url is swapped for the real one everywhere, the
// libraries and the history included (no undo step). An empty url means it failed.
func (e *Engine) ResolvePending(pending, url string) {
	delete(e.Inflight, pending)
	var root jsonv.Value = e.Doc.Root
	hit := replaceString(&root, pending, url)
	for i := range e.Libs {
		hit = replaceString(&e.Libs[i].Root, pending, url) || hit
	}
	for _, p := range append(append([]history.Patch(nil), e.undo...), e.redo...) {
		history.ValuesMut(p, func(v *jsonv.Value) { hit = replaceString(v, pending, url) || hit })
	}
	if hit {
		e.Touch()
		if url == "" {
			ev := jsonv.NewObject()
			ev.Set("type", "toast")
			ev.Set("text", "Failed to load a generated image")
			e.Emit(ev)
		}
	}
}

// FontReady: a background font download finished, so text that used a fallback face lays out again. The
// document is unchanged, so a clean one stays clean.
func (e *Engine) FontReady(family string, weight uint16, italic bool) {
	if e.Fonts.Downloaded(family, weight, italic) {
		e.redraw(!e.Dirty())
	}
}

// AssetReady: a background icon or image download finished (or failed), so layers that drew without it draw
// again. The document is unchanged, so a clean one stays clean.
func (e *Engine) AssetReady(key string, ok bool) {
	e.Assets.Downloaded(key, ok)
	e.redraw(!e.Dirty())
}

// nodeIn is the node id in the tree v (v itself or a descendant through `children`), or nil.
func nodeIn(v jsonv.Value, id string) *jsonv.Object {
	o := jsonv.Obj(v)
	if o == nil {
		return nil
	}
	if jsonv.StrIs(jsonv.Get(o, "id"), id) {
		return o
	}
	for _, k := range jsonv.Items(jsonv.Get(o, "children")) {
		if n := nodeIn(k, id); n != nil {
			return n
		}
	}
	return nil
}

// ApplyGenerated: a background generator (the AI Pen) drew into node id, so props are set on it without an
// undo step, in the document and the live preview. When done, every undo/redo snapshot holding the node gets
// them too, so history never brings back a half-drawn result. False when the node is gone.
func (e *Engine) ApplyGenerated(id string, props *jsonv.Object, done bool) bool {
	set := func(v jsonv.Value) bool {
		n := nodeIn(v, id)
		if n == nil {
			return false
		}
		props.Each(func(k string, x jsonv.Value) { n.Set(k, jsonv.Clone(x)) })
		return true
	}
	hit := set(e.Doc.Root)
	if e.Preview != nil {
		set(e.Preview.Root)
	}
	if done {
		// A step is relative to the document next to it, so each is rebuilt from the current one outwards.
		for _, stack := range [][]history.Patch{e.undo, e.redo} {
			cur := jsonv.Clone(e.Doc.Root)
			for i := len(stack) - 1; i >= 0; i-- {
				next := jsonv.Clone(cur)
				history.Apply(stack[i], &next)
				set(next)
				stack[i] = orNoop(history.Diff(cur, next))
				cur = next
			}
		}
	}
	if hit {
		e.Version++
	}
	return hit
}
