package usvg

// SimpleSegment is one absolute segment of SimplifyPath. Kind is 'M' or 'L' (X, Y), 'C' (X1, Y1, X2, Y2,
// X, Y), 'Q' (X1, Y1, X, Y) or 'Z'.
type SimpleSegment struct {
	Kind                 byte
	X1, Y1, X2, Y2, X, Y float64
}

// SimplifyPath is svgtypes' SimplifyingPathParser over d (arcs as cubics, H/V as lines), up to its first
// error.
func SimplifyPath(d string) []SimpleSegment {
	var out []SimpleSegment
	p := stNewSimplifyingPathParser(d)
	for {
		s, ok, err := p.Next()
		if !ok || err != nil {
			return out
		}
		var k byte
		switch s.Kind {
		case stSimpleMoveTo:
			k = 'M'
		case stSimpleLineTo:
			k = 'L'
		case stSimpleCurveTo:
			k = 'C'
		case stSimpleQuadratic:
			k = 'Q'
		case stSimpleClosePath:
			k = 'Z'
		}
		out = append(out, SimpleSegment{k, s.X1, s.Y1, s.X2, s.Y2, s.X, s.Y})
	}
}
