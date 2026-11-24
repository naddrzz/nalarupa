// Export golden tests: every top-level node of the svg fixtures and the blank and mobile
// templates is exported as PNG at scales 1 and 2, and the decoded pixels are compared against
// the committed goldens (testdata/render/export). Format cases (png, jpeg at quality 1, 80 and 100,
// webp) and clamped scales 0.01 and 20 are compared against the committed files too.
package export_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/image/webp"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

var (
	testdata   = filepath.Join("..", "testdata")
	goldenDir  = filepath.Join(testdata, "render", "export")
	nodeDocs   = []string{"svg/effects.pen", "svg/gradients.pen", "svg/images.pen", "svg/shapes.pen", "blank.pen", "mobile.pen"}
	formatRuns = []struct{ doc, id string }{{"svg/effects.pen", "shadows"}, {"svg/images.pen", "imgs"}, {"svg/shapes.pen", "fills"}}
)

// formatCase is one export of a format run: every format at scale 1, three at the scale clamped up from
// 0.01, and the scale clamped down from 20 (big files) for the last run only.
type formatCase struct {
	format  string
	quality uint8
	scale   float64
}

func formatCases(last bool) []formatCase {
	cases := []formatCase{{"png", 0, 1}, {"jpeg", 1, 1}, {"jpeg", 80, 1}, {"jpeg", 100, 1}, {"webp", 0, 1},
		{"png", 0, 0.01}, {"jpeg", 80, 0.01}, {"webp", 0, 0.01}}
	if last {
		cases = append(cases, formatCase{"png", 0, 20}, formatCase{"webp", 0, 20})
	}
	return cases
}

// Tolerance allows a stated difference in decoded pixels.
type Tolerance struct {
	MaxDelta  int     `json:"maxDelta"`
	MaxShare  float64 `json:"maxShare"`
	MeanDelta float64 `json:"meanDelta"`
	Why       string  `json:"why"`
}

func tolerances(t *testing.T) map[string]Tolerance {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, "render", "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Tolerance map[string]Tolerance `json:"tolerance"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f.Tolerance
}

// dataDir is a fresh NALARUPA_DATA with the fixture icons in its asset cache.
func dataDir(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	fillDataDir(t, data)
	return data
}

// fillDataDir puts the fixture icons in the asset cache of the data folder data.
func fillDataDir(t *testing.T, data string) {
	t.Helper()
	icons := filepath.Join(data, "cache", "icons")
	if err := os.MkdirAll(icons, 0o777); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(testdata, "svg", "icons", "*.svg"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || os.WriteFile(filepath.Join(icons, filepath.Base(f)), b, 0o666) != nil {
			t.Fatal("cannot copy icon", f, err)
		}
	}
}

func openCtx(t *testing.T, path, data string, fonts *text.Fonts) *export.Ctx {
	t.Helper()
	d, err := doc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	assets := svg.NewAssets(filepath.Join(data, "cache"))
	assets.Online = false
	return &export.Ctx{Doc: d, Fonts: fonts, Assets: assets}
}

func topIDs(t *testing.T, path string) []string {
	t.Helper()
	d, err := doc.Load(path)
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

func stem(doc string) string { return strings.TrimSuffix(filepath.Base(doc), ".pen") }

func scaleName(s float64) string { return strconv.FormatFloat(s, 'g', -1, 64) }

// decode is a file as straight RGBA rows.
func decode(t *testing.T, b []byte, format string) (*image.NRGBA, error) {
	t.Helper()
	var img image.Image
	var err error
	switch format {
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(b))
	case "webp":
		img, err = webp.Decode(bytes.NewReader(b))
	default:
		img, err = png.Decode(bytes.NewReader(b))
	}
	if err != nil {
		return nil, err
	}
	if m, ok := img.(*image.NRGBA); ok {
		return m, nil
	}
	out := image.NewNRGBA(img.Bounds())
	draw.Draw(out, out.Rect, img, img.Bounds().Min, draw.Src)
	return out, nil
}

// compareFiles decodes both files and compares their size and pixels within tol, reporting the max channel
// delta, the mean delta, the count of differing pixels and the first differing pixel.
func compareFiles(t *testing.T, got, want []byte, format string, tol Tolerance) {
	t.Helper()
	g, err := decode(t, got, format)
	if err != nil {
		t.Fatalf("Go file does not decode: %v", err)
	}
	w, err := decode(t, want, format)
	if err != nil {
		t.Fatalf("Rust file does not decode: %v", err)
	}
	if g.Rect != w.Rect {
		t.Fatalf("size %v, want %v", g.Rect.Size(), w.Rect.Size())
	}
	maxDelta, differing, first, sum := 0, 0, -1, 0
	for i := 0; i < len(w.Pix); i += 4 {
		d := 0
		for c := range 4 {
			dc := abs(int(g.Pix[i+c]) - int(w.Pix[i+c]))
			d = max(d, dc)
			sum += dc
		}
		if d > 0 {
			differing++
			if first < 0 {
				first = i
			}
		}
		maxDelta = max(maxDelta, d)
	}
	if differing == 0 {
		return
	}
	pixels := len(w.Pix) / 4
	share := float64(differing) / float64(pixels)
	mean := float64(sum) / float64(len(w.Pix))
	width := w.Rect.Dx()
	msg := fmt.Sprintf("max channel delta %d, mean %.3f, %d of %d pixels differ (%.4f%%); first at (%d,%d): got %v, want %v",
		maxDelta, mean, differing, pixels, share*100, first/4%width, first/4/width, g.Pix[first:first+4], w.Pix[first:first+4])
	if maxDelta > tol.MaxDelta || share > tol.MaxShare || (tol.MeanDelta > 0 && mean > tol.MeanDelta) {
		t.Error(msg)
	} else {
		t.Log("within tolerance:", msg)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// golden reads the golden file at name from testdata.
func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fonts(t *testing.T) *text.Fonts {
	t.Helper()
	f := text.New(t.TempDir())
	f.Online = false
	return f
}

func TestNodePNGParity(t *testing.T) {
	tol := tolerances(t)
	f := fonts(t)
	for _, d := range nodeDocs {
		path := filepath.Join(testdata, filepath.FromSlash(d))
		for _, scale := range []float64{1, 2} {
			data := dataDir(t)
			ctx := openCtx(t, path, data, f)
			for _, id := range topIDs(t, path) {
				key := stem(d) + "." + id + "@" + scaleName(scale)
				t.Run(key, func(t *testing.T) {
					want := golden(t, key+".png")
					got, err := ctx.NodePNG(id, scale)
					if err != nil {
						t.Fatal(err)
					}
					compareFiles(t, got, want, "png", tol["export/"+key])
				})
			}
		}
	}
}

func TestFormatParity(t *testing.T) {
	tol := tolerances(t)
	f := fonts(t)
	for i, r := range formatRuns {
		path := filepath.Join(testdata, filepath.FromSlash(r.doc))
		data := dataDir(t)
		ctx := openCtx(t, path, data, f)
		for _, fc := range formatCases(i == len(formatRuns)-1) {
			ext := map[string]string{"png": "png", "jpeg": "jpg", "webp": "webp"}[fc.format]
			key := fmt.Sprintf("%s.%s.q%d@%s", stem(r.doc), r.id, fc.quality, scaleName(fc.scale))
			t.Run(key+"."+ext, func(t *testing.T) {
				want := golden(t, key+"."+ext)
				got, err := ctx.NodeImage(r.id, fc.format, fc.scale, fc.quality)
				if err != nil {
					t.Fatal(err)
				}
				compareFiles(t, got, want, fc.format, tol["export/"+key])
			})
		}
	}
}

// TestErrorParity checks the error strings of nodes that cannot be exported.
func TestErrorParity(t *testing.T) {
	f := fonts(t)
	cases := []struct{ doc, id string }{{"svg/shapes.pen", "nope"}, {"svg/shapes.pen", "basic/nope"}, {"disabled.pen", "hidden"}, {"disabled.pen", "r2"}}
	wants := map[string]string{}
	goldenFile := filepath.Join(goldenDir, "errors.json")
	b, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &wants); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		path := filepath.Join(testdata, filepath.FromSlash(c.doc))
		data := dataDir(t)
		key := stem(c.doc) + "." + c.id
		_, err := openCtx(t, path, data, f).NodePNG(c.id, 1)
		if err == nil || err.Error() != wants[key] {
			t.Errorf("%s: got error %v, want %q", key, err, wants[key])
		}
	}
}
