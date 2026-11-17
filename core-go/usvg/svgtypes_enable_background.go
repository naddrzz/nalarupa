// Port of svgtypes 0.16.1 src/enable_background.rs

package usvg

// stEnableBackgroundKind lists the EnableBackground variants.
type stEnableBackgroundKind uint8

const (
	stEnableBackgroundAccumulate stEnableBackgroundKind = iota
	stEnableBackgroundNew
	stEnableBackgroundNewWithRegion // X, Y, Width, Height are set
)

// stEnableBackground is EnableBackground, the enable-background attribute.
type stEnableBackground struct {
	Kind                stEnableBackgroundKind
	X, Y, Width, Height float64
}

// stParseEnableBackground is EnableBackground::from_str.
func stParseEnableBackground(text string) (stEnableBackground, error) {
	s := stNewStream(text)
	s.skipSpaces()
	if s.startsWith("accumulate") {
		s.advance(10)
		s.skipSpaces()
		if !s.atEnd() {
			return stEnableBackground{}, stErrData(s.calcCharPos())
		}
		return stEnableBackground{Kind: stEnableBackgroundAccumulate}, nil
	}
	if !s.startsWith("new") {
		return stEnableBackground{}, stErrValue()
	}
	s.advance(3)
	s.skipSpaces()
	if s.atEnd() {
		return stEnableBackground{Kind: stEnableBackgroundNew}, nil
	}
	var v [4]float64
	for i := range v {
		n, err := s.parseListNumber()
		if err != nil {
			return stEnableBackground{}, err
		}
		v[i] = n
	}
	s.skipSpaces()
	if !s.atEnd() {
		return stEnableBackground{}, stErrData(s.calcCharPos())
	}
	// The region size must be valid.
	if !(v[2] > 0 && v[3] > 0) {
		return stEnableBackground{}, stErrValue()
	}
	return stEnableBackground{stEnableBackgroundNewWithRegion, v[0], v[1], v[2], v[3]}, nil
}
