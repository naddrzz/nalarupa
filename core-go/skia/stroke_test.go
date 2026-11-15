package skia

// The Rust unit tests of dash.rs and stroker.rs.

import (
	"math"
	"testing"
)

func TestStrokeDashNew(t *testing.T) {
	inf := float32(math.Inf(1))
	for _, c := range []struct {
		array  []float32
		offset float32
	}{
		{nil, 0}, {[]float32{1}, 0}, {[]float32{1, 2, 3}, 0}, {[]float32{1, -2}, 0}, {[]float32{0, 0}, 0},
		{[]float32{1, -1}, 0}, {[]float32{1, 1}, inf}, {[]float32{1, inf}, 0},
	} {
		if NewStrokeDash(c.array, c.offset) != nil {
			t.Errorf("NewStrokeDash(%v, %v) is valid", c.array, c.offset)
		}
	}
}

func TestDashBug26(t *testing.T) {
	pb := NewPathBuilder()
	pb.MoveTo(665.54, 287.3)
	pb.LineTo(675.67, 273.04)
	pb.LineTo(675.52, 271.32)
	pb.LineTo(674.79, 269.61)
	pb.LineTo(674.05, 268.04)
	pb.LineTo(672.88, 266.47)
	pb.LineTo(671.27, 264.9)
	path := pb.Finish()
	if path.Dash(NewStrokeDash([]float32{6, 4.5}, 0), 1) == nil {
		t.Fatal("no dashed path")
	}
}

func f32bits(b uint32) float32 { return math.Float32frombits(b) }

// Subpath auto closing is on while stroking.
func TestStrokeAutoClose(t *testing.T) {
	pb := NewPathBuilder()
	pb.MoveTo(10, 10)
	pb.LineTo(20, 50)
	pb.LineTo(30, 10)
	pb.Close()
	stroke := DefaultStroke()
	sp := NewPathStroker().Stroke(pb.Finish(), &stroke, 1)
	if sp == nil {
		t.Fatal("no stroked path")
	}
	m := func(x, y float32) PathSegment { return PathSegment{Verb: VerbMove, Pts: [3]Point{{x, y}}} }
	l := func(x, y float32) PathSegment { return PathSegment{Verb: VerbLine, Pts: [3]Point{{x, y}}} }
	z := PathSegment{Verb: VerbClose}
	want := []PathSegment{
		m(10.485071, 9.878732), l(20.485071, 49.878731), l(20, 50), l(19.514929, 49.878731),
		l(29.514929, 9.878732), l(30, 10), l(30, 10.5), l(10, 10.5), l(10, 10), l(10.485071, 9.878732), z,
		m(9.3596115, 9.5), l(30.640388, 9.5), l(20.485071, 50.121269), l(19.514929, 50.121269),
		l(9.514929, 10.121268), l(9.3596115, 9.5), z,
	}
	it := sp.Segments()
	it.SetAutoClose(true)
	for i, w := range want {
		if got, ok := it.Next(); !ok || got != w {
			t.Fatalf("segment %d: %+v, want %+v", i, got, w)
		}
	}
}

func strokeOf(t *testing.T, width float32, build func(pb *PathBuilder)) *Path {
	t.Helper()
	pb := NewPathBuilder()
	build(pb)
	stroke := DefaultStroke()
	stroke.Width = width
	return NewPathStroker().Stroke(pb.Finish(), &stroke, 1)
}

// The rest are from skia/tests/StrokeTest.cpp and StrokerTest.cpp.

func TestStrokeCubic1(t *testing.T) {
	sp := strokeOf(t, 0.394537568, func(pb *PathBuilder) {
		pb.MoveTo(51.0161362, 1511.52478)
		pb.CubicTo(51.0161362, 1511.52478, 51.0161362, 1511.52478, 51.0161362, 1511.52478)
	})
	if sp != nil {
		t.Fatal("a point cubic strokes")
	}
}

func TestStrokeCubic2(t *testing.T) {
	sp := strokeOf(t, 0.394537568, func(pb *PathBuilder) {
		pb.MoveTo(f32bits(0x424c1086), f32bits(0x44bcf0cb)) // 51.0161362, 1511.52478
		pb.CubicTo(
			f32bits(0x424c107c), f32bits(0x44bcf0cb), // 51.0160980, 1511.52478
			f32bits(0x424c10c2), f32bits(0x44bcf0cb), // 51.0163651, 1511.52478
			f32bits(0x424c1119), f32bits(0x44bcf0ca), // 51.0166969, 1511.52466
		)
	})
	if sp == nil {
		t.Fatal("no stroked path")
	}
}

// skbug.com/6491: a large stroke width can cause numerical instabilities.
func TestStrokeBig(t *testing.T) {
	sp := strokeOf(t, 1.49679073e+10, func(pb *PathBuilder) {
		pb.MoveTo(f32bits(0x46380000), f32bits(0xc6380000)) // 11776, -11776
		pb.LineTo(f32bits(0x46a00000), f32bits(0xc6a00000)) // 20480, -20480
		pb.LineTo(f32bits(0x468c0000), f32bits(0xc68c0000)) // 17920, -17920
		pb.LineTo(f32bits(0x46100000), f32bits(0xc6100000)) // 9216, -9216
		pb.LineTo(f32bits(0x46380000), f32bits(0xc6380000)) // 11776, -11776
		pb.Close()
	})
	if sp == nil {
		t.Fatal("no stroked path")
	}
}

func TestQuadStrokerOneOff(t *testing.T) {
	sp := strokeOf(t, 164.683548, func(pb *PathBuilder) {
		pb.MoveTo(f32bits(0x43c99223), f32bits(0x42b7417e))
		pb.QuadTo(f32bits(0x4285d839), f32bits(0x43ed6645), f32bits(0x43c941c8), f32bits(0x42b3ace3))
	})
	if sp == nil {
		t.Fatal("no stroked path")
	}
}

func TestCubicStrokerOneOff(t *testing.T) {
	sp := strokeOf(t, 42.835968, func(pb *PathBuilder) {
		pb.MoveTo(f32bits(0x433f5370), f32bits(0x43d1f4b3))
		pb.CubicTo(f32bits(0x4331cb76), f32bits(0x43ea3340), f32bits(0x4388f498), f32bits(0x42f7f08d), f32bits(0x43f1cd32), f32bits(0x42802ec1))
	})
	if sp == nil {
		t.Fatal("no stroked path")
	}
}
