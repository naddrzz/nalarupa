package skia

// The Rust unit tests of scalar.rs, size.rs, rect.rs, transform.rs and path_geometry.rs.

import (
	"math"
	"slices"
	"testing"
)

// mulAdd is a*b+c written as the port writes it, in a function of its own so the compiler cannot fold
// its operands.
//
//go:noinline
func mulAdd(a, b, c float32) float32 { return float32(a*b) + c }

// Bit parity needs every a*b+c rounded twice, as Rust does: a fused multiply-add rounds once. The port
// converts each product explicitly, which the Go spec says prevents fusing (arm64, GOAMD64=v3).
func TestNoFusedMultiplyAdd(t *testing.T) {
	// a*a is 1 + 2^-11 + 2^-24, which rounds to 1 + 2^-11.
	a, c := float32(1+1.0/(1<<12)), float32(-(1 + 1.0/(1<<11)))
	if got := mulAdd(a, a, c); got != 0 {
		t.Fatalf("float32(a*b)+c = %g, not 0: the product was fused into a multiply-add", got)
	}
}

func TestBound(t *testing.T) {
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	for _, c := range []struct{ in, want float32 }{
		{nan, 1}, {inf, 1}, {-inf, 0}, {f32Epsilon, f32Epsilon}, {0.5, 0.5}, {-1, 0}, {2, 1},
	} {
		if got := bound(c.in, 0, 1); got != c.want {
			t.Errorf("bound(%v, 0, 1) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIntSize(t *testing.T) {
	for _, wh := range [][2]uint32{{0, 0}, {1, 0}, {0, 1}} {
		if _, ok := IntSizeFromWH(wh[0], wh[1]); ok {
			t.Errorf("IntSizeFromWH%v is valid", wh)
		}
	}
	size, _ := IntSizeFromWH(3, 4)
	want, _ := IntRectFromXYWH(1, 2, 3, 4)
	if got := size.ToIntRect(1, 2); got != want {
		t.Errorf("ToIntRect = %v, want %v", got, want)
	}
}

func TestIntRect(t *testing.T) {
	for _, c := range [][4]int64{
		{0, 0, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1},
		{0, 0, math.MaxUint32, math.MaxUint32}, {0, 0, 1, math.MaxUint32}, {0, 0, math.MaxUint32, 1},
		{math.MaxInt32, 0, 1, 1}, {0, math.MaxInt32, 1, 1},
	} {
		if _, ok := IntRectFromXYWH(int32(c[0]), int32(c[1]), uint32(c[2]), uint32(c[3])); ok {
			t.Errorf("IntRectFromXYWH%v is valid", c)
		}
	}
	r := func(x, y int32, w, h uint32) IntRect { v, _ := IntRectFromXYWH(x, y, w, h); return v }
	if _, ok := r(1, 2, 3, 4).Intersect(r(11, 12, 13, 14)); ok {
		t.Error("disjoint rects intersect")
	}
	if got, _ := r(1, 2, 30, 40).Intersect(r(11, 12, 13, 14)); got != r(11, 12, 13, 14) {
		t.Errorf("inner intersection = %v", got)
	}
	if got, _ := r(1, 2, 30, 40).Intersect(r(11, 12, 50, 60)); got != r(11, 12, 20, 30) {
		t.Errorf("partial intersection = %v", got)
	}
}

func TestRect(t *testing.T) {
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	for _, c := range [][4]float32{
		{10, 10, 5, 10}, {10, 10, 10, 5}, {nan, 10, 10, 10}, {10, nan, 10, 10}, {10, 10, nan, 10}, {10, 10, 10, nan}, {10, 10, 10, inf},
	} {
		if _, ok := RectFromLTRB(c[0], c[1], c[2], c[3]); ok {
			t.Errorf("RectFromLTRB%v is valid", c)
		}
	}
	r, _ := RectFromLTRB(10, 20, 30, 40)
	if r.Left() != 10 || r.Top() != 20 || r.Right() != 30 || r.Bottom() != 40 || r.Width() != 20 || r.Height() != 20 {
		t.Errorf("rect %+v", r)
	}
	r, _ = RectFromLTRB(-30, 20, -10, 40)
	if r.Width() != 20 || r.Height() != 20 {
		t.Errorf("rect %+v", r)
	}
}

func TestRectTransform(t *testing.T) {
	rect, _ := RectFromLTRB(1, 2, 3, 4)
	ltrb := func(l, t, r, b float32) Rect { v, _ := RectFromLTRB(l, t, r, b); return v }
	for _, c := range []struct {
		ts   Transform
		want Rect
	}{
		{Identity(), rect},
		{TransformFromScale(1, 2), ltrb(1, 4, 3, 8)},
		// Skewing along x: vertical lines at y=c go to y=c+x, horizontal lines stay; the result bounds it.
		{TransformFromSkew(1, 0), ltrb(3, 2, 7, 4)},
		{TransformFromSkew(0, 2), ltrb(1, 4, 3, 10)},
	} {
		if got, _ := rect.Transform(c.ts); got != c.want {
			t.Errorf("%+v: %+v, want %+v", c.ts, got, c.want)
		}
	}
}

func TestTransform(t *testing.T) {
	if Identity() != TransformFromRow(1, 0, 0, 1, 0, 0) ||
		TransformFromScale(1, 2) != TransformFromRow(1, 0, 0, 2, 0, 0) ||
		TransformFromSkew(2, 3) != TransformFromRow(1, 3, 2, 1, 0, 0) ||
		TransformFromTranslate(5, 6) != TransformFromRow(1, 0, 0, 1, 5, 6) {
		t.Error("constructors")
	}
	type flags [8]bool // identity, scale, skew, translate, scale-translate, has scale, has skew, has translate
	get := func(ts Transform) flags {
		return flags{ts.IsIdentity(), ts.IsScale(), ts.IsSkew(), ts.IsTranslate(), ts.IsScaleTranslate(), ts.HasScale(), ts.HasSkew(), ts.HasTranslate()}
	}
	for _, c := range []struct {
		ts   Transform
		want flags
	}{
		{Identity(), flags{true, false, false, false, false, false, false, false}},
		{TransformFromScale(2, 3), flags{false, true, false, false, true, true, false, false}},
		{TransformFromSkew(2, 3), flags{false, false, true, false, false, false, true, false}},
		{TransformFromTranslate(2, 3), flags{false, false, false, true, true, false, false, true}},
		{TransformFromRow(1, 2, 3, 4, 5, 6), flags{false, false, false, false, false, true, true, true}},
	} {
		if got := get(c.ts); got != c.want {
			t.Errorf("%+v: %v, want %v", c.ts, got, c.want)
		}
	}
	if TransformFromScale(1, 1).HasScale() || TransformFromSkew(0, 0).HasSkew() || TransformFromTranslate(0, 0).HasTranslate() {
		t.Error("neutral parts count")
	}
}

func TestTransformConcat(t *testing.T) {
	ts := TransformFromRow(1.2, 3.4, -5.6, -7.8, 1.2, 3.4).PreScale(2, -4)
	if want := TransformFromRow(2.4, 6.8, 22.4, 31.2, 1.2, 3.4); ts != want {
		t.Errorf("pre_scale: %+v, want %+v", ts, want)
	}
	ts = TransformFromRow(1.2, 3.4, -5.6, -7.8, 1.2, 3.4).PostScale(2, -4)
	if want := TransformFromRow(2.4, -13.6, -11.2, 31.2, 2.4, -13.6); ts != want {
		t.Errorf("post_scale: %+v, want %+v", ts, want)
	}
}

// A transformed path owns its storage: clearing and rebuilding it leaves the original intact.
func TestPathTransformOwnsStorage(t *testing.T) {
	pb := NewPathBuilder()
	pb.MoveTo(1, 2)
	pb.LineTo(3, 4)
	pb.QuadTo(5, 6, 7, 8)
	orig := pb.Finish()
	verbs := append([]PathVerb(nil), orig.Verbs()...)
	points := append([]Point(nil), orig.Points()...)
	for _, ts := range []Transform{Identity(), TransformFromTranslate(10, 20)} {
		b := orig.Transform(ts).Clear()
		b.MoveTo(-1, -1)
		b.CubicTo(9, 9, 9, 9, 9, 9)
		b.Close()
		b.Finish()
		if !slices.Equal(orig.Verbs(), verbs) || !slices.Equal(orig.Points(), points) {
			t.Fatalf("%+v: original changed to %v %v", ts, orig.Verbs(), orig.Points())
		}
	}
}

func TestEvalCubicAt1(t *testing.T) {
	src := [4]Point{{30, 40}, {30, 40}, {171, 45}, {180, 155}}
	if got := evalCubicPosAt(&src, 0); got != (Point{30, 40}) {
		t.Errorf("pos %v", got)
	}
	if got := evalCubicTangentAt(&src, 0); got != (Point{141, 5}) {
		t.Errorf("tangent %v", got)
	}
}

func TestFindCubicMaxCurvature1(t *testing.T) {
	src := [4]Point{{20, 160}, {20.0001, 160}, {160, 20}, {160.0001, 20}}
	var ts [3]float32
	n := findCubicMaxCurvature(&src, &ts)
	if n != 3 || ts != [3]float32{0, 0.5, 1} {
		t.Errorf("%v", ts[:n])
	}
}
