// Port of usvg 0.47 src/parser/mask.rs.

package usvg

import "github.com/naddrzz/nalarupa/core-go/skia"

// convertMask is mask::convert; nil means the mask is invalid. objectBBox nil is None.
func convertMask(node svgNode, state *converterState, objectBBox *skia.NonZeroRect, cache *converterCache) *Mask {
	// A `mask` attribute must reference a `mask` element.
	if !node.hasTagName(eidMask) {
		return nil
	}

	u, ok := node.attrUnits(aidMaskUnits)
	if !ok {
		u = unitsObjectBoundingBox
	}
	contentUnits, ok := node.attrUnits(aidMaskContentUnits)
	if !ok {
		contentUnits = unitsUserSpaceOnUse
	}

	// Only userSpaceOnUse masks can be shared: an objectBoundingBox one becomes node-specific.
	cacheable := u == unitsUserSpaceOnUse && contentUnits == unitsUserSpaceOnUse
	if cacheable {
		if mask, ok := cache.masks[node.ElementID()]; ok {
			return mask
		}
	}

	rect, ok := skia.NonZeroRectFromXYWH(
		node.convertLength(aidX, u, state, stNewLength(-10, stLengthUnitPercent)),
		node.convertLength(aidY, u, state, stNewLength(-10, stLengthUnitPercent)),
		node.convertLength(aidWidth, u, state, stNewLength(120, stLengthUnitPercent)),
		node.convertLength(aidHeight, u, state, stNewLength(120, stLengthUnitPercent)),
	)
	if !ok {
		return nil // the mask has an invalid size
	}

	maskAll := false
	if u == unitsObjectBoundingBox {
		if objectBBox != nil {
			rect = rect.BBoxTransform(*objectBBox)
		} else {
			// With objectBoundingBox units and a zero-sized bbox the whole element is masked out.
			// Technically undefined, but Chrome and Firefox do this.
			maskAll = true
		}
	}

	id, ok := newNonEmptyString(node.ElementID())
	if !ok {
		return nil
	}
	// Generate an id only when an objectBoundingBox mask is parsed for the second time.
	if _, seen := cache.masks[id]; !cacheable && seen {
		id = cache.genMaskID()
	}
	idCopy := id

	if maskAll {
		mask := &Mask{id: id, rect: rect, kind: MaskTypeLuminance, root: newEmptyGroup()}
		cache.masks[idCopy] = mask
		return mask
	}

	// Resolve the linked mask, which must be valid.
	var sub *Mask
	if link, ok := node.attrNode(aidMask); ok {
		sub = convertMask(link, state, objectBBox, cache)
		if sub == nil {
			return nil
		}
	}

	kind := MaskTypeLuminance
	if t, ok := node.attrStr(aidMaskType); ok && t == "alpha" {
		kind = MaskTypeAlpha
	}

	mask := &Mask{id: id, rect: rect, kind: kind, mask: sub, root: newEmptyGroup()}

	// objectBoundingBox content units are emulated by a group with a transform around the children.
	var subroot *Group
	if contentUnits == unitsObjectBoundingBox {
		if objectBBox == nil {
			return nil // masking zero-sized shapes is not allowed
		}
		g := newEmptyGroup()
		g.transform = skia.TransformFromBBox(*objectBBox)
		// abs_transform must be set too: it propagates to all children.
		g.absTransform = g.transform
		subroot = &g
	}

	realRoot := &mask.root
	if subroot != nil {
		realRoot = subroot
	}
	convertChildren(node, state, cache, realRoot)
	// A mask without children is invalid at this point; only an objectBoundingBox one on a zero bbox
	// can be empty.
	if !realRoot.HasChildren() {
		return nil
	}

	if subroot != nil {
		subroot.calculateBoundingBoxes()
		mask.root.children = append(mask.root.children, subroot)
	}

	mask.root.calculateBoundingBoxes()
	cache.masks[idCopy] = mask
	return mask
}
