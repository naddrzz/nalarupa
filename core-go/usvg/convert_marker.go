// Port of usvg 0.47 src/parser/marker.rs.

package usvg

import (
	"math"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

type markerSegmentKind uint8

const (
	markerMoveTo markerSegmentKind = iota
	markerLineTo
	markerCubicTo
	markerClose
)

// markerSegment is marker.rs's Segment: a path segment without QuadTo. p is the end point (MoveTo,
// LineTo, CubicTo); p1 and p2 are the cubic's control points.
type markerSegment struct {
	kind      markerSegmentKind
	p1, p2, p skia.Point
}

type markerKind uint8

const (
	markerKindStart markerKind = iota
	markerKindMiddle
	markerKindEnd
)

// markerIsValid is marker::is_valid: the node has a marker property and is not inside a `clipPath`.
func markerIsValid(node svgNode) bool {
	// `marker-*` cannot be set on shapes inside a `clipPath`.
	for n := range node.Ancestors() {
		if n.hasTagName(eidClipPath) {
			return false
		}
	}
	_, start := svgFindAttr(node, aidMarkerStart, svgNode.attrNode)
	_, mid := svgFindAttr(node, aidMarkerMid, svgNode.attrNode)
	_, end := svgFindAttr(node, aidMarkerEnd, svgNode.attrNode)
	return start || mid || end
}

// convertMarkers is marker::convert: the start, middle and end markers of path into parent.
func convertMarkers(node svgNode, path *skia.Path, state *converterState, cache *converterCache, parent *Group) {
	list := [...]struct {
		a    aid
		kind markerKind
	}{
		{aidMarkerStart, markerKindStart},
		{aidMarkerMid, markerKindMiddle},
		{aidMarkerEnd, markerKindEnd},
	}
	for _, item := range list {
		link, ok := svgFindAttr(node, item.a, svgNode.attrNode)
		if !ok || !link.hasTagName(eidMarker) {
			continue
		}
		// A recursive marker is skipped.
		recursive := false
		for _, m := range state.parentMarkers {
			if m == link {
				recursive = true
				break
			}
		}
		if recursive {
			continue
		}
		resolveMarker(node, path, link, item.kind, state, cache, parent)
	}
}

type markerOrientationKind uint8

const (
	markerOrientAuto markerOrientationKind = iota
	markerOrientAutoStartReverse
	markerOrientAngle
)

// resolveMarker is marker.rs's resolve.
func resolveMarker(shapeNode svgNode, path *skia.Path, markerNode svgNode, kind markerKind,
	state *converterState, cache *converterCache, parent *Group) {
	strokeScale, ok := markerStrokeScale(shapeNode, markerNode, state)
	if !ok {
		return
	}

	r, ok := markerConvertRect(markerNode, state)
	if !ok {
		return
	}

	var vb *viewBox
	if rect, ok := markerNode.parseViewbox(); ok {
		aspect, ok := markerNode.attrAspectRatio(aidPreserveAspectRatio)
		if !ok {
			aspect = stDefaultAspectRatio()
		}
		vb = &viewBox{rect: rect, aspect: aspect}
	}

	// `overflow` is `hidden` by default.
	overflow, hasOverflowAttr := markerNode.attrStr(aidOverflow)
	hasOverflow := !hasOverflowAttr || overflow == "hidden" || overflow == "scroll"

	var clipPath *ClipPath
	if hasOverflow {
		clipRect := r.Size().ToNonZeroRect(0, 0)
		if vb != nil {
			clipRect = vb.rect
		}
		clipPath = newEmptyClipPath(cache.genClipPathID())
		p, ok := newSimplePath(skia.PathFromRect(clipRect.ToRect()))
		if !ok {
			return
		}
		fill := DefaultFill()
		p.fill = &fill
		clipPath.root.children = append(clipPath.root.children, p)
	}

	segments := make([]markerSegment, 0, path.Len())
	var prev, prevMove skia.Point
	it := path.Segments()
	for {
		seg, ok := it.Next()
		if !ok {
			break
		}
		switch seg.Verb {
		case skia.VerbMove:
			segments = append(segments, markerSegment{kind: markerMoveTo, p: seg.Pts[0]})
			prev, prevMove = seg.Pts[0], seg.Pts[0]
		case skia.VerbLine:
			segments = append(segments, markerSegment{kind: markerLineTo, p: seg.Pts[0]})
			prev = seg.Pts[0]
		case skia.VerbQuad:
			p1, p2, p := quadToCurve(prev, seg.Pts[0], seg.Pts[1])
			segments = append(segments, markerSegment{kind: markerCubicTo, p1: p1, p2: p2, p: p})
			prev = p
		case skia.VerbCubic:
			segments = append(segments, markerSegment{kind: markerCubicTo, p1: seg.Pts[0], p2: seg.Pts[1], p: seg.Pts[2]})
			prev = seg.Pts[2]
		case skia.VerbClose:
			segments = append(segments, markerSegment{kind: markerClose})
			prev = prevMove
		}
	}

	drawMarker := func(p skia.Point, idx int) {
		ts := skia.TransformFromTranslate(p.X, p.Y)

		var angle float32
		switch orient, a := markerConvertOrientation(markerNode); {
		case orient == markerOrientAutoStartReverse && idx == 0:
			angle = fmodF32(calcVertexAngle(segments, idx)+180, 360)
		case orient == markerOrientAuto || orient == markerOrientAutoStartReverse:
			angle = calcVertexAngle(segments, idx)
		default:
			angle = a
		}

		if !approxZeroUlpsF32(angle, 4) {
			ts = ts.PreRotate(angle)
		}

		if vb != nil {
			size, ok := skia.SizeFromWH(float32(r.Width()*strokeScale), float32(r.Height()*strokeScale))
			if !ok {
				return // Rust unwraps here (it panics on an overflowing marker size)
			}
			sx, sy := vb.toTransform(size).GetScale()
			ts = ts.PreScale(sx, sy)
		} else {
			ts = ts.PreScale(strokeScale, strokeScale)
		}

		ts = ts.PreTranslate(-r.X(), -r.Y())

		// TODO (as in Rust): do not create a group when there is no clip path.
		g := newEmptyGroup()
		g.transform = ts
		g.absTransform = parent.absTransform.PreConcat(ts)
		g.clipPath = clipPath

		markerState := state.withMarker(markerNode)
		convertChildren(markerNode, &markerState, cache, &g)
		g.calculateBoundingBoxes()

		if g.HasChildren() {
			parent.children = append(parent.children, &g)
		}
	}

	drawMarkers(segments, kind, drawMarker)
}

// markerStrokeScale is stroke_scale.
func markerStrokeScale(pathNode, markerNode svgNode, state *converterState) (float32, bool) {
	if u, ok := markerNode.attrStr(aidMarkerUnits); ok && u == "userSpaceOnUse" {
		return 1, true
	}
	return pathNode.resolveValidLength(aidStrokeWidth, state, 1)
}

func drawMarkers(path []markerSegment, kind markerKind, drawMarker func(skia.Point, int)) {
	switch kind {
	case markerKindStart:
		if len(path) > 0 && path[0].kind == markerMoveTo {
			drawMarker(path[0].p, 0)
		}
	case markerKindMiddle:
		total := len(path) - 1
		for i := 1; i < total; i++ {
			if path[i].kind == markerClose {
				continue
			}
			drawMarker(path[i].p, i)
		}
	case markerKindEnd:
		if len(path) == 0 {
			return
		}
		idx := len(path) - 1
		switch last := path[idx]; last.kind {
		case markerLineTo, markerCubicTo:
			drawMarker(last.p, idx)
		case markerClose:
			drawMarker(getSubpathStart(path, idx), idx)
		}
	}
}

func calcVertexAngle(path []markerSegment, idx int) float32 {
	if idx == 0 {
		// The first segment.
		seg1, seg2 := path[0], path[1]
		if seg1.kind == markerMoveTo {
			pm := seg1.p
			switch seg2.kind {
			case markerLineTo:
				return calcLineAngle(pm.X, pm.Y, seg2.p.X, seg2.p.Y)
			case markerCubicTo:
				p1, p := seg2.p1, seg2.p
				if approxEqUlpsF32(pm.X, p1.X, 4) && approxEqUlpsF32(pm.Y, p1.Y, 4) {
					return calcLineAngle(pm.X, pm.Y, p.X, p.Y)
				}
				return calcLineAngle(pm.X, pm.Y, p1.X, p1.Y)
			}
		}
		return 0
	}

	if idx == len(path)-1 {
		// The last segment.
		seg1, seg2 := path[idx-1], path[idx]
		switch seg2.kind {
		case markerMoveTo:
			return 0 // unreachable
		case markerLineTo:
			prev := getPrevVertex(path, idx)
			return calcLineAngle(prev.X, prev.Y, seg2.p.X, seg2.p.Y)
		case markerCubicTo:
			p1, p2, p := seg2.p1, seg2.p2, seg2.p
			if approxEqUlpsF32(p2.X, p.X, 4) && approxEqUlpsF32(p2.Y, p.Y, 4) {
				return calcLineAngle(p1.X, p1.Y, p.X, p.Y)
			}
			return calcLineAngle(p2.X, p2.Y, p.X, p.Y)
		}
		// seg2 is Close.
		switch seg1.kind {
		case markerLineTo:
			p := seg1.p
			next := getSubpathStart(path, idx)
			return calcLineAngle(p.X, p.Y, next.X, next.Y)
		case markerCubicTo:
			p2, p := seg1.p2, seg1.p
			prev := getPrevVertex(path, idx)
			next := getSubpathStart(path, idx)
			return calcCurvesAngle(prev.X, prev.Y, p2.X, p2.Y, p.X, p.Y, next.X, next.Y, next.X, next.Y)
		}
		return 0
	}

	// The middle segments.
	seg1, seg2 := path[idx], path[idx+1]
	switch {
	case seg1.kind == markerMoveTo && seg2.kind == markerLineTo:
		pm, p := seg1.p, seg2.p
		return calcLineAngle(pm.X, pm.Y, p.X, p.Y)
	case seg1.kind == markerMoveTo && seg2.kind == markerCubicTo:
		pm, p1 := seg1.p, seg2.p1
		return calcLineAngle(pm.X, pm.Y, p1.X, p1.Y)
	case seg1.kind == markerLineTo && seg2.kind == markerLineTo:
		p1, p2 := seg1.p, seg2.p
		prev := getPrevVertex(path, idx)
		return calcAngle(prev.X, prev.Y, p1.X, p1.Y, p1.X, p1.Y, p2.X, p2.Y)
	case seg1.kind == markerCubicTo && seg2.kind == markerCubicTo:
		c1p2, c1p := seg1.p2, seg1.p
		c2p1, c2p := seg2.p1, seg2.p
		prev := getPrevVertex(path, idx)
		return calcCurvesAngle(prev.X, prev.Y, c1p2.X, c1p2.Y, c1p.X, c1p.Y, c2p1.X, c2p1.Y, c2p.X, c2p.Y)
	case seg1.kind == markerLineTo && seg2.kind == markerCubicTo:
		pl := seg1.p
		p1, p := seg2.p1, seg2.p
		prev := getPrevVertex(path, idx)
		return calcCurvesAngle(prev.X, prev.Y, prev.X, prev.Y, pl.X, pl.Y, p1.X, p1.Y, p.X, p.Y)
	case seg1.kind == markerCubicTo && seg2.kind == markerLineTo:
		p2, p := seg1.p2, seg1.p
		pl := seg2.p
		prev := getPrevVertex(path, idx)
		return calcCurvesAngle(prev.X, prev.Y, p2.X, p2.Y, p.X, p.Y, pl.X, pl.Y, pl.X, pl.Y)
	case seg1.kind == markerLineTo && seg2.kind == markerMoveTo:
		p := seg1.p
		prev := getPrevVertex(path, idx)
		return calcLineAngle(prev.X, prev.Y, p.X, p.Y)
	case seg1.kind == markerCubicTo && seg2.kind == markerMoveTo:
		p2, p := seg1.p2, seg1.p
		if approxEqUlpsF32(p.X, p2.X, 4) && approxEqUlpsF32(p.Y, p2.Y, 4) {
			prev := getPrevVertex(path, idx)
			return calcLineAngle(prev.X, prev.Y, p.X, p.Y)
		}
		return calcLineAngle(p2.X, p2.Y, p.X, p.Y)
	case seg1.kind == markerLineTo && seg2.kind == markerClose:
		p := seg1.p
		prev := getPrevVertex(path, idx)
		next := getSubpathStart(path, idx)
		return calcAngle(prev.X, prev.Y, p.X, p.Y, p.X, p.Y, next.X, next.Y)
	case seg2.kind == markerClose:
		prev := getPrevVertex(path, idx)
		next := getSubpathStart(path, idx)
		return calcLineAngle(prev.X, prev.Y, next.X, next.Y)
	}
	// (_, MoveTo) | (Close, _)
	return 0
}

func calcLineAngle(x1, y1, x2, y2 float32) float32 {
	return calcAngle(x1, y1, x2, y2, x1, y1, x2, y2)
}

// calcCurvesAngle: (px, py) is the previous vertex, (cx1, cy1) the previous control point, (x, y) the
// current vertex, (cx2, cy2) the next control point and (nx, ny) the next vertex.
func calcCurvesAngle(px, py, cx1, cy1, x, y, cx2, cy2, nx, ny float32) float32 {
	if approxEqUlpsF32(cx1, x, 4) && approxEqUlpsF32(cy1, y, 4) {
		return calcAngle(px, py, x, y, x, y, cx2, cy2)
	} else if approxEqUlpsF32(x, cx2, 4) && approxEqUlpsF32(y, cy2, 4) {
		return calcAngle(cx1, cy1, x, y, x, y, nx, ny)
	}
	return calcAngle(cx1, cy1, x, y, x, y, cx2, cy2)
}

// f32 constants of std::f32::consts, rounded once.
var (
	markerPI      = float32(math.Pi)
	markerFracPI2 = float32(math.Pi / 2)
	markerTwoPI   = float32(markerPI * 2)
	// markerPIsIn180 is f32::to_degrees' constant.
	markerPIsIn180 = float32(57.2957795130823208767981548141051703)
)

// fmodF32 is f32's `%` (fmodf), which is exact.
func fmodF32(a, b float32) float32 { return float32(math.Mod(float64(a), float64(b))) }

// atan2F32 is f32::atan2 (libm atan2f), computed as a correctly rounded f64 then rounded to f32.
func atan2F32(y, x float32) float32 { return float32(stAtan2(float64(y), float64(x))) }

func calcAngle(x1, y1, x2, y2, x3, y3, x4, y4 float32) float32 {
	normalize := func(rad float32) float32 {
		v := fmodF32(rad, markerTwoPI)
		if v < 0 {
			return v + markerTwoPI
		}
		return v
	}
	vectorAngle := func(vx, vy float32) float32 {
		rad := atan2F32(vy, vx)
		if rad != rad {
			return 0
		}
		return normalize(rad)
	}

	inA := vectorAngle(x2-x1, y2-y1)
	outA := vectorAngle(x4-x3, y4-y3)
	d := float32((outA - inA) * 0.5)

	angle := inA + d
	if markerFracPI2 < float32(math.Abs(float64(d))) {
		angle -= markerPI
	}
	return float32(normalize(angle) * markerPIsIn180)
}

// getSubpathStart is get_subpath_start: the last MoveTo before idx.
func getSubpathStart(segments []markerSegment, idx int) skia.Point {
	for i := idx - 1; i >= 0; i-- {
		if segments[i].kind == markerMoveTo {
			return segments[i].p
		}
	}
	return skia.Point{}
}

func getPrevVertex(segments []markerSegment, idx int) skia.Point {
	switch s := segments[idx-1]; s.kind {
	case markerClose:
		return getSubpathStart(segments, idx)
	default:
		return s.p
	}
}

// markerConvertRect is marker.rs's convert_rect: refX, refY, markerWidth and markerHeight.
func markerConvertRect(node svgNode, state *converterState) (skia.NonZeroRect, bool) {
	return skia.NonZeroRectFromXYWH(
		node.convertUserLength(aidRefX, state, stLengthZero()),
		node.convertUserLength(aidRefY, state, stLengthZero()),
		node.convertUserLength(aidMarkerWidth, state, stNewLengthNumber(3)),
		node.convertUserLength(aidMarkerHeight, state, stNewLengthNumber(3)),
	)
}

// markerConvertOrientation is convert_orientation; the angle is set for markerOrientAngle.
func markerConvertOrientation(node svgNode) (markerOrientationKind, float32) {
	if s, ok := node.attrStr(aidOrient); ok {
		switch s {
		case "auto":
			return markerOrientAuto, 0
		case "auto-start-reverse":
			return markerOrientAutoStartReverse, 0
		}
	}
	if a, ok := node.attrAngle(aidOrient); ok {
		return markerOrientAngle, float32(a.ToDegrees())
	}
	return markerOrientAngle, 0
}

func quadToCurve(prev, p1, p skia.Point) (skia.Point, skia.Point, skia.Point) {
	calc := func(n1, n2 float32) float32 { return (n1 + float32(n2*2)) / 3 }
	return skia.Pt(calc(prev.X, p1.X), calc(prev.Y, p1.Y)), skia.Pt(calc(p.X, p1.X), calc(p.Y, p1.Y)), p
}
