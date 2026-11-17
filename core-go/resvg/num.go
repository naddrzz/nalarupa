package resvg

import (
	"math"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// Rust numeric semantics the port relies on.

func floor32(x float32) float32 { return float32(math.Floor(float64(x))) }
func ceil32(x float32) float32  { return float32(math.Ceil(float64(x))) }

// round32 is f32::round: half away from zero.
func round32(x float32) float32 { return float32(math.Round(float64(x))) }

func sqrt32(x float32) float32 { return float32(math.Sqrt(float64(x))) }

// cos32, sin32 and pow32 stand in for the platform libm's cosf, sinf and powf, which Rust calls: the
// results can differ from the CRT's in the last bit.
func cos32(x float32) float32    { return float32(math.Cos(float64(x))) }
func sin32(x float32) float32    { return float32(math.Sin(float64(x))) }
func pow32(x, y float32) float32 { return float32(math.Pow(float64(x), float64(y))) }

// radsPerDeg is f32::to_radians' factor, PI / 180 rounded once in f32.
var radsPerDeg = func() float32 { pi, deg := float32(math.Pi), float32(180); return pi / deg }()

func toRadians(deg float32) float32 { return float32(deg * radsPerDeg) }

// asI32 is Rust's `x as i32` for a float: truncated, saturating, NaN gives 0.
func asI32(x float32) int32 {
	f := float64(x)
	switch {
	case f != f:
		return 0
	case f >= math.MaxInt32:
		return math.MaxInt32
	case f <= math.MinInt32:
		return math.MinInt32
	}
	return int32(f)
}

// asI32F64 is `x as i32` for an f64.
func asI32F64(f float64) int32 {
	switch {
	case f != f:
		return 0
	case f >= math.MaxInt32:
		return math.MaxInt32
	case f <= math.MinInt32:
		return math.MinInt32
	}
	return int32(f)
}

// asI64F64 is `x as i64` for an f64.
func asI64F64(f float64) int64 {
	switch {
	case f != f:
		return 0
	case f >= math.MaxInt64:
		return math.MaxInt64
	case f <= math.MinInt64:
		return math.MinInt64
	}
	return int64(f)
}

// asU32 is Rust's `x as u32` for a float: truncated, saturating, NaN and negatives give 0.
func asU32(x float32) uint32 {
	f := float64(x)
	switch {
	case f != f || f <= 0:
		return 0
	case f >= math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(f)
}

// asUsize is `x as usize` for an f32 on a 64-bit target.
func asUsize(x float32) int {
	f := float64(x)
	switch {
	case f != f || f <= 0:
		return 0
	case f >= math.MaxInt:
		return math.MaxInt
	}
	return int(f)
}

// asU8 is Rust's `x as u8` for an f32: truncated, saturating, NaN gives 0.
func asU8(x float32) uint8 {
	switch {
	case x != x || x <= 0:
		return 0
	case x >= 255:
		return 255
	}
	return uint8(x)
}

// asU8F64 is `x as u8` for an f64.
func asU8F64(x float64) uint8 {
	switch {
	case x != x || x <= 0:
		return 0
	case x >= 255:
		return 255
	}
	return uint8(x)
}

func checkedSubI32(a, b int32) (int32, bool) {
	r := a - b
	return r, (b >= 0) == (r <= a)
}

func checkedAddU32(a, b uint32) (uint32, bool) {
	r := a + b
	return r, r >= a
}

// approxZeroUlps32 is float-cmp's f32 approx_eq_ulps against 0.
func approxZeroUlps32(x float32, ulps int32) bool { return approxEqUlps32(x, 0, ulps) }

// approxEqUlps32 is float-cmp's f32::approx_eq_ulps.
func approxEqUlps32(a, b float32, ulps int32) bool {
	if a == b {
		return true
	}
	if math.Signbit(float64(a)) != math.Signbit(float64(b)) {
		return false
	}
	diff := int32(math.Float32bits(a)) - int32(math.Float32bits(b)) // wrapping, as wrapping_sub
	return diff >= -ulps && diff <= ulps
}

// approxZeroUlps64 is float-cmp's f64 approx_eq_ulps against 0.
func approxZeroUlps64(x float64, ulps int64) bool {
	if x == 0 {
		return true
	}
	if math.Signbit(x) {
		return false
	}
	diff := int64(math.Float64bits(x)) // minus the bits of +0
	return diff >= -ulps && diff <= ulps
}

// f32Bound is resvg's filter::f32_bound: val clamped to [min, max].
func f32Bound(min, val, max float32) float32 {
	if val > max {
		return max
	} else if val < min {
		return min
	}
	return val
}

// opacityToU8 is NormalizedF32::to_u8.
func opacityToU8(v float32) uint8 { return asU8(float32(v*255) + 0.5) }

// mulOpacity is NormalizedF32's Mul: the product clamped to 0..=1 (zero for NaN or infinity).
func mulOpacity(a, b float32) float32 {
	n := float32(a * b)
	if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
		return 0
	}
	return max(min(n, 1), 0)
}

// clonePixmap is Pixmap::clone.
func clonePixmap(p *skia.Pixmap) *skia.Pixmap {
	c, _ := skia.PixmapFromVec(append([]byte(nil), p.Data()...), p.Size())
	return c
}
