package skia

import "math"

// Double precision cubic intersections (path64/), from SkPathOps; used by the edge clipper to chop
// monotonic cubics at an X or Y value. Every product is converted explicitly, as in float32 code, so it is
// never fused into a multiply-add.
//
// The cubic solver calls f64 acos and cos, which Rust takes from the platform libm. Go's math.Acos is off
// by several ulps near 1 and math.Cos by about one, enough to change clipping results, so p64Acos and
// p64Cos round double-double results once. That matches libm results that are correctly rounded (most of
// them); a libm result off by over half an ulp (seen with the Windows CRT) can still differ.

const (
	p64DblEpsilon        = 2.220446049250313e-16 // f64::EPSILON
	p64DblEpsilonErr     = p64DblEpsilon * 4
	p64FltEpsilonHalf    = float64(f32Epsilon / 2)
	p64FltEpsilonCubed   = 1.0 / (1 << 69) // f32::EPSILON cubed in f32, exact
	p64FltEpsilonInverse = 1.0 / float64(f32Epsilon)
	p64PI                = 3.141592653589793
)

// p64Min and p64Max are f64::min and max: a NaN operand gives the other operand.
func p64Min(a, b float64) float64 {
	if a != a || b < a {
		return b
	}
	return a
}

func p64Max(a, b float64) float64 {
	if a != a || b > a {
		return b
	}
	return a
}

// p64Bound works like SkTPin, returning hi for NaN.
func p64Bound(x, lo, hi float64) float64 { return p64Max(p64Min(hi, x), lo) }

// p64Between reports whether (a <= x <= b) || (a >= x >= b).
func p64Between(x, a, b float64) bool { return float64((a-x)*(b-x)) <= 0 }

func p64PreciselyZero(x float64) bool { return math.Abs(x) < p64DblEpsilonErr }

func p64ApproximatelyZeroOrMore(x float64) bool { return x > -p64DblEpsilon }

func p64ApproximatelyOneOrLess(x float64) bool { return x < 1+p64DblEpsilon }

func p64ApproximatelyZero(x float64) bool { return math.Abs(x) < p64DblEpsilon }

func p64ApproximatelyZeroInverse(x float64) bool { return math.Abs(x) > p64FltEpsilonInverse }

func p64ApproximatelyZeroCubed(x float64) bool { return math.Abs(x) < p64FltEpsilonCubed }

func p64ApproximatelyZeroHalf(x float64) bool { return x < p64FltEpsilonHalf }

func p64ApproximatelyZeroWhenComparedTo(x, other float64) bool {
	return x == 0 || math.Abs(x) < math.Abs(float64(other*float64(f32Epsilon)))
}

// p64ApproximatelyEqual compares t values in 0..1; use p64AlmostDequalUlps for general numbers.
func p64ApproximatelyEqual(x, other float64) bool { return p64ApproximatelyZero(x - other) }

func p64ApproximatelyEqualHalf(x, other float64) bool { return p64ApproximatelyZeroHalf(x - other) }

func p64AlmostDequalUlps(x, other float64) bool {
	if math.Abs(x) < float64(ScalarMax) && math.Abs(other) < float64(ScalarMax) {
		return almostDequalUlps(float32(x), float32(other))
	}
	return math.Abs(x-other)/p64Max(math.Abs(x), math.Abs(other)) < float64(f32Epsilon*16)
}

func p64CubeRoot(x float64) float64 {
	if p64ApproximatelyZeroCubed(x) {
		return 0
	}
	result := p64HalleyCbrt3d(math.Abs(x))
	if x < 0 {
		return -result
	}
	return result
}

// p64HalleyCbrt3d approximates a cube root with 3 iterations of Halley's method.
func p64HalleyCbrt3d(d float64) float64 {
	a := p64Cbrt5d(d)
	a = p64CbrtaHalleyd(a, d)
	a = p64CbrtaHalleyd(a, d)
	return p64CbrtaHalleyd(a, d)
}

// p64Cbrt5d is a bit hack cube root approximation adapted from Kahan's cbrt: the high word of d divided
// by 3 plus a bias, with a zero low word.
func p64Cbrt5d(d float64) float64 {
	const b1 = 715094163
	hi := uint32(math.Float64bits(d) >> 32)
	return math.Float64frombits(uint64(hi/3+b1) << 32)
}

func p64CbrtaHalleyd(a, r float64) float64 {
	a3 := float64(float64(a*a) * a)
	return float64(a*(a3+r+r)) / (a3 + a3 + r)
}

func p64Interp(a, b, t float64) float64 { return a + float64((b-a)*t) }

type point64 struct{ x, y float64 }

func point64FromPoint(p Point) point64 { return point64{float64(p.X), float64(p.Y)} }

func (p point64) toPoint() Point { return Point{float32(p.x), float32(p.y)} }

// axisCoord returns x for the X search axis, else y.
func (p point64) axisCoord(axisX bool) float64 {
	if axisX {
		return p.x
	}
	return p.y
}

type cubic64 struct{ points [4]point64 }

func (c *cubic64) asF64Slice() [8]float64 {
	return [8]float64{
		c.points[0].x, c.points[0].y,
		c.points[1].x, c.points[1].y,
		c.points[2].x, c.points[2].y,
		c.points[3].x, c.points[3].y,
	}
}

func (c *cubic64) pointAtT(t float64) point64 {
	if t == 0 {
		return c.points[0]
	}
	if t == 1 {
		return c.points[3]
	}
	oneT := 1 - t
	oneT2 := float64(oneT * oneT)
	a := float64(oneT2 * oneT)
	b := float64(float64(3*oneT2) * t)
	t2 := float64(t * t)
	c3 := float64(float64(3*oneT) * t2)
	d := float64(t2 * t)
	p := &c.points
	return point64{
		float64(a*p[0].x) + float64(b*p[1].x) + float64(c3*p[2].x) + float64(d*p[3].x),
		float64(a*p[0].y) + float64(b*p[1].y) + float64(c3*p[2].y) + float64(d*p[3].y),
	}
}

func (c *cubic64) searchRoots(extrema int, axisIntercept float64, axisX bool, extremeTs *[6]float64, validRoots []float64) int {
	extrema += c.findInflections(extremeTs[extrema:])
	extremeTs[extrema] = 0
	extrema++
	extremeTs[extrema] = 1
	// Rust sorts extremeTs[0..extrema] (without the trailing 1) with a stable sort; for these few
	// elements that is an insertion sort, reproduced here (NaN compares equal to everything).
	p64InsertionSort(extremeTs[:extrema])
	validCount := 0
	index := 0
	for index < extrema {
		lo := extremeTs[index]
		index++
		hi := extremeTs[index]
		if lo == hi {
			continue
		}

		newT := c.binarySearch(lo, hi, axisIntercept, axisX)
		if newT >= 0 {
			if validCount >= 3 {
				return 0
			}
			validRoots[validCount] = newT
			validCount++
		}
	}
	return validCount
}

// p64InsertionSort sorts in increasing order, stably, moving each element left past larger ones.
func p64InsertionSort(v []float64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func (c *cubic64) findInflections(tValues []float64) int {
	p := &c.points
	ax := p[1].x - p[0].x
	ay := p[1].y - p[0].y
	bx := p[2].x - float64(2*p[1].x) + p[0].x
	by := p[2].y - float64(2*p[1].y) + p[0].y
	cx := p[3].x + float64(3*(p[1].x-p[2].x)) - p[0].x
	cy := p[3].y + float64(3*(p[1].y-p[2].y)) - p[0].y
	return quad64RootsValidT(
		float64(bx*cy)-float64(by*cx),
		float64(ax*cy)-float64(ay*cx),
		float64(ax*by)-float64(ay*bx),
		tValues,
	)
}

// binarySearch gives up when changing t no longer moves the point; it returns -1 for no intercept.
func (c *cubic64) binarySearch(lo, hi, axisIntercept float64, axisX bool) float64 {
	t := (lo + hi) / 2
	step := (t - lo) / 2
	cubicAtT := c.pointAtT(t)
	calcPos := cubicAtT.axisCoord(axisX)
	calcDist := calcPos - axisIntercept
	for {
		priorT := p64Max(lo, t-step)
		lessPt := c.pointAtT(priorT)
		if p64ApproximatelyEqualHalf(lessPt.x, cubicAtT.x) && p64ApproximatelyEqualHalf(lessPt.y, cubicAtT.y) {
			return -1
		}

		lessDist := lessPt.axisCoord(axisX) - axisIntercept
		lastStep := step
		step /= 2
		var ok bool
		if calcDist > 0 {
			ok = calcDist > lessDist
		} else {
			ok = calcDist < lessDist
		}
		if ok {
			t = priorT
		} else {
			nextT := t + lastStep
			if nextT > hi {
				return -1
			}

			morePt := c.pointAtT(nextT)
			if p64ApproximatelyEqualHalf(morePt.x, cubicAtT.x) && p64ApproximatelyEqualHalf(morePt.y, cubicAtT.y) {
				return -1
			}

			moreDist := morePt.axisCoord(axisX) - axisIntercept
			if calcDist > 0 {
				ok = calcDist <= moreDist
			} else {
				ok = calcDist >= moreDist
			}
			if ok {
				continue
			}

			t = nextT
		}

		cubicAtT = c.pointAtT(t)
		calcPos = cubicAtT.axisCoord(axisX)
		calcDist = calcPos - axisIntercept

		if p64ApproximatelyEqual(calcPos, axisIntercept) {
			break
		}
	}
	return t
}

func (c *cubic64) chopAt(t float64) [7]point64 {
	var dst [7]point64
	p := &c.points
	if t == 0.5 {
		dst[0] = p[0]
		dst[1].x = (p[0].x + p[1].x) / 2
		dst[1].y = (p[0].y + p[1].y) / 2
		dst[2].x = (p[0].x + float64(2*p[1].x) + p[2].x) / 4
		dst[2].y = (p[0].y + float64(2*p[1].y) + p[2].y) / 4
		dst[3].x = (p[0].x + float64(3*(p[1].x+p[2].x)) + p[3].x) / 8
		dst[3].y = (p[0].y + float64(3*(p[1].y+p[2].y)) + p[3].y) / 8
		dst[4].x = (p[1].x + float64(2*p[2].x) + p[3].x) / 4
		dst[4].y = (p[1].y + float64(2*p[2].y) + p[3].y) / 4
		dst[5].x = (p[2].x + p[3].x) / 2
		dst[5].y = (p[2].y + p[3].y) / 2
		dst[6] = p[3]
		return dst
	}

	// classic one t subdivision, per coordinate
	ab := p64Interp(p[0].x, p[1].x, t)
	bc := p64Interp(p[1].x, p[2].x, t)
	cd := p64Interp(p[2].x, p[3].x, t)
	abc := p64Interp(ab, bc, t)
	bcd := p64Interp(bc, cd, t)
	dst[0].x = p[0].x
	dst[1].x = ab
	dst[2].x = abc
	dst[3].x = p64Interp(abc, bcd, t)
	dst[4].x = bcd
	dst[5].x = cd
	dst[6].x = p[3].x

	ab = p64Interp(p[0].y, p[1].y, t)
	bc = p64Interp(p[1].y, p[2].y, t)
	cd = p64Interp(p[2].y, p[3].y, t)
	abc = p64Interp(ab, bc, t)
	bcd = p64Interp(bc, cd, t)
	dst[0].y = p[0].y
	dst[1].y = ab
	dst[2].y = abc
	dst[3].y = p64Interp(abc, bcd, t)
	dst[4].y = bcd
	dst[5].y = cd
	dst[6].y = p[3].y
	return dst
}

// cubic64Coefficients returns the polynomial coefficients of one coordinate (src[0], src[2], src[4],
// src[6]).
func cubic64Coefficients(src []float64) (a, b, c, d float64) {
	a = src[6]                       // d
	b = float64(src[4] * 3)          // 3*c
	c = float64(src[2] * 3)          // 3*b
	d = src[0]                       // a
	a -= d - c + b                   // A =   -a + 3*b - 3*c + d
	b += float64(3*d) - float64(2*c) // B =  3*a - 6*b + 3*c
	c -= float64(3 * d)              // C = -3*a + 3*b
	return a, b, c, d
}

// cubic64RootsValidT solves the cubic for t in 0..1 (from SkGeometry.cpp and Numeric Solutions, 5.6).
func cubic64RootsValidT(a, b, c, d float64, t *[3]float64) int {
	var s [3]float64
	realRoots := cubic64RootsReal(a, b, c, d, &s)
	foundRoots := quad64PushValidTs(s[:], realRoots, t[:])
outer:
	for index := range realRoots {
		tValue := s[index]
		if !p64ApproximatelyOneOrLess(tValue) && p64Between(tValue, 1, 1.00005) {
			for idx2 := range foundRoots {
				if p64ApproximatelyEqual(t[idx2], 1) {
					continue outer
				}
			}
			t[foundRoots] = 1
			foundRoots++
		} else if !p64ApproximatelyZeroOrMore(tValue) && p64Between(tValue, -0.00005, 0) {
			for idx2 := range foundRoots {
				if p64ApproximatelyEqual(t[idx2], 0) {
					continue outer
				}
			}
			t[foundRoots] = 0
			foundRoots++
		}
	}
	return foundRoots
}

func cubic64RootsReal(a, b, c, d float64, s *[3]float64) int {
	if p64ApproximatelyZero(a) && p64ApproximatelyZeroWhenComparedTo(a, b) &&
		p64ApproximatelyZeroWhenComparedTo(a, c) && p64ApproximatelyZeroWhenComparedTo(a, d) {
		// just a quadratic
		return quad64RootsReal(b, c, d, s)
	}

	if p64ApproximatelyZeroWhenComparedTo(d, a) && p64ApproximatelyZeroWhenComparedTo(d, b) &&
		p64ApproximatelyZeroWhenComparedTo(d, c) {
		// 0 is one root
		num := quad64RootsReal(a, b, c, s)
		for i := range num {
			if p64ApproximatelyZero(s[i]) {
				return num
			}
		}
		s[num] = 0
		return num + 1
	}

	if p64ApproximatelyZero(a + b + c + d) {
		// 1 is one root
		num := quad64RootsReal(a, a+b, -d, s)
		for i := range num {
			if p64AlmostDequalUlps(s[i], 1) {
				return num
			}
		}
		s[num] = 1
		return num + 1
	}

	invA := 1 / a
	a, b, c = float64(b*invA), float64(c*invA), float64(d*invA)

	a2 := float64(a * a)
	q := (a2 - float64(b*3)) / 9
	r := (float64(float64(2*a2)*a) - float64(float64(9*a)*b) + float64(27*c)) / 54
	r2 := float64(r * r)
	q3 := float64(float64(q*q) * q)
	r2MinusQ3 := r2 - q3
	adiv3 := a / 3
	offset := 0
	if r2MinusQ3 < 0 {
		// 3 real roots; the divide/root can, due to finite precision, be slightly outside -1..1
		theta := p64Acos(p64Bound(r/math.Sqrt(q3), -1, 1))
		neg2RootQ := float64(-2 * math.Sqrt(q))

		rr := float64(neg2RootQ*p64Cos(theta/3)) - adiv3
		s[offset] = rr
		offset++

		rr = float64(neg2RootQ*p64Cos((theta+2*p64PI)/3)) - adiv3
		if !p64AlmostDequalUlps(s[0], rr) {
			s[offset] = rr
			offset++
		}

		rr = float64(neg2RootQ*p64Cos((theta-2*p64PI)/3)) - adiv3
		if !p64AlmostDequalUlps(s[0], rr) && (offset == 1 || !p64AlmostDequalUlps(s[1], rr)) {
			s[offset] = rr
			offset++
		}
	} else {
		// 1 real root
		sqrtR2MinusQ3 := math.Sqrt(r2MinusQ3)
		a := math.Abs(r) + sqrtR2MinusQ3
		a = p64CubeRoot(a)
		if r > 0 {
			a = -a
		}
		if a != 0 {
			a += q / a
		}

		r2 := a - adiv3
		s[offset] = r2
		offset++
		if p64AlmostDequalUlps(r2, q3) {
			r2 = -a/2 - adiv3
			if !p64AlmostDequalUlps(s[0], r2) {
				s[offset] = r2
				offset++
			}
		}
	}
	return offset
}

// cubic64FindExtrema solves Cubic64'(t) = 0 (divided by 3) for 0..1, for one coordinate (src[0], src[2],
// src[4], src[6]).
func cubic64FindExtrema(src []float64, tValues []float64) int {
	a := src[0]
	b := src[2]
	c := src[4]
	d := src[6]
	a2 := d - a + float64(3*(b-c))
	b2 := float64(2 * (a - b - b + c))
	c2 := b - a
	return quad64RootsValidT(a2, b2, c2, tValues)
}

func quad64PushValidTs(s []float64, realRoots int, t []float64) int {
	foundRoots := 0
outer:
	for index := range realRoots {
		tValue := s[index]
		if p64ApproximatelyZeroOrMore(tValue) && p64ApproximatelyOneOrLess(tValue) {
			tValue = p64Bound(tValue, 0, 1)
			for idx2 := range foundRoots {
				if p64ApproximatelyEqual(t[idx2], tValue) {
					continue outer
				}
			}
			t[foundRoots] = tValue
			foundRoots++
		}
	}
	return foundRoots
}

// quad64RootsValidT returns the roots in 0..1; callers expect several results sorted smaller first.
func quad64RootsValidT(a, b, c float64, t []float64) int {
	var s [3]float64
	realRoots := quad64RootsReal(a, b, c, &s)
	return quad64PushValidTs(s[:], realRoots, t)
}

// quad64RootsReal solves the quadratic with Q = -1/2(B + sgn(B)Sqrt(B^2 - 4 A C)), t1 = Q / A,
// t2 = C / Q (Numeric Solutions, 5.6). It does not discard real roots <= 0 or >= 1.
func quad64RootsReal(a, b, c float64, s *[3]float64) int {
	if a == 0 {
		return quad64HandleZero(b, c, s)
	}

	p := b / float64(2*a)
	q := c / a
	if p64ApproximatelyZero(a) && (p64ApproximatelyZeroInverse(p) || p64ApproximatelyZeroInverse(q)) {
		return quad64HandleZero(b, c, s)
	}

	// normal form: x^2 + px + q = 0
	p2 := float64(p * p)
	if !p64AlmostDequalUlps(p2, q) && p2 < q {
		return 0
	}

	sqrtD := 0.0
	if p2 > q {
		sqrtD = math.Sqrt(p2 - q)
	}

	s[0] = sqrtD - p
	s[1] = -sqrtD - p
	if p64AlmostDequalUlps(s[0], s[1]) {
		return 1
	}
	return 2
}

func quad64HandleZero(b, c float64, s *[3]float64) int {
	if p64ApproximatelyZero(b) {
		s[0] = 0
		if c == 0 {
			return 1
		}
		return 0
	}
	s[0] = -c / b
	return 1
}

// cubic64HorizontalIntersect finds the t values where the cubic crosses y == axisIntercept.
func cubic64HorizontalIntersect(cubic *cubic64, axisIntercept float64, roots *[3]float64) int {
	f := cubic.asF64Slice()
	a, b, c, d := cubic64Coefficients(f[1:])
	d -= axisIntercept
	count := cubic64RootsValidT(a, b, c, d, roots)
	for index := 0; index < count; index++ {
		calcPt := cubic.pointAtT(roots[index])
		if !p64ApproximatelyEqual(calcPt.y, axisIntercept) {
			var extremeTs [6]float64
			extrema := cubic64FindExtrema(f[1:], extremeTs[:])
			count = cubic.searchRoots(extrema, axisIntercept, false, &extremeTs, roots[:])
			break
		}
	}
	return count
}

// cubic64VerticalIntersect finds the t values where the cubic crosses x == axisIntercept.
func cubic64VerticalIntersect(cubic *cubic64, axisIntercept float64, roots *[3]float64) int {
	f := cubic.asF64Slice()
	a, b, c, d := cubic64Coefficients(f[:])
	d -= axisIntercept
	count := cubic64RootsValidT(a, b, c, d, roots)
	for index := 0; index < count; index++ {
		calcPt := cubic.pointAtT(roots[index])
		if !p64ApproximatelyEqual(calcPt.x, axisIntercept) {
			var extremeTs [6]float64
			extrema := cubic64FindExtrema(f[:], extremeTs[:])
			count = cubic.searchRoots(extrema, axisIntercept, true, &extremeTs, roots[:])
			break
		}
	}
	return count
}

// p64DD is a double-double number hi + lo, |lo| <= ulp(hi)/2, for p64Cos and p64Acos.
type p64DD struct{ hi, lo float64 }

// p64TwoSum returns a + b exactly as a normalized double-double.
func p64TwoSum(a, b float64) p64DD {
	s := a + b
	bb := s - a
	return p64DD{s, (a - (s - bb)) + (b - bb)}
}

func (x p64DD) add(y p64DD) p64DD {
	s := p64TwoSum(x.hi, y.hi)
	t := p64TwoSum(x.lo, y.lo)
	s = p64TwoSum(s.hi, s.lo+t.hi)
	return p64TwoSum(s.hi, s.lo+t.lo)
}

func (x p64DD) neg() p64DD { return p64DD{-x.hi, -x.lo} }

func (x p64DD) mul(y p64DD) p64DD {
	p := float64(x.hi * y.hi)
	e := math.FMA(x.hi, y.hi, -p) // the exact rounding error of the product
	e += float64(x.hi*y.lo) + float64(x.lo*y.hi)
	return p64TwoSum(p, e)
}

func (x p64DD) divF(d float64) p64DD {
	q := x.hi / d
	r := x.add(p64DD{q, 0}.mul(p64DD{d, 0}).neg())
	return p64TwoSum(q, (r.hi+r.lo)/d)
}

func (x p64DD) div(y p64DD) p64DD {
	q := x.hi / y.hi
	r := x.add(p64DD{q, 0}.mul(y).neg())
	return p64TwoSum(q, (r.hi+r.lo)/y.hi)
}

// p64PiDD is pi as a double-double.
var p64PiDD = p64DD{3.141592653589793, 1.2246467991473532e-16}

// p64CosSinDD returns cos(x) and sin(x) for |x| <= pi, by Taylor series after reducing to |x| <= pi/2.
func p64CosSinDD(x p64DD) (p64DD, p64DD) {
	negCos, negSin := false, false
	if x.hi < 0 {
		x = x.neg()
		negSin = true
	}
	if x.hi > math.Pi/2 {
		// cos(x) = -cos(pi - x), sin(x) = sin(pi - x)
		x = p64PiDD.add(x.neg())
		negCos = true
	}
	x2 := x.mul(x)
	c, s := p64DD{1, 0}, x
	ct, st := p64DD{1, 0}, x
	for k := 1; k <= 30; k++ {
		ct = ct.mul(x2).divF(float64((2*k - 1) * (2 * k))).neg()
		st = st.mul(x2).divF(float64((2 * k) * (2*k + 1))).neg()
		c = c.add(ct)
		s = s.add(st)
		if math.Abs(ct.hi) < 1e-34 && math.Abs(st.hi) < 1e-34 {
			break
		}
	}
	if negCos {
		c = c.neg()
	}
	if negSin {
		s = s.neg()
	}
	return c, s
}

// p64Cos is f64::cos, correctly rounded except in vanishingly rare cases, for |x| <= pi (the
// cubic solver's range); other values fall back to math.Cos.
func p64Cos(x float64) float64 {
	if !(math.Abs(x) <= math.Pi) {
		return math.Cos(x)
	}
	c, _ := p64CosSinDD(p64DD{x, 0})
	return c.hi + c.lo
}

// p64Acos is f64::acos, correctly rounded except in vanishingly rare cases: math.Acos refined by
// Newton steps in double-double.
func p64Acos(x float64) float64 {
	if !(x > -1 && x < 1) {
		return math.Acos(x) // NaN, 0 for 1, pi for -1 (both correctly rounded)
	}
	t := p64DD{math.Acos(x), 0}
	for range 2 {
		c, s := p64CosSinDD(t)
		t = t.add(c.add(p64DD{-x, 0}).div(s))
	}
	return t.hi + t.lo
}
