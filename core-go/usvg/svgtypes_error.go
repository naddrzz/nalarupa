// Port of svgtypes 0.16.1 src/error.rs

package usvg

import (
	"strconv"
	"strings"
)

// stErrorKind lists the svgtypes Error variants.
type stErrorKind uint8

const (
	stErrUnexpectedEndOfStream stErrorKind = iota // input ended earlier than expected
	stErrUnexpectedData                           // unknown data at Pos
	stErrInvalidValue                             // the string is not a valid value
	stErrInvalidIdent                             // an invalid CSS ident
	stErrInvalidChar                              // Chars[0] is the actual byte, the rest are expected; at Pos
	stErrInvalidString                            // Strings[0] is the actual text, the rest are expected; at Pos
	stErrInvalidNumber                            // an invalid number at Pos
)

// stError is svgtypes' Error. Pos is a char position (1-based) for every kind that has one, except where
// a Rust caller passes a byte position (FontShorthand).
type stError struct {
	Kind    stErrorKind
	Pos     int
	Chars   []byte   // stErrInvalidChar
	Strings []string // stErrInvalidString
}

func stErrEOS() *stError           { return &stError{Kind: stErrUnexpectedEndOfStream} }
func stErrData(pos int) *stError   { return &stError{Kind: stErrUnexpectedData, Pos: pos} }
func stErrValue() *stError         { return &stError{Kind: stErrInvalidValue} }
func stErrIdent() *stError         { return &stError{Kind: stErrInvalidIdent} }
func stErrNumber(pos int) *stError { return &stError{Kind: stErrInvalidNumber, Pos: pos} }
func stErrChar(c []byte, pos int) *stError {
	return &stError{Kind: stErrInvalidChar, Chars: c, Pos: pos}
}
func stErrString(s []string, pos int) *stError {
	return &stError{Kind: stErrInvalidString, Strings: s, Pos: pos}
}

// Error is Error's Display text.
func (e *stError) Error() string {
	switch e.Kind {
	case stErrUnexpectedEndOfStream:
		return "unexpected end of stream"
	case stErrUnexpectedData:
		return "unexpected data at position " + strconv.Itoa(e.Pos)
	case stErrInvalidValue:
		return "invalid value"
	case stErrInvalidIdent:
		return "invalid ident"
	case stErrInvalidChar:
		list := make([]string, 0, len(e.Chars))
		for _, c := range e.Chars[1:] {
			list = append(list, string(rune(c)))
		}
		return "expected '" + strings.Join(list, "', '") + "' not '" + string(rune(e.Chars[0])) +
			"' at position " + strconv.Itoa(e.Pos)
	case stErrInvalidString:
		return "expected '" + strings.Join(e.Strings[1:], "', '") + "' not '" + e.Strings[0] +
			"' at position " + strconv.Itoa(e.Pos)
	case stErrInvalidNumber:
		return "invalid number at position " + strconv.Itoa(e.Pos)
	}
	return "unknown error"
}
