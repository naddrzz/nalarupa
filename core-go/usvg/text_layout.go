// Port of usvg 0.47 src/text/layout.rs (with the vendored Nalarupa linear code-point patch) and the font
// resolver of src/text/mod.rs: bidi reordering, shaping with font fallback, glyph clusters, spacing,
// positioning (linear and on a path), baselines and decorations.

package usvg

import (
	"math"
	"slices"
	"sort"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// FontResolver resolves fonts for `text` elements (usvg's FontResolver). SelectFont picks the face for a
// font specification; SelectFallback picks a face for a character no used face has: not one of used,
// close to used[0], with c.
type FontResolver struct {
	SelectFont     func(font *Font, db *text.Database) (text.FaceID, bool)
	SelectFallback func(c rune, used []text.FaceID, db *text.Database) (text.FaceID, bool)
}

// DefaultFontResolver is FontResolver::default().
func DefaultFontResolver() FontResolver {
	return FontResolver{SelectFont: DefaultFontSelector(), SelectFallback: DefaultFallbackSelector()}
}

// DefaultFontSelector is FontResolver::default_font_selector: the database query over the font's
// families followed by serif.
func DefaultFontSelector() func(*Font, *text.Database) (text.FaceID, bool) {
	return func(font *Font, db *text.Database) (text.FaceID, bool) {
		families := make([]text.Family, 0, len(font.families)+1)
		for _, f := range font.families {
			families = append(families, text.Family{Generic: f.Generic, Name: f.Name})
		}
		families = append(families, text.Family{Generic: text.GenericSerif})
		var st text.Style
		switch font.style {
		case FontStyleItalic:
			st = text.StyleItalic
		case FontStyleOblique:
			st = text.StyleOblique
		default:
			st = text.StyleNormal
		}
		return db.Query(families, font.weight, uint16(font.stretch)+1, st)
	}
}

// DefaultFallbackSelector is FontResolver::default_fallback_selector.
func DefaultFallbackSelector() func(rune, []text.FaceID, *text.Database) (text.FaceID, bool) {
	return func(c rune, used []text.FaceID, db *text.Database) (text.FaceID, bool) {
		return db.DefaultFallback(c, used)
	}
}

// FontsResolver is the resolver the Rust core passes (text.rs `Fonts::resolver`): the default font
// selector, then the memoized Noto-first fallback of f. Options.FontDB must be f.Database().
func FontsResolver(f *text.Fonts) FontResolver {
	return FontResolver{
		SelectFont: DefaultFontSelector(),
		SelectFallback: func(c rune, used []text.FaceID, _ *text.Database) (text.FaceID, bool) {
			return f.Fallback(c, used)
		},
	}
}

// resolvedFont is ResolvedFont; the metrics are in font units.
type resolvedFont struct {
	id                  text.FaceID
	unitsPerEm          uint16
	ascent, descent     int16
	xHeight             uint16
	underlinePosition   int16
	underlineThickness  uint16
	lineThroughPosition int16
	subscriptOffset     int16
	superscriptOffset   int16
}

// saturateI16 is `f as i16`: saturating, NaN gives 0.
func saturateI16(f float32) int16 {
	switch {
	case f != f:
		return 0
	case f >= 32767:
		return 32767
	case f <= -32768:
		return -32768
	}
	return int16(f)
}

// loadFont is DatabaseExt::load_font.
func loadFont(db *text.Database, id text.FaceID) (*resolvedFont, bool) {
	m, ok := db.Metrics(id)
	if !ok || m.UnitsPerEm == 0 {
		return nil, false
	}
	f := &resolvedFont{id: id, unitsPerEm: m.UnitsPerEm, ascent: m.Ascender, descent: m.Descender}
	if m.HasXHeight && m.XHeight > 0 {
		f.xHeight = uint16(m.XHeight)
	} else {
		// Not set: 45% of the height, as Firefox does.
		v := float32(float32(f.ascent-f.descent) * 0.45)
		if !(v > -2147483904 && v < 2147483648) {
			v = 0 // `as i32` saturates; the result fails the u16 range below anyway
		}
		i := int32(v)
		if i <= 0 || i > 0xFFFF {
			return nil, false
		}
		f.xHeight = uint16(i)
	}
	if m.HasStrikeout {
		f.lineThroughPosition = m.Strikeout.Position
	} else {
		f.lineThroughPosition = int16(f.xHeight) / 2
	}
	if m.HasUnderline {
		f.underlinePosition = m.Underline.Position
		if m.Underline.Thickness > 0 {
			f.underlineThickness = uint16(m.Underline.Thickness)
		} else {
			f.underlineThickness = f.unitsPerEm / 12
		}
	} else {
		f.underlinePosition = -int16(f.unitsPerEm) / 9
		f.underlineThickness = f.unitsPerEm / 12
	}
	// Generic offsets (upem / 0.2 and / 0.4) unless OS/2 has them.
	f.subscriptOffset = saturateI16(float32(math.Round(float64(float32(m.UnitsPerEm) / 0.2))))
	f.superscriptOffset = saturateI16(float32(math.Round(float64(float32(m.UnitsPerEm) / 0.4))))
	if m.HasScriptMetrics {
		f.subscriptOffset = m.SubscriptYOffset
		f.superscriptOffset = m.SuperscriptYOffset
	}
	return f, true
}

func (f *resolvedFont) scale(fontSize float32) float32 { return fontSize / float32(f.unitsPerEm) }
func (f *resolvedFont) ascentAt(fs float32) float32    { return float32(float32(f.ascent) * f.scale(fs)) }
func (f *resolvedFont) descentAt(fs float32) float32 {
	return float32(float32(f.descent) * f.scale(fs))
}
func (f *resolvedFont) height(fs float32) float32 { return f.ascentAt(fs) - f.descentAt(fs) }
func (f *resolvedFont) xHeightAt(fs float32) float32 {
	return float32(float32(f.xHeight) * f.scale(fs))
}
func (f *resolvedFont) underlinePositionAt(fs float32) float32 {
	return float32(float32(f.underlinePosition) * f.scale(fs))
}
func (f *resolvedFont) underlineThicknessAt(fs float32) float32 {
	return float32(float32(f.underlineThickness) * f.scale(fs))
}
func (f *resolvedFont) lineThroughPositionAt(fs float32) float32 {
	return float32(float32(f.lineThroughPosition) * f.scale(fs))
}
func (f *resolvedFont) subscriptOffsetAt(fs float32) float32 {
	return float32(float32(f.subscriptOffset) * f.scale(fs))
}
func (f *resolvedFont) superscriptOffsetAt(fs float32) float32 {
	return float32(float32(f.superscriptOffset) * f.scale(fs))
}

func (f *resolvedFont) dominantBaselineShift(b DominantBaseline, fs float32) float32 {
	a := AlignmentBaselineAuto
	switch b {
	case DominantBaselineIdeographic:
		a = AlignmentBaselineIdeographic
	case DominantBaselineAlphabetic:
		a = AlignmentBaselineAlphabetic
	case DominantBaselineHanging:
		a = AlignmentBaselineHanging
	case DominantBaselineMathematical:
		a = AlignmentBaselineMathematical
	case DominantBaselineCentral:
		a = AlignmentBaselineCentral
	case DominantBaselineMiddle:
		a = AlignmentBaselineMiddle
	case DominantBaselineTextAfterEdge:
		a = AlignmentBaselineTextAfterEdge
	case DominantBaselineTextBeforeEdge:
		a = AlignmentBaselineTextBeforeEdge
	}
	return f.alignmentBaselineShift(a, fs)
}

// alignmentBaselineShift uses Chrome's hardcoded baselines, as usvg does: few fonts have BASE or bsln
// tables, and browsers disagree on the rest.
func (f *resolvedFont) alignmentBaselineShift(a AlignmentBaseline, fs float32) float32 {
	switch a {
	case AlignmentBaselineBeforeEdge, AlignmentBaselineTextBeforeEdge:
		return f.ascentAt(fs)
	case AlignmentBaselineMiddle:
		return float32(f.xHeightAt(fs) * 0.5)
	case AlignmentBaselineCentral:
		return f.ascentAt(fs) - float32(f.height(fs)*0.5)
	case AlignmentBaselineAfterEdge, AlignmentBaselineTextAfterEdge, AlignmentBaselineIdeographic:
		return f.descentAt(fs)
	case AlignmentBaselineHanging:
		return float32(f.ascentAt(fs) * 0.8)
	case AlignmentBaselineMathematical:
		return float32(f.ascentAt(fs) * 0.5)
	}
	return 0
}

// textGlyph is layout.rs's Glyph: a glyph id and its metrics in font units.
type textGlyph struct {
	id         uint16
	byteIdx    int
	clusterLen int
	text       string
	dx, dy     int32
	width      int32
	font       *resolvedFont
}

func (g *textGlyph) isMissing() bool { return g.id == 0 }

// glyphCluster is GlyphCluster.
type glyphCluster struct {
	byteIdx          int
	codepoint        rune
	width, advance   float32
	ascent, descent  float32
	hasRelativeShift bool
	glyphs           []PositionedGlyph
	transform        skia.Transform
	pathTransform    skia.Transform
	visible          bool
}

func (c *glyphCluster) height() float32 { return c.ascent - c.descent }

func (c *glyphCluster) fullTransform() skia.Transform { return c.pathTransform.PostConcat(c.transform) }

type fontsCache map[string]*resolvedFont

func spanContains(span *TextSpan, byteIdx int) bool {
	return byteIdx >= span.start && byteIdx < span.end
}

func chunkSpanAt(chunk *TextChunk, byteIdx int) *TextSpan {
	for i := range chunk.spans {
		if spanContains(&chunk.spans[i], byteIdx) {
			return &chunk.spans[i]
		}
	}
	return nil
}

// codePoints is ByteIndex::code_points (the Nalarupa patch): the code point index at each char start.
func codePoints(s string) []int {
	cps := make([]int, len(s))
	cp := 0
	for i := range s {
		cps[i] = cp
		cp++
	}
	return cps
}

func charFrom(s string, byteIdx int) rune {
	r, _ := utf8.DecodeRuneInString(s[byteIdx:])
	return r
}

// layoutText is layout_text: the laid-out spans and the text bounding box from glyph metrics; false
// when nothing has a bounding box.
func layoutText(t *Text, resolver *FontResolver, db *text.Database) ([]Span, skia.NonZeroRect, bool) {
	fonts := fontsCache{}
	for ci := range t.chunks {
		for si := range t.chunks[ci].spans {
			f := &t.chunks[ci].spans[si].font
			k := f.key()
			if _, ok := fonts[k]; ok {
				continue
			}
			if resolver.SelectFont == nil {
				continue
			}
			if id, ok := resolver.SelectFont(f, db); ok {
				if rf, ok := loadFont(db, id); ok {
					fonts[k] = rf
				}
			}
		}
	}

	var spans []Span
	charOffset := 0
	var lastX, lastY float32
	bb := defaultBBox()
	for ci := range t.chunks {
		chunk := &t.chunks[ci]
		var x, y float32
		if chunk.textFlow == nil {
			x, y = lastX, lastY
			if chunk.hasX {
				x = chunk.x
			}
			if chunk.hasY {
				y = chunk.y
			}
		}

		clusters := processChunk(chunk, fonts, resolver, db)
		if len(clusters) == 0 {
			charOffset += utf8.RuneCountInString(chunk.text)
			continue
		}

		applyWritingMode(t.writingMode, clusters)
		applyLetterSpacing(chunk, clusters)
		applyWordSpacing(chunk, clusters)
		applyLengthAdjust(chunk, clusters)
		curX, curY := resolveClustersPositions(t, chunk, charOffset, t.writingMode, fonts, clusters)

		textTS := skia.Identity()
		if t.writingMode == WritingModeTopToBottom && chunk.textFlow == nil {
			textTS = textTS.PreRotateAt(90, x, y)
		}

		for si := range chunk.spans {
			span := &chunk.spans[si]
			font, ok := fonts[span.font.key()]
			if !ok {
				continue
			}
			decorationSpans := collectDecorationSpans(span, clusters)

			spanTS := textTS.PreTranslate(x, y)
			if chunk.textFlow == nil {
				// Shift the transform rather than the clusters, which can be rotated.
				spanTS = spanTS.PreTranslate(0, resolveBaseline(span, font, t.writingMode))
			}

			var underline, overline, lineThrough *Path
			if d := span.decoration.underline; d != nil {
				var offset float32
				if t.writingMode == WritingModeLeftToRight {
					offset = -font.underlinePositionAt(span.fontSize)
				} else {
					offset = font.height(span.fontSize) / 2
				}
				if p, ok := convertDecoration(offset, span, font, *d, decorationSpans, spanTS); ok {
					bb = bb.expand(bboxFromRect(p.data.Bounds()))
					underline = p
				}
			}
			if d := span.decoration.overline; d != nil {
				var offset float32
				if t.writingMode == WritingModeLeftToRight {
					offset = -font.ascentAt(span.fontSize)
				} else {
					offset = -font.height(span.fontSize) / 2
				}
				if p, ok := convertDecoration(offset, span, font, *d, decorationSpans, spanTS); ok {
					bb = bb.expand(bboxFromRect(p.data.Bounds()))
					overline = p
				}
			}
			if d := span.decoration.lineThrough; d != nil {
				var offset float32
				if t.writingMode == WritingModeLeftToRight {
					offset = -font.lineThroughPositionAt(span.fontSize)
				}
				if p, ok := convertDecoration(offset, span, font, *d, decorationSpans, spanTS); ok {
					bb = bb.expand(bboxFromRect(p.data.Bounds()))
					lineThrough = p
				}
			}

			fill := span.fill
			if fill != nil {
				// fill-rule does not apply to text.
				f := *fill
				f.rule = FillRuleNonZero
				fill = &f
			}

			fragments, spanBBox, ok := convertSpan(span, clusters, spanTS)
			if !ok {
				continue
			}
			bb = bb.expand(bboxFromNonZeroRect(spanBBox))
			n := 0
			for _, gc := range fragments {
				n += len(gc.glyphs)
			}
			glyphs := make([]PositionedGlyph, 0, n)
			for _, gc := range fragments {
				clusterTS := gc.fullTransform()
				for _, g := range gc.glyphs {
					g.clusterTS = clusterTS
					g.spanTS = spanTS
					glyphs = append(glyphs, g)
				}
			}
			spans = append(spans, Span{
				fill:              fill,
				stroke:            span.stroke,
				paintOrder:        span.paintOrder,
				fontSize:          span.fontSize,
				variations:        span.font.variations,
				fontOpticalSizing: span.fontOpticalSizing,
				visible:           span.visible,
				positionedGlyphs:  glyphs,
				underline:         underline,
				overline:          overline,
				lineThrough:       lineThrough,
			})
		}

		charOffset += utf8.RuneCountInString(chunk.text)
		if t.writingMode == WritingModeTopToBottom && chunk.textFlow == nil {
			curX, curY = curY, curX
		}
		lastX = x + curX
		lastY = y + curY
	}

	r, ok := bb.toNonZeroRect()
	return spans, r, ok
}

// convertSpan is convert_span: the span's visible clusters and the bounding box of every visible
// cluster of the chunk from font metrics (not glyph outlines).
func convertSpan(span *TextSpan, clusters []glyphCluster, textTS skia.Transform) ([]glyphCluster, skia.NonZeroRect, bool) {
	spanClusters := make([]glyphCluster, 0, len(clusters))
	b := skia.NewPathBuilder()
	for i := range clusters {
		c := &clusters[i]
		if !c.visible {
			continue
		}
		if spanContains(span, c.byteIdx) {
			spanClusters = append(spanClusters, *c)
		}
		advance := c.advance
		if advance <= 0 {
			advance = 1
		}
		if r, ok := skia.NonZeroRectFromXYWH(0, -c.ascent, advance, c.height()); ok {
			if r, ok := r.Transform(c.fullTransform()); ok {
				b.PushRect(r.ToRect())
			}
		}
	}
	bboxes := b.Finish()
	if bboxes == nil {
		return nil, skia.NonZeroRect{}, false
	}
	if bboxes = bboxes.Transform(textTS); bboxes == nil {
		return nil, skia.NonZeroRect{}, false
	}
	tight, ok := bboxes.ComputeTightBounds()
	if !ok {
		return nil, skia.NonZeroRect{}, false
	}
	r, ok := tight.ToNonZeroRect()
	return spanClusters, r, ok
}

// decorationSpan is DecorationSpan: a decoration line without its thickness.
type decorationSpan struct {
	width     float32
	transform skia.Transform
}

func collectDecorationSpans(span *TextSpan, clusters []glyphCluster) []decorationSpan {
	var spans []decorationSpan
	started := false
	var width float32
	transform := skia.Identity()
	for i := range clusters {
		c := &clusters[i]
		if spanContains(span, c.byteIdx) {
			if started && c.hasRelativeShift {
				started = false
				spans = append(spans, decorationSpan{width, transform})
			}
			if !started {
				width = c.advance
				started = true
				transform = c.transform
			} else {
				width += c.advance
			}
		} else if started {
			spans = append(spans, decorationSpan{width, transform})
			started = false
		}
	}
	if started {
		spans = append(spans, decorationSpan{width, transform})
	}
	return spans
}

// convertDecoration is convert_decoration.
func convertDecoration(dy float32, span *TextSpan, font *resolvedFont, decoration TextDecorationStyle, spans []decorationSpan, transform skia.Transform) (*Path, bool) {
	thickness := font.underlineThicknessAt(span.fontSize)
	b := skia.NewPathBuilder()
	for _, ds := range spans {
		rect, ok := skia.NonZeroRectFromXYWH(0, -thickness/2, ds.width, thickness)
		if !ok {
			continue // a decoration span with a malformed bbox
		}
		ts := ds.transform.PreTranslate(0, dy)
		p := skia.PathFromRect(rect.ToRect()).Transform(ts)
		if p == nil {
			continue
		}
		b.PushPath(p)
	}
	data := b.Finish()
	if data == nil {
		return nil, false
	}
	if data = data.Transform(transform); data == nil {
		return nil, false
	}
	return newPath("", span.visible, decoration.fill, decoration.stroke, PaintOrderFillAndStroke,
		DefaultShapeRendering, data, skia.Identity())
}

func resolveClustersPositions(t *Text, chunk *TextChunk, charOffset int, wm WritingMode, fonts fontsCache, clusters []glyphCluster) (float32, float32) {
	if chunk.textFlow == nil {
		return resolveClustersPositionsHorizontal(t, chunk, charOffset, wm, clusters)
	}
	return resolveClustersPositionsPath(t, chunk, charOffset, chunk.textFlow, wm, fonts, clusters)
}

func clustersLength(clusters []glyphCluster) float32 {
	var w float32
	for i := range clusters {
		w += clusters[i].advance
	}
	return w
}

func resolveClustersPositionsHorizontal(t *Text, chunk *TextChunk, offset int, wm WritingMode, clusters []glyphCluster) (float32, float32) {
	x := processAnchor(chunk.anchor, clustersLength(clusters))
	var y float32
	cps := codePoints(chunk.text)
	for i := range clusters {
		c := &clusters[i]
		cp := offset + cps[c.byteIdx]
		if cp < len(t.dx) && cp < len(t.dy) {
			dx, dy := t.dx[cp], t.dy[cp]
			if wm == WritingModeLeftToRight {
				x += dx
				y += dy
			} else {
				y -= dx
				x += dy
			}
			c.hasRelativeShift = !approxZeroUlpsF32(dx, 4) || !approxZeroUlpsF32(dy, 4)
		}
		c.transform = c.transform.PreTranslate(x, y)
		if cp < len(t.rotate) {
			if angle := t.rotate[cp]; !approxZeroUlpsF32(angle, 4) {
				c.transform = c.transform.PreRotate(angle)
				c.hasRelativeShift = true
			}
		}
		x += c.advance
	}
	return x, y
}

// resolveBaseline matches Chrome rather than the SVG spec, which browsers implement differently once
// dominant-baseline and alignment-baseline mix.
func resolveBaseline(span *TextSpan, font *resolvedFont, wm WritingMode) float32 {
	shift := -resolveBaselineShift(span.baselineShift, font, span.fontSize)
	// Vertical layout is not supported.
	if wm == WritingModeLeftToRight {
		if span.alignmentBaseline == AlignmentBaselineAuto || span.alignmentBaseline == AlignmentBaselineBaseline {
			shift += font.dominantBaselineShift(span.dominantBaseline, span.fontSize)
		} else {
			shift += font.alignmentBaselineShift(span.alignmentBaseline, span.fontSize)
		}
	}
	return shift
}

func resolveBaselineShift(baselines []BaselineShift, font *resolvedFont, fontSize float32) float32 {
	var shift float32
	for i := len(baselines) - 1; i >= 0; i-- {
		switch b := baselines[i]; b.Kind {
		case BaselineShiftSubscript:
			shift -= font.subscriptOffsetAt(fontSize)
		case BaselineShiftSuperscript:
			shift += font.superscriptOffsetAt(fontSize)
		case BaselineShiftNumber:
			shift += b.Number
		}
	}
	return shift
}

func resolveClustersPositionsPath(t *Text, chunk *TextChunk, charOffset int, path *TextPath, wm WritingMode, fonts fontsCache, clusters []glyphCluster) (float32, float32) {
	var lastX, lastY, dy float32
	// The chunk's x (horizontal) or y (vertical) is an extra offset along the path.
	var chunkOffset float32
	if wm == WritingModeLeftToRight {
		if chunk.hasX {
			chunkOffset = chunk.x
		}
	} else if chunk.hasY {
		chunkOffset = chunk.y
	}
	startOffset := chunkOffset + path.startOffset + processAnchor(chunk.anchor, clustersLength(clusters))

	cps := codePoints(chunk.text)
	normals := collectNormals(t, chunk, clusters, path.path, charOffset, startOffset)
	for i := range clusters {
		if i >= len(normals) {
			break
		}
		c := &clusters[i]
		n := normals[i]
		if n == nil {
			// Hide clusters outside the text path.
			c.visible = false
			continue
		}
		// Each cluster breaks the decoration line on a path.
		c.hasRelativeShift = true
		origTS := c.transform

		// Clusters rotate around their x midpoint on the baseline.
		halfWidth := c.width / 2
		c.transform = skia.Identity().PreTranslate(n.x-halfWidth, n.y)
		c.transform = c.transform.PreRotateAt(n.angle, halfWidth, 0)

		cp := charOffset + cps[c.byteIdx]
		if cp < len(t.dy) {
			dy += t.dy[cp]
		}

		var baselineShift float32
		if span := chunkSpanAt(chunk, c.byteIdx); span != nil {
			if font, ok := fonts[span.font.key()]; ok {
				baselineShift = -resolveBaseline(span, font, wm)
			}
		}

		// Shift only by dy: dx is already in the offset along the path.
		if !approxZeroUlpsF32(dy, 4) || !approxZeroUlpsF32(baselineShift, 4) {
			shiftY := float32(float64(dy - baselineShift))
			c.transform = c.transform.PreTranslate(0, shiftY)
		}
		if cp < len(t.rotate) {
			if angle := t.rotate[cp]; !approxZeroUlpsF32(angle, 4) {
				c.transform = c.transform.PreRotate(angle)
			}
		}
		// A lengthAdjust transform applies after the text-on-path positioning.
		c.transform = c.transform.PreConcat(origTS)

		lastX = n.x + c.advance
		lastY = n.y
	}
	return lastX, lastY
}

func processAnchor(a TextAnchor, width float32) float32 {
	switch a {
	case TextAnchorMiddle:
		return -width / 2
	case TextAnchorEnd:
		return -width
	}
	return 0
}

type pathNormal struct{ x, y, angle float32 }

// collectNormals is collect_normals: the position and angle on the path at each cluster's x midpoint,
// nil for clusters before the path start or past its end.
func collectNormals(t *Text, chunk *TextChunk, clusters []glyphCluster, path *skia.Path, charOffset int, offset float32) []*pathNormal {
	cps := codePoints(chunk.text)
	offsets := make([]float64, 0, len(clusters))
	normals := make([]*pathNormal, 0, len(clusters))
	advance := offset
	for i := range clusters {
		c := &clusters[i]
		halfWidth := c.width / 2
		cp := charOffset + cps[c.byteIdx]
		if cp < len(t.dx) {
			advance += t.dx[cp]
		}
		o := advance + halfWidth
		// Clusters outside the path have no normals.
		if o < 0 {
			normals = append(normals, nil)
		}
		offsets = append(offsets, float64(o))
		advance += c.advance
	}

	pts := path.Points()
	prevMX, prevMY := pts[0].X, pts[0].Y
	prevX, prevY := prevMX, prevMY

	// The accuracy follows the scale: a tiny path scaled up needs a finer one. 1 is the floor, to avoid
	// dividing by zero or a low accuracy.
	sx, sy := t.absTransform.GetScale()
	accuracy := float64(float32(0.5) / maxF32(sqrt32(float32(sx*sy)), 1))

	length := 0.0
	it := path.Segments()
	for {
		seg, ok := it.Next()
		if !ok {
			break
		}
		var curve kurboCubic
		switch seg.Verb {
		case skia.VerbMove:
			prevMX, prevMY = seg.Pts[0].X, seg.Pts[0].Y
			prevX, prevY = prevMX, prevMY
			continue
		case skia.VerbLine:
			curve = kurboCubicFromLine(prevX, prevY, seg.Pts[0].X, seg.Pts[0].Y)
		case skia.VerbQuad:
			curve = kurboQuad{
				kurboPoint{float64(prevX), float64(prevY)},
				kurboPoint{float64(seg.Pts[0].X), float64(seg.Pts[0].Y)},
				kurboPoint{float64(seg.Pts[1].X), float64(seg.Pts[1].Y)},
			}.raise()
		case skia.VerbCubic:
			curve = kurboCubic{
				kurboPoint{float64(prevX), float64(prevY)},
				kurboPoint{float64(seg.Pts[0].X), float64(seg.Pts[0].Y)},
				kurboPoint{float64(seg.Pts[1].X), float64(seg.Pts[1].Y)},
				kurboPoint{float64(seg.Pts[2].X), float64(seg.Pts[2].Y)},
			}
		case skia.VerbClose:
			curve = kurboCubicFromLine(prevX, prevY, prevMX, prevMY)
		}

		curveLen := curve.arclen(accuracy)
		for _, o := range offsets[min(len(normals), len(offsets)):] {
			if o >= length && o <= length+curveLen {
				tt := curve.invArclen(o-length, accuracy)
				// Some rounding error may occur.
				if tt < 0 {
					tt = 0
				} else if tt > 1 {
					tt = 1
				}
				pos := curve.eval(tt)
				d := curve.deriv().eval(tt)
				angle := stToDegrees(stAtan2(d.X, -d.Y)) - 90
				normals = append(normals, &pathNormal{float32(pos.X), float32(pos.Y), float32(angle)})
				if len(normals) == len(offsets) {
					break
				}
			}
		}
		length += curveLen
		prevX, prevY = float32(curve.P3.X), float32(curve.P3.Y)
	}

	// The path ended: the remaining clusters have no normals.
	for len(normals) < len(offsets) {
		normals = append(normals, nil)
	}
	return normals
}

// processChunk is process_chunk: bidi reordering, shaping and glyph clustering of a chunk, with every
// cluster at the origin. The whole chunk is shaped with each span's font (bidi runs can interleave
// spans), and each span's clusters are then taken from its own shaping.
func processChunk(chunk *TextChunk, fonts fontsCache, resolver *FontResolver, db *text.Database) []glyphCluster {
	var glyphs []textGlyph
	positions := map[int]bool{}
	for si := range chunk.spans {
		span := &chunk.spans[si]
		font, ok := fonts[span.font.key()]
		if !ok {
			continue
		}
		tmp := shapeText(chunk.text, font, span.smallCaps, span.applyKerning, span.font.variations, span.fontSize,
			span.fontOpticalSizing, resolver, db)
		if len(glyphs) == 0 {
			glyphs = tmp
			continue
		}
		clear(positions)
		for k := 0; k < len(tmp); k++ {
			ng := tmp[k]
			if !spanContains(span, ng.byteIdx) {
				continue
			}
			idx := -1
			for j := range glyphs {
				if glyphs[j].byteIdx == ng.byteIdx {
					idx = j
					break
				}
			}
			if idx < 0 || positions[idx] {
				continue
			}
			positions[idx] = true
			prevLen := glyphs[idx].clusterLen
			if prevLen < ng.clusterLen {
				// The new font has fewer glyphs for this cluster: remove the rest.
				for r := 1; r < ng.clusterLen; r++ {
					if idx+1 < len(glyphs) {
						glyphs = append(glyphs[:idx+1], glyphs[idx+2:]...)
					}
				}
			} else if prevLen > ng.clusterLen {
				// The new font has more glyphs: insert them after this one.
				for j := 1; j < prevLen; j++ {
					if k+1 < len(tmp) {
						k++
						g := tmp[k]
						at := min(idx+j, len(glyphs))
						glyphs = append(glyphs, textGlyph{})
						copy(glyphs[at+1:], glyphs[at:])
						glyphs[at] = g
					}
				}
			}
			glyphs[idx] = ng
		}
	}

	clusters := make([]glyphCluster, 0, len(glyphs))
	for start := 0; start < len(glyphs); {
		end := start
		for end < len(glyphs) && glyphs[end].byteIdx == glyphs[start].byteIdx {
			end++
		}
		if span := chunkSpanAt(chunk, glyphs[start].byteIdx); span != nil {
			clusters = append(clusters, formGlyphCluster(glyphs[start:end], chunk.text, span.fontSize))
		}
		start = end
	}
	return clusters
}

func applyLengthAdjust(chunk *TextChunk, clusters []glyphCluster) {
	horizontal := chunk.textFlow == nil
	for si := range chunk.spans {
		span := &chunk.spans[si]
		if !span.hasTextLength {
			continue
		}
		target := span.textLength
		var indexes []int
		for i := span.start; i < span.end; i++ {
			for j := range clusters {
				if clusters[j].byteIdx == i {
					indexes = append(indexes, j)
					break
				}
			}
		}
		// Complex scripts have multi-code-point clusters: remove duplicates.
		sort.Ints(indexes)
		indexes = dedupInts(indexes)
		var width float32
		for _, i := range indexes {
			// The cluster width, not its advance: letter and word spacing are discarded.
			width += clusters[i].width
		}
		if len(indexes) == 0 {
			continue
		}
		if span.lengthAdjust == LengthAdjustSpacing {
			var factor float32
			if len(indexes) > 1 {
				factor = (target - width) / float32(len(indexes)-1)
			}
			for _, i := range indexes {
				clusters[i].advance = clusters[i].width + factor
			}
		} else {
			factor := target / width
			// Prevent multiplying by zero.
			if factor < 0.001 {
				continue
			}
			for _, i := range indexes {
				clusters[i].transform = clusters[i].transform.PreScale(factor, 1)
				// A hack for the text-on-path algorithm.
				if !horizontal {
					clusters[i].advance = float32(clusters[i].advance * factor)
					clusters[i].width = float32(clusters[i].width * factor)
				}
			}
		}
	}
}

func dedupInts(s []int) []int {
	if len(s) == 0 {
		return s
	}
	out := s[:1]
	for _, v := range s[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// applyWritingMode rotates clusters by the Unicode Vertical_Orientation property (UAX #50).
func applyWritingMode(wm WritingMode, clusters []glyphCluster) {
	if wm != WritingModeTopToBottom {
		return
	}
	for i := range clusters {
		c := &clusters[i]
		if voIsUpright(c.codepoint) {
			// Center on the vertical axis, then rotate by 90 degrees around the center.
			ts := skia.Identity().PreTranslate(0, (c.ascent+c.descent)/2)
			ts = ts.PreRotateAt(-90, c.width/2, -(c.ascent+c.descent)/2)
			c.pathTransform = ts
			// Move the baseline to the middle and make the height equal to the width.
			c.ascent = c.width / 2
			c.descent = -c.width / 2
		} else {
			// How other applications shift rotated characters in top-to-bottom mode.
			c.transform = c.transform.PreTranslate(0, (c.ascent+c.descent)/2)
		}
	}
}

// applyLetterSpacing applies `letter-spacing` to the clusters of scripts that allow it (CSS Text 3
// cursive tracking), checking each cluster's first code point.
func applyLetterSpacing(chunk *TextChunk, clusters []glyphCluster) {
	any := false
	for i := range chunk.spans {
		any = any || !approxZeroUlpsF32(chunk.spans[i].letterSpacing, 4)
	}
	if !any {
		return
	}
	n := len(clusters)
	for i := range clusters {
		c := &clusters[i]
		if !scriptSupportsLetterSpacing(c.codepoint) {
			continue
		}
		span := chunkSpanAt(chunk, c.byteIdx)
		if span == nil {
			continue
		}
		// No spacing after the last cluster: it would affect the bbox and alignment.
		if i != n-1 {
			c.advance += span.letterSpacing
		}
		// A negative advance is undefined; clear it as Chrome does.
		if !isValidLengthF32(c.advance) {
			c.width = 0
			c.advance = 0
			c.glyphs = nil
		}
	}
}

// applyWordSpacing applies `word-spacing` to word separators (CSS Text 3), all after the character.
func applyWordSpacing(chunk *TextChunk, clusters []glyphCluster) {
	any := false
	for i := range chunk.spans {
		any = any || !approxZeroUlpsF32(chunk.spans[i].wordSpacing, 4)
	}
	if !any {
		return
	}
	for i := range clusters {
		c := &clusters[i]
		if isWordSeparator(c.codepoint) {
			if span := chunkSpanAt(chunk, c.byteIdx); span != nil {
				c.advance += span.wordSpacing
			}
		}
	}
}

func isWordSeparator(c rune) bool {
	switch c {
	case 0x0020, 0x00A0, 0x1361, 0x010100, 0x010101, 0x01039F, 0x01091F:
		return true
	}
	return false
}

// scriptSupportsLetterSpacing is script_supports_letter_spacing, with the cursive scripts of
// https://github.com/harfbuzz/harfbuzz/issues/64.
func scriptSupportsLetterSpacing(c rune) bool {
	i := sort.Search(len(noLetterSpacingRanges), func(i int) bool { return noLetterSpacingRanges[i][1] >= c })
	return !(i < len(noLetterSpacingRanges) && noLetterSpacingRanges[i][0] <= c)
}

// voIsUpright is unicode_vo::char_orientation(c) == Upright.
func voIsUpright(c rune) bool {
	i := sort.Search(len(voUprightRanges), func(i int) bool { return voUprightRanges[i][1] >= c })
	return i < len(voUprightRanges) && voUprightRanges[i][0] <= c
}

func formGlyphCluster(glyphs []textGlyph, s string, fontSize float32) glyphCluster {
	var width, x float32
	positioned := make([]PositionedGlyph, 0, len(glyphs))
	for i := range glyphs {
		g := &glyphs[i]
		sx := g.font.scale(fontSize)
		// The first glyph is offset from the origin, the later ones from the current position.
		ts := skia.TransformFromTranslate(x+float32(g.dx), float32(-g.dy))
		positioned = append(positioned, PositionedGlyph{
			glyphTS:    ts,
			clusterTS:  skia.Identity(),
			spanTS:     skia.Identity(),
			unitsPerEm: g.font.unitsPerEm,
			fontSize:   fontSize,
			Font:       g.font.id,
			Text:       g.text,
			ID:         g.id,
		})
		x += float32(g.width)
		if w := float32(float32(g.width) * sx); w > width {
			width = w
		}
	}
	font := glyphs[0].font
	return glyphCluster{
		byteIdx:       glyphs[0].byteIdx,
		codepoint:     charFrom(s, glyphs[0].byteIdx),
		width:         width,
		advance:       width,
		ascent:        font.ascentAt(fontSize),
		descent:       font.descentAt(fontSize),
		transform:     skia.Identity(),
		pathTransform: skia.Identity(),
		glyphs:        positioned,
		visible:       true,
	}
}

// shapeText is shape_text: shaping with the primary font, then missing glyphs replaced from fallback
// fonts until every glyph resolves or no font is left.
func shapeText(s string, font *resolvedFont, smallCaps, kerning bool, variations []FontVariation, fontSize float32,
	opsz FontOpticalSizing, resolver *FontResolver, db *text.Database) []textGlyph {
	glyphs := shapeTextWithFont(s, font, smallCaps, kerning, variations, fontSize, opsz, db)
	used := []text.FaceID{font.id}
	for {
		var c rune = -1
		for i := range glyphs {
			if glyphs[i].isMissing() {
				c = charFrom(s, glyphs[i].byteIdx)
				break
			}
		}
		if c < 0 || resolver.SelectFallback == nil {
			break
		}
		id, ok := resolver.SelectFallback(c, used, db)
		if !ok {
			break
		}
		fb, ok := loadFont(db, id)
		if !ok {
			break
		}
		alt := shapeTextWithFont(s, fb, smallCaps, kerning, variations, fontSize, opsz, db)
		complete := true
		for i := range alt {
			complete = complete && !alt[i].isMissing()
		}
		if complete {
			// Replace all glyphs when all of them matched.
			glyphs = alt
			break
		}
		// Shaping with any font is assumed to give the same number of glyphs, which is good enough.
		if len(glyphs) != len(alt) {
			break
		}
		for i := range glyphs {
			if glyphs[i].isMissing() && !alt[i].isMissing() {
				glyphs[i] = alt[i]
			}
		}
		used = append(used, fb.id)
	}
	return glyphs
}

var tagOpsz = [4]byte{'o', 'p', 's', 'z'}

// shapeTextWithFont is shape_text_with_font: bidi reordering and shaping of each visual run. A face
// with an `opsz` axis gets the font size on it under font-optical-sizing: auto, as browsers do.
func shapeTextWithFont(s string, font *resolvedFont, smallCaps, kerning bool, variations []FontVariation, fontSize float32,
	opsz FontOpticalSizing, db *text.Database) []textGlyph {
	vars := make([]text.Variation, 0, len(variations)+1)
	explicitOpsz := false
	for _, v := range variations {
		vars = append(vars, text.Variation{Tag: v.Tag, Value: v.Value})
		explicitOpsz = explicitOpsz || v.Tag == tagOpsz
	}
	if opsz == FontOpticalSizingAuto && !explicitOpsz && db.HasAxis(font.id, tagOpsz) {
		vars = append(vars, text.Variation{Tag: tagOpsz, Value: fontSize})
	}

	levels, runs := bidiVisualRuns(s)
	var glyphs []textGlyph
	for _, run := range runs {
		sub := s[run.start:run.end]
		if sub == "" {
			continue
		}
		ltr := !levelIsRTL(levels[run.start])
		out, ok := db.Shape(font.id, sub, !ltr, smallCaps, kerning, vars)
		if !ok {
			return nil
		}
		glyphs = slices.Grow(glyphs, len(out))
		for i, g := range out {
			start := g.Cluster
			end := len(sub)
			if ltr {
				if i+1 < len(out) {
					end = out[i+1].Cluster
				}
			} else if i > 0 {
				end = out[i-1].Cluster
			}
			clusterLen := 0
			txt := ""
			if end >= start {
				clusterLen = end - start
				txt = sub[start:end]
			}
			glyphs = append(glyphs, textGlyph{
				byteIdx:    run.start + g.Cluster,
				clusterLen: clusterLen,
				text:       txt,
				id:         uint16(g.ID),
				dx:         g.XOffset,
				dy:         g.YOffset,
				width:      g.XAdvance,
				font:       font,
			})
		}
	}
	return glyphs
}

// textConvert is text::convert: layout (glyphs and the metrics bounding box), then flattening into
// paths (and the stroke bounding box). False drops the text node.
func textConvert(t *Text, opt *Options, cache *converterCache) bool {
	resolver := opt.FontResolver
	spans, bbox, ok := layoutText(t, &resolver, cache.fontdb)
	if !ok {
		return false
	}
	t.layouted = spans
	t.boundingBox = bbox.ToRect()
	abs, ok := bbox.Transform(t.absTransform)
	if !ok {
		return false
	}
	t.absBoundingBox = abs.ToRect()

	group, strokeBBox, ok := flattenText(t, cache)
	if !ok {
		return false
	}
	t.flattened = group
	t.strokeBoundingBox = strokeBBox.ToRect()
	absStroke, ok := strokeBBox.Transform(t.absTransform)
	if !ok {
		return false
	}
	t.absStrokeBoundingBox = absStroke.ToRect()
	return true
}

func sqrt32(x float32) float32 { return float32(math.Sqrt(float64(x))) }
