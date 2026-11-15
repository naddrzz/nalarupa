package skia

// Line clipping for scan conversion and hairlines (line_clipper.rs).

const lineClipperMaxPoints = 4

// lineClip clips the line src[0]..src[1] against clip, ignoring segments that lie completely above or
// below the clip. Portions to the left or right become vertical segments aligned to the clip's edge.
//
// It returns the used prefix of points: the end points of 0..3 segments stored sequentially (segment i
// is points[i]..points[i+1]).
func lineClip(src *[2]Point, clip *Rect, canCullToTheRight bool, points *[lineClipperMaxPoints]Point) []Point {
	index0, index1 := 1, 0
	if src[0].Y < src[1].Y {
		index0, index1 = 0, 1
	}

	// Completely clipped out in Y (above or below).
	if src[index1].Y <= clip.Top() {
		return nil
	}
	if src[index0].Y >= clip.Bottom() {
		return nil
	}

	// Chop in Y to produce a single segment, stored in tmp[0..1].
	tmp := *src

	if src[index0].Y < clip.Top() {
		tmp[index0] = Point{lineSectWithHorizontal(src, clip.Top()), clip.Top()}
	}
	if tmp[index1].Y > clip.Bottom() {
		tmp[index1] = Point{lineSectWithHorizontal(src, clip.Bottom()), clip.Bottom()}
	}

	// Chop it into 1..3 segments that are wholly within the clip in X.
	var resultStorage [lineClipperMaxPoints]Point
	lineCount := 1
	var reverse bool

	if src[0].X < src[1].X {
		index0, index1 = 0, 1
		reverse = false
	} else {
		index0, index1 = 1, 0
		reverse = true
	}

	var result []Point
	if tmp[index1].X <= clip.Left() {
		// wholly to the left
		tmp[0].X = clip.Left()
		tmp[1].X = clip.Left()
		reverse = false
		result = tmp[:]
	} else if tmp[index0].X >= clip.Right() {
		// wholly to the right
		if canCullToTheRight {
			return nil
		}
		tmp[0].X = clip.Right()
		tmp[1].X = clip.Right()
		reverse = false
		result = tmp[:]
	} else {
		offset := 0

		if tmp[index0].X < clip.Left() {
			resultStorage[offset] = Point{clip.Left(), tmp[index0].Y}
			offset++
			resultStorage[offset] = Point{clip.Left(), lineSectClampWithVertical(&tmp, clip.Left())}
		} else {
			resultStorage[offset] = tmp[index0]
		}
		offset++

		if tmp[index1].X > clip.Right() {
			resultStorage[offset] = Point{clip.Right(), lineSectClampWithVertical(&tmp, clip.Right())}
			offset++
			resultStorage[offset] = Point{clip.Right(), tmp[index1].Y}
		} else {
			resultStorage[offset] = tmp[index1]
		}

		lineCount = offset
		result = resultStorage[:]
	}

	// Copy the results into points.
	if reverse {
		// in reverse order to maintain winding order
		for i := 0; i <= lineCount; i++ {
			points[lineCount-i] = result[i]
		}
	} else {
		copy(points[:lineCount+1], result[:lineCount+1])
	}

	return points[:lineCount+1]
}

// lineSectWithHorizontal returns the X coordinate of the intersection with the horizontal line at y.
func lineSectWithHorizontal(src *[2]Point, y float32) float32 {
	dy := src[1].Y - src[0].Y
	if isNearlyZero(dy) {
		return ave(src[0].X, src[1].X)
	}
	// The extra precision keeps the value within the original limits.
	x0, y0 := float64(src[0].X), float64(src[0].Y)
	x1, y1 := float64(src[1].X), float64(src[1].Y)
	result := x0 + float64((float64(y)-y0)*(x1-x0))/(y1-y0)
	// The result might still exceed [x0..x1] due to rounding, so pin it.
	return float32(linePinUnsortedF64(result, x0, x1))
}

func lineSectClampWithVertical(src *[2]Point, x float32) float32 {
	y := lineSectWithVertical(src, x)
	// The caller expects y between src[0].y and src[1].y (unsorted); see skbug.com/7491.
	return linePinUnsortedF32(y, src[0].Y, src[1].Y)
}

// lineSectWithVertical returns the Y coordinate of the intersection with the vertical line at x.
func lineSectWithVertical(src *[2]Point, x float32) float32 {
	dx := src[1].X - src[0].X
	if isNearlyZero(dx) {
		return ave(src[0].Y, src[1].Y)
	}
	x0, y0 := float64(src[0].X), float64(src[0].Y)
	x1, y1 := float64(src[1].X), float64(src[1].Y)
	result := y0 + float64((float64(x)-x0)*(y1-y0))/(x1-x0)
	return float32(result)
}

func linePinUnsortedF32(value, limit0, limit1 float32) float32 {
	if limit1 < limit0 {
		limit0, limit1 = limit1, limit0
	}
	if value < limit0 {
		return limit0
	} else if value > limit1 {
		return limit1
	}
	return value
}

func linePinUnsortedF64(value, limit0, limit1 float64) float64 {
	if limit1 < limit0 {
		limit0, limit1 = limit1, limit0
	}
	if value < limit0 {
		return limit0
	} else if value > limit1 {
		return limit1
	}
	return value
}

// lineIntersect intersects the segment src with clip. If the result is non-empty it sets dst to it and
// returns true. Unlike lineClip, it adds no vertical segments on the sides.
func lineIntersect(src *[2]Point, clip *Rect, dst *[2]Point) bool {
	if bounds, ok := RectFromLTRB(
		minf(src[0].X, src[1].X),
		minf(src[0].Y, src[1].Y),
		maxf(src[0].X, src[1].X),
		maxf(src[0].Y, src[1].Y),
	); ok {
		if lineContainsNoEmptyCheck(clip, &bounds) {
			*dst = *src
			return true
		}

		// Check for no overlap, and only permit coincident edges if the line and the edge are
		// collinear.
		if lineNestedLT(bounds.Right(), clip.Left(), bounds.Width()) ||
			lineNestedLT(clip.Right(), bounds.Left(), bounds.Width()) ||
			lineNestedLT(bounds.Bottom(), clip.Top(), bounds.Height()) ||
			lineNestedLT(clip.Bottom(), bounds.Top(), bounds.Height()) {
			return false
		}
	}

	index0, index1 := 1, 0
	if src[0].Y < src[1].Y {
		index0, index1 = 0, 1
	}

	tmp := *src

	// Y intersections.
	if tmp[index0].Y < clip.Top() {
		tmp[index0] = Point{lineSectWithHorizontal(src, clip.Top()), clip.Top()}
	}
	if tmp[index1].Y > clip.Bottom() {
		tmp[index1] = Point{lineSectWithHorizontal(src, clip.Bottom()), clip.Bottom()}
	}

	index0, index1 = 1, 0
	if tmp[0].X < tmp[1].X {
		index0, index1 = 0, 1
	}

	// Quick-reject in X again, now that we may have been chopped.
	if tmp[index1].X <= clip.Left() || tmp[index0].X >= clip.Right() {
		// Not when the line is vertical and coincident with the clip.
		if tmp[0].X != tmp[1].X || tmp[0].X < clip.Left() || tmp[0].X > clip.Right() {
			return false
		}
	}

	if tmp[index0].X < clip.Left() {
		tmp[index0] = Point{clip.Left(), lineSectWithVertical(src, clip.Left())}
	}
	if tmp[index1].X > clip.Right() {
		tmp[index1] = Point{clip.Right(), lineSectWithVertical(src, clip.Right())}
	}

	*dst = tmp
	return true
}

func lineNestedLT(a, b, dim float32) bool { return a <= b && (a < b || dim > 0) }

// lineContainsNoEmptyCheck reports whether outer contains inner, even if inner is empty.
func lineContainsNoEmptyCheck(outer, inner *Rect) bool {
	return outer.Left() <= inner.Left() && outer.Top() <= inner.Top() &&
		outer.Right() >= inner.Right() && outer.Bottom() >= inner.Bottom()
}
