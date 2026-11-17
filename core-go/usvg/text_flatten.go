// Port of usvg 0.47 src/text/flatten.rs: laid-out glyphs into outline paths, grouped per span. Color
// (COLR), SVG and bitmap glyphs are not ported (src/text/colr.rs and the image lookups): such a glyph
// flattens to nothing, so text in a color emoji font differs from Rust.

package usvg

import (
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/text"
)

func resolveTextRenderingMode(t *Text) ShapeRendering {
	if t.renderingMode == TextRenderingOptimizeSpeed {
		return ShapeRenderingCrispEdges
	}
	return ShapeRenderingGeometricPrecision
}

// pushOutlinePaths turns the glyph outlines collected so far into one path of the span.
func pushOutlinePaths(span *Span, b **skia.PathBuilder, children []Node, mode ShapeRendering) []Node {
	builder := *b
	*b = skia.NewPathBuilder()
	if p := builder.Finish(); p != nil {
		if path, ok := newPath("", span.visible, span.fill, span.stroke, span.paintOrder, mode, p, skia.Identity()); ok {
			children = append(children, path)
		}
	}
	return children
}

func withRenderingMode(p *Path, mode ShapeRendering) *Path {
	c := *p
	c.renderingMode = mode
	return &c
}

// flattenText is flatten: the text's group of paths and its stroke bounding box; false when it has no
// stroke bounding box.
func flattenText(t *Text, cache *converterCache) (*Group, skia.NonZeroRect, bool) {
	var children []Node
	mode := resolveTextRenderingMode(t)
	for si := range t.layouted {
		span := &t.layouted[si]
		if span.overline != nil {
			children = append(children, withRenderingMode(span.overline, mode))
		}
		if span.underline != nil {
			children = append(children, withRenderingMode(span.underline, mode))
		}

		// Consecutive outline glyphs go into one path; anything else ends it. The outlines are looked up
		// first, so the path is sized once (a long line otherwise regrows it many times).
		hasExplicitVariations := len(span.variations) != 0
		outlines := make([]*skia.Path, len(span.positionedGlyphs))
		verbs, points := 0, 0
		for gi := range span.positionedGlyphs {
			g := &span.positionedGlyphs[gi]
			if colr, image := cache.glyphImage(g.Font, g.ID); colr || image {
				continue
			}
			// Variations bypass the outline cache: with explicit ones, or an automatic opsz.
			needsVariations := hasExplicitVariations ||
				(span.fontOpticalSizing == FontOpticalSizingAuto && cache.hasOpszAxis(g.Font))
			var outline *skia.Path
			if needsVariations {
				outline = outlineWithVariations(cache.fontdb, g.Font, g.ID, span.variations, g.FontSize(), span.fontOpticalSizing)
			} else {
				outline = cache.fontdbOutline(g.Font, g.ID)
			}
			if outline != nil {
				outlines[gi] = outline
				verbs += outline.Len()
				points += outline.PointsLen()
			}
		}
		b := skia.NewPathBuilderWithCapacity(verbs, points)
		for gi := range span.positionedGlyphs {
			g := &span.positionedGlyphs[gi]
			colr, image := cache.glyphImage(g.Font, g.ID)
			if colr {
				continue // a COLR glyph is a group of its own in usvg; not ported
			}
			if image {
				children = pushOutlinePaths(span, &b, children, mode)
				continue // an SVG or bitmap glyph; not ported
			}
			if outline := outlines[gi]; outline != nil {
				b.PushPathTransformed(outline, g.OutlineTransform())
			}
		}
		children = pushOutlinePaths(span, &b, children, mode)

		if span.lineThrough != nil {
			children = append(children, withRenderingMode(span.lineThrough, mode))
		}
	}

	group := newEmptyGroup()
	group.id = t.id
	group.children = children
	group.calculateBoundingBoxes()
	r, ok := group.strokeBoundingBox.ToNonZeroRect()
	if !ok {
		return nil, skia.NonZeroRect{}, false
	}
	return &group, r, true
}

// outlineWithVariations is DatabaseExt::outline_with_variations: the variations, then opsz at the font
// size under font-optical-sizing: auto unless set explicitly.
func outlineWithVariations(db *text.Database, id text.FaceID, gid uint16, variations []FontVariation, fontSize float32, opsz FontOpticalSizing) *skia.Path {
	vars := make([]text.Variation, 0, len(variations)+1)
	explicitOpsz := false
	for _, v := range variations {
		vars = append(vars, text.Variation{Tag: v.Tag, Value: v.Value})
		explicitOpsz = explicitOpsz || v.Tag == tagOpsz
	}
	if opsz == FontOpticalSizingAuto && !explicitOpsz && db.HasAxis(id, tagOpsz) {
		vars = append(vars, text.Variation{Tag: tagOpsz, Value: fontSize})
	}
	// Memoised per face, glyph and coordinates (text is converted again on every rebuild); callers only read
	// the path.
	p, _ := db.OutlineMemo(id, gid, vars, func() any {
		b := skia.NewPathBuilder()
		if !db.OutlineVaried(id, gid, vars, b) {
			return (*skia.Path)(nil)
		}
		return b.Finish()
	}).(*skia.Path)
	return p
}

type glyphKey struct {
	font text.FaceID
	id   uint16
}

// fontdbOutline is Cache::fontdb_outline: the outline at the default variation, memoized.
func (c *converterCache) fontdbOutline(font text.FaceID, id uint16) *skia.Path {
	k := glyphKey{font, id}
	if p, ok := c.outlines[k]; ok {
		return p
	}
	// Also memoised across parses, per face (text is converted again on every rebuild); callers only read it.
	p, _ := c.fontdb.OutlineDefaultMemo(font, id, func() any {
		b := skia.NewPathBuilder()
		if !c.fontdb.Outline(font, id, b) {
			return (*skia.Path)(nil)
		}
		return b.Finish()
	}).(*skia.Path)
	c.outlines[k] = p
	return p
}

// glyphImage reports, memoized, whether a glyph is drawn from a COLR, SVG or bitmap table.
func (c *converterCache) glyphImage(font text.FaceID, id uint16) (bool, bool) {
	k := glyphKey{font, id}
	if v, ok := c.glyphImages[k]; ok {
		return v[0], v[1]
	}
	colr, image := c.fontdb.GlyphImage(font, id)
	c.glyphImages[k] = [2]bool{colr, image}
	return colr, image
}

// hasOpszAxis is Cache::has_opsz_axis, memoized.
func (c *converterCache) hasOpszAxis(font text.FaceID) bool {
	if v, ok := c.opsz[font]; ok {
		return v
	}
	v := c.fontdb.HasAxis(font, tagOpsz)
	c.opsz[font] = v
	return v
}
