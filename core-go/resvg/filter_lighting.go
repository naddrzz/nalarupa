package resvg

import (
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Port of filter/lighting.rs.

const (
	factor1_2 = float32(1.0 / 2.0)
	factor1_3 = float32(1.0 / 3.0)
	factor1_4 = float32(1.0 / 4.0)
	factor2_3 = float32(2.0 / 3.0)
)

type vector2 struct{ x, y float32 }

func (v vector2) approxZero() bool { return approxZeroUlps32(v.x, 4) && approxZeroUlps32(v.y, 4) }

func (v vector2) mul(c float32) vector2 { return vector2{float32(v.x * c), float32(v.y * c)} }

type vector3 struct{ x, y, z float32 }

func (v vector3) dot(o vector3) float32 { return dot3(v.x, o.x, v.y, o.y, v.z, o.z) }

func (v vector3) length() float32 { return sqrt32(dot3(v.x, v.x, v.y, v.y, v.z, v.z)) }

func (v vector3) normalized() (vector3, bool) {
	length := v.length()
	if approxZeroUlps32(length, 4) {
		return vector3{}, false
	}
	return vector3{v.x / length, v.y / length, v.z / length}, true
}

func (v vector3) add(o vector3) vector3 { return vector3{v.x + o.x, v.y + o.y, v.z + o.z} }
func (v vector3) sub(o vector3) vector3 { return vector3{v.x - o.x, v.y - o.y, v.z - o.z} }

type lightNormal struct {
	factor vector2
	normal vector2
}

func newLightNormal(factorX, factorY float32, nx, ny int16) lightNormal {
	return lightNormal{vector2{factorX, factorY}, vector2{float32(-nx), float32(-ny)}}
}

// diffuseLighting renders a diffuse lighting: only src's alpha is read; dest gets unpremultiplied
// pixels. Nothing happens when src is smaller than 3x3.
func diffuseLighting(fe *usvg.FeDiffuseLighting, light usvg.LightSource, src, dest imageRef) {
	if src.width != dest.width || src.height != dest.height {
		panic("resvg: lighting sizes") // Rust asserts
	}
	lightFactor := func(normal lightNormal, lightVector vector3) float32 {
		var k float32
		if normal.normal.approxZero() {
			k = lightVector.z
		} else {
			n := normal.normal.mul(fe.SurfaceScale() / 255.0)
			n.x = float32(n.x * normal.factor.x)
			n.y = float32(n.y * normal.factor.y)
			nv := vector3{n.x, n.y, 1}
			k = nv.dot(lightVector) / nv.length()
		}
		return float32(fe.DiffuseConstant() * k)
	}
	applyLighting(light, fe.SurfaceScale(), fe.LightingColor(), lightFactor, calcDiffuseAlpha, src, dest)
}

// specularLighting renders a specular lighting: only src's alpha is read; dest gets premultiplied
// pixels. Nothing happens when src is smaller than 3x3.
func specularLighting(fe *usvg.FeSpecularLighting, light usvg.LightSource, src, dest imageRef) {
	if src.width != dest.width || src.height != dest.height {
		panic("resvg: lighting sizes") // Rust asserts
	}
	lightFactor := func(normal lightNormal, lightVector vector3) float32 {
		h := lightVector.add(vector3{0, 0, 1})
		hLength := h.length()
		if approxZeroUlps32(hLength, 4) {
			return 0
		}
		var k float32
		if normal.normal.approxZero() {
			nDotH := h.z / hLength
			if approxEqUlps32(fe.SpecularExponent(), 1, 4) {
				k = nDotH
			} else {
				k = pow32(nDotH, fe.SpecularExponent())
			}
		} else {
			n := normal.normal.mul(fe.SurfaceScale() / 255.0)
			n.x = float32(n.x * normal.factor.x)
			n.y = float32(n.y * normal.factor.y)
			nv := vector3{n.x, n.y, 1}
			nDotH := nv.dot(h) / nv.length() / hLength
			if approxEqUlps32(fe.SpecularExponent(), 1, 4) {
				k = nDotH
			} else {
				k = pow32(nDotH, fe.SpecularExponent())
			}
		}
		return float32(fe.SpecularConstant() * k)
	}
	applyLighting(light, fe.SurfaceScale(), fe.LightingColor(), lightFactor, calcSpecularAlpha, src, dest)
}

func applyLighting(light usvg.LightSource, surfaceScale float32, lightingColor usvg.Color,
	lightFactor func(lightNormal, vector3) float32, calcAlpha func(r, g, b uint8) uint8, src, dest imageRef) {
	if src.width < 3 || src.height < 3 {
		return
	}
	width := src.width
	height := src.height

	// `feDistantLight` has a fixed vector, so calculate it beforehand.
	lightVector := vector3{1, 1, 1}
	if l, ok := light.(usvg.DistantLight); ok {
		azimuth := toRadians(l.Azimuth)
		elevation := toRadians(l.Elevation)
		lightVector = vector3{
			float32(cos32(azimuth) * cos32(elevation)),
			float32(sin32(azimuth) * cos32(elevation)),
			sin32(elevation),
		}
	}

	calc := func(nx, ny uint32, normal lightNormal) {
		var origin vector3
		switch l := light.(type) {
		case usvg.PointLight:
			origin = vector3{l.X, l.Y, l.Z}
		case usvg.SpotLight:
			origin = vector3{l.X, l.Y, l.Z}
		}
		if _, distant := light.(usvg.DistantLight); !distant {
			nz := float32(float32(float32(src.alphaAt(nx, ny))/255.0) * surfaceScale)
			v := origin.sub(vector3{float32(nx), float32(ny), nz})
			if n, ok := v.normalized(); ok {
				lightVector = n
			} else {
				lightVector = v
			}
		}

		lc := lightColor(light, lightingColor, lightVector)
		factor := lightFactor(normal, lightVector)
		compute := func(x uint8) uint8 { return asU8(f32Bound(0, float32(float32(x)*factor), 255) + 0.5) }

		r := compute(lc.Red)
		g := compute(lc.Green)
		b := compute(lc.Blue)
		a := calcAlpha(r, g, b)
		px := dest.at(nx, ny)
		px[0], px[1], px[2], px[3] = r, g, b, a
	}

	calc(0, 0, topLeftNormal(src))
	calc(width-1, 0, topRightNormal(src))
	calc(0, height-1, bottomLeftNormal(src))
	calc(width-1, height-1, bottomRightNormal(src))

	for x := uint32(1); x < width-1; x++ {
		calc(x, 0, topRowNormal(src, x))
		calc(x, height-1, bottomRowNormal(src, x))
	}
	for y := uint32(1); y < height-1; y++ {
		calc(0, y, leftColumnNormal(src, y))
		calc(width-1, y, rightColumnNormal(src, y))
	}
	for y := uint32(1); y < height-1; y++ {
		for x := uint32(1); x < width-1; x++ {
			calc(x, y, interiorNormal(src, x, y))
		}
	}
}

func lightColor(light usvg.LightSource, lightingColor usvg.Color, lightVector vector3) usvg.Color {
	l, ok := light.(usvg.SpotLight)
	if !ok {
		return lightingColor
	}
	origin := vector3{l.X, l.Y, l.Z}
	direction := vector3{l.PointsAtX, l.PointsAtY, l.PointsAtZ}.sub(origin)
	if n, ok := direction.normalized(); ok {
		direction = n
	}
	minusLDotS := -lightVector.dot(direction)
	if minusLDotS <= 0 {
		return usvg.ColorBlack()
	}
	if l.HasLimitingConeAngle && minusLDotS < cos32(toRadians(l.LimitingConeAngle)) {
		return usvg.ColorBlack()
	}
	factor := pow32(minusLDotS, l.SpecularExponent)
	compute := func(x uint8) uint8 { return asU8(f32Bound(0, float32(float32(x)*factor), 255) + 0.5) }
	return usvg.NewColorRGB(compute(lightingColor.Red), compute(lightingColor.Green), compute(lightingColor.Blue))
}

func topLeftNormal(img imageRef) lightNormal {
	center := img.alphaAt(0, 0)
	right := img.alphaAt(1, 0)
	bottom := img.alphaAt(0, 1)
	bottomRight := img.alphaAt(1, 1)
	return newLightNormal(factor2_3, factor2_3,
		-2*center+2*right-bottom+bottomRight,
		-2*center-right+2*bottom+bottomRight)
}

func topRightNormal(img imageRef) lightNormal {
	left := img.alphaAt(img.width-2, 0)
	center := img.alphaAt(img.width-1, 0)
	bottomLeft := img.alphaAt(img.width-2, 1)
	bottom := img.alphaAt(img.width-1, 1)
	return newLightNormal(factor2_3, factor2_3,
		-2*left+2*center-bottomLeft+bottom,
		-left-2*center+bottomLeft+2*bottom)
}

func bottomLeftNormal(img imageRef) lightNormal {
	top := img.alphaAt(0, img.height-2)
	topRight := img.alphaAt(1, img.height-2)
	center := img.alphaAt(0, img.height-1)
	right := img.alphaAt(1, img.height-1)
	return newLightNormal(factor2_3, factor2_3,
		-top+topRight-2*center+2*right,
		-2*top-topRight+2*center+right)
}

func bottomRightNormal(img imageRef) lightNormal {
	topLeft := img.alphaAt(img.width-2, img.height-2)
	top := img.alphaAt(img.width-1, img.height-2)
	left := img.alphaAt(img.width-2, img.height-1)
	center := img.alphaAt(img.width-1, img.height-1)
	return newLightNormal(factor2_3, factor2_3,
		-topLeft+top-2*left+2*center,
		-topLeft-2*top+left+2*center)
}

func topRowNormal(img imageRef, x uint32) lightNormal {
	left := img.alphaAt(x-1, 0)
	center := img.alphaAt(x, 0)
	right := img.alphaAt(x+1, 0)
	bottomLeft := img.alphaAt(x-1, 1)
	bottom := img.alphaAt(x, 1)
	bottomRight := img.alphaAt(x+1, 1)
	return newLightNormal(factor1_3, factor1_2,
		-2*left+2*right-bottomLeft+bottomRight,
		-left-2*center-right+bottomLeft+2*bottom+bottomRight)
}

func bottomRowNormal(img imageRef, x uint32) lightNormal {
	topLeft := img.alphaAt(x-1, img.height-2)
	top := img.alphaAt(x, img.height-2)
	topRight := img.alphaAt(x+1, img.height-2)
	left := img.alphaAt(x-1, img.height-1)
	center := img.alphaAt(x, img.height-1)
	right := img.alphaAt(x+1, img.height-1)
	return newLightNormal(factor1_3, factor1_2,
		-topLeft+topRight-2*left+2*right,
		-topLeft-2*top-topRight+left+2*center+right)
}

func leftColumnNormal(img imageRef, y uint32) lightNormal {
	top := img.alphaAt(0, y-1)
	topRight := img.alphaAt(1, y-1)
	center := img.alphaAt(0, y)
	right := img.alphaAt(1, y)
	bottom := img.alphaAt(0, y+1)
	bottomRight := img.alphaAt(1, y+1)
	return newLightNormal(factor1_2, factor1_3,
		-top+topRight-2*center+2*right-bottom+bottomRight,
		-2*top-topRight+2*bottom+bottomRight)
}

func rightColumnNormal(img imageRef, y uint32) lightNormal {
	topLeft := img.alphaAt(img.width-2, y-1)
	top := img.alphaAt(img.width-1, y-1)
	left := img.alphaAt(img.width-2, y)
	center := img.alphaAt(img.width-1, y)
	bottomLeft := img.alphaAt(img.width-2, y+1)
	bottom := img.alphaAt(img.width-1, y+1)
	return newLightNormal(factor1_2, factor1_3,
		-topLeft+top-2*left+2*center-bottomLeft+bottom,
		-topLeft-2*top+bottomLeft+2*bottom)
}

func interiorNormal(img imageRef, x, y uint32) lightNormal {
	topLeft := img.alphaAt(x-1, y-1)
	top := img.alphaAt(x, y-1)
	topRight := img.alphaAt(x+1, y-1)
	left := img.alphaAt(x-1, y)
	right := img.alphaAt(x+1, y)
	bottomLeft := img.alphaAt(x-1, y+1)
	bottom := img.alphaAt(x, y+1)
	bottomRight := img.alphaAt(x+1, y+1)
	return newLightNormal(factor1_4, factor1_4,
		-topLeft+topRight-2*left+2*right-bottomLeft+bottomRight,
		-topLeft-2*top-topRight+bottomLeft+2*bottom+bottomRight)
}

func calcDiffuseAlpha(_, _, _ uint8) uint8 { return 255 }

func calcSpecularAlpha(r, g, b uint8) uint8 { return max(r, g, b) }
