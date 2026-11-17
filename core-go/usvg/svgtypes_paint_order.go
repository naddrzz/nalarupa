// Port of svgtypes 0.16.1 src/paint_order.rs

package usvg

// stPaintOrderKind is PaintOrderKind.
type stPaintOrderKind uint8

const (
	stPaintOrderFill stPaintOrderKind = iota
	stPaintOrderStroke
	stPaintOrderMarkers
)

// stPaintOrder is PaintOrder, the paint-order property; Order has no duplicates.
type stPaintOrder struct {
	Order [3]stPaintOrderKind
}

// stDefaultPaintOrder is PaintOrder::default(): fill, stroke, markers.
func stDefaultPaintOrder() stPaintOrder {
	return stPaintOrder{[3]stPaintOrderKind{stPaintOrderFill, stPaintOrderStroke, stPaintOrderMarkers}}
}

// stParsePaintOrder is PaintOrder::from_str, which never fails: invalid input gives the default.
func stParsePaintOrder(text string) stPaintOrder {
	order := make([]stPaintOrderKind, 0, 3)
	left := []stPaintOrderKind{stPaintOrderFill, stPaintOrderStroke, stPaintOrderMarkers}

	s := stNewStream(text)
	for !s.atEnd() && len(order) < 3 {
		s.skipSpaces()
		name := s.consumeASCIIIdent()
		s.skipSpaces()
		var kind stPaintOrderKind
		switch name {
		case "fill":
			kind = stPaintOrderFill
		case "stroke":
			kind = stPaintOrderStroke
		case "markers":
			kind = stPaintOrderMarkers
		default: // including `normal`, which short-circuits
			return stDefaultPaintOrder()
		}
		for i, v := range left {
			if v == kind {
				left = append(left[:i], left[i+1:]...)
				break
			}
		}
		order = append(order, kind)
	}

	s.skipSpaces()
	if !s.atEnd() {
		// Any trailing data is an error.
		return stDefaultPaintOrder()
	}
	if len(order) == 0 {
		return stDefaultPaintOrder()
	}
	// Any missing values are added in the original order.
	for len(order) < 3 && len(left) > 0 {
		order = append(order, left[0])
		left = left[1:]
	}
	// Any duplicates are an error.
	if order[0] == order[1] || order[0] == order[2] || order[1] == order[2] {
		return stDefaultPaintOrder()
	}
	return stPaintOrder{[3]stPaintOrderKind{order[0], order[1], order[2]}}
}
