package usvg

// Ported from roxmltree 0.21.1 tests/api.rs, tests/dom-api.rs and tests/ast.rs (the fixture-driven
// tree dumps, copied under testdata/usvg-xml).

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// xmlTestdata holds the ast.rs fixtures; it becomes ../testdata/usvg-xml after the move.
var xmlTestdata = filepath.Join("..", "testdata", "usvg-xml")

func xmlMustParse(t *testing.T, text string) *xmlDocument {
	t.Helper()
	doc, err := xmlParse(text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return doc
}

func xmlWantStr(t *testing.T, what string, got string, gotOK bool, want string, wantOK bool) {
	t.Helper()
	if got != want || gotOK != wantOK {
		t.Errorf("%s = (%q, %v), want (%q, %v)", what, got, gotOK, want, wantOK)
	}
}

func TestXMLRootElement01(t *testing.T) {
	doc := xmlMustParse(t, "<!-- comment -->\n<e/>\n")
	if got := doc.RootElement().TagName().Name(); got != "e" {
		t.Fatalf("got %q", got)
	}
}

func TestXMLGetText01(t *testing.T) {
	doc := xmlMustParse(t, "<root>\n    Text1\n    <item>\n        Text2\n    </item>\n    Text3\n</root>\n")
	root := doc.RootElement()

	s, ok := root.Text()
	xmlWantStr(t, "root.Text", s, ok, "\n    Text1\n    ", true)
	s, ok = root.Tail()
	xmlWantStr(t, "root.Tail", s, ok, "", false)

	item := root.Children()[1]
	s, ok = item.Text()
	xmlWantStr(t, "item.Text", s, ok, "\n        Text2\n    ", true)
	s, ok = item.Tail()
	xmlWantStr(t, "item.Tail", s, ok, "\n    Text3\n", true)
}

func TestXMLGetText02(t *testing.T) {
	doc := xmlMustParse(t, "<root>&apos;</root>")
	s, ok := doc.RootElement().Text()
	xmlWantStr(t, "Text", s, ok, "'", true)
}

func TestXMLAPI01(t *testing.T) {
	doc := xmlMustParse(t, "<e a:attr='a_ns' b:attr='b_ns' attr='no_ns' xmlns:b='http://www.ietf.org' "+
		"xmlns:a='http://www.w3.org' xmlns='http://www.uvic.ca'/>\n")
	p := doc.RootElement()

	found := false
	for _, a := range p.Attributes() {
		if _, hasNS := a.Namespace(); !hasNS && a.Name() == "attr" {
			found = a.Value() == "no_ns"
			break
		}
	}
	if !found {
		t.Error("no attr without namespace")
	}
	if !p.HasAttribute("attr") {
		t.Error("HasAttribute(attr)")
	}

	s, ok := p.AttributeNS("http://www.w3.org", "attr")
	xmlWantStr(t, "AttributeNS(w3, attr)", s, ok, "a_ns", true)
	if !p.HasAttributeNS("http://www.w3.org", "attr") {
		t.Error("HasAttributeNS(w3, attr)")
	}

	s, ok = p.Attribute("attr2")
	xmlWantStr(t, "Attribute(attr2)", s, ok, "", false)
	if p.HasAttribute("attr2") {
		t.Error("HasAttribute(attr2)")
	}

	s, ok = p.AttributeNS("http://www.w2.org", "attr")
	xmlWantStr(t, "AttributeNS(w2, attr)", s, ok, "", false)
	if p.HasAttributeNS("http://www.w2.org", "attr") {
		t.Error("HasAttributeNS(w2, attr)")
	}

	s, ok = p.Attribute("b")
	xmlWantStr(t, "Attribute(b)", s, ok, "", false)
	if p.HasAttribute("b") {
		t.Error("HasAttribute(b)")
	}

	s, ok = p.Attribute("xmlns")
	xmlWantStr(t, "Attribute(xmlns)", s, ok, "", false)
	if p.HasAttribute("xmlns") {
		t.Error("HasAttribute(xmlns)")
	}
}

func TestXMLHasLocalName(t *testing.T) {
	doc := xmlMustParse(t, `<root xmlns:foo="http://example.com/foo"><foo:bar foo:baz="qux"/></root>`)
	all := doc.Descendants()
	node := all[len(all)-1]

	if !node.HasTagName("bar") {
		t.Error("HasTagName(bar)")
	}
	if !node.HasAttribute("baz") {
		t.Error("HasAttribute(baz)")
	}
	s, ok := node.Attribute("baz")
	xmlWantStr(t, "Attribute(baz)", s, ok, "qux", true)
}

func TestXMLGetPI(t *testing.T) {
	doc := xmlMustParse(t, "<?target value?>\n<root/>\n")
	node, _ := doc.Root().FirstChild()
	pi, ok := node.PI()
	if !ok || pi != (xmlPI{Target: "target", Value: "value", HasValue: true}) {
		t.Fatalf("got %+v, %v", pi, ok)
	}
}

func TestXMLLookupPrefix01(t *testing.T) {
	node := xmlMustParse(t, "<e xmlns:n1='http://www.w3.org' n1:a='b1'/>").RootElement()
	s, ok := node.LookupPrefix("http://www.w3.org")
	xmlWantStr(t, "LookupPrefix(w3)", s, ok, "n1", true)
	s, ok = node.LookupPrefix("http://www.w4.org")
	xmlWantStr(t, "LookupPrefix(w4)", s, ok, "", false)
}

func TestXMLLookupPrefix02(t *testing.T) {
	node := xmlMustParse(t, "<e xml:space='preserve'/>").RootElement()
	s, ok := node.LookupPrefix(xmlNSXMLURI)
	xmlWantStr(t, "LookupPrefix(xml)", s, ok, "xml", true)
}

func TestXMLLookupNamespaceURI(t *testing.T) {
	node := xmlMustParse(t, "<e xmlns:n1='http://www.w3.org' xmlns='http://www.w4.org'/>").RootElement()
	s, ok := node.LookupNamespaceURI("n1")
	xmlWantStr(t, "LookupNamespaceURI(n1)", s, ok, "http://www.w3.org", true)
	s, ok = node.LookupNamespaceURI("")
	xmlWantStr(t, "LookupNamespaceURI(None)", s, ok, "http://www.w4.org", true)
	s, ok = node.LookupNamespaceURI("n2")
	xmlWantStr(t, "LookupNamespaceURI(n2)", s, ok, "", false)
}

func xmlWantPos(t *testing.T, doc *xmlDocument, what string, pos int, row, col uint32) {
	t.Helper()
	if got := doc.TextPosAt(pos); got != (xmlTextPos{row, col}) {
		t.Errorf("%s: TextPosAt(%d) = %v, want %d:%d", what, pos, got, row, col)
	}
}

func TestXMLTextPos01(t *testing.T) {
	doc := xmlMustParse(t, "<e a='b'>\n    <!-- comment -->\n    <p>Text</p>\n</e>\n")
	node := doc.RootElement()
	attr, _ := node.AttributeNode("a")

	xmlWantPos(t, doc, "root start", doc.Root().Range().Start, 1, 1)
	xmlWantPos(t, doc, "root end", doc.Root().Range().End, 5, 1)
	xmlWantPos(t, doc, "node start", node.Range().Start, 1, 1)
	xmlWantPos(t, doc, "node end", node.Range().End, 4, 5)
	xmlWantPos(t, doc, "attr start", attr.Range().Start, 1, 4)
	xmlWantPos(t, doc, "attr end", attr.Range().End, 1, 9)
	xmlWantPos(t, doc, "qname start", attr.RangeQName().Start, 1, 4)
	xmlWantPos(t, doc, "qname end", attr.RangeQName().End, 1, 5)
	xmlWantPos(t, doc, "value start", attr.RangeValue().Start, 1, 7)
	xmlWantPos(t, doc, "value end", attr.RangeValue().End, 1, 8)

	// The first child is whitespace text, not the comment.
	first, _ := node.FirstChild()
	comm, _ := first.NextSibling()
	xmlWantPos(t, doc, "comment start", comm.Range().Start, 2, 5)

	ws, _ := comm.NextSibling()
	p, _ := ws.NextSibling()
	xmlWantPos(t, doc, "p start", p.Range().Start, 3, 5)

	text, _ := p.FirstChild()
	xmlWantPos(t, doc, "text start", text.Range().Start, 3, 8)
}

func TestXMLTextPos02(t *testing.T) {
	doc := xmlMustParse(t, "<n1:e xmlns:n1='http://www.w3.org' n1:a='b'/>")
	node := doc.RootElement()
	attr, _ := node.AttributeNodeNS("http://www.w3.org", "a")

	xmlWantPos(t, doc, "node start", node.Range().Start, 1, 1)
	xmlWantPos(t, doc, "attr start", attr.Range().Start, 1, 36)
	xmlWantPos(t, doc, "attr end", attr.Range().End, 1, 44)
	xmlWantPos(t, doc, "qname start", attr.RangeQName().Start, 1, 36)
	xmlWantPos(t, doc, "qname end", attr.RangeQName().End, 1, 40)
	xmlWantPos(t, doc, "value start", attr.RangeValue().Start, 1, 42)
	xmlWantPos(t, doc, "value end", attr.RangeValue().End, 1, 43)
}

func TestXMLTextPos03(t *testing.T) {
	doc := xmlMustParse(t, "<!-- comment -->\n<e/>\n")
	node := doc.RootElement()
	xmlWantPos(t, doc, "node start", node.Range().Start, 2, 1)
	xmlWantPos(t, doc, "node end", node.Range().End, 2, 5)
}

func TestXMLTextPos04(t *testing.T) {
	doc := xmlMustParse(t, "<n1:e xmlns:n1='http://www.w3.org' n1:a=''/>")
	attr, _ := doc.RootElement().AttributeNode("a")
	xmlWantPos(t, doc, "attr start", attr.Range().Start, 1, 36)
	xmlWantPos(t, doc, "attr end", attr.Range().End, 1, 43)
	xmlWantPos(t, doc, "qname start", attr.RangeQName().Start, 1, 36)
	xmlWantPos(t, doc, "qname end", attr.RangeQName().End, 1, 40)
	xmlWantPos(t, doc, "value start", attr.RangeValue().Start, 1, 42)
	xmlWantPos(t, doc, "value end", attr.RangeValue().End, 1, 42)
}

func TestXMLTextPos05(t *testing.T) {
	doc := xmlMustParse(t, "<n1:e xmlns:n1='http://www.w3.org' n1:a  =   'b'/>")
	attr, _ := doc.RootElement().AttributeNode("a")
	xmlWantPos(t, doc, "attr start", attr.Range().Start, 1, 36)
	xmlWantPos(t, doc, "attr end", attr.Range().End, 1, 49)
	xmlWantPos(t, doc, "qname start", attr.RangeQName().Start, 1, 36)
	xmlWantPos(t, doc, "qname end", attr.RangeQName().End, 1, 40)
	xmlWantPos(t, doc, "value start", attr.RangeValue().Start, 1, 47)
	xmlWantPos(t, doc, "value end", attr.RangeValue().End, 1, 48)
}

func TestXMLTextPos06(t *testing.T) {
	data := "<e a" + strings.Repeat(" ", 100) + "=" + strings.Repeat(" ", 160) + "'b'/>"
	doc := xmlMustParse(t, data)
	attr, _ := doc.RootElement().AttributeNode("a")
	xmlWantPos(t, doc, "attr start", attr.Range().Start, 1, 4)
	xmlWantPos(t, doc, "attr end", attr.Range().End, 1, 269)
	xmlWantPos(t, doc, "qname start", attr.RangeQName().Start, 1, 4)
	xmlWantPos(t, doc, "qname end", attr.RangeQName().End, 1, 5)
	attr.RangeValue() // unreliable past 254 spaces around `=`, but must not panic
}

func TestXMLNextSiblingElement01(t *testing.T) {
	root := xmlMustParse(t, "<root><a/><b/><c/></root>").RootElement()
	a, _ := root.FirstElementChild()
	b, _ := a.NextSiblingElement()
	c, _ := b.NextSiblingElement()
	if _, ok := c.NextSiblingElement(); ok {
		t.Error("c has a next sibling element")
	}
	for n, want := range map[xmlNode]string{root: "root", a: "a", b: "b", c: "c"} {
		if got := n.TagName().Name(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestXMLNextPrevElement01(t *testing.T) {
	root := xmlMustParse(t, "<root><a/><b/><c/></root>").RootElement()
	c, _ := root.LastElementChild()
	b, _ := c.PrevSiblingElement()
	a, _ := b.PrevSiblingElement()
	if _, ok := a.PrevSiblingElement(); ok {
		t.Error("a has a previous sibling element")
	}
	for n, want := range map[xmlNode]string{root: "root", a: "a", b: "b", c: "c"} {
		if got := n.TagName().Name(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestXMLNodesDocumentOrder(t *testing.T) {
	root := xmlMustParse(t, "<root><a/><b/><c/></root>").RootElement()
	a, _ := root.FirstElementChild()
	b, _ := a.NextSiblingElement()
	c, _ := b.NextSiblingElement()

	elems := []xmlNode{b, c, a}
	sort.Slice(elems, func(i, j int) bool { return elems[i].ID() < elems[j].ID() })
	if elems[0] != a || elems[1] != b || elems[2] != c {
		t.Fatal("wrong document order")
	}
}

func TestXMLTagNameLifetime(t *testing.T) {
	root := xmlMustParse(t, "<e xmlns='http://www.w3.org' />").RootElement()
	if got := root.TagName().Name(); got != "e" {
		t.Fatalf("got %q", got)
	}
	if ns, ok := root.TagName().Namespace(); !ok || ns != "http://www.w3.org" {
		t.Fatalf("namespace (%q, %v)", ns, ok)
	}
}

func TestXMLEntityResolverWorks(t *testing.T) {
	text := `<!DOCTYPE foo [<!ENTITY bar SYSTEM "baz.xml">]> <qux>&bar;</qux>`
	entity := `<?xml version="1.0"?><foobar/>`

	opt := xmlDefaultParsingOptions()
	opt.AllowDTD = true
	opt.EntityResolver = func(string, bool, string) (string, bool, error) { return entity, true, nil }

	doc, err := xmlParseWithOptions(text, opt)
	if err != nil {
		t.Fatal(err)
	}
	child := doc.RootElement().Children()[0]
	if !child.HasTagName("foobar") {
		t.Fatalf("got %v", child.TagName())
	}
}

func TestXMLEntityResolverError(t *testing.T) {
	opt := xmlDefaultParsingOptions()
	opt.AllowDTD = true
	opt.EntityResolver = func(string, bool, string) (string, bool, error) { return "", false, errors.New("nope") }

	_, err := xmlParseWithOptions(`<!DOCTYPE foo [<!ENTITY bar SYSTEM "baz.xml">]><qux/>`, opt)
	var xe *xmlError
	if !errors.As(err, &xe) || xe.KindName() != "EntityResolver" || err.Error() != "entity resolver failed at 1:45: nope" {
		t.Fatalf("got %v", err)
	}
}

// Errors, kind names and the options usvg passes.
func TestXMLErrors(t *testing.T) {
	dtd := xmlDefaultParsingOptions()
	dtd.AllowDTD = true
	limit := xmlDefaultParsingOptions()
	limit.NodesLimit = 3

	cases := []struct {
		text string
		opt  xmlParsingOptions
		kind string
		msg  string
	}{
		{"", xmlDefaultParsingOptions(), "NoRootNode", "the document does not have a root node"},
		{"<!-- c -->", xmlDefaultParsingOptions(), "NoRootNode", "the document does not have a root node"},
		{"<a>", xmlDefaultParsingOptions(), "UnclosedRootNode", "the root node was opened but never closed"},
		{"<!DOCTYPE a><a/>", xmlDefaultParsingOptions(), "DtdDetected", "XML with DTD detected"},
		{"<a><b/><c/></a>", limit, "NodesLimitReached", "nodes limit reached"},
		{"<a>&b;</a>", dtd, "UnknownEntityReference", "unknown entity reference 'b' at 1:4"},
		{"<a x='&#0;'/>", dtd, "MalformedEntityReference", "malformed entity reference at 1:7"},
		{"<a>\x01</a>", dtd, "NonXmlChar", `a non-XML character '\u{1}' found at 1:4`},
		{"<a>￾</a>", dtd, "NonXmlChar", `a non-XML character '\u{fffe}' found at 1:4`},
		{"<a>\x00</a>", dtd, "NonXmlChar", `a non-XML character '\0' found at 1:4`},
		{"<a x='1' x='2'/>", dtd, "DuplicatedAttribute", "attribute 'x' at 1:10 is already defined"},
		{"<a></b>", dtd, "UnexpectedCloseTag", "expected 'a' tag, not 'b' at 1:4"},
		{"<a b:c='1'/>", dtd, "UnknownNamespace", "an unknown namespace prefix 'b' at 1:4"},
		{"<aé/>", dtd, "", ""},
		{"<a x='1'é='2'/>", dtd, "InvalidChar2", "expected a whitespace not 'Ã' at 1:9"},
	}
	for _, tc := range cases {
		_, err := xmlParseWithOptions(tc.text, tc.opt)
		if tc.kind == "" {
			if err != nil {
				t.Errorf("%q: unexpected error %v", tc.text, err)
			}
			continue
		}
		var xe *xmlError
		if !errors.As(err, &xe) {
			t.Errorf("%q: got %v, want %s", tc.text, err, tc.kind)
			continue
		}
		if xe.KindName() != tc.kind || xe.Error() != tc.msg {
			t.Errorf("%q: got %s %q, want %s %q", tc.text, xe.KindName(), xe.Error(), tc.kind, tc.msg)
		}
	}

	if e := xmlNoRootNodeError(); e.KindName() != "NoRootNode" || e.Position() != (xmlTextPos{1, 1}) {
		t.Errorf("xmlNoRootNodeError: %s %v", e.KindName(), e.Position())
	}
	if len(xmlErrorKindNames) != int(xmlErrUnexpectedEndOfStream)+1 {
		t.Errorf("%d kind names", len(xmlErrorKindNames))
	}
	if xmlDefaultParsingOptions().NodesLimit != math.MaxUint32 {
		t.Error("default nodes limit")
	}
}

// The billion laughs attack hits the reference limit.
func TestXMLBillionLaughs(t *testing.T) {
	var b strings.Builder
	b.WriteString("<!DOCTYPE lolz [\n<!ENTITY lol \"lol\">\n")
	prev := "lol"
	for i := 1; i <= 9; i++ {
		name := fmt.Sprintf("lol%d", i)
		fmt.Fprintf(&b, "<!ENTITY %s \"%s\">\n", name, strings.Repeat("&"+prev+";", 10))
		prev = name
	}
	b.WriteString("]>\n<lolz>&lol9;</lolz>")
	opt := xmlDefaultParsingOptions()
	opt.AllowDTD = true
	_, err := xmlParseWithOptions(b.String(), opt)
	var xe *xmlError
	if !errors.As(err, &xe) || xe.KindName() != "EntityReferenceLoop" {
		t.Fatalf("got %v", err)
	}
}

// dom-api.rs

const xmlDomData = "<!-- comment -->\n<svg>\n    <rect/>\n    <text>Text</text>\n    <g>\n        <!-- comment -->\n        <rect/>\n    </g>\n</svg>\n"

func TestXMLGetElementsByTagName(t *testing.T) {
	n := 0
	for _, d := range xmlMustParse(t, xmlDomData).Descendants() {
		if d.HasTagName("rect") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("got %d", n)
	}
}

func TestXMLGetElementsByTagNameNS(t *testing.T) {
	data := "<!-- comment -->\n<svg xmlns:q='http://www.w3.org/'>\n    <rect/>\n    <text>Text</text>\n    <g>\n" +
		"        <!-- comment -->\n        <q:rect/>\n    </g>\n</svg>\n"
	n := 0
	for _, d := range xmlMustParse(t, data).Descendants() {
		if d.HasTagNameNS("http://www.w3.org/", "rect") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("got %d", n)
	}
}

func TestXMLChildren(t *testing.T) {
	data := "<svg>\n    <rect/>\n    <!-- comment -->\n    <rect/>\n    <!-- comment -->\n    <rect/>\n</svg>\n"
	n := 0
	for _, c := range xmlMustParse(t, data).RootElement().Children() {
		if c.IsElement() {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("got %d", n)
	}
}

func TestXMLFirstLastElementChild(t *testing.T) {
	svg := xmlMustParse(t, "<svg>\n    <!-- comment -->\n    <rect/>\n    <!-- comment -->\n</svg>\n").RootElement()
	if e, ok := svg.FirstElementChild(); !ok || !e.HasTagName("rect") {
		t.Error("FirstElementChild")
	}
	if e, ok := svg.LastElementChild(); !ok || !e.HasTagName("rect") {
		t.Error("LastElementChild")
	}
}

func TestXMLGetElementByID(t *testing.T) {
	doc := xmlMustParse(t, "<svg id='svg1'>\n    <circle id='circle1'/>\n    <g>\n        <rect id='rect1'/>\n    </g>\n</svg>\n")
	for _, d := range doc.Descendants() {
		if v, ok := d.Attribute("id"); ok && v == "rect1" {
			if !d.HasTagName("rect") {
				t.Fatal("wrong element")
			}
			return
		}
	}
	t.Fatal("not found")
}

func TestXMLParentElementAndContains(t *testing.T) {
	doc := xmlMustParse(t, "<svg>\n    <!-- comment -->\n    <rect/>\n</svg>\n")
	var rect xmlNode
	for _, d := range doc.Descendants() {
		if d.HasTagName("rect") {
			rect = d
		}
	}
	if p, ok := rect.ParentElement(); !ok || !p.HasTagName("svg") {
		t.Error("ParentElement")
	}
	if a := rect.Ancestors(); len(a) != 3 || a[0] != rect || !a[1].HasTagName("svg") || !a[2].IsRoot() {
		t.Error("Ancestors")
	}
	found := false
	for _, d := range doc.RootElement().Descendants() {
		found = found || d == rect
	}
	if !found {
		t.Error("svg descendants miss rect")
	}
	if got, ok := doc.GetNode(rect.ID()); !ok || got != rect {
		t.Error("GetNode")
	}
	if _, ok := doc.GetNode(xmlNodeID(len(doc.Descendants()))); ok {
		t.Error("GetNode past the end")
	}
}

// ast.rs

func xmlToYAML(doc *xmlDocument) string {
	var b strings.Builder
	if !doc.Root().HasChildren() {
		return "Document:"
	}

	line := func(depth int, format string, args ...any) {
		b.WriteString(strings.Repeat("  ", depth))
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}

	var printChildren func(parent xmlNode, depth int)
	printChildren = func(parent xmlNode, depth int) {
		for _, child := range parent.Children() {
			switch child.NodeType() {
			case xmlNodeTypeElement:
				line(depth, "- Element:")

				tag := child.TagName()
				if ns, ok := tag.Namespace(); ok && ns != "" {
					line(depth+2, "tag_name: %s@%s", tag.Name(), ns)
				} else {
					line(depth+2, "tag_name: %s", tag.Name())
				}

				if attrs := child.Attributes(); len(attrs) != 0 {
					type kv struct{ name, value string }
					var list []kv
					for _, a := range attrs {
						name := a.Name()
						if ns, ok := a.Namespace(); ok {
							name = name + "@" + ns
						}
						list = append(list, kv{name, a.Value()})
					}
					sort.SliceStable(list, func(i, j int) bool { return list[i].name < list[j].name })

					line(depth+2, "attributes:")
					for _, a := range list {
						line(depth+3, "%s: %s", a.name, xmlRustDebugStr(a.value))
					}
				}

				if nss := child.Namespaces(); len(nss) != 0 {
					type kv struct{ name, uri string }
					var list []kv
					for _, ns := range nss {
						name, ok := ns.Name()
						if !ok {
							name = "None"
						}
						uri := ns.URI()
						if uri == "" {
							uri = `""`
						}
						list = append(list, kv{name, uri})
					}
					sort.SliceStable(list, func(i, j int) bool { return list[i].name < list[j].name })

					line(depth+2, "namespaces:")
					for _, ns := range list {
						line(depth+3, "%s: %s", ns.name, ns.uri)
					}
				}

				if child.HasChildren() {
					line(depth+2, "children:")
					printChildren(child, depth+3)
				}
			case xmlNodeTypeText:
				s, _ := child.Text()
				line(depth, "- Text: %s", xmlRustDebugStr(s))
			case xmlNodeTypeComment:
				s, _ := child.Text()
				line(depth, "- Comment: %s", xmlRustDebugStr(s))
			case xmlNodeTypePI:
				if p, _ := child.Parent(); p.IsRoot() {
					continue
				}
				line(depth, "- PI:")
				pi, _ := child.PI()
				line(depth+2, "target: %s", xmlRustDebugStr(pi.Target))
				if pi.HasValue {
					line(depth+2, "value: %s", xmlRustDebugStr(pi.Value))
				}
			}
		}
	}

	b.WriteString("Document:\n")
	printChildren(doc.Root(), 1)
	return b.String()
}

var xmlASTCases = []string{
	"attrs_001", "attrs_002", "attrs_003", "attrs_004", "attrs_005", "attrs_006", "attrs_err_001", "attrs_err_002",
	"cdata_001", "cdata_002", "cdata_003", "cdata_004", "cdata_005", "cdata_006", "cdata_007", "cdata_008", "cdata_009",
	"comments_001", "elems_err_001", "elems_err_002",
	"entity_001", "entity_002", "entity_003", "entity_004", "entity_005", "entity_006", "entity_007", "entity_008",
	"entity_009", "entity_010", "entity_011", "entity_012", "entity_013", "entity_014",
	"entity_err_001", "entity_err_002", "entity_err_003", "entity_err_004", "entity_err_005", "entity_err_006",
	"entity_err_007", "entity_err_008", "entity_err_009", "entity_err_010",
	"ns_001", "ns_002", "ns_003", "ns_004", "ns_005", "ns_006", "ns_007", "ns_008", "ns_009", "ns_010", "ns_011",
	"ns_012", "ns_013", "ns_014", "ns_015", "ns_016", "ns_017",
	"ns_err_001", "ns_err_002", "ns_err_003", "ns_err_004", "ns_err_005", "ns_err_006", "ns_err_007", "ns_err_008",
	"ns_err_009", "ns_err_010", "ns_err_011", "ns_err_012", "ns_err_013",
	"text_001", "text_002", "text_003", "text_004", "text_005", "text_006", "text_007", "text_008", "text_009",
	"text_010", "text_011", "text_012",
	"tree_001", "tree_002", // tree_003 is unsupported upstream (Windows-1251)
	"tree_err_001", "tree_err_002", "tree_err_003",
}

func TestXMLAST(t *testing.T) {
	for _, name := range xmlASTCases {
		t.Run(name, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join(xmlTestdata, name+".xml"))
			if err != nil {
				t.Fatal(err)
			}
			expected, err := os.ReadFile(filepath.Join(xmlTestdata, name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}

			opt := xmlDefaultParsingOptions()
			opt.AllowDTD = true
			doc, perr := xmlParseWithOptions(string(input), opt)
			if perr != nil {
				got := fmt.Sprintf("error: \"%s\"", perr)
				if want := strings.TrimSpace(string(expected)); got != want {
					t.Fatalf("got:\n%s\nwant:\n%s", got, want)
				}
				return
			}
			if got := xmlToYAML(doc); got != string(expected) {
				t.Fatalf("got:\n%s\nwant:\n%s", got, expected)
			}
		})
	}
}
