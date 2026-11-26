package render

import "math"

// FindEmptySpace is render.rs `find_empty_space`: a free spot of w×h next to anchor (or, when nil, the
// whole document), searched in steps of 40 toward dir ("left", "top", "bottom", else right), keeping pad
// clear of every top-level box. It returns the spot rounded, or (0, 0) on an empty canvas.
func FindEmptySpace(r *Renderer, w, h float64, dir string, pad float64, anchor *Flat) (x, y float64) {
	if x, y, ok := FindEmptySpaceOK(r, w, h, dir, pad, anchor); ok {
		return x, y
	}
	boxes := occupiedTopBoxes(r)
	if len(boxes) == 0 {
		return 0, 0
	}
	minX, minY := boxes[0].X, boxes[0].Y
	maxX, maxY := minX+boxes[0].W, minY+boxes[0].H
	for _, b := range boxes[1:] {
		minX, minY = fmin(minX, b.X), fmin(minY, b.Y)
		maxX, maxY = fmax(maxX, b.X+b.W), fmax(maxY, b.Y+b.H)
	}
	baseX, baseY := minX, minY
	if anchor != nil {
		baseX, baseY = anchor.X, anchor.Y
	}
	switch dir {
	case "left":
		return math.Floor(minX - pad - w), math.Round(baseY)
	case "top":
		return math.Round(baseX), math.Floor(minY - pad - h)
	case "bottom":
		return math.Round(baseX), math.Ceil(maxY + pad)
	default:
		return math.Ceil(maxX + pad), math.Round(baseY)
	}
}

func occupiedTopBoxes(r *Renderer) []Rect {
	var boxes []Rect
	for _, id := range r.Order {
		l, ok := r.Layers[id]
		if !ok || len(l.Flat) == 0 {
			continue
		}
		f := &l.Flat[0]
		x0, y0, x1, y1 := f.X, f.Y, f.X+f.W, f.Y+f.H
		if !f.Clip && len(l.Flat) > 1 {
			clipDepth := -1
			for j := 1; j < len(l.Flat); j++ {
				d := &l.Flat[j]
				if clipDepth >= 0 && d.Depth > clipDepth {
					continue
				}
				clipDepth = -1
				if d.Faded {
					continue
				}
				x0, y0 = fmin(x0, d.X), fmin(y0, d.Y)
				x1, y1 = fmax(x1, d.X+d.W), fmax(y1, d.Y+d.H)
				if d.Clip {
					clipDepth = d.Depth
				}
			}
		}
		boxes = append(boxes, Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0})
	}
	return boxes
}

// FindEmptySpaceOK is FindEmptySpace, ok false on an empty canvas or when the bounded 500-step search fails.
func FindEmptySpaceOK(r *Renderer, w, h float64, dir string, pad float64, anchor *Flat) (x, y float64, ok bool) {
	boxes := occupiedTopBoxes(r)
	busy := func(x, y float64) bool {
		for _, b := range boxes {
			if x < b.X+b.W+pad && x+w+pad > b.X && y < b.Y+b.H+pad && y+h+pad > b.Y {
				return true
			}
		}
		return false
	}
	var a Rect
	switch {
	case anchor != nil:
		a = Rect{X: anchor.X, Y: anchor.Y, W: anchor.W, H: anchor.H}
	case len(boxes) > 0:
		x0, y0 := boxes[0].X, boxes[0].Y
		x1, y1 := x0+boxes[0].W, y0+boxes[0].H
		for _, b := range boxes[1:] {
			x0, y0 = fmin(x0, b.X), fmin(y0, b.Y)
			x1, y1 = fmax(x1, b.X+b.W), fmax(y1, b.Y+b.H)
		}
		a = Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	}
	switch dir {
	case "left":
		x, y = a.X-pad-w, a.Y
	case "top":
		x, y = a.X, a.Y-pad-h
	case "bottom":
		x, y = a.X, a.Y+a.H+pad
	default:
		x, y = a.X+a.W+pad, a.Y
	}
	if len(boxes) == 0 {
		return 0, 0, false
	}
	outwardRound := func(cx, cy float64) (float64, float64) {
		switch dir {
		case "left":
			return math.Floor(cx), math.Round(cy)
		case "top":
			return math.Round(cx), math.Floor(cy)
		case "bottom":
			return math.Round(cx), math.Ceil(cy)
		default:
			return math.Ceil(cx), math.Round(cy)
		}
	}
	for range 500 {
		if !busy(x, y) {
			rx, ry := math.Round(x), math.Round(y)
			if !busy(rx, ry) {
				return rx, ry, true
			}
			if ox, oy := outwardRound(x, y); !busy(ox, oy) {
				return ox, oy, true
			}
		}
		switch dir {
		case "left":
			x -= 40
		case "top":
			y -= 40
		case "bottom":
			y += 40
		default:
			x += 40
		}
	}
	if !busy(x, y) {
		rx, ry := math.Round(x), math.Round(y)
		if !busy(rx, ry) {
			return rx, ry, true
		}
		if ox, oy := outwardRound(x, y); !busy(ox, oy) {
			return ox, oy, true
		}
	}
	return 0, 0, false
}
