package usvg

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// textDoc parses an SVG into an svgtree document.
func textDoc(t *testing.T, body string) *svgDocument {
	t.Helper()
	xml, err := xmlParseDoc(svgDoc(body), xmlParsingOptions{AllowDTD: true, NodesLimit: math.MaxUint32})
	if err != nil {
		t.Fatal(err)
	}
	doc, terr := svgParseTree(xml, "", false)
	if terr != nil {
		t.Fatal(terr)
	}
	return doc
}

func firstText(t *testing.T, doc *svgDocument) svgNode {
	t.Helper()
	for n := range doc.Descendants() {
		if n.hasTagName(eidText) {
			return n
		}
	}
	t.Fatal("no text element")
	return svgNode{}
}

func textState(doc *svgDocument) *converterState {
	opt := DefaultOptions()
	vb, _ := skia.NonZeroRectFromXYWH(0, 0, 200, 100)
	return &converterState{viewBox: vb, opt: &opt}
}

// Positions examples from the resolve_positions_list doc comment.
func TestResolvePositionsList(t *testing.T) {
	xs := func(body string) []string {
		doc := textDoc(t, body)
		var out []string
		for _, p := range resolvePositionsList(firstText(t, doc), textState(doc)) {
			if p.x == nil {
				out = append(out, "-")
			} else {
				out = append(out, fmtFloat(*p.x))
			}
		}
		return out
	}
	if got := strings.Join(xs(`<text>a<tspan x="10 20 30">bc</tspan>d</text>`), " "); got != "- 10 20 -" {
		t.Errorf("clamped list: %s", got)
	}
	if got := strings.Join(xs(`<text><tspan x="100 110 120 130">a<tspan x="50">bc</tspan></tspan>d</text>`), " "); got != "100 50 120 -" {
		t.Errorf("nested list: %s", got)
	}
}

func fmtFloat(f float32) string { return strconv.FormatFloat(float64(f), 'g', -1, 32) }

func itoa(i int) string { return strconv.Itoa(i) }

// The rotate example of the SVG 1.1 spec: characters past a list take its last angle.
func TestResolveRotateList(t *testing.T) {
	doc := textDoc(t, `<text rotate="1 2">abcd<tspan rotate="5">ef</tspan>g</text>`)
	got := resolveRotateList(firstText(t, doc))
	want := []float32{1, 2, 2, 2, 5, 5, 2}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestTextChunksAndSpans(t *testing.T) {
	doc := textDoc(t, `<defs><path id="p" d="M0 0 L100 0"/></defs>`+
		`<text x="5" font-size="10">ab<tspan fill="red">c</tspan><tspan x="50">d</tspan><textPath href="#p">e</textPath>f</text>`)
	st := textState(doc)
	chunks := collectTextChunks(firstText(t, doc), resolvePositionsList(firstText(t, doc), st), st, newConverterCache())
	var got []string
	for _, c := range chunks {
		s := c.text + ":"
		for _, sp := range c.spans {
			s += " " + itoa(sp.start) + "-" + itoa(sp.end)
		}
		if c.textFlow != nil {
			s += " path " + c.textFlow.id
		}
		got = append(got, s)
	}
	want := "abc: 0-2 2-3|d: 0-1|e: 0-1 path p|f: 0-1"
	if strings.Join(got, "|") != want {
		t.Errorf("chunks %q, want %q", strings.Join(got, "|"), want)
	}
}

func TestFontParsing(t *testing.T) {
	doc := textDoc(t, `<g font-weight="bold"><text font-family="'My Font', serif" font-style="oblique" font-stretch="condensed"`+
		` style="font-variation-settings: 'wght' 650, 'opsz' 12, 'bad' 1, &quot;wdth&quot; x">a<tspan font-weight="bolder">b</tspan></text></g>`)
	st := textState(doc)
	f := convertFont(firstText(t, doc), st)
	if len(f.families) != 2 || f.families[0].Name != "My Font" || f.families[1].Generic != text.GenericSerif {
		t.Errorf("families %v", f.families)
	}
	if f.weight != 700 || f.style != FontStyleOblique || f.stretch != FontStretchCondensed {
		t.Errorf("weight %d style %d stretch %d", f.weight, f.style, f.stretch)
	}
	// Explicit wght wins; wdth and slnt come from font-stretch and font-style.
	var vars []string
	for _, v := range f.variations {
		vars = append(vars, string(v.Tag[:])+"="+fmtFloat(v.Value))
	}
	if got := strings.Join(vars, " "); got != "wght=650 opsz=12 wdth=75 slnt=-12" {
		t.Errorf("variations %s", got)
	}
	var tspan svgNode
	for n := range doc.Descendants() {
		if n.hasTagName(eidTspan) {
			tspan = n
		}
	}
	// bolder steps from 700 by 100.
	if w := resolveFontWeight(tspan); w != 800 {
		t.Errorf("bolder weight %d", w)
	}
	doc = textDoc(t, `<text>a</text>`)
	if f := convertFont(firstText(t, doc), textState(doc)); len(f.families) != 1 || f.families[0].Name != "Times New Roman" {
		t.Errorf("default family %v", f.families)
	}
}

func TestRustParseF32(t *testing.T) {
	for s, want := range map[string]float32{"700": 700, "-1.5": -1.5, "+2e2": 200, ".5": 0.5, "5.": 5, "1e40": float32(math.Inf(1))} {
		if v, ok := rustParseF32(s); !ok || v != want {
			t.Errorf("%q: %v %v", s, v, ok)
		}
	}
	if v, ok := rustParseF32("NaN"); !ok || v == v {
		t.Errorf("NaN: %v %v", v, ok)
	}
	for _, s := range []string{"", "x", "0x10", "1_0", "+-1", "1e", "."} {
		if _, ok := rustParseF32(s); ok {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestBaselineShift(t *testing.T) {
	doc := textDoc(t, `<text font-size="10"><tspan baseline-shift="sub"><tspan baseline-shift="50%">a</tspan></tspan>`+
		`<tspan baseline-shift="baseline">b</tspan></text>`)
	st := textState(doc)
	var spans []svgNode
	for n := range doc.Descendants() {
		if n.hasTagName(eidTspan) {
			spans = append(spans, n)
		}
	}
	got := convertBaselineShift(spans[1], st)
	if len(got) != 2 || got[0].Kind != BaselineShiftNumber || got[0].Number != 5 || got[1].Kind != BaselineShiftSubscript {
		t.Errorf("shifts %+v", got)
	}
	if got := convertBaselineShift(spans[2], st); got != nil {
		t.Errorf("only baseline: %+v", got)
	}
}

// Ported from unicode-bidi's test_reorder_line, for the cases whose paragraph is left-to-right (usvg
// always uses an LTR paragraph level) and for the first paragraph only.
func TestBidiReorderLine(t *testing.T) {
	reorder := func(s string) string {
		levels, runs := bidiVisualRuns(s)
		var b strings.Builder
		for _, r := range runs {
			part := s[r.start:r.end]
			if levelIsRTL(levels[r.start]) {
				rs := []rune(part)
				for i := len(rs) - 1; i >= 0; i-- {
					b.WriteRune(rs[i])
				}
			} else {
				b.WriteString(part)
			}
		}
		return b.String()
	}
	for in, want := range map[string]string{
		"abc\ndef\nghi":        "abc\n",
		"ab1\nde2":             "ab1\n",
		"1.-2":                 "1.-2",
		"1-.2":                 "1-.2",
		"abc אבג":              "abc גבא",
		"abc‪def":              "abc‪def",
		"abc‪def‬ghi":          "abc‪def‬ghi",
		"abc⁦def⁩ghi":          "abc⁦def⁩ghi",
		"‫abc אבג‬":            "‫גבא abc‬",
		"A אבג?":               "A גבא?",
		"A אבג?‏":              "A ‏?גבא",
		"abc⁧.-⁩ghi":           "abc⁧-.⁩ghi",
		"Hello, ⁨‮world‬⁩!":    "Hello, ⁨‮‬dlrow⁩!",
		"a א(ב)ג.":             "a ג)ב(א.",
		"Hel lo שלום. 123 end": "Hel lo 123 .םולש end",
	} {
		if got := reorder(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	if levels, runs := bidiVisualRuns(""); levels != nil || runs != nil {
		t.Error("empty text has runs")
	}
}

// Ported from unicode-bidi's test_isolating_run_sequences (BD13 examples).
func TestBidiIsolatingRunSequences(t *testing.T) {
	runs := func(seqs []bidiSequence) string {
		var parts []string
		for _, s := range seqs {
			var rs []string
			for _, r := range s.runs {
				rs = append(rs, itoa(r.start)+".."+itoa(r.end))
			}
			parts = append(parts, strings.Join(rs, ","))
		}
		return strings.Join(parts, " ")
	}
	classes := []bidiClass{bidiL, bidiRLI, bidiL, bidiPDI, bidiRLI, bidiL, bidiPDI, bidiL}
	levels := []uint8{0, 0, 1, 0, 0, 1, 0, 0}
	got := bidiIsolatingRunSequences(0, classes, levels,
		[]bidiRun{{0, 2}, {2, 3}, {3, 5}, {5, 6}, {6, 8}}, true)
	if r := runs(got); r != "2..3 5..6 0..2,3..5,6..8" {
		t.Errorf("example 2: %s", r)
	}
}

// Ported from kurbo's cubicbez_arclen and cubicbez_inv_arclen tests.
func TestKurboArclen(t *testing.T) {
	c := kurboCubic{kurboPoint{0, 0}, kurboPoint{1.0 / 3, 0}, kurboPoint{2.0 / 3, 1.0 / 3}, kurboPoint{1, 1}}
	trueLen := 0.5*math.Sqrt(5) + 0.25*math.Log(2+math.Sqrt(5))
	for i := 0; i < 12; i++ {
		acc := math.Pow(0.1, float64(i))
		if err := c.arclen(acc) - trueLen; math.Abs(err) >= acc {
			t.Errorf("accuracy %g: error %g", acc, err)
		}
	}
	c = kurboCubic{kurboPoint{0, 0}, kurboPoint{100.0 / 3, 0}, kurboPoint{200.0 / 3, 100.0 / 3}, kurboPoint{100, 100}}
	trueLen *= 100
	for i := 0; i < 12; i++ {
		acc := math.Pow(0.1, float64(i))
		for j := 0; j <= 10; j++ {
			arc := float64(j) * (0.1 * trueLen)
			tt := c.invArclen(arc, acc*0.5)
			if actual := c.subsegment(0, tt).arclen(acc * 0.5); math.Abs(arc-actual) >= acc {
				t.Errorf("accuracy %g: wanted %g, got %g", acc, actual, arc)
			}
		}
	}
	// A zero-length segment measures zero instead of subdividing forever.
	if l := kurboCubicFromLine(3, 4, 3, 4).arclen(0.5); l != 0 {
		t.Errorf("degenerate arclen %g", l)
	}
}

func TestUnicodeProperties(t *testing.T) {
	if voIsUpright('A') || !voIsUpright('本') || !voIsUpright(0x1100) || voIsUpright(0x1F200) {
		t.Error("vertical orientation")
	}
	for c, want := range map[rune]bool{'a': true, 'א': true, 'س': false, 'ह': false, 'ᠠ': false, '本': true, ' ': true} {
		if scriptSupportsLetterSpacing(c) != want {
			t.Errorf("letter spacing for %q", c)
		}
	}
	for c, want := range map[rune]bidiClass{'A': bidiL, 'א': bidiR, 'ا': bidiAL, '1': bidiEN, ' ': bidiWS, '\n': bidiB, 0x0300: bidiNSM, 0xFEFF: bidiBN, 0xFDD0: bidiL} {
		if got := bidiClassOf(c); got != want {
			t.Errorf("bidi class of %U: %d, want %d", c, got, want)
		}
	}
}

// Without fonts (the default empty database) text is dropped, as in Rust.
func TestTextWithoutFonts(t *testing.T) {
	tree := parse(t, svgDoc(`<text id="t" x="10" y="20">hi</text><rect width="1" height="1"/>`))
	if tree.HasTextNodes() || len(tree.root.children) != 1 {
		t.Errorf("text kept without fonts: %d children", len(tree.root.children))
	}
	if tree.FontDB() == nil {
		t.Error("no font database")
	}
}

// Degenerate text gives no node, whatever the fonts.
func TestTextDegenerate(t *testing.T) {
	fonts := text.New(t.TempDir())
	fonts.Online = false
	opt := DefaultOptions()
	opt.FontDB = fonts.Database()
	opt.FontResolver = FontsResolver(fonts)
	for _, body := range []string{
		`<text x="10" y="20"></text>`,
		`<text x="10" y="20">   </text>`,
		`<text x="10" y="20" font-size="0">zero</text>`,
		`<text x="10" y="20" font-size="-5">negative</text>`,
		`<text x="10" y="20" display="none">hidden</text>`,
		`<text x="10" y="20"><textPath href="#none">no path</textPath></text>`,
	} {
		tree, err := FromStr(svgDoc(body), &opt)
		if err != nil {
			t.Fatal(err)
		}
		if tree.HasTextNodes() {
			t.Errorf("%s: text node kept", body)
		}
	}
}
