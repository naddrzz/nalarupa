package skia

// Anti-aliased path filling by supersampling (scan/path_aa.rs).

// pathAAShift controls how much we super-sample.
const (
	pathAAShift uint32 = 2
	pathAAScale uint32 = 1 << pathAAShift
	pathAAMask  uint32 = pathAAScale - 1
)

// fillPathAA fills path into clip through b with anti-aliasing.
func fillPathAA(path *Path, fillRule FillRule, clip screenIntRect, b blitter) {
	// Unlike path.bounds.round_out(), this rounds out first and then converts into a Rect.
	bounds := path.Bounds()
	r, ok := RectFromLTRB(floor32(bounds.Left()), floor32(bounds.Top()), ceil32(bounds.Right()), ceil32(bounds.Bottom()))
	if !ok {
		return
	}
	ir, ok := r.RoundOut()
	if !ok {
		return
	}

	// If the intersection of the path bounds and the clip bounds overflows 32767 when shifted by
	// pathAAShift, we can't supersample, so draw without anti-aliasing.
	clippedIR, ok := ir.Intersect(clip.ToIntRect())
	if !ok {
		return
	}
	if rectOverflowsShortShift(clippedIR, int32(pathAAShift)) != 0 {
		fillPath(path, fillRule, clip, b)
		return
	}

	// Our anti-aliasing can't handle a clip larger than 32767 (Skia limits the clip to it).
	const maxClipCoord = 32767
	if clip.Right() > maxClipCoord || clip.Bottom() > maxClipCoord {
		return
	}

	pathAAFillPathImpl(path, fillRule, ir, clip, b)
}

// rectOverflowsShortShift reports (non-zero) whether any coordinate of rect would not fit in an int16
// when shifted left by shift.
func rectOverflowsShortShift(rect IntRect, shift int32) int32 {
	return overflowsShortShift(rect.Left(), shift) |
		overflowsShortShift(rect.Top(), shift) |
		overflowsShortShift(rect.Right(), shift) |
		overflowsShortShift(rect.Bottom(), shift)
}

func overflowsShortShift(value, shift int32) int32 {
	s := 16 + shift
	return (leftShift(value, s) >> s) - value
}

func pathAAFillPathImpl(path *Path, fillRule FillRule, bounds IntRect, clip screenIntRect, b blitter) {
	sb, ok := newPathAASuperBlitter(bounds, clip, b)
	if !ok {
		return // clipped out, nothing else to do
	}

	// If the bounds cannot be converted into a screenIntRect, the path is out of clip.
	pathContainedInClip := false
	if sbounds, ok := bounds.toScreenIntRect(); ok {
		pathContainedInClip = clip.Contains(sbounds)
	}

	fillPathImpl(path, fillRule, clip, bounds.Top(), bounds.Bottom(), int32(pathAAShift), pathContainedInClip, sb)
	sb.flush() // Rust flushes in Drop
}

// pathAASuperBlitter is SuperBlitter with its BaseSuperBlitter fields: it accumulates supersampled
// rows in alpha runs and blits each complete row through realBlitter.
type pathAASuperBlitter struct {
	unreachableBlitter

	realBlitter blitter
	// currIY is the current y coordinate, in destination coordinates.
	currIY int32
	// width is the widest row of the region to be blitted, in destination coordinates.
	width uint32
	// left is the leftmost x coordinate in any row, in destination coordinates.
	left uint32
	// superLeft is the leftmost x coordinate in any row, in supersampled coordinates.
	superLeft uint32
	// currY is the current y coordinate in supersampled coordinates.
	currY int32
	// top is the initial y coordinate (top of bounds).
	top int32

	runs    *alphaRuns
	offsetX int
}

func newPathAASuperBlitter(bounds IntRect, clipRect screenIntRect, b blitter) (*pathAASuperBlitter, bool) {
	isect, ok := bounds.Intersect(clipRect.ToIntRect())
	if !ok {
		return nil, false
	}
	sect, ok := isect.toScreenIntRect()
	if !ok {
		return nil, false
	}
	return &pathAASuperBlitter{
		realBlitter: b,
		currIY:      int32(sect.Top()) - 1,
		width:       sect.Width(),
		left:        sect.Left(),
		superLeft:   sect.Left() << pathAAShift,
		currY:       int32(sect.Top()<<pathAAShift) - 1,
		top:         int32(sect.Top()),
		runs:        newAlphaRuns(sect.Width()),
	}, true
}

// flush blits out a complete supersampled row in runs through the wrapped blitter.
func (s *pathAASuperBlitter) flush() {
	if s.currIY >= s.top {
		if !s.runs.isEmpty() {
			s.realBlitter.blitAntiH(s.left, uint32(s.currIY), s.runs.alpha, s.runs.runs)
			s.runs.reset(s.width)
			s.offsetX = 0
		}
		s.currIY = s.top - 1
	}
}

// blitH blits a row of pixels, with location and width in supersampled coordinates.
func (s *pathAASuperBlitter) blitH(x, y, width uint32) {
	iy := int32(y >> pathAAShift)

	// hack, until I figure out why my cubics (I think) go beyond the bounds
	if x >= s.superLeft {
		x -= s.superLeft
	} else {
		width = x + width
		x = 0
	}

	if s.currY != int32(y) {
		s.offsetX = 0
		s.currY = int32(y)
	}

	if iy != s.currIY {
		// new scanline
		s.flush()
		s.currIY = iy
	}

	start := x
	stop := x + width

	// integer-pixel-aligned ends of blit, rounded out
	fb := start & pathAAMask
	fe := stop & pathAAMask
	n := (int32(stop) >> pathAAShift) - (int32(start) >> pathAAShift) - 1

	if n < 0 {
		fb = fe - fb
		n = 0
		fe = 0
	} else if fb == 0 {
		n++
	} else {
		fb = pathAAScale - fb
	}

	maxValue := uint8((1 << (8 - pathAAShift)) - (((y & pathAAMask) + 1) >> pathAAShift))
	s.offsetX = s.runs.add(
		x>>pathAAShift,
		pathAACoverageToPartialAlpha(fb),
		int(n),
		pathAACoverageToPartialAlpha(fe),
		maxValue,
		s.offsetX,
	)
}

// pathAACoverageToPartialAlpha scales coverage for alphaRuns, which accumulates pathAAScale pixels
// worth of alpha in [0, 256/pathAAScale] and clamps 256 to 255 itself.
func pathAACoverageToPartialAlpha(aa uint32) uint8 {
	aa <<= 8 - 2*pathAAShift
	return uint8(aa)
}
