// Package libm is the platform libm functions Rust calls for f64 (sin, cos, tan, atan2), computed so their
// results match Rust's.
//
// Rust takes them from the platform libm, whose results are almost always correctly rounded; Go's math
// versions are off by an ulp for about half of all whole-degree angles (cos(90°) among them), which changes
// rotation matrices and parsed transforms. These compute in double-double and round once, which matches a
// correctly rounded libm. A libm result off by over half an ulp (seen with the Windows CRT, sin(45°)) still
// differs. TotalCmp, Rust's f64 total order, sits here too.
package libm

import "math"

// dd is a double-double number hi + lo, |lo| <= ulp(hi)/2.
type dd struct{ hi, lo float64 }

// twoSum returns a + b exactly as a normalized double-double.
func twoSum(a, b float64) dd {
	s := a + b
	bb := s - a
	return dd{s, (a - (s - bb)) + (b - bb)}
}

// twoProd returns a * b exactly as a double-double.
func twoProd(a, b float64) dd {
	p := float64(a * b)
	return dd{p, math.FMA(a, b, -p)}
}

func (x dd) add(y dd) dd {
	s := twoSum(x.hi, y.hi)
	t := twoSum(x.lo, y.lo)
	s = twoSum(s.hi, s.lo+t.hi)
	return twoSum(s.hi, s.lo+t.lo)
}

func (x dd) neg() dd { return dd{-x.hi, -x.lo} }

func (x dd) mul(y dd) dd {
	p := float64(x.hi * y.hi)
	e := math.FMA(x.hi, y.hi, -p) // the exact rounding error of the product
	e += float64(x.hi*y.lo) + float64(x.lo*y.hi)
	return twoSum(p, e)
}

func (x dd) div(y dd) dd {
	q := x.hi / y.hi
	r := x.add(dd{q, 0}.mul(y).neg())
	return twoSum(q, (r.hi+r.lo)/y.hi)
}

func (x dd) f64() float64 { return x.hi + x.lo }

// pi/2 as a triple-double.
const (
	pio2a = 1.5707963267948966
	pio2b = 6.123233995736766e-17
	pio2c = -1.4973849048591698e-33
)

// reduce returns r and q with x = q*pi/2 + r, |r| <= ~pi/4, for |x.hi| <= 2^20. The triple-double
// pi/2 keeps r accurate to far more than f64 precision even when x is close to a multiple of pi/2.
func reduce(x dd) (dd, int) {
	q := math.RoundToEven(x.hi / pio2a)
	r := x.add(twoProd(q, pio2a).neg())
	r = r.add(twoProd(q, pio2b).neg())
	r = r.add(dd{-float64(q * pio2c), 0})
	return r, int(int64(q)) & 3
}

// cosSinSmall returns cos(r) and sin(r) for |r| <= ~pi/4 by Taylor series in double-double.
func cosSinSmall(r dd) (dd, dd) {
	r2 := r.mul(r)
	c, s := dd{1, 0}, r
	ct, st := dd{1, 0}, r
	for k := 1; k <= 30; k++ {
		ct = ct.mul(r2).div(dd{float64((2*k - 1) * (2 * k)), 0}).neg()
		st = st.mul(r2).div(dd{float64((2 * k) * (2*k + 1)), 0}).neg()
		c = c.add(ct)
		s = s.add(st)
		if math.Abs(ct.hi) < 1e-36 && math.Abs(st.hi) < 1e-36 {
			break
		}
	}
	return c, s
}

// cosSinDD returns cos(x) and sin(x) in double-double; ok is false outside the reduction range or for
// non-finite x.
func cosSinDD(x dd) (c, s dd, ok bool) {
	if !(math.Abs(x.hi) <= 1<<20) {
		return dd{}, dd{}, false
	}
	r, q := reduce(x)
	c, s = cosSinSmall(r)
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

// Sin is f64::sin.
func Sin(x float64) float64 {
	if x == 0 {
		return x // keeps the sign of zero
	}
	_, s, ok := cosSinDD(dd{x, 0})
	if !ok {
		return math.Sin(x)
	}
	return s.f64()
}

// Cos is f64::cos.
func Cos(x float64) float64 {
	c, _, ok := cosSinDD(dd{x, 0})
	if !ok {
		return math.Cos(x)
	}
	return c.f64()
}

// Tan is f64::tan.
func Tan(x float64) float64 {
	if x == 0 {
		return x
	}
	c, s, ok := cosSinDD(dd{x, 0})
	if !ok {
		return math.Tan(x)
	}
	return s.div(c).f64()
}

// Atan2 is f64::atan2 (y.atan2(x)): math.Atan2 refined by one Newton step in double-double.
func Atan2(y, x float64) float64 {
	if y == 0 || x == 0 || math.IsInf(x, 0) || math.IsInf(y, 0) || x != x || y != y {
		return math.Atan2(y, x) // exact special cases
	}
	// Scale both by the same power of two (exact) so the products below cannot overflow.
	_, e := math.Frexp(math.Max(math.Abs(x), math.Abs(y)))
	x, y = math.Ldexp(x, -e), math.Ldexp(y, -e)
	t := dd{math.Atan2(y, x), 0}
	c, s, ok := cosSinDD(t)
	if !ok {
		return t.hi
	}
	xd, yd := dd{x, 0}, dd{y, 0}
	// f(t) = x*sin(t) - y*cos(t), f'(t) = x*cos(t) + y*sin(t).
	f := xd.mul(s).add(yd.mul(c).neg())
	df := xd.mul(c).add(yd.mul(s))
	return t.add(f.div(df).neg()).f64()
}

// TotalCmp is f64::total_cmp: -NaN < -inf < … < -0 < +0 < … < +inf < NaN.
func TotalCmp(a, b float64) int {
	key := func(f float64) int64 {
		x := int64(math.Float64bits(f))
		return x ^ int64(uint64(x>>63)>>1)
	}
	ka, kb := key(a), key(b)
	switch {
	case ka < kb:
		return -1
	case ka > kb:
		return 1
	}
	return 0
}
