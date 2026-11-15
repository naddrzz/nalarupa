package skia

import mathbits "math/bits"

// Scan converter edges (edge.rs): lines, and quads and cubics walked as a series of line segments by
// forward differencing in fixed point.

// edgeMaxCoeffShift bounds curve_count (1<<shift is stored in an int8), which limits the number of lines
// approximating a curve.
const edgeMaxCoeffShift int32 = 6

type edgeKind uint8

const (
	edgeKindLine edgeKind = iota
	edgeKindQuad
	edgeKindCubic
)

// edge is Rust's Edge enum: kind says which of line, quad or cubic holds the edge.
type edge struct {
	kind  edgeKind
	line  lineEdge
	quad  quadraticEdge
	cubic cubicEdge
}

// asLine returns the current line segment of the edge.
func (e *edge) asLine() *lineEdge {
	switch e.kind {
	case edgeKindQuad:
		return &e.quad.line
	case edgeKindCubic:
		return &e.cubic.line
	}
	return &e.line
}

// edgeNone is None for the prev and next links.
const edgeNone int32 = -1

type lineEdge struct {
	// Imitate a linked list: indices into the edge slice, or edgeNone.
	prev, next int32

	x       fDot16
	dx      fDot16
	firstY  int32
	lastY   int32
	winding int8 // 1 or -1
}

// newLineEdge returns ok false for a zero-height line.
func newLineEdge(p0, p1 Point, shift int32) (lineEdge, bool) {
	scale := float32(int32(1) << (shift + 6))
	x0 := castI32(float64(float32(p0.X * scale)))
	y0 := castI32(float64(float32(p0.Y * scale)))
	x1 := castI32(float64(float32(p1.X * scale)))
	y1 := castI32(float64(float32(p1.Y * scale)))

	winding := int8(1)
	if y0 > y1 {
		x0, x1 = x1, x0
		y0, y1 = y1, y0
		winding = -1
	}

	top := fdot6Round(y0)
	bottom := fdot6Round(y1)

	// are we a zero-height line?
	if top == bottom {
		return lineEdge{}, false
	}

	slope := fdot6Div(x1-x0, y1-y0)
	dy := edgeComputeDy(top, y0)

	return lineEdge{
		prev:    edgeNone,
		next:    edgeNone,
		x:       fdot6ToFdot16(x0 + fdot16Mul(slope, dy)),
		dx:      slope,
		firstY:  top,
		lastY:   bottom - 1,
		winding: winding,
	}, true
}

func (l *lineEdge) isVertical() bool { return l.dx == 0 }

func (l *lineEdge) update(x0, y0, x1, y1 fDot16) bool {
	y0 >>= 10
	y1 >>= 10

	top := fdot6Round(y0)
	bottom := fdot6Round(y1)

	// are we a zero-height line?
	if top == bottom {
		return false
	}

	x0 >>= 10
	x1 >>= 10

	slope := fdot6Div(x1-x0, y1-y0)
	dy := edgeComputeDy(top, y0)

	l.x = fdot6ToFdot16(x0 + fdot16Mul(slope, dy))
	l.dx = slope
	l.firstY = top
	l.lastY = bottom - 1
	return true
}

type quadraticEdge struct {
	line       lineEdge
	curveCount int8
	curveShift uint8 // applied to all dx/ddx/dddx
	qx         fDot16
	qy         fDot16
	qdx        fDot16
	qdy        fDot16
	qddx       fDot16
	qddy       fDot16
	qLastX     fDot16
	qLastY     fDot16
}

// newQuadraticEdge reads points[0..3].
func newQuadraticEdge(points []Point, shift int32) (quadraticEdge, bool) {
	quad, ok := newQuadraticEdge2(points, shift)
	if !ok || !quad.update() {
		return quadraticEdge{}, false
	}
	return quad, true
}

func newQuadraticEdge2(points []Point, shift int32) (quadraticEdge, bool) {
	scale := float32(int32(1) << (shift + 6))
	x0 := castI32(float64(float32(points[0].X * scale)))
	y0 := castI32(float64(float32(points[0].Y * scale)))
	x1 := castI32(float64(float32(points[1].X * scale)))
	y1 := castI32(float64(float32(points[1].Y * scale)))
	x2 := castI32(float64(float32(points[2].X * scale)))
	y2 := castI32(float64(float32(points[2].Y * scale)))

	winding := int8(1)
	if y0 > y2 {
		x0, x2 = x2, x0
		y0, y2 = y2, y0
		winding = -1
	}

	top := fdot6Round(y0)
	bottom := fdot6Round(y2)

	// are we a zero-height quad (line)?
	if top == bottom {
		return quadraticEdge{}, false
	}

	// compute number of steps needed (1 << shift)
	{
		dx := (leftShift(x1, 1) - x0 - x2) >> 2
		dy := (leftShift(y1, 1) - y0 - y2) >> 2
		// Before this line shift is the scale up factor for AA; after it, the curve shift.
		shift = edgeDiffToShift(dx, dy, shift)
	}

	// need at least 1 subdivision for our bias trick
	if shift == 0 {
		shift = 1
	} else if shift > edgeMaxCoeffShift {
		shift = edgeMaxCoeffShift
	}

	curveCount := int8(int32(1) << shift)

	// p0 (1 - t)^2 + p1 t(1 - t) + p2 t^2 ==> At^2 + Bt + C, with A = p0 - 2p1 + p2, B = 2(p1 - p0),
	// C = p0. A and B are stored at 1/2 of their value to guard against 16.16 overflow, and the 2x is
	// applied in update(); hence curveShift is shift - 1.
	curveShift := uint8(shift - 1)

	a := edgeFdot6ToFixedDiv2(x0 - x1 - x1 + x2) // 1/2 the real value
	b := fdot6ToFdot16(x1 - x0)                  // 1/2 the real value

	qx := fdot6ToFdot16(x0)
	qdx := b + (a >> shift)  // biased by shift
	qddx := a >> (shift - 1) // biased by shift

	a = edgeFdot6ToFixedDiv2(y0 - y1 - y1 + y2) // 1/2 the real value
	b = fdot6ToFdot16(y1 - y0)                  // 1/2 the real value

	qy := fdot6ToFdot16(y0)
	qdy := b + (a >> shift)  // biased by shift
	qddy := a >> (shift - 1) // biased by shift

	return quadraticEdge{
		line:       lineEdge{prev: edgeNone, next: edgeNone, winding: winding},
		curveCount: curveCount,
		curveShift: curveShift,
		qx:         qx,
		qy:         qy,
		qdx:        qdx,
		qdy:        qdy,
		qddx:       qddx,
		qddy:       qddy,
		qLastX:     fdot6ToFdot16(x2),
		qLastY:     fdot6ToFdot16(y2),
	}, true
}

func (q *quadraticEdge) update() bool {
	var success bool
	count := q.curveCount
	oldx := q.qx
	oldy := q.qy
	dx := q.qdx
	dy := q.qdy
	var newx, newy fDot16
	shift := q.curveShift

	for {
		count--
		if count > 0 {
			newx = oldx + (dx >> shift)
			dx += q.qddx
			newy = oldy + (dy >> shift)
			dy += q.qddy
		} else {
			// last segment
			newx = q.qLastX
			newy = q.qLastY
		}
		success = q.line.update(oldx, oldy, newx, newy)
		oldx = newx
		oldy = newy

		if count == 0 || success {
			break
		}
	}

	q.qx = newx
	q.qy = newy
	q.qdx = dx
	q.qdy = dy
	q.curveCount = count
	return success
}

type cubicEdge struct {
	line       lineEdge
	curveCount int8
	curveShift uint8 // applied to all dx/ddx/dddx except for dshift exception
	dshift     uint8 // applied to cdx and cdy
	cx         fDot16
	cy         fDot16
	cdx        fDot16
	cdy        fDot16
	cddx       fDot16
	cddy       fDot16
	cdddx      fDot16
	cdddy      fDot16
	cLastX     fDot16
	cLastY     fDot16
}

// newCubicEdge reads points[0..4].
func newCubicEdge(points []Point, shift int32) (cubicEdge, bool) {
	cubic, ok := newCubicEdge2(points, shift, true)
	if !ok || !cubic.update() {
		return cubicEdge{}, false
	}
	return cubic, true
}

func newCubicEdge2(points []Point, shift int32, sortY bool) (cubicEdge, bool) {
	scale := float32(int32(1) << (shift + 6))
	x0 := castI32(float64(float32(points[0].X * scale)))
	y0 := castI32(float64(float32(points[0].Y * scale)))
	x1 := castI32(float64(float32(points[1].X * scale)))
	y1 := castI32(float64(float32(points[1].Y * scale)))
	x2 := castI32(float64(float32(points[2].X * scale)))
	y2 := castI32(float64(float32(points[2].Y * scale)))
	x3 := castI32(float64(float32(points[3].X * scale)))
	y3 := castI32(float64(float32(points[3].Y * scale)))

	winding := int8(1)
	if sortY && y0 > y3 {
		x0, x3 = x3, x0
		x1, x2 = x2, x1
		y0, y3 = y3, y0
		y1, y2 = y2, y1
		winding = -1
	}

	top := fdot6Round(y0)
	bot := fdot6Round(y3)

	// are we a zero-height cubic (line)?
	if sortY && top == bot {
		return cubicEdge{}, false
	}

	// compute number of steps needed (1 << shift)
	{
		// The center of the curve need not be the max delta from the baseline (it could even be
		// coincident), so look at the two off-curve points.
		dx := edgeCubicDeltaFromLine(x0, x1, x2, x3)
		dy := edgeCubicDeltaFromLine(y0, y1, y2, y3)
		// add 1 (by observation)
		shift = edgeDiffToShift(dx, dy, 2) + 1
	}
	// need at least 1 subdivision for our bias trick
	if shift > edgeMaxCoeffShift {
		shift = edgeMaxCoeffShift
	}

	// The incoming data is shifted down by 10 (or 8 for AA), so the most we can shift up is 8; the
	// coefficients are computed with a 3*, so the safest upshift is 6.
	upShift := int32(6) // largest safe value
	downShift := shift + upShift - 10
	if downShift < 0 {
		downShift = 0
		upShift = 10 - shift
	}

	curveCount := int8(leftShift(-1, shift))
	curveShift := uint8(shift)
	dshift := uint8(downShift)

	b := edgeFdot6UpShift(3*(x1-x0), upShift)
	c := edgeFdot6UpShift(3*(x0-x1-x1+x2), upShift)
	d := edgeFdot6UpShift(x3+3*(x1-x2)-x0, upShift)

	cx := fdot6ToFdot16(x0)
	cdx := b + (c >> shift) + (d >> (2 * shift)) // biased by shift
	cddx := 2*c + ((3 * d) >> (shift - 1))       // biased by 2*shift
	cdddx := (3 * d) >> (shift - 1)              // biased by 2*shift

	b = edgeFdot6UpShift(3*(y1-y0), upShift)
	c = edgeFdot6UpShift(3*(y0-y1-y1+y2), upShift)
	d = edgeFdot6UpShift(y3+3*(y1-y2)-y0, upShift)

	cy := fdot6ToFdot16(y0)
	cdy := b + (c >> shift) + (d >> (2 * shift)) // biased by shift
	cddy := 2*c + ((3 * d) >> (shift - 1))       // biased by 2*shift
	cdddy := (3 * d) >> (shift - 1)              // biased by 2*shift

	return cubicEdge{
		line:       lineEdge{prev: edgeNone, next: edgeNone, winding: winding},
		curveCount: curveCount,
		curveShift: curveShift,
		dshift:     dshift,
		cx:         cx,
		cy:         cy,
		cdx:        cdx,
		cdy:        cdy,
		cddx:       cddx,
		cddy:       cddy,
		cdddx:      cdddx,
		cdddy:      cdddy,
		cLastX:     fdot6ToFdot16(x3),
		cLastY:     fdot6ToFdot16(y3),
	}, true
}

func (c *cubicEdge) update() bool {
	var success bool
	count := c.curveCount
	oldx := c.cx
	oldy := c.cy
	var newx, newy fDot16
	ddshift := c.curveShift
	dshift := c.dshift

	for {
		count++
		if count < 0 {
			newx = oldx + (c.cdx >> dshift)
			c.cdx += c.cddx >> ddshift
			c.cddx += c.cdddx

			newy = oldy + (c.cdy >> dshift)
			c.cdy += c.cddy >> ddshift
			c.cddy += c.cdddy
		} else {
			// last segment
			newx = c.cLastX
			newy = c.cLastY
		}

		// Finite fixed point does not always keep oldy <= newy, so pin it here.
		if newy < oldy {
			newy = oldy
		}

		success = c.line.update(oldx, oldy, newx, newy)
		oldx = newx
		oldy = newy

		if count == 0 || success {
			break
		}
	}

	c.cx = newx
	c.cy = newy
	c.curveCount = count
	return success
}

// edgeComputeDy favors the lower pixel when y0 is on a 1/2 pixel boundary.
func edgeComputeDy(top, y0 fDot6) fDot6 { return leftShift(top, 6) + 32 - y0 }

func edgeDiffToShift(dx, dy fDot6, shiftAA int32) int32 {
	// cheap calc of distance from center of p0-p2 to the center of the curve
	dist := edgeCheapDistance(dx, dy)

	// Shift dist (dot6) down by 3 for 1/8 pixel accuracy; with AA everything is scaled up, so the
	// accuracy can be lower.
	dist = (dist + (1 << (2 + shiftAA))) >> (3 + shiftAA)

	// each subdivision (shift value) cuts this dist (error) by 1/4
	return (32 - int32(mathbits.LeadingZeros32(uint32(dist)))) >> 1
}

func edgeCheapDistance(dx, dy fDot6) fDot6 {
	// i32::abs wraps for MIN in release, as Go's negation does
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	// return max + min/2
	if dx > dy {
		return dx + (dy >> 1)
	}
	return dy + (dx >> 1)
}

// edgeFdot6ToFixedDiv2 is SkFDot6ToFixed(value >> 1) without losing the low bit of value.
func edgeFdot6ToFixedDiv2(value fDot6) fDot16 { return leftShift(value, 16-6-1) }

func edgeFdot6UpShift(x fDot6, upShift int32) int32 { return leftShift(x, upShift) }

// edgeCubicDeltaFromLine approximates the distance of the off-curve points from the baseline:
// f(1/3)-b = (8a - 15b + 6c + d) / 27 and f(2/3)-c = (a + 6b - 15c + 8d) / 27, with 19/512 for 1/27.
func edgeCubicDeltaFromLine(a, b, c, d fDot6) fDot6 {
	// the parameters may be negative, so no <<
	oneThird := ((a*8 - b*15 + 6*c + d) * 19) >> 9
	twoThird := ((a + 6*b - c*15 + d*8) * 19) >> 9
	if oneThird < 0 {
		oneThird = -oneThird
	}
	if twoThird < 0 {
		twoThird = -twoThird
	}
	return max(oneThird, twoThird)
}
