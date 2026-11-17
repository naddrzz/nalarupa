// Parity with the Rust core: every fixture in testdata is opened (migrate, repair ids), saved and laid
// out, and the result compared with what the Rust CLI produces (`nalarupa-cli layout <doc> <saved>`).
// Without NALARUPA_CLI the comparison is with the committed goldens (*.layout.json, *.saved.json); with it,
// with the CLI's live output, so drift shows whenever Rust changes. NALARUPA_UPDATE_GOLDEN=1 (with
// NALARUPA_CLI) rewrites the goldens from the CLI. The fixtures in testdata/live hold text, whose layout
// depends on the installed fonts: they are compared live only, laid out with the real text.Fonts.
package core_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
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
	cli := os.Getenv("NALARUPA_CLI")
	update := os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	if update && cli == "" {
		t.Fatal("NALARUPA_UPDATE_GOLDEN needs NALARUPA_CLI")
	}
	if cli != "" {
		// The test cache never rechecks files outside this module, so a rebuilt CLI would get a cached pass.
		// A file in the module written during the run is too new to cache: live runs always rerun.
		if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	fixtures, err := filepath.Glob(filepath.Join("testdata", "*.pen"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, f := range fixtures {
		name := strings.TrimSuffix(filepath.Base(f), ".pen")
		t.Run(name, func(t *testing.T) {
			goldenLayout := filepath.Join("testdata", name+".layout.json")
			goldenSaved := filepath.Join("testdata", name+".saved.json")
			var wantLayout, wantSaved []byte
			if cli != "" {
				wantLayout, wantSaved = rustLayout(t, cli, f)
				if update {
					if os.WriteFile(goldenLayout, wantLayout, 0o666) != nil || os.WriteFile(goldenSaved, wantSaved, 0o666) != nil {
						t.Fatal("cannot write goldens")
					}
				}
			} else {
				if wantLayout, err = os.ReadFile(goldenLayout); err == nil {
					wantSaved, err = os.ReadFile(goldenSaved)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			saved := filepath.Join(t.TempDir(), "go.pen")
			got := open(t, f, saved, noText{})
			gotSaved, err := os.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotSaved, wantSaved) {
				t.Errorf("saved document differs from Rust:\n%s", firstDiff(gotSaved, wantSaved))
			}
			var want []box
			if err := json.Unmarshal(wantLayout, &want); err != nil {
				t.Fatal(err)
			}
			sameBoxes(t, got, want, name)
		})
	}
}

// rustLayout runs `nalarupa-cli layout` on a fixture: the laid-out trees and the saved document.
func rustLayout(t *testing.T, cli, fixture string) (layout, saved []byte) {
	t.Helper()
	rustSaved := filepath.Join(t.TempDir(), "rust.pen")
	var stderr bytes.Buffer
	cmd := exec.Command(cli, "layout", fixture, rustSaved)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s layout %s: %v\n%s", cli, fixture, err, stderr.String())
	}
	if saved, err = os.ReadFile(rustSaved); err != nil {
		t.Fatal(err)
	}
	return out, saved
}

// Text fixtures are laid out with the fonts of this machine, so they are compared with the CLI only. Both
// sides use the CLI's font cache folder (`nalarupa-cli-fonts` in the temp folder).
func TestParityLiveText(t *testing.T) {
	cli := os.Getenv("NALARUPA_CLI")
	if cli == "" {
		t.Skip("text layout parity is live only: set NALARUPA_CLI to the release nalarupa-cli")
	}
	if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
		t.Fatal(err)
	}
	fixtures, err := filepath.Glob(filepath.Join("testdata", "live", "*.pen"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no live fixtures", err)
	}
	fonts := text.New(filepath.Join(os.TempDir(), "nalarupa-cli-fonts"))
	fonts.Online = false // as the CLI
	for _, f := range fixtures {
		name := strings.TrimSuffix(filepath.Base(f), ".pen")
		t.Run(name, func(t *testing.T) {
			wantLayout, wantSaved := rustLayout(t, cli, f)
			saved := filepath.Join(t.TempDir(), "go.pen")
			got := open(t, f, saved, fonts)
			gotSaved, err := os.ReadFile(saved)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(gotSaved, wantSaved) {
				t.Errorf("saved document differs from Rust:\n%s", firstDiff(gotSaved, wantSaved))
			}
			var want []box
			if err := json.Unmarshal(wantLayout, &want); err != nil {
				t.Fatal(err)
			}
			sameBoxes(t, got, want, name)
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
			return fmt.Sprintf("line %d\n  go:   %s\n  rust: %s", i+1, x, y)
		}
	}
	return "(same lines)"
}

// Source of truth: the Rust test `doc::tests::ids_match_the_go_golden` checks testdata/ids.json against Rust.
// The same reseed seed gives the same ids as Rust (testdata/ids.json comes from `doc::reseed` and
// `Doc::gen_id` in the Rust core).
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
