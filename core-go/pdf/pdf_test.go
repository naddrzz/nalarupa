package pdf

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/go-text/typesetting/font/cff"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/font/opentype/tables"

	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Parsed is a PDF read back through its xref table: every object's body and decoded stream data, and
// the page boxes in page order.
type Parsed struct {
	Objects map[int]string
	Streams map[int][]byte // decoded (inflated when FlateDecode)
	Pages   [][4]float64   // MediaBoxes
	Root    int
}

var (
	objHead    = regexp.MustCompile(`^(\d+) 0 obj\n`)
	lengthRe   = regexp.MustCompile(`/Length (\d+)`)
	refRe      = regexp.MustCompile(`(\d+) 0 R`)
	mediaBoxRe = regexp.MustCompile(`/MediaBox\[([-0-9. ]+)\]`)
)

// ParseStructure checks a file's structure: the header, startxref, an xref entry for every object whose
// offset starts that object, the trailer's size and root, stream lengths followed by endstream, and that
// every Flate stream inflates.
func ParseStructure(data []byte) (*Parsed, error) {
	if !bytes.HasPrefix(data, []byte("%PDF-1.7\n")) {
		return nil, fmt.Errorf("no PDF 1.7 header")
	}
	i := bytes.LastIndex(data, []byte("startxref\n"))
	if i < 0 || !bytes.HasSuffix(data, []byte("%%EOF\n")) {
		return nil, fmt.Errorf("no startxref or %%%%EOF")
	}
	line := strings.TrimSpace(string(data[i+len("startxref\n") : len(data)-len("%%EOF\n")]))
	xref, err := strconv.Atoi(line)
	if err != nil || xref <= 0 || xref >= len(data) {
		return nil, fmt.Errorf("bad startxref %q", line)
	}
	var count int
	if _, err := fmt.Sscanf(string(data[xref:]), "xref\n0 %d\n", &count); err != nil {
		return nil, fmt.Errorf("bad xref header: %v", err)
	}
	start := xref + len(fmt.Sprintf("xref\n0 %d\n", count))
	p := &Parsed{Objects: map[int]string{}, Streams: map[int][]byte{}}
	for n := 0; n < count; n++ {
		e := start + 20*n
		if e+20 > len(data) {
			return nil, fmt.Errorf("xref truncated at %d", n)
		}
		entry := string(data[e : e+20])
		if n == 0 {
			if entry != "0000000000 65535 f\r\n" {
				return nil, fmt.Errorf("bad free entry %q", entry)
			}
			continue
		}
		var off, gen int
		var kind string
		if _, err := fmt.Sscanf(entry, "%010d %05d %s", &off, &gen, &kind); err != nil || kind != "n" {
			return nil, fmt.Errorf("bad xref entry %d: %q", n, entry)
		}
		if off < 0 || off >= len(data) {
			return nil, fmt.Errorf("object %d: offset %d is outside the file", n, off)
		}
		m := objHead.FindSubmatch(data[off:])
		if m == nil || string(m[1]) != strconv.Itoa(n) {
			return nil, fmt.Errorf("object %d: offset %d does not start it", n, off)
		}
		body := data[off+len(m[0]):]
		end := bytes.Index(body, []byte("\nendobj\n"))
		if end < 0 {
			return nil, fmt.Errorf("object %d has no endobj", n)
		}
		if s := bytes.Index(body[:end], []byte(">>\nstream\n")); s >= 0 {
			dict := string(body[:s+2])
			lm := lengthRe.FindStringSubmatch(dict)
			if lm == nil {
				return nil, fmt.Errorf("stream %d has no /Length", n)
			}
			length, _ := strconv.Atoi(lm[1])
			raw := body[s+len(">>\nstream\n"):]
			if length > len(raw) || !bytes.HasPrefix(raw[length:], []byte("\nendstream\nendobj\n")) {
				return nil, fmt.Errorf("stream %d: /Length %d is not followed by endstream", n, length)
			}
			raw = raw[:length]
			if strings.Contains(dict, "/FlateDecode") {
				r, err := zlib.NewReader(bytes.NewReader(raw))
				if err != nil {
					return nil, fmt.Errorf("stream %d: %v", n, err)
				}
				if raw, err = io.ReadAll(r); err != nil {
					return nil, fmt.Errorf("stream %d does not inflate: %v", n, err)
				}
			}
			p.Streams[n] = raw
			p.Objects[n] = dict
		} else {
			p.Objects[n] = string(body[:end])
		}
	}
	trailer := string(data[start+20*count : i])
	if !strings.HasPrefix(trailer, "trailer\n") || !strings.Contains(trailer, fmt.Sprintf("/Size %d ", count)) {
		return nil, fmt.Errorf("bad trailer %q", trailer)
	}
	rm := regexp.MustCompile(`/Root (\d+) 0 R`).FindStringSubmatch(trailer)
	if rm == nil {
		return nil, fmt.Errorf("trailer has no /Root")
	}
	p.Root, _ = strconv.Atoi(rm[1])
	if len(p.Objects) != count-1 {
		return nil, fmt.Errorf("%d objects, xref says %d", len(p.Objects), count-1)
	}
	// Pages in order: the catalog's page tree (one level, as written).
	pm := regexp.MustCompile(`/Pages (\d+) 0 R`).FindStringSubmatch(p.Objects[p.Root])
	if pm == nil {
		return nil, fmt.Errorf("catalog has no /Pages")
	}
	pagesN, _ := strconv.Atoi(pm[1])
	km := regexp.MustCompile(`/Kids \[([0-9 R]*)\]`).FindStringSubmatch(p.Objects[pagesN])
	if km == nil {
		return nil, fmt.Errorf("page tree has no /Kids")
	}
	for _, k := range refRe.FindAllStringSubmatch(km[1], -1) {
		n, _ := strconv.Atoi(k[1])
		mb := mediaBoxRe.FindStringSubmatch(p.Objects[n])
		if mb == nil {
			return nil, fmt.Errorf("page %d has no /MediaBox", n)
		}
		var box [4]float64
		if _, err := fmt.Sscanf(mb[1], "%g %g %g %g", &box[0], &box[1], &box[2], &box[3]); err != nil {
			return nil, fmt.Errorf("page %d: bad /MediaBox %q", n, mb[1])
		}
		p.Pages = append(p.Pages, box)
	}
	return p, nil
}

func tree(t *testing.T, svg string, db *text.Database) *usvg.Tree {
	t.Helper()
	opt := usvg.DefaultOptions()
	if db != nil {
		opt.FontDB = db
	}
	tr, err := usvg.FromStr(svg, &opt)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func encode(t *testing.T, trees ...*usvg.Tree) *Parsed {
	t.Helper()
	data, err := Encode(trees)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseStructure(data)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *Parsed) count(substr string) int {
	n := 0
	for _, o := range p.Objects {
		if strings.Contains(o, substr) {
			n++
		}
	}
	return n
}

func (p *Parsed) contents() string {
	var b strings.Builder
	for n, s := range p.Streams {
		if !strings.Contains(p.Objects[n], "/Subtype /Image") && !strings.Contains(p.Objects[n], "FontFile") {
			b.Write(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

const shapes = `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100">
<defs>
<linearGradient id="lg"><stop offset="0" stop-color="red"/><stop offset="0.5" stop-color="lime" stop-opacity="0.5"/><stop offset="1" stop-color="blue"/></linearGradient>
<linearGradient id="rep" x2="0.2" spreadMethod="reflect"><stop offset="0" stop-color="red"/><stop offset="1" stop-color="blue"/></linearGradient>
<radialGradient id="rg"><stop offset="0" stop-color="#fff"/><stop offset="1" stop-color="#000"/></radialGradient>
<clipPath id="c"><circle cx="150" cy="50" r="30"/></clipPath>
<mask id="m"><rect width="200" height="100" fill="white" fill-opacity="0.5"/></mask>
<pattern id="p" width="10" height="10" patternUnits="userSpaceOnUse"><rect width="5" height="5" fill="green"/></pattern>
</defs>
<rect width="200" height="100" fill="#eee"/>
<rect x="10" y="10" width="50" height="30" fill="url(#lg)" stroke="black" stroke-width="2" stroke-dasharray="4 2"/>
<rect x="10" y="50" width="50" height="30" fill="url(#rep)"/>
<circle cx="100" cy="50" r="20" fill="url(#rg)" stroke="url(#p)" stroke-width="6" opacity="0.7"/>
<g clip-path="url(#c)" mask="url(#m)" style="mix-blend-mode:multiply"><rect x="100" y="0" width="100" height="100" fill="purple"/></g>
</svg>`

func TestStructure(t *testing.T) {
	p := encode(t, tree(t, shapes, nil))
	if len(p.Pages) != 1 || p.Pages[0] != [4]float64{0, 0, 200, 100} {
		t.Fatalf("pages %v", p.Pages)
	}
	for _, want := range []string{"/ShadingType 2", "/ShadingType 3", "/ShadingType 1", "/FunctionType 4", "/PatternType 1",
		"/S /Luminosity", "/BM /Multiply", "/S /Transparency"} {
		if p.count(want) == 0 {
			t.Errorf("no object with %s", want)
		}
	}
	c := p.contents()
	for _, want := range []string{"\nW n\n", "[4 2] 0 d\n", "\n2 w\n", "/Pattern cs"} {
		if !strings.Contains(c, want) {
			t.Errorf("content has no %q", want)
		}
	}
}

func TestMultiPage(t *testing.T) {
	var trees []*usvg.Tree
	for _, s := range [][2]int{{300, 200}, {50, 80}, {120.0, 40}} {
		trees = append(trees, tree(t, fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d"><rect width="10" height="10"/></svg>`, s[0], s[1]), nil))
	}
	p := encode(t, trees...)
	want := [][4]float64{{0, 0, 300, 200}, {0, 0, 50, 80}, {0, 0, 120, 40}}
	if fmt.Sprint(p.Pages) != fmt.Sprint(want) {
		t.Fatalf("pages %v, want %v", p.Pages, want)
	}
}

func TestEmptyPage(t *testing.T) {
	if _, err := Encode([]*usvg.Tree{{}}); err == nil || err.Error() != "empty page" {
		t.Fatalf("err %v, want empty page", err)
	}
}

func TestDamagedImageLeftOut(t *testing.T) {
	good, err := os.ReadFile(filepath.Join("..", "testdata", "pdf", "ok.png"))
	if err != nil {
		t.Fatal(err)
	}
	img := func(b []byte, x int) string {
		return fmt.Sprintf(`<image x="%d" width="40" height="20" href="data:image/png;base64,%s"/>`, x, base64.StdEncoding.EncodeToString(b))
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100"><rect y="50" width="30" height="30" fill="#123456"/>` +
		img(good, 0) + img(good[:len(good)/2], 50) + `</svg>`
	p := encode(t, tree(t, svg, nil))
	if n := p.count("/Subtype /Image"); n != 1 {
		t.Errorf("%d images, want 1 (the damaged one left out)", n)
	}
	if c := p.contents(); !strings.Contains(c, "0.07058824 0.20392157 0.3372549 rg") {
		t.Errorf("the rectangle is missing:\n%s", c)
	}
}

func TestNum(t *testing.T) {
	for in, want := range map[float32]string{0: "0", 1: "1", -2.5: "-2.5", 0.1: "0.1", 1e-7: "0.0000001", 123456.79: "123456.79"} {
		if got := num(in); got != want {
			t.Errorf("num(%v) = %s, want %s", in, got, want)
		}
	}
	if decimal(2) != "2.0" {
		t.Error("decimal(2)", decimal(2))
	}
}

func TestToUnicode(t *testing.T) {
	cmap := string(toUnicode([]string{"", "A", "", "fi", "😀"}, 1, 2))
	for _, want := range []string{"3 beginbfchar", "<0001> <0041>", "<0003> <00660069>", "<0004> <d83dde00>"} {
		if !strings.Contains(cmap, want) {
			t.Errorf("no %q in\n%s", want, cmap)
		}
	}
	if strings.Contains(cmap, "<0002>") {
		t.Error("a glyph without text has a mapping")
	}
}

func TestTextString(t *testing.T) {
	if got := textString(`a(b)\`); got != `(a\(b\)\\)` {
		t.Error(got)
	}
	if got := textString("é"); got != "<FEFF00E9>" {
		t.Error(got)
	}
}

func TestBuildCFF(t *testing.T) {
	glyphs := []func(text.OutlineBuilder){
		func(b text.OutlineBuilder) {}, // .notdef, empty
		func(b text.OutlineBuilder) {
			b.MoveTo(100, 0)
			b.LineTo(500, 0)
			b.LineTo(500, 700.5)
			b.Close()
			b.MoveTo(0, 0)
			b.CubicTo(10, 20, 30.25, 40, -50, 60)
			b.Close()
		},
	}
	data, err := buildCFF(1000, len(glyphs), func(i int, b text.OutlineBuilder) { glyphs[i](b) })
	if err != nil {
		t.Fatal(err)
	}
	f, err := cff.Parse(data)
	if err != nil {
		t.Fatalf("CFF does not parse: %v", err)
	}
	if len(f.Charstrings) != 2 {
		t.Fatalf("%d charstrings", len(f.Charstrings))
	}
	segs, _, err := f.LoadGlyph(tables.GlyphID(1))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range segs {
		switch s.Op {
		case ot.SegmentOpMoveTo:
			got = append(got, fmt.Sprintf("M%g,%g", s.Args[0].X, s.Args[0].Y))
		case ot.SegmentOpLineTo:
			got = append(got, fmt.Sprintf("L%g,%g", s.Args[0].X, s.Args[0].Y))
		case ot.SegmentOpCubeTo:
			got = append(got, fmt.Sprintf("C%g,%g,%g,%g,%g,%g", s.Args[0].X, s.Args[0].Y, s.Args[1].X, s.Args[1].Y, s.Args[2].X, s.Args[2].Y))
		}
	}
	want := "[M100,0 L500,0 L500,700.5 L100,0 M0,0 C10,20,30.25,40,-50,60 L0,0]"
	if fmt.Sprint(got) != want {
		// go-text closes each contour with a line back to its start.
		t.Errorf("outline %v, want %s", got, want)
	}
}

func TestSimpleGlyph(t *testing.T) {
	// A square with an implied on-curve point between two off-curve points, rounded.
	pen := &contourPen{}
	pen.MoveTo(0, 0)
	pen.LineTo(100.4, 0)
	pen.QuadTo(200, 0, 200, 50)
	pen.QuadTo(200, 100, 150, 100)
	pen.QuadTo(100, 100, 100, 150)
	pen.LineTo(0, 0)
	pen.Close()
	if len(pen.contours) != 1 {
		t.Fatalf("%d contours", len(pen.contours))
	}
	c := pen.contours[0]
	// The closing point is dropped, and (200,50) and (150,100) are implied: each lies midway between
	// its two off-curve neighbours.
	if len(c) != 6 {
		t.Fatalf("%d points: %v", len(c), c)
	}
	data, xMin, n := simpleGlyph(pen.contours)
	if xMin != 0 || n != 6 || int16(data[0])<<8|int16(data[1]) != 1 {
		t.Fatalf("xMin %d, %d points, header %v", xMin, n, data[:10])
	}
	if v := int16(data[6])<<8 | int16(data[7]); v != 200 {
		t.Errorf("xMax %d", v)
	}
	// 100.4 rounds to 100: the second point's x delta is one byte, 100.
	if flags := data[15]; flags&0x02 == 0 || data[14+6] != 100 {
		t.Errorf("flags %08b, x bytes %v", flags, data[14+6:])
	}
	// A midpoint between two off-curve points is implied.
	pen = &contourPen{}
	pen.MoveTo(0, 0)
	pen.QuadTo(10, 0, 15, 5)
	pen.QuadTo(20, 10, 20, 20)
	pen.LineTo(0, 0)
	pen.Close()
	if got := len(pen.contours[0]); got != 4 {
		t.Errorf("%d points, want 4 (the midpoint implied): %v", got, pen.contours[0])
	}
}

func TestAssembleSfnt(t *testing.T) {
	head := make([]byte, 54)
	data := assembleSfnt(0x00010000, map[string][]byte{"head": head, "maxp": {0, 0, 0x50, 0, 0, 3}, "glyf": {1, 2, 3}})
	sf, ok := parseSfnt(data, 0)
	if !ok {
		t.Fatal("does not parse")
	}
	if g, _ := sf.table("glyf"); !bytes.Equal(g, []byte{1, 2, 3}) {
		t.Errorf("glyf %v", g)
	}
	if checksum(data) != 0xB1B0AFBA {
		t.Errorf("checksum adjustment wrong: %08x", checksum(data))
	}
}
