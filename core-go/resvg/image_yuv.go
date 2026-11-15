package resvg

import (
	"encoding/binary"
	"image"
)

// The YCbCr to RGB steps of the decoders Rust uses, applied to the planes the Go decoders produce: the
// chroma upsampling and color conversion of zune-jpeg (upsampler/scalar.rs, color_convert/scalar.rs,
// worker.rs `upsample`) and image-webp (yuv.rs, fancy upsampling). VP8 decoding is exact, so WebP planes
// match image-webp's; Go's JPEG IDCT can differ from zune-jpeg's by up to 3
// levels (see image.go).

// zuneYCbCrToRGBA converts a decoded JPEG as zune-jpeg does: chroma is upsampled over the MCU-padded planes
// (the Go decoder keeps them past the image bounds), with the edge rows and columns of the padding
// repeated.
func zuneYCbCrToRGBA(m *image.YCbCr) []byte {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	cw := m.CStride
	ch := len(m.Cb) / cw
	var hs, vs int // chroma subsampling factors
	switch m.SubsampleRatio {
	case image.YCbCrSubsampleRatio444:
		hs, vs = 1, 1
	case image.YCbCrSubsampleRatio422:
		hs, vs = 2, 1
	case image.YCbCrSubsampleRatio420:
		hs, vs = 2, 2
	case image.YCbCrSubsampleRatio440:
		hs, vs = 1, 2
	case image.YCbCrSubsampleRatio411:
		hs, vs = 4, 1
	case image.YCbCrSubsampleRatio410:
		hs, vs = 4, 2
	}
	cb := zuneUpsample(m.Cb, cw, ch, hs, vs)
	cr := zuneUpsample(m.Cr, cw, ch, hs, vs)
	uw := cw * hs

	out := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := zuneYCbCrToRGB(int32(m.Y[y*m.YStride+x]), int32(cb[y*uw+x]), int32(cr[y*uw+x]))
			o := (y*w + x) * 4
			out[o], out[o+1], out[o+2], out[o+3] = r, g, b, 255
		}
	}
	return out
}

// zuneUpsample upsamples a w x h chroma plane by (hs, vs).
func zuneUpsample(plane []byte, w, h, hs, vs int) []int16 {
	in := make([]int16, w*h)
	for i := range in {
		in[i] = int16(plane[i])
	}
	switch {
	case hs == 1 && vs == 1:
		return in
	case hs == 2 && vs == 1:
		out := make([]int16, w*2*h)
		for y := 0; y < h; y++ {
			zuneUpsampleHorizontal(in[y*w:(y+1)*w], out[y*w*2:(y+1)*w*2])
		}
		return out
	case (hs == 1 || hs == 2) && vs == 2:
		vert := make([]int16, w*h*2)
		for y := 0; y < h; y++ {
			cur := in[y*w : (y+1)*w]
			up, down := cur, cur
			if y > 0 {
				up = in[(y-1)*w : y*w]
			}
			if y < h-1 {
				down = in[(y+1)*w : (y+2)*w]
			}
			top := vert[2*y*w : (2*y+1)*w]
			bottom := vert[(2*y+1)*w : (2*y+2)*w]
			for x := range cur {
				top[x] = (3*cur[x] + 2 + up[x]) >> 2
				bottom[x] = (3*cur[x] + 2 + down[x]) >> 2
			}
		}
		if hs == 1 {
			return vert
		}
		out := make([]int16, w*2*h*2)
		for y := 0; y < h*2; y++ {
			zuneUpsampleHorizontal(vert[y*w:(y+1)*w], out[y*w*2:(y+1)*w*2])
		}
		return out
	}
	// Generic ratios use the nearest sample.
	out := make([]int16, w*hs*h*vs)
	for y := 0; y < h*vs; y++ {
		for x := 0; x < w*hs; x++ {
			out[y*w*hs+x] = in[(y/vs)*w+x/hs]
		}
	}
	return out
}

// zuneUpsampleHorizontal is upsampler/scalar.rs upsample_horizontal.
func zuneUpsampleHorizontal(in, out []int16) {
	if len(in) < 3 {
		for i := range out {
			out[i] = in[i/2]
		}
		return
	}
	out[0] = in[0]
	out[1] = (in[0]*3 + in[1] + 2) >> 2
	for i := 1; i < len(in)-1; i++ {
		sample := 3*in[i] + 2
		out[i*2] = (sample + in[i-1]) >> 2
		out[i*2+1] = (sample + in[i+1]) >> 2
	}
	n, l := len(out), len(in)
	out[n-2] = (3*in[l-1] + in[l-2] + 2) >> 2
	out[n-1] = in[l-1]
}

// zuneYCbCrToRGB is color_convert/scalar.rs ycbcr_to_rgba_inner_16_scalar for one pixel.
func zuneYCbCrToRGB(y, cb, cr int32) (uint8, uint8, uint8) {
	const (
		yCF     = 16384
		crCF    = 22970
		cbCF    = 29032
		gCrCoef = -11700
		gCbCoef = -5638
		prec    = 14
		rnd     = (1 << (prec - 1)) - 1
	)
	cr -= 128
	cb -= 128
	y0 := y*yCF + rnd
	r := (y0 + cr*crCF) >> prec
	g := (y0 + cr*gCrCoef + cb*gCbCoef) >> prec
	b := (y0 + cb*cbCF) >> prec
	return clampU8(r), clampU8(g), clampU8(b)
}

func clampU8(v int32) uint8 { return uint8(min(max(v, 0), 255)) }

// webpFancyRGBA converts a decoded lossy WebP frame as image-webp's fill_rgb_buffer_fancy does; alpha is
// set to 255.
func webpFancyRGBA(m *image.YCbCr) []byte {
	w, h := m.Rect.Dx(), m.Rect.Dy()
	out := make([]byte, w*h*4)
	cw := (w + 1) / 2
	row := func(y int) []byte { return out[y*w*4 : (y+1)*w*4] }
	yRow := func(y int) []byte { return m.Y[y*m.YStride : y*m.YStride+w] }
	cRow := func(p []byte, cy int) []byte { return p[cy*m.CStride : cy*m.CStride+cw] }

	webpRow1(row(0), yRow(0), cRow(m.Cb, 0), cRow(m.Cr, 0))
	y := 1
	for cy := 0; y+1 < h; cy++ {
		u1, u2 := cRow(m.Cb, cy), cRow(m.Cb, cy+1)
		v1, v2 := cRow(m.Cr, cy), cRow(m.Cr, cy+1)
		webpRow2(row(y), yRow(y), u1, u2, v1, v2)
		webpRow2(row(y+1), yRow(y+1), u2, u1, v2, v1)
		y += 2
	}
	if y < h {
		// An even height leaves one final row with only one u/v row.
		last := (h+1)/2 - 1
		webpRow1(row(y), yRow(y), cRow(m.Cb, last), cRow(m.Cr, last))
	}
	return out
}

func webpRow2(out, y, u1, u2, v1, v2 []byte) {
	webpSetPixel(out[0:], y[0], webpFancy(u1[0], u1[0], u2[0], u2[0]), webpFancy(v1[0], v1[0], v2[0], v2[0]))
	i := 1
	for j := 0; i+1 < len(y) && j+1 < len(u1); j++ {
		webpSetPixel(out[i*4:], y[i], webpFancy(u1[j], u1[j+1], u2[j], u2[j+1]), webpFancy(v1[j], v1[j+1], v2[j], v2[j+1]))
		webpSetPixel(out[(i+1)*4:], y[i+1], webpFancy(u1[j+1], u1[j], u2[j+1], u2[j]), webpFancy(v1[j+1], v1[j], v2[j+1], v2[j]))
		i += 2
	}
	if i < len(y) {
		fu1, fu2, fv1, fv2 := u1[len(u1)-1], u2[len(u2)-1], v1[len(v1)-1], v2[len(v2)-1]
		webpSetPixel(out[i*4:], y[i], webpFancy(fu1, fu1, fu2, fu2), webpFancy(fv1, fv1, fv2, fv2))
	}
}

func webpRow1(out, y, u, v []byte) {
	webpSetPixel(out[0:], y[0], u[0], v[0])
	i := 1
	for j := 0; i+1 < len(y) && j+1 < len(u); j++ {
		webpSetPixel(out[i*4:], y[i], webpFancy(u[j], u[j+1], u[j], u[j+1]), webpFancy(v[j], v[j+1], v[j], v[j+1]))
		webpSetPixel(out[(i+1)*4:], y[i+1], webpFancy(u[j+1], u[j], u[j+1], u[j]), webpFancy(v[j+1], v[j], v[j+1], v[j]))
		i += 2
	}
	if i < len(y) {
		webpSetPixel(out[i*4:], y[i], u[len(u)-1], v[len(v)-1])
	}
}

func webpFancy(main, secondary1, secondary2, tertiary uint8) uint8 {
	return uint8((9*uint16(main) + 3*uint16(secondary1) + 3*uint16(secondary2) + uint16(tertiary) + 8) / 16)
}

func webpMulhi(v uint8, coeff uint16) int32 { return int32((uint32(v) * uint32(coeff)) >> 8) }

func webpClip(v int32) uint8 { return uint8(min(max(v>>6, 0), 255)) }

func webpSetPixel(rgb []byte, y, u, v uint8) {
	rgb[0] = webpClip(webpMulhi(y, 19077) + webpMulhi(v, 26149) - 14234)
	rgb[1] = webpClip(webpMulhi(y, 19077) - webpMulhi(u, 6419) - webpMulhi(v, 13320) + 8708)
	rgb[2] = webpClip(webpMulhi(y, 19077) + webpMulhi(u, 33050) - 17685)
	rgb[3] = 255
}

// webpHasAlpha is image-webp's has_alpha: the VP8L header's alpha hint, the VP8X alpha flag, or false for
// a plain VP8 image.
func webpHasAlpha(data []byte) bool {
	if len(data) < 21 {
		return false
	}
	payload := data[20:]
	switch string(data[12:16]) {
	case "VP8L":
		if len(payload) < 5 || payload[0] != 0x2f {
			return false
		}
		return (binary.LittleEndian.Uint32(payload[1:5])>>28)&1 != 0
	case "VP8X":
		return payload[0]&0x10 != 0
	}
	return false
}
