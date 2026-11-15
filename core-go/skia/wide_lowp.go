package skia

import "math"

// The lane types of the low precision pipeline (wide/u16x16_t.rs, wide/f32x16_t.rs), as scalar lanes.
// u16 arithmetic wraps, as in a Rust release build. f32x16 follows the x86-64 SSE2 code path of the two
// f32x8 (each two f32x4) it is made of in Rust.

type u16x16 [16]uint16

func u16x16Splat(n uint16) u16x16 {
	var r u16x16
	for i := range r {
		r[i] = n
	}
	return r
}

func (a u16x16) add(b u16x16) u16x16 {
	for i := range a {
		a[i] += b[i]
	}
	return a
}

func (a u16x16) sub(b u16x16) u16x16 {
	for i := range a {
		a[i] -= b[i]
	}
	return a
}

func (a u16x16) mul(b u16x16) u16x16 {
	for i := range a {
		a[i] *= b[i]
	}
	return a
}

// shr masks the shift amount to 0..15, as Rust's unchecked u16 shift does in release builds.
func (a u16x16) shr(b u16x16) u16x16 {
	for i := range a {
		a[i] >>= b[i] & 15
	}
	return a
}

func (a u16x16) and(b u16x16) u16x16 {
	for i := range a {
		a[i] &= b[i]
	}
	return a
}

func (a u16x16) or(b u16x16) u16x16 {
	for i := range a {
		a[i] |= b[i]
	}
	return a
}

func (a u16x16) not() u16x16 {
	for i := range a {
		a[i] = ^a[i]
	}
	return a
}

func (a u16x16) min(b u16x16) u16x16 {
	for i := range a {
		a[i] = min(a[i], b[i])
	}
	return a
}

func (a u16x16) max(b u16x16) u16x16 {
	for i := range a {
		a[i] = max(a[i], b[i])
	}
	return a
}

// cmpLe gives all ones in the lanes where a <= b, zero elsewhere.
func (a u16x16) cmpLe(b u16x16) u16x16 {
	var r u16x16
	for i := range a {
		if a[i] <= b[i] {
			r[i] = 0xffff
		}
	}
	return r
}

// blend picks t where the mask a is set and e elsewhere, bitwise.
func (a u16x16) blend(t, e u16x16) u16x16 { return t.and(a).or(e.and(a.not())) }

type f32x16 [16]float32

func f32x16Splat(n float32) f32x16 {
	var r f32x16
	for i := range r {
		r[i] = n
	}
	return r
}

func (a f32x16) add(b f32x16) f32x16 {
	for i := range a {
		a[i] += b[i]
	}
	return a
}

func (a f32x16) sub(b f32x16) f32x16 {
	for i := range a {
		a[i] -= b[i]
	}
	return a
}

func (a f32x16) mul(b f32x16) f32x16 {
	for i := range a {
		a[i] = float32(a[i] * b[i])
	}
	return a
}

// abs clears the sign bit, as Skia does.
func (a f32x16) abs() f32x16 {
	for i := range a {
		a[i] = math.Float32frombits(math.Float32bits(a[i]) & 0x7fffffff)
	}
	return a
}

// normalize clamps to 0..1 with _mm_max_ps then _mm_min_ps: a NaN lane becomes 0.
func (a f32x16) normalize() f32x16 {
	for i, x := range a {
		if !(x > 0) {
			x = 0
		}
		if !(x < 1) {
			x = 1
		}
		a[i] = x
	}
	return a
}

// floor is f32x16::floor: round, then subtract 1 where that rounded up.
func (a f32x16) floor() f32x16 {
	r := a.round()
	for i := range r {
		if r[i] > a[i] {
			r[i] -= 1
		}
	}
	return r
}

func (a f32x16) sqrt() f32x16 {
	for i := range a {
		a[i] = float32(math.Sqrt(float64(a[i])))
	}
	return a
}

// round is f32x4::round without sse4.1 (roundLane, wide.go) on each lane.
func (a f32x16) round() f32x16 {
	for i, v := range a {
		a[i] = roundLane(v)
	}
	return a
}

// saveToU16x16 casts each lane with Rust's saturating `as u16`, without rounding.
func (a f32x16) saveToU16x16(dst *u16x16) {
	for i, x := range a {
		dst[i] = castU16(x)
	}
}
