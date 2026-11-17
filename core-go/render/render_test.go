package render

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// The render.rs tests that do not need vello (the vello-vs-resvg comparisons are dropped), plus a
// serial-vs-parallel band test and a cached-vs-cold render test.

var (
	fontsOnce sync.Once
	testFonts *text.Fonts
)

// offline is a context with the machine's fonts and assets, without downloads.
func offline(t *testing.T, d *doc.Doc) *Ctx {
	t.Helper()
	fontsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "nalarupa-render-test")
		if err != nil {
			panic(err)
		}
		testFonts = text.New(filepath.Join(dir, "fonts"))
		testFonts.Online = false
	})
	assets := svg.NewAssets(filepath.Join(t.TempDir(), "cache"))
	assets.Online = false
	return &Ctx{Doc: d, Fonts: testFonts, Assets: assets}
}

func openJSON(t *testing.T, s string) *doc.Doc {
	t.Helper()
	d, err := doc.FromJSON([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// node is the document node at a path of child indices.
func node(d *doc.Doc, path ...int) *jsonv.Object {
	var n jsonv.Value = d.Root
	for _, i := range path {
		n = jsonv.Items(jsonv.Get(n, "children"))[i]
	}
	return jsonv.Obj(n)
}

func syncSlides(r *Renderer, cx *Ctx) {
	roots := r.ShiftSiblings(cx)
	rebuilt := r.Sync(cx)
	r.RefreshSlides(cx, rebuilt, roots)
}

func frame(r *Renderer, w, h uint32, camX, camY float64, bg [3]uint8) []byte {
	return slices.Clone(r.Render(w, h, camX, camY, 1, bg, "", true))
}

func stale(a, b []byte, by int) int {
	n := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d > by || -d > by {
			n++
		}
	}
	return n
}

func cold(t *testing.T, d *doc.Doc) *Renderer {
	r := &Renderer{}
	r.Sync(offline(t, d))
	return r
}

// Rotating a node repaints everything it painted before and after: the incremental frame equals a cold one.
func TestRotationLeavesNoTrails(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":2000,"height":2000,"fill":"#ffffff",
		"children":[{"type":"rectangle","id":"r","x":900,"y":900,"width":300,"height":60,"fill":"#ff0000"}]}]}`)
	fr := func(r *Renderer) []byte {
		return slices.Clone(r.Render(600, 400, 700, 700, 1, [3]uint8{240, 240, 240}, "", true))
	}
	live := &Renderer{}
	cx := offline(t, d)
	live.Sync(cx)
	fr(live)
	for _, deg := range []int64{30, 75, 0} {
		node(d, 0, 0).Set("rotation", jsonv.Int(deg))
		live.Sync(cx)
		if !bytes.Equal(fr(live), fr(cold(t, d))) {
			t.Errorf("stale pixels after rotating to %d degrees", deg)
		}
	}
}

// A container's opacity or effect changes its whole subtree, and a group's shadow follows its children's
// outlines: after either edit the incremental frame equals a cold render.
func TestSubtreeAndAncestorEffectsLeaveNoStalePixels(t *testing.T) {
	docs := []struct {
		json  string
		edits [][2]string
	}{
		{`{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":300,"layout":"none","fill":"#ffffff","children":[
			{"type":"frame","id":"o","x":20,"y":20,"width":100,"height":100,"layout":"none","fill":"#ff000080","children":[
				{"type":"rectangle","id":"r","x":60,"y":30,"width":80,"height":40,"fill":"#000000"}]}]}]}`,
			[][2]string{{"o", `{"opacity": 0.5}`}, {"o", `{"opacity": 0}`}, {"o", `{"effect": {"type": "blur", "radius": 6}}`}, {"o", `{"fill": "#00ff0080"}`},
				{"o", `{"fill": {"type": "color", "color": "#00ff0080", "blendMode": "multiply"}}`}}},
		{`{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":300,"layout":"none","fill":"#ffffff","children":[
			{"type":"group","id":"g","x":40,"y":30,"effect":{"type":"shadow","offset":{"x":12,"y":12},"blur":0,"color":"#000000"},"children":[
				{"type":"rectangle","id":"a","x":0,"y":0,"width":60,"height":50,"fill":"#000000"},
				{"type":"rectangle","id":"b","x":70,"y":60,"width":40,"height":40,"fill":"#0000ff"}]}]}]}`,
			[][2]string{{"b", `{"opacity": 0.2}`}, {"b", `{"x": 90}`}, {"g", `{"opacity": 0.2}`}}},
		{`{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":300,"height":300,"layout":"none","fill":"#ffffff","children":[
			{"type":"group","id":"g","x":19,"y":40,"children":[
				{"type":"frame","id":"box","x":26,"y":111,"width":144,"height":157,"layout":"none","effect":{"type":"shadow","offset":{"x":-8,"y":10},"blur":4,"color":"#00000080"}},
				{"type":"rectangle","id":"r","x":30,"y":120,"width":60,"height":60,"fill":"#ff0000"}]}]}]}`,
			[][2]string{{"g", `{"opacity": 0.2}`}}},
	}
	white := [3]uint8{255, 255, 255}
	for _, c := range docs {
		for _, e := range c.edits {
			d := openJSON(t, c.json)
			cx := offline(t, d)
			live := &Renderer{}
			live.Sync(cx)
			frame(live, 400, 300, 0, 0, white)
			if err := d.Update(e[0], jsonv.MustParse(e[1])); err != nil {
				t.Fatal(err)
			}
			live.Sync(cx)
			if n := stale(frame(live, 400, 300, 0, 0, white), frame(cold(t, d), 400, 300, 0, 0, white), 10); n != 0 {
				t.Errorf("%d stale channels after %s %s", n, e[0], e[1])
			}
		}
	}
}

// Glyphs of a tight-line-height text stick out of its layout box: editing or deleting it repaints them.
func TestTextOverflowLeavesNoGhosts(t *testing.T) {
	js := func(lh, box, size string) string {
		return `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":1920,"height":1080,"layout":"none","fill":"#0f1115","children":[
			{"type":"text","id":"t","x":160,"y":587,"width":1600,` + box + `,"content":"Say one thing,\nbeautifully.","fontSize":` + size + `,"fontWeight":"700","lineHeight":` + lh + `,"fill":"#f4f1ea"}]}]}`
	}
	bg := [3]uint8{28, 28, 31}
	fr := func(r *Renderer) []byte { return slices.Clone(r.Render(640, 400, 160, 700, 1, bg, "", true)) }
	edits := []struct {
		name string
		edit func(f, t *jsonv.Object)
	}{
		{"delete", func(f, _ *jsonv.Object) { f.Set("children", jsonv.NewArray()) }},
		{"recolor", func(_, t *jsonv.Object) { t.Set("fill", "#ff0000") }},
		{"move", func(_, t *jsonv.Object) { t.Set("y", jsonv.Int(287)) }},
		{"hide", func(_, t *jsonv.Object) { t.Set("enabled", false) }},
	}
	cases := [][3]string{{"1.05", `"textGrowth":"fixed-width"`, "120"}, {"0.8", `"textGrowth":"fixed-width"`, "120"},
		{"1.05", `"textGrowth":"fixed-width"`, `"120"`}, {"1.05", `"textGrowth":"fixed-width-height","height":40`, "120"}}
	for _, c := range cases {
		for _, e := range edits {
			d := openJSON(t, js(c[0], c[1], c[2]))
			cx := offline(t, d)
			live := &Renderer{}
			live.Sync(cx)
			fr(live)
			e.edit(node(d, 0), node(d, 0, 0))
			live.Sync(cx)
			// A ghost is hundreds of channels. The repaint paints the glyphs from other band origins than a cold
			// frame, which on the resvg path moves a few edge pixels of their antialiasing (Rust's test runs on
			// vello; the canvas parity scenarios show the same pixels from Rust's resvg path).
			if n := stale(fr(live), fr(cold(t, d)), 10); n > 24 {
				t.Errorf("%d stale glyph channels after %s at %v", n, e.name, c)
			}
		}
	}
}

// Culling, the cached view, resizes and dirty repaints never lose or misplace content: after a seeded walk
// of pans, resizes, zooms and edits (each preceded by a live frame), every sharp frame matches a cold
// render of a much larger view on the same pixel grid.
func TestCachedViewMatchesColdRender(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[
		{"type":"frame","id":"a","x":-1500,"y":-900,"width":900,"height":700,"fill":"#ffffff","layout":"none","clip":true,"children":[
			{"type":"rectangle","id":"a1","x":700,"y":500,"width":400,"height":400,"fill":"#ff0000"},
			{"type":"rectangle","id":"a2","x":50,"y":50,"width":200,"height":120,"fill":"#00aa00","cornerRadius":24,"strokeWidth":6,"stroke":"#000000"}]},
		{"type":"frame","id":"b","x":200,"y":-300,"width":600,"height":1600,"fill":"#eeeeff","layout":"none","children":[
			{"type":"rectangle","id":"b1","x":450,"y":1400,"width":400,"height":400,"fill":"#0000ff"},
			{"type":"ellipse","id":"b2","x":100,"y":200,"width":300,"height":300,"fill":"#ffaa00","rotation":30,
			 "effect":{"type":"shadow","blur":30,"offset":{"x":0,"y":12},"color":"#00000088"}}]},
		{"type":"frame","id":"c","x":3000,"y":2500,"width":400,"height":300,"fill":"#222222"}]}`)
	seed := uint64(0x2545F4914F6CDD1D)
	rnd := func(n uint64) uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed % n
	}
	cx := offline(t, d)
	live := &Renderer{}
	live.Sync(cx)
	w, h, camX, camY, zoom := uint32(800), uint32(600), -1600.0, -1000.0, 0.5
	bg := [3]uint8{28, 28, 31}
	steps := 150
	if testing.Short() {
		steps = 30
	}
	for step := range steps {
		switch rnd(6) {
		case 0:
			camX, camY = camX+(float64(rnd(101))-50)/zoom, camY+(float64(rnd(101))-50)/zoom
		case 1:
			camX, camY = camX+(float64(rnd(4001))-2000)/zoom, camY+(float64(rnd(4001))-2000)/zoom
		case 2:
			w, h = 300+uint32(rnd(900)), 200+uint32(rnd(800))
		case 3:
			zoom = []float64{0.13 * 1.25, 0.25, 0.5, 1.25, 2.5, 0.7}[rnd(6)]
		case 4:
			x, y := rnd(800), rnd(1500)
			node(d, 1, 0).Set("x", jsonv.Int(int64(x)))
			node(d, 1, 0).Set("y", jsonv.Int(int64(y)))
			live.Sync(cx)
		default:
			camX, camY = -1600+float64(rnd(5000)), -1000+float64(rnd(4000))
		}
		live.Render(w, h, camX, camY, zoom, bg, "", false)
		got := slices.Clone(live.Render(w, h, camX, camY, zoom, bg, "", true))
		const pad = 1500
		c := cold(t, d)
		bw, bh := w+2*pad, h+2*pad
		vx, vy := math.Round(camX*zoom)-pad, math.Round(camY*zoom)-pad
		big := c.Render(bw, bh, vx/zoom, vy/zoom, zoom, bg, "", true)
		bad := 0
		for y := range h {
			for x := range w {
				g := got[(y*w+x)*4:]
				b := big[((y+pad)*bw+x+pad)*4:]
				for i := range 3 {
					if dd := int(g[i]) - int(b[i]); dd > 16 || dd < -16 {
						bad++
						break
					}
				}
			}
		}
		// Rust's test runs on vello. On the resvg path a filter (the rotated ellipse's blurred shadow) is
		// computed over the painted region, so its edge pixels shift with the view origin: allow a few.
		if bad > int(w*h)/5000 {
			t.Fatalf("step %d: %d pixels differ from a cold render (%dx%d cam %.0f,%.0f zoom %v)", step, bad, w, h, camX, camY, zoom)
		}
	}
}

// Painting the bands on goroutines gives the bytes painting them one after another does.
func TestParallelBandsMatchSerial(t *testing.T) {
	d, err := doc.Load(filepath.Join("..", "testdata", "svg", "effects.pen"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Renderer{Threads: 7}
	r.Sync(offline(t, d))
	b, _ := r.Bounds()
	p := r.painter()
	w, h := uint32(700), uint32(1500)
	x0, y0 := int32(b.X), int32(b.Y)
	par := make([]byte, w*h*4)
	if !p.paint("", 1.25, [3]uint8{10, 20, 30}, x0, y0, w, h, par) {
		t.Fatal("parallel paint failed")
	}
	p.serial = true
	ser := make([]byte, w*h*4)
	if !p.paint("", 1.25, [3]uint8{10, 20, 30}, x0, y0, w, h, ser) {
		t.Fatal("serial paint failed")
	}
	if !bytes.Equal(par, ser) {
		t.Error("parallel bands differ from serial ones")
	}
}

// Frames served from the cached view (pans inside the margin, and after edits repaint their dirty
// rectangles) equal a cold render of the same camera, within the rounding of other band edges.
func TestCachedFrameMatchesCold(t *testing.T) {
	d, err := doc.Load(filepath.Join("..", "testdata", "svg", "shapes.pen"))
	if err != nil {
		t.Fatal(err)
	}
	cx := offline(t, d)
	r := &Renderer{}
	r.Sync(cx)
	bg := [3]uint8{30, 30, 30}
	r.Render(640, 360, 0, 0, 1, bg, "", true)
	for _, step := range []func(){
		func() {},
		func() { d.Update("r1", jsonv.MustParse(`{"x": 60, "fill": "#00ff00"}`)); r.Sync(cx) },
		func() { d.Update("basic", jsonv.MustParse(`{"opacity": 0.5}`)); r.Sync(cx) },
	} {
		step()
		for _, cam := range [][2]float64{{0, 0}, {100, 50}, {-200, -100}} {
			got := slices.Clone(r.Render(640, 360, cam[0], cam[1], 1, bg, "", true))
			want := cold(t, d).Render(640, 360, cam[0], cam[1], 1, bg, "", true)
			// Other band origins round some edge pixels differently (as in Rust's tests): no ghost may remain.
			if n := stale(got, want, 16); n > len(want)/20000 {
				t.Errorf("cam %v: %d channels differ from a cold render", cam, n)
			}
		}
	}
}

// Boxes carry their painted transform, and hits land where rotated or flipped nodes are painted.
func TestRotatedBoxesHitWherePainted(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","rotation":90,
		"children":[{"type":"rectangle","id":"r","x":10,"y":20,"width":50,"height":30},
					{"type":"rectangle","id":"k","x":300,"y":300,"width":40,"height":40,"flipX":true,"rotation":-90}]}]}`)
	r := cold(t, d)
	m := *r.Find("r").M
	x, y := m[0]*60+m[2]*50+m[4], m[1]*60+m[3]*50+m[5]
	if abs(x-50) > 1e-9 || abs(y+60) > 1e-9 {
		t.Errorf("corner maps to (%v, %v)", x, y)
	}
	want(t, HitChain(r, 35, -35), "f", "r")
	want(t, HitChain(r, 35, 35))
	want(t, HitChain(r, 280, -280), "f", "k")
}

func want(t *testing.T, got []string, ids ...string) {
	t.Helper()
	if !slices.Equal(got, ids) {
		t.Errorf("hit %v, want %v", got, ids)
	}
}

// A child painted outside its unclipped parent's box is hit there; a clipping ancestor still hides it.
func TestOverflowHitsUnlessClipped(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[
		{"type":"frame","id":"f","x":100,"y":100,"width":100,"height":100,"layout":"none","children":[
			{"type":"rectangle","id":"o","x":80,"y":80,"width":100,"height":100}]},
		{"type":"frame","id":"t","x":400,"y":0,"width":200,"height":200,"layout":"none","children":[
			{"type":"frame","id":"i","x":0,"y":0,"width":50,"height":50,"layout":"none","children":[
				{"type":"rectangle","id":"n","x":40,"y":40,"width":100,"height":100}]}]},
		{"type":"frame","id":"c","x":800,"y":0,"width":100,"height":100,"layout":"none","clip":true,"children":[
			{"type":"rectangle","id":"h","x":80,"y":80,"width":100,"height":100}]}]}`)
	r := cold(t, d)
	want(t, HitChain(r, 190, 190), "f", "o")
	want(t, HitChain(r, 250, 250), "f", "o")
	want(t, HitChain(r, 285, 285))
	want(t, HitChain(r, 500, 100), "t", "i", "n")
	want(t, HitChain(r, 860, 50), "c")
	want(t, HitChain(r, 880, 80), "c", "h")
	want(t, HitChain(r, 950, 150))
}

const pendingFill = `{"type":"image","url":"nalarupa:pending-x"}`

// A pending image or placeholder inside a clipping frame is cut to the frame's box; one clipped away is
// not live.
func TestLiveOverlaysFollowClippingAncestors(t *testing.T) {
	d := openJSON(t, strings.ReplaceAll(`{"version":"2.19","children":[
		{"type":"frame","id":"c","x":100,"y":100,"width":300,"height":200,"layout":"none","clip":true,"children":[
			{"type":"rectangle","id":"img","x":150,"y":50,"width":400,"height":300,"fill":P},
			{"type":"rectangle","id":"gone","x":500,"y":10,"width":50,"height":50,"fill":P},
			{"type":"frame","id":"far","x":350,"y":0,"width":100,"height":100,"layout":"none","clip":true,"children":[
				{"type":"rectangle","id":"apart","x":-50,"y":10,"width":100,"height":50,"fill":P}]}]},
		{"type":"frame","id":"open","x":600,"y":0,"width":100,"height":100,"layout":"none","children":[
			{"type":"rectangle","id":"free","x":80,"y":80,"width":100,"height":100,"fill":P}]}]}`, "P", pendingFill))
	live := cold(t, d).Live()
	got := fmt.Sprint(len(live))
	for _, l := range live {
		got += fmt.Sprintf(" %s %v %v", l.Flat.ID, l.HasClip, l.Clip)
	}
	if want := "2 img true {100 100 300 200} free false {0 0 0 0}"; got != want {
		t.Errorf("live %s, want %s", got, want)
	}
}

// A round placeholder is cut to its corners, and so is one inside rounded clipping frames (as painted).
func TestLiveOverlaysReportCornerRadii(t *testing.T) {
	d := openJSON(t, strings.ReplaceAll(`{"version":"2.19","children":[
		{"type":"rectangle","id":"round","x":0,"y":0,"width":100,"height":100,"cornerRadius":[50,50,10,0],"fill":P},
		{"type":"ellipse","id":"oval","x":200,"y":0,"width":100,"height":100,"fill":P},
		{"type":"frame","id":"c","x":400,"y":0,"width":100,"height":100,"layout":"none","clip":true,"cornerRadius":30,"children":[
			{"type":"rectangle","id":"in","x":0,"y":0,"width":100,"height":100,"fill":P}]},
		{"type":"frame","id":"tilt","x":600,"y":0,"width":100,"height":100,"layout":"none","clip":true,"cornerRadius":30,"rotation":10,"children":[
			{"type":"rectangle","id":"tin","x":0,"y":0,"width":100,"height":100,"fill":P}]},
		{"type":"frame","id":"outer","x":800,"y":0,"width":100,"height":100,"layout":"none","clip":true,"cornerRadius":30,"children":[
			{"type":"frame","id":"inner","x":0,"y":0,"width":60,"height":60,"layout":"none","clip":true,"children":[
				{"type":"rectangle","id":"deep","x":0,"y":0,"width":60,"height":60,"fill":P}]}]},
		{"type":"frame","id":"flip","x":1000,"y":0,"width":100,"height":100,"layout":"none","clip":true,"cornerRadius":[40,0,0,0],"flipX":true,"children":[
			{"type":"rectangle","id":"fin","x":0,"y":0,"width":100,"height":100,"fill":P}]}]}`, "P", pendingFill))
	var got []string
	for _, l := range cold(t, d).Live() {
		var cuts []string
		for _, c := range l.Cuts {
			cuts = append(cuts, fmt.Sprint(c.Radius))
		}
		got = append(got, fmt.Sprintf("%s %v %v %v", l.Flat.ID, l.Flat.Radius, l.HasClip, cuts))
	}
	exp := []string{"round [50 50 10 0] false []", "oval [0 0 0 0] false []", "in [0 0 0 0] true [[30 30 30 30]]",
		"tin [0 0 0 0] true []", "deep [0 0 0 0] true [[30 30 30 30]]", "fin [0 0 0 0] true [[0 40 0 0]]"}
	if !slices.Equal(got, exp) {
		t.Errorf("live\n%v\nwant\n%v", got, exp)
	}
}

// A node painted fully transparent (own or ancestor opacity 0) shows no overlay.
func TestLiveSkipsInvisibleNodes(t *testing.T) {
	d := openJSON(t, strings.ReplaceAll(`{"version":"2.19","children":[
		{"type":"frame","id":"f","x":0,"y":0,"width":800,"height":600,"layout":"none","children":[
			{"type":"rectangle","id":"visible","x":10,"y":10,"width":100,"height":100,"fill":P},
			{"type":"rectangle","id":"op0","x":300,"y":10,"width":100,"height":100,"opacity":0,"fill":P},
			{"type":"frame","id":"g","x":300,"y":200,"width":200,"height":200,"opacity":0,"layout":"none","children":[
				{"type":"rectangle","id":"inner","x":10,"y":10,"width":50,"height":50,"fill":P}]},
			{"type":"frame","id":"ph","x":550,"y":200,"width":100,"height":100,"opacity":0,"placeholder":true},
			{"type":"frame","id":"half","x":550,"y":350,"width":100,"height":100,"opacity":0.5,"placeholder":true}]}]}`, "P", pendingFill))
	var got []string
	for _, l := range cold(t, d).Live() {
		got = append(got, l.Flat.ID)
	}
	if !slices.Equal(got, []string{"visible", "half"}) {
		t.Errorf("live %v", got)
	}
}

// A group paints nothing of its own: a point in its box but on none of its children hits the layer below.
func TestGroupBoxDoesNotSwallowHits(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[
		{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","children":[
			{"type":"rectangle","id":"u","x":100,"y":100,"width":150,"height":150},
			{"type":"group","id":"g","children":[
				{"type":"rectangle","id":"a","x":20,"y":20,"width":40,"height":40},
				{"type":"rectangle","id":"b","x":300,"y":300,"width":40,"height":40}]}]},
		{"type":"group","id":"t","children":[
			{"type":"rectangle","id":"c","x":500,"y":0,"width":20,"height":20},
			{"type":"rectangle","id":"d","x":700,"y":200,"width":20,"height":20}]}]}`)
	r := cold(t, d)
	want(t, HitChain(r, 40, 40), "f", "g", "a")
	want(t, HitChain(r, 150, 150), "f", "u")
	want(t, HitChain(r, 320, 320), "f", "g", "b")
	want(t, HitChain(r, 510, 10), "t", "c")
	want(t, HitChain(r, 600, 100))
}

// A click lands on what a shape paints: empty parts of a shape's box fall through to the layer below.
func TestShapeBoxDoesNotSwallowHits(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[
		{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":300,"layout":"none","fill":"#ffffff","children":[
			{"type":"rectangle","id":"card","x":20,"y":20,"width":360,"height":260,"fill":"#3b82f6"},
			{"type":"ellipse","id":"avatar","x":100,"y":100,"width":100,"height":100,"fill":"#ef4444"},
			{"type":"path","id":"diag","x":220,"y":40,"width":150,"height":150,"geometry":"M0 0 L150 150","stroke":"#000000","strokeWidth":2},
			{"type":"rectangle","id":"outline","x":230,"y":200,"width":100,"height":60,"stroke":"#000000","strokeWidth":2},
			{"type":"ellipse","id":"ring","x":40,"y":220,"width":40,"height":40,"stroke":"#000000","strokeWidth":6,"strokeAlignment":"outer"}]}]}`)
	r := cold(t, d)
	want(t, HitChain(r, 150, 150), "f", "avatar")
	want(t, HitChain(r, 103, 103), "f", "card")
	want(t, HitChain(r, 295, 115), "f", "diag")
	want(t, HitChain(r, 360, 50), "f", "card")
	want(t, HitChain(r, 230, 230), "f", "outline")
	want(t, HitChain(r, 280, 230), "f", "card")
	want(t, HitChain(r, 60, 216), "f", "ring")
}

// Horizontal and vertical stroked lines have no layer box; they are still drawn in every band they cross.
func TestHairlinesSurviveBanding(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":800,"height":800,"layout":"none","fill":"#ffffff","children":[
		{"type":"path","id":"h","x":0,"y":400,"width":800,"height":0,"geometry":"M0 0 L800 0","stroke":"#000000","strokeWidth":2},
		{"type":"path","id":"v","x":400,"y":0,"width":0,"height":800,"geometry":"M0 0 L0 800","stroke":"#000000","strokeWidth":2}]}]}`)
	px := frame(cold(t, d), 800, 800, 0, 0, [3]uint8{255, 255, 255})
	dark := func(x, y int) bool { return px[(y*800+x)*4] < 128 || px[(y*800+x-1)*4] < 128 }
	for i := 25; i < 800; i += 50 {
		if !dark(400, i) {
			t.Errorf("vertical y=%d missing", i)
		}
		if !dark(i, 400) {
			t.Errorf("horizontal x=%d missing", i)
		}
	}
}

func count(px []byte, w, x0, x1, y0, y1 int, keep func(p []byte) bool) int {
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if keep(px[(y*w+x)*4:]) {
				n++
			}
		}
	}
	return n
}

// Text takes a stroke like a shape: stroke-only text is visible, and a stroke paints around filled glyphs.
func TestTextStrokeIsPainted(t *testing.T) {
	txt := func(x int, extra string) string {
		return fmt.Sprintf(`{"type":"text","x":%d,"y":10,"content":"Out","fontSize":50,"fontWeight":"700"%s}`, x, extra)
	}
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":580,"height":80,"layout":"none","fill":"#ffffff","children":[`+
		strings.Join([]string{txt(10, `,"fill":"#ffff00"`), txt(150, `,"fill":"#ffff00","stroke":"#000000","strokeWidth":4`),
			txt(290, `,"stroke":"#000000","strokeWidth":4`), txt(430, `,"fill":"#ffff00","stroke":"#000000","strokeWidth":4,"strokeAlignment":"outer"`),
			txt(570, `,"fill":"#ffff00","stroke":"#000000","strokeWidth":4,"strokeAlignment":"inner"`)}, ",")+`]}]}`)
	px := frame(cold(t, d), 720, 80, 0, 0, [3]uint8{255, 255, 255})
	dark := func(x0, x1 int) int { return count(px, 720, x0, x1, 0, 80, func(p []byte) bool { return p[0] < 80 }) }
	c := []int{dark(0, 140), dark(140, 280), dark(280, 420), dark(420, 560), dark(560, 720)}
	if c[0] != 0 || c[1] <= 100 || c[2] <= 100 || c[3] <= 100 || c[4] <= 100 || c[3] <= c[1] || c[4] >= c[1] {
		t.Errorf("stroke pixels %v", c)
	}
}

// A text shadow follows what paints: stroke-only text casts its stroke, not solid glyphs.
func TestStrokeOnlyTextCastsAHollowShadow(t *testing.T) {
	txt := func(y int, extra string) string {
		return fmt.Sprintf(`{"type":"text","x":10,"y":%d,"content":"Out","fontSize":50,"fontWeight":"700","stroke":"#000000","strokeWidth":2,"effect":{"type":"shadow","offset":{"x":150,"y":0},"color":"#ff0000"}%s}`, y, extra)
	}
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":300,"height":180,"layout":"none","fill":"#ffffff","children":[`+
		txt(10, `,"fill":"#ffff00"`)+","+txt(100, "")+`]}]}`)
	px := frame(cold(t, d), 300, 180, 0, 0, [3]uint8{255, 255, 255})
	red := func(y0, y1 int) int {
		return count(px, 300, 150, 300, y0, y1, func(p []byte) bool { return p[0] > 200 && p[1] < 80 })
	}
	solid, hollow := red(0, 95), red(95, 180)
	if hollow <= 100 || hollow*3 >= solid*2 {
		t.Errorf("shadow pixels filled %d, stroke-only %d", solid, hollow)
	}
}

// A shadow offset lives in the node's own axes: it turns with a rotation and mirrors with a flip.
func TestShadowOffsetFollowsRotationAndFlips(t *testing.T) {
	sh := `"effect":{"type":"shadow","offset":{"x":30,"y":0},"blur":0,"color":"#ff0000"}`
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":200,"height":200,"layout":"none","fill":"#ffffff","clip":true,"children":[
			{"type":"rectangle","x":80,"y":120,"width":40,"height":20,"rotation":90,"fill":"#0000ff",`+sh+`}]},
		  {"type":"frame","id":"g","x":0,"y":300,"width":200,"height":200,"layout":"none","fill":"#ffffff","clip":true,"children":[
			{"type":"rectangle","x":100,"y":50,"width":40,"height":40,"flipX":true,"fill":"#0000ff",`+sh+`}]}]}`)
	d.EnsureIDs()
	px := frame(cold(t, d), 200, 500, 0, 0, [3]uint8{255, 255, 255})
	red := func(x0, x1, y0, y1 int) int {
		return count(px, 200, x0, x1, y0, y1, func(p []byte) bool { return p[0] > 200 && p[1] < 60 && p[2] < 60 })
	}
	if a, b, c, e := red(80, 100, 50, 80), red(0, 200, 0, 200), red(30, 60, 350, 390), red(0, 200, 300, 500); a != 600 || b != 600 || c != 1200 || e != 1200 {
		t.Errorf("red pixels %d %d %d %d, want 600 600 1200 1200", a, b, c, e)
	}
}

// A group's shadow follows what its children paint, strokes included.
func TestGroupShadowIncludesChildrenStrokes(t *testing.T) {
	rect := `"type":"rectangle","x":40,"y":40,"width":70,"height":70,"fill":"#e03030","stroke":"#102030","strokeWidth":10`
	sh := `"effect":{"type":"shadow","offset":{"x":12,"y":12},"blur":0,"color":"#206020"}`
	fr := func(y int, n string) string {
		return fmt.Sprintf(`{"type":"frame","id":"f%d","x":0,"y":%d,"width":200,"height":200,"layout":"none","fill":"#ffffff","children":[%s]}`, y, y, n)
	}
	d := openJSON(t, `{"version":"2.19","children":[`+fr(0, `{"type":"group",`+sh+`,"children":[{`+rect+`}]}`)+","+fr(300, `{`+rect+","+sh+`}`)+`]}`)
	d.EnsureIDs()
	px := frame(cold(t, d), 200, 500, 0, 0, [3]uint8{255, 255, 255})
	row := func(y int) []int {
		var out []int
		for x := range 200 {
			if bytes.Equal(px[(y*200+x)*4:(y*200+x)*4+3], []byte{0x20, 0x60, 0x20}) {
				out = append(out, x)
			}
		}
		return out
	}
	g, own := row(118), row(418)
	if len(g) == 0 || g[0] != 47 || g[len(g)-1] != 126 || !slices.Equal(g, own) {
		t.Errorf("group shadow %v, own %v", g, own)
	}
}

// An inner shadow is measured from the fill edge whatever the stroke alignment.
func TestInnerShadowIgnoresStrokeAlignment(t *testing.T) {
	for _, al := range []string{"inner", "center", "outer"} {
		d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":200,"height":200,"layout":"none","fill":"#ffffff","children":[{"type":"rectangle","id":"r","x":50,"y":40,"width":100,"height":100,"fill":"#0000ff","stroke":"#00000000","strokeWidth":8,"strokeAlignment":"`+al+`","effect":{"type":"shadow","shadowType":"inner","offset":{"x":12,"y":0},"blur":0,"color":"#ff0000"}}]}]}`)
		px := frame(cold(t, d), 200, 200, 0, 0, [3]uint8{255, 255, 255})
		var red []int
		for x := range 200 {
			p := px[(90*200+x)*4:]
			if p[0] > 200 && p[1] < 60 && p[2] < 60 {
				red = append(red, x)
			}
		}
		if len(red) == 0 || red[0] != 50 || red[len(red)-1] != 61 {
			t.Errorf("%s: inner shadow band %v", al, red)
		}
	}
}

// Text paints fill, then inner shadows, then the stroke, so the stroke covers an inner shadow.
func TestTextStrokeCoversInnerShadow(t *testing.T) {
	txt := func(x int, extra string) string {
		return fmt.Sprintf(`{"type":"text","x":%d,"y":10,"content":"Out","fontSize":50,"fontWeight":"700","fill":"#ffff00","stroke":"#000000","strokeWidth":4%s}`, x, extra)
	}
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":300,"height":80,"layout":"none","fill":"#ffffff","children":[`+
		txt(10, "")+","+txt(150, `,"effect":{"type":"shadow","shadowType":"inner","offset":{"x":8,"y":8},"color":"#ff0000"}`)+`]}]}`)
	px := frame(cold(t, d), 300, 80, 0, 0, [3]uint8{255, 255, 255})
	c := func(x0 int, keep func(p []byte) bool) int { return count(px, 300, x0, x0+140, 0, 80, keep) }
	plain, shadowed := c(0, func(p []byte) bool { return p[0] < 80 }), c(140, func(p []byte) bool { return p[0] < 80 })
	red := c(140, func(p []byte) bool { return p[0] > 200 && p[1] < 80 })
	if red <= 50 || plain <= 100 || shadowed+plain/50 < plain {
		t.Errorf("red %d, stroke pixels without shadow %d, with %d", red, plain, shadowed)
	}
}

func ago(ms int) time.Time { return time.Now().Add(-time.Duration(ms) * time.Millisecond) }

func startAt(s *Slide, at time.Time) { s.start, s.started = at, true }

func slideByID(r *Renderer, id string) *Slide {
	for _, s := range r.Slides {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// An agent-inserted node is left out of the cached view, painted 50 units low and transparent at its first
// frame, part way there 100 ms later, and back in the cached view once it settles.
func TestInsertedNodeSlidesIn(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff",
		"children":[{"type":"rectangle","id":"r","x":100,"y":100,"width":100,"height":50,"fill":"#ff0000"}]}]}`)
	cx := offline(t, d)
	r := &Renderer{Slides: []*Slide{NewSlide("r", time.Now())}}
	syncSlides(r, cx)
	bg := [3]uint8{30, 30, 30}
	red := func(px []byte, x, y int) bool { return px[(y*400+x)*4] > 200 && px[(y*400+x)*4+1] < 200 }
	if px := frame(r, 400, 400, 0, 0, bg); red(px, 150, 125) {
		t.Error("first frame: fully transparent")
	}
	startAt(r.Slides[0], ago(100))
	px := frame(r, 400, 400, 0, 0, bg)
	if g := px[(103*400+150)*4+1]; g <= 245 {
		t.Errorf("the top rows have moved down, green %d", g)
	}
	if g := px[(153*400+150)*4+1]; g < 120 || g >= 200 {
		t.Errorf("drawn lower and part transparent, green %d", g)
	}
	startAt(r.Slides[0], time.Now().Add(-slideTime))
	if !r.ExpireSlides() || len(r.Slides) != 0 {
		t.Fatal("slide did not expire")
	}
	syncSlides(r, cx)
	px = frame(r, 400, 400, 0, 0, bg)
	if !red(px, 150, 101) || red(px, 150, 152) {
		t.Error("settled in the cached view at its own place")
	}
}

func slideIDs(r *Renderer) []string {
	var out []string
	for _, s := range r.Slides {
		out = append(out, s.ID)
	}
	return out
}

// A node that does not slide and stacks above a slide in its frame settles that slide.
func TestSlideSettlesUnderALaterNodeThatDoesNotSlide(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
		{"type":"rectangle","id":"panel","x":100,"y":100,"width":200,"height":200,"fill":"#ff0000"},
		{"type":"rectangle","id":"apart","x":10,"y":10,"width":20,"height":20,"fill":"#00ff00"},
		{"type":"frame","id":"card","x":320,"y":320,"width":60,"height":60,"layout":"none","fill":"#0000ff","children":[
			{"type":"rectangle","id":"inner","x":0,"y":0,"width":10,"height":10,"fill":"#ffff00"}]},
		{"type":"rectangle","id":"copy","x":150,"y":150,"width":50,"height":50,"fill":"#ffffff"}]}]}`)
	now := time.Now()
	r := &Renderer{Slides: []*Slide{NewSlide("panel", now), NewSlide("apart", now), NewSlide("card", now)}}
	r.Sync(offline(t, d))
	if !r.SettleCovered() || !slices.Equal(slideIDs(r), []string{"apart", "card"}) || r.SettleCovered() {
		t.Errorf("only the panel under the copy settles, left %v", slideIDs(r))
	}
	r = &Renderer{Slides: []*Slide{NewSlide("panel", now), NewSlide("copy", now)}}
	r.Sync(offline(t, d))
	if r.SettleCovered() || len(r.Slides) != 2 {
		t.Error("the copy slides too")
	}
	d = openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
		{"type":"rectangle","id":"base","x":0,"y":0,"width":300,"height":300,"fill":"#ff0000"},
		{"type":"rectangle","id":"mid","x":100,"y":100,"width":200,"height":200,"fill":"#00ff00"},
		{"type":"rectangle","id":"top","x":150,"y":150,"width":50,"height":50,"fill":"#ffffff"}]}]}`)
	r = &Renderer{Slides: []*Slide{NewSlide("base", now), NewSlide("mid", now)}}
	r.Sync(offline(t, d))
	if !r.SettleCovered() || len(r.Slides) != 0 {
		t.Error("mid settles under top, then base under the settled mid")
	}
	d = openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
		{"type":"rectangle","id":"thin","x":100,"y":100,"width":1,"height":50,"fill":"#ff0000"},
		{"type":"frame","id":"box","x":200,"y":0,"width":20,"height":20,"layout":"none","children":[
			{"type":"rectangle","id":"spill","x":0,"y":100,"width":20,"height":20,"fill":"#00ff00"}]},
		{"type":"frame","id":"cut","x":300,"y":0,"width":20,"height":20,"layout":"none","clip":true,"children":[
			{"type":"rectangle","id":"hidden","x":0,"y":100,"width":20,"height":20,"fill":"#00ff00"}]},
		{"type":"rectangle","id":"over_thin","x":90,"y":110,"width":30,"height":10,"fill":"#ffffff"},
		{"type":"rectangle","id":"over_spill","x":195,"y":105,"width":30,"height":10,"fill":"#ffffff"},
		{"type":"rectangle","id":"over_hidden","x":295,"y":105,"width":30,"height":10,"fill":"#ffffff"}]}]}`)
	r = &Renderer{Slides: []*Slide{NewSlide("thin", now), NewSlide("box", now), NewSlide("cut", now)}}
	r.Sync(offline(t, d))
	if !r.SettleCovered() || !slices.Equal(slideIDs(r), []string{"cut"}) {
		t.Errorf("thin and the overflowing box settle, left %v", slideIDs(r))
	}
}

// A clip frame above the slide cuts its overflow in the snapshot, so a node over the cut part does not
// settle it.
func TestSettleIgnoresOverflowCutByClippingAncestor(t *testing.T) {
	d := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
		{"type":"frame","id":"cl","x":0,"y":0,"width":100,"height":100,"layout":"none","clip":true,"children":[
			{"type":"frame","id":"box","x":10,"y":10,"width":20,"height":20,"layout":"none","children":[
				{"type":"rectangle","id":"spill","x":0,"y":150,"width":20,"height":20,"fill":"#00ff00"}]}]},
		{"type":"rectangle","id":"over","x":5,"y":160,"width":30,"height":10,"fill":"#ffffff"}]}]}`)
	r := &Renderer{Slides: []*Slide{NewSlide("box", time.Now())}}
	r.Sync(offline(t, d))
	if r.SettleCovered() || len(r.Slides) != 1 {
		t.Error("the cut spill is not painted, so it does not count")
	}
}

// A settled slide repaints the node's descendants, also where they overflow its box.
func TestSettledSlideRepaintsOverflowingChildren(t *testing.T) {
	before := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":500,"height":400,"layout":"none","fill":"#ffffff"}]}`)
	after := openJSON(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":500,"height":400,"layout":"none","fill":"#ffffff","children":[
		{"type":"frame","id":"c","x":100,"y":100,"width":100,"height":100,"layout":"none","fill":"#334455","children":[
			{"type":"rectangle","id":"b","x":80,"y":20,"width":100,"height":50,"fill":"#22ff55"}]}]}]}`)
	bg := [3]uint8{30, 30, 30}
	r := &Renderer{}
	syncSlides(r, offline(t, before))
	frame(r, 500, 400, 0, 0, bg)
	r.Slides = append(r.Slides, NewSlide("c", time.Now()))
	cx := offline(t, after)
	syncSlides(r, cx)
	frame(r, 500, 400, 0, 0, bg)
	startAt(r.Slides[0], time.Now().Add(-slideTime))
	if !r.ExpireSlides() {
		t.Fatal("not expired")
	}
	syncSlides(r, cx)
	px := frame(r, 500, 400, 0, 0, bg)
	if i := (150*500 + 250) * 4; !bytes.Equal(px[i:i+3], []byte{0x22, 0xff, 0x55}) {
		t.Errorf("the overflowing part of the badge is back, got %v", px[i:i+3])
	}
}

func slideCtx(t *testing.T, js string, ids ...string) (*Renderer, *Ctx) {
	d := openJSON(t, js)
	cx := offline(t, d)
	r := &Renderer{}
	for _, id := range ids {
		r.Slides = append(r.Slides, NewSlide(id, time.Now()))
	}
	syncSlides(r, cx)
	return r, cx
}

func rgb(px []byte, w, x, y int) []byte { return px[(y*w+x)*4 : (y*w+x)*4+3] }

// A sliding node stays in its frame's place in the stacking order.
func TestSlideStaysUnderFramesAboveItsOwn(t *testing.T) {
	r, _ := slideCtx(t, `{"version":"2.19","children":[
		{"type":"frame","id":"p","x":100,"y":100,"width":300,"height":200,"layout":"none","fill":"#2255ff","children":[
			{"type":"rectangle","id":"k","x":20,"y":20,"width":100,"height":100,"fill":"#ff0000"}]},
		{"type":"frame","id":"q","x":150,"y":100,"width":300,"height":300,"layout":"none","fill":"#ffff00"}]}`, "k")
	bg := [3]uint8{30, 30, 30}
	frame(r, 500, 500, 0, 0, bg)
	startAt(r.Slides[0], ago(100))
	px := frame(r, 500, 500, 0, 0, bg)
	if !bytes.Equal(rgb(px, 500, 180, 150), []byte{255, 255, 0}) || rgb(px, 500, 130, 150)[0] <= 100 {
		t.Errorf("got %v and %v", rgb(px, 500, 180, 150), rgb(px, 500, 130, 150))
	}
}

// Inside one top-level frame a sibling above the sliding node's container covers it mid-slide.
func TestSlideStaysUnderSiblingsAboveItsContainer(t *testing.T) {
	r, _ := slideCtx(t, `{"version":"2.19","children":[
		{"type":"frame","id":"d","x":0,"y":0,"width":640,"height":400,"layout":"none","fill":"#1c1c1f","children":[
			{"type":"frame","id":"p","x":100,"y":100,"width":500,"height":300,"layout":"none","fill":"#dddddd","children":[
				{"type":"rectangle","id":"n","x":150,"y":100,"width":300,"height":150,"fill":"#00ff00"}]},
			{"type":"rectangle","id":"q","x":200,"y":150,"width":300,"height":200,"fill":"#ff0000"}]}]}`, "n")
	bg := [3]uint8{30, 30, 30}
	frame(r, 640, 400, 0, 0, bg)
	startAt(r.Slides[0], ago(100))
	px := frame(r, 640, 400, 0, 0, bg)
	if !bytes.Equal(rgb(px, 640, 300, 300), []byte{255, 0, 0}) || rgb(px, 640, 520, 300)[1] <= 60 || !bytes.Equal(rgb(px, 640, 120, 120), []byte{221, 221, 221}) {
		t.Errorf("got %v %v %v", rgb(px, 640, 300, 300), rgb(px, 640, 520, 300), rgb(px, 640, 120, 120))
	}
}

// Two slides in different frames: the lower one's cover pass must not erase the higher one.
func TestSlideInHigherFrameSurvivesLowerCovers(t *testing.T) {
	r, _ := slideCtx(t, `{"version":"2.19","children":[
		{"type":"frame","id":"p","x":0,"y":0,"width":300,"height":300,"layout":"none","fill":"#2255ff","children":[
			{"type":"rectangle","id":"a","x":10,"y":10,"width":100,"height":240,"fill":"#ff0000"}]},
		{"type":"frame","id":"q","x":0,"y":0,"width":300,"height":300,"layout":"none","fill":"#ffff00","children":[
			{"type":"rectangle","id":"b","x":10,"y":150,"width":100,"height":100,"fill":"#00ff00"}]}]}`, "b", "a")
	bg := [3]uint8{30, 30, 30}
	frame(r, 300, 300, 0, 0, bg)
	for _, s := range r.Slides {
		startAt(s, ago(100))
	}
	px := frame(r, 300, 300, 0, 0, bg)
	if p := rgb(px, 300, 50, 200); p[1] <= 250 || p[0] >= 230 {
		t.Errorf("b still shows over q, got %v", p)
	}
}

// A node sliding into a rotated or semi-transparent frame paints under that frame's rotation and opacity.
func TestSlideFollowsParentRotationAndOpacity(t *testing.T) {
	for _, extra := range []string{`"rotation":20`, `"opacity":0.5`} {
		r, cx := slideCtx(t, `{"version":"2.19","children":[{"type":"frame","id":"p","x":100,"y":100,"width":300,"height":200,"layout":"none","fill":"#2255ff",`+extra+`,
			"children":[{"type":"rectangle","id":"k","x":120,"y":120,"width":100,"height":100,"fill":"#ff0000"}]}]}`, "k")
		bg := [3]uint8{30, 30, 30}
		frame(r, 500, 400, 0, 0, bg)
		startAt(r.Slides[0], ago(590))
		late := frame(r, 500, 400, 0, 0, bg)
		startAt(r.Slides[0], time.Now().Add(-slideTime))
		if !r.ExpireSlides() {
			t.Fatal("not expired")
		}
		syncSlides(r, cx)
		done := frame(r, 500, 400, 0, 0, bg)
		jumped := 0
		for i := 0; i < len(late); i += 4 {
			a := abs(float64(late[i])-float64(done[i])) + abs(float64(late[i+1])-float64(done[i+1]))
			if a > 40 {
				jumped++
			}
		}
		if jumped >= 100 {
			t.Errorf("%s: %d pixels change when the slide ends", extra, jumped)
		}
	}
}

// A node sliding into a clipping frame is cut at the frame's edge while it moves.
func TestSlideIsClippedByItsClippingParent(t *testing.T) {
	r, _ := slideCtx(t, `{"version":"2.19","children":[
		{"type":"frame","id":"p","x":100,"y":100,"width":300,"height":200,"layout":"none","clip":true,"fill":"#eeeeaa","children":[
			{"type":"rectangle","id":"k","x":20,"y":150,"width":260,"height":50,"fill":"#00ff00"}]}]}`, "k")
	bg := [3]uint8{30, 30, 30}
	frame(r, 500, 400, 0, 0, bg)
	startAt(r.Slides[0], ago(100))
	px := frame(r, 500, 400, 0, 0, bg)
	green := func(x, y int) bool { return int(px[(y*500+x)*4+1]) > int(px[(y*500+x)*4])+12 }
	outside := 0
	for y := 302; y < 400; y++ {
		for x := range 500 {
			if green(x, y) {
				outside++
			}
		}
	}
	if outside != 0 || !green(200, 290) {
		t.Errorf("%d green pixels below the clipping frame, inside %v", outside, green(200, 290))
	}
}

// Present mode renders one frame alone; a node sliding into it still paints there, one in another frame
// does not, and its clock does not start.
func TestSoloFramePaintsItsSlidingNodes(t *testing.T) {
	r, _ := slideCtx(t, `{"version":"2.19","children":[
		{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
			{"type":"rectangle","id":"r","x":100,"y":100,"width":100,"height":50,"fill":"#ff0000"}]},
		{"type":"frame","id":"g","x":0,"y":500,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
			{"type":"rectangle","id":"s","x":100,"y":100,"width":100,"height":50,"fill":"#ff0000"}]}]}`, "r", "s")
	bg := [3]uint8{30, 30, 30}
	r.Render(400, 400, 0, 0, 1, bg, "f", true)
	for _, s := range r.Slides {
		startAt(s, ago(100))
	}
	px := r.Render(400, 400, 0, 0, 1, bg, "f", true)
	if g := px[(153*400+150)*4+1]; g >= 200 {
		t.Errorf("the sliding node paints in the solo frame, green %d", g)
	}
	px = r.Render(400, 400, 0, 500, 1, bg, "f", true)
	if g := px[(153*400+150)*4+1]; g != 30 {
		t.Errorf("a node sliding in another frame stays out, green %d", g)
	}
	slideByID(r, "s").started = false
	r.Render(400, 400, 0, 0, 1, bg, "f", true)
	if slideByID(r, "s").started {
		t.Error("its clock does not start while another frame is presented")
	}
}

func column(kids string) string {
	return `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"vertical",
		"justifyContent":"center","fill":"#ffffff","children":[` + kids + `]}]}`
}

// Inserting into a centered column moves the existing child up; it glides there from where it was.
func TestReflowedSiblingsGlide(t *testing.T) {
	a := `{"type":"rectangle","id":"a","width":100,"height":100,"fill":"#ff0000"}`
	before, after := openJSON(t, column(a)), openJSON(t, column(a+`,{"type":"rectangle","id":"b","width":100,"height":100,"fill":"#0000ff"}`))
	r := &Renderer{}
	syncSlides(r, offline(t, before))
	if f := r.Find("a"); f == nil || f.Y != 150 {
		t.Fatal("a not at 150")
	}
	r.Slides = append(r.Slides, NewSlide("b", time.Now()))
	syncSlides(r, offline(t, after))
	if f := r.Find("a"); f == nil || f.Y != 100 {
		t.Fatal("a not at 100")
	}
	s := slideByID(r, "a")
	if s == nil || s.from != [2]float64{0, 50} || s.fade {
		t.Fatalf("the moved sibling glides: %+v", s)
	}
	px := frame(r, 400, 400, 0, 0, [3]uint8{30, 30, 30})
	red := func(x, y int) bool { return px[(y*400+x)*4] > 200 && px[(y*400+x)*4+2] < 100 }
	if !red(50, 240) || red(50, 120) {
		t.Error("the sibling starts at its old position")
	}
}

// Inserting into a group up/left of its children moves the group's box origin but not its content.
func TestGroupGrowingUpLeftDoesNotGlide(t *testing.T) {
	js := func(kids string) string {
		return `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","fill":"#ffffff","children":[
			{"type":"group","id":"g","children":[` + kids + `]}]}]}`
	}
	rn := `{"type":"rectangle","id":"r","x":200,"y":200,"width":100,"height":100,"fill":"#ff0000"}`
	r := &Renderer{}
	syncSlides(r, offline(t, openJSON(t, js(rn))))
	if f := r.Find("g"); f.X != 200 || f.Y != 200 {
		t.Fatal("g origin")
	}
	r.Slides = append(r.Slides, NewSlide("n", time.Now()))
	syncSlides(r, offline(t, openJSON(t, js(rn+`,{"type":"rectangle","id":"n","x":20,"y":20,"width":100,"height":100,"fill":"#00ff00"}`))))
	if f := r.Find("g"); f.X != 20 || f.Y != 20 {
		t.Error("the group's box origin moved")
	}
	if slideByID(r, "g") != nil {
		t.Error("the group does not glide")
	}
	px := frame(r, 400, 400, 0, 0, [3]uint8{30, 30, 30})
	if p := px[(250*400+250)*4:]; !(p[0] > 200 && p[2] < 100) {
		t.Error("the existing child stays in place on the first frame")
	}
}

// A sliding node with a drop or inner shadow renders quickly however big it is.
func TestSlidingHugeShadowedNodeRendersQuickly(t *testing.T) {
	for _, sh := range []string{"outer", "inner"} {
		r, _ := slideCtx(t, `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":400,"layout":"none","children":[
			{"type":"frame","id":"n","x":0,"y":0,"width":100000,"height":100000,"fill":"#ff0000","effect":{"type":"shadow","shadowType":"`+sh+`","blur":10}}]}]}`, "n")
		startAt(r.Slides[0], ago(300))
		t0 := time.Now()
		px := frame(r, 400, 400, 0, 0, [3]uint8{30, 30, 30})
		if el := time.Since(t0); el > 5*time.Second {
			t.Errorf("%s shadow: took %v", sh, el)
		}
		if p := px[(200*400+200)*4:]; p[0] <= 100 || p[1] >= 100 {
			t.Errorf("%s shadow: the node still paints, got %v", sh, p[:3])
		}
	}
}

// A node still fading in that a later insert reflows glides to its new place too, without restarting its
// fade, and its slide outlives the fade until the glide has settled.
func TestFadingSiblingGlidesWhenReflowed(t *testing.T) {
	a := `{"type":"rectangle","id":"a","width":100,"height":100,"fill":"#ff0000"}`
	before, after := openJSON(t, column(a)), openJSON(t, column(a+`,{"type":"rectangle","id":"b","width":100,"height":100,"fill":"#0000ff"}`))
	r := &Renderer{}
	syncSlides(r, offline(t, before))
	fading := NewSlide("a", time.Now())
	fading.fresh = false
	startAt(fading, ago(500))
	r.Slides = append(r.Slides, fading, NewSlide("b", time.Now()))
	syncSlides(r, offline(t, after))
	s := slideByID(r, "a")
	if !s.fade || s.rest < 0.5 {
		t.Fatalf("fade %v rest %v", s.fade, s.rest)
	}
	restart := s.start.Add(time.Duration(s.rest * 1e9))
	_, dy, alpha := s.State(restart)
	if dy <= 49.9 || dy > 50.1 || alpha <= 0.9 {
		t.Errorf("dy %v alpha %v", dy, alpha)
	}
	startAt(s, ago(600))
	s.rest = 0.5
	if r.ExpireSlides() || len(r.Slides) != 2 {
		t.Error("the fade ending does not cut the glide short")
	}
	startAt(s, ago(800))
	r.Slides = []*Slide{s}
	if !r.ExpireSlides() || len(r.Slides) != 0 {
		t.Error("it ends once the glide has settled")
	}
}

// Two inserts in one sync move a fading sibling once; a far move keeps the slide until it has settled.
func TestBatchedInsertsShiftASiblingOnce(t *testing.T) {
	col := func(kids string) string {
		return `{"version":"2.19","children":[{"type":"frame","id":"f","x":0,"y":0,"width":400,"height":1000,"layout":"vertical",
			"justifyContent":"center","fill":"#ffffff","children":[` + kids + `]}]}`
	}
	rect := func(id string) string {
		return `{"type":"rectangle","id":"` + id + `","width":100,"height":100,"fill":"#ff0000"}`
	}
	var all []string
	for _, id := range []string{"a", "b", "c", "d", "e", "g", "h"} {
		all = append(all, rect(id))
	}
	r := &Renderer{}
	syncSlides(r, offline(t, openJSON(t, col(rect("a")))))
	fading := NewSlide("a", time.Now())
	fading.fresh = false
	startAt(fading, ago(500))
	r.Slides = append(r.Slides, fading)
	for _, id := range []string{"b", "c", "d", "e", "g", "h"} {
		r.Slides = append(r.Slides, NewSlide(id, time.Now()))
	}
	syncSlides(r, offline(t, openJSON(t, col(strings.Join(all, ",")))))
	s := slideByID(r, "a")
	if abs(s.from[1]-300) >= 0.1 {
		t.Fatalf("a moved up 300 once, got %v", s.from)
	}
	startAt(s, ago(780))
	s.rest = 0.5
	if r.ExpireSlides() || slideByID(r, "a") == nil {
		t.Error("a far glide is not cut short")
	}
	startAt(slideByID(r, "a"), ago(900))
	if !r.ExpireSlides() || slideByID(r, "a") != nil {
		t.Error("it ends once settled")
	}
}

// The first layout resolves each top-level layer against one shared index, so it grows linearly with the
// layer count.
func TestFirstLayoutIsLinearInTopLevelLayers(t *testing.T) {
	timeOf := func(n int) float64 {
		var kids []string
		for i := range n {
			kids = append(kids, fmt.Sprintf(`{"type":"rectangle","id":"r%d","x":%d,"y":%d,"width":4,"height":4,"fill":"#f00"}`, i, i%100*5, i/100*5))
		}
		d := openJSON(t, `{"version":"2.19","children":[`+strings.Join(kids, ",")+`]}`)
		r := &Renderer{}
		t0 := time.Now()
		r.Sync(offline(t, d))
		if len(r.Order) != n {
			t.Fatalf("%d layers, want %d", len(r.Order), n)
		}
		return time.Since(t0).Seconds()
	}
	timeOf(200)
	small, large := timeOf(1000), timeOf(4000)
	if large >= small*8+0.2 {
		t.Errorf("1000 top-level layers took %.3f s, 4000 took %.3f s", small, large)
	}
}
