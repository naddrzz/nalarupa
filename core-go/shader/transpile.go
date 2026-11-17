package shader

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func errf(format string, a ...any) error { return errors.New(fmt.Sprintf(format, a...)) }

func itoa(i int) string { return strconv.Itoa(i) }

// Uniform is one declared uniform (shader.rs `Uniform`).
type Uniform struct {
	Name string
	// Member is the name inside the GLSL 450 uniform block (with `_nrb` for bool/bvec), or the texture/sampler
	// pair's base name.
	Member string
	Type   string
	Kind   Kind
}

// blankComments is a same-length copy of src with comments blanked (newlines kept), so byte offsets and
// lines still match.
func blankComments(src string) []byte {
	b := []byte(src)
	i := 0
	for i+1 < len(b) {
		var end int
		switch {
		case b[i] == '/' && b[i+1] == '/':
			end = len(b)
			if p := strings.IndexByte(src[i:], '\n'); p >= 0 {
				end = i + p
			}
		case b[i] == '/' && b[i+1] == '*':
			end = len(b)
			if p := strings.Index(src[i+2:], "*/"); p >= 0 {
				end = i + p + 4
			}
		default:
			i++
			continue
		}
		blankRange(b, i, end)
		i = end
	}
	return b
}

func blankRange(b []byte, a, e int) {
	for k := a; k < e; k++ {
		if b[k] != '\n' {
			b[k] = ' '
		}
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// idents is the identifiers of s with their byte ranges (numbers are skipped).
func idents(s string) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case isDigit(c) || c == '.' && i+1 < len(s) && isDigit(s[i+1]):
			i++
			for i < len(s) && (isIdentByte(s[i]) || s[i] == '.') {
				i++
			}
		case isAlpha(c) || c == '_':
			start := i
			for i < len(s) && isIdentByte(s[i]) {
				i++
			}
			out = append(out, [2]int{start, i})
		default:
			i++
		}
	}
	return out
}

// reserved is the GLSL 450 keywords and built-ins that WebGL code may use as plain names.
var reserved = []string{
	"texture", "sample", "input", "output", "filter", "common", "partition", "active", "buffer", "shared", "patch",
	"subroutine", "precise", "resource", "coherent", "volatile", "restrict", "readonly", "writeonly", "noperspective",
	"superp", "atomic_uint", "image2D", "smooth", "flat", "centroid",
}

func isReserved(w string) bool { return slices.Contains(reserved, w) }

// isES3 reports whether the first `#version` line names 300 or later.
func isES3(text string) bool {
	for _, l := range lines(text) {
		v, ok := strings.CutPrefix(trimSpace(l), "#version")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			return false
		}
		// Rust's `u32::from_str` takes one leading `+`.
		n, err := strconv.ParseUint(strings.TrimPrefix(f[0], "+"), 10, 32)
		return err == nil && n >= 300
	}
	return false
}

// Transpile rewrites a WebGL fragment shader as Vulkan GLSL 450 and collects its uniforms (shader.rs
// `transpile`). The uniforms' Member is the block name before the `_nrb` suffix that Parse adds.
func Transpile(src string) (string, []Uniform, error) {
	clean := blankComments(src)
	text := string(clean)
	es3 := isES3(text)
	var uniforms []Uniform
	outVar := ""
	hasOut := false
	var blank [][2]int
	// Top-level statements (brace depth 0) and preprocessor lines.
	i, depth, start, lineStart := 0, 0, 0, true
	for i < len(text) {
		c := text[i]
		if lineStart && (c == ' ' || c == '\t' || c == '\r') {
			i++
			continue
		}
		if lineStart && c == '#' {
			end := i
			for end < len(text) && text[end] != '\n' {
				if text[end] == '\\' {
					end += 2
				} else {
					end++
				}
			}
			end = min(end, len(text))
			directive := trimLeftSpace(strings.TrimLeft(text[i:end], "#"))
			if strings.HasPrefix(directive, "version") || strings.HasPrefix(directive, "extension") {
				blank = append(blank, [2]int{i, end})
			}
			if depth == 0 {
				start = end
			}
			i = end
			continue
		}
		lineStart = c == '\n'
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				start = i + 1
			}
		case ';':
			if depth == 0 {
				if err := statement(src, text, start, i, &uniforms, &outVar, &hasOut, &blank); err != nil {
					return "", nil, err
				}
				start = i + 1
			}
		}
		i++
	}
	for _, r := range blank {
		blankRange(clean, r[0], r[1])
	}
	body := string(clean)

	repl := map[string]string{}
	for _, r := range reserved {
		if !(es3 && r == "texture") {
			repl[r] = "nr_" + r
		}
	}
	for k := range uniforms {
		uniforms[k].Member = uniforms[k].Name
		if isReserved(uniforms[k].Name) {
			uniforms[k].Member = "nr_" + uniforms[k].Name
		}
	}
	if !es3 {
		repl["texture2D"] = "texture"
		repl["texture2DLod"] = "textureLod"
		repl["texture2DLodEXT"] = "textureLod"
		repl["texture2DProj"] = "textureProj"
		repl["gl_FragColor"] = "nr_FragColor"
	}
	if hasOut {
		repl[outVar] = "nr_FragColor"
	}
	repl["gl_FragCoord"] = "nr_FragCoord"
	repl["main"] = "nr_main"
	for _, q := range []string{"highp", "mediump", "lowp"} {
		repl[q] = ""
	}
	var block, samplers strings.Builder
	binding := 1
	for _, u := range uniforms {
		m := u.Member
		switch t := u.Type; {
		case t == "sampler2D":
			fmt.Fprintf(&samplers, "layout(set = 0, binding = %d) uniform texture2D %s_nrt; layout(set = 0, binding = %d) uniform sampler %s_nrs; ", binding, m, binding+1, m)
			binding += 2
			repl[u.Name] = "sampler2D(" + m + "_nrt, " + m + "_nrs)"
		case t == "bool":
			block.WriteString("int " + m + "_nrb; ")
			repl[u.Name] = "bool(" + m + "_nrb)"
		case strings.HasPrefix(t, "bvec"):
			block.WriteString("i" + t[1:] + " " + m + "_nrb; ")
			repl[u.Name] = t + "(" + m + "_nrb)"
		default:
			block.WriteString(t + " " + m + "; ")
		}
	}
	isUniform := func(w string) bool {
		for _, u := range uniforms {
			if u.Name == w {
				return true
			}
		}
		return false
	}
	var code strings.Builder
	code.Grow(len(body) + 512)
	last, hasMain, writesColor := 0, false, hasOut
	for _, t := range idents(body) {
		a, e := t[0], t[1]
		word := body[a:e]
		if word == "main" && strings.HasPrefix(trimLeftSpace(body[e:]), "(") {
			hasMain = true
		}
		writesColor = writesColor || !es3 && word == "gl_FragColor"
		// A uniform's name after `.` is a swizzle or a member (`c.b`), not the uniform.
		afterDot := strings.HasSuffix(trimRightSpace(body[:a]), ".")
		if rep, ok := repl[word]; ok && (!afterDot || isReserved(word) || !isUniform(word)) {
			code.WriteString(body[last:a])
			code.WriteString(rep)
			last = e
		}
	}
	code.WriteString(body[last:])
	if !hasMain {
		return "", nil, errors.New("main() is missing!")
	}
	if !writesColor {
		return "", nil, errors.New("No color output found. Expected `out vec4 <name>` or `gl_FragColor`.")
	}
	glsl := "#version 450\nlayout(std140, set = 0, binding = 0) uniform NrUniforms { " + block.String() + "vec4 nr_unit; }; " + samplers.String() +
		"layout(location = 0) out vec4 nr_FragColor; vec4 nr_FragCoord;\n" + code.String() +
		"\nvoid main() { nr_FragCoord = vec4(gl_FragCoord.x * nr_unit.x, nr_unit.z - gl_FragCoord.y * nr_unit.y, 0.0, 1.0); nr_FragColor = vec4(0.0); nr_main(); }\n"
	return glsl, uniforms, nil
}

var uniformTypes = []string{"float", "int", "bool", "vec2", "vec3", "vec4", "ivec2", "ivec3", "ivec4", "bvec2", "bvec3", "bvec4", "mat2", "mat3", "mat4", "sampler2D"}

// statement handles one top-level statement text[start:end] (end is its `;`): uniform, precision and in/out
// declarations.
func statement(src, text string, start, end int, uniforms *[]Uniform, outVar *string, hasOut *bool, blank *[][2]int) error {
	stmt := text[start:end]
	var words []string
	for _, t := range idents(stmt) {
		if w := stmt[t[0]:t[1]]; w != "highp" && w != "mediump" && w != "lowp" {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return nil
	}
	first := words[0]
	quals := words
	if first == "layout" {
		k := 0
		for k < len(words) && words[k] != "out" && words[k] != "in" {
			k++
		}
		quals = words[k:]
	}
	head := first
	if len(quals) > 0 {
		head = quals[0]
	}
	switch head {
	case "precision":
		*blank = append(*blank, [2]int{start, end + 1})
	case "in", "varying", "attribute":
		return errors.New("The shader must not use anything but gl_FragCoord and uniforms!")
	case "out":
		if *hasOut || strings.ContainsAny(stmt, ",[") || len(quals) != 3 || quals[1] != "vec4" {
			return errors.New("Must use a single `out vec4` variable!")
		}
		*outVar, *hasOut = quals[2], true
		*blank = append(*blank, [2]int{start, end + 1})
	case "uniform":
		if len(words) < 2 {
			return errors.New("uniform without a type")
		}
		ty := words[1]
		if strings.Contains(stmt, "[") {
			return errf("Unsupported uniform type: uniform arrays (%s)", trimSpace(stmt))
		}
		if !slices.Contains(uniformTypes, ty) {
			return errf("Unsupported uniform type: %s", ty)
		}
		at := start + max(strings.Index(stmt, "uniform"), 0)
		n, err := parseNotes(src, at)
		if err != nil {
			return err
		}
		name := ""
		if len(words) > 2 {
			name = words[2]
		}
		k, err := kindOf(name, ty, &n)
		if err != nil {
			return err
		}
		for _, w := range words[2:] {
			*uniforms = append(*uniforms, Uniform{Name: w, Member: w, Type: ty, Kind: k})
		}
		*blank = append(*blank, [2]int{start, end + 1})
	}
	return nil
}
