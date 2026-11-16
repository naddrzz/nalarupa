package engine

// The engine-bound tests of script.rs (the transaction, slides, screenshots, exports, the running script and
// Stop), then the engine's own: Generate downloads, the gate, refused calls and the icon settle.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/script/scripttest"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// execArgs is the tool arguments {input: src} plus key, value pairs.
func execArgs(src string, kv ...any) *jsonv.Object {
	return Object(append([]any{"input", src}, kv...)...)
}

// execute runs one execute call under the lock, through the engine's tool (script.rs `execute(&mut e, ...)`).
func (e *Engine) testExecute(t *testing.T, src string, kv ...any) ToolOut {
	t.Helper()
	var out ToolOut
	e.Locked(func() { out = e.Tool("execute", execArgs(src, kv...)) })
	return out
}

func emptyEngine(t *testing.T) *Engine {
	t.Helper()
	e := newEngine(t)
	e.LoadTemplate("empty")
	return e
}

func mustOK(t *testing.T, out ToolOut) {
	t.Helper()
	if out.Error {
		t.Fatal(out.Text)
	}
}

func TestEngineBadChildrenFailTheCallWithARepairableError(t *testing.T) {
	e := emptyEngine(t)
	for _, kids := range []string{`["str"]`, "[5]", "[null]", `[{type:"nope",name:"bad"}]`} {
		out := e.testExecute(t, fmt.Sprintf(`Insert(document, {type:"frame", width:10, height:10, children:%s})`, kids))
		if !out.Error || !strings.Contains(out.Text, "children[0]") || !strings.Contains(out.Text, "editId") {
			t.Fatal(kids, out.Text)
		}
		if len(e.Doc.Children()) != 0 || e.Executing {
			t.Fatal(kids, "rolled back")
		}
	}
}

func TestEngineExecuteTransactionAndRepair(t *testing.T) {
	e := emptyEngine(t)
	ok := e.testExecute(t, `
            card = Insert(document, {type:"frame", name:"Card", layout:"vertical", padding:16, gap:8, x:0, y:0});
            for (const t of ["A","B"]) Insert(card, {type:"text", name:"T"+t, content:t, fill:"#111111", fontSize:14});
            Print(Get(card, {depth:1}).children.length, Get(n => n.type === "text" ? n.name : undefined));
        `)
	mustOK(t, ok)
	if !strings.Contains(ok.Text, `"Card"`) || !strings.Contains(ok.Text, `2 ["TA","TB"]`) {
		t.Fatal(ok.Text)
	}
	if len(e.Doc.Children()) != 1 || !e.Script.Globals["agent"].Has("card") {
		t.Fatal("one frame, card kept")
	}
	bad := e.testExecute(t, "Insert(document,{type:'frame',name:'X'}); Update('nope', {x:1});")
	if !bad.Error || len(e.Doc.Children()) != 1 {
		t.Fatal("rolled back", bad.Text)
	}
	e.Saved = e.Version
	again := e.testExecute(t, "Insert(document,{type:'frame',name:'Y'}); Update('nope', {x:1});")
	if !again.Error || e.Dirty() {
		t.Fatal("a rolled-back call leaves a clean document clean", again.Text)
	}
	editID := regexp.MustCompile(`editId: "(\w+)"`).FindStringSubmatch(bad.Text)[1]
	var fixed ToolOut
	e.Locked(func() {
		fixed = e.Tool("execute", Object("editId", editID, "edits", jsonv.MustParse(`[{"find": "Update('nope', {x:1});", "replace": "Update(card, {x:5});"}]`)))
	})
	mustOK(t, fixed)
	if len(e.Doc.Children()) != 2 || e.Script.Failed[editID] != "" {
		t.Fatal("repaired and forgotten")
	}
	e.Locked(func() { e.Undo() })
	if len(e.Doc.Children()) != 1 {
		t.Fatal("the repair is one undo step")
	}
}

// Every inserted node used to slide on its own: few slides start, and none inside another.
func TestBulkInsertsSlideOnlyAFewTopNodes(t *testing.T) {
	e := emptyEngine(t)
	mustOK(t, e.testExecute(t, `
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            for (let i = 0; i < 300; i++) Insert(root, {type:"rectangle", name:"r"+i, x:i*20, y:0, width:10, height:10});`))
	if len(e.Renderer.Slides) != 32 {
		t.Fatal(len(e.Renderer.Slides))
	}
	e.Locked(e.Sync)
	if len(e.Renderer.Slides) != 32 {
		t.Fatal(len(e.Renderer.Slides))
	}
	e = emptyEngine(t)
	mustOK(t, e.testExecute(t, `
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none"});
            for (let i = 0; i < 5; i++) Insert(card, {type:"rectangle", name:"r"+i, width:10, height:10});`))
	if len(e.Renderer.Slides) != 1 {
		t.Fatal("the card carries its children along", len(e.Renderer.Slides))
	}
}

// A rolled-back script's slides have no node to paint; slides of nodes that survive are kept.
func TestARolledBackScriptLeavesNoSlidesBehind(t *testing.T) {
	e := emptyEngine(t)
	mustOK(t, e.testExecute(t, `
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            kept = Insert(root, {type:"rectangle", name:"Kept", width:10, height:10});`))
	if len(e.Renderer.Slides) != 1 {
		t.Fatal(len(e.Renderer.Slides))
	}
	out := e.testExecute(t, "Insert(root, {type:'rectangle', name:'R', width:10, height:10})\nthrow new Error('boom')")
	if !out.Error || !strings.Contains(out.Text, "rolled back") || len(e.Renderer.Slides) != 1 {
		t.Fatal("only the surviving node still slides", out.Text, len(e.Renderer.Slides))
	}
	for _, s := range e.Renderer.Slides {
		if e.Doc.Find(s.ID) == nil {
			t.Fatal("a slide without its node")
		}
	}
	mustOK(t, e.testExecute(t, "Insert(root, {type:'rectangle', name:'R', width:10, height:10})"))
	if len(e.Renderer.Slides) != 2 {
		t.Fatal("the retry slides its own node", len(e.Renderer.Slides))
	}
}

// A glass node appears in place, and so does a sliding card that gets a glass child.
func TestGlassNodesDoNotSlide(t *testing.T) {
	e := emptyEngine(t)
	mustOK(t, e.testExecute(t, `
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"rectangle", name:"Glass", width:100, height:100, effect:{type:"background_blur", radius:20}});`))
	if len(e.Renderer.Slides) != 0 {
		t.Fatal(len(e.Renderer.Slides))
	}
	e = emptyEngine(t)
	mustOK(t, e.testExecute(t, `
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            card = Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none"});
            Insert(card, {type:"rectangle", name:"Plain", width:10, height:10});`))
	if len(e.Renderer.Slides) != 1 {
		t.Fatal("a plain card slides")
	}
	mustOK(t, e.testExecute(t, `
            card = Get(n => n.name === "Card" ? n.id : undefined)[0];
            Insert(card, {type:"rectangle", name:"Glass", width:50, height:50, effect:[{type:"background_blur", radius:20}]});`))
	if len(e.Renderer.Slides) != 0 {
		t.Fatal("the card holds a glass node now")
	}
}

// A node with a blend mode (itself or in its subtree) appears in place instead of sliding.
func TestBlendedInsertsDoNotSlide(t *testing.T) {
	e := emptyEngine(t)
	mustOK(t, e.testExecute(t, `
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"rectangle", name:"plain", width:10, height:10, fill:"#ff8800"});`))
	if len(e.Renderer.Slides) != 1 {
		t.Fatal("a plain insert slides")
	}
	for name, props := range map[string]string{
		"fill":      `fill:{type:"color", color:"#ff8800", blendMode:"multiply"}`,
		"fill list": `fill:["#ffffff", {type:"color", color:"#ff8800", blendMode:"overlay"}]`,
		"stroke":    `fill:"#ffffff", stroke:{type:"color", color:"#ff8800", blendMode:"screen"}, strokeWidth:4`,
		"shadow":    `fill:"#ffffff", effect:[{type:"shadow", blur:8, color:"#ff0066a0", blendMode:"multiply"}]`,
	} {
		e := emptyEngine(t)
		mustOK(t, e.testExecute(t, `root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"}); Insert(root, {type:"rectangle", name:"b", width:10, height:10, `+props+`});`))
		e.Locked(e.Sync)
		if len(e.Renderer.Slides) != 0 {
			t.Fatal(name, ": a blended insert appears in place")
		}
	}
	e = emptyEngine(t)
	mustOK(t, e.testExecute(t, `
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"frame", name:"Card", width:100, height:100, layout:"none", children:[
                {type:"rectangle", width:10, height:10, enabled:false, fill:{type:"color", color:"#ff8800", blendMode:"multiply"}}]});`))
	e.Locked(e.Sync)
	if len(e.Renderer.Slides) != 1 {
		t.Fatal("a disabled blended child is not painted")
	}
}

// An insert under a node with opacity below 1, at any depth, appears in place.
func TestInsertUnderASemiTransparentAncestorAppearsInPlace(t *testing.T) {
	e := emptyEngine(t)
	mustOK(t, e.testExecute(t, `
            root = Insert(document, {type:"frame", name:"Root", width:300, height:300, layout:"none"});
            Insert(root, {type:"frame", name:"Glass", width:200, height:200, layout:"none", opacity:0.5});`))
	if len(e.Renderer.Slides) != 1 {
		t.Fatal("the glass frame itself slides into its opaque parent")
	}
	e.Renderer.Slides = nil
	mustOK(t, e.testExecute(t, `Insert(Get(n => n.name === "Glass" ? n.id : undefined)[0], {type:"rectangle", name:"a", width:10, height:10});`))
	if len(e.Renderer.Slides) != 0 {
		t.Fatal("a child of a semi-transparent frame does not slide")
	}
	mustOK(t, e.testExecute(t, `Insert(Get(n => n.name === "Root" ? n.id : undefined)[0], {type:"rectangle", name:"c", width:10, height:10});`))
	if len(e.Renderer.Slides) != 1 {
		t.Fatal("an insert under an opaque frame still slides")
	}
}

func TestRolledBackAgentEditsLeaveACleanDocumentClean(t *testing.T) {
	e := emptyEngine(t)
	out := e.testExecute(t, "Insert(document, {type:\"frame\", name:\"A\", width:10, height:10})\nUpdate(\"no-such-node\", {x:1})")
	if !out.Error || !strings.Contains(out.Text, "rolled back") || len(e.Doc.Children()) != 0 || e.Dirty() {
		t.Fatal("a failed execute is not an edit", out.Text)
	}
	mustOK(t, e.testExecute(t, "Insert(document, {type:\"frame\", name:\"C\", width:10, height:10})"))
	if !e.Dirty() {
		t.Fatal("a real edit dirties")
	}
	e.testExecute(t, "Update(\"no-such-node\", {x:1})")
	if !e.Dirty() {
		t.Fatal("a failed call keeps a dirty document dirty")
	}
}

func TestEngineGlobalsArePerAgent(t *testing.T) {
	e := emptyEngine(t)
	run := func(agent, src string) string {
		out := e.testExecute(t, src, "__agent", agent)
		mustOK(t, out)
		return out.Text
	}
	run("agA", `page = Insert(document, {type:"frame", name:"A page", width:400, height:300})`)
	if b := run("agB", `page = Insert(document, {type:"frame", name:"B page", x:500, width:400, height:300})`); !strings.Contains(b, "carry over to later calls: page") {
		t.Fatal(b)
	}
	run("agA", `Insert(page, {type:"rectangle", name:"A box", width:10, height:10})`)
	of := func(name string) int {
		for _, n := range e.Doc.Children() {
			if str(jsonv.Get(n, "name")) == name {
				return len(jsonv.Items(jsonv.Get(n, "children")))
			}
		}
		return -1
	}
	if of("A page") != 1 || of("B page") != 0 {
		t.Fatal("A's box lands in A's frame")
	}
	if c := run("agC", "Print(typeof page)"); !strings.Contains(c, "undefined") {
		t.Fatal("another agent starts without A's globals:", c)
	}
}

// The script runs with the engine free: polls answer, a document switch and a direct execute are refused, a gated
// edit waits for the call to end, and Stop interrupts the script and rolls it back (run with -race).
func TestARunningScriptLeavesTheEngineFreeAndStopsWithTheAgent(t *testing.T) {
	e := emptyEngine(t)
	stop := make(chan struct{})
	var asked atomic.Value
	e.Hooks.Stop = func(agent string) <-chan struct{} {
		asked.Store(agent)
		return stop
	}
	done := make(chan ToolOut)
	go func() {
		done <- e.CallTool("execute", execArgs("Insert(document, {type:'frame', name:'Half'}); for (;;) {}", "__agent", "agZ")).Finish(e)
	}()
	started := time.Now()
	for {
		var running bool
		e.Locked(func() { running = e.Executing })
		if running {
			break
		}
		if time.Since(started) > time.Minute {
			t.Fatal("the script never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The script loops until Stop, so a lock held across it would never be free here.
	free := make(chan bool, 1)
	go func() { e.Locked(func() { free <- true }) }()
	scripttest.Within(t, free, "the engine stayed locked while the script ran")
	var blocked, direct ToolOut
	e.Locked(func() {
		blocked = e.Tool("open_document", Object("path", filepath.Join(t.TempDir(), "never-opened.pen")))
		direct = e.Tool("execute", execArgs("Print(1)"))
	})
	if !blocked.Error || blocked.Text != SwitchBusy {
		t.Fatal("a document switch waits for the running script:", blocked.Text)
	}
	if !direct.Error || direct.Text != "Another execute call is still running." {
		t.Fatal(direct.Text)
	}
	if st, _ := e.dispatch(Object("cmd", "state")); !jsonv.IsTrue(jsonv.Get(st, "executing")) {
		t.Fatal("the UI state reports the running script")
	}
	// A gated edit waits at the edit gate until the call has rolled back.
	edited := make(chan error)
	go func() {
		_, err := e.dispatch(Object("cmd", "insert", "node", jsonv.MustParse(`{"type": "rectangle", "name": "User", "width": 5, "height": 5}`)))
		edited <- err
	}()
	select {
	case <-edited:
		t.Fatal("a gated edit ran while the script did")
	case <-time.After(200 * time.Millisecond):
	}
	close(stop)
	out := <-done
	if !out.Error || !strings.Contains(out.Text, "the agent was stopped, so the snippet was interrupted") || !strings.Contains(out.Text, "rolled back") {
		t.Fatal(out.Text)
	}
	if err := <-edited; err != nil {
		t.Fatal(err)
	}
	if asked.Load() != "agZ" {
		t.Fatal("Stop is asked for the call's agent:", asked.Load())
	}
	e.Locked(func() {
		if e.Executing {
			t.Fatal("still executing")
		}
		names := []string{}
		for _, n := range e.Doc.Children() {
			names = append(names, str(jsonv.Get(n, "name")))
		}
		if !slices.Equal(names, []string{"User"}) {
			t.Fatal("the stopped script's edits are rolled back and the user's edit kept:", names)
		}
	})
}

// Two executes from two goroutines never interleave: the gate runs them one after the other (run with -race).
func TestExecutesAreSerialised(t *testing.T) {
	e := emptyEngine(t)
	done := make(chan ToolOut)
	for i := range 4 {
		go func() {
			done <- e.CallTool("execute", execArgs(fmt.Sprintf("const f = Insert(document, {type:'frame', name:'F%d', width:10, height:10}); for (let k = 0; k < 2e5; k++) {} Update(f, {x: %d});", i, i))).Finish(e)
		}()
	}
	for range 4 {
		mustOK(t, <-done)
	}
	e.Locked(func() {
		if len(e.Doc.Children()) != 4 || len(e.undo) != 4 {
			t.Fatal(len(e.Doc.Children()), len(e.undo))
		}
	})
}

// decodeShot decodes a base64 PNG.
func decodeShot(t *testing.T, b64 string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatal("not a PNG", err)
	}
	return b
}

// redMiddle reports whether the middle pixel of a PNG is red.
func redMiddle(t *testing.T, b []byte) bool {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	r, g, _, _ := img.At(img.Bounds().Dx()/2, img.Bounds().Dy()/2).RGBA()
	return r>>8 > 200 && g>>8 < 80
}

// Rasterizing inside the tool call held the engine lock: only the tree is built during the call.
func TestScreenshotsRasterizeAfterTheToolCall(t *testing.T) {
	e := emptyEngine(t)
	out := e.testExecute(t, "c = Insert(document, {type:\"frame\", width:300, height:200, fill:\"#ff0000\"})\nTakeScreenshot(c)")
	mustOK(t, out)
	if len(out.Images) != 0 || len(out.pending.shots) != 1 {
		t.Fatal("only the tree is built during the call")
	}
	out = out.Finish(e)
	if len(out.Images) != 1 || !redMiddle(t, decodeShot(t, out.Images[0])) {
		t.Fatal(out.Text)
	}
}

const redPNG = "iVBORw0KGgoAAAANSUhEUgAAAAUAAAAFCAYAAACNbyblAAAAHElEQVQI12P4//8/w38GIAXDIBKE0DHxgljNBAAO9TXL0Y4OHwAAAABJRU5ErkJggg=="

// slowRedPNG is the url of a red PNG on a local server that answers each request after 300 ms.
func slowRedPNG(t *testing.T, name string) string {
	t.Helper()
	red, _ := base64.StdEncoding.DecodeString(redPNG)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write(red)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/" + name + ".png"
}

// A screenshot of an image still downloading waits for it (without the engine lock) and shows it; a download it
// does not show is not waited for.
func TestScreenshotsWaitForImagesStillDownloading(t *testing.T) {
	e := emptyEngine(t)
	url := slowRedPNG(t, "shot")
	hang := make(chan struct{})
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-hang:
		}
	}))
	defer silent.Close()
	defer close(hang)
	other := silent.URL + "/elsewhere.png"
	e.Assets.Online = true
	var out ToolOut
	e.Locked(func() {
		if _, ok := e.Assets.Image(other, ""); ok || !e.Assets.Loading(other) {
			t.Fatal("the other download runs")
		}
		out = e.Tool("execute", execArgs(fmt.Sprintf("c = Insert(document, {type:\"frame\", width:100, height:100, layout:\"none\", fill:{type:\"image\", url:%q, mode:\"fill\"}})\nTakeScreenshot(c)", url)))
	})
	mustOK(t, out)
	if !slices.Contains(out.pending.downloads, url) || out.pending.callDoc == nil {
		t.Fatal("the shot is redrawn once the image is in", out.pending.downloads)
	}
	if slices.Contains(out.pending.downloads, other) {
		t.Fatal("only what the screenshot shows is waited for")
	}
	out = out.Finish(e)
	e.Locked(func() {
		if e.Assets.Loading(url) {
			t.Fatal("still loading")
		}
	})
	if !redMiddle(t, decodeShot(t, out.Images[0])) {
		t.Fatal("the image is in the screenshot")
	}
}

// firstFile is the one file in dir.
func firstFile(t *testing.T, dir string) []byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatal("no file in", dir, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An export of an image still downloading waits for it and includes it; planned again, it is still a script's
// export, so a file that appeared meanwhile is kept.
func TestScriptExportsWaitForImagesStillDownloading(t *testing.T) {
	e := emptyEngine(t)
	e.Assets.Online = true
	dir := filepath.Join(e.DataDir, "exports", "script-export-waits")
	url := slowRedPNG(t, "export")
	out := e.testExecute(t, fmt.Sprintf("c = Insert(document, {type:\"frame\", width:100, height:100, layout:\"none\", fill:{type:\"image\", url:%q, mode:\"fill\"}})\nExport(c, \"png\", %q, {scale: 1})", url, dir))
	mustOK(t, out)
	out = out.Finish(e)
	if !strings.Contains(out.Text, "Exported:") || !redMiddle(t, firstFile(t, dir)) {
		t.Fatal("the image is in the export", out.Text)
	}
	late := filepath.Join(dir, "late.pdf")
	out = e.testExecute(t, fmt.Sprintf("Export(Insert(document, {type:\"frame\", width:100, height:100, fill:{type:\"image\", url:%q, mode:\"fill\"}}), \"pdf\", %q)", slowRedPNG(t, "late"), late))
	mustOK(t, out)
	if len(out.pending.exports[0].Waits) == 0 {
		t.Fatal("the export waits")
	}
	os.WriteFile(late, []byte("the user's own file"), 0o666)
	out = out.Finish(e)
	if !strings.Contains(out.Text, "already exists") || strings.Contains(out.Text, "Exported:") {
		t.Fatal(out.Text)
	}
	if b, _ := os.ReadFile(late); string(b) != "the user's own file" {
		t.Fatal("the user's file is kept")
	}
}

// Rasterizing and encoding inside the tool call held the engine lock for the whole export.
func TestScriptExportsWriteAfterTheToolCall(t *testing.T) {
	e := emptyEngine(t)
	dir := filepath.Join(e.DataDir, "exports", "script-export-after-call")
	out := e.testExecute(t, fmt.Sprintf("c = Insert(document, {type:\"frame\", width:300, height:200, fill:\"#ff0000\"})\nExport(c, \"png\", %q, {scale: 1})", dir))
	mustOK(t, out)
	if _, err := os.Stat(dir); len(out.pending.exports) != 1 || err == nil {
		t.Fatal("only the tree is built during the call")
	}
	out = out.Finish(e)
	if !strings.Contains(out.Text, "Exported:") || !bytes.HasPrefix(firstFile(t, dir), []byte("\x89PNG")) {
		t.Fatal(out.Text)
	}
}

func TestScriptExportNeverReplacesAFileItDidNotWrite(t *testing.T) {
	e := emptyEngine(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "shots"), 0o777)
	e.Doc.Path = filepath.Join(dir, "design.nala")
	mustOK(t, e.testExecute(t, "Insert(document, {type:\"frame\", width:100, height:80, fill:\"#ff0000\"})"))
	id := str(at(e.Doc.Root, "children", 0, "id"))
	for _, c := range [][3]string{{"report.pdf", "pdf", "report.pdf"}, {"page.html", "html-css", "page.html"}, {"shots/" + id + ".png", "png", "shots"}} {
		os.WriteFile(filepath.Join(dir, c[0]), []byte("the user's own file"), 0o666)
		res := e.testExecute(t, fmt.Sprintf("Export([%q], %q, %q)", id, c[1], c[2])).Finish(e)
		if !strings.Contains(res.Text, "Export failed") || !strings.Contains(res.Text, "already exists") || strings.Contains(res.Text, "Exported:") {
			t.Fatal(c[0], res.Text)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, c[0])); string(b) != "the user's own file" {
			t.Fatal(c[0], "is kept")
		}
	}
	out := e.testExecute(t, fmt.Sprintf("Export([%q], \"pdf\", \"late.pdf\")", id))
	os.WriteFile(filepath.Join(dir, "late.pdf"), []byte("the user's own file"), 0o666)
	if res := out.Finish(e); !strings.Contains(res.Text, "already exists") || strings.Contains(res.Text, "Exported:") {
		t.Fatal(res.Text)
	}
	for range 2 {
		if res := e.testExecute(t, fmt.Sprintf("Export([%q], \"pdf\", \"deck.pdf\")", id)).Finish(e); !strings.Contains(res.Text, "Exported:") {
			t.Fatal("a file the script exported itself is written again:", res.Text)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "deck.pdf")); !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
	// Outside the document's folder is refused (agent.rs `path_allowed`), and so is the open document.
	outside := filepath.Join(t.TempDir(), "x.pdf")
	res := e.testExecute(t, fmt.Sprintf("Export([%q], \"pdf\", %q)", id, outside)).Finish(e)
	if want := "Export failed: Export writes only inside " + dir + " and never over the open document"; !strings.Contains(res.Text, want) {
		t.Fatal(res.Text)
	}
	e.Doc.Path = filepath.Join(dir, "design.pdf")
	if res := e.testExecute(t, fmt.Sprintf("Export([%q], \"pdf\", \"design.pdf\")", id)).Finish(e); !strings.Contains(res.Text, "Export writes only inside") {
		t.Fatal(res.Text)
	}
	// A hook replaces the rule.
	e.Hooks.ExportAllowed = func(file, base, docPath string) bool { return true }
	if res := e.testExecute(t, fmt.Sprintf("Export([%q], \"pdf\", %q)", id, outside)).Finish(e); !strings.Contains(res.Text, "Exported: "+outside) {
		t.Fatal(res.Text)
	}
}

// openSlowScript opens a document whose page holds a script node running src.
func openSlowScript(t *testing.T, e *Engine, src string) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "slow.js"), []byte(src), 0o666)
	os.WriteFile(filepath.Join(dir, "doc.pen"), []byte(`{"version": "2.19", "children": [
            {"type": "frame", "id": "page", "x": 0, "y": 0, "width": 100, "height": 100, "layout": "none", "fill": "#ffffff", "children": [
                {"type": "script", "id": "s", "x": 0, "y": 0, "width": 100, "height": 100, "scriptUri": "./slow.js"}]}]}`), 0o666)
	e.Locked(func() {
		if err := e.Open(filepath.Join(dir, "doc.pen")); err != nil {
			t.Fatal(err)
		}
	})
	return dir
}

// The agent's screenshots and exports of a script node run it after the tool call, off the engine lock: the call
// answers while the run is held, and the engine lock is free while Finish waits for it.
func TestAgentShotsAndExportsRunScriptNodesOffTheLock(t *testing.T) {
	e := newEngine(t)
	marker := fmt.Sprintf("held-%d", time.Now().UnixNano())
	started, release := scripttest.HoldRuns(t, script.SetRunHook, marker)
	dir := openSlowScript(t, e, fmt.Sprintf("/** @schema 2.19 */\n// %s\nreturn [{type: 'rectangle', width: nalarupa.width, height: nalarupa.height, fill: '#ff0101'}];", marker))
	called := make(chan ToolOut, 1)
	go func() {
		called <- e.testExecute(t, fmt.Sprintf("TakeScreenshot(\"page\")\nExport(\"page\", \"png\", %q, {scale: 1})", filepath.Join(dir, "agent")))
	}()
	out := scripttest.Within(t, called, "the tool call waited for the script")
	mustOK(t, out)
	finished := make(chan ToolOut, 1)
	go func() { finished <- out.Finish(e) }()
	scripttest.Within(t, started, "Finish runs the script")
	// The run is held inside Finish: the engine answers meanwhile.
	locked := make(chan bool, 1)
	go func() { e.Locked(func() { locked <- true }) }()
	scripttest.Within(t, locked, "the engine lock is held while the script runs")
	release()
	out = scripttest.Within(t, finished, "Finish ends once the run is released")
	if !strings.Contains(out.Text, "Exported:") || !redMiddle(t, decodeShot(t, out.Images[0])) || !redMiddle(t, firstFile(t, filepath.Join(dir, "agent"))) {
		t.Fatal("the script's output is in the screenshot and the export:", out.Text)
	}
}

// The icons a call used that are still unknown get their warning once Finish has waited for them: after the
// issues already listed, or as a new list.
func TestSettleIconsAddsMissingIconsToTheIssues(t *testing.T) {
	e := emptyEngine(t)
	icons := []script.Icon{{Warning: "icon \"A\" missing", Library: "lucide", Name: "no-such-icon", Weight: 200}}
	until := time.Now().Add(time.Second)
	head := "## Issues detected (fix them in the next call)\n"
	if got := e.settleIcons("OK\n\n"+head+"- one\n- two\n\n## More\n", icons, until); got != "OK\n\n"+head+"- one\n- two\n- icon \"A\" missing\n\n## More\n" {
		t.Fatalf("%q", got)
	}
	if got := e.settleIcons("OK\n", icons, until); got != "OK\n\n"+head+"- icon \"A\" missing\n" {
		t.Fatalf("%q", got)
	}
	if got := e.settleIcons("OK\n"+head+"- one", icons, until); got != "OK\n"+head+"- one- icon \"A\" missing\n" {
		t.Fatalf("a last bullet without a line end: %q", got)
	}
}

// generateServer serves placeholder photos (or fails with status) and counts the requests.
func generateServer(t *testing.T, status int, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.URL.Path, "/seed/") || !strings.HasSuffix(r.URL.Path, "/1600/1000") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte("jpeg bytes"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// waitResolved waits until no generated image is still downloading.
func waitResolved(t *testing.T, e *Engine) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for {
		var n int
		e.Locked(func() { n = len(e.Inflight) })
		if n == 0 {
			return
		}
		if time.Now().After(until) {
			t.Fatal("the download never resolved")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Generate("ai") points the fill at a pending url, downloads a photo seeded by the prompt and swaps it in, in the
// document and in the history; a saved document links it relatively, an unsaved one by its path in the data
// folder; a photo already on disk is not fetched again; a failed download toasts and leaves an empty url.
func TestGenerateDownloadsAndResolves(t *testing.T) {
	var hits atomic.Int32
	e := emptyEngine(t)
	e.ImageBase = generateServer(t, 200, &hits)
	dir := t.TempDir()
	e.Locked(func() {
		e.Doc.Path = filepath.Join(dir, "doc.pen")
		e.Edit(setChildren(`[{"id": "r", "type": "rectangle", "width": 20, "height": 20, "fill": "#f00"}]`))
	})
	out := e.testExecute(t, `Generate("ai", "r", "a cat");`, "__seed", jsonv.Int(3))
	mustOK(t, out)
	if !strings.Contains(out.Text, `Note: Generate("ai") on r: the image is loading in the background`) {
		t.Fatal(out.Text)
	}
	pending := ""
	e.Locked(func() {
		pending = str(at(e.Doc.Root, "children", 0, "fill", "url"))
		if !strings.HasPrefix(pending, "nalarupa:pending-image-") {
			t.Fatal(string(jsonv.Marshal(e.Doc.Root)))
		}
		// Undone before the download lands: the history gets the image too.
		e.Undo()
	})
	waitResolved(t, e)
	seed := fmt.Sprintf("%x", svg.FxHash("a cat"))
	e.Locked(func() {
		e.Redo()
		if got := str(at(e.Doc.Root, "children", 0, "fill", "url")); got != "./images/"+seed+".jpg" {
			t.Fatal(got)
		}
	})
	if b, _ := os.ReadFile(filepath.Join(dir, "images", seed+".jpg")); string(b) != "jpeg bytes" {
		t.Fatal("the photo is in the document's images folder")
	}
	// Unsaved: the data folder, by path; a photo already there is reused.
	e.Locked(func() { e.Doc.Path = "" })
	mustOK(t, e.testExecute(t, `Generate("stock", "r", "a cat");`))
	waitResolved(t, e)
	want := strings.ReplaceAll(filepath.Join(e.DataDir, "images", seed+".jpg"), `\`, "/")
	e.Locked(func() {
		if got := str(at(e.Doc.Root, "children", 0, "fill", "url")); got != want {
			t.Fatal(got, want)
		}
	})
	mustOK(t, e.testExecute(t, `Generate("ai", "r", "a cat");`))
	waitResolved(t, e)
	if hits.Load() != 2 {
		t.Fatal("a photo already on disk is not fetched again:", hits.Load())
	}
	// A bad target starts no download; a preview step none either (see script's tests).
	before := hits.Load()
	if out := e.testExecute(t, `Generate("ai", "nope", "a dog");`); !out.Error {
		t.Fatal(out.Text)
	}
	e.Locked(func() {
		if len(e.Inflight) != 0 {
			t.Fatal("a bad target starts no download")
		}
	})
	if hits.Load() != before {
		t.Fatal("fetched for a bad target")
	}

	// A failed download: an empty url and a toast.
	var fails atomic.Int32
	e.ImageBase = generateServer(t, 500, &fails)
	e.Locked(func() { e.TakeEvents() })
	mustOK(t, e.testExecute(t, `Generate("ai", "r", "a failing dog");`))
	waitResolved(t, e)
	e.Locked(func() {
		if got := at(e.Doc.Root, "children", 0, "fill", "url"); got != "" {
			t.Fatal(got)
		}
		toast := false
		for _, ev := range e.TakeEvents() {
			toast = toast || str(jsonv.Get(ev, "text")) == "Failed to load a generated image"
		}
		if !toast {
			t.Fatal("no toast")
		}
	})
}

// endExecute reports the writes and reads of the call's `agent_edit` event as `agent_activity`.
func TestExecuteEvents(t *testing.T) {
	e := emptyEngine(t)
	mustOK(t, e.testExecute(t, `const f = Insert(document, {type:"frame", width:10, height:10}); Get(f); Update(f, {x: 1});`, "__agent", "agQ"))
	var evs []jsonv.Value
	e.Locked(func() { evs = e.TakeEvents() })
	var kinds []string
	for _, ev := range evs {
		kinds = append(kinds, str(jsonv.Get(ev, "type"))+"/"+str(jsonv.Get(ev, "op"))+"/"+str(jsonv.Get(ev, "agent")))
	}
	if !slices.Equal(kinds, []string{"agent_edit//", "agent_activity/write/agQ", "agent_activity/read/agQ"}) {
		t.Fatal(kinds)
	}
	// A failed call emits nothing.
	e.testExecute(t, "throw 1")
	e.Locked(func() {
		if evs := e.TakeEvents(); len(evs) != 0 {
			t.Fatal(evs)
		}
	})
}

// Hooks: NoteFlagged sees the document before the call, StartPen gets each Generate("svg") frame with its size.
func TestAgentHooks(t *testing.T) {
	e := emptyEngine(t)
	var flagged, pens []string
	e.Agent.Stops = 4
	e.Hooks.NoteFlagged = func(agent string, before *jsonv.Object) {
		flagged = append(flagged, fmt.Sprintf("%s:%d", agent, len(jsonv.Items(jsonv.Get(before, "children")))))
	}
	e.Hooks.StartPen = func(agent string, stops uint64, frame, prompt string, w, h float64) {
		pens = append(pens, fmt.Sprintf("%s:%d:%s:%g:%g", agent, stops, prompt, w, h))
	}
	mustOK(t, e.testExecute(t, `f = Insert(document, {type:"frame", width:120, height:80, placeholder:true}); Generate("svg", f, "a tree");`, "__agent", "agP"))
	if !slices.Equal(flagged, []string{"agP:0"}) || !slices.Equal(pens, []string{"agP:4:a tree:120:80"}) {
		t.Fatal(flagged, pens)
	}
}

// A panic while the report validates (here: no fonts to lay text out with) leaves the call no longer running.
func TestAPanicInTheReportClearsExecuting(t *testing.T) {
	e := emptyEngine(t)
	fonts := e.Fonts
	e.Fonts = nil
	func() {
		defer func() { recover() }()
		e.testExecute(t, `Insert(document, {type: "text", content: "hi", fill: "#000000"})`)
		t.Fatal("no panic")
	}()
	e.Fonts = fonts
	if e.Executing {
		t.Fatal("still executing")
	}
	mustOK(t, e.testExecute(t, "Print(1)"))
}

// The png command draws library components.
func TestPNGResolvesLibraries(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.nala"), []byte(`{"version":"2.19","children":[{"id":"C","type":"frame","reusable":true,"width":40,"height":30,"layout":"none","fill":"#ff0000"}]}`), 0o666)
	os.WriteFile(filepath.Join(dir, "doc.nala"), []byte(`{"version":"2.19","imports":{"lib":"lib.nala"},"children":[{"id":"i","type":"ref","ref":"lib:C","x":0,"y":0}]}`), 0o666)
	e := newEngine(t)
	if err := e.Open(filepath.Join(dir, "doc.nala")); err != nil {
		t.Fatal(err)
	}
	v, err := e.png(Object("id", "i"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := base64.StdEncoding.DecodeString(v.(string))
	if !redMiddle(t, b) {
		t.Fatal("the library component is drawn")
	}
}
