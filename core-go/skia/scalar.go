// Package skia is a port of tiny-skia 0.12 (BSD-3-Clause), the rasteriser resvg draws with. This slice
// holds tiny-skia-path 0.12.0: geometry types, paths, the path builder, curve geometry, dashing and
// stroking.
//
// The port keeps tiny-skia's algorithms, constants and order of floating-point operations, in float32
// wherever Rust uses f32, so results match bit for bit. SIMD helpers (f32x2, f32x4) are plain scalar code
// with the same per lane results. Where Rust calls sin, cos, acos or powf, the Go float64 function rounded
// to float32 is used instead; those results may differ by one ulp. Every product is converted explicitly
// (float32(a*b) + c), which the Go spec says keeps the compiler from fusing it into one multiply-add
// rounding (as it would on arm64 or with GOAMD64=v3); keep that when changing the arithmetic.
package skia

import "math"

// Scalar constants (scalar.rs, floating_point.rs).
const (
	ScalarMax        = float32(3.402823466e+38)
	ScalarNearlyZero = float32(1.0 / (1 << 12))
	ScalarRoot2Over2 = float32(0.707106781)

	floatPI         = float32(3.14159265)
	maxI32FitsInF32 = float32(2147483520.0)
	minI32FitsInF32 = -maxI32FitsInF32
	f32Epsilon      = float32(1.1920929e-7) // f32::EPSILON, 2^-23
)

func isFinite(x float32) bool { return math.Float32bits(x)&0x7f800000 != 0x7f800000 }

func isNaN(x float32) bool { return x != x }

func abs32(x float32) float32 { return math.Float32frombits(math.Float32bits(x) &^ (1 << 31)) }

// sqrt32 is f32::sqrt: the float64 square root of a float32 rounded to float32 is the correctly rounded
// float32 square root, so it matches bit for bit.
func sqrt32(x float32) float32 { return float32(math.Sqrt(float64(x))) }

func floor32(x float32) float32 { return float32(math.Floor(float64(x))) }
func ceil32(x float32) float32  { return float32(math.Ceil(float64(x))) }
func round32(x float32) float32 { return float32(math.Round(float64(x))) } // half away from zero, as f32::round

// The closest Go equivalents of f32::cos, f32::sin, f32::acos and f32::powf: the float64 function rounded
// to float32, which may differ from the C library's float function by one ulp.
func cos32(x float32) float32    { return float32(math.Cos(float64(x))) }
func sin32(x float32) float32    { return float32(math.Sin(float64(x))) }
func acos32(x float32) float32   { return float32(math.Acos(float64(x))) }
func pow32(x, y float32) float32 { return float32(math.Pow(float64(x), float64(y))) }
func mod32(x, y float32) float32 { return float32(math.Mod(float64(x), float64(y))) } // exact, as Rust's %

func half(x float32) float32   { return float32(x * 0.5) }
func ave(a, b float32) float32 { return float32((a + b) * 0.5) }
func sqr(x float32) float32    { return float32(x * x) }
func invert(x float32) float32 { return 1.0 / x }

// minf is f32::min as Rust compiles it on x86-64: a NaN operand gives the other operand, and equal
// operands (+0 and -0) give the first one.
func minf(a, b float32) float32 {
	if isNaN(a) || b < a {
		return b
	}
	return a
}

// maxf is f32::max, see minf.
func maxf(a, b float32) float32 {
	if isNaN(a) || b > a {
		return b
	}
	return a
}

// bound is Scalar::bound, SkTPin: a non-panicking clamp that gives hi for NaN.
func bound(x, lo, hi float32) float32 { return maxf(minf(hi, x), lo) }

func isNearlyEqual(a, b float32) bool { return abs32(a-b) <= ScalarNearlyZero }

func isNearlyEqualWithinTolerance(a, b, tolerance float32) bool { return abs32(a-b) <= tolerance }

func isNearlyZero(x float32) bool { return isNearlyZeroWithinTolerance(x, ScalarNearlyZero) }

func isNearlyZeroWithinTolerance(x, tolerance float32) bool { return abs32(x) <= tolerance }

// almostDequalUlps is from SkPathOpsTypes.
func almostDequalUlps(a, b float32) bool {
	const ulpsEpsilon = 16
	aBits, bBits := f32As2sCompliment(a), f32As2sCompliment(b)
	return aBits < bBits+ulpsEpsilon && bBits < aBits+ulpsEpsilon
}

// f32As2sCompliment returns the float as a 2s complement int, only for comparing floats with each other.
func f32As2sCompliment(x float32) int32 {
	v := int32(math.Float32bits(x))
	if v < 0 {
		v &= 0x7FFFFFFF
		v = -v
	}
	return v
}

// castI32 is Rust's saturating `f as i32`: NaN gives 0.
func castI32(x float64) int32 {
	switch {
	case x != x:
		return 0
	case x >= math.MaxInt32:
		return math.MaxInt32
	case x <= math.MinInt32:
		return math.MinInt32
	}
	return int32(x)
}

// castU32 is Rust's saturating `f as u32`: NaN and negatives give 0.
func castU32(x float64) uint32 {
	switch {
	case x != x || x <= 0:
		return 0
	case x >= math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(x)
}

// saturateFromF32 is i32::saturate_from(f32): the closest integer, MAX_I32_FITS_IN_F32 for NaN.
func saturateFromF32(x float32) int32 {
	if !(x < maxI32FitsInF32) {
		x = maxI32FitsInF32
	}
	if !(x > minI32FitsInF32) {
		x = minI32FitsInF32
	}
	return castI32(float64(x))
}

func saturateFloor(x float32) int32 { return saturateFromF32(floor32(x)) }
func saturateCeil(x float32) int32  { return saturateFromF32(ceil32(x)) }
func saturateRound(x float32) int32 { return saturateFromF32(floor32(x) + 0.5) }

// NormalizedF32Exclusive (0 < n < 1) and NormalizedF32 (0 <= n <= 1) are plain float32 values here; these
// are their checked constructors.

// newNormalizedExclusive is NormalizedF32Exclusive::new.
func newNormalizedExclusive(n float32) (float32, bool) {
	if n > 0 && n < 1 {
		return n, true
	}
	return 0, false
}

// normalizedExclusiveBounded is NormalizedF32Exclusive::new_bounded.
func normalizedExclusiveBounded(n float32) float32 { return bound(n, f32Epsilon, 1-f32Epsilon) }

// newNormalized is NormalizedF32::new.
func newNormalized(n float32) (float32, bool) {
	if isFinite(n) && n >= 0 && n <= 1 {
		return n, true
	}
	return 0, false
}

// normalizedClamped is NormalizedF32::new_clamped: NaN and infinities give 0.
func normalizedClamped(n float32) float32 {
	if isFinite(n) {
		return maxf(minf(1, n), 0)
	}
	return 0
}

// i32 helpers for IntRect, which checks overflow as Rust's checked/saturating arithmetic does.

func fitsI32(v int64) bool { return v >= math.MinInt32 && v <= math.MaxInt32 }

func satI32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}
