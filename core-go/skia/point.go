package skia

import "math"

// Point is a point (lib.rs). It is not guaranteed to be finite.
type Point struct{ X, Y float32 }

// Pt creates a point.
func Pt(x, y float32) Point { return Point{x, y} }

func (p Point) Add(o Point) Point        { return Point{p.X + o.X, p.Y + o.Y} }
func (p Point) Sub(o Point) Point        { return Point{p.X - o.X, p.Y - o.Y} }
func (p Point) Mul(o Point) Point        { return Point{float32(p.X * o.X), float32(p.Y * o.Y)} }
func (p Point) Neg() Point               { return Point{-p.X, -p.Y} }
func (p Point) Scaled(s float32) Point   { return Point{float32(p.X * s), float32(p.Y * s)} }
func (p Point) IsZero() bool             { return p.X == 0 && p.Y == 0 }
func (p Point) Dot(o Point) float32      { return float32(p.X*o.X) + float32(p.Y*o.Y) }
func (p Point) Cross(o Point) float32    { return float32(p.X*o.Y) - float32(p.Y*o.X) }
func (p Point) Distance(o Point) float32 { return p.Sub(o).Length() }

// IsFinite reports whether both coordinates are finite, tested as Rust does on their product (so two
// huge coordinates whose product overflows count as not finite).
func (p Point) IsFinite() bool { return isFinite(float32(p.X * p.Y)) }

func (p Point) almostEqual(o Point) bool { return !p.Sub(o).canNormalize() }

func (p Point) equalsWithinTolerance(o Point, tolerance float32) bool {
	return isNearlyZeroWithinTolerance(p.X-o.X, tolerance) && isNearlyZeroWithinTolerance(p.Y-o.Y, tolerance)
}

func (p Point) canNormalize() bool { return isFinite(p.X) && isFinite(p.Y) && (p.X != 0 || p.Y != 0) }

// Normalize scales the vector to length one; a nearly zero vector becomes (0, 0) and gives false.
func (p *Point) Normalize() bool { return p.SetLengthFrom(p.X, p.Y, 1) }

// SetNormalize sets the vector to (x, y) scaled to length one, see Normalize.
func (p *Point) SetNormalize(x, y float32) bool { return p.SetLengthFrom(x, y, 1) }

// Length is the distance from the origin.
func (p Point) Length() float32 {
	mag2 := float32(p.X*p.X) + float32(p.Y*p.Y)
	if isFinite(mag2) {
		return sqrt32(mag2)
	}
	xx, yy := float64(p.X), float64(p.Y)
	return float32(math.Sqrt(float64(xx*xx) + float64(yy*yy)))
}

// SetLength scales the vector to the length, see Normalize.
func (p *Point) SetLength(length float32) bool { return p.SetLengthFrom(p.X, p.Y, length) }

// SetLengthFrom sets the vector to (x, y) scaled to the length, see Normalize.
func (p *Point) SetLengthFrom(x, y, length float32) bool {
	// set_point_length: always in doubles, so a huge x or y does not overflow.
	xx, yy := float64(x), float64(y)
	dmag := math.Sqrt(float64(xx*xx) + float64(yy*yy))
	dscale := float64(length) / dmag
	x = float32(x * float32(dscale))
	y = float32(y * float32(dscale))
	if !isFinite(x) || !isFinite(y) || (x == 0 && y == 0) {
		*p = Point{}
		return false
	}
	*p = Point{x, y}
	return true
}

func (p Point) distanceToSqd(o Point) float32 {
	dx, dy := p.X-o.X, p.Y-o.Y
	return float32(dx*dx) + float32(dy*dy)
}

func (p Point) lengthSqd() float32 { return p.Dot(p) }

func (p *Point) Scale(s float32) { p.X = float32(p.X * s); p.Y = float32(p.Y * s) }

func (p *Point) rotateCW() { p.X, p.Y = -p.Y, p.X }

func (p *Point) rotateCCW() { p.X, p.Y = p.Y, -p.X }
