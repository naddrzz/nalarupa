package shader

import (
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/shader/gpu"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

func needGPU(t *testing.T) {
	t.Helper()
	if _, err := gpu.Adapter(); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("no GPU adapter under CI: %v", err)
		}
		t.Skipf("no GPU adapter, skipped: %v", err)
	}
}

func clearCaches() {
	programs.Lock()
	programs.m = nil
	programs.Unlock()
	pngs.Lock()
	pngs.m = nil
	pngs.Unlock()
}

// The PNG cache is cleared once it holds more than 32 images, the program cache past 64 programs, as in
// shader.rs: a hit adds nothing, a miss adds one entry.
func TestCaches(t *testing.T) {
	needGPU(t)
	clearCaches()
	src := "uniform float v; void main() { gl_FragColor = vec4(v, 0.0, 0.0, 1.0); }"
	fill := func(i int) {
		t.Helper()
		u := jsonv.NewObject()
		u.Set("v", jsonv.Float(float64(i)/40))
		if _, err := PNG(src, u, 8, 8, svg.Outline{}, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	size := func() int {
		pngs.Lock()
		defer pngs.Unlock()
		return len(pngs.m)
	}
	for i := range 40 {
		fill(i)
	}
	// Entries 1-33 fill it; the 34th clears it first, leaving the last 7.
	if n := size(); n != 7 {
		t.Fatalf("after 40 fills: %d cached, want 7", n)
	}
	fill(39)
	if n := size(); n != 7 {
		t.Errorf("a recent fill missed: %d cached", n)
	}
	fill(0)
	if n := size(); n != 8 {
		t.Errorf("an evicted fill hit: %d cached", n)
	}

	clearCaches()
	for i := range 70 {
		program("void main() { gl_FragColor = vec4(" + strconv.Itoa(i) + ".0); }")
	}
	programs.Lock()
	n := len(programs.m)
	programs.Unlock()
	// Lookups 1-65 fill it; the 66th clears it first, leaving the last 5.
	if n != 5 {
		t.Errorf("after 70 programs: %d cached, want 5", n)
	}
	// Errors are cached with the program.
	bad := "void main() { gl_FragColor = vec4(oops); }"
	if a, b := program(bad), program(bad); a != b || a.err == nil {
		t.Errorf("a broken shader is compiled again or accepted: %v", a.err)
	}
}

func TestPainter(t *testing.T) {
	needGPU(t)
	glass, err := os.ReadFile("../testdata/shader/glass.frag")
	if err != nil {
		t.Fatal(err)
	}
	var p svg.ShaderPainter = Painter{}
	if !p.UsesBackdrop(string(glass)) || p.UsesBackdrop("/** @backdrop */ uniform sampler2D b; void main() { gl_FragColor = vec4(oops); }") {
		t.Error("UsesBackdrop needs the shader to read @backdrop and to compile")
	}
	data, err := p.PNG(string(glass), nil, 30, 20, svg.Outline{Kind: svg.OutlineEllipse}, nil, nil)
	if err != nil || len(data) < 20 {
		t.Fatalf("PNG %q, %v", data, err)
	}
	if svg.DefaultShader == nil {
		t.Error("the package does not register its painter")
	}
}

// Exports from several goroutines share the caches and the device (run with -race).
func TestConcurrentPNG(t *testing.T) {
	needGPU(t)
	var wg sync.WaitGroup
	for g := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := range 10 {
				src := "void main() { gl_FragColor = vec4(" + strconv.Itoa((g+k)%4) + ".0 / 4.0); }"
				if _, err := PNG(src, nil, 16, 16, svg.Outline{}, nil, nil); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
