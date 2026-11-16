package html_test

// The CSS-level tests of html.rs, plus the options and a missing node.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/html"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/shader"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

var (
	fontsOnce sync.Once
	fonts     *text.Fonts
)

// page is a document holding nodes at the top level (JSON), exported with its own data folder: dir, or a fresh one.
type page struct {
	t   *testing.T
	ctx *export.Ctx
}

func newPage(t *testing.T, dir string) *page {
	t.Helper()
	fontsOnce.Do(func() {
		fonts = text.New(filepath.Join(os.TempDir(), "nalarupa-go-html-test-fonts"))
		fonts.Online = false
	})
	if dir == "" {
		dir = t.TempDir()
	}
	assets := svg.NewAssets(filepath.Join(dir, "cache"))
	assets.Online = false
	return &page{t, &export.Ctx{Doc: doc.New(), Fonts: fonts, Assets: assets}}
}

func (p *page) insert(node string) string {
	p.t.Helper()
	id, err := p.ctx.Doc.Insert("", jsonv.MustParse(node), -1)
	if err != nil {
		p.t.Fatal(err)
	}
	return id
}

// fragment is the elements of node, without the page around them.
func (p *page) fragment(node string) string {
	p.t.Helper()
	return p.export([]string{p.insert(node)}, `{"includeHtmlScaffold": false}`)
}

func (p *page) export(ids []string, opts string) string {
	p.t.Helper()
	h, err := html.Document(p.ctx, ids, jsonv.MustParse(opts))
	if err != nil {
		p.t.Fatal(err)
	}
	return h
}

// rect is the style of a 160×100 rectangle with extra properties, and its whole fragment.
func rect(t *testing.T, extra string) (string, string) {
	t.Helper()
	h := newPage(t, "").fragment(`{"type": "rectangle", "x": 0, "y": 0, "width": 160, "height": 100, ` + extra + `}`)
	return firstStyle(h), h
}

func firstStyle(h string) string {
	_, s, _ := strings.Cut(h, `style="`)
	s, _, _ = strings.Cut(s, `"`)
	return s
}

// styleOf is the style of the element named name.
func styleOf(t *testing.T, h, name string) string {
	t.Helper()
	_, s, ok := strings.Cut(h, `data-name="`+name+`" style="`)
	if !ok {
		t.Fatalf("no %s in\n%s", name, h)
	}
	s, _, _ = strings.Cut(s, `"`)
	return s
}

// lineOf is the line of the element named name.
func lineOf(t *testing.T, h, name string) string {
	t.Helper()
	for _, l := range strings.Split(h, "\n") {
		if strings.Contains(l, `data-name="`+name+`"`) {
			return l
		}
	}
	t.Fatalf("no %s in\n%s", name, h)
	return ""
}

func has(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("want %q in\n%s", p, s)
		}
	}
}

func hasNot(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(s, p) {
			t.Errorf("want no %q in\n%s", p, s)
		}
	}
}

// before reports that a occurs in s and before b.
func before(t *testing.T, s, a, b string) {
	t.Helper()
	i, j := strings.Index(s, a), strings.Index(s, b)
	if i < 0 || j < 0 || i >= j {
		t.Errorf("want %q before %q in\n%s", a, b, s)
	}
}

func grad(extra string) string {
	return `{"type": "gradient", "rotation": 90, "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]` + extra + `}`
}

func TestPaintsMatchTheCanvas(t *testing.T) {
	// The first fill is painted first, so it is the last CSS layer.
	s, _ := rect(t, `"fill": ["#ff0000", "#0000ff80"]`)
	has(t, s, "background:linear-gradient(rgba(0, 0, 255, 0.502),rgba(0, 0, 255, 0.502)),linear-gradient(#ff0000,#ff0000);")
	// Gradient opacity is folded into the stops.
	s, _ = rect(t, `"fill": `+grad(`, "opacity": 0.4`))
	has(t, s, "linear-gradient(-90deg,rgba(255, 0, 0, 0.400) 0%,rgba(0, 0, 255, 0.400) 100%)")
	// A translucent image and the fills above it are overlays; the fills below stay backgrounds.
	img := `{"type": "image", "url": "a.png", "mode": "stretch", "opacity": 0.5}`
	s, h := rect(t, `"fill": ["#ffff00", `+img+`, "#00ff0080"]`)
	has(t, s, "background:linear-gradient(#ffff00,#ffff00);", "isolation:isolate")
	has(t, h, "opacity:0.5;")
	before(t, h, "url('a.png')", "rgba(0, 255, 0, 0.502)")
	// Inner strokes are not hidden by the overlay.
	_, h = rect(t, `"fill": `+img+`, "stroke": "#000000", "strokeWidth": 4, "strokeAlignment": "inner"`)
	has(t, h, "box-shadow:inset 0 0 0 4px #000000;")
	if strings.Index(h, "url('a.png')") >= strings.LastIndex(h, "box-shadow:inset") {
		t.Error("the inner stroke is under the image overlay:\n" + h)
	}
	// A gradient stroke is a masked ring.
	s, h = rect(t, `"fill": "#ffff00", "stroke": `+grad("")+`, "strokeWidth": 12`)
	has(t, s, "isolation:isolate")
	hasNot(t, s, "border")
	has(t, h, "padding:12px;background:linear-gradient(-90deg,#ff0000 0%,#0000ff 100%);", "mask-composite:exclude")
	// A fill blend mode blends with the fills under it; a translucent image's overlay uses mix-blend-mode.
	s, _ = rect(t, `"fill": ["#ffee00", {"type": "color", "color": "#00ffff", "blendMode": "multiply"}]`)
	has(t, s, "background:linear-gradient(#00ffff,#00ffff),linear-gradient(#ffee00,#ffee00);background-blend-mode:multiply,normal;")
	_, h = rect(t, `"fill": ["#ffee00", `+img+`, {"type": "color", "color": "#00ffff", "blendMode": "screen"}]`)
	has(t, h, "mix-blend-mode:screen;")
	// A bottom fill that blends is an overlay in the page's stacking order (no background, no isolation, no negative z-index),
	// so it blends with the layers beneath the element; fills above it follow as overlays.
	s, h = rect(t, `"fill": [{"type": "color", "color": "#00ffff", "blendMode": "multiply"}, "#ff000080"]`)
	hasNot(t, s, "background", "isolation")
	hasNot(t, h, "z-index")
	has(t, h, "background:linear-gradient(#00ffff,#00ffff);opacity:1;mix-blend-mode:multiply;")
	before(t, h, "#00ffff", "rgba(255, 0, 0, 0.502)")
	// A blending stroke is a masked ring with the blend mode.
	s, h = rect(t, `"fill": "#ffffff", "stroke": {"type": "color", "color": "#00ffff", "blendMode": "screen"}, "strokeWidth": 6`)
	has(t, s, "background:linear-gradient(#ffffff,#ffffff);")
	hasNot(t, s, "border", "isolation")
	has(t, h, "padding:6px;background:linear-gradient(#00ffff,#00ffff);mix-blend-mode:screen;")
	hasNot(t, h, "z-index")
	// Background blur is a backdrop filter with the standard deviation, like layer blur.
	s, _ = rect(t, `"fill": "#ffffff40", "effect": {"type": "background_blur", "radius": 16}`)
	has(t, s, "backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px);")
	s, _ = rect(t, `"fill": "#ffffff40", "opacity": 0.5, "effect": {"type": "background_blur", "radius": 16}`)
	hasNot(t, s, "backdrop-filter")
	// A blending stroke makes the whole stack rings, so a plain stroke above it stays above it.
	s, h = rect(t, `"fill": "#ffffff", "stroke": [{"type": "color", "color": "#00ffff", "blendMode": "screen"}, "#ff0000"], "strokeWidth": 6`)
	hasNot(t, s, "box-shadow")
	if strings.Count(h, "mask-composite:exclude") != 2 {
		t.Error("want two rings:\n" + h)
	}
	before(t, h, "mix-blend-mode:screen", "#ff0000")
	// Every stroke is painted, the topmost first in the box-shadow list.
	s, _ = rect(t, `"fill": "#ffffff", "stroke": ["#ff0000", "#0000ff80"], "strokeWidth": 10, "strokeAlignment": "inner"`)
	has(t, s, "box-shadow:inset 0 0 0 10px rgba(0, 0, 255, 0.502),inset 0 0 0 10px #ff0000;")
	// Per-side widths of a stroke stack are masked rings, one per stroke.
	s, h = rect(t, `"fill": "#ffffff", "stroke": ["#ff0000", "#0000ff80"], "strokeWidth": {"top": 4, "left": 2}`)
	hasNot(t, s, "border")
	if strings.Count(h, "mask-composite:exclude") != 2 {
		t.Error("want two rings:\n" + h)
	}
	before(t, h, "#ff0000", "rgba(0, 0, 255")
	// Per-side rings follow the stroke alignment: inner stays inside, outer reaches out by each side's width, centered by half.
	for _, c := range [][2]string{{"inner", "0px 0px 0px 0px"}, {"outer", "-4px 0px 0px -2px"}, {"center", "-2px 0px 0px -1px"}} {
		_, h := rect(t, `"fill": "#ffffff", "stroke": ["#ff0000", "#0000ff80"], "strokeWidth": {"top": 4, "left": 2}, "strokeAlignment": "`+c[0]+`"`)
		has(t, h, "inset:"+c[1]+";padding:4px 0px 0px 2px;")
	}
	// A lone per-side stroke is a CSS border only when inner; centered (the default) and outer rings reach out of the box.
	w := `{"top": 4, "left": 2}`
	s, _ = rect(t, `"fill": "#ffffff", "stroke": "#000000", "strokeWidth": `+w+`, "strokeAlignment": "inner"`)
	has(t, s, "border-top:4px solid #000000;", "border-left:2px solid #000000;")
	for _, c := range [][2]string{{`null`, "-2px 0px 0px -1px"}, {`"outer"`, "-4px 0px 0px -2px"}} {
		s, h := rect(t, `"fill": "#ffffff", "stroke": "#000000", "strokeWidth": `+w+`, "strokeAlignment": `+c[0])
		hasNot(t, s, "border-")
		has(t, h, "inset:"+c[1]+";padding:4px 0px 0px 2px;")
	}
	// Layer blur: CSS takes the standard deviation, half the radius.
	s, _ = rect(t, `"fill": "#ff0000", "effect": {"type": "blur", "radius": 4}`)
	has(t, s, "filter:blur(2px);")
	// Angular gradients are conic, rotated like the canvas.
	s, _ = rect(t, `"fill": `+grad(`, "gradientType": "angular"`))
	has(t, s, "conic-gradient(from -90deg at 50% 50%,#ff0000 0%,#0000ff 100%)")
	// Radial gradients carry center and size; stops are sorted like the canvas.
	s, _ = rect(t, `"fill": {"type": "gradient", "gradientType": "radial", "rotation": 0, "center": {"x": 0.3, "y": 0.4}, "size": {"width": 0.5, "height": 1.0}, "colors": [{"color": "#0000ff", "position": 1.5}, {"color": "#ff0000", "position": 0}]}`)
	has(t, s, "radial-gradient(25% 50% at 30% 40%,#ff0000 0%,#0000ff 100%)")
	// A shortened or shifted linear gradient and a rotated ellipse are SVG backgrounds with the canvas geometry.
	s, _ = rect(t, `"fill": `+grad(`, "size": {"height": 0.5}`))
	has(t, s, "data:image/svg+xml,%3Csvg", "x1=%220.75%22 y1=%220.5%22 x2=%220.25%22 y2=%220.5%22")
	s, _ = rect(t, `"fill": `+grad(`, "center": {"x": 0.25, "y": 0.5}`))
	has(t, s, "x1=%220.75%22 y1=%220.5%22 x2=%22-0.25%22 y2=%220.5%22")
	s, _ = rect(t, `"fill": {"type": "gradient", "gradientType": "radial", "rotation": 45, "size": {"width": 0.3, "height": 0.8}, "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]}`)
	has(t, s, "radialGradient", "matrix(0.106066 -0.106066 0.282843 0.282843 0.5 0.5)")
	// A linear gradient rotated off a quarter turn is an SVG background too (CSS measures its line in pixels); quarter turns stay CSS.
	s, _ = rect(t, `"fill": {"type": "gradient", "rotation": 60, "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]}`)
	has(t, s, "data:image/svg+xml,%3Csvg", "linearGradient")
	hasNot(t, s, "linear-gradient(-60deg")
	s, _ = rect(t, `"fill": {"type": "gradient", "rotation": 180, "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]}`)
	has(t, s, "linear-gradient(-180deg,#ff0000 0%,#0000ff 100%)")
	// An unparseable stop colour stays as transparent black, like the canvas.
	s, _ = rect(t, `"fill": {"type": "gradient", "rotation": 90, "colors": [{"color": "#ff0000", "position": 0}, {"color": "transparent", "position": 1}]}`)
	has(t, s, "linear-gradient(-90deg,#ff0000 0%,rgba(0, 0, 0, 0) 100%)")
}

// The inner stroke of a clipping frame is drawn above its children, so it is an overlay after them, not a box-shadow under them.
func TestClippingFrameStrokesAboveChildren(t *testing.T) {
	p := newPage(t, "")
	kid := `{"type": "rectangle", "name": "kid", "x": 0, "y": 0, "width": 100, "height": 100, "fill": "#ff0000"}`
	frame := func(clip, align string) string {
		return p.fragment(`{"type": "frame", "layout": "none", "clip": ` + clip + `, "width": 100, "height": 100, "fill": "#eeeeee", "stroke": "#0000ff", "strokeWidth": 10, "strokeAlignment": "` + align + `", "children": [` + kid + `]}`)
	}
	h := frame("true", "inner")
	before(t, h, "#ff0000", `<div style="position:absolute;inset:0;border-radius:inherit;pointer-events:none;box-shadow:inset 0 0 0 10px #0000ff;"></div>`)
	if strings.Count(h, "box-shadow") != 1 {
		t.Error("want one box-shadow:\n" + h)
	}
	// The outer half of a centered stroke stays on the frame, which overflow does not clip.
	h = frame("true", "center")
	has(t, h, "box-shadow:0 0 0 5px #0000ff;")
	before(t, h, "#ff0000", "box-shadow:inset 0 0 0 5px #0000ff;")
	// Without clip the children sit inside the stroke as before.
	h = frame("false", "inner")
	has(t, h, "box-shadow:inset 0 0 0 10px #0000ff;")
	hasNot(t, h, "pointer-events")
	// A per-side stroke that cannot be a border is a masked ring: its inner part goes after the children too (a lone one is a border, which no child covers).
	ring := func(align string) string {
		return p.fragment(`{"type": "frame", "layout": "none", "clip": true, "width": 100, "height": 100, "stroke": ["#0000ff", "#00ff00"], "strokeWidth": {"top": 10, "right": 10, "bottom": 10, "left": 10}, "strokeAlignment": "` + align + `", "children": [` + kid + `]}`)
	}
	h = ring("inner")
	before(t, h, "#ff0000", "padding:10px;")
	hasNot(t, h, "z-index:-1")
	h = ring("center")
	before(t, h, "#ff0000", "inset:0px;padding:5px;")
	has(t, h, "inset:-5px;padding:5px;")
	if strings.Count(h, "z-index:-1") != 2 {
		t.Error("want two rings under the children:\n" + h)
	}
}

// A centered stroke is one ring half outside the box: an inner and an outer shadow meeting on the outline leave a
// hairline of the fill through its middle wherever the outline is anti-aliased.
func TestCenteredStrokeIsASingleRing(t *testing.T) {
	p := newPage(t, "")
	node := func(extra string) string {
		return p.fragment(`{"type": "rectangle", "width": 120, "height": 80, "fill": "#f0c000", "stroke": "#000000", "strokeWidth": 6` + extra + `}`)
	}
	ring := "box-shadow:inset 0 0 0 6px #000000;"
	h := node(`, "type": "ellipse"`)
	has(t, h, "inset:-3px;border-radius:50%;"+ring)
	hasNot(t, h, "box-shadow:inset 0 0 0 3px")
	has(t, node(`, "cornerRadius": 30`), "border-radius:33px 33px 33px 33px;"+ring)
	h = node(`, "rotation": -30`)
	has(t, h, "border-radius:0px 0px 0px 0px;"+ring)
	hasNot(t, h, "0 0 0 3px")
	// Inner and outer strokes are one edge already.
	has(t, node(`, "strokeAlignment": "outer"`), "box-shadow:0 0 0 6px #000000;")
}

// The canvas casts a drop shadow from the outline with the stroke's outer part, so the shadow spreads by that reach;
// inner shadows and inner strokes do not.
func TestDropShadowIsCastFromTheStrokedOutline(t *testing.T) {
	p := newPage(t, "")
	node := func(align, width, shadow string) string {
		return p.fragment(`{"type": "rectangle", "width": 100, "height": 50, "fill": "#ffffff", "stroke": "#000000", "strokeWidth": ` + width + `, "strokeAlignment": "` + align + `", "effect": ` + shadow + `}`)
	}
	drop := `{"type": "shadow", "offset": {"x": 12, "y": 12}, "color": "#ff0000"}`
	has(t, node("outer", "20", drop), "box-shadow:0 0 0 20px #000000,12px 12px 0px 20px #ff0000;")
	has(t, node("center", "20", drop), "box-shadow:12px 12px 0px 10px #ff0000;", "inset:-10px;border-radius:0px 0px 0px 0px;box-shadow:inset 0 0 0 20px #000000;")
	// The widest side reaches furthest.
	has(t, node("outer", `{"top": 2, "right": 8, "bottom": 4, "left": 6}`, drop), "12px 12px 0px 8px #ff0000")
	has(t, node("inner", "20", drop), "box-shadow:inset 0 0 0 20px #000000,12px 12px 0px #ff0000;")
	h := node("outer", "20", `{"type": "shadow", "shadowType": "inner", "offset": {"x": 12, "y": 12}, "color": "#ff0000"}`)
	has(t, h, "inset 12px 12px 0px #ff0000")
	hasNot(t, h, "20px #ff0000")
	// A gradient stroke without stops paints nothing, so it adds no reach.
	h = p.fragment(`{"type": "rectangle", "width": 100, "height": 50, "fill": "#ffffff", "stroke": {"type": "gradient", "colors": []}, "strokeWidth": 20, "strokeAlignment": "outer", "effect": ` + drop + `}`)
	has(t, h, "12px 12px 0px #ff0000")
	hasNot(t, h, "20px #ff0000")
}

// A frame whose fill blends with the backdrop paints its overlay in tree order: flow children must be positioned to stay above it.
func TestChildrenStayAboveABlendingFill(t *testing.T) {
	p := newPage(t, "")
	frame := func(fill string) string {
		return p.fragment(`{"type": "frame", "layout": "horizontal", "width": 100, "height": 60, "fill": ` + fill + `, "children": [{"type": "rectangle", "name": "kid", "width": 20, "height": 20, "fill": "#ff0000"}]}`)
	}
	h := frame(`{"type": "color", "color": "#00ffff", "blendMode": "multiply"}`)
	has(t, lineOf(t, h, "kid"), "position:relative;")
	has(t, h, "mix-blend-mode:multiply;")
	hasNot(t, lineOf(t, frame(`"#00ffff"`), "kid"), "position:relative;")
}

// Text and groups cast the shadow of their glyphs and children; gradient text keeps its fill.
func TestTextAndGroupShadowsFollowTheirShapes(t *testing.T) {
	sh := `{"type": "shadow", "offset": {"x": 6, "y": 6}, "blur": 4, "color": "#ff0000"}`
	g := `{"type": "gradient", "gradientType": "linear", "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]}`
	h := newPage(t, "").fragment(`{"type": "frame", "layout": "none", "width": 300, "height": 200, "children": [
		{"type": "text", "name": "t", "content": "Hi", "fontSize": 40, "fill": "#0000ff", "effect": ` + sh + `},
		{"type": "text", "name": "g", "content": "Grad", "fontSize": 40, "fill": ` + g + `, "effect": ` + sh + `},
		{"type": "text", "name": "g2", "content": "Grad", "fontSize": 40, "fill": ` + g + `, "underline": true, "effect": [` + sh + `, ` + sh + `]},
		{"type": "group", "name": "grp", "effect": [` + sh + `, {"type": "blur", "radius": 4}], "children": [{"type": "ellipse", "width": 60, "height": 60, "fill": "#00aa00"}]}]}`)
	ts, gs, g2, grp := styleOf(t, h, "t"), styleOf(t, h, "g"), styleOf(t, h, "g2"), styleOf(t, h, "grp")
	has(t, ts, "text-shadow:6px 6px 4px #ff0000;", "color:#0000ff;")
	hasNot(t, ts, "box-shadow")
	// Gradient text is clipped to its glyphs, so its shadow is a filter that follows them.
	has(t, gs, "background:linear-gradient(-0deg,#ff0000 0%,#0000ff 100%);-webkit-background-clip:text;background-clip:text;color:transparent;", "filter:drop-shadow(6px 6px 2px #ff0000);")
	hasNot(t, gs, "text-shadow", "box-shadow", "text-decoration")
	// Several shadows all start from the element's alpha, so they are one SVG filter, not a chain of `drop-shadow()`s.
	has(t, g2, "filter:url('data:image/svg+xml,%3Csvg", "%3CfeMergeNode in=%22SourceGraphic%22/%3E")
	hasNot(t, g2, "drop-shadow")
	// The filter region grows with the offset and blur (6 + 1.5 * 4, rounded up, plus one) instead of a share of the element.
	has(t, g2, "filterUnits=%22userSpaceOnUse%22 x=%22-13%22 y=%22-13%22")
	// The decoration needs a colour of its own: the text colour is transparent.
	has(t, g2, "text-decoration:underline;", "text-decoration-color:#000000;")
	has(t, grp, "filter:drop-shadow(6px 6px 2px #ff0000) blur(2px);")
	hasNot(t, grp, "box-shadow")
}

// Like the canvas, a shape without a visible fill casts no shadow; its stroke stays, and a frame keeps its shadow.
func TestShapesWithoutAVisibleFillCastNoShadow(t *testing.T) {
	sh := `{"type": "shadow", "offset": {"x": 20, "y": 20}, "blur": 0, "color": "#0000ff"}`
	node := func(name, ty, extra string) string {
		return `{"type": "` + ty + `", "name": "` + name + `", "width": 100, "height": 100, "effect": ` + sh + extra + `}`
	}
	h := newPage(t, "").fragment(`{"type": "frame", "layout": "none", "width": 300, "height": 200, "children": [` + strings.Join([]string{
		node("none", "rectangle", ""),
		node("stroke", "rectangle", `, "stroke": "#ff0000", "strokeWidth": 4`),
		node("clear", "ellipse", `, "fill": "#ff000000"`),
		node("mesh", "rectangle", `, "fill": {"type": "mesh_gradient"}`),
		`{"type": "rectangle", "name": "inner", "width": 100, "height": 100, "effect": {"type": "shadow", "shadowType": "inner", "offset": {"x": 5, "y": 5}, "color": "#0000ff"}}`,
		node("filled", "rectangle", `, "fill": "#00ff00"`),
		node("box", "frame", `, "layout": "none"`),
	}, ",") + `]}`)
	for _, name := range []string{"none", "clear", "mesh", "inner"} {
		hasNot(t, styleOf(t, h, name), "box-shadow")
	}
	hasNot(t, styleOf(t, h, "stroke"), "box-shadow", "#0000ff")
	has(t, h, "inset:-2px;border-radius:0px 0px 0px 0px;box-shadow:inset 0 0 0 4px #ff0000;")
	has(t, styleOf(t, h, "filled"), "box-shadow:20px 20px 0px #0000ff;")
	has(t, styleOf(t, h, "box"), "box-shadow:20px 20px 0px #0000ff;")
}

// The canvas paints the later effect over the earlier one, while the first CSS shadow is the topmost: the lists are reversed.
func TestShadowsStackLikeTheCanvas(t *testing.T) {
	fx := `[{"type": "shadow", "offset": {"x": 10, "y": 10}, "blur": 0, "color": "#ff0000"}, {"type": "shadow", "offset": {"x": 20, "y": 20}, "blur": 0, "color": "#0000ff"}]`
	h := newPage(t, "").fragment(`{"type": "frame", "layout": "none", "width": 300, "height": 200, "children": [
		{"type": "rectangle", "name": "r", "width": 100, "height": 80, "fill": "#ffffff", "effect": ` + fx + `},
		{"type": "text", "name": "t", "content": "Hi", "fontSize": 40, "fill": "#000000", "effect": ` + fx + `},
		{"type": "text", "name": "g", "content": "Hi", "fontSize": 40, "fill": {"type": "gradient", "gradientType": "linear", "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]}, "effect": ` + fx + `}]}`)
	has(t, styleOf(t, h, "r"), "box-shadow:20px 20px 0px #0000ff,10px 10px 0px #ff0000;")
	has(t, styleOf(t, h, "t"), "text-shadow:20px 20px 0px #0000ff,10px 10px 0px #ff0000;")
	// In an feMerge the first node is the bottom one: the earlier effect goes first.
	before(t, styleOf(t, h, "g"), "in=%22s0%22", "in=%22s1%22")
}

// Fixed-size children keep their main-axis size in an overflowing auto layout, as on the canvas.
func TestFixedSizeChildrenDoNotShrink(t *testing.T) {
	p := newPage(t, "")
	row := func(layout string) string {
		kid := func(ty string) string {
			return `{"type": "` + ty + `", "width": 60, "height": 20, "fill": "#ff0000"}`
		}
		return p.fragment(`{"type": "frame", "width": 100, "height": 60, "layout": "` + layout + `", "children": [` + kid("rectangle") + `, ` + kid("ellipse") +
			`, {"type": "rectangle", "width": "fill_container", "height": 20, "fill": "#0000ff"}]}`)
	}
	count := func(s, sub string, want int) {
		t.Helper()
		if got := strings.Count(s, sub); got != want {
			t.Errorf("%q: %d, want %d in\n%s", sub, got, want, s)
		}
	}
	h := row("horizontal")
	count(h, "width:60px;flex-shrink:0;", 2)
	has(t, h, "flex:1 1 0;")
	hasNot(t, h, "height:20px;flex-shrink:0;")
	v := row("vertical")
	count(v, "height:20px;flex-shrink:0;", 3)
	hasNot(t, v, "width:60px;flex-shrink:0;")
	count(row("diagonal"), "width:60px;flex-shrink:0;", 2)
	// Groups take their size from their children and must not shrink either.
	g := p.fragment(`{"type": "frame", "width": 100, "height": 60, "layout": "horizontal", "children": [{"type": "group", "children": [{"type": "rectangle", "width": 80, "height": 20, "fill": "#ff0000"}]}]}`)
	has(t, g, "width:80px;flex-shrink:0;")
}

// `fill_container(N)` in a parent without a layout is N px, like the canvas; plain `fill_container` stays 100%.
func TestFillContainerFallbackInUnlaidParent(t *testing.T) {
	h := newPage(t, "").fragment(`{"type": "frame", "width": 300, "height": 200, "layout": "none", "children": [
		{"type": "rectangle", "name": "A", "width": "fill_container(60)", "height": "fill_container(40)", "fill": "#ff0000"},
		{"type": "rectangle", "name": "B", "width": "fill_container", "height": "fill_container", "fill": "#00ff00"}]}`)
	a := styleOf(t, h, "A")
	has(t, a, "width:60px;", "height:40px;")
	hasNot(t, a, "100%")
	has(t, styleOf(t, h, "B"), "width:100%;", "height:100%;")
}

// `fit_content(N)` on a node with nothing to hug is N, like the canvas; only a layout frame with children hugs.
func TestFitContentFallbackIsExportedAsPixels(t *testing.T) {
	h := newPage(t, "").fragment(`{"type": "frame", "name": "root", "layout": "none", "width": 300, "height": 200, "children": [
		{"type": "frame", "name": "row", "layout": "horizontal", "width": "fit_content", "height": "fit_content", "gap": 10, "padding": 10, "x": 10, "y": 10, "children": [
			{"type": "frame", "name": "A", "layout": "none", "width": "fit_content(40)", "height": "fit_content(40)", "fill": "#00aa00"},
			{"type": "rectangle", "name": "B", "width": "fit_content(60)", "height": "fit_content(30)", "fill": "#ff0000"}]},
		{"type": "frame", "name": "E", "layout": "vertical", "x": 0, "y": 100, "width": "fit_content(25)", "height": "fit_content", "children": [
			{"type": "rectangle", "width": "fill_container", "height": 10}]}]}`)
	a, b, row, empty := lineOf(t, h, "A"), lineOf(t, h, "B"), lineOf(t, h, "row"), lineOf(t, h, "E")
	has(t, a, "width:40px;", "height:40px;")
	hasNot(t, a, "fit-content")
	has(t, b, "width:60px;", "height:30px;")
	hasNot(t, b, "fit-content")
	// The row hugs its children.
	has(t, row, "width:fit-content;", "height:fit-content;")
	// Its only child fills the width, so nothing hugs there and the fallback is the size; the height still hugs.
	has(t, empty, "width:25px;", "height:fit-content;")
}

// A fit_content parent with a fill child gets the laid-out size: CSS would size it from the fill child's content.
func TestHuggingParentOfAFillChildIsExportedAsPixels(t *testing.T) {
	h := newPage(t, "").fragment(`{"type": "frame", "name": "Card", "layout": "vertical", "padding": 16, "gap": 8, "children": [
		{"type": "rectangle", "name": "fill", "width": "fill_container", "height": 20, "fill": "#00ff00"},
		{"type": "rectangle", "name": "btn", "width": 120, "height": 40, "fill": "#0000ff"}]}`)
	card := lineOf(t, h, "Card")
	// The width has a fill child (152 = 120 + 2 * 16); the height has none and stays the browser's to hug.
	has(t, card, "width:152px;")
	hasNot(t, card, "fit-content", "height:")
}

// A rotated or flipped child of an auto layout takes its bounding box in the flow, like the canvas.
func TestTiltedChildrenReserveTheirBoundingBox(t *testing.T) {
	p := newPage(t, "")
	r := func(extra string) string {
		return `{"type": "rectangle", "width": 60, "height": 60, "fill": "#ff0000"` + extra + `}`
	}
	h := p.fragment(`{"type": "frame", "layout": "horizontal", "width": 300, "height": 120, "padding": 10, "gap": 10, "children": [` + r(`, "rotation": 90`) + `, ` + r(`, "flipX": true`) + `, ` + r("") + `]}`)
	// 90deg CCW about the origin: the box spans (0..60, -60..0), so it sits 60 down in its wrapper.
	has(t, h, "<div style=\"position:relative;flex-shrink:0;width:60px;height:60px;\">\n<div data-name=\"rectangle\" style=\"position:absolute;left:0px;top:60px;width:60px;height:60px;",
		"transform:rotate(-90deg);transform-origin:0px 0px;")
	// flipX about the origin spans (-60..0): the origin is the wrapper's right edge.
	has(t, h, "left:60px;top:0px;width:60px;height:60px;", "transform:scale(-1,1);")
	if strings.Count(h, "position:relative;flex-shrink:0;") != 2 {
		t.Error("only the tilted children are wrapped:\n" + h)
	}
	// 45deg: the wrapper is the 84.85 wide bounding box.
	h = p.fragment(`{"type": "frame", "layout": "horizontal", "width": 300, "height": 120, "children": [` + r(`, "rotation": 45`) + `]}`)
	has(t, h, "width:84.852814px;height:84.852814px;")
}

// A rotated or flipped top-level layer sits in a bounding-box wrapper, so none of its paint lands above the page.
func TestTiltedTopLevelLayersAreWrappedInTheirBoundingBox(t *testing.T) {
	p := newPage(t, "")
	r := func(extra string) string {
		return p.fragment(`{"type": "rectangle", "name": "r", "x": 100, "y": 100, "width": 160, "height": 120, "fill": "#3366cc"` + extra + `}`)
	}
	starts := func(h, prefix string) {
		t.Helper()
		if !strings.HasPrefix(h, prefix) {
			t.Errorf("want it to start with %q:\n%s", prefix, h)
		}
	}
	// flipY about the origin spans (0..160, -120..0): the origin is the wrapper's bottom-left.
	h := r(`, "flipY": true`)
	starts(h, "<div style=\"position:relative;flex-shrink:0;width:160px;height:120px;\">\n<div data-name=\"r\" style=\"position:absolute;left:0px;top:120px;")
	has(t, h, "transform:scale(1,-1);")
	// 90deg CCW: the box spans (0..120, -160..0).
	starts(r(`, "rotation": 90`), "<div style=\"position:relative;flex-shrink:0;width:120px;height:160px;\">\n<div data-name=\"r\" style=\"position:absolute;left:0px;top:160px;")
	// An untilted layer stays unwrapped.
	starts(r(""), "<div data-name=\"r\" style=\"position:relative;")
}

func TestOpacityIsAppliedOnce(t *testing.T) {
	p := newPage(t, "")
	for _, node := range []string{
		`"type": "polygon", "polygonCount": 5, "fill": "#ff0000"`,
		`"type": "path", "geometry": "M0 0 L10 0 L10 10 Z", "fill": "#ff0000"`,
		`"type": "line", "stroke": "#ff0000", "strokeWidth": 3`,
		`"type": "ellipse", "innerRadius": 0.5, "fill": "#ff0000"`,
	} {
		h := p.fragment(`{` + node + `, "x": 0, "y": 0, "width": 50, "height": 50, "opacity": 0.5}`)
		// `fill-opacity` is paint alpha; only the node's own opacity must not repeat.
		if strings.Count(h, "opacity:0.5;")+strings.Count(h, " opacity=") != 1 || !strings.Contains(h, "opacity:0.5;") {
			t.Errorf("%s: %s", node, h)
		}
	}
}

func iconDir(t *testing.T, name, markup string) string {
	t.Helper()
	dir := t.TempDir()
	icons := filepath.Join(dir, "cache", "icons")
	if err := os.MkdirAll(icons, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(icons, name), []byte(markup), 0o666); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestIconWithAGradientFillIsDrawn(t *testing.T) {
	p := newPage(t, iconDir(t, "lucide_0.563-star.svg", `<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M12 2 15 9 22 9 17 14 19 22 12 18 5 22 7 14 2 9 9 9Z"/></svg>`))
	g := func(a, b string) string {
		return `{"type": "gradient", "colors": [{"color": "` + a + `", "position": 0}, {"color": "` + b + `", "position": 1}]}`
	}
	icon := func(fill string) string {
		return `{"type": "icon", "icon": "star", "library": "lucide", "x": 0, "y": 0, "width": 80, "height": 80, "fill": ` + fill + `}`
	}
	h := p.fragment(`{"type": "frame", "x": 0, "y": 0, "width": 200, "height": 100, "layout": "none", "children": [` + icon(`"#ff0000"`) + `, ` + icon(g("#ff0000", "#0000ff")) + `, ` + icon(g("#00ff00", "#ffff00")) + `]}`)
	// The solid icon stays an inline svg tinted by `color`; each gradient icon is painted with its own gradient.
	has(t, h, "color:#ff0000", `stop-color="#0000ff"`, `stop-color="#ffff00"`)
	if strings.Count(h, "<path") != 3 {
		t.Error("want three paths:\n" + h)
	}
	// Ids are unique across the page, or the second icon would take the first one's gradient.
	ids := regexp.MustCompile(` id="([^"]*)"`).FindAllStringSubmatch(h, -1)
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id[1]] {
			t.Errorf("id %s repeats:\n%s", id[1], h)
		}
		seen[id[1]] = true
	}
	if len(ids) < 2 {
		t.Error("want ids:\n" + h)
	}
}

func TestIconWithoutItsOwnFillIsDrawnInTheFillColour(t *testing.T) {
	p := newPage(t, iconDir(t, "material_outlined_200-home.svg", `<svg xmlns="http://www.w3.org/2000/svg" height="48" viewBox="0 -960 960 960" width="48"><path d="M230-190h161v-250h178v250h161v-380L480-750 230-570Z"/></svg>`))
	h := p.fragment(`{"type": "icon", "icon": "home", "library": "Material Symbols Outlined", "x": 0, "y": 0, "width": 80, "height": 80, "fill": "#cc0000"}`)
	// A bare `<path>` paints black unless the svg itself hands it `currentColor`.
	has(t, h, `color:#cc0000" fill="currentColor"`)
}

// Mesh gradients, shaders and image fills on text reach the HTML as backgrounds.
func TestMeshShaderAndTextImageFillsAreExported(t *testing.T) {
	mesh := `"columns": 2, "rows": 2, "colors": ["#ff0000", "#00ff00", "#0000ff", "#ffff00"], "points": [[0, 0], [1, 0], [0, 1], [1, 1]]`
	s, _ := rect(t, `"fill": {"type": "mesh_gradient", `+mesh+`}`)
	has(t, s, "background:url('data:image/png;base64,", ") 0 0/100% 100% no-repeat;")
	// A translucent mesh is an overlay with the paint's opacity, like a translucent image.
	s, h := rect(t, `"fill": ["#ffffff", {"type": "mesh_gradient", "opacity": 0.5, `+mesh+`}]`)
	has(t, s, "background:linear-gradient(#ffffff,#ffffff);")
	has(t, h, "data:image/png;base64,", "opacity:0.5;")
	// A shader that is missing leaves no background (and does not break the export).
	s, _ = rect(t, `"fill": {"type": "shader", "url": "nope.glsl"}`)
	hasNot(t, s, "background")
	// A shader that runs is a PNG background too (needs a GPU adapter).
	if _, err := shader.Adapter(); err == nil {
		dir := t.TempDir()
		frag := filepath.Join(dir, "red.glsl")
		if err := os.WriteFile(frag, []byte("void main() { gl_FragColor = vec4(1.0, 0.0, 0.0, 1.0); }"), 0o666); err != nil {
			t.Fatal(err)
		}
		s, _ = rect(t, `"fill": {"type": "shader", "url": "`+filepath.ToSlash(frag)+`"}`)
		has(t, s, "background:url('data:image/png;base64,")
	}
	// An image fill on text is the background clipped to the glyphs, as a gradient is.
	p := newPage(t, "")
	has(t, p.fragment(`{"type": "text", "content": "IMG", "fontSize": 60, "fill": {"type": "image", "url": "pic.png", "mode": "fill"}}`),
		"background:url('pic.png') center/cover no-repeat;-webkit-background-clip:text;background-clip:text;color:transparent;")
	// The image paint's opacity multiplies the node's, as the element is all a background can fade.
	has(t, p.fragment(`{"type": "text", "content": "IMG", "fontSize": 60, "opacity": 0.5, "fill": {"type": "image", "url": "pic.png", "mode": "fill", "opacity": 0.5}}`),
		"opacity:0.5;", "color:transparent;opacity:0.25;")
}

// A font family or image url cannot break out of the `<link href>` or a quoted CSS string.
func TestFontAndURLAreEscaped(t *testing.T) {
	p := newPage(t, "")
	id := p.insert(`{"type": "frame", "width": 300, "height": 200, "layout": "none", "children": [
		{"type": "text", "content": "hi", "fontSize": 12, "fontFamily": "X\"><script>alert(1)</script><x y=\""},
		{"type": "text", "content": "hi", "fontSize": 12, "fontFamily": "A'B\\C"},
		{"type": "text", "content": "hi", "fontSize": 12, "fontFamily": "Noto Sans"},
		{"type": "text", "content": "hi", "fontSize": 12, "fontFamily": "L\nM\rN"},
		{"type": "rectangle", "y": 50, "width": 50, "height": 50, "fill": {"type": "image", "url": "a.png');position:fixed;/*\\", "mode": "fill"}}]}`)
	h := p.export([]string{id}, `{}`)
	hasNot(t, h, "<script", "family=X", "family=A")
	has(t, h, "family=Noto+Sans:wght@", `font-family:'A\&apos;B\\C',sans-serif;`, `font-family:'L\a M\d N',sans-serif;`, `url('a.png\&apos;);position:fixed;/*\\')`)
}

// A text stroke reaches the HTML export.
func TestTextStrokeIsExported(t *testing.T) {
	p := newPage(t, "")
	text := func(extra string) string {
		return p.export([]string{p.insert(`{"type": "text", "content": "Out", "fontSize": 50` + extra + `}`)}, `{}`)
	}
	center := text(`, "fill": "#ffff00", "stroke": "#000000", "strokeWidth": 4, "strokeAlignment": "center"`)
	has(t, center, "-webkit-text-stroke:4px #000000;")
	hasNot(t, center, "color:transparent")
	has(t, text(`, "fill": "#ffff00", "stroke": "#000000", "strokeWidth": 4, "strokeAlignment": "outer"`), "-webkit-text-stroke:8px #000000;paint-order:stroke fill;")
	// Stroke-only text does not take the inherited color as its fill.
	has(t, text(`, "stroke": "#000000", "strokeWidth": 4`), "color:transparent;-webkit-text-stroke:4px #000000;")
}

// Fixed-width text keeps its newlines and spaces and breaks long words like the canvas; auto-growth text stays on `pre`.
func TestFixedWidthTextWrapsLikeTheCanvas(t *testing.T) {
	p := newPage(t, "")
	style := func(growth string) string {
		return firstStyle(p.fragment(`{"type": "text", "content": "a\n\nb   c", "fontSize": 16, "textGrowth": "` + growth + `", "width": 100}`))
	}
	for _, growth := range []string{"fixed-width", "fixed-width-height"} {
		has(t, style(growth), "white-space:pre-wrap;overflow-wrap:anywhere;")
	}
	// Without a width the box is 0 wide: one word per line, no split inside a word.
	h := p.fragment(`{"type": "text", "content": "one two", "fontSize": 16, "textGrowth": "fixed-width"}`)
	has(t, h, "width:0px;", "white-space:pre-wrap;")
	hasNot(t, h, "overflow-wrap")
	auto := style("auto")
	has(t, auto, "white-space:pre;")
	hasNot(t, auto, "overflow-wrap")
}

// Middle and bottom text in a fixed-size box is placed like the canvas; other boxes keep the top.
func TestVerticalTextAlignmentIsExported(t *testing.T) {
	p := newPage(t, "")
	// Without a height the box is 0 high on the canvas; the flex container needs that height to spill the text.
	h := p.fragment(`{"type": "text", "content": "a\nb", "fontSize": 16, "textGrowth": "fixed-width-height", "width": 100, "textAlignVertical": "middle", "fill": {"type": "gradient", "gradientType": "linear", "rotation": 0, "colors": [{"color": "#ff0000", "position": 0}, {"color": "#0000ff", "position": 1}]}}`)
	has(t, h, "height:0px;", "justify-content:center;\">a\nb</p>", "background-clip:text")
	node := func(growth, valign string) string {
		return p.fragment(`{"type": "text", "content": "ab", "fontSize": 16, "textGrowth": "` + growth + `", "width": 100, "height": 60, "textAlignVertical": "` + valign + `"}`)
	}
	has(t, node("fixed-width-height", "middle"), "display:flex;flex-direction:column;justify-content:center;\">ab</p>")
	has(t, node("fixed-width-height", "bottom"), "justify-content:flex-end;")
	for _, c := range [][2]string{{"fixed-width-height", "top"}, {"fixed-width", "middle"}, {"auto", "bottom"}} {
		hasNot(t, node(c[0], c[1]), "justify-content")
	}
}

// Newlines in text become `<br>`: Esc drops control characters, so each line is escaped on its own.
func TestTextKeepsItsLineBreaks(t *testing.T) {
	p := newPage(t, "")
	for _, growth := range []string{"auto", "fixed-width"} {
		has(t, p.fragment(`{"type": "text", "content": "a\nb <i>\n\nc", "fontSize": 16, "textGrowth": "`+growth+`", "width": 100}`), ">a<br>b &lt;i&gt;<br><br>c</p>")
	}
}

// The options: layer names (default on), layer ids and the page scaffold; several ids follow each other.
func TestOptions(t *testing.T) {
	p := newPage(t, "")
	a := p.insert(`{"type": "rectangle", "name": "A <b>", "width": 10, "height": 10, "fill": "#ff0000"}`)
	b := p.insert(`{"type": "text", "name": "B", "content": "hi", "fontSize": 12, "fontFamily": "Noto Sans", "fontWeight": 700}`)
	c := p.insert(`{"type": "text", "content": "hi", "fontSize": 12, "fontFamily": "Noto Sans"}`)
	h := p.export([]string{a, b, c}, `{}`)
	has(t, h, "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n", "<title>Untitled</title>\n",
		"<link rel=\"stylesheet\" href=\"https://fonts.googleapis.com/css2?family=Noto+Sans:wght@400;700&display=swap\">\n\n<style>",
		"<body>\n<div data-name=\"A &lt;b&gt;\" style=", "</p>\n</body>\n</html>\n")
	before(t, h, `data-name="A &lt;b&gt;"`, `data-name="B"`)
	hasNot(t, h, "data-id")
	h = p.export([]string{a}, `{"includeLayerNames": false, "includeLayerIds": true, "includeHtmlScaffold": false}`)
	if want := `<div data-id="` + a + `" style="`; !strings.HasPrefix(h, want) {
		t.Errorf("want %q first:\n%s", want, h)
	}
	hasNot(t, h, "data-name", "<html")
	// A missing node is an error, as in Rust.
	if _, err := html.Document(p.ctx, []string{"nope"}, nil); err == nil || !strings.Contains(err.Error(), "node not found: nope") {
		t.Errorf("missing node: %v", err)
	}
}
