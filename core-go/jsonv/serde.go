package jsonv

import (
	"fmt"
	"math"
	"strconv"
)

// serdeCheck reads data as serde_json 1.0.154 reads a Value from a string (no `float_roundtrip`, no
// `arbitrary_precision`), only to find the first error it would report: its message and position, column counted
// in bytes. nil when serde_json reads it.
func serdeCheck(data []byte) error {
	s := &serdeScan{b: data, depth: maxDepth + 1}
	if err := s.value(); err != nil {
		return err
	}
	if _, ok := s.whitespace(); ok {
		return s.peekErr("trailing characters")
	}
	return nil
}

type serdeScan struct {
	b     []byte
	i     int
	depth int // serde's remaining_depth
}

type serdeError struct {
	msg       string
	line, col int
}

func (e *serdeError) Error() string {
	return fmt.Sprintf("%s at line %d column %d", e.msg, e.line, e.col)
}

// at is serde's SliceRead::position_of_index.
func (s *serdeScan) at(msg string, i int) error {
	start := 0
	line := 1
	for k := 0; k < i; k++ {
		if s.b[k] == '\n' {
			start = k + 1
			line++
		}
	}
	return &serdeError{msg, line, i - start}
}

// err is an error caused by the byte just read; peekErr one caused by the byte about to be read.
func (s *serdeScan) err(msg string) error     { return s.at(msg, s.i) }
func (s *serdeScan) peekErr(msg string) error { return s.at(msg, min(len(s.b), s.i+1)) }

func (s *serdeScan) peek() (byte, bool) {
	if s.i < len(s.b) {
		return s.b[s.i], true
	}
	return 0, false
}

func (s *serdeScan) peekOrNull() byte {
	c, _ := s.peek()
	return c
}

func (s *serdeScan) whitespace() (byte, bool) {
	for {
		c, ok := s.peek()
		if !ok || (c != ' ' && c != '\n' && c != '\t' && c != '\r') {
			return c, ok
		}
		s.i++
	}
}

func (s *serdeScan) value() error {
	c, ok := s.whitespace()
	if !ok {
		return s.peekErr("EOF while parsing a value")
	}
	switch {
	case c == 'n':
		s.i++
		return s.ident("ull")
	case c == 't':
		s.i++
		return s.ident("rue")
	case c == 'f':
		s.i++
		return s.ident("alse")
	case c == '-':
		s.i++
		return s.integer(false)
	case '0' <= c && c <= '9':
		return s.integer(true)
	case c == '"':
		s.i++
		return s.str()
	case c == '[':
		if s.depth--; s.depth == 0 {
			return s.peekErr("recursion limit exceeded")
		}
		s.i++
		err := s.seq()
		s.depth++
		if err != nil {
			return err
		}
		return s.endSeq()
	case c == '{':
		if s.depth--; s.depth == 0 {
			return s.peekErr("recursion limit exceeded")
		}
		s.i++
		err := s.mapping()
		s.depth++
		if err != nil {
			return err
		}
		return s.endMap()
	}
	return s.peekErr("expected value")
}

func (s *serdeScan) ident(rest string) error {
	for k := 0; k < len(rest); k++ {
		c, ok := s.peek()
		if !ok {
			return s.err("EOF while parsing a value")
		}
		s.i++
		if c != rest[k] {
			return s.err("expected ident")
		}
	}
	return nil
}

func (s *serdeScan) integer(positive bool) error {
	c, ok := s.peek()
	if !ok {
		return s.err("EOF while parsing a value")
	}
	s.i++
	switch {
	case c == '0':
		if d := s.peekOrNull(); '0' <= d && d <= '9' {
			return s.peekErr("invalid number")
		}
		return s.number(positive, 0)
	case '1' <= c && c <= '9':
		sig := uint64(c - '0')
		for {
			d := s.peekOrNull()
			if d < '0' || d > '9' {
				return s.number(positive, sig)
			}
			digit := uint64(d - '0')
			if sig >= math.MaxUint64/10 && (sig > math.MaxUint64/10 || digit > math.MaxUint64%10) {
				return s.longInteger(positive, sig)
			}
			s.i++
			sig = sig*10 + digit
		}
	}
	return s.err("invalid number")
}

func (s *serdeScan) number(positive bool, sig uint64) error {
	switch s.peekOrNull() {
	case '.':
		return s.decimal(positive, sig, 0)
	case 'e', 'E':
		return s.exponent(positive, sig, 0)
	}
	return nil
}

func (s *serdeScan) longInteger(positive bool, sig uint64) error {
	exp := 0
	for {
		switch d := s.peekOrNull(); {
		case '0' <= d && d <= '9':
			s.i++
			exp++
		case d == '.':
			return s.decimal(positive, sig, exp)
		case d == 'e' || d == 'E':
			return s.exponent(positive, sig, exp)
		default:
			return s.fromParts(sig, exp)
		}
	}
}

func (s *serdeScan) decimal(positive bool, sig uint64, before int) error {
	s.i++
	after := 0
	for {
		d := s.peekOrNull()
		if d < '0' || d > '9' {
			break
		}
		digit := uint64(d - '0')
		if sig >= math.MaxUint64/10 && (sig > math.MaxUint64/10 || digit > math.MaxUint64%10) {
			// parse_decimal_overflow: the remaining digits only move nothing.
			for d := s.peekOrNull(); '0' <= d && d <= '9'; d = s.peekOrNull() {
				s.i++
			}
			if e := s.peekOrNull(); e == 'e' || e == 'E' {
				return s.exponent(positive, sig, before+after)
			}
			return s.fromParts(sig, before+after)
		}
		s.i++
		sig = sig*10 + digit
		after--
	}
	if after == 0 {
		if _, ok := s.peek(); ok {
			return s.peekErr("invalid number")
		}
		return s.peekErr("EOF while parsing a value")
	}
	if e := s.peekOrNull(); e == 'e' || e == 'E' {
		return s.exponent(positive, sig, before+after)
	}
	return s.fromParts(sig, before+after)
}

func (s *serdeScan) exponent(positive bool, sig uint64, start int) error {
	s.i++
	positiveExp := true
	switch s.peekOrNull() {
	case '+':
		s.i++
	case '-':
		s.i++
		positiveExp = false
	}
	c, ok := s.peek()
	if !ok {
		return s.err("EOF while parsing a value")
	}
	s.i++
	if c < '0' || c > '9' {
		return s.err("invalid number")
	}
	exp := int32(c - '0')
	for {
		d := s.peekOrNull()
		if d < '0' || d > '9' {
			break
		}
		s.i++
		digit := int32(d - '0')
		if exp >= math.MaxInt32/10 && (exp > math.MaxInt32/10 || digit > math.MaxInt32%10) {
			if sig != 0 && positiveExp {
				return s.err("number out of range")
			}
			for d := s.peekOrNull(); '0' <= d && d <= '9'; d = s.peekOrNull() {
				s.i++
			}
			return nil
		}
		exp = exp*10 + digit
	}
	final := int64(start)
	if positiveExp {
		final += int64(exp)
	} else {
		final -= int64(exp)
	}
	return s.fromParts(sig, int(max(min(final, math.MaxInt32), math.MinInt32)))
}

// serdePow10 is serde_json's POW10: 1e0 to 1e308, each the nearest f64.
var serdePow10 = func() (t [309]float64) {
	for i := range t {
		t[i], _ = strconv.ParseFloat("1e"+strconv.Itoa(i), 64)
	}
	return t
}()

// fromParts is serde's f64_from_parts without `float_roundtrip`, only for its out-of-range error.
func (s *serdeScan) fromParts(sig uint64, exp int) error {
	f := float64(sig)
	for {
		a := exp
		if a < 0 {
			a = -a
		}
		if a < len(serdePow10) {
			if exp >= 0 {
				if f = f * serdePow10[a]; math.IsInf(f, 0) {
					return s.err("number out of range")
				}
			}
			return nil
		}
		if f == 0 {
			return nil
		}
		if exp >= 0 {
			return s.err("number out of range")
		}
		f /= 1e308
		exp += 308
	}
}

func (s *serdeScan) str() error {
	for {
		for s.i < len(s.b) && s.b[s.i] != '"' && s.b[s.i] != '\\' && s.b[s.i] >= 0x20 {
			s.i++
		}
		if s.i == len(s.b) {
			return s.err("EOF while parsing a string")
		}
		switch s.b[s.i] {
		case '"':
			s.i++
			return nil
		case '\\':
			s.i++
			if err := s.escape(); err != nil {
				return err
			}
		default:
			s.i++
			return s.err("control character (\\u0000-\\u001F) found while parsing a string")
		}
	}
}

func (s *serdeScan) escape() error {
	c, ok := s.peek()
	if !ok {
		return s.err("EOF while parsing a string")
	}
	s.i++
	switch c {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return nil
	case 'u':
		return s.unicodeEscape()
	}
	return s.err("invalid escape")
}

func (s *serdeScan) hex4() (uint16, error) {
	if s.i+4 > len(s.b) {
		s.i = len(s.b)
		return 0, s.err("EOF while parsing a string")
	}
	var v uint16
	ok := true
	for _, c := range s.b[s.i : s.i+4] {
		switch {
		case '0' <= c && c <= '9':
			v = v<<4 | uint16(c-'0')
		case 'a' <= c && c <= 'f':
			v = v<<4 | uint16(c-'a'+10)
		case 'A' <= c && c <= 'F':
			v = v<<4 | uint16(c-'A'+10)
		default:
			ok = false
		}
	}
	s.i += 4
	if !ok {
		return 0, s.err("invalid escape")
	}
	return v, nil
}

func (s *serdeScan) unicodeEscape() error {
	n, err := s.hex4()
	if err != nil {
		return err
	}
	if n >= 0xDC00 && n <= 0xDFFF {
		return s.err("lone leading surrogate in hex escape")
	}
	if n < 0xD800 || n > 0xDBFF {
		return nil
	}
	for _, want := range []byte{'\\', 'u'} {
		c, ok := s.peek()
		if !ok {
			return s.err("EOF while parsing a string")
		}
		s.i++
		if c != want {
			return s.err("unexpected end of hex escape")
		}
	}
	n2, err := s.hex4()
	if err != nil {
		return err
	}
	if n2 < 0xDC00 || n2 > 0xDFFF {
		return s.err("lone leading surrogate in hex escape")
	}
	return nil
}

func (s *serdeScan) seq() error {
	first := true
	for {
		c, ok := s.whitespace()
		if !ok {
			return s.peekErr("EOF while parsing a list")
		}
		switch {
		case c == ']':
			return nil
		case first:
			first = false
		case c == ',':
			s.i++
			c, ok := s.whitespace()
			if !ok {
				return s.peekErr("EOF while parsing a value")
			}
			if c == ']' {
				return s.peekErr("trailing comma")
			}
		default:
			return s.peekErr("expected `,` or `]`")
		}
		if err := s.value(); err != nil {
			return err
		}
	}
}

func (s *serdeScan) endSeq() error {
	c, ok := s.whitespace()
	switch {
	case !ok:
		return s.peekErr("EOF while parsing a list")
	case c == ']':
		s.i++
		return nil
	case c == ',':
		s.i++
		if c, ok := s.whitespace(); ok && c == ']' {
			return s.peekErr("trailing comma")
		}
	}
	return s.peekErr("trailing characters")
}

func (s *serdeScan) mapping() error {
	first := true
	for {
		c, ok := s.whitespace()
		if !ok {
			return s.peekErr("EOF while parsing an object")
		}
		switch {
		case c == '}':
			return nil
		case first:
			first = false
			if c != '"' {
				return s.peekErr("key must be a string")
			}
		case c == ',':
			s.i++
			c, ok := s.whitespace()
			switch {
			case !ok:
				return s.peekErr("EOF while parsing a value")
			case c == '}':
				return s.peekErr("trailing comma")
			case c != '"':
				return s.peekErr("key must be a string")
			}
		default:
			return s.peekErr("expected `,` or `}`")
		}
		s.i++ // the key's opening quote
		if err := s.str(); err != nil {
			return err
		}
		switch c, ok := s.whitespace(); {
		case !ok:
			return s.peekErr("EOF while parsing an object")
		case c != ':':
			return s.peekErr("expected `:`")
		}
		s.i++
		if err := s.value(); err != nil {
			return err
		}
	}
}

func (s *serdeScan) endMap() error {
	c, ok := s.whitespace()
	switch {
	case !ok:
		return s.peekErr("EOF while parsing an object")
	case c == '}':
		s.i++
		return nil
	case c == ',':
		return s.peekErr("trailing comma")
	}
	return s.peekErr("trailing characters")
}
