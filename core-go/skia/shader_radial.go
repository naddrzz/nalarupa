package skia

// The two-point conical (radial) gradient shader (shaders/radial_gradient.rs).

type radialFocalData struct {
	r1        float32 // r1 after mapping the focal point to (0, 0)
	focalX    float32 // f
	isSwapped bool
}

func (fd *radialFocalData) set(r0, r1 float32, matrix *Transform) bool {
	fd.isSwapped = false
	fd.focalX = r0 / (r0 - r1)

	if isNearlyZero(fd.focalX - 1) {
		// Swap r0 and r1.
		*matrix = matrix.PostTranslate(-1, 0).PostScale(-1, 1)
		r0, r1 = r1, r0
		fd.focalX = 0 // because r0 is now 0
		fd.isSwapped = true
	}

	// Map {focal point, (1, 0)} to {(0, 0), (1, 0)}.
	focalMatrix, ok := radialTsFromPolyToPoly(Pt(fd.focalX, 0), Pt(1, 0), Pt(0, 0), Pt(1, 0))
	if !ok {
		return false
	}

	*matrix = matrix.PostConcat(focalMatrix)
	fd.r1 = r1 / abs32(1-fd.focalX) // focalMatrix has a scale of 1/(1-f)

	// These only save some arithmetic in the shader.
	if fd.isFocalOnCircle() {
		*matrix = matrix.PostScale(0.5, 0.5)
	} else {
		r1r1 := float32(fd.r1*fd.r1) - 1
		*matrix = matrix.PostScale(fd.r1/r1r1, 1/sqrt32(abs32(r1r1)))
	}

	s := abs32(1 - fd.focalX)
	*matrix = matrix.PostScale(s, s) // scale |1 - f|
	return true
}

func (fd radialFocalData) isFocalOnCircle() bool { return isNearlyZero(1 - fd.r1) }

func (fd radialFocalData) isWellBehaved() bool { return !fd.isFocalOnCircle() && fd.r1 > 1 }

func (fd radialFocalData) isNativelyFocal() bool { return isNearlyZero(fd.focalX) }

// radialType is Rust's GradientType enum.
type radialType uint8

const (
	radialTypeRadial radialType = iota // radius1, radius2
	radialTypeStrip                    // scaledR0: r0 / d_center
	radialTypeFocal                    // focal
)

// RadialGradient is a two-point conical gradient shader.
type RadialGradient struct {
	base     gradientBase
	typ      radialType
	radius1  float32
	radius2  float32
	scaledR0 float32
	focal    radialFocalData
}

// NewRadialGradient creates a two-point conical gradient shader, which interpolates colors between two
// circles defined by their centers and radii.
//
// It returns a *SolidColor when there is one stop, and false when there are no stops, a radius is
// negative, transform is not invertible, or the gradient is degenerate (both radii and centers equal,
// except in some pad mode cases).
func NewRadialGradient(start Point, startRadius float32, end Point, endRadius float32, stops []GradientStop, mode SpreadMode, transform Transform) (Shader, bool) {
	if startRadius < 0 || endRadius < 0 {
		return nil, false
	}

	switch len(stops) {
	case 0:
		return nil, false
	case 1:
		return NewSolidColor(stops[0].color), true
	}

	if _, ok := transform.Invert(); !ok {
		return nil, false
	}

	length := start.Sub(end).Length()
	if !isFinite(length) {
		return nil, false
	}
	if isNearlyZeroWithinTolerance(length, gradientDegenerateThreshold) {
		if isNearlyEqualWithinTolerance(startRadius, endRadius, gradientDegenerateThreshold) {
			// The interpolation region area approaches zero. Pad with radii > 0 becomes an infinitely
			// thin ring: the first color from p=0 to 1, then a hard stop to the last color.
			if mode == SpreadModePad && endRadius > gradientDegenerateThreshold {
				startColor := stops[0].color
				endColor := stops[len(stops)-1].color
				newStops := []GradientStop{
					NewGradientStop(0, startColor),
					NewGradientStop(1, startColor),
					NewGradientStop(1, endColor),
				}
				return newRadialUnchecked(start, endRadius, newStops, mode, transform), true
			}
			return nil, false
		}

		if isNearlyZeroWithinTolerance(startRadius, gradientDegenerateThreshold) {
			// Same centers and startRadius == 0: a simple radial gradient, which is faster.
			return newRadialUnchecked(start, endRadius, stops, mode, transform), true
		}
	}

	return radialCreate(start, startRadius, end, endRadius, stops, mode, transform)
}

// newRadialUnchecked creates a simple radial gradient (start radius 0) without validation.
func newRadialUnchecked(center Point, radius float32, stops []GradientStop, mode SpreadMode, transform Transform) *RadialGradient {
	inv := invert(radius)
	pointsToUnit := TransformFromTranslate(-center.X, -center.Y).PostScale(inv, inv)
	return &RadialGradient{
		base:    newGradient(stops, mode, transform, pointsToUnit),
		typ:     radialTypeRadial,
		radius1: 0,
		radius2: radius,
	}
}

func (s *RadialGradient) IsOpaque() bool { return false } // may have "undefined" points

func (s *RadialGradient) pushStages(cs ColorSpace, p *rasterPipelineBuilder) bool {
	var p0, p1 float32
	switch s.typ {
	case radialTypeRadial:
		if s.radius1 == 0 {
			p0, p1 = 1, 0
		} else {
			dRadius := s.radius2 - s.radius1
			// For concentric gradients: t = t * scale + bias.
			p0 = maxf(s.radius1, s.radius2) / dRadius
			p1 = -s.radius1 / dRadius
		}
	case radialTypeStrip:
		p0, p1 = float32(s.scaledR0*s.scaledR0), 0 // p1 unused
	case radialTypeFocal:
		p0, p1 = 1/s.focal.r1, s.focal.focalX
	}

	p.ctx.twoPointConicalGradient = twoPointConicalGradientCtx{p0: p0, p1: p1}

	pre := func(p *rasterPipelineBuilder) {
		switch s.typ {
		case radialTypeRadial:
			p.push(stageXYToRadius)
			// Map t from [0, 1] based on r_max to t=0 at r0 and t=1 at r1.
			if p0 != 1 || p1 != 0 {
				p.push(stageApplyConcentricScaleBias)
			}
		case radialTypeStrip:
			p.push(stageXYTo2PtConicalStrip)
			p.push(stageMask2PtConicalNan)
		case radialTypeFocal:
			fd := s.focal
			switch {
			case fd.isFocalOnCircle():
				p.push(stageXYTo2PtConicalFocalOnCircle)
			case fd.isWellBehaved():
				p.push(stageXYTo2PtConicalWellBehaved)
			case fd.isSwapped || 1-fd.focalX < 0:
				p.push(stageXYTo2PtConicalSmaller)
			default:
				p.push(stageXYTo2PtConicalGreater)
			}
			if !fd.isWellBehaved() {
				p.push(stageMask2PtConicalDegenerates)
			}
			if 1-fd.focalX < 0 {
				p.push(stageNegateX)
			}
			if !fd.isNativelyFocal() {
				p.push(stageAlter2PtConicalCompensateFocal)
			}
			if fd.isSwapped {
				p.push(stageAlter2PtConicalUnswap)
			}
		}
	}
	post := func(p *rasterPipelineBuilder) {
		if s.typ == radialTypeStrip || (s.typ == radialTypeFocal && !s.focal.isWellBehaved()) {
			p.push(stageApplyVectorMask)
		}
	}
	return s.base.pushStages(p, cs, pre, post)
}

func (s *RadialGradient) postTransform(ts Transform) {
	s.base.transform = s.base.transform.PostConcat(ts)
}

func (s *RadialGradient) applyOpacity(opacity float32) { s.base.applyOpacity(opacity) }

func (s *RadialGradient) clone() Shader {
	c := *s
	c.base = s.base.clone()
	return &c
}

func radialCreate(c0 Point, r0 float32, c1 Point, r1 float32, stops []GradientStop, mode SpreadMode, transform Transform) (Shader, bool) {
	g := &RadialGradient{}
	var gradientMatrix Transform

	if isNearlyZero(c0.Sub(c1).Length()) {
		if isNearlyZero(maxf(r0, r1)) || isNearlyEqual(r0, r1) {
			// Degenerate; avoid dividing by zero (the caller should have caught it).
			return nil, false
		}
		// Concentric: pretend to be radial (with a tiny twist).
		scale := 1 / maxf(r0, r1)
		gradientMatrix = TransformFromTranslate(-c1.X, -c1.Y).PostScale(scale, scale)
		g.typ = radialTypeRadial
		g.radius1 = r0
		g.radius2 = r1
	} else {
		m, ok := radialMapToUnitX(c0, c1)
		if !ok {
			return nil, false
		}
		gradientMatrix = m
		dCenter := c0.Sub(c1).Length()
		if isNearlyZero(r0 - r1) {
			g.typ = radialTypeStrip
			g.scaledR0 = r0 / dCenter
		} else {
			g.typ = radialTypeFocal
		}
	}
	if g.typ == radialTypeFocal {
		dCenter := c0.Sub(c1).Length()
		if !g.focal.set(r0/dCenter, r1/dCenter, &gradientMatrix) {
			return nil, false
		}
	}

	g.base = newGradient(stops, mode, transform, gradientMatrix)
	return g, true
}

func radialMapToUnitX(origin, xIsOne Point) (Transform, bool) {
	return radialTsFromPolyToPoly(origin, xIsOne, Pt(0, 0), Pt(1, 0))
}

func radialTsFromPolyToPoly(src1, src2, dst1, dst2 Point) (Transform, bool) {
	res, ok := radialFromPoly2(src1, src2).Invert()
	if !ok {
		return Transform{}, false
	}
	return radialFromPoly2(dst1, dst2).PreConcat(res), true
}

func radialFromPoly2(p0, p1 Point) Transform {
	return TransformFromRow(p1.Y-p0.Y, p0.X-p1.X, p1.X-p0.X, p1.Y-p0.Y, p0.X, p0.Y)
}
