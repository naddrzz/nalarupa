package skia

import "math"

// IntRect is an integer rectangle (rect.rs). Width and height are in 1..=i32::MAX, and x+width and
// y+height do not overflow.
type IntRect struct {
	x, y          int32
	width, height uint32
}

// IntRectFromXYWH creates an IntRect; ok is false where Rust gives None.
func IntRectFromXYWH(x, y int32, width, height uint32) (IntRect, bool) {
	if width > math.MaxInt32 || height > math.MaxInt32 || !fitsI32(int64(x)+int64(width)) || !fitsI32(int64(y)+int64(height)) {
		return IntRect{}, false
	}
	if width == 0 || height == 0 {
		return IntRect{}, false
	}
	return IntRect{x, y, width, height}, true
}

// IntRectFromLTRB creates an IntRect from its edges.
func IntRectFromLTRB(left, top, right, bottom int32) (IntRect, bool) {
	w, h := int64(right)-int64(left), int64(bottom)-int64(top)
	if !fitsI32(w) || !fitsI32(h) || w < 0 || h < 0 {
		return IntRect{}, false
	}
	return IntRectFromXYWH(left, top, uint32(w), uint32(h))
}

func (r IntRect) X() int32       { return r.x }
func (r IntRect) Y() int32       { return r.y }
func (r IntRect) Width() uint32  { return r.width }
func (r IntRect) Height() uint32 { return r.height }
func (r IntRect) Left() int32    { return r.x }
func (r IntRect) Top() int32     { return r.y }
func (r IntRect) Right() int32   { return r.x + int32(r.width) }
func (r IntRect) Bottom() int32  { return r.y + int32(r.height) }
func (r IntRect) Size() IntSize  { return IntSize{r.width, r.height} }

// Contains reports whether the rect completely includes other.
func (r IntRect) Contains(o IntRect) bool {
	return r.x <= o.x && r.y <= o.y && r.Right() >= o.Right() && r.Bottom() >= o.Bottom()
}

// Intersect returns the intersection of two rects.
func (r IntRect) Intersect(o IntRect) (IntRect, bool) {
	left, top := max(r.x, o.x), max(r.y, o.y)
	right, bottom := min(r.Right(), o.Right()), min(r.Bottom(), o.Bottom())
	return IntRectFromLTRB(left, top, right, bottom)
}

// Inset insets the rect.
func (r IntRect) Inset(dx, dy int32) (IntRect, bool) {
	return IntRectFromLTRB(r.Left()+dx, r.Top()+dy, r.Right()-dx, r.Bottom()-dy)
}

// MakeOutset outsets the rect, saturating at the i32 range.
func (r IntRect) MakeOutset(dx, dy int32) (IntRect, bool) {
	return IntRectFromLTRB(satI32(int64(r.Left())-int64(dx)), satI32(int64(r.Top())-int64(dy)),
		satI32(int64(r.Right())+int64(dx)), satI32(int64(r.Bottom())+int64(dy)))
}

// Translate moves the rect by an offset.
func (r IntRect) Translate(tx, ty int32) (IntRect, bool) {
	return IntRectFromXYWH(r.x+tx, r.y+ty, r.width, r.height)
}

// TranslateTo moves the rect to a position.
func (r IntRect) TranslateTo(x, y int32) (IntRect, bool) {
	return IntRectFromXYWH(x, y, r.width, r.height)
}

// ToRect converts into a Rect.
func (r IntRect) ToRect() Rect {
	v, _ := RectFromLTRB(float32(r.x), float32(r.y), float32(r.x)+float32(r.width), float32(r.y)+float32(r.height))
	return v
}

// Rect is a rectangle defined by its edges (rect.rs). All values are finite, left <= right, top <= bottom,
// and width and height are <= f32::MAX. It can have zero width or height.
type Rect struct{ left, top, right, bottom float32 }

// checkedF32Sub is checked_f32_sub: the difference must stay in the f32 range.
func checkedF32Sub(a, b float32) bool {
	n := float64(a) - float64(b)
	return n > -float64(math.MaxFloat32) && n < float64(math.MaxFloat32)
}

// RectFromLTRB creates a Rect; ok is false where Rust gives None.
func RectFromLTRB(left, top, right, bottom float32) (Rect, bool) {
	if !isFinite(left) || !isFinite(top) || !isFinite(right) || !isFinite(bottom) {
		return Rect{}, false
	}
	if left <= right && top <= bottom && checkedF32Sub(right, left) && checkedF32Sub(bottom, top) {
		return Rect{left, top, right, bottom}, true
	}
	return Rect{}, false
}

// RectFromXYWH creates a Rect from its position and size.
func RectFromXYWH(x, y, w, h float32) (Rect, bool) { return RectFromLTRB(x, y, w+x, h+y) }

func (r Rect) Left() float32   { return r.left }
func (r Rect) Top() float32    { return r.top }
func (r Rect) Right() float32  { return r.right }
func (r Rect) Bottom() float32 { return r.bottom }
func (r Rect) X() float32      { return r.left }
func (r Rect) Y() float32      { return r.top }
func (r Rect) Width() float32  { return r.right - r.left }
func (r Rect) Height() float32 { return r.bottom - r.top }
func (r Rect) IsEmpty() bool   { return r.left == r.right || r.top == r.bottom }

// Round converts into an IntRect by adding 0.5 and discarding the fraction; width and height are >= 1.
func (r Rect) Round() (IntRect, bool) {
	left, top := saturateRound(r.left), saturateRound(r.top)
	right, bottom := saturateRound(r.right), saturateRound(r.bottom)
	return IntRectFromXYWH(left, top, uint32(max(1, satI32(int64(right)-int64(left)))), uint32(max(1, satI32(int64(bottom)-int64(top)))))
}

// RoundOut converts into an IntRect rounding outwards; width and height are >= 1.
func (r Rect) RoundOut() (IntRect, bool) {
	left, top := saturateFloor(r.left), saturateFloor(r.top)
	right, bottom := saturateCeil(r.right), saturateCeil(r.bottom)
	return IntRectFromXYWH(left, top, uint32(max(1, satI32(int64(right)-int64(left)))), uint32(max(1, satI32(int64(bottom)-int64(top)))))
}

// Intersect returns the intersection of two rects.
func (r Rect) Intersect(o Rect) (Rect, bool) {
	return RectFromLTRB(maxf(r.X(), o.X()), maxf(r.Y(), o.Y()), minf(r.right, o.right), minf(r.bottom, o.bottom))
}

// Join returns the union of two rects; ok is false if the width or height would overflow.
func (r Rect) Join(o Rect) (Rect, bool) {
	if o.IsEmpty() {
		return r, true
	}
	if r.IsEmpty() {
		return o, true
	}
	return RectFromLTRB(minf(r.X(), o.X()), minf(r.Y(), o.Y()), maxf(r.right, o.right), maxf(r.bottom, o.bottom))
}

// RectFromPoints creates the bounds of points; ok is false for no points or a non-finite one.
func RectFromPoints(points []Point) (Rect, bool) {
	var offset int
	var mn, mx [4]float32
	switch n := len(points); {
	case n == 0:
		return Rect{}, false
	case n == 1:
		return RectFromXYWH(points[0].X, points[0].Y, 0, 0)
	case n == 2:
		x0, y0, x1, y1 := points[0].X, points[0].Y, points[1].X, points[1].Y
		// No min/max, so a NaN reaches RectFromLTRB and is rejected.
		l, r := x1, x0
		if x0 < x1 {
			l, r = x0, x1
		}
		t, b := y1, y0
		if y0 < y1 {
			t, b = y0, y1
		}
		return RectFromLTRB(l, t, r, b)
	case n&1 != 0:
		p := points[0]
		mn = [4]float32{p.X, p.Y, p.X, p.Y}
		offset = 1
	default:
		p0, p1 := points[0], points[1]
		mn = [4]float32{p0.X, p0.Y, p1.X, p1.Y}
		offset = 2
	}
	mx = mn
	var accum [4]float32
	for offset != len(points) {
		p0, p1 := points[offset], points[offset+1]
		xy := [4]float32{p0.X, p0.Y, p1.X, p1.Y}
		for i := range 4 {
			accum[i] = float32(accum[i] * xy[i])
			mn[i] = minf(mn[i], xy[i])
			mx[i] = maxf(mx[i], xy[i])
		}
		offset += 2
	}
	allFinite := true
	for i := range 4 {
		allFinite = allFinite && float32(accum[i]*0) == 0
	}
	if !allFinite {
		return Rect{}, false
	}
	return RectFromLTRB(minf(mn[0], mn[2]), minf(mn[1], mn[3]), maxf(mx[0], mx[2]), maxf(mx[1], mx[3]))
}

// Inset insets the rect.
func (r Rect) Inset(dx, dy float32) (Rect, bool) {
	return RectFromLTRB(r.left+dx, r.top+dy, r.right-dx, r.bottom-dy)
}

// Outset outsets the rect.
func (r Rect) Outset(dx, dy float32) (Rect, bool) { return r.Inset(-dx, -dy) }

// Transform maps the rect; under a skew the result bounds the mapped corners.
func (r Rect) Transform(ts Transform) (Rect, bool) {
	switch {
	case ts.IsIdentity():
		return r, true
	case ts.HasSkew():
		pts := []Point{{r.left, r.top}, {r.right, r.top}, {r.left, r.bottom}, {r.right, r.bottom}}
		ts.MapPoints(pts)
		return RectFromPoints(pts)
	default:
		pts := []Point{{r.left, r.top}, {r.right, r.bottom}}
		ts.MapPoints(pts)
		return RectFromPoints(pts)
	}
}

// BBoxTransform maps the rect from object bounding box units into bbox.
func (r Rect) BBoxTransform(bbox NonZeroRect) Rect {
	x := float32(r.X()*bbox.Width()) + bbox.X()
	y := float32(r.Y()*bbox.Height()) + bbox.Y()
	w := float32(r.Width() * bbox.Width())
	h := float32(r.Height() * bbox.Height())
	v, ok := RectFromXYWH(x, y, w, h)
	if !ok {
		panic("skia: Rect.BBoxTransform overflow") // Rust unwraps
	}
	return v
}

// ToNonZeroRect converts into a NonZeroRect.
func (r Rect) ToNonZeroRect() (NonZeroRect, bool) {
	return NonZeroRectFromXYWH(r.X(), r.Y(), r.Width(), r.Height())
}

// NonZeroRect is a Rect with a positive width and height.
type NonZeroRect struct{ left, top, right, bottom float32 }

// NonZeroRectFromLTRB creates a NonZeroRect; ok is false where Rust gives None.
func NonZeroRectFromLTRB(left, top, right, bottom float32) (NonZeroRect, bool) {
	if !isFinite(left) || !isFinite(top) || !isFinite(right) || !isFinite(bottom) {
		return NonZeroRect{}, false
	}
	if left < right && top < bottom && checkedF32Sub(right, left) && checkedF32Sub(bottom, top) {
		return NonZeroRect{left, top, right, bottom}, true
	}
	return NonZeroRect{}, false
}

// NonZeroRectFromXYWH creates a NonZeroRect from its position and size.
func NonZeroRectFromXYWH(x, y, w, h float32) (NonZeroRect, bool) {
	return NonZeroRectFromLTRB(x, y, w+x, h+y)
}

func (r NonZeroRect) Left() float32   { return r.left }
func (r NonZeroRect) Top() float32    { return r.top }
func (r NonZeroRect) Right() float32  { return r.right }
func (r NonZeroRect) Bottom() float32 { return r.bottom }
func (r NonZeroRect) X() float32      { return r.left }
func (r NonZeroRect) Y() float32      { return r.top }
func (r NonZeroRect) Width() float32  { return r.right - r.left }
func (r NonZeroRect) Height() float32 { return r.bottom - r.top }

// Size returns the rect's size.
func (r NonZeroRect) Size() Size {
	s, _ := SizeFromWH(r.Width(), r.Height())
	return s
}

// TranslateTo moves the rect to a position.
func (r NonZeroRect) TranslateTo(x, y float32) (NonZeroRect, bool) {
	return NonZeroRectFromXYWH(x, y, r.Width(), r.Height())
}

// Transform maps the rect; under a skew the result bounds the mapped corners.
func (r NonZeroRect) Transform(ts Transform) (NonZeroRect, bool) {
	if ts.IsIdentity() {
		return r, true
	}
	v, ok := r.ToRect().Transform(ts)
	if !ok {
		return NonZeroRect{}, false
	}
	return v.ToNonZeroRect()
}

// BBoxTransform maps the rect from object bounding box units into bbox.
func (r NonZeroRect) BBoxTransform(bbox NonZeroRect) NonZeroRect {
	x := float32(r.X()*bbox.Width()) + bbox.X()
	y := float32(r.Y()*bbox.Height()) + bbox.Y()
	w := float32(r.Width() * bbox.Width())
	h := float32(r.Height() * bbox.Height())
	v, ok := NonZeroRectFromXYWH(x, y, w, h)
	if !ok {
		panic("skia: NonZeroRect.BBoxTransform is empty or overflows") // Rust unwraps
	}
	return v
}

// ToRect converts into a Rect.
func (r NonZeroRect) ToRect() Rect {
	v, _ := RectFromXYWH(r.X(), r.Y(), r.Width(), r.Height())
	return v
}

// ToIntRect converts into an IntRect.
func (r NonZeroRect) ToIntRect() IntRect {
	v, ok := IntRectFromXYWH(castI32(float64(floor32(r.X()))), castI32(float64(floor32(r.Y()))),
		max(1, castU32(float64(ceil32(r.Width())))), max(1, castU32(float64(ceil32(r.Height())))))
	if !ok {
		panic("skia: NonZeroRect.ToIntRect overflows") // Rust unwraps
	}
	return v
}
