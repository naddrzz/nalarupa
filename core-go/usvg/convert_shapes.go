// Port of usvg 0.47 src/parser/shapes.rs: basic shapes and `path` data to paths.

package usvg

import (
	"math"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// convertShape is shapes::convert.
func convertShape(node svgNode, state *converterState) (*skia.Path, bool) {
	tag, ok := node.TagName()
	if !ok {
		return nil, false
	}
	switch tag {
	case eidRect:
		return convertRect(node, state)
	case eidCircle:
		return convertCircle(node, state)
	case eidEllipse:
		return convertEllipse(node, state)
	case eidLine:
		return convertLine(node, state)
	case eidPolyline:
		return convertPolyline(node)
	case eidPolygon:
		return convertPolygon(node)
	case eidPath:
		return convertPathData(node)
	}
	return nil, false
}

// finish is PathBuilder::finish as an Option.
func finish(b *skia.PathBuilder) (*skia.Path, bool) {
	p := b.Finish()
	return p, p != nil
}

// convertPathData is shapes::convert_path: the `d` attribute, up to its first error.
func convertPathData(node svgNode) (*skia.Path, bool) {
	value, ok := node.attrStr(aidD)
	if !ok {
		return nil, false
	}
	b := skia.NewPathBuilder()
	p := stNewSimplifyingPathParser(value)
	for {
		seg, ok, err := p.Next()
		if !ok || err != nil {
			break
		}
		switch seg.Kind {
		case stSimpleMoveTo:
			b.MoveTo(float32(seg.X), float32(seg.Y))
		case stSimpleLineTo:
			b.LineTo(float32(seg.X), float32(seg.Y))
		case stSimpleQuadratic:
			b.QuadTo(float32(seg.X1), float32(seg.Y1), float32(seg.X), float32(seg.Y))
		case stSimpleCurveTo:
			b.CubicTo(float32(seg.X1), float32(seg.Y1), float32(seg.X2), float32(seg.Y2),
				float32(seg.X), float32(seg.Y))
		case stSimpleClosePath:
			b.Close()
		}
	}
	return finish(b)
}

func convertRect(node svgNode, state *converterState) (*skia.Path, bool) {
	// `width` and `height` must be positive and non-zero.
	width := node.convertUserLength(aidWidth, state, stLengthZero())
	height := node.convertUserLength(aidHeight, state, stLengthZero())
	if !isValidLengthF32(width) || !isValidLengthF32(height) {
		return nil, false
	}

	x := node.convertUserLength(aidX, state, stLengthZero())
	y := node.convertUserLength(aidY, state, stLengthZero())

	rx, ry := resolveRxRy(node, state)

	// Clamp rx/ry to half the width/height, only after resolving.
	if rx > width/2 {
		rx = width / 2
	}
	if ry > height/2 {
		ry = height / 2
	}

	// As https://www.w3.org/TR/SVG11/shapes.html#RectElement converts it.
	if approxEqUlpsF32(rx, 0, 4) {
		r, ok := skia.RectFromXYWH(x, y, width, height)
		if !ok {
			return nil, false
		}
		return skia.PathFromRect(r), true
	}

	b := skia.NewPathBuilder()
	b.MoveTo(x+rx, y)

	b.LineTo(x+width-rx, y)
	arcTo(b, rx, ry, 0, false, true, x+width, y+ry)

	b.LineTo(x+width, y+height-ry)
	arcTo(b, rx, ry, 0, false, true, x+width-rx, y+height)

	b.LineTo(x+rx, y+height)
	arcTo(b, rx, ry, 0, false, true, x, y+height-ry)

	b.LineTo(x, y+ry)
	arcTo(b, rx, ry, 0, false, true, x+rx, y)

	b.Close()
	return finish(b)
}

// resolveRxRy is resolve_rx_ry: negative radii are dropped first, and a missing one takes the other.
func resolveRxRy(node svgNode, state *converterState) (float32, float32) {
	rx, hasRx := node.attrLength(aidRx)
	ry, hasRy := node.attrLength(aidRy)

	// Remove negative values first.
	if hasRx && math.Signbit(rx.Number) {
		hasRx = false
	}
	if hasRy && math.Signbit(ry.Number) {
		hasRy = false
	}

	switch {
	case hasRx && hasRy:
		return convertUserLength(rx, node, aidRx, state), convertUserLength(ry, node, aidRy, state)
	case hasRx:
		v := convertUserLength(rx, node, aidRx, state)
		return v, v
	case hasRy:
		v := convertUserLength(ry, node, aidRy, state)
		return v, v
	}
	return 0, 0
}

func convertLine(node svgNode, state *converterState) (*skia.Path, bool) {
	x1 := node.convertUserLength(aidX1, state, stLengthZero())
	y1 := node.convertUserLength(aidY1, state, stLengthZero())
	x2 := node.convertUserLength(aidX2, state, stLengthZero())
	y2 := node.convertUserLength(aidY2, state, stLengthZero())

	b := skia.NewPathBuilder()
	b.MoveTo(x1, y1)
	b.LineTo(x2, y2)
	return finish(b)
}

func convertPolyline(node svgNode) (*skia.Path, bool) {
	b, ok := pointsToPath(node)
	if !ok {
		return nil, false
	}
	return finish(b)
}

func convertPolygon(node svgNode) (*skia.Path, bool) {
	b, ok := pointsToPath(node)
	if !ok {
		return nil, false
	}
	b.Close()
	return finish(b)
}

// pointsToPath is points_to_path: `polyline` and `polygon` need at least 2 points.
func pointsToPath(node svgNode) (*skia.PathBuilder, bool) {
	text, ok := node.attrStr(aidPoints)
	if !ok {
		return nil, false
	}
	b := skia.NewPathBuilder()
	p := stNewPointsParser(text)
	for {
		x, y, ok := p.Next()
		if !ok {
			break
		}
		if b.IsEmpty() {
			b.MoveTo(float32(x), float32(y))
		} else {
			b.LineTo(float32(x), float32(y))
		}
	}
	if b.Len() < 2 {
		return nil, false
	}
	return b, true
}

func convertCircle(node svgNode, state *converterState) (*skia.Path, bool) {
	cx := node.convertUserLength(aidCx, state, stLengthZero())
	cy := node.convertUserLength(aidCy, state, stLengthZero())
	r := node.convertUserLength(aidR, state, stLengthZero())
	if !isValidLengthF32(r) {
		return nil, false
	}
	return ellipseToPath(cx, cy, r, r)
}

func convertEllipse(node svgNode, state *converterState) (*skia.Path, bool) {
	cx := node.convertUserLength(aidCx, state, stLengthZero())
	cy := node.convertUserLength(aidCy, state, stLengthZero())
	rx, ry := resolveRxRy(node, state)
	if !isValidLengthF32(rx) || !isValidLengthF32(ry) {
		return nil, false
	}
	return ellipseToPath(cx, cy, rx, ry)
}

func ellipseToPath(cx, cy, rx, ry float32) (*skia.Path, bool) {
	b := skia.NewPathBuilder()
	b.MoveTo(cx+rx, cy)
	arcTo(b, rx, ry, 0, false, true, cx, cy+ry)
	arcTo(b, rx, ry, 0, false, true, cx-rx, cy)
	arcTo(b, rx, ry, 0, false, true, cx, cy-ry)
	arcTo(b, rx, ry, 0, false, true, cx+rx, cy)
	b.Close()
	return finish(b)
}

// arcTo is shapes.rs's PathBuilderExt::arc_to: an SVG arc from the last point, as kurbo cubics with a
// 0.1 tolerance, or a line when the arc degenerates.
func arcTo(b *skia.PathBuilder, rx, ry, xAxisRotation float32, largeArc, sweep bool, x, y float32) {
	prev, ok := b.LastPoint()
	if !ok {
		return
	}
	svgArc := kurboSvgArc{
		From:      kurboPoint{float64(prev.X), float64(prev.Y)},
		To:        kurboPoint{float64(x), float64(y)},
		Radii:     kurboVec2{float64(rx), float64(ry)},
		XRotation: stToRadians(float64(xAxisRotation)),
		LargeArc:  largeArc,
		Sweep:     sweep,
	}
	arc, ok := kurboArcFromSvgArc(&svgArc)
	if !ok {
		b.LineTo(x, y)
		return
	}
	arc.ToCubicBeziers(0.1, func(p1, p2, p kurboPoint) {
		b.CubicTo(float32(p1.X), float32(p1.Y), float32(p2.X), float32(p2.Y), float32(p.X), float32(p.Y))
	})
}
