package skia

// Masks (mask.rs). PNG encoding and decoding are not ported.

// MaskType says how a mask is made from a pixmap.
type MaskType uint8

const (
	MaskTypeAlpha     MaskType = iota // the pixmap's alpha
	MaskTypeLuminance                 // the pixmap's luminance multiplied by alpha
)

// Mask is an 8-bit coverage mask, one byte per pixel.
type Mask struct {
	data []byte
	size IntSize
}

// NewMask allocates a zeroed mask; ok is false for a zero size.
func NewMask(width, height uint32) (*Mask, bool) {
	size, ok := IntSizeFromWH(width, height)
	if !ok {
		return nil, false
	}
	return &Mask{data: make([]byte, int(width)*int(height)), size: size}, true
}

// MaskFromPixmap creates a mask from a pixmap's alpha or luminance.
func MaskFromPixmap(pixmap PixmapRef, maskType MaskType) *Mask {
	n := int(pixmap.Width()) * int(pixmap.Height())
	m := &Mask{data: make([]byte, n), size: pixmap.Size()}
	switch maskType {
	case MaskTypeAlpha:
		for i := range m.data {
			m.data[i] = pixmap.data[i*4+3]
		}
	case MaskTypeLuminance:
		for i := range m.data {
			p := pixmap.pixelAt(i)
			r := float32(p.Red()) / 255.0
			g := float32(p.Green()) / 255.0
			b := float32(p.Blue()) / 255.0
			a := float32(p.Alpha()) / 255.0

			if p.Alpha() != 0 {
				r /= a
				g /= a
				b /= a
			}

			luma := float32(r*0.2126) + float32(g*0.7152) + float32(b*0.0722)
			m.data[i] = castU8(ceil32(clamp32(float32(float32(luma*a)*255.0), 0, 255)))
		}
	}
	return m
}

// MaskFromVec creates a mask from bytes; ok is false when the length does not match the size.
func MaskFromVec(data []byte, size IntSize) (*Mask, bool) {
	if len(data) != int(size.width)*int(size.height) {
		return nil, false
	}
	return &Mask{data: data, size: size}, true
}

func (m *Mask) Width() uint32   { return m.size.width }
func (m *Mask) Height() uint32  { return m.size.height }
func (m *Mask) Size() IntSize   { return m.size }
func (m *Mask) Data() []byte    { return m.data }
func (m *Mask) DataMut() []byte { return m.data }
func (m *Mask) Take() []byte    { return m.data }

func (m *Mask) asSubmask() subMaskRef {
	return subMaskRef{data: m.data, size: m.size, realWidth: m.size.width}
}

func (m *Mask) submask(rect IntRect) (subMaskRef, bool) {
	r, ok := m.size.ToIntRect(0, 0).Intersect(rect)
	if !ok {
		return subMaskRef{}, false
	}
	offset := int(r.Top())*int(m.Width()) + int(r.Left())
	return subMaskRef{data: m.data[offset:], size: r.Size(), realWidth: m.size.width}, true
}

func (m *Mask) asSubpixmap() subPixmapMut {
	return subPixmapMut{data: m.data, size: m.size, realWidth: int(m.size.width)}
}

func (m *Mask) subpixmap(rect IntRect) (subPixmapMut, bool) {
	r, ok := m.size.ToIntRect(0, 0).Intersect(rect)
	if !ok {
		return subPixmapMut{}, false
	}
	offset := int(r.Top())*int(m.Width()) + int(r.Left())
	return subPixmapMut{data: m.data[offset:], size: r.Size(), realWidth: int(m.size.width)}, true
}

// FillPath draws a filled path onto the mask, as PixmapMut.FillPath does with a white paint.
func (m *Mask) FillPath(path *Path, fillRule FillRule, antiAlias bool, transform Transform) {
	if !transform.IsIdentity() {
		p := path.Transform(transform)
		if p == nil {
			return // path transformation failed
		}
		m.FillPath(p, fillRule, antiAlias, Identity())
		return
	}

	// Skip empty paths and horizontal/vertical lines.
	pathBounds := path.Bounds()
	if isNearlyZero(pathBounds.Width()) || isNearlyZero(pathBounds.Height()) {
		return
	}
	if isTooBigForMath(path) {
		return
	}

	if tiler, ok := newDrawTiler(m.Width(), m.Height()); ok {
		path := path
		for {
			tile, ok := tiler.next()
			if !ok {
				break
			}
			path = path.Transform(TransformFromTranslate(-float32(tile.X()), -float32(tile.Y())))
			if path == nil {
				return // path transformation failed
			}

			clipRect := tile.Size().toScreenIntRect(0, 0)
			subpix, ok := m.subpixmap(tile.ToIntRect())
			if !ok {
				continue // technically unreachable
			}

			b := newRasterPipelineBlitterMask(&subpix)
			// A tile the path does not touch is not an error.
			if antiAlias {
				fillPathAA(path, fillRule, clipRect, b)
			} else {
				fillPath(path, fillRule, clipRect, b)
			}

			path = path.Transform(TransformFromTranslate(float32(tile.X()), float32(tile.Y())))
			if path == nil {
				return // technically unreachable
			}
		}
		return
	}

	clipRect := m.size.toScreenIntRect(0, 0)
	subpix := m.asSubpixmap()
	b := newRasterPipelineBlitterMask(&subpix)
	if antiAlias {
		fillPathAA(path, fillRule, clipRect, b)
	} else {
		fillPath(path, fillRule, clipRect, b)
	}
}

// IntersectPath intersects the path with the mask, through a temporary mask of the same size.
func (m *Mask) IntersectPath(path *Path, fillRule FillRule, antiAlias bool, transform Transform) {
	sub, _ := NewMask(m.Width(), m.Height())
	sub.FillPath(path, fillRule, antiAlias, transform)
	for i := range m.data {
		m.data[i] = premultiplyU8(m.data[i], sub.data[i])
	}
}

// Invert inverts the mask.
func (m *Mask) Invert() {
	for i := range m.data {
		m.data[i] = 255 - m.data[i]
	}
}

// Clear zero-fills the mask.
func (m *Mask) Clear() { clear(m.data) }

// subMaskRef is a window into a mask: data starts at the window's top-left and rows are realWidth apart.
type subMaskRef struct {
	data      []byte
	size      IntSize
	realWidth uint32
}

func (m subMaskRef) maskCtx() maskCtx { return maskCtx{data: m.data, realWidth: m.realWidth} }
