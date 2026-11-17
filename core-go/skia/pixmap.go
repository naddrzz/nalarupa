package skia

import "math"

// Pixmaps (pixmap.rs). Pixels are premultiplied RGBA bytes (PremultipliedColorU8 layout). PNG encoding
// and decoding are not ported.

// BytesPerPixel is the number of bytes per pixel.
const BytesPerPixel = 4

// Pixmap is a container that owns premultiplied RGBA pixels.
type Pixmap struct {
	data []byte
	size IntSize
}

// NewPixmap allocates a transparent pixmap; ok is false for a zero or too large size.
func NewPixmap(width, height uint32) (*Pixmap, bool) {
	size, ok := IntSizeFromWH(width, height)
	if !ok {
		return nil, false
	}
	n, ok := dataLenForSize(size)
	if !ok {
		return nil, false
	}
	return &Pixmap{data: make([]byte, n), size: size}, true
}

// PixmapFromVec creates a pixmap from premultiplied RGBA bytes; ok is false when the length does not
// match the size.
func PixmapFromVec(data []byte, size IntSize) (*Pixmap, bool) {
	n, ok := dataLenForSize(size)
	if !ok || len(data) != n {
		return nil, false
	}
	return &Pixmap{data: data, size: size}, true
}

func (p *Pixmap) AsRef() PixmapRef                               { return PixmapRef{p.data, p.size} }
func (p *Pixmap) AsMut() PixmapMut                               { return PixmapMut{p.data, p.size} }
func (p *Pixmap) Width() uint32                                  { return p.size.width }
func (p *Pixmap) Height() uint32                                 { return p.size.height }
func (p *Pixmap) Size() IntSize                                  { return p.size }
func (p *Pixmap) Data() []byte                                   { return p.data }
func (p *Pixmap) DataMut() []byte                                { return p.data }
func (p *Pixmap) Fill(color Color)                               { p.AsMut().Fill(color) }
func (p *Pixmap) Take() []byte                                   { return p.data }
func (p *Pixmap) Pixel(x, y uint32) (PremultipliedColorU8, bool) { return p.AsRef().Pixel(x, y) }

// TakeDemultiplied returns the pixels demultiplied (RGBA, not premultiplied); the pixmap must not be
// used afterwards.
func (p *Pixmap) TakeDemultiplied() []byte {
	for i := 0; i+4 <= len(p.data); i += 4 {
		c := PremultipliedColorU8(p.data[i : i+4]).Demultiply()
		copy(p.data[i:i+4], c[:])
	}
	return p.data
}

// CloneRect returns a copy of the part of the pixmap inside rect.
func (p *Pixmap) CloneRect(rect IntRect) (*Pixmap, bool) { return p.AsRef().CloneRect(rect) }

// PixmapRef is a borrowed view of premultiplied RGBA pixels.
type PixmapRef struct {
	data []byte
	size IntSize
}

// PixmapRefFromBytes creates a view; ok is false when data is shorter than the size needs.
func PixmapRefFromBytes(data []byte, width, height uint32) (PixmapRef, bool) {
	size, ok := IntSizeFromWH(width, height)
	if !ok {
		return PixmapRef{}, false
	}
	n, ok := dataLenForSize(size)
	if !ok || len(data) < n {
		return PixmapRef{}, false
	}
	return PixmapRef{data, size}, true
}

// ToOwned copies the pixels into a new pixmap.
func (p PixmapRef) ToOwned() *Pixmap {
	return &Pixmap{data: append([]byte(nil), p.data...), size: p.size}
}

func (p PixmapRef) Width() uint32       { return p.size.width }
func (p PixmapRef) Height() uint32      { return p.size.height }
func (p PixmapRef) Size() IntSize       { return p.size }
func (p PixmapRef) Data() []byte        { return p.data }
func (p PixmapRef) rect() screenIntRect { return p.size.toScreenIntRect(0, 0) }

// Pixel returns the pixel at (x, y); ok is false outside the data.
func (p PixmapRef) Pixel(x, y uint32) (PremultipliedColorU8, bool) {
	idx := uint64(p.Width())*uint64(y) + uint64(x)
	if idx > math.MaxUint32 || idx >= uint64(len(p.data)/BytesPerPixel) {
		return PremultipliedColorU8{}, false
	}
	return p.pixelAt(int(idx)), true
}

// pixelAt returns the pixel at index i of the data.
func (p PixmapRef) pixelAt(i int) PremultipliedColorU8 {
	return PremultipliedColorU8(p.data[i*4 : i*4+4])
}

// CloneRect returns a copy of the part of the pixmap inside rect.
func (p PixmapRef) CloneRect(rect IntRect) (*Pixmap, bool) {
	r, ok := p.rect().ToIntRect().Intersect(rect)
	if !ok {
		return nil, false
	}
	n, ok := NewPixmap(r.Width(), r.Height())
	if !ok {
		return nil, false
	}
	for y := uint32(0); y < r.Height(); y++ {
		for x := uint32(0); x < r.Width(); x++ {
			oldIdx := (y+uint32(r.Y()))*p.Width() + (x + uint32(r.X()))
			newIdx := y*r.Width() + x
			copy(n.data[newIdx*4:newIdx*4+4], p.data[oldIdx*4:oldIdx*4+4])
		}
	}
	return n, true
}

// PixmapMut is a mutable view of premultiplied RGBA pixels. The painting methods are in painter.go.
type PixmapMut struct {
	data []byte
	size IntSize
}

// PixmapMutFromBytes creates a view; ok is false when data is shorter than the size needs.
func PixmapMutFromBytes(data []byte, width, height uint32) (PixmapMut, bool) {
	r, ok := PixmapRefFromBytes(data, width, height)
	return PixmapMut(r), ok
}

func (p PixmapMut) ToOwned() *Pixmap { return p.AsRef().ToOwned() }
func (p PixmapMut) AsRef() PixmapRef { return PixmapRef(p) }
func (p PixmapMut) Width() uint32    { return p.size.width }
func (p PixmapMut) Height() uint32   { return p.size.height }
func (p PixmapMut) Size() IntSize    { return p.size }
func (p PixmapMut) DataMut() []byte  { return p.data }

// Fill fills the whole pixmap with a color.
func (p PixmapMut) Fill(color Color) {
	c := color.Premultiply().ToColorU8()
	for i := 0; i+4 <= len(p.data); i += 4 {
		copy(p.data[i:i+4], c[:])
	}
}

func (p PixmapMut) asSubpixmap() subPixmapMut {
	return subPixmapMut{data: p.data, size: p.size, realWidth: int(p.size.width)}
}

func (p PixmapMut) subpixmap(rect IntRect) (subPixmapMut, bool) {
	r, ok := p.size.ToIntRect(0, 0).Intersect(rect)
	if !ok {
		return subPixmapMut{}, false
	}
	rowBytes := int(p.Width()) * BytesPerPixel
	offset := int(r.Top())*rowBytes + int(r.Left())*BytesPerPixel
	return subPixmapMut{data: p.data[offset:], size: r.Size(), realWidth: int(p.Width())}, true
}

// subPixmapMut is a window into a pixmap (4 bytes per pixel) or a mask (1 byte per pixel): data starts at
// the window's top-left pixel and rows are realWidth pixels apart.
type subPixmapMut struct {
	data      []byte
	size      IntSize
	realWidth int
}

// offset is the pixel index of (dx, dy) in data.
func (p *subPixmapMut) offset(dx, dy int) int { return p.realWidth*dy + dx }

func minRowBytes(size IntSize) (int, bool) {
	if size.width > math.MaxInt32 {
		return 0, false
	}
	w := int64(size.width) * BytesPerPixel
	if w > math.MaxInt32 || w == 0 {
		return 0, false
	}
	return int(w), true
}

func computeDataLen(size IntSize, rowBytes int) (int, bool) {
	if size.height == 0 {
		return 0, false
	}
	h := uint64(size.height-1) * uint64(rowBytes)
	w := uint64(size.width) * BytesPerPixel
	n := h + w
	if n > math.MaxInt {
		return 0, false
	}
	return int(n), true
}

func dataLenForSize(size IntSize) (int, bool) {
	rowBytes, ok := minRowBytes(size)
	if !ok {
		return 0, false
	}
	return computeDataLen(size, rowBytes)
}
