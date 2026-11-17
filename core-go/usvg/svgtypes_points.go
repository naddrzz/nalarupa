// Port of svgtypes 0.16.1 src/points.rs

package usvg

// stPointsParser is PointsParser, a pull parser for <list-of-points>. It stops at the first invalid
// number; an odd trailing coordinate is ignored.
type stPointsParser struct{ s stStream }

func stNewPointsParser(text string) *stPointsParser { return &stPointsParser{stNewStream(text)} }

// Next returns the next point; ok is false at the end or on invalid data (the stream is not moved to
// the end, as in Rust, so later calls retry from where parsing stopped).
func (p *stPointsParser) Next() (x, y float64, ok bool) {
	if p.s.atEnd() {
		return 0, 0, false
	}
	x, err := p.s.parseListNumber()
	if err != nil {
		return 0, 0, false
	}
	y, err = p.s.parseListNumber()
	if err != nil {
		return 0, 0, false
	}
	return x, y, true
}
