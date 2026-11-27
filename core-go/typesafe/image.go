package typesafe

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
)

const (
	// TypeSafe System One image constraints (https://docs.typesafe.ai/go/images.md):
	// Each side must be in [280, 1568] pixels inclusive, and area in [78400, 2458624].
	MinImageSide   = 280
	MaxImageSide   = 1568
	MaxImageBytes  = 16 * 1024 * 1024
)

// IdealRasterScale computes a raster scale factor for a laid-out screen of w×h CSS/canvas pixels
// so its longest side lands near MaxImageSide without needlessly rasterizing giant bitmaps first.
func IdealRasterScale(w, h float64) float64 {
	longest := math.Max(w, h)
	shortest := math.Min(w, h)
	if longest <= 0 || shortest <= 0 {
		return 1.0
	}
	scale := 1.0
	if longest > float64(MaxImageSide) {
		scale = float64(MaxImageSide) / longest
	} else if shortest < float64(MinImageSide) {
		// Upscale small frames modestly if doing so keeps longest <= MaxImageSide.
		up := float64(MinImageSide) / shortest
		if longest*up <= float64(MaxImageSide) {
			scale = up
		}
	}
	if scale < 0.05 {
		scale = 0.05
	}
	if scale > 4.0 {
		scale = 4.0
	}
	return scale
}

// PrepareScreenPNG ensures rawPNG strictly satisfies TypeSafe's 280..1568px side and pixel-area limits
// without stretching or squishing the screen's aspect ratio:
//   - If either dimension exceeds 1568px, it scales down proportionally using area-averaged box/bilinear sampling.
//   - If either dimension is still < 280px (e.g., an ultra-tall mobile landing page scaled to height 1568),
//     it centers the intact screen image on a neutral matte canvas padded to >= 280px.
func PrepareScreenPNG(rawPNG []byte) ([]byte, int, int, error) {
	if len(rawPNG) == 0 {
		return nil, 0, 0, errors.New("empty screen image")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(rawPNG))
	if err == nil &&
		cfg.Width >= MinImageSide && cfg.Width <= MaxImageSide &&
		cfg.Height >= MinImageSide && cfg.Height <= MaxImageSide &&
		len(rawPNG) <= MaxImageBytes {
		return rawPNG, cfg.Width, cfg.Height, nil
	}

	src, _, err := image.Decode(bytes.NewReader(rawPNG))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode screen image: %w", err)
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return nil, 0, 0, errors.New("screen image has zero dimensions")
	}

	// Step 1: Fit within MaxImageSide preserving aspect ratio (upscaling tiny frames if both fit <= MaxImageSide).
	tw, th := sw, sh
	longest := max(tw, th)
	shortest := min(tw, th)
	if longest > MaxImageSide {
		ratio := float64(MaxImageSide) / float64(longest)
		tw = max(1, min(MaxImageSide, int(math.Round(float64(sw)*ratio))))
		th = max(1, min(MaxImageSide, int(math.Round(float64(sh)*ratio))))
	} else if shortest < MinImageSide {
		ratio := float64(MinImageSide) / float64(shortest)
		if float64(longest)*ratio <= float64(MaxImageSide) {
			tw = max(MinImageSide, min(MaxImageSide, int(math.Round(float64(sw)*ratio))))
			th = max(MinImageSide, min(MaxImageSide, int(math.Round(float64(sh)*ratio))))
		}
	}

	var scaled *image.NRGBA
	if tw == sw && th == sh {
		scaled = toNRGBA(src)
	} else {
		scaled = resizeBilinear(src, tw, th)
	}

	// Step 2: If one side is still < MinImageSide (extreme aspect ratio > 5.6:1), pad cleanly on a neutral matte
	// so the UI pixels are never distorted and TypeSafe's 280px floor is strictly met.
	fw := max(tw, MinImageSide)
	fh := max(th, MinImageSide)
	finalImg := scaled
	if fw != tw || fh != th {
		padded := image.NewNRGBA(image.Rect(0, 0, fw, fh))
		matte := sampleEdgeMatte(scaled)
		draw.Draw(padded, padded.Bounds(), &image.Uniform{C: matte}, image.Point{}, draw.Src)
		ox := (fw - tw) / 2
		oy := (fh - th) / 2
		draw.Draw(padded, image.Rect(ox, oy, ox+tw, oy+th), scaled, image.Point{}, draw.Src)
		finalImg = padded
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, finalImg); err != nil {
		return nil, 0, 0, fmt.Errorf("encode prepared PNG: %w", err)
	}
	out := buf.Bytes()
	if len(out) > MaxImageBytes {
		return nil, 0, 0, fmt.Errorf("prepared screen PNG (%d bytes) exceeds 16 MiB limit", len(out))
	}
	return out, fw, fh, nil
}

func toNRGBA(src image.Image) *image.NRGBA {
	if n, ok := src.(*image.NRGBA); ok && n.Bounds().Min == (image.Point{}) {
		return n
	}
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

// sampleEdgeMatte picks a subtle contrasting neutral backdrop for letterbox/pillarbox padding based on corner luminance.
func sampleEdgeMatte(img *image.NRGBA) color.NRGBA {
	b := img.Bounds()
	if b.Empty() {
		return color.NRGBA{R: 24, G: 24, B: 28, A: 255}
	}
	pts := [][2]int{
		{b.Min.X, b.Min.Y},
		{b.Max.X - 1, b.Min.Y},
		{b.Min.X, b.Max.Y - 1},
		{b.Max.X - 1, b.Max.Y - 1},
	}
	var lumSum float64
	for _, p := range pts {
		c := img.NRGBAAt(p[0], p[1])
		lumSum += 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	}
	avgLum := lumSum / float64(len(pts))
	if avgLum > 128 {
		return color.NRGBA{R: 232, G: 234, B: 238, A: 255}
	}
	return color.NRGBA{R: 22, G: 23, B: 27, A: 255}
}

// resizeBilinear performs area-aware box/bilinear resampling in pure Go.
func resizeBilinear(src image.Image, dstW, dstH int) *image.NRGBA {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	srcN := toNRGBA(src)
	dst := image.NewNRGBA(image.Rect(0, 0, dstW, dstH))

	scaleX := float64(sw) / float64(dstW)
	scaleY := float64(sh) / float64(dstH)

	// When downscaling by > 1.5x, box-filter over the source footprint so fine 1px UI strokes & glyphs stay crisp
	// instead of aliasing away.
	if scaleX > 1.25 || scaleY > 1.25 {
		for y := 0; y < dstH; y++ {
			sy0 := float64(y) * scaleY
			sy1 := math.Min(float64(sh), float64(y+1)*scaleY)
			iy0 := int(math.Floor(sy0))
			iy1 := min(sh-1, int(math.Ceil(sy1))-1)
			for x := 0; x < dstW; x++ {
				sx0 := float64(x) * scaleX
				sx1 := math.Min(float64(sw), float64(x+1)*scaleX)
				ix0 := int(math.Floor(sx0))
				ix1 := min(sw-1, int(math.Ceil(sx1))-1)

				var rSum, gSum, bSum, aSum, wSum float64
				for sy := iy0; sy <= iy1; sy++ {
					wy := math.Min(float64(sy+1), sy1) - math.Max(float64(sy), sy0)
					if wy <= 0 {
						wy = 1
					}
					rowOff := sy * srcN.Stride
					for sx := ix0; sx <= ix1; sx++ {
						wx := math.Min(float64(sx+1), sx1) - math.Max(float64(sx), sx0)
						if wx <= 0 {
							wx = 1
						}
						w := wx * wy
						off := rowOff + sx*4
						rSum += float64(srcN.Pix[off]) * w
						gSum += float64(srcN.Pix[off+1]) * w
						bSum += float64(srcN.Pix[off+2]) * w
						aSum += float64(srcN.Pix[off+3]) * w
						wSum += w
					}
				}
				if wSum <= 0 {
					wSum = 1
				}
				dOff := y*dst.Stride + x*4
				dst.Pix[dOff] = uint8(math.Round(rSum / wSum))
				dst.Pix[dOff+1] = uint8(math.Round(gSum / wSum))
				dst.Pix[dOff+2] = uint8(math.Round(bSum / wSum))
				dst.Pix[dOff+3] = uint8(math.Round(aSum / wSum))
			}
		}
		return dst
	}

	// Bilinear interpolation for mild scale changes or upscaling.
	for y := 0; y < dstH; y++ {
		fy := (float64(y)+0.5)*scaleY - 0.5
		if fy < 0 {
			fy = 0
		}
		y0 := int(math.Floor(fy))
		y1 := min(y0+1, sh-1)
		wy := fy - float64(y0)
		for x := 0; x < dstW; x++ {
			fx := (float64(x)+0.5)*scaleX - 0.5
			if fx < 0 {
				fx = 0
			}
			x0 := int(math.Floor(fx))
			x1 := min(x0+1, sw-1)
			wx := fx - float64(x0)

			c00 := srcN.NRGBAAt(x0, y0)
			c10 := srcN.NRGBAAt(x1, y0)
			c01 := srcN.NRGBAAt(x0, y1)
			c11 := srcN.NRGBAAt(x1, y1)

			lerp := func(a, b, c, d uint8) uint8 {
				top := float64(a)*(1-wx) + float64(b)*wx
				bot := float64(c)*(1-wx) + float64(d)*wx
				return uint8(math.Round(top*(1-wy) + bot*wy))
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: lerp(c00.R, c10.R, c01.R, c11.R),
				G: lerp(c00.G, c10.G, c01.G, c11.G),
				B: lerp(c00.B, c10.B, c01.B, c11.B),
				A: lerp(c00.A, c10.A, c01.A, c11.A),
			})
		}
	}
	return dst
}
