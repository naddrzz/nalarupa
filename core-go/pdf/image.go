package pdf

// Raster images as krilla embeds them (graphics/image.rs): JPEG passed through as DCT, everything else as
// Flate-compressed samples in DeviceRGB or DeviceGray with 8 or 16 bits per component, and any alpha as
// a soft mask image. Unlike krilla, which decodes late and fails the whole document on a damaged image,
// images are decoded when drawn and a damaged one is left out.

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	stdimage "image"
	"image/color"
	"image/jpeg"
	"image/png"

	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

type image struct {
	key        [32]byte
	w, h       int
	cs         string // DeviceRGB, DeviceGray or DeviceCMYK
	bpc        int
	jpeg       bool   // data is a JPEG file
	invertCMYK bool   // Adobe CMYK JPEGs store inverted values
	data       []byte // raw samples, or the JPEG file
	alpha      []byte // raw alpha samples, or nil
}

func imageKey(kind string, data []byte) [32]byte {
	return sha256.Sum256(append([]byte(kind+"\x00"), data...))
}

// decodeImage reads a raster usvg image; false when it does not decode.
func decodeImage(kind usvg.ImageKind) (*image, bool) {
	switch data := kind.(type) {
	case usvg.ImageKindJPEG:
		return decodeJPEG(data)
	case usvg.ImageKindPNG:
		return decodePNG(data)
	case usvg.ImageKindGIF, usvg.ImageKindWEBP:
		rgba, w, h, alpha, ok := resvg.DecodeRGBA(kind)
		if !ok || w <= 0 || h <= 0 {
			return nil, false
		}
		img := fromRGBA8(rgba, w, h, alpha)
		name := "gif"
		if _, ok := kind.(usvg.ImageKindWEBP); ok {
			name = "webp"
		}
		img.key = imageKey(fmt.Sprintf("%s %dx%d", name, w, h), rgba)
		return img, true
	}
	return nil, false
}

// fromRGBA8 is handle_u8_image on RGBA: the color samples, and the alpha only when it is not all opaque.
func fromRGBA8(rgba []byte, w, h int, alpha bool) *image {
	img := &image{w: w, h: h, cs: "DeviceRGB", bpc: 8}
	img.data = make([]byte, 0, w*h*3)
	var as []byte
	opaque := true
	for i := 0; i+3 < len(rgba); i += 4 {
		img.data = append(img.data, rgba[i], rgba[i+1], rgba[i+2])
		if alpha {
			as = append(as, rgba[i+3])
			opaque = opaque && rgba[i+3] == 255
		}
	}
	if alpha && !opaque {
		img.alpha = as
	}
	return img
}

func decodeJPEG(data []byte) (*image, bool) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, false
	}
	img := &image{key: imageKey("jpeg", data), w: cfg.Width, h: cfg.Height, bpc: 8, jpeg: true, data: data}
	switch cfg.ColorModel {
	case color.GrayModel:
		img.cs = "DeviceGray"
	case color.CMYKModel:
		img.cs, img.invertCMYK = "DeviceCMYK", true
	default:
		img.cs = "DeviceRGB"
	}
	return img, true
}

// decodePNG keeps PNG's bit depth (16 bits stay 16) and channels, palettes expanded to RGB(A).
func decodePNG(data []byte) (*image, bool) {
	m, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, false
	}
	img := &image{key: imageKey("png", data), w: w, h: h, bpc: 8}
	switch p := m.(type) {
	case *stdimage.Gray:
		img.cs = "DeviceGray"
		for y := b.Min.Y; y < b.Max.Y; y++ {
			i := p.PixOffset(b.Min.X, y)
			img.data = append(img.data, p.Pix[i:i+w]...)
		}
	case *stdimage.Gray16:
		img.cs, img.bpc = "DeviceGray", 16
		for y := b.Min.Y; y < b.Max.Y; y++ {
			i := p.PixOffset(b.Min.X, y)
			img.data = append(img.data, p.Pix[i:i+w*2]...)
		}
	case *stdimage.RGBA64, *stdimage.NRGBA64:
		img.cs, img.bpc = "DeviceRGB", 16
		_, hasAlpha := p.(*stdimage.NRGBA64)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBA64Model.Convert(m.At(x, y)).(color.NRGBA64)
				img.data = append(img.data, byte(c.R>>8), byte(c.R), byte(c.G>>8), byte(c.G), byte(c.B>>8), byte(c.B))
				if hasAlpha {
					img.alpha = append(img.alpha, byte(c.A>>8), byte(c.A))
				}
			}
		}
	default:
		// 8-bit RGB, RGBA, gray with alpha and palettes.
		rgba := make([]byte, 0, w*h*4)
		hasAlpha := false
		switch p := m.(type) {
		case *stdimage.RGBA:
		case *stdimage.Paletted:
			for _, c := range p.Palette {
				if _, _, _, a := c.RGBA(); a != 0xffff {
					hasAlpha = true
				}
			}
		default:
			hasAlpha = true
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(m.At(x, y)).(color.NRGBA)
				rgba = append(rgba, c.R, c.G, c.B, c.A)
			}
		}
		key := img.key
		img = fromRGBA8(rgba, w, h, hasAlpha)
		img.key = key
	}
	return img, true
}

// image writes the image XObject (and its soft mask) once per image.
func (d *document) image(img *image) ref {
	if r, ok := d.images[img.key]; ok {
		return r
	}
	var smask ref
	if img.alpha != nil {
		smask = d.w.alloc()
		d.w.stream(smask, "/Type /XObject /Subtype /Image /Width "+itoa(img.w)+" /Height "+itoa(img.h)+
			" /ColorSpace /DeviceGray /BitsPerComponent "+itoa(img.bpc), img.alpha, true)
	}
	r := d.w.alloc()
	dict := "/Type /XObject /Subtype /Image /Width " + itoa(img.w) + " /Height " + itoa(img.h) +
		" /ColorSpace /" + img.cs + " /BitsPerComponent " + itoa(img.bpc)
	if img.invertCMYK {
		dict += " /Decode [1 0 1 0 1 0 1 0]"
	}
	if smask != 0 {
		dict += " /SMask " + smask.String()
	}
	if img.jpeg {
		d.w.rawStream(r, "/Filter /DCTDecode "+dict, img.data)
	} else {
		d.w.stream(r, dict, img.data, true)
	}
	d.images[img.key] = r
	return r
}
