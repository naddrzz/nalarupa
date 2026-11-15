// Port of usvg 0.47 src/parser/svgtree/names.rs (generated from it): element and attribute ids.
// The phf/siphasher table is a Go map; the recognised names are exactly the Rust table entries.

package usvg

// eid is usvg's EId: an SVG element id.
type eid uint16

const (
	eidA eid = iota
	eidCircle
	eidClipPath
	eidDefs
	eidEllipse
	eidFeBlend
	eidFeColorMatrix
	eidFeComponentTransfer
	eidFeComposite
	eidFeConvolveMatrix
	eidFeDiffuseLighting
	eidFeDisplacementMap
	eidFeDistantLight
	eidFeDropShadow
	eidFeFlood
	eidFeFuncA
	eidFeFuncB
	eidFeFuncG
	eidFeFuncR
	eidFeGaussianBlur
	eidFeImage
	eidFeMerge
	eidFeMergeNode
	eidFeMorphology
	eidFeOffset
	eidFePointLight
	eidFeSpecularLighting
	eidFeSpotLight
	eidFeTile
	eidFeTurbulence
	eidFilter
	eidG
	eidImage
	eidLine
	eidLinearGradient
	eidMarker
	eidMask
	eidPath
	eidPattern
	eidPolygon
	eidPolyline
	eidRadialGradient
	eidRect
	eidStop
	eidStyle
	eidSvg
	eidSwitch
	eidSymbol
	eidText
	eidTextPath
	eidTref
	eidTspan
	eidUse
)

// svgEIdNames maps each eid to its SVG name (Rust's to_str).
var svgEIdNames = [...]string{
	eidA:                   "a",
	eidCircle:              "circle",
	eidClipPath:            "clipPath",
	eidDefs:                "defs",
	eidEllipse:             "ellipse",
	eidFeBlend:             "feBlend",
	eidFeColorMatrix:       "feColorMatrix",
	eidFeComponentTransfer: "feComponentTransfer",
	eidFeComposite:         "feComposite",
	eidFeConvolveMatrix:    "feConvolveMatrix",
	eidFeDiffuseLighting:   "feDiffuseLighting",
	eidFeDisplacementMap:   "feDisplacementMap",
	eidFeDistantLight:      "feDistantLight",
	eidFeDropShadow:        "feDropShadow",
	eidFeFlood:             "feFlood",
	eidFeFuncA:             "feFuncA",
	eidFeFuncB:             "feFuncB",
	eidFeFuncG:             "feFuncG",
	eidFeFuncR:             "feFuncR",
	eidFeGaussianBlur:      "feGaussianBlur",
	eidFeImage:             "feImage",
	eidFeMerge:             "feMerge",
	eidFeMergeNode:         "feMergeNode",
	eidFeMorphology:        "feMorphology",
	eidFeOffset:            "feOffset",
	eidFePointLight:        "fePointLight",
	eidFeSpecularLighting:  "feSpecularLighting",
	eidFeSpotLight:         "feSpotLight",
	eidFeTile:              "feTile",
	eidFeTurbulence:        "feTurbulence",
	eidFilter:              "filter",
	eidG:                   "g",
	eidImage:               "image",
	eidLine:                "line",
	eidLinearGradient:      "linearGradient",
	eidMarker:              "marker",
	eidMask:                "mask",
	eidPath:                "path",
	eidPattern:             "pattern",
	eidPolygon:             "polygon",
	eidPolyline:            "polyline",
	eidRadialGradient:      "radialGradient",
	eidRect:                "rect",
	eidStop:                "stop",
	eidStyle:               "style",
	eidSvg:                 "svg",
	eidSwitch:              "switch",
	eidSymbol:              "symbol",
	eidText:                "text",
	eidTextPath:            "textPath",
	eidTref:                "tref",
	eidTspan:               "tspan",
	eidUse:                 "use",
}

// svgEIdByName is the reverse of svgEIdNames (Rust's from_str over the phf table).
var svgEIdByName = func() map[string]eid {
	m := make(map[string]eid, len(svgEIdNames))
	for i, s := range svgEIdNames {
		m[s] = eid(i)
	}
	return m
}()

// aid is usvg's AId: an SVG attribute id (local name, any of the svg, xlink and xml namespaces).
type aid uint16

const (
	aidAlignmentBaseline aid = iota
	aidAmplitude
	aidAzimuth
	aidBackgroundColor
	aidBaseFrequency
	aidBaselineShift
	aidBias
	aidClass
	aidClip
	aidClipPath
	aidClipRule
	aidClipPathUnits
	aidColor
	aidColorInterpolation
	aidColorInterpolationFilters
	aidColorProfile
	aidColorRendering
	aidCx
	aidCy
	aidD
	aidDiffuseConstant
	aidDirection
	aidDisplay
	aidDivisor
	aidDominantBaseline
	aidDx
	aidDy
	aidEdgeMode
	aidElevation
	aidEnableBackground
	aidExponent
	aidFill
	aidFillOpacity
	aidFillRule
	aidFilter
	aidFilterUnits
	aidFloodColor
	aidFloodOpacity
	aidFont
	aidFontFamily
	aidFontFeatureSettings
	aidFontKerning
	aidFontOpticalSizing
	aidFontSize
	aidFontSizeAdjust
	aidFontStretch
	aidFontStyle
	aidFontSynthesis
	aidFontVariant
	aidFontVariantCaps
	aidFontVariantEastAsian
	aidFontVariantLigatures
	aidFontVariantNumeric
	aidFontVariantPosition
	aidFontVariationSettings
	aidFontWeight
	aidFr
	aidFx
	aidFy
	aidGlyphOrientationHorizontal
	aidGlyphOrientationVertical
	aidGradientTransform
	aidGradientUnits
	aidHeight
	aidHref
	aidId
	aidImageRendering
	aidIn
	aidIn2
	aidInlineSize
	aidIntercept
	aidIsolation
	aidK1
	aidK2
	aidK3
	aidK4
	aidKernelMatrix
	aidKernelUnitLength
	aidKerning
	aidLengthAdjust
	aidLetterSpacing
	aidLightingColor
	aidLimitingConeAngle
	aidLineHeight
	aidMarkerEnd
	aidMarkerMid
	aidMarkerStart
	aidMarkerHeight
	aidMarkerUnits
	aidMarkerWidth
	aidMask
	aidMaskBorder
	aidMaskBorderMode
	aidMaskBorderOutset
	aidMaskBorderRepeat
	aidMaskBorderSlice
	aidMaskBorderSource
	aidMaskBorderWidth
	aidMaskClip
	aidMaskComposite
	aidMaskImage
	aidMaskMode
	aidMaskOrigin
	aidMaskPosition
	aidMaskSize
	aidMaskType
	aidMaskContentUnits
	aidMaskUnits
	aidMixBlendMode
	aidMode
	aidNumOctaves
	aidOffset
	aidOpacity
	aidOperator
	aidOrder
	aidOrient
	aidOverflow
	aidPaintOrder
	aidPath
	aidPathLength
	aidPatternContentUnits
	aidPatternTransform
	aidPatternUnits
	aidPoints
	aidPointsAtX
	aidPointsAtY
	aidPointsAtZ
	aidPreserveAlpha
	aidPreserveAspectRatio
	aidPrimitiveUnits
	aidR
	aidRadius
	aidRefX
	aidRefY
	aidRequiredExtensions
	aidRequiredFeatures
	aidResult
	aidRotate
	aidRx
	aidRy
	aidScale
	aidSeed
	aidShapeImageThreshold
	aidShapeInside
	aidShapeMargin
	aidShapePadding
	aidShapeRendering
	aidShapeSubtract
	aidSide
	aidSlope
	aidSpace
	aidSpecularConstant
	aidSpecularExponent
	aidSpreadMethod
	aidStartOffset
	aidStdDeviation
	aidStitchTiles
	aidStopColor
	aidStopOpacity
	aidStroke
	aidStrokeDasharray
	aidStrokeDashoffset
	aidStrokeLinecap
	aidStrokeLinejoin
	aidStrokeMiterlimit
	aidStrokeOpacity
	aidStrokeWidth
	aidStyle
	aidSurfaceScale
	aidSystemLanguage
	aidTableValues
	aidTargetX
	aidTargetY
	aidTextAlign
	aidTextAlignLast
	aidTextAnchor
	aidTextDecoration
	aidTextDecorationColor
	aidTextDecorationFill
	aidTextDecorationLine
	aidTextDecorationStroke
	aidTextDecorationStyle
	aidTextIndent
	aidTextOrientation
	aidTextOverflow
	aidTextRendering
	aidTextUnderlinePosition
	aidTextLength
	aidTransform
	aidTransformBox
	aidTransformOrigin
	aidType
	aidUnicodeBidi
	aidUnicodeRange
	aidValues
	aidVectorEffect
	aidViewBox
	aidVisibility
	aidWhiteSpace
	aidWidth
	aidWordSpacing
	aidWritingMode
	aidX
	aidX1
	aidX2
	aidXChannelSelector
	aidY
	aidY1
	aidY2
	aidYChannelSelector
	aidZ
)

// svgAIdNames maps each aid to its SVG name (Rust's to_str).
var svgAIdNames = [...]string{
	aidAlignmentBaseline:          "alignment-baseline",
	aidAmplitude:                  "amplitude",
	aidAzimuth:                    "azimuth",
	aidBackgroundColor:            "background-color",
	aidBaseFrequency:              "baseFrequency",
	aidBaselineShift:              "baseline-shift",
	aidBias:                       "bias",
	aidClass:                      "class",
	aidClip:                       "clip",
	aidClipPath:                   "clip-path",
	aidClipRule:                   "clip-rule",
	aidClipPathUnits:              "clipPathUnits",
	aidColor:                      "color",
	aidColorInterpolation:         "color-interpolation",
	aidColorInterpolationFilters:  "color-interpolation-filters",
	aidColorProfile:               "color-profile",
	aidColorRendering:             "color-rendering",
	aidCx:                         "cx",
	aidCy:                         "cy",
	aidD:                          "d",
	aidDiffuseConstant:            "diffuseConstant",
	aidDirection:                  "direction",
	aidDisplay:                    "display",
	aidDivisor:                    "divisor",
	aidDominantBaseline:           "dominant-baseline",
	aidDx:                         "dx",
	aidDy:                         "dy",
	aidEdgeMode:                   "edgeMode",
	aidElevation:                  "elevation",
	aidEnableBackground:           "enable-background",
	aidExponent:                   "exponent",
	aidFill:                       "fill",
	aidFillOpacity:                "fill-opacity",
	aidFillRule:                   "fill-rule",
	aidFilter:                     "filter",
	aidFilterUnits:                "filterUnits",
	aidFloodColor:                 "flood-color",
	aidFloodOpacity:               "flood-opacity",
	aidFont:                       "font",
	aidFontFamily:                 "font-family",
	aidFontFeatureSettings:        "font-feature-settings",
	aidFontKerning:                "font-kerning",
	aidFontOpticalSizing:          "font-optical-sizing",
	aidFontSize:                   "font-size",
	aidFontSizeAdjust:             "font-size-adjust",
	aidFontStretch:                "font-stretch",
	aidFontStyle:                  "font-style",
	aidFontSynthesis:              "font-synthesis",
	aidFontVariant:                "font-variant",
	aidFontVariantCaps:            "font-variant-caps",
	aidFontVariantEastAsian:       "font-variant-east-asian",
	aidFontVariantLigatures:       "font-variant-ligatures",
	aidFontVariantNumeric:         "font-variant-numeric",
	aidFontVariantPosition:        "font-variant-position",
	aidFontVariationSettings:      "font-variation-settings",
	aidFontWeight:                 "font-weight",
	aidFr:                         "fr",
	aidFx:                         "fx",
	aidFy:                         "fy",
	aidGlyphOrientationHorizontal: "glyph-orientation-horizontal",
	aidGlyphOrientationVertical:   "glyph-orientation-vertical",
	aidGradientTransform:          "gradientTransform",
	aidGradientUnits:              "gradientUnits",
	aidHeight:                     "height",
	aidHref:                       "href",
	aidId:                         "id",
	aidImageRendering:             "image-rendering",
	aidIn:                         "in",
	aidIn2:                        "in2",
	aidInlineSize:                 "inline-size",
	aidIntercept:                  "intercept",
	aidIsolation:                  "isolation",
	aidK1:                         "k1",
	aidK2:                         "k2",
	aidK3:                         "k3",
	aidK4:                         "k4",
	aidKernelMatrix:               "kernelMatrix",
	aidKernelUnitLength:           "kernelUnitLength",
	aidKerning:                    "kerning",
	aidLengthAdjust:               "lengthAdjust",
	aidLetterSpacing:              "letter-spacing",
	aidLightingColor:              "lighting-color",
	aidLimitingConeAngle:          "limitingConeAngle",
	aidLineHeight:                 "line-height",
	aidMarkerEnd:                  "marker-end",
	aidMarkerMid:                  "marker-mid",
	aidMarkerStart:                "marker-start",
	aidMarkerHeight:               "markerHeight",
	aidMarkerUnits:                "markerUnits",
	aidMarkerWidth:                "markerWidth",
	aidMask:                       "mask",
	aidMaskBorder:                 "mask-border",
	aidMaskBorderMode:             "mask-border-mode",
	aidMaskBorderOutset:           "mask-border-outset",
	aidMaskBorderRepeat:           "mask-border-repeat",
	aidMaskBorderSlice:            "mask-border-slice",
	aidMaskBorderSource:           "mask-border-source",
	aidMaskBorderWidth:            "mask-border-width",
	aidMaskClip:                   "mask-clip",
	aidMaskComposite:              "mask-composite",
	aidMaskImage:                  "mask-image",
	aidMaskMode:                   "mask-mode",
	aidMaskOrigin:                 "mask-origin",
	aidMaskPosition:               "mask-position",
	aidMaskSize:                   "mask-size",
	aidMaskType:                   "mask-type",
	aidMaskContentUnits:           "maskContentUnits",
	aidMaskUnits:                  "maskUnits",
	aidMixBlendMode:               "mix-blend-mode",
	aidMode:                       "mode",
	aidNumOctaves:                 "numOctaves",
	aidOffset:                     "offset",
	aidOpacity:                    "opacity",
	aidOperator:                   "operator",
	aidOrder:                      "order",
	aidOrient:                     "orient",
	aidOverflow:                   "overflow",
	aidPaintOrder:                 "paint-order",
	aidPath:                       "path",
	aidPathLength:                 "pathLength",
	aidPatternContentUnits:        "patternContentUnits",
	aidPatternTransform:           "patternTransform",
	aidPatternUnits:               "patternUnits",
	aidPoints:                     "points",
	aidPointsAtX:                  "pointsAtX",
	aidPointsAtY:                  "pointsAtY",
	aidPointsAtZ:                  "pointsAtZ",
	aidPreserveAlpha:              "preserveAlpha",
	aidPreserveAspectRatio:        "preserveAspectRatio",
	aidPrimitiveUnits:             "primitiveUnits",
	aidR:                          "r",
	aidRadius:                     "radius",
	aidRefX:                       "refX",
	aidRefY:                       "refY",
	aidRequiredExtensions:         "requiredExtensions",
	aidRequiredFeatures:           "requiredFeatures",
	aidResult:                     "result",
	aidRotate:                     "rotate",
	aidRx:                         "rx",
	aidRy:                         "ry",
	aidScale:                      "scale",
	aidSeed:                       "seed",
	aidShapeImageThreshold:        "shape-image-threshold",
	aidShapeInside:                "shape-inside",
	aidShapeMargin:                "shape-margin",
	aidShapePadding:               "shape-padding",
	aidShapeRendering:             "shape-rendering",
	aidShapeSubtract:              "shape-subtract",
	aidSide:                       "side",
	aidSlope:                      "slope",
	aidSpace:                      "space",
	aidSpecularConstant:           "specularConstant",
	aidSpecularExponent:           "specularExponent",
	aidSpreadMethod:               "spreadMethod",
	aidStartOffset:                "startOffset",
	aidStdDeviation:               "stdDeviation",
	aidStitchTiles:                "stitchTiles",
	aidStopColor:                  "stop-color",
	aidStopOpacity:                "stop-opacity",
	aidStroke:                     "stroke",
	aidStrokeDasharray:            "stroke-dasharray",
	aidStrokeDashoffset:           "stroke-dashoffset",
	aidStrokeLinecap:              "stroke-linecap",
	aidStrokeLinejoin:             "stroke-linejoin",
	aidStrokeMiterlimit:           "stroke-miterlimit",
	aidStrokeOpacity:              "stroke-opacity",
	aidStrokeWidth:                "stroke-width",
	aidStyle:                      "style",
	aidSurfaceScale:               "surfaceScale",
	aidSystemLanguage:             "systemLanguage",
	aidTableValues:                "tableValues",
	aidTargetX:                    "targetX",
	aidTargetY:                    "targetY",
	aidTextAlign:                  "text-align",
	aidTextAlignLast:              "text-align-last",
	aidTextAnchor:                 "text-anchor",
	aidTextDecoration:             "text-decoration",
	aidTextDecorationColor:        "text-decoration-color",
	aidTextDecorationFill:         "text-decoration-fill",
	aidTextDecorationLine:         "text-decoration-line",
	aidTextDecorationStroke:       "text-decoration-stroke",
	aidTextDecorationStyle:        "text-decoration-style",
	aidTextIndent:                 "text-indent",
	aidTextOrientation:            "text-orientation",
	aidTextOverflow:               "text-overflow",
	aidTextRendering:              "text-rendering",
	aidTextUnderlinePosition:      "text-underline-position",
	aidTextLength:                 "textLength",
	aidTransform:                  "transform",
	aidTransformBox:               "transform-box",
	aidTransformOrigin:            "transform-origin",
	aidType:                       "type",
	aidUnicodeBidi:                "unicode-bidi",
	aidUnicodeRange:               "unicode-range",
	aidValues:                     "values",
	aidVectorEffect:               "vector-effect",
	aidViewBox:                    "viewBox",
	aidVisibility:                 "visibility",
	aidWhiteSpace:                 "white-space",
	aidWidth:                      "width",
	aidWordSpacing:                "word-spacing",
	aidWritingMode:                "writing-mode",
	aidX:                          "x",
	aidX1:                         "x1",
	aidX2:                         "x2",
	aidXChannelSelector:           "xChannelSelector",
	aidY:                          "y",
	aidY1:                         "y1",
	aidY2:                         "y2",
	aidYChannelSelector:           "yChannelSelector",
	aidZ:                          "z",
}

// svgAIdByName is the reverse of svgAIdNames (Rust's from_str over the phf table).
var svgAIdByName = func() map[string]aid {
	m := make(map[string]aid, len(svgAIdNames))
	for i, s := range svgAIdNames {
		m[s] = aid(i)
	}
	return m
}()

// svgParseEID is EId::from_str.
func svgParseEID(s string) (eid, bool) {
	e, ok := svgEIdByName[s]
	return e, ok
}

// String is EId::to_str.
func (e eid) String() string { return svgEIdNames[e] }

// svgParseAID is AId::from_str.
func svgParseAID(s string) (aid, bool) {
	a, ok := svgAIdByName[s]
	return a, ok
}

// String is AId::to_str.
func (a aid) String() string { return svgAIdNames[a] }
