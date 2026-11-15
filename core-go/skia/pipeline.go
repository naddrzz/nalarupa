package skia

// The raster pipeline (pipeline/mod.rs). A pipeline is a list of stages sharing one context. Every stage
// has a high precision (float32, 8 lanes) implementation in pipeline_highp.go; some have a low precision
// (u16, 16 lanes) one in pipeline_lowp.go. Compiling picks lowp when every stage has a lowp
// implementation and the high precision pipeline is not forced, as Rust does.
//
// Rust chains stages by having each one call the next; here start runs the stage functions in order, so
// a stage function does its work and returns.

// stage identifies a pipeline stage. The order matches Rust's Stage enum, which indexes the stage tables.
type stage uint8

const (
	stageMoveSourceToDestination stage = iota
	stageMoveDestinationToSource
	stageClamp0
	stageClampA
	stagePremultiply
	stageUniformColor
	stageSeedShader
	stageLoadDestination
	stageStore
	stageLoadDestinationU8
	stageStoreU8
	stageGather
	stageLoadMaskU8
	stageMaskU8
	stageScaleU8
	stageLerpU8
	stageScale1Float
	stageLerp1Float
	stageDestinationAtop
	stageDestinationIn
	stageDestinationOut
	stageDestinationOver
	stageSourceAtop
	stageSourceIn
	stageSourceOut
	stageSourceOver
	stageClear
	stageModulate
	stageMultiply
	stagePlus
	stageScreen
	stageXor
	stageColorBurn
	stageColorDodge
	stageDarken
	stageDifference
	stageExclusion
	stageHardLight
	stageLighten
	stageOverlay
	stageSoftLight
	stageHue
	stageSaturation
	stageColor
	stageLuminosity
	stageSourceOverRgba
	stageTransform
	stageReflect
	stageRepeat
	stageBilinear
	stageBicubic
	stagePadX1
	stageReflectX1
	stageRepeatX1
	stageGradient
	stageEvenlySpaced2StopGradient
	stageXYToUnitAngle
	stageXYToRadius
	stageXYTo2PtConicalFocalOnCircle
	stageXYTo2PtConicalWellBehaved
	stageXYTo2PtConicalSmaller
	stageXYTo2PtConicalGreater
	stageXYTo2PtConicalStrip
	stageMask2PtConicalNan
	stageMask2PtConicalDegenerates
	stageApplyVectorMask
	stageAlter2PtConicalCompensateFocal
	stageAlter2PtConicalUnswap
	stageNegateX
	stageApplyConcentricScaleBias
	stageGammaExpand2
	stageGammaExpandDestination2
	stageGammaCompress2
	stageGammaExpand22
	stageGammaExpandDestination22
	stageGammaCompress22
	stageGammaExpandSrgb
	stageGammaExpandDestinationSrgb
	stageGammaCompressSrgb

	stagesCount = int(stageGammaCompressSrgb) + 1
)

const maxStages = 32 // ArrayVec capacity; pushing more panics, as in Rust

// aaMaskCtx is the up to two coverage values of a blitter mask (AAMaskCtx).
type aaMaskCtx struct {
	pixels [2]uint8
	stride uint32 // can be zero
	shift  int    // the mask offset/position in pixmap coordinates
}

// copyAtXY returns the coverage at (dx, dy) for a run of tail pixels; only three cases exist.
func (c aaMaskCtx) copyAtXY(dx, dy, tail int) [2]uint8 {
	offset := (int(c.stride)*dy + dx) - c.shift
	switch {
	case offset == 0 && tail == 1:
		return [2]uint8{c.pixels[0], 0}
	case offset == 0 && tail == 2:
		return [2]uint8{c.pixels[0], c.pixels[1]}
	case offset == 1 && tail == 1:
		return [2]uint8{c.pixels[1], 0}
	}
	return [2]uint8{} // unreachable
}

// maskCtx is a clip mask window (MaskCtx); data is nil when there is no mask.
type maskCtx struct {
	data      []byte
	realWidth uint32
}

func (c maskCtx) offset(dx, dy int) int { return int(c.realWidth)*dy + dx }

// pipelineContext is the context shared by all stages (Context).
type pipelineContext struct {
	currentCoverage           float32
	sampler                   samplerCtx
	uniformColor              uniformColorCtx
	evenlySpaced2StopGradient evenlySpaced2StopGradientCtx
	gradient                  gradientCtx
	twoPointConicalGradient   twoPointConicalGradientCtx
	limitX                    tileCtx
	limitY                    tileCtx
	transform                 Transform
}

type samplerCtx struct {
	spreadMode SpreadMode
	invWidth   float32
	invHeight  float32
}

type uniformColorCtx struct {
	r, g, b, a float32
	rgba       [4]uint16 // 0..=255 in 16-bit lanes
}

// gradientColor is an unpremultiplied RGBA color with components of any value.
type gradientColor struct{ r, g, b, a float32 }

func gradientColorFromColor(c Color) gradientColor { return gradientColor{c.r, c.g, c.b, c.a} }

type evenlySpaced2StopGradientCtx struct {
	factor gradientColor
	bias   gradientColor
}

// gradientCtx: len is the real number of colors; factors and biases hold at least 16 entries (the lowp
// stage width), zero past len.
type gradientCtx struct {
	len     int
	factors []gradientColor
	biases  []gradientColor
	tValues []float32 // NormalizedF32
}

func (c *gradientCtx) pushConstColor(color gradientColor) {
	c.factors = append(c.factors, gradientColor{})
	c.biases = append(c.biases, color)
}

// twoPointConicalGradientCtx is used only by highp.
type twoPointConicalGradientCtx struct {
	mask [8]uint32 // u32x8
	p0   float32
	p1   float32
}

type tileCtx struct {
	scale    float32
	invScale float32 // cache of 1/scale
}

type rasterPipelineBuilder struct {
	stages          []stage
	forceHQPipeline bool
	ctx             pipelineContext
}

func newRasterPipelineBuilder() *rasterPipelineBuilder {
	return &rasterPipelineBuilder{ctx: pipelineContext{transform: Identity()}}
}

func (b *rasterPipelineBuilder) setForceHQPipeline(hq bool) { b.forceHQPipeline = hq }

func (b *rasterPipelineBuilder) push(s stage) {
	if len(b.stages) == maxStages {
		panic("skia: too many pipeline stages")
	}
	b.stages = append(b.stages, s)
}

func (b *rasterPipelineBuilder) pushTransform(ts Transform) {
	if ts.IsFinite() && !ts.IsIdentity() {
		b.push(stageTransform)
		b.ctx.transform = ts
	}
}

func (b *rasterPipelineBuilder) pushUniformColor(c PremultipliedColor) {
	r, g, bl, a := c.Red(), c.Green(), c.Blue(), c.Alpha()
	rgba := [4]uint16{
		castU16(float32(r*255) + 0.5),
		castU16(float32(g*255) + 0.5),
		castU16(float32(bl*255) + 0.5),
		castU16(float32(a*255) + 0.5),
	}
	b.push(stageUniformColor)
	b.ctx.uniformColor = uniformColorCtx{r: r, g: g, b: bl, a: a, rgba: rgba}
}

// rasterPipeline is a compiled pipeline: the stage functions for full runs of the stage width and for
// the tail, which differ only in their load and store stages.
type rasterPipeline struct {
	lowp     bool
	highFns  []highpStageFn
	highTail []highpStageFn
	lowFns   []lowpStageFn
	lowTail  []lowpStageFn
	ctx      pipelineContext
}

func (b *rasterPipelineBuilder) compile() *rasterPipeline {
	if len(b.stages) == 0 {
		return &rasterPipeline{ctx: pipelineContext{transform: Identity()}}
	}

	isLowpCompatible := true
	for _, s := range b.stages {
		if lowpStages[s] == nil {
			isLowpCompatible = false
			break
		}
	}

	p := &rasterPipeline{ctx: b.ctx}
	if b.forceHQPipeline || !isLowpCompatible {
		for _, s := range b.stages {
			f := highpStages[s]
			p.highFns = append(p.highFns, f)
			if t := highpTailStages[s]; t != nil {
				f = t
			}
			p.highTail = append(p.highTail, f)
		}
	} else {
		p.lowp = true
		for _, s := range b.stages {
			f := lowpStages[s]
			p.lowFns = append(p.lowFns, f)
			if t := lowpTailStages[s]; t != nil {
				f = t
			}
			p.lowTail = append(p.lowTail, f)
		}
	}
	return p
}

func (p *rasterPipeline) run(rect screenIntRect, aaMask aaMaskCtx, mask maskCtx, pixmapSrc PixmapRef, pixmapDst *subPixmapMut) {
	if p.lowp {
		// lowp does not support patterns, so it gets no source pixmap.
		lowpStart(p.lowFns, p.lowTail, rect, aaMask, mask, &p.ctx, pixmapDst)
		return
	}
	highpStart(p.highFns, p.highTail, rect, aaMask, mask, &p.ctx, pixmapSrc, pixmapDst)
}

// dummyPixmapSrc is the 1x1 transparent source pixmap Rust passes when there is no pattern.
var dummyPixmapSrc = PixmapRef{data: make([]byte, 4), size: IntSize{1, 1}}
