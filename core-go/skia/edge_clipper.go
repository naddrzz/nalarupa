package skia

// Clipping path segments to a rect before edge building (edge_clipper.rs).

// edgeClipperMaxVerbs: max curvature in X and Y split a cubic into 9 pieces, * (line + cubic).
const edgeClipperMaxVerbs = 18

type edgeClipper struct {
	clip              Rect
	canCullToTheRight bool
	edges             []pathEdge // at most edgeClipperMaxVerbs (an ArrayVec in Rust)
}

// clipLine and the other clip methods return the clipped edges, or nil if there are none.
func (c *edgeClipper) clipLine(p0, p1 Point) []pathEdge {
	var buf [lineClipperMaxPoints]Point
	points := lineClip(&[2]Point{p0, p1}, &c.clip, c.canCullToTheRight, &buf)
	if len(points) > 0 {
		for i := 0; i < len(points)-1; i++ {
			c.pushLine(points[i], points[i+1])
		}
	}
	return c.result()
}

func (c *edgeClipper) result() []pathEdge {
	if len(c.edges) == 0 {
		return nil
	}
	return c.edges
}

func (c *edgeClipper) push(e pathEdge) {
	if len(c.edges) >= edgeClipperMaxVerbs {
		panic("skia: edge clipper overflow") // ArrayVec::push panics when full
	}
	c.edges = append(c.edges, e)
}

func (c *edgeClipper) pushLine(p0, p1 Point) {
	c.push(pathEdge{kind: pathEdgeLine, p: [4]Point{p0, p1}})
}

func (c *edgeClipper) pushVLine(x, y0, y1 float32, reverse bool) {
	if reverse {
		y0, y1 = y1, y0
	}
	c.push(pathEdge{kind: pathEdgeLine, p: [4]Point{{x, y0}, {x, y1}}})
}

func (c *edgeClipper) clipQuad(p0, p1, p2 Point) []pathEdge {
	pts := [3]Point{p0, p1, p2}
	bounds, ok := RectFromPoints(pts[:])
	if !ok {
		return nil
	}

	if !edgeClipQuickReject(bounds, c.clip) {
		var monoY [5]Point
		countY := chopQuadAtYExtrema(&pts, &monoY)
		for y := 0; y <= countY; y++ {
			var monoX [5]Point
			yPoints := [3]Point{monoY[y*2], monoY[y*2+1], monoY[y*2+2]}
			countX := chopQuadAtXExtrema(&yPoints, &monoX)
			for x := 0; x <= countX; x++ {
				xPoints := [3]Point{monoX[x*2], monoX[x*2+1], monoX[x*2+2]}
				c.clipMonoQuad(&xPoints)
			}
		}
	}
	return c.result()
}

// clipMonoQuad needs src monotonic in X and Y.
func (c *edgeClipper) clipMonoQuad(src *[3]Point) {
	var pts [3]Point
	reverse := edgeClipSortIncreasingY(src[:], pts[:])

	// are we completely above or below
	if pts[2].Y <= c.clip.Top() || pts[0].Y >= c.clip.Bottom() {
		return
	}

	// Now chop so that pts is contained within clip in Y
	edgeClipChopQuadInY(c.clip, &pts)

	if pts[0].X > pts[2].X {
		pts[0], pts[2] = pts[2], pts[0]
		reverse = !reverse
	}

	// Now chop in X as needed, and record the segments

	if pts[2].X <= c.clip.Left() {
		// wholly to the left
		c.pushVLine(c.clip.Left(), pts[0].Y, pts[2].Y, reverse)
		return
	}

	if pts[0].X >= c.clip.Right() {
		// wholly to the right
		if !c.canCullToTheRight {
			c.pushVLine(c.clip.Right(), pts[0].Y, pts[2].Y, reverse)
		}
		return
	}

	t := float32(0.5) // NormalizedF32Exclusive::ANY
	var tmp [5]Point

	// are we partially to the left
	if pts[0].X < c.clip.Left() {
		if edgeClipChopMonoQuadAt(pts[0].X, pts[1].X, pts[2].X, c.clip.Left(), &t) {
			chopQuadAt(pts[:], t, &tmp)
			c.pushVLine(c.clip.Left(), tmp[0].Y, tmp[2].Y, reverse)
			// clamp to clean up imprecise numerics in the chop
			tmp[2].X = c.clip.Left()
			tmp[3].X = maxf(tmp[3].X, c.clip.Left())

			pts[0] = tmp[2]
			pts[1] = tmp[3]
		} else {
			// the chop failed on inexact numerics, so just clamp against the left
			c.pushVLine(c.clip.Left(), pts[0].Y, pts[2].Y, reverse)
			return
		}
	}

	// are we partially to the right
	if pts[2].X > c.clip.Right() {
		if edgeClipChopMonoQuadAt(pts[0].X, pts[1].X, pts[2].X, c.clip.Right(), &t) {
			chopQuadAt(pts[:], t, &tmp)
			// clamp to clean up imprecise numerics in the chop
			tmp[1].X = minf(tmp[1].X, c.clip.Right())
			tmp[2].X = c.clip.Right()

			c.pushQuad(&[3]Point{tmp[0], tmp[1], tmp[2]}, reverse)
			c.pushVLine(c.clip.Right(), tmp[2].Y, tmp[4].Y, reverse)
		} else {
			// the chop failed on inexact numerics, so just clamp against the right
			pts[1].X = minf(pts[1].X, c.clip.Right())
			pts[2].X = minf(pts[2].X, c.clip.Right())
			c.pushQuad(&pts, reverse)
		}
	} else {
		// wholly inside the clip
		c.pushQuad(&pts, reverse)
	}
}

func (c *edgeClipper) pushQuad(pts *[3]Point, reverse bool) {
	if reverse {
		c.push(pathEdge{kind: pathEdgeQuad, p: [4]Point{pts[2], pts[1], pts[0]}})
	} else {
		c.push(pathEdge{kind: pathEdgeQuad, p: [4]Point{pts[0], pts[1], pts[2]}})
	}
}

func (c *edgeClipper) clipCubic(p0, p1, p2, p3 Point) []pathEdge {
	pts := [4]Point{p0, p1, p2, p3}
	bounds, ok := RectFromPoints(pts[:])
	if !ok {
		return nil
	}

	// check if we're clipped out vertically
	if bounds.Bottom() > c.clip.Top() && bounds.Top() < c.clip.Bottom() {
		if edgeClipTooBigForReliableFloatMath(bounds) {
			// The cubic cannot be clipped safely, so draw a line (which can be).
			return c.clipLine(p0, p3)
		}
		var monoY [10]Point
		countY := chopCubicAtYExtrema(&pts, &monoY)
		for y := 0; y <= countY; y++ {
			var monoX [10]Point
			yPoints := [4]Point{monoY[y*3], monoY[y*3+1], monoY[y*3+2], monoY[y*3+3]}
			countX := chopCubicAtXExtrema(&yPoints, &monoX)
			for x := 0; x <= countX; x++ {
				xPoints := [4]Point{monoX[x*3], monoX[x*3+1], monoX[x*3+2], monoX[x*3+3]}
				c.clipMonoCubic(&xPoints)
			}
		}
	}
	return c.result()
}

// clipMonoCubic needs src monotonic in X and Y.
func (c *edgeClipper) clipMonoCubic(src *[4]Point) {
	var pts [4]Point
	reverse := edgeClipSortIncreasingY(src[:], pts[:])

	// are we completely above or below
	if pts[3].Y <= c.clip.Top() || pts[0].Y >= c.clip.Bottom() {
		return
	}

	// Now chop so that pts is contained within clip in Y
	edgeClipChopCubicInY(c.clip, &pts)

	if pts[0].X > pts[3].X {
		pts[0], pts[3] = pts[3], pts[0]
		pts[1], pts[2] = pts[2], pts[1]
		reverse = !reverse
	}

	// Now chop in X as needed, and record the segments

	if pts[3].X <= c.clip.Left() {
		// wholly to the left
		c.pushVLine(c.clip.Left(), pts[0].Y, pts[3].Y, reverse)
		return
	}

	if pts[0].X >= c.clip.Right() {
		// wholly to the right
		if !c.canCullToTheRight {
			c.pushVLine(c.clip.Right(), pts[0].Y, pts[3].Y, reverse)
		}
		return
	}

	// are we partially to the left
	if pts[0].X < c.clip.Left() {
		var tmp [7]Point
		edgeClipChopMonoCubicAtX(&pts, c.clip.Left(), &tmp)
		c.pushVLine(c.clip.Left(), tmp[0].Y, tmp[3].Y, reverse)

		// tmp[3, 4].x should be right of clip.left(); the chopper's numerics cannot be trusted, so
		// force it.
		tmp[3].X = c.clip.Left()
		tmp[4].X = maxf(tmp[4].X, c.clip.Left())

		pts[0] = tmp[3]
		pts[1] = tmp[4]
		pts[2] = tmp[5]
	}

	// are we partially to the right
	if pts[3].X > c.clip.Right() {
		var tmp [7]Point
		edgeClipChopMonoCubicAtX(&pts, c.clip.Right(), &tmp)
		tmp[3].X = c.clip.Right()
		tmp[2].X = minf(tmp[2].X, c.clip.Right())

		c.pushCubic(&[4]Point{tmp[0], tmp[1], tmp[2], tmp[3]}, reverse)
		c.pushVLine(c.clip.Right(), tmp[3].Y, tmp[6].Y, reverse)
	} else {
		// wholly inside the clip
		c.pushCubic(&pts, reverse)
	}
}

func (c *edgeClipper) pushCubic(pts *[4]Point, reverse bool) {
	if reverse {
		c.push(pathEdge{kind: pathEdgeCubic, p: [4]Point{pts[3], pts[2], pts[1], pts[0]}})
	} else {
		c.push(pathEdge{kind: pathEdgeCubic, p: *pts})
	}
}

// edgeClipperIter clips each path segment and yields the non-empty results.
type edgeClipperIter struct {
	edgeIter          pathEdgeIter
	clip              Rect
	canCullToTheRight bool
	buf               []pathEdge
}

func newEdgeClipperIter(path *Path, clip Rect, canCullToTheRight bool) *edgeClipperIter {
	return &edgeClipperIter{
		edgeIter:          newPathEdgeIter(path),
		clip:              clip,
		canCullToTheRight: canCullToTheRight,
		buf:               make([]pathEdge, 0, edgeClipperMaxVerbs),
	}
}

// next returns the clipped edges of the next segment that has any. The slice is reused by the following
// call, as Rust's iterator hands out a fresh ArrayVec only valid until then in the builder's loop.
func (it *edgeClipperIter) next() ([]pathEdge, bool) {
	for {
		e, ok := it.edgeIter.next()
		if !ok {
			return nil, false
		}
		clipper := edgeClipper{clip: it.clip, canCullToTheRight: it.canCullToTheRight, edges: it.buf[:0]}

		var edges []pathEdge
		switch e.kind {
		case pathEdgeLine:
			edges = clipper.clipLine(e.p[0], e.p[1])
		case pathEdgeQuad:
			edges = clipper.clipQuad(e.p[0], e.p[1], e.p[2])
		case pathEdgeCubic:
			edges = clipper.clipCubic(e.p[0], e.p[1], e.p[2], e.p[3])
		}
		if edges != nil {
			return edges, true
		}
	}
}

func edgeClipQuickReject(bounds, clip Rect) bool {
	return bounds.Top() >= clip.Bottom() || bounds.Bottom() <= clip.Top()
}

// edgeClipSortIncreasingY copies src (monotonic in Y) into dst sorted by increasing Y, and reports
// whether it reversed the order.
func edgeClipSortIncreasingY(src, dst []Point) bool {
	if src[0].Y > src[len(src)-1].Y {
		for i := range src {
			dst[i] = src[len(src)-1-i]
		}
		return true
	}
	copy(dst, src)
	return false
}

// edgeClipChopQuadInY clips pts in Y to the clip rect, in place.
func edgeClipChopQuadInY(clip Rect, pts *[3]Point) {
	t := float32(0.5) // NormalizedF32Exclusive::ANY
	var tmp [5]Point

	// are we partially above
	if pts[0].Y < clip.Top() {
		if edgeClipChopMonoQuadAt(pts[0].Y, pts[1].Y, pts[2].Y, clip.Top(), &t) {
			// take the 2nd chopped quad
			chopQuadAt(pts[:], t, &tmp)
			// clamp to clean up imprecise numerics in the chop
			tmp[2].Y = clip.Top()
			tmp[3].Y = maxf(tmp[3].Y, clip.Top())

			pts[0] = tmp[2]
			pts[1] = tmp[3]
		} else {
			// the chop failed on inexact numerics, so just clamp against the top
			for i := range pts {
				if pts[i].Y < clip.Top() {
					pts[i].Y = clip.Top()
				}
			}
		}
	}

	// are we partially below
	if pts[2].Y > clip.Bottom() {
		if edgeClipChopMonoQuadAt(pts[0].Y, pts[1].Y, pts[2].Y, clip.Bottom(), &t) {
			chopQuadAt(pts[:], t, &tmp)
			// clamp to clean up imprecise numerics in the chop
			tmp[1].Y = minf(tmp[1].Y, clip.Bottom())
			tmp[2].Y = clip.Bottom()

			pts[1] = tmp[1]
			pts[2] = tmp[2]
		} else {
			// the chop failed on inexact numerics, so just clamp against the bottom
			for i := range pts {
				if pts[i].Y > clip.Bottom() {
					pts[i].Y = clip.Bottom()
				}
			}
		}
	}
}

// edgeClipChopMonoQuadAt solves F(t) = target, F(t) := [0](1-t)^2 + 2[1]t(1-t) + [2]t^2, as
// At^2 + Bt + C; it sets t to the first root.
func edgeClipChopMonoQuadAt(c0, c1, c2, target float32, t *float32) bool {
	a := c0 - c1 - c1 + c2
	b := float32(2 * (c1 - c0))
	c := c0 - target

	var roots [3]float32
	if findUnitQuadRoots(a, b, c, &roots) != 0 {
		*t = roots[0]
		return true
	}
	return false
}

// edgeClipTooBigForReliableFloatMath: the limit is the largest value for which chopping at extrema and
// at X or Y values still works reliably, chosen by experiment in Skia.
func edgeClipTooBigForReliableFloatMath(r Rect) bool {
	const limit = float32(1 << 22)
	return r.Left() < -limit || r.Top() < -limit || r.Right() > limit || r.Bottom() > limit
}

// edgeClipChopCubicInY clips pts in Y to the clip rect, in place.
func edgeClipChopCubicInY(clip Rect, pts *[4]Point) {
	// are we partially above
	if pts[0].Y < clip.Top() {
		var tmp [7]Point
		edgeClipChopMonoCubicAtY(pts, clip.Top(), &tmp)

		// A poor chop of a large cubic can leave the lower part partly above the clip. If the first 3
		// Ys are above the top, smashing them would distort the cubic, so re-chop tmp[3..7] against the
		// top first; then smash the first 1 or 2 Y values.
		if tmp[3].Y < clip.Top() && tmp[4].Y < clip.Top() && tmp[5].Y < clip.Top() {
			tmp2 := [4]Point{tmp[3], tmp[4], tmp[5], tmp[6]}
			edgeClipChopMonoCubicAtY(&tmp2, clip.Top(), &tmp)
		}

		// tmp[3, 4].y should be below the clip top; the chopper's numerics cannot be trusted, so force it.
		tmp[3].Y = clip.Top()
		tmp[4].Y = maxf(tmp[4].Y, clip.Top())

		pts[0] = tmp[3]
		pts[1] = tmp[4]
		pts[2] = tmp[5]
	}

	// are we partially below
	if pts[3].Y > clip.Bottom() {
		var tmp [7]Point
		edgeClipChopMonoCubicAtY(pts, clip.Bottom(), &tmp)
		tmp[3].Y = clip.Bottom()
		tmp[2].Y = minf(tmp[2].Y, clip.Bottom())

		pts[1] = tmp[1]
		pts[2] = tmp[2]
		pts[3] = tmp[3]
	}
}

func edgeClipChopMonoCubicAtX(src *[4]Point, x float32, dst *[7]Point) {
	if chopMonoCubicAtX(src, x, dst) {
		return
	}
	srcValues := [4]float32{src[0].X, src[1].X, src[2].X, src[3].X}
	chopCubicAt2(src, edgeClipMonoCubicClosestT(&srcValues, x), dst[:])
}

func edgeClipChopMonoCubicAtY(src *[4]Point, y float32, dst *[7]Point) {
	if chopMonoCubicAtY(src, y, dst) {
		return
	}
	srcValues := [4]float32{src[0].Y, src[1].Y, src[2].Y, src[3].Y}
	chopCubicAt2(src, edgeClipMonoCubicClosestT(&srcValues, y), dst[:])
}

// edgeClipMonoCubicClosestT binary searches the t (0 < t < 1) whose coordinate is closest to x.
func edgeClipMonoCubicClosestT(src *[4]float32, x float32) float32 {
	t := float32(0.5)
	var lastT float32
	bestT := t
	step := float32(0.25)
	d := src[0]
	a := src[3] + float32(3*(src[1]-src[2])) - d
	b := float32(3 * (src[2] - src[1] - src[1] + d))
	c := float32(3 * (src[1] - d))
	x -= d
	closest := ScalarMax
	for {
		loc := float32((float32(float32(float32(a*t)+b)*t) + c) * t)
		dist := abs32(loc - x)
		if closest > dist {
			closest = dist
			bestT = t
		}

		lastT = t
		if loc < x {
			t += step
		} else {
			t += -step
		}
		step = float32(step * 0.5)

		if !(closest > 0.25 && lastT != t) {
			break
		}
	}

	r, ok := newNormalizedExclusive(bestT)
	if !ok {
		panic("skia: closest t out of range") // NormalizedF32Exclusive::new(..).unwrap()
	}
	return r
}
