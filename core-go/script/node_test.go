package script

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script/scripttest"
)

// noText measures nothing: these documents hold no text.
type noText struct{}

func (noText) Metrics(layout.TextStyle) layout.Metrics               { panic("no text expected") }
func (noText) Width(string, layout.TextStyle) float64                { panic("no text expected") }
func (noText) Wrap(string, float64, layout.TextStyle) []resolve.Line { return nil }

func layoutNoText(t *testing.T, n *resolve.RNode) {
	t.Helper()
	layout.Layout(n, noText{})
}

func scriptDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func num(t *testing.T, v jsonv.Value, k string) float64 {
	t.Helper()
	f, ok := jsonv.F64(jsonv.Get(v, k))
	if !ok {
		t.Fatalf("%s is not a number in %s", k, jsonv.Marshal(v))
	}
	return f
}

// mulberry is the script prelude's Math.random.
func mulberry(s *uint32) float64 {
	*s += 0x6D2B79F5
	t := (*s ^ (*s >> 15)) * (1 | *s)
	t = (t + (t^(t>>7))*(61|t)) ^ t
	return float64(t^(t>>14)) / 4294967296
}

func errOf(t *testing.T, r Result) *Status {
	t.Helper()
	if r.Status == nil {
		t.Fatalf("no error: %d nodes", len(r.Nodes))
	}
	return r.Status
}

func desc(s *Status) string {
	if s.Description == nil {
		return ""
	}
	return *s.Description
}

func code(s *Status) string {
	if s.Code == nil {
		return ""
	}
	return *s.Code
}

func TestScriptNodeRuns(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"bars.js":     "/** @schema 2.10\n * @input count: number = 3 */\nconst out = [];\nfor (let i = 0; i < nalarupa.input.count; i++) out.push({type: 'rectangle', id: 'kept?', x: i * 10, y: 0, width: 8, height: nalarupa.height * Math.random(), fill: '#ff0000', stroke: {align: 'outside', thickness: 2, fill: '#000000'}});\nreturn out;",
		"none.js":     "/** @schema 2.19 */\nconst a = 1;",
		"throws.js":   "/** @schema 2.19 */\nconst a = 1;\nnull.boom;\nreturn [];",
		"loop.js":     "/** @schema 2.19 */\nwhile (true) {}",
		"many.js":     "/** @schema 2.19 */\nreturn Array.from({length: 1001}, () => ({type: 'rectangle', width: 1, height: 1}));",
		"noschema.js": "return [];",
	})
	inputs := jsonv.Obj(jsonv.MustParse(`{"count": 2}`))
	r := scriptNode("./bars.js", dir, inputs, 100, 50, "", nil)
	if r.Status != nil || len(r.Nodes) != 2 {
		t.Fatalf("%+v", r)
	}
	if jsonv.Obj(r.Nodes[0]).Has("id") {
		t.Fatal("ids are dropped")
	}
	if got := string(jsonv.Marshal(jsonv.Get(r.Nodes[0], "strokeWidth"))) + " " + string(jsonv.Marshal(jsonv.Get(r.Nodes[0], "strokeAlignment"))); got != `2 "outer"` {
		t.Fatalf("2.10 output is migrated: %s", got)
	}
	seed := uint32(0)
	for i := range 2 {
		if h, want := num(t, r.Nodes[i], "height"), 50*mulberry(&seed); math.Abs(h-want) > 1e-9 {
			t.Fatalf("node %d height %v, want mulberry32 seeded 0: %v", i, h, want)
		}
	}
	again := scriptNode("./bars.js", dir, inputs, 100, 50, "", nil)
	if fmt.Sprint(jsonv.Marshal(jsonv.NewArray(again.Nodes...))) != fmt.Sprint(jsonv.Marshal(jsonv.NewArray(r.Nodes...))) {
		t.Fatal("deterministic")
	}

	run := func(f string) *Status { return errOf(t, scriptNode(f, dir, nil, 10, 10, "", nil)) }
	if s := run(""); s.Kind != "empty" || s.Title != "No script file selected" {
		t.Fatalf("%+v", s)
	}
	if s := run("./none.js"); !strings.Contains(desc(s), "Script must return an array of nodes") {
		t.Fatalf("%+v", s)
	}
	s := run("./throws.js")
	if s.Title != "Script runtime error" || !strings.Contains(code(s), "> 3: null.boom;") {
		t.Fatalf("%+v %q", s, code(s))
	}
	started := time.Now()
	if s := run("./loop.js"); s.Title != "Script runtime error" || !strings.HasPrefix(desc(s), "InternalError: interrupted") {
		t.Fatalf("%+v", s)
	}
	// The budget runs on the engine's clock (QueryPerformanceCounter on Windows, as Rust's Instant); Go's clock there
	// advances by timer ticks, so it may read up to one tick (at most 15.6 ms) short.
	if el := time.Since(started); el < scriptTimeout-16*time.Millisecond || el > 4*time.Second {
		t.Fatalf("2 s budget: %v", el)
	}
	if s := run("./many.js"); s.Title != "Too many nodes" || desc(s) != "Script output cannot exceed 1000 nodes." {
		t.Fatalf("%+v", s)
	}
	if s := run("./noschema.js"); s.Title != "Missing @schema version tag" || desc(s) != "Add /** @schema 2.19 */ to the top of the script." {
		t.Fatalf("%+v", s)
	}
	if s := run("./missing.js"); s.Title != "Script not found" || desc(s) != "We could not find “./missing.js” at this path. Check for typos or missing folders." {
		t.Fatalf("%+v", s)
	}
	if s := errOf(t, scriptNode("./bars.js", "", nil, 10, 10, "", nil)); s.Title != "Cannot resolve script path: ./bars.js" || s.Description != nil {
		t.Fatalf("an unsaved document cannot resolve a relative path: %+v", s)
	}
	abs := filepath.Join(dir, "bars.js")
	if r := scriptNode("file://"+abs, "", nil, 10, 10, "", nil); r.Status != nil || len(r.Nodes) != 3 {
		t.Fatalf("an absolute file:// path needs no folder: %+v", r)
	}
}

// Header errors show the directive's error as the title.
func TestScriptNodeHeaderErrors(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"bad-schema.js": "/** @schema banana */\nreturn [{type: 'rectangle', width: 1, height: 1}];",
		"bad-input.js":  "/** @schema 2.19\n * @input x: vec3 */\nreturn [];",
		"bad-value.js":  "/** @schema 2.19\n * @input m: enum(\"a\") */\nreturn [];",
		"binary.js":     "/** @schema 2.19 */\nreturn ['\xff'];",
	})
	s := errOf(t, scriptNode("./bad-input.js", dir, nil, 10, 10, "", nil))
	if s.Title != "Invalid @input type 'vec3'. Must be one of: number, string, boolean, color, ref, enum" || s.Description != nil {
		t.Fatalf("%+v", s)
	}
	s = errOf(t, scriptNode("./bad-value.js", dir, jsonv.Obj(jsonv.MustParse(`{"m": "b"}`)), 10, 10, "", nil))
	if s.Title != "Invalid input value" || desc(s) != "Value 'b' is not allowed for input 'm'. Must be one of: a" {
		t.Fatalf("%+v", s)
	}
	// An unknown schema migrates as the current version (Migrate cannot read it).
	if r := scriptNode("./bad-schema.js", dir, nil, 10, 10, "", nil); r.Status != nil || len(r.Nodes) != 1 {
		t.Fatalf("%+v", r)
	}
	// A file that is not UTF-8 cannot be read as a script.
	if s := errOf(t, scriptNode("./binary.js", dir, nil, 10, 10, "", nil)); s.Title != "Script not found" {
		t.Fatalf("%+v", s)
	}
}

func TestScriptRuntimeErrors(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"err.js":    "/** @schema 2.19 */\nthrow new Error(\"boom\")",
		"str.js":    "/** @schema 2.19 */\nthrow \"plain\"",
		"undef.js":  "/** @schema 2.19 */\nthrow undefined;",
		"obj.js":    "/** @schema 2.19 */\nthrow {a: 1};",
		"sym.js":    "/** @schema 2.19 */\nthrow Symbol(\"x\");",
		"ref.js":    "/** @schema 2.19 */\n\nfoo();",
		"fn.js":     "/** @schema 2.19 */\nreturn () => 1;",
		"syntax.js": "/** @schema 2.19 */\nconst = 3",
		"named.js":  "/** @schema 2.19 */\nconst e = new TypeError('m'); e.name = 5; throw e;",
	})
	run := func(f string) *Status { return errOf(t, scriptNode(f, dir, nil, 10, 10, "", nil)) }
	for _, c := range []struct{ file, title, first, code string }{
		{"./err.js", "Script runtime error", "Error: boom", "  1: /** @schema 2.19 */\n> 2: throw new Error(\"boom\")"},
		{"./ref.js", "Script runtime error", "ReferenceError: foo is not defined", "  1: /** @schema 2.19 */\n  2: \n> 3: foo();"},
		{"./syntax.js", "Script compile error", "SyntaxError: variable name expected", "  1: /** @schema 2.19 */\n> 2: const = 3"},
		{"./named.js", "Script runtime error", "Error: m", "  1: /** @schema 2.19 */\n> 2: const e = new TypeError('m'); e.name = 5; throw e;"},
	} {
		s := run(c.file)
		first, stack, _ := strings.Cut(desc(s), "\n")
		if s.Title != c.title || first != c.first || !strings.HasPrefix(stack, "    at ") || code(s) != c.code {
			t.Errorf("%s: %+v %q %q", c.file, s, desc(s), code(s))
		}
	}
	// A thrown value that is not an Error shows as a string, with no code.
	for file, want := range map[string]string{"./str.js": "plain", "./undef.js": "undefined", "./obj.js": "[object Object]", "./sym.js": ""} {
		if s := run(file); s.Title != "Script runtime error" || desc(s) != want || s.Code != nil {
			t.Errorf("%s: %+v %q", file, s, desc(s))
		}
	}
	// A result JSON cannot hold is rquickjs's conversion error.
	if s := run("./fn.js"); desc(s) != "Error converting from js 'undefined' into type 'string'" || s.Code != nil {
		t.Errorf("%+v %q", s, desc(s))
	}
}

func TestScriptLimits(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		// The interrupt is uncatchable, so a try/catch around the loop cannot swallow it.
		"loop.js":  "/** @schema 2.19 */\nwhile (true) { try { while (true) {} } catch (err) {} }",
		"oom.js":   "/** @schema 2.19 */\nconst a = []\nfor (;;) a.push(new Array(100000).fill(1.5))",
		"big.js":   "/** @schema 2.19 */\nconst s = 'x'.repeat(100 * 1024 * 1024);\nreturn [];",
		"deep.js":  "/** @schema 2.19 */\nfunction f(n) { return f(n + 1) + 1 }\nreturn f(0)",
		"depth.js": "/** @schema 2.19 */\nlet d = 0; function f(n) { d = n; return f(n + 1) + 1 }\ntry { f(0) } catch (e) { return [{type: 'rectangle', width: d, height: 1, name: String(e)}] }",
		"slow.js":  "/** @schema 2.19 */\nlet s = 0; for (let i = 0; i < 3e5; i++) s += i;\nreturn [{type: 'rectangle', width: 1, height: 1}];",
	})
	run := func(f string) Result { return scriptNode(f, dir, nil, 10, 10, "", nil) }
	started := time.Now()
	if s := errOf(t, run("./loop.js")); !strings.HasPrefix(desc(s), "InternalError: interrupted\n    at ") || s.Code == nil {
		t.Fatalf("%+v %q", s, code(s))
	}
	if el := time.Since(started); el > 4*time.Second {
		t.Fatalf("no hang: %v", el)
	}
	for _, f := range []string{"./oom.js", "./big.js"} {
		if s := errOf(t, run(f)); s.Title != "Script runtime error" || !strings.HasPrefix(desc(s), "InternalError: out of memory") {
			t.Fatalf("%s: %+v %q", f, s, desc(s))
		}
	}
	s := errOf(t, run("./deep.js"))
	if !strings.HasPrefix(desc(s), "RangeError: Maximum call stack size exceeded\n") || strings.Count(desc(s), "\n    at ") != 10 || !strings.Contains(code(s), "> 2: function f") {
		t.Fatalf("%+v %q", s, desc(s))
	}
	// Caught, the overflow leaves the script running. A 512 KB stack holds at least the 146 frames Rust's MSVC
	// build reaches (gcc's smaller frames hold more: see differs in parity_test.go).
	r := run("./depth.js")
	if r.Status != nil || num(t, r.Nodes[0], "width") < 146 {
		t.Fatalf("%+v", r)
	}
	// The budget holds plain work comfortably (rquickjs runs this in well under a second).
	if r := run("./slow.js"); r.Status != nil {
		t.Fatalf("%+v %q", r.Status, desc(r.Status))
	}
}

func TestScriptOutputRules(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"reusable.js": "/** @schema 2.19 */\nreturn [{type: 'frame', children: [{type: 'frame', reusable: true}]}];",
		"conn.js":     "/** @schema 2.19 */\nreturn [{type: 'connection'}];",
		"num.js":      "/** @schema 2.19 */\nreturn [{type: 'frame', children: [1.5]}];",
		"obj.js":      "/** @schema 2.19 */\nreturn {a: 1};",
		"promise.js":  "/** @schema 2.19 */\nreturn Promise.resolve([{type: 'rectangle', width: 5, height: 5}]);",
		"nested.js":   "/** @schema 2.19 */\nconst kid = () => ({type: 'rectangle', width: 1, height: 1});\nreturn [{type: 'frame', children: Array.from({length: 999}, kid)}, kid()];",
		"exact.js":    "/** @schema 2.19 */\nreturn Array.from({length: 1000}, () => ({type: 'rectangle', width: 1, height: 1}));",
	})
	run := func(f string) Result { return scriptNode(f, dir, nil, 10, 10, "", nil) }
	for f, want := range map[string]string{
		"./reusable.js": "Invalid script output|Reusable nodes are not supported in script outputs.",
		"./conn.js":     "Invalid script output|Connection nodes are not supported in script outputs.",
		"./num.js":      "Error inserting script children|Expected a node object, got 1.5",
		"./obj.js":      "Invalid script output|Script must return an array of nodes.",
		"./promise.js":  "Invalid script output|Script must return an array of nodes.",
		"./nested.js":   "Too many nodes|Script output cannot exceed 1000 nodes.",
	} {
		if s := errOf(t, run(f)); s.Title+"|"+desc(s) != want {
			t.Errorf("%s: %s|%s", f, s.Title, desc(s))
		}
	}
	if r := run("./exact.js"); r.Status != nil || len(r.Nodes) != 1000 {
		t.Fatalf("1000 nodes are allowed: %+v", r.Status)
	}
}

// Ids are `sid/k` in pre-order, `ref` targets resolve against the document, and a dangling one fails.
func TestExpandScripts(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"tree.js":     "/** @schema 2.19 */\nreturn [{type: 'frame', id: 'x', x: 5, y: 6, width: 30, height: 20, layout: 'vertical', children: [{type: 'rectangle', width: 4, height: 4}, {type: 'ref', ref: 'comp'}]}, {type: 'ellipse', x: nalarupa.width - 10, width: 10, height: 10}];",
		"dangling.js": "/** @schema 2.19 */\nreturn [{type: 'frame', children: [{type: 'ref', ref: 'nope'}]}];",
	})
	root := jsonv.MustParse(`{"version": "2.19", "children": [
		{"type": "frame", "id": "comp", "x": 0, "y": 500, "width": 10, "height": 10, "reusable": true},
		{"type": "frame", "id": "page", "x": 100, "y": 50, "width": 300, "height": 200, "layout": "none", "children": [
			{"type": "script", "id": "s", "x": 10, "y": 20, "width": 120, "height": 80, "scriptUri": "./tree.js"},
			{"type": "script", "id": "d", "x": 0, "y": 0, "width": 50, "height": 50, "scriptUri": "./dangling.js"},
			{"type": "script", "id": "e", "x": 0, "y": 0, "width": 50, "height": 50}
		]}
	]}`)
	r := resolve.New(root, nil)
	n := r.Resolve(jsonv.Items(jsonv.Get(root, "children"))[1])
	layoutNoText(t, n)
	ExpandScripts(n, r, noText{}, dir, filepath.Join(dir, "doc.pen"), false, nil)
	s := n.Kids[0]
	var got []string
	s.Visit(func(k *resolve.RNode, _ int) {
		got = append(got, fmt.Sprintf("%s %s %v,%v %vx%v", k.ID, k.Type, k.X, k.Y, k.W, k.H))
	}, 0)
	want := []string{"s script 110,70 120x80", "s/0 frame 115,76 30x20", "s/1 rectangle 115,76 4x4", "s/2 frame 115,80 10x10", "s/3 ellipse 220,70 10x10"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if s.Kids[0].Kids[1].Inst != "comp" {
		t.Fatalf("ref output is an instance of comp: %+v", s.Kids[0].Kids[1])
	}
	d := n.Kids[1]
	st, _ := d.P.Get("__status")
	if len(d.Kids) != 0 || string(jsonv.Marshal(st)) != `{"kind":"error","title":"Component not found","description":"Component 'nope' could not be resolved.","code":null}` {
		t.Fatalf("%s", jsonv.Marshal(st))
	}
	st, _ = n.Kids[2].P.Get("__status")
	if string(jsonv.Marshal(st)) != `{"kind":"empty","title":"No script file selected","description":null,"code":null}` {
		t.Fatalf("%s", jsonv.Marshal(st))
	}
}

func TestPromisesAreNotRun(t *testing.T) {
	// Script nodes call the script and read its result: queued jobs never run, so a pending promise neither
	// adds output nor fails the run, and a self-feeding job loop costs nothing.
	dir := scriptDir(t, map[string]string{
		"async.js":  "/** @schema 2.19 */\nconst out = [{type: 'rectangle', width: 4, height: 4}];\n(async () => { await null; out.push({type: 'rectangle', width: 5, height: 5}) })();\nPromise.resolve().then(() => out.push({type: 'rectangle', width: 6, height: 6}));\nreturn out;",
		"reject.js": "/** @schema 2.19 */\nPromise.reject(new Error('x'));\nreturn [{type: 'rectangle', width: 7, height: 7}];",
		"spin.js":   "/** @schema 2.19 */\nconst f = () => Promise.resolve().then(f); f();\nreturn [{type: 'rectangle', width: 8, height: 8}];",
	})
	for f, w := range map[string]float64{"./async.js": 4, "./reject.js": 7, "./spin.js": 8} {
		started := time.Now()
		r := scriptNode(f, dir, nil, 10, 10, "", nil)
		if r.Status != nil || len(r.Nodes) != 1 || num(t, r.Nodes[0], "width") != w || time.Since(started) > time.Second {
			t.Errorf("%s: %+v %v", f, r, time.Since(started))
		}
	}
}

func TestCache(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"count.js": "/** @schema 2.19\n * @input n: number */\nreturn [{type: 'rectangle', width: nalarupa.input.n, height: nalarupa.width, name: String(Date.now() + Math.random())}];",
	})
	call := func(n, w float64) Result {
		return scriptNode("./count.js", dir, jsonv.Obj(jsonv.MustParse(fmt.Sprintf(`{"n": %v}`, n))), w, 10, "", nil)
	}
	name := func(r Result) string { return jsonv.StrOr(jsonv.Get(r.Nodes[0], "name")) }
	first := call(1, 10)
	time.Sleep(2 * time.Millisecond)
	if name(call(1, 10)) != name(first) {
		t.Fatal("the same source, inputs and size hit the cache")
	}
	if name(call(2, 10)) == name(first) || name(call(1, 11)) == name(first) {
		t.Fatal("another input or size runs again")
	}
	// A returned output is a copy: editing it leaves the cache as it was.
	jsonv.Obj(first.Nodes[0]).Set("name", "edited")
	if name(call(1, 10)) == "edited" {
		t.Fatal("the cache was edited through a result")
	}
	// Past 256 entries the cache is cleared before the next insert.
	before := name(call(1, 10))
	for i := range 300 {
		call(100+float64(i), 10)
	}
	scripts.Lock()
	size := len(scripts.cache)
	scripts.Unlock()
	if size > cacheSize+1 {
		t.Fatalf("cache holds %d", size)
	}
	if name(call(1, 10)) == before {
		t.Fatal("an evicted entry runs again")
	}
}

func TestSlots(t *testing.T) {
	// The cache is process-wide and keyed by the script text, not its folder: start from an empty one so a
	// repeated run (-count) or another test cannot turn the first miss into a hit.
	scripts.Lock()
	scripts.cache = nil
	delete(scripts.slots, "back-slot")
	scripts.Unlock()
	marker := fmt.Sprintf("held-%d", time.Now().UnixNano())
	started, release := scripttest.HoldRuns(t, SetRunHook, marker)
	dir := scriptDir(t, map[string]string{
		"slow.js": "/** @schema 2.19 */\n// " + marker + "\nreturn [{type: 'rectangle', width: nalarupa.width, height: 10}];",
	})
	call := func(w float64) (r Result) {
		scripttest.Returns(t, "a live call", func() { r = scriptNode("./slow.js", dir, nil, w, 10, "back-slot", nil) })
		return r
	}
	wait := func(until func() bool) {
		t.Helper()
		limit := time.Now().Add(time.Minute)
		for !until() {
			if time.Now().After(limit) {
				t.Fatal("timed out")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	nextEmpty := func() bool {
		scripts.Lock()
		defer scripts.Unlock()
		return scripts.slots["back-slot"].next == nil
	}
	before := Finished()
	// The run is held: a call that waited for it would never return.
	if s := errOf(t, call(201)); s.Kind != "running" || s.Title != "Running script" {
		t.Fatalf("a live miss returns at once as running: %+v", s)
	}
	if !Busy() {
		t.Fatal("the run is in the background")
	}
	scripttest.Started(t, started, "the worker has taken it")
	wait(nextEmpty)
	call(202)
	call(201)
	if !nextEmpty() {
		t.Fatal("back to the size that is running drops the stale one waiting behind it")
	}
	release()
	wait(func() bool { return !Busy() })
	r := call(201)
	if r.Status != nil || num(t, r.Nodes[0], "width") != 201 || Finished() != before+1 {
		t.Fatalf("%+v finished %d", r, Finished()-before)
	}
	// A newer size shows the last output while it runs.
	if r := call(203); r.Status != nil || num(t, r.Nodes[0], "width") != 201 {
		t.Fatalf("%+v", r)
	}
	wait(func() bool { return !Busy() })
	if r := call(203); r.Status != nil || num(t, r.Nodes[0], "width") != 203 {
		t.Fatalf("%+v", r)
	}
}

func TestWorkersAndEviction(t *testing.T) {
	// Twelve slots with eight workers: the rest wait and are picked up as workers free. The runs are held, so all
	// eight are still busy when counted.
	marker := fmt.Sprintf("held-%d", time.Now().UnixNano())
	started, release := scripttest.HoldRuns(t, SetRunHook, marker)
	dir := scriptDir(t, map[string]string{
		"slow.js": "/** @schema 2.19 */\n// " + marker + "\nreturn [{type: 'rectangle', width: nalarupa.width, height: 1}];",
	})
	for i := range 12 {
		scripttest.Returns(t, "a live call", func() { scriptNode("./slow.js", dir, nil, 300+float64(i), 1, fmt.Sprint("w", i), nil) })
	}
	for range workers {
		scripttest.Started(t, started, "a worker runs")
	}
	scripts.Lock()
	active := scripts.active
	scripts.Unlock()
	if active != workers {
		t.Fatalf("%d workers", active)
	}
	release()
	limit := time.Now().Add(time.Minute)
	for Busy() {
		if time.Now().After(limit) {
			t.Fatal("the waiting slots never ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i := range 12 {
		if r := scriptNode("./slow.js", dir, nil, 300+float64(i), 1, fmt.Sprint("w", i), nil); r.Status != nil {
			t.Fatalf("slot %d: %+v", i, r.Status)
		}
	}
	// At 1024 slots the longest-unused idle quarter is dropped.
	scripts.Lock()
	for i := len(scripts.slots); i < slots; i++ {
		scripts.slots[fmt.Sprint("idle", i)] = &slot{}
	}
	scripts.slots["busy"] = &slot{running: true}
	scripts.Unlock()
	scriptNode("./slow.js", dir, nil, 999, 1, "w0", nil)
	scripts.Lock()
	n, busy := len(scripts.slots), scripts.slots["busy"] != nil
	delete(scripts.slots, "busy")
	for k := range scripts.slots {
		if strings.HasPrefix(k, "idle") {
			delete(scripts.slots, k)
		}
	}
	scripts.Unlock()
	if n != slots+1-slots/4 || !busy {
		t.Fatalf("%d slots after eviction, busy kept %v", n, busy)
	}
	for Busy() {
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRecordMode(t *testing.T) {
	marker := fmt.Sprintf("held-%d", time.Now().UnixNano())
	_, release := scripttest.HoldRuns(t, SetRunHook, marker)
	dir := scriptDir(t, map[string]string{
		"slow.js": "/** @schema 2.19 */\n// " + marker + "\nreturn [{type: 'rectangle', width: nalarupa.width, height: nalarupa.height}];",
	})
	draws := 0
	draw := func(rec *Runs) Result {
		draws++
		return scriptNode("./slow.js", dir, nil, 77, 7, "", rec)
	}
	// The run is held: a draw that ran it would never return.
	rec := &Runs{}
	var r Result
	scripttest.Returns(t, "a recorded draw", func() { r = draw(rec) })
	if s := errOf(t, r); s.Kind != "running" {
		t.Fatalf("a recorded miss draws as running at once: %+v", s)
	}
	scripttest.Returns(t, "a recorded draw", func() { draw(rec) })
	release()
	if rec.IsEmpty() || len(rec.jobs) != 1 {
		t.Fatalf("one run noted: %d", len(rec.jobs))
	}
	other := &Runs{}
	other.Extend(rec)
	other.Extend(rec)
	if len(other.jobs) != 1 {
		t.Fatal("Extend skips runs already noted")
	}
	draws = 0
	r = OffLock(draw)
	if r.Status != nil || num(t, r.Nodes[0], "width") != 77 || draws != 2 {
		t.Fatalf("OffLock runs the noted run and draws again: %+v draws %d", r, draws)
	}
	draws = 0
	if r := OffLock(draw); r.Status != nil || draws != 1 {
		t.Fatalf("a cached output draws once: %d", draws)
	}
}

// Runs on many goroutines at once each get their own runtime and give what they give one at a time.
func TestParallelRunsMatchSerial(t *testing.T) {
	srcs := []string{
		"return Array.from({length: 20}, (_, i) => ({type: 'rectangle', width: Math.random() * 100, height: i}));",
		"function f(n) { return f(n + 1) + 1 }\nreturn f(0);",
		"const a = [];\nfor (;;) a.push(new Array(100000).fill(1.5));",
		"null.boom;",
		"let o = {}; for (let i = 0; i < 400; i++) o = {a: o};\nreturn [{type: 'rectangle', width: JSON.stringify(o).length, height: 1}];",
	}
	jobs := make([]job, 4*len(srcs))
	for i := range jobs {
		jobs[i] = job{src: "/** @schema 2.19 */\n" + srcs[i%len(srcs)], file: fmt.Sprintf("p%d.js", i%len(srcs)), input: `{"width":10,"height":10,"input":{}}`, schema: "2.19"}
	}
	text := func(r Result) string {
		if r.Status != nil {
			return r.Status.Title + "|" + desc(r.Status) + "|" + code(r.Status)
		}
		return string(jsonv.Marshal(jsonv.NewArray(r.Nodes...)))
	}
	want := make([]string, len(jobs))
	for i := range jobs {
		want[i] = text(runJob(&jobs[i]))
	}
	got := make([]string, len(jobs))
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = text(runJob(&jobs[i]))
		}()
	}
	wg.Wait()
	for i := range jobs {
		if got[i] != want[i] {
			t.Errorf("job %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}
