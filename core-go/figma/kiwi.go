package figma

// kiwi (github.com/evanw/kiwi) binary schema and message decoding.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

var errEOF = errors.New("truncated Figma clipboard data")

type reader struct {
	d []byte
	i int
}

func (b *reader) byte() (byte, error) {
	if b.i >= len(b.d) {
		return 0, errEOF
	}
	c := b.d[b.i]
	b.i++
	return c, nil
}

func (b *reader) uint() (uint32, error) {
	var v, s uint32
	for {
		c, err := b.byte()
		if err != nil {
			return 0, err
		}
		v |= uint32(c&127) << s
		s += 7
		if c&128 == 0 || s >= 35 {
			return v, nil
		}
	}
}

func (b *reader) int() (int32, error) {
	v, err := b.uint()
	if v&1 == 1 {
		return ^int32(v >> 1), err
	}
	return int32(v >> 1), err
}

func (b *reader) uint64() (uint64, error) {
	var v uint64
	var s uint
	for {
		c, err := b.byte()
		if err != nil {
			return 0, err
		}
		if c&128 == 0 || s >= 56 {
			return v | uint64(c)<<s, nil
		}
		v |= uint64(c&127) << s
		s += 7
	}
}

// float is one zero byte for 0, else 4 bytes with the exponent rotated to the front.
func (b *reader) float() (float32, error) {
	if b.i >= len(b.d) {
		return 0, errEOF
	}
	if b.d[b.i] == 0 {
		b.i++
		return 0, nil
	}
	if b.i+4 > len(b.d) {
		return 0, errEOF
	}
	v := binary.LittleEndian.Uint32(b.d[b.i:])
	b.i += 4
	return math.Float32frombits(bits.RotateLeft32(v, 23)), nil
}

func (b *reader) string() (string, error) {
	rest := b.d[b.i:]
	n := bytes.IndexByte(rest, 0)
	if n < 0 {
		return "", errEOF
	}
	b.i += n + 1
	return lossy(rest[:n]), nil
}

func (b *reader) left() int { return max(len(b.d)-b.i, 0) }

// lossy is Rust's String::from_utf8_lossy: each maximal invalid subpart becomes one U+FFFD.
func lossy(s []byte) string {
	if utf8.Valid(s) {
		return string(s)
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		if r, n := utf8.DecodeRune(s[i:]); r != utf8.RuneError || n > 1 {
			out.Write(s[i : i+n])
			i += n
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += invalidLen(s[i:])
	}
	return out.String()
}

// invalidLen is the length of the invalid sequence at the start of s (Rust's Utf8Chunks).
func invalidLen(s []byte) int {
	cont := func(i int) bool { return i < len(s) && s[i]&0xc0 == 0x80 }
	in := func(i int, lo, hi byte) bool { return i < len(s) && s[i] >= lo && s[i] <= hi }
	c := s[0]
	switch {
	case c >= 0xc2 && c <= 0xdf:
		return 1
	case c >= 0xe0 && c <= 0xef:
		ok := (c == 0xe0 && in(1, 0xa0, 0xbf)) || (c >= 0xe1 && c <= 0xec && in(1, 0x80, 0xbf)) ||
			(c == 0xed && in(1, 0x80, 0x9f)) || (c >= 0xee && in(1, 0x80, 0xbf))
		if !ok {
			return 1
		}
		return 2
	case c >= 0xf0 && c <= 0xf4:
		ok := (c == 0xf0 && in(1, 0x90, 0xbf)) || (c >= 0xf1 && c <= 0xf3 && in(1, 0x80, 0xbf)) || (c == 0xf4 && in(1, 0x80, 0x8f))
		if !ok {
			return 1
		}
		if !cont(2) {
			return 2
		}
		return 3
	}
	return 1
}

type tyKind uint8

const (
	tyBool tyKind = iota
	tyByte
	tyInt
	tyUint
	tyFloat
	tyStr
	tyInt64
	tyUint64
	tyDef
)

type ty struct {
	kind tyKind
	def  int
}

type field struct {
	name  string
	ty    *ty // nil in enums
	array bool
	id    uint32
}

// def is an enum (kind 0), struct (1) or message (2).
type def struct {
	name   string
	kind   byte
	fields []field
}

type schema struct {
	defs []def
}

func readSchema(b []byte) (*schema, error) {
	bb := &reader{d: b}
	type rawField struct {
		name  string
		t     int32
		array bool
		id    uint32
	}
	type rawDef struct {
		name   string
		kind   byte
		fields []rawField
	}
	var raw []rawDef
	n, err := bb.uint()
	if err != nil {
		return nil, err
	}
	for range n {
		name, err := bb.string()
		if err != nil {
			return nil, err
		}
		kind, err := bb.byte()
		if err != nil {
			return nil, err
		}
		nf, err := bb.uint()
		if err != nil {
			return nil, err
		}
		var fields []rawField
		for range nf {
			var f rawField
			if f.name, err = bb.string(); err != nil {
				return nil, err
			}
			if f.t, err = bb.int(); err != nil {
				return nil, err
			}
			a, err := bb.byte()
			if err != nil {
				return nil, err
			}
			f.array = a&1 == 1
			if f.id, err = bb.uint(); err != nil {
				return nil, err
			}
			fields = append(fields, f)
		}
		raw = append(raw, rawDef{name, kind, fields})
	}
	s := &schema{}
	for _, r := range raw {
		if r.kind > 2 {
			return nil, fmt.Errorf("bad Figma schema kind %d", r.kind)
		}
		d := def{name: r.name, kind: r.kind}
		for _, f := range r.fields {
			var t *ty
			switch {
			case r.kind == 0:
			case f.t < 0:
				k := ^f.t
				if k > 7 {
					return nil, fmt.Errorf("bad Figma schema type %d", f.t)
				}
				t = &ty{kind: tyKind(k)}
			case int(f.t) < len(raw):
				t = &ty{kind: tyDef, def: int(f.t)}
			default:
				return nil, fmt.Errorf("bad Figma schema type %d", f.t)
			}
			d.fields = append(d.fields, field{f.name, t, f.array, f.id})
		}
		s.defs = append(s.defs, d)
	}
	return s, nil
}

// decode reads one struct or message; byte arrays become lowercase hex strings.
func (s *schema) decode(bb *reader, di, depth int) (*jsonv.Object, error) {
	if depth > maxDepth {
		return nil, errors.New("Figma data nested too deep")
	}
	d := &s.defs[di]
	out := jsonv.NewObject()
	if d.kind == 2 {
		for {
			id, err := bb.uint()
			if err != nil {
				return nil, err
			}
			if id == 0 {
				return out, nil
			}
			var f *field
			for i := range d.fields {
				if d.fields[i].id == id {
					f = &d.fields[i]
					break
				}
			}
			if f == nil {
				return nil, errors.New("bad Figma message")
			}
			v, err := s.field(bb, f, depth)
			if err != nil {
				return nil, err
			}
			out.Set(f.name, v)
		}
	}
	for i := range d.fields {
		v, err := s.field(bb, &d.fields[i], depth)
		if err != nil {
			return nil, err
		}
		out.Set(d.fields[i].name, v)
	}
	return out, nil
}

func (s *schema) field(bb *reader, f *field, depth int) (jsonv.Value, error) {
	if f.ty == nil {
		return nil, errors.New("bad Figma schema")
	}
	if !f.array {
		return s.value(bb, f.ty, depth)
	}
	n, err := bb.uint()
	if err != nil {
		return nil, err
	}
	if uint64(n) > uint64(bb.left()) {
		return nil, errEOF
	}
	if f.ty.kind == tyByte {
		h := make([]byte, 0, 2*n)
		for _, c := range bb.d[bb.i : bb.i+int(n)] {
			h = append(h, hexDigits[c>>4], hexDigits[c&15])
		}
		bb.i += int(n)
		return string(h), nil
	}
	var items []jsonv.Value // not preallocated: n is only bounded by the bytes left
	for range n {
		v, err := s.value(bb, f.ty, depth)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return jsonv.NewArray(items...), nil
}

const hexDigits = "0123456789abcdef"

func (s *schema) value(bb *reader, t *ty, depth int) (jsonv.Value, error) {
	switch t.kind {
	case tyBool:
		c, err := bb.byte()
		return c != 0, err
	case tyByte:
		c, err := bb.byte()
		return jsonv.Int(int64(c)), err
	case tyInt:
		v, err := bb.int()
		return jsonv.Int(int64(v)), err
	case tyUint:
		v, err := bb.uint()
		return jsonv.Int(int64(v)), err
	case tyFloat:
		v, err := bb.float()
		return jsonv.Float(float64(v)), err
	case tyStr:
		return bb.string()
	case tyInt64:
		v, err := bb.uint64()
		if v&1 == 1 {
			return jsonv.Int(^int64(v >> 1)), err
		}
		return jsonv.Int(int64(v >> 1)), err
	case tyUint64:
		v, err := bb.uint64()
		return jsonv.Number(strconv.FormatUint(v, 10)), err
	}
	if s.defs[t.def].kind == 0 {
		v, err := bb.uint()
		if err != nil {
			return nil, err
		}
		for _, f := range s.defs[t.def].fields {
			if f.id == v {
				return f.name, nil
			}
		}
		return nil, nil
	}
	o, err := s.decode(bb, t.def, depth+1)
	if err != nil {
		return nil, err
	}
	return o, nil
}
