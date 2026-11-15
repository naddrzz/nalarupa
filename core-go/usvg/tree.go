package usvg

import (
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// The output tree (tree/mod.rs). Rust's pub(crate) fields are unexported and set directly by the
// converters; each Rust getter is an exported method. Arc<T> is *T (identity by pointer), Option<T>
// of a struct is a nil-able pointer, and strict-num wrappers (Opacity, StopOffset, PositiveF32,
// StrokeWidth, NonZeroF32) are plain float32 fields checked where they are built.

// newNonEmptyString is NonEmptyString::new: s, unless it is empty after trimming whitespace.
func newNonEmptyString(s string) (string, bool) {
	return s, strings.TrimSpace(s) != ""
}

// newNonZeroF32 is NonZeroF32::new: n, unless it is within 4 ulps of zero.
func newNonZeroF32(n float32) (float32, bool) {
	return n, !approxEqUlpsF32(n, 0, 4)
}

// units is usvg's Units. It has no default: it depends on the element.
type units uint8

const (
	unitsUserSpaceOnUse units = iota
	unitsObjectBoundingBox
)

// visibility is the `visibility` property; the zero value is the default.
type visibility uint8

const (
	visibilityVisible visibility = iota
	visibilityHidden
	visibilityCollapse
)

// ShapeRendering is the `shape-rendering` property.
type ShapeRendering uint8

const (
	ShapeRenderingOptimizeSpeed ShapeRendering = iota
	ShapeRenderingCrispEdges
	ShapeRenderingGeometricPrecision
)

// DefaultShapeRendering is ShapeRendering::default().
const DefaultShapeRendering = ShapeRenderingGeometricPrecision

// UseShapeAntialiasing checks if anti-aliasing should be enabled.
func (s ShapeRendering) UseShapeAntialiasing() bool {
	return s == ShapeRenderingGeometricPrecision
}

// ParseShapeRendering is ShapeRendering's FromStr; ok is false for an invalid value.
func ParseShapeRendering(s string) (ShapeRendering, bool) {
	switch s {
	case "optimizeSpeed":
		return ShapeRenderingOptimizeSpeed, true
	case "crispEdges":
		return ShapeRenderingCrispEdges, true
	case "geometricPrecision":
		return ShapeRenderingGeometricPrecision, true
	}
	return 0, false
}

// TextRendering is the `text-rendering` property.
type TextRendering uint8

const (
	TextRenderingOptimizeSpeed TextRendering = iota
	TextRenderingOptimizeLegibility
	TextRenderingGeometricPrecision
)

// DefaultTextRendering is TextRendering::default().
const DefaultTextRendering = TextRenderingOptimizeLegibility

// ParseTextRendering is TextRendering's FromStr; ok is false for an invalid value.
func ParseTextRendering(s string) (TextRendering, bool) {
	switch s {
	case "optimizeSpeed":
		return TextRenderingOptimizeSpeed, true
	case "optimizeLegibility":
		return TextRenderingOptimizeLegibility, true
	case "geometricPrecision":
		return TextRenderingGeometricPrecision, true
	}
	return 0, false
}

// ImageRendering is the `image-rendering` property; the zero value is the default.
type ImageRendering uint8

const (
	ImageRenderingOptimizeQuality ImageRendering = iota
	ImageRenderingOptimizeSpeed
	// The following can only appear as presentation attributes.
	ImageRenderingSmooth
	ImageRenderingHighQuality
	ImageRenderingCrispEdges
	ImageRenderingPixelated
)

// ParseImageRendering is ImageRendering's FromStr; ok is false for an invalid value.
func ParseImageRendering(s string) (ImageRendering, bool) {
	switch s {
	case "optimizeQuality":
		return ImageRenderingOptimizeQuality, true
	case "optimizeSpeed":
		return ImageRenderingOptimizeSpeed, true
	case "smooth":
		return ImageRenderingSmooth, true
	case "high-quality":
		return ImageRenderingHighQuality, true
	case "crisp-edges":
		return ImageRenderingCrispEdges, true
	case "pixelated":
		return ImageRenderingPixelated, true
	}
	return 0, false
}

// BlendMode is the `mix-blend-mode` property; the zero value is the default.
type BlendMode uint8

const (
	BlendModeNormal BlendMode = iota
	BlendModeMultiply
	BlendModeScreen
	BlendModeOverlay
	BlendModeDarken
	BlendModeLighten
	BlendModeColorDodge
	BlendModeColorBurn
	BlendModeHardLight
	BlendModeSoftLight
	BlendModeDifference
	BlendModeExclusion
	BlendModeHue
	BlendModeSaturation
	BlendModeColor
	BlendModeLuminosity
)

var blendModeNames = [...]string{
	"normal", "multiply", "screen", "overlay", "darken", "lighten", "color-dodge", "color-burn",
	"hard-light", "soft-light", "difference", "exclusion", "hue", "saturation", "color", "luminosity",
}

// String is BlendMode's Display: the CSS name.
func (b BlendMode) String() string { return blendModeNames[b] }

// SpreadMethod is the `spreadMethod` attribute; the zero value is the default.
type SpreadMethod uint8

const (
	SpreadMethodPad SpreadMethod = iota
	SpreadMethodReflect
	SpreadMethodRepeat
)

// BaseGradient holds what linear and radial gradients share.
type BaseGradient struct {
	id           string
	units        units // used only during parsing
	transform    skia.Transform
	spreadMethod SpreadMethod
	stops        []Stop
}

// ID is the element's ID, taken from the SVG.
func (g *BaseGradient) ID() string { return g.id }

// Transform is `gradientTransform`.
func (g *BaseGradient) Transform() skia.Transform { return g.transform }

// SpreadMethod is `spreadMethod`.
func (g *BaseGradient) SpreadMethod() SpreadMethod { return g.spreadMethod }

// Stops is the list of `stop` elements.
func (g *BaseGradient) Stops() []Stop { return g.stops }

// baseGradient lets LinearGradient and RadialGradient embed BaseGradient under an unexported field
// name, which promotes its fields and getters as Rust's Deref does.
type baseGradient = BaseGradient

// LinearGradient is a `linearGradient` element.
type LinearGradient struct {
	baseGradient
	x1, y1, x2, y2 float32
}

func (g *LinearGradient) X1() float32 { return g.x1 }
func (g *LinearGradient) Y1() float32 { return g.y1 }
func (g *LinearGradient) X2() float32 { return g.x2 }
func (g *LinearGradient) Y2() float32 { return g.y2 }

// RadialGradient is a `radialGradient` element. r and fr are PositiveF32.
type RadialGradient struct {
	baseGradient
	cx, cy float32
	r      float32
	fx, fy float32
	fr     float32
}

func (g *RadialGradient) CX() float32 { return g.cx }
func (g *RadialGradient) CY() float32 { return g.cy }
func (g *RadialGradient) R() float32  { return g.r }
func (g *RadialGradient) FX() float32 { return g.fx }
func (g *RadialGradient) FY() float32 { return g.fy }
func (g *RadialGradient) FR() float32 { return g.fr }

// Stop is a gradient `stop` element. offset and opacity are NormalizedF32.
type Stop struct {
	offset  float32
	color   Color
	opacity float32
}

func (s Stop) Offset() float32  { return s.offset }
func (s Stop) Color() Color     { return s.color }
func (s Stop) Opacity() float32 { return s.opacity }

// Pattern is a `pattern` element.
type Pattern struct {
	id           string
	units        units // used only during parsing
	contentUnits units // used only during parsing
	transform    skia.Transform
	rect         skia.NonZeroRect
	viewBox      *viewBox
	root         Group
}

// ID is the element's ID, taken from the SVG.
func (p *Pattern) ID() string { return p.id }

// Transform is `patternTransform`.
func (p *Pattern) Transform() skia.Transform { return p.transform }

// Rect is `x`, `y`, `width` and `height`.
func (p *Pattern) Rect() skia.NonZeroRect { return p.rect }

// Root holds the pattern's children.
func (p *Pattern) Root() *Group { return &p.root }

// StrokeMiterlimit is `stroke-miterlimit`, always >= 1.
type StrokeMiterlimit float32

// NewStrokeMiterlimit is StrokeMiterlimit::new: n, or 1 when n is below 1 or NaN.
func NewStrokeMiterlimit(n float32) StrokeMiterlimit {
	if !(n >= 1) {
		n = 1
	}
	return StrokeMiterlimit(n)
}

// DefaultStrokeMiterlimit is StrokeMiterlimit::default().
func DefaultStrokeMiterlimit() StrokeMiterlimit { return NewStrokeMiterlimit(4) }

// Get returns the underlying value.
func (m StrokeMiterlimit) Get() float32 { return float32(m) }

// Equal is StrokeMiterlimit's PartialEq: equal within 4 ulps.
func (m StrokeMiterlimit) Equal(o StrokeMiterlimit) bool {
	return approxEqUlpsF32(float32(m), float32(o), 4)
}

// LineCap is `stroke-linecap`; the zero value is the default.
type LineCap uint8

const (
	LineCapButt LineCap = iota
	LineCapRound
	LineCapSquare
)

// LineJoin is `stroke-linejoin`; the zero value is the default.
type LineJoin uint8

const (
	LineJoinMiter LineJoin = iota
	LineJoinMiterClip
	LineJoinRound
	LineJoinBevel
)

var (
	treeSkiaLineCaps  = [...]skia.LineCap{LineCapButt: skia.LineCapButt, LineCapRound: skia.LineCapRound, LineCapSquare: skia.LineCapSquare}
	treeSkiaLineJoins = [...]skia.LineJoin{
		LineJoinMiter: skia.LineJoinMiter, LineJoinMiterClip: skia.LineJoinMiterClip,
		LineJoinRound: skia.LineJoinRound, LineJoinBevel: skia.LineJoinBevel,
	}
)

// Stroke is a stroke style. dasharray nil is None; a non-nil empty slice is Some(empty). opacity is
// NormalizedF32 and width NonZeroPositiveF32.
type Stroke struct {
	paint      Paint
	dasharray  []float32
	dashoffset float32
	miterlimit StrokeMiterlimit
	opacity    float32
	width      float32
	linecap    LineCap
	linejoin   LineJoin
	// Whether the stroke needs to be resolved relative to a context element.
	contextElement contextElement
}

func (s *Stroke) Paint() Paint                 { return s.paint }
func (s *Stroke) Dasharray() []float32         { return s.dasharray }
func (s *Stroke) Dashoffset() float32          { return s.dashoffset }
func (s *Stroke) Miterlimit() StrokeMiterlimit { return s.miterlimit }
func (s *Stroke) Opacity() float32             { return s.opacity }
func (s *Stroke) Width() float32               { return s.width }
func (s *Stroke) Linecap() LineCap             { return s.linecap }
func (s *Stroke) Linejoin() LineJoin           { return s.linejoin }

// ToTinySkia converts into a skia.Stroke, with the dash when dasharray is set and valid.
func (s *Stroke) ToTinySkia() skia.Stroke {
	stroke := skia.Stroke{
		Width:      s.width,
		MiterLimit: s.miterlimit.Get(),
		LineCap:    treeSkiaLineCaps[s.linecap],
		LineJoin:   treeSkiaLineJoins[s.linejoin],
	}
	if s.dasharray != nil {
		stroke.Dash = skia.NewStrokeDash(append([]float32(nil), s.dasharray...), s.dashoffset)
	}
	return stroke
}

// FillRule is `fill-rule`; the zero value is the default.
type FillRule uint8

const (
	FillRuleNonZero FillRule = iota
	FillRuleEvenOdd
)

type contextElementKind uint8

const (
	contextElementNone contextElementKind = iota
	// contextElementUseNode: the context element is a use node, whose bounding box is known only once
	// the whole tree is converted, so the fill/stroke is fixed up afterwards.
	contextElementUseNode
	// contextElementPathNode: the context element is a path (for markers), resolved while parsing.
	contextElementPathNode
)

// contextElement is Option<ContextElement>; the zero value is None. transform, bbox and hasBBox are
// the PathNode(Transform, Option<NonZeroRect>) payload.
type contextElement struct {
	kind      contextElementKind
	transform skia.Transform
	bbox      skia.NonZeroRect
	hasBBox   bool
}

// Fill is a fill style. opacity is NormalizedF32.
type Fill struct {
	paint   Paint
	opacity float32
	rule    FillRule
	// Whether the fill needs to be resolved relative to a context element.
	contextElement contextElement
}

func (f *Fill) Paint() Paint     { return f.paint }
func (f *Fill) Opacity() float32 { return f.opacity }
func (f *Fill) Rule() FillRule   { return f.rule }

// DefaultFill is Fill::default(): opaque black, non-zero.
func DefaultFill() Fill {
	return Fill{paint: ColorBlack(), opacity: 1, rule: FillRuleNonZero}
}

// Color is an 8-bit RGB color.
type Color struct {
	Red, Green, Blue uint8
}

func NewColorRGB(red, green, blue uint8) Color { return Color{red, green, blue} }
func ColorBlack() Color                        { return Color{0, 0, 0} }
func ColorWhite() Color                        { return Color{255, 255, 255} }

// Paint is a paint: Color, *LinearGradient, *RadialGradient or *Pattern. == matches Rust's
// PartialEq: colors by value, paint servers by pointer.
type Paint interface{ isPaint() }

func (Color) isPaint()           {}
func (*LinearGradient) isPaint() {}
func (*RadialGradient) isPaint() {}
func (*Pattern) isPaint()        {}

// ClipPath is a `clipPath` element.
type ClipPath struct {
	id        string
	transform skia.Transform
	clipPath  *ClipPath
	root      Group
}

// newEmptyClipPath is ClipPath::empty.
func newEmptyClipPath(id string) *ClipPath {
	return &ClipPath{id: id, transform: skia.Identity(), root: newEmptyGroup()}
}

func (c *ClipPath) ID() string                { return c.id }
func (c *ClipPath) Transform() skia.Transform { return c.transform }

// ClipPath is the additional `clip-path`, or nil.
func (c *ClipPath) ClipPath() *ClipPath { return c.clipPath }
func (c *ClipPath) Root() *Group        { return &c.root }

// MaskType is `mask-type`; the zero value is the default.
type MaskType uint8

const (
	MaskTypeLuminance MaskType = iota
	MaskTypeAlpha
)

// Mask is a `mask` element.
type Mask struct {
	id   string
	rect skia.NonZeroRect
	kind MaskType
	mask *Mask
	root Group
}

func (m *Mask) ID() string             { return m.id }
func (m *Mask) Rect() skia.NonZeroRect { return m.rect }
func (m *Mask) Kind() MaskType         { return m.kind }

// Mask is the additional `mask`, or nil.
func (m *Mask) Mask() *Mask { return m.mask }

// Root holds the mask's children; with none, the whole element is masked out.
func (m *Mask) Root() *Group { return &m.root }

// Node is a tree node: *Group, *Path, *Image or *Text.
type Node interface {
	ID() string
	AbsTransform() skia.Transform
	BoundingBox() skia.Rect
	AbsBoundingBox() skia.Rect
	StrokeBoundingBox() skia.Rect
	AbsStrokeBoundingBox() skia.Rect
	// AbsLayerBoundingBox is the group's abs layer bounding box, else the abs bounding box when it is
	// not zero-sized.
	AbsLayerBoundingBox() (skia.NonZeroRect, bool)
	// Subroots calls f for each subtree the node holds: clip paths, masks, patterns, SVG images and
	// flattened text.
	Subroots(f func(*Group))
}

// Group is a `g` element, kept only where it starts a new canvas. opacity is NormalizedF32.
type Group struct {
	id           string
	transform    skia.Transform
	absTransform skia.Transform
	opacity      float32
	blendMode    BlendMode
	isolate      bool
	clipPath     *ClipPath
	// Whether the group is a context element (a use node).
	isContextElement     bool
	mask                 *Mask
	filters              []*Filter
	boundingBox          skia.Rect
	absBoundingBox       skia.Rect
	strokeBoundingBox    skia.Rect
	absStrokeBoundingBox skia.Rect
	layerBoundingBox     skia.NonZeroRect
	absLayerBoundingBox  skia.NonZeroRect
	children             []Node
}

// newEmptyGroup is Group::empty: identity transforms, zero bounding boxes and a 0,0,1,1 layer box.
func newEmptyGroup() Group {
	dummy, _ := skia.RectFromXYWH(0, 0, 0, 0)
	layer, _ := skia.NonZeroRectFromXYWH(0, 0, 1, 1)
	return Group{
		transform:            skia.Identity(),
		absTransform:         skia.Identity(),
		opacity:              1,
		blendMode:            BlendModeNormal,
		boundingBox:          dummy,
		absBoundingBox:       dummy,
		strokeBoundingBox:    dummy,
		absStrokeBoundingBox: dummy,
		layerBoundingBox:     layer,
		absLayerBoundingBox:  layer,
	}
}

func (g *Group) ID() string                   { return g.id }
func (g *Group) Transform() skia.Transform    { return g.transform }
func (g *Group) AbsTransform() skia.Transform { return g.absTransform }
func (g *Group) Opacity() float32             { return g.opacity }
func (g *Group) BlendMode() BlendMode         { return g.blendMode }
func (g *Group) Isolate() bool                { return g.isolate }

// ClipPath is the group's clip path, or nil.
func (g *Group) ClipPath() *ClipPath { return g.clipPath }

// Mask is the group's mask, or nil.
func (g *Group) Mask() *Mask                     { return g.mask }
func (g *Group) Filters() []*Filter              { return g.filters }
func (g *Group) BoundingBox() skia.Rect          { return g.boundingBox }
func (g *Group) AbsBoundingBox() skia.Rect       { return g.absBoundingBox }
func (g *Group) StrokeBoundingBox() skia.Rect    { return g.strokeBoundingBox }
func (g *Group) AbsStrokeBoundingBox() skia.Rect { return g.absStrokeBoundingBox }

// LayerBoundingBox is the stroke bounding box expanded or clipped by the filters region and including
// all children: what a renderer allocates for an isolated group. It is 0,0,1,1 for empty groups.
func (g *Group) LayerBoundingBox() skia.NonZeroRect { return g.layerBoundingBox }

// AbsLayerBoundingBox is the layer bounding box in canvas units; ok is always true (Rust's Group
// method returns the rect, and Node's wraps it in Some).
func (g *Group) AbsLayerBoundingBox() (skia.NonZeroRect, bool) { return g.absLayerBoundingBox, true }

func (g *Group) Children() []Node { return g.children }

// ShouldIsolate checks if the group should be isolated during rendering.
func (g *Group) ShouldIsolate() bool {
	return g.isolate || g.opacity != 1 || g.clipPath != nil || g.mask != nil || len(g.filters) != 0 ||
		g.blendMode != BlendModeNormal
}

func (g *Group) HasChildren() bool { return len(g.children) != 0 }

// FiltersBoundingBox is the union of the filter regions; ok is false when the group has no filters.
// It can be smaller than the bounding box, since a filter region clips.
func (g *Group) FiltersBoundingBox() (skia.NonZeroRect, bool) {
	full := defaultBBox()
	for _, f := range g.filters {
		full = full.expand(bboxFromNonZeroRect(f.rect))
	}
	return full.toNonZeroRect()
}

// Subroots calls f for the clip path and mask roots (and their nested clip path and mask). Filter
// image primitives are not visited until filters are ported (slice 4c3).
func (g *Group) Subroots(f func(*Group)) {
	if clip := g.clipPath; clip != nil {
		f(&clip.root)
		if sub := clip.clipPath; sub != nil {
			f(&sub.root)
		}
	}
	if mask := g.mask; mask != nil {
		f(&mask.root)
		if sub := mask.mask; sub != nil {
			f(&sub.root)
		}
	}
}

// PaintOrder is `paint-order`; markers are already converted, so only fill/stroke order remains.
// The zero value is the default.
type PaintOrder uint8

const (
	PaintOrderFillAndStroke PaintOrder = iota
	PaintOrderStrokeAndFill
)

// Path is a path element. data is in object coordinates; absTransform maps it onto the canvas.
type Path struct {
	id                   string
	visible              bool
	fill                 *Fill
	stroke               *Stroke
	paintOrder           PaintOrder
	renderingMode        ShapeRendering
	data                 *skia.Path
	absTransform         skia.Transform
	boundingBox          skia.Rect
	absBoundingBox       skia.Rect
	strokeBoundingBox    skia.Rect
	absStrokeBoundingBox skia.Rect
}

// newSimplePath is Path::new_simple: a visible path with no fill, stroke or transform.
func newSimplePath(data *skia.Path) (*Path, bool) {
	return newPath("", true, nil, nil, PaintOrderFillAndStroke, DefaultShapeRendering, data, skia.Identity())
}

// newPath is Path::new: it computes the bounding boxes; ok is false where Rust gives None (no
// bounds, or a transform that overflows).
func newPath(id string, visible bool, fill *Fill, stroke *Stroke, paintOrder PaintOrder,
	renderingMode ShapeRendering, data *skia.Path, absTransform skia.Transform) (*Path, bool) {
	boundingBox, ok := data.ComputeTightBounds()
	if !ok {
		return nil, false
	}
	strokeBoundingBox, ok := treePathStrokeBBox(stroke, data)
	if !ok {
		strokeBoundingBox = boundingBox
	}

	var absBoundingBox, absStrokeBoundingBox skia.Rect
	if absTransform.HasSkew() {
		path2 := data.Transform(absTransform)
		if path2 == nil {
			return nil, false
		}
		if absBoundingBox, ok = path2.ComputeTightBounds(); !ok {
			return nil, false
		}
		if absStrokeBoundingBox, ok = treePathStrokeBBox(stroke, path2); !ok {
			absStrokeBoundingBox = absBoundingBox
		}
	} else {
		// A transform without a skew can be applied to the bbox alone.
		if absBoundingBox, ok = boundingBox.Transform(absTransform); !ok {
			return nil, false
		}
		if absStrokeBoundingBox, ok = strokeBoundingBox.Transform(absTransform); !ok {
			return nil, false
		}
	}

	return &Path{
		id:                   id,
		visible:              visible,
		fill:                 fill,
		stroke:               stroke,
		paintOrder:           paintOrder,
		renderingMode:        renderingMode,
		data:                 data,
		absTransform:         absTransform,
		boundingBox:          boundingBox,
		absBoundingBox:       absBoundingBox,
		strokeBoundingBox:    strokeBoundingBox,
		absStrokeBoundingBox: absStrokeBoundingBox,
	}, true
}

func (p *Path) ID() string                      { return p.id }
func (p *Path) IsVisible() bool                 { return p.visible }
func (p *Path) Fill() *Fill                     { return p.fill }
func (p *Path) Stroke() *Stroke                 { return p.stroke }
func (p *Path) PaintOrder() PaintOrder          { return p.paintOrder }
func (p *Path) RenderingMode() ShapeRendering   { return p.renderingMode }
func (p *Path) Data() *skia.Path                { return p.data }
func (p *Path) AbsTransform() skia.Transform    { return p.absTransform }
func (p *Path) BoundingBox() skia.Rect          { return p.boundingBox }
func (p *Path) AbsBoundingBox() skia.Rect       { return p.absBoundingBox }
func (p *Path) StrokeBoundingBox() skia.Rect    { return p.strokeBoundingBox }
func (p *Path) AbsStrokeBoundingBox() skia.Rect { return p.absStrokeBoundingBox }

// AbsLayerBoundingBox: a horizontal or vertical path without a stroke gives ok false, as expected.
func (p *Path) AbsLayerBoundingBox() (skia.NonZeroRect, bool) {
	return p.absBoundingBox.ToNonZeroRect()
}

// Subroots calls f for the roots of a pattern fill and a pattern stroke.
func (p *Path) Subroots(f func(*Group)) {
	if p.fill != nil {
		if patt, ok := p.fill.paint.(*Pattern); ok {
			f(&patt.root)
		}
	}
	if p.stroke != nil {
		if patt, ok := p.stroke.paint.(*Pattern); ok {
			f(&patt.root)
		}
	}
}

// treePathStrokeBBox is Path::calculate_stroke_bbox: the tight bounds of the stroked path, without
// the dash, which the spec leaves out of the bbox.
func treePathStrokeBBox(stroke *Stroke, path *skia.Path) (skia.Rect, bool) {
	if stroke == nil {
		return skia.Rect{}, false
	}
	s := stroke.ToTinySkia()
	s.Dash = nil
	stroked := path.Stroke(&s, 1)
	if stroked == nil {
		return skia.Rect{}, false
	}
	return stroked.ComputeTightBounds()
}

// ImageKind is the embedded image data: ImageKindJPEG, ImageKindPNG, ImageKindGIF, ImageKindWEBP
// (raw bytes, decoded by the caller) or ImageKindSVG (a converted tree, rendered as is).
type ImageKind interface {
	// actualSize is the image's own size; ok is false for raster data with no valid size.
	actualSize() (skia.Size, bool)
}

type (
	ImageKindJPEG []byte
	ImageKindPNG  []byte
	ImageKindGIF  []byte
	ImageKindWEBP []byte
	ImageKindSVG  struct{ Tree *Tree }
)

func (k ImageKindJPEG) actualSize() (skia.Size, bool) { return treeRasterSize(k) }
func (k ImageKindPNG) actualSize() (skia.Size, bool)  { return treeRasterSize(k) }
func (k ImageKindGIF) actualSize() (skia.Size, bool)  { return treeRasterSize(k) }
func (k ImageKindWEBP) actualSize() (skia.Size, bool) { return treeRasterSize(k) }
func (k ImageKindSVG) actualSize() (skia.Size, bool)  { return k.Tree.size, true }

// treeRasterSize is actual_size for raster data. Rust logs "Image has an invalid size. Skipped." on
// failure; the port has no logger.
func treeRasterSize(data []byte) (skia.Size, bool) {
	s, err := imgszBlobSize(data)
	if err != nil {
		return skia.Size{}, false
	}
	return skia.SizeFromWH(float32(s.Width), float32(s.Height))
}

// Image is an `image` element. size is the image's own size, not its `width` and `height`.
type Image struct {
	id             string
	visible        bool
	size           skia.Size
	renderingMode  ImageRendering
	kind           ImageKind
	absTransform   skia.Transform
	absBoundingBox skia.NonZeroRect
}

func (i *Image) ID() string                    { return i.id }
func (i *Image) IsVisible() bool               { return i.visible }
func (i *Image) Size() skia.Size               { return i.size }
func (i *Image) RenderingMode() ImageRendering { return i.renderingMode }
func (i *Image) Kind() ImageKind               { return i.kind }
func (i *Image) AbsTransform() skia.Transform  { return i.absTransform }

// BoundingBox is the image size at the origin.
func (i *Image) BoundingBox() skia.Rect {
	r, ok := i.size.ToRect(0, 0)
	if !ok {
		panic("usvg: Image.BoundingBox overflow") // Rust unwraps
	}
	return r
}

func (i *Image) AbsBoundingBox() skia.Rect { return i.absBoundingBox.ToRect() }

// StrokeBoundingBox is the bounding box: an image cannot be stroked.
func (i *Image) StrokeBoundingBox() skia.Rect { return i.BoundingBox() }

// AbsStrokeBoundingBox is the abs bounding box: an image cannot be stroked.
func (i *Image) AbsStrokeBoundingBox() skia.Rect { return i.AbsBoundingBox() }

func (i *Image) AbsLayerBoundingBox() (skia.NonZeroRect, bool) {
	return i.AbsBoundingBox().ToNonZeroRect()
}

// Subroots calls f for the root of an SVG image.
func (i *Image) Subroots(f func(*Group)) {
	if svg, ok := i.kind.(ImageKindSVG); ok {
		f(&svg.Tree.root)
	}
}

// Tree is a converted SVG document. There is no font database until text is ported (slice 4c2).
type Tree struct {
	size            skia.Size
	root            Group
	linearGradients []*LinearGradient
	radialGradients []*RadialGradient
	patterns        []*Pattern
	clipPaths       []*ClipPath
	masks           []*Mask
	filters         []*Filter
}

// Size is the image size the SVG should be rendered at: `width` and `height`.
func (t *Tree) Size() skia.Size { return t.size }

// Root is the root element.
func (t *Tree) Root() *Group { return &t.root }

// NodeByID returns a renderable node by ID, or nil; an empty id always gives nil.
func (t *Tree) NodeByID(id string) Node {
	if id == "" {
		return nil
	}
	return treeNodeByID(&t.root, id)
}

// HasTextNodes checks if the tree has text nodes. Like Rust, it looks at the root's children and,
// through them, their subroots and SVG images, not into nested groups' children.
func (t *Tree) HasTextNodes() bool { return treeHasTextNodes(&t.root) }

// HasDefsNodes checks if the tree has paint servers, filters, clip paths or masks.
func (t *Tree) HasDefsNodes() bool {
	return len(t.linearGradients) != 0 || len(t.radialGradients) != 0 || len(t.patterns) != 0 ||
		len(t.filters) != 0 || len(t.clipPaths) != 0 || len(t.masks) != 0
}

func (t *Tree) LinearGradients() []*LinearGradient { return t.linearGradients }
func (t *Tree) RadialGradients() []*RadialGradient { return t.radialGradients }
func (t *Tree) Patterns() []*Pattern               { return t.patterns }
func (t *Tree) ClipPaths() []*ClipPath             { return t.clipPaths }
func (t *Tree) Masks() []*Mask                     { return t.masks }
func (t *Tree) Filters() []*Filter                 { return t.filters }

// collectPaintServers appends every gradient and pattern the tree uses, each once, in visit order.
func (t *Tree) collectPaintServers() {
	treeLoopOverPaintServers(&t.root, func(paint Paint) {
		switch p := paint.(type) {
		case *LinearGradient:
			t.linearGradients = treePushUnique(t.linearGradients, p)
		case *RadialGradient:
			t.radialGradients = treePushUnique(t.radialGradients, p)
		case *Pattern:
			t.patterns = treePushUnique(t.patterns, p)
		}
	})
}

// treePushUnique appends v unless list already holds it (Arc::ptr_eq for pointers).
func treePushUnique[T comparable](list []T, v T) []T {
	for _, other := range list {
		if other == v {
			return list
		}
	}
	return append(list, v)
}

func treeNodeByID(parent *Group, id string) Node {
	for _, child := range parent.children {
		if child.ID() == id {
			return child
		}
		if g, ok := child.(*Group); ok {
			if n := treeNodeByID(g, id); n != nil {
				return n
			}
		}
	}
	return nil
}

func treeHasTextNodes(root *Group) bool {
	for _, node := range root.children {
		if _, ok := node.(*Text); ok {
			return true
		}

		hasText := false
		if image, ok := node.(*Image); ok {
			if svg, ok := image.kind.(ImageKindSVG); ok && treeHasTextNodes(&svg.Tree.root) {
				hasText = true
			}
		}
		node.Subroots(func(subroot *Group) {
			if treeHasTextNodes(subroot) {
				hasText = true
			}
		})
		if hasText {
			return true
		}
	}
	return false
}

func treeLoopOverPaintServers(parent *Group, f func(Paint)) {
	for _, node := range parent.children {
		switch n := node.(type) {
		case *Group:
			treeLoopOverPaintServers(n, f)
		case *Path:
			if n.fill != nil {
				f(n.fill.paint)
			}
			if n.stroke != nil {
				f(n.stroke.paint)
			}
		}
		// Images have no paint; text is visited through its flattened subroot.
		node.Subroots(func(subroot *Group) { treeLoopOverPaintServers(subroot, f) })
	}
}

// collectClipPaths appends every clip path used under g, each once, and returns the list.
func (g *Group) collectClipPaths(clipPaths []*ClipPath) []*ClipPath {
	for _, node := range g.children {
		group, isGroup := node.(*Group)
		if isGroup && group.clipPath != nil {
			clipPaths = treePushUnique(clipPaths, group.clipPath)
			if sub := group.clipPath.clipPath; sub != nil {
				clipPaths = treePushUnique(clipPaths, sub)
			}
		}
		node.Subroots(func(subroot *Group) { clipPaths = subroot.collectClipPaths(clipPaths) })
		if isGroup {
			clipPaths = group.collectClipPaths(clipPaths)
		}
	}
	return clipPaths
}

// collectMasks appends every mask used under g, each once, and returns the list.
func (g *Group) collectMasks(masks []*Mask) []*Mask {
	for _, node := range g.children {
		group, isGroup := node.(*Group)
		if isGroup && group.mask != nil {
			masks = treePushUnique(masks, group.mask)
			if sub := group.mask.mask; sub != nil {
				masks = treePushUnique(masks, sub)
			}
		}
		node.Subroots(func(subroot *Group) { masks = subroot.collectMasks(masks) })
		if isGroup {
			masks = group.collectMasks(masks)
		}
	}
	return masks
}

// collectFilters appends every filter used under g, each once, and returns the list.
func (g *Group) collectFilters(filters []*Filter) []*Filter {
	for _, node := range g.children {
		group, isGroup := node.(*Group)
		if isGroup {
			for _, f := range group.filters {
				filters = treePushUnique(filters, f)
			}
		}
		node.Subroots(func(subroot *Group) { filters = subroot.collectFilters(filters) })
		if isGroup {
			filters = group.collectFilters(filters)
		}
	}
	return filters
}

// treeChildBBox is a child's bounding box (bbox or stroke bbox) in the parent's coordinates: a group's
// own transform is applied when it does not overflow.
func treeChildBBox(child Node, r skia.Rect) skia.Rect {
	if group, ok := child.(*Group); ok {
		if t, ok := r.Transform(group.transform); ok {
			return t
		}
	}
	return r
}

// calculateObjectBBox is the union of the children's bounding boxes in g's coordinates; ok is false
// when it is empty or zero-sized.
func (g *Group) calculateObjectBBox() (skia.NonZeroRect, bool) {
	b := defaultBBox()
	for _, child := range g.children {
		b = b.expand(bboxFromRect(treeChildBBox(child, child.BoundingBox())))
	}
	return b.toNonZeroRect()
}

// calculateBoundingBoxes sets g's bounding boxes from its children (which must be up to date). ok is
// false where Rust returns None, leaving the fields set so far; an empty group keeps its boxes.
func (g *Group) calculateBoundingBoxes() bool {
	bb := defaultBBox()
	absBB := defaultBBox()
	strokeBB := defaultBBox()
	absStrokeBB := defaultBBox()
	layerBB := defaultBBox()
	for _, child := range g.children {
		bb = bb.expand(bboxFromRect(treeChildBBox(child, child.BoundingBox())))
		absBB = absBB.expand(bboxFromRect(child.AbsBoundingBox()))
		strokeBB = strokeBB.expand(bboxFromRect(treeChildBBox(child, child.StrokeBoundingBox())))
		absStrokeBB = absStrokeBB.expand(bboxFromRect(child.AbsStrokeBoundingBox()))

		if group, ok := child.(*Group); ok {
			if r, ok := group.layerBoundingBox.Transform(group.transform); ok {
				layerBB = layerBB.expand(bboxFromNonZeroRect(r))
			}
		} else {
			// Not a group: no transform needed.
			layerBB = layerBB.expand(bboxFromRect(child.StrokeBoundingBox()))
		}
	}

	// bb is default for an empty group, but the layer bounding box is still calculated.
	if r, ok := bb.toRect(); ok {
		g.boundingBox = r
		if r, ok = absBB.toRect(); !ok {
			return false
		}
		g.absBoundingBox = r
		if r, ok = strokeBB.toRect(); !ok {
			return false
		}
		g.strokeBoundingBox = r
		if r, ok = absStrokeBB.toRect(); !ok {
			return false
		}
		g.absStrokeBoundingBox = r
	}

	// The filter region has priority over the layer bbox.
	if r, ok := g.FiltersBoundingBox(); ok {
		g.layerBoundingBox = r
	} else {
		r, ok := layerBB.toNonZeroRect()
		if !ok {
			return false
		}
		g.layerBoundingBox = r
	}

	r, ok := g.layerBoundingBox.Transform(g.absTransform)
	if !ok {
		return false
	}
	g.absLayerBoundingBox = r
	return true
}

// Text is a placeholder for usvg's text node until slice 4c2 ports tree/text.rs: it carries only the
// fields the tree needs. flattened stays nil until text layout exists, so Subroots does nothing.
type Text struct {
	id                   string
	absTransform         skia.Transform
	boundingBox          skia.Rect
	absBoundingBox       skia.Rect
	strokeBoundingBox    skia.Rect
	absStrokeBoundingBox skia.Rect
	flattened            *Group
}

func (t *Text) ID() string                      { return t.id }
func (t *Text) AbsTransform() skia.Transform    { return t.absTransform }
func (t *Text) BoundingBox() skia.Rect          { return t.boundingBox }
func (t *Text) AbsBoundingBox() skia.Rect       { return t.absBoundingBox }
func (t *Text) StrokeBoundingBox() skia.Rect    { return t.strokeBoundingBox }
func (t *Text) AbsStrokeBoundingBox() skia.Rect { return t.absStrokeBoundingBox }

// Flattened is the text converted to paths, or nil in this placeholder.
func (t *Text) Flattened() *Group { return t.flattened }

func (t *Text) AbsLayerBoundingBox() (skia.NonZeroRect, bool) {
	return t.absBoundingBox.ToNonZeroRect()
}

// Subroots calls f for the flattened group, once it exists.
func (t *Text) Subroots(f func(*Group)) {
	if t.flattened != nil {
		f(t.flattened)
	}
}

// Filter is a placeholder for usvg's filter::Filter until slice 4c3 ports tree/filter.rs: only the id
// and the filter region, which FiltersBoundingBox needs. Group.Subroots does not visit filters yet.
type Filter struct {
	id   string
	rect skia.NonZeroRect
}

func (f *Filter) ID() string             { return f.id }
func (f *Filter) Rect() skia.NonZeroRect { return f.rect }
