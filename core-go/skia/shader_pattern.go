package skia

import "math"

// The pattern shader (shaders/pattern.rs), essentially SkImageShader without mipmaps.

// FilterQuality says how much filtering is done when transforming images.
type FilterQuality uint8

const (
	FilterQualityNearest  FilterQuality = iota // nearest neighbor: low quality, but fastest
	FilterQualityBilinear                      // bilinear
	FilterQualityBicubic                       // bicubic: high quality, but slow
)

// PixmapPaint says how a pixmap is blended, like Paint for a pixmap.
type PixmapPaint struct {
	Opacity   float32 // in 0..=1, default 1
	BlendMode BlendMode
	Quality   FilterQuality
}

// DefaultPixmapPaint returns opacity 1, SourceOver and nearest filtering.
func DefaultPixmapPaint() PixmapPaint {
	return PixmapPaint{Opacity: 1, BlendMode: BlendModeSourceOver, Quality: FilterQualityNearest}
}

// Pattern is a pattern shader. The blitter reads pixmap as its source.
type Pattern struct {
	pixmap     PixmapRef
	quality    FilterQuality
	spreadMode SpreadMode
	opacity    float32 // NormalizedF32
	transform  Transform
}

// NewPattern creates a pattern shader; opacity is clamped to 0..=1.
func NewPattern(pixmap PixmapRef, spreadMode SpreadMode, quality FilterQuality, opacity float32, transform Transform) *Pattern {
	return &Pattern{
		pixmap:     pixmap,
		spreadMode: spreadMode,
		quality:    quality,
		opacity:    normalizedClamped(opacity),
		transform:  transform,
	}
}

func (s *Pattern) IsOpaque() bool { return false }

func (s *Pattern) pushStages(cs ColorSpace, p *rasterPipelineBuilder) bool {
	// A zero PixmapRef (Rust's IntSize cannot be empty) has nothing to sample.
	if s.pixmap.Width() == 0 || s.pixmap.Height() == 0 {
		return false
	}
	ts, ok := s.transform.Invert()
	if !ok {
		return false
	}

	p.push(stageSeedShader)
	p.pushTransform(ts)

	quality := s.quality
	if ts.IsIdentity() || ts.IsTranslate() {
		quality = FilterQualityNearest
	}
	if quality == FilterQualityBilinear && ts.IsTranslate() {
		if ts.TX == patternTrunc(ts.TX) && ts.TY == patternTrunc(ts.TY) {
			// An integer translate: bilerp == nearest neighbor.
			quality = FilterQualityNearest
		}
	}

	w, h := float32(s.pixmap.Width()), float32(s.pixmap.Height())
	switch quality {
	case FilterQualityNearest:
		p.ctx.limitX = tileCtx{scale: w, invScale: 1 / w}
		p.ctx.limitY = tileCtx{scale: h, invScale: 1 / h}
		switch s.spreadMode {
		case SpreadModePad: // the gather stage clamps
		case SpreadModeRepeat:
			p.push(stageRepeat)
		case SpreadModeReflect:
			p.push(stageReflect)
		}
		p.push(stageGather)
	case FilterQualityBilinear:
		p.ctx.sampler = samplerCtx{spreadMode: s.spreadMode, invWidth: 1 / w, invHeight: 1 / h}
		p.push(stageBilinear)
	case FilterQualityBicubic:
		p.ctx.sampler = samplerCtx{spreadMode: s.spreadMode, invWidth: 1 / w, invHeight: 1 / h}
		p.push(stageBicubic)
		// Bicubic filtering produces out of range values on both sides of [0, 1].
		p.push(stageClamp0)
		p.push(stageClampA)
	}

	// Only Pattern supports a global opacity.
	if s.opacity != 1 {
		p.ctx.currentCoverage = s.opacity
		p.push(stageScale1Float)
	}

	if st, ok := cs.expandStage(); ok {
		// Gamma-expanding before sampling would be better, but there is no stage for it.
		p.push(st)
	}
	return true
}

func (s *Pattern) postTransform(ts Transform) { s.transform = s.transform.PostConcat(ts) }

func (s *Pattern) applyOpacity(opacity float32) {
	// The product of two values in 0..=1 is in 0..=1, so NormalizedF32::new cannot fail.
	s.opacity = float32(s.opacity * bound(opacity, 0, 1))
}

func (s *Pattern) clone() Shader { c := *s; return &c }

func patternTrunc(x float32) float32 { return float32(math.Trunc(float64(x))) }
