// Port of svgtypes 0.16.1 src/path.rs tests (tests and simple_tests)

package usvg

import (
	"math"
	"testing"
)

func stMoveTo(abs bool, x, y float64) stPathSegment {
	return stPathSegment{Kind: stPathMoveTo, Abs: abs, X: x, Y: y}
}
func stLineTo(abs bool, x, y float64) stPathSegment {
	return stPathSegment{Kind: stPathLineTo, Abs: abs, X: x, Y: y}
}
func stHLineTo(abs bool, x float64) stPathSegment {
	return stPathSegment{Kind: stPathHorizontalLineTo, Abs: abs, X: x}
}
func stVLineTo(abs bool, y float64) stPathSegment {
	return stPathSegment{Kind: stPathVerticalLineTo, Abs: abs, Y: y}
}
func stCurveTo(abs bool, x1, y1, x2, y2, x, y float64) stPathSegment {
	return stPathSegment{Kind: stPathCurveTo, Abs: abs, X1: x1, Y1: y1, X2: x2, Y2: y2, X: x, Y: y}
}
func stArcTo(abs bool, rx, ry, rot float64, large, sweep bool, x, y float64) stPathSegment {
	return stPathSegment{Kind: stPathEllipticalArc, Abs: abs, Rx: rx, Ry: ry, XAxisRotation: rot, LargeArc: large, Sweep: sweep, X: x, Y: y}
}
func stClose(abs bool) stPathSegment { return stPathSegment{Kind: stPathClosePath, Abs: abs} }

func TestSTPathParser(t *testing.T) {
	allSegs := func(abs bool) []stPathSegment {
		return []stPathSegment{
			stMoveTo(abs, 10, 20), stLineTo(abs, 30, 40), stHLineTo(abs, 50), stVLineTo(abs, 60),
			stCurveTo(abs, 70, 80, 90, 100, 110, 120),
			{Kind: stPathSmoothCurveTo, Abs: abs, X2: 130, Y2: 140, X: 150, Y: 160},
			{Kind: stPathQuadratic, Abs: abs, X1: 170, Y1: 180, X: 190, Y: 200},
			{Kind: stPathSmoothQuadratic, Abs: abs, X: 210, Y: 220},
			stArcTo(abs, 50, 50, 30, true, true, 230, 240),
			stClose(abs),
		}
	}
	sep := []stPathSegment{stMoveTo(true, 10, 20), stLineTo(true, 5, 15), stCurveTo(true, 10, 20, 30, 40, 50, 60)}
	cases := []struct {
		name, text string
		segs       []stPathSegment
	}{
		{"null", "", nil},
		{"not_a_path", "q", nil},
		{"not_a_move_to", "L 20 30", nil},
		{"stop_on_err_1", "M 10 20 L 30 40 L 50", []stPathSegment{stMoveTo(true, 10, 20), stLineTo(true, 30, 40)}},
		{"move_to_1", "M 10 20", []stPathSegment{stMoveTo(true, 10, 20)}},
		{"move_to_2", "m 10 20", []stPathSegment{stMoveTo(false, 10, 20)}},
		{"move_to_3", "M 10 20 30 40 50 60", []stPathSegment{stMoveTo(true, 10, 20), stLineTo(true, 30, 40), stLineTo(true, 50, 60)}},
		{"move_to_4", "M 10 20 30 40 50 60 M 70 80 90 100 110 120", []stPathSegment{
			stMoveTo(true, 10, 20), stLineTo(true, 30, 40), stLineTo(true, 50, 60),
			stMoveTo(true, 70, 80), stLineTo(true, 90, 100), stLineTo(true, 110, 120)}},
		{"arc_to_1", "M 10 20 A 5 5 30 1 1 20 20", []stPathSegment{stMoveTo(true, 10, 20), stArcTo(true, 5, 5, 30, true, true, 20, 20)}},
		{"arc_to_2", "M 10 20 a 5 5 30 0 0 20 20", []stPathSegment{stMoveTo(true, 10, 20), stArcTo(false, 5, 5, 30, false, false, 20, 20)}},
		{"arc_to_10", "M10-20A5.5.3-4 010-.1", []stPathSegment{stMoveTo(true, 10, -20), stArcTo(true, 5.5, 0.3, -4, false, true, 0, -0.1)}},
		{"separator_1", "M 10 20 L 5 15 C 10 20 30 40 50 60", sep},
		{"separator_2", "M 10, 20 L 5, 15 C 10, 20 30, 40 50, 60", sep},
		{"separator_3", "M 10,20 L 5,15 C 10,20 30,40 50,60", sep},
		{"separator_4", "M10, 20 L5, 15 C10, 20 30 40 50 60", sep},
		{"separator_5", "M10 20V30H40V50H60Z", []stPathSegment{
			stMoveTo(true, 10, 20), stVLineTo(true, 30), stHLineTo(true, 40), stVLineTo(true, 50), stHLineTo(true, 60), stClose(true)}},
		{"all_segments_1", "M 10 20 L 30 40 H 50 V 60 C 70 80 90 100 110 120 S 130 140 150 160\n        Q 170 180 190 200 T 210 220 A 50 50 30 1 1 230 240 Z", allSegs(true)},
		{"all_segments_2", "m 10 20 l 30 40 h 50 v 60 c 70 80 90 100 110 120 s 130 140 150 160\n        q 170 180 190 200 t 210 220 a 50 50 30 1 1 230 240 z", allSegs(false)},
		{"close_path_1", "M10 20 L 30 40 ZM 100 200 L 300 400", []stPathSegment{
			stMoveTo(true, 10, 20), stLineTo(true, 30, 40), stClose(true), stMoveTo(true, 100, 200), stLineTo(true, 300, 400)}},
		{"close_path_2", "M10 20 L 30 40 zM 100 200 L 300 400", []stPathSegment{
			stMoveTo(true, 10, 20), stLineTo(true, 30, 40), stClose(false), stMoveTo(true, 100, 200), stLineTo(true, 300, 400)}},
		{"close_path_3", "M10 20 L 30 40 Z Z Z", []stPathSegment{
			stMoveTo(true, 10, 20), stLineTo(true, 30, 40), stClose(true), stClose(true), stClose(true)}},
		// The first token should be an error.
		{"invalid_1", "M\t.", nil},
		// ClosePath can't be followed by a number.
		{"invalid_2", "M 0 0 Z 2", []stPathSegment{stMoveTo(true, 0, 0), stClose(true)}},
		// ClosePath can be followed by any command.
		{"invalid_3", "M 0 0 Z H 10", []stPathSegment{stMoveTo(true, 0, 0), stClose(true), stHLineTo(true, 10)}},
	}
	for _, c := range cases {
		p := stNewPathParser(c.text)
		for i, want := range c.segs {
			got, ok, err := p.Next()
			if !ok || err != nil || got != want {
				t.Errorf("%s: segment %d = %+v, %v, %v; want %+v", c.name, i, got, ok, err, want)
			}
		}
		if _, ok, err := p.Next(); ok && err == nil {
			t.Errorf("%s: expected end or an error", c.name)
		}
	}
}

func TestSTPathHelpers(t *testing.T) {
	if !stMoveTo(true, 0, 0).IsAbs() || stMoveTo(false, 0, 0).IsAbs() {
		t.Error("IsAbs")
	}
	if stMoveTo(true, 0, 0).Command() != 'M' {
		t.Error("Command")
	}
	// The is_abs and command doc examples.
	p := stNewPathParser("M10-20l30.1.5.1-20z")
	var segs []stPathSegment
	for {
		s, ok, err := p.Next()
		if !ok {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		segs = append(segs, s)
	}
	want := []stPathSegment{stMoveTo(true, 10, -20), stLineTo(false, 30.1, 0.5), stLineTo(false, 0.1, -20), stClose(false)}
	if len(segs) != len(want) {
		t.Fatalf("got %d segments", len(segs))
	}
	for i := range want {
		if segs[i] != want[i] || segs[i].Command() != "Mllz"[i] {
			t.Errorf("segment %d = %+v (%c)", i, segs[i], segs[i].Command())
		}
	}
}

func TestSTSimplifyingPathParser(t *testing.T) {
	M := func(x, y float64) stSimplePathSegment { return stSimplePathSegment{Kind: stSimpleMoveTo, X: x, Y: y} }
	L := func(x, y float64) stSimplePathSegment { return stSimplePathSegment{Kind: stSimpleLineTo, X: x, Y: y} }
	C := func(x1, y1, x2, y2, x, y float64) stSimplePathSegment {
		return stSimplePathSegment{stSimpleCurveTo, x1, y1, x2, y2, x, y}
	}
	Q := func(x1, y1, x, y float64) stSimplePathSegment {
		return stSimplePathSegment{Kind: stSimpleQuadratic, X1: x1, Y1: y1, X: x, Y: y}
	}
	Z := stSimplePathSegment{Kind: stSimpleClosePath}
	arcCurve := C(1.0, 3.4262134833347355, 1.7409707866677877, 1.9442719099991592, 2.9999999999999996, 1.0000000000000004)
	cases := []struct {
		name, text string
		segs       []stSimplePathSegment
	}{
		{"ignore_duplicated_close_paths", "M 10 20 L 30 40 Z Z Z Z", []stSimplePathSegment{M(10, 20), L(30, 40), Z}},
		{"relative_move_to", "m 30 40 110 120 -20 -130", []stSimplePathSegment{M(30, 40), L(140, 160), L(120, 30)}},
		{"smooth_curve_to_after_move_to", "M 30 40 S 171 45 180 155", []stSimplePathSegment{M(30, 40), C(30, 40, 171, 45, 180, 155)}},
		{"smooth_curve_to_after_curve_to", "M 30 40 C 16 137 171 45 100 90 S 171 45 180 155", []stSimplePathSegment{
			M(30, 40), C(16, 137, 171, 45, 100, 90), C(29, 135, 171, 45, 180, 155)}},
		{"relative_smooth_curve_to_after_arc_to", "M 1 5 A 5 5 0 0 1 3 1 s 3 2 8 2", []stSimplePathSegment{
			M(1, 5), arcCurve, C(2.9999999999999996, 1.0000000000000004, 6.0, 3.0000000000000004, 11.0, 3.0000000000000004)}},
		{"smooth_quadratic_after_move_to", "M 30 40 T 180 155", []stSimplePathSegment{M(30, 40), Q(30, 40, 180, 155)}},
		{"smooth_quadratic_after_quadratic", "M 30 40 Q 171 45 100 90 T 160 180", []stSimplePathSegment{
			M(30, 40), Q(171, 45, 100, 90), Q(29, 135, 160, 180)}},
		{"relative_smooth_quadratic_after_quadratic", "M 30 40 Q 171 45 100 90 t 60 80", []stSimplePathSegment{
			M(30, 40), Q(171, 45, 100, 90), Q(29, 135, 160, 170)}},
		{"relative_smooth_quadratic_after_relative_quadratic", "M 30 40 q 171 45 50 40 t 60 80", []stSimplePathSegment{
			M(30, 40), Q(201, 85, 80, 80), Q(-41, 75, 140, 160)}},
		{"smooth_quadratic_after_smooth_quadratic", "M 30 30 T 40 140 T 170 30", []stSimplePathSegment{
			M(30, 30), Q(30, 30, 40, 140), Q(50, 250, 170, 30)}},
		{"smooth_quadratic_after_relative_smooth_quadratic", "M 30 30 T 40 140 t 100 -30", []stSimplePathSegment{
			M(30, 30), Q(30, 30, 40, 140), Q(50, 250, 140, 110)}},
		{"smooth_quadratic_after_relative_quadratic", "M 30 30 T 40 140 q 30 100 120 -30", []stSimplePathSegment{
			M(30, 30), Q(30, 30, 40, 140), Q(70, 240, 160, 110)}},
		{"smooth_quadratic_after_relative_smooth_curve_to", "M 30 30 T 40 170 s 90 -20 90 -90", []stSimplePathSegment{
			M(30, 30), Q(30, 30, 40, 170), C(40, 170, 130, 150, 130, 80)}},
		{"quadratic_after_smooth_quadratic", "M 30 30 T 40 140 Q 80 180 170 30", []stSimplePathSegment{
			M(30, 30), Q(30, 30, 40, 140), Q(80, 180, 170, 30)}},
		{"relative_smooth_quadratic_to_after_arc_to", "M 1 5 A 5 5 0 0 1 3 1 t 8 2", []stSimplePathSegment{
			M(1, 5), arcCurve, Q(2.9999999999999996, 1.0000000000000004, 11.0, 3.0000000000000004)}},
		{"implicit_move_to_after_close_path", "M 10 20 L 30 40 Z L 50 60", []stSimplePathSegment{
			M(10, 20), L(30, 40), Z, M(10, 20), L(50, 60)}},
	}
	for _, c := range cases {
		p := stNewSimplifyingPathParser(c.text)
		for i, want := range c.segs {
			got, ok, err := p.Next()
			if !ok || err != nil || got != want {
				t.Errorf("%s: segment %d = %+v, %v, %v; want %+v", c.name, i, got, ok, err, want)
			}
		}
		if _, ok, err := p.Next(); ok && err == nil {
			t.Errorf("%s: expected end or an error", c.name)
		}
	}
}

func TestSTSimplifyingPathParserArcTo(t *testing.T) {
	p := stNewSimplifyingPathParser("M 30 40 A 40 30 20 1 1 150 100")
	if s, ok, err := p.Next(); !ok || err != nil || s != (stSimplePathSegment{Kind: stSimpleMoveTo, X: 30, Y: 40}) {
		t.Fatalf("first = %+v, %v, %v", s, ok, err)
	}
	c1, _, _ := p.Next()
	c2, _, _ := p.Next()
	if _, ok, err := p.Next(); ok && err == nil {
		t.Fatal("expected end or an error")
	}
	for i, c := range []struct {
		seg  stSimplePathSegment
		want [6]float64
	}{
		{c1, [6]float64{45, 16, 84, 10, 117, 27}},
		{c2, [6]float64{150, 43, 165, 76, 150, 100}},
	} {
		s := c.seg
		got := [6]float64{math.Round(s.X1), math.Round(s.Y1), math.Round(s.X2), math.Round(s.Y2), math.Round(s.X), math.Round(s.Y)}
		if s.Kind != stSimpleCurveTo || got != c.want {
			t.Errorf("curve %d = %+v (rounded %v); want %v", i+1, s, got, c.want)
		}
	}
}
