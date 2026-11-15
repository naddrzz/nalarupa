// Port of usvg 0.47 src/parser/filter.rs: `filter` elements and filter functions.

package usvg

import (
	"math"
	"strconv"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// convertFilters is filter.rs's convert: the filters of the node's `filter` property. ok false is
// Err(()): the element must not be rendered.
func convertFilters(node svgNode, state *converterState, objectBBox *skia.NonZeroRect,
	cache *converterCache) ([]*Filter, bool) {
	value, ok := node.attrStr(aidFilter)
	if !ok {
		return nil, true
	}

	hasInvalidURLs := false
	var filters []*Filter

	createBaseFilterFunc := func(kind FilterKind) {
		// Filter functions, unlike `filter` elements, have no filter region; usvg uses a fairly large one.
		rect, _ := skia.NonZeroRectFromXYWH(-0.1, -0.1, 1.2, 1.2)
		switch kind.(type) {
		case *FeDropShadow, *FeGaussianBlur:
			rect, _ = skia.NonZeroRectFromXYWH(-0.5, -0.5, 2, 2)
		}
		if objectBBox == nil {
			return // the filter has an invalid region
		}
		rect = rect.BBoxTransform(*objectBBox)
		filters = append(filters, &Filter{
			id:   cache.genFilterID(),
			rect: rect,
			primitives: []Primitive{{
				rect: rect,
				// Unlike `filter` elements, filter functions use sRGB colors by default.
				colorInterpolation: ColorInterpolationSRGB,
				result:             "result",
				kind:               kind,
			}},
		})
	}

	p := stNewFilterValueListParser(value)
	for {
		fv, ok, err := p.Next()
		if !ok {
			break
		}
		if err != nil {
			// The whole attribute list is skipped on error.
			return nil, true
		}

		switch fv.Kind {
		case stFilterBlur:
			createBaseFilterFunc(convertBlurFunction(node, fv.Length, state))
		case stFilterDropShadow:
			var color *stColor
			if fv.HasColor {
				color = &fv.Color
			}
			createBaseFilterFunc(convertDropShadowFunction(node, color, fv.Dx, fv.Dy, fv.StdDev, state))
		case stFilterBrightness:
			createBaseFilterFunc(convertBrightnessFunction(fv.Amount))
		case stFilterContrast:
			createBaseFilterFunc(convertContrastFunction(fv.Amount))
		case stFilterGrayscale:
			createBaseFilterFunc(convertGrayscaleFunction(fv.Amount))
		case stFilterHueRotate:
			createBaseFilterFunc(convertHueRotateFunction(fv.Angle))
		case stFilterInvert:
			createBaseFilterFunc(convertInvertFunction(fv.Amount))
		case stFilterOpacity:
			createBaseFilterFunc(convertOpacityFunction(fv.Amount))
		case stFilterSepia:
			createBaseFilterFunc(convertSepiaFunction(fv.Amount))
		case stFilterSaturate:
			createBaseFilterFunc(convertSaturateFunction(fv.Amount))
		case stFilterURL:
			link, ok := node.Document().ElementByID(fv.URL)
			if !ok {
				hasInvalidURLs = true
				continue
			}
			f, ok := convertFilterURL(link, state, objectBBox, cache)
			if !ok {
				hasInvalidURLs = true
				continue
			}
			if f != nil {
				filters = append(filters, f)
			}
		}
	}

	// Links to missing elements with no valid filter at all are an error; an invalid link alone is not.
	if len(filters) == 0 && hasInvalidURLs {
		return nil, false
	}
	return filters, true
}

// convertFilterURL is filter.rs's convert_url; ok false is Err(()).
func convertFilterURL(node svgNode, state *converterState, objectBBox *skia.NonZeroRect,
	cache *converterCache) (*Filter, bool) {
	u := convertUnits(node, aidFilterUnits, unitsObjectBoundingBox)
	primitiveUnits := convertUnits(node, aidPrimitiveUnits, unitsUserSpaceOnUse)

	// Only userSpaceOnUse filters can be shared: an objectBoundingBox one becomes node-specific.
	cacheable := u == unitsUserSpaceOnUse && primitiveUnits == unitsUserSpaceOnUse
	if cacheable {
		if f, ok := cache.filters[node.ElementID()]; ok {
			return f, true
		}
	}

	rect, ok := skia.NonZeroRectFromXYWH(
		resolveNumber(node, aidX, u, state, stNewLength(-10, stLengthUnitPercent)),
		resolveNumber(node, aidY, u, state, stNewLength(-10, stLengthUnitPercent)),
		resolveNumber(node, aidWidth, u, state, stNewLength(120, stLengthUnitPercent)),
		resolveNumber(node, aidHeight, u, state, stNewLength(120, stLengthUnitPercent)),
	)
	if !ok {
		return nil, false // the filter has an invalid region
	}

	if u == unitsObjectBoundingBox {
		if objectBBox == nil {
			return nil, false // filters on zero-sized shapes are not allowed
		}
		rect = rect.BBoxTransform(*objectBBox)
	}

	withPrimitives, ok := findFilterWithPrimitives(node)
	if !ok {
		return nil, false
	}
	primitives := collectFilterChildren(withPrimitives, primitiveUnits, state, objectBBox, rect, cache)
	if len(primitives) == 0 {
		return nil, false
	}

	id, ok := newNonEmptyString(node.ElementID())
	if !ok {
		return nil, false
	}
	// Generate an id only when an objectBoundingBox filter is parsed for the second time.
	if _, seen := cache.filters[id]; !cacheable && seen {
		id = cache.genFilterID()
	}

	f := &Filter{id: id, rect: rect, primitives: primitives}
	cache.filters[id] = f
	return f, true
}

// findFilterWithPrimitives is find_filter_with_primitives.
func findFilterWithPrimitives(node svgNode) (svgNode, bool) {
	for link := range node.HrefIter() {
		if !link.hasTagName(eidFilter) {
			return svgNode{}, false
		}
		if link.HasChildren() {
			return link, true
		}
	}
	return svgNode{}, false
}

// filterResults is FilterResults: the predefined `result` names and the next generated index.
type filterResults struct {
	names map[string]struct{}
	idx   int
}

// collectFilterChildren is collect_children: the primitives up to the first one with an invalid
// region, skipping elements that are not primitives.
func collectFilterChildren(filter svgNode, u units, state *converterState, objectBBox *skia.NonZeroRect,
	filterRegion skia.NonZeroRect, cache *converterCache) []Primitive {
	var primitives []Primitive
	results := filterResults{names: map[string]struct{}{}, idx: 1}

	scale, _ := skia.SizeFromWH(1, 1)
	if u == unitsObjectBoundingBox {
		if objectBBox == nil {
			return nil
		}
		scale = objectBBox.Size()
	}

	for child := range filter.Children() {
		tag, ok := child.TagName()
		if !ok {
			continue
		}

		subregion, ok := resolvePrimitiveRegion(child, tag, u, state, objectBBox, filterRegion)
		if !ok {
			break
		}

		var kind FilterKind
		switch tag {
		case eidFeDropShadow:
			kind = convertFeDropShadow(child, scale, primitives)
		case eidFeGaussianBlur:
			kind = convertFeGaussianBlur(child, scale, primitives)
		case eidFeOffset:
			kind = convertFeOffset(child, scale, primitives)
		case eidFeBlend:
			kind = convertFeBlend(child, primitives)
		case eidFeFlood:
			kind = convertFeFlood(child)
		case eidFeComposite:
			kind = convertFeComposite(child, primitives)
		case eidFeMerge:
			kind = convertFeMerge(child, primitives)
		case eidFeTile:
			kind = &FeTile{input: resolveFilterInput(child, aidIn, primitives)}
		case eidFeImage:
			kind = convertFeImage(child, subregion, state, cache)
		case eidFeComponentTransfer:
			kind = convertFeComponentTransfer(child, primitives)
		case eidFeColorMatrix:
			kind = convertFeColorMatrix(child, primitives)
		case eidFeConvolveMatrix:
			if k, ok := convertFeConvolveMatrix(child, primitives); ok {
				kind = k
			} else {
				kind = createDummyPrimitive()
			}
		case eidFeMorphology:
			kind = convertFeMorphology(child, scale, primitives)
		case eidFeDisplacementMap:
			kind = convertFeDisplacementMap(child, scale, primitives)
		case eidFeTurbulence:
			kind = convertFeTurbulence(child)
		case eidFeDiffuseLighting:
			if k, ok := convertFeDiffuseLighting(child, primitives); ok {
				kind = k
			} else {
				kind = createDummyPrimitive()
			}
		case eidFeSpecularLighting:
			if k, ok := convertFeSpecularLighting(child, primitives); ok {
				kind = k
			} else {
				kind = createDummyPrimitive()
			}
		default:
			continue // not a valid filter primitive
		}

		ci, ok := svgFindAttr(child, aidColorInterpolationFilters, svgNode.attrColorInterpolation)
		if !ok {
			ci = ColorInterpolationLinearRGB
		}

		primitives = append(primitives, Primitive{
			rect:               subregion,
			colorInterpolation: ci,
			result:             genFilterResult(child, &results),
			kind:               kind,
		})
	}
	return primitives
}

// resolvePrimitiveRegion is filter.rs's resolve_primitive_region.
func resolvePrimitiveRegion(fe svgNode, kind eid, u units, state *converterState, bbox *skia.NonZeroRect,
	filterRegion skia.NonZeroRect) (skia.NonZeroRect, bool) {
	x, hasX := fe.tryConvertLength(aidX, u, state)
	y, hasY := fe.tryConvertLength(aidY, u, state)
	width, hasW := fe.tryConvertLength(aidWidth, u, state)
	height, hasH := fe.tryConvertLength(aidHeight, u, state)
	or := func(v float32, ok bool, def float32) float32 {
		if ok {
			return v
		}
		return def
	}

	region := filterRegion
	if (kind == eidFeFlood || kind == eidFeImage) && u == unitsObjectBoundingBox {
		// `feImage` uses the object bbox.
		if bbox == nil {
			return skia.NonZeroRect{}, false
		}
		r, ok := skia.NonZeroRectFromXYWH(or(x, hasX, 0), or(y, hasY, 0), or(width, hasW, 1), or(height, hasH, 1))
		if !ok {
			return skia.NonZeroRect{}, false
		}
		return r.BBoxTransform(*bbox), true
	}

	if u == unitsObjectBoundingBox {
		sub, ok := skia.NonZeroRectFromXYWH(or(x, hasX, 0), or(y, hasY, 0), or(width, hasW, 1), or(height, hasH, 1))
		if !ok {
			return skia.NonZeroRect{}, false
		}
		return region.BBoxTransform(sub), true
	}
	return skia.NonZeroRectFromXYWH(or(x, hasX, region.X()), or(y, hasY, region.Y()),
		or(width, hasW, region.Width()), or(height, hasH, region.Height()))
}

// createDummyPrimitive is create_dummy_primitive: a malformed primitive should produce a transparent
// image, and since primitives are always valid, a transparent flood stands in.
func createDummyPrimitive() FilterKind {
	return &FeFlood{color: ColorBlack(), opacity: 0}
}

// resolveFilterInput is resolve_input: `in`/`in2`, falling back to the previous result (or
// SourceGraphic for the first primitive) when it is missing or names an unknown result.
func resolveFilterInput(node svgNode, a aid, primitives []Primitive) FilterInput {
	prev := func() FilterInput {
		if n := len(primitives); n != 0 {
			return FilterInput{Kind: FilterInputReference, Name: primitives[n-1].result}
		}
		return FilterInput{Kind: FilterInputSourceGraphic}
	}
	s, ok := node.attrStr(a)
	if !ok {
		return prev()
	}
	in := parseFilterIn(s)
	if in.Kind == FilterInputReference {
		for i := range primitives {
			if primitives[i].result == in.Name {
				return in
			}
		}
		return prev()
	}
	return in
}

// parseFilterIn is parse_in; the unsupported inputs become SourceGraphic.
func parseFilterIn(s string) FilterInput {
	switch s {
	case "SourceGraphic", "BackgroundImage", "BackgroundAlpha", "FillPaint", "StrokePaint":
		return FilterInput{Kind: FilterInputSourceGraphic}
	case "SourceAlpha":
		return FilterInput{Kind: FilterInputSourceAlpha}
	}
	return FilterInput{Kind: FilterInputReference, Name: s}
}

// genFilterResult is gen_result: the `result` attribute, or a generated unique name.
func genFilterResult(node svgNode, results *filterResults) string {
	if s, ok := node.attrStr(aidResult); ok {
		// Remember the predefined result.
		results.names[s] = struct{}{}
		results.idx++
		return s
	}
	for {
		name := "result" + strconv.Itoa(results.idx)
		results.idx++
		if _, taken := results.names[name]; !taken {
			return name
		}
	}
}

// f32Or is `node.attribute::<f32>(a).unwrap_or(def)`.
func (n svgNode) f32Or(a aid, def float32) float32 {
	if v, ok := n.attrF32(a); ok {
		return v
	}
	return def
}

// strOr is `node.attribute::<&str>(a).unwrap_or(def)`.
func (n svgNode) strOr(a aid, def string) string {
	if v, ok := n.attrStr(a); ok {
		return v
	}
	return def
}

// stNumberListNextOK is `parser.next().and_then(|n| n.ok())`.
func stNumberListNextOK(p *stNumberListParser) (float64, bool) {
	n, ok, err := p.Next()
	return n, ok && err == nil
}

func convertFeBlend(fe svgNode, primitives []Primitive) FilterKind {
	mode, _ := fe.attrBlendMode(aidMode)
	return &FeBlend{
		mode:   mode,
		input1: resolveFilterInput(fe, aidIn, primitives),
		input2: resolveFilterInput(fe, aidIn2, primitives),
	}
}

func convertFeColorMatrix(fe svgNode, primitives []Primitive) FilterKind {
	kind, ok := convertColorMatrixKind(fe)
	if !ok {
		kind = defaultColorMatrixKind()
	}
	return &FeColorMatrix{input: resolveFilterInput(fe, aidIn, primitives), kind: kind}
}

func convertColorMatrixKind(fe svgNode) (ColorMatrixKind, bool) {
	typ, _ := fe.attrStr(aidType)
	switch typ {
	case "saturate":
		if list, ok := fe.attrNumberList(aidValues); ok {
			if len(list) != 0 {
				return ColorMatrixSaturate(f32Bound(0, list[0], 1)), true
			}
			return ColorMatrixSaturate(1), true
		}
	case "hueRotate":
		if list, ok := fe.attrNumberList(aidValues); ok {
			if len(list) != 0 {
				return ColorMatrixHueRotate(list[0]), true
			}
			return ColorMatrixHueRotate(0), true
		}
	case "luminanceToAlpha":
		return ColorMatrixLuminanceToAlpha{}, true
	default:
		// Fall back to `matrix` (also when `type` is missing).
		if list, ok := fe.attrNumberList(aidValues); ok && len(list) == 20 {
			return ColorMatrixMatrix(list), true
		}
	}
	return nil, false
}

func convertFeComponentTransfer(fe svgNode, primitives []Primitive) FilterKind {
	kind := &FeComponentTransfer{
		input: resolveFilterInput(fe, aidIn, primitives),
		funcR: TransferIdentity{},
		funcG: TransferIdentity{},
		funcB: TransferIdentity{},
		funcA: TransferIdentity{},
	}
	for child := range fe.Children() {
		if !child.IsElement() {
			continue
		}
		f, ok := convertTransferFunction(child)
		if !ok {
			continue
		}
		tag, _ := child.TagName()
		switch tag {
		case eidFeFuncR:
			kind.funcR = f
		case eidFeFuncG:
			kind.funcG = f
		case eidFeFuncB:
			kind.funcB = f
		case eidFeFuncA:
			kind.funcA = f
		}
	}
	return kind
}

func convertTransferFunction(node svgNode) (TransferFunction, bool) {
	typ, ok := node.attrStr(aidType)
	if !ok {
		return nil, false
	}
	switch typ {
	case "identity":
		return TransferIdentity{}, true
	case "table":
		values, ok := node.attrNumberList(aidTableValues)
		if !ok {
			values = []float32{}
		}
		return TransferTable(values), true
	case "discrete":
		values, ok := node.attrNumberList(aidTableValues)
		if !ok {
			values = []float32{}
		}
		return TransferDiscrete(values), true
	case "linear":
		return TransferLinear{
			Slope:     node.f32Or(aidSlope, 1),
			Intercept: node.f32Or(aidIntercept, 0),
		}, true
	case "gamma":
		return TransferGamma{
			Amplitude: node.f32Or(aidAmplitude, 1),
			Exponent:  node.f32Or(aidExponent, 1),
			Offset:    node.f32Or(aidOffset, 0),
		}, true
	}
	return nil, false
}

func convertFeComposite(fe svgNode, primitives []Primitive) FilterKind {
	var op CompositeOperator
	switch fe.strOr(aidOperator, "over") {
	case "in":
		op.Op = CompositeIn
	case "out":
		op.Op = CompositeOut
	case "atop":
		op.Op = CompositeAtop
	case "xor":
		op.Op = CompositeXor
	case "arithmetic":
		op = CompositeOperator{
			Op: CompositeArithmetic,
			K1: fe.f32Or(aidK1, 0),
			K2: fe.f32Or(aidK2, 0),
			K3: fe.f32Or(aidK3, 0),
			K4: fe.f32Or(aidK4, 0),
		}
	default:
		op.Op = CompositeOver
	}
	return &FeComposite{
		operator: op,
		input1:   resolveFilterInput(fe, aidIn, primitives),
		input2:   resolveFilterInput(fe, aidIn2, primitives),
	}
}

func convertFeConvolveMatrix(fe svgNode, primitives []Primitive) (FilterKind, bool) {
	parseTarget := func(target float32, hasTarget bool, order uint32) (uint32, bool) {
		defaultTarget := asU32(math.Floor(float64(float32(order) / 2)))
		if !hasTarget {
			target = float32(defaultTarget)
		}
		t := asI32(float64(target))
		if t < 0 || t >= int32(order) { // `order as i32` wraps
			return 0, false
		}
		return uint32(t), true
	}

	orderX, orderY := uint32(3), uint32(3)
	if value, ok := fe.attrStr(aidOrder); ok {
		s := stNewNumberListParser(value)
		x := int32(3)
		if n, ok := stNumberListNextOK(s); ok {
			x = asI32(n)
		}
		y := x
		if n, ok := stNumberListNextOK(s); ok {
			y = asI32(n)
		}
		if x > 0 && y > 0 {
			orderX, orderY = uint32(x), uint32(y)
		}
	}

	matrix := []float32{}
	if list, ok := fe.attrNumberList(aidKernelMatrix); ok && uint64(len(list)) == uint64(orderX*orderY) {
		matrix = list
	}

	var kernelSum float32
	for _, v := range matrix {
		kernelSum += v
	}
	// Round to prevent float precision issues.
	kernelSum = float32(math.Round(float64(float32(kernelSum*1_000_000)))) / 1_000_000
	if approxZeroUlpsF32(kernelSum, 4) {
		kernelSum = 1
	}

	divisor := fe.f32Or(aidDivisor, kernelSum)
	if approxZeroUlpsF32(divisor, 4) {
		return nil, false
	}

	bias := fe.f32Or(aidBias, 0)

	tx, hasTX := fe.attrF32(aidTargetX)
	targetX, ok := parseTarget(tx, hasTX, orderX)
	if !ok {
		return nil, false
	}
	ty, hasTY := fe.attrF32(aidTargetY)
	targetY, ok := parseTarget(ty, hasTY, orderY)
	if !ok {
		return nil, false
	}

	kernelMatrix, ok := newConvolveMatrixData(targetX, targetY, orderX, orderY, matrix)
	if !ok {
		return nil, false
	}

	edgeMode := EdgeModeDuplicate
	switch fe.strOr(aidEdgeMode, "duplicate") {
	case "none":
		edgeMode = EdgeModeNone
	case "wrap":
		edgeMode = EdgeModeWrap
	}

	preserveAlpha := fe.strOr(aidPreserveAlpha, "false") == "true"

	return &FeConvolveMatrix{
		input:         resolveFilterInput(fe, aidIn, primitives),
		matrix:        kernelMatrix,
		divisor:       divisor,
		bias:          bias,
		edgeMode:      edgeMode,
		preserveAlpha: preserveAlpha,
	}, true
}

func convertFeDisplacementMap(fe svgNode, scale skia.Size, primitives []Primitive) FilterKind {
	parseChannel := func(a aid) ColorChannel {
		switch fe.strOr(a, "A") {
		case "R":
			return ColorChannelR
		case "G":
			return ColorChannelG
		case "B":
			return ColorChannelB
		}
		return ColorChannelA
	}

	// Rust notes the scale should probably be split into x and y.
	s := (scale.Width() + scale.Height()) / 2

	return &FeDisplacementMap{
		input1:           resolveFilterInput(fe, aidIn, primitives),
		input2:           resolveFilterInput(fe, aidIn2, primitives),
		scale:            float32(fe.f32Or(aidScale, 0) * s),
		xChannelSelector: parseChannel(aidXChannelSelector),
		yChannelSelector: parseChannel(aidYChannelSelector),
	}
}

// floodColor is the `flood-color` (black by default) split into a color and an opacity times
// `flood-opacity`.
func floodColor(fe svgNode) (Color, float32) {
	c, ok := fe.attrColor(aidFloodColor)
	if !ok {
		c = stColorBlack()
	}
	color, opacity := splitAlpha(c)
	floodOpacity, ok := fe.attrOpacity(aidFloodOpacity)
	if !ok {
		floodOpacity = 1
	}
	return color, normalizedClampedF32(float32(opacity * floodOpacity))
}

func convertFeDropShadow(fe svgNode, scale skia.Size, primitives []Primitive) FilterKind {
	stdDevX, stdDevY := convertStdDevAttr(fe, scale, "2 2")
	color, opacity := floodColor(fe)
	return &FeDropShadow{
		input:   resolveFilterInput(fe, aidIn, primitives),
		dx:      float32(fe.f32Or(aidDx, 2) * scale.Width()),
		dy:      float32(fe.f32Or(aidDy, 2) * scale.Height()),
		stdDevX: stdDevX,
		stdDevY: stdDevY,
		color:   color,
		opacity: opacity,
	}
}

func convertFeFlood(fe svgNode) FilterKind {
	color, opacity := floodColor(fe)
	return &FeFlood{color: color, opacity: opacity}
}

func convertFeGaussianBlur(fe svgNode, scale skia.Size, primitives []Primitive) FilterKind {
	stdDevX, stdDevY := convertStdDevAttr(fe, scale, "0 0")
	return &FeGaussianBlur{input: resolveFilterInput(fe, aidIn, primitives), stdDevX: stdDevX, stdDevY: stdDevY}
}

// convertStdDevAttr is convert_std_dev_attr: one or two numbers; anything else is `0 0`.
func convertStdDevAttr(fe svgNode, scale skia.Size, def string) (float32, float32) {
	p := stNewNumberListParser(fe.strOr(aidStdDeviation, def))
	n1, ok1 := stNumberListNextOK(p)
	n2, ok2 := stNumberListNextOK(p)
	// `stdDeviation` must have no more than two values.
	_, ok3 := stNumberListNextOK(p)

	var x, y float64
	switch {
	case ok1 && ok2 && !ok3:
		x, y = n1, n2
	case ok1 && !ok2 && !ok3:
		x, y = n1, n1
	}

	sx := float32(float32(x) * scale.Width())
	sy := float32(float32(y) * scale.Height())
	if _, ok := newPositiveF32(sx); !ok {
		sx = 0
	}
	if _, ok := newPositiveF32(sy); !ok {
		sy = 0
	}
	return sx, sy
}

func convertFeImage(fe svgNode, subregion skia.NonZeroRect, state *converterState, cache *converterCache) FilterKind {
	if kind, ok := convertFeImageInner(fe, subregion, state, cache); ok {
		return kind
	}
	return createDummyPrimitive()
}

func convertFeImageInner(fe svgNode, subregion skia.NonZeroRect, state *converterState,
	cache *converterCache) (FilterKind, bool) {
	renderingMode, ok := svgFindAttr(fe, aidImageRendering, svgNode.attrImageRendering)
	if !ok {
		renderingMode = state.opt.ImageRendering
	}

	if node, ok := svgTryAttr(fe, aidHref, svgParseNode); ok {
		linkState := *state
		linkState.feImageLink = true
		root := newEmptyGroup()
		convertElement(node, &linkState, cache, &root)
		if !root.HasChildren() {
			return nil, false
		}
		root.calculateBoundingBoxes()
		// Move the node id from the group's child to the group itself.
		if g, ok := root.children[0].(*Group); ok && len(g.children) != 0 {
			switch child2 := g.children[0].(type) {
			case *Group:
				g.id, child2.id = child2.id, ""
			case *Path:
				g.id, child2.id = child2.id, ""
			case *Image:
				g.id, child2.id = child2.id, ""
			case *Text:
				g.id, child2.id = child2.id, ""
			}
		}
		return &FeImage{root: root}, true
	}

	href, ok := svgTryAttr(fe, aidHref, svgParseStr)
	if !ok {
		return nil, false // the feImage lacks `xlink:href`
	}
	imgData, ok := getHrefData(href, state)
	if !ok {
		return nil, false
	}
	actualSize, ok := imgData.actualSize()
	if !ok {
		return nil, false
	}

	aspect, ok := fe.attrAspectRatio(aidPreserveAspectRatio)
	if !ok {
		aspect = stDefaultAspectRatio()
	}

	root := newEmptyGroup()
	id := cache.genImageID() // taken before the rect, as Rust evaluates the arguments
	rect, ok := subregion.TranslateTo(0, 0)
	if !ok {
		return nil, false
	}
	convertImageInner(imgData, id, true, renderingMode, aspect, actualSize, rect, cache, &root)
	root.calculateBoundingBoxes()
	return &FeImage{root: root}, true
}

func convertFeDiffuseLighting(fe svgNode, primitives []Primitive) (FilterKind, bool) {
	light, ok := convertLightSource(fe)
	if !ok {
		return nil, false
	}
	return &FeDiffuseLighting{
		input:           resolveFilterInput(fe, aidIn, primitives),
		surfaceScale:    fe.f32Or(aidSurfaceScale, 1),
		diffuseConstant: fe.f32Or(aidDiffuseConstant, 1),
		lightingColor:   convertLightingColor(fe),
		lightSource:     light,
	}, true
}

func convertFeSpecularLighting(fe svgNode, primitives []Primitive) (FilterKind, bool) {
	light, ok := convertLightSource(fe)
	if !ok {
		return nil, false
	}

	specularExponent := fe.f32Or(aidSpecularExponent, 1)
	if !(specularExponent >= 1 && specularExponent <= 128) {
		// An exponent out of range disables the whole primitive.
		return nil, false
	}
	specularExponent = f32Bound(1, specularExponent, 128)

	return &FeSpecularLighting{
		input:            resolveFilterInput(fe, aidIn, primitives),
		surfaceScale:     fe.f32Or(aidSurfaceScale, 1),
		specularConstant: fe.f32Or(aidSpecularConstant, 1),
		specularExponent: specularExponent,
		lightingColor:    convertLightingColor(fe),
		lightSource:      light,
	}, true
}

// convertLightingColor is convert_lighting_color; the color's alpha does not affect lighting.
func convertLightingColor(node svgNode) Color {
	value, ok := node.attrStr(aidLightingColor)
	if !ok {
		return ColorWhite()
	}
	if value == "currentColor" {
		// A missing `currentColor` resolves to black, not white.
		c, ok := svgFindAttr(node, aidColor, svgNode.attrColor)
		if !ok {
			c = stColorBlack()
		}
		color, _ := splitAlpha(c)
		return color
	}
	c, err := stParseColor(value)
	if err != nil {
		return ColorWhite()
	}
	color, _ := splitAlpha(c)
	return color
}

// convertLightSource is convert_light_source: the first light source child.
func convertLightSource(parent svgNode) (LightSource, bool) {
	for child := range parent.Children() {
		tag, _ := child.TagName()
		switch {
		case !child.IsElement():
		case tag == eidFeDistantLight:
			return DistantLight{
				Azimuth:   child.f32Or(aidAzimuth, 0),
				Elevation: child.f32Or(aidElevation, 0),
			}, true
		case tag == eidFePointLight:
			return PointLight{X: child.f32Or(aidX, 0), Y: child.f32Or(aidY, 0), Z: child.f32Or(aidZ, 0)}, true
		case tag == eidFeSpotLight:
			specularExponent, ok := newPositiveF32(child.f32Or(aidSpecularExponent, 1))
			if !ok {
				specularExponent = 1
			}
			angle, hasAngle := child.attrF32(aidLimitingConeAngle)
			return SpotLight{
				X:                    child.f32Or(aidX, 0),
				Y:                    child.f32Or(aidY, 0),
				Z:                    child.f32Or(aidZ, 0),
				PointsAtX:            child.f32Or(aidPointsAtX, 0),
				PointsAtY:            child.f32Or(aidPointsAtY, 0),
				PointsAtZ:            child.f32Or(aidPointsAtZ, 0),
				SpecularExponent:     specularExponent,
				LimitingConeAngle:    angle,
				HasLimitingConeAngle: hasAngle,
			}, true
		}
	}
	return nil, false
}

func convertFeMerge(fe svgNode, primitives []Primitive) FilterKind {
	inputs := []FilterInput{}
	for child := range fe.Children() {
		inputs = append(inputs, resolveFilterInput(child, aidIn, primitives))
	}
	return &FeMerge{inputs: inputs}
}

func convertFeMorphology(fe svgNode, scale skia.Size, primitives []Primitive) FilterKind {
	operator := MorphologyErode
	if fe.strOr(aidOperator, "erode") == "dilate" {
		operator = MorphologyDilate
	}

	radiusX := mustPositiveF32(scale.Width())
	radiusY := mustPositiveF32(scale.Height())
	if list, ok := fe.attrNumberList(aidRadius); ok {
		var rx, ry float32
		if len(list) == 2 {
			rx, ry = list[0], list[1]
		} else if len(list) == 1 {
			rx, ry = list[0], list[0] // the same as rx
		}

		if approxZeroUlpsF32(rx, 4) && approxZeroUlpsF32(ry, 4) {
			rx, ry = 1, 1
		}
		// If only one of the values is zero, reset it to 1, as Chrome and Safari do.
		if approxZeroUlpsF32(rx, 4) && !approxZeroUlpsF32(ry, 4) {
			rx = 1
		}
		if !approxZeroUlpsF32(rx, 4) && approxZeroUlpsF32(ry, 4) {
			ry = 1
		}

		// Both values must be positive.
		if !math.Signbit(float64(rx)) && !math.Signbit(float64(ry)) {
			radiusX = mustPositiveF32(float32(rx * scale.Width()))
			radiusY = mustPositiveF32(float32(ry * scale.Height()))
		}
	}

	return &FeMorphology{
		input:    resolveFilterInput(fe, aidIn, primitives),
		operator: operator,
		radiusX:  radiusX,
		radiusY:  radiusY,
	}
}

// mustPositiveF32 is `PositiveF32::new(n).unwrap()`.
func mustPositiveF32(n float32) float32 {
	if _, ok := newPositiveF32(n); !ok {
		panic("usvg: PositiveF32::new(" + strconv.FormatFloat(float64(n), 'g', -1, 32) + ").unwrap()") // Rust unwraps
	}
	return n
}

func convertFeOffset(fe svgNode, scale skia.Size, primitives []Primitive) FilterKind {
	return &FeOffset{
		input: resolveFilterInput(fe, aidIn, primitives),
		dx:    float32(fe.f32Or(aidDx, 0) * scale.Width()),
		dy:    float32(fe.f32Or(aidDy, 0) * scale.Height()),
	}
}

func convertFeTurbulence(fe svgNode) FilterKind {
	var baseFrequencyX, baseFrequencyY float32
	if list, ok := fe.attrNumberList(aidBaseFrequency); ok {
		var x, y float32
		if len(list) == 2 {
			x, y = list[0], list[1]
		} else if len(list) == 1 {
			x, y = list[0], list[0] // the same as x
		}
		if !math.Signbit(float64(x)) && !math.Signbit(float64(y)) {
			baseFrequencyX = mustPositiveF32(x)
			baseFrequencyY = mustPositiveF32(y)
		}
	}

	numOctaves := fe.f32Or(aidNumOctaves, 1)
	if math.Signbit(float64(numOctaves)) {
		numOctaves = 0
	}

	kind := TurbulenceTurbulence
	if fe.strOr(aidType, "turbulence") == "fractalNoise" {
		kind = TurbulenceFractalNoise
	}

	stitch, _ := fe.attrStr(aidStitchTiles)
	return &FeTurbulence{
		baseFrequencyX: baseFrequencyX,
		baseFrequencyY: baseFrequencyY,
		numOctaves:     asU32(math.Round(float64(numOctaves))),
		seed:           asI32(math.Trunc(float64(fe.f32Or(aidSeed, 0)))),
		stitchTiles:    stitch == "stitch",
		kind:           kind,
	}
}

// The CSS filter functions. Their f32 arithmetic is spelled out with explicit conversions (no FMA).

func convertGrayscaleFunction(amount float64) FilterKind {
	a := float32(minF64(amount, 1))
	r := float32(1 - a)
	return &FeColorMatrix{
		input: FilterInput{Kind: FilterInputSourceGraphic},
		kind: ColorMatrixMatrix{
			0.2126 + float32(0.7874*r), 0.7152 - float32(0.7152*r), 0.0722 - float32(0.0722*r), 0, 0,
			0.2126 - float32(0.2126*r), 0.7152 + float32(0.2848*r), 0.0722 - float32(0.0722*r), 0, 0,
			0.2126 - float32(0.2126*r), 0.7152 - float32(0.7152*r), 0.0722 + float32(0.9278*r), 0, 0,
			0, 0, 0, 1, 0,
		},
	}
}

func convertSepiaFunction(amount float64) FilterKind {
	a := float32(minF64(amount, 1))
	r := float32(1 - a)
	return &FeColorMatrix{
		input: FilterInput{Kind: FilterInputSourceGraphic},
		kind: ColorMatrixMatrix{
			0.393 + float32(0.607*r), 0.769 - float32(0.769*r), 0.189 - float32(0.189*r), 0, 0,
			0.349 - float32(0.349*r), 0.686 + float32(0.314*r), 0.168 - float32(0.168*r), 0, 0,
			0.272 - float32(0.272*r), 0.534 - float32(0.534*r), 0.131 + float32(0.869*r), 0, 0,
			0, 0, 0, 1, 0,
		},
	}
}

func convertSaturateFunction(amount float64) FilterKind {
	a, ok := newPositiveF32(float32(amount))
	if !ok {
		a = 0
	}
	return &FeColorMatrix{input: FilterInput{Kind: FilterInputSourceGraphic}, kind: ColorMatrixSaturate(a)}
}

func convertHueRotateFunction(angle stAngle) FilterKind {
	return &FeColorMatrix{
		input: FilterInput{Kind: FilterInputSourceGraphic},
		kind:  ColorMatrixHueRotate(float32(angle.ToDegrees())),
	}
}

func convertInvertFunction(amount float64) FilterKind {
	a := float32(minF64(amount, 1))
	return &FeComponentTransfer{
		input: FilterInput{Kind: FilterInputSourceGraphic},
		funcR: TransferTable{a, 1 - a},
		funcG: TransferTable{a, 1 - a},
		funcB: TransferTable{a, 1 - a},
		funcA: TransferIdentity{},
	}
}

func convertOpacityFunction(amount float64) FilterKind {
	a := float32(minF64(amount, 1))
	return &FeComponentTransfer{
		input: FilterInput{Kind: FilterInputSourceGraphic},
		funcR: TransferIdentity{},
		funcG: TransferIdentity{},
		funcB: TransferIdentity{},
		funcA: TransferTable{0, a},
	}
}

func convertBrightnessFunction(amount float64) FilterKind {
	a := float32(amount)
	return &FeComponentTransfer{
		input: FilterInput{Kind: FilterInputSourceGraphic},
		funcR: TransferLinear{Slope: a, Intercept: 0},
		funcG: TransferLinear{Slope: a, Intercept: 0},
		funcB: TransferLinear{Slope: a, Intercept: 0},
		funcA: TransferIdentity{},
	}
}

func convertContrastFunction(amount float64) FilterKind {
	a := float32(amount)
	intercept := -float32(0.5*a) + 0.5
	return &FeComponentTransfer{
		input: FilterInput{Kind: FilterInputSourceGraphic},
		funcR: TransferLinear{Slope: a, Intercept: intercept},
		funcG: TransferLinear{Slope: a, Intercept: intercept},
		funcB: TransferLinear{Slope: a, Intercept: intercept},
		funcA: TransferIdentity{},
	}
}

func convertBlurFunction(node svgNode, stdDev stLength, state *converterState) FilterKind {
	sd, ok := newPositiveF32(convertUserLength(stdDev, node, aidDx, state))
	if !ok {
		sd = 0
	}
	return &FeGaussianBlur{input: FilterInput{Kind: FilterInputSourceGraphic}, stdDevX: sd, stdDevY: sd}
}

func convertDropShadowFunction(node svgNode, color *stColor, dx, dy, stdDev stLength,
	state *converterState) FilterKind {
	sd, ok := newPositiveF32(convertUserLength(stdDev, node, aidDx, state))
	if !ok {
		sd = 0
	}

	var c stColor
	if color != nil {
		c = *color
	} else if cc, ok := svgFindAttr(node, aidColor, svgNode.attrColor); ok {
		c = cc
	} else {
		c = stColorBlack()
	}
	rgb, opacity := splitAlpha(c)

	return &FeDropShadow{
		input:   FilterInput{Kind: FilterInputSourceGraphic},
		dx:      convertUserLength(dx, node, aidDx, state),
		dy:      convertUserLength(dy, node, aidDy, state),
		stdDevX: sd,
		stdDevY: sd,
		color:   rgb,
		opacity: opacity,
	}
}
