package usvg

import (
	"slices"
	"strings"
	"testing"
)

// Ported from simplecss 0.2.2 tests/ (declaration_tokenizer, selector_tokenizer, specificity, select,
// stylesheet), plus extra cases whose expectations come from running simplecss 0.2.2. warnings.rs checks
// log output, which this port does not produce.

func TestCSSDeclarationTokenizer(t *testing.T) {
	d := func(name, value string) cssDeclaration { return cssDeclaration{Name: name, Value: value} }
	di := func(name, value string) cssDeclaration {
		return cssDeclaration{Name: name, Value: value, Important: true}
	}
	for _, c := range []struct {
		text string
		want []cssDeclaration
	}{
		{"", nil},
		{" ", nil},
		{"/**/", nil},
		{"color:red", []cssDeclaration{d("color", "red")}},
		{"color:red;", []cssDeclaration{d("color", "red")}},
		{"color:red ", []cssDeclaration{d("color", "red")}},
		{" color: red; ", []cssDeclaration{d("color", "red")}},
		{"  color  :  red  ; ", []cssDeclaration{d("color", "red")}},
		{"  color:red;;;;color:red; ", []cssDeclaration{d("color", "red"), d("color", "red")}},
		{"background: url(\"img.png\");", []cssDeclaration{d("background", "url(\"img.png\")")}},
		{"background: url(\"{}\");", []cssDeclaration{d("background", "url(\"{}\")")}},
		{"color: red ! important", []cssDeclaration{di("color", "red")}},
		{"color: red !important", []cssDeclaration{di("color", "red")}},
		{"color: red!important", []cssDeclaration{di("color", "red")}},
		{"color: red !/**/important", []cssDeclaration{di("color", "red")}},
		{"border: 1em solid blue", []cssDeclaration{d("border", "1em solid blue")}},
		{"background: navy url(support/diamond.png) -2em -2em no-repeat",
			[]cssDeclaration{d("background", "navy url(support/diamond.png) -2em -2em no-repeat")}},
		{"/**/color:red", []cssDeclaration{d("color", "red")}},
		{"/* *\\/*/color: red;", []cssDeclaration{d("color", "red")}},
		{"/**/color/**/:/**/red/**/;/**/", []cssDeclaration{d("color", "red")}},
		{"\ncolor\n:\nred\n;\n", []cssDeclaration{d("color", "red")}},
		{"{color:red}", nil},
		{"(color:red)", nil},
		{"[color:red]", nil},
		{"color:", nil},
		{"value:\"text\"", []cssDeclaration{d("value", "\"text\"")}},
		{"value:'text'", []cssDeclaration{d("value", "'text'")}},
		{"color:#fff", []cssDeclaration{d("color", "#fff")}},
		{"color:0.5", []cssDeclaration{d("color", "0.5")}},
		{"color:.5", []cssDeclaration{d("color", ".5")}},
		{"color:#FFF", []cssDeclaration{d("color", "#FFF")}},
		{"content: counter(chapno, upper-roman) \". \"", []cssDeclaration{d("content", "counter(chapno, upper-roman) \". \"")}},
		{"font-family:'Noto Serif','DejaVu Serif',serif", []cssDeclaration{d("font-family", "'Noto Serif','DejaVu Serif',serif")}},
		{"*zoom:1;", []cssDeclaration{d("zoom", "1")}},
	} {
		if got := cssCollectDeclarations(c.text); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.text, got, c.want)
		}
	}
}

func cssCollectDeclarations(text string) []cssDeclaration {
	tok := cssNewDeclarationTokenizer(text)
	var got []cssDeclaration
	for {
		d, ok := tok.Next()
		if !ok {
			return got
		}
		got = append(got, d)
	}
}

// cssFormatDeclarations formats declarations as the reference oracle prints them.
func cssFormatDeclarations(ds []cssDeclaration) string {
	var parts []string
	for _, d := range ds {
		s := d.Name + "=" + d.Value
		if d.Important {
			s += "!"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "|")
}

func TestCSSSelectorTokenizer(t *testing.T) {
	tok := func(k cssSelectorTokenKind, ident string) cssSelectorToken {
		return cssSelectorToken{Kind: k, Ident: ident}
	}
	attr := func(name string, k cssAttrOpKind, v string) cssSelectorToken {
		return cssSelectorToken{Kind: cssTokAttributeSelector, Ident: name, Op: cssAttributeOperator{Kind: k, Value: v}}
	}
	uni := tok(cssTokUniversalSelector, "")
	desc := tok(cssTokDescendantCombinator, "")
	child := tok(cssTokChildCombinator, "")
	adj := tok(cssTokAdjacentCombinator, "")
	typ := func(s string) cssSelectorToken { return tok(cssTokTypeSelector, s) }
	class := func(s string) cssSelectorToken { return tok(cssTokClassSelector, s) }
	idt := func(s string) cssSelectorToken { return tok(cssTokIDSelector, s) }
	pseudo := func(s string) cssSelectorToken { return tok(cssTokPseudoClass, s) }
	type ts = []cssSelectorToken
	for _, c := range []struct {
		text string
		want ts
	}{
		{"*", ts{uni}},
		{"div", ts{typ("div")}},
		{"#div", ts{idt("div")}},
		{".div", ts{class("div")}},
		{"[id]", ts{attr("id", cssAttrExists, "")}},
		{"[id=test]", ts{attr("id", cssAttrMatches, "test")}},
		{"[id~=test]", ts{attr("id", cssAttrContains, "test")}},
		{"[id|=test]", ts{attr("id", cssAttrStartsWith, "test")}},
		{"[id='test']", ts{attr("id", cssAttrMatches, "test")}},
		{"[id=\"test\"]", ts{attr("id", cssAttrMatches, "test")}},
		{"[id='te\\'st']", ts{attr("id", cssAttrMatches, "te\\'st")}},
		{"[id=\"te\\\"st\"]", ts{attr("id", cssAttrMatches, "te\\\"st")}},
		{"div:first-child", ts{typ("div"), pseudo("first-child")}},
		{":first-child", ts{pseudo("first-child")}},
		{"div p", ts{typ("div"), desc, typ("p")}},
		{"div p a", ts{typ("div"), desc, typ("p"), desc, typ("a")}},
		{"div>p", ts{typ("div"), child, typ("p")}},
		{"div >p", ts{typ("div"), child, typ("p")}},
		{"div> p", ts{typ("div"), child, typ("p")}},
		{"div > p", ts{typ("div"), child, typ("p")}},
		{"div .p", ts{typ("div"), desc, class("p")}},
		{"div *", ts{typ("div"), desc, uni}},
		{"div #p", ts{typ("div"), desc, idt("p")}},
		{"div [id]", ts{typ("div"), desc, attr("id", cssAttrExists, "")}},
		{"div :link", ts{typ("div"), desc, pseudo("link")}},
		{"div+p", ts{typ("div"), adj, typ("p")}},
		{"div +p", ts{typ("div"), adj, typ("p")}},
		{"div+ p", ts{typ("div"), adj, typ("p")}},
		{"div + p", ts{typ("div"), adj, typ("p")}},
		{"div {", ts{typ("div")}},
		{"div,", ts{typ("div")}},
		{"div{", ts{typ("div")}},
		{"div ,", ts{typ("div")}},
		{"div.test", ts{typ("div"), class("test")}},
		{"div.test.warn", ts{typ("div"), class("test"), class("warn")}},
		{"div#id", ts{typ("div"), idt("id")}},
		{"*[id]", ts{uni, attr("id", cssAttrExists, "")}},
		{"*.test", ts{uni, class("test")}},
		{"*#id", ts{uni, idt("id")}},
		{"div * p", ts{typ("div"), desc, uni, desc, typ("p")}},
		{"div[id=test][color=red]", ts{typ("div"), attr("id", cssAttrMatches, "test"), attr("color", cssAttrMatches, "red")}},
		{"a.external:visited", ts{typ("a"), class("external"), pseudo("visited")}},
		{":lang(en)", ts{tok(cssTokLangPseudoClass, "en")}},
		{"a\nb", ts{typ("a"), desc, typ("b")}},
		{".warn :first-child", ts{class("warn"), desc, pseudo("first-child")}},
		{"/**/a", ts{typ("a")}},
		{"/* */a", ts{typ("a")}},
		{"/* comment */a", ts{typ("a")}},
		{"/**/ /**/a", ts{typ("a")}},
		{"/**/ a /**/", ts{typ("a")}},
		{"a /**/ b", ts{typ("a"), desc, typ("b")}},
		{"a /**/b", ts{typ("a"), desc, typ("b")}},
		{"a/**/ b", ts{typ("a"), desc, typ("b")}},
		{"a/**/ /**/b", ts{typ("a"), desc, typ("b")}},
		{"a /**/ /**/ b", ts{typ("a"), desc, typ("b")}},
		{"a /**//**/ b", ts{typ("a"), desc, typ("b")}},
	} {
		tz := cssNewSelectorTokenizer(c.text)
		var got ts
		for {
			token, ok, err := tz.Next()
			if !ok {
				break
			}
			if err != nil {
				t.Errorf("%q: unexpected error %v", c.text, err)
				break
			}
			got = append(got, token)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.text, got, c.want)
		}
	}
}

func TestCSSSelectorTokenizerMalformed(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{">", "unexpected combinator"},
		{"+", "unexpected combinator"},
		{"> a", "unexpected combinator"},
		{"a >", "selector missing"},
		{"*a", "unexpected selector"},
		{"a*", "unexpected selector"},
		{"a > ,", "selector missing"},
		{"a > >", "unexpected combinator"},
		{"a > {", "selector missing"},
		{"a/**/b", "unexpected selector"},
		{"a < b", "invalid ident at 1:3"},
		{":lang()", "invalid language pseudo-class"},
		{":lang( )", "invalid language pseudo-class"},
		{"::first-child", "invalid ident at 1:2"},
		{"[olor:red", "invalid or unsupported attribute selector"},
		{"", "selector missing"},
		{" ", "selector missing"},
		{"/**/", "selector missing"},
		// Extra cases.
		{"[a=b", "unexpected end of stream"},
		{"[a~b]", "expected '=' not 'b' at 1:4"},
		{"a /* x", "invalid comment at 1:3"},
		{"a\n\u00e9", "invalid ident at 2:1"},
	} {
		tz := cssNewSelectorTokenizer(c.text)
		got := ""
		for {
			_, ok, err := tz.Next()
			if !ok {
				break
			}
			if err != nil {
				got = err.Error()
				break
			}
		}
		if got != c.want {
			t.Errorf("%q: got error %q, want %q", c.text, got, c.want)
		}
	}
}

func TestCSSSpecificity(t *testing.T) {
	for _, c := range []struct {
		text string
		want [3]uint8
	}{
		{"*", [3]uint8{0, 0, 0}},
		{"li", [3]uint8{0, 0, 1}},
		{"ul li", [3]uint8{0, 0, 2}},
		{"ul ol + li", [3]uint8{0, 0, 3}},
		{"h1 + *[rel=up]", [3]uint8{0, 1, 1}},
		{"ul ol li.red", [3]uint8{0, 1, 3}},
		{"li.red.level", [3]uint8{0, 2, 1}},
		{"#x34y", [3]uint8{1, 0, 0}},
		{"[id=x]:first-child", [3]uint8{1, 1, 0}},
	} {
		sel, ok := cssParseSelector(c.text)
		if !ok {
			t.Fatalf("%q: no selector", c.text)
		}
		if got := sel.Specificity(); got != c.want {
			t.Errorf("%q: got %v, want %v", c.text, got, c.want)
		}
	}
	// Counters saturate at 255.
	sel, _ := cssParseSelector(strings.Repeat(".a", 300))
	if got := sel.Specificity(); got != [3]uint8{0, 255, 0} {
		t.Errorf("saturation: got %v", got)
	}
}

// cssTestNode is a minimal element tree implementing cssElement as usvg's XmlNode does.
type cssTestNode struct {
	name     string
	attrs    map[string]string
	parent   *cssTestNode
	children []*cssTestNode
}

func cssEl(name string, attrs map[string]string, children ...*cssTestNode) *cssTestNode {
	n := &cssTestNode{name: name, attrs: attrs, children: children}
	for _, c := range children {
		c.parent = n
	}
	return n
}

func (n *cssTestNode) ParentElement() (cssElement, bool) {
	if n.parent == nil {
		return nil, false
	}
	return n.parent, true
}

func (n *cssTestNode) PrevSiblingElement() (cssElement, bool) {
	if n.parent == nil {
		return nil, false
	}
	i := slices.Index(n.parent.children, n)
	if i <= 0 {
		return nil, false
	}
	return n.parent.children[i-1], true
}

func (n *cssTestNode) HasLocalName(name string) bool { return n.name == name }

func (n *cssTestNode) AttributeMatches(localName string, op cssAttributeOperator) bool {
	v, ok := n.attrs[localName]
	return ok && op.Matches(v)
}

func (n *cssTestNode) PseudoClassMatches(class cssPseudoClass) bool {
	if class.Kind == cssPseudoFirstChild {
		_, ok := n.PrevSiblingElement()
		return !ok
	}
	return false
}

// selectIDs returns the id attributes of the elements matching sel, in document order.
func (n *cssTestNode) selectIDs(t *testing.T, sel string) []string {
	s, ok := cssParseSelector(sel)
	if !ok {
		t.Fatalf("%q: no selector", sel)
	}
	var out []string
	var walk func(*cssTestNode)
	walk = func(e *cssTestNode) {
		if s.Matches(e) {
			out = append(out, e.attrs["id"])
		}
		for _, c := range e.children {
			walk(c)
		}
	}
	walk(n)
	return out
}

func TestCSSSelect(t *testing.T) {
	type m = map[string]string
	id := func(v string) m { return m{"id": v} }
	div1 := cssEl("div", id("div1"))
	divP := cssEl("div", id("div1"), cssEl("p", id("p1")))
	divGP := cssEl("div", id("div1"), cssEl("g", id("g1"), cssEl("p", id("p1"))))
	rects := cssEl("div", id("div1"), cssEl("p", id("p1"), cssEl("rect", id("rect1")), cssEl("rect", m{"id": "rect2", "color": "green"})))
	withP := func(attrs m) *cssTestNode { return cssEl("div", id("div1"), cssEl("p", attrs)) }
	for _, c := range []struct {
		doc  *cssTestNode
		sel  string
		want []string
	}{
		{div1, "*", []string{"div1"}},
		{div1, "div", []string{"div1"}},
		{div1, "p", nil},
		{div1, "#div1", []string{"div1"}},
		{div1, "#d1", nil},
		{div1, "p#div1", nil},
		{divP, "div p", []string{"p1"}},
		{divGP, "div p", []string{"p1"}},
		{cssEl("div", id("div1"), cssEl("div", id("div2"), cssEl("g", id("g1"), cssEl("p", id("p1"))))), "div p", []string{"p1"}},
		{cssEl("div", id("div1"), cssEl("g", id("g1"), cssEl("p", id("p1"), cssEl("div", nil)))), "div p", []string{"p1"}},
		{divGP, "div g p", []string{"p1"}},
		{cssEl("div", id("div1"), cssEl("q", id("g1"), cssEl("p", id("p1")))), "div g p", nil},
		{divGP, "div * p", []string{"p1"}},
		{rects, "div p *[color]", []string{"rect2"}},
		{rects, "div p [color]", []string{"rect2"}},
		{divP, "div > p", []string{"p1"}},
		{cssEl("p", id("p1")), "div > p", nil},
		{divGP, "div > p", nil},
		{cssEl("div", id("div1"), cssEl("p", nil, cssEl("ol", nil, cssEl("li", nil, cssEl("g", nil, cssEl("p", id("p1"))))))), "div ol>li p", []string{"p1"}},
		{cssEl("div", id("div1"), cssEl("p", nil, cssEl("ol", nil, cssEl("g", nil, cssEl("li", nil, cssEl("g", nil, cssEl("p", id("p1")))))))), "div ol>li p", nil},
		{cssEl("div", id("div1"), cssEl("g", nil), cssEl("p", id("p1"))), "g + p", []string{"p1"}},
		{cssEl("div", id("div1"), cssEl("test", nil), cssEl("g", nil), cssEl("p", id("p1"))), "g + p", []string{"p1"}},
		{cssEl("div", id("div1"), cssEl("p", id("p1")), cssEl("g", nil)), "g + p", nil},
		{divP, "div + p", nil},
		{divP, "[id=p1]", []string{"p1"}},
		{withP(m{"id": "p1", "class": "test warn"}), "[class~=warn]", []string{"p1"}},
		{withP(m{"id": "p1", "class": "test warn"}), "[class~='test warn']", nil},
		{withP(m{"id": "p1", "lang": "en"}), "[lang=en]", []string{"p1"}},
		{withP(m{"id": "p1", "lang": "en"}), "[lang|=en]", []string{"p1"}},
		{withP(m{"id": "p1", "lang": "en-US"}), "[lang='en-US']", []string{"p1"}},
		{withP(m{"id": "p1", "lang": "en-US"}), "[lang|=en]", []string{"p1"}},
		{withP(m{"id": "p1", "lang": "english"}), "[lang|=en]", nil},
		{withP(m{"id": "p1", "class": "pastoral blue aqua marine"}), ".marine.pastoral", []string{"p1"}},
		{divP, "p:first-child", []string{"p1"}},
		{cssEl("div", id("div1"), cssEl("rect", nil), cssEl("p", id("p1"))), "p:first-child", nil},
		{cssEl("div", id("div1"), cssEl("p", id("p1")), cssEl("p", id("p2"))), ":first-child", []string{"div1", "p1"}},
	} {
		if got := c.doc.selectIDs(t, c.sel); !slices.Equal(got, c.want) {
			t.Errorf("%q: got %q, want %q", c.sel, got, c.want)
		}
	}

	sel, _ := cssParseSelector("a > b")
	if got := sel.String(); got != "a > b" {
		t.Errorf("String: got %q", got)
	}
	for _, text := range []string{":nth-child", "a >", "", "> a"} {
		if _, ok := cssParseSelector(text); ok {
			t.Errorf("%q: want no selector", text)
		}
	}
}

func TestCSSStyleSheet(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"", ""},
		{"a {}", ""},
		{"a { color:red }", "a { color:red; }"},
		{"/**/", ""},
		{"a { color:red } /**/", "a { color:red; }"},
		{"a, b { color:red }", "a { color:red; }\nb { color:red; }"},
		{"a, { color:red }", "a { color:red; }"},
		{"a,, { color:red }", "a { color:red; }"},
		{"a,,b { color:red }", "a { color:red; }\nb { color:red; }"},
		{",a { color:red }", "a { color:red; }"},
		{"@import \"subs.css\";\na { color:red }", "a { color:red; }"},
		{"@media screen {\n    p:before { content: 'Hello'; }\n}\na { color:red }", "a { color:red; }"},
		{"a > { color:red }", ""},
		{"p { color:green; color }", "p { color:green; }"},
		{"p { color; color:green }", ""},
		{"p { color:green; color: }", "p { color:green; }"},
		{"p { color:green; color:; color:red; }", "p { color:green; }"},
		{"p { color:green; color{;color:maroon} }", "p { color:green; }"},
		{"p { color{;color:maroon} color:green; }", ""},
		{"        h1 { color: green }\n        h2 & h3 { color: red }\n        h4 { color: black }\n    ", "h1 { color:green; }\nh4 { color:black; }"},
		{":le>*", ""},
	} {
		if got := cssParseStyleSheet(c.text).String(); got != c.want {
			t.Errorf("%q:\ngot  %q\nwant %q", c.text, got, c.want)
		}
	}
}

// cssOracleSheets and cssOracleDecls hold inputs with the output of simplecss 0.2.2 (StyleSheet::parse
// then Display, and DeclarationTokenizer), covering rule order, !important, at-rules, unclosed blocks
// and comments, unsupported selectors and non-ASCII text.
var cssOracleSheets = []struct{ text, want string }{
	{"#a{fill:red} .b{fill:blue} c{fill:green} *{fill:x} d{o:1}", "* { fill:x; }\nc { fill:green; }\nd { o:1; }\n*[class~='b'] { fill:blue; }\n*[id='a'] { fill:red; }"},
	{"rect { fill: red !important; stroke:blue }", "rect { fill:red !important;stroke:blue; }"},
	{"a:hover { fill:red } b { fill:blue }", "b { fill:blue; }\na:hover { fill:red; }"},
	{"a:nth-child(2) { fill:red } b { fill:blue }", "b { fill:blue; }"},
	{"a { fill:red", "a { fill:red; }"},
	{"a { fill:red } /* unclosed", "a { fill:red; }"},
	{"/* unclosed", ""},
	{"@font-face { src: url(x) } a{b:c}", "a { b:c; }"},
	{"@ { } a{b:c}", "a { b:c; }"},
	{"}a{b:c}", ""},
	{"svg|rect { fill:red } circle { fill:blue }", "circle { fill:blue; }"},
	{"a b > c + d { x:1 }", "a b > c + d { x:1; }"},
	{"[x=\"1\"] { y:2 }", "*[x='1'] { y:2; }"},
	{":lang(en) { y:2 }", "*:lang(en) { y:2; }"},
	{".cls-1{fill:#fff;}.cls-2{fill:none;stroke:#000;stroke-width:2px;}", "*[class~='cls-1'] { fill:#fff; }\n*[class~='cls-2'] { fill:none;stroke:#000;stroke-width:2px; }"},
	{"g > rect.a#b[c]:first-child, path { opacity: 0.5 !important }", "path { opacity:0.5 !important; }\ng > rect[class~='a'][id='b'][c]:first-child { opacity:0.5 !important; }"},
	{"@media print { a { b: c } } @charset \"x\"; d { e: f }", "d { e:f; }"},
	{"a { b: c; } } d { e: f }", "a { b:c; }"},
	{"\u00e9t\u00e9 { fill: red } \u0100x { fill: blue }", "\u0100x { fill:blue; }"},
	{"a { font-family: \"A B\", 'C' ; fill: url(#g) }", "a { font-family:\"A B\", 'C';fill:url(#g); }"},
}

var cssOracleDecls = []struct{ text, want string }{
	{"fill:red;stroke", "fill=red"},
	{"fill:red /* unclosed", ""},
	{"fill: #12345z; opacity: .5", "fill=#12345z|opacity=.5"},
	{"fill:red !imp; x:1", "fill=red"},
	{"a:1;b: 2px 3% -4.5e; c : url(x) ", "a=1|b=2px 3% -4.5e|c=url(x)"},
	{"\u00e9:1", ""},
	{"fill:\u00ffx", "fill=\u00ffx"},
	{"fill:red;;stroke:blue !important;stroke-width:2", "fill=red|stroke=blue!|stroke-width=2"},
	{"fill: rgb(1, 2, 3); stroke: url( #a )", "fill=rgb(1, 2, 3)|stroke=url( #a )"},
	{"fill: 'unterminated", ""},
	{"marker-start:url(#m);display:none", "marker-start=url(#m)|display=none"},
	{"-inkscape-font:x; fill:red", "-inkscape-font=x|fill=red"},
	{"fill:+.5e-3", "fill=+.5e-3"},
}

func TestCSSOracle(t *testing.T) {
	for _, c := range cssOracleSheets {
		if got := cssParseStyleSheet(c.text).String(); got != c.want {
			t.Errorf("sheet %q:\ngot  %q\nwant %q", c.text, got, c.want)
		}
	}
	for _, c := range cssOracleDecls {
		if got := cssFormatDeclarations(cssCollectDeclarations(c.text)); got != c.want {
			t.Errorf("declarations %q: got %q, want %q", c.text, got, c.want)
		}
	}
}

func TestCSSParseMore(t *testing.T) {
	// usvg parses the injected sheet first, then each <style>; all rules are re-sorted, stably, each time.
	sh := cssNewStyleSheet()
	sh.ParseMore("#x { a:1 } r { b:2 }")
	sh.ParseMore(".c { d:3 } r { e:4 } /* unclosed")
	sh.ParseMore("/* only an unclosed comment")
	want := "r { b:2; }\nr { e:4; }\n*[class~='c'] { d:3; }\n*[id='x'] { a:1; }"
	if got := sh.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCSSAttributeOperator(t *testing.T) {
	for _, c := range []struct {
		op    cssAttributeOperator
		value string
		want  bool
	}{
		{cssAttributeOperator{Kind: cssAttrExists}, "", true},
		{cssAttributeOperator{Kind: cssAttrMatches, Value: "a"}, "a", true},
		{cssAttributeOperator{Kind: cssAttrMatches, Value: "a"}, "a ", false},
		{cssAttributeOperator{Kind: cssAttrContains, Value: "b"}, "a b c", true},
		{cssAttributeOperator{Kind: cssAttrContains, Value: "b"}, "a\tb", false},
		{cssAttributeOperator{Kind: cssAttrContains, Value: ""}, "a  b", true},
		{cssAttributeOperator{Kind: cssAttrStartsWith, Value: "en"}, "en", true},
		{cssAttributeOperator{Kind: cssAttrStartsWith, Value: "en"}, "en-GB", true},
		{cssAttributeOperator{Kind: cssAttrStartsWith, Value: "en"}, "eng", false},
		{cssAttributeOperator{Kind: cssAttrStartsWith, Value: "en"}, "e", false},
	} {
		if got := c.op.Matches(c.value); got != c.want {
			t.Errorf("%+v.Matches(%q) = %v", c.op, c.value, got)
		}
	}
}
