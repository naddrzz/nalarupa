package gpu

import (
	"errors"
	"strconv"
	"strings"
)

// headerLines is the number of lines the transpiler puts in front of the user's code (shader.rs
// `HEADER_LINES`), taken off naga's line numbers.
const headerLines = 2

// wgpu-native reports errors as wgpu-core's error tree: "Validation Error\n\nCaused by:\n" and then each
// error of the chain on its own line, indented two spaces per level. The Rust core goes through the wgpu
// crate, which formats the same tree with its own entry point names.
const causedBy = "Validation Error\n\nCaused by:\n"

// unwrap is the first error of a wgpu-native error tree, as Display prints it.
func unwrap(msg string) string {
	if rest, ok := strings.CutPrefix(msg, causedBy); ok {
		return strings.TrimSuffix(strings.TrimPrefix(rest, "  "), "\n")
	}
	return msg
}

// adapterMessage is the request-adapter error as the Rust core words it. wgpu reports each backend the
// instance did not start as "not requested"; the Rust core builds wgpu without GL and the noop backend, so
// those it was asked for (uncompiled) are "support not compiled in" there (and never reach wgpu-native here).
func adapterMessage(msg string, uncompiled []string) string {
	msg = unwrap(msg)
	for _, b := range uncompiled {
		msg = strings.Replace(msg, b+" not requested", b+" support not compiled in", 1)
	}
	return msg
}

// noBackends is wgpu's request-adapter error for an instance with no backends but the uncompiled ones.
func noBackends(uncompiled []string) string {
	return adapterMessage("No suitable graphics adapter found; noop not requested, vulkan not requested, metal not requested, dx12 not requested, gl not requested, webgpu not requested", uncompiled)
}

// entryPoints maps wgpu-native's entry points in error trees to the wgpu crate's.
var entryPoints = strings.NewReplacer(
	"In wgpuDeviceCreateShaderModule", "In Device::create_shader_module",
	"In wgpuDeviceCreateRenderPipeline", "In Device::create_render_pipeline",
	"In wgpuDeviceCreateBindGroupLayout", "In Device::create_bind_group_layout",
	"In wgpuDeviceCreatePipelineLayout", "In Device::create_pipeline_layout",
)

// compileError is "shader compile: " and the error tree as the wgpu crate formats it.
func compileError(msg string) error {
	return errors.New("shader compile: " + entryPoints.Replace(msg))
}

// moduleError maps the error of creating the fragment module from glsl to shader.rs `compile`'s: naga's
// parse errors as `line {N-2}: {kind}` (one per line), its validation error as `shader validation: …`.
func moduleError(msg, glsl string) error {
	const validation = "Shader validation error: "
	if i := strings.Index(msg, "Error { kind: "); i >= 0 {
		if lines, ok := parseErrors(msg[i:], glsl); ok {
			return errors.New(strings.Join(lines, "\n"))
		}
	} else if i := strings.Index(msg, validation); i >= 0 {
		rest := msg[i+len(validation):]
		if j := strings.IndexByte(rest, '\n'); j >= 0 {
			rest = rest[:j]
		}
		return errors.New("shader validation: " + rest)
	}
	return compileError(msg)
}

// parseErrors reads naga's GLSL `ParseErrors`, which wgpu-native prints with Debug (`Error { kind: …, meta:
// Span { start: …, end: … } }` per error), and words each as shader.rs does with Display.
func parseErrors(s, glsl string) ([]string, bool) {
	var out []string
	for {
		s = strings.TrimLeft(s, " \n")
		if !strings.HasPrefix(s, "Error {") {
			break
		}
		p := &debugParser{s: s}
		v, ok := p.value()
		if !ok || v.name != "Error" {
			return nil, false
		}
		kind, meta := v.field("kind"), v.field("meta")
		if kind == nil || meta == nil {
			return nil, false
		}
		start, _ := strconv.Atoi(meta.field("start").raw)
		line := 1
		if start > 0 && start <= len(glsl) {
			line += strings.Count(glsl[:start], "\n")
		}
		out = append(out, "line "+strconv.Itoa(max(line-headerLines, 0))+": "+kindDisplay(kind))
		s = s[p.i:]
	}
	return out, len(out) > 0
}

// kindDisplay is naga's `glsl::ErrorKind` Display from its Debug form.
func kindDisplay(k *debugValue) string {
	arg := func(i int) string {
		if i < len(k.args) {
			return k.args[i].text()
		}
		return ""
	}
	field := func(name string) string {
		if f := k.field(name); f != nil {
			return f.raw
		}
		return ""
	}
	switch k.name {
	case "EndOfFile":
		return "Unexpected end of file"
	case "InvalidProfile":
		return "Invalid profile: " + arg(0)
	case "InvalidVersion":
		return "Invalid version: " + arg(0)
	case "InvalidToken":
		if len(k.args) == 2 {
			var exp []string
			for _, e := range k.args[1].args {
				exp = append(exp, expectedDisplay(e))
			}
			return "Expected " + joinWithComma(exp) + ", found " + k.args[0].raw
		}
	case "NotImplemented":
		return "Not implemented: " + arg(0)
	case "UnknownVariable":
		return "Unknown variable: " + arg(0)
	case "UnknownType":
		return "Unknown type: " + arg(0)
	case "UnknownField":
		return "Unknown field: " + arg(0)
	case "UnknownLayoutQualifier":
		return "Unknown layout qualifier: " + arg(0)
	case "UnsupportedMatrixWithTwoRowsInStd140":
		return "unsupported matrix of the form matCx2 (in this case mat" + field("columns") +
			"x2) in std140 block layout. See https://github.com/gfx-rs/wgpu/issues/4375"
	case "UnsupportedF16MatrixInStd140":
		return "unsupported matrix of the form f16matCxR (in this case f16mat" + field("columns") + "x" + field("rows") +
			") in std140 block layout. See https://github.com/gfx-rs/wgpu/issues/4375"
	case "VariableAlreadyDeclared":
		return "Variable already declared: " + arg(0)
	case "SemanticError":
		return arg(0)
	case "PreprocessorError":
		if len(k.args) == 1 {
			return k.args[0].raw
		}
	case "InternalError":
		return "Internal error: " + arg(0)
	}
	return k.raw
}

// expectedDisplay is naga's `ExpectedToken` Display.
func expectedDisplay(e *debugValue) string {
	switch e.name {
	case "Token":
		if len(e.args) == 1 {
			return e.args[0].raw
		}
	case "TypeName":
		return "a type"
	case "Identifier":
		return "identifier"
	case "IntLiteral":
		return "integer literal"
	case "FloatLiteral":
		return "float literal"
	case "BoolLiteral":
		return "bool literal"
	case "Eof":
		return "end of file"
	}
	return e.raw
}

// joinWithComma is naga's `join_with_comma`: "a, b or c".
func joinWithComma(list []string) string {
	var b strings.Builder
	for i, v := range list {
		b.WriteString(v)
		switch {
		case i == len(list)-1:
		case i == len(list)-2:
			b.WriteString(" or ")
		default:
			b.WriteString(", ")
		}
	}
	return b.String()
}

// debugValue is a value in Rust's Debug notation: a string, a number, a path with tuple arguments or named
// fields, or a list; raw is its text as printed.
type debugValue struct {
	raw    string
	name   string
	str    *string
	args   []*debugValue
	fields []debugField
}

type debugField struct {
	name string
	v    *debugValue
}

func (v *debugValue) field(name string) *debugValue {
	if v == nil {
		return nil
	}
	for _, f := range v.fields {
		if f.name == name {
			return f.v
		}
	}
	return nil
}

// text is a string's contents, else the value as printed.
func (v *debugValue) text() string {
	if v.str != nil {
		return *v.str
	}
	return v.raw
}

type debugParser struct {
	s string
	i int
}

func (p *debugParser) space() {
	for p.i < len(p.s) && p.s[p.i] == ' ' {
		p.i++
	}
}

func (p *debugParser) eat(tok string) bool {
	p.space()
	if strings.HasPrefix(p.s[p.i:], tok) {
		p.i += len(tok)
		return true
	}
	return false
}

func isWord(c byte) bool {
	return c == '_' || c == ':' || c == '.' || c == '-' || c == '+' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func (p *debugParser) value() (*debugValue, bool) {
	p.space()
	start := p.i
	v := &debugValue{}
	switch {
	case p.i >= len(p.s):
		return nil, false
	case p.s[p.i] == '"':
		s, ok := p.str()
		if !ok {
			return nil, false
		}
		v.str = &s
	case p.s[p.i] == '[':
		p.i++
		items, ok := p.list("]")
		if !ok {
			return nil, false
		}
		v.args = items
	default:
		for p.i < len(p.s) && isWord(p.s[p.i]) {
			p.i++
		}
		if p.i == start {
			return nil, false
		}
		v.name = p.s[start:p.i]
		if p.i < len(p.s) && p.s[p.i] == '(' {
			p.i++
			items, ok := p.list(")")
			if !ok {
				return nil, false
			}
			v.args = items
		} else if strings.HasPrefix(p.s[p.i:], " {") {
			p.i += 2
			for !p.eat("}") {
				p.space()
				ns := p.i
				for p.i < len(p.s) && isWord(p.s[p.i]) && p.s[p.i] != ':' {
					p.i++
				}
				name := p.s[ns:p.i]
				if name == "" || !p.eat(":") {
					return nil, false
				}
				f, ok := p.value()
				if !ok {
					return nil, false
				}
				v.fields = append(v.fields, debugField{name, f})
				p.eat(",")
			}
		}
	}
	v.raw = p.s[start:p.i]
	return v, true
}

func (p *debugParser) list(end string) ([]*debugValue, bool) {
	var items []*debugValue
	for !p.eat(end) {
		v, ok := p.value()
		if !ok {
			return nil, false
		}
		items = append(items, v)
		p.eat(",")
	}
	return items, true
}

// str reads a Rust Debug string literal and unescapes it.
func (p *debugParser) str() (string, bool) {
	var b strings.Builder
	p.i++ // opening quote
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		switch c {
		case '"':
			return b.String(), true
		case '\\':
			if p.i >= len(p.s) {
				return "", false
			}
			e := p.s[p.i]
			p.i++
			switch e {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '0':
				b.WriteByte(0)
			case 'u':
				end := strings.IndexByte(p.s[p.i:], '}')
				if !strings.HasPrefix(p.s[p.i:], "{") || end < 0 {
					return "", false
				}
				r, err := strconv.ParseUint(p.s[p.i+1:p.i+end], 16, 32)
				if err != nil {
					return "", false
				}
				b.WriteRune(rune(r))
				p.i += end + 1
			default:
				b.WriteByte(e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}
