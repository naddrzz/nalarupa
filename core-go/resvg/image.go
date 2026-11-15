package resvg

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/webp"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Port of image.rs (with the raster-images feature). Rust decodes with the png, zune-jpeg, gif and
// image-webp crates; the port uses the Go decoders, with the chroma upsampling and color conversion of
// zune-jpeg and image-webp (image_yuv.go). PNG, GIF and WebP decode to the same pixels; Go's JPEG IDCT does not
// round as zune-jpeg's does: testdata/render/images/images-jpeg.svg differs by up to 3 levels per channel
// on about 3.5% of its pixels (fixtures.json allows 3 levels on 5%).

func renderImage(img *usvg.Image, transform skia.Transform, pixmap skia.PixmapMut) {
	if !img.IsVisible() {
		return
	}
	renderImageKind(img.Kind(), transform, img.RenderingMode(), pixmap)
}

func renderImageKind(kind usvg.ImageKind, transform skia.Transform, mode usvg.ImageRendering, pixmap skia.PixmapMut) {
	if svg, ok := kind.(usvg.ImageKindSVG); ok {
		renderVector(svg.Tree, transform, pixmap)
		return
	}
	renderRaster(kind, transform, mode, pixmap)
}

func renderVector(tree *usvg.Tree, transform skia.Transform, pixmap skia.PixmapMut) {
	sub, ok := skia.NewPixmap(pixmap.Width(), pixmap.Height())
	if !ok {
		panic("resvg: render_vector pixmap") // Rust unwraps
	}
	Render(tree, transform, sub.AsMut())
	paint := skia.DefaultPixmapPaint()
	pixmap.DrawPixmap(0, 0, sub.AsRef(), &paint, skia.Identity(), nil)
}

func renderRaster(kind usvg.ImageKind, transform skia.Transform, mode usvg.ImageRendering, pixmap skia.PixmapMut) {
	raster, ok := decodeRaster(kind)
	if !ok {
		return // Rust logs "Failed to decode ..."
	}
	size, ok := skia.SizeFromWH(float32(raster.Width()), float32(raster.Height()))
	if !ok {
		return
	}
	rect, ok := size.ToRect(0, 0)
	if !ok {
		return
	}
	quality := skia.FilterQualityBicubic
	switch mode {
	case usvg.ImageRenderingOptimizeSpeed, usvg.ImageRenderingCrispEdges, usvg.ImageRenderingPixelated:
		quality = skia.FilterQualityNearest
	case usvg.ImageRenderingSmooth:
		quality = skia.FilterQualityBilinear
	}
	paint := skia.DefaultPaint()
	paint.Shader = skia.NewPattern(raster.AsRef(), skia.SpreadModePad, quality, 1, skia.Identity())
	pixmap.FillRect(rect, &paint, transform, nil)
}

func decodeRaster(kind usvg.ImageKind) (*skia.Pixmap, bool) {
	switch data := kind.(type) {
	case usvg.ImageKindPNG:
		return decodePNG(data)
	case usvg.ImageKindJPEG:
		return decodeJPEG(data)
	case usvg.ImageKindGIF:
		return decodeGIF(data)
	case usvg.ImageKindWEBP:
		return decodeWebP(data)
	}
	return nil, false
}

// decodePNG is tiny-skia's Pixmap::decode_png: the png crate normalised to 8-bit color (palette and
// transparency expanded, 16-bit channels cut to their high byte), then premultiplied with premultiply_u8.
func decodePNG(data []byte) (*skia.Pixmap, bool) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	rgba, w, h := straightRGBA8(img)
	for i := 0; i < len(rgba); i += 4 {
		a := rgba[i+3]
		rgba[i] = premultiplyU8(rgba[i], a)
		rgba[i+1] = premultiplyU8(rgba[i+1], a)
		rgba[i+2] = premultiplyU8(rgba[i+2], a)
	}
	return pixmapFromRGBA(rgba, w, h)
}

func decodeJPEG(data []byte) (*skia.Pixmap, bool) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	// zune-jpeg's RGBA output is opaque, so it is used as is.
	if m, ok := img.(*image.YCbCr); ok {
		rgba := zuneYCbCrToRGBA(m)
		return pixmapFromRGBA(rgba, m.Rect.Dx(), m.Rect.Dy())
	}
	rgba, w, h := straightRGBA8(img)
	return pixmapFromRGBA(rgba, w, h)
}

func decodeGIF(data []byte) (*skia.Pixmap, bool) {
	img, err := gif.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	// The first frame alone, at its own size (the gif crate's frame buffer).
	rgba, w, h := straightRGBA8(img)
	premultiplyF64(rgba)
	return pixmapFromRGBA(rgba, w, h)
}

func decodeWebP(data []byte) (*skia.Pixmap, bool) {
	img, err := webp.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	var rgba []byte
	var w, h int
	switch m := img.(type) {
	case *image.NYCbCrA:
		rgba, w, h = webpFancyRGBA(&m.YCbCr), m.Rect.Dx(), m.Rect.Dy()
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				rgba[(y*w+x)*4+3] = m.A[y*m.AStride+x]
			}
		}
	case *image.YCbCr:
		rgba, w, h = webpFancyRGBA(m), m.Rect.Dx(), m.Rect.Dy()
	default:
		rgba, w, h = straightRGBA8(img)
	}
	if webpHasAlpha(data) {
		premultiplyF64(rgba) // rgba_to_pixmap
	} else {
		// rgb_to_pixmap: the decoder drops the alpha channel.
		for i := 3; i < len(rgba); i += 4 {
			rgba[i] = 255
		}
	}
	return pixmapFromRGBA(rgba, w, h)
}

func pixmapFromRGBA(rgba []byte, w, h int) (*skia.Pixmap, bool) {
	size, ok := skia.IntSizeFromWH(uint32(w), uint32(h))
	if !ok {
		return nil, false
	}
	return skia.PixmapFromVec(rgba, size)
}

// straightRGBA8 is img as non-premultiplied 8-bit RGBA rows; 16-bit channels keep their high byte.
func straightRGBA8(img image.Image) ([]byte, int, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, 0, w*h*4)
	switch m := img.(type) {
	case *image.NRGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			i := m.PixOffset(b.Min.X, y)
			out = append(out, m.Pix[i:i+w*4]...)
		}
		return out, w, h
	case *image.Gray:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			i := m.PixOffset(b.Min.X, y)
			for _, g := range m.Pix[i : i+w] {
				out = append(out, g, g, g, 255)
			}
		}
		return out, w, h
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			var c color.NRGBA
			switch v := img.At(x, y).(type) {
			case color.NRGBA:
				c = v
			case color.NRGBA64:
				c = color.NRGBA{uint8(v.R >> 8), uint8(v.G >> 8), uint8(v.B >> 8), uint8(v.A >> 8)}
			case color.Gray16:
				g := uint8(v.Y >> 8)
				c = color.NRGBA{g, g, g, 255}
			case color.RGBA64:
				if v.A == 0xffff {
					c = color.NRGBA{uint8(v.R >> 8), uint8(v.G >> 8), uint8(v.B >> 8), 255}
				} else {
					c = color.NRGBAModel.Convert(v).(color.NRGBA)
				}
			case color.RGBA:
				if v.A == 0xff {
					c = color.NRGBA{v.R, v.G, v.B, 255}
				} else {
					c = color.NRGBAModel.Convert(v).(color.NRGBA)
				}
			default:
				c = color.NRGBAModel.Convert(v).(color.NRGBA)
			}
			out = append(out, c.R, c.G, c.B, c.A)
		}
	}
	return out, w, h
}

// premultiplyF64 is image.rs rgba_to_pixmap's premultiplication.
func premultiplyF64(rgba []byte) {
	for i := 0; i < len(rgba); i += 4 {
		a := float64(rgba[i+3]) / 255.0
		rgba[i] = uint8(float64(float64(rgba[i])*a) + 0.5)
		rgba[i+1] = uint8(float64(float64(rgba[i+1])*a) + 0.5)
		rgba[i+2] = uint8(float64(float64(rgba[i+2])*a) + 0.5)
	}
}

// premultiplyU8 is tiny-skia's premultiply_u8.
func premultiplyU8(c, a uint8) uint8 {
	prod := uint32(c)*uint32(a) + 128
	return uint8((prod + (prod >> 8)) >> 8)
}
