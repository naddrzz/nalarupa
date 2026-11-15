package skia

// The linear gradient shader (shaders/linear_gradient.rs).

// LinearGradient is a linear gradient shader.
type LinearGradient struct {
	base gradientBase
}

// NewLinearGradient creates a linear gradient shader.
//
// It returns a *SolidColor when there is one stop or start and end are very close, and false when there
// are no stops, start and end are not finitely apart, or transform is not invertible.
func NewLinearGradient(start, end Point, stops []GradientStop, mode SpreadMode, transform Transform) (Shader, bool) {
	if len(stops) == 0 {
		return nil, false
	}
	if len(stops) == 1 {
		return NewSolidColor(stops[0].color), true
	}

	length := end.Sub(start).Length()
	if !isFinite(length) {
		return nil, false
	}

	if isNearlyZeroWithinTolerance(length, gradientDegenerateThreshold) {
		// Degenerate gradient: pad uses the last color; repeat and reflect approximate infinite
		// repetitions by the average color.
		switch mode {
		case SpreadModeReflect, SpreadModeRepeat:
			return NewSolidColor(gradientAverageColor(stops)), true
		default:
			return NewSolidColor(stops[len(stops)-1].color), true
		}
	}

	if _, ok := transform.Invert(); !ok {
		return nil, false
	}

	unitTs := linearPointsToUnitTs(start, end)
	return &LinearGradient{base: newGradient(stops, mode, transform, unitTs)}, true
}

func (s *LinearGradient) IsOpaque() bool { return s.base.colorsAreOpaque }

func (s *LinearGradient) pushStages(cs ColorSpace, p *rasterPipelineBuilder) bool {
	return s.base.pushStages(p, cs, gradientNoStages, gradientNoStages)
}

func (s *LinearGradient) postTransform(ts Transform) {
	s.base.transform = s.base.transform.PostConcat(ts)
}

func (s *LinearGradient) applyOpacity(opacity float32) { s.base.applyOpacity(opacity) }

func (s *LinearGradient) clone() Shader { return &LinearGradient{base: s.base.clone()} }

func linearPointsToUnitTs(start, end Point) Transform {
	vec := end.Sub(start)
	mag := vec.Length()
	inv := float32(0)
	if mag != 0 {
		inv = invert(mag)
	}

	vec.Scale(inv)

	ts := linearTsFromSinCosAt(-vec.Y, vec.X, start.X, start.Y)
	ts = ts.PostTranslate(-start.X, -start.Y)
	ts = ts.PostScale(inv, inv)
	return ts
}

// gradientAverageColor is average_gradient_color; Rust's f32x4 is scalarized per lane.
func gradientAverageColor(points []GradientStop) Color {
	load := func(c Color) [4]float32 { return [4]float32{c.r, c.g, c.b, c.a} }

	// The gradient is a piecewise linear interpolation between colors. An interval's integral is
	// 0.5 * (ci + cj) * (pj - pi); the average color is the sum of the pieces, plus the implicit
	// intervals at p = 0 and p = 1.
	var blend [4]float32
	const wScale = float32(0.5)

	for i := 0; i < len(points)-1; i++ {
		c0 := load(points[i].color)
		c1 := load(points[i+1].color)
		w := points[i+1].position - points[i].position
		ws := float32(wScale * w)
		for l := range blend {
			blend[l] = blend[l] + float32(ws*(c1[l]+c0[l]))
		}
	}

	if p := points[0].position; p > 0 {
		// The first color is fixed between p = 0 and pos[0].
		c := load(points[0].color)
		for l := range blend {
			blend[l] = blend[l] + float32(p*c[l])
		}
	}

	last := len(points) - 1
	if p := points[last].position; p < 1 {
		// The last color is fixed between pos[n-1] and p = 1.
		c := load(points[last].color)
		w := 1 - p
		for l := range blend {
			blend[l] = blend[l] + float32(w*c[l])
		}
	}

	c, ok := ColorFromRGBA(blend[0], blend[1], blend[2], blend[3])
	if !ok {
		// Rust unwraps here too (stops with decreasing positions can reach it).
		panic("skia: average gradient color out of range")
	}
	return c
}

func linearTsFromSinCosAt(sin, cos, px, py float32) Transform {
	cosInv := 1 - cos
	return TransformFromRow(
		cos,
		sin,
		-sin,
		cos,
		linearSdot(sin, py, cosInv, px),
		linearSdot(-sin, px, cosInv, py),
	)
}

func linearSdot(a, b, c, d float32) float32 { return float32(a*b) + float32(c*d) }
