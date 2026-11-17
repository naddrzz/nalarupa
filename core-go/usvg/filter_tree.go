// Port of usvg 0.47 src/tree/filter.rs: the SVG filter types.

package usvg

import "github.com/naddrzz/nalarupa/core-go/skia"

// Filter is a `filter` element, or a filter function of the `filter` property.
type Filter struct {
	id         string
	rect       skia.NonZeroRect
	primitives []Primitive
}

// ID is the element's id, or a generated one.
func (f *Filter) ID() string { return f.id }

// Rect is the filter region: `x`, `y`, `width` and `height`.
func (f *Filter) Rect() skia.NonZeroRect { return f.rect }

// Primitives lists the filter primitives; never empty.
func (f *Filter) Primitives() []Primitive { return f.primitives }

// Primitive is a filter primitive element.
type Primitive struct {
	rect               skia.NonZeroRect
	colorInterpolation ColorInterpolation
	result             string
	kind               FilterKind
}

// Rect is the filter subregion: `x`, `y`, `width` and `height`.
func (p *Primitive) Rect() skia.NonZeroRect { return p.rect }

// ColorInterpolation is `color-interpolation-filters`.
func (p *Primitive) ColorInterpolation() ColorInterpolation { return p.colorInterpolation }

// Result is the primitive's `result` name, generated when the element has none.
func (p *Primitive) Result() string { return p.result }

// Kind is the primitive: *FeBlend, *FeColorMatrix, *FeComponentTransfer, *FeComposite,
// *FeConvolveMatrix, *FeDiffuseLighting, *FeDisplacementMap, *FeDropShadow, *FeFlood, *FeGaussianBlur,
// *FeImage, *FeMerge, *FeMorphology, *FeOffset, *FeSpecularLighting, *FeTile or *FeTurbulence.
func (p *Primitive) Kind() FilterKind { return p.kind }

// FilterKind is filter::Kind.
type FilterKind interface {
	// HasInput checks that the primitive reads the given input.
	HasInput(in FilterInput) bool
}

// FilterInputKind lists the filter::Input variants.
type FilterInputKind uint8

const (
	FilterInputSourceGraphic FilterInputKind = iota
	FilterInputSourceAlpha
	FilterInputReference
)

// FilterInput is a primitive's `in` or `in2`; Name is the referenced `result` for
// FilterInputReference. == is Rust's PartialEq.
type FilterInput struct {
	Kind FilterInputKind
	Name string
}

// ColorInterpolation is `color-interpolation-filters`; LinearRGB is the default.
type ColorInterpolation uint8

const (
	ColorInterpolationSRGB ColorInterpolation = iota
	ColorInterpolationLinearRGB
)

// FeBlend is `feBlend`.
type FeBlend struct {
	input1, input2 FilterInput
	mode           BlendMode
}

func (fe *FeBlend) Input1() FilterInput { return fe.input1 }
func (fe *FeBlend) Input2() FilterInput { return fe.input2 }
func (fe *FeBlend) Mode() BlendMode     { return fe.mode }
func (fe *FeBlend) HasInput(in FilterInput) bool {
	return fe.input1 == in || fe.input2 == in
}

// FeColorMatrix is `feColorMatrix`.
type FeColorMatrix struct {
	input FilterInput
	kind  ColorMatrixKind
}

func (fe *FeColorMatrix) Input() FilterInput           { return fe.input }
func (fe *FeColorMatrix) Kind() ColorMatrixKind        { return fe.kind }
func (fe *FeColorMatrix) HasInput(in FilterInput) bool { return fe.input == in }

// ColorMatrixKind is ColorMatrixMatrix (always 20 numbers), ColorMatrixSaturate (not negative),
// ColorMatrixHueRotate or ColorMatrixLuminanceToAlpha.
type ColorMatrixKind interface{ colorMatrixKind() }

type (
	ColorMatrixMatrix           []float32
	ColorMatrixSaturate         float32
	ColorMatrixHueRotate        float32
	ColorMatrixLuminanceToAlpha struct{}
)

func (ColorMatrixMatrix) colorMatrixKind()           {}
func (ColorMatrixSaturate) colorMatrixKind()         {}
func (ColorMatrixHueRotate) colorMatrixKind()        {}
func (ColorMatrixLuminanceToAlpha) colorMatrixKind() {}

// defaultColorMatrixKind is ColorMatrixKind::default(): the identity matrix.
func defaultColorMatrixKind() ColorMatrixKind {
	return ColorMatrixMatrix{
		1, 0, 0, 0, 0,
		0, 1, 0, 0, 0,
		0, 0, 1, 0, 0,
		0, 0, 0, 1, 0,
	}
}

// FeComponentTransfer is `feComponentTransfer`.
type FeComponentTransfer struct {
	input                      FilterInput
	funcR, funcG, funcB, funcA TransferFunction
}

func (fe *FeComponentTransfer) Input() FilterInput           { return fe.input }
func (fe *FeComponentTransfer) FuncR() TransferFunction      { return fe.funcR }
func (fe *FeComponentTransfer) FuncG() TransferFunction      { return fe.funcG }
func (fe *FeComponentTransfer) FuncB() TransferFunction      { return fe.funcB }
func (fe *FeComponentTransfer) FuncA() TransferFunction      { return fe.funcA }
func (fe *FeComponentTransfer) HasInput(in FilterInput) bool { return fe.input == in }

// TransferFunction is TransferIdentity, TransferTable, TransferDiscrete (both lists can be empty),
// TransferLinear or TransferGamma.
type TransferFunction interface{ transferFunction() }

type (
	TransferIdentity struct{}
	TransferTable    []float32
	TransferDiscrete []float32
	TransferLinear   struct{ Slope, Intercept float32 }
	TransferGamma    struct{ Amplitude, Exponent, Offset float32 }
)

func (TransferIdentity) transferFunction() {}
func (TransferTable) transferFunction()    {}
func (TransferDiscrete) transferFunction() {}
func (TransferLinear) transferFunction()   {}
func (TransferGamma) transferFunction()    {}

// FeComposite is `feComposite`.
type FeComposite struct {
	input1, input2 FilterInput
	operator       CompositeOperator
}

func (fe *FeComposite) Input1() FilterInput          { return fe.input1 }
func (fe *FeComposite) Input2() FilterInput          { return fe.input2 }
func (fe *FeComposite) Operator() CompositeOperator  { return fe.operator }
func (fe *FeComposite) HasInput(in FilterInput) bool { return fe.input1 == in || fe.input2 == in }

// CompositeOp lists the CompositeOperator variants.
type CompositeOp uint8

const (
	CompositeOver CompositeOp = iota
	CompositeIn
	CompositeOut
	CompositeAtop
	CompositeXor
	CompositeArithmetic
)

// CompositeOperator is an `operator`; K1..K4 are set for CompositeArithmetic only.
type CompositeOperator struct {
	Op             CompositeOp
	K1, K2, K3, K4 float32
}

// FeConvolveMatrix is `feConvolveMatrix`.
type FeConvolveMatrix struct {
	input         FilterInput
	matrix        ConvolveMatrixData
	divisor       float32 // NonZeroF32
	bias          float32
	edgeMode      EdgeMode
	preserveAlpha bool
}

func (fe *FeConvolveMatrix) Input() FilterInput           { return fe.input }
func (fe *FeConvolveMatrix) Matrix() *ConvolveMatrixData  { return &fe.matrix }
func (fe *FeConvolveMatrix) Divisor() float32             { return fe.divisor }
func (fe *FeConvolveMatrix) Bias() float32                { return fe.bias }
func (fe *FeConvolveMatrix) EdgeMode() EdgeMode           { return fe.edgeMode }
func (fe *FeConvolveMatrix) PreserveAlpha() bool          { return fe.preserveAlpha }
func (fe *FeConvolveMatrix) HasInput(in FilterInput) bool { return fe.input == in }

// ConvolveMatrixData is a convolve matrix with its target cell.
type ConvolveMatrixData struct {
	targetX, targetY uint32
	columns, rows    uint32
	data             []float32
}

// newConvolveMatrixData is ConvolveMatrixData::new: ok is false when columns*rows (wrapping, as the
// release build) is not len(data) or a target is out of range.
func newConvolveMatrixData(targetX, targetY, columns, rows uint32, data []float32) (ConvolveMatrixData, bool) {
	if uint64(columns*rows) != uint64(len(data)) || targetX >= columns || targetY >= rows {
		return ConvolveMatrixData{}, false
	}
	return ConvolveMatrixData{targetX: targetX, targetY: targetY, columns: columns, rows: rows, data: data}, true
}

func (m *ConvolveMatrixData) TargetX() uint32 { return m.targetX }
func (m *ConvolveMatrixData) TargetY() uint32 { return m.targetY }
func (m *ConvolveMatrixData) Columns() uint32 { return m.columns }
func (m *ConvolveMatrixData) Rows() uint32    { return m.rows }
func (m *ConvolveMatrixData) Data() []float32 { return m.data }

// Get returns the value at column x, row y; it panics when out of bounds.
func (m *ConvolveMatrixData) Get(x, y uint32) float32 { return m.data[y*m.columns+x] }

// EdgeMode is `edgeMode`.
type EdgeMode uint8

const (
	EdgeModeNone EdgeMode = iota
	EdgeModeDuplicate
	EdgeModeWrap
)

// FeDisplacementMap is `feDisplacementMap`.
type FeDisplacementMap struct {
	input1, input2                     FilterInput
	scale                              float32
	xChannelSelector, yChannelSelector ColorChannel
}

func (fe *FeDisplacementMap) Input1() FilterInput            { return fe.input1 }
func (fe *FeDisplacementMap) Input2() FilterInput            { return fe.input2 }
func (fe *FeDisplacementMap) Scale() float32                 { return fe.scale }
func (fe *FeDisplacementMap) XChannelSelector() ColorChannel { return fe.xChannelSelector }
func (fe *FeDisplacementMap) YChannelSelector() ColorChannel { return fe.yChannelSelector }
func (fe *FeDisplacementMap) HasInput(in FilterInput) bool {
	return fe.input1 == in || fe.input2 == in
}

// ColorChannel is a color channel selector.
type ColorChannel uint8

const (
	ColorChannelR ColorChannel = iota
	ColorChannelG
	ColorChannelB
	ColorChannelA
)

// FeDropShadow is `feDropShadow`: feGaussianBlur, feOffset and feFlood joined together.
type FeDropShadow struct {
	input            FilterInput
	dx, dy           float32
	stdDevX, stdDevY float32 // PositiveF32
	color            Color
	opacity          float32
}

func (fe *FeDropShadow) Input() FilterInput           { return fe.input }
func (fe *FeDropShadow) Dx() float32                  { return fe.dx }
func (fe *FeDropShadow) Dy() float32                  { return fe.dy }
func (fe *FeDropShadow) StdDevX() float32             { return fe.stdDevX }
func (fe *FeDropShadow) StdDevY() float32             { return fe.stdDevY }
func (fe *FeDropShadow) Color() Color                 { return fe.color }
func (fe *FeDropShadow) Opacity() float32             { return fe.opacity }
func (fe *FeDropShadow) HasInput(in FilterInput) bool { return fe.input == in }

// FeFlood is `feFlood`.
type FeFlood struct {
	color   Color
	opacity float32
}

func (fe *FeFlood) Color() Color              { return fe.color }
func (fe *FeFlood) Opacity() float32          { return fe.opacity }
func (fe *FeFlood) HasInput(FilterInput) bool { return false }

// FeGaussianBlur is `feGaussianBlur`.
type FeGaussianBlur struct {
	input            FilterInput
	stdDevX, stdDevY float32 // PositiveF32
}

func (fe *FeGaussianBlur) Input() FilterInput           { return fe.input }
func (fe *FeGaussianBlur) StdDevX() float32             { return fe.stdDevX }
func (fe *FeGaussianBlur) StdDevY() float32             { return fe.stdDevY }
func (fe *FeGaussianBlur) HasInput(in FilterInput) bool { return fe.input == in }

// FeImage is `feImage`: the referenced element or image, converted.
type FeImage struct {
	root Group
}

func (fe *FeImage) Root() *Group              { return &fe.root }
func (fe *FeImage) HasInput(FilterInput) bool { return false }

// FeDiffuseLighting is `feDiffuseLighting`.
type FeDiffuseLighting struct {
	input           FilterInput
	surfaceScale    float32
	diffuseConstant float32
	lightingColor   Color
	lightSource     LightSource
}

func (fe *FeDiffuseLighting) Input() FilterInput           { return fe.input }
func (fe *FeDiffuseLighting) SurfaceScale() float32        { return fe.surfaceScale }
func (fe *FeDiffuseLighting) DiffuseConstant() float32     { return fe.diffuseConstant }
func (fe *FeDiffuseLighting) LightingColor() Color         { return fe.lightingColor }
func (fe *FeDiffuseLighting) LightSource() LightSource     { return fe.lightSource }
func (fe *FeDiffuseLighting) HasInput(in FilterInput) bool { return fe.input == in }

// FeSpecularLighting is `feSpecularLighting`.
type FeSpecularLighting struct {
	input            FilterInput
	surfaceScale     float32
	specularConstant float32
	specularExponent float32 // in 1..=128
	lightingColor    Color
	lightSource      LightSource
}

func (fe *FeSpecularLighting) Input() FilterInput           { return fe.input }
func (fe *FeSpecularLighting) SurfaceScale() float32        { return fe.surfaceScale }
func (fe *FeSpecularLighting) SpecularConstant() float32    { return fe.specularConstant }
func (fe *FeSpecularLighting) SpecularExponent() float32    { return fe.specularExponent }
func (fe *FeSpecularLighting) LightingColor() Color         { return fe.lightingColor }
func (fe *FeSpecularLighting) LightSource() LightSource     { return fe.lightSource }
func (fe *FeSpecularLighting) HasInput(in FilterInput) bool { return fe.input == in }

// LightSource is a DistantLight, PointLight or SpotLight.
type LightSource interface{ lightSource() }

// DistantLight is `feDistantLight`; the angles are in degrees.
type DistantLight struct {
	Azimuth, Elevation float32
}

// PointLight is `fePointLight`.
type PointLight struct {
	X, Y, Z float32
}

// SpotLight is `feSpotLight`. SpecularExponent is not negative; LimitingConeAngle is set when
// HasLimitingConeAngle is.
type SpotLight struct {
	X, Y, Z                         float32
	PointsAtX, PointsAtY, PointsAtZ float32
	SpecularExponent                float32
	LimitingConeAngle               float32
	HasLimitingConeAngle            bool
}

func (DistantLight) lightSource() {}
func (PointLight) lightSource()   {}
func (SpotLight) lightSource()    {}

// FeMerge is `feMerge`: one input per `feMergeNode`.
type FeMerge struct {
	inputs []FilterInput
}

func (fe *FeMerge) Inputs() []FilterInput { return fe.inputs }
func (fe *FeMerge) HasInput(in FilterInput) bool {
	for _, i := range fe.inputs {
		if i == in {
			return true
		}
	}
	return false
}

// FeMorphology is `feMorphology`; a zero radius disables the primitive.
type FeMorphology struct {
	input            FilterInput
	operator         MorphologyOperator
	radiusX, radiusY float32 // PositiveF32
}

func (fe *FeMorphology) Input() FilterInput           { return fe.input }
func (fe *FeMorphology) Operator() MorphologyOperator { return fe.operator }
func (fe *FeMorphology) RadiusX() float32             { return fe.radiusX }
func (fe *FeMorphology) RadiusY() float32             { return fe.radiusY }
func (fe *FeMorphology) HasInput(in FilterInput) bool { return fe.input == in }

// MorphologyOperator is a morphology `operator`.
type MorphologyOperator uint8

const (
	MorphologyErode MorphologyOperator = iota
	MorphologyDilate
)

// FeOffset is `feOffset`.
type FeOffset struct {
	input  FilterInput
	dx, dy float32
}

func (fe *FeOffset) Input() FilterInput           { return fe.input }
func (fe *FeOffset) Dx() float32                  { return fe.dx }
func (fe *FeOffset) Dy() float32                  { return fe.dy }
func (fe *FeOffset) HasInput(in FilterInput) bool { return fe.input == in }

// FeTile is `feTile`.
type FeTile struct {
	input FilterInput
}

func (fe *FeTile) Input() FilterInput           { return fe.input }
func (fe *FeTile) HasInput(in FilterInput) bool { return fe.input == in }

// FeTurbulence is `feTurbulence`.
type FeTurbulence struct {
	baseFrequencyX, baseFrequencyY float32 // PositiveF32
	numOctaves                     uint32
	seed                           int32
	stitchTiles                    bool
	kind                           TurbulenceKind
}

func (fe *FeTurbulence) BaseFrequencyX() float32   { return fe.baseFrequencyX }
func (fe *FeTurbulence) BaseFrequencyY() float32   { return fe.baseFrequencyY }
func (fe *FeTurbulence) NumOctaves() uint32        { return fe.numOctaves }
func (fe *FeTurbulence) Seed() int32               { return fe.seed }
func (fe *FeTurbulence) StitchTiles() bool         { return fe.stitchTiles }
func (fe *FeTurbulence) Kind() TurbulenceKind      { return fe.kind }
func (fe *FeTurbulence) HasInput(FilterInput) bool { return false }

// TurbulenceKind is the turbulence `type`.
type TurbulenceKind uint8

const (
	TurbulenceFractalNoise TurbulenceKind = iota
	TurbulenceTurbulence
)
