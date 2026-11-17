// Port of svgtypes 0.16.1 src/filter_functions.rs

package usvg

import (
	"math"
	"strconv"
)

// stFilterValueKind lists the FilterValue variants.
type stFilterValueKind uint8

const (
	stFilterBlur       stFilterValueKind = iota // Length: not negative, no percent
	stFilterDropShadow                          // HasColor/Color (currentColor is no color), Dx, Dy, StdDev
	stFilterBrightness                          // Amount
	stFilterContrast                            // Amount
	stFilterGrayscale                           // Amount
	stFilterHueRotate                           // Angle
	stFilterInvert                              // Amount
	stFilterOpacity                             // Amount
	stFilterSepia                               // Amount
	stFilterSaturate                            // Amount
	stFilterURL                                 // URL: not empty
)

// stFilterValue is FilterValue, a <filter-function> or <url>; only the fields named for Kind are set.
type stFilterValue struct {
	Kind           stFilterValueKind
	Length         stLength // stFilterBlur
	HasColor       bool     // stFilterDropShadow: false is None
	Color          stColor  // stFilterDropShadow when HasColor
	Dx, Dy, StdDev stLength // stFilterDropShadow
	Amount         float64  // the normalized amount of brightness...saturate; not negative
	Angle          stAngle  // stFilterHueRotate
	URL            string   // stFilterURL
}

// stFilterErrorKind lists the FilterValueListParserError variants.
type stFilterErrorKind uint8

const (
	stFilterErrPercentageValue         stFilterErrorKind = iota // Pos
	stFilterErrNegativeValue                                    // Pos
	stFilterErrInvalidAngle                                     // Pos
	stFilterErrMissingDropShadowOffset                          // Pos
	stFilterErrInvalidURL                                       // Pos
	stFilterErrStreamErrors                                     // Err
)

// stFilterAmountKinds maps the functions that take a normalized amount to their kinds.
var stFilterAmountKinds = map[string]stFilterValueKind{
	"brightness": stFilterBrightness, "contrast": stFilterContrast, "grayscale": stFilterGrayscale,
	"invert": stFilterInvert, "opacity": stFilterOpacity, "saturate": stFilterSaturate, "sepia": stFilterSepia,
}

// stFilterValueListParserError is FilterValueListParserError.
type stFilterValueListParserError struct {
	Kind stFilterErrorKind
	Pos  int
	Err  *stError // stFilterErrStreamErrors
}

func (e *stFilterValueListParserError) Error() string {
	p := strconv.Itoa(e.Pos)
	switch e.Kind {
	case stFilterErrPercentageValue:
		return "a percentage value detected at position " + p
	case stFilterErrNegativeValue:
		return "a negative value detected at position " + p
	case stFilterErrInvalidAngle:
		return "an invalid angle at position " + p
	case stFilterErrMissingDropShadowOffset:
		return "drop-shadow offset values are expected at position " + p
	case stFilterErrInvalidURL:
		return "an invalid url at position " + p
	}
	return e.Err.Error()
}

// stFilterErr wraps a stream error (always a *stError) as StreamErrors.
func stFilterErr(err error) error {
	return &stFilterValueListParserError{Kind: stFilterErrStreamErrors, Err: err.(*stError)}
}

func stFilterPosErr(kind stFilterErrorKind, pos int) error {
	return &stFilterValueListParserError{Kind: kind, Pos: pos}
}

// stFilterValueListParser is FilterValueListParser, a pull parser for <filter-value-list>. `none` and
// an empty string both end at once. After an error it ends. Errors are *stFilterValueListParserError.
type stFilterValueListParser struct{ s stStream }

func stNewFilterValueListParser(text string) *stFilterValueListParser {
	return &stFilterValueListParser{stNewStream(text)}
}

// Next returns the next filter value; ok is false at the end.
func (p *stFilterValueListParser) Next() (stFilterValue, bool, error) {
	p.s.skipSpaces()
	if p.s.atEnd() {
		// An empty attribute is still a valid value.
		return stFilterValue{}, false, nil
	}
	if p.s.startsWith("none") {
		p.s.advance(4)
		p.s.skipSpaces()
		if p.s.atEnd() {
			return stFilterValue{}, false, nil
		}
		p.s.jumpToEnd()
		return stFilterValue{}, true, stFilterErr(stErrValue())
	}
	v, err := p.parseNext()
	if err != nil {
		p.s.jumpToEnd()
	}
	return v, true, err
}

func (p *stFilterValueListParser) parseNext() (stFilterValue, error) {
	s := &p.s
	start := s.pos
	name := s.consumeASCIIIdent()
	s.skipSpaces()
	if err := s.consumeByte('('); err != nil {
		return stFilterValue{}, stFilterErr(err)
	}
	s.skipSpaces()

	var v stFilterValue
	var err error
	switch name {
	case "blur":
		v.Kind = stFilterBlur
		if !s.isCurrByteEq(')') {
			v.Length, err = stParseFilterPositiveLength(s)
		}
	case "drop-shadow":
		v, err = stParseDropShadowFunc(s)
	case "hue-rotate":
		v.Kind = stFilterHueRotate
		if !s.isCurrByteEq(')') {
			v.Angle, err = stParseFilterAngle(s)
		}
	case "brightness", "contrast", "grayscale", "invert", "opacity", "saturate", "sepia":
		v.Kind = stFilterAmountKinds[name]
		v.Amount, err = stParseGenericColorFunc(s)
	case "url":
		if err := s.consumeByte('#'); err != nil {
			return stFilterValue{}, stFilterErr(err)
		}
		link := s.consumeBytes(func(c byte) bool { return c != ' ' && c != ')' })
		if link == "" {
			return stFilterValue{}, stFilterPosErr(stFilterErrInvalidURL, s.calcCharPosAt(start))
		}
		v = stFilterValue{Kind: stFilterURL, URL: link}
	default:
		return stFilterValue{}, stFilterErr(stErrData(s.calcCharPosAt(start)))
	}
	if err != nil {
		return stFilterValue{}, err
	}

	s.skipSpaces()
	if err := s.consumeByte(')'); err != nil {
		return stFilterValue{}, stFilterErr(err)
	}
	s.skipSpaces()
	return v, nil
}

func stParseDropShadowFunc(s *stStream) (stFilterValue, error) {
	if s.isCurrByteEq(')') {
		return stFilterValue{}, stFilterPosErr(stFilterErrMissingDropShadowOffset, s.calcCharPos())
	}
	v := stFilterValue{Kind: stFilterDropShadow}

	// The color can be set before and after the lengths.
	isCurrentColor := false
	if c, ok := s.tryParseColor(); ok {
		v.HasColor, v.Color = true, c
		s.skipSpaces()
	} else if s.startsWith("currentColor") {
		isCurrentColor = true
		s.advance(12)
		s.skipSpaces()
	}

	// The offset is the only mandatory value.
	var err error
	if v.Dx, err = stParseFilterLength(s); err != nil {
		return stFilterValue{}, err
	}
	s.skipSpaces()
	if v.Dy, err = stParseFilterLength(s); err != nil {
		return stFilterValue{}, err
	}
	s.skipSpaces()

	// std_dev is optional; a failed attempt is not rewound, as in Rust.
	if sd, err := stParseFilterPositiveLength(s); err == nil {
		v.StdDev = sd
		s.skipSpaces()
	}

	// Try to parse a color after the lengths, if it wasn't set before.
	if !v.HasColor && !isCurrentColor {
		if c, ok := s.tryParseColor(); ok {
			v.HasColor, v.Color = true, c
			s.skipSpaces()
		} else if s.startsWith("currentColor") {
			s.advance(12)
		}
	}
	return v, nil
}

func stParseGenericColorFunc(s *stStream) (float64, error) {
	if s.isCurrByteEq(')') {
		return 1, nil
	}
	start := s.pos
	value, err := s.parseNumberOrPercent()
	if err != nil {
		return 0, stFilterErr(err)
	}
	if math.Signbit(value) {
		return 0, stFilterPosErr(stFilterErrNegativeValue, s.calcCharPosAt(start))
	}
	return value, nil
}

func stParseFilterLength(s *stStream) (stLength, error) {
	start := s.pos
	value, err := s.parseLength()
	if err != nil {
		return stLength{}, stFilterErr(err)
	}
	if value.Unit == stLengthUnitPercent {
		return stLength{}, stFilterPosErr(stFilterErrPercentageValue, s.calcCharPosAt(start))
	}
	return value, nil
}

func stParseFilterPositiveLength(s *stStream) (stLength, error) {
	start := s.pos
	value, err := s.parseLength()
	if err != nil {
		return stLength{}, stFilterErr(err)
	}
	if math.Signbit(value.Number) {
		return stLength{}, stFilterPosErr(stFilterErrNegativeValue, s.calcCharPosAt(start))
	}
	if value.Unit == stLengthUnitPercent {
		return stLength{}, stFilterPosErr(stFilterErrPercentageValue, s.calcCharPosAt(start))
	}
	return value, nil
}

// stParseFilterAngle is a normal angle, except the unit is mandatory for non-zero values.
func stParseFilterAngle(s *stStream) (stAngle, error) {
	s.skipSpaces()
	start := s.pos
	n, err := s.parseNumber()
	if err != nil {
		return stAngle{}, stFilterErr(err)
	}
	u := stAngleUnitDegrees
	switch {
	case s.startsWith("deg"):
		s.advance(3)
	case s.startsWith("grad"):
		s.advance(4)
		u = stAngleUnitGradians
	case s.startsWith("rad"):
		s.advance(3)
		u = stAngleUnitRadians
	case s.startsWith("turn"):
		s.advance(4)
		u = stAngleUnitTurns
	default:
		// Only zero may be unit-less.
		if n != 0 {
			return stAngle{}, stFilterPosErr(stFilterErrInvalidAngle, s.calcCharPosAt(start))
		}
	}
	return stAngle{n, u}, nil
}
