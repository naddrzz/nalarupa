package skia

// PathVerb is a path verb (path.rs).
type PathVerb uint8

const (
	VerbMove PathVerb = iota
	VerbLine
	VerbQuad
	VerbCubic
	VerbClose
)

// Path is an immutable Bezier path made by a PathBuilder (path.rs). It has precomputed bounds, finite
// points and at least two segments; every contour starts with a move, with no repeated moves or closes.
// Zero-length contours are allowed.
type Path struct {
	verbs  []PathVerb
	points []Point
	bounds Rect
}

// Len is the number of segments.
func (p *Path) Len() int { return len(p.verbs) }

// PointsLen is the number of points.
func (p *Path) PointsLen() int { return len(p.points) }

// IsEmpty reports whether the path has no segments.
func (p *Path) IsEmpty() bool { return len(p.verbs) == 0 }

// Bounds is the bounds of the path's points.
func (p *Path) Bounds() Rect { return p.bounds }

// Verbs returns the verbs; do not modify them.
func (p *Path) Verbs() []PathVerb { return p.verbs }

// Points returns the points; do not modify them.
func (p *Path) Points() []Point { return p.points }

// ComputeTightBounds computes the bounds of the curves themselves, which can be expensive.
func (p *Path) ComputeTightBounds() (Rect, bool) {
	// Big enough for the worst case (cubic) extremas + 1.
	var extremas [5]Point
	mn, mx := p.points[0], p.points[0]
	// The segments as Segments gives them (no auto-close), walked inline: text conversion bounds every path.
	pts := p.points
	pi := 0
	var lastPoint, lastMoveTo Point
	for _, verb := range p.verbs {
		count := 0
		switch verb {
		case VerbMove:
			extremas[0] = pts[pi]
			count = 1
			lastMoveTo = pts[pi]
			lastPoint = lastMoveTo
			pi++
		case VerbLine:
			extremas[0] = pts[pi]
			count = 1
			lastPoint = pts[pi]
			pi++
		case VerbQuad:
			count = computeQuadExtremas(lastPoint, pts[pi], pts[pi+1], &extremas)
			lastPoint = pts[pi+1]
			pi += 2
		case VerbCubic:
			count = computeCubicExtremas(lastPoint, pts[pi], pts[pi+1], pts[pi+2], &extremas)
			lastPoint = pts[pi+2]
			pi += 3
		default:
			lastPoint = lastMoveTo
		}
		for _, e := range extremas[:count] {
			mn.X = minf(mn.X, e.X)
			mn.Y = minf(mn.Y, e.Y)
			mx.X = maxf(mx.X, e.X)
			mx.Y = maxf(mx.Y, e.Y)
		}
	}
	return RectFromLTRB(mn.X, mn.Y, mx.X, mx.Y)
}

// Transform returns the path mapped by ts, or nil when a point becomes non-finite. The result owns its
// storage; the receiver is not changed.
func (p *Path) Transform(ts Transform) *Path {
	verbs := append([]PathVerb(nil), p.verbs...)
	points := append([]Point(nil), p.points...)
	if ts.IsIdentity() {
		return &Path{verbs: verbs, points: points, bounds: p.bounds}
	}
	ts.MapPoints(points)
	bounds, ok := RectFromPoints(points)
	if !ok {
		return nil
	}
	return &Path{verbs: verbs, points: points, bounds: bounds}
}

// Clear returns an empty builder that reuses the path's storage; the path must not be used afterwards.
func (p *Path) Clear() *PathBuilder {
	b := &PathBuilder{verbs: p.verbs[:0], points: p.points[:0], moveToRequired: true}
	p.verbs, p.points = nil, nil
	return b
}

func computeQuadExtremas(p0, p1, p2 Point, extremas *[5]Point) int {
	src := [3]Point{p0, p1, p2}
	i := 0
	if t, ok := findQuadExtrema(p0.X, p1.X, p2.X); ok {
		extremas[i] = evalQuadAt(&src, t)
		i++
	}
	if t, ok := findQuadExtrema(p0.Y, p1.Y, p2.Y); ok {
		extremas[i] = evalQuadAt(&src, t)
		i++
	}
	extremas[i] = p2
	return i + 1
}

func computeCubicExtremas(p0, p1, p2, p3 Point, extremas *[5]Point) int {
	var ts0, ts1 [3]float32
	n0 := findCubicExtrema(p0.X, p1.X, p2.X, p3.X, &ts0)
	n1 := findCubicExtrema(p0.Y, p1.Y, p2.Y, p3.Y, &ts1)
	total := n0 + n1
	src := [4]Point{p0, p1, p2, p3}
	i := 0
	for _, t := range ts0[:n0] {
		extremas[i] = evalCubicPosAt(&src, t)
		i++
	}
	for _, t := range ts1[:n1] {
		extremas[i] = evalCubicPosAt(&src, t)
		i++
	}
	extremas[total] = p3
	return total + 1
}

// PathSegment is a path segment: Pts holds 1 point for a move or line, 2 for a quad, 3 for a cubic and
// none for a close.
type PathSegment struct {
	Verb PathVerb
	Pts  [3]Point
}

// PathSegmentsIter iterates over a path's segments. Copying it copies its position.
type PathSegmentsIter struct {
	path        *Path
	verbIndex   int
	pointsIndex int

	isAutoClose bool
	lastMoveTo  Point
	lastPoint   Point
}

// Segments returns an iterator over the segments.
func (p *Path) Segments() PathSegmentsIter { return PathSegmentsIter{path: p} }

// SetAutoClose turns auto closing on: a close then first yields a line back to the contour's start, if
// the contour does not end there, and only then the close. Off by default.
func (it *PathSegmentsIter) SetAutoClose(flag bool) { it.isAutoClose = flag }

func (it *PathSegmentsIter) autoClose() PathSegment {
	if it.isAutoClose && it.lastPoint != it.lastMoveTo {
		it.verbIndex--
		return PathSegment{Verb: VerbLine, Pts: [3]Point{it.lastMoveTo}}
	}
	return PathSegment{Verb: VerbClose}
}

func (it PathSegmentsIter) hasValidTangent() bool {
	for {
		seg, ok := it.Next()
		if !ok {
			return false
		}
		switch seg.Verb {
		case VerbMove:
			return false
		case VerbLine:
			if it.lastPoint == seg.Pts[0] {
				continue
			}
			return true
		case VerbQuad:
			if it.lastPoint == seg.Pts[0] && it.lastPoint == seg.Pts[1] {
				continue
			}
			return true
		case VerbCubic:
			if it.lastPoint == seg.Pts[0] && it.lastPoint == seg.Pts[1] && it.lastPoint == seg.Pts[2] {
				continue
			}
			return true
		case VerbClose:
			return false
		}
	}
}

// CurrVerb returns the verb of the last segment returned.
func (it *PathSegmentsIter) CurrVerb() PathVerb { return it.path.verbs[it.verbIndex-1] }

// NextVerb returns the next verb, if any.
func (it *PathSegmentsIter) NextVerb() (PathVerb, bool) {
	if it.verbIndex < len(it.path.verbs) {
		return it.path.verbs[it.verbIndex], true
	}
	return 0, false
}

// Next returns the next segment.
func (it *PathSegmentsIter) Next() (PathSegment, bool) {
	if it.verbIndex >= len(it.path.verbs) {
		return PathSegment{}, false
	}
	verb := it.path.verbs[it.verbIndex]
	it.verbIndex++
	pts := it.path.points
	switch verb {
	case VerbMove:
		it.pointsIndex++
		it.lastMoveTo = pts[it.pointsIndex-1]
		it.lastPoint = it.lastMoveTo
		return PathSegment{Verb: VerbMove, Pts: [3]Point{it.lastMoveTo}}, true
	case VerbLine:
		it.pointsIndex++
		it.lastPoint = pts[it.pointsIndex-1]
		return PathSegment{Verb: VerbLine, Pts: [3]Point{it.lastPoint}}, true
	case VerbQuad:
		it.pointsIndex += 2
		it.lastPoint = pts[it.pointsIndex-1]
		return PathSegment{Verb: VerbQuad, Pts: [3]Point{pts[it.pointsIndex-2], it.lastPoint}}, true
	case VerbCubic:
		it.pointsIndex += 3
		it.lastPoint = pts[it.pointsIndex-1]
		return PathSegment{Verb: VerbCubic, Pts: [3]Point{pts[it.pointsIndex-3], pts[it.pointsIndex-2], it.lastPoint}}, true
	default:
		seg := it.autoClose()
		it.lastPoint = it.lastMoveTo
		return seg, true
	}
}
