// Port of kurbo 0.13.1 src/arc.rs and the SvgArc part of src/svg.rs: only the SVG arc to cubic Bezier
// conversion that usvg's parser/shapes.rs uses (SvgArc, Arc::from_svg_arc, Arc::append_iter,
// Arc::to_cubic_beziers), with the Point/Vec2 arithmetic inlined.
//
// All math is f64 in Rust's operation order; products feeding an add or subtract are wrapped in float64()
// so Go cannot fuse them. sin, cos, tan and atan2 are the correctly rounded stSin, stCos, stTan and stAtan2
// (svgtypes_math.go); powf is math.Pow, which only feeds a ceil.

package usvg

import "math"

// Package-level variables rather than constants so that expressions using them are evaluated in IEEE
// float64 arithmetic, as rustc does, not in Go's exact constant arithmetic.
var (
	kurboPI      = math.Pi     // core::f64::consts::PI
	kurboFracPI2 = math.Pi / 2 // core::f64::consts::FRAC_PI_2 (exact halving)
)

// kurboPoint is kurbo::Point.
type kurboPoint struct{ X, Y float64 }

// kurboVec2 is kurbo::Vec2.
type kurboVec2 struct{ X, Y float64 }

// kurboSvgArc is kurbo::SvgArc, a single SVG arc segment.
type kurboSvgArc struct {
	From, To  kurboPoint
	Radii     kurboVec2 // x is the radius along the x axis after XRotation
	XRotation float64   // radians
	LargeArc  bool
	Sweep     bool
}

// IsStraightLine is SvgArc::is_straight_line: the arc can be replaced by a line.
func (a *kurboSvgArc) IsStraightLine() bool {
	return math.Abs(a.Radii.X) <= 1e-5 || math.Abs(a.Radii.Y) <= 1e-5 || (a.From.X == a.To.X && a.From.Y == a.To.Y)
}

// kurboArc is kurbo::Arc, a single elliptical arc segment.
type kurboArc struct {
	Center     kurboPoint
	Radii      kurboVec2
	StartAngle float64 // radians
	SweepAngle float64 // radians
	XRotation  float64 // radians
}

// kurboArcFromSvgArc is Arc::from_svg_arc; false when the arc is a straight line.
func kurboArcFromSvgArc(arc *kurboSvgArc) (kurboArc, bool) {
	// Checked first, otherwise sumOfSq would be 0.
	if arc.IsStraightLine() {
		return kurboArc{}, false
	}

	rx := math.Abs(arc.Radii.X)
	ry := math.Abs(arc.Radii.Y)

	twoPI := 2.0 * kurboPI
	xr := math.Mod(arc.XRotation, twoPI)   // Rust f64 % is fmod, as math.Mod
	sinPhi, cosPhi := stSin(xr), stCos(xr) // f64::sin_cos: libm sin and cos
	hdX := (arc.From.X - arc.To.X) * 0.5
	hdY := (arc.From.Y - arc.To.Y) * 0.5
	hsX := (arc.From.X + arc.To.X) * 0.5
	hsY := (arc.From.Y + arc.To.Y) * 0.5

	// F6.5.1
	pX := float64(cosPhi*hdX) + float64(sinPhi*hdY)
	pY := float64(-sinPhi*hdX) + float64(cosPhi*hdY)

	// F6.6.2: radii too small to connect the end points are scaled up, per the SVG spec.
	rf := float64(float64(pX*pX)/float64(rx*rx)) + float64(float64(pY*pY)/float64(ry*ry))
	if rf > 1.0 {
		scale := math.Sqrt(rf)
		rx *= scale
		ry *= scale
	}

	rxry := rx * ry
	rxpy := rx * pY
	rypx := ry * pX
	sumOfSq := float64(rxpy*rxpy) + float64(rypx*rypx)

	// F6.5.2
	signCoe := 1.0
	if arc.LargeArc == arc.Sweep {
		signCoe = -1.0
	}
	coe := signCoe * math.Sqrt(math.Abs((float64(rxry*rxry)-sumOfSq)/sumOfSq))
	transformedCx := float64(coe*rxpy) / ry
	transformedCy := float64(-coe*rypx) / rx

	// F6.5.3
	center := kurboPoint{
		X: float64(cosPhi*transformedCx) - float64(sinPhi*transformedCy) + hsX,
		Y: float64(sinPhi*transformedCx) + float64(cosPhi*transformedCy) + hsY,
	}

	startV := kurboVec2{(pX - transformedCx) / rx, (pY - transformedCy) / ry}
	endV := kurboVec2{(-pX - transformedCx) / rx, (-pY - transformedCy) / ry}

	startAngle := stAtan2(startV.Y, startV.X) // Vec2::atan2: libm atan2

	sweepAngle := math.Mod(stAtan2(endV.Y, endV.X)-startAngle, twoPI)
	if arc.Sweep && sweepAngle < 0.0 {
		sweepAngle += twoPI
	} else if !arc.Sweep && sweepAngle > 0.0 {
		sweepAngle -= twoPI
	}

	return kurboArc{
		Center:     center,
		Radii:      kurboVec2{rx, ry},
		StartAngle: startAngle,
		SweepAngle: sweepAngle,
		XRotation:  arc.XRotation,
	}, true
}

// kurboArcAppendIter is kurbo::ArcAppendIter: it yields the arc as cubic Bezier segments.
type kurboArcAppendIter struct {
	idx uint64

	center    kurboPoint
	radii     kurboVec2
	xRotation float64
	n         uint64
	armLen    float64
	angleStep float64

	p0     kurboVec2
	angle0 float64
}

// AppendIter is Arc::append_iter.
func (a kurboArc) AppendIter(tolerance float64) kurboArcAppendIter {
	sign := stSignum(a.SweepAngle)
	scaledErr := maxF64(a.Radii.X, a.Radii.Y) / tolerance
	// Number of subdivisions per ellipse based on error tolerance.
	nErr := maxF64(math.Pow(1.1163*scaledErr, 1.0/6.0), 3.999_999) // f64::powf: libm pow
	nf := math.Ceil(float64(nErr*math.Abs(a.SweepAngle)) * (1.0 / (2.0 * kurboPI)))
	angleStep := a.SweepAngle / nf
	armLen := float64((4.0/3.0)*stTan(math.Abs(0.25*angleStep))) * sign // f64::tan: libm tan
	angle0 := a.StartAngle
	p0 := kurboSampleEllipse(a.Radii, a.XRotation, angle0)

	return kurboArcAppendIter{
		center:    a.Center,
		radii:     a.Radii,
		xRotation: a.XRotation,
		n:         stF64ToUsize(nf),
		armLen:    armLen,
		angleStep: angleStep,
		p0:        p0,
		angle0:    angle0,
	}
}

// Next yields the control points and end point of the next cubic (PathEl::CurveTo), or false at the end.
func (it *kurboArcAppendIter) Next() (p1, p2, p3 kurboPoint, ok bool) {
	if it.idx >= it.n {
		return p1, p2, p3, false
	}

	angle1 := it.angle0 + it.angleStep
	p0 := it.p0
	s1 := kurboSampleEllipse(it.radii, it.xRotation, it.angle0+kurboFracPI2)
	v1 := kurboVec2{p0.X + float64(it.armLen*s1.X), p0.Y + float64(it.armLen*s1.Y)}
	v3 := kurboSampleEllipse(it.radii, it.xRotation, angle1)
	s2 := kurboSampleEllipse(it.radii, it.xRotation, angle1+kurboFracPI2)
	v2 := kurboVec2{v3.X - float64(it.armLen*s2.X), v3.Y - float64(it.armLen*s2.Y)}

	it.angle0 = angle1
	it.p0 = v3
	it.idx++

	c := it.center
	return kurboPoint{c.X + v1.X, c.Y + v1.Y}, kurboPoint{c.X + v2.X, c.Y + v2.Y}, kurboPoint{c.X + v3.X, c.Y + v3.Y}, true
}

// ToCubicBeziers is Arc::to_cubic_beziers: p is called with each segment's control points and end point.
func (a kurboArc) ToCubicBeziers(tolerance float64, p func(p1, p2, p3 kurboPoint)) {
	it := a.AppendIter(tolerance)
	for {
		p1, p2, p3, ok := it.Next()
		if !ok {
			return
		}
		p(p1, p2, p3)
	}
}

// kurboSampleEllipse returns the point on the ellipse with the given radii and rotation at angle.
func kurboSampleEllipse(radii kurboVec2, xRotation, angle float64) kurboVec2 {
	angleSin, angleCos := stSin(angle), stCos(angle) // f64::sin_cos
	u := radii.X * angleCos
	v := radii.Y * angleSin
	return kurboRotatePt(kurboVec2{u, v}, xRotation)
}

// kurboRotatePt rotates pt about the origin by angle radians.
func kurboRotatePt(pt kurboVec2, angle float64) kurboVec2 {
	angleSin, angleCos := stSin(angle), stCos(angle) // f64::sin_cos
	return kurboVec2{
		X: float64(pt.X*angleCos) - float64(pt.Y*angleSin),
		Y: float64(pt.X*angleSin) + float64(pt.Y*angleCos),
	}
}
