// Port of svgtypes 0.16.1 src/length.rs

package usvg

// stLengthUnit is LengthUnit.
type stLengthUnit uint8

const (
	stLengthUnitNone stLengthUnit = iota
	stLengthUnitEm
	stLengthUnitEx
	stLengthUnitPx
	stLengthUnitIn
	stLengthUnitCm
	stLengthUnitMm
	stLengthUnitPt
	stLengthUnitPc
	stLengthUnitPercent
)

// stLength is Length, the <length> type. The zero value is Length::zero() / default().
type stLength struct {
	Number float64
	Unit   stLengthUnit
}

// stNewLength is Length::new.
func stNewLength(number float64, unit stLengthUnit) stLength { return stLength{number, unit} }

// stNewLengthNumber is Length::new_number.
func stNewLengthNumber(number float64) stLength { return stLength{number, stLengthUnitNone} }

// stLengthZero is Length::zero.
func stLengthZero() stLength { return stLength{0, stLengthUnitNone} }

// stParseLength is Length::from_str. Trailing spaces are an error.
func stParseLength(text string) (stLength, error) {
	s := stNewStream(text)
	l, err := s.parseLength()
	if err != nil {
		return stLength{}, err
	}
	if !s.atEnd() {
		return stLength{}, stErrData(s.calcCharPos())
	}
	return l, nil
}

// parseLength parses a length; units must be lowercase, and an unknown suffix is left unconsumed.
func (s *stStream) parseLength() (stLength, error) {
	s.skipSpaces()
	n, err := s.parseNumber()
	if err != nil {
		return stLength{}, err
	}
	if s.atEnd() {
		return stLength{n, stLengthUnitNone}, nil
	}
	var u stLengthUnit
	switch {
	case s.startsWith("%"):
		u = stLengthUnitPercent
	case s.startsWith("em"):
		u = stLengthUnitEm
	case s.startsWith("ex"):
		u = stLengthUnitEx
	case s.startsWith("px"):
		u = stLengthUnitPx
	case s.startsWith("in"):
		u = stLengthUnitIn
	case s.startsWith("cm"):
		u = stLengthUnitCm
	case s.startsWith("mm"):
		u = stLengthUnitMm
	case s.startsWith("pt"):
		u = stLengthUnitPt
	case s.startsWith("pc"):
		u = stLengthUnitPc
	default:
		u = stLengthUnitNone
	}
	switch u {
	case stLengthUnitPercent:
		s.advance(1)
	case stLengthUnitNone:
	default:
		s.advance(2)
	}
	return stLength{n, u}, nil
}

// parseListLength parses a length from a list, then skips spaces and one comma.
func (s *stStream) parseListLength() (stLength, error) {
	if s.atEnd() {
		return stLength{}, stErrEOS()
	}
	l, err := s.parseLength()
	if err != nil {
		return stLength{}, err
	}
	s.skipSpaces()
	s.parseListSeparator()
	return l, nil
}

// stLengthListParser is LengthListParser, a pull parser for <list-of-length>. After an error it ends.
type stLengthListParser struct{ s stStream }

func stNewLengthListParser(text string) *stLengthListParser {
	return &stLengthListParser{stNewStream(text)}
}

// Next returns the next length; ok is false at the end.
func (p *stLengthListParser) Next() (stLength, bool, error) {
	if p.s.atEnd() {
		return stLength{}, false, nil
	}
	v, err := p.s.parseListLength()
	if err != nil {
		p.s.jumpToEnd()
	}
	return v, true, err
}
