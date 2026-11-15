// Port of simplecss 0.2.2 src/lib.rs: a CSS 2.1 style sheet parser with selector matching. At-rules are
// skipped, values are kept as strings, and parsing is case sensitive. simplecss logs warnings through the
// `log` crate; this port drops them.

package usvg

import (
	"slices"
	"strings"
)

// cssDeclaration is simplecss::Declaration.
type cssDeclaration struct {
	Name, Value string
	Important   bool
}

// cssRule is simplecss::Rule.
type cssRule struct {
	Selector     cssSelector
	Declarations []cssDeclaration
}

// cssStyleSheet is simplecss::StyleSheet; Rules are sorted by specificity.
type cssStyleSheet struct {
	Rules []cssRule
}

// cssNewStyleSheet creates an empty style sheet.
func cssNewStyleSheet() *cssStyleSheet { return &cssStyleSheet{} }

// cssParseStyleSheet parses a style sheet; it never fails, at worst giving an empty sheet.
func cssParseStyleSheet(text string) *cssStyleSheet {
	sheet := cssNewStyleSheet()
	sheet.ParseMore(text)
	return sheet
}

// ParseMore parses text into the sheet, then drops rules without declarations and stably re-sorts all
// rules by specificity (Rust's sort_by_cached_key is stable).
func (sh *cssStyleSheet) ParseMore(text string) {
	s := cssNewStream(text)
	if s.skipSpacesAndComments() != nil {
		return
	}
	for !s.atEnd() {
		if s.skipSpacesAndComments() != nil {
			break
		}
		_ = cssConsumeStatement(&s, &sh.Rules)
	}

	sh.Rules = slices.DeleteFunc(sh.Rules, func(r cssRule) bool { return len(r.Declarations) == 0 })
	slices.SortStableFunc(sh.Rules, func(a, b cssRule) int {
		sa, sb := a.Selector.Specificity(), b.Selector.Specificity()
		for i := range sa {
			if sa[i] != sb[i] {
				return int(sa[i]) - int(sb[i])
			}
		}
		return 0
	})
}

// String matches simplecss's Display for StyleSheet.
func (sh *cssStyleSheet) String() string {
	var b strings.Builder
	for i, rule := range sh.Rules {
		b.WriteString(rule.Selector.String() + " { ")
		for _, d := range rule.Declarations {
			b.WriteString(d.Name + ":" + d.Value)
			if d.Important {
				b.WriteString(" !important")
			}
			b.WriteString(";")
		}
		b.WriteString(" }")
		if i != len(sh.Rules)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func cssConsumeStatement(s *cssStream, rules *[]cssRule) *cssError {
	if s.currIs('@') {
		s.advance(1)
		return cssConsumeAtRule(s)
	}
	return cssConsumeRuleSet(s, rules)
}

func cssConsumeAtRule(s *cssStream) *cssError {
	if _, err := s.consumeIdent(); err != nil {
		return err
	}
	// The at-rule is not supported and is skipped.
	s.skipBytes(func(c byte) bool { return c != ';' && c != '{' })
	b, err := s.currByte()
	if err != nil {
		return err
	}
	switch b {
	case ';':
		s.advance(1)
	case '{':
		cssConsumeBlock(s)
	}
	return nil
}

func cssConsumeRuleSet(s *cssStream, rules *[]cssRule) *cssError {
	start := len(*rules)

	for {
		b, err := s.currByte()
		if err != nil {
			return err
		}
		if b != ',' && start != len(*rules) {
			break
		}
		if b == ',' {
			s.advance(1)
		}

		selector, ok, offset := cssParseSelectorPrefix(s.sliceTail())
		s.advance(offset)
		s.skipSpaces()

		if ok {
			*rules = append(*rules, cssRule{Selector: selector})
		}

		b, err = s.currByte()
		if err != nil {
			return err
		}
		if b == '{' {
			break
		}
		if b != ',' {
			s.skipBytes(func(c byte) bool { return c != '{' })
			break
		}
	}

	s.tryConsumeByte('{')

	declarations := cssConsumeDeclarations(s)
	for i := start; i < len(*rules); i++ {
		(*rules)[i].Declarations = slices.Clone(declarations)
	}

	s.tryConsumeByte('}')
	return nil
}

func cssConsumeBlock(s *cssStream) {
	s.tryConsumeByte('{')
	cssConsumeUntilBlockEnd(s)
}

// cssConsumeUntilBlockEnd skips to the matching '}' by counting braces (ignoring strings, as simplecss
// does).
func cssConsumeUntilBlockEnd(s *cssStream) {
	braces := 0
	for !s.atEnd() {
		switch s.text[s.pos] {
		case '{':
			braces++
		case '}':
			if braces == 0 {
				s.tryConsumeByte('}')
				return
			}
			braces--
		}
		s.advance(1)
	}
	s.tryConsumeByte('}')
}

func cssConsumeDeclarations(s *cssStream) []cssDeclaration {
	var declarations []cssDeclaration
	for !s.atEnd() && !s.currIs('}') {
		d, err := cssConsumeDeclaration(s)
		if err != nil {
			cssConsumeUntilBlockEnd(s)
			break
		}
		declarations = append(declarations, d)
	}
	return declarations
}

// cssDeclarationTokenizer is simplecss::DeclarationTokenizer, used for `style` attributes. It stops at
// the first invalid declaration.
type cssDeclarationTokenizer struct {
	stream cssStream
}

func cssNewDeclarationTokenizer(text string) *cssDeclarationTokenizer {
	return &cssDeclarationTokenizer{stream: cssNewStream(text)}
}

// Next returns the next declaration, or false at the end or on an error.
func (t *cssDeclarationTokenizer) Next() (cssDeclaration, bool) {
	_ = t.stream.skipSpacesAndComments()
	if t.stream.atEnd() {
		return cssDeclaration{}, false
	}
	d, err := cssConsumeDeclaration(&t.stream)
	if err != nil {
		t.stream.jumpToEnd()
		return cssDeclaration{}, false
	}
	return d, true
}

func cssConsumeDeclaration(s *cssStream) (cssDeclaration, *cssError) {
	var none cssDeclaration
	if err := s.skipSpacesAndComments(); err != nil {
		return none, err
	}

	// https://snook.ca/archives/html_and_css/targetting_ie7
	if s.currIs('*') {
		s.advance(1)
	}

	name, err := s.consumeIdent()
	if err != nil {
		return none, err
	}
	if err := s.skipSpacesAndComments(); err != nil {
		return none, err
	}
	if err := s.consumeByte(':'); err != nil {
		return none, err
	}
	if err := s.skipSpacesAndComments(); err != nil {
		return none, err
	}

	start := s.pos
	end := s.pos
	for cssConsumeTerm(s) == nil {
		end = s.pos
		if err := s.skipSpacesAndComments(); err != nil {
			return none, err
		}
	}
	value := strings.TrimSpace(s.sliceRange(start, end))

	if err := s.skipSpacesAndComments(); err != nil {
		return none, err
	}

	important := false
	if s.currIs('!') {
		s.advance(1)
		if err := s.skipSpacesAndComments(); err != nil {
			return none, err
		}
		if strings.HasPrefix(s.sliceTail(), "important") {
			s.advance(9)
			important = true
		}
	}

	if err := s.skipSpacesAndComments(); err != nil {
		return none, err
	}
	for s.currIs(';') {
		s.advance(1)
		if err := s.skipSpacesAndComments(); err != nil {
			return none, err
		}
	}
	if err := s.skipSpacesAndComments(); err != nil {
		return none, err
	}

	if value == "" {
		return none, &cssError{Kind: cssErrInvalidValue, Pos: s.genTextPosFrom(start)}
	}
	return cssDeclaration{Name: name, Value: value, Important: important}, nil
}

func cssConsumeTerm(s *cssStream) *cssError {
	isDigit := func() bool { return !s.atEnd() && s.text[s.pos] >= '0' && s.text[s.pos] <= '9' }
	consumeDigits := func() {
		for isDigit() {
			s.advance(1)
		}
	}

	b, err := s.currByte()
	if err != nil {
		return err
	}
	switch {
	case b == '#':
		s.advance(1)
		if _, err := s.consumeIdent(); err != nil {
			// Try to consume a hex color.
			for !s.atEnd() {
				c := s.text[s.pos]
				if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
					s.advance(1)
				} else {
					break
				}
			}
		}
	case b == '+' || b == '-' || (b >= '0' && b <= '9') || b == '.':
		// A number.
		s.advance(1)
		consumeDigits()
		if s.currIs('.') {
			s.advance(1)
			consumeDigits()
		}
		if s.currIs('%') {
			s.advance(1)
		} else {
			_, _ = s.consumeIdent() // a suffix, if any
		}
	case b == '\'' || b == '"':
		if _, err := s.consumeString(); err != nil {
			return err
		}
	case b == ',':
		s.advance(1)
	default:
		if _, err := s.consumeIdent(); err != nil {
			return err
		}
		// A function.
		if s.currIs('(') {
			s.skipBytes(func(c byte) bool { return c != ')' })
			if err := s.consumeByte(')'); err != nil {
				return err
			}
		}
	}
	return nil
}
