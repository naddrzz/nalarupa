package render

import (
	"math"
	"math/bits"
	"strconv"

	"github.com/naddrzz/nalarupa/core-go/svg"
)

// Rect is a doc-space (x, y, w, h).
type Rect = svg.Rect

// fmax is f64::max: a NaN operand gives the other one.
func fmax(a, b float64) float64 {
	if a != a {
		return b
	}
	if b != b {
		return a
	}
	return math.Max(a, b)
}

// fmin is f64::min: a NaN operand gives the other one.
func fmin(a, b float64) float64 {
	if a != a {
		return b
	}
	if b != b {
		return a
	}
	return math.Min(a, b)
}

// i32 is `f as i32`: saturating, NaN gives 0.
func i32(f float64) int32 {
	switch {
	case f != f:
		return 0
	case f >= math.MaxInt32:
		return math.MaxInt32
	case f <= math.MinInt32:
		return math.MinInt32
	}
	return int32(f)
}

// fxhash is svg.rs `fxhash`: FNV-1a 64 of the string's bytes.
func fxhash(s string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * 0x100000001b3
	}
	return h
}

func rotl(x uint64, k int) uint64 { return bits.RotateLeft64(x, k) }

// ftoa is a float as text for signatures: only equality matters, as with Rust's Display.
func ftoa(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func union(a, b Rect) Rect {
	x0, y0 := fmin(a.X, b.X), fmin(a.Y, b.Y)
	return Rect{X: x0, Y: y0, W: fmax(a.X+a.W, b.X+b.W) - x0, H: fmax(a.Y+a.H, b.Y+b.H) - y0}
}

func overlaps(a, b Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
}

// compose is `p ∘ m` (m applies first); a nil p is the identity.
func compose(p *[6]float64, m [6]float64) [6]float64 {
	if p == nil {
		return m
	}
	return [6]float64{
		float64(p[0]*m[0]) + float64(p[2]*m[1]), float64(p[1]*m[0]) + float64(p[3]*m[1]),
		float64(p[0]*m[2]) + float64(p[2]*m[3]), float64(p[1]*m[2]) + float64(p[3]*m[3]),
		float64(p[0]*m[4]) + float64(p[2]*m[5]) + p[4], float64(p[1]*m[4]) + float64(p[3]*m[5]) + p[5],
	}
}

// canvasBox is the box on the canvas (the bounding box of the painted corners).
func canvasBox(f *Flat) Rect {
	m := [6]float64{1, 0, 0, 1, 0, 0}
	if f.M != nil {
		m = *f.M
	}
	pts := [4][2]float64{{f.X, f.Y}, {f.X + f.W, f.Y}, {f.X, f.Y + f.H}, {f.X + f.W, f.Y + f.H}}
	lx, ly, hx, hy := math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64
	for _, p := range pts {
		x := float64(m[0]*p[0]) + float64(m[2]*p[1]) + m[4]
		y := float64(m[1]*p[0]) + float64(m[3]*p[1]) + m[5]
		lx, ly, hx, hy = fmin(lx, x), fmin(ly, y), fmax(hx, x), fmax(hy, y)
	}
	return Rect{X: lx, Y: ly, W: hx - lx, H: hy - ly}
}
