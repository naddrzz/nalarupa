package skia

// Edge list building (edge_builder.rs).

type edgeCombine uint8

const (
	edgeCombineNo edgeCombine = iota
	edgeCombinePartial
	edgeCombineTotal
)

type shiftedIntRect struct {
	shifted screenIntRect
	shift   int32
}

func newShiftedIntRect(rect screenIntRect, shift int32) (shiftedIntRect, bool) {
	shifted, ok := screenIntRectFromXYWH(rect.X()<<shift, rect.Y()<<shift, rect.Width()<<shift, rect.Height()<<shift)
	if !ok {
		return shiftedIntRect{}, false
	}
	return shiftedIntRect{shifted: shifted, shift: shift}, true
}

func (r *shiftedIntRect) recover() screenIntRect {
	v, ok := screenIntRectFromXYWH(r.shifted.X()>>r.shift, r.shifted.Y()>>r.shift,
		r.shifted.Width()>>r.shift, r.shifted.Height()>>r.shift)
	if !ok {
		panic("skia: shifted rect cannot be recovered") // cannot fail, the original rect was valid
	}
	return v
}

type basicEdgeBuilder struct {
	edges     []edge
	clipShift int32
}

// buildEdges returns nil for infinite or NaN segments or fewer than 2 edges. Skia returns a linked list;
// tiny-skia mimics it with a slice.
func buildEdges(path *Path, clip *shiftedIntRect, clipShift int32) []edge {
	// If we're convex, then we need both edges, even if the right edge is past the clip.
	canCullToTheRight := false // TODO in tiny-skia: !path.isConvex()

	builder := basicEdgeBuilder{edges: make([]edge, 0, 64), clipShift: clipShift}
	if !builder.build(path, clip, canCullToTheRight) {
		return nil // infinite or NaN segments
	}
	if len(builder.edges) < 2 {
		return nil
	}
	return builder.edges
}

func (b *basicEdgeBuilder) build(path *Path, clip *shiftedIntRect, canCullToTheRight bool) bool {
	if clip != nil {
		it := newEdgeClipperIter(path, clip.recover().ToRect(), canCullToTheRight)
		for {
			edges, ok := it.next()
			if !ok {
				break
			}
			for i := range edges {
				e := &edges[i]
				switch e.kind {
				case pathEdgeLine:
					if !e.p[0].IsFinite() || !e.p[1].IsFinite() {
						return false
					}
					b.pushLine(e.p[0], e.p[1])
				case pathEdgeQuad:
					if !e.p[0].IsFinite() || !e.p[1].IsFinite() || !e.p[2].IsFinite() {
						return false
					}
					b.pushQuad(e.p[:3])
				case pathEdgeCubic:
					if !e.p[0].IsFinite() || !e.p[1].IsFinite() || !e.p[2].IsFinite() || !e.p[3].IsFinite() {
						return false
					}
					b.pushCubic(e.p[:4])
				}
			}
		}
		return true
	}

	it := newPathEdgeIter(path)
	for {
		e, ok := it.next()
		if !ok {
			break
		}
		switch e.kind {
		case pathEdgeLine:
			b.pushLine(e.p[0], e.p[1])
		case pathEdgeQuad:
			points := [3]Point{e.p[0], e.p[1], e.p[2]}
			var monoX [5]Point
			n := chopQuadAtYExtrema(&points, &monoX)
			for i := 0; i <= n; i++ {
				b.pushQuad(monoX[i*2:])
			}
		case pathEdgeCubic:
			points := [4]Point{e.p[0], e.p[1], e.p[2], e.p[3]}
			var monoY [10]Point
			n := chopCubicAtYExtrema(&points, &monoY)
			for i := 0; i <= n; i++ {
				b.pushCubic(monoY[i*3:])
			}
		}
	}
	return true
}

func (b *basicEdgeBuilder) pushLine(p0, p1 Point) {
	e, ok := newLineEdge(p0, p1, b.clipShift)
	if !ok {
		return
	}
	combine := edgeCombineNo
	if e.isVertical() && len(b.edges) > 0 {
		if last := &b.edges[len(b.edges)-1]; last.kind == edgeKindLine {
			combine = edgeCombineVertical(&e, &last.line)
		}
	}

	switch combine {
	case edgeCombineTotal:
		b.edges = b.edges[:len(b.edges)-1]
	case edgeCombinePartial:
	case edgeCombineNo:
		b.edges = append(b.edges, edge{kind: edgeKindLine, line: e})
	}
}

func (b *basicEdgeBuilder) pushQuad(points []Point) {
	if e, ok := newQuadraticEdge(points, b.clipShift); ok {
		b.edges = append(b.edges, edge{kind: edgeKindQuad, quad: e})
	}
}

func (b *basicEdgeBuilder) pushCubic(points []Point) {
	if e, ok := newCubicEdge(points, b.clipShift); ok {
		b.edges = append(b.edges, edge{kind: edgeKindCubic, cubic: e})
	}
}

func edgeCombineVertical(e *lineEdge, last *lineEdge) edgeCombine {
	if last.dx != 0 || e.x != last.x {
		return edgeCombineNo
	}

	if e.winding == last.winding {
		switch {
		case e.lastY+1 == last.firstY:
			last.firstY = e.firstY
			return edgeCombinePartial
		case e.firstY == last.lastY+1:
			last.lastY = e.lastY
			return edgeCombinePartial
		}
		return edgeCombineNo
	}

	if e.firstY == last.firstY {
		switch {
		case e.lastY == last.lastY:
			return edgeCombineTotal
		case e.lastY < last.lastY:
			last.firstY = e.lastY + 1
			return edgeCombinePartial
		}
		last.firstY = last.lastY + 1
		last.lastY = e.lastY
		last.winding = e.winding
		return edgeCombinePartial
	}

	if e.lastY == last.lastY {
		if e.firstY > last.firstY {
			last.lastY = e.firstY - 1
		} else {
			last.lastY = last.firstY - 1
			last.firstY = e.firstY
			last.winding = e.winding
		}
		return edgeCombinePartial
	}

	return edgeCombineNo
}

type pathEdgeKind uint8

const (
	pathEdgeLine pathEdgeKind = iota
	pathEdgeQuad
	pathEdgeCubic
)

// pathEdge is Rust's PathEdge enum: LineTo uses p[0..2], QuadTo p[0..3], CubicTo p[0..4].
type pathEdge struct {
	kind pathEdgeKind
	p    [4]Point
}

// pathEdgeIter is a lightweight path iterator that returns only segments (lines, quads, cubics), never
// Move or Close, and always auto-closes each contour.
type pathEdgeIter struct {
	path           *Path
	verbIndex      int
	pointsIndex    int
	moveTo         Point
	needsCloseLine bool
}

func newPathEdgeIter(path *Path) pathEdgeIter { return pathEdgeIter{path: path} }

func (it *pathEdgeIter) closeLine() pathEdge {
	it.needsCloseLine = false
	return pathEdge{kind: pathEdgeLine, p: [4]Point{it.path.points[it.pointsIndex-1], it.moveTo}}
}

func (it *pathEdgeIter) next() (pathEdge, bool) {
	points := it.path.points
	for it.verbIndex < len(it.path.verbs) {
		verb := it.path.verbs[it.verbIndex]
		it.verbIndex++

		switch verb {
		case VerbMove:
			if it.needsCloseLine {
				res := it.closeLine()
				it.moveTo = points[it.pointsIndex]
				it.pointsIndex++
				return res, true
			}
			it.moveTo = points[it.pointsIndex]
			it.pointsIndex++
		case VerbClose:
			if it.needsCloseLine {
				return it.closeLine(), true
			}
		default:
			// Actual edge.
			it.needsCloseLine = true
			var e pathEdge
			switch verb {
			case VerbLine:
				e = pathEdge{kind: pathEdgeLine, p: [4]Point{points[it.pointsIndex-1], points[it.pointsIndex]}}
				it.pointsIndex++
			case VerbQuad:
				e = pathEdge{kind: pathEdgeQuad, p: [4]Point{points[it.pointsIndex-1], points[it.pointsIndex],
					points[it.pointsIndex+1]}}
				it.pointsIndex += 2
			case VerbCubic:
				e = pathEdge{kind: pathEdgeCubic, p: [4]Point{points[it.pointsIndex-1], points[it.pointsIndex],
					points[it.pointsIndex+1], points[it.pointsIndex+2]}}
				it.pointsIndex += 3
			default:
				panic("unreachable")
			}
			return e, true
		}
	}

	if it.needsCloseLine {
		return it.closeLine(), true
	}
	return pathEdge{}, false
}
