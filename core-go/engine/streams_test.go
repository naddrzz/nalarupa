package engine

// The streaming tests of script.rs and engine.rs (save_as_moves_the_live_preview), then the 8d matrix: rewrites,
// several streams, errors, endless loops and Generate in a preview, and the live contexts' lifetime.

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script/scripttest"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

func (e *Engine) stream(key, agent, source string) {
	e.Locked(func() { e.StreamUpdate(key, agent, source) })
}

func (e *Engine) endStream(key string) {
	e.Locked(func() { e.StreamEnd(key) })
}

func names(nodes []jsonv.Value) []string {
	out := []string{}
	for _, n := range nodes {
		out = append(out, str(jsonv.Get(n, "name")))
	}
	return out
}

func (e *Engine) previewNames() []string {
	if e.Preview == nil {
		return nil
	}
	return names(e.Preview.Children())
}

func (e *Engine) streamKeys() []string {
	var out []string
	for _, s := range e.Streams {
		out = append(out, s.Key)
	}
	return out
}

// A streamed step that gives a sliding card glass stops the slide: the card lives in the preview document, which is
// the one Sync renders.
func TestGlassAddedByAStreamedUpdateUnslides(t *testing.T) {
	e := emptyEngine(t)
	head := "root = Insert(document, {type:\"frame\", name:\"Root\", width:300, height:300, layout:\"none\"})\ncard = Insert(root, {type:\"frame\", name:\"Card\", width:100, height:100, layout:\"none\"})\n"
	full := head + "Update(card, {effect:{type:\"background_blur\", radius:20}})\nx = 1\n"
	e.stream("toolu_1", "ag0", head+"Update(card")
	e.Locked(e.Sync)
	if len(e.Doc.Children()) != 0 {
		t.Fatal("the card exists only in the preview")
	}
	if len(e.Renderer.Slides) != 1 {
		t.Fatal("a plain streamed card slides", len(e.Renderer.Slides))
	}
	e.stream("toolu_1", "ag0", full)
	e.Locked(e.Sync)
	if len(e.Renderer.Slides) != 0 {
		t.Fatal("the streamed update gave the card glass")
	}
}

func TestStreamingPreviewThenCommit(t *testing.T) {
	e := emptyEngine(t)
	full := "card = Insert(document, {type:\"frame\", name:\"Card\", width:200, height:100, fill:\"#ffffff\"})\nInsert(card, {type:\"text\", name:\"Title\", content:\"Hi\", fill:\"#111111\"})\n"
	cut := strings.Index(full, "Insert(card") + 10
	e.stream("toolu_1", "ag0", full[:cut])
	if len(e.Doc.Children()) != 0 {
		t.Fatal("real document untouched while streaming")
	}
	if e.Preview == nil || len(e.Preview.Children()) != 1 {
		t.Fatal("first complete statement is visible")
	}
	previewID := str(jsonv.Get(e.Preview.Children()[0], "id"))
	if !slices.ContainsFunc(e.Events, func(ev jsonv.Value) bool {
		return str(jsonv.Get(ev, "type")) == "agent_activity" && jsonv.IsTrue(jsonv.Get(ev, "streaming"))
	}) {
		t.Fatal("no streaming activity", e.Events)
	}
	mustOK(t, e.testExecute(t, full, "__stream", "toolu_1", "__agent", "ag0"))
	if e.Preview != nil || len(e.Streams) != 0 {
		t.Fatal("stream retired")
	}
	if got := str(jsonv.Get(e.Doc.Children()[0], "id")); got != previewID {
		t.Fatal("final run keeps the previewed ids", got, previewID)
	}
	if len(jsonv.Items(jsonv.Get(e.Doc.Children()[0], "children"))) != 1 {
		t.Fatal(e.Doc.Children())
	}
	if !e.Undo() || len(e.Doc.Children()) != 0 {
		t.Fatal("one undo step for the whole call")
	}
}

func TestKeylessExecuteOnlyTakesItsOwnAgentsStream(t *testing.T) {
	e := emptyEngine(t)
	common := "pos = {x: 0, y: 0}\n"
	frame := func(name string) string {
		return common + "Insert(document, {type:\"frame\", name:\"" + name + "\", width:10, height:10})"
	}
	// Agent B streams a script that starts with the same statement as agent A's final call.
	e.stream("toolu_b", "agB", common+"Insert(")
	e.Events = nil
	// Claude Code's MCP `execute` carries no `__stream`, only the caller's `__agent`.
	mustOK(t, e.testExecute(t, frame("A frame"), "__agent", "agA"))
	if len(e.Streams) != 1 {
		t.Fatal("agent B's stream is untouched")
	}
	var writes []jsonv.Value
	for _, ev := range e.Events {
		if str(jsonv.Get(ev, "type")) == "agent_activity" && str(jsonv.Get(ev, "op")) == "write" && jsonv.IsFalse(jsonv.Get(ev, "streaming")) {
			writes = append(writes, ev)
		}
	}
	if len(writes) == 0 || slices.ContainsFunc(writes, func(ev jsonv.Value) bool { return str(jsonv.Get(ev, "agent")) != "agA" }) {
		t.Fatal(writes)
	}
	// The owner's own keyless call still retires its stream.
	mustOK(t, e.testExecute(t, frame("B frame"), "__agent", "agB"))
	if len(e.Streams) != 0 || e.Preview != nil {
		t.Fatal("B's stream is retired")
	}
}

func TestAKeyedExecuteTakesItsOwnStreamBeforeAnEarlierPrefixMatch(t *testing.T) {
	e := emptyEngine(t)
	common := "pos = {x: 0, y: 0}\n"
	e.stream("toolu_1", "agA", common+"Insert(")
	e.stream("toolu_2", "agA", common+"Insert(")
	mustOK(t, e.testExecute(t, common+"Insert(document, {type:\"frame\", name:\"Two\", width:10, height:10})", "__stream", "toolu_2", "__agent", "agA"))
	if !slices.Equal(e.streamKeys(), []string{"toolu_1"}) {
		t.Fatal("the keyed stream is retired, the earlier one stays", e.streamKeys())
	}
	e.endStream("toolu_1")
	if len(e.Script.live) != 0 || e.Preview != nil {
		t.Fatal("every context is freed")
	}
}

func TestStreamingPreviewsAreNotEdits(t *testing.T) {
	e := emptyEngine(t)
	// Stop in the middle of a streamed call: the preview goes away, the document never changed.
	src := "Insert(document, {type:\"frame\", name:\"B\", width:10, height:10})\nInsert("
	e.stream("toolu_stop", "ag0", src)
	if e.Preview == nil || len(e.Preview.Children()) != 1 {
		t.Fatal("preview exists")
	}
	if e.Dirty() {
		t.Fatal("a streaming preview is not an edit")
	}
	e.endStream("toolu_stop")
	if e.Preview != nil || len(e.Doc.Children()) != 0 || e.Dirty() {
		t.Fatal("a stopped stream is not an edit")
	}
	// A document that really has unsaved changes stays dirty through the same paths.
	mustOK(t, e.testExecute(t, "Insert(document, {type:\"frame\", name:\"C\", width:10, height:10})"))
	e.stream("toolu_stop", "ag0", src)
	e.endStream("toolu_stop")
	if !e.Dirty() {
		t.Fatal("still dirty")
	}
}

// A streamed call that hits a limit leaves the preview instead of re-running on every update (memory_and_stack_limits).
func TestAStreamAtTheMemoryLimitStalls(t *testing.T) {
	e := emptyEngine(t)
	e.stream("k", "ag0", "big = []\nfor (;;) big.push(new Array(100000).fill(1.5))\nx = 1")
	if l := e.Script.live["k"]; l == nil || !l.Stalled() {
		t.Fatal("stalled")
	}
	mustOK(t, e.testExecute(t, "Insert(document, {type:'frame'})"))
}

func TestIncrementalStreamingMatchesColdRun(t *testing.T) {
	src := `stamp = Math.random()
const card = (p, t) => Insert(p, {type: "frame", name: t, width: 120, height: 40, fill: "#ffffff"})
page = Insert(document, {type: "frame", name: "Page", layout: "vertical", gap: 8, width: 400})
for (let i = 0; i < 5; i++) card(page, "Card " + i)
let first = Get(page, {depth: 1}).children[0].id
Update(first, {fill: "#ff0000"})
copied = Copy(first, page, {name: "Copied"})
Move(copied, page, 0)
SetVariables({accent: {type: "color", value: "#3366ff"}})
Delete(Get(page, {depth: 1}).children[2].id)
Insert(page, {type: "text", name: "Done", content: "done", fill: "#111111"})
`
	seed := svg.FxHash("toolu_s")
	cold := emptyEngine(t)
	mustOK(t, cold.testExecute(t, src, "__seed", u64(seed)))

	e := emptyEngine(t)
	firstStamp := ""
	for i := 5; ; i += 5 {
		i = min(i, len(src))
		e.stream("toolu_s", "ag0", src[:i])
		if l := e.Script.live["toolu_s"]; firstStamp == "" && l != nil && strings.Contains(l.Ran(), "stamp") {
			firstStamp = l.Global("stamp")
		}
		if i == len(src) {
			break
		}
	}
	// The last statement is held until the next token shows it ended.
	e.stream("toolu_s", "ag0", src+"Print(1)")
	l := e.Script.live["toolu_s"]
	if l.Broken() {
		t.Fatal("broken")
	}
	if got := l.Global("stamp"); firstStamp == "" || got != firstStamp {
		t.Fatal("statements already run are not run again", got, firstStamp)
	}
	if !jsonv.Equal(e.Preview.Root, cold.Doc.Root) {
		t.Fatalf("incremental preview equals a cold run\n%s\n%s", jsonv.Marshal(e.Preview.Root), jsonv.Marshal(cold.Doc.Root))
	}
	mustOK(t, e.testExecute(t, src, "__stream", "toolu_s", "__agent", "ag0"))
	if e.Preview != nil || len(e.Script.live) != 0 {
		t.Fatal("stream retired")
	}
	if !jsonv.Equal(e.Doc.Root, cold.Doc.Root) {
		t.Fatal("final run keeps the previewed ids")
	}
	if !e.Undo() || len(e.Doc.Children()) != 0 {
		t.Fatal("one undo step")
	}
}

func TestStreamingStatementErrorMidway(t *testing.T) {
	e := emptyEngine(t)
	src := `a = Insert(document, {type:'frame', name:'A', width:10, height:10})
mk('B')
function mk(n) { return Insert(document, {type:'frame', name:n, width:10, height:10}) }
Update('nope', {x: 1})
Insert(document, {type:'frame', name:'C', width:10, height:10})
`
	upto := func(s string) string { return src[:strings.Index(src, s)+len(s)] }
	// `mk` is not declared yet when its call runs on its own: the statement throws.
	e.stream("k", "ag0", upto("mk('B')\nfunction mk"))
	if !e.Script.live["k"].Broken() || !slices.Equal(e.previewNames(), []string{"A"}) {
		t.Fatal(e.previewNames())
	}
	// Once the declaration arrives, the stream re-runs from scratch in one piece and it hoists.
	e.stream("k", "ag0", upto("}\nUpdate("))
	if e.Script.live["k"].Broken() || !slices.Equal(e.previewNames(), []string{"A", "B"}) {
		t.Fatal(e.previewNames())
	}
	// A real error stops the preview there; later statements do not run.
	e.stream("k", "ag0", src+"x = 1")
	if !e.Script.live["k"].Broken() || !slices.Equal(e.previewNames(), []string{"A", "B"}) {
		t.Fatal(e.previewNames())
	}
	out := e.testExecute(t, src, "__stream", "k", "__agent", "ag0")
	if !out.Error || !strings.Contains(out.Text, "node not found: nope (line 4)") || !strings.Contains(out.Text, "editId") {
		t.Fatal(out.Text)
	}
	if len(e.Doc.Children()) != 0 || e.Preview != nil || len(e.Streams) != 0 {
		t.Fatal("rolled back and retired")
	}
}

func TestSaveAsMovesTheLivePreview(t *testing.T) {
	dir := t.TempDir()
	e := New(filepath.Join(dir, "data"))
	e.Doc.Path = filepath.Join(dir, "a", "x.pen")
	e.Doc.Root = root(`{"children": [{"id": "i", "type": "rectangle", "fill": {"type": "image", "url": "./img.png"}}]}`)
	e.StreamUpdate("k", "agent", "")
	if e.Preview == nil {
		t.Fatal("no preview")
	}
	if _, err := e.Save(filepath.Join(dir, "b", "y.pen")); err != nil {
		t.Fatal(err)
	}
	if e.Preview.Path != e.Doc.Path {
		t.Fatal(e.Preview.Path, e.Doc.Path)
	}
	if got := str(at(e.Preview.Root, "children", 0, "fill", "url")); got != "../a/img.png" {
		t.Fatal(got)
	}
	if e.Dirty() {
		t.Fatal("dirty")
	}
	e.StreamEnd("k")
}

// Rewrite: a source that is not an append of what ran rebuilds the preview from scratch.
func TestARewrittenStreamRebuildsThePreview(t *testing.T) {
	e := emptyEngine(t)
	e.stream("k", "ag0", "Insert(document, {type:'frame', name:'A', width:10, height:10})\nInsert(document, {type:'frame', name:'B', width:10, height:10})\nq(")
	if !slices.Equal(e.previewNames(), []string{"A", "B"}) {
		t.Fatal(e.previewNames())
	}
	ids := slices.Clone(e.Script.live["k"].Touched())
	e.stream("k", "ag0", "Insert(document, {type:'frame', name:'A', width:10, height:10})\nInsert(document, {type:'frame', name:'Z', width:10, height:10})\nq(")
	if !slices.Equal(e.previewNames(), []string{"A", "Z"}) {
		t.Fatal(e.previewNames())
	}
	// The same seed from the start: the same ids.
	if got := e.Script.live["k"].Touched(); !slices.Equal(got, ids) {
		t.Fatal(got, ids)
	}
	// A user edit while streaming layers the preview on the new document.
	e.Locked(func() {
		if _, err := e.InsertRoot(root(`{"type":"frame","name":"U","width":5,"height":5}`)); err != nil {
			t.Fatal(err)
		}
	})
	if !slices.Equal(e.previewNames(), []string{"U", "A", "Z"}) || !slices.Equal(names(e.Doc.Children()), []string{"U"}) {
		t.Fatal(e.previewNames(), names(e.Doc.Children()))
	}
}

// Errors in a preview: an endless loop stops at the 2 s budget and the stream stalls (out of the preview, never run
// again); Generate is skipped, so no download starts.
func TestEndlessLoopsAndGenerateInAPreview(t *testing.T) {
	e := emptyEngine(t)
	start := time.Now()
	e.stream("loop", "ag0", "Insert(document, {type:'frame', name:'L', width:10, height:10})\nwhile (true) {}\nx = 1\nq(")
	if d := time.Since(start); d < 2*time.Second {
		t.Fatal("the preview budget", d)
	}
	l := e.Script.live["loop"]
	if l == nil || !l.Stalled() || len(l.Touched()) != 1 {
		t.Fatal("stalled after its insert")
	}
	ran := l.Ran()
	e.stream("loop", "ag0", "Insert(document, {type:'frame', name:'L', width:10, height:10})\nwhile (true) {}\nx = 1\nq(1)\ny = 2\nz")
	if e.Script.live["loop"] != l || l.Ran() != ran {
		t.Fatal("a stalled stream does not run again")
	}
	e.stream("gen", "ag0", "f = Insert(document, {type:'frame', name:'G', width:10, height:10})\nGenerate('ai', f, 'a cat')\nGenerate('nope', f, 'x')\nx")
	if l := e.Script.live["gen"]; l == nil || l.Broken() {
		t.Fatal("Generate is skipped, even an unknown type")
	}
	if len(e.Inflight) != 0 || strings.Contains(string(jsonv.Marshal(e.Preview.Root)), "pending") {
		t.Fatal("no download in a preview")
	}
	// The stalled stream stays out of a rebuild; its context is kept until it ends.
	e.endStream("gen")
	if len(e.previewNames()) != 0 || e.Script.live["loop"] != l {
		t.Fatal(e.previewNames())
	}
	e.endStream("loop")
	if e.Preview != nil || len(e.Script.live) != 0 {
		t.Fatal("ended")
	}
}

// Opening a document frees the live contexts; the streams go on in fresh ones on the new document.
func TestOpeningADocumentRestartsTheStreams(t *testing.T) {
	e := emptyEngine(t)
	e.stream("k", "ag0", "Insert(document, {type:'frame', name:'A', width:10, height:10})\nq(")
	old := e.Script.live["k"]
	e.Locked(func() { e.LoadTemplate("empty") })
	if l := e.Script.live["k"]; l == nil || l == old || !slices.Equal(e.previewNames(), []string{"A"}) {
		t.Fatal("restarted", e.previewNames())
	}
	e.endStream("k")
}

// Streams update under the lock while other goroutines poll and render, and while a shared execute runs (run with
// -race). Every context is freed once the streams end.
func TestStreamsUnderConcurrency(t *testing.T) {
	e := emptyEngine(t)
	goroutines := runtime.NumGoroutine()
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			e.Locked(func() { e.Render(64, 48, 0, 0, 1, [3]uint8{255, 255, 255}, "", true); e.TakeEvents() })
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.CallTool("execute", execArgs("for (let i = 0; i < 20; i++) Insert(document, {type:'frame', name:'X'+i, width:5, height:5})", "__agent", "agX")).Finish(e)
	}()
	src := ""
	for i := range 20 {
		src += "Insert(document, {type:'frame', name:'S" + string(rune('a'+i)) + "', width:10, height:10})\n"
		e.stream("k1", "ag1", src+"x")
		e.stream("k2", "ag2", "a = 1\n"+src+"y")
	}
	close(done)
	wg.Wait()
	mustOK(t, e.CallTool("execute", execArgs(src, "__stream", "k1", "__agent", "ag1")).Finish(e))
	e.endStream("k2")
	if len(e.Script.live) != 0 || e.Preview != nil {
		t.Fatal("every context is freed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > goroutines && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > goroutines {
		t.Fatal("goroutines left", n, goroutines)
	}
}

// A stream update that lands while another call's script runs with the lock released does not move that call's
// ids: they equal a run of the same call without the stream (Rust's generators are per thread).
func TestAStreamStepMidExecuteKeepsTheCallsIDs(t *testing.T) {
	// The call inserts A, waits until the `go` variable is set, then inserts B.
	src := "Insert(document, {type:'frame', name:'A', width:10, height:10})\nwhile (!GetVariables().variables.go) {}\nInsert(document, {type:'frame', name:'B', width:10, height:10})"
	setGo := func(e *Engine) {
		e.Locked(func() { e.Doc.Root.Set("variables", root(`{"go": {"type": "boolean", "value": true}}`)) })
	}
	ids := func(e *Engine) []string {
		var out []string
		e.Locked(func() {
			for _, n := range e.Doc.Children() {
				out = append(out, str(jsonv.Get(n, "id")))
			}
		})
		return out
	}
	alone := emptyEngine(t)
	setGo(alone)
	mustOK(t, alone.CallTool("execute", execArgs(src, "__seed", u64(5))).Finish(alone))

	e := emptyEngine(t)
	done := make(chan ToolOut, 1)
	go func() { done <- e.CallTool("execute", execArgs(src, "__seed", u64(5), "__agent", "agX")).Finish(e) }()
	running := make(chan struct{})
	go func() {
		for {
			var r bool
			e.Locked(func() { r = e.Executing })
			if r {
				close(running)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	scripttest.Within(t, running, "the call never ran")
	e.stream("k", "agY", "Insert(document, {type:'frame', name:'S', width:10, height:10})\nInsert(document, {type:'frame', name:'T', width:10, height:10})\nq(")
	if l := e.Script.live["k"]; l == nil || len(l.Touched()) != 2 {
		t.Fatal("the stream step ran")
	}
	setGo(e)
	mustOK(t, scripttest.Within(t, done, "the call never ended"))
	if got, want := ids(e), ids(alone); !slices.Equal(got, want) {
		t.Fatal("the call's ids moved:", got, want)
	}
	e.endStream("k")
}

// Ending one agent's streams leaves the other agents' alone, rebuilding the preview once per stream it ends.
func TestStreamsOfAgentEnd(t *testing.T) {
	e := emptyEngine(t)
	e.stream("a1", "agA", "Insert(document, {type:'frame', name:'A1', width:10, height:10})\nq(")
	e.stream("b1", "agB", "x = 1\nq(")
	e.stream("a2", "agA", "Insert(document, {type:'frame', name:'A2', width:10, height:10})\nq(")
	if !slices.Equal(e.previewNames(), []string{"A1", "A2"}) {
		t.Fatal(e.previewNames())
	}
	b := e.Script.live["b1"]
	version := e.Version
	e.Locked(func() { e.StreamsOfAgentEnd("agA") })
	// Each end rebuilds once: the first re-runs a2 (one insert) and b1 (no edits), the second b1 only, and each
	// redraws: 2 + 1.
	if got := e.Version - version; got != 3 {
		t.Fatal("rebuilds", got)
	}
	if !slices.Equal(e.streamKeys(), []string{"b1"}) || e.Preview == nil || len(e.previewNames()) != 0 {
		t.Fatal(e.streamKeys(), e.previewNames())
	}
	if l := e.Script.live["b1"]; l == nil || l == b || len(e.Script.live) != 1 {
		t.Fatal("agB's stream runs on in a fresh context")
	}
	e.endStream("b1")
}
