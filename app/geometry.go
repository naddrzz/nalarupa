package main

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

// Pt is a 2D point [x, y] in document or screen coordinates.
type Pt [2]float64

// SnapGuide is a snap guide line on Axis (0 = vertical at x=Pos, 1 = horizontal at y=Pos) from From..To with X Marks.
type SnapGuide struct {
	Axis  int
	Pos   float64
	From  float64
	To    float64
	Marks []float64
}

// MeasureLine is an Alt-hover distance measurement line (X1,Y1)->(X2,Y2) of length Len with optional dashed Ext.
type MeasureLine struct {
	X1     float64
	Y1     float64
	X2     float64
	Y2     float64
	Len    float64
	HasExt bool
	Ext    [4]float64
}

// Camera holds the canvas viewport's top-left document coordinate (X, Y) and Zoom factor.
type Camera struct {
	X    float64
	Y    float64
	Zoom float64
}

// ApplyMatrix transforms (x, y) through m (identity if m == nil).
func ApplyMatrix(m *Matrix6, x, y float64) Pt {
	if m == nil {
		return Pt{x, y}
	}
	return Pt{m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]}
}

// UnapplyMatrix inverts ApplyMatrix; when linearOnly is true, translation (m[4], m[5]) is omitted.
func UnapplyMatrix(m *Matrix6, x, y float64, linearOnly bool) Pt {
	if m == nil {
		return Pt{x, y}
	}
	det := m[0]*m[3] - m[1]*m[2]
	if math.Abs(det) < 1e-12 {
		return Pt{x, y}
	}
	u, v := x, y
	if !linearOnly {
		u -= m[4]
		v -= m[5]
	}
	return Pt{(m[3]*u - m[2]*v) / det, (m[0]*v - m[1]*u) / det}
}

// InParent converts a canvas-space delta into local x/y deltas for a child inside parent painted under m.
func InParent(m *Matrix6, dx, dy float64) Pt {
	p := UnapplyMatrix(m, dx, dy, true)
	return Pt{math.Round(p[0]*1e6) / 1e6, math.Round(p[1]*1e6) / 1e6}
}

// LinMatrix applies only the linear 2x2 part of m.
func LinMatrix(m *Matrix6, x, y float64) Pt {
	if m == nil {
		return Pt{x, y}
	}
	lin := Matrix6{m[0], m[1], m[2], m[3], 0, 0}
	return ApplyMatrix(&lin, x, y)
}

// Turned reports whether m includes non-zero rotation/skew (flips alone stay axis-aligned).
func Turned(m *Matrix6) bool {
	return m != nil && (math.Abs(m[1]) > 1e-9 || math.Abs(m[2]) > 1e-9)
}

// OwnMatrix builds a node's own rotation (CCW degrees) and flip matrix around its origin.
func OwnMatrix(deg float64, flipX, flipY bool) Matrix6 {
	t := (-deg * math.Pi) / 180.0
	sx, sy := 1.0, 1.0
	if flipX {
		sx = -1.0
	}
	if flipY {
		sy = -1.0
	}
	c, s := math.Cos(t), math.Sin(t)
	return Matrix6{c * sx, s * sx, -s * sy, c * sy, 0, 0}
}

// CenterOffset returns the offset from a node's origin to its visual center in parent space.
func CenterOffset(w, h, deg float64, flipX, flipY bool, bx, by float64) Pt {
	m := OwnMatrix(deg, flipX, flipY)
	return LinMatrix(&m, bx+w/2, by+h/2)
}

// TurnAroundCenter returns the new (x, y) origin keeping a node's center stationary when rotating from -> to degrees.
func TurnAroundCenter(x, y, w, h, fromDeg, toDeg float64, flipX, flipY bool, bx, by float64) Pt {
	o0 := CenterOffset(w, h, fromDeg, flipX, flipY, bx, by)
	o1 := CenterOffset(w, h, toDeg, flipX, flipY, bx, by)
	return Pt{
		math.Round((x+o0[0]-o1[0])*100) / 100,
		math.Round((y+o0[1]-o1[1])*100) / 100,
	}
}

// RectCorners returns the 4 corners (tl, tr, br, bl) of r transformed by m.
func RectCorners(r RectD, m *Matrix6) [4]Pt {
	return [4]Pt{
		ApplyMatrix(m, r.X, r.Y),
		ApplyMatrix(m, r.X+r.W, r.Y),
		ApplyMatrix(m, r.X+r.W, r.Y+r.H),
		ApplyMatrix(m, r.X, r.Y+r.H),
	}
}

// PointsBounds returns the axis-aligned bounding RectD of pts.
func PointsBounds(pts []Pt) RectD {
	if len(pts) == 0 {
		return RectD{}
	}
	minX, maxX := pts[0][0], pts[0][0]
	minY, maxY := pts[0][1], pts[0][1]
	for _, p := range pts[1:] {
		minX = min(minX, p[0])
		maxX = max(maxX, p[0])
		minY = min(minY, p[1])
		maxY = max(maxY, p[1])
	}
	return RectD{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
}

// Painted returns where box b is painted in canvas space (bounding box of its transformed corners).
func Painted(b Box) RectD {
	r := b.RectOf()
	if b.M == nil {
		return r
	}
	cs := RectCorners(r, b.M)
	return PointsBounds(cs[:])
}

// PointIn returns the painted canvas point at fractions (u, v) of box b.
func PointIn(b Box, u, v float64) Pt {
	return ApplyMatrix(b.M, b.X+b.W*u, b.Y+b.H*v)
}

// UnionRects returns the bounding union of rs (or nil if empty).
func UnionRects(rs []RectD) *RectD {
	if len(rs) == 0 {
		return nil
	}
	minX, minY := rs[0].X, rs[0].Y
	maxX, maxY := rs[0].X+rs[0].W, rs[0].Y+rs[0].H
	for _, r := range rs[1:] {
		minX = min(minX, r.X)
		minY = min(minY, r.Y)
		maxX = max(maxX, r.X+r.W)
		maxY = max(maxY, r.Y+r.H)
	}
	out := RectD{X: minX, Y: minY, W: maxX - minX, H: maxY - minY}
	return &out
}

// ShimmerHalves computes two contiguous whole-pixel [left, width] pairs for pending-image shimmers.
func ShimmerHalves(pos, band float64) ([2]float64, [2]float64) {
	a := math.Round(pos)
	mid := math.Round(pos + band/2)
	b := math.Round(pos + band)
	return [2]float64{a, mid - a}, [2]float64{mid, b - mid}
}

// FlipRadii mirrors or swaps [tl, tr, br, bl] corner radii under a flip or half-turn matrix m.
func FlipRadii(r [4]float64, m *Matrix6) [4]float64 {
	a, b, c, d := r[0], r[1], r[2], r[3]
	fx := m != nil && m[0] < 0
	fy := m != nil && m[3] < 0
	tl, tr, br, bl := a, b, c, d
	if fx {
		tl, tr, br, bl = b, a, d, c
	}
	if fy {
		tl, tr, br, bl = bl, br, tr, tl
	}
	return [4]float64{tl, tr, br, bl}
}

// OutlineSVGPath returns an SVG path string for a rounded box or inscribed ellipse.
func OutlineSVGPath(x, y, w, h float64, radii *[4]float64, isEllipse bool) string {
	if isEllipse {
		return fmt.Sprintf("M%g %gA%g %g 0 0 1 %g %gA%g %g 0 0 1 %g %gZ",
			x, y+h/2, w/2, h/2, x+w, y+h/2, w/2, h/2, x, y+h/2)
	}
	var a, b, c, d float64
	if radii != nil {
		a, b, c, d = radii[0], radii[1], radii[2], radii[3]
	}
	return fmt.Sprintf("M%g %gH%gA%g %g 0 0 1 %g %gV%gA%g %g 0 0 1 %g %gH%gA%g %g 0 0 1 %g %gV%gA%g %g 0 0 1 %g %gZ",
		x+a, y, x+w-b, b, b, x+w, y+b, y+h-c, c, c, x+w-c, y+h, x+d, d, d, x, y+h-d, y+a, a, a, x+a, y)
}

// DragPoints returns pixel-rounded corners and center of r for snap queries.
func DragPoints(r RectD) []Pt {
	return []Pt{
		{math.Round(r.X), math.Round(r.Y)},
		{math.Round(r.X + r.W), math.Round(r.Y)},
		{math.Round(r.X), math.Round(r.Y + r.H)},
		{math.Round(r.X + r.W), math.Round(r.Y + r.H)},
		{math.Round(r.X + r.W/2), math.Round(r.Y + r.H/2)},
	}
}

// AnchorPoints returns pixel-rounded corners and edge midpoints of r for snap targets.
func AnchorPoints(r RectD) []Pt {
	x0, y0 := math.Round(r.X), math.Round(r.Y)
	x1, y1 := math.Round(r.X+r.W), math.Round(r.Y+r.H)
	cx, cy := math.Round(r.X+r.W/2), math.Round(r.Y+r.H/2)
	return []Pt{
		{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1},
		{cx, y0}, {cx, y1}, {x0, cy}, {x1, cy},
	}
}

// Snap computes the smallest (< threshold) per-axis offset aligning pts onto anchors and returns guides.
func Snap(pts, anchors []Pt, threshold float64, axes [2]bool) (Pt, []SnapGuide) {
	best := [2]float64{math.Inf(1), math.Inf(1)}
	var hits [2][][2]Pt
	for _, s := range pts {
		for _, o := range anchors {
			for a := 0; a < 2; a++ {
				if !axes[a] {
					continue
				}
				d := o[a] - s[a]
				if math.Abs(d) < threshold && math.Abs(d) <= math.Abs(best[a]) {
					if d != best[a] {
						hits[a] = nil
					}
					hits[a] = append(hits[a], [2]Pt{s, o})
					best[a] = d
				}
			}
		}
	}
	var offset Pt
	for a := 0; a < 2; a++ {
		if !math.IsInf(best[a], 0) {
			offset[a] = best[a]
		}
	}
	var guides []SnapGuide
	for a := 0; a < 2; a++ {
		if len(hits[a]) == 0 {
			continue
		}
		b := 1 - a
		// Group hits by distinct anchor coordinate so simultaneous multi-edge alignments each get a guide line.
		var posOrder []float64
		byPos := map[float64][][2]Pt{}
		for _, pair := range hits[a] {
			pos := pair[1][a]
			if _, seen := byPos[pos]; !seen {
				posOrder = append(posOrder, pos)
			}
			byPos[pos] = append(byPos[pos], pair)
		}
		for _, pos := range posOrder {
			minS, maxS := math.Inf(1), math.Inf(-1)
			minO, maxO := math.Inf(1), math.Inf(-1)
			for _, pair := range byPos[pos] {
				sv := pair[0][b] + offset[b]
				ov := pair[1][b]
				minS = min(minS, sv)
				maxS = max(maxS, sv)
				minO = min(minO, ov)
				maxO = max(maxO, ov)
			}
			from := min(minS, minO)
			to := max(maxS, maxO)
			var marks []float64
			for _, v := range []float64{minS, maxS, minO, maxO} {
				dup := false
				for _, ex := range marks {
					if ex == v {
						dup = true
						break
					}
				}
				if !dup {
					marks = append(marks, v)
				}
			}
			guides = append(guides, SnapGuide{
				Axis:  a,
				Pos:   pos,
				From:  from,
				To:    to,
				Marks: marks,
			})
		}
	}
	return offset, guides
}

// Measure computes X and Y distance measurement lines from selection s to hovered target t.
func Measure(s, t RectD) []MeasureLine {
	var out []MeasureLine
	lo := func(r RectD, a int) float64 {
		if a == 0 {
			return r.X
		}
		return r.Y
	}
	hi := func(r RectD, a int) float64 {
		if a == 0 {
			return r.X + r.W
		}
		return r.Y + r.H
	}
	for a := 0; a < 2; a++ {
		c := 1 - a
		overlapCross := hi(s, c) > lo(t, c) && hi(t, c) > lo(s, c)
		var spans [][2]float64
		switch {
		case lo(t, a) >= hi(s, a):
			spans = [][2]float64{{hi(s, a), lo(t, a)}}
		case hi(t, a) <= lo(s, a):
			spans = [][2]float64{{hi(t, a), lo(s, a)}}
		case overlapCross:
			spans = [][2]float64{{lo(s, a), lo(t, a)}, {hi(s, a), hi(t, a)}}
		}
		at := (lo(s, c) + hi(s, c)) / 2
		for _, sp := range spans {
			p, q := sp[0], sp[1]
			length := math.Abs(q - p)
			if length < 0.005 {
				continue
			}
			var m MeasureLine
			if a == 0 {
				m = MeasureLine{X1: p, Y1: at, X2: q, Y2: at, Len: length}
			} else {
				m = MeasureLine{X1: at, Y1: p, X2: at, Y2: q, Len: length}
			}
			if at < lo(t, c) || at > hi(t, c) {
				edge := hi(t, c)
				if at < lo(t, c) {
					edge = lo(t, c)
				}
				end := p
				if math.Abs(q-lo(t, a)) < 1e-9 || math.Abs(q-hi(t, a)) < 1e-9 {
					end = q
				}
				m.HasExt = true
				if a == 0 {
					m.Ext = [4]float64{end, at, end, edge}
				} else {
					m.Ext = [4]float64{at, end, edge, end}
				}
			}
			out = append(out, m)
		}
	}
	return out
}

// ResizeBox computes resized (and potentially negative/flipped) RectD for handle ("nw","n","ne","e","se","s","sw","w").
func ResizeBox(b RectD, handle string, dx, dy float64, shift, alt bool) RectD {
	k := 1.0
	if alt {
		k = 2.0
	}
	hasE := handle == "e" || handle == "ne" || handle == "se"
	hasW := handle == "w" || handle == "nw" || handle == "sw"
	hasS := handle == "s" || handle == "se" || handle == "sw"
	hasN := handle == "n" || handle == "ne" || handle == "nw"

	w := b.W
	if hasE {
		w += dx * k
	} else if hasW {
		w -= dx * k
	}
	h := b.H
	if hasS {
		h += dy * k
	} else if hasN {
		h -= dy * k
	}

	horiz := handle == "e" || handle == "w"
	vert := handle == "n" || handle == "s"
	if shift && b.W > 0 && b.H > 0 {
		aspect := b.W / b.H
		if vert || (!horiz && math.Abs(h/b.H) > math.Abs(w/b.W)) {
			sign := 1.0
			if !vert && w < 0 {
				sign = -1.0
			}
			w = sign * math.Abs(h*aspect)
		} else {
			sign := 1.0
			if !horiz && h < 0 {
				sign = -1.0
			}
			h = sign * math.Abs(w/aspect)
		}
	}
	if w < 0 {
		w = -max(1.0, math.Abs(w))
	} else {
		w = max(1.0, math.Abs(w))
	}
	if h < 0 {
		h = -max(1.0, math.Abs(h))
	} else {
		h = max(1.0, math.Abs(h))
	}
	cx, cy := b.X+b.W/2, b.Y+b.H/2
	var x, y float64
	if alt || (shift && vert) {
		x = cx - w/2
	} else if hasW {
		x = b.X + b.W - w
	} else {
		x = b.X
	}
	if alt || (shift && horiz) {
		y = cy - h/2
	} else if hasN {
		y = b.Y + b.H - h
	} else {
		y = b.Y
	}
	return RectD{X: x, Y: y, W: w, H: h}
}

// ResizeCursorName returns the CSS-style resize cursor name for handle under m.
func ResizeCursorName(handle string, m *Matrix6) string {
	var ox, oy float64
	if handle == "e" || handle == "ne" || handle == "se" {
		ox = 1
	} else if handle == "w" || handle == "nw" || handle == "sw" {
		ox = -1
	}
	if handle == "s" || handle == "se" || handle == "sw" {
		oy = 1
	} else if handle == "n" || handle == "ne" || handle == "nw" {
		oy = -1
	}
	v := LinMatrix(m, ox, oy)
	a := math.Mod(math.Mod(math.Atan2(v[1], v[0])*180/math.Pi, 180)+180, 180)
	switch {
	case a < 22.5 || a >= 157.5:
		return "ew-resize"
	case a < 67.5:
		return "nwse-resize"
	case a < 112.5:
		return "ns-resize"
	default:
		return "nesw-resize"
	}
}

// ResizeUICursor maps ResizeCursorName to ui.Cursor.
func ResizeUICursor(handle string, m *Matrix6) ui.Cursor {
	switch ResizeCursorName(handle, m) {
	case "ew-resize":
		return ui.CursorResizeEW
	case "nwse-resize":
		return ui.CursorResizeNWSE
	case "ns-resize":
		return ui.CursorResizeNS
	default:
		return ui.CursorResizeNESW
	}
}

// FitCamera computes a Camera fitting box r inside (viewW x viewH) with pad and maxZoom.
func FitCamera(r RectD, viewW, viewH, pad, maxZoom float64) Camera {
	if r.W <= 0 || r.H <= 0 || viewW <= 0 || viewH <= 0 {
		return Camera{X: -100, Y: -100, Zoom: 0.5}
	}
	availW := max(40.0, viewW-pad*2)
	availH := max(40.0, viewH-pad*2)
	z := min(availW/r.W, availH/r.H)
	if maxZoom > 0 {
		z = min(z, maxZoom)
	}
	z = max(0.02, min(64.0, z))
	cx := r.X + r.W/2
	cy := r.Y + r.H/2
	return Camera{
		X:    cx - (viewW/2)/z,
		Y:    cy - (viewH/2)/z,
		Zoom: z,
	}
}

// SlideCam computes the camera fitting slide (accounting for rotation/flips) into (viewW x viewH) for Present mode.
func SlideCam(slide Box, viewW, viewH float64) Camera {
	return FitCamera(Painted(slide), viewW, viewH, 40, 8)
}
