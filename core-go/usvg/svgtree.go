// Port of usvg 0.47 src/parser/svgtree/mod.rs: the intermediate SVG document, with CSS, presentation
// attributes, `inherit` and `use` already resolved, that the converters read.
//
// Map for converter authors (Rust -> Go):
//
//	Document                   *svgDocument (built by svgParseTree, svgtree_parse.go)
//	SvgNode<'a, 'input>        svgNode, a value type {doc, id}; == is Rust's PartialEq
//	NodeKind / NodeData        svgNodeKind / svgNodeData
//	Attribute                  svgAttribute{Name, Value, Important}
//	EId::X / AId::X            eidX / aidX (svgtree_names.go); String() is to_str
//	Option<T> returns          (T, bool)
//	Ancestors/Children/...     iter.Seq[svgNode], lazy like the Rust iterators
//
// FromValue impls: each is a parse func svgParseX(node, aid, value) (T, bool), wrapped by a method
// n.attrX(aid) that is `node.attribute::<X>(aid)` (including its "none" rule for mask, markers,
// clip-path, filter, font-size-adjust, text-decoration, stroke and stroke-dasharray). Generic forms:
// svgTryAttr(n, aid, svgParseX) is try_attribute::<X>, svgFindAttr(n, aid, svgNode.attrX) is
// find_attribute::<X>.
//
//	&str                       attrStr        string
//	f32                        attrF32        float32 (svgtypes Number, as f32)
//	svgtypes::Length           attrLength     stLength
//	Opacity                    attrOpacity    float32, NormalizedF32::new_clamped
//	Transform                  attrTransform  skia.Transform (identity when not valid)
//	svgtypes::TransformOrigin  attrTransformOrigin stTransformOrigin
//	svgtypes::ViewBox          attrViewBox    stViewBox
//	Units                      attrUnits      units
//	svgtypes::AspectRatio      attrAspectRatio stAspectRatio
//	svgtypes::PaintOrder       attrPaintOrder stPaintOrder
//	svgtypes::Color            attrColor      stColor
//	svgtypes::Angle            attrAngle      stAngle
//	svgtypes::EnableBackground attrEnableBackground stEnableBackground
//	svgtypes::Paint            attrPaint      stPaint
//	Vec<f32>                   attrNumberList []float32
//	Vec<svgtypes::Length>      attrLengthList []stLength
//	Visibility                 attrVisibility visibility
//	SpreadMethod               attrSpreadMethod SpreadMethod
//	ShapeRendering             attrShapeRendering ShapeRendering
//	TextRendering              attrTextRendering TextRendering
//	ImageRendering             attrImageRendering ImageRendering
//	BlendMode                  attrBlendMode  BlendMode
//	SvgNode                    attrNode       svgNode (IRI for href, FuncIRI otherwise)
//	LineCap (style.rs)         attrLineCap    LineCap
//	LineJoin (style.rs)        attrLineJoin   LineJoin
//	FillRule (style.rs)        attrFillRule   FillRule
//	Isolation (converter.rs)   attrIsolation  svgIsolation
//
// The text-only impls (parser/text.rs) and filter::ColorInterpolation (parser/filter.rs) belong to
// slices 4c2 and 4c3, whose types do not exist yet.

package usvg

import (
	"iter"
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// ErrorKind lists usvg::Error variants, in usvg's order.
type ErrorKind uint8

const (
	// ErrNotAnUtf8Str: only UTF-8 content is supported.
	ErrNotAnUtf8Str ErrorKind = iota
	// ErrMalformedGZip: compressed SVG must use the GZip algorithm.
	ErrMalformedGZip
	// ErrElementsLimitReached: usvg defines it but never returns it; the svgtree's 1_000_000-node and
	// depth limits are ParsingFailed with roxmltree's NodesLimitReached, as in Rust.
	ErrElementsLimitReached
	// ErrInvalidSize: SVG doesn't have a valid size.
	ErrInvalidSize
	// ErrParsingFailed: failed to parse the SVG data; XML holds the roxmltree error.
	ErrParsingFailed
)

var errorKindNames = [...]string{
	"NotAnUtf8Str", "MalformedGZip", "ElementsLimitReached", "InvalidSize", "ParsingFailed",
}

// Error is usvg::Error. XML is set for ErrParsingFailed only.
type Error struct {
	Kind ErrorKind
	XML  *xmlError
}

// Error is usvg::Error's Display text.
func (e *Error) Error() string {
	switch e.Kind {
	case ErrNotAnUtf8Str:
		return "provided data has not an UTF-8 encoding"
	case ErrMalformedGZip:
		return "provided data has a malformed GZip content"
	case ErrElementsLimitReached:
		return "the maximum number of SVG elements has been reached"
	case ErrInvalidSize:
		return "SVG has an invalid size"
	}
	return "SVG data parsing failed cause " + e.XML.Error()
}

// KindName returns the Rust variant name.
func (e *Error) KindName() string { return errorKindNames[e.Kind] }

// svgNodeID is NodeId: an index into svgDocument.nodes; the root is 0.
type svgNodeID uint32

// svgNoNode is Option<NodeId>::None.
const svgNoNode svgNodeID = math.MaxUint32

// svgNodeKind lists the NodeKind variants.
type svgNodeKind uint8

const (
	svgNodeRoot svgNodeKind = iota
	svgNodeElement
	svgNodeText
)

// svgNodeData is NodeData. tagName and attrStart/attrEnd (ShortRange) are set for elements, text for
// text nodes. firstChild/lastChild are both svgNoNode when there are no children.
type svgNodeData struct {
	parent, nextSibling   svgNodeID
	firstChild, lastChild svgNodeID
	kind                  svgNodeKind
	tagName               eid
	attrStart, attrEnd    uint32
	text                  string
}

// svgAttribute is Attribute.
type svgAttribute struct {
	Name      aid
	Value     string
	Important bool
}

// String is Attribute's Debug.
func (a svgAttribute) String() string {
	return "Attribute { name: " + a.Name.String() + ", value: " + a.Value + ", important: " +
		strconv.FormatBool(a.Important) + " }"
}

// svgDocument is Document: only element and text nodes; text nodes appear only inside `text`.
type svgDocument struct {
	nodes []svgNodeData
	attrs []svgAttribute
	links map[string]svgNodeID
}

// Root returns the root node.
func (d *svgDocument) Root() svgNode { return svgNode{d, 0} }

// RootElement returns the root element, which is always an `svg` element.
func (d *svgDocument) RootElement() svgNode {
	n, _ := d.Root().FirstElementChild()
	return n
}

// Descendants is doc.root().descendants().
func (d *svgDocument) Descendants() iter.Seq[svgNode] { return d.Root().Descendants() }

// ElementByID returns the element with the given id (the last one in document order).
func (d *svgDocument) ElementByID(id string) (svgNode, bool) {
	nid, ok := d.links[id]
	if !ok {
		return svgNode{}, false
	}
	return d.get(nid), true
}

func (d *svgDocument) get(id svgNodeID) svgNode { return svgNode{d, id} }

// String is Document's Debug output. Text is quoted with strconv.Quote, not Rust's escaping.
func (d *svgDocument) String() string {
	if !d.Root().HasChildren() {
		return "Document []"
	}
	var b strings.Builder
	line := func(depth int, s string) {
		b.WriteString(strings.Repeat("    ", depth))
		b.WriteString(s)
		b.WriteByte('\n')
	}
	var printChildren func(parent svgNode, depth int)
	printChildren = func(parent svgNode, depth int) {
		for child := range parent.Children() {
			if !child.IsElement() {
				line(depth, child.String())
				continue
			}
			line(depth, "Element {")
			tag, _ := child.TagName()
			line(depth, "    tag_name: Some("+tag.String()+")")
			if attrs := child.Attributes(); len(attrs) != 0 {
				line(depth+1, "attributes: [")
				for _, a := range attrs {
					line(depth+2, a.String())
				}
				line(depth+1, "]")
			}
			if child.HasChildren() {
				line(depth, "    children: [")
				printChildren(child, depth+2)
				line(depth, "    ]")
			}
			line(depth, "}")
		}
	}
	b.WriteString("Document [\n")
	printChildren(d.Root(), 1)
	b.WriteString("]\n")
	return b.String()
}

// svgNode is SvgNode: a node of a document.
type svgNode struct {
	doc *svgDocument
	id  svgNodeID
}

func (n svgNode) d() *svgNodeData { return &n.doc.nodes[n.id] }

func (n svgNode) node(id svgNodeID) (svgNode, bool) {
	if id == svgNoNode {
		return svgNode{}, false
	}
	return svgNode{n.doc, id}, true
}

// ID returns the node id.
func (n svgNode) ID() svgNodeID { return n.id }

// IsElement checks if the node is an element.
func (n svgNode) IsElement() bool { return n.d().kind == svgNodeElement }

// IsText checks if the node is a text node.
func (n svgNode) IsText() bool { return n.d().kind == svgNodeText }

// Document returns the node's document.
func (n svgNode) Document() *svgDocument { return n.doc }

// TagName returns the element's tag name; ok is false for other nodes.
func (n svgNode) TagName() (eid, bool) {
	d := n.d()
	if d.kind != svgNodeElement {
		return 0, false
	}
	return d.tagName, true
}

// hasTagName is `node.tag_name() == Some(e)`.
func (n svgNode) hasTagName(e eid) bool {
	t, ok := n.TagName()
	return ok && t == e
}

// ElementID returns the `id` attribute, or an empty string.
func (n svgNode) ElementID() string {
	s, _ := n.attrStr(aidId)
	return s
}

// Attributes returns the element's attributes; nil for other nodes.
func (n svgNode) Attributes() []svgAttribute {
	d := n.d()
	if d.kind != svgNodeElement {
		return nil
	}
	return n.doc.attrs[d.attrStart:d.attrEnd]
}

// HasAttribute checks if an attribute is present.
func (n svgNode) HasAttribute(a aid) bool {
	_, ok := n.rawAttr(a)
	return ok
}

// rawAttr is the value of the first attribute named a, with no further rules.
func (n svgNode) rawAttr(a aid) (string, bool) {
	for _, at := range n.Attributes() {
		if at.Name == a {
			return at.Value, true
		}
	}
	return "", false
}

// attributeID is attribute_id: the index of the attribute in doc.attrs.
func (n svgNode) attributeID(a aid) (int, bool) {
	d := n.d()
	if d.kind != svgNodeElement {
		return 0, false
	}
	for i, at := range n.Attributes() {
		if at.Name == a {
			return int(d.attrStart) + i, true
		}
	}
	return 0, false
}

// svgIsPossibleNone lists the attributes whose initial value is none: attribute() treats "none" on
// them as absent.
func svgIsPossibleNone(a aid) bool {
	switch a {
	case aidMask, aidMarkerStart, aidMarkerMid, aidMarkerEnd, aidClipPath, aidFilter,
		aidFontSizeAdjust, aidTextDecoration, aidStroke, aidStrokeDasharray:
		return true
	}
	return false
}

// svgAttr is SvgNode::attribute::<T>: parse is T's FromValue impl.
func svgAttr[T any](n svgNode, a aid, parse func(svgNode, aid, string) (T, bool)) (T, bool) {
	var zero T
	v, ok := n.rawAttr(a)
	if !ok {
		return zero, false
	}
	if svgIsPossibleNone(a) && v == "none" {
		return zero, false
	}
	return parse(n, a, v)
}

// svgTryAttr is SvgNode::try_attribute::<T>: like svgAttr without the "none" rule (and Rust's warning).
func svgTryAttr[T any](n svgNode, a aid, parse func(svgNode, aid, string) (T, bool)) (T, bool) {
	v, ok := n.rawAttr(a)
	if !ok {
		var zero T
		return zero, false
	}
	return parse(n, a, v)
}

// svgFindAttr is SvgNode::find_attribute::<T>: get is the matching attrX method expression, such as
// svgNode.attrLength.
func svgFindAttr[T any](n svgNode, a aid, get func(svgNode, aid) (T, bool)) (T, bool) {
	m, ok := n.findAttributeNode(a)
	if !ok {
		var zero T
		return zero, false
	}
	return get(m, a)
}

// findAttributeNode is find_attribute_impl: for inheritable attributes the closest ancestor (or self)
// that has it; otherwise self or the parent element only.
func (n svgNode) findAttributeNode(a aid) (svgNode, bool) {
	if a.isInheritable() {
		for m := range n.Ancestors() {
			if m.HasAttribute(a) {
				return m, true
			}
		}
		return svgNode{}, false
	}
	if n.HasAttribute(a) {
		return n, true
	}
	// Non-inheritable attributes can inherit a value only from a direct parent.
	p, ok := n.ParentElement()
	if ok && p.HasAttribute(a) {
		return p, true
	}
	return svgNode{}, false
}

// Text returns a text node's content, or an element's first child text; "" otherwise.
func (n svgNode) Text() string {
	d := n.d()
	switch d.kind {
	case svgNodeElement:
		if c, ok := n.FirstChild(); ok && c.IsText() {
			return c.d().text
		}
	case svgNodeText:
		return d.text
	}
	return ""
}

// Parent returns the parent node.
func (n svgNode) Parent() (svgNode, bool) { return n.node(n.d().parent) }

// ParentElement returns the closest ancestor element.
func (n svgNode) ParentElement() (svgNode, bool) {
	for p, ok := n.Parent(); ok; p, ok = p.Parent() {
		if p.IsElement() {
			return p, true
		}
	}
	return svgNode{}, false
}

// NextSibling returns the next sibling.
func (n svgNode) NextSibling() (svgNode, bool) { return n.node(n.d().nextSibling) }

// FirstChild returns the first child.
func (n svgNode) FirstChild() (svgNode, bool) { return n.node(n.d().firstChild) }

// FirstElementChild returns the first child element.
func (n svgNode) FirstElementChild() (svgNode, bool) {
	for c := range n.Children() {
		if c.IsElement() {
			return c, true
		}
	}
	return svgNode{}, false
}

// LastChild returns the last child.
func (n svgNode) LastChild() (svgNode, bool) { return n.node(n.d().lastChild) }

// HasChildren checks if the node has child nodes.
func (n svgNode) HasChildren() bool { return n.d().firstChild != svgNoNode }

// Ancestors iterates over this node and its ancestors.
func (n svgNode) Ancestors() iter.Seq[svgNode] {
	return func(yield func(svgNode) bool) {
		for m, ok := n, true; ok; m, ok = m.Parent() {
			if !yield(m) {
				return
			}
		}
	}
}

// Children iterates over the child nodes.
func (n svgNode) Children() iter.Seq[svgNode] {
	return func(yield func(svgNode) bool) {
		for c, ok := n.FirstChild(); ok; c, ok = c.NextSibling() {
			if !yield(c) {
				return
			}
		}
	}
}

// Descendants iterates over this node and its descendants in document order (Rust's Traverse,
// keeping the Open edges).
func (n svgNode) Descendants() iter.Seq[svgNode] {
	return func(yield func(svgNode) bool) {
		cur, open := n, true
		for {
			if open {
				if !yield(cur) {
					return
				}
				if c, ok := cur.FirstChild(); ok {
					cur = c
				} else {
					open = false
				}
				continue
			}
			if cur == n {
				return
			}
			if s, ok := cur.NextSibling(); ok {
				cur, open = s, true
			} else if p, ok := cur.Parent(); ok {
				cur = p
			} else {
				return
			}
		}
	}
}

// HrefIter iterates over this node and the elements it links to via `xlink:href`. It stops when a
// link points back at the current or the first node; other loops are not detected, as in Rust.
func (n svgNode) HrefIter() iter.Seq[svgNode] {
	return func(yield func(svgNode) bool) {
		origin, curr := n.id, n.id
		if !yield(n) {
			return
		}
		for {
			link, ok := n.doc.get(curr).attrNode(aidHref)
			if !ok || link.id == curr || link.id == origin {
				return
			}
			curr = link.id
			if !yield(link) {
				return
			}
		}
	}
}

// String is SvgNode's Debug.
func (n svgNode) String() string {
	d := n.d()
	switch d.kind {
	case svgNodeElement:
		parts := make([]string, len(n.Attributes()))
		for i, a := range n.Attributes() {
			parts[i] = a.String()
		}
		return "Element { tag_name: Some(" + d.tagName.String() + "), attributes: [" +
			strings.Join(parts, ", ") + "] }"
	case svgNodeText:
		return "Text(" + strconv.Quote(d.text) + ")"
	}
	return "Root"
}

// isGraphic checks if this is a graphics element.
func (e eid) isGraphic() bool {
	switch e {
	case eidCircle, eidEllipse, eidImage, eidLine, eidPath, eidPolygon, eidPolyline, eidRect,
		eidText, eidUse:
		return true
	}
	return false
}

// isGradient checks if this is a gradient element.
func (e eid) isGradient() bool { return e == eidLinearGradient || e == eidRadialGradient }

// isPaintServer checks if this is a paint server element.
func (e eid) isPaintServer() bool {
	return e == eidLinearGradient || e == eidRadialGradient || e == eidPattern
}

func (a aid) isPresentation() bool {
	switch a {
	case aidAlignmentBaseline, aidBaselineShift,
		aidBackgroundColor, // non-standard SVG attribute
		aidClipPath, aidClipRule, aidColor, aidColorInterpolation, aidColorInterpolationFilters,
		aidColorRendering, aidDirection, aidDisplay, aidDominantBaseline, aidFill, aidFillOpacity,
		aidFillRule, aidFilter, aidFloodColor, aidFloodOpacity, aidFontFamily,
		aidFontKerning,       // technically not presentation
		aidFontOpticalSizing, // technically not presentation
		aidFontSize, aidFontSizeAdjust, aidFontStretch, aidFontStyle, aidFontVariant,
		aidFontWeight, aidFontVariationSettings, aidGlyphOrientationHorizontal,
		aidGlyphOrientationVertical, aidImageRendering,
		aidIsolation, // technically not presentation
		aidLetterSpacing, aidLightingColor, aidMarkerEnd, aidMarkerMid, aidMarkerStart, aidMask,
		aidMaskType,
		aidMixBlendMode, // technically not presentation
		aidOpacity, aidOverflow, aidPaintOrder, aidShapeRendering, aidStopColor, aidStopOpacity,
		aidStroke, aidStrokeDasharray, aidStrokeDashoffset, aidStrokeLinecap, aidStrokeLinejoin,
		aidStrokeMiterlimit, aidStrokeOpacity, aidStrokeWidth, aidTextAnchor, aidTextDecoration,
		aidTextOverflow, aidTextRendering, aidTransform, aidTransformOrigin, aidUnicodeBidi,
		aidVectorEffect, aidVisibility, aidWhiteSpace, aidWordSpacing, aidWritingMode:
		return true
	}
	return false
}

// isInheritable checks if the attribute is inheritable.
func (a aid) isInheritable() bool {
	return a.isPresentation() && !svgIsNonInheritable(a)
}

func (a aid) allowsInheritValue() bool {
	switch a {
	case aidAlignmentBaseline, aidBaselineShift, aidClipPath, aidClipRule, aidColor,
		aidColorInterpolationFilters, aidDirection, aidDisplay, aidDominantBaseline, aidFill,
		aidFillOpacity, aidFillRule, aidFilter, aidFloodColor, aidFloodOpacity, aidFontFamily,
		aidFontKerning, aidFontOpticalSizing, aidFontSize, aidFontStretch, aidFontStyle,
		aidFontVariant, aidFontWeight, aidImageRendering, aidKerning, aidLetterSpacing,
		aidMarkerEnd, aidMarkerMid, aidMarkerStart, aidMask, aidOpacity, aidOverflow,
		aidShapeRendering, aidStopColor, aidStopOpacity, aidStroke, aidStrokeDasharray,
		aidStrokeDashoffset, aidStrokeLinecap, aidStrokeLinejoin, aidStrokeMiterlimit,
		aidStrokeOpacity, aidStrokeWidth, aidTextAnchor, aidTextDecoration, aidTextRendering,
		aidVisibility, aidWordSpacing, aidWritingMode:
		return true
	}
	return false
}

func svgIsNonInheritable(a aid) bool {
	switch a {
	case aidAlignmentBaseline, aidBaselineShift, aidClipPath, aidDisplay, aidDominantBaseline,
		aidFilter, aidFloodColor, aidFloodOpacity, aidMask, aidOpacity, aidOverflow,
		aidLightingColor, aidStopColor, aidStopOpacity, aidTextDecoration, aidTransform,
		aidTransformOrigin:
		return true
	}
	return false
}

// FromValue impls. Each svgParseX is `impl FromValue for X`; attrX is node.attribute::<X>(aid).

func svgParseStr(_ svgNode, _ aid, v string) (string, bool) { return v, true }

func svgParseF32(_ svgNode, _ aid, v string) (float32, bool) {
	n, err := stParseNumber(v)
	return float32(n), err == nil
}

func svgParseLength(_ svgNode, _ aid, v string) (stLength, bool) {
	l, err := stParseLength(v)
	return l, err == nil
}

func svgParseOpacity(_ svgNode, _ aid, v string) (float32, bool) {
	l, err := stParseLength(v)
	if err != nil {
		return 0, false
	}
	switch l.Unit {
	case stLengthUnitPercent:
		return normalizedClampedF32(float32(l.Number) / 100), true
	case stLengthUnitNone:
		return normalizedClampedF32(float32(l.Number)), true
	}
	return 0, false
}

func svgParseTransform(_ svgNode, _ aid, v string) (skia.Transform, bool) {
	ts, err := stParseTransform(v)
	if err != nil {
		return skia.Transform{}, false
	}
	t := skia.TransformFromRow(float32(ts.A), float32(ts.B), float32(ts.C), float32(ts.D),
		float32(ts.E), float32(ts.F))
	if t.IsValid() {
		return t, true
	}
	return skia.Identity(), true
}

func svgParseTransformOrigin(_ svgNode, _ aid, v string) (stTransformOrigin, bool) {
	o, err := stParseTransformOrigin(v)
	return o, err == nil
}

func svgParseViewBox(_ svgNode, _ aid, v string) (stViewBox, bool) {
	vb, err := stParseViewBox(v)
	return vb, err == nil
}

func svgParseUnits(_ svgNode, _ aid, v string) (units, bool) {
	switch v {
	case "userSpaceOnUse":
		return unitsUserSpaceOnUse, true
	case "objectBoundingBox":
		return unitsObjectBoundingBox, true
	}
	return 0, false
}

func svgParseAspectRatio(_ svgNode, _ aid, v string) (stAspectRatio, bool) {
	r, err := stParseAspectRatio(v)
	return r, err == nil
}

func svgParsePaintOrder(_ svgNode, _ aid, v string) (stPaintOrder, bool) {
	return stParsePaintOrder(v), true
}

func svgParseColor(_ svgNode, _ aid, v string) (stColor, bool) {
	c, err := stParseColor(v)
	return c, err == nil
}

func svgParseAngle(_ svgNode, _ aid, v string) (stAngle, bool) {
	a, err := stParseAngle(v)
	return a, err == nil
}

func svgParseEnableBackground(_ svgNode, _ aid, v string) (stEnableBackground, bool) {
	e, err := stParseEnableBackground(v)
	return e, err == nil
}

func svgParsePaint(_ svgNode, _ aid, v string) (stPaint, bool) {
	p, err := stParsePaint(v)
	return p, err == nil
}

func svgParseNumberList(_ svgNode, _ aid, v string) ([]float32, bool) {
	list := []float32{}
	p := stNewNumberListParser(v)
	for {
		n, ok, err := p.Next()
		if !ok {
			return list, true
		}
		if err != nil {
			return nil, false
		}
		list = append(list, float32(n))
	}
}

func svgParseLengthList(_ svgNode, _ aid, v string) ([]stLength, bool) {
	list := []stLength{}
	p := stNewLengthListParser(v)
	for {
		l, ok, err := p.Next()
		if !ok {
			return list, true
		}
		if err != nil {
			return nil, false
		}
		list = append(list, l)
	}
}

func svgParseVisibility(_ svgNode, _ aid, v string) (visibility, bool) {
	switch v {
	case "visible":
		return visibilityVisible, true
	case "hidden":
		return visibilityHidden, true
	case "collapse":
		return visibilityCollapse, true
	}
	return 0, false
}

func svgParseSpreadMethod(_ svgNode, _ aid, v string) (SpreadMethod, bool) {
	switch v {
	case "pad":
		return SpreadMethodPad, true
	case "reflect":
		return SpreadMethodReflect, true
	case "repeat":
		return SpreadMethodRepeat, true
	}
	return 0, false
}

func svgParseShapeRendering(_ svgNode, _ aid, v string) (ShapeRendering, bool) {
	switch v {
	case "optimizeSpeed":
		return ShapeRenderingOptimizeSpeed, true
	case "crispEdges":
		return ShapeRenderingCrispEdges, true
	case "auto", "geometricPrecision":
		return ShapeRenderingGeometricPrecision, true
	}
	return 0, false
}

func svgParseTextRendering(_ svgNode, _ aid, v string) (TextRendering, bool) {
	switch v {
	case "optimizeSpeed":
		return TextRenderingOptimizeSpeed, true
	case "auto", "optimizeLegibility":
		return TextRenderingOptimizeLegibility, true
	case "geometricPrecision":
		return TextRenderingGeometricPrecision, true
	}
	return 0, false
}

func svgParseImageRendering(_ svgNode, _ aid, v string) (ImageRendering, bool) {
	switch v {
	case "auto", "optimizeQuality":
		return ImageRenderingOptimizeQuality, true
	case "optimizeSpeed":
		return ImageRenderingOptimizeSpeed, true
	case "smooth":
		return ImageRenderingSmooth, true
	case "high-quality":
		return ImageRenderingHighQuality, true
	case "crisp-edges":
		return ImageRenderingCrispEdges, true
	case "pixelated":
		return ImageRenderingPixelated, true
	}
	return 0, false
}

func svgParseBlendMode(_ svgNode, _ aid, v string) (BlendMode, bool) {
	for i, name := range blendModeNames {
		if v == name {
			return BlendMode(i), true
		}
	}
	return 0, false
}

// svgParseNode resolves a link: an IRI (`#id`) for href, a FuncIRI (`url(#id)`) otherwise.
func svgParseNode(n svgNode, a aid, v string) (svgNode, bool) {
	var id string
	if a == aidHref {
		iri, err := stParseIRI(v)
		if err != nil {
			return svgNode{}, false
		}
		id = string(iri)
	} else {
		iri, err := stParseFuncIRI(v)
		if err != nil {
			return svgNode{}, false
		}
		id = string(iri)
	}
	return n.Document().ElementByID(id)
}

func svgParseLineCap(_ svgNode, _ aid, v string) (LineCap, bool) {
	switch v {
	case "butt":
		return LineCapButt, true
	case "round":
		return LineCapRound, true
	case "square":
		return LineCapSquare, true
	}
	return 0, false
}

func svgParseLineJoin(_ svgNode, _ aid, v string) (LineJoin, bool) {
	switch v {
	case "miter":
		return LineJoinMiter, true
	case "miter-clip":
		return LineJoinMiterClip, true
	case "round":
		return LineJoinRound, true
	case "bevel":
		return LineJoinBevel, true
	}
	return 0, false
}

func svgParseFillRule(_ svgNode, _ aid, v string) (FillRule, bool) {
	switch v {
	case "nonzero":
		return FillRuleNonZero, true
	case "evenodd":
		return FillRuleEvenOdd, true
	}
	return 0, false
}

// svgIsolation is converter.rs's private Isolation (`isolation` property); the zero value is the default.
type svgIsolation uint8

const (
	svgIsolationAuto svgIsolation = iota
	svgIsolationIsolate
)

func svgParseIsolation(_ svgNode, _ aid, v string) (svgIsolation, bool) {
	switch v {
	case "auto":
		return svgIsolationAuto, true
	case "isolate":
		return svgIsolationIsolate, true
	}
	return 0, false
}

func (n svgNode) attrStr(a aid) (string, bool)      { return svgAttr(n, a, svgParseStr) }
func (n svgNode) attrF32(a aid) (float32, bool)     { return svgAttr(n, a, svgParseF32) }
func (n svgNode) attrLength(a aid) (stLength, bool) { return svgAttr(n, a, svgParseLength) }
func (n svgNode) attrOpacity(a aid) (float32, bool) { return svgAttr(n, a, svgParseOpacity) }
func (n svgNode) attrTransform(a aid) (skia.Transform, bool) {
	return svgAttr(n, a, svgParseTransform)
}
func (n svgNode) attrTransformOrigin(a aid) (stTransformOrigin, bool) {
	return svgAttr(n, a, svgParseTransformOrigin)
}
func (n svgNode) attrViewBox(a aid) (stViewBox, bool) { return svgAttr(n, a, svgParseViewBox) }
func (n svgNode) attrUnits(a aid) (units, bool)       { return svgAttr(n, a, svgParseUnits) }
func (n svgNode) attrAspectRatio(a aid) (stAspectRatio, bool) {
	return svgAttr(n, a, svgParseAspectRatio)
}
func (n svgNode) attrPaintOrder(a aid) (stPaintOrder, bool) {
	return svgAttr(n, a, svgParsePaintOrder)
}
func (n svgNode) attrColor(a aid) (stColor, bool) { return svgAttr(n, a, svgParseColor) }
func (n svgNode) attrAngle(a aid) (stAngle, bool) { return svgAttr(n, a, svgParseAngle) }
func (n svgNode) attrEnableBackground(a aid) (stEnableBackground, bool) {
	return svgAttr(n, a, svgParseEnableBackground)
}
func (n svgNode) attrPaint(a aid) (stPaint, bool)         { return svgAttr(n, a, svgParsePaint) }
func (n svgNode) attrNumberList(a aid) ([]float32, bool)  { return svgAttr(n, a, svgParseNumberList) }
func (n svgNode) attrLengthList(a aid) ([]stLength, bool) { return svgAttr(n, a, svgParseLengthList) }
func (n svgNode) attrVisibility(a aid) (visibility, bool) { return svgAttr(n, a, svgParseVisibility) }
func (n svgNode) attrSpreadMethod(a aid) (SpreadMethod, bool) {
	return svgAttr(n, a, svgParseSpreadMethod)
}
func (n svgNode) attrShapeRendering(a aid) (ShapeRendering, bool) {
	return svgAttr(n, a, svgParseShapeRendering)
}
func (n svgNode) attrTextRendering(a aid) (TextRendering, bool) {
	return svgAttr(n, a, svgParseTextRendering)
}
func (n svgNode) attrImageRendering(a aid) (ImageRendering, bool) {
	return svgAttr(n, a, svgParseImageRendering)
}
func (n svgNode) attrBlendMode(a aid) (BlendMode, bool) { return svgAttr(n, a, svgParseBlendMode) }

// attrNode is attribute::<SvgNode>, which is also Rust's private node_attribute.
func (n svgNode) attrNode(a aid) (svgNode, bool)           { return svgAttr(n, a, svgParseNode) }
func (n svgNode) attrLineCap(a aid) (LineCap, bool)        { return svgAttr(n, a, svgParseLineCap) }
func (n svgNode) attrLineJoin(a aid) (LineJoin, bool)      { return svgAttr(n, a, svgParseLineJoin) }
func (n svgNode) attrFillRule(a aid) (FillRule, bool)      { return svgAttr(n, a, svgParseFillRule) }
func (n svgNode) attrIsolation(a aid) (svgIsolation, bool) { return svgAttr(n, a, svgParseIsolation) }
