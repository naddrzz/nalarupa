package jsonv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// maxDepth matches serde_json's recursion limit: it refuses the 128th nested array or object.
const maxDepth = 127

// Parse decodes one JSON value as serde_json does: what it refuses is refused with its message, line and column.
func Parse(data []byte) (Value, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("invalid UTF-8")
	}
	// serde_json's own message and position for anything it refuses (encoding/json accepts some of it, such
	// as numbers out of range, and words the rest differently).
	if err := serdeCheck(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decode(dec, 0)
	if err == nil {
		if _, e := dec.Token(); e != io.EOF {
			err = errors.New("trailing characters")
		}
	}
	if err != nil {
		off := dec.InputOffset()
		var se *json.SyntaxError
		if errors.As(err, &se) {
			off = se.Offset
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			err, off = errors.New("unexpected end of input"), int64(len(data))
		}
		line, col := position(data, off)
		return nil, fmt.Errorf("%v at line %d column %d", err, line, col)
	}
	return v, nil
}

// MustParse is Parse for literals known to be valid; it panics otherwise.
func MustParse(s string) Value {
	v, err := Parse([]byte(s))
	if err != nil {
		panic(err)
	}
	return v
}

func position(data []byte, off int64) (line, col int) {
	if off > int64(len(data)) {
		off = int64(len(data))
	}
	before := data[:off]
	line = bytes.Count(before, []byte("\n")) + 1
	col = utf8.RuneCount(before[bytes.LastIndexByte(before, '\n')+1:])
	return line, col
}

func decode(dec *json.Decoder, depth int) (Value, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		if x != '[' && x != '{' {
			return nil, fmt.Errorf("unexpected %q", rune(x))
		}
		if depth >= maxDepth {
			return nil, errors.New("recursion limit exceeded")
		}
		if x == '[' {
			a := &Array{Items: []Value{}}
			for dec.More() {
				v, err := decode(dec, depth+1)
				if err != nil {
					return nil, err
				}
				a.Items = append(a.Items, v)
			}
			_, err := dec.Token()
			return a, err
		}
		o := NewObject()
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decode(dec, depth+1)
			if err != nil {
				return nil, err
			}
			// A repeated key keeps its first position and takes the last value, as serde_json.
			o.Set(kt.(string), v)
		}
		_, err := dec.Token()
		return o, err
	case json.Number:
		return Number(x), nil
	}
	return t, nil // nil, bool or string
}

// Marshal encodes v compactly, as serde_json's `to_string`.
func Marshal(v Value) []byte {
	var b strings.Builder
	encode(&b, v, -1)
	return []byte(b.String())
}

// MarshalPretty encodes v as serde_json's `to_string_pretty`: two-space indent, `": "` between key
// and value, no trailing newline, and only `"`, `\` and control characters escaped.
func MarshalPretty(v Value) []byte {
	var b strings.Builder
	encode(&b, v, 0)
	return []byte(b.String())
}

// encode writes v; indent < 0 is compact, otherwise the current nesting level.
func encode(b *strings.Builder, v Value, indent int) {
	newline := func(level int) {
		if indent >= 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat("  ", level))
		}
	}
	next := indent
	if indent >= 0 {
		next = indent + 1
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case Number:
		b.WriteString(string(x))
	case string:
		writeString(b, x)
	case *Array:
		if len(x.Items) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i, it := range x.Items {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(next)
			encode(b, it, next)
		}
		newline(indent)
		b.WriteByte(']')
	case *Object:
		if x.Len() == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			newline(next)
			writeString(b, k)
			b.WriteByte(':')
			if indent >= 0 {
				b.WriteByte(' ')
			}
			encode(b, x.vals[i], next)
		}
		newline(indent)
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("jsonv: not a JSON value: %T", v))
	}
}

const hexDigits = "0123456789abcdef"

func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\f':
			b.WriteString(`\f`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20:
			b.WriteString(`\u00`)
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}
