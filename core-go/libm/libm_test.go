package libm

import (
	"math"
	"testing"
)

// Whole-degree turns, as rotation matrices take them, round like a correctly rounded libm where Go's math
// does not.
func TestRotationAngles(t *testing.T) {
	pi := math.Pi
	rad := func(d float64) float64 { return -(d * (pi / 180)) }
	for _, c := range []struct {
		deg, sin, cos float64
	}{
		{90, -1, 6.123233995736766e-17},
		{10, -0.17364817766693033, 0.984807753012208},
		{180, -1.2246467991473532e-16, -1},
		{30, -0.49999999999999994, 0.8660254037844387},
	} {
		if s, co := Sin(rad(c.deg)), Cos(rad(c.deg)); s != c.sin || co != c.cos {
			t.Errorf("%v°: sin %v cos %v, want %v %v", c.deg, s, co, c.sin, c.cos)
		}
	}
	if Atan2(1, 1) != math.Pi/4 || Tan(0) != 0 || math.Signbit(Sin(math.Copysign(0, -1))) != true {
		t.Error("special cases")
	}
}

// SinCos gives exactly Sin and Cos, special cases included.
func TestSinCosIsSinAndCos(t *testing.T) {
	same := func(a, b float64) bool { return math.Float64bits(a) == math.Float64bits(b) || a != a && b != b }
	for _, x := range []float64{0, math.Copysign(0, -1), 0.3, -2.5, 1e6, 1e300, math.Inf(1), math.NaN()} {
		if s, c := SinCos(x); !same(s, Sin(x)) || !same(c, Cos(x)) {
			t.Errorf("SinCos(%v) = %v, %v; want %v, %v", x, s, c, Sin(x), Cos(x))
		}
	}
}
