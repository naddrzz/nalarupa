package skia

// The rasteriser's curve geometry (tiny-skia's path_geometry.rs); the functions it re-exports from
// tiny-skia-path are in path_geometry.go. t values (NormalizedF32Exclusive) are plain float32.

// chopQuadAtXExtrema returns 0 for one quad and 1 for two, stored in dst; the quads are monotonic in X.
func chopQuadAtXExtrema(src *[3]Point, dst *[5]Point) int {
	a := src[0].X
	b := src[1].X
	c := src[2].X

	if quadIsNotMonotonic(a, b, c) {
		if t, ok := validUnitDivide(a-b, a-b-b+c); ok {
			chopQuadAt(src[:], t, dst)
			// flatten double quad extrema
			dst[1].X = dst[2].X
			dst[3].X = dst[2].X
			return 1
		}
		// Force dst to be monotonic, even though no unit_divide value was found (probably underflow).
		if abs32(a-b) < abs32(b-c) {
			b = a
		} else {
			b = c
		}
	}

	dst[0] = Point{a, src[0].Y}
	dst[1] = Point{b, src[1].Y}
	dst[2] = Point{c, src[2].Y}
	return 0
}

// chopQuadAtYExtrema returns 0 for one quad and 1 for two, stored in dst; the quads are monotonic in Y.
func chopQuadAtYExtrema(src *[3]Point, dst *[5]Point) int {
	a := src[0].Y
	b := src[1].Y
	c := src[2].Y

	if quadIsNotMonotonic(a, b, c) {
		if t, ok := validUnitDivide(a-b, a-b-b+c); ok {
			chopQuadAt(src[:], t, dst)
			// flatten double quad extrema
			dst[1].Y = dst[2].Y
			dst[3].Y = dst[2].Y
			return 1
		}
		if abs32(a-b) < abs32(b-c) {
			b = a
		} else {
			b = c
		}
	}

	dst[0] = Point{src[0].X, a}
	dst[1] = Point{src[1].X, b}
	dst[2] = Point{src[2].X, c}
	return 0
}

func quadIsNotMonotonic(a, b, c float32) bool {
	ab := a - b
	bc := b - c
	if ab < 0 {
		bc = -bc
	}
	return ab == 0 || bc < 0
}

// chopCubicAtXExtrema chops a cubic into 1, 2 or 3 cubics monotonic in X and returns how many chops
// it made (dst[0..4], dst[3..7], dst[6..10]).
func chopCubicAtXExtrema(src *[4]Point, dst *[10]Point) int {
	var tValues [3]float32
	n := findCubicExtrema(src[0].X, src[1].X, src[2].X, src[3].X, &tValues)

	chopCubicAt(src, tValues[:n], dst[:])
	if n > 0 {
		// make the X extrema flat
		dst[2].X = dst[3].X
		dst[4].X = dst[3].X
		if n == 2 {
			dst[5].X = dst[6].X
			dst[7].X = dst[6].X
		}
	}
	return n
}

// chopCubicAtYExtrema chops a cubic into 1, 2 or 3 cubics monotonic in Y, for the scan converter:
// 0 means dst[0..4] is the original cubic, 1 adds dst[3..7], 2 adds dst[6..10].
func chopCubicAtYExtrema(src *[4]Point, dst *[10]Point) int {
	var tValues [3]float32
	n := findCubicExtrema(src[0].Y, src[1].Y, src[2].Y, src[3].Y, &tValues)

	chopCubicAt(src, tValues[:n], dst[:])
	if n > 0 {
		// make the Y extrema flat
		dst[2].Y = dst[3].Y
		dst[4].Y = dst[3].Y
		if n == 2 {
			dst[5].Y = dst[6].Y
			dst[7].Y = dst[6].Y
		}
	}
	return n
}

// chopCubicAt chops src at each of tValues (increasing, 0 < t < 1) into len(tValues)+1 cubics sharing
// end points in dst. If a renormalized t falls out of range (skia issue 32), the last cubic becomes a
// degenerate clump of 4 points.
func chopCubicAt(src *[4]Point, tValues []float32, dst []Point) {
	if len(tValues) == 0 {
		dst[0] = src[0]
		dst[1] = src[1]
		dst[2] = src[2]
		dst[3] = src[3]
		return
	}

	t := tValues[0]
	var tmp [4]Point
	dstOffset := 0
	for i := range tValues {
		chopCubicAt2(src, t, dst[dstOffset:])
		if i == len(tValues)-1 {
			break
		}

		dstOffset += 3
		// src is now the remaining cubic (after the chop)
		tmp[0] = dst[dstOffset+0]
		tmp[1] = dst[dstOffset+1]
		tmp[2] = dst[dstOffset+2]
		tmp[3] = dst[dstOffset+3]
		src = &tmp

		n, ok := validUnitDivide(tValues[i+1]-tValues[i], 1-tValues[i])
		if !ok {
			dst[dstOffset+4] = src[3]
			dst[dstOffset+5] = src[3]
			dst[dstOffset+6] = src[3]
			break
		}
		t = n
	}
}

// chopCubicAtMaxCurvature chops src at its points of maximum curvature inside 0 < t < 1, stores those t
// in tValues, and returns the number of cubics in dst.
func chopCubicAtMaxCurvature(src *[4]Point, tValues *[3]float32, dst []Point) int {
	var roots [3]float32
	n := findCubicMaxCurvature(src, &roots)

	// Throw out values not inside 0..1.
	count := 0
	for _, root := range roots[:n] {
		if 0 < root && root < 1 {
			tValues[count] = normalizedExclusiveBounded(root)
			count++
		}
	}

	if count == 0 {
		copy(dst[:4], src[:])
	} else {
		chopCubicAt(src, tValues[:count], dst)
	}
	return count + 1
}

func chopMonoCubicAtX(src *[4]Point, x float32, dst *[7]Point) bool {
	return cubicDChopAtIntercept(src, x, true, dst)
}

func chopMonoCubicAtY(src *[4]Point, y float32, dst *[7]Point) bool {
	return cubicDChopAtIntercept(src, y, false, dst)
}

func cubicDChopAtIntercept(src *[4]Point, intercept float32, isVertical bool, dst *[7]Point) bool {
	cubic := cubic64{points: [4]point64{
		point64FromPoint(src[0]),
		point64FromPoint(src[1]),
		point64FromPoint(src[2]),
		point64FromPoint(src[3]),
	}}
	var roots [3]float64
	var count int
	if isVertical {
		count = cubic64VerticalIntersect(&cubic, float64(intercept), &roots)
	} else {
		count = cubic64HorizontalIntersect(&cubic, float64(intercept), &roots)
	}

	if count > 0 {
		pair := cubic.chopAt(roots[0])
		for i := range 7 {
			dst[i] = pair[i].toPoint()
		}
		return true
	}
	return false
}
