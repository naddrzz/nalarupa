// Port of svgtypes 0.16.1 src/directional_position.rs

package usvg

// stDirectionalPosition is DirectionalPosition.
type stDirectionalPosition uint8

const (
	stDirectionalPositionTop stDirectionalPosition = iota
	stDirectionalPositionCenter
	stDirectionalPositionBottom
	stDirectionalPositionRight
	stDirectionalPositionLeft
)

// IsHorizontal reports whether the value can be a horizontal position.
func (d stDirectionalPosition) IsHorizontal() bool {
	return d == stDirectionalPositionCenter || d == stDirectionalPositionLeft || d == stDirectionalPositionRight
}

// IsVertical reports whether the value can be a vertical position.
func (d stDirectionalPosition) IsVertical() bool {
	return d == stDirectionalPositionCenter || d == stDirectionalPositionTop || d == stDirectionalPositionBottom
}

// ToLength is From<DirectionalPosition> for Length: 0%, 50% or 100%.
func (d stDirectionalPosition) ToLength() stLength {
	switch d {
	case stDirectionalPositionLeft, stDirectionalPositionTop:
		return stLength{0, stLengthUnitPercent}
	case stDirectionalPositionRight, stDirectionalPositionBottom:
		return stLength{100, stLengthUnitPercent}
	}
	return stLength{50, stLengthUnitPercent}
}

// stParseDirectionalPosition is DirectionalPosition::from_str. Trailing spaces are an error.
func stParseDirectionalPosition(text string) (stDirectionalPosition, error) {
	s := stNewStream(text)
	d, err := s.parseDirectionalPosition()
	if err != nil {
		return 0, err
	}
	if !s.atEnd() {
		return 0, stErrData(s.calcCharPos())
	}
	return d, nil
}

// parseDirectionalPosition parses left, right, top, bottom or center (as a prefix).
func (s *stStream) parseDirectionalPosition() (stDirectionalPosition, error) {
	s.skipSpaces()
	switch {
	case s.startsWith("left"):
		s.advance(4)
		return stDirectionalPositionLeft, nil
	case s.startsWith("right"):
		s.advance(5)
		return stDirectionalPositionRight, nil
	case s.startsWith("top"):
		s.advance(3)
		return stDirectionalPositionTop, nil
	case s.startsWith("bottom"):
		s.advance(6)
		return stDirectionalPositionBottom, nil
	case s.startsWith("center"):
		s.advance(6)
		return stDirectionalPositionCenter, nil
	}
	return 0, stErrString([]string{s.sliceTail(), "left", "right", "top", "bottom", "center"}, s.calcCharPos())
}
