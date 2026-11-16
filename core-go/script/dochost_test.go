package script_test

// docHost is the execute Host of the tests: a document with the canvas renderer, offline fonts and assets, and
// what an engine would do with Generate and slides recorded instead. agent keeps what the engine keeps between
// calls: each agent's globals and the snippets of failed calls.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/render"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

type docHost struct {
	doc      *doc.Doc
	fonts    *text.Fonts
	assets   *svg.Assets
	ids      *doc.IDSource
	renderer render.Renderer
	version  int
	slid     []string
	// generated holds each Generate call's kind, target, prompt and pending url.
	generated [][4]string
}

var _ script.Host = (*docHost)(nil)

func (h *docHost) Doc() *doc.Doc       { return h.doc }
func (h *docHost) Changed()            { h.version++ }
func (h *docHost) SlideIn(id string)   { h.slid = append(h.slid, id) }
func (h *docHost) IDs() *doc.IDSource  { return h.ids }
func (h *docHost) Fonts() *text.Fonts  { return h.fonts }
func (h *docHost) Assets() *svg.Assets { return h.assets }

func (h *docHost) ctx() *render.Ctx {
	return &render.Ctx{Doc: h.doc, Fonts: h.fonts, Assets: h.assets}
}

func (h *docHost) LaidOut(index int) *resolve.RNode { return h.ctx().LaidOut(index) }

func (h *docHost) FindEmptySpace(w, hh float64, dir string, pad float64, anchor string) (float64, float64) {
	h.renderer.Sync(h.ctx())
	return render.FindEmptySpace(&h.renderer, w, hh, dir, pad, h.renderer.Find(anchor))
}

func (h *docHost) Generate(kind, target, prompt, pending string) error {
	h.generated = append(h.generated, [4]string{kind, target, prompt, pending})
	return nil
}

// execOffline is the fonts (system fonts and an empty cache) and assets of every docHost, as `nalarupa-cli exec
// --offline` has them with a fresh NALARUPA_DATA. text.Fonts is not safe for concurrent use: a test that runs
// hosts in parallel gives each its own (newOffline).
var execOffline = sync.OnceValues(newOffline)

func newOffline() (*text.Fonts, *svg.Assets) {
	dir, err := os.MkdirTemp("", "nalarupa-exec-")
	if err != nil {
		panic(err)
	}
	fonts := text.New(filepath.Join(dir, "fonts"))
	fonts.Online = false
	assets := svg.NewAssets(filepath.Join(dir, "cache"))
	assets.Online = false
	return fonts, assets
}

// newHost is a host over d with the shared offline fonts and assets and ids seeded from the clock.
func newHost(d *doc.Doc) *docHost {
	fonts, assets := execOffline()
	h := &docHost{doc: d, fonts: fonts, assets: assets, ids: doc.NewIDSource(uint64(time.Now().UnixNano()))}
	d.IDs = h.ids
	return h
}

// emptyHost is a host over the `empty` template.
func emptyHost(t testing.TB) *docHost {
	t.Helper()
	d, err := doc.FromJSON([]byte(`{"version":"2.19","children":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	return newHost(d)
}

// openHost opens path as `Engine::open` does (migrate, repair ids; the fixtures have no imports, pending images or
// placeholders).
func openHost(t testing.TB, path string) *docHost {
	t.Helper()
	d, err := doc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	return newHost(d)
}

// agents is what the engine keeps between execute calls: each agent's globals and failed snippets by editId.
type agents struct {
	globals map[string]*jsonv.Object
	failed  map[string]string
}

func newAgents() *agents {
	return &agents{globals: map[string]*jsonv.Object{}, failed: map[string]string{}}
}

// call runs one execute call as the engine does around script.Execute: agent's globals in, the new ones merged
// in on success, a failed snippet stored under its editId and a repaired one forgotten.
func (a *agents) call(h *docHost, agent string, args jsonv.Value, budget time.Duration) *script.Outcome {
	g := a.globals[agent]
	out := script.Execute(h, args, script.Call{
		Globals: g,
		Failed:  func(id string) (string, bool) { s, ok := a.failed[id]; return s, ok },
		Budget:  budget,
	})
	switch {
	case out.Error && out.EditID != "":
		a.failed[out.EditID] = out.Source
	case !out.Error:
		delete(a.failed, out.Repaired)
		if g == nil {
			g = jsonv.NewObject()
			a.globals[agent] = g
		}
		out.Globals.Each(func(k string, v jsonv.Value) { g.Set(k, v) })
	}
	return out
}

// input is the tool arguments {input: src}.
func input(src string) jsonv.Value {
	o := jsonv.NewObject()
	o.Set("input", src)
	return o
}
