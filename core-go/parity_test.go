// Golden tests: every fixture in testdata is opened (migrate, repair ids), saved and laid out,
// and compared with the committed goldens (*.layout.json, *.saved.json).
package core_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// box is one node of the CLI's laid-out tree.
type box struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"w"`
	H        float64 `json:"h"`
	Problem  *string `json:"problem"`
	Children []box   `json:"children"`
}

func dump(n *resolve.RNode) box {
	b := box{ID: n.ID, Type: n.Type, X: n.X, Y: n.Y, W: n.W, H: n.H, Children: []box{}}
	if n.Problem != "" {
		b.Problem = &n.Problem
	}
	for _, k := range n.Kids {
		b.Children = append(b.Children, dump(k))
	}
	return b
}

// noText is the measurer for the parity fixtures, which hold no text: Rust measures with system fonts.
type noText struct{}

func (noText) Metrics(layout.TextStyle) layout.Metrics               { panic("parity fixtures are text-free") }
func (noText) Width(string, layout.TextStyle) float64                { panic("parity fixtures are text-free") }
func (noText) Wrap(string, float64, layout.TextStyle) []resolve.Line { return nil } // notes only

// open does what `nalarupa-cli layout` does: load, migrate, repair ids, save, lay out every top-level node.
func open(t *testing.T, path, saved string, m layout.TextMeasurer) []box {
	t.Helper()
	d, err := doc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	if err := d.Save(saved); err != nil {
		t.Fatal(err)
	}
	r := resolve.New(d.Root, nil)
	trees := []box{}
	for _, n := range d.Children() {
		if rn := r.Resolve(n); rn != nil {
			layout.Layout(rn, m)
			trees = append(trees, dump(rn))
		}
	}
	return trees
}

func sameBoxes(t *testing.T, got, want []box, at string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d nodes, want %d", at, len(got), len(want))
	}
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-6*math.Max(1, math.Abs(b)) }
	for i := range got {
		g, w := got[i], want[i]
		id := at + "/" + w.ID
		if g.ID != w.ID || g.Type != w.Type || (g.Problem == nil) != (w.Problem == nil) || (g.Problem != nil && *g.Problem != *w.Problem) {
			t.Fatalf("%s: got %s %s %v, want %s %s %v", id, g.ID, g.Type, deref(g.Problem), w.ID, w.Type, deref(w.Problem))
		}
		if !near(g.X, w.X) || !near(g.Y, w.Y) || !near(g.W, w.W) || !near(g.H, w.H) {
			t.Fatalf("%s: got (%v, %v, %v, %v), want (%v, %v, %v, %v)", id, g.X, g.Y, g.W, g.H, w.X, w.Y, w.W, w.H)
		}
		sameBoxes(t, g.Children, w.Children, id)
	}
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func TestParity(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.pen"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, f := range fixtures {
		name := strings.TrimSuffix(filepath.Base(f), ".pen")
		t.Run(name, func(t *testing.T) {
			goldenLayout := filepath.Join("testdata", name+".layout.json")
			goldenSaved := filepath.Join("testdata", name+".saved.json")
			wantLayout, err := os.ReadFile(goldenLayout)
			var wantSaved []byte
			if err == nil {
				wantSaved, err = os.ReadFile(goldenSaved)
			}
			if err != nil {
				t.Fatal(err)
			}
			saved := filepath.Join(t.TempDir(), "go.pen")
			got := open(t, f, saved, noText{})
			gotSaved, err := os.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotSaved, wantSaved) {
				t.Errorf("saved document differs from golden:\n%s", firstDiff(gotSaved, wantSaved))
			}
			var want []box
			if err := json.Unmarshal(wantLayout, &want); err != nil {
				t.Fatal(err)
			}
			sameBoxes(t, got, want, name)
		})
	}
}

func TestLiveTextLayout(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "live", "*.pen"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no live fixtures", err)
	}
	fonts := text.New(t.TempDir())
	fonts.Online = false
	for _, f := range fixtures {
		name := strings.TrimSuffix(filepath.Base(f), ".pen")
		t.Run(name, func(t *testing.T) {
			saved1 := filepath.Join(t.TempDir(), "first.pen")
			saved2 := filepath.Join(t.TempDir(), "second.pen")
			got1 := open(t, f, saved1, fonts)
			got2 := open(t, f, saved2, fonts)
			if len(got1) == 0 {
				t.Fatal("no laid-out nodes")
			}
			sameBoxes(t, got1, got2, name)
			b1, err1 := os.ReadFile(saved1)
			b2, err2 := os.ReadFile(saved2)
			if err1 != nil || err2 != nil {
				t.Fatal(err1, err2)
			}
			if !bytes.Equal(b1, b2) {
				t.Errorf("repeat save differs:\n%s", firstDiff(b1, b2))
			}
			byID := map[string]box{}
			var walk func([]box)
			walk = func(nodes []box) {
				for _, n := range nodes {
					if n.Problem != nil {
						t.Errorf("%s: unexpected problem %q", n.ID, *n.Problem)
					}
					if math.IsNaN(n.W) || math.IsInf(n.W, 0) || math.IsNaN(n.H) || math.IsInf(n.H, 0) || n.W < 0 || n.H < 0 {
						t.Errorf("%s: invalid size (%v, %v)", n.ID, n.W, n.H)
					}
					if n.Type == "text" {
						if n.ID == "zero" {
							// fixed-width with no width (0) wraps one word per line.
							if n.W != 0 || n.H <= 40 {
								t.Errorf("text zero box = (%v, %v), want width 0 and wrapped height > 40", n.W, n.H)
							}
						} else if n.W <= 0 || n.H <= 0 {
							t.Errorf("text %s has empty bounds (%v, %v)", n.ID, n.W, n.H)
						}
					}
					byID[n.ID] = n
					walk(n.Children)
				}
			}
			walk(got1)
			if body, ok := byID["body"]; ok {
				// Fixed-width paragraph (width 280 inside 320px card with 20px horizontal padding) must wrap past one line.
				if body.W != 280 || body.H <= 28 {
					t.Errorf("body box = (%v, %v), want width 280 and wrapped height > 28", body.W, body.H)
				}
			}
			if fixed, ok := byID["fixed"]; ok {
				if fixed.W != 80 || fixed.H != 30 {
					t.Errorf("fixed box = (%v, %v), want (80, 30)", fixed.W, fixed.H)
				}
			}
		})
	}
}

func firstDiff(a, b []byte) string {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := range max(len(al), len(bl)) {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d\n  got:  %s\n  want: %s", i+1, x, y)
		}
	}
	return "(same lines)"
}

func TestParityIDs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "ids.json"))
	if err != nil {
		t.Fatal(err)
	}
	var runs []struct {
		Seed uint64   `json:"seed"`
		IDs  []string `json:"ids"`
	}
	if err := json.Unmarshal(data, &runs); err != nil || len(runs) == 0 {
		t.Fatal(err)
	}
	for _, run := range runs {
		doc.Reseed(run.Seed)
		d := doc.New()
		got := make([]string, len(run.IDs))
		for i := range got {
			got[i] = d.GenID()
		}
		if len(got) != 100 || !slices.Equal(got, run.IDs) {
			t.Errorf("seed %d: %v\nwant %v", run.Seed, got, run.IDs)
		}
	}
}
