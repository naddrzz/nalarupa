package editor

import (
	"encoding/base64"
	"fmt"
)

const b64Pad = '='

var b64Table = func() (t [256]byte) {
	for i := range t {
		t[i] = 0xff
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := range len(alphabet) {
		t[alphabet[i]] = byte(i)
	}
	return t
}()

// b64Encode is base64 0.22's STANDARD.encode.
func b64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// b64Decode is base64 0.22's STANDARD.decode (canonical padding, no trailing bits), failing where Rust fails
// with its DecodeError's Display as the error text.
func b64Decode(s string) ([]byte, error) {
	in := []byte(s)
	rem := len(in) % 4
	if rem == 1 {
		if last := in[len(in)-1]; last != b64Pad && b64Table[last] == 0xff {
			return nil, invalidByte(len(in)-1, last)
		}
	}
	// The complete quads before the last one (or before a partial one): the suffix decoder takes the rest.
	quads := len(in) - rem
	if rem == 0 {
		quads = max(quads-4, 0)
	}
	out := make([]byte, 0, (len(in)/4+1)*3)
	for q := 0; q < quads; q += 4 {
		var acc uint32
		for k := range 4 {
			m := b64Table[in[q+k]]
			if m == 0xff {
				return nil, invalidByte(q+k, in[q+k])
			}
			acc = acc<<6 | uint32(m)
		}
		out = append(out, byte(acc>>16), byte(acc>>8), byte(acc))
	}
	var morsels [4]byte
	n, pads, firstPad := 0, 0, 0
	var last byte
	for i, b := range in[quads:] {
		if b == b64Pad {
			if i < 2 {
				return nil, invalidByte(quads+i, b)
			}
			if pads == 0 {
				firstPad = i
			}
			pads++
			continue
		}
		if pads > 0 {
			return nil, invalidByte(quads+firstPad, b64Pad)
		}
		last = b
		m := b64Table[b]
		if m == 0xff {
			return nil, invalidByte(quads+i, b)
		}
		morsels[n] = m
		n++
	}
	if len(in) > 0 && n < 2 {
		return nil, fmt.Errorf("Invalid input length: %d", quads+n)
	}
	if (pads+n)%4 != 0 {
		return nil, fmt.Errorf("Invalid padding")
	}
	bytes := n * 6 / 8
	acc := uint32(morsels[0])<<26 | uint32(morsels[1])<<20 | uint32(morsels[2])<<14 | uint32(morsels[3])<<8
	if mask := ^uint32(0) >> (bytes * 8); acc&mask != 0 {
		return nil, fmt.Errorf("Invalid last symbol %d, offset %d.", last, quads+n-1)
	}
	for range bytes {
		out = append(out, byte(acc>>24))
		acc <<= 8
	}
	return out, nil
}

func invalidByte(at int, b byte) error { return fmt.Errorf("Invalid symbol %d, offset %d.", b, at) }
