package skia

import (
	"math"
	"sort"
)

// Non anti-aliased path filling (scan/path.rs).

// fillPath fills path into clip through b.
func fillPath(path *Path, fillRule FillRule, clip screenIntRect, b blitter) {
	ir, ok := conservativeRoundToInt(path.Bounds())
	if !ok {
		return
	}

	// If the bounds cannot be converted into a screenIntRect, the path is out of clip.
	pathContainedInClip := false
	if bounds, ok := ir.toScreenIntRect(); ok {
		pathContainedInClip = clip.Contains(bounds)
	}

	fillPathImpl(path, fillRule, clip, ir.Y(), ir.Bottom(), 0, pathContainedInClip, b)
}

// conservativeRoundToInt nudges the int rect slightly larger than Rect.Round might, as a safety net:
// the scan converter skips clipping when it thinks the edges fit in the clip, but its accumulated slope
// additions introduce slight numeric errors.
func conservativeRoundToInt(src Rect) (IntRect, bool) {
	// From LTRB, otherwise the rounding is wrong.
	return IntRectFromLTRB(
		roundDownToInt(src.Left()),
		roundDownToInt(src.Top()),
		roundUpToInt(src.Right()),
		roundUpToInt(src.Bottom()),
	)
}

// conservativeRoundBias nudges int rects a little larger, so a path is not "thought" inside a clip
// when, due to numeric drift in the scan converter, it might walk beyond it. Found by trial and error in
// Skia (crbug.com/844457); cubics are the main reason for it.
const conservativeRoundBias = 0.5 + 1.5/float64(fdot6One)

// roundDownToInt rounds the top and left, as the scan converter treats them, biased smaller.
func roundDownToInt(x float32) int32 {
	xx := float64(x)
	xx -= conservativeRoundBias
	return saturateFromF64(math.Ceil(xx))
}

// roundUpToInt rounds the right and bottom, biased larger.
func roundUpToInt(x float32) int32 {
	xx := float64(x)
	xx += conservativeRoundBias
	return saturateFromF64(math.Floor(xx))
}

// saturateFromF64 is i32::saturate_from(f64): the closest integer, i32::MAX for NaN.
func saturateFromF64(x float64) int32 {
	if !(x < math.MaxInt32) {
		x = math.MaxInt32
	}
	if !(x > math.MinInt32) {
		x = math.MinInt32
	}
	return castI32(x)
}

func fillPathImpl(path *Path, fillRule FillRule, clipRect screenIntRect, startY, stopY, shiftEdgesUp int32,
	pathContainedInClip bool, b blitter) {
	shiftedClip, ok := newShiftedIntRect(clipRect, shiftEdgesUp)
	if !ok {
		return
	}

	var clip *shiftedIntRect
	if !pathContainedInClip {
		clip = &shiftedClip
	}
	built := buildEdges(path, clip, shiftEdgesUp)
	if built == nil {
		return // no edges to render
	}

	// Rust's sort_by is stable; the keys are a total order, so any stable sort gives the same result.
	sort.SliceStable(built, func(i, j int) bool {
		a, b := built[i].asLine(), built[j].asLine()
		if a.firstY == b.firstY {
			return a.x < b.x
		}
		return a.firstY < b.firstY
	})

	// Index 0 is the head and len+1 the tail, so the edges are 1..=len.
	edges := make([]edge, 0, len(built)+2)
	edges = append(edges, edge{kind: edgeKindLine, line: lineEdge{
		prev:   edgeNone,
		next:   1,
		x:      math.MinInt32,
		firstY: math.MinInt32, // EDGE_HEAD_Y
	}})
	edges = append(edges, built...)
	for i := 1; i <= len(built); i++ {
		l := edges[i].asLine()
		l.prev = int32(i - 1)
		l.next = int32(i + 1)
	}
	edges = append(edges, edge{kind: edgeKindLine, line: lineEdge{
		prev:   int32(len(edges) - 1),
		next:   edgeNone,
		firstY: math.MaxInt32, // EDGE_TAIL_Y
	}})

	startY <<= shiftEdgesUp
	stopY <<= shiftEdgesUp

	top := int32(shiftedClip.shifted.Y())
	if !pathContainedInClip && startY < top {
		startY = top
	}

	bottom := int32(shiftedClip.shifted.Bottom())
	if !pathContainedInClip && stopY > bottom {
		stopY = bottom
	}

	if startY < 0 || stopY < 0 {
		return // u32::try_from failed
	}

	walkEdges(fillRule, uint32(startY), uint32(stopY), shiftedClip.shifted.Right(), edges, b)
}

func walkEdges(fillRule FillRule, startY, stopY, rightClip uint32, edges []edge, b blitter) {
	currY := startY
	windingMask := int32(-1)
	if fillRule == FillRuleEvenOdd {
		windingMask = 1
	}

	for {
		w := int32(0)
		left := uint32(0)
		prevX := edges[0].asLine().x

		currIdx := edges[0].asLine().next
		for edges[currIdx].asLine().firstY <= int32(currY) {
			curr := &edges[currIdx]
			x := uint32(fdot16RoundToI32(curr.asLine().x))

			if w&windingMask == 0 {
				// we're starting interval
				left = x
			}

			w += int32(curr.asLine().winding)

			if w&windingMask == 0 {
				// we finished an interval
				if width := x - left; width != 0 {
					b.blitH(left, currY, width)
				}
			}

			nextIdx := curr.asLine().next

			if curr.asLine().lastY == int32(currY) {
				// are we done with this edge?
				switch curr.kind {
				case edgeKindLine:
					removeEdge(currIdx, edges)
				case edgeKindQuad:
					if curr.quad.curveCount > 0 && curr.quad.update() {
						newX := curr.quad.line.x
						if newX < prevX {
							// ripple current edge backwards until it is x-sorted
							backwardInsertEdgeBasedOnX(currIdx, edges)
						} else {
							prevX = newX
						}
					} else {
						removeEdge(currIdx, edges)
					}
				case edgeKindCubic:
					if curr.cubic.curveCount < 0 && curr.cubic.update() {
						newX := curr.cubic.line.x
						if newX < prevX {
							// ripple current edge backwards until it is x-sorted
							backwardInsertEdgeBasedOnX(currIdx, edges)
						} else {
							prevX = newX
						}
					} else {
						removeEdge(currIdx, edges)
					}
				}
			} else {
				l := curr.asLine()
				newX := l.x + l.dx
				l.x = newX

				if newX < prevX {
					// ripple current edge backwards until it is x-sorted
					backwardInsertEdgeBasedOnX(currIdx, edges)
				} else {
					prevX = newX
				}
			}

			currIdx = nextIdx
		}

		if w&windingMask != 0 {
			// was our right-edge culled away?
			if width := rightClip - left; width != 0 {
				b.blitH(left, currY, width)
			}
		}

		currY++
		if currY >= stopY {
			break
		}

		// now current edge points to the first edge with a Yint larger than currY
		insertNewEdges(currIdx, int32(currY), edges)
	}
}

func removeEdge(currIdx int32, edges []edge) {
	prev := edges[currIdx].asLine().prev
	next := edges[currIdx].asLine().next

	edges[prev].asLine().next = next
	edges[next].asLine().prev = prev
}

func backwardInsertEdgeBasedOnX(currIdx int32, edges []edge) {
	x := edges[currIdx].asLine().x
	prevIdx := edges[currIdx].asLine().prev
	for prevIdx != 0 {
		if edges[prevIdx].asLine().x > x {
			prevIdx = edges[prevIdx].asLine().prev
		} else {
			break
		}
	}

	nextIdx := edges[prevIdx].asLine().next
	if nextIdx != currIdx {
		removeEdge(currIdx, edges)
		insertEdgeAfter(currIdx, prevIdx, edges)
	}
}

func insertEdgeAfter(currIdx, afterIdx int32, edges []edge) {
	edges[currIdx].asLine().prev = afterIdx
	edges[currIdx].asLine().next = edges[afterIdx].asLine().next

	afterNextIdx := edges[afterIdx].asLine().next
	edges[afterNextIdx].asLine().prev = currIdx
	edges[afterIdx].asLine().next = currIdx
}

// backwardInsertStart searches backwards from the right for the point to begin inserting new edges.
func backwardInsertStart(prevIdx int32, x fDot16, edges []edge) int32 {
	for {
		prev := edges[prevIdx].asLine().prev
		if prev == edgeNone {
			break
		}
		prevIdx = prev
		if edges[prevIdx].asLine().x <= x {
			break
		}
	}
	return prevIdx
}

func insertNewEdges(newIdx int32, currY int32, edges []edge) {
	if edges[newIdx].asLine().firstY != currY {
		return
	}

	prevIdx := edges[newIdx].asLine().prev
	if edges[prevIdx].asLine().x <= edges[newIdx].asLine().x {
		return
	}

	// find first x pos to insert
	startIdx := backwardInsertStart(prevIdx, edges[newIdx].asLine().x, edges)
	// insert the lot, fixing up the links as we go
	for {
		nextIdx := edges[newIdx].asLine().next
		keepEdge := false
		for {
			afterIdx := edges[startIdx].asLine().next
			if afterIdx == newIdx {
				keepEdge = true
				break
			}
			if edges[afterIdx].asLine().x >= edges[newIdx].asLine().x {
				break
			}
			startIdx = afterIdx
		}

		if !keepEdge {
			removeEdge(newIdx, edges)
			insertEdgeAfter(newIdx, startIdx, edges)
		}

		startIdx = newIdx
		newIdx = nextIdx

		if edges[newIdx].asLine().firstY != currY {
			break
		}
	}
}
