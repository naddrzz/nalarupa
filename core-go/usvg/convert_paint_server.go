// Port of usvg 0.47 src/parser/paint_server.rs: gradients and patterns, and the pass that turns
// objectBoundingBox units into userSpaceOnUse once the tree is converted.

package usvg

import (
	"github.com/naddrzz/nalarupa/core-go/skia"
)

// serverOrColor is ServerOrColor: a paint server, or a plain color (a degenerate gradient).
type serverOrColor struct {
	paint   Paint // when !isColor
	isColor bool
	color   Color
	opacity float32
}

// convertPaintServer is paint_server::convert; ok is false for an invalid server. Converted servers
// are cached by element id, so references share one object.
func convertPaintServer(node svgNode, state *converterState, cache *converterCache) (serverOrColor, bool) {
	// Check for an existing one.
	if paint, ok := cache.paint[node.ElementID()]; ok {
		return serverOrColor{paint: paint}, true
	}

	var res serverOrColor
	var ok bool
	tag, _ := node.TagName()
	switch tag {
	case eidLinearGradient:
		res, ok = convertLinear(node, state)
	case eidRadialGradient:
		res, ok = convertRadial(node, state)
	case eidPattern:
		res, ok = convertPattern(node, state, cache)
	default:
		panic("usvg: not a paint server") // unreachable: callers check isPaintServer
	}

	if ok && !res.isColor {
		cache.paint[node.ElementID()] = res.paint
	}
	return res, ok
}

func convertLinear(node svgNode, state *converterState) (serverOrColor, bool) {
	id, ok := newNonEmptyString(node.ElementID())
	if !ok {
		return serverOrColor{}, false
	}

	withStops, ok := findGradientWithStops(node)
	if !ok {
		return serverOrColor{}, false
	}
	stops := convertStops(withStops)
	if len(stops) < 2 {
		return stopsToColor(stops)
	}

	u := convertUnits(node, aidGradientUnits, unitsObjectBoundingBox)
	transform := node.resolveTransform(aidGradientTransform, state)

	g := &LinearGradient{
		x1: resolveNumber(node, aidX1, u, state, stLengthZero()),
		y1: resolveNumber(node, aidY1, u, state, stLengthZero()),
		x2: resolveNumber(node, aidX2, u, state, stNewLength(100, stLengthUnitPercent)),
		y2: resolveNumber(node, aidY2, u, state, stLengthZero()),
		baseGradient: BaseGradient{
			id:           id,
			units:        u,
			transform:    transform,
			spreadMethod: convertSpreadMethod(node),
			stops:        stops,
		},
	}
	return serverOrColor{paint: g}, true
}

func convertRadial(node svgNode, state *converterState) (serverOrColor, bool) {
	id, ok := newNonEmptyString(node.ElementID())
	if !ok {
		return serverOrColor{}, false
	}

	withStops, ok := findGradientWithStops(node)
	if !ok {
		return serverOrColor{}, false
	}
	stops := convertStops(withStops)
	if len(stops) < 2 {
		return stopsToColor(stops)
	}

	u := convertUnits(node, aidGradientUnits, unitsObjectBoundingBox)
	r := resolveNumber(node, aidR, u, state, stNewLength(50, stLengthUnitPercent))
	fr := resolveNumber(node, aidFr, u, state, stLengthZero())

	// 'A value of zero will cause the area to be painted as a single color using the color and opacity
	// of the last gradient stop.'
	if !isValidLengthF32(r) {
		stop := stops[len(stops)-1]
		return serverOrColor{isColor: true, color: stop.color, opacity: stop.opacity}, true
	}

	spreadMethod := convertSpreadMethod(node)
	cx := resolveNumber(node, aidCx, u, state, stNewLength(50, stLengthUnitPercent))
	cy := resolveNumber(node, aidCy, u, state, stNewLength(50, stLengthUnitPercent))
	fx := resolveNumber(node, aidFx, u, state, stNewLengthNumber(float64(cx)))
	fy := resolveNumber(node, aidFy, u, state, stNewLengthNumber(float64(cy)))
	transform := node.resolveTransform(aidGradientTransform, state)

	if _, ok := newPositiveF32(fr); !ok {
		fr = 0
	}
	g := &RadialGradient{
		cx: cx,
		cy: cy,
		r:  r,
		fx: fx,
		fy: fy,
		fr: fr,
		baseGradient: BaseGradient{
			id:           id,
			units:        u,
			transform:    transform,
			spreadMethod: spreadMethod,
			stops:        stops,
		},
	}
	return serverOrColor{paint: g}, true
}

func convertPattern(node svgNode, state *converterState, cache *converterCache) (serverOrColor, bool) {
	withChildren, ok := findPatternWithChildren(node)
	if !ok {
		return serverOrColor{}, false
	}

	id, ok := newNonEmptyString(node.ElementID())
	if !ok {
		return serverOrColor{}, false
	}

	var vb *viewBox
	n1 := resolveAttr(node, aidViewBox)
	n2 := resolveAttr(node, aidPreserveAspectRatio)
	if rect, ok := n1.parseViewbox(); ok {
		aspect, ok := n2.attrAspectRatio(aidPreserveAspectRatio)
		if !ok {
			aspect = stDefaultAspectRatio()
		}
		vb = &viewBox{rect: rect, aspect: aspect}
	}

	u := convertUnits(node, aidPatternUnits, unitsObjectBoundingBox)
	contentUnits := convertUnits(node, aidPatternContentUnits, unitsUserSpaceOnUse)

	transform := node.resolveTransform(aidPatternTransform, state)

	rect, ok := skia.NonZeroRectFromXYWH(
		resolveNumber(node, aidX, u, state, stLengthZero()),
		resolveNumber(node, aidY, u, state, stLengthZero()),
		resolveNumber(node, aidWidth, u, state, stLengthZero()),
		resolveNumber(node, aidHeight, u, state, stLengthZero()),
	)
	if !ok {
		return serverOrColor{}, false // the pattern has an invalid size
	}

	patt := &Pattern{
		id:           id,
		units:        u,
		contentUnits: contentUnits,
		transform:    transform,
		rect:         rect,
		viewBox:      vb,
		root:         newEmptyGroup(),
	}

	// The view box transform can be applied only for user space coordinates; otherwise it needs a
	// bounding box, which is unknown at this point.
	if patt.viewBox != nil && patt.units == unitsUserSpaceOnUse && patt.contentUnits == unitsUserSpaceOnUse {
		g := newEmptyGroup()
		g.transform = vb.toTransform(rect.Size())
		g.absTransform = g.transform

		convertChildren(withChildren, state, cache, &g)
		if !g.HasChildren() {
			return serverOrColor{}, false
		}
		g.calculateBoundingBoxes()
		patt.root.children = append(patt.root.children, &g)
	} else {
		convertChildren(withChildren, state, cache, &patt.root)
		if !patt.root.HasChildren() {
			return serverOrColor{}, false
		}
	}

	patt.root.calculateBoundingBoxes()
	return serverOrColor{paint: patt}, true
}

func convertSpreadMethod(node svgNode) SpreadMethod {
	m, _ := resolveAttr(node, aidSpreadMethod).attrSpreadMethod(aidSpreadMethod)
	return m
}

// convertUnits is paint_server::convert_units.
func convertUnits(node svgNode, name aid, def units) units {
	u, ok := resolveAttr(node, name).attrUnits(name)
	if !ok {
		return def
	}
	return u
}

// findGradientWithStops is find_gradient_with_stops: the first gradient in the href chain with `stop`
// children; a link to a non-gradient ends the search with nothing.
func findGradientWithStops(node svgNode) (svgNode, bool) {
	for link := range node.HrefIter() {
		tag, _ := link.TagName()
		if !tag.isGradient() {
			return svgNode{}, false
		}
		for c := range link.Children() {
			if c.hasTagName(eidStop) {
				return link, true
			}
		}
	}
	return svgNode{}, false
}

// findPatternWithChildren is find_pattern_with_children.
func findPatternWithChildren(node svgNode) (svgNode, bool) {
	for link := range node.HrefIter() {
		if !link.hasTagName(eidPattern) {
			return svgNode{}, false
		}
		if link.HasChildren() {
			return link, true
		}
	}
	return svgNode{}, false
}

// stopOffsetEpsilon is f32::EPSILON.
const stopOffsetEpsilon float32 = 1.0 / (1 << 23)

// convertStops is convert_stops: the `stop` children, with offsets clamped and made increasing.
func convertStops(grad svgNode) []Stop {
	var stops []Stop

	prevOffset := stLengthZero()
	for stop := range grad.Children() {
		if !stop.hasTagName(eidStop) {
			continue // not a gradient child
		}

		// The offset is a number or a percentage.
		l, ok := stop.attrLength(aidOffset)
		if !ok {
			l = prevOffset
		}
		var offset64 float64
		switch l.Unit {
		case stLengthUnitNone:
			offset64 = l.Number
		case stLengthUnitPercent:
			offset64 = l.Number / 100
		default:
			offset64 = prevOffset.Number
		}
		prevOffset = stNewLengthNumber(offset64)
		offset := f32Bound(0, float32(offset64), 1)

		c := stColorBlack()
		if v, ok := stop.attrStr(aidStopColor); ok {
			if v == "currentColor" {
				if cc, ok := svgFindAttr(stop, aidColor, svgNode.attrColor); ok {
					c = cc
				}
			} else if pc, err := stParseColor(v); err == nil {
				c = pc
			}
		}
		color, opacity := splitAlpha(c)

		stopOpacity, ok := stop.attrOpacity(aidStopOpacity)
		if !ok {
			stopOpacity = 1
		}
		stops = append(stops, Stop{
			offset:  normalizedClampedF32(offset),
			color:   color,
			opacity: float32(opacity * stopOpacity),
		})
	}

	// Remove the middle of three stops with equal offsets.
	if len(stops) >= 3 {
		i := 0
		for i < len(stops)-2 {
			o1, o2, o3 := stops[i].offset, stops[i+1].offset, stops[i+2].offset
			if approxEqUlpsF32(o1, o2, 4) && approxEqUlpsF32(o2, o3, 4) {
				stops = append(stops[:i+1], stops[i+2:]...)
			} else {
				i++
			}
		}
	}

	// Shift a zero offset that follows another one.
	if len(stops) >= 2 {
		for i := 0; i < len(stops)-1; i++ {
			o1, o2 := stops[i].offset, stops[i+1].offset
			if approxEqUlpsF32(o1, 0, 4) && approxEqUlpsF32(o2, 0, 4) {
				stops[i+1].offset = normalizedClampedF32(o1 + stopOffsetEpsilon)
			}
		}
	}

	// Each offset must be larger than the previous one: make the previous one a bit smaller.
	for i := 1; i < len(stops); i++ {
		o1, o2 := stops[i-1].offset, stops[i].offset
		if o1 > o2 || approxEqUlpsF32(o1, o2, 4) {
			stops[i-1].offset = normalizedClampedF32(o1 - stopOffsetEpsilon)
			stops[i].offset = normalizedClampedF32(o1)
		}
	}

	return stops
}

// resolveNumber is paint_server::resolve_number: the attribute, taken through the href chain.
func resolveNumber(node svgNode, name aid, u units, state *converterState, def stLength) float32 {
	return resolveAttr(node, name).convertLength(name, u, state, def)
}

// resolveAttr is resolve_attr: node, or the element in its href chain the attribute is taken from.
func resolveAttr(node svgNode, name aid) svgNode {
	if node.HasAttribute(name) {
		return node
	}
	tag, _ := node.TagName()
	switch tag {
	case eidLinearGradient:
		return resolveGradientAttr(node, name, false)
	case eidRadialGradient:
		return resolveGradientAttr(node, name, true)
	case eidPattern:
		return resolveSameTagAttr(node, name, eidPattern)
	case eidFilter:
		return resolveSameTagAttr(node, name, eidFilter)
	}
	return node
}

// resolveGradientAttr is resolve_lg_attr (radial false) and resolve_rg_attr (radial true):
// coordinates come only from a gradient of the same kind, the rest from any gradient.
func resolveGradientAttr(node svgNode, name aid, radial bool) svgNode {
	for link := range node.HrefIter() {
		tag, ok := link.TagName()
		if !ok {
			return node
		}
		allowed := false
		switch name {
		case aidX1, aidY1, aidX2, aidY2:
			allowed = !radial && tag == eidLinearGradient
		case aidCx, aidCy, aidR, aidFx, aidFy:
			allowed = radial && tag == eidRadialGradient
		case aidGradientUnits, aidSpreadMethod, aidGradientTransform:
			allowed = tag == eidLinearGradient || tag == eidRadialGradient
		}
		if !allowed {
			break
		}
		if link.HasAttribute(name) {
			return link
		}
	}
	return node
}

// resolveSameTagAttr is resolve_pattern_attr and resolve_filter_attr.
func resolveSameTagAttr(node svgNode, name aid, tag eid) svgNode {
	for link := range node.HrefIter() {
		t, ok := link.TagName()
		if !ok {
			return node
		}
		if t != tag {
			break
		}
		if link.HasAttribute(name) {
			return link
		}
	}
	return node
}

func stopsToColor(stops []Stop) (serverOrColor, bool) {
	if len(stops) == 0 {
		return serverOrColor{}, false
	}
	return serverOrColor{isColor: true, color: stops[0].color, opacity: stops[0].opacity}, true
}

// updatePaintServers is update_paint_servers: it replaces context fills and strokes linked to a use
// node with their actual values, and converts objectBoundingBox units to userSpaceOnUse. A nil rect
// pointer is None.
func updatePaintServers(group *Group, contextTransform skia.Transform, contextBBox, textBBox *skia.Rect,
	cache *converterCache) {
	for _, child := range group.children {
		// A use node supplies the context transform and bbox.
		ts, bb := contextTransform, contextBBox
		if group.isContextElement {
			r := group.boundingBox
			ts, bb = group.absTransform, &r
		}
		nodeToUserCoordinates(child, ts, bb, textBBox, cache)
	}
}

// nodeToUserCoordinates is node_to_user_coordinates. Gradients and patterns keep their units while
// parsing, since a text bounding box is unknown until the text is laid out; this pass replaces them
// with userSpaceOnUse ones.
func nodeToUserCoordinates(node Node, contextTransform skia.Transform, contextBBox, textBBox *skia.Rect,
	cache *converterCache) {
	switch n := node.(type) {
	case *Group:
		// Clip paths cannot have paint servers.
		if mask := n.mask; mask != nil && cache.getMut(mask) {
			updatePaintServers(&mask.root, contextTransform, contextBBox, nil, cache)
			if sub := mask.mask; sub != nil && cache.getMut(sub) {
				updatePaintServers(&sub.root, contextTransform, contextBBox, nil, cache)
			}
		}
		for _, fl := range n.filters {
			if !cache.getMut(fl) {
				continue
			}
			for i := range fl.primitives {
				if image, ok := fl.primitives[i].kind.(*FeImage); ok {
					updatePaintServers(&image.root, contextTransform, contextBBox, nil, cache)
				}
			}
		}
		updatePaintServers(n, contextTransform, contextBBox, textBBox, cache)
	case *Path:
		// Paths inside Text.flattened use the text's bounding box instead of their own.
		bbox := n.boundingBox
		if textBBox != nil {
			bbox = *textBBox
		}
		n.fill = processFill(n.fill, n.absTransform, contextTransform, contextBBox, bbox, cache)
		n.stroke = processStroke(n.stroke, n.absTransform, contextTransform, contextBBox, bbox, cache)
	case *Image:
		if svg, ok := n.kind.(ImageKindSVG); ok {
			updatePaintServers(&svg.Tree.root, contextTransform, contextBBox, nil, cache)
		}
	case *Text:
		// A tspan has no bbox of its own and uses the text's: for the chunks' styles, the laid-out
		// spans' and the flattened paths'.
		bbox := n.boundingBox
		// Decoration styles may be shared between spans (Rust clones them): replace, never modify.
		processDeco := func(d **TextDecorationStyle) {
			if *d != nil {
				*d = &TextDecorationStyle{
					fill:   processFill((*d).fill, skia.Identity(), skia.Identity(), nil, bbox, cache),
					stroke: processStroke((*d).stroke, skia.Identity(), skia.Identity(), nil, bbox, cache),
				}
			}
		}
		for ci := range n.chunks {
			for si := range n.chunks[ci].spans {
				sp := &n.chunks[ci].spans[si]
				sp.fill = processFill(sp.fill, n.absTransform, contextTransform, contextBBox, bbox, cache)
				sp.stroke = processStroke(sp.stroke, n.absTransform, contextTransform, contextBBox, bbox, cache)
				processDeco(&sp.decoration.underline)
				processDeco(&sp.decoration.overline)
				processDeco(&sp.decoration.lineThrough)
			}
		}
		processPath := func(p *Path) {
			if p != nil {
				p.fill = processFill(p.fill, n.absTransform, contextTransform, contextBBox, bbox, cache)
				p.stroke = processStroke(p.stroke, n.absTransform, contextTransform, contextBBox, bbox, cache)
			}
		}
		for si := range n.layouted {
			sp := &n.layouted[si]
			sp.fill = processFill(sp.fill, n.absTransform, contextTransform, contextBBox, bbox, cache)
			sp.stroke = processStroke(sp.stroke, n.absTransform, contextTransform, contextBBox, bbox, cache)
			processPath(sp.overline)
			processPath(sp.underline)
			processPath(sp.lineThrough)
		}
		if n.flattened != nil {
			updatePaintServers(n.flattened, contextTransform, contextBBox, &bbox, cache)
		}
	}
}

// processFill is process_fill; it returns the updated fill, nil when it can no longer paint. The fill
// is copied, never modified in place.
func processFill(fill *Fill, pathTransform, contextTransform skia.Transform, contextBBox *skia.Rect,
	bbox skia.Rect, cache *converterCache) *Fill {
	if fill == nil {
		return nil
	}
	f := *fill
	// Path context elements (markers) are resolved already; only use nodes are left.
	if !processPaint(&f.paint, f.contextElement.kind == contextElementUseNode, contextTransform, contextBBox,
		pathTransform, bbox, cache) {
		cache.decRef(f.paint)
		return nil
	}
	return &f
}

// processStroke is process_stroke, see processFill.
func processStroke(stroke *Stroke, pathTransform, contextTransform skia.Transform, contextBBox *skia.Rect,
	bbox skia.Rect, cache *converterCache) *Stroke {
	if stroke == nil {
		return nil
	}
	s := *stroke
	if !processPaint(&s.paint, s.contextElement.kind == contextElementUseNode, contextTransform, contextBBox,
		pathTransform, bbox, cache) {
		cache.decRef(s.paint)
		return nil
	}
	return &s
}

// processContextPaint is process_context_paint: a copy of the paint server whose transform cancels
// the transforms between the context element (A) and the path (C): C x B^-1 with B^-1 = (A^-1 x C)^-1.
func processContextPaint(paint *Paint, contextTransform, pathTransform skia.Transform, cache *converterCache) {
	inv, ok := contextTransform.Invert()
	if !ok {
		return
	}
	revTransform, ok := inv.PreConcat(pathTransform).Invert()
	if !ok {
		return
	}

	var replacement Paint
	switch p := (*paint).(type) {
	case *LinearGradient:
		replacement = &LinearGradient{
			x1: p.x1, y1: p.y1, x2: p.x2, y2: p.y2,
			baseGradient: BaseGradient{
				id:           cache.genLinearGradientID(),
				units:        p.units,
				transform:    p.transform.PostConcat(revTransform),
				spreadMethod: p.spreadMethod,
				stops:        p.stops,
			},
		}
	case *RadialGradient:
		replacement = &RadialGradient{
			cx: p.cx, cy: p.cy, r: p.r, fx: p.fx, fy: p.fy, fr: p.fr,
			baseGradient: BaseGradient{
				id:           cache.genRadialGradientID(),
				units:        p.units,
				transform:    p.transform.PostConcat(revTransform),
				spreadMethod: p.spreadMethod,
				stops:        p.stops,
			},
		}
	case *Pattern:
		transform := p.transform.PostConcat(revTransform)
		replacement = &Pattern{
			id:           cache.genPatternID(),
			units:        p.units,
			contentUnits: p.contentUnits,
			transform:    transform,
			rect:         p.rect,
			viewBox:      p.viewBox,
			root:         cache.cloneGroup(&p.root),
		}
	default:
		return // a color
	}
	cache.replacePaint(paint, replacement)
}

// processPaint is process_paint; false means the paint cannot be used (objectBoundingBox units with
// no bbox).
func processPaint(paint *Paint, hasContext bool, contextTransform skia.Transform, contextBBox *skia.Rect,
	pathTransform skia.Transform, bbox skia.Rect, cache *converterCache) bool {
	if paintUnits(*paint) == unitsObjectBoundingBox || paintContentUnits(*paint) == unitsObjectBoundingBox {
		if hasContext {
			if contextBBox == nil {
				return false
			}
			bbox = *contextBBox
		}
		if !paintToUserCoordinates(paint, bbox, cache) {
			return false
		}
	}

	if patt, ok := (*paint).(*Pattern); ok && cache.getMut(patt) {
		updatePaintServers(&patt.root, skia.Identity(), nil, nil, cache)
	}

	if hasContext {
		processContextPaint(paint, contextTransform, pathTransform, cache)
	}
	return true
}

// paintToUserCoordinates is Paint::to_user_coordinates; false for a zero-sized bbox. An unshared paint
// server is updated in place, keeping its id; a shared one is replaced by a copy with a new id.
func paintToUserCoordinates(paint *Paint, bbox skia.Rect, cache *converterCache) bool {
	nzBBox, ok := bbox.ToNonZeroRect()
	if !ok {
		return false // gradients and patterns on zero-sized shapes are not allowed
	}

	switch p := (*paint).(type) {
	case *LinearGradient:
		transform := p.transform.PostConcat(skia.TransformFromBBox(nzBBox))
		if cache.getMut(p) {
			p.transform = transform
			p.units = unitsUserSpaceOnUse
		} else {
			cache.replacePaint(paint, &LinearGradient{
				x1: p.x1, y1: p.y1, x2: p.x2, y2: p.y2,
				baseGradient: BaseGradient{
					id:           cache.genLinearGradientID(),
					units:        unitsUserSpaceOnUse,
					transform:    transform,
					spreadMethod: p.spreadMethod,
					stops:        p.stops,
				},
			})
		}
	case *RadialGradient:
		transform := p.transform.PostConcat(skia.TransformFromBBox(nzBBox))
		if cache.getMut(p) {
			p.transform = transform
			p.units = unitsUserSpaceOnUse
		} else {
			cache.replacePaint(paint, &RadialGradient{
				cx: p.cx, cy: p.cy, r: p.r, fx: p.fx, fy: p.fy, fr: p.fr,
				baseGradient: BaseGradient{
					id:           cache.genRadialGradientID(),
					units:        unitsUserSpaceOnUse,
					transform:    transform,
					spreadMethod: p.spreadMethod,
					stops:        p.stops,
				},
			})
		}
	case *Pattern:
		rect := p.rect
		if p.units == unitsObjectBoundingBox {
			rect = p.rect.BBoxTransform(nzBBox)
		}

		if cache.getMut(p) {
			p.rect = rect
			p.units = unitsUserSpaceOnUse
			if p.contentUnits == unitsObjectBoundingBox && p.viewBox == nil {
				// No need to shift patterns.
				pushPatternTransform(&p.root, skia.TransformFromScale(nzBBox.Width(), nzBBox.Height()))
			}
			if p.viewBox != nil {
				pushPatternTransform(&p.root, p.viewBox.toTransform(rect.Size()))
			}
			p.contentUnits = unitsUserSpaceOnUse
		} else {
			root := cache.cloneGroup(&p.root)
			if p.contentUnits == unitsObjectBoundingBox && p.viewBox == nil {
				// No need to shift patterns.
				pushPatternTransform(&root, skia.TransformFromScale(nzBBox.Width(), nzBBox.Height()))
			}
			if p.viewBox != nil {
				pushPatternTransform(&root, p.viewBox.toTransform(rect.Size()))
			}
			cache.replacePaint(paint, &Pattern{
				id:           cache.genPatternID(),
				units:        unitsUserSpaceOnUse,
				contentUnits: unitsUserSpaceOnUse,
				transform:    p.transform,
				rect:         rect,
				viewBox:      p.viewBox,
				root:         root,
			})
		}
	}
	return true
}

// pushPatternTransform moves root's content into a child group with transform. Like Rust, it does not
// update the descendants' abs transforms.
func pushPatternTransform(root *Group, transform skia.Transform) {
	g := *root
	*root = newEmptyGroup()
	g.transform = transform
	g.absTransform = transform
	root.children = append(root.children, &g)
	root.calculateBoundingBoxes()
}

// paintUnits is Paint::units.
func paintUnits(p Paint) units {
	switch p := p.(type) {
	case *LinearGradient:
		return p.units
	case *RadialGradient:
		return p.units
	case *Pattern:
		return p.units
	}
	return unitsUserSpaceOnUse
}

// paintContentUnits is Paint::content_units.
func paintContentUnits(p Paint) units {
	if p, ok := p.(*Pattern); ok {
		return p.contentUnits
	}
	return unitsUserSpaceOnUse
}
