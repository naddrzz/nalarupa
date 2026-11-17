package usvg

// A read-only XML tree, ported from roxmltree 0.21.1 src/lib.rs. Strings are Go strings (Rust's
// StringStorage), and iterators return slices in document order.

import (
	"fmt"
	"sort"
)

const (
	// xmlNSXMLURI is the http://www.w3.org/XML/1998/namespace URI.
	xmlNSXMLURI = "http://www.w3.org/XML/1998/namespace"
	// xmlNSXMLPrefix is the prefix 'xml', which is by definition bound to xmlNSXMLURI.
	xmlNSXMLPrefix = "xml"
	// xmlNSXMLNSURI is the http://www.w3.org/2000/xmlns/ URI.
	xmlNSXMLNSURI = "http://www.w3.org/2000/xmlns/"
	// xmlXMLNS is the string 'xmlns', which is used to declare new namespaces.
	xmlXMLNS = "xmlns"
)

// xmlTextPos is a row/column position in the original text, starting at 1:1. Columns count chars.
type xmlTextPos struct {
	Row, Col uint32
}

func (p xmlTextPos) String() string {
	return fmt.Sprintf("%d:%d", p.Row, p.Col)
}

// xmlDocument is an XML tree. It has five node types: Root, Element, PI, Comment and Text; the XML
// declaration is skipped and CDATA becomes text.
type xmlDocument struct {
	// The original data, needed by TextPosAt.
	text       string
	nodes      []xmlNodeData
	attributes []xmlAttributeData
	namespaces xmlNamespaces
}

// Root returns the root node.
func (d *xmlDocument) Root() xmlNode {
	return xmlNode{doc: d, id: 0}
}

// GetNode returns the node with the given id; id 0 is the root.
func (d *xmlDocument) GetNode(id xmlNodeID) (xmlNode, bool) {
	if uint64(id) >= uint64(len(d.nodes)) {
		return xmlNode{}, false
	}
	return xmlNode{doc: d, id: id}, true
}

// RootElement returns the first element child of the root, which every parsed document has.
func (d *xmlDocument) RootElement() xmlNode {
	n, ok := d.Root().FirstElementChild()
	if !ok {
		panic("XML documents must contain a root element")
	}
	return n
}

// Descendants returns the root and all its descendants.
func (d *xmlDocument) Descendants() []xmlNode {
	return d.Root().Descendants()
}

// TextPosAt converts a byte position in the original text into a TextPos. Expensive.
func (d *xmlDocument) TextPosAt(pos int) xmlTextPos {
	s := xmlNewStream(d.text)
	return s.genTextPosFrom(pos)
}

// InputText returns the original text.
func (d *xmlDocument) InputText() string {
	return d.text
}

// xmlNodeType lists the node types.
type xmlNodeType uint8

const (
	xmlNodeTypeRoot    xmlNodeType = iota // the root node of the document
	xmlNodeTypeElement                    // an element; only elements have a tag name and attributes
	xmlNodeTypePI                         // a processing instruction
	xmlNodeTypeComment                    // a comment
	xmlNodeTypeText                       // a text node
)

// xmlPI is a processing instruction; Value is set when HasValue.
type xmlPI struct {
	Target   string
	Value    string
	HasValue bool
}

// xmlShortRange is a range of u32 indices.
type xmlShortRange struct {
	start, end uint32
}

// xmlNodeID is a node index in its document. It is not checked against the document it is used with.
type xmlNodeID uint32

// xmlNoNode stands for Rust's None in node links; roxmltree ids stay below u32::MAX.
const xmlNoNode = ^xmlNodeID(0)

type xmlNodeData struct {
	parent      xmlNodeID
	prevSibling xmlNodeID
	nextSubtree xmlNodeID
	lastChild   xmlNodeID
	typ         xmlNodeType
	// Element.
	tagName xmlExpandedNameIndexed
	attrs   xmlShortRange
	nss     xmlShortRange
	// PI.
	pi xmlPI
	// Comment and Text.
	text string
	rng  xmlRange
}

type xmlAttributeData struct {
	name     xmlExpandedNameIndexed
	value    string
	rng      xmlRange
	qnameLen uint16
	eqLen    uint8 // includes any surrounding spaces
}

// xmlAttribute is an attribute of an element.
type xmlAttribute struct {
	doc *xmlDocument
	d   *xmlAttributeData
}

// Namespace returns the attribute's namespace URI.
func (a xmlAttribute) Namespace() (string, bool) {
	ns, ok := a.d.name.namespace(a.doc)
	if !ok {
		return "", false
	}
	return ns.uri, true
}

// Name returns the attribute's local name.
func (a xmlAttribute) Name() string {
	return a.d.name.localName
}

// Value returns the attribute's value.
func (a xmlAttribute) Value() string {
	return a.d.value
}

// Position returns the attribute's start position in bytes (deprecated in Rust for Range).
func (a xmlAttribute) Position() int {
	return a.d.rng.Start
}

// Range returns the attribute's byte range in the original text: `n:attr='value'`.
func (a xmlAttribute) Range() xmlRange {
	return a.d.rng
}

// RangeQName returns the byte range of the attribute's qualified name. The qname length is capped
// at u16::MAX, past which the end is wrong.
func (a xmlAttribute) RangeQName() xmlRange {
	return xmlRange{a.d.rng.Start, a.d.rng.Start + int(a.d.qnameLen)}
}

// RangeValue returns the byte range of the attribute's value without the quotes; for an empty
// value both ends are at the closing quote. Past u16::MAX qname bytes or u8::MAX spaces around `=`
// the start is wrong.
func (a xmlAttribute) RangeValue() xmlRange {
	start := a.d.rng.Start + int(a.d.qnameLen) + int(a.d.eqLen) + 1
	return xmlRange{start, a.d.rng.End - 1}
}

// xmlNamespace is a namespace prefix and URI pair.
type xmlNamespace struct {
	name    string
	hasName bool
	uri     string
}

// Name returns the namespace prefix; false for the default namespace.
func (n xmlNamespace) Name() (string, bool) {
	return n.name, n.hasName
}

// URI returns the namespace URI.
func (n xmlNamespace) URI() string {
	return n.uri
}

type xmlNamespaces struct {
	// Deduplicated namespace values used throughout the document.
	values []xmlNamespace
	// Indices into values in tree order as the document is parsed.
	treeOrder []uint16
	// Indices into values sorted by value, used for deduplication while parsing.
	sortedOrder []uint16
}

// xmlCmpNamespace orders like Rust's (Option<&str>, &str) tuples: None before Some.
func xmlCmpNamespace(v *xmlNamespace, name string, hasName bool, uri string) int {
	if v.hasName != hasName {
		if !v.hasName {
			return -1
		}
		return 1
	}
	if hasName && v.name != name {
		if v.name < name {
			return -1
		}
		return 1
	}
	switch {
	case v.uri < uri:
		return -1
	case v.uri > uri:
		return 1
	}
	return 0
}

func (n *xmlNamespaces) pushNS(name string, hasName bool, uri string) *xmlError {
	i := sort.Search(len(n.sortedOrder), func(i int) bool {
		return xmlCmpNamespace(&n.values[n.sortedOrder[i]], name, hasName, uri) >= 0
	})
	var idx uint16
	if i < len(n.sortedOrder) && xmlCmpNamespace(&n.values[n.sortedOrder[i]], name, hasName, uri) == 0 {
		idx = n.sortedOrder[i]
	} else {
		if len(n.values) > 0xFFFF {
			return &xmlError{Kind: xmlErrNamespacesLimitReached}
		}
		idx = uint16(len(n.values))
		n.values = append(n.values, xmlNamespace{name: name, hasName: hasName, uri: uri})
		n.sortedOrder = append(n.sortedOrder, 0)
		copy(n.sortedOrder[i+1:], n.sortedOrder[i:])
		n.sortedOrder[i] = idx
	}

	n.treeOrder = append(n.treeOrder, idx)
	return nil
}

func (n *xmlNamespaces) pushRef(treeIdx int) {
	n.treeOrder = append(n.treeOrder, n.treeOrder[treeIdx])
}

func (n *xmlNamespaces) exists(start int, prefix string, hasPrefix bool) bool {
	for _, idx := range n.treeOrder[start:] {
		v := &n.values[idx]
		if v.hasName == hasPrefix && v.name == prefix {
			return true
		}
	}
	return false
}

func (n *xmlNamespaces) get(idx uint16) *xmlNamespace {
	return &n.values[idx]
}

type xmlExpandedNameIndexed struct {
	nsIdx     int32 // -1 for none
	localName string
}

func (e xmlExpandedNameIndexed) namespace(doc *xmlDocument) (*xmlNamespace, bool) {
	if e.nsIdx < 0 {
		return nil, false
	}
	return doc.namespaces.get(uint16(e.nsIdx)), true
}

func (e xmlExpandedNameIndexed) asExpandedName(doc *xmlDocument) xmlExpandedName {
	ns, ok := e.namespace(doc)
	if !ok {
		return xmlExpandedName{name: e.localName}
	}
	return xmlExpandedName{uri: ns.uri, hasURI: true, name: e.localName}
}

// xmlExpandedName is a namespace URI and local name pair. Comparable with ==.
type xmlExpandedName struct {
	uri    string
	hasURI bool
	name   string
}

// Namespace returns the namespace URI.
func (e xmlExpandedName) Namespace() (string, bool) {
	return e.uri, e.hasURI
}

// Name returns the local name.
func (e xmlExpandedName) Name() string {
	return e.name
}

// String formats like Rust's Debug: `{uri}name` or `name`.
func (e xmlExpandedName) String() string {
	if e.hasURI {
		return "{" + e.uri + "}" + e.name
	}
	return e.name
}

// xmlNode is a node in a document. Nodes compare equal with == when they are the same node of the
// same document; ID gives document order.
type xmlNode struct {
	doc *xmlDocument
	id  xmlNodeID
}

func (n xmlNode) d() *xmlNodeData {
	return &n.doc.nodes[n.id]
}

func (n xmlNode) node(id xmlNodeID) (xmlNode, bool) {
	if id == xmlNoNode {
		return xmlNode{}, false
	}
	return xmlNode{doc: n.doc, id: id}, true
}

// ID returns the node's id.
func (n xmlNode) ID() xmlNodeID {
	return n.id
}

// NodeType returns the node's type.
func (n xmlNode) NodeType() xmlNodeType {
	return n.d().typ
}

func (n xmlNode) IsRoot() bool    { return n.NodeType() == xmlNodeTypeRoot }
func (n xmlNode) IsElement() bool { return n.NodeType() == xmlNodeTypeElement }
func (n xmlNode) IsPI() bool      { return n.NodeType() == xmlNodeTypePI }
func (n xmlNode) IsComment() bool { return n.NodeType() == xmlNodeTypeComment }
func (n xmlNode) IsText() bool    { return n.NodeType() == xmlNodeTypeText }

// Document returns the node's document.
func (n xmlNode) Document() *xmlDocument {
	return n.doc
}

// TagName returns the element's tag name; an empty name without namespace for other nodes.
func (n xmlNode) TagName() xmlExpandedName {
	d := n.d()
	if d.typ != xmlNodeTypeElement {
		return xmlExpandedName{}
	}
	return d.tagName.asExpandedName(n.doc)
}

// HasTagName reports whether the node is an element with the given local name, in any namespace.
func (n xmlNode) HasTagName(name string) bool {
	d := n.d()
	return d.typ == xmlNodeTypeElement && d.tagName.localName == name
}

// HasTagNameNS reports whether the node is an element with the given namespace URI and local name.
func (n xmlNode) HasTagNameNS(ns, name string) bool {
	d := n.d()
	return d.typ == xmlNodeTypeElement &&
		d.tagName.asExpandedName(n.doc) == xmlExpandedName{uri: ns, hasURI: true, name: name}
}

// DefaultNamespace returns the element's default namespace URI.
func (n xmlNode) DefaultNamespace() (string, bool) {
	for _, ns := range n.namespaceData() {
		if !ns.hasName {
			return ns.uri, true
		}
	}
	return "", false
}

// LookupPrefix returns the prefix of the first in-scope namespace with the given URI; false when
// there is none or it is the default namespace.
func (n xmlNode) LookupPrefix(uri string) (string, bool) {
	if uri == xmlNSXMLURI {
		return xmlNSXMLPrefix, true
	}
	for _, ns := range n.namespaceData() {
		if ns.uri == uri {
			return ns.name, ns.hasName
		}
	}
	return "", false
}

// LookupNamespaceURI returns the URI bound to prefix; an empty prefix looks up the default
// namespace (a declared prefix is never empty, so this is lossless).
func (n xmlNode) LookupNamespaceURI(prefix string) (string, bool) {
	hasPrefix := prefix != ""
	for _, ns := range n.namespaceData() {
		if ns.hasName == hasPrefix && ns.name == prefix {
			return ns.uri, true
		}
	}
	return "", false
}

// Attribute returns the value of the attribute with the given local name, in any namespace.
func (n xmlNode) Attribute(name string) (string, bool) {
	if a, ok := n.AttributeNode(name); ok {
		return a.Value(), true
	}
	return "", false
}

// AttributeNS returns the value of the attribute with the given namespace URI and local name.
func (n xmlNode) AttributeNS(ns, name string) (string, bool) {
	if a, ok := n.AttributeNodeNS(ns, name); ok {
		return a.Value(), true
	}
	return "", false
}

// AttributeNode is Attribute returning the attribute itself.
func (n xmlNode) AttributeNode(name string) (xmlAttribute, bool) {
	for i := range n.attributeData() {
		d := &n.attributeData()[i]
		if d.name.localName == name {
			return xmlAttribute{doc: n.doc, d: d}, true
		}
	}
	return xmlAttribute{}, false
}

// AttributeNodeNS is AttributeNS returning the attribute itself.
func (n xmlNode) AttributeNodeNS(ns, name string) (xmlAttribute, bool) {
	want := xmlExpandedName{uri: ns, hasURI: true, name: name}
	attrs := n.attributeData()
	for i := range attrs {
		if attrs[i].name.asExpandedName(n.doc) == want {
			return xmlAttribute{doc: n.doc, d: &attrs[i]}, true
		}
	}
	return xmlAttribute{}, false
}

// HasAttribute reports whether the element has an attribute with the given local name.
func (n xmlNode) HasAttribute(name string) bool {
	_, ok := n.AttributeNode(name)
	return ok
}

// HasAttributeNS reports whether the element has an attribute with the given namespace and name.
func (n xmlNode) HasAttributeNS(ns, name string) bool {
	_, ok := n.AttributeNodeNS(ns, name)
	return ok
}

func (n xmlNode) attributeData() []xmlAttributeData {
	d := n.d()
	if d.typ != xmlNodeTypeElement {
		return nil
	}
	return n.doc.attributes[d.attrs.start:d.attrs.end]
}

// Attributes returns the element's attributes.
func (n xmlNode) Attributes() []xmlAttribute {
	attrs := n.attributeData()
	out := make([]xmlAttribute, len(attrs))
	for i := range attrs {
		out[i] = xmlAttribute{doc: n.doc, d: &attrs[i]}
	}
	return out
}

func (n xmlNode) namespaceData() []*xmlNamespace {
	d := n.d()
	if d.typ != xmlNodeTypeElement {
		return nil
	}
	idxs := n.doc.namespaces.treeOrder[d.nss.start:d.nss.end]
	out := make([]*xmlNamespace, len(idxs))
	for i, idx := range idxs {
		out[i] = n.doc.namespaces.get(idx)
	}
	return out
}

// Namespaces returns the namespaces in scope on the element.
func (n xmlNode) Namespaces() []xmlNamespace {
	data := n.namespaceData()
	out := make([]xmlNamespace, len(data))
	for i, ns := range data {
		out[i] = *ns
	}
	return out
}

// Text returns an element's first child text, or a comment's or text node's own text.
func (n xmlNode) Text() (string, bool) {
	d := n.d()
	switch d.typ {
	case xmlNodeTypeElement:
		if c, ok := n.FirstChild(); ok && c.IsText() {
			return c.d().text, true
		}
	case xmlNodeTypeComment, xmlNodeTypeText:
		return d.text, true
	}
	return "", false
}

// Tail returns the text right after an element.
func (n xmlNode) Tail() (string, bool) {
	if !n.IsElement() {
		return "", false
	}
	if s, ok := n.NextSibling(); ok && s.IsText() {
		return s.d().text, true
	}
	return "", false
}

// PI returns the node as a processing instruction.
func (n xmlNode) PI() (xmlPI, bool) {
	d := n.d()
	if d.typ != xmlNodeTypePI {
		return xmlPI{}, false
	}
	return d.pi, true
}

// Parent returns the node's parent.
func (n xmlNode) Parent() (xmlNode, bool) {
	return n.node(n.d().parent)
}

// ParentElement returns the closest ancestor element.
func (n xmlNode) ParentElement() (xmlNode, bool) {
	for p, ok := n.Parent(); ok; p, ok = p.Parent() {
		if p.IsElement() {
			return p, true
		}
	}
	return xmlNode{}, false
}

// PrevSibling returns the previous sibling.
func (n xmlNode) PrevSibling() (xmlNode, bool) {
	return n.node(n.d().prevSibling)
}

// PrevSiblingElement returns the previous sibling element.
func (n xmlNode) PrevSiblingElement() (xmlNode, bool) {
	for s, ok := n.PrevSibling(); ok; s, ok = s.PrevSibling() {
		if s.IsElement() {
			return s, true
		}
	}
	return xmlNode{}, false
}

// NextSibling returns the next sibling.
func (n xmlNode) NextSibling() (xmlNode, bool) {
	next, ok := n.node(n.d().nextSubtree)
	if !ok {
		return xmlNode{}, false
	}
	// next_subtree always has a previous sibling; it is the next sibling only when that is n.
	if next.d().prevSibling == n.id {
		return next, true
	}
	return xmlNode{}, false
}

// NextSiblingElement returns the next sibling element.
func (n xmlNode) NextSiblingElement() (xmlNode, bool) {
	for s, ok := n.NextSibling(); ok; s, ok = s.NextSibling() {
		if s.IsElement() {
			return s, true
		}
	}
	return xmlNode{}, false
}

// FirstChild returns the first child.
func (n xmlNode) FirstChild() (xmlNode, bool) {
	if n.d().lastChild == xmlNoNode {
		return xmlNode{}, false
	}
	return xmlNode{doc: n.doc, id: n.id + 1}, true
}

// FirstElementChild returns the first child element.
func (n xmlNode) FirstElementChild() (xmlNode, bool) {
	for c, ok := n.FirstChild(); ok; c, ok = c.NextSibling() {
		if c.IsElement() {
			return c, true
		}
	}
	return xmlNode{}, false
}

// LastChild returns the last child.
func (n xmlNode) LastChild() (xmlNode, bool) {
	return n.node(n.d().lastChild)
}

// LastElementChild returns the last child element.
func (n xmlNode) LastElementChild() (xmlNode, bool) {
	for c, ok := n.LastChild(); ok; c, ok = c.PrevSibling() {
		if c.IsElement() {
			return c, true
		}
	}
	return xmlNode{}, false
}

// HasSiblings reports whether the node has siblings.
func (n xmlNode) HasSiblings() bool {
	if n.d().prevSibling != xmlNoNode {
		return true
	}
	_, ok := n.NextSibling()
	return ok
}

// HasChildren reports whether the node has children.
func (n xmlNode) HasChildren() bool {
	return n.d().lastChild != xmlNoNode
}

func (n xmlNode) axis(next func(xmlNode) (xmlNode, bool)) []xmlNode {
	var out []xmlNode
	for c, ok := n, true; ok; c, ok = next(c) {
		out = append(out, c)
	}
	return out
}

// Ancestors returns this node and its ancestors, nearest first.
func (n xmlNode) Ancestors() []xmlNode { return n.axis(xmlNode.Parent) }

// PrevSiblings returns this node and its previous siblings, nearest first.
func (n xmlNode) PrevSiblings() []xmlNode { return n.axis(xmlNode.PrevSibling) }

// NextSiblings returns this node and its next siblings, nearest first.
func (n xmlNode) NextSiblings() []xmlNode { return n.axis(xmlNode.NextSibling) }

// FirstChildren returns this node, its first child, that child's first child and so on.
func (n xmlNode) FirstChildren() []xmlNode { return n.axis(xmlNode.FirstChild) }

// LastChildren returns this node, its last child, that child's last child and so on.
func (n xmlNode) LastChildren() []xmlNode { return n.axis(xmlNode.LastChild) }

// Children returns the node's children.
func (n xmlNode) Children() []xmlNode {
	var out []xmlNode
	for c, ok := n.FirstChild(); ok; c, ok = c.NextSibling() {
		out = append(out, c)
	}
	return out
}

// Descendants returns this node and its descendants in document order.
func (n xmlNode) Descendants() []xmlNode {
	until := xmlNodeID(len(n.doc.nodes))
	if next := n.d().nextSubtree; next != xmlNoNode {
		until = next
	}
	out := make([]xmlNode, 0, until-n.id)
	for id := n.id; id < until; id++ {
		out = append(out, xmlNode{doc: n.doc, id: id})
	}
	return out
}

// Range returns the node's byte range in the original text.
func (n xmlNode) Range() xmlRange {
	return n.d().rng
}

// Position returns the node's start position in bytes.
func (n xmlNode) Position() int {
	return n.d().rng.Start
}
