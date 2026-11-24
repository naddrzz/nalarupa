// Golden tests: every top-level node of each fixture in testdata/svg is exported and compared with the
// committed goldens (<fixture>.<id>.svg). Rasterised patterns (`data:image/png`) are compared by their
// decoded pixels, within ±1 per channel.
package svg_test

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// noText measures for the text-free fixtures.
type noText struct{}

func (noText) Metrics(layout.TextStyle) layout.Metrics { panic("svg goldens are text-free") }
func (noText) Width(string, layout.TextStyle) float64  { panic("svg goldens are text-free") }
func (noText) Wrap(string, float64, layout.TextStyle) []resolve.Line {
	panic("svg goldens are text-free")
}

// dataDir is a fresh NALARUPA_DATA for one export, with the fixture icons in its asset cache.
func dataDir(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	icons := filepath.Join(data, "cache", "icons")
	if err := os.MkdirAll(icons, 0o777); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join("..", "testdata", "svg", "icons", "*.svg"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || os.WriteFile(filepath.Join(icons, filepath.Base(f)), b, 0o666) != nil {
			t.Fatal("cannot copy icon", f, err)
		}
	}
	return data
}

func goExport(t *testing.T, fixture, id, data string, fonts layout.TextMeasurer) string {
	t.Helper()
	d, err := doc.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	assets := svg.NewAssets(filepath.Join(data, "cache"))
	assets.Online = false
	out, embedded, ok := svg.Export(d, nil, id, fonts, assets, nil)
	if !ok {
		t.Fatalf("%s: node %s not exported", fixture, id)
	}
	return svg.InlineEmbedded(out, embedded)
}

func topIDs(t *testing.T, fixture string) []string {
	t.Helper()
	d, err := doc.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range d.Children() {
		if id, ok := jsonv.Str(jsonv.Get(n, "id")); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

func TestParity(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("..", "testdata", "svg", "*.pen"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, fx := range fixtures {
		name := strings.TrimSuffix(filepath.Base(fx), ".pen")
		for _, id := range topIDs(t, fx) {
			t.Run(name+"/"+id, func(t *testing.T) {
				golden := filepath.Join("..", "testdata", "svg", name+"."+id+".svg")
				data := dataDir(t)
				b, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				sameSVG(t, goExport(t, fx, id, data, noText{}), string(b))
			})
		}
	}
}

var (
	dataPNG = regexp.MustCompile(`data:image/png;base64,[A-Za-z0-9+/=]*`)
	// number leaves the sign out: it stays literal text, so `-0` against `0` is a difference.
	number = regexp.MustCompile(`[0-9]+(\.[0-9]+)?`)
)

// sameSVG compares byte for byte, except PNG data URIs, which are compared by decoded pixels (±1), and
// numbers, which may differ in their last bits: trigonometry comes from Go's math package in Go and from the
// C library in Rust (the Windows CRT's sin, cos, tan, acos and atan2 differ from Go's on about one input
// in four), so coordinates derived from angles can be one ulp apart.
func sameSVG(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	g, w := dataPNG.ReplaceAllString(got, "data:image/png;base64,…"), dataPNG.ReplaceAllString(want, "data:image/png;base64,…")
	if g != w {
		if !sameUpToULPs(g, w) {
			t.Fatalf("SVG differs from Rust:\n%s", firstDiff(g, w))
		}
		t.Log("numbers differ in their last bits")
	}
	gp, wp := dataPNG.FindAllString(got, -1), dataPNG.FindAllString(want, -1)
	for i := range gp {
		if gp[i] != wp[i] {
			if err := samePixels(gp[i], wp[i]); err != nil {
				t.Errorf("PNG data URI %d: %v", i, err)
			} else {
				t.Logf("PNG data URI %d: other bytes, same pixels (±1)", i)
			}
		}
	}
}

// sameUpToULPs reports whether a and b differ only in numbers within 1e-12 of each other (relative, or
// absolute near zero), with the same signs.
func sameUpToULPs(a, b string) bool {
	an, bn := number.FindAllStringIndex(a, -1), number.FindAllStringIndex(b, -1)
	if len(an) != len(bn) {
		return false
	}
	ai, bi := 0, 0
	for k := range an {
		if a[ai:an[k][0]] != b[bi:bn[k][0]] {
			return false
		}
		x, errx := strconv.ParseFloat(a[an[k][0]:an[k][1]], 64)
		y, erry := strconv.ParseFloat(b[bn[k][0]:bn[k][1]], 64)
		if errx != nil || erry != nil || math.Abs(x-y) > 1e-12*math.Max(1, math.Max(math.Abs(x), math.Abs(y))) {
			return false
		}
		ai, bi = an[k][1], bn[k][1]
	}
	return a[ai:] == b[bi:]
}

func decodePNG(uri string) (image.Image, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/png;base64,"))
	if err != nil {
		return nil, err
	}
	return png.Decode(bytes.NewReader(b))
}

func samePixels(got, want string) error {
	g, err := decodePNG(got)
	if err != nil {
		return err
	}
	w, err := decodePNG(want)
	if err != nil {
		return err
	}
	if g.Bounds() != w.Bounds() {
		return fmt.Errorf("size %v, want %v", g.Bounds(), w.Bounds())
	}
	b := w.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			gc, wc := color.NRGBAModel.Convert(g.At(x, y)).(color.NRGBA), color.NRGBAModel.Convert(w.At(x, y)).(color.NRGBA)
			for _, d := range []int{int(gc.R) - int(wc.R), int(gc.G) - int(wc.G), int(gc.B) - int(wc.B), int(gc.A) - int(wc.A)} {
				if d < -1 || d > 1 {
					return fmt.Errorf("pixel (%d, %d) is %v, want %v", x, y, gc, wc)
				}
			}
		}
	}
	return nil
}

func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(i-120, 0)
	cut := func(s string) string { return s[from:min(i+120, len(s))] }
	return fmt.Sprintf("at byte %d\n  go:   …%s…\n  rust: …%s…", i, cut(a), cut(b))
}
