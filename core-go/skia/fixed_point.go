package skia

// Fixed-point numbers (fixed_point.rs). Skia uses them loosely, so they are plain int32 aliases, as in
// Rust. Rust's `fdot6::round` is fdot6Round here, and so on.

type (
	fDot6  = int32 // 26.6
	fDot8  = int32 // 24.8
	fDot16 = int32 // 16.16
)

const fdot6One fDot6 = 64

func fdot6FromI32(n int32) fDot6 { return n << 6 }

func fdot6FromF32(n float32) fDot6 { return castI32(float64(float32(n * 64))) }

func fdot6Floor(n fDot6) fDot6 { return n >> 6 }

func fdot6Ceil(n fDot6) fDot6 { return (n + 63) >> 6 }

func fdot6Round(n fDot6) fDot6 { return (n + 32) >> 6 }

func fdot6ToFdot16(n fDot6) fDot16 { return leftShift(n, 10) }

func fdot6Div(a, b fDot6) fDot16 {
	if a >= -32768 && a <= 32767 { // i16::try_from(a).is_ok()
		return leftShift(a, 16) / b
	}
	return fdot16Div(a, b)
}

func fdot6CanConvertToFdot16(n fDot6) bool {
	const maxDot6 = int32(0x7fffffff >> (16 - 6))
	if n < 0 {
		n = -n // i32::abs wraps for MIN in release, as Go's negation does
	}
	return n <= maxDot6
}

func fdot6SmallScale(value uint8, dot6 fDot6) uint8 { return uint8((int32(value) * dot6) >> 6) }

func fdot8FromFdot16(x fDot16) fDot8 { return (x + 0x80) >> 8 }

const (
	fdot16Half fDot16 = (1 << 16) / 2
	fdot16One  fDot16 = 1 << 16
)

func fdot16FromF32(x float32) fDot16 { return saturateFromF32(float32(x * float32(fdot16One))) }

func fdot16FloorToI32(x fDot16) int32 { return x >> 16 }

func fdot16CeilToI32(x fDot16) int32 { return (x + fdot16One - 1) >> 16 }

func fdot16RoundToI32(x fDot16) int32 { return (x + fdot16Half) >> 16 }

// fdot16Mul may exceed 32 bits; the result is truncated, as Rust's `as i32`.
func fdot16Mul(a, b fDot16) fDot16 { return fDot16((int64(a) * int64(b)) >> 16) }

// fdot16Div clamps the quotient to a signed 32 bit result.
func fdot16Div(numer, denom fDot6) fDot16 {
	v := leftShift64(int64(numer), 16) / int64(denom)
	return int32(max(min(v, 0x7fffffff), -0x80000000))
}

func fdot16FastDiv(a, b fDot6) fDot16 { return leftShift(a, 16) / b }
