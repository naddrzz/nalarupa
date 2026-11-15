package usvg

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/skia"
)

// testPNG is a 3x2 PNG.
const testPNG = "iVBORw0KGgoAAAANSUhEUgAAAAMAAAACCAIAAAASFvFNAAAAEklEQVR4nGP4z8DAwMDAxMAAAA4LAQGrGGp7AAAAAElFTkSuQmCC"

func parse(t *testing.T, svg string) *Tree {
	t.Helper()
	opt := DefaultOptions()
	tree, err := FromStr(svg, &opt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return tree
}

func svgDoc(body string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="200" height="100">` +
		body + `</svg>`
}

func child[T Node](t *testing.T, g *Group, i int) T {
	t.Helper()
	if i >= len(g.children) {
		t.Fatalf("child %d of %d", i, len(g.children))
	}
	n, ok := g.children[i].(T)
	if !ok {
		t.Fatalf("child %d is %T", i, g.children[i])
	}
	return n
}

func verbs(p *skia.Path) string {
	s := ""
	for _, v := range p.Verbs() {
		s += string("MLQCZ"[v])
	}
	return s
}

func approx(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-4 }

func TestConvertLengthUnits(t *testing.T) {
	tree := parse(t, `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100" font-size="20">
		<g font-size="200%"><rect width="1em" height="1ex"/></g>
		<rect width="1in" height="2.54cm"/>
		<rect width="25.4mm" height="72pt"/>
		<rect width="6pc" height="50%"/>
		<rect width="10%" height="10" rx="10%"/>
	</svg>`)
	want := [][2]float32{{40, 20}, {96, 96}, {96, 96}, {96, 50}, {20, 10}}
	for i, w := range want {
		g := &tree.root
		if i == 0 {
			g, i = child[*Group](t, g, 0), 0
		}
		p := child[*Path](t, g, i)
		b := p.boundingBox
		if !approx(b.Width(), w[0]) || !approx(b.Height(), w[1]) {
			t.Errorf("rect %d: %vx%v, want %v", i, b.Width(), b.Height(), w)
		}
	}

	// Lengths other than x or y take the view box diagonal: sqrt((200^2+100^2)/2) * 10%.
	state := &converterState{opt: &Options{DPI: 96, FontSize: 12}}
	state.viewBox, _ = skia.NonZeroRectFromXYWH(0, 0, 200, 100)
	got := convertLength(stNewLength(10, stLengthUnitPercent), svgNode{}, aidR, unitsUserSpaceOnUse, state)
	if !approx(got, float32(math.Sqrt((200*200+100*100)/2)/10)) {
		t.Errorf("r 10%% = %v", got)
	}
	if got := convertLength(stNewLength(25, stLengthUnitPercent), svgNode{}, aidX, unitsObjectBoundingBox, state); got != 0.25 {
		t.Errorf("objectBoundingBox 25%% = %v", got)
	}
}

func TestNamedFontSize(t *testing.T) {
	if got := convertNamedFontSize("medium", 12); got != 12 {
		t.Errorf("medium = %v", got)
	}
	if got := convertNamedFontSize("bogus", 12); got != 12 {
		t.Errorf("unknown = %v", got)
	}
	// __powisf2: 1.2 * (1.2*1.2) in f32 for 3.
	a := float32(1.2)
	sq := float32(a * a)
	if got := namedFontSizeScale(3); got != float32(a*sq) {
		t.Errorf("scale 3 = %v", got)
	}
	if got := namedFontSizeScale(-2); got != 1/sq {
		t.Errorf("scale -2 = %v", got)
	}
}

func TestRectRadii(t *testing.T) {
	tree := parse(t, svgDoc(`
		<rect width="10" height="10"/>
		<rect width="10" height="10" rx="2"/>
		<rect width="10" height="20" ry="30"/>
		<rect width="10" height="10" rx="-2" ry="3"/>
		<rect width="0" height="10"/>
		<rect width="10" height="10" rx="0" ry="5"/>`))
	if len(tree.root.children) != 5 {
		t.Fatalf("%d children, want 5 (zero width is skipped)", len(tree.root.children))
	}
	if v := verbs(child[*Path](t, &tree.root, 0).data); v != "MLLLZ" {
		t.Errorf("plain rect verbs %s", v)
	}
	if v := verbs(child[*Path](t, &tree.root, 1).data); v != "MLCLCLCLCZ" {
		t.Errorf("rounded rect verbs %s", v)
	}
	// ry only: rx = ry, both clamped to half the side: rx 5, ry 10.
	p := child[*Path](t, &tree.root, 2).data
	if first := p.Points()[0]; first.X != 5 || first.Y != 0 {
		t.Errorf("ry-only rect starts at %v, want (5, 0)", first)
	}
	// A negative rx is dropped, so rx = ry = 3.
	if first := child[*Path](t, &tree.root, 3).data.Points()[0]; first.X != 3 {
		t.Errorf("negative rx rect starts at %v", first)
	}
	// rx 0 gives a plain rect whatever ry is.
	if v := verbs(child[*Path](t, &tree.root, 4).data); v != "MLLLZ" {
		t.Errorf("rx=0 verbs %s", v)
	}
}

func TestShapes(t *testing.T) {
	tree := parse(t, svgDoc(`
		<circle cx="50" cy="50" r="10"/>
		<circle r="0"/>
		<ellipse cx="10" cy="10" rx="5"/>
		<ellipse cx="10" cy="10" rx="5" ry="-1"/>
		<line x2="10" y2="10" stroke="black"/>
		<polyline points="1 1"/>
		<polygon points="0,0 10,0 10,10 5"/>
		<path d="M0 0 L10 0 L10 10 X 20 20"/>`))
	// A negative ry is dropped, so the last ellipse takes ry = rx.
	if len(tree.root.children) != 6 {
		t.Fatalf("%d children, want 6", len(tree.root.children))
	}
	c := child[*Path](t, &tree.root, 0)
	if ltrb(c.boundingBox) != [4]float32{40, 40, 60, 60} || c.data.Points()[0] != skia.Pt(60, 50) {
		t.Errorf("circle bbox %v start %v", ltrb(c.boundingBox), c.data.Points()[0])
	}
	if v := verbs(c.data); v[0] != 'M' || v[len(v)-1] != 'Z' {
		t.Errorf("circle verbs %s", v)
	}
	if b := child[*Path](t, &tree.root, 1).boundingBox; ltrb(b) != [4]float32{5, 5, 15, 15} {
		t.Errorf("ellipse with rx only: %v", ltrb(b))
	}
	if v := verbs(child[*Path](t, &tree.root, 4).data); v != "MLLZ" {
		t.Errorf("polygon verbs %s (the odd coordinate is ignored)", v)
	}
	if v := verbs(child[*Path](t, &tree.root, 5).data); v != "MLL" {
		t.Errorf("path stops at the error: %s", v)
	}
}

func TestPathVisibility(t *testing.T) {
	tree := parse(t, svgDoc(`<rect width="5" height="5" fill="none"/><rect width="5" height="5" visibility="hidden"/>
		<rect width="5" height="5" fill="#ff000080" stroke="bogus"/>`))
	if child[*Path](t, &tree.root, 0).visible || child[*Path](t, &tree.root, 1).visible {
		t.Error("a path without paint, or hidden, must be invisible")
	}
	p := child[*Path](t, &tree.root, 2)
	if p.stroke != nil || p.fill.paint != NewColorRGB(255, 0, 0) || p.fill.opacity != float32(128)/255 {
		t.Errorf("fill %+v stroke %+v", p.fill, p.stroke)
	}
}

func TestUseSymbolViewBox(t *testing.T) {
	tree := parse(t, svgDoc(`<symbol id="s" viewBox="0 0 10 10"><rect width="10" height="10"/></symbol>
		<use id="u" href="#s" x="20" y="30" width="40" height="40"/>`))
	// The symbol is clipped: a clip group carrying the use id, then the use group, then the symbol group.
	g := child[*Group](t, &tree.root, 0)
	if g.id != "u" || g.clipPath == nil || !g.isContextElement {
		t.Fatalf("clip group %q clip %v context %v", g.id, g.clipPath != nil, g.isContextElement)
	}
	if r := g.clipPath.root.children[0].(*Path).boundingBox; ltrb(r) != [4]float32{20, 30, 60, 70} {
		t.Errorf("clip rect %v", ltrb(r))
	}
	inner := child[*Group](t, child[*Group](t, g, 0), 0)
	if inner.transform != skia.TransformFromRow(4, 0, 0, 4, 20, 30) {
		t.Errorf("symbol transform %v", inner.transform)
	}
	p := child[*Path](t, inner, 0)
	if ltrb(p.absBoundingBox) != [4]float32{20, 30, 60, 70} {
		t.Errorf("abs bbox %v", ltrb(p.absBoundingBox))
	}
}

func TestSwitch(t *testing.T) {
	tree := parse(t, svgDoc(`<switch>
		<rect id="a" systemLanguage="fr" width="1" height="1"/>
		<rect id="b" requiredExtensions="x" width="1" height="1"/>
		<rect id="c" systemLanguage="de, en-US" width="1" height="1"/>
		<rect id="d" width="1" height="1"/></switch>`))
	if p := child[*Path](t, &tree.root, 0); p.id != "c" || len(tree.root.children) != 1 {
		t.Errorf("switch picked %q of %d", p.id, len(tree.root.children))
	}
	opt := DefaultOptions()
	opt.Languages = []string{"de"}
	tree, err := FromStr(svgDoc(`<rect systemLanguage="de-AT" width="1" height="1"/>`), &opt)
	if err != nil || len(tree.root.children) != 1 {
		t.Errorf("language prefix match: %v %d", err, len(tree.root.children))
	}
}

func TestMarkers(t *testing.T) {
	tree := parse(t, svgDoc(`
		<marker id="m" orient="auto" markerUnits="userSpaceOnUse" overflow="visible"><rect width="1" height="1"/></marker>
		<marker id="r" orient="auto-start-reverse" markerUnits="userSpaceOnUse"><rect width="1" height="1"/></marker>
		<polyline id="p" points="0,0 10,10 20,0" stroke="black" marker-mid="url(#m)" marker-end="url(#m)"/>
		<polyline points="0,0 10,0" stroke="black" marker-start="url(#r)" paint-order="markers"/>`))
	if len(tree.root.children) != 4 {
		t.Fatalf("%d children", len(tree.root.children))
	}
	if p := child[*Path](t, &tree.root, 0); p.id != "p" {
		t.Errorf("path first, got %q", p.id)
	}
	markers := child[*Group](t, &tree.root, 1)
	if len(markers.children) != 2 {
		t.Fatalf("%d markers, want mid and end", len(markers.children))
	}
	mid := child[*Group](t, markers, 0)
	// The middle vertex bisects 45° and -45° (up to rounding), translated to (10, 10).
	if mid.transform.TX != 10 || mid.transform.TY != 10 || !approx(mid.transform.SX, 1) || mid.clipPath != nil {
		t.Errorf("mid marker %v clip %v", mid.transform, mid.clipPath != nil)
	}
	end := child[*Group](t, markers, 1)
	want := skia.TransformFromTranslate(20, 0).PreRotate(calcLineAngle(10, 10, 20, 0))
	if end.transform != want {
		t.Errorf("end marker %v, want %v", end.transform, want)
	}
	if got := calcLineAngle(10, 10, 20, 0); !approx(got, 315) {
		t.Errorf("end angle %v", got)
	}
	// paint-order markers: the markers come before the path; auto-start-reverse turns the start by 180°.
	start := child[*Group](t, child[*Group](t, &tree.root, 2), 0)
	if start.clipPath == nil || start.transform != skia.TransformFromTranslate(0, 0).PreRotate(180) {
		t.Errorf("reversed start %v", start.transform)
	}
}

func TestGradientUnits(t *testing.T) {
	tree := parse(t, svgDoc(`<linearGradient id="g"><stop offset="0"/><stop offset="1" stop-color="red"/></linearGradient>
		<rect x="10" y="10" width="20" height="40" fill="url(#g)"/>
		<rect x="50" y="10" width="10" height="10" fill="url(#g)"/>`))
	a := child[*Path](t, &tree.root, 0).fill.paint.(*LinearGradient)
	b := child[*Path](t, &tree.root, 1).fill.paint.(*LinearGradient)
	// A shared gradient is copied (with a generated id) until its last user updates it in place.
	if a.id != "linearGradient1" || b.id != "g" {
		t.Errorf("ids %q %q", a.id, b.id)
	}
	if a.units != unitsUserSpaceOnUse || a.transform != skia.TransformFromRow(20, 0, 0, 40, 10, 10) {
		t.Errorf("first %v %v", a.units, a.transform)
	}
	if b.transform != skia.TransformFromRow(10, 0, 0, 10, 50, 10) || b.x2 != 1 {
		t.Errorf("second %v x2 %v", b.transform, b.x2)
	}
	if len(tree.linearGradients) != 2 {
		t.Errorf("%d collected gradients", len(tree.linearGradients))
	}
}

func TestDegenerateGradients(t *testing.T) {
	tree := parse(t, svgDoc(`
		<linearGradient id="one"><stop stop-color="lime" stop-opacity="0.5"/></linearGradient>
		<linearGradient id="none"/>
		<radialGradient id="r0" r="0"><stop stop-color="red"/><stop offset="1" stop-color="blue"/></radialGradient>
		<radialGradient id="neg" fr="-1"><stop stop-color="red"/><stop offset="1" stop-color="blue"/></radialGradient>
		<rect width="5" height="5" fill="url(#one)"/>
		<rect width="5" height="5" fill="url(#none) yellow"/>
		<rect width="5" height="5" fill="url(#r0)"/>
		<rect width="5" height="5" fill="url(#neg)"/>
		<line x2="5" stroke="url(#neg)"/>`))
	if f := child[*Path](t, &tree.root, 0).fill; f.paint != NewColorRGB(0, 255, 0) || f.opacity != 0.5 {
		t.Errorf("one stop: %+v", f)
	}
	if f := child[*Path](t, &tree.root, 1).fill; f.paint != NewColorRGB(255, 255, 0) {
		t.Errorf("no stops falls back: %+v", f)
	}
	if f := child[*Path](t, &tree.root, 2).fill; f.paint != NewColorRGB(0, 0, 255) {
		t.Errorf("r=0 uses the last stop: %+v", f)
	}
	if g := child[*Path](t, &tree.root, 3).fill.paint.(*RadialGradient); g.fr != 0 || g.fx != 0.5 {
		t.Errorf("negative fr %v fx %v", g.fr, g.fx)
	}
	// An objectBoundingBox paint on a shape without a bbox has no paint (and no fallback).
	if p := child[*Path](t, &tree.root, 4); p.stroke != nil {
		t.Errorf("zero-height line stroke %+v", p.stroke)
	}
}

func TestStops(t *testing.T) {
	tree := parse(t, svgDoc(`<linearGradient id="g">
		<stop offset="0"/><stop offset="0"/><stop offset="50%"/><stop offset="0.5"/><stop offset="0.5"/>
		<stop offset="0.5"/><stop offset="0.2"/><stop offset="3"/></linearGradient>
		<rect width="5" height="5" fill="url(#g)"/>`))
	stops := child[*Path](t, &tree.root, 0).fill.paint.(*LinearGradient).stops
	eps := float32(1.0 / (1 << 23))
	half := float32(0.5)
	// 0 0 .5 .5 .5 .5 .2 1: the middle equal stops go, the second zero moves up by an epsilon, and
	// each offset not above the previous one pushes the previous one down.
	want := []float32{0, eps, half - eps, half - eps, half, 1}
	if len(stops) != len(want) {
		t.Fatalf("%d stops", len(stops))
	}
	for i, s := range stops {
		if s.offset != want[i] {
			t.Errorf("stop %d offset %v, want %v", i, s.offset, want[i])
		}
	}
}

func TestClipPath(t *testing.T) {
	tree := parse(t, svgDoc(`
		<clipPath id="u"><rect width="10" height="10"/></clipPath>
		<clipPath id="o" clipPathUnits="objectBoundingBox"><rect width="0.5" height="1"/></clipPath>
		<clipPath id="bad" transform="scale(0 1)"><rect width="10" height="10"/></clipPath>
		<rect x="10" y="10" width="20" height="20" clip-path="url(#u)"/>
		<rect x="40" y="10" width="20" height="20" clip-path="url(#u)"/>
		<rect x="70" y="10" width="20" height="40" clip-path="url(#o)"/>
		<rect x="100" y="10" width="20" height="40" clip-path="url(#o)"/>
		<rect width="20" height="20" clip-path="url(#bad)"/>
		<rect width="20" height="20" clip-path="url(#missing)"/>`))
	if len(tree.root.children) != 5 {
		t.Fatalf("%d children", len(tree.root.children))
	}
	c0 := child[*Group](t, &tree.root, 0).clipPath
	if c0 != child[*Group](t, &tree.root, 1).clipPath {
		t.Error("userSpaceOnUse clip paths must be shared")
	}
	o1, o2 := child[*Group](t, &tree.root, 2).clipPath, child[*Group](t, &tree.root, 3).clipPath
	if o1.id != "o" || o2.id != "clipPath1" || o1.transform != skia.TransformFromRow(20, 0, 0, 40, 70, 10) {
		t.Errorf("objectBoundingBox clips %q %q %v", o1.id, o2.id, o1.transform)
	}
	// An unknown link is ignored, unlike an invalid clip path.
	if p := child[*Path](t, &tree.root, 4); p.id != "" {
		t.Errorf("missing clip %v", p)
	}
	if len(tree.clipPaths) != 3 {
		t.Errorf("%d collected clip paths", len(tree.clipPaths))
	}
}

func TestMask(t *testing.T) {
	tree := parse(t, svgDoc(`<mask id="m"><rect width="100" height="100" fill="white"/></mask>
		<mask id="e"/>
		<rect x="10" y="10" width="20" height="40" mask="url(#m)"/>
		<line x2="10" stroke="black" mask="url(#m)"/>
		<rect width="5" height="5" mask="url(#e)"/>`))
	if len(tree.root.children) != 2 {
		t.Fatalf("%d children (an empty mask drops the element)", len(tree.root.children))
	}
	m := child[*Group](t, &tree.root, 0).mask
	// The default region is -10%/120% of the bbox.
	if !approx(m.rect.Left(), 8) || !approx(m.rect.Top(), 6) || !approx(m.rect.Right(), 32) || !approx(m.rect.Bottom(), 54) {
		t.Errorf("mask rect %v", ltrb(m.rect))
	}
	// A zero-sized bbox with objectBoundingBox units masks everything: an empty mask.
	if m2 := child[*Group](t, &tree.root, 1).mask; m2 == nil || m2.root.HasChildren() || m2.id != "mask1" {
		t.Errorf("mask-all %+v", m2)
	}
}

func TestFilterPlaceholder(t *testing.T) {
	tree := parse(t, svgDoc(`<filter id="f"><feOffset/></filter><filter id="empty"/><filter id="desc"><desc/></filter>
		<rect x="10" y="10" width="20" height="20" filter="url(#f)"/>
		<rect width="5" height="5" filter="url(#missing)"/>
		<rect width="5" height="5" filter="url(#desc)"/>
		<rect x="10" y="10" width="20" height="20" filter="blur(1) url(#missing)"/>
		<rect width="5" height="5" filter="bogus("/>`))
	if len(tree.root.children) != 3 {
		t.Fatalf("%d children", len(tree.root.children))
	}
	f := child[*Group](t, &tree.root, 0).filters
	if len(f) != 1 || f[0].id != "f" || ltrb(f[0].rect) != [4]float32{8, 8, 32, 32} {
		t.Errorf("filter %+v", f)
	}
	g := child[*Group](t, &tree.root, 1)
	if len(g.filters) != 1 || g.filters[0].id != "filter1" || ltrb(g.filters[0].rect) != [4]float32{0, 0, 40, 40} {
		t.Errorf("blur filter %+v", g.filters[0])
	}
	if ltrb(g.layerBoundingBox) != [4]float32{0, 0, 40, 40} {
		t.Errorf("layer bbox follows the filter region: %v", ltrb(g.layerBoundingBox))
	}
	// An unparsable list is skipped and the element is rendered without filters.
	if _, ok := tree.root.children[2].(*Path); !ok {
		t.Errorf("bad filter list gave %T", tree.root.children[2])
	}
}

func TestTextPlaceholder(t *testing.T) {
	tree := parse(t, svgDoc(`<g transform="translate(5 6)"><text id="t">hi</text></g><text/>`))
	txt := child[*Text](t, child[*Group](t, &tree.root, 0), 0)
	if txt.id != "t" || txt.absTransform != skia.TransformFromTranslate(5, 6) {
		t.Errorf("text %q %v", txt.id, txt.absTransform)
	}
	if !tree.HasTextNodes() {
		t.Error("HasTextNodes")
	}
}

func TestGroupFlattening(t *testing.T) {
	tree := parse(t, svgDoc(`<g><g opacity="1"><rect width="1" height="1"/></g></g>
		<svg x="5"><rect width="1" height="1"/></svg>
		<g opacity="0.5" style="isolation:isolate"><rect width="1" height="1"/></g>`))
	// `g` elements always make a group (they may carry an id); a nested svg only with a transform.
	g := child[*Group](t, &tree.root, 0)
	if _, ok := g.children[0].(*Group); !ok {
		t.Error("nested g must stay a group")
	}
	if g := child[*Group](t, &tree.root, 1); g.transform != skia.TransformFromTranslate(5, 0) {
		t.Errorf("nested svg %v", g.transform)
	}
	if g := child[*Group](t, &tree.root, 2); g.opacity != 0.5 || !g.isolate || !g.ShouldIsolate() {
		t.Errorf("isolated group %+v", g)
	}
}

func TestImageKinds(t *testing.T) {
	gif := append([]byte("GIF89a"), 2, 0, 3, 0, 0, 0, 0)
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1}
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 20)...)
	for _, c := range []struct {
		data []byte
		want imageFormat
	}{{gif, imageFormatGIF}, {jpeg, imageFormatJPEG}, {webp, imageFormatWEBP}} {
		if f, ok := getImageDataFormat(c.data); !ok || f != c.want {
			t.Errorf("%q: %v %v", c.data[:4], f, ok)
		}
	}
	if _, ok := getImageDataFormat([]byte("<svg/>")); ok {
		t.Error("svg text detected as raster")
	}
	if f, ok := getImageFileFormat("dir/x.SVGZ", nil); !ok || f != imageFormatSVG {
		t.Error("svgz extension")
	}
	if _, ok := getImageFileFormat("dir/.svg", nil); ok {
		t.Error("a dot file has no extension")
	}

	resolve := DefaultDataResolver()
	opt := DefaultOptions()
	if k, ok := resolve("text/plain", jpeg, &opt); !ok {
		t.Error("text/plain jpeg")
	} else if _, ok := k.(ImageKindJPEG); !ok {
		t.Errorf("text/plain jpeg gave %T", k)
	}
	if k, ok := resolve("text/plain", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="4" height="2"/>`), &opt); !ok {
		t.Error("text/plain svg")
	} else if s, ok := k.(ImageKindSVG); !ok || s.Tree.size.Width() != 4 {
		t.Errorf("text/plain svg gave %T", k)
	}
	if _, ok := resolve("image/bmp", gif, &opt); ok {
		t.Error("unknown mime resolved")
	}
}

func TestImageElement(t *testing.T) {
	tree := parse(t, svgDoc(`<image id="i" x="10" y="10" width="30" href="data:image/png;base64,`+testPNG+`"/>
		<image id="s" x="10" y="10" width="30" height="30" preserveAspectRatio="xMidYMid slice" href="data:image/png;base64,`+testPNG+`"/>
		<image href="data:image/png;base64,!!"/>`))
	if len(tree.root.children) != 2 {
		t.Fatalf("%d children", len(tree.root.children))
	}
	g := child[*Group](t, &tree.root, 0)
	img := child[*Image](t, g, 0)
	// Only the width is set: the height keeps the 3:2 ratio.
	if g.id != "i" || img.id != "" || img.size.Width() != 3 || ltrb(img.AbsBoundingBox()) != [4]float32{10, 10, 40, 30} {
		t.Errorf("image %q %v %v", g.id, img.size, ltrb(img.AbsBoundingBox()))
	}
	s := child[*Group](t, &tree.root, 1)
	if s.id != "s" || s.clipPath == nil || child[*Group](t, s, 0).id != "" {
		t.Errorf("sliced image group %q clip %v", s.id, s.clipPath != nil)
	}
}

func TestDataURL(t *testing.T) {
	for _, c := range []struct {
		in, mime, body string
		ok             bool
	}{
		{"data:image/png;base64,QUJD", "image/png", "ABC", true},
		{"  DATA:Image/SVG+XML ; BASE64 ,QUJD#frag", "image/svg+xml", "ABC", true},
		{"data:,a%20b%zz", "text/plain", "a b%zz", true},
		{"data:;base64,QQ==", "text/plain", "A", true},
		{"data:bogus,x", "text/plain", "x", true},
		{"data:text/plain;charset=utf-8,x\ty", "text/plain", "xy", true},
		{"da\nta:image/gif,x", "image/gif", "x", true},
		{"data:image/png#,x", "", "", false},
		{"data:image/png", "", "", false},
		{"http://x/a.png", "", "", false},
	} {
		u, ok := imageProcessDataURL(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok %v", c.in, ok)
			continue
		}
		if !ok {
			continue
		}
		body, ok := u.decodeToVec()
		if got := u.mimeType + "/" + u.mimeSubtype; got != c.mime || !ok || string(body) != c.body {
			t.Errorf("%q: %q %q %v", c.in, got, body, ok)
		}
	}
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"QUJD", "ABC", true}, {"QUI", "AB", true}, {"QUI=", "AB", true}, {"QQ", "A", true},
		{"Q Q = =", "A", true}, {"QQ=", "", false}, {"Q", "", false}, {"QUJD=", "", false},
		{"QQ==QQ", "", false}, {"Q!==", "", false},
	} {
		got, ok := imageForgivingBase64([]byte(c.in))
		if ok != c.ok || string(got) != c.want {
			t.Errorf("base64 %q: %q %v", c.in, got, ok)
		}
	}
}

func TestNestedSVG(t *testing.T) {
	dir := t.TempDir()
	inner := `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="4"><image width="2" height="2" href="other.png"/><rect width="8" height="4"/></svg>`
	if err := writeFile(filepath.Join(dir, "inner.svg"), inner); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "other.png"), string(mustBase64(t, testPNG))); err != nil {
		t.Fatal(err)
	}
	opt := DefaultOptions()
	opt.ResourcesDir = dir
	tree, err := FromStr(svgDoc(`<image width="16" height="8" href="inner.svg"/><image x="20" href="other.png"/>`), &opt)
	if err != nil {
		t.Fatal(err)
	}
	img := child[*Image](t, child[*Group](t, &tree.root, 0), 0)
	sub, ok := img.kind.(ImageKindSVG)
	if !ok || sub.Tree.size.Width() != 8 {
		t.Fatalf("nested kind %T", img.kind)
	}
	// The nested document ignores images linking to files.
	if n := len(sub.Tree.root.children); n != 1 {
		t.Errorf("nested root has %d children", n)
	}
	if _, ok := child[*Image](t, child[*Group](t, &tree.root, 1), 0).kind.(ImageKindPNG); !ok {
		t.Error("png file")
	}
}

func TestErrors(t *testing.T) {
	opt := DefaultOptions()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(svgDoc(`<rect width="1" height="1"/>`)))
	_ = zw.Close()
	if tree, err := FromData(buf.Bytes(), &opt); err != nil || len(tree.root.children) != 1 {
		t.Errorf("svgz: %v", err)
	}
	cases := []struct {
		data []byte
		kind ErrorKind
	}{
		{[]byte{0x1f, 0x8b, 1, 2, 3}, ErrMalformedGZip},
		{[]byte("<svg \xff/>"), ErrNotAnUtf8Str},
		{[]byte("<svg"), ErrParsingFailed},
		{[]byte(`<html xmlns="http://www.w3.org/2000/svg"/>`), ErrParsingFailed},
		{[]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="0" height="10"/>`), ErrInvalidSize},
	}
	for _, c := range cases {
		_, err := FromData(c.data, &opt)
		var e *Error
		if !errors.As(err, &e) || e.Kind != c.kind {
			t.Errorf("%q: %v, want %v", c.data, err, errorKindNames[c.kind])
		}
	}
	if _, err := DecompressSVGZ([]byte("plain")); err == nil {
		t.Error("DecompressSVGZ of plain data")
	}
}

func TestSizeWithoutViewBox(t *testing.T) {
	tree := parse(t, `<svg xmlns="http://www.w3.org/2000/svg" width="50%"><rect x="10" y="20" width="30" height="40"/></svg>`)
	// The size comes from the content (right and bottom of its bbox).
	if tree.size.Width() != 40 || tree.size.Height() != 60 {
		t.Errorf("size %v", tree.size)
	}
	tree = parse(t, `<svg xmlns="http://www.w3.org/2000/svg" width="50%" viewBox="0 0 30 20"/>`)
	if tree.size.Width() != 15 || tree.size.Height() != 20 {
		t.Errorf("viewBox size %v", tree.size)
	}
}

func TestBackgroundAndRootTransform(t *testing.T) {
	tree := parse(t, `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10" viewBox="0 0 10 5" background-color="#00ff0080"><rect width="1" height="1"/></svg>`)
	g := child[*Group](t, &tree.root, 0)
	if g.transform != skia.TransformFromScale(2, 2) {
		t.Errorf("root transform %v", g.transform)
	}
	bg := child[*Path](t, g, 0)
	if bg.fill.paint != NewColorRGB(0, 255, 0) || bg.fill.opacity != float32(128)/255 || ltrb(bg.boundingBox) != [4]float32{0, 0, 10, 5} {
		t.Errorf("background %+v %v", bg.fill, ltrb(bg.boundingBox))
	}
}

func TestPaintOrder(t *testing.T) {
	for _, c := range []struct {
		in   string
		want PaintOrder
	}{
		{"normal", PaintOrderFillAndStroke}, {"stroke", PaintOrderStrokeAndFill},
		{"markers stroke", PaintOrderStrokeAndFill}, {"markers fill", PaintOrderFillAndStroke},
	} {
		if got := svgPaintOrderToUsvg(stParsePaintOrder(c.in)); got != c.want {
			t.Errorf("%q: %v", c.in, got)
		}
	}
	tree := parse(t, svgDoc(`<marker id="m"><rect width="1" height="1"/></marker>
		<line id="l" x2="10" y2="10" fill="red" stroke="blue" marker-start="url(#m)" paint-order="stroke markers fill"/>`))
	if len(tree.root.children) != 3 {
		t.Fatalf("%d children", len(tree.root.children))
	}
	s, f := child[*Path](t, &tree.root, 0), child[*Path](t, &tree.root, 2)
	if s.fill != nil || s.stroke == nil || f.stroke != nil || f.fill == nil || s.id != "" || f.id != "" {
		t.Error("split paint order paths")
	}
}

func TestContextPaint(t *testing.T) {
	tree := parse(t, svgDoc(`<symbol id="s"><rect width="10" height="10" fill="context-fill" stroke="context-stroke"/></symbol>
		<use href="#s" fill="red" style="overflow:visible"/>
		<rect width="5" height="5" fill="context-fill"/>`))
	g := child[*Group](t, &tree.root, 0)
	if !g.isContextElement {
		t.Error("use group is a context element")
	}
	p := child[*Path](t, child[*Group](t, g, 0), 0)
	if p.fill.paint != NewColorRGB(255, 0, 0) || p.stroke != nil {
		t.Errorf("context fill %+v stroke %+v", p.fill, p.stroke)
	}
	if p := child[*Path](t, &tree.root, 1); p.fill != nil {
		t.Error("context-fill outside a use is no fill")
	}
}

func TestGetAbsPath(t *testing.T) {
	opt := DefaultOptions()
	if got := opt.GetAbsPath("a.png"); got != "a.png" {
		t.Errorf("no dir: %q", got)
	}
	sep := string(filepath.Separator)
	opt.ResourcesDir = "res"
	if got := opt.GetAbsPath("img/a.png"); got != "res"+sep+"img/a.png" {
		t.Errorf("join: %q", got)
	}
	opt.ResourcesDir = "res/"
	if got := opt.GetAbsPath("a.png"); got != "res/a.png" {
		t.Errorf("trailing separator: %q", got)
	}
	abs := "/abs/a.png"
	if runtime.GOOS == "windows" {
		abs = `C:\abs\a.png`
	}
	if got := opt.GetAbsPath(abs); got != abs {
		t.Errorf("absolute: %q", got)
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o644) }

func mustBase64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
