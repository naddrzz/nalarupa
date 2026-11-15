// Port of usvg 0.47 src/tree/text.rs and the public layout types of src/text/layout.rs.

package usvg

import (
	"math"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// FontStretch is the `font-stretch` property, from UltraCondensed (0) to UltraExpanded (8).
type FontStretch uint8

const (
	FontStretchUltraCondensed FontStretch = iota
	FontStretchExtraCondensed
	FontStretchCondensed
	FontStretchSemiCondensed
	FontStretchNormal
	FontStretchSemiExpanded
	FontStretchExpanded
	FontStretchExtraExpanded
	FontStretchUltraExpanded
)

// FontStyle is the `font-style` property; the zero value is the default.
type FontStyle uint8

const (
	FontStyleNormal FontStyle = iota
	FontStyleItalic
	FontStyleOblique
)

// FontVariation is a variation axis setting, such as wght 700.
type FontVariation struct {
	Tag   [4]byte
	Value float32
}

// FontFamily is a family of `font-family`: a generic family or a named one.
type FontFamily struct {
	// Generic is the generic family, or text.GenericNone for a named one.
	Generic text.Generic
	// Name is the family name when Generic is text.GenericNone.
	Name string
}

// String is FontFamily's Display: the generic keyword, or the name in double quotes.
func (f FontFamily) String() string {
	switch f.Generic {
	case text.GenericSerif:
		return "serif"
	case text.GenericSansSerif:
		return "sans-serif"
	case text.GenericCursive:
		return "cursive"
	case text.GenericFantasy:
		return "fantasy"
	case text.GenericMonospace:
		return "monospace"
	}
	return "\"" + f.Name + "\""
}

// Font holds the text font properties.
type Font struct {
	families   []FontFamily
	style      FontStyle
	stretch    FontStretch
	weight     uint16
	variations []FontVariation
}

// Families is never empty: Options.FontFamily is the fallback.
func (f *Font) Families() []FontFamily      { return f.families }
func (f *Font) Style() FontStyle            { return f.style }
func (f *Font) Stretch() FontStretch        { return f.stretch }
func (f *Font) Weight() uint16              { return f.weight }
func (f *Font) Variations() []FontVariation { return f.variations }

// key is the font's identity for the fonts cache (Rust's Hash/Eq on Font; variation values by bits).
func (f *Font) key() string {
	b := make([]byte, 0, 64)
	for _, fam := range f.families {
		b = append(b, byte(fam.Generic))
		b = append(b, fam.Name...)
		b = append(b, 0)
	}
	b = append(b, 1, byte(f.style), byte(f.stretch), byte(f.weight>>8), byte(f.weight))
	for _, v := range f.variations {
		bits := math.Float32bits(v.Value)
		b = append(b, v.Tag[:]...)
		b = append(b, byte(bits>>24), byte(bits>>16), byte(bits>>8), byte(bits))
	}
	return string(b)
}

// DominantBaseline is the `dominant-baseline` property; the zero value is the default.
type DominantBaseline uint8

const (
	DominantBaselineAuto DominantBaseline = iota
	DominantBaselineUseScript
	DominantBaselineNoChange
	DominantBaselineResetSize
	DominantBaselineIdeographic
	DominantBaselineAlphabetic
	DominantBaselineHanging
	DominantBaselineMathematical
	DominantBaselineCentral
	DominantBaselineMiddle
	DominantBaselineTextAfterEdge
	DominantBaselineTextBeforeEdge
)

// AlignmentBaseline is the `alignment-baseline` property; the zero value is the default.
type AlignmentBaseline uint8

const (
	AlignmentBaselineAuto AlignmentBaseline = iota
	AlignmentBaselineBaseline
	AlignmentBaselineBeforeEdge
	AlignmentBaselineTextBeforeEdge
	AlignmentBaselineMiddle
	AlignmentBaselineCentral
	AlignmentBaselineAfterEdge
	AlignmentBaselineTextAfterEdge
	AlignmentBaselineIdeographic
	AlignmentBaselineAlphabetic
	AlignmentBaselineHanging
	AlignmentBaselineMathematical
)

// BaselineShiftKind lists the BaselineShift variants.
type BaselineShiftKind uint8

const (
	BaselineShiftBaseline BaselineShiftKind = iota
	BaselineShiftSubscript
	BaselineShiftSuperscript
	BaselineShiftNumber
)

// BaselineShift is a `baseline-shift` value; Number is set for BaselineShiftNumber.
type BaselineShift struct {
	Kind   BaselineShiftKind
	Number float32
}

// LengthAdjust is the `lengthAdjust` attribute; the zero value is the default.
type LengthAdjust uint8

const (
	LengthAdjustSpacing LengthAdjust = iota
	LengthAdjustSpacingAndGlyphs
)

// FontOpticalSizing is the `font-optical-sizing` property; the zero value is the default.
type FontOpticalSizing uint8

const (
	FontOpticalSizingAuto FontOpticalSizing = iota
	FontOpticalSizingNone
)

// TextDecorationStyle is the fill and stroke of a decoration line.
type TextDecorationStyle struct {
	fill   *Fill
	stroke *Stroke
}

func (s *TextDecorationStyle) Fill() *Fill     { return s.fill }
func (s *TextDecorationStyle) Stroke() *Stroke { return s.stroke }

// TextDecoration holds a span's decoration lines; nil is none.
type TextDecoration struct {
	underline   *TextDecorationStyle
	overline    *TextDecorationStyle
	lineThrough *TextDecorationStyle
}

func (d *TextDecoration) Underline() *TextDecorationStyle   { return d.underline }
func (d *TextDecoration) Overline() *TextDecorationStyle    { return d.overline }
func (d *TextDecoration) LineThrough() *TextDecorationStyle { return d.lineThrough }

// TextSpan is a styled run of a text chunk. Spans do not overlap inside a chunk. fontSize is
// NonZeroPositiveF32; textLength is set when hasTextLength is.
type TextSpan struct {
	start, end        int
	fill              *Fill
	stroke            *Stroke
	paintOrder        PaintOrder
	font              Font
	fontSize          float32
	smallCaps         bool
	applyKerning      bool
	fontOpticalSizing FontOpticalSizing
	decoration        TextDecoration
	dominantBaseline  DominantBaseline
	alignmentBaseline AlignmentBaseline
	baselineShift     []BaselineShift
	visible           bool
	letterSpacing     float32
	wordSpacing       float32
	textLength        float32
	hasTextLength     bool
	lengthAdjust      LengthAdjust
}

// Start and End are byte offsets relative to the parent text chunk.
func (s *TextSpan) Start() int                           { return s.start }
func (s *TextSpan) End() int                             { return s.end }
func (s *TextSpan) Fill() *Fill                          { return s.fill }
func (s *TextSpan) Stroke() *Stroke                      { return s.stroke }
func (s *TextSpan) PaintOrder() PaintOrder               { return s.paintOrder }
func (s *TextSpan) Font() *Font                          { return &s.font }
func (s *TextSpan) FontSize() float32                    { return s.fontSize }
func (s *TextSpan) SmallCaps() bool                      { return s.smallCaps }
func (s *TextSpan) ApplyKerning() bool                   { return s.applyKerning }
func (s *TextSpan) FontOpticalSizing() FontOpticalSizing { return s.fontOpticalSizing }
func (s *TextSpan) Decoration() *TextDecoration          { return &s.decoration }
func (s *TextSpan) DominantBaseline() DominantBaseline   { return s.dominantBaseline }
func (s *TextSpan) AlignmentBaseline() AlignmentBaseline { return s.alignmentBaseline }
func (s *TextSpan) BaselineShift() []BaselineShift       { return s.baselineShift }
func (s *TextSpan) IsVisible() bool                      { return s.visible }
func (s *TextSpan) LetterSpacing() float32               { return s.letterSpacing }
func (s *TextSpan) WordSpacing() float32                 { return s.wordSpacing }
func (s *TextSpan) TextLength() (float32, bool)          { return s.textLength, s.hasTextLength }
func (s *TextSpan) LengthAdjust() LengthAdjust           { return s.lengthAdjust }

// TextAnchor is the `text-anchor` property; the zero value is the default.
type TextAnchor uint8

const (
	TextAnchorStart TextAnchor = iota
	TextAnchorMiddle
	TextAnchorEnd
)

// TextPath is the path of a text-on-path chunk.
type TextPath struct {
	id          string
	startOffset float32
	path        *skia.Path
}

func (p *TextPath) ID() string           { return p.id }
func (p *TextPath) StartOffset() float32 { return p.startOffset }
func (p *TextPath) Path() *skia.Path     { return p.path }

// TextChunk is a run of text that is aligned and bidi-reordered on its own. textFlow nil is the
// linear flow; x and y are set when hasX and hasY are.
type TextChunk struct {
	x, y       float32
	hasX, hasY bool
	anchor     TextAnchor
	spans      []TextSpan
	textFlow   *TextPath
	text       string
}

func (c *TextChunk) X() (float32, bool) { return c.x, c.hasX }
func (c *TextChunk) Y() (float32, bool) { return c.y, c.hasY }
func (c *TextChunk) Anchor() TextAnchor { return c.anchor }
func (c *TextChunk) Spans() []TextSpan  { return c.spans }
func (c *TextChunk) Text() string       { return c.text }

// TextFlow is the chunk's text path, or nil for a linear flow.
func (c *TextChunk) TextFlow() *TextPath { return c.textFlow }

// WritingMode is the `writing-mode` property.
type WritingMode uint8

const (
	WritingModeLeftToRight WritingMode = iota
	WritingModeTopToBottom
)

// Text is a `text` element, laid out and flattened into paths.
type Text struct {
	id                   string
	renderingMode        TextRendering
	dx, dy, rotate       []float32
	writingMode          WritingMode
	chunks               []TextChunk
	absTransform         skia.Transform
	boundingBox          skia.Rect
	absBoundingBox       skia.Rect
	strokeBoundingBox    skia.Rect
	absStrokeBoundingBox skia.Rect
	flattened            *Group
	layouted             []Span
}

func (t *Text) ID() string                   { return t.id }
func (t *Text) RenderingMode() TextRendering { return t.renderingMode }

// Dx, Dy and Rotate hold one value per Unicode code point.
func (t *Text) Dx() []float32                { return t.dx }
func (t *Text) Dy() []float32                { return t.dy }
func (t *Text) Rotate() []float32            { return t.rotate }
func (t *Text) WritingMode() WritingMode     { return t.writingMode }
func (t *Text) Chunks() []TextChunk          { return t.chunks }
func (t *Text) AbsTransform() skia.Transform { return t.absTransform }

// BoundingBox is the text bounding box from glyph metrics (objectBoundingBox), not the outlines.
func (t *Text) BoundingBox() skia.Rect          { return t.boundingBox }
func (t *Text) AbsBoundingBox() skia.Rect       { return t.absBoundingBox }
func (t *Text) StrokeBoundingBox() skia.Rect    { return t.strokeBoundingBox }
func (t *Text) AbsStrokeBoundingBox() skia.Rect { return t.absStrokeBoundingBox }

// Flattened is the text converted into paths. Color (COLR), SVG and bitmap glyphs are left out.
func (t *Text) Flattened() *Group { return t.flattened }

// Layouted are the positioned glyphs and decoration spans.
func (t *Text) Layouted() []Span { return t.layouted }

func (t *Text) AbsLayerBoundingBox() (skia.NonZeroRect, bool) {
	return t.absBoundingBox.ToNonZeroRect()
}

// Subroots calls f for the flattened group.
func (t *Text) Subroots(f func(*Group)) {
	if t.flattened != nil {
		f(t.flattened)
	}
}

// PositionedGlyph is a glyph positioned by the layout. Its transform takes the font size into account,
// so it maps the glyph outline directly.
type PositionedGlyph struct {
	glyphTS, clusterTS, spanTS skia.Transform
	unitsPerEm                 uint16
	fontSize                   float32
	// ID is the glyph id in Font.
	ID uint16
	// Text is the source text of the glyph.
	Text string
	// Font is the face in the tree's font database.
	Font text.FaceID
}

func (g *PositionedGlyph) FontSize() float32 { return g.fontSize }

// Transform is the glyph's transform.
func (g *PositionedGlyph) Transform() skia.Transform {
	sx := g.fontSize / float32(g.unitsPerEm)
	return g.spanTS.PreConcat(g.clusterTS).PreConcat(skia.TransformFromScale(sx, sx)).PreConcat(g.glyphTS)
}

// OutlineTransform is the transform for an outline glyph (glyf or CFF), whose y axis points up.
func (g *PositionedGlyph) OutlineTransform() skia.Transform {
	return g.Transform().PreConcat(skia.TransformFromScale(1, -1))
}

// Span is a run of laid-out glyphs sharing fill, stroke, paint order and visibility.
type Span struct {
	fill              *Fill
	stroke            *Stroke
	paintOrder        PaintOrder
	fontSize          float32
	variations        []FontVariation
	fontOpticalSizing FontOpticalSizing
	visible           bool
	positionedGlyphs  []PositionedGlyph
	underline         *Path
	overline          *Path
	lineThrough       *Path
}

func (s *Span) Fill() *Fill                          { return s.fill }
func (s *Span) Stroke() *Stroke                      { return s.stroke }
func (s *Span) PaintOrder() PaintOrder               { return s.paintOrder }
func (s *Span) FontSize() float32                    { return s.fontSize }
func (s *Span) Variations() []FontVariation          { return s.variations }
func (s *Span) FontOpticalSizing() FontOpticalSizing { return s.fontOpticalSizing }
func (s *Span) IsVisible() bool                      { return s.visible }
func (s *Span) PositionedGlyphs() []PositionedGlyph  { return s.positionedGlyphs }

// Underline and Overline are drawn before the glyphs, LineThrough after; nil is none.
func (s *Span) Underline() *Path   { return s.underline }
func (s *Span) Overline() *Path    { return s.overline }
func (s *Span) LineThrough() *Path { return s.lineThrough }
