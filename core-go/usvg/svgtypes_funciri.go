// Port of svgtypes 0.16.1 src/funciri.rs

package usvg

import (
	"strings"
	"unicode"
)

// stIRI is IRI, the <IRI> type: the link without the leading '#'.
type stIRI string

// stFuncIRI is FuncIRI, the <FuncIRI> type: the link inside url(#...).
type stFuncIRI string

// stParseIRI is IRI::from_str. Any characters are accepted in the ID, as in Rust.
func stParseIRI(text string) (stIRI, error) {
	s := stNewStream(text)
	link, err := s.parseIRI()
	if err != nil {
		return "", err
	}
	s.skipSpaces()
	if !s.atEnd() {
		return "", stErrData(s.calcCharPos())
	}
	return stIRI(link), nil
}

// stParseFuncIRI is FuncIRI::from_str.
func stParseFuncIRI(text string) (stFuncIRI, error) {
	s := stNewStream(text)
	link, err := s.parseFuncIRI()
	if err != nil {
		return "", err
	}
	s.skipSpaces()
	if !s.atEnd() {
		return "", stErrData(s.calcCharPos())
	}
	return stFuncIRI(link), nil
}

// parseIRI parses `#id` and returns id (everything up to a space).
func (s *stStream) parseIRI() (string, error) {
	s.skipSpaces()
	if err := s.consumeByte('#'); err != nil {
		return "", err
	}
	link := s.consumeBytes(func(c byte) bool { return c != ' ' })
	if link == "" {
		return "", stErrValue()
	}
	return link, nil
}

// parseFuncIRI parses `url(#id)`, optionally with the #id quoted, and returns id.
func (s *stStream) parseFuncIRI() (string, error) {
	s.skipSpaces()
	if err := s.consumeString("url("); err != nil {
		return "", err
	}
	s.skipSpaces()

	var quote byte
	if c, err := s.currByte(); err == nil && (c == '\'' || c == '"') {
		quote = c
		s.advance(1)
		s.skipSpaces()
	}
	if err := s.consumeByte('#'); err != nil {
		return "", err
	}
	var link string
	if quote != 0 {
		link = strings.TrimRightFunc(s.consumeBytes(func(c byte) bool { return c != quote }), unicode.IsSpace)
	} else {
		link = s.consumeBytes(func(c byte) bool { return c != ' ' && c != ')' })
	}
	if link == "" {
		return "", stErrValue()
	}
	// Non-paired quotes are an error.
	if strings.ContainsAny(link, "'\"") {
		return "", stErrValue()
	}
	s.skipSpaces()
	if quote != 0 {
		if err := s.consumeByte(quote); err != nil {
			return "", err
		}
		s.skipSpaces()
	}
	if err := s.consumeByte(')'); err != nil {
		return "", err
	}
	return link, nil
}
