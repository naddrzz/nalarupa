package gpu_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/export"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/shader"
	"github.com/naddrzz/nalarupa/core-go/shader/gpu"
	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/svg"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// needGPU skips without an adapter, except under CI, where the software adapters are installed.
func needGPU(t *testing.T) {
	t.Helper()
	if _, err := gpu.Adapter(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("no GPU adapter under CI: %v", err)
		}
		t.Skipf("no GPU adapter, skipped: %v", err)
	}
}

func TestAdapter(t *testing.T) {
	needGPU(t)
	name, _ := gpu.Adapter()
	t.Log(name)
}

// shader.rs `renders_on_the_gpu`.
func TestRendersOnTheGPU(t *testing.T) {
	needGPU(t)
	src := "/** @resolution */ uniform vec2 r;\n/** @color @default #0000ff */ uniform vec3 c;\n/** @sdf */ uniform sampler2D s;\n/** @backdrop */ uniform sampler2D b;\n" +
		"void main() { vec2 uv = gl_FragCoord.xy / r; float inside = texture2D(s, uv).r > 0.0 ? 1.0 : 0.0; gl_FragColor = vec4(uv.y, texture2D(b, uv).g, c.b * inside, 1.0); }"
	behind := func(w, h int) (*skia.Pixmap, bool) {
		p, ok := skia.NewPixmap(uint32(w), uint32(h))
		if ok {
			p.Fill(skia.ColorFromRGBA8(0, 128, 0, 255))
		}
		return p, ok
	}
	pm, err := shader.Render(src, nil, 64, 32, 2, svg.Outline{Kind: svg.OutlineEllipse}, nil, behind)
	if err != nil {
		t.Fatal(err)
	}
	if pm.Width() != 128 || pm.Height() != 64 {
		t.Fatalf("size %dx%d", pm.Width(), pm.Height())
	}
	px := func(x, y uint32) skia.PremultipliedColorU8 { p, _ := pm.Pixel(x, y); return p }
	if !(px(64, 0).Red() > 250 && px(64, 63).Red() < 5) {
		t.Errorf("origin bottom-left: the top row has uv.y ≈ 1: %v %v", px(64, 0), px(64, 63))
	}
	if g := int(px(64, 32).Green()); g < 126 || g > 130 {
		t.Errorf("backdrop sampled: green %d", g)
	}
	if !(px(64, 32).Blue() > 250 && px(0, 0).Blue() == 0) {
		t.Errorf("@default colour, and the SDF is positive inside the ellipse only: %v %v", px(64, 32), px(0, 0))
	}
	// A hash that Vulkan fast-math would make differ between neighbouring lattice cells. shader.rs's
	// `strict_spirv` prevents it on Vulkan; wgpu-native cannot, so on a hardware Vulkan adapter the result is
	// only logged (a deferred gap), everywhere else it is asserted.
	hash := "float h(vec3 p) { return fract(sin(dot(p, vec3(127.1, 311.7, 74.7))) * 43758.5453); }\n" +
		"void main() { vec3 p = vec3(gl_FragCoord.xy / 4.0, 0.3); vec3 i = floor(p); gl_FragColor = vec4(abs(h(i + vec3(1.0, 0.0, 0.0)) - h(floor(p + vec3(1.0, 0.0, 0.0)))) * 1000.0, 0.0, 0.0, 1.0); }"
	pm, err = shader.Render(hash, nil, 64, 64, 1, svg.Outline{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := 0
	for i := 0; i < len(pm.Data()); i += 4 {
		if pm.Data()[i] != 0 {
			bad++
		}
	}
	name, _ := gpu.Adapter()
	switch {
	case gpu.HardwareVulkan():
		t.Logf("NoContraction gap on %s: lattice hash differs in %d of %d pixels", name, bad, len(pm.Data())/4)
	case bad > 0:
		t.Errorf("the same lattice point hashes differently from neighbouring cells on %s: %d of %d pixels", name, bad, len(pm.Data())/4)
	}
}

func TestTiles(t *testing.T) {
	needGPU(t)
	// 300 rows at scale 2 are three tiles (256 + 256 + 88); every row must be drawn once.
	src := "void main() { gl_FragColor = vec4(fract(gl_FragCoord.y / 7.0), 0.0, 0.0, 1.0); }"
	pm, err := shader.Render(src, nil, 10, 300, 2, svg.Outline{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for y := uint32(0); y < pm.Height(); y++ {
		if p, _ := pm.Pixel(5, y); p.Alpha() != 255 {
			t.Fatalf("row %d not drawn: %v", y, p)
		}
	}
}

func TestErrors(t *testing.T) {
	needGPU(t)
	if _, err := shader.Render("void main() { gl_FragColor = vec4(1.0); }", nil, 0, 10, 2, svg.Outline{}, nil, nil); err == nil || err.Error() != "empty fill" {
		t.Errorf("empty fill: %v", err)
	}
	_, err := shader.Render("void main() {\n gl_FragColor = vec4(oops);\n}", nil, 10, 10, 2, svg.Outline{}, nil, nil)
	if err == nil || err.Error() != "line 2: Unknown variable: oops" {
		t.Errorf("parse error: %v", err)
	}
}

// The device's start-up error is cached for good: run in a child process with an unavailable backend.
func TestNoAdapter(t *testing.T) {
	if os.Getenv("NR_GPU_CHILD") != "" {
		_, e1 := gpu.Adapter()
		_, e2 := shader.Render("void main() { gl_FragColor = vec4(1.0); }", nil, 10, 10, 2, svg.Outline{}, nil, nil)
		if e1 == nil || e2 == nil || e1.Error() != e2.Error() {
			t.Fatalf("errors %v / %v", e1, e2)
		}
		os.Stdout.WriteString("ERR " + e1.Error() + "\n")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNoAdapter$", "-test.count=1")
	cmd.Env = append(os.Environ(), "NR_GPU_CHILD=1", "WGPU_BACKEND=webgpu")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ERR no GPU adapter for shader fills: No suitable graphics adapter found; ") {
		t.Fatalf("%s", out)
	}
}

func TestConcurrentDraws(t *testing.T) {
	needGPU(t)
	srcs := []string{
		"/** @resolution */ uniform vec2 r; void main() { gl_FragColor = vec4(gl_FragCoord.xy / r, 0.5, 1.0); }",
		"/** @sdf */ uniform sampler2D s; void main() { gl_FragColor = vec4(texture2D(s, vec2(0.5)).r > 0.0 ? 1.0 : 0.0); }",
		"void main() { gl_FragColor = vec4(0.25, 0.5, 0.75, 1.0); }",
	}
	want := make([][]byte, len(srcs))
	for i, s := range srcs {
		pm, err := shader.Render(s, nil, 40, 30, 2, svg.Outline{Kind: svg.OutlineEllipse}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		want[i] = pm.Data()
	}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range 6 {
				i := (g + k) % len(srcs)
				pm, err := shader.Render(srcs[i], nil, 40, 30, 2, svg.Outline{Kind: svg.OutlineEllipse}, nil, nil)
				if err != nil {
					t.Error(err)
					return
				}
				if string(pm.Data()) != string(want[i]) {
					t.Errorf("shader %d: pixels differ from the serial draw", i)
				}
			}
		}()
	}
	wg.Wait()
}

// Exports of the backdrop fixture (shader fills, one reading @backdrop) through core-go/export from several
// goroutines at once, each with its own context: every PNG matches the same export made alone afterwards.
func TestConcurrentExports(t *testing.T) {
	needGPU(t)
	path := filepath.Join("..", "..", "testdata", "shader", "backdrop.pen")
	open := func() *export.Ctx {
		d, err := doc.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		resolve.Migrate(d.Root)
		d.EnsureIDs()
		assets := svg.NewAssets(filepath.Join(t.TempDir(), "cache"))
		assets.Online = false
		fonts := text.New(t.TempDir())
		fonts.Online = false
		return &export.Ctx{Doc: d, Fonts: fonts, Assets: assets}
	}
	type job struct {
		id    string
		scale float64
	}
	jobs := []job{{"card", 1}, {"card", 2}, {"lone", 1}, {"lone", 2}}
	got := make([][]byte, 4*len(jobs))
	ctxs := make([]*export.Ctx, len(got))
	for i := range ctxs {
		ctxs[i] = open()
	}
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := jobs[i%len(jobs)]
			b, err := ctxs[i].NodeImage(j.id, "png", j.scale, 100)
			if err != nil {
				t.Error(err)
			}
			got[i] = b
		}()
	}
	wg.Wait()
	ctx := open()
	for k, j := range jobs {
		want, err := ctx.NodeImage(j.id, "png", j.scale, 100)
		if err != nil {
			t.Fatal(err)
		}
		for i := k; i < len(got); i += len(jobs) {
			if !bytes.Equal(got[i], want) {
				t.Errorf("%s@%s, goroutine %d: the export differs from the serial one", j.id, strconv.FormatFloat(j.scale, 'g', -1, 64), i)
			}
		}
	}
}
