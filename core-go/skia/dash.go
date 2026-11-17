package skia

// Dashing (dash.rs): a mix of SkDashPath, SkDashPathEffect, SkContourMeasure and SkPathMeasure.

// StrokeDash holds a dash array of "on" and "off" interval pairs and an offset. The array has an even
// number (at least two) of finite values >= 0 with a positive, finite sum, and the offset is finite.
type StrokeDash struct {
	array       []float32
	offset      float32
	intervalLen float32
	firstLen    float32
	firstIndex  int
}

// NewStrokeDash creates a dash, or nil for an invalid array or offset.
func NewStrokeDash(array []float32, offset float32) *StrokeDash {
	if !isFinite(offset) {
		return nil
	}
	if len(array) < 2 || len(array)%2 != 0 {
		return nil
	}
	for _, n := range array {
		if n < 0 {
			return nil
		}
	}
	var intervalLen float32
	for _, n := range array {
		intervalLen += n
	}
	if !isFinite(intervalLen) || !(intervalLen > 0) {
		return nil
	}
	offset = adjustDashOffset(offset, intervalLen)
	firstLen, firstIndex := findFirstInterval(array, offset)
	return &StrokeDash{array: append([]float32(nil), array...), offset: offset, intervalLen: intervalLen, firstLen: firstLen, firstIndex: firstIndex}
}

// adjustDashOffset brings the offset into 0..len, flipping a negative one: with len 100, -20 (or -120)
// becomes 80.
func adjustDashOffset(offset, length float32) float32 {
	switch {
	case offset < 0:
		offset = -offset
		if offset > length {
			offset = mod32(offset, length)
		}
		offset = length - offset
		// With finite precision the subtraction can still give len (if len >>> offset).
		if offset == length {
			offset = 0
		}
		return offset
	case offset >= length:
		return mod32(offset, length)
	}
	return offset
}

func findFirstInterval(array []float32, offset float32) (float32, int) {
	for i, gap := range array {
		if offset > gap || (offset == gap && gap != 0) {
			offset -= gap
		} else {
			return gap - offset, i
		}
	}
	// The offset "appears" to be longer than the intervals, from rounding in their sum: eat the error.
	return array[0], 0
}

// Dash returns the dashed path, or nil when more than 1,000,000 dashes would be made or the result has
// invalid bounds. resScale can come from ComputeResolutionScale.
func (p *Path) Dash(dash *StrokeDash, resScale float32) *Path {
	// Like tiny-skia, without Skia's cull_path and SpecialLineRec cases.
	pb := NewPathBuilder()
	var dashCount float32
	it := newContourMeasureIter(p, resScale)
	for {
		contour, ok := it.next()
		if !ok {
			break
		}
		skipFirstSegment := contour.isClosed
		addedSegment := false
		length := contour.length
		index := dash.firstIndex

		// The path length / dash length ratio may be huge: give up beyond a million dashes (crbug.com/165432)
		// rather than exhaust memory.
		const maxDashCount = 1000000
		dashCount += float32(length*float32(len(dash.array)>>1)) / dash.intervalLen
		if dashCount > maxDashCount {
			return nil
		}

		var distance float32
		dLen := dash.firstLen
		for distance < length {
			addedSegment = false
			if index%2 == 0 && !skipFirstSegment {
				addedSegment = true
				contour.pushSegment(distance, distance+dLen, true, pb)
			}
			distance += dLen
			// Respected only the first time around.
			skipFirstSegment = false
			index++
			if index == len(dash.array) {
				index = 0
			}
			dLen = dash.array[index]
		}
		// Ended on a segment: join up with the skipped initial segment.
		if contour.isClosed && dash.firstIndex%2 == 0 && dash.firstLen >= 0 {
			contour.pushSegment(0, dash.firstLen, !addedSegment, pb)
		}
	}
	return pb.Finish()
}

const maxTValue = 0x3FFFFFFF

type contourMeasureIter struct {
	iter      PathSegmentsIter
	tolerance float32
}

func newContourMeasureIter(p *Path, resScale float32) *contourMeasureIter {
	// Midpoints, not tangents, so [0..1..................2] is not taken for a line.
	const cheapDistLimit = 0.5 // just made this value up
	return &contourMeasureIter{iter: p.Segments(), tolerance: float32(cheapDistLimit * invert(resScale))}
}

// next measures the next contour; false ends the iteration (no contours left, or a non-finite length).
func (it *contourMeasureIter) next() (*contourMeasure, bool) {
	// A tiny delta may be > 0 and still not change the accumulated distance, so every += is checked to
	// have made it larger (here and in computeQuadSegs and computeCubicSegs).
	contour := &contourMeasure{}
	pointIndex := 0
	var distance float32
	haveSeenClose := false
	var prevP Point
	for {
		seg, ok := it.iter.Next()
		if !ok {
			break
		}
		switch seg.Verb {
		case VerbMove:
			contour.points = append(contour.points, seg.Pts[0])
			prevP = seg.Pts[0]
		case VerbLine:
			prevD := distance
			distance = contour.computeLineSeg(prevP, seg.Pts[0], distance, pointIndex)
			if distance > prevD {
				contour.points = append(contour.points, seg.Pts[0])
				pointIndex++
			}
			prevP = seg.Pts[0]
		case VerbQuad:
			prevD := distance
			distance = contour.computeQuadSegs(prevP, seg.Pts[0], seg.Pts[1], distance, 0, maxTValue, pointIndex, it.tolerance)
			if distance > prevD {
				contour.points = append(contour.points, seg.Pts[0], seg.Pts[1])
				pointIndex += 2
			}
			prevP = seg.Pts[1]
		case VerbCubic:
			prevD := distance
			distance = contour.computeCubicSegs(prevP, seg.Pts[0], seg.Pts[1], seg.Pts[2], distance, 0, maxTValue, pointIndex, it.tolerance)
			if distance > prevD {
				contour.points = append(contour.points, seg.Pts[0], seg.Pts[1], seg.Pts[2])
				pointIndex += 3
			}
			prevP = seg.Pts[2]
		case VerbClose:
			haveSeenClose = true
		}
		if v, ok := it.iter.NextVerb(); ok && v == VerbMove {
			break
		}
	}
	if !isFinite(distance) {
		return nil, false
	}
	if haveSeenClose {
		prevD := distance
		firstPt := contour.points[0]
		distance = contour.computeLineSeg(contour.points[pointIndex], firstPt, distance, pointIndex)
		if distance > prevD {
			contour.points = append(contour.points, firstPt)
		}
	}
	contour.length = distance
	contour.isClosed = haveSeenClose
	if len(contour.points) == 0 {
		return nil, false
	}
	return contour, true
}

type segmentType uint8

const (
	segLine segmentType = iota
	segQuad
	segCubic
)

type segment struct {
	distance   float32 // total distance up to this point
	pointIndex int     // index into contourMeasure.points
	tValue     uint32
	kind       segmentType
}

func (s segment) scalarT() float32 {
	// 1/maxTValue is not representable as a float, but it is close and the limits work fine.
	maxTReciprocal := 1.0 / float32(maxTValue)
	return float32(float32(s.tValue) * maxTReciprocal)
}

type contourMeasure struct {
	segments []segment
	points   []Point
	length   float32
	isClosed bool
}

func (c *contourMeasure) pushSegment(startD, stopD float32, startWithMoveTo bool, pb *PathBuilder) {
	if startD < 0 {
		startD = 0
	}
	if stopD > c.length {
		stopD = c.length
	}
	if !(startD <= stopD) { // NaN too
		return
	}
	if len(c.segments) == 0 {
		return
	}
	segIndex, startT, ok := c.distanceToSegment(startD)
	if !ok {
		return
	}
	seg := c.segments[segIndex]
	stopSegIndex, stopT, ok := c.distanceToSegment(stopD)
	if !ok {
		return
	}
	stopSeg := c.segments[stopSegIndex]
	if startWithMoveTo {
		p := computePos(c.points[seg.pointIndex:], seg.kind, startT)
		pb.MoveTo(p.X, p.Y)
	}
	if seg.pointIndex == stopSeg.pointIndex {
		segmentTo(c.points[seg.pointIndex:], seg.kind, startT, stopT, pb)
		return
	}
	newSegIndex := segIndex
	for {
		segmentTo(c.points[seg.pointIndex:], seg.kind, startT, 1, pb)
		oldPointIndex := seg.pointIndex
		for {
			newSegIndex++
			if c.segments[newSegIndex].pointIndex != oldPointIndex {
				break
			}
		}
		seg = c.segments[newSegIndex]
		startT = 0
		if seg.pointIndex >= stopSeg.pointIndex {
			break
		}
	}
	segmentTo(c.points[seg.pointIndex:], seg.kind, 0, stopT, pb)
}

func (c *contourMeasure) distanceToSegment(distance float32) (int, float32, bool) {
	index := findSegment(c.segments, distance)
	// Exact match or not does not matter: xor a negative index.
	index ^= index >> 31
	seg := c.segments[index]
	// Interpolate t with the previous segment, if it is on the same points.
	var startT, startD float32
	if index > 0 {
		startD = c.segments[index-1].distance
		if c.segments[index-1].pointIndex == seg.pointIndex {
			startT = c.segments[index-1].scalarT()
		}
	}
	t := startT + float32((seg.scalarT()-startT)*(distance-startD))/(seg.distance-startD)
	t, ok := newNormalized(t)
	return int(index), t, ok
}

func (c *contourMeasure) computeLineSeg(p0, p1 Point, distance float32, pointIndex int) float32 {
	d := p0.Distance(p1)
	prevD := distance
	distance += d
	if distance > prevD {
		c.segments = append(c.segments, segment{distance, pointIndex, maxTValue, segLine})
	}
	return distance
}

func (c *contourMeasure) computeQuadSegs(p0, p1, p2 Point, distance float32, minT, maxT uint32, pointIndex int, tolerance float32) float32 {
	if tSpanBigEnough(maxT-minT) != 0 && quadTooCurvy(p0, p1, p2, tolerance) {
		var tmp [5]Point
		halfT := (minT + maxT) >> 1
		chopQuadAt([]Point{p0, p1, p2}, 0.5, &tmp)
		distance = c.computeQuadSegs(tmp[0], tmp[1], tmp[2], distance, minT, halfT, pointIndex, tolerance)
		return c.computeQuadSegs(tmp[2], tmp[3], tmp[4], distance, halfT, maxT, pointIndex, tolerance)
	}
	d := p0.Distance(p2)
	prevD := distance
	distance += d
	if distance > prevD {
		c.segments = append(c.segments, segment{distance, pointIndex, maxT, segQuad})
	}
	return distance
}

func (c *contourMeasure) computeCubicSegs(p0, p1, p2, p3 Point, distance float32, minT, maxT uint32, pointIndex int, tolerance float32) float32 {
	if tSpanBigEnough(maxT-minT) != 0 && cubicTooCurvy(p0, p1, p2, p3, tolerance) {
		var tmp [7]Point
		halfT := (minT + maxT) >> 1
		chopCubicAt2(&[4]Point{p0, p1, p2, p3}, 0.5, tmp[:])
		distance = c.computeCubicSegs(tmp[0], tmp[1], tmp[2], tmp[3], distance, minT, halfT, pointIndex, tolerance)
		return c.computeCubicSegs(tmp[3], tmp[4], tmp[5], tmp[6], distance, halfT, maxT, pointIndex, tolerance)
	}
	d := p0.Distance(p3)
	prevD := distance
	distance += d
	if distance > prevD {
		c.segments = append(c.segments, segment{distance, pointIndex, maxT, segCubic})
	}
	return distance
}

// findSegment binary searches the segments for key: the index of a match, else the bitwise not of the
// insertion index.
func findSegment(base []segment, key float32) int32 {
	lo, hi := uint32(0), uint32(len(base)-1)
	for lo < hi {
		mid := (hi + lo) >> 1
		if base[mid].distance < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if base[hi].distance < key {
		hi++
		hi = ^hi
	} else if key < base[hi].distance {
		hi = ^hi
	}
	return int32(hi)
}

func computePos(points []Point, kind segmentType, t float32) Point {
	switch kind {
	case segLine:
		return Point{interp(points[0].X, points[1].X, t), interp(points[0].Y, points[1].Y, t)}
	case segQuad:
		return evalQuadAt((*[3]Point)(points[:3]), t)
	default:
		return evalCubicPosAt((*[4]Point)(points[:4]), t)
	}
}

func segmentTo(points []Point, kind segmentType, startT, stopT float32, pb *PathBuilder) {
	if startT == stopT {
		// A zero-length "on" interval becomes a zero-length line, which the stroker caps.
		if pt, ok := pb.LastPoint(); ok {
			pb.LineTo(pt.X, pt.Y)
		}
		return
	}
	switch kind {
	case segLine:
		if stopT == 1 {
			pb.LineTo(points[1].X, points[1].Y)
		} else {
			pb.LineTo(interp(points[0].X, points[1].X, stopT), interp(points[0].Y, points[1].Y, stopT))
		}
	case segQuad:
		var tmp0, tmp1 [5]Point
		if startT == 0 {
			if stopT == 1 {
				pb.quadToPt(points[1], points[2])
			} else {
				chopQuadAt(points, normalizedExclusiveBounded(stopT), &tmp0)
				pb.quadToPt(tmp0[1], tmp0[2])
			}
		} else {
			chopQuadAt(points, normalizedExclusiveBounded(startT), &tmp0)
			if stopT == 1 {
				pb.quadToPt(tmp0[3], tmp0[4])
			} else {
				newT := normalizedExclusiveBounded((stopT - startT) / (1 - startT))
				chopQuadAt(tmp0[2:], newT, &tmp1)
				pb.quadToPt(tmp1[1], tmp1[2])
			}
		}
	case segCubic:
		var tmp0, tmp1 [7]Point
		src := (*[4]Point)(points[:4])
		if startT == 0 {
			if stopT == 1 {
				pb.cubicToPt(points[1], points[2], points[3])
			} else {
				chopCubicAt2(src, normalizedExclusiveBounded(stopT), tmp0[:])
				pb.cubicToPt(tmp0[1], tmp0[2], tmp0[3])
			}
		} else {
			chopCubicAt2(src, normalizedExclusiveBounded(startT), tmp0[:])
			if stopT == 1 {
				pb.cubicToPt(tmp0[4], tmp0[5], tmp0[6])
			} else {
				newT := normalizedExclusiveBounded((stopT - startT) / (1 - startT))
				chopCubicAt2((*[4]Point)(tmp0[3:7]), newT, tmp1[:])
				pb.cubicToPt(tmp1[1], tmp1[2], tmp1[3])
			}
		}
	}
}

func tSpanBigEnough(tSpan uint32) uint32 { return tSpan >> 10 }

func quadTooCurvy(p0, p1, p2 Point, tolerance float32) bool {
	// diff = (a/4 + b/2 + c/4) - (a/2 + c/2) = -a/4 + b/2 - c/4
	dx := half(p1.X) - half(half(p0.X+p2.X))
	dy := half(p1.Y) - half(half(p0.Y+p2.Y))
	return maxf(abs32(dx), abs32(dy)) > tolerance
}

func cubicTooCurvy(p0, p1, p2, p3 Point, tolerance float32) bool {
	n0 := cheapDistExceedsLimit(p1, interpSafe(p0.X, p3.X, 1.0/3.0), interpSafe(p0.Y, p3.Y, 1.0/3.0), tolerance)
	n1 := cheapDistExceedsLimit(p2, interpSafe(p0.X, p3.X, 2.0/3.0), interpSafe(p0.Y, p3.Y, 2.0/3.0), tolerance)
	return n0 || n1
}

func cheapDistExceedsLimit(pt Point, x, y, tolerance float32) bool {
	return maxf(abs32(x-pt.X), abs32(y-pt.Y)) > tolerance
}

// interp linearly interpolates from a (t = 0) to b (t = 1).
func interp(a, b, t float32) float32 { return a + float32((b-a)*t) }

func interpSafe(a, b, t float32) float32 { return a + float32((b-a)*t) }
