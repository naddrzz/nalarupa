package skia

// pathDirection is the direction for adding closed contours.
type pathDirection uint8

const (
	dirCW pathDirection = iota
	dirCCW
)

// PathBuilder builds a Path (path_builder.rs, a reimplementation of SkPath). Create it with
// NewPathBuilder: the zero value is Rust's PathBuilder::default(), which does not require a first move.
type PathBuilder struct {
	verbs           []PathVerb
	points          []Point
	lastMoveToIndex int
	moveToRequired  bool
}

// NewPathBuilder creates a builder.
func NewPathBuilder() *PathBuilder { return &PathBuilder{moveToRequired: true} }

// PathFromRect creates a closed clockwise contour: top left, top right, bottom right, bottom left.
func PathFromRect(r Rect) *Path {
	return &Path{
		verbs:  []PathVerb{VerbMove, VerbLine, VerbLine, VerbLine, VerbClose},
		points: []Point{{r.left, r.top}, {r.right, r.top}, {r.right, r.bottom}, {r.left, r.bottom}},
		bounds: r,
	}
}

// PathFromCircle creates a circle, see PushCircle.
func PathFromCircle(cx, cy, radius float32) *Path {
	b := NewPathBuilder()
	b.PushCircle(cx, cy, radius)
	return b.Finish()
}

// PathFromOval creates an oval, see PushOval.
func PathFromOval(oval Rect) *Path {
	b := NewPathBuilder()
	b.PushOval(oval)
	return b.Finish()
}

// Len is the number of segments.
func (b *PathBuilder) Len() int { return len(b.verbs) }

// IsEmpty reports whether no segment was added.
func (b *PathBuilder) IsEmpty() bool { return len(b.verbs) == 0 }

// MoveTo starts a contour. A move right after a move replaces it.
func (b *PathBuilder) MoveTo(x, y float32) {
	if n := len(b.verbs); n > 0 && b.verbs[n-1] == VerbMove {
		b.points[len(b.points)-1] = Point{x, y}
		return
	}
	b.lastMoveToIndex = len(b.points)
	b.moveToRequired = false
	b.verbs = append(b.verbs, VerbMove)
	b.points = append(b.points, Point{x, y})
}

func (b *PathBuilder) injectMoveToIfNeeded() {
	if !b.moveToRequired {
		return
	}
	if b.lastMoveToIndex < len(b.points) {
		p := b.points[b.lastMoveToIndex]
		b.MoveTo(p.X, p.Y)
	} else {
		b.MoveTo(0, 0)
	}
}

// LineTo adds a line from the last point. On an empty builder it first moves to (0, 0), after a close to
// the closed contour's start.
func (b *PathBuilder) LineTo(x, y float32) {
	b.injectMoveToIfNeeded()
	b.verbs = append(b.verbs, VerbLine)
	b.points = append(b.points, Point{x, y})
}

// QuadTo adds a quad curve from the last point, see LineTo.
func (b *PathBuilder) QuadTo(x1, y1, x, y float32) {
	b.injectMoveToIfNeeded()
	b.verbs = append(b.verbs, VerbQuad)
	b.points = append(b.points, Point{x1, y1}, Point{x, y})
}

func (b *PathBuilder) quadToPt(p1, p Point) { b.QuadTo(p1.X, p1.Y, p.X, p.Y) }

// conicTo adds a conic as quads; tiny-skia has no conic segments but Skia relies on them.
func (b *PathBuilder) conicTo(x1, y1, x, y, weight float32) {
	switch {
	case !(weight > 0): // <= 0 or NaN
		b.LineTo(x, y)
	case !isFinite(weight):
		b.LineTo(x1, y1)
		b.LineTo(x, y)
	case weight == 1:
		b.QuadTo(x1, y1, x, y)
	default:
		b.injectMoveToIfNeeded()
		last := b.points[len(b.points)-1]
		q, ok := computeAutoConicToQuads(last, Point{x1, y1}, Point{x, y}, weight)
		if !ok {
			return
		}
		// Points are ordered as 0 - 1 2 - 3 4 - 5 6 - ...
		offset := 1
		for range q.len {
			p1, p2 := q.points[offset], q.points[offset+1]
			b.QuadTo(p1.X, p1.Y, p2.X, p2.Y)
			offset += 2
		}
	}
}

func (b *PathBuilder) conicPointsTo(p1, p2 Point, weight float32) {
	b.conicTo(p1.X, p1.Y, p2.X, p2.Y, weight)
}

// CubicTo adds a cubic curve from the last point, see LineTo.
func (b *PathBuilder) CubicTo(x1, y1, x2, y2, x, y float32) {
	b.injectMoveToIfNeeded()
	b.verbs = append(b.verbs, VerbCubic)
	b.points = append(b.points, Point{x1, y1}, Point{x2, y2}, Point{x, y})
}

func (b *PathBuilder) cubicToPt(p1, p2, p Point) { b.CubicTo(p1.X, p1.Y, p2.X, p2.Y, p.X, p.Y) }

// Close closes the current contour; it does nothing on an empty or already closed builder. Stroking caps
// an open contour and joins a closed one at its start and end.
func (b *PathBuilder) Close() {
	if n := len(b.verbs); n > 0 && b.verbs[n-1] != VerbClose {
		b.verbs = append(b.verbs, VerbClose)
	}
	b.moveToRequired = true
}

// LastPoint returns the last point, if any.
func (b *PathBuilder) LastPoint() (Point, bool) {
	if len(b.points) == 0 {
		return Point{}, false
	}
	return b.points[len(b.points)-1], true
}

func (b *PathBuilder) setLastPoint(p Point) {
	if len(b.points) == 0 {
		b.MoveTo(p.X, p.Y)
		return
	}
	b.points[len(b.points)-1] = p
}

func (b *PathBuilder) isZeroLengthSincePoint(start int) bool {
	count := len(b.points) - start
	if count < 2 {
		return true
	}
	first := b.points[start]
	for i := 1; i < count; i++ {
		if first != b.points[start+i] {
			return false
		}
	}
	return true
}

// PushRect adds a closed clockwise rectangle contour.
func (b *PathBuilder) PushRect(r Rect) {
	b.MoveTo(r.left, r.top)
	b.LineTo(r.right, r.top)
	b.LineTo(r.right, r.bottom)
	b.LineTo(r.left, r.bottom)
	b.Close()
}

// PushOval adds a closed clockwise oval contour bounded by the rect.
func (b *PathBuilder) PushOval(oval Rect) {
	cx := half(oval.left) + half(oval.right)
	cy := half(oval.top) + half(oval.bottom)
	ovalPoints := [4]Point{{cx, oval.bottom}, {oval.left, cy}, {cx, oval.top}, {oval.right, cy}}
	rectPoints := [4]Point{{oval.right, oval.bottom}, {oval.left, oval.bottom}, {oval.left, oval.top}, {oval.right, oval.top}}
	b.MoveTo(ovalPoints[3].X, ovalPoints[3].Y)
	for i := range rectPoints {
		b.conicPointsTo(rectPoints[i], ovalPoints[i], ScalarRoot2Over2)
	}
	b.Close()
}

// PushCircle adds a closed clockwise circle contour. It does nothing for a radius <= 0 or values that are
// not finite or too large.
func (b *PathBuilder) PushCircle(x, y, r float32) {
	if rect, ok := RectFromXYWH(x-r, y-r, r+r, r+r); ok {
		b.PushOval(rect)
	}
}

// PushPath appends a path.
func (b *PathBuilder) PushPath(other *Path) {
	b.lastMoveToIndex = len(b.points)
	b.verbs = append(b.verbs, other.verbs...)
	b.points = append(b.points, other.points...)
}

func (b *PathBuilder) pushPathBuilder(other *PathBuilder) {
	if other.IsEmpty() {
		return
	}
	if b.lastMoveToIndex != 0 {
		b.lastMoveToIndex = len(b.points) + other.lastMoveToIndex
	}
	b.verbs = append(b.verbs, other.verbs...)
	b.points = append(b.points, other.points...)
}

// reversePathTo appends the first contour of other in reverse order, without its last point.
func (b *PathBuilder) reversePathTo(other *PathBuilder) {
	if other.IsEmpty() {
		return
	}
	off := len(other.points) - 1
	for i := len(other.verbs) - 1; i >= 0; i-- {
		switch other.verbs[i] {
		case VerbMove:
			return // with several contours, stop after reversing the last
		case VerbLine:
			p := other.points[off-1]
			off--
			b.LineTo(p.X, p.Y)
		case VerbQuad:
			p1, p2 := other.points[off-1], other.points[off-2]
			off -= 2
			b.QuadTo(p1.X, p1.Y, p2.X, p2.Y)
		case VerbCubic:
			p1, p2, p3 := other.points[off-1], other.points[off-2], other.points[off-3]
			off -= 3
			b.CubicTo(p1.X, p1.Y, p2.X, p2.Y, p3.X, p3.Y)
		}
	}
}

// Clear resets the builder, keeping its storage.
func (b *PathBuilder) Clear() {
	b.verbs = b.verbs[:0]
	b.points = b.points[:0]
	b.lastMoveToIndex = 0
	b.moveToRequired = true
}

// Finish returns the path, or nil when it is empty, only a move, or has non-finite points. The builder
// starts over empty.
func (b *PathBuilder) Finish() *Path {
	verbs, points := b.verbs, b.points
	*b = PathBuilder{moveToRequired: true}
	if len(verbs) <= 1 { // empty, or just a move
		return nil
	}
	bounds, ok := RectFromPoints(points)
	if !ok {
		return nil
	}
	return &Path{verbs: verbs, points: points, bounds: bounds}
}
