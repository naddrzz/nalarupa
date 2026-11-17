package skia

// Colors (color.rs).

const (
	AlphaU8Transparent uint8 = 0x00 // a fully transparent 8-bit alpha
	AlphaU8Opaque      uint8 = 0xFF // a fully opaque 8-bit alpha
)

// ColorU8 is an RGBA color with 8-bit components, byte order RGBA.
type ColorU8 [4]uint8

// ColorU8FromRGBA creates a color.
func ColorU8FromRGBA(r, g, b, a uint8) ColorU8 { return ColorU8{r, g, b, a} }

func (c ColorU8) Red() uint8     { return c[0] }
func (c ColorU8) Green() uint8   { return c[1] }
func (c ColorU8) Blue() uint8    { return c[2] }
func (c ColorU8) Alpha() uint8   { return c[3] }
func (c ColorU8) IsOpaque() bool { return c[3] == AlphaU8Opaque }

// Premultiply converts into a premultiplied color.
func (c ColorU8) Premultiply() PremultipliedColorU8 {
	a := c.Alpha()
	if a != AlphaU8Opaque {
		return PremultipliedColorU8{premultiplyU8(c.Red(), a), premultiplyU8(c.Green(), a), premultiplyU8(c.Blue(), a), a}
	}
	return PremultipliedColorU8(c)
}

// PremultipliedColorU8 is a premultiplied RGBA color with 8-bit components (RGB <= A), byte order RGBA.
// A pixmap stores its pixels in this layout.
type PremultipliedColorU8 [4]uint8

// PremultipliedColorU8Transparent is a transparent color.
var PremultipliedColorU8Transparent = PremultipliedColorU8{}

// PremultipliedColorU8FromRGBA creates a premultiplied color; ok is false when a color component is
// greater than alpha.
func PremultipliedColorU8FromRGBA(r, g, b, a uint8) (PremultipliedColorU8, bool) {
	if r <= a && g <= a && b <= a {
		return PremultipliedColorU8{r, g, b, a}, true
	}
	return PremultipliedColorU8{}, false
}

func (c PremultipliedColorU8) Red() uint8     { return c[0] }
func (c PremultipliedColorU8) Green() uint8   { return c[1] }
func (c PremultipliedColorU8) Blue() uint8    { return c[2] }
func (c PremultipliedColorU8) Alpha() uint8   { return c[3] }
func (c PremultipliedColorU8) IsOpaque() bool { return c[3] == AlphaU8Opaque }

// Demultiply returns the unpremultiplied color.
func (c PremultipliedColorU8) Demultiply() ColorU8 {
	alpha := c.Alpha()
	if alpha == AlphaU8Opaque {
		return ColorU8(c)
	}
	a := float64(alpha) / 255.0
	return ColorU8{demulU8(c.Red(), a), demulU8(c.Green(), a), demulU8(c.Blue(), a), alpha}
}

// demulU8 is `(c as f64 / a + 0.5) as u8`, saturating.
func demulU8(c uint8, a float64) uint8 {
	v := float64(c)/a + 0.5
	switch {
	case v != v || v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v)
}

// Color is an RGBA color with float components, each in 0..=1.
type Color struct{ r, g, b, a float32 }

var (
	ColorTransparent = Color{0, 0, 0, 0}
	ColorBlack       = Color{0, 0, 0, 1}
	ColorWhite       = Color{1, 1, 1, 1}
)

// ColorFromRGBA creates a color; ok is false unless every component is in 0..=1.
func ColorFromRGBA(r, g, b, a float32) (Color, bool) {
	var ok [4]bool
	var c Color
	c.r, ok[0] = newNormalized(r)
	c.g, ok[1] = newNormalized(g)
	c.b, ok[2] = newNormalized(b)
	c.a, ok[3] = newNormalized(a)
	if !ok[0] || !ok[1] || !ok[2] || !ok[3] {
		return Color{}, false
	}
	return c, true
}

// ColorFromRGBA8 creates a color, dividing each component by 255.
func ColorFromRGBA8(r, g, b, a uint8) Color {
	return Color{float32(r) / 255.0, float32(g) / 255.0, float32(b) / 255.0, float32(a) / 255.0}
}

func (c Color) Red() float32   { return c.r }
func (c Color) Green() float32 { return c.g }
func (c Color) Blue() float32  { return c.b }
func (c Color) Alpha() float32 { return c.a }

// SetRed sets the red component, clamped to 0..=1 (NaN gives 0).
func (c *Color) SetRed(v float32)   { c.r = normalizedClamped(v) }
func (c *Color) SetGreen(v float32) { c.g = normalizedClamped(v) }
func (c *Color) SetBlue(v float32)  { c.b = normalizedClamped(v) }
func (c *Color) SetAlpha(v float32) { c.a = normalizedClamped(v) }

// ApplyOpacity multiplies alpha by opacity, which is clamped to 0..=1 first.
func (c *Color) ApplyOpacity(opacity float32) {
	c.a = normalizedClamped(float32(c.a * bound(opacity, 0, 1)))
}

// IsOpaque reports whether alpha is 1.
func (c Color) IsOpaque() bool { return c.a == 1 }

// Premultiply converts into a premultiplied color.
func (c Color) Premultiply() PremultipliedColor {
	if c.IsOpaque() {
		return PremultipliedColor(c)
	}
	return PremultipliedColor{
		r: normalizedClamped(float32(c.r * c.a)),
		g: normalizedClamped(float32(c.g * c.a)),
		b: normalizedClamped(float32(c.b * c.a)),
		a: c.a,
	}
}

// ToColorU8 converts into 8-bit components.
func (c Color) ToColorU8() ColorU8 { return ColorU8(colorF32ToU8(c.r, c.g, c.b, c.a)) }

// PremultipliedColor is a premultiplied RGBA color with float components, each in 0..=1, RGB <= A.
type PremultipliedColor struct{ r, g, b, a float32 }

func (c PremultipliedColor) Red() float32   { return c.r }
func (c PremultipliedColor) Green() float32 { return c.g }
func (c PremultipliedColor) Blue() float32  { return c.b }
func (c PremultipliedColor) Alpha() float32 { return c.a }

// Demultiply returns the unpremultiplied color.
func (c PremultipliedColor) Demultiply() Color {
	if c.a == 0 {
		return ColorTransparent
	}
	return Color{normalizedClamped(c.r / c.a), normalizedClamped(c.g / c.a), normalizedClamped(c.b / c.a), c.a}
}

// ToColorU8 converts into 8-bit components.
func (c PremultipliedColor) ToColorU8() PremultipliedColorU8 {
	return PremultipliedColorU8(colorF32ToU8(c.r, c.g, c.b, c.a))
}

// premultiplyU8 returns c*a/255, rounded.
func premultiplyU8(c, a uint8) uint8 {
	prod := uint32(c)*uint32(a) + 128
	return uint8((prod + (prod >> 8)) >> 8)
}

func colorF32ToU8(r, g, b, a float32) [4]uint8 {
	return [4]uint8{
		castU8(float32(r*255) + 0.5),
		castU8(float32(g*255) + 0.5),
		castU8(float32(b*255) + 0.5),
		castU8(float32(a*255) + 0.5),
	}
}

// ColorSpace is the color space blending happens in, a very limited subset of SkColorSpace. Any space
// other than linear forces the high precision pipeline.
type ColorSpace uint8

const (
	ColorSpaceLinear        ColorSpace = iota // linear RGB, the default
	ColorSpaceGamma2                          // a gamma of 2
	ColorSpaceSimpleSRGB                      // a gamma of 2.2
	ColorSpaceFullSRGBGamma                   // the full sRGB gamma function (no conversion to XYZ)
)

func (cs ColorSpace) expandChannel(x float32) float32 {
	switch cs {
	case ColorSpaceGamma2:
		return normalizedClamped(float32(x * x))
	case ColorSpaceSimpleSRGB:
		return normalizedClamped(approxPowf(x, 2.2))
	case ColorSpaceFullSRGBGamma:
		if x <= 0.04045 {
			x = x / 12.92
		} else {
			x = approxPowf((x+0.055)/1.055, 2.4)
		}
		return normalizedClamped(x)
	}
	return x
}

func (cs ColorSpace) expandColor(c Color) Color {
	c.r = cs.expandChannel(c.r)
	c.g = cs.expandChannel(c.g)
	c.b = cs.expandChannel(c.b)
	return c
}

func (cs ColorSpace) compressChannel(x float32) float32 {
	switch cs {
	case ColorSpaceGamma2:
		return normalizedClamped(sqrt32(x))
	case ColorSpaceSimpleSRGB:
		return normalizedClamped(approxPowf(x, 0.45454545))
	case ColorSpaceFullSRGBGamma:
		if x <= 0.0031308 {
			x = float32(x * 12.92)
		} else {
			x = float32(approxPowf(x, 0.416666666)*1.055) - 0.055
		}
		return normalizedClamped(x)
	}
	return x
}

func (cs ColorSpace) expandStage() (stage, bool) {
	switch cs {
	case ColorSpaceGamma2:
		return stageGammaExpand2, true
	case ColorSpaceSimpleSRGB:
		return stageGammaExpand22, true
	case ColorSpaceFullSRGBGamma:
		return stageGammaExpandSrgb, true
	}
	return 0, false
}

func (cs ColorSpace) expandDestStage() (stage, bool) {
	switch cs {
	case ColorSpaceGamma2:
		return stageGammaExpandDestination2, true
	case ColorSpaceSimpleSRGB:
		return stageGammaExpandDestination22, true
	case ColorSpaceFullSRGBGamma:
		return stageGammaExpandDestinationSrgb, true
	}
	return 0, false
}

func (cs ColorSpace) compressStage() (stage, bool) {
	switch cs {
	case ColorSpaceGamma2:
		return stageGammaCompress2, true
	case ColorSpaceSimpleSRGB:
		return stageGammaCompress22, true
	case ColorSpaceFullSRGBGamma:
		return stageGammaCompressSrgb, true
	}
	return 0, false
}
