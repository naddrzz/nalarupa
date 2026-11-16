package script_test

// Script nodes through the renderer and the exporter: the script.rs tests that open a document.

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/script/scripttest"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// open writes files and doc.pen into a fresh folder and opens the document as the engine does.
func open(t *testing.T, docJSON string, files map[string]string) *doc.Doc {
	t.Helper()
	dir := t.TempDir()
	files["doc.pen"] = docJSON
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	d, err := doc.Load(filepath.Join(dir, "doc.pen"))
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	return d
}

func offline(t *testing.T) (*text.Fonts, *svg.Assets) {
	t.Helper()
	fonts := text.New(filepath.Join(t.TempDir(), "fonts"))
	fonts.Online = false
	assets := svg.NewAssets(filepath.Join(t.TempDir(), "cache"))
	assets.Online = false
	return fonts, assets
}

func waitIdle(t *testing.T) {
	t.Helper()
	limit := time.Now().Add(10 * time.Second)
	for script.Busy() {
		if time.Now().After(limit) {
			t.Fatal("background runs never finished")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestScriptNodeRendersAndHitsAsOne(t *testing.T) {
	d := open(t, `{"version": "2.19", "children": [
		{"type": "frame", "id": "comp", "x": 0, "y": 500, "width": 10, "height": 10, "reusable": true, "fill": "#0000ff"},
		{"type": "frame", "id": "page", "x": 0, "y": 0, "width": 300, "height": 200, "layout": "vertical", "children": [
			{"type": "script", "id": "s", "width": "fill_container", "height": 100, "clip": true, "scriptUri": "./dots.js", "inputs": {"color": "#ff0000"}},
			{"type": "script", "id": "bad", "width": 120, "height": 80, "scriptUri": "./nope.js"}
		]}
	]}`, map[string]string{"dots.js": "/** @schema 2.19\n * @input color: color = #00ff00 */\nreturn [{type: 'ellipse', name: 'dot', x: nalarupa.width - 20, y: 0, width: 20, height: 20, fill: pencil.input.color}, {type: 'ref', ref: 'comp', x: 0, y: 30}];"})
	fonts, assets := offline(t)
	ex := &export.Ctx{Doc: d, Fonts: fonts, Assets: assets}
	markup, _, _, ok := svg.NodeSVGAt(d, nil, "page", 1, false, fonts, assets, func(n *resolve.RNode, r *resolve.Resolver) {
		script.ExpandScripts(n, r, fonts, d.Dir(), d.Path, false, nil)
	})
	if !ok {
		t.Fatal("page not found")
	}
	// The script ran with its laid-out width (fill_container = 300) and the input value.
	if !strings.Contains(markup, "#ff0000") || !strings.Contains(markup, "clip-path") {
		t.Fatal(markup)
	}
	if !strings.Contains(markup, `fill-opacity="0.102"`) {
		t.Fatal("error status is drawn")
	}
	if _, err := ex.NodePNG("page", 1); err != nil {
		t.Fatal(err)
	}
	cx := &render.Ctx{Doc: d, Fonts: fonts, Assets: assets}
	r := &render.Renderer{}
	r.Sync(cx)
	dot := r.Find("s/0")
	if dot == nil || dot.X != 280 || dot.W != 20 {
		t.Fatalf("generated child is laid out: %+v", dot)
	}
	if f := r.Find("s/1"); f == nil || f.Y != 30 {
		t.Fatalf("ref output resolves the component: %+v", f)
	}
	if chain := render.HitChain(r, 290, 10); len(chain) == 0 || chain[len(chain)-1] != "s" {
		t.Fatalf("a generated child selects its script node: %v", chain)
	}
	if jsonv.Obj(d.Find("s")).Has("children") {
		t.Fatal("output is never saved")
	}
}

func TestLiveScriptRunsOffTheLock(t *testing.T) {
	waitIdle(t)
	marker := fmt.Sprintf("held-%d", time.Now().UnixNano())
	started, release := scripttest.HoldRuns(t, script.SetRunHook, marker)
	d := open(t, `{"version": "2.19", "children": [
		{"type": "frame", "id": "page", "x": 0, "y": 0, "width": 300, "height": 200, "layout": "none", "children": [
			{"type": "script", "id": "s", "x": 0, "y": 0, "width": 100, "height": 100, "scriptUri": "./slow.js"}]}
	]}`, map[string]string{"slow.js": "/** @schema 2.19 */\n// " + marker + "\nreturn [{type: 'rectangle', width: nalarupa.width, height: 10}];"})
	fonts, assets := offline(t)
	cx := &render.Ctx{Doc: d, Fonts: fonts, Assets: assets}
	r := &render.Renderer{}
	// The run is held: a sync that waited for it would never return.
	scripttest.Returns(t, "sync", func() { r.Sync(cx) })
	scripttest.Started(t, started, "the run is in the background")
	if r.Find("s/0") != nil {
		t.Fatal("no output yet")
	}
	if !script.Busy() {
		t.Fatal("the run is in the background")
	}
	// A resize meanwhile only queues the newest size and does not wait either.
	scripttest.Returns(t, "resizing", func() {
		for w := 101; w < 106; w++ {
			if err := d.Update("s", jsonv.MustParse(`{"width": `+string(jsonv.Int(int64(w)))+`}`)); err != nil {
				panic(err)
			}
			r.Sync(cx)
		}
	})
	if r.Find("s/0") != nil {
		t.Fatal("still no output while the run is held")
	}
	release()
	limit := time.Now().Add(time.Minute)
	for f := r.Find("s/0"); f == nil || f.W != 105; f = r.Find("s/0") {
		if time.Now().After(limit) {
			t.Fatal("the runs finish and the layer is rebuilt")
		}
		time.Sleep(20 * time.Millisecond)
		r.Sync(cx)
	}
	waitIdle(t)
}

// Exporting a script node in record mode never waits for its JavaScript, and OffLock still gets its output
// into the file (ui_exports_run_script_nodes_off_the_lock).
func TestExportsRunScriptNodesOffTheLock(t *testing.T) {
	marker := fmt.Sprintf("held-%d", time.Now().UnixNano())
	started, release := scripttest.HoldRuns(t, script.SetRunHook, marker)
	d := open(t, `{"version": "2.19", "children": [
		{"type": "frame", "id": "page", "x": 0, "y": 0, "width": 100, "height": 100, "layout": "none", "fill": "#ffffff", "children": [
			{"type": "script", "id": "s", "x": 0, "y": 0, "width": 100, "height": 100, "scriptUri": "./slow.js"}]}
	]}`, map[string]string{"slow.js": "/** @schema 2.19 */\n// " + marker + "\nreturn [{type: 'rectangle', width: nalarupa.width, height: nalarupa.height, fill: '#ff0000'}];"})
	fonts, assets := offline(t)
	draws := 0
	exported := make(chan []byte, 1)
	go func() {
		exported <- script.OffLock(func(rec *script.Runs) []byte {
			draws++
			ex := &export.Ctx{Doc: d, Fonts: fonts, Assets: assets, Runs: rec}
			b, err := ex.NodePNG("page", 1)
			if err != nil {
				panic(err)
			}
			return b
		})
	}()
	// The held run starts only after the first draw planned it in record mode: planning did not wait for it.
	scripttest.Started(t, started, "OffLock runs the noted script")
	if draws != 1 {
		t.Fatalf("planning in record mode waited for the script: %d draws", draws)
	}
	release()
	var file []byte
	select {
	case file = <-exported:
	case <-time.After(time.Minute):
		t.Fatal("OffLock ends once the run is released")
	}
	if draws != 2 {
		t.Fatalf("drawn again with the output: %d draws", draws)
	}
	img, err := png.Decode(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	if r, g, b, _ := img.At(50, 50).RGBA(); r>>8 != 255 || g>>8 != 0 || b>>8 != 0 {
		t.Fatalf("the script's output is in the export: %d %d %d", r>>8, g>>8, b>>8)
	}
	// The same layout outside record mode waits for the run and reads it from the cache now.
	cx := &render.Ctx{Doc: d, Fonts: fonts, Assets: assets}
	root := cx.LaidOut(0)
	if !slices.ContainsFunc(root.Kids[0].Kids, func(k *resolve.RNode) bool { return k.ID == "s/0" }) {
		t.Fatal("cached output is laid out")
	}
}
