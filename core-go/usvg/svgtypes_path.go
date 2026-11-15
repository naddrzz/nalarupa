// Port of svgtypes 0.16.1 src/path.rs. Arcs go through the kurbo port (kurbo_arc.go).

package usvg

// stPathSegmentKind lists the PathSegment variants.
type stPathSegmentKind uint8

const (
	stPathMoveTo           stPathSegmentKind = iota // X, Y
	stPathLineTo                                    // X, Y
	stPathHorizontalLineTo                          // X
	stPathVerticalLineTo                            // Y
	stPathCurveTo                                   // X1, Y1, X2, Y2, X, Y
	stPathSmoothCurveTo                             // X2, Y2, X, Y
	stPathQuadratic                                 // X1, Y1, X, Y
	stPathSmoothQuadratic                           // X, Y
	stPathEllipticalArc                             // Rx, Ry, XAxisRotation, LargeArc, Sweep, X, Y
	stPathClosePath                                 // no payload
)

// stPathSegment is PathSegment; Abs is set for every kind, the other fields as listed per kind.
type stPathSegment struct {
	Kind                  stPathSegmentKind
	Abs                   bool
	X1, Y1, X2, Y2, X, Y  float64
	Rx, Ry, XAxisRotation float64
	LargeArc, Sweep       bool
}

// IsAbs is PathSegment::is_abs.
func (p stPathSegment) IsAbs() bool { return p.Abs }

// Command is PathSegment::command: the segment letter, uppercase when absolute.
func (p stPathSegment) Command() byte {
	c := "mlhvcsqtaz"[p.Kind]
	if p.Abs {
		c -= 'a' - 'A'
	}
	return c
}

// stPathParser is PathParser, a pull parser for path data. Implicit commands become explicit ones (the
// pairs after a MoveTo become LineTos). After an error it ends.
type stPathParser struct {
	s          stStream
	hasPrevCmd bool
	prevCmd    byte
}

func stNewPathParser(text string) *stPathParser { return &stPathParser{s: stNewStream(text)} }

// Next returns the next segment; ok is false at the end.
func (p *stPathParser) Next() (stPathSegment, bool, error) {
	p.s.skipSpaces()
	if p.s.atEnd() {
		return stPathSegment{}, false, nil
	}
	seg, err := p.nextImpl()
	if err != nil {
		p.s.jumpToEnd()
	}
	return seg, true, err
}

func (p *stPathParser) nextImpl() (stPathSegment, error) {
	s := &p.s
	start := s.pos
	firstChar := s.currByteUnchecked()

	if !p.hasPrevCmd && !stIsCmd(firstChar) {
		return stPathSegment{}, stErrData(s.calcCharPosAt(start))
	}
	if !p.hasPrevCmd && firstChar != 'M' && firstChar != 'm' {
		// The first segment must be a MoveTo.
		return stPathSegment{}, stErrData(s.calcCharPosAt(start))
	}

	var isImplicitMoveTo bool
	var cmd byte
	switch {
	case stIsCmd(firstChar):
		cmd = firstChar
		s.advance(1)
	case stIsNumberStart(firstChar) && p.hasPrevCmd:
		pc := p.prevCmd
		if pc == 'Z' || pc == 'z' {
			// ClosePath cannot be followed by a number.
			return stPathSegment{}, stErrData(s.calcCharPosAt(start))
		}
		if pc == 'M' || pc == 'm' {
			// The pairs after a moveto are implicit linetos.
			isImplicitMoveTo = true
			if stIsAbsolute(pc) {
				cmd = 'L'
			} else {
				cmd = 'l'
			}
		} else {
			cmd = pc
		}
	default:
		return stPathSegment{}, stErrData(s.calcCharPosAt(start))
	}

	absolute := stIsAbsolute(cmd)
	seg := stPathSegment{Abs: absolute}
	nums := func(out ...*float64) error {
		for _, o := range out {
			v, err := s.parseListNumber()
			if err != nil {
				return err
			}
			*o = v
		}
		return nil
	}
	var err error
	switch stToRelative(cmd) {
	case 'm':
		seg.Kind = stPathMoveTo
		err = nums(&seg.X, &seg.Y)
	case 'l':
		seg.Kind = stPathLineTo
		err = nums(&seg.X, &seg.Y)
	case 'h':
		seg.Kind = stPathHorizontalLineTo
		err = nums(&seg.X)
	case 'v':
		seg.Kind = stPathVerticalLineTo
		err = nums(&seg.Y)
	case 'c':
		seg.Kind = stPathCurveTo
		err = nums(&seg.X1, &seg.Y1, &seg.X2, &seg.Y2, &seg.X, &seg.Y)
	case 's':
		seg.Kind = stPathSmoothCurveTo
		err = nums(&seg.X2, &seg.Y2, &seg.X, &seg.Y)
	case 'q':
		seg.Kind = stPathQuadratic
		err = nums(&seg.X1, &seg.Y1, &seg.X, &seg.Y)
	case 't':
		seg.Kind = stPathSmoothQuadratic
		err = nums(&seg.X, &seg.Y)
	case 'a':
		seg.Kind = stPathEllipticalArc
		if err = nums(&seg.Rx, &seg.Ry, &seg.XAxisRotation); err == nil {
			if seg.LargeArc, err = stParseFlag(s); err == nil {
				if seg.Sweep, err = stParseFlag(s); err == nil {
					err = nums(&seg.X, &seg.Y)
				}
			}
		}
	case 'z':
		seg.Kind = stPathClosePath
	}
	if err != nil {
		return stPathSegment{}, err
	}

	p.hasPrevCmd = true
	if isImplicitMoveTo {
		if absolute {
			p.prevCmd = 'M'
		} else {
			p.prevCmd = 'm'
		}
	} else {
		p.prevCmd = cmd
	}
	return seg, nil
}

func stIsCmd(c byte) bool {
	switch c {
	case 'M', 'm', 'Z', 'z', 'L', 'l', 'H', 'h', 'V', 'v', 'C', 'c', 'S', 's', 'Q', 'q', 'T', 't', 'A', 'a':
		return true
	}
	return false
}

func stIsAbsolute(c byte) bool {
	switch c {
	case 'M', 'Z', 'L', 'H', 'V', 'C', 'S', 'Q', 'T', 'A':
		return true
	}
	return false
}

func stToRelative(c byte) byte {
	if stIsAbsolute(c) {
		return c + ('a' - 'A')
	}
	return c
}

func stIsNumberStart(c byte) bool { return stIsDigit(c) || c == '.' || c == '-' || c == '+' }

// stParseFlag parses an arc flag: a single 0 or 1, which needs no separator after it.
func stParseFlag(s *stStream) (bool, error) {
	s.skipSpaces()
	c, err := s.currByte()
	if err != nil {
		return false, err
	}
	if c != '0' && c != '1' {
		return false, stErrData(s.calcCharPosAt(s.pos))
	}
	s.advance(1)
	if s.isCurrByteEq(',') {
		s.advance(1)
	}
	s.skipSpaces()
	return c == '1', nil
}

// stSimplePathSegmentKind lists the SimplePathSegment variants.
type stSimplePathSegmentKind uint8

const (
	stSimpleMoveTo    stSimplePathSegmentKind = iota // X, Y
	stSimpleLineTo                                   // X, Y
	stSimpleCurveTo                                  // X1, Y1, X2, Y2, X, Y
	stSimpleQuadratic                                // X1, Y1, X, Y
	stSimpleClosePath                                // no payload
)

// stSimplePathSegment is SimplePathSegment: an absolute segment; fields as listed per kind.
type stSimplePathSegment struct {
	Kind                 stSimplePathSegmentKind
	X1, Y1, X2, Y2, X, Y float64
}

// stSimplifyingPathParser is SimplifyingPathParser: PathParser output made absolute, with arcs as
// cubics, smooth segments resolved, H/V as LineTo, and the implicit MoveTo after ClosePath added. It
// yields only MoveTo, LineTo, CurveTo, Quadratic and ClosePath. An error item does not end it; the
// underlying PathParser has ended.
type stSimplifyingPathParser struct {
	parser *stPathParser

	prevMx, prevMy float64 // previous MoveTo
	prevTx, prevTy float64 // previous SmoothQuadratic control point
	prevX, prevY   float64 // current point

	prevSeg           stPathSegment
	hasPrevSimpleSeg  bool
	prevSimpleSegKind stSimplePathSegmentKind

	buffer []stSimplePathSegment
}

func stNewSimplifyingPathParser(text string) *stSimplifyingPathParser {
	return &stSimplifyingPathParser{
		parser:  stNewPathParser(text),
		prevSeg: stPathSegment{Kind: stPathMoveTo, Abs: true},
	}
}

func (p *stSimplifyingPathParser) pop() stSimplePathSegment {
	v := p.buffer[0]
	p.buffer = p.buffer[1:]
	return v
}

func (p *stSimplifyingPathParser) prevIsClosePath() bool {
	return p.hasPrevSimpleSeg && p.prevSimpleSegKind == stSimpleClosePath
}

// Next returns the next simple segment; ok is false at the end.
func (p *stSimplifyingPathParser) Next() (stSimplePathSegment, bool, error) {
	for {
		if len(p.buffer) > 0 {
			return p.pop(), true, nil
		}
		segment, ok, err := p.parser.Next()
		if !ok {
			return stSimplePathSegment{}, false, nil
		}
		if err != nil {
			return stSimplePathSegment{}, true, err
		}
		p.process(segment)
		if len(p.buffer) > 0 {
			return p.pop(), true, nil
		}
		// Nothing was produced (a repeated ClosePath): read the next segment, as Rust's recursion does.
	}
}

func (p *stSimplifyingPathParser) push(seg stSimplePathSegment) { p.buffer = append(p.buffer, seg) }

func (p *stSimplifyingPathParser) process(segment stPathSegment) {
	// A ClosePath followed by anything but MoveTo or ClosePath implies a MoveTo.
	if p.prevIsClosePath() && segment.Kind != stPathMoveTo && segment.Kind != stPathClosePath {
		p.push(stSimplePathSegment{Kind: stSimpleMoveTo, X: p.prevMx, Y: p.prevMy})
		p.hasPrevSimpleSeg, p.prevSimpleSegKind = true, stSimpleMoveTo
	}

	switch segment.Kind {
	case stPathMoveTo:
		x, y := segment.X, segment.Y
		if !segment.Abs {
			// A relative 'm' after a ClosePath is relative to the previous MoveTo.
			if p.prevIsClosePath() {
				x += p.prevMx
				y += p.prevMy
			} else {
				x += p.prevX
				y += p.prevY
			}
		}
		p.push(stSimplePathSegment{Kind: stSimpleMoveTo, X: x, Y: y})
		p.prevSeg = segment
	case stPathLineTo:
		x, y := segment.X, segment.Y
		if !segment.Abs {
			x += p.prevX
			y += p.prevY
		}
		p.push(stSimplePathSegment{Kind: stSimpleLineTo, X: x, Y: y})
		p.prevSeg = segment
	case stPathHorizontalLineTo:
		x := segment.X
		if !segment.Abs {
			x += p.prevX
		}
		p.push(stSimplePathSegment{Kind: stSimpleLineTo, X: x, Y: p.prevY})
		p.prevSeg = segment
	case stPathVerticalLineTo:
		y := segment.Y
		if !segment.Abs {
			y += p.prevY
		}
		p.push(stSimplePathSegment{Kind: stSimpleLineTo, X: p.prevX, Y: y})
		p.prevSeg = segment
	case stPathCurveTo:
		x1, y1, x2, y2, x, y := segment.X1, segment.Y1, segment.X2, segment.Y2, segment.X, segment.Y
		if !segment.Abs {
			x1 += p.prevX
			y1 += p.prevY
			x2 += p.prevX
			y2 += p.prevY
			x += p.prevX
			y += p.prevY
		}
		p.push(stSimplePathSegment{stSimpleCurveTo, x1, y1, x2, y2, x, y})
		// Remember as absolute.
		p.prevSeg = stPathSegment{Kind: stPathCurveTo, Abs: true, X1: x1, Y1: y1, X2: x2, Y2: y2, X: x, Y: y}
	case stPathSmoothCurveTo:
		// The first control point reflects the previous C/S second control point, else the current point.
		var x1, y1 float64
		if k := p.prevSeg.Kind; k == stPathCurveTo || k == stPathSmoothCurveTo {
			x1, y1 = p.prevSeg.X*2-p.prevSeg.X2, p.prevSeg.Y*2-p.prevSeg.Y2
		} else {
			x1, y1 = p.prevX, p.prevY
		}
		x2, y2, x, y := segment.X2, segment.Y2, segment.X, segment.Y
		if !segment.Abs {
			x2 += p.prevX
			y2 += p.prevY
			x += p.prevX
			y += p.prevY
		}
		p.push(stSimplePathSegment{stSimpleCurveTo, x1, y1, x2, y2, x, y})
		p.prevSeg = stPathSegment{Kind: stPathSmoothCurveTo, Abs: true, X2: x2, Y2: y2, X: x, Y: y}
	case stPathQuadratic:
		x1, y1, x, y := segment.X1, segment.Y1, segment.X, segment.Y
		if !segment.Abs {
			x1 += p.prevX
			y1 += p.prevY
			x += p.prevX
			y += p.prevY
		}
		p.push(stSimplePathSegment{Kind: stSimpleQuadratic, X1: x1, Y1: y1, X: x, Y: y})
		p.prevSeg = stPathSegment{Kind: stPathQuadratic, Abs: true, X1: x1, Y1: y1, X: x, Y: y}
	case stPathSmoothQuadratic:
		// The control point reflects the previous Q/T control point, else the current point.
		var x1, y1 float64
		switch p.prevSeg.Kind {
		case stPathQuadratic:
			x1, y1 = p.prevSeg.X*2-p.prevSeg.X1, p.prevSeg.Y*2-p.prevSeg.Y1
		case stPathSmoothQuadratic:
			x1, y1 = p.prevSeg.X*2-p.prevTx, p.prevSeg.Y*2-p.prevTy
		default:
			x1, y1 = p.prevX, p.prevY
		}
		p.prevTx, p.prevTy = x1, y1
		x, y := segment.X, segment.Y
		if !segment.Abs {
			x += p.prevX
			y += p.prevY
		}
		p.push(stSimplePathSegment{Kind: stSimpleQuadratic, X1: x1, Y1: y1, X: x, Y: y})
		p.prevSeg = stPathSegment{Kind: stPathSmoothQuadratic, Abs: true, X: x, Y: y}
	case stPathEllipticalArc:
		x, y := segment.X, segment.Y
		if !segment.Abs {
			x += p.prevX
			y += p.prevY
		}
		arc := kurboSvgArc{
			From:      kurboPoint{p.prevX, p.prevY},
			To:        kurboPoint{x, y},
			Radii:     kurboVec2{segment.Rx, segment.Ry},
			XRotation: stToRadians(segment.XAxisRotation),
			LargeArc:  segment.LargeArc,
			Sweep:     segment.Sweep,
		}
		if a, ok := kurboArcFromSvgArc(&arc); ok {
			a.ToCubicBeziers(0.1, func(p1, p2, p3 kurboPoint) {
				p.push(stSimplePathSegment{stSimpleCurveTo, p1.X, p1.Y, p2.X, p2.Y, p3.X, p3.Y})
			})
		} else {
			p.push(stSimplePathSegment{Kind: stSimpleLineTo, X: x, Y: y})
		}
		p.prevSeg = segment
	case stPathClosePath:
		// Sequential ClosePaths are dropped; they break marker rendering.
		if !p.prevIsClosePath() {
			p.push(stSimplePathSegment{Kind: stSimpleClosePath})
		}
		p.prevSeg = segment
	}

	// Remember the last position.
	if len(p.buffer) > 0 {
		last := p.buffer[len(p.buffer)-1]
		p.hasPrevSimpleSeg, p.prevSimpleSegKind = true, last.Kind
		switch last.Kind {
		case stSimpleMoveTo:
			p.prevX, p.prevY = last.X, last.Y
			p.prevMx, p.prevMy = p.prevX, p.prevY
		case stSimpleClosePath:
			// ClosePath moves to the last MoveTo.
			p.prevX, p.prevY = p.prevMx, p.prevMy
		default:
			p.prevX, p.prevY = last.X, last.Y
		}
	}
}
