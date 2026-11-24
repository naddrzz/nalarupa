// Golden tests over the shader fixtures in testdata/shader: transpile text, errors, block layout, packed
// uniform blocks, SDF texels within 1 ulp (f32 and f16), and backdrop SVG emission.
package shader

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

var fixtures = filepath.Join("..", "testdata", "shader")

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fixtures, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ── transpile ────────────────────────────────────────────────────────────────

type node struct {
	W, H, Scale float64
	Uniforms    json.RawMessage
}

type transpiled struct {
	GLSL         *string `json:"glsl"`
	Error        *string `json:"error"`
	CompileError *string `json:"compileError"`
	Layout       *struct {
		Size     uint32  `json:"size"`
		Members  [][]any `json:"members"`
		Uniforms []struct {
			Name   string `json:"name"`
			Member string `json:"member"`
			Type   string `json:"type"`
			Kind   []any  `json:"kind"`
		} `json:"uniforms"`
	} `json:"layout"`
	UBO string `json:"ubo"`
}

func kindJSON(k Kind) []any {
	nums := func(v []float64) []any {
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	}
	switch k.Type {
	case KindFloat:
		return []any{"float", k.Def[0]}
	case KindInt:
		return []any{"int", k.Def[0]}
	case KindBool:
		return []any{"bool", k.Flag}
	case KindVec:
		return []any{"vec", nums(k.Def), k.Flag}
	case KindColor:
		return []any{"color", nums(k.Def), k.Flag}
	case KindResolution:
		return []any{"resolution", k.Flag}
	case KindTime:
		return []any{"time"}
	case KindMouse:
		return []any{"mouse", k.Flag}
	case KindImage:
		return []any{"image"}
	case KindSDF:
		return []any{"sdf"}
	case KindBackdrop:
		return []any{"backdrop"}
	}
	return []any{"zero"}
}

// checkTranspile compares Go's transpile of src (and its block packed from n) with Rust's output want.
func checkTranspile(t *testing.T, src string, n node, want []byte) {
	t.Helper()
	var w transpiled
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	p, err := Parse(src)
	if w.Error != nil {
		if err == nil || err.Error() != *w.Error {
			t.Fatalf("error %v, want %q", err, *w.Error)
		}
		return
	}
	if err != nil {
		t.Fatalf("error %v, want none", err)
	}
	if p.GLSL != *w.GLSL {
		t.Fatalf("GLSL differs:\n%s", firstDiff(p.GLSL, *w.GLSL))
	}
	if w.CompileError != nil {
		t.Logf("naga rejects it (slice 6b): %s", *w.CompileError)
		return
	}
	if p.Size != w.Layout.Size {
		t.Errorf("block size %d, want %d", p.Size, w.Layout.Size)
	}
	members := map[string]Slot{}
	for _, m := range w.Layout.Members {
		members[m[0].(string)] = Slot{uint32(m[1].(float64)), m[2].(bool)}
	}
	if !reflect.DeepEqual(p.Layout, members) {
		t.Errorf("layout %v, want %v", p.Layout, members)
	}
	if len(p.Uniforms) != len(w.Layout.Uniforms) {
		t.Fatalf("%d uniforms, want %d", len(p.Uniforms), len(w.Layout.Uniforms))
	}
	for i, u := range w.Layout.Uniforms {
		g := p.Uniforms[i]
		if g.Name != u.Name || g.Member != u.Member || g.Type != u.Type || !reflect.DeepEqual(kindJSON(g.Kind), u.Kind) {
			t.Errorf("uniform %d: %+v, want %+v", i, g, u)
		}
	}
	var uniforms *jsonv.Object
	if len(n.Uniforms) > 0 {
		v, err := jsonv.Parse(n.Uniforms)
		if err != nil {
			t.Fatal(err)
		}
		uniforms = jsonv.Obj(v)
	}
	pw, ph := Pixels(n.W, n.H, n.Scale)
	if got := hex.EncodeToString(p.Pack(uniforms, n.W, n.H, pw, ph)); got != w.UBO {
		t.Errorf("uniform block\n  go:   %s\n  rust: %s", got, w.UBO)
	}
}

func TestParityTranspile(t *testing.T) {
	frags, err := filepath.Glob(filepath.Join(fixtures, "*.frag"))
	if err != nil || len(frags) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, frag := range frags {
		name := strings.TrimSuffix(filepath.Base(frag), ".frag")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(frag)
			if err != nil {
				t.Fatal(err)
			}
			n := node{W: 100, H: 100, Scale: 2}
			if b, err := os.ReadFile(filepath.Join(fixtures, name+".node.json")); err == nil {
				if err := json.Unmarshal(b, &n); err != nil {
					t.Fatal(err)
				}
			}
			want := readGolden(t, name+".golden.json")
			checkTranspile(t, string(src), n, want)
		})
	}
}

func TestParityTranspileErrors(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(fixtures, "errors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var srcs []string
	if err := json.Unmarshal(b, &srcs); err != nil {
		t.Fatal(err)
	}
	n := node{W: 100, H: 100, Scale: 2}
	wants := readGolden(t, "errors.golden.json")
	var want []json.RawMessage
	if err := json.Unmarshal(wants, &want); err != nil || len(want) != len(srcs) {
		t.Fatal("errors.golden.json does not match errors.json", err)
	}
	for i, src := range srcs {
		t.Run(strconv.Itoa(i), func(t *testing.T) { checkTranspile(t, src, n, want[i]) })
	}
}

// ── SDF ──────────────────────────────────────────────────────────────────────

type sdfCase struct {
	Name    string          `json:"name"`
	Outline json.RawMessage `json:"outline"`
	W, H    float64
	DPI     float64 `json:"dpi"`
}

func (c sdfCase) outline(t *testing.T) svg.Outline {
	t.Helper()
	var o struct {
		Kind    string     `json:"kind"`
		Radii   [4]float64 `json:"radii"`
		D       string     `json:"d"`
		X, Y    float64
		EvenOdd bool `json:"evenodd"`
	}
	if err := json.Unmarshal(c.Outline, &o); err != nil {
		t.Fatal(err)
	}
	switch o.Kind {
	case "rrect":
		return svg.Outline{Kind: svg.OutlineRRect, Radii: o.Radii}
	case "ellipse":
		return svg.Outline{Kind: svg.OutlineEllipse}
	}
	return svg.Outline{Kind: svg.OutlinePath, D: o.D, X: o.X, Y: o.Y, EvenOdd: o.EvenOdd}
}

// sdfGolden is a texture's size with texels on a grid of up to sampleN×sampleN (f32 RGBA and f16 RGBA, little
// endian, base64), in each array's own row order.
type sdfGolden struct {
	Name          string `json:"name"`
	Width, Height int
	F32, F16      string
}

const sampleN = 24

// samples are the grid indices (texel numbers) of a w×h texture.
func samples(w, h int) []int {
	pick := func(size int) []int {
		n := min(size, sampleN)
		out := make([]int, n)
		for k := range out {
			out[k] = k * (size - 1) / max(n-1, 1)
		}
		return out
	}
	var out []int
	for _, r := range pick(h) {
		for _, c := range pick(w) {
			out = append(out, r*w+c)
		}
	}
	return out
}

func sampleBytes(b []byte, texel int, idx []int) []byte {
	var out []byte
	for _, i := range idx {
		out = append(out, b[i*texel:(i+1)*texel]...)
	}
	return out
}

// ulps is how far apart two floats of the given width are, in units in the last place.
func ulps(a, b uint32, signBit uint32) uint32 {
	ord := func(x uint32) int64 {
		if x&signBit != 0 {
			return -int64(x &^ signBit)
		}
		return int64(x)
	}
	d := ord(a) - ord(b)
	return uint32(max(d, -d))
}

// compareTexels compares f32 (or f16) little-endian arrays within 1 ulp and logs how many differ.
func compareTexels(t *testing.T, what string, got, want []byte, width int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d bytes, want %d", what, len(got), len(want))
	}
	differ, worst := 0, uint32(0)
	for i := 0; i < len(got); i += width {
		var a, b, sign uint32
		if width == 4 {
			a, b, sign = binary.LittleEndian.Uint32(got[i:]), binary.LittleEndian.Uint32(want[i:]), 1<<31
		} else {
			a, b, sign = uint32(binary.LittleEndian.Uint16(got[i:])), uint32(binary.LittleEndian.Uint16(want[i:])), 1<<15
		}
		if a != b {
			differ++
			if u := ulps(a, b, sign); u > worst {
				worst = u
				if u > 1 {
					t.Errorf("%s: value %d (texel %d channel %d) is %#x, want %#x", what, i/width, i/width/4, i/width%4, a, b)
				}
			}
		}
	}
	if differ > 0 {
		t.Logf("%s: %d of %d values differ, by at most %d ulp", what, differ, len(got)/width, worst)
	}
}

func f32Bytes(px [][4]float32) []byte {
	out := make([]byte, 0, len(px)*16)
	for _, p := range px {
		for _, v := range p {
			out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v))
		}
	}
	return out
}

func u16Bytes(v []uint16) []byte {
	out := make([]byte, 0, len(v)*2)
	for _, x := range v {
		out = binary.LittleEndian.AppendUint16(out, x)
	}
	return out
}

func TestParitySDF(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(fixtures, "sdf.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []sdfCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	var goldens []sdfGolden
	g := readGolden(t, "sdf.golden.json")
	if err := json.Unmarshal(g, &goldens); err != nil || len(goldens) != len(cases) {
		t.Fatal("sdf.golden.json does not match sdf.json", err)
	}
	for i, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			sw, sh, px := sdfTexels(c.outline(t), c.W, c.H, c.DPI)
			got32, got16 := f32Bytes(px), u16Bytes(halfRows(sw, sh, px))
			idx := samples(sw, sh)
			g := goldens[i]
			if g.Width != sw || g.Height != sh {
				t.Fatalf("size %dx%d, want %dx%d", sw, sh, g.Width, g.Height)
			}
			w32, _ := base64.StdEncoding.DecodeString(g.F32)
			w16, _ := base64.StdEncoding.DecodeString(g.F16)
			compareTexels(t, "f32 samples", sampleBytes(got32, 16, idx), w32, 4)
			compareTexels(t, "f16 samples", sampleBytes(got16, 8, idx), w16, 2)
		})
	}
}

// ── backdrop ─────────────────────────────────────────────────────────────────

type noText struct{}

func (noText) Metrics(layout.TextStyle) layout.Metrics { panic("shader fixtures are text-free") }
func (noText) Width(string, layout.TextStyle) float64  { panic("shader fixtures are text-free") }
func (noText) Wrap(string, float64, layout.TextStyle) []resolve.Line {
	panic("shader fixtures are text-free")
}

// recorder notes the backdrop SVGs it is handed and draws a fixed image.
type recorder struct{ svgs []string }

func (r *recorder) UsesBackdrop(src string) bool { return UsesBackdrop(src) }

func (r *recorder) PNG(_ string, _ *jsonv.Object, _, _ float64, _ svg.Outline, _ func(string) (*svg.Pixmap, bool), b *svg.Backdrop) (string, error) {
	if b != nil {
		r.svgs = append(r.svgs, b.SVG)
	}
	return "AAAA", nil
}

// goBackdrops is the backdrop SVGs of the SVG export of top-level node id, as render.rs `export_svg` emits it.
func goBackdrops(t *testing.T, fixture, id, data string) []string {
	t.Helper()
	d, err := doc.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	resolve.Migrate(d.Root)
	d.EnsureIDs()
	var top jsonv.Value
	for _, n := range d.Children() {
		if jsonv.StrIs(jsonv.Get(n, "id"), id) {
			top = n
		}
	}
	root := resolve.New(d.Root, nil).Resolve(top)
	if root == nil {
		t.Fatal("no node", id)
	}
	layout.Layout(root, noText{})
	assets := svg.NewAssets(filepath.Join(data, "cache"))
	assets.Online = false
	r := &recorder{}
	s := svg.New(noText{}, assets, d.Dir())
	s.Embed = true
	s.Shader = r
	s.Node(root)
	return r.svgs
}

func TestParityBackdrop(t *testing.T) {
	fixture := filepath.Join(fixtures, "backdrop.pen")
	d, err := doc.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range d.Children() {
		id, _ := jsonv.Str(jsonv.Get(n, "id"))
		t.Run(id, func(t *testing.T) {
			data := t.TempDir()
			b := readGolden(t, "backdrop."+id+".json")
			var want []string
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			got := goBackdrops(t, fixture, id, data)
			if len(got) != len(want) {
				t.Fatalf("%d backdrops, want %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("backdrop %d differs:\n%s", i, firstDiff(got[i], want[i]))
				}
			}
		})
	}
}

func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(i-120, 0)
	cut := func(s string) string { return s[from:min(i+120, len(s))] }
	return fmt.Sprintf("at byte %d\n  got:  …%s…\n  want: …%s…", i, cut(a), cut(b))
}
