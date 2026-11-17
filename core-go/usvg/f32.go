// Package usvg is a Go port of usvg 0.47 (core/vendor/usvg): it parses SVG into a simplified tree of
// groups, paths, images and text (laid out and flattened into paths), with styles, references, `use`,
// units, paint servers and filters resolved.
//
// Floating-point products are wrapped in explicit float32/float64 conversions, which the Go spec says
// prevents fusing them into a multiply-add, so results do not depend on GOAMD64 or the architecture.
package usvg

import "math"

// Shared float helpers, ported from Rust's f32/f64 methods and strict-num/float-cmp.

// minF32 is f32::min as Rust compiles it on x86-64: a NaN operand gives the other operand, and equal
// operands (+0 and -0) give the first one.
func minF32(a, b float32) float32 {
	if a != a || b < a {
		return b
	}
	return a
}

// maxF32 is f32::max, see minF32.
func maxF32(a, b float32) float32 {
	if a != a || b > a {
		return b
	}
	return a
}

// maxF64 is f64::max, see minF32.
func maxF64(a, b float64) float64 {
	if a != a || b > a {
		return b
	}
	return a
}

// asI32 is Rust's `x as i32` for a float (f32 converts to f64 exactly): truncated, saturating, NaN
// gives 0.
func asI32(x float64) int32 {
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

// asU32 is Rust's `x as u32` for a float: truncated, saturating, NaN and negatives give 0.
func asU32(x float64) uint32 {
	switch {
	case x != x || x <= 0:
		return 0
	case x >= math.MaxUint32:
		return math.MaxUint32
	}
	return uint32(x)
}

func isFiniteF32(x float32) bool { return math.Float32bits(x)&0x7f800000 != 0x7f800000 }

// isValidLengthF32 is usvg's IsValidLength: > 0 and finite.
func isValidLengthF32(x float32) bool { return x > 0 && isFiniteF32(x) }

// approxEqUlpsF32 is float-cmp's f32::approx_eq_ulps.
func approxEqUlpsF32(a, b float32, ulps int32) bool {
	if a == b {
		return true
	}
	if math.Signbit(float64(a)) != math.Signbit(float64(b)) {
		return false
	}
	diff := int32(math.Float32bits(a)) - int32(math.Float32bits(b)) // wrapping, as wrapping_sub
	return diff >= -ulps && diff <= ulps
}

// approxZeroUlpsF32 is usvg's ApproxZeroUlps for f32.
func approxZeroUlpsF32(x float32, ulps int32) bool { return approxEqUlpsF32(x, 0, ulps) }

// f32Bound is usvg's parser::f32_bound: val clamped to [min, max] (all finite).
func f32Bound(min, val, max float32) float32 {
	if val > max {
		return max
	} else if val < min {
		return min
	}
	return val
}

// normalizedClampedF32 is strict-num's NormalizedF32::new_clamped: clamped to 0..=1, zero for NaN or
// infinity.
func normalizedClampedF32(n float32) float32 {
	if !isFiniteF32(n) {
		return 0
	}
	return maxF32(minF32(1, n), 0) // clamp_f32: max.min(val).max(min)
}

// newNormalizedF32 is strict-num's NormalizedF32::new: finite and within 0..=1.
func newNormalizedF32(n float32) (float32, bool) {
	return n, isFiniteF32(n) && n >= 0 && n <= 1
}

// newPositiveF32 is strict-num's PositiveF32::new: finite and >= 0.
func newPositiveF32(n float32) (float32, bool) { return n, isFiniteF32(n) && n >= 0 }

// newNonZeroPositiveF32 is strict-num's NonZeroPositiveF32::new: finite and > 0.
func newNonZeroPositiveF32(n float32) (float32, bool) { return n, isFiniteF32(n) && n > 0 }
