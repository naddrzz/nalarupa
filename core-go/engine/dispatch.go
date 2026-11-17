package engine

import (
	"errors"
	"fmt"
	"slices"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/model"
	"github.com/naddrzz/nalarupa/core-go/render"
)

// gated are the commands that take the edit gate (lib.rs `dispatch`).
var gated = []string{
	"update", "move_by", "insert", "delete", "duplicate", "reorder", "reparent", "wrap", "unwrap", "add_layout", "set_layout",
	"remove_layout", "align", "paste", "path_write", "drag_begin", "drag_end", "undo", "redo", "set_variable", "save",
}

// Dispatch runs one UI command (lib.rs `dispatch`: the engine core's and the inline commands, and those added
// with Handle), taking the edit gate and the engine lock as Rust does. `nr_call` and the replay answer with it.
func (e *Engine) Dispatch(req jsonv.Value) (jsonv.Value, error) {
	cmd := jsonv.StrOr(jsonv.Get(req, "cmd"))
	if slices.Contains(gated, cmd) {
		defer e.EditGate()()
	}
	arg := func(k string) jsonv.Value { return jsonv.Get(req, k) }
	switch {
	case cmd == "png":
		return e.png(req)
	case cmd == "tool":
		// The tool runs and finishes without the lock held (lib.rs: `call_tool`, then `finish`).
		out := e.CallTool(jsonv.StrOr(arg("name")), arg("args")).Finish(e)
		return Object("text", out.Text, "error", out.Error, "images", jsonv.Int(int64(len(out.Images)))), nil
	case cmd == "export":
		format, ok := jsonv.Str(arg("format"))
		if !ok {
			format = "png"
		}
		files, err := e.Export(IDsArg(arg("ids")), format, jsonv.StrOr(arg("path")), arg("opts"))
		if err != nil {
			return nil, err
		}
		return Strings(files), nil
	case cmd == "provider_test":
		// One tiny request in the background (a slow provider must not freeze the editor); the result arrives as a
		// `provider_test` event: {id, ok, model, reply} or {id, ok: false, error}.
		id, ok := jsonv.Str(arg("id"))
		if !ok {
			return nil, errors.New("id required")
		}
		var client model.Provider
		var name string
		var err error
		e.Locked(func() { client, name, err = e.ProviderTest(id) })
		if err != nil {
			return nil, err
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					e.Locked(func() {
						e.Emit(Object("type", "provider_test", "id", id, "ok", false, "model", name, "error", fmt.Sprintf("internal error (panic): %v", r)))
					})
				}
			}()
			ev := model.Test(client, id, name)
			e.Locked(func() { e.Emit(ev) })
		}()
		return true, nil
	case cmd == "init":
		var start func() (uint16, error)
		e.Locked(func() {
			if e.MCPPort == 0 {
				start = e.Hooks.StartMCP
			}
		})
		if start != nil {
			if port, err := start(); err == nil {
				e.Locked(func() { e.MCPPort = port })
			}
		}
	case unlocked[cmd] != nil:
		return unlocked[cmd](e, req)
	}
	e.Lock()
	defer e.Unlock()
	num := func(k string) float64 { f, _ := jsonv.F64(arg(k)); return f }
	switch cmd {
	case "init":
		recent, ok := e.Settings.Get("recent")
		if !ok {
			recent = jsonv.NewArray()
		}
		_, hasKey := e.APIKey()
		return Object("mcpPort", u64(uint64(e.MCPPort)), "mcpUrl", fmt.Sprintf("http://127.0.0.1:%d/mcp", e.MCPPort), "dataDir", e.DataDir,
			"state", e.State(), "recent", jsonv.Clone(recent), "hasApiKey", hasKey), nil
	case "provider_save":
		id, err := e.SaveProvider(arg("provider"))
		if err != nil {
			return nil, err
		}
		return id, nil
	case "provider_remove":
		id, ok := jsonv.Str(arg("id"))
		if !ok {
			return nil, errors.New("id required")
		}
		if err := e.RemoveProvider(id); err != nil {
			return nil, err
		}
		return true, nil
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
