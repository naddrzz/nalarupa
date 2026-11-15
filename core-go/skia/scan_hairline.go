package skia

import (
	"math"
	mbits "math/bits"
)

// Aliased hairlines (scan/hairline.rs).

// hairLineProc draws the polyline points; clip is nil when no clipping is needed (LineProc).
type hairLineProc func(points []Point, clip *screenIntRect, b blitter)

const (
	hairMaxCubicSubdivideLevel = 9
	hairMaxQuadSubdivideLevel  = 5
)

// strokeHairline strokes a path with a one pixel wide aliased line (hairline::stroke_path).
func strokeHairline(path *Path, lineCap LineCap, clip screenIntRect, b blitter) {
	strokeHairlineImpl(path, lineCap, clip, hairLineRgn, b)
}

func hairLineRgn(points []Point, clip *screenIntRect, b blitter) {
	const maxV = 32767.0
	fixedBounds, _ := RectFromLTRB(-maxV, -maxV, maxV, maxV)

	var clipBounds *Rect
	if clip != nil {
		r := clip.ToRect()
		clipBounds = &r
	}

	for i := 0; i < len(points)-1; i++ {
		var pts [2]Point

		// Pre-clip the line to fit in a Fixed, so just chop the line.
		if !lineIntersect(&[2]Point{points[i], points[i+1]}, &fixedBounds, &pts) {
			continue
		}

		if clipBounds != nil {
			tmp := pts
			// Clip in scalar space, so huge values which might be missed after converting to FDot6
			// (overflow) are caught.
			if !lineIntersect(&tmp, clipBounds, &pts) {
				continue
			}
		}

		x0 := fdot6FromF32(pts[0].X)
		y0 := fdot6FromF32(pts[0].Y)
		x1 := fdot6FromF32(pts[1].X)
		y1 := fdot6FromF32(pts[1].Y)

		dx := x1 - x0
		dy := y1 - y0

		if hairAbsI32(dx) > hairAbsI32(dy) {
			// mostly horizontal
			if x0 > x1 {
				// go left-to-right
				x0, x1 = x1, x0
				y0, y1 = y1, y0
			}

			ix0 := fdot6Round(x0)
			ix1 := fdot6Round(x1)
			if ix0 == ix1 {
				continue // too short to draw
			}

			slope := fdot16Div(dy, dx)
			startY := fdot6ToFdot16(y0) + ((slope * ((32 - x0) & 63)) >> 6)

			// tiny-skia specific: rounding can make startY equal to the image height, which would
			// write outside the buffer.
			maxY := int32(math.MaxInt32)
			if clipBounds != nil {
				maxY = fdot16FromF32(clipBounds.Bottom())
			}

			for {
				if ix0 >= 0 && startY >= 0 && startY < maxY {
					b.blitH(uint32(ix0), uint32(startY>>16), 1)
				}

				startY += slope
				ix0++
				if ix0 >= ix1 {
					break
				}
			}
		} else {
			// mostly vertical
			if y0 > y1 {
				// go top-to-bottom
				x0, x1 = x1, x0
				y0, y1 = y1, y0
			}

			iy0 := fdot6Round(y0)
			iy1 := fdot6Round(y1)
			if iy0 == iy1 {
				continue // too short to draw
			}

			slope := fdot16Div(dx, dy)
			startX := fdot6ToFdot16(x0) + ((slope * ((32 - y0) & 63)) >> 6)

			for {
				if startX >= 0 && iy0 >= 0 {
					b.blitH(uint32(startX>>16), uint32(iy0), 1)
				}

				startX += slope
				iy0++
				if iy0 >= iy1 {
					break
				}
			}
		}
	}
}

// hairAbsI32 is i32::abs in a release build: MIN wraps to itself.
func hairAbsI32(x int32) int32 {
	if x < 0 {
		return -x
	}
	return x
}

// strokeHairlineImpl walks the path segments, extending the ends for caps, and draws them with lineProc.
func strokeHairlineImpl(path *Path, lineCap LineCap, clip screenIntRect, lineProc hairLineProc, b blitter) {
	var insetClip, outsetClip *IntRect

	{
		capOut := float32(1.0)
		if lineCap != LineCapButt {
			capOut = 2.0
		}
		r, ok := path.Bounds().Outset(capOut, capOut)
		if !ok {
			return
		}
		ibounds, ok := r.RoundOut()
		if !ok {
			return
		}
		if _, ok := clip.ToIntRect().Intersect(ibounds); !ok {
			return
		}

		if !clip.ToIntRect().Contains(ibounds) {
			// Two scalar rects for culling per segment. A hairline can draw up to 1 pixel beyond the
			// control points' bounds (aa-hairs), so insetClip (quick-accept: the segment is not
			// clipped) is inset from the clip bounds and outsetClip (quick-reject: the segment is
			// entirely outside) is outset.
			o, ok := clip.ToIntRect().MakeOutset(1, 1)
			if !ok {
				return
			}
			outsetClip = &o
			in, ok := clip.ToIntRect().Inset(1, 1)
			if !ok {
				return
			}
			insetClip = &in
		}
	}

	clipP := &clip
	prevVerb := VerbMove
	var firstPt, lastPt Point

	iter := path.Segments()
	for {
		seg, ok := iter.Next()
		if !ok {
			break
		}
		verb := iter.CurrVerb()
		nextVerb, hasNext := iter.NextVerb()
		var lastPt2 Point
		switch seg.Verb {
		case VerbMove:
			p := seg.Pts[0]
			firstPt = p
			lastPt = p
			lastPt2 = p
		case VerbLine:
			p := seg.Pts[0]
			points := [2]Point{lastPt, p}
			if lineCap != LineCapButt {
				hairExtendPts(lineCap, prevVerb, nextVerb, hasNext, points[:])
			}
			lineProc(points[:], clipP, b)
			lastPt = p
			lastPt2 = points[0]
		case VerbQuad:
			p0, p1 := seg.Pts[0], seg.Pts[1]
			points := [3]Point{lastPt, p0, p1}
			if lineCap != LineCapButt {
				hairExtendPts(lineCap, prevVerb, nextVerb, hasNext, points[:])
			}
			hairQuad(&points, clipP, insetClip, outsetClip, hairComputeQuadLevel(&points), lineProc, b)
			lastPt = p1
			lastPt2 = points[0]
		case VerbCubic:
			p0, p1, p2 := seg.Pts[0], seg.Pts[1], seg.Pts[2]
			points := [4]Point{lastPt, p0, p1, p2}
			if lineCap != LineCapButt {
				hairExtendPts(lineCap, prevVerb, nextVerb, hasNext, points[:])
			}
			hairCubic(&points, clipP, insetClip, outsetClip, lineProc, b)
			lastPt = p2
			lastPt2 = points[0]
		case VerbClose:
			points := [2]Point{lastPt, firstPt}
			if lineCap != LineCapButt && prevVerb == VerbMove {
				// cap moveTo/close to match svg expectations for degenerate segments
				hairExtendPts(lineCap, prevVerb, nextVerb, hasNext, points[:])
			}
			lineProc(points[:], clipP, b)
			lastPt2 = points[0]
		}

		if lineCap != LineCapButt {
			if prevVerb == VerbMove && (verb == VerbLine || verb == VerbQuad || verb == VerbCubic) {
				firstPt = lastPt2 // the curve moved the initial point, so close to it instead
			}
			prevVerb = verb
		}
	}
}

// hairExtendPts extends the points in the direction of the starting or ending tangent by 1/2 unit for a
// round or square cap. If the end point and the control point coincide, the next control point gives the
// tangent; a degenerate curve moves the cap out 1/2 unit horizontally. hasNext is false where Rust's
// next_verb is None.
func hairExtendPts(lineCap LineCap, prevVerb, nextVerb PathVerb, hasNext bool, points []Point) {
	// The area of a circle is PI*R*R. For a unit circle, R=1/2, and the cap covers half of that.
	capOutset := floatPI / 8.0
	if lineCap == LineCapSquare {
		capOutset = 0.5
	}

	if prevVerb == VerbMove {
		first := points[0]
		offset := 0
		controls := len(points) - 1
		var tangent Point
		for {
			offset++
			tangent = first.Sub(points[offset])
			if !tangent.IsZero() {
				break
			}
			controls--
			if controls == 0 {
				break
			}
		}

		if tangent.IsZero() {
			tangent = Point{1, 0}
			controls = len(points) - 1 // If all points are equal, move all but one.
		} else {
			tangent.Normalize()
		}

		offset = 0
		for {
			// If the end point and control points are equal, move them in tandem.
			points[offset].X += float32(tangent.X * capOutset)
			points[offset].Y += float32(tangent.Y * capOutset)

			offset++
			controls++
			if controls >= len(points) {
				break
			}
		}
	}

	if !hasNext || nextVerb == VerbMove || nextVerb == VerbClose {
		last := points[len(points)-1]
		offset := len(points) - 1
		controls := len(points) - 1
		var tangent Point
		for {
			offset--
			tangent = last.Sub(points[offset])
			if !tangent.IsZero() {
				break
			}
			controls--
			if controls == 0 {
				break
			}
		}

		if tangent.IsZero() {
			tangent = Point{-1, 0}
			controls = len(points) - 1
		} else {
			tangent.Normalize()
		}

		offset = len(points) - 1
		for {
			points[offset].X += float32(tangent.X * capOutset)
			points[offset].Y += float32(tangent.Y * capOutset)

			offset--
			controls++
			if controls >= len(points) {
				break
			}
		}
	}
}

func hairQuad(points *[3]Point, clip *screenIntRect, insetClip, outsetClip *IntRect, level uint8, lineProc hairLineProc, b blitter) {
	if insetClip != nil {
		inset := insetClip.ToRect()
		if outsetClip == nil {
			return
		}
		outset := outsetClip.ToRect()

		bounds, ok := hairComputeNocheckBounds(points[:])
		if !ok {
			return
		}
		if !hairGeometricOverlap(&outset, &bounds) {
			return // nothing to do
		} else if hairGeometricContains(&inset, &bounds) {
			clip = nil
		}
	}

	hairQuad2(points, clip, level, lineProc, b)
}

// hairComputeNocheckBounds is compute_nocheck_quad_bounds and compute_nocheck_cubic_bounds.
func hairComputeNocheckBounds(points []Point) (Rect, bool) {
	mn := points[0]
	mx := mn
	for _, p := range points[1:] {
		mn = Point{hairPMin(mn.X, p.X), hairPMin(mn.Y, p.Y)}
		mx = Point{hairPMax(mx.X, p.X), hairPMax(mx.Y, p.Y)}
	}
	return RectFromLTRB(mn.X, mn.Y, mx.X, mx.Y)
}

// hairPMin and hairPMax are tiny-skia-path's f32x2 pmin and pmax, which ignore NaN.
func hairPMin(a, b float32) float32 {
	if b < a {
		return b
	}
	return a
}

func hairPMax(a, b float32) float32 {
	if a < b {
		return b
	}
	return a
}

func hairGeometricOverlap(a, b *Rect) bool {
	return a.Left() < b.Right() && b.Left() < a.Right() && a.Top() < b.Bottom() && b.Top() < a.Bottom()
}

func hairGeometricContains(outer, inner *Rect) bool {
	return inner.Right() <= outer.Right() && inner.Left() >= outer.Left() &&
		inner.Bottom() <= outer.Bottom() && inner.Top() >= outer.Top()
}

func hairQuad2(points *[3]Point, clip *screenIntRect, level uint8, lineProc hairLineProc, b blitter) {
	coeff := quadCoeffFromPoints(points)

	const maxPoints = (1 << hairMaxQuadSubdivideLevel) + 1
	lines := 1 << level

	var tmp [maxPoints]Point
	tmp[0] = points[0]

	var t Point
	dt := splat(1.0 / float32(lines))
	for i := 1; i < lines; i++ {
		t = t.Add(dt)
		tmp[i] = coeff.eval(t)
	}

	tmp[lines] = points[2]
	lineProc(tmp[:lines+1], clip, b)
}

func hairComputeQuadLevel(points *[3]Point) uint8 {
	d := hairComputeIntQuadDist(points)
	// Quadratics approach the line connecting their start and end points 4x closer with each
	// subdivision, so the level is the minimum needed to get that distance below a pixel.
	level := uint32(33-mbits.LeadingZeros32(d)) >> 1
	// sanity check on level (from the previous version)
	if level > hairMaxQuadSubdivideLevel {
		level = hairMaxQuadSubdivideLevel
	}
	return uint8(level)
}

func hairComputeIntQuadDist(points *[3]Point) uint32 {
	// The vector between the control point and the middle of the line connecting the end points.
	dx := abs32(half(points[0].X+points[2].X) - points[1].X)
	dy := abs32(half(points[0].Y+points[2].Y) - points[1].Y)

	// Whole pixel values (ceiling to be conservative), unsigned so half of the smaller can be added.
	idx := uint32(saturateFromF32(ceil32(dx)))
	idy := uint32(saturateFromF32(ceil32(dy)))

	// cheap approximation of the distance
	if idx > idy {
		return idx + (idy >> 1)
	}
	return idy + (idx >> 1)
}

func hairCubic(points *[4]Point, clip *screenIntRect, insetClip, outsetClip *IntRect, lineProc hairLineProc, b blitter) {
	if insetClip != nil {
		inset := insetClip.ToRect()
		if outsetClip == nil {
			return
		}
		outset := outsetClip.ToRect()

		bounds, ok := hairComputeNocheckBounds(points[:])
		if !ok {
			return
		}
		if !hairGeometricOverlap(&outset, &bounds) {
			return // nothing to do
		} else if hairGeometricContains(&inset, &bounds) {
			clip = nil
		}
	}

	if hairQuickCubicNicenessCheck(points) {
		hairCubic2(points, clip, lineProc, b)
	} else {
		var tmp [13]Point
		tValues := [3]float32{0.5, 0.5, 0.5} // new_t_values: NormalizedF32Exclusive::ANY

		count := chopCubicAtMaxCurvature(points, &tValues, tmp[:])
		for i := 0; i < count; i++ {
			offset := i * 3
			newPoints := [4]Point(tmp[offset : offset+4])
			hairCubic2(&newPoints, clip, lineProc, b)
		}
	}
}

// hairQuickCubicNicenessCheck reports whether the off-curve points are "inside" the limits of the
// on-curve points.
func hairQuickCubicNicenessCheck(points *[4]Point) bool {
	return hairLt90(points[1], points[0], points[3]) &&
		hairLt90(points[2], points[0], points[3]) &&
		hairLt90(points[1], points[3], points[0]) &&
		hairLt90(points[2], points[3], points[0])
}

func hairLt90(p0, pivot, p2 Point) bool { return p0.Sub(pivot).Dot(p2.Sub(pivot)) >= 0 }

func hairCubic2(points *[4]Point, clip *screenIntRect, lineProc hairLineProc, b blitter) {
	lines := hairComputeCubicSegments(points)
	if lines == 1 {
		lineProc([]Point{points[0], points[3]}, clip, b)
		return
	}

	coeff := cubicCoeffFromPoints(points)

	const maxPoints = (1 << hairMaxCubicSubdivideLevel) + 1
	var tmp [maxPoints]Point

	dt := splat(1.0 / float32(lines))
	var t Point

	tmp[0] = points[0]
	for i := 1; i < lines; i++ {
		t = t.Add(dt)
		tmp[i] = coeff.eval(t)
	}

	// Entries past lines are zero, so checking the prefix is the same as checking all of tmp.
	for _, p := range tmp[:lines] {
		if !p.IsFinite() {
			return // some point(s) are non-finite, so don't draw
		}
	}
	tmp[lines] = points[3]
	lineProc(tmp[:lines+1], clip, b)
}

func hairComputeCubicSegments(points *[4]Point) int {
	p0, p1, p2, p3 := points[0], points[1], points[2], points[3]

	oneThird := splat(1.0 / 3.0)
	twoThird := splat(2.0 / 3.0)

	p13 := oneThird.Mul(p3).Add(twoThird.Mul(p0))
	p23 := oneThird.Mul(p0).Add(twoThird.Mul(p3))

	d1, d2 := p1.Sub(p13), p2.Sub(p23)
	m := Point{hairPMax(abs32(d1.X), abs32(d2.X)), hairPMax(abs32(d1.Y), abs32(d2.Y))}
	diff := hairPMax(m.X, m.Y)
	tol := float32(1.0 / 8.0)

	for i := 0; i < hairMaxCubicSubdivideLevel; i++ {
		if diff < tol {
			return 1 << i
		}
		tol = float32(tol * 4.0)
	}

	return 1 << hairMaxCubicSubdivideLevel
}
