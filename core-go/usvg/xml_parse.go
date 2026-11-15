package usvg

// XML document builder and errors, ported from roxmltree 0.21.1 src/parse.rs.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// xmlErrorKind lists roxmltree's Error variants, in the same order.
type xmlErrorKind uint8

const (
	// The `xmlns:xml` attribute must have an <http://www.w3.org/XML/1998/namespace> URI.
	xmlErrInvalidXmlPrefixUri xmlErrorKind = iota
	// Only the `xmlns:xml` attribute can have the <http://www.w3.org/XML/1998/namespace> URI.
	xmlErrUnexpectedXmlUri
	// The <http://www.w3.org/2000/xmlns/> URI must not be declared.
	xmlErrUnexpectedXmlnsUri
	// `xmlns` can't be used as an element prefix.
	xmlErrInvalidElementNamePrefix
	// A namespace was already defined on this element (Name).
	xmlErrDuplicatedNamespace
	// An element or an attribute has an unknown qualified name prefix (Name).
	xmlErrUnknownNamespace
	// Incorrect tree structure: expected Name, actual Name2.
	xmlErrUnexpectedCloseTag
	// Entity value starts with a close tag.
	xmlErrUnexpectedEntityCloseTag
	// A reference to an entity that was not defined in the DTD (Name).
	xmlErrUnknownEntityReference
	// A malformed entity reference.
	xmlErrMalformedEntityReference
	// A possible entity reference loop: depth over 10 or over 255 references per reference.
	xmlErrEntityReferenceLoop
	// Attribute value cannot have a `<` character.
	xmlErrInvalidAttributeValue
	// An element has a duplicated attribute (Name), namespaces resolved.
	xmlErrDuplicatedAttribute
	// The XML document must have at least one element.
	xmlErrNoRootNode
	// The root node was opened but never closed.
	xmlErrUnclosedRootNode
	// An XML declaration not at the start of the document.
	xmlErrUnexpectedDeclaration
	// An XML with DTD while ParsingOptions.AllowDTD is false.
	xmlErrDtdDetected
	// ParsingOptions.NodesLimit was reached.
	xmlErrNodesLimitReached
	// Too many attributes were parsed.
	xmlErrAttributesLimitReached
	// Too many namespaces were parsed.
	xmlErrNamespacesLimitReached
	// An invalid name.
	xmlErrInvalidName
	// A non-XML character (Char).
	xmlErrNonXmlChar
	// An invalid character: expected Expected, actual Actual.
	xmlErrInvalidChar
	// An invalid character: expected ExpectedStr, actual Actual.
	xmlErrInvalidChar2
	// An unexpected string; ExpectedStr was expected.
	xmlErrInvalidString
	// An invalid ExternalID in the DTD.
	xmlErrInvalidExternalID
	// The entity resolver failed (Msg).
	xmlErrEntityResolver
	// A comment cannot contain `--` or end with `-`.
	xmlErrInvalidComment
	// A Character Data node contains `]]>`.
	xmlErrInvalidCharacterData
	// An unknown token.
	xmlErrUnknownToken
	// The stream ended earlier than expected.
	xmlErrUnexpectedEndOfStream
)

var xmlErrorKindNames = [...]string{
	"InvalidXmlPrefixUri", "UnexpectedXmlUri", "UnexpectedXmlnsUri", "InvalidElementNamePrefix",
	"DuplicatedNamespace", "UnknownNamespace", "UnexpectedCloseTag", "UnexpectedEntityCloseTag",
	"UnknownEntityReference", "MalformedEntityReference", "EntityReferenceLoop", "InvalidAttributeValue",
	"DuplicatedAttribute", "NoRootNode", "UnclosedRootNode", "UnexpectedDeclaration", "DtdDetected",
	"NodesLimitReached", "AttributesLimitReached", "NamespacesLimitReached", "InvalidName", "NonXmlChar",
	"InvalidChar", "InvalidChar2", "InvalidString", "InvalidExternalID", "EntityResolver", "InvalidComment",
	"InvalidCharacterData", "UnknownToken", "UnexpectedEndOfStream",
}

// xmlError is roxmltree's Error: Kind plus the payload of that variant.
type xmlError struct {
	Kind xmlErrorKind
	// Pos is the variant's TextPos; unset for variants without one (see Position).
	Pos xmlTextPos
	// Name is the name of DuplicatedNamespace, UnknownNamespace, UnknownEntityReference and
	// DuplicatedAttribute, and the expected name of UnexpectedCloseTag; Name2 is its actual name.
	Name, Name2 string
	// Char is NonXmlChar's character.
	Char rune
	// Expected is InvalidChar's expected byte; ExpectedStr is InvalidChar2's and InvalidString's.
	Expected    byte
	ExpectedStr string
	// Actual is InvalidChar's and InvalidChar2's actual byte.
	Actual byte
	// Msg is EntityResolver's message.
	Msg string
}

// xmlNoRootNodeError returns Error::NoRootNode, which usvg's svgtree also raises.
func xmlNoRootNodeError() *xmlError {
	return &xmlError{Kind: xmlErrNoRootNode}
}

// KindName returns the Rust variant name, as Debug prints it before any parenthesis.
func (e *xmlError) KindName() string {
	return xmlErrorKindNames[e.Kind]
}

// Position returns the error position (Error::pos); 1:1 for variants without one.
func (e *xmlError) Position() xmlTextPos {
	switch e.Kind {
	case xmlErrNoRootNode, xmlErrUnclosedRootNode, xmlErrDtdDetected, xmlErrNodesLimitReached,
		xmlErrAttributesLimitReached, xmlErrNamespacesLimitReached, xmlErrUnexpectedEndOfStream:
		return xmlTextPos{1, 1}
	}
	return e.Pos
}

// Error returns roxmltree's Display text.
func (e *xmlError) Error() string {
	p := e.Pos
	switch e.Kind {
	case xmlErrInvalidXmlPrefixUri:
		return fmt.Sprintf("'xml' namespace prefix mapped to wrong URI at %v", p)
	case xmlErrUnexpectedXmlUri:
		return fmt.Sprintf("the 'xml' namespace URI is used for not 'xml' prefix at %v", p)
	case xmlErrUnexpectedXmlnsUri:
		return fmt.Sprintf("the 'xmlns' URI is used at %v, but it must not be declared", p)
	case xmlErrInvalidElementNamePrefix:
		return fmt.Sprintf("the 'xmlns' prefix is used at %v, but it must not be", p)
	case xmlErrDuplicatedNamespace:
		return fmt.Sprintf("namespace '%s' at %v is already defined", e.Name, p)
	case xmlErrUnknownNamespace:
		return fmt.Sprintf("an unknown namespace prefix '%s' at %v", e.Name, p)
	case xmlErrUnexpectedCloseTag:
		return fmt.Sprintf("expected '%s' tag, not '%s' at %v", e.Name, e.Name2, p)
	case xmlErrUnexpectedEntityCloseTag:
		return fmt.Sprintf("unexpected close tag at %v", p)
	case xmlErrMalformedEntityReference:
		return fmt.Sprintf("malformed entity reference at %v", p)
	case xmlErrUnknownEntityReference:
		return fmt.Sprintf("unknown entity reference '%s' at %v", e.Name, p)
	case xmlErrEntityReferenceLoop:
		return fmt.Sprintf("a possible entity reference loop is detected at %v", p)
	case xmlErrInvalidAttributeValue:
		return fmt.Sprintf("unescaped '<' found at %v", p)
	case xmlErrDuplicatedAttribute:
		return fmt.Sprintf("attribute '%s' at %v is already defined", e.Name, p)
	case xmlErrNoRootNode:
		return "the document does not have a root node"
	case xmlErrUnclosedRootNode:
		return "the root node was opened but never closed"
	case xmlErrUnexpectedDeclaration:
		return fmt.Sprintf("unexpected XML declaration at %v", p)
	case xmlErrDtdDetected:
		return "XML with DTD detected"
	case xmlErrNodesLimitReached:
		return "nodes limit reached"
	case xmlErrAttributesLimitReached:
		return "more than 2^32 attributes were parsed"
	case xmlErrNamespacesLimitReached:
		return "more than 2^16 unique namespaces were parsed"
	case xmlErrInvalidName:
		return fmt.Sprintf("invalid name token at %v", p)
	case xmlErrNonXmlChar:
		return fmt.Sprintf("a non-XML character %s found at %v", xmlRustDebugChar(e.Char), p)
	case xmlErrInvalidChar:
		// Rust's `u8 as char` maps a byte to the code point of the same value.
		return fmt.Sprintf("expected '%c' not '%c' at %v", rune(e.Expected), rune(e.Actual), p)
	case xmlErrInvalidChar2:
		return fmt.Sprintf("expected %s not '%c' at %v", e.ExpectedStr, rune(e.Actual), p)
	case xmlErrInvalidString:
		return fmt.Sprintf("expected '%s' at %v", e.ExpectedStr, p)
	case xmlErrInvalidExternalID:
		return fmt.Sprintf("invalid ExternalID at %v", p)
	case xmlErrEntityResolver:
		return fmt.Sprintf("entity resolver failed at %v: %s", p, e.Msg)
	case xmlErrInvalidComment:
		return fmt.Sprintf("comment at %v contains '--'", p)
	case xmlErrInvalidCharacterData:
		return fmt.Sprintf("']]>' at %v is not allowed inside a character data", p)
	case xmlErrUnknownToken:
		return fmt.Sprintf("unknown token at %v", p)
	case xmlErrUnexpectedEndOfStream:
		return "unexpected end of stream"
	}
	return "unknown XML error"
}

// xmlRustEscape appends c as Rust's char::escape_debug writes it. quote is the delimiter that gets a
// backslash (a single quote for a char, a double quote for a str); graphemeExt asks to escape grapheme extenders.
// Printability follows unicode.IsGraphic, close to Rust's table for the characters roxmltree reports.
func xmlRustEscape(b *strings.Builder, c rune, quote rune, graphemeExt bool) {
	switch {
	case c == 0:
		b.WriteString(`\0`)
	case c == '\t':
		b.WriteString(`\t`)
	case c == '\r':
		b.WriteString(`\r`)
	case c == '\n':
		b.WriteString(`\n`)
	case c == '\\':
		b.WriteString(`\\`)
	case c == quote:
		b.WriteByte('\\')
		b.WriteRune(c)
	case (graphemeExt && unicode.In(c, unicode.Mn, unicode.Me)) || !unicode.IsGraphic(c):
		b.WriteString(`\u{`)
		b.WriteString(strconv.FormatInt(int64(c), 16))
		b.WriteByte('}')
	default:
		b.WriteRune(c)
	}
}

// xmlRustDebugChar formats c like Rust's `{:?}` on a char.
func xmlRustDebugChar(c rune) string {
	var b strings.Builder
	b.WriteByte('\'')
	xmlRustEscape(&b, c, '\'', true)
	b.WriteByte('\'')
	return b.String()
}

// xmlRustDebugStr formats s like Rust's `{:?}` on a str.
func xmlRustDebugStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i, c := range s {
		xmlRustEscape(&b, c, '"', i == 0)
	}
	b.WriteByte('"')
	return b.String()
}

// xmlEntityResolver resolves an external entity from its optional public ID and its URI. It yields
// the content (possibly preceded by a text declaration), ok false to discard the entity, or an
// error that becomes xmlErrEntityResolver.
type xmlEntityResolver func(pubID string, hasPubID bool, uri string) (value string, ok bool, err error)

// xmlParsingOptions is roxmltree's ParsingOptions; build it with xmlDefaultParsingOptions.
type xmlParsingOptions struct {
	// AllowDTD allows DTD parsing; when false any DOCTYPE is an error. Default: false.
	AllowDTD bool
	// NodesLimit is the maximum number of nodes to parse. Default: math.MaxUint32.
	NodesLimit uint32
	// EntityResolver resolves external entities. Default: nil.
	EntityResolver xmlEntityResolver
}

func xmlDefaultParsingOptions() xmlParsingOptions {
	return xmlParsingOptions{AllowDTD: false, NodesLimit: math.MaxUint32}
}

// xmlParse parses text with the default options. text must be valid UTF-8 (Rust's &str is).
func xmlParse(text string) (*xmlDocument, error) {
	return xmlParseWithOptions(text, xmlDefaultParsingOptions())
}

// xmlParseWithOptions parses text with opt. text must be valid UTF-8 (Rust's &str is).
func xmlParseWithOptions(text string, opt xmlParsingOptions) (*xmlDocument, error) {
	doc, err := xmlParseDoc(text, opt)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

type xmlTempAttributeData struct {
	prefix, local string
	value         string
	rng           xmlRange
	qnameLen      uint16
	eqLen         uint8
}

type xmlEntity struct {
	name  string
	value xmlStrSpan
}

type xmlTagNameSpan struct {
	prefix, name   string
	pos, prefixPos int
}

// xmlLoopDetector is an entity loop detector: the entity depth is limited to 10, and a reference
// may resolve at most 255 references. The first catches `<!ENTITY a '&b;'> <!ENTITY b '&a;'>`,
// the second the billion laughs attack.
type xmlLoopDetector struct {
	// References depth.
	depth uint8
	// Number of references resolved by the root reference.
	references uint8
}

func (l *xmlLoopDetector) incDepth(s *xmlStream) *xmlError {
	if l.depth < 10 {
		l.depth++
		return nil
	}
	return &xmlError{Kind: xmlErrEntityReferenceLoop, Pos: s.genTextPos()}
}

func (l *xmlLoopDetector) decDepth() {
	if l.depth > 0 {
		l.depth--
	}
	// Reset references count after reaching zero depth.
	if l.depth == 0 {
		l.references = 0
	}
}

func (l *xmlLoopDetector) incReferences(s *xmlStream) *xmlError {
	if l.depth == 0 {
		// Allow infinite amount of references at zero depth.
		return nil
	}
	if l.references == math.MaxUint8 {
		return &xmlError{Kind: xmlErrEntityReferenceLoop, Pos: s.genTextPos()}
	}
	l.references++
	return nil
}

type xmlContext struct {
	opt               xmlParsingOptions
	namespaceStartIdx int
	currentAttributes []xmlTempAttributeData
	awaitingSubtree   []xmlNodeID
	parentPrefixes    []string
	entities          []xmlEntity
	afterText         []string
	parentID          xmlNodeID
	tagName           xmlTagNameSpan
	loopDetector      xmlLoopDetector
	doc               *xmlDocument
}

func (ctx *xmlContext) appendNode(d xmlNodeData) (xmlNodeID, *xmlError) {
	nodes := ctx.doc.nodes
	if uint64(len(nodes)) >= uint64(ctx.opt.NodesLimit) {
		return 0, &xmlError{Kind: xmlErrNodesLimitReached}
	}

	newChildID := xmlNodeID(len(nodes))
	appendingElement := d.typ == xmlNodeTypeElement
	d.parent = ctx.parentID
	d.prevSibling = ctx.doc.nodes[ctx.parentID].lastChild
	d.nextSubtree = xmlNoNode
	d.lastChild = xmlNoNode
	ctx.doc.nodes = append(ctx.doc.nodes, d)
	ctx.doc.nodes[ctx.parentID].lastChild = newChildID

	for _, id := range ctx.awaitingSubtree {
		ctx.doc.nodes[id].nextSubtree = newChildID
	}
	ctx.awaitingSubtree = ctx.awaitingSubtree[:0]

	if !appendingElement {
		ctx.awaitingSubtree = append(ctx.awaitingSubtree, newChildID)
	}

	return newChildID, nil
}

func (ctx *xmlContext) appendText(text string, rng xmlRange) *xmlError {
	if len(ctx.afterText) == 0 {
		if _, err := ctx.appendNode(xmlNodeData{typ: xmlNodeTypeText, text: text, rng: rng}); err != nil {
			return err
		}
	}
	ctx.afterText = append(ctx.afterText, text)
	return nil
}

func (ctx *xmlContext) mergeText() {
	node := &ctx.doc.nodes[len(ctx.doc.nodes)-1]
	if node.typ != xmlNodeTypeText {
		panic("unreachable")
	}
	node.text = strings.Join(ctx.afterText, "")
}

func (ctx *xmlContext) resetAfterText() {
	if len(ctx.afterText) == 0 {
		return
	}
	if len(ctx.afterText) > 1 {
		ctx.mergeText()
	}
	ctx.afterText = ctx.afterText[:0]
}

func xmlParseDoc(text string, opt xmlParsingOptions) (*xmlDocument, *xmlError) {
	// Rough guesses of the node and attribute counts.
	doc := &xmlDocument{
		text:       text,
		nodes:      make([]xmlNodeData, 0, strings.Count(text, "<")+1),
		attributes: make([]xmlAttributeData, 0, strings.Count(text, "=")),
	}

	// The root node.
	doc.nodes = append(doc.nodes, xmlNodeData{
		parent: xmlNoNode, prevSibling: xmlNoNode, nextSubtree: xmlNoNode, lastChild: xmlNoNode,
		typ: xmlNodeTypeRoot, rng: xmlRange{0, len(text)},
	})

	if err := doc.namespaces.pushNS(xmlNSXMLPrefix, true, xmlNSXMLURI); err != nil {
		return nil, err
	}

	ctx := &xmlContext{
		opt:               opt,
		namespaceStartIdx: 1,
		currentAttributes: make([]xmlTempAttributeData, 0, 16),
		parentPrefixes:    []string{""},
		afterText:         make([]string, 0, 1),
		parentID:          0,
		doc:               doc,
	}

	if err := xmlTokenize(text, opt.AllowDTD, ctx); err != nil {
		return nil, err
	}

	if _, ok := doc.Root().FirstElementChild(); !ok {
		return nil, &xmlError{Kind: xmlErrNoRootNode}
	}

	if len(ctx.parentPrefixes) > 1 {
		return nil, &xmlError{Kind: xmlErrUnclosedRootNode}
	}

	// Only needed to deduplicate namespaces during parsing.
	doc.namespaces.sortedOrder = nil
	return doc, nil
}

func (ctx *xmlContext) token(t *xmlToken) *xmlError {
	switch t.kind {
	case xmlTokenPI:
		ctx.resetAfterText()
		_, err := ctx.appendNode(xmlNodeData{typ: xmlNodeTypePI, pi: xmlPI{Target: t.local, Value: t.text, HasValue: t.hasText}, rng: t.rng})
		return err
	case xmlTokenComment:
		ctx.resetAfterText()
		_, err := ctx.appendNode(xmlNodeData{typ: xmlNodeTypeComment, text: t.text, rng: t.rng})
		return err
	case xmlTokenEntityDeclaration:
		ctx.entities = append(ctx.entities, xmlEntity{name: t.local, value: t.span})
	case xmlTokenElementStart:
		ctx.resetAfterText()

		if t.prefix == xmlXMLNS {
			return &xmlError{Kind: xmlErrInvalidElementNamePrefix, Pos: ctx.doc.TextPosAt(t.start + 1)}
		}

		ctx.tagName = xmlTagNameSpan{prefix: t.prefix, name: t.local, pos: t.start, prefixPos: t.start + 1}
	case xmlTokenAttribute:
		return xmlProcessAttribute(t.rng, t.qnameLen, t.eqLen, t.prefix, t.local, t.span, ctx)
	case xmlTokenElementEnd:
		ctx.resetAfterText()
		return xmlProcessElement(t, ctx)
	case xmlTokenText:
		return xmlProcessText(t.text, t.rng, ctx)
	case xmlTokenCdata:
		return xmlProcessCdata(t.text, t.rng, ctx)
	}
	return nil
}

func (ctx *xmlContext) resolveEntity(pubID string, hasPubID bool, uri string) (string, bool, error) {
	if ctx.opt.EntityResolver == nil {
		return "", false, nil
	}
	return ctx.opt.EntityResolver(pubID, hasPubID, uri)
}

func xmlProcessAttribute(rng xmlRange, qnameLen uint16, eqLen uint8, prefix, local string, valueSpan xmlStrSpan, ctx *xmlContext) *xmlError {
	value, err := xmlNormalizeAttribute(valueSpan, ctx)
	if err != nil {
		return err
	}

	if prefix == xmlXMLNS {
		// The xmlns namespace MUST NOT be declared as the default namespace.
		if value == xmlNSXMLNSURI {
			return &xmlError{Kind: xmlErrUnexpectedXmlnsUri, Pos: ctx.doc.TextPosAt(rng.Start)}
		}

		isXMLNSURI := value == xmlNSXMLURI

		// The prefix 'xml' is by definition bound to the namespace name
		// http://www.w3.org/XML/1998/namespace.
		// It MUST NOT be bound to any other namespace name.
		if local == xmlNSXMLPrefix {
			if !isXMLNSURI {
				return &xmlError{Kind: xmlErrInvalidXmlPrefixUri, Pos: ctx.doc.TextPosAt(rng.Start)}
			}
		} else if isXMLNSURI {
			// The xml namespace MUST NOT be bound to a non-xml prefix.
			return &xmlError{Kind: xmlErrUnexpectedXmlUri, Pos: ctx.doc.TextPosAt(rng.Start)}
		}

		// Check for duplicated namespaces.
		if ctx.doc.namespaces.exists(ctx.namespaceStartIdx, local, true) {
			return &xmlError{Kind: xmlErrDuplicatedNamespace, Name: local, Pos: ctx.doc.TextPosAt(rng.Start)}
		}

		// Xml namespace should not be added to the namespaces.
		if !isXMLNSURI {
			if err := ctx.doc.namespaces.pushNS(local, true, value); err != nil {
				return err
			}
		}
	} else if local == xmlXMLNS {
		// The xml namespace MUST NOT be declared as the default namespace.
		if value == xmlNSXMLURI {
			return &xmlError{Kind: xmlErrUnexpectedXmlUri, Pos: ctx.doc.TextPosAt(rng.Start)}
		}

		// The xmlns namespace MUST NOT be declared as the default namespace.
		if value == xmlNSXMLNSURI {
			return &xmlError{Kind: xmlErrUnexpectedXmlnsUri, Pos: ctx.doc.TextPosAt(rng.Start)}
		}

		if err := ctx.doc.namespaces.pushNS("", false, value); err != nil {
			return err
		}
	} else {
		ctx.currentAttributes = append(ctx.currentAttributes, xmlTempAttributeData{
			prefix: prefix, local: local, value: value, rng: rng, qnameLen: qnameLen, eqLen: eqLen,
		})
	}

	return nil
}

func xmlProcessElement(t *xmlToken, ctx *xmlContext) *xmlError {
	tokenRange := t.rng
	if ctx.tagName.name == "" {
		// May occur in XML like this:
		// <!DOCTYPE test [ <!ENTITY p '</p>'> ]>
		// <root>&p;</root>
		if t.end == xmlElementEndClose {
			return &xmlError{Kind: xmlErrUnexpectedEntityCloseTag, Pos: ctx.doc.TextPosAt(tokenRange.Start)}
		}
		panic("should be already checked by the tokenizer")
	}

	namespaces := ctx.resolveNamespaces()
	ctx.namespaceStartIdx = len(ctx.doc.namespaces.treeOrder)

	attributes, err := xmlResolveAttributes(namespaces, ctx)
	if err != nil {
		return err
	}

	switch t.end {
	case xmlElementEndEmpty:
		tagNSIdx, err := xmlGetNSIdxByPrefix(namespaces, ctx.tagName.prefixPos, ctx.tagName.prefix, ctx.doc)
		if err != nil {
			return err
		}
		newElementID, err := ctx.appendNode(xmlNodeData{
			typ:     xmlNodeTypeElement,
			tagName: xmlExpandedNameIndexed{nsIdx: tagNSIdx, localName: ctx.tagName.name},
			attrs:   attributes,
			nss:     namespaces,
			rng:     xmlRange{ctx.tagName.pos, tokenRange.End},
		})
		if err != nil {
			return err
		}
		ctx.awaitingSubtree = append(ctx.awaitingSubtree, newElementID)
	case xmlElementEndClose:
		parentNode := &ctx.doc.nodes[ctx.parentID]
		// Never empty: it starts with the root's prefix and grows with each parent change.
		parentPrefix := ctx.parentPrefixes[len(ctx.parentPrefixes)-1]

		parentNode.rng.End = tokenRange.End

		if parentNode.typ == xmlNodeTypeElement {
			if t.prefix != parentPrefix || t.local != parentNode.tagName.localName {
				return &xmlError{
					Kind:  xmlErrUnexpectedCloseTag,
					Name:  xmlGenQNameString(parentPrefix, parentNode.tagName.localName),
					Name2: xmlGenQNameString(t.prefix, t.local),
					Pos:   ctx.doc.TextPosAt(tokenRange.Start),
				}
			}
		}
		ctx.awaitingSubtree = append(ctx.awaitingSubtree, ctx.parentID)

		if parentNode.parent != xmlNoNode {
			ctx.parentID = parentNode.parent
			ctx.parentPrefixes = ctx.parentPrefixes[:len(ctx.parentPrefixes)-1]
		} else {
			// May occur in XML like this:
			// <!DOCTYPE test [ <!ENTITY p '<p></p></p>'> ]>
			// <p>&p;&p;
			return &xmlError{Kind: xmlErrUnexpectedEntityCloseTag, Pos: ctx.doc.TextPosAt(tokenRange.Start)}
		}
	case xmlElementEndOpen:
		tagNSIdx, err := xmlGetNSIdxByPrefix(namespaces, ctx.tagName.prefixPos, ctx.tagName.prefix, ctx.doc)
		if err != nil {
			return err
		}
		id, err := ctx.appendNode(xmlNodeData{
			typ:     xmlNodeTypeElement,
			tagName: xmlExpandedNameIndexed{nsIdx: tagNSIdx, localName: ctx.tagName.name},
			attrs:   attributes,
			nss:     namespaces,
			rng:     xmlRange{ctx.tagName.pos, tokenRange.End},
		})
		if err != nil {
			return err
		}
		ctx.parentID = id
		ctx.parentPrefixes = append(ctx.parentPrefixes, ctx.tagName.prefix)
	}

	return nil
}

func (ctx *xmlContext) resolveNamespaces() xmlShortRange {
	ns := &ctx.doc.namespaces
	if parent := &ctx.doc.nodes[ctx.parentID]; parent.typ == xmlNodeTypeElement {
		parentNS := parent.nss
		if ctx.namespaceStartIdx == len(ns.treeOrder) {
			return parentNS
		}

		for i := parentNS.start; i < parentNS.end; i++ {
			v := ns.get(ns.treeOrder[i])
			if !ns.exists(ctx.namespaceStartIdx, v.name, v.hasName) {
				ns.pushRef(int(i))
			}
		}
	}

	return xmlShortRange{uint32(ctx.namespaceStartIdx), uint32(len(ns.treeOrder))}
}

func xmlResolveAttributes(namespaces xmlShortRange, ctx *xmlContext) (xmlShortRange, *xmlError) {
	if len(ctx.currentAttributes) == 0 {
		return xmlShortRange{}, nil
	}

	if uint64(len(ctx.doc.attributes))+uint64(len(ctx.currentAttributes)) >= math.MaxUint32 {
		return xmlShortRange{}, &xmlError{Kind: xmlErrAttributesLimitReached}
	}

	startIdx := len(ctx.doc.attributes)

	attrs := ctx.currentAttributes
	ctx.currentAttributes = ctx.currentAttributes[:0]
	for _, attr := range attrs {
		var nsIdx int32
		switch {
		case attr.prefix == xmlNSXMLPrefix:
			// The prefix 'xml' is by definition bound to the namespace name
			// http://www.w3.org/XML/1998/namespace. This namespace is added
			// to the document on creation and is always element 0.
			nsIdx = 0
		case attr.prefix == "":
			// 'The namespace name for an unprefixed attribute name always has no value.'
			nsIdx = -1
		default:
			var err *xmlError
			nsIdx, err = xmlGetNSIdxByPrefix(namespaces, attr.rng.Start, attr.prefix, ctx.doc)
			if err != nil {
				return xmlShortRange{}, err
			}
		}

		attrName := xmlExpandedNameIndexed{nsIdx: nsIdx, localName: attr.local}

		// Check for duplicated attributes.
		name := attrName.asExpandedName(ctx.doc)
		for i := startIdx; i < len(ctx.doc.attributes); i++ {
			if ctx.doc.attributes[i].name.asExpandedName(ctx.doc) == name {
				return xmlShortRange{}, &xmlError{Kind: xmlErrDuplicatedAttribute, Name: attr.local, Pos: ctx.doc.TextPosAt(attr.rng.Start)}
			}
		}

		ctx.doc.attributes = append(ctx.doc.attributes, xmlAttributeData{
			name: attrName, value: attr.value, rng: attr.rng, qnameLen: attr.qnameLen, eqLen: attr.eqLen,
		})
	}

	return xmlShortRange{uint32(startIdx), uint32(len(ctx.doc.attributes))}, nil
}

func xmlProcessText(text string, rng xmlRange, ctx *xmlContext) *xmlError {
	// Add text as is if it has only valid characters.
	if strings.IndexByte(text, '&') < 0 && strings.IndexByte(text, '\r') < 0 {
		return ctx.appendText(text, rng)
	}

	var buf xmlTextBuffer
	isAsIs := false
	st := xmlStreamFromSubstr(ctx.doc.text, rng)
	stream := &st
	for !stream.atEnd() {
		chunk, err := xmlParseNextChunk(stream, ctx.entities)
		if err != nil {
			return err
		}
		switch chunk.kind {
		case xmlChunkByte:
			if isAsIs {
				buf.pushRaw(chunk.b)
				isAsIs = false
			} else {
				buf.pushFromText(chunk.b, stream.atEnd())
			}
		case xmlChunkChar:
			for _, b := range utf8.AppendRune(nil, chunk.c) {
				if ctx.loopDetector.depth > 0 {
					buf.pushFromText(b, stream.atEnd())
				} else {
					// Characters not from an entity are added as is, like lxml does.
					buf.pushRaw(b)
					isAsIs = true
				}
			}
		case xmlChunkText:
			isAsIs = false

			if !buf.isEmpty() {
				if err := ctx.appendText(buf.finish(), rng); err != nil {
					return err
				}
			}

			if err := ctx.loopDetector.incReferences(stream); err != nil {
				return err
			}
			if err := ctx.loopDetector.incDepth(stream); err != nil {
				return err
			}

			fragment := chunk.span
			text := ctx.doc.text
			if fragment.rng().Start == 0 {
				text = fragment.text
			}

			sub := xmlStreamFromSubstr(text, fragment.rng())
			prevTagName := ctx.tagName
			ctx.tagName = xmlTagNameSpan{}
			if err := xmlParseContent(&sub, ctx); err != nil {
				return err
			}
			ctx.tagName = prevTagName
			buf.clear()

			ctx.loopDetector.decDepth()
		}
	}

	if !buf.isEmpty() {
		return ctx.appendText(buf.finish(), rng)
	}
	return nil
}

// xmlProcessCdata adds CDATA text as is, except that `\r\n` and `\r` become `\n`.
func xmlProcessCdata(text string, rng xmlRange, ctx *xmlContext) *xmlError {
	pos := strings.IndexByte(text, '\r')

	// Add text as is if it has only valid characters.
	if pos < 0 {
		return ctx.appendText(text, rng)
	}

	var buf strings.Builder
	for pos >= 0 {
		line, rest := text[:pos], text[pos:]
		buf.WriteString(line)
		buf.WriteByte('\n')

		if len(rest) > 1 && rest[1] == '\n' {
			text = rest[2:]
		} else {
			text = rest[1:]
		}

		pos = strings.IndexByte(text, '\r')
	}
	buf.WriteString(text)

	return ctx.appendText(buf.String(), rng)
}

type xmlChunkKind uint8

const (
	xmlChunkByte xmlChunkKind = iota
	xmlChunkChar
	xmlChunkText
)

type xmlNextChunk struct {
	kind xmlChunkKind
	b    byte
	c    rune
	span xmlStrSpan
}

func xmlParseNextChunk(stream *xmlStream, entities []xmlEntity) (xmlNextChunk, *xmlError) {
	c := stream.currByteUnchecked()

	// Check for character/entity references.
	if c != '&' {
		stream.advance(1)
		return xmlNextChunk{kind: xmlChunkByte, b: c}, nil
	}

	start := stream.pos
	ref, ok := stream.consumeReference()
	if !ok {
		return xmlNextChunk{}, &xmlError{Kind: xmlErrMalformedEntityReference, Pos: stream.genTextPosFrom(start)}
	}
	if ref.isChar {
		return xmlNextChunk{kind: xmlChunkChar, c: ref.ch}, nil
	}
	for _, e := range entities {
		if e.name == ref.name {
			return xmlNextChunk{kind: xmlChunkText, span: e.value}, nil
		}
	}
	return xmlNextChunk{}, &xmlError{Kind: xmlErrUnknownEntityReference, Name: ref.name, Pos: stream.genTextPosFrom(start)}
}

// xmlNormalizeAttribute normalizes an attribute value (https://www.w3.org/TR/REC-xml/#AVNormalize).
func xmlNormalizeAttribute(text xmlStrSpan, ctx *xmlContext) (string, *xmlError) {
	// We assume that `&` indicates an entity or a character reference.
	// But in rare cases it can be just an another character.
	if strings.ContainsAny(text.text, "&\t\n\r") {
		var buf xmlTextBuffer
		if err := xmlNormalizeAttributeInto(text, &buf, ctx); err != nil {
			return "", err
		}
		return buf.finish(), nil
	}
	return text.text, nil
}

func xmlNormalizeAttributeInto(text xmlStrSpan, buf *xmlTextBuffer, ctx *xmlContext) *xmlError {
	st := xmlStreamFromSubstr(ctx.doc.text, text.rng())
	stream := &st
	for !stream.atEnd() {
		c := stream.currByteUnchecked()

		if c != '&' {
			stream.advance(1)
			next, err := stream.currByte()
			buf.pushFromAttr(c, next, err == nil)
			continue
		}

		// Check for character/entity references.
		start := stream.pos
		ref, ok := stream.consumeReference()
		if !ok {
			return &xmlError{Kind: xmlErrMalformedEntityReference, Pos: stream.genTextPosFrom(start)}
		}
		if ref.isChar {
			for _, b := range utf8.AppendRune(nil, ref.ch) {
				if ctx.loopDetector.depth > 0 {
					// Escaped `<` inside an ENTITY is an error.
					// Escaped `<` outside an ENTITY is ok.
					if b == '<' {
						return &xmlError{Kind: xmlErrInvalidAttributeValue, Pos: stream.genTextPosFrom(start)}
					}
					buf.pushFromAttr(b, 0, false)
				} else {
					// Characters not from an entity are added as is, like lxml does.
					buf.pushRaw(b)
				}
			}
			continue
		}

		found := false
		for _, e := range ctx.entities {
			if e.name != ref.name {
				continue
			}
			found = true
			if err := ctx.loopDetector.incReferences(stream); err != nil {
				return err
			}
			if err := ctx.loopDetector.incDepth(stream); err != nil {
				return err
			}
			if err := xmlNormalizeAttributeInto(e.value, buf, ctx); err != nil {
				return err
			}
			ctx.loopDetector.decDepth()
			break
		}
		if !found {
			return &xmlError{Kind: xmlErrUnknownEntityReference, Name: ref.name, Pos: stream.genTextPosFrom(start)}
		}
	}

	return nil
}

// xmlGetNSIdxByPrefix finds the namespace bound to prefix among namespaces; -1 when prefix is empty
// and no default namespace is declared.
func xmlGetNSIdxByPrefix(namespaces xmlShortRange, prefixPos int, prefix string, doc *xmlDocument) (int32, *xmlError) {
	// Prefix CAN be empty when the default namespace was defined.
	// Example: <e xmlns='http://www.w3.org'/>
	hasPrefix := prefix != ""
	ns := &doc.namespaces
	for _, idx := range ns.treeOrder[namespaces.start:namespaces.end] {
		v := ns.get(idx)
		if v.hasName == hasPrefix && v.name == prefix {
			return int32(idx), nil
		}
	}

	if hasPrefix {
		// An unknown namespace. Example: <e random:a='b'/>
		return -1, &xmlError{Kind: xmlErrUnknownNamespace, Name: prefix, Pos: doc.TextPosAt(prefixPos)}
	}
	// No namespace. Example: <e a='b'/>
	return -1, nil
}

func xmlGenQNameString(prefix, local string) string {
	if prefix == "" {
		return local
	}
	return prefix + ":" + local
}

type xmlTextBuffer struct {
	buf []byte
}

func (t *xmlTextBuffer) pushRaw(c byte) {
	t.buf = append(t.buf, c)
}

func (t *xmlTextBuffer) pushFromAttr(current byte, next byte, hasNext bool) {
	// \r in \r\n should be ignored.
	if current == '\r' && hasNext && next == '\n' {
		return
	}

	// \n, \r and \t should be converted into spaces.
	switch current {
	case '\n', '\r', '\t':
		current = ' '
	}

	t.buf = append(t.buf, current)
}

// pushFromText translates \r\n and any \r that is not followed by \n into a single \n
// (https://www.w3.org/TR/xml/#sec-line-ends).
func (t *xmlTextBuffer) pushFromText(c byte, atEnd bool) {
	if n := len(t.buf); n > 0 && t.buf[n-1] == '\r' {
		t.buf[n-1] = '\n'

		if atEnd && c == '\r' {
			t.buf = append(t.buf, '\n')
		} else if c != '\n' {
			t.buf = append(t.buf, c)
		}
	} else if atEnd && c == '\r' {
		t.buf = append(t.buf, '\n')
	} else {
		t.buf = append(t.buf, c)
	}
}

func (t *xmlTextBuffer) clear() {
	t.buf = t.buf[:0]
}

func (t *xmlTextBuffer) isEmpty() bool {
	return len(t.buf) == 0
}

func (t *xmlTextBuffer) finish() string {
	s := string(t.buf)
	t.buf = nil
	return s
}
