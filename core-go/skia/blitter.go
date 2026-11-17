package skia

// Blitters write pixels into memory (blitter.rs, pipeline/blitter.rs). Coordinates are in destination
// pixel space. Widths and heights are LengthU32 in Rust: never zero.

// blitter is implemented by the raster pipeline blitter and by the scan converters' wrappers. Embed
// unreachableBlitter for the methods a wrapper does not support, as Rust's default methods do.
type blitter interface {
	// blitH blits a horizontal run of one or more pixels.
	blitH(x, y, width uint32)
	// blitAntiH blits a horizontal run of anti-aliased pixels. runs is a sparse zero-terminated
	// run-length encoding (alpha_runs.go): runs[i] pixels from i on have coverage antialias[i], and the
	// next run starts at runs[i+runs[i]]. A run of 0 (None) ends the list.
	blitAntiH(x, y uint32, antialias []uint8, runs []uint16)
	// blitV blits a vertical run of pixels with a constant alpha.
	blitV(x, y, height uint32, alpha uint8)
	blitAntiH2(x, y uint32, alpha0, alpha1 uint8)
	blitAntiV2(x, y uint32, alpha0, alpha1 uint8)
	// blitRect blits a solid rectangle one or more pixels wide.
	blitRect(rect screenIntRect)
	// blitMask blits a pattern of pixels defined by a rectangle-clipped mask.
	blitMask(mask *blitterMask, clip screenIntRect)
}

// unreachableBlitter panics in every method, as the Rust trait's default methods do.
type unreachableBlitter struct{}

func (unreachableBlitter) blitH(x, y, width uint32)                         { panic("unreachable") }
func (unreachableBlitter) blitAntiH(x, y uint32, aa []uint8, runs []uint16) { panic("unreachable") }
func (unreachableBlitter) blitV(x, y, height uint32, alpha uint8)           { panic("unreachable") }
func (unreachableBlitter) blitAntiH2(x, y uint32, alpha0, alpha1 uint8)     { panic("unreachable") }
func (unreachableBlitter) blitAntiV2(x, y uint32, alpha0, alpha1 uint8)     { panic("unreachable") }
func (unreachableBlitter) blitRect(rect screenIntRect)                      { panic("unreachable") }
func (unreachableBlitter) blitMask(mask *blitterMask, clip screenIntRect)   { panic("unreachable") }

// blitterMask describes an alpha bitmap of at most two pixels (blitter::Mask).
type blitterMask struct {
	image    [2]uint8
	bounds   screenIntRect
	rowBytes uint32
}

// rasterPipelineBlitter blits through raster pipelines (RasterPipelineBlitter).
type rasterPipelineBlitter struct {
	mask          *subMaskRef
	pixmapSrc     PixmapRef
	pixmap        *subPixmapMut
	memset2dColor *PremultipliedColorU8
	blitAntiHRP   *rasterPipeline
	blitRectRP    *rasterPipeline
	blitMaskRP    *rasterPipeline
	isMask        bool
}

// newRasterPipelineBlitter returns nil where Rust returns None: nothing to draw, a mask of the wrong
// size, or a shader that cannot be drawn.
func newRasterPipelineBlitter(paint *Paint, mask *subMaskRef, pixmap *subPixmapMut) *rasterPipelineBlitter {
	// The mask must have the same size as the pixmap.
	if mask != nil && (mask.size.width != pixmap.size.width || mask.size.height != pixmap.size.height) {
		return nil
	}

	// Fast reject, SkInterpretXfermode().
	switch paint.BlendMode {
	case BlendModeDestination:
		return nil // keeps the pixmap unchanged
	case BlendModeDestinationIn:
		if paint.Shader.IsOpaque() && paint.IsSolidColor() {
			return nil
		}
	}

	// SourceOver strength-reduces to Source when opaque.
	blendMode := paint.BlendMode
	if paint.Shader.IsOpaque() && blendMode == BlendModeSourceOver && mask == nil {
		blendMode = BlendModeSource
	}

	// A constant color in Source mode can sometimes just be a memset.
	var memset2dColor *PremultipliedColorU8
	if paint.IsSolidColor() && blendMode == BlendModeSource && mask == nil {
		// Unlike Skia, a shader cannot be constant, so no pipeline is needed for its color.
		if sc, ok := paint.Shader.(*SolidColor); ok {
			c := sc.Color.Premultiply().ToColorU8()
			memset2dColor = &c
		}
	}

	// Clear is a transparent memset.
	if blendMode == BlendModeClear && !paint.AntiAlias && mask == nil {
		blendMode = BlendModeSource
		c := PremultipliedColorU8Transparent
		memset2dColor = &c
	}

	cs := paint.Colorspace
	pushExpandDest := func(p *rasterPipelineBuilder) {
		if s, ok := cs.expandDestStage(); ok {
			p.push(s)
		}
	}
	pushCompress := func(p *rasterPipelineBuilder) {
		if s, ok := cs.compressStage(); ok {
			p.push(s)
		}
	}
	pushBlend := func(p *rasterPipelineBuilder) {
		if s, ok := blendMode.toStage(); ok {
			p.push(s)
		}
	}

	var blitAntiHRP *rasterPipeline
	{
		p := newRasterPipelineBuilder()
		p.setForceHQPipeline(paint.ForceHQPipeline)
		if !paint.Shader.pushStages(cs, p) {
			return nil
		}
		if mask != nil {
			p.push(stageMaskU8)
		}
		if blendMode.shouldPreScaleCoverage() {
			p.push(stageScale1Float)
			p.push(stageLoadDestination)
			pushExpandDest(p)
			pushBlend(p)
		} else {
			p.push(stageLoadDestination)
			pushExpandDest(p)
			pushBlend(p)
			p.push(stageLerp1Float)
		}
		pushCompress(p)
		p.push(stageStore)
		blitAntiHRP = p.compile()
	}

	var blitRectRP *rasterPipeline
	{
		p := newRasterPipelineBuilder()
		p.setForceHQPipeline(paint.ForceHQPipeline)
		if !paint.Shader.pushStages(cs, p) {
			return nil
		}
		if mask != nil {
			p.push(stageMaskU8)
		}
		if blendMode == BlendModeSourceOver && mask == nil {
			pushCompress(p)
			p.push(stageSourceOverRgba)
		} else {
			if blendMode != BlendModeSource {
				p.push(stageLoadDestination)
				if s, ok := blendMode.toStage(); ok {
					pushExpandDest(p)
					p.push(s)
				}
			}
			pushCompress(p)
			p.push(stageStore)
		}
		blitRectRP = p.compile()
	}

	var blitMaskRP *rasterPipeline
	{
		p := newRasterPipelineBuilder()
		p.setForceHQPipeline(paint.ForceHQPipeline)
		if !paint.Shader.pushStages(cs, p) {
			return nil
		}
		if mask != nil {
			p.push(stageMaskU8)
		}
		if blendMode.shouldPreScaleCoverage() {
			p.push(stageScaleU8)
			p.push(stageLoadDestination)
			pushExpandDest(p)
			pushBlend(p)
		} else {
			p.push(stageLoadDestination)
			pushExpandDest(p)
			pushBlend(p)
			p.push(stageLerpU8)
		}
		pushCompress(p)
		p.push(stageStore)
		blitMaskRP = p.compile()
	}

	pixmapSrc := dummyPixmapSrc
	if pt, ok := paint.Shader.(*Pattern); ok {
		pixmapSrc = pt.pixmap
	}

	return &rasterPipelineBlitter{
		mask:          mask,
		pixmapSrc:     pixmapSrc,
		pixmap:        pixmap,
		memset2dColor: memset2dColor,
		blitAntiHRP:   blitAntiHRP,
		blitRectRP:    blitRectRP,
		blitMaskRP:    blitMaskRP,
	}
}

// newRasterPipelineBlitterMask blits white coverage into an 8-bit mask (new_mask).
func newRasterPipelineBlitterMask(pixmap *subPixmapMut) *rasterPipelineBlitter {
	color := ColorWhite.Premultiply()
	memset := color.ToColorU8()

	var blitAntiHRP, blitRectRP, blitMaskRP *rasterPipeline
	{
		p := newRasterPipelineBuilder()
		p.pushUniformColor(color)
		p.push(stageLoadDestinationU8)
		p.push(stageLerp1Float)
		p.push(stageStoreU8)
		blitAntiHRP = p.compile()
	}
	{
		p := newRasterPipelineBuilder()
		p.pushUniformColor(color)
		p.push(stageStoreU8)
		blitRectRP = p.compile()
	}
	{
		p := newRasterPipelineBuilder()
		p.pushUniformColor(color)
		p.push(stageLoadDestinationU8)
		p.push(stageLerpU8)
		p.push(stageStoreU8)
		blitMaskRP = p.compile()
	}

	return &rasterPipelineBlitter{
		pixmapSrc:     dummyPixmapSrc,
		pixmap:        pixmap,
		memset2dColor: &memset,
		blitAntiHRP:   blitAntiHRP,
		blitRectRP:    blitRectRP,
		blitMaskRP:    blitMaskRP,
		isMask:        true,
	}
}

func (b *rasterPipelineBlitter) maskCtx() maskCtx {
	if b.mask == nil {
		return maskCtx{}
	}
	return b.mask.maskCtx()
}

func (b *rasterPipelineBlitter) blitH(x, y, width uint32) {
	b.blitRect(screenIntRectFromXYWHSafe(x, y, width, 1))
}

func (b *rasterPipelineBlitter) blitAntiH(x, y uint32, aa []uint8, runs []uint16) {
	mask := b.maskCtx()

	aaOffset := 0
	runOffset := 0
	run := runs[0]
	for run != 0 {
		width := uint32(run)

		switch alpha := aa[aaOffset]; alpha {
		case AlphaU8Transparent:
		case AlphaU8Opaque:
			b.blitH(x, y, width)
		default:
			b.blitAntiHRP.ctx.currentCoverage = float32(float32(alpha) * (1.0 / 255.0))
			rect := screenIntRectFromXYWHSafe(x, y, width, 1)
			b.blitAntiHRP.run(rect, aaMaskCtx{}, mask, b.pixmapSrc, b.pixmap)
		}

		x += width
		runOffset += int(run)
		aaOffset += int(run)
		run = runs[runOffset]
	}
}

func (b *rasterPipelineBlitter) blitV(x, y, height uint32, alpha uint8) {
	bounds := screenIntRectFromXYWHSafe(x, y, 1, height)
	mask := blitterMask{
		image:    [2]uint8{alpha, alpha},
		bounds:   bounds,
		rowBytes: 0, // reuse the one row for the whole height
	}
	b.blitMask(&mask, bounds)
}

func (b *rasterPipelineBlitter) blitAntiH2(x, y uint32, alpha0, alpha1 uint8) {
	bounds, ok := screenIntRectFromXYWH(x, y, 2, 1)
	if !ok {
		panic("skia: blitAntiH2 bounds")
	}
	mask := blitterMask{image: [2]uint8{alpha0, alpha1}, bounds: bounds, rowBytes: 2}
	b.blitMask(&mask, bounds)
}

func (b *rasterPipelineBlitter) blitAntiV2(x, y uint32, alpha0, alpha1 uint8) {
	bounds, ok := screenIntRectFromXYWH(x, y, 1, 2)
	if !ok {
		panic("skia: blitAntiV2 bounds")
	}
	mask := blitterMask{image: [2]uint8{alpha0, alpha1}, bounds: bounds, rowBytes: 1}
	b.blitMask(&mask, bounds)
}

func (b *rasterPipelineBlitter) blitRect(rect screenIntRect) {
	if c := b.memset2dColor; c != nil {
		for y := uint32(0); y < rect.Height(); y++ {
			start := b.pixmap.offset(int(rect.X()), int(rect.Y()+y))
			end := start + int(rect.Width())
			if b.isMask {
				row := b.pixmap.data[start:end]
				for i := range row {
					row[i] = c.Alpha()
				}
			} else {
				row := b.pixmap.data[start*4 : end*4]
				for i := 0; i < len(row); i += 4 {
					copy(row[i:i+4], c[:])
				}
			}
		}
		return
	}

	b.blitRectRP.run(rect, aaMaskCtx{}, b.maskCtx(), b.pixmapSrc, b.pixmap)
}

func (b *rasterPipelineBlitter) blitMask(mask *blitterMask, clip screenIntRect) {
	aa := aaMaskCtx{
		pixels: mask.image,
		stride: mask.rowBytes,
		shift:  int(mask.bounds.Left() + mask.bounds.Top()*mask.rowBytes),
	}
	b.blitMaskRP.run(clip, aa, b.maskCtx(), b.pixmapSrc, b.pixmap)
}
