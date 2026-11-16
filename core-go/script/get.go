package script

// `Get` and `Copy` reading (script.rs `GetOpts`, `to_json`, `node_json`, `trim_depth`, `n_get_tree`): nodes as
// schema data that round-trips into Insert and Update, from the document or, when variables or instances are
// resolved or the path addresses an instance descendant, from the laid-out tree.

import (
	"fmt"
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// anyDepth is `usize::MAX`: no depth limit.
const anyDepth = math.MaxInt

// getOpts are the options of a Get.
type getOpts struct {
	resolveVars, resolveInst, geometry bool
	depth                              int
}

func getOptsFrom(v jsonv.Value) getOpts {
	o := getOpts{
		resolveVars: jsonv.IsTrue(jsonv.Get(v, "resolveVariables")),
		resolveInst: jsonv.IsTrue(jsonv.Get(v, "resolveInstances")),
		geometry:    jsonv.IsTrue(jsonv.Get(v, "includePathGeometry")),
		depth:       anyDepth,
	}
	if n, ok := jsonv.Get(v, "depth").(jsonv.Number); ok {
		if d, ok := n.Uint64(); ok {
			o.depth = int(min(d, math.MaxInt))
		}
	}
	return o
}

// toJSON serializes a laid-out node back into schema data.
func (ex *exec) toJSON(r *resolve.RNode, o getOpts, depth int, children bool) jsonv.Value {
	var v *jsonv.Object
	if !strings.Contains(r.ID, "/") && !o.resolveVars {
		if raw := ex.host.Doc().Find(r.ID); raw != nil {
			v = jsonv.Obj(jsonv.Clone(raw))
			v.Remove("children")
		}
	}
	if v == nil {
		v = jsonv.NewObject()
		v.Set("id", r.ID)
		v.Set("type", r.Type)
		r.P.Each(func(k string, val jsonv.Value) {
			// `__status` is a script node's render-only state.
			if k != "id" && k != "type" && !strings.HasPrefix(k, "__") {
				v.Set(k, jsonv.Clone(val))
			}
		})
		if r.Inst != "" {
			v.Set("ref", r.Inst)
		}
	}
	if !o.geometry && v.Has("geometry") {
		v.Set("geometry", "...")
	}
	// A script node's generated children are not part of the document.
	if children && len(r.Kids) > 0 && (r.Inst == "" || o.resolveInst) && r.Type != "script" {
		if depth >= o.depth {
			v.Set("children", "...")
		} else {
			kids := jsonv.NewArray()
			for _, k := range r.Kids {
				kids.Items = append(kids.Items, ex.toJSON(k, o, depth+1, true))
			}
			v.Set("children", kids)
		}
	}
	return v
}

// nodeJSON is what `Get(path, opts)` returns.
func (ex *exec) nodeJSON(path string, o getOpts) (jsonv.Value, error) {
	notFound := fmt.Errorf("node not found: %s", path)
	d := ex.host.Doc()
	if !o.resolveVars && !o.resolveInst && !strings.Contains(path, "/") {
		raw := d.Find(path)
		if raw == nil {
			return nil, notFound
		}
		return trimDepth(jsonv.Clone(raw), o, 0), nil
	}
	top := d.TopIndex(path)
	if top < 0 {
		return nil, notFound
	}
	root := ex.host.LaidOut(top)
	if root == nil {
		return nil, notFound
	}
	r := root.Find(path)
	if r == nil {
		return nil, notFound
	}
	return ex.toJSON(r, o, 0, true), nil
}

// trimDepth cuts a document node's children below the depth limit and its path geometry unless asked for.
func trimDepth(v jsonv.Value, o getOpts, depth int) jsonv.Value {
	obj := jsonv.Obj(v)
	if obj == nil {
		return v
	}
	if !o.geometry && obj.Has("geometry") {
		obj.Set("geometry", "...")
	}
	if kids, ok := obj.Get("children"); ok {
		if depth >= o.depth {
			obj.Set("children", "...")
		} else if arr := jsonv.Arr(kids); arr != nil {
			for i, k := range arr.Items {
				arr.Items[i] = trimDepth(k, o, depth+1)
			}
		}
	}
	return v
}

// getTree is `__GT`: the laid-out tree the visitor form of `Get` walks. Each entry is {n: node, b: bounds
// relative to the parent, p: problem, k: children}.
func (ex *exec) getTree(a []string) (string, error) {
	path := arg(a, 0)
	opts, err := jsonArg(a, 1)
	if err != nil {
		return "", err
	}
	o := getOptsFrom(opts)
	var entry func(r *resolve.RNode, px, py float64) jsonv.Value
	entry = func(r *resolve.RNode, px, py float64) jsonv.Value {
		kids := jsonv.NewArray()
		if !(r.Inst != "" && !o.resolveInst) && r.Type != "script" {
			for _, k := range r.Kids {
				kids.Items = append(kids.Items, entry(k, r.X, r.Y))
			}
		}
		b := jsonv.NewObject()
		b.Set("x", jsonv.Float(r.X-px))
		b.Set("y", jsonv.Float(r.Y-py))
		b.Set("width", jsonv.Float(r.W))
		b.Set("height", jsonv.Float(r.H))
		e := jsonv.NewObject()
		e.Set("n", ex.toJSON(r, o, 0, false))
		e.Set("b", b)
		if r.Problem != "" {
			e.Set("p", r.Problem)
		} else {
			e.Set("p", nil)
		}
		e.Set("k", kids)
		return e
	}
	out := jsonv.NewArray()
	d := ex.host.Doc()
	if path == "document" || path == "" {
		for i := range d.Children() {
			if root := ex.host.LaidOut(i); root != nil {
				out.Items = append(out.Items, entry(root, 0, 0))
			}
		}
	} else {
		notFound := fmt.Errorf("node not found: %s", path)
		top := d.TopIndex(path)
		if top < 0 {
			return "", notFound
		}
		root := ex.host.LaidOut(top)
		if root == nil {
			return "", notFound
		}
		px, py, _ := parentOrigin(root, path)
		r := root.Find(path)
		if r == nil {
			return "", notFound
		}
		out.Items = append(out.Items, entry(r, px, py))
	}
	return string(jsonv.Marshal(out)), nil
}

// parentOrigin is the position of the parent of node id in n's subtree.
func parentOrigin(n *resolve.RNode, id string) (float64, float64, bool) {
	for _, k := range n.Kids {
		if k.ID == id {
			return n.X, n.Y, true
		}
	}
	for _, k := range n.Kids {
		if x, y, ok := parentOrigin(k, id); ok {
			return x, y, true
		}
	}
	return 0, 0, false
}
