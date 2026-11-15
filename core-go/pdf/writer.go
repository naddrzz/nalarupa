// Package pdf writes usvg trees as PDF pages the way krilla-svg 0.8 draws them through krilla 0.8: paths,
// gradients, patterns, clips and masks stay vector, text is real text in embedded subset fonts, and
// filters are rasterised with core-go/resvg. The file structure is this package's own (one PDF 1.7 file
// with a classic xref table); what is drawn, and how, follows krilla.
package pdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ref is an indirect object number.
type ref int

// writer lays out the objects of one file and remembers where each starts.
type writer struct {
	buf     bytes.Buffer
	offsets []int // by object number; 0 is the free head, -1 an object not written yet
}

func newWriter() *writer {
	w := &writer{offsets: []int{0}}
	// A binary comment after the header marks the file as binary for transfer tools.
	w.buf.WriteString("%PDF-1.7\n%\x80\x80\x80\x80\n")
	return w
}

// alloc reserves an object number; the object can be written later, in any order.
func (w *writer) alloc() ref {
	w.offsets = append(w.offsets, -1)
	return ref(len(w.offsets) - 1)
}

// obj writes object r with the given body (a dictionary, array or other direct object).
func (w *writer) obj(r ref, body string) {
	w.begin(r)
	w.buf.WriteString(body)
	w.buf.WriteString("\nendobj\n")
}

func (w *writer) begin(r ref) {
	if w.offsets[r] != -1 {
		panic(fmt.Sprintf("pdf: object %d written twice", r))
	}
	w.offsets[r] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n", r)
}

// stream writes object r as a stream: dict holds the entries without << >>; /Length is added, and
// /Filter /FlateDecode when compress deflates the data.
func (w *writer) stream(r ref, dict string, data []byte, compress bool) {
	filter := ""
	if compress {
		data = deflate(data)
		filter = "/Filter /FlateDecode "
	}
	w.rawStream(r, filter+dict, data)
}

// rawStream writes a stream whose data is already encoded as dict says.
func (w *writer) rawStream(r ref, dict string, data []byte) {
	w.begin(r)
	fmt.Fprintf(&w.buf, "<<%s /Length %d>>\nstream\n", dict, len(data))
	w.buf.Write(data)
	w.buf.WriteString("\nendstream\nendobj\n")
}

// finish writes the xref table and trailer. Every allocated object must have been written.
func (w *writer) finish(root ref) []byte {
	start := w.buf.Len()
	fmt.Fprintf(&w.buf, "xref\n0 %d\n", len(w.offsets))
	w.buf.WriteString("0000000000 65535 f\r\n")
	for i, off := range w.offsets[1:] {
		if off < 0 {
			panic(fmt.Sprintf("pdf: object %d allocated but not written", i+1))
		}
		fmt.Fprintf(&w.buf, "%010d 00000 n\r\n", off)
	}
	fmt.Fprintf(&w.buf, "trailer\n<</Size %d /Root %d 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(w.offsets), root, start)
	return w.buf.Bytes()
}

// deflate is zlib (Flate with the zlib header and Adler-32), as /FlateDecode expects.
func deflate(data []byte) []byte {
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	zw.Write(data)
	zw.Close()
	return b.Bytes()
}

// num formats a real as pdf-writer does: an integer when it is one, else the shortest decimal that reads
// back as the same f32 (never an exponent, which PDF does not allow). Non-finite values write 0.
func num(f float32) string {
	if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
		return "0"
	}
	if f >= -2147483648 && f < 2147483648 && float32(int32(f)) == f {
		return strconv.Itoa(int(int32(f)))
	}
	return strconv.FormatFloat(float64(f), 'f', -1, 32)
}

// decimal is num with a decimal point always (PostScript calculator reals).
func decimal(f float32) string {
	s := num(f)
	if !strings.ContainsRune(s, '.') {
		s += ".0"
	}
	return s
}

// nums joins reals with spaces.
func nums(fs ...float32) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = num(f)
	}
	return strings.Join(parts, " ")
}

func (r ref) String() string { return strconv.Itoa(int(r)) + " 0 R" }

// textString is a PDF text string: a literal in PDFDocEncoding when the text is printable ASCII, else
// UTF-16BE with a byte order mark, in hex.
func textString(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			ascii = false
			break
		}
	}
	if ascii {
		r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
		return "(" + r.Replace(s) + ")"
	}
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, u := range utf16Units(s) {
		fmt.Fprintf(&b, "%04X", u)
	}
	b.WriteString(">")
	return b.String()
}

// utf16Units encodes s as UTF-16 code units.
func utf16Units(s string) []uint16 {
	var out []uint16
	for _, c := range s {
		if c >= 0x10000 {
			c -= 0x10000
			out = append(out, uint16(0xD800+(c>>10)), uint16(0xDC00+(c&0x3FF)))
		} else {
			out = append(out, uint16(c))
		}
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }
