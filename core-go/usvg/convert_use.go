// Port of usvg 0.47 src/parser/use_node.rs: `use` elements and nested `svg` elements.

package usvg

import "github.com/naddrzz/nalarupa/core-go/skia"

// convertUse is use_node::convert. svgtree has already copied the linked element in as the `use`
// element's only child.
func convertUse(node svgNode, state *converterState, cache *converterCache, parent *Group) {
	child, ok := node.FirstChild()
	if !ok {
		return
	}

	if state.hasParentClipPath && child.hasTagName(eidSymbol) {
		// A `symbol` used inside a `clipPath` is ignored later anyway; skipping it here avoids
		// creating the clip path a `symbol` requires.
		return
	}

	useState := *state
	var cf *Fill
	if f := resolveFill(node, true, state, cache); f != nil {
		f.contextElement = contextElement{kind: contextElementUseNode}
		cf = f
	}
	var cs *Stroke
	if s := resolveStroke(node, true, state, cache); s != nil {
		s.contextElement = contextElement{kind: contextElementUseNode}
		cs = s
	}
	useState.contextElement = &fillStroke{fill: cf, stroke: cs}

	// The original transform is needed to set up the `clipPath`.
	origTS := node.resolveTransform(aidTransform, state)
	newTS := skia.Identity()
	{
		x := node.convertUserLength(aidX, &useState, stLengthZero())
		y := node.convertUserLength(aidY, &useState, stLengthZero())
		newTS = newTS.PreTranslate(x, y)
	}

	linkedToSymbol := child.hasTagName(eidSymbol)

	if linkedToSymbol {
		// With `width`/`height` on a `use` that references a symbol, relative units resolve against
		// the `use` size rather than the original SVG's, so the view box may change here.
		def := stNewLength(100, stLengthUnitPercent)
		x, y := useState.viewBox.X(), useState.viewBox.Y()
		width := useState.viewBox.Width()
		if node.HasAttribute(aidWidth) {
			width = node.convertUserLength(aidWidth, &useState, def)
		}
		height := useState.viewBox.Height()
		if node.HasAttribute(aidHeight) {
			height = node.convertUserLength(aidHeight, &useState, def)
		}
		// Fail silently if the rect is not valid.
		if vb, ok := skia.NonZeroRectFromXYWH(x, y, width, height); ok {
			useState.viewBox = vb
		}

		if ts, ok := viewboxTransform(node, child, &useState); ok {
			newTS = newTS.PreConcat(ts)
		}

		if clipRect, ok := getClipRect(node, child, &useState); ok {
			g := clipElement(node, clipRect, origTS, &useState, cache)
			g.absTransform = parent.absTransform

			// Make a group for the `use`.
			if g2 := convertGroup(node, &useState, true, cache, g, func(cache *converterCache, g2 *Group) {
				convertUseChildren(child, newTS, &useState, cache, false, g2)
			}); g2 != nil {
				// The transform is reset: the group with the clip path already has it.
				g.isContextElement = true
				g2.id = "" // prevent id duplication
				g2.transform = skia.Identity()
				g.children = append(g.children, g2)
			}

			if len(g.children) == 0 {
				return
			}
			g.calculateBoundingBoxes()
			parent.children = append(parent.children, g)
			return
		}
	}

	origTS = origTS.PreConcat(newTS)

	if linkedToSymbol {
		// Make a group for the `use`.
		if g := convertGroup(node, &useState, false, cache, parent, func(cache *converterCache, g *Group) {
			convertUseChildren(child, origTS, &useState, cache, false, g)
		}); g != nil {
			g.isContextElement = true
			g.transform = skia.Identity()
			parent.children = append(parent.children, g)
		}
		return
	}

	if child.hasTagName(eidSvg) {
		// A `use` referencing an `svg` replaces the `svg` size with its own. The spec leaves it open,
		// but each `use` seems to reset it: with nested `use` elements, one setting `width` and the
		// other `height`, the first `width` is ignored.
		def := stNewLength(100, stLengthUnitPercent)
		useState.hasUseW, useState.hasUseH = false, false
		// Width and height can be set independently.
		if node.HasAttribute(aidWidth) {
			useState.useW, useState.hasUseW = node.convertUserLength(aidWidth, &useState, def), true
		}
		if node.HasAttribute(aidHeight) {
			useState.useH, useState.hasUseH = node.convertUserLength(aidHeight, &useState, def), true
		}
	}
	convertUseChildren(node, origTS, &useState, cache, true, parent)
}

// convertUseSVG is use_node::convert_svg: a nested `svg` element.
func convertUseSVG(node svgNode, state *converterState, cache *converterCache, parent *Group) {
	// The original transform is needed to set up the `clipPath`.
	origTS := node.resolveTransform(aidTransform, state)
	newTS := skia.Identity()

	x := node.convertUserLength(aidX, state, stLengthZero())
	y := node.convertUserLength(aidY, state, stLengthZero())
	newTS = newTS.PreTranslate(x, y)

	if ts, ok := viewboxTransform(node, node, state); ok {
		newTS = newTS.PreConcat(ts)
	}

	// The new state's view box is this element's (State has no size to update here).
	newState := *state
	if vb, ok := node.parseViewbox(); ok {
		newState.viewBox = vb
	} else {
		// No `viewBox`: use `x`, `y`, `width` and `height`, where a `use` size overrides the `svg` one.
		w, h := useNodeSize(node, state)
		if state.hasUseW {
			w = state.useW
		}
		if state.hasUseH {
			h = state.useH
		}
		if vb, ok := skia.NonZeroRectFromXYWH(x, y, w, h); ok {
			newState.viewBox = vb
		}
	}

	if clipRect, ok := getClipRect(node, node, state); ok {
		g := clipElement(node, clipRect, origTS, state, cache)
		g.absTransform = parent.absTransform
		convertUseChildren(node, newTS, &newState, cache, false, g)
		g.calculateBoundingBoxes()
		parent.children = append(parent.children, g)
	} else {
		origTS = origTS.PreConcat(newTS)
		convertUseChildren(node, origTS, &newState, cache, false, parent)
	}
}

// clipElement is clip_element: a group with transform clipped to clipRect, which emulates a new
// viewport. The clip cannot go on the element itself, which its own transform would affect.
func clipElement(node svgNode, clipRect skia.NonZeroRect, transform skia.Transform, state *converterState,
	cache *converterCache) *Group {
	clipPath := newEmptyClipPath(cache.genClipPathID())

	path, ok := newSimplePath(skia.PathFromRect(clipRect.ToRect()))
	if !ok {
		panic("usvg: clip element path") // Rust unwraps
	}
	fill := DefaultFill()
	path.fill = &fill
	clipPath.root.children = append(clipPath.root.children, path)

	// Nodes generated by markers must not have an ID, or there would be duplicates.
	id := ""
	if len(state.parentMarkers) == 0 {
		id = node.ElementID()
	}

	g := newEmptyGroup()
	g.id = id
	g.transform = transform
	g.clipPath = clipPath
	return &g
}

// convertUseChildren is use_node.rs's convert_children: node's children in a group with transform.
func convertUseChildren(node svgNode, transform skia.Transform, state *converterState, cache *converterCache,
	isContextElement bool, parent *Group) {
	// Adjust the abs transform for the duration, so that convertGroup accounts for transform.
	oldAbsTransform := parent.absTransform
	parent.absTransform = parent.absTransform.PreConcat(transform)

	required := !transform.IsIdentity()
	if g := convertGroup(node, state, required, cache, parent, func(cache *converterCache, g *Group) {
		if state.hasParentClipPath {
			convertClipPathElements(node, state, cache, g)
		} else {
			convertChildren(node, state, cache, g)
		}
	}); g != nil {
		g.isContextElement = isContextElement
		g.transform = transform
		parent.children = append(parent.children, g)
	}

	parent.absTransform = oldAbsTransform
}

// getClipRect is get_clip_rect: the viewport clip of a symbol or nested svg, if it needs one.
func getClipRect(useNode, symbolNode svgNode, state *converterState) (skia.NonZeroRect, bool) {
	// No need to clip elements with overflow:visible.
	if o, ok := symbolNode.attrStr(aidOverflow); ok && (o == "visible" || o == "auto") {
		return skia.NonZeroRect{}, false
	}

	isSVG := useNode.hasTagName(eidSvg)
	// A nested `svg` with only a `viewBox` and no rectangle (x, y, width, height) is not clipped; one
	// referenced by a `use` is, by the `use` bounds.
	if isSVG && !state.hasUseW && !state.hasUseH {
		if !(useNode.HasAttribute(aidWidth) && useNode.HasAttribute(aidHeight)) {
			return skia.NonZeroRect{}, false
		}
	}

	x := useNode.convertUserLength(aidX, state, stLengthZero())
	y := useNode.convertUserLength(aidY, state, stLengthZero())
	w, h := useNodeSize(useNode, state)

	if isSVG {
		// A `use` size overrides the `svg` one.
		if state.hasUseW {
			w = state.useW
		}
		if state.hasUseH {
			h = state.useH
		}
	}

	if !isValidLengthF32(w) || !isValidLengthF32(h) {
		return skia.NonZeroRect{}, false
	}
	return skia.NonZeroRectFromXYWH(x, y, w, h)
}

// useNodeSize is use_node_size: `width` and `height`, 100% by default.
func useNodeSize(node svgNode, state *converterState) (float32, float32) {
	def := stNewLength(100, stLengthUnitPercent)
	return node.convertUserLength(aidWidth, state, def), node.convertUserLength(aidHeight, state, def)
}

// viewboxTransform is viewbox_transform: linked's view box mapped onto node's size.
func viewboxTransform(node, linked svgNode, state *converterState) (skia.Transform, bool) {
	w, h := useNodeSize(node, state)
	if node.hasTagName(eidSvg) {
		// A `use` size overrides the `svg` one.
		if state.hasUseW {
			w = state.useW
		}
		if state.hasUseH {
			h = state.useH
		}
	}

	size, ok := skia.SizeFromWH(w, h)
	if !ok {
		return skia.Transform{}, false
	}
	rect, ok := linked.parseViewbox()
	if !ok {
		return skia.Transform{}, false
	}
	aspect, ok := linked.attrAspectRatio(aidPreserveAspectRatio)
	if !ok {
		aspect = stDefaultAspectRatio()
	}
	return viewBox{rect: rect, aspect: aspect}.toTransform(size), true
}
