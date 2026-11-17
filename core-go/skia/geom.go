package skia

import "math"

// screenIntRect is a screen IntRect (geom.rs): x and y in 0..=i32::MAX, width and height in
// 1..=i32::MAX, and x+width, y+height do not overflow. The constructors check this; the methods rely on
// it.
type screenIntRect struct{ x, y, width, height uint32 }

// screenIntRectFromXYWH creates a rect; ok is false where Rust gives None.
func screenIntRectFromXYWH(x, y, width, height uint32) (screenIntRect, bool) {
	if x > math.MaxInt32 || y > math.MaxInt32 || width > math.MaxInt32 || height > math.MaxInt32 {
		return screenIntRect{}, false
	}
	if uint64(x)+uint64(width) > math.MaxUint32 || uint64(y)+uint64(height) > math.MaxUint32 {
		return screenIntRect{}, false
	}
	if width == 0 || height == 0 {
		return screenIntRect{}, false
	}
	return screenIntRect{x, y, width, height}, true
}

// screenIntRectFromXYWHSafe creates a rect without checks: width and height must be non-zero
// (LengthU32), as from_xywh_safe requires.
func screenIntRectFromXYWHSafe(x, y, width, height uint32) screenIntRect {
	return screenIntRect{x, y, width, height}
}

func (r screenIntRect) X() uint32      { return r.x }
func (r screenIntRect) Y() uint32      { return r.y }
func (r screenIntRect) Width() uint32  { return r.width }
func (r screenIntRect) Height() uint32 { return r.height }
func (r screenIntRect) Left() uint32   { return r.x }
func (r screenIntRect) Top() uint32    { return r.y }
func (r screenIntRect) Right() uint32  { return r.x + r.width }
func (r screenIntRect) Bottom() uint32 { return r.y + r.height }
func (r screenIntRect) Size() IntSize  { return IntSize{r.width, r.height} }

// Contains reports whether the rect completely includes other.
func (r screenIntRect) Contains(o screenIntRect) bool {
	return r.x <= o.x && r.y <= o.y && r.Right() >= o.Right() && r.Bottom() >= o.Bottom()
}

// ToIntRect converts into an IntRect, which cannot fail.
func (r screenIntRect) ToIntRect() IntRect { return IntRect{int32(r.x), int32(r.y), r.width, r.height} }

// ToRect converts into a Rect, which cannot fail.
func (r screenIntRect) ToRect() Rect {
	return Rect{float32(r.x), float32(r.y), float32(r.x) + float32(r.width), float32(r.y) + float32(r.height)}
}

// toScreenIntRect is IntSizeExt::to_screen_int_rect; it panics where Rust unwraps.
func (s IntSize) toScreenIntRect(x, y uint32) screenIntRect {
	r, ok := screenIntRectFromXYWH(x, y, s.width, s.height)
	if !ok {
		panic("skia: size does not fit a screen rect")
	}
	return r
}

// toScreenIntRect is IntRectExt::to_screen_int_rect: ok is false for a negative x or y.
func (r IntRect) toScreenIntRect() (screenIntRect, bool) {
	if r.x < 0 || r.y < 0 {
		return screenIntRect{}, false
	}
	return screenIntRectFromXYWH(uint32(r.x), uint32(r.y), r.width, r.height)
}
