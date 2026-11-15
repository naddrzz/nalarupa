package script

// A script's header: `/** @schema X */` (required; the output is migrated from that version) and
// `/** @input name: type(args) = default */`, which declares `nalarupa.input.name`.

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

type inputKind uint8

const (
	kindNumber inputKind = iota
	kindText
	kindBoolean
	kindColor
	kindRef
	kindEnum
)

type input struct {
	name string
	kind inputKind
	// min and max bound a number (nil: unbounded); options are an enum's.
	min, max *float64
	options  []string
	def      jsonv.Value
}

type tokKind uint8

const (
	tokIdent tokKind = iota
	tokNum
	tokStr
	tokHash
	tokPunct
	tokBool
	tokEnd
)

type tok struct {
	kind tokKind
	s    string // Ident, Num, Str and Hash text; Punct's character
	b    bool
}

func punct(c rune) tok { return tok{kind: tokPunct, s: string(c)} }

func (t tok) is(c rune) bool { return t.kind == tokPunct && t.s == string(c) }

func (t tok) show() string {
	switch t.kind {
	case tokIdent, tokNum, tokStr, tokHash:
		if t.s != "" {
			return "'" + t.s + "'"
		}
		return map[tokKind]string{tokIdent: "identifier", tokNum: "number", tokStr: "string", tokHash: "hash literal"}[t.kind]
	case tokPunct:
		return "'" + t.s + "'"
	case tokBool:
		return fmt.Sprintf("'%t'", t.b)
	}
	return "end of input"
}

func (t tok) literal() bool { return t.kind != tokPunct && t.kind != tokEnd }

func isASCIIDigit(c rune) bool { return c >= '0' && c <= '9' }

func isASCIIAlpha(c rune) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isHex(c rune) bool { return isASCIIDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// lex is the tokens of one `@input` directive.
func lex(src string) ([]tok, error) {
	c := []rune(src)
	var out []tok
	word := func(ch rune) bool { return isASCIIAlpha(ch) || isASCIIDigit(ch) || ch == '_' }
	for i := 0; i < len(c); {
		ch, start := c[i], i
		if unicode.IsSpace(ch) {
			i++
			continue
		}
		i++
		switch {
		case strings.ContainsRune("(),:=", ch):
			out = append(out, punct(ch))
		case ch == '"':
			var s []rune
			for i < len(c) && c[i] != '"' {
				if c[i] == '\\' && i+1 < len(c) {
					i++
				}
				s = append(s, c[i])
				i++
			}
			if i >= len(c) {
				return nil, errorString("Unterminated string")
			}
			i++
			out = append(out, tok{kind: tokStr, s: string(s)})
		case ch == '#':
			for i < len(c) && isHex(c[i]) {
				i++
			}
			if i == start+1 {
				return nil, errorString("Expected hex digits after '#'")
			}
			out = append(out, tok{kind: tokHash, s: string(c[start:i])})
		case ch == '-' || ch == '.' || isASCIIDigit(ch):
			for i < len(c) && isASCIIDigit(c[i]) {
				i++
			}
			if i < len(c) && c[i] == '.' && ch != '.' {
				i++
				for i < len(c) && isASCIIDigit(c[i]) {
					i++
				}
			}
			s := string(c[start:i])
			if s == "-" || s == "." {
				return nil, fmt.Errorf("Invalid number '%s'", s)
			}
			out = append(out, tok{kind: tokNum, s: s})
		case isASCIIAlpha(ch) || ch == '_':
			for i < len(c) && word(c[i]) {
				i++
			}
			switch s := string(c[start:i]); s {
			case "true", "false":
				out = append(out, tok{kind: tokBool, b: s == "true"})
			default:
				out = append(out, tok{kind: tokIdent, s: s})
			}
		default:
			return nil, fmt.Errorf("Unexpected character '%c'", ch)
		}
	}
	return append(out, tok{kind: tokEnd}), nil
}

// parseNum is Rust's `str::parse::<f64>`.
func parseNum(s string) (float64, bool) { return resolve.ParseFloat(s) }

type namedArg struct {
	k string
	v tok
}

// parseInput is `name: type(args) = default`, with validation messages.
func parseInput(src string) (input, error) {
	toks, err := lex(src)
	if err != nil {
		return input{}, err
	}
	pos := 0
	// next and peek past the end are None in Rust, which no comparison matches.
	next := func() (tok, bool) {
		if pos >= len(toks) {
			return tok{}, false
		}
		pos++
		return toks[pos-1], true
	}
	peekIs := func(c rune) bool { return pos < len(toks) && toks[pos].is(c) }
	orEnd := func(t tok, ok bool) tok {
		if !ok {
			return tok{kind: tokEnd}
		}
		return t
	}
	first, _ := next()
	if first.kind != tokIdent {
		return input{}, errorString("@input must start with a name")
	}
	name := first.s
	if t, ok := next(); !ok || !t.is(':') {
		return input{}, fmt.Errorf("@input '%s': expected ':'", name)
	}
	tyTok, ok := next()
	if !ok || tyTok.kind != tokIdent {
		return input{}, fmt.Errorf("@input '%s' is missing a type", name)
	}
	ty := tyTok.s
	var positional []tok
	var named []namedArg
	if peekIs('(') {
		next()
		if peekIs(')') {
			next()
		} else {
			for {
				t := orEnd(next())
				if t.kind == tokIdent && peekIs('=') {
					next()
					v := orEnd(next())
					if !v.literal() {
						return input{}, fmt.Errorf("@input '%s' arg '%s' must be a literal, got %s", name, t.s, v.show())
					}
					named = append(named, namedArg{t.s, v})
				} else if t.literal() {
					positional = append(positional, t)
				} else {
					return input{}, fmt.Errorf("@input '%s' arg must be a literal, got %s", name, t.show())
				}
				if peekIs(',') {
					next()
					continue
				}
				break
			}
			if t := orEnd(next()); !t.is(')') {
				return input{}, fmt.Errorf("@input '%s' arg list: expected ')', got %s", name, t.show())
			}
		}
	}
	var def *tok
	if peekIs('=') {
		next()
		if t, ok := next(); ok {
			def = &t
		}
	}
	if t, ok := next(); ok && t.kind != tokEnd {
		return input{}, fmt.Errorf("@input '%s' has unexpected trailing token %s", name, t.show())
	}
	noArgs := func() error {
		if len(positional) == 0 && len(named) == 0 {
			return nil
		}
		return fmt.Errorf("@input '%s' of type '%s' does not accept arguments", name, ty)
	}
	badDefault := func(what string, t *tok) error {
		return fmt.Errorf("@input '%s' %s, got %s", name, what, t.show())
	}
	in := input{name: name}
	switch ty {
	case "number":
		if len(positional) > 0 {
			return input{}, fmt.Errorf("@input number '%s' does not accept positional arguments", name)
		}
		in.kind = kindNumber
		for _, a := range named {
			if a.v.kind != tokNum {
				return input{}, fmt.Errorf("@input number '%s' arg '%s' must be a number, got %s", name, a.k, a.v.show())
			}
			var bound *float64
			if f, ok := parseNum(a.v.s); ok {
				bound = &f
			}
			switch a.k {
			case "min":
				in.min = bound
			case "max":
				in.max = bound
			default:
				return input{}, fmt.Errorf("@input number '%s' has unknown arg '%s'. Valid args: min, max", name, a.k)
			}
		}
		d := 0.0
		if def != nil {
			if def.kind != tokNum {
				return input{}, badDefault("default must be a number", def)
			}
			if f, ok := parseNum(def.s); ok {
				d = f
			}
		}
		in.def = jsonv.Float(d)
	case "string":
		if err := noArgs(); err != nil {
			return input{}, err
		}
		in.kind, in.def = kindText, ""
		if def != nil {
			if def.kind != tokStr {
				return input{}, badDefault("string default must be a quoted string", def)
			}
			in.def = def.s
		}
	case "boolean":
		if err := noArgs(); err != nil {
			return input{}, err
		}
		in.kind, in.def = kindBoolean, false
		if def != nil {
			if def.kind != tokBool {
				return input{}, badDefault("default must be 'true' or 'false'", def)
			}
			in.def = def.b
		}
	case "color":
		if err := noArgs(); err != nil {
			return input{}, err
		}
		in.kind, in.def = kindColor, "#000000"
		if def != nil {
			if def.kind != tokHash && def.kind != tokStr {
				return input{}, badDefault("color default must be a hex literal like #RRGGBB", def)
			}
			in.def = def.s
		}
	case "ref":
		if err := noArgs(); err != nil {
			return input{}, err
		}
		in.kind, in.def = kindRef, ""
		if def != nil {
			if def.kind != tokStr && def.kind != tokIdent {
				return input{}, badDefault("ref default must be an identifier or quoted string", def)
			}
			in.def = def.s
		}
	case "enum":
		if len(named) > 0 {
			return input{}, fmt.Errorf("@input enum '%s' does not accept named arguments", name)
		}
		if len(positional) == 0 {
			return input{}, fmt.Errorf("@input enum '%s' must specify at least one option", name)
		}
		in.kind = kindEnum
		for _, t := range positional {
			if t.kind != tokStr {
				return input{}, fmt.Errorf("@input enum '%s' option must be a quoted string, got %s", name, t.show())
			}
			in.options = append(in.options, t.s)
		}
		in.def = in.options[0]
		if def != nil {
			switch {
			case def.kind != tokStr:
				return input{}, badDefault("enum default must be a quoted string", def)
			case !slices.Contains(in.options, def.s):
				return input{}, fmt.Errorf("@input enum '%s' default '%s' is not one of the options", name, def.s)
			}
			in.def = def.s
		}
	default:
		return input{}, fmt.Errorf("Invalid @input type '%s'. Must be one of: number, string, boolean, color, ref, enum", ty)
	}
	return in, nil
}

// lines is Rust's `str::lines`: split after each `\n`, the `\n` or `\r\n` that ends a line stripped (a lone
// `\r` at the very end stays), no empty last line.
func lines(s string) []string {
	out := strings.SplitAfter(s, "\n")
	if out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	for i, l := range out {
		if l, ok := strings.CutSuffix(l, "\n"); ok {
			out[i] = strings.TrimSuffix(l, "\r")
		}
	}
	return out
}

func isWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// scriptHeader is the `@schema` (hasSchema false when absent) and `@input` directives of every `/** … */`
// block of a script. A later input of the same name replaces the earlier one and moves to the end.
func scriptHeader(src string) (schema string, hasSchema bool, inputs []input, err error) {
	rest := src
	for {
		open := strings.Index(rest, "/**")
		if open < 0 {
			break
		}
		body := rest[open+3:]
		end := strings.Index(body, "*/")
		if end < 0 {
			break
		}
		for _, line := range lines(body[:end]) {
			l := strings.TrimLeftFunc(line, unicode.IsSpace)
			if r, ok := strings.CutPrefix(l, "*"); ok {
				l = strings.TrimPrefix(r, " ")
			}
			l = strings.TrimSpace(l)
			d, ok := strings.CutPrefix(l, "@")
			if !ok {
				continue
			}
			n := 0
			for n < len(d) && isWord(d[n]) {
				n++
			}
			if n == 0 {
				continue
			}
			switch d[:n] {
			case "schema":
				schema, hasSchema = strings.TrimSpace(d[n:]), true
			case "input":
				in, err := parseInput(strings.TrimSpace(d[n:]))
				if err != nil {
					return "", false, nil, err
				}
				inputs = slices.DeleteFunc(inputs, func(x input) bool { return x.name == in.name })
				inputs = append(inputs, in)
			}
		}
		rest = body[end+2:]
	}
	return schema, hasSchema, inputs, nil
}

// scriptInputs is `nalarupa.input`: the declared inputs only, stored values coerced (numbers clamped).
func scriptInputs(decl []input, given *jsonv.Object) (*jsonv.Object, *Status) {
	out := jsonv.NewObject()
	for _, in := range decl {
		v := in.def
		if given != nil {
			if g, ok := given.Get(in.name); ok && g != nil {
				v = g
			}
		}
		switch in.kind {
		case kindNumber:
			x, ok := jsonv.F64(v)
			if !ok {
				if s, isStr := jsonv.Str(v); isStr {
					x, ok = parseNum(strings.TrimSpace(s))
				}
			}
			if !ok || math.IsNaN(x) {
				x = 0
				if in.min != nil {
					x = *in.min
				}
			}
			if in.min != nil {
				x = math.Max(x, *in.min)
			}
			if in.max != nil {
				x = math.Min(x, *in.max)
			}
			v = jsonv.Float(x)
		case kindEnum:
			s, isStr := jsonv.Str(v)
			if !isStr || !slices.Contains(in.options, s) {
				shown := s
				if !isStr {
					shown = string(jsonv.Marshal(v))
				}
				return nil, errStatus("Invalid input value", fmt.Sprintf("Value '%s' is not allowed for input '%s'. Must be one of: %s", shown, in.name, strings.Join(in.options, ", ")))
			}
		default:
			v = jsonv.Clone(v)
		}
		out.Set(in.name, v)
	}
	return out, nil
}
