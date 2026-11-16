package engine

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// gated are the commands that take the edit gate (lib.rs `dispatch`).
var gated = []string{
	"update", "move_by", "insert", "delete", "duplicate", "reorder", "reparent", "wrap", "unwrap", "add_layout", "set_layout",
	"remove_layout", "align", "paste", "path_write", "drag_begin", "drag_end", "undo", "redo", "set_variable", "save",
}

// IDsArg is lib.rs `ids`: an array's strings, or one string.
func IDsArg(v jsonv.Value) []string {
	switch x := v.(type) {
	case *jsonv.Array:
		out := []string{}
		for _, it := range x.Items {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{x}
	}
	return []string{}
}

// Strings is a JSON array of a, [] when empty.
func Strings(a []string) *jsonv.Array {
	out := jsonv.NewArray()
	for _, s := range a {
		out.Items = append(out.Items, s)
	}
	if out.Items == nil {
		out.Items = []jsonv.Value{}
	}
	return out
}

// Object is a JSON object of key, value pairs in order.
func Object(kv ...any) *jsonv.Object {
	o := jsonv.NewObject()
	for i := 0; i+1 < len(kv); i += 2 {
		o.Set(kv[i].(string), kv[i+1])
	}
	return o
}

func u64(v uint64) jsonv.Number { return jsonv.Number(fmt.Sprint(v)) }

// State is lib.rs `state`: what the UI shows of the engine.
func (e *Engine) State() *jsonv.Object {
	var path jsonv.Value
	if e.Doc.Path != "" {
		path = e.Doc.Path
	}
	return Object("version", u64(e.Version), "title", e.Title(), "path", path, "dirty", e.Dirty(), "selection", Strings(e.Selection),
		"agentRunning", e.Agent.Running, "executing", e.Executing, "penDrawing", len(e.Agent.Pens) > 0, "mcpPort", u64(uint64(e.MCPPort)),
		"animating", len(e.Renderer.Slides) > 0 || script.Busy())
}

// Command is a UI command of another package (the editor operations), added to the dispatcher with Handle. It
// runs with the edit gate (when the command is gated) and the engine lock held.
type Command func(e *Engine, req jsonv.Value) (jsonv.Value, error)

var commands = map[string]Command{}

// Handle adds cmd to the dispatcher. Call it from an init function: the table is not locked.
func Handle(cmd string, f Command) { commands[cmd] = f }

// MatrixJSON is a painted transform as Rust writes `Option<[f64; 6]>`: null for the identity (nil).
func MatrixJSON(m *[6]float64) jsonv.Value {
	if m == nil {
		return nil
	}
	out := jsonv.NewArray()
	for _, v := range m {
		out.Items = append(out.Items, jsonv.Float(v))
	}
	return out
}

// FlatJSON is lib.rs `rect`: a laid-out node's box as the UI reads it.
func FlatJSON(f *render.Flat) *jsonv.Object {
	var parent jsonv.Value
	if f.HasParent {
		parent = f.Parent
	}
	return Object("id", f.ID, "name", f.Name, "type", f.Type, "x", jsonv.Float(f.X), "y", jsonv.Float(f.Y), "w", jsonv.Float(f.W),
		"h", jsonv.Float(f.H), "parent", parent, "depth", jsonv.Int(int64(f.Depth)), "placeholder", f.Placeholder, "pending", f.Pending,
		"m", MatrixJSON(f.M))
}

func rectJSON(r render.Rect) *jsonv.Object {
	return Object("x", jsonv.Float(r.X), "y", jsonv.Float(r.Y), "w", jsonv.Float(r.W), "h", jsonv.Float(r.H))
}

func floats(v []float64) *jsonv.Array {
	out := jsonv.NewArray()
	for _, f := range v {
		out.Items = append(out.Items, jsonv.Float(f))
	}
	return out
}

// Layers is lib.rs `layers`: the layer rows, auto-layout children in layout order and everything else topmost
// first. `index` is the position among the siblings in the document.
func (e *Engine) Layers() *jsonv.Array {
	out := jsonv.NewArray()
	out.Items = []jsonv.Value{}
	var walk func(nodes []jsonv.Value, depth int, parent string, flow bool)
	walk = func(nodes []jsonv.Value, depth int, parent string, flow bool) {
		for k := range nodes {
			index := k
			if !flow {
				index = len(nodes) - 1 - k
			}
			n := nodes[index]
			id := jsonv.StrOr(jsonv.Get(n, "id"))
			ty := jsonv.Get(n, "type")
			layout := jsonv.StrIs(ty, "frame") && !jsonv.StrIs(jsonv.Get(n, "layout"), "none")
			name, ok := jsonv.Str(jsonv.Get(n, "name"))
			if !ok || strings.TrimSpace(name) == "" {
				name, ok = jsonv.Str(jsonv.Get(n, "content"))
				if ok && jsonv.StrIs(ty, "text") {
					// The content's first line (`str::lines`), or all of it when it has none.
					if line, _, cut := strings.Cut(name, "\n"); cut {
						name = strings.TrimSuffix(line, "\r")
					}
				} else if name, ok = jsonv.Str(ty); !ok {
					name = "node"
				}
			}
			kids := jsonv.Arr(jsonv.Get(n, "children"))
			count := 0
			if kids != nil {
				count = len(kids.Items)
			}
			out.Items = append(out.Items, Object("id", id, "name", name, "type", jsonv.Clone(ty), "depth", jsonv.Int(int64(depth)),
				"parent", parent, "index", jsonv.Int(int64(index)), "layout", layout, "reusable", jsonv.IsTrue(jsonv.Get(n, "reusable")),
				"hidden", jsonv.IsFalse(jsonv.Get(n, "enabled")), "children", jsonv.Int(int64(count))))
			if kids != nil {
				walk(kids.Items, depth+1, id, layout)
			}
		}
	}
	walk(e.Doc.Children(), 0, doc.Root, false)
	return out
}

// NodeInfo is lib.rs's `node` command: the node as stored, what an instance inherits, its laid-out box and own
// turn, its parent and the parent's layout, and the document's variables.
func (e *Engine) NodeInfo(id string) *jsonv.Object {
	raw := jsonv.Clone(e.Doc.Find(id))
	e.Sync()
	var parent jsonv.Value
	parentLayout := "none"
	if p, _, ok := e.Doc.ParentOf(id); ok {
		parent = p
		if pn := e.Doc.Find(p); pn != nil && jsonv.StrIs(jsonv.Get(pn, "type"), "frame") {
			if parentLayout, ok = jsonv.Str(jsonv.Get(pn, "layout")); !ok {
				parentLayout = "horizontal"
			}
		}
	}
	var local, box jsonv.Value
	if f := e.Renderer.Find(id); f != nil {
		local = Object("bx", jsonv.Float(f.LocalX), "by", jsonv.Float(f.LocalY), "rotation", jsonv.Float(f.Rotation), "flipX", f.FlipX, "flipY", f.FlipY)
		box = FlatJSON(f)
	}
	// A component instance holds only its overrides: what it inherits, for the Inspector to show where it has none.
	res := resolve.New(e.Doc.Root, e.Libs)
	var inherited jsonv.Value
	if raw != nil && jsonv.StrIs(jsonv.Get(raw, "type"), "ref") {
		inherited = res.Inherited(raw)
	}
	vars, ok := e.Doc.Root.Get("variables")
	if !ok {
		vars = jsonv.NewObject()
	}
	return Object("node", raw, "exists", raw != nil || res.HasPath(id), "inherited", inherited, "box", box, "local", local, "parent", parent,
		"parentLayout", parentLayout, "variables", jsonv.Clone(vars))
}

// Reorder is lib.rs's `reorder` command: the stacking order (layout order inside auto layout) up or down by one,
// or to the front or back.
func (e *Engine) Reorder(targets []string, dir string) error {
	type item struct {
		id, parent string
		index      int
	}
	var items []item
	for _, id := range targets {
		if p, i, ok := e.Doc.ParentOf(id); ok {
			items = append(items, item{id, p, i})
		}
	}
	slices.SortStableFunc(items, func(a, b item) int { return a.index - b.index })
	if dir == "up" || dir == "front" {
		slices.Reverse(items)
	}
	return e.EditOrDrag(func(d *doc.Doc) error {
		// Final slot of the previous layer per parent: a one-step move must not pass it (the block stays in order at either end).
		last := map[string]int{}
		// Position within the block per parent: front/back slots count from each parent's own end.
		rank := map[string]int{}
		for _, it := range items {
			k, seen := rank[it.parent]
			if seen {
				k++
			}
			rank[it.parent] = k
			_, idx, ok := d.ParentOf(it.id)
			if !ok {
				return errors.New("node not found")
			}
			count := 1
			if it.parent == doc.Root {
				count = len(d.Children())
			} else if kids := jsonv.Arr(jsonv.Get(d.Find(it.parent), "children")); kids != nil {
				count = len(kids.Items)
			}
			var to int
			switch dir {
			case "front":
				to = max(count-(1+k), 0)
			case "back":
				to = k
			case "down":
				to = max(idx-1, 0)
			default:
				to = min(idx+1, count-1)
			}
			if l, ok := last[it.parent]; ok && l == to && (dir == "up" || dir == "down") {
				to = idx
			}
			last[it.parent] = to
			if err := d.MoveNode(it.id, it.parent, to); err != nil {
				return err
			}
		}
		return nil
	})
}

// png is lib.rs's `png` command: the node as a base64 PNG. Its script nodes run with no lock held.
func (e *Engine) png(req jsonv.Value) (jsonv.Value, error) {
	id, ok := jsonv.Str(jsonv.Get(req, "id"))
	if !ok {
		return nil, errors.New("id required")
	}
	scale, ok := jsonv.F64(jsonv.Get(req, "scale"))
	if !ok {
		scale = 1
	}
	scale = min(max(scale, 0.05), 8)
	type built struct {
		tree *usvg.Tree
		err  error
	}
	b := script.OffLock(func(rec *script.Runs) (out built) {
		e.Locked(func() {
			cx := &export.Ctx{Doc: e.Doc, Libs: e.Libs, Fonts: e.Fonts, Assets: e.Assets, Runs: rec}
			out.tree, out.err = cx.NodeTreeAt(id, scale)
		})
		return out
	})
	if b.err != nil {
		return nil, b.err
	}
	png, err := export.EncodeTree(b.tree, "png", scale, 0)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.EncodeToString(png), nil
}

// dispatch runs one UI command (lib.rs `dispatch`: the engine core's and the inline commands, and those added
// with Handle), taking the edit gate and the engine lock as Rust does.
func (e *Engine) dispatch(req jsonv.Value) (jsonv.Value, error) {
	cmd := jsonv.StrOr(jsonv.Get(req, "cmd"))
	if slices.Contains(gated, cmd) {
		defer e.EditGate()()
	}
	if cmd == "png" {
		return e.png(req)
	}
	if cmd == "tool" {
		// The tool runs and finishes without the lock held (lib.rs: `call_tool`, then `finish`).
		out := e.CallTool(jsonv.StrOr(jsonv.Get(req, "name")), jsonv.Get(req, "args")).Finish(e)
		return Object("text", out.Text, "error", out.Error, "images", jsonv.Int(int64(len(out.Images)))), nil
	}
	e.Lock()
	defer e.Unlock()
	arg := func(k string) jsonv.Value { return jsonv.Get(req, k) }
	num := func(k string) float64 { f, _ := jsonv.F64(arg(k)); return f }
	switch cmd {
	case "layers":
		return e.Layers(), nil
	case "frames":
		e.Sync()
		out := jsonv.NewArray()
		out.Items = []jsonv.Value{}
		for _, id := range e.Renderer.Order {
			if f := e.Renderer.Find(id); f != nil {
				out.Items = append(out.Items, FlatJSON(f))
			}
		}
		return out, nil
	case "hit":
		e.Sync()
		return Strings(render.HitChain(&e.Renderer, num("x"), num("y"))), nil
	case "boxes":
		e.Sync()
		out := jsonv.NewArray()
		out.Items = []jsonv.Value{}
		for _, id := range IDsArg(arg("ids")) {
			if f := e.Renderer.Find(id); f != nil {
				out.Items = append(out.Items, FlatJSON(f))
			}
		}
		return out, nil
	case "children_boxes":
		e.Sync()
		parent := jsonv.StrOr(arg("id"))
		out := jsonv.NewArray()
		out.Items = []jsonv.Value{}
		for _, f := range e.Renderer.Flats() {
			if f.HasParent && f.Parent == parent {
				out.Items = append(out.Items, FlatJSON(f))
			}
		}
		return out, nil
	case "node":
		id, ok := jsonv.Str(arg("id"))
		if !ok {
			return nil, errors.New("id required")
		}
		return e.NodeInfo(id), nil
	case "reorder":
		var targets []string
		if jsonv.Arr(arg("ids")) != nil {
			targets = IDsArg(arg("ids"))
		} else {
			targets = IDsArg(arg("id"))
		}
		dir, ok := jsonv.Str(arg("dir"))
		if !ok {
			dir = "up"
		}
		if err := e.Reorder(targets, dir); err != nil {
			return nil, err
		}
		return e.State(), nil
	case "live":
		e.Sync()
		// `clip` is the canvas box its clipping ancestors leave visible; the overlay must not paint outside it. `cuts` are
		// those ancestors that round their corners (box `x y w h` and radii `r`), each of which cuts it too. `radius` is the
		// node's own [tl, tr, br, bl] (an ellipse is round by its type).
		out := jsonv.NewArray()
		out.Items = []jsonv.Value{}
		for _, l := range e.Renderer.Live() {
			v := FlatJSON(l.Flat)
			v.Set("radius", floats(l.Flat.Radius[:]))
			var clip jsonv.Value
			if l.HasClip {
				clip = rectJSON(l.Clip)
			}
			v.Set("clip", clip)
			cuts := jsonv.NewArray()
			cuts.Items = []jsonv.Value{}
			for _, c := range l.Cuts {
				cut := rectJSON(c.Box)
				cut.Set("r", floats(c.Radius[:]))
				cuts.Items = append(cuts.Items, cut)
			}
			v.Set("cuts", cuts)
			out.Items = append(out.Items, v)
		}
		return out, nil
	case "font_state":
		family, ok := jsonv.Str(arg("family"))
		if !ok {
			return nil, errors.New("family required")
		}
		return Object("available", e.Fonts.HasFamily(family), "loading", e.Fonts.Loading(family)), nil
	case "poll":
		e.ExpireSlides()
		return Object("state", e.State(), "events", jsonv.NewArray(append([]jsonv.Value{}, e.TakeEvents()...)...)), nil
	case "state":
		return e.State(), nil
	case "new":
		if e.SwitchBusyNow() {
			return nil, errors.New(SwitchBusy)
		}
		t, ok := jsonv.Str(arg("template"))
		if !ok {
			t = "blank"
		}
		e.LoadTemplate(t)
		return e.State(), nil
	case "open":
		if e.SwitchBusyNow() {
			return nil, errors.New(SwitchBusy)
		}
		p, ok := jsonv.Str(arg("path"))
		if !ok {
			return nil, errors.New("path required")
		}
		if err := e.Open(p); err != nil {
			return nil, err
		}
		return e.State(), nil
	case "save":
		p, err := e.Save(jsonv.StrOr(arg("path")))
		if err != nil {
			return nil, err
		}
		return Object("path", p, "state", e.State()), nil
	case "select":
		e.Selection = IDsArg(arg("ids"))
		return e.State(), nil
	case "update":
		targets := IDsArg(arg("ids"))
		props := arg("props")
		apply := func(d *doc.Doc) error {
			items := make([]doc.UpdateItem, len(targets))
			for i, id := range targets {
				items[i] = doc.UpdateItem{Path: id, Data: props}
			}
			return d.UpdateAll(items)
		}
		var err error
		if key, ok := jsonv.Str(arg("coalesce")); ok {
			err = e.EditCoalesced(key, apply)
		} else {
			err = e.EditOrDrag(apply)
		}
		if err != nil {
			return nil, err
		}
		return e.State(), nil
	case "insert":
		parent, ok := jsonv.Str(arg("parent"))
		if !ok {
			parent = doc.Root
		}
		node := jsonv.Clone(arg("node"))
		var id string
		if err := e.Edit(func(d *doc.Doc) (err error) {
			id, err = d.Insert(parent, node, -1)
			return err
		}); err != nil {
			return nil, err
		}
		e.Selection = []string{id}
		return Object("id", id, "state", e.State()), nil
	case "delete":
		// A selected descendant of a selected layer goes with it; deleting it separately would fail.
		targets := e.Doc.Outermost(IDsArg(arg("ids")))
		if err := e.EditOrDrag(func(d *doc.Doc) error { return d.DeleteAll(targets) }); err != nil {
			return nil, err
		}
		return e.State(), nil
	case "drag_begin":
		e.DragBefore = cloneRoot(e.Doc.Root)
		e.DragClean = !e.Dirty()
		return true, nil
	case "drag_end":
		if before := e.DragBefore; before != nil {
			e.DragBefore = nil
			e.EndDrag(before, e.DragClean)
		}
		return e.State(), nil
	case "undo":
		e.Undo()
		return e.State(), nil
	case "redo":
		e.Redo()
		return e.State(), nil
	case "variables":
		return e.Doc.Variables(), nil
	case "set_variable":
		name, ok := jsonv.Str(arg("name"))
		if !ok {
			return nil, errors.New("name required")
		}
		vars := Object(name, jsonv.Clone(arg("def")))
		if err := e.Edit(func(d *doc.Doc) error { return d.SetVariables(vars, false) }); err != nil {
			return nil, err
		}
		return e.State(), nil
	case "settings_get":
		return e.SettingsView(), nil
	case "settings_set":
		key, ok := jsonv.Str(arg("key"))
		if !ok {
			return nil, errors.New("key required")
		}
		if err := e.SetSetting(key, jsonv.Clone(arg("value"))); err != nil {
			return nil, err
		}
		return true, nil
	case "reduce_motion":
		e.ReduceMotion = jsonv.IsTrue(arg("on"))
		return true, nil
	case "viewport":
		g := func(k string) float64 { f, _ := jsonv.F64(arg(k)); return f }
		e.Viewport = &[4]float64{g("x"), g("y"), g("w"), g("h")}
		return true, nil
	}
	if f := commands[cmd]; f != nil {
		return f(e, req)
	}
	return nil, fmt.Errorf("unknown command '%s'", cmd)
}

// Replayer runs replay scripts on an engine as `nalarupa-cli replay` does (see replay in core/src/bin/cli.rs):
// one `{"cmd", ...}` request per line, answered as `nr_call` answers it, with the slide clock pinned. Besides
// the engine-core commands of `dispatch`, test-only steps reach what the UI cannot: `render` (the RGB bytes go
// to Frames), `slide_in`, `tick`, `settle`, `font_ready`, `asset_ready`, `inflight`, `resolve_pending`,
// `apply_generated`, `doc` and `copy` with `from` (a file; without `from`, `copy` is the clipboard command). A
// `tool` step with `png: true` answers with the names of its screenshots (`<step>.<k>.png`), which go to Shots.
type Replayer struct {
	E *Engine
	// Frames receives each render step's RGB bytes by step index.
	Frames func(step int, rgb []byte)
	// Shots receives the k-th screenshot (PNG) of a `tool` step with `png: true`.
	Shots func(step, k int, png []byte)
	now   time.Time
	step  int
}

// NewReplayer pins e's slide clock and live-zoom estimate for a replay. Make e offline first, as the CLI is.
func NewReplayer(e *Engine) *Replayer {
	r := &Replayer{E: e, now: time.Now()}
	e.Locked(func() { e.Renderer.Now = func() time.Time { return r.now } })
	return r
}

// Line runs one script line: blank lines and `#` comments give nil, anything else its response line.
func (r *Replayer) Line(line string) (*jsonv.Object, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil, nil
	}
	req, err := jsonv.Parse([]byte(line))
	if err != nil {
		return nil, fmt.Errorf("step %d: %v", r.step, err)
	}
	v, err := r.run(req)
	r.step++
	if err != nil {
		return Object("ok", false, "error", err.Error()), nil
	}
	return Object("ok", true, "data", v), nil
}

// run is one step, a panic answered as `nr_call` answers it.
func (r *Replayer) run(req jsonv.Value) (v jsonv.Value, err error) {
	defer func() {
		if recover() != nil {
			v, err = nil, errors.New("internal error (panic)")
		}
	}()
	return r.step1(req)
}

func (r *Replayer) slides() *jsonv.Array {
	out := jsonv.NewArray()
	out.Items = []jsonv.Value{}
	for _, s := range r.E.Renderer.Slides {
		dx, dy, a := s.State(r.now)
		out.Items = append(out.Items, Object("id", s.ID, "state", jsonv.NewArray(jsonv.Float(dx), jsonv.Float(dy), jsonv.Float(a))))
	}
	return out
}

func (r *Replayer) version() *jsonv.Object {
	return Object("version", u64(r.E.Version), "dirty", r.E.Dirty())
}

func (r *Replayer) step1(req jsonv.Value) (jsonv.Value, error) {
	e := r.E
	num := func(k string, d float64) float64 {
		if f, ok := jsonv.F64(jsonv.Get(req, k)); ok {
			return f
		}
		return d
	}
	s := func(k string) string { return jsonv.StrOr(jsonv.Get(req, k)) }
	switch s("cmd") {
	case "render":
		e.Lock()
		defer e.Unlock()
		w, h := uint32(num("w", 800)), uint32(num("h", 600))
		bg := [3]uint8{255, 255, 255}
		if c := jsonv.Arr(jsonv.Get(req, "bg")); c != nil {
			for k := range 3 {
				bg[k] = 0
				if k < len(c.Items) {
					if n, ok := c.Items[k].(jsonv.Number); ok {
						if u, ok := n.Uint64(); ok {
							bg[k] = uint8(u)
						}
					}
				}
			}
		}
		e.Renderer.MsPerMP = 0
		if jsonv.IsTrue(jsonv.Get(req, "slow")) {
			e.Renderer.MsPerMP = 1e9
		}
		cam := func(k int) float64 {
			if a := jsonv.Arr(jsonv.Get(req, "cam")); a != nil && k < len(a.Items) {
				f, _ := jsonv.F64(a.Items[k])
				return f
			}
			return 0
		}
		px := e.Render(w, h, cam(0), cam(1), num("zoom", 1), bg, s("solo"), !jsonv.IsFalse(jsonv.Get(req, "sharp")))
		rgb := make([]byte, 0, len(px)/4*3)
		for p := 0; p+3 < len(px); p += 4 {
			rgb = append(rgb, px[p], px[p+1], px[p+2])
		}
		if r.Frames != nil {
			r.Frames(r.step, rgb)
		}
		return Object("width", jsonv.Int(int64(min(max(w, 1), 8192))), "height", jsonv.Int(int64(min(max(h, 1), 8192))),
			"file", fmt.Sprintf("%d.rgb", r.step), "slides", r.slides()), nil
	case "slide_in":
		e.Lock()
		defer e.Unlock()
		e.SlideIn(s("id"))
		return r.slides(), nil
	case "tick":
		e.Lock()
		defer e.Unlock()
		n, _ := jsonv.Get(req, "ms").(jsonv.Number)
		ms, _ := n.Uint64()
		r.now = r.now.Add(time.Duration(ms) * time.Millisecond)
		e.ExpireSlides()
		return r.slides(), nil
	case "settle":
		until := time.Now().Add(30 * time.Second)
		for script.Busy() {
			if time.Now().After(until) {
				return nil, errors.New("script nodes still running")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return true, nil
	case "font_ready":
		e.Lock()
		defer e.Unlock()
		e.FontReady(s("family"), uint16(num("weight", 400)), jsonv.IsTrue(jsonv.Get(req, "italic")))
		return r.version(), nil
	case "asset_ready":
		e.Lock()
		defer e.Unlock()
		e.AssetReady(s("key"), !jsonv.IsFalse(jsonv.Get(req, "ok")))
		return r.version(), nil
	case "inflight":
		e.Lock()
		defer e.Unlock()
		e.Inflight[s("url")] = true
		return true, nil
	case "resolve_pending":
		e.Lock()
		defer e.Unlock()
		e.ResolvePending(s("pending"), s("url"))
		return r.version(), nil
	case "apply_generated":
		e.Lock()
		defer e.Unlock()
		props := jsonv.Obj(jsonv.Clone(jsonv.Get(req, "props")))
		if props == nil {
			props = jsonv.NewObject()
		}
		hit := e.ApplyGenerated(s("id"), props, jsonv.IsTrue(jsonv.Get(req, "done")))
		v := r.version()
		v.Set("hit", hit)
		return v, nil
	case "doc":
		e.Lock()
		defer e.Unlock()
		return jsonv.Clone(e.Doc.Root), nil
	case "tool":
		if !jsonv.IsTrue(jsonv.Get(req, "png")) {
			break
		}
		out := e.CallTool(s("name"), jsonv.Get(req, "args")).Finish(e)
		files := []string{}
		for k, b64 := range out.Images {
			png, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, err
			}
			if r.Shots != nil {
				r.Shots(r.step, k, png)
			}
			files = append(files, fmt.Sprintf("%d.%d.png", r.step, k))
		}
		return Object("text", out.Text, "error", out.Error, "images", Strings(files)), nil
	case "copy":
		// With `from` it copies a file; without, it is the clipboard command.
		if o := jsonv.Obj(req); o == nil || !o.Has("from") {
			break
		}
		path := func(p string) string {
			if rest, ok := strings.CutPrefix(p, "@data/"); ok {
				return doc.JoinPath(e.DataDir, rest)
			}
			return p
		}
		to := path(s("to"))
		if dir, ok := doc.ParentPath(to); ok && dir != "" {
			if err := doc.CreateDirAll(dir); err != nil {
				return nil, errors.New(doc.IOError(err))
			}
		}
		if err := copyFile(path(s("from")), to); err != nil {
			return nil, errors.New(doc.IOError(err))
		}
		return true, nil
	}
	return e.dispatch(req)
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(filepath.Clean(to))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Final is the replay's last line: the document and where it stands.
func (r *Replayer) Final() *jsonv.Object {
	e := r.E
	e.Lock()
	defer e.Unlock()
	var path jsonv.Value
	if e.Doc.Path != "" {
		path = e.Doc.Path
	}
	return Object("doc", jsonv.Clone(e.Doc.Root), "path", path, "version", u64(e.Version), "dirty", e.Dirty(), "selection", Strings(e.Selection))
}
