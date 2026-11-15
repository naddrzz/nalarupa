package skia

import "math"

// Curve geometry (path_geometry.rs). The f32x2 arithmetic is written per lane on Point, with the same
// operations in the same order. t values are float32: NormalizedF32Exclusive is 0 < t < 1 and
// NormalizedF32 is 0 <= t <= 1.

func splat(v float32) Point { return Point{v, v} }

func times2(v Point) Point { return v.Add(v) }

func interpPt(v0, v1, t Point) Point { return v0.Add(v1.Sub(v0).Mul(t)) }

// quadCoeff evaluates A*t^2 + B*t + C.
type quadCoeff struct{ a, b, c Point }

func quadCoeffFromPoints(p *[3]Point) quadCoeff {
	c, p1, p2 := p[0], p[1], p[2]
	return quadCoeff{a: p2.Sub(times2(p1)).Add(c), b: times2(p1.Sub(c)), c: c}
}

func (q quadCoeff) eval(t Point) Point { return q.a.Mul(t).Add(q.b).Mul(t).Add(q.c) }

type cubicCoeff struct{ a, b, c, d Point }

func cubicCoeffFromPoints(p *[4]Point) cubicCoeff {
	p0, p1, p2, p3 := p[0], p[1], p[2], p[3]
	three := splat(3)
	return cubicCoeff{
		a: p3.Add(three.Mul(p1.Sub(p2))).Sub(p0),
		b: three.Mul(p2.Sub(times2(p1)).Add(p0)),
		c: three.Mul(p1.Sub(p0)),
		d: p0,
	}
}

func (c cubicCoeff) eval(t Point) Point { return c.a.Mul(t).Add(c.b).Mul(t).Add(c.c).Mul(t).Add(c.d) }

// chopQuadAt splits a quad at t (0 < t < 1) into dst[0..3] and dst[2..5].
func chopQuadAt(src []Point, t float32, dst *[5]Point) {
	p0, p1, p2 := src[0], src[1], src[2]
	tt := splat(t)
	p01 := interpPt(p0, p1, tt)
	p12 := interpPt(p1, p2, tt)
	dst[0] = p0
	dst[1] = p01
	dst[2] = interpPt(p01, p12, tt)
	dst[3] = p12
	dst[4] = p2
}

// findUnitQuadRoots solves A*t^2 + B*t + C = 0 for 0 < t < 1, from Numerical Recipes in C:
// Q = -1/2 (B + sign(B) sqrt[B*B - 4*A*C]), x1 = Q / A, x2 = C / Q.
func findUnitQuadRoots(a, b, c float32, roots *[3]float32) int {
	if a == 0 {
		if r, ok := validUnitDivide(-c, b); ok {
			roots[0] = r
			return 1
		}
		return 0
	}
	// Doubles, so R does not overflow.
	dr := float64(float64(b)*float64(b)) - float64(4.0*float64(a)*float64(c))
	if dr < 0 {
		return 0
	}
	r := float32(math.Sqrt(dr))
	if !isFinite(r) {
		return 0
	}
	var q float32
	if b < 0 {
		q = -(b - r) / 2
	} else {
		q = -(b + r) / 2
	}
	n := 0
	if r, ok := validUnitDivide(q, a); ok {
		roots[n] = r
		n++
	}
	if r, ok := validUnitDivide(c, q); ok {
		roots[n] = r
		n++
	}
	if n == 2 {
		if roots[0] > roots[1] {
			roots[0], roots[1] = roots[1], roots[0]
		} else if roots[0] == roots[1] {
			n-- // skip the double root
		}
	}
	return n
}

// chopCubicAt2 splits a cubic at t (0 < t < 1) into dst[0..4] and dst[3..7].
func chopCubicAt2(src *[4]Point, t float32, dst []Point) {
	p0, p1, p2, p3 := src[0], src[1], src[2], src[3]
	tt := splat(t)
	ab := interpPt(p0, p1, tt)
	bc := interpPt(p1, p2, tt)
	cd := interpPt(p2, p3, tt)
	abc := interpPt(ab, bc, tt)
	bcd := interpPt(bc, cd, tt)
	abcd := interpPt(abc, bcd, tt)
	dst[0] = p0
	dst[1] = ab
	dst[2] = abc
	dst[3] = abcd
	dst[4] = bcd
	dst[5] = cd
	dst[6] = p3
}

// findQuadExtrema solves Quad'(t) = 0 (t = -B / A, A = 2(a - 2b + c), B = 2(b - a)) for 0 < t < 1.
func findQuadExtrema(a, b, c float32) (float32, bool) { return validUnitDivide(a-b, a-b-b+c) }

func validUnitDivide(numer, denom float32) (float32, bool) {
	if numer < 0 {
		numer = -numer
		denom = -denom
	}
	if denom == 0 || numer == 0 || numer >= denom {
		return 0, false
	}
	return newNormalizedExclusive(numer / denom)
}

// findQuadMaxCurvature returns t of a quad's maximum curvature, solving Fx' Fx” + Fy' Fy” = 0.
func findQuadMaxCurvature(src *[3]Point) float32 {
	ax := src[1].X - src[0].X
	ay := src[1].Y - src[0].Y
	bx := src[0].X - src[1].X - src[1].X + src[2].X
	by := src[0].Y - src[1].Y - src[1].Y + src[2].Y
	numer := -(float32(ax*bx) + float32(ay*by))
	denom := float32(bx*bx) + float32(by*by)
	if denom < 0 {
		numer = -numer
		denom = -denom
	}
	if numer <= 0 {
		return 0
	}
	if numer >= denom { // also catches denom == 0
		return 1
	}
	return numer / denom
}

func evalQuadAt(src *[3]Point, t float32) Point { return quadCoeffFromPoints(src).eval(splat(t)) }

func evalQuadTangentAt(src *[3]Point, tol float32) Point {
	// The derivative is zero at t = 0 or 1 when the control point equals the end point: use the end points.
	if (tol == 0 && src[0] == src[1]) || (tol == 1 && src[1] == src[2]) {
		return src[2].Sub(src[0])
	}
	p0, p1, p2 := src[0], src[1], src[2]
	b := p1.Sub(p0)
	a := p2.Sub(p1).Sub(b)
	t := a.Mul(splat(tol)).Add(b)
	return t.Add(t)
}

// findCubicMaxCurvature finds t where F' dot F” == 0 (CCt^3 + 3BCt^2 + (2BB + CA)t + AB) and returns
// how many of tValues it set.
func findCubicMaxCurvature(src *[4]Point, tValues *[3]float32) int {
	cx := formulateF1DotF2(src[0].X, src[1].X, src[2].X, src[3].X)
	cy := formulateF1DotF2(src[0].Y, src[1].Y, src[2].Y, src[3].Y)
	for i := range 4 {
		cx[i] += cy[i]
	}
	return solveCubicPoly(&cx, tValues)
}

func formulateF1DotF2(s0, s1, s2, s3 float32) [4]float32 {
	a := s1 - s0
	b := s2 - float32(2*s1) + s0
	c := s3 + float32(3*(s1-s2)) - s0
	return [4]float32{float32(c * c), float32(3 * b * c), float32(2*b*b) + float32(c*a), float32(a * b)}
}

// solveCubicPoly solves coeff[0]t^3 + coeff[1]t^2 + coeff[2]t + coeff[3] == 0 for 0 <= t <= 1, with
// distinct roots in increasing order.
func solveCubicPoly(coeff *[4]float32, tValues *[3]float32) int {
	if isNearlyZero(coeff[0]) { // just a quadratic
		var tmp [3]float32
		n := findUnitQuadRoots(coeff[1], coeff[2], coeff[3], &tmp)
		copy(tValues[:n], tmp[:n])
		return n
	}
	inva := invert(coeff[0])
	a := float32(coeff[1] * inva)
	b := float32(coeff[2] * inva)
	c := float32(coeff[3] * inva)

	q := (float32(a*a) - float32(b*3)) / 9
	r := (float32(2*a*a*a) - float32(9*a*b) + float32(27*c)) / 54
	q3 := float32(q * q * q)
	r2MinusQ3 := float32(r*r) - q3
	adiv3 := a / 3

	if r2MinusQ3 < 0 { // three real roots
		// The divide/root can, due to finite precision, be slightly outside -1..1.
		theta := acos32(bound(r/sqrt32(q3), -1, 1))
		neg2RootQ := float32(-2 * sqrt32(q))
		tValues[0] = normalizedClamped(float32(neg2RootQ*cos32(theta/3)) - adiv3)
		tValues[1] = normalizedClamped(float32(neg2RootQ*cos32((theta+2*floatPI)/3)) - adiv3)
		tValues[2] = normalizedClamped(float32(neg2RootQ*cos32((theta-2*floatPI)/3)) - adiv3)
		sortArray3(tValues)
		return collapseDuplicates3(tValues)
	}
	// One real root.
	a = abs32(r) + sqrt32(r2MinusQ3)
	a = scalarCubeRoot(a)
	if r > 0 {
		a = -a
	}
	if a != 0 {
		a += q / a
	}
	tValues[0] = normalizedClamped(a - adiv3)
	return 1
}

func sortArray3(a *[3]float32) {
	if a[0] > a[1] {
		a[0], a[1] = a[1], a[0]
	}
	if a[1] > a[2] {
		a[1], a[2] = a[2], a[1]
	}
	if a[0] > a[1] {
		a[0], a[1] = a[1], a[0]
	}
}

func collapseDuplicates3(a *[3]float32) int {
	n := 3
	if a[1] == a[2] {
		n = 2
	}
	if a[0] == a[1] {
		n = 1
	}
	return n
}

func scalarCubeRoot(x float32) float32 { return pow32(x, 0.3333333) }

// evalCubicPosAt is the position part of SkEvalCubicAt.
func evalCubicPosAt(src *[4]Point, t float32) Point { return cubicCoeffFromPoints(src).eval(splat(t)) }

// evalCubicTangentAt is the tangent part of SkEvalCubicAt.
func evalCubicTangentAt(src *[4]Point, t float32) Point {
	// The derivative is zero at t = 0 or 1 when the adjacent control point equals the end point: use the
	// next control point or the end points.
	if (t == 0 && src[0] == src[1]) || (t == 1 && src[2] == src[3]) {
		var tangent Point
		if t == 0 {
			tangent = src[2].Sub(src[0])
		} else {
			tangent = src[3].Sub(src[1])
		}
		if tangent.X == 0 && tangent.Y == 0 {
			tangent = src[3].Sub(src[0])
		}
		return tangent
	}
	return evalCubicDerivative(src, t)
}

func evalCubicDerivative(src *[4]Point, t float32) Point {
	p0, p1, p2, p3 := src[0], src[1], src[2], src[3]
	coeff := quadCoeff{
		a: p3.Add(splat(3).Mul(p1.Sub(p2))).Sub(p0),
		b: times2(p2.Sub(times2(p1)).Add(p0)),
		c: p1.Sub(p0),
	}
	return coeff.eval(splat(t))
}

// findCubicExtrema solves Cubic'(t) = 0 (divided by 3) for 0 < t < 1.
func findCubicExtrema(a, b, c, d float32, tValues *[3]float32) int {
	aa := d - a + float32(3*(b-c))
	bb := float32(2 * (a - b - b + c))
	cc := b - a
	return findUnitQuadRoots(aa, bb, cc, tValues)
}

// findCubicInflections solves (BxCy - ByCx)t^2 + (AxCy - AyCx)t + AxBy - AyBx == 0 for 0 < t < 1.
func findCubicInflections(src *[4]Point, tValues *[3]float32) int {
	ax := src[1].X - src[0].X
	ay := src[1].Y - src[0].Y
	bx := src[2].X - float32(2*src[1].X) + src[0].X
	by := src[2].Y - float32(2*src[1].Y) + src[0].Y
	cx := src[3].X + float32(3*(src[1].X-src[2].X)) - src[0].X
	cy := src[3].Y + float32(3*(src[1].Y-src[2].Y)) - src[0].Y
	return findUnitQuadRoots(float32(bx*cy)-float32(by*cx), float32(ax*cy)-float32(ay*cx), float32(ax*by)-float32(ay*bx), tValues)
}

// findCubicCusp returns t (0 < t < 1) of a cubic's cusp, if it has one.
func findCubicCusp(src *[4]Point) (float32, bool) {
	// A control point on its end point behaves like a cusp; such cubics are common, skip them.
	if src[0] == src[1] || src[2] == src[3] {
		return 0, false
	}
	// A cusp needs the lines formed by the control and end points to cross.
	if onSameSide(src, 0, 2) || onSameSide(src, 2, 0) {
		return 0, false
	}
	// Of several points of maximum curvature at most one is a cusp.
	var tValues [3]float32
	n := findCubicMaxCurvature(src, &tValues)
	for _, t := range tValues[:n] {
		if 0 >= t || t >= 1 {
			continue // no need to consider max curvature on the end
		}
		// A cusp is at max curvature with a derivative close to zero, relative to the cubic's size.
		d := evalCubicDerivative(src, t)
		if d.lengthSqd() < calcCubicPrecision(src) {
			return normalizedExclusiveBounded(t), true
		}
	}
	return 0, false
}

// onSameSide reports whether src[test] and src[test+1] are in the same half plane of the line through
// src[line] and src[line+1].
func onSameSide(src *[4]Point, test, line int) bool {
	origin := src[line]
	l := src[line+1].Sub(origin)
	var crosses [2]float32
	for i := range 2 {
		crosses[i] = l.Cross(src[test+i].Sub(origin))
	}
	return float32(crosses[0]*crosses[1]) >= 0
}

// calcCubicPrecision is a constant proportional to the cubic's size, found by experiment.
func calcCubicPrecision(src *[4]Point) float32 {
	return float32((src[1].distanceToSqd(src[0]) + src[2].distanceToSqd(src[1]) + src[3].distanceToSqd(src[2])) * 1e-8)
}

type conic struct {
	points [3]Point
	weight float32
}

func (c *conic) computeQuadPow2(tolerance float32) (uint8, bool) {
	if tolerance < 0 || !isFinite(tolerance) {
		return 0, false
	}
	if !c.points[0].IsFinite() || !c.points[1].IsFinite() || !c.points[2].IsFinite() {
		return 0, false
	}
	// Limit the number of quads that approximate a conic.
	const maxConicToQuadPow2 = 4
	// "High order approximation of conic sections by quadratic splines", Michael Floater, 1993.
	a := c.weight - 1
	k := a / float32(4*(2+a))
	x := float32(k * (c.points[0].X - float32(2*c.points[1].X) + c.points[2].X))
	y := float32(k * (c.points[0].Y - float32(2*c.points[1].Y) + c.points[2].Y))
	err := sqrt32(float32(x*x) + float32(y*y))
	var pow2 uint8
	for range maxConicToQuadPow2 {
		if err <= tolerance {
			break
		}
		err = float32(err * 0.25)
		pow2++
	}
	// Unlike Skia, at least one quad, or the results look bad.
	return max(pow2, 1), true
}

// chopIntoQuadsPow2 chops the conic into 1<<pow2 quads, stored contiguously in points (1 + 2*N points).
func (c *conic) chopIntoQuadsPow2(pow2 uint8, points []Point) uint8 {
	points[0] = c.points[0]
	subdivide(c, points[1:], pow2)
	quadCount := 1 << pow2
	ptCount := 2*quadCount + 1
	for _, p := range points[:ptCount] {
		if !p.IsFinite() {
			// Pin a non-finite result to the middle of the hull; the first and last are already on it.
			for i := 1; i < ptCount-1; i++ {
				points[i] = c.points[1]
			}
			break
		}
	}
	return 1 << pow2
}

func (c *conic) chop() (conic, conic) {
	scale := splat(invert(1 + c.weight))
	newW := subdivideWeightValue(c.weight)
	p0, p1, p2 := c.points[0], c.points[1], c.points[2]
	ww := splat(c.weight)
	wp1 := ww.Mul(p1)
	mPt := p0.Add(times2(wp1)).Add(p2).Mul(scale).Mul(splat(0.5))
	if !mPt.IsFinite() {
		wD := float64(c.weight)
		w2 := float64(wD * 2)
		scaleHalf := float64(1 / (1 + wD) * 0.5)
		mPt.X = float32((float64(c.points[0].X) + float64(w2*float64(c.points[1].X)) + float64(c.points[2].X)) * scaleHalf)
		mPt.Y = float32((float64(c.points[0].Y) + float64(w2*float64(c.points[1].Y)) + float64(c.points[2].Y)) * scaleHalf)
	}
	return conic{points: [3]Point{c.points[0], p0.Add(wp1).Mul(scale), mPt}, weight: newW},
		conic{points: [3]Point{mPt, wp1.Add(p2).Mul(scale), c.points[2]}, weight: newW}
}

// buildUnitArc builds the conics of the unit arc from uStart to uStop, mapped by userTransform, into
// dst and returns how many; 0 is Rust's None.
func buildUnitArc(uStart, uStop Point, dir pathDirection, userTransform Transform, dst *[5]conic) int {
	// Rotate by x, y so that uStart is (1, 0).
	x := uStart.Dot(uStop)
	y := uStart.Cross(uStop)
	absY := abs32(y)
	// Check for (effectively) coincident vectors: an angle of nearly 0 or 180 (y == 0); the dot product
	// tells 0 from 180 (x > 0).
	if absY <= ScalarNearlyZero && x > 0 && ((y >= 0 && dir == dirCW) || (y <= 0 && dir == dirCCW)) {
		return 0
	}
	if dir == dirCCW {
		y = -y
	}
	// One conic per quadrant of a circle: 0 == [0..90), 1 == [90..180), 2 == [180..270), 3 == [270..360).
	quadrant := 0
	switch {
	case y == 0:
		quadrant = 2 // 180
	case x == 0:
		if y > 0 {
			quadrant = 1 // 90
		} else {
			quadrant = 3 // 270
		}
	default:
		if y < 0 {
			quadrant += 2
		}
		if (x < 0) != (y < 0) {
			quadrant++
		}
	}
	quadrantPoints := [8]Point{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}
	const quadrantWeight = ScalarRoot2Over2

	conicCount := quadrant
	for i := range conicCount {
		dst[i] = conic{points: [3]Point{quadrantPoints[i*2], quadrantPoints[i*2+1], quadrantPoints[i*2+2]}, weight: quadrantWeight}
	}
	// Any remaining (sub-90-degree) arc for the last conic.
	finalPt := Point{x, y}
	lastQ := quadrantPoints[quadrant*2] // already a unit vector
	dot := lastQ.Dot(finalPt)
	if dot < 1 {
		offCurve := Point{lastQ.X + x, lastQ.Y + y}
		// The bisector, rescaled to be the off-curve point: length = sqrt(2 / (1 + cos(theta))) from
		// cos(theta/2) = length / 1, and cos(theta/2) is also the weight.
		cosThetaOver2 := sqrt32((1 + dot) / 2)
		offCurve.SetLength(invert(cosThetaOver2))
		if !lastQ.almostEqual(offCurve) {
			dst[conicCount] = conic{points: [3]Point{lastQ, offCurve, finalPt}, weight: cosThetaOver2}
			conicCount++
		}
	}
	// Counter-clockwise and the initial uStart rotation.
	ts := transformFromSinCos(uStart.Y, uStart.X)
	if dir == dirCCW {
		ts = ts.PreScale(1, -1)
	}
	ts = ts.PostConcat(userTransform)
	for i := range conicCount {
		ts.MapPoints(dst[i].points[:])
	}
	return conicCount
}

func subdivideWeightValue(w float32) float32 { return sqrt32(0.5 + float32(w*0.5)) }

func subdivide(src *conic, points []Point, level uint8) []Point {
	if level == 0 {
		points[0] = src.points[1]
		points[1] = src.points[2]
		return points[2:]
	}
	d0, d1 := src.chop()
	startY, endY := src.points[0].Y, src.points[2].Y
	if between(startY, src.points[1].Y, endY) {
		// A monotonic input must give monotonic output, or the scan converter hangs: keep the chopped
		// conics in y order.
		midY := d0.points[2].Y
		if !between(startY, midY, endY) {
			// A midpoint outside the ends moves to the closer one.
			closerY := endY
			if abs32(midY-startY) < abs32(midY-endY) {
				closerY = startY
			}
			d0.points[2].Y = closerY
			d1.points[0].Y = closerY
		}
		if !between(startY, d0.points[1].Y, d0.points[2].Y) {
			// The first control outside its ends goes to the start, which reduces the quad to a line.
			d0.points[1].Y = startY
		}
		if !between(d1.points[0].Y, d1.points[1].Y, endY) {
			// The second control outside its ends goes to the end.
			d1.points[1].Y = endY
		}
	}
	level--
	points = subdivide(&d0, points, level)
	return subdivide(&d1, points, level)
}

// between reports (a <= b <= c) || (a >= b >= c), from SkOpTypes.h.
func between(a, b, c float32) bool { return float32((a-b)*(c-b)) <= 0 }

type autoConicToQuads struct {
	points [64]Point
	len    uint8 // the number of quads
}

func computeAutoConicToQuads(p0, p1, p2 Point, weight float32) (*autoConicToQuads, bool) {
	c := conic{points: [3]Point{p0, p1, p2}, weight: weight}
	pow2, ok := c.computeQuadPow2(0.25)
	if !ok {
		return nil, false
	}
	q := &autoConicToQuads{}
	q.len = c.chopIntoQuadsPow2(pow2, q.points[:])
	return q, true
}
