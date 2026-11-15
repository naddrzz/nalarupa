package usvg

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// svgTestParseErr parses text as usvg's Tree::from_str does (DTD allowed), then builds the svgtree.
func svgTestParseErr(t *testing.T, text string, sheet ...string) (*svgDocument, error) {
	t.Helper()
	x, err := xmlParseWithOptions(text, xmlParsingOptions{AllowDTD: true, NodesLimit: math.MaxUint32})
	if err != nil {
		t.Fatalf("xml: %v", err)
	}
	if len(sheet) > 0 {
		return svgParseTree(x, sheet[0], true)
	}
	return svgParseTree(x, "", false)
}

func svgTestParse(t *testing.T, text string, sheet ...string) *svgDocument {
	t.Helper()
	doc, err := svgTestParseErr(t, text, sheet...)
	if err != nil {
		t.Fatalf("svgParseTree: %v", err)
	}
	return doc
}

// svgTestAttrs formats a node's attributes as "name=value" ("!" marks important), in order.
func svgTestAttrs(n svgNode) string {
	var parts []string
	for _, a := range n.Attributes() {
		s := a.Name.String() + "=" + a.Value
		if a.Important {
			s += "!"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func svgTestByID(t *testing.T, doc *svgDocument, id string) svgNode {
	t.Helper()
	n, ok := doc.ElementByID(id)
	if !ok {
		t.Fatalf("no element #%s", id)
	}
	return n
}

// svgTestTags lists the tag names of the element descendants of n (excluding n), with text nodes as
// their quoted content.
func svgTestTags(n svgNode) string {
	var parts []string
	for d := range n.Descendants() {
		if d == n {
			continue
		}
		if tag, ok := d.TagName(); ok {
			parts = append(parts, tag.String())
		} else if d.IsText() {
			parts = append(parts, "'"+d.Text()+"'")
		}
	}
	return strings.Join(parts, " ")
}

func TestSvgNames(t *testing.T) {
	if len(svgEIdNames) != 53 || len(svgEIdByName) != 53 {
		t.Errorf("EId count %d/%d", len(svgEIdNames), len(svgEIdByName))
	}
	if len(svgAIdNames) != 211 || len(svgAIdByName) != 211 {
		t.Errorf("AId count %d/%d", len(svgAIdNames), len(svgAIdByName))
	}
	for i, s := range svgEIdNames {
		if e, ok := svgParseEID(s); !ok || e != eid(i) || e.String() != s {
			t.Errorf("EId %q round trip", s)
		}
	}
	for i, s := range svgAIdNames {
		if a, ok := svgParseAID(s); !ok || a != aid(i) || a.String() != s {
			t.Errorf("AId %q round trip", s)
		}
	}
	for _, c := range []struct {
		s  string
		e  eid
		ok bool
	}{
		{"linearGradient", eidLinearGradient, true}, {"feDropShadow", eidFeDropShadow, true},
		{"a", eidA, true}, {"Rect", 0, false}, {"lineargradient", 0, false}, {"foreignObject", 0, false},
		{"", 0, false},
	} {
		if e, ok := svgParseEID(c.s); ok != c.ok || (ok && e != c.e) {
			t.Errorf("svgParseEID(%q) = %v %v", c.s, e, ok)
		}
	}
	for _, c := range []struct {
		s  string
		a  aid
		ok bool
	}{
		{"stroke-width", aidStrokeWidth, true}, {"href", aidHref, true}, {"space", aidSpace, true},
		{"viewBox", aidViewBox, true}, {"viewbox", 0, false}, {"xlink:href", 0, false},
		{"marker", 0, false}, {"font", aidFont, true}, {"mask-type", aidMaskType, true},
	} {
		if a, ok := svgParseAID(c.s); ok != c.ok || (ok && a != c.a) {
			t.Errorf("svgParseAID(%q) = %v %v", c.s, a, ok)
		}
	}
	if !eidUse.isGraphic() || eidG.isGraphic() || !eidPattern.isPaintServer() || eidPattern.isGradient() {
		t.Error("EId predicates")
	}
	if !aidFill.isInheritable() || aidOpacity.isInheritable() || aidWidth.isInheritable() ||
		!aidTransform.isPresentation() || aidTransform.isInheritable() {
		t.Error("AId predicates")
	}
}

func TestSvgBasicTree(t *testing.T) {
	doc := svgTestParse(t, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:o="urn:other" id="root"
		width="10" unknown="1" o:fill="red" class="c" style="">
		<!-- comment -->
		<style>rect { stroke: blue }</style>
		<foo><rect id="lost"/></foo>
		<o:rect id="other"/>
		<a id="link"><circle r="1"/></a>
		<g><rect id="r" x="1" fill="red"/></g>
		text outside text is dropped
	</svg>`)
	root := doc.RootElement()
	if !root.hasTagName(eidSvg) || svgTestAttrs(root) != "id=root width=10" {
		t.Errorf("root: %s", svgTestAttrs(root))
	}
	if got := svgTestTags(root); got != "g circle g rect" {
		t.Errorf("tags: %q", got)
	}
	if _, ok := doc.ElementByID("lost"); ok {
		t.Error("children of unknown elements must be dropped")
	}
	if _, ok := doc.ElementByID("other"); ok {
		t.Error("elements in another namespace must be dropped")
	}
	if n := svgTestByID(t, doc, "link"); !n.hasTagName(eidG) {
		t.Error("`a` must become `g`")
	}
	r := svgTestByID(t, doc, "r")
	if svgTestAttrs(r) != "id=r x=1 fill=red stroke=blue" {
		t.Errorf("rect: %s", svgTestAttrs(r))
	}
	if p, _ := r.Parent(); !p.hasTagName(eidG) {
		t.Error("parent")
	}
	if pe, _ := r.ParentElement(); pe.ElementID() != "" {
		t.Error("parent element id")
	}
	if doc.Root().IsElement() || doc.Root().String() != "Root" {
		t.Error("root node")
	}
	var anc []string
	for a := range r.Ancestors() {
		anc = append(anc, a.String()[:4])
	}
	if strings.Join(anc, ",") != "Elem,Elem,Elem,Root" {
		t.Errorf("ancestors %v", anc)
	}
	want := "Document [\n    Element {\n        tag_name: Some(svg)\n        attributes: [\n" +
		"            Attribute { name: id, value: root, important: false }\n"
	if !strings.HasPrefix(doc.String(), want) {
		t.Errorf("debug:\n%s", doc.String())
	}
}

func TestSvgNoRootNode(t *testing.T) {
	for _, text := range []string{
		`<html/>`,
		`<svg xmlns="urn:other"/>`,
		`<style/>`,
		`<g><svg/></g>`,
	} {
		_, err := svgTestParseErr(t, text)
		var e *Error
		if !errors.As(err, &e) || e.Kind != ErrParsingFailed || e.KindName() != "ParsingFailed" ||
			e.XML.KindName() != "NoRootNode" {
			t.Errorf("%s: %v", text, err)
			continue
		}
		if e.Error() != "SVG data parsing failed cause the document does not have a root node" {
			t.Errorf("text %q", e.Error())
		}
	}
	if _, err := svgTestParseErr(t, `<svg xmlns="http://www.w3.org/2000/svg"/>`); err != nil {
		t.Error(err)
	}
}

func TestSvgErrorText(t *testing.T) {
	for _, c := range []struct {
		k          ErrorKind
		name, text string
	}{
		{ErrNotAnUtf8Str, "NotAnUtf8Str", "provided data has not an UTF-8 encoding"},
		{ErrMalformedGZip, "MalformedGZip", "provided data has a malformed GZip content"},
		{ErrElementsLimitReached, "ElementsLimitReached", "the maximum number of SVG elements has been reached"},
		{ErrInvalidSize, "InvalidSize", "SVG has an invalid size"},
	} {
		e := &Error{Kind: c.k}
		if e.KindName() != c.name || e.Error() != c.text {
			t.Errorf("%v: %s %s", c.k, e.KindName(), e.Error())
		}
	}
}

func TestSvgNodesLimit(t *testing.T) {
	nest := func(n int) string {
		return `<svg>` + strings.Repeat(`<g>`, n) + strings.Repeat(`</g>`, n) + `</svg>`
	}
	if _, err := svgTestParseErr(t, nest(1024)); err != nil {
		t.Errorf("depth 1024: %v", err)
	}
	_, err := svgTestParseErr(t, nest(1025))
	var e *Error
	if !errors.As(err, &e) || e.XML.KindName() != "NodesLimitReached" ||
		e.Error() != "SVG data parsing failed cause nodes limit reached" {
		t.Errorf("depth 1025: %v", err)
	}

	// Each level uses the previous one ten times: far more than 1_000_000 nodes from a small file.
	var b strings.Builder
	b.WriteString(`<svg xmlns:xlink="http://www.w3.org/1999/xlink"><defs><g id="g0">`)
	b.WriteString(strings.Repeat(`<rect/>`, 10))
	b.WriteString(`</g>`)
	for i := 1; i <= 6; i++ {
		b.WriteString(`<g id="g` + string(rune('0'+i)) + `">`)
		b.WriteString(strings.Repeat(`<use xlink:href="#g`+string(rune('0'+i-1))+`"/>`, 10))
		b.WriteString(`</g>`)
	}
	b.WriteString(`</defs></svg>`)
	_, err = svgTestParseErr(t, b.String())
	if !errors.As(err, &e) || e.Kind != ErrParsingFailed || e.XML.KindName() != "NodesLimitReached" {
		t.Errorf("node count: %v", err)
	}
}

func TestSvgCSSPrecedence(t *testing.T) {
	doc := svgTestParse(t, `<svg>
		<style type="text/css">
			#a { fill: blue }
			rect { fill: red; stroke: green; width: 5 }
			.c { fill: yellow }
			rect:first-child { opacity: 0.5 }
			[data-x="1"] { stroke-width: 3 }
			#imp { fill: red !important }
			rect.c2 { fill: lime !important }
			#imp2 { fill: red !important }
		</style>
		<style type="text/other">rect { fill: black }</style>
		<rect id="a" class="c" fill="gray" stroke-width="1" data-x="1"/>
		<rect id="b" class="c" fill="gray"/>
		<rect id="s" class="c" style="fill:purple; stroke : navy"/>
		<rect id="imp" style="fill:purple"/>
		<rect id="imp2" class="c2"/>
		<rect id="imp3" style="fill:red !important; fill:blue"/>
		<rect id="imp4" style="fill:red; fill:blue !important"/>
	</svg>`)
	for _, c := range []struct{ id, want string }{
		// Presentation attributes keep their position; CSS by specificity (#a last) wins.
		{"a", "id=a fill=blue stroke-width=3 stroke=green"},
		{"b", "id=b fill=yellow stroke=green"},
		// The style attribute wins over CSS.
		{"s", "id=s fill=purple stroke=navy"},
		// !important in CSS wins over a later style declaration.
		{"imp", "id=imp fill=red! stroke=green"},
		// Between important declarations the first applied (lower specificity) one wins.
		{"imp2", "id=imp2 fill=lime! stroke=green"},
		{"imp3", "id=imp3 fill=red! stroke=green"},
		{"imp4", "id=imp4 fill=blue! stroke=green"},
	} {
		if got := svgTestAttrs(svgTestByID(t, doc, c.id)); got != c.want {
			t.Errorf("#%s: %q, want %q", c.id, got, c.want)
		}
	}
	// :first-child matches the first element child of svg, the first style element: no rect.
	for n := range doc.Descendants() {
		if n.HasAttribute(aidOpacity) {
			t.Errorf("first-child matched %s", n.ElementID())
		}
	}
}

func TestSvgInjectedStyleSheet(t *testing.T) {
	text := `<svg><style>rect { fill: blue }</style><rect id="r" stroke="red"/></svg>`
	doc := svgTestParse(t, text, `rect { fill: green; stroke: black } svg rect { opacity: 0.1 }`)
	if got := svgTestAttrs(svgTestByID(t, doc, "r")); got != "id=r stroke=black fill=blue opacity=0.1" {
		t.Errorf("injected: %q", got)
	}
}

func TestSvgShorthands(t *testing.T) {
	doc := svgTestParse(t, `<svg>
		<rect id="f" font-weight="900" style="font: italic small-caps bold condensed 12px 'My Font', serif"/>
		<rect id="lh" font-size="3" style="font: 12px/2 serif"/>
		<rect id="f2" style="font: 10px serif"/>
		<rect id="bad" font-size="3" style="font: bold"/>
		<rect id="m" marker-mid="url(#x)" style="marker: url(#m)"/>
	</svg>`)
	for _, c := range []struct{ id, want string }{
		{"f", "id=f font-weight=bold font-style=italic font-variant=small-caps font-stretch=condensed " +
			"line-height=normal font-size-adjust=none font-kerning=auto font-variant-caps=normal " +
			"font-variant-ligatures=normal font-variant-numeric=normal font-variant-east-asian=normal " +
			"font-variant-position=normal font-size=12px font-family='My Font', serif"},
		{"f2", "id=f2 font-style=normal font-variant=normal font-weight=normal font-stretch=normal " +
			"line-height=normal font-size-adjust=none font-kerning=auto font-variant-caps=normal " +
			"font-variant-ligatures=normal font-variant-numeric=normal font-variant-east-asian=normal " +
			"font-variant-position=normal font-size=10px font-family=serif"},
		{"bad", "id=bad font-size=3"},
		// simplecss ends the declaration value at '/', leaving "12px", which is not a shorthand.
		{"lh", "id=lh font-size=3"},
		{"m", "id=m marker-mid=url(#m) marker-start=url(#m) marker-end=url(#m)"},
	} {
		if got := svgTestAttrs(svgTestByID(t, doc, c.id)); got != c.want {
			t.Errorf("#%s:\n got %q\nwant %q", c.id, got, c.want)
		}
	}
}

func TestSvgStyleOnlyProperties(t *testing.T) {
	doc := svgTestParse(t, `<svg>
		<g id="a" mix-blend-mode="multiply" isolation="isolate" font-kerning="none"
			image-rendering="pixelated" shape-rendering="crispEdges"/>
		<g id="b" image-rendering="optimizeSpeed"
			style="mix-blend-mode:multiply; isolation:isolate; image-rendering:pixelated; width:3; x:1"/>
	</svg>`)
	if got := svgTestAttrs(svgTestByID(t, doc, "a")); got != "id=a shape-rendering=crispEdges" {
		t.Errorf("a: %q", got)
	}
	b := svgTestByID(t, doc, "b")
	if got := svgTestAttrs(b); got != "id=b image-rendering=pixelated mix-blend-mode=multiply isolation=isolate" {
		t.Errorf("b: %q", got)
	}
	if m, ok := b.attrBlendMode(aidMixBlendMode); !ok || m != BlendModeMultiply {
		t.Error("blend mode")
	}
	if i, ok := b.attrIsolation(aidIsolation); !ok || i != svgIsolationIsolate {
		t.Error("isolation")
	}
	if r, ok := b.attrImageRendering(aidImageRendering); !ok || r != ImageRenderingPixelated {
		t.Error("image rendering")
	}
}

func TestSvgInheritValue(t *testing.T) {
	doc := svgTestParse(t, `<svg fill="red" opacity="0.3" stroke-width="2!">
		<g opacity="0.5" style="fill: blue !important">
			<rect id="a" fill="inherit" opacity="inherit" stroke-width="inherit" stroke="inherit"
				clip-path="inherit" x="inherit"/>
		</g>
		<g><rect id="b" opacity="inherit" font-size="inherit" writing-mode="inherit"/></g>
	</svg>`)
	if got := svgTestAttrs(svgTestByID(t, doc, "a")); got !=
		"id=a fill=blue! opacity=0.5 stroke-width=2! stroke=none clip-path=none x=inherit" {
		t.Errorf("a: %q", got)
	}
	if got := svgTestAttrs(svgTestByID(t, doc, "b")); got != "id=b opacity=1 font-size=medium writing-mode=lr-tb" {
		t.Errorf("b: %q", got)
	}
}

func TestSvgFindAttribute(t *testing.T) {
	doc := svgTestParse(t, `<svg fill="red" opacity="0.5" transform="scale(2)">
		<g id="g" stroke-width="3" opacity="0.25"><g id="g2"><rect id="r"/></g><rect id="r2"/></g>
	</svg>`)
	r, r2 := svgTestByID(t, doc, "r"), svgTestByID(t, doc, "r2")
	if n, ok := r.findAttributeNode(aidFill); !ok || n != doc.RootElement() {
		t.Error("inheritable fill must come from svg")
	}
	if w, ok := svgFindAttr(r, aidStrokeWidth, svgNode.attrLength); !ok || w.Number != 3 {
		t.Error("inheritable stroke-width")
	}
	if _, ok := r.findAttributeNode(aidOpacity); ok {
		t.Error("non-inheritable opacity must come only from the parent element")
	}
	if o, ok := svgFindAttr(r2, aidOpacity, svgNode.attrOpacity); !ok || o != 0.25 {
		t.Error("opacity from the parent element")
	}
	if _, ok := r.findAttributeNode(aidTransform); ok {
		t.Error("transform is not inheritable")
	}
	if _, ok := svgFindAttr(r, aidX, svgNode.attrLength); ok {
		t.Error("x")
	}
}

func TestSvgTypedAttributes(t *testing.T) {
	doc := svgTestParse(t, `<svg xmlns:xlink="http://www.w3.org/1999/xlink">
		<linearGradient id="lg"/>
		<rect id="r" opacity="50%" fill-opacity="2" stroke-opacity="1px" stop-opacity="-1"
			transform="scale(0)" gradientTransform="matrix(1 2 3 4 5 6)" patternTransform="foo"
			stroke="none" stroke-dasharray="1, 2 3" stroke-width="5mm" x="abc" values=""
			k1="1 x" fill="url(#lg) red" mask="none" clip-path="url(#lg)"
			xlink:href="#lg" gradientUnits="objectBoundingBox" visibility="collapse"
			spreadMethod="reflect" shape-rendering="auto" text-rendering="auto"
			image-rendering="auto" stroke-linecap="square" stroke-linejoin="miter-clip"
			fill-rule="evenodd" viewBox="0 0 10 20" preserveAspectRatio="xMinYMax slice"
			paint-order="stroke" color="#f00" orient="90deg" enable-background="new"
			transform-origin="left top" width=" 10 " height="10px" rotate="1 2"/>
	</svg>`)
	r := svgTestByID(t, doc, "r")
	if v, ok := r.attrOpacity(aidOpacity); !ok || v != 0.5 {
		t.Errorf("opacity %v", v)
	}
	if v, ok := r.attrOpacity(aidFillOpacity); !ok || v != 1 {
		t.Errorf("fill-opacity %v", v)
	}
	if v, ok := r.attrOpacity(aidStopOpacity); !ok || v != 0 {
		t.Errorf("stop-opacity %v", v)
	}
	if _, ok := r.attrOpacity(aidStrokeOpacity); ok {
		t.Error("stroke-opacity with a unit")
	}
	if v, ok := r.attrTransform(aidTransform); !ok || v != skia.Identity() {
		t.Errorf("invalid transform %v", v)
	}
	if v, ok := r.attrTransform(aidGradientTransform); !ok || v != skia.TransformFromRow(1, 2, 3, 4, 5, 6) {
		t.Errorf("transform %v", v)
	}
	if _, ok := r.attrTransform(aidPatternTransform); ok {
		t.Error("bad transform")
	}
	if _, ok := r.attrStr(aidStroke); ok {
		t.Error("stroke=none must read as absent")
	}
	if v, ok := svgTryAttr(r, aidStroke, svgParseStr); !ok || v != "none" {
		t.Error("try_attribute keeps none")
	}
	if _, ok := r.attrStr(aidMask); ok {
		t.Error("mask=none")
	}
	if v, ok := r.attrNumberList(aidStrokeDasharray); !ok || !slices.Equal(v, []float32{1, 2, 3}) {
		t.Errorf("dasharray %v", v)
	}
	if v, ok := r.attrNumberList(aidValues); !ok || v == nil || len(v) != 0 {
		t.Errorf("empty list %v", v)
	}
	if _, ok := r.attrNumberList(aidK1); ok {
		t.Error("bad number list")
	}
	if v, ok := r.attrLengthList(aidRotate); !ok || len(v) != 2 || v[1].Number != 2 {
		t.Errorf("length list %v", v)
	}
	if v, ok := r.attrLength(aidStrokeWidth); !ok || v != (stLength{5, stLengthUnitMm}) {
		t.Errorf("length %v", v)
	}
	if _, ok := r.attrF32(aidX); ok {
		t.Error("bad number")
	}
	if v, ok := r.attrF32(aidWidth); !ok || v != 10 {
		t.Errorf("number with leading space %v", v)
	}
	if v, ok := r.attrPaint(aidFill); !ok || v.Kind != stPaintFuncIRI || v.Link != "lg" || !v.HasFallback {
		t.Errorf("paint %+v", v)
	}
	lg := svgTestByID(t, doc, "lg")
	if v, ok := r.attrNode(aidClipPath); !ok || v != lg {
		t.Error("FuncIRI link")
	}
	if v, ok := r.attrNode(aidHref); !ok || v != lg {
		t.Error("IRI link")
	}
	if _, ok := r.attrNode(aidFill); ok {
		t.Error("a paint with a fallback is not a FuncIRI")
	}
	if v, _ := r.attrUnits(aidGradientUnits); v != unitsObjectBoundingBox {
		t.Error("units")
	}
	if v, _ := r.attrVisibility(aidVisibility); v != visibilityCollapse {
		t.Error("visibility")
	}
	if v, _ := r.attrSpreadMethod(aidSpreadMethod); v != SpreadMethodReflect {
		t.Error("spread")
	}
	if v, ok := r.attrShapeRendering(aidShapeRendering); !ok || v != ShapeRenderingGeometricPrecision {
		t.Error("shape-rendering")
	}
	if v, ok := r.attrTextRendering(aidTextRendering); !ok || v != TextRenderingOptimizeLegibility {
		t.Error("text-rendering")
	}
	if v, ok := r.attrImageRendering(aidImageRendering); !ok || v != ImageRenderingOptimizeQuality {
		t.Error("image-rendering")
	}
	if v, _ := r.attrLineCap(aidStrokeLinecap); v != LineCapSquare {
		t.Error("linecap")
	}
	if v, _ := r.attrLineJoin(aidStrokeLinejoin); v != LineJoinMiterClip {
		t.Error("linejoin")
	}
	if v, _ := r.attrFillRule(aidFillRule); v != FillRuleEvenOdd {
		t.Error("fill-rule")
	}
	if v, ok := r.attrViewBox(aidViewBox); !ok || v.H != 20 {
		t.Error("viewBox")
	}
	if v, ok := r.attrAspectRatio(aidPreserveAspectRatio); !ok || v.Align != stAlignXMinYMax || !v.Slice {
		t.Error("aspect ratio")
	}
	if v, ok := r.attrPaintOrder(aidPaintOrder); !ok || v.Order[0] != stPaintOrderStroke {
		t.Error("paint-order")
	}
	if v, ok := r.attrColor(aidColor); !ok || v != stNewColorRGB(255, 0, 0) {
		t.Error("color")
	}
	if v, ok := r.attrAngle(aidOrient); !ok || v.Number != 90 {
		t.Error("angle")
	}
	if v, ok := r.attrEnableBackground(aidEnableBackground); !ok || v.Kind != stEnableBackgroundNew {
		t.Error("enable-background")
	}
	if _, ok := r.attrTransformOrigin(aidTransformOrigin); !ok {
		t.Error("transform-origin")
	}
	if _, ok := r.attrF32(aidHeight); ok {
		t.Error("svgtypes Number rejects trailing data")
	}
}

func TestSvgUse(t *testing.T) {
	doc := svgTestParse(t, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">
		<defs><g id="tpl" class="k"><rect id="inner" width="5"/></g></defs>
		<rect id="dup" width="1"/><rect id="dup" width="2"/>
		<use id="u1" xlink:href="#tpl" x="3"/>
		<use id="u2" href="#dup"/>
		<use id="self" href="#self"/>
		<g id="g1"><use id="parent" href="#g1"/></g>
		<g id="g2"><use id="use2" href="#use1"/></g>
		<use id="use1" href="#g2"/>
		<use id="missing" href="#nope"/>
		<use id="badiri" href="tpl"/>
	</svg>`)
	u1 := svgTestByID(t, doc, "u1")
	if got := svgTestTags(u1); got != "g rect" {
		t.Errorf("u1 children %q", got)
	}
	for d := range u1.Descendants() {
		if d != u1 && d.HasAttribute(aidId) {
			t.Error("ids must be dropped inside use")
		}
	}
	// `use` resolves the first element with an id; ElementByID gives the last.
	u2 := svgTestByID(t, doc, "u2")
	if c, ok := u2.FirstChild(); !ok || svgTestAttrs(c) != "width=1" {
		t.Errorf("u2 child %v", c)
	}
	if v, _ := svgTestByID(t, doc, "dup").attrStr(aidWidth); v != "2" {
		t.Error("links must keep the last id")
	}
	for _, id := range []string{"self", "parent", "use1", "missing", "badiri"} {
		if svgTestByID(t, doc, id).HasChildren() {
			t.Errorf("#%s must have no children", id)
		}
	}
	// use2 instantiates use1, whose copy then skips g2 (its `use2` links back to use1); the
	// top-level use1 is skipped for the same reason.
	if got := svgTestTags(svgTestByID(t, doc, "use2")); got != "use" {
		t.Errorf("use2 %q", got)
	}
}

func TestSvgRecursiveLinks(t *testing.T) {
	doc := svgTestParse(t, `<svg xmlns:xlink="http://www.w3.org/1999/xlink">
		<pattern id="p"><rect id="pr" fill="url(#p)" stroke="url(#p) red"/></pattern>
		<pattern id="p1"><rect id="p1r" fill="url(#p2)"/></pattern>
		<pattern id="p2"><rect id="p2r" fill="url(#p1)"/></pattern>
		<clipPath id="c"><rect id="cr" clip-path="url(#c)"/></clipPath>
		<mask id="m1"><rect id="m1r" mask="url(#m2)"/></mask>
		<mask id="m2"><rect id="m2r" mask="url(#m1)"/></mask>
		<filter id="f"><feImage xlink:href="#fr"/></filter>
		<rect id="fr" filter="blur(1) url(#f)"/>
		<filter id="f2"><feImage xlink:href="#fr2"/></filter>
		<rect id="fr2" filter="url(#other)"/>
	</svg>`)
	for _, c := range []struct {
		id   string
		a    aid
		want string
	}{
		{"pr", aidFill, "none"}, {"pr", aidStroke, "none"},
		{"p1r", aidFill, "url(#p2)"}, {"p2r", aidFill, "none"},
		{"cr", aidClipPath, "none"},
		{"m1r", aidMask, "url(#m2)"}, {"m2r", aidMask, "none"},
		{"fr", aidFilter, "none"}, {"fr2", aidFilter, "url(#other)"},
	} {
		if v, _ := svgTryAttr(svgTestByID(t, doc, c.id), c.a, svgParseStr); v != c.want {
			t.Errorf("#%s %s = %q, want %q", c.id, c.a, v, c.want)
		}
	}
}

func TestSvgHrefIter(t *testing.T) {
	doc := svgTestParse(t, `<svg>
		<linearGradient id="a" href="#b"/><linearGradient id="b" href="#c"/><linearGradient id="c"/>
		<linearGradient id="x" href="#y"/><linearGradient id="y" href="#x"/>
		<linearGradient id="s" href="#s"/>
	</svg>`)
	ids := func(start string) string {
		var out []string
		for n := range svgTestByID(t, doc, start).HrefIter() {
			out = append(out, n.ElementID())
		}
		return strings.Join(out, ",")
	}
	for start, want := range map[string]string{"a": "a,b,c", "x": "x,y", "s": "s", "c": "c"} {
		if got := ids(start); got != want {
			t.Errorf("%s: %q, want %q", start, got, want)
		}
	}
}

func TestSvgText(t *testing.T) {
	doc := svgTestParse(t, `<!DOCTYPE svg [<!ENTITY ent "Ent"><!ENTITY col "red">]>
	<svg xmlns:xlink="http://www.w3.org/1999/xlink" fill="&col;">
		<text id="t1">  Hello   <tspan> world </tspan> !</text>
		<text id="t2" xml:space="preserve">  a	 b  </text>
		<text id="t3">
			&ent;  <!-- c --> <a href="#x">link</a><rect/><tspan><textPath>no</textPath></tspan>
		</text>
		<defs><text id="src">Ref <tspan>text</tspan></text></defs>
		<text id="t4"><tref xlink:href="#src" id="tr"/></text>
		<text id="t5"><textPath id="tp">on path</textPath></text>
		<text id="t6"> </text>
		<g xml:space="preserve"><text id="t7"> x  y </text></g>
		<text id="t8">a <tspan xml:space="preserve"> b </tspan> c</text>
	</svg>`)
	if v, _ := doc.RootElement().attrStr(aidFill); v != "red" {
		t.Error("entity in attribute")
	}
	for _, c := range []struct{ id, want string }{
		{"t1", "'Hello ' tspan 'world' ' !'"},
		{"t2", "'  a  b  '"},
		{"t3", "'Ent' ' ' tspan 'link' tspan ''"},
		{"t4", "tspan 'Ref text'"},
		{"t5", "textPath 'on path'"},
		{"t6", "''"},
		{"t7", "' x  y '"},
		{"t8", "'a ' tspan ' b ' 'c'"},
	} {
		if got := svgTestTags(svgTestByID(t, doc, c.id)); got != c.want {
			t.Errorf("#%s: %q, want %q", c.id, got, c.want)
		}
	}
	if svgTestByID(t, doc, "tr").HasAttribute(aidHref) {
		t.Error("href must be dropped from a tref turned into tspan")
	}
	if n := svgTestByID(t, doc, "t1"); n.Text() != "Hello " {
		t.Errorf("element text %q", n.Text())
	}
}

func TestSvgTrimText(t *testing.T) {
	for _, c := range []struct {
		in    string
		space svgXMLSpace
		want  string
	}{
		{"  a \t\n b  ", svgXMLSpaceDefault, " a b "},
		{"  a \t\n b  ", svgXMLSpacePreserve, "  a    b  "},
		{"\r\n", svgXMLSpaceDefault, " "},
		{"é  ü", svgXMLSpaceDefault, "é ü"},
	} {
		if got := svgTrimText(c.in, c.space); got != c.want {
			t.Errorf("svgTrimText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The recursion check only looks for `use` in the SVG namespace: without xmlns a `use` of its own
// parent group recurses until the depth limit, as in Rust.
func TestSvgUseWithoutNamespace(t *testing.T) {
	_, err := svgTestParseErr(t, `<svg><g id="g1"><use href="#g1"/></g></svg>`)
	var e *Error
	if !errors.As(err, &e) || e.XML.KindName() != "NodesLimitReached" {
		t.Errorf("no namespace: %v", err)
	}
	doc := svgTestParse(t, `<svg><g id="g2"><use id="use2" href="#use1"/></g><use id="use1" href="#g2"/></svg>`)
	if got := svgTestTags(svgTestByID(t, doc, "use1")); got != "g use" {
		t.Errorf("use1 %q", got)
	}
	if got := svgTestTags(svgTestByID(t, doc, "use2")); got != "use g use" {
		t.Errorf("use2 %q", got)
	}
}
