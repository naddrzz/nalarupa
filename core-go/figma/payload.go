// Package figma turns Figma's clipboard into .pen nodes (the port of core/src/figma.rs). Figma's
// `text/html` clipboard carries a "fig-kiwi" archive, base64 between `<!--(figma)` and `(/figma)-->`: a
// kiwi schema and a kiwi message, each raw-deflate or zstd compressed. Instances are expanded with their
// overrides and styles, variables are resolved to the values of their modes, and the result is plain
// frames, groups, text, shapes and paths (components do not stay linked).
package figma

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// Decompressed size, nesting and output are capped: the archive comes from the clipboard.
const (
	maxBytes = 256 << 20
	maxDepth = 64
	maxNodes = 100_000
	tooBig   = "bad Figma clipboard data: decompressed size exceeds 256 MB"
)

// Node is one top-level layer: its .pen node and its Figma box.
type Node struct {
	Value      *jsonv.Object
	X, Y, W, H float64
}

// Decode returns the nodes of a Figma copy in paint order, positioned in Figma page coordinates, each
// with its Figma box; ok is false when html is not Figma data.
func Decode(html string) (nodes []Node, ok bool, err error) {
	b64, ok := buffer(html)
	if !ok {
		return nil, false, nil
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(b64)
	if err != nil {
		return nil, false, errors.New("bad Figma clipboard data: " + err.Error())
	}
	msg, err := message(raw)
	if err != nil {
		return nil, false, err
	}
	return newDoc(msg).page(), true, nil
}

// buffer is the base64 archive in Figma's `data-buffer` (the `data-metadata` element is also required).
func buffer(html string) (string, bool) {
	if !strings.Contains(html, "data-metadata") {
		return "", false
	}
	i := strings.Index(html, "(figma)")
	if i < 0 {
		return "", false
	}
	i += len("(figma)")
	j := strings.Index(html[i:], "(/figma)")
	if j < 0 {
		return "", false
	}
	// Rust's is_ascii_whitespace: no vertical tab.
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\f' || r == '\r' {
			return -1
		}
		return r
	}, html[i:i+j]), true
}

// message reads the archive: "fig-kiwi" | "fig-jam.", u32 version, then (u32 length, bytes) chunks:
// schema, message.
func message(b []byte) (*jsonv.Object, error) {
	if len(b) < 12 || (string(b[:8]) != "fig-kiwi" && string(b[:8]) != "fig-jam.") {
		return nil, errors.New("not a Figma archive")
	}
	// Every chunk is checked, but only the first two are kept: a run of empty ones costs no memory.
	var chunks [][]byte
	for i := 12; i+4 < len(b); {
		n := uint64(binary.LittleEndian.Uint32(b[i:]))
		end := uint64(i+4) + n
		if end > uint64(len(b)) {
			return nil, errors.New("truncated Figma archive")
		}
		if len(chunks) < 2 {
			chunks = append(chunks, b[i+4:end])
		}
		i = int(end)
	}
	if len(chunks) < 2 {
		return nil, errors.New("truncated Figma archive")
	}
	sb, err := inflate(chunks[0])
	if err != nil {
		return nil, err
	}
	defs, err := readSchema(sb)
	if err != nil {
		return nil, err
	}
	root := -1
	for i, d := range defs.defs {
		if d.name == "Message" {
			root = i
			break
		}
	}
	if root < 0 {
		return nil, errors.New("Figma schema has no Message")
	}
	data, err := inflate(chunks[1])
	if err != nil {
		return nil, err
	}
	return defs.decode(&reader{d: data}, root, 0)
}

// inflate is raw deflate, or zstd (newer Figma builds) by its magic number.
func inflate(c []byte) ([]byte, error) {
	var r io.Reader
	if bytes.HasPrefix(c, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		// libzstd's default window limit (128 MB), which the Rust zstd crate keeps.
		d, err := zstd.NewReader(bytes.NewReader(c), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(1<<27))
		if err != nil {
			return nil, errors.New("bad Figma clipboard data: " + err.Error())
		}
		defer d.Close()
		r = d
	} else {
		fr := flate.NewReader(bytes.NewReader(c))
		defer fr.Close()
		r = fr
	}
	out, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if len(out) > maxBytes {
		return nil, errors.New(tooBig)
	}
	if err != nil {
		return nil, errors.New("bad Figma clipboard data: " + err.Error())
	}
	return out, nil
}
