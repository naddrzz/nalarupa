package pdf

import (
	"fmt"
	"strings"
)

// toUnicode is the ToUnicode CMap of a font (pdf-writer's UnicodeCmap with krilla's "Custom" name):
// every code from first on with source text maps to that text in UTF-16BE. Codes are size bytes: 2 for
// CID fonts, whose CID 0 (.notdef) has no entry, and 1 for Type 3 fonts. Codes whose glyph belongs to no
// text (a later glyph of a cluster) have no entry.
func toUnicode(texts []string, first, size int) []byte {
	code, space := "%04x", "<0000> <ffff>"
	if size == 1 {
		code, space = "%02x", "<00> <ff>"
	}
	var b strings.Builder
	b.WriteString("%!PS-Adobe-3.0 Resource-CMap\n%%DocumentNeededResources: procset CIDInit\n%%IncludeResource: procset CIDInit\n")
	b.WriteString("%%BeginResource: CMap Custom\n%%Title: (Custom Adobe Identity 0)\n%%Version: 1\n%%EndComments\n")
	b.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n")
	b.WriteString("/CIDSystemInfo 3 dict dup begin\n    /Registry (Adobe) def\n    /Ordering (Identity) def\n    /Supplement 0 def\nend def\n")
	b.WriteString("/CMapName /Custom def\n/CMapVersion 1 def\n/CMapType 0 def\n/WMode 0 def\n")
	b.WriteString("1 begincodespacerange\n" + space + "\nendcodespacerange\n")
	var entries []string
	for cid := first; cid < len(texts); cid++ {
		if texts[cid] == "" {
			continue
		}
		var e strings.Builder
		fmt.Fprintf(&e, "<"+code+"> <", cid)
		for _, u := range utf16Units(texts[cid]) {
			fmt.Fprintf(&e, "%04x", u)
		}
		e.WriteString(">\n")
		entries = append(entries, e.String())
	}
	// At most 100 mappings per block.
	for len(entries) > 0 {
		n := min(len(entries), 100)
		fmt.Fprintf(&b, "%d beginbfchar\n", n)
		for _, e := range entries[:n] {
			b.WriteString(e)
		}
		b.WriteString("endbfchar\n")
		entries = entries[n:]
	}
	b.WriteString("endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n%%EndResource\n%%EOF")
	return []byte(b.String())
}
