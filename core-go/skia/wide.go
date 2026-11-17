package skia

import "math"

// SIMD lane types (wide/). Rust builds f32x8, i32x8 and u32x8 from two 4-lane SSE2 halves on x86-64
// (no avx), so every method here is lane-wise scalar code with the SSE2 result of each lane:
// min/max are `a < b ? a : b` / `a > b ? a : b`, comparisons give all-ones or zero lanes, blends are
// bitwise, and float to int conversions give math.MinInt32 for NaN and out of range values.

type f32x4 [4]float32
type f32x8 [8]float32
type i32x8 [8]int32
type u32x8 [8]uint32

const laneTrue = 0xFFFFFFFF

func f32x4Splat(n float32) f32x4 { return f32x4{n, n, n, n} }

func (a f32x4) add(b f32x4) (r f32x4) {
	for i := range r {
		r[i] = a[i] + b[i]
	}
	return
}

func (a f32x4) sub(b f32x4) (r f32x4) {
	for i := range r {
		r[i] = a[i] - b[i]
	}
	return
}

func (a f32x4) mul(b f32x4) (r f32x4) {
	for i := range r {
		r[i] = float32(a[i] * b[i])
	}
	return
}

// sseMin is _mm_min_ps for one lane.
func sseMin(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

// sseMax is _mm_max_ps for one lane.
func sseMax(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func laneMask(c bool) uint32 {
	if c {
		return laneTrue
	}
	return 0
}

// cvtps is _mm_cvtps_epi32 for one lane: round half to even.
func cvtps(x float32) int32 {
	if x != x || x >= 2147483648.0 || x < -2147483648.0 {
		return math.MinInt32
	}
	return int32(math.RoundToEven(float64(x)))
}

// cvttps is _mm_cvttps_epi32 for one lane: truncate.
func cvttps(x float32) int32 {
	if x != x || x >= 2147483648.0 || x < -2147483648.0 {
		return math.MinInt32
	}
	return int32(x)
}

// floorLane is f32x4::floor without simd128: a truncation round trip, minus one when it went up.
func floorLane(x float32) float32 {
	roundtrip := float32(cvttps(x))
	if roundtrip > x {
		return roundtrip - 1
	}
	return roundtrip - 0
}

// roundLane is f32x4::round without sse4.1 (musl's roundf on lanes, with upstream's `- -1.0` kept).
func roundLane(x float32) float32 {
	const toInt = float32(1.0 / f32Epsilon)
	u := math.Float32bits(x)
	e := int32((u >> 23) & 0xff)
	if e >= 0x7f+23 {
		return x
	}
	if e < 0x7f-1 {
		return float32(x * 0)
	}
	neg := int32(u) < 0
	v := x
	if neg {
		v = 0 - x
	}
	y := v + toInt - toInt - v
	switch {
	case y > 0.5:
		y = y + v - -1.0
	case y < -0.5:
		y = y + v + 1.0
	default:
		y = y + v
	}
	if neg {
		y = 0 - y
	}
	return y
}

func f32x8Splat(n float32) f32x8 { return f32x8{n, n, n, n, n, n, n, n} }

func (a f32x8) floor() (r f32x8) {
	for i := range r {
		r[i] = floorLane(a[i])
	}
	return
}

func (a f32x8) fract() f32x8 { return a.sub(a.floor()) }

func (a f32x8) normalize() f32x8 { return a.max(f32x8{}).min(f32x8Splat(1.0)) }

func (a f32x8) toI32x8Bitcast() (r i32x8) {
	for i := range r {
		r[i] = int32(math.Float32bits(a[i]))
	}
	return
}

func (a f32x8) toU32x8Bitcast() (r u32x8) {
	for i := range r {
		r[i] = math.Float32bits(a[i])
	}
	return
}

func f32x8Mask(c [8]bool) (r f32x8) {
	for i := range r {
		r[i] = math.Float32frombits(laneMask(c[i]))
	}
	return
}

func (a f32x8) cmpEq(b f32x8) f32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] == b[i]
	}
	return f32x8Mask(c)
}

// cmpNe is _mm_cmpneq_ps: true when either lane is NaN.
func (a f32x8) cmpNe(b f32x8) f32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] != b[i]
	}
	return f32x8Mask(c)
}

func (a f32x8) cmpGe(b f32x8) f32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] >= b[i]
	}
	return f32x8Mask(c)
}

func (a f32x8) cmpGt(b f32x8) f32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] > b[i]
	}
	return f32x8Mask(c)
}

func (a f32x8) cmpLe(b f32x8) f32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] <= b[i]
	}
	return f32x8Mask(c)
}

func (a f32x8) cmpLt(b f32x8) f32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] < b[i]
	}
	return f32x8Mask(c)
}

// blend picks t where the mask a is set and f elsewhere, bit by bit (generic_bit_blend).
func (a f32x8) blend(t, f f32x8) (r f32x8) {
	for i := range r {
		m, y, n := math.Float32bits(a[i]), math.Float32bits(t[i]), math.Float32bits(f[i])
		r[i] = math.Float32frombits(n ^ ((n ^ y) & m))
	}
	return
}

func (a f32x8) abs() (r f32x8) {
	for i := range r {
		r[i] = math.Float32frombits(math.Float32bits(a[i]) & 0x7fffffff)
	}
	return
}

func (a f32x8) max(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = sseMax(a[i], b[i])
	}
	return
}

func (a f32x8) min(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = sseMin(a[i], b[i])
	}
	return
}

func (a f32x8) round() (r f32x8) {
	for i := range r {
		r[i] = roundLane(a[i])
	}
	return
}

func (a f32x8) roundInt() (r i32x8) {
	for i := range r {
		r[i] = cvtps(a[i])
	}
	return
}

func (a f32x8) truncInt() (r i32x8) {
	for i := range r {
		r[i] = cvttps(a[i])
	}
	return
}

// recipFast is _mm_rcp_ps in Rust, a hardware approximation (about 12 bits) whose result differs
// between CPUs; Go cannot reproduce it, so this is the exact 1/x.
func (a f32x8) recipFast() (r f32x8) {
	for i := range r {
		r[i] = 1.0 / a[i]
	}
	return
}

func (a f32x8) sqrt() (r f32x8) {
	for i := range r {
		r[i] = sqrt32(a[i])
	}
	return
}

// powf is Skia's approximate pow for non-negative lanes; 0 and 1 are returned unchanged.
func (a f32x8) powf(exp float32) (r f32x8) {
	const infBits = float32(0x7f800000) // f32::INFINITY.to_bits() as f32
	for i := range r {
		x := a[i]
		bits := math.Float32bits(x)
		e := float32(float32(int32(bits)) * float32(1.0/(1<<23)))
		m := math.Float32frombits(bits&0x007fffff | 0x3f000000)

		log2X := e - 124.225514990 - float32(1.498030302*m) - 1.725879990/(0.3520887068+m)

		v := float32(log2X * exp)
		f := v - floorLane(v)

		p := v + 121.274057500
		p = p - float32(f*1.490129070)
		p += 27.728023300 / (4.84252568 - f)
		p = float32(p * float32(1<<23))

		r[i] = math.Float32frombits(uint32(cvtps(sseMin(sseMax(p, 0), infBits))))
		if x == 0 || x == 1 {
			r[i] = x
		}
	}
	return
}

func (a f32x8) add(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = a[i] + b[i]
	}
	return
}

func (a f32x8) sub(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = a[i] - b[i]
	}
	return
}

func (a f32x8) mul(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = float32(a[i] * b[i])
	}
	return
}

func (a f32x8) div(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = a[i] / b[i]
	}
	return
}

func (a f32x8) and(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = math.Float32frombits(math.Float32bits(a[i]) & math.Float32bits(b[i]))
	}
	return
}

func (a f32x8) or(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = math.Float32frombits(math.Float32bits(a[i]) | math.Float32bits(b[i]))
	}
	return
}

func (a f32x8) xor(b f32x8) (r f32x8) {
	for i := range r {
		r[i] = math.Float32frombits(math.Float32bits(a[i]) ^ math.Float32bits(b[i]))
	}
	return
}

// neg is 0 - a, as Rust's Neg, so -(+0) is +0.
func (a f32x8) neg() f32x8 { return f32x8{}.sub(a) }

func (a f32x8) not() (r f32x8) {
	for i := range r {
		r[i] = math.Float32frombits(^math.Float32bits(a[i]))
	}
	return
}

// eq is PartialEq: every lane compares equal (so NaN lanes never do).
func (a f32x8) eq(b f32x8) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func i32x8Splat(n int32) i32x8 { return i32x8{n, n, n, n, n, n, n, n} }

func (a i32x8) blend(t, f i32x8) (r i32x8) {
	for i := range r {
		r[i] = f[i] ^ ((f[i] ^ t[i]) & a[i])
	}
	return
}

func i32x8Mask(c [8]bool) (r i32x8) {
	for i := range r {
		r[i] = int32(laneMask(c[i]))
	}
	return
}

func (a i32x8) cmpEq(b i32x8) i32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] == b[i]
	}
	return i32x8Mask(c)
}

func (a i32x8) cmpGt(b i32x8) i32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] > b[i]
	}
	return i32x8Mask(c)
}

func (a i32x8) cmpLt(b i32x8) i32x8 {
	var c [8]bool
	for i := range c {
		c[i] = a[i] < b[i]
	}
	return i32x8Mask(c)
}

// toF32x8 is _mm_cvtepi32_ps: round to nearest even.
func (a i32x8) toF32x8() (r f32x8) {
	for i := range r {
		r[i] = float32(a[i])
	}
	return
}

func (a i32x8) toU32x8Bitcast() (r u32x8) {
	for i := range r {
		r[i] = uint32(a[i])
	}
	return
}

func (a i32x8) toF32x8Bitcast() (r f32x8) {
	for i := range r {
		r[i] = math.Float32frombits(uint32(a[i]))
	}
	return
}

func (a i32x8) add(b i32x8) (r i32x8) {
	for i := range r {
		r[i] = a[i] + b[i]
	}
	return
}

func (a i32x8) mul(b i32x8) (r i32x8) {
	for i := range r {
		r[i] = a[i] * b[i]
	}
	return
}

func (a i32x8) and(b i32x8) (r i32x8) {
	for i := range r {
		r[i] = a[i] & b[i]
	}
	return
}

func (a i32x8) or(b i32x8) (r i32x8) {
	for i := range r {
		r[i] = a[i] | b[i]
	}
	return
}

func (a i32x8) xor(b i32x8) (r i32x8) {
	for i := range r {
		r[i] = a[i] ^ b[i]
	}
	return
}

func u32x8Splat(n uint32) u32x8 { return u32x8{n, n, n, n, n, n, n, n} }

func (a u32x8) toI32x8Bitcast() (r i32x8) {
	for i := range r {
		r[i] = int32(a[i])
	}
	return
}

func (a u32x8) toF32x8Bitcast() (r f32x8) {
	for i := range r {
		r[i] = math.Float32frombits(a[i])
	}
	return
}

func (a u32x8) cmpEq(b u32x8) (r u32x8) {
	for i := range r {
		r[i] = laneMask(a[i] == b[i])
	}
	return
}

func (a u32x8) shl(n uint) (r u32x8) {
	for i := range r {
		r[i] = a[i] << n
	}
	return
}

func (a u32x8) shr(n uint) (r u32x8) {
	for i := range r {
		r[i] = a[i] >> n
	}
	return
}

func (a u32x8) not() (r u32x8) {
	for i := range r {
		r[i] = ^a[i]
	}
	return
}

func (a u32x8) add(b u32x8) (r u32x8) {
	for i := range r {
		r[i] = a[i] + b[i]
	}
	return
}

func (a u32x8) and(b u32x8) (r u32x8) {
	for i := range r {
		r[i] = a[i] & b[i]
	}
	return
}

func (a u32x8) or(b u32x8) (r u32x8) {
	for i := range r {
		r[i] = a[i] | b[i]
	}
	return
}
