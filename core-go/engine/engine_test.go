package engine

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/history"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
)

// The ports of engine.rs's tests come first (save_as_moves_the_live_preview waits for the streams of phase 8d),
// then the engine's own.

func newEngine(t *testing.T) *Engine {
	t.Helper()
	e := New(t.TempDir())
	e.Offline()
	return e
}

func root(s string) *jsonv.Object { return jsonv.MustParse(s).(*jsonv.Object) }

// at reads a value by a path of keys and indexes, e.g. at(v, "children", 0, "x").
func at(v jsonv.Value, path ...any) jsonv.Value {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			v = jsonv.Get(v, k)
		case int:
			items := jsonv.Items(v)
			if k >= len(items) {
				return nil
			}
			v = items[k]
		}
	}
	return v
}

func str(v jsonv.Value) string { return jsonv.StrOr(v) }

func num(v jsonv.Value) float64 {
	f, _ := jsonv.F64(v)
	return f
}

func set(t *testing.T, v jsonv.Value, key string, x string) {
	t.Helper()
	jsonv.Obj(v).Set(key, jsonv.MustParse(x))
}

func TestTheEditorStateShowsTheExistingStyle(t *testing.T) {
	e := newEngine(t)
	e.Doc.Root = root(`{
		"variables": {
			"ink": {"type": "color", "value": "#1F2A1C"},
			"paper": {"type": "color", "value": [{"value": "#111111", "theme": {"mode": "dark"}}, {"value": "#F4EFE6"}]},
			"space": {"type": "number", "value": 24},
			"dense": {"type": "boolean", "value": true}
		},
		"themes": {"mode": ["light", "dark"]},
		"children": [{"id": "home", "type": "frame", "name": "Home", "width": 1440, "height": 900, "children": [
			{"id": "h", "type": "text", "name": "Headline", "fontFamily": "Fraunces", "fontSize": 72, "content": "Hi"},
			{"id": "l", "type": "text", "name": "Logo", "fontFamily": "Fraunces", "fontSize": 30, "content": "fernway"},
			{"id": "b", "type": "text", "name": "Body", "fontFamily": "Work Sans", "fontSize": 17.4, "content": "..."},
			{"id": "b2", "type": "text", "name": "Body 2", "fontFamily": "Work Sans", "fontSize": 17, "content": "..."}
		]}]}`)
	s := e.EditorState()
	if !strings.Contains(s, "- Variables: 4 (theme axes: mode): ink #1F2A1C, paper #F4EFE6, space 24, dense true\n") {
		t.Errorf("the base entry of a themed variable, wherever it sits: %s", s)
	}
	if !strings.Contains(s, "- Fonts in use: Fraunces (72, 30); Work Sans (17)\n") {
		t.Error(s)
	}
	e.Doc.Root = root(`{"children": [{"id": "r", "type": "rectangle", "width": 10, "height": 10}]}`)
	if s := e.EditorState(); !strings.Contains(s, "- Variables: 0\n") || strings.Contains(s, "Fonts in use") {
		t.Error(s)
	}
	var texts []string
	for i := range 14 {
		texts = append(texts, fmt.Sprintf(`{"id": "t%d", "type": "text", "fontFamily": "Face %d", "fontSize": 16, "content": "x"}`, i, i))
	}
	e.Doc.Root = root(`{"children": [{"id": "f", "type": "frame", "children": [` + strings.Join(texts, ",") + `]}]}`)
	if s := e.EditorState(); !strings.Contains(s, "Face 11 (16); ... +2 other families\n") || strings.Contains(s, "Face 12") {
		t.Errorf("the font list is capped with a count of what it left out: %s", s)
	}
}

func TestDragThatEndsWhereItStartedLeavesNoUndoStep(t *testing.T) {
	e := newEngine(t)
	e.Doc.Root = root(`{"children": [{"id": "a", "type": "rectangle", "x": 10, "y": 10}]}`)
	e.DragBefore = cloneRoot(e.Doc.Root)
	set(t, at(e.Doc.Root, "children", 0), "x", "40.0")
	set(t, at(e.Doc.Root, "children", 0), "x", "10.0")
	before := e.DragBefore
	e.DragBefore = nil
	e.Commit(before)
	if len(e.undo) != 0 {
		t.Fatal("10 written back as 10.0 is not a change")
	}
	set(t, at(e.Doc.Root, "children", 0), "x", "10.5")
	e.Commit(root(`{"children": [{"id": "a", "type": "rectangle", "x": 10, "y": 10}]}`))
	if len(e.undo) != 1 {
		t.Fatal(len(e.undo))
	}
}

func TestUnchangedDragKeepsACleanDocumentClean(t *testing.T) {
	e := newEngine(t)
	e.Doc.Root = root(`{"children": [{"id": "a", "type": "rectangle", "x": 10}]}`)
	e.Saved = e.Version
	before := cloneRoot(e.Doc.Root)
	set(t, at(e.Doc.Root, "children", 0), "x", "10.0")
	e.Touch()
	if !e.Dirty() {
		t.Fatal("touched")
	}
	e.EndDrag(before, true)
	if e.Dirty() || len(e.undo) != 0 {
		t.Fatal("an unchanged drag is no step and keeps the document clean")
	}
}

func setChildren(s string) func(*doc.Doc) error {
	return func(d *doc.Doc) error {
		d.Root.Set("children", jsonv.MustParse(s))
		return nil
	}
}

func setAt(value string, path ...any) func(*doc.Doc) error {
	return func(d *doc.Doc) error {
		jsonv.Obj(at(d.Root, path[:len(path)-1]...)).Set(path[len(path)-1].(string), jsonv.MustParse(value))
		return nil
	}
}

func TestUndoBackToTheSavedDocumentIsClean(t *testing.T) {
	e := newEngine(t)
	if e.Dirty() {
		t.Fatal("a new engine is clean")
	}
	e.Edit(setChildren(`[{"id": "a", "type": "rectangle", "x": 10}]`))
	if !e.Dirty() {
		t.Fatal("edited")
	}
	e.Undo()
	if e.Dirty() {
		t.Fatal("back at the created document")
	}
	e.Redo()
	if !e.Dirty() {
		t.Fatal("redone")
	}
	e.markSaved()
	e.Edit(setAt("20", "children", 0, "x"))
	e.Undo()
	if e.Dirty() {
		t.Fatal("back at the saved document")
	}
	e.Undo()
	if !e.Dirty() {
		t.Fatal("past the saved document")
	}
	e.Redo()
	if e.Dirty() {
		t.Fatal("forward to the saved document")
	}
	// A save in the middle of a coalesced field edit: undo after it still stops at the saved document.
	e.EditCoalesced("x", setAt("30", "children", 0, "x"))
	e.markSaved()
	e.EditCoalesced("x", setAt("40", "children", 0, "x"))
	e.Undo()
	if num(at(e.Doc.Root, "children", 0, "x")) != 30 || e.Dirty() {
		t.Fatal("a coalesced edit does not merge across a save")
	}
}

func TestSameNumberComparesIntegersExactly(t *testing.T) {
	same := func(a, b string) bool { return history.Same(jsonv.MustParse(a), jsonv.MustParse(b)) }
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"10", "10.0", true}, {"10", "10.5", false}, {"9007199254740992", "9007199254740993", false},
		{"9007199254740993", "9007199254740992.0", false}, {"9007199254740992", "9007199254740992.0", true},
		{"18446744073709551615", "18446744073709551615", true}, {"18446744073709551615", "-1", false},
	} {
		if same(c.a, c.b) != c.want {
			t.Errorf("same(%s, %s) != %v", c.a, c.b, c.want)
		}
	}
}

func TestRewrittenNumberDoesNotStartACoalescingGroup(t *testing.T) {
	e := newEngine(t)
	e.Doc.Root = root(`{"children": [{"id": "a", "x": 10, "y": 0}]}`)
	e.Edit(setAt("1", "children", 0, "y"))
	e.EditCoalesced("x", setAt("10.0", "children", 0, "x"))
	if e.hasCoalesce {
		t.Fatal("no change, no group")
	}
	e.EditCoalesced("x", setAt("20", "children", 0, "x"))
	if len(e.undo) != 2 {
		t.Fatal("the real edit is its own undo step")
	}
}

func TestBlankKeyIsNone(t *testing.T) {
	for _, c := range []struct {
		k  string
		ok bool
	}{{"", false}, {"", true}, {"  \t", true}} {
		if _, ok := nonblank(c.k, c.ok); ok {
			t.Errorf("%q is no key", c.k)
		}
	}
	if k, ok := nonblank(" sk-ant-x\n", true); !ok || k != "sk-ant-x" {
		t.Error(k)
	}
}

func TestMerge3KeepsAReorderByTheirs(t *testing.T) {
	n := func(id string, x int) string { return fmt.Sprintf(`{"id": %q, "x": %d}`, id, x) }
	base := jsonv.MustParse(`{"children": [` + n("a", 0) + "," + n("b", 0) + "," + n("c", 0) + `]}`)
	theirs := jsonv.MustParse(`{"children": [` + n("c", 0) + "," + n("a", 0) + "," + n("b", 0) + `]}`)
	ours := jsonv.MustParse(`{"children": [` + n("a", 5) + "," + n("b", 0) + "," + n("c", 0) + "," + n("d", 0) + `]}`)
	m := merge3(base, theirs, ours)
	var ids []string
	for _, k := range jsonv.Items(jsonv.Get(m, "children")) {
		ids = append(ids, str(jsonv.Get(k, "id")))
	}
	if !slices.Equal(ids, []string{"c", "a", "b", "d"}) || num(at(m, "children", 1, "x")) != 5 {
		t.Fatal(string(jsonv.Marshal(m)))
	}
}

func TestOpenKeepsPendingURLsOfRunningDownloads(t *testing.T) {
	v := jsonv.MustParse(`{"children": [
		{"fill": {"type": "image", "url": "nalarupa:pending-image-1-a"}},
		{"fill": {"type": "image", "url": "nalarupa:pending-image-2-b"}},
		{"fill": {"type": "image", "url": "./images/x.jpg"}}]}`)
	dropPending(v, map[string]bool{"nalarupa:pending-image-1-a": true})
	for i, want := range []string{"nalarupa:pending-image-1-a", "", "./images/x.jpg"} {
		if got := str(at(v, "children", i, "fill", "url")); got != want {
			t.Errorf("%d: %q, want %q", i, got, want)
		}
	}
}

func TestOpenClearsPlaceholderFlagsNobodyWillClear(t *testing.T) {
	v := jsonv.MustParse(`{"children": [
		{"id": "a", "type": "frame", "placeholder": true, "children": [{"id": "b", "type": "frame", "placeholder": true}]},
		{"id": "c", "type": "frame", "placeholder": true},
		{"id": "d", "type": "frame"},
		{"id": "e", "type": "rectangle", "metadata": {"placeholder": true}}]}`)
	dropPlaceholder(v, []string{"c"})
	if !jsonv.IsTrue(at(v, "children", 3, "metadata", "placeholder")) {
		t.Error("only a frame's own flag is cleared")
	}
	if !jsonv.IsFalse(at(v, "children", 0, "placeholder")) || !jsonv.IsFalse(at(v, "children", 0, "children", 0, "placeholder")) {
		t.Error("cleared")
	}
	if !jsonv.IsTrue(at(v, "children", 1, "placeholder")) || jsonv.Obj(at(v, "children", 2)).Has("placeholder") {
		t.Error("a pen's frame keeps its flag; none is added")
	}
}

func TestAFileSavedMidRunReopensWithoutGeneratingFrames(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)
	e.Offline()
	d, _ := doc.FromJSON([]byte(`{"version":"2.19","children":[{"id":"f","type":"frame","name":"Fake","x":0,"y":0,"width":800,"height":500,"placeholder":true}]}`))
	e.setDoc(d)
	path := filepath.Join(dir, "mid.nala")
	if _, err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := e.Open(path); err != nil {
		t.Fatal(err)
	}
	if !jsonv.IsFalse(jsonv.Get(e.Doc.Find("f"), "placeholder")) {
		t.Fatal("cleared on open")
	}
	// A pen drawing frame "f" of this file keeps it Generating on a reopen, not in another file with the same id.
	e.Doc.Update("f", jsonv.MustParse(`{"placeholder": true}`))
	e.Save("")
	other := filepath.Join(dir, "other.nala")
	b, _ := os.ReadFile(path)
	os.WriteFile(other, b, 0o666)
	// An imported library saved mid-run is cleared too.
	os.WriteFile(filepath.Join(dir, "lib.nala"), []byte(`{"version":"2.19","children":[{"id":"L","type":"frame","reusable":true,"placeholder":true}]}`), 0o666)
	e.Doc.Root.Set("imports", jsonv.MustParse(`{"lib": "lib.nala"}`))
	e.Save("")
	e.Agent.Pens = append(e.Agent.Pens, "f")
	e.Open(path)
	if !jsonv.IsTrue(jsonv.Get(e.Doc.Find("f"), "placeholder")) {
		t.Error("the pen's frame keeps its flag")
	}
	if !jsonv.IsFalse(at(e.Libs[0].Root, "children", 0, "placeholder")) {
		t.Error("the library's flag is cleared")
	}
	e.Open(other)
	if !jsonv.IsFalse(jsonv.Get(e.Doc.Find("f"), "placeholder")) {
		t.Error("another file with the same id is cleared")
	}
}

// A library with a child without an id and two children sharing one is repaired on open like the document:
// otherwise an instance's children resolve to the paths `i/` and `i/t` twice, and cannot be told apart.
func TestOpenRepairsLibraryIDs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.nala"), []byte(`{"version":"2.19","children":[{"id":"C","type":"frame","reusable":true,"children":[
		{"type":"text","content":"no id"},{"id":"t","type":"text","content":"first"},{"id":"t","type":"text","content":"second"}]}]}`), 0o666)
	os.WriteFile(filepath.Join(dir, "doc.nala"), []byte(`{"version":"2.19","imports":{"lib":"lib.nala"},"children":[{"id":"i","type":"ref","ref":"lib:C"}]}`), 0o666)
	e := New(filepath.Join(dir, "data"))
	e.Offline()
	paths := func() []string {
		inst := resolve.New(e.Doc.Root, e.Libs).Resolve(e.Doc.Children()[0])
		var out []string
		for _, k := range inst.Kids {
			out = append(out, k.ID)
		}
		return out
	}
	if err := e.Open(filepath.Join(dir, "doc.nala")); err != nil {
		t.Fatal(err)
	}
	first := paths()
	if len(first) != 3 || first[1] != "i/t" {
		t.Fatal(first)
	}
	seen := map[string]bool{}
	for _, p := range first {
		if len(p) <= 2 || !strings.HasPrefix(p, "i/") || seen[p] {
			t.Fatal(first)
		}
		seen[p] = true
	}
	// The repair is the same on every open, and when the library itself is opened, so an override saved on a
	// repaired child still reaches it.
	e.Open(filepath.Join(dir, "doc.nala"))
	if again := paths(); !slices.Equal(again, first) {
		t.Errorf("the same ids on the next open: %v, %v", again, first)
	}
	e.Open(filepath.Join(dir, "lib.nala"))
	var own []string
	for _, k := range jsonv.Items(at(e.Doc.Children()[0], "children")) {
		own = append(own, "i/"+str(jsonv.Get(k, "id")))
	}
	if !slices.Equal(own, first) {
		t.Errorf("the library opened as a document gets the same ids: %v, %v", own, first)
	}
}

func TestResolvePendingReachesLibraries(t *testing.T) {
	e := newEngine(t)
	e.Libs = append(e.Libs, resolve.Lib{Alias: "lib", Root: jsonv.MustParse(`{"children": [{"fill": {"type": "image", "url": "nalarupa:pending-image-1-a"}}]}`)})
	e.Inflight["nalarupa:pending-image-1-a"] = true
	e.ResolvePending("nalarupa:pending-image-1-a", "./images/x.jpg")
	if str(at(e.Libs[0].Root, "children", 0, "fill", "url")) != "./images/x.jpg" || len(e.Inflight) != 0 {
		t.Fatal(string(jsonv.Marshal(e.Libs[0].Root)))
	}
}

func TestSaveAsRelocatesTheDragSnapshot(t *testing.T) {
	dir := t.TempDir()
	e := New(filepath.Join(dir, "data"))
	e.Doc.Path = filepath.Join(dir, "a", "x.pen")
	e.Doc.Root = root(`{"children": [{"id": "i", "type": "rectangle", "fill": {"type": "image", "url": "./img.png"}}]}`)
	e.DragBefore = cloneRoot(e.Doc.Root)
	if _, err := e.Save(filepath.Join(dir, "b", "y.pen")); err != nil {
		t.Fatal(err)
	}
	if got := str(at(e.DragBefore, "children", 0, "fill", "url")); got != "../a/img.png" {
		t.Fatal(got)
	}
}

// The undo history holds patches between states: after Save as into another folder, undo and redo bring back
// each earlier state with its relative urls rewritten, not the ones of the old folder.
func TestSaveAsRelocatesTheUndoHistory(t *testing.T) {
	dir := t.TempDir()
	fill := func(name string) func(*doc.Doc) error {
		return func(d *doc.Doc) error {
			return d.Update("i", jsonv.MustParse(`{"fill": {"type": "image", "url": "./`+name+`.png"}}`))
		}
	}
	start := func() *Engine {
		e := New(filepath.Join(dir, "data"))
		e.Doc.Path = filepath.Join(dir, "a", "x.pen")
		e.Doc.Root = root(`{"children": [{"id": "i", "type": "rectangle", "fill": {"type": "image", "url": "./one.png"}}]}`)
		return e
	}
	url := func(e *Engine) string { return str(at(e.Doc.Root, "children", 0, "fill", "url")) }
	e := start()
	e.Edit(fill("two"))
	e.Edit(fill("three"))
	if !e.Undo() {
		t.Fatal("undo")
	}
	if _, err := e.Save(filepath.Join(dir, "b", "y.pen")); err != nil {
		t.Fatal(err)
	}
	if url(e) != "../a/two.png" {
		t.Fatal(url(e))
	}
	if !e.Redo() || url(e) != "../a/three.png" {
		t.Fatal(url(e))
	}
	if !e.Undo() || !e.Undo() || url(e) != "../a/one.png" {
		t.Fatal(url(e))
	}
	// Save as during a drag: the history leads back from the snapshot taken when the drag began.
	e = start()
	e.Edit(fill("two"))
	e.DragBefore = cloneRoot(e.Doc.Root)
	e.Doc.Update("i", jsonv.MustParse(`{"fill": {"type": "image", "url": "./drag.png"}}`))
	if _, err := e.Save(filepath.Join(dir, "b", "y.pen")); err != nil {
		t.Fatal(err)
	}
	if got := str(at(e.DragBefore, "children", 0, "fill", "url")); got != "../a/two.png" {
		t.Fatal(got)
	}
	e.Doc.Root, e.DragBefore = e.DragBefore, nil
	if !e.Undo() || url(e) != "../a/one.png" {
		t.Fatal(url(e))
	}
}

func TestFailedSettingsSaveKeepsOldValues(t *testing.T) {
	dir := t.TempDir()
	e := New(dir)
	if err := e.SetSetting("k", "old"); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "settings.json.tmp")
	os.MkdirAll(blocker, 0o777) // a folder where the temp file goes: every save fails
	for _, c := range []struct {
		k string
		v jsonv.Value
	}{{"k", "new"}, {"k", nil}, {"other", "x"}} {
		if err := e.SetSetting(c.k, c.v); err == nil {
			t.Fatalf("setting %s to %v: a failed save is an error", c.k, c.v)
		}
	}
	if k, _ := e.Setting("k"); k != "old" {
		t.Error(k)
	}
	if _, ok := e.Setting("other"); ok {
		t.Error("other was never set")
	}
	os.Remove(blocker)
	if err := e.SetSetting("k", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if jsonv.Obj(jsonv.MustParse(string(b))).Has("k") || exists(blocker) {
		t.Fatal(string(b))
	}
	os.Remove(filepath.Join(dir, "settings.json"))
	os.Mkdir(filepath.Join(dir, "settings.json"), 0o777) // the temp file writes, the rename fails
	if e.SetSetting("k", "new") == nil {
		t.Fatal("the rename fails")
	}
	if exists(blocker) {
		t.Error("a failed rename must not leave the new settings in the temp file")
	}
	if _, ok := e.Setting("k"); ok {
		t.Error("k keeps its old value: none")
	}
}

func pngOf(t *testing.T, w int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, w, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// An image generated while Untitled sits in the data folder: Save copies it next to the file and refers to it
// relatively; the SVG export inlines it.
func TestSaveLocalizesGeneratedImagesAndSvgExportEmbedsThem(t *testing.T) {
	dir := t.TempDir()
	e := New(filepath.Join(dir, "data"))
	e.Offline()
	e.LoadTemplate("empty")
	os.MkdirAll(filepath.Join(dir, "data", "images"), 0o777)
	pic := filepath.Join(dir, "data", "images", "abc.png")
	os.WriteFile(pic, pngOf(t, 2), 0o666)
	url := filepath.ToSlash(pic)
	id, err := e.InsertRoot(jsonv.MustParse(`{"type": "frame", "x": 0, "y": 0, "width": 40, "height": 40, "layout": "none",
		"fill": {"type": "image", "url": "` + url + `", "mode": "fill"}}`))
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(dir, "proj", "doc.pen")
	if _, err := e.Save(saved); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(saved)
	if got := str(at(jsonv.MustParse(string(b)), "children", 0, "fill", "url")); got != "./images/abc.png" {
		t.Fatal(got)
	}
	if !isFile(filepath.Join(dir, "proj", "images", "abc.png")) || !isFile(pic) {
		t.Fatal("copied, not moved")
	}
	if got := str(at(e.Doc.Root, "children", 0, "fill", "url")); got != "./images/abc.png" {
		t.Fatal(got)
	}
	out := filepath.Join(dir, "out")
	job, err := export.Plan(&export.Ctx{Doc: e.Doc, Fonts: e.Fonts, Assets: e.Assets}, e.DataDir, []string{id}, "svg", out, jsonv.NewObject())
	if err != nil {
		t.Fatal(err)
	}
	files, err := job.Run()
	if err != nil {
		t.Fatal(err)
	}
	svg, _ := os.ReadFile(files[0])
	if !strings.Contains(string(svg), `xlink:href="data:image/png;base64,`) || strings.Contains(string(svg), "images/abc.png") {
		t.Fatal(string(svg))
	}
	// Save as into another folder keeps the localized image reachable.
	if _, err := e.Save(filepath.Join(dir, "other", "doc.pen")); err != nil {
		t.Fatal(err)
	}
	if got := str(at(e.Doc.Root, "children", 0, "fill", "url")); got != "../proj/images/abc.png" {
		t.Fatal(got)
	}
	// A different file already under the name is not replaced; an identical one is reused.
	fresh := filepath.Join(dir, "data", "images", "new.png")
	os.WriteFile(fresh, pngOf(t, 3), 0o666)
	os.MkdirAll(filepath.Join(dir, "third", "images"), 0o777)
	os.WriteFile(filepath.Join(dir, "third", "images", "new.png"), pngOf(t, 5), 0o666)
	freshURL := filepath.ToSlash(fresh)
	e.InsertRoot(jsonv.MustParse(`{"type": "rectangle", "width": 4, "height": 4, "fill": {"type": "image", "url": "` + freshURL + `"}}`))
	e.InsertRoot(jsonv.MustParse(`{"type": "rectangle", "width": 4, "height": 4, "fill": {"type": "image", "url": "` + url + `"}}`))
	os.WriteFile(filepath.Join(dir, "third", "images", "abc.png"), pngOf(t, 2), 0o666)
	if _, err := e.Save(filepath.Join(dir, "third", "doc.pen")); err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, n := range e.Doc.Children()[1:] {
		urls = append(urls, str(at(n, "fill", "url")))
	}
	if !slices.Equal(urls, []string{"./images/new-2.png", "./images/abc.png"}) {
		t.Fatal(urls)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "third", "images", "new.png")); !bytes.Equal(b, pngOf(t, 5)) {
		t.Error("the different file is kept")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "third", "images", "new-2.png")); !bytes.Equal(b, pngOf(t, 3)) {
		t.Error("the copy takes the next name")
	}
	// A failed save leaves no copies behind.
	blocked := filepath.Join(dir, "fourth", "doc.pen")
	os.MkdirAll(blocked, 0o777) // a folder where the file should go
	e.InsertRoot(jsonv.MustParse(`{"type": "rectangle", "width": 4, "height": 4, "fill": {"type": "image", "url": "` + freshURL + `"}}`))
	if _, err := e.Save(blocked); err == nil {
		t.Fatal("saving onto a folder fails")
	}
	if exists(filepath.Join(dir, "fourth", "images")) {
		t.Fatal("a failed save leaves no copies behind")
	}
}

func xs(e *Engine) []string {
	var out []string
	for _, n := range e.Doc.Children() {
		out = append(out, fmt.Sprintf("%s %v", str(jsonv.Get(n, "name")), num(jsonv.Get(n, "x"))))
	}
	return out
}

// A commit that lands during a drag is its own undo step; the drag undoes without it.
func TestCommitDuringDragKeepsBothSteps(t *testing.T) {
	e := newEngine(t)
	e.LoadTemplate("empty")
	e.Doc.Root.Set("children", jsonv.MustParse(`[{"id": "u", "type": "frame", "name": "U", "x": 0}]`))
	e.DragBefore = cloneRoot(e.Doc.Root)
	set(t, at(e.Doc.Root, "children", 0), "x", "50")
	before := cloneRoot(e.Doc.Root) // the script's snapshot, taken mid-drag
	kids := jsonv.Arr(jsonv.Get(e.Doc.Root, "children"))
	kids.Items = append(kids.Items, jsonv.MustParse(`{"id": "a", "type": "frame", "name": "AgentF", "x": 300}`))
	e.Commit(before)
	set(t, at(e.Doc.Root, "children", 0), "x", "100")
	start := e.DragBefore
	e.DragBefore = nil
	e.Commit(start)
	if got := xs(e); !slices.Equal(got, []string{"U 100", "AgentF 300"}) {
		t.Fatal(got)
	}
	if !e.Undo() || !slices.Equal(xs(e), []string{"U 0", "AgentF 300"}) {
		t.Fatal(xs(e))
	}
	if !e.Undo() || !slices.Equal(xs(e), []string{"U 0"}) {
		t.Fatal(xs(e))
	}
	if e.Undo() {
		t.Fatal("nothing more to undo")
	}
}

// Undo and redo during a drag do nothing, so the drag commits as one step on top of the history.
func TestUndoAndRedoAreIgnoredDuringADrag(t *testing.T) {
	e := newEngine(t)
	e.LoadTemplate("empty")
	e.Doc.Root.Set("children", jsonv.MustParse(`[{"id": "u", "type": "frame", "name": "U", "x": 0}]`))
	e.Edit(setAt("10", "children", 0, "x"))
	e.Edit(setAt("20", "children", 0, "x"))
	if !e.Undo() {
		t.Fatal("undo")
	}
	e.DragBefore = cloneRoot(e.Doc.Root)
	set(t, at(e.Doc.Root, "children", 0), "x", "50")
	if e.Undo() || e.Redo() || len(e.undo) != 1 || len(e.redo) != 1 {
		t.Fatal("ignored during a drag")
	}
	before := e.DragBefore
	e.DragBefore = nil
	e.EndDrag(before, false)
	for _, want := range []string{"U 50", "U 10", "U 0"} {
		if got := xs(e); !slices.Equal(got, []string{want}) {
			t.Fatal(got, want)
		}
		e.Undo()
	}
	if e.Undo() {
		t.Fatal("nothing more to undo")
	}
}

// Undo of the step that cleared `placeholder` must not show a finished design as "Generating"; while an agent
// runs, history is left alone.
func TestUndoDoesNotRestoreGeneratingWithoutAnAgent(t *testing.T) {
	e := newEngine(t)
	e.LoadTemplate("empty")
	// `h` stands for a frame an external agent is still building: no undo step touches its flag.
	e.Doc.Root.Set("children", jsonv.MustParse(`[{"id": "g", "type": "frame", "name": "G", "x": 0, "placeholder": true}, {"id": "h", "type": "frame", "name": "H", "placeholder": true}]`))
	e.Edit(setAt("10", "children", 0, "x"))
	e.Edit(setAt("false", "children", 0, "placeholder"))
	flag := func() bool { return jsonv.IsTrue(at(e.Doc.Root, "children", 0, "placeholder")) }
	external := func() bool { return jsonv.IsTrue(at(e.Doc.Root, "children", 1, "placeholder")) }
	e.Agent.Running = true
	if !e.Undo() || !flag() {
		t.Fatal("an agent still works: its flag stays")
	}
	e.Agent.Running = false
	if !e.Redo() || flag() {
		t.Fatal("redo")
	}
	if !e.Undo() || flag() {
		t.Fatal("no agent: undo does not bring the flag back")
	}
	if !e.Undo() || flag() {
		t.Fatal("nor does an older step")
	}
	if !e.Redo() || !e.Redo() || flag() {
		t.Fatal("redo")
	}
	if !external() {
		t.Fatal("a flag no undo or redo put back is left to its agent")
	}
}

// systemFont is a font file of the machine (its family does not matter), or "".
func systemFont() string {
	for _, f := range []string{`C:\Windows\Fonts\arial.ttf`, "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", "/System/Library/Fonts/Supplemental/Arial.ttf"} {
		if isFile(f) {
			return f
		}
	}
	return ""
}

// A font landing in the background redraws the canvas but is not an edit.
func TestDownloadedFontRedrawsWithoutDirtying(t *testing.T) {
	font := systemFont()
	if font == "" {
		t.Skip("no system font to stand for a download")
	}
	dir := t.TempDir()
	e := New(dir)
	e.Offline()
	b, _ := os.ReadFile(font)
	os.WriteFile(filepath.Join(dir, "fonts", "Fake_Face-400.ttf"), b, 0o666)
	before := e.Version
	e.FontReady("Fake Face", 400, false)
	if e.Version <= before || e.Dirty() {
		t.Fatal("a redraw that keeps the document clean")
	}
	e.Touch()
	v := e.Version
	e.FontReady("Other Face", 400, false)
	if e.Version != v || !e.Dirty() {
		t.Fatal("nothing new loaded: no redraw")
	}
}

func TestFinishedDownloadRedrawsWithoutDirtying(t *testing.T) {
	e := newEngine(t)
	before, generation := e.Version, e.Assets.Generation
	e.AssetReady("http://x/a.png", true)
	if e.Version <= before || e.Dirty() || e.Assets.Generation != generation+1 {
		t.Fatal("a clean document stays clean")
	}
	e.Touch()
	v := e.Version
	e.AssetReady("http://x/b.png", false)
	if e.Version <= v || !e.Dirty() {
		t.Fatal("a failed download redraws too (the image shows as unavailable)")
	}
}

func bigEngine(t *testing.T, n int) *Engine {
	e := newEngine(t)
	kids := make([]string, n)
	for i := range n {
		kids[i] = fmt.Sprintf(`{"id": "n%d", "type": "rectangle", "name": "r%d", "x": %d, "y": 0, "width": 20, "height": 20, "fill": "#f00"}`, i, i, i)
	}
	e.Doc.Root = root(`{"children": [{"id": "f", "type": "frame", "children": [` + strings.Join(kids, ",") + `]}]}`)
	return e
}

func xOf(e *Engine, i int) float64 { return num(at(e.Doc.Root, "children", 0, "children", i, "x")) }

// An undo step costs what the edit changed, not a copy of the document.
func TestUndoStepsStoreOnlyWhatChanged(t *testing.T) {
	e := bigEngine(t, 5000)
	docLen := len(jsonv.Marshal(e.Doc.Root))
	for i := 1; i <= 100; i++ {
		e.Edit(setAt(fmt.Sprint(1000+i), "children", 0, "children", 5, "x"))
	}
	if len(e.undo) != 100 {
		t.Fatal(len(e.undo))
	}
	held := 0
	for _, p := range e.undo {
		held += len(fmt.Sprintf("%+v", p))
	}
	if held*20 >= docLen {
		t.Fatalf("100 steps hold %d bytes of a %d-byte document", held, docLen)
	}
	for i := 99; i >= 0; i-- {
		want := float64(1000 + i)
		if i == 0 {
			want = 5
		}
		if !e.Undo() || xOf(e, 5) != want {
			t.Fatal(i, xOf(e, 5))
		}
	}
	for i := 1; i <= 100; i++ {
		if !e.Redo() || xOf(e, 5) != float64(1000+i) {
			t.Fatal(i)
		}
	}
	// The cap: the oldest step goes.
	e.Edit(setAt("1", "children", 0, "children", 5, "x"))
	if len(e.undo) != undoLimit {
		t.Fatal(len(e.undo))
	}
}

// Edits that share a coalescing key undo together and redo to the last one.
func TestCoalescedEditsUndoToTheFirstState(t *testing.T) {
	e := bigEngine(t, 20)
	for _, x := range []string{"100", "200", "300"} {
		e.EditCoalesced("x", setAt(x, "children", 0, "children", 3, "x"))
	}
	e.EditCoalesced("y", setAt("7", "children", 0, "children", 4, "y"))
	if len(e.undo) != 2 {
		t.Fatal(len(e.undo))
	}
	if !e.Undo() || xOf(e, 3) != 300 || num(at(e.Doc.Root, "children", 0, "children", 4, "y")) != 0 {
		t.Fatal("y undone")
	}
	if !e.Undo() || xOf(e, 3) != 3 {
		t.Fatal("x undone to the first state")
	}
	if !e.Redo() || xOf(e, 3) != 300 {
		t.Fatal("redone to the last")
	}
}

// A finished generation reaches the history too, so undo never brings the half-drawn node back.
func TestFinishedGenerationReachesTheHistory(t *testing.T) {
	e := bigEngine(t, 5)
	e.Edit(setAt("50", "children", 0, "children", 1, "x"))
	e.Edit(func(d *doc.Doc) error {
		kids := jsonv.Arr(at(d.Root, "children", 0, "children"))
		kids.Items = slices.Delete(kids.Items, 2, 3)
		return nil
	})
	props := root(`{"fill": "#0f0"}`)
	for _, id := range []string{"n1", "n2", "n3"} {
		if e.ApplyGenerated(id, props, true) != (id != "n2") {
			t.Fatal(id)
		}
	}
	fills := func() []string {
		var out []string
		for _, n := range jsonv.Items(at(e.Doc.Root, "children", 0, "children")) {
			out = append(out, str(jsonv.Get(n, "fill")))
		}
		return out
	}
	if !slices.Equal(fills(), []string{"#f00", "#0f0", "#0f0", "#f00"}) {
		t.Fatal(fills())
	}
	if !e.Undo() || !slices.Equal(fills(), []string{"#f00", "#0f0", "#0f0", "#0f0", "#f00"}) {
		t.Fatal("n2 is only in the history", fills())
	}
	if !e.Undo() || xOf(e, 1) != 1 || fills()[1] != "#0f0" {
		t.Fatal(fills())
	}
	if !e.Redo() || !e.Redo() || xOf(e, 1) != 50 || !slices.Equal(fills(), []string{"#f00", "#0f0", "#0f0", "#f00"}) {
		t.Fatal(fills())
	}
}

// ── the engine's own tests ────────────────────────────────────────────────

func TestTemplatesMatchTheRustCore(t *testing.T) {
	rust, _ := filepath.Glob(filepath.Join("..", "..", "core", "templates", "*.json"))
	if len(rust) == 0 {
		t.Skip("no core/templates next to core-go")
	}
	own, _ := templates.ReadDir("templates")
	if len(own) != len(rust) {
		t.Fatalf("%d templates, core/templates has %d", len(own), len(rust))
	}
	for _, f := range rust {
		want, _ := os.ReadFile(f)
		if got, err := templates.ReadFile("templates/" + filepath.Base(f)); err != nil || !bytes.Equal(got, want) {
			t.Errorf("templates/%s differs from %s: copy it again", filepath.Base(f), f)
		}
	}
}

// The edge cases of what an edit does to the history: an edit that fails leaves the document and history as they
// were; a batch is one step or nothing.
func TestFailedEditsAndBatches(t *testing.T) {
	e := newEngine(t)
	e.LoadTemplate("empty")
	e.Edit(setChildren(`[{"id": "a", "type": "rectangle", "x": 0}]`))
	steps := len(e.undo)
	failing := func(d *doc.Doc) error {
		setAt("99", "children", 0, "x")(d)
		return fmt.Errorf("no")
	}
	if e.Edit(failing) == nil || e.EditCoalesced("x", failing) == nil || num(at(e.Doc.Root, "children", 0, "x")) != 0 || len(e.undo) != steps {
		t.Fatal("a failed edit leaves no trace")
	}
	clean := !e.Dirty()
	err := e.Batch(func(e *Engine) error {
		e.EditOrDrag(setAt("5", "children", 0, "x"))
		return e.EditOrDrag(setAt("6", "children", 0, "y"))
	})
	if err != nil || len(e.undo) != steps+1 || num(at(e.Doc.Root, "children", 0, "x")) != 5 {
		t.Fatal("a batch is one step")
	}
	e.markSaved()
	err = e.Batch(func(e *Engine) error {
		e.EditOrDrag(setAt("7", "children", 0, "x"))
		return fmt.Errorf("no")
	})
	if err == nil || len(e.undo) != steps+1 || num(at(e.Doc.Root, "children", 0, "x")) != 5 || e.Dirty() {
		t.Fatal("a failed batch is rolled back and keeps a clean document clean", clean)
	}
}

// Events past the cap drop the oldest thousand.
func TestEventsAreCapped(t *testing.T) {
	e := newEngine(t)
	for i := range eventLimit + 1 {
		e.Emit(jsonv.Int(int64(i)))
	}
	if len(e.Events) != eventLimit+1-eventDrop || e.Events[0] != jsonv.Int(eventDrop) {
		t.Fatal(len(e.Events), e.Events[0])
	}
	if got := e.TakeEvents(); len(got) != eventLimit+1-eventDrop || len(e.Events) != 0 {
		t.Fatal("taken")
	}
}

// Downloads deliver their results on their own goroutines under the engine lock while edits run, and a panic in
// a delivery is contained (run with -race).
func TestDownloadsDeliverUnderTheLock(t *testing.T) {
	e := newEngine(t)
	e.LoadTemplate("empty")
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			e.Assets.OnReady(fmt.Sprintf("http://x/%d.png", i), i%2 == 0)
		}()
		go func() {
			defer wg.Done()
			e.Fonts.OnReady("No Such Face", 400, false)
		}()
		e.Locked(func() {
			e.Edit(func(d *doc.Doc) error {
				_, err := d.Insert(doc.Root, jsonv.MustParse(`{"type": "rectangle", "width": 1, "height": 1}`), -1)
				return err
			})
			e.Sync()
		})
	}
	wg.Wait()
	e.Locked(func() {
		if len(e.Doc.Children()) != 50 || len(e.undo) != 50 {
			t.Fatal(len(e.Doc.Children()), len(e.undo))
		}
	})
	// A delivery that panics leaves the lock free and the engine usable.
	e.guard(func() { panic("boom") })
	e.Locked(func() { e.Touch() })
}

// The engine is the execute sandbox's host: a script inserts below the top level, the node slides in, and the
// document's ids come from the engine's generator.
func TestTheEngineHostsExecute(t *testing.T) {
	e := newEngine(t)
	e.LoadTemplate("empty")
	e.Edit(setChildren(`[{"id": "col", "type": "frame", "x": 0, "y": 0, "width": 200, "height": 200, "layout": "vertical"}]`))
	e.Seed(7)
	args := jsonv.NewObject()
	args.Set("input", `const r = Insert("col", {type: "rectangle", width: 20, height: 20}); const s = FindEmptySpace({width: 10, height: 10});`)
	out := script.Execute(e.Host(), args, script.Call{})
	if out.Error {
		t.Fatal(out.Text)
	}
	if len(jsonv.Items(at(e.Doc.Root, "children", 0, "children"))) != 1 || len(e.Renderer.Slides) != 1 {
		t.Fatal("inserted and sliding:", string(jsonv.Marshal(e.Doc.Root)), len(e.Renderer.Slides))
	}
	id := str(at(e.Doc.Root, "children", 0, "children", 0, "id"))
	e2 := newEngine(t)
	e2.Seed(7)
	if got := e2.Doc.GenID(); got != id {
		t.Errorf("ids come from the engine's generator: %s, %s", got, id)
	}
}

func TestStemAndExtension(t *testing.T) {
	for name, want := range map[string][2]string{"a.png": {"a", ".png"}, ".hidden": {".hidden", ""}, "x.tar.gz": {"x.tar", ".gz"}, "foo.": {"foo", "."}, "noext": {"noext", ""}} {
		if s, e := stemExt(name); s != want[0] || e != want[1] {
			t.Errorf("stemExt(%q) = %q %q", name, s, e)
		}
	}
}
