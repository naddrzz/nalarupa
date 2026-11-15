// Port of usvg 0.47 src/parser/switch.rs.

package usvg

import (
	"slices"
	"strings"
)

// switchFeatures are the supported feature strings (https://www.w3.org/TR/SVG11/feature.html).
var switchFeatures = []string{
	"http://www.w3.org/TR/SVG11/feature#SVGDOM-static",
	"http://www.w3.org/TR/SVG11/feature#SVG-static",
	"http://www.w3.org/TR/SVG11/feature#CoreAttribute", // no xml:base and xml:lang
	"http://www.w3.org/TR/SVG11/feature#Structure",
	"http://www.w3.org/TR/SVG11/feature#BasicStructure",
	"http://www.w3.org/TR/SVG11/feature#ContainerAttribute", // `enable-background`
	"http://www.w3.org/TR/SVG11/feature#ConditionalProcessing",
	"http://www.w3.org/TR/SVG11/feature#Image",
	"http://www.w3.org/TR/SVG11/feature#Style",
	// "http://www.w3.org/TR/SVG11/feature#ViewportAttribute", // `clip` and `overflow`, not yet
	"http://www.w3.org/TR/SVG11/feature#Shape",
	"http://www.w3.org/TR/SVG11/feature#Text",
	"http://www.w3.org/TR/SVG11/feature#BasicText",
	"http://www.w3.org/TR/SVG11/feature#PaintAttribute",      // no color-interpolation and color-rendering
	"http://www.w3.org/TR/SVG11/feature#BasicPaintAttribute", // no color-interpolation
	"http://www.w3.org/TR/SVG11/feature#OpacityAttribute",
	"http://www.w3.org/TR/SVG11/feature#GraphicsAttribute",
	"http://www.w3.org/TR/SVG11/feature#BasicGraphicsAttribute",
	"http://www.w3.org/TR/SVG11/feature#Marker",
	// "http://www.w3.org/TR/SVG11/feature#ColorProfile", // not yet
	"http://www.w3.org/TR/SVG11/feature#Gradient",
	"http://www.w3.org/TR/SVG11/feature#Pattern",
	"http://www.w3.org/TR/SVG11/feature#Clip",
	"http://www.w3.org/TR/SVG11/feature#BasicClip",
	"http://www.w3.org/TR/SVG11/feature#Mask",
	"http://www.w3.org/TR/SVG11/feature#Filter",
	"http://www.w3.org/TR/SVG11/feature#BasicFilter",
	// only xlink:href
	"http://www.w3.org/TR/SVG11/feature#XlinkAttribute",
	// "http://www.w3.org/TR/SVG11/feature#Font",
	// "http://www.w3.org/TR/SVG11/feature#BasicFont",
}

// convertSwitch is switch::convert: only the first child whose conditions pass is rendered.
func convertSwitch(node svgNode, state *converterState, cache *converterCache, parent *Group) {
	var child svgNode
	found := false
	for c := range node.Children() {
		if isConditionPassed(c, state.opt) {
			child, found = c, true
			break
		}
	}
	if !found {
		return
	}
	if g := convertGroup(node, state, false, cache, parent, func(cache *converterCache, g *Group) {
		convertElement(child, state, cache, g)
	}); g != nil {
		parent.children = append(parent.children, g)
	}
}

// isConditionPassed is is_condition_passed: requiredExtensions always fails, requiredFeatures needs
// every listed feature, systemLanguage one of Options.Languages.
func isConditionPassed(node svgNode, opt *Options) bool {
	if !node.IsElement() {
		return false
	}
	if node.HasAttribute(aidRequiredExtensions) {
		return false
	}
	if features, ok := node.attrStr(aidRequiredFeatures); ok {
		for _, feature := range strings.Split(features, " ") {
			if !slices.Contains(switchFeatures, feature) {
				return false
			}
		}
	}
	return isValidSysLang(node, opt)
}

// isValidSysLang is is_valid_sys_lang (SVG spec 5.8.5): only simple BCP 47 values like `en` or
// `en-US`; a language matches exactly, or a user language matches its prefix before a `-`.
func isValidSysLang(node svgNode, opt *Options) bool {
	langs, ok := node.attrStr(aidSystemLanguage)
	if !ok {
		return true
	}
	for _, lang := range strings.Split(langs, ",") {
		lang = strings.TrimFunc(lang, isRustWhitespace)
		if slices.Contains(opt.Languages, lang) {
			return true
		}
		if idx := strings.IndexByte(lang, '-'); idx >= 0 {
			if slices.Contains(opt.Languages, lang[:idx]) {
				return true
			}
		}
	}
	return false
}

// isRustWhitespace is char::is_whitespace (Unicode White_Space), which str::trim uses.
func isRustWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}
