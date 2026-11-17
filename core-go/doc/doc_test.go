package doc

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

var j = jsonv.MustParse

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func same(t *testing.T, a, b jsonv.Value, msg string) {
	t.Helper()
	if x, y := string(jsonv.Marshal(a)), string(jsonv.Marshal(b)); x != y {
		t.Fatalf("%s:\n%s\n%s", msg, x, y)
	}
}

func TestOpsRoundtrip(t *testing.T) {
	d := New()
	f := must(d.Insert(Root, j(`{"type":"frame","id":"ignored","name":"F","layout":"vertical"}`), -1))
	if f == "ignored" {
		t.Fatal("input id kept")
	}
	tx := must(d.Insert(f, j(`{"type":"text","content":"hi"}`), -1))
	// Leaves get no `children: null` (re-inserting a copy must validate).
	if jsonv.Obj(d.Find(tx)).Has("children") {
		t.Fatal("leaf got children")
	}
	ok(t, d.Update(tx, j(`{"fontSize": 20, "content": null}`)))
	if !jsonv.Equal(jsonv.Get(d.Find(tx), "fontSize"), jsonv.Number("20")) || jsonv.Obj(d.Find(tx)).Has("content") {
		t.Fatal(string(jsonv.Marshal(d.Find(tx))))
	}
	if _, err := d.Insert(tx, j(`{"type":"text"}`), -1); err == nil {
		t.Fatal("text took a child")
	}
	g := must(d.Insert(Root, j(`{"type":"group"}`), -1))
	ok(t, d.MoveNode(tx, g, -1))
	if p, _, _ := d.ParentOf(tx); p != g {
		t.Fatal(p)
	}
	if d.MoveNode(g, g, -1) == nil {
		t.Fatal("moved into itself")
	}
	src := jsonv.Clone(d.Find(f))
	c := must(d.Copy(src, Root, j(`{"name":"Copy"}`)))
	if !jsonv.StrIs(jsonv.Get(d.Find(c), "name"), "Copy") {
		t.Fatal("copy name")
	}
	ok(t, d.Delete(c))
	if d.Find(c) != nil {
		t.Fatal("not deleted")
	}
	ok(t, d.SetVariables(j(`{"bg":{"type":"color","value":[{"value":"#fff","theme":{"mode":"light"}},{"value":"#000","theme":{"mode":"dark"}}]}}`), false))
	same(t, jsonv.Get(jsonv.Get(d.Root, "themes"), "mode"), j(`["light","dark"]`), "themes")
}

func nestedDoc(t *testing.T) *Doc {
	return must(FromJSON([]byte(`{"children":[
		{"type":"frame","id":"f","children":[{"type":"rectangle","id":"a"}, {"type":"group","id":"g","children":[{"type":"rectangle","id":"b"}, {"type":"rectangle","id":"c"}, {"type":"rectangle","id":"d"}]}, {"type":"rectangle","id":"e"}]},
		{"type":"frame","id":"h","children":[{"type":"rectangle","id":"i"}, {"type":"rectangle","id":"j"}]},
		{"type":"group","id":"k","children":[]}]}`)))
}

// The bulk operations are Update / Delete / MoveNode over many ids with one walk of the tree.
func TestBulkOperationsMatchOneNodeAtATime(t *testing.T) {
	one, all := nestedDoc(t), nestedDoc(t)
	props := j(`{"opacity": 0.5, "name": null}`)
	var items []UpdateItem
	for _, id := range []string{"j", "b", "f", "e"} {
		ok(t, one.Update(id, props))
		items = append(items, UpdateItem{id, props})
	}
	ok(t, all.UpdateAll(items))
	same(t, one.Root, all.Root, "update_all")
	kept := jsonv.Clone(all.Root)
	if err := all.UpdateAll([]UpdateItem{{"a", props}, {"nope", props}}); err == nil || err.Error() != "node not found: nope" {
		t.Fatal(err)
	}
	if all.UpdateAll([]UpdateItem{{"a", props}, {"e", j(`5`)}}) == nil || all.UpdateAll([]UpdateItem{{"a", props}, {"e/x", props}}) == nil {
		t.Fatal("bad update accepted")
	}
	same(t, all.Root, kept, "a refused update_all changes nothing, not even the items before the bad one")
	// Instance descendants ("inst/child") become overrides, like Update; a non-instance is refused.
	withRef := []byte(`{"children":[{"type":"ref","id":"r","ref":"c"},{"type":"rectangle","id":"a"}]}`)
	oneI, allI := must(FromJSON(withRef)), must(FromJSON(withRef))
	over := j(`{"opacity": 0.5}`)
	ok(t, oneI.Update("r/x", over))
	ok(t, oneI.Update("a", over))
	ok(t, allI.UpdateAll([]UpdateItem{{"r/x", over}, {"a", over}}))
	same(t, oneI.Root, allI.Root, "update_all overrides")
	if allI.UpdateAll([]UpdateItem{{"a/x", over}}) == nil {
		t.Fatal("override on a non-instance")
	}

	for _, id := range []string{"i", "c", "g", "a"} {
		ok(t, one.Delete(id))
	}
	ok(t, all.DeleteAll([]string{"i", "c", "g", "a"}))
	same(t, one.Root, all.Root, "delete_all")
	if err := all.DeleteAll([]string{"e", "nope"}); err == nil || err.Error() != "node not found: nope" {
		t.Fatal(err)
	}
	if all.DeleteAll([]string{"f/x"}) == nil {
		t.Fatal("deleted an instance descendant")
	}

	one, all = nestedDoc(t), nestedDoc(t)
	for _, id := range []string{"j", "e", "b", "i"} {
		ok(t, one.MoveNode(id, "k", -1))
	}
	ok(t, all.MoveAll([]string{"j", "e", "b", "i", "j"}, "k"))
	same(t, one.Root, all.Root, "move_all")
	kept = jsonv.Clone(all.Root)
	if all.MoveAll([]string{"a", "f"}, "g") == nil {
		t.Fatal("a node cannot move into its own descendant")
	}
	if all.MoveAll([]string{"a", "nope"}, "k") == nil || all.MoveAll([]string{"a"}, "a") == nil {
		t.Fatal("bad move accepted")
	}
	same(t, all.Root, kept, "a refused move changes nothing")
}

func TestOutermostDropsNestedAndRepeatedIDs(t *testing.T) {
	d := nestedDoc(t)
	for _, c := range []struct{ in, want []string }{
		{[]string{"c", "g", "i", "c", "i", "f", "b"}, []string{"i", "f"}},
		{[]string{"c", "i", "c", "d"}, []string{"c", "i", "d"}},
		// An instance's rendered descendant goes with the instance or its ancestors, and stays when only unrelated layers are listed.
		{[]string{"k/x", "f"}, []string{"k/x", "f"}},
		{[]string{"g/x", "f"}, []string{"f"}},
		{[]string{"g/x", "g"}, []string{"g"}},
	} {
		if got := d.Outermost(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%v: %v, want %v", c.in, got, c.want)
		}
	}
}

func TestChildrenAreValidatedLikeTheTopLevelNode(t *testing.T) {
	d := New()
	f := must(d.Insert(Root, j(`{"type":"frame"}`), -1))
	for _, bad := range []string{`"x"`, `5`, `null`, `{"type":"nope"}`, `{"name":"x"}`, `{"type":"ref"}`, `{"type":"text","children":[]}`} {
		node := `{"type":"frame","children":[` + bad + `]}`
		_, err := d.Insert(Root, j(node), -1)
		if err == nil || !strings.Contains(err.Error(), "children[0]") {
			t.Errorf("%s: %v", bad, err)
		}
		if _, err := d.Replace(f, j(node)); err == nil {
			t.Errorf("replace %s", bad)
		}
	}
	_, err := d.Insert(Root, j(`{"type":"frame","children":[{"type":"group","children":[{"type":"text"}, "x"]}]}`), -1)
	if err == nil || !strings.Contains(err.Error(), "children[1]") {
		t.Fatal(err)
	}
	if _, err := d.Insert(Root, j(`{"type":"frame","children":"x"}`), -1); err == nil {
		t.Fatal("children string accepted")
	}
	if len(d.Children()) != 1 {
		t.Fatal("something was stored")
	}
	must(d.Insert(Root, j(`{"type":"frame","children":[{"type":"group","children":[{"type":"text"}]}]}`), -1))
}

func TestEnsureIDsFillsMissingAndRepeatedIDs(t *testing.T) {
	d := must(FromJSON([]byte(`{"children":[
		{"type":"frame","id":"f","children":[{"type":"rectangle"},{"type":"rectangle","id":""},{"type":"ellipse","id":"f"}]},
		{"type":"frame","id":"f"},{"type":"frame","id":7},{"type":"frame","id":"hero/title"}]}`)))
	d.EnsureIDs()
	var ids []string
	Walk(d.Children(), func(n jsonv.Value) {
		id, isStr := jsonv.Str(jsonv.Get(n, "id"))
		if !isStr {
			t.Fatal("every node has a string id")
		}
		ids = append(ids, id)
	})
	if len(ids) != 7 {
		t.Fatal(ids)
	}
	uniq := map[string]bool{}
	for _, id := range ids {
		if id == "" || strings.Contains(id, "/") {
			t.Fatal("an id with '/' (the instance path separator) is replaced", ids)
		}
		uniq[id] = true
	}
	if len(uniq) != 7 || ids[0] != "f" || !jsonv.StrIs(jsonv.Get(d.Find("f"), "type"), "frame") {
		t.Fatal("the first node keeps its id", ids)
	}
}

func TestBaseValueWithOneOverrideKeepsADefaultTheme(t *testing.T) {
	d := New()
	ok(t, d.SetVariables(j(`{"bg":{"type":"color","value":[{"value":"#fff"},{"value":"#111","theme":{"mode":"dark"}}]}}`), false))
	same(t, jsonv.Get(jsonv.Get(d.Root, "themes"), "mode"), j(`["default","dark"]`), "themes")
}

func frame() jsonv.Value { return j(`{"type":"frame","children":[]}`) }

func wrap(v jsonv.Value, n int, f func(jsonv.Value) jsonv.Value) jsonv.Value {
	for range n {
		v = f(v)
	}
	return v
}

func inFrame(v jsonv.Value) jsonv.Value {
	o := j(`{"type":"frame"}`).(*jsonv.Object)
	o.Set("children", jsonv.NewArray(v))
	return o
}

// Everything the doc accepts must reopen: nesting is capped below serde_json's recursion limit.
func TestNestingIsCappedToWhatReopens(t *testing.T) {
	d := New()
	parent := Root
	var ids []string
	for range 60 {
		parent = must(d.Insert(parent, frame(), -1))
		ids = append(ids, parent)
	}
	if _, err := d.Insert(parent, frame(), -1); err == nil || !strings.Contains(err.Error(), "nesting too deep") {
		t.Fatal(err)
	}
	must(FromJSON(jsonv.MarshalPretty(d.Root)))
	// Paste/copy, replace and move are held to the same cap.
	if _, err := d.Copy(frame(), parent, nil); err == nil {
		t.Fatal("copy")
	}
	if _, err := d.Replace(parent, inFrame(frame())); err == nil {
		t.Fatal("replace")
	}
	top := must(d.Insert(Root, frame(), -1))
	if d.MoveNode(top, parent, -1) == nil || d.MoveNode(ids[0], top, -1) == nil {
		t.Fatal("move")
	}
	if d.Find(top) == nil || d.Find(parent) == nil {
		t.Fatal("lost a node")
	}
	// A whole subtree is measured, not just its root.
	if _, err := New().Insert(Root, wrap(frame(), 60, inFrame), -1); err == nil {
		t.Fatal("deep subtree")
	}
	// Property values and instance overrides count too, however they get in.
	nest := wrap(jsonv.Number("1"), MaxLevels, func(v jsonv.Value) jsonv.Value { return jsonv.NewArray(v) })
	effects := func(v jsonv.Value) jsonv.Value {
		o := jsonv.NewObject()
		o.Set("effects", v)
		return o
	}
	if d.Update(parent, effects(jsonv.Clone(nest))) == nil || d.Update(parent, j(`{"effects": [[1]]}`)) == nil {
		t.Fatal("deep property")
	}
	e := New()
	comp := must(e.Insert(Root, j(`{"type":"frame","reusable":true,"children":[]}`), -1))
	inst := must(e.Insert(Root, j(`{"type":"ref","ref":"`+comp+`"}`), -1))
	if e.Update(inst+"/x", effects(jsonv.Clone(nest))) == nil {
		t.Fatal("deep override")
	}
	repl := j(`{"type":"frame","children":[]}`).(*jsonv.Object)
	repl.Set("effects", nest)
	if _, err := e.Replace(inst+"/x", repl); err == nil {
		t.Fatal("deep replacement")
	}
	desc := jsonv.NewObject()
	desc.Set("x", wrap(frame(), MaxLevels/2, inFrame))
	data := jsonv.NewObject()
	data.Set("descendants", desc)
	if _, err := e.Copy(j(`{"type":"ref","ref":"`+comp+`"}`), Root, data); err == nil {
		t.Fatal("deep copy override")
	}
	ok(t, e.Update(inst+"/x", j(`{"opacity": 0.5}`)))
}

// A copy's typed descendant overrides are checked like any node, whether it copies a ref or a frame.
func TestCopyValidatesDescendantReplacements(t *testing.T) {
	e := New()
	comp := must(e.Insert(Root, j(`{"type":"frame","reusable":true,"children":[{"type":"text","name":"t"}]}`), -1))
	bad := j(`{"descendants":{"t":{"type":"frame","children":["str"]}}}`)
	_, err := e.Copy(j(`{"type":"ref","ref":"`+comp+`"}`), Root, bad)
	if err == nil || !strings.Contains(err.Error(), "descendant 't'") || !strings.Contains(err.Error(), "children[0]") {
		t.Fatal(err)
	}
	_, err = e.Copy(j(`{"type":"frame","children":[{"type":"text","name":"t"}]}`), Root, bad)
	if err == nil || !strings.Contains(err.Error(), "descendant 't'") {
		t.Fatal(err)
	}
	if len(e.Children()) != 1 {
		t.Fatal("stored a refused copy")
	}
}

// A loaded frame can hold a non-object child; copying it errors instead of panicking in assignIDs.
func TestCopyValidatesSource(t *testing.T) {
	e := New()
	_, err := e.Copy(j(`{"type":"frame","children":["str"]}`), Root, nil)
	if err == nil || !strings.Contains(err.Error(), "source") || !strings.Contains(err.Error(), "children[0]") {
		t.Fatal(err)
	}
	if len(e.Children()) != 0 {
		t.Fatal("stored a refused copy")
	}
}

func TestRelocateRewritesRelativeReferences(t *testing.T) {
	root := j(`{"imports": {"lib": "my%20lib.pen", "abs": "/x/l.pen"}, "children": [{"id": "f", "type": "frame", "children": [
		{"fill": {"type": "image", "url": "img/r.png"}},
		{"fill": [{"type": "shader", "url": "./s.glsl"}, {"type": "image", "url": "../up/u.png"}]},
		{"type": "script", "scriptUri": "dots.js"},
		{"type": "browser", "url": "example.com"},
		{"fill": {"type": "image", "url": "https://h/i.png"}},
		{"fill": {"type": "image", "url": "data:image/png;base64,AA=="}},
		{"fill": {"type": "image", "url": "nalarupa:pending-x"}},
		{"fill": {"type": "image", "url": "/abs/i.png"}},
		{"fill": {"type": "image", "url": ""}},
		{"fill": {"type": "image", "url": "file://img/f.png"}},
		{"type": "script", "scriptUri": "file:///abs/s.js"}]}]}`)
	Relocate(root, "/p/a", "/p/b/c")
	same(t, jsonv.Get(root, "imports"), j(`{"lib": "../../a/my%20lib.pen", "abs": "/x/l.pen"}`), "imports")
	k := jsonv.Items(jsonv.Get(jsonv.Items(jsonv.Get(root, "children"))[0], "children"))
	url := func(i int) string { return jsonv.StrOr(jsonv.Get(jsonv.Get(k[i], "fill"), "url")) }
	check := func(got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	check(url(0), "../../a/img/r.png")
	fills := jsonv.Items(jsonv.Get(k[1], "fill"))
	check(jsonv.StrOr(jsonv.Get(fills[0], "url")), "../../a/s.glsl")
	check(jsonv.StrOr(jsonv.Get(fills[1], "url")), "../../up/u.png")
	check(jsonv.StrOr(jsonv.Get(k[2], "scriptUri")), "../../a/dots.js")
	check(jsonv.StrOr(jsonv.Get(k[3], "url")), "example.com")
	for i, want := range []string{"https://h/i.png", "data:image/png;base64,AA==", "nalarupa:pending-x", "/abs/i.png", ""} {
		check(url(4+i), want)
	}
	check(url(9), "file://../../a/img/f.png")
	check(jsonv.StrOr(jsonv.Get(k[10], "scriptUri")), "file:///abs/s.js")
	// Into a folder below the old one the path points down.
	root = j(`{"children": [{"fill": {"type": "image", "url": "./images/x.jpg"}}]}`)
	first := func() string {
		return jsonv.StrOr(jsonv.Get(jsonv.Get(jsonv.Items(jsonv.Get(root, "children"))[0], "fill"), "url"))
	}
	Relocate(root, "/p", "/p/sub")
	check(first(), "../images/x.jpg")
	Relocate(root, "/p/sub", "/p")
	check(first(), "./images/x.jpg")
	if !SameDir("/p/a/../a/", "/p/a") {
		t.Error("same_dir")
	}
}

func TestLoadStripsBOMAndReportsParseErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bom.pen")
	ok(t, os.WriteFile(p, []byte("\uFEFF{\"version\":\"2.19\",\"children\":[]}"), 0o666))
	d := must(Load(p))
	if d.Path != p || len(d.Children()) != 0 || !jsonv.StrIs(jsonv.Get(d.Root, "version"), "2.19") {
		t.Fatal(string(jsonv.Marshal(d.Root)))
	}
	trunc := filepath.Join(dir, "trunc.pen")
	ok(t, os.WriteFile(trunc, []byte("{\n  \"children\": [\n    {\"type\": \"frame\""), 0o666))
	_, err := Load(trunc)
	if err == nil || !strings.HasPrefix(err.Error(), "invalid document JSON: ") || !strings.Contains(err.Error(), "line 3") {
		t.Fatal(err)
	}
	if _, err := FromJSON([]byte(`[1]`)); err == nil || err.Error() != "document must be a JSON object" {
		t.Fatal(err)
	}
}

func TestSaveIsAtomicAndSerdeFormatted(t *testing.T) {
	dir := t.TempDir()
	d := must(FromJSON([]byte(`{"version":"2.19","children":[{"type":"text","id":"t","content":"a\u2028b","x":10.0}]}`)))
	p := filepath.Join(dir, "sub", "out.pen")
	ok(t, d.Save(p))
	got := string(must(os.ReadFile(p)))
	want := "{\n  \"version\": \"2.19\",\n  \"children\": [\n    {\n      \"type\": \"text\",\n      \"id\": \"t\",\n      \"content\": \"a\u2028b\",\n      \"x\": 10.0\n    }\n  ]\n}"
	if got != want {
		t.Fatalf("%q", got)
	}
	if _, err := os.Stat(p + ".tmp-nalarupa"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}

// The zero IDSource draws as seed 0 made odd, so it never sticks at 0 (and GenID never loops).
func TestZeroIDSourceIsSeedOne(t *testing.T) {
	var zero IDSource
	one := NewIDSource(0)
	for range 3 {
		if a, b := zero.Next(), one.Next(); a != b || a == 0 {
			t.Fatal(a, b)
		}
	}
	d := New()
	d.IDs = &IDSource{}
	if a, b := d.GenID(), d.GenID(); a == "" || a == b {
		t.Fatal(a, b)
	}
}

// RemoveFile removes a file but never a folder, even an empty one, and reports the system's error for it.
func TestRemoveFileNeverRemovesAFolder(t *testing.T) {
	dir := t.TempDir()
	file, sub := filepath.Join(dir, "a.json"), filepath.Join(dir, "sub")
	if err := os.WriteFile(file, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sub, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := RemoveFile(file); err != nil {
		t.Fatal(err)
	}
	err := RemoveFile(sub)
	if err == nil || !strings.Contains(IOError(err), "(os error ") {
		t.Fatal(err)
	}
	if st, err := os.Stat(sub); err != nil || !st.IsDir() {
		t.Fatal("the folder was removed")
	}
	if err := RemoveFile(file); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

// An errno outside Go's table reads as libc's strerror text, as Rust shows it.
func TestUnknownErrnoReadsAsLibc(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows messages come from FormatMessageW")
	}
	want := "Unknown error 4000"
	if runtime.GOOS == "darwin" {
		want = "Unknown error: 4000"
	}
	if got := systemMessage(syscall.Errno(4000)); got != want {
		t.Fatal(got)
	}
}

// Folders are created as Rust's create_dir_all creates them: a file in the way reports what creating that
// folder reports, and a save under it fails with Rust's text.
func TestCreateDirAllAsRust(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c")
	if err := CreateDirAll(deep); err != nil {
		t.Fatal(err)
	}
	if err := CreateDirAll(deep); err != nil {
		t.Fatal("an existing folder is fine:", err)
	}
	if st, err := os.Stat(deep); err != nil || !st.IsDir() {
		t.Fatal("created")
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o666)
	err := CreateDirAll(filepath.Join(file, "sub", "x"))
	if err == nil {
		t.Fatal("a file in the way fails")
	}
	want := "Not a directory (os error 20)" // mkdir of x below the file
	if runtime.GOOS == "windows" {
		want = "Cannot create a file when that file already exists. (os error 183)"
	}
	if IOError(err) != want {
		t.Fatalf("%q, want %q", IOError(err), want)
	}
	d := New()
	if err := d.Save(filepath.Join(file, "sub", "doc.pen")); err == nil || err.Error() != want {
		t.Fatalf("save under a file: %v", err)
	}
	if err := CreateDirAll(""); err != nil {
		t.Fatal(err)
	}
}
