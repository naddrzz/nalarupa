package resvg

import (
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Ports of filter/color_matrix.rs, component_transfer.rs, composite.rs, convolve_matrix.rs,
// displacement_map.rs and morphology.rs.

func normalized(c byte) float32 { return float32(c) / 255.0 }

func fromNormalized(c float32) uint8 { return asU8(float32(f32Bound(0, c, 1) * 255)) }

// dot3 is a*x + b*y + c*z evaluated left to right, without fused multiply-adds.
func dot3(a, x, b, y, c, z float32) float32 { return float32(a*x) + float32(b*y) + float32(c*z) }

// colorMatrix applies a color matrix to unpremultiplied pixels.
func colorMatrix(kind usvg.ColorMatrixKind, src imageRef) {
	d := src.data
	switch m := kind.(type) {
	case usvg.ColorMatrixMatrix:
		for i := 0; i < len(d); i += 4 {
			r, g, b, a := normalized(d[i]), normalized(d[i+1]), normalized(d[i+2]), normalized(d[i+3])
			newR := dot3(r, m[0], g, m[1], b, m[2]) + float32(a*m[3]) + m[4]
			newG := dot3(r, m[5], g, m[6], b, m[7]) + float32(a*m[8]) + m[9]
			newB := dot3(r, m[10], g, m[11], b, m[12]) + float32(a*m[13]) + m[14]
			newA := dot3(r, m[15], g, m[16], b, m[17]) + float32(a*m[18]) + m[19]
			d[i], d[i+1], d[i+2], d[i+3] = fromNormalized(newR), fromNormalized(newG), fromNormalized(newB), fromNormalized(newA)
		}
	case usvg.ColorMatrixSaturate:
		v := max(float32(m), 0)
		mat := [9]float32{
			0.213 + float32(0.787*v),
			0.715 - float32(0.715*v),
			0.072 - float32(0.072*v),
			0.213 - float32(0.213*v),
			0.715 + float32(0.285*v),
			0.072 - float32(0.072*v),
			0.213 - float32(0.213*v),
			0.715 - float32(0.715*v),
			0.072 + float32(0.928*v),
		}
		colorMatrix3(mat, d)
	case usvg.ColorMatrixHueRotate:
		angle := toRadians(float32(m))
		a1 := cos32(angle)
		a2 := sin32(angle)
		mat := [9]float32{
			0.213 + float32(0.787*a1) - float32(0.213*a2),
			0.715 - float32(0.715*a1) - float32(0.715*a2),
			0.072 - float32(0.072*a1) + float32(0.928*a2),
			0.213 - float32(0.213*a1) + float32(0.143*a2),
			0.715 + float32(0.285*a1) + float32(0.140*a2),
			0.072 - float32(0.072*a1) - float32(0.283*a2),
			0.213 - float32(0.213*a1) - float32(0.787*a2),
			0.715 - float32(0.715*a1) + float32(0.715*a2),
			0.072 + float32(0.928*a1) + float32(0.072*a2),
		}
		colorMatrix3(mat, d)
	case usvg.ColorMatrixLuminanceToAlpha:
		for i := 0; i < len(d); i += 4 {
			r, g, b := normalized(d[i]), normalized(d[i+1]), normalized(d[i+2])
			newA := dot3(r, 0.2125, g, 0.7154, b, 0.0721)
			d[i], d[i+1], d[i+2], d[i+3] = 0, 0, 0, fromNormalized(newA)
		}
	}
}

func colorMatrix3(m [9]float32, d []byte) {
	for i := 0; i < len(d); i += 4 {
		r, g, b := normalized(d[i]), normalized(d[i+1]), normalized(d[i+2])
		d[i] = fromNormalized(dot3(r, m[0], g, m[1], b, m[2]))
		d[i+1] = fromNormalized(dot3(r, m[3], g, m[4], b, m[5]))
		d[i+2] = fromNormalized(dot3(r, m[6], g, m[7], b, m[8]))
	}
}

// componentTransfer applies the transfer functions to each channel of unpremultiplied pixels.
func componentTransfer(fe *usvg.FeComponentTransfer, src imageRef) {
	d := src.data
	for i := 0; i < len(d); i += 4 {
		if !isDummyTransfer(fe.FuncR()) {
			d[i] = transfer(fe.FuncR(), d[i])
		}
		if !isDummyTransfer(fe.FuncB()) {
			d[i+2] = transfer(fe.FuncB(), d[i+2])
		}
		if !isDummyTransfer(fe.FuncG()) {
			d[i+1] = transfer(fe.FuncG(), d[i+1])
		}
		if !isDummyTransfer(fe.FuncA()) {
			d[i+3] = transfer(fe.FuncA(), d[i+3])
		}
	}
}

func isDummyTransfer(f usvg.TransferFunction) bool {
	switch f := f.(type) {
	case usvg.TransferIdentity:
		return true
	case usvg.TransferTable:
		return len(f) == 0
	case usvg.TransferDiscrete:
		return len(f) == 0
	}
	return false
}

func transfer(f usvg.TransferFunction, c8 uint8) uint8 {
	c := float32(c8) / 255.0
	switch f := f.(type) {
	case usvg.TransferTable:
		n := len(f) - 1
		k := min(asUsize(floor32(float32(c*float32(n)))), n)
		if k == n {
			c = f[k]
		} else {
			vk := f[k]
			vk1 := f[k+1]
			kf := float32(k)
			nf := float32(n)
			c = vk + float32(float32((c-kf/nf)*nf)*(vk1-vk))
		}
	case usvg.TransferDiscrete:
		n := len(f)
		k := asUsize(floor32(float32(c * float32(n))))
		c = f[min(k, n-1)]
	case usvg.TransferLinear:
		c = float32(f.Slope*c) + f.Intercept
	case usvg.TransferGamma:
		c = float32(f.Amplitude*pow32(c, f.Exponent)) + f.Offset
	}
	return asU8(float32(f32Bound(0, c, 1) * 255))
}

// compositeArithmetic performs an arithmetic composition of premultiplied pixels.
func compositeArithmetic(k1, k2, k3, k4 float32, src1, src2, dest imageRef) {
	if !(src1.width == src2.width && src1.width == dest.width) || !(src1.height == src2.height && src1.height == dest.height) {
		panic("resvg: arithmetic composite sizes") // Rust asserts
	}
	calc := func(i1, i2 uint8, max float32) float32 {
		a := float32(i1) / 255.0
		b := float32(i2) / 255.0
		result := float32(float32(k1*a)*b) + float32(k2*a) + float32(k3*b) + k4
		return f32Bound(0, result, max)
	}
	s1, s2, d := src1.data, src2.data, dest.data
	for i := 0; i < len(s1) && i < len(s2); i += 4 {
		a := calc(s1[i+3], s2[i+3], 1)
		if approxZeroUlps32(a, 4) {
			continue
		}
		d[i] = asU8(float32(calc(s1[i], s2[i], a) * 255))
		d[i+1] = asU8(float32(calc(s1[i+1], s2[i+1], a) * 255))
		d[i+2] = asU8(float32(calc(s1[i+2], s2[i+2], a) * 255))
		d[i+3] = asU8(float32(a * 255))
	}
}

// convolveMatrix applies a convolve matrix; pixels are premultiplied unless the alpha is preserved.
func convolveMatrix(fe *usvg.FeConvolveMatrix, src imageRef) {
	bound := func(lo, v, hi int32) int32 { return max(lo, min(hi, v)) }
	matrix := fe.Matrix()
	widthMax := int32(src.width) - 1
	heightMax := int32(src.height) - 1

	buf := make([]byte, len(src.data))
	var x, y uint32
	for p := 0; p < len(src.data); p += 4 {
		var newR, newG, newB, newA float32
		for oy := uint32(0); oy < matrix.Rows(); oy++ {
			for ox := uint32(0); ox < matrix.Columns(); ox++ {
				tx := int32(x) - int32(matrix.TargetX()) + int32(ox)
				ty := int32(y) - int32(matrix.TargetY()) + int32(oy)

				switch fe.EdgeMode() {
				case usvg.EdgeModeNone:
					if tx < 0 || tx > widthMax || ty < 0 || ty > heightMax {
						continue
					}
				case usvg.EdgeModeDuplicate:
					tx = bound(0, tx, widthMax)
					ty = bound(0, ty, heightMax)
				case usvg.EdgeModeWrap:
					for tx < 0 {
						tx += int32(src.width)
					}
					tx %= int32(src.width)
					for ty < 0 {
						ty += int32(src.height)
					}
					ty %= int32(src.height)
				}

				k := matrix.Get(matrix.Columns()-ox-1, matrix.Rows()-oy-1)
				px := src.at(uint32(tx), uint32(ty))
				newR += float32(float32(px[0]) / 255.0 * k)
				newG += float32(float32(px[1]) / 255.0 * k)
				newB += float32(float32(px[2]) / 255.0 * k)
				if !fe.PreserveAlpha() {
					newA += float32(float32(px[3]) / 255.0 * k)
				}
			}
		}

		if fe.PreserveAlpha() {
			newA = float32(src.data[p+3]) / 255.0
		} else {
			newA = newA/fe.Divisor() + fe.Bias()
		}
		boundedNewA := f32Bound(0, newA, 1)

		calc := func(v float32) uint8 {
			v = v/fe.Divisor() + float32(fe.Bias()*newA)
			if fe.PreserveAlpha() {
				v = float32(f32Bound(0, v, 1) * boundedNewA)
			} else {
				v = f32Bound(0, v, boundedNewA)
			}
			return asU8(float32(v*255) + 0.5)
		}

		buf[p] = calc(newR)
		buf[p+1] = calc(newG)
		buf[p+2] = calc(newB)
		buf[p+3] = asU8(float32(boundedNewA*255) + 0.5)

		x++
		if x == src.width {
			x = 0
			y++
		}
	}
	copy(src.data, buf)
}

// displacementMap applies a displacement map: map pixels are unpremultiplied, src pixels can be either;
// sx and sy are the canvas scale.
func displacementMap(fe *usvg.FeDisplacementMap, sx, sy float32, src, mapImg, dest imageRef) {
	if !(src.width == mapImg.width && src.width == dest.width) || !(src.height == mapImg.height && src.height == dest.height) {
		panic("resvg: displacement map sizes") // Rust asserts
	}
	w := int32(src.width)
	h := int32(src.height)
	offset := func(px []byte, ch usvg.ColorChannel) float32 {
		var c byte
		switch ch {
		case usvg.ColorChannelB:
			c = px[2]
		case usvg.ColorChannelG:
			c = px[1]
		case usvg.ColorChannelR:
			c = px[0]
		case usvg.ColorChannelA:
			c = px[3]
		}
		return float32(c)/255.0 - 0.5
	}

	var x, y uint32
	for p := 0; p < len(mapImg.data); p += 4 {
		px := mapImg.data[p : p+4]
		dx := offset(px, fe.XChannelSelector())
		dy := offset(px, fe.YChannelSelector())
		ox := asI32(round32(float32(x) + float32(float32(dx*sx)*fe.Scale())))
		oy := asI32(round32(float32(y) + float32(float32(dy*sy)*fe.Scale())))

		// TODO upstream: some kind of anti-aliasing when the offset is on a pixel border.
		if x < uint32(w) && y < uint32(h) && ox >= 0 && ox < w && oy >= 0 && oy < h {
			idx := int(oy*w+ox) * 4
			idx1 := int(y*uint32(w)+x) * 4
			copy(dest.data[idx1:idx1+4], src.data[idx:idx+4])
		}

		x++
		if x == src.width {
			x = 0
			y++
		}
	}
}

// morphology applies an erode or dilate to premultiplied pixels.
func morphology(op usvg.MorphologyOperator, rx, ry float32, src imageRef) {
	// No point in making matrix larger than image.
	columns := min(asU32(ceil32(rx))*2, src.width)
	rows := min(asU32(ceil32(ry))*2, src.height)
	targetX := asU32(floor32(float32(columns) / 2.0))
	targetY := asU32(floor32(float32(rows) / 2.0))

	widthMax := int32(src.width) - 1
	heightMax := int32(src.height) - 1

	erode := op == usvg.MorphologyErode
	buf := make([]byte, len(src.data))
	var x, y uint32
	for p := 0; p < len(src.data); p += 4 {
		var newP [4]byte
		if erode {
			newP = [4]byte{255, 255, 255, 255}
		}
		for oy := uint32(0); oy < rows; oy++ {
			for ox := uint32(0); ox < columns; ox++ {
				tx := int32(x) - int32(targetX) + int32(ox)
				ty := int32(y) - int32(targetY) + int32(oy)
				if tx < 0 || tx > widthMax || ty < 0 || ty > heightMax {
					continue
				}
				px := src.at(uint32(tx), uint32(ty))
				for c := range 4 {
					if erode {
						newP[c] = min(px[c], newP[c])
					} else {
						newP[c] = max(px[c], newP[c])
					}
				}
			}
		}
		copy(buf[p:p+4], newP[:])

		x++
		if x == src.width {
			x = 0
			y++
		}
	}
	copy(src.data, buf)
}
