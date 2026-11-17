// Port of usvg 0.47 src/parser/clippath.rs.

package usvg

import "github.com/naddrzz/nalarupa/core-go/skia"

// convertClipPath is clippath::convert; nil means the clip path is invalid. objectBBox nil is None.
func convertClipPath(node svgNode, state *converterState, objectBBox *skia.NonZeroRect,
	cache *converterCache) *ClipPath {
	// A `clip-path` attribute must reference a `clipPath` element.
	if !node.hasTagName(eidClipPath) {
		return nil
	}

	// The whole clip path is ignored when its transform is invalid.
	transform, ok := resolveClipPathTransform(node, state)
	if !ok {
		return nil
	}

	u, ok := node.attrUnits(aidClipPathUnits)
	if !ok {
		u = unitsUserSpaceOnUse
	}

	// Only userSpaceOnUse clip paths can be shared: an objectBoundingBox one becomes node-specific.
	cacheable := u == unitsUserSpaceOnUse
	if cacheable {
		if clip, ok := cache.clipPaths[node.ElementID()]; ok {
			return clip
		}
	}

	if u == unitsObjectBoundingBox {
		if objectBBox == nil {
			return nil // clipping zero-sized shapes is not allowed
		}
		transform = transform.PreConcat(skia.TransformFromBBox(*objectBBox))
	}

	// Resolve the linked clip path, which must be valid.
	var clipPath *ClipPath
	if link, ok := node.attrNode(aidClipPath); ok {
		clipPath = convertClipPath(link, state, objectBBox, cache)
		if clipPath == nil {
			return nil
		}
	}

	id, ok := newNonEmptyString(node.ElementID())
	if !ok {
		return nil
	}
	// Generate an id only when an objectBoundingBox clip is parsed for the second time.
	if _, seen := cache.clipPaths[id]; !cacheable && seen {
		id = cache.genClipPathID()
	}
	idCopy := id

	clip := &ClipPath{id: id, transform: transform, clipPath: clipPath, root: newEmptyGroup()}

	clipState := *state
	clipState.parentClipPath, clipState.hasParentClipPath = node, true
	convertClipPathElements(node, &clipState, cache, &clip.root)

	if !clip.root.HasChildren() {
		return nil // a clip path without children is invalid
	}
	clip.root.calculateBoundingBoxes()
	cache.clipPaths[idCopy] = clip
	return clip
}

// resolveClipPathTransform is resolve_clip_path_transform: ok is false for an invalid transform
// (attrTransform cannot tell, since it always gives a valid one).
func resolveClipPathTransform(node svgNode, state *converterState) (skia.Transform, bool) {
	value, ok := node.attrStr(aidTransform)
	if !ok {
		return skia.Identity(), true
	}
	ts, err := stParseTransform(value)
	if err != nil {
		return skia.Transform{}, false
	}
	t := skia.TransformFromRow(float32(ts.A), float32(ts.B), float32(ts.C), float32(ts.D),
		float32(ts.E), float32(ts.F))
	if !t.IsValid() {
		return skia.Transform{}, false
	}
	return node.resolveTransform(aidTransform, state), true
}
