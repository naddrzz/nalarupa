// Render parity with resvg 0.47: each SVG of the usvg corpora (testdata/usvg, testdata/usvg/filter) and of
// testdata/render/svg is rendered here and by `nalarupa-cli resvg` (see resvg_render in
// core/src/bin/cli.rs) at the scales listed for its corpus in testdata/render/fixtures.json, and the premultiplied RGBA
// bytes are compared. Without NALARUPA_CLI the comparison is with the committed goldens
// (testdata/render/resvg/<corpus>/<stem>@<scale>.rgba.gz, raw bytes gzipped, plus index.json); with it, with the CLI's live
// output. NALARUPA_UPDATE_GOLDEN=1 (with NALARUPA_CLI) rewrites the goldens. The text corpus
// (testdata/usvg/text) depends on the installed fonts, so it is compared live only.
//
// Pixels must match exactly, except a fixture listed in fixtures.json under "tolerance": its max channel
// delta and max share of differing pixels hold only where the difference comes from a libm call, rcpps or
// a decoder (its "why" says which).
package resvg_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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

func liveMark(t *testing.T) {
	t.Helper()
	// A file in the module written during the run keeps the test cache from replaying a live pass.
	if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
		t.Fatal(err)
	}
}

func rustRender(t *testing.T, cli, file string, scale float32, fonts string) rendered {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.rgba")
	args := []string{"resvg", file, out, "--scale", strconv.FormatFloat(float64(scale), 'g', -1, 32)}
	if fonts != "" {
		args = append(args, "--fonts", fonts)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(cli, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "panicked") {
			return rendered{Panic: true}
		}
		t.Fatalf("%s resvg %s: %v\n%s", cli, file, err, stderr.String())
	}
	var r rendered
	if err := json.Unmarshal(stdout.Bytes(), &r); err != nil {
		t.Fatalf("%s resvg %s: %v (%s)", cli, file, err, stdout.String())
	}
	if r.Error == "" {
		var err error
		if r.data, err = os.ReadFile(out); err != nil {
			t.Fatal(err)
		}
	}
	return r
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
	cli := os.Getenv("NALARUPA_CLI")
	update := os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	if update && cli == "" {
		t.Fatal("NALARUPA_UPDATE_GOLDEN needs NALARUPA_CLI")
	}
	if cli != "" {
		liveMark(t)
	}
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
		if cli == "" {
			b, err := os.ReadFile(indexFile)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, &index); err != nil {
				t.Fatal(err)
			}
		}
		for _, doc := range docs {
			stem := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(doc), ".svgz"), ".svg")
			for _, scale := range c.Scales {
				key := stem + "@" + scaleName(scale)
				total++
				t.Run(name+"/"+key, func(t *testing.T) {
					var want rendered
					if cli != "" {
						want = rustRender(t, cli, doc, scale, "")
						if update {
							if err := os.MkdirAll(goldenDir, 0o777); err != nil {
								t.Fatal(err)
							}
							if want.data != nil {
								writeGolden(t, filepath.Join(goldenDir, key+".rgba.gz"), want.data)
							}
							index[key] = want
						}
					} else {
						var ok bool
						if want, ok = index[key]; !ok {
							t.Fatalf("no golden for %s", key)
						}
						if want.Error == "" && !want.Panic {
							want.data = readGolden(t, filepath.Join(goldenDir, key+".rgba.gz"))
						}
					}
					got := goRender(t, doc, scale, nil)
					checkRendered(t, got, want, fx.tolerance(name+"/"+key))
				})
			}
		}
		if update {
			b, err := json.MarshalIndent(index, "", " ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(indexFile, b, 0o666); err != nil {
				t.Fatal(err)
			}
		}
	}
	if total < 150 {
		t.Fatalf("%d renders, want at least 150", total)
	}
}

// writeGolden stores raw premultiplied RGBA bytes gzipped: renders are mostly flat color, so this keeps the
// goldens small.
func writeGolden(t *testing.T, path string, data []byte) {
	t.Helper()
	var b bytes.Buffer
	z, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	z.Write(data)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o666); err != nil {
		t.Fatal(err)
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

// TestTextRenderParity renders the text corpus with the fonts of this machine, live only.
func TestTextRenderParity(t *testing.T) {
	cli := os.Getenv("NALARUPA_CLI")
	if cli == "" {
		t.Skip("text render parity is live only: set NALARUPA_CLI to the release nalarupa-cli")
	}
	liveMark(t)
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
	for _, doc := range docs {
		stem := strings.TrimSuffix(filepath.Base(doc), ".svg")
		for _, scale := range c.Scales {
			key := stem + "@" + scaleName(scale)
			t.Run(key, func(t *testing.T) {
				if strings.HasPrefix(stem, "emoji") && !emoji {
					// As in usvg's TestTextParity: without monochrome Noto Emoji, emoji may fall back to a color
					// font (Segoe UI Emoji's COLR glyphs on a stock Windows), which the port does not draw.
					t.Skip("Noto Emoji is not installed")
				}
				want := rustRender(t, cli, doc, scale, cache)
				got := goRender(t, doc, scale, fonts)
				checkRendered(t, got, want, fx.tolerance("text/"+key))
			})
		}
	}
}
