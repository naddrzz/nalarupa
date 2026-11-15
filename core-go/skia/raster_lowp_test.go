package skia

import (
	"math"
	"testing"
)

// The lowp half of pipeline/mod.rs blend_tests.
func TestBlendLowp(t *testing.T) {
	tests := []struct {
		name       string
		mode       BlendMode
		r, g, b, a uint8
	}{
		{"clear", BlendModeClear, 0, 0, 0, 0},
		{"destination", BlendModeDestination, 39, 100, 118, 200},
		{"source_over", BlendModeSourceOver, 167, 129, 88, 239},
		{"destination_over", BlendModeDestinationOver, 73, 122, 130, 239},
		{"source_in", BlendModeSourceIn, 122, 78, 42, 141},
		{"destination_in", BlendModeDestinationIn, 28, 71, 83, 141},
		{"source_out", BlendModeSourceOut, 34, 22, 12, 39},
		{"destination_out", BlendModeDestinationOut, 12, 30, 35, 59},
		{"source_atop", BlendModeSourceAtop, 133, 107, 76, 200},
		{"destination_atop", BlendModeDestinationAtop, 61, 92, 95, 180},
		{"xor", BlendModeXor, 45, 51, 46, 98},
		{"plus", BlendModePlus, 194, 199, 171, 255},
		{"modulate", BlendModeModulate, 24, 39, 25, 141},
		{"screen", BlendModeScreen, 170, 160, 146, 239},
		{"overlay", BlendModeOverlay, 92, 128, 106, 239},
		{"darken", BlendModeDarken, 72, 121, 88, 239},
		{"lighten", BlendModeLighten, 166, 128, 129, 239},
		{"hard_light", BlendModeHardLight, 154, 128, 95, 239},
		{"difference", BlendModeDifference, 138, 57, 87, 239},
		{"exclusion", BlendModeExclusion, 146, 121, 121, 239},
		{"multiply", BlendModeMultiply, 69, 90, 71, 238},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pixmap, _ := NewPixmap(1, 1)
			pixmap.Fill(ColorFromRGBA8(50, 127, 150, 200))

			s, ok := tt.mode.toStage()
			if !ok {
				t.Fatal("no stage")
			}
			b := newRasterPipelineBuilder()
			b.setForceHQPipeline(false)
			b.pushUniformColor(ColorFromRGBA8(220, 140, 75, 180).Premultiply())
			b.push(stageLoadDestination)
			b.push(s)
			b.push(stageStore)
			p := b.compile()
			if !p.lowp {
				t.Fatal("pipeline is not lowp")
			}
			rect := pixmap.Size().toScreenIntRect(0, 0)
			sub := pixmap.AsMut().asSubpixmap()
			p.run(rect, aaMaskCtx{}, maskCtx{}, dummyPixmapSrc, &sub)

			got, _ := pixmap.Pixel(0, 0)
			want, _ := PremultipliedColorU8FromRGBA(tt.r, tt.g, tt.b, tt.a)
			if got != want {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

// Every stage with a lowp implementation stays lowp; the rest fall back to highp.
func TestLowpStageTable(t *testing.T) {
	for _, s := range []stage{stageClamp0, stageClampA, stageGather, stageColorBurn, stageColorDodge,
		stageSoftLight, stageHue, stageSaturation, stageColor, stageLuminosity, stageReflect, stageRepeat,
		stageBilinear, stageBicubic, stageXYToUnitAngle, stageGammaCompressSrgb} {
		if lowpStages[s] != nil {
			t.Errorf("stage %d has a lowp implementation", s)
		}
	}
	n := 0
	for _, f := range lowpStages {
		if f != nil {
			n++
		}
	}
	if n != 43 {
		t.Errorf("%d lowp stages, want 43", n)
	}
}

// Full 16-pixel runs and the tail both go through source-over and store.
func TestLowpSourceOverRowWithTail(t *testing.T) {
	pixmap, _ := NewPixmap(21, 2)
	pixmap.Fill(ColorFromRGBA8(50, 127, 150, 200))

	b := newRasterPipelineBuilder()
	b.pushUniformColor(ColorFromRGBA8(220, 140, 75, 180).Premultiply())
	b.push(stageSourceOverRgba)
	p := b.compile()
	sub := pixmap.AsMut().asSubpixmap()
	rect, _ := screenIntRectFromXYWH(1, 1, 20, 1)
	p.run(rect, aaMaskCtx{}, maskCtx{}, dummyPixmapSrc, &sub)

	want, _ := PremultipliedColorU8FromRGBA(167, 129, 88, 239)
	bg, _ := PremultipliedColorU8FromRGBA(39, 100, 118, 200)
	for y := uint32(0); y < 2; y++ {
		for x := uint32(0); x < 21; x++ {
			got, _ := pixmap.Pixel(x, y)
			exp := bg
			if y == 1 && x >= 1 {
				exp = want
			}
			if got != exp {
				t.Fatalf("pixel (%d,%d) = %v, want %v", x, y, got, exp)
			}
		}
	}
}

// Coverage drawn into a mask goes through the U8 stages.
func TestLowpMaskU8Stages(t *testing.T) {
	m, _ := NewMask(20, 3)
	sub := m.asSubpixmap()
	bl := newRasterPipelineBlitterMask(&sub)

	// lerp_1_float on a 17-pixel run (one full run and a tail) over a zero mask.
	aa := []uint8{128, 0, 0}
	runs := make([]uint16, 20)
	runs[0] = 17
	bl.blitAntiH(1, 0, aa, runs)
	for x := range 20 {
		want := uint8(0)
		if x >= 1 && x < 18 {
			want = 128
		}
		if got := m.data[x]; got != want {
			t.Fatalf("row 0 x=%d: %d, want %d", x, got, want)
		}
	}

	// lerp_u8 over existing coverage: (100*(255-200) + 255*200 + 255) >> 8.
	copy(m.data[20:], []byte{0, 100, 100})
	bl.blitAntiH2(1, 1, 200, 50)
	if got := m.data[20:24]; got[0] != 0 || got[1] != 221 || got[2] != 130 || got[3] != 0 {
		t.Errorf("row 1: %v, want [0 221 130 0]", got)
	}

	// blit_v: rows sharing one coverage value.
	bl.blitV(5, 1, 2, 255)
	if m.data[20+5] != 255 || m.data[40+5] != 255 {
		t.Errorf("blitV: %d %d", m.data[25], m.data[45])
	}
}

// mask_u8 ends the run when the clip mask is all zero, so nothing is stored.
func TestLowpMaskU8StopsOnEmptyMask(t *testing.T) {
	pixmap, _ := NewPixmap(4, 1)
	pixmap.Fill(ColorFromRGBA8(10, 20, 30, 40))
	before := append([]byte(nil), pixmap.Data()...)

	b := newRasterPipelineBuilder()
	b.pushUniformColor(ColorFromRGBA8(220, 140, 75, 180).Premultiply())
	b.push(stageMaskU8)
	b.push(stageStore)
	p := b.compile()
	sub := pixmap.AsMut().asSubpixmap()
	rect, _ := screenIntRectFromXYWH(0, 0, 4, 1)
	p.run(rect, aaMaskCtx{}, maskCtx{data: make([]byte, 4), realWidth: 4}, dummyPixmapSrc, &sub)
	if string(pixmap.Data()) != string(before) {
		t.Errorf("pixels changed under an empty mask")
	}

	mask := []byte{255, 0, 255, 0}
	p.run(rect, aaMaskCtx{}, maskCtx{data: mask, realWidth: 4}, dummyPixmapSrc, &sub)
	if pixmap.Data()[3] != 180 || pixmap.Data()[7] != 0 {
		t.Errorf("masked store: %v", pixmap.Data())
	}
}

func TestLowpSplitJoin(t *testing.T) {
	var v f32x16
	for i := range v {
		v[i] = float32(i)*1.25 - 3
	}
	var lo, hi u16x16
	lowpSplit(&v, &lo, &hi)
	if lo[0] != uint16(math.Float32bits(v[0])) || lo[1] != uint16(math.Float32bits(v[0])>>16) ||
		hi[0] != uint16(math.Float32bits(v[8])) {
		t.Fatal("split layout")
	}
	if lowpJoin(&lo, &hi) != v {
		t.Fatal("join is not the inverse of split")
	}
}

// f32x16 round/floor emulate the SSE2 fallback: half to even, -0 kept, NaN and inf passed through.
func TestF32x16RoundFloor(t *testing.T) {
	in := []float32{0.5, 1.5, 2.5, -0.5, -1.5, 0.49999997, -0.3, 1e10, -7.7, 3.2, 8388607.5}
	wantRound := []float32{0, 2, 2, 0, -2, 0, 0, 1e10, -8, 3, 8388608}
	wantFloor := []float32{0, 1, 2, -1, -2, 0, -1, 1e10, -8, 3, 8388607}
	var v f32x16
	copy(v[:], in)
	v[14] = float32(math.NaN())
	v[15] = float32(math.Inf(-1))
	r, f := v.round(), v.floor()
	for i := range in {
		if r[i] != wantRound[i] {
			t.Errorf("round(%v) = %v, want %v", in[i], r[i], wantRound[i])
		}
		if f[i] != wantFloor[i] {
			t.Errorf("floor(%v) = %v, want %v", in[i], f[i], wantFloor[i])
		}
	}
	if !math.Signbit(float64(r[6])) {
		t.Error("round lost the sign of a negative zero result")
	}
	if !isNaN(r[14]) || !isNaN(f[14]) || !math.IsInf(float64(f[15]), -1) {
		t.Error("NaN/inf not passed through")
	}

	n := f32x16{float32(math.NaN()), -1, 2, 0.5}.normalize()
	if n[0] != 0 || n[1] != 0 || n[2] != 1 || n[3] != 0.5 {
		t.Errorf("normalize: %v", n[:4])
	}
	var d u16x16
	f32x16{float32(math.NaN()), -1, 70000, 254.9}.saveToU16x16(&d)
	if d[0] != 0 || d[1] != 0 || d[2] != 65535 || d[3] != 254 {
		t.Errorf("saveToU16x16: %v", d[:4])
	}
}
