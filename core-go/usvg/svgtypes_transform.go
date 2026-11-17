// Port of svgtypes 0.16.1 src/transform.rs

package usvg

// stTransform is Transform, the <transform> type as a matrix.
type stTransform struct {
	A, B, C, D, E, F float64
}

// stNewTransform is Transform::new.
func stNewTransform(a, b, c, d, e, f float64) stTransform { return stTransform{a, b, c, d, e, f} }

// stDefaultTransform is Transform::default(): identity.
func stDefaultTransform() stTransform { return stTransform{1, 0, 0, 1, 0, 0} }

// stTransformListTokenKind lists the TransformListToken variants.
type stTransformListTokenKind uint8

const (
	stTransformMatrix    stTransformListTokenKind = iota // A..F
	stTransformTranslate                                 // Tx, Ty
	stTransformScale                                     // Sx, Sy
	stTransformRotate                                    // Angle
	stTransformSkewX                                     // Angle
	stTransformSkewY                                     // Angle
)

// stTransformListToken is TransformListToken; only the fields named for Kind are set.
type stTransformListToken struct {
	Kind             stTransformListTokenKind
	A, B, C, D, E, F float64
	Tx, Ty           float64
	Sx, Sy           float64
	Angle            float64
}

// stTransformListParser is TransformListParser, a pull parser for <transform-list>. rotate(a cx cy) is
// split into translate(cx cy) rotate(a) translate(-cx -cy). After an error it ends.
type stTransformListParser struct {
	s           stStream
	hasRotateTs bool
	rotateX     float64
	rotateY     float64
	hasAngle    bool
	lastAngle   float64
}

func stNewTransformListParser(text string) *stTransformListParser {
	return &stTransformListParser{s: stNewStream(text)}
}

// Next returns the next token; ok is false at the end.
func (p *stTransformListParser) Next() (stTransformListToken, bool, error) {
	if p.hasAngle {
		p.hasAngle = false
		return stTransformListToken{Kind: stTransformRotate, Angle: p.lastAngle}, true, nil
	}
	if p.hasRotateTs {
		p.hasRotateTs = false
		return stTransformListToken{Kind: stTransformTranslate, Tx: -p.rotateX, Ty: -p.rotateY}, true, nil
	}
	p.s.skipSpaces()
	if p.s.atEnd() {
		// An empty attribute is still a valid value.
		return stTransformListToken{}, false, nil
	}
	t, err := p.parseNext()
	if err != nil {
		p.s.jumpToEnd()
	}
	return t, true, err
}

func (p *stTransformListParser) parseNext() (stTransformListToken, error) {
	s := &p.s
	start := s.pos
	name := s.consumeASCIIIdent()
	s.skipSpaces()
	if err := s.consumeByte('('); err != nil {
		return stTransformListToken{}, err
	}

	// nums parses n list numbers in order, stopping at the first error.
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

	var t stTransformListToken
	switch name {
	case "matrix":
		t.Kind = stTransformMatrix
		if err := nums(&t.A, &t.B, &t.C, &t.D, &t.E, &t.F); err != nil {
			return stTransformListToken{}, err
		}
	case "translate":
		t.Kind = stTransformTranslate
		if err := nums(&t.Tx); err != nil {
			return stTransformListToken{}, err
		}
		s.skipSpaces()
		// 'If <ty> is not provided, it is assumed to be zero.'
		if !s.isCurrByteEq(')') {
			if err := nums(&t.Ty); err != nil {
				return stTransformListToken{}, err
			}
		}
	case "scale":
		t.Kind = stTransformScale
		if err := nums(&t.Sx); err != nil {
			return stTransformListToken{}, err
		}
		s.skipSpaces()
		// 'If <sy> is not provided, it is assumed to be equal to <sx>.'
		if s.isCurrByteEq(')') {
			t.Sy = t.Sx
		} else if err := nums(&t.Sy); err != nil {
			return stTransformListToken{}, err
		}
	case "rotate":
		var a float64
		if err := nums(&a); err != nil {
			return stTransformListToken{}, err
		}
		s.skipSpaces()
		if !s.isCurrByteEq(')') {
			// rotate(a cx cy) is translate(cx cy) rotate(a) translate(-cx -cy).
			var cx, cy float64
			if err := nums(&cx, &cy); err != nil {
				return stTransformListToken{}, err
			}
			p.hasRotateTs, p.rotateX, p.rotateY = true, cx, cy
			p.hasAngle, p.lastAngle = true, a
			t = stTransformListToken{Kind: stTransformTranslate, Tx: cx, Ty: cy}
		} else {
			t = stTransformListToken{Kind: stTransformRotate, Angle: a}
		}
	case "skewX":
		t.Kind = stTransformSkewX
		if err := nums(&t.Angle); err != nil {
			return stTransformListToken{}, err
		}
	case "skewY":
		t.Kind = stTransformSkewY
		if err := nums(&t.Angle); err != nil {
			return stTransformListToken{}, err
		}
	default:
		return stTransformListToken{}, stErrData(s.calcCharPosAt(start))
	}

	s.skipSpaces()
	if err := s.consumeByte(')'); err != nil {
		return stTransformListToken{}, err
	}
	s.skipSpaces()
	if s.isCurrByteEq(',') {
		s.advance(1)
	}
	return t, nil
}

// stParseTransform is Transform::from_str: the product of a transform list.
func stParseTransform(text string) (stTransform, error) {
	p := stNewTransformListParser(text)
	ts := stDefaultTransform()
	for {
		t, ok, err := p.Next()
		if !ok {
			break
		}
		if err != nil {
			return stTransform{}, err
		}
		switch t.Kind {
		case stTransformMatrix:
			ts = stMultiplyTransform(ts, stTransform{t.A, t.B, t.C, t.D, t.E, t.F})
		case stTransformTranslate:
			ts = stMultiplyTransform(ts, stTransform{1, 0, 0, 1, t.Tx, t.Ty})
		case stTransformScale:
			ts = stMultiplyTransform(ts, stTransform{t.Sx, 0, 0, t.Sy, 0, 0})
		case stTransformRotate:
			v := stToRadians(t.Angle)
			a := stCos(v)
			b := stSin(v)
			ts = stMultiplyTransform(ts, stTransform{a, b, -b, a, 0, 0})
		case stTransformSkewX:
			c := stTan(stToRadians(t.Angle))
			ts = stMultiplyTransform(ts, stTransform{1, 0, c, 1, 0, 0})
		case stTransformSkewY:
			b := stTan(stToRadians(t.Angle))
			ts = stMultiplyTransform(ts, stTransform{1, b, 0, 1, 0, 0})
		}
	}
	return ts, nil
}

func stMultiplyTransform(ts1, ts2 stTransform) stTransform {
	return stTransform{
		A: float64(ts1.A*ts2.A) + float64(ts1.C*ts2.B),
		B: float64(ts1.B*ts2.A) + float64(ts1.D*ts2.B),
		C: float64(ts1.A*ts2.C) + float64(ts1.C*ts2.D),
		D: float64(ts1.B*ts2.C) + float64(ts1.D*ts2.D),
		E: float64(ts1.A*ts2.E) + float64(ts1.C*ts2.F) + ts1.E,
		F: float64(ts1.B*ts2.E) + float64(ts1.D*ts2.F) + ts1.F,
	}
}
