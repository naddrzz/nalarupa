// Script-node golden tests: every document in testdata/script is opened, laid out with its script
// nodes run (waiting for each, as an export does) and compared with the committed goldens
// (testdata/script/golden). The text-free documents are also exported (PNG, scale 1) and the decoded
// pixels compared exactly.
package script_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/script"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

var (
	testdata  = filepath.Join("..", "testdata", "script")
	goldenDir = filepath.Join(testdata, "golden")
	// images are the top-level nodes exported per document: those without text (a status box draws text).
	images = map[string][]string{"runs": {"bars", "flow", "old"}}
)

// status is a node's `__status`.
type status struct {
	Kind        string  `json:"kind"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Code        *string `json:"code"`
}

// box is one node of the CLI's laid-out tree.
type box struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	Problem  *string `json:"problem"`
	Status   *status `json:"status"`
	Children []box   `json:"children"`
}

func dump(n *resolve.RNode) box {
	b := box{ID: n.ID, Type: n.Type, X: n.X, Y: n.Y, W: n.W, H: n.H, Children: []box{}}
	if n.Problem != "" {
		b.Problem = &n.Problem
	}
	if v, ok := n.P.Get("__status"); ok {
		var s status
		_ = json.Unmarshal(jsonv.Marshal(v), &s)
		b.Status = &s
	}
	for _, k := range n.Kids {
		b.Children = append(b.Children, dump(k))
	}
	return b
}

// opened is the document as `nalarupa-cli layout` opens it: migrated, ids repaired.
func opened(t *testing.T, path string) *doc.Doc {
	t.Helper()
	d, err := doc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	return d
}

// laidOut is `nalarupa-cli layout <doc> --scripts`.
func laidOut(t *testing.T, path string, fonts *text.Fonts) []box {
	t.Helper()
	d := opened(t, path)
	r := resolve.New(d.Root, nil)
	trees := []box{}
	for _, n := range d.Children() {
		if rn := r.Resolve(n); rn != nil {
			layout.Layout(rn, fonts)
			script.ExpandScripts(rn, r, fonts, d.Dir(), d.Path, false, nil)
			trees = append(trees, dump(rn))
		}
	}
	return trees
}

// differs masks what depends on the C compiler rather than the engine: quickjs-ng's C stack frames are smaller
// built by gcc (cgo) than by MSVC (Rust's Windows build), so the same 512 KB stack holds more JS frames (325
// against 146 for the depth probe's one-line recursion, measured on Windows). Only that count is masked.
var differs = map[string]*regexp.Regexp{"probes/depths/depth": regexp.MustCompile(`^Error: \d+ `)}

// statusText is the status of the node at path at, as compared.
func statusText(at string, s *status) string {
	if s == nil {
		return "-"
	}
	desc, code := "<nil>", "<nil>"
	if s.Description != nil {
		desc = *s.Description
		if re := differs[at]; re != nil {
			desc = re.ReplaceAllLiteralString(desc, "Error: (frames) ")
		}
	}
	if s.Code != nil {
		code = *s.Code
	}
	return s.Kind + " | " + s.Title + " | " + desc + " | " + code
}

func sameBoxes(t *testing.T, got, want []box, at string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d nodes, want %d", at, len(got), len(want))
		return
	}
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }
	for i := range got {
		g, w := got[i], want[i]
		id := at + "/" + w.ID
		if g.ID != w.ID || g.Type != w.Type || (g.Problem == nil) != (w.Problem == nil) || g.Problem != nil && *g.Problem != *w.Problem {
			t.Errorf("%s: got %s %s, want %s %s", id, g.ID, g.Type, w.ID, w.Type)
			continue
		}
		if !near(g.X, w.X) || !near(g.Y, w.Y) || !near(g.W, w.W) || !near(g.H, w.H) {
			t.Errorf("%s: got (%v, %v, %v, %v), want (%v, %v, %v, %v)", id, g.X, g.Y, g.W, g.H, w.X, w.Y, w.W, w.H)
		}
		if gs, ws := statusText(id, g.Status), statusText(id, w.Status); gs != ws {
			t.Errorf("%s status:\n got %q\nwant %q", id, gs, ws)
		}
		sameBoxes(t, g.Children, w.Children, id)
	}
}

func decode(t *testing.T, b []byte) *image.NRGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out := image.NewNRGBA(img.Bounds())
	draw.Draw(out, out.Rect, img, img.Bounds().Min, draw.Src)
	return out
}

func samePixels(t *testing.T, got, want []byte, at string) {
	t.Helper()
	g, w := decode(t, got), decode(t, want)
	if g.Rect != w.Rect {
		t.Errorf("%s: size %v, want %v", at, g.Rect.Size(), w.Rect.Size())
		return
	}
	differing, first := 0, -1
	for i := 0; i < len(w.Pix); i += 4 {
		if !bytes.Equal(g.Pix[i:i+4], w.Pix[i:i+4]) {
			differing++
			if first < 0 {
				first = i / 4
			}
		}
	}
	if differing > 0 {
		x, y := first%w.Rect.Dx(), first/w.Rect.Dx()
		t.Errorf("%s: %d pixels differ, first at (%d, %d): got %v, want %v", at, differing, x, y, g.Pix[first*4:first*4+4], w.Pix[first*4:first*4+4])
	}
}

func TestParity(t *testing.T) {
	docs, err := filepath.Glob(filepath.Join(testdata, "*.pen"))
	if err != nil || len(docs) == 0 {
		t.Fatal("no script documents", err)
	}
	fonts := text.New(filepath.Join(t.TempDir(), "fonts"))
	fonts.Online = false
	for _, path := range docs {
		stem := strings.TrimSuffix(filepath.Base(path), ".pen")
		t.Run(stem, func(t *testing.T) {
			golden := filepath.Join(goldenDir, stem+".scripts.json")
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			var wantBoxes []box
			if err := json.Unmarshal(want, &wantBoxes); err != nil {
				t.Fatal(err)
			}
			sameBoxes(t, laidOut(t, path, fonts), wantBoxes, stem)

			for _, id := range images[stem] {
				gold := filepath.Join(goldenDir, stem, id+".png")
				wantPNG, err := os.ReadFile(gold)
				if err != nil {
					t.Fatal(err)
				}
				assets := svg.NewAssets(filepath.Join(t.TempDir(), "cache"))
				assets.Online = false
				ex := &export.Ctx{Doc: opened(t, path), Fonts: fonts, Assets: assets}
				got, err := ex.NodePNG(id, 1)
				if err != nil {
					t.Fatal(err)
				}
				samePixels(t, got, wantPNG, stem+"/"+id)
			}
		})
	}
}
