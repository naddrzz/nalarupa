package resvg

import "math"

// Port of filter/turbulence.rs.

const (
	randM    = 2147483647 // 2**31 - 1
	randA    = 16807      // 7**5; primitive root of m
	randQ    = 127773     // m / a
	randR    = 2836       // m % a
	bSize    = 0x100
	bLen     = bSize + bSize + 2
	bm       = 0xff
	perlinN  = 0x1000
	bSize32  = int32(bSize)
	perlinNf = float64(perlinN)
)

type stitchInfo struct {
	width, height int32 // How much to subtract to wrap for stitching.
	wrapX, wrapY  int32 // Minimum value to wrap.
}

// turbulence renders feTurbulence into dest as unpremultiplied pixels. offsetX and offsetY are the
// filter region offset, sx and sy the canvas scale.
func turbulence(offsetX, offsetY, sx, sy, baseFrequencyX, baseFrequencyY float64, numOctaves uint32, seed int32,
	stitchTiles, fractalNoise bool, dest imageRef) {
	latticeSelector, gradient := turbulenceInit(seed)
	width := dest.width
	height := dest.height
	var x, y uint32
	for p := 0; p < len(dest.data); p += 4 {
		turb := func(channel int) uint8 {
			tx, ty := (float64(x)+offsetX)/sx, (float64(y)+offsetY)/sy
			n := turbulenceSum(channel, tx, ty, float64(x), float64(y), float64(width), float64(height),
				baseFrequencyX, baseFrequencyY, numOctaves, fractalNoise, stitchTiles, latticeSelector, gradient)
			if fractalNoise {
				n = (float64(n*255.0) + 255.0) / 2.0
			} else {
				n = float64(n * 255.0)
			}
			return asU8(f32Bound(0, float32(n), 255) + 0.5)
		}
		dest.data[p] = turb(0)
		dest.data[p+1] = turb(1)
		dest.data[p+2] = turb(2)
		dest.data[p+3] = turb(3)

		x++
		if x == dest.width {
			x = 0
			y++
		}
	}
}

func turbulenceInit(seed int32) ([]int, *[4][bLen][2]float64) {
	latticeSelector := make([]int, bLen)
	gradient := new([4][bLen][2]float64)

	if seed <= 0 {
		seed = -seed%(randM-1) + 1
	}
	if seed > randM-1 {
		seed = randM - 1
	}

	for k := range 4 {
		for i := range bSize {
			latticeSelector[i] = i
			for j := range 2 {
				seed = turbulenceRandom(seed)
				gradient[k][i][j] = float64((seed%(bSize32+bSize32))-bSize32) / float64(bSize32)
			}
			g := &gradient[k][i]
			s := math.Sqrt(float64(g[0]*g[0]) + float64(g[1]*g[1]))
			g[0] /= s
			g[1] /= s
		}
	}

	for i := bSize - 1; i >= 1; i-- {
		k := latticeSelector[i]
		seed = turbulenceRandom(seed)
		j := int(seed % bSize32)
		latticeSelector[i] = latticeSelector[j]
		latticeSelector[j] = k
	}

	for i := range bSize + 2 {
		latticeSelector[bSize+i] = latticeSelector[i]
		for g := range 4 {
			gradient[g][bSize+i] = gradient[g][i]
		}
	}
	return latticeSelector, gradient
}

func turbulenceSum(colorChannel int, x, y, tileX, tileY, tileWidth, tileHeight, baseFreqX, baseFreqY float64,
	numOctaves uint32, fractalSum, doStitching bool, latticeSelector []int, gradient *[4][bLen][2]float64) float64 {
	// Adjust the base frequencies if necessary for stitching.
	var stitch *stitchInfo
	if doStitching {
		// When stitching tiled turbulence, the frequencies must be adjusted so that the tile borders will be
		// continuous.
		if !approxZeroUlps64(baseFreqX, 4) {
			loFreq := math.Floor(float64(tileWidth*baseFreqX)) / tileWidth
			hiFreq := math.Ceil(float64(tileWidth*baseFreqX)) / tileWidth
			if baseFreqX/loFreq < hiFreq/baseFreqX {
				baseFreqX = loFreq
			} else {
				baseFreqX = hiFreq
			}
		}
		if !approxZeroUlps64(baseFreqY, 4) {
			loFreq := math.Floor(float64(tileHeight*baseFreqY)) / tileHeight
			hiFreq := math.Ceil(float64(tileHeight*baseFreqY)) / tileHeight
			if baseFreqY/loFreq < hiFreq/baseFreqY {
				baseFreqY = loFreq
			} else {
				baseFreqY = hiFreq
			}
		}

		// Set up initial stitch values.
		w := asI32F64(float64(tileWidth*baseFreqX) + 0.5)
		h := asI32F64(float64(tileHeight*baseFreqY) + 0.5)
		stitch = &stitchInfo{
			width:  w,
			height: h,
			wrapX:  asI32F64(float64(tileX*baseFreqX) + perlinNf + float64(w)),
			wrapY:  asI32F64(float64(tileY*baseFreqY) + perlinNf + float64(h)),
		}
	}

	sum := 0.0
	x = float64(x * baseFreqX)
	y = float64(y * baseFreqY)
	ratio := 1.0
	for range numOctaves {
		if fractalSum {
			sum += noise2(colorChannel, x, y, latticeSelector, gradient, stitch) / ratio
		} else {
			sum += math.Abs(noise2(colorChannel, x, y, latticeSelector, gradient, stitch)) / ratio
		}
		x = float64(x * 2.0)
		y = float64(y * 2.0)
		ratio = float64(ratio * 2.0)

		if stitch != nil {
			// Update stitch values. Subtracting PerlinN before the multiplication and adding it afterward
			// simplifies to subtracting it once.
			stitch.width *= 2
			stitch.wrapX = 2*stitch.wrapX - perlinN
			stitch.height *= 2
			stitch.wrapY = 2*stitch.wrapY - perlinN
		}
	}
	return sum
}

func noise2(colorChannel int, x, y float64, latticeSelector []int, gradient *[4][bLen][2]float64, stitch *stitchInfo) float64 {
	t := x + perlinNf
	bx0 := asI32F64(t)
	bx1 := bx0 + 1
	rx0 := t - float64(asI64F64(t))
	rx1 := rx0 - 1.0
	t = y + perlinNf
	by0 := asI32F64(t)
	by1 := by0 + 1
	ry0 := t - float64(asI64F64(t))
	ry1 := ry0 - 1.0

	// If stitching, adjust lattice points accordingly.
	if stitch != nil {
		if bx0 >= stitch.wrapX {
			bx0 -= stitch.width
		}
		if bx1 >= stitch.wrapX {
			bx1 -= stitch.width
		}
		if by0 >= stitch.wrapY {
			by0 -= stitch.height
		}
		if by1 >= stitch.wrapY {
			by1 -= stitch.height
		}
	}

	bx0 &= bm
	bx1 &= bm
	by0 &= bm
	by1 &= bm
	i := latticeSelector[bx0]
	j := latticeSelector[bx1]
	b00 := latticeSelector[i+int(by0)]
	b10 := latticeSelector[j+int(by0)]
	b01 := latticeSelector[i+int(by1)]
	b11 := latticeSelector[j+int(by1)]
	sx := sCurve(rx0)
	sy := sCurve(ry0)
	g := &gradient[colorChannel]
	q := g[b00]
	u := float64(rx0*q[0]) + float64(ry0*q[1])
	q = g[b10]
	v := float64(rx1*q[0]) + float64(ry0*q[1])
	a := lerp(sx, u, v)
	q = g[b01]
	u = float64(rx0*q[0]) + float64(ry1*q[1])
	q = g[b11]
	v = float64(rx1*q[0]) + float64(ry1*q[1])
	b := lerp(sx, u, v)
	return lerp(sy, a, b)
}

func turbulenceRandom(seed int32) int32 {
	result := randA*(seed%randQ) - randR*(seed/randQ)
	if result <= 0 {
		result += randM
	}
	return result
}

func sCurve(t float64) float64 { return float64(float64(t*t) * (3.0 - float64(2.0*t))) }

func lerp(t, a, b float64) float64 { return a + float64(t*(b-a)) }
