package skia

// IntSize is an integer size with a positive width and height (size.rs).
type IntSize struct{ width, height uint32 }

// IntSizeFromWH creates an IntSize; ok is false for a zero side.
func IntSizeFromWH(width, height uint32) (IntSize, bool) {
	if width == 0 || height == 0 {
		return IntSize{}, false
	}
	return IntSize{width, height}, true
}

func (s IntSize) Width() uint32                { return s.width }
func (s IntSize) Height() uint32               { return s.height }
func (s IntSize) Dimensions() (uint32, uint32) { return s.width, s.height }

// ScaleBy scales the size by a factor.
func (s IntSize) ScaleBy(factor float32) (IntSize, bool) {
	return IntSizeFromWH(castU32(float64(round32(float32(float32(s.width)*factor)))), castU32(float64(round32(float32(float32(s.height)*factor)))))
}

// ScaleTo scales the size to fit into another, keeping the aspect ratio.
func (s IntSize) ScaleTo(to IntSize) IntSize { return intSizeScale(s, to, false) }

// ScaleToWidth scales the size to a width, keeping the aspect ratio.
func (s IntSize) ScaleToWidth(newWidth uint32) (IntSize, bool) {
	newHeight := ceil32(float32(float32(newWidth)*float32(s.height)) / float32(s.width))
	return IntSizeFromWH(newWidth, castU32(float64(newHeight)))
}

// ScaleToHeight scales the size to a height, keeping the aspect ratio.
func (s IntSize) ScaleToHeight(newHeight uint32) (IntSize, bool) {
	newWidth := ceil32(float32(float32(newHeight)*float32(s.width)) / float32(s.height))
	return IntSizeFromWH(castU32(float64(newWidth)), newHeight)
}

// ToSize converts into a Size.
func (s IntSize) ToSize() Size { return Size{float32(s.width), float32(s.height)} }

// ToIntRect converts into an IntRect at a position.
func (s IntSize) ToIntRect(x, y int32) IntRect {
	r, ok := IntRectFromXYWH(x, y, s.width, s.height)
	if !ok {
		panic("skia: IntSize.ToIntRect overflows") // Rust unwraps
	}
	return r
}

func intSizeScale(s1, s2 IntSize, expand bool) IntSize {
	rw := castU32(float64(ceil32(float32(float32(s2.height)*float32(s1.width)) / float32(s1.height))))
	withH := rw >= s2.width
	if expand {
		withH = rw <= s2.width
	}
	w, h := rw, s2.height
	if withH {
		w, h = s2.width, castU32(float64(ceil32(float32(float32(s2.width)*float32(s1.height))/float32(s1.width))))
	}
	r, ok := IntSizeFromWH(w, h)
	if !ok {
		panic("skia: IntSize scale is empty") // Rust unwraps
	}
	return r
}

// Size is a size with a positive, finite width and height (size.rs).
type Size struct{ width, height float32 }

// SizeFromWH creates a Size; ok is false unless both sides are finite and positive.
func SizeFromWH(width, height float32) (Size, bool) {
	if isFinite(width) && width > 0 && isFinite(height) && height > 0 {
		return Size{width, height}, true
	}
	return Size{}, false
}

func (s Size) Width() float32  { return s.width }
func (s Size) Height() float32 { return s.height }

// ScaleTo scales the size to fit into another, keeping the aspect ratio.
func (s Size) ScaleTo(to Size) Size { return sizeScale(s, to, false) }

// ExpandTo scales the size to cover another, keeping the aspect ratio.
func (s Size) ExpandTo(to Size) Size { return sizeScale(s, to, true) }

// ScaleBy scales the size by a factor.
func (s Size) ScaleBy(factor float32) (Size, bool) {
	return SizeFromWH(float32(s.width*factor), float32(s.height*factor))
}

// ScaleToWidth scales the size to a width, keeping the aspect ratio.
func (s Size) ScaleToWidth(newWidth float32) (Size, bool) {
	return SizeFromWH(newWidth, float32(newWidth*s.height)/s.width)
}

// ScaleToHeight scales the size to a height, keeping the aspect ratio.
func (s Size) ScaleToHeight(newHeight float32) (Size, bool) {
	return SizeFromWH(float32(newHeight*s.width)/s.height, newHeight)
}

// ToIntSize rounds into an IntSize of at least 1x1.
func (s Size) ToIntSize() IntSize {
	return IntSize{max(1, castU32(float64(round32(s.width)))), max(1, castU32(float64(round32(s.height))))}
}

// ToRect converts into a Rect at a position.
func (s Size) ToRect(x, y float32) (Rect, bool) { return RectFromXYWH(x, y, s.width, s.height) }

// ToNonZeroRect converts into a NonZeroRect at a position.
func (s Size) ToNonZeroRect(x, y float32) NonZeroRect {
	r, ok := NonZeroRectFromXYWH(x, y, s.width, s.height)
	if !ok {
		panic("skia: Size.ToNonZeroRect overflows") // Rust unwraps
	}
	return r
}

func sizeScale(s1, s2 Size, expand bool) Size {
	rw := float32(s2.height*s1.width) / s1.height
	withH := rw >= s2.width
	if expand {
		withH = rw <= s2.width
	}
	if !withH {
		r, ok := SizeFromWH(rw, s2.height)
		if !ok {
			panic("skia: Size scale overflows") // Rust unwraps
		}
		return r
	}
	r, ok := SizeFromWH(s2.width, float32(s2.width*s1.height)/s1.width)
	if !ok {
		panic("skia: Size scale overflows") // Rust unwraps
	}
	return r
}
