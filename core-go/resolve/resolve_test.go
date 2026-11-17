package resolve

import (
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

var j = jsonv.MustParse

func child(root jsonv.Value, i int) jsonv.Value {
	return jsonv.Items(jsonv.Get(root, "children"))[i]
}

func prop(t *testing.T, n *RNode, k string) jsonv.Value {
	t.Helper()
	v, ok := n.P.Get(k)
	if !ok {
		t.Fatalf("%s has no %s", n.ID, k)
	}
	return v
}

func wantStr(t *testing.T, got jsonv.Value, want string) {
	t.Helper()
	if !jsonv.StrIs(got, want) {
		t.Fatalf("%s, want %q", jsonv.Marshal(got), want)
	}
}

func wantF64(t *testing.T, got jsonv.Value, want float64) {
	t.Helper()
	if f, ok := jsonv.F64(got); !ok || f != want {
		t.Fatalf("%s, want %v", jsonv.Marshal(got), want)
	}
}

func TestHasPathReachesHiddenInstanceDescendants(t *testing.T) {
	root := j(`{"children": [
		{"type": "frame", "id": "C", "reusable": true, "children": [{"type": "frame", "id": "row", "children": [{"type": "text", "id": "t"}]}, {"type": "ref", "id": "n", "ref": "D"}]},
		{"type": "frame", "id": "D", "reusable": true, "children": [{"type": "rectangle", "id": "r"}]},
		{"type": "ref", "id": "i", "ref": "C", "enabled": false, "descendants": {"t": {"enabled": false}}}]}`)
	r := New(root, nil)
	for _, p := range []string{"i", "i/t", "i/row", "i/n", "i/n/r", "C", "row"} {
		if !r.HasPath(p) {
			t.Error(p)
		}
	}
	for _, p := range []string{"nope", "i/nope", "i/n/t", "i/t/x", "row/t"} {
		if r.HasPath(p) {
			t.Error(p)
		}
	}
}

func TestInstancesAndThemes(t *testing.T) {
	root := j(`{
		"themes": {"mode": ["light", "dark"]},
		"variables": {"fg": {"type": "color", "value": [{"value": "#111111"}, {"value": "#EEEEEE", "theme": {"mode": "dark"}}]}},
		"children": [
			{"type": "frame", "id": "btn", "reusable": true, "children": [{"type": "text", "id": "lbl", "content": "Go", "fill": "$fg"}]},
			{"type": "ref", "id": "b1", "ref": "btn", "theme": {"mode": "dark"}, "descendants": {"lbl": {"content": "Buy $49"}}}
		]}`)
	r := New(root, nil)
	inst := r.Resolve(child(root, 1))
	if inst.ID != "b1" || inst.Kids[0].ID != "b1/lbl" {
		t.Fatal(inst.ID, inst.Kids[0].ID)
	}
	wantStr(t, prop(t, inst.Kids[0], "content"), "Buy $49")
	wantStr(t, prop(t, inst.Kids[0], "fill"), "#EEEEEE")
	comp := r.Resolve(child(root, 0))
	wantStr(t, prop(t, comp.Kids[0], "fill"), "#111111")
}

func themedFrames(t *testing.T, value string) jsonv.Value {
	d := doc.New()
	if err := d.SetVariables(j(`{"bg": {"type": "color", "value": `+value+`}}`), false); err != nil {
		t.Fatal(err)
	}
	root := jsonv.Clone(d.Root).(*jsonv.Object)
	root.Set("children", j(`[{"type": "frame", "id": "f", "fill": "$bg"}, {"type": "frame", "id": "g", "fill": "$bg", "theme": {"mode": "dark"}}]`))
	return root
}

// A base value plus one themed override must keep the base as the default theme.
func TestBaseValueStaysDefaultWithOneOverride(t *testing.T) {
	root := themedFrames(t, `[{"value": "#ffffff"}, {"value": "#111111", "theme": {"mode": "dark"}}]`)
	r := New(root, nil)
	wantStr(t, prop(t, r.Resolve(child(root, 0)), "fill"), "#ffffff")
	wantStr(t, prop(t, r.Resolve(child(root, 1)), "fill"), "#111111")
}

// The un-themed base applies only when no themed entry matches, even if it is listed after the override.
func TestBaseValueAfterOverrideStaysDefault(t *testing.T) {
	root := themedFrames(t, `[{"value": "#111111", "theme": {"mode": "dark"}}, {"value": "#ffffff"}]`)
	r := New(root, nil)
	wantStr(t, prop(t, r.Resolve(child(root, 0)), "fill"), "#ffffff")
	wantStr(t, prop(t, r.Resolve(child(root, 1)), "fill"), "#111111")
}

// A component that is itself an instance (ref of a ref) expands through the whole chain, keeping each
// level's overrides.
func TestRefOfRefExpands(t *testing.T) {
	root := j(`{"children": [
		{"type": "frame", "id": "card", "reusable": true, "width": 100, "children": [{"type": "text", "id": "t", "content": "A"}]},
		{"type": "ref", "id": "v", "ref": "card", "reusable": true, "width": 80, "descendants": {"t": {"content": "B"}}},
		{"type": "ref", "id": "i", "ref": "v", "x": 5}]}`)
	r := New(root, nil)
	inst := r.Resolve(child(root, 2))
	if inst.Type != "frame" || inst.Inst != "v" {
		t.Fatal(inst.Type, inst.Inst)
	}
	wantF64(t, prop(t, inst, "width"), 80)
	wantF64(t, prop(t, inst, "x"), 5)
	if len(inst.Kids) != 1 || inst.Kids[0].ID != "i/t" {
		t.Fatal(len(inst.Kids))
	}
	wantStr(t, prop(t, inst.Kids[0], "content"), "B")
}

// What an instance inherits: the component's props through the whole ref chain, without the instance's
// own values or the children.
func TestInheritedPropsOfAnInstance(t *testing.T) {
	root := j(`{"children": [
		{"type": "frame", "id": "card", "reusable": true, "width": 100, "fill": "#336699", "cornerRadius": 8, "children": [{"type": "text", "id": "t"}]},
		{"type": "ref", "id": "v", "ref": "card", "reusable": true, "width": 80},
		{"type": "ref", "id": "i", "ref": "v", "x": 5, "fill": "#ff0000"}]}`)
	r := New(root, nil)
	got := r.Inherited(child(root, 2))
	wantStr(t, jsonv.Get(got, "fill"), "#336699")
	wantF64(t, jsonv.Get(got, "width"), 80)
	wantF64(t, jsonv.Get(got, "cornerRadius"), 8)
	if o := jsonv.Obj(got); o.Has("x") || o.Has("children") {
		t.Fatal(string(jsonv.Marshal(got)))
	}
	if r.Inherited(j(`{"type": "ref", "ref": "gone"}`)) != nil {
		t.Fatal("inherited from a missing component")
	}
}

// A library component's inherited `$variable` comes back as the color the instance paints: the
// library's value, under the component's theme or the instance's own.
func TestInheritedLibraryVariablesAreSettled(t *testing.T) {
	lib := j(`{
		"themes": {"mode": ["light", "dark"]},
		"variables": {"fg": {"type": "color", "value": [{"value": "#111111"}, {"value": "#eeeeee", "theme": {"mode": "dark"}}]}},
		"children": [{"type": "frame", "id": "btn", "reusable": true, "theme": {"mode": "dark"}, "fill": "$fg"}]}`)
	root := j(`{"variables": {"fg": {"type": "color", "value": "#0000ff"}}, "children": [
		{"type": "ref", "id": "a", "ref": "kit:btn"},
		{"type": "ref", "id": "b", "ref": "kit:btn", "theme": {"mode": "light"}}]}`)
	r := New(root, []Lib{{"kit", lib}})
	for i, want := range []string{"#eeeeee", "#111111"} {
		inst := child(root, i)
		wantStr(t, jsonv.Get(r.Inherited(inst), "fill"), want)
		wantStr(t, prop(t, r.Resolve(inst), "fill"), want)
	}
}

// An imported library resolves its own variables and its plain-id refs in its own scope, while the
// instance's values still use the open document's variables.
func TestLibraryScope(t *testing.T) {
	lib := j(`{
		"themes": {"mode": ["light", "dark"]},
		"variables": {"brand": {"type": "color", "value": [{"value": "#ff0000"}, {"value": "#00ff00", "theme": {"mode": "dark"}}]}},
		"children": [
			{"type": "frame", "id": "btn", "reusable": true, "fill": "$brand", "children": [{"type": "text", "id": "lbl", "content": "Kit"}]},
			{"type": "frame", "id": "card", "reusable": true, "children": [{"type": "ref", "id": "inner", "ref": "btn"}]}]}`)
	root := j(`{
		"variables": {"brand": {"type": "color", "value": "#0000ff"}, "ink": {"type": "color", "value": "#111111"}},
		"children": [
			{"type": "ref", "id": "b", "ref": "kit:btn"},
			{"type": "ref", "id": "c", "ref": "kit:card", "descendants": {"inner/lbl": {"fill": "$ink"}}},
			{"type": "ref", "id": "d", "ref": "kit:btn", "stroke": "$brand"}]}`)
	r := New(root, []Lib{{"kit", lib}})
	wantStr(t, prop(t, r.Resolve(child(root, 0)), "fill"), "#ff0000")
	c := r.Resolve(child(root, 1))
	if c.Kids[0].Problem != "" {
		t.Fatal(c.Kids[0].Problem)
	}
	wantStr(t, prop(t, c.Kids[0], "fill"), "#ff0000")
	if c.Kids[0].Kids[0].ID != "c/inner/lbl" {
		t.Fatal(c.Kids[0].Kids[0].ID)
	}
	wantStr(t, prop(t, c.Kids[0].Kids[0], "fill"), "#111111")
	wantStr(t, prop(t, r.Resolve(child(root, 2)), "stroke"), "#0000ff")
}

// Values written in the open document are settled there: an unknown `$name` stays literal even when the
// library happens to define it. A component root's own theme governs its subtree.
func TestLibraryScopeSettlesInstanceValues(t *testing.T) {
	lib := j(`{
		"themes": {"mode": ["light", "dark"]},
		"variables": {"brand": {"type": "color", "value": "#ff0000"}, "fg": {"type": "color", "value": [{"value": "#111111"}, {"value": "#eeeeee", "theme": {"mode": "dark"}}]}},
		"children": [
			{"type": "frame", "id": "btn", "reusable": true, "theme": {"mode": "dark"}, "fill": "$fg", "children": [{"type": "text", "id": "lbl", "content": "Kit", "fill": "$fg"}]}]}`)
	root := j(`{"children": [
		{"type": "ref", "id": "a", "ref": "kit:btn", "stroke": "$brand", "descendants": {"lbl": {"content": "$brand"}}},
		{"type": "ref", "id": "b", "ref": "kit:btn", "theme": {"mode": "light"}}]}`)
	r := New(root, []Lib{{"kit", lib}})
	a := r.Resolve(child(root, 0))
	wantStr(t, prop(t, a, "stroke"), "$brand")
	wantStr(t, prop(t, a.Kids[0], "content"), "$brand")
	wantStr(t, prop(t, a, "fill"), "#eeeeee")
	wantStr(t, prop(t, a.Kids[0], "fill"), "#eeeeee")
	wantStr(t, prop(t, r.Resolve(child(root, 1)), "fill"), "#111111")
}

func TestMigratesOldNodes(t *testing.T) {
	root := j(`{"version": "2.6", "children": [{"type": "icon_font", "iconFontName": "x", "iconFontFamily": "lucide",
		"stroke": {"thickness": 1, "fill": "#000"}},
		{"type": "ellipse", "sweepAngle": 90, "effect": {"type": "shadow", "shadowType": "inner", "spread": 2}},
		{"type": "group", "layout": "vertical", "children": [{"type": "group", "gap": 4}]}]}`)
	Migrate(root)
	n := child(root, 0)
	wantStr(t, jsonv.Get(n, "type"), "icon")
	wantStr(t, jsonv.Get(n, "icon"), "x")
	wantStr(t, jsonv.Get(n, "strokeAlignment"), "inner")
	wantStr(t, jsonv.Get(n, "stroke"), "#000")
	wantStr(t, jsonv.Get(n, "strokeLinecap"), "butt")
	e := child(root, 1)
	if !jsonv.Equal(jsonv.Get(e, "sweepAngle"), jsonv.Number("-90.0")) {
		t.Fatal(string(jsonv.Marshal(e)))
	}
	if !jsonv.Equal(jsonv.Get(e, "effect"), j(`{"type": "shadow", "shadowType": "outer"}`)) {
		t.Fatal(string(jsonv.Marshal(e)))
	}
	g := child(root, 2)
	wantStr(t, jsonv.Get(g, "type"), "frame")
	if !jsonv.IsFalse(jsonv.Get(g, "clip")) || jsonv.Obj(child(g, 0)).Has("gap") {
		t.Fatal(string(jsonv.Marshal(g)))
	}
	wantStr(t, jsonv.Get(root, "version"), "2.19")
	Migrate(root)
	if !jsonv.Equal(jsonv.Get(child(root, 1), "sweepAngle"), jsonv.Number("-90.0")) {
		t.Fatal("migrations run once")
	}
}

func TestMissingRefAndRefCycle(t *testing.T) {
	root := j(`{"children": [
		{"type": "ref", "id": "m", "ref": "gone", "fill": "#fff", "width": 10},
		{"type": "ref", "id": "A", "ref": "B", "reusable": true},
		{"type": "ref", "id": "B", "ref": "A", "reusable": true},
		{"type": "ref", "id": "i", "ref": "A"}]}`)
	r := New(root, nil)
	m := r.Resolve(child(root, 0))
	if m.Problem != "missing component" || m.Type != "frame" || m.Inst != "" {
		t.Fatal(m.Problem, m.Type, m.Inst)
	}
	wantStr(t, prop(t, m, "fill"), "#FF3B3022")
	for i := 1; i < 4; i++ {
		if n := r.Resolve(child(root, i)); n == nil || n.Problem != "" || n.Type != "frame" {
			t.Fatal(i)
		}
	}
	// A component holding an instance of itself expands until the depth guard (64 levels below the root).
	root = j(`{"children": [{"type": "frame", "id": "X", "reusable": true, "children": [{"type": "ref", "id": "xi", "ref": "X"}]}]}`)
	depth := 0
	New(root, nil).Resolve(child(root, 0)).Visit(func(_ *RNode, d int) { depth = max(depth, d) }, 0)
	if depth != 64 {
		t.Fatal(depth)
	}
}
