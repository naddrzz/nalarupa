// Package shader is the CPU half of shader fills (core/src/shader.rs): a WebGL fragment shader (GLSL ES
// 1.00, or 3.00 with one `out vec4`) whose uniforms carry `@directives` in the `/** … */` comment right above
// them is rewritten as Vulkan GLSL 450 with a std140 uniform block and texture/sampler pairs; the block is
// packed from the node's `uniforms`; `@sdf` gets the signed distance field of the node's outline. Compiling
// and drawing on the GPU are not here.
//
// Float products that feed an addition are wrapped in explicit float32/float64 conversions, which the Go
// spec says prevents fusing them into a multiply-add, so results match Rust's on any GOAMD64.
package shader

import (
	"math"
	"strings"
	"unicode"

	"github.com/naddrzz/nalarupa/core-go/resolve"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

// KindType is what a uniform receives.
type KindType uint8

const (
	// KindFloat is a float with default Def[0].
	KindFloat KindType = iota
	// KindInt is an int with default Def[0] (truncated).
	KindInt
	// KindBool is a bool with default Flag.
	KindBool
	// KindVec is a vecN or ivecN (Flag) with default Def.
	KindVec
	// KindColor is `@color`: default RGBA Def, Flag when the uniform is a vec4.
	KindColor
	// KindResolution is `@resolution`, the node size; Flag for ivec2.
	KindResolution
	// KindTime is `@time`, always 0.
	KindTime
	// KindMouse is `@mouse`, the node centre; Flag for ivec2.
	KindMouse
	// KindImage is a plain sampler2D: the image at the uniform's url.
	KindImage
	// KindSDF is `@sdf`: the outline's signed distance field.
	KindSDF
	// KindBackdrop is `@backdrop`: what lies behind the node.
	KindBackdrop
	// KindZero is a matrix or other type, left at zero.
	KindZero
)

// Kind is a uniform's kind and defaults (shader.rs `Kind`).
type Kind struct {
	Type KindType
	Def  []float64
	Flag bool
}

// defVal is an `@default` value as serde_json holds it: null (a non-finite number), a bool, a string, a
// number or an array of numbers (non-finite elements dropped by `as_f64`).
type defVal struct {
	isBool, isStr, isNum, isArr bool
	b                           bool
	s                           string
	nums                        []float64
}

// notes are the directives of a uniform's comment.
type notes struct {
	color, resolution, time, mouse, sdf, backdrop, rng bool
	def                                                *defVal
}

func trimSpace(s string) string      { return strings.TrimFunc(s, unicode.IsSpace) }
func trimLeftSpace(s string) string  { return strings.TrimLeftFunc(s, unicode.IsSpace) }
func trimRightSpace(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) }

// lines is Rust's `str::lines`: split at `\n` with a `\r` before it dropped, and no empty line after a final
// `\n`.
func lines(s string) []string {
	var out []string
	for s != "" {
		line, rest, found := strings.Cut(s, "\n")
		if found {
			line = strings.TrimSuffix(line, "\r")
		}
		out = append(out, line)
		s = rest
	}
	return out
}

func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// splitNums is `args.split(',').map(str::trim).filter(|s| !s.is_empty())`.
func splitNums(args string) []string {
	var out []string
	for _, p := range strings.Split(args, ",") {
		if p = trimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseNotes reads the directives of the `/** … */` comment that ends right before byte at (only whitespace
// between).
func parseNotes(src string, at int) (notes, error) {
	var n notes
	body, ok := strings.CutSuffix(trimRightSpace(src[:at]), "*/")
	if !ok {
		return n, nil
	}
	i := strings.LastIndex(body, "/*")
	if i < 0 {
		return n, nil
	}
	text, ok := strings.CutPrefix(body[i+2:], "*")
	if !ok {
		return n, nil
	}
	for _, line := range lines(text) {
		t := trimLeftSpace(line)
		rest := trimSpace(strings.TrimPrefix(t, "*"))
		for strings.HasPrefix(rest, "@") {
			r := rest[1:]
			end := 0
			for end < len(r) && isIdentByte(r[end]) {
				end++
			}
			name := r[:end]
			r = trimLeftSpace(r[end:])
			var args string
			if inner, ok := strings.CutPrefix(r, "("); ok {
				close := strings.IndexByte(inner, ')')
				if close < 0 {
					return n, errf("Unterminated argument list in annotation: '%s'", r)
				}
				args, r = inner[:close], trimLeftSpace(inner[close+1:])
			} else {
				next := strings.IndexByte(r, '@')
				if next < 0 {
					next = len(r)
				}
				args, r = trimSpace(r[:next]), r[next:]
			}
			rest = r
			flag := func(f *bool) error {
				if args != "" {
					return errf("@%s takes no arguments", name)
				}
				*f = true
				return nil
			}
			var err error
			switch name {
			case "color":
				err = flag(&n.color)
			case "resolution":
				err = flag(&n.resolution)
			case "time":
				err = flag(&n.time)
			case "mouse":
				err = flag(&n.mouse)
			case "sdf":
				err = flag(&n.sdf)
			case "backdrop":
				err = flag(&n.backdrop)
			case "min", "max", "range":
				parts := splitNums(args)
				want := 1
				if name == "range" {
					want = 2
				}
				bad := len(parts) != want
				for _, p := range parts {
					if _, ok := resolve.ParseFloat(p); !ok {
						bad = true
					}
				}
				if bad {
					return n, errf("@%s expects numeric arguments, got '%s'", name, args)
				}
				n.rng = true
			case "label":
				if args == "" {
					return n, errf("@label requires a value")
				}
			case "default":
				d, e := defaultValue(args)
				if e != nil {
					return n, e
				}
				n.def = &d
			default:
				return n, errf("Unknown annotation directive: @%s", name)
			}
			if err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func finite(f float64) bool { return f-f == 0 }

// defaultValue is shader.rs `default_value`.
func defaultValue(a string) (defVal, error) {
	switch t := trimSpace(a); {
	case t == "":
		return defVal{}, errf("@default requires a value")
	case t == "true" || t == "false":
		return defVal{isBool: true, b: t == "true"}, nil
	case strings.HasPrefix(t, "#"):
		return defVal{isStr: true, s: t}, nil
	default:
		var nums []float64
		for _, s := range splitNums(t) {
			f, ok := resolve.ParseFloat(s)
			if !ok {
				return defVal{}, errf("@default got non-numeric value: '%s'", s)
			}
			nums = append(nums, f)
		}
		if len(nums) == 1 {
			if !finite(nums[0]) {
				return defVal{}, nil // json!(NaN) is null
			}
			return defVal{isNum: true, nums: nums}, nil
		}
		kept := nums[:0]
		for _, f := range nums {
			if finite(f) {
				kept = append(kept, f)
			}
		}
		return defVal{isArr: true, nums: kept}, nil
	}
}

// numbers is the numbers of a default: an array's, a number's, else none.
func (d *defVal) numbers() []float64 {
	if d.isArr || d.isNum {
		return d.nums
	}
	return nil
}

// rgba is svg.rs `rgba`: `#RGB`, `#RRGGBB` or `#RRGGBBAA` as 0..1 components.
func rgba(s string) ([4]float64, bool) {
	c, ok := svg.RGBA8(s)
	if !ok {
		return [4]float64{}, false
	}
	return [4]float64{float64(c[0]) / 255, float64(c[1]) / 255, float64(c[2]) / 255, float64(c[3]) / 255}, true
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// kindOf is shader.rs `kind`: the uniform kind from its GLSL type and directives.
func kindOf(name, ty string, n *notes) (Kind, error) {
	fail := func(m string) (Kind, error) { return Kind{}, errf("Uniform '%s': %s", name, m) }
	isInt := strings.HasPrefix(ty, "i")
	dim := 1
	if l := ty[len(ty)-1:]; l >= "0" && l <= "9" {
		dim = int(l[0] - '0')
	}
	if n.color && ty != "vec3" && ty != "vec4" {
		return fail("@color requires vec3 or vec4")
	}
	special := n.resolution || n.time || n.mouse || n.sdf || n.backdrop
	if (special || n.color) && n.rng {
		return fail("@range/@min/@max is not valid for '" + ty + "'")
	}
	if (n.time || n.mouse || n.sdf || n.backdrop) && n.def != nil {
		return fail("this uniform does not support @default")
	}
	switch {
	case n.color:
		alpha := ty == "vec4"
		c := []float64{1, 1, 1, 1}
		switch d := n.def; {
		case d == nil:
		case d.isStr:
			v, ok := rgba(d.s)
			if !ok {
				return Kind{}, errf("Uniform '%s': @default invalid hex literal: '%s'", name, d.s)
			}
			c = v[:]
		default:
			v := d.numbers()
			want := 3
			if alpha {
				want = 4
			}
			if len(v) != want {
				return fail("@color @default needs one number per component")
			}
			c = []float64{v[0], v[1], v[2], 1}
			if alpha {
				c[3] = v[3]
			}
			for i, x := range c {
				c[i] = math.Round(clamp(x, 0, 1)*255) / 255
			}
		}
		return Kind{KindColor, c, alpha}, nil
	case n.resolution:
		if ty != "vec2" && ty != "ivec2" {
			return fail("@resolution requires vec2 or ivec2")
		}
		return Kind{Type: KindResolution, Flag: isInt}, nil
	case n.time:
		if ty != "float" {
			return fail("@time requires float")
		}
		return Kind{Type: KindTime}, nil
	case n.mouse:
		if ty != "vec2" && ty != "ivec2" {
			return fail("@mouse requires vec2 or ivec2")
		}
		return Kind{Type: KindMouse, Flag: isInt}, nil
	case n.sdf || n.backdrop:
		if ty != "sampler2D" {
			return fail("@sdf/@backdrop requires sampler2D")
		}
		if n.sdf {
			return Kind{Type: KindSDF}, nil
		}
		return Kind{Type: KindBackdrop}, nil
	}
	switch ty {
	case "float", "int":
		d := 0.0
		if n.def != nil {
			if !n.def.isNum {
				return Kind{}, errf("Uniform '%s': @default must be a single number", name)
			}
			d = n.def.nums[0]
		}
		if ty == "int" {
			return Kind{KindInt, []float64{math.Trunc(d)}, false}, nil
		}
		return Kind{KindFloat, []float64{d}, false}, nil
	case "bool":
		b := false
		if n.def != nil {
			if !n.def.isBool {
				return fail("is bool, @default must be true or false")
			}
			b = n.def.b
		}
		return Kind{Type: KindBool, Flag: b}, nil
	case "vec2", "vec3", "vec4", "ivec2", "ivec3", "ivec4":
		d := make([]float64, dim)
		if n.def != nil {
			d = n.def.numbers()
		}
		if len(d) != dim {
			return fail("@default needs " + itoa(dim) + " numbers")
		}
		return Kind{KindVec, append([]float64(nil), d...), isInt}, nil
	case "sampler2D":
		if n.def != nil {
			return fail("is sampler2D, @default is not supported")
		}
		return Kind{Type: KindImage}, nil
	}
	return Kind{Type: KindZero}, nil
}
