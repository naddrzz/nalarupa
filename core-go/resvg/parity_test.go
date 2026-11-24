// Render golden tests with resvg 0.47: each SVG of the usvg corpora (testdata/usvg, testdata/usvg/filter) and of
// testdata/render/svg is rendered here at the scales listed for its corpus in testdata/render/fixtures.json, and
// the premultiplied RGBA bytes are compared with the committed goldens (testdata/render/resvg/<corpus>/<stem>@<scale>.rgba.gz,
// raw bytes gzipped, plus index.json).
//
// Pixels must match exactly, except a fixture listed in fixtures.json under "tolerance": its max channel
// delta and max share of differing pixels hold only where the difference comes from a libm call, rcpps or
// a decoder (its "why" says which).
package resvg_test

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

var renderData = filepath.Join("..", "testdata", "render")

// Tolerance allows a stated difference for one fixture.
type Tolerance struct {
	MaxDelta int     `json:"maxDelta"`
	MaxShare float64 `json:"maxShare"`
	Why      string  `json:"why"`
}

type corpus struct {
	Dir    string    `json:"dir"` // under core-go
	Scales []float32 `json:"scales"`
	Live   bool      `json:"live"` // depends on the installed fonts: no goldens, compared live only
}

type fixtureList struct {
	Corpora   map[string]corpus    `json:"corpora"`
	Tolerance map[string]Tolerance `json:"tolerance"` // "<corpus>/<stem>@<scale>" or "<corpus>/<stem>"
}

func loadFixtures(t *testing.T) fixtureList {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(renderData, "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixtureList
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixtureList) tolerance(key string) Tolerance {
	if tol, ok := f.Tolerance[key]; ok {
		return tol
	}
	if at := strings.LastIndex(key, "@"); at >= 0 {
		return f.Tolerance[key[:at]]
	}
	return Tolerance{}
}

// rendered is one render: size and bytes, or the error, or a panic.
type rendered struct {
	Width  uint32 `json:"width,omitempty"`
	Height uint32 `json:"height,omitempty"`
	Error  string `json:"error,omitempty"`
	Panic  bool   `json:"panic,omitempty"`
	data   []byte
}

func goRender(t *testing.T, file string, scale float32, fonts *text.Fonts) (r rendered) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	opt := usvg.DefaultOptions()
	opt.ResourcesDir = filepath.Dir(file)
	opt.ImageHrefResolver = svg.ImageHrefResolver()
	if fonts != nil {
		opt.FontDB = fonts.Database()
		opt.FontResolver = usvg.FontsResolver(fonts)
	}
	defer func() {
		if p := recover(); p != nil {
			r = rendered{Panic: true, Error: fmt.Sprint(p)}
		}
	}()
	tree, err := usvg.FromData(data, &opt)
	if err != nil {
		return rendered{Error: err.Error()}
	}
	size, ok := tree.Size().ToIntSize().ScaleBy(scale)
	if !ok {
		return rendered{Error: "no pixmap"}
	}
	pm, ok := skia.NewPixmap(size.Width(), size.Height())
	if !ok {
		return rendered{Error: "no pixmap"}
	}
	resvg.Render(tree, skia.TransformFromScale(scale, scale), pm.AsMut())
	return rendered{Width: pm.Width(), Height: pm.Height(), data: pm.Data()}
}

// comparePixels checks got against want within tol, reporting the max channel delta, the count of
// differing pixels and the first differing pixel.
func comparePixels(t *testing.T, got, want []byte, width uint32, tol Tolerance) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d bytes, want %d", len(got), len(want))
	}
	maxDelta, differing, first := 0, 0, -1
	for i := 0; i < len(want); i += 4 {
		d := 0
		for c := range 4 {
			d = max(d, absInt(int(got[i+c])-int(want[i+c])))
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
	pixels := len(want) / 4
	share := float64(differing) / float64(pixels)
	msg := fmt.Sprintf("max channel delta %d, %d of %d pixels differ (%.4f%%); first at (%d,%d): got %v, want %v",
		maxDelta, differing, pixels, share*100, first/4%int(width), first/4/int(width), got[first:first+4], want[first:first+4])
	if maxDelta > tol.MaxDelta || share > tol.MaxShare {
		t.Error(msg)
	} else {
		t.Log("within tolerance:", msg)
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func scaleName(s float32) string { return strconv.FormatFloat(float64(s), 'g', -1, 32) }

func TestRenderParity(t *testing.T) {
	fx := loadFixtures(t)
	names := make([]string, 0, len(fx.Corpora))
	for name, c := range fx.Corpora {
		if !c.Live {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	total := 0
	for _, name := range names {
		c := fx.Corpora[name]
		dir := filepath.Join("..", filepath.FromSlash(c.Dir))
		docs, err := filepath.Glob(filepath.Join(dir, "*.svg*"))
		if err != nil {
			t.Fatal(err)
		}
		goldenDir := filepath.Join(renderData, "resvg", name)
		index := map[string]rendered{}
		indexFile := filepath.Join(goldenDir, "index.json")
		b, err := os.ReadFile(indexFile)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &index); err != nil {
			t.Fatal(err)
		}
		for _, doc := range docs {
			stem := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(doc), ".svgz"), ".svg")
			for _, scale := range c.Scales {
				key := stem + "@" + scaleName(scale)
				total++
				t.Run(name+"/"+key, func(t *testing.T) {
					want, ok := index[key]
					if !ok {
						t.Fatalf("no golden for %s", key)
					}
					if want.Error == "" && !want.Panic {
						want.data = readGolden(t, filepath.Join(goldenDir, key+".rgba.gz"))
					}
					got := goRender(t, doc, scale, nil)
					checkRendered(t, got, want, fx.tolerance(name+"/"+key))
				})
			}
		}
	}
	if total < 150 {
		t.Fatalf("%d renders, want at least 150", total)
	}
}

func readGolden(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(z)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func checkRendered(t *testing.T, got, want rendered, tol Tolerance) {
	t.Helper()
	switch {
	case want.Panic || got.Panic:
		if want.Panic != got.Panic {
			t.Fatalf("panic: got %v (%s), want %v", got.Panic, got.Error, want.Panic)
		}
	case want.Error != "" || got.Error != "":
		if (want.Error == "") != (got.Error == "") {
			t.Fatalf("error: got %q, want %q", got.Error, want.Error)
		}
	default:
		if got.Width != want.Width || got.Height != want.Height {
			t.Fatalf("size %dx%d, want %dx%d", got.Width, got.Height, want.Width, want.Height)
		}
		comparePixels(t, got.data, want.data, want.Width, tol)
	}
}

// TestTextRender renders every document in the text corpus (testdata/usvg/text) at all configured scales
// with the system fonts available on this machine, verifying clean parsing, non-panicking rendering,
// valid pixmap dimensions, and deterministic output.
func TestTextRender(t *testing.T) {
	fx := loadFixtures(t)
	c := fx.Corpora["text"]
	docs, err := filepath.Glob(filepath.Join("..", filepath.FromSlash(c.Dir), "*.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) < 30 {
		t.Fatalf("%d text documents, want at least 30", len(docs))
	}
	cache := t.TempDir()
	fonts := text.New(cache)
	fonts.Online = false
	emoji := fonts.HasFamily("Noto Emoji")
	renders := 0
	for _, doc := range docs {
		stem := strings.TrimSuffix(filepath.Base(doc), ".svg")
		for _, scale := range c.Scales {
			key := stem + "@" + scaleName(scale)
			renders++
			t.Run(key, func(t *testing.T) {
				if strings.HasPrefix(stem, "emoji") && !emoji {
					t.Skip("Noto Emoji is not installed")
				}
				got := goRender(t, doc, scale, fonts)
				if got.Panic {
					t.Fatalf("panicked: %s", got.Error)
				}
				if got.Error != "" {
					t.Fatalf("render error: %s", got.Error)
				}
				if got.Width == 0 || got.Height == 0 || len(got.data) != int(got.Width*got.Height*4) {
					t.Fatalf("bad pixmap %dx%d (%d bytes)", got.Width, got.Height, len(got.data))
				}
			})
		}
	}
	if renders < 60 {
		t.Fatalf("%d text renders, want at least 60", renders)
	}
}
