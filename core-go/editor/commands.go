package editor

import (
	"errors"
	"math"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// The editor's UI commands, as lib.rs `dispatch` routes them.
func init() {
	engine.Handle("bounds", bounds)
	engine.Handle("move_by", moveBy)
	engine.Handle("duplicate", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		out, err := Duplicate(e, engine.IDsArg(jsonv.Get(req, "ids")), jsonv.IsTrue(jsonv.Get(req, "inPlace")))
		if err != nil {
			return nil, err
		}
		e.Selection = out
		return engine.Object("ids", engine.Strings(out), "state", e.State()), nil
	})
	// Move under another parent keeping the canvas position; `index` among its other children.
	engine.Handle("reparent", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		index := -1
		if n, ok := jsonv.Get(req, "index").(jsonv.Number); ok {
			if u, ok := n.Uint64(); ok {
				index = int(min(u, math.MaxInt)) // past every child, as Rust's `i as usize`
			}
		}
		if err := Reparent(e, targets(req), strOr(req, "parent", doc.Root), index); err != nil {
			return nil, err
		}
		return e.State(), nil
	})
	engine.Handle("snap_targets", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		e.Sync()
		return SnapTargets(e, engine.IDsArg(jsonv.Get(req, "ids")), rectArg(jsonv.Get(req, "view"))), nil
	})
	engine.Handle("nodes_in_rect", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		e.Sync()
		r := rectArg(req)
		if r == nil {
			return nil, errors.New("x, y, w, h required")
		}
		return engine.Strings(NodesInRect(e, *r, jsonv.IsTrue(jsonv.Get(req, "deep")))), nil
	})
	engine.Handle("drop_target", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		e.Sync()
		g := func(k string) float64 { return f64Or0(jsonv.Get(req, k)) }
		parent, hasParent := jsonv.Str(jsonv.Get(req, "parent"))
		return DropTarget(e, g("x"), g("y"), g("w"), g("h"), engine.IDsArg(jsonv.Get(req, "ignore")), jsonv.IsTrue(jsonv.Get(req, "any")), parent, hasParent), nil
	})
	engine.Handle("wrap", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		id, err := Wrap(e, engine.IDsArg(jsonv.Get(req, "ids")), strOr(req, "kind", "group"))
		if err != nil {
			return nil, err
		}
		return engine.Object("id", id, "state", e.State()), nil
	})
	engine.Handle("unwrap", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		out, err := Unwrap(e, engine.IDsArg(jsonv.Get(req, "ids")))
		if err != nil {
			return nil, err
		}
		return engine.Object("ids", engine.Strings(out), "state", e.State()), nil
	})
	engine.Handle("add_layout", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		id, err := AddLayout(e, engine.IDsArg(jsonv.Get(req, "ids")))
		if err != nil {
			return nil, err
		}
		return engine.Object("id", id, "state", e.State()), nil
	})
	engine.Handle("set_layout", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		if err := SetLayout(e, engine.IDsArg(jsonv.Get(req, "ids")), strOr(req, "layout", "horizontal")); err != nil {
			return nil, err
		}
		return e.State(), nil
	})
	engine.Handle("remove_layout", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		if err := RemoveLayout(e, engine.IDsArg(jsonv.Get(req, "ids"))); err != nil {
			return nil, err
		}
		return e.State(), nil
	})
	engine.Handle("align", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		if err := Align(e, engine.IDsArg(jsonv.Get(req, "ids")), strOr(req, "dir", "")); err != nil {
			return nil, err
		}
		return e.State(), nil
	})
	engine.Handle("copy", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		return Copy(e, engine.IDsArg(jsonv.Get(req, "ids"))), nil
	})
	engine.Handle("paste", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		out, err := Paste(e, strOr(req, "html", ""), strOr(req, "text", ""), engine.IDsArg(jsonv.Get(req, "targets")), rectArg(jsonv.Get(req, "view")))
		if err != nil {
			return nil, err
		}
		return engine.Object("ids", engine.Strings(out), "state", e.State()), nil
	})
	// Vector editing: a path's anchors in canvas coordinates, and writing them back (or a new path).
	engine.Handle("path_edit", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		id, ok := jsonv.Str(jsonv.Get(req, "id"))
		if !ok {
			return nil, errors.New("id required")
		}
		return PathEdit(e, id)
	})
	engine.Handle("path_write", func(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
		id, hasID := jsonv.Str(jsonv.Get(req, "id"))
		out, err := PathWrite(e, id, hasID, strOr(req, "parent", doc.Root), jsonv.Get(req, "contours"))
		if err != nil {
			return nil, err
		}
		return engine.Object("id", out, "state", e.State()), nil
	})
}

// targets is `ids` when it is an array, else `id`.
func targets(req jsonv.Value) []string {
	if jsonv.Arr(jsonv.Get(req, "ids")) != nil {
		return engine.IDsArg(jsonv.Get(req, "ids"))
	}
	return engine.IDsArg(jsonv.Get(req, "id"))
}

func strOr(req jsonv.Value, k, d string) string {
	if s, ok := jsonv.Str(jsonv.Get(req, k)); ok {
		return s
	}
	return d
}

// rectArg is lib.rs `rect_arg`: x, y, w and h, all numbers, or nil.
func rectArg(v jsonv.Value) *Rect {
	var r [4]float64
	for i, k := range []string{"x", "y", "w", "h"} {
		f, ok := jsonv.F64(jsonv.Get(v, k))
		if !ok {
			return nil
		}
		r[i] = f
	}
	return &Rect{X: r[0], Y: r[1], W: r[2], H: r[3]}
}

// bounds is what `ids` paint, or with none every top-level layer, as {x, y, w, h} (null when nothing).
func bounds(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
	e.Sync()
	sel := engine.IDsArg(jsonv.Get(req, "ids"))
	var b Rect
	var ok bool
	if len(sel) == 0 {
		b, ok = FitBounds(e)
	} else {
		b, ok = PaintedBounds(e, sel)
	}
	if !ok {
		return nil, nil
	}
	return engine.Object("x", jsonv.Float(b.X), "y", jsonv.Float(b.Y), "w", jsonv.Float(b.W), "h", jsonv.Float(b.H)), nil
}

// moveBy moves layers by (dx, dy) in their parents' space. A moved layer carries its descendants: one that is
// also listed would move twice.
func moveBy(e *engine.Engine, req jsonv.Value) (jsonv.Value, error) {
	dx, dy := f64Or0(jsonv.Get(req, "dx")), f64Or0(jsonv.Get(req, "dy"))
	list := engine.IDsArg(jsonv.Get(req, "ids"))
	e.Sync()
	type pos struct {
		id   string
		x, y float64
	}
	var at []pos
	for _, id := range e.Doc.Outermost(list) {
		if n := e.Doc.Find(id); n != nil {
			at = append(at, pos{id, f64Or0(jsonv.Get(n, "x")), f64Or0(jsonv.Get(n, "y"))})
		}
	}
	apply := func(d *doc.Doc) error {
		for _, p := range at {
			if err := d.Update(p.id, engine.Object("x", Num(p.x+dx), "y", Num(p.y+dy))); err != nil {
				return err
			}
		}
		return nil
	}
	var err error
	if key, ok := jsonv.Str(jsonv.Get(req, "coalesce")); ok {
		err = e.EditCoalesced(key, apply)
	} else {
		err = e.EditOrDrag(apply)
	}
	if err != nil {
		return nil, err
	}
	return e.State(), nil
}
