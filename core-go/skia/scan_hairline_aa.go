package skia

// Anti-aliased hairlines and rect fills (scan/hairline_aa.rs).

type antiHairFixedRect struct{ left, top, right, bottom fDot16 }

func antiHairFixedRectFromRect(src *Rect) antiHairFixedRect {
	return antiHairFixedRect{
		left:   fdot16FromF32(src.Left()),
		top:    fdot16FromF32(src.Top()),
		right:  fdot16FromF32(src.Right()),
		bottom: fdot16FromF32(src.Bottom()),
	}
}

// antiHairAlphaMul returns (value * alpha256) >> 8, alpha256 being 0..256.
func antiHairAlphaMul(value uint8, alpha256 int32) uint8 {
	return uint8((int32(value) * alpha256) >> 8)
}

// antiHairFillRect is hairline_aa::fill_rect.
func antiHairFillRect(rect Rect, clip screenIntRect, b blitter) {
	r, ok := rect.Intersect(clip.ToRect())
	if !ok {
		return // everything was clipped out
	}
	fr := antiHairFixedRectFromRect(&r)
	antiHairFillDot8(
		fdot8FromFdot16(fr.left),
		fdot8FromFdot16(fr.top),
		fdot8FromFdot16(fr.right),
		fdot8FromFdot16(fr.bottom),
		true,
		b,
	)
}

func antiHairFillDot8(l, t, r, bot fDot8, fillInner bool, b blitter) {
	toAlpha := func(a int32) uint8 { return uint8(a) }

	// check for empty now that we're in our reduced precision space
	if l >= r || t >= bot {
		return
	}

	top := t >> 8
	if top == ((bot - 1) >> 8) {
		// just one scanline high
		antiHairDoScanline(l, top, r, toAlpha(bot-t-1), b)
		return
	}

	if t&0xFF != 0 {
		antiHairDoScanline(l, top, r, toAlpha(256-(t&0xFF)), b)
		top++
	}

	bottom := bot >> 8
	height := bottom - top
	if height > 0 {
		h := uint32(height)
		left := l >> 8
		if left == ((r - 1) >> 8) {
			// just 1-pixel wide
			if left >= 0 && top >= 0 {
				b.blitV(uint32(left), uint32(top), h, toAlpha(r-l-1))
			}
		} else {
			if l&0xFF != 0 {
				if left >= 0 && top >= 0 {
					b.blitV(uint32(left), uint32(top), h, toAlpha(256-(l&0xFF)))
				}
				left++
			}

			right := r >> 8
			width := right - left
			if fillInner && width > 0 && left >= 0 && top >= 0 {
				b.blitRect(screenIntRectFromXYWHSafe(uint32(left), uint32(top), uint32(width), h))
			}

			if r&0xFF != 0 && right >= 0 && top >= 0 {
				b.blitV(uint32(right), uint32(top), h, toAlpha(r&0xFF))
			}
		}
	}

	if bot&0xFF != 0 {
		antiHairDoScanline(l, bottom, r, toAlpha(bot&0xFF), b)
	}
}

func antiHairDoScanline(l fDot8, top int32, r fDot8, alpha uint8, b blitter) {
	if top < 0 {
		return
	}
	y := uint32(top)

	if (l >> 8) == ((r - 1) >> 8) {
		// 1x1 pixel
		if l>>8 >= 0 {
			b.blitV(uint32(l>>8), y, 1, antiHairAlphaMul(alpha, r-l))
		}
		return
	}

	left := l >> 8

	if l&0xFF != 0 {
		if l>>8 >= 0 {
			b.blitV(uint32(l>>8), y, 1, antiHairAlphaMul(alpha, 256-(l&0xFF)))
		}
		left++
	}

	right := r >> 8
	width := right - left
	if width > 0 && left >= 0 {
		antiHairCallHLineBlitter(uint32(left), y, true, uint32(width), alpha, b)
	}

	if r&0xFF != 0 && right >= 0 {
		b.blitV(uint32(right), y, 1, antiHairAlphaMul(alpha, r&0xFF))
	}
}

// antiHairCallHLineBlitter is call_hline_blitter; hasY is false where Rust's y is None, in which case
// nothing is blitted. count must be non-zero.
func antiHairCallHLineBlitter(x, y uint32, hasY bool, count uint32, alpha uint8, b blitter) {
	const hlineStackBuffer = 100

	var runs [hlineStackBuffer + 1]uint16
	var aa [hlineStackBuffer]uint8

	for {
		// Wrapper blitters may modify the buffers in place, so reseed the aa value.
		aa[0] = alpha

		n := count
		if n > hlineStackBuffer {
			n = hlineStackBuffer
		}

		runs[0] = uint16(n)
		runs[n] = 0
		if hasY {
			b.blitAntiH(x, y, aa[:], runs[:])
		}
		x += n

		if n >= count || count == 0 {
			break
		}

		count -= n
	}
}

// strokeHairlineAA strokes a path with a one pixel wide anti-aliased line (hairline_aa::stroke_path).
func strokeHairlineAA(path *Path, lineCap LineCap, clip screenIntRect, b blitter) {
	strokeHairlineImpl(path, lineCap, clip, antiHairLineRgn, b)
}

func antiHairLineRgn(points []Point, clip *screenIntRect, b blitter) {
	const maxV = 32767.0
	fixedBounds, _ := RectFromLTRB(-maxV, -maxV, maxV, maxV)

	var clipBounds *Rect
	if clip != nil {
		// Integral clipping comes later; a scalar clip first keeps the coordinates expressible in
		// fixed point. AA hairlines can draw up to 1/2 pixel outside their bounds, so outset the clip,
		// by a whole pixel to keep away from the 1/2 pixel boundary the antihair blitter relies on.
		if r, ok := clip.ToRect().Outset(1.0, 1.0); ok {
			clipBounds = &r
		}
	}

	for i := 0; i < len(points)-1; i++ {
		var pts [2]Point

		// Pre-clip the line to fit in a Fixed, so just chop the line.
		if !lineIntersect(&[2]Point{points[i], points[i+1]}, &fixedBounds, &pts) {
			continue
		}

		if clipBounds != nil {
			tmp := pts
			if !lineIntersect(&tmp, clipBounds, &pts) {
				continue
			}
		}

		x0 := fdot6FromF32(pts[0].X)
		y0 := fdot6FromF32(pts[0].Y)
		x1 := fdot6FromF32(pts[1].X)
		y1 := fdot6FromF32(pts[1].Y)

		if clip != nil {
			left := min(x0, x1)
			top := min(y0, y1)
			right := max(x0, x1)
			bottom := max(y0, y1)

			ir, ok := IntRectFromLTRB(
				fdot6Floor(left)-1,
				fdot6Floor(top)-1,
				fdot6Ceil(right)+1,
				fdot6Ceil(bottom)+1,
			)
			if !ok {
				return
			}

			if _, ok := clip.ToIntRect().Intersect(ir); !ok {
				continue
			}

			if !clip.ToIntRect().Contains(ir) {
				if r, ok := ir.Intersect(clip.ToIntRect()); ok {
					if subclip, ok := r.toScreenIntRect(); ok {
						doAntiHairline(x0, y0, x1, y1, &subclip, b)
					}
				}
				continue
			}

			// fall through to the no-clip case
		}

		doAntiHairline(x0, y0, x1, y1, nil, b)
	}
}

type antiHairBlitterKind uint8

const (
	antiHairKindHLine antiHairBlitterKind = iota
	antiHairKindHorish
	antiHairKindVLine
	antiHairKindVertish
)

func doAntiHairline(x0, y0, x1, y1 fDot6, clipOpt *screenIntRect, b blitter) {
	// Integer NaN (0x80000000) cannot be negated. It typically comes from a huge float (inf or nan)
	// converted to int; just don't draw.
	if antiHairAnyBadInts(x0, y0, x1, y1) != 0 {
		return
	}

	// The caller must clip the line to [-32767.0 ... 32767.0] ahead of time (in dot6 format).

	if hairAbsI32(x1-x0) > fdot6FromI32(511) || hairAbsI32(y1-y0) > fdot6FromI32(511) {
		// Shift each half separately instead of (x0 + x1) >> 1: less precise, but the intermediate
		// result cannot overflow for huge values.
		hx := (x0 >> 1) + (x1 >> 1)
		hy := (y0 >> 1) + (y1 >> 1)
		doAntiHairline(x0, y0, hx, hy, clipOpt, b)
		doAntiHairline(hx, hy, x1, y1, clipOpt, b)
		return
	}

	var scaleStart, scaleStop, istart, istop int32
	var fstart, slope fDot16
	var kind antiHairBlitterKind

	if hairAbsI32(x1-x0) > hairAbsI32(y1-y0) {
		// mostly horizontal
		if x0 > x1 {
			// go left-to-right
			x0, x1 = x1, x0
			y0, y1 = y1, y0
		}

		istart = fdot6Floor(x0)
		istop = fdot6Ceil(x1)
		fstart = fdot6ToFdot16(y0)
		if y0 == y1 {
			// completely horizontal, take fast case
			slope = 0
			kind = antiHairKindHLine
		} else {
			slope = fdot16FastDiv(y1-y0, x1-x0)
			fstart += (slope*(32-(x0&63)) + 32) >> 6
			kind = antiHairKindHorish
		}

		if istop-istart == 1 {
			// within a single pixel
			scaleStart = x1 - x0
			scaleStop = 0
		} else {
			scaleStart = 64 - (x0 & 63)
			scaleStop = x1 & 63
		}

		if clipOpt != nil {
			clip := clipOpt.ToIntRect()

			if istart >= clip.Right() || istop <= clip.Left() {
				return
			}

			if istart < clip.Left() {
				fstart += slope * (clip.Left() - istart)
				istart = clip.Left()
				scaleStart = 64
				if istop-istart == 1 {
					// within a single pixel
					scaleStart = antiHairContribution64(x1)
					scaleStop = 0
				}
			}

			if istop > clip.Right() {
				istop = clip.Right()
				scaleStop = 0 // so this last column is not drawn
			}

			if istart == istop {
				return
			}

			// Are the Y values completely inside the clip?
			var top, bottom int32
			if slope >= 0 {
				// T2B
				top = fdot16FloorToI32(fstart - fdot16Half)
				bottom = fdot16CeilToI32(fstart + (istop-istart-1)*slope + fdot16Half)
			} else {
				// B2T
				bottom = fdot16CeilToI32(fstart + fdot16Half)
				top = fdot16FloorToI32(fstart + (istop-istart-1)*slope - fdot16Half)
			}

			top--
			bottom++

			if top >= clip.Bottom() || bottom <= clip.Top() {
				return
			}

			if clip.Top() <= top && clip.Bottom() >= bottom {
				clipOpt = nil
			}
		}
	} else {
		// mostly vertical
		if y0 > y1 {
			// go top-to-bottom
			x0, x1 = x1, x0
			y0, y1 = y1, y0
		}

		istart = fdot6Floor(y0)
		istop = fdot6Ceil(y1)
		fstart = fdot6ToFdot16(x0)
		if x0 == x1 {
			if y0 == y1 {
				return // zero length, nothing to do
			}
			slope = 0
			kind = antiHairKindVLine
		} else {
			slope = fdot16FastDiv(x1-x0, y1-y0)
			fstart += (slope*(32-(y0&63)) + 32) >> 6
			kind = antiHairKindVertish
		}

		if istop-istart == 1 {
			// within a single pixel
			scaleStart = y1 - y0
			scaleStop = 0
		} else {
			scaleStart = 64 - (y0 & 63)
			scaleStop = y1 & 63
		}

		if clipOpt != nil {
			clip := clipOpt.ToIntRect()

			if istart >= clip.Bottom() || istop <= clip.Top() {
				return
			}

			if istart < clip.Top() {
				fstart += slope * (clip.Top() - istart)
				istart = clip.Top()
				scaleStart = 64
				if istop-istart == 1 {
					// within a single pixel
					scaleStart = antiHairContribution64(y1)
					scaleStop = 0
				}
			}
			if istop > clip.Bottom() {
				istop = clip.Bottom()
				scaleStop = 0 // so this last row is not drawn
			}

			if istart == istop {
				return
			}

			// Are the X values completely inside the clip?
			var left, right int32
			if slope >= 0 {
				// L2R
				left = fdot16FloorToI32(fstart - fdot16Half)
				right = fdot16CeilToI32(fstart + (istop-istart-1)*slope + fdot16Half)
			} else {
				// R2L
				right = fdot16CeilToI32(fstart + fdot16Half)
				left = fdot16FloorToI32(fstart + (istop-istart-1)*slope - fdot16Half)
			}

			left--
			right++

			if left >= clip.Right() || right <= clip.Left() {
				return
			}

			if clip.Left() <= left && clip.Right() >= right {
				clipOpt = nil
			}
		}
	}

	if clipOpt != nil {
		b = &antiHairRectClipBlitter{b: b, clip: *clipOpt}
	}

	var hb antiHairBlitter
	switch kind {
	case antiHairKindHLine:
		hb = antiHairHLine{b}
	case antiHairKindHorish:
		hb = antiHairHorish{b}
	case antiHairKindVLine:
		hb = antiHairVLine{b}
	default:
		hb = antiHairVertish{b}
	}

	ustart := uint32(istart)
	ustop := uint32(istop)

	fstart = hb.drawCap(ustart, fstart, slope, scaleStart)
	ustart++
	var stopAdj uint32
	if scaleStop > 0 {
		stopAdj = 1
	}
	fullSpans := ustop - ustart - stopAdj
	if fullSpans > 0 {
		fstart = hb.drawLine(ustart, ustart+fullSpans, fstart, slope)
	}

	if scaleStop > 0 {
		hb.drawCap(ustop-1, fstart, slope, scaleStop)
	}
}

// antiHairBadInt returns the high bit set if x == 0x80000000.
func antiHairBadInt(x int32) int32 { return x & -x }

func antiHairAnyBadInts(a, b, c, d int32) int32 {
	return (antiHairBadInt(a) | antiHairBadInt(b) | antiHairBadInt(c) | antiHairBadInt(d)) >> 31
}

// antiHairContribution64 returns the fractional part of ordinate, with multiples of 64 giving 64, not 0.
func antiHairContribution64(ordinate fDot6) int32 { return ((ordinate - 1) & 63) + 1 }

type antiHairBlitter interface {
	drawCap(x uint32, fy, slope fDot16, mod64 int32) fDot16
	drawLine(x, stopX uint32, fy, slope fDot16) fDot16
}

func antiHairI32ToAlpha(a int32) uint8 { return uint8(a & 0xFF) }

type antiHairHLine struct{ b blitter }

func (h antiHairHLine) drawCap(x uint32, fy, _ fDot16, mod64 int32) fDot16 {
	fy += fdot16One / 2
	fy = max(fy, 0)

	y := uint32(fy >> 16)
	a := antiHairI32ToAlpha(fy >> 8)

	// lower line
	ma := fdot6SmallScale(a, mod64)
	if ma != 0 {
		antiHairCallHLineBlitter(x, y, true, 1, ma, h.b)
	}

	// upper line
	ma = fdot6SmallScale(255-a, mod64)
	if ma != 0 {
		antiHairCallHLineBlitter(x, y-1, y != 0, 1, ma, h.b)
	}

	return fy - fdot16One/2
}

func (h antiHairHLine) drawLine(x, stopX uint32, fy, _ fDot16) fDot16 {
	count := stopX - x
	if count == 0 {
		return fy
	}

	fy += fdot16One / 2
	fy = max(fy, 0)

	y := uint32(fy >> 16)
	a := antiHairI32ToAlpha(fy >> 8)

	// lower line
	if a != 0 {
		antiHairCallHLineBlitter(x, y, true, count, a, h.b)
	}

	// upper line
	a = 255 - a
	if a != 0 {
		antiHairCallHLineBlitter(x, y-1, y != 0, count, a, h.b)
	}

	return fy - fdot16One/2
}

type antiHairHorish struct{ b blitter }

func (h antiHairHorish) drawCap(x uint32, fy, dy fDot16, mod64 int32) fDot16 {
	fy += fdot16One / 2
	fy = max(fy, 0)

	lowerY := uint32(fy >> 16)
	a := antiHairI32ToAlpha(fy >> 8)
	a0 := fdot6SmallScale(255-a, mod64)
	a1 := fdot6SmallScale(a, mod64)
	h.b.blitAntiV2(x, max(lowerY, 1)-1, a0, a1)

	return fy + dy - fdot16One/2
}

func (h antiHairHorish) drawLine(x, stopX uint32, fy, dy fDot16) fDot16 {
	fy += fdot16One / 2
	for {
		fy = max(fy, 0)
		lowerY := uint32(fy >> 16)
		a := antiHairI32ToAlpha(fy >> 8)
		h.b.blitAntiV2(x, max(lowerY, 1)-1, 255-a, a)
		fy += dy

		x++
		if x >= stopX {
			break
		}
	}

	return fy - fdot16One/2
}

type antiHairVLine struct{ b blitter }

func (v antiHairVLine) drawCap(y uint32, fx, _ fDot16, mod64 int32) fDot16 {
	fx += fdot16One / 2
	fx = max(fx, 0)

	x := uint32(fx >> 16)
	a := antiHairI32ToAlpha(fx >> 8)

	ma := fdot6SmallScale(a, mod64)
	if ma != 0 {
		v.b.blitV(x, y, 1, ma)
	}

	ma = fdot6SmallScale(255-a, mod64)
	if ma != 0 {
		v.b.blitV(max(x, 1)-1, y, 1, ma)
	}

	return fx - fdot16One/2
}

func (v antiHairVLine) drawLine(y, stopY uint32, fx, _ fDot16) fDot16 {
	height := stopY - y
	if height == 0 {
		return fx
	}

	fx += fdot16One / 2
	fx = max(fx, 0)

	x := uint32(fx >> 16)
	a := antiHairI32ToAlpha(fx >> 8)

	if a != 0 {
		v.b.blitV(x, y, height, a)
	}

	a = 255 - a
	if a != 0 {
		v.b.blitV(max(x, 1)-1, y, height, a)
	}

	return fx - fdot16One/2
}

type antiHairVertish struct{ b blitter }

func (v antiHairVertish) drawCap(y uint32, fx, dx fDot16, mod64 int32) fDot16 {
	fx += fdot16One / 2
	fx = max(fx, 0)

	x := uint32(fx >> 16)
	a := antiHairI32ToAlpha(fx >> 8)
	v.b.blitAntiH2(max(x, 1)-1, y, fdot6SmallScale(255-a, mod64), fdot6SmallScale(a, mod64))

	return fx + dx - fdot16One/2
}

func (v antiHairVertish) drawLine(y, stopY uint32, fx, dx fDot16) fDot16 {
	fx += fdot16One / 2
	for {
		fx = max(fx, 0)
		x := uint32(fx >> 16)
		a := antiHairI32ToAlpha(fx >> 8)
		v.b.blitAntiH2(max(x, 1)-1, y, 255-a, a)
		fx += dx

		y++
		if y >= stopY {
			break
		}
	}

	return fx - fdot16One/2
}

// antiHairRectClipBlitter clips the blits of the anti-aliased hairline blitters to a rect
// (RectClipBlitter).
type antiHairRectClipBlitter struct {
	unreachableBlitter
	b    blitter
	clip screenIntRect
}

func (c *antiHairRectClipBlitter) blitAntiH(x, y uint32, antialias []uint8, runs []uint16) {
	yInRect := (y - c.clip.Top()) < c.clip.Height()
	if !yInRect || x >= c.clip.Right() {
		return
	}

	x0 := x
	x1 := x + antiHairComputeWidth(runs)

	if x1 <= c.clip.Left() {
		return
	}

	if x0 < c.clip.Left() {
		dx := c.clip.Left() - x0
		alphaRunsBreakAt(antialias, runs, int32(dx))
		antialias = antialias[dx:]
		runs = runs[dx:]
		x0 = c.clip.Left()
	}

	if x1 > c.clip.Right() {
		x1 = c.clip.Right()
		alphaRunsBreakAt(antialias, runs, int32(x1-x0))
		runs[x1-x0] = 0
	}

	c.b.blitAntiH(x0, y, antialias, runs)
}

func (c *antiHairRectClipBlitter) blitV(x, y, height uint32, alpha uint8) {
	xInRect := (x - c.clip.Left()) < c.clip.Width()
	if !xInRect {
		return
	}

	y0 := y
	y1 := y + height

	if y0 < c.clip.Top() {
		y0 = c.clip.Top()
	}
	if y1 > c.clip.Bottom() {
		y1 = c.clip.Bottom()
	}

	if y0 < y1 {
		c.b.blitV(x, y0, y1-y0, alpha)
	}
}

func (c *antiHairRectClipBlitter) blitAntiH2(x, y uint32, alpha0, alpha1 uint8) {
	c.blitAntiH(x, y, []uint8{alpha0, alpha1}, []uint16{1, 1, 0})
}

func (c *antiHairRectClipBlitter) blitAntiV2(x, y uint32, alpha0, alpha1 uint8) {
	c.blitAntiH(x, y, []uint8{alpha0}, []uint16{1, 0})
	c.blitAntiH(x, y+1, []uint8{alpha1}, []uint16{1, 0})
}

func antiHairComputeWidth(runs []uint16) uint32 {
	i := 0
	var width uint32
	for runs[i] != 0 {
		count := runs[i]
		width += uint32(count)
		i += int(count)
	}
	return width
}
