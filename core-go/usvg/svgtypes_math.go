// Float helpers for the svgtypes 0.16.1 port: Rust casts and the libm functions svgtypes and kurbo call.
//
// Rust takes sin, cos, tan and atan2 from the platform libm, whose results are almost always correctly
// rounded; Go's math versions are off by an ulp often enough to change parsed transforms (tan(45deg) gives
// 1, libm gives 0.9999999999999999). stSin, stCos, stTan and stAtan2 compute in double-double and round
// once, which matches a correctly rounded libm except in vanishingly rare cases.

package usvg

import "math"

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

// stDD is a double-double number hi + lo, |lo| <= ulp(hi)/2.
type stDD struct{ hi, lo float64 }

// stTwoSum returns a + b exactly as a normalized double-double.
func stTwoSum(a, b float64) stDD {
	s := a + b
	bb := s - a
	return stDD{s, (a - (s - bb)) + (b - bb)}
}

// stTwoProd returns a * b exactly as a double-double.
func stTwoProd(a, b float64) stDD {
	p := float64(a * b)
	return stDD{p, math.FMA(a, b, -p)}
}

func (x stDD) add(y stDD) stDD {
	s := stTwoSum(x.hi, y.hi)
	t := stTwoSum(x.lo, y.lo)
	s = stTwoSum(s.hi, s.lo+t.hi)
	return stTwoSum(s.hi, s.lo+t.lo)
}

func (x stDD) neg() stDD { return stDD{-x.hi, -x.lo} }

func (x stDD) mul(y stDD) stDD {
	p := float64(x.hi * y.hi)
	e := math.FMA(x.hi, y.hi, -p) // the exact rounding error of the product
	e += float64(x.hi*y.lo) + float64(x.lo*y.hi)
	return stTwoSum(p, e)
}

func (x stDD) div(y stDD) stDD {
	q := x.hi / y.hi
	r := x.add(stDD{q, 0}.mul(y).neg())
	return stTwoSum(q, (r.hi+r.lo)/y.hi)
}

func (x stDD) f64() float64 { return x.hi + x.lo }

// pi/2 as a triple-double.
const (
	stPio2a = 1.5707963267948966
	stPio2b = 6.123233995736766e-17
	stPio2c = -1.4973849048591698e-33
)

// stReduceDD returns r and q with x = q*pi/2 + r, |r| <= ~pi/4, for |x.hi| <= 2^20. The triple-double
// pi/2 keeps r accurate to far more than f64 precision even when x is close to a multiple of pi/2.
func stReduceDD(x stDD) (stDD, int) {
	q := math.RoundToEven(x.hi / stPio2a)
	r := x.add(stTwoProd(q, stPio2a).neg())
	r = r.add(stTwoProd(q, stPio2b).neg())
	r = r.add(stDD{-float64(q * stPio2c), 0})
	return r, int(int64(q)) & 3
}

// stCosSinSmall returns cos(r) and sin(r) for |r| <= ~pi/4 by Taylor series in double-double.
func stCosSinSmall(r stDD) (stDD, stDD) {
	r2 := r.mul(r)
	c, s := stDD{1, 0}, r
	ct, st := stDD{1, 0}, r
	for k := 1; k <= 30; k++ {
		ct = ct.mul(r2).div(stDD{float64((2*k - 1) * (2 * k)), 0}).neg()
		st = st.mul(r2).div(stDD{float64((2 * k) * (2*k + 1)), 0}).neg()
		c = c.add(ct)
		s = s.add(st)
		if math.Abs(ct.hi) < 1e-36 && math.Abs(st.hi) < 1e-36 {
			break
		}
	}
	return c, s
}

// stCosSinDD returns cos(x) and sin(x) in double-double; ok is false outside the reduction range or for
// non-finite x.
func stCosSinDD(x stDD) (c, s stDD, ok bool) {
	if !(math.Abs(x.hi) <= 1<<20) {
		return stDD{}, stDD{}, false
	}
	r, q := stReduceDD(x)
	c, s = stCosSinSmall(r)
	switch q {
	case 1:
		c, s = s.neg(), c
	case 2:
		c, s = c.neg(), s.neg()
	case 3:
		c, s = s, c.neg()
	}
	return c, s, true
}

// stSin is f64::sin.
func stSin(x float64) float64 {
	if x == 0 {
		return x // keeps the sign of zero
	}
	_, s, ok := stCosSinDD(stDD{x, 0})
	if !ok {
		return math.Sin(x)
	}
	return s.f64()
}

// stCos is f64::cos.
func stCos(x float64) float64 {
	c, _, ok := stCosSinDD(stDD{x, 0})
	if !ok {
		return math.Cos(x)
	}
	return c.f64()
}

// stSinCos is f64::sin_cos.
func stSinCos(x float64) (float64, float64) { return stSin(x), stCos(x) }

// stTan is f64::tan.
func stTan(x float64) float64 {
	if x == 0 {
		return x
	}
	c, s, ok := stCosSinDD(stDD{x, 0})
	if !ok {
		return math.Tan(x)
	}
	return s.div(c).f64()
}

// stAtan2 is f64::atan2 (y.atan2(x)): math.Atan2 refined by one Newton step in double-double.
func stAtan2(y, x float64) float64 {
	if y == 0 || x == 0 || math.IsInf(x, 0) || math.IsInf(y, 0) || x != x || y != y {
		return math.Atan2(y, x) // exact special cases
	}
	// Scale both by the same power of two (exact) so the products below cannot overflow.
	_, e := math.Frexp(math.Max(math.Abs(x), math.Abs(y)))
	x, y = math.Ldexp(x, -e), math.Ldexp(y, -e)
	t := stDD{math.Atan2(y, x), 0}
	c, s, ok := stCosSinDD(t)
	if !ok {
		return t.hi
	}
	xd, yd := stDD{x, 0}, stDD{y, 0}
	// f(t) = x*sin(t) - y*cos(t), f'(t) = x*cos(t) + y*sin(t).
	f := xd.mul(s).add(yd.mul(c).neg())
	df := xd.mul(c).add(yd.mul(s))
	return t.add(f.div(df).neg()).f64()
}

// Sin, Cos and Atan2 are stSin, stCos and stAtan2 for other ports that must match Rust's libm on any GOAMD64
// (the Figma import).
func Sin(x float64) float64 { return stSin(x) }

func Cos(x float64) float64 { return stCos(x) }

func Atan2(y, x float64) float64 { return stAtan2(y, x) }
