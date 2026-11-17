// Port of the kurbo 0.13.1 curve math usvg's text uses: Line/QuadBez/CubicBez evaluation, QuadBez::raise,
// CubicBez::deriv, arclen (cubicbez.rs, adaptive Gauss-Legendre quadrature) and inv_arclen
// (param_curve.rs, with common.rs solve_itp). All math is f64 in Rust's operation order; products feeding
// an add or subtract are wrapped in float64() so Go cannot fuse them.

package usvg

import "math"

// kurboCubic is kurbo::CubicBez.
type kurboCubic struct{ P0, P1, P2, P3 kurboPoint }

// kurboQuad is kurbo::QuadBez.
type kurboQuad struct{ P0, P1, P2 kurboPoint }

// kurboLineEval is Line::eval: p0.lerp(p1, t), Vec2::lerp being self + t * (other - self).
func kurboLineEval(p0, p1 kurboPoint, t float64) kurboPoint {
	return kurboPoint{p0.X + float64(t*(p1.X-p0.X)), p0.Y + float64(t*(p1.Y-p0.Y))}
}

// kurboCubicFromLine is usvg's create_curve_from_line: a cubic through the line at t = 0.33 and 0.66.
func kurboCubicFromLine(px, py, x, y float32) kurboCubic {
	p0 := kurboPoint{float64(px), float64(py)}
	p3 := kurboPoint{float64(x), float64(y)}
	return kurboCubic{p0, kurboLineEval(p0, p3, 0.33), kurboLineEval(p0, p3, 0.66), p3}
}

// raise is QuadBez::raise: the same curve as a cubic.
func (q kurboQuad) raise() kurboCubic {
	var twoThirds = 2.0 / 3.0
	return kurboCubic{
		q.P0,
		kurboPoint{q.P0.X + float64(twoThirds*(q.P1.X-q.P0.X)), q.P0.Y + float64(twoThirds*(q.P1.Y-q.P0.Y))},
		kurboPoint{q.P2.X + float64(twoThirds*(q.P1.X-q.P2.X)), q.P2.Y + float64(twoThirds*(q.P1.Y-q.P2.Y))},
		q.P2,
	}
}

// eval is QuadBez::eval.
func (q kurboQuad) eval(t float64) kurboPoint {
	mt := 1 - t
	f := func(p0, p1, p2 float64) float64 {
		return float64(p0*float64(mt*mt)) + float64((float64(p1*float64(mt*2))+float64(p2*t))*t)
	}
	return kurboPoint{f(q.P0.X, q.P1.X, q.P2.X), f(q.P0.Y, q.P1.Y, q.P2.Y)}
}

// eval is CubicBez::eval.
func (c kurboCubic) eval(t float64) kurboPoint {
	mt := 1 - t
	f := func(p0, p1, p2, p3 float64) float64 {
		a := float64(p0 * float64(float64(mt*mt)*mt))
		inner := float64(p2*float64(mt*3)) + float64(p3*t)
		mid := float64(p1*float64(float64(mt*mt)*3)) + float64(inner*t)
		return a + float64(mid*t)
	}
	return kurboPoint{f(c.P0.X, c.P1.X, c.P2.X, c.P3.X), f(c.P0.Y, c.P1.Y, c.P2.Y, c.P3.Y)}
}

// deriv is CubicBez::deriv.
func (c kurboCubic) deriv() kurboQuad {
	d := func(a, b kurboPoint) kurboPoint {
		return kurboPoint{float64(3 * (b.X - a.X)), float64(3 * (b.Y - a.Y))}
	}
	return kurboQuad{d(c.P0, c.P1), d(c.P1, c.P2), d(c.P2, c.P3)}
}

// subdivide is CubicBez::subdivide (de Casteljau halves).
func (c kurboCubic) subdivide() (kurboCubic, kurboCubic) {
	pm := c.eval(0.5)
	mid := func(a, b kurboPoint) kurboPoint {
		return kurboPoint{float64(0.5 * (a.X + b.X)), float64(0.5 * (a.Y + b.Y))}
	}
	q := func(a, b, d kurboPoint) kurboPoint {
		return kurboPoint{float64((a.X + float64(b.X*2) + d.X) * 0.25), float64((a.Y + float64(b.Y*2) + d.Y) * 0.25)}
	}
	return kurboCubic{c.P0, mid(c.P0, c.P1), q(c.P0, c.P1, c.P2), pm},
		kurboCubic{pm, q(c.P1, c.P2, c.P3), mid(c.P2, c.P3), c.P3}
}

// subsegment is CubicBez::subsegment.
func (c kurboCubic) subsegment(t0, t1 float64) kurboCubic {
	p0 := c.eval(t0)
	p3 := c.eval(t1)
	d := c.deriv()
	scale := float64((t1 - t0) * (1.0 / 3.0))
	d0, d1 := d.eval(t0), d.eval(t1)
	p1 := kurboPoint{p0.X + float64(scale*d0.X), p0.Y + float64(scale*d0.Y)}
	p2 := kurboPoint{p3.X - float64(scale*d1.X), p3.Y - float64(scale*d1.Y)}
	return kurboCubic{p0, p1, p2, p3}
}

var (
	kurboGL8 = [...][2]float64{
		{0.3626837833783620, -0.1834346424956498},
		{0.3626837833783620, 0.1834346424956498},
		{0.3137066458778873, -0.5255324099163290},
		{0.3137066458778873, 0.5255324099163290},
		{0.2223810344533745, -0.7966664774136267},
		{0.2223810344533745, 0.7966664774136267},
		{0.1012285362903763, -0.9602898564975363},
		{0.1012285362903763, 0.9602898564975363},
	}
	kurboGL8Half = []([2]float64){
		{0.3626837833783620, 0.1834346424956498},
		{0.3137066458778873, 0.5255324099163290},
		{0.2223810344533745, 0.7966664774136267},
		{0.1012285362903763, 0.9602898564975363},
	}
	kurboGL16Half = []([2]float64){
		{0.1894506104550685, 0.0950125098376374},
		{0.1826034150449236, 0.2816035507792589},
		{0.1691565193950025, 0.4580167776572274},
		{0.1495959888165767, 0.6178762444026438},
		{0.1246289712555339, 0.7554044083550030},
		{0.0951585116824928, 0.8656312023878318},
		{0.0622535239386479, 0.9445750230732326},
		{0.0271524594117541, 0.9894009349916499},
	}
	kurboGL24Half = []([2]float64){
		{0.1279381953467522, 0.0640568928626056},
		{0.1258374563468283, 0.1911188674736163},
		{0.1216704729278034, 0.3150426796961634},
		{0.1155056680537256, 0.4337935076260451},
		{0.1074442701159656, 0.5454214713888396},
		{0.0976186521041139, 0.6480936519369755},
		{0.0861901615319533, 0.7401241915785544},
		{0.0733464814110803, 0.8200019859739029},
		{0.0592985849154368, 0.8864155270044011},
		{0.0442774388174198, 0.9382745520027328},
		{0.0285313886289337, 0.9747285559713095},
		{0.0123412297999872, 0.9951872199970213},
	}
)

// minF64 is f64::min, see minF32.
func minF64(a, b float64) float64 {
	if a != a || b < a {
		return b
	}
	return a
}

func kurboHypot2(v kurboVec2) float64 { return float64(v.X*v.X) + float64(v.Y*v.Y) }

// kurboHypot is Vec2::hypot: hypot2().sqrt(), not the libm hypot.
func kurboHypot(v kurboVec2) float64 { return math.Sqrt(kurboHypot2(v)) }

// kurboPowi is f64::powi for a positive constant exponent: LLVM's repeated squaring.
func kurboPowi(x float64, n int) float64 {
	var res float64
	have := false
	cur := x
	for n > 0 {
		if n&1 != 0 {
			if have {
				res = float64(res * cur)
			} else {
				res, have = cur, true
			}
		}
		cur = float64(cur * cur)
		n >>= 1
	}
	return res
}

func kurboSub(a, b kurboPoint) kurboVec2 { return kurboVec2{a.X - b.X, a.Y - b.Y} }

func kurboArclenQuadrature(coeffs [][2]float64, dm, dm1, dm2 kurboVec2) float64 {
	sum := 0.0
	for _, c := range coeffs {
		wi, xi := c[0], c[1]
		xx := float64(xi * xi)
		d := kurboVec2{dm.X + float64(dm2.X*xx), dm.Y + float64(dm2.Y*xx)}
		dpx := kurboHypot(kurboVec2{d.X + float64(dm1.X*xi), d.Y + float64(dm1.Y*xi)})
		dmx := kurboHypot(kurboVec2{d.X - float64(dm1.X*xi), d.Y - float64(dm1.Y*xi)})
		sum += float64(float64(1.5*wi) * (dpx + dmx))
	}
	return sum
}

func kurboArclenRec(c kurboCubic, accuracy float64, depth int) float64 {
	d03 := kurboSub(c.P3, c.P0)
	d01 := kurboSub(c.P1, c.P0)
	d12 := kurboSub(c.P2, c.P1)
	d23 := kurboSub(c.P3, c.P2)
	lpLc := kurboHypot(d01) + kurboHypot(d12) + kurboHypot(d23) - kurboHypot(d03)
	dd1 := kurboVec2{d12.X - d01.X, d12.Y - d01.Y}
	dd2 := kurboVec2{d23.X - d12.X, d23.Y - d12.Y}
	dm := kurboVec2{float64(0.25*(d01.X+d23.X)) + float64(0.5*d12.X), float64(0.25*(d01.Y+d23.Y)) + float64(0.5*d12.Y)}
	dm1 := kurboVec2{float64(0.5 * (dd2.X + dd1.X)), float64(0.5 * (dd2.Y + dd1.Y))}
	dm2 := kurboVec2{float64(0.25 * (dd2.X - dd1.X)), float64(0.25 * (dd2.Y - dd1.Y))}

	est := 0.0
	for _, c := range kurboGL8 {
		wi, xi := c[0], c[1]
		xx := float64(xi * xi)
		dNorm2 := kurboHypot2(kurboVec2{
			dm.X + float64(dm1.X*xi) + float64(dm2.X*xx),
			dm.Y + float64(dm1.Y*xi) + float64(dm2.Y*xx),
		})
		x2 := float64(2 * xi)
		ddNorm2 := kurboHypot2(kurboVec2{dm1.X + float64(dm2.X*x2), dm1.Y + float64(dm2.Y*x2)})
		est += float64(wi * (ddNorm2 / dNorm2))
	}
	if float64(minF64(float64(kurboPowi(est, 3)*2.5e-6), 3e-2)*lpLc) < accuracy {
		return kurboArclenQuadrature(kurboGL8Half, dm, dm1, dm2)
	}
	if float64(minF64(float64(kurboPowi(est, 6)*1.5e-11), 9e-3)*lpLc) < accuracy {
		return kurboArclenQuadrature(kurboGL16Half, dm, dm1, dm2)
	}
	if float64(minF64(float64(kurboPowi(est, 9)*3.5e-16), 3.5e-3)*lpLc) < accuracy || depth >= 20 {
		return kurboArclenQuadrature(kurboGL24Half, dm, dm1, dm2)
	}
	c0, c1 := c.subdivide()
	return kurboArclenRec(c0, accuracy*0.5, depth+1) + kurboArclenRec(c1, accuracy*0.5, depth+1)
}

// arclen is CubicBez::arclen.
func (c kurboCubic) arclen(accuracy float64) float64 { return kurboArclenRec(c, accuracy, 0) }

// invArclen is ParamCurveArclen::inv_arclen: the parameter where the arc length reaches arclen.
func (c kurboCubic) invArclen(arclen, accuracy float64) float64 {
	if arclen <= 0 {
		return 0
	}
	total := c.arclen(accuracy)
	if arclen >= total {
		return 1
	}
	tLast, arclenLast := 0.0, 0.0
	epsilon := accuracy / total
	n := 1 - minF64(math.Ceil(math.Log2(epsilon)), 0)
	inner := accuracy / n
	f := func(t float64) float64 {
		var arc, dir float64
		if t > tLast {
			arc, dir = c.subsegment(tLast, t).arclen(inner), 1
		} else {
			arc, dir = c.subsegment(t, tLast).arclen(inner), -1
		}
		arclenLast += float64(arc * dir)
		tLast = t
		return arclenLast - arclen
	}
	return kurboSolveITP(f, 0, 1, epsilon, 1, 0.2, -arclen, total-arclen)
}

// kurboSolveITP is common::solve_itp: the midpoint of the bracket solve_itp_fallible narrows.
func kurboSolveITP(f func(float64) float64, a, b, epsilon float64, n0 int, k1, ya, yb float64) float64 {
	n12 := int(stF64ToUsize(maxF64(math.Ceil(math.Log2((b-a)/epsilon))-1, 0)))
	nmax := n0 + n12
	scaled := float64(epsilon * float64(uint64(1)<<(uint(nmax)&63)))
	for b-a > float64(2*epsilon) {
		x12 := float64(0.5 * (a + b))
		r := scaled - float64(0.5*(b-a))
		xf := (float64(yb*a) - float64(ya*b)) / (yb - ya)
		sigma := x12 - xf
		delta := float64(k1 * float64((b-a)*(b-a)))
		var xt float64
		if delta <= math.Abs(x12-xf) {
			xt = xf + math.Copysign(delta, sigma)
		} else {
			xt = x12
		}
		var xitp float64
		if math.Abs(xt-x12) <= r {
			xitp = xt
		} else {
			xitp = x12 - math.Copysign(r, sigma)
		}
		yitp := f(xitp)
		switch {
		case yitp > 0:
			b, yb = xitp, yitp
		case yitp < 0:
			a, ya = xitp, yitp
		default:
			a, b = xitp, xitp
			return float64(0.5 * (a + b))
		}
		scaled *= 0.5
	}
	return float64(0.5 * (a + b))
}
