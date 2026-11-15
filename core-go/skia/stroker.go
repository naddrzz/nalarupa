package skia

// Stroking (stroker.rs, based on SkStroke.cpp).

// Stroke holds stroke properties.
type Stroke struct {
	// Width is the stroke thickness, >= 0. Zero means a hairline, which Path.Stroke does not produce.
	Width float32
	// MiterLimit is the limit at which a sharp corner is drawn beveled.
	MiterLimit float32
	LineCap    LineCap
	LineJoin   LineJoin
	// Dash is the dashing, or nil. Path.Stroke ignores it: dash the path first, as tiny-skia's painter does.
	Dash *StrokeDash
}

// DefaultStroke is Stroke::default(): width 1, miter limit 4, butt caps, miter joins, no dash.
func DefaultStroke() Stroke { return Stroke{Width: 1, MiterLimit: 4} }

// LineCap is drawn at the start and end of an open contour.
type LineCap uint8

const (
	LineCapButt   LineCap = iota // no extension
	LineCapRound                 // a half circle
	LineCapSquare                // a half square
)

// LineJoin is drawn at corners.
type LineJoin uint8

const (
	LineJoinMiter     LineJoin = iota // extends to the miter limit, then bevels
	LineJoinMiterClip                 // extends to the miter limit, then clips the corner
	LineJoinRound                     // a circle
	LineJoinBevel                     // connects the outside edges
)

const quadRecursiveLimit = 3

// recursiveLimits are 3x the recursion seen in practice (cubics 5 and 26, quads 11): quads with extreme
// widths (e.g. (0,1) (1,6) (0,3) width=5e7) recurse to the point of failure.
var recursiveLimits = [4]int32{5 * 3, 26 * 3, 11 * 3, 11 * 3}

type reductionType uint8

const (
	reductionPoint       reductionType = iota // all curve points are practically identical
	reductionLine                             // the control point is on the line between the ends
	reductionQuad                             // the control point is outside the line between the ends
	reductionDegenerate                       // the control point is on the line but outside the ends
	reductionDegenerate2                      // two control points are on the line but outside ends (cubic)
	reductionDegenerate3                      // three areas of max curvature found (for cubic)
)

type strokeType int8

const (
	strokeOuter strokeType = 1 // opposite signs flip the perpendicular axis
	strokeInner strokeType = -1
)

type resultType uint8

const (
	resultSplit      resultType = iota // the caller should split the quad stroke in two
	resultDegenerate                   // the caller should add a line
	resultQuad                         // the caller should (continue to try to) add a quad stroke
)

type intersectRayType uint8

const (
	intersectCtrlPt intersectRayType = iota
	intersectResultType
)

// Stroke returns the stroked path, or nil when the width is not positive and finite or the result is
// empty. resScale can come from ComputeResolutionScale. A PathStroker reuses its buffers across paths.
func (p *Path) Stroke(stroke *Stroke, resScale float32) *Path {
	return NewPathStroker().Stroke(p, stroke, resScale)
}

// PathStroker strokes paths.
type PathStroker struct {
	radius             float32
	invMiterLimit      float32
	resScale           float32
	invResScale        float32
	invResScaleSquared float32

	firstNormal     Point
	prevNormal      Point
	firstUnitNormal Point
	prevUnitNormal  Point

	// on the original path
	firstPt Point
	prevPt  Point

	firstOuterPt               Point
	firstOuterPtIndexInContour int
	segmentCount               int32
	prevIsLine                 bool

	capper LineCap
	joiner LineJoin

	// outer is the working answer, inner is temporary
	inner  PathBuilder
	outer  PathBuilder
	cusper PathBuilder

	strokeType strokeType

	recursionDepth int32 // abort if numerics run amok
	foundTangents  bool  // do less work until tangents meet (cubic)
	joinCompleted  bool  // the previous join was not degenerate
}

// NewPathStroker creates a stroker.
func NewPathStroker() *PathStroker {
	return &PathStroker{
		resScale: 1, invResScale: 1, invResScaleSquared: 1,
		segmentCount: -1,
		inner:        PathBuilder{moveToRequired: true},
		outer:        PathBuilder{moveToRequired: true},
		cusper:       PathBuilder{moveToRequired: true},
		strokeType:   strokeOuter,
	}
}

// ComputeResolutionScale is the intended output resolution for a transform: above 1 strokes more
// precisely (the result is zoomed in), below 1 less. It is 1 for a degenerate transform.
func ComputeResolutionScale(ts Transform) float32 {
	sx := Point{ts.SX, ts.KX}.Length()
	sy := Point{ts.KY, ts.SY}.Length()
	if isFinite(sx) && isFinite(sy) {
		if scale := maxf(sx, sy); scale > 0 {
			return scale
		}
	}
	return 1
}

// Stroke strokes a path, see Path.Stroke.
func (s *PathStroker) Stroke(path *Path, stroke *Stroke, resScale float32) *Path {
	if !isFinite(stroke.Width) || !(stroke.Width > 0) {
		return nil
	}
	return s.strokeInner(path, stroke.Width, stroke.MiterLimit, stroke.LineCap, stroke.LineJoin, resScale)
}

func (s *PathStroker) strokeInner(path *Path, width, miterLimit float32, lineCap LineCap, lineJoin LineJoin, resScale float32) *Path {
	var invMiterLimit float32
	if lineJoin == LineJoinMiter {
		if miterLimit <= 1 {
			lineJoin = LineJoinBevel
		} else {
			invMiterLimit = invert(miterLimit)
		}
	}
	if lineJoin == LineJoinMiterClip {
		invMiterLimit = invert(miterLimit)
	}

	s.resScale = resScale
	// The 4 matches the fill scan converter's error term.
	s.invResScale = invert(float32(resScale * 4))
	s.invResScaleSquared = sqr(s.invResScale)

	s.radius = half(width)
	s.invMiterLimit = invMiterLimit

	s.firstNormal, s.prevNormal, s.firstUnitNormal, s.prevUnitNormal = Point{}, Point{}, Point{}, Point{}
	s.firstPt, s.prevPt = Point{}, Point{}
	s.firstOuterPt = Point{}
	s.firstOuterPtIndexInContour = 0
	s.segmentCount = -1
	s.prevIsLine = false

	s.capper = lineCap
	s.joiner = lineJoin

	s.inner.Clear()
	s.outer.Clear()
	s.cusper.Clear()

	s.strokeType = strokeOuter
	s.recursionDepth = 0
	s.foundTangents = false
	s.joinCompleted = false

	lastSegmentIsLine := false
	iter := path.Segments()
	iter.SetAutoClose(true)
	for {
		seg, ok := iter.Next()
		if !ok {
			break
		}
		switch seg.Verb {
		case VerbMove:
			s.moveTo(seg.Pts[0])
		case VerbLine:
			s.lineTo(seg.Pts[0], &iter)
			lastSegmentIsLine = true
		case VerbQuad:
			s.quadTo(seg.Pts[0], seg.Pts[1])
			lastSegmentIsLine = false
		case VerbCubic:
			s.cubicTo(seg.Pts[0], seg.Pts[1], seg.Pts[2])
			lastSegmentIsLine = false
		case VerbClose:
			if lineCap != LineCapButt {
				// A move followed by a close counts as a zero-length line, which square and round caps draw.
				if s.hasOnlyMoveTo() {
					s.lineTo(s.moveToPt(), nil)
					lastSegmentIsLine = true
					continue
				}
				// So does a move followed by only zero-length verbs and a close.
				if s.isCurrentContourEmpty() {
					lastSegmentIsLine = true
					continue
				}
			}
			s.close(lastSegmentIsLine)
		}
	}
	return s.finish(lastSegmentIsLine)
}

func (s *PathStroker) moveToPt() Point { return s.firstPt }

func (s *PathStroker) moveTo(p Point) {
	if s.segmentCount > 0 {
		s.finishContour(false, false)
	}
	s.segmentCount = 0
	s.firstPt = p
	s.prevPt = p
	s.joinCompleted = false
}

func (s *PathStroker) lineTo(p Point, iter *PathSegmentsIter) {
	teenyLine := s.prevPt.equalsWithinTolerance(p, float32(ScalarNearlyZero*s.invResScale))
	if s.capper == LineCapButt && teenyLine {
		return
	}
	if teenyLine && (s.joinCompleted || (iter != nil && iter.hasValidTangent())) {
		return
	}
	var normal, unitNormal Point
	if !s.preJoinTo(p, true, &normal, &unitNormal) {
		return
	}
	s.outer.LineTo(p.X+normal.X, p.Y+normal.Y)
	s.inner.LineTo(p.X-normal.X, p.Y-normal.Y)
	s.postJoinTo(p, normal, unitNormal)
}

func (s *PathStroker) quadTo(p1, p2 Point) {
	quad := [3]Point{s.prevPt, p1, p2}
	reduction, rt := checkQuadLinear(&quad)
	switch rt {
	case reductionPoint, reductionLine:
		// A degenerate curve counts as a (possibly zero-length) line, which square and round caps draw.
		s.lineTo(p2, nil)
		return
	case reductionDegenerate:
		s.lineTo(reduction, nil)
		saveJoiner := s.joiner
		s.joiner = LineJoinRound
		s.lineTo(p2, nil)
		s.joiner = saveJoiner
		return
	}

	var normalAB, unitAB, normalBC, unitBC Point
	if !s.preJoinTo(p1, false, &normalAB, &unitAB) {
		s.lineTo(p2, nil)
		return
	}
	var qp quadConstruct
	s.initQuad(strokeOuter, 0, 1, &qp)
	s.quadStroke(&quad, &qp)
	s.initQuad(strokeInner, 0, 1, &qp)
	s.quadStroke(&quad, &qp)

	if !setNormalUnitNormal(quad[1], quad[2], s.resScale, s.radius, &normalBC, &unitBC) {
		normalBC = normalAB
		unitBC = unitAB
	}
	s.postJoinTo(p2, normalBC, unitBC)
}

func (s *PathStroker) cubicTo(pt1, pt2, pt3 Point) {
	cubic := [4]Point{s.prevPt, pt1, pt2, pt3}
	var reduction [3]Point
	var tangentPt Point
	rt := checkCubicLinear(&cubic, &reduction, &tangentPt)
	if rt == reductionPoint || rt == reductionLine {
		// A degenerate curve counts as a (possibly zero-length) line, which square and round caps draw.
		s.lineTo(pt3, nil)
		return
	}
	if reductionDegenerate <= rt && rt <= reductionDegenerate3 {
		s.lineTo(reduction[0], nil)
		saveJoiner := s.joiner
		s.joiner = LineJoinRound
		if reductionDegenerate2 <= rt {
			s.lineTo(reduction[1], nil)
		}
		if rt == reductionDegenerate3 {
			s.lineTo(reduction[2], nil)
		}
		s.lineTo(pt3, nil)
		s.joiner = saveJoiner
		return
	}

	var normalAB, unitAB, normalCD, unitCD Point
	if !s.preJoinTo(tangentPt, false, &normalAB, &unitAB) {
		s.lineTo(pt3, nil)
		return
	}
	var tValues [3]float32
	n := findCubicInflections(&cubic, &tValues)
	var lastT float32
	for i := 0; i <= n; i++ {
		nextT := float32(1)
		if i < n {
			nextT = tValues[i]
		}
		var qp quadConstruct
		s.initQuad(strokeOuter, lastT, nextT, &qp)
		s.cubicStroke(&cubic, &qp)
		s.initQuad(strokeInner, lastT, nextT, &qp)
		s.cubicStroke(&cubic, &qp)
		lastT = nextT
	}
	if cusp, ok := findCubicCusp(&cubic); ok {
		loc := evalCubicPosAt(&cubic, cusp)
		s.cusper.PushCircle(loc.X, loc.Y, s.radius)
	}
	// Emit the join even if one stroke succeeded and the last failed, so an inner stroke with a partial
	// path is not reversed and followed by another move.
	s.setCubicEndNormal(&cubic, normalAB, unitAB, &normalCD, &unitCD)
	s.postJoinTo(pt3, normalCD, unitCD)
}

func (s *PathStroker) cubicStroke(cubic *[4]Point, qp *quadConstruct) bool {
	if !s.foundTangents {
		rt := s.tangentsMeet(cubic, qp)
		if rt != resultQuad {
			ok := pointsWithinDist(qp.quad[0], qp.quad[2], s.invResScale)
			if (rt == resultDegenerate || ok) && s.cubicMidOnLine(cubic, qp) {
				s.addDegenerateLine(qp)
				return true
			}
		} else {
			s.foundTangents = true
		}
	}
	if s.foundTangents {
		rt := s.compareQuadCubic(cubic, qp)
		if rt == resultQuad {
			st := &qp.quad
			if s.strokeType == strokeOuter {
				s.outer.QuadTo(st[1].X, st[1].Y, st[2].X, st[2].Y)
			} else {
				s.inner.QuadTo(st[1].X, st[1].Y, st[2].X, st[2].Y)
			}
			return true
		}
		if rt == resultDegenerate && !qp.oppositeTangents {
			s.addDegenerateLine(qp)
			return true
		}
	}
	if !isFinite(qp.quad[2].X) || !isFinite(qp.quad[2].X) { // sic: Rust tests x twice
		return false // just abort if the projected quad is not representable
	}
	s.recursionDepth++
	limit := recursiveLimits[0]
	if s.foundTangents {
		limit = recursiveLimits[1]
	}
	if s.recursionDepth > limit {
		return false
	}
	var half quadConstruct
	if !half.initWithStart(qp) {
		s.addDegenerateLine(qp)
		s.recursionDepth--
		return true
	}
	if !s.cubicStroke(cubic, &half) {
		return false
	}
	if !half.initWithEnd(qp) {
		s.addDegenerateLine(qp)
		s.recursionDepth--
		return true
	}
	if !s.cubicStroke(cubic, &half) {
		return false
	}
	s.recursionDepth--
	return true
}

func (s *PathStroker) cubicMidOnLine(cubic *[4]Point, qp *quadConstruct) bool {
	var strokeMid Point
	s.cubicQuadMid(cubic, qp, &strokeMid)
	return ptToLine(strokeMid, qp.quad[0], qp.quad[2]) < s.invResScaleSquared
}

func (s *PathStroker) cubicQuadMid(cubic *[4]Point, qp *quadConstruct, mid *Point) {
	var cubicMidPt Point
	s.cubicPerpRay(cubic, qp.midT, &cubicMidPt, mid, nil)
}

// cubicPerpRay returns the point on the cubic at t, its perpendicular and the perpendicular's tangent.
func (s *PathStroker) cubicPerpRay(cubic *[4]Point, t float32, tPt, onPt, tangent *Point) {
	*tPt = evalCubicPosAt(cubic, t)
	dxy := evalCubicTangentAt(cubic, t)
	var chopped [7]Point
	if dxy.X == 0 && dxy.Y == 0 {
		cPoints := cubic[:]
		switch {
		case isNearlyZero(t):
			dxy = cubic[2].Sub(cubic[0])
		case isNearlyZero(1 - t):
			dxy = cubic[3].Sub(cubic[1])
		default:
			// The inflection falls on the cusp: subdivide to find the tangent there. t is not 0 or 1 here.
			chopCubicAt2(cubic, t, chopped[:])
			dxy = chopped[3].Sub(chopped[2])
			if dxy.X == 0 && dxy.Y == 0 {
				dxy = chopped[3].Sub(chopped[1])
				cPoints = chopped[:]
			}
		}
		if dxy.X == 0 && dxy.Y == 0 {
			dxy = cPoints[3].Sub(cPoints[0])
		}
	}
	s.setRayPoints(*tPt, &dxy, onPt, tangent)
}

func (s *PathStroker) setCubicEndNormal(cubic *[4]Point, normalAB, unitNormalAB Point, normalCD, unitNormalCD *Point) {
	ab := cubic[1].Sub(cubic[0])
	cd := cubic[3].Sub(cubic[2])
	degenerateAB := degenerateVector(ab)
	degenerateCB := degenerateVector(cd)
	if degenerateAB && degenerateCB {
		*normalCD, *unitNormalCD = normalAB, unitNormalAB
		return
	}
	if degenerateAB {
		ab = cubic[2].Sub(cubic[0])
		degenerateAB = degenerateVector(ab)
	}
	if degenerateCB {
		cd = cubic[3].Sub(cubic[1])
		degenerateCB = degenerateVector(cd)
	}
	if degenerateAB || degenerateCB {
		*normalCD, *unitNormalCD = normalAB, unitNormalAB
		return
	}
	setNormalUnitNormal2(cd, s.radius, normalCD, unitNormalCD)
}

func (s *PathStroker) compareQuadCubic(cubic *[4]Point, qp *quadConstruct) resultType {
	// The quadratic approximation of the stroke.
	s.cubicQuadEnds(cubic, qp)
	if rt := s.intersectRay(intersectCtrlPt, qp); rt != resultQuad {
		return rt
	}
	// A ray from the curve to the stroke, near the midpoints of the quad and the cubic.
	var ray0, ray1 Point
	s.cubicPerpRay(cubic, qp.midT, &ray1, &ray0, nil)
	stroke := qp.quad
	return s.strokeCloseEnough(&stroke, &[2]Point{ray0, ray1}, qp)
}

// cubicQuadEnds finds the start and end of the cubic's t range if they are not found yet.
func (s *PathStroker) cubicQuadEnds(cubic *[4]Point, qp *quadConstruct) {
	if !qp.startSet {
		var cubicStartPt Point
		s.cubicPerpRay(cubic, qp.startT, &cubicStartPt, &qp.quad[0], &qp.tangentStart)
		qp.startSet = true
	}
	if !qp.endSet {
		var cubicEndPt Point
		s.cubicPerpRay(cubic, qp.endT, &cubicEndPt, &qp.quad[2], &qp.tangentEnd)
		qp.endSet = true
	}
}

func (s *PathStroker) close(isLine bool) { s.finishContour(true, isLine) }

func (s *PathStroker) finishContour(close, currIsLine bool) {
	if s.segmentCount > 0 {
		if close {
			join(s.joiner, s.prevUnitNormal, s.prevPt, s.firstUnitNormal, s.radius, s.invMiterLimit, s.prevIsLine, currIsLine, &s.inner, &s.outer)
			s.outer.Close()
			// The inner path as its own contour.
			pt, _ := s.inner.LastPoint()
			s.outer.MoveTo(pt.X, pt.Y)
			s.outer.reversePathTo(&s.inner)
			s.outer.Close()
		} else {
			// Cap the end.
			pt, _ := s.inner.LastPoint()
			capLine(s.capper, s.prevPt, s.prevNormal, pt, currIsLine, &s.outer)
			s.outer.reversePathTo(&s.inner)
			// Cap the start.
			capLine(s.capper, s.firstPt, s.firstNormal.Neg(), s.firstOuterPt, s.prevIsLine, &s.outer)
			s.outer.Close()
		}
		if !s.cusper.IsEmpty() {
			s.outer.pushPathBuilder(&s.cusper)
			s.cusper.Clear()
		}
	}
	// inner is reused: rewind it rather than reset it.
	s.inner.Clear()
	s.segmentCount = -1
	s.firstOuterPtIndexInContour = len(s.outer.points)
}

func (s *PathStroker) preJoinTo(p Point, currIsLine bool, normal, unitNormal *Point) bool {
	prevX, prevY := s.prevPt.X, s.prevPt.Y
	if !setNormalUnitNormal(s.prevPt, p, s.resScale, s.radius, normal, unitNormal) {
		if s.capper == LineCapButt {
			return false
		}
		// Square and round caps draw even a zero-length segment, which has no direction: default to upright.
		*normal = Point{s.radius, 0}
		*unitNormal = Point{1, 0}
	}
	if s.segmentCount == 0 {
		s.firstNormal = *normal
		s.firstUnitNormal = *unitNormal
		s.firstOuterPt = Point{prevX + normal.X, prevY + normal.Y}
		s.outer.MoveTo(s.firstOuterPt.X, s.firstOuterPt.Y)
		s.inner.MoveTo(prevX-normal.X, prevY-normal.Y)
	} else { // there is a previous segment
		join(s.joiner, s.prevUnitNormal, s.prevPt, *unitNormal, s.radius, s.invMiterLimit, s.prevIsLine, currIsLine, &s.inner, &s.outer)
	}
	s.prevIsLine = currIsLine
	return true
}

func (s *PathStroker) postJoinTo(p, normal, unitNormal Point) {
	s.joinCompleted = true
	s.prevPt = p
	s.prevUnitNormal = unitNormal
	s.prevNormal = normal
	s.segmentCount++
}

func (s *PathStroker) initQuad(st strokeType, start, end float32, qp *quadConstruct) {
	s.strokeType = st
	s.foundTangents = false
	qp.init(start, end)
}

func (s *PathStroker) quadStroke(quad *[3]Point, qp *quadConstruct) bool {
	rt := s.compareQuadQuad(quad, qp)
	if rt == resultQuad {
		path := &s.outer
		if s.strokeType != strokeOuter {
			path = &s.inner
		}
		path.QuadTo(qp.quad[1].X, qp.quad[1].Y, qp.quad[2].X, qp.quad[2].Y)
		return true
	}
	if rt == resultDegenerate {
		s.addDegenerateLine(qp)
		return true
	}
	s.recursionDepth++
	if s.recursionDepth > recursiveLimits[quadRecursiveLimit] {
		return false
	}
	var half quadConstruct
	half.initWithStart(qp)
	if !s.quadStroke(quad, &half) {
		return false
	}
	half.initWithEnd(qp)
	if !s.quadStroke(quad, &half) {
		return false
	}
	s.recursionDepth--
	return true
}

func (s *PathStroker) compareQuadQuad(quad *[3]Point, qp *quadConstruct) resultType {
	// The quadratic approximation of the stroke.
	if !qp.startSet {
		var quadStartPt Point
		s.quadPerpRay(quad, qp.startT, &quadStartPt, &qp.quad[0], &qp.tangentStart)
		qp.startSet = true
	}
	if !qp.endSet {
		var quadEndPt Point
		s.quadPerpRay(quad, qp.endT, &quadEndPt, &qp.quad[2], &qp.tangentEnd)
		qp.endSet = true
	}
	if rt := s.intersectRay(intersectCtrlPt, qp); rt != resultQuad {
		return rt
	}
	// A ray from the curve to the stroke.
	var ray0, ray1 Point
	s.quadPerpRay(quad, qp.midT, &ray1, &ray0, nil)
	stroke := qp.quad
	return s.strokeCloseEnough(&stroke, &[2]Point{ray0, ray1}, qp)
}

// setRayPoints scales the derivative at a curve point by the radius and computes the perpendicular
// point and its tangent.
func (s *PathStroker) setRayPoints(tp Point, dxy, onP, tangent *Point) {
	if !dxy.SetLength(s.radius) {
		*dxy = Point{s.radius, 0}
	}
	axisFlip := float32(s.strokeType) // opposite ways for outer and inner
	onP.X = tp.X + float32(axisFlip*dxy.Y)
	onP.Y = tp.Y - float32(axisFlip*dxy.X)
	if tangent != nil {
		tangent.X = onP.X + dxy.X
		tangent.Y = onP.Y + dxy.Y
	}
}

// quadPerpRay returns the point on the quad at t, its perpendicular and the perpendicular's tangent.
func (s *PathStroker) quadPerpRay(quad *[3]Point, t float32, tp, onP, tangent *Point) {
	*tp = evalQuadAt(quad, t)
	dxy := evalQuadTangentAt(quad, t)
	if dxy.IsZero() {
		dxy = quad[2].Sub(quad[0])
	}
	s.setRayPoints(*tp, &dxy, onP, tangent)
}

func (s *PathStroker) addDegenerateLine(qp *quadConstruct) {
	if s.strokeType == strokeOuter {
		s.outer.LineTo(qp.quad[2].X, qp.quad[2].Y)
	} else {
		s.inner.LineTo(qp.quad[2].X, qp.quad[2].Y)
	}
}

func (s *PathStroker) strokeCloseEnough(stroke *[3]Point, ray *[2]Point, qp *quadConstruct) resultType {
	strokeMid := evalQuadAt(stroke, normalizedClamped(0.5))
	// The distance from the curve to the quad-stroke midpoint, compared to the radius.
	if pointsWithinDist(ray[0], strokeMid, s.invResScale) {
		// The difference is small.
		if sharpAngle(&qp.quad) {
			return resultSplit
		}
		return resultQuad
	}
	// Quick reject on the quad's bounds; far points subdivide.
	if !ptInQuadBounds(stroke, ray[0], s.invResScale) {
		return resultSplit
	}
	// The distance from the curve ray to the quad-stroke.
	var roots [3]float32
	if intersectQuadRay(ray, stroke, &roots) != 1 {
		return resultSplit
	}
	quadPt := evalQuadAt(stroke, roots[0])
	errorLimit := float32(s.invResScale * (1 - float32(abs32(roots[0]-0.5)*2)))
	if pointsWithinDist(ray[0], quadPt, errorLimit) {
		// The difference is small: done.
		if sharpAngle(&qp.quad) {
			return resultSplit
		}
		return resultQuad
	}
	return resultSplit
}

// intersectRay intersects the stroke tangents to construct a stroke quad: a degenerate (a line), a quad,
// or a split. For intersectCtrlPt it also computes the quad's control point.
func (s *PathStroker) intersectRay(irt intersectRayType, qp *quadConstruct) resultType {
	start, end := qp.quad[0], qp.quad[2]
	aLen := qp.tangentStart.Sub(start)
	bLen := qp.tangentEnd.Sub(end)
	// The slopes match when denom is zero: byLen * axLen - ayLen * bxLen.
	denom := aLen.Cross(bLen)
	if denom == 0 || !isFinite(denom) {
		qp.oppositeTangents = aLen.Dot(bLen) < 0
		return resultDegenerate
	}
	qp.oppositeTangents = false
	ab0 := start.Sub(end)
	numerA := bLen.Cross(ab0)
	numerB := aLen.Cross(ab0)
	if (numerA >= 0) == (numerB >= 0) {
		// The control point is outside the quad ends. If the perpendicular distances from the quad points
		// to the opposite tangent line are small, a straight line is good enough.
		dist1 := ptToLine(start, end, qp.tangentEnd)
		dist2 := ptToLine(end, start, qp.tangentStart)
		if maxf(dist1, dist2) <= s.invResScaleSquared {
			return resultDegenerate
		}
		return resultSplit
	}
	// The denominator is teeny relative to the numerator when an offset of one is lost.
	numerA /= denom
	if numerA > numerA-1 {
		if irt == intersectCtrlPt {
			// The tangents' intersection need not be on the tangent segment: 0 <= numerA <= 1 may not hold.
			qp.quad[1].X = float32(start.X*(1-numerA)) + float32(qp.tangentStart.X*numerA)
			qp.quad[1].Y = float32(start.Y*(1-numerA)) + float32(qp.tangentStart.Y*numerA)
		}
		return resultQuad
	}
	qp.oppositeTangents = aLen.Dot(bLen) < 0
	// Parallel lines: a straight line is good enough.
	return resultDegenerate
}

// tangentsMeet tells whether the cubic's stroke over a t range can be a quad.
func (s *PathStroker) tangentsMeet(cubic *[4]Point, qp *quadConstruct) resultType {
	s.cubicQuadEnds(cubic, qp)
	return s.intersectRay(intersectResultType, qp)
}

func (s *PathStroker) finish(isLine bool) *Path {
	s.finishContour(false, isLine)
	buf := s.outer
	s.outer = PathBuilder{moveToRequired: true}
	return buf.Finish()
}

func (s *PathStroker) hasOnlyMoveTo() bool { return s.segmentCount == 0 }

func (s *PathStroker) isCurrentContourEmpty() bool {
	return s.inner.isZeroLengthSincePoint(0) && s.outer.isZeroLengthSincePoint(s.firstOuterPtIndexInContour)
}

// capLine draws a cap; otherPath is whether the other side (the inner path) ends in a line.
func capLine(c LineCap, pivot, normal, stop Point, otherPath bool, path *PathBuilder) {
	switch c {
	case LineCapButt:
		path.LineTo(stop.X, stop.Y)
	case LineCapRound:
		parallel := normal
		parallel.rotateCW()
		projectedCenter := pivot.Add(parallel)
		path.conicPointsTo(projectedCenter.Add(normal), projectedCenter, ScalarRoot2Over2)
		path.conicPointsTo(projectedCenter.Sub(normal), stop, ScalarRoot2Over2)
	case LineCapSquare:
		parallel := normal
		parallel.rotateCW()
		if otherPath {
			path.setLastPoint(Point{pivot.X + normal.X + parallel.X, pivot.Y + normal.Y + parallel.Y})
			path.LineTo(pivot.X-normal.X+parallel.X, pivot.Y-normal.Y+parallel.Y)
		} else {
			path.LineTo(pivot.X+normal.X+parallel.X, pivot.Y+normal.Y+parallel.Y)
			path.LineTo(pivot.X-normal.X+parallel.X, pivot.Y-normal.Y+parallel.Y)
			path.LineTo(stop.X, stop.Y)
		}
	}
}

// join draws a join. Skia swaps which builder is inner and which outer during a join, so the joiners
// take both and may swap them locally.
func join(j LineJoin, beforeUnitNormal, pivot, afterUnitNormal Point, radius, invMiterLimit float32, prevIsLine, currIsLine bool, inner, outer *PathBuilder) {
	switch j {
	case LineJoinMiter:
		miterJoiner(beforeUnitNormal, pivot, afterUnitNormal, radius, invMiterLimit, false, prevIsLine, currIsLine, inner, outer)
	case LineJoinMiterClip:
		miterJoiner(beforeUnitNormal, pivot, afterUnitNormal, radius, invMiterLimit, true, prevIsLine, currIsLine, inner, outer)
	case LineJoinRound:
		roundJoiner(beforeUnitNormal, pivot, afterUnitNormal, radius, inner, outer)
	case LineJoinBevel:
		bevelJoiner(beforeUnitNormal, pivot, afterUnitNormal, radius, inner, outer)
	}
}

func isClockwise(before, after Point) bool {
	return float32(before.X*after.Y) > float32(before.Y*after.X)
}

type angleType uint8

const (
	angleNearly180 angleType = iota
	angleSharp
	angleShallow
	angleNearlyLine
)

func dotToAngleType(dot float32) angleType {
	if dot >= 0 { // shallow or line
		if isNearlyZero(1 - dot) {
			return angleNearlyLine
		}
		return angleShallow
	}
	// sharp or 180
	if isNearlyZero(1 + dot) {
		return angleNearly180
	}
	return angleSharp
}

func handleInnerJoin(pivot, after Point, inner *PathBuilder) {
	// When the radius is larger than the segments, connecting the two inner segments directly may show
	// through as a diagonal: go through the pivot instead, at the cost of an extra point.
	inner.LineTo(pivot.X, pivot.Y)
	inner.LineTo(pivot.X-after.X, pivot.Y-after.Y)
}

func bevelJoiner(beforeUnitNormal, pivot, afterUnitNormal Point, radius float32, inner, outer *PathBuilder) {
	after := afterUnitNormal.Scaled(radius)
	if !isClockwise(beforeUnitNormal, afterUnitNormal) {
		inner, outer = outer, inner
		after = after.Neg()
	}
	outer.LineTo(pivot.X+after.X, pivot.Y+after.Y)
	handleInnerJoin(pivot, after, inner)
}

func roundJoiner(beforeUnitNormal, pivot, afterUnitNormal Point, radius float32, inner, outer *PathBuilder) {
	dotProd := beforeUnitNormal.Dot(afterUnitNormal)
	if dotToAngleType(dotProd) == angleNearlyLine {
		return
	}
	before, after := beforeUnitNormal, afterUnitNormal
	dir := dirCW
	if !isClockwise(before, after) {
		inner, outer = outer, inner
		before = before.Neg()
		after = after.Neg()
		dir = dirCCW
	}
	ts := TransformFromRow(radius, 0, 0, radius, pivot.X, pivot.Y)
	var conics [5]conic
	n := buildUnitArc(before, after, dir, ts, &conics)
	if n == 0 {
		return
	}
	for _, c := range conics[:n] {
		outer.conicPointsTo(c.points[1], c.points[2], c.weight)
	}
	after.Scale(radius)
	handleInnerJoin(pivot, after, inner)
}

func miterJoiner(beforeUnitNormal, pivot, afterUnitNormal Point, radius, invMiterLimit float32, miterClip, prevIsLine, currIsLine bool, inner, outer *PathBuilder) {
	doBluntOrClipped := func(before, mid, after Point) {
		after.Scale(radius)
		if miterClip {
			mid.Normalize()
			cosBeta := before.Dot(mid)
			sinBeta := before.Cross(mid)
			var x float32
			if abs32(sinBeta) <= ScalarNearlyZero {
				x = 1 / invMiterLimit
			} else {
				x = ((1 / invMiterLimit) - cosBeta) / sinBeta
			}
			before.Scale(radius)
			beforeTangent := before
			beforeTangent.rotateCW()
			afterTangent := after
			afterTangent.rotateCCW()
			c1 := pivot.Add(before).Add(beforeTangent.Scaled(x))
			c2 := pivot.Add(after).Add(afterTangent.Scaled(x))
			if prevIsLine {
				outer.setLastPoint(c1)
			} else {
				outer.LineTo(c1.X, c1.Y)
			}
			outer.LineTo(c2.X, c2.Y)
		}
		if !currIsLine {
			outer.LineTo(pivot.X+after.X, pivot.Y+after.Y)
		}
		handleInnerJoin(pivot, after, inner)
	}
	doMiter := func(mid, after Point) {
		after.Scale(radius)
		if prevIsLine {
			outer.setLastPoint(Point{pivot.X + mid.X, pivot.Y + mid.Y})
		} else {
			outer.LineTo(pivot.X+mid.X, pivot.Y+mid.Y)
		}
		if !currIsLine {
			outer.LineTo(pivot.X+after.X, pivot.Y+after.Y)
		}
		handleInnerJoin(pivot, after, inner)
	}

	// Negate the dot since these are normals, not tangents.
	dotProd := beforeUnitNormal.Dot(afterUnitNormal)
	at := dotToAngleType(dotProd)
	before, after := beforeUnitNormal, afterUnitNormal
	var mid Point

	if at == angleNearlyLine {
		return
	}
	if at == angleNearly180 {
		currIsLine = false
		mid = after.Sub(before).Scaled(radius / 2)
		doBluntOrClipped(before, mid, after)
		return
	}
	ccw := !isClockwise(before, after)
	if ccw {
		inner, outer = outer, inner
		before = before.Neg()
		after = after.Neg()
	}
	// An upright right angle (common when stroking rectangles) is special cased for speed and accuracy;
	// with dot == 0 one normal is enough to check.
	if dotProd == 0 && invMiterLimit <= ScalarRoot2Over2 {
		mid = before.Add(after).Scaled(radius)
		doMiter(mid, after)
		return
	}
	// The most accurate way to form the initial mid vector.
	if at == angleSharp {
		mid = Point{after.Y - before.Y, before.X - after.X}
		if ccw {
			mid = mid.Neg()
		}
	} else {
		mid = Point{before.X + after.X, before.Y + after.Y}
	}
	// midLength = radius / sinHalfAngle; abort when midLength > miterLimit * radius, that is when
	// 1 / miterLimit > sinHalf. The dot is built from normals, hence 1 + dot instead of 1 - dot.
	sinHalfAngle := sqrt32(half(1 + dotProd))
	if sinHalfAngle < invMiterLimit {
		currIsLine = false
		doBluntOrClipped(before, mid, after)
		return
	}
	mid.SetLength(radius / sinHalfAngle)
	doMiter(mid, after)
}

func setNormalUnitNormal(before, after Point, scale, radius float32, normal, unitNormal *Point) bool {
	if !unitNormal.SetNormalize(float32((after.X-before.X)*scale), float32((after.Y-before.Y)*scale)) {
		return false
	}
	unitNormal.rotateCCW()
	*normal = unitNormal.Scaled(radius)
	return true
}

func setNormalUnitNormal2(vec Point, radius float32, normal, unitNormal *Point) bool {
	if !unitNormal.SetNormalize(vec.X, vec.Y) {
		return false
	}
	unitNormal.rotateCCW()
	*normal = unitNormal.Scaled(radius)
	return true
}

// quadConstruct is the state of a quad stroke under construction.
type quadConstruct struct {
	quad         [3]Point // the stroked quad parallel to the original curve
	tangentStart Point    // a point tangent to quad[0]
	tangentEnd   Point    // a point tangent to quad[2]
	startT       float32  // a segment of the original curve
	midT         float32
	endT         float32
	startSet     bool // share common points across structs
	endSet       bool
	// oppositeTangents is set if coincident tangents have opposite directions.
	oppositeTangents bool
}

// init returns false if start and end are too close to have a unique middle.
func (q *quadConstruct) init(start, end float32) bool {
	q.startT = start
	q.midT = normalizedClamped(half(start + end))
	q.endT = end
	q.startSet = false
	q.endSet = false
	return q.startT < q.midT && q.midT < q.endT
}

func (q *quadConstruct) initWithStart(parent *quadConstruct) bool {
	if !q.init(parent.startT, parent.midT) {
		return false
	}
	q.quad[0] = parent.quad[0]
	q.tangentStart = parent.tangentStart
	q.startSet = true
	return true
}

func (q *quadConstruct) initWithEnd(parent *quadConstruct) bool {
	if !q.init(parent.midT, parent.endT) {
		return false
	}
	q.quad[2] = parent.quad[2]
	q.tangentEnd = parent.tangentEnd
	q.endSet = true
	return true
}

func checkQuadLinear(quad *[3]Point) (Point, reductionType) {
	degenerateAB := degenerateVector(quad[1].Sub(quad[0]))
	degenerateBC := degenerateVector(quad[2].Sub(quad[1]))
	if degenerateAB && degenerateBC {
		return Point{}, reductionPoint
	}
	if degenerateAB || degenerateBC {
		return Point{}, reductionLine
	}
	if !quadInLine(quad) {
		return Point{}, reductionQuad
	}
	t := findQuadMaxCurvature(quad)
	if t == 0 || t == 1 {
		return Point{}, reductionLine
	}
	return evalQuadAt(quad, t), reductionDegenerate
}

func degenerateVector(v Point) bool { return !v.canNormalize() }

// quadInLine reports whether the inner point is close to the line through the outermost points, found
// as the largest difference in x or y. The indices xor to 3, so the missing one is outer1 ^ outer2 ^ 3.
func quadInLine(quad *[3]Point) bool {
	ptMax := float32(-1)
	outer1, outer2 := 0, 0
	for index := range 2 {
		for inner := index + 1; inner < 3; inner++ {
			d := quad[inner].Sub(quad[index])
			testMax := maxf(abs32(d.X), abs32(d.Y))
			if ptMax < testMax {
				outer1, outer2 = index, inner
				ptMax = testMax
			}
		}
	}
	mid := outer1 ^ outer2 ^ 3
	const curvatureSlop = 0.000005 // pulled out of the air
	lineSlop := float32(ptMax * ptMax * curvatureSlop)
	return ptToLine(quad[mid], quad[outer1], quad[outer2]) <= lineSlop
}

// ptToLine returns the squared distance from the point to the line segment.
func ptToLine(pt, lineStart, lineEnd Point) float32 {
	dxy := lineEnd.Sub(lineStart)
	ab0 := pt.Sub(lineStart)
	numer := dxy.Dot(ab0)
	denom := dxy.Dot(dxy)
	t := numer / denom
	if t >= 0 && t <= 1 {
		hit := Point{float32(lineStart.X*(1-t)) + float32(lineEnd.X*t), float32(lineStart.Y*(1-t)) + float32(lineEnd.Y*t)}
		return hit.distanceToSqd(pt)
	}
	return pt.distanceToSqd(lineStart)
}

// intersectQuadRay intersects the line with the quad and returns how many roots (t on the quad) it set.
func intersectQuadRay(line *[2]Point, quad *[3]Point, roots *[3]float32) int {
	vec := line[1].Sub(line[0])
	var r [3]float32
	for n := range 3 {
		r[n] = float32((quad[n].Y-line[0].Y)*vec.X) - float32((quad[n].X-line[0].X)*vec.Y)
	}
	a, b, c := r[2], r[1], r[0]
	a += c - float32(2*b) // A = a - 2*b + c
	b -= c                // B = -(b - c)
	return findUnitQuadRoots(a, float32(2*b), c, roots)
}

func pointsWithinDist(nearPt, farPt Point, limit float32) bool {
	return nearPt.distanceToSqd(farPt) <= float32(limit*limit)
}

func sharpAngle(quad *[3]Point) bool {
	smaller := quad[1].Sub(quad[0])
	larger := quad[1].Sub(quad[2])
	smallerLen := smaller.lengthSqd()
	largerLen := larger.lengthSqd()
	if smallerLen > largerLen {
		smaller, larger = larger, smaller
		largerLen = smallerLen
	}
	if !smaller.SetLength(largerLen) {
		return false
	}
	return smaller.Dot(larger) > 0
}

// ptInQuadBounds reports whether the point is close to the quad's bounds, as a quick reject.
func ptInQuadBounds(quad *[3]Point, pt Point, invResScale float32) bool {
	xMin := minf(minf(quad[0].X, quad[1].X), quad[2].X)
	if pt.X+invResScale < xMin {
		return false
	}
	xMax := maxf(maxf(quad[0].X, quad[1].X), quad[2].X)
	if pt.X-invResScale > xMax {
		return false
	}
	yMin := minf(minf(quad[0].Y, quad[1].Y), quad[2].Y)
	if pt.Y+invResScale < yMin {
		return false
	}
	yMax := maxf(maxf(quad[0].Y, quad[1].Y), quad[2].Y)
	return !(pt.Y-invResScale > yMax)
}

func checkCubicLinear(cubic *[4]Point, reduction *[3]Point, tangentPt *Point) reductionType {
	degenerateAB := degenerateVector(cubic[1].Sub(cubic[0]))
	degenerateBC := degenerateVector(cubic[2].Sub(cubic[1]))
	degenerateCD := degenerateVector(cubic[3].Sub(cubic[2]))
	if degenerateAB && degenerateBC && degenerateCD {
		return reductionPoint
	}
	if b2i(degenerateAB)+b2i(degenerateBC)+b2i(degenerateCD) == 2 {
		return reductionLine
	}
	if !cubicInLine(cubic) {
		if tangentPt != nil {
			if degenerateAB {
				*tangentPt = cubic[2]
			} else {
				*tangentPt = cubic[1]
			}
		}
		return reductionQuad
	}
	var tValues [3]float32
	n := findCubicMaxCurvature(cubic, &tValues)
	rCount := 0
	// Reject t values that evaluate to either end point.
	for _, t := range tValues[:n] {
		if 0 >= t || t >= 1 {
			continue
		}
		reduction[rCount] = evalCubicPosAt(cubic, t)
		if reduction[rCount] != cubic[0] && reduction[rCount] != cubic[3] {
			rCount++
		}
	}
	return [4]reductionType{reductionLine, reductionDegenerate, reductionDegenerate2, reductionDegenerate3}[rCount]
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cubicInLine reports whether the inner points are close to the line through the outermost points,
// found as the largest difference in x or y. With outer1 < outer2, the smaller remaining index is
// (1 + (2 >> outer2)) >> outer1, and the last one outer1 ^ outer2 ^ mid1.
func cubicInLine(cubic *[4]Point) bool {
	ptMax := float32(-1)
	outer1, outer2 := 0, 0
	for index := range 3 {
		for inner := index + 1; inner < 4; inner++ {
			d := cubic[inner].Sub(cubic[index])
			testMax := maxf(abs32(d.X), abs32(d.Y))
			if ptMax < testMax {
				outer1, outer2 = index, inner
				ptMax = testMax
			}
		}
	}
	mid1 := (1 + (2 >> outer2)) >> outer1
	mid2 := outer1 ^ outer2 ^ mid1
	lineSlop := float32(ptMax * ptMax * 0.00001) // pulled out of the air
	return ptToLine(cubic[mid1], cubic[outer1], cubic[outer2]) <= lineSlop &&
		ptToLine(cubic[mid2], cubic[outer1], cubic[outer2]) <= lineSlop
}
