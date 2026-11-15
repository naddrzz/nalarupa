// Port of svgtypes 0.16.1 src/aspect_ratio.rs

package usvg

// stAlign is Align, the align value of preserveAspectRatio (same order as the Rust enum).
type stAlign uint8

const (
	stAlignNone stAlign = iota
	stAlignXMinYMin
	stAlignXMidYMin
	stAlignXMaxYMin
	stAlignXMinYMid
	stAlignXMidYMid
	stAlignXMaxYMid
	stAlignXMinYMax
	stAlignXMidYMax
	stAlignXMaxYMax
)

// stAspectRatio is AspectRatio, the preserveAspectRatio attribute. Defer is the SVG 1.1 `defer`
// keyword; Slice is true for `slice`, false for `meet` or when absent.
type stAspectRatio struct {
	Defer bool
	Align stAlign
	Slice bool
}

// stDefaultAspectRatio is AspectRatio::default(): xMidYMid meet.
func stDefaultAspectRatio() stAspectRatio { return stAspectRatio{Align: stAlignXMidYMid} }

// stParseAspectRatio is AspectRatio::from_str.
func stParseAspectRatio(text string) (stAspectRatio, error) {
	s := stNewStream(text)
	s.skipSpaces()

	def := s.startsWith("defer")
	if def {
		s.advance(5)
		if err := s.consumeByte(' '); err != nil {
			return stAspectRatio{}, err
		}
		s.skipSpaces()
	}

	start := s.pos
	var align stAlign
	switch s.consumeASCIIIdent() {
	case "none":
		align = stAlignNone
	case "xMinYMin":
		align = stAlignXMinYMin
	case "xMidYMin":
		align = stAlignXMidYMin
	case "xMaxYMin":
		align = stAlignXMaxYMin
	case "xMinYMid":
		align = stAlignXMinYMid
	case "xMidYMid":
		align = stAlignXMidYMid
	case "xMaxYMid":
		align = stAlignXMaxYMid
	case "xMinYMax":
		align = stAlignXMinYMax
	case "xMidYMax":
		align = stAlignXMidYMax
	case "xMaxYMax":
		align = stAlignXMaxYMax
	default:
		return stAspectRatio{}, stErrData(s.calcCharPosAt(start))
	}

	s.skipSpaces()
	slice := false
	if !s.atEnd() {
		start := s.pos
		switch s.consumeASCIIIdent() {
		case "meet", "":
		case "slice":
			slice = true
		default:
			return stAspectRatio{}, stErrData(s.calcCharPosAt(start))
		}
	}
	return stAspectRatio{Defer: def, Align: align, Slice: slice}, nil
}
