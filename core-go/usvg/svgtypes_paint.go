// Port of svgtypes 0.16.1 src/paint.rs

package usvg

import "strings"

// stPaintFallbackKind lists the PaintFallback variants.
type stPaintFallbackKind uint8

const (
	stPaintFallbackNone stPaintFallbackKind = iota
	stPaintFallbackCurrentColor
	stPaintFallbackColor // Color is set
)

// stPaintFallback is PaintFallback, the fallback part of <paint>.
type stPaintFallback struct {
	Kind  stPaintFallbackKind
	Color stColor // stPaintFallbackColor only
}

// stPaintKind lists the Paint variants.
type stPaintKind uint8

const (
	stPaintNone stPaintKind = iota
	stPaintInherit
	stPaintCurrentColor
	stPaintColor   // Color is set
	stPaintFuncIRI // Link is set; HasFallback/Fallback are Option<PaintFallback>
	stPaintContextFill
	stPaintContextStroke
)

// stPaint is Paint, the <paint> type. <icccolor> is not supported.
type stPaint struct {
	Kind        stPaintKind
	Color       stColor         // stPaintColor
	Link        string          // stPaintFuncIRI
	HasFallback bool            // stPaintFuncIRI: false is None
	Fallback    stPaintFallback // stPaintFuncIRI when HasFallback
}

// stParsePaint is Paint::from_str. The text is trimmed of Unicode whitespace first.
func stParsePaint(text string) (stPaint, error) {
	text = strings.TrimSpace(text) // str::trim and TrimSpace both trim the Unicode White_Space set
	switch text {
	case "none":
		return stPaint{Kind: stPaintNone}, nil
	case "inherit":
		return stPaint{Kind: stPaintInherit}, nil
	case "currentColor":
		return stPaint{Kind: stPaintCurrentColor}, nil
	case "context-fill":
		return stPaint{Kind: stPaintContextFill}, nil
	case "context-stroke":
		return stPaint{Kind: stPaintContextStroke}, nil
	}
	s := stNewStream(text)
	if s.startsWith("url(") {
		link, err := s.parseFuncIRI()
		if err != nil {
			return stPaint{}, stErrValue()
		}
		p := stPaint{Kind: stPaintFuncIRI, Link: link}
		s.skipSpaces()
		// get fallback
		if !s.atEnd() {
			p.HasFallback = true
			switch fallback := s.sliceTail(); fallback {
			case "none":
				p.Fallback = stPaintFallback{Kind: stPaintFallbackNone}
			case "currentColor":
				p.Fallback = stPaintFallback{Kind: stPaintFallbackCurrentColor}
			default:
				c, err := stParseColor(fallback)
				if err != nil {
					return stPaint{}, err
				}
				p.Fallback = stPaintFallback{Kind: stPaintFallbackColor, Color: c}
			}
		}
		return p, nil
	}
	c, err := stParseColor(text)
	if err != nil {
		return stPaint{}, stErrValue()
	}
	return stPaint{Kind: stPaintColor, Color: c}, nil
}
