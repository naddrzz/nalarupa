package skia

// The gradient base shared by the linear, radial and sweep gradients (shaders/gradient.rs).

// gradientDegenerateThreshold is DEGENERATE_THRESHOLD: ScalarNearlyZero (.0024) is too big and causes
// regressions for SVG gradients defined in the wild.
const gradientDegenerateThreshold = float32(1.0 / (1 << 15))

// GradientStop is a gradient point.
type GradientStop struct {
	position float32 // NormalizedF32
	color    Color
}

// NewGradientStop creates a gradient point; position is clamped to 0..=1.
func NewGradientStop(position float32, color Color) GradientStop {
	return GradientStop{position: normalizedClamped(position), color: color}
}

type gradientBase struct {
	stops           []GradientStop
	tileMode        SpreadMode
	transform       Transform
	pointsToUnit    Transform
	colorsAreOpaque bool
	hasUniformStops bool
}

// newGradient copies stops (at least two) before changing them.
func newGradient(stops []GradientStop, tileMode SpreadMode, transform, pointsToUnit Transform) gradientBase {
	// The caller may skip the first and/or last position (pos[0] = 0.3, pos[1] = 0.7); insert dummy
	// entries so the data is bracketed by [0, 1].
	dummyFirst := stops[0].position != 0
	dummyLast := stops[len(stops)-1].position != 1

	s := make([]GradientStop, 0, len(stops)+2)
	if dummyFirst {
		s = append(s, NewGradientStop(0, stops[0].color))
	}
	s = append(s, stops...)
	if dummyLast {
		s = append(s, NewGradientStop(1, s[len(s)-1].color))
	}

	colorsAreOpaque := true
	for _, p := range s {
		if !p.color.IsOpaque() {
			colorsAreOpaque = false
			break
		}
	}

	// Pin the last value to 1.0, and make sure positions are monotonic.
	startIndex := 1
	if dummyFirst {
		startIndex = 0
	}
	prev := float32(0)
	hasUniformStops := true
	uniformStep := s[startIndex].position - prev
	for i := startIndex; i < len(s); i++ {
		var curr float32
		if i+1 == len(s) {
			curr = 1 // the last one must be one
		} else {
			curr = bound(s[i].position, prev, 1)
		}
		hasUniformStops = hasUniformStops && isNearlyEqual(uniformStep, curr-prev)
		s[i].position = normalizedClamped(curr)
		prev = curr
	}

	return gradientBase{
		stops:           s,
		tileMode:        tileMode,
		transform:       transform,
		pointsToUnit:    pointsToUnit,
		colorsAreOpaque: colorsAreOpaque,
		hasUniformStops: hasUniformStops,
	}
}

func (g *gradientBase) pushStages(p *rasterPipelineBuilder, cs ColorSpace, pre, post func(*rasterPipelineBuilder)) bool {
	p.push(stageSeedShader)

	ts, ok := g.transform.Invert()
	if !ok {
		return false
	}
	ts = ts.PostConcat(g.pointsToUnit)
	p.pushTransform(ts)

	pre(p)

	switch g.tileMode {
	case SpreadModeReflect:
		p.push(stageReflectX1)
	case SpreadModeRepeat:
		p.push(stageRepeatX1)
	case SpreadModePad:
		if g.hasUniformStops {
			// Clamp only when the stops are evenly spaced: clamping ruins hard stops at 0 and/or 1,
			// which only the general gradient stage handles with an unclamped t.
			p.push(stagePadX1)
		}
	}

	if len(g.stops) == 2 {
		// The two-stop case with stops at 0 and 1.
		c0 := cs.expandColor(g.stops[0].color)
		c1 := cs.expandColor(g.stops[1].color)
		p.ctx.evenlySpaced2StopGradient = evenlySpaced2StopGradientCtx{
			factor: gradientColor{c1.r - c0.r, c1.g - c0.g, c1.b - c0.b, c1.a - c0.a},
			bias:   gradientColorFromColor(c0),
		}
		p.push(stageEvenlySpaced2StopGradient)
	} else {
		// The search assumes a stop conceptually placed at -inf, so there are up to len(stops)+1
		// entries; lowp needs at least 16.
		n := max(len(g.stops)+1, 16)
		ctx := gradientCtx{
			factors: make([]gradientColor, 0, n),
			biases:  make([]gradientColor, 0, n),
			tValues: make([]float32, 0, len(g.stops)+1),
		}

		// Skip the dummy stops inserted by newGradient: the search handles them naturally.
		firstStop, lastStop := 0, 1
		if len(g.stops) > 2 {
			if g.stops[0].color == g.stops[1].color {
				firstStop = 1
			}
			l := len(g.stops)
			lastStop = l - 1
			if g.stops[l-2].color == g.stops[l-1].color {
				lastStop = l - 2
			}
		}

		tL := g.stops[firstStop].position
		cL := gradientColorFromColor(cs.expandColor(g.stops[firstStop].color))
		ctx.pushConstColor(cL)
		ctx.tValues = append(ctx.tValues, 0)
		// lastStop is the index of the last stop, not one after.
		for i := firstStop; i < lastStop; i++ {
			tR := g.stops[i+1].position
			cR := gradientColorFromColor(cs.expandColor(g.stops[i+1].color))
			if tL < tR {
				// For t between stops n and n+1 the color is B[n] + F[n]*t.
				d := tR - tL
				f := gradientColor{(cR.r - cL.r) / d, (cR.g - cL.g) / d, (cR.b - cL.b) / d, (cR.a - cL.a) / d}
				ctx.factors = append(ctx.factors, f)
				ctx.biases = append(ctx.biases, gradientColor{
					cL.r - float32(f.r*tL),
					cL.g - float32(f.g*tL),
					cL.b - float32(f.b*tL),
					cL.a - float32(f.a*tL),
				})
				ctx.tValues = append(ctx.tValues, normalizedClamped(tL))
			}
			tL = tR
			cL = cR
		}

		ctx.pushConstColor(cL)
		ctx.tValues = append(ctx.tValues, normalizedClamped(tL))
		ctx.len = len(ctx.factors)

		// Fill with zeros up to the lowp width.
		for len(ctx.factors) < 16 {
			ctx.factors = append(ctx.factors, gradientColor{})
			ctx.biases = append(ctx.biases, gradientColor{})
		}

		p.push(stageGradient)
		p.ctx.gradient = ctx
	}

	if !g.colorsAreOpaque {
		p.push(stagePremultiply)
	}

	post(p)
	return true
}

func (g *gradientBase) applyOpacity(opacity float32) {
	for i := range g.stops {
		g.stops[i].color.ApplyOpacity(opacity)
	}
	g.colorsAreOpaque = true
	for _, p := range g.stops {
		if !p.color.IsOpaque() {
			g.colorsAreOpaque = false
			break
		}
	}
}

func (g gradientBase) clone() gradientBase {
	g.stops = append([]GradientStop(nil), g.stops...)
	return g
}

func gradientNoStages(*rasterPipelineBuilder) {}
