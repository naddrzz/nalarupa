// Port of simplecss 0.2.2 src/stream.rs (and the Error/TextPos types of src/lib.rs).

package usvg

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// cssErrorKind lists simplecss::Error variants.
type cssErrorKind uint8

const (
	cssErrUnexpectedEndOfStream cssErrorKind = iota
	cssErrInvalidIdent
	cssErrInvalidComment
	cssErrInvalidValue
	cssErrInvalidByte
	cssErrSelectorMissing
	cssErrUnexpectedSelector
	cssErrUnexpectedCombinator
	cssErrInvalidAttributeSelector
	cssErrInvalidLanguagePseudoClass
)

// cssTextPos is simplecss::TextPos: a 1-based row and column (in chars).
type cssTextPos struct{ Row, Col uint32 }

func (p cssTextPos) String() string { return fmt.Sprintf("%d:%d", p.Row, p.Col) }

// cssError is simplecss::Error. Pos is set for InvalidIdent, InvalidComment, InvalidValue and
// InvalidByte; Expected and Actual only for InvalidByte.
type cssError struct {
	Kind             cssErrorKind
	Pos              cssTextPos
	Expected, Actual byte
}

// Error matches simplecss's Display.
func (e *cssError) Error() string {
	switch e.Kind {
	case cssErrUnexpectedEndOfStream:
		return "unexpected end of stream"
	case cssErrInvalidIdent:
		return "invalid ident at " + e.Pos.String()
	case cssErrInvalidComment:
		return "invalid comment at " + e.Pos.String()
	case cssErrInvalidValue:
		return "invalid value at " + e.Pos.String()
	case cssErrInvalidByte:
		// `byte as char` maps a byte to U+0000..U+00FF.
		return fmt.Sprintf("expected '%c' not '%c' at %s", rune(e.Expected), rune(e.Actual), e.Pos)
	case cssErrSelectorMissing:
		return "selector missing"
	case cssErrUnexpectedSelector:
		return "unexpected selector"
	case cssErrUnexpectedCombinator:
		return "unexpected combinator"
	case cssErrInvalidAttributeSelector:
		return "invalid or unsupported attribute selector"
	case cssErrInvalidLanguagePseudoClass:
		return "invalid language pseudo-class"
	}
	return "unknown css error"
}

var cssErrEOS = &cssError{Kind: cssErrUnexpectedEndOfStream}

// cssIsNameStart is CssCharExt::is_name_start.
func cssIsNameStart(c rune) bool {
	switch {
	case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	}
	return cssIsNonASCII(c) // is_escape is always false
}

// cssIsNameChar is CssCharExt::is_name_char.
func cssIsNameChar(c rune) bool {
	switch {
	case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		return true
	}
	return cssIsNonASCII(c)
}

// cssIsNonASCII is CssCharExt::is_non_ascii; note that simplecss uses > 237, not > 127.
func cssIsNonASCII(c rune) bool { return uint32(c) > 237 }

// cssStream is simplecss's byte stream over a string.
type cssStream struct {
	text     string
	pos, end int
}

func cssNewStream(text string) cssStream { return cssStream{text: text, end: len(text)} }

func (s *cssStream) jumpToEnd()  { s.pos = s.end }
func (s *cssStream) atEnd() bool { return s.pos >= s.end }

func (s *cssStream) currByte() (byte, *cssError) {
	if s.atEnd() {
		return 0, cssErrEOS
	}
	return s.text[s.pos], nil
}

// currIs reports `s.curr_byte() == Ok(c)`.
func (s *cssStream) currIs(c byte) bool { return !s.atEnd() && s.text[s.pos] == c }

func (s *cssStream) nextByte() (byte, *cssError) {
	if s.pos+1 >= s.end {
		return 0, cssErrEOS
	}
	return s.text[s.pos+1], nil
}

func (s *cssStream) advance(n int) { s.pos += n }

func (s *cssStream) consumeByte(c byte) *cssError {
	b, err := s.currByte()
	if err != nil {
		return err
	}
	if b != c {
		return &cssError{Kind: cssErrInvalidByte, Expected: c, Actual: b, Pos: s.genTextPos()}
	}
	s.advance(1)
	return nil
}

func (s *cssStream) tryConsumeByte(c byte) {
	if s.currIs(c) {
		s.advance(1)
	}
}

func (s *cssStream) consumeBytes(f func(byte) bool) string {
	start := s.pos
	s.skipBytes(f)
	return s.sliceBack(start)
}

func (s *cssStream) skipBytes(f func(byte) bool) {
	for !s.atEnd() && f(s.text[s.pos]) {
		s.advance(1)
	}
}

func (s *cssStream) sliceRange(start, end int) string { return s.text[start:end] }
func (s *cssStream) sliceBack(pos int) string         { return s.text[pos:s.pos] }
func (s *cssStream) sliceTail() string                { return s.text[s.pos:] }

func (s *cssStream) skipSpaces() {
	for !s.atEnd() {
		switch s.text[s.pos] {
		case ' ', '\t', '\n', '\r', '\x0C':
			s.advance(1)
		default:
			return
		}
	}
}

func (s *cssStream) skipSpacesAndComments() *cssError {
	s.skipSpaces()
	for s.currIs('/') {
		if b, err := s.nextByte(); err != nil || b != '*' {
			break
		}
		if err := s.skipComment(); err != nil {
			return err
		}
		s.skipSpaces()
	}
	return nil
}

func (s *cssStream) consumeIdent() (string, *cssError) {
	start := s.pos
	if s.currIs('-') {
		s.advance(1)
	}

	// Iterate chars of text[pos:end]; invalid UTF-8 decodes as U+FFFD, a name char.
	rest := s.text[s.pos:s.end]
	if rest != "" {
		c, size := utf8.DecodeRuneInString(rest)
		if !cssIsNameStart(c) {
			return "", &cssError{Kind: cssErrInvalidIdent, Pos: s.genTextPosFrom(start)}
		}
		s.advance(size)
		rest = rest[size:]
		for rest != "" {
			c, size := utf8.DecodeRuneInString(rest)
			if !cssIsNameChar(c) {
				break
			}
			s.advance(size)
			rest = rest[size:]
		}
	}

	if start == s.pos {
		return "", &cssError{Kind: cssErrInvalidIdent, Pos: s.genTextPosFrom(start)}
	}
	return s.sliceBack(start), nil
}

func (s *cssStream) consumeString() (string, *cssError) {
	quote, err := s.currByte()
	if err != nil {
		return "", err
	}
	if quote != '\'' && quote != '"' {
		return s.consumeIdent()
	}
	prev := quote
	s.advance(1)
	start := s.pos
	for !s.atEnd() {
		curr := s.text[s.pos]
		// Advance until the closing quote, unless it is escaped.
		if curr == quote && prev != '\\' {
			break
		}
		prev = curr
		s.advance(1)
	}
	value := s.sliceBack(start)
	if err := s.consumeByte(quote); err != nil {
		return "", err
	}
	return value, nil
}

func (s *cssStream) skipComment() *cssError {
	start := s.pos
	if s.skipCommentImpl() != nil {
		return &cssError{Kind: cssErrInvalidComment, Pos: s.genTextPosFrom(start)}
	}
	return nil
}

func (s *cssStream) skipCommentImpl() *cssError {
	if err := s.consumeByte('/'); err != nil {
		return err
	}
	if err := s.consumeByte('*'); err != nil {
		return err
	}
	for !s.atEnd() {
		if s.text[s.pos] == '*' {
			if b, err := s.nextByte(); err == nil && b == '/' {
				break
			}
		}
		s.advance(1)
	}
	if err := s.consumeByte('*'); err != nil {
		return err
	}
	return s.consumeByte('/')
}

func (s *cssStream) genTextPos() cssTextPos {
	return cssTextPos{Row: cssCalcRow(s.text, s.pos), Col: cssCalcCol(s.text, s.pos)}
}

func (s *cssStream) genTextPosFrom(pos int) cssTextPos {
	c := *s
	c.pos = min(pos, len(s.text))
	return c.genTextPos()
}

func cssCalcRow(text string, end int) uint32 {
	return 1 + uint32(strings.Count(text[:end], "\n"))
}

// cssCalcCol counts the chars between the last newline before end and end, plus one.
func cssCalcCol(text string, end int) uint32 {
	line := text[:end]
	if i := strings.LastIndexByte(line, '\n'); i >= 0 {
		line = line[i+1:]
	}
	return 1 + uint32(utf8.RuneCountInString(line))
}
