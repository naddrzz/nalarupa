package engine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script"
)

// gated are the commands that take the edit gate (lib.rs `dispatch`).
var gated = []string{
	"update", "move_by", "insert", "delete", "duplicate", "reorder", "reparent", "wrap", "unwrap", "add_layout", "set_layout",
	"remove_layout", "align", "paste", "path_write", "drag_begin", "drag_end", "undo", "redo", "set_variable", "save",
}

// ids is lib.rs `ids`: an array's strings, or one string.
func ids(v jsonv.Value) []string {
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

func strs(a []string) *jsonv.Array {
	out := jsonv.NewArray()
	for _, s := range a {
		out.Items = append(out.Items, s)
	}
	if out.Items == nil {
		out.Items = []jsonv.Value{}
	}
	return out
}

func obj(kv ...any) *jsonv.Object {
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
	return obj("version", u64(e.Version), "title", e.Title(), "path", path, "dirty", e.Dirty(), "selection", strs(e.Selection),
		"agentRunning", e.Agent.Running, "executing", e.Executing, "penDrawing", len(e.Agent.Pens) > 0, "mcpPort", u64(uint64(e.MCPPort)),
		"animating", len(e.Renderer.Slides) > 0 || script.Busy())
}

// dispatch runs one UI command of the engine core (lib.rs `dispatch`, the commands phase 8a owns), taking the
// edit gate and the engine lock as Rust does.
func (e *Engine) dispatch(req jsonv.Value) (jsonv.Value, error) {
	cmd := jsonv.StrOr(jsonv.Get(req, "cmd"))
	if slices.Contains(gated, cmd) {
		defer e.EditGate()()
	}
	e.Lock()
	defer e.Unlock()
	arg := func(k string) jsonv.Value { return jsonv.Get(req, k) }
	switch cmd {
	case "poll":
		e.ExpireSlides()
		return obj("state", e.State(), "events", jsonv.NewArray(append([]jsonv.Value{}, e.TakeEvents()...)...)), nil
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
		return obj("path", p, "state", e.State()), nil
	case "select":
		e.Selection = ids(arg("ids"))
		return e.State(), nil
	case "update":
		targets := ids(arg("ids"))
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
		return obj("id", id, "state", e.State()), nil
	case "delete":
		// A selected descendant of a selected layer goes with it; deleting it separately would fail.
		targets := e.Doc.Outermost(ids(arg("ids")))
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
	case "tool":
		out := e.Tool(jsonv.StrOr(arg("name")), arg("args"))
		return obj("text", out.Text, "error", out.Error, "images", jsonv.Int(int64(len(out.Images)))), nil
	case "variables":
		return e.Doc.Variables(), nil
	case "set_variable":
		name, ok := jsonv.Str(arg("name"))
		if !ok {
			return nil, errors.New("name required")
		}
		vars := obj(name, jsonv.Clone(arg("def")))
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
	return nil, fmt.Errorf("unknown command '%s'", cmd)
}

// Replayer runs replay scripts on an engine as `nalarupa-cli replay` does (see replay in core/src/bin/cli.rs):
// one `{"cmd", ...}` request per line, answered as `nr_call` answers it, with the slide clock pinned. Besides
// the engine-core commands of `dispatch`, test-only steps reach what the UI cannot: `render` (the RGB bytes go
// to Frames), `slide_in`, `tick`, `settle`, `font_ready`, `asset_ready`, `inflight`, `resolve_pending`,
// `apply_generated`, `doc` and `copy`.
type Replayer struct {
	E *Engine
	// Frames receives each render step's RGB bytes by step index.
	Frames func(step int, rgb []byte)
	now    time.Time
	step   int
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
		return obj("ok", false, "error", err.Error()), nil
	}
	return obj("ok", true, "data", v), nil
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
		out.Items = append(out.Items, obj("id", s.ID, "state", jsonv.NewArray(jsonv.Float(dx), jsonv.Float(dy), jsonv.Float(a))))
	}
	return out
}

func (r *Replayer) version() *jsonv.Object {
	return obj("version", u64(r.E.Version), "dirty", r.E.Dirty())
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
		return obj("width", jsonv.Int(int64(min(max(w, 1), 8192))), "height", jsonv.Int(int64(min(max(h, 1), 8192))),
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
	case "copy":
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
	return obj("doc", jsonv.Clone(e.Doc.Root), "path", path, "version", u64(e.Version), "dirty", e.Dirty(), "selection", strs(e.Selection))
}
