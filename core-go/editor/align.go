package editor

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
)

// Align aligns layers (left, hcenter, right, top, vcenter, bottom). One layer aligns to its parent (a
// top-level frame aligns its children inside it); several align to their union. Layers placed by auto layout
// are skipped. Edges and centers are those of the painted (rotated, flipped) bounds.
func Align(e *engine.Engine, ids []string, dir string) error {
	e.Sync()
	v := newView(e)
	var free []string
	for _, id := range e.Doc.Outermost(ids) {
		if _, ok := v.rect(id); ok && !strings.Contains(id, "/") && !v.inFlow(id) {
			free = append(free, id)
		}
	}
	// frame: the container whose own coordinates the alignment happens in (nil = canvas).
	var targets []string
	var to Rect
	var hasTo bool
	var frame *Affine
	matrixOf := func(id string) *Affine {
		if f := v.flat(id); f != nil {
			return f.M
		}
		return nil
	}
	if len(free) == 1 {
		one := free[0]
		if p, _, ok := e.Doc.ParentOf(one); ok && p != doc.Root {
			targets = free
			to, hasTo = v.rect(p)
			frame = matrixOf(p)
		} else {
			for _, k := range kids(e.Doc, one) {
				id, ok := jsonv.Str(jsonv.Get(k, "id"))
				if !ok || v.inFlow(id) {
					continue
				}
				if _, ok := v.rect(id); ok {
					targets = append(targets, id)
				}
			}
			to, hasTo = v.rect(one)
			frame = matrixOf(one)
		}
	} else {
		targets = free
		var boxes []Rect
		for _, id := range free {
			if b, ok := v.painted(id, nil); ok {
				boxes = append(boxes, b)
			}
		}
		to, hasTo = union(boxes)
	}
	if !hasTo {
		return nil
	}
	type step struct {
		id     string
		dx, dy float64
	}
	var moves []step
	for _, id := range targets {
		b, ok := v.painted(id, frame)
		if !ok {
			continue
		}
		var dx, dy float64
		switch dir {
		case "left":
			dx = to.X - b.X
		case "hcenter":
			dx = to.X + to.W/2 - b.X - b.W/2
		case "right":
			dx = to.X + to.W - b.X - b.W
		case "top":
			dy = to.Y - b.Y
		case "vcenter":
			dy = to.Y + to.H/2 - b.Y - b.H/2
		case "bottom":
			dy = to.Y + to.H - b.Y - b.H
		default:
			continue
		}
		// A canvas delta becomes a step in the node's parent space (x/y are relative to it).
		if f := v.flat(id); f != nil && f.HasParent && frame == nil {
			if p := v.flat(f.Parent); p != nil && p.M != nil {
				ox, oy := render.Unapply(*p.M, 0, 0)
				x, y := render.Unapply(*p.M, dx, dy)
				dx, dy = x-ox, y-oy
			}
		}
		if math.Abs(dx) > 1e-9 || math.Abs(dy) > 1e-9 {
			moves = append(moves, step{id, dx, dy})
		}
	}
	return e.EditOrDrag(func(d *doc.Doc) error {
		for _, m := range moves {
			n := d.Find(m.id)
			if n == nil {
				return errors.New("node not found")
			}
			x, _ := jsonv.F64(jsonv.Get(n, "x"))
			y, _ := jsonv.F64(jsonv.Get(n, "y"))
			if err := d.Update(m.id, engine.Object("x", Num(x+m.dx), "y", Num(y+m.dy))); err != nil {
				return err
			}
		}
		return nil
	})
}

// Duplicate is Ctrl+D: copies right above their sources at the same position. A single top-level frame goes
// into free space on its right instead, unless inPlace (Alt-drag duplicate). It returns the copies' ids.
func Duplicate(e *engine.Engine, ids []string, inPlace bool) ([]string, error) {
	e.Sync()
	ids = newView(e).paintOrder(e.Doc.Outermost(ids))
	var right jsonv.Value
	if len(ids) == 1 && !inPlace {
		one := ids[0]
		if p, _, ok := e.Doc.ParentOf(one); ok && p == doc.Root && jsonv.StrIs(jsonv.Get(e.Doc.Find(one), "type"), "frame") {
			if f := e.Renderer.Find(one); f != nil {
				right = emptySpace(&e.Renderer, f)
			}
		}
	}
	var out []string
	err := e.EditOrDrag(func(d *doc.Doc) error {
		out = []string{}
		for _, id := range ids {
			src := jsonv.Obj(jsonv.Clone(d.Find(id)))
			if src == nil {
				return fmt.Errorf("node not found: %s", id)
			}
			parent, idx, ok := d.ParentOf(id)
			if !ok {
				return fmt.Errorf("node not found: %s", id)
			}
			data := engine.Object("reusable", nil)
			if right != nil {
				data.Set("x", jsonv.Clone(jsonv.Get(right, "x")))
				data.Set("y", jsonv.Clone(jsonv.Get(right, "y")))
			} else if _, isNum := jsonv.Get(src, "x").(jsonv.Number); isNum || parent == doc.Root {
				x, _ := jsonv.F64(jsonv.Get(src, "x"))
				y, _ := jsonv.F64(jsonv.Get(src, "y"))
				data.Set("x", jsonv.Float(x))
				data.Set("y", jsonv.Float(y))
			}
			src.Set("reusable", false)
			n, err := d.Copy(src, parent, data)
			if err != nil {
				return err
			}
			if err := d.MoveNode(n, parent, idx+1); err != nil {
				return err
			}
			out = append(out, n)
		}
		return nil
	})
	return out, err
}

// emptySpace is render.rs `find_empty_space` to the right of f as its JSON {x, y}.
func emptySpace(r *render.Renderer, f *render.Flat) jsonv.Value {
	x, y, ok := render.FindEmptySpaceOK(r, f.W, f.H, "right", 40, f)
	if !ok {
		return engine.Object("x", jsonv.Int(0), "y", jsonv.Int(0))
	}
	return engine.Object("x", jsonv.Float(x), "y", jsonv.Float(y))
}
