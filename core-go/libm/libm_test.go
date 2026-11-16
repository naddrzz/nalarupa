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
