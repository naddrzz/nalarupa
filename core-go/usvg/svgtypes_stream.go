// Port of svgtypes 0.16.1 src/stream.rs

package usvg

import "unicode/utf8"

func stIsSign(c byte) bool  { return c == '+' || c == '-' }
func stIsDigit(c byte) bool { return c >= '0' && c <= '9' }
func stIsHexDigit(c byte) bool {
	return stIsDigit(c) || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')
}
func stIsSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func stIsASCIIIdent(c byte) bool {
	return stIsDigit(c) || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '-' || c == '_'
}

// stIsNonASCII is CharExt::is_non_ascii, which svgtypes defines as a code point above 237.
func stIsNonASCII(c rune) bool { return c > 237 }

func stIsNameStart(c rune) bool {
	switch {
	case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	}
	return stIsNonASCII(c) // is_escape is always false in svgtypes
}

func stIsNameChar(c rune) bool {
	switch {
	case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
		return true
	}
	return stIsNonASCII(c)
}

// stStream is svgtypes' Stream: a text and a byte position. It is a value type; copying it saves the
// position, as Rust's Copy does.
type stStream struct {
	text string
	pos  int
}

// stNewStream is Stream::from.
func stNewStream(text string) stStream { return stStream{text: text} }

// Pos returns the current byte position.
func (s *stStream) Pos() int { return s.pos }

// calcCharPos is the current position in chars, 1-based.
func (s *stStream) calcCharPos() int { return s.calcCharPosAt(s.pos) }

// calcCharPosAt is the 1-based char position of bytePos.
func (s *stStream) calcCharPosAt(bytePos int) int {
	pos := 1
	for idx := range s.text {
		if idx >= bytePos {
			break
		}
		pos++
	}
	return pos
}

// jumpToEnd sets the position to the end, used to stop parsing after an error.
func (s *stStream) jumpToEnd() { s.pos = len(s.text) }

func (s *stStream) atEnd() bool { return s.pos >= len(s.text) }

// currByte errors with UnexpectedEndOfStream at the end.
func (s *stStream) currByte() (byte, error) {
	if s.atEnd() {
		return 0, stErrEOS()
	}
	return s.text[s.pos], nil
}

func (s *stStream) currByteUnchecked() byte { return s.text[s.pos] }

func (s *stStream) isCurrByteEq(c byte) bool { return !s.atEnd() && s.text[s.pos] == c }

// nextByte returns the byte after the current one.
func (s *stStream) nextByte() (byte, error) {
	if s.pos+1 >= len(s.text) {
		return 0, stErrEOS()
	}
	return s.text[s.pos+1], nil
}

func (s *stStream) advance(n int) { s.pos += n }

// skipSpaces skips ' ', \n, \r and \t.
func (s *stStream) skipSpaces() {
	for !s.atEnd() && stIsSpace(s.text[s.pos]) {
		s.pos++
	}
}

func (s *stStream) startsWith(text string) bool {
	return len(s.text)-s.pos >= len(text) && s.text[s.pos:s.pos+len(text)] == text
}

// consumeByte consumes c or errors with InvalidChar / UnexpectedEndOfStream.
func (s *stStream) consumeByte(c byte) error {
	b, err := s.currByte()
	if err != nil {
		return err
	}
	if b != c {
		return stErrChar([]byte{b, c}, s.calcCharPos())
	}
	s.advance(1)
	return nil
}

// parseIdent parses a CSS ident.
func (s *stStream) parseIdent() (string, error) {
	start := s.pos
	if s.isCurrByteEq('-') {
		s.advance(1)
	}
	rest := s.text[s.pos:]
	if c, size := utf8.DecodeRuneInString(rest); len(rest) > 0 {
		if !stIsNameStart(c) {
			return "", stErrIdent()
		}
		s.advance(size)
		rest = rest[size:]
		for len(rest) > 0 {
			c, size := utf8.DecodeRuneInString(rest)
			if !stIsNameChar(c) {
				break
			}
			s.advance(size)
			rest = rest[size:]
		}
	}
	if start == s.pos {
		return "", stErrIdent()
	}
	return s.sliceBack(start), nil
}

// consumeASCIIIdent consumes ASCII ident bytes, possibly none.
func (s *stStream) consumeASCIIIdent() string {
	start := s.pos
	s.skipBytes(stIsASCIIIdent)
	return s.sliceBack(start)
}

// parseQuotedString parses a '...' or "..." string and returns its contents.
func (s *stStream) parseQuotedString() (string, error) {
	quote, err := s.currByte()
	if err != nil {
		return "", err
	}
	if quote != '\'' && quote != '"' {
		return "", stErrValue()
	}
	prev := quote
	s.advance(1)
	start := s.pos
	for !s.atEnd() {
		curr := s.text[s.pos]
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

// consumeString consumes text or errors with InvalidString / UnexpectedEndOfStream.
func (s *stStream) consumeString(text string) error {
	if s.atEnd() {
		return stErrEOS()
	}
	if !s.startsWith(text) {
		n := min(len(text), len(s.text)-s.pos)
		// Take n chars (not bytes), as Rust does.
		rest := s.text[s.pos:]
		end := 0
		for i := 0; i < n && end < len(rest); i++ {
			_, size := utf8.DecodeRuneInString(rest[end:])
			end += size
		}
		return stErrString([]string{rest[:end], text}, s.calcCharPos())
	}
	s.advance(len(text))
	return nil
}

// consumeBytes consumes bytes while f holds and returns them.
func (s *stStream) consumeBytes(f func(byte) bool) string {
	start := s.pos
	s.skipBytes(f)
	return s.sliceBack(start)
}

func (s *stStream) skipBytes(f func(byte) bool) {
	for !s.atEnd() && f(s.text[s.pos]) {
		s.pos++
	}
}

func (s *stStream) sliceBack(pos int) string { return s.text[pos:s.pos] }

func (s *stStream) sliceTail() string { return s.text[s.pos:] }

// parseNumberOrPercent parses a number, dividing a percentage by 100.
func (s *stStream) parseNumberOrPercent() (float64, error) {
	s.skipSpaces()
	n, err := s.parseNumber()
	if err != nil {
		return 0, err
	}
	if s.startsWith("%") {
		s.advance(1)
		return n / 100, nil
	}
	return n, nil
}

// parseListNumberOrPercent parses a number or percentage from a list.
func (s *stStream) parseListNumberOrPercent() (float64, error) {
	if s.atEnd() {
		return 0, stErrEOS()
	}
	l, err := s.parseNumberOrPercent()
	if err != nil {
		return 0, err
	}
	s.skipSpaces()
	s.parseListSeparator()
	return l, nil
}

func (s *stStream) skipDigits() { s.skipBytes(stIsDigit) }

func (s *stStream) parseListSeparator() {
	if s.isCurrByteEq(',') {
		s.advance(1)
	}
}
