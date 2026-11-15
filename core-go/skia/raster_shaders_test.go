package skia

import (
	"math"
	"slices"
	"testing"
)

// The shaders files have no Rust unit tests; these check the constructors' degenerate cases and the
// stages and context pushStages produces, worked out by hand from the Rust code.

func shTestColor(t *testing.T, r, g, b, a float32) Color {
	t.Helper()
	c, ok := ColorFromRGBA(r, g, b, a)
	if !ok {
		t.Fatalf("bad color %v %v %v %v", r, g, b, a)
	}
	return c
}

var (
	shRed   = Color{1, 0, 0, 1}
	shGreen = Color{0, 1, 0, 1}
	shBlue  = Color{0, 0, 1, 1}
)

func shTwoStops() []GradientStop {
	return []GradientStop{NewGradientStop(0, shRed), NewGradientStop(1, shBlue)}
}

func shPush(t *testing.T, s Shader, cs ColorSpace) *rasterPipelineBuilder {
	t.Helper()
	p := newRasterPipelineBuilder()
	if !s.pushStages(cs, p) {
		t.Fatal("pushStages returned false")
	}
	return p
}

func shWantStages(t *testing.T, p *rasterPipelineBuilder, want ...stage) {
	t.Helper()
	if !slices.Equal(p.stages, want) {
		t.Fatalf("stages = %v, want %v", p.stages, want)
	}
}

func shWantSolid(t *testing.T, s Shader, ok bool, want Color) {
	t.Helper()
	sc, isSolid := s.(*SolidColor)
	if !ok || !isSolid {
		t.Fatalf("got %T %v, want a solid color", s, ok)
	}
	if sc.Color != want {
		t.Fatalf("color = %v, want %v", sc.Color, want)
	}
}

func TestGradientStopClamps(t *testing.T) {
	for _, c := range []struct{ in, want float32 }{{-1, 0}, {0.5, 0.5}, {2, 1}, {float32(math.NaN()), 0}, {float32(math.Inf(1)), 0}} {
		if got := NewGradientStop(c.in, shRed).position; got != c.want {
			t.Errorf("NewGradientStop(%v) position = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestLinearGradientDegenerate(t *testing.T) {
	id := Identity()
	if s, ok := NewLinearGradient(Pt(0, 0), Pt(10, 0), nil, SpreadModePad, id); ok || s != nil {
		t.Error("no stops: want none")
	}
	s, ok := NewLinearGradient(Pt(0, 0), Pt(10, 0), []GradientStop{NewGradientStop(0.3, shGreen)}, SpreadModePad, id)
	shWantSolid(t, s, ok, shGreen)

	if _, ok := NewLinearGradient(Pt(-3e38, 0), Pt(3e38, 0), shTwoStops(), SpreadModePad, id); ok {
		t.Error("infinite length: want none")
	}
	if _, ok := NewLinearGradient(Pt(0, 0), Pt(10, 0), shTwoStops(), SpreadModePad, shSingular); ok {
		t.Error("non-invertible transform: want none")
	}

	// Start == end: pad gives the last color, repeat and reflect the average color.
	s, ok = NewLinearGradient(Pt(5, 5), Pt(5, 5), shTwoStops(), SpreadModePad, id)
	shWantSolid(t, s, ok, shBlue)
	s, ok = NewLinearGradient(Pt(5, 5), Pt(5, 5), shTwoStops(), SpreadModeRepeat, id)
	shWantSolid(t, s, ok, Color{0.5, 0, 0.5, 1})
	// Implicit intervals: 0.25*(red+blue) + 0.25*red + 0.25*blue.
	stops := []GradientStop{NewGradientStop(0.25, shRed), NewGradientStop(0.75, shBlue)}
	s, ok = NewLinearGradient(Pt(5, 5), Pt(5, 5), stops, SpreadModeReflect, id)
	shWantSolid(t, s, ok, Color{0.5, 0, 0.5, 1})
}

func TestLinearGradientCopiesStops(t *testing.T) {
	stops := []GradientStop{NewGradientStop(0.3, shRed), NewGradientStop(0.2, shBlue)}
	orig := slices.Clone(stops)
	s, ok := NewLinearGradient(Pt(0, 0), Pt(10, 0), stops, SpreadModePad, Identity())
	if !ok {
		t.Fatal("want a gradient")
	}
	if !slices.Equal(stops, orig) {
		t.Fatalf("caller's stops changed: %v", stops)
	}
	g := s.(*LinearGradient)
	// Dummies at 0 and 1; the 0.2 stop is pinned to 0.3 to stay monotonic.
	want := []float32{0, 0.3, 0.3, 1}
	for i, st := range g.base.stops {
		if st.position != want[i] {
			t.Fatalf("positions = %v, want %v", g.base.stops, want)
		}
	}
}

func TestLinearGradientTwoStops(t *testing.T) {
	s, ok := NewLinearGradient(Pt(0, 0), Pt(10, 0), shTwoStops(), SpreadModePad, Identity())
	if !ok || !s.IsOpaque() {
		t.Fatal("want an opaque gradient")
	}
	p := shPush(t, s, ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageTransform, stagePadX1, stageEvenlySpaced2StopGradient)
	if p.ctx.transform != TransformFromScale(0.1, 0.1) {
		t.Errorf("transform = %v", p.ctx.transform)
	}
	want := evenlySpaced2StopGradientCtx{factor: gradientColor{-1, 0, 1, 0}, bias: gradientColor{1, 0, 0, 1}}
	if p.ctx.evenlySpaced2StopGradient != want {
		t.Errorf("ctx = %v, want %v", p.ctx.evenlySpaced2StopGradient, want)
	}

	// The stop colors are expanded into the color space.
	stops := []GradientStop{NewGradientStop(0, shTestColor(t, 0.5, 0, 0, 1)), NewGradientStop(1, shBlue)}
	s, _ = NewLinearGradient(Pt(0, 0), Pt(10, 0), stops, SpreadModePad, Identity())
	p = shPush(t, s, ColorSpaceGamma2)
	if got := p.ctx.evenlySpaced2StopGradient.bias.r; got != 0.25 {
		t.Errorf("gamma2 bias.r = %v, want 0.25", got)
	}
}

func TestLinearGradientThreeStops(t *testing.T) {
	stops := []GradientStop{
		NewGradientStop(0, shRed),
		NewGradientStop(0.5, shGreen),
		NewGradientStop(1, shTestColor(t, 0, 0, 1, 0.5)),
	}
	s, ok := NewLinearGradient(Pt(0, 0), Pt(10, 0), stops, SpreadModeRepeat, Identity())
	if !ok || s.IsOpaque() {
		t.Fatal("want a translucent gradient")
	}
	p := shPush(t, s, ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageTransform, stageRepeatX1, stageGradient, stagePremultiply)

	ctx := p.ctx.gradient
	if ctx.len != 4 || len(ctx.factors) != 16 || len(ctx.biases) != 16 {
		t.Fatalf("len = %d, factors %d, biases %d", ctx.len, len(ctx.factors), len(ctx.biases))
	}
	if want := []float32{0, 0, 0.5, 1}; !slices.Equal(ctx.tValues, want) {
		t.Errorf("tValues = %v, want %v", ctx.tValues, want)
	}
	wantF := []gradientColor{{}, {-2, 2, 0, 0}, {0, -2, 2, -1}, {}}
	wantB := []gradientColor{{1, 0, 0, 1}, {1, 0, 0, 1}, {0, 2, -1, 1.5}, {0, 0, 1, 0.5}}
	if !slices.Equal(ctx.factors[:4], wantF) || !slices.Equal(ctx.biases[:4], wantB) {
		t.Errorf("factors = %v, biases = %v", ctx.factors[:4], ctx.biases[:4])
	}
	for i := 4; i < 16; i++ {
		if ctx.factors[i] != (gradientColor{}) || ctx.biases[i] != (gradientColor{}) {
			t.Fatalf("padding %d not zero", i)
		}
	}
}

func TestLinearGradientDummyStops(t *testing.T) {
	stops := []GradientStop{NewGradientStop(0.25, shRed), NewGradientStop(0.75, shBlue)}
	s, _ := NewLinearGradient(Pt(0, 0), Pt(10, 0), stops, SpreadModePad, Identity())
	p := shPush(t, s, ColorSpaceLinear)
	// Non-uniform stops: no PadX1. The dummies are skipped since they repeat the edge colors.
	shWantStages(t, p, stageSeedShader, stageTransform, stageGradient)
	ctx := p.ctx.gradient
	if ctx.len != 3 {
		t.Fatalf("len = %d", ctx.len)
	}
	if want := []float32{0, 0.25, 0.75}; !slices.Equal(ctx.tValues, want) {
		t.Errorf("tValues = %v, want %v", ctx.tValues, want)
	}
	wantF := []gradientColor{{}, {-2, 0, 2, 0}, {}}
	wantB := []gradientColor{{1, 0, 0, 1}, {1.5, 0, -0.5, 1}, {0, 0, 1, 1}}
	if !slices.Equal(ctx.factors[:3], wantF) || !slices.Equal(ctx.biases[:3], wantB) {
		t.Errorf("factors = %v, biases = %v", ctx.factors[:3], ctx.biases[:3])
	}
}

func TestGradientOpacityTransformClone(t *testing.T) {
	s, _ := NewLinearGradient(Pt(0, 0), Pt(10, 0), shTwoStops(), SpreadModePad, Identity())
	c := s.clone()
	c.applyOpacity(0.5)
	if !s.IsOpaque() || c.IsOpaque() {
		t.Fatal("clone shares stops with the original")
	}
	if a := c.(*LinearGradient).base.stops[1].color.a; a != 0.5 {
		t.Errorf("alpha = %v", a)
	}
	shWantStages(t, shPush(t, c, ColorSpaceLinear),
		stageSeedShader, stageTransform, stagePadX1, stageEvenlySpaced2StopGradient, stagePremultiply)

	// A non-invertible transform draws nothing, after pushing SeedShader as Rust does.
	c.postTransform(shSingular)
	p := newRasterPipelineBuilder()
	if c.pushStages(ColorSpaceLinear, p) {
		t.Fatal("want false")
	}
	shWantStages(t, p, stageSeedShader)
	if s.(*LinearGradient).base.transform != Identity() {
		t.Fatal("postTransform changed the original")
	}
}

func TestRadialGradientDegenerate(t *testing.T) {
	id := Identity()
	if _, ok := NewRadialGradient(Pt(0, 0), -1, Pt(0, 0), 10, shTwoStops(), SpreadModePad, id); ok {
		t.Error("negative radius: want none")
	}
	if _, ok := NewRadialGradient(Pt(0, 0), 0, Pt(0, 0), 10, nil, SpreadModePad, id); ok {
		t.Error("no stops: want none")
	}
	s, ok := NewRadialGradient(Pt(0, 0), 0, Pt(0, 0), 10, []GradientStop{NewGradientStop(0, shRed)}, SpreadModePad, id)
	shWantSolid(t, s, ok, shRed)
	if _, ok := NewRadialGradient(Pt(0, 0), 0, Pt(0, 0), 10, shTwoStops(), SpreadModePad, shSingular); ok {
		t.Error("non-invertible transform: want none")
	}
	if _, ok := NewRadialGradient(Pt(5, 5), 10, Pt(5, 5), 10, shTwoStops(), SpreadModeRepeat, id); ok {
		t.Error("equal circles, repeat: want none")
	}
	if _, ok := NewRadialGradient(Pt(5, 5), 0, Pt(5, 5), 0, shTwoStops(), SpreadModePad, id); ok {
		t.Error("zero radii: want none")
	}

	// Equal circles in pad mode: first color, then a hard stop to the last one at the radius.
	s, ok = NewRadialGradient(Pt(5, 5), 10, Pt(5, 5), 10, shTwoStops(), SpreadModePad, id)
	if !ok {
		t.Fatal("want a gradient")
	}
	g := s.(*RadialGradient)
	if g.typ != radialTypeRadial || g.radius1 != 0 || g.radius2 != 10 || len(g.base.stops) != 3 {
		t.Fatalf("got %+v", g)
	}
	p := shPush(t, s, ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageTransform, stageXYToRadius, stageGradient)
	ctx := p.ctx.gradient
	if ctx.len != 2 || !slices.Equal(ctx.tValues, []float32{0, 1}) ||
		ctx.biases[0] != gradientColorFromColor(shRed) || ctx.biases[1] != gradientColorFromColor(shBlue) {
		t.Errorf("ctx = %+v", ctx)
	}
}

func TestRadialGradientSimple(t *testing.T) {
	s, ok := NewRadialGradient(Pt(5, 5), 0, Pt(5, 5), 10, shTwoStops(), SpreadModePad, Identity())
	if !ok || s.IsOpaque() {
		t.Fatal("want a gradient, never opaque")
	}
	p := shPush(t, s, ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageTransform, stageXYToRadius, stagePadX1, stageEvenlySpaced2StopGradient)
	if want := TransformFromRow(0.1, 0, 0, 0.1, -0.5, -0.5); p.ctx.transform != want {
		t.Errorf("transform = %v, want %v", p.ctx.transform, want)
	}
	if c := p.ctx.twoPointConicalGradient; c.p0 != 1 || c.p1 != 0 || c.mask != [8]uint32{} {
		t.Errorf("ctx = %+v", c)
	}
}

func TestRadialGradientConcentric(t *testing.T) {
	s, _ := NewRadialGradient(Pt(0, 0), 5, Pt(0, 0), 10, shTwoStops(), SpreadModePad, Identity())
	p := shPush(t, s, ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageTransform, stageXYToRadius, stageApplyConcentricScaleBias,
		stagePadX1, stageEvenlySpaced2StopGradient)
	if p.ctx.transform != TransformFromScale(0.1, 0.1) {
		t.Errorf("transform = %v", p.ctx.transform)
	}
	if c := p.ctx.twoPointConicalGradient; c.p0 != 2 || c.p1 != -1 {
		t.Errorf("ctx = %+v", c)
	}
}

func TestRadialGradientStrip(t *testing.T) {
	s, _ := NewRadialGradient(Pt(0, 0), 5, Pt(10, 0), 5, shTwoStops(), SpreadModePad, Identity())
	p := shPush(t, s, ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageTransform, stageXYTo2PtConicalStrip, stageMask2PtConicalNan,
		stagePadX1, stageEvenlySpaced2StopGradient, stageApplyVectorMask)
	if p.ctx.transform != TransformFromScale(0.1, 0.1) {
		t.Errorf("transform = %v", p.ctx.transform)
	}
	if c := p.ctx.twoPointConicalGradient; c.p0 != 0.25 || c.p1 != 0 {
		t.Errorf("ctx = %+v", c)
	}
}

func TestRadialGradientFocal(t *testing.T) {
	cases := []struct {
		name   string
		r0, r1 float32
		p0, p1 float32
		stages []stage
	}{
		// focal_x = 0, r1 = 0.5: not well behaved, greater.
		{"greater", 0, 5, 2, 0, []stage{stageXYTo2PtConicalGreater, stageMask2PtConicalDegenerates,
			stagePadX1, stageEvenlySpaced2StopGradient, stageApplyVectorMask}},
		// r1 = 2: well behaved.
		{"well behaved", 0, 20, 0.5, 0, []stage{stageXYTo2PtConicalWellBehaved,
			stagePadX1, stageEvenlySpaced2StopGradient}},
		// focal_x = 1: swapped, then r1 = 1 is on the circle.
		{"swapped on circle", 10, 0, 1, 0, []stage{stageXYTo2PtConicalFocalOnCircle, stageMask2PtConicalDegenerates,
			stageAlter2PtConicalUnswap, stagePadX1, stageEvenlySpaced2StopGradient, stageApplyVectorMask}},
	}
	for _, c := range cases {
		s, ok := NewRadialGradient(Pt(0, 0), c.r0, Pt(10, 0), c.r1, shTwoStops(), SpreadModePad, Identity())
		if !ok {
			t.Fatalf("%s: want a gradient", c.name)
		}
		if typ := s.(*RadialGradient).typ; typ != radialTypeFocal {
			t.Fatalf("%s: type %v", c.name, typ)
		}
		p := shPush(t, s, ColorSpaceLinear)
		shWantStages(t, p, append([]stage{stageSeedShader, stageTransform}, c.stages...)...)
		if g := p.ctx.twoPointConicalGradient; g.p0 != c.p0 || g.p1 != c.p1 {
			t.Errorf("%s: ctx = %+v", c.name, g)
		}
	}
}

func TestSweepGradient(t *testing.T) {
	id := Identity()
	for _, c := range []struct{ a0, a1 float32 }{{10, 5}, {float32(math.NaN()), 90}, {0, float32(math.Inf(1))}} {
		if _, ok := NewSweepGradient(Pt(0, 0), c.a0, c.a1, shTwoStops(), SpreadModePad, id); ok {
			t.Errorf("angles %v %v: want none", c.a0, c.a1)
		}
	}
	if _, ok := NewSweepGradient(Pt(0, 0), 0, 90, nil, SpreadModePad, id); ok {
		t.Error("no stops: want none")
	}
	s, ok := NewSweepGradient(Pt(0, 0), 0, 90, []GradientStop{NewGradientStop(0, shGreen)}, SpreadModePad, id)
	shWantSolid(t, s, ok, shGreen)
	if _, ok := NewSweepGradient(Pt(0, 0), 90, 90, shTwoStops(), SpreadModeRepeat, id); ok {
		t.Error("equal angles, repeat: want none")
	}

	// Equal angles in pad mode: 0..angle with a hard stop at the end.
	s, ok = NewSweepGradient(Pt(0, 0), 90, 90, shTwoStops(), SpreadModePad, id)
	if !ok {
		t.Fatal("want a gradient")
	}
	if g := s.(*SweepGradient); g.t0 != 0 || g.t1 != 0.25 || len(g.base.stops) != 3 || g.base.hasUniformStops {
		t.Errorf("got %+v", g)
	}

	cases := []struct {
		a0, a1 float32
		mode   SpreadMode
		p0, p1 float32
		stages []stage
	}{
		// A full turn is always padded.
		{0, 360, SpreadModeRepeat, 1, 0, []stage{stageXYToUnitAngle, stagePadX1, stageEvenlySpaced2StopGradient}},
		{90, 180, SpreadModeRepeat, 4, -1, []stage{stageXYToUnitAngle, stageApplyConcentricScaleBias,
			stageRepeatX1, stageEvenlySpaced2StopGradient}},
		// Rust only scales when the bias is non-zero too.
		{0, 180, SpreadModePad, 2, 0, []stage{stageXYToUnitAngle, stagePadX1, stageEvenlySpaced2StopGradient}},
	}
	for _, c := range cases {
		s, ok := NewSweepGradient(Pt(5, 5), c.a0, c.a1, shTwoStops(), c.mode, id)
		if !ok || !s.IsOpaque() {
			t.Fatalf("%v..%v: want an opaque gradient", c.a0, c.a1)
		}
		p := shPush(t, s, ColorSpaceLinear)
		shWantStages(t, p, append([]stage{stageSeedShader, stageTransform}, c.stages...)...)
		if p.ctx.transform != TransformFromTranslate(-5, -5) {
			t.Errorf("transform = %v", p.ctx.transform)
		}
		if g := p.ctx.twoPointConicalGradient; g.p0 != c.p0 || g.p1 != c.p1 {
			t.Errorf("%v..%v: ctx = %+v", c.a0, c.a1, g)
		}
	}
}

func shTestPixmap(t *testing.T) PixmapRef {
	t.Helper()
	pm, ok := NewPixmap(4, 2)
	if !ok {
		t.Fatal("NewPixmap")
	}
	return pm.AsRef()
}

func TestPattern(t *testing.T) {
	pm := shTestPixmap(t)

	if NewPattern(pm, SpreadModePad, FilterQualityNearest, 1.5, Identity()).opacity != 1 {
		t.Error("opacity not clamped")
	}

	// Identity: bilinear becomes nearest, no transform stage.
	s := NewPattern(pm, SpreadModeRepeat, FilterQualityBilinear, 0.5, Identity())
	if s.IsOpaque() {
		t.Error("a pattern is never opaque")
	}
	p := shPush(t, s, ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageRepeat, stageGather, stageScale1Float)
	if p.ctx.limitX != (tileCtx{4, 0.25}) || p.ctx.limitY != (tileCtx{2, 0.5}) || p.ctx.currentCoverage != 0.5 {
		t.Errorf("ctx = %+v", p.ctx)
	}

	// A translate is nearest too.
	p = shPush(t, NewPattern(pm, SpreadModeReflect, FilterQualityBicubic, 1, TransformFromTranslate(0.5, 0)), ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageTransform, stageReflect, stageGather)

	p = shPush(t, NewPattern(pm, SpreadModeReflect, FilterQualityBilinear, 1, TransformFromScale(2, 2)), ColorSpaceLinear)
	shWantStages(t, p, stageSeedShader, stageTransform, stageBilinear)
	if p.ctx.sampler != (samplerCtx{SpreadModeReflect, 0.25, 0.5}) || p.ctx.transform != TransformFromScale(0.5, 0.5) {
		t.Errorf("ctx = %+v", p.ctx)
	}

	p = shPush(t, NewPattern(pm, SpreadModePad, FilterQualityBicubic, 1, TransformFromScale(2, 2)), ColorSpaceGamma2)
	shWantStages(t, p, stageSeedShader, stageTransform, stageBicubic, stageClamp0, stageClampA, stageGammaExpand2)
	if p.ctx.sampler != (samplerCtx{SpreadModePad, 0.25, 0.5}) {
		t.Errorf("sampler = %+v", p.ctx.sampler)
	}

	// A non-invertible transform pushes nothing.
	p = newRasterPipelineBuilder()
	if NewPattern(pm, SpreadModePad, FilterQualityNearest, 1, shSingular).pushStages(ColorSpaceLinear, p) || len(p.stages) != 0 {
		t.Error("non-invertible transform: want false and no stages")
	}

	c := s.clone().(*Pattern)
	c.applyOpacity(0.5)
	c.applyOpacity(2)
	c.postTransform(TransformFromScale(2, 2))
	if c.opacity != 0.25 || s.opacity != 0.5 || c.transform != TransformFromScale(2, 2) || s.transform != Identity() {
		t.Errorf("clone: %v %v %v %v", c.opacity, s.opacity, c.transform, s.transform)
	}
	if c.pixmap.Width() != 4 {
		t.Error("clone lost the pixmap")
	}
}

// shSingular has a zero determinant. A zero scale without skew is not enough: Rust inverts it to
// infinities.
var shSingular = TransformFromRow(1, 1, 1, 1, 0, 0)
