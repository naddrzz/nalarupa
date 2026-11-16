package shader

import (
	"encoding/binary"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

const demo = `
precision mediump float;
/** @resolution */
uniform vec2 u_res;
/** @time */
uniform float u_time;
/**
 * Tint used on the left half.
 * @label Tint @color
 * @default #ff0000
 */
uniform vec3 u_tint;
/** @default 0.25 @range 0, 1 */
uniform float u_mix;
/** @default true */
uniform bool u_on;
/** @sdf */
uniform sampler2D u_shape;
vec3 shade(vec3 c) { return c * 0.5; }
vec2 shade(vec2 c) { return c; }
void main() {
  vec2 uv = gl_FragCoord.xy / u_res;
  float d = texture2D(u_shape, uv).r;
  vec2 size = vec2(textureSize(u_shape, 0));
  vec3 c = uv.x < 0.5 ? u_tint : vec3(0.0, uv.y, u_mix);
  if (!u_on) { c = shade(c); }
  gl_FragColor = vec4(c, d > 0.0 && size.x > 0.0 ? 1.0 : 0.0);
}
`

// shader.rs `annotations_and_transpile`, without naga's parse errors (slice 6b).
func TestAnnotationsAndTranspile(t *testing.T) {
	glsl, u, err := Transpile(demo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(glsl, "sampler2D(u_shape_nrt, u_shape_nrs)") || !strings.Contains(glsl, "bool(u_on_nrb)") || strings.Contains(glsl, "mediump") {
		t.Fatal(glsl)
	}
	swz, _, err := Transpile("uniform sampler2D b; void main() { vec3 c = vec3(1.0); gl_FragColor = vec4(c.b) + texture2D(b, vec2(0.5)); }")
	if err != nil || !strings.Contains(swz, "vec4(c.b)") || !strings.Contains(swz, "texture(sampler2D(b_nrt, b_nrs)") {
		t.Fatal("swizzles keep their name:", swz, err)
	}
	if _, err := Parse("uniform bvec2 f; void main() { gl_FragColor = vec4(f.x ? 1.0 : 0.0); }"); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name string
		kind Kind
	}{
		{"u_res", Kind{Type: KindResolution}},
		{"u_time", Kind{Type: KindTime}},
		{"u_tint", Kind{KindColor, []float64{1, 0, 0, 1}, false}},
		{"u_mix", Kind{KindFloat, []float64{0.25}, false}},
		{"u_on", Kind{Type: KindBool, Flag: true}},
		{"u_shape", Kind{Type: KindSDF}},
	}
	for i, w := range want {
		if u[i].Name != w.name || !reflect.DeepEqual(u[i].Kind, w.kind) {
			t.Errorf("uniform %d: %+v, want %s %+v", i, u[i], w.name, w.kind)
		}
	}
	p, err := Parse(demo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Layout["u_on_nrb"]; !ok {
		t.Fatal(p.Layout)
	}
	if _, ok := p.Layout["nr_unit"]; !ok {
		t.Fatal(p.Layout)
	}
	if _, err := Parse("void main() { }"); err == nil || !strings.Contains(err.Error(), "No color output") {
		t.Fatal(err)
	}
	if _, err := Parse("/** @bogus */ uniform float x; void main() { gl_FragColor = vec4(x); }"); err == nil || !strings.Contains(err.Error(), "@bogus") {
		t.Fatal(err)
	}
}

// Each annotation and structure error has Rust's text.
func TestTranspileErrors(t *testing.T) {
	for _, c := range []struct{ src, err string }{
		{"/** @x */ uniform float a; void main() { gl_FragColor = vec4(a); }", "Unknown annotation directive: @x"},
		{"/** @range 0 */ uniform float a; void main() { gl_FragColor = vec4(a); }", "@range expects numeric arguments, got '0'"},
		{"/** @min(a) */ uniform float a; void main() { gl_FragColor = vec4(a); }", "@min expects numeric arguments, got 'a'"},
		{"/** @label(x */ uniform float a; void main() { gl_FragColor = vec4(a); }", "Unterminated argument list in annotation: '(x'"},
		{"/** @color(1) */ uniform vec3 a; void main() { gl_FragColor = vec4(a, 1.0); }", "@color takes no arguments"},
		{"/** @label */ uniform float a; void main() { gl_FragColor = vec4(a); }", "@label requires a value"},
		{"/** @default 1, x */ uniform vec2 a; void main() { gl_FragColor = vec4(a, a); }", "@default got non-numeric value: 'x'"},
		{"/** @sdf */ uniform float a; void main() { gl_FragColor = vec4(a); }", "Uniform 'a': @sdf/@backdrop requires sampler2D"},
		{"/** @color */ uniform float a; void main() { gl_FragColor = vec4(a); }", "Uniform 'a': @color requires vec3 or vec4"},
		{"/** @color @range 0, 1 */ uniform vec3 a; void main() { gl_FragColor = vec4(a, 1.0); }", "Uniform 'a': @range/@min/@max is not valid for 'vec3'"},
		{"/** @time @default 1 */ uniform float a; void main() { gl_FragColor = vec4(a); }", "Uniform 'a': this uniform does not support @default"},
		{"/** @color @default 1, 0 */ uniform vec3 a; void main() { gl_FragColor = vec4(a, 1.0); }", "Uniform 'a': @color @default needs one number per component"},
		{"/** @color @default #12 */ uniform vec3 a; void main() { gl_FragColor = vec4(a, 1.0); }", "Uniform 'a': @default invalid hex literal: '#12'"},
		{"/** @default 1, 2 */ uniform float a; void main() { gl_FragColor = vec4(a); }", "Uniform 'a': @default must be a single number"},
		{"/** @default 1 */ uniform bool a; void main() { gl_FragColor = vec4(a); }", "Uniform 'a': is bool, @default must be true or false"},
		{"/** @default 1 */ uniform vec2 a; void main() { gl_FragColor = vec4(a, a); }", "Uniform 'a': @default needs 2 numbers"},
		{"/** @default 1 */ uniform sampler2D a; void main() { gl_FragColor = vec4(1.0); }", "Uniform 'a': is sampler2D, @default is not supported"},
		{"uniform samplerCube a; void main() { gl_FragColor = vec4(1.0); }", "Unsupported uniform type: samplerCube"},
		{"uniform float a[2]; void main() { gl_FragColor = vec4(a[0]); }", "Unsupported uniform type: uniform arrays (uniform float a[2])"},
		{"uniform; void main() { gl_FragColor = vec4(1.0); }", "uniform without a type"},
		{"void mane() { gl_FragColor = vec4(1.0); }", "main() is missing!"},
		{"void main() { }", "No color output found. Expected `out vec4 <name>` or `gl_FragColor`."},
		{"#version 300 es\nout vec4 a; out vec4 b; void main() { a = vec4(1.0); }", "Must use a single `out vec4` variable!"},
		{"#version 300 es\nout vec3 a; void main() { a = vec3(1.0); }", "Must use a single `out vec4` variable!"},
		{"varying vec2 uv; void main() { gl_FragColor = vec4(uv, 0.0, 1.0); }", "The shader must not use anything but gl_FragCoord and uniforms!"},
		{"#version 300 es\nin vec2 uv; out vec4 c; void main() { c = vec4(uv, 0.0, 1.0); }", "The shader must not use anything but gl_FragCoord and uniforms!"},
	} {
		_, _, err := Transpile(c.src)
		if err == nil || err.Error() != c.err {
			t.Errorf("%q: %v, want %q", c.src, err, c.err)
		}
	}
	// gl_PointCoord is left to the compiler (naga, slice 6b).
	if _, _, err := Transpile("void main() { gl_FragColor = vec4(gl_PointCoord, 0.0, 1.0); }"); err != nil {
		t.Fatal(err)
	}
}

// ES 3.00: `out vec4` and `texture` stay; `#version`/`#extension` and precision lines are blanked to their
// length, so line numbers hold; reserved words are renamed.
func TestTranspileES3(t *testing.T) {
	src := "#version 300 es\n#extension GL_OES_standard_derivatives : enable\nprecision highp float;\nlayout(location = 0) out highp vec4 color;\nuniform sampler2D input;\nvoid main() {\n  float sample = 1.0; // comment\n  color = texture(input, vec2(sample));\n}\n"
	glsl, u, err := Transpile(src)
	if err != nil {
		t.Fatal(err)
	}
	// The header adds two lines before the code, the wrapper two after.
	if strings.Count(glsl, "\n") != strings.Count(src, "\n")+4 || strings.Split(glsl, "\n")[2+5] != "void nr_main() {" {
		t.Fatal("line numbers moved:", glsl)
	}
	for _, want := range []string{"nr_FragColor = texture(sampler2D(nr_input_nrt, nr_input_nrs), vec2(nr_sample));", "uniform texture2D nr_input_nrt"} {
		if !strings.Contains(glsl, want) {
			t.Fatal(want, glsl)
		}
	}
	if strings.Contains(glsl, "#extension") || strings.Contains(glsl, "precision") || strings.Contains(glsl, "comment") || u[0].Member != "nr_input" {
		t.Fatal(glsl)
	}
}

func f32s(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func i32At(b []byte, off uint32) int32 { return int32(binary.LittleEndian.Uint32(b[off:])) }

// std140 offsets, defaults and given values, ints truncated and saturated, bools from numbers, and
// `@min`/`@max` not clamping (shader.rs only validates them).
func TestPack(t *testing.T) {
	src := `/** @color @default 0.5, 2, -1, 0.25 */ uniform vec4 c;
/** @min 0 @max 1 */ uniform float f;
uniform int i;
/** @default true */ uniform bool b;
uniform vec3 v;
/** @resolution */ uniform ivec2 r;
/** @mouse */ uniform vec2 m;
uniform mat3 x;
uniform float g;
void main() { gl_FragColor = c * f * float(i) * v.x; }`
	p, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	slots := map[string]Slot{"c": {0, false}, "f": {16, false}, "i": {20, true}, "b_nrb": {24, true}, "v": {32, false}, "r": {48, true}, "m": {56, false}, "x": {64, false}, "g": {112, false}, "nr_unit": {128, false}}
	if !reflect.DeepEqual(p.Layout, slots) || p.Size != 144 {
		t.Fatal(p.Layout, p.Size)
	}
	if p.Uniforms[3].Member != "b_nrb" {
		t.Fatal(p.Uniforms[3])
	}
	ubo := p.Pack(jsonv.MustParse(`{"f": 7.5, "i": 3e10, "b": 0, "v": [1, "x"]}`).(*jsonv.Object), 30.6, 20, 62, 40)
	fl := f32s(ubo)
	if fl[0] != float32(128.0/255) || fl[1] != 1 || fl[2] != 0 || fl[3] != float32(64.0/255) || fl[4] != 7.5 || i32At(ubo, 20) != math.MaxInt32 || i32At(ubo, 24) != 0 ||
		fl[8] != 1 || fl[9] != 0 || fl[10] != 0 || i32At(ubo, 48) != 30 || i32At(ubo, 52) != 20 || fl[14] != 15.3 || fl[15] != 10 ||
		fl[32] != float32(30.6/62) || fl[33] != 0.5 || fl[34] != 20 {
		t.Fatal(fl)
	}
}

func TestF16(t *testing.T) {
	for _, c := range []struct {
		v    float32
		want uint16
	}{
		{0, 0}, {1, 0x3c00}, {-2, 0xc000}, {65504, 0x7bff}, {1e6, 0x7c00}, {float32(math.NaN()), 0x7e00},
		{6.1035156e-05, 0x0400}, {5.9604645e-08, 0x0001}, {1e-9, 0}, {0.333333, 0x3555}, {float32(math.Copysign(0, -1)), 0x8000},
	} {
		if got := F16(c.v); got != c.want {
			t.Errorf("F16(%v) = %#04x, want %#04x", c.v, got, c.want)
		}
	}
}

func half(v uint16) float32 {
	e := int((v >> 10) & 0x1f)
	m := float32(v & 0x3ff)
	s := float32(1)
	if v&0x8000 != 0 {
		s = -1
	}
	if e == 0 {
		return s * m / 1024 * float32(math.Pow(2, -14))
	}
	return s * (1 + m/1024) * float32(math.Pow(2, float64(e-15)))
}

// shader.rs `sdf_of_a_circle`.
func TestSDFOfACircle(t *testing.T) {
	w, h, px := SDF(svg.Outline{Kind: svg.OutlineEllipse}, 100, 100, 1)
	if w != 100 || h != 100 {
		t.Fatal(w, h)
	}
	centre, corner := half(px[(50*100+50)*4]), half(px[0])
	if math.Abs(float64(centre)-49.3) >= 0.5 || corner >= -19 {
		t.Fatal(centre, corner)
	}
	// A path outline gives the same field, except that it is blurred (σ = 3 texels), which lowers the cone's
	// peak by about σ·√(π/2).
	path := svg.Outline{Kind: svg.OutlinePath, D: "M0 50A50 50 0 1 0 100 50A50 50 0 1 0 0 50Z"}
	_, _, p2 := SDF(path, 100, 100, 1)
	c2, e2 := half(p2[(50*100+50)*4]), half(p2[(50*100+1)*4])
	if centre-c2 <= 2.5 || centre-c2 >= 4.5 || math.Abs(float64(e2)-1.5) >= 1 {
		t.Fatal(c2, e2)
	}
}

// The texture is capped at 1024 px per side; a rounded rect's corners and an even-odd hole read outside.
func TestSDFShapes(t *testing.T) {
	sw, sh, _ := sdfTexels(svg.Outline{Kind: svg.OutlineEllipse}, 1500, 900, 1)
	if sw != 1024 || sh != 615 {
		t.Fatal(sw, sh)
	}
	_, _, px := sdfTexels(svg.Outline{Kind: svg.OutlineRRect, Radii: [4]float64{20, 0, 0, 0}}, 100, 100, 1)
	if tl, tr := px[0][0], px[99][0]; tl > -5 || tr != 0.5 {
		t.Fatal(tl, tr)
	}
	ring := svg.Outline{Kind: svg.OutlinePath, D: "M10 10H90V90H10ZM30 30H70V70H30Z", X: 0, Y: 0, EvenOdd: true}
	_, _, px = sdfTexels(ring, 100, 100, 1)
	if hole, band := px[50*100+50][0], px[50*100+20][0]; hole >= 0 || band <= 0 {
		t.Fatal(hole, band)
	}
	if _, _, px = sdfTexels(svg.Outline{Kind: svg.OutlinePath, D: "M0 0 L"}, 10, 10, 1); px[0][0] >= 0 {
		t.Fatal("a broken path is empty, outside everywhere:", px[0])
	}
}
