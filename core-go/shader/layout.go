package shader

import (
	"encoding/binary"
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// MaxSide caps a shader fill's image at 4096 px per side.
const MaxSide = 4096.0

// Slot is where a block member sits: its byte offset and whether it holds integers.
type Slot struct {
	Offset uint32
	Int    bool
}

// Program is a transpiled shader with its uniform block layout (shader.rs `Program` without the naga module).
type Program struct {
	GLSL     string
	Uniforms []Uniform
	// Layout maps a block member to its slot; Size is the block's size in bytes.
	Layout map[string]Slot
	Size   uint32
}

// Parse transpiles src and lays out its uniform block by std140, as naga's GLSL front end does.
func Parse(src string) (*Program, error) {
	glsl, uniforms, err := Transpile(src)
	if err != nil {
		return nil, err
	}
	p := &Program{GLSL: glsl, Uniforms: uniforms, Layout: map[string]Slot{}}
	var off, align uint32 = 0, 1
	put := func(name, ty string) {
		a, span, isInt := std140(ty)
		off = (off + a - 1) / a * a
		p.Layout[name] = Slot{off, isInt}
		off += span
		align = max(align, a)
	}
	for _, u := range uniforms {
		switch t := u.Type; {
		case t == "sampler2D":
		case t == "bool":
			put(u.Member+"_nrb", "int")
		case strings.HasPrefix(t, "bvec"):
			put(u.Member+"_nrb", "i"+t[1:])
		default:
			put(u.Member, t)
		}
	}
	put("nr_unit", "vec4")
	p.Size = (off + align - 1) / align * align
	for i := range p.Uniforms {
		if _, ok := p.Layout[p.Uniforms[i].Member+"_nrb"]; ok {
			p.Uniforms[i].Member += "_nrb"
		}
	}
	return p, nil
}

// std140 is the base alignment and size of a block member type, and whether its components are integers.
// Matrices are arrays of column vectors rounded up to 16 bytes (naga rejects mat2 there; 6b's compile
// reports it).
func std140(ty string) (align, span uint32, isInt bool) {
	isInt = strings.HasPrefix(ty, "i")
	switch ty {
	case "float", "int":
		return 4, 4, isInt
	case "vec2", "ivec2":
		return 8, 8, isInt
	case "vec3", "ivec3":
		return 16, 12, isInt
	case "vec4", "ivec4":
		return 16, 16, isInt
	case "mat2":
		return 16, 32, false
	case "mat3":
		return 16, 48, false
	}
	return 16, 64, false // mat4
}

// UsesBackdrop reports whether the program reads `@backdrop`.
func (p *Program) UsesBackdrop() bool {
	for _, u := range p.Uniforms {
		if u.Kind.Type == KindBackdrop {
			return true
		}
	}
	return false
}

// UsesBackdrop reports whether src transpiles and reads `@backdrop` (shader.rs `uses_backdrop`, which also
// needs naga to accept it).
func UsesBackdrop(src string) bool {
	p, err := Parse(src)
	return err == nil && p.UsesBackdrop()
}

// Pixels is the pixel size of a w×h node's image at scale, at most MaxSide per side.
func Pixels(w, h, scale float64) (uint32, uint32) {
	side := func(v float64) uint32 {
		if v = clamp(math.Ceil(v), 1, MaxSide); v != v {
			return 0 // Rust's `NaN as u32`
		}
		return uint32(v)
	}
	return side(w * scale), side(h * scale)
}

// Pack is the uniform block for a w×h node drawn at pw×ph pixels: the given values (else the defaults), then
// `nr_unit` (shader.rs `pack`).
func (p *Program) Pack(uniforms *jsonv.Object, w, h float64, pw, ph uint32) []byte {
	ubo := make([]byte, p.Size)
	put := func(member string, vals []float64) {
		s, ok := p.Layout[member]
		if !ok {
			return
		}
		for i, v := range vals {
			at := int(s.Offset) + i*4
			if at+4 > len(ubo) {
				continue
			}
			if s.Int {
				binary.LittleEndian.PutUint32(ubo[at:], uint32(satI32(v)))
			} else {
				binary.LittleEndian.PutUint32(ubo[at:], math.Float32bits(float32(v)))
			}
		}
	}
	for _, u := range p.Uniforms {
		switch u.Kind.Type {
		case KindImage, KindSDF, KindBackdrop:
		default:
			var given jsonv.Value
			if uniforms != nil {
				given, _ = uniforms.Get(u.Name)
			}
			put(u.Member, components(u.Kind, given, w, h))
		}
	}
	put("nr_unit", []float64{w / float64(pw), h / float64(ph), h, 0})
	return ubo
}

// satI32 is Rust's `f as i32`: saturating, NaN to 0.
func satI32(v float64) int32 {
	switch {
	case v != v:
		return 0
	case v <= math.MinInt32:
		return math.MinInt32
	case v >= math.MaxInt32:
		return math.MaxInt32
	}
	return int32(v)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// components is the stored value when it has the right shape, else the default (shader.rs `components`).
func components(k Kind, v jsonv.Value, w, h float64) []float64 {
	switch k.Type {
	case KindFloat:
		if f, ok := jsonv.F64(v); ok {
			return []float64{f}
		}
		return []float64{k.Def[0]}
	case KindInt:
		if f, ok := jsonv.F64(v); ok {
			return []float64{math.Trunc(f)}
		}
		return []float64{math.Trunc(k.Def[0])}
	case KindBool:
		switch x := v.(type) {
		case bool:
			return []float64{b2f(x)}
		case jsonv.Number:
			f, ok := x.Float64()
			return []float64{b2f(!ok || f != 0)}
		}
		return []float64{b2f(k.Flag)}
	case KindVec:
		a := jsonv.Arr(v)
		if a == nil {
			return k.Def
		}
		// Components the array does not give stay 0.
		out := make([]float64, len(k.Def))
		for i := range out {
			if i < len(a.Items) {
				if f, ok := jsonv.F64(a.Items[i]); ok {
					out[i] = f
				} else {
					out[i] = k.Def[i]
				}
			}
		}
		return out
	case KindColor:
		c := k.Def
		if s, ok := jsonv.Str(v); ok {
			v, _ := rgba(s)
			c = v[:]
		}
		if k.Flag {
			return c[:4]
		}
		return c[:3]
	case KindResolution:
		if k.Flag {
			return []float64{math.Trunc(w), math.Trunc(h)}
		}
		return []float64{w, h}
	case KindTime:
		return []float64{0}
	case KindMouse:
		if k.Flag {
			return []float64{math.Trunc(w / 2), math.Trunc(h / 2)}
		}
		return []float64{w / 2, h / 2}
	}
	return nil
}

// F16 is shader.rs `f16`: v as an IEEE half float, rounded to nearest.
func F16(v float32) uint16 {
	x := math.Float32bits(v)
	sign := uint16((x >> 16) & 0x8000)
	exp := int32((x>>23)&0xff) - 127 + 15
	man := x & 0x7f_ffff
	if v != v {
		return sign | 0x7e00
	}
	if exp >= 31 {
		return sign | 0x7c00
	}
	if exp <= 0 {
		if exp < -10 {
			return sign
		}
		m := (man | 0x80_0000) >> (1 - exp)
		return sign | uint16((m+0x1000)>>13)
	}
	h := uint32(exp)<<10 | man>>13
	// Round to nearest; a carry into the exponent is still the right value.
	return sign | uint16(h+((man>>12)&1))
}
