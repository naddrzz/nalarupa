package pdf

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/naddrzz/nalarupa/core-go/text"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// synthFont is a minimal TrueType font of family whose glyph 1 is c: a square outline when glyf is set,
// else no outline table at all (as a bitmap-only font has none).
func synthFont(family string, c rune, glyf bool) []byte {
	be := binary.BigEndian
	head := make([]byte, 54)
	be.PutUint32(head, 0x00010000)
	be.PutUint32(head[12:], 0x5F0F3CF5)
	be.PutUint16(head[18:], 1000)
	be.PutUint16(head[40:], 600)
	be.PutUint16(head[42:], 700)
	be.PutUint16(head[50:], 1) // long loca
	hhea := make([]byte, 36)
	be.PutUint32(hhea, 0x00010000)
	be.PutUint16(hhea[4:], 800)
	be.PutUint16(hhea[6:], uint16(0x10000-200))
	be.PutUint16(hhea[34:], 2)
	hmtx := []byte{2, 88, 0, 0, 2, 88, 0, 0} // two glyphs advancing 600
	// cmap: one format 4 subtable mapping c to glyph 1.
	sub := make([]byte, 32)
	be.PutUint16(sub, 4)
	be.PutUint16(sub[2:], 32)
	be.PutUint16(sub[6:], 4) // segCountX2
	be.PutUint16(sub[8:], 4)
	be.PutUint16(sub[10:], 1)
	be.PutUint16(sub[14:], uint16(c)) // endCode
	be.PutUint16(sub[16:], 0xFFFF)
	be.PutUint16(sub[20:], uint16(c)) // startCode
	be.PutUint16(sub[22:], 0xFFFF)
	be.PutUint16(sub[24:], uint16(1-int(c))) // idDelta
	be.PutUint16(sub[26:], 1)
	cmap := append([]byte{0, 0, 0, 1, 0, 3, 0, 1, 0, 0, 0, 12}, sub...)
	// name: the family (id 1) and a PostScript name (id 6), Windows Unicode.
	utf16be := func(s string) []byte {
		var b []byte
		for _, u := range utf16.Encode([]rune(s)) {
			b = be.AppendUint16(b, u)
		}
		return b
	}
	fam, ps := utf16be(family), utf16be(strings.ReplaceAll(family, " ", ""))
	name := []byte{0, 0, 0, 2, 0, 30}
	for i, rec := range [][2]int{{1, len(fam)}, {6, len(ps)}} {
		off := 0
		if i == 1 {
			off = len(fam)
		}
		for _, v := range []int{3, 1, 0x409, rec[0], rec[1], off} {
			name = be.AppendUint16(name, uint16(v))
		}
	}
	name = append(append(name, fam...), ps...)
	post := make([]byte, 32)
	be.PutUint32(post, 0x00030000)
	tables := map[string][]byte{"head": head, "hhea": hhea, "hmtx": hmtx, "cmap": cmap, "name": name, "post": post}
	if glyf {
		g, _, _ := simpleGlyph([][]contourPoint{{{0, 0, true}, {600, 0, true}, {600, 700, true}, {0, 700, true}}})
		tables["glyf"] = g
		tables["loca"] = []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, byte(len(g))}
		maxp := make([]byte, 32)
		be.PutUint32(maxp, 0x00010000)
		be.PutUint16(maxp[4:], 2)
		tables["maxp"] = maxp
	} else {
		tables["maxp"] = []byte{0, 0, 0x50, 0, 0, 2}
	}
	return assembleSfnt(0x00010000, tables)
}

// TestOutlinelessFaceIsType3: a glyph from a face without outlines (here the fallback for B) goes into
// a Type 3 font that keeps its text, as krilla's Type 3 fonts do.
func TestOutlinelessFaceIsType3(t *testing.T) {
	db := text.NewDatabase()
	db.LoadFontData(synthFont("Outline Test", 'A', true))
	db.LoadFontData(synthFont("Bitmap Test", 'B', false))
	data, err := Encode([]*usvg.Tree{tree(t, `<svg xmlns="http://www.w3.org/2000/svg" width="200" height="100">`+
		`<text x="10" y="50" font-family="Outline Test" font-size="20">AB</text></svg>`, db)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseStructure(data)
	if err != nil {
		t.Fatal(err)
	}
	if p.count("/Subtype /Type3") != 1 || p.count("/Subtype /Type0") != 1 {
		t.Fatalf("want one Type 3 and one Type 0 font, got %d and %d", p.count("/Subtype /Type3"), p.count("/Subtype /Type0"))
	}
	var cmaps string
	for n, s := range p.Streams {
		if strings.Contains(p.Objects[n], "/Type /CMap") {
			cmaps += string(s)
		}
	}
	if !strings.Contains(cmaps, "<00> <0042>") || !strings.Contains(cmaps, "<0001> <0041>") {
		t.Errorf("ToUnicode maps do not hold A and B:\n%s", cmaps)
	}
	if c := p.contents(); !strings.Contains(c, "<00> Tj") || !strings.Contains(c, "<0001> Tj") {
		t.Errorf("content does not show both glyphs:\n%s", c)
	}
	tool, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Log("no pdftotext: extraction not checked")
		return
	}
	file := filepath.Join(t.TempDir(), "t3.pdf")
	if err := os.WriteFile(file, data, 0o666); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(tool, "-enc", "UTF-8", file, "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "AB" {
		t.Errorf("extracted %q, want AB", got)
	}
}
