package render

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Reproduces the exact Arunika liquid-glass landing page structure (8 sections x 3 glass cards
// over glowing blurred ellipses) that caused exponential <use> expansion, heavy lag during generating,
// "SVG data parsing failed cause nodes limit reached", and blank canvas disappearance.
func buildLiquidGlassLandingPageJSON(sections, cardsPerSection int) string {
	var b strings.Builder
	b.WriteString(`{"version":"2.19","children":[{"type":"frame","id":"page","name":"Landing Page","x":0,"y":0,"width":1440,"height":`)
	fmt.Fprintf(&b, `%d`, sections*500)
	b.WriteString(`,"layout":"none","fill":"#eef5ff","children":[`)
	// Background glow ellipses like Cahaya persik/mint/kobalt
	for i := 0; i < 6; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"type":"ellipse","id":"glow%d","x":%d,"y":%d,"width":520,"height":520,"fill":"#7dd3fc55","effect":{"type":"blur","radius":48}}`,
			i, (i%3)*420, i*600)
	}
	for s := 0; s < sections; s++ {
		fmt.Fprintf(&b, `,{"type":"frame","id":"sec%d","name":"Section %d","x":0,"y":%d,"width":1440,"height":460,"layout":"none","children":[`,
			s, s, s*500)
		for c := 0; c < cardsPerSection; c++ {
			if c > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"type":"frame","id":"card%d_%d","x":%d,"y":40,"width":380,"height":360,"cornerRadius":24,"fill":"#ffffff70","effect":{"type":"background_blur","radius":20},"children":[`+
				`{"type":"rectangle","id":"pill%d_%d","x":24,"y":24,"width":120,"height":36,"cornerRadius":18,"fill":"#2563eb"},`+
				`{"type":"rectangle","id":"bar%d_%d","x":24,"y":88,"width":320,"height":180,"cornerRadius":12,"fill":"#dbeafe"}]}`,
				s, c, 60+c*420, s, c, s, c)
		}
		b.WriteString(`]}`)
	}
	b.WriteString(`]}]}`)
	return b.String()
}

func TestManyGlassSectionsRenderWithoutNodesLimit(t *testing.T) {
	js := buildLiquidGlassLandingPageJSON(8, 3)
	d := openJSON(t, js)
	cx := offline(t, d)

	start := time.Now()
	r := &Renderer{}
	r.Sync(cx)
	syncDur := time.Since(start)

	layer := r.Layers["page"]
	if layer == nil || layer.tree == nil {
		t.Fatalf("page layer failed to parse into render tree (canvas disappeared!); syncDur=%v", syncDur)
	}

	renderStart := time.Now()
	pix := r.Render(1200, 800, 0, 0, 0.5, [3]uint8{22, 22, 26}, "", true)
	renderDur := time.Since(renderStart)
	t.Logf("8 sections x 3 glass cards: Sync=%v Render=%v", syncDur, renderDur)

	// Verify canvas actually drew the landing page (not blank background #16161a).
	nonBG := 0
	for i := 0; i < len(pix); i += 4 {
		if pix[i] != 22 || pix[i+1] != 22 || pix[i+2] != 26 {
			nonBG++
		}
	}
	if nonBG == 0 {
		t.Fatal("rendered canvas is completely blank background")
	}

	// Standalone SVG export must ALSO parse cleanly in usvg without hitting NodesLimitReached.
	markup, _, _, ok := svg.NodeSVGAt(cx.Doc, cx.Libs, "page", 1, false, cx.Fonts, cx.Assets, nil)
	if !ok {
		t.Fatal("svg.NodeSVGAt returned ok=false")
	}
	opt := cx.TreeOptions()
	if _, err := usvg.FromStr(markup, &opt); err != nil {
		t.Fatalf("standalone SVG failed usvg.FromStr: %v (markup bytes=%d)", err, len(markup))
	}
}

func TestNativeBackdropBlurBlursUnderlyingPixels(t *testing.T) {
	// Left half red (#ff0000), right half blue (#0000ff), covered by a centered translucent glass card
	// plus 4 small corner glass badges (5 backdrop blurs total -> triggers native O(1) resvg backdrop blur).
	// At the seam (x=100, y=100), native backdrop blur must blend red and blue into a purple/mixed tone.
	js := `{"version":"2.19","children":[{"type":"frame","id":"root","x":0,"y":0,"width":200,"height":200,"layout":"none","fill":"#ffffff","children":[
		{"type":"rectangle","id":"left","x":0,"y":0,"width":100,"height":200,"fill":"#ff0000"},
		{"type":"rectangle","id":"right","x":100,"y":0,"width":100,"height":200,"fill":"#0000ff"},
		{"type":"frame","id":"glass","x":40,"y":40,"width":120,"height":120,"cornerRadius":16,"fill":"#ffffff20","effect":{"type":"background_blur","radius":16}},
		{"type":"rectangle","id":"b1","x":170,"y":10,"width":16,"height":16,"fill":"#ffffff20","effect":{"type":"background_blur","radius":4}},
		{"type":"rectangle","id":"b2","x":170,"y":34,"width":16,"height":16,"fill":"#ffffff20","effect":{"type":"background_blur","radius":4}},
		{"type":"rectangle","id":"b3","x":170,"y":58,"width":16,"height":16,"fill":"#ffffff20","effect":{"type":"background_blur","radius":4}},
		{"type":"rectangle","id":"b4","x":170,"y":82,"width":16,"height":16,"fill":"#ffffff20","effect":{"type":"background_blur","radius":4}}
	]}]}`
	d := openJSON(t, js)
	cx := offline(t, d)
	r := &Renderer{}
	r.Sync(cx)
	pix := r.Render(200, 200, 0, 0, 1, [3]uint8{255, 255, 255}, "", true)

	// Pixel slightly left of seam inside glass (x=96, y=100) must have significant blue blended in from right half.
	off := (100*200 + 96) * 4
	red, blue := pix[off], pix[off+2]
	if red < 80 || blue < 60 {
		t.Fatalf("expected blurred backdrop mix at (96,100) inside glass, got RGBA=(%d,%d,%d,%d)", pix[off], pix[off+1], pix[off+2], pix[off+3])
	}
	// Pixel outside glass on left (x=10, y=10) must stay crisp red with near-zero blue.
	outOff := (10*200 + 10) * 4
	if pix[outOff] < 240 || pix[outOff+2] > 10 {
		t.Fatalf("expected sharp red outside glass at (10,10), got RGBA=(%d,%d,%d,%d)", pix[outOff], pix[outOff+1], pix[outOff+2], pix[outOff+3])
	}
}

func TestOverlappingGlassExportStaysBelowNodesLimit(t *testing.T) {
	// 25 overlapping background_blur cards at identical coordinates would expand to O(2^25) > 33M
	// <use> nodes without bounded expansion tracking. Both canvas sync and standalone SVG export
	// must parse cleanly under usvg's 1M node cap.
	var b strings.Builder
	b.WriteString(`{"version":"2.19","children":[{"type":"frame","id":"stack","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#0f172a","children":[`)
	for i := 0; i < 25; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"type":"frame","id":"g%d","x":%d,"y":%d,"width":300,"height":300,"cornerRadius":20,"fill":"#ffffff18","effect":{"type":"background_blur","radius":12}}`,
			i, i*2, i*2)
	}
	b.WriteString(`]}]}`)

	d := openJSON(t, b.String())
	cx := offline(t, d)

	r := &Renderer{}
	r.Sync(cx)
	if layer := r.Layers["stack"]; layer == nil || layer.tree == nil {
		t.Fatal("overlapping glass stack failed to parse in renderer")
	}

	markup, _, _, ok := svg.NodeSVGAt(cx.Doc, cx.Libs, "stack", 1, false, cx.Fonts, cx.Assets, nil)
	if !ok {
		t.Fatal("svg.NodeSVGAt returned ok=false")
	}
	opt := cx.TreeOptions()
	if _, err := usvg.FromStr(markup, &opt); err != nil {
		t.Fatalf("overlapping glass standalone SVG hit usvg error: %v (bytes=%d)", err, len(markup))
	}
}
