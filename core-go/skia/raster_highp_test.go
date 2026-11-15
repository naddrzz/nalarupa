package skia

import (
	"math"
	"testing"
)

// The highp half of blend_tests (pipeline/mod.rs).
func TestBlendHighp(t *testing.T) {
	for _, c := range []struct {
		name       string
		mode       BlendMode
		r, g, b, a uint8
	}{
		{"clear", BlendModeClear, 0, 0, 0, 0},
		// Source is a no-op
		{"destination", BlendModeDestination, 39, 100, 118, 200},
		{"source_over", BlendModeSourceOver, 167, 128, 88, 239},
		{"destination_over", BlendModeDestinationOver, 72, 121, 129, 239},
		{"source_in", BlendModeSourceIn, 122, 78, 42, 141},
		{"destination_in", BlendModeDestinationIn, 28, 71, 83, 141},
		{"source_out", BlendModeSourceOut, 33, 21, 11, 39},
		{"destination_out", BlendModeDestinationOut, 11, 29, 35, 59},
		{"source_atop", BlendModeSourceAtop, 133, 107, 76, 200},
		{"destination_atop", BlendModeDestinationAtop, 61, 92, 95, 180},
		{"xor", BlendModeXor, 45, 51, 46, 98},
		{"plus", BlendModePlus, 194, 199, 171, 255},
		{"modulate", BlendModeModulate, 24, 39, 24, 141},
		{"screen", BlendModeScreen, 171, 160, 146, 239},
		{"overlay", BlendModeOverlay, 92, 128, 106, 239},
		{"darken", BlendModeDarken, 72, 121, 88, 239},
		{"lighten", BlendModeLighten, 167, 128, 129, 239},
		{"color_dodge", BlendModeColorDodge, 186, 192, 164, 239},
		{"color_burn", BlendModeColorBurn, 54, 63, 46, 239},
		{"hard_light", BlendModeHardLight, 155, 128, 95, 239},
		{"soft_light", BlendModeSoftLight, 98, 124, 115, 239},
		{"difference", BlendModeDifference, 139, 58, 88, 239},
		{"exclusion", BlendModeExclusion, 147, 121, 122, 239},
		{"multiply", BlendModeMultiply, 69, 89, 71, 239},
		{"hue", BlendModeHue, 128, 103, 74, 239},
		{"saturation", BlendModeSaturation, 59, 126, 140, 239},
		{"color", BlendModeColor, 139, 100, 60, 239},
		{"luminosity", BlendModeLuminosity, 100, 149, 157, 239},
	} {
		t.Run(c.name, func(t *testing.T) {
			pixmap, _ := NewPixmap(1, 1)
			pixmap.Fill(ColorFromRGBA8(50, 127, 150, 200))

			pixmapSrc, _ := PixmapRefFromBytes([]byte{0, 0, 0, 0}, 1, 1)

			b := newRasterPipelineBuilder()
			b.setForceHQPipeline(true)
			b.pushUniformColor(ColorFromRGBA8(220, 140, 75, 180).Premultiply())
			b.push(stageLoadDestination)
			s, _ := c.mode.toStage()
			b.push(s)
			b.push(stageStore)
			p := b.compile()
			if p.lowp {
				t.Fatal("pipeline is not highp")
			}
			rect := pixmap.Size().toScreenIntRect(0, 0)
			dst := pixmap.AsMut().asSubpixmap()
			p.run(rect, aaMaskCtx{}, maskCtx{}, pixmapSrc, &dst)

			got, _ := pixmap.Pixel(0, 0)
			want, _ := PremultipliedColorU8FromRGBA(c.r, c.g, c.b, c.a)
			if got != want {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

// A run wider than one chunk exercises the full and tail programs, and stores only inside the rect.
func TestHighpChunkAndTail(t *testing.T) {
	pixmap, _ := NewPixmap(12, 1)
	b := newRasterPipelineBuilder()
	b.setForceHQPipeline(true)
	b.pushUniformColor(ColorFromRGBA8(255, 0, 0, 255).Premultiply())
	b.push(stageStore)
	p := b.compile()
	rect, _ := screenIntRectFromXYWH(1, 0, 10, 1)
	dst := pixmap.AsMut().asSubpixmap()
	p.run(rect, aaMaskCtx{}, maskCtx{}, dummyPixmapSrc, &dst)

	for x := uint32(0); x < 12; x++ {
		got, _ := pixmap.Pixel(x, 0)
		want := PremultipliedColorU8{255, 0, 0, 255}
		if x == 0 || x == 11 {
			want = PremultipliedColorU8{}
		}
		if got != want {
			t.Errorf("pixel %d = %v, want %v", x, got, want)
		}
	}
}

// mask_u8 ends the chain for an all-zero mask chunk, so nothing is stored there.
func TestHighpMaskU8Stops(t *testing.T) {
	pixmap, _ := NewPixmap(2, 1)
	b := newRasterPipelineBuilder()
	b.setForceHQPipeline(true)
	b.pushUniformColor(ColorFromRGBA8(0, 0, 255, 255).Premultiply())
	b.push(stageMaskU8)
	b.push(stageStore)
	p := b.compile()
	rect, _ := screenIntRectFromXYWH(0, 0, 2, 1)
	dst := pixmap.AsMut().asSubpixmap()

	p.run(rect, aaMaskCtx{}, maskCtx{data: []byte{0, 0}, realWidth: 2}, dummyPixmapSrc, &dst)
	if got, _ := pixmap.Pixel(0, 0); got != (PremultipliedColorU8{}) {
		t.Fatalf("zero mask stored %v", got)
	}

	p.run(rect, aaMaskCtx{}, maskCtx{data: []byte{0, 255}, realWidth: 2}, dummyPixmapSrc, &dst)
	// The zero lane stores transparent black; the full lane stores the color.
	if got, _ := pixmap.Pixel(1, 0); got != (PremultipliedColorU8{0, 0, 255, 255}) {
		t.Fatalf("full mask stored %v", got)
	}
}

func TestWideSSESemantics(t *testing.T) {
	nan := float32(math.NaN())
	if got := cvtps(2.5); got != 2 {
		t.Errorf("cvtps(2.5) = %d, want 2", got)
	}
	if got := cvtps(-3.5); got != -4 {
		t.Errorf("cvtps(-3.5) = %d, want -4", got)
	}
	for _, v := range []float32{nan, 3e9, -3e9} {
		if cvtps(v) != math.MinInt32 || cvttps(v) != math.MinInt32 {
			t.Errorf("conversion of %v is not i32::MIN", v)
		}
	}
	if got := cvttps(-2.7); got != -2 {
		t.Errorf("cvttps(-2.7) = %d, want -2", got)
	}
	if sseMin(nan, 1) != 1 || sseMax(nan, 1) != 1 || !isNaN(sseMin(1, nan)) || !isNaN(sseMax(1, nan)) {
		t.Error("min/max do not follow SSE operand order")
	}
	for _, c := range []struct{ in, want float32 }{
		{-1.5, -2}, {-1, -1}, {0.5, 0}, {2.99, 2}, {-0.25, -1},
	} {
		if got := floorLane(c.in); got != c.want {
			t.Errorf("floor(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want float32 }{
		{0.5, 0}, {1.5, 2}, {2.5, 2}, {-2.5, -2}, {0.49, 0}, {-0.7, -1}, {1e10, 1e10},
	} {
		if got := roundLane(c.in); got != c.want {
			t.Errorf("round(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	if !isNaN(roundLane(nan)) {
		t.Error("round(NaN) is not NaN")
	}
	if got := (f32x8{0}).neg()[0]; math.Float32bits(got) != 0 {
		t.Errorf("neg(+0) = %v, want +0", got)
	}
	m := f32x8Splat(1).cmpNe(f32x8{nan})
	if math.Float32bits(m[0]) != laneTrue || math.Float32bits(m[1]) != laneTrue {
		t.Error("cmpNe is not true for NaN and different lanes")
	}
	p := f32x8{0, 1, 0.5, 0.25}.powf(2.2)
	if p[0] != 0 || p[1] != 1 || abs32(p[2]-0.2176) > 1e-3 || abs32(p[3]-0.0474) > 1e-3 {
		t.Errorf("powf = %v", p)
	}
}

func TestHighpImplementsEveryStage(t *testing.T) {
	for s, f := range highpStages {
		if f == nil {
			t.Errorf("stage %d has no highp implementation", s)
		}
	}
}
