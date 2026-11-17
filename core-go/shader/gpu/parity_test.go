// Parity with the Rust core's GPU half (`nalarupa-cli shader-render`, `image`). Pixels depend on the adapter,
// so pixel parity runs live only (NALARUPA_CLI), on whatever adapter both pick (WGPU_BACKEND and
// WGPU_POWER_PREF reach both): every case of testdata/shader/gpu/render.json within its tolerance, the
// backdrop fixture exported as PNG, and the no-adapter errors. Compile errors compare with compile-errors.golden.json
// offline and with the CLI live; NALARUPA_UPDATE_GOLDEN=1 (with NALARUPA_CLI) rewrites it.
package gpu_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/shader"
	"github.com/naddrzz/nalarupa/core-go/shader/gpu"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

var fixtures = filepath.Join("..", "..", "testdata", "shader", "gpu")

// live is the CLI when NALARUPA_CLI is set, marking the run so the test cache does not replay it.
func live(t *testing.T) string {
	t.Helper()
	cli := os.Getenv("NALARUPA_CLI")
	if cli != "" {
		if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return cli
}

func runCLI(t *testing.T, cli string, env []string, args ...string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(cli, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v\n%s", cli, args, err, stderr.String())
	}
	return stdout.Bytes()
}

type oracleOut struct {
	Width, Height int
	Adapter       string
	Error         *string
}

// shaderRender runs `nalarupa-cli shader-render`; pixels holds the premultiplied RGBA it wrote.
func shaderRender(t *testing.T, cli string, env []string, frag string, uniforms json.RawMessage, w, h, scale float64, outline json.RawMessage, backdrop bool) (oracleOut, []byte) {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o666); err != nil {
			t.Fatal(err)
		}
		return p
	}
	u := "-"
	if len(uniforms) > 0 {
		u = write("uniforms.json", uniforms)
	}
	num := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	out := filepath.Join(dir, "out.rgba")
	args := []string{"shader-render", frag, u, num(w), num(h), num(scale), write("outline.json", outline), out}
	if backdrop {
		args = append(args, "--backdrop")
	}
	var o oracleOut
	if err := json.Unmarshal(runCLI(t, cli, env, args...), &o); err != nil {
		t.Fatal(err)
	}
	if o.Error != nil {
		return o, nil
	}
	px, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return o, px
}

// oracleBackdrop is shader.rs `oracle_backdrop`: opaque, red = column and green = row, 0 to 255 across.
func oracleBackdrop(w, h int) (*skia.Pixmap, bool) {
	p, ok := skia.NewPixmap(uint32(w), uint32(h))
	if !ok {
		return nil, false
	}
	d := p.DataMut()
	for i := range w * h {
		x, y := i%w, i/w
		d[4*i], d[4*i+1], d[4*i+2], d[4*i+3] = byte(x*255/max(w-1, 1)), byte(y*255/max(h-1, 1)), 64, 255
	}
	return p, true
}

func outlineOf(t *testing.T, raw json.RawMessage) svg.Outline {
	t.Helper()
	var o struct {
		Kind    string
		Radii   []float64
		D       string
		X, Y    float64
		EvenOdd bool
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	switch o.Kind {
	case "rrect":
		return svg.Outline{Kind: svg.OutlineRRect, Radii: [4]float64(o.Radii)}
	case "ellipse":
		return svg.Outline{Kind: svg.OutlineEllipse}
	}
	return svg.Outline{Kind: svg.OutlinePath, D: o.D, X: o.X, Y: o.Y, EvenOdd: o.EvenOdd}
}

type renderCase struct {
	Name      string
	Frag      string
	Node      string
	W, H      float64
	Scale     float64
	Uniforms  json.RawMessage
	Outline   json.RawMessage
	Backdrop  bool
	Tol       *int
	VulkanGap bool
}

func TestParityRender(t *testing.T) {
	cli := live(t)
	if cli == "" {
		t.Skip("pixel parity runs live only (NALARUPA_CLI): pixels depend on the adapter")
	}
	needGPU(t)
	adapter, _ := gpu.Adapter()
	b, err := os.ReadFile(filepath.Join(fixtures, "render.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct{ Cases []renderCase }
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Node != "" {
				b, err := os.ReadFile(filepath.Join(fixtures, c.Node))
				if err != nil || json.Unmarshal(b, &c) != nil {
					t.Fatal("bad node file", c.Node, err)
				}
			}
			frag := filepath.Join(fixtures, c.Frag)
			src, err := os.ReadFile(frag)
			if err != nil {
				t.Fatal(err)
			}
			want, px := shaderRender(t, cli, nil, frag, c.Uniforms, c.W, c.H, c.Scale, c.Outline, c.Backdrop)
			if want.Error != nil {
				t.Fatalf("Rust: %s", *want.Error)
			}
			if want.Adapter != adapter {
				t.Fatalf("Rust ran on %s, Go on %s", want.Adapter, adapter)
			}
			var uniforms *jsonv.Object
			if len(c.Uniforms) > 0 {
				uniforms = jsonv.Obj(jsonv.MustParse(string(c.Uniforms)))
			}
			load := func(u string) (*svg.Pixmap, bool) { return svg.DecodeImage(filepath.Join(filepath.Dir(frag), u)) }
			var behind func(w, h int) (*skia.Pixmap, bool)
			if c.Backdrop {
				behind = oracleBackdrop
			}
			pm, err := shader.Render(string(src), uniforms, c.W, c.H, c.Scale, outlineOf(t, c.Outline), load, behind)
			if err != nil {
				t.Fatal(err)
			}
			if int(pm.Width()) != want.Width || int(pm.Height()) != want.Height {
				t.Fatalf("size %dx%d, want %dx%d", pm.Width(), pm.Height(), want.Width, want.Height)
			}
			tol := 3
			if c.Tol != nil {
				tol = *c.Tol
			}
			maxDelta, over, first := diff(pm.Data(), px, tol)
			msg := "max delta " + strconv.Itoa(maxDelta) + ", " + strconv.Itoa(over) + " of " + strconv.Itoa(len(px)/4) + " pixels over " + strconv.Itoa(tol) + first
			switch {
			case c.VulkanGap && gpu.HardwareVulkan():
				t.Logf("NoContraction gap on %s: %s", adapter, msg)
			case over > 0:
				t.Error(msg)
			default:
				t.Log(msg)
			}
		})
	}
}

// diff compares premultiplied RGBA bytes: the largest channel delta, the pixels with a channel over tol and
// the first of them.
func diff(got, want []byte, tol int) (maxDelta, over int, first string) {
	for i := 0; i+4 <= len(want); i += 4 {
		d := 0
		for c := range 4 {
			d = max(d, abs(int(got[i+c])-int(want[i+c])))
		}
		maxDelta = max(maxDelta, d)
		if d > tol {
			if over == 0 {
				first = "; first at byte " + strconv.Itoa(i) + ": " + strconv.Itoa(int(got[i])) + "," + strconv.Itoa(int(got[i+1])) + "," + strconv.Itoa(int(got[i+2])) + "," + strconv.Itoa(int(got[i+3])) +
					" want " + strconv.Itoa(int(want[i])) + "," + strconv.Itoa(int(want[i+1])) + "," + strconv.Itoa(int(want[i+2])) + "," + strconv.Itoa(int(want[i+3]))
			}
			over++
		}
	}
	return
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Compile and validation errors word naga's errors as shader.rs `compile` does, line numbers included.
func TestParityCompileErrors(t *testing.T) {
	needGPU(t)
	cli := live(t)
	b, err := os.ReadFile(filepath.Join(fixtures, "compile-errors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var srcs []string
	if err := json.Unmarshal(b, &srcs); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join(fixtures, "compile-errors.golden.json")
	var want []string
	if cli == "" {
		b, err := os.ReadFile(golden)
		if err != nil || json.Unmarshal(b, &want) != nil {
			t.Fatal("bad golden", err)
		}
	} else {
		dir := t.TempDir()
		for i, src := range srcs {
			frag := filepath.Join(dir, strconv.Itoa(i)+".frag")
			if err := os.WriteFile(frag, []byte(src), 0o666); err != nil {
				t.Fatal(err)
			}
			o, _ := shaderRender(t, cli, nil, frag, nil, 10, 10, 1, json.RawMessage(`{"kind":"ellipse"}`), false)
			e := ""
			if o.Error != nil {
				e = *o.Error
			}
			want = append(want, e)
		}
		if os.Getenv("NALARUPA_UPDATE_GOLDEN") != "" {
			b, _ := json.MarshalIndent(want, "", "  ")
			if err := os.WriteFile(golden, append(b, '\n'), 0o666); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(want) != len(srcs) {
		t.Fatalf("%d golden errors for %d sources", len(want), len(srcs))
	}
	adapter, _ := gpu.Adapter()
	for i, src := range srcs {
		_, err := shader.Render(src, nil, 10, 10, 1, svg.Outline{Kind: svg.OutlineEllipse}, nil, nil)
		got := ""
		if err != nil {
			got = err.Error()
		}
		switch {
		case got == want[i]:
		case compileGaps[src] != "" && strings.HasSuffix(adapter, compileGaps[src]):
			t.Logf("known gap on %s: %q:\n got %q\nwant %q", adapter, src, got, want[i])
		default:
			t.Errorf("%q:\n got %q\nwant %q", src, got, want[i])
		}
	}
}

// compileGaps are sources whose result differs on one backend between wgpu-native's naga 29 and the Rust
// core's naga 30: naga 30's HLSL writer gained gl_PointCoord.
var compileGaps = map[string]string{
	"void main() { gl_FragColor = vec4(gl_PointCoord, 0.0, 1.0); }": "(Dx12)",
}

// The start-up errors for unavailable backends, in a child process each (the device is started once).
func TestParityNoAdapter(t *testing.T) {
	cli := live(t)
	if cli == "" {
		t.Skip("live only (NALARUPA_CLI)")
	}
	frag := filepath.Join(fixtures, "..", "plain.frag")
	for _, b := range []string{"webgpu", "metal", "bogus", "gl", "gl,metal", "noop", "noop,metal"} {
		env := []string{"WGPU_BACKEND=" + b}
		want, _ := shaderRender(t, cli, env, frag, nil, 10, 10, 1, json.RawMessage(`{"kind":"ellipse"}`), false)
		if want.Error == nil {
			t.Errorf("%s: Rust found an adapter (%s)", b, want.Adapter)
			continue
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestNoAdapter$", "-test.count=1")
		cmd.Env = append(os.Environ(), append(env, "NR_GPU_CHILD=1")...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", b, err, out)
		}
		if !strings.Contains(string(out), "ERR "+*want.Error+"\n") {
			t.Errorf("%s: want %q in\n%s", b, *want.Error, out)
		}
	}
}

// A document with shader fills (one reading `@backdrop`) exported as PNG through core-go/export: no error
// hatch, and the pixels of the Rust export within 3 per channel.
func TestParityExport(t *testing.T) {
	cli := live(t)
	if cli == "" {
		t.Skip("live only (NALARUPA_CLI): pixels depend on the adapter")
	}
	needGPU(t)
	path := filepath.Join(fixtures, "..", "backdrop.pen")
	d, err := doc.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	data := t.TempDir()
	assets := svg.NewAssets(filepath.Join(data, "cache"))
	assets.Online = false
	fonts := text.New(t.TempDir())
	fonts.Online = false
	ctx := &export.Ctx{Doc: d, Fonts: fonts, Assets: assets}
	for _, id := range []string{"card", "lone"} {
		for _, scale := range []string{"1", "2"} {
			t.Run(id+"@"+scale, func(t *testing.T) {
				out := filepath.Join(t.TempDir(), "out.png")
				runCLI(t, cli, []string{"NALARUPA_DATA=" + data}, "image", path, id, "png", scale, "100", out, "--offline")
				wantPNG, err := os.ReadFile(out)
				if err != nil {
					t.Fatal(err)
				}
				s, _ := strconv.ParseFloat(scale, 64)
				gotPNG, err := ctx.NodeImage(id, "png", s, 100)
				if err != nil {
					t.Fatal(err)
				}
				got, want := rgba(t, gotPNG), rgba(t, wantPNG)
				if got.Rect != want.Rect {
					t.Fatalf("size %v, want %v", got.Rect, want.Rect)
				}
				maxDelta, over, first := diff(got.Pix, want.Pix, 3)
				if over > 0 {
					t.Errorf("max delta %d, %d pixels over 3%s", maxDelta, over, first)
				}
			})
		}
	}
}

func rgba(t *testing.T, b []byte) *image.RGBA {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out := image.NewRGBA(img.Bounds())
	draw.Draw(out, out.Rect, img, img.Bounds().Min, draw.Src)
	return out
}
