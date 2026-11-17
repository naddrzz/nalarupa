// Port of usvg 0.47 src/parser/svgtree/parse.rs: builds an svgDocument from an XML document, resolving
// CSS, the `style` attribute, `inherit`, `use` and recursive links.

package usvg

const (
	svgNamespaceSVG   = "http://www.w3.org/2000/svg"
	svgNamespaceXLink = "http://www.w3.org/1999/xlink"
)

// svgParseTree is Document::parse_tree; injectedStyleSheet is Options.style_sheet when hasInjected.
// A non-nil error is an *Error: ParsingFailed with roxmltree's NoRootNode when the root element is not
// `svg`, or NodesLimitReached past 1_000_000 nodes or a nesting depth of 1024.
func svgParseTree(xml *xmlDocument, injectedStyleSheet string, hasInjected bool) (*svgDocument, error) {
	doc, err := svgParse(xml, injectedStyleSheet, hasInjected)
	if err != nil {
		return nil, &Error{Kind: ErrParsingFailed, XML: err}
	}
	return doc, nil
}

// appendNode is Document::append: adds a node as the last child of parent.
func (d *svgDocument) appendNode(parent svgNodeID, data svgNodeData) svgNodeID {
	id := svgNodeID(len(d.nodes))
	data.parent = parent
	data.nextSibling = svgNoNode
	data.firstChild, data.lastChild = svgNoNode, svgNoNode
	d.nodes = append(d.nodes, data)
	p := &d.nodes[parent]
	if p.lastChild != svgNoNode {
		d.nodes[p.lastChild].nextSibling = id
	}
	if p.firstChild == svgNoNode {
		p.firstChild = id
	}
	p.lastChild = id
	return id
}

func (d *svgDocument) appendText(parent svgNodeID, text string) svgNodeID {
	return d.appendNode(parent, svgNodeData{kind: svgNodeText, text: text})
}

// svgParser carries parse()'s shared arguments: the document being built, the style sheet and the
// map of XML ids for `use`.
type svgParser struct {
	doc   *svgDocument
	sheet *cssStyleSheet
	idMap map[string]xmlNode
}

func svgParse(xml *xmlDocument, injected string, hasInjected bool) (*svgDocument, *xmlError) {
	doc := &svgDocument{links: map[string]svgNodeID{}}

	// Build a map of id -> node for resolve_href; the first node wins.
	idMap := map[string]xmlNode{}
	for _, node := range xml.Descendants() {
		if id, ok := node.Attribute("id"); ok {
			if _, dup := idMap[id]; !dup {
				idMap[id] = node
			}
		}
	}

	// Add a root node.
	doc.nodes = append(doc.nodes, svgNodeData{
		parent: svgNoNode, nextSibling: svgNoNode, firstChild: svgNoNode, lastChild: svgNoNode,
		kind: svgNodeRoot,
	})

	p := &svgParser{doc: doc, sheet: svgResolveCSS(xml, injected, hasInjected), idMap: idMap}
	if err := p.parseXMLNodeChildren(xml.Root(), xml.Root(), 0, false, 0); err != nil {
		return nil, err
	}

	// Check that the root element is `svg`.
	child, ok := doc.Root().FirstElementChild()
	if !ok || !child.hasTagName(eidSvg) {
		return nil, xmlNoRootNodeError()
	}

	// Collect all elements with an `id` attribute; the last one wins.
	links := map[string]svgNodeID{}
	for node := range doc.Descendants() {
		if id, ok := node.attrStr(aidId); ok {
			links[id] = node.id
		}
	}
	doc.links = links

	svgFixRecursivePatterns(doc)
	svgFixRecursiveLinks(eidClipPath, aidClipPath, doc)
	svgFixRecursiveLinks(eidMask, aidMask, doc)
	svgFixRecursiveLinks(eidFilter, aidFilter, doc)
	svgFixRecursiveFeImage(doc)
	return doc, nil
}

// svgParseTagName is parse_tag_name: the EId of an element in no namespace or the SVG one.
func svgParseTagName(node xmlNode) (eid, bool) {
	if !node.IsElement() {
		return 0, false
	}
	if ns, ok := node.TagName().Namespace(); ok && ns != svgNamespaceSVG {
		return 0, false
	}
	return svgParseEID(node.TagName().Name())
}

func (p *svgParser) parseXMLNodeChildren(parent, origin xmlNode, parentID svgNodeID, ignoreIDs bool,
	depth uint32) *xmlError {
	for _, node := range parent.Children() {
		if err := p.parseXMLNode(node, origin, parentID, ignoreIDs, depth); err != nil {
			return err
		}
	}
	return nil
}

func (p *svgParser) parseXMLNode(node, origin xmlNode, parentID svgNodeID, ignoreIDs bool,
	depth uint32) *xmlError {
	if depth > 1024 {
		return &xmlError{Kind: xmlErrNodesLimitReached}
	}

	tagName, ok := svgParseTagName(node)
	if !ok || tagName == eidStyle {
		return nil
	}

	// Treat links as groups.
	if tagName == eidA {
		tagName = eidG
	}

	nodeID, err := p.parseSVGElement(node, parentID, tagName, ignoreIDs)
	if err != nil {
		return err
	}
	switch tagName {
	case eidText:
		return p.parseSVGTextElement(node, nodeID)
	case eidUse:
		return p.parseSVGUseElement(node, origin, nodeID, depth+1)
	}
	return p.parseXMLNodeChildren(node, origin, nodeID, ignoreIDs, depth+1)
}

// parseSVGElement is parse_svg_element: appends an element with its presentation attributes, then the
// CSS rules in specificity order, then the `style` attribute declarations.
func (p *svgParser) parseSVGElement(xmlNode xmlNode, parentID svgNodeID, tagName eid,
	ignoreIDs bool) (svgNodeID, *xmlError) {
	doc := p.doc
	attrsStart := len(doc.attrs)

	// Copy presentational attributes first.
	for _, attr := range xmlNode.Attributes() {
		if ns, ok := attr.Namespace(); ok {
			switch ns {
			case svgNamespaceSVG, svgNamespaceXLink, xmlNSXMLURI:
			default:
				continue
			}
		}

		a, ok := svgParseAID(attr.Name())
		if !ok {
			continue
		}

		// During a `use` resolving, all `id` attributes must be ignored.
		// Otherwise we will get elements with duplicated id's.
		if ignoreIDs && a == aidId {
			continue
		}

		// For some reason those properties are allowed only inside a `style` attribute and CSS.
		if a == aidMixBlendMode || a == aidIsolation || a == aidFontKerning {
			continue
		}
		if a == aidImageRendering {
			switch attr.Value() {
			case "smooth", "high-quality", "crisp-edges", "pixelated":
				continue
			}
		}

		svgAppendAttribute(parentID, tagName, a, attr.Value(), false, doc)
	}

	insertAttribute := func(a aid, value string, important bool) {
		// Check that attribute already exists.
		idx := -1
		for i, at := range doc.attrs[attrsStart:] {
			if at.Name == a {
				idx = i
				break
			}
		}

		// Append an attribute as usual. It could be skipped.
		if !svgAppendAttribute(parentID, tagName, a, value, important, doc) || idx < 0 {
			return
		}
		last := len(doc.attrs) - 1
		existing := attrsStart + idx

		// An important declaration reverses the order of precedence: an existing important
		// attribute wins over any new one, while a new one replaces an existing unimportant one.
		if !doc.attrs[existing].Important {
			doc.attrs[existing], doc.attrs[last] = doc.attrs[last], doc.attrs[existing]
		}
		doc.attrs = doc.attrs[:last]
	}

	writeDeclaration := func(decl cssDeclaration) {
		imp, val := decl.Important, decl.Value
		switch decl.Name {
		case "marker":
			insertAttribute(aidMarkerStart, val, imp)
			insertAttribute(aidMarkerMid, val, imp)
			insertAttribute(aidMarkerEnd, val, imp)
		case "font":
			sh, err := stParseFontShorthand(val)
			if err != nil {
				return
			}
			// First we need to reset all values to their default.
			insertAttribute(aidFontStyle, "normal", imp)
			insertAttribute(aidFontVariant, "normal", imp)
			insertAttribute(aidFontWeight, "normal", imp)
			insertAttribute(aidFontStretch, "normal", imp)
			insertAttribute(aidLineHeight, "normal", imp)
			insertAttribute(aidFontSizeAdjust, "none", imp)
			insertAttribute(aidFontKerning, "auto", imp)
			insertAttribute(aidFontVariantCaps, "normal", imp)
			insertAttribute(aidFontVariantLigatures, "normal", imp)
			insertAttribute(aidFontVariantNumeric, "normal", imp)
			insertAttribute(aidFontVariantEastAsian, "normal", imp)
			insertAttribute(aidFontVariantPosition, "normal", imp)

			// Then, we set the properties that have been declared.
			if sh.FontStretch != "" {
				insertAttribute(aidFontStretch, sh.FontStretch, imp)
			}
			if sh.FontWeight != "" {
				insertAttribute(aidFontWeight, sh.FontWeight, imp)
			}
			if sh.FontVariant != "" {
				insertAttribute(aidFontVariant, sh.FontVariant, imp)
			}
			if sh.FontStyle != "" {
				insertAttribute(aidFontStyle, sh.FontStyle, imp)
			}
			insertAttribute(aidFontSize, sh.FontSize, imp)
			insertAttribute(aidFontFamily, sh.FontFamily, imp)
		default:
			// Parse only the presentation attributes.
			if a, ok := svgParseAID(decl.Name); ok && a.isPresentation() {
				insertAttribute(a, val, imp)
			}
		}
	}

	// Apply CSS.
	for _, rule := range p.sheet.Rules {
		if rule.Selector.Matches(svgXMLElement{xmlNode}) {
			for _, decl := range rule.Declarations {
				writeDeclaration(decl)
			}
		}
	}

	// Split a `style` attribute.
	if value, ok := xmlNode.Attribute("style"); ok {
		tok := cssNewDeclarationTokenizer(value)
		for decl, ok := tok.Next(); ok; decl, ok = tok.Next() {
			writeDeclaration(decl)
		}
	}

	if len(doc.nodes) > 1_000_000 {
		return 0, &xmlError{Kind: xmlErrNodesLimitReached}
	}

	return doc.appendNode(parentID, svgNodeData{
		kind:      svgNodeElement,
		tagName:   tagName,
		attrStart: uint32(attrsStart),
		attrEnd:   uint32(len(doc.attrs)),
	}), nil
}

// svgAppendAttribute is append_attribute; it reports whether an attribute was added.
func svgAppendAttribute(parentID svgNodeID, tagName eid, a aid, value string, important bool,
	doc *svgDocument) bool {
	switch a {
	// The `style` attribute will be split into attributes, so we don't need it.
	// No need to copy a `class` attribute since CSS were already resolved.
	case aidStyle, aidClass:
		return false
	}

	// Ignore `xlink:href` on `tspan` (which was originally `tref` or `a`),
	// because we will convert `tref` into `tspan` anyway.
	if tagName == eidTspan && a == aidHref {
		return false
	}

	if a.allowsInheritValue() && value == "inherit" {
		return svgResolveInherit(parentID, a, doc)
	}

	doc.attrs = append(doc.attrs, svgAttribute{Name: a, Value: value, Important: important})
	return true
}

func svgResolveInherit(parentID svgNodeID, a aid, doc *svgDocument) bool {
	from, found := svgNode{}, false
	if a.isInheritable() {
		// Inheritable attributes can inherit a value from any ancestor.
		for n := range doc.get(parentID).Ancestors() {
			if n.HasAttribute(a) {
				from, found = n, true
				break
			}
		}
	} else if n := doc.get(parentID); n.HasAttribute(a) {
		// Non-inheritable attributes can inherit a value only from a direct parent.
		from, found = n, true
	}
	if found {
		for _, at := range from.Attributes() {
			if at.Name == a {
				doc.attrs = append(doc.attrs, svgAttribute{Name: a, Value: at.Value, Important: at.Important})
				return true
			}
		}
	}

	// Fallback to a default value if possible.
	var value string
	switch a {
	case aidImageRendering, aidShapeRendering, aidTextRendering:
		value = "auto"
	case aidClipPath, aidFilter, aidMarkerEnd, aidMarkerMid, aidMarkerStart, aidMask, aidStroke,
		aidStrokeDasharray, aidTextDecoration:
		value = "none"
	case aidFontStretch, aidFontStyle, aidFontVariant, aidFontWeight, aidLetterSpacing,
		aidWordSpacing:
		value = "normal"
	case aidFill, aidFloodColor, aidStopColor:
		value = "black"
	case aidFillOpacity, aidFloodOpacity, aidOpacity, aidStopOpacity, aidStrokeOpacity:
		value = "1"
	case aidClipRule, aidFillRule:
		value = "nonzero"
	case aidBaselineShift:
		value = "baseline"
	case aidColorInterpolationFilters:
		value = "linearRGB"
	case aidDirection:
		value = "ltr"
	case aidDisplay:
		value = "inline"
	case aidFontSize:
		value = "medium"
	case aidOverflow:
		value = "visible"
	case aidStrokeDashoffset:
		value = "0"
	case aidStrokeLinecap:
		value = "butt"
	case aidStrokeLinejoin:
		value = "miter"
	case aidStrokeMiterlimit:
		value = "4"
	case aidStrokeWidth:
		value = "1"
	case aidTextAnchor:
		value = "start"
	case aidVisibility:
		value = "visible"
	case aidWritingMode:
		value = "lr-tb"
	default:
		return false
	}
	doc.attrs = append(doc.attrs, svgAttribute{Name: a, Value: value})
	return true
}

// svgResolveHref is resolve_href: the XML element an `xlink:href` or `href` links to.
func svgResolveHref(node xmlNode, idMap map[string]xmlNode) (xmlNode, bool) {
	link, ok := node.AttributeNS(svgNamespaceXLink, "href")
	if !ok {
		if link, ok = node.Attribute("href"); !ok {
			return xmlNode{}, false
		}
	}
	id, err := stParseIRI(link)
	if err != nil {
		return xmlNode{}, false
	}
	n, ok := idMap[string(id)]
	return n, ok
}

func (p *svgParser) parseSVGUseElement(node, origin xmlNode, parentID svgNodeID,
	depth uint32) *xmlError {
	link, ok := svgResolveHref(node, p.idMap)
	if !ok {
		return nil
	}

	// A recursive `use` is skipped.
	if link == node || link == origin {
		return nil
	}

	// Make sure we're linked to an SVG element.
	if _, ok := svgParseTagName(link); !ok {
		return nil
	}

	// Check that none of the linked node's children reference the current `use` node via another
	// `use` node, and that no child references its parent:
	//
	//	<g id="g1"><use xlink:href="#use1" id="use2"/></g>
	//	<use xlink:href="#g1" id="use1"/>
	//
	// skips `use2`, and `<g id="g1"><use xlink:href="#g1" id="use1"/></g>` skips `use1`.
	for _, linkChild := range link.Descendants()[1:] {
		if !linkChild.HasTagNameNS(svgNamespaceSVG, "use") {
			continue
		}
		if link2, ok := svgResolveHref(linkChild, p.idMap); ok && (link2 == node || link2 == link) {
			return nil
		}
	}

	return p.parseXMLNode(link, node, parentID, true, depth+1)
}

// svgResolveCSS is resolve_css: the injected style sheet first (it does not override internal ones,
// as in rsvg-convert), then every `style` element of type text/css or without a type.
func svgResolveCSS(xml *xmlDocument, injected string, hasInjected bool) *cssStyleSheet {
	sheet := cssNewStyleSheet()
	if hasInjected {
		sheet.ParseMore(injected)
	}
	for _, node := range xml.Descendants() {
		if !node.HasTagName("style") {
			continue
		}
		if t, ok := node.Attribute("type"); ok && t != "text/css" {
			continue
		}
		text, ok := node.Text()
		if !ok {
			continue
		}
		sheet.ParseMore(text)
	}
	return sheet
}

// svgXMLElement is parse.rs's XmlNode: an XML element for CSS selector matching.
type svgXMLElement struct{ n xmlNode }

func (e svgXMLElement) ParentElement() (cssElement, bool) {
	p, ok := e.n.ParentElement()
	if !ok {
		return nil, false
	}
	return svgXMLElement{p}, true
}

func (e svgXMLElement) PrevSiblingElement() (cssElement, bool) {
	p, ok := e.n.PrevSiblingElement()
	if !ok {
		return nil, false
	}
	return svgXMLElement{p}, true
}

func (e svgXMLElement) HasLocalName(name string) bool { return e.n.TagName().Name() == name }

func (e svgXMLElement) AttributeMatches(localName string, op cssAttributeOperator) bool {
	v, ok := e.n.Attribute(localName)
	return ok && op.Matches(v)
}

func (e svgXMLElement) PseudoClassMatches(class cssPseudoClass) bool {
	if class.Kind == cssPseudoFirstChild {
		_, ok := e.n.PrevSiblingElement()
		return !ok
	}
	// TODO in Rust: lang. A static SVG can ignore the other pseudo-classes.
	return false
}

// svgSetNone sets the attribute a of node id to "none".
func svgSetNone(doc *svgDocument, id svgNodeID, a aid) {
	idx, _ := doc.get(id).attributeID(a)
	doc.attrs[idx].Value = "none"
}

func svgFixRecursivePatterns(doc *svgDocument) {
	for _, a := range [...]aid{aidFill, aidStroke} {
		for {
			id, ok := svgFindRecursivePattern(a, doc)
			if !ok {
				break
			}
			svgSetNone(doc, id, a)
		}
	}
}

// svgPaintLink is `if let Ok(Paint::FuncIRI(link, _)) = Paint::from_str(value)`.
func svgPaintLink(value string) (string, bool) {
	p, err := stParsePaint(value)
	if err != nil || p.Kind != stPaintFuncIRI {
		return "", false
	}
	return p.Link, true
}

func svgFindRecursivePattern(a aid, doc *svgDocument) (svgNodeID, bool) {
	for patternNode := range doc.Root().Descendants() {
		if !patternNode.hasTagName(eidPattern) {
			continue
		}
		for node := range patternNode.Descendants() {
			value, ok := node.attrStr(a)
			if !ok {
				continue
			}
			linkID, ok := svgPaintLink(value)
			if !ok {
				continue
			}
			if linkID == patternNode.ElementID() {
				// A pattern child linking to the pattern itself is replaced with `none`, otherwise we
				// would get an endless loop.
				return node.id, true
			}
			// Check that the linked node's children don't link this pattern.
			linked, ok := doc.ElementByID(linkID)
			if !ok {
				continue
			}
			for node2 := range linked.Descendants() {
				value2, ok := node2.attrStr(a)
				if !ok {
					continue
				}
				if linkID2, ok := svgPaintLink(value2); ok && linkID2 == patternNode.ElementID() {
					return node2.id, true
				}
			}
		}
	}
	return 0, false
}

func svgFixRecursiveLinks(e eid, a aid, doc *svgDocument) {
	for {
		id, ok := svgFindRecursiveLink(e, a, doc)
		if !ok {
			return
		}
		svgSetNone(doc, id, a)
	}
}

func svgFindRecursiveLink(e eid, a aid, doc *svgDocument) (svgNodeID, bool) {
	for node := range doc.Root().Descendants() {
		if !node.hasTagName(e) {
			continue
		}
		for child := range node.Descendants() {
			link, ok := child.attrNode(a)
			if !ok {
				continue
			}
			if link == node {
				// An element child linking to the element itself is replaced with `none`.
				return child.id, true
			}
			// Check that the linked node's children don't link this element.
			for node2 := range link.Descendants() {
				if link2, ok := node2.attrNode(a); ok && link2 == node {
					return node2.id, true
				}
			}
		}
	}
	return 0, false
}

// svgFixRecursiveFeImage detects an feImage linking to an element that uses the feImage's filter:
//
//	<filter id="filter1"><feImage xlink:href="#rect1"/></filter>
//	<rect id="rect1" x="36" y="36" width="120" height="120" fill="green" filter="url(#filter1)"/>
func svgFixRecursiveFeImage(doc *svgDocument) {
	var ids []svgNodeID
	for feNode := range doc.Root().Descendants() {
		if !feNode.hasTagName(eidFeImage) {
			continue
		}
		link, ok := feNode.attrNode(aidHref)
		if !ok {
			continue
		}
		filterURI, ok := link.attrStr(aidFilter)
		if !ok {
			continue
		}
		parent, _ := feNode.Parent()
		filterID := parent.ElementID()
		parser := stNewFilterValueListParser(filterURI)
		for {
			f, ok, err := parser.Next()
			if !ok {
				break
			}
			if err == nil && f.Kind == stFilterURL && f.URL == filterID {
				ids = append(ids, link.id)
			}
		}
	}
	for _, id := range ids {
		svgSetNone(doc, id, aidFilter)
	}
}
