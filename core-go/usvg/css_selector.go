// Port of simplecss 0.2.2 src/selector.rs.

package usvg

import (
	"strings"
)

// cssAttrOpKind lists simplecss::AttributeOperator variants.
type cssAttrOpKind uint8

const (
	cssAttrExists     cssAttrOpKind = iota // [attr]
	cssAttrMatches                         // [attr=value]
	cssAttrContains                        // [attr~=value]
	cssAttrStartsWith                      // [attr|=value]
)

// cssAttributeOperator is simplecss::AttributeOperator; Value is unused for cssAttrExists.
type cssAttributeOperator struct {
	Kind  cssAttrOpKind
	Value string
}

// Matches checks that value matches the operator.
func (op cssAttributeOperator) Matches(value string) bool {
	switch op.Kind {
	case cssAttrExists:
		return true
	case cssAttrMatches:
		return value == op.Value
	case cssAttrContains:
		for _, s := range strings.Split(value, " ") {
			if s == op.Value {
				return true
			}
		}
		return false
	case cssAttrStartsWith:
		// Exactly the value, or beginning with it immediately followed by '-'.
		v := op.Value
		if value == v {
			return true
		}
		return strings.HasPrefix(value, v) && len(value) > len(v) && value[len(v)] == '-'
	}
	return false
}

// cssPseudoClassKind lists simplecss::PseudoClass variants.
type cssPseudoClassKind uint8

const (
	cssPseudoFirstChild cssPseudoClassKind = iota
	cssPseudoLink
	cssPseudoVisited
	cssPseudoHover
	cssPseudoActive
	cssPseudoFocus
	cssPseudoLang
)

// cssPseudoClass is simplecss::PseudoClass; Lang is set for cssPseudoLang only.
type cssPseudoClass struct {
	Kind cssPseudoClassKind
	Lang string
}

func (c cssPseudoClass) String() string {
	switch c.Kind {
	case cssPseudoFirstChild:
		return "first-child"
	case cssPseudoLink:
		return "link"
	case cssPseudoVisited:
		return "visited"
	case cssPseudoHover:
		return "hover"
	case cssPseudoActive:
		return "active"
	case cssPseudoFocus:
		return "focus"
	}
	return "lang(" + c.Lang + ")"
}

// cssElement is simplecss::Element: an element node a selector is matched against.
type cssElement interface {
	ParentElement() (cssElement, bool)
	PrevSiblingElement() (cssElement, bool)
	HasLocalName(name string) bool
	AttributeMatches(localName string, op cssAttributeOperator) bool
	PseudoClassMatches(class cssPseudoClass) bool
}

// cssSubSelector is an attribute selector (Pseudo false) or a pseudo-class (Pseudo true).
type cssSubSelector struct {
	Pseudo      bool
	AttrName    string
	AttrOp      cssAttributeOperator
	PseudoClass cssPseudoClass
}

// cssSimpleSelector has a type selector (Universal false, TypeName set) or `*`.
type cssSimpleSelector struct {
	Universal    bool
	TypeName     string
	Subselectors []cssSubSelector
}

type cssCombinator uint8

const (
	cssCombNone cssCombinator = iota
	cssCombDescendant
	cssCombChild
	cssCombAdjacentSibling
)

// cssComponent is a simple selector with the combinator that precedes it.
type cssComponent struct {
	Combinator cssCombinator
	Selector   cssSimpleSelector
}

// cssSelector is simplecss::Selector.
type cssSelector struct {
	Components []cssComponent
}

// cssParseSelector parses a selector, stopping at EOF, ',' or '{'. Errors (logged as warnings by
// simplecss) give false.
func cssParseSelector(text string) (cssSelector, bool) {
	sel, ok, _ := cssParseSelectorPrefix(text)
	return sel, ok
}

// Specificity computes the selector's specificity (https://www.w3.org/TR/selectors/#specificity), with
// saturating u8 counters.
func (s cssSelector) Specificity() [3]uint8 {
	var spec [3]uint8
	inc := func(i int) {
		if spec[i] < 255 {
			spec[i]++
		}
	}
	for _, c := range s.Components {
		if !c.Selector.Universal {
			inc(2)
		}
		for _, sub := range c.Selector.Subselectors {
			if !sub.Pseudo && sub.AttrName == "id" {
				inc(0)
			} else {
				inc(1)
			}
		}
	}
	return spec
}

// Matches checks that element matches the selector. It panics on an empty selector, as Rust asserts.
func (s cssSelector) Matches(element cssElement) bool {
	if len(s.Components) == 0 {
		panic("selector must not be empty")
	}
	if s.Components[0].Combinator != cssCombNone {
		panic("the first component must not have a combinator")
	}
	return s.matchesImpl(len(s.Components)-1, element)
}

func (s cssSelector) matchesImpl(idx int, element cssElement) bool {
	component := &s.Components[idx]
	if !cssMatchSelector(&component.Selector, element) {
		return false
	}

	switch component.Combinator {
	case cssCombDescendant:
		parent, ok := element.ParentElement()
		for ok {
			if s.matchesImpl(idx-1, parent) {
				return true
			}
			parent, ok = parent.ParentElement()
		}
		return false
	case cssCombChild:
		if parent, ok := element.ParentElement(); ok {
			return s.matchesImpl(idx-1, parent)
		}
		return false
	case cssCombAdjacentSibling:
		if prev, ok := element.PrevSiblingElement(); ok {
			return s.matchesImpl(idx-1, prev)
		}
		return false
	}
	return true
}

func cssMatchSelector(selector *cssSimpleSelector, element cssElement) bool {
	if !selector.Universal && !element.HasLocalName(selector.TypeName) {
		return false
	}
	for _, sub := range selector.Subselectors {
		if sub.Pseudo {
			if !element.PseudoClassMatches(sub.PseudoClass) {
				return false
			}
		} else if !element.AttributeMatches(sub.AttrName, sub.AttrOp) {
			return false
		}
	}
	return true
}

// cssParseSelectorPrefix is selector::parse: the selector (false on any error, which simplecss logs) and
// the byte offset where parsing stopped.
func cssParseSelectorPrefix(text string) (cssSelector, bool, int) {
	var components []cssComponent
	combinator := cssCombNone

	addSub := func(sub cssSubSelector) {
		if combinator == cssCombNone && len(components) != 0 {
			last := &components[len(components)-1]
			last.Selector.Subselectors = append(last.Selector.Subselectors, sub)
		} else {
			components = append(components, cssComponent{
				Selector:   cssSimpleSelector{Universal: true, Subselectors: []cssSubSelector{sub}},
				Combinator: combinator,
			})
			combinator = cssCombNone
		}
	}

	t := cssNewSelectorTokenizer(text)
	for {
		token, ok, err := t.Next()
		if !ok {
			break
		}
		if err != nil {
			return cssSelector{}, false, t.stream.pos
		}

		switch token.Kind {
		case cssTokUniversalSelector:
			components = append(components, cssComponent{
				Selector:   cssSimpleSelector{Universal: true},
				Combinator: combinator,
			})
			combinator = cssCombNone
		case cssTokTypeSelector:
			components = append(components, cssComponent{
				Selector:   cssSimpleSelector{TypeName: token.Ident},
				Combinator: combinator,
			})
			combinator = cssCombNone
		case cssTokClassSelector:
			addSub(cssSubSelector{AttrName: "class", AttrOp: cssAttributeOperator{Kind: cssAttrContains, Value: token.Ident}})
		case cssTokIDSelector:
			addSub(cssSubSelector{AttrName: "id", AttrOp: cssAttributeOperator{Kind: cssAttrMatches, Value: token.Ident}})
		case cssTokAttributeSelector:
			addSub(cssSubSelector{AttrName: token.Ident, AttrOp: token.Op})
		case cssTokPseudoClass:
			var kind cssPseudoClassKind
			switch token.Ident {
			case "first-child":
				kind = cssPseudoFirstChild
			case "link":
				kind = cssPseudoLink
			case "visited":
				kind = cssPseudoVisited
			case "hover":
				kind = cssPseudoHover
			case "active":
				kind = cssPseudoActive
			case "focus":
				kind = cssPseudoFocus
			default:
				// Unsupported pseudo-class: the selector is skipped.
				return cssSelector{}, false, t.stream.pos
			}
			addSub(cssSubSelector{Pseudo: true, PseudoClass: cssPseudoClass{Kind: kind}})
		case cssTokLangPseudoClass:
			addSub(cssSubSelector{Pseudo: true, PseudoClass: cssPseudoClass{Kind: cssPseudoLang, Lang: token.Ident}})
		case cssTokDescendantCombinator:
			combinator = cssCombDescendant
		case cssTokChildCombinator:
			combinator = cssCombChild
		case cssTokAdjacentCombinator:
			combinator = cssCombAdjacentSibling
		}
	}

	if len(components) == 0 || components[0].Combinator != cssCombNone {
		return cssSelector{}, false, t.stream.pos
	}
	return cssSelector{Components: components}, true, t.stream.pos
}

// String matches simplecss's Display for Selector.
func (s cssSelector) String() string {
	var b strings.Builder
	for _, c := range s.Components {
		switch c.Combinator {
		case cssCombDescendant:
			b.WriteString(" ")
		case cssCombChild:
			b.WriteString(" > ")
		case cssCombAdjacentSibling:
			b.WriteString(" + ")
		}
		if c.Selector.Universal {
			b.WriteString("*")
		} else {
			b.WriteString(c.Selector.TypeName)
		}
		for _, sub := range c.Selector.Subselectors {
			if sub.Pseudo {
				b.WriteString(":" + sub.PseudoClass.String())
				continue
			}
			switch sub.AttrOp.Kind {
			case cssAttrExists:
				b.WriteString("[" + sub.AttrName + "]")
			case cssAttrMatches:
				b.WriteString("[" + sub.AttrName + "='" + sub.AttrOp.Value + "']")
			case cssAttrContains:
				b.WriteString("[" + sub.AttrName + "~='" + sub.AttrOp.Value + "']")
			case cssAttrStartsWith:
				b.WriteString("[" + sub.AttrName + "|='" + sub.AttrOp.Value + "']")
			}
		}
	}
	return b.String()
}

// cssSelectorTokenKind lists simplecss::SelectorToken variants.
type cssSelectorTokenKind uint8

const (
	cssTokUniversalSelector    cssSelectorTokenKind = iota // *
	cssTokTypeSelector                                     // div
	cssTokClassSelector                                    // .class
	cssTokIDSelector                                       // #id
	cssTokAttributeSelector                                // [color=red]
	cssTokPseudoClass                                      // :first-child
	cssTokLangPseudoClass                                  // :lang(en)
	cssTokDescendantCombinator                             // a b
	cssTokChildCombinator                                  // a > b
	cssTokAdjacentCombinator                               // a + b
)

// cssSelectorToken is simplecss::SelectorToken. Ident holds the type, class, id, attribute name,
// pseudo-class or language; Op the attribute operator.
type cssSelectorToken struct {
	Kind  cssSelectorTokenKind
	Ident string
	Op    cssAttributeOperator
}

// cssSelectorTokenizer is simplecss::SelectorTokenizer.
type cssSelectorTokenizer struct {
	stream          cssStream
	afterCombinator bool
	finished        bool
}

func cssNewSelectorTokenizer(text string) *cssSelectorTokenizer {
	return &cssSelectorTokenizer{stream: cssNewStream(text), afterCombinator: true}
}

// Next is Iterator::next: ok false means None; otherwise the item is the token, or err when non-nil.
func (t *cssSelectorTokenizer) Next() (tok cssSelectorToken, ok bool, err *cssError) {
	if t.finished || t.stream.atEnd() {
		if t.afterCombinator {
			t.afterCombinator = false
			return tok, true, &cssError{Kind: cssErrSelectorMissing}
		}
		return tok, false, nil
	}

	fail := func(e *cssError) (cssSelectorToken, bool, *cssError) {
		t.finished = true
		return cssSelectorToken{}, true, e
	}
	s := &t.stream

	switch s.text[s.pos] {
	case '*':
		if !t.afterCombinator {
			return fail(&cssError{Kind: cssErrUnexpectedSelector})
		}
		t.afterCombinator = false
		s.advance(1)
		return cssSelectorToken{Kind: cssTokUniversalSelector}, true, nil
	case '#':
		t.afterCombinator = false
		s.advance(1)
		ident, err := s.consumeIdent()
		if err != nil {
			return fail(err)
		}
		return cssSelectorToken{Kind: cssTokIDSelector, Ident: ident}, true, nil
	case '.':
		t.afterCombinator = false
		s.advance(1)
		ident, err := s.consumeIdent()
		if err != nil {
			return fail(err)
		}
		return cssSelectorToken{Kind: cssTokClassSelector, Ident: ident}, true, nil
	case '[':
		t.afterCombinator = false
		s.advance(1)
		ident, err := s.consumeIdent()
		if err != nil {
			return fail(err)
		}
		b, err := s.currByte()
		if err != nil {
			return fail(err)
		}
		var op cssAttributeOperator
		switch b {
		case ']':
			op = cssAttributeOperator{Kind: cssAttrExists}
		case '=', '~', '|':
			s.advance(1)
			switch b {
			case '=':
				op.Kind = cssAttrMatches
			case '~':
				op.Kind = cssAttrContains
			default:
				op.Kind = cssAttrStartsWith
			}
			if b != '=' {
				if err := s.consumeByte('='); err != nil {
					return fail(err)
				}
			}
			value, err := s.consumeString()
			if err != nil {
				return fail(err)
			}
			op.Value = value
		default:
			return fail(&cssError{Kind: cssErrInvalidAttributeSelector})
		}
		if err := s.consumeByte(']'); err != nil {
			return fail(err)
		}
		return cssSelectorToken{Kind: cssTokAttributeSelector, Ident: ident, Op: op}, true, nil
	case ':':
		t.afterCombinator = false
		s.advance(1)
		ident, err := s.consumeIdent()
		if err != nil {
			return fail(err)
		}
		if ident != "lang" {
			return cssSelectorToken{Kind: cssTokPseudoClass, Ident: ident}, true, nil
		}
		if err := s.consumeByte('('); err != nil {
			return fail(err)
		}
		lang := strings.TrimSpace(s.consumeBytes(func(c byte) bool { return c != ')' }))
		if err := s.consumeByte(')'); err != nil {
			return fail(err)
		}
		if lang == "" {
			return fail(&cssError{Kind: cssErrInvalidLanguagePseudoClass})
		}
		return cssSelectorToken{Kind: cssTokLangPseudoClass, Ident: lang}, true, nil
	case '>', '+':
		if t.afterCombinator {
			t.afterCombinator = false
			return fail(&cssError{Kind: cssErrUnexpectedCombinator})
		}
		kind := cssTokChildCombinator
		if s.text[s.pos] == '+' {
			kind = cssTokAdjacentCombinator
		}
		s.advance(1)
		t.afterCombinator = true
		return cssSelectorToken{Kind: kind}, true, nil
	case ' ', '\t', '\n', '\r', '\x0C':
		s.skipSpaces()
		if t.afterCombinator {
			return t.Next()
		}
		for s.currIs('/') {
			if err := s.skipComment(); err != nil {
				return fail(err)
			}
			s.skipSpaces()
		}
		if b, err := s.currByte(); err != nil || b == '>' || b == '+' || b == ',' || b == '{' {
			return t.Next()
		}
		// after_combinator is false here, so Rust's UnexpectedSelector branch cannot run.
		t.afterCombinator = true
		return cssSelectorToken{Kind: cssTokDescendantCombinator}, true, nil
	case '/':
		if b, err := s.nextByte(); err == nil && b == '*' {
			if err := s.skipComment(); err != nil {
				return fail(err)
			}
		} else {
			t.finished = true
		}
		return t.Next()
	case ',', '{':
		t.finished = true
		return t.Next()
	default:
		ident, err := s.consumeIdent()
		if err != nil {
			return fail(err)
		}
		if !t.afterCombinator {
			return fail(&cssError{Kind: cssErrUnexpectedSelector})
		}
		t.afterCombinator = false
		return cssSelectorToken{Kind: cssTokTypeSelector, Ident: ident}, true, nil
	}
}
