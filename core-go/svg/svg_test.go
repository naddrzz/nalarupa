package svg

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// noFonts is the measurer for emitter tests without text.
type noFonts struct{}

func (noFonts) Metrics(layout.TextStyle) layout.Metrics               { panic("no text here") }
func (noFonts) Width(string, layout.TextStyle) float64                { panic("no text here") }
func (noFonts) Wrap(string, float64, layout.TextStyle) []resolve.Line { panic("no text here") }

func node(ty, p string, r Rect) *resolve.RNode {
	return &resolve.RNode{ID: "n", Type: ty, P: jsonv.MustParse(p).(*jsonv.Object), X: r.X, Y: r.Y, W: r.W, H: r.H}
}

func offlineAssets(t *testing.T) *Assets {
	a := NewAssets(t.TempDir())
	a.Online = false
	return a
}

func TestSvgzInlinesDecompressedAndAGzipBombIsRefused(t *testing.T) {
	gz := func(b []byte) []byte {
		var out bytes.Buffer
		w, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
		w.Write(b)
		w.Close()
		return out.Bytes()
	}
	dir := t.TempDir()
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"/>`)
	small := filepath.Join(dir, "a.svgz")
	os.WriteFile(small, gz(svg), 0o666)
	uri, ok := dataURI(small)
	b64, found := strings.CutPrefix(uri, "data:image/svg+xml;base64,")
	if !ok || !found {
		t.Fatal(uri)
	}
	if b, _ := base64.StdEncoding.DecodeString(b64); !bytes.Equal(b, svg) {
		t.Fatalf("%q", b)
	}
	bomb := filepath.Join(dir, "bomb.svgz")
	os.WriteFile(bomb, gz(bytes.Repeat([]byte(" "), maxSVGZBytes+1)), 0o666)
	if st, _ := os.Stat(bomb); st.Size() >= 2<<20 {
		t.Fatal("bomb too big", st.Size())
	}
	if _, ok := dataURI(bomb); ok {
		t.Fatal("a gzip bomb is inlined")
	}
}

// An icon still downloading draws nothing, like a remote image that has not arrived; only an icon that
// does not exist gets the dashed "missing" box.
func TestALoadingIconDrawsNoMissingBox(t *testing.T) {
	a := offlineAssets(t)
	n := node("icon", `{"icon": "zap", "library": "lucide", "fill": "#cc0000"}`, Rect{0, 0, 24, 24})
	_, tag := iconURL(a.IconBase, "lucide", "zap", 200)
	key := tag + "/zap"
	a.loading[key] = true
	s := New(noFonts{}, a, "")
	s.Node(n)
	if out := s.Finish(nil); strings.Contains(out, "stroke-dasharray") {
		t.Fatal("a loading icon draws no missing box:", out)
	}
	delete(a.loading, key)
	s = New(noFonts{}, a, "")
	s.Node(n)
	if out := s.Finish(nil); !strings.Contains(out, "stroke-dasharray") {
		t.Fatal("a missing icon keeps its dashed box:", out)
	}
}

// An image an SVG export leaves out paints nothing: its stand-in pixel is fully transparent.
func TestLeftOutImageIsATransparentPixel(t *testing.T) {
	b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(LeftOut, "data:image/png;base64,"))
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 1 || b.Dy() != 1 {
		t.Fatal(b)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("the left-out stand-in must not paint")
	}
}

// Web images are cached with an extension sniffed from their bytes; old extension-less files are migrated.
func TestCachedWebImagesGetAnExtension(t *testing.T) {
	dir := t.TempDir()
	a := NewAssets(dir)
	a.Online = false
	url := "https://example.com/photo"
	legacy := filepath.Join(dir, "images", fmt.Sprintf("%016x", FxHash(url)))
	os.WriteFile(legacy, []byte("\x89PNG\r\n\x1a\n rest"), 0o666)
	path, ok := a.Image(url, "")
	if !ok || !strings.HasSuffix(path, ".png") {
		t.Fatal(path, ok)
	}
	if exists(legacy) || !exists(path) {
		t.Fatal("legacy file not migrated")
	}
	if again, _ := a.Image(url, ""); again != path {
		t.Fatal(again)
	}
	if imageExt([]byte{0xFF, 0xD8, 0xFF}) != "jpg" {
		t.Fatal("jpeg")
	}
}

// A web image that is not cached downloads in the background: Image answers at once (nothing drawn, not
// "unavailable") and the file is used once it lands. A failed download is not retried.
func TestWebImagesDownloadInTheBackground(t *testing.T) {
	release := make(chan struct{})
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("User-Agent") != userAgent {
			t.Error("user agent", r.Header.Get("User-Agent"))
		}
		<-release
		w.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	a := NewAssets(dir)
	ready := make(chan bool, 1)
	a.OnReady = func(key string, ok bool) { ready <- ok }
	url := srv.URL + "/a.png"
	started := time.Now()
	if _, ok := a.Image(url, ""); ok || !a.Loading(url) {
		t.Fatal("a missing web image is loading")
	}
	if time.Since(started) > time.Second {
		t.Fatal("the call waited for the download")
	}
	if _, ok := a.Image(url, ""); ok || !a.Loading(url) {
		t.Fatal("asking again keeps it loading")
	}
	close(release)
	ok := <-ready
	if !ok || hits.Load() != 1 {
		t.Fatal("one successful download", ok, hits.Load())
	}
	a.Downloaded(url, ok)
	if a.Loading(url) || a.Generation != 1 {
		t.Fatal("downloaded", a.Generation)
	}
	if p, ok := a.Image(url, ""); !ok || p != strings.ReplaceAll(filepath.Join(dir, "images", fmt.Sprintf("%016x.png", FxHash(url))), `\`, "/") {
		t.Fatal(p, ok)
	}
	missing := "http://127.0.0.1:1/b.png"
	a.Downloaded(missing, false)
	if _, ok := a.Image(missing, ""); ok || a.Loading(missing) {
		t.Fatal("a failed download is not retried")
	}
}

// A document with many missing assets does not start a download for each: past maxDownloads the rest wait.
func TestDownloadsRunAtMostMaxAtOnce(t *testing.T) {
	a := offlineAssets(t)
	hold := make(chan struct{})
	var ended sync.WaitGroup
	ended.Add(maxDownloads + 1)
	for i := range maxDownloads + 3 {
		a.spawn(fmt.Sprint("k", i), func() bool {
			<-hold
			ended.Done()
			return true
		})
	}
	if a.running != maxDownloads || len(a.queue) != 3 {
		t.Fatal(a.running, len(a.queue))
	}
	for i := range maxDownloads + 3 {
		if !a.Loading(fmt.Sprint("k", i)) {
			t.Fatal("queued keys count as loading")
		}
	}
	a.Downloaded("k0", true)
	if a.running != maxDownloads || len(a.queue) != 2 {
		t.Fatal("an ended download frees a slot for the next", a.running, len(a.queue))
	}
	close(hold)
	ended.Wait()
}

// Without a server a remote image and an icon stay loading and draw nothing (no placeholder).
func TestMissingAssetsWithoutAServerAreLoading(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	dead := srv.URL
	srv.Close()
	a := NewAssets(t.TempDir())
	a.IconBase = dead
	done := make(chan string, 2)
	a.OnReady = func(key string, ok bool) {
		if ok {
			t.Error("a download from no server succeeded:", key)
		}
		done <- key
	}
	frame := node("frame", `{"layout": "none"}`, Rect{0, 0, 100, 100})
	img := node("rectangle", `{"fill": {"type": "image", "url": "`+dead+`/x.png"}}`, Rect{0, 0, 50, 50})
	img.ID = "img"
	icon := node("icon", `{"icon": "zap", "fill": "#c00"}`, Rect{60, 0, 24, 24})
	icon.ID = "icon"
	frame.Kids = []*resolve.RNode{img, icon}
	s := New(noFonts{}, a, "")
	s.Node(frame)
	out := s.Finish(nil)
	if strings.Contains(out, "<pattern") || strings.Contains(out, "stroke-dasharray") || strings.Contains(out, "#808080") {
		t.Fatal("a loading asset draws nothing:", out)
	}
	if !a.Loading(dead+"/x.png") || !a.IconLoading("lucide", "zap", 200) {
		t.Fatal("both keys are loading")
	}
	for range 2 {
		a.Downloaded(<-done, false)
	}
	s = New(noFonts{}, a, "")
	s.Node(frame)
	if out := s.Finish(nil); !strings.Contains(out, "#808080") || !strings.Contains(out, "stroke-dasharray") {
		t.Fatal("after failing they draw as unavailable:", out)
	}
}

// An image declaring more than MaxImagePixels is refused from its header alone, like a missing file.
func TestOversizedImagesAreRefused(t *testing.T) {
	dir := t.TempDir()
	a := NewAssets(dir)
	be32 := func(v uint32) []byte { return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
	pngh := func(w, h uint32) []byte {
		return append(append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), be32(w)...), be32(h)...)
	}
	gifh := func(w, h uint16) []byte { return append([]byte("GIF89a"), byte(w), byte(w>>8), byte(h), byte(h>>8)) }
	// SOI, an APP0 segment, then SOF0 (precision 8, height, width).
	jpgh := func(w, h uint16) []byte {
		return append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 4, 0, 0, 0xFF, 0xC0, 0, 11, 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w)}, 1, 1, 0x11, 0)
	}
	if w, h, ok := imageSize(bytes.NewReader(jpgh(640, 480))); !ok || w != 640 || h != 480 {
		t.Fatal(w, h, ok)
	}
	cases := []struct {
		name string
		b    []byte
		ok   bool
	}{{"a.png", pngh(30000, 30000), false}, {"b.png", pngh(8000, 8000), true}, {"c.gif", gifh(60000, 60000), false}, {"d.jpg", jpgh(20000, 20000), false}, {"e.jpg", jpgh(640, 480), true}}
	for _, c := range cases {
		os.WriteFile(filepath.Join(dir, c.name), c.b, 0o666)
		if _, ok := a.Image(c.name, dir); ok != c.ok {
			t.Error(c.name)
		}
		if _, ok := a.Image("data:image/png;base64,"+base64.StdEncoding.EncodeToString(c.b), ""); ok != c.ok {
			t.Error(c.name, "as data url")
		}
		var pct strings.Builder
		for _, b := range c.b {
			fmt.Fprintf(&pct, "%%%02X", b)
		}
		if _, ok := a.Image("data:image/png,"+pct.String(), ""); ok != c.ok {
			t.Error(c.name, "as percent-encoded data url")
		}
	}
	// As the data-url crate: the body ends at the fragment, and control characters around the url are trimmed.
	over := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngh(30000, 30000))
	for _, u := range []string{over + "#frag", over + "", " " + over + "#frag"} {
		if _, ok := a.Image(u, ""); ok {
			t.Errorf("%q is over the limit", u)
		}
	}
	if _, ok := decodeDataURL("data:image/png#x,AAAA"); ok {
		t.Error("a comma inside the fragment is no body")
	}
	// Bytes that only start like a PNG are not read as one.
	bad := pngh(30000, 30000)
	bad[5] = 'X'
	if _, _, ok := imageSize(bytes.NewReader(bad)); ok {
		t.Fatal("not a PNG")
	}
}

// An SVG image nested thousands of levels deep is unavailable, whether base64, percent-encoded or a file.
func TestDeeplyNestedSvgImagesAreUnavailable(t *testing.T) {
	deep := func(n int) string {
		return `<?xml version="1.0"?><!-- <g> --><svg xmlns="http://www.w3.org/2000/svg" width="8" height="8">` + strings.Repeat(`<g id="a>">`, n) + `<rect width="5" height="5"/><path d="M0 0"/>` + strings.Repeat("</g>", n) + `</svg>`
	}
	a := offlineAssets(t)
	b64 := func(s string) string {
		return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(s))
	}
	pct := func(s string) string {
		return "data:image/svg+xml," + strings.NewReplacer("%", "%25", "<", "%3C", ">", "%3E").Replace(s)
	}
	for _, c := range []struct {
		url string
		ok  bool
	}{{b64(deep(100)), true}, {b64(deep(6000)), false}, {pct(deep(100)), true}, {pct(deep(6000)), false}} {
		if _, ok := a.Image(c.url, ""); ok != c.ok {
			t.Error(c.url[:40], ok)
		}
	}
	file := filepath.Join(t.TempDir(), "deep.svg")
	os.WriteFile(file, []byte(deep(6000)), 0o666)
	if _, ok := a.Image(file, ""); ok {
		t.Fatal("a deep file is unavailable")
	}
	// The verdict is stamped with the file's size and time: a rewritten file is scanned again.
	os.WriteFile(file, []byte(deep(100)), 0o666)
	if _, ok := a.Image(file, ""); !ok {
		t.Fatal("a shallow file is fine")
	}
	// Binary payloads are not scanned as markup, whatever their MIME type.
	if _, ok := a.Image("data:application/octet-stream;base64,"+base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n<g><g>")), ""); !ok {
		t.Fatal("binary payload")
	}
	// usvg inflates gzip data, so an SVGZ payload is measured decompressed, at the top level and nested.
	gz := func(s string) []byte {
		var out bytes.Buffer
		w := gzip.NewWriter(&out)
		w.Write([]byte(s))
		w.Close()
		return out.Bytes()
	}
	for _, c := range []struct {
		svg string
		ok  bool
	}{{deep(100), true}, {deep(6000), false}} {
		if _, ok := a.Image("data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString(gz(c.svg)), ""); ok != c.ok {
			t.Error("svgz image", ok)
		}
		// The refusal comes before usvg parses anything, so it needs no options.
		if !c.ok {
			if _, ok := ImageHrefResolver().ResolveData("image/svg+xml", gz(c.svg), nil); ok {
				t.Error("nested deep svgz is resolved")
			}
		}
	}
	if _, ok := ImageHrefResolver().ResolveData("image/svg+xml", []byte{0x1f, 0x8b, 0}, nil); ok {
		t.Error("broken gzip is resolved")
	}
}

// Export area: box + outer stroke + drop shadow (offset ± 3σ, σ = blur/2), not inner shadows.
func TestExportVisualBounds(t *testing.T) {
	n := node("frame", `{"stroke": "#000", "strokeWidth": 2, "strokeAlignment": "outer",
		"effect": [{"type": "shadow", "offset": {"x": 0, "y": 4}, "blur": 8}, {"type": "shadow", "shadowType": "inner", "blur": 40}]}`, Rect{0, 0, 100, 50})
	if got := VisualBounds(n); got != (Rect{-14, -10, 128, 78}) {
		t.Fatal(got)
	}
	if got := VisualBounds(node("rectangle", `{}`, Rect{5, 6, 7, 8})); got != (Rect{5, 6, 7, 8}) {
		t.Fatal(got)
	}
}

// Lines past the box of a fixed-size text are painted, so they are exported.
func TestExportVisualBoundsIncludeOverflowingText(t *testing.T) {
	text := func(p string) *resolve.RNode {
		n := node("text", p, Rect{100, 100, 120, 40})
		for _, s := range []string{"a", "b", "c", "d"} {
			n.Lines = append(n.Lines, resolve.Line{Text: s, Width: 50, Soft: true})
		}
		n.LineH = 29
		return n
	}
	grow := `{"textGrowth": "fixed-width-height"}`
	if got := VisualBounds(text(grow)); got != (Rect{100, 100, 120, 116}) {
		t.Fatal(got)
	}
	if got := extent(text(grow)); got != (Rect{100, 100, 120, 116}) {
		t.Fatal(got)
	}
	if got := VisualBounds(text(`{"textGrowth": "fixed-width-height", "textAlignVertical": "bottom"}`)); got != (Rect{100, 24, 120, 116}) {
		t.Fatal(got)
	}
	if got := VisualBounds(text(`{"textGrowth": "fixed-width-height", "textAlignVertical": "middle"}`)); got != (Rect{100, 62, 120, 116}) {
		t.Fatal(got)
	}
}

// Rotated and flipped nodes export the box they paint, not their unrotated box.
func TestExportVisualBoundsFollowRotationAndFlips(t *testing.T) {
	rot := func(p string) Rect { return VisualBounds(node("rectangle", p, Rect{340, 40, 100, 60})) }
	for _, c := range []struct {
		p    string
		want Rect
	}{{`{"rotation": 90}`, Rect{340, -60, 60, 100}}, {`{"flipX": true}`, Rect{240, 40, 100, 60}}, {`{"flipY": true}`, Rect{340, -20, 100, 60}}} {
		if got := rot(c.p); got != c.want {
			t.Error(c.p, got)
		}
	}
	r := rot(`{"rotation": 30}`)
	c, s := math.Cos(toRad(30)), math.Sin(toRad(30))
	if math.Abs(r.W-(100*c+60*s)) > 1e-5 || math.Abs(r.H-(100*s+60*c)) > 1e-5 || math.Abs(r.X-340) > 1e-5 || math.Abs(r.Y-(40-100*s)) > 1e-5 {
		t.Fatal(r)
	}
}

// A blurred frame's filter region covers its children where they paint.
func TestExtentFollowsChildrenRotationAndFlips(t *testing.T) {
	f := node("frame", `{}`, Rect{100, 100, 100, 100})
	f.Kids = []*resolve.RNode{node("rectangle", `{"flipX": true}`, Rect{139, 138, 112, 130})}
	// flipX mirrors about the child's left edge: it paints 27..139 instead of 139..251.
	if got := extent(f); got != (Rect{27, 100, 173, 168}) {
		t.Fatal(got)
	}
	f.Kids[0] = node("rectangle", `{"rotation": 180}`, Rect{139, 138, 112, 130})
	if got := extent(f); got != (Rect{27, 8, 173, 192}) {
		t.Fatal(got)
	}
}

func TestColorsAndBBox(t *testing.T) {
	if c, a, ok := Color("#fff"); c != "#ffffff" || a != 1 || !ok {
		t.Fatal(c, a)
	}
	if c, a, _ := Color("#00000080"); c != "#000000" || math.Abs(a-0.502) > 0.01 {
		t.Fatal(c, a)
	}
	for _, bad := range []string{"$missing", "red", "#fff8"} {
		if _, _, ok := Color(bad); ok {
			t.Error(bad, "is transparent")
		}
	}
	if b, ok := PathBBox("M10 20 L30 60 Z"); !ok || b != (Rect{10, 20, 20, 40}) {
		t.Fatal(b)
	}
	// Tight bounds: the control points (y = 100) are not reached by the curve (max y = 75).
	if b, _ := PathBBox("M0 0 C0 100 100 100 100 0"); math.Abs(b.Y) > 1e-9 || math.Abs(b.H-75) > 1e-9 {
		t.Fatal(b)
	}
	if _, ok := PathBBox("M0 0 L10 10 L"); ok {
		t.Fatal("a path with an error has no bounds")
	}
}

func TestCSSRadiusScaling(t *testing.T) {
	if r := Radii(node("rectangle", `{"cornerRadius": [30, 30, 0, 0]}`, Rect{0, 0, 100, 40})); r != [4]float64{30, 30, 0, 0} {
		t.Fatal(r)
	}
	if r := Radii(node("rectangle", `{"cornerRadius": 50}`, Rect{0, 0, 100, 40})); r != [4]float64{20, 20, 20, 20} {
		t.Fatal(r)
	}
}

func TestShapes(t *testing.T) {
	if d := polygonD(node("polygon", `{}`, Rect{0, 0, 10, 10})); d != "" {
		t.Fatal(d)
	}
	if d := polygonD(node("polygon", `{"polygonCount": 3, "cornerRadius": 2}`, Rect{0, 0, 10, 10})); strings.Count(d, "A") != 3 {
		t.Fatal(d)
	}
	// A huge count from the document is capped instead of allocating or stalling.
	for _, c := range []string{"1e4", "4294967296", "1e300"} {
		if d := polygonD(node("polygon", `{"polygonCount": `+c+`}`, Rect{0, 0, 10, 10})); strings.Count(d, "L") != 999 {
			t.Error(c)
		}
	}
	if g := Geometry(node("line", `{}`, Rect{5, 5, 20, 0})); g.D != "M5 5L25 5" || g.Closed {
		t.Fatal(g)
	}
	_, closed := pathGeometry(node("path", `{"geometry": "M0 0L10 0L10 10Z"}`, Rect{0, 0, 10, 10}))
	_, open := pathGeometry(node("path", `{"geometry": "M0 0L10 10"}`, Rect{0, 0, 10, 10}))
	if !closed || open {
		t.Fatal(closed, open)
	}
	// Flips mirror about the origin (top-left), after which rotation applies.
	if tf := transform(node("rectangle", `{"flipX": true, "rotation": 90}`, Rect{10, 0, 100, 50})); tf != "rotate(-90 10 0) matrix(-1 0 0 1 20 0)" {
		t.Fatal(tf)
	}
}

// svgtypes' SimplifyingPathParser tests (path.rs simple_tests), on the segments path geometry writes.
func TestPathParserLikeSvgtypes(t *testing.T) {
	str := func(d string) string {
		segs, failed := parsePath(d)
		var b strings.Builder
		for _, s := range segs {
			switch s.kind {
			case segMove:
				fmt.Fprintf(&b, "M%v %v ", s.x, s.y)
			case segLine:
				fmt.Fprintf(&b, "L%v %v ", s.x, s.y)
			case segCurve:
				fmt.Fprintf(&b, "C%v %v %v %v %v %v ", s.x1, s.y1, s.x2, s.y2, s.x, s.y)
			case segQuad:
				fmt.Fprintf(&b, "Q%v %v %v %v ", s.x1, s.y1, s.x, s.y)
			case segClose:
				b.WriteString("Z ")
			}
		}
		if failed {
			b.WriteString("error")
		}
		return strings.TrimSpace(b.String())
	}
	for _, c := range [][2]string{
		{"M 10 20 L 30 40 Z Z Z Z", "M10 20 L30 40 Z"},
		{"m 30 40 110 120 -20 -130", "M30 40 L140 160 L120 30"},
		{"M 30 40 S 171 45 180 155", "M30 40 C30 40 171 45 180 155"},
		{"M 30 40 C 16 137 171 45 100 90 S 171 45 180 155", "M30 40 C16 137 171 45 100 90 C29 135 171 45 180 155"},
		{"M 30 40 T 180 155", "M30 40 Q30 40 180 155"},
		{"M 30 40 Q 171 45 100 90 T 160 180", "M30 40 Q171 45 100 90 Q29 135 160 180"},
		{"M 30 40 Q 171 45 100 90 t 60 80", "M30 40 Q171 45 100 90 Q29 135 160 170"},
		{"M10-20l30.1.5.1-20z", "M10 -20 L40.1 -19.5 L40.2 -39.5 Z"},
		{"M 10 20 H 30 V 40 h 5 v -5 Z L 1 2", "M10 20 L30 20 L30 40 L35 40 L35 35 Z M10 20 L1 2"},
		{"M 10 20 L 30 40 L 50", "M10 20 L30 40 error"},
		{"M 0 0 Z 2", "M0 0 Z error"},
		{"L 20 30", "error"},
		{"M 1e2 .5e1 L 2em 3", "M100 5 error"},
		{"M 10 20 A 0 5 0 0 0 30 40", "M10 20 L30 40"},
	} {
		if got := str(c[0]); got != c[1] {
			t.Errorf("%q: %s, want %s", c[0], got, c[1])
		}
	}
	// An arc becomes cubics (kurbo); the values depend on the trigonometry by an ulp or so.
	segs, _ := parsePath("M 1 5 A 5 5 0 0 1 3 1 s 3 2 8 2")
	want := []seg{{segMove, 0, 0, 0, 0, 1, 5}, {segCurve, 1, 3.4262134833347355, 1.7409707866677877, 1.9442719099991592, 2.9999999999999996, 1.0000000000000004},
		{segCurve, 2.9999999999999996, 1.0000000000000004, 6, 3.0000000000000004, 11, 3.0000000000000004}}
	if len(segs) != len(want) {
		t.Fatal(segs)
	}
	for i, s := range segs {
		g, w := [6]float64{s.x1, s.y1, s.x2, s.y2, s.x, s.y}, [6]float64{want[i].x1, want[i].y1, want[i].x2, want[i].y2, want[i].x, want[i].y}
		for k := range g {
			if s.kind != want[i].kind || math.Abs(g[k]-w[k]) > 1e-12 {
				t.Fatal(i, s, want[i])
			}
		}
	}
}

// A line's box is flat: its gradient is written in user space, without float leftovers.
func TestGradientsOnAFlatBoxStayInvertible(t *testing.T) {
	s := New(noFonts{}, offlineAssets(t), "")
	grad := func(ty string, rotation float64) *jsonv.Object {
		return jsonv.MustParse(fmt.Sprintf(`{"gradientType": %q, "rotation": %v, "colors": [{"color": "#f00", "position": 0}, {"color": "#00f", "position": 1}]}`, ty, rotation)).(*jsonv.Object)
	}
	for _, c := range []struct {
		ty  string
		rot float64
		b   Rect
	}{{"linear", 90, Rect{20, 50, 160, 0}}, {"linear", 0, Rect{100, 20, 0, 160}}, {"radial", 90, Rect{20, 50, 160, 0}}} {
		if _, ok := s.gradient(grad(c.ty, c.rot), c.b); !ok {
			t.Fatal(c)
		}
	}
	defs := s.defs.String()
	if strings.Contains(defs, "gradientTransform") || !strings.Contains(defs, `x1="180" y1="50" x2="20" y2="50"`) || !strings.Contains(defs, `cx="100" cy="50" r="80"`) {
		t.Fatal(defs)
	}
	s.defs.Reset()
	s.gradient(grad("radial", 90), Rect{20, 20, 160, 80})
	if defs := s.defs.String(); !strings.Contains(defs, `gradientTransform="matrix(0 -40 80 0 100 60)"`) || strings.Contains(defs, "e-") {
		t.Fatal(defs)
	}
}

// The cover built by Above carries the ancestor's layer blur, and a sibling with a background blur in it
// sees the sibling painted before it.
func TestAboveKeepsAncestorBlurAndBackdrop(t *testing.T) {
	s := New(noFonts{}, offlineAssets(t), "")
	b := Rect{0, 0, 100, 100}
	leaf := func(id, p string) *resolve.RNode {
		n := node("rectangle", p, b)
		n.ID = id
		return n
	}
	root := node("frame", `{"effect": {"type": "blur", "radius": 8}}`, b)
	root.Kids = []*resolve.RNode{leaf("s", `{"fill": "#0f0"}`), leaf("a", `{"fill": "#f00"}`), leaf("g", `{"fill": "#00f8", "effect": {"type": "background_blur", "radius": 4}}`)}
	s.Above(root, "s")
	out := s.Finish(nil)
	if !strings.Contains(out, `feGaussianBlur stdDeviation="4"`) || !strings.Contains(out, `<g filter="url(#f`) {
		t.Fatal("ancestor blur:", out)
	}
	_, rest, _ := strings.Cut(out, `<g id="`)
	a, _, _ := strings.Cut(rest, `"`)
	if a == "" || !strings.Contains(out, `<use xlink:href="#`+a+`"/>`) {
		t.Fatal("backdrop sees the earlier sibling:", out)
	}
	if strings.Contains(out, "#00ff00") || strings.Contains(out, "#0f0") {
		t.Fatal("the sliding node is left out:", out)
	}
}

func TestPremultipliedStops(t *testing.T) {
	s := stopTags([]stop{{0, [4]float64{1, 0, 0, 1}}, {1, [4]float64{0, 0, 1, 0}}})
	if strings.Count(s, "<stop") != 9 || strings.Contains(s, "#7f") || strings.Count(s, "#ff0000") != 8 {
		t.Fatal("no grey fringe toward transparent:", s)
	}
	if c := sample([]stop{{0.2, [4]float64{1, 1, 1, 1}}, {0.8, [4]float64{0, 0, 0, 1}}}, 0); c != [4]float64{1, 1, 1, 1} {
		t.Fatal(c)
	}
}

type painter struct {
	got      string
	outline  Outline
	err      error
	backdrop bool
	behind   *Backdrop
}

func (p *painter) UsesBackdrop(string) bool { return p.backdrop }

func (p *painter) PNG(src string, _ *jsonv.Object, w, h float64, o Outline, _ func(string) (*Pixmap, bool), b *Backdrop) (string, error) {
	p.got, p.outline, p.behind = src, o, b
	return "AAAA", p.err
}

// A shader reading `@backdrop` gets what its frame painted so far, with open groups closed, rendered at the
// asked size; others get none.
func TestShaderBackdrop(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "glass.frag"), []byte("void main() {}"), 0o666)
	frame := jsonv.MustParse(`{"id": "f", "type": "frame", "fill": "#00ff00", "opacity": 0.5}`).(*jsonv.Object)
	f := &resolve.RNode{ID: "f", Type: "frame", P: frame, X: 0, Y: 0, W: 40, H: 20}
	f.Kids = []*resolve.RNode{node("rectangle", `{"fill": {"type": "shader", "url": "glass.frag"}}`, Rect{10, 0, 20, 20})}
	for _, uses := range []bool{false, true} {
		p := &painter{backdrop: uses}
		s := New(noFonts{}, offlineAssets(t), dir)
		s.Shader = p
		s.Node(f)
		if (p.behind != nil) != uses {
			t.Fatalf("uses %v: backdrop %v", uses, p.behind)
		}
	}
	p := &painter{backdrop: true}
	s := New(noFonts{}, offlineAssets(t), dir)
	s.Shader = p
	s.Node(f)
	b := p.behind
	if !strings.HasPrefix(b.SVG, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="20" height="20" viewBox="10 0 20 20"><rect x="10" y="0" width="20" height="20" fill="#f6f6f6"/><defs>`) ||
		!strings.HasSuffix(b.SVG, `</g></svg>`) || b.Key != FxHash(b.SVG) {
		t.Fatal(b.SVG)
	}
	pm, ok := b.Paint(4, 4)
	if !ok || pm.Width() != 4 || pm.Height() != 4 {
		t.Fatal("no backdrop pixels")
	}
	// Half-transparent green over #f6f6f6.
	if c, _ := pm.Pixel(1, 1); c.Red() != 123 || c.Green() != 251 || c.Blue() != 123 || c.Alpha() != 255 {
		t.Fatal(c)
	}
}

// A shader fill without a painter, a missing file or a failing painter draws the red error hatch; a
// working painter's PNG fills the node box.
func TestShaderFills(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "wave.frag"), []byte("void main() {}"), 0o666)
	hatch := `<pattern id="p2" patternUnits="userSpaceOnUse" width="1" height="1" patternTransform="translate(10 20) rotate(135) scale(5)"><rect x="0.4" width="0.2" height="1" fill="#ff0000"/></pattern>`
	n := node("rectangle", `{"fill": {"type": "shader", "url": "wave.frag", "opacity": 0.5}, "cornerRadius": 4}`, Rect{10, 20, 30, 40})
	for _, c := range []struct {
		name string
		p    ShaderPainter
		url  string
		want string
	}{
		{"no painter", nil, "wave.frag", hatch},
		{"missing file", &painter{}, "nope.frag", hatch},
		{"failing painter", &painter{err: errors.New("does not compile")}, "wave.frag", hatch},
		{"painter", &painter{}, "wave.frag", `<pattern id="p2" patternUnits="userSpaceOnUse" x="10" y="20" width="30" height="40"><image width="30" height="40" preserveAspectRatio="none" xlink:href="data:image/png;base64,AAAA"/></pattern>`},
	} {
		jsonv.Obj(jsonv.Get(n.P, "fill")).Set("url", c.url)
		s := New(noFonts{}, offlineAssets(t), dir)
		s.Shader = c.p
		s.Node(n)
		out := s.Finish(nil)
		if !strings.Contains(out, c.want) || !strings.Contains(out, `fill="url(#p2)" fill-opacity="0.5"`) {
			t.Errorf("%s: %s", c.name, out)
		}
	}
	p := &painter{}
	s := New(noFonts{}, offlineAssets(t), dir)
	s.Shader = p
	s.Node(n)
	if p.got != "void main() {}" || p.outline.Kind != OutlineRRect || p.outline.Radii != [4]float64{4, 4, 4, 4} {
		t.Fatal(p.got, p.outline)
	}
}

// A mesh gradient rasterises into a PNG pattern; an invalid one paints nothing and takes no id.
func TestMeshGradient(t *testing.T) {
	o := jsonv.MustParse(`{"columns": 2, "rows": 2, "points": [[0, 0], [1, 0], [0, 1], [1, 1]], "colors": ["#ff0000", "#ff0000", "#0000ff", "#0000ff"]}`).(*jsonv.Object)
	pm := MeshGradient(o, 8, 8)
	if pm == nil {
		t.Fatal("valid mesh")
	}
	// Red on top, blue at the bottom, opaque everywhere.
	last := len(pm.Pix) - 4
	if pm.Pix[0] < 240 || pm.Pix[2] > 15 || pm.Pix[last] > 15 || pm.Pix[last+2] < 240 || pm.Pix[3] != 255 || pm.Pix[last+3] != 255 {
		t.Fatal(pm)
	}
	for _, bad := range []string{`{"columns": 2, "rows": 2, "points": [[0, 0]], "colors": ["#f00"]}`, `{"columns": 1.5, "rows": 2, "points": [], "colors": []}`, `{"rows": 1}`} {
		if MeshGradient(jsonv.MustParse(bad).(*jsonv.Object), 8, 8) != nil {
			t.Error(bad)
		}
	}
}

// DecodeImage gives premultiplied pixels; an SVG is not a raster image.
func TestDecodeImage(t *testing.T) {
	pm, ok := DecodeImage(filepath.Join("..", "testdata", "svg", "tiny.png"))
	if !ok || pm.W != 4 || pm.H != 3 {
		t.Fatal(pm, ok)
	}
	// Pixel (3, 0) is NRGBA(180, 0, 200, 135): premultiplied, colour channels shrink with alpha.
	if p := pm.Pix[12:16]; p[3] != 135 || p[0] != premultiplyU8(180, 135) || p[2] != premultiplyU8(200, 135) {
		t.Fatal(p)
	}
	// Premultiplied as tiny-skia rounds: (1, 128) gives 1 where Go's image/draw gives 0.
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Pix = []uint8{1, 3, 255, 128}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	file := filepath.Join(t.TempDir(), "half.png")
	os.WriteFile(file, buf.Bytes(), 0o666)
	if pm, ok := DecodeImage(file); !ok || pm.Pix[0] != 1 || pm.Pix[1] != premultiplyU8(3, 128) || pm.Pix[2] != 128 || pm.Pix[3] != 128 {
		t.Fatal(pm, ok)
	}
	if _, ok := DecodeImage(filepath.Join("..", "testdata", "svg", "shallow.svg")); ok {
		t.Fatal("an SVG decodes")
	}
}

func TestInlineEmbedded(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "a.png")
	os.WriteFile(png, []byte("\x89PNG\r\n\x1a\nxx"), 0o666)
	deep := filepath.Join(dir, "deep.svg")
	os.WriteFile(deep, []byte(`<svg>`+strings.Repeat("<g>", 300)+strings.Repeat("</g>", 300)+`</svg>`), 0o666)
	e := Embedded{Tag: "tag:", Hrefs: []string{png, deep, filepath.Join(dir, "gone.png"), "data:image/png;base64,QQ=="}}
	got := InlineEmbedded(`<a href="tag:0"/><b href="tag:1"/><c href="tag:2"/><d href="tag:3"/>tag:9 tag:`, e)
	want := `<a href="data:image/png;base64,` + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nxx")) + `"/><b href="` + LeftOut + `"/><c href="` + LeftOut + `"/><d href="data:image/png;base64,QQ=="/>tag:9 tag:`
	if got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
}

func TestEsc(t *testing.T) {
	if got := Esc("a&b<c>d\"e'f\tg\nh\x01"); got != "a&amp;b&lt;c&gt;d&quot;e&apos;f\tgh" {
		t.Fatal(got)
	}
}

// A download that panics does not take the process down: it is reported as a failed one.
func TestPanickingDownloadIsReportedFailed(t *testing.T) {
	a := NewAssets(t.TempDir())
	ready := make(chan bool, 1)
	a.OnReady = func(key string, ok bool) { ready <- ok }
	a.spawn("boom", func() bool { panic("broken download") })
	select {
	case ok := <-ready:
		if ok {
			t.Fatal("a panicking download failed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OnReady never fired")
	}
}
