package usvg

import "github.com/naddrzz/nalarupa/core-go/skia"

// convertFilters stands in for parser/filter.rs's convert until slice 4c3 ports filters. It keeps what
// the rest of the conversion depends on: the same Ok/Err outcome (ok false: the element is not
// rendered), the same filter ids, the filter regions (which feed the layer bounding boxes) and the
// cache sharing of userSpaceOnUse filters. The filters carry no primitives.
func convertFilters(node svgNode, state *converterState, objectBBox *skia.NonZeroRect,
	cache *converterCache) ([]*Filter, bool) {
	value, ok := node.attrStr(aidFilter)
	if !ok {
		return nil, true
	}

	hasInvalidURLs := false
	var filters []*Filter

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

		if fv.Kind != stFilterURL {
			// A filter function has no filter region of its own: Rust uses a fairly large one.
			rect, _ := skia.NonZeroRectFromXYWH(-0.1, -0.1, 1.2, 1.2)
			if fv.Kind == stFilterDropShadow || fv.Kind == stFilterBlur {
				rect, _ = skia.NonZeroRectFromXYWH(-0.5, -0.5, 2, 2)
			}
			if objectBBox == nil {
				continue // the filter has an invalid region
			}
			rect = rect.BBoxTransform(*objectBBox)
			filters = append(filters, &Filter{id: cache.genFilterID(), rect: rect})
			continue
		}

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

	// Links to missing elements with no valid filter at all are an error; an invalid link alone is not.
	if len(filters) == 0 && hasInvalidURLs {
		return nil, false
	}
	return filters, true
}

// convertFilterURL is filter.rs's convert_url without the primitives; ok false is Err(()).
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
	if !hasFilterPrimitives(withPrimitives, primitiveUnits, state, objectBBox, rect) {
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

	f := &Filter{id: id, rect: rect}
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

// hasFilterPrimitives reports whether filter.rs's collect_children would return any primitive: it
// walks the children until a primitive region is invalid, skipping elements that are not primitives.
// TODO(slice 4c3): feImage converts the element it references, which can consume generated ids; the
// placeholder does not.
func hasFilterPrimitives(filter svgNode, u units, state *converterState, objectBBox *skia.NonZeroRect,
	filterRegion skia.NonZeroRect) bool {
	if u == unitsObjectBoundingBox && objectBBox == nil {
		return false
	}
	for child := range filter.Children() {
		tag, ok := child.TagName()
		if !ok {
			continue
		}
		if _, ok := resolvePrimitiveRegion(child, tag, u, state, objectBBox, filterRegion); !ok {
			break
		}
		switch tag {
		case eidFeDropShadow, eidFeGaussianBlur, eidFeOffset, eidFeBlend, eidFeFlood, eidFeComposite,
			eidFeMerge, eidFeTile, eidFeImage, eidFeComponentTransfer, eidFeColorMatrix, eidFeConvolveMatrix,
			eidFeMorphology, eidFeDisplacementMap, eidFeTurbulence, eidFeDiffuseLighting, eidFeSpecularLighting:
			return true
		}
	}
	return false
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
