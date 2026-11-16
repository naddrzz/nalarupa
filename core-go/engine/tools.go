package engine

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/guide"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// RustF64 is how Rust's `{}` writes an f64: the shortest digits that read back, never an exponent.
func RustF64(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// numText is how serde_json displays a number: an integer as written, a float as its shortest round trip.
func numText(n jsonv.Number) string {
	if !n.IsFloat() {
		if i, ok := n.Int64(); ok {
			return strconv.FormatInt(i, 10)
		}
		if u, ok := n.Uint64(); ok {
			return strconv.FormatUint(u, 10)
		}
	}
	f, _ := n.Float64()
	return jsonv.FormatFloat(f)
}

// roundU64 is Rust's `f.round() as u64`: saturating, NaN as 0.
func roundU64(f float64) uint64 {
	r := math.Round(f)
	switch {
	case !(r > 0):
		return 0
	case r >= math.MaxUint64:
		return math.MaxUint64
	}
	return uint64(r)
}

// EditorState is the Markdown summary the agent reads before editing (`get_editor_state`).
func (e *Engine) EditorState() string {
	e.Sync()
	var s strings.Builder
	s.WriteString("# Nalarupa editor state\n\n")
	path := "Untitled (not saved yet)"
	if e.Doc.Path != "" {
		path = e.Doc.Path
	}
	unsaved := ""
	if e.Dirty() {
		unsaved = " (unsaved changes)"
	}
	fmt.Fprintf(&s, "- Document: %s%s\n", path, unsaved)
	var sel []string
	for _, id := range e.Selection[:min(len(e.Selection), 40)] {
		if f := e.Renderer.Find(id); f != nil {
			sel = append(sel, fmt.Sprintf("`%s` \"%s\" %s %sx%s", id, f.Name, f.Type, RustF64(math.Round(f.W)), RustF64(math.Round(f.H))))
		} else {
			sel = append(sel, "`"+id+"`")
		}
	}
	if len(e.Selection) > 40 {
		sel = append(sel, fmt.Sprintf("... +%d others", len(e.Selection)-40))
	}
	selText := "nothing"
	if len(sel) > 0 {
		selText = strings.Join(sel, ", ")
	}
	fmt.Fprintf(&s, "- Selection: %s\n", selText)
	type top struct {
		visible bool
		line    string
	}
	var tops []top
	vp := e.Viewport
	for _, n := range e.Doc.Children() {
		id, ok := jsonv.Str(jsonv.Get(n, "id"))
		if !ok {
			continue
		}
		f := e.Renderer.Find(id)
		if f == nil {
			continue
		}
		visible := vp == nil || f.X < vp[0]+vp[2] && f.Y < vp[1]+vp[3] && f.X+f.W > vp[0] && f.Y+f.H > vp[1]
		comp, where := "", ""
		if jsonv.IsTrue(jsonv.Get(n, "reusable")) {
			comp = " [component]"
		}
		if vp != nil {
			where = " [outside viewport]"
			if visible {
				where = " [user visible]"
			}
		}
		r := func(v float64) string { return RustF64(math.Round(v)) }
		tops = append(tops, top{visible, fmt.Sprintf("  - `%s` \"%s\" %s at (%s, %s) size %sx%s%s%s", id, f.Name, f.Type, r(f.X), r(f.Y), r(f.W), r(f.H), comp, where)})
	}
	slices.SortStableFunc(tops, func(a, b top) int {
		switch {
		case a.visible == b.visible:
			return 0
		case a.visible:
			return -1
		}
		return 1
	})
	more := max(len(tops)-40, 0)
	var lines []string
	for _, t := range tops[:min(len(tops), 40)] {
		lines = append(lines, t.line)
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("  - ... +%d others", more))
	}
	fmt.Fprintf(&s, "- Top-level nodes (%d):\n%s\n", len(e.Doc.Children()), strings.Join(lines, "\n"))
	var comps []string
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
		if jsonv.IsTrue(jsonv.Get(n, "reusable")) && len(comps) < 80 {
			comps = append(comps, fmt.Sprintf("`%s` \"%s\"", jsonv.StrOr(jsonv.Get(n, "id")), jsonv.StrOr(jsonv.Get(n, "name"))))
		}
	})
	if len(comps) > 0 {
		fmt.Fprintf(&s, "- Components: %s\n", strings.Join(comps, ", "))
	}
	// The existing style, so the agent can post its direction from the prompt alone, before any read-only look:
	// the variables with their values (a themed one shows its first value) and the fonts in use with their sizes.
	vars := jsonv.Obj(jsonv.Get(e.Doc.Root, "variables"))
	themes := ""
	if t := jsonv.Obj(jsonv.Get(e.Doc.Root, "themes")); t != nil {
		themes = strings.Join(t.Keys(), ", ")
	}
	var values []string
	nvars := 0
	if vars != nil {
		nvars = vars.Len()
		vars.Each(func(name string, v jsonv.Value) {
			if len(values) >= 30 {
				return
			}
			value := jsonv.Get(v, "value")
			if a := jsonv.Arr(value); a != nil {
				// A themed variable shows its base (un-themed) entry, wherever it sits, as the resolver applies it.
				var pick jsonv.Value
				found := false
				for _, x := range a.Items {
					if t := jsonv.Obj(jsonv.Get(x, "theme")); t == nil || t.Len() == 0 {
						pick, found = x, true
						break
					}
				}
				if !found && len(a.Items) > 0 {
					pick, found = a.Items[0], true
				}
				value = nil
				if found {
					value = jsonv.Get(pick, "value")
				}
			}
			switch x := value.(type) {
			case string:
				values = append(values, name+" "+x)
			case jsonv.Number:
				values = append(values, name+" "+numText(x))
			case bool:
				values = append(values, name+" "+strconv.FormatBool(x))
			default:
				values = append(values, name)
			}
		})
	}
	if nvars > 30 {
		values = append(values, fmt.Sprintf("... +%d others", nvars-30))
	}
	axes, list := "", ""
	if themes != "" {
		axes = " (theme axes: " + themes + ")"
	}
	if len(values) > 0 {
		list = ": " + strings.Join(values, ", ")
	}
	fmt.Fprintf(&s, "- Variables: %d%s%s\n", nvars, axes, list)
	type family struct {
		name  string
		sizes []uint64
	}
	var fonts []family
	moreFonts := 0
	doc.Walk(e.Doc.Children(), func(n jsonv.Value) {
		name, ok := jsonv.Str(jsonv.Get(n, "fontFamily"))
		if !ok || name == "" {
			return
		}
		f, hasSize := jsonv.F64(jsonv.Get(n, "fontSize"))
		size := roundU64(f)
		if i := slices.IndexFunc(fonts, func(x family) bool { return x.name == name }); i >= 0 {
			if hasSize && !slices.Contains(fonts[i].sizes, size) {
				fonts[i].sizes = append(fonts[i].sizes, size)
			}
		} else if len(fonts) < 12 {
			fam := family{name: name}
			if hasSize {
				fam.sizes = []uint64{size}
			}
			fonts = append(fonts, fam)
		} else {
			moreFonts++
		}
	})
	if len(fonts) > 0 {
		var list []string
		for _, f := range fonts {
			slices.SortFunc(f.sizes, func(a, b uint64) int {
				switch {
				case a > b:
					return -1
				case a < b:
					return 1
				}
				return 0
			})
			f.sizes = f.sizes[:min(len(f.sizes), 8)]
			if len(f.sizes) == 0 {
				list = append(list, f.name)
				continue
			}
			sizes := make([]string, len(f.sizes))
			for i, sz := range f.sizes {
				sizes[i] = strconv.FormatUint(sz, 10)
			}
			list = append(list, f.name+" ("+strings.Join(sizes, ", ")+")")
		}
		if moreFonts > 0 {
			list = append(list, fmt.Sprintf("... +%d other families", moreFonts))
		}
		fmt.Fprintf(&s, "- Fonts in use: %s\n", strings.Join(list, "; "))
	}
	if b, ok := e.Renderer.Bounds(); ok {
		r := func(v float64) string { return RustF64(math.Round(v)) }
		fmt.Fprintf(&s, "- Canvas content bounds: x %s y %s w %s h %s\n", r(b.X), r(b.Y), r(b.W), r(b.H))
	}
	s.WriteString("\nCall `read_guide` (no arguments) before your first `execute`; it explains the .pen schema and the execute API.\n")
	return s.String()
}

// Tool dispatches one agent tool call that runs under the engine lock (shared by MCP and the built-in agent).
// `execute` comes with phase 8c.
func (e *Engine) Tool(name string, args jsonv.Value) ToolOut {
	switch name {
	case "get_editor_state":
		return ok(e.EditorState())
	case "read_guide":
		text, failed := guide.Read(jsonv.StrOr(jsonv.Get(args, "topic")))
		return ToolOut{Text: text, Error: failed}
	case "get_style":
		text, failed := guide.Directions(jsonv.StrOr(jsonv.Get(args, "name")))
		return ToolOut{Text: text, Error: failed}
	case "open_document":
		if e.SwitchBusyNow() {
			return bad(SwitchBusy)
		}
		if e.Dirty() {
			return bad(SwitchDirty)
		}
		path, has := jsonv.Str(jsonv.Get(args, "path"))
		if !has {
			return bad("path is required")
		}
		// A missing file is created on disk first, so a failed create leaves the open document alone.
		var err error
		if !exists(path) {
			err = doc.New().Save(path)
		}
		if err == nil {
			err = e.Open(path)
		}
		if err != nil {
			return bad(err.Error())
		}
		ev := jsonv.NewObject()
		ev.Set("type", "document_opened")
		e.Emit(ev)
		return ok("Opened " + path)
	}
	return bad("unknown tool: " + name)
}

// ToolSpecs is the tool schemas advertised over MCP and to the built-in agent.
func ToolSpecs() jsonv.Value { return jsonv.MustParse(toolSpecs) }

const toolSpecs = `[
{"name": "get_editor_state", "description": "Summary of the open Nalarupa document: path, selection, top-level frames, components, variables. Call this first.",
 "inputSchema": {"type": "object", "properties": {}}},
{"name": "read_guide", "description": "Read Nalarupa's design guide. No topic returns the overview and the list of topics (designing, schema, execute, components, slides, design, code).",
 "inputSchema": {"type": "object", "properties": {"topic": {"type": "string", "description": "Guide topic, e.g. 'schema', 'execute', 'slides'"}}}},
{"name": "get_style", "description": "Worked examples of design directions (framing, concept, palette and type with roles, layout, signature move), each grown from a sample brief. No name lists them.",
 "inputSchema": {"type": "object", "properties": {"name": {"type": "string"}}}},
{"name": "execute", "description": "Run a JavaScript snippet against the canvas (Insert/Update/Copy/Replace/Move/Delete/Get/SetVariables/TakeScreenshot/Export...). All operations in one call are a single undoable transaction; on error everything is rolled back. Read the 'execute' guide first. To fix a failed call, send ` + "`editId` with `edits`" + ` instead of resending the snippet.",
 "inputSchema": {"type": "object", "properties": {
    "input": {"type": "string", "description": "JavaScript source"},
    "editId": {"type": "string", "description": "editId from a failed call, used with edits"},
    "edits": {"type": "array", "items": {"type": "object", "properties": {"find": {"type": "string"}, "replace": {"type": "string"}, "all": {"type": "boolean"}}, "required": ["find", "replace"]}}
 }}},
{"name": "open_document", "description": "Open a .pen/.nala file in the editor, or create it when it does not exist.",
 "inputSchema": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}}
]`
