// Port of svgtypes 0.16.1 src/color.rs

package usvg

import "math"

// stColor is Color, the <color> type.
type stColor struct {
	Red, Green, Blue, Alpha uint8
}

// stNewColorRGB is Color::new_rgb (opaque).
func stNewColorRGB(red, green, blue uint8) stColor { return stColor{red, green, blue, 255} }

// stNewColorRGBA is Color::new_rgba.
func stNewColorRGBA(red, green, blue, alpha uint8) stColor { return stColor{red, green, blue, alpha} }

func stColorBlack() stColor { return stNewColorRGB(0, 0, 0) }
func stColorWhite() stColor { return stNewColorRGB(255, 255, 255) }
func stColorGray() stColor  { return stNewColorRGB(128, 128, 128) }
func stColorRed() stColor   { return stNewColorRGB(255, 0, 0) }
func stColorGreen() stColor { return stNewColorRGB(0, 128, 0) }
func stColorBlue() stColor  { return stNewColorRGB(0, 0, 255) }

// stParseColor is Color::from_str: a CSS3 color (plus #rgba/#rrggbbaa hex), with optional surrounding
// spaces. Trailing data (such as an icc-color) is an error.
func stParseColor(text string) (stColor, error) {
	s := stNewStream(text)
	c, err := s.parseColor()
	if err != nil {
		return stColor{}, err
	}
	s.skipSpaces()
	if !s.atEnd() {
		return stColor{}, stErrData(s.calcCharPos())
	}
	return c, nil
}

// tryParseColor parses a color, leaving the stream untouched on error.
func (s *stStream) tryParseColor() (stColor, bool) {
	t := *s
	c, err := t.parseColor()
	if err != nil {
		return stColor{}, false
	}
	*s = t
	return c, true
}

// stASCIILower is str::to_ascii_lowercase.
func stASCIILower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// parseColor parses a color.
func (s *stStream) parseColor() (stColor, error) {
	s.skipSpaces()
	color := stColorBlack()
	c, err := s.currByte()
	if err != nil {
		return stColor{}, err
	}
	if c == '#' {
		s.advance(1)
		str := s.consumeBytes(stIsHexDigit)
		switch len(str) {
		case 6:
			color.Red = stHexPair(str[0], str[1])
			color.Green = stHexPair(str[2], str[3])
			color.Blue = stHexPair(str[4], str[5])
		case 8:
			color.Red = stHexPair(str[0], str[1])
			color.Green = stHexPair(str[2], str[3])
			color.Blue = stHexPair(str[4], str[5])
			color.Alpha = stHexPair(str[6], str[7])
		case 3:
			color.Red = stShortHex(str[0])
			color.Green = stShortHex(str[1])
			color.Blue = stShortHex(str[2])
		case 4:
			color.Red = stShortHex(str[0])
			color.Green = stShortHex(str[1])
			color.Blue = stShortHex(str[2])
			color.Alpha = stShortHex(str[3])
		default:
			return stColor{}, stErrValue()
		}
		return color, nil
	}

	name := stASCIILower(s.consumeASCIIIdent())
	switch name {
	case "rgb", "rgba":
		if err := s.consumeByte('('); err != nil {
			return stColor{}, err
		}
		isPercent := false
		value, err := s.parseNumber()
		if err != nil {
			return stColor{}, err
		}
		if s.startsWith("%") {
			s.advance(1)
			isPercent = true
		}
		s.skipSpaces()
		s.parseListSeparator()
		if isPercent {
			// The division and multiply are explicitly not collapsed, so red rounds like green and blue.
			color.Red = stRoundF64ToU8((value / 100) * 255)
			g, err := s.parseListNumberOrPercent()
			if err != nil {
				return stColor{}, err
			}
			color.Green = stRoundF64ToU8(g * 255)
			b, err := s.parseListNumberOrPercent()
			if err != nil {
				return stColor{}, err
			}
			color.Blue = stRoundF64ToU8(b * 255)
		} else {
			color.Red = stRoundF64ToU8(value)
			g, err := s.parseListNumber()
			if err != nil {
				return stColor{}, err
			}
			color.Green = stRoundF64ToU8(g)
			b, err := s.parseListNumber()
			if err != nil {
				return stColor{}, err
			}
			color.Blue = stRoundF64ToU8(b)
		}
		if err := s.parseColorAlphaAndClose(&color); err != nil {
			return stColor{}, err
		}
	case "hsl", "hsla":
		if err := s.consumeByte('('); err != nil {
			return stColor{}, err
		}
		hue, err := s.parseListNumber()
		if err != nil {
			return stColor{}, err
		}
		hue = math.Mod(math.Mod(hue, 360)+360, 360)
		sat, err := s.parseListNumberOrPercent()
		if err != nil {
			return stColor{}, err
		}
		saturation := stF64Bound(0, sat, 1)
		light, err := s.parseListNumberOrPercent()
		if err != nil {
			return stColor{}, err
		}
		lightness := stF64Bound(0, light, 1)
		color = stHSLToRGB(float32(hue)/60, float32(saturation), float32(lightness))
		if err := s.parseColorAlphaAndClose(&color); err != nil {
			return stColor{}, err
		}
	default:
		named, ok := stColorFromName(name)
		if !ok {
			return stColor{}, stErrValue()
		}
		color = named
	}
	return color, nil
}

// parseColorAlphaAndClose parses the optional alpha of rgb()/hsl() and the closing parenthesis.
func (s *stStream) parseColorAlphaAndClose(color *stColor) error {
	s.skipSpaces()
	if !s.startsWith(")") {
		a, err := s.parseListNumber()
		if err != nil {
			return err
		}
		color.Alpha = stRoundF64ToU8(a * 255)
	}
	s.skipSpaces()
	return s.consumeByte(')')
}

// stFromHex maps a hex digit to its value (other bytes give b'0', as in Rust; they cannot occur).
func stFromHex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return '0'
}

func stShortHex(c byte) uint8 {
	h := stFromHex(c)
	return (h << 4) | h
}

func stHexPair(c1, c2 byte) uint8 { return (stFromHex(c1) << 4) | stFromHex(c2) }

// stHSLToRGB converts hue in 0..6 and saturation and lightness in 0..=1 to an opaque color.
func stHSLToRGB(hue, saturation, lightness float32) stColor {
	var t2 float32
	if lightness <= 0.5 {
		t2 = lightness * (saturation + 1)
	} else {
		t2 = lightness + saturation - float32(lightness*saturation)
	}
	t1 := float32(lightness*2) - t2
	red := stHueToRGB(t1, t2, hue+2)
	green := stHueToRGB(t1, t2, hue)
	blue := stHueToRGB(t1, t2, hue-2)
	return stNewColorRGB(stRoundF32ToU8(red*255), stRoundF32ToU8(green*255), stRoundF32ToU8(blue*255))
}

func stHueToRGB(t1, t2, hue float32) float32 {
	if hue < 0 {
		hue += 6
	}
	if hue >= 6 {
		hue -= 6
	}
	switch {
	case hue < 1:
		return float32((t2-t1)*hue) + t1
	case hue < 3:
		return t2
	case hue < 4:
		return float32((t2-t1)*(4-hue)) + t1
	}
	return t1
}

// stF64Bound is f64::clamp(min, max) for a finite value.
func stF64Bound(lo, val, hi float64) float64 {
	if val < lo {
		return lo
	}
	if val > hi {
		return hi
	}
	return val
}
