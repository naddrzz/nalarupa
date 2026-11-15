// Text parity with the Rust core: each case of testdata/text-cases.json is measured here and by
// `nalarupa-cli measure` on the same machine, with the same (temporary) font cache. Live only: the result
// depends on the installed fonts, so it runs when NALARUPA_CLI is set (relative paths are relative to the
// core-go module root).
package text

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/layout"
)

type measureCase struct {
	Text  string `json:"text"`
	Style struct {
		Family        string   `json:"family,omitempty"`
		Size          float64  `json:"size,omitempty"`
		Weight        uint16   `json:"weight,omitempty"`
		Italic        bool     `json:"italic,omitempty"`
		LetterSpacing float64  `json:"letterSpacing,omitempty"`
		LineHeight    *float64 `json:"lineHeight,omitempty"`
	} `json:"style"`
	MaxW *float64 `json:"maxW,omitempty"`
}

type measured struct {
	Metrics struct {
		Ascent   float64 `json:"ascent"`
		Descent  float64 `json:"descent"`
		LineH    float64 `json:"lineH"`
		Baseline float64 `json:"baseline"`
	} `json:"metrics"`
	Width float64 `json:"width"`
	Lines []struct {
		Text  string  `json:"text"`
		Width float64 `json:"width"`
		Soft  bool    `json:"soft"`
	} `json:"lines"`
}

func liveCLI(t *testing.T) string {
	t.Helper()
	cli := os.Getenv("NALARUPA_CLI")
	if cli == "" {
		t.Skip("text parity is live only: set NALARUPA_CLI to the release nalarupa-cli")
	}
	// The test cache never rechecks the CLI: a file in the module written during the run keeps it uncached.
	if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cli) {
		cli = filepath.Join("..", cli)
	}
	return cli
}

func (c measureCase) style() layout.TextStyle {
	st := layout.TextStyle{Family: c.Style.Family, Size: c.Style.Size, Weight: c.Style.Weight, Italic: c.Style.Italic, LetterSpacing: c.Style.LetterSpacing}
	if st.Family == "" {
		st.Family = "Inter"
	}
	if st.Size == 0 {
		st.Size = 14
	}
	if st.Weight == 0 {
		st.Weight = 400
	}
	if c.Style.LineHeight != nil {
		st.LineHeight = *c.Style.LineHeight
	}
	return st
}

// measureGo does what `nalarupa-cli measure` does.
func measureGo(f *Fonts, cases []measureCase) []measured {
	out := make([]measured, len(cases))
	for i, c := range cases {
		st := c.style()
		m := f.Metrics(st)
		r := &out[i]
		r.Metrics.Ascent, r.Metrics.Descent, r.Metrics.LineH, r.Metrics.Baseline = m.Ascent, m.Descent, m.LineH, m.Baseline
		r.Width = f.Width(c.Text, st)
		maxW := math.Inf(1)
		if c.MaxW != nil {
			maxW = *c.MaxW
		}
		for _, l := range f.Wrap(c.Text, maxW, st) {
			r.Lines = append(r.Lines, struct {
				Text  string  `json:"text"`
				Width float64 `json:"width"`
				Soft  bool    `json:"soft"`
			}{l.Text, l.Width, l.Soft})
		}
	}
	return out
}

func measureRust(t *testing.T, cli string, cases []measureCase, cache string) []measured {
	t.Helper()
	in := filepath.Join(t.TempDir(), "cases.json")
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in, data, 0o666); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(cli, "measure", in, cache)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s measure: %v\n%s", cli, err, stderr.String())
	}
	var want []measured
	if err := json.Unmarshal(out, &want); err != nil || len(want) != len(cases) {
		t.Fatalf("measure output: %v (%d results for %d cases)", err, len(want), len(cases))
	}
	return want
}

func sameMeasures(t *testing.T, cases []measureCase, got, want []measured) {
	t.Helper()
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-4 }
	for i := range cases {
		g, w, at := got[i], want[i], cases[i].Style.Family+" "+strings.ReplaceAll(cases[i].Text, "\n", `\n`)
		if !near(g.Metrics.Ascent, w.Metrics.Ascent) || !near(g.Metrics.Descent, w.Metrics.Descent) || !near(g.Metrics.LineH, w.Metrics.LineH) || !near(g.Metrics.Baseline, w.Metrics.Baseline) {
			t.Errorf("%s: metrics %+v, want %+v", at, g.Metrics, w.Metrics)
		}
		if !near(g.Width, w.Width) {
			t.Errorf("%s: width %v, want %v", at, g.Width, w.Width)
		}
		if len(g.Lines) != len(w.Lines) {
			t.Errorf("%s: lines %+v, want %+v", at, g.Lines, w.Lines)
			continue
		}
		for j := range g.Lines {
			if g.Lines[j].Text != w.Lines[j].Text || g.Lines[j].Soft != w.Lines[j].Soft || !near(g.Lines[j].Width, w.Lines[j].Width) {
				t.Errorf("%s: line %d %+v, want %+v", at, j, g.Lines[j], w.Lines[j])
			}
		}
	}
}

func TestParityMeasure(t *testing.T) {
	cli := liveCLI(t)
	data, err := os.ReadFile(filepath.Join("..", "testdata", "text-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []measureCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	sameMeasures(t, cases, measureGo(New(cache), cases), measureRust(t, cli, cases, cache))
}

// A variable font in the cache's var/ folder replaces the installed copies of its family and serves every
// weight through `wght`. It takes the variable font file in NALARUPA_VARIABLE_FONT (CI downloads one), else
// an installed one (Segoe UI Variable on Windows); in CI a missing font fails the test instead of skipping it.
func TestParityVariableFont(t *testing.T) {
	cli := liveCLI(t)
	var family, path string
	faces := New(t.TempDir()).db.faces()
	if path = os.Getenv("NALARUPA_VARIABLE_FONT"); path != "" {
		db := newDB()
		db.loadFile(path)
		faces = db.faces()
	}
	for _, x := range faces {
		if x.src.path != "" && x.index == 0 && x.isVariable() && (family == "" || x.families[0].name == "Segoe UI Variable") {
			family, path = x.families[0].name, x.src.path
		}
	}
	if family == "" {
		msg := "no variable font: set NALARUPA_VARIABLE_FONT to a font file with a wght axis, or install one (for example Segoe UI Variable)"
		if os.Getenv("CI") != "" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "var"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(varFile(cache, family, false), data, 0o666); err != nil {
		t.Fatal(err)
	}
	var cases []measureCase
	for _, w := range []uint16{300, 450, 700} {
		c := measureCase{Text: "Variable weight AVA wrapping text", MaxW: new(float64)}
		*c.MaxW = 120
		c.Style.Family, c.Style.Size, c.Style.Weight = family, 18, w
		cases = append(cases, c)
	}
	got := measureGo(New(cache), cases)
	if got[0].Width == got[2].Width {
		t.Fatalf("%s measures the same at 300 and 700 (%v)", family, got[0].Width)
	}
	sameMeasures(t, cases, got, measureRust(t, cli, cases, cache))
}
