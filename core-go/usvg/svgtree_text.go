// Port of usvg 0.47 src/parser/svgtree/text.rs: collects the text content of `text`, `tspan`, `tref`
// and `textPath` into svgtree text nodes, with xml:space handling and whitespace trimming.

package usvg

import (
	"strings"
	"unicode/utf8"
)

// svgXMLSpace is text.rs's XmlSpace.
type svgXMLSpace uint8

const (
	svgXMLSpaceDefault svgXMLSpace = iota
	svgXMLSpacePreserve
)

// parseSVGTextElement is parse_svg_text_element: parent is the `text` element, already appended as
// parentID.
func (p *svgParser) parseSVGTextElement(parent xmlNode, parentID svgNodeID) *xmlError {
	doc := p.doc
	space := svgXMLSpaceDefault
	if doc.get(parentID).HasAttribute(aidSpace) {
		space = svgGetXMLSpace(doc, parentID, svgXMLSpaceDefault)
	} else {
		for n := range doc.get(parentID).Ancestors() {
			if n.HasAttribute(aidSpace) {
				space = svgGetXMLSpace(doc, n.id, svgXMLSpaceDefault)
				break
			}
		}
	}

	if err := p.parseSVGTextElementImpl(parent, parentID, space); err != nil {
		return err
	}
	svgTrimTextNodes(parentID, space, doc)
	return nil
}

func (p *svgParser) parseSVGTextElementImpl(parent xmlNode, parentID svgNodeID,
	space svgXMLSpace) *xmlError {
	doc := p.doc
	for _, node := range parent.Children() {
		if node.IsText() {
			text, _ := node.Text()
			doc.appendText(parentID, svgTrimText(text, space))
			continue
		}

		tagName, ok := svgParseTagName(node)
		if !ok {
			continue
		}

		if tagName == eidA {
			// Treat links as simple text.
			tagName = eidTspan
		}

		if tagName != eidTspan && tagName != eidTref && tagName != eidTextPath {
			continue
		}

		// `textPath` must be a direct `text` child.
		if tagName == eidTextPath && parent.TagName().Name() != "text" {
			continue
		}

		// We are converting `tref` into `tspan` to simplify later use.
		isTref := false
		if tagName == eidTref {
			tagName = eidTspan
			isTref = true
		}

		nodeID, err := p.parseSVGElement(node, parentID, tagName, false)
		if err != nil {
			return err
		}
		space := svgGetXMLSpace(doc, nodeID, space)

		if isTref {
			href, ok := node.AttributeNS(svgNamespaceXLink, "href")
			if !ok {
				href, ok = node.Attribute("href")
			}
			if ok {
				if text, ok := svgResolveTrefText(node.Document(), href); ok {
					doc.appendText(nodeID, svgTrimText(text, space))
				}
			}
		} else if err := p.parseSVGTextElementImpl(node, nodeID, space); err != nil {
			return err
		}
	}
	return nil
}

// svgResolveTrefText is resolve_tref_text: all character data within the linked element, which must
// be an SVG element; ok is false when there is none.
func svgResolveTrefText(xml *xmlDocument, href string) (string, bool) {
	id, err := stParseIRI(href)
	if err != nil {
		return "", false
	}

	// Find the linked element in the original tree.
	var node xmlNode
	found := false
	for _, n := range xml.Descendants() {
		if v, ok := n.Attribute("id"); ok && v == string(id) {
			node, found = n, true
			break
		}
	}
	if !found {
		return "", false
	}

	// `tref` should be linked to an SVG element.
	if _, ok := svgParseTagName(node); !ok {
		return "", false
	}

	// Only text nodes are collected: an element's text() would repeat its first text child.
	var b strings.Builder
	for _, n := range node.Descendants() {
		if n.IsText() {
			t, _ := n.Text()
			b.WriteString(t)
		}
	}
	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

func svgGetXMLSpace(doc *svgDocument, id svgNodeID, def svgXMLSpace) svgXMLSpace {
	v, ok := doc.get(id).attrStr(aidSpace)
	if !ok {
		return def
	}
	if v == "preserve" {
		return svgXMLSpacePreserve
	}
	return svgXMLSpaceDefault
}

// svgRemoveFirstSpace is String::remove_first_space (drain(0..1)); callers check the first byte is a
// space. Rust would panic on an empty string, which callers also rule out.
func svgRemoveFirstSpace(doc *svgDocument, id svgNodeID) {
	d := &doc.nodes[id]
	if d.text != "" {
		d.text = d.text[1:]
	}
}

// svgRemoveLastSpace is String::remove_last_space (pop): removes the last char, if any.
func svgRemoveLastSpace(doc *svgDocument, id svgNodeID) {
	d := &doc.nodes[id]
	_, size := utf8.DecodeLastRuneInString(d.text)
	d.text = d.text[:len(d.text)-size]
}

// svgTextNodeRef is a collected text node and its depth below the `text` element.
type svgTextNodeRef struct {
	id    svgNodeID
	depth int
}

// svgByteAt is the first (first true) or last byte of s, as Rust's bytes.first()/last(); ok is false
// for an empty string.
func svgByteAt(s string, first bool) (byte, bool) {
	if s == "" {
		return 0, false
	}
	if first {
		return s[0], true
	}
	return s[len(s)-1], true
}

// svgIsSpaceByte is `c == Some(b' ')`.
func svgIsSpaceByte(c byte, ok bool) bool { return ok && c == ' ' }

// svgTrimTextNodes is trim_text_nodes: prepares the text nodes according to
// https://www.w3.org/TR/SVG11/text.html#WhiteSpace (xml:space, then leading and trailing spaces,
// across nodes as if they were one text). svgTrimText already collapsed runs of spaces, so only one
// leading or trailing space needs checking.
func svgTrimTextNodes(textElemID svgNodeID, xmlspace svgXMLSpace, doc *svgDocument) {
	var nodes []svgTextNodeRef
	svgCollectTextNodes(doc.get(textElemID), 0, &nodes)

	if len(nodes) == 1 {
		// An element with a single text node child; xml:space=preserve keeps it as is.
		if xmlspace != svgXMLSpaceDefault {
			return
		}
		id := nodes[0].id
		text := doc.nodes[id].text
		switch len(text) {
		case 0: // An empty string. Do nothing.
		case 1:
			// A single space is cleared.
			if text[0] == ' ' {
				doc.nodes[id].text = ""
			}
		default:
			c1, c2 := text[0], text[len(text)-1]
			if c1 == ' ' {
				svgRemoveFirstSpace(doc, id)
			}
			if c2 == ' ' {
				svgRemoveLastSpace(doc, id)
			}
		}
		return
	}
	if len(nodes) < 2 {
		return
	}

	// All text nodes are managed as a single text, removing duplicated spaces across nodes:
	// '<text>Text <tspan> text </tspan> text</text>' is '<text>Text <tspan>text</tspan> text</text>'.
	n := len(nodes) - 1
	lastNonEmpty, hasLastNonEmpty := svgNodeID(0), false
	for i := 0; i < n; i++ {
		// Process pairs.
		node1ID, depth1 := nodes[i].id, nodes[i].depth
		node2ID, depth2 := nodes[i+1].id, nodes[i+1].depth

		if doc.get(node1ID).Text() == "" && hasLastNonEmpty {
			node1ID = lastNonEmpty
		}

		// The parent of a text node is always an element.
		p1, _ := doc.get(node1ID).Parent()
		p2, _ := doc.get(node2ID).Parent()
		xmlspace1 := svgGetXMLSpace(doc, p1.id, xmlspace)
		xmlspace2 := svgGetXMLSpace(doc, p2.id, xmlspace)

		// >text<..>text<
		//  1  2    3  4
		text1, text2 := doc.get(node1ID).Text(), doc.get(node2ID).Text()
		c1, ok1 := svgByteAt(text1, true)
		c2, ok2 := svgByteAt(text1, false)
		c3, ok3 := svgByteAt(text2, true)
		c4, ok4 := svgByteAt(text2, false)

		// xml:space processing is mostly undefined behaviour; this mimics Chrome.

		// Remove the space from the second text node if both nodes have bound spaces:
		// '<text>Text <tspan> text</tspan></text>' becomes '<text>Text <tspan>text</tspan></text>'.
		if depth1 < depth2 {
			if svgIsSpaceByte(c3, ok3) && xmlspace2 == svgXMLSpaceDefault {
				svgRemoveFirstSpace(doc, node2ID)
			}
		} else if svgIsSpaceByte(c2, ok2) && ok3 && c2 == c3 {
			if xmlspace1 == svgXMLSpaceDefault && xmlspace2 == svgXMLSpaceDefault {
				svgRemoveLastSpace(doc, node1ID)
			} else if xmlspace1 == svgXMLSpacePreserve && xmlspace2 == svgXMLSpaceDefault {
				svgRemoveFirstSpace(doc, node2ID)
			}
		}

		isFirst := i == 0
		isLast := i == n-1

		if isFirst && svgIsSpaceByte(c1, ok1) && xmlspace1 == svgXMLSpaceDefault &&
			doc.get(node1ID).Text() != "" {
			// Remove a leading space from the first text node.
			svgRemoveFirstSpace(doc, node1ID)
		} else if isLast && svgIsSpaceByte(c4, ok4) && doc.get(node2ID).Text() != "" &&
			xmlspace2 == svgXMLSpaceDefault {
			// Remove a trailing space from the last text node.
			svgRemoveLastSpace(doc, node2ID)
		}

		if isLast && svgIsSpaceByte(c2, ok2) && doc.get(node1ID).Text() != "" &&
			doc.get(node2ID).Text() == "" && strings.HasSuffix(doc.get(node1ID).Text(), " ") {
			svgRemoveLastSpace(doc, node1ID)
		}

		if strings.TrimSpace(doc.get(node1ID).Text()) != "" {
			lastNonEmpty, hasLastNonEmpty = node1ID, true
		}
	}
	// TODO in Rust: find a way to remove all empty text nodes.
}

func svgCollectTextNodes(parent svgNode, depth int, nodes *[]svgTextNodeRef) {
	for child := range parent.Children() {
		if child.IsText() {
			*nodes = append(*nodes, svgTextNodeRef{child.id, depth})
		} else if child.IsElement() {
			svgCollectTextNodes(child, depth+1, nodes)
		}
	}
}

// svgTrimText is trim_text: \r, \n and \t become spaces, and with xml:space=default runs of spaces
// collapse into one.
func svgTrimText(text string, space svgXMLSpace) string {
	var b strings.Builder
	b.Grow(len(text))
	prev := '0'
	for _, c := range text {
		switch c {
		case '\r', '\n', '\t':
			c = ' '
		}
		// Skip continuous spaces.
		if space == svgXMLSpaceDefault && c == ' ' && c == prev {
			continue
		}
		prev = c
		b.WriteRune(c)
	}
	return b.String()
}
