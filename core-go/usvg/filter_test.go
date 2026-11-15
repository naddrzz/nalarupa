package usvg

import (
	"strings"
	"testing"
)

// filterOf returns the only filter of the only child group of the root.
func filterOf(t *testing.T, tree *Tree) *Filter {
	t.Helper()
	g := child[*Group](t, &tree.root, 0)
	if len(g.filters) != 1 {
		t.Fatalf("%d filters, want 1", len(g.filters))
	}
	return g.filters[0]
}

func ref(name string) FilterInput { return FilterInput{Kind: FilterInputReference, Name: name} }

var sourceGraphic = FilterInput{Kind: FilterInputSourceGraphic}

func TestFilterBrokenReferences(t *testing.T) {
	rect := `<rect width="10" height="10" filter="%s"/>`
	for _, tc := range []struct {
		name, filter string
		children     int
	}{
		// A link to a missing element with no valid filter turns the element off (e-filter-051.svg).
		{"missing", "url(#missing)", 0},
		// An invalid link next to a valid filter is not an error.
		{"missing-and-valid", "url(#missing) url(#f)", 1},
		{"not-a-filter", "url(#g)", 0},
		{"no-primitives", "url(#empty)", 0},
		// An unparsable list is skipped as a whole: the element renders unfiltered.
		{"invalid-list", "blur(2px) bogus(1)", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := parse(t, svgDoc(`<filter id="f"><feFlood/></filter><filter id="empty"/><linearGradient id="g"/>`+
				strings.Replace(rect, "%s", tc.filter, 1)))
			if got := len(tree.root.children); got != tc.children {
				t.Fatalf("%d children, want %d", got, tc.children)
			}
		})
	}
}

func TestFilterInputs(t *testing.T) {
	// `in` naming a result defined later (a cycle) or an unknown result falls back to the previous
	// result, or SourceGraphic for the first primitive.
	tree := parse(t, svgDoc(`<filter id="f">
		<feOffset in="b" result="a"/>
		<feOffset in="a" result="b"/>
		<feOffset in="unknown"/>
		<feOffset in="SourceAlpha"/>
		<feOffset in="BackgroundImage"/>
		<feOffset/>
	</filter><rect width="10" height="10" filter="url(#f)"/>`))
	want := []FilterInput{sourceGraphic, ref("a"), ref("b"), {Kind: FilterInputSourceAlpha}, sourceGraphic, ref("result5")}
	f := filterOf(t, tree)
	if len(f.primitives) != len(want) {
		t.Fatalf("%d primitives, want %d", len(f.primitives), len(want))
	}
	for i, w := range want {
		if got := f.primitives[i].kind.(*FeOffset).input; got != w {
			t.Errorf("primitive %d input %+v, want %+v", i, got, w)
		}
	}
}

func TestFilterResultNames(t *testing.T) {
	// A predefined result also advances the index; generated names skip the predefined ones.
	tree := parse(t, svgDoc(`<filter id="f"><feFlood/><feFlood result="result3"/><feFlood/><feFlood/></filter>`+
		`<rect width="10" height="10" filter="url(#f)"/>`))
	var got []string
	for _, p := range filterOf(t, tree).primitives {
		got = append(got, p.result)
	}
	if strings.Join(got, " ") != "result1 result3 result4 result5" {
		t.Errorf("results %v", got)
	}
}

func TestFilterHasInput(t *testing.T) {
	blend := &FeBlend{input1: sourceGraphic, input2: ref("a")}
	merge := &FeMerge{inputs: []FilterInput{ref("x"), ref("a")}}
	for _, tc := range []struct {
		kind FilterKind
		in   FilterInput
		want bool
	}{
		{blend, ref("a"), true},
		{blend, ref("b"), false},
		{merge, ref("a"), true},
		{merge, sourceGraphic, false},
		{&FeFlood{}, sourceGraphic, false},
		{&FeImage{}, sourceGraphic, false},
		{&FeTurbulence{}, sourceGraphic, false},
		{&FeOffset{input: sourceGraphic}, sourceGraphic, true},
	} {
		if got := tc.kind.HasInput(tc.in); got != tc.want {
			t.Errorf("%T.HasInput(%+v) = %v", tc.kind, tc.in, got)
		}
	}
}

func TestConvolveMatrixData(t *testing.T) {
	m, ok := newConvolveMatrixData(1, 0, 2, 3, []float32{1, 2, 3, 4, 5, 6})
	if !ok || m.Get(1, 2) != 6 || m.Get(0, 1) != 3 {
		t.Fatalf("matrix %+v %v", m, ok)
	}
	for _, tc := range []struct{ tx, ty, cols, rows, n uint32 }{
		{0, 0, 2, 2, 3}, // wrong data length
		{2, 0, 2, 2, 4}, // target x out of range
		{0, 2, 2, 2, 4}, // target y out of range
	} {
		if _, ok := newConvolveMatrixData(tc.tx, tc.ty, tc.cols, tc.rows, make([]float32, tc.n)); ok {
			t.Errorf("%+v accepted", tc)
		}
	}
	// columns*rows wraps as in the release build: 2147483647² is 1 modulo 2^32.
	if _, ok := newConvolveMatrixData(0, 0, 2147483647, 2147483647, []float32{5}); !ok {
		t.Error("wrapped size rejected")
	}
}

func TestFeImageUsesGeneratedIDs(t *testing.T) {
	// A sliced data-URL feImage takes an image id and a clip-path id, as image::convert_inner does, so a
	// later sliced image gets clipPath2.
	png := "data:image/png;base64," + testPNG
	tree := parse(t, svgDoc(`<filter id="f"><feImage href="`+png+`" preserveAspectRatio="xMidYMid slice"/></filter>`+
		`<rect width="10" height="10" filter="url(#f)"/>`+
		`<image x="20" width="10" height="10" preserveAspectRatio="xMidYMid slice" href="`+png+`"/>`))
	image := child[*Group](t, &tree.root, 1)
	if image.clipPath == nil || image.clipPath.id != "clipPath2" {
		t.Fatalf("image clip path %+v, want clipPath2", image.clipPath)
	}
	root := filterOf(t, tree).primitives[0].kind.(*FeImage).root
	inner := child[*Group](t, &root, 0)
	if inner.clipPath == nil || inner.clipPath.id != "clipPath1" || inner.id != "image1" {
		t.Fatalf("feImage group id %q clip %+v", inner.id, inner.clipPath)
	}
}

func TestFilterFunctionRegion(t *testing.T) {
	// Filter functions get a generated id and a region around the object bbox; none on a zero-size one.
	tree := parse(t, svgDoc(`<rect x="10" y="20" width="100" height="50" filter="blur(2px) invert(1)"/>`+
		`<line x2="10" stroke="black" filter="blur(2px)"/>`))
	g := child[*Group](t, &tree.root, 0)
	if len(g.filters) != 2 || g.filters[0].id != "filter1" || g.filters[1].id != "filter2" {
		t.Fatalf("filters %+v", g.filters)
	}
	if r := g.filters[0].rect; r.X() != -40 || r.Y() != -5 || r.Width() != 200 || r.Height() != 100 {
		t.Errorf("blur region %v", r)
	}
	if r := g.filters[1].rect; !approx(r.X(), 0) || !approx(r.Width(), 120) {
		t.Errorf("invert region %v", r)
	}
	if p := g.filters[0].primitives[0]; p.colorInterpolation != ColorInterpolationSRGB || p.result != "result" {
		t.Errorf("function primitive %+v", p)
	}
	// Without filters the line needs no group of its own.
	child[*Path](t, &tree.root, 1)
}

func TestFilterPanicsLikeRust(t *testing.T) {
	// usvg unwraps PositiveF32::new on an infinite radius or base frequency and panics; the port mirrors it.
	for _, fe := range []string{`<feMorphology radius="1e39"/>`, `<feTurbulence baseFrequency="1e39"/>`} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", fe)
				}
			}()
			parse(t, svgDoc(`<filter id="f">`+fe+`</filter><rect width="10" height="10" filter="url(#f)"/>`))
		}()
	}
}
