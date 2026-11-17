package skia

import "math"

// The high precision raster pipeline (pipeline/highp.rs): every stage, on 8 float32 lanes. Unlike
// Rust, where each stage calls the next, highpStart runs a chunk's stage list in a loop; a stage that
// ends the chain early in Rust (it returns without calling next_stage) sets stop instead.

const highpStageWidth = 8

type highpStageFn func(p *highpPipeline)

type highpPipeline struct {
	stop           bool
	pixmapSrc      PixmapRef
	pixmapDst      *subPixmapMut
	ctx            *pipelineContext
	maskCtx        maskCtx
	aaMaskCtx      aaMaskCtx
	r, g, b, a     f32x8
	dr, dg, db, da f32x8
	tail           int
	dx, dy         int
}

// highpStages is indexed by stage, in the order of Rust's STAGES.
var highpStages = [stagesCount]highpStageFn{
	stageMoveSourceToDestination:        highpMoveSourceToDestination,
	stageMoveDestinationToSource:        highpMoveDestinationToSource,
	stageClamp0:                         highpClamp0,
	stageClampA:                         highpClampA,
	stagePremultiply:                    highpPremultiply,
	stageUniformColor:                   highpUniformColor,
	stageSeedShader:                     highpSeedShader,
	stageLoadDestination:                highpLoadDst,
	stageStore:                          highpStore,
	stageLoadDestinationU8:              highpLoadDstU8,
	stageStoreU8:                        highpStoreU8,
	stageGather:                         highpGather,
	stageLoadMaskU8:                     highpLoadMaskU8,
	stageMaskU8:                         highpMaskU8,
	stageScaleU8:                        highpScaleU8,
	stageLerpU8:                         highpLerpU8,
	stageScale1Float:                    highpScale1Float,
	stageLerp1Float:                     highpLerp1Float,
	stageDestinationAtop:                highpDestinationAtop,
	stageDestinationIn:                  highpDestinationIn,
	stageDestinationOut:                 highpDestinationOut,
	stageDestinationOver:                highpDestinationOver,
	stageSourceAtop:                     highpSourceAtop,
	stageSourceIn:                       highpSourceIn,
	stageSourceOut:                      highpSourceOut,
	stageSourceOver:                     highpSourceOver,
	stageClear:                          highpClear,
	stageModulate:                       highpModulate,
	stageMultiply:                       highpMultiply,
	stagePlus:                           highpPlus,
	stageScreen:                         highpScreen,
	stageXor:                            highpXor,
	stageColorBurn:                      highpColorBurn,
	stageColorDodge:                     highpColorDodge,
	stageDarken:                         highpDarken,
	stageDifference:                     highpDifference,
	stageExclusion:                      highpExclusion,
	stageHardLight:                      highpHardLight,
	stageLighten:                        highpLighten,
	stageOverlay:                        highpOverlay,
	stageSoftLight:                      highpSoftLight,
	stageHue:                            highpHue,
	stageSaturation:                     highpSaturation,
	stageColor:                          highpColor,
	stageLuminosity:                     highpLuminosity,
	stageSourceOverRgba:                 highpSourceOverRgba,
	stageTransform:                      highpTransform,
	stageReflect:                        highpReflect,
	stageRepeat:                         highpRepeat,
	stageBilinear:                       highpBilinear,
	stageBicubic:                        highpBicubic,
	stagePadX1:                          highpPadX1,
	stageReflectX1:                      highpReflectX1,
	stageRepeatX1:                       highpRepeatX1,
	stageGradient:                       highpGradient,
	stageEvenlySpaced2StopGradient:      highpEvenlySpaced2StopGradient,
	stageXYToUnitAngle:                  highpXYToUnitAngle,
	stageXYToRadius:                     highpXYToRadius,
	stageXYTo2PtConicalFocalOnCircle:    highpXYTo2PtConicalFocalOnCircle,
	stageXYTo2PtConicalWellBehaved:      highpXYTo2PtConicalWellBehaved,
	stageXYTo2PtConicalSmaller:          highpXYTo2PtConicalSmaller,
	stageXYTo2PtConicalGreater:          highpXYTo2PtConicalGreater,
	stageXYTo2PtConicalStrip:            highpXYTo2PtConicalStrip,
	stageMask2PtConicalNan:              highpMask2PtConicalNan,
	stageMask2PtConicalDegenerates:      highpMask2PtConicalDegenerates,
	stageApplyVectorMask:                highpApplyVectorMask,
	stageAlter2PtConicalCompensateFocal: highpAlter2PtConicalCompensateFocal,
	stageAlter2PtConicalUnswap:          highpAlter2PtConicalUnswap,
	stageNegateX:                        highpNegateX,
	stageApplyConcentricScaleBias:       highpApplyConcentricScaleBias,
	stageGammaExpand2:                   highpGammaExpand2,
	stageGammaExpandDestination2:        highpGammaExpandDst2,
	stageGammaCompress2:                 highpGammaCompress2,
	stageGammaExpand22:                  highpGammaExpand22,
	stageGammaExpandDestination22:       highpGammaExpandDst22,
	stageGammaCompress22:                highpGammaCompress22,
	stageGammaExpandSrgb:                highpGammaExpandSrgb,
	stageGammaExpandDestinationSrgb:     highpGammaExpandDstSrgb,
	stageGammaCompressSrgb:              highpGammaCompressSrgb,
}

// highpTailStages replaces, in the tail program, the stages that load or store whole chunks.
var highpTailStages = [stagesCount]highpStageFn{
	stageLoadDestination:   highpLoadDstTail,
	stageStore:             highpStoreTail,
	stageLoadDestinationU8: highpLoadDstU8Tail,
	stageStoreU8:           highpStoreU8Tail,
	stageSourceOverRgba:    highpSourceOverRgbaTail,
}

func highpStart(fns, tail []highpStageFn, rect screenIntRect, aaMask aaMaskCtx, mask maskCtx, ctx *pipelineContext, pixmapSrc PixmapRef, pixmapDst *subPixmapMut) {
	p := highpPipeline{
		pixmapSrc: pixmapSrc,
		pixmapDst: pixmapDst,
		ctx:       ctx,
		maskCtx:   mask,
		aaMaskCtx: aaMask,
	}

	for y := rect.Y(); y < rect.Bottom(); y++ {
		x := int(rect.X())
		end := int(rect.Right())

		for x+highpStageWidth <= end {
			p.dx = x
			p.dy = int(y)
			p.tail = highpStageWidth
			p.run(fns)
			x += highpStageWidth
		}

		if x != end {
			p.dx = x
			p.dy = int(y)
			p.tail = end - x
			p.run(tail)
		}
	}
}

func (p *highpPipeline) run(fns []highpStageFn) {
	p.stop = false
	for _, f := range fns {
		f(p)
		if p.stop {
			return
		}
	}
}

func highpMoveSourceToDestination(p *highpPipeline) {
	p.dr = p.r
	p.dg = p.g
	p.db = p.b
	p.da = p.a
}

func highpPremultiply(p *highpPipeline) {
	p.r = p.r.mul(p.a)
	p.g = p.g.mul(p.a)
	p.b = p.b.mul(p.a)
}

func highpMoveDestinationToSource(p *highpPipeline) {
	p.r = p.dr
	p.g = p.dg
	p.b = p.db
	p.a = p.da
}

func highpClamp0(p *highpPipeline) {
	p.r = p.r.max(f32x8{})
	p.g = p.g.max(f32x8{})
	p.b = p.b.max(f32x8{})
	p.a = p.a.max(f32x8{})
}

func highpClampA(p *highpPipeline) {
	one := f32x8Splat(1.0)
	p.r = p.r.min(one)
	p.g = p.g.min(one)
	p.b = p.b.min(one)
	p.a = p.a.min(one)
}

func highpUniformColor(p *highpPipeline) {
	ctx := &p.ctx.uniformColor
	p.r = f32x8Splat(ctx.r)
	p.g = f32x8Splat(ctx.g)
	p.b = f32x8Splat(ctx.b)
	p.a = f32x8Splat(ctx.a)
}

func highpSeedShader(p *highpPipeline) {
	iota := f32x8{0.5, 1.5, 2.5, 3.5, 4.5, 5.5, 6.5, 7.5}

	p.r = f32x8Splat(float32(p.dx)).add(iota)
	p.g = f32x8Splat(float32(p.dy) + 0.5)
	p.b = f32x8Splat(1.0)
	p.a = f32x8{}

	p.dr = f32x8{}
	p.dg = f32x8{}
	p.db = f32x8{}
	p.da = f32x8{}
}

// dstAt returns the destination bytes from (dx, dy) on.
func (p *highpPipeline) dstAt() []byte {
	return p.pixmapDst.data[p.pixmapDst.offset(p.dx, p.dy)*BytesPerPixel:]
}

// dst8At returns the 8 destination pixels at (dx, dy); it panics past the data, as Rust does.
func (p *highpPipeline) dst8At() []byte {
	return p.dstAt()[:highpStageWidth*BytesPerPixel]
}

func highpLoadDst(p *highpPipeline) {
	highpLoad8888(p.dst8At(), &p.dr, &p.dg, &p.db, &p.da)
}

func highpLoadDstTail(p *highpPipeline) {
	highpLoad8888Tail(p.tail, p.dstAt(), &p.dr, &p.dg, &p.db, &p.da)
}

func highpStore(p *highpPipeline) {
	highpStore8888(&p.r, &p.g, &p.b, &p.a, p.dst8At())
}

func highpStoreTail(p *highpPipeline) {
	highpStore8888Tail(&p.r, &p.g, &p.b, &p.a, p.tail, p.dstAt())
}

// Mask and A8 pixmaps are always handled by lowp: these stages are unreachable and, as in Rust, end
// the chain.
func highpLoadDstU8(p *highpPipeline)     { p.stop = true }
func highpLoadDstU8Tail(p *highpPipeline) { p.stop = true }
func highpStoreU8(p *highpPipeline)       { p.stop = true }
func highpStoreU8Tail(p *highpPipeline)   { p.stop = true }
func highpLoadMaskU8(p *highpPipeline)    { p.stop = true }

func highpGather(p *highpPipeline) {
	ix := highpGatherIx(p.pixmapSrc, p.r, p.g)
	pixels := highpGatherPixels(p.pixmapSrc, ix)
	highpLoad8888(pixels[:], &p.r, &p.g, &p.b, &p.a)
}

// highpGatherPixels is PixmapRef::gather: the 8 pixels at the given indices.
func highpGatherPixels(pixmap PixmapRef, ix u32x8) (out [highpStageWidth * BytesPerPixel]byte) {
	for i, idx := range ix {
		o := int(idx) * BytesPerPixel
		copy(out[i*BytesPerPixel:(i+1)*BytesPerPixel], pixmap.data[o:o+BytesPerPixel])
	}
	return
}

func highpGatherIx(pixmap PixmapRef, x, y f32x8) u32x8 {
	// Exclusive -> inclusive.
	w := highpUlpSub(float32(pixmap.Width()))
	h := highpUlpSub(float32(pixmap.Height()))
	x = x.max(f32x8{}).min(f32x8Splat(w))
	y = y.max(f32x8{}).min(f32x8Splat(h))

	return y.truncInt().mul(i32x8Splat(int32(pixmap.Width()))).add(x.truncInt()).toU32x8Bitcast()
}

// highpUlpSub is somewhat similar to v - f32::EPSILON.
func highpUlpSub(v float32) float32 {
	return math.Float32frombits(math.Float32bits(v) - 1)
}

func highpMaskU8(p *highpPipeline) {
	offset := p.maskCtx.offset(p.dx, p.dy)
	var c f32x8
	for i := 0; i < p.tail; i++ {
		c[i] = float32(p.maskCtx.data[offset+i])
	}
	c = c.div(f32x8Splat(255.0))

	if c.eq(f32x8{}) {
		p.stop = true // Rust returns without calling the next stage
		return
	}

	p.r = p.r.mul(c)
	p.g = p.g.mul(c)
	p.b = p.b.mul(c)
	p.a = p.a.mul(c)
}

func highpScaleU8(p *highpPipeline) {
	// Load u8xTail and cast it to f32x8.
	data := p.aaMaskCtx.copyAtXY(p.dx, p.dy, p.tail)
	c := f32x8{float32(data[0]), float32(data[1])}
	c = c.div(f32x8Splat(255.0))

	p.r = p.r.mul(c)
	p.g = p.g.mul(c)
	p.b = p.b.mul(c)
	p.a = p.a.mul(c)
}

func highpLerpU8(p *highpPipeline) {
	// Load u8xTail and cast it to f32x8.
	data := p.aaMaskCtx.copyAtXY(p.dx, p.dy, p.tail)
	c := f32x8{float32(data[0]), float32(data[1])}
	c = c.div(f32x8Splat(255.0))

	p.r = highpLerp(p.dr, p.r, c)
	p.g = highpLerp(p.dg, p.g, c)
	p.b = highpLerp(p.db, p.b, c)
	p.a = highpLerp(p.da, p.a, c)
}

func highpScale1Float(p *highpPipeline) {
	c := f32x8Splat(p.ctx.currentCoverage)
	p.r = p.r.mul(c)
	p.g = p.g.mul(c)
	p.b = p.b.mul(c)
	p.a = p.a.mul(c)
}

func highpLerp1Float(p *highpPipeline) {
	c := f32x8Splat(p.ctx.currentCoverage)
	p.r = highpLerp(p.dr, p.r, c)
	p.g = highpLerp(p.dg, p.g, c)
	p.b = highpLerp(p.db, p.b, c)
	p.a = highpLerp(p.da, p.a, c)
}

type highpBlendKernel func(s, d, sa, da f32x8) f32x8

// highpBlend is blend_fn!: the same kernel for color and alpha.
func highpBlend(p *highpPipeline, f highpBlendKernel) {
	p.r = f(p.r, p.dr, p.a, p.da)
	p.g = f(p.g, p.dg, p.a, p.da)
	p.b = f(p.b, p.db, p.a, p.da)
	p.a = f(p.a, p.da, p.a, p.da)
}

// highpBlend2 is blend_fn2!: the kernel for color, source_over for alpha.
func highpBlend2(p *highpPipeline, f highpBlendKernel) {
	p.r = f(p.r, p.dr, p.a, p.da)
	p.g = f(p.g, p.dg, p.a, p.da)
	p.b = f(p.b, p.db, p.a, p.da)
	p.a = highpMad(p.da, highpInv(p.a), p.a)
}

func highpClear(p *highpPipeline) {
	highpBlend(p, func(_, _, _, _ f32x8) f32x8 { return f32x8{} })
}

func highpSourceAtop(p *highpPipeline) {
	highpBlend(p, func(s, d, sa, da f32x8) f32x8 { return s.mul(da).add(d.mul(highpInv(sa))) })
}

func highpDestinationAtop(p *highpPipeline) {
	highpBlend(p, func(s, d, sa, da f32x8) f32x8 { return d.mul(sa).add(s.mul(highpInv(da))) })
}

func highpSourceIn(p *highpPipeline) {
	highpBlend(p, func(s, _, _, da f32x8) f32x8 { return s.mul(da) })
}

func highpDestinationIn(p *highpPipeline) {
	highpBlend(p, func(_, d, sa, _ f32x8) f32x8 { return d.mul(sa) })
}

func highpSourceOut(p *highpPipeline) {
	highpBlend(p, func(s, _, _, da f32x8) f32x8 { return s.mul(highpInv(da)) })
}

func highpDestinationOut(p *highpPipeline) {
	highpBlend(p, func(_, d, sa, _ f32x8) f32x8 { return d.mul(highpInv(sa)) })
}

func highpSourceOver(p *highpPipeline) {
	highpBlend(p, func(s, d, sa, _ f32x8) f32x8 { return highpMad(d, highpInv(sa), s) })
}

func highpDestinationOver(p *highpPipeline) {
	highpBlend(p, func(s, d, _, da f32x8) f32x8 { return highpMad(s, highpInv(da), d) })
}

func highpModulate(p *highpPipeline) {
	highpBlend(p, func(s, d, _, _ f32x8) f32x8 { return s.mul(d) })
}

func highpMultiply(p *highpPipeline) {
	highpBlend(p, func(s, d, sa, da f32x8) f32x8 {
		return s.mul(highpInv(da)).add(d.mul(highpInv(sa))).add(s.mul(d))
	})
}

func highpScreen(p *highpPipeline) {
	highpBlend(p, func(s, d, _, _ f32x8) f32x8 { return s.add(d).sub(s.mul(d)) })
}

func highpXor(p *highpPipeline) {
	highpBlend(p, func(s, d, sa, da f32x8) f32x8 { return s.mul(highpInv(da)).add(d.mul(highpInv(sa))) })
}

func highpPlus(p *highpPipeline) {
	highpBlend(p, func(s, d, _, _ f32x8) f32x8 { return s.add(d).min(f32x8Splat(1.0)) })
}

func highpDarken(p *highpPipeline) {
	highpBlend2(p, func(s, d, sa, da f32x8) f32x8 { return s.add(d).sub(s.mul(da).max(d.mul(sa))) })
}

func highpLighten(p *highpPipeline) {
	highpBlend2(p, func(s, d, sa, da f32x8) f32x8 { return s.add(d).sub(s.mul(da).min(d.mul(sa))) })
}

func highpDifference(p *highpPipeline) {
	highpBlend2(p, func(s, d, sa, da f32x8) f32x8 {
		return s.add(d).sub(highpTwo(s.mul(da).min(d.mul(sa))))
	})
}

func highpExclusion(p *highpPipeline) {
	highpBlend2(p, func(s, d, _, _ f32x8) f32x8 { return s.add(d).sub(highpTwo(s.mul(d))) })
}

// color_burn and color_dodge use recip_fast, which is exact here (see f32x8.recipFast).
func highpColorBurn(p *highpPipeline) {
	highpBlend2(p, func(s, d, sa, da f32x8) f32x8 {
		return d.cmpEq(da).blend(
			d.add(s.mul(highpInv(da))),
			s.cmpEq(f32x8{}).blend(
				d.mul(highpInv(sa)),
				sa.mul(da.sub(da.min(da.sub(d).mul(sa).mul(s.recipFast())))).
					add(s.mul(highpInv(da))).add(d.mul(highpInv(sa))),
			),
		)
	})
}

func highpColorDodge(p *highpPipeline) {
	highpBlend2(p, func(s, d, sa, da f32x8) f32x8 {
		return d.cmpEq(f32x8{}).blend(
			s.mul(highpInv(da)),
			s.cmpEq(sa).blend(
				s.add(d.mul(highpInv(sa))),
				sa.mul(da.min(d.mul(sa).mul(sa.sub(s).recipFast()))).
					add(s.mul(highpInv(da))).add(d.mul(highpInv(sa))),
			),
		)
	})
}

func highpHardLight(p *highpPipeline) {
	highpBlend2(p, func(s, d, sa, da f32x8) f32x8 {
		return s.mul(highpInv(da)).add(d.mul(highpInv(sa))).add(highpTwo(s).cmpLe(sa).blend(
			highpTwo(s.mul(d)),
			sa.mul(da).sub(highpTwo(da.sub(d).mul(sa.sub(s)))),
		))
	})
}

func highpOverlay(p *highpPipeline) {
	highpBlend2(p, func(s, d, sa, da f32x8) f32x8 {
		return s.mul(highpInv(da)).add(d.mul(highpInv(sa))).add(highpTwo(d).cmpLe(da).blend(
			highpTwo(s.mul(d)),
			sa.mul(da).sub(highpTwo(da.sub(d).mul(sa.sub(s)))),
		))
	})
}

func highpSoftLight(p *highpPipeline) {
	highpBlend2(p, func(s, d, sa, da f32x8) f32x8 {
		one := f32x8Splat(1.0)
		m := da.cmpGt(f32x8{}).blend(d.div(da), f32x8{})
		s2 := highpTwo(s)
		m4 := highpTwo(highpTwo(m))

		// The logic forks three ways:
		//    1. dark src?
		//    2. light src, dark dst?
		//    3. light src, light dst?
		darkSrc := d.mul(sa.add(s2.sub(sa).mul(one.sub(m))))
		darkDst := m4.mul(m4).add(m4).mul(m.sub(one)).add(f32x8Splat(7.0).mul(m))
		liteDst := m.sqrt().sub(m)
		liteSrc := d.mul(sa).add(da.mul(s2.sub(sa)).
			mul(highpTwo(highpTwo(d)).cmpLe(da).blend(darkDst, liteDst))) // 2 or 3?

		return s.mul(highpInv(da)).add(d.mul(highpInv(sa))).add(s2.cmpLe(sa).blend(darkSrc, liteSrc)) // 1 or (2 or 3)?
	})
}

// The non-separable blend modes follow https://www.w3.org/TR/compositing-1/#blendingnonseparable and
// the OpenGL ES 3.2 spec (same math, better simplified), plus what makes it work with premul inputs.

type highpBlendKernel3 func(r, g, b, a, dr, dg, db, da f32x8) (f32x8, f32x8, f32x8, f32x8)

func highpBlend3(p *highpPipeline, f highpBlendKernel3) {
	p.r, p.g, p.b, p.a = f(p.r, p.g, p.b, p.a, p.dr, p.dg, p.db, p.da)
}

func highpHue(p *highpPipeline)        { highpBlend3(p, highpHueK) }
func highpSaturation(p *highpPipeline) { highpBlend3(p, highpSaturationK) }
func highpColor(p *highpPipeline)      { highpBlend3(p, highpColorK) }
func highpLuminosity(p *highpPipeline) { highpBlend3(p, highpLuminosityK) }

// highpNonSepResult is the common tail of the non-separable kernels.
func highpNonSepResult(r, g, b, a, dr, dg, db, da, rr, gg, bb f32x8) (f32x8, f32x8, f32x8, f32x8) {
	r = r.mul(highpInv(da)).add(dr.mul(highpInv(a))).add(rr)
	g = g.mul(highpInv(da)).add(dg.mul(highpInv(a))).add(gg)
	b = b.mul(highpInv(da)).add(db.mul(highpInv(a))).add(bb)
	a = a.add(da).sub(a.mul(da))
	return r, g, b, a
}

func highpHueK(r, g, b, a, dr, dg, db, da f32x8) (f32x8, f32x8, f32x8, f32x8) {
	rr := r.mul(a)
	gg := g.mul(a)
	bb := b.mul(a)

	highpSetSat(&rr, &gg, &bb, highpSat(dr, dg, db).mul(a))
	highpSetLum(&rr, &gg, &bb, highpLum(dr, dg, db).mul(a))
	highpClipColor(&rr, &gg, &bb, a.mul(da))

	return highpNonSepResult(r, g, b, a, dr, dg, db, da, rr, gg, bb)
}

func highpSaturationK(r, g, b, a, dr, dg, db, da f32x8) (f32x8, f32x8, f32x8, f32x8) {
	rr := dr.mul(a)
	gg := dg.mul(a)
	bb := db.mul(a)

	highpSetSat(&rr, &gg, &bb, highpSat(r, g, b).mul(da))
	highpSetLum(&rr, &gg, &bb, highpLum(dr, dg, db).mul(a)) // (This is not redundant.)
	highpClipColor(&rr, &gg, &bb, a.mul(da))

	return highpNonSepResult(r, g, b, a, dr, dg, db, da, rr, gg, bb)
}

func highpColorK(r, g, b, a, dr, dg, db, da f32x8) (f32x8, f32x8, f32x8, f32x8) {
	rr := r.mul(da)
	gg := g.mul(da)
	bb := b.mul(da)

	highpSetLum(&rr, &gg, &bb, highpLum(dr, dg, db).mul(a))
	highpClipColor(&rr, &gg, &bb, a.mul(da))

	return highpNonSepResult(r, g, b, a, dr, dg, db, da, rr, gg, bb)
}

func highpLuminosityK(r, g, b, a, dr, dg, db, da f32x8) (f32x8, f32x8, f32x8, f32x8) {
	rr := dr.mul(a)
	gg := dg.mul(a)
	bb := db.mul(a)

	highpSetLum(&rr, &gg, &bb, highpLum(r, g, b).mul(da))
	highpClipColor(&rr, &gg, &bb, a.mul(da))

	return highpNonSepResult(r, g, b, a, dr, dg, db, da, rr, gg, bb)
}

func highpSat(r, g, b f32x8) f32x8 {
	return r.max(g.max(b)).sub(r.min(g.min(b)))
}

func highpLum(r, g, b f32x8) f32x8 {
	return r.mul(f32x8Splat(0.30)).add(g.mul(f32x8Splat(0.59))).add(b.mul(f32x8Splat(0.11)))
}

func highpSetSat(r, g, b *f32x8, s f32x8) {
	mn := r.min(g.min(*b))
	mx := r.max(g.max(*b))
	sat := mx.sub(mn)

	// Map min channel to 0, max channel to s, and scale the middle proportionally.
	scale := func(c f32x8) f32x8 {
		return sat.cmpEq(f32x8{}).blend(f32x8{}, c.sub(mn).mul(s).div(sat))
	}

	*r = scale(*r)
	*g = scale(*g)
	*b = scale(*b)
}

func highpSetLum(r, g, b *f32x8, l f32x8) {
	diff := l.sub(highpLum(*r, *g, *b))
	*r = r.add(diff)
	*g = g.add(diff)
	*b = b.add(diff)
}

func highpClipColor(r, g, b *f32x8, a f32x8) {
	mn := r.min(g.min(*b))
	mx := r.max(g.max(*b))
	l := highpLum(*r, *g, *b)

	clip := func(c f32x8) f32x8 {
		// Nalarupa: pull a negative channel back toward the luminosity (W3C ClipColor). Upstream
		// tested `mx >= 0`, which is always true here, so this step never ran.
		c = mn.cmpLt(f32x8{}).blend(l.add(c.sub(l).mul(l).div(l.sub(mn))), c)
		c = mx.cmpGt(a).blend(l.add(c.sub(l).mul(a.sub(l)).div(mx.sub(l))), c)
		c = c.max(f32x8{}) // Sometimes without this we may dip just a little negative.
		return c
	}

	*r = clip(*r)
	*g = clip(*g)
	*b = clip(*b)
}

func highpSourceOverRgba(p *highpPipeline) {
	pixels := p.dst8At()
	highpLoad8888(pixels, &p.dr, &p.dg, &p.db, &p.da)
	p.r = highpMad(p.dr, highpInv(p.a), p.r)
	p.g = highpMad(p.dg, highpInv(p.a), p.g)
	p.b = highpMad(p.db, highpInv(p.a), p.b)
	p.a = highpMad(p.da, highpInv(p.a), p.a)
	highpStore8888(&p.r, &p.g, &p.b, &p.a, pixels)
}

func highpSourceOverRgbaTail(p *highpPipeline) {
	pixels := p.dstAt()
	highpLoad8888Tail(p.tail, pixels, &p.dr, &p.dg, &p.db, &p.da)
	p.r = highpMad(p.dr, highpInv(p.a), p.r)
	p.g = highpMad(p.dg, highpInv(p.a), p.g)
	p.b = highpMad(p.db, highpInv(p.a), p.b)
	p.a = highpMad(p.da, highpInv(p.a), p.a)
	highpStore8888Tail(&p.r, &p.g, &p.b, &p.a, p.tail, pixels)
}

func highpTransform(p *highpPipeline) {
	ts := &p.ctx.transform

	tr := highpMad(p.r, f32x8Splat(ts.SX), highpMad(p.g, f32x8Splat(ts.KX), f32x8Splat(ts.TX)))
	tg := highpMad(p.r, f32x8Splat(ts.KY), highpMad(p.g, f32x8Splat(ts.SY), f32x8Splat(ts.TY)))
	p.r = tr
	p.g = tg
}

// Tile x or y to [0,limit) == [0,limit - 1 ulp] (think, sampling from images). The gather stages
// hard clamp the output of these stages to [0,limit); they only do the basic repeat or mirroring.

func highpReflect(p *highpPipeline) {
	ctx := &p.ctx.limitX
	p.r = highpExclusiveReflect(p.r, ctx.scale, ctx.invScale)

	ctx = &p.ctx.limitY
	p.g = highpExclusiveReflect(p.g, ctx.scale, ctx.invScale)
}

func highpExclusiveReflect(v f32x8, limit, invLimit float32) f32x8 {
	l := f32x8Splat(limit)
	il := f32x8Splat(invLimit)
	return v.sub(l).sub(l.add(l).mul(v.sub(l).mul(il.mul(f32x8Splat(0.5))).floor())).sub(l).abs()
}

func highpRepeat(p *highpPipeline) {
	ctx := &p.ctx.limitX
	p.r = highpExclusiveRepeat(p.r, ctx.scale, ctx.invScale)

	ctx = &p.ctx.limitY
	p.g = highpExclusiveRepeat(p.g, ctx.scale, ctx.invScale)
}

func highpExclusiveRepeat(v f32x8, limit, invLimit float32) f32x8 {
	return v.sub(v.mul(f32x8Splat(invLimit)).floor().mul(f32x8Splat(limit)))
}

func highpBilinear(p *highpPipeline) {
	x := p.r
	fx := x.add(f32x8Splat(0.5)).fract()
	y := p.g
	fy := y.add(f32x8Splat(0.5)).fract()
	one := f32x8Splat(1.0)
	wx := [2]f32x8{one.sub(fx), fx}
	wy := [2]f32x8{one.sub(fy), fy}

	highpSampler2x2(p.pixmapSrc, &p.ctx.sampler, x, y, &wx, &wy, &p.r, &p.g, &p.b, &p.a)
}

func highpBicubic(p *highpPipeline) {
	x := p.r
	fx := x.add(f32x8Splat(0.5)).fract()
	y := p.g
	fy := y.add(f32x8Splat(0.5)).fract()
	one := f32x8Splat(1.0)
	wx := [4]f32x8{highpBicubicFar(one.sub(fx)), highpBicubicNear(one.sub(fx)), highpBicubicNear(fx), highpBicubicFar(fx)}
	wy := [4]f32x8{highpBicubicFar(one.sub(fy)), highpBicubicNear(one.sub(fy)), highpBicubicNear(fy), highpBicubicFar(fy)}

	highpSampler4x4(p.pixmapSrc, &p.ctx.sampler, x, y, &wx, &wy, &p.r, &p.g, &p.b, &p.a)
}

// In bicubic interpolation, the 16 pixels and +/- 0.5 and +/- 1.5 offsets from the sample pixel
// center are combined with a non-uniform cubic filter, with higher values near the center. This is
// split into one part for the near 0.5 offsets and one for the far 1.5 offsets.

func highpBicubicNear(t f32x8) f32x8 {
	// 1/18 + 9/18t + 27/18t^2 - 21/18t^3 == t ( t ( -21/18t + 27/18) + 9/18) + 1/18
	return highpMad(
		t,
		highpMad(t,
			highpMad(
				f32x8Splat(-21.0/18.0),
				t,
				f32x8Splat(27.0/18.0),
			),
			f32x8Splat(9.0/18.0),
		),
		f32x8Splat(1.0/18.0),
	)
}

func highpBicubicFar(t f32x8) f32x8 {
	// 0/18 + 0/18*t - 6/18t^2 + 7/18t^3 == t^2 (7/18t - 6/18)
	return t.mul(t).mul(highpMad(f32x8Splat(7.0/18.0), t, f32x8Splat(-6.0/18.0)))
}

func highpSampler2x2(pixmap PixmapRef, ctx *samplerCtx, cx, cy f32x8, wx, wy *[2]f32x8, r, g, b, a *f32x8) {
	*r = f32x8{}
	*g = f32x8{}
	*b = f32x8{}
	*a = f32x8{}

	one := f32x8Splat(1.0)
	const start = -0.5
	y := cy.add(f32x8Splat(start))
	for j := 0; j < 2; j++ {
		x := cx.add(f32x8Splat(start))
		for i := 0; i < 2; i++ {
			var rr, gg, bb, aa f32x8
			highpSample(pixmap, ctx, x, y, &rr, &gg, &bb, &aa)

			w := wx[i].mul(wy[j])
			*r = highpMad(w, rr, *r)
			*g = highpMad(w, gg, *g)
			*b = highpMad(w, bb, *b)
			*a = highpMad(w, aa, *a)

			x = x.add(one)
		}

		y = y.add(one)
	}
}

func highpSampler4x4(pixmap PixmapRef, ctx *samplerCtx, cx, cy f32x8, wx, wy *[4]f32x8, r, g, b, a *f32x8) {
	*r = f32x8{}
	*g = f32x8{}
	*b = f32x8{}
	*a = f32x8{}

	one := f32x8Splat(1.0)
	const start = -1.5
	y := cy.add(f32x8Splat(start))
	for j := 0; j < 4; j++ {
		x := cx.add(f32x8Splat(start))
		for i := 0; i < 4; i++ {
			var rr, gg, bb, aa f32x8
			highpSample(pixmap, ctx, x, y, &rr, &gg, &bb, &aa)

			w := wx[i].mul(wy[j])
			*r = highpMad(w, rr, *r)
			*g = highpMad(w, gg, *g)
			*b = highpMad(w, bb, *b)
			*a = highpMad(w, aa, *a)

			x = x.add(one)
		}

		y = y.add(one)
	}
}

func highpSample(pixmap PixmapRef, ctx *samplerCtx, x, y f32x8, r, g, b, a *f32x8) {
	x = highpTile(x, ctx.spreadMode, float32(pixmap.Width()), ctx.invWidth)
	y = highpTile(y, ctx.spreadMode, float32(pixmap.Height()), ctx.invHeight)

	ix := highpGatherIx(pixmap, x, y)
	pixels := highpGatherPixels(pixmap, ix)
	highpLoad8888(pixels[:], r, g, b, a)
}

func highpTile(v f32x8, mode SpreadMode, limit, invLimit float32) f32x8 {
	switch mode {
	case SpreadModeRepeat:
		return highpExclusiveRepeat(v, limit, invLimit)
	case SpreadModeReflect:
		return highpExclusiveReflect(v, limit, invLimit)
	}
	return v // SpreadModePad
}

func highpPadX1(p *highpPipeline) {
	p.r = p.r.normalize()
}

func highpReflectX1(p *highpPipeline) {
	one := f32x8Splat(1.0)
	p.r = p.r.sub(one).sub(highpTwo(p.r.sub(one).mul(f32x8Splat(0.5)).floor())).sub(one).abs().normalize()
}

func highpRepeatX1(p *highpPipeline) {
	p.r = p.r.sub(p.r.floor()).normalize()
}

func highpGradient(p *highpPipeline) {
	ctx := &p.ctx.gradient

	// N.B. The loop starts at 1 because idx 0 is the color to use before the first stop.
	t := p.r
	var idx u32x8
	for i := 1; i < ctx.len; i++ {
		tt := ctx.tValues[i]
		var n u32x8
		for l := range n {
			if t[l] >= tt {
				n[l] = 1
			}
		}
		idx = idx.add(n)
	}
	highpGradientLookup(ctx, &idx, p.r, &p.r, &p.g, &p.b, &p.a)
}

func highpGradientLookup(ctx *gradientCtx, idx *u32x8, t f32x8, r, g, b, a *f32x8) {
	var fr, fg, fb, fa, br, bg, bb, ba f32x8
	for l, i := range idx {
		// idx can contain any number, so the bounds check stays.
		f, c := ctx.factors[i], ctx.biases[i]
		fr[l], fg[l], fb[l], fa[l] = f.r, f.g, f.b, f.a
		br[l], bg[l], bb[l], ba[l] = c.r, c.g, c.b, c.a
	}

	*r = highpMad(t, fr, br)
	*g = highpMad(t, fg, bg)
	*b = highpMad(t, fb, bb)
	*a = highpMad(t, fa, ba)
}

func highpEvenlySpaced2StopGradient(p *highpPipeline) {
	ctx := &p.ctx.evenlySpaced2StopGradient

	t := p.r
	p.r = highpMad(t, f32x8Splat(ctx.factor.r), f32x8Splat(ctx.bias.r))
	p.g = highpMad(t, f32x8Splat(ctx.factor.g), f32x8Splat(ctx.bias.g))
	p.b = highpMad(t, f32x8Splat(ctx.factor.b), f32x8Splat(ctx.bias.b))
	p.a = highpMad(t, f32x8Splat(ctx.factor.a), f32x8Splat(ctx.bias.a))
}

func highpXYToUnitAngle(p *highpPipeline) {
	x := p.r
	y := p.g
	xAbs := x.abs()
	yAbs := y.abs()
	slope := xAbs.min(yAbs).div(xAbs.max(yAbs))
	s := slope.mul(slope)
	// A 7th degree polynomial approximating atan, generated with sollya.gforge.inria.fr:
	// P1 = fpminimax((1/(2*Pi))*atan(x),[|1,3,5,7|],[|24...|],[2^(-40),1],relative);
	phi := slope.mul(f32x8Splat(0.15912117063999176025390625).
		add(s.mul(f32x8Splat(-5.185396969318389892578125e-2).
			add(s.mul(f32x8Splat(2.476101927459239959716796875e-2).
				add(s.mul(f32x8Splat(-7.0547382347285747528076171875e-3))))))))
	phi = xAbs.cmpLt(yAbs).blend(f32x8Splat(0.25).sub(phi), phi)
	phi = x.cmpLt(f32x8Splat(0.0)).blend(f32x8Splat(0.5).sub(phi), phi)
	phi = y.cmpLt(f32x8Splat(0.0)).blend(f32x8Splat(1.0).sub(phi), phi)
	phi = phi.cmpNe(phi).blend(f32x8Splat(0.0), phi)
	p.r = phi
}

func highpXYToRadius(p *highpPipeline) {
	x2 := p.r.mul(p.r)
	y2 := p.g.mul(p.g)
	p.r = x2.add(y2).sqrt()
}

func highpXYTo2PtConicalFocalOnCircle(p *highpPipeline) {
	x := p.r
	y := p.g
	p.r = x.add(y.mul(y).div(x))
}

func highpXYTo2PtConicalWellBehaved(p *highpPipeline) {
	ctx := &p.ctx.twoPointConicalGradient

	x := p.r
	y := p.g
	p.r = x.mul(x).add(y.mul(y)).sqrt().sub(x.mul(f32x8Splat(ctx.p0)))
}

func highpXYTo2PtConicalGreater(p *highpPipeline) {
	ctx := &p.ctx.twoPointConicalGradient

	x := p.r
	y := p.g
	p.r = x.mul(x).sub(y.mul(y)).sqrt().sub(x.mul(f32x8Splat(ctx.p0)))
}

func highpXYTo2PtConicalSmaller(p *highpPipeline) {
	ctx := &p.ctx.twoPointConicalGradient

	x := p.r
	y := p.g
	p.r = x.mul(x).sub(y.mul(y)).sqrt().neg().sub(x.mul(f32x8Splat(ctx.p0)))
}

func highpXYTo2PtConicalStrip(p *highpPipeline) {
	ctx := &p.ctx.twoPointConicalGradient

	x := p.r
	y := p.g
	p.r = x.add(f32x8Splat(ctx.p0).sub(y.mul(y)).sqrt())
}

func highpMask2PtConicalNan(p *highpPipeline) {
	ctx := &p.ctx.twoPointConicalGradient

	t := p.r
	isDegenerate := t.cmpNe(t)
	p.r = isDegenerate.blend(f32x8{}, t)
	ctx.mask = highpCondToMask(isDegenerate.toU32x8Bitcast().not())
}

func highpMask2PtConicalDegenerates(p *highpPipeline) {
	ctx := &p.ctx.twoPointConicalGradient

	t := p.r
	isDegenerate := t.cmpLe(f32x8{}).or(t.cmpNe(t))
	p.r = isDegenerate.blend(f32x8{}, t)
	ctx.mask = highpCondToMask(isDegenerate.toU32x8Bitcast().not())
}

func highpApplyVectorMask(p *highpPipeline) {
	mask := u32x8(p.ctx.twoPointConicalGradient.mask)

	p.r = p.r.toU32x8Bitcast().and(mask).toF32x8Bitcast()
	p.g = p.g.toU32x8Bitcast().and(mask).toF32x8Bitcast()
	p.b = p.b.toU32x8Bitcast().and(mask).toF32x8Bitcast()
	p.a = p.a.toU32x8Bitcast().and(mask).toF32x8Bitcast()
}

func highpAlter2PtConicalCompensateFocal(p *highpPipeline) {
	ctx := &p.ctx.twoPointConicalGradient

	p.r = p.r.add(f32x8Splat(ctx.p1))
}

func highpAlter2PtConicalUnswap(p *highpPipeline) {
	p.r = f32x8Splat(1.0).sub(p.r)
}

func highpNegateX(p *highpPipeline) {
	p.r = p.r.neg()
}

func highpApplyConcentricScaleBias(p *highpPipeline) {
	ctx := &p.ctx.twoPointConicalGradient

	// Apply t = t * scale + bias for concentric gradients
	x := p.r
	p.r = x.mul(f32x8Splat(ctx.p0)).add(f32x8Splat(ctx.p1))
}

func highpGammaExpand2(p *highpPipeline) {
	p.r = p.r.mul(p.r)
	p.g = p.g.mul(p.g)
	p.b = p.b.mul(p.b)
}

func highpGammaExpandDst2(p *highpPipeline) {
	p.dr = p.dr.mul(p.dr)
	p.dg = p.dg.mul(p.dg)
	p.db = p.db.mul(p.db)
}

func highpGammaCompress2(p *highpPipeline) {
	p.r = p.r.sqrt()
	p.g = p.g.sqrt()
	p.b = p.b.sqrt()
}

func highpGammaExpand22(p *highpPipeline) {
	p.r = p.r.powf(2.2)
	p.g = p.g.powf(2.2)
	p.b = p.b.powf(2.2)
}

func highpGammaExpandDst22(p *highpPipeline) {
	p.dr = p.dr.powf(2.2)
	p.dg = p.dg.powf(2.2)
	p.db = p.db.powf(2.2)
}

func highpGammaCompress22(p *highpPipeline) {
	p.r = p.r.powf(0.45454545)
	p.g = p.g.powf(0.45454545)
	p.b = p.b.powf(0.45454545)
}

func highpSrgbExpand(x f32x8) f32x8 {
	small := x.cmpLe(f32x8Splat(0.04045))
	linear := x.div(f32x8Splat(12.92))
	exp := x.add(f32x8Splat(0.055)).div(f32x8Splat(1.055)).powf(2.4)
	return small.blend(linear, exp)
}

func highpSrgbCompress(x f32x8) f32x8 {
	small := x.cmpLe(f32x8Splat(0.0031308))
	linear := x.mul(f32x8Splat(12.92))
	exp := x.powf(0.416666666).mul(f32x8Splat(1.055)).sub(f32x8Splat(0.055))
	return small.blend(linear, exp)
}

func highpGammaExpandSrgb(p *highpPipeline) {
	p.r = highpSrgbExpand(p.r)
	p.g = highpSrgbExpand(p.g)
	p.b = highpSrgbExpand(p.b)
}

func highpGammaExpandDstSrgb(p *highpPipeline) {
	p.dr = highpSrgbExpand(p.dr)
	p.dg = highpSrgbExpand(p.dg)
	p.db = highpSrgbExpand(p.db)
}

func highpGammaCompressSrgb(p *highpPipeline) {
	p.r = highpSrgbCompress(p.r)
	p.g = highpSrgbCompress(p.g)
	p.b = highpSrgbCompress(p.b)
}

func highpCondToMask(cond u32x8) (r [8]uint32) {
	for i := range r {
		if cond[i] != 0 {
			r[i] = laneTrue
		}
	}
	return
}

// highpLoad8888 loads 8 premultiplied RGBA pixels (32 bytes) as floats in 0..=1.
func highpLoad8888(data []byte, r, g, b, a *f32x8) {
	const factor = float32(1.0 / 255.0)
	_ = data[highpStageWidth*BytesPerPixel-1]
	for i := 0; i < highpStageWidth; i++ {
		px := data[i*BytesPerPixel:]
		r[i] = float32(float32(px[0]) * factor)
		g[i] = float32(float32(px[1]) * factor)
		b[i] = float32(float32(px[2]) * factor)
		a[i] = float32(float32(px[3]) * factor)
	}
}

func highpLoad8888Tail(tail int, data []byte, r, g, b, a *f32x8) {
	// Fill a transparent chunk with tail pixels; tail is always in 1..STAGE_WIDTH-1.
	var tmp [highpStageWidth * BytesPerPixel]byte
	copy(tmp[:tail*BytesPerPixel], data[:tail*BytesPerPixel])
	highpLoad8888(tmp[:], r, g, b, a)
}

func highpStore8888(r, g, b, a *f32x8, data []byte) {
	ri, gi, bi, ai := highpUnnorm(r), highpUnnorm(g), highpUnnorm(b), highpUnnorm(a)
	_ = data[highpStageWidth*BytesPerPixel-1]
	for i := 0; i < highpStageWidth; i++ {
		px := data[i*BytesPerPixel : (i+1)*BytesPerPixel]
		px[0], px[1], px[2], px[3] = uint8(ri[i]), uint8(gi[i]), uint8(bi[i]), uint8(ai[i])
	}
}

func highpStore8888Tail(r, g, b, a *f32x8, tail int, data []byte) {
	ri, gi, bi, ai := highpUnnorm(r), highpUnnorm(g), highpUnnorm(b), highpUnnorm(a)
	for i := 0; i < highpStageWidth; i++ {
		px := data[i*BytesPerPixel : (i+1)*BytesPerPixel]
		px[0], px[1], px[2], px[3] = uint8(ri[i]), uint8(gi[i]), uint8(bi[i]), uint8(ai[i])

		if i+1 == tail {
			break
		}
	}
}

func highpUnnorm(v *f32x8) i32x8 {
	return v.max(f32x8{}).min(f32x8Splat(1.0)).mul(f32x8Splat(255.0)).roundInt()
}

func highpInv(v f32x8) f32x8 { return f32x8Splat(1.0).sub(v) }

func highpTwo(v f32x8) f32x8 { return v.add(v) }

func highpMad(f, m, a f32x8) f32x8 { return f.mul(m).add(a) }

func highpLerp(from, to, t f32x8) f32x8 { return highpMad(to.sub(from), t, from) }
