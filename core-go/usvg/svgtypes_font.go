// Port of svgtypes 0.16.1 src/font.rs

package usvg

import (
	"strings"
	"unicode/utf8"
)

// stParseFontFamilies is parse_font_families: a font-family list with nothing after it but spaces.
func stParseFontFamilies(text string) ([]stFontFamily, error) {
	s := stNewStream(text)
	families, err := s.parseFontFamilies()
	if err != nil {
		return nil, err
	}
	s.skipSpaces()
	if !s.atEnd() {
		return nil, stErrData(s.calcCharPos())
	}
	return families, nil
}

// stFontFamilyKind lists the FontFamily variants.
type stFontFamilyKind uint8

const (
	stFontFamilySerif stFontFamilyKind = iota
	stFontFamilySansSerif
	stFontFamilyCursive
	stFontFamilyFantasy
	stFontFamilyMonospace
	stFontFamilyNamed // Name is set
)

// stFontFamily is FontFamily: a generic family or a named one.
type stFontFamily struct {
	Kind stFontFamilyKind
	Name string // stFontFamilyNamed
}

// String is FontFamily's Display: the generic keyword, or the name in double quotes.
func (f stFontFamily) String() string {
	switch f.Kind {
	case stFontFamilyMonospace:
		return "monospace"
	case stFontFamilySerif:
		return "serif"
	case stFontFamilySansSerif:
		return "sans-serif"
	case stFontFamilyCursive:
		return "cursive"
	case stFontFamilyFantasy:
		return "fantasy"
	}
	return "\"" + f.Name + "\""
}

// parseFontFamilies parses a comma-separated list of quoted names, generic families and unquoted names
// (idents joined by single spaces). Empty names are dropped.
func (s *stStream) parseFontFamilies() ([]stFontFamily, error) {
	var families []stFontFamily
	for !s.atEnd() {
		s.skipSpaces()

		ch, err := s.currByte()
		if err != nil {
			return nil, err
		}
		var family stFontFamily
		if ch == '\'' || ch == '"' {
			res, err := s.parseQuotedString()
			if err != nil {
				return nil, err
			}
			family = stFontFamily{Kind: stFontFamilyNamed, Name: res}
		} else {
			var idents []string
			for {
				c, _ := utf8.DecodeRuneInString(s.sliceTail())
				if s.atEnd() || c == ',' {
					break
				}
				ident, err := s.parseIdent()
				if err != nil {
					return nil, err
				}
				idents = append(idents, ident)
				s.skipSpaces()
			}
			// TODO (Rust): no CSS keyword must be matched as a family name.
			switch joined := strings.Join(idents, " "); joined {
			case "serif":
				family = stFontFamily{Kind: stFontFamilySerif}
			case "sans-serif":
				family = stFontFamily{Kind: stFontFamilySansSerif}
			case "cursive":
				family = stFontFamily{Kind: stFontFamilyCursive}
			case "fantasy":
				family = stFontFamily{Kind: stFontFamilyFantasy}
			case "monospace":
				family = stFontFamily{Kind: stFontFamilyMonospace}
			default:
				family = stFontFamily{Kind: stFontFamilyNamed, Name: joined}
			}
		}
		families = append(families, family)

		if b, err := s.currByte(); err == nil {
			if b == ',' {
				s.advance(1)
			} else {
				break
			}
		}
	}

	out := families[:0]
	for _, f := range families {
		if f.Kind != stFontFamilyNamed || f.Name != "" {
			out = append(out, f)
		}
	}
	return out, nil
}

// stFontShorthand is FontShorthand, the values of the `font` shorthand. An empty optional field is
// Rust's None (a present value is never empty).
type stFontShorthand struct {
	FontStyle   string
	FontVariant string
	FontWeight  string
	FontStretch string
	FontSize    string
	FontFamily  string
}

// stParseFontShorthand is FontShorthand::from_str. Its UnexpectedData positions are byte offsets, as
// in Rust.
func stParseFontShorthand(text string) (stFontShorthand, error) {
	s := stNewStream(text)
	s.skipSpaces()
	prevPos := s.pos
	var r stFontShorthand

	for range 4 {
		ident := s.consumeASCIIIdent()
		stop := false
		switch ident {
		case "normal":
			// Ignored: it's ambiguous which property it belongs to, and the others reset anyway.
		case "small-caps":
			r.FontVariant = ident
		case "italic", "oblique":
			r.FontStyle = ident
		case "bold", "bolder", "lighter", "100", "200", "300", "400", "500", "600", "700", "800", "900":
			r.FontWeight = ident
		case "ultra-condensed", "extra-condensed", "condensed", "semi-condensed", "semi-expanded",
			"expanded", "extra-expanded", "ultra-expanded":
			r.FontStretch = ident
		default:
			// Not one of the 4 properties: backtrack and parse the size and family.
			s = stNewStream(text)
			s.advance(prevPos)
			stop = true
		}
		if stop {
			break
		}
		s.skipSpaces()
		prevPos = s.pos
	}

	prevPos = s.pos
	c, err := s.currByte()
	if err != nil {
		return stFontShorthand{}, err
	}
	if stIsDigit(c) {
		// A font size such as '15pt'.
		if _, err := s.parseLength(); err != nil {
			return stFontShorthand{}, err
		}
	} else {
		// A font size like 'xx-large'.
		switch s.consumeASCIIIdent() {
		case "xx-small", "x-small", "small", "medium", "large", "x-large", "xx-large", "larger", "smaller":
		default:
			return stFontShorthand{}, stErrData(prevPos)
		}
	}
	r.FontSize = s.sliceBack(prevPos)
	s.skipSpaces()

	c, err = s.currByte()
	if err != nil {
		return stFontShorthand{}, err
	}
	if c == '/' {
		// Line height has no effect in SVG; skip it.
		s.advance(1)
		s.skipSpaces()
		if _, err := s.parseLength(); err != nil {
			return stFontShorthand{}, err
		}
		s.skipSpaces()
	}
	if s.atEnd() {
		return stFontShorthand{}, stErrEOS()
	}
	r.FontFamily = s.sliceTail()
	return r, nil
}
