package resvg

import "math"

// Ports of filter/box_blur.rs (based on https://github.com/fschutt/fastblur) and filter/iir_blur.rs.

const boxBlurSteps = 5

// boxBlur applies a box blur to premultiplied pixels. A zero sigma disables the blur along that axis.
func boxBlur(sigmaX, sigmaY float64, src imageRef) {
	boxesHorz := createBoxGauss(float32(sigmaX))
	boxesVert := createBoxGauss(float32(sigmaY))
	backbuf := imageRef{append([]byte(nil), src.data...), src.width, src.height}

	for i := range boxesHorz {
		radiusHorz := int((boxesHorz[i] - 1) / 2)
		radiusVert := int((boxesVert[i] - 1) / 2)
		boxBlurVert(radiusVert, src, backbuf)
		boxBlurHorz(radiusHorz, backbuf, src)
	}
}

func createBoxGauss(sigma float32) [boxBlurSteps]int32 {
	var sizes [boxBlurSteps]int32
	if !(sigma > 0) {
		for i := range sizes {
			sizes[i] = 1
		}
		return sizes
	}
	const nFloat = float32(boxBlurSteps)

	// Ideal averaging filter width.
	wIdeal := sqrt32(float32(float32(12*sigma)*sigma)/nFloat) + 1
	wl := asI32(floor32(wIdeal))
	if wl%2 == 0 {
		wl--
	}
	wu := wl + 2

	wlFloat := float32(wl)
	mIdeal := (float32(float32(12*sigma)*sigma) - float32(float32(nFloat*wlFloat)*wlFloat) -
		float32(float32(4*nFloat)*wlFloat) - float32(3*nFloat)) / (float32(-4*wlFloat) - 4)
	m := asUsize(round32(mIdeal))

	for i := range sizes {
		if i < m {
			sizes[i] = wl
		} else {
			sizes[i] = wu
		}
	}
	return sizes
}

// boxRound is box_blur.rs `round`: fast rounding for x <= 2^23.
func boxRound(x float32) float32 {
	x = float32(x + 12582912.0)
	x = float32(x - 12582912.0)
	return x
}

// boxStore writes the running sums divided by the box size at index i (in pixels).
func boxStore(dst []byte, i int, r, g, b, a int, iarr float32) {
	o := i * 4
	dst[o] = asU8(boxRound(float32(float32(r) * iarr)))
	dst[o+1] = asU8(boxRound(float32(float32(g) * iarr)))
	dst[o+2] = asU8(boxRound(float32(float32(b) * iarr)))
	dst[o+3] = asU8(boxRound(float32(float32(a) * iarr)))
}

// boxBlurVert blurs the columns of backbuf into frontbuf. Pixels beyond the edges are transparent.
func boxBlurVert(blurRadius int, backbuf, frontbuf imageRef) {
	if blurRadius == 0 {
		copy(frontbuf.data, backbuf.data)
		return
	}
	width := int(backbuf.width)
	height := int(backbuf.height)
	src := backbuf.data

	iarr := 1 / float32(blurRadius+blurRadius+1)

	for i := 0; i < width; i++ {
		colEnd := i + width*(height-1) // inclusive
		ti := i
		li := ti
		ri := ti + blurRadius*width

		var valR, valG, valB, valA int
		for j := 0; j < min(blurRadius, height); j++ {
			o := (ti + j*width) * 4
			valR += int(src[o])
			valG += int(src[o+1])
			valB += int(src[o+2])
			valA += int(src[o+3])
		}

		for range min(height, blurRadius+1) {
			if ri <= colEnd {
				o := ri * 4
				valR += int(src[o])
				valG += int(src[o+1])
				valB += int(src[o+2])
				valA += int(src[o+3])
			}
			ri += width
			boxStore(frontbuf.data, ti, valR, valG, valB, valA, iarr)
			ti += width
		}

		if height <= blurRadius {
			continue
		}

		for range height - blurRadius - (blurRadius + 1) {
			o1 := ri * 4
			ri += width
			o2 := li * 4
			li += width
			valR += int(src[o1]) - int(src[o2])
			valG += int(src[o1+1]) - int(src[o2+1])
			valB += int(src[o1+2]) - int(src[o2+2])
			valA += int(src[o1+3]) - int(src[o2+3])
			boxStore(frontbuf.data, ti, valR, valG, valB, valA, iarr)
			ti += width
		}

		for range min(height-blurRadius-1, blurRadius) {
			if li >= i {
				o := li * 4
				valR -= int(src[o])
				valG -= int(src[o+1])
				valB -= int(src[o+2])
				valA -= int(src[o+3])
			}
			li += width
			boxStore(frontbuf.data, ti, valR, valG, valB, valA, iarr)
			ti += width
		}
	}
}

// boxBlurHorz blurs the rows of backbuf into frontbuf. Pixels beyond the edges are transparent.
func boxBlurHorz(blurRadius int, backbuf, frontbuf imageRef) {
	if blurRadius == 0 {
		copy(frontbuf.data, backbuf.data)
		return
	}
	width := int(backbuf.width)
	height := int(backbuf.height)
	src := backbuf.data

	iarr := 1 / float32(blurRadius+blurRadius+1)

	for i := 0; i < height; i++ {
		rowStart := i * width     // inclusive
		rowEnd := (i+1)*width - 1 // inclusive
		ti := i * width
		li := ti
		ri := ti + blurRadius

		var valR, valG, valB, valA int
		for j := 0; j < min(blurRadius, width); j++ {
			o := (ti + j) * 4
			valR += int(src[o])
			valG += int(src[o+1])
			valB += int(src[o+2])
			valA += int(src[o+3])
		}

		// The left side, where pixels beyond the left edge are needed.
		for range min(width, blurRadius+1) {
			if ri <= rowEnd {
				o := ri * 4
				valR += int(src[o])
				valG += int(src[o+1])
				valB += int(src[o+2])
				valA += int(src[o+3])
			}
			ri++
			boxStore(frontbuf.data, ti, valR, valG, valB, valA, iarr)
			ti++
		}

		if width <= blurRadius {
			continue
		}

		// The middle, which never reaches a border.
		for range width - blurRadius - (blurRadius + 1) {
			o1 := ri * 4
			ri++
			o2 := li * 4
			li++
			valR += int(src[o1]) - int(src[o2])
			valG += int(src[o1+1]) - int(src[o2+1])
			valB += int(src[o1+2]) - int(src[o2+2])
			valA += int(src[o1+3]) - int(src[o2+3])
			boxStore(frontbuf.data, ti, valR, valG, valB, valA, iarr)
			ti++
		}

		// The right side, where pixels beyond the right edge are needed.
		for range min(width-blurRadius-1, blurRadius) {
			if li >= rowStart {
				o := li * 4
				valR -= int(src[o])
				valG -= int(src[o+1])
				valB -= int(src[o+2])
				valA -= int(src[o+3])
			}
			li++
			boxStore(frontbuf.data, ti, valR, valG, valB, valA, iarr)
			ti++
		}
	}
}

// iirBlur applies an IIR blur (Alvarez and Mazorra's fast Gaussian convolution, after
// http://www.getreuer.info/home/gaussianiir) to premultiplied pixels. A zero sigma disables the blur along
// that axis.
func iirBlur(sigmaX, sigmaY float64, src imageRef) {
	buf := make([]float64, int(src.width)*int(src.height))
	d := iirData{int(src.width), int(src.height), sigmaX, sigmaY, 4}
	for channel := range 4 {
		iirChannel(src.data, &d, channel, buf)
	}
}

type iirData struct {
	width, height  int
	sigmaX, sigmaY float64
	steps          int
}

func iirChannel(data []byte, d *iirData, channel int, buf []float64) {
	for i := range len(data) / 4 {
		buf[i] = float64(data[i*4+channel]) / 255.0
	}
	gaussianIIR2D(d, buf)
	for i := range len(data) / 4 {
		data[i*4+channel] = asU8F64(float64(buf[i] * 255.0))
	}
}

func gaussianIIR2D(d *iirData, buf []float64) {
	// Filter horizontally along each row.
	lambdaX, dnuX := 1.0, 1.0
	if d.sigmaX > 0 {
		lambda, dnu := iirCoefficients(d.sigmaX, d.steps)
		for y := 0; y < d.height; y++ {
			for range d.steps {
				idx := d.width * y
				// Filter rightwards.
				for x := 1; x < d.width; x++ {
					buf[idx+x] = buf[idx+x] + float64(dnu*buf[idx+x-1])
				}
				// Filter leftwards.
				for x := d.width - 1; x > 0; x-- {
					buf[idx+x-1] = buf[idx+x-1] + float64(dnu*buf[idx+x])
				}
			}
		}
		lambdaX, dnuX = lambda, dnu
	}

	// Filter vertically along each column.
	lambdaY, dnuY := 1.0, 1.0
	if d.sigmaY > 0 {
		lambda, dnu := iirCoefficients(d.sigmaY, d.steps)
		for x := 0; x < d.width; x++ {
			for range d.steps {
				idx := x
				// Filter downwards.
				for y := d.width; y < len(buf); y += d.width {
					buf[idx+y] = buf[idx+y] + float64(dnu*buf[idx+y-d.width])
				}
				// Filter upwards.
				for y := len(buf) - d.width; y > 0; y -= d.width {
					buf[idx+y-d.width] = buf[idx+y-d.width] + float64(dnu*buf[idx+y])
				}
			}
		}
		lambdaY, dnuY = lambda, dnu
	}

	postScale := powi(math.Sqrt(float64(dnuX*dnuY))/math.Sqrt(float64(lambdaX*lambdaY)), 2*d.steps)
	for i := range buf {
		buf[i] = float64(buf[i] * postScale)
	}
}

func iirCoefficients(sigma float64, steps int) (float64, float64) {
	lambda := float64(sigma*sigma) / float64(2.0*float64(steps))
	dnu := (1.0 + float64(2.0*lambda) - math.Sqrt(1.0+float64(4.0*lambda))) / float64(2.0*lambda)
	return lambda, dnu
}

// powi is f64::powi as compiler-rt's __powidf2 computes it: square-and-multiply.
func powi(a float64, b int) float64 {
	recip := b < 0
	r := 1.0
	for {
		if b&1 != 0 {
			r = float64(r * a)
		}
		b /= 2
		if b == 0 {
			break
		}
		a = float64(a * a)
	}
	if recip {
		return 1 / r
	}
	return r
}
