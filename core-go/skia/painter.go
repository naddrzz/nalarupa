package skia

// The painting API (painter.rs). Where Rust logs a warning and returns, these return silently.

// FillRule says which points are inside a path.
type FillRule uint8

const (
	FillRuleWinding FillRule = iota // a non-zero sum of signed edge crossings, the default
	FillRuleEvenOdd                 // an odd number of edge crossings
)

// Paint controls how a shape is painted.
type Paint struct {
	// Shader is the paint source. Rust's default is black.
	Shader Shader
	// BlendMode defaults to SourceOver in Rust.
	BlendMode BlendMode
	// AntiAlias defaults to true in Rust.
	AntiAlias bool
	// Colorspace is the space blending happens in; anything but linear forces the high precision
	// pipeline.
	Colorspace ColorSpace
	// ForceHQPipeline forces the high precision (float32) pipeline. By default the pipeline is picked
	// from the blend mode and the other parameters.
	ForceHQPipeline bool
}

// DefaultPaint is Paint::default(): black, SourceOver, anti-aliased, linear.
func DefaultPaint() Paint {
	return Paint{Shader: NewSolidColor(ColorBlack), BlendMode: BlendModeSourceOver, AntiAlias: true}
}

// SetColor sets the shader to a solid color.
func (p *Paint) SetColor(c Color) { p.Shader = NewSolidColor(c) }

// SetColorRGBA8 sets the shader to a solid color from 8-bit components.
func (p *Paint) SetColorRGBA8(r, g, b, a uint8) { p.SetColor(ColorFromRGBA8(r, g, b, a)) }

// IsSolidColor reports whether the shader is a solid color.
func (p *Paint) IsSolidColor() bool { _, ok := p.Shader.(*SolidColor); return ok }

func (p *Paint) clone() Paint {
	c := *p
	c.Shader = p.Shader.clone()
	return c
}

func (p *Pixmap) FillRect(rect Rect, paint *Paint, transform Transform, mask *Mask) {
	p.AsMut().FillRect(rect, paint, transform, mask)
}

func (p *Pixmap) FillPath(path *Path, paint *Paint, fillRule FillRule, transform Transform, mask *Mask) {
	p.AsMut().FillPath(path, paint, fillRule, transform, mask)
}

func (p *Pixmap) StrokePath(path *Path, paint *Paint, stroke *Stroke, transform Transform, mask *Mask) {
	p.AsMut().StrokePath(path, paint, stroke, transform, mask)
}

func (p *Pixmap) DrawPixmap(x, y int32, pixmap PixmapRef, paint *PixmapPaint, transform Transform, mask *Mask) {
	p.AsMut().DrawPixmap(x, y, pixmap, paint, transform, mask)
}

func (p *Pixmap) ApplyMask(mask *Mask) { p.AsMut().ApplyMask(mask) }

// FillRect draws a filled rectangle. It is usually slower than filling a rectangular path, but does not
// suffer from odd clipping of horizontal and vertical edges. mask may be nil.
func (p PixmapMut) FillRect(rect Rect, paint *Paint, transform Transform, mask *Mask) {
	if transform.IsIdentity() && !drawTilerRequired(p.Width(), p.Height()) {
		clip := p.size.toScreenIntRect(0, 0)

		var submask *subMaskRef
		if mask != nil {
			m := mask.asSubmask()
			submask = &m
		}
		subpix := p.asSubpixmap()
		b := newRasterPipelineBlitter(paint, submask, &subpix)
		if b == nil {
			return // nothing to do
		}

		if paint.AntiAlias {
			fillRectAA(rect, clip, b)
		} else {
			fillRect(rect, clip, b)
		}
		return
	}

	p.FillPath(PathFromRect(rect), paint, FillRuleWinding, transform, mask)
}

// FillPath draws a filled path. mask may be nil.
func (p PixmapMut) FillPath(path *Path, paint *Paint, fillRule FillRule, transform Transform, mask *Mask) {
	if !transform.IsIdentity() {
		tp := path.Transform(transform)
		if tp == nil {
			return // path transformation failed
		}
		pc := paint.clone()
		pc.Shader.postTransform(transform)
		p.FillPath(tp, &pc, fillRule, Identity(), mask)
		return
	}

	// This is similar to SkDraw::drawPath. Skip empty paths and horizontal/vertical lines.
	pathBounds := path.Bounds()
	if isNearlyZero(pathBounds.Width()) || isNearlyZero(pathBounds.Height()) {
		return
	}
	if isTooBigForMath(path) {
		return
	}

	if tiler, ok := newDrawTiler(p.Width(), p.Height()); ok {
		path := path
		pc := paint.clone()
		for {
			tile, ok := tiler.next()
			if !ok {
				break
			}
			ts := TransformFromTranslate(-float32(tile.X()), -float32(tile.Y()))
			path = path.Transform(ts)
			if path == nil {
				return // path transformation failed
			}
			pc.Shader.postTransform(ts)

			clipRect := tile.Size().toScreenIntRect(0, 0)
			subpix, ok := p.subpixmap(tile.ToIntRect())
			if !ok {
				continue // technically unreachable
			}
			var submask *subMaskRef
			if mask != nil {
				if m, ok := mask.submask(tile.ToIntRect()); ok {
					submask = &m
				}
			}
			b := newRasterPipelineBlitter(&pc, submask, &subpix)
			if b == nil {
				continue // nothing to do; as in Rust, this skips translating back
			}
			// A tile the path does not touch is not an error.
			if pc.AntiAlias {
				fillPathAA(path, fillRule, clipRect, b)
			} else {
				fillPath(path, fillRule, clipRect, b)
			}

			ts = TransformFromTranslate(float32(tile.X()), float32(tile.Y()))
			path = path.Transform(ts)
			if path == nil {
				return // technically unreachable
			}
			pc.Shader.postTransform(ts)
		}
		return
	}

	clipRect := p.size.toScreenIntRect(0, 0)
	var submask *subMaskRef
	if mask != nil {
		m := mask.asSubmask()
		submask = &m
	}
	subpix := p.asSubpixmap()
	b := newRasterPipelineBlitter(paint, submask, &subpix)
	if b == nil {
		return // nothing to do
	}
	if paint.AntiAlias {
		fillPathAA(path, fillRule, clipRect, b)
	} else {
		fillPath(path, fillRule, clipRect, b)
	}
}

// StrokePath strokes a path. A stroke wider than one pixel after the transform is converted into a
// path and filled; a thinner one is drawn as a hairline. A dashed stroke is dashed first. mask may be nil.
func (p PixmapMut) StrokePath(path *Path, paint *Paint, stroke *Stroke, transform Transform, mask *Mask) {
	if stroke.Width < 0 {
		return // negative stroke width
	}

	resScale := ComputeResolutionScale(transform)

	if stroke.Dash != nil {
		path = path.Dash(stroke.Dash, resScale)
		if path == nil {
			return // path dashing failed
		}
	}

	if coverage, ok := treatAsHairline(paint, stroke, transform); ok {
		pc := paint.clone()
		if coverage == 1 {
			// No change to the paint.
		} else if pc.BlendMode.shouldPreScaleCoverage() {
			// The old technique, kept so results do not change; the new way is a tiny bit different.
			scale := castI32(float64(float32(coverage * 256)))
			newAlpha := (255 * scale) >> 8
			pc.Shader.applyOpacity(float32(newAlpha) / 255)
		}

		if tiler, ok := newDrawTiler(p.Width(), p.Height()); ok {
			path := path
			pc := pc.clone()

			if !transform.IsIdentity() {
				pc.Shader.postTransform(transform)
				path = path.Transform(transform)
				if path == nil {
					return // path transformation failed
				}
			}

			for {
				tile, ok := tiler.next()
				if !ok {
					break
				}
				ts := TransformFromTranslate(-float32(tile.X()), -float32(tile.Y()))
				path = path.Transform(ts)
				if path == nil {
					return // path transformation failed
				}
				pc.Shader.postTransform(ts)

				subpix, ok := p.subpixmap(tile.ToIntRect())
				if !ok {
					continue // technically unreachable
				}
				var submask *subMaskRef
				if mask != nil {
					if m, ok := mask.submask(tile.ToIntRect()); ok {
						submask = &m
					}
				}

				// A tile the path does not touch is not an error.
				strokeHairlineInto(path, &pc, stroke.LineCap, submask, &subpix)

				ts = TransformFromTranslate(float32(tile.X()), float32(tile.Y()))
				path = path.Transform(ts)
				if path == nil {
					return
				}
				pc.Shader.postTransform(ts)
			}
			return
		}

		subpix := p.asSubpixmap()
		var submask *subMaskRef
		if mask != nil {
			m := mask.asSubmask()
			submask = &m
		}
		if !transform.IsIdentity() {
			pc.Shader.postTransform(transform)
			tp := path.Transform(transform)
			if tp == nil {
				return // path transformation failed
			}
			strokeHairlineInto(tp, &pc, stroke.LineCap, submask, &subpix)
		} else {
			strokeHairlineInto(path, &pc, stroke.LineCap, submask, &subpix)
		}
		return
	}

	sp := path.Stroke(stroke, resScale)
	if sp == nil {
		return // path stroking failed
	}
	p.FillPath(sp, paint, FillRuleWinding, transform, mask)
}

// strokeHairlineInto strokes a path with subpixel (hairline) width (PixmapMut::stroke_hairline).
func strokeHairlineInto(path *Path, paint *Paint, lineCap LineCap, mask *subMaskRef, pixmap *subPixmapMut) {
	clip := pixmap.size.toScreenIntRect(0, 0)
	b := newRasterPipelineBlitter(paint, mask, pixmap)
	if b == nil {
		return // nothing to do
	}
	if paint.AntiAlias {
		strokeHairlineAA(path, lineCap, clip, b)
	} else {
		strokeHairline(path, lineCap, clip, b)
	}
}

// DrawPixmap draws a pixmap on top of this one, as filling a rectangle with a pixmap pattern. paint may
// be nil for DefaultPixmapPaint; mask may be nil.
func (p PixmapMut) DrawPixmap(x, y int32, pixmap PixmapRef, paint *PixmapPaint, transform Transform, mask *Mask) {
	if paint == nil {
		d := DefaultPixmapPaint()
		paint = &d
	}
	rect := pixmap.Size().ToIntRect(x, y).ToRect()

	// Translate the pattern as well as the bounds.
	pattTransform := TransformFromTranslate(float32(x), float32(y))

	pt := Paint{
		// Pad, otherwise the borders overlap oddly.
		Shader:          NewPattern(pixmap, SpreadModePad, paint.Quality, paint.Opacity, pattTransform),
		BlendMode:       paint.BlendMode,
		AntiAlias:       false, // Skia does not use it either
		ForceHQPipeline: false, // a pattern uses highp anyway
	}
	p.FillRect(rect, &pt, transform, mask)
}

// ApplyMask masks out already drawn content: like filling the whole pixmap with a mask-like pixmap in
// DestinationIn mode. The mask must have the pixmap's size.
func (p PixmapMut) ApplyMask(mask *Mask) {
	if p.size != mask.size {
		return // the pixmap and mask must have the same size
	}

	b := newRasterPipelineBuilder()
	b.push(stageLoadMaskU8)
	b.push(stageLoadDestination)
	b.push(stageDestinationIn)
	b.push(stageStore)
	rp := b.compile()
	rect := p.size.toScreenIntRect(0, 0)
	subpix := p.asSubpixmap()
	rp.run(rect, aaMaskCtx{}, mask.asSubmask().maskCtx(), dummyPixmapSrc, &subpix)
}

func treatAsHairline(paint *Paint, stroke *Stroke, ts Transform) (float32, bool) {
	fastLen := func(p Point) float32 {
		x, y := abs32(p.X), abs32(p.Y)
		if x < y {
			x, y = y, x
		}
		return x + half(y)
	}

	if stroke.Width == 0 {
		return 1, true
	}
	if !paint.AntiAlias {
		return 0, false
	}

	// Translation does not matter.
	ts.TX = 0
	ts.TY = 0

	// Try to fake a thick stroke with a modulated hairline.
	points := []Point{{stroke.Width, 0}, {0, stroke.Width}}
	ts.MapPoints(points)

	len0 := fastLen(points[0])
	len1 := fastLen(points[1])
	if len0 <= 1 && len1 <= 1 {
		return ave(len0, len1), true
	}
	return 0, false
}

// isTooBigForMath reports whether the path bounds (in device space) are so close to the float limit that
// tessellation or clipping math could overflow into infinities or NaNs.
func isTooBigForMath(path *Path) bool {
	const scaleDownToAllowForSmallMultiplies = 0.25
	const mx = ScalarMax * scaleDownToAllowForSmallMultiplies

	b := path.Bounds()
	// Negated so NaN bounds count as too big.
	return !(b.Left() >= -mx && b.Top() >= -mx && b.Right() <= mx && b.Bottom() <= mx)
}

// drawTiler splits a pixmap into tiles: fixed-point rendering is limited to 8192 pixels, so larger
// pixmaps are drawn in tiles.
type drawTiler struct {
	imageWidth, imageHeight uint32
	xOffset, yOffset        uint32
	finished                bool
}

// drawTilerMaxDimensions: 8K is one too big, since 8K << supersample == 32768, too big for fixed point.
const drawTilerMaxDimensions = 8192 - 1

func drawTilerRequired(w, h uint32) bool {
	return w > drawTilerMaxDimensions || h > drawTilerMaxDimensions
}

func newDrawTiler(w, h uint32) (*drawTiler, bool) {
	if drawTilerRequired(w, h) {
		return &drawTiler{imageWidth: w, imageHeight: h}, true
	}
	return nil, false
}

func (t *drawTiler) next() (screenIntRect, bool) {
	if t.finished {
		return screenIntRect{}, false
	}
	if t.xOffset < t.imageWidth && t.yOffset < t.imageHeight {
		h := t.imageHeight
		if t.yOffset < t.imageHeight {
			h = min(t.imageHeight-t.yOffset, drawTilerMaxDimensions)
		}
		r, ok := screenIntRectFromXYWH(t.xOffset, t.yOffset, min(t.imageWidth-t.xOffset, drawTilerMaxDimensions), h)

		t.xOffset += drawTilerMaxDimensions
		if t.xOffset >= t.imageWidth {
			t.xOffset = 0
			t.yOffset += drawTilerMaxDimensions
		}
		return r, ok
	}
	return screenIntRect{}, false
}
