package usvg

import (
	"math"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// Geometry helpers (tree/geom.rs). ApproxZeroUlps and IsValidLength are approxZeroUlpsF32/F64 and
// isValidLengthF32/F64 in f32.go.

// viewBox is usvg's ViewBox: the `viewBox` and `preserveAspectRatio` attributes.
type viewBox struct {
	rect   skia.NonZeroRect
	aspect stAspectRatio
}

// toTransform is ViewBox::to_transform: maps the view box onto an image of imgSize.
func (vb viewBox) toTransform(imgSize skia.Size) skia.Transform {
	vr := vb.rect

	sx := imgSize.Width() / vr.Width()
	sy := imgSize.Height() / vr.Height()

	if vb.aspect.Align != stAlignNone {
		var s float32
		if vb.aspect.Slice {
			if sx < sy {
				s = sy
			} else {
				s = sx
			}
		} else {
			if sx > sy {
				s = sy
			} else {
				s = sx
			}
		}
		sx, sy = s, s
	}

	x := float32(-vr.X() * sx)
	y := float32(-vr.Y() * sy)
	w := imgSize.Width() - float32(vr.Width()*sx)
	h := imgSize.Height() - float32(vr.Height()*sy)

	tx, ty := alignedPos(vb.aspect.Align, x, y, w, h)
	return skia.TransformFromRow(sx, 0, 0, sy, tx, ty)
}

// bbox is usvg's BBox, a bounding box accumulator. Its zero value is not the default; start from
// defaultBBox.
type bbox struct {
	left, top, right, bottom float32
}

func bboxFromRect(r skia.Rect) bbox {
	return bbox{r.Left(), r.Top(), r.Right(), r.Bottom()}
}

func bboxFromNonZeroRect(r skia.NonZeroRect) bbox {
	return bbox{r.Left(), r.Top(), r.Right(), r.Bottom()}
}

// defaultBBox is BBox::default(), an invalid box that any expand replaces.
func defaultBBox() bbox {
	return bbox{math.MaxFloat32, math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
}

// isDefault checks if the bounding box is default, i.e. invalid.
func (b bbox) isDefault() bool {
	return b.left == math.MaxFloat32 && b.top == math.MaxFloat32 &&
		b.right == -math.MaxFloat32 && b.bottom == -math.MaxFloat32
}

// expand returns the box grown to include r, with f32::min/max semantics.
func (b bbox) expand(r bbox) bbox {
	return bbox{
		left:   minF32(b.left, r.left),
		top:    minF32(b.top, r.top),
		right:  maxF32(b.right, r.right),
		bottom: maxF32(b.bottom, r.bottom),
	}
}

// toRect is BBox::to_rect; ok is false for the default box or an invalid rect.
func (b bbox) toRect() (skia.Rect, bool) {
	if b.isDefault() {
		return skia.Rect{}, false
	}
	return skia.RectFromLTRB(b.left, b.top, b.right, b.bottom)
}

// toNonZeroRect is BBox::to_non_zero_rect; ok is false for the default box or an invalid rect.
func (b bbox) toNonZeroRect() (skia.NonZeroRect, bool) {
	if b.isDefault() {
		return skia.NonZeroRect{}, false
	}
	return skia.NonZeroRectFromLTRB(b.left, b.top, b.right, b.bottom)
}

// alignedPos returns the object position for align, given its offset (x, y) and free space (w, h).
func alignedPos(align stAlign, x, y, w, h float32) (float32, float32) {
	switch align {
	case stAlignXMidYMin:
		return x + w/2, y
	case stAlignXMaxYMin:
		return x + w, y
	case stAlignXMinYMid:
		return x, y + h/2
	case stAlignXMidYMid:
		return x + w/2, y + h/2
	case stAlignXMaxYMid:
		return x + w, y + h/2
	case stAlignXMinYMax:
		return x, y + h
	case stAlignXMidYMax:
		return x + w/2, y + h
	case stAlignXMaxYMax:
		return x + w, y + h
	default: // stAlignNone, stAlignXMinYMin
		return x, y
	}
}
