package layout

import (
	"math"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// stub measures every character as 10 px wide; a line is round(lineHeight × size) px, or 20 px without a
// line height. Words wrap greedily at spaces and are never split.
type stub struct{}

func (stub) Metrics(st TextStyle) Metrics {
	if st.LineHeight > 0 {
		return Metrics{LineH: math.Round(st.LineHeight * st.Size)}
	}
	return Metrics{LineH: 20}
}

func (stub) Width(text string, st TextStyle) float64 {
	n := float64(utf8.RuneCountInString(text))
	return n*10 + st.LetterSpacing*n
}

func (s stub) Wrap(text string, maxW float64, st TextStyle) []resolve.Line {
	var out []resolve.Line
	for _, para := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line != "" && s.Width(line+" "+word, st) > maxW+0.01 {
				out = append(out, resolve.Line{Text: line, Width: s.Width(line, st), Soft: true})
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		out = append(out, resolve.Line{Text: line, Width: s.Width(line, st)})
	}
	return out
}

func lay(t *testing.T, node string) *resolve.RNode {
	t.Helper()
	root := jsonv.MustParse(`{"children": [` + node + `]}`)
	n := resolve.New(root, nil).Resolve(jsonv.Items(jsonv.Get(root, "children"))[0])
	Layout(n, stub{})
	return n
}

func rect(w, h string) string {
	return `{"type": "rectangle", "width": ` + w + `, "height": ` + h + `}`
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !slicesEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func slicesEqual(a, b any) bool {
	switch x := a.(type) {
	case []float64:
		return slices.Equal(x, b.([]float64))
	case [2]float64:
		return x == b.([2]float64)
	}
	return a == b
}

func xs(n *resolve.RNode) []float64 {
	var out []float64
	for _, k := range n.Kids {
		out = append(out, k.X)
	}
	return out
}

func xws(n *resolve.RNode) []float64 {
	var out []float64
	for _, k := range n.Kids {
		out = append(out, k.X, k.W)
	}
	return out
}

func TestFlexFillAndJustify(t *testing.T) {
	n := lay(t, `{"type":"frame","id":"f","x":10,"y":20,"width":300,"height":100,"padding":10,"gap":10,
		"justifyContent":"space_between","alignItems":"center","children":[
			{"type":"rectangle","id":"a","width":50,"height":20},
			{"type":"rectangle","id":"b","width":"fill_container","height":"fill_container"},
			{"type":"rectangle","id":"c","width":50,"height":40}]}`)
	eq(t, [2]float64{n.W, n.H}, [2]float64{300, 100})
	b := n.Kids[1]
	// space_between ignores the gap, so the fill child takes everything the others leave.
	eq(t, b.W, 300.0-20.0-100.0)
	eq(t, b.H, 80.0)
	eq(t, n.Kids[0].X, 20.0)
	eq(t, n.Kids[0].Y, 20.0+10.0+30.0)
	eq(t, n.Kids[2].X+50, 300.0)
}

func TestFitContentVerticalAndAbsolute(t *testing.T) {
	n := lay(t, `{"type":"frame","id":"f","layout":"vertical","padding":[8,12],"gap":4,"children":[
		{"type":"rectangle","id":"a","width":100,"height":10},
		{"type":"rectangle","id":"b","width":"fill_container","height":10},
		{"type":"rectangle","id":"c","width":5,"height":5,"layoutPosition":"absolute","x":-10,"y":0}]}`)
	eq(t, [2]float64{n.W, n.H}, [2]float64{124, 16 + 24})
	eq(t, n.Kids[1].W, 100.0)
	eq(t, n.Kids[2].X, -10.0)
	eq(t, n.Kids[2].Problem, "fully clipped")
}

func TestGroupIsUnionOfChildren(t *testing.T) {
	n := lay(t, `{"type":"group","id":"g","x":5,"children":[{"type":"ellipse","id":"e","x":10,"y":10,"width":20,"height":30}]}`)
	eq(t, []float64{n.X, n.Y, n.W, n.H}, []float64{15, 10, 20, 30})
	eq(t, [2]float64{n.BX, n.BY}, [2]float64{10, 10})
	eq(t, n.Kids[0].X, 15.0)
	// In a row, the group's box (not its origin) lands on the slot.
	row := lay(t, `{"type":"frame","id":"r","padding":4,"children":[
		{"type":"group","id":"g","children":[{"type":"rectangle","id":"a","x":-7,"y":3,"width":10,"height":10}]}]}`)
	eq(t, [2]float64{row.W, row.H}, [2]float64{18, 18})
	eq(t, [2]float64{row.Kids[0].X, row.Kids[0].Kids[0].X}, [2]float64{4, 4})
}

func TestFillChildrenDoNotSizeFitParents(t *testing.T) {
	// [A, Fill] in a fit row: width = A + padding, the fill child gets max(1, -gap) = 1.
	n := lay(t, `{"type":"frame","id":"f","gap":8,"padding":2,"children":[`+rect("40", "10")+`, `+rect(`"fill_container"`, `"fill_container(99)"`)+`]}`)
	eq(t, [2]float64{n.W, n.H}, [2]float64{44, 14})
	eq(t, [2]float64{n.Kids[1].W, n.Kids[1].H}, [2]float64{1, 10})
	// Only fill children: the fit parent keeps its fallback.
	n = lay(t, `{"type":"frame","id":"f","width":"fit_content(30)","children":[`+rect(`"fill_container"`, "5")+`]}`)
	eq(t, [2]float64{n.W, n.Kids[0].W}, [2]float64{30, 30})
	// layout:none + fit_content keeps the fallback instead of hugging the children.
	n = lay(t, `{"type":"frame","id":"f","layout":"none","children":[`+rect("40", "10")+`]}`)
	eq(t, [2]float64{n.W, n.H}, [2]float64{0, 0})
	// Three-value padding is invalid → 0.
	n = lay(t, `{"type":"frame","id":"f","padding":[1,2,3],"children":[`+rect("40", "10")+`]}`)
	eq(t, [2]float64{n.W, n.H}, [2]float64{40, 10})
}

func TestSpaceDistributionIgnoresGap(t *testing.T) {
	row := func(j, w string) *resolve.RNode {
		r := rect("20", "5")
		return lay(t, `{"type":"frame","id":"f","width":`+w+`,"gap":50,"justifyContent":"`+j+`","children":[`+r+`,`+r+`,`+r+`]}`)
	}
	eq(t, xs(row("space_between", "100")), []float64{0, 40, 80})
	eq(t, xs(row("space_around", "90")), []float64{5, 35, 65})
	// Overflow packs the children from the start instead of overlapping them (CSS never uses negative space).
	for _, j := range []string{"space_between", "space_around"} {
		eq(t, xs(row(j, "40")), []float64{0, 20, 40})
	}
}

func TestSpaceWithFillChildKeepsNoGap(t *testing.T) {
	kids := rect("40", "20") + `, ` + rect(`"fill_container"`, "20") + `, ` + rect("27", "20")
	for _, j := range []string{"space_between", "space_around"} {
		n := lay(t, `{"type":"frame","id":"f","width":300,"height":100,"gap":18,"justifyContent":"`+j+`","children":[`+kids+`]}`)
		eq(t, xws(n), []float64{0, 40, 40, 233, 273, 27})
	}
	// start keeps the gap.
	n := lay(t, `{"type":"frame","id":"f","width":300,"height":100,"gap":18,"children":[`+kids+`]}`)
	eq(t, xws(n), []float64{0, 40, 58, 197, 273, 27})
	// A fit row with space_between hugs the children without the gap, like the HTML export.
	n = lay(t, `{"type":"frame","id":"f","gap":18,"justifyContent":"space_between","children":[`+rect("40", "20")+`, `+rect("27", "20")+`]}`)
	eq(t, n.W, 67.0)
}

func TestRotatedAndFlippedChildrenUseTheirBoundingBox(t *testing.T) {
	n := lay(t, `{"type":"frame","id":"f","children":[
		{"type":"rectangle","id":"a","width":20,"height":10,"rotation":90},
		{"type":"rectangle","id":"b","width":20,"height":10,"flipX":true}]}`)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if !near(n.W, 30) || !near(n.H, 20) {
		t.Fatal(n.W, n.H)
	}
	// 90° CCW about the origin: box spans (0..10, -20..0) locally, so the origin sits at y = 20.
	if a := n.Kids[0]; !near(a.X, 0) || !near(a.Y, 20) {
		t.Fatal(a.X, a.Y)
	}
	// flipX mirrors about the origin: the origin is the right edge of the slot.
	if !near(n.Kids[1].X, 30) {
		t.Fatal(n.Kids[1].X)
	}
}

func TestTextInLayout(t *testing.T) {
	n := lay(t, `{"type":"frame","id":"f","width":60,"layout":"vertical","children":[
		{"type":"text","id":"t","content":"one two three four","textGrowth":"fixed-width","width":"fill_container","lineHeight":1.5},
		{"type":"text","id":"u","content":"auto text never wraps","width":"fill_container"}]}`)
	tx, u := n.Kids[0], n.Kids[1]
	eq(t, tx.W, 60.0)
	if len(tx.Lines) < 2 || slices.ContainsFunc(tx.Lines, func(l resolve.Line) bool { return l.Width > 60.01 }) {
		t.Fatal(tx.Lines)
	}
	// 10 px per character at 60 px: "one", "two", "three", "four", each line round(1.5 × 14) = 21 px.
	eq(t, len(tx.Lines), 4)
	eq(t, tx.H, float64(len(tx.Lines))*21)
	eq(t, len(u.Lines), 1)
	// Auto text is intrinsic and ceiled: 21 characters, never wrapped by its fill width.
	eq(t, [2]float64{u.W, u.H}, [2]float64{210, 20})
	eq(t, n.H, tx.H+u.H)
	// fixed-width without a width wraps at 0: one word per line.
	z := lay(t, `{"type":"text","id":"z","content":"a b c","textGrowth":"fixed-width"}`)
	eq(t, len(z.Lines), 3)
	w := lay(t, `{"type":"text","id":"w","content":"one two three","textGrowth":"fixed-width"}`)
	eq(t, len(w.Lines), 3) // a word is never split without a width
}

func TestFontWeights(t *testing.T) {
	for v, want := range map[string]uint16{`"bold"`: 700, `"600"`: 600, `500`: 500, `"semibold"`: 400, `null`: 400, `"normal"`: 400, `2000`: 400} {
		if got := WeightNum(jsonv.MustParse(v)); got != want {
			t.Errorf("%s: %d, want %d", v, got, want)
		}
	}
}
