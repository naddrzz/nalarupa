package editor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// move is a node and the properties it takes where it goes.
type move struct {
	id    string
	props *jsonv.Object
}

// Reparent moves ids under parent (at index among its other children, else on top when index < 0) keeping
// their position on the canvas. The same parent without an index is a no-op.
func Reparent(e *engine.Engine, ids []string, parent string, index int) error {
	e.Sync()
	v := newView(e)
	if !v.acceptsChildren(parent) {
		return fmt.Errorf("'%s' cannot contain other layers", parent)
	}
	flow := v.hasLayout(parent)
	var plan []move
	for _, id := range v.paintOrder(e.Doc.Outermost(ids)) {
		if strings.Contains(id, "/") || id == parent || e.Doc.Find(id) == nil {
			continue
		}
		if p, _, ok := e.Doc.ParentOf(id); index < 0 && ok && p == parent {
			continue
		}
		absolute := jsonv.StrIs(jsonv.Get(e.Doc.Find(id), "layoutPosition"), "absolute")
		props := jsonv.NewObject()
		if !flow {
			props = v.freezeFill(id)
		}
		if ox, oy, t, ok := v.placed(id, parent); ok {
			if !flow || absolute {
				props.Set("x", Num(ox))
				props.Set("y", Num(oy))
			}
			setTurn(props, t)
		}
		plan = append(plan, move{id, props})
	}
	return e.EditOrDrag(func(d *doc.Doc) error {
		for _, m := range plan {
			if err := d.MoveNode(m.id, parent, -1); err != nil {
				return err
			}
		}
		if index >= 0 {
			for k, m := range plan {
				if err := d.MoveNode(m.id, parent, index+k); err != nil {
					return err
				}
			}
		}
		for _, m := range plan {
			if err := d.Update(m.id, jsonv.Clone(m.props)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Wrap wraps the selection in a new group or (unfilled, freeform) frame placed above the topmost selected
// layer, keeping everything where it is (Ctrl+G / Ctrl+Alt+G). It returns the new layer's id.
func Wrap(e *engine.Engine, ids []string, kind string) (string, error) {
	e.Sync()
	v := newView(e)
	var members []string
	for _, id := range v.paintOrder(e.Doc.Outermost(ids)) {
		if _, ok := v.rect(id); ok && !strings.Contains(id, "/") {
			members = append(members, id)
		}
	}
	if len(members) == 0 {
		return "", errors.New("select layers to wrap")
	}
	parent, at, ok := e.Doc.ParentOf(members[len(members)-1])
	if !ok {
		return "", errors.New("node not found")
	}
	pox, poy := v.originOr0(parent)
	// Each member's origin in the parent's space (the members may sit in differently turned frames).
	type place struct {
		id     string
		ox, oy float64
		t      *turn
	}
	var placed []place
	for _, id := range members {
		if ox, oy, t, ok := v.placed(id, parent); ok {
			placed = append(placed, place{id, pox + ox, poy + oy, t})
		}
	}
	// The bounds of what the members paint, in the parent's own (possibly turned) space.
	var pm *Affine
	if p := v.flat(parent); p != nil {
		pm = p.M
	}
	var boxes []Rect
	for _, p := range placed {
		if b, ok := v.painted(p.id, pm); ok {
			boxes = append(boxes, b)
		}
	}
	u, ok := union(boxes)
	if !ok {
		return "", errors.New("nothing visible to wrap")
	}
	var node *jsonv.Object
	if kind == "frame" {
		node = engine.Object("type", "frame", "name", "Frame", "width", Num(u.W), "height", Num(u.H), "layout", "none")
	} else {
		node = engine.Object("type", "group", "name", "Group")
	}
	node.Set("x", Num(u.X-pox))
	node.Set("y", Num(u.Y-poy))
	moves := make([]doc.UpdateItem, len(placed))
	moved := make([]string, len(placed))
	for i, p := range placed {
		props := v.freezeFill(p.id)
		props.Set("x", Num(p.ox-u.X))
		props.Set("y", Num(p.oy-u.Y))
		setTurn(props, p.t)
		moves[i] = doc.UpdateItem{Path: p.id, Data: props}
		moved[i] = p.id
	}
	var id string
	err := e.EditOrDrag(func(d *doc.Doc) (err error) {
		if id, err = d.Insert(parent, jsonv.Clone(node), at+1); err != nil {
			return err
		}
		items := make([]doc.UpdateItem, len(moves))
		for i, m := range moves {
			items[i] = doc.UpdateItem{Path: m.Path, Data: jsonv.Clone(m.Data)}
		}
		if err := d.UpdateAll(items); err != nil {
			return err
		}
		return d.MoveAll(moved, id)
	})
	return id, err
}

// Unwrap replaces groups and frames by their children, in place (Ctrl+Shift+G), and returns the new
// selection.
func Unwrap(e *engine.Engine, ids []string) ([]string, error) {
	e.Sync()
	v := newView(e)
	type container struct {
		id    string
		moves []move
	}
	var plan []container
	var rest []string
	for _, id := range e.Doc.Outermost(ids) {
		t := v.ty(id)
		isContainer := !strings.Contains(id, "/") && (t == "frame" || t == "group")
		parent, _, ok := e.Doc.ParentOf(id)
		if !isContainer || !ok {
			rest = append(rest, id)
			continue
		}
		pox, poy := v.originOr0(parent)
		cox, coy := v.originOr0(id)
		flow := v.hasLayout(parent)
		var moves []move
		for _, k := range kids(e.Doc, id) {
			kid, ok := jsonv.Str(jsonv.Get(k, "id"))
			if !ok {
				continue
			}
			props := jsonv.NewObject()
			if !flow {
				props = v.freezeFill(kid)
			}
			ox, oy, t, ok := v.placed(kid, parent)
			if !ok {
				kx, _ := jsonv.F64(jsonv.Get(k, "x"))
				ky, _ := jsonv.F64(jsonv.Get(k, "y"))
				ox, oy, t = cox+kx-pox, coy+ky-poy, nil
			}
			props.Set("x", Num(ox))
			props.Set("y", Num(oy))
			setTurn(props, t)
			moves = append(moves, move{kid, props})
		}
		plan = append(plan, container{id, moves})
	}
	if len(plan) == 0 {
		return nil, errors.New("select a group or frame to ungroup")
	}
	var out []string
	err := e.EditOrDrag(func(d *doc.Doc) error {
		out = append([]string{}, rest...)
		for _, c := range plan {
			parent, at, ok := d.ParentOf(c.id)
			if !ok {
				return errors.New("node not found")
			}
			for k, m := range c.moves {
				if err := d.MoveNode(m.id, parent, at+1+k); err != nil {
					return err
				}
				if err := d.Update(m.id, jsonv.Clone(m.props)); err != nil {
					return err
				}
				out = append(out, m.id)
			}
			if err := d.Delete(c.id); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}
