// Port of svgtypes 0.16.1 src/number.rs

package usvg

import (
	"math"
	"strconv"
)

// stParseNumber is Number::from_str: a number with optional surrounding spaces and nothing else.
func stParseNumber(text string) (float64, error) {
	s := stNewStream(text)
	n, err := s.parseNumber()
	if err != nil {
		return 0, err
	}
	s.skipSpaces()
	if !s.atEnd() {
		return 0, stErrData(s.calcCharPos())
	}
	return n, nil
}

// parseNumber parses a number: it finds the number's extent and parses that slice as f64. Errors are
// always InvalidNumber at the start position; the stream is not rewound.
func (s *stStream) parseNumber() (float64, error) {
	s.skipSpaces()
	start := s.pos
	if s.atEnd() {
		return 0, stErrNumber(s.calcCharPosAt(start))
	}
	n, ok := s.parseNumberImpl()
	if !ok {
		return 0, stErrNumber(s.calcCharPosAt(start))
	}
	return n, nil
}

func (s *stStream) parseNumberImpl() (float64, bool) {
	start := s.pos
	c, err := s.currByte()
	if err != nil {
		return 0, false
	}
	// Consume sign.
	if stIsSign(c) {
		s.advance(1)
		if c, err = s.currByte(); err != nil {
			return 0, false
		}
	}
	// Consume integer.
	switch {
	case stIsDigit(c):
		s.skipDigits()
	case c == '.':
	default:
		return 0, false
	}
	// Consume fraction.
	if s.isCurrByteEq('.') {
		s.advance(1)
		s.skipDigits()
	}
	if c, err := s.currByte(); err == nil && (c == 'e' || c == 'E') {
		c2, err := s.nextByte()
		if err != nil {
			return 0, false
		}
		// Check for `em`/`ex`.
		if c2 != 'm' && c2 != 'x' {
			s.advance(1)
			c3, err := s.currByte()
			if err != nil {
				return 0, false
			}
			switch {
			case c3 == '+' || c3 == '-':
				s.advance(1)
				s.skipDigits()
			case stIsDigit(c3):
				s.skipDigits()
			default:
				return 0, false
			}
		}
	}
	// f64::from_str and strconv.ParseFloat are both correctly rounded and accept the same forms of the
	// slices built above. Overflow (an error here, infinity in Rust) is rejected by both paths.
	n, err := strconv.ParseFloat(s.sliceBack(start), 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, false
	}
	return n, true
}

// parseListNumber parses a number from a list, then skips spaces and one comma.
func (s *stStream) parseListNumber() (float64, error) {
	if s.atEnd() {
		return 0, stErrEOS()
	}
	n, err := s.parseNumber()
	if err != nil {
		return 0, err
	}
	s.skipSpaces()
	s.parseListSeparator()
	return n, nil
}

// stNumberListParser is NumberListParser, a pull parser for <list-of-numbers>. After an error it ends.
type stNumberListParser struct{ s stStream }

func stNewNumberListParser(text string) *stNumberListParser {
	return &stNumberListParser{stNewStream(text)}
}

// Next returns the next number; ok is false at the end.
func (p *stNumberListParser) Next() (float64, bool, error) {
	if p.s.atEnd() {
		return 0, false, nil
	}
	v, err := p.s.parseListNumber()
	if err != nil {
		p.s.jumpToEnd()
	}
	return v, true, err
}
