package render

import "math"

// FindEmptySpace is render.rs `find_empty_space`: a free spot of w×h next to anchor (or, when nil, the
// whole document), searched in steps of 40 toward dir ("left", "top", "bottom", else right), keeping pad
// clear of every top-level box. It returns the spot rounded, or (0, 0) on an empty canvas.
func FindEmptySpace(r *Renderer, w, h float64, dir string, pad float64, anchor *Flat) (x, y float64) {
	var boxes []Rect
	for _, id := range r.Order {
		if l, ok := r.Layers[id]; ok && len(l.Flat) > 0 {
			f := &l.Flat[0]
			boxes = append(boxes, Rect{X: f.X, Y: f.Y, W: f.W, H: f.H})
		}
	}
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
	default:
		if b, ok := r.Bounds(); ok {
			a = Rect{X: b.X + 64, Y: b.Y + 64, W: b.W - 128, H: b.H - 128}
		}
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
		return 0, 0
	}
	for range 500 {
		if !busy(x, y) {
			break
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
	return math.Round(x), math.Round(y)
}
