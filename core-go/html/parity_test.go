// Golden tests: every top-level node of each fixture in testdata/html is exported and compared with the
// committed goldens (<fixture>.<id>.html). PNG data URIs (mesh gradients, shaders) are compared by decoded
// pixels within ±1 per channel.
package html_test

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
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/html"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

var fixtureDir = filepath.Join("..", "testdata", "html")

// dataDir is a fresh NALARUPA_DATA for one export, with the svg fixture icons in its asset cache.
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

// goExport exports id for the text-free fixtures.
func goExport(t *testing.T, fixture, id, data string) string {
	t.Helper()
	d, err := doc.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	assets := svg.NewAssets(filepath.Join(data, "cache"))
	assets.Online = false
	out, err := html.Document(&export.Ctx{Doc: d, Assets: assets}, []string{id}, jsonv.NewObject())
	if err != nil {
		t.Fatal(err)
	}
	return out
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
	fixtures, err := filepath.Glob(filepath.Join(fixtureDir, "*.pen"))
	if err != nil || len(fixtures) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, fx := range fixtures {
		name := strings.TrimSuffix(filepath.Base(fx), ".pen")
		for _, id := range topIDs(t, fx) {
			t.Run(name+"/"+id, func(t *testing.T) {
				golden := filepath.Join(fixtureDir, name+"."+id+".html")
				data := dataDir(t)
				b, err := os.ReadFile(golden)
				if err != nil {
					t.Fatal(err)
				}
				sameHTML(t, goExport(t, fx, id, data), string(b), 1, name == "vectors")
			})
		}
	}
}

var (
	dataPNG = regexp.MustCompile(`data:image/png;base64,[A-Za-z0-9+/=]*`)
	// number leaves the sign out: it stays literal text, so `-0` against `0` is a difference. A percent escape
	// (`%22` in an SVG data URI) is matched as its own token, so its digits never join the number after it.
	number = regexp.MustCompile(`%[0-9A-Fa-f]{2}|[0-9]+(\.[0-9]+)?`)
)

// sameHTML compares byte for byte, except PNG data URIs, which are compared by decoded pixels (within tol per
// channel), and, with ulps, numbers, which may differ in their last bits where they come from trigonometry
// (Go's math against the C library's, as in the SVG export).
func sameHTML(t *testing.T, got, want string, tol int, ulps bool) {
	t.Helper()
	if got == want {
		return
	}
	g, w := dataPNG.ReplaceAllString(got, "data:image/png;base64,…"), dataPNG.ReplaceAllString(want, "data:image/png;base64,…")
	if g != w {
		if !ulps || !sameUpToULPs(g, w) {
			t.Fatalf("HTML differs from Rust:\n%s", firstDiff(g, w))
		}
		t.Log("numbers differ in their last bits")
	}
	gp, wp := dataPNG.FindAllString(got, -1), dataPNG.FindAllString(want, -1)
	for i := range gp {
		if gp[i] != wp[i] {
			if err := samePixels(gp[i], wp[i], tol); err != nil {
				t.Errorf("PNG data URI %d: %v", i, err)
			} else {
				t.Logf("PNG data URI %d: other bytes, same pixels (±%d)", i, tol)
			}
		}
	}
}

// sameUpToULPs reports whether a and b differ only in numbers within 1e-12 of each other (relative, or
// absolute near zero), with the same signs. Percent escapes must be equal.
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
		ta, tb := a[an[k][0]:an[k][1]], b[bn[k][0]:bn[k][1]]
		if strings.HasPrefix(ta, "%") || strings.HasPrefix(tb, "%") {
			if ta != tb {
				return false
			}
			ai, bi = an[k][1], bn[k][1]
			continue
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

func samePixels(got, want string, tol int) error {
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
				if d < -tol || d > tol {
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
	from := max(i-160, 0)
	cut := func(s string) string { return s[from:min(i+160, len(s))] }
	return fmt.Sprintf("at byte %d\n  go:   …%s…\n  rust: …%s…", i, cut(a), cut(b))
}

func TestSameUpToULPs(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"x=0.30000000000000004px", "x=0.3px", true},
		{"x=0.31px", "x=0.3px", false},
		{"-0px", "0px", false},
		// The digits of %22 are not part of the number after it: 0.75 against 0.7500001 is no last-bit difference.
		{"x1=%220.75%22", "x1=%220.7500001%22", false},
		{"x1=%220.75%22", "x1=%220.7500000000000001%22", true},
		{"x1=%220.75%22", "x1=%230.75%22", false},
	} {
		if got := sameUpToULPs(c.a, c.b); got != c.want {
			t.Errorf("sameUpToULPs(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
