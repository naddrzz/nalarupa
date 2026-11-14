// Package history is undo as patches (core/src/history.rs): a step stores what the edit changed, not a
// copy of the document.
package history

import (
	"slices"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Patch turns one document into another; unchanged parts are not stored. It is a Set, an Obj or an Arr.
type Patch interface{ patch() }

// Set replaces the whole value.
type Set struct{ V jsonv.Value }

// Obj patches an object per changed key. Reorder restores Order as the key order when keys were added
// or removed.
type Obj struct {
	Reorder bool
	Order   []string
	Keys    []KeyPatch
}

// KeyPatch is the patch of one key; a nil P removes the key.
type KeyPatch struct {
	Key string
	P   Patch
}

// Arr replaces items [Head, len - Tail): the slice outright (Replace, with Values), or item by item
// (Each, nil = unchanged) when its length is kept.
type Arr struct {
	Head, Tail int
	Replace    bool
	Values     []jsonv.Value
	Each       []Patch
}

func (*Set) patch() {}
func (*Obj) patch() {}
func (*Arr) patch() {}

// Noop is the patch that changes nothing.
func Noop() Patch { return &Obj{} }

// Same is equality as documents: a whole number written back as a float (10 vs 10.0) is not a change.
func Same(a, b jsonv.Value) bool {
	switch x := a.(type) {
	case jsonv.Number:
		y, ok := b.(jsonv.Number)
		return ok && jsonv.SameNumber(x, y)
	case *jsonv.Array:
		y, ok := b.(*jsonv.Array)
		return ok && slices.EqualFunc(x.Items, y.Items, Same)
	case *jsonv.Object:
		y, ok := b.(*jsonv.Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		eq := true
		x.Each(func(k string, v jsonv.Value) {
			w, ok := y.Get(k)
			eq = eq && ok && Same(v, w)
		})
		return eq
	}
	return jsonv.Equal(a, b)
}

// Diff returns the patch that turns from into to; nil when they are the same document.
func Diff(from, to jsonv.Value) Patch {
	switch a := from.(type) {
	case *jsonv.Object:
		if b, ok := to.(*jsonv.Object); ok {
			var keys []KeyPatch
			reshaped := false
			b.Each(func(k string, w jsonv.Value) {
				if v, ok := a.Get(k); ok {
					if p := Diff(v, w); p != nil {
						keys = append(keys, KeyPatch{k, p})
					}
				} else {
					reshaped = true
					keys = append(keys, KeyPatch{k, &Set{jsonv.Clone(w)}})
				}
			})
			a.Each(func(k string, _ jsonv.Value) {
				if !b.Has(k) {
					reshaped = true
					keys = append(keys, KeyPatch{k, nil})
				}
			})
			if len(keys) == 0 {
				return nil
			}
			p := &Obj{Keys: keys, Reorder: reshaped}
			if reshaped {
				p.Order = b.Keys()
			}
			return p
		}
	case *jsonv.Array:
		if b, ok := to.(*jsonv.Array); ok {
			ai, bi := a.Items, b.Items
			head := 0
			for head < len(ai) && head < len(bi) && Same(ai[head], bi[head]) {
				head++
			}
			if head == len(ai) && head == len(bi) {
				return nil
			}
			tail := 0
			for tail < len(ai)-head && tail < len(bi)-head && Same(ai[len(ai)-1-tail], bi[len(bi)-1-tail]) {
				tail++
			}
			am, bm := ai[head:len(ai)-tail], bi[head:len(bi)-tail]
			p := &Arr{Head: head, Tail: tail}
			if len(am) == len(bm) {
				p.Each = make([]Patch, len(am))
				for i := range am {
					p.Each[i] = Diff(am[i], bm[i])
				}
			} else {
				p.Replace = true
				p.Values = make([]jsonv.Value, len(bm))
				for i, v := range bm {
					p.Values[i] = jsonv.Clone(v)
				}
			}
			return p
		}
	}
	if Same(from, to) {
		return nil
	}
	return &Set{jsonv.Clone(to)}
}

// Apply applies p to *target and returns the patch that undoes it.
func Apply(p Patch, target *jsonv.Value) Patch {
	switch x := p.(type) {
	case *Set:
		old := *target
		*target = x.V
		return &Set{old}
	case *Obj:
		m, ok := (*target).(*jsonv.Object)
		if !ok {
			return Noop()
		}
		var before []string
		if x.Reorder {
			before = m.Keys()
		}
		inv := make([]KeyPatch, 0, len(x.Keys))
		for _, kp := range x.Keys {
			slot := m.Ref(kp.Key)
			switch set, isSet := kp.P.(*Set); {
			case kp.P != nil && slot != nil:
				inv = append(inv, KeyPatch{kp.Key, Apply(kp.P, slot)})
			case isSet && slot == nil:
				m.Set(kp.Key, set.V)
				inv = append(inv, KeyPatch{kp.Key, nil})
			case kp.P == nil && slot != nil:
				old, _ := m.Remove(kp.Key)
				inv = append(inv, KeyPatch{kp.Key, &Set{old}})
			}
		}
		if x.Reorder {
			reorder(m, x.Order)
		}
		return &Obj{Reorder: x.Reorder, Order: before, Keys: inv}
	case *Arr:
		a, ok := (*target).(*jsonv.Array)
		if !ok {
			return Noop()
		}
		end := max(len(a.Items)-x.Tail, 0)
		if x.Head > end {
			return Noop()
		}
		out := &Arr{Head: x.Head, Tail: x.Tail, Replace: x.Replace}
		if x.Replace {
			out.Values = slices.Clone(a.Items[x.Head:end])
			a.Items = slices.Replace(a.Items, x.Head, end, x.Values...)
			return out
		}
		n := min(len(x.Each), end-x.Head)
		out.Each = make([]Patch, n)
		for i := range n {
			if x.Each[i] != nil {
				out.Each[i] = Apply(x.Each[i], &a.Items[x.Head+i])
			}
		}
		return out
	}
	return Noop()
}

// ValuesMut calls f on every value the patch stores outright (what the step would write back).
func ValuesMut(p Patch, f func(*jsonv.Value)) {
	switch x := p.(type) {
	case *Set:
		f(&x.V)
	case *Obj:
		for _, kp := range x.Keys {
			if kp.P != nil {
				ValuesMut(kp.P, f)
			}
		}
	case *Arr:
		for i := range x.Values {
			f(&x.Values[i])
		}
		for _, q := range x.Each {
			if q != nil {
				ValuesMut(q, f)
			}
		}
	}
}

// reorder puts m's keys in order (as history.rs `reorder`, with serde_json's swap-removing `remove`).
func reorder(m *jsonv.Object, order []string) {
	rest := jsonv.NewObject()
	for _, k := range m.Keys() {
		v, _ := m.Remove(k)
		rest.Set(k, v)
	}
	for _, k := range order {
		if v, ok := rest.Remove(k); ok {
			m.Set(k, v)
		}
	}
	rest.Each(func(k string, v jsonv.Value) { m.Set(k, v) })
}
