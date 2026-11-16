// Float helpers for the svgtypes 0.16.1 port: Rust casts and the libm functions svgtypes and kurbo call.
//
// The sin, cos, tan and atan2 Rust takes from the platform libm come from the libm package.

package usvg

import (
	"math"

	"github.com/naddrzz/nalarupa/core-go/libm"
)

// stPi is consts::PI as a variable, so expressions using it are evaluated in f64 as Rust does, not as
// exact Go constant arithmetic.
var stPi = math.Pi

// stToRadians is f64::to_radians: x * (PI / 180.0).
func stToRadians(x float64) float64 { return x * (stPi / 180) }

// stToDegrees is f64::to_degrees: x * 57.2957795130823208767981548141051703.
func stToDegrees(x float64) float64 { return x * 57.2957795130823208767981548141051703 }

// stRoundF64ToU8 is `x.round() as u8`: round half away from zero, then a saturating cast (NaN gives 0).
func stRoundF64ToU8(x float64) uint8 {
	r := math.Round(x)
	if !(r > 0) {
		return 0
	}
	if r >= 255 {
		return 255
	}
	return uint8(r)
}

// stRoundF32ToU8 is `x.round() as u8` for an f32.
func stRoundF32ToU8(x float32) uint8 { return stRoundF64ToU8(float64(x)) }

// stF64ToUsize is `x as usize` (64-bit): saturating, NaN gives 0.
func stF64ToUsize(x float64) uint64 {
	if !(x > 0) {
		return 0
	}
	if x >= 18446744073709551616.0 {
		return math.MaxUint64
	}
	return uint64(x)
}

// stSignum is f64::signum: 1 or -1 by the sign bit, NaN for NaN.
func stSignum(x float64) float64 {
	if x != x {
		return math.NaN()
	}
	return math.Copysign(1, x)
}

// stSin, stCos, stTan and stAtan2 are f64::sin, cos, tan and atan2 (see libm).
func stSin(x float64) float64 { return libm.Sin(x) }

func stCos(x float64) float64 { return libm.Cos(x) }

func stTan(x float64) float64 { return libm.Tan(x) }

func stAtan2(y, x float64) float64 { return libm.Atan2(y, x) }

// stSinCos is f64::sin_cos.
func stSinCos(x float64) (float64, float64) { return stSin(x), stCos(x) }
