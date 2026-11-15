package skia

import "math"

// Rasteriser helpers (tiny-skia's math.rs) and Rust cast semantics used across the raster files.
//
// Rust release builds wrap on integer overflow, as Go does for same-width integers. Rust's `f as uN`/`f as
// iN` saturate (NaN gives 0); use castI32, castU32, castU8 and castU16 for them, never a plain Go
// conversion.

// leftShift is math::left_shift: the shift done on the unsigned value, so it never overflows.
func leftShift(value, shift int32) int32 { return int32(uint32(value) << uint32(shift)) }

// leftShift64 is math::left_shift64.
func leftShift64(value int64, shift int32) int64 { return int64(uint64(value) << uint32(shift)) }

// castU8 is Rust's saturating `f as u8`: NaN and negatives give 0.
func castU8(x float32) uint8 {
	switch {
	case x != x || x <= 0:
		return 0
	case x >= 255:
		return 255
	}
	return uint8(x)
}

// castU16 is Rust's saturating `f as u16`: NaN and negatives give 0.
func castU16(x float32) uint16 {
	switch {
	case x != x || x <= 0:
		return 0
	case x >= math.MaxUint16:
		return math.MaxUint16
	}
	return uint16(x)
}

// clamp32 is f32::clamp: NaN stays NaN.
func clamp32(x, lo, hi float32) float32 {
	if x < lo {
		x = lo
	}
	if x > hi {
		x = hi
	}
	return x
}

// approxPowf is math::approx_powf, Skia's fast approximation
// (http://www.machinedlearnings.com/2011/06/fast-approximate-logarithm-exponential.html).
func approxPowf(x, y float32) float32 {
	if x == 0 || x == 1 {
		return x
	}

	e := float32(float32(math.Float32bits(x)) * float32(1.0/(1<<23)))
	m := math.Float32frombits((math.Float32bits(x) & 0x007fffff) | 0x3f000000)

	log2X := e - 124.225514990 - float32(1.498030302*m) - 1.725879990/(0.3520887068+m)

	x = float32(log2X * y)

	f := x - floor32(x)

	a := x + 121.274057500
	a -= float32(f * 1.490129070)
	a += 27.728023300 / (4.84252568 - f)
	a = float32(a * float32(1<<23))

	if a < float32(math.Float32bits(float32(math.Inf(1)))) {
		if a > 0 {
			return math.Float32frombits(castU32(float64(round32(a))))
		}
		return 0
	}
	return float32(math.Inf(1))
}
