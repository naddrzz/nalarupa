package usvg

// Ported from roxmltree 0.21.1 src/tokenizer_tests.rs. Tokens are compared as strings built by the
// xmlTk* helpers, which mirror the Rust test Token enum.

import (
	"fmt"
	"strings"
	"testing"
)

func TestXMLTextPos1(t *testing.T) {
	s := xmlNewStream("text")
	s.advance(2)
	if got := s.genTextPos(); got != (xmlTextPos{1, 3}) {
		t.Fatalf("got %v", got)
	}
}

func TestXMLTextPos2(t *testing.T) {
	s := xmlNewStream("text\ntext")
	s.advance(6)
	if got := s.genTextPos(); got != (xmlTextPos{2, 2}) {
		t.Fatalf("got %v", got)
	}
}

func TestXMLTextPos3(t *testing.T) {
	s := xmlNewStream("текст\nтекст")
	s.advance(15)
	if got := s.genTextPos(); got != (xmlTextPos{2, 3}) {
		t.Fatalf("got %v", got)
	}
}

const (
	xmlEEOpen  = "Open"
	xmlEEEmpty = "Empty"
)

func xmlEEClose(prefix, local string) string { return fmt.Sprintf("Close(%q, %q)", prefix, local) }

func xmlSome(s string) *string { return &s }

func xmlTkPI(target string, value *string, start, end int) string {
	v := "None"
	if value != nil {
		v = fmt.Sprintf("Some(%q)", *value)
	}
	return fmt.Sprintf("PI(%q, %s, %d..%d)", target, v, start, end)
}

func xmlTkComment(text string, start, end int) string {
	return fmt.Sprintf("Comment(%q, %d..%d)", text, start, end)
}

func xmlTkEntityDecl(name, definition string) string {
	return fmt.Sprintf("EntityDecl(%q, %q)", name, definition)
}

func xmlTkElementStart(prefix, local string, start int) string {
	return fmt.Sprintf("ElementStart(%q, %q, %d)", prefix, local, start)
}

func xmlTkAttribute(prefix, local, value string) string {
	return fmt.Sprintf("Attribute(%q, %q, %q)", prefix, local, value)
}

func xmlTkElementEnd(end string, start, stop int) string {
	return fmt.Sprintf("ElementEnd(%s, %d..%d)", end, start, stop)
}

func xmlTkText(text string, start, end int) string {
	return fmt.Sprintf("Text(%q, %d..%d)", text, start, end)
}

func xmlTkCdata(text string, start, end int) string {
	return fmt.Sprintf("Cdata(%q, %d..%d)", text, start, end)
}

func xmlTkError(msg string) string { return fmt.Sprintf("Error(%q)", msg) }

type xmlEventsCollector struct {
	tokens []string
}

func (c *xmlEventsCollector) token(t *xmlToken) *xmlError {
	var s string
	switch t.kind {
	case xmlTokenPI:
		var v *string
		if t.hasText {
			v = xmlSome(t.text)
		}
		s = xmlTkPI(t.local, v, t.rng.Start, t.rng.End)
	case xmlTokenComment:
		s = xmlTkComment(t.text, t.rng.Start, t.rng.End)
	case xmlTokenEntityDeclaration:
		s = xmlTkEntityDecl(t.local, t.span.text)
	case xmlTokenElementStart:
		s = xmlTkElementStart(t.prefix, t.local, t.start)
	case xmlTokenAttribute:
		s = xmlTkAttribute(t.prefix, t.local, t.span.text)
	case xmlTokenElementEnd:
		end := xmlEEOpen
		switch t.end {
		case xmlElementEndClose:
			end = xmlEEClose(t.prefix, t.local)
		case xmlElementEndEmpty:
			end = xmlEEEmpty
		}
		s = xmlTkElementEnd(end, t.rng.Start, t.rng.End)
	case xmlTokenText:
		s = xmlTkText(t.text, t.rng.Start, t.rng.End)
	case xmlTokenCdata:
		s = xmlTkCdata(t.text, t.rng.Start, t.rng.End)
	}
	c.tokens = append(c.tokens, s)
	return nil
}

func (c *xmlEventsCollector) resolveEntity(string, bool, string) (string, bool, error) {
	return "", false, nil
}

func xmlCollectTokens(text string) []string {
	c := &xmlEventsCollector{}
	if err := xmlTokenize(text, true, c); err != nil {
		c.tokens = append(c.tokens, xmlTkError(err.Error()))
	}
	return c.tokens
}

func TestXMLTokenizer(t *testing.T) {
	for _, tc := range xmlTokenizerCases {
		t.Run(tc.name, func(t *testing.T) {
			got := xmlCollectTokens(tc.text)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("tokens of %q:\ngot:\n%s\nwant:\n%s", tc.text, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestXMLTokenizerErrors(t *testing.T) {
	for _, tc := range xmlTokenizerErrCases {
		t.Run(tc.name, func(t *testing.T) {
			if err := xmlTokenize(tc.text, true, &xmlEventsCollector{}); err == nil {
				t.Fatalf("%q: expected an error", tc.text)
			}
		})
	}
}

// Many sequential unsupported DTD tokens must not overflow the stack.
func TestXMLTokenizerDtdEntity07(t *testing.T) {
	var b strings.Builder
	b.WriteString("<!DOCTYPE svg [\n")
	for range 500 {
		b.WriteString("<!ELEMENT sgml ANY>\n")
	}
	b.WriteString("]>\n")
	if err := xmlTokenize(b.String(), true, &xmlEventsCollector{}); err != nil {
		t.Fatal(err)
	}
}

var xmlTokenizerCases = []struct {
	name, text string
	want       []string
}{
	{"cdata_01", "<p><![CDATA[content]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkCdata("content", 3, 22),
		xmlTkElementEnd(xmlEEClose("", "p"), 22, 26),
	}},
	{"cdata_02", "<p><![CDATA[&amping]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkCdata("&amping", 3, 22),
		xmlTkElementEnd(xmlEEClose("", "p"), 22, 26),
	}},
	{"cdata_03", "<p><![CDATA[&amping ]]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkCdata("&amping ]", 3, 24),
		xmlTkElementEnd(xmlEEClose("", "p"), 24, 28),
	}},
	{"cdata_04", "<p><![CDATA[&amping]] ]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkCdata("&amping]] ", 3, 25),
		xmlTkElementEnd(xmlEEClose("", "p"), 25, 29),
	}},
	{"cdata_05", "<p><![CDATA[<message>text</message>]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkCdata("<message>text</message>", 3, 38),
		xmlTkElementEnd(xmlEEClose("", "p"), 38, 42),
	}},
	{"cdata_06", "<p><![CDATA[</this is malformed!</malformed</malformed & worse>]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkCdata("</this is malformed!</malformed</malformed & worse>", 3, 66),
		xmlTkElementEnd(xmlEEClose("", "p"), 66, 70),
	}},
	{"cdata_07", "<p><![CDATA[1]]><![CDATA[2]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkCdata("1", 3, 16),
		xmlTkCdata("2", 16, 29),
		xmlTkElementEnd(xmlEEClose("", "p"), 29, 33),
	}},
	{"cdata_08", "<p> \n <![CDATA[data]]> \t </p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkText(" \n ", 3, 6),
		xmlTkCdata("data", 6, 22),
		xmlTkText(" \t ", 22, 25),
		xmlTkElementEnd(xmlEEClose("", "p"), 25, 29),
	}},
	{"cdata_09", "<p><![CDATA[bracket ]after]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkCdata("bracket ]after", 3, 29),
		xmlTkElementEnd(xmlEEClose("", "p"), 29, 33),
	}},
	{"cdata_err_01", "<p><![CDATA[\u0001]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkError("a non-XML character '\\u{1}' found at 1:13"),
	}},
	{"comment_01", "<!--comment-->", []string{
		xmlTkComment("comment", 0, 14),
	}},
	{"comment_02", "<!--<head>-->", []string{
		xmlTkComment("<head>", 0, 13),
	}},
	{"comment_03", "<!--<!-x-->", []string{
		xmlTkComment("<!-x", 0, 11),
	}},
	{"comment_04", "<!--<!x-->", []string{
		xmlTkComment("<!x", 0, 10),
	}},
	{"comment_05", "<!--<<!x-->", []string{
		xmlTkComment("<<!x", 0, 11),
	}},
	{"comment_06", "<!--<<!-x-->", []string{
		xmlTkComment("<<!-x", 0, 12),
	}},
	{"comment_07", "<!--<x-->", []string{
		xmlTkComment("<x", 0, 9),
	}},
	{"comment_08", "<!--<>-->", []string{
		xmlTkComment("<>", 0, 9),
	}},
	{"comment_09", "<!--<-->", []string{
		xmlTkComment("<", 0, 8),
	}},
	{"comment_10", "<!--<!-->", []string{
		xmlTkComment("<!", 0, 9),
	}},
	{"comment_11", "<!---->", []string{
		xmlTkComment("", 0, 7),
	}},
	{"dtd_01", "<!DOCTYPE greeting SYSTEM \"hello.dtd\">", []string{}},
	{"dtd_02", "<!DOCTYPE greeting PUBLIC \"hello.dtd\" \"goodbye.dtd\">", []string{}},
	{"dtd_03", "<!DOCTYPE greeting SYSTEM 'hello.dtd'>", []string{}},
	{"dtd_04", "<!DOCTYPE greeting>", []string{}},
	{"dtd_05", "<!DOCTYPE greeting []>", []string{}},
	{"dtd_06", "<!DOCTYPE greeting><a/>", []string{
		xmlTkElementStart("", "a", 19),
		xmlTkElementEnd(xmlEEEmpty, 21, 23),
	}},
	{"dtd_07", "<!DOCTYPE greeting [] >", []string{}},
	{"dtd_08", "<!DOCTYPE greeting [ ] >", []string{}},
	{"dtd_entity_01", "<!DOCTYPE svg [\n    <!ENTITY ns_extend \"http://ns.adobe.com/Extensibility/1.0/\">\n]>", []string{
		xmlTkEntityDecl("ns_extend", "http://ns.adobe.com/Extensibility/1.0/"),
	}},
	{"dtd_entity_02", "<!DOCTYPE svg [\n    <!ENTITY Pub-Status \"This is a pre-release of the\nspecification.\">\n]>", []string{
		xmlTkEntityDecl("Pub-Status", "This is a pre-release of the\nspecification."),
	}},
	{"dtd_entity_03", "<!DOCTYPE svg [\n    <!ENTITY open-hatch SYSTEM \"http://www.textuality.com/boilerplate/OpenHatch.xml\">\n]>", []string{}},
	{"dtd_entity_04", "<!DOCTYPE svg [\n    <!ENTITY open-hatch\n             PUBLIC \"-//Textuality//TEXT Standard open-hatch boilerplate//EN\"\n             \"http://www.textuality.com/boilerplate/OpenHatch.xml\">\n]>", []string{}},
	{"dtd_entity_05", "<!DOCTYPE svg [\n    <!ENTITY hatch-pic SYSTEM \"../grafix/OpenHatch.gif\" NDATA gif >\n]>", []string{}},
	{"dtd_entity_06", "<!DOCTYPE svg [\n    <!ELEMENT sgml ANY>\n    <!ENTITY ns_extend \"http://ns.adobe.com/Extensibility/1.0/\">\n    <!NOTATION example1SVG-rdf SYSTEM \"example1.svg.rdf\">\n    <!ATTLIST img data ENTITY #IMPLIED>\n]>", []string{
		xmlTkEntityDecl("ns_extend", "http://ns.adobe.com/Extensibility/1.0/"),
	}},
	{"dtd_err_01", "<!DOCTYPEEG[<!ENTITY%ETT\nSSSSSSSS<D_IDYT;->\n<", []string{
		xmlTkError("expected a whitespace not 'E' at 1:10"),
	}},
	{"dtd_err_02", "<!DOCTYPE s [<!ENTITY % name S YSTEM", []string{
		xmlTkError("invalid ExternalID at 1:30"),
	}},
	{"dtd_err_03", "<!DOCTYPE s [<!ENTITY % name B", []string{
		xmlTkError("expected a quote, SYSTEM or PUBLIC not 'B' at 1:30"),
	}},
	{"dtd_err_04", "<!DOCTYPE s []", []string{
		xmlTkError("unexpected end of stream"),
	}},
	{"dtd_err_05", "<!DOCTYPE s [] !", []string{
		xmlTkError("expected '>' not '!' at 1:16"),
	}},
	{"document_01", "", []string{}},
	{"document_02", "    ", []string{}},
	{"document_03", " \n\t\r ", []string{}},
	{"document_05", "\xEF\xBB\xBF<a/>", []string{
		xmlTkElementStart("", "a", 3),
		xmlTkElementEnd(xmlEEEmpty, 5, 7),
	}},
	{"document_06", "\xEF\xBB\xBF<?xml version='1.0'?>", []string{}},
	{"document_07", "<?xml version='1.0' encoding='utf-8'?>\n<!-- comment -->\n<!DOCTYPE svg PUBLIC '-//W3C//DTD SVG 1.1//EN' 'http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd'>", []string{
		xmlTkComment(" comment ", 39, 55),
	}},
	{"document_08", "<?xml-stylesheet?>\n<!DOCTYPE svg PUBLIC '-//W3C//DTD SVG 1.1//EN' 'http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd'>", []string{
		xmlTkPI("xml-stylesheet", nil, 0, 18),
	}},
	{"document_09", "<?xml version='1.0' encoding='utf-8'?>\n<?xml-stylesheet?>\n<!DOCTYPE svg PUBLIC '-//W3C//DTD SVG 1.1//EN' 'http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd'>", []string{
		xmlTkPI("xml-stylesheet", nil, 39, 57),
	}},
	{"document_err_01", "<![CDATA[text]]>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"document_err_02", " &www---------Ӥ+----------w-----www_", []string{
		xmlTkError("unknown token at 1:2"),
	}},
	{"document_err_03", "q", []string{
		xmlTkError("unknown token at 1:1"),
	}},
	{"document_err_04", "<!>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"document_err_05", "<!DOCTYPE greeting1><!DOCTYPE greeting2>", []string{
		xmlTkError("invalid name token at 1:22"),
	}},
	{"document_err_06", "&#x20;", []string{
		xmlTkError("unknown token at 1:1"),
	}},
	{"element_01", "<a/>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkElementEnd(xmlEEEmpty, 2, 4),
	}},
	{"element_02", "<a></a>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkElementEnd(xmlEEClose("", "a"), 3, 7),
	}},
	{"element_03", "  \t  <a/>   \n ", []string{
		xmlTkElementStart("", "a", 5),
		xmlTkElementEnd(xmlEEEmpty, 7, 9),
	}},
	{"element_04", "  \t  <b><a/></b>   \n ", []string{
		xmlTkElementStart("", "b", 5),
		xmlTkElementEnd(xmlEEOpen, 7, 8),
		xmlTkElementStart("", "a", 8),
		xmlTkElementEnd(xmlEEEmpty, 10, 12),
		xmlTkElementEnd(xmlEEClose("", "b"), 12, 16),
	}},
	{"element_06", "<俄语 լեզու=\"ռուսերեն\">данные</俄语>", []string{
		xmlTkElementStart("", "俄语", 0),
		xmlTkAttribute("", "լեզու", "ռուսերեն"),
		xmlTkElementEnd(xmlEEOpen, 37, 38),
		xmlTkText("данные", 38, 50),
		xmlTkElementEnd(xmlEEClose("", "俄语"), 50, 59),
	}},
	{"element_07", "<svg:circle></svg:circle>", []string{
		xmlTkElementStart("svg", "circle", 0),
		xmlTkElementEnd(xmlEEOpen, 11, 12),
		xmlTkElementEnd(xmlEEClose("svg", "circle"), 12, 25),
	}},
	{"element_08", "<:circle/>", []string{
		xmlTkElementStart("", "circle", 0),
		xmlTkElementEnd(xmlEEEmpty, 8, 10),
	}},
	{"element_err_01", "<>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_02", "</", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_03", "</a", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_04", "<a x='test' /", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkAttribute("", "x", "test"),
		xmlTkError("unexpected end of stream"),
	}},
	{"element_err_05", "<<", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_06", "< a", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_07", "< ", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_08", "<&#x9;", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_09", "<a></a></a>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkElementEnd(xmlEEClose("", "a"), 3, 7),
		xmlTkError("unknown token at 1:8"),
	}},
	{"element_err_10", "<a/><a/>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkElementEnd(xmlEEEmpty, 2, 4),
		xmlTkError("unknown token at 1:5"),
	}},
	{"element_err_11", "<a></br/></a>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkError("expected '>' not '/' at 1:8"),
	}},
	{"element_err_12", "<svg:/>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_13", "<root>\n</root>\n</root>", []string{
		xmlTkElementStart("", "root", 0),
		xmlTkElementEnd(xmlEEOpen, 5, 6),
		xmlTkText("\n", 6, 7),
		xmlTkElementEnd(xmlEEClose("", "root"), 7, 14),
		xmlTkError("unknown token at 3:1"),
	}},
	{"element_err_14", "<-svg/>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_15", "<svg:-svg/>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_16", "<svg::svg/>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_17", "<svg:s:vg/>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_18", "<::svg/>", []string{
		xmlTkError("invalid name token at 1:2"),
	}},
	{"element_err_19", "<a><", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkError("unknown token at 1:4"),
	}},
	{"attribute_01", "<a ax=\"test\"/>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkAttribute("", "ax", "test"),
		xmlTkElementEnd(xmlEEEmpty, 12, 14),
	}},
	{"attribute_02", "<a ax='test'/>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkAttribute("", "ax", "test"),
		xmlTkElementEnd(xmlEEEmpty, 12, 14),
	}},
	{"attribute_03", "<a b='test1' c=\"test2\"/>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkAttribute("", "b", "test1"),
		xmlTkAttribute("", "c", "test2"),
		xmlTkElementEnd(xmlEEEmpty, 22, 24),
	}},
	{"attribute_04", "<a b='\"test1\"' c=\"'test2'\"/>", []string{
		xmlTkElementStart("", "a", 0),
		xmlTkAttribute("", "b", "\"test1\""),
		xmlTkAttribute("", "c", "'test2'"),
		xmlTkElementEnd(xmlEEEmpty, 26, 28),
	}},
	{"attribute_05", "<c a=\"test1' c='test2\" b='test1\" c=\"test2'/>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkAttribute("", "a", "test1' c='test2"),
		xmlTkAttribute("", "b", "test1\" c=\"test2"),
		xmlTkElementEnd(xmlEEEmpty, 42, 44),
	}},
	{"attribute_06", "<c   a   =    'test1'     />", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkAttribute("", "a", "test1"),
		xmlTkElementEnd(xmlEEEmpty, 26, 28),
	}},
	{"attribute_07", "<c q:a='b'/>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkAttribute("q", "a", "b"),
		xmlTkElementEnd(xmlEEEmpty, 10, 12),
	}},
	{"attribute_err_01", "<c az=test>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkError("expected a quote not 't' at 1:7"),
	}},
	{"attribute_err_02", "<c a>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkError("expected '=' not '>' at 1:5"),
	}},
	{"attribute_err_03", "<c a/>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkError("expected '=' not '/' at 1:5"),
	}},
	{"attribute_err_04", "<c a='b' q/>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkAttribute("", "a", "b"),
		xmlTkError("expected '=' not '/' at 1:11"),
	}},
	{"attribute_err_05", "<c a='<'/>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkError("expected ''' not '<' at 1:7"),
	}},
	{"attribute_err_06", "<c a='\u0001'/>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkError("a non-XML character '\\u{1}' found at 1:7"),
	}},
	{"attribute_err_07", "<c a='v'b='v'/>", []string{
		xmlTkElementStart("", "c", 0),
		xmlTkAttribute("", "a", "v"),
		xmlTkError("expected a whitespace not 'b' at 1:9"),
	}},
	{"pi_01", "<?xslt ma?>", []string{
		xmlTkPI("xslt", xmlSome("ma"), 0, 11),
	}},
	{"pi_02", "<?xslt \t\n m?>", []string{
		xmlTkPI("xslt", xmlSome("m"), 0, 13),
	}},
	{"pi_03", "<?xslt?>", []string{
		xmlTkPI("xslt", nil, 0, 8),
	}},
	{"pi_04", "<?xslt ?>", []string{
		xmlTkPI("xslt", nil, 0, 9),
	}},
	{"pi_05", "<?xml-stylesheet?>", []string{
		xmlTkPI("xml-stylesheet", nil, 0, 18),
	}},
	{"pi_err_01", "<??xml \t\n m?>", []string{
		xmlTkError("invalid name token at 1:3"),
	}},
	{"declaration_01", "<?xml version=\"1.0\"?>", []string{}},
	{"declaration_02", "<?xml version='1.0'?>", []string{}},
	{"declaration_03", "<?xml version='1.0' encoding=\"UTF-8\"?>", []string{}},
	{"declaration_04", "<?xml version='1.0' encoding='UTF-8'?>", []string{}},
	{"declaration_05", "<?xml version='1.0' encoding='utf-8'?>", []string{}},
	{"declaration_06", "<?xml version='1.0' encoding='EUC-JP'?>", []string{}},
	{"declaration_07", "<?xml version='1.0' encoding='UTF-8' standalone='yes'?>", []string{}},
	{"declaration_08", "<?xml version='1.0' encoding='UTF-8' standalone='no'?>", []string{}},
	{"declaration_09", "<?xml version='1.0' standalone='no'?>", []string{}},
	{"declaration_10", "<?xml version='1.0' standalone='no' ?>", []string{}},
	{"declaration_err_01", "<?xml encoding='UTF-8' version='1.0'?>", []string{
		xmlTkError("expected 'version' at 1:7"),
	}},
	{"declaration_err_05", "<?xml version='1.0' yes='true'?>", []string{
		xmlTkError("expected '?>' at 1:21"),
	}},
	{"declaration_err_06", "<?xml version='1.0' encoding='UTF-8' standalone='yes' yes='true'?>", []string{
		xmlTkError("expected '?>' at 1:55"),
	}},
	{"declaration_err_07", "\n<?xml\n&jg'];", []string{
		xmlTkError("expected '?>' at 3:7"),
	}},
	{"declaration_err_08", "<?xml \t\n ?m?>", []string{
		xmlTkError("expected 'version' at 2:2"),
	}},
	{"declaration_err_09", "<?xml \t\n m?>", []string{
		xmlTkError("expected 'version' at 2:2"),
	}},
	{"declaration_err_10", " <?xml version='1.0'?>", []string{
		xmlTkError("unexpected XML declaration at 1:2"),
	}},
	{"declaration_err_11", "<!-- comment --><?xml version='1.0'?>", []string{
		xmlTkComment(" comment ", 0, 16),
		xmlTkError("unexpected XML declaration at 1:17"),
	}},
	{"declaration_err_12", "<?xml version='1.0'?><?xml version='1.0'?>", []string{
		xmlTkError("unexpected XML declaration at 1:22"),
	}},
	{"declaration_err_13", "<?target \u0001content>", []string{
		xmlTkError("a non-XML character '\\u{1}' found at 1:10"),
	}},
	{"declaration_err_14", "<?xml version='1.0'encoding='UTF-8'?>", []string{
		xmlTkError("expected a whitespace not 'e' at 1:20"),
	}},
	{"declaration_err_15", "<?xml version='1.0' encoding='UTF-8'standalone='yes'?>", []string{
		xmlTkError("expected a whitespace not 's' at 1:37"),
	}},
	{"declaration_err_16", "<?xml version='1.0'", []string{
		xmlTkError("expected '?>' at 1:20"),
	}},
	{"text_01", "<p>text</p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkText("text", 3, 7),
		xmlTkElementEnd(xmlEEClose("", "p"), 7, 11),
	}},
	{"text_02", "<p> text </p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkText(" text ", 3, 9),
		xmlTkElementEnd(xmlEEClose("", "p"), 9, 13),
	}},
	{"text_03", "<p>欄</p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkText("欄", 3, 6),
		xmlTkElementEnd(xmlEEClose("", "p"), 6, 10),
	}},
	{"text_04", "<p> </p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkText(" ", 3, 4),
		xmlTkElementEnd(xmlEEClose("", "p"), 4, 8),
	}},
	{"text_05", "<p> \r\n\t </p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkText(" \r\n\t ", 3, 8),
		xmlTkElementEnd(xmlEEClose("", "p"), 8, 12),
	}},
	{"text_06", "<p>&#x20;</p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkText("&#x20;", 3, 9),
		xmlTkElementEnd(xmlEEClose("", "p"), 9, 13),
	}},
	{"text_07", "<p>]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkText("]>", 3, 5),
		xmlTkElementEnd(xmlEEClose("", "p"), 5, 9),
	}},
	{"text_err_01", "<p>]]></p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkError("']]>' at 1:7 is not allowed inside a character data"),
	}},
	{"text_err_02", "<p>\u000c</p>", []string{
		xmlTkElementStart("", "p", 0),
		xmlTkElementEnd(xmlEEOpen, 2, 3),
		xmlTkError("a non-XML character '\\u{c}' found at 1:4"),
	}},
}

var xmlTokenizerErrCases = []struct{ name, text string }{
	{"comment_err_01", "<!----!>"},
	{"comment_err_02", "<!----!"},
	{"comment_err_03", "<!----"},
	{"comment_err_04", "<!--->"},
	{"comment_err_05", "<!-----"},
	{"comment_err_06", "<!-->"},
	{"comment_err_07", "<!--"},
	{"comment_err_08", "<!--x"},
	{"comment_err_09", "<!--<"},
	{"comment_err_10", "<!--<!"},
	{"comment_err_11", "<!--<!-"},
	{"comment_err_12", "<!--<!--"},
	{"comment_err_13", "<!--<!--!"},
	{"comment_err_14", "<!--<!--!>"},
	{"comment_err_15", "<!--<!---"},
	{"comment_err_16", "<!--<!--x"},
	{"comment_err_17", "<!--<!--x-"},
	{"comment_err_18", "<!--<!--x--"},
	{"comment_err_19", "<!--<!--x-->"},
	{"comment_err_20", "<!--<!-x"},
	{"comment_err_21", "<!--<!-x-"},
	{"comment_err_22", "<!--<!-x--"},
	{"comment_err_23", "<!--<!x"},
	{"comment_err_24", "<!--<!x-"},
	{"comment_err_25", "<!--<!x--"},
	{"comment_err_26", "<!--<<!--x-->"},
	{"comment_err_27", "<!--<!<!--x-->"},
	{"comment_err_28", "<!--<!-<!--x-->"},
	{"comment_err_29", "<!----!->"},
	{"comment_err_30", "<!----!x>"},
	{"comment_err_31", "<!-----x>"},
	{"comment_err_32", "<!----->"},
	{"comment_err_33", "<!------>"},
	{"comment_err_34", "<!-- --->"},
	{"comment_err_35", "<!--a--->"},
}
