// Port of usvg 0.47 src/parser/style.rs. The LineCap, LineJoin and FillRule FromValue impls are
// svgParseLineCap, svgParseLineJoin and svgParseFillRule in svgtree.go.

package usvg

import "math"

// resolveFill is resolve_fill: nil is no fill.
func resolveFill(node svgNode, hasBBox bool, state *converterState, cache *converterCache) *Fill {
	if state.hasParentClipPath {
		// A `clipPath` child can be filled only with black.
		rule, _ := svgFindAttr(node, aidClipRule, svgNode.attrFillRule)
		return &Fill{paint: ColorBlack(), opacity: 1, rule: rule}
	}

	subOpacity := float32(1)
	var paint Paint = ColorBlack()
	var ce contextElement
	if n, ok := findAncestorWith(node, aidFill); ok {
		value, ok := n.attrStr(aidFill)
		if !ok {
			return nil
		}
		paint, ce, ok = convertPaint(node, value, aidFill, hasBBox, state, &subOpacity, cache)
		if !ok {
			return nil
		}
	}

	fillOpacity, ok := svgFindAttr(node, aidFillOpacity, svgNode.attrOpacity)
	if !ok {
		fillOpacity = 1
	}
	rule, _ := svgFindAttr(node, aidFillRule, svgNode.attrFillRule)
	return &Fill{paint: paint, opacity: float32(subOpacity * fillOpacity), rule: rule, contextElement: ce}
}

// resolveStroke is resolve_stroke: nil is no stroke.
func resolveStroke(node svgNode, hasBBox bool, state *converterState, cache *converterCache) *Stroke {
	if state.hasParentClipPath {
		// A `clipPath` child cannot be stroked.
		return nil
	}

	subOpacity := float32(1)
	n, ok := findAncestorWith(node, aidStroke)
	if !ok {
		return nil
	}
	value, ok := n.attrStr(aidStroke)
	if !ok {
		return nil
	}
	paint, ce, ok := convertPaint(node, value, aidStroke, hasBBox, state, &subOpacity, cache)
	if !ok {
		return nil
	}

	width, ok := node.resolveValidLength(aidStrokeWidth, state, 1)
	if !ok {
		return nil
	}

	// Must be at least 1.
	miterlimit, ok := svgFindAttr(node, aidStrokeMiterlimit, svgNode.attrF32)
	if !ok {
		miterlimit = 4
	}
	if miterlimit < 1 {
		miterlimit = 1
	}

	strokeOpacity, ok := svgFindAttr(node, aidStrokeOpacity, svgNode.attrOpacity)
	if !ok {
		strokeOpacity = 1
	}
	linecap, _ := svgFindAttr(node, aidStrokeLinecap, svgNode.attrLineCap)
	linejoin, _ := svgFindAttr(node, aidStrokeLinejoin, svgNode.attrLineJoin)

	return &Stroke{
		paint:          paint,
		dasharray:      convDasharray(node, state),
		dashoffset:     node.resolveLength(aidStrokeDashoffset, state, 0),
		miterlimit:     NewStrokeMiterlimit(miterlimit),
		opacity:        float32(subOpacity * strokeOpacity),
		width:          width,
		linecap:        linecap,
		linejoin:       linejoin,
		contextElement: ce,
	}
}

// findAncestorWith is `node.ancestors().find(|n| n.has_attribute(a))`.
func findAncestorWith(node svgNode, a aid) (svgNode, bool) {
	for n := range node.Ancestors() {
		if n.HasAttribute(a) {
			return n, true
		}
	}
	return svgNode{}, false
}

// convertPaint is convert_paint; ok is false for no paint. opacity receives the color's alpha.
func convertPaint(node svgNode, value string, a aid, hasBBox bool, state *converterState, opacity *float32,
	cache *converterCache) (Paint, contextElement, bool) {
	paint, err := stParsePaint(value)
	if err != nil {
		if a != aidFill {
			return nil, contextElement{}, false // an unparsable stroke is no stroke
		}
		paint = stPaint{Kind: stPaintColor, Color: stColorBlack()} // an unparsable fill is black
	}

	switch paint.Kind {
	case stPaintContextFill:
		if state.contextElement != nil && state.contextElement.fill != nil {
			f := state.contextElement.fill
			return f.paint, f.contextElement, true
		}
		return nil, contextElement{}, false
	case stPaintContextStroke:
		if state.contextElement != nil && state.contextElement.stroke != nil {
			s := state.contextElement.stroke
			return s.paint, s.contextElement, true
		}
		return nil, contextElement{}, false
	case stPaintCurrentColor:
		c, ok := svgFindAttr(node, aidColor, svgNode.attrColor)
		if !ok {
			c = stColorBlack()
		}
		color, alpha := splitAlpha(c)
		*opacity = alpha
		return color, contextElement{}, true
	case stPaintColor:
		color, alpha := splitAlpha(paint.Color)
		*opacity = alpha
		return color, contextElement{}, true
	case stPaintFuncIRI:
		link, ok := node.Document().ElementByID(paint.Link)
		if !ok {
			p, ok := fromFallback(node, paint, opacity)
			return p, contextElement{}, ok
		}
		tag, _ := link.TagName()
		if !tag.isPaintServer() {
			return nil, contextElement{}, false // this element cannot paint a shape
		}
		server, ok := convertPaintServer(link, state, cache)
		if !ok {
			p, ok := fromFallback(node, paint, opacity)
			return p, contextElement{}, ok
		}
		if server.isColor {
			*opacity = server.opacity
			return server.color, contextElement{}, true
		}
		// A paint server with objectBoundingBox units can paint only a shape that has a bbox (SVG
		// spec 7.11).
		if !hasBBox && paintUnits(server.paint) == unitsObjectBoundingBox {
			p, ok := fromFallback(node, paint, opacity)
			return p, contextElement{}, ok
		}
		return server.paint, contextElement{}, true
	}
	// None, and Inherit, which svgtree has resolved already.
	return nil, contextElement{}, false
}

// fromFallback is from_fallback: the FuncIRI paint's fallback.
func fromFallback(node svgNode, paint stPaint, opacity *float32) (Paint, bool) {
	if !paint.HasFallback {
		return nil, false
	}
	switch paint.Fallback.Kind {
	case stPaintFallbackCurrentColor:
		c, ok := svgFindAttr(node, aidColor, svgNode.attrColor)
		if !ok {
			c = stColorBlack()
		}
		color, alpha := splitAlpha(c)
		*opacity = alpha
		return color, true
	case stPaintFallbackColor:
		color, alpha := splitAlpha(paint.Fallback.Color)
		*opacity = alpha
		return color, true
	}
	return nil, false
}

// convDasharray is conv_dasharray: `stroke-dasharray` prepared as SVG 1.1 says. nil is None.
func convDasharray(node svgNode, state *converterState) []float32 {
	n, ok := findAncestorWith(node, aidStrokeDasharray)
	if !ok {
		return nil
	}
	list, ok := convertList(n, aidStrokeDasharray, state)
	if !ok {
		return nil
	}

	// A negative value is an error.
	for _, v := range list {
		if math.Float32bits(v)>>31 != 0 { // is_sign_negative
			return nil
		}
	}

	// A zero sum is rendered as none.
	var sum float32
	for _, v := range list {
		sum += v
	}
	if approxEqUlpsF32(sum, 0, 4) {
		return nil
	}

	// An odd number of values is repeated to make it even.
	if len(list)%2 != 0 {
		return append(append(make([]float32, 0, 2*len(list)), list...), list...)
	}
	return list
}
