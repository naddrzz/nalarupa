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

// DD is a double-double number Hi + Lo, |Lo| <= ulp(Hi)/2: the arithmetic here and in skia's cubic solver.
type DD struct{ Hi, Lo float64 }

// TwoSum returns a + b exactly as a normalized double-double.
func TwoSum(a, b float64) DD {
	s := a + b
	bb := s - a
	return DD{s, (a - (s - bb)) + (b - bb)}
}

// TwoProd returns a * b exactly as a double-double.
func TwoProd(a, b float64) DD {
	p := float64(a * b)
	return DD{p, math.FMA(a, b, -p)}
}

// Add is x + y.
func (x DD) Add(y DD) DD {
	s := TwoSum(x.Hi, y.Hi)
	t := TwoSum(x.Lo, y.Lo)
	s = TwoSum(s.Hi, s.Lo+t.Hi)
	return TwoSum(s.Hi, s.Lo+t.Lo)
}

// Neg is -x.
func (x DD) Neg() DD { return DD{-x.Hi, -x.Lo} }

// Mul is x * y.
func (x DD) Mul(y DD) DD {
	p := float64(x.Hi * y.Hi)
	e := math.FMA(x.Hi, y.Hi, -p) // the exact rounding error of the product
	e += float64(x.Hi*y.Lo) + float64(x.Lo*y.Hi)
	return TwoSum(p, e)
}

// Div is x / y.
func (x DD) Div(y DD) DD {
	q := x.Hi / y.Hi
	r := x.Add(DD{q, 0}.Mul(y).Neg())
	return TwoSum(q, (r.Hi+r.Lo)/y.Hi)
}

// F64 is x rounded to a float64.
func (x DD) F64() float64 { return x.Hi + x.Lo }

// pi/2 as a triple-double.
const (
	pio2a = 1.5707963267948966
	pio2b = 6.123233995736766e-17
	pio2c = -1.4973849048591698e-33
)

// reduce returns r and q with x = q*pi/2 + r, |r| <= ~pi/4, for |x.Hi| <= 2^20. The triple-double
// pi/2 keeps r accurate to far more than f64 precision even when x is close to a multiple of pi/2.
func reduce(x DD) (DD, int) {
	q := math.RoundToEven(x.Hi / pio2a)
	r := x.Add(TwoProd(q, pio2a).Neg())
	r = r.Add(TwoProd(q, pio2b).Neg())
	r = r.Add(DD{-float64(q * pio2c), 0})
	return r, int(int64(q)) & 3
}

// cosSinSmall returns cos(r) and sin(r) for |r| <= ~pi/4 by Taylor series in double-double.
func cosSinSmall(r DD) (DD, DD) {
	r2 := r.Mul(r)
	c, s := DD{1, 0}, r
	ct, st := DD{1, 0}, r
	for k := 1; k <= 30; k++ {
		ct = ct.Mul(r2).Div(DD{float64((2*k - 1) * (2 * k)), 0}).Neg()
		st = st.Mul(r2).Div(DD{float64((2 * k) * (2*k + 1)), 0}).Neg()
		c = c.Add(ct)
		s = s.Add(st)
		if math.Abs(ct.Hi) < 1e-36 && math.Abs(st.Hi) < 1e-36 {
			break
		}
	}
	return c, s
}

// cosSinDD returns cos(x) and sin(x) in double-double; ok is false outside the reduction range or for
// non-finite x.
func cosSinDD(x DD) (c, s DD, ok bool) {
	if !(math.Abs(x.Hi) <= 1<<20) {
		return DD{}, DD{}, false
	}
	r, q := reduce(x)
	c, s = cosSinSmall(r)
	switch q {
	case 1:
		c, s = s.Neg(), c
	case 2:
		c, s = c.Neg(), s.Neg()
	case 3:
		c, s = s, c.Neg()
	}
	return c, s, true
}

// Sin is f64::sin.
func Sin(x float64) float64 {
	if x == 0 {
		return x // keeps the sign of zero
	}
	_, s, ok := cosSinDD(DD{x, 0})
	if !ok {
		return math.Sin(x)
	}
	return s.F64()
}

// Cos is f64::cos.
func Cos(x float64) float64 {
	c, _, ok := cosSinDD(DD{x, 0})
	if !ok {
		return math.Cos(x)
	}
	return c.F64()
}

// SinCos is (Sin(x), Cos(x)) from one evaluation.
func SinCos(x float64) (sin, cos float64) {
	c, s, ok := cosSinDD(DD{x, 0})
	if !ok {
		return math.Sin(x), math.Cos(x)
	}
	if x == 0 {
		return x, c.F64() // keeps the sign of zero
	}
	return s.F64(), c.F64()
}

// Tan is f64::tan.
func Tan(x float64) float64 {
	if x == 0 {
		return x
	}
	c, s, ok := cosSinDD(DD{x, 0})
	if !ok {
		return math.Tan(x)
	}
	return s.Div(c).F64()
}

// Atan2 is f64::atan2 (y.atan2(x)): math.Atan2 refined by one Newton step in double-double.
func Atan2(y, x float64) float64 {
	if y == 0 || x == 0 || math.IsInf(x, 0) || math.IsInf(y, 0) || x != x || y != y {
		return math.Atan2(y, x) // exact special cases
	}
	// Scale both by the same power of two (exact) so the products below cannot overflow.
	_, e := math.Frexp(math.Max(math.Abs(x), math.Abs(y)))
	x, y = math.Ldexp(x, -e), math.Ldexp(y, -e)
	t := DD{math.Atan2(y, x), 0}
	c, s, ok := cosSinDD(t)
	if !ok {
		return t.Hi
	}
	xd, yd := DD{x, 0}, DD{y, 0}
	// f(t) = x*sin(t) - y*cos(t), f'(t) = x*cos(t) + y*sin(t).
	f := xd.Mul(s).Add(yd.Mul(c).Neg())
	df := xd.Mul(c).Add(yd.Mul(s))
	return t.Add(f.Div(df).Neg()).F64()
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
