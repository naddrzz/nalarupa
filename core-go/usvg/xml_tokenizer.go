package usvg

// XML tokenizer, ported from roxmltree 0.21.1 src/tokenizer.rs.

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// xmlCharIsNameStart reports whether c is in the NameStartChar range
// (https://www.w3.org/TR/xml/#NT-NameStartChar).
func xmlCharIsNameStart(c rune) bool {
	if uint32(c) <= 128 {
		return xmlByteIsNameStart(byte(c))
	}
	switch {
	case c >= 0x0000C0 && c <= 0x0000D6,
		c >= 0x0000D8 && c <= 0x0000F6,
		c >= 0x0000F8 && c <= 0x0002FF,
		c >= 0x000370 && c <= 0x00037D,
		c >= 0x00037F && c <= 0x001FFF,
		c >= 0x00200C && c <= 0x00200D,
		c >= 0x002070 && c <= 0x00218F,
		c >= 0x002C00 && c <= 0x002FEF,
		c >= 0x003001 && c <= 0x00D7FF,
		c >= 0x00F900 && c <= 0x00FDCF,
		c >= 0x00FDF0 && c <= 0x00FFFD,
		c >= 0x010000 && c <= 0x0EFFFF:
		return true
	}
	return false
}

// xmlCharIsName reports whether c is in the NameChar range (https://www.w3.org/TR/xml/#NT-NameChar).
func xmlCharIsName(c rune) bool {
	if uint32(c) <= 128 {
		return xmlByteIsName(byte(c))
	}
	switch {
	case c == 0x0000B7,
		c >= 0x0000C0 && c <= 0x0000D6,
		c >= 0x0000D8 && c <= 0x0000F6,
		c >= 0x0000F8 && c <= 0x0002FF,
		c >= 0x000300 && c <= 0x00036F,
		c >= 0x000370 && c <= 0x00037D,
		c >= 0x00037F && c <= 0x001FFF,
		c >= 0x00200C && c <= 0x00200D,
		c >= 0x00203F && c <= 0x002040,
		c >= 0x002070 && c <= 0x00218F,
		c >= 0x002C00 && c <= 0x002FEF,
		c >= 0x003001 && c <= 0x00D7FF,
		c >= 0x00F900 && c <= 0x00FDCF,
		c >= 0x00FDF0 && c <= 0x00FFFD,
		c >= 0x010000 && c <= 0x0EFFFF:
		return true
	}
	return false
}

// xmlCharIsChar reports whether c is in the Char range (https://www.w3.org/TR/xml/#NT-Char).
// Surrogates cannot occur: decoded runes never are.
func xmlCharIsChar(c rune) bool {
	if uint32(c) < 0x20 {
		return xmlByteIsSpace(byte(c))
	}
	return c != 0xFFFF && c != 0xFFFE
}

// xmlByteIsSpace reports whether b is one of ` \t\n\r`.
func xmlByteIsSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func xmlByteIsNameStart(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || b == ':' || b == '_'
}

func xmlByteIsName(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
		b == ':' || b == '_' || b == '-' || b == '.'
}

func xmlByteIsChar(b byte) bool {
	return b > 0x20 || xmlByteIsSpace(b)
}

func xmlIsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func xmlCheckStr(s string, valueStart int, stream *xmlStream) *xmlError {
	if xmlIsASCII(s) {
		for i := 0; i < len(s); i++ {
			if !xmlByteIsChar(s[i]) {
				return &xmlError{Kind: xmlErrNonXmlChar, Char: rune(s[i]), Pos: stream.genTextPosFrom(valueStart + i)}
			}
		}
		return nil
	}
	for i, ch := range s {
		if !xmlCharIsChar(ch) {
			return &xmlError{Kind: xmlErrNonXmlChar, Char: ch, Pos: stream.genTextPosFrom(valueStart + i)}
		}
	}
	return nil
}

// xmlRange is a half-open byte range, like Rust's Range<usize>.
type xmlRange struct {
	Start, End int
}

// xmlStrSpan is a string slice together with its start position in the text it was taken from.
type xmlStrSpan struct {
	text  string
	start int
}

func xmlStrSpanFromSubstr(text string, start, end int) xmlStrSpan {
	return xmlStrSpan{text: text[start:end], start: start}
}

func (s xmlStrSpan) rng() xmlRange {
	return xmlRange{s.start, s.start + len(s.text)}
}

type xmlTokenKind uint8

const (
	xmlTokenPI                xmlTokenKind = iota // <?target content?>
	xmlTokenComment                               // <!-- text -->
	xmlTokenEntityDeclaration                     // <!ENTITY ns_extend "http://test.com">
	xmlTokenElementStart                          // <ns:elem
	xmlTokenAttribute                             // ns:attr="value"
	xmlTokenElementEnd                            // > or </ns:elem> or />
	xmlTokenText                                  // everything between `>` and `<`
	xmlTokenCdata                                 // <![CDATA[text]]>
)

type xmlElementEndKind uint8

const (
	xmlElementEndOpen  xmlElementEndKind = iota // >
	xmlElementEndClose                          // </ns:name>
	xmlElementEndEmpty                          // />
)

// xmlToken is roxmltree's tokenizer::Token; only the fields of its kind are set.
type xmlToken struct {
	kind xmlTokenKind
	// ElementStart, Attribute and ElementEnd(Close): the qualified name.
	// PI: local is the target. EntityDeclaration: local is the name.
	prefix, local string
	// Comment, Text, Cdata: the text. PI: the value, present when hasText.
	text    string
	hasText bool
	// EntityDeclaration: the definition. Attribute: the value.
	span xmlStrSpan
	// Every kind but ElementStart and EntityDeclaration.
	rng xmlRange
	// ElementStart: the position of `<`.
	start int
	// Attribute.
	qnameLen uint16
	eqLen    uint8
	// ElementEnd.
	end xmlElementEndKind
}

type xmlEvents interface {
	token(t *xmlToken) *xmlError
	// resolveEntity resolves an external entity; ok false discards it.
	resolveEntity(pubID string, hasPubID bool, uri string) (value string, ok bool, err error)
}

// xmlTokenize is tokenizer::parse.
//
// document ::= prolog element Misc*
func xmlTokenize(text string, allowDTD bool, events xmlEvents) *xmlError {
	st := xmlNewStream(text)
	s := &st

	if err := xmlParseDeclaration(s); err != nil {
		return err
	}
	if err := xmlParseMisc(s, events); err != nil {
		return err
	}

	s.skipSpaces()
	if s.startsWith("<!DOCTYPE") {
		if !allowDTD {
			return &xmlError{Kind: xmlErrDtdDetected}
		}
		if err := xmlParseDoctype(s, events); err != nil {
			return err
		}
		if err := xmlParseMisc(s, events); err != nil {
			return err
		}
	}

	s.skipSpaces()
	if c, err := s.currByte(); err == nil && c == '<' {
		if err := xmlParseElement(s, events); err != nil {
			return err
		}
	}

	if err := xmlParseMisc(s, events); err != nil {
		return err
	}

	if !s.atEnd() {
		return &xmlError{Kind: xmlErrUnknownToken, Pos: s.genTextPos()}
	}
	return nil
}

// Misc ::= Comment | PI | S
func xmlParseMisc(s *xmlStream, events xmlEvents) *xmlError {
	for !s.atEnd() {
		s.skipSpaces()
		if s.startsWith("<!--") {
			if err := xmlParseComment(s, events); err != nil {
				return err
			}
		} else if s.startsWith("<?") {
			if err := xmlParsePI(s, events); err != nil {
				return err
			}
		} else {
			break
		}
	}
	return nil
}

func xmlDeclConsumeSpaces(s *xmlStream) *xmlError {
	if s.startsWithSpace() {
		s.skipSpaces()
	} else if !s.startsWith("?>") && !s.atEnd() {
		return &xmlError{Kind: xmlErrInvalidChar2, ExpectedStr: "a whitespace", Actual: s.currByteUnchecked(), Pos: s.genTextPos()}
	}
	return nil
}

// XMLDecl ::= '<?xml' VersionInfo EncodingDecl? SDDecl? S? '?>'
//
// The declaration is only validated, no token is emitted.
func xmlParseDeclaration(s *xmlStream) *xmlError {
	// Skip UTF-8 BOM.
	if s.startsWith("\xEF\xBB\xBF") {
		s.advance(3)
	}

	if !s.startsWith("<?xml ") {
		return nil
	}

	s.advance(5) // <?xml
	if err := xmlDeclConsumeSpaces(s); err != nil {
		return err
	}

	// The `version` "attribute" is mandatory.
	if !s.startsWith("version") {
		// Triggers the InvalidString error.
		return s.skipString("version")
	}
	if _, _, _, err := xmlParseAttribute(s); err != nil {
		return err
	}
	if err := xmlDeclConsumeSpaces(s); err != nil {
		return err
	}

	if s.startsWith("encoding") {
		if _, _, _, err := xmlParseAttribute(s); err != nil {
			return err
		}
		if err := xmlDeclConsumeSpaces(s); err != nil {
			return err
		}
	}

	if s.startsWith("standalone") {
		if _, _, _, err := xmlParseAttribute(s); err != nil {
			return err
		}
	}

	s.skipSpaces()
	return s.skipString("?>")
}

// '<!--' ((Char - '-') | ('-' (Char - '-')))* '-->'
func xmlParseComment(s *xmlStream, events xmlEvents) *xmlError {
	start := s.pos
	s.advance(4)
	text, err := s.consumeChars(func(s *xmlStream, c rune) bool { return !(c == '-' && s.startsWith("-->")) })
	if err != nil {
		return err
	}
	if err := s.skipString("-->"); err != nil {
		return err
	}

	if strings.Contains(text, "--") {
		return &xmlError{Kind: xmlErrInvalidComment, Pos: s.genTextPosFrom(start)}
	}
	if strings.HasSuffix(text, "-") {
		return &xmlError{Kind: xmlErrInvalidComment, Pos: s.genTextPosFrom(start)}
	}

	return events.token(&xmlToken{kind: xmlTokenComment, text: text, rng: s.rangeFrom(start)})
}

// PI       ::= '<?' PITarget (S (Char* - (Char* '?>' Char*)))? '?>'
// PITarget ::= Name - (('X' | 'x') ('M' | 'm') ('L' | 'l'))
func xmlParsePI(s *xmlStream, events xmlEvents) *xmlError {
	if s.startsWith("<?xml ") {
		return &xmlError{Kind: xmlErrUnexpectedDeclaration, Pos: s.genTextPos()}
	}

	start := s.pos
	s.advance(2)
	target, err := s.consumeName()
	if err != nil {
		return err
	}
	s.skipSpaces()
	content, err := s.consumeChars(func(s *xmlStream, c rune) bool { return !(c == '?' && s.startsWith("?>")) })
	if err != nil {
		return err
	}

	if err := s.skipString("?>"); err != nil {
		return err
	}

	return events.token(&xmlToken{kind: xmlTokenPI, local: target, text: content, hasText: content != "", rng: s.rangeFrom(start)})
}

func xmlParseDoctype(s *xmlStream, events xmlEvents) *xmlError {
	start := s.pos
	if err := xmlParseDoctypeStart(s); err != nil {
		return err
	}
	s.skipSpaces()

	if c, err := s.currByte(); err == nil && c == '>' {
		s.advance(1)
		return nil
	}

	s.advance(1) // [
	for !s.atEnd() {
		s.skipSpaces()
		switch {
		case s.startsWith("<!ENTITY"):
			if err := xmlParseEntityDecl(s, events); err != nil {
				return err
			}
		case s.startsWith("<!--"):
			if err := xmlParseComment(s, events); err != nil {
				return err
			}
		case s.startsWith("<?"):
			if err := xmlParsePI(s, events); err != nil {
				return err
			}
		case s.startsWith("]"):
			// DTD ends with ']' S? '>', therefore we have to skip possible spaces.
			s.advance(1)
			s.skipSpaces()
			c, err := s.currByte()
			if err != nil {
				return &xmlError{Kind: xmlErrUnexpectedEndOfStream}
			}
			if c == '>' {
				s.advance(1)
				return nil
			}
			return &xmlError{Kind: xmlErrInvalidChar2, ExpectedStr: "'>'", Actual: c, Pos: s.genTextPos()}
		case s.startsWith("<!ELEMENT") || s.startsWith("<!ATTLIST") || s.startsWith("<!NOTATION"):
			if xmlConsumeDecl(s) != nil {
				return &xmlError{Kind: xmlErrUnknownToken, Pos: s.genTextPosFrom(start)}
			}
		default:
			return &xmlError{Kind: xmlErrUnknownToken, Pos: s.genTextPos()}
		}
	}

	return nil
}

// doctypedecl ::= '<!DOCTYPE' S Name (S ExternalID)? S? ('[' intSubset ']' S?)? '>'
func xmlParseDoctypeStart(s *xmlStream) *xmlError {
	s.advance(9)

	if err := s.consumeSpaces(); err != nil {
		return err
	}
	if err := s.skipName(); err != nil {
		return err
	}
	s.skipSpaces()

	if _, _, _, _, err := xmlParseExternalID(s); err != nil {
		return err
	}
	s.skipSpaces()

	c, err := s.currByte()
	if err != nil {
		return err
	}
	if c != '[' && c != '>' {
		return &xmlError{Kind: xmlErrInvalidChar2, ExpectedStr: "'[' or '>'", Actual: c, Pos: s.genTextPos()}
	}
	return nil
}

// ExternalID ::= 'SYSTEM' S SystemLiteral | 'PUBLIC' S PubidLiteral S SystemLiteral
func xmlParseExternalID(s *xmlStream) (pubID string, hasPubID bool, uri string, ok bool, err *xmlError) {
	if !s.startsWith("SYSTEM") && !s.startsWith("PUBLIC") {
		return "", false, "", false, nil
	}

	start := s.pos
	s.advance(6)
	id := s.sliceBack(start)

	if err := s.consumeSpaces(); err != nil {
		return "", false, "", false, err
	}
	quote, err := s.consumeQuote()
	if err != nil {
		return "", false, "", false, err
	}
	first := s.consumeBytes(func(c byte) bool { return c != quote })
	if err := s.consumeByte(quote); err != nil {
		return "", false, "", false, err
	}

	if id == "SYSTEM" {
		return "", false, first, true, nil
	}

	if err := s.consumeSpaces(); err != nil {
		return "", false, "", false, err
	}
	quote, err = s.consumeQuote()
	if err != nil {
		return "", false, "", false, err
	}
	second := s.consumeBytes(func(c byte) bool { return c != quote })
	if err := s.consumeByte(quote); err != nil {
		return "", false, "", false, err
	}
	return first, true, second, true, nil
}

// EntityDecl  ::= GEDecl | PEDecl
// GEDecl      ::= '<!ENTITY' S Name S EntityDef S? '>'
// PEDecl      ::= '<!ENTITY' S '%' S Name S PEDef S? '>'
func xmlParseEntityDecl(s *xmlStream, events xmlEvents) *xmlError {
	s.advance(8)
	if err := s.consumeSpaces(); err != nil {
		return err
	}

	isGE := true
	if s.tryConsumeByte('%') {
		if err := s.consumeSpaces(); err != nil {
			return err
		}
		isGE = false
	}

	name, err := s.consumeName()
	if err != nil {
		return err
	}
	if err := s.consumeSpaces(); err != nil {
		return err
	}
	definition, ok, err := xmlParseEntityDef(s, events, isGE)
	if err != nil {
		return err
	}
	if ok {
		if err := events.token(&xmlToken{kind: xmlTokenEntityDeclaration, local: name, span: definition}); err != nil {
			return err
		}
	}
	s.skipSpaces()
	return s.consumeByte('>')
}

// EntityDef   ::= EntityValue | (ExternalID NDataDecl?)
// PEDef       ::= EntityValue | ExternalID
// EntityValue ::= '"' ([^%&"] | PEReference | Reference)* '"' |  "'" ([^%&']
//
//	| PEReference | Reference)* "'"
//
// ExternalID  ::= 'SYSTEM' S SystemLiteral | 'PUBLIC' S PubidLiteral S SystemLiteral
// NDataDecl   ::= S 'NDATA' S Name
func xmlParseEntityDef(s *xmlStream, events xmlEvents, isGE bool) (xmlStrSpan, bool, *xmlError) {
	c, err := s.currByte()
	if err != nil {
		return xmlStrSpan{}, false, err
	}
	switch c {
	case '"', '\'':
		quote, err := s.consumeQuote()
		if err != nil {
			return xmlStrSpan{}, false, err
		}
		start := s.pos
		s.skipBytes(func(c byte) bool { return c != quote })
		value := s.sliceBackSpan(start)
		if err := s.consumeByte(quote); err != nil {
			return xmlStrSpan{}, false, err
		}
		return value, true, nil
	case 'S', 'P':
		pubID, hasPubID, uri, ok, err := xmlParseExternalID(s)
		if err != nil {
			return xmlStrSpan{}, false, err
		}
		if !ok {
			return xmlStrSpan{}, false, &xmlError{Kind: xmlErrInvalidExternalID, Pos: s.genTextPos()}
		}
		if isGE {
			s.skipSpaces()
			if s.startsWith("NDATA") {
				s.advance(5)
				if err := s.consumeSpaces(); err != nil {
					return xmlStrSpan{}, false, err
				}
				if err := s.skipName(); err != nil {
					return xmlStrSpan{}, false, err
				}
				// NDataDecl is not supported.
			}
		}

		value, found, rerr := events.resolveEntity(pubID, hasPubID, uri)
		if rerr != nil {
			return xmlStrSpan{}, false, &xmlError{Kind: xmlErrEntityResolver, Pos: s.genTextPos(), Msg: rerr.Error()}
		}
		if !found {
			return xmlStrSpan{}, false, nil
		}
		st := xmlNewStream(value)
		if err := xmlParseDeclaration(&st); err != nil {
			return xmlStrSpan{}, false, err
		}
		return xmlStrSpan{text: value[st.pos:], start: 0}, true, nil
	default:
		return xmlStrSpan{}, false, &xmlError{Kind: xmlErrInvalidChar2, ExpectedStr: "a quote, SYSTEM or PUBLIC", Actual: c, Pos: s.genTextPos()}
	}
}

func xmlConsumeDecl(s *xmlStream) *xmlError {
	s.skipBytes(func(c byte) bool { return c != '>' })
	return s.consumeByte('>')
}

// element ::= EmptyElemTag | STag content ETag
// '<' Name (S Attribute)* S? '>'
func xmlParseElement(s *xmlStream, events xmlEvents) *xmlError {
	start := s.pos
	s.advance(1) // <
	prefix, local, err := s.consumeQName()
	if err != nil {
		return err
	}
	if err := events.token(&xmlToken{kind: xmlTokenElementStart, prefix: prefix, local: local, start: start}); err != nil {
		return err
	}

	open := false
	for !s.atEnd() {
		hasSpace := s.startsWithSpace()
		s.skipSpaces()
		start := s.pos
		c, err := s.currByte()
		if err != nil {
			return err
		}
		if c == '/' {
			s.advance(1)
			if err := s.consumeByte('>'); err != nil {
				return err
			}
			if err := events.token(&xmlToken{kind: xmlTokenElementEnd, end: xmlElementEndEmpty, rng: s.rangeFrom(start)}); err != nil {
				return err
			}
			break
		}
		if c == '>' {
			s.advance(1)
			if err := events.token(&xmlToken{kind: xmlTokenElementEnd, end: xmlElementEndOpen, rng: s.rangeFrom(start)}); err != nil {
				return err
			}
			open = true
			break
		}

		// An attribute must be preceded with a whitespace.
		if !hasSpace {
			// Always triggers an error.
			if err := s.consumeSpaces(); err != nil {
				return err
			}
		}

		prefix, local, err := s.consumeQName()
		if err != nil {
			return err
		}
		qnameEnd := s.pos
		qnameLen := uint16(0xFFFF)
		if n := qnameEnd - start; n < 0xFFFF {
			qnameLen = uint16(n)
		}
		if err := s.consumeEq(); err != nil {
			return err
		}
		eqLen := uint8(0xFF)
		if n := s.pos - qnameEnd; n < 0xFF {
			eqLen = uint8(n)
		}
		quote, err := s.consumeQuote()
		if err != nil {
			return err
		}
		// The attribute value must not contain the < character.
		valueStart := s.pos
		if err := s.advanceUntil2(quote, '<'); err != nil {
			return err
		}
		value := s.sliceBackSpan(valueStart)
		if err := xmlCheckStr(value.text, valueStart, s); err != nil {
			return err
		}
		if err := s.consumeByte(quote); err != nil {
			return err
		}
		end := s.pos
		if err := events.token(&xmlToken{kind: xmlTokenAttribute, rng: xmlRange{start, end}, qnameLen: qnameLen, eqLen: eqLen,
			prefix: prefix, local: local, span: value}); err != nil {
			return err
		}
	}

	if open {
		return xmlParseContent(s, events)
	}
	return nil
}

// Attribute ::= Name Eq AttValue (used by the XML declaration only)
func xmlParseAttribute(s *xmlStream) (prefix, local string, value xmlStrSpan, err *xmlError) {
	prefix, local, err = s.consumeQName()
	if err != nil {
		return
	}
	if err = s.consumeEq(); err != nil {
		return
	}
	quote, err := s.consumeQuote()
	if err != nil {
		return
	}
	quoteC := rune(quote)
	// The attribute value must not contain the < character.
	valueStart := s.pos
	if err = s.skipChars(func(_ *xmlStream, c rune) bool { return c != quoteC && c != '<' }); err != nil {
		return
	}
	value = s.sliceBackSpan(valueStart)
	err = s.consumeByte(quote)
	return
}

// xmlParseContent is tokenizer::parse_content.
//
// content ::= CharData? ((element | Reference | CDSect | PI | Comment) CharData?)*
func xmlParseContent(s *xmlStream, events xmlEvents) *xmlError {
	for !s.atEnd() {
		c, err := s.currByte()
		if err != nil {
			return &xmlError{Kind: xmlErrUnknownToken, Pos: s.genTextPos()}
		}
		if c != '<' {
			if err := xmlParseText(s, events); err != nil {
				return err
			}
			continue
		}
		next, err := s.nextByte()
		if err != nil {
			return &xmlError{Kind: xmlErrUnknownToken, Pos: s.genTextPos()}
		}
		switch next {
		case '!':
			if s.startsWith("<!--") {
				if err := xmlParseComment(s, events); err != nil {
					return err
				}
			} else if s.startsWith("<![CDATA[") {
				if err := xmlParseCdata(s, events); err != nil {
					return err
				}
			} else {
				return &xmlError{Kind: xmlErrUnknownToken, Pos: s.genTextPos()}
			}
		case '?':
			if err := xmlParsePI(s, events); err != nil {
				return err
			}
		case '/':
			return xmlParseCloseElement(s, events)
		default:
			if err := xmlParseElement(s, events); err != nil {
				return err
			}
		}
	}
	return nil
}

// CDSect  ::= CDStart CData CDEnd
// CDStart ::= '<![CDATA['
// CData   ::= (Char* - (Char* ']]>' Char*))
// CDEnd   ::= ']]>'
func xmlParseCdata(s *xmlStream, events xmlEvents) *xmlError {
	start := s.pos
	s.advance(9) // <![CDATA[
	text, err := s.consumeChars(func(s *xmlStream, c rune) bool { return !(c == ']' && s.startsWith("]]>")) })
	if err != nil {
		return err
	}
	if err := s.skipString("]]>"); err != nil {
		return err
	}
	return events.token(&xmlToken{kind: xmlTokenCdata, text: text, rng: s.rangeFrom(start)})
}

// '</' Name S? '>'
func xmlParseCloseElement(s *xmlStream, events xmlEvents) *xmlError {
	start := s.pos
	s.advance(2) // </

	prefix, tagName, err := s.consumeQName()
	if err != nil {
		return err
	}
	s.skipSpaces()
	if err := s.consumeByte('>'); err != nil {
		return err
	}

	return events.token(&xmlToken{kind: xmlTokenElementEnd, end: xmlElementEndClose, prefix: prefix, local: tagName, rng: s.rangeFrom(start)})
}

func xmlParseText(s *xmlStream, events xmlEvents) *xmlError {
	start := s.pos
	text, err := s.consumeChars(func(_ *xmlStream, c rune) bool { return c != '<' })
	if err != nil {
		return err
	}

	// According to the spec, `]]>` must not appear inside a Text node.
	// https://www.w3.org/TR/xml/#syntax
	if strings.IndexByte(text, '>') >= 0 && strings.Contains(text, "]]>") {
		return &xmlError{Kind: xmlErrInvalidCharacterData, Pos: s.genTextPos()}
	}

	return events.token(&xmlToken{kind: xmlTokenText, text: text, rng: s.rangeFrom(start)})
}

// xmlReference is a Reference (https://www.w3.org/TR/xml/#NT-Reference): an entity reference when
// isChar is false, else a character reference.
type xmlReference struct {
	isChar bool
	ch     rune
	name   string
}

type xmlStream struct {
	pos, end int
	text     string
}

func xmlNewStream(text string) xmlStream {
	return xmlStream{pos: 0, end: len(text), text: text}
}

func xmlStreamFromSubstr(text string, fragment xmlRange) xmlStream {
	return xmlStream{pos: fragment.Start, end: fragment.End, text: text}
}

func (s *xmlStream) atEnd() bool {
	return s.pos >= s.end
}

func (s *xmlStream) currByte() (byte, *xmlError) {
	if s.atEnd() {
		return 0, &xmlError{Kind: xmlErrUnexpectedEndOfStream}
	}
	return s.currByteUnchecked(), nil
}

func (s *xmlStream) currByteUnchecked() byte {
	return s.text[s.pos]
}

func (s *xmlStream) nextByte() (byte, *xmlError) {
	if s.pos+1 >= s.end {
		return 0, &xmlError{Kind: xmlErrUnexpectedEndOfStream}
	}
	return s.text[s.pos+1], nil
}

func (s *xmlStream) advance(n int) {
	s.pos += n
}

func (s *xmlStream) startsWith(text string) bool {
	return strings.HasPrefix(s.text[s.pos:s.end], text)
}

func (s *xmlStream) consumeByte(c byte) *xmlError {
	curr, err := s.currByte()
	if err != nil {
		return err
	}
	if curr != c {
		return &xmlError{Kind: xmlErrInvalidChar, Expected: c, Actual: curr, Pos: s.genTextPos()}
	}
	s.advance(1)
	return nil
}

// tryConsumeByte is consumeByte without an error.
func (s *xmlStream) tryConsumeByte(c byte) bool {
	if b, err := s.currByte(); err == nil && b == c {
		s.advance(1)
		return true
	}
	return false
}

func (s *xmlStream) skipString(text string) *xmlError {
	if !s.startsWith(text) {
		return &xmlError{Kind: xmlErrInvalidString, ExpectedStr: text, Pos: s.genTextPos()}
	}
	s.advance(len(text))
	return nil
}

func (s *xmlStream) consumeBytes(f func(byte) bool) string {
	start := s.pos
	s.skipBytes(f)
	return s.sliceBack(start)
}

func (s *xmlStream) skipBytes(f func(byte) bool) {
	for !s.atEnd() && f(s.currByteUnchecked()) {
		s.advance(1)
	}
}

func (s *xmlStream) consumeChars(f func(*xmlStream, rune) bool) (string, *xmlError) {
	start := s.pos
	if err := s.skipChars(f); err != nil {
		return "", err
	}
	return s.sliceBack(start), nil
}

func (s *xmlStream) skipChars(f func(*xmlStream, rune) bool) *xmlError {
	for s.pos < s.end {
		c, size := utf8.DecodeRuneInString(s.text[s.pos:s.end])
		if !xmlCharIsChar(c) {
			return &xmlError{Kind: xmlErrNonXmlChar, Char: c, Pos: s.genTextPos()}
		} else if f(s, c) {
			s.advance(size)
		} else {
			break
		}
	}
	return nil
}

func (s *xmlStream) advanceUntil2(needle1, needle2 byte) *xmlError {
	for i := s.pos; i < s.end; i++ {
		if b := s.text[i]; b == needle1 || b == needle2 {
			s.pos = i
			return nil
		}
	}
	return &xmlError{Kind: xmlErrUnexpectedEndOfStream}
}

func (s *xmlStream) sliceBack(pos int) string {
	return s.text[pos:s.pos]
}

func (s *xmlStream) sliceBackSpan(pos int) xmlStrSpan {
	return xmlStrSpanFromSubstr(s.text, pos, s.pos)
}

func (s *xmlStream) rangeFrom(start int) xmlRange {
	return xmlRange{start, s.pos}
}

func (s *xmlStream) skipSpaces() {
	for s.startsWithSpace() {
		s.advance(1)
	}
}

func (s *xmlStream) startsWithSpace() bool {
	return !s.atEnd() && xmlByteIsSpace(s.currByteUnchecked())
}

// consumeSpaces is skipSpaces, but checks that the first char is actually a space.
func (s *xmlStream) consumeSpaces() *xmlError {
	if s.atEnd() {
		return &xmlError{Kind: xmlErrUnexpectedEndOfStream}
	}
	if !s.startsWithSpace() {
		return &xmlError{Kind: xmlErrInvalidChar2, ExpectedStr: "a whitespace", Actual: s.currByteUnchecked(), Pos: s.genTextPos()}
	}
	s.skipSpaces()
	return nil
}

// consumeReference consumes a Reference (https://www.w3.org/TR/xml/#NT-Reference).
func (s *xmlStream) consumeReference() (xmlReference, bool) {
	if !s.tryConsumeByte('&') {
		return xmlReference{}, false
	}

	var ref xmlReference
	if s.tryConsumeByte('#') {
		var value string
		base := 10
		if s.tryConsumeByte('x') {
			value = s.consumeBytes(func(c byte) bool {
				return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			})
			base = 16
		} else {
			value = s.consumeBytes(func(c byte) bool { return c >= '0' && c <= '9' })
		}

		n, err := strconv.ParseUint(value, base, 32)
		if err != nil {
			return xmlReference{}, false
		}

		c := rune(n)
		if n > utf8.MaxRune || (n >= 0xD800 && n <= 0xDFFF) {
			c = '�'
		}
		if !xmlCharIsChar(c) {
			return xmlReference{}, false
		}
		ref = xmlReference{isChar: true, ch: c}
	} else {
		name, err := s.consumeName()
		if err != nil {
			return xmlReference{}, false
		}
		switch name {
		case "quot":
			ref = xmlReference{isChar: true, ch: '"'}
		case "amp":
			ref = xmlReference{isChar: true, ch: '&'}
		case "apos":
			ref = xmlReference{isChar: true, ch: '\''}
		case "lt":
			ref = xmlReference{isChar: true, ch: '<'}
		case "gt":
			ref = xmlReference{isChar: true, ch: '>'}
		default:
			ref = xmlReference{name: name}
		}
	}

	if s.consumeByte(';') != nil {
		return xmlReference{}, false
	}
	return ref, true
}

// consumeName consumes a Name (https://www.w3.org/TR/xml/#NT-Name).
func (s *xmlStream) consumeName() (string, *xmlError) {
	start := s.pos
	if err := s.skipName(); err != nil {
		return "", err
	}

	name := s.sliceBack(start)
	if name == "" {
		return "", &xmlError{Kind: xmlErrInvalidName, Pos: s.genTextPosFrom(start)}
	}
	return name, nil
}

// skipName is consumeName without returning the name.
func (s *xmlStream) skipName() *xmlError {
	start := s.pos
	if s.pos >= s.end {
		return nil
	}
	c, size := utf8.DecodeRuneInString(s.text[s.pos:s.end])
	if !xmlCharIsNameStart(c) {
		return &xmlError{Kind: xmlErrInvalidName, Pos: s.genTextPosFrom(start)}
	}
	s.advance(size)

	for s.pos < s.end {
		c, size := utf8.DecodeRuneInString(s.text[s.pos:s.end])
		if !xmlCharIsName(c) {
			break
		}
		s.advance(size)
	}
	return nil
}

func xmlStrIsNameStart(name string) bool {
	if name == "" {
		return false
	}
	if name[0] < 128 {
		return xmlByteIsNameStart(name[0])
	}
	c, _ := utf8.DecodeRuneInString(name)
	return xmlCharIsNameStart(c)
}

// consumeQName consumes a qualified name (https://www.w3.org/TR/xml-names/#ns-qualnames).
func (s *xmlStream) consumeQName() (prefix, local string, err *xmlError) {
	start := s.pos
	splitter := -1

	for !s.atEnd() {
		// Check for ASCII first for performance reasons.
		b := s.currByteUnchecked()
		if b < 128 {
			if b == ':' {
				if splitter < 0 {
					splitter = s.pos
					s.advance(1)
				} else {
					// Multiple `:` is an error.
					return "", "", &xmlError{Kind: xmlErrInvalidName, Pos: s.genTextPosFrom(start)}
				}
			} else if xmlByteIsName(b) {
				s.advance(1)
			} else {
				break
			}
		} else {
			// Fallback to Unicode code point.
			c, size := utf8.DecodeRuneInString(s.text[s.pos:s.end])
			if !xmlCharIsName(c) {
				break
			}
			s.advance(size)
		}
	}

	if splitter >= 0 {
		prefix = s.text[start:splitter]
		local = s.sliceBack(splitter + 1)
	} else {
		local = s.sliceBack(start)
		// An empty prefix slice preserves the attribute start position.
		prefix = s.text[start:start]
	}

	// Prefix must be empty or start with a `NameStartChar`.
	if prefix != "" && !xmlStrIsNameStart(prefix) {
		return "", "", &xmlError{Kind: xmlErrInvalidName, Pos: s.genTextPosFrom(start)}
	}

	// Local name must start with a `NameStartChar`.
	if !xmlStrIsNameStart(local) {
		return "", "", &xmlError{Kind: xmlErrInvalidName, Pos: s.genTextPosFrom(start)}
	}

	return prefix, local, nil
}

func (s *xmlStream) consumeEq() *xmlError {
	s.skipSpaces()
	if err := s.consumeByte('='); err != nil {
		return err
	}
	s.skipSpaces()
	return nil
}

func (s *xmlStream) consumeQuote() (byte, *xmlError) {
	c, err := s.currByte()
	if err != nil {
		return 0, err
	}
	if c == '\'' || c == '"' {
		s.advance(1)
		return c, nil
	}
	return 0, &xmlError{Kind: xmlErrInvalidChar2, ExpectedStr: "a quote", Actual: c, Pos: s.genTextPos()}
}

// genTextPos calculates the current absolute position: rows counted in '\n' bytes, columns in
// chars. Expensive; use for errors only.
func (s *xmlStream) genTextPos() xmlTextPos {
	text := s.text[:s.pos]
	row := uint32(strings.Count(text, "\n")) + 1
	col := uint32(utf8.RuneCountInString(text[strings.LastIndexByte(text, '\n')+1:])) + 1
	return xmlTextPos{Row: row, Col: col}
}

// genTextPosFrom calculates the absolute position at pos (clamped to the text length).
func (s *xmlStream) genTextPosFrom(pos int) xmlTextPos {
	c := *s
	c.pos = min(pos, len(c.text))
	return c.genTextPos()
}
