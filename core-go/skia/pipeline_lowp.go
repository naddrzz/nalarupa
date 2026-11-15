package skia

import "math"

// The low precision raster pipeline (pipeline/lowp.rs): u16 lanes instead of f32, 16 pixels per run.
// It lacks the stages that need high precision; compile() falls back to highp for those.

const lowpStageWidth = 16

type lowpStageFn func(p *lowpPipeline)

type lowpPipeline struct {
	pixmap    *subPixmapMut
	maskCtx   maskCtx
	aaMaskCtx aaMaskCtx
	ctx       *pipelineContext

	r, g, b, a     u16x16
	dr, dg, db, da u16x16

	tail, dx, dy int

	// stop ends the current run of stages early: Rust's mask_u8 returns without calling the next stage.
	stop bool
}

// lowpStages is lowp::STAGES, indexed by stage; nil is Rust's null_fn (no lowp implementation).
var lowpStages = [stagesCount]lowpStageFn{
	stageMoveSourceToDestination:   lowpMoveSourceToDestination,
	stageMoveDestinationToSource:   lowpMoveDestinationToSource,
	stagePremultiply:               lowpPremultiply,
	stageUniformColor:              lowpUniformColor,
	stageSeedShader:                lowpSeedShader,
	stageLoadDestination:           lowpLoadDst,
	stageStore:                     lowpStore,
	stageLoadDestinationU8:         lowpLoadDstU8,
	stageStoreU8:                   lowpStoreU8,
	stageLoadMaskU8:                lowpLoadMaskU8,
	stageMaskU8:                    lowpMaskU8,
	stageScaleU8:                   lowpScaleU8,
	stageLerpU8:                    lowpLerpU8,
	stageScale1Float:               lowpScale1Float,
	stageLerp1Float:                lowpLerp1Float,
	stageDestinationAtop:           lowpDestinationAtop,
	stageDestinationIn:             lowpDestinationIn,
	stageDestinationOut:            lowpDestinationOut,
	stageDestinationOver:           lowpDestinationOver,
	stageSourceAtop:                lowpSourceAtop,
	stageSourceIn:                  lowpSourceIn,
	stageSourceOut:                 lowpSourceOut,
	stageSourceOver:                lowpSourceOver,
	stageClear:                     lowpClear,
	stageModulate:                  lowpModulate,
	stageMultiply:                  lowpMultiply,
	stagePlus:                      lowpPlus,
	stageScreen:                    lowpScreen,
	stageXor:                       lowpXor,
	stageDarken:                    lowpDarken,
	stageDifference:                lowpDifference,
	stageExclusion:                 lowpExclusion,
	stageHardLight:                 lowpHardLight,
	stageLighten:                   lowpLighten,
	stageOverlay:                   lowpOverlay,
	stageSourceOverRgba:            lowpSourceOverRgba,
	stageTransform:                 lowpTransform,
	stagePadX1:                     lowpPadX1,
	stageReflectX1:                 lowpReflectX1,
	stageRepeatX1:                  lowpRepeatX1,
	stageGradient:                  lowpGradient,
	stageEvenlySpaced2StopGradient: lowpEvenlySpaced2StopGradient,
	stageXYToRadius:                lowpXYToRadius,
}

// lowpTailStages holds the tail variants compile() swaps in for the last, partial run of a row.
var lowpTailStages = [stagesCount]lowpStageFn{
	stageLoadDestination:   lowpLoadDstTail,
	stageStore:             lowpStoreTail,
	stageLoadDestinationU8: lowpLoadDstU8Tail,
	stageStoreU8:           lowpStoreU8Tail,
	stageSourceOverRgba:    lowpSourceOverRgbaTail,
}

func lowpStart(fns, tail []lowpStageFn, rect screenIntRect, aaMask aaMaskCtx, mask maskCtx, ctx *pipelineContext, pixmapDst *subPixmapMut) {
	p := lowpPipeline{pixmap: pixmapDst, maskCtx: mask, aaMaskCtx: aaMask, ctx: ctx}

	for y := rect.Y(); y < rect.Bottom(); y++ {
		x := int(rect.X())
		end := int(rect.Right())

		for x+lowpStageWidth <= end {
			p.dx, p.dy, p.tail = x, int(y), lowpStageWidth
			p.runStages(fns)
			x += lowpStageWidth
		}

		if x != end {
			p.dx, p.dy, p.tail = x, int(y), end-x
			p.runStages(tail)
		}
	}
}

func (p *lowpPipeline) runStages(fns []lowpStageFn) {
	p.stop = false
	for _, f := range fns {
		f(p)
		if p.stop {
			return
		}
	}
}

// pixels returns the destination bytes from (dx, dy) on, 4 per pixel.
func (p *lowpPipeline) pixels() []byte { return p.pixmap.data[p.pixmap.offset(p.dx, p.dy)*4:] }

// maskBytes returns the destination bytes from (dx, dy) on, 1 per pixel (a mask being drawn).
func (p *lowpPipeline) maskBytes() []byte { return p.pixmap.data[p.pixmap.offset(p.dx, p.dy):] }

func lowpMoveSourceToDestination(p *lowpPipeline) {
	p.dr, p.dg, p.db, p.da = p.r, p.g, p.b, p.a
}

func lowpMoveDestinationToSource(p *lowpPipeline) {
	p.r, p.g, p.b, p.a = p.dr, p.dg, p.db, p.da
}

func lowpPremultiply(p *lowpPipeline) {
	p.r = lowpDiv255(p.r.mul(p.a))
	p.g = lowpDiv255(p.g.mul(p.a))
	p.b = lowpDiv255(p.b.mul(p.a))
}

func lowpUniformColor(p *lowpPipeline) {
	c := &p.ctx.uniformColor
	p.r = u16x16Splat(c.rgba[0])
	p.g = u16x16Splat(c.rgba[1])
	p.b = u16x16Splat(c.rgba[2])
	p.a = u16x16Splat(c.rgba[3])
}

func lowpSeedShader(p *lowpPipeline) {
	var x f32x16
	for i := range x {
		x[i] = float32(p.dx) + (float32(i) + 0.5)
	}
	y := f32x16Splat(float32(p.dy) + 0.5)
	lowpSplit(&x, &p.r, &p.g)
	lowpSplit(&y, &p.b, &p.a)
}

func lowpLoadDst(p *lowpPipeline) {
	lowpLoad8888(p.pixels()[:lowpStageWidth*4], &p.dr, &p.dg, &p.db, &p.da)
}

func lowpLoadDstTail(p *lowpPipeline) {
	lowpLoad8888Tail(p.tail, p.pixels(), &p.dr, &p.dg, &p.db, &p.da)
}

func lowpStore(p *lowpPipeline) {
	lowpStore8888(&p.r, &p.g, &p.b, &p.a, p.pixels()[:lowpStageWidth*4])
}

func lowpStoreTail(p *lowpPipeline) {
	lowpStore8888Tail(&p.r, &p.g, &p.b, &p.a, p.tail, p.pixels())
}

func lowpLoadDstU8(p *lowpPipeline) {
	lowpLoad8((*[lowpStageWidth]byte)(p.maskBytes()), &p.da)
}

func lowpLoadDstU8Tail(p *lowpPipeline) {
	var tmp [lowpStageWidth]byte
	copy(tmp[:p.tail], p.maskBytes()[:p.tail])
	lowpLoad8(&tmp, &p.da)
}

func lowpStoreU8(p *lowpPipeline) {
	data := (*[lowpStageWidth]byte)(p.maskBytes())
	for i := range data {
		data[i] = uint8(p.a[i])
	}
}

func lowpStoreU8Tail(p *lowpPipeline) {
	data := p.maskBytes()[:p.tail]
	for i := range data {
		data[i] = uint8(p.a[i])
	}
}

// lowpLoadMaskU8 loads the clip mask values into alpha without masking the pipeline.
func lowpLoadMaskU8(p *lowpPipeline) {
	offset := p.maskCtx.offset(p.dx, p.dy)

	var c u16x16
	for i := 0; i < p.tail; i++ {
		c[i] = uint16(p.maskCtx.data[offset+i])
	}

	p.r = u16x16{}
	p.g = u16x16{}
	p.b = u16x16{}
	p.a = c
}

func lowpMaskU8(p *lowpPipeline) {
	offset := p.maskCtx.offset(p.dx, p.dy)

	var c u16x16
	for i := 0; i < p.tail; i++ {
		c[i] = uint16(p.maskCtx.data[offset+i])
	}

	if c == (u16x16{}) {
		p.stop = true // fully masked out: Rust returns without running the remaining stages
		return
	}

	p.r = lowpDiv255(p.r.mul(c))
	p.g = lowpDiv255(p.g.mul(c))
	p.b = lowpDiv255(p.b.mul(c))
	p.a = lowpDiv255(p.a.mul(c))
}

// lowpAACoverage loads the up to two blitter mask values for this run.
func lowpAACoverage(p *lowpPipeline) u16x16 {
	data := p.aaMaskCtx.copyAtXY(p.dx, p.dy, p.tail)
	return u16x16{uint16(data[0]), uint16(data[1])}
}

func lowpScaleU8(p *lowpPipeline) {
	c := lowpAACoverage(p)
	p.r = lowpDiv255(p.r.mul(c))
	p.g = lowpDiv255(p.g.mul(c))
	p.b = lowpDiv255(p.b.mul(c))
	p.a = lowpDiv255(p.a.mul(c))
}

func lowpLerpU8(p *lowpPipeline) {
	c := lowpAACoverage(p)
	p.r = lowpLerp(p.dr, p.r, c)
	p.g = lowpLerp(p.dg, p.g, c)
	p.b = lowpLerp(p.db, p.b, c)
	p.a = lowpLerp(p.da, p.a, c)
}

func lowpScale1Float(p *lowpPipeline) {
	c := lowpFromFloat(p.ctx.currentCoverage)
	p.r = lowpDiv255(p.r.mul(c))
	p.g = lowpDiv255(p.g.mul(c))
	p.b = lowpDiv255(p.b.mul(c))
	p.a = lowpDiv255(p.a.mul(c))
}

func lowpLerp1Float(p *lowpPipeline) {
	c := lowpFromFloat(p.ctx.currentCoverage)
	p.r = lowpLerp(p.dr, p.r, c)
	p.g = lowpLerp(p.dg, p.g, c)
	p.b = lowpLerp(p.db, p.b, c)
	p.a = lowpLerp(p.da, p.a, c)
}

// lowpBlend applies f to every channel, alpha included (blend_fn!).
func lowpBlend(p *lowpPipeline, f func(s, d, sa, da u16x16) u16x16) {
	p.r = f(p.r, p.dr, p.a, p.da)
	p.g = f(p.g, p.dg, p.a, p.da)
	p.b = f(p.b, p.db, p.a, p.da)
	p.a = f(p.a, p.da, p.a, p.da)
}

// lowpBlend2 applies f to the color channels and source-over to alpha (blend_fn2!).
func lowpBlend2(p *lowpPipeline, f func(s, d, sa, da u16x16) u16x16) {
	p.r = f(p.r, p.dr, p.a, p.da)
	p.g = f(p.g, p.dg, p.a, p.da)
	p.b = f(p.b, p.db, p.a, p.da)
	p.a = p.a.add(lowpDiv255(p.da.mul(lowpInv(p.a))))
}

var lowpTwo = u16x16Splat(2)

func lowpClear(p *lowpPipeline) {
	lowpBlend(p, func(_, _, _, _ u16x16) u16x16 { return u16x16{} })
}

func lowpSourceAtop(p *lowpPipeline) {
	lowpBlend(p, func(s, d, sa, da u16x16) u16x16 { return lowpDiv255(s.mul(da).add(d.mul(lowpInv(sa)))) })
}

func lowpDestinationAtop(p *lowpPipeline) {
	lowpBlend(p, func(s, d, sa, da u16x16) u16x16 { return lowpDiv255(d.mul(sa).add(s.mul(lowpInv(da)))) })
}

func lowpSourceIn(p *lowpPipeline) {
	lowpBlend(p, func(s, _, _, da u16x16) u16x16 { return lowpDiv255(s.mul(da)) })
}

func lowpDestinationIn(p *lowpPipeline) {
	lowpBlend(p, func(_, d, sa, _ u16x16) u16x16 { return lowpDiv255(d.mul(sa)) })
}

func lowpSourceOut(p *lowpPipeline) {
	lowpBlend(p, func(s, _, _, da u16x16) u16x16 { return lowpDiv255(s.mul(lowpInv(da))) })
}

func lowpDestinationOut(p *lowpPipeline) {
	lowpBlend(p, func(_, d, sa, _ u16x16) u16x16 { return lowpDiv255(d.mul(lowpInv(sa))) })
}

func lowpSourceOver(p *lowpPipeline) {
	lowpBlend(p, func(s, d, sa, _ u16x16) u16x16 { return s.add(lowpDiv255(d.mul(lowpInv(sa)))) })
}

func lowpDestinationOver(p *lowpPipeline) {
	lowpBlend(p, func(s, d, _, da u16x16) u16x16 { return d.add(lowpDiv255(s.mul(lowpInv(da)))) })
}

func lowpModulate(p *lowpPipeline) {
	lowpBlend(p, func(s, d, _, _ u16x16) u16x16 { return lowpDiv255(s.mul(d)) })
}

func lowpMultiply(p *lowpPipeline) {
	lowpBlend(p, func(s, d, sa, da u16x16) u16x16 {
		return lowpDiv255(s.mul(lowpInv(da)).add(d.mul(lowpInv(sa))).add(s.mul(d)))
	})
}

func lowpScreen(p *lowpPipeline) {
	lowpBlend(p, func(s, d, _, _ u16x16) u16x16 { return s.add(d).sub(lowpDiv255(s.mul(d))) })
}

func lowpXor(p *lowpPipeline) {
	lowpBlend(p, func(s, d, sa, da u16x16) u16x16 {
		return lowpDiv255(s.mul(lowpInv(da)).add(d.mul(lowpInv(sa))))
	})
}

func lowpPlus(p *lowpPipeline) {
	lowpBlend(p, func(s, d, _, _ u16x16) u16x16 { return s.add(d).min(u16x16Splat(255)) })
}

func lowpDarken(p *lowpPipeline) {
	lowpBlend2(p, func(s, d, sa, da u16x16) u16x16 {
		return s.add(d).sub(lowpDiv255(s.mul(da).max(d.mul(sa))))
	})
}

func lowpLighten(p *lowpPipeline) {
	lowpBlend2(p, func(s, d, sa, da u16x16) u16x16 {
		return s.add(d).sub(lowpDiv255(s.mul(da).min(d.mul(sa))))
	})
}

func lowpExclusion(p *lowpPipeline) {
	lowpBlend2(p, func(s, d, _, _ u16x16) u16x16 {
		return s.add(d).sub(lowpTwo.mul(lowpDiv255(s.mul(d))))
	})
}

func lowpDifference(p *lowpPipeline) {
	lowpBlend2(p, func(s, d, sa, da u16x16) u16x16 {
		return s.add(d).sub(lowpTwo.mul(lowpDiv255(s.mul(da).min(d.mul(sa)))))
	})
}

func lowpHardLight(p *lowpPipeline) {
	lowpBlend2(p, func(s, d, sa, da u16x16) u16x16 {
		return lowpDiv255(s.mul(lowpInv(da)).add(d.mul(lowpInv(sa))).add(
			s.add(s).cmpLe(sa).blend(
				lowpTwo.mul(s).mul(d),
				sa.mul(da).sub(lowpTwo.mul(sa.sub(s)).mul(da.sub(d))),
			),
		))
	})
}

func lowpOverlay(p *lowpPipeline) {
	lowpBlend2(p, func(s, d, sa, da u16x16) u16x16 {
		return lowpDiv255(s.mul(lowpInv(da)).add(d.mul(lowpInv(sa))).add(
			d.add(d).cmpLe(da).blend(
				lowpTwo.mul(s).mul(d),
				sa.mul(da).sub(lowpTwo.mul(sa.sub(s)).mul(da.sub(d))),
			),
		))
	})
}

func lowpSourceOverRgba(p *lowpPipeline) {
	pixels := p.pixels()[:lowpStageWidth*4]
	lowpLoad8888(pixels, &p.dr, &p.dg, &p.db, &p.da)
	lowpSourceOverRgbaBlend(p)
	lowpStore8888(&p.r, &p.g, &p.b, &p.a, pixels)
}

func lowpSourceOverRgbaTail(p *lowpPipeline) {
	pixels := p.pixels()
	lowpLoad8888Tail(p.tail, pixels, &p.dr, &p.dg, &p.db, &p.da)
	lowpSourceOverRgbaBlend(p)
	lowpStore8888Tail(&p.r, &p.g, &p.b, &p.a, p.tail, pixels)
}

func lowpSourceOverRgbaBlend(p *lowpPipeline) {
	p.r = p.r.add(lowpDiv255(p.dr.mul(lowpInv(p.a))))
	p.g = p.g.add(lowpDiv255(p.dg.mul(lowpInv(p.a))))
	p.b = p.b.add(lowpDiv255(p.db.mul(lowpInv(p.a))))
	p.a = p.a.add(lowpDiv255(p.da.mul(lowpInv(p.a))))
}

func lowpTransform(p *lowpPipeline) {
	ts := &p.ctx.transform

	x := lowpJoin(&p.r, &p.g)
	y := lowpJoin(&p.b, &p.a)

	nx := lowpMad(x, f32x16Splat(ts.SX), lowpMad(y, f32x16Splat(ts.KX), f32x16Splat(ts.TX)))
	ny := lowpMad(x, f32x16Splat(ts.KY), lowpMad(y, f32x16Splat(ts.SY), f32x16Splat(ts.TY)))

	lowpSplit(&nx, &p.r, &p.g)
	lowpSplit(&ny, &p.b, &p.a)
}

func lowpPadX1(p *lowpPipeline) {
	x := lowpJoin(&p.r, &p.g).normalize()
	lowpSplit(&x, &p.r, &p.g)
}

func lowpReflectX1(p *lowpPipeline) {
	one := f32x16Splat(1)
	x := lowpJoin(&p.r, &p.g)
	f := x.sub(one).mul(f32x16Splat(0.5)).floor()
	x = x.sub(one).sub(f.add(f)).sub(one).abs().normalize()
	lowpSplit(&x, &p.r, &p.g)
}

func lowpRepeatX1(p *lowpPipeline) {
	x := lowpJoin(&p.r, &p.g)
	x = x.sub(x.floor()).normalize()
	lowpSplit(&x, &p.r, &p.g)
}

func lowpGradient(p *lowpPipeline) {
	ctx := &p.ctx.gradient

	// The loop starts at 1 because idx 0 is the color to use before the first stop.
	t := lowpJoin(&p.r, &p.g)
	var idx u16x16
	for i := 1; i < ctx.len; i++ {
		tt := ctx.tValues[i]
		for j := range idx {
			if t[j] >= tt {
				idx[j]++
			}
		}
	}
	lowpGradientLookup(ctx, &idx, t, &p.r, &p.g, &p.b, &p.a)
}

func lowpEvenlySpaced2StopGradient(p *lowpPipeline) {
	ctx := &p.ctx.evenlySpaced2StopGradient

	t := lowpJoin(&p.r, &p.g)
	lowpRoundF32ToU16(
		lowpMad(t, f32x16Splat(ctx.factor.r), f32x16Splat(ctx.bias.r)),
		lowpMad(t, f32x16Splat(ctx.factor.g), f32x16Splat(ctx.bias.g)),
		lowpMad(t, f32x16Splat(ctx.factor.b), f32x16Splat(ctx.bias.b)),
		lowpMad(t, f32x16Splat(ctx.factor.a), f32x16Splat(ctx.bias.a)),
		&p.r, &p.g, &p.b, &p.a,
	)
}

func lowpXYToRadius(p *lowpPipeline) {
	x := lowpJoin(&p.r, &p.g)
	y := lowpJoin(&p.b, &p.a)
	x = x.mul(x).add(y.mul(y)).sqrt()
	lowpSplit(&x, &p.r, &p.g)
	lowpSplit(&y, &p.b, &p.a)
}

// lowpGradientLookup uses u16 indices, not u32 as Skia; the gradient code never makes that many stops.
func lowpGradientLookup(ctx *gradientCtx, idx *u16x16, t f32x16, r, g, b, a *u16x16) {
	var fr, fg, fb, fa, br, bg, bb, ba f32x16
	for i, n := range idx {
		f := ctx.factors[n]
		fr[i], fg[i], fb[i], fa[i] = f.r, f.g, f.b, f.a
	}
	for i, n := range idx {
		c := ctx.biases[n]
		br[i], bg[i], bb[i], ba[i] = c.r, c.g, c.b, c.a
	}

	lowpRoundF32ToU16(
		lowpMad(t, fr, br),
		lowpMad(t, fg, bg),
		lowpMad(t, fb, bb),
		lowpMad(t, fa, ba),
		r, g, b, a,
	)
}

func lowpRoundF32ToU16(rf, gf, bf, af f32x16, r, g, b, a *u16x16) {
	c255 := f32x16Splat(255)
	half := f32x16Splat(0.5)
	rf.normalize().mul(c255).add(half).saveToU16x16(r)
	gf.normalize().mul(c255).add(half).saveToU16x16(g)
	bf.normalize().mul(c255).add(half).saveToU16x16(b)
	af.mul(c255).add(half).saveToU16x16(a)
}

// lowpLoad8888 loads 16 RGBA pixels from data (64 bytes).
func lowpLoad8888(data []byte, r, g, b, a *u16x16) {
	data = data[:lowpStageWidth*4]
	for i := range lowpStageWidth {
		px := data[i*4 : i*4+4]
		r[i] = uint16(px[0])
		g[i] = uint16(px[1])
		b[i] = uint16(px[2])
		a[i] = uint16(px[3])
	}
}

// lowpLoad8888Tail loads tail pixels, the rest transparent.
func lowpLoad8888Tail(tail int, data []byte, r, g, b, a *u16x16) {
	var tmp [lowpStageWidth * 4]byte
	copy(tmp[:tail*4], data[:tail*4])
	lowpLoad8888(tmp[:], r, g, b, a)
}

// lowpStore8888 stores 16 pixels, each lane truncated to u8.
func lowpStore8888(r, g, b, a *u16x16, data []byte) {
	data = data[:lowpStageWidth*4]
	for i := range lowpStageWidth {
		px := data[i*4 : i*4+4]
		px[0] = uint8(r[i])
		px[1] = uint8(g[i])
		px[2] = uint8(b[i])
		px[3] = uint8(a[i])
	}
}

func lowpStore8888Tail(r, g, b, a *u16x16, tail int, data []byte) {
	data = data[:tail*4]
	for i := range tail {
		px := data[i*4 : i*4+4]
		px[0] = uint8(r[i])
		px[1] = uint8(g[i])
		px[2] = uint8(b[i])
		px[3] = uint8(a[i])
	}
}

func lowpLoad8(data *[lowpStageWidth]byte, a *u16x16) {
	for i, v := range data {
		a[i] = uint16(v)
	}
}

// lowpDiv255 is (v + 255) >> 8; Skia's NEON variant is not used, to keep results reproducible.
func lowpDiv255(v u16x16) u16x16 {
	return v.add(u16x16Splat(255)).shr(u16x16Splat(8))
}

func lowpInv(v u16x16) u16x16 { return u16x16Splat(255).sub(v) }

func lowpFromFloat(f float32) u16x16 { return u16x16Splat(castU16(float32(f*255) + 0.5)) }

func lowpLerp(from, to, t u16x16) u16x16 {
	return lowpDiv255(from.mul(lowpInv(t)).add(to.mul(t)))
}

// lowpSplit reinterprets the 64 bytes of v as two u16x16 (little-endian, as on x86-64).
func lowpSplit(v *f32x16, lo, hi *u16x16) {
	for i, f := range v {
		bits := math.Float32bits(f)
		d := lo
		j := i * 2
		if i >= 8 {
			d = hi
			j -= 16
		}
		d[j] = uint16(bits)
		d[j+1] = uint16(bits >> 16)
	}
}

// lowpJoin is the inverse of lowpSplit.
func lowpJoin(lo, hi *u16x16) f32x16 {
	var v f32x16
	for i := range v {
		d := lo
		j := i * 2
		if i >= 8 {
			d = hi
			j -= 16
		}
		v[i] = math.Float32frombits(uint32(d[j]) | uint32(d[j+1])<<16)
	}
	return v
}

func lowpMad(f, m, a f32x16) f32x16 {
	for i := range f {
		f[i] = float32(f[i]*m[i]) + a[i]
	}
	return f
}
