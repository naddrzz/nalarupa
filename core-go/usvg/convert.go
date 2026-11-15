// Port of usvg 0.47 src/parser/converter.rs: the svgtree document to Tree conversion.

package usvg

import (
	"strconv"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// fillStroke is the Option<(Option<Fill>, Option<Stroke>)> payload of State::context_element; a nil
// field is None. The pointed-to styles are never modified.
type fillStroke struct {
	fill   *Fill
	stroke *Stroke
}

// converterState is converter.rs's State. Copy it by value to clone; parentMarkers must then be copied
// before it is appended to (see withMarker).
type converterState struct {
	parentClipPath    svgNode
	hasParentClipPath bool
	parentMarkers     []svgNode
	// contextElement holds the resolved fill and stroke of a use node, or of a path element for
	// markers; nil is None.
	contextElement *fillStroke
	feImageLink    bool
	// viewBox is the view box of the parent SVG element.
	viewBox skia.NonZeroRect
	// useSize is the size of the parent `use` element, used only to size a nested `svg`. Width and
	// height can be set independently.
	useW, useH       float32
	hasUseW, hasUseH bool
	opt              *Options
}

// withMarker is a clone of s with marker pushed onto parentMarkers.
func (s converterState) withMarker(marker svgNode) converterState {
	s.parentMarkers = append(append([]svgNode(nil), s.parentMarkers...), marker)
	return s
}

// converterCache is converter.rs's Cache. Rust keys allIDs by a
// SipHash of each id; the port keeps the ids themselves. Rust never iterates its maps, so their order
// cannot reach the output.
type converterCache struct {
	clipPaths map[string]*ClipPath
	masks     map[string]*Mask
	filters   map[string]*Filter
	paint     map[string]Paint

	// Used for ID generation.
	allIDs              map[string]struct{}
	linearGradientIndex int
	radialGradientIndex int
	patternIndex        int
	clipPathIndex       int
	maskIndex           int
	filterIndex         int
	imageIndex          int

	// rc emulates Arc strong counts for Arc::get_mut, see refcount.go.
	rc map[any]int

	// The font database text resolves against, and the glyph caches.
	fontdb      *text.Database
	outlines    map[glyphKey]*skia.Path
	glyphImages map[glyphKey][2]bool
	opsz        map[text.FaceID]bool
}

func newConverterCache() *converterCache {
	return &converterCache{
		clipPaths: map[string]*ClipPath{},
		masks:     map[string]*Mask{},
		filters:   map[string]*Filter{},
		paint:     map[string]Paint{},
		allIDs:    map[string]struct{}{},
		rc:        map[any]int{},

		outlines:    map[glyphKey]*skia.Path{},
		glyphImages: map[glyphKey][2]bool{},
		opsz:        map[text.FaceID]bool{},
	}
}

// genID returns prefix+N for the next N that no element id uses.
func (c *converterCache) genID(prefix string, index *int) string {
	for {
		*index++
		id := prefix + strconv.Itoa(*index)
		if _, ok := c.allIDs[id]; !ok {
			return id
		}
	}
}

func (c *converterCache) genLinearGradientID() string {
	return c.genID("linearGradient", &c.linearGradientIndex)
}
func (c *converterCache) genRadialGradientID() string {
	return c.genID("radialGradient", &c.radialGradientIndex)
}
func (c *converterCache) genPatternID() string  { return c.genID("pattern", &c.patternIndex) }
func (c *converterCache) genClipPathID() string { return c.genID("clipPath", &c.clipPathIndex) }
func (c *converterCache) genMaskID() string     { return c.genID("mask", &c.maskIndex) }
func (c *converterCache) genFilterID() string   { return c.genID("filter", &c.filterIndex) }
func (c *converterCache) genImageID() string    { return c.genID("image", &c.imageIndex) }

// convertLength is SvgNode::convert_length: the attribute (or def) in user units.
func (n svgNode) convertLength(a aid, objectUnits units, state *converterState, def stLength) float32 {
	l, ok := n.attrLength(a)
	if !ok {
		l = def
	}
	return convertLength(l, n, a, objectUnits, state)
}

// convertUserLength is SvgNode::convert_user_length.
func (n svgNode) convertUserLength(a aid, state *converterState, def stLength) float32 {
	return n.convertLength(a, unitsUserSpaceOnUse, state, def)
}

// parseViewbox is SvgNode::parse_viewbox.
func (n svgNode) parseViewbox() (skia.NonZeroRect, bool) {
	vb, ok := n.attrViewBox(aidViewBox)
	if !ok {
		return skia.NonZeroRect{}, false
	}
	return skia.NonZeroRectFromXYWH(float32(vb.X), float32(vb.Y), float32(vb.W), float32(vb.H))
}

// resolveLength is SvgNode::resolve_length: the closest ancestor's (or own) attribute in user units,
// or def. Not for baseline-shift or font-size.
func (n svgNode) resolveLength(a aid, state *converterState, def float32) float32 {
	for m := range n.Ancestors() {
		if m.HasAttribute(a) {
			if l, ok := m.attrLength(a); ok {
				return convertUserLength(l, m, a, state)
			}
			break
		}
	}
	return def
}

// resolveValidLength is SvgNode::resolve_valid_length: resolveLength when finite and positive.
func (n svgNode) resolveValidLength(a aid, state *converterState, def float32) (float32, bool) {
	return newNonZeroPositiveF32(n.resolveLength(a, state, def))
}

// tryConvertLength is SvgNode::try_convert_length.
func (n svgNode) tryConvertLength(a aid, objectUnits units, state *converterState) (float32, bool) {
	l, ok := n.attrLength(a)
	if !ok {
		return 0, false
	}
	return convertLength(l, n, a, objectUnits, state), true
}

// hasValidTransform is SvgNode::has_valid_transform: attrTransform cannot be used, since it always
// gives a valid transform.
func (n svgNode) hasValidTransform(a aid) bool {
	attr, ok := n.attrStr(a)
	if !ok {
		return true
	}
	ts, err := stParseTransform(attr)
	if err != nil {
		return true
	}
	return skia.TransformFromRow(float32(ts.A), float32(ts.B), float32(ts.C), float32(ts.D),
		float32(ts.E), float32(ts.F)).IsValid()
}

// isVisibleElement is SvgNode::is_visible_element.
func (n svgNode) isVisibleElement(opt *Options) bool {
	if d, ok := n.attrStr(aidDisplay); ok && d == "none" {
		return false
	}
	return n.hasValidTransform(aidTransform) && isConditionPassed(n, opt)
}

// splitAlpha is SvgColorExt::split_alpha.
func splitAlpha(c stColor) (Color, float32) {
	return NewColorRGB(c.Red, c.Green, c.Blue), float32(c.Alpha) / 255
}

// convertDoc is convert_doc: an invisible root `svg` gives an empty tree, an invalid size
// ErrInvalidSize.
func convertDoc(svgDoc *svgDocument, opt *Options) (*Tree, error) {
	svg := svgDoc.RootElement()
	size, restoreViewbox, ok := resolveSVGSize(svg, opt)
	if !ok {
		return nil, &Error{Kind: ErrInvalidSize}
	}
	vbRect, ok := svg.parseViewbox()
	if !ok {
		vbRect = size.ToNonZeroRect(0, 0)
	}
	aspect, ok := svg.attrAspectRatio(aidPreserveAspectRatio)
	if !ok {
		aspect = stDefaultAspectRatio()
	}
	vb := viewBox{rect: vbRect, aspect: aspect}

	var backgroundColor stColor
	hasBackground := false
	if s, ok := svg.attrStr(aidBackgroundColor); ok {
		if paint, err := stParsePaint(s); err == nil && paint.Kind == stPaintColor {
			backgroundColor, hasBackground = paint.Color, true
		}
	}

	fontdb := opt.FontDB
	if fontdb == nil {
		fontdb = text.NewDatabase()
	}
	tree := &Tree{size: size, root: newEmptyGroup(), fontdb: fontdb}

	if !svg.isVisibleElement(opt) {
		return tree, nil
	}

	state := &converterState{viewBox: vb.rect, opt: opt}
	cache := newConverterCache()
	cache.fontdb = fontdb

	for node := range svgDoc.Descendants() {
		if tag, ok := node.TagName(); ok {
			switch tag {
			case eidClipPath, eidFilter, eidLinearGradient, eidMask, eidPattern, eidRadialGradient, eidImage:
				if id := node.ElementID(); id != "" {
					cache.allIDs[id] = struct{}{}
				}
			}
		}
	}

	rootTS := vb.toTransform(tree.size)
	if rootTS.IsIdentity() && !hasBackground {
		convertChildren(svgDoc.Root(), state, cache, &tree.root)
	} else {
		g := newEmptyGroup()
		if hasBackground {
			if path, ok := backgroundPath(backgroundColor, vb.rect.ToRect()); ok {
				g.children = append(g.children, path)
			}
		}
		g.transform = rootTS
		g.absTransform = rootTS
		convertChildren(svgDoc.Root(), state, cache, &g)
		g.calculateBoundingBoxes()
		tree.root.children = append(tree.root.children, &g)
	}

	// Clear the cache so that every Arc has a single strong reference.
	clear(cache.clipPaths)
	clear(cache.masks)
	clear(cache.filters)
	clear(cache.paint)

	cache.rcInit(tree)
	updatePaintServers(&tree.root, skia.Identity(), nil, nil, cache)
	tree.collectPaintServers()
	tree.clipPaths = tree.root.collectClipPaths(tree.clipPaths)
	tree.masks = tree.root.collectMasks(tree.masks)
	tree.filters = tree.root.collectFilters(tree.filters)
	tree.root.calculateBoundingBoxes()

	if restoreViewbox {
		calculateSVGBBox(tree)
	}
	return tree, nil
}

// backgroundPath is background_path: a rect filled with the `background-color`.
func backgroundPath(c stColor, area skia.Rect) (*Path, bool) {
	opacity, ok := newNormalizedF32(float32(c.Alpha) / 255)
	if !ok {
		return nil, false
	}
	fill := Fill{paint: NewColorRGB(c.Red, c.Green, c.Blue), opacity: opacity, rule: FillRuleNonZero}
	path, ok := newSimplePath(skia.PathFromRect(area))
	if !ok {
		return nil, false
	}
	path.fill = &fill
	return path, true
}

// resolveSVGSize is resolve_svg_size: the size and whether the view box must be computed from the
// content afterwards (percentage width or height without a viewBox). As in Rust, the lengths it
// rescales by DefaultSize are not used again: width and height are re-read from the attributes.
func resolveSVGSize(svg svgNode, opt *Options) (skia.Size, bool, bool) {
	vb100, _ := skia.NonZeroRectFromXYWH(0, 0, 100, 100)
	state := &converterState{viewBox: vb100, opt: opt}

	def := stNewLength(100, stLengthUnitPercent)
	width, ok := svg.attrLength(aidWidth)
	if !ok {
		width = def
	}
	height, ok := svg.attrLength(aidHeight)
	if !ok {
		height = def
	}

	viewBox, hasViewBox := svg.parseViewbox()

	restoreViewbox := false
	if (width.Unit == stLengthUnitPercent || height.Unit == stLengthUnitPercent) && !hasViewBox {
		// Apply the percentages to the fallback size.
		if width.Unit == stLengthUnitPercent {
			width = stNewLength(float64(width.Number/100)*float64(opt.DefaultSize.Width()), stLengthUnitNone)
		}
		if height.Unit == stLengthUnitPercent {
			height = stNewLength(float64(height.Number/100)*float64(opt.DefaultSize.Height()), stLengthUnitNone)
		}
		restoreViewbox = true
	}

	var size skia.Size
	if hasViewBox {
		state.viewBox = viewBox
		var w, h float32
		if width.Unit == stLengthUnitPercent {
			w = float32(viewBox.Width() * float32(float32(width.Number)/100))
		} else {
			w = svg.convertUserLength(aidWidth, state, def)
		}
		if height.Unit == stLengthUnitPercent {
			h = float32(viewBox.Height() * float32(float32(height.Number)/100))
		} else {
			h = svg.convertUserLength(aidHeight, state, def)
		}
		size, ok = skia.SizeFromWH(w, h)
	} else {
		size, ok = skia.SizeFromWH(svg.convertUserLength(aidWidth, state, def),
			svg.convertUserLength(aidHeight, state, def))
	}
	return size, restoreViewbox, ok
}

// calculateSVGBBox is calculate_svg_bbox: the size from the content's bounding box, for an SVG whose
// size and view box were not set.
func calculateSVGBBox(tree *Tree) {
	bbox := tree.root.absBoundingBox
	if size, ok := skia.SizeFromWH(bbox.Right(), bbox.Bottom()); ok {
		tree.size = size
	}
}

// convertChildren is convert_children.
func convertChildren(parentNode svgNode, state *converterState, cache *converterCache, parent *Group) {
	for node := range parentNode.Children() {
		convertElement(node, state, cache, parent)
	}
}

// convertElement is convert_element.
func convertElement(node svgNode, state *converterState, cache *converterCache, parent *Group) {
	tag, ok := node.TagName()
	if !ok {
		return
	}
	if !tag.isGraphic() && tag != eidG && tag != eidSwitch && tag != eidSvg {
		return
	}
	if !node.isVisibleElement(state.opt) {
		return
	}
	if tag == eidUse {
		convertUse(node, state, cache, parent)
		return
	}
	if tag == eidSwitch {
		convertSwitch(node, state, cache, parent)
		return
	}
	if g := convertGroup(node, state, false, cache, parent, func(cache *converterCache, g *Group) {
		convertElementImpl(tag, node, state, cache, g)
	}); g != nil {
		parent.children = append(parent.children, g)
	}
}

func convertElementImpl(tag eid, node svgNode, state *converterState, cache *converterCache, parent *Group) {
	switch tag {
	case eidRect, eidCircle, eidEllipse, eidLine, eidPolyline, eidPolygon, eidPath:
		if path, ok := convertShape(node, state); ok {
			convertPath(node, path, state, cache, parent)
		}
	case eidImage:
		convertImage(node, state, cache, parent)
	case eidText:
		convertText(node, state, cache, parent)
	case eidSvg:
		if _, ok := node.ParentElement(); ok {
			convertUseSVG(node, state, cache, parent)
		} else {
			// Skip the root `svg`.
			convertChildren(node, state, cache, parent)
		}
	case eidG:
		convertChildren(node, state, cache, parent)
	}
}

// convertClipPathElements is convert_clip_path_elements: a `clipPath` can hold only shapes and
// `text`. A `line` cannot affect rendering, since clip path children are never stroked.
func convertClipPathElements(clipNode svgNode, state *converterState, cache *converterCache, parent *Group) {
	for node := range clipNode.Children() {
		tag, ok := node.TagName()
		if !ok || !tag.isGraphic() {
			continue
		}
		if !node.isVisibleElement(state.opt) {
			continue
		}
		if tag == eidUse {
			convertUse(node, state, cache, parent)
			continue
		}
		if g := convertGroup(node, state, false, cache, parent, func(cache *converterCache, g *Group) {
			convertClipPathElementsImpl(tag, node, state, cache, g)
		}); g != nil {
			parent.children = append(parent.children, g)
		}
	}
}

func convertClipPathElementsImpl(tag eid, node svgNode, state *converterState, cache *converterCache, parent *Group) {
	switch tag {
	case eidRect, eidCircle, eidEllipse, eidPolyline, eidPolygon, eidPath:
		if path, ok := convertShape(node, state); ok {
			convertPath(node, path, state, cache, parent)
		}
	case eidText:
		convertText(node, state, cache, parent)
	}
	// Anything else is not a valid clip path child.
}

// convertGroup is convert_group: it collects the node's children into a new group and returns it when
// the group is required (it changes rendering, carries a `g`/`use` id, or force is set); otherwise the
// children go straight into parent and it returns nil. It also returns nil when the node must not be
// rendered (an invalid clip path, mask or filter reference).
func convertGroup(node svgNode, state *converterState, force bool, cache *converterCache, parent *Group,
	collectChildren func(*converterCache, *Group)) *Group {
	// A `clipPath` child cannot have an opacity.
	opacity := float32(1)
	if !state.hasParentClipPath {
		if o, ok := node.attrOpacity(aidOpacity); ok {
			opacity = o
		}
	}

	transform := node.resolveTransform(aidTransform, state)
	blendMode, _ := node.attrBlendMode(aidMixBlendMode)
	isolation, _ := node.attrIsolation(aidIsolation)
	isolate := isolation == svgIsolationIsolate

	// Nodes generated by markers must not have an ID, or there would be duplicates.
	isGOrUse := node.hasTagName(eidG) || node.hasTagName(eidUse)
	id := ""
	if isGOrUse && len(state.parentMarkers) == 0 {
		id = node.ElementID()
	}

	g := newEmptyGroup()
	g.id = id
	g.transform = transform
	g.absTransform = parent.absTransform.PreConcat(transform)
	g.opacity = opacity
	g.blendMode = blendMode
	g.isolate = isolate
	collectChildren(cache, &g)

	// The group's bounding box is needed before converting clip paths, masks and filters.
	objectBBox, hasObjectBBox := g.calculateObjectBBox()
	var objectBBoxPtr *skia.NonZeroRect
	if hasObjectBBox {
		objectBBoxPtr = &objectBBox
	}

	// `mask` and `filter` cannot be set on `clipPath` children, but `clip-path` can.
	var clipPath *ClipPath
	if link, ok := node.attrNode(aidClipPath); ok {
		clipPath = convertClipPath(link, state, objectBBoxPtr, cache)
		if clipPath == nil {
			return nil
		}
	}

	var mask *Mask
	if !state.hasParentClipPath {
		if link, ok := node.attrNode(aidMask); ok {
			mask = convertMask(link, state, objectBBoxPtr, cache)
			if mask == nil {
				return nil
			}
		}
	}

	var filters []*Filter
	if !state.hasParentClipPath {
		if v, ok := node.attrStr(aidFilter); ok && v == "none" {
			// Nothing to do. (attrStr hides "none" on `filter` already; kept to mirror Rust.)
		} else if node.HasAttribute(aidFilter) {
			f, ok := convertFilters(node, state, objectBBoxPtr, cache)
			if !ok {
				// A filter that is not a link, or links to a missing element: unlike `clip-path` and
				// `mask`, the whole element is then not rendered (e-filter-051.svg).
				return nil
			}
			filters = f
		}
	}

	required := !approxEqUlpsF32(opacity, 1, 4) || clipPath != nil || mask != nil || len(filters) != 0 ||
		!transform.IsIdentity() || blendMode != BlendModeNormal || isolate || isGOrUse || force
	if !required {
		parent.children = append(parent.children, g.children...)
		return nil
	}

	g.clipPath = clipPath
	g.mask = mask
	g.filters = filters

	// Must be called after the filters are set.
	g.calculateBoundingBoxes()
	return &g
}

// convertPath is converter.rs's convert_path: the path node with its fill, stroke and markers, in
// `paint-order`.
func convertPath(node svgNode, tsPath *skia.Path, state *converterState, cache *converterCache, parent *Group) {
	if tsPath.Len() < 2 {
		return
	}

	bounds := tsPath.Bounds()
	hasBBox := bounds.Width() > 0 && bounds.Height() > 0
	fill := resolveFill(node, hasBBox, state, cache)
	stroke := resolveStroke(node, hasBBox, state, cache)
	vis, _ := svgFindAttr(node, aidVisibility, svgNode.attrVisibility)
	visible := vis == visibilityVisible
	renderingMode, ok := svgFindAttr(node, aidShapeRendering, svgNode.attrShapeRendering)
	if !ok {
		renderingMode = state.opt.ShapeRendering
	}

	rawPaintOrder, ok := svgFindAttr(node, aidPaintOrder, svgNode.attrPaintOrder)
	if !ok {
		rawPaintOrder = stDefaultPaintOrder()
	}
	paintOrder := svgPaintOrderToUsvg(rawPaintOrder)
	pathTransform := parent.absTransform

	// A path with neither fill nor stroke is invisible.
	if fill == nil && stroke == nil {
		visible = false
	}

	if fill != nil && fill.contextElement.kind == contextElementPathNode {
		ce := fill.contextElement
		f := *fill
		clear(cache.rc) // during conversion only paint servers made by processPaint itself are unshared
		processPaint(&f.paint, true, ce.transform, contextRect(ce), pathTransform, bounds, cache)
		f.contextElement = contextElement{}
		fill = &f
	}
	if stroke != nil && stroke.contextElement.kind == contextElementPathNode {
		ce := stroke.contextElement
		s := *stroke
		clear(cache.rc)
		processPaint(&s.paint, true, ce.transform, contextRect(ce), pathTransform, bounds, cache)
		s.contextElement = contextElement{}
		stroke = &s
	}

	var marker *Group
	if markerIsValid(node) && vis == visibilityVisible {
		markerGroup := newEmptyGroup()
		markerGroup.absTransform = parent.absTransform

		markerState := *state

		ce := contextElement{kind: contextElementPathNode, transform: pathTransform}
		if r, ok := tsPath.ComputeTightBounds(); ok {
			ce.bbox, ce.hasBBox = r.ToNonZeroRect()
		}
		var mf *Fill
		if fill != nil {
			f := *fill
			f.contextElement = ce
			mf = &f
		}
		var ms *Stroke
		if stroke != nil {
			s := *stroke
			s.contextElement = ce
			ms = &s
		}
		markerState.contextElement = &fillStroke{fill: mf, stroke: ms}

		convertMarkers(node, tsPath, &markerState, cache, &markerGroup)
		markerGroup.calculateBoundingBoxes()
		marker = &markerGroup
	}

	// Nodes generated by markers must not have an ID, or there would be duplicates.
	id := ""
	if len(state.parentMarkers) == 0 {
		id = node.ElementID()
	}

	path, ok := newPath(id, visible, fill, stroke, paintOrder, renderingMode, tsPath, pathTransform)
	if !ok {
		return
	}

	order := rawPaintOrder.Order
	switch {
	case marker != nil && order[0] == stPaintOrderMarkers:
		parent.children = append(parent.children, marker, path)
	case marker != nil && order[1] == stPaintOrderMarkers:
		appendSinglePaintPath(order[0], path, parent)
		parent.children = append(parent.children, marker)
		appendSinglePaintPath(order[2], path, parent)
	case marker != nil && order[2] == stPaintOrderMarkers:
		parent.children = append(parent.children, path, marker)
	default:
		parent.children = append(parent.children, path)
	}
}

// contextRect is the context bbox of a PathNode context element as an Option<Rect> (nil is None).
func contextRect(ce contextElement) *skia.Rect {
	if !ce.hasBBox {
		return nil
	}
	r := ce.bbox.ToRect()
	return &r
}

// appendSinglePaintPath is append_single_paint_path: a copy of path with only its fill or only its
// stroke, and no id.
func appendSinglePaintPath(kind stPaintOrderKind, path *Path, parent *Group) {
	switch kind {
	case stPaintOrderFill:
		if path.fill != nil {
			p := *path
			p.stroke = nil
			p.id = ""
			parent.children = append(parent.children, &p)
		}
	case stPaintOrderStroke:
		if path.stroke != nil {
			p := *path
			p.fill = nil
			p.id = ""
			parent.children = append(parent.children, &p)
		}
	}
}

// svgPaintOrderToUsvg is svg_paint_order_to_usvg.
func svgPaintOrderToUsvg(order stPaintOrder) PaintOrder {
	switch {
	case order.Order[0] == stPaintOrderStroke:
		return PaintOrderStrokeAndFill
	case order.Order[0] == stPaintOrderMarkers && order.Order[1] == stPaintOrderStroke:
		return PaintOrderStrokeAndFill
	}
	return PaintOrderFillAndStroke
}

// resolveTransform is SvgNode::resolve_transform: the transform attribute around `transform-origin`.
func (n svgNode) resolveTransform(transformAid aid, state *converterState) skia.Transform {
	transform, ok := n.attrTransform(transformAid)
	if !ok {
		transform = skia.Identity()
	}
	if origin, ok := n.attrTransformOrigin(aidTransformOrigin); ok {
		dx := convertLength(origin.XOffset, n, aidWidth, unitsUserSpaceOnUse, state)
		dy := convertLength(origin.YOffset, n, aidHeight, unitsUserSpaceOnUse, state)
		transform = skia.Identity().PreTranslate(dx, dy).PreConcat(transform).PreTranslate(-dx, -dy)
	}
	return transform
}
