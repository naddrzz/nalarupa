// PDF parity with the Rust core. Each case of testdata/pdf/fixtures.json is written as a PDF here
// (export.PDFTrees and EncodePDF for document nodes, pdf.Encode for the SVG corpora) and by the Rust CLI
// (`nalarupa-cli pdf <doc> <out> <id>... --offline` and `pdf-svg <file> <out> --fonts <cache>`). Every Go
// file must pass the structure check; then
//   - pdftotext must extract the same text from both files, and
//   - both files rasterised by the same PDF rasterizer (Windows.Data.Pdf through PowerShell on Windows,
//     pdftoppm elsewhere) must match within the case's "pdf" tolerance, and the Go file must match
//     core-go/export's PNG of the same tree (composited on the white page) within its "png" tolerance.
//
// It needs NALARUPA_CLI. Without a rasterizer it is skipped with a message, and without pdftotext the
// text comparison is; both fail instead under CI on Linux, where CI installs poppler-utils.
// NALARUPA_PDF_OUT=<dir> keeps the files and rasters there.
package pdf_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/pdf"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/resvg"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

var testdata = filepath.Join("..", "testdata")

// Tolerance bounds a raster difference: at most MaxShare of the pixels may differ by more than Delta in
// some channel, and the mean channel difference may be at most MeanDelta.
type Tolerance struct {
	Delta     int     `json:"delta"`
	MaxShare  float64 `json:"maxShare"`
	MeanDelta float64 `json:"meanDelta"`
	Why       string  `json:"why"`
}

type docCase struct {
	Name string   `json:"name"`
	Doc  string   `json:"doc"`
	IDs  []string `json:"ids"`
}

type fixtures struct {
	Scale     float64              `json:"scale"`
	Docs      []docCase            `json:"docs"`
	Corpora   []string             `json:"corpora"`
	Tolerance map[string]Tolerance `json:"tolerance"`
}

// tolerance is the most specific entry, for this rasterizer first: "<kind>.<rasterizer>/<case>/p<page>",
// "<kind>.<rasterizer>/<case>", "<kind>/<case>/p<page>", "<kind>/<case>", "<kind>.<rasterizer>", "<kind>".
// No entry allows no difference.
func (f *fixtures) tolerance(kind, rasterizer, name string, page int) Tolerance {
	kr := kind + "." + rasterizer
	for _, k := range []string{fmt.Sprintf("%s/%s/p%d", kr, name, page), kr + "/" + name, fmt.Sprintf("%s/%s/p%d", kind, name, page),
		kind + "/" + name, kr, kind} {
		if tol, ok := f.Tolerance[k]; ok {
			return tol
		}
	}
	return Tolerance{}
}

func loadFixtures(t *testing.T) *fixtures {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, "pdf", "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

// required reports whether a missing tool fails the test: under CI on Linux, which installs them.
func required() bool { return os.Getenv("CI") != "" && runtime.GOOS == "linux" }

func liveMark(t *testing.T) {
	t.Helper()
	// A file in the module written during the run keeps the test cache from replaying a live pass.
	if err := os.WriteFile(filepath.Join("..", ".parity-live"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
}

// dataDir is a fresh NALARUPA_DATA with the fixture icons in its asset cache.
func dataDir(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
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
	return data
}

func run(cli, data string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(cli, args...)
	cmd.Env = append(os.Environ(), "NALARUPA_DATA="+data)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v\n%s", cli, strings.Join(args, " "), err, stderr.String())
	}
	return nil
}

// pdfCase is one Go/Rust file pair and the trees the Go file was written from.
type pdfCase struct {
	name  string
	trees []*usvg.Tree
}

const powershellRaster = `param([string]$Dir, [double]$Scale)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Runtime.WindowsRuntime
$methods = [System.WindowsRuntimeSystemExtensions].GetMethods()
$asTaskOp = ($methods | Where-Object { $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncOperation` + "`" + `1' })[0]
$asTaskAction = ($methods | Where-Object { $_.Name -eq 'AsTask' -and $_.GetParameters().Count -eq 1 -and $_.GetParameters()[0].ParameterType.Name -eq 'IAsyncAction' })[0]
function Await($op, [Type]$t) { $task = $asTaskOp.MakeGenericMethod($t).Invoke($null, @($op)); $task.Wait() | Out-Null; $task.Result }
function AwaitAction($a) { $task = $asTaskAction.Invoke($null, @($a)); $task.Wait() | Out-Null }
[void][Windows.Storage.StorageFile, Windows.Storage, ContentType = WindowsRuntime]
[void][Windows.Storage.StorageFolder, Windows.Storage, ContentType = WindowsRuntime]
[void][Windows.Data.Pdf.PdfDocument, Windows.Data.Pdf, ContentType = WindowsRuntime]
[void][Windows.Data.Pdf.PdfPageRenderOptions, Windows.Data.Pdf, ContentType = WindowsRuntime]
Add-Type -AssemblyName System.Drawing
$folder = Await ([Windows.Storage.StorageFolder]::GetFolderFromPathAsync($Dir)) ([Windows.Storage.StorageFolder])
function Render($page, [uint32]$w, [uint32]$h, [string]$name) {
  $opts = New-Object Windows.Data.Pdf.PdfPageRenderOptions
  $opts.DestinationWidth = $w
  $opts.DestinationHeight = $h
  $out = Await ($folder.CreateFileAsync($name, [Windows.Storage.CreationCollisionOption]::ReplaceExisting)) ([Windows.Storage.StorageFile])
  $stream = Await ($out.OpenAsync([Windows.Storage.FileAccessMode]::ReadWrite)) ([Windows.Storage.Streams.IRandomAccessStream])
  AwaitAction ($page.RenderToStreamAsync($stream, $opts))
  $stream.Dispose()
}
# The renderer scales its destination size by the display's scale factor: measured once.
$factor = $null
foreach ($pdf in Get-ChildItem -Path $Dir -Filter *.pdf) {
  $file = Await ([Windows.Storage.StorageFile]::GetFileFromPathAsync($pdf.FullName)) ([Windows.Storage.StorageFile])
  $doc = Await ([Windows.Data.Pdf.PdfDocument]::LoadFromFileAsync($file)) ([Windows.Data.Pdf.PdfDocument])
  for ($i = 0; $i -lt $doc.PageCount; $i++) {
    $page = $doc.GetPage($i)
    if ($null -eq $factor) {
      Render $page 1000 1000 'factor.png'
      $img = [System.Drawing.Image]::FromFile((Join-Path $Dir 'factor.png'))
      $factor = $img.Width / 1000
      $img.Dispose()
      Remove-Item (Join-Path $Dir 'factor.png')
    }
    # Page.Size is in DIPs (96 per inch); one PDF point becomes Scale pixels.
    $w = [uint32][math]::Round($page.Size.Width * 72 / 96 * $Scale / $factor)
    $h = [uint32][math]::Round($page.Size.Height * 72 / 96 * $Scale / $factor)
    Render $page $w $h ($pdf.BaseName + '.p' + ($i + 1) + '.png')
    $page.Dispose()
  }
}
`

// rasterizer rasterises every PDF in a directory to <stem>.p<page>.png at scale pixels per point, and
// names itself ("windows" or "pdftoppm"); nil when this machine has none.
func rasterizer(t *testing.T) (func(dir string, scale float64) error, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		ps, err := exec.LookPath("powershell")
		if err != nil {
			return nil, ""
		}
		script := filepath.Join(t.TempDir(), "raster.ps1")
		if err := os.WriteFile(script, []byte(powershellRaster), 0o666); err != nil {
			t.Fatal(err)
		}
		return func(dir string, scale float64) error {
			abs, _ := filepath.Abs(dir)
			out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, abs, strconv.FormatFloat(scale, 'g', -1, 64)).CombinedOutput()
			if err != nil {
				return fmt.Errorf("Windows.Data.Pdf: %v\n%s", err, out)
			}
			return nil
		}, "windows"
	}
	tool, err := exec.LookPath("pdftoppm")
	if err != nil {
		return nil, ""
	}
	return func(dir string, scale float64) error {
		files, _ := filepath.Glob(filepath.Join(dir, "*.pdf"))
		for _, f := range files {
			stem := strings.TrimSuffix(f, ".pdf")
			if out, err := exec.Command(tool, "-r", strconv.FormatFloat(72*scale, 'g', -1, 64), "-png", f, stem+".p").CombinedOutput(); err != nil {
				return fmt.Errorf("pdftoppm %s: %v\n%s", f, err, out)
			}
			// pdftoppm names pages <prefix>-<n> (zero-padded to the page count's width).
			pages, _ := filepath.Glob(stem + ".p-*.png")
			for _, p := range pages {
				n, _ := strconv.Atoi(strings.TrimSuffix(p[len(stem+".p-"):], ".png"))
				if err := os.Rename(p, fmt.Sprintf("%s.p%d.png", stem, n)); err != nil {
					return err
				}
			}
		}
		return nil
	}, "pdftoppm"
}

// pages reads the rasterised pages of one PDF.
func pages(t *testing.T, dir, stem string) []*image.NRGBA {
	t.Helper()
	var out []*image.NRGBA
	for i := 1; ; i++ {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("%s.p%d.png", stem, i)))
		if err != nil {
			return out
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, toNRGBA(img))
	}
}

func toNRGBA(img image.Image) *image.NRGBA {
	if m, ok := img.(*image.NRGBA); ok && m.Rect.Min == (image.Point{}) {
		return m
	}
	out := image.NewNRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(out, out.Rect, img, img.Bounds().Min, draw.Src)
	return out
}

// onWhite composites an image over white, as a PDF page shows it.
func onWhite(img *image.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(img.Rect)
	draw.Draw(out, out.Rect, image.White, image.Point{}, draw.Src)
	draw.Draw(out, out.Rect, img, image.Point{}, draw.Over)
	return out
}

// compare checks got against want within tol, reporting the max and mean channel delta, the share of
// pixels beyond tol.Delta and the first such pixel.
func compare(t *testing.T, label string, got, want *image.NRGBA, tol Tolerance) {
	t.Helper()
	if got.Rect != want.Rect {
		t.Errorf("%s: size %v, want %v", label, got.Rect.Size(), want.Rect.Size())
		return
	}
	maxDelta, beyond, first, sum := 0, 0, -1, 0
	for i := 0; i < len(want.Pix); i += 4 {
		d := 0
		for c := range 3 {
			dc := int(got.Pix[i+c]) - int(want.Pix[i+c])
			if dc < 0 {
				dc = -dc
			}
			d = max(d, dc)
			sum += dc
		}
		if d > tol.Delta {
			beyond++
			if first < 0 {
				first = i
			}
		}
		maxDelta = max(maxDelta, d)
	}
	pixels := len(want.Pix) / 4
	share := float64(beyond) / float64(pixels)
	mean := float64(sum) / float64(3*pixels)
	msg := fmt.Sprintf("%s: max channel delta %d, mean %.3f, %d of %d pixels beyond %d (%.4f%%)", label, maxDelta, mean, beyond, pixels, tol.Delta, share*100)
	if first >= 0 {
		w := want.Rect.Dx()
		msg += fmt.Sprintf("; first at (%d,%d): got %v, want %v", first/4%w, first/4/w, got.Pix[first:first+4], want.Pix[first:first+4])
	}
	if share > tol.MaxShare || mean > tol.MeanDelta {
		t.Error(msg)
	} else {
		t.Log(msg)
	}
}

// rendered is what core-go/export's PNG export paints for a tree (resvg scaled to the pixel size),
// here at the size the rasterizer gave the page, composited on white.
func rendered(t *testing.T, tr *usvg.Tree, r image.Rectangle) *image.NRGBA {
	t.Helper()
	pm, ok := skia.NewPixmap(uint32(r.Dx()), uint32(r.Dy()))
	if !ok {
		t.Fatalf("no pixmap of %v", r.Size())
	}
	size := tr.Size()
	resvg.Render(tr, skia.TransformFromScale(float32(r.Dx())/size.Width(), float32(r.Dy())/size.Height()), pm.AsMut())
	return onWhite(&image.NRGBA{Pix: pm.TakeDemultiplied(), Stride: 4 * r.Dx(), Rect: r})
}

// crashed reports a CLI that panicked or aborted (resvg and krilla have inputs they crash on).
func crashed(err error) bool {
	s := err.Error()
	return strings.Contains(s, "panicked") || strings.Contains(s, "0xc0000409") || strings.Contains(s, "signal: abort")
}

func pdftotext(t *testing.T, tool, file string) string {
	t.Helper()
	out, err := exec.Command(tool, "-enc", "UTF-8", file, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext %s: %v", file, err)
	}
	return string(out)
}

func TestPDFParity(t *testing.T) {
	cli := os.Getenv("NALARUPA_CLI")
	if cli == "" {
		t.Skip("PDF parity needs the Rust oracle: set NALARUPA_CLI to the release nalarupa-cli")
	}
	liveMark(t)
	raster, rasterName := rasterizer(t)
	if raster == nil {
		msg := "no PDF rasterizer: install poppler-utils (pdftoppm), or run on Windows (Windows.Data.Pdf)"
		if required() {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	textTool, err := exec.LookPath("pdftotext")
	if err != nil {
		if required() {
			t.Fatal("no pdftotext: install poppler-utils")
		}
		t.Log("text comparison skipped: no pdftotext (install poppler-utils or Xpdf)")
	}
	fx := loadFixtures(t)
	dir := os.Getenv("NALARUPA_PDF_OUT") // keeps the files and rasters for inspection
	if dir == "" {
		dir = t.TempDir()
	} else if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	data := dataDir(t)
	fonts := text.New(t.TempDir())
	fonts.Online = false
	fontCache := t.TempDir()
	svgFonts := text.New(fontCache)
	svgFonts.Online = false

	var cases []pdfCase
	write := func(name string, trees []*usvg.Tree) {
		b, err := export.EncodePDF(trees)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".go.pdf"), b, 0o666); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, pdfCase{name, trees})
	}
	for _, dc := range fx.Docs {
		// Absolute: image fills resolve against the document's folder.
		path, err := filepath.Abs(filepath.Join(testdata, filepath.FromSlash(dc.Doc)))
		if err != nil {
			t.Fatal(err)
		}
		if err := run(cli, data, append([]string{"pdf", path, filepath.Join(dir, dc.Name+".rust.pdf")}, append(dc.IDs, "--offline")...)...); err != nil {
			t.Fatal(err)
		}
		d, err := doc.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		resolve.Migrate(d.Root)
		d.EnsureIDs()
		assets := svg.NewAssets(filepath.Join(data, "cache"))
		assets.Online = false
		ctx := &export.Ctx{Doc: d, Fonts: fonts, Assets: assets}
		trees, err := ctx.PDFTrees(dc.IDs)
		if err != nil {
			t.Fatalf("%s: %v", dc.Name, err)
		}
		write(dc.Name, trees)
	}
	emoji := svgFonts.HasFamily("Noto Emoji")
	for _, corpus := range fx.Corpora {
		files, _ := filepath.Glob(filepath.Join(testdata, filepath.FromSlash(corpus), "*.svg"))
		for _, file := range files {
			stem := strings.TrimSuffix(filepath.Base(file), ".svg")
			name := strings.ReplaceAll(corpus, "/", "-") + "-" + stem
			if corpus == "usvg/text" && strings.HasPrefix(stem, "emoji") && !emoji {
				// As in usvg's TestTextParity: without monochrome Noto Emoji, emoji may fall back to a color font
				// (Segoe UI Emoji's COLR glyphs on a stock Windows), which the port does not draw.
				t.Logf("%s: skipped, Noto Emoji is not installed", name)
				continue
			}
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			opt := usvg.DefaultOptions()
			opt.ResourcesDir = filepath.Dir(file)
			opt.ImageHrefResolver = svg.ImageHrefResolver()
			opt.FontDB = svgFonts.Database()
			opt.FontResolver = usvg.FontsResolver(svgFonts)
			tree, goErr := usvg.FromData(b, &opt)
			rustErr := run(cli, data, "pdf-svg", file, filepath.Join(dir, name+".rust.pdf"), "--fonts", fontCache)
			if rustErr != nil && goErr == nil && crashed(rustErr) {
				t.Logf("%s: skipped, Rust crashes on it: %v", name, rustErr)
				continue
			}
			if (goErr != nil) != (rustErr != nil) {
				t.Errorf("%s: Go error %v, Rust error %v", name, goErr, rustErr)
				continue
			}
			if goErr != nil {
				continue // neither parses it
			}
			write(name, []*usvg.Tree{tree})
		}
	}
	if err := raster(dir, fx.Scale); err != nil {
		t.Fatal(err)
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			goFile := filepath.Join(dir, c.name+".go.pdf")
			b, err := os.ReadFile(goFile)
			if err != nil {
				t.Fatal(err)
			}
			p, err := pdf.ParseStructure(b)
			if err != nil {
				t.Fatalf("structure: %v", err)
			}
			if len(p.Pages) != len(c.trees) {
				t.Fatalf("%d pages, want %d", len(p.Pages), len(c.trees))
			}
			for i, tr := range c.trees {
				want := [4]float64{0, 0, float64(tr.Size().Width()), float64(tr.Size().Height())}
				if fmt.Sprintf("%.3f", p.Pages[i]) != fmt.Sprintf("%.3f", want) {
					t.Errorf("page %d box %v, want %v", i+1, p.Pages[i], want)
				}
			}
			if textTool != "" {
				if got, want := pdftotext(t, textTool, goFile), pdftotext(t, textTool, filepath.Join(dir, c.name+".rust.pdf")); got != want {
					t.Errorf("text %q, want %q", got, want)
				}
			}
			goPages, rustPages := pages(t, dir, c.name+".go"), pages(t, dir, c.name+".rust")
			if len(goPages) != len(c.trees) || len(rustPages) != len(c.trees) {
				t.Fatalf("rasterised %d Go and %d Rust pages, want %d", len(goPages), len(rustPages), len(c.trees))
			}
			for i, tr := range c.trees {
				compare(t, fmt.Sprintf("page %d vs Rust's PDF", i+1), goPages[i], rustPages[i], fx.tolerance("pdf", rasterName, c.name, i+1))
				compare(t, fmt.Sprintf("page %d vs the PNG export", i+1), goPages[i], rendered(t, tr, goPages[i].Rect), fx.tolerance("png", rasterName, c.name, i+1))
			}
		})
	}
	names := make([]string, len(cases))
	for i, c := range cases {
		names[i] = c.name
	}
	sort.Strings(names)
	t.Logf("%d cases: %s", len(cases), strings.Join(names, " "))
}
