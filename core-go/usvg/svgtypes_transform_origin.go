// Port of svgtypes 0.16.1 src/transform_origin.rs

package usvg

// stPosition is transform_origin's private Position: a length or a directional keyword.
type stPosition struct {
	IsDir  bool
	Length stLength              // !IsDir
	Dir    stDirectionalPosition // IsDir
}

func (p stPosition) isVertical() bool   { return !p.IsDir || p.Dir.IsVertical() }
func (p stPosition) isHorizontal() bool { return !p.IsDir || p.Dir.IsHorizontal() }

func (p stPosition) toLength() stLength {
	if p.IsDir {
		return p.Dir.ToLength()
	}
	return p.Length
}

// stTransformOrigin is TransformOrigin, the transform-origin property.
type stTransformOrigin struct {
	XOffset, YOffset, ZOffset stLength
}

// stNewTransformOrigin is TransformOrigin::new.
func stNewTransformOrigin(x, y, z stLength) stTransformOrigin { return stTransformOrigin{x, y, z} }

// stTransformOriginError is TransformOriginError.
type stTransformOriginError uint8

const (
	stTransformOriginMissingParameters stTransformOriginError = iota
	stTransformOriginInvalidParameters
	stTransformOriginZIndexIsPercentage
)

func (e stTransformOriginError) Error() string {
	switch e {
	case stTransformOriginMissingParameters:
		return "transform origin doesn't have enough parameters"
	case stTransformOriginInvalidParameters:
		return "transform origin has invalid parameters"
	}
	return "z-index cannot be a percentage"
}

// stParseTransformOrigin is TransformOrigin::from_str.
//
// Like Rust, it panics ("unreachable") when the first part fails to parse but consumes the rest of the
// input, e.g. for " " or "-".
func stParseTransformOrigin(text string) (stTransformOrigin, error) {
	s := stNewStream(text)
	if s.atEnd() {
		return stTransformOrigin{}, stTransformOriginMissingParameters
	}

	parsePart := func(s *stStream) (stPosition, bool) {
		if dp, err := s.parseDirectionalPosition(); err == nil {
			return stPosition{IsDir: true, Dir: dp}, true
		}
		if l, err := s.parseLength(); err == nil {
			return stPosition{Length: l}, true
		}
		return stPosition{}, false
	}

	first, hasFirst := parsePart(&s)
	var second stPosition
	hasSecond := false
	var third stLength
	hasThird := false

	if !s.atEnd() {
		s.skipSpaces()
		s.parseListSeparator()
		p, ok := parsePart(&s)
		if !ok {
			return stTransformOrigin{}, stTransformOriginInvalidParameters
		}
		second, hasSecond = p, true
	}
	if !s.atEnd() {
		s.skipSpaces()
		s.parseListSeparator()
		l, err := s.parseLength()
		if err != nil {
			return stTransformOrigin{}, stTransformOriginInvalidParameters
		}
		third, hasThird = l, true
	}
	s.skipSpaces()
	if !s.atEnd() {
		return stTransformOrigin{}, stTransformOriginInvalidParameters
	}

	switch {
	case hasFirst && !hasSecond && !hasThird:
		zero := stLength{0, stLengthUnitPx}
		if first.isHorizontal() {
			return stTransformOrigin{first.toLength(), stDirectionalPositionCenter.ToLength(), zero}, nil
		}
		return stTransformOrigin{stDirectionalPositionCenter.ToLength(), first.toLength(), zero}, nil
	case hasFirst && hasSecond:
		if hasThird && third.Unit == stLengthUnitPercent {
			return stTransformOrigin{}, stTransformOriginZIndexIsPercentage
		}
		length := stLength{0, stLengthUnitPx}
		if hasThird {
			length = third
		}
		check := func(p stPosition) bool { return !p.IsDir || p.Dir == stDirectionalPositionCenter }
		if check(first) && check(second) {
			return stTransformOrigin{first.toLength(), second.toLength(), length}, nil
		}
		// There is at least one of `left`, `right`, `top`, or `bottom`.
		if first.isHorizontal() && second.isVertical() {
			return stTransformOrigin{first.toLength(), second.toLength(), length}, nil
		}
		if first.isVertical() && second.isHorizontal() {
			return stTransformOrigin{second.toLength(), first.toLength(), length}, nil
		}
		return stTransformOrigin{}, stTransformOriginInvalidParameters
	}
	panic("internal error: entered unreachable code")
}
