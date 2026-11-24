// Golden tests for GPU shader compilation errors against compile-errors.golden.json.
package gpu_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/shader"
	"github.com/naddrzz/nalarupa/core-go/shader/gpu"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

var fixtures = filepath.Join("..", "..", "testdata", "shader", "gpu")

func TestParityCompileErrors(t *testing.T) {
	needGPU(t)
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
	b, err = os.ReadFile(golden)
	if err != nil || json.Unmarshal(b, &want) != nil {
		t.Fatal("bad golden", err)
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

// compileGaps are sources whose result differs on DX12 in wgpu-native's naga 29.
var compileGaps = map[string]string{
	"void main() { gl_FragColor = vec4(gl_PointCoord, 0.0, 1.0); }": "(Dx12)",
}
