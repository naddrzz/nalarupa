package skia

// The sweep gradient shader (shaders/sweep_gradient.rs).

// SweepGradient is a sweep gradient shader.
type SweepGradient struct {
	base gradientBase
	t0   float32
	t1   float32
}

// NewSweepGradient creates a sweep gradient shader with angles in degrees.
//
// It returns a *SolidColor when there is one stop, and false when there are no stops, an angle is not
// finite, startAngle > endAngle, transform is not invertible, or the angles are nearly equal (except in
// pad mode with endAngle > 0).
func NewSweepGradient(center Point, startAngle, endAngle float32, stops []GradientStop, mode SpreadMode, transform Transform) (Shader, bool) {
	if !isFinite(startAngle) || !isFinite(endAngle) || startAngle > endAngle {
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

	if isNearlyEqualWithinTolerance(startAngle, endAngle, gradientDegenerateThreshold) {
		if mode == SpreadModePad && endAngle > gradientDegenerateThreshold {
			// The first color is repeated from 0 to the angle, then a hard stop switches to the last
			// color (all other colors are compressed into the infinitely thin interpolation region).
			frontColor := stops[0].color
			backColor := stops[len(stops)-1].color
			newStops := []GradientStop{
				NewGradientStop(0, frontColor),
				NewGradientStop(1, frontColor),
				NewGradientStop(1, backColor),
			}
			return NewSweepGradient(center, 0, endAngle, newStops, mode, transform)
		}
		return nil, false
	}

	if startAngle <= 0 && endAngle >= 360 {
		mode = SpreadModePad
	}
	return &SweepGradient{
		base: newGradient(stops, mode, transform, TransformFromTranslate(-center.X, -center.Y)),
		t0:   startAngle / 360,
		t1:   endAngle / 360,
	}, true
}

func (s *SweepGradient) IsOpaque() bool { return s.base.colorsAreOpaque }

func (s *SweepGradient) pushStages(cs ColorSpace, p *rasterPipelineBuilder) bool {
	scale := 1 / (s.t1 - s.t0)
	bias := float32(-scale * s.t0)
	p.ctx.twoPointConicalGradient.p0 = scale
	p.ctx.twoPointConicalGradient.p1 = bias
	pre := func(p *rasterPipelineBuilder) {
		p.push(stageXYToUnitAngle)
		if scale != 1 && bias != 0 {
			p.push(stageApplyConcentricScaleBias)
		}
	}
	return s.base.pushStages(p, cs, pre, gradientNoStages)
}

func (s *SweepGradient) postTransform(ts Transform) {
	s.base.transform = s.base.transform.PostConcat(ts)
}

func (s *SweepGradient) applyOpacity(opacity float32) { s.base.applyOpacity(opacity) }

func (s *SweepGradient) clone() Shader {
	c := *s
	c.base = s.base.clone()
	return &c
}
